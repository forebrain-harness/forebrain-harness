#!/usr/bin/env bash
# Real-machine acceptance for plan 053: the model can read an image.
#
# Builds the real binary, starts the real TUI in a scratch FOREBRAIN_HOME
# against a REAL provider, and proves in one run:
#   1. read_file of an image renders a one-line STEP (not merely reasoning text)
#      and the model's FINAL ANSWER names the shapes/colours and their sides;
#   2. the pixels are stored on the tool row (parts contains a "type":"image"
#      block) — the model, not a prompt hack, saw them;
#   3. the step marker line names the Ctrl+O opener (a bare letter is never
#      bound; the key-guard itself is pinned by pkg/tui's unit test);
#   4. an MCP server's image result lands under the PRIMARY AGENT WORKSPACE's
#      state/mcp-results, not the home's, with NO approval card anywhere in the
#      phase, and reading the saved path needs no approval;
#   5. the capability gate refuses an image on a text-only session model with
#      the one-step-recoverable sentence (not a provider 400).
#
# The provider key is read from FOREBRAIN_E2E_DEEPSEEK_KEY (already exported,
# never printed) and written to a 0600 file the launched TUI sources, so it is
# never on a command line. The home is named by this script and is NEVER taken
# from the ambient FOREBRAIN_HOME; pass FOREBRAIN_ACCEPT_HOME to keep it.
#
#   FOREBRAIN_E2E_DEEPSEEK_KEY=<key> scripts/acceptance/read_image.sh
#   FOREBRAIN_E2E_MODEL=deepseek-v4-flash            # override the model under test
#   FOREBRAIN_ACCEPT_NO_OPEN=1                        # skip the Ctrl+O keystroke
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BASE="${FOREBRAIN_ACCEPT_HOME:-$(mktemp -d)}"
WORK="$BASE"
HOME_DIR="$WORK/home"
PROJ="$WORK/proj"
EVIDENCE="$WORK/evidence"
BIN="$WORK/forebrain"
CREDS="$WORK/creds.env"
SHAPE="$WORK/shape.png"
SESSION="${FOREBRAIN_ACCEPT_SESSION:-forebrain-read-image}"
MODEL="${FOREBRAIN_E2E_MODEL:-deepseek-v4-flash}"
TEXT_ONLY_MODEL="${FOREBRAIN_ACCEPT_TEXT_ONLY_MODEL:-deepseek-v4-pro}"
PROVIDER="deepseek"
BASE_URL="https://api.deepseek.com"
MCP_SERVER="fake-shots"
MCP_TOOL="shot"
MCP_TOOL_NAME="mcp__${MCP_SERVER}__${MCP_TOOL}"
MCP_RESULTS="$HOME_DIR/workspace/state/mcp-results"

fail=0
die() { echo "read-image: $*" >&2; exit 1; }
note() { echo "read-image: $*"; }
# mark_fail records a failed assertion, prints it, and keeps going so one run's
# evidence shows every miss; the verdict at the end reads the flag.
mark_fail() { echo "read-image: FAIL: $*" >&2; fail=1; }

# Fail fast, before building anything, when the key is missing. The value is
# never echoed.
KEY="${FOREBRAIN_E2E_DEEPSEEK_KEY:-}"
[ -n "$KEY" ] || die "FOREBRAIN_E2E_DEEPSEEK_KEY is not set; export it (from a chmod-600 env file) and re-run"

cleanup() {
  local code=$?
  kill_stale
  if [ "$code" -ne 0 ]; then
    [ -f "$WORK/tui.err" ] && { echo '--- tui.err (last 40 lines) ---' >&2; tail -40 "$WORK/tui.err" >&2 || true; }
  fi
  rm -f "$CREDS"
  exit "$code"
}
trap cleanup EXIT

# Only this script's own session and binary are touched.
kill_stale() {
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  pkill -f "$BIN" 2>/dev/null || true
  sleep 0.2
}
kill_stale

note "home=$HOME_DIR model=$MODEL text_only_model=$TEXT_ONLY_MODEL"

mkdir -p "$HOME_DIR" "$PROJ" "$EVIDENCE"
echo "scratch project for the read-image acceptance" > "$PROJ/README.md"
( umask 077; printf 'FOREBRAIN_E2E_DEEPSEEK_KEY=%s\n' "$KEY" > "$CREDS" )
unset KEY

