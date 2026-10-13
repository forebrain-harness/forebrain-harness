---
name: tui-row-edge-audit
description: Diagnose TUI bugs where the last character of a full-width row is missing (footer stats 100%/1 instead of 100%/1M, "clipboar") or a card/rule's right edge shows a gap only in some terminals. Covers classifying every \x1b[K emission, fixing by explicit cell painting, and byte-level verification via tmux capture-pane -e -N.
---

# TUI row-edge audit: trailing characters eaten, right-edge gaps

Use when a row's last painted cell disappears (footer, clip notification) or a
background-filled edge shows a gap. The cause is almost never layout: it is a
row producer relying on `\x1b[K` (erase-in-line) for something EL cannot do
portably.

## Terminal semantics you must know first

Two facts about macOS Terminal.app, invisible inside tmux/iTerm2 (both support
BCE and clear the wrap-pending flag on EL):

1. **Deferred wrap + EL erases the last cell.** After a row of exactly the
   terminal width is written, the cursor parks in the final column with the
   wrap pending. A `\x1b[K` emitted there erases *from the cursor* — the just
   painted last character. Symptom: footer `100%/1M` → `100%/1`, `clipboard`
   → `clipboar`.
2. **No back-color-erase (BCE).** `\x1b[K` fills with the *default*
   background, not the currently active SGR background. A row that paints
   `width-1` cells and lets EL "fill the rest" with a card background loses
   the edge on Terminal.app. Symptom: composer rule missing its last chunk.

## Procedure

### 1. Confirm what binary the user actually runs

`npm link` installs a JS launcher; it resolves, in order: installed platform
package → `npm/dist/@forebrain-harness/<pkg>/` → repo `build/bin/forebrain`. Simulate the
resolution (`node -e "require.resolve(...)"` from `npm/`) before debugging code;
`go version -m <binary>` prints the source commit it was built from. Stale
binaries masquerade as render bugs (compare mtime vs HEAD commit time).

### 2. Classify every EL emission, exhaustively

```
grep -rn '\\x1b\[K' pkg/tui --include='*.go' | grep -v _test
```

Sort each hit into exactly one class:

| Class | Pattern | Verdict |
|---|---|---|
| A | full-width content + trailing EL | broken on deferred-wrap terminals: EL eats the last cell. Fix. |
| B | non-default SGR background active + EL as edge fill | broken without BCE: EL fills default background. Fix. |
| C | default background, content ≤ width−1, EL plain clear | safe everywhere. Leave. |
| D | `\x1b[49m` (default bg) before the EL, content ≤ width−1 | safe: both sides agree on default background. Leave. |
| A′ | `\x1b[49m` + trailing EL but content **exactly width** | same deferred-wrap hazard as A despite matching backgrounds — the selection layer once shipped this on the composer footer (drag-select `100%/1M` → `100%/1`). Fix: drop the EL, keep the `49m` close. |

The safety condition behind every class is one rule: **an EL is safe only
when it neither lands on the deferred-wrap cell (content < width) nor fills
a non-default background (class C/D semantics).** Read each call site with
that rule; the grep line alone cannot tell A from C, and background
agreement (D) does not excuse a full-width row (A′).

### 3. Fix by owning every cell — never sniff the terminal

- Class A, exactly-full-width rows (composer footer is flush-right *by
  design*): drop the trailing EL. The viewport paint loop already pre-clears
  every row with `CUP + \x1b[2K` before writing, so a trailing EL is
  redundant bytes plus a hazard.
- Class B rows (card border/prompt/continuation, hover highlight): pad to the
  full width with explicit spaces emitted under the active background
  (`sharedCardPad` / `applyHoverHighlight`). Every cell to the edge is
  painted; no terminal-dependent behavior left.
- Producers that receive an unbounded string then end in EL (preview headers)
  must truncate to `termWidth` minus the fixed chrome, so the EL stays a
  plain clear (class C).

Do not add terminal capability detection; the byte stream must be identical
for every terminal. Keep these invariants: one synthesized row = one physical
row (`fitPaintRow` budget stays `width`, not `width−1`), and the flush-right
footer layout is intentional — do not "fix" it with right padding.

### 4. Sync tests that pin the old bytes

Tests may assert the exact old byte forms (fixture strings with trailing
`\x1b[K`, or copies of the production transformation instead of calls into
it). Update fixtures, and add a byte-contract test for the fixed rows: no EL
in the row, `lipgloss.Width == termWidth`, border ends in an explicit
card-background space cell.

### 5. Flake triage before believing failures

A full-suite failure that passes with `-run` on its own is not your
regression. Confirm with a clean-HEAD worktree running the same full suite:
if it fails there too, report it as a pre-existing order-dependent flake and
move on. One identical retry confirms suspected flakiness; do not loop.

```
git worktree add /tmp/baseline HEAD
cd /tmp/baseline && CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/ -count=1
```

### 6. Verify bytes in tmux, then reason about Terminal.app

Build via the run-forebrain skill (`$D build`, `$D start hang`) and capture with
**both** flags:

```
tmux capture-pane -t <session> -p -e -N | .forebrain/skills/tui-row-edge-audit/scripts/row-bytes.py <termWidth>
```

- `-e` keeps SGR bytes; `-N` keeps trailing spaces — **without `-N` tmux
  trims them and every pad-filled row falsely looks one column short**.
- `row-bytes.py` tracks one continuous SGR state machine across the whole
  capture: tmux re-emits SGR incrementally (attributes still active at a
  row's end are not repeated on the next row), so per-row analysis falsely
  reports missing backgrounds from the second row on. The reported
  `last-sgr` is the state at the last painted cell.
- Pass criteria: rule rows exactly `termWidth` visible columns, zero
  trailing EL on full-width rows, last painted cell's SGR carries the card
  background (`bg=238`).
- tmux cannot reproduce the Terminal.app symptoms (its EL neither erases the
  pending cell nor drops the background). "Cannot reproduce in tmux" proves
  nothing; the byte invariants plus the classification in step 2 are the
  proof for Terminal.app. Ask the owner to confirm on the real terminal.

## Related skills

- `run-forebrain` — driver, fake provider, tmux session mechanics.
- `tui-render-ab-verify` — A/B colour comparison of two builds; use it when
  the question is "did the colour change", not "is the edge intact".

## Failure modes seen here

- Debugging the code when the binary was stale (check step 1 first).
- Capturing without `-N` and "confirming" a missing cell that was only
  trimmed by tmux.
- Treating one class (e.g. only the footer) instead of auditing every EL
  emitter — the same defect class hides in card rows, hover fills and
  preview headers.
