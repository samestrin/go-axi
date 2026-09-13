package goaxi

import (
	"fmt"
	"reflect"
	"testing"
	"time"
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
		{"empty string slice", []string{}},
		{"nil string slice", []string(nil)},
		{"shared string slice twice", func() any {
			s := []string{"a", "b"}
			return map[string]any{"x": s, "y": s}
		}()},
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

// Values that CROSS between the two lanes mid-walk.
//
// Every shape in the table above stays in one lane or the other. This covers the
// case neither reaches: a container entered through a fast arm, which keys its
// identity from reflect.ValueOf(x).Pointer(), and the same container reached
// again through the reflect fall-through, which keys from v.Pointer(). If those
// two ever disagree, a DAG reads as a cycle or — far worse — a real cycle goes
// undetected and sanitizeValue walks it into a fatal stack overflow.
//
// A struct is the natural bridge, since structs always fall through and
// map[string]any always takes the fast arm.
func TestFastInspect_AgreesAcrossLaneBoundaries(t *testing.T) {
	type bridge struct {
		M map[string]any `toon:"m"`
		S []any          `toon:"s"`
	}

	// A cycle that closes through a struct: map -> struct -> same map.
	mapThroughStruct := map[string]any{"name": "root"}
	mapThroughStruct["b"] = bridge{M: mapThroughStruct}

	// A cycle that closes through a struct from a slice: slice -> struct -> same slice.
	sliceThroughStruct := make([]any, 1)
	sliceThroughStruct[0] = bridge{S: sliceThroughStruct}

	// A DAG, not a cycle: one map reached directly AND through a struct field.
	// This must NOT be reported as a cycle.
	sharedMap := map[string]any{"k": "v"}
	dag := map[string]any{
		"direct":    sharedMap,
		"viaStruct": bridge{M: sharedMap},
	}

	// The same, for a slice.
	sharedSlice := []any{"a", "b"}
	sliceDag := map[string]any{
		"direct":    sharedSlice,
		"viaStruct": bridge{S: sharedSlice},
	}

	// A dirty string reachable only by crossing into the struct lane and back.
	dirtyAcross := map[string]any{
		"b": bridge{M: map[string]any{"note": "bad\x1bhere"}},
	}

	shapes := []struct {
		name string
		in   any
	}{
		{"cycle closing through a struct", mapThroughStruct},
		{"slice cycle closing through a struct", sliceThroughStruct},
		{"map reached directly and via a struct", dag},
		{"slice reached directly and via a struct", sliceDag},
		{"dirty string across a lane boundary", dirtyAcross},
		{"struct wrapping clean containers", bridge{M: map[string]any{"k": "v"}, S: []any{"a"}}},
	}

	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			var refDirty, fastDirty bool
			refCycle := inspect(reflect.ValueOf(s.in), map[nodeID]bool{}, &refDirty)
			fastCycle := fastInspect(s.in, map[nodeID]bool{}, &fastDirty)

			if refCycle != fastCycle {
				t.Errorf("cycle answer differs across lanes: inspect=%v fastInspect=%v",
					refCycle, fastCycle)
			}
			if refDirty != fastDirty {
				t.Errorf("dirty answer differs across lanes: inspect=%v fastInspect=%v",
					refDirty, fastDirty)
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

// The same agreement requirement as the walk above, for the lossy pair.
//
// This one carries an extra trap: a WRONG answer here is silent data loss, not a
// crash. lossyInValue exists because toon-go drops a TextMarshaler's value while
// still emitting its key, so if the fast lane ever answers "no loss" for a
// payload that has one, the command prints a field with its contents missing and
// exits zero. Every shape below that should be lossy is included precisely
// because a false negative is invisible.
//
// Defined types matter here more than anywhere else. A Go type switch matches
// only exact unnamed types, so `type Kind string` and a named map type do NOT
// hit the fast arms and fall through to reflect — which is what makes skipping
// the builtin arms sound.
func TestFastLossyInValue_AgreesWithReflect(t *testing.T) {
	type inner struct {
		M textMarshaler `toon:"m"`
	}
	type kinded struct {
		K definedString `toon:"k"`
	}

	shared := []any{"a", "b"}
	lossyShared := []any{textMarshaler{v: "SECRET"}}

	deep := any(map[string]any{"m": textMarshaler{v: "SECRET"}})
	for i := 0; i < 60; i++ {
		deep = map[string]any{"next": deep}
	}

	shapes := []struct {
		name string
		in   any
	}{
		// Not lossy: the fast arms must return "" for all of these.
		{"clean rows", benchRows(20, false)},
		{"dirty rows", benchRows(20, true)},
		{"plain string", "hello"},
		{"scalars", map[string]any{"i": 42, "f": 3.5, "b": true, "u": uint8(3)}},
		{"nil map", map[string]any(nil)},
		{"empty map", map[string]any{}},
		{"nil slice", []any(nil)},
		{"empty slice", []any{}},
		{"string slice", []string{"a", "b"}},
		{"time.Time is special-cased", map[string]any{"at": time.Now()}},
		{"shared clean slice twice", map[string]any{"a": shared, "b": shared}},

		// Lossy: a false negative in any of these is silent data loss.
		{"marshaler at top level", textMarshaler{v: "x"}},
		{"marshaler in a map value", map[string]any{"m": textMarshaler{v: "x"}}},
		{"marshaler in an any slice", map[string]any{"r": []any{textMarshaler{v: "x"}}}},
		{"marshaler in a struct", inner{M: textMarshaler{v: "x"}}},
		{"marshaler behind a pointer", &textMarshaler{v: "x"}},
		{"marshaler in a shared node", map[string]any{"a": lossyShared, "b": lossyShared}},
		{"marshaler at 60 levels", deep},
		{"marshaler beside a timestamp", map[string]any{"at": time.Now(), "m": textMarshaler{v: "x"}}},

		// Defined types must fall through to reflect, not hit the fast arms.
		{"defined string type", kinded{K: "resolved"}},
		{"defined string in a map", map[string]any{"k": definedString("x")}},
		{"typed map", map[string]string{"k": "v"}},
		{"array", [2]string{"a", "b"}},
	}

	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			ref := lossyInValue(reflect.ValueOf(s.in), map[nodeID]bool{})
			fast := fastLossyInValue(s.in, map[nodeID]bool{})

			if (ref != "") != (fast != "") {
				t.Errorf("lossy answer differs: lossyInValue=%q fastLossyInValue=%q", ref, fast)
			}
		})
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
