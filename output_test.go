package goaxi

import (
	"encoding/json"
	"strings"
	"testing"

	toon "github.com/toon-format/toon-go"
)

// --- help[] ----------------------------------------------------------------

// Three incompatible help formats exist across the projects, and a probe of the
// pinned codec showed two of them DO NOT DECODE:
//
//	help[2]: a,b          -> decodes            (the inline array toon-go emits)
//	help[2]:\n  a\n  b    -> "list length mismatch"   (the AXI spec's example)
//	help[] a              -> "missing colon after key" (cadence's current form)
//
// So the canonical form is the inline array, and the test that matters is not
// "does it look right" but "does the decoder read it back".
func TestWriteHelp_RoundTripsThroughTheDecoder(t *testing.T) {
	var b strings.Builder
	if err := WriteHelp(&b, []string{"Run `x view <id>`", "Run `x create --title \"...\"`"}); err != nil {
		t.Fatalf("WriteHelp: %v", err)
	}

	got, err := toon.DecodeString(strings.TrimRight(b.String(), "\n"))
	if err != nil {
		t.Fatalf("the help block must decode with the same codec that wrote it: %v (payload %q)", err, b.String())
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("help must decode to a map, got %T", got)
	}
	lines, ok := m["help"].([]any)
	if !ok {
		t.Fatalf("help must decode to a list under the key \"help\", got %#v", m)
	}
	if len(lines) != 2 {
		t.Fatalf("both help lines must survive, got %#v", lines)
	}
	if lines[0] != "Run `x view <id>`" {
		t.Errorf("line 1 corrupted: %#v", lines[0])
	}
	if lines[1] != `Run `+"`"+`x create --title "..."`+"`" {
		t.Errorf("line 2 corrupted (quoting must survive): %#v", lines[1])
	}
}

// A command with no meaningful next step must emit nothing rather than a stray
// empty section. An agent reading "help[0]:" learns nothing and pays tokens.
func TestWriteHelp_EmptyWritesNothing(t *testing.T) {
	for _, lines := range [][]string{nil, {}} {
		var b strings.Builder
		if err := WriteHelp(&b, lines); err != nil {
			t.Fatalf("WriteHelp: %v", err)
		}
		if b.String() != "" {
			t.Errorf("an empty help list must write nothing, got %q", b.String())
		}
	}
}

// Help text is the one place a command echoes values back to the agent, so it is
// the most likely place for a control byte to arrive. It must be sanitized like
// any other output.
func TestWriteHelp_SanitizesControlBytes(t *testing.T) {
	var b strings.Builder
	if err := WriteHelp(&b, []string{"Run \x1b[31mx\x1b[0m view"}); err != nil {
		t.Fatalf("WriteHelp: %v", err)
	}
	if strings.Contains(b.String(), "\x1b") {
		t.Errorf("a control byte must never reach the help block, got %q", b.String())
	}
	if !strings.Contains(b.String(), "view") {
		t.Errorf("visible text must survive, got %q", b.String())
	}
}

// Appending help after a body must not break the body. A probe confirmed the
// combined payload decodes into one map carrying both.
func TestWriteHelp_AppendsToABodyWithoutBreakingIt(t *testing.T) {
	var b strings.Builder
	if err := Encode(&b, map[string]any{
		"count": 2,
		"rows": []any{
			map[string]any{"id": 1, "name": "a"},
			map[string]any{"id": 2, "name": "b"},
		},
	}); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if err := WriteHelp(&b, []string{"Run `x view <id>`"}); err != nil {
		t.Fatalf("WriteHelp: %v", err)
	}

	got, err := toon.DecodeString(strings.TrimRight(b.String(), "\n"))
	if err != nil {
		t.Fatalf("body+help must decode as one payload: %v (payload %q)", err, b.String())
	}
	m := got.(map[string]any)
	if m["count"] != 2 {
		t.Errorf("the body must survive the appended help, got count=%#v", m["count"])
	}
	if _, ok := m["help"]; !ok {
		t.Errorf("the help block must be present alongside the body, got %#v", m)
	}
}

// --- EncodeOrJSON ----------------------------------------------------------

