# Plan 007: Move the console log format out of the CLI entrypoint

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `docs/plan/LOGGING_HARDENING/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat eafaf46..HEAD -- cmd/forebrain/console_log.go cmd/forebrain/console_log_test.go cmd/forebrain/root.go cmd/forebrain/gateway_log.go pkg/telemetry/ pkg/architecture/testdata/graph.json`
> The `cmd/forebrain` log files are **untracked new files**. Compare the
> "Current state" excerpts against the live code; on a mismatch, treat it as a
> STOP condition.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: LOW (a move, with two CLI-side decisions deliberately left in place)
- **Depends on**: `docs/plan/LOGGING_HARDENING/003-console-format-fidelity.md`
  and `docs/plan/LOGGING_HARDENING/004-console-sink-resilience.md` — both edit
  the file this plan moves, so they must be settled first. Sequence
  `006-access-log-middleware-seam` **before** this plan so the two moves into
  `pkg/telemetry` stay separate, reviewable diffs.
- **Category**: tech-debt
- **Planned at**: commit `eafaf46`, 2026-10-08

## Why this matters

`FOREBRAIN.md` states the layout rule this repo enforces: *"`cmd/forebrain` —
thin Cobra entrypoint (`gateway`, `interactive`, `resume`, `serve`, `lsp`); keep
CLI wiring here, push behavior down into `pkg/...`"*. The console log format is
the largest thing that violates it today: roughly 350 lines of format policy plus
its golden tests live in `cmd/forebrain/console_log.go`, while `pkg/telemetry` is
the package that already owns log plumbing (`logfile.go` rotation,
`slog_handler.go` tee, `levels.go`, `debuglog.go`) and whose doc says it provides
"logs, traces, metrics, and usage records".

Two concrete costs, not just tidiness:

1. **The web surface is required to have 1:1 parity with the TUI**, and the
   gateway already reuses `pkg/telemetry` for its log file. A log *format* that
   only `cmd/forebrain` can reach cannot be reused by any other binary or surface
   (`serve`, a future gateway-only entrypoint, an integration test that asserts
   the log shape) without copying it.
2. **The format's tests can only run through the CLI package**, so the format's
   rules (colour policy, quoting, one-line-per-record) are entangled with Cobra
   wiring — the golden tests need `cmd/forebrain` to compile with cobra, the
   package's other tests' fixtures, and a fake-terminal seam that is a CLI
   concern.

After this plan the format lives in `pkg/telemetry` beside the primitives it
renders into, and `cmd/forebrain` keeps only the decisions that are genuinely
about *this process's* stderr.

## Current state

Files:

- `cmd/forebrain/console_log.go` (untracked, ~355 lines) — everything to move.
- `cmd/forebrain/console_log_test.go` (untracked) — the format tests to move, and
  the CLI-glue tests to keep.
- `cmd/forebrain/root.go` — `defaultSlogLevel()` and `rootIsTerminal`; both stay.
- `cmd/forebrain/gateway_log.go` — the gateway installer; keeps its own file
  opening and stays (except for the constructor rename).
- `pkg/telemetry/doc.go` — `// Package telemetry provides logs, traces, metrics, and usage records.`
- `pkg/telemetry/logfile.go`, `levels.go`, `slog_handler.go` — the primitives the
  format sits beside.

### What moves, and what stays (this split is the core decision — do not renegotiate it)

**Moves to `pkg/telemetry`** (all pure format/rendering, no process policy):
`consoleLogHandler`, `consoleSink`, `consoleAttr`, `newConsoleLogHandler`,
`appendLine`, `appendConsoleAttrs`, `appendAttrLine`, `writeAttr`, `attrValue`,
`statusColor`, `durationColor`, `humanBytes`, `levelTag`, `messageText`, and —
if plan 004 has landed — the non-aborting fan-out writer
(`multiWriter`/`fanoutWriter`) used to mirror a logfmt stream into a file.

**Stays in `cmd/forebrain`** (process policy):

- `rootIsTerminal` — a package-level test seam the CLI tests swap
  (`fakeTerminal` in `console_log_test.go`), used by `rootMainRun`,
  `interactive.go`, `lsp.go`, `serve.go`.
- `stderrColor()` — "is *this process's* stderr a terminal, and is NO_COLOR
  unset". The env var is a process/CLI concern and is already read here.
- `stderrLogHandler` / `stderrLogHandlerTo` — the ~15-line decision of which sink
  set to build (terminal and/or the mirrored file, console vs logfmt). These
  become thin: they build sinks and call the moved constructor.
