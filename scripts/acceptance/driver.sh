#!/usr/bin/env bash
# Drive the forebrain TUI from an agent: launch it against a provider whose
# timing you control. Copy of the repo's run-forebrain driver, adapted for the
# project-level MCP acceptance: the scratch project is a git repository the
# driver pre-trusts, and project MCP files can be dropped into it.
#
# Everything lives in an isolated FOREBRAIN_HOME and a throwaway project dir, so
# nothing here touches the user's real config, sessions or credentials.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="${FOREBRAIN_RUN_WORK:-${TMPDIR:-/tmp}/forebrain-accept}"
SESSION="${FOREBRAIN_RUN_SESSION:-forebrain-accept}"
PORT="${FOREBRAIN_RUN_PORT:-8741}"
BIN="$WORK/forebrain"
HOME_DIR="$WORK/home"
PROJ="$WORK/proj"

die() { echo "driver: $*" >&2; exit 1; }

wait_for() { # wait_for <seconds> <grep-pattern> -- polls the rendered screen
  local secs="$1" pat="$2" i
  for ((i = 0; i < secs * 5; i++)); do
    if tmux capture-pane -t "$SESSION" -p 2>/dev/null | grep -qF -- "$pat"; then return 0; fi
    sleep 0.2
  done
  echo "driver: timed out after ${secs}s waiting for: $pat" >&2
  tmux capture-pane -t "$SESSION" -p 2>/dev/null >&2 || true
  return 1
}

cmd_build() {
  mkdir -p "$WORK"
  ( cd "$REPO" && CGO_ENABLED=1 go build -tags fts5 -o "$BIN" ./cmd/forebrain )
  # Memory search reads its dictionary from beside the binary.
  "$REPO/scripts/install-dictionary.sh" "$(dirname "$BIN")"
  echo "built $BIN"
}

cmd_start() { # start [hang|stream|reply|drip] [answer-text] [drip-delay]
  local mode="${1:-hang}" text="${2:-FAKE_ANSWER}" delay="${3:-0.6}"
  cmd_stop >/dev/null 2>&1 || true
  [ -x "$BIN" ] || cmd_build
  mkdir -p "$HOME_DIR" "$PROJ"
  if [ ! -d "$PROJ/.git" ]; then
    git -C "$PROJ" init -q
    git -C "$PROJ" -c user.email=accept@example.com -c user.name=acceptance commit -q --allow-empty -m init || true
  fi
  echo "scratch project for driving the forebrain TUI" > "$PROJ/README.md"

  # api_key MUST be a ${ENV_NAME} reference: forebrain rejects a plaintext secret
  # at startup and exits 1 before the TUI ever paints.
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

  python3 "$REPO/scripts/acceptance/fake_provider.py" "$mode" "$PORT" "$text" "$delay" \
    > "$WORK/provider.log" 2>&1 &
  echo $! > "$WORK/provider.pid"
  local i
  for ((i = 0; i < 50; i++)); do
    grep -q LISTENING "$WORK/provider.log" 2>/dev/null && break
    sleep 0.1
  done
  grep -q LISTENING "$WORK/provider.log" || die "fake provider ($mode) never bound port $PORT"

  tmux new-session -d -s "$SESSION" -x 140 -y 45 -c "$PROJ" \
    "FAKE_LLM_KEY=sk-local-fake FOREBRAIN_HOME=$HOME_DIR $BIN 2>$WORK/tui.err; echo EXIT=\$?; exec bash"

  wait_for 20 "/ commands" || die "TUI never reached the composer; see $WORK/tui.err"
  echo "started: session=$SESSION provider=$mode port=$PORT home=$HOME_DIR proj=$PROJ"
}

cmd_send() { tmux send-keys -t "$SESSION" -l "$*"; }          # literal text, no Enter
cmd_key() { tmux send-keys -t "$SESSION" "$@"; }              # Enter, Escape, C-c, ...
cmd_submit() { tmux send-keys -t "$SESSION" -l "$*"; sleep 0.5; tmux send-keys -t "$SESSION" Enter; }
cmd_screen() { tmux capture-pane -t "$SESSION" -p; }
cmd_wait() { wait_for "${2:-20}" "$1"; }
cmd_provider_log() { cat "$WORK/provider.log"; }
cmd_home() { echo "$HOME_DIR"; }
cmd_proj() { echo "$PROJ"; }

cmd_db() { sqlite3 "$(echo "$HOME_DIR"/state/*.sqlite)" "$*"; }
cmd_messages() { cmd_db "SELECT id, role, source, substr(content,1,60) FROM fb_messages ORDER BY id;"; }
cmd_sid() { cmd_db "SELECT session_id FROM fb_messages ORDER BY id DESC LIMIT 1;"; }
cmd_title() { cmd_db "SELECT id, title FROM fb_sessions;"; }

cmd_stop() {
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  [ -f "$WORK/provider.pid" ] && kill "$(cat "$WORK/provider.pid")" 2>/dev/null || true
  rm -f "$WORK/provider.pid"
  echo "stopped"
}

cmd_reset() { cmd_stop >/dev/null 2>&1 || true; rm -rf "$HOME_DIR" "$PROJ"; echo "reset $WORK"; }

case "${1:-}" in
  build|start|send|key|submit|screen|wait|db|messages|title|sid|stop|reset|home|proj) c="$1"; shift; "cmd_$c" "$@" ;;
  provider-log) shift; cmd_provider_log ;;
  *) cat >&2 <<USAGE
usage: driver.sh <command>
  build                  build the binary into $WORK
  start [hang|stream|reply|drip] [answer-text] [drip-delay]
  submit <text>          type <text> and press Enter
  send <text>            type <text> without pressing Enter
  key <key...>           send keys (Enter, Escape, C-c, Down, ...)
  wait <text> [secs]     poll the screen until <text> appears
  screen                 print the rendered screen
  messages | title | sid read the state DB
  home | proj            print the isolated home / project dir
  db <sql>               run SQL against the state DB
  provider-log           what the fake provider received
  stop | reset           tear down / wipe the isolated home
USAGE
     exit 2 ;;
esac
