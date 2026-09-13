package goaxi

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

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
	return encodeSanitized(w, clean)
}

// EncodeChecked sanitizes v, verifies TOON can carry it losslessly, and writes
// it — one sanitize walk and one TOON marshal serving both jobs.
//
// Use this instead of Check followed by Encode. That pair sanitizes and
// marshals the same value twice to serve a single guard. Measured medians on
// the same payload, against a bare Encode as the floor:
//
//	rows   Encode    EncodeChecked   Check+Encode
//	100    134us     156us (+16%)    338us (+152%)
//	2000   2.91ms    3.32ms (+14%)   7.18ms (+147%)
//
// Allocations at 2000 rows: 98,073 / 110,099 / 222,204. The guard costs about
// 14%; the rest was duplicated work.
//
// Nothing is written unless the verdict is OK, because a partial payload is
// worse than none — it parses. The returned Verdict describes the bytes
// actually written, so Tier is measured rather than predicted.
//
// Efficient is deliberately NOT measured; SizeCompared reports that. The
// comparison needs a second marshal that a caller writing the payload does not
// read. Call Check when you want the cost signal.
func EncodeChecked(w io.Writer, v any) (Verdict, error) {
	// Sanitize is also the cycle and key-collision guard, so its error is
	// propagated as-is for errors.As rather than wrapped into a reason string.
	clean, err := Sanitize(v)
	if err != nil {
		return Verdict{Tier: TierLossy, Reason: err.Error()}, err
	}

	verdict, b := verdictFor(v, clean, false)
	if !verdict.OK {
		return verdict, fmt.Errorf("goaxi: %s", verdict.Reason)
	}
	return verdict, writeLine(w, b)
}

// EncodeOrJSON writes v as TOON when that is lossless, and otherwise falls back
// to a self-describing JSON envelope naming the reason.
//
// The fallback is triggered only by data loss, never by size. Routing on cost
// would make a command's output format vary with payload length, which no
// consumer could predict and every consumer would have to handle both ways.
//
// This is runtime format detection. A project that fixes the output format per
// command at design time should keep its build-time guard and not call this.
// It exists for callers that have no such rule.
func EncodeOrJSON(w io.Writer, v any) error {
	// Sanitized ONCE, for both the routing decision and whichever payload wins.
	// This used to call Check (which sanitizes internally) and then Encode (which
	// sanitizes again), so the OK path cleaned the same value twice for no
	// correctness benefit — a finding from atcr's review.
	//
	// Sanitize is also the cycle and key-collision guard, so its error is simply
	// propagated rather than pre-checked.
	clean, err := Sanitize(v)
	if err != nil {
		return err
	}

	// verdictFor hands back the TOON bytes it judged, so the OK path writes
	// those rather than marshalling the same value again. sizeCompare is off
	// because routing here depends on loss only, never on cost.
	verdict, body := verdictFor(v, clean, false)
	if verdict.OK {
		return writeLine(w, body)
	}

	// The fallback carries the SANITIZED payload. An earlier version put the raw
	// value straight into the envelope, so the JSON path emitted the very control
	// bytes the TOON path strips — the escape hatch defeated the sanitizer.
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

// encodeSanitized marshals an already-sanitized value and writes it. Split out
// so a caller holding the cleaned copy does not sanitize it a second time.
func encodeSanitized(w io.Writer, clean any) error {
	b, err := toon.Marshal(clean)
	if err != nil {
		return fmt.Errorf("goaxi: encoding TOON: %w", err)
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
// mismatch", and the `help[] line` form fails with "missing colon after key".
// Appending this after a body is safe: the combined payload decodes as one
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

// writeLine writes b terminated by exactly one newline, in a SINGLE Write call.
//
// Encoders disagree about trailing newlines, and a caller appending a help block
// to a body needs the boundary to be predictable.
//
// The single write is the point. This used to write the body and then the
// newline separately, which left a window where the body landed and the
// terminator did not — against a closed pipe the consumer receives a payload
// missing its last byte and parses it happily. One write either lands or fails;
// there is no half-written state for a consumer to misread.
//
// An empty body writes nothing rather than a bare newline. A command with no
// payload should be silent, not emit a blank line an agent pays tokens to read.
var lineBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 4096)
		return &b
	},
}

func writeLine(w io.Writer, b []byte) error {
	if len(b) == 0 {
		return nil
	}
	if b[len(b)-1] == '\n' {
		_, err := w.Write(b)
		return err
	}
	p := lineBufPool.Get().(*[]byte)
	buf := (*p)[:0]
	if cap(buf) < len(b)+1 {
		buf = make([]byte, 0, len(b)+1)
	}
	buf = append(buf, b...)
	buf = append(buf, '\n')
	_, err := w.Write(buf)
	if cap(buf) <= 65536 {
		*p = buf
		lineBufPool.Put(p)
	}
	return err
}
