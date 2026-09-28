#!/usr/bin/env bash
# Acceptance for the MCP startup work, on the real TUI binary.
#
# It mirrors .claude/skills/run-forebrain/driver.sh: same isolated FOREBRAIN_HOME, same
# fake provider, same tmux capture. The one difference is the configuration it
# writes, which is the point — the TUI has to be driven with an MCP server that
# does not answer, because that is the case this work is about.
#
# A second, separate work dir and port are used so a run of the stock driver is
# not disturbed.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WORK="${FOREBRAIN_MCP_ACC_WORK:-${TMPDIR:-/tmp}/forebrain-mcp-acc}"
SESSION="${FOREBRAIN_MCP_ACC_SESSION:-forebrain-mcp-acc}"
PORT="${FOREBRAIN_MCP_ACC_PORT:-8742}"
BIN="$WORK/forebrain"
HOME_DIR="$WORK/home"
PROJ="$WORK/proj"

die() { echo "acc: $*" >&2; exit 1; }

wait_for() { # wait_for <seconds> <fixed-pattern>
  local secs="$1" pat="$2" i
  for ((i = 0; i < secs * 5; i++)); do
    if tmux capture-pane -t "$SESSION" -p 2>/dev/null | grep -qF -- "$pat"; then return 0; fi
    sleep 0.2
  done
  echo "acc: timed out after ${secs}s waiting for: $pat" >&2
  tmux capture-pane -t "$SESSION" -p 2>/dev/null >&2 || true
  return 1
}

cmd_build() {
  mkdir -p "$WORK"
  ( cd "$REPO" && CGO_ENABLED=1 go build -tags fts5 -o "$BIN" ./cmd/forebrain )
  echo "built $BIN"
}

start_provider() {
  python3 "$REPO/.claude/skills/run-forebrain/fake_provider.py" "${1:-hang}" "$PORT" FAKE_ANSWER 0.6 \
    > "$WORK/provider.log" 2>&1 &
  echo $! > "$WORK/provider.pid"
  local i
  for ((i = 0; i < 50; i++)); do
    grep -q LISTENING "$WORK/provider.log" 2>/dev/null && break
    sleep 0.1
  done
  grep -q LISTENING "$WORK/provider.log" || die "fake provider never bound port $PORT"
}

cmd_start() { # start [--with-mcp] [provider-mode] : launch the TUI, optionally with a broken MCP server
  local with_mcp="${1:-}"
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  [ -x "$BIN" ] || cmd_build
  mkdir -p "$HOME_DIR" "$PROJ"
  echo "scratch project for driving the forebrain TUI" > "$PROJ/README.md"

  if [ "$with_mcp" = "--with-mcp" ]; then
    # required-docs never answers, so its startup ends on its own deadline;
    # optional-slow never answers either but is not required, which is the pair
    # the Esc-skip path needs. A stdio entry is validated for name and command
    # only, so these are exactly what the runner starts.
    cat > "$HOME_DIR/forebrain.yaml" <<YAML
approval_policy: on-request
agents:
  defaults:
    mcp_servers:
    - name: required-docs
      transport: stdio
      command: /bin/sleep
      args: ["60"]
      required: true
      startup_timeout: ${REQ_TIMEOUT:-90}
    - name: optional-slow
      transport: stdio
      command: /bin/sleep
      args: ["60"]
      startup_timeout: ${OPT_TIMEOUT:-90}
  definitions:
    main:
      primary: true
      enable_subagent: false
      llm_providers:
      - provider: deepseek
        model: deepseek-chat
        api_key: \${FAKE_LLM_KEY}
        base_url: http://127.0.0.1:$PORT
YAML
  else
    cat > "$HOME_DIR/forebrain.yaml" <<YAML
approval_policy: on-request
agents:
  definitions:
    main:
      primary: true
      enable_subagent: false
      llm_providers:
      - provider: deepseek
        model: deepseek-chat
        api_key: \${FAKE_LLM_KEY}
        base_url: http://127.0.0.1:$PORT
YAML
  fi

  start_provider "${2:-hang}"

  local started_at
  started_at=$(python3 -c 'import time; print(time.time())')
  tmux new-session -d -s "$SESSION" -x 120 -y 40 -c "$PROJ" \
    "FAKE_LLM_KEY=sk-local-fake FOREBRAIN_HOME=$HOME_DIR $BIN 2>$WORK/tui.err; echo EXIT=\$?; exec bash"

  local trusted=no i pane
  for ((i = 0; i < 300; i++)); do
    pane="$(tmux capture-pane -t "$SESSION" -p 2>/dev/null || true)"
    case "$pane" in
      *"Trust this directory?"*)
        if [ "$trusted" = no ]; then
          tmux send-keys -t "$SESSION" Down Enter   # the page opens on Quit
          trusted=yes
          # The composer appearing is the first frame; record when it did.
          first_frame_at=$(python3 -c 'import time; print(time.time())')
          echo "first_frame_after_trust_secs=$(python3 -c "print(round($first_frame_at-$started_at,2))")"
        fi
        ;;
      *"/ commands"*) break ;;
    esac
    sleep 0.05
  done
  wait_for 30 "/ commands" || die "TUI never reached the composer; see $WORK/tui.err"
  if [ "$trusted" = no ]; then
    first_frame_at=$(python3 -c 'import time; print(time.time())')
    echo "first_frame_secs=$(python3 -c "print(round($first_frame_at-$started_at,2))")"
  fi
  echo "started: session=$SESSION home=$HOME_DIR port=$PORT mcp=$with_mcp"
}

cmd_screen() { tmux capture-pane -t "$SESSION" -p; }
cmd_key() { tmux send-keys -t "$SESSION" "$@"; }
cmd_submit() { tmux send-keys -t "$SESSION" -l "$*"; sleep 0.5; tmux send-keys -t "$SESSION" Enter; }
cmd_send() { tmux send-keys -t "$SESSION" -l "$*"; }
cmd_wait() { wait_for "${2:-20}" "$1"; }
cmd_db() {
  local f
  f=$(ls "$HOME_DIR"/workspace/state/*.sqlite "$HOME_DIR"/state/*.sqlite 2>/dev/null | head -1)
  [ -n "$f" ] || die "no state DB yet"
  sqlite3 -header -column "$f" "$1"
}
cmd_dbfile() { ls "$HOME_DIR"/workspace/state/*.sqlite "$HOME_DIR"/state/*.sqlite 2>/dev/null | head -1; }
cmd_sid() { cmd_db "SELECT session_id FROM fb_messages ORDER BY rowid DESC LIMIT 1;" | tail -1 | tr -d ' '; }
cmd_stop() {
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  if [ -f "$WORK/provider.pid" ]; then kill "$(cat "$WORK/provider.pid")" 2>/dev/null || true; rm -f "$WORK/provider.pid"; fi
}
cmd_reset() { cmd_stop; rm -rf "$WORK"; }

case "${1:-}" in
  build) cmd_build ;;
  start) shift; cmd_start "$@" ;;
  screen) cmd_screen ;;
  key) shift; cmd_key "$@" ;;
  submit) shift; cmd_submit "$@" ;;
  send) shift; cmd_send "$@" ;;
  wait) shift; cmd_wait "$@" ;;
  db) shift; cmd_db "$1" ;;
  dbfile) cmd_dbfile ;;
  sid) cmd_sid ;;
  stop) cmd_stop ;;
  reset) cmd_reset ;;
  *) echo "usage: acc.sh build|start [--with-mcp] [mode]|screen|key|submit|send|wait|db|dbfile|sid|stop|reset"; exit 1 ;;
esac
