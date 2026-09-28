#!/usr/bin/env bash
# Check a commit message (or a pull request title) against this project's
# Conventional Commits contract. This script is the single source of truth:
# .githooks/commit-msg, .github/workflows/pr-title.yml and CONTRIBUTING.md
# all defer to it.
#
# Usage:
#   scripts/check-commit-message.sh <message-file>
#   scripts/check-commit-message.sh -              read the message from stdin
#   scripts/check-commit-message.sh --strict -     CI mode (see below)
#
# Rules:
#   <type>(<scope>)!: <subject>
#   - type    one of: feat fix perf refactor test docs build ci chore revert
#   - scope   [a-z0-9._/-]+ (optional)
#   - !       optional breaking-change marker
#   - subject starts with a lowercase letter or digit and does not end
#             with a period
#   - the whole line is at most 100 characters
#
# Without --strict, titles that git itself generates are allowed through:
# "fixup! ", "squash! ", "amend! ", "Merge " and `Revert "` (they disappear
# on squash or are written by git). CI passes --strict when checking PR titles.

set -u

types='feat|fix|perf|refactor|test|docs|build|ci|chore|revert'
pattern="^(${types})(\([a-z0-9._/-]+\))?!?: [a-z0-9].*[^.]\$"
max_length=100

strict=0
message_file=

for arg in "$@"; do
  case "$arg" in
    --strict) strict=1 ;;
    -) message_file=- ;;
    *) message_file="$arg" ;;
  esac
done

if [[ -z "$message_file" ]]; then
  echo "usage: $0 [--strict] <message-file|->" >&2
  exit 1
fi

# The title is the first line that is neither blank nor a comment.
title=
if [[ "$message_file" == "-" ]]; then
  input=/dev/stdin
else
  if [[ ! -r "$message_file" ]]; then
    echo "cannot read message file: $message_file" >&2
    exit 1
  fi
  input="$message_file"
fi
while IFS= read -r line || [[ -n "$line" ]]; do
  case "$line" in
    ''|'#'*) ;;
    *) title="$line"; break ;;
  esac
done < "$input"

fail() {
  echo "PR title must look like \"fix(state): keep the exec timing triple whole\" (type: ${types}); got: ${title}"
  exit 1
}

[[ -n "$title" ]] || fail

if [[ "$strict" -eq 0 ]]; then
  case "$title" in
    'fixup! '*|'squash! '*|'amend! '*|'Merge '*|'Revert "'*) exit 0 ;;
  esac
fi

grep -Eq "$pattern" <<<"$title" || fail
(( ${#title} <= max_length )) || fail

exit 0
