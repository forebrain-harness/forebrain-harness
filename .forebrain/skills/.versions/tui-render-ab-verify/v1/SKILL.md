---
name: tui-render-ab-verify
description: A/B verify a forebrain TUI rendering change (syntax colours, card layout, any on-screen styling) on the real binary with SGR-level evidence. Use when you changed how something renders in pkg/tui and need proof that the visible output actually changed as intended — the "colors/layout look wrong" or "did my render change take effect" situation. Drives two builds through the run-forebrain fake-provider harness in isolated tmux sessions, captures `capture-pane -e`, and extracts per-token colour codes so old and new are compared as bytes, not by eye. Also the place to look when you need the 256-colour rendering path (screen/tmux/Apple Terminal) rather than truecolor.
---

# A/B verify a TUI rendering change

Unit tests render the same code path, but they do not prove the pixels. When the
change is *styling* — syntax-highlight palettes, fg/bg bands, card layout — the
only honest evidence is the SGR byte stream the real binary paints into a real
terminal. This skill produces that from two builds and lets you diff them.

It is deliberately a **pair**: `git HEAD` (the build before your change) against
your working tree. Comparing only "the new look" against your expectation is how
a wrong-palette change passes review; comparing against HEAD shows exactly which
bytes moved.

## Preconditions

- macOS or Linux with `tmux`, `python3`, `go` (CGO available), `sqlite3`.
- The repo's driver skill exists: `.claude/skills/run-forebrain/driver.sh` (see its
  `SKILL.md` for the fake-provider model; this skill only adds a workflow on
  top).
- A fixture file the tool call will read, and the JSON for that tool call.
- A **clean** `git HEAD` for the "before" build. If your working tree is dirty
  with unrelated changes, that is fine: the old build comes from a throwaway
  worktree of HEAD, never from your tree.

## Steps

All paths below are relative to the project root. `scripts/` here means
`.forebrain/skills/tui-render-ab-verify/scripts/`.

### 1. Build both binaries

```bash
S=.forebrain/skills/tui-render-ab-verify/scripts
$S/build-binary.sh old "$TMPDIR/forebrain-ab/old"   # clean HEAD -> forebrain.bin
$S/build-binary.sh new "$TMPDIR/forebrain-ab/new"   # working tree -> forebrain.bin
```

The `old` build creates a throwaway worktree of HEAD at `<work>/head-tree`. Do
this **before** you keep editing, or re-run it after your edit to refresh.

### 2. Drive one card per build and capture it

```bash
FIX="$TMPDIR/forebrain-ab/render_sample.go"     # fixture the tool call reads
ARGS='{"name":"read_file","arguments":{"file_path":"render_sample.go","offset":4,"limit":3}}'

$S/drive-card.sh "$TMPDIR/forebrain-ab/old" ab-old 8741 "$FIX" "$ARGS" 'read it' /tmp/ab-old truecolor
$S/drive-card.sh "$TMPDIR/forebrain-ab/new" ab-new 8731 "$FIX" "$ARGS" 'read it' /tmp/ab-new truecolor
```

Arguments: `<work-dir> <tmux-session> <port> <fixture> <tool-args-json> <prompt> <out-prefix> <truecolor|256>`.
It writes `<out-prefix>.card.ansi` (with SGR) and `<out-prefix>.card.txt` (plain).
The prompt only has to start a turn — the fake provider scripts its tool call by
request count — so keep it short.

Run each build in its **own** work dir, session name and port — the fake
provider binds the port, so they cannot share. Expect roughly 20-60s per
capture, most of it waiting for the app to come up.

### 3. Extract and compare

```bash
$S/extract-tokens.py /tmp/ab-old.card.ansi toolStatusAwaitingApproval
$S/extract-tokens.py /tmp/ab-new.card.ansi toolStatusAwaitingApproval
```

It prints, per matching line, every `(colour, text)` run plus the distinct
foreground colours, so old vs new is a readable side-by-side.

### 4. Judge the difference

