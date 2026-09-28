#!/usr/bin/env bash
# Build the forebrain-dict.tar.gz release asset: every memory.DictionaryFiles
# entry, laid out as under dict/ beside a binary. A binary installed with
# `go install` downloads it on first need (pkg/memory/dictionary.go).
set -euo pipefail

[ "$#" -eq 1 ] || { echo "usage: $0 <out-dir>" >&2; exit 2; }
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "$1"; OUT="$(cd "$1" && pwd)"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
"$ROOT_DIR/scripts/install-dictionary.sh" "$TMP"
tar -C "$TMP/dict" -czf "$OUT/forebrain-dict.tar.gz" .
echo "$OUT/forebrain-dict.tar.gz"
