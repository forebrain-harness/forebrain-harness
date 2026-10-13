#!/usr/bin/env bash
# Drive the forebrain TUI through the run-forebrain fake provider, submit one turn
# that makes the scripted tool render a card, and capture the pane both with
# SGR codes and plain.
#
#   drive-card.sh <work-dir> <session> <port> <fixture> <tool-args-json> \
#                 <prompt> <out-prefix> [truecolor|256]
#
# Writes <out-prefix>.card.ansi and <out-prefix>.card.txt.
#
# Requires <work>/forebrain.bin (see build-binary.sh) and the repo's driver skill.
#
# The prompt text does not select the card (the fake provider scripts its tool
# call by request count), so keep it short: it exists only to start a turn.
set -euo pipefail

WORK="${1:-}"; SESSION="${2:-}"; PORT="${3:-}"; FIXTURE="${4:-}"
TOOL_ARGS="${5:-}"; PROMPT="${6:-}"; OUT="${7:-}"; DEPTH="${8:-truecolor}"
REPO="${FOREBRAIN_REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)}"
D="$REPO/.claude/skills/run-forebrain/driver.sh"
ATTEMPTS="${ATTEMPTS:-3}"
# Wait after the turn reaches the provider, for the card to paint.
SETTLE="${SETTLE:-5}"

die() { echo "drive-card: $*" >&2; exit 1; }
[ -n "$OUT" ] && [ -n "$SESSION" ] || die "usage: drive-card.sh <work> <session> <port> <fixture> <tool-args-json> <prompt> <out-prefix> [truecolor|256]"
[ -x "$D" ] || die "missing $D (the run-forebrain skill is a precondition)"
[ -x "$WORK/forebrain.bin" ] || die "no $WORK/forebrain.bin — run build-binary.sh first"

# The driver launches $WORK/forebrain. Park a wrapper there: it is the only way to
# control COLORTERM inside the pane, because tmux injects COLORTERM=truecolor
# into every session it creates regardless of the launching shell.
case "$DEPTH" in
  256)
    cat > "$WORK/forebrain" <<WRAP
#!/bin/sh
exec env -u COLORTERM TERM=xterm-256color $WORK/forebrain.bin "\$@"
WRAP
    ;;
  truecolor)
    cat > "$WORK/forebrain" <<WRAP
#!/bin/sh
exec env COLORTERM=truecolor TERM=xterm-256color $WORK/forebrain.bin "\$@"
WRAP
    ;;
  *) die "depth must be truecolor or 256, got $DEPTH" ;;
esac
chmod +x "$WORK/forebrain"

export FOREBRAIN_RUN_WORK="$WORK" FOREBRAIN_RUN_SESSION="$SESSION" FOREBRAIN_RUN_PORT="$PORT"

# The composer's own line, trailing whitespace trimmed. The prompt marker "›" is
# what distinguishes the ready composer from the splash screen, which paints the
# same "/ commands" hint the driver otherwise waits on.
#
# A pane still showing the workspace-trust page has no composer yet, whatever
# else is on it: anything typed there goes to the page, not the composer.
composer_line() {
  local pane
  pane="$(tmux capture-pane -t "$SESSION" -p 2>/dev/null)" || return 0
  case "$pane" in *"Trust this directory?"*) return 0 ;; esac
  printf '%s\n' "$pane" | grep -F "›" | tail -1 | sed 's/[[:space:]]*$//'
}

start_session() {
  # reset recreates $WORK/proj and $WORK/home, so the fixture is copied after
  # start — staging it earlier means the tool call finds nothing.
  "$D" stop >/dev/null 2>&1 || true
  "$D" reset >/dev/null 2>&1 || true
  # A start that reports "never reached the composer" is not fatal: the screen
  # lags the producer, so poll the pane ourselves.
  "$D" start tool "$TOOL_ARGS" >/dev/null 2>&1 || true
  mkdir -p "$WORK/proj"
  cp "$FIXTURE" "$WORK/proj/$(basename "$FIXTURE")"
  # driver.sh answers the trust page, but only inside its own 15s window; a
  # slower launch leaves it on screen and the driver's failure is ignored above,
  # so answer it here too: Down then Enter, since the page opens on Quit. Once
  # only: a second Enter would reach the composer and submit an empty turn.
  local trusted=no
  for _ in $(seq 1 180); do
    if [ "$trusted" = no ] \
      && tmux capture-pane -t "$SESSION" -p 2>/dev/null | grep -qF "Trust this directory?"; then
      tmux send-keys -t "$SESSION" Down Enter
      trusted=yes
      sleep 0.5
      continue
    fi
    [ -n "$(composer_line)" ] && return 0
    sleep 0.5
  done
  return 1
}

# Get the prompt into the composer, then submit.
#
# Type once and verify, never in a retry loop: this composer has no clear-line
# key (`C-u` is a no-op and Escape does not wipe the line), so repeating the send
# appends and interleaves into visible garbage, which is easy to mistake for lost
# input. A miss means the session is thrown away and started again — the retry
# lives one level up, in the attempt loop.
#
# Writing to the pane's slave tty is not an alternative: that only *prints* the
# text. The composer buffer stays empty, and Enter does nothing.
submit_prompt() {
  tmux send-keys -t "$SESSION" -l "$PROMPT"
  for _ in $(seq 1 24); do
    # Exact match: a partial delivery leaves different text behind, and the
    # composer is the authority on what will be submitted.
    [ "$(composer_line)" = "› $PROMPT" ] && return 0
    sleep 0.25
  done
  return 1
}

# Producer-side confirmation that the turn left forebrain (the screen lags it).
wait_for_request() {
  for _ in $(seq 1 30); do
    grep -q REQUEST "$WORK/provider.log" 2>/dev/null && return 0
    sleep 0.5
  done
  return 1
}

landed=false
for attempt in $(seq 1 "$ATTEMPTS"); do
  start_session || die "composer never appeared; see $WORK/tui.err"
  submit_prompt || { echo "drive-card: attempt $attempt: composer holds $(composer_line)" >&2; tmux kill-session -t "$SESSION" 2>/dev/null || true; continue; }
  tmux send-keys -t "$SESSION" Enter
  if wait_for_request; then landed=true; break; fi
  echo "drive-card: attempt $attempt: no request reached the provider" >&2
  tmux kill-session -t "$SESSION" 2>/dev/null || true
done
[ "$landed" = true ] || die "no turn reached the provider in $ATTEMPTS attempts"

sleep "$SETTLE"

tmux capture-pane -t "$SESSION" -p -e > "$OUT.card.ansi"
tmux capture-pane -t "$SESSION" -p > "$OUT.card.txt"
echo "wrote $OUT.card.ansi ($DEPTH)"
