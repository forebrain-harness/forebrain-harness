# Plan 004: A failing log destination must never silence the durable one

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `docs/plan/LOGGING_HARDENING/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat eafaf46..HEAD -- cmd/forebrain/console_log.go cmd/forebrain/gateway_log.go cmd/forebrain/console_log_test.go`
> All three are **untracked new files** in the working tree (none exists at
> `eafaf46`). Compare the "Current state" excerpts against the live code; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: `docs/plan/LOGGING_HARDENING/003-console-format-fidelity.md`
  (same file; 003 lands first so this plan edits a settled `console_log.go`)
- **Category**: bug
- **Planned at**: commit `eafaf46`, 2026-10-08

## Why this matters

The gateway writes its console log to **two destinations at once**: stderr (what
the operator watches) and `<home>/logs/gateway.log` (what survives the session).
Both destinations are driven from a single code path that **abandons the
remaining destinations as soon as one write fails** — in both format modes:

1. **Console mode (TTY):** `consoleLogHandler.Handle` loops over sinks and
   `return`s on the first write error. Sinks are ordered **stderr first**, so a
   failing stderr aborts before the log file is ever written.
2. **Logfmt mode (no TTY):** the two destinations are glued with
   `io.MultiWriter`, whose documented behaviour is: *"If a listed writer returns
   an error, that overall write operation stops and returns the error; it does
   not continue down the list."* Same loss, same cause.

How stderr fails in practice, none of them exotic:

- `forebrain gateway start | tee gateway.log` or `| less`: when the reader exits,
  the next write to stderr gets `EPIPE`.
- A terminal/tmux pane that is gone: writes fail with `EIO`.
- Output piped into a consumer that exits early.

In all of those the process keeps running and serving (only its *logging* sink is
broken), so the durable file silently stops receiving records exactly when an
operator most wants the record — and nothing reports it, because `slog` discards
the error returned by `Handler.Handle`.

The fix makes one destination's failure a non-event for the others: every
destination is attempted for every record, and the **durable** destination is
written first so a process killed mid-record still leaves the line that matters.

## Current state

Files:

- `cmd/forebrain/console_log.go` (untracked) — `consoleLogHandler`, plus the
  `stderrLogHandlerTo` constructor that decides which sinks exist. **Both defects
  live here.**
- `cmd/forebrain/gateway_log.go` (untracked) — `installGatewayConsoleLog`, which
  opens `gateway.log` and calls `stderrLogHandlerTo`.
- `cmd/forebrain/console_log_test.go` (untracked) — existing tests for the format
  and for the sink plumbing.

Defect 1 — `Handle` abandons the remaining sinks (`console_log.go`):

```go
func (h *consoleLogHandler) Handle(_ context.Context, r slog.Record) error {
	if len(h.sinks) == 0 {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.sinks {
		var b strings.Builder
		h.appendLine(&b, r, s.color)
		if _, err := io.WriteString(s.w, b.String()); err != nil {
			return err
		}
	}
	return nil
}
```

Defect 2 — sink construction, both modes (`console_log.go`,
`stderrLogHandlerTo`):

```go
func stderrLogHandlerTo(file io.Writer, level slog.Level, addSource bool) slog.Handler {
	if !rootIsTerminal(os.Stderr) {
		w := io.Writer(os.Stderr)
		if file != nil {
			w = io.MultiWriter(os.Stderr, file)
		}
		return slog.NewTextHandler(w, &slog.HandlerOptions{Level: level, AddSource: addSource})
	}
	sinks := []consoleSink{{w: os.Stderr, color: stderrColor()}}
	if file != nil {
		sinks = append(sinks, consoleSink{w: file})
	}
	return newConsoleLogHandler(level, addSource, sinks...)
}
```

The call site in `gateway_log.go`, for context (do not change it here):

```go
	var sink io.Writer
	if file != nil {
		sink = file
	}
	prev := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewSlogTeeHandler(root, stderrLogHandlerTo(sink, defaultSlogLevel(), true))))
```

**Reference behaviour** (stdlib, recorded because the executor has not read it):
`go doc io.MultiWriter` states the failure mode quoted in "Why this matters";
`errors.Join(errs ...error) error` combines several errors and discards nil ones,
returning nil when all are nil.

**Repo conventions that apply:**

- This file explains *why* a decision was made, not what a line does — match the
  existing comments on `consoleSink`.
- Tests use the file's own helpers: `consoleRecord(level, msg, attrs...)`,
  `renderConsole`, and a `writerFunc` adapter (already defined in
  `console_log_test.go`) for injecting failing writers. Follow
  `TestConsoleLogHandlerMultipleSinks` as the structural pattern — it is the test
  that already asserts both sinks receive a line.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Format | `gofmt -l cmd pkg` | only `pkg/tui/render.go` (pre-existing at `eafaf46`) |
