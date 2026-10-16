#!/usr/bin/env bash
# Fetch the PDFium native library every forebrain binary links.
#
# forebrain renders PDF pages through github.com/klippa-app/go-pdfium, a cgo
# binding whose C files carry `// #cgo pkg-config: pdfium`. That directive is
# the whole wiring: with a `pdfium.pc` on PKG_CONFIG_PATH pointing at the
# include directory and the shared library, `go build` compiles and links with
# no other environment variable. This script puts that library and a matching
# `pdfium.pc` in-repo, per target:
#
#   third_party/pdfium/<goos>_<goarch>/include/        (headers)
#   third_party/pdfium/<goos>_<goarch>/lib/            (libpdfium.{dylib,so})
#   third_party/pdfium/<goos>_<goarch>/bin/            (pdfium.dll, Windows only)
#   third_party/pdfium/<goos>_<goarch>/pdfium.pc       (pkg-config file)
#   third_party/pdfium/<goos>_<goarch>/.version        (the pinned release)
#
# The location is fixed and committed-adjacent (the library itself is
# gitignored, like third_party/libtokenizers/); the Makefile points
# PKG_CONFIG_PATH at the host triple's directory. PDFium is Apache-2.0 and the
# go-pdfium wrapper is MIT — never swap in an AGPL engine.
#
# Usage:
#   scripts/fetch-pdfium.sh [goos/goarch ...]
#
# With no arguments the host triple is fetched. FOREBRAIN_PDFIUM_TARGETS, a
# space-separated list, overrides the arguments (so CI can request several).
# Each target is one of the supported triples below — the same five platforms
# the release pipeline builds.
#
# The release is pinned (RELEASE_TAG); the numeric asset ids are NOT — they are
# resolved at run time from that tag's asset list by file name, because the ids
# change on every release. The sha256 of each asset is pinned in the table
# below, taken from RELEASE_TAG; a moved or re-cut release fails the check
# loudly instead of silently linking something else.
#
# Idempotent: a target whose .version matches RELEASE_TAG and whose library is
# present is left alone, so a CI cache hit costs nothing.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
DEST_ROOT="$ROOT_DIR/third_party/pdfium"

# The bblanchon/pdfium-binaries release this repository links against.
RELEASE_TAG="chromium/8100"
RELEASE_TAG_ENCODED="chromium%2F8100"
PDFIUM_VERSION="157.0.8100.0"

RELEASES_API="https://api.github.com/repos/bblanchon/pdfium-binaries"
ASSETS_API="$RELEASES_API/releases/assets"

# api.github.com is rate-limited hard for anonymous callers (60/h per IP); CI
# runs several fetches per push. Use the workflow token when the environment
# provides one; local fetches stay anonymous.
gh_curl_opts=()
if [ -n "${GITHUB_TOKEN:-}${GH_TOKEN:-}" ]; then
  tok="${GITHUB_TOKEN:-}"
  [ -n "$tok" ] || tok="$GH_TOKEN"
  gh_curl_opts=(-H "Authorization: Bearer $tok")
fi

die() { echo "fetch-pdfium: $*" >&2; exit 1; }
log() { echo "==> $*" >&2; }

sha256() { # sha256 <file>
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    die "neither sha256sum nor shasum is available"
  fi
}

# asset_and_sha <goos>/<goarch> -> echoes "<asset> <sha256>"; nonzero if unknown.
asset_and_sha() {
  case "$1" in
    darwin/arm64)  echo "pdfium-mac-arm64.tgz 7ea647c7a7a9c1169be3f947000bc071ece2fade2cd6bd7e401e3675c6560cce" ;;
    darwin/amd64)  echo "pdfium-mac-x64.tgz d513a0530a0f0c1ca9929ded06660e27edb9a108a7d18d9df55a5fb060b091c8" ;;
    linux/amd64)   echo "pdfium-linux-x64.tgz 1c1894a12d4d96ee01400c3874444126254180c5c7d2ab628576b67f8120320a" ;;
    linux/arm64)   echo "pdfium-linux-arm64.tgz 58c87a95a40a24c914a9c755fae798843f46f06adf9e5e9f2c893302d4bdb040" ;;
    windows/amd64) echo "pdfium-win-x64.tgz 51621bcc1d9be9ea448f4b84b0516862b928e3968d3fca899af1a6d6e2848d6d" ;;
    *) return 1 ;;
  esac
}

# resolve_asset_id <asset-name>: the numeric id, looked up in the pinned
# release's asset list (never hardcoded).
resolve_asset_id() {
  local name="$1" json="$2" id
  id="$(jq -r --arg n "$name" '.assets[] | select(.name == $n) | .id' "$json" | head -1)"
  [ -n "$id" ] || die "asset $name is not in release $RELEASE_TAG"
  printf '%s' "$id"
}

write_pc() { # write_pc <dir> <libdir-rel> <extra-libs>
  local dir="$1" lib="$2" extra="$3"
  cat > "$dir/pdfium.pc" <<EOF
prefix=$dir
libdir=\${prefix}/$lib
includedir=\${prefix}/include

Name: pdfium
Description: PDFium $PDFIUM_VERSION ($RELEASE_TAG)
Version: $PDFIUM_VERSION
Libs: -L\${libdir} -lpdfium $extra
Cflags: -I\${includedir}
EOF
}

