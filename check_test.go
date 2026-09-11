package goaxi

import (
	"strings"
	"testing"
	"time"
)

// Check exists because toon-go fails in ways neither an error nor a length test
// catches. Every case below is grounded in a probe of the pinned version rather
// than assumption:
//
//   - A defined string type (type Kind string) returns a real error. Loud, and
//     already handled by any caller checking err.
//   - A TextMarshaler at TOP LEVEL returns err==nil with EMPTY output. The
//     command prints nothing and exits zero.
//   - A TextMarshaler NESTED in a struct or map encodes to "m:" — the key is
//     present and the value is gone, with err==nil and len(b)>0. cadence's
//     AssertEncodable passes this while the data is lost. It is the reason
//     Check cannot simply reuse that guard.

type textMarshaler struct{ v string }

func (m textMarshaler) MarshalText() ([]byte, error) { return []byte(m.v), nil }

type definedString string

// --- Lossy detection -------------------------------------------------------

func TestCheck_FlagsNestedTextMarshalerAsLossy(t *testing.T) {
	type row struct {
		M textMarshaler `toon:"m"`
	}
	got := Check(row{M: textMarshaler{v: "PAYLOAD"}})

	if got.OK {
		t.Error("a nested TextMarshaler silently drops its value; Check must not report OK")
	}
	if got.Tier != TierLossy {
		t.Errorf("tier must be TierLossy, got %v", got.Tier)
	}
	if !strings.Contains(got.Reason, "TextMarshaler") {
		t.Errorf("reason must name the offending construct, got %q", got.Reason)
	}
}

func TestCheck_FlagsTopLevelTextMarshalerAsLossy(t *testing.T) {
	got := Check(textMarshaler{v: "PAYLOAD"})

	if got.OK {
		t.Error("a top-level TextMarshaler encodes to empty output; Check must not report OK")
	}
	if got.Tier != TierLossy {
		t.Errorf("tier must be TierLossy, got %v", got.Tier)
	}
}

// A TextMarshaler hidden deep in the value is just as lossy as one at the top.
// A detector that only inspects the outermost type would miss every realistic
// payload, where the offending field sits inside a row.
func TestCheck_FindsTextMarshalerAtAnyDepth(t *testing.T) {
	cases := []struct {
		name string
		in   any
	}{
		{"in a slice element", []any{textMarshaler{v: "x"}}},
		{"in a map value", map[string]any{"k": textMarshaler{v: "x"}}},
		{"behind a pointer", &textMarshaler{v: "x"}},
		{"in a nested struct", struct {
			Inner struct {
				M textMarshaler `toon:"m"`
			} `toon:"inner"`
		}{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Check(c.in); got.OK {
				t.Errorf("must detect a TextMarshaler nested %s, got OK with reason %q", c.name, got.Reason)
			}
		})
	}
}

// A defined string type fails loudly rather than silently, but it still cannot
// be encoded, so Check must report it as unusable with a reason that names the
// documented workaround.
func TestCheck_FlagsDefinedStringType(t *testing.T) {
	type row struct {
		K definedString `toon:"k"`
	}
	got := Check(row{K: "resolved"})

	if got.OK {
		t.Error("a defined string type cannot be encoded; Check must not report OK")
	}
	if !strings.Contains(got.Reason, "alias") {
		t.Errorf("reason should point at the type-alias workaround, got %q", got.Reason)
	}
}

// The alias workaround cadence documents must actually pass, or Check would
// reject the very fix it recommends.
func TestCheck_AcceptsTypeAliasWorkaround(t *testing.T) {
	type aliasString = string
	type row struct {
		K aliasString `toon:"k"`
	}
	if got := Check(row{K: "resolved"}); !got.OK {
		t.Errorf("a type ALIAS encodes correctly and must pass, got reason %q", got.Reason)
	}
}

