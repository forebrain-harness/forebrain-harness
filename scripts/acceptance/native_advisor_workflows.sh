#!/usr/bin/env bash
# Behavioural acceptance for the plan workflows the primary agent carries: the
# shared audit/planning contract, the single-plan branch, the independent plan
# reviewer, and the execution contract an approved plan hands back.
#
# Two halves, one entry point:
#
#   --fake   a scripted provider. Every marker here is a WIRING assertion: what
#            the model was sent, which tool results came back, what landed on
#            disk, what the CLI stub was asked to do. It never claims the model
#            behaved well.
#   --real   the owner's zhipuai/glm-5.3. The same fixtures, driven by
#            a real model, which is the only place model judgement is measured.
#            Five markers are OBSERVED rather than PASS: coverage policy is not
#            machine-checkable, so it is written into the evidence for a human.
#
# Everything runs in an isolated home under $FOREBRAIN_ACCEPT_HOME and a
# throwaway workspace under $FOREBRAIN_ACCEPT_WORK. Both must already exist and
# be empty, and the caller must not have FOREBRAIN_HOME set: a run that inherits
# the owner's home would certify the wrong thing.
#
#   env -u FOREBRAIN_HOME FOREBRAIN_ACCEPT_HOME="$(mktemp -d)" \
#       FOREBRAIN_ACCEPT_WORK="$(mktemp -d)" \
#       scripts/acceptance/native_advisor_workflows.sh --fake
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DRIVER="$REPO/scripts/acceptance/driver.sh"

MODE="${1:-}"
case "$MODE" in
  --fake|--real) ;;
  *) echo "usage: native_advisor_workflows.sh --fake|--real" >&2; exit 2 ;;
esac

[ -z "${FOREBRAIN_HOME:-}" ] || { echo "refusing ambient FOREBRAIN_HOME"; exit 2; }
ACCEPT_HOME="${FOREBRAIN_ACCEPT_HOME:-}"
ACCEPT_WORK="${FOREBRAIN_ACCEPT_WORK:-}"
[ -n "$ACCEPT_HOME" ] && [ -d "$ACCEPT_HOME" ] && [ -z "$(ls -A "$ACCEPT_HOME")" ] \
  || { echo "FOREBRAIN_ACCEPT_HOME must be an existing empty directory" >&2; exit 2; }
[ -n "$ACCEPT_WORK" ] && [ -d "$ACCEPT_WORK" ] && [ -z "$(ls -A "$ACCEPT_WORK")" ] \
  || { echo "FOREBRAIN_ACCEPT_WORK must be an existing empty directory" >&2; exit 2; }

PROVIDER_MODEL="zhipuai/glm-5.3"
REAL_PROVIDER="zhipuai"
REAL_MODEL="glm-5.3"
REAL_BASE_URL="https://open.bigmodel.cn/api/coding/paas/v4"
# glm-5.3 reasons before every answer; at the owner's own "xhigh" setting a single
# turn can outlast the whole matrix. The acceptance run is about whether the
# workflow wiring holds, so it asks for the lowest reasoning effort the provider
# offers - the model is unchanged.
REAL_REASONING_EFFORT="${REAL_REASONING_EFFORT:-low}"
BIN="$ACCEPT_HOME/forebrain"
HOME_DIR="$ACCEPT_HOME/home"
PROJ="$ACCEPT_HOME/proj"
EVIDENCE="$ACCEPT_WORK/evidence"
# Its own tmux server: the acceptance run must not share one with whatever else
# the machine is running, and it must not depend on that server's environment.
export TMUX_TMPDIR="$ACCEPT_WORK/tmux"
mkdir -p "$TMUX_TMPDIR"
FIXTURES="$ACCEPT_WORK/fixtures"
FAKE_BIN="$ACCEPT_WORK/bin"
DUMP="$ACCEPT_WORK/dump"
LOGS="$ACCEPT_WORK/logs"
PORT=$(( 8700 + ($$ % 90) ))
SESSION="adv-$MODE-$$"
PASSED=0
FAILED=0
OBSERVED=0
START_TS=$(date +%s)
mkdir -p "$EVIDENCE" "$FIXTURES" "$FAKE_BIN" "$LOGS"
# A scenario that wants the model to answer in plain text points the provider at
# this empty script: the tool-script path falls through to the answer text once
# the script is exhausted.
EMPTY_SCRIPT="$LOGS/scripts-empty.json"
printf '%s' '[]' > "$EMPTY_SCRIPT"

note() { printf '%s\n' "$*" >> "$LOGS/run.log"; }
pass() { PASSED=$((PASSED+1)); printf 'PASS %s\n' "$1"; note "PASS $1"; }
fail() {
  FAILED=$((FAILED+1))
  printf 'FAIL %s: %s\n' "$1" "${2:-}"
  note "FAIL $1: ${2:-}"
  if [ -f "$DUMP/requests.jsonl" ]; then cp -f "$DUMP/requests.jsonl" "$EVIDENCE/$1.requests.jsonl" 2>/dev/null || true; fi
  tmux capture-pane -t "$SESSION" -p > "$EVIDENCE/$1.screen.txt" 2>/dev/null || true
}
observed() { OBSERVED=$((OBSERVED+1)); printf 'OBSERVED %s: %s\n' "$1" "${2:-}"; note "OBSERVED $1: ${2:-}"; }

TMUX_ENV_NAMES=""
tui_env() { # tui_env <NAME> <value>
  tmux set-environment -g "$1" "$2"
  TMUX_ENV_NAMES="$TMUX_ENV_NAMES $1"
}
tui_env_clear() {
  local name
  for name in $TMUX_ENV_NAMES; do tmux set-environment -gu "$name" 2>/dev/null || true; done
  TMUX_ENV_NAMES=""
}
cleanup_run() {
  tmux kill-server 2>/dev/null || true
  tui_env_clear
}
trap cleanup_run EXIT

driver() {
  env -u FOREBRAIN_HOME \
    FOREBRAIN_RUN_WORK="$ACCEPT_HOME" \
    FOREBRAIN_RUN_SESSION="$SESSION" \
    FOREBRAIN_RUN_PORT="$PORT" \
    PATH="$FAKE_BIN:$PATH" \
    bash "$DRIVER" "$@"
}
db() { sqlite3 "$HOME_DIR/state/forebrain.state.sqlite" "$1"; }
screen() { tmux capture-pane -t "$SESSION" -p 2>/dev/null || true; }
# A live approval card is identified by what sits at the BOTTOM of the pane: its
# key-hint line, with an option row just above it. The pane keeps history, so
# grepping the whole screen would find cards that closed minutes ago.
card_on_screen() {
  screen | tail -n 8 | grep -q "Esc to cancel" || return 1
  screen | tail -n 16 | grep -qE "^ *>? *[0-9]+\. " || return 1
  return 0
}
shot() { screen > "$EVIDENCE/${1:-screen}.txt"; }

# last_request / request_at print the i-th (1-based) or final request body the
# provider received in the current scenario.
request_at() { sed -n "${1}p" "$DUMP/requests.jsonl" 2>/dev/null; }
last_request() { tail -n 1 "$DUMP/requests.jsonl" 2>/dev/null; }
request_count() { wc -l < "$DUMP/requests.jsonl" 2>/dev/null | tr -d ' '; }

# ---------------------------------------------------------------- fixtures --
# Three project shapes, small enough to reason about in one screen: a version-
# controlled code fixture with a feature branch that is ahead of main, a
# document fixture with a source folder, and a batch task workspace whose plan
# backlog sits in a read-only inputs/ directory. Each carries one deliberate
# weakness so an audit has something true to find.
make_plan_backlog() { # make_plan_backlog <dir>
  local dir="$1"
  mkdir -p "$dir/plans"
  cat > "$dir/plans/README.md" <<'MD'
# Plan backlog

| # | Plan | Status |
|---|---|---|
| 023 | Split the importer | DONE |
| 024 | Cache the sheet parser | BLOCKED |
| 025 | Retry the uploader | IN PROGRESS |
| 026 | Document the column mapping | TODO |
MD
  cat > "$dir/plans/023-split-the-importer.md" <<'MD'
# Split the importer

Status: DONE

Done criteria: `python3 -m unittest discover tests` lists `test_importer` cases
and they pass. Verified on the current tree.
MD
  cat > "$dir/plans/024-cache-the-sheet-parser.md" <<'MD'
# Cache the sheet parser

Status: BLOCKED

Blocked on: the parser cache needs a key the workbook loader does not expose
today (see `reader.py:41`). Nothing else in the plan can start until that key
exists.
MD
  cat > "$dir/plans/025-retry-the-uploader.md" <<'MD'
# Retry the uploader

Status: IN PROGRESS

Steps 1-2 landed in `uploader.py`; step 3 (`Retry-After` handling) was never
started. The executor that held this plan left no execution workspace.
MD
  cat > "$dir/plans/026-document-the-column-mapping.md" <<'MD'
# Document the column mapping

Status: TODO

Add the mapping table to `docs/columns.md`. The file does not exist yet, and
`reader.py:12` still maps three columns by hand.
MD
}