// AC4 originally said a consumer tells the formats apart by first byte, treating
// '{' or '[' as JSON. A probe disproved half of that: four TOON shapes start
// with '[' — "[0]:", "[2]: a,b", "[2]: 1,2" and "[1]{id}:". Only '{' is safe,
// and the envelope makes that explicit by putting axi_format first, so a payload
// literally begins with {"axi_format".
func TestEncodeOrJSON_LosslessValueEmitsTOON(t *testing.T) {
	var b strings.Builder
	if err := EncodeOrJSON(&b, map[string]any{"rows": []any{
		map[string]any{"id": 1, "name": "a"},
	}}); err != nil {
		t.Fatalf("EncodeOrJSON: %v", err)
	}
	out := b.String()
	if strings.HasPrefix(out, "{") {
		t.Errorf("a lossless value must be emitted as TOON, not the JSON envelope: %q", out)
	}
	if !strings.Contains(out, "rows") {
		t.Errorf("payload must carry the data, got %q", out)
	}
}

func TestEncodeOrJSON_LossyValueFallsBackToASelfDescribingEnvelope(t *testing.T) {
	var b strings.Builder
	if err := EncodeOrJSON(&b, map[string]any{"m": textMarshaler{v: "PAYLOAD"}}); err != nil {
		t.Fatalf("EncodeOrJSON: %v", err)
	}
	out := b.String()

	if !strings.HasPrefix(out, `{"axi_format"`) {
		t.Errorf("the envelope must begin with {\"axi_format\" so a consumer can route it unambiguously, got %q", out)
	}

	var env struct {
		Format string          `json:"axi_format"`
		Notice string          `json:"axi_notice"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("the fallback must be valid JSON: %v (payload %q)", err, out)
	}
	if env.Format != "json" {
		t.Errorf("axi_format must say json, got %q", env.Format)
	}
	if env.Notice == "" {
		t.Error("axi_notice must explain WHY TOON was unsafe; a bare fallback is undiagnosable")
	}
	if !strings.Contains(env.Notice, "TextMarshaler") {
		t.Errorf("the notice must name the offending construct, got %q", env.Notice)
	}
	if len(env.Data) == 0 {
		t.Error("the envelope must still carry the data")
	}
}

// The fallback must never be reached for data that merely costs more as TOON.
// Efficiency is a cost signal; routing on it would change the output format
// based on payload size, which no consumer could predict.
func TestEncodeOrJSON_InefficientButLosslessStaysTOON(t *testing.T) {
	var b strings.Builder
	ragged := map[string]any{"rows": []any{
		map[string]any{"id": 1, "name": "a"},
		map[string]any{"id": 2, "extra": "b", "more": "c"},
	}}
	if got := Check(ragged); got.Efficient {
		t.Skip("fixture is no longer inefficient; the test needs a new one")
	}
	if err := EncodeOrJSON(&b, ragged); err != nil {
		t.Fatalf("EncodeOrJSON: %v", err)
	}
	if strings.HasPrefix(b.String(), "{") {
		t.Errorf("an inefficient but lossless value must stay TOON, got the envelope: %q", b.String())
	}
}

// Encode sanitizes. A caller should not have to remember to do it, because the
// one time they forget is the time a control byte reaches a terminal.
func TestEncode_SanitizesAndTerminatesWithOneNewline(t *testing.T) {
	var b strings.Builder
	if err := Encode(&b, map[string]any{"f": "a\x1bb"}); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out := b.String()
	if strings.Contains(out, "\x1b") {
		t.Errorf("Encode must sanitize, got %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("output must be newline-terminated, got %q", out)
	}
	if strings.HasSuffix(out, "\n\n") {
		t.Errorf("output must end with exactly one newline, got %q", out)
	}
}

// A value that cannot be encoded at all must report an error rather than write a
// partial payload. A truncated payload is worse than none: it parses.
func TestEncode_RefusesRatherThanWritePartialOutput(t *testing.T) {
	var b strings.Builder
	err := Encode(&b, map[string]any{"k": definedString("x")})
	if err == nil {
		t.Fatalf("an unencodable value must error, got output %q", b.String())
	}
	if b.String() != "" {
		t.Errorf("nothing may be written when encoding fails, got %q", b.String())
	}
}
