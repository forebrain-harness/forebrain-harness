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
  "$REPO/scripts/fetch-libtokenizers.sh"
  # CGO and the fts5 tag are both required: the state store is SQLite with
  # full-text search compiled in, and the binary will not build without them.
  ( cd "$REPO" && CGO_ENABLED=1 go build -tags fts5 -o "$BIN" ./cmd/forebrain )
  # Memory search reads its dictionary, and the runtime its model catalog,
  # from beside the binary.
  "$REPO/scripts/install-dictionary.sh" "$(dirname "$BIN")"
  "$REPO/scripts/install-assets.sh" "$(dirname "$BIN")"
  echo "built $BIN"
}

# serve [mode] [answer-text] [drip-delay] -- everything start does except
# launching the TUI: the isolated home, its config and the fake provider. It is
# the half term_live.py needs, which runs the TUI in a Terminal.app window of its
# own instead of in tmux.
cmd_serve() {
  local mode="${1:-hang}" text="${2:-FAKE_ANSWER}" delay="${3:-0.6}"
  cmd_stop >/dev/null 2>&1 || true
  [ -x "$BIN" ] || cmd_build
  mkdir -p "$HOME_DIR" "$PROJ"
  echo "scratch project for driving the forebrain TUI" > "$PROJ/README.md"

  # api_key MUST be a ${ENV_NAME} reference: forebrain rejects a plaintext secret
  # at startup and exits 1 before the TUI ever paints.
  # ENABLE_SUBAGENT=1 turns the subagent tools on for the session: the plan
  # scripts that dispatch a subagent (roster, subagent cards, live continue)
  # need them, while every other script keeps the lean default. The switch the
  # runtime reads is agents.defaults.enable_subagent (absent means off).
  local subagent=false
  [ -n "${ENABLE_SUBAGENT:-}" ] && subagent=true
  # FAKE_PROVIDER_KIND swaps the configured provider section: the default is
  # the deepseek/OpenAI-compat surface; opencode-gemini points the session at
  # the OpenCode gateway's /google surface via gemini-4-argon-contributor,
  # which is the surface the fake provider's gemini mode answers.
  local provider=deepseek model=deepseek-chat
  if [ "${FAKE_PROVIDER_KIND:-}" = "opencode-gemini" ]; then
    provider=opencode
    model=gemini-4-argon-contributor
  fi
  cat > "$HOME_DIR/forebrain.yaml" <<YAML
approval_policy: on-request
agents:
  defaults:
    enable_subagent: $subagent
  definitions:
    main:
      primary: true
      llm_providers:
      - provider: $provider
        model: $model
        api_key: \${FAKE_LLM_KEY}
        base_url: http://127.0.0.1:$PORT
YAML
  # NETWORK_RETRY_MAX pins agents.defaults.network_retry_max for the run: the
  # transport-retry scripts use it to watch a custom cap's n/total wording and
  # its exhaustion. Inserted under agents.defaults, where the key lives.
  if [ -n "${NETWORK_RETRY_MAX:-}" ]; then
    python3 - "$HOME_DIR/forebrain.yaml" "$NETWORK_RETRY_MAX" <<'PY'
import sys
path, value = sys.argv[1], sys.argv[2]
lines = open(path).read().splitlines()
out = []
for line in lines:
    out.append(line)
    if line.strip().startswith("enable_subagent:"):
        out.append("    network_retry_max: %s" % value)
open(path, "w").write("\n".join(out) + "\n")
PY
  fi
  # COMPACT_LIMIT lowers the auto-compaction threshold so a short scripted
  # conversation crosses it: the way to watch a pre-turn or mid-turn compaction.
  if [ -n "${COMPACT_LIMIT:-}" ]; then
    printf 'compact:\n  model_auto_compact_token_limit: %s\n' "$COMPACT_LIMIT" >> "$HOME_DIR/forebrain.yaml"
  fi

  # FAKE_MARKER rides through to the fake provider untouched (the streamtools
  # mode's first tool call touches it): the provider inherits this process's
  # environment, and naming it here keeps that contract visible.
  FAKE_MARKER="${FAKE_MARKER:-}" python3 "$REPO/.claude/skills/run-forebrain/fake_provider.py" "$mode" "$PORT" "$text" "$delay" \
    > "$WORK/provider.log" 2>&1 &
  echo $! > "$WORK/provider.pid"
  local i
  for ((i = 0; i < 50; i++)); do
    grep -q LISTENING "$WORK/provider.log" 2>/dev/null && break
    sleep 0.1
  done
  grep -q LISTENING "$WORK/provider.log" || die "fake provider ($mode) never bound port $PORT"
  echo "serving: provider=$mode port=$PORT home=$HOME_DIR"
}