make_fixtures() {
  local d
  # --- version control: main plus a feature branch two commits ahead ---------
  d="$FIXTURES/vcs"
  mkdir -p "$d/src"
  printf '# Ledger importer\n\nImports monthly ledgers from CSV.\n' > "$d/README.md"
  cat > "$d/src/reader.py" <<'PY'
COLUMNS = {"date": 0, "amount": 1, "note": 2}


def read_row(line):
    parts = line.split(",")
    return {name: parts[i] for name, i in COLUMNS.items()}
PY
  ( cd "$d" && git init -q -b main \
      && git -c user.email=accept@example.com -c user.name=acceptance add -A \
      && git -c user.email=accept@example.com -c user.name=acceptance commit -q -m "importer" \
      && git checkout -q -b feature/amount-scale \
      && printf 'SCALE = 100\n' >> src/reader.py \
      && git -c user.email=accept@example.com -c user.name=acceptance commit -q -am "amount scale" \
      && printf 'def scale(amount):\n    return amount / SCALE\n' >> src/reader.py \
      && git -c user.email=accept@example.com -c user.name=acceptance commit -q -am "scale helper" )

  # --- version control: on the default branch, nothing ahead ----------------
  d="$FIXTURES/vcs-default"
  mkdir -p "$d/src"
  printf '# Importer\n' > "$d/README.md"
  printf 'def read_row(line):\n    return line.split(",")\n' > "$d/src/reader.py"
  ( cd "$d" && git init -q -b main \
      && git -c user.email=accept@example.com -c user.name=acceptance add -A \
      && git -c user.email=accept@example.com -c user.name=acceptance commit -q -m "importer" )

  # --- version control: a feature branch with zero commits ahead -----------
  d="$FIXTURES/vcs-zero"
  mkdir -p "$d/src"
  printf '# Importer\n' > "$d/README.md"
  printf 'def read_row(line):\n    return line.split(",")\n' > "$d/src/reader.py"
  ( cd "$d" && git init -q -b main \
      && git -c user.email=accept@example.com -c user.name=acceptance add -A \
      && git -c user.email=accept@example.com -c user.name=acceptance commit -q -m "importer" \
      && git checkout -q -b feature/empty )

  # --- no version control at all -------------------------------------------
  d="$FIXTURES/nongit"
  mkdir -p "$d/src"
  printf '# Ledger importer\n\nNo history here; the folder is all there is.\n' > "$d/README.md"
  printf 'def read_row(line):\n    return line.split(",")\n' > "$d/src/reader.py"

  # --- documents: sources plus a deliverable that does not exist yet --------
  d="$FIXTURES/docs"
  mkdir -p "$d/sources" "$d/inputs"
  printf '# Q3 board pack\n\nAudience: the board. Three pages maximum.\n' > "$d/README.md"
  cat > "$d/sources/spend.csv" <<'CSV'
unit,planned,actual
ops,120000,137500
sales,90000,88000
CSV
  cat > "$d/sources/notes.md" <<'MD'
## Section notes

Ops overspent; sales came in under plan. The board asked for one page per unit.
MD
  printf 'Immutable source: do not edit.\n' > "$d/inputs/board-request.txt"

  # --- batch workspace: a plan backlog inside read-only inputs/ ------------
  d="$FIXTURES/batch"
  mkdir -p "$d/inputs" "$d/outputs"
  printf 'Unattended audit task. Nothing outside this folder may be written.\n' > "$d/TASK.md"
  make_plan_backlog "$d/inputs"
  printf 'Immutable: the backlog snapshot this run reconciles.\n' > "$d/inputs/README.txt"

  # --- issue publication: a backlog plus a sensitive finding ---------------
  d="$FIXTURES/issues"
  mkdir -p "$d"
  make_plan_backlog "$d"
  cat > "$d/plans/027-rotate-the-api-secret.md" <<'MD'
# Rotate the API secret

Status: TODO

`config/deploy.yaml:14` still carries a long-lived API key of the deployment
provider. The plan rotates it, moves it to the environment, and records the
rotation. This is sensitive: the location must not be published to a public
issue before the key is rotated.
MD

  # --- trivial request: one obvious edit, no plan needed -------------------
  d="$FIXTURES/trivial"
  mkdir -p "$d"
  printf '# Ledger\n\n## Intput\n\nA typo the reader can see.\n' > "$d/README.md"
}

# ----------------------------------------------------------------- gh stub --
# A stand-in for the issue CLI. It never opens a socket: its whole job is to
# answer the three questions a publication run asks and to record every call, so
# "no issue was created" is a fact on disk rather than a claim.
# make_gh_stub <name> <auth:ok|fail> <remote:ok|fail> <visibility> -> prints path
make_gh_stub() {
  local name="$1" auth="$2" remote="$3" vis="$4"
  local path="$FAKE_BIN/gh-$name" log="$LOGS/gh-$name.jsonl"
  rm -f "$log"
  cat > "$path" <<SH
#!/usr/bin/env bash
log="$log"
auth="$auth"
remote="$remote"
vis="$vis"
printf '{"argv":%s,"auth":"%s","remote":"%s","visibility":"%s"}\n' \
  "\$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1:]))' "\$@")" \
  "\$auth" "\$remote" "\$vis" >> "\$log"
case "\$1 \$2" in
  "auth status")
    [ "\$auth" = "ok" ] || { echo "gh: not logged in" >&2; exit 1; }
    echo "Logged in as acceptance"
    ;;
  "repo view")
    [ "\$auth" = "ok" ] || { echo "gh: not logged in" >&2; exit 1; }
    [ "\$remote" = "ok" ] || { echo "gh: no git remotes configured" >&2; exit 1; }
    printf '{"visibility":"%s"}\n' "\$vis"
    ;;
  "issue create")
    [ "\$auth" = "ok" ] || { echo "gh: not logged in" >&2; exit 1; }
    [ "\$remote" = "ok" ] || { echo "gh: no git remotes configured" >&2; exit 1; }
    echo "https://example.invalid/acceptance/issues/\$\$"
    ;;
  *) echo "gh: unsupported in acceptance: \$*" >&2; exit 1 ;;
esac
SH
  chmod +x "$path"
  cp -f "$path" "$FAKE_BIN/gh"
  printf '%s' "$path"
}

# ------------------------------------------------------- request inspection --
# One small reader over the dumped request bodies: it answers the questions the
# assertions ask (what system prompt went out, what the last user turn said,
# whether a fragment appears, how often) without a second language in the test.

make_probe() {
  cat > "$LOGS/probe.py" <<'PY'
import json, sys

path, mode = sys.argv[1], sys.argv[2]
lines = [l for l in open(path, encoding="utf-8").read().split("\n") if l.strip()]


def load(n):
    return json.loads(lines[n - 1])


def text_of(part):
    if isinstance(part, str):
        return part
    if isinstance(part, dict):
        return part.get("text") or part.get("content") or ""
    return ""


def messages(n):
    return load(n).get("messages") or []


if mode == "count":          # count -> number of requests
    print(len(lines))
elif mode == "sys":          # sys <n> -> the system prompt
    for m in messages(int(sys.argv[3])):
        if m.get("role") == "system":
            body = m.get("content")
            if isinstance(body, list):
                print("\n".join(text_of(part) for part in body), end="")
            else:
                print(text_of(body), end="")
            break
elif mode == "user":         # user <n> -> the last non-meta user turn
    body = ""
    for m in messages(int(sys.argv[3])):
        if m.get("role") == "user":
            parts = m.get("content")
            body = "\n".join(text_of(p) for p in parts) if isinstance(parts, list) else text_of(parts)
    print(body, end="")
elif mode == "has":          # has <n> <needle> -> yes|no over the whole body
    print("yes" if sys.argv[4] in lines[int(sys.argv[3]) - 1] else "no")
elif mode == "countin":      # countin <n> <needle> -> occurrences in the whole body
    print(lines[int(sys.argv[3]) - 1].count(sys.argv[4]))
elif mode == "toolnames":    # toolnames <n> -> tool names the conversation already called
    names = []
    for m in messages(int(sys.argv[3])):
        for call in m.get("tool_calls") or []:
            names.append((call.get("function") or {}).get("name") or "")
    print("\n".join(names))
elif mode == "syswith":       # syswith <needle> -> system prompt of that request
    for n, line in enumerate(lines, start=1):
        if sys.argv[3] in line:
            for m in messages(n):
                if m.get("role") == "system":
                    body = m.get("content")
                    if isinstance(body, list):
                        print("\n".join(text_of(part) for part in body), end="")
                    else:
                        print(text_of(body), end="")
                    break
            break
elif mode == "all":          # all <needle> -> yes|no over every request
    print("yes" if any(sys.argv[3] in l for l in lines) else "no")
PY
}
q() { python3 "$LOGS/probe.py" "$DUMP/requests.jsonl" "$@"; }

# ------------------------------------------------------------- session flow --
CARD_COUNT=0
TURN_COUNT=0
screen_count() { screen | grep -cF -- "$1" || true; }
wait_grew() { # wait_grew <needle> <previous-count> <seconds>
  local needle="$1" prev="$2" secs="$3" i
  for ((i = 0; i < secs * 4; i++)); do
    [ "$(screen_count "$needle")" -gt "$prev" ] && return 0
    sleep 0.25
  done
  return 1
}
wait_text() { driver wait "$1" "${2:-60}" >/dev/null 2>&1; }
# ADV_ONLY narrows a debugging run to the named scenarios; unset runs them all.
want() { [ -z "${ADV_ONLY:-}" ] && return 0; case " $ADV_ONLY " in *" $1 "*) return 0 ;; esac; return 1; }
# The trust question is a dialog INSIDE the TUI: the composer hint renders
# behind it, so "/ commands" being visible does not prove the dialog is gone,
# and it can paint a frame after the composer. Its actions are [Quit, Trust and
# continue] and it opens on Quit, so anything that presses Enter while it is up
# quits the app. Answer it with one Down+Enter and then wait for it to leave;
# never conclude "no dialog" from a single early frame.
accept_trust() {
  local i answered=no
  for ((i = 0; i < 40; i++)); do
    if screen | grep -q "Trust this folder"; then
      if [ "$answered" = no ]; then
        driver key Down >/dev/null 2>&1
        sleep 0.2
        driver key Enter >/dev/null 2>&1
        answered=yes
      fi
      sleep 0.3
      continue
    fi
    [ "$answered" = yes ] && return 0
    # No dialog yet: give it three seconds to paint before trusting the view.
    [ "$i" -ge 15 ] && return 0
    sleep 0.2
  done
  echo "trust dialog still on screen" >&2
  return 1
}

# Start a fresh provider + TUI for one scenario. <mode> <fixture> [--plan]
# [--type office|batch] [--no-git]
scenario_start() {
  local mode="$1" fixture="$2"; shift 2
  local want_plan=no want_type="" want_git=yes
  while [ $# -gt 0 ]; do
    case "$1" in
      --plan) want_plan=yes ;;
      --type) want_type="$2"; shift ;;
      --no-git) want_git=no ;;
    esac
    shift
  done
  driver reset >/dev/null 2>&1 || true
  rm -rf "$DUMP"; mkdir -p "$DUMP"
  tui_env PATH "$FAKE_BIN:$PATH"
  export FAKE_TOOL_SCRIPT="${SCENARIO_SCRIPT:-$EMPTY_SCRIPT}"
  export FAKE_DUMP_DIR="$DUMP"
  # A launch can lose its provider port or its first paint; one clean retry
  # keeps an infrastructure hiccup from reading as a failed assertion.
  local attempt
  for attempt in 1 2; do
    if driver start "$mode" >/dev/null 2>&1 && accept_trust && wait_text "/ commands" 30; then
      break
    fi
    driver stop >/dev/null 2>&1 || true
    PORT=$(( PORT + 7 ))
    sleep 1
  done
  screen | grep -q "/ commands" || { echo "composer never appeared"; return 1; }
  # The fixture lands after the project row exists and before the first turn.
  if [ -n "$fixture" ]; then
    cp -R "$FIXTURES/$fixture/." "$PROJ/" 2>/dev/null || true
  fi
  [ "$want_git" = no ] && rm -rf "$PROJ/.git"
  if [ -n "$want_type" ]; then
    db "UPDATE fb_projects SET project_type='$want_type'; UPDATE fb_sessions SET project_type='$want_type';" || return 1
    # The runner takes its project type when the app loads, so a type change
    # needs one restart before the conversation is born with it.
    driver stop >/dev/null 2>&1 || true
    driver start "$mode" >/dev/null 2>&1 || return 1
    accept_trust
    wait_text "/ commands" 30 || return 1
  fi
  CARD_COUNT=0
  TURN_COUNT=$(screen_count "Worked for")
  SCENARIO_SCRIPT=""
  if [ "$want_plan" = yes ]; then
    driver submit "/plan" >/dev/null 2>&1
    sleep 1
  fi
  return 0
}

# Submit one user turn and wait for the turn to close (the footer's elapsed
# line grows by one). <text> [seconds]
turn() {
  driver submit "$1" >/dev/null 2>&1
  wait_grew "Worked for" "$TURN_COUNT" "${2:-90}" || return 1
  TURN_COUNT=$(screen_count "Worked for")
  return 0
}

