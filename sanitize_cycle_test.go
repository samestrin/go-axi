package goaxi

import (
	"errors"
	"io"
	"testing"
)

type cycNode struct {
	Name string   `toon:"name"`
	Next *cycNode `toon:"next"`
}

// A cyclic value must return an error rather than kill the process.
//
// There is deliberately no RED version of this test. Before the guard,
// sanitizeValue recursed through pointers with no seen-set, and the resulting
// stack overflow is a fatal runtime error rather than a panic — recover() cannot
// catch it, so a failing test would take the whole test binary down with a
// goroutine dump instead of reporting. The pre-fix behaviour was pinned by an
// out-of-process probe instead: `go run` against v0.1.0 exited 2 with
// "fatal error: stack overflow", immediately after Check had correctly refused
// the very same value.
func TestSanitize_RefusesAReferenceCycle(t *testing.T) {
	n := &cycNode{Name: "a"}
	n.Next = n

	got, err := Sanitize(n)
	if err == nil {
		t.Fatalf("Sanitize accepted a cyclic value and returned %#v", got)
	}
	var ce *CycleError
	if !errors.As(err, &ce) {
		t.Fatalf("error = %v, want *CycleError", err)
	}
}

// Encode reaches Sanitize, and it is the path a CLI actually takes. Check
// refusing a cycle was never enough on its own, because Encode does not consult
// Check.
func TestEncode_RefusesAReferenceCycleInsteadOfCrashing(t *testing.T) {
	n := &cycNode{Name: "a"}
	n.Next = n

	if err := Encode(io.Discard, n); err == nil {
		t.Fatal("Encode accepted a cyclic value")
	}
}

// The guard must not reject a value that merely shares a node. Two fields
// pointing at one struct is a DAG, not a cycle, and refusing it would break
// ordinary payloads — which is the failure mode an over-eager seen-set produces.
func TestSanitize_AcceptsASharedNodeThatIsNotACycle(t *testing.T) {
	shared := &cycNode{Name: "shared"}
	pair := struct {
		A *cycNode `toon:"a"`
		B *cycNode `toon:"b"`
	}{A: shared, B: shared}

	if _, err := Sanitize(pair); err != nil {
		t.Fatalf("Sanitize refused a shared node: %v", err)
	}
}

// A cycle deeper than the old maxWalkDepth cap. hasCycle used to return false
// at depth 100, so this chain was reported cycle-free and sanitizeValue — which
// has no depth limit and no seen-set — walked it into a fatal stack overflow.
// atcr's review caught it as HIGH (greta) and MEDIUM (kai, dax), and it was
// reproduced exactly that way before the cap was removed.
//
// If this test ever crashes the suite instead of failing, the cap is back.
func TestSanitize_RefusesACycleDeeperThanTheOldDepthCap(t *testing.T) {
	const n = 200 // each node costs two levels of walk depth; the old cap was 100

	nodes := make([]*cycNode, n)
	for i := range nodes {
		nodes[i] = &cycNode{Name: "n"}
	}
	for i := 0; i < n-1; i++ {
		nodes[i].Next = nodes[i+1]
	}
	nodes[n-1].Next = nodes[0]

	if _, err := Sanitize(nodes[0]); err == nil {
		t.Fatal("a cycle deeper than the walk cap must be refused")
	}
	if Check(nodes[0]).OK {
		t.Error("a cycle deeper than the walk cap must not be reported OK")
	}
}

// A chain of the same depth that does NOT close must still be accepted. Refusing
// every deep value would trade the crash for a guard that lies about legitimate
// data.
func TestSanitize_AcceptsADeepAcyclicChain(t *testing.T) {
	const n = 200

	head := &cycNode{Name: "n"}
	cur := head
	for i := 1; i < n; i++ {
		cur.Next = &cycNode{Name: "n"}
		cur = cur.Next
	}

	if _, err := Sanitize(head); err != nil {
		t.Fatalf("a deep but acyclic chain must be accepted, got %v", err)
	}
}

// A slice that contains itself closes a loop with no pointer and no map. hasCycle
// tracked only pointers and maps, so this reached sanitizeValue and killed the
// process with a stack overflow.
//
// If this test ever crashes the suite instead of failing, slice identity
// tracking is gone.
func TestSanitize_RefusesASelfContainingSlice(t *testing.T) {
	s := make([]any, 1)
	s[0] = s

	if _, err := Sanitize(s); err == nil {
		t.Fatal("a slice containing itself must be refused")
	}
	if Check(s).OK {
		t.Error("a slice containing itself must not be reported OK")
	}
}

// A sub-slice shares its parent's backing array without either containing the
// other. Keying the seen set on the data pointer alone reports this as a cycle;
// the length is what keeps the two apart.
func TestSanitize_SubSliceSharingABackingArrayIsNotACycle(t *testing.T) {
	outer := make([]any, 2)
	outer[0] = "a"
	outer[1] = outer[:1]

	if _, err := Sanitize(outer); err != nil {
		t.Fatalf("a sub-slice is not a cycle, got %v", err)
	}
}

// The same slice reached twice by two different paths is a DAG, not a cycle. The
// seen entry has to be popped on the way back out for this to pass.
func TestSanitize_SliceReachedTwiceIsNotACycle(t *testing.T) {
	shared := []any{"a", "b"}
	outer := []any{shared, shared}

	if _, err := Sanitize(outer); err != nil {
		t.Fatalf("a slice reached by two paths is not a cycle, got %v", err)
	}
}

// A cyclic value that ALSO carries a lossy type must still be refused as a
// cycle.
//
// This is a live guard, not a hypothetical one. inspect answers the cycle
// question and the dirtiness question in a single traversal, which is where the
// hazard lives: a merged walk that RETURNS EARLY once it knows the value is
// dirty never reaches a cycle further along. Sanitize would then see no cycle,
// proceed into sanitizeValue — which has neither a seen-set nor a depth limit —
// and exhaust the stack. That is a fatal runtime error rather than a panic:
// recover() cannot catch it, so the process dies with a goroutine dump instead
// of returning an error to the caller.
//
// So inspect never short-circuits on dirtiness. It records the answer through a
// pointer and keeps walking every edge; only a CYCLE may return early, because
// Sanitize refuses the value outright in that case. This test is what keeps that
// rule honest.
//
// The value here carries a lossy type as well, so it also covers the separate
// walk: lossyInValue stays independent of inspect because the two need opposite
// seen-set policies — popped for cycle detection, kept for memoization — and
// merging those would break one of them.
//
// If this test ever crashes the suite instead of failing, a walk is
// short-circuiting before it has finished looking for cycles.
func TestSanitize_CyclicValueCarryingALossyTypeIsRefusedAsACycle(t *testing.T) {
	type node struct {
		M    textMarshaler `toon:"m"`
		Next *node         `toon:"next"`
	}
	n := &node{M: textMarshaler{v: "PAYLOAD"}}
	n.Next = n

	_, err := Sanitize(n)
	if err == nil {
		t.Fatal("a cyclic value must be refused even when it also carries a lossy type")
	}
	var ce *CycleError
	if !errors.As(err, &ce) {
		t.Fatalf("error must be *CycleError, not the lossy reason: got %T: %v", err, err)
	}

	if Check(n).OK {
		t.Error("a cyclic value must not be reported OK, whatever else it carries")
	}
}
