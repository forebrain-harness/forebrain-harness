#!/usr/bin/env bash
# Install the runtime assets (models.json) beside a forebrain binary.
#
#   scripts/install-assets.sh <bin-dir>
#
# The runtime reads models.json from <bin-dir> — the directory holding the
# forebrain executable — as its shipped base; it is not compiled into the
# binary. The file carries per-model capability data: the model type (`type`,
# set only for non-chat models), the context/output `limit`, `attachment`,
# `reasoning`, and `reasoning_options` (the thinking levels a model offers,
# from which both the TUI and the web build their effort pickers).
#
# $FOREBRAIN_HOME/models.json is a different thing: the user's overlay over
# that base, merged per key and per field when the catalog loads, written once
# as a full copy if the home has none and never touched afterwards.
#
# The list and the copying live in Go (llm.InstallAssets, cmd/installassets),
# so this only runs that.

set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <bin-dir>" >&2
  exit 2
fi

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "$1"
BIN_DIR="$(cd "$1" && pwd)"
cd "$ROOT_DIR" && go run ./cmd/installassets "$BIN_DIR"
