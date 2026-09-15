#!/usr/bin/env bash
#
# benchgate.sh — fail a build that makes go-axi slower or greedier.
#
# Usage: benchgate.sh <base-profile> <head-profile>
#
# Both arguments are `go test -bench` output files. The comparison is A/B on one
# machine — base and head are compiled first, then measured ALTERNATELY on the
# same runner, so neither side owns a contiguous block of time that interference
# could skew on its own — rather than against a committed baseline. See the
# TIME_THRESHOLD note below for what happened when they were not interleaved.
# Committed baselines do not survive a shared
# CI fleet: GitHub rotates runner hardware, so an absolute number recorded on one
# generation fails on the next for reasons that have nothing to do with the diff.
# The README's published figures are Apple M5 and are not reproducible in CI at
# all, which is the same problem stated another way.
#
# benchstat has no fail-on-regression flag (checked: it exposes only -alpha,
# -format, -filter and the projection flags), so the verdict is parsed from its
# CSV. It reports a delta only when p < alpha, so anything that is not "~" has
# already passed a significance test and noise is filtered before we see it.

set -euo pipefail

BASE=${1:?usage: benchgate.sh <base-profile> <head-profile>}
HEAD=${2:?usage: benchgate.sh <base-profile> <head-profile>}

# A significant wall-time regression beyond this percentage fails the build.
#
# Deliberately loose, and raised from 10 after 10 proved to be BELOW the noise
# floor of a shared runner. The p-value alone does not save it: benchstat asks
# whether two sample sets differ, and when interference covers one side's whole
# measurement block the two sets genuinely do differ — significantly, and for
# reasons that have nothing to do with the diff. PR #17 changed no encode path
# and still failed three TabularHeader benchmarks together at +17.95%, +17.79%
# and +10.65%; a re-run of the same commit put them at +1.97% and +1.90%.
#
# The workflow now alternates base and head rather than measuring each in one
# block, which is the real fix — it denies interference the chance to land on
# one side only. This threshold is the margin left over, set above the worst
# false positive observed rather than below it. Timing spread on a QUIET M5
# already reached 21% on EncodeOrJSON and 13% on EncodeChecked, so anything
# tighter buys flakes, not sensitivity.
#
# What still catches a real regression: it has to survive interleaving, which
# means it has to be present in every round rather than in one unlucky window.
TIME_THRESHOLD=${TIME_THRESHOLD:-15}

# Allocations are deterministic: every allocs/op row measures at CI 0%. So any
# significant increase is real, and the threshold is zero. This is the gate that
# protects the "one allocation regardless of size" guarantee in the README.
ALLOC_THRESHOLD=${ALLOC_THRESHOLD:-0}

# Below this magnitude (seconds), a benchmark is measuring single-digit-to-low-
# double-digit nanoseconds — close enough to the clock's own noise floor that a
# percentage comparison stops meaning anything. Confirmed directly: comparing
# BenchmarkSanitizeString/clean-short (2-3ns/op) against ITSELF, same binary, no
# code change, on a quiet local machine, still swung ~22% between runs. On the
# shared CI fleet this exact benchmark family (clean-short, clean-long) failed
# the 15% gate three separate times in a row while carrying zero diff to
# sanitize.go — always this family, never a microsecond-scale benchmark, which
# is what points at magnitude rather than a real regression or a bad interleave.
TINY_MAGNITUDE_SEC=${TINY_MAGNITUDE_SEC:-5e-8}

# The loosened threshold used only below TINY_MAGNITUDE_SEC. Set with margin
# above the worst false positive actually observed in CI (26.91%), the same way
# TIME_THRESHOLD itself was set above its own observed noise ceiling. Still
# tight enough to catch a regression that would matter at this scale (anything
# doubling a nanosecond-scale op survives this easily).
TINY_TIME_THRESHOLD=${TINY_TIME_THRESHOLD:-30}

command -v benchstat >/dev/null 2>&1 || {
  echo "::error::benchstat not on PATH"
  exit 1
}

echo "=== benchstat comparison ==="
benchstat "$BASE" "$HEAD" || true
echo

# mktemp rather than a fixed /tmp path: two concurrent runs would otherwise
# clobber each other's CSV, and a predictable name in a shared /tmp is a symlink
# hazard on any machine that is not a single-use CI runner.
CSV=$(mktemp "${TMPDIR:-/tmp}/benchgate.XXXXXX")
trap 'rm -f "$CSV"' EXIT

