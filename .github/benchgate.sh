#!/usr/bin/env bash
#
# benchgate.sh — fail a build that makes go-axi slower or greedier.
#
# Usage: benchgate.sh <base-profile> <head-profile>
#
# Both arguments are `go test -bench` output files. The comparison is A/B on one
# machine — base and head are measured back to back on the same runner — rather
# than against a committed baseline. Committed baselines do not survive a shared
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
# Deliberately loose. Timing spread on a quiet M5 reached 21% on EncodeOrJSON and
# 13% on EncodeChecked; a shared runner is worse. Tightening this buys flakes,
# not sensitivity — the p-value is what actually separates signal from noise.
TIME_THRESHOLD=${TIME_THRESHOLD:-10}

# Allocations are deterministic: every allocs/op row measures at CI 0%. So any
# significant increase is real, and the threshold is zero. This is the gate that
# protects the "one allocation regardless of size" guarantee in the README.
ALLOC_THRESHOLD=${ALLOC_THRESHOLD:-0}

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
  -v alloc_thr="$ALLOC_THRESHOLD" '
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

    if (metric == "sec/op" && pct + 0 > time_thr + 0) {
      failures[++nf] = sprintf("  %-52s %-10s %s  (limit +%s%%)", name, metric, delta, time_thr)
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
