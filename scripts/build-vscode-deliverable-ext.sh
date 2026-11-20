#!/usr/bin/env bash
# Rebuilds the embedded VSIX after editing pkg/tui/vscode_ext/{package.json,extension.js}.
# The .vsix is git-tracked: go:embed needs it present in a fresh clone.
set -euo pipefail
cd "$(dirname "$0")/../pkg/tui/vscode_ext"
# Build outside the source dir: a .vsix left inside it would be packaged into
# the next .vsix by vsce.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
npx --yes @vscode/vsce@2.32.0 package --allow-missing-repository --out "$tmp/deliverable-open.vsix"
mv -f "$tmp/deliverable-open.vsix" deliverable-open.vsix
