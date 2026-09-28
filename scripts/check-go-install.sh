#!/usr/bin/env bash
# Prove that `go install github.com/forebrain-harness/forebrain-harness/cmd/forebrain@<version>`
# works for the current tree the way a user runs it: outside any checkout, through a module
# proxy, with go.mod read as a dependency's go.mod (so replace/exclude directives are fatal).
#
#   scripts/check-go-install.sh
#
# The module zip is built from the files a clone would contain: tracked files plus untracked
# files that are not ignored.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
MODULE=github.com/forebrain-harness/forebrain-harness
VERSION=v0.0.0-installcheck
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
PROXY_DIR="$WORK/proxy/$MODULE/@v"
mkdir -p "$PROXY_DIR" "$WORK/bin" "$WORK/outside"

cd "$ROOT_DIR"
git ls-files -z --cached --others --exclude-standard > "$WORK/files"
python3 - "$WORK/files" "$PROXY_DIR/$VERSION.zip" "$MODULE@$VERSION" <<'PY'
import os, sys, zipfile
names = [n for n in open(sys.argv[1], 'rb').read().decode().split('\0') if n]
prefix = sys.argv[3] + '/'
with zipfile.ZipFile(sys.argv[2], 'w', zipfile.ZIP_DEFLATED) as z:
    for name in names:
        if os.path.islink(name) or not os.path.isfile(name):
            continue
        z.write(name, prefix + name)
PY
cp go.mod "$PROXY_DIR/$VERSION.mod"
printf '{"Version":"%s","Time":"2026-01-01T00:00:00Z"}\n' "$VERSION" > "$PROXY_DIR/$VERSION.info"
printf '%s\n' "$VERSION" > "$PROXY_DIR/list"

# The module cache turns a fixed fake version into a stale-tree check: when
# cache/download already holds this version, the go command never fetches the
# zip built above and compiles the sources unpacked by an earlier run instead.
# Drop this script's own cached entries (they are all named after its fake
# version) so the install below really consumes this tree.
MODCACHE="$(go env GOMODCACHE)"
chmod -R u+w "$MODCACHE/$MODULE@$VERSION" 2>/dev/null || true
rm -rf "$MODCACHE/$MODULE@$VERSION" \
  "$MODCACHE/cache/download/$MODULE/@v/$VERSION.info" \
  "$MODCACHE/cache/download/$MODULE/@v/$VERSION.lock" \
  "$MODCACHE/cache/download/$MODULE/@v/$VERSION.mod" \
  "$MODCACHE/cache/download/$MODULE/@v/$VERSION.zip" \
  "$MODCACHE/cache/download/$MODULE/@v/$VERSION.ziphash"

cd "$WORK/outside"
env -u GOFLAGS GOWORK=off GOBIN="$WORK/bin" CGO_ENABLED=1 \
  GOPROXY="file://$WORK/proxy,https://proxy.golang.org,direct" \
  GONOSUMDB="$MODULE" \
  go install -tags fts5 "$MODULE/cmd/forebrain@$VERSION"
got="$("$WORK/bin/forebrain" --version)"
[ "$got" = "$VERSION" ] || { echo "go-installed forebrain reports '$got', want '$VERSION'" >&2; exit 1; }
echo "go install $MODULE/cmd/forebrain@$VERSION: ok"
