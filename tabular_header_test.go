package goaxi

import (
	"math/rand"
	"testing"
)

// isTabularHeader is a hand-rolled parser standing in for the tabularHeader
// regexp, and the two must answer identically for every input.
//
// WHY THIS TEST EXISTS. The scanner was written to avoid the regexp engine on
// the output path and shipped with nothing asserting the two agree. They did
// not: under `(?m)` the regexp's `[^\[\]{}:]*`, `[^\]]*` and `[^}]*` classes all
// match a newline, so the pattern can bridge a header across lines, while the
// scanner restarts at every `\n` and stops its prefix at `\r`. `rows[2\nx]{a}:`
// matched the regexp and not the scanner.
//
// A disagreement is not cosmetic. The answer sets Verdict.Tier, which is a
// public field that callers route on, so a divergence silently reclassifies
// TierTabular as TierNested with no error and — before this test — no failure.
//
// The regexp is therefore deliberately KEPT as the oracle rather than deleted as
// dead code. It is the specification; the scanner is an optimisation of it, and
// this test is the only thing that makes that claim checkable.
func TestIsTabularHeader_AgreesWithTheRegexp(t *testing.T) {
	shapes := []string{
		"",
		"rows[2]{id,name}:",
		"  rows[2]{id,name}:",
		"\trows[2]{id,name}:",
		"rows[2]:",
		"rows[]{a}:",
		"rows[2]{}:",
		"rows[2]{a}",
		"rows{a}[2]:",
		"a: 1\nrows[2]{id,name}:\nb: 2",
		"prefix\nrows[3]{x}:",
		"rows[2\nx]{a}:",
		"rows[2\rx]{a}:",
		"rows[2]{a\nb}:",
		"foo\nbar[2]{a,b}:",
		"[2]{a}:",
		"x[12345]{a,b,c}:",
		"x[2 extra]{a}:",
		"nested:\n  rows[2]{id}:",
		"rows[2]{a}\n:",
		"\f rows[2]{a}:",
		"\v rows[2]{a}:",
		"3[0\r30]{:{}:",
		"no header here at all",
		"}{][:",
	}

	for _, s := range shapes {
		got := isTabularHeader([]byte(s))
		want := tabularHeader.Match([]byte(s))
		if got != want {
			t.Errorf("isTabularHeader(%q) = %v, regexp = %v", s, got, want)
		}
	}
}

// The fixed cases above were written by someone who already knew where the
// divergence was. This one is not: it draws from the alphabet the pattern is
// built out of, so it explores bracket and brace nesting no human would think to
// write down. Both divergences that shipped were found this way first.
func TestIsTabularHeader_AgreesWithTheRegexpOnRandomInput(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	alphabet := []byte("ab[]{}:0123\n \t\r")

	for i := 0; i < 200000; i++ {
		b := make([]byte, rng.Intn(18))
		for j := range b {
			b[j] = alphabet[rng.Intn(len(alphabet))]
		}
		if got, want := isTabularHeader(b), tabularHeader.Match(b); got != want {
			t.Fatalf("isTabularHeader(%q) = %v, regexp = %v", b, got, want)
		}
	}
}
