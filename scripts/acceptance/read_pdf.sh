#!/usr/bin/env bash
# Real-machine acceptance for plan 054: the model reads a PDF as page images,
# and Chinese text in it renders as real glyphs (not tofu boxes).
#
# Builds the real binary, starts the real TUI in a scratch FOREBRAIN_HOME
# against a REAL provider, and proves in one run, for BOTH fixtures. Each phase
# is SEQUENCED on the DB, not the pane: it first waits for this fixture's read
# row (an image-block tool row naming the fixture), and only then captures the
# pane — so a previous phase's marker still on screen can never satisfy it.
#   1. (TUI) the read_file step renders as an image-style step — the terminal's
#      own header ("● Read <path>") and its image marker line
#      ("└ ▤ …-p1.png · …×… · … · Ctrl+O 打开"). The "Read … · PDF n 页 ·
#      渲染 a-b 页" string is a surface SUMMARY (SummarizeToolStep) the TUI never
#      shows, so it is asserted by pkg/tool's unit test, not here.
#   2. (DB) the FINAL ANSWER reproduces the page's Chinese text VERBATIM
#      (中文渲染测试：甲乙丙丁戊己庚辛壬癸) — impossible if the pages rendered
#      as tofu, so this is the end-to-end proof that CJK rendering works;
#   3. (DB) the tool row stores a "type":"image" block — the model, not a prompt
#      hack, saw the rendered pixels — and its note names the PDF and its page
#      count. The canonical session event also retains the completed output, so
#      the page-range phase can assert the exact pdf_pages array independently.
#   4. the SAME holds for the not-embedded fixture (STSong-Light, no embedded
#      font), which is exactly the case that needs the fallback font, and for
#      the embedded one (the control).
#
# The provider key is read from FOREBRAIN_E2E_DEEPSEEK_KEY (already exported,
# never printed) and written to a 0600 file the launched TUI sources, so it is
# never on a command line. The home is named by this script and is NEVER taken
# from the ambient FOREBRAIN_HOME; pass FOREBRAIN_ACCEPT_HOME to keep it.
#
#   FOREBRAIN_E2E_DEEPSEEK_KEY=<key> scripts/acceptance/read_pdf.sh
#   FOREBRAIN_E2E_MODEL=deepseek-v4-flash   # override the model under test
#
# The rendered page PNGs are printed at the end so a human can look at them.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BASE="${FOREBRAIN_ACCEPT_HOME:-$(mktemp -d)}"
WORK="$BASE"
HOME_DIR="$WORK/home"
PROJ="$WORK/proj"
EVIDENCE="$WORK/evidence"
BIN="$WORK/forebrain"
CREDS="$WORK/creds.env"
PDF_NE="$WORK/zh-notembedded.pdf"
PDF_EM="$WORK/zh-embedded.pdf"
RANGE_DIR="$WORK/range"
PDF_RANGE="$RANGE_DIR/zh-notembedded.pdf"
SESSION="${FOREBRAIN_ACCEPT_SESSION:-forebrain-read-pdf}"
MODEL="${FOREBRAIN_E2E_MODEL:-deepseek-v4-flash}"
PROVIDER="deepseek"
BASE_URL="https://api.deepseek.com"
# The page line the model must reproduce; the ten ideographs are what a tofu
# renderer cannot produce.
PAGE_TEXT="中文渲染测试：甲乙丙丁戊己庚辛壬癸"
PAGE_IDEOGRAPHS="甲乙丙丁戊己庚辛壬癸"

fail=0
die() { echo "read-pdf: $*" >&2; exit 1; }
note() { echo "read-pdf: $*"; }
# mark_fail records a failed assertion, prints it, and keeps going so one run's
# evidence shows every miss; the verdict at the end reads the flag.
mark_fail() { echo "read-pdf: FAIL: $*" >&2; fail=1; }

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

note "home=$HOME_DIR model=$MODEL"

mkdir -p "$HOME_DIR" "$PROJ" "$EVIDENCE"
echo "scratch project for the read-pdf acceptance" > "$PROJ/README.md"
( umask 077; printf 'FOREBRAIN_E2E_DEEPSEEK_KEY=%s\n' "$KEY" > "$CREDS" )
unset KEY

