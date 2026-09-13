# Changelog

All notable changes to this project are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html). While the major version is 0 the API is not frozen.

## [v0.2.0] - 2026-09-12

Minor rather than patch because two changes are visible to callers without being visible in any signature. Neither produces a compile error, so both are worth reading before upgrading.

### Changed

- `Sanitize` may now return the input itself when nothing needed cleaning, rather than always allocating a copy. The documented guarantee is unchanged — the input is never mutated — but independence was never promised and is no longer provided. Two rules follow: do not mutate your own value after calling `Sanitize` and expect the result to stay as it was, and do not mutate it concurrently while the call runs or while the result is being encoded. `Encode`, `EncodeChecked` and `EncodeOrJSON` pass the result straight to `toon.Marshal`, so a clean payload is marshalled from the caller's own map rather than from a private snapshot, and a concurrent write during that window is a fatal error `recover()` cannot catch.
- Sanitizing a map now cleans string-kinded keys only. A key of any other kind is returned unchanged, so its contents are no longer cleaned. Such a key is still traversed, so an error found inside it is still reported. toon-go accepts plain builtin key types only, so a non-string key cannot reach encoded output under any name — this affects map lookups rather than output.

### Fixed

- A value implementing `encoding.TextMarshaler` nested deeper than 100 levels was reported as lossless while toon-go dropped its value and still emitted its key. The boundary was exact: 49 layers of `[]any` were detected, 50 were not, nor 200, nor 1000. Detection no longer depends on how deeply the value is nested.
- Sanitizing a map with a pointer key whose target held a string needing cleaning rebuilt that key, so the result was keyed on an address the caller never had and could not reconstruct. The entry became unreachable by any means — neither by the original key nor by a cleaned one.
- Checking a large payload allocated tracking state proportional to its total node count, measured at just over 50,000 entries for a 50,000-row listing. This is on the output path through `EncodeChecked` and `EncodeOrJSON`. Peak tracking state is now bounded by a fixed ceiling plus the depth of the value.

### Performance

A single call previously traversed the same value tree up to five times.

- `EncodeOrJSON` at 2000 rows: 4.67 ms to 1.24 ms.
- `Encode` at 2000 rows: 2.65 ms to 1.09 ms.
- `Sanitize` on a clean 500-row listing: 449,761 ns and 15,512 allocations, to 49,967 ns and 1 allocation.

## [v0.1.4] - 2026-09-12

### Added

- `EncodeChecked`, a single-pass check-and-encode. `Check` followed by `Encode` sanitized and marshalled the same value twice to serve one guard; `EncodeChecked` derives its verdict from the bytes it writes.
- `Verdict.SizeCompared`, so a skipped size comparison is distinguishable from a measured "TOON is larger".

### Changed

- `EncodeOrJSON` no longer marshals the same value twice on the lossless path.

Medians against a bare `Encode` as the floor: at 100 rows 134 µs / 156 µs / 338 µs, and at 2000 rows 2.91 ms / 3.32 ms / 7.18 ms, for `Encode`, `EncodeChecked`, and `Check` followed by `Encode`.

## [v0.1.3] - 2026-09-12

### Fixed

- Two gaps in the cycle guard that killed the process. It gave up at 100 levels of nesting and reported no cycle, and slices were not tracked at all. Either let a self-referential value reach the sanitizer, which traversed it into a fatal stack overflow that `recover()` cannot catch.
- The "declare it as a type alias" advice on a marshal error is now conditional, so a func or a channel is no longer told to rename itself.

## [v0.1.2] - 2026-09-11

Anyone on v0.1.1 calling `Sanitize` directly should take this release.

### Added

- `CheckSanitized`, for a caller that already holds the sanitized value, so `EncodeOrJSON` sanitizes once rather than twice on the lossless path.

### Fixed

- A process kill. The cycle guard traversed map values only, while sanitizing also recurses into map keys, so a cycle closing through a pointer key bypassed the guard and exhausted the stack — a fatal Go error `recover()` cannot catch.
- Output is written as a single write rather than as body and terminator separately, closing a window where a consumer could read a payload whose last byte had not arrived.

### Changed

- Cleaning a string is a single pass, and strings that need no cleaning now allocate nothing at all.

## [v0.1.1] - 2026-09-11

### Fixed

- `Sanitize` refuses a reference cycle with a `*CycleError` rather than exhausting the stack. `Check` already refused a cyclic value, but `Encode` does not consult `Check` — it sanitizes directly, and that path followed pointers without tracking what it had already seen. Against v0.1.0 this ended the process with "fatal error: stack overflow" immediately after `Check` had correctly refused the same value. Fixed in `Sanitize` so every entry point is covered, including a caller reaching for `Sanitize` on its own. A value that merely shares a node is a DAG rather than a cycle and is still accepted.
- Dropped a stale `// indirect` marker on toon-go, which is a direct dependency of this module.

## [v0.1.0] - 2026-09-11

First tagged release. Not a codec: toon-go encodes and decodes, and this is the part it does not cover.

### Added

- `Sanitize`, which strips ANSI escapes, U+2028 and U+2029, invalid UTF-8, and C1 bytes that toon-go either rejects outright or forwards untouched. It returns `(any, error)` because a cleaning-induced map key collision cannot be resolved without either losing or inventing data.
- `Check`, which reports data loss, payload shape, and whether TOON actually costs less than JSON.
- `Encode`, a sanitizing encoder, and `EncodeOrJSON`, which adds a self-describing fallback.
- `WriteHelp`, the one `help[]` form that survives its own codec.
- `ExitCode`, one 0-4 set, as constants rather than prose in a style guide.

[v0.2.0]: https://github.com/samestrin/go-axi/compare/v0.1.4...v0.2.0
[v0.1.4]: https://github.com/samestrin/go-axi/compare/v0.1.3...v0.1.4
[v0.1.3]: https://github.com/samestrin/go-axi/compare/v0.1.2...v0.1.3
[v0.1.2]: https://github.com/samestrin/go-axi/compare/v0.1.1...v0.1.2
[v0.1.1]: https://github.com/samestrin/go-axi/compare/v0.1.0...v0.1.1
[v0.1.0]: https://github.com/samestrin/go-axi/releases/tag/v0.1.0
