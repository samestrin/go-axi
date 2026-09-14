// Package goaxi is a hardening and guard layer over the official TOON codec,
// github.com/toon-format/toon-go.
//
// It is deliberately NOT a codec. toon-go encodes and decodes TOON; this
// package adds the five things toon-go does not cover, which an agent-facing
// CLI has to get right before it prints or reads:
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
//     JSON for a given shape. EncodeChecked applies that guard on the way out
//     in a single pass, for callers that are about to print.
//
//   - Exit codes: one canonical set, as exported constants rather than a rule
//     written in a style guide.
//
//   - Help blocks: one canonical writer for AXI contextual-disclosure lines.
//
//   - Tabular reading: toon-go decodes a whole document in one strict pass and
//     returns a map, which drops the three things a tabular array declares
//     about itself. Its parsedHeader type is unexported, so the field ORDER,
//     the delimiter and the declared row count are all unreachable from the
//     public API — and a paginated payload, which declares more rows than it
//     carries on purpose, fails the whole document. DecodeTabular reads the
//     header itself and returns those three. Decode is the ordinary whole-
//     document read and forwards to toon-go, which keeps one decoder rather
//     than two.
//
// The constants-over-prose choice is deliberate. A documented convention
// drifts; a compiled constant cannot.
package goaxi
