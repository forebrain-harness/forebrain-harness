#!/usr/bin/env bash
# Regenerate assets/models.json from models.dev.
#
#   scripts/update-models-asset.sh [catalog.json]
#
# Without an argument it downloads https://models.dev/catalog.json?type=all
# (FOREBRAIN_MODELSDEV_CATALOG overrides with a local copy for an offline run).
#
# Three views are merged, because no single one is sufficient:
#   models                    canonical "provider/model" table: the entry set and
#                             the capability fields (reasoning/attachment/limit/type);
#   providers[opencode|opencode-go]
#                             the two login-backed surfaces are NOT in the canonical
#                             table; every one of their models is synthesizable into
#                             the canonical shape, so they are injected instead of
#                             being hand-maintained in pkg/llm/opencode;
#   providers[*]              reasoning_options exists ONLY here (the canonical table
#                             carries none), keyed by provider because the same model
#                             id has different levels per provider.
#
# Local overrides live in OVERRIDES and are the ONLY fields that may differ from
# upstream. Each one carries a dated reason.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="$repo_root/assets/models.json"

catalog="${1:-${FOREBRAIN_MODELSDEV_CATALOG:-}}"
cleanup=""
if [[ -z "$catalog" ]]; then
	catalog="$(mktemp)"
	cleanup="$catalog"
	curl -fsS -o "$catalog" "https://models.dev/catalog.json?type=all"
fi
if [[ -n "$cleanup" ]]; then
	trap 'rm -f "$cleanup"' EXIT
fi

python3 - "$catalog" "$out" <<'PYTHON'
import json
import sys

# The levels this build can send on some protocol. Anything else an effort
# description carries is dropped with a warning: an unknown level must never
# reach a picker it cannot honor.
EFFORT_LEVELS = ("none", "minimal", "low", "medium", "high", "xhigh", "max")

# The login-backed surfaces, injected into the canonical shape from the
# providers tree. Their catalogs arrive with the account login, so the static
# file is reference data for them (windows, modalities, reasoning levels) —
# pkg/turn's chat projection filters them out of a listing for that reason.
INJECTED_PROVIDERS = ("opencode", "opencode-go")
INJECTED_FIELDS = ("id", "name", "family", "attachment", "reasoning", "modalities", "limit", "type")

OVERRIDES = {
    # owner 2026-10-14: the DeepSeek endpoint routes this id to deepseek-flash,
    # which takes image input. Upstream's canonical row is coarser (its deepseek
    # provider lists four models, no routing detail), and pkg/run.ImageInputRefusal
    # reads exactly this field — taking upstream would make the owner's own default
    # model refuse images. Pinned by pkg/llm/catalog_test.go:189.
    "deepseek/deepseek-v4-flash": {"attachment": True, "modalities": {"input": ["text", "image"], "output": ["text"]}},
}


def provider_models(catalog, provider):
    return ((catalog.get("providers") or {}).get(provider) or {}).get("models") or {}


def main():
    catalog_path, out_path = sys.argv[1], sys.argv[2]
    with open(catalog_path, encoding="utf-8") as handle:
        catalog = json.load(handle)
    if not isinstance(catalog.get("models"), dict) or not isinstance(catalog.get("providers"), dict):
        sys.exit("upstream catalog has no top-level models/providers: the shape changed")

    entries = {key: dict(value) for key, value in catalog["models"].items()}
    canonical = len(entries)

    injected = 0
    for provider in INJECTED_PROVIDERS:
        for model_id, meta in provider_models(catalog, provider).items():
            entry = {field: meta[field] for field in INJECTED_FIELDS if field in meta}
            # The canonical table spells `id` as the full "provider/model" key
            # (the tree spells it bare), and consumers read it as that key.
            entry["id"] = "%s/%s" % (provider, model_id)
            entries["%s/%s" % (provider, model_id)] = entry
            injected += 1

    # reasoning_options exists only in the providers tree. An entry the tree
    # carries an empty list for keeps the empty list: "reasons, no discrete
    # level" is different data from "no row at all", and pkg/llm parses the
    # difference.
    dropped = 0
    for key, entry in entries.items():
        provider, _, model_id = key.partition("/")
        meta = provider_models(catalog, provider).get(model_id) or {}
        options = meta.get("reasoning_options")
        if options is None:
            continue
        cleaned = []
        for option in options:
            option = dict(option)
            if option.get("type") == "effort":
                values = []
                for value in option.get("values") or []:
                    if value in EFFORT_LEVELS:
                        values.append(value)
                        continue
                    dropped += 1
                    print("warning: dropped unknown effort %r for %s" % (value, key), file=sys.stderr)
                option["values"] = values
            cleaned.append(option)
        entry["reasoning_options"] = cleaned

    for key, fields in OVERRIDES.items():
        entry = entries.get(key)
        if entry is None:
            sys.exit("override %r names a model upstream no longer carries" % key)
        entry.update(fields)

    payload = json.dumps(entries, indent=4, sort_keys=True, ensure_ascii=False)
    with open(out_path, "w", encoding="utf-8", newline="\n") as handle:
        handle.write(payload)

    chat = sum(1 for entry in entries.values() if not str(entry.get("type") or "").strip())
    with_options = sum(1 for entry in entries.values() if entry.get("reasoning_options"))
    empty = sum(1 for entry in entries.values() if entry.get("reasoning_options") == [])
    print(
        "entries=%d canonical=%d injected=%d with-options=%d empty=%d chat=%d non-chat=%d overrides=%d dropped=%d bytes=%d"
        % (
            len(entries),
            canonical,
            injected,
            with_options,
            empty,
            chat,
            len(entries) - chat,
            len(OVERRIDES),
            dropped,
            len(payload.encode("utf-8")),
        )
    )


main()
PYTHON
