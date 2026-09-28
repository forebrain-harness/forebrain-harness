#!/usr/bin/env bash
# Drive the forebrain TUI against the REAL DeepSeek provider for P8 acceptance.
# The API key stays in the environment (forebrain.yaml references ${DEEPSEEK_API_KEY}).
#
#   p8.sh build
#   p8.sh start
#   p8.sh submit '<text>'
#   p8.sh screen | messages | db <sql> | sid | title
#   p8.sh wait '<pattern>' [secs]
#   p8.sh key <key...> | send <text>
#   p8.sh stop | reset
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WORK="${P8_WORK:-${TMPDIR:-/tmp}/forebrain-p8}"
SESSION="${P8_SESSION:-forebrain-p8}"
BIN="$WORK/forebrain"
HOME_DIR="$WORK/home"
PROJ="$WORK/proj"

die() { echo "p8: $*" >&2; exit 1; }

wait_for() {
  local secs="$1" pat="$2" i
  for ((i = 0; i < secs * 5; i++)); do
    if tmux capture-pane -t "$SESSION" -p 2>/dev/null | grep -qF -- "$pat"; then return 0; fi
    sleep 0.2
  done
  echo "p8: timed out after ${secs}s waiting for: $pat" >&2
  tmux capture-pane -t "$SESSION" -p 2>/dev/null >&2 || true
  return 1
}

cmd_build() {
  mkdir -p "$WORK"
  ( cd "$REPO" && CGO_ENABLED=1 go build -tags fts5 -o "$BIN" ./cmd/forebrain )
  echo "built $BIN"
}

cmd_start() {
  cmd_stop >/dev/null 2>&1 || true
  [ -x "$BIN" ] || cmd_build
  : "${DEEPSEEK_API_KEY:?set DEEPSEEK_API_KEY first}"
  mkdir -p "$HOME_DIR" "$PROJ"
  # The project must be a git repo: project skills load only from a
  # version-controlled, trusted project root.
  if [ ! -d "$PROJ/.git" ]; then
    git -C "$PROJ" init -q
    echo "# acceptance project" > "$PROJ/README.md"
    git -C "$PROJ" add -A
    git -C "$PROJ" -c user.name=p8 -c user.email=p8@example.com commit -qm init
  fi

  cat > "$HOME_DIR/forebrain.yaml" <<YAML
approval_policy: on-request
sandbox_mode: workspace-write
agents:
  definitions:
    main:
      primary: true
      enable_subagent: true
      llm_providers:
      - provider: deepseek
        model: deepseek-flash
        api_key: \${DEEPSEEK_API_KEY}
        base_url: https://api.deepseek.com
YAML

  local launch="$BIN"
  if [ -n "${RESUME_ID:-}" ]; then launch="$BIN resume $RESUME_ID"; fi
  tmux new-session -d -s "$SESSION" -x 120 -y 45 -c "$PROJ" \
    "DEEPSEEK_API_KEY=$DEEPSEEK_API_KEY FOREBRAIN_HOME=$HOME_DIR $launch 2>$WORK/tui.err; echo EXIT=\$?; exec bash"

  if wait_for 20 "Trust this directory?" 2>/dev/null; then
    tmux send-keys -t "$SESSION" Down Enter   # the page opens on Quit
    sleep 1
    tmux send-keys -t "$SESSION" Enter   # second confirmation pane, if any
  fi
  wait_for 30 "/ commands" || die "TUI never reached the composer; see $WORK/tui.err"
  echo "started: session=$SESSION home=$HOME_DIR proj=$PROJ"
}

cmd_send() { tmux send-keys -t "$SESSION" -l "$*"; }
cmd_key() { tmux send-keys -t "$SESSION" "$@"; }
cmd_submit() { tmux send-keys -t "$SESSION" -l "$*"; sleep 0.5; tmux send-keys -t "$SESSION" Enter; }
cmd_screen() { tmux capture-pane -t "$SESSION" -p; }
cmd_wait() { wait_for "${2:-20}" "$1"; }
cmd_db() { sqlite3 "$(echo "$HOME_DIR"/state/*.sqlite)" "$*"; }
cmd_messages() { cmd_db "SELECT id, role, substr(content,1,70) FROM fb_messages ORDER BY id;"; }
cmd_sid() { cmd_db "SELECT session_id FROM fb_messages ORDER BY id DESC LIMIT 1;"; }
cmd_title() { cmd_db "SELECT id, title FROM fb_sessions;"; }
cmd_skills() { find "$PROJ/.forebrain/skills" "$HOME_DIR/skills/.system" -maxdepth 2 -name SKILL.md 2>/dev/null | sort; }
cmd_projectskills() { find "$PROJ/.forebrain/skills" -maxdepth 3 -name 'SKILL.md' 2>/dev/null | sort; }
cmd_skillmd() { cat "$PROJ/.forebrain/skills/$1/SKILL.md"; }

cmd_stop() {
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  echo "stopped"
}

cmd_reset() { cmd_stop >/dev/null 2>&1 || true; rm -rf "$HOME_DIR" "$PROJ"; echo "reset $WORK"; }

case "${1:-}" in
  build|start|send|key|submit|screen|wait|db|messages|sid|title|stop|reset|skills|projectskills|skillmd)
    c="$1"; shift; "cmd_$c" "$@" ;;
  *) echo "usage: p8.sh build|start|stop|reset|send|key|submit|screen|wait|db|messages|sid|title|skills|projectskills|skillmd <name>" >&2; exit 2 ;;
esac