- `defaultSlogLevel()` (in `root.go`) — log-level env policy.
- `installGatewayConsoleLog` (`gateway_log.go`) — opening `gateway.log` with
  `telemetry.Open`.

The current constructors, verbatim:

```go
// cmd/forebrain/console_log.go
func newConsoleLogHandler(level slog.Level, addSource bool, sinks ...consoleSink) slog.Handler {
	return &consoleLogHandler{
		sinks:     sinks,
		level:     level,
		addSource: addSource,
		mu:        &sync.Mutex{},
	}
}

// stderrLogHandlerTo builds that handler and, when file is non-nil, mirrors
// every record into it: the compact console format on a terminal — colored
// unless NO_COLOR — with the file receiving the same text without a single
// SGR byte. Everywhere else (systemd, docker, a redirected descriptor) the
// terminal and the file both get logfmt, so machine consumers see one stable
// format.
func stderrLogHandlerTo(file io.Writer, level slog.Level, addSource bool) slog.Handler {
	if !rootIsTerminal(os.Stderr) {
		w := io.Writer(os.Stderr)
		if file != nil {
			w = io.MultiWriter(os.Stderr, file)      // ← plan 004 replaces this
		}
		return slog.NewTextHandler(w, &slog.HandlerOptions{Level: level, AddSource: addSource})
	}
	sinks := []consoleSink{{w: os.Stderr, color: stderrColor()}}
	if file != nil {
		sinks = append(sinks, consoleSink{w: file})  // ← plan 004 reorders (file first)
	}
	return newConsoleLogHandler(level, addSource, sinks...)
}
```

```go
type consoleSink struct {
	w     io.Writer
	color bool
}
```

The format itself (the part that must arrive byte-identical):

```
16:40:05.131 INF request method=GET path=/ status=200 duration=1.8ms bytes=2.1KB remote=127.0.0.1 · access_log.go:23
```

**Repo conventions that apply:**

- Every package carries an accurate `doc.go` (`FOREBRAIN.md`, House rules) — the
  `pkg/telemetry` doc must mention the console format.
- Golden tests assert format **byte for byte**, including SGR sequences, and the
  file sink carries no ESC byte — see `TestConsoleLogHandlerColorGolden` /
  `TestConsoleLogHandlerMultipleSinks`.
- **The package graph is gated**: `scripts/package-graph.sh` and
  `pkg/architecture/testdata/graph.json`. The graph records only `./pkg/...`, so
  this move changes `telemetry`'s `loc` **and nothing else** (no new import edge —
  the moved code imports only stdlib). Regenerate and commit that.
- CGO is mandatory for tests here: `CGO_ENABLED=1 go test -tags fts5 …`.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Format | `gofmt -l cmd pkg` | only `pkg/tui/render.go` (pre-existing at `eafaf46`) |
| New home tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/... -count=1` | `ok` |
| CLI tests | `CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain/... -count=1` | `ok` |
| Whole tree | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | all `ok` |
| Package graph | `scripts/package-graph.sh` | regenerates `graph.json`; diff limited to `telemetry`'s `loc` |

## Scope

**In scope**:

- `pkg/telemetry/console_log.go` (create) — the moved format
- `pkg/telemetry/console_log_test.go` (create) — the moved format tests
- `pkg/telemetry/doc.go` (one line)
- `cmd/forebrain/console_log.go` — shrink to the CLI-side wiring only
- `cmd/forebrain/console_log_test.go` — keep only the CLI-glue tests
- `cmd/forebrain/gateway_log.go` — constructor rename only
- `pkg/architecture/testdata/graph.json` — regenerated (`telemetry`'s `loc`)

**Out of scope** (do NOT touch):

- `rootIsTerminal`, `stderrColor`, `defaultSlogLevel`, `fakeTerminal` — they stay
  in `cmd/forebrain` (see the split above). Moving the TTY decision into
  `pkg/telemetry` would break the CLI tests' seam and put process policy in a
  library.
- The format itself. This is a move: **no** change to the colour table,
  thresholds, quoting, message escaping, sink ordering, or the source-shortening
  rule. If a test needs updating, the move changed behaviour — that is a bug in
  the move, not a test to relax.
- `pkg/telemetry`'s existing primitives (plans 002/006 own those).
- `cmd/forebrain/root.go`'s `init()` / `prepPersistentHome` wiring beyond the
  constructor name.
- Do not introduce an options-struct abstraction, a format registry, or an
  interface for "log renderers". One constructor with variadic sinks is the shape
  the code already has and the shape to keep.

## Git workflow

- Branch: `refactor/telemetry-console-log-format`
- Two commits preferred: (1) add the format to `pkg/telemetry` with its tests,
  (2) delete it from `cmd/forebrain` and rewire. That order leaves the tree
  building between commits. Conventional Commits + `-s`, e.g.
  `refactor(telemetry): own the compact console log format`.
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Move the format into `pkg/telemetry`

Create `pkg/telemetry/console_log.go` with the moved symbols, exported at exactly
the two boundaries the CLI needs:

```go
// ConsoleSink is one destination of a console log line. Color decides whether
// that destination receives SGR sequences: a terminal gets them, a log file
// never does.
type ConsoleSink struct {
	W     io.Writer
	Color bool
}

