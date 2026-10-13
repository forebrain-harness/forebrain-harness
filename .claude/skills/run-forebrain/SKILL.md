---
name: run-forebrain
description: Build, launch, drive and screenshot the forebrain TUI for real - send keys, read the rendered screen, and check the state DB; and replay the viewport painter in macOS Terminal.app to screenshot pixel-level rendering (stray dots, residue after scrolling, glyphs drawing past their row). Use when asked to run, start, or screenshot forebrain, to confirm an interactive change (Esc/Ctrl+C handling, composer, approvals, cancellation, viewport) works in the actual app rather than only in tests, or to reproduce/verify a visual rendering artifact in the TUI.
---

forebrain is a Go terminal UI. It takes over the terminal, so an agent drives it
through `.claude/skills/run-forebrain/driver.sh`, which wraps it in tmux and points
it at a fake OpenAI-compatible provider whose timing you control. Paths below
are relative to the repo root.

tmux works on cells, so a bug that lives in pixels — ink left on screen after a
scroll, a glyph reaching into the next row — never shows in `driver.sh screen`.
For those, `paint_dump.sh` records the painter's real bytes and `term_replay.py`
plays them in a real Terminal.app window and screenshots every step (see
[Run: pixel-level rendering](#run-pixel-level-rendering-in-terminalapp)).

The reason for the fake provider is not offline convenience: most of forebrain's
interactive behaviour is defined **relative to the model's first output-bearing
event** — before it, Esc withdraws the submission; after it, Esc is ordinary
cancellation. A real provider answers too fast and too unpredictably to park a
turn on either side of that line on purpose.

## Prerequisites

```bash
brew install tmux      # macOS; not preinstalled. `screen` is NOT a substitute (see Gotchas)
```

`go`, `python3` and `sqlite3` are already present on a machine that builds this
repo. Nothing else is needed — no API key, no network.

## Run (agent path)

```bash
D=.claude/skills/run-forebrain/driver.sh

$D start hang                       # build if needed + launch; provider accepts and never answers
$D submit 'this is the wrong question'
$D wait 'esc to interrupt' 25       # the run is in flight
$D key Escape
$D screen                           # the rendered screen
$D messages                         # what the DB holds
$D stop
```

`start` takes the provider mode, which is how you choose where the turn parks:

| Mode | Behaviour | Use it to test |
|---|---|---|
| `hang` (default) | accepts the request, never answers | everything *before* the first output event |
| `stream` | sends one content delta, then hangs | everything *after* the response boundary, turn still in flight |
| `reply` | sends one delta and finishes | a normally completing turn |
| `usage` | like `reply`, and reports usage with a cache split (2000 prompt, 1800 cached, 40 output) | persisted run usage, `/status`'s Usage tab and cache hit rate |
| `drip` | sends the answer word by word, then finishes | that streamed output actually renders as it arrives |
| `shell` | the first request answers with a `shell` call running the answer text (a JSON object sets the other shell arguments), later ones with the text | a real shell tool card: `start shell 'echo hello-from-shell'` runs without approval and draws `● Ran echo hello-from-shell` with `└ hello-from-shell` |
| `toolcall` | `enter_plan_mode`, then `exit_plan_mode` | a real approval overlay and the action row it leaves |
| `ask` | one two-question `user_interaction` call | the real question form: tab bar, options, and the Other text field |
| `limit` | 429 "usage limit reached" (with `resets_in_seconds`) until `FAKE_LIMIT_SECONDS` (default 20) after the first request, then like `reply` | auto-continue: the notice under the composer, Esc or typing to cancel it, and the continuation turn once the limit resets |
| `subagent-net` | the main agent's first request dispatches a general-purpose `subagent_run` probe; the subagent's own request (recognised by its system prompt) is dropped on the wire until its last user message is `continue`, then answers with the answer text; the main agent's later requests answer with a fixed line | talking to a subagent directly: after it fails on the network, sending it `continue` from its own view and seeing the answer there. Needs `ENABLE_SUBAGENT=1` |
| `subagent-limit` | the main agent's first request dispatches a general-purpose `subagent_run` probe; the subagent's own request is refused with the `limit` 429 (with `resets_in_seconds`) for `FAKE_LIMIT_SECONDS` (default 20) after its first request and then answers with the answer text; the main agent's later requests answer with a fixed line | a subagent's automatic continuation: the notice in the subagent's own view, and, once the limit resets, the continuation message running there without the user doing anything. Needs `ENABLE_SUBAGENT=1` |

```bash
$D start drip 'the answer arrives one word at a time as it streams' 0.6
$D submit 'stream me an answer'
$D wait '●' 25          # wait for the FIRST word, not for the run to start
$D screen | grep '●'; sleep 1; $D screen | grep '●'   # the line must have grown
```

`start reply 'hello from the fake model'` overrides the answer text; `drip`
takes a third argument for the per-word delay (default 0.6s).

`COMPACT_LIMIT=300 $D start drip ...` lowers the auto-compaction threshold so a
short scripted conversation crosses it; with `drip` the compaction's own summary
streams slowly, which is how to watch the progress card move. `/compact` in any
mode exercises the manual path, and Escape during it cancels it.

`FAKE_REPLY_DELAY=20 $D start tool '{"name":"shell","arguments":{"command":"sleep 6"}}'`
holds the reply that follows the scripted tool call for 20 seconds: a message
queued while the tool runs is taken for that request and sits in flight —
still shown in the queue, not yet answered — for the whole delay.

Full command list: `$D` with no arguments.

### Reading the result

Two independent surfaces, and you usually want both:

```bash
$D screen                   # rendered viewport + composer, via tmux capture-pane
$D messages                 # id | role | visibility | content
$D title                    # session naming, which /resume and the window title show
$D db "SELECT count(*) FROM fb_messages WHERE visibility='visible';"
$D provider-log             # proof the request actually went out
```

`FAKE_DUMP_DIR=$TMPDIR/fb-dump $D start reply ...` keeps every request body as
`request-NNNN.json` (the directory is created on first use): it is how you check
what the model was actually sent, and that one turn's request is the
byte-for-byte prefix of the next (the prompt cache depends on it).

`visibility` is the durable half of the contract: a `visible` row is what the
next model request carries; `repaired` and `withdrawn` rows are hidden from both
the transcript and the model while staying on disk for audit. A plain turn
writes the user message, the environment-context message and the answer, all
`visible`.

### A worked example: Esc before the model answers

```bash
D=.claude/skills/run-forebrain/driver.sh
$D reset; $D start hang
$D submit 'this is the wrong question'
$D wait 'esc to interrupt' 25
$D key Escape; sleep 3
$D screen; $D messages; $D title
```

Observed: the user card disappears from the transcript, the text is back in the
composer, no `cancelling run` and no `Worked for` are ever painted, and nothing
of the withdrawn turn reaches the DB — in a fresh session `$D messages` and
`$D title` print nothing at all. Withdrawing a later turn of an existing session
(`RESUME_ID=$($D sid) $D start hang`, submit, Esc) leaves the earlier rows and
the title exactly as they were.

Swap `start hang` for `start stream` and the same keystrokes produce ordinary
cancellation instead — user card kept, `cancelling run`, `Worked for`, and the
partial assistant text persisted as a `visible` row.

## Run: pixel-level rendering in Terminal.app

For artifacts that only exist in a real terminal's pixels. macOS only — it
opens a Terminal.app window and uses `screencapture`; the diff needs Pillow
(`python3 -c 'import PIL'` must succeed).

```bash
R=.claude/skills/run-forebrain
$R/paint_dump.sh $TMPDIR/fb-paint                      # record this checkout's painter
$R/paint_dump.sh $TMPDIR/fb-paint-old a329b89~1        # ...and an older commit, in a throwaway worktree
python3 $R/term_replay.py run $TMPDIR/fb-paint         # replay in Terminal.app, screenshot every step
python3 $R/term_replay.py run $TMPDIR/fb-paint-old
python3 $R/term_replay.py diff $TMPDIR/fb-paint 020 ref
python3 $R/term_replay.py diff $TMPDIR/fb-paint-old 020 ref
```

Observed (the stray-dot bug fixed in `a329b89`):

```
020 vs ref: stray ink 0 px None, other differences 0 px None
020 vs ref: stray ink 21 px (53, 816, 55, 822), other differences 0 px None
zoomed crop of 020: .../fb-paint-old/020-vs-ref.png
```

- `paint_dump.sh <out-dir> [commit]` copies `paint_dump_test.go.tmpl` into
  `pkg/tui` as a temporary test, runs it, and deletes it on exit; with a commit
  it does the same in a temporary `git worktree` (removed afterwards). Edit the
  template's scene and steps to fit the bug: it has the package's test helpers
  (`newComposerRenderer`, `forceColorProfile`, `RenderFrame`, `ViewportScroll`,
  `ForceRepaint`, …). Each step file holds exactly the bytes the renderer wrote
  during that step; `ref` is a forced full repaint of the final state.
- `term_replay.py run <dir> [rows] [cols]` (default 24×80, the template's
  size) writes `<dir>/<step>.png` for every step. The window closes by itself.
- `term_replay.py diff <dir> <a> <b>` counts **stray ink** (pixels inked in `a`
  where `b` is bare background — something a fresh paint does not draw) and
  other differences, and saves a 2× crop around them. Read the crop.

## Run (human path)

```bash
scripts/fetch-libtokenizers.sh && CGO_ENABLED=1 go build -tags fts5 -o /tmp/forebrain ./cmd/forebrain && scripts/install-dictionary.sh /tmp && scripts/install-assets.sh /tmp && /tmp/forebrain
```

Uses the real `~/.forebrain` config and real providers. Ctrl+C twice to quit.
Its build half is exactly what `$D build` runs; the launch is for a human at
the keyboard — an agent should not start it, as it writes to the owner's real
sessions.

## Gotchas

- **One delta cannot prove streaming works.** Every LLM request is streamed and
  the answer must appear as it arrives, in the TUI and on the web. A provider
  that sends a single delta renders identically whether the UI paints on arrival
  or holds everything to the end, so verifying streaming needs `start drip`,
  which sends the answer word by word — capture the screen twice and the text
  must have grown. (A real defect hid behind exactly this confusion: the reducer
  used to buffer assistant text and paint it only on run end.)
- **The screen still lags the producer.** Notifications reach the UI through an
  asynchronous queue, so a delta can be in flight when you look. When you need
  to know the response boundary was actually crossed — the line Esc-withdrawal
  turns on — wait on `$D provider-log` or the DB, which are producer-side, never
  on the screen.
- **A plaintext `api_key` in `forebrain.yaml` is a hard startup failure**, not a
  warning: `plaintext secret is not allowed at agents.definitions.main.llm_providers.[0].api_key`
  and exit 1 before the TUI paints. It must be `${ENV_NAME}`, with the variable
  exported into the process. The driver does this with `FAKE_LLM_KEY`.
- **`screen(1)` cannot drive this app**, even though macOS ships it and tmux is
  not installed. A detached `screen` session keeps no display buffer, so
  `hardcopy` writes a 0-byte file. tmux's `capture-pane` is the only capture
  that works here.
- **First launch in a new directory blocks on a trust page** ("Trust this
  directory?") that must be answered before the composer appears. It opens on
  Quit, so the answer is Down then Enter; a bare Enter quits forebrain. The driver handles it; if you launch forebrain yourself, the
  session will look hung until you do.
- **The driver never touches `~/.forebrain`.** It uses an isolated `FOREBRAIN_HOME`
  under `$TMPDIR/forebrain-run`, so real sessions, history and credentials are
  untouched and `$D reset` is safe.
- **`-l` matters when sending text.** `tmux send-keys -t s -l 'text'` sends it
  literally; without `-l`, words like `Escape` or `Enter` are interpreted as key
  names. `$D submit`/`$D send` already use `-l`; `$D key` deliberately does not.
- **`$D click <col> <row>`** sends one mouse click at 1-based screen
  coordinates (as the SGR press+release pair, forwarded literally like any
  other `-l` bytes). Mouse reporting is already on, so the TUI reads it as a
  real click — use it to hit a card row, a roster row or a footer control
  without a keybinding. Find the coordinates in a `$D screen` capture.
- **The binary is ~90MB and the build is not fast** (~30s warm). `$D start` reuses
  `$TMPDIR/forebrain-run/forebrain` if it exists — run `$D build` explicitly after
  changing Go code, or `$D reset` will not rebuild it for you.
- **Other sessions may be driving the default instance.** Give your run its own
  work dir, tmux session and port —
  `export FOREBRAIN_RUN_WORK=$TMPDIR/forebrain-run-mine FOREBRAIN_RUN_SESSION=fb-mine FOREBRAIN_RUN_PORT=8761`
  — before any `$D` command; `build` then builds into that dir as well.

### Pixel-level replay

- **The owner's terminal is Terminal.app with Fira Code 14pt**, where box-drawing
  glyphs (`│ ├ ╭ ╰ …`) and block elements (`█ ▌ ▄`) draw past their row: Fira
  Code's `│` spans -1000..2000 font units against a 2400-unit line, and
  Terminal.app redraws a row's rectangle ~1.5pt higher than it places the glyph.
  The foot of a `│` therefore lies on the next row. CJK, emoji, `●`, Braille and
  `↓ … ·` stay inside their row.
- **Terminal.app clips each redraw to the rows that changed.** A glyph's overflow
  only lands on a neighbour when both rows redraw together, so a scene that
  never does that cannot show residue at all. The template forces a full
  repaint at step 3 for exactly this; in the live app the "Working" status line
  ticking under the transcript does it.
- **Never disable a fix in the working tree to get the "before" screenshots.**
  The owner commits by hand and other sessions share the tree: a commit landed
  while this skill was being written. Pass the old commit to `paint_dump.sh`
  instead.
- **Only the window id `do script` returns may be touched**, never "front
  window": the owner runs live sessions in other Terminal windows.
  `term_replay.py` follows this and runs the replay under `exec`, so the window
  closes when the replay ends.
- **The hardware caret blinks**, which would make two screenshots of one frame
  differ by a cell; the replay hides it before every capture.
- **Write the scene, not just one step.** Residue is history: it needs a frame
  that drew the glyph and a later frame that removed it without touching the
  neighbour. Record every step and compare the last one against `ref`, which is
  how a fresh paint of the same state looks.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `no server running on /private/tmp/tmux-501/default` | The app exited and took the tmux server with it. Read `$TMPDIR/forebrain-run/tui.err` — the driver redirects stderr there for exactly this. |
| `driver: TUI never reached the composer` | Same: check `tui.err`. A config validation failure is the usual cause. |
| `driver: fake provider never bound port 8731` | Port in use by an earlier run. `$D stop`, or set `FOREBRAIN_RUN_PORT`. |
| `$D messages` errors on a glob | The TUI has not created the state DB yet; it appears once the first turn is submitted. |
| Turn never leaves `Working` in `reply` mode | Expected in `hang`/`stream` mode. In `reply` mode check `$D provider-log` for a `REQUEST` line — no line means the request never left forebrain. A `REQUEST` line followed by a Python traceback means the fake provider crashed on that request; the traceback names why. |
| `$D messages` / `$D title` print nothing after Esc in `hang` mode | Not an error: a turn withdrawn before the first output writes nothing. |
| `no matches found: …/fb-paint/*` from zsh | zsh fails a glob that matches nothing. `paint_dump.sh` clears its out-dir itself; don't `rm` its contents with a glob. |

## Test suite

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/turn ./pkg/state -count=1
```

`pkg/architecture` enforces repo-wide structure (e.g. at most 20 production
files per package) and is worth running after adding files.