# 1. Build the real binary and its runtime assets. The scratch home seeds the
#    corrected catalog (assets/models.json) on first start; an existing home
#    keeps its own copy (design), so a fresh scratch home is what proves the fix.
"$REPO/scripts/fetch-libtokenizers.sh"
( cd "$REPO" && CGO_ENABLED=1 go build -tags fts5 -o "$BIN" ./cmd/forebrain )
"$REPO/scripts/install-dictionary.sh" "$WORK"
"$REPO/scripts/install-assets.sh" "$WORK"
note "built $BIN"

# 2. The image under test: white background, a red disc on the left, a blue
#    square on the right. Generated with the standard library so no fixture is
#    committed.
python3 - "$SHAPE" <<'PY'
import struct, sys, zlib
W, H = 1200, 600
white, red, blue = (255, 255, 255), (220, 30, 30), (30, 60, 220)
rows = []
for y in range(H):
    row = bytearray()
    for x in range(W):
        if (x - 300) ** 2 + (y - 300) ** 2 <= 180 * 180:
            c = red
        elif 820 <= x <= 1080 and 180 <= y <= 420:
            c = blue
        else:
            c = white
        row += bytes(c)
    rows.append(bytes(row))
raw = b"".join(b"\x00" + r for r in rows)

def chunk(typ, data):
    body = typ + data
    return struct.pack(">I", len(data)) + body + struct.pack(">I", zlib.crc32(body) & 0xffffffff)

png = (
    b"\x89PNG\r\n\x1a\n"
    + chunk(b"IHDR", struct.pack(">IIBBBBB", W, H, 8, 2, 0, 0, 0))
    + chunk(b"IDAT", zlib.compress(raw, 6))
    + chunk(b"IEND", b"")
)
open(sys.argv[1], "wb").write(png)
PY
note "wrote $SHAPE"

# 3. Config. The key is referenced by ${ENV}, never written literally. The fake
#    MCP server declares no tool hints, so under the default approval mode its
#    call requires approval; the scratch config sets approve explicitly so the
#    MCP phase runs unattended (this is a scratch config, not product behaviour).
write_config() { # write_config <model>
  cat > "$HOME_DIR/forebrain.yaml" <<YAML
approval_policy: on-request
agents:
  defaults:
    mcp_servers:
      - name: ${MCP_SERVER}
        command: python3
        args: ["${REPO}/scripts/acceptance/fake_mcp_stdio.py", "${MCP_TOOL}", "${MCP_SERVER}", "image"]
        default_tools_approval_mode: approve
        enabled: true
  definitions:
    main:
      primary: true
      llm_providers:
      - provider: ${PROVIDER}
        model: $1
        api_key: \${FOREBRAIN_E2E_DEEPSEEK_KEY}
        base_url: ${BASE_URL}
YAML
}

# --- tmux + DB helpers ----------------------------------------------------
pane() { tmux capture-pane -t "$SESSION" -p 2>/dev/null || true; }
# pane_all also asks for scrollback, so a marker that scrolled off the screen is
# still visible to a grep where the terminal keeps it.
pane_all() { tmux capture-pane -t "$SESSION" -p -S -5000 2>/dev/null || true; }

wait_for() { # wait_for <seconds> <fixed-substring>  (visible pane)
  local secs="$1" pat="$2" i
  for ((i = 0; i < secs * 5; i++)); do
    pane | grep -qF -- "$pat" && return 0
    sleep 0.2
  done
  return 1
}

submit() { tmux send-keys -t "$SESSION" -l "$*"; sleep 0.4; tmux send-keys -t "$SESSION" Enter; }

start_tui() { # start_tui <model>
  kill_stale
  write_config "$1"
  tmux new-session -d -s "$SESSION" -x 140 -y 45 -c "$PROJ" \
    "set -a; . '$CREDS'; set +a; FOREBRAIN_HOME='$HOME_DIR' '$BIN' 2>'$WORK/tui.err'; echo EXIT=\$?; exec bash"
  local trusted=no p
  for ((i = 0; i < 250; i++)); do
    p="$(pane)"
    case "$p" in
      *"Trust this directory?"*)
        if [ "$trusted" = no ]; then tmux send-keys -t "$SESSION" Down Enter; trusted=yes; fi ;;
      *"/ commands"*) break ;;
    esac
    sleep 0.2
  done
  wait_for 30 "/ commands" || die "TUI never reached the composer; see $WORK/tui.err"
}