// NewConsoleHandler returns a handler printing one line per record:
//
//	16:40:05.131 INF request method=GET path=/ status=200 … · serve_run.go:203
//
// Every record goes to each sink in turn, so one handler can serve the terminal
// and the log file at once. addSource keeps the process's AddSource semantics,
// with the file shortened to its basename at the end of the line.
func NewConsoleHandler(level slog.Level, addSource bool, sinks ...ConsoleSink) slog.Handler
```

Everything else stays unexported (`consoleLogHandler`, `consoleAttr`,
`appendLine`, `writeAttr`, `attrValue`, `levelTag`, `statusColor`,
`durationColor`, `humanBytes`, `messageText`, `appendAttrLine`,
`appendConsoleAttrs`). If plan 004 has landed, move its fan-out writer in too and
export it only if a caller outside the package needs it — the CLI needs it for
the logfmt path, so export it as `MultiWriter(writers ...io.Writer) io.Writer`
with the same doc rationale ("each write goes to every writer even when one
fails; errors are joined").

Rename the internals: `h.sinks` becomes `[]ConsoleSink`; field accesses
`s.w`/`s.color` become `s.W`/`s.Color`.

Update `pkg/telemetry/doc.go`:

```go
// Package telemetry provides logs, traces, metrics, and usage records,
// including the compact console log format and the bounded, rotating log files
// the process writes them into.
```

**Verify**: `go build ./pkg/telemetry/ && go vet ./pkg/telemetry/` → exit 0.

### Step 2: Move the format tests; assert they pass unchanged

Move these tests from `cmd/forebrain/console_log_test.go` to
`pkg/telemetry/console_log_test.go`, changing only the constructor name
(`newConsoleLogHandler` → `NewConsoleHandler`) and the sink literal
(`consoleSink{w: …}` → `ConsoleSink{W: …}`):

- `TestConsoleLogHandlerColorGolden`
- `TestConsoleLogHandlerPlainGolden`
- `TestConsoleLogHandlerLevelColors`
- `TestConsoleLogHandlerErrorValuesAreRed`
- `TestConsoleLogHandlerValuePolicyByKey`
- `TestConsoleLogHandlerQuotesValuesThatNeedIt`
- `TestConsoleLogHandlerWithAttrsAndGroup`
- `TestConsoleLogHandlerRespectsLevel`
- `TestConsoleLogHandlerMultipleSinks`
- `TestConsoleLogHandlerShortensSource`
- `TestConsoleLogHandlerConcurrentWrites` (plus the `writerFunc` adapter and the
  `consoleTestTime`/`consoleRecord`/`renderConsole` helpers it needs)
- plan 003's `TestConsoleLogHandlerResolvesLogValuer`,
  `TestConsoleLogHandlerKeepsOneLinePerRecord`,
  `TestConsoleLogHandlerDoesNotQuoteProseMessages`
- plan 004's `TestConsoleLogHandlerWritesEverySinkWhenOneFails`,
  `TestConsoleLogHandlerWritesDurableSinkFirst`,
  `TestMultiWriterKeepsGoingAfterAFailure`,
  `TestConsoleLogHandlerHandleReturnsNoErrorOnSuccess`

Mind helper collisions inside the destination package: run
`grep -rn "captureStderr\|writerFunc\|consoleRecord\|renderConsole" pkg/telemetry/`
first and rename the moved helpers if anything clashes (the `pkg/telemetry` tests
already define their own helpers).

**Verify**:
`CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/... -count=1 -run 'ConsoleLog|MultiWriter' -v`
→ every moved test `PASS`, byte-for-byte, with **no assertion edited**. This is
the proof the move is behaviour-preserving.

### Step 3: Shrink `cmd/forebrain/console_log.go` to CLI wiring

Delete every symbol listed under "Moves", and leave the process-policy decision
plus the `stderr*` constructors:

```go
// cmd/forebrain/console_log.go
package main

