package goaxi

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// EncodeChecked exists because Check followed by Encode did the same work twice.
// Check sanitized and marshalled to TOON to reach its verdict, threw the bytes
// away, and Encode then sanitized and marshalled the identical value again — on
// a 2000-row payload, 2.27x the allocations of encoding alone, for one guard.
//
// Measured before and after on the same payload: 6.96ms -> 3.30ms at 2000 rows,
// 325us -> 176us at 100. The guard itself is ~19% over a bare Encode; the rest
// was duplication.
func TestEncodeChecked_EmitsTheSameBytesAsEncode(t *testing.T) {
	v := map[string]any{"rows": []any{
		map[string]any{"id": 1, "name": "a"},
		map[string]any{"id": 2, "name": "b"},
	}}

	var checked, plain bytes.Buffer
	verdict, err := EncodeChecked(&checked, v)
	if err != nil {
		t.Fatalf("EncodeChecked: %v", err)
	}
	if err := Encode(&plain, v); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if checked.String() != plain.String() {
		t.Errorf("EncodeChecked wrote %q, Encode wrote %q", checked.String(), plain.String())
	}
	if !verdict.OK {
		t.Errorf("a lossless value must verdict OK, got reason %q", verdict.Reason)
	}
	if verdict.Tier != TierTabular {
		t.Errorf("tier = %v, want tabular for uniform rows", verdict.Tier)
	}
}

// The verdict must be derived from the bytes actually written, not from a second
// encode of the same value. Tier is the observable proof: it is decided by
// matching the emitted header.
func TestEncodeChecked_TierDescribesTheBytesWritten(t *testing.T) {
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			verdict, err := EncodeChecked(&buf, c.in)
			if err != nil {
				t.Fatalf("EncodeChecked: %v", err)
			}
			if verdict.Tier != c.want {
				t.Errorf("tier = %v, want %v", verdict.Tier, c.want)
			}
			if buf.Len() == 0 {
				t.Error("a lossless value must be written")
			}
		})
	}
}

// The whole point of the guard: a value TOON cannot carry losslessly must be
// refused, and NOTHING may be written. toon-go does not error on such a value —
// it emits empty or near-empty output, so a command prints nothing and exits
// zero. A partial payload is worse than none, because it parses.
func TestEncodeChecked_RefusesALossyValueAndWritesNothing(t *testing.T) {
	v := map[string]any{"rows": []any{textMarshaler{v: "x"}}}

	var buf bytes.Buffer
	verdict, err := EncodeChecked(&buf, v)

	if err == nil {
		t.Fatal("a lossy value must return an error")
	}
	if verdict.OK {
		t.Error("a lossy value must not verdict OK")
	}
	if verdict.Tier != TierLossy {
		t.Errorf("tier = %v, want lossy", verdict.Tier)
	}
	if buf.Len() != 0 {
		t.Errorf("nothing may be written for a lossy value, got %q", buf.String())
	}
	if !strings.Contains(err.Error(), verdict.Reason) {
		t.Errorf("err %q must carry the verdict reason %q", err, verdict.Reason)
	}
}

// A lossy type declared inside an empty container holds no values to inspect, so
// only the type walk can catch it. Both walks have to survive the refactor.
func TestEncodeChecked_CatchesALossyTypeInAnEmptyContainer(t *testing.T) {
	type row struct {
		M textMarshaler `toon:"m"`
	}

	var buf bytes.Buffer
	verdict, err := EncodeChecked(&buf, struct {
		Rows []row `toon:"rows"`
	}{Rows: nil})

	if err == nil {
		t.Fatalf("a lossy type in an empty container must be refused, wrote %q", buf.String())
	}
	if verdict.OK {
		t.Error("verdict must not be OK")
	}
}

