package goaxi

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// decode_fast_test.go is Tier 2's safety net: decodeRowsFast is a second,
// independent implementation of what decodeRowsGeneric already does
// correctly, and the two must agree byte-for-byte whenever the fast path
// claims to handle a document. This file is written before decodeRowsFast has
// any real logic (it starts as a stub that always declines), per the
// project's Red -> Green -> Adversarial Review -> Refactor process.

// fastFixtureHeader builds a *header for the given fields and delimiter, the
// same way synthHeaderFixture (decode_test.go) does for a fixed shape.
func fastFixtureHeader(fields []string, delim rune) *header {
	return &header{
		name:       "r",
		nameText:   "r",
		fields:     fields,
		fieldsText: strings.Join(fields, string(delim)),
		hasFields:  true,
		delimiter:  delim,
		declared:   -1, // unused by decodeRowsFast/decodeRowsGeneric
	}
}

// runBothDecoders is the core differential check (AC1): whenever the fast
// path declares ok, its output must be identical to the generic decoder's
// output on the exact same (h, rows) input, and the generic decoder must not
// have errored either.
func runBothDecoders(t *testing.T, h *header, rows []string) (fastOut []map[string]string, fastOK bool, genOut []map[string]string, genErr error) {
	t.Helper()
	fastOut, fastOK = decodeRowsFast(h, rows)
	genOut, genErr = decodeRowsGeneric(h, rows, 1)
	if fastOK {
		if genErr != nil {
			t.Fatalf("fast path said ok, but the generic decoder it must match errored: %v", genErr)
		}
		if !reflect.DeepEqual(fastOut, genOut) {
			t.Fatalf("fast and generic disagree\n fast: %#v\n generic: %#v", fastOut, genOut)
		}
	}
	return
}

// row builds one already-indented row the way DecodeTabular's own collection
// loop does: "  " + the trimmed row text.
func row(s string) string { return "  " + s }

func TestDecodeRowsFast_AgreesWithGeneric_TableDriven(t *testing.T) {
	cases := []struct {
		name       string
		fields     []string
		delim      rune
		rows       []string
		wantFastOK bool // explicit classification, on top of the DeepEqual check above
	}{
		{
			name:       "clean unquoted rows",
			fields:     []string{"severity", "file", "problem", "fix"},
			delim:      '|',
			rows:       []string{row("CRITICAL|auth.go|token never expires|check expiry")},
			wantFastOK: true,
		},
		{
			name:       "atcr shape: one quoted field, no escapes",
			fields:     []string{"severity", "file", "problem", "fix"},
			delim:      '|',
			rows:       []string{row(`CRITICAL|"auth.go:42"|token never expires|check expiry`)},
			wantFastOK: true,
		},
		{
			name:       "multiple quoted fields in one row",
			fields:     []string{"a", "b", "c"},
			delim:      '|',
			rows:       []string{row(`"x:1"|"y:2"|plain`)},
			wantFastOK: true,
		},
		{
			name:       "quoted field containing the delimiter literally",
			fields:     []string{"a", "b"},
			delim:      '|',
			rows:       []string{row(`"a|b"|c`)},
			wantFastOK: true,
		},
		{
			name:       "empty field",
			fields:     []string{"a", "b"},
			delim:      '|',
			rows:       []string{row(`|x`)},
			wantFastOK: true,
		},
		{
			name:       "keywords true false null",
			fields:     []string{"t", "f", "n"},
			delim:      '|',
			rows:       []string{row(`true|false|null`)},
			wantFastOK: true,
		},
		{
			name:       "comma delimiter",
			fields:     []string{"a", "b"},
			delim:      ',',
			rows:       []string{row(`x,y`)},
			wantFastOK: true,
		},
		{
			name:       "tab delimiter",
			fields:     []string{"a", "b"},
			delim:      '\t',
			rows:       []string{row("x\ty")},
			wantFastOK: true,
		},
		{
			name:       "leading zero stays a string",
			fields:     []string{"a"},
			delim:      '|',
			rows:       []string{row(`007`)},
			wantFastOK: true,
		},
		{
			name:       "negative zero normalizes to 0",
			fields:     []string{"a"},
			delim:      '|',
			rows:       []string{row(`-0`)},
			wantFastOK: true,
		},
		{
			name:       "two decimal places collapse",
			fields:     []string{"a"},
			delim:      '|',
			rows:       []string{row(`0.50`)},
			wantFastOK: true,
		},
		{
			name:       "unicode content, no special bytes",
			fields:     []string{"a", "b"},
			delim:      '|',
			rows:       []string{row(`café|日本語`)},
			wantFastOK: true,
		},
		{
			name:       "padding trimmed unquoted, kept quoted",
			fields:     []string{"bare", "quoted"},
			delim:      '|',
			rows:       []string{row(`pad me  |"  keep me  "`)},
			wantFastOK: true,
		},
		{
			name:       "escaped quote inside a value",
			fields:     []string{"a"},
			delim:      '|',
			rows:       []string{row(`"a\"b"`)},
			wantFastOK: false,
		},
		{
			name:       "escaped backslash",
			fields:     []string{"a"},
			delim:      '|',
			rows:       []string{row(`"a\\b"`)},
			wantFastOK: false,
		},
		{
			name:       "escaped newline",
			fields:     []string{"a"},
			delim:      '|',
			rows:       []string{row(`"a\nb"`)},
			wantFastOK: false,
		},
		{
			name:       "escaped tab",
			fields:     []string{"a"},
			delim:      '|',
			rows:       []string{row(`"a\tb"`)},
			wantFastOK: false,
		},
		{
			name:       "backslash outside quotes is still declined",
			fields:     []string{"a", "b"},
			delim:      '|',
			rows:       []string{row(`a\b|c`)},
			wantFastOK: false,
		},
		{
			name:       "unquoted colon in a value",
			fields:     []string{"a", "b"},
			delim:      '|',
			rows:       []string{row(`see line 42: fix it|x`)},
			wantFastOK: false,
		},
		{
			name:       "too few columns",
			fields:     []string{"a", "b", "c"},
			delim:      '|',
			rows:       []string{row(`x|y`)},
			wantFastOK: false,
		},
		{
			name:       "too many columns",
			fields:     []string{"a", "b"},
			delim:      '|',
			rows:       []string{row(`x|y|z`)},
			wantFastOK: false,
		},
		{
			name:       "quote does not simply wrap the token",
			fields:     []string{"a", "b"},
			delim:      '|',
			rows:       []string{row(`"ab"cd|x`)},
			wantFastOK: false,
		},
		{
			name:   "quote starts mid-token",
			fields: []string{"a", "b"},
			delim:  '|',
			rows:   []string{row(`ab"cd"|x`)},
			// starts with a non-quote byte, so it's treated as a plain string
			// on both paths -- included to confirm that, not to decline.
			wantFastOK: true,
		},
		{
			name:   "many rows, mixed plain and atcr-shaped",
			fields: []string{"severity", "file", "problem", "fix"},
			delim:  '|',
			rows: []string{
				row(`CRITICAL|"auth.go:42"|token never expires|check expiry`),
				row(`LOW|util.go|unused var|""`),
				row(`MEDIUM|"cache.go:9"|stale entry|invalidate on write`),
			},
			wantFastOK: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := fastFixtureHeader(tc.fields, tc.delim)
			fastOut, fastOK, _, _ := runBothDecoders(t, h, tc.rows)
			if fastOK != tc.wantFastOK {
				t.Fatalf("decodeRowsFast ok = %v, want %v (out=%#v)", fastOK, tc.wantFastOK, fastOut)
			}
		})
	}
}

