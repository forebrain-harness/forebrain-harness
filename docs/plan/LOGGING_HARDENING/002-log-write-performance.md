# Plan 002: Make log-file writes stop paying a per-record fsync

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `docs/plan/LOGGING_HARDENING/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat eafaf46..HEAD -- pkg/telemetry/logfile.go pkg/telemetry/debuglog.go pkg/telemetry/levels.go cmd/forebrain/gateway_log.go`
> If any of these changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S–M
- **Risk**: LOW–MED
- **Depends on**: `docs/plan/LOGGING_HARDENING/001-log-file-permissions.md`
  (both edit `cmd/forebrain/gateway_log.go`; land 001 first so its `Perm:` line
  is already present and this plan only removes `SyncWrites:`)
- **Category**: perf
- **Planned at**: commit `eafaf46`, 2026-10-08

## Why this matters

Every record written to `debug.log`, `info.log` and `gateway.log` goes through
`telemetry.File.Write`, which does `fstat + write + fsync` **per record**, with
the fsync inside a process-global mutex. Measured on this repo's real code path
(Apple M4 / APFS SSD), one record costs **≈3.7 ms regardless of size**, and
concurrent writers get **zero parallelism**:

| Scenario (real `telemetry.File` path) | Cost per record |
|---|---|
| `debug.log` typical 37 KB record, **today** | **3,798 µs** |
| same record, per-record fsync removed | 20.8 µs (**183×**) |
| `gateway.log` access-log 120 B record, **today** | **3,697 µs** |
| same record, fsync removed | 3.9 µs (**938×**) |
| 8 concurrent writers, **today** | 3,741 µs/op (**serialized**) |
| 8 concurrent writers, fsync removed | 9.7 µs/op |

`debug.log` records average **~37 KB** (measured: `debug.log.1` = 20,932,542 B
over 565 records) and a 20 MB segment fills in ~10 minutes, i.e. ~1 record/s
steady state with bursts when subagents run in parallel.

This is *not* the same as "logging too much" — the cost is **a disk barrier per
record, on the caller's thread**, and the callers are the LLM request path
(each provider HTTP round trip writes a request block *and* a response block;
each tool call writes two more) and the HTTP response path (`withAccessLog`
writes before the handler returns, so **every** response — including a page
load's ~50 sub-requests — waits on a disk barrier, serially, behind one lock).
Note the access log reports `duration` taken *before* its own write, so the log
under-reports the latency it adds.

**Root cause, and why removing the fsync is correct rather than a tradeoff
being smuggled in**: `write(2)` to an `O_APPEND` descriptor already survives
process death (it is in the page cache), and `tail -f` sees it immediately.
fsync only adds *power-loss* durability, which a diagnostic log does not need.
The same package's own forensic logs (`error.log`: `paniclog.go`,
`error_write.go`, `slog_handler.go`) already write with a bare `WriteString` and
**no fsync at all**. **The owner has reviewed and accepted this durability
tradeoff** (loss window = the last few seconds on power loss/kernel panic;
process crash loses nothing). Do not re-litigate it in review comments.

## Current state

Files involved:

- `pkg/telemetry/logfile.go` — the shared rotating-log primitive (`File`,
  `Options`, `Open`, `WriteRaw`, `RotateOnce`, `RotateActive`). **Where the fix
  goes.**
- `pkg/telemetry/debuglog.go` — opens `debug.log`.
- `pkg/telemetry/levels.go` — `OpenLevelLogger` (info.log + debug.log for the TUI).
- `cmd/forebrain/gateway_log.go` — opens `gateway.log`.

The hot path, verbatim (`pkg/telemetry/logfile.go`):

```go
// :86-95  every record: WriteRaw (fstat + write) then fsync
func (f *File) Write(p []byte) (int, error) {
	if f == nil {
		return 0, os.ErrInvalid
	}
	n, err := WriteRaw(f.raw, p, f.options.MaxBytes, f.options.Backups)
	if err == nil && f.options.SyncWrites {
		err = f.Sync()
	}
	return n, err
}
```

```go
// :128-131  one lock guards BOTH the startup-rotation dedupe table AND every record write
var (
	rotateMu sync.Mutex
	rotated  = map[string]struct{}{}
)
```

