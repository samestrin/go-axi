package goaxi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// Compared by value, not by Go type. Which numeric type the decoder returns
	// is its own business, and pinning it here would make this test fail on a
	// codec change that broke nothing.
	count, ok := m["count"]
	if !ok {
		t.Fatalf("the body must survive the appended help, got %#v", m)
	}
	if fmt.Sprint(count) != "2" {
		t.Errorf("body value corrupted by the appended help: count=%#v (%T)", count, count)
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

// Hostile-review finding: Check judged the RAW value while Encode emitted the
// SANITIZED one, so the two disagreed. A string carrying an ANSI escape makes
// toon.Marshal fail, so Check called a fine value lossy and advised "declare it
// as a type alias" — advice unrelated to the real cause — and EncodeOrJSON then
// emitted a JSON envelope for a value TOON handles perfectly.
func TestEncodeOrJSON_SanitizableValueStaysTOON(t *testing.T) {
	v := map[string]any{"f": "\x1b[31mred\x1b[0m"}

	if got := Check(v); !got.OK {
		t.Errorf("a value that only needs sanitizing must not be reported lossy, got %q", got.Reason)
	}

	var b strings.Builder
	if err := EncodeOrJSON(&b, v); err != nil {
		t.Fatalf("EncodeOrJSON: %v", err)
	}
	if strings.HasPrefix(b.String(), "{") {
		t.Errorf("a sanitizable value must stay TOON, got the envelope: %q", b.String())
	}
	if strings.Contains(b.String(), "\x1b") {
		t.Errorf("the control byte must be stripped, got %q", b.String())
	}
}

// The JSON fallback must sanitize its payload too. An escape hatch that emits
// the bytes the main path strips is worse than no escape hatch, because it is
// reached exactly when something already went wrong.
func TestEncodeOrJSON_FallbackSanitizesItsData(t *testing.T) {
	var b strings.Builder
	err := EncodeOrJSON(&b, map[string]any{
		"m":    textMarshaler{v: "x"},
		"text": "bad\x1bhere",
	})
	if err != nil {
		t.Fatalf("EncodeOrJSON: %v", err)
	}
	out := b.String()

	if !strings.HasPrefix(out, `{"axi_format"`) {
		t.Fatalf("expected the JSON envelope, got %q", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("the fallback must not leak a raw control byte, got %q", out)
	}
	// JSON escapes a control byte as  rather than emitting it raw; neither
	// form may appear, since both put the sequence back together downstream.
	if strings.Contains(out, ``) {
		t.Errorf("the fallback must strip the control byte, not escape it, got %q", out)
	}
	if !strings.Contains(out, "badhere") {
		t.Errorf("visible text must survive in the fallback, got %q", out)
	}
}

// A cyclic value must fail safely rather than crash. Sanitize has no cycle
// guard, so the fallback deliberately hands the raw value to encoding/json,
// which detects the cycle and returns an error.
func TestEncodeOrJSON_CyclicValueErrorsRatherThanCrashes(t *testing.T) {
	m := map[string]any{"name": "root"}
	m["self"] = m

	var b strings.Builder
	err := EncodeOrJSON(&b, m)
	if err == nil {
		t.Fatalf("a cyclic value must report an error, got output %q", b.String())
	}
}

// --- write errors ----------------------------------------------------------

// failingWriter fails every write.
type failingWriter struct{ err error }

func (f failingWriter) Write([]byte) (int, error) { return 0, f.err }

// countingWriter records how many times Write was called, so a test can pin
// that output lands in exactly one call rather than two.
type countingWriter struct {
	writes int
	n      int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.writes++
	w.n += len(p)
	return len(p), nil
}

// A write error must propagate rather than be swallowed. This is not a
// hypothetical: an agent piping output into `head` closes the pipe, and a
// swallowed EPIPE means the caller believes it emitted a complete payload when
// the consumer received a truncated one.
func TestWriteErrorsPropagate(t *testing.T) {
	boom := errors.New("broken pipe")

	cases := []struct {
		name string
		call func(io.Writer) error
	}{
		{"Encode", func(w io.Writer) error { return Encode(w, map[string]any{"f": "x"}) }},
		{"WriteHelp", func(w io.Writer) error { return WriteHelp(w, []string{"Run x"}) }},
		{"EncodeOrJSON lossless", func(w io.Writer) error {
			return EncodeOrJSON(w, map[string]any{"f": "x"})
		}},
		{"EncodeOrJSON fallback", func(w io.Writer) error {
			return EncodeOrJSON(w, map[string]any{"m": textMarshaler{v: "x"}})
		}},
	}
	for _, c := range cases {
		t.Run(c.name+" body write", func(t *testing.T) {
			if err := c.call(failingWriter{err: boom}); !errors.Is(err, boom) {
				t.Errorf("a failed body write must propagate, got %v", err)
			}
		})
		// Stronger than the subtest this replaces, which asserted that a SECOND
		// write could fail. The body and its terminator now go out together, so
		// there is no second write to fail — and no window where a consumer sees
		// a payload whose last byte never arrived.
		t.Run(c.name+" writes exactly once", func(t *testing.T) {
			w := &countingWriter{}
			if err := c.call(w); err != nil {
				t.Fatalf("call must succeed against a working writer: %v", err)
			}
			if w.writes != 1 {
				t.Errorf("output must land in exactly 1 Write call, got %d", w.writes)
			}
			if w.n == 0 {
				t.Error("something must actually be written")
			}
		})
	}
}

// A body that already ends in a newline must not gain a second one. toon-go does
// not emit trailing newlines today, so nothing exercises this in practice — but
// the contract is "exactly one", and an encoder change should not silently
// double-terminate every payload in every consumer.
func TestWriteLine_AlreadyTerminatedBodyIsNotDoubled(t *testing.T) {
	const body = "f: x\n"
	w := &countingWriter{}
	if err := writeLine(w, []byte(body)); err != nil {
		t.Fatalf("writeLine: %v", err)
	}
	if w.writes != 1 {
		t.Errorf("must still be a single write, got %d", w.writes)
	}
	if w.n != len(body) {
		t.Errorf("no extra byte may be appended: wrote %d bytes, want %d", w.n, len(body))
	}
}

// An empty payload writes nothing at all, not a bare newline.
func TestWriteLine_EmptyBodyWritesNothing(t *testing.T) {
	w := &countingWriter{}
	if err := writeLine(w, nil); err != nil {
		t.Fatalf("writeLine: %v", err)
	}
	if w.writes != 0 || w.n != 0 {
		t.Errorf("an empty body must write nothing, got %d write(s) of %d byte(s)", w.writes, w.n)
	}
}

// --- output path cost ------------------------------------------------------

// jsonSizeProbe counts how many times encoding/json reaches it.
//
// It implements json.Marshaler ONLY, never encoding.TextMarshaler, so the two
// lossy walks must not flag it and the value stays on the lossless TOON path.
// Confirmed by probe: Check reports OK at tier "nested".
//
// This is the only way to observe the size comparison from outside the package.
// Verdict.Efficient is computed by marshalling the entire payload a second time
// as JSON purely to compare byte counts, and nothing on the output path reads
// the answer.
type jsonSizeProbe struct {
	hits *int
	Name string `toon:"name" json:"name"`
}

func (p jsonSizeProbe) MarshalJSON() ([]byte, error) {
	*p.hits++
	return []byte(`"probed"`), nil
}

// EncodeOrJSON must not pay for a TOON-vs-JSON size comparison it never reads.
//
// Check computes Verdict.Efficient by marshalling the whole payload as JSON. That
// number is a real diagnostic for a caller asking "would JSON be cheaper for this
// command", and cadence asserts on it at 20 sites, so Check must keep computing
// it. But EncodeOrJSON routes on verdict.OK alone and discards Efficient. On a
// 2000-row listing that throwaway marshal measured 0.56ms and 14,008 allocations.
func TestEncodeOrJSON_DoesNotMeasureSizeOnTheLosslessPath(t *testing.T) {
	hits := 0
	v := map[string]any{
		"path":  "/some/dir",
		"probe": jsonSizeProbe{hits: &hits, Name: "row"},
	}

	// Guard the probe itself. If this fixture ever stops being lossless the test
	// would be asserting about the envelope branch and could pass for the wrong
	// reason.
	if got := Check(v); !got.OK {
		t.Fatalf("fixture must stay lossless or the probe watches the wrong branch, got %q", got.Reason)
	}
	if hits == 0 {
		t.Fatal("probe is broken: Check must reach MarshalJSON, or there is nothing here to observe")
	}

	hits = 0
	if err := EncodeOrJSON(io.Discard, v); err != nil {
		t.Fatalf("EncodeOrJSON: %v", err)
	}
	if hits != 0 {
		t.Errorf("EncodeOrJSON must not marshal the payload as JSON to compare sizes; MarshalJSON was called %d time(s)", hits)
	}
}

// EncodeOrJSON must marshal the payload as TOON once, not twice.
//
// CheckSanitized marshals to reach its verdict and throws the bytes away;
// encodeSanitized then marshals the identical value again in order to write it.
// Measured on a 2000-row listing, the duplicate cost 0.97ms and 33,939
// allocations.
//
// Counted in allocations rather than wall time because allocation counts are
// deterministic, and expressed as a RATIO against Encode rather than an absolute
// so the test survives a different machine or a codec that allocates differently.
// Measured baseline before the fix: 1.63x. Measured prototype after: 1.13x.
func TestEncodeOrJSON_DoesNotMarshalTheSameValueTwice(t *testing.T) {
	v := losslessRows(200)

	if got := Check(v); !got.OK {
		t.Fatalf("fixture must be lossless, got %q", got.Reason)
	}

	encode := testing.AllocsPerRun(20, func() {
		if err := Encode(io.Discard, v); err != nil {
			t.Fatalf("Encode: %v", err)
		}
	})
	both := testing.AllocsPerRun(20, func() {
		if err := EncodeOrJSON(io.Discard, v); err != nil {
			t.Fatalf("EncodeOrJSON: %v", err)
		}
	})

	const maxRatio = 1.25
	if ratio := both / encode; ratio > maxRatio {
		t.Errorf("EncodeOrJSON allocates %.2fx Encode (%.0f vs %.0f allocs), want at most %.2fx. "+
			"A ratio this high means the value is still marshalled twice, or the JSON "+
			"size comparison is still running on the output path.",
			ratio, both, encode, maxRatio)
	}
}

// losslessRows builds a uniform listing of the shape a real command emits: a
// scalar key or two beside a list of rows.
func losslessRows(n int) map[string]any {
	rows := make([]any, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, map[string]any{
			"id":    i,
			"name":  fmt.Sprintf("row-%d", i),
			"state": "open",
		})
	}
	return map[string]any{"path": "/some/dir", "total": n, "rows": rows}
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
