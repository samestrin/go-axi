package goaxi

import (
	"fmt"
	"testing"
)

// atcr's review (pace, MEDIUM) proposed a sync.Pool arena for Sanitize, on the
// grounds that every string allocates via reflect.New, every map via
// MakeMapWithSize, every slice via MakeSlice and every struct via New+Set.
//
// That is an accurate description of the code and NOT sufficient reason to add
// pooling. Pooling costs real complexity, can be slower than the allocator for
// short-lived objects, and introduces a class of bug (a value escaping its pool)
// that this module exists to avoid. These benchmarks exist so the decision is
// made on numbers rather than on an inspection of the source.
//
// Run: go test -bench=Sanitize -benchmem -run '^$'

func benchRows(n int, dirty bool) map[string]any {
	rows := make([]any, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("row-%d", i)
		if dirty {
			name = "row\x1b[31m" + fmt.Sprint(i)
		}
		rows = append(rows, map[string]any{
			"id":    i,
			"name":  name,
			"state": "open",
		})
	}
	return map[string]any{"rows": rows}
}

// The common case: nothing to clean. cleanString should return each string
// unchanged after a single scan, with no builder allocated.
func BenchmarkSanitize_CleanRows(b *testing.B) {
	for _, n := range []int{1, 20, 500} {
		v := benchRows(n, false)
		b.Run(fmt.Sprintf("rows=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := Sanitize(v); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// The dirty case: every name carries an ANSI escape, so every string is rebuilt.
// This is the worst case for the single-pass cleanString.
func BenchmarkSanitize_DirtyRows(b *testing.B) {
	for _, n := range []int{1, 20, 500} {
		v := benchRows(n, true)
		b.Run(fmt.Sprintf("rows=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := Sanitize(v); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Strings alone, isolating cleanString from the reflection walk.
func BenchmarkSanitizeString(b *testing.B) {
	cases := []struct{ name, in string }{
		{"clean-short", "hello"},
		{"clean-long", "a fairly ordinary sentence of the sort a CLI emits all day"},
		{"dirty-first-byte", "\x1b[31mred"},
		{"dirty-last-byte", "a fairly ordinary sentence that only goes wrong at the end\x1b"},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = SanitizeString(c.in)
			}
		})
	}
}

// Check on the OK path, which EncodeOrJSON now shares via CheckSanitized rather
// than sanitizing a second time.
func BenchmarkCheck_CleanRows(b *testing.B) {
	v := benchRows(20, false)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if got := Check(v); !got.OK {
			b.Fatalf("fixture must be OK, got %q", got.Reason)
		}
	}
}
