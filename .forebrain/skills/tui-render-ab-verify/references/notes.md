# Notes: what the regression was, and the guard for it

Detail behind `SKILL.md`. Read this when you are writing the invariant test or
trying to recognise the flattened-palette failure from numbers alone.

## The failure this skill was written for

forebrain paints code in several places, and two very different policies were
being applied to them:

- **A diff row** sits on a coloured add/del band. The band raises the background
  out from under the text, so `claudeDiffPaletteFor` lifts *every* token to one
  target luminance (`syntaxLuminanceDark = 0.62` on a dark theme) — otherwise
  chroma's deliberately muted comment grey becomes unreadable against the band.
- **Code on the plain terminal background** (read_file output, markdown fenced
  code blocks) has no band to fight, but was using that same policy. Pulling
  every token to one luminance erases the differences the theme uses to say
  *what a token is*, and because the theme's plain text was already brighter
  than the target, plain text ended up the brightest thing on the line and the
  "highlighting" read as the washed-out part.

Under the 256-colour path the pale lifted colours then quantised onto the
palette's pale end, which is why the symptom looked like "one sandy tone".

The fix keeps the band policy for diffs and gives plain-background code its own
policy: a *legibility floor* only (`plainLuminanceDark`, ~4.5:1 against a
typical dark canvas), leaving the theme's own emphasis intact, plus a mainstream
theme rather than the muted one.

## The generalisable rule

A brightness normaliser is only correct where the background is no longer the
terminal's own. Elsewhere, clamp for legibility — never equalise.

## Measured separation

Same fixture and same card, before (clean HEAD) vs after, extracted from
`capture-pane -e`:

| capture | strongest saturation | luminance range | closest pair |
|---|---|---|---|
| truecolor, before | 0.35 | 0.62 | 44 |
| truecolor, after | 0.80 | 0.72 | 115 |
| 256-colour, before | 0.31 | 0.21 | 69 |
| 256-colour, after | 1.00 | 0.54 | 108 |

Note the 256-colour "before" row: 69 RGB steps still looks far apart numerically
while reading as one pale tone on screen. Saturation is the signal that tracks
what the eye sees.

## Shape of the guard test

Assert the *property*, not the palette, so a future theme change does not make
the test meaningless and a future regression cannot pass it:

```go
// Distinct 24-bit foregrounds a rendered string emits.
colours := syntaxRGBs(t, formatHighlightedToolOutput(body, "render.go", DiffThemeDark))
if len(colours) < 4 {
    t.Fatalf("emitted %d distinct syntax colours, want the theme's classes", len(colours))
}
dist, a, b := closestSyntaxColours(colours)
if dist < minDistance { // 60
    t.Fatalf("#%02x%02x%02x and #%02x%02x%02x are only %d apart", ...)
}
for _, c := range colours {
    if lum := relativeLuminance(c[0], c[1], c[2]); lum < plainLuminanceDark-0.02 {
        t.Fatalf("#%02x%02x%02x at luminance %.3f is below the legibility floor", ...)
    }
}
// and: the diff surface must still hold the band policy
for _, lum := range diffForegroundLuminances(t, RenderDiff(doc, DiffThemeDark)) {
    if lum < syntaxLuminanceDark-0.02 { t.Fatalf("band policy regressed") }
}
```

Two ways to prove the guard actually guards, run once and then reverted:

- restore the old constant (`plainLuminanceDark` -> `syntaxLuminanceDark`) and
  watch it fail with a one-step distance between the comment and whitespace
  greys;
- keep the new policy but restore the muted style name, and watch the count of
  distinct colours fall.

## TUI specifics that cost time

- A tool card renders only the first few body rows
  (`toolOutputMaxLines` in `pkg/tui/render.go`), then a
  `··· N more lines (click to expand)` hint. Pass the read tool an `offset` and
  `limit` so the lines you want to inspect land inside that preview.
- The read tool's argument is `file_path`; `path` fails with
  `file_path is required and must not be blank`.