# Ask the model to submit the plan for approval and wait for the card.
exit_card() { # exit_card [seconds] -> the plan decision is on screen now
  local secs="${1:-60}" i
  for ((i = 0; i < secs * 4; i++)); do
    screen | grep -q "Would you like to proceed?" && return 0
    sleep 0.25
  done
  return 1
}

scenario_stop() { driver stop >/dev/null 2>&1 || true; }

# The two prompt fragments every project type must receive, and the reviewer
# fragment every review task must carry. A missing one is a wiring failure and
# nothing else in the scenario matters.
SHARED_WORKFLOW="# Audit and planning workflows"
SHARED_HANDOFF="# Handoff plan quality bar"
SHARED_SINGLE="## Single-plan workflow"
REVIEWER_BAR="handoff plan quality bar below, item by item"
EXECUTOR_CONTRACT="You are the primary executor of an approved plan."
REVIEW_ROW="Ask another model to review this plan"

check_system_prompt() { # check_system_prompt <marker-name> [request-number]
  local name="$1" n="${2:-1}" sys
  sys="$(q sys "$n")"
  case "$sys" in
    *"$SHARED_WORKFLOW"*) ;;
    *) fail "$name" "system prompt is missing the shared audit/planning contract"; return 1 ;;
  esac
  case "$sys" in
    *"$SHARED_HANDOFF"*) ;;
    *) fail "$name" "system prompt is missing the handoff quality bar"; return 1 ;;
  esac
  case "$sys" in
    *SKILL.md*) fail "$name" "system prompt names a skill file"; return 1 ;;
    *"$HOME/.forebrain"*) fail "$name" "system prompt leaks the owner's home"; return 1 ;;
  esac
  return 0
}

check_single_plan_wiring() { # check_single_plan_wiring <marker-name> [request-number]
  local name="$1" n="${2:-1}" sys user
  sys="$(q sys "$n")"
  case "$sys" in
    *"$SHARED_SINGLE"*) fail "$name" "the single-plan workflow must not sit in the system prompt"; return 1 ;;
  esac
  [ "$(q has "$n" "$SHARED_SINGLE")" = yes ] \
    || { fail "$name" "the full plan-mode reminder did not reach the request"; return 1; }
  return 0
}

# ------------------------------------------------------------ shared checks --
plan_dir_on_disk() { # plan_dir_on_disk <request-number>
  grep -o "$HOME_DIR/workspace/plans/[^\"\\\\ ]*" "$DUMP/requests.jsonl" 2>/dev/null | sed -n "${1:-1}p"
}
fixture_hash() { ( cd "$PROJ" && find . -path ./.git -prune -o -type f -print 2>/dev/null | sort | xargs shasum 2>/dev/null | shasum | cut -d' ' -f1 ); }
session_id() { db "SELECT id FROM fb_sessions ORDER BY created_at DESC LIMIT 1;" 2>/dev/null | tr -d ' '; }
seed_plan() { # seed_plan <dir> <file> <body-file>
  mkdir -p "$1"
  cp "$3" "$1/$2"
}
no_plan_machinery() { # no_plan_machinery <name> -> asserts nothing planned and nothing reviewed
  local name="$1" sid; sid=$(session_id)
  if db "SELECT count(*) FROM fb_messages WHERE session_id='$sid' AND content LIKE '%exit_plan_mode%';" | grep -qv '^0$'; then
    fail "$name" "the conversation planned where it should have acted"; return 1
  fi
  if [ -n "$(find "$HOME_DIR/workspace/plans" -name '*.md' 2>/dev/null | head -1)" ]; then
    fail "$name" "a plan file exists for a request that needed none"; return 1
  fi
  if [ -f "$DUMP/requests.jsonl" ] && [ "$(q all 'Review the plan below before the user decides')" = yes ]; then
    fail "$name" "a plan reviewer ran where no plan existed"; return 1
  fi
  return 0
}

# --------------------------------------------------- scenario 1: coding loop --
# Plan, review, approve, execute: the whole loop, driven through the cards the
# user actually sees. The reviewer's verdict here is the provider's fallback
# text, so what this asserts is the machinery: the plan travelled from the
# session plan file into the card and into the review task, the review was
# recorded against the approval, and the approval handed the executor contract
# back to the primary agent without ever putting it on screen.
fake_coding_plan_review_execute() {
  local name=coding-plan-review-execute
  want "$name" || return 0
  cat > "$LOGS/scripts-coding.json" <<'JSON'
[["exit_plan_mode", "{}"], ["exit_plan_mode", "{}"], ["", "{}"], ["exit_plan_mode", "{}"], ["", "{}"], ["", "{}"]]
JSON
  SCENARIO_SCRIPT="$LOGS/scripts-coding.json"
  scenario_start toolcall vcs --plan || { fail "$name" "session did not start"; return; }

  # Turn 1: no plan on disk yet, so the card is the empty-plan case.
  driver submit "Audit the importer and write me a plan." >/dev/null 2>&1
  exit_card 90 || { fail "$name" "the first turn never reached its card"; return; }
  check_system_prompt "$name" || return
  check_single_plan_wiring "$name" || return
  if ! screen | grep -q "No plan found"; then
    fail "$name" "the empty-plan card did not say the plan was missing"; shot "$name-empty"; return
  fi
  driver key Escape >/dev/null 2>&1
  sleep 2
  TURN_COUNT=$(screen_count "Worked for")

  local dir; dir=$(plan_dir_on_disk 1)
  [ -n "$dir" ] || { fail "$name" "the plan reminder named no plan directory"; return; }
  cat > "$LOGS/plan-coding.md" <<'MD'
# Scale ledger amounts before summing

## Context
`src/reader.py` reads raw amounts. The feature branch added `SCALE` and a
`scale()` helper, so callers now multiply by hand.

## Files to change
- `src/reader.py`
- `tests/test_reader.py`

## Steps
1. Apply `scale()` inside `read_row` so every caller gets ledger units.
2. Add a test that pins the scaled value for one row.

## Verification
- `python3 -m unittest discover tests`

## Tasks
- [ ] scale inside read_row
- [ ] test the scaled value

## Implementation record
MD
  seed_plan "$dir" scale-ledger-amounts.md "$LOGS/plan-coding.md"

  # Turn 2: the card now carries the plan, and offers the reviewer.
  driver submit "Submit that plan for approval." >/dev/null 2>&1
  exit_card 90 || { fail "$name" "the second turn never reached its card"; return; }
  if ! screen | grep -q "Files to change"; then
    fail "$name" "the approval card did not show the plan text"; shot "$name-card"; return
  fi
  if ! screen | grep -q "$REVIEW_ROW"; then
    fail "$name" "the approval card did not offer the independent review"; shot "$name-card"; return
  fi
  if screen | grep -qF "$EXECUTOR_CONTRACT"; then
    fail "$name" "the execution contract leaked onto the human approval card"; shot "$name-card"; return
  fi
  shot "$name-card"

  driver key m >/dev/null 2>&1
  wait_text "Which model should review this plan" 20 || { fail "$name" "the reviewer picker never opened"; return; }
  driver key Enter >/dev/null 2>&1
  wait_text "plan-reviewer task" 60 || { fail "$name" "no reviewer run was recorded"; shot "$name-review"; return; }
  sleep 2
  shot "$name-review"

  local n; n=$(q count)
  [ "$(q all "Review the plan below before the user decides")" = yes ] \
    || { fail "$name" "the review task is not the plan-review prompt"; return; }
  [ "$(q all "Investigate the project sources this plan touches")" = yes ] \
    || { fail "$name" "the review task does not order investigation first"; return; }
  [ "$(q all "Scale ledger amounts before summing")" = yes ] \
    || { fail "$name" "the review task does not carry the plan text"; return; }
  local rv; rv="$(q syswith "Review the plan below before the user decides")"
  case "$rv" in
    *"# Handoff plan quality bar"*) ;;
    *) fail "$name" "the reviewer's system prompt is missing the handoff quality bar"; return ;;
  esac
  case "$rv" in
    *"handoff quality bar below, item by item"*) ;;
    *) fail "$name" "the reviewer does not check the quality bar item by item"; return ;;
  esac
  # The fake half's contract is "dispatched and recorded, verdict wording is
  # the model's business": the plan_reviewed event is the recorded result (the
  # real half additionally checks the verdict wording, line ~1345).
  local reviewed
  reviewed=$(db "SELECT count(*) FROM fb_session_events WHERE event_type='plan_reviewed';" 2>/dev/null | tr -d ' ')
  if [ "${reviewed:-0}" = "0" ]; then
    fail "$name" "no review result was recorded"; return
  fi

  # The review is delivered by closing the card, so the planner resubmits and a
  # fresh card appears. Approve that one: option 2 is "Yes, proceed" in a
  # coding conversation.
  exit_card 90 || { fail "$name" "the plan was never resubmitted after the review"; return; }
  shot "$name-resubmitted"
  driver key 2 >/dev/null 2>&1
  sleep 0.3
  driver key Enter >/dev/null 2>&1
  wait_grew "Worked for" "$TURN_COUNT" 60 || true
  TURN_COUNT=$(screen_count "Worked for")
  sleep 2
  shot "$name-approved"
  n=$(q count)
  [ "$(q has "$n" "$EXECUTOR_CONTRACT")" = yes ] \
    || { fail "$name" "the approval did not hand back the execution contract"; return; }
  [ "$(q has "$n" "scale-ledger-amounts.md")" = yes ] \
    || { fail "$name" "the contract does not name the approved plan file"; return; }
  # The same contract comes back as the recurring reminder on the next turn.
  turn "Carry on with the plan." 60 || { fail "$name" "the follow-up turn never closed"; return; }
  [ "$(q has "$(q count)" "Plan follow-through required")" = yes ] \
    || { fail "$name" "the recurring implementation reminder is missing"; return; }
  [ "$(q has "$(q count)" "$EXECUTOR_CONTRACT")" = yes ] \
    || { fail "$name" "the recurring reminder does not carry the shared contract"; return; }
  pass "$name"
}

