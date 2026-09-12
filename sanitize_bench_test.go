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
// The floor is not zero. Sanitize walks a clean payload once, via inspect, and
// that single traversal costs 6.0 allocations per row of reflect map-iteration
// boxing — a cost any reflective walk of a map[string]any pays, not overhead this
// package adds. So 6.0 is the floor and 31.0 was the starting point.
//
// Two earlier versions of this bound were wrong, both recorded because the
// mistakes are instructive. The first was an absolute 10.0 per row, from counting
// one walk when there were two — the cycle guard and the dirtiness check ran
// separately then, 6.0 each, so the real floor was 12.0. The second was absolute
// at all, which three reviewers flagged: AllocsPerRun counts are a runtime
// implementation detail, so a toolchain that changes how MapRange allocates fails
// the suite with no code change. Hence the ratio below.
func TestSanitize_CleanPayloadDoesNotPayToBeCopied(t *testing.T) {
	clean := allocationsPerRow(t, false)
	dirty := allocationsPerRow(t, true)

	// Compared against the DIRTY path measured in the same run, not against a
	// fixed number. Both figures are dominated by reflect map-iteration boxing,
	// which is a runtime implementation detail: a toolchain that changes how
	// MapRange allocates moves both together and the ratio holds, where an
	// absolute ceiling would fail the suite with no code change at all.
	//
	// Reinstating the unconditional copy puts clean at 31.0 against roughly 32.0
	// dirty — a ratio near 0.97, because then both paths rebuild everything and
	// the clean case has nothing left to save.
	//
	// The bound is tighter than that, because it also pins the number of WALKS a
	// clean payload pays for. Two separate traversals of the same value — a cycle
	// guard and then a dirtiness check — measured 12.0 against 22.0 dirty, a
	// ratio of 0.55. One traversal answering both questions measures about 6.0,
	// a ratio near 0.27. At 0.40 this fails if either the copy comes back or the
	// walks split apart again.
	const maxShare = 0.40

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
// Measured 32.0 per row before any of this, 54.0 with the discarding version,
// 23.0 now. Expressed against the CLEAN path from the same run rather than as a
// fixed ceiling, for the reason given on the clean test: both figures move
// together under a toolchain change, so a ratio survives one where an absolute
// number does not.
//
// The bound has been recalibrated ONCE, and the reason matters more than the
// number. It was 3.0, set when dirty measured 22.0 against 12.0 clean — a ratio
// of 1.8 — with the discarding version at 54.0/12.0, a ratio of 4.5. Merging the
// cycle and dirtiness walks then halved the clean path to 6.0. Dirty did not move
// (22.0 to 23.0, noise), but the ratio jumped to 3.8 purely because the
// DENOMINATOR improved, and this test failed while the thing it guards was fine.
//
// That is the exact flaw a reviewer found in output_test.go's guard-cost
// assertion — a ratio firing when its denominator gets better — reintroduced here
// while fixing it there. Recorded rather than quietly patched, because the shape
// of the assertion is the lesson: dividing by a number you are actively trying to
// reduce will keep doing this.
//
// The separation is still wide. Reintroducing the discarding detection today
// leaves clean at 6.0, since the merged walk handles that, and puts dirty near
// 54.0 — a ratio of 9.0 against 3.8 now. 6.0 sits between them with room on both
// sides. This guards the DOUBLE BUILD specifically, not the copy; the clean test
// above is what requires the copy to be skipped at all.
func TestSanitize_DirtyPayloadDoesNotPayTwice(t *testing.T) {
	clean := allocationsPerRow(t, false)
	dirty := allocationsPerRow(t, true)

	const maxRatio = 6.0

	if ratio := dirty / clean; ratio > maxRatio {
		t.Errorf("a payload needing cleaning costs %.1fx a clean one (%.1f vs %.1f allocations "+
			"per row), want at most %.1fx. A cost this high means the rebuild is allocated "+
			"twice — once to decide whether anything changed, once to build the result.",
			ratio, dirty, clean, maxRatio)
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
