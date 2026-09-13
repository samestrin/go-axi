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
// one traversal, and they must agree forever. What must match inspect exactly is
// the set of ANSWERS, and the rules that produce them: every reference-bearing
// kind tracked by identity with the same {ptr} or {ptr,len} key, the same
// pop-on-exit so a DAG is not mistaken for a cycle, empty slices left untracked
// because zero-length allocations share runtime.zerobase, map KEYS walked as
// well as values, and no short-circuit once dirtiness is known, because the walk
// must still complete to be sure about cycles.
//
// An earlier version of this comment claimed the edge sets matched exactly while
// the []string arm tracked no identity at all. Review caught it. The arm now
// tracks, so the claim holds — but the lesson is that a stated invariant nobody
// checks is worth less than no claim, and this package's history is mostly bugs
// of exactly that shape. That agreement is not left to
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
		// any list of plain values.
		//
		// Identity IS tracked here, even though a string cannot hold a reference
		// and so a []string can never be its own ancestor, which makes the repeat
		// branch below unreachable in practice. It is tracked because inspect
		// tracks every non-empty slice by {ptr,len}, and this file claims the two
		// edge sets match exactly. Review found that claim already false on
		// arrival because this arm tracked nothing: harmless for cycles, but an
		// invariant stated and not held is the failure mode this package keeps
		// getting bitten by.
		if len(x) == 0 {
			return false
		}
		id := nodeID{ptr: reflect.ValueOf(x).Pointer(), len: len(x)}
		if path[id] {
			return true
		}
		path[id] = true
		defer delete(path, id)

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

// fastLossyInValue answers the same question as lossyInValue — does any value in
// here have a type toon-go silently drops — without boxing a reflect.Value per
// map entry.
//
// Same motivation and same shape as fastInspect. Profiling put this walk at
// 25.85% of EncodeOrJSON's allocations, essentially all of it MapIter.Key and
// MapIter.Value boxing, which is the entire gap between EncodeOrJSON and a bare
// Encode.
//
// WHY THE FAST ARMS ARE SAFE TO SKIP OUTRIGHT. The question is whether a type
// implements encoding.TextMarshaler. A builtin has no method set, so string,
// the integer and float kinds and bool cannot. Neither can the unnamed types
// map[string]any, []any or []string. A type switch matches only those exact
// unnamed types: a DEFINED type — type Kind string, or a named map type
// carrying MarshalText — does not match and falls through to the reflect walk.
// That is exactly right, because defined string types and TextMarshaler are the
// two constructs this walk exists to catch.
//
// Keys are not walked, unlike inspect's map arm. A key here is a string, which
// cannot be lossy; inspect walks keys because a POINTER key can close a cycle,
// which is a different question.
//
// MEMO POLICY, and it is the opposite of fastInspect's. Entries are KEPT, never
// popped, because this walk memoizes "no loss below this node" — a node already
// cleared stays cleared however it is reached again. fastInspect pops, because
// it asks whether a node is on the CURRENT path and without popping every DAG
// would read as a cycle. The two sets must not be shared, and are not: each walk
// is handed its own.
//
// Termination therefore does not depend on depth, which matters because
// CheckSanitized is public and reaches this on a raw value with no cycle
// pre-check of its own.
func fastLossyInValue(v any, seen map[nodeID]bool) string {
	switch x := v.(type) {
	case nil:
		return ""

	case string,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, bool:
		return ""

	case map[string]any:
		if x == nil {
			return ""
		}
		id := nodeID{ptr: reflect.ValueOf(x).Pointer()}
		if seen[id] {
			return ""
		}
		seen[id] = true

		for _, val := range x {
			if reason := fastLossyInValue(val, seen); reason != "" {
				return reason
			}
		}
		return ""

	case []any:
		if len(x) == 0 {
			return ""
		}
		id := nodeID{ptr: reflect.ValueOf(x).Pointer(), len: len(x)}
		if seen[id] {
			return ""
		}
		seen[id] = true

		for _, e := range x {
			if reason := fastLossyInValue(e, seen); reason != "" {
				return reason
			}
		}
		return ""

	case []string:
		// Deliberately NOT memoized, unlike lossyInValue's slice arm and unlike
		// fastInspect's []string arm above. Both of those iterate; this one
		// cannot find anything, because string does not implement TextMarshaler,
		// so it answers in constant time without touching an element.
		//
		// A review finding claimed the opposite — that omitting the memo makes a
		// shared []string rescanned once per reference. There is no scan to
		// repeat: memoizing an O(1) arm would add a map write and a Pointer call
		// to buy nothing. The arm the reflect walk uses here does iterate, so
		// this is strictly cheaper than what it replaces.
		return ""

	default:
		return lossyInValue(reflect.ValueOf(v), seen)
	}
}