| Focused tests | `CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain/... -count=1 -run 'ConsoleLog|StderrLogHandler|InstallGateway' -v` | all `PASS` |
| Package tests | `CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain/... -count=1` | `ok` |
| Race | `CGO_ENABLED=1 go test -tags fts5 -race ./cmd/forebrain/... -count=1` | `ok` |

## Scope

**In scope** (the only files you should modify):

- `cmd/forebrain/console_log.go`
- `cmd/forebrain/console_log_test.go` (add new tests only)

**Out of scope** (do NOT touch):

- `cmd/forebrain/gateway_log.go` — the file is *opened* there; the fan-out
  semantics belong to the handler constructor. (Plan 001 edits that file for
  permissions; do not re-edit its options here.)
- The record **format** (`appendLine`, `messageText`, the colour policy, value
  quoting) — settled by plan 003 and the approved design.
- The non-TTY `slog.NewTextHandler` **format** choice. Only how many destinations
  it writes to changes, not what the line looks like.
- `pkg/telemetry` — `NewSlogTeeHandler` already fans out independently to
  `error.log` and must keep doing so untouched.
- Retry/backoff, rate-limited error reporting, or "disable a sink after N
  failures". There is no evidence any destination needs that, and this repo does
  not add machinery for states it has not observed.

## Git workflow

- Branch: `fix/console-log-sink-resilience`
- One commit, Conventional Commits + `-s`. Suggested:
  `fix(gateway): keep writing the log file when the terminal sink fails`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Make `Handle` attempt every sink

Replace the aborting loop. Write to every destination, collect the errors, and
return them joined. Keep the lock held across all destinations so two goroutines
cannot interleave records between sinks (that is the existing guarantee — see the
`mu` comment on the type).

```go
func (h *consoleLogHandler) Handle(_ context.Context, r slog.Record) error {
	if len(h.sinks) == 0 {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// Every destination is attempted, always. A dead terminal (EPIPE from a
	// closed `tee`, a vanished tmux pane) must not cost the operator the record
	// that survives the session, and slog discards this error entirely, so
	// returning early would lose it silently.
	var errs []error
	for _, s := range h.sinks {
		var b strings.Builder
		h.appendLine(&b, r, s.color)
		if _, err := io.WriteString(s.w, b.String()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
```

Add `"errors"` to the imports if it is not already there.

**Verify**: `go build ./... && go vet ./cmd/forebrain/` → exit 0.

### Step 2: Write the durable destination first

In `stderrLogHandlerTo`, put the file sink ahead of the terminal sink in both
modes. The rationale goes in a comment, because the ordering is load-bearing:

```go
	// The durable destination is written first: if the process dies between the
	// two writes, the record that is supposed to outlive the session is the one
	// already on disk.
	if !rootIsTerminal(os.Stderr) {
		w := io.Writer(os.Stderr)
		if file != nil {
			w = multiWriter(file, os.Stderr)
		}
		return slog.NewTextHandler(w, &slog.HandlerOptions{Level: level, AddSource: addSource})
	}
	sinks := []consoleSink{{w: os.Stderr, color: stderrColor()}}
	if file != nil {
		sinks = []consoleSink{{w: file}, {w: os.Stderr, color: stderrColor()}}
	}
	return newConsoleLogHandler(level, addSource, sinks...)
```

**Verify**: `go build ./...` → exit 0.

### Step 3: Replace `io.MultiWriter` with a non-aborting fan-out

`io.MultiWriter` stops at the first failure (see "Why this matters"), so it
reintroduces defect 1 in logfmt mode. Add a small fan-out writer next to
`consoleSink` and use it in Step 2:

```go
// multiWriter duplicates each write to every writer, and does so even when one
// of them fails: io.MultiWriter stops at the first error, which would let a
// broken terminal silence the log file on a headless or piped run. The errors
// are joined so a caller that inspects them still sees every failure.
func multiWriter(writers ...io.Writer) io.Writer {
	return writerFunc(func(p []byte) (int, error) {
		n := len(p)
		var errs []error
		for _, w := range writers {
			if w == nil {
				continue
			}
			if _, err := w.Write(p); err != nil {
				errs = append(errs, err)
			}
		}
		return n, errors.Join(errs...)
	})
}
```

`writerFunc` already exists in this package's test file
(`func (f writerFunc) Write(...)`) — **tests only**. Production code needs the
adapter in non-test form, so define the type in `console_log.go` if it is not
already there, **or** use a small named struct implementing `io.Writer` in
`console_log.go`. Do not import a test file's helper into production code, and do
not move `writerFunc` out of the test file if the tests use it (a same-named
production type in the same *package* would collide — check first with
`grep -rn "writerFunc" cmd/forebrain/` and pick a distinct name such as
`fanoutWriter` if there is a clash).

