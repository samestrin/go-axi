package goaxi

import "reflect"

// fastInspect answers the same two questions as inspect — does this value refer
// to itself, and does anything in it need cleaning — without boxing a
// reflect.Value for every map entry.
//
// WHY IT EXISTS. Profiling the output path put reflect.unsafe_New at 52% of
// every allocation in the process, called 100% from reflect.copyVal, with
// MapIter.Key and MapIter.Value summing to almost exactly the same share. The
// reflect walk pays that twice per map entry, once for the key and once for the
// value, and none of it is necessary for the shapes a TOON payload is actually
// built from. This boxes once per CONTAINER, for identity tracking, and not at
// all for string keys: a clean 500-row listing went from 3,002 allocations to 1.
//
// WHAT IT DOES NOT HANDLE it hands to inspect unchanged. Structs, pointers,
// arrays, typed maps, TextMarshalers and anything else exotic keep exactly their
// existing behaviour, so this is a fast lane rather than a replacement.
//
// THE HAZARD, stated because it is real and was raised by three independent
// reviewers before this existed: the package now contains two implementations of
// one traversal, and they must agree forever. The edge set here has to match
// inspect's exactly — the same reference-bearing kinds tracked by identity, the
// same pop-on-exit so a DAG is not mistaken for a cycle, map KEYS walked as well
// as values, and no short-circuit once dirtiness is known, because the walk must
// still complete to be sure about cycles. That agreement is not left to
// inspection: TestFastInspect_AgreesWithReflect asserts it over every shape,
// and is the reason this is safe to keep.
func fastInspect(v any, path map[nodeID]bool, dirty *bool) (cycle bool) {
	switch x := v.(type) {
	case nil:
		return false

	case string:
		// Skipped once the answer is known, exactly as inspect does — see its
		// note on why the flag is threaded by pointer rather than returned.
		if !*dirty && cleanString(x) != x {
			*dirty = true
		}
		return false

	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, bool:
		// Nothing to clean, nothing to loop, no identity to track.
		return false

	case map[string]any:
		if x == nil {
			return false
		}
		// One boxed Value for the whole container, purely to get its address.
		// The reflect walk boxes two per ENTRY.
		id := nodeID{ptr: reflect.ValueOf(x).Pointer()}
		if path[id] {
			return true
		}
		path[id] = true
		defer delete(path, id)

		for k, val := range x {
			// A string key needs no boxing at all. It still has to be checked:
			// a key becomes a field name in tabular output, so a control byte
			// there lands in the header rather than in a cell.
			if !*dirty && cleanString(k) != k {
				*dirty = true
			}
			if fastInspect(val, path, dirty) {
				return true
			}
		}
		return false

	case []any:
		// An empty slice is not tracked, matching inspect: zero-length
		// allocations share one address (runtime.zerobase), so tracking them
		// collides unrelated slices, and a slice with no elements cannot
		// recurse anyway. A nil slice has length zero, so this covers both.
		if len(x) == 0 {
			return false
		}
		id := nodeID{ptr: reflect.ValueOf(x).Pointer(), len: len(x)}
		if path[id] {
			return true
		}
		path[id] = true
		defer delete(path, id)

		for _, e := range x {
			if fastInspect(e, path, dirty) {
				return true
			}
		}
		return false

	case []string:
		// Common enough to be worth its own arm — WriteHelp's input shape, and
		// any list of plain values. Strings cannot hold a reference, so no
		// identity tracking is needed.
		for _, s := range x {
			if !*dirty && cleanString(s) != s {
				*dirty = true
			}
		}
		return false

	default:
		return inspect(reflect.ValueOf(v), path, dirty)
	}
}
