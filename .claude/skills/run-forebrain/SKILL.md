---
name: run-forebrain
description: Build, launch, drive and screenshot the forebrain TUI for real - send keys, read the rendered screen, and check the state DB. Use when asked to run, start, or screenshot forebrain, or to confirm an interactive change (Esc/Ctrl+C handling, composer, approvals, cancellation, viewport) works in the actual app rather than only in tests.
---

forebrain is a Go terminal UI. It takes over the terminal, so an agent drives it
through `.claude/skills/run-forebrain/driver.sh`, which wraps it in tmux and points
it at a fake OpenAI-compatible provider whose timing you control. Paths below
are relative to the repo root.

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
| `toolcall` | `enter_plan_mode`, then `exit_plan_mode` | a real approval overlay and the action row it leaves |
| `ask` | one two-question `user_interaction` call | the real question form: tab bar, options, and the Other text field |
| `limit` | 429 "usage limit reached" (with `resets_in_seconds`) until `FAKE_LIMIT_SECONDS` (default 20) after the first request, then like `reply` | auto-continue: the notice under the composer, Esc or typing to cancel it, and the continuation turn once the limit resets |

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

Full command list: `$D` with no arguments.

### Reading the result

Two independent surfaces, and you usually want both:

```bash
$D screen                   # rendered viewport + composer, via tmux capture-pane
$D messages                 # id | role | source | content
$D title                    # session naming, which /resume and the window title show
$D db "SELECT count(*) FROM fb_messages WHERE IFNULL(source,'transcript')='transcript';"
$D provider-log             # proof the request actually went out
```

Set `FAKE_DUMP_DIR=<dir>` in the provider's environment to keep every request
body as `request-NNNN.json`: it is how you check what the model was actually
sent, and that one turn's request is the byte-for-byte prefix of the next (the
prompt cache depends on it).

`source` is the durable half of the contract: a row at `transcript` is what the
next model request carries, anything else is hidden from both the transcript and
the model while staying on disk for audit.

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
composer, no `cancelling run` and no `Worked for` are ever painted, the row
flips to `transcript_withdrawn`, and the session title reverts from the message
text to the session id.

Swap `start hang` for `start stream` and the same keystrokes produce ordinary
cancellation instead — user card kept, `cancelling run`, `Worked for`, and the
partial assistant text persisted.

## Run (human path)

```bash
CGO_ENABLED=1 go build -tags fts5 -o /tmp/forebrain ./cmd/forebrain && scripts/install-dictionary.sh /tmp && /tmp/forebrain
```

Uses the real `~/.forebrain` config and real providers. Ctrl+C twice to quit.

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
- **The binary is ~90MB and the build is not fast.** `$D start` reuses
  `$TMPDIR/forebrain-run/forebrain` if it exists — run `$D build` explicitly after
  changing Go code, or `$D reset` will not rebuild it for you.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `no server running on /private/tmp/tmux-501/default` | The app exited and took the tmux server with it. Read `$TMPDIR/forebrain-run/tui.err` — the driver redirects stderr there for exactly this. |
| `driver: TUI never reached the composer` | Same: check `tui.err`. A config validation failure is the usual cause. |
| `driver: fake provider never bound port 8731` | Port in use by an earlier run. `$D stop`, or set `FOREBRAIN_RUN_PORT`. |
| `$D messages` errors on a glob | The TUI has not created the state DB yet; it appears once the first turn is submitted. |
| Turn never leaves `Working` in `reply` mode | Expected in `hang`/`stream` mode. In `reply` mode check `$D provider-log` for a `REQUEST` line — no line means the request never left forebrain. |

## Test suite

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/turn ./pkg/state -count=1
```

`pkg/architecture` enforces repo-wide structure (e.g. at most 20 production
files per package) and is worth running after adding files.
