# Plan 393: Keep the user_interaction Other caret visible on wrap, hang its continuation rows under the text column, and stop reflow markers leaking to non-viewport output

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on. If a
> "STOP conditions" item occurs, stop and report — do not improvise. Execute
> **directly on the current working tree** (owner ruling for plan-gated-bugfix:
> no worktree, no snapshot, no commit — changes stay in the working tree for the
> owner). Other agents may be editing this tree concurrently; never revert,
> stash, or commit their work, and scope your own diff to the in-scope files.
> When done, land `plans/393-fix-other-caret-lost-on-soft-wrap.md` (this content)
> and add its index row to `plans/README.md` (DONE + acceptance date), then
> backfill the acceptance record.
>
> **Drift check (run first)**: `git rev-parse --short HEAD` (planned at
> `16322dcf`) and `git status --porcelain -- pkg/tui/overlays.go pkg/tui/reducer.go pkg/tui/overlays_test.go`.
> Record the baseline `git status --porcelain` (other agents' files may appear).
> Compare the "Current state" excerpts against the live code before editing; on a
> content mismatch (not mere line drift) STOP.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED (touches the shared soft-wrap reflow and adds a wire marker; the
  marker must never reach a `c.lines` consumer — both the viewport reflow and the
  non-viewport fallback strip it)
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `16322dcf`, 2026-11-27

## Why this matters

In the TUI `user_interaction` question panel the "Other" free-text answer is
edited inline. The owner's screenshot of a long CJK answer shows two defects, and
investigation found a third in the same rendering path:

1. **The caret disappears when the text soft-wraps** — the user cannot see where
   typing lands.
2. **Wrapped continuation rows fall to column 0** instead of hanging under the
   answer's text column.
3. **Reflow markers leak to non-viewport output**: `finishCollector`'s fallback
   writes `c.lines` verbatim, so a piped (non-TTY) run prints the raw `\x00QSEP\x00`
   separator marker today, and would print the new hang marker too.

Owner rulings (2026-11-27): fix all three in this pass. The stray `Å` glyph at the
wrap point in the screenshot is to be confirmed visually by the owner after the
caret fix (its terminal mechanism is unverified — see below), not pursued as a
separate root cause unless it persists.

## Current state

Files and roles:

- `pkg/tui/overlays.go` — the question overlay (`questionOverlay`), the Other
  field (`renderOtherRow`, `otherCaretCell`, `placeOtherCaret`), the line collector
  (`qlineCollector`) and `finishCollector` (viewport vs non-viewport output).
- `pkg/tui/reducer.go` — the viewport paint and the soft-wrap reflow
  (`softWrapOverlayLine`, `overlayWrapPrefix`, `overlaySoftWrapRefs`).
- `pkg/tui/overlays_test.go` — home of the existing Other-caret tests and the new
  regression tests.
- Vendored `github.com/charmbracelet/x/ansi v0.11.9` (`wrap.go`) — the wrapper.
  **Do not edit the vendor.**

### Defect 1 — the caret cell is breakable whitespace

The Other field draws its caret as one reverse-video cell inserted into the
logical line (`pkg/tui/overlays.go:3097-3100`):

```go
// otherCaretCell is the reverse-video cell the Other field draws at its caret.
// It is one column of decoration inside the text, which is why a click past it
// is shifted back by its width before it is resolved to a rune offset.
const otherCaretCell = "\x1b[7m \x1b[0m"
```

`renderOtherRow` writes `prefix + before + otherCaretCell + after` as ONE logical
line (`pkg/tui/overlays.go:3439-3448`):

```go
o.otherRow = len(c.lines)
o.otherPrefixWidth = lipgloss.Width(prefix)
c.writeLine(prefix + before + otherCaretCell + after)
...
c.caret = softWrapCaret{Set: true, Line: o.otherRow, Col: o.otherPrefixWidth + lipgloss.Width(before)}
```

The question overlay installs its lines through the soft-wrapping path with
`cursorRow = -1` (`overlays.go:3667`) — the paint draws no caret of its own, so the
visible caret is exactly the embedded `otherCaretCell`. That path reflows every
logical line at paint time (`reducer.go:6864-6900`, `softWrapOverlayLine`) via
`xansi.WrapWc(line[len(prefix):], contentWidth, "")`, `contentWidth = termWidth - 2`
(`reducer.go:5356`).

**Root cause (proven):** `xansi.WrapWc` (v0.11.9 `wrap.go`, `wrap()`) treats the
caret cell's **space** as breakable whitespace: a space flushes the pending word
and is buffered in `space`; `addNewline()` then does `space.Reset()`
(`wrap.go:334`), discarding it. The `\x1b[7m` rides with the preceding word, the
space is dropped at the break, the `\x1b[0m` lands on the next row. Whenever a
wrap break lands on the caret cell, the visible cell vanishes and a dangling
`\x1b[7m` is left at the row end.

### Defect 2 — continuation rows have no hang indent

`softWrapOverlayLine` derives the continuation indent from the line's literal
leading prefix (`reducer.go:6906-6922`, `overlayWrapPrefix`): it strips leading
spaces and a list bullet (`· ↳ • -`); continuation rows carry spaces as wide as
that prefix. Option **description** rows lead with literal spaces
(`renderOptionRow`, `overlays.go:3400`, `pad := strings.Repeat(" ", idxWidth+4)`),
so they hang correctly. The Other editing line begins with an ANSI-styled pointer
(`\x1b[36m❯\x1b[0m …`), so `overlayWrapPrefix` finds no leading space and returns
`prefix="", continuation=""` — continuation rows fall to column 0.

### Defect 3 — reflow markers leak to the non-viewport fallback

`finishCollector` (`pkg/tui/overlays.go:3665-3674`):

```go
func (o *questionOverlay) finishCollector(c *qlineCollector, top int) {
	if o.rs != nil && o.rs.inViewportOverlay() {
		o.rs.setSoftWrappingOverlayComposer(c.lines, -1, 0, top, softWrapFocus{...})
		return
	}
	// Non-viewport fallback: write lines directly.
	for _, line := range c.lines {
		_, _ = fmt.Fprintln(o.out, line)
	}
}
```

The separator is the in-band marker `\x00QSEP\x00` (`questionSeparatorLine`,
`overlays.go:3691`), expanded only inside `softWrapOverlayLine` (`reducer.go:6869`).
Viewport mode is on only for a TTY (`run.go:705-712`), so a piped run writes the
raw marker. The QSEP marker is NUL bytes, which terminals drop — so this
pre-existing leak has no visible symptom today; the new `\x00HANG\x00` marker,
however, contains the letters "HANG" and would print visibly. Two existing tests
render through this fallback and assert on the buffer:
`chat_session_test.go:15662` wants `"Other: dddd"+otherCaretCell`, and `:15788`
wants `"Other: first answer"` after `stripANSI` (which drops NULs but keeps
letters, `tty_session.go:241-243`). Any new in-band marker must be stripped here
or it leaks and breaks these tests.

### Reproduction (run before changing anything)

Unit-level, phantom test via `go test -overlay` (zero working-tree writes). Save
as `/tmp/repro393_test.go`:

```go
package tui

import (
	"fmt"
	"strings"
	"testing"
)

func TestRepro393CaretLost(t *testing.T) {
	prefix := fmt.Sprintf("%s\x1b[2m%s\x1b[0m Other: ", "\x1b[36m❯\x1b[0m ", "4.")
	text := "移到 Agents & tools – Agents – Agent definitions节，既可以新建主代理又可以新建子代理，配置项是完全一致的，只通过primary配置项区分主代理还是子代理，primary为true表示主代理，primary为false表示子代理"
	rows := softWrapOverlayLine(prefix+text+otherCaretCell, 159) // caret at end
	if strings.Contains(strings.Join(rows, "\n"), "\x1b[7m ") {
		t.Fatalf("caret survived; bug not reproduced")
	}
	t.Logf("caret lost; last row=%q", rows[len(rows)-1])
}

func TestRepro393ContinuationAtCol0(t *testing.T) {
	prefix := fmt.Sprintf("%s\x1b[2m%s\x1b[0m Other: ", "\x1b[36m❯\x1b[0m ", "4.")
	text := "移到 Agents & tools – Agents – Agent definitions节，既可以新建主代理又可以新建子代理，配置项是完全一致的，只通过primary配置项区分主代理还是子代理，primary为true表示主代理，primary为false表示子代理"
	rows := softWrapOverlayLine(prefix+text+otherCaretCell, 60)
	if len(rows) < 2 || strings.HasPrefix(stripAnsi(rows[1]), " ") {
		t.Fatalf("expected a col-0 continuation; got %q", rows)
	}
	t.Logf("continuation at col0: %q", rows[1])
}
```

```bash
printf '{"Replace":{"%s/pkg/tui/zz_repro393_test.go":"/tmp/repro393_test.go"}}' "$PWD" > /tmp/ov393.json
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
CGO_ENABLED=1 go test -overlay /tmp/ov393.json -tags fts5 ./pkg/tui -run TestRepro393 -count=1 -v
```

Expected (bugs present): both PASS their `t.Logf` branches — caret lost (last row
`…子代理\x1b[7m\x1b[0m`), continuation at column 0. (An all-position sweep loses
the caret in ~13k of ~24k caret×width combinations.)

Real-machine before-fix evidence (isolated home, `run-forebrain` driver `ask`
mode): at terminal 161 (contentWidth 159) no Other row carries `\x1b[7m`; control
cases — short text `abc` shows `Other: abc\x1b[7m \x1b[0m`, and the same long text
at 118 columns shows the caret at the last row's end.

### The stray `Å` (unverified, not load-bearing)

The screenshot shows a stray `Å` at the wrap point, exactly where the caret cell
was dropped. A dangling SGR with no following cell normally draws nothing (tmux
shows no glyph), so its precise Terminal.app mechanism is **unverified**. Owner
ruling: land the caret fix and confirm visually whether the `Å` is gone; if a
stray glyph persists at the wrap point after the fix, that is a second cause —
STOP and report (do not expand this plan to chase it).

## Fix design

### Fix A — make the caret cell non-breaking

Replace the caret cell's space with a no-break space (U+00A0). `xansi.WrapWc`
exempts nbsp from whitespace breaking (`wrap.go:348`,
`case r != utf8.RuneError && unicode.IsSpace(r) && r != nbsp:`), so the caret cell
becomes part of the surrounding word and is never the whitespace a break
consumes. The `\x1b[7m` bytes and the nbsp stay atomic (no flush point between
them: ESC bytes take the action-default branch with no width check; a hard-wrap
flush only happens after a printable rune). The trailing `\x1b[0m` **can**
separate onto the next row when the nbsp completes a full word (`wordLen == limit`
flushes before the `\x1b[0m` is appended); the block still renders because
`selfContainedRows` (`render.go:118`) closes the open SGR at row end and carries it
to the next. nbsp is one display column, so all column arithmetic is unchanged:

- `softWrapCaret.Col` (`overlays.go:3448`) counts `before` only — unchanged.
- `overlaySoftWrapRefs` / `plainWrapRowStarts` walk `stripAnsi` of the line; nbsp
  is preserved by `stripAnsi` (= `sanitizeTerminalInputText`, `tty_session.go:195`)
  on both sides — consistent.
- Click-to-caret `placeOtherCaret` → `caretIndexAroundBlock(buf, cursor,
  prefixWidth, 1, col)` (`overlays.go:3109`): the cell is still one column —
  unchanged.
- Selection/copy read `o.otherBuf`, never the rendered cell — unchanged.

Change the constant + comment (`pkg/tui/overlays.go:3097-3100`):

```go
// otherCaretCell is the reverse-video cell the Other field draws at its caret.
// It is one column of decoration inside the text, which is why a click past it
// is shifted back by its width before it is resolved to a rune offset. The glyph
// is a no-break space, not a plain space: the soft-wrap reflow (xansi.WrapWc)
// consumes breakable whitespace at a line break, so a plain space here was
// dropped whenever the break landed on the caret and the caret vanished (owner
// 2026-11-27). nbsp is non-breaking, so the cell survives every wrap position.
const otherCaretCell = "\x1b[7m\u00A0\x1b[0m"
```

### Fix B — hang the Other continuation rows under the text column

Teach the reflow the Other line's styled hang prefix via a NUL marker, reusing the
existing `\x00QSEP\x00` convention. The question overlay declares where the prefix
ends; the generic reflow honors it; **every `c.lines` consumer strips the marker**
(the viewport reflow in Fix B.4/B.5, the non-viewport fallback in Fix B.6).

1. Define the markers in `pkg/tui/reducer.go` near `softWrapOverlayLine`. Converge
   the existing inline QSEP literal to a named constant at the same time:

```go
// overlaySepMarker is the question overlay's separator line, expanded to a
// full-width rule by softWrapOverlayLine at the current terminal width.
const overlaySepMarker = "\x00QSEP\x00"

// overlayHangMarker separates a wrapped overlay line's styled label prefix from
// its wrappable text. The prefix before it sets the column continuation rows
// hang under (its display width); the marker itself is dropped from the content.
// Emitted by the question overlay's Other field, whose line begins with an
// ANSI-styled pointer that overlayWrapPrefix's plain leading-space scan cannot
// measure. Like overlaySepMarker it must be stripped by every c.lines consumer.
const overlayHangMarker = "\x00HANG\x00"
```

2. `renderOtherRow` (`overlays.go:3441`) inserts the marker between the styled
   prefix and the text:

```go
c.writeLine(prefix + overlayHangMarker + before + otherCaretCell + after)
```

3. `overlayWrapPrefix` (`reducer.go:6906`) becomes marker-aware (the space/bullet
   path is unchanged):

```go
func overlayWrapPrefix(line string) (prefix, continuation string) {
	if i := strings.Index(line, overlayHangMarker); i >= 0 {
		prefix = line[:i]
		return prefix, strings.Repeat(" ", displayLineWidth(prefix))
	}
	// … existing leading-space + bullet scan, unchanged …
}
```

4. `softWrapOverlayLine` (`reducer.go:6883-6891`) measures the prefix with the
   ANSI-aware `displayLineWidth` and drops the marker from the content. Also use
   `overlaySepMarker` in the QSEP check (`reducer.go:6869`):

```go
if line == overlaySepMarker { … unchanged rule expansion … }
prefix, continuation := overlayWrapPrefix(line)
contentWidth := width - displayLineWidth(prefix)
if contentWidth < 1 {
	prefix, continuation, contentWidth = "", "", width
}
content := strings.ReplaceAll(line[len(prefix):], overlayHangMarker, "")
wrapped := strings.Split(xansi.WrapWc(content, contentWidth, ""), "\n")
// … row0 = prefix + wrapped[0]; rows i>0 = continuation + wrapped[i], unchanged …
```

(`displayLineWidth` (`render.go:5274`) strips SGR then measures; for a literal
space prefix it equals the old `runewidth.StringWidth`, so description/option
wrapping is byte-identical. The `contentWidth < 1` fallback still strips the
marker via `ReplaceAll`.)

5. `overlaySoftWrapRefs` (`reducer.go:6837`) drops the marker before stripping ANSI
   so the plain text matches the rows, **and takes the effective width so its
   continuation agrees with the wrap at every width** (see below):

```go
func overlaySoftWrapRefs(logical string, rows []string, lineIdx, width int) []overlayRowRef {
	prefix, continuation := overlayWrapPrefix(logical)
	if width-displayLineWidth(prefix) < 1 {
		continuation = "" // match softWrapOverlayLine's contentWidth<1 fallback
	}
	plain := stripAnsi(strings.ReplaceAll(logical, overlayHangMarker, ""))
	// … rest unchanged …
}
```

   Update its sole production caller `overlayContentLinesLocked`
   (`reducer.go:6822`) to `overlaySoftWrapRefs(line, wrapped, i, width)`.

   **Why width-aware (a regression Fix B would otherwise introduce):** below the
   prefix's own width, `softWrapOverlayLine`'s `contentWidth < 1` fallback drops
   the prefix and wraps the whole line with no hang. `overlaySoftWrapRefs` derives
   the continuation independently; without the width it would still report the
   12-column hang while the rows carry none, so a click on a continuation row
   would resolve up to 12 columns off. Before Fix B the Other line had `prefix=""`
   at every width, so rows and refs always agreed. Passing the width and applying
   the same fallback keeps them consistent at **all** widths (proven by prototype
   at widths 4–220).

6. `finishCollector`'s non-viewport fallback (`overlays.go:3670-3673`) strips both
   reflow markers so a piped run never prints them (Fix C — the QSEP leak is
   pre-existing; owner ruled to fix it here):

```go
for _, line := range c.lines {
	line = strings.ReplaceAll(line, overlayHangMarker, "")
	line = strings.ReplaceAll(line, overlaySepMarker, "")
	_, _ = fmt.Fprintln(o.out, line)
}
```

Also update `questionSeparatorLine` (`overlays.go:3691`) to `return overlaySepMarker`.

**Why clicks stay correct (proven by prototype):** `plainWrapRowStarts`
(`reducer.go:8671`) walks the plain buffer text and so counts the whitespace the
wrap consumes — `logicalCol` is a buffer display offset, not a sum of displayed
row widths. A click on a continuation row at visual column `c` resolves to
`column = ref.logicalCol + c - ref.rowCol` (`reducer.go:7626`);
`caretIndexAroundBlock` subtracts `prefixWidth`, yielding the correct buffer rune.
With `rowCol = prefixWidth` and `logicalCol = prefixWidth + bufferOffset`, the
prefix cancels exactly. A phantom prototype verified this with `buf = []rune(text)`
(the caret cell is not stored in the buffer): rows/refs agreement at widths
4–220, and hang + no-overflow + click-to-rune mapping from `pw+2`–220 (below
`pw+2` a wide rune cannot fit a 1-cell budget — the pre-existing vendor overflow
noted in Maintenance). Description lines wrap byte-identically and the caret cell
survives.

### Behavior matrix

| Scenario | Old | New | Kind |
|---|---|---|---|
| Other text short (no wrap) | caret block visible | caret block visible | unchanged |
| Long text, last row has room | caret block at end | caret block at end | unchanged |
| Long text, break lands on caret | **caret gone**, dangling `\x1b[7m` | caret block visible (row end / continuation start / own row) | **bug fix A** |
| Other wrapped continuation rows | fall to column 0 | hang under the text column (`otherPrefixWidth` spaces) | **bug fix B** |
| Piped (non-TTY) question output | carries raw `\x00QSEP\x00` bytes (NULs terminals drop, so no visible symptom today); the new `\x00HANG\x00` would print visible "HANG" | markers stripped, clean text | **bug fix C** |
| Click-to-caret on a wrapped Other row | resolves via refs | identical (proven) | unchanged |
| Arrow keys / selection / copy / scroll-into-view | buffer-based | identical | unchanged |
| Option description / header / option wrapping | literal-space hang | byte-identical | unchanged |
| Nav chips | reverse-video text survives; inter-chip padding wraps normally | identical (untouched) | unchanged |
| ASCII text, caret mid-word | caret space acted as a word break | caret no longer a break point; wrap may shift one cell | deliberate, benign |
| Stray `Å` at wrap point (screenshot) | present (mechanism unverified) | owner confirms visually; if persists → STOP (second cause) | verification, not a code claim |

## Scope

**In scope** (the only files to modify):
- `pkg/tui/overlays.go` — `otherCaretCell` constant + comment; `renderOtherRow`
  inserts `overlayHangMarker`; `finishCollector` fallback strips both markers;
  `questionSeparatorLine` returns `overlaySepMarker`.
- `pkg/tui/reducer.go` — `overlayHangMarker`/`overlaySepMarker` consts;
  `overlayWrapPrefix`, `softWrapOverlayLine`, `overlaySoftWrapRefs` marker handling.
- `pkg/tui/overlays_test.go` — new regression tests (add functions; do not rewrite
  existing tests).
- `plans/393-fix-other-caret-lost-on-soft-wrap.md` (create post-approval) and
  `plans/README.md` (append index row + dated entry).

**Out of scope** (do NOT touch):
- Vendored `github.com/charmbracelet/x/ansi` — never edit the vendor.
- `softwareCursorCell` / composer / one-line editor carets — a different, working
  path.
- The stray `Å`'s terminal mechanism — owner confirms visually; pursue only if it
  persists after this fix (then STOP and report).
- The public `SetSoftWrappingOverlayComposer` / `setOverlayComposer` signatures and
  the `qlineCollector`/renderer structs — the marker stays in-band precisely to
  avoid plumbing a new out-of-band channel (which would churn the three test
  callers of `softWrapOverlayLine`).
- Any other overlay's wrapping, key handling, or the `softWrapCaret` scroll contract.
- Other agents' concurrent edits anywhere in the tree.

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| Focused tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestOtherCaret\|TestOtherContinuation\|TestOverlayDescription\|TestQuestionOverlayFallback' -count=1` | pass |
| Package regression | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/turn ./pkg/state -count=1` | ok (pkg/tui ~200s) |
| Vet (CI) | `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0 |
| Format | `gofmt -l pkg/tui` | nothing |
| Build (fts5 mandatory) | `CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` | exit 0 |

`export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`
when calling `go` directly. `no such module: fts5` means a missed `-tags fts5` — a
toolchain error, not a product bug.

## Steps

### Step 1: Add regression tests, confirm RED

In `pkg/tui/overlays_test.go`, model helper usage on the existing
`TestQuestionOverlayOtherCaretStaysOnScreenWhileTyping` (`overlays_test.go:1136`)
and the non-viewport render pattern at `chat_session_test.go:15662`
(`o.out = buf; o.render()`).

(a) Caret survives the reflow at **every** caret position and width, on the real
Other line (with the hang marker), asserting on `selfContainedRows` output:

```go
func TestOtherCaretCellSurvivesSoftWrap(t *testing.T) {
	prefix := "\x1b[36m❯\x1b[0m \x1b[2m4.\x1b[0m Other: "
	text := "移到 Agents & tools – Agents – Agent definitions节，既可以新建主代理又可以新建子代理，配置项是完全一致的，只通过primary配置项区分主代理还是子代理，primary为true表示主代理，primary为false表示子代理"
	runes := []rune(text)
	for ci := 0; ci <= len(runes); ci++ {
		line := prefix + overlayHangMarker + string(runes[:ci]) + otherCaretCell + string(runes[ci:])
		for width := 40; width <= 220; width++ {
			rows := selfContainedRows(softWrapOverlayLine(line, width))
			caret := false
			for _, row := range rows {
				if strings.HasSuffix(row, "\x1b[7m") {
					t.Fatalf("caret=%d width=%d: dangling reverse-video escape:\n%q", ci, width, row)
				}
				if w := lipgloss.Width(row); w > width {
					t.Fatalf("caret=%d width=%d: row wider than width (%d):\n%q", ci, width, w, row)
				}
				if strings.Contains(row, "\x1b[7m\u00A0") {
					caret = true
				}
			}
			if !caret {
				t.Fatalf("caret=%d width=%d: caret cell lost:\n%q", ci, width, rows)
			}
		}
	}
}
```

(b) Continuation rows hang under the text column, no overflow, and clicks map —
**from `pw+2` up**, where the reflow guarantees it. Below `pw+2` a wide rune cannot
fit a 1-cell content budget and `xansi.WrapWc` may overflow (a **pre-existing**
vendor property: the pristine tree overflows too), so there only rows/refs
agreement is asserted — the property Fix B owns — with no silent skip:

```go
func TestOtherContinuationHangsUnderTextColumn(t *testing.T) {
	prefix := "\x1b[36m❯\x1b[0m \x1b[2m4.\x1b[0m Other: "
	pw := lipgloss.Width(prefix)
	text := "移到 Agents & tools – Agents – Agent definitions节，既可以新建主代理又可以新建子代理，配置项是完全一致的，只通过primary配置项区分主代理还是子代理，primary为true表示主代理，primary为false表示子代理"
	line := prefix + overlayHangMarker + text + otherCaretCell
	buf := []rune(text) // the field stores the text only, never the caret cell
	prefixPlain := stripAnsi(prefix)
	// cont mirrors softWrapOverlayLine: the prefix hangs when contentWidth >= 1.
	contFor := func(width int) string {
		if width-pw >= 1 {
			return strings.Repeat(" ", pw)
		}
		return ""
	}
	// Narrow widths (4..pw+1): only rows/refs agreement is guaranteed. No
	// hang/overflow/click claim below contentWidth 2 (pre-existing wide-rune
	// overflow in xansi.WrapWc).
	for width := 4; width <= pw+1; width++ {
		rows := softWrapOverlayLine(line, width)
		refs := overlaySoftWrapRefs(line, rows, 0, width)
		cont := contFor(width)
		for i := 1; i < len(rows); i++ {
			if refs[i].rowCol != lipgloss.Width(cont) {
				t.Fatalf("narrow width=%d row%d rows/refs disagree: rowCol=%d cont=%q", width, i, refs[i].rowCol, cont)
			}
		}
	}
	// width >= pw+2: hang under the text column, no overflow, clicks map.
	for width := pw + 2; width <= 220; width++ {
		rows := softWrapOverlayLine(line, width)
		if len(rows) < 2 {
			continue
		}
		cont := contFor(width) // == spaces(pw) here
		if !strings.HasPrefix(rows[0], prefix) || lipgloss.Width(rows[0]) > width {
			t.Fatalf("width=%d row0 bad: %q", width, rows[0])
		}
		refs := overlaySoftWrapRefs(line, rows, 0, width)
		plain := stripAnsi(strings.ReplaceAll(line, overlayHangMarker, ""))
		contents := make([]string, len(rows))
		for i, row := range rows {
			c := stripAnsi(row)
			if i > 0 {
				c = strings.TrimPrefix(c, cont)
			}
			contents[i] = c
		}
		starts := plainWrapRowStarts(plain, contents)
		for i := 1; i < len(rows); i++ {
			if !strings.HasPrefix(rows[i], cont) || lipgloss.Width(rows[i]) > width {
				t.Fatalf("width=%d row%d hang/overflow: %q", width, i, rows[i])
			}
			if refs[i].rowCol != pw {
				t.Fatalf("width=%d row%d rowCol=%d want %d", width, i, refs[i].rowCol, pw)
			}
			want := starts[i] - len([]rune(prefixPlain))
			if got := caretIndexAroundBlock(buf, len(buf), pw, 1, refs[i].logicalCol); got != want {
				t.Fatalf("width=%d row%d click first cell: rune %d want %d", width, i, got, want)
			}
		}
	}
}
```

(c) Description wrapping is unchanged (guard the shared reflow):

```go
func TestOverlayDescriptionWrapUnchangedByHangMarker(t *testing.T) {
	desc := "     轮询间隔 2 秒，最多 60 秒。兼顾安装耗时与网关等待，请求快但较慢的安装会超时等待充分但请求长时间占用连接"
	for width := 20; width <= 120; width++ {
		rows := softWrapOverlayLine(desc, width)
		for i := 1; i < len(rows); i++ {
			if !strings.HasPrefix(rows[i], "     ") {
				t.Fatalf("width=%d row%d lost its 5-space hang: %q", width, i, rows[i])
			}
		}
	}
}
```

(d) The non-viewport fallback strips reflow markers (Fix B.6 / Fix C). Uses the
existing helpers `newTestOverlay` (`chat_session_test.go:14972`, builds an overlay
with `rs == nil` → non-viewport) and `twoQuestionForm` (`:15005`, 2 options per
question, so Down×2 reaches Other; **two** questions so the per-question separator
at `overlays.go:3283` makes the QSEP half non-vacuous):

```go
func TestQuestionOverlayFallbackStripsReflowMarkers(t *testing.T) {
	o := newTestOverlay(twoQuestionForm())
	o.handleKey(parsedKey{kind: rawKeyDown})
	o.handleKey(parsedKey{kind: rawKeyDown})
	for _, r := range "dddd" {
		o.handleKey(parsedKey{kind: rawKeyRune, r: r})
	}
	buf := &bytes.Buffer{}
	o.out = buf
	o.render()
	s := buf.String()
	if !strings.Contains(s, "Other: dddd"+otherCaretCell) {
		t.Fatalf("Other row rendered without a caret:\n%s", s)
	}
	for _, tok := range []string{"\x00HANG\x00", "HANG", "\x00QSEP\x00", "QSEP", "\x00"} {
		if strings.Contains(s, tok) {
			t.Fatalf("reflow marker leaked %q:\n%s", tok, s)
		}
	}
}
```

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestOtherCaretCellSurvivesSoftWrap|TestOtherContinuationHangsUnderTextColumn|TestQuestionOverlayFallbackStripsReflowMarkers' -count=1`
→ (a), (b), (d) **FAIL** (caret lost / continuation at col 0 / marker leaks). (c)
passes (guards existing behavior). If (a) or (b) passes before any production
change, STOP — check (a) matches `\x1b[7m\u00A0` (not a bare `\x1b[7m`, which
false-positives on the bug's dangling residue).

> **Bootstrap to make Step 1 compile and genuinely RED:** the tests reference
> `overlayHangMarker`/`overlaySepMarker` and call `overlaySoftWrapRefs(line, rows,
> 0, width)` (4 args). Before running the tests, apply ONLY these production
> pieces (they are inert until `overlayWrapPrefix` learns the marker and
> `otherCaretCell` becomes nbsp, so behavior is still the bug):
> - the two consts (Step 2.1);
> - `overlaySoftWrapRefs`'s width parameter + mirrored `contentWidth < 1`
>   fallback + `ReplaceAll` plain (Step 2.3, the B.5 part);
> - its caller `overlayContentLinesLocked` (`reducer.go:6822`) passing `width`.
>
> Then run the tests: (a)/(b)/(d) must FAIL on **behavior** (`caret cell lost`,
> `rows/refs disagree`, `reflow marker leaked "\x00QSEP\x00"`), not on compilation.
> If the package fails to build, the bootstrap is incomplete — finish it before
> judging RED. If (a) or (b) passes on behavior, STOP (premise false; check (a)
> matches `\x1b[7m\u00A0`).

### Step 2: Apply the fixes

(The consts and the `overlaySoftWrapRefs` width signature/fallback/caller were
already applied in the Step 1 bootstrap; finish the rest here.)

1. `pkg/tui/reducer.go`: add `overlaySepMarker` + `overlayHangMarker` consts (B.1)
   — if not already present from the bootstrap.
2. `pkg/tui/overlays.go`: `otherCaretCell` space → `\u00A0` + comment (Fix A);
   `renderOtherRow` inserts `overlayHangMarker` (B.2); `questionSeparatorLine`
   returns `overlaySepMarker`; `finishCollector` fallback strips both markers (B.6).
3. `pkg/tui/reducer.go`: `overlayWrapPrefix` marker branch (B.3);
   `softWrapOverlayLine` `overlaySepMarker` check + `displayLineWidth` +
   `ReplaceAll` content (B.4). (B.5 — `overlaySoftWrapRefs` width param +
   `ReplaceAll` plain + `contentWidth<1` fallback — and its caller were applied in
   the Step 1 bootstrap.)

**Verify**: Step-1 tests (a)/(b)/(c)/(d) all **PASS** (same command).

### Step 3: Regression + lint

**Verify**:
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/turn ./pkg/state -count=1` → ok.
  These existing tests MUST pass (they exercise the changed paths):
  `TestQuestionOverlayOtherCaretStaysOnScreenWhileTyping`,
  `TestQuestionOtherClickPlacesCaret`,
  `TestQuestionOverlayOtherFieldKeepsCaretAcrossTabs`,
  `TestQuestionOverlayOtherTextIsolatedAcrossQuestionsAndSubmission`
  (`chat_session_test.go:15662`/`:15788` — the fallback-leak regressions),
  `TestQuestionHeaderChipWrapsAtTheTerminalWidth`, and the `softWrapOverlayLine`
  tests at `reducer_test.go:3851/5359/5375`.
- `CGO_ENABLED=1 go vet -tags fts5 ./...` → exit 0.
- `gofmt -l pkg/tui` → nothing.

### Step 4: Real-machine acceptance (tmux, isolated home)

Build with fts5; drive the `run-forebrain` driver `ask` mode in an isolated work
dir/session/port (never the real `~/.forebrain`). **The driver's `rows`/`col`/bare
`caret` commands scan for the `›` composer row, which the overlay replaces — they
cannot find the Other row.** Locate rows with `tmux capture-pane -t fb-caret -p |
grep -n 'Other'` and read the painted caret glyph with `$D caret <row>`.

```bash
export FOREBRAIN_RUN_WORK=$TMPDIR/forebrain-run-caret FOREBRAIN_RUN_SESSION=fb-caret FOREBRAIN_RUN_PORT=8763
D=.claude/skills/run-forebrain/driver.sh
$D build           # rebuild after the Go change — start reuses a stale binary otherwise
$D start ask && $D submit 'polling question please' && $D wait 'Other' 30
tmux capture-pane -t fb-caret -p | grep -n 'Other'   # find the Other row; arrow until ❯ is on it
$D send '移到 Agents & tools – Agents – Agent definitions节，既可以新建主代理又可以新建子代理，配置项是完全一致的，只通过primary配置项区分主代理还是子代理，primary为true表示主代理，primary为false表示子代理'
tmux resize-window -t fb-caret -x 161 -y 42
```

Acceptance scenes (capture with `capture-pane -p -e`; the caret at text end sits
on a continuation row that does NOT contain "Other:", so scan the WHOLE pane):

1. **Caret present (fix A)**: `tmux capture-pane -t fb-caret -p -e | grep -n $'\x1b\[7m'`
   shows a reverse-video cell on an Other row (exclude ☐/✔ nav-chip rows);
   `$D caret <row>` reads the glyph and confirms the nbsp renders as one cell.
   Sweep widths 118/159/161/200; a caret cell is present at each.
2. **Caret advances (position check)**: note the caret cell's column, type one
   more character, re-capture — the caret advances one cell.
3. **Continuation hang (fix B)**: the wrapped Other continuation rows begin with
   `otherPrefixWidth` spaces (align under the answer text), not column 0.
4. **Regression**: short text `abc` shows the caret; a click on a wrapped Other
   row (`$D click <col> <row>`, coordinates from the capture) places the caret;
   Enter confirms, Esc discards; arrow keys move the caret.
5. **Interrupt self-rescue**: Esc out of the field and overlay cleanly; relaunch
   reaches the composer.
6. **Å visual check**: capture the wrap-point bytes; the dangling `\x1b[7m`
   residue is gone. The owner confirms visually in Terminal.app whether the stray
   `Å` is gone. If a stray glyph persists at the wrap point, STOP (second cause).
7. Clean up: `$D stop`, `tmux kill-session -t fb-caret`, remove the temp work dir.

If a stale provider from an earlier attempt holds the port, kill that listener
(`lsof -ti :8763 | xargs kill`) before `start` — it is this run's own process.

### Step 5: Land the plan and backfill

- Create `plans/393-fix-other-caret-lost-on-soft-wrap.md` with this content.
- Append the `plans/README.md` index row (393, P1, M, —, DONE + date) and a dated
  entry (root causes + acceptance).
- Add an "验收记录" section (scene → evidence, toolchain pits hit).
- Leave source changes uncommitted in the working tree (owner ruling).

## Test plan

- New (in `pkg/tui/overlays_test.go`): `TestOtherCaretCellSurvivesSoftWrap` (all
  caret positions × widths 40–220, real Other line with marker, on
  `selfContainedRows` output), `TestOtherContinuationHangsUnderTextColumn` (hang
  indent + click mapping, `buf = []rune(text)`),
  `TestOverlayDescriptionWrapUnchangedByHangMarker` (shared-reflow guard),
  `TestQuestionOverlayFallbackStripsReflowMarkers` (non-viewport no-leak).
- (a)/(b)/(d) must be RED before Step 2 and GREEN after. Do not weaken existing
  assertions; the existing Other-caret assertions reference the constant or the
  reverse-video cell generically and stay valid.
- Broader: `pkg/tui ./pkg/turn ./pkg/state` + `go vet` + `gofmt` (Step 3).

## Done criteria

ALL must hold:

- [ ] Step-1 (a)/(b)/(d) fail before the production change and pass after.
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/turn ./pkg/state -count=1` exits 0, including the two fallback-leak regressions (`chat_session_test.go:15662`/`:15788`).
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./...` exits 0; `gofmt -l pkg/tui` prints nothing.
- [ ] `grep -n 'otherCaretCell = ' pkg/tui/overlays.go` shows `\u00A0`.
- [ ] `grep -n 'overlayHangMarker\|overlaySepMarker' pkg/tui/overlays.go` shows exactly 4 lines: 1 emit (`renderOtherRow`), 2 strips (`finishCollector` fallback), 1 separator return (`questionSeparatorLine`); test (d) proves the behaviour.
- [ ] Real-machine: a reverse-video caret cell is present on an Other row at the previously-failing width (161) and across the sweep; the caret advances one cell on typing; continuation rows hang under the text column.
- [ ] The diff **made by this executor** (against the baseline `git status`) is confined to `pkg/tui/overlays.go`, `pkg/tui/reducer.go`, `pkg/tui/overlays_test.go` (plus `plans/`); nothing committed; other agents' files untouched.
- [ ] `plans/393-...md` exists and `plans/README.md` carries its row.

## STOP conditions

Stop and report (do not improvise) if:

- The "Current state" excerpts do not match live code (content drift).
- Step-1 (a) or (b) passes before any production change (premise false; first
  check (a) matches `\x1b[7m\u00A0`).
- A step's verification fails twice after a reasonable fix attempt.
- The fix appears to require an out-of-scope file (vendor, other overlays, public
  signatures).
- The key assumptions are falsified: "WrapWc keeps the `\x1b[7m`+nbsp cell atomic"
  (caret still lost / dangling escape after Fix A); "the hang marker preserves
  click mapping and description wrapping" (test (b)/(c) fails after Fix B); "rows
  and refs agree at every width" (test (b) fails at a narrow width after Fix B.5);
  or "the marker never reaches a c.lines consumer" (a fallback/viewport test still
  sees HANG/QSEP after Fix B.6).
- The real-machine caret is still absent at 161 columns after the fix.
- A stray glyph persists at the wrap point after the fix (the `Å` is a second
  cause — report, do not chase it in this plan).

## Maintenance notes

- The caret cell's glyph is load-bearing: it must stay a **non-breaking** one-cell
  glyph. A swap back to breakable whitespace re-introduces the loss;
  `TestOtherCaretCellSurvivesSoftWrap` guards it.
- The hang marker is an in-band reflow protocol (like QSEP). **Every `c.lines`
  consumer must strip it** — today the viewport reflow (`softWrapOverlayLine`,
  `overlaySoftWrapRefs`) and the non-viewport fallback (`finishCollector`). A
  future consumer of `c.lines` that forgets to strip will print `HANG`;
  `TestQuestionOverlayFallbackStripsReflowMarkers` guards the fallback. If a third
  consumer appears, prefer carrying the hang prefix out of band then.
- `overlaySoftWrapRefs` must apply the **same** `contentWidth < 1` fallback as
  `softWrapOverlayLine` (it takes the width for this). If a future change alters
  one's fallback, alter the other's, or rows and refs disagree below the prefix
  width and clicks drift; `TestOtherContinuationHangsUnderTextColumn` checks
  rows/refs agreement from width 4.
- Pre-existing, out of scope: at `contentWidth < 2` (terminal narrower than the
  prefix + 2) `xansi.WrapWc` cannot split a wide (CJK) rune into a 1-cell budget
  and may emit an over-width row. The pristine tree does this too; it is a vendor
  limitation, not introduced here. Test (b) asserts hang/no-overflow/click only
  from `pw+2` up for this reason. Fixing it (e.g. clamping the fallback to
  `contentWidth < 2`) would change every overlay's narrow-width wrapping — a
  separate plan if wanted.
- A reviewer should scrutinize: only the listed functions changed; nbsp does not
  alter click/selection column math (one column); the marker is stripped in BOTH
  consumers and never reaches painted rows or the plain-text ref walk; no existing
  caret assertion weakened; the QSEP literal converged to `overlaySepMarker`
  consistently (definition, expansion, fallback strip).
- Equal-substitution wart (pre-existing): a viewport-level mouse selection copied
  across the Other row carries the caret cell's glyph — now U+00A0 instead of the
  old phantom space. Buffer-level selection/copy (`copy_on_select`) reads
  `o.otherBuf` and is unaffected.
- Vendor pin: charmbracelet/x/ansi v0.11.9; the nbsp exemption is upstream's
  documented non-breaking-space semantics.
- The stray `Å` mechanism remains unverified; the owner confirms visually. If it
  recurs, investigate the Terminal.app pixel path (`paint_dump.sh` +
  `term_replay.py`, `tui-row-edge-audit` skill) as a separate plan.

## Implementation Status

- **Verdict**: APPROVE (tech-lead review; every done criterion re-run independently in the worktree).
- **Worktree / branch**: `.worktrees/fix-other-caret-wrap` / `fix-other-caret-wrap` (base `a379d586`). **Merged ff-only into dev `a379d586..29d9030b` (owner instruction 2026-11-28, not pushed).** Post-merge gates in the main tree green: gofmt empty, build 0, vet 0, focused + named tests ok (full package suite green in the worktree on the identical base).
- **Executor**: custom/qwen4-max-preview (xhigh), commit `9298e0dd` `fix(tui): keep Other caret visible on soft wrap` (3 files +160/−11, DCO signed). Execution-mode handoff overrode the plan's embedded "no worktree/no commit/Step 5" notes: committed on the worktree branch; plans/ index maintained by reviewer.
- **Deviations**: none material. Production diff matches the plan verbatim (Fix A nbsp caret; Fix B hang marker + width-aware refs; Fix C fallback strips both markers; QSEP converged to `overlaySepMarker`). Executor noted a pre-existing `ask`-mode fake-provider race (a background unary request consumed the user_interaction script; retry succeeded) — unrelated to this diff, not changed; candidate for a separate harness fix.

### Verification (reviewer's own runs, in the worktree)

- Scope: `git diff --stat a379d586..HEAD` = exactly `pkg/tui/overlays.go`, `pkg/tui/reducer.go`, `pkg/tui/overlays_test.go`; `git status` clean; no out-of-scope file.
- `grep otherCaretCell =` → `"\x1b[7m\u00A0\x1b[0m"`; `grep overlayHangMarker|overlaySepMarker overlays.go` → exactly 4 lines (3445 emit, 3676/3677 strips, 3697 separator return).
- `gofmt -l pkg/tui` → empty; `go vet -tags fts5 ./...` → exit 0.
- Focused new + named existing tests (`TestOtherCaret*`, `TestOtherContinuation*`, `TestOverlayDescription*`, `TestQuestionOverlayFallback*`, `TestQuestionOverlayOtherCaretStaysOnScreenWhileTyping`, `TestQuestionOtherClickPlacesCaret`, `TestQuestionOverlayOtherFieldKeepsCaretAcrossTabs`, `TestQuestionOverlayOtherTextIsolatedAcrossQuestionsAndSubmission`, `TestQuestionHeaderChipWrapsAtTheTerminalWidth`) → ok 0.85s.
- Package regression `go test -tags fts5 ./pkg/tui ./pkg/turn ./pkg/state -count=1` → ok (tui 212.7s, turn 5.1s, state 7.2s).
- RED→GREEN: executor demonstrated Step-1 RED (caret lost / rows-refs disagree / QSEP leaked) → GREEN; reviewer confirmed the bootstrap RED independently during planning and the GREEN above.
- Real-machine (reviewer's own tmux run, isolated home, worktree binary, width 161): caret row 36 = `            se表示子代理\x1b[7m\xa0\x1b[0m` (nbsp caret present, 12-space hang); no row ends in bare `\x1b[7m`; continuation rows 35/36 hang 12 spaces; typing `X` advances the caret (plain-col 20). Matches the executor's report and the reviewer's pre-fix reproduction (caret absent at 161 before the fix).
- New tests audited: all four assert meaningful properties (caret survival across positions×widths on `selfContainedRows`; hang + click-to-rune mapping with narrow/wide regimes; description-wrap guard; fallback no-leak). Existing tests untouched (additions only).

### Left for the owner

- Visual confirmation in Terminal.app that the stray `Å` is gone (tmux byte evidence shows the dangling-SGR residue removed and no `Å`; the Terminal.app pixel mechanism was never verified). If a stray glyph persists, that is a second cause — separate investigation.
- Worktree/branch cleaned per owner 「删」 (2026-11-28): `git worktree remove .worktrees/fix-other-caret-wrap` + `git branch -d fix-other-caret-wrap` (was `29d9030b`, verified clean and fully merged into dev before deletion). Other worktrees/branches untouched.

### Round 2 — pre-existing issues fixed this pass (owner 2026-11-28: 「预存问题必须本期一并修复，不立计划，直接修复」)

Commit `29d9030b` (on top of `9298e0dd`, same branch), 3 files +42/−6:

- **Narrow-width wide-rune overflow** (pre-existing, pristine tree overflowed too):
  `softWrapOverlayLine` / `overlaySoftWrapRefs` clamp the no-hang fallback from
  `contentWidth < 1` to `contentWidth < 2` — a wide (CJK) rune needs two cells, so a
  one-cell budget made `xansi.WrapWc` emit an over-width row. Below two cells the
  hang prefix is dropped and the whole line wraps at full width. New regression
  `TestOverlayNarrowWidthDoesNotOverflow` (widths 2–40, plain space-prefix + Other
  hang-marker + description lines): **red on the old `<1` clamp** (`width=13 row
  overflows (w=17)`), green after. `TestOtherContinuationHangsUnderTextColumn`'s
  `contFor` updated to mirror the `<2` fallback.
- **ask-mode fake-provider script race** (pre-existing): the `TOOL_SCRIPTS`
  dispatch (`ask`/`tool`/`toolcall`/`shell`) used `next(REQUESTS)`, so a background
  memory/compaction unary request arriving first consumed the turn's
  `user_interaction` script. Fixed to the established multishell pattern:
  `_is_turn_request(body)` routes non-turn passes to `serve_background`, and a
  per-turn counter `TOOL_SCRIPT_TURNS` picks the script. Verified live: a
  background memory unary request sent **first** got `serve_background` (the memory
  JSON), and the turn still received its `user_interaction` form.

Round-2 verification (reviewer's own runs, worktree): `gofmt -l pkg/tui` empty;
focused tests incl. the new overflow regression pass; full
`pkg/tui ./pkg/turn ./pkg/state` ok (211.9 / 5.1 / 6.5s); `go vet -tags fts5 ./...`
exit 0; `fake_provider.py` `py_compile` OK; overflow red-probe and background-first
race probe both confirm the fixes. `pkg/architecture` pre-existing red unchanged
(no new production files; tests added to existing `overlays_test.go`).