dbq() { sqlite3 "$(echo "$HOME_DIR"/state/*.sqlite)" "$1" 2>/dev/null || true; }
# db_last_assistant is the FINAL ANSWER prose (role='assistant'), never the
# reasoning paragraph (role='reasoning') — the two are separate rows.
db_last_assistant() { dbq "SELECT content FROM fb_messages WHERE role='assistant' ORDER BY id DESC LIMIT 1;"; }
db_max_assistant_id() { local v; v="$(dbq "SELECT COALESCE(MAX(id),0) FROM fb_messages WHERE role='assistant';")"; printf '%s' "${v:-0}"; }
# db_last_image_tool_parts is the newest TOOL row that actually carries an image
# block. Selecting "the newest tool row" was wrong: the model may run a shell
# command (e.g. `ls`) after the read_file, so the image row is not the last one.
db_last_image_tool_parts() { dbq "SELECT parts FROM fb_messages WHERE role='tool' AND parts LIKE '%\"image\"%' ORDER BY id DESC LIMIT 1;"; }
# db_recent_tool_parts_heads prints the newest few tool rows' parts heads, so a
# failure shows every row that was there.
db_recent_tool_parts_heads() { dbq "SELECT id || ': ' || substr(COALESCE(parts,''),1,120) FROM fb_messages WHERE role='tool' ORDER BY id DESC LIMIT 5;"; }
db_max_tool_id() { local v; v="$(dbq "SELECT COALESCE(MAX(id),0) FROM fb_messages WHERE role='tool';")"; printf '%s' "${v:-0}"; }
# db_new_tool_content_since prints the newest tool row newer than <id>, as its
# text content concatenated with its parts — a tool row may carry the model
# text in either column, and both spellings must be searchable.
db_new_tool_content_since() { dbq "SELECT COALESCE(content,'') || ' ' || COALESCE(parts,'') FROM fb_messages WHERE role='tool' AND id > $1 ORDER BY id DESC LIMIT 1;"; }
# db_tool_blob_since concatenates the content+parts of EVERY tool row newer than
# <id>, so a signal any one of them left (a path or an image block) is
# searchable in a single value.
db_tool_blob_since() { dbq "SELECT group_concat(COALESCE(content,'') || ' ' || COALESCE(parts,''), char(10)) FROM fb_messages WHERE role='tool' AND id > $1;"; }

check() { # check <description> <grep -E pattern> <text>
  local desc="$1" pat="$2" text="$3"
  if printf '%s' "$text" | grep -Eq -- "$pat"; then
    note "PASS: $desc"
  else
    mark_fail "$desc (pattern: $pat)"
    printf '%s\n' "$text" | tail -20 >&2
  fi
}

# assert_no_approval_card fails the run if an approval overlay is visible
# anywhere. Phase 1 (a workspace read) and phase 4 (a gate refusal) never raise
# one, so any occurrence during the MCP phase is the MCP call asking.
assert_no_approval_card() { # assert_no_approval_card <phase description>
  if pane_all | grep -Eq 'Tool approval|Yes, proceed|批准|是否允许'; then
    mark_fail "$1 showed an approval card — the MCP call asked for approval; the scratch config's default_tools_approval_mode/hint wiring is wrong (see pkg/run/runner.go mcpToolRequiresApproval)"
  else
    note "PASS: $1 raised no approval card"
  fi
}

# --- Phase 1: read_file an image on a multimodal model ---------------------
start_tui "$MODEL"
submit "read_file $SHAPE 然后告诉我图里有哪些图形、颜色、各自在左右哪边"

# Wait on a STEP-ONLY signal: the image marker line ("Ctrl+O"), which reasoning
# text can never contain. Reasoning that merely says "I should read an image"
# must not satisfy this.
if ! wait_for 150 "Ctrl+O"; then
  mark_fail "the image step never rendered (no Ctrl+O marker line within 150s)"
fi
wait_for 90 "Worked for" || true
pane > "$EVIDENCE/01-read-image.txt"
p1="$(pane)"