cmd_start() { # start [hang|stream|reply|usage|drip|toolcall|ask|shell|multishell|streamtools|tool|limit|subagent-limit|subagent-steer] [answer-text] [drip-delay]
  local mode="${1:-hang}"
  cmd_serve "$@"

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
      *"Trust this folder?"*)
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
# click <col> <row> — one mouse click at 1-based screen coordinates, as the
# SGR press+release pair the TUI's mouse reporting reads. tmux forwards both
# sequences verbatim, so the TUI sees a click exactly where a terminal would
# send one.
cmd_click() {
  tmux send-keys -t "$SESSION" -l "$(printf '\033[<0;%s;%sM' "$1" "$2")"
  tmux send-keys -t "$SESSION" -l "$(printf '\033[<0;%s;%sm' "$1" "$2")"
}
cmd_screen() { tmux capture-pane -t "$SESSION" -p; }
cmd_wait() { wait_for "${2:-20}" "$1"; }

# The software caret is painted as reverse video over the glyph it sits on, so
# the pane's own escape stream is a direct read of which cell it is really on.
# capture-pane without -e strips the SGR and shows nothing at all, and the caret
# blinks, so `caret` samples rather than trusting one frame.
pane_probe() { # pane_probe <rows|col|caret> [args...]
  PANE_SESSION="$SESSION" python3 - "$@" <<'PY'
import os, re, subprocess, sys, time, unicodedata

PROMPT = "\u203a"
SGR = re.compile(r"\x1b\[([0-9;]*)m")
ESC = re.compile(r"\x1b\[[0-9;?]*[A-Za-z]")


def capture():
    r = subprocess.run(
        ["tmux", "capture-pane", "-t", os.environ["PANE_SESSION"], "-e", "-p"],
        capture_output=True, text=True)
    if r.returncode != 0:
        sys.exit("tmux capture-pane failed: " + r.stderr.strip())
    return r.stdout.split("\n")


def cells(raw):
    """(printable char, reverse video?) for one captured row."""
    out, reverse, i = [], False, 0
    while i < len(raw):
        if raw[i] == "\x1b":
            m = SGR.match(raw, i)
            if m:
                for p in m.group(1).split(";"):
                    if p == "7":
                        reverse = True
                    elif p in ("", "0", "27"):
                        reverse = False
                i = m.end()
                continue
            m = ESC.match(raw, i)
            i = m.end() if m else i + 1
            continue
        out.append((raw[i], reverse))
        i += 1
    return out


def plain(raw):
    return "".join(ch for ch, _ in cells(raw))


def width(ch):
    if unicodedata.combining(ch):
        return 0
    return 2 if unicodedata.east_asian_width(ch) in ("W", "F") else 1


def composer_row(lines):
    for idx in range(len(lines) - 1, -1, -1):
        if PROMPT in plain(lines[idx]):
            return idx + 1
    return 0


cmd, rest = sys.argv[1], sys.argv[2:]

if cmd == "rows":
    lines = capture()
    found = [(i + 1, plain(l).rstrip()) for i, l in enumerate(lines) if PROMPT in plain(l)]
    if not found:
        sys.exit("no composer row: is the TUI running (start)?")
    for row, text in found:
        print("%d\t%s" % (row, text))
elif cmd == "col":
    if not rest:
        sys.exit("usage: col <substring> [row]")
    lines = capture()
    row = int(rest[1]) if len(rest) > 1 else composer_row(lines)
    if not row:
        sys.exit("no composer row: is the TUI running (start)?")
    text = plain(lines[row - 1]).rstrip()
    at = text.find(rest[0])
    if at < 0:
        sys.exit("%r is not on row %d: %r" % (rest[0], row, text))
    print("row=%d col=%d  line=%r" % (row, sum(width(c) for c in text[:at]) + 1, text))
elif cmd == "caret":
    want = int(rest[0]) if rest else 0
    seen, text, row = [], "", 0
    for n in range(6):  # 6 x 250ms crosses the 500ms blink, so on-phase is seen
        if n:
            time.sleep(0.25)
        lines = capture()
        row = want or composer_row(lines)
        if not row:
            sys.exit("no composer row: is the TUI running (start)?")
        text = plain(lines[row - 1]).rstrip()
        glyph = "".join(ch for ch, rev in cells(lines[row - 1]) if rev)
        if glyph:
            seen.append(glyph)
    if not seen:
        print("caret=(none in 6 samples over 1.5s)  row=%d" % row)
    else:
        counts = ", ".join("%r x%d" % (g, seen.count(g)) for g in dict.fromkeys(seen))
        print("caret=%s  row=%d  (%d/6 samples)" % (counts, row, len(seen)))
    print("line=%r" % text)
else:
    sys.exit("unknown pane probe: " + cmd)
PY
}
cmd_rows() { pane_probe rows "$@"; }
cmd_col() { pane_probe col "$@"; }
cmd_caret() { pane_probe caret "$@"; }

cmd_provider_log() { cat "$WORK/provider.log"; }