```go
// :183-206  the shared write helper: takes the global lock and fstats on every call
func WriteRaw(file *os.File, p []byte, maxBytes int64, backups int) (int, error) {
	if file == nil {
		return 0, os.ErrInvalid
	}
	options := (Options{MaxBytes: maxBytes, Backups: backups}).normalized()
	originalLen := len(p)
	if int64(len(p)) > options.MaxBytes {
		budget := int(options.MaxBytes)
		if budget > 256 {
			budget -= 128
		}
		p = []byte(BoundText(string(p), budget))
	}
	rotateMu.Lock()
	defer rotateMu.Unlock()
	if info, err := file.Stat(); err == nil && info.Size()+int64(len(p)) > options.MaxBytes {
		rotateActiveLocked(file, options.MaxBytes, options.Backups)
	}
	n, err := file.Write(p)
	if err == nil && originalLen != len(p) {
		return originalLen, nil
	}
	return n, err
}
```

`SyncWrites` is defined at `logfile.go` (`SyncWrites bool` in `Options`) and set
in exactly three places:

| File | Call |
|---|---|
| `pkg/telemetry/debuglog.go` | `Options{ExistingParentOnly: true, SyncWrites: true}` |
| `pkg/telemetry/levels.go` | `options.SyncWrites = true` inside `OpenLevelLogger` (unconditional) |
| `cmd/forebrain/gateway_log.go` | `Options{Perm: 0o600, ExistingParentOnly: true, SyncWrites: true}` (after plan 001) |

The gateway's log path really does land on this code: `consoleLogHandler.Handle`
writes with `io.WriteString(s.w, …)`, and `*telemetry.File` has **no**
`WriteString` method (only `Write`), so `io.WriteString` degrades to
`File.Write([]byte)` → `WriteRaw` → fstat + write + fsync.

### Evidence that the fsync is not a deliberate invariant

| Location | Write path |
|---|---|
| `pkg/telemetry/slog_handler.go` (`error.log`, slog ERR+) | `writeMu` + `os.OpenFile` + `f.WriteString` — **no Sync** |
| `pkg/telemetry/paniclog.go` (`error.log`, panics) | same — **no Sync** |
| `pkg/telemetry/error_write.go` (`error.log`, errors) | same — **no Sync** |
| `pkg/tui/render.go` (`telemetry.RawWriter{File: f}`) | `WriteRaw(...)` directly — **no SyncWrites branch, so no fsync today either** |

### Repo conventions that apply

- `pkg/telemetry/logfile.go`'s package comment (`:1-6`) enumerates the package's
  responsibilities; it must stay accurate.
- Syscall/locking rationale in this package is written as a comment **on the
  type or the lock**, explaining *why* — see the `Registry.bindMu` comment style
  in `pkg/channel/channel.go:54-64` for the tone and depth to match.
- Tests live beside the code (`pkg/telemetry/logfile_test.go`) and use
  `t.TempDir()`; see `TestOpenCreatesParentAndRotatesDuringLifetime`
  (`logfile_test.go:18-42`) as the structural exemplar for rotation behaviour.
- Existing invariant the tests rely on: a record longer than `MaxBytes` is
  bounded by `BoundText` and `Write` still returns the **original** length
  (`logfile.go:202-204`, covered by `TestWriteRawBoundsSingleRecord`).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Format | `gofmt -l cmd pkg` | only `pkg/tui/render.go` (pre-existing at `eafaf46`) |
| Telemetry tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/... -count=1` | `ok` |
| Race | `CGO_ENABLED=1 go test -tags fts5 -race ./pkg/telemetry/ -count=1` | `ok` |
| Benchmarks (before/after) | `CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/ -run '^$' -bench 'BenchmarkFileWrite' -benchtime 500x -count=1` | prints ns/op |
| Consumers | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/... ./cmd/forebrain/... -count=1` | `ok` |
| Package graph | `scripts/package-graph.sh` | no diff in `pkg/architecture/testdata/graph.json` |

## Scope

**In scope** (the only files you should modify):

- `pkg/telemetry/logfile.go`
- `pkg/telemetry/debuglog.go` (delete the `SyncWrites` field only)
- `pkg/telemetry/levels.go` (delete the `options.SyncWrites = true` line only)
- `cmd/forebrain/gateway_log.go` (delete the `SyncWrites` field only)
- `pkg/telemetry/logfile_test.go` (new tests + benchmarks)

**Out of scope** (do NOT touch, even though they look related):

- `WriteRaw`, `RawWriter`, `RotateOnce`, `RotateActive`, `BoundText`, `OpenRaw`
  — public/shared helpers with other callers (the TUI's redirected-descriptor
  path, `pkg/tui/render.go`). `WriteRaw` keeps its per-call `fstat`; it is not
  the hot path and changing it widens the diff.