import (
	"io"
	"log/slog"
	"os"
)

// stderrColor reports whether the terminal this process logs to should get SGR
// sequences: only a real terminal, and never when NO_COLOR asks for none.
func stderrColor() bool {
	return rootIsTerminal(os.Stderr) && os.Getenv("NO_COLOR") == ""
}

// stderrLogHandler is the inner handler the process logs to stderr through
// before the telemetry tee wraps it.
func stderrLogHandler(level slog.Level, addSource bool) slog.Handler {
	return stderrLogHandlerTo(nil, level, addSource)
}

// stderrLogHandlerTo builds that handler and, when file is non-nil, mirrors
// every record into it: the compact console format on a terminal — colored
// unless NO_COLOR — with the file receiving the same text without a single SGR
// byte. Everywhere else (systemd, docker, a redirected descriptor) the terminal
// and the file both get logfmt, so machine consumers see one stable format.
func stderrLogHandlerTo(file io.Writer, level slog.Level, addSource bool) slog.Handler {
	if !rootIsTerminal(os.Stderr) {
		writers := []io.Writer{os.Stderr}
		if file != nil {
			writers = append(writers, file)
		}
		return slog.NewTextHandler(telemetry.MultiWriter(writers...), &slog.HandlerOptions{Level: level, AddSource: addSource})
	}
	// The durable destination is written first: if the process dies between the
	// two writes, the record that is supposed to outlive the session is the one
	// already on disk.
	sinks := []telemetry.ConsoleSink{{W: os.Stderr, Color: stderrColor()}}
	if file != nil {
		sinks = []telemetry.ConsoleSink{{W: file}, {W: os.Stderr, Color: stderrColor()}}
	}
	return telemetry.NewConsoleHandler(level, addSource, sinks...)
}
```

(If plan 004's durable-first ordering or its `MultiWriter` is not present, keep
the pre-004 form `io.MultiWriter(os.Stderr, file)` and the terminal-first sink
order — this step must reproduce whatever 004 left behind, not introduce it.)

In `cmd/forebrain/gateway_log.go`, the only change is the constructor name:
`stderrLogHandlerTo(sink, defaultSlogLevel(), true)` stays as it is.

**Verify**:
```
grep -n "consoleLogHandler\|appendLine\|attrValue\|levelTag\|humanBytes\|messageText" cmd/forebrain/console_log.go   # expect no matches
go build ./... && go vet ./...
```

### Step 4: Move the CLI still needs into `console_log_test.go`

Keep in `cmd/forebrain/console_log_test.go` only the tests that exercise this
process's wiring, deleting the format tests that are now in `pkg/telemetry`:

- `TestStderrLogHandlerFormatFollowsTTY` (redirected / terminal / NO_COLOR — it
  needs `fakeTerminal`, which is CLI glue) and its helpers `fakeTerminal`,
  `captureStderr`.
- The gateway installer tests: `TestInstallGatewayConsoleLogMirrorsConsoleFormat`,
  `TestInstallGatewayConsoleLogKeepsLogfmtOffATerminal`,
  `TestInstallGatewayConsoleLogSurvivesMissingLogsDir`, plus the mode assertion
  plan 001 adds, and `stripSGR`.

These tests read the installed handler's output through `slog`, so they must keep
passing **without** edits beyond renames forced by the move (none expected: they
call `stderrLogHandlerTo`/`installGatewayConsoleLog`, both of which stay in the
package).

**Verify**:
`CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain/... -count=1 -run 'StderrLogHandler|InstallGateway' -v`
→ all `PASS`.

### Step 5: Regenerate the package graph and run the full gates

Moving ~330 lines of *non-test* Go into `pkg/telemetry` changes that package's
`loc` in the gated graph (`./pkg/...` only — `cmd/` is not in the graph, so no
edge changes):

```
scripts/package-graph.sh
git diff pkg/architecture/testdata/graph.json
```

**Verify**: the diff contains a `loc` change for
`github.com/forebrain-harness/forebrain-harness/pkg/telemetry` and **nothing
else**. A new edge means the moved code imports something unexpected — STOP.

Then:

```
go build ./... && go vet ./... && gofmt -l cmd pkg
CGO_ENABLED=1 go test -tags fts5 ./... -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/... -count=1
git diff --stat -- pkg/agent pkg/assembly pkg/llm pkg/turn/prompt* internal/agentrun
```

**Verify**: build/vet exit 0; `gofmt` lists only `pkg/tui/render.go`; every
package `ok`; the prompt-cache diff is empty.

## Test plan

This plan's test work is a **move plus one new boundary test**, not new coverage:

- The moved tests keep every assertion identical (step 2). If an assertion must
  change to pass, the move altered behaviour — fix the move, not the test.
- New: `TestNewConsoleHandlerIsTheOnlyExportedFormatEntryPoint` is **not**
  wanted — an API-shape test would pin an implementation detail. Instead, the
  existing goldens are the contract, and the CLI glue's three-way branch is
  covered by `TestStderrLogHandlerFormatFollowsTTY` (kept in step 4).
- Verification commands and expected results are given per step above; the
  decisive one is step 2's
  `CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/... -run 'ConsoleLog' -v`
  passing with unedited assertions.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `grep -rn "consoleLogHandler\|func attrValue\|func levelTag\|func humanBytes" cmd/forebrain/` → no matches
- [ ] `grep -n "func NewConsoleHandler\|type ConsoleSink" pkg/telemetry/console_log.go` → 2 matches
- [ ] `grep -c "func Test" cmd/forebrain/console_log_test.go` → only the CLI-glue tests remain (≤ 6)
- [ ] `grep -n "rootIsTerminal\|stderrColor" cmd/forebrain/console_log.go` → both still present
- [ ] `go build ./...` exits 0
- [ ] `go vet ./...` exits 0
- [ ] `gofmt -l cmd pkg` prints only `pkg/tui/render.go`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → all packages `ok`
- [ ] `git diff pkg/architecture/testdata/graph.json` shows only `telemetry`'s `loc`
- [ ] `git diff --stat -- pkg/agent pkg/assembly pkg/llm pkg/turn/prompt* internal/agentrun` → empty
- [ ] `docs/plan/LOGGING_HARDENING/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- The excerpts do not match the live files (untracked work in progress may have
  changed), **or** plans 003/004 have not landed — check for `messageText` and
  `MultiWriter`/`fanoutWriter` in `cmd/forebrain/console_log.go` before starting;
  if absent, STOP, because the file you would move is not the file this plan was
  written against.