# The reviewer's task context must survive a plan turn that wrote hundreds of
# the agent's own rows after the request. The store-side window used to be the
# newest 200 transcript rows; 110 scripted read_file calls put 220 rows between
# the request and the review, so a window like that holds no user row at all.
fake_plan_review_long_investigation() {
  local name=plan-review-long-investigation
  want "$name" || return 0
  local script="$LOGS/scripts-long-review.json" i
  {
    printf '[["", "{}"],'
    for i in $(seq 1 110); do
      printf '["read_file", "{\\"file_path\\":\\"README.md\\",\\"offset\\":%d}"],' "$((i % 5))"
    done
    printf '["exit_plan_mode", "{}"]]'
  } > "$script"
  SCENARIO_SCRIPT="$script"
  scenario_start toolcall vcs --plan || { fail "$name" "session did not start"; return; }

  # Turn 1 only names the session's plan directory (the plan-mode reminder
  # carries it); the review needs a plan on disk, and the long turn below is the
  # request the reviewer must still be able to see.
  driver submit "Tell me where this session keeps its plan." >/dev/null 2>&1
  wait_grew "Worked for" "$TURN_COUNT" 60 || { fail "$name" "the plan-directory turn never closed"; return; }
  TURN_COUNT=$(screen_count "Worked for")
  local dir; dir=$(plan_dir_on_disk 1)
  [ -n "$dir" ] || { fail "$name" "the plan reminder named no plan directory"; return; }
  cat > "$LOGS/plan-long-investigation.md" <<'MD'
# Add S3 sync to the skill installer

## Steps
1. Upload the staged skill bundle to the configured S3 bucket.
2. Keep the local installer working when no bucket is configured.

## Verification
- `go test ./pkg/skill`
MD
  seed_plan "$dir" s3-sync.md "$LOGS/plan-long-investigation.md"

  driver submit "Add S3 sync to the skill installer." >/dev/null 2>&1
  exit_card 120 || { fail "$name" "the long investigation never reached its card"; return; }
  driver key m >/dev/null 2>&1
  wait_text "Which model should review this plan" 20 || { fail "$name" "the reviewer picker never opened"; return; }
  driver key Enter >/dev/null 2>&1
  wait_text "plan-reviewer task" 60 || { fail "$name" "no reviewer run was recorded"; shot "$name-review"; return; }
  sleep 2
  shot "$name-review"

  local review
  review=$(db "SELECT content FROM fb_messages WHERE role='user' AND content LIKE 'Review the plan below%' ORDER BY id DESC LIMIT 1;")
  case "$review" in
    *"Add S3 sync to the skill installer."*) ;;
    *) fail "$name" "the reviewer's <request> does not carry the user's ask"; return ;;
  esac
  case "$review" in
    *forebrain_environment_context*) fail "$name" "the reviewer's <request> carries the runtime environment block"; return ;;
  esac

  # The discriminator: the request sits more than one 200-row window before the
  # review, so a newest-rows window cannot reach it. The review dispatch line
  # lands in the reviewer's worker session, so the row ids are read globally and
  # the conversation id is read back from the request row itself.
  local sid req_row review_row between
  req_row=$(db "SELECT id FROM fb_messages WHERE role='user' AND content LIKE '%Add S3 sync%' AND content NOT LIKE 'Review the plan below%' ORDER BY id DESC LIMIT 1;" | tr -d ' ')
  review_row=$(db "SELECT id FROM fb_messages WHERE role='user' AND content LIKE 'Review the plan below%' ORDER BY id DESC LIMIT 1;" | tr -d ' ')
  sid=$(db "SELECT session_id FROM fb_messages WHERE id=${req_row:-0};" | tr -d ' ')
  between=$(db "SELECT count(*) FROM fb_messages WHERE session_id='${sid:-none}' AND visibility='visible' AND id > ${req_row:-0} AND id < ${review_row:-0};" | tr -d ' ')
  if [ -z "$req_row" ] || [ -z "$review_row" ] || [ "${between:-0}" -le 200 ]; then
    fail "$name" "not discriminating: ${between:-0} rows between the request and the review"; return
  fi
  pass "$name"
}

# The input rail must judge what the person typed. The runtime appends its
# environment block as a meta user message after the turn's own message, so a
# rail that picks the last user row scans the block: a blocked phrase in what
# the person typed reaches the provider anyway, and a clean message is judged
# by the block. The store rows the failed turn persisted are the discriminator:
# the environment block follows the blocked message, so the pre-fix rail would
# have scanned the block.
fake_input_rail_scans_the_persons_message() {
  local name=input-rail-scans-the-persons-message
  want "$name" || return 0
  scenario_start reply vcs || { fail "$name" "session did not start"; return; }
  # driver start wrote the config; the guardrail tunables are not part of it, so
  # the TUI is restarted by hand over the same home, project and provider.
  driver stop >/dev/null 2>&1 || true
  cat > "$HOME_DIR/forebrain.yaml" <<YAML
approval_policy: on-request
agents:
  defaults:
    guardrails:
      input:
        block_substrings:
        - zzz-blocked-needle
  definitions:
    main:
      primary: true
      enable_subagent: false
      llm_providers:
      - provider: deepseek
        model: deepseek-v4-flash
        api_key: \${FAKE_LLM_KEY}
        base_url: http://127.0.0.1:$PORT
YAML
  python3 "$REPO/scripts/acceptance/fake_provider.py" reply "$PORT" FAKE_ANSWER 0.6 \
    > "$ACCEPT_HOME/provider.log" 2>&1 &
  echo $! > "$ACCEPT_HOME/provider.pid"
  local i
  for ((i = 0; i < 50; i++)); do
    grep -q LISTENING "$ACCEPT_HOME/provider.log" 2>/dev/null && break
    sleep 0.1
  done
  grep -q LISTENING "$ACCEPT_HOME/provider.log" || { fail "$name" "the fake provider never bound its port"; return; }
  tmux new-session -d -s "$SESSION" -x 140 -y 45 -c "$PROJ" \
    "FAKE_LLM_KEY=sk-local-fake FAKE_TOOL_SCRIPT=$EMPTY_SCRIPT FAKE_DUMP_DIR=$DUMP FOREBRAIN_HOME=$HOME_DIR $BIN 2>$ACCEPT_HOME/tui.err; echo EXIT=\$?; exec bash"
  accept_trust || { fail "$name" "the TUI never left the trust dialog"; return; }
  wait_text "/ commands" 30 || { fail "$name" "the composer never appeared"; return; }

  local before after status
  before=$(request_count); before=${before:-0}
  driver submit "please explain zzz-blocked-needle" >/dev/null 2>&1
  sleep 8
  after=$(request_count); after=${after:-0}
  if [ "$after" != "$before" ]; then
    fail "$name" "the rail let the blocked message reach the provider (${before} -> ${after})"; return
  fi
  status=$(db "SELECT status FROM fb_runs ORDER BY rowid DESC LIMIT 1;" | tr -d ' ')
  [ "$status" = failed ] || { fail "$name" "latest run status = ${status:-none}, want failed"; return; }
  screen | grep -q 'input guard "input-rail" failed' \
    || { fail "$name" "the turn did not fail on the input rail"; return; }

  # The discriminator: the runtime did append its environment block after the
  # blocked message (the failed turn persisted both rows), so a rail that read
  # the last user row scanned that block instead of the person's text.
  local sid blocked_row env_row
  sid=$(db "SELECT session_id FROM fb_messages WHERE role='user' AND content LIKE '%zzz-blocked-needle%' ORDER BY id DESC LIMIT 1;" | tr -d ' ')
  blocked_row=$(db "SELECT id FROM fb_messages WHERE session_id='${sid:-none}' AND role='user' AND content LIKE '%zzz-blocked-needle%' ORDER BY id DESC LIMIT 1;" | tr -d ' ')
  env_row=$(db "SELECT id FROM fb_messages WHERE session_id='${sid:-none}' AND role='user' AND content LIKE '<forebrain_environment_context>%' ORDER BY id DESC LIMIT 1;" | tr -d ' ')
  if [ -z "$blocked_row" ] || [ -z "$env_row" ] || [ "${env_row:-0}" -le "${blocked_row:-0}" ]; then
    fail "$name" "not discriminating: the environment block did not follow the blocked message"; return
  fi
  pass "$name"
}

# --------------------------------------------------- scenario 4a: office loop --
fake_office_plan_review_execute() {
  local name=office-plan-review-execute
  want "$name" || return 0
  cat > "$LOGS/scripts-office.json" <<'JSON'
[["exit_plan_mode", "{}"], ["exit_plan_mode", "{}"], ["", "{}"], ["exit_plan_mode", "{}"], ["", "{}"], ["", "{}"]]
JSON
  SCENARIO_SCRIPT="$LOGS/scripts-office.json"
  scenario_start toolcall docs --plan --type office || { fail "$name" "session did not start"; return; }
  driver submit "Draft the Q3 board pack plan." >/dev/null 2>&1
  exit_card 90 || { fail "$name" "the first turn never reached its card"; return; }
  check_system_prompt "$name" || return
  check_single_plan_wiring "$name" || return
  local sys; sys="$(q sys 1)"
  case "$sys" in
    *"# Scope discipline"*) fail "$name" "an office conversation carries the coding scope discipline"; return ;;
  esac
  case "$sys" in
    *"You cannot ask anyone anything."*) fail "$name" "office inherited the batch no-questions rule"; return ;;
  esac

  [ -n "$(screen | grep 'No plan found')" ] || { fail "$name" "the empty-plan card did not appear"; return; }
  driver key Escape >/dev/null 2>&1; sleep 2

  local dir; dir=$(plan_dir_on_disk 1)
  [ -n "$dir" ] || { fail "$name" "the plan reminder named no plan directory"; return; }
  cat > "$LOGS/plan-office.md" <<'MD'
# Q3 board pack

## Goal
One three-page pack for the board: spend, variance, decisions.

## Deliverables
- `q3-board-pack.md`

## Sources
- `sources/spend.csv` (rows 2-3)
- `sources/notes.md` (section notes)

## Steps
1. Summarise spend by unit for ops and sales.
2. Explain the variance from the notes.

## Checks before hand-over
- [ ] every figure traces to a spend.csv cell

## Implementation record
MD
  seed_plan "$dir" q3-board-pack-plan.md "$LOGS/plan-office.md"

  driver submit "Submit the plan for approval." >/dev/null 2>&1
  exit_card 90 || { fail "$name" "the second turn never reached its card"; return; }
  screen | grep -q "Deliverables" || { fail "$name" "the office card did not show the plan"; return; }
  driver key m >/dev/null 2>&1
  wait_text "Which model should review this plan" 20 || { fail "$name" "the reviewer picker never opened"; return; }
  driver key Enter >/dev/null 2>&1
  wait_text "plan-reviewer task" 60 || { fail "$name" "no reviewer run was recorded"; return; }
  sleep 2
  [ "$(q all "file/page/section for documents, sheet/cell for data")" = yes ] \
    || { fail "$name" "the review task does not ask for document coordinates"; return; }
  [ "$(q all "ONE user_interaction call")" = yes ] \
    || { fail "$name" "the review hand-back does not send open questions to the user"; return; }

  # The review closes its card and the planner resubmits one; office option 1 is
  # "Yes, carry out the plan". The contract comes back as the exit_plan_mode
  # tool result, so wait for the request that carries it.
  exit_card 90 || { fail "$name" "the office plan was never resubmitted after the review"; return; }
  local before_req; before_req=$(q count)
  driver key 1 >/dev/null 2>&1
  sleep 0.3
  driver key Enter >/dev/null 2>&1
  local j
  for ((j = 0; j < 60; j++)); do
    [ "$(q count)" -gt "$before_req" ] && break
    sleep 0.25
  done
  sleep 1
  TURN_COUNT=$(screen_count "Worked for")
  n=$(q count)
  [ "$(q has "$n" "$EXECUTOR_CONTRACT")" = yes ] \
    || { fail "$name" "the office approval did not hand back the shared execution contract"; return; }
  pass "$name"
}

