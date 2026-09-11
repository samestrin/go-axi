# go-axi

> **A hardening and guard layer over the official TOON codec.**
> *Not a codec. The ~250 lines [toon-go](https://github.com/toon-format/toon-go) does not cover.*

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