# The state DB is the durable half of the contract: what the next model request
# would carry, and what /resume would show.
cmd_db() { sqlite3 "$(echo "$HOME_DIR"/state/*.sqlite)" "$*"; }
cmd_messages() { cmd_db "SELECT id, role, visibility, substr(content,1,60) FROM fb_messages ORDER BY id;"; }
# The id RESUME_ID wants: the session the last message was written to.
cmd_sid() { cmd_db "SELECT session_id FROM fb_messages ORDER BY id DESC LIMIT 1;"; }
cmd_title() { cmd_db "SELECT id, title FROM fb_sessions;"; }

cmd_wheel() {  # wheel <col> <row> <up|down>
  local col="$1" row="$2" dir="${3:-down}"
  local cb=65  # 65=down
  [ "$dir" = "up" ] && cb=64
  tmux send-keys -t "$SESSION" -l "$(printf '\033[<%s;%s;%sM' "$cb" "$col" "$row")"
}

cmd_stop() {
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  [ -f "$WORK/provider.pid" ] && kill "$(cat "$WORK/provider.pid")" 2>/dev/null || true
  rm -f "$WORK/provider.pid"
  echo "stopped"
}

cmd_reset() { cmd_stop >/dev/null 2>&1 || true; rm -rf "$HOME_DIR" "$PROJ"; echo "reset $WORK"; }

case "${1:-}" in
  build|serve|start|send|key|submit|click|wheel|screen|rows|col|caret|wait|db|messages|title|sid|stop|reset) c="$1"; shift; "cmd_$c" "$@" ;;
  provider-log) shift; cmd_provider_log ;;
  *) cat >&2 <<USAGE
usage: driver.sh <command>
  build                  build the binary into $WORK
  start [hang|stream|reply|usage|drip|toolcall|ask|shell|multishell|streamtools|tool|limit|subagent-limit|subagent-steer] [answer-text] [drip-delay]
                         launch the TUI against a fake provider (default: hang)
                         drip = stream word by word; the only mode that proves
                         streaming actually renders
                         multishell = three shell calls in one reply, so a
                         steer queued while the first one runs takes the turn
                         at the next call boundary
                         streamtools = two shell calls streamed one at a time;
                         the provider waits for the first call's marker file
                         (FAKE_MARKER, touched by the call) before finishing
                         the second call's arguments — a run that executes
                         calls only after the response ends shows
                         "echo MARKER MISSING"
                         tool = call whatever the answer text names, as JSON
                         usage = reply, with a usage report carrying a cache split
                         {"name":...,"arguments":{...}} or an array of them
                         toolcall = enter_plan_mode then exit_plan_mode, so a
                         real approval overlay appears
                         ask = a two-question user_interaction call, so the real
                         question form with its Other field appears
                        subagent-limit = the main agent dispatches a
                        general-purpose probe (needs ENABLE_SUBAGENT=1); the
                        subagent's own request is refused with a usage-limit 429
                        for FAKE_LIMIT_SECONDS (default 20) and then answers.
                        Dispatched with subagent_run (the default) the main
                        agent gets the failure account (task_id, ran_for, error,
                        resets_at) and the subagent arms no continuation of its
                        own; FAKE_SUBAGENT_DISPATCH=send uses subagent_send
                        instead, so the subagent's automatic continuation is
                        what runs the answer text
                        subagent-steer = the main agent dispatches a
                        general-purpose probe (needs ENABLE_SUBAGENT=1) that
                        runs a shell call (the answer text, default sleep 8);
                        its next request takes a steer queued meanwhile, sends
                        one delta and hangs, so the subagent keeps running
                         RESUME_ID=<session> resumes that session instead of
                         starting a new one (does not reset the home)
                         ENABLE_SUBAGENT=1 enables the subagent tools
                         (subagent_run, subagent_fanout, ...) for the session
  serve [mode] [answer-text] [drip-delay]
                         start's home, config and fake provider without the
                         TUI, for term_live.py to run it in Terminal.app
  submit <text>          type <text> and press Enter
  send <text>            type <text> without pressing Enter
  key <key...>           send keys (Enter, Escape, C-c, Down, ...)
  click <col> <row>      click at 1-based screen coordinates (SGR mouse)
  wheel <col> <row> <up|down>
                         send one wheel notch at 1-based coordinates (default: down)
  wait <text> [secs]     poll the screen until <text> appears
  screen                 print the rendered screen
  rows                   composer rows: 1-based pane row + its text
  col <substring> [row] 1-based display column where <substring> starts
                         (CJK takes two cells, so never count by hand)
  caret [row]            the glyph painted in reverse video, i.e. where the
                         text caret really is; 6 samples over 1.5s because the
                         caret blinks every 500ms
  messages | title       read the state DB
  sid                    print the session id of the newest message
  db <sql>               run SQL against the state DB
  provider-log           what the fake provider received
  stop | reset           tear down / wipe the isolated home
USAGE
     exit 2 ;;
esac
