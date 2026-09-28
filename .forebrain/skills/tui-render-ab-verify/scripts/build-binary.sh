#!/usr/bin/env bash
# Build one side of an A/B pair into <work>/forebrain.bin.
#
#   build-binary.sh new <work-dir>   # build the current working tree
#   build-binary.sh old <work-dir>   # build a throwaway worktree of clean HEAD
#
# The driver launches <work>/forebrain, so the real binary is parked at
# <work>/forebrain.bin and the driver-facing path is written by drive-card.sh (a
# wrapper that can strip COLORTERM for the 256-colour path).
set -euo pipefail

SIDE="${1:-}"
WORK="${2:-}"
REPO="${FOREBRAIN_REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)}"

die() { echo "build-binary: $*" >&2; exit 1; }
[ -n "$SIDE" ] && [ -n "$WORK" ] || die "usage: build-binary.sh <new|old> <work-dir>"

mkdir -p "$WORK"
BIN="$WORK/forebrain.bin"

# CGO and the fts5 tag are both mandatory: the state store is SQLite with
# full-text search compiled in, and the binary will not link without them.
case "$SIDE" in
  new)
    ( cd "$REPO" && CGO_ENABLED=1 go build -tags fts5 -o "$BIN" ./cmd/forebrain )
    echo "built working tree -> $BIN"
    ;;
  old)
    WT="$WORK/head-tree"
    if [ ! -e "$WT/.git" ]; then
      # A worktree of HEAD, never of the dirty working tree: the "before" side
      # must be the committed baseline, not a mixture of the user's edits.
      git -C "$REPO" worktree add --detach "$WT" HEAD >/dev/null
    fi
    ( cd "$WT" && CGO_ENABLED=1 go build -tags fts5 -o "$BIN" ./cmd/forebrain )
    echo "built HEAD worktree -> $BIN"
    ;;
  *) die "side must be new or old, got $SIDE" ;;
esac
