package goaxi

import (
	"reflect"
	"testing"
)

// memoProbeRows builds a payload of n DISTINCT containers: one map per row, all
// reachable from a single slice. Nothing is shared, so every row is a node the
// lossy walk has to track, and the tracking map is the thing under test.
func memoProbeRows(n int) map[string]any {
	rows := make([]any, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, map[string]any{"id": i, "name": "row"})
	}
	return map[string]any{"rows": rows}
}

// The lossy walk must not retain one entry per node for the whole walk.
//
// It memoizes "no loss below this node" and never pops, so the map grew to one
// nodeID per distinct pointer, map and slice in the entire payload — O(total
// nodes) live, where the depth cap it replaced was O(depth). This sits on the
// output path through EncodeChecked and EncodeOrJSON into verdictFor, so a
// million-row listing built a multi-million-entry map on top of the payload
// itself.
//
// The memo cannot simply be removed. It is what TERMINATES the walk: a cycle is
// caught by arriving at a node already in the map. Popping on exit, as inspect
// does, would defeat the memoization and leave nothing to stop a loop. So the
// requirement is a BOUND, not a removal, and the bound has to hold while cycle
// safety still does — TestLossyWalk_TerminatesOnACycleAfterTheMemoIsFull is the
// other half of this pair.
//
// The bound asserted here is deliberately loose and literal rather than the
// implementation's own constant. A test that reads the constant it is checking
// proves only that the code equals itself; this one fails for any unbounded
// implementation regardless of what the constant is set to.
func TestLossyWalk_MemoIsBounded(t *testing.T) {
	const (
		rows        = 50000
		wantAtMost  = 8192
		description = "one entry per node for the whole walk"
	)

	in := memoProbeRows(rows)

	t.Run("fast lane", func(t *testing.T) {
		nodes := map[nodeID]bool{}
		if reason := fastLossyInValue(in, nodes); reason != "" {
			t.Fatalf("probe payload must be lossless, got %q", reason)
		}
		if len(nodes) > wantAtMost {
			t.Errorf("tracking map holds %d entries after walking %d rows, want at most %d: "+
				"the walk is retaining %s, so peak memory tracks node count rather than depth",
				len(nodes), rows, wantAtMost, description)
		}
	})

	t.Run("reflect lane", func(t *testing.T) {
		nodes := map[nodeID]bool{}
		if reason := lossyInValue(reflect.ValueOf(in), nodes); reason != "" {
			t.Fatalf("probe payload must be lossless, got %q", reason)
		}
		if len(nodes) > wantAtMost {
			t.Errorf("tracking map holds %d entries after walking %d rows, want at most %d: "+
				"the walk is retaining %s, so peak memory tracks node count rather than depth",
				len(nodes), rows, wantAtMost, description)
		}
	})
}

// Bounding the memo must not cost cycle safety, which is the property the memo
// was carrying.
//
// The hazard is specific: once the bound starts evicting entries, an evicted
// node is indistinguishable from an unvisited one. If a node on the CURRENT path
// were ever evicted, arriving at it again would recurse forever — and a stack
// overflow is a fatal runtime error, not a panic, so recover() cannot catch it
// and the process dies. Entries for nodes still being walked therefore have to
// survive eviction.
//
// The cycle here sits AFTER enough distinct nodes to push the walk past any
// sane bound, so it is reached in the evicting regime rather than the
// comfortable one. A regression does not fail this test politely: it kills the
// test binary.
func TestLossyWalk_TerminatesOnACycleAfterTheMemoIsFull(t *testing.T) {
	in := memoProbeRows(20000)

	cyclic := map[string]any{"self": nil}
	cyclic["self"] = cyclic
	in["cycle"] = cyclic

	t.Run("fast lane", func(t *testing.T) {
		if reason := fastLossyInValue(in, map[nodeID]bool{}); reason != "" {
			t.Errorf("a cycle is not a lossy TYPE, so the walk must report nothing, got %q", reason)
		}
	})

	t.Run("reflect lane", func(t *testing.T) {
		if reason := lossyInValue(reflect.ValueOf(in), map[nodeID]bool{}); reason != "" {
			t.Errorf("a cycle is not a lossy TYPE, so the walk must report nothing, got %q", reason)
		}
	})
}

// A shared subtree must still be answered correctly once the memo stops
// retaining it. Eviction may make the walk re-visit a node, which costs time;
// it must never change the ANSWER.
//
// Both directions matter: a shared CLEAN node must stay clean however often it
// is reached, and a shared LOSSY node must still be caught on whichever visit
// reaches it first.
func TestLossyWalk_EvictionDoesNotChangeTheAnswer(t *testing.T) {
	clean := map[string]any{"k": "v"}
	in := memoProbeRows(20000)
	in["a"] = clean
	in["b"] = clean

	if reason := fastLossyInValue(in, map[nodeID]bool{}); reason != "" {
		t.Errorf("a shared clean node must stay clean however often it is reached, got %q", reason)
	}

	lossy := map[string]any{"m": textMarshaler{v: "x"}}
	dirty := memoProbeRows(20000)
	dirty["a"] = lossy
	dirty["b"] = lossy

	if reason := fastLossyInValue(dirty, map[nodeID]bool{}); reason == "" {
		t.Error("a shared lossy node must still be caught after eviction, got no reason")
	}
}