// time.Time implements TextMarshaler but toon-go special-cases it, encoding
// `at: "2026-09-11T12:00:00Z"` rather than dropping the value. A detector that
// flags every TextMarshaler would reject any payload carrying a timestamp —
// which an earlier version of Check did, in a struct field but not in a map,
// so it was inconsistent as well as wrong.
func TestCheck_TimeTimeIsNotLossy(t *testing.T) {
	when := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

	if got := Check(map[string]any{"at": when}); !got.OK {
		t.Errorf("time.Time in a map must be OK, got reason %q", got.Reason)
	}
	if got := Check(struct {
		At time.Time `toon:"at"`
	}{At: when}); !got.OK {
		t.Errorf("time.Time in a struct field must be OK, got reason %q", got.Reason)
	}
	if got := Check([]any{when}); !got.OK {
		t.Errorf("time.Time in a slice must be OK, got reason %q", got.Reason)
	}
}

// A lossy type declared inside an EMPTY container has no value to inspect, so
// only the type walk can find it. Without that walk a command would pass its
// guard whenever its test fixture happened to be empty, then lose data in
// production once the list was populated.
func TestCheck_FindsLossyTypeInEmptyContainer(t *testing.T) {
	cases := []struct {
		name string
		in   any
	}{
		{"empty typed slice", []textMarshaler{}},
		{"nil typed slice", []textMarshaler(nil)},
		{"empty typed map", map[string]textMarshaler{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Check(c.in); got.OK {
				t.Errorf("a lossy element type must be detected even when the container is empty, got OK")
			}
		})
	}
}

// --- Empty output ----------------------------------------------------------

// An empty map has nothing to lose, so empty output is the correct answer and
// must not be reported as a failure. Treating every zero-length payload as an
// error would make Check cry wolf on legitimately empty results — exactly the
// "definitive empty state" AXI asks for.
func TestCheck_EmptyInputIsNotLossy(t *testing.T) {
	cases := []struct {
		name string
		in   any
	}{
		{"empty map", map[string]any{}},
		{"empty struct", struct{}{}},
		{"empty slice", []string{}},
		{"nil", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Check(c.in); !got.OK {
				t.Errorf("an empty input has nothing to lose and must be OK, got reason %q", got.Reason)
			}
		})
	}
}

// A struct whose only field is omitempty-and-empty encodes to zero bytes, and
// that is correct rather than a fault. An earlier version counted struct fields
// to decide whether the input had content, saw one field, and reported a
// perfectly good value as lossy.
func TestCheck_OmitEmptyFieldsAreNotLossy(t *testing.T) {
	type onlyOmitEmpty struct {
		A string `toon:"a,omitempty" json:"a,omitempty"`
	}
	if got := Check(onlyOmitEmpty{A: ""}); !got.OK {
		t.Errorf("an omitempty field that is empty has nothing to lose, got reason %q", got.Reason)
	}

	type mixed struct {
		A string `toon:"a,omitempty" json:"a,omitempty"`
		B string `toon:"b" json:"b"`
	}
	if got := Check(mixed{A: "", B: "kept"}); !got.OK {
		t.Errorf("a partially omitted struct must still be OK, got reason %q", got.Reason)
	}
}

// A reference cycle exhausts the stack inside toon.Marshal, and stack
// exhaustion is a FATAL runtime error in Go — recover() cannot catch it, so the
// process dies with a stack dump and no diagnostic. Verified by an isolated
// probe before this guard existed. encoding/json reports a clean error for the
// same input.
//
// If this test ever crashes the suite rather than failing, the guard has
// regressed and Check is calling the encoder on a cyclic value again.
func TestCheck_ReferenceCycleIsRefusedNotFatal(t *testing.T) {
	selfMap := map[string]any{"name": "root"}
	selfMap["self"] = selfMap

	got := Check(selfMap)
	if got.OK {
		t.Error("a cyclic value must not be reported OK; encoding it would kill the process")
	}
	if !strings.Contains(got.Reason, "cycle") {
		t.Errorf("reason must name the cycle, got %q", got.Reason)
	}

	type node struct {
		Name string `toon:"name"`
		Next *node  `toon:"next"`
	}
	n := &node{Name: "a"}
	n.Next = n
	if Check(n).OK {
		t.Error("a self-referencing pointer must not be reported OK")
	}
}

