package goaxi

import (
	"encoding/json"
	"fmt"
	"io"

	toon "github.com/toon-format/toon-go"
)

// formatJSON is the value carried in an envelope's axi_format field.
const formatJSON = "json"

// envelope wraps a value TOON cannot carry losslessly.
//
// Field order matters and is not incidental. axi_format is declared first so the
// rendered payload literally begins with {"axi_format, which is what lets a
// consumer route it. The obvious rule — treat a leading '{' or '[' as JSON — is
// half wrong: four TOON shapes start with '[' ("[0]:", "[2]: a,b", "[2]: 1,2",
// "[1]{id}:"), so only '{' is safe, and an explicit marker is safer still.
type envelope struct {
	Format string `json:"axi_format"`
	Notice string `json:"axi_notice"`
	Data   any    `json:"data"`
}

// Encode sanitizes v and writes it as TOON, terminated by exactly one newline.
//
// Sanitizing here rather than leaving it to the caller is deliberate. The one
// time a caller forgets is the time a control byte reaches a terminal, and a
// control byte is exactly what arrives when a tool echoes back text it did not
// author.
//
// Nothing is written when encoding fails. A partial payload is worse than no
// payload, because it parses.
func Encode(w io.Writer, v any) error {
	clean, err := Sanitize(v)
	if err != nil {
		return err
	}
	b, err := toon.Marshal(clean)
	if err != nil {
		return fmt.Errorf("goaxi: encoding TOON: %w", err)
	}
	return writeLine(w, b)
}

// EncodeOrJSON writes v as TOON when that is lossless, and otherwise falls back
// to a self-describing JSON envelope naming the reason.
//
// The fallback is triggered only by data loss, never by size. Routing on cost
// would make a command's output format vary with payload length, which no
// consumer could predict and every consumer would have to handle both ways.
//
// Note for cadence: its STYLE.md prohibits runtime format detection, fixing the
// format per command at design time. This function is that prohibited thing, so
// cadence should keep its build-time guards and simply not call it. It exists
// for callers that have no such rule.
func EncodeOrJSON(w io.Writer, v any) error {
	verdict := Check(v)
	if verdict.OK {
		return Encode(w, v)
	}

	// The fallback sanitizes too. An earlier version put the raw value straight
	// into the envelope, so the JSON path emitted the very control bytes the
	// TOON path strips — the escape hatch defeated the sanitizer.
	//
	// Sanitize refuses a cyclic value with *CycleError instead of exhausting the
	// stack, so its error is simply propagated. This used to pre-check hasCycle
	// to route around a crash that no longer happens, which meant one call
	// detected the same cycle three times: here, in Check, and inside Sanitize.
	clean, err := Sanitize(v)
	if err != nil {
		return err
	}

	b, err := json.Marshal(envelope{
		Format: formatJSON,
		Notice: verdict.Reason,
		Data:   clean,
	})
	if err != nil {
		return fmt.Errorf("goaxi: encoding JSON fallback: %w", err)
	}
	return writeLine(w, b)
}

// WriteHelp emits an AXI contextual-disclosure block: next-step command
// templates with fixed flags carried forward and runtime values left as
// placeholders like <id>.
//
// The form is the inline TOON array, `help[2]: first,second`, chosen because it
// is the only one of the three in circulation that survives its own codec. The
// AXI specification's own indented example fails to decode with "list length
// mismatch", and cadence's `help[] line` form fails with "missing colon after
// key". Appending this after a body is safe: the combined payload decodes as one
// value carrying both.
//
// An empty list writes nothing. A command with no meaningful next step should
// not emit a stray "help[0]:" that an agent pays tokens to read and learns
// nothing from.
func WriteHelp(w io.Writer, lines []string) error {
	if len(lines) == 0 {
		return nil
	}
	clean := make([]string, len(lines))
	for i, l := range lines {
		clean[i] = SanitizeString(l)
	}
	b, err := toon.Marshal(map[string]any{"help": clean})
	if err != nil {
		return fmt.Errorf("goaxi: encoding help block: %w", err)
	}
	return writeLine(w, b)
}

// writeLine writes b followed by exactly one newline, adding one only when the
// encoder did not. Encoders disagree about trailing newlines, and a caller
// appending a help block to a body needs the boundary to be predictable.
func writeLine(w io.Writer, b []byte) error {
	if _, err := w.Write(b); err != nil {
		return err
	}
	if len(b) > 0 && b[len(b)-1] == '\n' {
		return nil
	}
	_, err := io.WriteString(w, "\n")
	return err
}
