#!/usr/bin/env bash
# Renders the package manifest report P10-7 asks CI to produce: package count,
# LOC, fan-in/fan-out per package, packages with no importer, and the state of
# the prompt-prefix cache baseline.
#
# This only *reports*. Every threshold in it is asserted by a guard test in
# pkg/architecture (package count, fan-out ratchet, production-file count,
# no-importer, cache baseline coverage), and CI fails through `go test`. Adding
# a second copy of the thresholds here would be exactly the duplicate
# implementation the guards exist to prevent.
set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "$root"

graph=pkg/architecture/testdata/graph.json
baseline=pkg/architecture/testdata/cache_baseline.json
out=${1:-manifest-report.md}

if [ ! -f "$graph" ]; then
  echo "missing $graph; run scripts/package-graph.sh first" >&2
  exit 1
fi

{
  echo "# Package manifest"
  echo
  echo "Generated from \`$graph\` at $(git rev-parse --short HEAD)."
  echo

  jq -r '
    "- packages: \(.packages | length)",
    "- production LOC: \([.packages[].loc] | add)",
    "- edges: \(.edges | length)"
  ' "$graph"
  echo

  echo "## Packages"
  echo
  echo "| package | LOC | fan-in | fan-out |"
  echo "| --- | ---: | ---: | ---: |"
  jq -r '.packages[] | "| \(.path | sub("^github.com/forebrain-harness/forebrain-harness/"; "")) | \(.loc) | \(.fan_in) | \(.fan_out) |"' "$graph"
  echo

  echo "## No importer inside pkg/"
  echo
  echo "Reported, not judged: the surfaces are imported from \`cmd/\` and the"
  echo "test-only packages from \`_test.go\` files, neither of which this graph"
  echo "records. TestEveryPackageHasAnImporter is what actually fails on a"
  echo "genuinely orphaned package."
  echo
  jq -r '.packages[] | select(.fan_in == 0) | "- \(.path | sub("^github.com/forebrain-harness/forebrain-harness/"; "")) (\(.loc) LOC)"' "$graph"
  echo

  echo "## Prompt prefix cache baseline"
  echo
  if [ -f "$baseline" ]; then
    jq -r '
      .providers | to_entries[]
      | .key as $p | .value.model as $m
      | ([.value.cases[] | .cache_read_input_tokens] | add) as $read
      | ([.value.cases[] | .cache_read_input_tokens + .cache_creation_input_tokens + .input_tokens] | add) as $total
      | "- \($p) (\($m)): hit rate \((100 * $read / $total) | floor)% over \(.value.cases | length) recorded cases"
    ' "$baseline"
  else
    echo "- no baseline recorded"
  fi
} > "$out"

echo "wrote $out"
