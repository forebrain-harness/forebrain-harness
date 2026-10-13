#!/usr/bin/env bash
# Fetch the static library every forebrain binary links.
#
# forebrain counts tokens through github.com/daulet/tokenizers, a cgo binding to
# the HuggingFace Rust `tokenizers` crate. That Go module ships no C archive: it
# is linked against a prebuilt `libtokenizers.a` (or one compiled from the Rust
# source). This script puts that archive where the build can find it, in-repo:
#
#   third_party/libtokenizers/<goos>_<goarch>/libtokenizers.a
#   third_party/libtokenizers/<goos>_<goarch>/.version   (the module version)
#
# The location is fixed and committed-adjacent (the archive itself is
# gitignored); pkg/llm/hftok/link.go points the cgo linker at it with ${SRCDIR},
# so any `go build` / `go test` in this checkout links with no environment
# variable. `go install` builds from the module cache, where the archive is
# absent: see README for the CGO_LDFLAGS a user passing that archive must set.
#
# Usage:
#   scripts/fetch-libtokenizers.sh [goos/goarch ...]
#
# With no arguments the host triple is fetched. Each argument is one target,
# e.g. `scripts/fetch-libtokenizers.sh darwin/arm64 linux/amd64`.
#
# Windows has no official prebuilt archive, so it is compiled here from the
# pinned Rust source with cargo (needs git, cargo, and the
# x86_64-pc-windows-gnu target, added automatically with rustup when present).
# The other targets download the pinned release asset and verify its sha256.
#
# Idempotent: a target whose archive already matches the version in go.mod is
# left alone, so a CI cache hit costs nothing.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
MODULE="github.com/daulet/tokenizers"
DEST_ROOT="$ROOT_DIR/third_party/libtokenizers"

die() { echo "fetch-libtokenizers: $*" >&2; exit 1; }

# Version comes from go.mod: the archive only needs to be replaced when that
# line changes, and a missing line means the dependency was never added.
version="$(awk -v m="$MODULE" '$1==m {print $2}' "$ROOT_DIR/go.mod")"
[ -n "$version" ] || die "$MODULE is not required in go.mod"

sha256() { # sha256 <file>
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    die "neither sha256sum nor shasum is available"
  fi
}

# The Rust crate version the Go module expects to link against. tokenizer.go
# declares `extern void tokenizers_version_<ver>(void);`, and the linker fails
# unless the archive exports exactly that symbol. Read it from the unpacked
# module so the check tracks the dependency instead of a hardcoded copy.
required_version_symbol() {
  local gofile
  gofile="$(go env GOMODCACHE)/$MODULE@$version/tokenizer.go"
  [ -f "$gofile" ] || return 0
  sed -n 's/.*extern void tokenizers_version_\([0-9_]*\)(void).*/\1/p' "$gofile" | head -1
}

# Confirm the archive exports the version symbol the Go module links against.
# A mismatch (or a truncated download) surfaces here, not as an opaque linker
# error deep in cgo.
verify_symbol() { # verify_symbol <archive>
  command -v nm >/dev/null 2>&1 || return 0
  local want sym out
  want="$(required_version_symbol)"
  [ -n "$want" ] || return 0
  sym="tokenizers_version_$want"
  # nm exits non-zero when the archive was built by a newer LLVM than the local
  # one, yet still prints the symbols it can read; capture stdout rather than
  # trusting the exit status (with pipefail a non-zero nm would look like a
  # missing symbol). Feed grep a here-string, not a pipe: grep -q stops at the
  # first match, and pipefail would read the resulting SIGPIPE as a failure.
  out="$(nm "$1" 2>/dev/null || true)"
  if ! grep -q -- "$sym" <<<"$out"; then
    die "$1 does not export $sym; the archive does not match $MODULE@$version"
  fi
}

