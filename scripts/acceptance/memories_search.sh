#!/usr/bin/env bash
# Acceptance for memory search: prove that, with no manual step, a forebrain the
# npm packaging flow produced — or any other forebrain sharing its home — can
# search memories, including a Chinese query. A Chinese query is the point: only
# text with Han characters loads the word-segmentation dictionary, so only such
# a query exercises the install this plan fixes.
#
# What the script does NOT do is install anything for the binary under test. If
# that binary ships its dictionaries (the packaged one) it seeds its own home at
# startup (process.Seed); if it does not (a plain `go build`) it reads the home
# copy a previous run seeded. The binary preparing its own home is exactly the
# behaviour under test, so the script must never lend a hand.
#
# Env:
#   FOREBRAIN_BIN          the binary under test (required)
#   FOREBRAIN_ACCEPT_HOME  use a specific home instead of a fresh one (cases 2, 3)
#
# This deliberately does NOT read FOREBRAIN_HOME: a stray FOREBRAIN_HOME in the
# caller's environment (e.g. the owner's real home) must never be used here, or
# the run would seed and search inside a real install. The home is always
# exports FOREBRAIN_HOME to the binary under test, but never trusts an ambient
# one.
#
# Certify the three cases in this order:
#
#   1. npm install / local dev. A `npm link` (or npm install) launcher runs the
#      packaged binary under npm/dist, so running it directly is the same thing:
#      FOREBRAIN_BIN=<repo>/npm/dist/@forebrain-harness/forebrain-<os>-<arch>/vendor/<triple>/bin/forebrain \
#        scripts/acceptance/memories_search.sh
#      Expects PASS and that the home was seeded at startup, before any search.
#
#   2. another binary sharing that home. Reuse the home case 1 seeded, with a
#      binary that carries no dictionary of its own:
#      ( cd <repo> && CGO_ENABLED=1 go build -tags fts5 -o /tmp/fb-plain ./cmd/forebrain )
#      FOREBRAIN_BIN=/tmp/fb-plain FOREBRAIN_ACCEPT_HOME=<case 1's home> scripts/acceptance/memories_search.sh
#      Expects PASS: it reads the home copy.
#
#   3. an unwritable home. The copy beside the binary is read where it stands.
#      Prepare a home whose dict/ cannot be written, then:
#      FOREBRAIN_BIN=<the packaged binary> FOREBRAIN_ACCEPT_HOME=<that home> scripts/acceptance/memories_search.sh
#      Expects PASS — reading beside the binary is not a failure.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN="${FOREBRAIN_BIN:?set FOREBRAIN_BIN to the binary under test}"
WORK="${FOREBRAIN_ACCEPT_WORK:-${TMPDIR:-/tmp}/forebrain-memsearch}"
SESSION="${FOREBRAIN_ACCEPT_SESSION:-forebrain-memsearch}"
PORT="${FOREBRAIN_ACCEPT_PORT:-8751}"
PROJ="$WORK/proj"
DICT_ERROR="memory search dictionary unavailable"

die() { echo "memories search: $*" >&2; exit 1; }

# The caller's home when given (cases 2 and 3); a fresh one otherwise, so case 1
# observes the startup seed from nothing. Never FOREBRAIN_HOME — see above.
if [ -n "${FOREBRAIN_ACCEPT_HOME:-}" ]; then
  HOME_DIR="$FOREBRAIN_ACCEPT_HOME"
else
  HOME_DIR="$WORK/home"
  rm -rf "$HOME_DIR"
fi

wait_for() { # wait_for <seconds> <grep-pattern> -- polls the rendered screen
  local secs="$1" pat="$2" i
  for ((i = 0; i < secs * 5; i++)); do
    if tmux capture-pane -t "$SESSION" -p 2>/dev/null | grep -qF -- "$pat"; then return 0; fi
    sleep 0.2
  done
  echo "memories search: timed out after ${secs}s waiting for: $pat" >&2
  tmux capture-pane -t "$SESSION" -p 2>/dev/null >&2 || true
  return 1
}

screen() { tmux capture-pane -t "$SESSION" -p 2>/dev/null || true; }

cleanup() {
  local code=$?
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  [ -f "$WORK/provider.pid" ] && kill "$(cat "$WORK/provider.pid")" 2>/dev/null || true
  rm -f "$WORK/provider.pid"
  exit "$code"
}
trap cleanup EXIT