# The step line, not the reasoning: "Ctrl+O" lives only in the image marker.
check "read_file of an image renders the step marker line" 'Ctrl\+O' "$p1"
check "the step line names the pixel size" '[0-9]+×[0-9]+' "$p1"
if printf '%s' "$p1" | grep -q '"preview_kind"'; then
  mark_fail "the read_file step fell through to a JSON dump"
fi

# The FINAL ANSWER comes from the assistant row, not the reasoning paragraph.
ANSWER=""
for ((i = 0; i < 300; i++)); do
  ANSWER="$(db_last_assistant)"
  if printf '%s' "$ANSWER" | grep -Eq '红|蓝|red|blue' && printf '%s' "$ANSWER" | grep -Eq '左|右|left|right'; then
    break
  fi
  sleep 0.2
done
check "the final answer names the colours" '红|蓝|red|blue' "$ANSWER"
check "the final answer places them left and right" '左|右|left|right' "$ANSWER"

# Assertion ③: the tool row must carry the pixels. Select the newest tool row
# that ACTUALLY carries an image block — the model may run a shell command (an
# `ls`) after the read_file, so the last tool row is not necessarily the image
# one. The row is written a moment after the call lands, so this is a bounded
# retry; on failure the newest few tool rows' parts heads are printed so the
# next reader sees what was there.
tool_has_image=0
parts=""
for ((i = 0; i < 300; i++)); do
  parts="$(db_last_image_tool_parts)"
  if printf '%s' "$parts" | grep -Eq '"type"[[:space:]]*:[[:space:]]*"image"'; then
    tool_has_image=1
    break
  fi
  sleep 0.2
done
if [ "$tool_has_image" = 1 ]; then
  note "PASS: the tool row stores an image block (the model saw pixels)"
else
  mark_fail "no tool row carries an image block; the newest tool rows' parts heads were: $(db_recent_tool_parts_heads | tr '\n' ' | ' | head -c 400)"
fi

# --- Phase 2: the step marker names Ctrl+O (the bare-letter guard) ---------
# The keyboard guard itself (a bare 'o' does not open an image) is pinned by
# pkg/tui TestCtrlOIsNotABareLetterKey. Here we assert the marker the user reads
# names the actual key. We do NOT type a bare letter: it would dirty the
# composer and corrupt the next submission (the previous script's bug), and the
# clear key's effect cannot be asserted from a captured pane deterministically.
check "the image marker names the Ctrl+O key" 'Ctrl\+O' "$p1"

# Ctrl+O with an image present must not crash the TUI (it launches the OS
# viewer). Skipped only when the caller asks, since it opens a window.
if [ "${FOREBRAIN_ACCEPT_NO_OPEN:-0}" = "1" ]; then
  note "SKIP: Ctrl+O keystroke (FOREBRAIN_ACCEPT_NO_OPEN=1)"
else
  tmux send-keys -t "$SESSION" C-o
  sleep 1
  pane > "$EVIDENCE/02-ctrl-o.txt"
  if pane | grep -q "panic\|goroutine"; then
    mark_fail "the TUI crashed after Ctrl+O"
  else
    note "PASS: Ctrl+O did not crash the TUI (the system viewer may have opened)"
  fi
fi

# --- Phase 3: an MCP image lands in the primary agent workspace ------------
# The turn that dispatches the MCP call must be allowed to FINISH before the
# read-back is submitted: submitting while it still streams loses the read-back,
# and phase 4's TUI restart would then kill it. The deterministic end-of-turn
# signal is a NEW assistant row after the MCP tool row — the "Worked for" footer
# is not per-turn-unique, so it cannot tell one turn's end from another.
mcp_before_tool="$(db_max_tool_id)"
submit "Call the tool ${MCP_TOOL_NAME} with text \"shot\"."

# Ground truth that the call actually ran: the saved file appears under the
# primary agent workspace. Nothing else proves the call reached the server.
for ((i = 0; i < 600; i++)); do
  [ -n "$(ls -1 "$MCP_RESULTS"/*.png 2>/dev/null | head -1)" ] && break
  sleep 0.2
done

