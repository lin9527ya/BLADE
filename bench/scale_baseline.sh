#!/usr/bin/env bash
# Deterministic synthetic scale baseline. Requires an isolated DB whose name
# contains test/acceptance; never point this at a production BLADE database.
set -euo pipefail
cd "$(dirname "$0")/.."
: "${BLADE_PG_DSN:?set BLADE_PG_DSN to an isolated test/acceptance database}"
case "$BLADE_PG_DSN" in *test*|*acceptance*) ;; *) echo "refusing non-isolated database" >&2; exit 2;; esac
exec go test -run '^$' -bench 'Benchmark(AssetExportSnapshot|FindingPages|BlackboardViewScale)' -benchmem -benchtime="${BLADE_BENCHTIME:-1x}" -count="${BLADE_BENCH_COUNT:-3}" -p 1 ./db ./agent