benchstat -format csv "$BASE" "$HEAD" 2>/dev/null > "$CSV"

awk -F, \
  -v time_thr="$TIME_THRESHOLD" \
  -v alloc_thr="$ALLOC_THRESHOLD" \
  -v tiny_mag="$TINY_MAGNITUDE_SEC" \
  -v tiny_thr="$TINY_TIME_THRESHOLD" '
  # Header row for a metric block, e.g. ",sec/op,CI,sec/op,CI,vs base,P".
  # Identified by the CI marker in column 3, which a filename row never has.
  $2 ~ /\/op$/ && $3 == "CI" { metric = $2; seen_metrics++; next }

  # Not a data row: preamble (goos/goarch/pkg/cpu), filename rows, blank
  # separators, and the trailing geomean summary.
  $1 == ""        { next }
  $1 == "geomean" { next }
  metric == ""    { next }

  {
    name  = $1
    base  = $2
    head  = $4
    delta = $6

    # Which side carries a measurement, NOT how many fields the row has.
    #
    # THIS LINE WAS THE BUG. The guard here used to be `NF < 6`, which looked
    # reasonable and was not: benchstat TRUNCATES a row when a benchmark is
    # missing from one side. A head-only benchmark renders as "Name,,,val,CI"
    # (5 fields) and a base-only one as "Name,val,CI" (3), so the old guard
    # discarded exactly the rows the next branch existed to report. The
    # new-benchmark message was dead code and never once fired across two PRs.
    #
    # The part that was not merely cosmetic: a benchmark that FAILED to run on
    # the base side truncates identically, so it vanished without a word.
    if (base == "" && head == "") { next }
    if (base == "") { if (!(name in added))   { added[name]   = 1; nadded++   } next }
    if (head == "") { if (!(name in removed)) { removed[name] = 1; nremoved++ } next }

    compared++

    # "~" means benchstat found no statistically significant difference.
    if (delta == "" || delta == "~") next

    pct = delta
    gsub(/[+%]/, "", pct)

    # A negative delta is an improvement. Report it, never fail on it.
    if (pct + 0 < 0) { improved[++ni] = sprintf("  %-52s %-10s %s", name, metric, delta); next }

    if (metric == "sec/op") {
      # base is the CSV base-side value, e.g. "2.4255e-09" -- awk parses
      # scientific notation as a number natively, no extra handling needed.
      thr = (base + 0 > 0 && base + 0 < tiny_mag + 0) ? tiny_thr + 0 : time_thr + 0
      if (pct + 0 > thr) {
        failures[++nf] = sprintf("  %-52s %-10s %s  (limit +%s%%)", name, metric, delta, thr)
      } else {
        tolerated[++nt] = sprintf("  %-52s %-10s %s", name, metric, delta)
      }
    } else if (metric == "allocs/op" && pct + 0 > alloc_thr + 0) {
      failures[++nf] = sprintf("  %-52s %-10s %s  (allocations must not grow)", name, metric, delta)
    } else {
      tolerated[++nt] = sprintf("  %-52s %-10s %s", name, metric, delta)
    }
  }

  END {
    if (ni > 0) {
      print "Improvements:"
      for (i = 1; i <= ni; i++) print improved[i]
      print ""
    }
    if (nt > 0) {
      print "Within tolerance:"
      for (i = 1; i <= nt; i++) print tolerated[i]
      print ""
    }
    if (nadded > 0) {
      print "New benchmarks, no base to compare:"
      for (n in added) print "  " n
      print ""
    }
    if (nremoved > 0) {
      print "Absent from this branch (deleted, or failed to run on base):"
      for (n in removed) print "  " n
      print ""
    }

    # The count is not decoration. The bug above was invisible precisely because
    # a parser that silently drops rows prints the same thing as one that has
    # nothing to say. A run that compared zero measurements is broken, not clean.
    printf "Compared %d benchmark measurement(s) across %d metric(s).\n", compared + 0, seen_metrics + 0

    if (compared + 0 == 0) {
      print "::error::no benchmark measurements were compared — the gate parsed nothing and cannot vouch for this change"
      exit 1
    }

    if (nf > 0) {
      print ""
      print "REGRESSIONS:"
      for (i = 1; i <= nf; i++) print failures[i]
      printf "::error::%d benchmark regression(s) — go-axi may grow, but it may not get slower\n", nf
      exit 1
    }
    print "No regression. go-axi is as fast and as lean as its base."
  }
' "$CSV"