Read the three numbers the extractor prints for each capture, on the line of
code you care about. The thresholds below are **calibrated against this repo's
own regression** — a palette that had been flattened to one tone — so they are
empirical, not theoretical:

| signal | flattened (bad) | mainstream theme (good) |
|---|---|---|
| strongest saturation among the code tokens | ~0.3 (all washed pastels) | >= 0.6 |
| closest pair of colours, in RGB steps | 44 truecolor / 69 on 256 | 115 / 108 |
| distinct colours on one line of code | 4-6, all pale | 4-6, four clearly different hues |

- **The saturation figure is the one to trust.** Flattening lifts every token
  towards one luminance and drains the hue, which shows up here first; a
  distance check alone can still pass on the coarse 256-colour palette, where
  pale blue and white sit far apart in RGB yet look identical on screen. (That
  is measured, not assumed: the regression's 256-capture scored 69 on distance
  but 0.31 on saturation.)
- The closest-pair and distinct-colour numbers include the card's own chrome
  (the tree prefix and the line-number gutter), which are greys; when in doubt,
  read the per-run list, which shows exactly which token got which colour.
- **Plain text must not be the brightest token.** If punctuation/plain text is
  the brightest thing on the line, emphasis is inverted and the code reads as
  *less* highlighted than the prose around it.

For a non-colour change (layout, wrapping, spacing), skip to step 5 — the
stripped-text diff *is* the evidence, and nothing about colours applies.

Write a unit test for the invariant, not just for today's palette; the shape
that caught this regression is in `references/notes.md`.

### 5. Verify the change did not disturb anything else

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/architecture -count=1
diff <(sed 's/\x1b\[[0-9;]*m//g' /tmp/ab-old.card.ansi) \
     <(sed 's/\x1b\[[0-9;]*m//g' /tmp/ab-new.card.ansi)
```

The stripped-text diff must show **only** the colour change — same card header,
same gutter, same wrapping. A layout delta is an unrequested change.

### 6. Clean up

```bash
$S/cleanup.sh "$TMPDIR/forebrain-ab"
```

Kills the tmux sessions, the fake providers, and removes the HEAD worktree.
Leaving the worktree registered breaks later `git worktree` commands.

## Getting a turn to start

Four points, all measured on this TUI rather than assumed.

- **`"/ commands"` is not proof the composer is mounted**, so `driver.sh start`
  returning (it waits for that string) does not mean keys will land. Wait for the
  composer's `›` marker instead.
- **The workspace-trust page comes before the composer and opens on Quit.**
  Anything typed while it is up goes to the page, and a bare Enter quits
  forebrain. `composer_line` in `drive-card.sh` therefore treats a pane still
  showing `"Trust this directory?"` as having no composer, and `start_session`
  answers that page itself with Down then Enter — `driver.sh` only answers it
  inside its own 15s window, and `drive-card.sh` ignores the failure when it
  misses.
- **Writing to the pane's slave tty (`#{pane_tty}`) does not type.** It only
  prints: the text appears on screen at the cursor, the composer buffer stays
  empty, and Enter does nothing. It looks like it worked, which is what makes it
  a trap. `tmux send-keys` is the only input path.
- **Do not retry the prompt in a loop.** `C-u` is not kill-line in this composer
  and Escape does not wipe the line, so a retry appends to whatever is there and
  the result reads as mangled input (`eieealdeeeeadhleeale`). One send, then
  verify; on a miss, start a fresh session.

### If characters go missing, measure before you blame the harness