- The fixture must be inside the harness project directory *at call time*.
- `go test ./pkg/tui` has pre-existing failures on some machines (a network
  test, and sandbox/approval tests where the sandbox is unavailable). Compare
  against the same run on a clean HEAD worktree rather than expecting green.
- Renaming a style resolver breaks test helpers that call it directly — update
  those call sites in `pkg/tui/chat_session_test.go` as part of the change.
- Builds are large: a full binary is ~100-125 MB and a link needs a few hundred
  MB of free disk. On a nearly full disk the linker fails with
  `write() failed, errno=28`, which reads like a code error but is not.

## Driving the composer (harness-facing, worth knowing before blaming your change)

Measured with `tmux send-keys -l` on an isolated home, watching both the rendered
composer and `fb_messages`:

- **Typing works, including during startup** — *once nothing else is reading the
  terminal fd*. See the next section: for a long time it did not, and the harness
  took the blame.
- **The three harness traps that produce fake losses:**
  1. typing before the composer is mounted — `driver.sh start` waits for
     `"/ commands"`, which the *splash* screen also paints, and the
     workspace-trust prompt marks its selected option with the composer's own `›`
     glyph, so both "hints" can be true while no composer exists;
  2. retrying the send in a loop — `C-u` is not kill-line here, so retries append
     and interleave into visible garbage;
  3. writing to the pane's slave tty (`printf … > /dev/ttysNNN`) — that only
     *prints*; the composer buffer stays empty and Enter does nothing.
- **A fast burst can become a paste.** Characters arriving inside the burst
  interval are held, then either typed out or flushed as a paste (bracketed
  paste, or the heuristic in `pasteBurstState`). Content is preserved either way;
  only the presentation differs (a placeholder instead of literal text). The unit
  pipeline reproduces this: writing the same string with 0/2/5 ms gaps yields one
  paste event, with 9 ms and up one draft per character — all of them complete.
- **No clear-line key.** `C-u` is a no-op and Escape does not wipe the composer,
  so a wrongly-filled composer is only recoverable by starting a new session.

## The startup keystroke thieves (fixed)

This is the defect that made `drive-card.sh` report `composer holds › e`, and the
reason an earlier version of these notes wrongly concluded "suspect the harness
before the app". The composer was mounted, empty and ready; the characters were
taken before the input pipeline could read them.

A terminal fd has exactly one input queue. Whoever calls `read()` on it takes
bytes out of everyone else's hands, so the TUI serialises every legitimate reader
(the input loop, `readKey`, the modal drain) behind `stdinReadMu`. Two readers at
startup did not participate:

1. **`probeDarkBackgroundBounded` abandoned its own reader.** It wrote an OSC 11
   background query, handed the read to a goroutine, and gave up on it after
   200 ms with `select`/`time.After`. A `Read()` blocked on a tty cannot be
   cancelled, so that goroutine stayed live for the rest of the session and
   consumed the next thing that arrived — up to 256 bytes, delivered into a
   channel nobody was left to receive from. Captured verbatim from an
   instrumented build: `probe: read RETURNED n=7 data="read it"`.
2. **lipgloss was left to detect the background lazily.** `ApplyDiffTheme` pinned
   `lipgloss.SetHasDarkBackground` only for Dark and Light; `DiffThemeUnknown`
   fell through with a comment claiming the branch was harmless. It was not. The
   first `AdaptiveColor` render — `forebrainLogoStyle` in the startup banner — makes
   lipgloss ask termenv, and termenv writes `OSC 11` **plus `CSI 6n`** and then
   reads the fd itself: `readNextResponse` discards every byte that is not the
   `ESC` it wants, and `readNextByte` waits up to five seconds for each one. So
   for ~5 s after the banner painted, a second thief sat on the queue eating
   plain characters.

With both of them live the seven bytes of `read it` were split three ways, which
is why the composer came back holding one arbitrary character (`› e`, `› r`,
`› rad it`) or nothing at all.