- A moved test fails. The format is pinned byte-for-byte; a failure means the
  move changed rendering (an obvious candidate: forgetting to carry `Color`
  through the renamed `ConsoleSink`, or resolving `Resolve()` in the wrong
  place).
- You need to move `rootIsTerminal` or `defaultSlogLevel` into `pkg/telemetry` to
  make something compile. That is out of scope by design (see the split);
  report what forced it.
- The regenerated graph shows any change beyond `telemetry`'s `loc`.
- Moving the file turns out to require exporting internals beyond
  `NewConsoleHandler`, `ConsoleSink` and (if 004 landed) `MultiWriter` — e.g.
  `appendLine` for a test. Exporting render internals is a design change; STOP
  and report which symbol and why.

## Maintenance notes

- **Where the split is, and why**: format/rendering in `pkg/telemetry`; "is this
  process's stderr a terminal", `NO_COLOR`, log-level env, and which sinks exist
  in `cmd/forebrain`. A future surface (a gateway-only binary, an integration
  test asserting the log shape) can now call
  `telemetry.NewConsoleHandler` without importing the CLI.
- **`ConsoleSink.Color` is positional-free**: colour travels with the sink, not
  with ordering. If someone reorders sinks, colour must follow the terminal.
- **The goldens are the format contract.** Any change to a threshold, a colour
  code, or the quoting rule must be made deliberately in `pkg/telemetry` and show
  up as a golden diff — that is the review signal, not noise.
- **Interaction with plan 006**: both plans move logging code into
  `pkg/telemetry`. Landing 006 first keeps this diff purely "console format";
  landing this one first sympathetically keeps 006 purely "access log". Do not
  interleave them in one commit.
- **Reviewer scrutiny**: read the moved file and the deleted one as a diff — a
  move that also edits is where behaviour hides. Then confirm `cmd/forebrain`'s
  tests still fake a terminal through `rootIsTerminal` (the seam must not have
  been lost) and that `installGatewayConsoleLog` still opens `gateway.log` with
  `telemetry.Open` and its `Perm`/`ExistingParentOnly` options intact.