[ -x "$BIN" ] || die "FOREBRAIN_BIN=$BIN is not an executable"
command -v tmux >/dev/null 2>&1 || die "tmux is required"

mkdir -p "$WORK" "$HOME_DIR" "$PROJ"
echo "scratch project for the memories-search acceptance" > "$PROJ/README.md"

# Whether the home will accept a dictionary write decides whether we may assert
# the startup seed. An unwritable home must still pass (case 3); it just cannot
# be expected to hold a copy.
mkdir -p "$HOME_DIR/dict" 2>/dev/null || true
assert_seed=yes
if ! touch "$HOME_DIR/dict/.accept-probe" 2>/dev/null; then
  assert_seed=no
fi
rm -f "$HOME_DIR/dict/.accept-probe"

# api_key MUST be a ${ENV_NAME} reference: forebrain rejects a plaintext secret
# at startup and exits before the TUI paints. memories is on so the dedicated
# memory tools are registered; use_memories and dedicated_tools default on.
cat > "$HOME_DIR/forebrain.yaml" <<YAML
approval_policy: on-request
features:
  memories: true
agents:
  definitions:
    main:
      primary: true
      llm_providers:
      - provider: deepseek
        model: deepseek-v4-flash
        api_key: \${FAKE_LLM_KEY}
        base_url: http://127.0.0.1:$PORT
YAML

tmux kill-session -t "$SESSION" 2>/dev/null || true
# The fake provider answers the first request with a memories_search call whose
# queries are Chinese, which is what forces the dictionary to load, and later
# requests with the plain answer text.
FAKE_TOOL_NAME=memories_search \
FAKE_TOOL_ARGS='{"queries":["006 计划 修法","待拍板 决策"]}' \
  python3 "$REPO/scripts/acceptance/fake_provider.py" toolcall "$PORT" FAKE_ANSWER \
  > "$WORK/provider.log" 2>&1 &
echo $! > "$WORK/provider.pid"
for _ in $(seq 1 50); do
  grep -q LISTENING "$WORK/provider.log" 2>/dev/null && break
  sleep 0.1
done
grep -q LISTENING "$WORK/provider.log" 2>/dev/null || die "the fake provider never bound port $PORT"

tmux new-session -d -s "$SESSION" -x 140 -y 50 -c "$PROJ" \
  "FAKE_LLM_KEY=sk-local-fake FOREBRAIN_HOME=$HOME_DIR $BIN 2>$WORK/tui.err; echo EXIT=\$?; exec bash"

# First launch in a new directory asks whether the directory is trusted. Answer
# it inside the wait for the composer (a fixed window can miss a slow launch),
# once: the page opens on Quit, so Down then Enter.
trusted=no
for ((i = 0; i < 250; i++)); do
  pane="$(screen || true)"
  case "$pane" in
    *"Trust this directory?"*)
      if [ "$trusted" = no ]; then tmux send-keys -t "$SESSION" Down Enter; trusted=yes; fi
      ;;
    *"/ commands"*) break ;;
  esac
  sleep 0.2
done
wait_for 25 "/ commands" || die "the TUI never reached the composer; see $WORK/tui.err"

# The startup seed is the product behaviour that makes case 2 work: the binary
# prepared the home before any search.
if [ "$assert_seed" = yes ]; then
  [ -f "$HOME_DIR/dict/zh/s_1.txt" ] || die "the home was not seeded at startup: $HOME_DIR/dict/zh/s_1.txt is missing"
  [ -f "$HOME_DIR/dict/ja/ipa.dict" ] || die "the home was not seeded at startup: $HOME_DIR/dict/ja/ipa.dict is missing"
  echo "home seeded at startup: $HOME_DIR/dict"
else
  echo "home is not writable; expecting the copy beside the binary to be read instead"
fi

# Send a plain question; the provider answers it with the memory search.
tmux send-keys -t "$SESSION" -l "search my memories for the plan"
sleep 0.3
tmux send-keys -t "$SESSION" Enter

# The plain answer arrives only after the tool result has been fed back, so its
# appearance means the search ran and the turn completed.
wait_for 30 "FAKE_ANSWER" || die "the memory search turn never completed; see $WORK/tui.err"

rendered="$(screen)"
case "$rendered" in
  *"$DICT_ERROR"*) die "the search failed: '$DICT_ERROR' is on screen" ;;
esac
case "$rendered" in
  *memories_search*) : ;;
  *) die "the memories_search tool card never rendered" ;;
esac

echo "memories search: PASS"