- `File.Sync()` — keep the public method even though it loses its production
  caller. Removing a public API method is a separate decision.
- **Do not add userspace buffering** (`bufio.Writer`, an async writer queue, a
  background flush goroutine). Buffering would delay `tail -f` and *drop the
  tail on a crash* — exactly what `debug.log` exists to prevent. The whole point
  is that `write(2)` already gives crash-durability without fsync.
- Do not change any log **format**, level, file name, rotation parameter
  (20 MB × 3), `DefaultRecordBytes`, or `ExistingParentOnly` semantics.
- Do not change *what* is logged anywhere (no trimming of debug.log content).

## Git workflow

- Branch: `perf/log-file-write-path`
- One commit (or two: primitive + call sites). Conventional Commits + `-s`.
  Suggested subject: `perf(telemetry): stop fsyncing every log record`
  with a `Why:` / `What:` / `Verification:` body quoting the before/after
  benchmark numbers you actually measured.
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Record the "before" numbers

Run the consumer suites once so a later failure is unambiguous, then capture a
baseline of the existing behaviour **before** changing anything:

```
CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/... ./pkg/tui/... ./cmd/forebrain/... -count=1
```

**Verify**: all three packages report `ok`. Keep this output; a package that is
already failing before your change is a STOP condition (report it, do not fix it
inside this plan).

### Step 2: Give `File` its own lock and byte counter

In `pkg/telemetry/logfile.go`, extend the type and initialize the counter in
`Open`:

```go
// File is an append-only log whose writes and lifecycle rotation are bounded.
type File struct {
	raw     *os.File
	options Options
	// mu serializes writes and rotation for THIS file. Rotation needs the
	// package-level rotateMu as well, but that lock is taken once per rotation
	// (every MaxBytes), not once per record: a global lock on the write path
	// would serialize every log file in the process behind one another.
	mu sync.Mutex
	// size is the number of bytes this File has written to raw's path. It
	// replaces an fstat per record; see writeLocked for the bounded drift.
	size int64
}
```

In `Open`, after `OpenRaw` succeeds, seed `size` from one `Stat` (the startup
rotation has just run, so this is the true size):

```go
func Open(path string, options Options) (*File, error) {
	options = options.normalized()
	raw, err := OpenRaw(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, options.Perm, options)
	if err != nil {
		return nil, err
	}
	f := &File{raw: raw, options: options}
	if info, statErr := raw.Stat(); statErr == nil {
		f.size = info.Size()
	}
	return f, nil
}
```

**Verify**: `go build ./...` → exit 0.

### Step 3: Move the write body onto the per-file lock, with the counter

Replace `File.Write` with a `writeLocked` body. **This is the load-bearing step**;
reproduce the contract exactly: bound an oversized record the way `WriteRaw`
does, return the **original** length when bounding occurred, and rotate only when
the counter says the next write would cross the limit.

```go
// Write appends one bounded record.
//
// Records are written straight to the descriptor: no userspace buffering, and
// deliberately no fsync. Buffering would delay `tail -f` and lose the tail on a
// crash; an fsync per record buys only power-loss durability, which a diagnostic
// log does not need, at the cost of a disk barrier on the caller's thread (the
// LLM request path and the HTTP response path both call this). The package's own
// error.log has always written this way. write(2) already survives process death.
func (f *File) Write(p []byte) (int, error) {
	if f == nil {
		return 0, os.ErrInvalid
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writeLocked(p)
}

// writeLocked is Write's body, called with f.mu held.
func (f *File) writeLocked(p []byte) (int, error) {
	originalLen := len(p)
	if int64(len(p)) > f.options.MaxBytes {
		budget := int(f.options.MaxBytes)
		if budget > 256 {
			budget -= 128
		}
		p = []byte(BoundText(string(p), budget))
	}
	if f.size+int64(len(p)) > f.options.MaxBytes {
		// f.size counts only what this File wrote, so it can drift when another
		// writer touches the same path (the TUI's StartPeriodicRotation does).
		// Ask the filesystem once, but only when the counter says a rotation may
		// be due — not on every record.
		if info, err := f.raw.Stat(); err == nil {
			f.size = info.Size()
		}
		if f.size+int64(len(p)) > f.options.MaxBytes {
			rotateMu.Lock()
			rotateActiveLocked(f.raw, f.options.MaxBytes, f.options.Backups)
			rotateMu.Unlock()
			f.size = 0
		}
	}
	n, err := f.raw.Write(p)
	f.size += int64(n)
	if err == nil && originalLen != len(p) {
		return originalLen, nil
	}
	return n, err
}
```

