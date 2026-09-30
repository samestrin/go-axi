package goaxi

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// invisibleRunes is every bidi control and zero-width character the sanitizer
// strips. All are Unicode category Cf, not Cc, so unicode.IsControl misses every
// one of them. Left in, they let untrusted text reorder or hide what a reader
// sees in a terminal, an editor or a diff (the Trojan Source class).
var invisibleRunes = []struct {
	name string
	r    rune
}{
	{"U+061C arabic letter mark", '\u061c'},
	{"U+200B zero width space", '\u200b'},
	{"U+200C zero width non-joiner", '\u200c'},
	{"U+200D zero width joiner", '\u200d'},
	{"U+200E left-to-right mark", '\u200e'},
	{"U+200F right-to-left mark", '\u200f'},
	{"U+202A left-to-right embedding", '\u202a'},
	{"U+202B right-to-left embedding", '\u202b'},
	{"U+202C pop directional formatting", '\u202c'},
	{"U+202D left-to-right override", '\u202d'},
	{"U+202E right-to-left override", '\u202e'},
	{"U+2060 word joiner", '\u2060'},
	{"U+2066 left-to-right isolate", '\u2066'},
	{"U+2067 right-to-left isolate", '\u2067'},
	{"U+2068 first strong isolate", '\u2068'},
	{"U+2069 pop directional isolate", '\u2069'},
	{"U+FEFF zero width no-break space", '\ufeff'},
}

// TestSanitizeString_StripsInvisibleRunes checks each rune at the start, the
// middle and the end of a string, because cleanString and cleanFrom take
// different branches depending on where the first dirty byte sits.
func TestSanitizeString_StripsInvisibleRunes(t *testing.T) {
	for _, c := range invisibleRunes {
		t.Run(c.name, func(t *testing.T) {
			s := string(c.r)
			for _, tc := range []struct{ in, want string }{
				{s + "abc", "abc"},
				{"ab" + s + "cd", "abcd"},
				{"abc" + s, "abc"},
				{s + s, ""},
				{"é" + s + "ü", "éü"}, // dirty rune between two clean non-ASCII runes
			} {
				if got := SanitizeString(tc.in); got != tc.want {
					t.Errorf("SanitizeString(%q) = %q, want %q", tc.in, got, tc.want)
				}
			}
		})
	}
}

// TestSanitize_StripsInvisibleRunesOnTheWire asserts on encoded output, not on
// Sanitize's return value, matching the rest of sanitize_test.go.
func TestSanitize_StripsInvisibleRunesOnTheWire(t *testing.T) {
	for _, c := range invisibleRunes {
		t.Run(c.name, func(t *testing.T) {
			out := encodes(t, map[string]any{"f": "end" + string(c.r) + "sep"})
			if strings.ContainsRune(out, c.r) {
				t.Errorf("%s must not reach stdout, got %q", c.name, out)
			}
			if !strings.Contains(out, "endsep") {
				t.Errorf("text around a stripped rune must join contiguously, got %q", out)
			}
		})
	}
}

// The classic Trojan Source payload: an override that makes the tail of a line
// render before its head. It must be removed wherever the walk can reach.
func TestSanitize_StripsInvisibleRunesAtEveryDepth(t *testing.T) {
	type finding struct {
		Problem string   `toon:"problem"`
		Notes   []string `toon:"notes"`
	}
	const rlo = "\u202e"
	in := map[string]any{
		"rows":    []finding{{Problem: "ok" + rlo + "evil", Notes: []string{"a\u200bb"}}},
		"k" + rlo: map[string]any{"inner": "x\ufeffy"},
	}
	out := encodes(t, in)
	for _, c := range invisibleRunes {
		if strings.ContainsRune(out, c.r) {
			t.Errorf("%s must not reach stdout, got %q", c.name, out)
		}
	}
	for _, want := range []string{"okevil", "ab", "xy"} {
		if !strings.Contains(out, want) {
			t.Errorf("visible text %q must survive, got %q", want, out)
		}
	}
}

// Two keys that differ only by an invisible rune LOOK identical to a reader, so
// they must collide rather than both surviving.
func TestSanitize_InvisibleRuneKeyCollisionIsRefused(t *testing.T) {
	_, err := Sanitize(map[string]any{"name": 1, "na\u200bme": 2})
	var collision *KeyCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("keys differing only by U+200B must collide, got %v", err)
	}
	if collision.Cleaned != "name" {
		t.Errorf("collision must report the cleaned key %q, got %q", "name", collision.Cleaned)
	}
}

// TestSanitizeString_KeepsNeighboursOfInvisibleRunes pins the edges of every
// stripped range, so a range written one wider than intended fails here.
func TestSanitizeString_KeepsNeighboursOfInvisibleRunes(t *testing.T) {
	keep := []rune{
		'\t', '\n', '\r',
		'\u061b', '\u061d', // around U+061C
		'\u200a',           // hair space, just below U+200B
		'\u2010',           // hyphen, just above U+200F
		'\u2027',           // below U+2028-U+202E, which is stripped end to end
		'\u202f',           // narrow no-break space, just above U+202E
		'\u205f', '\u2061', // around U+2060
		'\u2065', '\u206a', // around U+2066-U+2069
		'\ufefe', '\ufffd', // around U+FEFF, and a genuine replacement character
	}
	for _, r := range keep {
		in := fmt.Sprintf("a%cb", r)
		if got := SanitizeString(in); got != in {
			t.Errorf("SanitizeString(%q) = %q, U+%04X must be kept", in, got, r)
		}
	}
}

// A clean string with non-ASCII text must still return itself, without
// allocating. The new checks run on every non-ASCII rune, so this is where a
// slower unsafeRune would show.
func TestSanitizeString_CleanNonASCIIDoesNotAllocate(t *testing.T) {
	const in = "naïve café — 日本語 ✓ \u202f"
	if got := SanitizeString(in); got != in {
		t.Fatalf("clean string must be unchanged, got %q", got)
	}
	if n := testing.AllocsPerRun(100, func() { _ = SanitizeString(in) }); n != 0 {
		t.Errorf("clean string must not allocate, got %v allocs", n)
	}
}
