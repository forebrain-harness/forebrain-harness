#!/usr/bin/env bash
# Install the runtime assets (models.json) beside a forebrain binary.
#
#   scripts/install-assets.sh <bin-dir>
#
# The runtime reads models.json from <bin-dir> — the directory holding the
# forebrain executable — and from nowhere else; it is not compiled into the
# binary. The file carries per-model capability data: the model type (`type`,
# set only for non-chat models), the context/output `limit`, `attachment`,
# `reasoning`, and `reasoning_options` (the thinking levels a model offers,
# from which both the TUI and the web build their effort pickers).
#
# That copy is the whole catalog: there is no per-home overlay, no copy
# installed into $FOREBRAIN_HOME and no runtime download (owner 2026-11-09).
# A user's capability changes live in the model configuration (web 设置 › 模型).
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
