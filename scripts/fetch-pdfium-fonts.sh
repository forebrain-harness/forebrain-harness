#!/usr/bin/env bash
# Fetch the Noto Sans SC/TC fallback fonts PDFium uses to render CJK text.
#
# A PDF that references a standard CJK font without embedding it (STSong-Light,
# SimSun, AdobeSongStd — the norm in government forms and reports) has no glyph
# outlines of its own; PDFium falls back to a font on the machine, and without
# one draws tofu boxes. This repository ships Noto Sans SC (GB2312 / Simplified)
# and Noto Sans TC (Big5 / Traditional) for that fallback; pkg/pdf hands their
# directory to PDFium's font loader (UserFontPaths) and pkg/process seeds them
# into $FOREBRAIN_HOME/fonts at startup.
#
# The upstream Noto Sans CJK OTFs are distributed as Git LFS blobs and are not
# reachable as raw files from this build environment; the same upstream files
# are carried verbatim by the EmbedPDF npm packages, whose licence (SIL OFL
# 1.1) permits redistribution:
#
#   @embedpdf/fonts-sc@1.0.0  fonts/NotoSansHans-Regular.otf  (Noto Sans SC)
#   @embedpdf/fonts-tc@1.0.0  fonts/NotoSansHant-Regular.otf  (Noto Sans TC)
#
# They land in assets/fonts/ under the names the runtime uses:
#
#   assets/fonts/NotoSansSC-Regular.otf
#   assets/fonts/NotoSansTC-Regular.otf
#
# The fonts themselves are NOT committed (like third_party/libtokenizers/); the
# script, the pinned versions and the sha256s are. See assets/fonts/README.md.
#
# Idempotent: a file whose sha256 already matches is left alone, so a CI cache
# hit costs nothing.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
DEST_DIR="$ROOT_DIR/assets/fonts"

die() { echo "fetch-pdfium-fonts: $*" >&2; exit 1; }
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

# fetch_font <npm-package> <version> <path-in-package> <dest-name> <sha256>
fetch_font() {
  local pkg="$1" version="$2" inner="$3" dest="$4" want="$5"
  local out="$DEST_DIR/$dest"
  if [ -f "$out" ] && [ "$(sha256 "$out")" = "$want" ]; then
    log "$dest already at $version"
    return 0
  fi

  local tgz_name
  tgz_name="${pkg##*/}-$version.tgz"
  local url="https://registry.npmjs.org/$pkg/-/$tgz_name"

  local tmp
  tmp="$(mktemp -d)"
  trap "rm -rf '$tmp'" RETURN
  log "downloading $pkg@$version"
  curl -fsSL --retry 3 --retry-all-errors -o "$tmp/pkg.tgz" "$url" \
    || die "download failed: $url"
  tar -xzf "$tmp/pkg.tgz" -C "$tmp" "$inner" \
    || die "$pkg@$version does not contain $inner"

  local got
  got="$(sha256 "$tmp/$inner")"
  [ "$got" = "$want" ] || die "$inner sha256 mismatch: got $got, want $want"

  mkdir -p "$DEST_DIR"
  mv -f "$tmp/$inner" "$out"
  log "$dest -> $out"
}

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"

fetch_font "@embedpdf/fonts-sc" "1.0.0" "package/fonts/NotoSansHans-Regular.otf" \
  "NotoSansSC-Regular.otf" "7db3c634bbd0301b3082a80ffcac2f422e8ef9ce0730f8b279c3e8f756598f68"
fetch_font "@embedpdf/fonts-tc" "1.0.0" "package/fonts/NotoSansHant-Regular.otf" \
  "NotoSansTC-Regular.otf" "81f39cffba8b45367a21249720782603b852bee71aad710f8f66fec3d3776af3"
