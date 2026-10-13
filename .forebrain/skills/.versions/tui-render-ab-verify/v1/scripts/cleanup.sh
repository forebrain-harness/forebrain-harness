#!/usr/bin/env bash
# Tear down everything an A/B run left behind: tmux sessions, fake providers,
# and the clean-HEAD worktree.
#
#   cleanup.sh [work-dir] [session ...]
#
# The root dirs themselves are left in place so binaries are not rebuilt; pass
# them to `rm -rf` yourself if you want the disk back (each build is ~100 MB).
set -euo pipefail

WORK="${1:-}"
shift || true
REPO="${FOREBRAIN_REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)}"

# Sessions first: killing one takes its provider's port with it.
for s in "$@"; do
  tmux kill-session -t "$s" 2>/dev/null && echo "killed session $s" || true
done
pkill -f fake_provider.py 2>/dev/null && echo "stopped fake providers" || true

if [ -n "$WORK" ] && [ -e "$WORK/head-tree/.git" ]; then
  # A registered worktree left behind makes later `git worktree` commands fail
  # with "already registered", so remove it rather than just deleting the dir.
  git -C "$REPO" worktree remove --force "$WORK/head-tree" >/dev/null 2>&1 \
    && echo "removed worktree $WORK/head-tree" || true
fi

git -C "$REPO" worktree prune
echo "done"