# ------------------------------------------------- scenario 4b: keep the plan --
fake_office_plan_only() {
  local name=office-plan-only
  want "$name" || return 0
  cat > "$LOGS/scripts-planonly.json" <<'JSON'
[["exit_plan_mode", "{}"], ["exit_plan_mode", "{}"]]
JSON
  SCENARIO_SCRIPT="$LOGS/scripts-planonly.json"
  scenario_start toolcall docs --plan --type office || { fail "$name" "session did not start"; return; }
  driver submit "Plan the prospectus checklist." >/dev/null 2>&1
  exit_card 90 || { fail "$name" "the first turn never reached its card"; return; }
  driver key Escape >/dev/null 2>&1; sleep 2
  local dir; dir=$(plan_dir_on_disk 1)
  [ -n "$dir" ] || { fail "$name" "the plan reminder named no plan directory"; return; }
  printf '# Prospectus checklist\n\n## Deliverables\n- `checklist.md`\n' > "$LOGS/plan-keep.md"
  seed_plan "$dir" prospectus-checklist.md "$LOGS/plan-keep.md"
  driver submit "Submit the plan." >/dev/null 2>&1
  exit_card 90 || { fail "$name" "the second turn never reached its card"; return; }
  screen | grep -q "Keep the plan only" || { fail "$name" "the keep-plan option is gone"; return; }
  driver key 3 >/dev/null 2>&1
  sleep 0.3
  driver key Enter >/dev/null 2>&1
  wait_grew "Worked for" "$TURN_COUNT" 60 || true
  TURN_COUNT=$(screen_count "Worked for"); sleep 2
  shot "$name"
  local n; n=$(q count)
  if [ "$(q has "$n" "$EXECUTOR_CONTRACT")" = yes ]; then
    fail "$name" "a kept plan must not hand back an execution contract"; return
  fi
  if [ "$(q has "$n" "Plan follow-through required")" = yes ]; then
    fail "$name" "a kept plan must not be reminded to implement"; return
  fi
  # "Keep the plan only" is recorded in the session's mode JSON
  # (workspace/state/modes/<session>.json, pkg/state/mode.go), not in fb_runs:
  # the session left plan mode with the plan as the deliverable, and nothing
  # may nudge it to implement. The key is omitempty, so it is present only
  # when the decision was actually made; the file is MarshalIndent-ed, so the
  # value sits after ": ".
  local modes; modes="$(driver home)/workspace/state/modes/$(driver sid).json"
  grep -Eq '"plan_kept_only": *true' "$modes" 2>/dev/null \
    || { fail "$name" "the kept-plan decision was not recorded in $modes"; return; }
  pass "$name"
}

# ------------------------------------------------ scenario 2: advisor matrix --
# One generic driver: start the fixture, ask for the variant, and check that the
# request the model received carries that variant's rules and that the audit
# changed nothing on disk. The variant's own behaviour is the real half's job.
fake_advisor() { # fake_advisor <name> <fixture> <ask> <system-fragment>...
  local name="$1" fixture="$2" ask="$3"; shift 3
  want "$name" || return 0
  scenario_start toolcall "$fixture" || { fail "$name" "session did not start"; return; }
  local before; before=$(fixture_hash)
  turn "$ask" 60 || { fail "$name" "the turn never closed"; return; }
  check_system_prompt "$name" || return
  local frag n; n=$(q count)
  for frag in "$@"; do
    [ "$(q has "$n" "$frag")" = yes ] || { fail "$name" "the request is missing its rule: $frag"; return; }
  done
  [ "$(q has "$n" "$ask")" = yes ] || { fail "$name" "the ask never reached the model"; return; }
  [ "$before" = "$(fixture_hash)" ] || { fail "$name" "the audit changed the project"; return; }
  pass "$name"
  scenario_stop
}

fake_advisor_all() {
  fake_advisor advisor-default vcs "Audit this project and tell me what to fix." \
    "Default request: run recon, audit, vetting, prioritization and planning." \
    "standard (the default): use hotspot-weighted coverage"
  fake_advisor advisor-standard vcs "Run a standard depth audit of this project." \
    "standard (the default): use hotspot-weighted coverage"
  fake_advisor advisor-quick-focus vcs "Quick audit, security category only." \
    "quick: cover only recon hotspots" "A category focus ("
  fake_advisor advisor-deep vcs "Run a deep audit of this project." \
    "deep: cover the whole requested scope" "explicitly marked investigate"
  fake_advisor advisor-next vcs "What should we build next?" \
    "next, features or roadmap: audit direction only" "Return 4–6 grounded options"
  fake_advisor advisor-features vcs "Which features are worth adding?" \
    "next, features or roadmap: audit direction only"
  fake_advisor advisor-roadmap vcs "Give me a product roadmap for this project." \
    "next, features or roadmap: audit direction only"
  fake_advisor advisor-reconcile issues "Reconcile the plan backlog in plans/." \
    "reconcile: process the existing plan backlog" "read-only inputs/ directory"

  # branch mode, in the four states it has to tell apart
  fake_advisor advisor-branch vcs "Audit the changes on this branch against main." \
    "merge-base" "introduced or pre-existing"
  if want advisor-branch && [ "$(git -C "$PROJ" rev-parse --abbrev-ref HEAD 2>/dev/null)" != "feature/amount-scale" ]; then
    fail advisor-branch "the fixture is not on the feature branch the scenario needs"
  fi
  if want advisor-branch && [ "$(git -C "$PROJ" rev-list --count main..HEAD 2>/dev/null)" != "2" ]; then
    fail advisor-branch "the fixture is not two commits ahead of main"
  fi

  fake_advisor advisor-branch-default vcs-default "Audit the changes on this branch." \
    "On the default branch or with zero commits ahead"
  if want advisor-branch-default && [ "$(git -C "$PROJ" rev-parse --abbrev-ref HEAD 2>/dev/null)" != "main" ]; then
    fail advisor-branch-default "the fixture is not on the default branch"
  fi

  fake_advisor advisor-branch-zero-ahead vcs-zero "Audit the changes on this branch." \
    "On the default branch or with zero commits ahead"
  if want advisor-branch-zero-ahead && [ "$(git -C "$PROJ" rev-list --count main..HEAD 2>/dev/null)" != "0" ]; then
    fail advisor-branch-zero-ahead "the fixture is not zero commits ahead"
  fi

  if want advisor-branch-non-git; then
    scenario_start toolcall nongit --no-git || { fail advisor-branch-non-git "session did not start"; return; }
    turn "Audit the changes on this branch." 60 || { fail advisor-branch-non-git "the turn never closed"; return; }
    check_system_prompt advisor-branch-non-git || return
    if [ "$(q has "$(q count)" "Where no version-control history exists")" != yes ]; then
      fail advisor-branch-non-git "the non-git fallback is missing"; return
    fi
    if [ -d "$PROJ/.git" ]; then fail advisor-branch-non-git "the fixture unexpectedly has history"; return; fi
    pass advisor-branch-non-git
    scenario_stop
  fi
}

# ------------------------------------------- scenario 6: audit inside plan mode --
fake_plan_mode_audit() {
  local name=plan-mode-audit
  want "$name" || return 0
  scenario_start toolcall vcs --plan || { fail "$name" "session did not start"; return; }
  local before; before=$(fixture_hash)
  turn "Audit this project and give me a plan." 60 || { fail "$name" "the turn never closed"; return; }
  check_system_prompt "$name" || return
  check_single_plan_wiring "$name" || return
  local n; n=$(q count)
  # The scripted model makes no tool call at all here, so the assertion that
  # matters is that nothing was attempted outside the session plan file.
  [ -z "$(q toolnames "$n")" ] || { fail "$name" "plan mode drove tool calls: $(q toolnames "$n" | tr '\n' ' ')"; return; }
  if q toolnames "$n" | grep -qx "write_file"; then fail "$name" "a write_file call was made in plan mode"; return; fi
  [ ! -d "$PROJ/plans" ] || { fail "$name" "plan mode created a project plans/ directory"; return; }
  [ "$before" = "$(fixture_hash)" ] || { fail "$name" "plan mode wrote into the project"; return; }
  # The reminder must name the session plan directory, not the project backlog.
  q has "$n" "$HOME_DIR/workspace/plans/" | grep -q yes \
    || { fail "$name" "the reminder does not name the session plan directory"; return; }
  pass "$name"
  scenario_stop
}

# ----------------------------------hand-made fixtures for the remaining shapes --
# The trivial request, the issue publications and the batch workspace need the
# plan backlog or a one-line typo in place before the session starts, so they
# seed the project from the harness instead of relying on a canned fixture.
seed_project() { # seed_project <fixture>
  cp -R "$FIXTURES/$1/." "$PROJ/" 2>/dev/null || true
}

# ------------------------------------- scenario 7: a typo must not become a plan --
fake_trivial_request() {
  local name=trivial-request-no-plan
  want "$name" || return 0
  scenario_start toolcall trivial || { fail "$name" "session did not start"; return; }
  turn "Fix the typo in README.md: 'Intput' should be 'Input'." 45 || { fail "$name" "the turn never closed"; return; }
  check_system_prompt "$name" || return
  no_plan_machinery "$name" || return
  if screen | grep -qi "plan mode"; then
    fail "$name" "a one-line request switched the conversation into plan mode"; return
  fi
  pass "$name"
  scenario_stop
}

# ------------------------------------------ scenario 3: publishing as issues --
# Every call goes to the stub in $FAKE_BIN, which opens no socket: "no issue was
# created" is a line count on disk. The fake half checks the stub and the
# fixture state; whether a model respects an auth failure is the real half.
# The stub records argv as a JSON array, so a two-word subcommand appears as
# "issue", "create" — matching the flat spelling would count nothing.
gh_log_count() { # gh_log_count <argv-fragment>
  [ -f "$GH_LOG" ] || { echo 0; return; }
  grep -cF -- "$1" "$GH_LOG" 2>/dev/null || true
}
gh_issue_creates() { gh_log_count '"issue", "create"'; }
issues_case() { # issues_case <name> <auth> <remote> <visibility> <ask> <kind> <expect-create>
  local name="$1" auth="$2" remote="$3" vis="$4" ask="$5" kind="$6" expect="$7"
  want "$name" || return 0
  local cli; cli=$(make_gh_stub "$name" "$auth" "$remote" "$vis")
  GH_LOG="$LOGS/gh-$name.jsonl"
  issues_script "$kind" "$cli" "$LOGS/scripts-$name.json"
  python3 -c "import json,sys; json.load(open(sys.argv[1]))" "$LOGS/scripts-$name.json" 2>/dev/null \
    || { fail "$name" "the scenario's tool script is not valid JSON"; return; }
  SCENARIO_SCRIPT="$LOGS/scripts-$name.json"
  scenario_start toolcall issues || { fail "$name" "session did not start"; return; }
  turn "$ask The issue CLI is $cli - run it directly, it stands in for gh." 60 || { fail "$name" "the turn never closed"; return; }
  check_system_prompt "$name" || return
  local creates; creates=$(gh_issue_creates)
  if [ "$expect" = zero ] && [ "$creates" != "0" ]; then
    fail "$name" "an issue was created where the preflight said not to"; return
  fi
  if [ "$expect" = some ] && [ "$creates" = "0" ]; then
    fail "$name" "no issue was created on the happy path"; return
  fi
  # The stub proves the preflight ran before any publication.
  if [ "$expect" = some ]; then
    [ "$(gh_log_count '"auth", "status"')" -ge 1 ] || { fail "$name" "the authentication preflight never ran"; return; }
    [ "$(gh_log_count '"repo", "view"')" -ge 1 ] || { fail "$name" "the remote/visibility preflight never ran"; return; }
  fi
  pass "$name"
  scenario_stop
}

