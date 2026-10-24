---
name: run-forebrain
description: Build, launch, drive and screenshot the forebrain TUI for real - send keys, click, read the rendered screen, read which cell the composer's text caret is actually on, and check the state DB; replay the viewport painter in macOS Terminal.app to screenshot pixel-level rendering (stray dots, residue after scrolling, glyphs drawing past their row); and run it live in a Terminal.app window with real key presses through an input method (pinyin marked text). Use when asked to run, start, or screenshot forebrain, to confirm an interactive change (Esc/Ctrl+C handling, composer, approvals, cancellation, viewport) works in the actual app rather than only in tests, to check where a mouse click or a chip like `[Image #1]` puts the caret, or to reproduce/verify a visual rendering artifact in the TUI, including one that only appears while typing Chinese.
---

forebrain is a Go terminal UI. It takes over the terminal, so an agent drives it
through `.claude/skills/run-forebrain/driver.sh`, which wraps it in tmux and points
it at a fake OpenAI-compatible provider whose timing you control. Paths below
are relative to the repo root.

tmux works on cells, so a bug that lives in pixels — ink left on screen after a
scroll, a glyph reaching into the next row — never shows in `driver.sh screen`.
A caret drawn on the wrong *cell* is a cell-level bug, so tmux reads that fine:
`driver.sh caret` prints the reverse-video glyph the composer actually paints
(see [the caret example](#a-worked-example-where-a-click-puts-the-composer-caret)).

For pixel artifacts, `paint_dump.sh` records the painter's real bytes and
`term_replay.py` plays them in a real Terminal.app window and screenshots every
step (see [Run: pixel-level rendering](#run-pixel-level-rendering-in-terminalapp)).

None of that ever sees an input method: pinyin being composed lives between the
keyboard and Terminal.app, and the program gets only the committed text, or
nothing at all. For those, `term_live.py` runs forebrain in a Terminal.app
window of its own and presses real keys in it (see
[Run: live in Terminal.app](#run-live-in-terminalapp-with-a-real-input-method)).

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
| `netdrop` | request 1 streams part of an answer, then closes the connection cleanly with no finish frame; later requests answer normally. `FAKE_NETDROP_EVERY=1` drops every request, `FAKE_NETDROP_SILENT=1` drops with no bytes; `NETWORK_RETRY_MAX=N` sets `agents.defaults.network_retry_max` | network retry: the Working line's retry status, the dead attempt withdrawn, exhaustion |
| `limit` | 429 "usage limit reached" (with `resets_in_seconds`) until `FAKE_LIMIT_SECONDS` (default 20) after the first request, then like `reply` | auto-continue: the notice under the composer, Esc or typing to cancel it, and the continuation turn once the limit resets |
| `subagent-net` | the main agent's first request dispatches a general-purpose `subagent_run` probe; the subagent's own request (recognised by its system prompt) is dropped on the wire until its last user message is `continue`, then answers with the answer text; the main agent's later requests answer with a fixed line | talking to a subagent directly: after it fails on the network, sending it `continue` from its own view and seeing the answer there. Needs `ENABLE_SUBAGENT=1` |
| `subagent-limit` | the main agent's first request dispatches a general-purpose `subagent_run` probe; the subagent's own request is refused with the `limit` 429 (with `resets_in_seconds`) for `FAKE_LIMIT_SECONDS` (default 20) after its first request and then answers with the answer text; the main agent's later requests answer with a fixed line | a subagent's automatic continuation: the notice in the subagent's own view, and, once the limit resets, the continuation message running there without the user doing anything. Needs `ENABLE_SUBAGENT=1` |
| `subagent-steer` | the main agent's first request dispatches a general-purpose `subagent_run` probe; the subagent's first request runs a shell call (the answer text, default `sleep 8`); its next request — carrying the tool result and any steer queued meanwhile — sends one delta and hangs, so the subagent keeps running | a steer queued in a subagent's view (Down, Down, Enter opens it; type and Enter) being taken at its tool boundary: the "submitted after next tool call" preview must drop it with no key pressed, and it appears as a user message in that view. Needs `ENABLE_SUBAGENT=1` |

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

### A worked example: where a click puts the composer caret

The composer paints no hardware cursor: it draws its own caret as **reverse
video over the glyph it sits on**. So the pane's escape stream answers "which
character is the caret on", and `$D caret` reads it. This is the only honest
check of a click-to-caret change — a unit test can pass while the app puts the
caret five characters off.

```bash
D=.claude/skills/run-forebrain/driver.sh
$D reset; $D start hang
osascript -e 'set the clipboard to (read (POSIX file "/tmp/fb-chip.png") as «class PNGf»)'
$D key C-v                                   # the chip: [Image #1]
$D send "现在用浏览器打开打不开 妃"
$D rows                                      # 38	› [Image #1]现在用浏览器打开打不开 妃
$D col 妃                                    # row=38 col=36
$D click 36 38; $D caret
```

Observed, after the coordinate-space fix:

```
caret='妃' x4  row=38  (4/6 samples)
line='› [Image #1]现在用浏览器打开打不开 妃'
```

Before it the same gesture drew the caret on `打`, five characters to the left.
Two more cases belong in the same run, because they fail independently:

```bash
$D col g; $D click 7 38; $D caret     # inside the chip: caret='g'  (was '[': chip's start)
$D key C-c; $D send "hello world"
$D col w; $D click 9 38; $D caret     # plain draft:  caret='w'  (unchanged behaviour)
```

An image for the clipboard, the fake PNG the example uses:

```bash
python3 - <<'PY'
import zlib, struct
w = h = 8
raw = b''.join(b'\x00' + bytes([200, 30, 30]) * w for _ in range(h))
def chunk(t, d):
    return struct.pack('>I', len(d)) + t + d + struct.pack('>I', zlib.crc32(t + d) & 0xffffffff)
open('/tmp/fb-chip.png', 'wb').write(
    b'\x89PNG\r\n\x1a\n'
    + chunk(b'IHDR', struct.pack('>IIBBBBB', w, h, 8, 2, 0, 0, 0))
    + chunk(b'IDAT', zlib.compress(raw)) + chunk(b'IEND', b''))
PY
```

The right-aligned `Image in clipboard · ctrl+v to paste` hint above the card is
the proof the clipboard holds an image before `C-v` is worth sending.

**Why it needs the real app: two rune spaces.** The composer has the buffer the
raw input reader owns (plain text) and the display string the card lays out —
the same text with SGR styling around every chip
(`renderComposerImagePlaceholder`, `renderComposerPastePlaceholder`). Counting
must happen in the **plain** space: escape bytes are phantom *columns*
(`lipgloss.Width` of each escape rune is 1) and escape runes are phantom *caret
offsets* (`utf8.RuneCountInString`), so a click drifts by an amount that depends
on how many chips there are and how much text surrounds them — never a clean
"one character". `ComposerRenderState.Cursor` is the one offset that stays in
display space, which is why a caret *inside* a chip needs the chip's opening
escapes added back. When touching any of this, keep hit-test tables
(`composerTextArea.runes`, the `composerTextRowsFromSpans` offsets) in the plain
space, and feed tests `buildComposerDisplayText(draft, pastes, attachments)` —
bare text is the input that let the original bug ship green.

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

## Run: live in Terminal.app with a real input method

For bugs that need the real terminal *and* the real keyboard path — above all
an input method's marked text. macOS only; the owner types with Sogou pinyin.
`driver.sh serve` sets up the same isolated home, config and fake provider as
`start` (same modes and arguments) without launching the TUI in tmux, and
`term_live.py open` runs it in a new Terminal.app window instead. Read
[the live gotchas](#live-terminalapp-and-input-methods) before the first
`keys`: it needs two permissions, and the input method in its Chinese mode.

```bash
R=.claude/skills/run-forebrain
D=$TMPDIR/fb-live
$R/driver.sh serve reply ok                        # home, config, fake provider; no tmux
python3 $R/term_live.py open $D/fb 131 30          # forebrain in its own window, 131x30
python3 $R/term_live.py send $D/fb ' 如forebrain harness tui的composer截图所示，鼠标拖拽选中某一区间会导致后面的直到终端最右边的区域都露出终端背景色，请定位根因并'
python3 $R/term_live.py keys $D/fb xiufu           # Sogou composes xiu'fu at the caret
python3 $R/term_live.py keys $D/fb space           # and commits 修复
python3 $R/term_live.py contents $D/fb | tail -3   # Terminal.app's own screen
python3 $R/term_live.py close $D/fb
$R/driver.sh stop
```

Observed (the bite out of the composer card's bottom-right corner, fixed by
`imeRepairStart`) — the caret sat 3 columns from the right edge, the marked text
ran on into the bottom rule, and the rule kept 3 blank cells after the commit:

```
before                                      after
 131|  修复                                   131|  修复
 128|─────────────────────────…               131|─────────────────────────…
 131|deepseek/deepseek-chat …                 131|deepseek/deepseek-chat …
```

What Terminal.app itself does, with no forebrain involved — `probe` paints a
40×12 screen of lettered rows, parks the cursor and never writes again:

```bash
python3 $R/term_live.py probe $D/probe 6 20        # cursor parked at row 6, column 20
python3 $R/term_live.py keys $D/probe xiufu
python3 $R/term_live.py contents $D/probe | sed -n 6p
python3 $R/term_live.py keys $D/probe esc          # cancels the composition
python3 $R/term_live.py contents $D/probe | sed -n 6p
cat $D/probe/in.log                                # what reached the program
python3 $R/term_live.py close $D/probe
```

```
  40|FFFFFFFFFFFFFFFFFFFxiu'fuFFFFFFFFFFFFFFF
  34|FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF
size 40x12 cursor 6;20
```

The marked text is *inserted* into Terminal.app's screen; cancelling it shifts
the row back and leaves 6 blank cells at its end, and the program is sent
nothing at all.

- `open <dir> [cols] [rows]` (default 141×30) runs `$FOREBRAIN_RUN_WORK/forebrain`
  in the driver's home and project inside a transparent pty proxy: every byte
  passes through unchanged both ways, the program's output is kept in
  `<dir>/out.bin` with one `offset time` line per write in `out.idx`, and every
  input byte in `in.log`, marked `kbd` (the keyboard) or `send`. It answers the
  trust page and returns once the composer is drawn.
- `send <dir> <text>` writes bytes straight to the program, with Python escapes
  (`'\x16'` is ctrl+v, `'\x1b'` Esc, `'\x1b[200~…\x1b[201~'` a bracketed paste).
  No input method sees them: use it to set up text, never to test typing.
- `keys <dir> <key>...` presses real keys through System Events, which the
  input method does see. A key is a word of lowercase letters (`xiufu` presses
  x, i, u, f, u), a name (`space esc return tab delete shift`) or a raw macOS
  key code.
- `contents <dir>` prints Terminal.app's own screen, each line with its display
  width. Compared with what `bytes <dir> [seconds]` shows the program wrote,
  it tells a terminal's own change from the program's.
- `shot <dir> <png>` screenshots the window; `close <dir>` quits and the window
  closes by itself.
- `probe <dir> <row> <col> [cols] [rows]` and `paint <dir> fill|row R|cup R C`
  isolate the terminal: the probe repaints only when told to.
- For the "before" run, build the old commit in a throwaway `git worktree` into
  a second `FOREBRAIN_RUN_WORK` and symlink `dict`, `fonts`, `models.json` and
  `tokenizers` beside it from the current one; never revert the fix in the
  shared tree.

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
  without a keybinding. Find the coordinates in a `$D screen` capture, then
  `$D col <substring>` for a display column — never count them by hand, a CJK
  glyph takes two cells and a chip's styling offsets nothing here.
- **The caret is reverse video, so it must be read with `-e`.** `$D screen` is
  `capture-pane` without `-e`, which strips SGR and therefore *always* shows no
  caret; `$D caret` captures with `-e` and prints the glyph painted in reverse
  video. It samples six times over 1.5s because the caret blinks every 500ms —
  a shorter look can land entirely in the off phase and read as a bug.
- **Click coordinates are 1-based, and a wide glyph owns two of them.** Clicking
  either cell of a CJK glyph puts the caret *before* that glyph; that is the
  intended mapping, not an off-by-one.
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

### Live Terminal.app and input methods

- **Two permissions, both the owner's to grant.** The first `keys` asks
  "Terminal wants to control System Events"; until it is answered every
  osascript call to System Events hangs, which `term_live.py` reports as a
  timeout. Pressing keys also needs Terminal under System Settings → Privacy &
  Security → Accessibility, or osascript fails with error 1002. Ask the owner;
  never click either yourself.
- **`keystroke "text"` bypasses the input method**: the letters reach the
  program as they are. Only `key code` presses — what `keys` sends — go through
  it.
- **The input method must be composing.** If `in.log` shows the letters arriving
  as `kbd b'x'`, Sogou is in its English mode: `keys <dir> shift` toggles it
  (`keys` presses Shift in a System Events call of its own, because pressed in
  the same call as the keys after it, it shifts them). Put it back the way you found it when you are done.
- **`keys` takes the focus.** It brings its own window to the front, by the id
  `do script` returned, and checks that window is in front before every press;
  the owner's window loses the focus meanwhile, so keep the run short.
- **Terminal.app inserts marked text into its screen at the hardware cursor**,
  even a hidden one: the rest of the row moves right, cells pushed past the
  edge are dropped, and text that reaches the edge carries on into the next
  rows. Committing or cancelling shifts every touched row back and leaves blank
  cells, on the default background, at the end of each. Marked text that runs
  past the last row scrolls the whole alt screen. A cancel sends the program
  nothing. This is why the painter rewrites the rows from the hardware cursor
  down and parks the cursor when no caret is shown (`imeRepairStart`).
- **`contents` joins soft-wrapped rows into one line**, and marked text that
  wraps leaves the rows it crossed marked as wrapped: after an input-method run
  two screen rows may come back as one line.
- **Terminal.app erases with the background in effect.** `\x1b[2K` and `\x1b[K`
  fill with whatever background the last SGR left on, so a row that ends with
  a background still set paints the next row the painter erases.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `no server running on /private/tmp/tmux-501/default` | The app exited and took the tmux server with it. Read `$TMPDIR/forebrain-run/tui.err` — the driver redirects stderr there for exactly this. |
| `driver: TUI never reached the composer` | Same: check `tui.err`. A config validation failure is the usual cause. |
| `driver: fake provider never bound port 8731` | Port in use by an earlier run. `$D stop`, or set `FOREBRAIN_RUN_PORT`. |
| `$D messages` errors on a glob | The TUI has not created the state DB yet; it appears once the first turn is submitted. |
| Turn never leaves `Working` in `reply` mode | Expected in `hang`/`stream` mode. In `reply` mode check `$D provider-log` for a `REQUEST` line — no line means the request never left forebrain. A `REQUEST` line followed by a Python traceback means the fake provider crashed on that request; the traceback names why. |
| `$D messages` / `$D title` print nothing after Esc in `hang` mode | Not an error: a turn withdrawn before the first output writes nothing. |
| `$D caret` always says `(none in 6 samples …)` | You are reading a capture without SGR: `$D screen` strips it. Only `$D caret` sees reverse video. |
| `$D caret` shows a space | The click landed past the end of that line — a valid "caret at end of text", not a miss. |
| `$D click` seems to do nothing | Screen coordinates are 1-based and the composer moves as the draft grows or wraps; re-read them with `$D rows` instead of reusing an old row. |
| `$D key C-v` inserts no chip | The clipboard holds no image (macOS); check `osascript -e 'clipboard info'` and re-set it. Never change product code to get a pass. |
| `no matches found: …/fb-paint/*` from zsh | zsh fails a glob that matches nothing. `paint_dump.sh` clears its out-dir itself; don't `rm` its contents with a glob. |
| `term_live: nothing listens on port …` | Run `driver.sh serve <mode>` first, with the same `FOREBRAIN_RUN_WORK` and `FOREBRAIN_RUN_PORT`. |
| `term_live: osascript timed out` | A permission dialog is waiting on screen; see the live gotchas. |
| `osascript failed: … (1002)` on `keys` | Terminal lacks Accessibility; see the live gotchas. |
| `keys` letters land in the composer as plain text | The input method is in its English mode, or not selected; see the live gotchas. |
| `term_live: forebrain never reached the composer` | Read `<dir>/out.bin` and `<dir>/proxy.err`; a config failure prints there before the TUI paints. |

## Test suite

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/turn ./pkg/state -count=1
```

`pkg/architecture` enforces repo-wide structure (e.g. at most 20 production
files per package) and is worth running after adding files.
