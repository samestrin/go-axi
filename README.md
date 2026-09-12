# go-axi

> **A hardening and guard layer over the official TOON codec.**
> *Not a codec. The part [toon-go](https://github.com/toon-format/toon-go) does not cover.*

[![License](https://img.shields.io/github/license/samestrin/go-axi)](LICENSE)

## Why this exists

[TOON](https://toonformat.dev/) is the token-efficient output format behind [AXI principle 1](https://axi.md). `github.com/toon-format/toon-go` is its official Go implementation and it handles encoding and decoding well. Use it directly for that.

This library exists because four separate projects each needed the *same four things* toon-go does not provide, and each solved them differently — or not at all.

| Need | toon-go | go-axi |
|---|---|---|
| Strip ANSI escapes, U+2028/29, invalid UTF-8, C1 bytes | no | `Sanitize` |
| Detect the silent empty-output trap | no | `Check` |
| Report when TOON is *larger* than JSON | no | `Check` |
| One exit-code set, one `help[]` format | out of scope | constants |

### The empty-output trap

toon-go supports neither defined string types nor `encoding.TextMarshaler`. A type violating that does **not** return an error — it produces *empty output*. The command prints nothing and exits zero. This has already shipped as a real bug in a consuming project.

`Check` turns that class of failure into something a test can catch.

### Hardening

toon-go rejects control bytes below `0x20` with an error, but passes U+2028, U+2029, invalid UTF-8, and lone C1 bytes (`0x9B`, `0x9D`) straight through. A payload carrying a raw escape sequence reaches whatever terminal renders it.

`Sanitize` closes those gaps, preserving surrounding visible text rather than dropping the field.

### Reference cycles

A value that refers to itself has no TOON representation, and encoding one exhausts the stack. Stack exhaustion is a **fatal** Go runtime error: `recover()` cannot catch it, so the process dies with a goroutine dump and no diagnostic where `encoding/json` would return a clean error.

`Sanitize` and `Check` both refuse a cycle before the encoder is reached — including one that closes through a **map key**, which is reachable because a pointer is comparable regardless of what it points at.

## API

```go
import goaxi "github.com/samestrin/go-axi"
```

### Sanitizing

| Function | Returns | Use when |
|---|---|---|
| `Sanitize(v any) (any, error)` | cleaned copy, same concrete types | default |
| `MustSanitize(v any) any` | cleaned copy; **panics** | keys are fixed identifiers and shapes are acyclic |
| `SanitizeString(s string) string` | cleaned string | building output one field at a time |

Concrete types are preserved, so `toon:` struct tags survive. That matters: TOON field names are part of the contract consuming tools parse, and flattening to `map[string]any` would rename every column to its Go identifier.

**Handle the errors with `errors.As`** — there are two, and neither can be resolved without losing or inventing data, so `Sanitize` refuses rather than guessing:

```go
clean, err := goaxi.Sanitize(v)
if err != nil {
    var cyc *goaxi.CycleError
    var dup *goaxi.KeyCollisionError
    switch {
    case errors.As(err, &cyc): // self-referential value
    case errors.As(err, &dup): // two keys that clean to the same string
    }
}
```

`*KeyCollisionError` fires when e.g. `"na\x1bme"` and `"name"` both clean to `"name"`. Dropping one is silent data loss; renaming one invents data the caller never wrote.

### Checking

```go
type Verdict struct {
    OK        bool   // encoding preserves the data
    Tier      Tier   // TierTabular | TierNested | TierLossy
    Efficient bool   // the TOON payload is no larger than the JSON one
    Reason    string // explains a false OK or Efficient
}

func Check(v any) Verdict                    // sanitizes internally
func CheckSanitized(v any, clean any) Verdict // for a value already sanitized
func CanEncode(v any) bool                   // Check(v).OK
```

`OK` and `Efficient` are separate on purpose. A payload can be perfectly lossless and still cost more as TOON, which is a reason to choose JSON for that command but never a reason to call the value broken.

Use `CheckSanitized` when you already hold the cleaned copy, so it is not sanitized twice.

Tiers describe the shape actually emitted, measured rather than assumed — toon-go encodes ragged and nested shapes losslessly in a list form, so "can it encode" separates nothing.

### Writing

```go
func Encode(w io.Writer, v any) error        // sanitize + TOON, one newline
func EncodeOrJSON(w io.Writer, v any) error  // TOON, or a JSON envelope if lossy
func WriteHelp(w io.Writer, lines []string) error
```

`Encode` sanitizes so callers cannot forget, and writes nothing when encoding fails — a partial payload is worse than none, because it parses.

`EncodeOrJSON` falls back on **data loss only, never on size**, to a self-describing envelope:

```json
{"axi_format":"json","axi_notice":"<why>","data":{...}}
```

Route on the `{"axi_format"` prefix, **not** on a leading `{` or `[`. Four TOON shapes start with `[` — `[0]:`, `[2]: a,b`, `[2]: 1,2`, `[1]{id}:`.

> If your project fixes output format per command at design time (as cadence's `STYLE.md` does), `EncodeOrJSON` is runtime format detection and you should not call it. Keep build-time guards instead.

`WriteHelp` emits `help[N]: first,second` — the inline array, chosen because it is the only form in circulation that survives its own codec. An empty list writes nothing.

### Exit codes

```go
ExitOK         = 0
ExitError      = 1 // the tool itself failed
ExitUsage      = 2 // unknown subcommand or flag — AXI requires failing loud
ExitNotFound   = 3
ExitValidation = 4 // ran correctly; the thing it checked did not pass
```

`ExitValidation` is deliberately distinct from `ExitError`. A checker reporting "this does not conform" is a successful run with a real result, not a broken tool — and an agent cannot decide whether retrying is worthwhile if the two collapse.

## Zero drift

Three mechanisms, in order of strength:

1. **One module, one sanitizer.** Four repos importing one function cannot diverge. Four copy-pasted sanitizers will.
2. **Round-trip is a build failure.** `Decode(Encode(Sanitize(v)))` must equal `Sanitize(v)`. Encoder/decoder pairs tested only against frozen fixtures go stale in silence.
3. **Constants, not prose.** Exit codes and the `help[]` format ship as exported Go constants. A documented convention drifts; a compiled constant cannot.

## Install

```bash
go get github.com/samestrin/go-axi
```

Requires Go 1.23 or newer.

## Development

```bash
go test ./... -race -cover
go vet ./... && gofmt -s -l .
```

## License

[MIT License](LICENSE)