fake_issues_all() {
  # The beats are generated rather than assembled from shell strings: a stray
  # quote in a hand-built JSON is a provider that never starts, which reads like
  # a product failure. Three shapes cover every case: one preflight command, one
  # publication, and no tool at all.
  issues_script() { # issues_script <kind: auth|view|create|none> <cli> <out>
    python3 - "$1" "$2" "$3" <<'PYJSON'
import json, sys
kind, cli, out = sys.argv[1], sys.argv[2], sys.argv[3]
if kind == "none":
    beats = []
elif kind == "auth":
    beats = [["shell", {"command": cli + " auth status"}]] * 4
elif kind == "view":
    beats = [["shell", {"command": cli + " repo view --json visibility"}]] * 4
else:
    publish = (cli + " auth status && " + cli + " repo view --json visibility && "
               + cli + " issue create --title Split-the-importer --body acceptance")
    beats = [["shell", {"command": publish}]] * 4
open(out, "w", encoding="utf-8").write(json.dumps(beats))
PYJSON
  }

  issues_case issues-success ok ok private \
    "Publish our selected plans as issues with --issues." create some
  issues_case issues-auth-failure fail ok private \
    "Publish the selected plans with --issues." auth zero
  issues_case issues-remote-failure ok fail private \
    "Publish the selected plans with --issues." view zero
  # Public repository, sensitive plan: the fake half checks the state that makes
  # the confirmation necessary; the refusal itself is measured on the real half.
  issues_case issues-sensitive-denied ok ok public \
    "Publish the selected plans with --issues." auth zero
  if want issues-sensitive-denied && ! grep -q "Rotate the API secret" "$PROJ/plans/027-rotate-the-api-secret.md" 2>/dev/null; then
    fail issues-sensitive-denied "the sensitive plan is not in the backlog"
  fi
  issues_case issues-sensitive-confirmed ok ok public \
    "Publish the selected plans with --issues, I confirm the sensitive one." create some
  issues_case issues-unflagged-noop ok ok private \
    "Review the plans and tell me what to do next (no publication)." none zero
  # Nothing at all was asked of the CLI when no publication was requested.
  if [ -s "$LOGS/gh-issues-unflagged-noop.jsonl" ]; then
    fail issues-unflagged-noop "the issue CLI was called without an --issues request"
  fi
}

# --------------------------------------------- scenario 5: unattended batch --
fake_batch_all() {
  # A batch conversation refuses a chat turn that has no task execution behind
  # it: that refusal is the guard, and nothing may be written or published
  # before it fires. The batch system prompt itself is pinned where it can be
  # read deterministically - the runner test - because this conversation never
  # reaches a model at all.
  local name=batch-audit
  if want "$name"; then :; else return 0; fi
  SCENARIO_SCRIPT="$EMPTY_SCRIPT"
  scenario_start toolcall batch --type batch || { fail "$name" "session did not start"; return; }
  local before; before=$(fixture_hash)
  driver submit "Audit this task workspace and write the plans under outputs/plans/." >/dev/null 2>&1
  sleep 6
  shot "$name"
  if ! screen | grep -q "has no task execution"; then
    fail "$name" "an unattended batch turn ran without a task execution"; return
  fi
  [ "$before" = "$(fixture_hash)" ] || { fail "$name" "the unattended run wrote into the workspace"; return; }
  if [ -s "$LOGS/gh-batch.jsonl" ]; then
    fail batch-sensitive-skip "an unattended run called the issue CLI"; return
  fi
  ( cd "$REPO" && FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run \
      -run TestSystemPromptFollowsProjectType -count=1 ) > "$LOGS/batch-prompt-test.log" 2>&1 \
    || { fail "$name" "the batch system prompt is not the specified composition"; return; }
  pass "$name"
  pass batch-sensitive-skip
  scenario_stop
  # --- the reconcile move: read-only inputs/ to a complete outputs/plans/ ---
  # Reconcile is a workflow rule, not a project type: the same conversation
  # shape that carries it for coding carries it for a task workspace.
  local rname=batch-reconcile
  python3 - "$LOGS/scripts-reconcile.json" <<'PYJSON'
import json, sys
body = ("# Refreshed backlog\n\n| # | Plan | Status |\n|---|---|---|\n"
        "| 023 | Split the importer | DONE (verified) |\n"
        "| 024 | Cache the sheet parser | BLOCKED |\n"
        "| 025 | Retry the uploader | IN PROGRESS |\n"
        "| 026 | Document the column mapping | TODO |\n")
command = ("mkdir -p outputs/plans && cat > outputs/plans/index.md <<'MD'\n" + body + "MD\n")
beats = [["shell", {"command": command}]] * 4
open(sys.argv[1], "w", encoding="utf-8").write(json.dumps(beats))
PYJSON
  SCENARIO_SCRIPT="$LOGS/scripts-reconcile.json"
  scenario_start toolcall batch || { fail "$rname" "session did not start"; return; }
  local inputs_before; inputs_before=$(cd "$PROJ" && find inputs -type f | sort | xargs shasum | shasum)
  turn "Reconcile the plan backlog in inputs/ into outputs/plans/ and record what you could not verify." 60 \
    || { fail "$rname" "the turn never closed"; return; }
  [ "$inputs_before" = "$(cd "$PROJ" && find inputs -type f | sort | xargs shasum | shasum)" ] \
    || { fail "$rname" "the read-only inputs/ snapshot was modified"; return; }
  [ -f "$PROJ/outputs/plans/index.md" ] || { fail "$rname" "no refreshed index was written"; return; }
  grep -q "DONE (verified)" "$PROJ/outputs/plans/index.md" \
    || { fail "$rname" "the refreshed index does not carry the verified status"; return; }
  pass "$rname"
  scenario_stop
}

# ================================================================ real half ==
# The same fixtures driven by the owner's model. The key is resolved in the order
# the plan fixes and never leaves a 0600 file: nothing here logs it, prints it,
# or puts it on a command line.
CREDS="$ACCEPT_WORK/creds.env"
REAL_CALLS=0
REAL_LIMIT_SECONDS=${REAL_LIMIT_SECONDS:-$(( 90 * 60 ))}
REAL_LIMIT_CALLS=150

resolve_real_key() {
  # Resolution order, mirroring where this owner actually keeps the credential:
  # an exported FOREBRAIN_E2E_ZHIPU_KEY, then the ZHIPUAI_API_KEY that
  # ~/.forebrain/forebrain.yaml itself references (its .env beside the config),
  # then the older e2e env file. The key only ever reaches a 0600 file the
  # launched TUI sources; it is never printed, logged or put on a command line.
  local key="${FOREBRAIN_E2E_ZHIPU_KEY:-}"
  if [ -z "$key" ] && [ -f "$HOME/.forebrain/.env" ]; then
    key="$(set -a; . "$HOME/.forebrain/.env"; set +a; printf '%s' "${ZHIPUAI_API_KEY:-}")"
  fi
  if [ -z "$key" ] && [ -f "$HOME/.forebrain/e2e-zhipu.env" ]; then
    key="$(set -a; . "$HOME/.forebrain/e2e-zhipu.env"; set +a; printf '%s' "${FOREBRAIN_E2E_ZHIPU_KEY:-}")"
  fi
  [ -n "$key" ] || { echo "credentials missing: set FOREBRAIN_E2E_ZHIPU_KEY" >&2; return 1; }
  ( umask 077; printf 'FOREBRAIN_E2E_ZHIPU_KEY=%s\n' "$key" > "$CREDS" )
  chmod 600 "$CREDS"
  unset key
}

real_yaml() {
  # approval_policy: never is what makes the run unattended; a host with no OS
  # sandbox has to say so explicitly, or the app refuses to start.
  cat > "$HOME_DIR/forebrain.yaml" <<YAML
approval_policy: never
sandbox_mode: danger-full-access
agents:
  defaults:
    enable_subagent: true
  definitions:
    main:
      primary: true
      llm_providers:
      - provider: $REAL_PROVIDER
        model: $REAL_MODEL
        api_key: \${FOREBRAIN_E2E_ZHIPU_KEY}
        base_url: $REAL_BASE_URL
        params:
          reasoning:
            effort: $REAL_REASONING_EFFORT
YAML
}

real_start() { # real_start <fixture> [--plan] [--type X] [--no-git]
  local fixture="$1"; shift
  local want_plan=no want_type="" want_git=yes
  while [ $# -gt 0 ]; do
    case "$1" in
      --plan) want_plan=yes ;;
      --type) want_type="$2"; shift ;;
      --no-git) want_git=no ;;
    esac
    shift
  done
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  rm -rf "$HOME_DIR" "$PROJ"; mkdir -p "$HOME_DIR" "$PROJ"
  [ -n "$fixture" ] && cp -R "$FIXTURES/$fixture/." "$PROJ/" 2>/dev/null
  [ "$want_git" = no ] && rm -rf "$PROJ/.git"
  real_yaml
  tmux new-session -d -s "$SESSION" -x 150 -y 48 -c "$PROJ" \
    "set -a; . '$CREDS'; set +a; PATH='$FAKE_BIN:\$PATH' FOREBRAIN_HOME='$HOME_DIR' '$BIN' 2>'$LOGS/tui-real.err'; echo EXIT=\$?; exec bash"
  local i answered=no
  for ((i = 0; i < 60; i++)); do
    if screen | grep -q "Trust this folder"; then
      if [ "$answered" = no ]; then
        tmux send-keys -t "$SESSION" Down; sleep 0.2
        tmux send-keys -t "$SESSION" Enter; answered=yes
      fi
      sleep 0.3
      continue
    fi
    [ "$answered" = yes ] && break
    [ "$i" -ge 15 ] && break
    sleep 0.2
  done
  if screen | grep -q "Trust this folder"; then
    echo "trust dialog still on screen" >&2
    return 1
  fi
  wait_text "/ commands" 40 || return 1
  if [ -n "$want_type" ]; then db "UPDATE fb_projects SET project_type='$want_type';" || return 1; fi
  TURN_COUNT=$(screen_count "Worked for")
  if [ "$want_plan" = yes ]; then driver submit "/plan" >/dev/null 2>&1; sleep 1; fi
  return 0
}

real_calls_here() { db "SELECT count(*) FROM fb_llm_attempts;" 2>/dev/null | tr -d ' '; }