# asset_and_sha <goos>/<goarch> -> echoes "<asset> <sha256>"; nonzero if unknown.
asset_and_sha() {
  case "$1" in
    darwin/arm64) echo "libtokenizers.darwin-arm64.tar.gz fb84b8b2e349a5952767ffe80ccd862fc44084de47f3b0cc3f0b7c9d4e649cf7" ;;
    darwin/amd64) echo "libtokenizers.darwin-x86_64.tar.gz 6239efe5a81fde8089ef2df8ae710366542b4e5deab6d8ecb74d7d1862db2ddb" ;;
    linux/amd64)  echo "libtokenizers.linux-amd64.tar.gz 72556cdca798dd4ea7cdaba308e5f0d68a8cb93b67c96edf485b7a0edd7b07f4" ;;
    linux/arm64)  echo "libtokenizers.linux-arm64.tar.gz e96545ad05930c26f51f63d932ee6d3bbd32bbed149e102c5290d587a2293067" ;;
    *) return 1 ;;
  esac
}

fetch_download() { # fetch_download <target> <dest>
  local target="$1" dest="$2" asset sha
  if ! read -r asset sha < <(asset_and_sha "$target"); then
    die "no known prebuilt archive for $target at $version; update the asset table in this script"
  fi
  local url="https://github.com/daulet/tokenizers/releases/download/$version/$asset"
  local tmp
  tmp="$(mktemp -d)"
  trap "rm -rf '$tmp'" RETURN
  echo "==> downloading $asset ($version)" >&2
  curl -fsSL --retry 3 --retry-all-errors -o "$tmp/$asset" "$url" \
    || die "download failed: $url"
  local got
  got="$(sha256 "$tmp/$asset")"
  [ "$got" = "$sha" ] || die "$asset sha256 mismatch: got $got, want $sha"
  tar -xzf "$tmp/$asset" -C "$tmp"
  [ -f "$tmp/libtokenizers.a" ] || die "$asset has no libtokenizers.a"
  mv -f "$tmp/libtokenizers.a" "$dest"
}

fetch_build_windows() { # fetch_build_windows <dest>
  local dest="$1" triple=x86_64-pc-windows-gnu
  command -v git >/dev/null 2>&1 || die "git is required to build the Windows archive"
  command -v cargo >/dev/null 2>&1 || die "cargo (Rust) is required to build the Windows archive"
  if command -v rustup >/dev/null 2>&1; then
    rustup target add "$triple" || die "rustup target add $triple failed"
  fi
  local tmp
  tmp="$(mktemp -d)"
  trap "rm -rf '$tmp'" RETURN
  echo "==> building libtokenizers.a for $triple ($version)" >&2
  git clone --depth 1 --branch "$version" "https://github.com/daulet/tokenizers" "$tmp/src" \
    || die "git clone of $MODULE@$version failed"
  # On Windows (Git Bash) cargo may not find a C/C++ compiler for the GNU
  # target unless it is named; mingw's gcc/g++ satisfy it.
  local extra_env=()
  if [ "$(uname -s)" = "MINGW"* ] || [ "$(uname -s)" = "MSYS"* ]; then
    extra_env=(CC_x86_64_pc_windows_gnu=gcc CXX_x86_64_pc_windows_gnu=g++)
  fi
  ( cd "$tmp/src" && env "${extra_env[@]}" cargo build --release --locked -p tokenizers-ffi --target "$triple" ) \
    || die "cargo build for $triple failed"
  mv -f "$tmp/src/target/$triple/release/libtokenizers_ffi.a" "$dest"
}

fetch_one() { # fetch_one <goos>/<goarch>
  local target="$1"
  local goos="${target%%/*}" goarch="${target##*/}"
  local dir="$DEST_ROOT/${goos}_${goarch}"
  local dest="$dir/libtokenizers.a" verfile="$dir/.version"
  if [ -f "$dest" ] && [ -f "$verfile" ] && [ "$(cat "$verfile")" = "$version" ]; then
    echo "==> $target already at $version" >&2
    return 0
  fi
  mkdir -p "$dir"
  case "$target" in
    windows/amd64) fetch_build_windows "$dest" ;;
    *) fetch_download "$target" "$dest" ;;
  esac
  verify_symbol "$dest"
  printf '%s\n' "$version" > "$verfile"
  echo "==> $target -> $dest" >&2
}

targets=("$@")
if [ "${#targets[@]}" -eq 0 ]; then
  targets=("$(go env GOOS)/$(go env GOARCH)")
fi

for target in "${targets[@]}"; do
  case "$target" in
    */*) ;;
    *) die "target \"$target\" must be <goos>/<goarch>" ;;
  esac
  fetch_one "$target"
done
