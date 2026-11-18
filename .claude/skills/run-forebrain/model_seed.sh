#!/usr/bin/env bash
# Seed the harness's model row into a scratch home's state database.
#
# The startup gate judges by the install-wide model table (fb_llm_models,
# plans 262/315): a yaml llm_providers list no longer configures anything, and
# a fresh home with only such a list lands in the onboarding wizard instead of
# the composer. Every acceptance script therefore seeds its row(s) here.
#
# The home's database is created by one headless `gateway start` pass — it
# migrates the schema and imports the yaml's runtime sections before it
# listens; the pass is killed as soon as the import's marker lands. A home
# whose gateway is already up (web_e2e) skips the pass. A model row's key is
# plaintext in the table by design (owner 2026-11-07).
#
# The key arrives on STDIN, never as an argument: real acceptance keys must
# not reach a command line (ps), and the SQL goes to sqlite3 on stdin for the
# same reason.
#
# usage: printf '%s' "<api_key>" | model_seed.sh <bin> <home> <provider> <model> <base_url> [id] [created_at_ms] [params_json]
set -euo pipefail

[ $# -ge 5 ] && [ $# -le 8 ] || { echo "usage: printf '%s' \"<api_key>\" | model_seed.sh <bin> <home> <provider> <model> <base_url> [id] [created_at_ms] [params_json]" >&2; exit 2; }
BIN="$1"; HOME_DIR="$2"; PROVIDER="$3"; MODEL="$4"; BASE_URL="$5"
ROW_ID="${6:-harness-seeded-model}"
CREATED_AT="${7:-$(python3 -c 'import time; print(int(time.time()*1000))')}"
PARAMS_JSON="${8:-}"
# printf '%s' sends no trailing newline: read reports EOF but still fills the
# variable — keep what it read.
IFS= read -r API_KEY || true

DB="$HOME_DIR/state/forebrain.state.sqlite"

# Ready means the import finished, not just that migrations created the
# tables: the startup gate reads the model table only through the composed
# config, which activates once fb_config holds sections. The import marker
# lands last (config_import.go), so killing the pass before it races the
# import and leaves a home the gate judges unconfigured.
ready() {
  sqlite3 "$DB" "SELECT 1 FROM fb_llm_models LIMIT 1;" >/dev/null 2>&1 || return 1
  sqlite3 "$DB" "SELECT 1 FROM fb_config WHERE section='meta.legacy_yaml_imported';" 2>/dev/null | grep -q 1
}

if ! ready; then
  FOREBRAIN_HOME="$HOME_DIR" "$BIN" gateway start </dev/null >"$HOME_DIR/gw-init.log" 2>&1 &
  gwpid=$!
  for ((i = 0; i < 150; i++)); do
    ready && break
    sleep 0.1
  done
  kill "$gwpid" 2>/dev/null || true
  wait "$gwpid" 2>/dev/null || true
  ready || { echo "model_seed: headless init never finished the config import; see $HOME_DIR/gw-init.log" >&2; exit 1; }
fi

# One upsert per row id: a re-serve updates the row in place (a new port must
# not leave a stale row ahead of it in created_at order). created_at survives
# the conflict so a multi-row seed keeps its ordering.
sql_key="${API_KEY//\'/\'\'}"
sql_params="${PARAMS_JSON//\'/\'\'}"
printf "INSERT INTO fb_llm_models (id, provider, model, base_url, api_key, params_json, created_at, updated_at)
  VALUES ('%s', '%s', '%s', '%s', '%s', '%s', %s, %s)
  ON CONFLICT(id) DO UPDATE SET provider=excluded.provider, model=excluded.model,
    base_url=excluded.base_url, api_key=excluded.api_key, params_json=excluded.params_json, updated_at=excluded.updated_at;\n" \
  "$ROW_ID" "$PROVIDER" "$MODEL" "$BASE_URL" "$sql_key" "$sql_params" "$CREATED_AT" "$CREATED_AT" \
  | sqlite3 "$DB"
echo "model_seed: $PROVIDER/$MODEL -> $BASE_URL (row $ROW_ID)"