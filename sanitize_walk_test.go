package goaxi

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// The tests in this file cover the value walk rather than the cleaning rules.
// sanitize_test.go proves the right characters are removed; these prove the walk
// reaches every string without corrupting the shape, the types, or the nil-ness
// of what it copies.

func TestSanitize_Nil(t *testing.T) {
	if got := MustSanitize(nil); got != nil {
		t.Errorf("MustSanitize(nil) must be nil, got %#v", got)
	}
}

func TestSanitizeString(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"clean passes through", "hello", "hello"},
		{"ansi stripped", "a\x1bb", "ab"},
		{"separator stripped", "end\u2028sep", "endsep"},
		{"invalid utf8 dropped", "ab\xffcd", "abcd"},
		{"whitespace preserved", "a\nb\tc\rd", "a\nb\tc\rd"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SanitizeString(c.in); got != c.want {
				t.Errorf("SanitizeString(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// A real U+FFFD the caller supplied must survive. cleanString scans manually
// rather than with a range loop precisely so it can tell a genuine replacement
// character apart from an invalid byte that decodes to one.
func TestSanitize_KeepsGenuineReplacementChar(t *testing.T) {
	const in = "a\ufffdb"
	if got := SanitizeString(in); got != in {
		t.Errorf("a genuine U+FFFD must be preserved: got %q, want %q", got, in)
	}
	if got := SanitizeString("a\xffb"); got != "ab" {
		t.Errorf("an invalid byte must be dropped, not replaced: got %q, want %q", got, "ab")
	}
}

// Map KEYS are sanitized, not just values. A key becomes a field name in tabular
// output, so a control byte there lands in the header rather than in a cell.
func TestSanitize_SanitizesMapKeys(t *testing.T) {
	in := map[string]any{"na\x1bme": "value"}
	got, ok := MustSanitize(in).(map[string]any)
	if !ok {
		t.Fatalf("map must stay a map[string]any, got %T", MustSanitize(in))
	}
	if _, bad := got["na\x1bme"]; bad {
		t.Error("the unsanitized key must not survive")
	}
	if v, good := got["name"]; !good || v != "value" {
		t.Errorf("key must be cleaned to %q with its value intact, got %#v", "name", got)
	}
}

func TestSanitize_Pointer(t *testing.T) {
	s := "a\x1bb"
	got, ok := MustSanitize(&s).(*string)
	if !ok {
		t.Fatalf("pointer type must be preserved, got %T", MustSanitize(&s))
	}
	if *got != "ab" {
		t.Errorf("pointed-to string must be cleaned, got %q", *got)
	}
	if s != "a\x1bb" {
		t.Errorf("the caller's value must not be mutated, got %q", s)
	}
}

func TestSanitize_Array(t *testing.T) {
	in := [2]string{"a\x1bb", "ok"}
	got, ok := MustSanitize(in).([2]string)
	if !ok {
		t.Fatalf("array type must be preserved, got %T", MustSanitize(in))
	}
	if got[0] != "ab" || got[1] != "ok" {
		t.Errorf("array elements must be cleaned in place, got %#v", got)
	}
}

// Nil containers must stay nil rather than becoming empty ones. toon-go encodes
// a nil slice and an empty slice differently, so quietly converting would change
// output for every caller with an absent list.
func TestSanitize_NilContainersStayNil(t *testing.T) {
	var nilSlice []string
	var nilMap map[string]string
	var nilPtr *string

	if got := MustSanitize(nilSlice); !reflect.ValueOf(got).IsNil() {
		t.Errorf("nil slice must stay nil, got %#v", got)
	}
	if got := MustSanitize(nilMap); !reflect.ValueOf(got).IsNil() {
		t.Errorf("nil map must stay nil, got %#v", got)
	}
	if got := MustSanitize(nilPtr); !reflect.ValueOf(got).IsNil() {
		t.Errorf("nil pointer must stay nil, got %#v", got)
	}
}

func TestSanitize_NilInsideInterface(t *testing.T) {
	in := map[string]any{"absent": nil, "present": "a\x1bb"}
	got, ok := MustSanitize(in).(map[string]any)
	if !ok {
		t.Fatalf("map must stay a map, got %T", MustSanitize(in))
	}
	if got["absent"] != nil {
		t.Errorf("a nil interface value must stay nil, got %#v", got["absent"])
	}
	if got["present"] != "ab" {
		t.Errorf("sibling values must still be cleaned, got %#v", got["present"])
	}
}

// Scalars have nothing to clean and must survive with their types intact. A walk
// that boxed every number into a float64 would silently change integer output.
func TestSanitize_ScalarsPassThroughUnchanged(t *testing.T) {
	cases := []any{
		42, int64(-7), uint8(3), 3.14, float32(1.5), true, false,
	}
	for _, in := range cases {
		got := MustSanitize(in)
		if got != in {
			t.Errorf("scalar must pass through unchanged: MustSanitize(%#v) = %#v", in, got)
		}
		if reflect.TypeOf(got) != reflect.TypeOf(in) {
			t.Errorf("scalar type must be preserved: %T became %T", in, got)
		}
	}
}

// Strings inside unexported fields cannot be reached by reflection. The
// documented behavior is that they are carried through as-is rather than
// silently dropping the whole struct, and time.Time is the case that proves it
// matters: it is entirely unexported state and must survive intact.
func TestSanitize_StructWithUnexportedFields(t *testing.T) {
	type holder struct {
		Exported string `toon:"exported"`
		hidden   string
	}
	in := holder{Exported: "a\x1bb", hidden: "untouched"}
	got, ok := MustSanitize(in).(holder)
	if !ok {
		t.Fatalf("struct type must be preserved, got %T", MustSanitize(in))
	}
	if got.Exported != "ab" {
		t.Errorf("exported field must be cleaned, got %q", got.Exported)
	}
	if got.hidden != "untouched" {
		t.Errorf("unexported field must be carried through, got %q", got.hidden)
	}

	when := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	gotTime, ok := MustSanitize(when).(time.Time)
	if !ok {
		t.Fatalf("time.Time must be preserved, got %T", MustSanitize(when))
	}
	if !gotTime.Equal(when) {
		t.Errorf("time.Time must survive intact, got %v want %v", gotTime, when)
	}
}

// The caller's value must never be mutated. A sanitizer with a side effect on
// its argument is a trap: a caller writing the same value as JSON afterwards
// would silently get the stripped version.
func TestSanitize_DoesNotMutateInput(t *testing.T) {
	in := map[string]any{
		"rows": []any{map[string]any{"note": "bad\x1bhere"}},
	}
	_ = MustSanitize(in)

	rows := in["rows"].([]any)
	note := rows[0].(map[string]any)["note"].(string)
	if note != "bad\x1bhere" {
		t.Errorf("input must not be mutated, got %q", note)
	}
	if !strings.Contains(note, "\x1b") {
		t.Error("the original ANSI byte must still be present in the caller's value")
	}
}

func TestSanitize_DeeplyNestedPointers(t *testing.T) {
	type inner struct {
		Note string `toon:"note"`
	}
	type outer struct {
		In *inner `toon:"in"`
	}
	in := outer{In: &inner{Note: "a\x1bb"}}
	got, ok := MustSanitize(in).(outer)
	if !ok {
		t.Fatalf("struct type must be preserved, got %T", MustSanitize(in))
	}
	if got.In == nil {
		t.Fatal("nested pointer must not be nilled out")
	}
	if got.In.Note != "ab" {
		t.Errorf("string behind a nested pointer must be cleaned, got %q", got.In.Note)
	}
	if in.In.Note != "a\x1bb" {
		t.Errorf("the caller's nested value must not be mutated, got %q", in.In.Note)
	}
}

// A value with nothing to clean is returned as-is rather than rebuilt, so the
// result SHARES memory with the input.
//
// This is the one contract that changed when the copy became conditional.
// Non-mutation is unaffected and is pinned by TestSanitize_DoesNotMutateInput
// above; what is no longer promised is that the result occupies different
// memory. Asserted by identity rather than inferred from an allocation count,
// because identity is the property a caller can actually observe — and four
// downstream consumers could be relying on the old behaviour.
//
// Aliasing was already true before this change for nil containers, scalars,
// funcs, channels and every unexported struct field. What changed is that it now
// also holds for a populated map, slice or struct that needed no cleaning.
func TestSanitize_CleanValueIsReturnedNotCopied(t *testing.T) {
	in := map[string]any{"rows": []any{map[string]any{"name": "ok"}}}

	got, ok := MustSanitize(in).(map[string]any)
	if !ok {
		t.Fatalf("map must stay a map[string]any, got %T", MustSanitize(in))
	}
	if reflect.ValueOf(got).Pointer() != reflect.ValueOf(in).Pointer() {
		t.Error("a clean map must be returned as-is, not rebuilt into an identical copy")
	}

	// The other direction: a value that does need cleaning must still be
	// rebuilt, and the caller's copy left alone.
	dirty := map[string]any{"name": "a\x1bb"}
	cleaned, ok := MustSanitize(dirty).(map[string]any)
	if !ok {
		t.Fatalf("map must stay a map[string]any, got %T", MustSanitize(dirty))
	}
	if reflect.ValueOf(cleaned).Pointer() == reflect.ValueOf(dirty).Pointer() {
		t.Error("a dirty map must be rebuilt, not returned as-is")
	}
	if cleaned["name"] != "ab" {
		t.Errorf("the dirty value must be cleaned, got %q", cleaned["name"])
	}
	if dirty["name"] != "a\x1bb" {
		t.Errorf("the caller's value must not be mutated, got %q", dirty["name"])
	}
}
