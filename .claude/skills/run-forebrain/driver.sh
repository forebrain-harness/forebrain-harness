#!/usr/bin/env bash
# Drive the forebrain TUI from an agent: launch it against a provider whose
# timing you control, send keys, read the rendered screen, and query the
# state DB that the interactive behaviour is supposed to have written.
#
# Everything lives in an isolated FOREBRAIN_HOME and a throwaway project dir, so
# nothing here touches the user's real config, sessions or history.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WORK="${FOREBRAIN_RUN_WORK:-${TMPDIR:-/tmp}/forebrain-run}"
SESSION="${FOREBRAIN_RUN_SESSION:-forebrain-run}"
PORT="${FOREBRAIN_RUN_PORT:-8731}"
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
  # CGO and the fts5 tag are both required: the state store is SQLite with
  # full-text search compiled in, and the binary will not build without them.
  ( cd "$REPO" && CGO_ENABLED=1 go build -tags fts5 -o "$BIN" ./cmd/forebrain )
  # Memory search reads its dictionary from beside the binary.
  "$REPO/scripts/install-dictionary.sh" "$(dirname "$BIN")"
  echo "built $BIN"
}

cmd_start() { # start [hang|stream|reply|usage|drip|toolcall|ask|shell|tool] [answer-text] [drip-delay]
  local mode="${1:-hang}" text="${2:-FAKE_ANSWER}" delay="${3:-0.6}"
  cmd_stop >/dev/null 2>&1 || true
  [ -x "$BIN" ] || cmd_build
  mkdir -p "$HOME_DIR" "$PROJ"
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
  # COMPACT_LIMIT lowers the auto-compaction threshold so a short scripted
  # conversation crosses it: the way to watch a pre-turn or mid-turn compaction.
  if [ -n "${COMPACT_LIMIT:-}" ]; then
    printf 'compact:\n  model_auto_compact_token_limit: %s\n' "$COMPACT_LIMIT" >> "$HOME_DIR/forebrain.yaml"
  fi

  python3 "$REPO/.claude/skills/run-forebrain/fake_provider.py" "$mode" "$PORT" "$text" "$delay" \
    > "$WORK/provider.log" 2>&1 &
  echo $! > "$WORK/provider.pid"
  local i
  for ((i = 0; i < 50; i++)); do
    grep -q LISTENING "$WORK/provider.log" 2>/dev/null && break
    sleep 0.1
  done
  grep -q LISTENING "$WORK/provider.log" || die "fake provider ($mode) never bound port $PORT"

  # RESUME_ID reopens an existing session instead of starting a new one, which
  # is the only way to check what a resume replays. It deliberately does NOT
  # reset the home: the session being resumed lives there.
  local launch="$BIN"
  if [ -n "${RESUME_ID:-}" ]; then launch="$BIN resume $RESUME_ID"; fi

  tmux new-session -d -s "$SESSION" -x 120 -y 40 -c "$PROJ" \
    "FAKE_LLM_KEY=sk-local-fake FOREBRAIN_HOME=$HOME_DIR $launch 2>$WORK/tui.err; echo EXIT=\$?; exec bash"

  # First launch in a new directory asks whether the directory is trusted.
  # Answered inside the wait for the composer rather than in a window of its
  # own: a slow launch can put the prompt on screen after a fixed window has
  # already expired, and then nothing answers it and the composer never comes.
  # The page opens on Quit, so the answer is Down then Enter, sent once — a
  # second Enter would reach the composer and submit an empty turn.
  local trusted=no i pane
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
  echo "started: session=$SESSION provider=$mode port=$PORT home=$HOME_DIR${RESUME_ID:+ resume=$RESUME_ID}"
}

cmd_send() { tmux send-keys -t "$SESSION" -l "$*"; }          # literal text, no Enter
cmd_key() { tmux send-keys -t "$SESSION" "$@"; }              # Enter, Escape, C-c, ...
cmd_submit() { tmux send-keys -t "$SESSION" -l "$*"; sleep 0.5; tmux send-keys -t "$SESSION" Enter; }
cmd_screen() { tmux capture-pane -t "$SESSION" -p; }
cmd_wait() { wait_for "${2:-20}" "$1"; }
cmd_provider_log() { cat "$WORK/provider.log"; }

# The state DB is the durable half of the contract: what the next model request
# would carry, and what /resume would show.
cmd_db() { sqlite3 "$(echo "$HOME_DIR"/state/*.sqlite)" "$*"; }
cmd_messages() { cmd_db "SELECT id, role, source, substr(content,1,60) FROM fb_messages ORDER BY id;"; }
# The id RESUME_ID wants: the session the last message was written to.
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
  build|start|send|key|submit|screen|wait|db|messages|title|sid|stop|reset) c="$1"; shift; "cmd_$c" "$@" ;;
  provider-log) shift; cmd_provider_log ;;
  *) cat >&2 <<USAGE
usage: driver.sh <command>
  build                  build the binary into $WORK
  start [hang|stream|reply|usage|drip|toolcall|ask|shell|tool] [answer-text] [drip-delay]
                         launch the TUI against a fake provider (default: hang)
                         drip = stream word by word; the only mode that proves
                         streaming actually renders
                         tool = call whatever the answer text names, as JSON
                         usage = reply, with a usage report carrying a cache split
                         {"name":...,"arguments":{...}} or an array of them
                         toolcall = enter_plan_mode then exit_plan_mode, so a
                         real approval overlay appears
                         ask = a two-question user_interaction call, so the real
                         question form with its Other field appears
                         RESUME_ID=<session> resumes that session instead of
                         starting a new one (does not reset the home)
  submit <text>          type <text> and press Enter
  send <text>            type <text> without pressing Enter
  key <key...>           send keys (Enter, Escape, C-c, Down, ...)
  wait <text> [secs]     poll the screen until <text> appears
  screen                 print the rendered screen
  messages | title       read the state DB
  sid                    print the session id of the newest message
  db <sql>               run SQL against the state DB
  provider-log           what the fake provider received
  stop | reset           tear down / wipe the isolated home
USAGE
     exit 2 ;;
esac
