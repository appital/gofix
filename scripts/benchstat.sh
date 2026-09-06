#!/usr/bin/env bash
#
# Compare two benchmark runs. benchstat is fetched on demand rather than added
# to go.mod, so this stays out of the module's dependency graph.
#
# Usage: scripts/benchstat.sh [baseline] [current]

set -euo pipefail

cd "$(dirname "$0")/.."

base="${1:-.bench/baseline.txt}"
cur="${2:-.bench/current.txt}"

for f in "$base" "$cur"; do
  if [ ! -f "$f" ]; then
    echo "missing $f - run 'make bench-baseline' then 'make bench'" >&2
    exit 1
  fi
done

exec go run golang.org/x/perf/cmd/benchstat@latest "$base" "$cur"