Add the drift reasoning as a comment on the type or on the `size` field (the
`Registry.bindMu` comment in `pkg/channel/channel.go:54-64` is the tone to
match). The bounded argument you must state, because a reviewer will ask:
an external writer that **truncates** (the TUI's rotation is copy-and-truncate on
the same inode) makes `f.size` an over-estimate → at worst we rotate early;
an external writer that **appends** makes it an under-estimate → at worst the
active file exceeds `MaxBytes` until that writer's own 2-second rotation brings
it back, so the file stays bounded either way. That is why "stat only when the
counter triggers" is sufficient and a periodic stat timer is not needed.

**Verify**: `go build ./... && go vet ./pkg/telemetry/` → exit 0, no diagnostics.

### Step 4: Delete the `SyncWrites` semantics

Remove the field and all three settings — this repo deletes a replaced semantic
completely rather than leaving a no-op knob:

1. `pkg/telemetry/logfile.go`: delete `SyncWrites bool` from `Options` **and**
   the `err = f.Sync()` branch (already gone if you wrote Step 3 as above).
2. `pkg/telemetry/debuglog.go`: `Options{ExistingParentOnly: true, SyncWrites: true}`
   → `Options{ExistingParentOnly: true}`.
3. `pkg/telemetry/levels.go`: delete the line `options.SyncWrites = true`.
4. `cmd/forebrain/gateway_log.go`:
   `Options{Perm: 0o600, ExistingParentOnly: true, SyncWrites: true}`
   → `Options{Perm: 0o600, ExistingParentOnly: true}`.

Keep `func (f *File) Sync() error` and its body.

**Verify**: `grep -rn "SyncWrites" pkg/ cmd/` → **no matches**;
`go build ./... && go vet ./...` → exit 0.

### Step 5: Make the package comment true again

`pkg/telemetry/logfile.go`'s package doc currently ends its list of
responsibilities with "syncing", which is no longer the default. Reword so the
doc matches the code (keep `File.Sync` available and documented as an explicit,
on-demand call):

```go
// Package logfile owns Forebrain Harness's bounded, rotating file-log primitives.
//
// Callers decide what to log and which severity/file receives it. This package
// is the single owner of file creation, record bounds, runtime rotation for
// redirected descriptors, and closing. Records reach the descriptor unbuffered
// and unsynced by design (see File.Write); File.Sync is the explicit,
// on-demand durability call.
```

**Verify**: `go doc ./pkg/telemetry | head -12` → the new wording appears.

### Step 6: Full gates and benchmark after

```
CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/... ./pkg/tui/... ./cmd/forebrain/... -count=1
CGO_ENABLED=1 go test -tags fts5 -race ./pkg/telemetry/ -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/ -run '^$' -bench 'BenchmarkFileWrite' -benchtime 500x -count=1
CGO_ENABLED=1 go test -tags fts5 ./... -count=1
scripts/package-graph.sh
```

**Verify**: every package `ok`; `-race` clean; benchmarks show 120 B records in
the **µs** range (not ms); `git diff --stat pkg/architecture/testdata/graph.json`
empty.

Also confirm the prompt-cache invariant this repo guards:
`git diff --stat -- pkg/agent pkg/assembly pkg/llm pkg/turn/prompt* internal/agentrun`
→ **empty**.

## Test plan

New tests in `pkg/telemetry/logfile_test.go` (same package, same `t.TempDir()`
style as `TestOpenCreatesParentAndRotatesDuringLifetime`):

1. `TestWriteIsVisibleToAnotherReaderBeforeClose` — **pins the decision to stay
   unbuffered**. `Open` a file, `Write` a record, then, **without `Close` and
   without `Sync`**, open the same path read-only from the test and assert the
   record is there. This is the regression guard against someone later "fixing"
   this with a `bufio.Writer`.
2. `TestWriteRotatesUsingTheByteCounter` — `Options{MaxBytes: 1024, Backups: 2}`
   (plus `Perm: 0o600`), write ~10 records of 256 B. Assert: the active file size
   is `<= 1024`, `path+".1"` exists and is non-empty, and every `Write` returned
   the original length.
3. `TestWriteReturnsOriginalLengthForOversizedRecord` — one 4 KB record with
   `MaxBytes: 1024`; assert the returned count equals the 4 KB input length and
   the file stayed `<= 1024` (mirrors `TestWriteRawBoundsSingleRecord` but goes
   through `File.Write`).