# One real turn: submit, then keep answering whatever the run asks until the
# footer's elapsed line grows. A tool approval that is not the plan decision is
# allowed on the first row; the plan decision is left to the caller.
real_turn() { # real_turn <text> [seconds] [--leave-cards]
  local text="$1" secs="${2:-150}" leave="${3:-}"
  local before="$TURN_COUNT" i cards
  driver submit "$text" >/dev/null 2>&1
  for ((i = 0; i < secs * 4; i++)); do
    [ "$(screen_count "Worked for")" -gt "$before" ] && { TURN_COUNT=$(screen_count "Worked for"); return 0; }
    if [ -z "$leave" ]; then
      if screen | grep -q "Tool approval"; then
        if screen | grep -q "Would you like to proceed?"; then
          : # the plan decision: the caller presses m / 1 / 2 / 3 itself
        else
          cards=$(screen_count "Tool approval")
          tmux send-keys -t "$SESSION" Enter >/dev/null 2>&1
        fi
      fi
      if screen | grep -q "Which model should review"; then
        tmux send-keys -t "$SESSION" Enter >/dev/null 2>&1
      fi
    fi
    sleep 0.25
  done
  return 1
}

real_assert_reply() { # real_assert_reply <name> <needle>...
  local name="$1"; shift
  local body; body=$(screen)
  local needle
  for needle in "$@"; do
    case "$body" in
      *"$needle"*) ;;
      *) fail "$name" "the reply does not show: $needle"; return 1 ;;
    esac
  done
  return 0
}

real_advisor() { # real_advisor <name> <fixture> <ask> <needle>... | --observe
  local name="$1" fixture="$2" ask="$3"; shift 3
  want "$name" || return 0
  ask="$ask Answer in at most 120 words and do not write any file."
  local observe=no
  [ "${1:-}" = "--observe" ] && { observe=yes; shift; }
  real_start "$fixture" || { fail "$name" "session did not start"; return; }
  local before_calls; before_calls=$(real_calls_here)
  if ! real_turn "$ask" 420; then fail "$name" "the turn never closed"; shot "$name"; return; fi
  REAL_CALLS=$(( REAL_CALLS + $(real_calls_here) - before_calls ))
  screen > "$EVIDENCE/$name.reply.txt"
  local needle body; body=$(screen)
  if [ "$observe" = yes ]; then
    observed "$name" "$(printf '%s' "$body" | tr '\n' ' ' | cut -c1-400)"
    return
  fi
  for needle in "$@"; do
    case "$body" in
      *"$needle"*) ;;
      *) fail "$name" "the reply does not show: $needle"; return ;;
    esac
  done
  pass "$name"
}


# Approve an option by digit. A digit only moves the card's cursor and Enter
# commits it, and a key pressed between two cards lands in the composer instead
# - so press, then prove the card is gone, and press again if it is not.
approve() { # approve <digit> [seconds]
  local digit="$1" secs="${2:-60}" i
  for ((i = 0; i < secs * 2; i++)); do
    card_on_screen || return 0
    driver key "$digit" >/dev/null 2>&1
    sleep 0.2
    driver key Enter >/dev/null 2>&1
    sleep 0.7
    card_on_screen || return 0
    sleep 0.5
  done
  return 1
}

real_wait_card() { # real_wait_card <seconds> -> the plan decision is on screen now
  local secs="${1:-420}" i
  for ((i = 0; i < secs * 4; i++)); do
    if card_on_screen; then return 0; fi
    sleep 0.25
  done
  return 1
}
real_wait_turn() { # real_wait_turn <seconds>
  local before="$TURN_COUNT" secs="${1:-420}" i
  for ((i = 0; i < secs * 4; i++)); do
    [ "$(screen_count "Worked for")" -gt "$before" ] && { TURN_COUNT=$(screen_count "Worked for"); return 0; }
    sleep 0.25
  done
  return 1
}
real_plan_file() { find "$HOME_DIR/workspace/plans" -name '*.md' 2>/dev/null | head -1; }


MATRIX_STARTED=no
real_matrix_once() { # real_matrix_once <name> <ask> [assert <needle>|observe]
  local name="$1" ask="$2" mode="${3:-assert}" needle="${4:-}"
  want "$name" || return 0
  if [ "$MATRIX_STARTED" = no ]; then
    real_start issues || { fail "$name" "session did not start"; return; }
    MATRIX_STARTED=yes
  fi
  local before_calls; before_calls=$(real_calls_here)
  if ! real_turn "$ask Answer in at most 120 words and do not write any file." 420; then
    fail "$name" "the turn never closed"; shot "$name"; return
  fi
  REAL_CALLS=$(( REAL_CALLS + $(real_calls_here) - before_calls ))
  screen > "$EVIDENCE/$name.reply.txt"
  if [ "$mode" = observe ]; then
    observed "$name" "$(screen | tr '\n' ' ' | cut -c1-400)"
    return
  fi
  if [ -n "$needle" ]; then
    case "$(screen)" in
      *"$needle"*) ;;
      *) fail "$name" "the reply does not show: $needle"; return ;;
    esac
  fi
  pass "$name"
}

real_coding_loop() {
  local name=coding-plan-review-execute
  want "$name" || return 0
  real_start vcs --plan || { fail "$name" "session did not start"; return; }
  local calls0; calls0=$(real_calls_here)
  driver submit "Add a scale step to src/reader.py: apply scale() inside read_row so callers stop multiplying by hand. Do not ask questions - write the plan now with your recommended answers. The plan must have Context, Files to change, ordered Steps, Verification with expected results, Done criteria, STOP conditions and Maintenance notes." >/dev/null 2>&1
  if ! real_wait_card 420; then
    fail "$name" "the plan never reached an approval card"
    shot "$name-missed"; screen > "$EVIDENCE/$name-missed-full.txt"
    REAL_CALLS=$(( REAL_CALLS + $(real_calls_here) - calls0 )); return
  fi
  shot "$name-card"
  local plan; plan=$(real_plan_file)
  [ -n "$plan" ] || { fail "$name" "no plan file was written"; REAL_CALLS=$(( REAL_CALLS + $(real_calls_here) - calls0 )); return; }
  grep -qi "context" "$plan" || { fail "$name" "the plan has no context section"; cp "$plan" "$EVIDENCE/$name.plan.md"; return; }
  grep -qiE "step|phase|files to change|files:|change" "$plan" \
    || { fail "$name" "the plan names no work to do"; cp "$plan" "$EVIDENCE/$name.plan.md"; return; }
  grep -qiE "verif|check" "$plan" || { fail "$name" "the plan has no verification step"; cp "$plan" "$EVIDENCE/$name.plan.md"; return; }
  grep -qi "done criteria\|acceptance" "$plan" || { fail "$name" "the plan has no done criteria"; cp "$plan" "$EVIDENCE/$name.plan.md"; return; }
  grep -qi "stop" "$plan" || { fail "$name" "the plan has no STOP conditions"; cp "$plan" "$EVIDENCE/$name.plan.md"; return; }
  cp "$plan" "$EVIDENCE/$name.plan.md"

  driver key m >/dev/null 2>&1
  wait_text "Which model should review this plan" 20 && driver key Enter >/dev/null 2>&1
  wait_text "Ran 1 plan-reviewer task" 420 || { fail "$name" "the independent review never finished"; shot "$name-review"; return; }
  sleep 2
  shot "$name-review"
  # What the plan asks the real half to establish is that the review was
  # dispatched and its result recorded - the verdict's wording is the model's
  # business. A recorded plan_reviewed event with a done outcome is that fact.
  local reviewed
  reviewed=$(db "SELECT count(*) FROM fb_session_events WHERE event_type='plan_reviewed';" 2>/dev/null | tr -d ' ')
  if [ "${reviewed:-0}" = "0" ]; then
    fail "$name" "no review result was recorded"; return
  fi
  if ! db "SELECT payload_json FROM fb_session_events WHERE event_type='plan_reviewed' LIMIT 1;" 2>/dev/null | grep -qi "verdict\|approve\|rework"; then
    fail "$name" "the recorded review carries no verdict wording"; return
  fi

  approve 2 120 || { fail "$name" "the plan was never approved"; return; }
  real_wait_turn 420 || true
  sleep 2
  shot "$name-done"
  REAL_CALLS=$(( REAL_CALLS + $(real_calls_here) - calls0 ))
  # The executor writes its record at the end of its own turn: give that turn
  # its own budget instead of reading the plan the instant the approval lands.
  local waited=0 planrec
  while [ "$waited" -lt 420 ]; do
    planrec=$(real_plan_file)
    [ -n "$planrec" ] && grep -qi "implementation record" "$planrec" && break
    sleep 5
    waited=$(( waited + 5 ))
  done
  if ! grep -qi "implementation record" "$(real_plan_file)"; then
    fail "$name" "the executor never recorded the implementation in the plan"; return
  fi
  if ! screen | grep -qi "STATUS:"; then
    fail "$name" "the final report did not use the required structure"; return
  fi
  pass "$name"
}

real_office_loop() {
  local name=office-plan-review-execute
  want "$name" || return 0
  real_start docs --plan --type office || { fail "$name" "session did not start"; return; }
  local calls0; calls0=$(real_calls_here)
  driver submit "Plan the Q3 board pack: one three-page document from sources/spend.csv and sources/notes.md. Do not ask questions - write the plan now with your recommended answers. The plan must have Goal, Deliverables, Sources, Outline, Steps, Checks before hand-over and open questions." >/dev/null 2>&1
  if ! real_wait_card 420; then fail "$name" "the office plan never reached a card"; shot "$name"; REAL_CALLS=$(( REAL_CALLS + $(real_calls_here) - calls0 )); return; fi
  local plan; plan=$(real_plan_file)
  [ -n "$plan" ] || { fail "$name" "no office plan file was written"; return; }
  cp "$plan" "$EVIDENCE/$name.plan.md"
  grep -qi "spend" "$plan" || { fail "$name" "the office plan names none of the real sources"; return; }
  driver key m >/dev/null 2>&1
  wait_text "Which model should review this plan" 20 && driver key Enter >/dev/null 2>&1
  wait_text "Ran 1 plan-reviewer task" 420 || { fail "$name" "the office review never finished"; return; }
  sleep 2
  driver key 1 >/dev/null 2>&1
  sleep 0.3
  driver key Enter >/dev/null 2>&1
  real_wait_turn 420 || true
  sleep 2
  shot "$name-done"
  REAL_CALLS=$(( REAL_CALLS + $(real_calls_here) - calls0 ))
  pass "$name"
}

real_office_plan_only() {
  local name=office-plan-only
  want "$name" || return 0
  real_start docs --plan --type office || { fail "$name" "session did not start"; return; }
  local calls0; calls0=$(real_calls_here)
  driver submit "Plan a one-page prospectus checklist. Do not ask questions - write the plan now." >/dev/null 2>&1
  if ! real_wait_card 420; then fail "$name" "the plan never reached a card"; REAL_CALLS=$(( REAL_CALLS + $(real_calls_here) - calls0 )); return; fi
  approve 3 120 || { fail "$name" "the keep-plan option was never chosen"; return; }
  real_wait_turn 420 || true
  sleep 2
  shot "$name"
  REAL_CALLS=$(( REAL_CALLS + $(real_calls_here) - calls0 ))
  local plan; plan=$(real_plan_file)
  if [ -n "$plan" ] && grep -qi "implementation record" "$plan"; then
    fail "$name" "a kept plan was implemented anyway"; return
  fi
  if [ -n "$(find "$PROJ" -name 'checklist.md' -o -name '*.docx' 2>/dev/null | head -1)" ]; then
    fail "$name" "a kept plan produced a deliverable"; return
  fi
  pass "$name"
}

