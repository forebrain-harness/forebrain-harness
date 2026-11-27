#!/usr/bin/env bash
# Builds editors/vscode into a VSIX under editors/vscode/out/ (git-ignored).
# The build runs outside the package directory so no stray .vsix is packaged
# into the next one; vsce is pinned to the version the sibling script uses.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
pkg="$root/editors/vscode"
# The native app is bundled into the extension: build the webview first, from
# frontend/ (the pnpm workspace root), with the lockfile frozen so a cold clone
# resolves exactly what was committed.
( cd "$root/frontend" && corepack pnpm install --frozen-lockfile && corepack pnpm --filter @forebrain/vscode-webview build )
cd "$pkg"
if [ -f package-lock.json ]; then npm ci --no-audit --no-fund; else npm install --no-audit --no-fund; fi
npm run typecheck
npm test
npm run compile
mkdir -p "$pkg/out"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
version="$(node -p "require('./package.json').version")"
vsix="forebrain-vscode-$version.vsix"
npx --yes @vscode/vsce@2.32.0 package --allow-missing-repository --out "$tmp/$vsix"
mv -f "$tmp/$vsix" "$pkg/out/$vsix"
echo "built $pkg/out/$vsix"