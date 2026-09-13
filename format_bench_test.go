package goaxi

import (
	"io"
	"strconv"
	"testing"
)

// benchRow carries both tag sets so the same payload can be measured down both
// paths. Comparing a dual-tagged type against itself is the honest comparison:
// it isolates the cost of the projection rather than also changing the input.
type benchRow struct {
	File  string `json:"file"  toon:"file"`
	Line  int    `json:"line"  toon:"line"`
	Match string `json:"match" toon:"match"`
}

type benchResult struct {
	Count int        `json:"count" toon:"count"`
	Rows  []benchRow `json:"rows"  toon:"rows"`
}

func benchPayload(n int) benchResult {
	r := benchResult{Count: n, Rows: make([]benchRow, 0, n)}
	for i := 0; i < n; i++ {
		r.Rows = append(r.Rows, benchRow{
			File:  "internal/support/commands/yaml.go",
			Line:  i,
			Match: "func runYAML(cmd *cobra.Command) error {",
		})
	}
	return r
}

// BenchmarkFormat_Encode measures the direct path: sanitize, then marshal from
// the `toon` tags.
func BenchmarkFormat_Encode(b *testing.B) {
	for _, n := range []int{20, 500} {
		p := benchPayload(n)
		b.Run("rows="+strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if err := TOON.Encode(io.Discard, p); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkFormat_EncodeProjected measures the convenience path: marshal to
// JSON, decode to a generic value, then sanitize and marshal that.
//
// The gap between this and BenchmarkFormat_Encode is what the README quotes as
// the cost of not maintaining a second tag on every field. Reproduce with:
//
//	go test -run '^$' -bench 'Format_Encode' -benchmem
func BenchmarkFormat_EncodeProjected(b *testing.B) {
	for _, n := range []int{20, 500} {
		p := benchPayload(n)
		b.Run("rows="+strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if err := TOON.EncodeProjected(io.Discard, p); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkCheckTags measures the guard itself. It walks types rather than
// values, so its cost does not grow with payload size — only with type shape.
func BenchmarkCheckTags(b *testing.B) {
	p := benchPayload(1)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if v := CheckTags(p); !v.OK {
			b.Fatalf("benchmark payload should be fully tagged: %s", v)
		}
	}
}
