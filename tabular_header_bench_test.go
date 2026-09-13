package goaxi

import (
	"bytes"
	"fmt"
	"testing"

	toon "github.com/toon-format/toon-go"
)

// These exist so the decision to skip the regexp engine stays reviewable on
// numbers rather than on an argument, the same reason the sanitize benchmarks
// exist. They also record the case that justifies the skip, which is NOT the one
// it looks like from the call site.
//
// Run: go test -bench=TabularHeader -run '^$'

func tabularFixture(b *testing.B, rows int) []byte {
	b.Helper()
	out, err := toon.Marshal(benchRows(rows, false))
	if err != nil {
		b.Fatalf("fixture: %v", err)
	}
	if !bytes.Contains(out, bracedFieldList) {
		b.Fatalf("fixture must be tabular, so the skip does NOT fire and the engine runs")
	}
	return out
}

// A non-uniform array emits indented list items with no braced field list
// anywhere, so the engine scans the whole payload and finds nothing. This is the
// shape the skip exists for.
func listFixture(b *testing.B, lines int) []byte {
	b.Helper()
	var buf bytes.Buffer
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&buf, "- item value number %d\n", i)
	}
	out := buf.Bytes()
	if bytes.Contains(out, bracedFieldList) {
		b.Fatalf("fixture must have no braced field list, or it measures the wrong path")
	}
	return out
}

// Measured on an M5, medians of 3 at 200ms:
//
//	                      engine        with skip
//	tabular 2000 rows      518ns            563ns
//	list 2000 lines    907,610ns            480ns
//
// The skip costs about 45ns on tabular output, which is 0.005% of the
// ~1,043,000ns it takes to encode that same payload. It saves about 907
// MICROseconds on a 2000-line list, which rivals the whole encode.
//
// The hand-rolled parser this replaced ran tabular input in 11ns — 47x faster
// than the engine, and irrelevant at that scale — while still costing 44,045ns
// on the list case, 92x more than the skip does. It bought speed on the shape
// that was already cheap and left the expensive one expensive, which is why it
// is worth measuring both shapes rather than the obvious one.
func BenchmarkTabularHeader(b *testing.B) {
	for _, n := range []int{20, 500, 2000} {
		tab := tabularFixture(b, n)
		b.Run(fmt.Sprintf("tabular-rows=%d/engine", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = tabularHeader.Match(tab)
			}
		})
		b.Run(fmt.Sprintf("tabular-rows=%d/with-skip", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = isTabularHeader(tab)
			}
		})
	}
	for _, n := range []int{100, 2000} {
		list := listFixture(b, n)
		b.Run(fmt.Sprintf("list-lines=%d/engine", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = tabularHeader.Match(list)
			}
		})
		b.Run(fmt.Sprintf("list-lines=%d/with-skip", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = isTabularHeader(list)
			}
		})
	}
}
