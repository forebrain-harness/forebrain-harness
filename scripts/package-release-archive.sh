#!/usr/bin/env bash
# Package one platform's npm package directory as a GitHub Release archive.
#
# Usage: package-release-archive.sh <target> <version> <out-dir>
#   <target>   a FOREBRAIN_BUILD_TARGETS entry, e.g. darwin/arm64
#   <version>  release version without the leading v (e.g. 0.1.0)
#   <out-dir>  directory the archive is written to (created if missing)
#
# The archive contains what the binary needs in place: forebrain[.exe] and the
# dict/ directory beside it. It is built from npm/dist/@forebrain-harness/<pkg>
# so the Release asset and the npm platform package carry the same binary.
set -euo pipefail

target="${1:?usage: package-release-archive.sh <target> <version> <out-dir>}"
version="${2:?usage: package-release-archive.sh <target> <version> <out-dir>}"
out_arg="${3:?usage: package-release-archive.sh <target> <version> <out-dir>}"

case "$target" in
  darwin/arm64)  triple=aarch64-apple-darwin;      pkg=forebrain-darwin-arm64 ;;
  darwin/amd64)  triple=x86_64-apple-darwin;       pkg=forebrain-darwin-x64 ;;
  linux/amd64)   triple=x86_64-unknown-linux-gnu;  pkg=forebrain-linux-x64 ;;
  linux/arm64)   triple=aarch64-unknown-linux-gnu; pkg=forebrain-linux-arm64 ;;
  windows/amd64) triple=x86_64-pc-windows-gnu;     pkg=forebrain-win32-x64 ;;
  *) echo "unknown target: $target" >&2; exit 1 ;;
esac

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
bin_dir="$ROOT_DIR/npm/dist/@forebrain-harness/$pkg/vendor/$triple/bin"

mkdir -p "$out_arg"
out_dir="$(cd "$out_arg" && pwd)"

goos="${target%%/*}"
goarch="${target##*/}"

if [ "$goos" = "windows" ]; then
  # windows-latest runners ship 7z; zip keeps the .exe extension and the
  # Windows Explorer "extract all" flow working.
  archive="$out_dir/forebrain_v${version}_${goos}_${goarch}.zip"
  (cd "$bin_dir" && 7z a -tzip "$archive" . >/dev/null)
else
  archive="$out_dir/forebrain_v${version}_${goos}_${goarch}.tar.gz"
  tar -C "$bin_dir" -czf "$archive" .
fi

echo "$archive"
