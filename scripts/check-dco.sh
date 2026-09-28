#!/usr/bin/env bash
# Check the Developer Certificate of Origin sign-off for every commit in
# <base>..<head>: each commit must carry a "Signed-off-by: Name <email>"
# trailer that matches its own author. Commits authored by GitHub bots
# (...[bot]@users.noreply.github.com) are exempt, and merge commits are
# skipped. Used by .github/workflows/dco.yml; can be run locally:
#
#   scripts/check-dco.sh <base> <head>

set -u

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <base> <head>" >&2
  exit 1
fi

base="$1"
head="$2"

signed_count=0
failed=0

for sha in $(git rev-list --no-merges "${base}..${head}"); do
  author_email="$(git log -1 --format='%ae' "$sha")"
  case "$author_email" in
    *'[bot]@users.noreply.github.com') continue ;;
  esac

  author="$(git log -1 --format='%an <%ae>' "$sha")"

  ok=0
  while IFS= read -r trailer; do
    [[ -z "$trailer" ]] && continue
    if [[ "$trailer" == "$author" ]]; then
      ok=1
      break
    fi
  done < <(git log -1 --format='%(trailers:key=Signed-off-by,valueonly)' "$sha")

  if [[ "$ok" -eq 1 ]]; then
    signed_count=$((signed_count + 1))
  else
    short_sha="$(git rev-parse --short "$sha")"
    subject="$(git log -1 --format='%s' "$sha")"
    echo "${short_sha} \"${subject}\" is missing \"Signed-off-by: ${author}\"; sign it with: git rebase --signoff ${base}"
    failed=1
  fi
done

if [[ "$failed" -eq 1 ]]; then
  exit 1
fi

echo "DCO: ${signed_count} commit(s) signed off"