# 1. Build the real binary and its runtime assets. pkg/pdf links PDFium through
#    pkg-config, so the build needs the fetched library on PKG_CONFIG_PATH, and
#    the CJK fallback fonts must be fetched for the scratch home to seed them
#    (the not-embedded fixture renders tofu without them).
"$REPO/scripts/fetch-libtokenizers.sh"
"$REPO/scripts/fetch-pdfium.sh"
"$REPO/scripts/fetch-pdfium-fonts.sh"
PDFIUM_DIR="$REPO/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
( cd "$REPO" && CGO_ENABLED=1 PKG_CONFIG_PATH="$PDFIUM_DIR" go build -tags fts5 -o "$BIN" ./cmd/forebrain )
"$REPO/scripts/install-dictionary.sh" "$WORK"
"$REPO/scripts/install-assets.sh" "$WORK"
note "built $BIN"

# 2. The two fixtures: the embedded control and the not-embedded case that
#    needs the fallback font. Generated here (the generator is committed, the
#    PDFs under pkg/pdf/testdata are the test's copy).
python3 "$REPO/scripts/acceptance/make_zh_pdf.py" --out "$WORK"
[ -f "$PDF_NE" ] && [ -f "$PDF_EM" ] || die "make_zh_pdf.py did not write both fixtures into $WORK"
note "wrote $PDF_NE and $PDF_EM"

