package goaxi

import (
	"strconv"
	"strings"
)

// decodeRowsFast is the Tier 2 fast path: it tries to build rows directly
// from the already-collected row text, skipping toon-go's generic decoder
// (synthesize + toon.DecodeString + arrayOf + projectValue) entirely.
//
// It is all-or-nothing across the whole document: if ANY row cannot be
// PROVEN to decode identically to decodeRowsGeneric, the whole document
// declines (ok == false) and DecodeTabular falls back to that proven path.
// Mixing fast rows with generic rows in one document is deliberately not
// attempted -- it would multiply the cases the differential test has to
// cover for no benefit real findings payloads need.
//
// Eligibility line: no backslash anywhere in the row. With no backslash
// present, toon-go's SplitInlineValues never enters its escape-handling
// branch and its UnquoteString never enters its escape branch either, so a
// quote-aware byte scan that just strips the surrounding quote marks off a
// quoted token reproduces their combined output exactly -- covering both
// plain rows and the atcr shape (one quoted "file:line"-style column) that
// motivated widening the fast path past unquoted-only rows.
func decodeRowsFast(h *header, rows []string) ([]map[string]string, bool) {
	// parseHeader only ever accepts ',', '|' or '\t' as h.delimiter, all of
	// which fit in one byte, so truncating the rune to a byte is exact.
	delim := byte(h.delimiter)

	// decodeRowsGeneric leaves out nil when it appends zero rows (`var out
	// []map[string]string`); returning a non-nil empty slice here for the same
	// input would flip doc.Rows from nil to [] for a clean `findings[0]:`
	// payload, breaking `Rows == nil` checks and JSON's null-vs-[] output.
	if len(rows) == 0 {
		return nil, true
	}

	var tokenArr [16]string
	var tokens []string
	if len(h.fields) <= len(tokenArr) {
		tokens = tokenArr[:len(h.fields)]
	} else {
		tokens = make([]string, len(h.fields))
	}

	out := make([]map[string]string, 0, len(rows))
	for _, r := range rows {
		content := strings.TrimSpace(r)
		if content == "" {
			// toon-go's generic parseArray SKIPS a blank line rather than
			// treating it as a one-empty-token row, which changes how many
			// rows end up decoded. DecodeTabular's own collection loop
			// already drops blank rows before rows ever reaches this
			// function, so a real caller never hits this — but decodeRowsFast
			// must not assume that and silently disagree if it ever did.
			return nil, false
		}
		n, ok := fastSplitRow(content, delim, tokens)
		if !ok || n != len(h.fields) {
			return nil, false
		}
		rowOut := make(map[string]string, len(h.fields))
		for i, field := range h.fields {
			val, ok := fastProjectToken(tokens[i])
			if !ok {
				return nil, false
			}
			rowOut[field] = val
		}
		out = append(out, rowOut)
	}
	return out, true
}

// fastSplitRow splits one row's trimmed content on delim into dst, honouring
// quotes exactly like toon-go's SplitInlineValues does when no escape ever fires
// (guaranteed by the backslash check below) -- a delimiter or a colon byte
// inside quotes is not a boundary. It writes directly into dst to avoid slice
// heap allocations, and reports ok == false the moment it sees anything the
// fast path must not handle: a backslash anywhere, an unquoted colon, or more
// fields than dst can hold.
func fastSplitRow(content string, delim byte, dst []string) (int, bool) {
	start := 0
	inQuotes := false
	count := 0
	maxFields := len(dst)
	for i := 0; i < len(content); i++ {
		switch c := content[i]; {
		case c == '\\':
			return 0, false
		case c == '"':
			inQuotes = !inQuotes
		case c == ':' && !inQuotes:
			return 0, false
		case c == delim && !inQuotes:
			if count >= maxFields {
				return 0, false
			}
			dst[count] = strings.TrimSpace(content[start:i])
			count++
			start = i + 1
		}
	}
	if inQuotes {
		return 0, false
	}
	if count >= maxFields {
		return 0, false
	}
	dst[count] = strings.TrimSpace(content[start:])
	count++
	return count, true
}

func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

