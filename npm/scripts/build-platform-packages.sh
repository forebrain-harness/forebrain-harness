#!/usr/bin/env bash
# Build per-platform npm packages for forebrain.
# Output: npm/dist/<pkg>/  (one dir per platform)
#   vendor/{triple}/bin/forebrain{,.exe}
#   vendor/{triple}/bin/dict/        (word-segmentation dictionary, read from
#                                     beside the binary; see scripts/install-dictionary.sh)
#   vendor/{triple}/bin/models.json  (model catalog, read from beside the
#                                     binary; see scripts/install-assets.sh)
#   vendor/{triple}/bin/tokenizers/  (bundled tokenizer files, read/installed
#                                     from beside the binary; see
#                                     scripts/install-assets.sh and
#                                     assets/tokenizers/README.md)
#   package.json (with os/cpu fields)
#
# The web UI is rebuilt here by default (`make ui`) so every platform binary
# embeds the current frontend. Set FOREBRAIN_SKIP_UI_BUILD=1 to embed the
# pkg/gateway/dist already in the tree instead (the release pipeline does
# this: its tag carries the UI committed by the release PR).
# Every platform (including windows/amd64) is built with CGO_ENABLED=1 so the
# weixin silk voice decoder, sqlite, and other cgo features are always present.
# Cross-compiling therefore needs the matching C toolchain; this script honors
# FOREBRAIN_BUILD_TARGETS (default: only the host triple) so local dev does not
# need every C toolchain. For windows/amd64 cross-builds install mingw-w64
# (provides x86_64-w64-mingw32-gcc) or override FOREBRAIN_CC_WINDOWS_AMD64.
#
# Every target also links the tokenizers static library, fetched per target by
# scripts/fetch-libtokenizers.sh (called below). darwin/linux download a pinned
# prebuilt archive; windows/amd64 is compiled from the pinned Rust source, so a
# windows/amd64 build needs git, cargo, and the x86_64-pc-windows-gnu Rust
# target (added by the fetch script when rustup is present).
#
# Every target also links the PDFium native library (pkg/pdf), fetched per
# target by scripts/fetch-pdfium.sh (called below) and found at build time
# through PKG_CONFIG_PATH — so the build host needs pkg-config (the release
# matrix installs it; see .github/workflows/release.yml). The shared library
# (libpdfium.{dylib,so} / pdfium.dll) is copied into the package beside the
# binary and the binary is linked with an rpath that reaches it there
# (@loader_path on darwin, $ORIGIN on linux; Windows finds a DLL next to the
# .exe). The CJK fallback fonts are fetched once by
# scripts/fetch-pdfium-fonts.sh and ship as the "fonts" asset.
#
# Env:
#   FOREBRAIN_VERSION            — package version (default: the VERSION file's content)
#   FOREBRAIN_BUILD_TARGETS      — space-separated targets (e.g. "darwin/arm64 linux/amd64 windows/amd64"); default = host
#   FOREBRAIN_SKIP_UI_BUILD      — set to 1 to skip the web UI rebuild and embed pkg/gateway/dist as-is
#   FOREBRAIN_CC_WINDOWS_AMD64   — C compiler for windows/amd64 cross-build (default: x86_64-w64-mingw32-gcc)
#   FOREBRAIN_CC_LINUX_ARM64     — C compiler for linux/arm64 cross-build (default: aarch64-linux-gnu-gcc)
#   FOREBRAIN_CC_LINUX_AMD64     — C compiler override for linux/amd64 cross-build
#   FOREBRAIN_CC_DARWIN_ARM64    — C compiler override for darwin/arm64 cross-build
#   FOREBRAIN_CC_DARWIN_AMD64    — C compiler override for darwin/amd64 cross-build

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
NPM_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ROOT_DIR="$(cd "$NPM_DIR/.." && pwd)"

FOREBRAIN_VERSION="${FOREBRAIN_VERSION:-$(tr -d '[:space:]' < "$ROOT_DIR/VERSION")}"

