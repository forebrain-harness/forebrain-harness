#!/usr/bin/env bash
# Drive the forebrain TUI against a REAL model, through the controllable
# `flaky_proxy.py`, so "the network died and the user continued the subagent
# from its own view" can be exercised for real.
#
# Everything lives in an isolated FOREBRAIN_HOME and a throwaway project dir;
# the user's real ~/.forebrain is never touched. The provider key is read from
# FOREBRAIN_E2E_ZHIPU_KEY (already exported, or ~/.forebrain/e2e-zhipu.env) and
# is never printed: the config references ${FOREBRAIN_E2E_ZHIPU_KEY}, and the
# launched process gets it from a 0600 file this script writes and deletes.
#
# Subcommands mirror .claude/skills/run-forebrain/driver.sh. `start` brings up
# the home, the config, the proxy and the tmux session; `control` flips the
# proxy between pass/drop-subagent/drop-all; the rest drive and read the TUI.
#
#   start            build, launch TUI behind the proxy, wait for the composer
#   control <value>  pass | drop-subagent | drop-all   (also plain: `set`)
#   submit <text>    type <text> and press Enter
#   send <text>      type <text> without pressing Enter
#   key <key...>     send keys (Enter, Escape, C-c, Down, ...)
#   click <col> <row> click at 1-based screen coordinates (SGR mouse)
#   wait <text> [s]  poll the rendered screen until <text> appears
#   screen           print the rendered screen
#   shot <name>      save the rendered screen to $WORK/evidence/<name>.txt
#   messages|title|sid|db <sql>   read the state DB
#   proxy-log        the proxy's decision log (never bodies or headers)
#   stop             tear down tmux + proxy, keep evidence, delete the key file
#   reset            stop, then wipe the isolated home and project
#
# Overrides: LIVE_WORK, LIVE_SESSION, LIVE_PROXY_PORT, LIVE_UPSTREAM,
# LIVE_PROVIDER, LIVE_MODEL, COMPACT_LIMIT. For a no-model plumbing self-test,
# point LIVE_UPSTREAM at a local fake provider and export a dummy
# FOREBRAIN_E2E_ZHIPU_KEY.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="${LIVE_WORK:-${TMPDIR:-/tmp}/forebrain-live}"
SESSION="${LIVE_SESSION:-forebrain-live}"
PROXY_PORT="${LIVE_PROXY_PORT:-8743}"
UPSTREAM="${LIVE_UPSTREAM:-https://open.bigmodel.cn/api/coding/paas/v4}"
PROVIDER="${LIVE_PROVIDER:-zhipuai}"
MODEL="${LIVE_MODEL:-glm-5.3-flash}"
BIN="$WORK/forebrain"
HOME_DIR="$WORK/home"
PROJ="$WORK/proj"
CONTROL="$WORK/control"
EVIDENCE="$WORK/evidence"
CREDS="$WORK/creds.env"
ZHIPU_ENV="${LIVE_ZHIPU_ENV:-$HOME/.forebrain/e2e-zhipu.env}"

die() { echo "live: $*" >&2; exit 1; }

wait_for() { # wait_for <seconds> <grep-pattern>
  local secs="$1" pat="$2" i
  for ((i = 0; i < secs * 5; i++)); do
    if tmux capture-pane -t "$SESSION" -p 2>/dev/null | grep -qF -- "$pat"; then return 0; fi
    sleep 0.2
  done
  echo "live: timed out after ${secs}s waiting for: $pat" >&2
  tmux capture-pane -t "$SESSION" -p 2>/dev/null >&2 || true
  return 1
}

cmd_build() {
  mkdir -p "$WORK"
  ( cd "$REPO" && CGO_ENABLED=1 go build -tags fts5 -o "$BIN" ./cmd/forebrain )
  "$REPO/scripts/install-dictionary.sh" "$(dirname "$BIN")"
  echo "built $BIN"
}

