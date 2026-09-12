# go-axi

> **Safe TOON output for Go command-line tools.** A hardening and guard layer over the official codec, [toon-go](https://github.com/toon-format/toon-go).

[![Go Reference](https://pkg.go.dev/badge/github.com/samestrin/go-axi.svg)](https://pkg.go.dev/github.com/samestrin/go-axi)
[![Test](https://github.com/samestrin/go-axi/actions/workflows/test.yml/badge.svg)](https://github.com/samestrin/go-axi/actions/workflows/test.yml)
[![Go 1.23+](https://img.shields.io/badge/go-1.23%2B-00ADD8)](https://go.dev/dl/)
[![License](https://img.shields.io/github/license/samestrin/go-axi)](LICENSE)

[TOON](https://toonformat.dev/) is the token-efficient output format behind [AXI](https://axi.md) principle 1, and `toon-go` is its official Go implementation. `toon-go` encodes and decodes. `go-axi` covers what an agent-facing CLI still has to get right before it prints:

- **Sanitize untrusted text.** Strip ANSI escapes, `U+2028`/`U+2029`, lone C1 bytes and invalid UTF-8 out of anything the tool did not author, without dropping the surrounding visible text.
- **Catch silent encoding failures.** Some Go types encode to *empty output* instead of an error, so a command prints nothing and exits `0`. `Check` turns that into a condition a test can assert on.
- **Know when TOON is not the cheaper choice.** `Check` measures the TOON payload against the JSON one and reports which is smaller.
- **Ship one exit-code set and one `help[]` writer** as compiled constants instead of a convention in a style guide.

## Install

```bash
go get github.com/samestrin/go-axi
```

Go 1.23 or newer. One dependency: `toon-go`.

## Quick start

```go
package main

import (
	"fmt"
	"os"

	goaxi "github.com/samestrin/go-axi"
)

type run struct {
	ID     string `toon:"id"`
	Status string `toon:"status"`
}

func main() {
	payload := map[string]any{
		"total": 2,
		"runs": []run{
			{ID: "4812", Status: "passed"},
			{ID: "4813", Status: "failed"},
		},
	}

	if err := goaxi.Encode(os.Stdout, payload); err != nil {
		fmt.Fprintln(os.Stderr, "ci:", err)
		os.Exit(int(goaxi.ExitError))
	}

	if err := goaxi.WriteHelp(os.Stdout, []string{"ci logs 4813", "ci rerun 4813"}); err != nil {
		fmt.Fprintln(os.Stderr, "ci:", err)
		os.Exit(int(goaxi.ExitError))
	}

	os.Exit(int(goaxi.ExitOK))
}
```

```
runs[2]{id,status}:
  "4812",passed
  "4813",failed
total: 2
help[2]: ci logs 4813,ci rerun 4813
```

`Encode` sanitized the payload on the way out, and `WriteHelp` appended a contextual-disclosure block that decodes as part of the same document.

## API

```go
import goaxi "github.com/samestrin/go-axi"
```

Full reference: [pkg.go.dev/github.com/samestrin/go-axi](https://pkg.go.dev/github.com/samestrin/go-axi).

### Writing output

| Function | Behavior |
|---|---|
| `Encode(w io.Writer, v any) error` | Sanitize, encode as TOON, terminate with exactly one newline |
| `EncodeOrJSON(w io.Writer, v any) error` | Same, but fall back to a JSON envelope when TOON would lose data |
| `WriteHelp(w io.Writer, lines []string) error` | Write `help[N]: first,second`; an empty list writes nothing |

`Encode` sanitizes internally so a caller cannot forget, and writes nothing at all when encoding fails — a partial payload is worse than none, because it parses.

`EncodeOrJSON` falls back on **data loss only, never on size**, to a self-describing envelope:

```json
{"axi_format":"json","axi_notice":"<why>","data":{...}}
```

Route on the `{"axi_format` prefix rather than on a leading `{` or `[`. Four TOON shapes begin with `[`: `[0]:`, `[2]: a,b`, `[2]: 1,2` and `[1]{id}:`.

If your project fixes the output format per command at design time, `EncodeOrJSON` is runtime format detection and you should keep your build-time guard instead.

### Checking a value before you print it

```go
type Verdict struct {
	OK        bool   // encoding preserves the data
	Tier      Tier   // TierTabular | TierNested | TierLossy
	Efficient bool   // the TOON payload is no larger than the JSON one
	Reason    string // explains a false OK or Efficient, in actionable terms
}

func Check(v any) Verdict                     // sanitizes internally
func CheckSanitized(v any, clean any) Verdict // for a value already sanitized
func CanEncode(v any) bool                    // Check(v).OK
```

| Tier | Meaning |
|---|---|
| `TierTabular` | Uniform rows encoded as a table with a declared field list — where the token savings come from |
| `TierNested` | Lossless but not columnar: ragged rows, list-valued fields, nested objects |
| `TierLossy` | Encoding would discard data. Do not emit this as TOON |

Tiers describe the shape actually emitted, measured rather than assumed.

`OK` and `Efficient` are separate on purpose. A payload can be perfectly lossless and still cost more as TOON, which is a reason to choose JSON for that command but never a reason to call the value broken.

A `Check` assertion in a table test is the cheapest place to catch the empty-output trap:

```go
func TestListOutputSurvivesTOON(t *testing.T) {
	v := goaxi.Check(listRunsPayload())
	if !v.OK {
		t.Fatalf("payload is lossy: %s", v.Reason)
	}
	if v.Tier != goaxi.TierTabular {
		t.Errorf("expected a tabular payload, got %s", v.Tier)
	}
}
```

Use `CheckSanitized` when you already hold the cleaned copy, so the value is not sanitized twice.

### Sanitizing

| Function | Returns | Use when |
|---|---|---|
| `Sanitize(v any) (any, error)` | Cleaned copy, same concrete types | Default |
| `MustSanitize(v any) any` | Cleaned copy; **panics** on error | Keys are fixed identifiers and shapes are acyclic |
| `SanitizeString(s string) string` | Cleaned string | Building output one field at a time |

Concrete types are preserved, so `toon:` struct tags survive. That matters: TOON field names are part of the contract a consuming tool parses, and flattening to `map[string]any` would rename every column to its Go identifier.

`Sanitize` returns two error types, and neither can be resolved without losing or inventing data, so it refuses rather than guessing. Match them with `errors.As`:

```go
clean, err := goaxi.Sanitize(v)
if err != nil {
	var cyc *goaxi.CycleError
	var dup *goaxi.KeyCollisionError
	switch {
	case errors.As(err, &cyc): // a self-referential value
	case errors.As(err, &dup): // two keys that clean to the same string
	}
}
```

`*KeyCollisionError` fires when, for example, `"na\x1bme"` and `"name"` both clean to `"name"`. Dropping one is silent data loss; renaming one invents data the caller never wrote.

`*CycleError` refuses a self-referential value before the encoder sees it. That includes the cases a pointer-only check misses: a cycle closing through a **map key**, a slice that **contains itself**, and a loop that closes at any depth. Encoding one exhausts the stack, and stack exhaustion is a fatal Go runtime error: `recover()` cannot catch it, so the process dies with a goroutine dump where `encoding/json` would return a clean error.

### Exit codes

```go
ExitOK         = 0
ExitError      = 1 // the tool itself failed
ExitUsage      = 2 // unknown subcommand or flag — AXI requires failing loud
ExitNotFound   = 3
ExitValidation = 4 // ran correctly; the thing it checked did not pass
```

`ExitCode` is a defined type with a `String()` method, so `os.Exit(int(goaxi.ExitValidation))`.

`ExitValidation` is deliberately distinct from `ExitError`. A checker reporting "this does not conform" is a successful run with a real result, not a broken tool — and an agent cannot decide whether a retry is worthwhile if the two collapse into one code.

## What this adds to toon-go

`toon-go` is a good codec. Use it directly for encoding and decoding. These are the gaps a production CLI hits:

| Gap in toon-go | What happens without a guard | go-axi |
|---|---|---|
| Passes `U+2028`/`U+2029`, invalid UTF-8 and lone C1 bytes (`0x9B`, `0x9D`) through | A payload carrying a raw escape sequence reaches whatever terminal renders it | `Sanitize` |
| Supports neither defined string types nor `encoding.TextMarshaler`, and does not error on them | **Empty output.** The command prints nothing and exits `0` | `Check` |
| Reference cycles reach the encoder | Stack exhaustion kills the process; `recover()` cannot catch it | `Sanitize`, `Check` |
| Size relative to JSON is not reported | A command pays more tokens as TOON than it would as JSON | `Check.Efficient` |
| Exit codes and `help[]` formatting are out of scope | Every tool invents its own, and they drift | Exported constants, `WriteHelp` |

The `help[]` form is the inline TOON array, because it is the only one in circulation that survives its own codec: the AXI specification's indented example fails to decode with "list length mismatch", and the `help[] line` form fails with "missing colon after key".

## AXI principle coverage

go-axi is a library, not an AXI tool itself. It implements the output-layer principles so each CLI does not reimplement them:

| AXI principle | Provided by |
|---|---|
| 1 — Token-efficient output | `Encode`, plus `Check.Efficient` and `Check.Tier` to prove it |
| 5 — Definitive empty states | `Check` distinguishes a legitimately empty result from encoder failure |
| 6 — Structured errors and exit codes | `ExitCode` constants; typed `Sanitize` errors |
| 9 — Contextual disclosure | `WriteHelp` |

Principles 2, 3, 4, 7, 8 and 10 are per-command design decisions and stay with the tool.

## Guarantees

- **Round-trip is a build failure.** `Decode(Encode(Sanitize(v)))` must equal `Sanitize(v)`. Codec pairs tested only against frozen fixtures go stale in silence.
- **One write per payload.** A body and its terminating newline land in a single `Write` call, so a closed pipe cannot deliver a payload missing its last byte.
- **Nothing on failure.** `Encode` writes zero bytes when encoding fails.
- **Input is never mutated.** `Sanitize` returns a copy.
- **No output means no output.** An empty body and an empty help list write nothing, not a blank line an agent pays tokens to read.

## Development

```bash
go test ./... -race -cover
go vet ./... && gofmt -s -l .
```

## License

[MIT](LICENSE)