# 3. Config. The key is referenced by ${ENV}, never written literally.
write_config() { # write_config <model>
  cat > "$HOME_DIR/forebrain.yaml" <<YAML
approval_policy: on-request
agents:
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

wait_for() { # wait_for <seconds> <fixed-substring>  (visible pane)
  local secs="$1" pat="$2" i
  for ((i = 0; i < secs * 5; i++)); do
    pane | grep -qF -- "$pat" && return 0
    sleep 0.2
  done
  return 1
}

# wait_capture polls until <fixed-substring> appears in the pane and echoes the
# FIRST capture that contains it, so an assertion can observe the pane at the
# instant a step completed — not after the model has gone on to do other things.
# Empty output (nonzero) when the pattern never appears in <seconds>.
wait_capture() { # wait_capture <seconds> <fixed-substring>
  local secs="$1" pat="$2" i p
  for ((i = 0; i < secs * 5; i++)); do
    p="$(pane)"
    if printf '%s' "$p" | grep -qF -- "$pat"; then
      printf '%s' "$p"
      return 0
    fi
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
# db_last_assistant_since is the newest assistant row newer than <id>, so a
# phase reads its own FINAL ANSWER (role='assistant') and not the previous
# phase's, nor the reasoning paragraph (role='reasoning') — a separate row.
db_last_assistant_since() { dbq "SELECT content FROM fb_messages WHERE role='assistant' AND id > $1 ORDER BY id DESC LIMIT 1;"; }
db_max_tool_id() { local v; v="$(dbq "SELECT COALESCE(MAX(id),0) FROM fb_messages WHERE role='tool';")"; printf '%s' "${v:-0}"; }
db_max_event_sequence() { local v; v="$(dbq "SELECT COALESCE(MAX(sequence),0) FROM fb_session_events;")"; printf '%s' "${v:-0}"; }
# db_pdf_row_since prints the newest role='tool' row newer than <baseline-id>
# that carries an image block AND names <basename> — THIS phase's read, as the
# model received it. The basename ties the row to one fixture, so a previous
# phase's row can never satisfy it. This is the ground truth, NOT a
# pdf_page_count key: the extra pdf_* keys live only in the live step event and
# are never persisted; what the database keeps is the row's text (the
# pdfReadNote) and the image block. The parts match is deliberately loose
# (%"image"%) so both '"type":"image"' and '"type": "image"' spellings are
# found; the assertion below pins the exact type spelling. content and parts are
# concatenated because the note and the image block live in different columns.
db_pdf_row_since() { # <baseline-id> <basename>
  dbq "SELECT COALESCE(content,'') || ' ' || COALESCE(parts,'') FROM fb_messages WHERE role='tool' AND id > $1 AND COALESCE(parts,'') LIKE '%\"image\"%' AND (COALESCE(content,'') LIKE '%$2%' OR COALESCE(parts,'') LIKE '%$2%') ORDER BY id DESC LIMIT 1;"
}
db_pdf_pages_event_since() { # <baseline-sequence>
  dbq "SELECT payload_json FROM fb_session_events WHERE sequence > $1 AND event_type='tool_call_completed' AND json_extract(payload_json, '$.tool_name')='read_file' AND json_extract(payload_json, '$.output.pdf_pages[0]')=7 AND json_extract(payload_json, '$.output.pdf_pages[1]')=8 AND json_array_length(json_extract(payload_json, '$.output.pdf_pages'))=2 ORDER BY sequence DESC LIMIT 1;"
}

check() { # check <description> <grep -E pattern> <text>
  local desc="$1" pat="$2" text="$3"
  if printf '%s' "$text" | grep -Eq -- "$pat"; then
    note "PASS: $desc"
  else
    mark_fail "$desc (pattern: $pat)"
    printf '%s\n' "$text" | tail -20 >&2
  fi
}

# assert_no_card fails the run when a capture shows an approval overlay. It is
# handed a capture taken AT THE READ'S completion on purpose: the model may go
# on to run an unrelated shell command that legitimately asks for approval, and
# that must never be reported as "reading a PDF asked for approval". A card up
# while the read itself completes is the read asking, and fails the phase.
assert_no_card() { # assert_no_card <description> <pane text>
  local desc="$1" text="$2"
  if printf '%s' "$text" | grep -Eq 'Tool approval|Yes, proceed|批准|是否允许'; then
    mark_fail "$desc shows an approval card — reading a PDF must never ask for approval"
  else
    note "PASS: $desc raised no approval card"
  fi
}

# --- one phase: read a PDF, then assert on its step, row and answer --------
# Every assertion below names the SURFACE it observes: the TUI pane (what the
# terminal draws), or the DB (what the model actually received). The step
# SUMMARY string ("Read … · PDF n 页 · 渲染 a-b 页") is a surface-level string
# that reaches the web card via SummarizeToolStep; the TUI draws its own header
# and never shows it, so it is asserted nowhere here — it is pinned by
# pkg/tool's TestSummarizeToolStepNamesAReadPDF instead.
run_phase() { # run_phase <evidence-label> <pdf path>
  local label="$1" pdf="$2" base before_tool p row answer i
  base="$(basename "$pdf")"
  before_tool="$(db_max_tool_id)"

  # The prompt asks the model to call only read_file: this phase measures the
  # READ, and a shell command the model chooses to run on its own can raise an
  # unrelated approval card that would stall the turn (and it is not what this
  # phase proves).
  submit "read_file $pdf 然后逐字念出页面上的中文内容：请原样输出页面上那一行中文，一个字都不要改。只调用 read_file，不要执行任何 shell 命令。"

  # (DB) THE phase signal, and it is phase-scoped. Poll for THIS fixture's read
  # row: the newest tool row after the baseline that carries an image block and
  # names $base. The pane is not consulted for the signal at all, so nothing a
  # previous phase left on screen can satisfy it early — the 053 lesson applied:
  # the DB is the signal, the pane is evidence. (The earlier version waited on a
  # bare "Ctrl+O" marker, which a previous phase's still-visible marker matched;
  # that is the race this ordering removes.)
  row=""
  for ((i = 0; i < 600; i++)); do
    row="$(db_pdf_row_since "$before_tool" "$base")"
    [ -n "$row" ] && break
    sleep 0.2
  done
  if [ -z "$row" ]; then
    mark_fail "$label: this phase's read row never landed (no image-block tool row naming $base within 120s)"
  fi

  # (TUI) Capture the pane only AFTER that row landed, so the capture shows THIS
  # phase's step and not whatever a previous phase left on screen. The wait is on
  # "Read <full path>" — the capital-R header line, which the echoed composer
  # prompt ("read_file <path>") does not match and a previous phase's different
  # path does not match either. A bare "Ctrl+O" wait is deliberately NOT used.
  p=""
  if ! p="$(wait_capture 60 "Read $pdf")"; then
    p="$(pane)"
    mark_fail "$label: the TUI never drew this phase's step header (no 'Read $pdf' within 60s)"
  fi
  pane > "$EVIDENCE/$label-pane.txt"
  # The phase's own header and marker lines, saved beside the pane so a reviewer
  # can see the capture belongs to this phase (and not a stale one).
  printf '%s\n' "$p" | grep -nE "Read .*${base}|▤ .*Ctrl\+O" > "$EVIDENCE/$label-marker.txt" || true

  # (TUI) the terminal's own rendering of THIS phase's step.
  check "$label: the TUI header names the read PDF" "Read .*${base}" "$p"
  check "$label: the TUI draws the image step marker" '▤ .*Ctrl\+O' "$p"
  if printf '%s' "$p" | grep -q '"preview_kind"'; then
    mark_fail "$label: the read_file step fell through to a JSON dump"
  fi

  # (the read itself) no approval card at the moment the read completed.
  assert_no_card "$label: the read" "$p"

  # (DB) what the model actually received: the image block and the note. The
  # page count is carried in the note's own text, not a persisted pdf_* key.
  check "$label: the tool row carries an image block (the model saw pixels)" '"type"[[:space:]]*:[[:space:]]*"image"' "$row"
  check "$label: the tool row's note names the PDF and its page count" 'pdf .*: [0-9]+ pages' "$row"
  check "$label: the tool row's note names this PDF" "$base" "$row"

  # (DB) the FINAL ANSWER comes from the assistant row, not the reasoning
  # paragraph. It must reproduce the page's Chinese text verbatim.
  answer=""
  for ((i = 0; i < 900; i++)); do
    answer="$(db_last_assistant_since "$before_tool")"
    printf '%s' "$answer" | grep -qF "$PAGE_IDEOGRAPHS" && break
    sleep 0.2
  done
  check "$label: the final answer reproduces the page's Chinese text verbatim" "$PAGE_IDEOGRAPHS" "$answer"
}

# --- page-range phase: ask for pages 7-8 of a nine-page PDF -----------------
run_range_phase() {
  local label="03-page-range" pdf="$1" base before_tool before_event p row event answer i
  base="$(basename "$pdf")"
  before_tool="$(db_max_tool_id)"
  before_event="$(db_max_event_sequence)"

  submit "read_file $pdf pages=\"7-8\" 然后逐字念出第 7 页页脚的页码，必须原样输出第 7 页页脚，不要执行任何 shell 命令。"

  row=""
  for ((i = 0; i < 600; i++)); do
    row="$(db_pdf_row_since "$before_tool" "$base")"
    [ -n "$row" ] && break
    sleep 0.2
  done
  if [ -z "$row" ]; then
    mark_fail "$label: this phase's read row never landed (no image-block tool row naming $base within 120s)"
  fi

  event=""
  for ((i = 0; i < 600; i++)); do
    event="$(db_pdf_pages_event_since "$before_event")"
    [ -n "$event" ] && break
    sleep 0.2
  done
  if [ -z "$event" ]; then
    mark_fail "$label: canonical event never recorded pdf_pages=[7,8] within 120s"
  fi

  p=""
  if ! p="$(wait_capture 60 "Read $pdf")"; then
    p="$(pane)"
    mark_fail "$label: the TUI never drew this phase's step header"
  fi
  pane > "$EVIDENCE/$label-pane.txt"
  printf '%s\n' "$p" | grep -nE "Read .*${base}|▤ .*Ctrl\+O" > "$EVIDENCE/$label-marker.txt" || true

  check "$label: the TUI header names the range-read PDF" "Read .*${base}" "$p"
  check "$label: the TUI draws the image step marker" '▤ .*Ctrl\+O' "$p"
  assert_no_card "$label: the range read" "$p"
  check "$label: the tool row carries an image block" '"type"[[:space:]]*:[[:space:]]*"image"' "$row"
  check "$label: the note names rendered pages 7-8" 'rendered pages 7-8' "$row"
  check "$label: the canonical event stores page 7" '"pdf_pages"[[:space:]]*:[[:space:]]*\[[[:space:]]*7' "$event"
  check "$label: the canonical event stores page 8" '"pdf_pages"[[:space:]]*:[^]]*8' "$event"

  answer=""
  for ((i = 0; i < 900; i++)); do
    answer="$(db_last_assistant_since "$before_tool")"
    printf '%s' "$answer" | grep -qF "第 7 页" && break
    sleep 0.2
  done
  check "$label: the final answer names page 7" '第 7 页' "$answer"
}

# --- Phase 1: the not-embedded fixture (needs the fallback font) ----------
start_tui "$MODEL"
run_phase "01-notembedded" "$PDF_NE"

# --- Phase 2: the embedded fixture (the control) --------------------------
run_phase "02-embedded" "$PDF_EM"

# --- Phase 3: a nine-page fixture makes the requested page observable -------
mkdir -p "$RANGE_DIR"
python3 "$REPO/scripts/acceptance/make_zh_pdf.py" --out "$RANGE_DIR" --no-embed --pages 9
[ -f "$PDF_RANGE" ] || die "make_zh_pdf.py did not write the range fixture"
run_range_phase "$PDF_RANGE"

# --- Evidence: the rendered page PNGs, for a human to look at -------------
rendered="$(find "$HOME_DIR" -path '*tool-outputs*' -name '*.png' 2>/dev/null | sort || true)"
if [ -n "$rendered" ]; then
  note "rendered page PNGs (left for inspection):"
  printf '%s\n' "$rendered"
else
  mark_fail "no rendered page PNG was written under $HOME_DIR (tool-outputs)"
fi
note "expected page text: ${PAGE_TEXT}"

# --- Verdict ---------------------------------------------------------------
if [ "$fail" -eq 0 ]; then
  echo "read pdf: PASS"
else
  echo "read pdf: FAIL" >&2
  exit 1
fi