cmd_start() {
  cmd_stop >/dev/null 2>&1 || true
  [ -x "$BIN" ] || cmd_build
  mkdir -p "$HOME_DIR" "$PROJ" "$EVIDENCE"
  echo "scratch project for driving the forebrain TUI (live)" > "$PROJ/README.md"
  printf 'pass\n' > "$CONTROL"

  # The key: prefer one already in the environment, else the owner's env file.
  # It is never echoed; it goes only into a 0600 file the launched TUI sources.
  local key="${FOREBRAIN_E2E_ZHIPU_KEY:-}"
  if [ -z "$key" ] && [ -f "$ZHIPU_ENV" ]; then
    key="$(set -a; . "$ZHIPU_ENV"; set +a; printf '%s' "${FOREBRAIN_E2E_ZHIPU_KEY:-}")"
  fi
  [ -n "$key" ] || die "credentials missing: set FOREBRAIN_E2E_ZHIPU_KEY or install ${ZHIPU_ENV}"
  ( umask 077; printf 'FOREBRAIN_E2E_ZHIPU_KEY=%s\n' "$key" > "$CREDS" )
  unset key

  cat > "$HOME_DIR/forebrain.yaml" <<YAML
approval_policy: on-request
agents:
  defaults:
    enable_subagent: true
  definitions:
    main:
      primary: true
      llm_providers:
      - provider: $PROVIDER
        model: $MODEL
        api_key: \${FOREBRAIN_E2E_ZHIPU_KEY}
        base_url: http://127.0.0.1:$PROXY_PORT
YAML
  if [ -n "${COMPACT_LIMIT:-}" ]; then
    printf 'compact:\n  model_auto_compact_token_limit: %s\n' "$COMPACT_LIMIT" >> "$HOME_DIR/forebrain.yaml"
  fi

  python3 "$REPO/scripts/acceptance/flaky_proxy.py" "$PROXY_PORT" "$UPSTREAM" "$CONTROL" \
    > "$WORK/proxy.log" 2>&1 &
  echo $! > "$WORK/proxy.pid"
  local i
  for ((i = 0; i < 50; i++)); do
    grep -q LISTENING "$WORK/proxy.log" 2>/dev/null && break
    sleep 0.1
  done
  grep -q LISTENING "$WORK/proxy.log" || die "flaky proxy never bound port $PROXY_PORT"

  # The key reaches the TUI through a sourced file, so it is never on a command
  # line (where `ps` could show it) and never in the config.
  tmux new-session -d -s "$SESSION" -x 120 -y 40 -c "$PROJ" \
    "set -a; . '$CREDS'; set +a; FOREBRAIN_HOME='$HOME_DIR' '$BIN' 2>'$WORK/tui.err'; echo EXIT=\$?; exec bash"

  local trusted=no pane
  for ((i = 0; i < 200; i++)); do
    pane="$(tmux capture-pane -t "$SESSION" -p 2>/dev/null || true)"
    case "$pane" in
      *"Trust this directory?"*)
        if [ "$trusted" = no ]; then
          tmux send-keys -t "$SESSION" Down Enter
          trusted=yes
        fi
        ;;
      *"/ commands"*) break ;;
    esac
    sleep 0.2
  done
  wait_for 25 "/ commands" || die "TUI never reached the composer; see $WORK/tui.err"
  echo "started: session=$SESSION proxy=$PROXY_PORT upstream=$UPSTREAM provider=$PROVIDER model=$MODEL home=$HOME_DIR"
}

cmd_control() { printf '%s\n' "${1:-pass}" > "$CONTROL"; echo "control=${1:-pass}"; }
cmd_send() { tmux send-keys -t "$SESSION" -l "$*"; }
cmd_key() { tmux send-keys -t "$SESSION" "$@"; }
cmd_submit() { tmux send-keys -t "$SESSION" -l "$*"; sleep 0.5; tmux send-keys -t "$SESSION" Enter; }
cmd_click() {
  tmux send-keys -t "$SESSION" -l "$(printf '\033[<0;%s;%sM' "$1" "$2")"
  tmux send-keys -t "$SESSION" -l "$(printf '\033[<0;%s;%sm' "$1" "$2")"
}
cmd_screen() { tmux capture-pane -t "$SESSION" -p; }
cmd_shot() { mkdir -p "$EVIDENCE"; tmux capture-pane -t "$SESSION" -p > "$EVIDENCE/${1:-shot}.txt"; echo "saved $EVIDENCE/${1:-shot}.txt"; }
cmd_wait() { wait_for "${2:-20}" "$1"; }
cmd_proxy_log() { cat "$WORK/proxy.log"; }

cmd_db() { sqlite3 "$(echo "$HOME_DIR"/state/*.sqlite)" "$*"; }
cmd_messages() { cmd_db "SELECT id, role, session_id, visibility, substr(content,1,60) FROM fb_messages ORDER BY id;"; }
cmd_sid() { cmd_db "SELECT session_id FROM fb_messages ORDER BY id DESC LIMIT 1;"; }
cmd_title() { cmd_db "SELECT id, title FROM fb_sessions;"; }

cmd_stop() {
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  [ -f "$WORK/proxy.pid" ] && kill "$(cat "$WORK/proxy.pid")" 2>/dev/null || true
  rm -f "$WORK/proxy.pid" "$CREDS"
  mkdir -p "$EVIDENCE"
  if [ -d "$HOME_DIR/state" ]; then
    for db in "$HOME_DIR"/state/*.sqlite; do
      [ -f "$db" ] && cp -f "$db" "$EVIDENCE/$(basename "$db")" || true
    done
  fi
  echo "stopped"
}

cmd_reset() { cmd_stop >/dev/null 2>&1 || true; rm -rf "$HOME_DIR" "$PROJ" "$CONTROL" "$WORK/proxy.log" "$WORK/tui.err"; echo "reset $WORK"; }

case "${1:-}" in
  build|start|control|submit|send|key|click|screen|shot|wait|db|messages|title|sid|stop|reset) c="$1"; shift; "cmd_$c" "$@" ;;
  proxy-log) shift; cmd_proxy_log ;;
  set) shift; cmd_control "$@" ;;
  *) cat >&2 <<USAGE
usage: live_subagent.sh <command>
  build | start | stop | reset
  control <pass|drop-subagent|drop-all>   flip the proxy's policy
  submit <text> | send <text> | key <key...> | click <col> <row>
  wait <text> [secs] | screen | shot <name>
  messages | title | sid | db <sql> | proxy-log
env overrides: LIVE_WORK LIVE_SESSION LIVE_PROXY_PORT LIVE_UPSTREAM
               LIVE_PROVIDER LIVE_MODEL COMPACT_LIMIT LIVE_ZHIPU_ENV
USAGE
     exit 2 ;;
esac
