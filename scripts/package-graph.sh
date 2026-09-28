#!/usr/bin/env bash
set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "$root"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

go list -f '{{.ImportPath}}|{{.Dir}}' ./pkg/... > "$tmp/packages.txt"
while IFS='|' read -r path dir; do
  loc=$(find "$dir" -type f -name '*.go' ! -name '*_test.go' -exec wc -l {} + | awk 'END { print $1 + 0 }')
  jq -n --arg path "$path" --argjson loc "$loc" '{path: $path, loc: $loc}'
done < "$tmp/packages.txt" > "$tmp/loc.jsonl"

jq -s . "$tmp/loc.jsonl" > "$tmp/loc.json"
go list -json ./pkg/... > "$tmp/packages.json"
jq -s --slurpfile loc "$tmp/loc.json" '
  ($loc[0] | map({key: .path, value: .loc}) | from_entries) as $locs
  | [ .[]
      | select(.ImportPath | startswith("github.com/forebrain-harness/forebrain-harness/pkg/"))
      | {path: .ImportPath, imports: [.Imports[]? | select(startswith("github.com/forebrain-harness/forebrain-harness/pkg/"))]}
    ] as $all
  | [$all[] | .path as $from | .imports[] | {from: $from, to: .}] as $edges
  | ($edges | group_by(.to) | map({key: .[0].to, value: length}) | from_entries) as $fanIn
  | {
      packages: [$all[] | {
        path: .path,
        loc: ($locs[.path] // 0),
        fan_in: ($fanIn[.path] // 0),
        fan_out: (.imports | length)
      }],
      edges: $edges
    }
  | .packages |= sort_by(.path)
  | .edges |= sort_by(.from, .to)
' "$tmp/packages.json" > pkg/architecture/testdata/graph.json
