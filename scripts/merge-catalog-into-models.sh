#!/usr/bin/env bash
# Regenerate assets/models.json from a local models.dev snapshot, offline.
#
#   scripts/merge-catalog-into-models.sh [catalog.json]
#
# The snapshot defaults to assets/catalog.json — owner-supplied input, ignored
# by git and never shipped. This is the one entry point a developer or a package
# build runs: it checks the snapshot and the interpreter, then calls
# scripts/update-models-asset.sh, which carries the merge itself. Only
# assets/models.json is committed.
#
# The product never refreshes this data: no runtime download, no scheduled
# workflow (owner 2026-11-09).
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
catalog="${1:-$repo_root/assets/catalog.json}"

if [[ ! -f "$catalog" ]]; then
	echo "no models.dev snapshot at $catalog" >&2
	echo "download https://models.dev/catalog.json?type=all to that path (or pass one as the single argument), then rerun;" >&2
	echo "the file is local input: never commit it — only assets/models.json is committed" >&2
	exit 2
fi

if ! command -v python3 >/dev/null 2>&1; then
	echo "python3 is required to regenerate assets/models.json from $catalog" >&2
	echo "install python3 (the merge is a python script reading the JSON snapshot)" >&2
	exit 2
fi

# The shape the merge depends on: a canonical models table and a providers tree
# keyed by provider. A snapshot from another source is a STOP, not a re-merge.
python3 - "$catalog" <<'PY'
import json
import sys

path = sys.argv[1]
try:
    with open(path, encoding="utf-8") as handle:
        catalog = json.load(handle)
except (OSError, ValueError) as err:
    sys.exit("catalog snapshot %s is not readable JSON: %s" % (path, err))
if not isinstance(catalog.get("models"), dict) or not isinstance(catalog.get("providers"), dict):
    sys.exit(
        "catalog snapshot %s has no top-level models/providers tables: "
        "download https://models.dev/catalog.json?type=all instead" % path
    )
PY

status=0
"$repo_root/scripts/update-models-asset.sh" "$catalog" || status=$?
if [[ "$status" -ne 0 ]]; then
	echo "regeneration failed (exit $status): assets/models.json was not accepted" >&2
	exit "$status"
fi

cat <<'NOTE'
Regenerated assets/models.json from the snapshot above.
Review the diff and commit ONLY assets/models.json: the snapshot is local input
and is gitignored, and nothing ships it.
NOTE
