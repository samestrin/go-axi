package goaxi

import (
	"fmt"
	"reflect"
	"testing"
)

// fastInspect and inspect are two implementations of one traversal, and the
// package is only safe while they agree. Three independent reviewers flagged
// exactly this class of hazard — separate walkers whose edge sets must stay
// identical, enforced by nothing but comments — before the fast lane existed.
//
// So the agreement is asserted rather than argued, over both answers the walk
// produces and over every shape either implementation treats differently:
// the fast arms (map[string]any, []any, []string, string, scalars) and the
// fall-through arms (structs, pointers, arrays, typed maps, TextMarshalers).
//
// If this test ever fails, the fast lane is wrong. It does not get "fixed" by
// adjusting the expectation.
func TestFastInspect_AgreesWithReflect(t *testing.T) {
	type holder struct {
		Note   string `toon:"note"`
		hidden string
	}

	cyclicMap := map[string]any{"name": "root"}
	cyclicMap["self"] = cyclicMap

	selfSlice := make([]any, 1)
	selfSlice[0] = selfSlice

	shared := []any{"a", "b"}
	sharedPtr := &holder{Note: "ok"}

	deepNest := any(map[string]any{"note": "bad\x1bhere"})
	for i := 0; i < 60; i++ {
		deepNest = map[string]any{"next": deepNest}
	}

	shapes := []struct {
		name string
		in   any
	}{
		// Fast arms, clean.
		{"clean rows", benchRows(20, false)},
		{"clean string", "hello"},
		{"scalars", map[string]any{"i": 42, "i64": int64(-7), "f": 3.5, "b": true, "u": uint8(3)}},
		{"nil map", map[string]any(nil)},
		{"empty map", map[string]any{}},
		{"nil slice", []any(nil)},
		{"empty slice", []any{}},
		{"clean string slice", []string{"a", "b"}},
		{"unicode text", "naïve façade 🙂"},
		{"genuine replacement char", "a\ufffdb"},
		{"preserved whitespace", "a\nb\tc\rd"},

		// Fast arms, dirty in each position.
		{"dirty rows", benchRows(20, true)},
		{"dirty string", "a\x1bb"},
		{"line separator", "end\u2028sep"},
		{"paragraph separator", "end\u2029sep"},
		{"lone C1 byte", "ab\x9bcd"},
		{"invalid utf8", "ab\xffcd"},
		{"dirty map key", map[string]any{"na\x1bme": 1}},
		{"dirty map value", map[string]any{"k": "a\x1bb"}},
		{"dirty slice element", []any{"ok", "b\x1bc"}},
		{"dirty string slice", []string{"a\x1bb", "ok"}},
		{"dirty deep in rows", map[string]any{"r": []any{[]any{map[string]any{"k": "x\x1b"}}}}},
		{"dirty at 60 levels", deepNest},

		// Identity edges: these are where tracking has historically broken.
		{"shared slice twice", map[string]any{"a": shared, "b": shared}},
		{"sub-slice sharing a backing array", map[string]any{"s": shared[0:1], "o": shared}},
		{"shared pointer twice", map[string]any{"a": sharedPtr, "b": sharedPtr}},
		{"cyclic map", cyclicMap},
		{"self-containing slice", selfSlice},

		// Fall-through arms: everything the fast lane hands to inspect.
		{"struct", holder{Note: "a\x1bb"}},
		{"struct with unexported field", holder{Note: "ok", hidden: "a\x1bb"}},
		{"pointer to struct", &holder{Note: "a\x1bb"}},
		{"nil pointer", (*holder)(nil)},
		{"array", [2]string{"a\x1bb", "ok"}},
		{"typed map", map[string]string{"k": "a\x1bb"}},
		{"pointer as map key", map[*holder]int{{Note: "a\x1bb"}: 1}},
		{"text marshaler", map[string]any{"m": textMarshaler{v: "x"}}},
		{"nil inside interface", map[string]any{"absent": nil, "present": "a\x1bb"}},
	}

	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			var refDirty, fastDirty bool
			refCycle := inspect(reflect.ValueOf(s.in), map[nodeID]bool{}, &refDirty)
			fastCycle := fastInspect(s.in, map[nodeID]bool{}, &fastDirty)

			if refCycle != fastCycle {
				t.Errorf("cycle answer differs: inspect=%v fastInspect=%v", refCycle, fastCycle)
			}
			if refDirty != fastDirty {
				t.Errorf("dirty answer differs: inspect=%v fastInspect=%v", refDirty, fastDirty)
			}
		})
	}
}

// The fast lane must leave the path set exactly as it found it, or a second call
// sharing that set would see phantom cycles. inspect pops on the way out and so
// must this.
func TestFastInspect_LeavesThePathSetEmpty(t *testing.T) {
	shared := []any{"a", "b"}
	v := map[string]any{
		"rows":  benchRows(5, false),
		"a":     shared,
		"b":     shared,
		"inner": map[string]any{"k": "v"},
	}

	path := map[nodeID]bool{}
	var dirty bool
	if fastInspect(v, path, &dirty) {
		t.Fatal("fixture is acyclic and must not report a cycle")
	}
	if len(path) != 0 {
		t.Errorf("path set must be empty after the walk, got %d entries", len(path))
	}
}

// The measured claim, kept runnable: the fast lane allocates nothing per row.
func BenchmarkFastInspect_vs_Reflect(b *testing.B) {
	for _, n := range []int{20, 500} {
		v := benchRows(n, false)
		rv := reflect.ValueOf(v)

		b.Run(fmt.Sprintf("reflect-n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var d bool
				_ = inspect(rv, map[nodeID]bool{}, &d)
			}
		})
		b.Run(fmt.Sprintf("fast-n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var d bool
				_ = fastInspect(v, map[nodeID]bool{}, &d)
			}
		})
	}
}