// A TextMarshaler reaching an `any` element has a static type of interface{},
// which the type walk cannot see through. Only the value walk catches it. This
// is the bug that shipped once, so it is pinned here too.
func TestEncodeChecked_CatchesALossyValueInsideAnAnySlice(t *testing.T) {
	var buf bytes.Buffer
	verdict, err := EncodeChecked(&buf, map[string]any{
		"rows": []any{map[string]any{"m": textMarshaler{v: "x"}}},
	})

	if err == nil {
		t.Fatalf("a lossy value inside []any must be refused, wrote %q", buf.String())
	}
	if verdict.OK {
		t.Error("verdict must not be OK")
	}
}

// Sanitizing still happens, and exactly once. The written bytes must be clean.
func TestEncodeChecked_SanitizesWhatItWrites(t *testing.T) {
	var buf bytes.Buffer
	if _, err := EncodeChecked(&buf, map[string]any{"name": "plain\x1b[31mred"}); err != nil {
		t.Fatalf("EncodeChecked: %v", err)
	}

	if strings.Contains(buf.String(), "\x1b") {
		t.Errorf("an ANSI escape survived: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "red") {
		t.Errorf("visible text around the stripped byte was lost: %q", buf.String())
	}
}

// The cycle and key-collision guards live in Sanitize, which runs first. A cycle
// must be refused here rather than exhausting the stack in the encoder.
func TestEncodeChecked_RefusesACycleAndAKeyCollision(t *testing.T) {
	n := &cycNode{Name: "a"}
	n.Next = n

	var buf bytes.Buffer
	if _, err := EncodeChecked(&buf, n); err == nil {
		t.Error("a reference cycle must be refused")
	} else {
		var cyc *CycleError
		if !errors.As(err, &cyc) {
			t.Errorf("err = %v, want *CycleError", err)
		}
	}
	if buf.Len() != 0 {
		t.Errorf("nothing may be written for a cycle, got %q", buf.String())
	}

	buf.Reset()
	if _, err := EncodeChecked(&buf, map[string]any{"na\x1bme": 1, "name": 2}); err == nil {
		t.Error("a key collision must be refused")
	} else {
		var dup *KeyCollisionError
		if !errors.As(err, &dup) {
			t.Errorf("err = %v, want *KeyCollisionError", err)
		}
	}
}

// Efficient is NOT measured here, because computing it costs a second marshal
// that no caller of this function reads. Callers that want the cost signal call
// Check. The field must be honestly reported as unmeasured rather than silently
// false, which would read as "TOON is bigger than JSON".
func TestEncodeChecked_ReportsEfficiencyAsUnmeasured(t *testing.T) {
	var buf bytes.Buffer
	verdict, err := EncodeChecked(&buf, map[string]any{"rows": []any{
		map[string]any{"id": 1, "name": "a"},
	}})
	if err != nil {
		t.Fatalf("EncodeChecked: %v", err)
	}

	if verdict.SizeCompared {
		t.Error("EncodeChecked must not pay for the size comparison")
	}
	if verdict.Efficient {
		t.Error("Efficient must stay zero when it was never measured")
	}

	// Check still measures it, so the cost signal remains available.
	if got := Check(map[string]any{"rows": []any{
		map[string]any{"id": 1, "name": "a"},
	}}); !got.SizeCompared {
		t.Error("Check must still measure efficiency")
	}
}

// An empty input legitimately encodes to nothing, and that is not a fault. The
// empty-output guard must not cry wolf on it.
func TestEncodeChecked_EmptyInputIsNotAFault(t *testing.T) {
	var buf bytes.Buffer
	verdict, err := EncodeChecked(&buf, map[string]any{})
	if err != nil {
		t.Fatalf("an empty value is not a fault, got %v", err)
	}
	if !verdict.OK {
		t.Errorf("an empty value must verdict OK, got reason %q", verdict.Reason)
	}
}

// A write failure must surface, not be swallowed behind the verdict.
func TestEncodeChecked_PropagatesAWriteError(t *testing.T) {
	want := fmt.Errorf("sink closed")

	_, err := EncodeChecked(failingWriter{err: want}, map[string]any{"a": 1})
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want it to wrap %v", err, want)
	}
}
