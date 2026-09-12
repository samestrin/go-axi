package goaxi

import (
	"fmt"
	"reflect"
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

// A payload with nothing to clean must not pay to be copied.
//
// sanitizeValue rebuilds every container node by node even when no string
// changed, producing a result identical to the input. Measured at 500 rows, that
// copy is 12,524 of Sanitize's 15,530 allocations — 80.6% of the total.
//
// Stated as MARGINAL allocations per row rather than a total, so the number is
// independent of the fixed cost of one call and of the machine running it. It
// also names no internal function, so merging or renaming the walks cannot break
// this test.
//
// The floor is not zero, and it is not 6 either. Sanitize makes TWO passes over
// a clean payload — the cycle guard, then needsCleaning — and each independently
// pays 6 allocations per row of reflect map-iteration boxing. Measured
// separately, hasCycle, needsCleaning and sanitizeValue's own walk all cost
// exactly 6.0 per row on the same fixture, so 12.0 is the floor for a two-walk
// design and the only route below it is merging the walks.
//
// This ceiling was first written as 10.0, from counting one walk instead of two.
// The measured numbers are 31.0 per row before the fix and 12.0 after, so the
// copy is entirely gone; 15.0 leaves headroom above the floor without letting a
// reinstated copy pass, which would show up at 31.0.
func TestSanitize_CleanPayloadDoesNotPayToBeCopied(t *testing.T) {
	const (
		small     = 20
		large     = 500
		maxPerRow = 15.0
	)

	allocsFor := func(n int) float64 {
		v := benchRows(n, false)

		// Guard the fixture. If it ever stops being clean this measures the
		// dirty path, where copying is unavoidable and the number is meaningless.
		clean, err := Sanitize(v)
		if err != nil {
			t.Fatalf("fixture must sanitize without error: %v", err)
		}
		if !reflect.DeepEqual(clean, v) {
			t.Fatal("fixture must need no cleaning, or this measures the copy of a dirty value")
		}

		return testing.AllocsPerRun(50, func() {
			if _, err := Sanitize(v); err != nil {
				t.Fatal(err)
			}
		})
	}

	perRow := (allocsFor(large) - allocsFor(small)) / float64(large-small)
	if perRow > maxPerRow {
		t.Errorf("Sanitize allocates %.1f times per row for a payload with nothing to clean, "+
			"want at most %.1f. A clean value is being rebuilt node by node into a copy "+
			"identical to the input.", perRow, maxPerRow)
	}
}

// Making the clean path cheap must not be paid for by the dirty path.
//
// The first attempt at skipping the copy decided whether a container had changed
// by recursing sanitizeValue and discarding the copies it built. That allocated
// the whole rebuild twice: a fully dirty payload went from 32.0 to 54.0
// allocations per row, 1.69x worse than before the optimization existed, and it
// was not a cold path either — one dirty string makes every ancestor container
// dirty. needsCleaning answers the same question without building anything and
// exits at the first string that needs work.
//
// Measured: 32.0 per row before any of this, 54.0 with the discarding version,
// 27.0 now. The ceiling of 35.0 would have failed the discarding version while
// still admitting the original unconditional copy, because what this guards is
// the double build, not the copy itself — the clean-path test above is what
// requires the copy to be skipped.
func TestSanitize_DirtyPayloadDoesNotPayTwice(t *testing.T) {
	const (
		small     = 20
		large     = 500
		maxPerRow = 35.0
	)

	allocsFor := func(n int) float64 {
		v := benchRows(n, true)

		// Guard the fixture from the opposite direction to the clean test: this
		// one is meaningless unless sanitizing really does change the value.
		cleaned, err := Sanitize(v)
		if err != nil {
			t.Fatalf("fixture must sanitize without error: %v", err)
		}
		if reflect.DeepEqual(cleaned, v) {
			t.Fatal("fixture must need cleaning, or this measures the clean path")
		}

		return testing.AllocsPerRun(50, func() {
			if _, err := Sanitize(v); err != nil {
				t.Fatal(err)
			}
		})
	}

	perRow := (allocsFor(large) - allocsFor(small)) / float64(large-small)
	if perRow > maxPerRow {
		t.Errorf("Sanitize allocates %.1f times per row for a payload that needs cleaning, "+
			"want at most %.1f. A cost this high means the rebuild is being allocated "+
			"twice — once to decide whether anything changed, once to build the result.",
			perRow, maxPerRow)
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