real_plan_mode_audit() {
  local name=plan-mode-audit
  want "$name" || return 0
  real_start vcs --plan || { fail "$name" "session did not start"; return; }
  local calls0; calls0=$(real_calls_here)
  if ! real_turn "Audit this project and give me a plan for the most valuable fix. Do not ask questions." 420; then
    fail "$name" "the turn never closed"; REAL_CALLS=$(( REAL_CALLS + $(real_calls_here) - calls0 )); return
  fi
  REAL_CALLS=$(( REAL_CALLS + $(real_calls_here) - calls0 ))
  screen > "$EVIDENCE/$name.reply.txt"
  [ -n "$(real_plan_file)" ] || { fail "$name" "the plan did not land in the session plan file"; return; }
  [ ! -d "$PROJ/plans" ] || { fail "$name" "the plan was written into the project's plans/ directory"; return; }
  pass "$name"
}

real_trivial() {
  local name=trivial-request-no-plan
  want "$name" || return 0
  real_start trivial || { fail "$name" "session did not start"; return; }
  local calls0; calls0=$(real_calls_here)
  if ! real_turn "Fix the typo in README.md: 'Intput' should be 'Input'." 420; then
    fail "$name" "the turn never closed"; REAL_CALLS=$(( REAL_CALLS + $(real_calls_here) - calls0 )); return
  fi
  REAL_CALLS=$(( REAL_CALLS + $(real_calls_here) - calls0 ))
  screen > "$EVIDENCE/$name.reply.txt"
  grep -q "## Input" "$PROJ/README.md" || { fail "$name" "the one-line fix never landed"; return; }
  no_plan_machinery "$name" || return
  pass "$name"
}

real_issues() { # real_issues <name> <auth> <remote> <visibility> <ask> <expect-create>
  local name="$1" auth="$2" remote="$3" vis="$4" ask="$5" expect="$6"
  want "$name" || return 0
  local cli; cli=$(make_gh_stub "$name" "$auth" "$remote" "$vis")
  GH_LOG="$LOGS/gh-$name.jsonl"
  real_start issues || { fail "$name" "session did not start"; return; }
  local calls0; calls0=$(real_calls_here)
  if ! real_turn "$ask The issue CLI is $cli - it stands in for gh auth status, gh repo view --json visibility and gh issue create, run it directly." 420; then
    fail "$name" "the turn never closed"; REAL_CALLS=$(( REAL_CALLS + $(real_calls_here) - calls0 )); return
  fi
  REAL_CALLS=$(( REAL_CALLS + $(real_calls_here) - calls0 ))
  screen > "$EVIDENCE/$name.reply.txt"
  local creates; creates=$(gh_issue_creates)
  case "$expect" in
    zero) [ "$creates" = "0" ] || { fail "$name" "an issue was published where it must not be"; return; } ;;
    some) [ "$creates" -ge 1 ] || { fail "$name" "no issue was published on the happy path"; return; } ;;
  esac
  pass "$name"
}

real_batch() {
  want batch-audit || return 0
  real_start batch --type batch || { fail batch-audit "session did not start"; return; }
  local calls0; calls0=$(real_calls_here)
  if ! real_turn "Audit this task workspace. Reconcile the plan backlog in inputs/ into outputs/plans/ and record what you could not verify. Do not ask anything." 420; then
    fail batch-audit "the turn never closed"; REAL_CALLS=$(( REAL_CALLS + $(real_calls_here) - calls0 )); return
  fi
  REAL_CALLS=$(( REAL_CALLS + $(real_calls_here) - calls0 ))
  screen > "$EVIDENCE/batch-audit.reply.txt"
  local sys; sys=$(db "SELECT system_prompt FROM fb_runs WHERE coalesce(system_prompt,'') <> '' ORDER BY rowid DESC LIMIT 1;" 2>/dev/null)
  [ -n "$(find "$PROJ/outputs/plans" -name '*.md' 2>/dev/null | head -1)" ] \
    || { fail batch-audit "nothing was written under outputs/plans"; return; }
  pass batch-audit
  if [ -n "$(find "$PROJ/inputs" -newer "$PROJ/TASK.md" -name '*.md' 2>/dev/null | head -1)" ]; then
    fail batch-reconcile "the read-only inputs/ snapshot was modified"; return
  fi
  pass batch-reconcile
  if [ -s "$LOGS/gh-batch-real.jsonl" ]; then
    fail batch-sensitive-skip "the unattended run called the issue CLI"; return
  fi
  pass batch-sensitive-skip
}

# ------------------------------------------------------------- environment --
build_once() {
  driver build >/dev/null 2>&1 || { echo "build failed; see $LOGS" >&2; return 1; }
  [ -x "$BIN" ] || { echo "no binary at $BIN" >&2; return 1; }
  return 0
}

# The capability must hold with nothing installed on top of the binary. The
# binary ships its own bundled skills under home/skills/.system; anything else
# would be an environment leaking into the run.
skill_vacuum() {
  local stray
  stray=$(find "$ACCEPT_WORK" -name SKILL.md 2>/dev/null | head -5)
  if [ -n "$stray" ]; then echo "skills found in the acceptance workspace: $stray" >&2; return 1; fi
  stray=$(find "$ACCEPT_HOME" -name SKILL.md 2>/dev/null | grep -v "/skills/\.system/" | head -5)
  if [ -n "$stray" ]; then echo "skills outside the bundled set: $stray" >&2; return 1; fi
  note "bundled skills: $(find "$ACCEPT_HOME" -path '*/skills/.system/*' -name SKILL.md 2>/dev/null | wc -l | tr -d ' ')"
  return 0
}

web_half() {
  local out="$LOGS/web-e2e.log"
  local saved_dump="${FAKE_DUMP_DIR:-}"
  unset FAKE_DUMP_DIR
  FOREBRAIN_E2E_SPEC="e2e/exit-plan-approval.spec.ts e2e/office-plan-approval.spec.ts e2e/batch-tasks.spec.ts" \
  FOREBRAIN_E2E_WORK="$ACCEPT_WORK/web" \
  env -u FOREBRAIN_HOME bash "$REPO/scripts/acceptance/web_e2e.sh" > "$out" 2>&1
  local rc=$?
  cp -f "$out" "$EVIDENCE/web-e2e.log" 2>/dev/null || true
  [ -n "$saved_dump" ] && export FAKE_DUMP_DIR="$saved_dump"
  printf 'WEB-HALF exit=%d log=%s\n' "$rc" "$EVIDENCE/web-e2e.log"
  note "web half exit=$rc"
  return $rc
}

# -------------------------------------------------------------------- fake --
run_fake() {
  build_once || exit 2
  make_fixtures
  make_probe
  export FAKE_LLM_KEY=sk-local-fake
  skill_vacuum || { echo "the acceptance environment is not skill-vacuum" >&2; exit 2; }

  fake_coding_plan_review_execute
  fake_plan_review_long_investigation
  fake_input_rail_scans_the_persons_message
  fake_advisor_all
  fake_plan_mode_audit
  fake_trivial_request
  fake_issues_all
  fake_office_plan_review_execute
  fake_office_plan_only
  fake_batch_all
  scenario_stop
  web_half
}

# -------------------------------------------------------------------- real --
run_real() {
  build_once || exit 2
  make_fixtures
  make_probe
  resolve_real_key || exit 2
  chmod 600 "$CREDS"
  skill_vacuum || { echo "the acceptance environment is not skill-vacuum" >&2; exit 2; }
  echo "provider/model: $PROVIDER_MODEL"
  MATRIX_STARTED=no

  real_coding_loop
  # One session carries the whole audit matrix: the variants are read-only, the
  # fixture is the same, and a launch per ask costs more than the model call.
  real_matrix_once advisor-default "Audit this project and tell me the three highest-value fixes, naming the file and line for each." assert reader.py
  real_matrix_once advisor-quick-focus "Quick audit, security category only: the most dangerous gap, and what you did NOT audit." assert security
  real_matrix_once advisor-standard "Run a standard depth audit of this project." observe
  real_matrix_once advisor-deep "Run a deep audit of this project." observe
  real_matrix_once advisor-next "What should we build next on this project?" observe
  real_matrix_once advisor-features "Which features are worth adding?" observe
  real_matrix_once advisor-roadmap "Give me a roadmap for this project." observe
  real_advisor advisor-branch vcs "Audit the changes on this branch against main and tag each finding introduced or pre-existing."
  real_advisor advisor-branch-default vcs-default "Audit the changes on this branch against main."
  real_advisor advisor-branch-zero-ahead vcs-zero "Audit the changes on this branch against main."
  real_advisor advisor-branch-non-git nongit "Audit the changes on this branch against main."
  real_matrix_once advisor-reconcile "Reconcile the plan backlog in plans/ and tell me what is currently executable."
  real_plan_mode_audit
  real_trivial

  real_issues issues-success ok ok private \
    "Publish the selected plans as GitHub issues with --issues. Use the gh CLI. Confirm the titles once, then create them and record each URL in the plan and the index." some
  real_issues issues-auth-failure fail ok private \
    "Publish the selected plans with --issues. Use the gh CLI." zero
  real_issues issues-remote-failure ok fail private \
    "Publish the selected plans with --issues. Use the gh CLI." zero
  real_issues issues-sensitive-denied ok ok public \
    "Publish the selected plans with --issues. The repository is public; I do NOT confirm publishing the sensitive one." zero
  real_issues issues-sensitive-confirmed ok ok public \
    "Publish the selected plans with --issues. The repository is public and I explicitly confirm publishing every plan, including the sensitive one." some
  real_issues issues-unflagged-noop ok ok private \
    "Review the backlog in plans/ and tell me what to do next. Do not publish anything." zero

  real_office_loop
  real_office_plan_only
  export GH_LOG="$LOGS/gh-batch-real.jsonl"; rm -f "$GH_LOG"
  export GH_AUTH=ok GH_REMOTE=ok GH_VISIBILITY=private
  real_batch
  tmux kill-session -t "$SESSION" 2>/dev/null || true

  local elapsed=$(( $(date +%s) - START_TS ))
  printf 'real budget: %ds of %ds, %d model calls of %d\n' "$elapsed" "$REAL_LIMIT_SECONDS" "$REAL_CALLS" "$REAL_LIMIT_CALLS"
  note "real budget: ${elapsed}s ${REAL_CALLS} calls"
  if [ "$elapsed" -gt "$REAL_LIMIT_SECONDS" ] || [ "$REAL_CALLS" -gt "$REAL_LIMIT_CALLS" ]; then
    fail real-budget "elapsed ${elapsed}s / ${REAL_CALLS} calls"
  else
    pass real-budget
  fi
  web_half
}

# -------------------------------------------------------------------- main --
case "$MODE" in
  --fake) run_fake ;;
  --real) run_real ;;
esac

elapsed=$(( $(date +%s) - START_TS ))
printf '\n%d PASS, %d FAIL, %d OBSERVED in %ds\n' "$PASSED" "$FAILED" "$OBSERVED" "$elapsed"
note "summary: PASS=$PASSED FAIL=$FAILED OBSERVED=$OBSERVED elapsed=${elapsed}s"
[ "$FAILED" -eq 0 ] || exit 1
exit 0
