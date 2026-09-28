#!/usr/bin/env bash
# Check that every place a release version is written agrees with VERSION.
# release-please rewrites them together (release-please-config.json); this
# catches a file it was not told about.
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
version="$(tr -d '[:space:]' < "$ROOT_DIR/VERSION")"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "VERSION must be X.Y.Z, found '$version'" >&2; exit 1; }
pkg="$ROOT_DIR/npm/package.json"
[ "$(jq -r .version "$pkg")" = "$version" ] || { echo "npm/package.json version is not $version" >&2; exit 1; }
want='["@forebrain-harness/forebrain-darwin-arm64","@forebrain-harness/forebrain-darwin-x64","@forebrain-harness/forebrain-linux-arm64","@forebrain-harness/forebrain-linux-x64","@forebrain-harness/forebrain-win32-x64"]'
[ "$(jq -c '.optionalDependencies | keys' "$pkg")" = "$want" ] || { echo "npm/package.json optionalDependencies must list exactly the five platform packages" >&2; exit 1; }
bad="$(jq -r --arg v "$version" '.optionalDependencies | to_entries[] | select(.value != $v) | .key' "$pkg")"
[ -z "$bad" ] || { echo "npm/package.json optionalDependencies not at $version: $bad" >&2; exit 1; }
echo "release versions agree: $version"