// isPlainNonNegativeInt checks if token is a plain non-negative integer
// (single '0', or 1-15 digits starting with '1'-'9'). For any such integer,
// strconv.FormatFloat(float64(n), 'f', -1, 64) is mathematically guaranteed to
// reproduce token identically, allowing ParseFloat and FormatFloat to be skipped.
func isPlainNonNegativeInt(token string) bool {
	if len(token) == 0 || len(token) > 15 {
		return false
	}
	if token[0] == '0' {
		return len(token) == 1
	}
	for i := 0; i < len(token); i++ {
		if !isASCIIDigit(token[i]) {
			return false
		}
	}
	return true
}

// fastProjectToken merges decodePrimitiveToken and projectValue (both in
// toon-go/decode.go's doc-comment lineage) into one string-to-string step,
// skipping the `any` boxing that makes the generic path allocate. It returns
// ok == false only when it cannot prove its result matches the generic
// path: a quote that does not simply wrap the token (decodePrimitiveToken
// would hand that to UnquoteString, which errors), or a numeric-looking
// token whose ParseFloat overflows (also an error on the generic path).
// Falling back to the generic path in both cases reproduces that error
// exactly instead of guessing at its text.
func fastProjectToken(token string) (string, bool) {
	if token == "" {
		return "", true
	}
	if token[0] == '"' {
		if len(token) < 2 || token[len(token)-1] != '"' {
			return "", false
		}
		return token[1 : len(token)-1], true
	}
	switch token {
	case "true":
		return "true", true
	case "false":
		return "false", true
	case "null":
		return "null", true
	}
	first := token[0]
	if (first < '0' || first > '9') && first != '-' {
		return token, true
	}
	if hasForbiddenLeadingZerosFast(token) {
		return token, true
	}
	if isPlainNonNegativeInt(token) {
		return token, true
	}
	if looksNumericFast(token) {
		num, err := strconv.ParseFloat(token, 64)
		if err != nil {
			return "", false
		}
		if num == 0 {
			num = 0 // normalizes -0 to 0, matching toon-go/internal/codec.decodePrimitiveToken
		}
		return strconv.FormatFloat(num, 'f', -1, 64), true
	}
	return token, true
}

// hasForbiddenLeadingZerosFast mirrors
// toon-go/internal/codec.hasForbiddenLeadingZeros exactly (that function is
// unexported, so it cannot be called directly). unicode.IsDigit(rune(b)) on
// a raw byte b is equivalent to an ASCII digit check here: the only Unicode
// "Nd" codepoint below 256 is the ASCII range '0'-'9', so simplifying to a
// byte comparison changes nothing.
func hasForbiddenLeadingZerosFast(token string) bool {
	if len(token) < 2 {
		return false
	}
	if token[0] != '0' && (len(token) <= 1 || token[0] != '-' || token[1] != '0') {
		return false
	}
	for i := 0; i < len(token); i++ {
		if c := token[i]; c == '.' || c == 'e' || c == 'E' {
			return false
		}
	}
	if token[0] == '-' {
		return len(token) > 2 && token[1] == '0' && isASCIIDigit(token[2])
	}
	return isASCIIDigit(token[1])
}

// looksNumericFast mirrors toon-go/internal/format.LooksNumeric exactly
// (also unexported); it already scans byte-by-byte in the original, so this
// is a direct copy rather than an approximation.
func looksNumericFast(s string) bool {
	if len(s) == 0 {
		return false
	}
	i := 0
	if s[0] == '-' {
		i++
		if i == len(s) {
			return false
		}
	}
	digits := 0
	for i < len(s) && isASCIIDigit(s[i]) {
		i++
		digits++
	}
	if digits == 0 {
		return false
	}
	if i < len(s) && s[i] == '.' {
		i++
		if i == len(s) || !isASCIIDigit(s[i]) {
			return false
		}
		for i < len(s) && isASCIIDigit(s[i]) {
			i++
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		if i == len(s) || !isASCIIDigit(s[i]) {
			return false
		}
		for i < len(s) && isASCIIDigit(s[i]) {
			i++
		}
	}
	return i == len(s)
}