DEFAULT_TARGET="$(go env GOOS)/$(go env GOARCH)"
TARGETS="${FOREBRAIN_BUILD_TARGETS:-$DEFAULT_TARGET}"

if [ "${FOREBRAIN_SKIP_UI_BUILD:-}" = "1" ]; then
  [ -f "$ROOT_DIR/pkg/gateway/dist/index.html" ] || { echo "pkg/gateway/dist has no web UI build; run make ui first" >&2; exit 1; }
else
  echo "==> building the web UI (make ui)"
  make -C "$ROOT_DIR" ui || {
    echo "web UI build failed; fix the frontend build, or set FOREBRAIN_SKIP_UI_BUILD=1 to embed the existing pkg/gateway/dist" >&2
    exit 1
  }
  echo "==> web UI rebuilt into pkg/gateway/dist (release-managed: do not commit local rebuilds; scripts/check-webui-dist.sh rejects PR changes)"
fi

DIST_DIR="$NPM_DIR/dist"
rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

# The CJK fallback fonts are one target-independent set; fetch them once so
# install-assets.sh (per target, below) ships them as the "fonts" asset.
"$ROOT_DIR/scripts/fetch-pdfium-fonts.sh"

triple_for() {
  case "$1" in
    darwin/arm64) echo aarch64-apple-darwin ;;
    darwin/amd64) echo x86_64-apple-darwin ;;
    linux/amd64)  echo x86_64-unknown-linux-gnu ;;
    linux/arm64)  echo aarch64-unknown-linux-gnu ;;
    windows/amd64) echo x86_64-pc-windows-gnu ;;
    *) return 1 ;;
  esac
}

pkg_name_for() {
  case "$1" in
    darwin/arm64) echo forebrain-darwin-arm64 ;;
    darwin/amd64) echo forebrain-darwin-x64 ;;
    linux/amd64)  echo forebrain-linux-x64 ;;
    linux/arm64)  echo forebrain-linux-arm64 ;;
    windows/amd64) echo forebrain-win32-x64 ;;
    *) return 1 ;;
  esac
}

npm_os_for() {
  case "${1%%/*}" in
    darwin) echo darwin ;;
    linux)  echo linux ;;
    windows) echo win32 ;;
    *) return 1 ;;
  esac
}

npm_cpu_for() {
  case "${1##*/}" in
    amd64) echo x64 ;;
    arm64) echo arm64 ;;
    *) return 1 ;;
  esac
}

