package goaxi

import (
	"strings"
	"testing"
	"unicode/utf8"

	toon "github.com/toon-format/toon-go"
)

// encodes is the AC1 workhorse: Sanitize the value, hand it to toon-go, and
// return the wire output. Every AC1 case asserts on what actually reaches
// stdout, not on Sanitize's return value in isolation — a sanitizer that
// cleaned the value but still produced an unsafe payload would pass the weaker
// test and fail the real requirement.
func encodes(t *testing.T, v any) string {
	t.Helper()
	b, err := toon.Marshal(Sanitize(v))
	if err != nil {
		t.Fatalf("encoding sanitized value must succeed, got: %v", err)
	}
	return string(b)
}

// TestSanitize_StripsANSIEscape pins the direction toon-go FAILS rather than
// leaks: a raw \x1b makes toon.Marshal return an error, so without Sanitize the
// command emits nothing at all. After Sanitize the encode must succeed and the
// visible text must survive.
func TestSanitize_StripsANSIEscape(t *testing.T) {
	out := encodes(t, map[string]any{"f": "\x1b[31mred\x1b[0m text"})

	if strings.Contains(out, "\x1b") {
		t.Errorf("raw ANSI escape byte must never reach stdout, got %q", out)
	}
	if !strings.Contains(out, "red") {
		t.Errorf("visible text must survive stripping, got %q", out)
	}
	if !strings.Contains(out, "text") {
		t.Errorf("trailing visible text must survive stripping, got %q", out)
	}
}

// TestSanitize_StripsLineAndParagraphSeparators covers the leak direction.
// U+2028 and U+2029 are separators, not Unicode "control", so a naive
// unicode.IsControl check misses them and toon-go passes them straight through.
func TestSanitize_StripsLineAndParagraphSeparators(t *testing.T) {
	cases := []struct {
		name string
		in   string
		bad  rune
	}{
		{"U+2028 line separator", "end\u2028sep", '\u2028'},
		{"U+2029 paragraph separator", "end\u2029sep", '\u2029'},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := encodes(t, map[string]any{"f": c.in})
			if strings.ContainsRune(out, c.bad) {
				t.Errorf("%s must be stripped, got %q", c.name, out)
			}
			// Contiguous, not space-substituted: the text on either side must
			// join, matching atcr's long-standing behavior.
			if !strings.Contains(out, "endsep") {
				t.Errorf("text around a stripped separator must survive contiguously, got %q", out)
			}
		})
	}
}

// TestSanitize_StripsC1Bytes pins the invalid-UTF-8 path. A lone raw C1 byte
// (8-bit CSI 0x9b / OSC 0x9d) is not valid UTF-8, and a C1 codepoint written as
// a proper rune (U+009B) is valid UTF-8 but still a control character. Both
// reach stdout through toon-go untouched.
func TestSanitize_StripsC1Bytes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		keep []string
	}{
		{"raw 0x9b (8-bit CSI)", "ab\x9bcd", []string{"ab", "cd"}},
		{"raw 0x9d (8-bit OSC)", "ef\x9dgh", []string{"ef", "gh"}},
		{"rune U+009B", "ab\u009bcd", []string{"ab", "cd"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := encodes(t, map[string]any{"f": c.in})
			if strings.ContainsAny(out, "\x9b\x9d") {
				t.Errorf("raw C1 byte must not reach stdout, got %q", out)
			}
			if strings.ContainsRune(out, '\u009b') {
				t.Errorf("C1 codepoint must not reach stdout, got %q", out)
			}
			for _, want := range c.keep {
				if !strings.Contains(out, want) {
					t.Errorf("visible text %q must survive, got %q", want, out)
				}
			}
		})
	}
}

// TestSanitize_OutputIsAlwaysValidUTF8 is the blanket invariant. Whatever goes
// in, what reaches stdout must be decodable.
func TestSanitize_OutputIsAlwaysValidUTF8(t *testing.T) {
	hostile := []string{
		"ab\xffcd",
		"\xfe\xff",
		"ab\x9bcd",
		"\x1b[0m",
		"end\u2028sep",
		"plain",
	}
	for _, in := range hostile {
		out := encodes(t, map[string]any{"f": in})
		if !utf8.ValidString(out) {
			t.Errorf("output must be valid UTF-8 for input %q, got %q", in, out)
		}
	}
}

