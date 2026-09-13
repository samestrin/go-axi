package goaxi

import (
	"bytes"
	"strings"
	"testing"
)

// dualTagged carries both tag sets, the way cadence-axi tags every type.
type dualTagged struct {
	Count int    `json:"count" toon:"count"`
	File  string `json:"file"  toon:"file"`
}

// jsonTagged carries only `json` tags, the way every result type in llm-tools
// is written. Encoded directly this publishes Go identifiers; projected it does
// not. That difference is the whole reason EncodeProjected exists.
type jsonTagged struct {
	Count int    `json:"count"`
	File  string `json:"file"`
}

func TestParseFormat(t *testing.T) {
	cases := []struct {
		in      string
		want    Format
		wantErr bool
	}{
		{"", Default, false},
		{"toon", TOON, false},
		{"json", JSON, false},
		{"TOON", TOON, false},
		{"  json  ", JSON, false},
		{"yaml", "", true},
		{"tone", "", true},
	}
	for _, c := range cases {
		got, err := ParseFormat(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseFormat(%q): want error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseFormat(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseFormat(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseFormat_EmptySelectsDefaultTOON(t *testing.T) {
	// An unset flag must pass straight through without the caller special-casing
	// it, and the default must be TOON per AXI principle 1.
	got, err := ParseFormat("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != TOON {
		t.Errorf("ParseFormat(\"\") = %q, want %q", got, TOON)
	}
}

func TestFormatEncode_TOON(t *testing.T) {
	var buf bytes.Buffer
	if err := TOON.Encode(&buf, dualTagged{Count: 3, File: "a.go"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "count: 3\nfile: a.go\n"
	if buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
}

func TestFormatEncode_JSON(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON.Encode(&buf, dualTagged{Count: 3, File: "a.go"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"count":3,"file":"a.go"}` + "\n"
	if buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
}

func TestFormatEncode_UnknownFormatIsRefused(t *testing.T) {
	var buf bytes.Buffer
	err := Format("yaml").Encode(&buf, dualTagged{})
	if err == nil {
		t.Fatal("want an error for an unknown format, got nil")
	}
	if buf.Len() != 0 {
		t.Errorf("wrote %q on a refused format; must write nothing", buf.String())
	}
}

func TestFormatEncode_TerminatesWithExactlyOneNewline(t *testing.T) {
	for _, f := range []Format{TOON, JSON} {
		var buf bytes.Buffer
		if err := f.Encode(&buf, dualTagged{Count: 1, File: "x"}); err != nil {
			t.Fatalf("%s: unexpected error: %v", f, err)
		}
		s := buf.String()
		if !strings.HasSuffix(s, "\n") {
			t.Errorf("%s: output does not end in a newline: %q", f, s)
		}
		if strings.HasSuffix(s, "\n\n") {
			t.Errorf("%s: output ends in two newlines: %q", f, s)
		}
	}
}

func TestFormatEncode_SanitizesBeforeTOON(t *testing.T) {
	// toon-go errors on a raw ANSI escape rather than emitting it, so without
	// sanitizing, a command handed coloured text would print nothing at all.
	var buf bytes.Buffer
	err := TOON.Encode(&buf, dualTagged{Count: 1, File: "\x1b[31mred\x1b[0m"})
	if err != nil {
		t.Fatalf("escape sequence must be sanitized, not fail the encode: %v", err)
	}
	if strings.Contains(buf.String(), "\x1b") {
		t.Errorf("escape survived into output: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "red") {
		t.Errorf("visible text was dropped along with the escape: %q", buf.String())
	}
}

func TestEncodeProjected_UsesJSONTagsWhenNoToonTagExists(t *testing.T) {
	// The load-bearing test. Probed against the pinned toon-go: marshalling a
	// struct with only `json` tags emits the Go identifiers "Count" and "File".
	// Projecting through JSON first is what makes one tag set serve both formats.
	var buf bytes.Buffer
	if err := TOON.EncodeProjected(&buf, jsonTagged{Count: 3, File: "a.go"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, "Count") || strings.Contains(got, "File") {
		t.Errorf("projected output published Go identifiers: %q", got)
	}
	want := "count: 3\nfile: a.go\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEncodeProjected_MatchesDirectWhenDualTagged(t *testing.T) {
	// A dual-tagged type must encode identically either way, or the two entry
	// points would be two contracts rather than one with a cheaper path.
	v := dualTagged{Count: 7, File: "internal/x.go"}

	var direct, projected bytes.Buffer
	if err := TOON.Encode(&direct, v); err != nil {
		t.Fatalf("direct: %v", err)
	}
	if err := TOON.EncodeProjected(&projected, v); err != nil {
		t.Fatalf("projected: %v", err)
	}
	if direct.String() != projected.String() {
		t.Errorf("paths disagree:\n direct   = %q\n projected= %q", direct.String(), projected.String())
	}
}

func TestEncodeProjected_KeepsIntegersBeyondFloat64Exactly(t *testing.T) {
	// Measured in llm-tools before this guard existed: --json printed
	// 9007199254740993 while the TOON path printed 9007199254740992, so the two
	// flags disagreed about the data. A plain Unmarshal into `any` rounds every
	// number through float64.
	type big struct {
		Size int64 `json:"size"`
	}
	var buf bytes.Buffer
	if err := TOON.EncodeProjected(&buf, big{Size: 9007199254740993}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "9007199254740993") {
		t.Errorf("large integer lost its digits: %q", buf.String())
	}
}

func TestEncodeProjected_LeavesOrdinaryIntegersNumeric(t *testing.T) {
	// In-range numbers must stay numeric. Rendering every integer as text would
	// keep the digits but break agreement with JSON for the common payload.
	var buf bytes.Buffer
	if err := TOON.EncodeProjected(&buf, jsonTagged{Count: 42, File: "a"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "count: 42") {
		t.Errorf("ordinary integer was not numeric: %q", buf.String())
	}
}

func TestEncodeProjected_JSONMatchesPlainJSON(t *testing.T) {
	var projected, plain bytes.Buffer
	v := jsonTagged{Count: 2, File: "b.go"}
	if err := JSON.EncodeProjected(&projected, v); err != nil {
		t.Fatalf("projected: %v", err)
	}
	if err := JSON.Encode(&plain, v); err != nil {
		t.Fatalf("plain: %v", err)
	}
	if projected.String() != plain.String() {
		t.Errorf("JSON paths disagree:\n projected= %q\n plain    = %q", projected.String(), plain.String())
	}
}
