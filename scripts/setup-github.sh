#!/usr/bin/env bash
# One-time (and idempotent) GitHub configuration for this repository: merge
# strategy, security features, the dependencies label, the npm environment,
# the main branch ruleset, and a presence check of the variable and secrets
# the automation needs.
#
# Usage: scripts/setup-github.sh [--dry-run] [owner/repo]
#
# --dry-run prints every command instead of running it and never touches the
# network. The script never reads or accepts secret values: it only checks
# that the variable and secrets exist and prints the gh commands that set
# whatever is missing.
set -euo pipefail

usage() {
  echo "usage: $0 [--dry-run] [owner/repo]" >&2
}

REPO=forebrain-harness/forebrain-harness
DRY_RUN=false
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=true ;;
    -h|--help) usage; exit 0 ;;
    */*) REPO="$arg" ;;
    *) usage; exit 2 ;;
  esac
done

if [ "$DRY_RUN" = false ] && ! command -v gh >/dev/null 2>&1; then
  echo "error: gh is required outside --dry-run" >&2
  exit 1
fi

RULESET_FILE="$(cd "$(dirname "$0")" && pwd)/setup-github/ruleset-main.json"

# run CMD… executes CMD, or prints it under --dry-run.
run() {
  if [ "$DRY_RUN" = true ]; then
    echo "dry-run: $*"
  else
    "$@"
  fi
}

echo "==> $REPO: merge strategy (squash only, PR title and body)"
run gh api -X PATCH "repos/$REPO" \
  -F allow_squash_merge=true -F allow_merge_commit=false -F allow_rebase_merge=false \
  -f squash_merge_commit_title=PR_TITLE -f squash_merge_commit_message=PR_BODY \
  -F allow_auto_merge=true -F delete_branch_on_merge=true -F allow_update_branch=true

echo "==> security: private vulnerability reporting, alerts, automated fixes"
run gh api -X PUT "repos/$REPO/private-vulnerability-reporting"
run gh api -X PUT "repos/$REPO/vulnerability-alerts"
run gh api -X PUT "repos/$REPO/automated-security-fixes"

echo "==> label: dependencies"
run gh label create dependencies --color 0366d6 --description "Dependency updates" --force -R "$REPO"

echo "==> environment: npm"
run gh api -X PUT "repos/$REPO/environments/npm"

echo "==> ruleset: main (from setup-github/ruleset-main.json)"
if [ "$DRY_RUN" = true ]; then
  run gh api "repos/$REPO/rulesets" --jq '.[] | select(.name=="main") | .id'
  run gh api -X POST "repos/$REPO/rulesets" --input "$RULESET_FILE"
  echo "dry-run: if a ruleset named main already exists, the live run PUTs repos/$REPO/rulesets/<id> instead"
else
  ruleset_id="$(gh api "repos/$REPO/rulesets" --jq '.[] | select(.name=="main") | .id')"
  if [ -n "$ruleset_id" ]; then
    gh api -X PUT "repos/$REPO/rulesets/$ruleset_id" --input "$RULESET_FILE"
  else
    gh api -X POST "repos/$REPO/rulesets" --input "$RULESET_FILE"
  fi
fi

echo "==> required variable and secrets"
REQUIRED_VARIABLES=(FOREBRAIN_APP_ID)
REQUIRED_SECRETS=(FOREBRAIN_APP_PRIVATE_KEY NPM_TOKEN)
if [ "$DRY_RUN" = true ]; then
  run gh variable list -R "$REPO"
  run gh secret list -R "$REPO"
  echo "dry-run: the live run then checks for variable ${REQUIRED_VARIABLES[*]} and secrets ${REQUIRED_SECRETS[*]}"
else
  variables="$(gh variable list -R "$REPO" --json name --jq '.[].name')"
  secrets="$(gh secret list -R "$REPO" --json name --jq '.[].name')"
  missing=()
  for name in "${REQUIRED_VARIABLES[@]}"; do
    grep -qx "$name" <<<"$variables" || missing+=("variable:$name")
  done
  for name in "${REQUIRED_SECRETS[@]}"; do
    grep -qx "$name" <<<"$secrets" || missing+=("secret:$name")
  done
  if [ "${#missing[@]}" -gt 0 ]; then
    echo "missing configuration — set these, then re-run this script:"
    for item in "${missing[@]}"; do
      kind="${item%%:*}"
      name="${item#*:}"
      if [ "$kind" = variable ]; then
        echo "  gh variable set $name --body <value> -R $REPO"
      else
        echo "  gh secret set $name < file -R $REPO"
      fi
    done
    exit 1
  fi
  echo "all required variable and secrets are set"
fi

echo "done."