// TestSanitize_PreservesLegitimateWhitespace guards against over-stripping.
// toon-go already escapes \n, \r and \t correctly, so Sanitize must leave them
// alone. Removing them would silently corrupt multi-line content.
func TestSanitize_PreservesLegitimateWhitespace(t *testing.T) {
	cases := []struct {
		name, in, wantEscape string
	}{
		{"newline", "a\nb", `\n`},
		{"carriage return", "a\rb", `\r`},
		{"tab", "a\tb", `\t`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := encodes(t, map[string]any{"f": c.in})
			if !strings.Contains(out, c.wantEscape) {
				t.Errorf("%s must be preserved and escaped as %s, got %q", c.name, c.wantEscape, out)
			}
		})
	}
}

// TestSanitize_LeavesCleanTextAlone is the no-op guarantee. A sanitizer that
// mangles ordinary content is worse than none, and Unicode text is ordinary
// content.
func TestSanitize_LeavesCleanTextAlone(t *testing.T) {
	clean := []string{
		"hello",
		"src/café/main.go",
		"naïve façade",
		"emoji: 🙂",
		"a|b",
		"colon: here",
		"-3.14",
	}
	for _, in := range clean {
		if got := Sanitize(in); got != in {
			t.Errorf("clean input must pass through unchanged: Sanitize(%q) = %q", in, got)
		}
	}
}

// TestSanitize_RecursesIntoNestedValues pins that the walk reaches every string,
// not just a top-level one. A sanitizer covering only the outermost value leaves
// every realistic payload — a list of rows — unprotected.
func TestSanitize_RecursesIntoNestedValues(t *testing.T) {
	v := map[string]any{
		"rows": []any{
			map[string]any{"name": "ok", "note": "bad\x1bhere"},
			map[string]any{"name": "two\u2028x", "note": "fine"},
		},
		"top": "clean",
	}
	out := encodes(t, v)

	if strings.Contains(out, "\x1b") {
		t.Errorf("ANSI escape nested in a slice-of-maps must be stripped, got %q", out)
	}
	if strings.ContainsRune(out, '\u2028') {
		t.Errorf("U+2028 nested in a slice-of-maps must be stripped, got %q", out)
	}
	for _, want := range []string{"badhere", "twox", "clean", "fine"} {
		if !strings.Contains(out, want) {
			t.Errorf("nested visible text %q must survive, got %q", want, out)
		}
	}
}

// TestSanitize_PreservesStructTags is the reason Sanitize cannot simply convert
// everything to map[string]any. Field names in TOON output are part of the CLI
// contract that consuming skills parse; losing the `toon:` tag would silently
// rename every column to its Go identifier.
func TestSanitize_PreservesStructTags(t *testing.T) {
	type row struct {
		Name string `toon:"name"`
		Note string `toon:"note"`
	}
	out := encodes(t, row{Name: "a\x1bb", Note: "ok"})

	if strings.Contains(out, "\x1b") {
		t.Errorf("ANSI escape in a struct field must be stripped, got %q", out)
	}
	if !strings.Contains(out, "name") || !strings.Contains(out, "note") {
		t.Errorf("toon struct tags must survive sanitizing, got %q", out)
	}
	if strings.Contains(out, "Name") || strings.Contains(out, "Note") {
		t.Errorf("Go field names must not leak; tags define the contract, got %q", out)
	}
}

// TestSanitize_RoundTrips is AC2, and the whole reason this module exists.
// Encoder and decoder currently live in different repositories and are tested
// only against frozen fixtures of each other's output, which go stale silently.
func TestSanitize_RoundTrips(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
	}{
		{"plain", map[string]any{"f": "hello"}},
		{"after ANSI strip", map[string]any{"f": "\x1b[31mred\x1b[0m"}},
		{"after separator strip", map[string]any{"f": "end\u2028sep"}},
		{"after C1 strip", map[string]any{"f": "ab\x9bcd"}},
		{"escaped whitespace", map[string]any{"f": "a\nb\tc"}},
		{"delimiter in value", map[string]any{"f": "a|b,c"}},
		{"colon in value", map[string]any{"f": "file.go:42"}},
		{"unicode", map[string]any{"f": "naïve 🙂"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clean := Sanitize(c.in)

			b, err := toon.Marshal(clean)
			if err != nil {
				t.Fatalf("encode sanitized value: %v", err)
			}
			got, err := toon.Decode(b)
			if err != nil {
				t.Fatalf("decode own output: %v (payload %q)", err, string(b))
			}

			gotMap, ok := got.(map[string]any)
			if !ok {
				t.Fatalf("decoded value must be a map, got %T", got)
			}
			cleanMap := clean.(map[string]any)
			if gotMap["f"] != cleanMap["f"] {
				t.Errorf("round-trip must be lossless:\n  sanitized = %q\n  decoded   = %q",
					cleanMap["f"], gotMap["f"])
			}
		})
	}
}