// A node reachable by two different paths is a DAG, not a cycle, and must not be
// refused. A detector that never unmarks a visited node would reject it.
func TestCheck_SharedNodeIsNotACycle(t *testing.T) {
	shared := map[string]any{"k": "v"}
	in := map[string]any{"first": shared, "second": shared}

	if got := Check(in); !got.OK {
		t.Errorf("a value shared by two keys is a DAG, not a cycle, got reason %q", got.Reason)
	}

	list := []any{shared, shared, shared}
	if got := Check(list); !got.OK {
		t.Errorf("a node repeated in a slice is not a cycle, got reason %q", got.Reason)
	}
}

// --- Tier classification ---------------------------------------------------

// Tiers are defined by MEASURED behavior, not by guesswork. toon-go encodes
// ragged and nested shapes losslessly using a list form, so "cannot encode" does
// not separate anything. The tiers describe the payload shape actually emitted.
func TestCheck_Tiers(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want Tier
	}{
		{
			"uniform rows encode as a tabular array",
			map[string]any{"rows": []any{
				map[string]any{"id": 1, "name": "a"},
				map[string]any{"id": 2, "name": "b"},
			}},
			TierTabular,
		},
		{
			"ragged rows fall back to a list, losslessly",
			map[string]any{"rows": []any{
				map[string]any{"id": 1, "name": "a"},
				map[string]any{"id": 2, "extra": "b"},
			}},
			TierNested,
		},
		{
			"a list-valued field is still lossless",
			map[string]any{"rows": []any{
				map[string]any{"id": 1, "tags": []any{"x", "y"}},
			}},
			TierNested,
		},
		{
			"a lossy construct outranks any shape",
			map[string]any{"rows": []any{textMarshaler{v: "x"}}},
			TierLossy,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Check(c.in); got.Tier != c.want {
				t.Errorf("tier = %v, want %v (reason %q)", got.Tier, c.want, got.Reason)
			}
		})
	}
}

// --- Efficiency ------------------------------------------------------------

// Efficiency is the cost axis and is ORTHOGONAL to correctness. A payload can be
// perfectly lossless and still cost more than JSON, which is the case cadence's
// AssertEfficient exists to catch. Sizes are measured, not assumed.
func TestCheck_Efficiency(t *testing.T) {
	rows := make([]any, 0, 20)
	for i := 0; i < 20; i++ {
		rows = append(rows, map[string]any{"id": i, "name": "row", "state": "open"})
	}

	if got := Check(map[string]any{"rows": rows}); !got.Efficient {
		t.Errorf("uniform rows are TOON's best case and must be efficient, got reason %q", got.Reason)
	}

	ragged := map[string]any{"rows": []any{
		map[string]any{"id": 1, "name": "a"},
		map[string]any{"id": 2, "extra": "b", "more": "c"},
	}}
	got := Check(ragged)
	if got.Efficient {
		t.Error("a ragged shape costs more as TOON than JSON and must not be reported efficient")
	}
	// Inefficient is a cost signal, never a correctness one.
	if !got.OK {
		t.Errorf("an inefficient shape is still lossless and must remain OK, got reason %q", got.Reason)
	}
}

// --- CanEncode -------------------------------------------------------------

func TestCanEncode(t *testing.T) {
	if !CanEncode(map[string]any{"f": "x"}) {
		t.Error("a plain map must be encodable")
	}
	if CanEncode(map[string]any{"f": textMarshaler{v: "x"}}) {
		t.Error("a value containing a TextMarshaler must not be reported encodable")
	}
}

// Tier must render as something a human can read in a failure message. A bare
// integer in an error tells an operator nothing.
func TestTier_String(t *testing.T) {
	for _, tr := range []Tier{TierTabular, TierNested, TierLossy} {
		if s := tr.String(); s == "" || strings.HasPrefix(s, "Tier(") {
			t.Errorf("Tier(%d) must have a readable name, got %q", tr, s)
		}
	}
}
