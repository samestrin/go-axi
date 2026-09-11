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