// TestDecodeRowsFast_RandomizedAgreement is the "wide range of fixtures"
// generator: it builds pseudo-random rows out of a pool that deliberately
// includes every category from the table above (plain tokens, quoted-no-
// escape tokens, escapes, embedded colons, mismatched widths, leading
// zeros, unicode) and asserts ONLY the safety property that matters --
// whenever decodeRowsFast says ok, it must match decodeRowsGeneric exactly.
// It does not assert which fixtures activate the fast path, so it stays
// correct as eligibility rules are tuned.
func TestDecodeRowsFast_RandomizedAgreement(t *testing.T) {
	tokenPool := []string{
		"plain", "CRITICAL", "007", "-0", "0.50", "1e3", "true", "false", "null", "",
		`"quoted"`, `"file:line"`, `"a|b"`, `"  padded  "`,
		`"escaped\"quote"`, `"escaped\\slash"`, `"escaped\nline"`,
		"café", "日本語",
		"see line 42: fix it",
		`"ab"cd`, `ab"cd"`,
		"a\\b",
		"9007199254740993",
	}
	delims := []rune{'|', ',', '\t'}

	rng := rand.New(rand.NewSource(42))
	fastActivations := 0
	const iterations = 500
	for i := 0; i < iterations; i++ {
		delim := delims[rng.Intn(len(delims))]
		numFields := 1 + rng.Intn(4)
		fields := make([]string, numFields)
		for f := range fields {
			fields[f] = fmt.Sprintf("f%d", f)
		}
		h := fastFixtureHeader(fields, delim)

		numRows := 1 + rng.Intn(3)
		rows := make([]string, numRows)
		for r := range rows {
			tokens := make([]string, numFields)
			for c := range tokens {
				tokens[c] = tokenPool[rng.Intn(len(tokenPool))]
			}
			rows[r] = row(strings.Join(tokens, string(delim)))
		}

		fastOut, fastOK, _, _ := runBothDecoders(t, h, rows)
		if fastOK {
			fastActivations++
			_ = fastOut
		}
	}
	if fastActivations == 0 {
		t.Fatal("fast path never activated across the randomized suite -- the pool or the implementation is broken")
	}
	t.Logf("fast path activated on %d/%d randomized fixtures", fastActivations, iterations)
}