# GNU ld (MinGW) searches -lpdfium as libpdfium.dll.a pdfium.dll.a libpdfium.a
# pdfium.lib libpdfium.lib cygpdfium.dll libpdfium.dll pdfium.dll — never the
# MSVC short-import name the archive ships (lib/pdfium.dll.lib). pdfium.lib,
# which the sequence does try, is the same archive, so alias it. Both the
# fresh-extract and the idempotent short-circuit paths run this: a cache hit
# must not leave a windows dir un-aliased by an older script version.
ensure_windows_alias() { # ensure_windows_alias <dir>
  local dir="$1"
  if [ -f "$dir/lib/pdfium.dll.lib" ] && [ ! -f "$dir/lib/pdfium.lib" ]; then
    cp "$dir/lib/pdfium.dll.lib" "$dir/lib/pdfium.lib"
  fi
  [ -f "$dir/lib/pdfium.lib" ] || die "windows archive has no import library (lib/pdfium.dll.lib)"
}

fetch_one() { # fetch_one <goos>/<goarch> <release-json>
  local target="$1" json="$2"
  local goos="${target%%/*}" goarch="${target##*/}"
  local dir="$DEST_ROOT/${goos}_${goarch}"
  local verfile="$dir/.version"
  if [ -f "$verfile" ] && [ "$(cat "$verfile")" = "$RELEASE_TAG" ] && [ -d "$dir/lib" ]; then
    if [ "$goos" = "windows" ]; then
      ensure_windows_alias "$dir"
    fi
    log "$target already at $RELEASE_TAG"
    return 0
  fi

  local asset sha
  if ! read -r asset sha < <(asset_and_sha "$target"); then
    die "no PDFium build for $target; the supported triples are darwin/{arm64,amd64}, linux/{amd64,arm64}, windows/amd64"
  fi
  local id
  id="$(resolve_asset_id "$asset" "$json")"

  local tmp
  tmp="$(mktemp -d)"
  trap "rm -rf '$tmp'" RETURN
  log "downloading $asset ($RELEASE_TAG)"
  curl -fsSL --retry 3 --retry-all-errors \
    ${gh_curl_opts[@]+"${gh_curl_opts[@]}"} \
    -H 'Accept: application/octet-stream' \
    -o "$tmp/$asset" "$ASSETS_API/$id" \
    || die "download failed: $ASSETS_API/$id"
  local got
  got="$(sha256 "$tmp/$asset")"
  [ "$got" = "$sha" ] || die "$asset sha256 mismatch: got $got, want $sha"

  rm -rf "$dir"
  mkdir -p "$dir"
  tar -xzf "$tmp/$asset" -C "$dir"
  [ -d "$dir/include" ] || die "$asset has no include/ directory"
  [ -d "$dir/lib" ] || die "$asset has no lib/ directory"

  case "$goos" in
    darwin)
      # The shipped dylib's install name is the relative "./libpdfium.dylib",
      # which dyld cannot resolve from an arbitrary working directory. Give it
      # an @rpath name and let the pc add -Wl,-rpath so the built binary finds
      # the library next to the checkout.
      if command -v install_name_tool >/dev/null 2>&1; then
        install_name_tool -id "@rpath/libpdfium.dylib" "$dir/lib/libpdfium.dylib"
      fi
      write_pc "$dir" lib "-Wl,-rpath,\${libdir}"
      ;;
    linux)
      write_pc "$dir" lib "-Wl,-rpath,\${libdir}"
      ;;
    windows)
      # An MSVC/MinGW link uses the import library in lib/ and loads
      # bin/pdfium.dll at run time.
      ensure_windows_alias "$dir"
      # pkgconfiglite is a native Windows pkg-config: hand it a Windows
      # prefix, not the POSIX path Git Bash computes (PKG_CONFIG_PATH was
      # already cygpath-converted by CI; the .pc contents must match).
      if command -v cygpath >/dev/null 2>&1; then
        write_pc "$(cygpath -w "$dir")" lib ""
      else
        write_pc "$dir" lib ""
      fi
      ;;
  esac

  printf '%s\n' "$RELEASE_TAG" > "$verfile"
  log "$target -> $dir"
}

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"
command -v jq >/dev/null 2>&1 || die "jq is required to resolve asset ids"
command -v pkg-config >/dev/null 2>&1 || die "pkg-config is required: go-pdfium links through 'cgo pkg-config: pdfium'"

targets=("$@")
if [ "${#targets[@]}" -eq 0 ] && [ -n "${FOREBRAIN_PDFIUM_TARGETS:-}" ]; then
  read -r -a targets <<< "$FOREBRAIN_PDFIUM_TARGETS"
fi
if [ "${#targets[@]}" -eq 0 ]; then
  targets=("$(go env GOOS)/$(go env GOARCH)")
fi

# Fetch the pinned release's asset list once; every target resolves its id here.
release_json="$(mktemp)"
trap 'rm -f "$release_json"' EXIT
curl -fsSL --retry 3 --retry-all-errors \
  ${gh_curl_opts[@]+"${gh_curl_opts[@]}"} \
  "$RELEASES_API/releases/tags/$RELEASE_TAG_ENCODED" -o "$release_json" \
  || die "could not read release $RELEASE_TAG"

for target in "${targets[@]}"; do
  case "$target" in
    */*) ;;
    *) die "target \"$target\" must be <goos>/<goarch>" ;;
  esac
  fetch_one "$target" "$release_json"
done

host_dir="$DEST_ROOT/$(go env GOOS)_$(go env GOARCH)"
if [ -f "$host_dir/pdfium.pc" ]; then
  log "host build: PKG_CONFIG_PATH=$host_dir:\${PKG_CONFIG_PATH}"
fi
