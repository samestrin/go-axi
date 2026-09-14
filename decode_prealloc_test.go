package goaxi

import (
	"strings"
	"testing"
)

// These pin the safety property Tier 1 performance work adds: DecodeTabular is
// about to size a slice off the header's DECLARED row count, purely to avoid
// growing it one append at a time when the count is known up front. The header
// is attacker-controlled input, so that count must never reach make([]T, 0, n)
// or Builder.Grow(n) directly — a hostile `findings[999999999999|]{a}:` must
// not try to reserve room for a trillion rows, and a malformed negative count
// (`findings[-5|]{a}:`, which strconv.Atoi accepts without complaint) must not
// reach `make` with a negative capacity at all, which panics.
//
// preallocRowCapacity is the single choke point both callers go through, so it
// is pinned directly rather than only through DecodeTabular's behavior.

// --- the helper: bounded, never negative, never wrong for the common case

func TestPreallocRowCapacity(t *testing.T) {
	tests := []struct {
		name     string
		declared int
		want     int
	}{
		{"typical small count is used as-is", 3, 3},
		{"zero is used as-is", 0, 0},
		// A 12-digit value here would overflow a 32-bit int at compile time — this
		// package has no 32-bit build target today, but a test fixture should not
		// be the thing that decides that. 999999999 (9 nines) clears
		// maxPreallocRows by five orders of magnitude while staying under
		// math.MaxInt32 (~2.147 billion).
		{"a huge declared count is capped", 999999999, maxPreallocRows},
		{"exactly at the cap is used as-is", maxPreallocRows, maxPreallocRows},
		{"one past the cap is capped", maxPreallocRows + 1, maxPreallocRows},
		{"a malformed negative count clamps to zero", -5, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := preallocRowCapacity(tt.declared); got != tt.want {
				t.Errorf("preallocRowCapacity(%d) = %d, want %d", tt.declared, got, tt.want)
			}
		})
	}
}

// --- the integration boundary: DecodeTabular's behavior must not move

func TestDecodeTabular_HugeDeclaredCountDoesNotChangeBehavior(t *testing.T) {
	// Fewer rows than declared is legitimate (TestFewerRowsThanDeclaredSurfacesBothNumbers,
	// decode_toon_test.go) — zero physical rows against a trillion declared is the
	// same shape, taken to an extreme. This must still succeed, and Declared must
	// still report the number exactly as written, proving the capacity cap only
	// bounds the internal preallocation and never the reported/observable value.
	// 999999999 (not a 12-digit value): parseHeader reads this through
	// strconv.Atoi, which returns ErrRange past math.MaxInt32 on a 32-bit build.
	// A fixture that overflows there would fail this test for a portability
	// reason unrelated to what it verifies.
	doc, err := DecodeTabular(strings.NewReader("findings[999999999|]{a}:\n"))
	if err != nil {
		t.Fatalf("a huge declared count with zero physical rows must still decode: %v", err)
	}
	if doc.Declared != 999999999 {
		t.Errorf("Declared = %d, want 999999999", doc.Declared)
	}
	if len(doc.Rows) != 0 {
		t.Errorf("Rows = %d, want 0", len(doc.Rows))
	}
}

func TestDecodeTabular_NegativeDeclaredCountDoesNotPanic(t *testing.T) {
	// strconv.Atoi accepts a leading '-', so parseHeader happily produces a
	// negative h.declared today. Nothing currently preallocates off it, so this
	// case is latent rather than yet dangerous — it becomes dangerous the moment
	// a `make([]string, 0, h.declared)` is added without a clamp, which is
	// exactly what this Tier 1 change does. Proven by absence of panic, and by
	// the existing "more rows than declared" gate still firing: zero rows is
	// already more than -5.
	_, err := DecodeTabular(strings.NewReader("findings[-5|]{a}:\n"))
	if err == nil {
		t.Fatal("a negative declared count against zero physical rows built cleanly")
	}
	if !strings.Contains(err.Error(), "-5") {
		t.Errorf("error %q does not name the declared count", err)
	}
}
