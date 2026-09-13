package goaxi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Format is an output encoding chosen by the caller.
//
// The choice is explicit rather than detected. Output shape is a property of the
// command, not of the data: a format that varied per call would force every
// consumer to handle both, and would make an instruction like "the third column
// is SEVERITY" unsafe to write down. EncodeOrJSON is the opposite policy and
// exists for callers that have no design-time rule.
type Format string

const (
	// TOON is the token-efficient default of AXI principle 1.
	TOON Format = "toon"

	// JSON is the escape hatch for genuinely nested or non-uniform output.
	JSON Format = "json"
)

// Default is the format used when the caller expresses no preference.
const Default = TOON

// ParseFormat maps a flag value to a Format.
//
// An empty string selects Default, so an unset flag can be passed straight
// through without the caller special-casing it. Matching is case-insensitive and
// ignores surrounding spaces, because the value usually arrives from a shell.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return Default, nil
	case string(TOON):
		return TOON, nil
	case string(JSON):
		return JSON, nil
	default:
		return "", fmt.Errorf("goaxi: unknown output format %q: expected toon or json", s)
	}
}

// Encode writes v to w in the receiver's format, terminated by exactly one
// newline, in a single Write call.
//
// TOON is sanitized first, which is load-bearing rather than cosmetic: toon-go
// errors on a raw ANSI escape instead of emitting it, so without sanitizing, a
// command handed coloured text would fail outright on data it should simply
// print. JSON is marshalled as-is; encoding/json escapes control bytes itself.
//
// Field names come from struct tags — `toon` for TOON and `json` for JSON.
// toon-go does NOT fall back to the `json` tag, so a type carrying only `json`
// tags publishes its Go identifiers here. Use CheckTags to catch that in a test,
// or EncodeProjected to encode from the `json` tags instead.
//
// Nothing is written when encoding fails, including on an unknown format. A
// partial payload is worse than none, because it parses.
func (f Format) Encode(w io.Writer, v any) error {
	switch f {
	case TOON:
		// The package-level Encode already sanitizes, marshals, and writes the
		// payload and its terminator in one call. Reused rather than repeated so
		// there is one implementation of "nothing on failure" to keep correct.
		return Encode(w, v)
	case JSON:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("goaxi: encoding JSON: %w", err)
		}
		return writeLine(w, b)
	default:
		return fmt.Errorf("goaxi: unknown output format %q: expected toon or json", f)
	}
}

// EncodeProjected writes v the way Encode does, but derives TOON field names
// from the `json` struct tags rather than requiring a second `toon` tag.
//
// WHY THIS EXISTS. toon-go does not read the `json` tag. A codebase whose result
// types carry only `json` tags therefore has two options: add a duplicate `toon`
// tag to every field, or route the value through its JSON form first. The first
// is what cadence-axi does — 102 tags, maintained by hand, where a single
// omission silently publishes a Go identifier. The second is this function.
//
// It is not free. Medians of six runs on a 500-row payload: 257us and 5,427
// allocations direct, against 665us and 15,055 projected — about 2.6x the time
// and 2.8x the allocations, because the value is marshalled and unmarshalled
// once more. Encode remains the fast path and the default; this is the
// convenience. Reproduce with:
//
//	go test -run '^$' -bench Format_Encode -benchmem
//
// Projection applies to TOON only. encoding/json already reads the `json` tag,
// so a JSON payload has nothing to correct and is passed to Encode unchanged —
// routing it through a generic map would also reorder its keys, since struct
// fields marshal in declaration order and maps in sorted order.
//
// Going through JSON additionally normalises values to the shapes toon-go
// handles, so a type that marshals specially — time.Time, a json.Marshaler —
// reaches the encoder looking the way JSON output would have shown it.
func (f Format) EncodeProjected(w io.Writer, v any) error {
	if f != TOON {
		return f.Encode(w, v)
	}
	projected, err := projectJSON(v)
	if err != nil {
		return fmt.Errorf("goaxi: projecting through JSON: %w", err)
	}
	return Encode(w, projected)
}

// projectJSON renders v through its JSON form so the `json` struct tags become
// the field names, and normalises it to the types toon-go handles: maps, slices,
// strings, float64, bool and nil.
func projectJSON(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	// UseNumber, because unmarshalling into `any` turns every number into a
	// float64 and silently rounds anything past 2^53 — file sizes and byte
	// totals reach that range. Measured in llm-tools before this guard: the JSON
	// path printed 9007199254740993 while the TOON path printed 9007199254740992,
	// so the two formats disagreed about the data.
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var projected any
	if err := dec.Decode(&projected); err != nil {
		return nil, err
	}
	return exactifyNumbers(projected), nil
}

// maxExactInt is the largest integer a float64 represents exactly.
const maxExactInt = 1 << 53

// exactifyNumbers converts the integers float64 cannot hold exactly into their
// verbatim digits, and leaves every other number numeric.
//
// json.Number alone does not solve this: the encoder re-parses it back into a
// number and the digits die again. Carrying only the out-of-range values across
// as text is also what toon-go does natively when it encodes a large int64
// directly, so this matches the encoder's own convention rather than inventing
// one.
//
// In-range numbers stay numeric deliberately. Rendering every integer as text
// would keep the digits but break agreement with JSON for every ordinary
// payload, which is the far more common case.
func exactifyNumbers(v any) any {
	switch t := v.(type) {
	case json.Number:
		s := t.String()
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			if i > maxExactInt || i < -maxExactInt {
				return s
			}
			return float64(i)
		}
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			if u > maxExactInt {
				return s
			}
			return float64(u)
		}
		f, err := t.Float64()
		if err != nil {
			// Not representable as a float either; the digits are all there is.
			return s
		}
		return f
	case map[string]any:
		for k, vv := range t {
			t[k] = exactifyNumbers(vv)
		}
		return t
	case []any:
		for i, vv := range t {
			t[i] = exactifyNumbers(vv)
		}
		return t
	}
	return v
}