The fix is in `pkg/tui/tty_session.go`: the probe polls for readability with
`pollReadableInputFD` and reads on its own goroutine one byte at a time, so it
never leaves a read in flight; it does not query at all when input is already
waiting (that input is the user's, and once the two share a stream they cannot
be told apart); it stops at the first byte that cannot belong to an OSC 11
answer; and that byte is handed to the input pipeline
(`keepTerminalInputForInputPipeline`, delivered as its first chunk) because a
tty has no pushback and dropping it would lose a keystroke. `ApplyDiffTheme`
pins lipgloss for *every* theme, Unknown included, so the termenv query never
happens. Windows has no readiness gate (`canGateTerminalReads`), so the probe is
skipped there rather than blocking.

## The third reader: the workspace-trust prompt (fixed)

Same invariant, different place, found by auditing the rest of the startup path.
`cmd/forebrain/workspace_trust.go` read the answer through a `bufio.Reader`. bufio
fills from one read, so a user who answers and keeps typing in the same burst
had the rest of that burst pulled into a 4 KiB buffer that the function threw
away on return — and the TUI, which reads the same terminal next, never saw it.

Measured by sending `Enter` and `hello` as one `tmux send-keys`, so both land in
the same read:

| build | composer afterwards |
|---|---|
| before | `›` — the whole word gone |
| with the buffer removed only | `› ello` — the theme probe still took one byte |
| after both | `› hello` (5 of 5) |

The raw path now reads one byte at a time (`unbufferedByteReader`); the
non-terminal path keeps `bufio` because nothing takes the stream over from it.
Guard: `TestWorkspaceTrustPromptLeavesTypeAheadForTheTUI`.

## The rule this leaves behind

One process, one terminal queue. Before adding anything that reads the terminal
fd — a capability query, a colour probe, a pre-TUI prompt — answer two
questions: *can this read be abandoned?* (if not, do not start it) and *what
happens to a byte that turns out not to be mine?* (it belongs to the input
pipeline, so carry it there). Every defect in this file is one of those two
questions going unasked.

**Measured, typing into the composer the instant it appears:**

| build | prompt landed intact |
|---|---|
| before | 0 of 10 |
| after | 15 of 15 |

Guards: `TestReadOSC11ResponseStopsReadingWhenItGivesUp`,
`TestReadOSC11ResponseLeavesTypeAheadBehind`,
`TestReadOSC11ResponseParsesAnAnswerSplitAcrossWrites`,
`TestApplyDiffThemePinsLipglossForEveryTheme` — each checked to fail when its
half of the fix is reverted.

## The input-loss defect that *was* real (fixed)

The burst machinery holds a character for up to one burst interval so a fast
burst is not painted character by character. That hold used to live in a single
slot that other paths could clear without emitting, so input the reader had
already accepted could be discarded:

- `onASCIIChar`/`onNonASCIIChar` **overwrote** the held rune when the next one
  arrived just outside the burst interval, dropping the earlier one;
- `clearWindowAfterNonChar` (any non-plain key, a click, a burst-window reset)
  cleared it, and because it also zeroed the idle clock, `flushIfDue` could never
  emit it afterwards;
- the agent-roster stop shortcut ('x' with an empty composer) cleared it and an
  active burst body without flushing first.

The fix replaces the slot with an ordered queue (`held` + `heldAt`) whose idle
timer is independent of the burst window, and makes every path either hand the
text back (`flushBeforeModifiedInput`) or emit it (`flushIfDue`). Only `clear()`
still drops held text, and its two callers replace the draft wholesale after
flushing. Guards: `TestPasteBurstHeldCharacterSurvivesTheNextKey`,
`…SurvivesABurstWindowReset`, `…SecondCharacterDoesNotEraseTheFirst`,
`TestReadRawInputEventsKeepsACharTypedBeforeTheRosterStopKey`, plus
`TestReadRawInputEventsSubmitsEveryCharacterWhateverTheTiming` (0/2/5/9 ms), each
checked to fail when the fix is reverted.