4. `TestWriteThroughputIsNotFsyncBound` — a coarse **regression gate** so the
   fsync cannot be reintroduced: write 2,000 records of 120 B and assert the
   whole loop finishes in **< 2 s**. Today this takes ~7.4 s (2,000 × 3.7 ms) and
   fails; after the fix it takes ~10 ms, so the margin is ~200×. Put a comment on
   the test saying exactly that: it is a barrier detector, not a precise
   performance assertion, and the margin is deliberately loose to avoid flakes.
   Use `time.Since` inside the test, not `testing.B`.

Benchmarks (not asserted, recorded in the plan's completion note):

- `BenchmarkFileWrite120B`, `BenchmarkFileWrite37KB`,
  `BenchmarkFileWriteParallel8` — model them on the existing
  `TestWriteRawBoundsSingleRecord` setup; open via `Open` so the real path is
  exercised, and loop `f.Write` on a pre-built 120 B / 37 KB payload.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `grep -rn "SyncWrites" pkg/ cmd/` → no matches
- [ ] `go build ./...` exits 0
- [ ] `go vet ./...` exits 0
- [ ] `gofmt -l cmd pkg` prints only `pkg/tui/render.go`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/... -count=1` → `ok`, including the 4 new tests
- [ ] `CGO_ENABLED=1 go test -tags fts5 -race ./pkg/telemetry/ -count=1` → `ok`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → all packages `ok`
- [ ] `scripts/package-graph.sh` leaves `pkg/architecture/testdata/graph.json` unchanged
- [ ] `git diff --stat -- pkg/agent pkg/assembly pkg/llm pkg/turn/prompt* internal/agentrun` → empty
- [ ] `grep -n "fsync" pkg/telemetry/logfile.go` → only the explanatory comment on `File.Write`
- [ ] `git status --short` shows no modified file outside the in-scope list
- [ ] `docs/plan/LOGGING_HARDENING/README.md` status row updated, with the measured before/after numbers in the completion note

## STOP conditions

Stop and report back (do not improvise) if:

- The excerpts in "Current state" do not match the live `pkg/telemetry/logfile.go`
  (line numbers will have moved — match on the code, not the numbers).
- `TestOpenCreatesParentAndRotatesDuringLifetime` or
  `TestWriteRawBoundsSingleRecord` fails after Step 3. Rotation semantics and the
  "return the original length" contract are load-bearing for other callers; a
  failure means the rewrite changed behaviour, not that the test is stale.
- Removing the per-record rotation `fstat` makes the active file exceed
  `MaxBytes` in a way test 2 cannot express — i.e. you find a *third* writer path
  to the same file that neither `startupRotation` nor the TUI's periodic rotation
  covers. Report it with the writer's `file:line`.
- You conclude the fix needs buffering or an async writer to hit the numbers.
  It does not; that is a different (rejected) design — stop and report.
- `scripts/package-graph.sh` produces a diff in `graph.json`. This plan adds no
  package and no import, so a diff means scope creep somewhere.

## Maintenance notes

- **The durability contract changed, deliberately and with owner sign-off**:
  the last few seconds of any log file can be lost on power loss or kernel
  panic. A process crash, `kill -9`, or `SIGKILL` loses nothing (page cache).
  A reviewer should re-confirm this is still the accepted policy, not silently
  "restore" the fsync.
- **`f.size` is a hint, not the truth.** If a future change adds a writer that
  appends to a path a `File` also owns without going through `File.Write`, the
  active file can exceed `MaxBytes` until the next rotation. The comment on the
  field must name that contract; if a new writer appears, update it.
- **`WriteRaw`/`RawWriter` still fstat per call** (the TUI's redirected-descriptor
  path). That is intentional and low-cost (~0.7 µs); unify only if that path ever
  becomes hot.
- **Interaction with plan 007** (`console-handler-layering`): moving the console
  handler into `pkg/telemetry` does not touch this write path, but the handler's
  sinks are `io.Writer`s — if someone later wraps the `*File` in a buffered
  writer there, the fsync cost comes back through the front door. That is what
  test 1 guards, in the package that owns the rule.
- **How to re-measure** (do not assert these in CI):
  `CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/ -run '^$' -bench BenchmarkFileWrite -benchtime 500x -count=1`.
  Absolute numbers are device-dependent (an HDD or a container overlayfs is far
  worse than this machine's SSD); the portable claims are the *ratio* and the
  parallel-scaling result.
