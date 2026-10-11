#!/usr/bin/env bash
#
# Run the quickfix micro-benchmarks with the knobs that make results
# comparable between runs, and write them where bench-compare can find them.
#
# Environment:
#   NAME       output file stem under .bench/            (default: current)
#   BENCH      -bench regexp                             (default: .)
#   COUNT      -count, i.e. repetitions per benchmark    (default: 10)
#   BENCHTIME  -benchtime                                (default: 1s)
#   GOMAXPROCS pinned CPU count                          (default: 4)
#   CPUPROFILE path for a CPU profile                    (default: none)
#   MEMPROFILE path for a memory profile                 (default: none)
#
# Repetitions matter more than duration: benchstat needs several samples to
# tell a real change from noise, and on a machine with asymmetric cores
# (Apple silicon, Intel P/E) a single run can land on either kind.

set -euo pipefail

cd "$(dirname "$0")/.."

NAME="${NAME:-current}"
BENCH="${BENCH:-.}"
COUNT="${COUNT:-10}"
BENCHTIME="${BENCHTIME:-1s}"
export GOMAXPROCS="${GOMAXPROCS:-4}"

mkdir -p .bench
out=".bench/${NAME}.txt"

args=(-run '^$' -bench "${BENCH}" -benchmem -count="${COUNT}" -benchtime="${BENCHTIME}")

if [ -n "${CPUPROFILE:-}" ]; then
  args+=(-cpuprofile "${CPUPROFILE}")
fi
if [ -n "${MEMPROFILE:-}" ]; then
  args+=(-memprofile "${MEMPROFILE}" -memprofilerate=1)
fi

echo "running: go test ${args[*]} ." >&2
echo "GOMAXPROCS=${GOMAXPROCS}" >&2

go test "${args[@]}" . | tee "${out}"

echo >&2
echo "wrote ${out}" >&2
