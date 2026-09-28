#!/usr/bin/env bash
# Guard pkg/gateway/dist: the built web UI is release-managed.
#
#   * release-please branches: rebuild the UI and require the committed dist
#     to match the frontend source, so the tagged commit embeds current UI;
#   * every other pull request: dist must not change at all — only the
#     release workflow (webui-into-release-pr in release.yml) rewrites it.
#
# Usage: check-webui-dist.sh <base-sha>
# On a release-please branch the base sha is not needed (the comparison is
# against a fresh build); everywhere else it is the merge base of the PR.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT_DIR"

if [ "${GITHUB_HEAD_REF:-}" != "${GITHUB_HEAD_REF#release-please--}" ]; then
  make ui
  if [ -n "$(git status --porcelain -- pkg/gateway/dist)" ]; then
    echo "pkg/gateway/dist does not match the frontend source; the release workflow rebuilds it on this branch" >&2
    exit 1
  fi
  exit 0
fi

base_sha="${1:?usage: check-webui-dist.sh <base-sha>}"
if ! git diff --quiet "${base_sha}"...HEAD -- pkg/gateway/dist; then
  echo "pull requests must not change pkg/gateway/dist; restore it with: git checkout origin/main -- pkg/gateway/dist" >&2
  exit 1
fi
