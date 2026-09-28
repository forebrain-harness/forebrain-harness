#!/usr/bin/env bash
# Install the word-segmentation dictionaries beside a forebrain binary.
#
#   scripts/install-dictionary.sh <bin-dir>
#
# Memory search reads its dictionaries from <bin-dir>/dict at runtime, where
# <bin-dir> is the directory holding the forebrain executable; they are not
# compiled into the binary. The list and the copying live in Go
# (memory.DictionaryFiles, cmd/installdict), so this only runs that.

set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <bin-dir>" >&2
  exit 2
fi

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "$1"
BIN_DIR="$(cd "$1" && pwd)"
cd "$ROOT_DIR" && go run ./cmd/installdict "$BIN_DIR"
