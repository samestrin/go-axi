// Package goaxi is a hardening and guard layer over the official TOON codec,
// github.com/toon-format/toon-go.
//
// It is deliberately NOT a codec. toon-go encodes and decodes TOON; this
// package adds the four things toon-go does not cover, which every
// agent-facing CLI in this ecosystem needs and currently either duplicates or
// lacks entirely:
//
//   - Sanitize: hostile input handling. toon-go rejects control bytes below
//     0x20 with an error but passes U+2028/U+2029, invalid UTF-8, and lone C1
//     bytes straight through. A payload carrying a raw ANSI escape reaches the
//     terminal that renders it.
//
//   - Check: toon-go supports neither defined string types nor
//     encoding.TextMarshaler, and a violating type does not error — it emits
//     EMPTY output. A command silently prints nothing. Check turns that into a
//     detectable condition, and additionally reports when TOON is larger than
//     JSON for a given shape.
//
//   - Exit codes: one canonical set, as exported constants rather than a rule
//     written in a style guide.
//
//   - Help blocks: one canonical writer for AXI contextual-disclosure lines.
//
// The constants-over-prose choice is deliberate. A documented convention
// drifts; a compiled constant cannot.
package goaxi