**Verify**:
`grep -n "io.MultiWriter" cmd/forebrain/console_log.go` → **no matches**;
`go build ./... && go vet ./...` → exit 0.

### Step 4: Full gates

**Verify**:
```
go build ./... && go vet ./... && gofmt -l cmd pkg
CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain/... -count=1
CGO_ENABLED=1 go test -tags fts5 -race ./cmd/forebrain/... -count=1
```
→ build/vet exit 0; `gofmt` lists only `pkg/tui/render.go`; both test runs `ok`.

## Test plan

New tests in `cmd/forebrain/console_log_test.go`. Use the existing `writerFunc`
adapter for failing writers and `consoleRecord` for frozen records.

1. `TestConsoleLogHandlerWritesEverySinkWhenOneFails` — construct
   `newConsoleLogHandler(level, false, consoleSink{w: failingWriter}, consoleSink{w: &file})`
   where `failingWriter` returns a non-nil error, call `Handle`, and assert:
   the file sink received the line, and `Handle` returned a non-nil error.
   This is the direct regression test for defect 1.
2. `TestConsoleLogHandlerWritesDurableSinkFirst` — two recorders that append to a
   shared, ordered observation slice; assert the durable sink's index precedes
   the terminal sink's. Pins the Step 2 ordering contract.
3. `TestMultiWriterKeepsGoingAfterAFailure` — call the new fan-out with a failing
   writer and a `bytes.Buffer`, assert the buffer got the payload and the returned
   error is non-nil. This is the regression test for defect 2, and it is the one
   that would have caught `io.MultiWriter`'s documented stop-on-error behaviour.
4. `TestConsoleLogHandlerHandleReturnsNoErrorOnSuccess` — sanity: two healthy
   sinks → `Handle` returns nil (`errors.Join` of no errors).

Structural pattern: `TestConsoleLogHandlerMultipleSinks` (same file) for the
multi-sink shape; the file's `writerFunc` for injection.

**Verification**:
`CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain/... -count=1 -run 'ConsoleLog|MultiWriter' -v`
→ all pass, including the 4 new tests and all pre-existing ones unchanged.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `grep -n "io.MultiWriter" cmd/forebrain/console_log.go` → no matches
- [ ] `grep -n "errors.Join" cmd/forebrain/console_log.go` → at least 2 matches (`Handle` and the fan-out)
- [ ] `grep -n "return err" cmd/forebrain/console_log.go` inside `Handle` → no early return on a sink write failure
- [ ] `go build ./...` exits 0
- [ ] `go vet ./...` exits 0
- [ ] `gofmt -l cmd pkg` prints only `pkg/tui/render.go`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain/... -count=1` → `ok`
- [ ] `CGO_ENABLED=1 go test -tags fts5 -race ./cmd/forebrain/... -count=1` → `ok`
- [ ] `git status --short` shows no modified file outside the in-scope list
- [ ] `docs/plan/LOGGING_HARDENING/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- `cmd/forebrain/console_log.go` does not contain the excerpts above (untracked
  work in progress may have moved).
- A pre-existing test — in particular `TestConsoleLogHandlerMultipleSinks` or
  `TestConsoleLogHandlerConcurrentWrites` — fails. Those pin the "every sink gets
  the record" and "records never interleave" contracts; a failure means the
  rewrite changed behaviour.
- Introducing the fan-out requires moving `writerFunc` out of the test file in a
  way that touches other tests. Report the collision instead of renaming things
  broadly.
- You find yourself adding retry logic, a failure counter, or a "muted sink"
  state. That is out of scope by design.
- `pkg/telemetry.NewSlogTeeHandler` turns out to share this code path. It does
  not today (it wraps an inner handler and opens `error.log` itself), but if the
  tree has changed, stop: the durable-destination ordering argument must be
  re-checked for the tee as well.

## Maintenance notes

- **Invariant to preserve: every destination is attempted for every record.** If
  a future sink is added (a syslog sink, a ring buffer for the web UI), it
  inherits this for free — as long as nobody reintroduces an early return or
  swaps the fan-out for `io.MultiWriter`/`io.Pipe`.
- **`slog` discards `Handle`'s error.** The joined error is therefore only
  observable to tests and to direct callers of `Handle`. Do not treat a returned
  error as "the record was not logged" — a partially-successful write still
  returns an error by design.
- **A permanently broken destination keeps being written to.** That is
  deliberate: a terminal can come back (a re-attached pane cannot, but a
  re-opened pipe can), and the cost of a failed write is a syscall. If a
  destination ever becomes expensive to fail (a network sink), it must carry its
  own sink-side damping rather than a global early return here.
- **Reviewer scrutiny**: the ordering change in Step 2 is the only behaviour
  change beyond error propagation — confirm the durable sink really is first in
  both the terminal and non-terminal branches, and that the `color` flag still
  travels with the terminal sink (not with position).
