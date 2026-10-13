#!/usr/bin/env bash
# Install the runtime assets (models.json) beside a forebrain binary.
#
#   scripts/install-assets.sh <bin-dir>
#
# The runtime reads models.json from <bin-dir> at startup, where <bin-dir> is
# the directory holding the forebrain executable; it is not compiled into the
# binary. The list and the copying live in Go (llm.InstallAssets,
# cmd/installassets), so this only runs that.

set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <bin-dir>" >&2
  exit 2
fi

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "$1"
BIN_DIR="$(cd "$1" && pwd)"
cd "$ROOT_DIR" && go run ./cmd/installassets "$BIN_DIR"
