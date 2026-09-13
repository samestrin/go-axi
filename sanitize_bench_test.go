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
// Measured as MARGINAL allocations per row, so the figure is independent of the
// fixed cost of one call and of the machine running it — and it names no internal
// function, so merging or renaming the walks cannot break this test.
func TestSanitize_CleanPayloadDoesNotPayToBeCopied(t *testing.T) {
	clean := allocationsPerRow(t, false)
	dirty := allocationsPerRow(t, true)

	// THE FLOOR IS ZERO, and that is the point of this bound. Walking a clean
	// payload requires no allocation at all: nothing is copied, nothing is built,
	// and the only reason it ever cost anything was reflect boxing a Value for
	// every map key and every map value. A walk that type-switches the shapes a
	// TOON payload is actually made of — map[string]any, []any, string, scalars —
	// boxes once per CONTAINER for identity tracking and not at all for keys.
	//
	// History of this figure: 31.0 allocations per row when every clean container
	// was rebuilt, 12.0 once the copy was skipped but two separate walks remained,
	// 6.0 after those merged into one, and 0.0 once that one walk stops using
	// reflect for the common shapes.
	//
	// WHY A RATIO against the dirty path measured in the SAME run, rather than a
	// fixed count. An absolute AllocsPerRun ceiling is hostage to the runtime, so
	// a toolchain that changes how MapRange allocates fails the suite with no code
	// change at all. Two earlier bounds here were absolute and both were wrong —
	// 10.0, from counting one walk when there were two, then 15.0, which three
	// reviewers flagged.
	//
	// WHY 0.10. Reinstating the unconditional copy puts clean at 31.0 against
	// roughly 32.0 dirty, a ratio near 0.97. Skipping the copy but splitting the
	// walks again gives 12.0 against 22.0, a ratio of 0.55. Keeping one merged
	// walk but going back to reflect gives 6.0 against 22.0, a ratio of 0.27. A
	// reflect-free walk gives 0.0, a ratio of 0.00. At 0.10 every one of those
	// regressions fails and only the intended state passes.
	const maxShare = 0.10

	if share := clean / dirty; share > maxShare {
		t.Errorf("a clean payload costs %.2f of what a dirty one costs (%.1f vs %.1f allocations "+
			"per row), want at most %.2f. A clean value is being rebuilt node by node into a "+
			"copy identical to the input.", share, clean, dirty, maxShare)
	}
}

// allocationsPerRow measures the MARGINAL allocation cost of one row, so the
// figure is independent of the fixed cost of a call. It names no internal
// function, so merging or renaming the walks cannot break its callers.
func allocationsPerRow(t *testing.T, dirty bool) float64 {
	t.Helper()

	const (
		small = 20
		large = 500
	)

	measure := func(n int) float64 {
		v := benchRows(n, dirty)

		// Guard the fixture in whichever direction this caller needs. A clean
		// measurement taken on a dirty payload, or the reverse, is meaningless.
		got, err := Sanitize(v)
		if err != nil {
			t.Fatalf("fixture must sanitize without error: %v", err)
		}
		if changed := !reflect.DeepEqual(got, v); changed != dirty {
			t.Fatalf("fixture dirty=%v but sanitizing changed it=%v", dirty, changed)
		}

		return testing.AllocsPerRun(50, func() {
			if _, err := Sanitize(v); err != nil {
				t.Fatal(err)
			}
		})
	}

	return (measure(large) - measure(small)) / float64(large-small)
}

// deepCopyPayload rebuilds a payload by hand: no reflection, no cleaning, no
// guards. It is the yardstick the dirty-path assertion below measures against —
// the cost of exactly one rebuild of this shape, and nothing else.
func deepCopyPayload(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = deepCopyPayload(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = deepCopyPayload(e)
		}
		return out
	default:
		return x
	}
}

// deepCopyAllocationsPerRow is allocationsPerRow's yardstick twin, measured the
// same way over the same fixture so the two are directly comparable.
func deepCopyAllocationsPerRow(t *testing.T) float64 {
	t.Helper()

	const (
		small = 20
		large = 500
	)

	measure := func(n int) float64 {
		v := benchRows(n, true)
		return testing.AllocsPerRun(50, func() { _ = deepCopyPayload(v) })
	}

	return (measure(large) - measure(small)) / float64(large-small)
}

// Making the clean path cheap must not be paid for by the dirty path.
//
// The first attempt at skipping the copy decided whether a container had changed
// by recursing sanitizeValue and discarding the copies it built. That allocated
// the whole rebuild twice: a fully dirty payload went from 32.0 to 54.0
// allocations per row, 1.69x worse than before the optimization existed, and it
// was not a cold path either — one dirty string makes every ancestor container
// dirty.
//
// WHAT THE DENOMINATOR MUST NOT BE: the clean path. This assertion was written
// twice against it and broke twice, both times because the clean path got
// FASTER. First the walk merge halved it and the ratio jumped from 1.8 to 3.8,
// failing while the property held; the bound was recalibrated, which treated the
// symptom. Then the reflect-free walk took the clean path to zero allocations
// per row and the ratio became +Inf. An external reviewer filed that second
// failure as a prediction before it happened, having already found the identical
// flaw in output_test.go's guard-cost assertion. Dividing by a number you are
// actively driving down will keep doing this.
//
// So the denominator is now a hand-written deep copy of the same fixture,
// measured in the same run. It moves WITH the rebuild rather than against it,
// and it is immune to every optimisation applied to the walks: measured at 2.0
// allocations per row under both the reflect walk and the reflect-free one.
//
// Against that yardstick, and every figure here is MEASURED rather than derived:
//
//	as shipped, reflect-free walk     8.00
//	as shipped, reflect walk         11.00
//	a double build                   16.00   <- this must fail
//
// The bound was 16.0, and it was worthless. It came from arithmetic — a 54.0
// per-row figure recorded under a different accounting, divided by this 2.0
// yardstick — which put a double build at "roughly 27.0". A reviewer objected
// that the anchor had never been measured. Measuring it put a double build at
// exactly 16.00, which against a strictly-greater comparison means the guard
// would have PASSED while the regression it exists to catch was present. The
// estimate was wrong by 1.7x in the one direction that makes a test useless.
//
// The measured 16.00 is a conservative floor, not an estimate of the original
// bug. It models the regression by running the full rebuild twice, whereas the
// version that shipped did a partial detection pass per CONTAINER and measured
// far worse. A bound that fails this model therefore fails anything worse too.
//
// 12.0 sits above both shipped implementations, tolerates either walk, and fails
// a double build by 4.0.
//
// This guards the DOUBLE BUILD specifically, not the copy. The clean test above
// is what requires the copy to be skipped at all.
func TestSanitize_DirtyPayloadDoesNotPayTwice(t *testing.T) {
	dirty := allocationsPerRow(t, true)
	rebuild := deepCopyAllocationsPerRow(t)

	const maxRatio = 12.0

	if ratio := dirty / rebuild; ratio > maxRatio {
		t.Errorf("sanitizing a dirty payload costs %.1fx one hand-written rebuild (%.1f vs %.1f "+
			"allocations per row), want at most %.1fx. A cost this high means the rebuild is "+
			"allocated twice — once to decide whether anything changed, once to build the result.",
			ratio, dirty, rebuild, maxRatio)
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