build_target() {
  local target="$1"
  local goos="${target%%/*}"
  local goarch="${target##*/}"
  local triple pkg_short pkg_dir
  triple="$(triple_for "$target")"
  pkg_short="$(pkg_name_for "$target")"
  pkg_dir="$DIST_DIR/@forebrain-harness/$pkg_short"
  mkdir -p "$pkg_dir/vendor/$triple/bin"

  local bin_ext=""
  [ "$goos" = "windows" ] && bin_ext=".exe"

  # The release binary is built as `make build` builds it: -trimpath keeps the
  # build machine's paths out of it, and -s -w drop the symbol table and DWARF
  # debug info, which a user never reads (panic stack traces come from the
  # runtime's own tables and survive the strip).
  #
  # CGO is required on every platform (weixin silk voice decoding, sqlite, etc.).
  # When cross-compiling to a foreign OS we must point cgo at the matching C
  # toolchain; the host toolchain only works for native builds.
  # The cross C compiler is exported through a shell variable assignment, not
  # spliced into the command line: a CC value with spaces ("clang -arch
  # x86_64") must stay one assignment or the shell looks for a command named
  # "CC=clang -arch x86_64".
  local cc=
  local host_goos host_goarch
  host_goos="$(go env GOHOSTOS)"
  host_goarch="$(go env GOHOSTARCH)"
  if [ "$goos" != "$host_goos" ] || [ "$goarch" != "$host_goarch" ]; then
    case "$target" in
      windows/amd64) cc="${FOREBRAIN_CC_WINDOWS_AMD64:-x86_64-w64-mingw32-gcc}" ;;
      linux/amd64)   cc="${FOREBRAIN_CC_LINUX_AMD64:-}" ;;
      linux/arm64)   cc="${FOREBRAIN_CC_LINUX_ARM64:-aarch64-linux-gnu-gcc}" ;;
      darwin/arm64)  cc="${FOREBRAIN_CC_DARWIN_ARM64:-}" ;;
      darwin/amd64)  cc="${FOREBRAIN_CC_DARWIN_AMD64:-}" ;;
    esac
  fi

  echo "==> building $target ($triple)"
  "$ROOT_DIR/scripts/fetch-libtokenizers.sh" "$target"
  # PDFium links through `// #cgo pkg-config: pdfium`, so the build needs the
  # target's pdfium.pc on PKG_CONFIG_PATH. The shared library also ships beside
  # the binary; an rpath that reaches the binary's own directory makes the
  # package work wherever it is unpacked.
  "$ROOT_DIR/scripts/fetch-pdfium.sh" "$target"
  local pdfium_dir="$ROOT_DIR/third_party/pdfium/${goos}_${goarch}"
  local pdfium_rpath= pdfium_lib_glob=
  case "$goos" in
    darwin) pdfium_rpath='-Wl,-rpath,@loader_path'; pdfium_lib_glob="libpdfium.dylib" ;;
    linux)  pdfium_rpath='-Wl,-rpath,$ORIGIN';      pdfium_lib_glob="libpdfium.so" ;;
    windows) pdfium_lib_glob="pdfium.dll" ;;
  esac
  (
    cd "$ROOT_DIR"
    CC="$cc" CGO_ENABLED=1 GOOS="$goos" GOARCH="$goarch" \
      PKG_CONFIG_PATH="$pdfium_dir" \
      CGO_LDFLAGS="$pdfium_rpath" \
      go build \
        -trimpath \
        -tags fts5 \
        -ldflags "-s -w -X github.com/forebrain-harness/forebrain-harness/pkg/home.Version=v$FOREBRAIN_VERSION" \
        -o "$pkg_dir/vendor/$triple/bin/forebrain${bin_ext}" \
        ./cmd/forebrain
  )
  chmod +x "$pkg_dir/vendor/$triple/bin/forebrain${bin_ext}" || true
  # The PDFium shared library rides beside the binary (darwin/linux in lib/,
  # windows in bin/), which is where the rpath above — or the Windows loader —
  # looks for it.
  local pdfium_src="$pdfium_dir/lib/$pdfium_lib_glob"
  [ "$goos" = "windows" ] && pdfium_src="$pdfium_dir/bin/$pdfium_lib_glob"
  cp "$pdfium_src" "$pkg_dir/vendor/$triple/bin/"
  # The dictionary, the model catalog and the CJK fonts ship in the same package
  # as the binary, so one install puts them all where forebrain reads them.
  "$ROOT_DIR/scripts/install-dictionary.sh" "$pkg_dir/vendor/$triple/bin"
  "$ROOT_DIR/scripts/install-assets.sh" "$pkg_dir/vendor/$triple/bin"
  # A package that cannot search memory is a package to reject here, not a
  # failure for the user to discover later. The dictionaries ship beside the
  # binary (above); the runtime seeds them into the forebrain home on first
  # need, which is what makes every later binary sharing that home search.
  ( cd "$ROOT_DIR" && go run ./cmd/installdict -verify "$pkg_dir/vendor/$triple/bin" )

  cat >"$pkg_dir/package.json" <<JSON
{
  "name": "@forebrain-harness/$pkg_short",
  "version": "$FOREBRAIN_VERSION",
  "description": "forebrain native binary for $target",
  "license": "Apache-2.0",
  "os": ["$(npm_os_for "$target")"],
  "cpu": ["$(npm_cpu_for "$target")"],
  "files": ["vendor/"],
  "repository": {
    "type": "git",
    "url": "https://github.com/forebrain-harness/forebrain-harness.git"
  }
}
JSON
}

for target in $TARGETS; do
  build_target "$target"
done

echo "==> done. packages under $DIST_DIR"
ls -1 "$DIST_DIR/@forebrain-harness" 2>/dev/null || true