An earlier version of this skill asserted that the app never drops input and
that a partial composer always means a harness mistake. That was wrong, and it
cost a lot of time. Typing into a *mounted, empty* composer immediately after it
appeared lost characters in **10 of 10** runs: the composer came back holding
`› e`, or nothing at all. Two startup readers were eating the bytes before the
input pipeline saw them (`probeDarkBackgroundBounded`'s abandoned goroutine, and
lipgloss/termenv's lazy background query); both are fixed, and the same
measurement is now 15 of 15. `references/notes.md` has the mechanism.

The lesson to keep: when input disappears, log what the process actually read
before theorising. A terminal fd has exactly one queue, and anything else in the
process that reads it takes bytes out of the user's hands.

## Why the non-obvious parts are done that way

- **The fixture must be copied into the harness project *after* `driver.sh
  start`, not before.** The driver's `reset` and `start` recreate
  `<work>/proj`, so a file staged earlier is deleted. `drive-card.sh` does the
  copy at the right moment.
- **The 256-colour path needs a wrapper binary, not an env var.** tmux injects
  `COLORTERM=truecolor` into every pane it creates, regardless of the
  environment you launched tmux from. `forebrain` decides truecolor vs 256 from
  `COLORTERM`/`TERM`, so unsetting it in your shell changes nothing. The only
  way to exercise the 256 path is to strip it *inside* the launched process —
  `drive-card.sh` keeps the real binary at `<work>/forebrain.bin` and puts a
  one-line `exec env -u COLORTERM TERM=xterm-256color …` wrapper at
  `<work>/forebrain`, which is the path the driver launches. Test this path: it is
  the one real terminals and screenshots of them actually use, and quantisation
  is where distinct theme colours collapse onto one another.
- **A failed turn is retried as a whole session, never by patching the composer.**
  No clear-line key works here (`C-u` is a no-op and Escape does not wipe the
  line), so a composer holding the wrong text stays that way; the script starts a
  fresh session instead.

## Failure modes

| Symptom | Cause / fix |
|---|---|
| `nothing reached the composer` / `composer never appeared` | the app was still mounting the composer (the splash also paints `"/ commands"`) or died — read `<work>/tui.err`; the script retries with a fresh session |
| `EXIT=0` right after launch | the workspace-trust page was answered with a bare Enter, which quits; it opens on Quit and takes Down then Enter |
| `composer holds › e` (or a bare `›`) after typing | a reader other than the input pipeline is consuming the terminal fd — see "If characters go missing" above |
| `no request reached the provider` | the Enter never landed; check `<work>/provider.log` for `LISTENING` and that no other run holds the port |
| Typed text appears on screen but Enter does nothing | you wrote to the pane's slave tty, which only prints; use `tmux send-keys` |
| `Failed to read <file> … file_path is required` | the tool wants `file_path`, not `path` |
| `open …: no such file or directory` | fixture not copied into `<work>/proj` after `start` |
| Card shows `··· N more lines (click to expand)` instead of the code | tool cards preview only the first few body lines; pass `offset`/`limit` so the interesting lines land in the first three |
| Capture has the same colours for old and new | stale binary — the driver reuses `<work>/forebrain` if it exists; run `build-binary.sh` again |
| **All** of a line's colours print as `38;2;…` even with the `256` depth | the wrapper was not installed, or the driver rebuilt `<work>/forebrain` and overwrote it |
| `driver: fake provider never bound port N` | port still held by an earlier run; use distinct ports per build, or `cleanup.sh` |
| `TestCharacterizationNetworkApproval` fails | pre-existing baseline failure on a machine without network; not caused by a rendering change |
| A Go test fails on `undefined: resolve…Style` after a rename | test helpers call the resolver directly — update the test call site, it is not a production bug |
| Linker error `write() failed, errno=28` | disk full; one build plus its cache is a few hundred MB, so clear space before rebuilding |

## Success looks like

- `extract-tokens.py` on the new capture shows the intended token classes
  separated by clearly distinct colours, in **both** the truecolor and the 256
  capture, with saturation and closest-pair at or past the step-4 thresholds and
  plain text not the brightest.
- The stripped-text diff of the two captures shows no layout change.
- `go test ./pkg/tui` reports only the known baseline failures, and the new
  invariant test fails if you revert the production change (verify this once by
  temporarily restoring the old constant — a guard that cannot fail is not a
  guard).
- Keep the `.ansi` captures as the evidence attached to the change.
