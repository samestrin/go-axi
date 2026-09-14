package goaxi

import (
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

// BenchmarkDecodeTabular measures the array-isolation path: reading the
// header, collecting rows, and synthesizing a document for toon-go.
func BenchmarkDecodeTabular(b *testing.B) {
	for _, n := range []int{20, 500} {
		payload := cleanTabularFixture(n)
		b.Run("rows="+strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := DecodeTabular(strings.NewReader(payload)); err != nil {
					b.Fatal(err)
				}
			}
		})
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