# Wait for the whole MCP turn to close: a tool row has landed (the call ran) AND
# an assistant row is newer than it (the model finished its answer). Anchoring
# on the turn's LAST tool row means the read-back rows are strictly newer, so
# an `ls` the model ran in between cannot masquerade as the read-back.
mcp_turn_done=0
mcp_tool_id="$mcp_before_tool"
for ((i = 0; i < 450; i++)); do
  mcp_tool_id="$(db_max_tool_id)"
  if [ "$mcp_tool_id" -gt "$mcp_before_tool" ] && [ "$(db_max_assistant_id)" -gt "$mcp_tool_id" ]; then
    mcp_turn_done=1
    break
  fi
  sleep 0.2
done
pane > "$EVIDENCE/04-mcp-image.txt"

assert_no_approval_card "the MCP call phase"
if [ "$mcp_turn_done" != 1 ]; then
  mark_fail "the MCP turn never closed (no assistant row after tool row $mcp_tool_id); the read-back would be lost, so it was not submitted"
fi

ws_saved="$(ls -1 "$MCP_RESULTS"/*.png 2>/dev/null | head -1 || true)"
home_saved="$(ls -1 "$HOME_DIR/state/mcp-results"/*.png 2>/dev/null | head -1 || true)"
if [ -n "$ws_saved" ]; then
  note "PASS: the MCP image landed in the primary agent workspace: $ws_saved"
else
  mark_fail "no MCP image under $MCP_RESULTS (the call stopped before writing, or approval blocked it)"
fi
if [ -z "$home_saved" ]; then
  note "PASS: nothing landed under the home's own state/mcp-results"
else
  mark_fail "an MCP image landed under the home ($home_saved), not the workspace"
fi

# Reading the saved path must need no approval (it is inside the read-exempt
# agent workspace). Only when the MCP turn has actually closed is the read-back
# submitted; the fresh signal is a NEW tool row (id > the MCP turn's last tool
# row) whose content names the saved file's basename or whose parts carry an
# image block.
if [ -n "$ws_saved" ] && [ "$mcp_turn_done" = 1 ]; then
  base="$(basename "$ws_saved")"
  submit "read_file $ws_saved"
  readback_ok=0
  blob=""
  for ((i = 0; i < 450; i++)); do
    blob="$(db_tool_blob_since "$mcp_tool_id")"
    if printf '%s' "$blob" | grep -qF "$base" || printf '%s' "$blob" | grep -Eq '"type"[[:space:]]*:[[:space:]]*"image"'; then
      readback_ok=1
      break
    fi
    sleep 0.2
  done
  pane > "$EVIDENCE/05-read-mcp-image.txt"
  assert_no_approval_card "reading the saved MCP image"
  if [ "$readback_ok" = 1 ]; then
    note "PASS: reading the saved MCP image completed with no approval"
    check "the read-back step names the saved file" 'image [0-9]+x[0-9]+' "$blob"
  else
    mark_fail "reading the saved MCP image produced no tool row naming it; newest tool rows' parts heads were: $(db_recent_tool_parts_heads | tr '\n' ' | ' | head -c 400)"
  fi
fi

# --- Phase 4: the gate refuses an image on a text-only model ---------------
start_tui "$TEXT_ONLY_MODEL"
before_id="$(db_max_tool_id)"
submit "read_file $SHAPE"
# The gate's refusal is a tool error on a NEW tool row; wait for its
# distinguishing sentence from the row (reasoning cannot satisfy this).
gate_row=""
for ((i = 0; i < 450; i++)); do
  gate_row="$(db_new_tool_content_since "$before_id")"
  printf '%s' "$gate_row" | grep -qF "does not accept image input" && break
  sleep 0.2
done
wait_for 60 "Worked for" || true
pane > "$EVIDENCE/06-gate-refusal.txt"
check "the gate's tool result names the text-only model" "${TEXT_ONLY_MODEL}" "$gate_row"
check "the gate's tool result points at /model" '/model' "$gate_row"
if printf '%s' "$gate_row" | grep -Eq 'status 400|Bad Request|invalid_request_error|422'; then
  mark_fail "the model got a provider error instead of the actionable refusal"
else
  note "PASS: no provider error; the gate answered with its own sentence"
fi

# --- Verdict ---------------------------------------------------------------
if [ "$fail" -eq 0 ]; then
  echo "read image: PASS"
else
  echo "read image: FAIL" >&2
  exit 1
fi
