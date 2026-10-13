#!/usr/bin/env bash
# Record the viewport painter's real bytes for term_replay.py.
#
#   paint_dump.sh <out-dir> [commit]
#
# Copies paint_dump_test.go.tmpl into pkg/tui as a temporary test, runs it with
# FB_PAINT_DUMP=<out-dir>, and deletes it again whatever happens. With a commit
# it runs in a throwaway git worktree at that commit instead, so code from
# before a change can be recorded without touching this checkout - never
# disable a fix in the working tree to get the "before" picture: the owner
# commits by hand and other sessions share the tree.
set -euo pipefail

SKILL="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$SKILL/../../.." && pwd)"
OUT="${1:?usage: paint_dump.sh <out-dir> [commit]}"
COMMIT="${2:-}"

rm -rf "$OUT" && mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"

TREE="$REPO"
WT=""
cleanup() {
  rm -f "$TREE/pkg/tui/zz_paint_dump_test.go"
  if [ -n "$WT" ]; then git -C "$REPO" worktree remove --force "$WT" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

if [ -n "$COMMIT" ]; then
  WT="$(mktemp -d "${TMPDIR:-/tmp}/fb-paint-wt.XXXXXX")"
  git -C "$REPO" worktree add --quiet --detach "$WT" "$COMMIT"
  # The tokenizer archive is gitignored, so a fresh worktree lacks it; copying
  # the one this checkout already fetched avoids a download.
  cp -R "$REPO/third_party/libtokenizers" "$WT/third_party/"
  TREE="$WT"
fi

cp "$SKILL/paint_dump_test.go.tmpl" "$TREE/pkg/tui/zz_paint_dump_test.go"
( cd "$TREE" && FB_PAINT_DUMP="$OUT" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '^TestZZPaintDump$' -count=1 2>&1 \
    | grep -v 'ld: warning' )
echo "$(ls "$OUT" | wc -l | tr -d ' ') step files in $OUT${COMMIT:+ (at $COMMIT)}"
