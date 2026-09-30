package goaxi

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode"
)

// invisibleRunes samples the Unicode format characters (category Cf) the
// sanitizer strips. None is category Cc, so unicode.IsControl misses every one.
// Left in, they let untrusted text reorder or hide what a reader sees in a
// terminal, an editor or a diff (the Trojan Source class), or smuggle text a
// human cannot see into what an agent reads (tag characters).
//
// TestSanitizeString_StripsEveryFormatRune covers the whole category; this list
// names the ones worth reading about when a test fails.
var invisibleRunes = []struct {
	name string
	r    rune
}{
	{"U+00AD soft hyphen", '\u00ad'},
	{"U+061C arabic letter mark", '\u061c'},
	{"U+180E mongolian vowel separator", '\u180e'},
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
	{"U+2061 function application", '\u2061'},
	{"U+2066 left-to-right isolate", '\u2066'},
	{"U+2067 right-to-left isolate", '\u2067'},
	{"U+2068 first strong isolate", '\u2068'},
	{"U+2069 pop directional isolate", '\u2069'},
	{"U+206A inhibit symmetric swapping", '\u206a'},
	{"U+FEFF zero width no-break space", '\ufeff'},
	{"U+E0001 language tag", '\U000e0001'},
	{"U+E0041 tag latin capital letter a", '\U000e0041'},
}

// TestSanitizeString_StripsInvisibleRunes checks each rune at the start, the
// middle and the end of a string, because cleanString and cleanFrom take
// different branches depending on where the first dirty byte sits.
func TestSanitizeString_StripsInvisibleRunes(t *testing.T) {
	for _, c := range invisibleRunes {
		s := string(c.r)
		for _, tc := range []struct{ pos, in, want string }{
			{"start", s + "abc", "abc"},
			{"middle", "ab" + s + "cd", "abcd"},
			{"end", "abc" + s, "abc"},
			{"only", s + s, ""},
			{"between non-ASCII", "é" + s + "ü", "éü"},
		} {
			t.Run(c.name+"/"+tc.pos, func(t *testing.T) {
				if got := SanitizeString(tc.in); got != tc.want {
					t.Errorf("SanitizeString(%q) = %q, want %q", tc.in, got, tc.want)
				}
			})
		}
	}
}

// TestSanitizeString_StripsEveryFormatRune walks the whole Cf table, so a rune
// Unicode adds to the category in a later Go release is covered without a
// change here.
func TestSanitizeString_StripsEveryFormatRune(t *testing.T) {
	forEachRune(unicode.Cf, func(r rune) {
		in := fmt.Sprintf("a%cb", r)
		if got := SanitizeString(in); got != "ab" {
			t.Errorf("SanitizeString(%q) = %q, U+%04X is category Cf and must be stripped", in, got, r)
		}
	})
}

// TestSanitizeString_KeepsEveryOtherRune is the other half of the contract:
// every valid non-ASCII rune that is not a control, a format character or a
// line/paragraph separator must survive. It checks all of them, so a rule
// written wider than intended fails here.
func TestSanitizeString_KeepsEveryOtherRune(t *testing.T) {
	for _, r := range []rune{'\t', '\n', '\r'} {
		in := fmt.Sprintf("a%cb", r)
		if got := SanitizeString(in); got != in {
			t.Errorf("SanitizeString(%q) = %q, want it unchanged", in, got)
		}
	}
	var b strings.Builder
	for r := rune(0x80); r <= unicode.MaxRune; r++ {
		if (r >= 0xd800 && r <= 0xdfff) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) ||
			r == 0x2028 || r == 0x2029 {
			continue
		}
		b.Reset()
		b.WriteByte('a')
		b.WriteRune(r)
		b.WriteByte('b')
		in := b.String()
		if got := SanitizeString(in); got != in {
			t.Errorf("SanitizeString(%q) = %q, U+%04X must be kept", in, got, r)
		}
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

// Each rune is placed in a struct field, a slice element, a nested map value
// and a map key, so a walk that misses one kind of node fails for every rune.
func TestSanitize_StripsInvisibleRunesAtEveryDepth(t *testing.T) {
	type finding struct {
		Problem string   `toon:"problem"`
		Notes   []string `toon:"notes"`
	}
	for _, c := range invisibleRunes {
		t.Run(c.name, func(t *testing.T) {
			s := string(c.r)
			in := map[string]any{
				"rows":    []finding{{Problem: "ok" + s + "evil", Notes: []string{"a" + s + "b"}}},
				"key" + s: map[string]any{"inner": "x" + s + "y"},
			}
			out := encodes(t, in)
			if strings.ContainsRune(out, c.r) {
				t.Errorf("%s must not reach stdout, got %q", c.name, out)
			}
			for _, want := range []string{"okevil", "ab", "xy", "key:"} {
				if !strings.Contains(out, want) {
					t.Errorf("visible text %q must survive, got %q", want, out)
				}
			}
		})
	}
}

// Two keys that differ only by an invisible rune LOOK identical to a reader, so
// they must collide rather than both surviving.
func TestSanitize_InvisibleRuneKeyCollisionIsRefused(t *testing.T) {
	for _, c := range invisibleRunes {
		t.Run(c.name, func(t *testing.T) {
			_, err := Sanitize(map[string]any{"name": 1, "na" + string(c.r) + "me": 2})
			var collision *KeyCollisionError
			if !errors.As(err, &collision) {
				t.Fatalf("keys differing only by %s must collide, got %v", c.name, err)
			}
			if collision.Cleaned != "name" {
				t.Errorf("collision must report the cleaned key %q, got %q", "name", collision.Cleaned)
			}
		})
	}
}

// A clean string with non-ASCII text must still return itself, without
// allocating. The category check runs on every non-ASCII rune, so this is where
// a slower unsafeRune would show.
func TestSanitizeString_CleanNonASCIIDoesNotAllocate(t *testing.T) {
	const in = "naïve café — 日本語 ✓ \u202f"
	if got := SanitizeString(in); got != in {
		t.Fatalf("clean string must be unchanged, got %q", got)
	}
	if n := testing.AllocsPerRun(100, func() { _ = SanitizeString(in) }); n != 0 {
		t.Errorf("clean string must not allocate, got %v allocs", n)
	}
}

// forEachRune calls fn for every rune in t.
func forEachRune(t *unicode.RangeTable, fn func(rune)) {
	for _, r := range t.R16 {
		for c := rune(r.Lo); c <= rune(r.Hi); c += rune(r.Stride) {
			fn(c)
		}
	}
	for _, r := range t.R32 {
		for c := rune(r.Lo); c <= rune(r.Hi); c += rune(r.Stride) {
			fn(c)
		}
	}
}
