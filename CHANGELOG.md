# Changelog

All notable changes to this project are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html). While the major version is 0 the API is not frozen.

## [v0.3.1] - 2026-09-15

Performance only. No API change, and no observable behaviour change: `DecodeTabular`'s fast path is proven against the pre-existing generic decoder by a differential test suite that requires byte-for-byte agreement, and every row it cannot prove safe (an escape, an unquoted colon, a field-count mismatch) still falls back to that unchanged, proven path.

### Performance

Medians of six runs, `DecodeTabular` against v0.3.0, on a clean payload, an `atcr`-shaped payload (a quoted `"file:line"` column), and a payload with a backslash escape in every row that always falls back to the generic decoder.

- Time: 74-81% less at 20 rows, 13-75% less at 500 rows. The escaped/fallback fixture improves too, because part of the win — a pooled scanner buffer — applies before the fast path is ever considered.
- Memory: 73-91% less across every fixture and row count.
- Allocations: 70-85% fewer on the clean and `atcr` fixtures; 6-8% fewer even on the fallback fixture.

Three changes produced this, landing as PRs #21, #22, and #23:

- A fast path builds `map[string]string` rows directly from the row text for any row with no backslash, skipping `toon-go`'s generic decoder — the `any`-boxing decode step and the `map[string]any` it builds per row — entirely. This covers plain rows and the `atcr` shape alike, since a quote with no escape inside it needs only its surrounding quote marks stripped, not a real unescape pass.
- `DecodeTabular`'s scanner buffer (64KB, sized for large `PROBLEM`/`FIX` fields) now comes from a pool instead of being allocated fresh per call, which is most of the win at 20 rows, where that buffer used to dominate total memory.
- The fast path reuses a token buffer across rows instead of allocating one per row, and skips a `ParseFloat`/`FormatFloat` round-trip for plain non-negative integers up to 15 digits, where it is mathematically guaranteed to be a no-op.

## [v0.3.0] - 2026-09-14

Minor rather than patch because the API grows in three directions. Nothing is removed and no signature changes, so upgrading from v0.2.1 cannot produce a compile error.

### Added

- A decode side. `Decode` reads a whole TOON document of any shape and forwards to toon-go, so there is one decoder rather than two; it adds only an empty-document guard, because toon-go reports an empty document as a non-nil EMPTY container that a caller cannot distinguish from real data. `DecodeFile` is the same from a path.
- `DecodeTabular` and `DecodeTabularFile`, which read ONE tabular array and return the three things toon-go's public API cannot give back: the declared field ORDER, the delimiter, and the declared row count. Its `parsedHeader` type is unexported and it decodes to a map, so all three are unreachable. Three of its strict-pass rules also make it unusable on real CLI output — an explicitly written comma delimiter is rejected outright, a trailing block declaring more rows than it carries fails the whole document, and a row count disagreeing with the physical rows is an error in either direction. That last rule matters: a paginated payload declares the true total while emitting fewer rows on purpose. `Document.Declared` and `len(Document.Rows)` are both returned and the caller decides. More rows than declared is still refused.
- `IsTabularHeader`, the dispatch point between the two readers. It tests one line for the shape that opens a tabular array and deliberately does NOT validate, so a BROKEN tabular header stays on the tabular path and reports its real problem instead of being rerouted to the document reader and silently losing a column.
- `Format`, `ParseFormat`, `Format.Encode` and `Format.EncodeProjected`, so a command supporting `--toon` and `--json` carries the choice as a value rather than branching at every call site. `EncodeProjected` routes through the JSON form so one tag set drives both output shapes, at roughly 2.6x the time and 2.8x the allocations of `Encode` on a 500-row payload. `Encode` stays the default. Projection is TOON-only, because `encoding/json` already reads the `json` tag.
- `CheckTags` and `TagVerdict`, which find a field missing a `toon` tag. toon-go does not fall back to the `json` tag, and a missing tag does not error and is not empty — it silently publishes Go identifiers as the column contract, which is exactly what breaks an instruction like "the third column is SEVERITY". 368 ns, zero allocations, intended for a test over every type a command prints.

### Changed

- The package documentation no longer says toon-go's decoding is out of scope, because `Decode` now forwards to it and `DecodeTabular` covers the tabular header it cannot expose.

### Development

- A benchmark gate runs on every pull request. It measures the base commit and the branch back to back on one runner rather than against a committed baseline, because GitHub rotates runner hardware and an absolute number recorded on one generation fails on the next for reasons unrelated to the change. Allocations fail at any significant increase, which is what protects the "one allocation regardless of size" guarantee; wall time fails past 10%; `B/op` is reported and not gated. The gate reports how many measurements it compared and fails at zero, so a run that parsed nothing cannot be read as a pass.

## [v0.2.1] - 2026-09-13

Performance only. No API change, and no observable behaviour change: the string cleaner was checked against the implementation it replaces over 200,000 random inputs including invalid UTF-8 and is byte-identical on every one, and tabular-header detection is unchanged because the regexp that defines it still decides every case.

### Performance

Medians of six runs on a 2000-row payload unless stated.

- `SanitizeString` on a clean string: 52.1 ns to 18.8 ns for a sentence, 5.1 ns to 2.4 ns for a short string, still zero allocations. ASCII bytes are now classified by lookup instead of by decoding each rune.
- `Sanitize` on a clean 500-row payload: 51.5 µs to 35.0 µs, still one allocation.
- `EncodeOrJSON`: 1.28 ms to 1.17 ms. `EncodeChecked`: 1.25 ms to 1.16 ms, and about 8% less memory.
- `Check`: 18.1 µs to 17.1 µs.
- Tabular-header detection now skips the regexp engine when the payload cannot contain a header. This is negligible on tabular output — roughly 45 ns against the ~1 ms it takes to encode the same payload — but a 2000-line list previously cost 908 µs there, rivalling the entire encode, and now costs 480 ns.

### Fixed

- The identity set the loss walk uses is bounded rather than growing with the payload, and the buffer a line is written from is reused between calls.

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

[v0.3.1]: https://github.com/samestrin/go-axi/compare/v0.3.0...v0.3.1
[v0.3.0]: https://github.com/samestrin/go-axi/compare/v0.2.1...v0.3.0
[v0.2.1]: https://github.com/samestrin/go-axi/compare/v0.2.0...v0.2.1
[v0.2.0]: https://github.com/samestrin/go-axi/compare/v0.1.4...v0.2.0
[v0.1.4]: https://github.com/samestrin/go-axi/compare/v0.1.3...v0.1.4
[v0.1.3]: https://github.com/samestrin/go-axi/compare/v0.1.2...v0.1.3
[v0.1.2]: https://github.com/samestrin/go-axi/compare/v0.1.1...v0.1.2
[v0.1.1]: https://github.com/samestrin/go-axi/compare/v0.1.0...v0.1.1
[v0.1.0]: https://github.com/samestrin/go-axi/releases/tag/v0.1.0
