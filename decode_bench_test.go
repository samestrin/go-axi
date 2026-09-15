package goaxi

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// Decode had no benchmark at all before this file — Encode got five dedicated
// speed-up PRs (#8-#13) with benchmarks proving each one, Decode (added in
// PR #17) shipped with none. This closes that gap: from this file on,
// BenchmarkDecodeTabular runs under the default `-bench=.` sweep and is
// therefore covered by .github/benchgate.sh on every PR, the same way every
// other benchmark in this package already is.
//
// Reproduce with: go test -run '^$' -bench BenchmarkDecodeTabular -benchmem .

// atcrFindingsFixture builds a tabular payload shaped like real atcr review
// output: the file column is quoted because it carries a "path:line" colon,
// which is the case that motivated widening the Tier 2 fast path past
// fully-unquoted rows (a fast path that only handled zero-quote rows would
// give near-zero benefit on this shape).
func atcrFindingsFixture(n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "findings[%d|]{severity|file|problem|fix}:\n", n)
	for i := 0; i < n; i++ {
		b.WriteString(`  CRITICAL|"auth.go:42"|token never expires|check expiry` + "\n")
	}
	return b.String()
}

// escapedTabularFixture builds a payload with a backslash escape in every
// row, so the fast path must decline every row and fall back to the generic
// decoder. It proves that fallback is not more expensive than the pre-Tier-2
// baseline.
func escapedTabularFixture(n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "findings[%d|]{severity|file|problem|fix}:\n", n)
	for i := 0; i < n; i++ {
		b.WriteString(`  CRITICAL|auth.go|"token \"never\" expires"|check expiry` + "\n")
	}
	return b.String()
}

// BenchmarkDecodeTabular measures the array-isolation path: reading the
// header, collecting rows, and synthesizing a document for toon-go.
func BenchmarkDecodeTabular(b *testing.B) {
	fixtures := map[string]func(int) string{
		"plain":   cleanTabularFixture,
		"atcr":    atcrFindingsFixture,
		"escaped": escapedTabularFixture,
	}
	for _, name := range []string{"plain", "atcr", "escaped"} {
		build := fixtures[name]
		for _, n := range []int{20, 500} {
			payload := build(n)
			b.Run(name+"/rows="+strconv.Itoa(n), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := DecodeTabular(strings.NewReader(payload)); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkDecode measures the whole-document path DecodeTabular is compared
// against in TestDecodeTabular_CostsLittleOverDecode — the same rows, but
// without the header pre-scan, row re-indenting, or synthesis.
func BenchmarkDecode(b *testing.B) {
	for _, n := range []int{20, 500} {
		payload := cleanTabularFixture(n)
		b.Run("rows="+strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := Decode(strings.NewReader(payload)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
