# Plan 003: Make the console log faithful to slog values and one-line-per-record

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `docs/plan/LOGGING_HARDENING/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat eafaf46..HEAD -- cmd/forebrain/console_log.go cmd/forebrain/console_log_test.go`
> Both files are **untracked new files** in the working tree (they do not exist
> at `eafaf46`). Compare the "Current state" excerpts against the live code; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none (but lands **before** plan 004, which edits the same file,
  and before plan 007, which moves it)
- **Category**: bug
- **Planned at**: commit `eafaf46`, 2026-10-08

## Why this matters

The new compact console handler in `cmd/forebrain/console_log.go` is the format
an operator reads live and the format that lands in `<home>/logs/gateway.log`.
Two fidelity defects make that record set unreliable:

1. **`slog.LogValuer` is never resolved.** The handler renders values with
   `slog.Value.String()`, while the standard library's handlers call
   `Value.Resolve()` first. A record logged with a `LogValuer` — the idiomatic
   way to log structured values, and the way the ecosystem's error/logging
   libraries (e.g. `samber/oops`-style errors, OTel attributes) expose
   themselves — renders as `err=&{...}` instead of its `LogValue()`. The repo has
   no `LogValuer` implementation *today*, so this is latent, but it silently
   produces unreadable output the first day one is adopted, and the fix is three
   lines.
2. **The message is written verbatim, so a newline in it destroys the
   one-record-per-line contract.** Attribute *values* are quoted when they need
   it (logfmt convention), but `r.Message` is not escaped at all. A message
   containing `\n` (multi-line error strings, embedded stack traces, provider
   error bodies are all common) turns one record into several physical lines —
   which breaks `grep`-by-record, breaks the file's "one line per record"
   structure, and cannot be parsed back. The previous handler
   (`slog.TextHandler`, which this replaced) quoted the message when it needed
   it, so this is also a small regression against the code it replaced.

## Current state

File: `cmd/forebrain/console_log.go` (untracked, created by the gateway console
log work — ~355 lines). It defines `consoleLogHandler`, which implements
`slog.Handler` and writes one line per record to one or more sinks (terminal,
log file), optionally colored.

Defect 1 — value rendering (`consoleLog.go`, `attrValue`, ends with):

```go
func attrValue(key string, v slog.Value, color bool) string {
	s := v.String()
	code := ""
	switch key {
	case "err", "error":
		code = "\x1b[31m"
	case "status":
		code = statusColor(s)
	case "duration":
		if d, err := time.ParseDuration(s); err == nil {
			code = durationColor(d)
		}
	case "bytes":
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			s = humanBytes(n)
		}
	}
	if s == "" || strings.ContainsAny(s, " =\"\t\r\n") {
		s = strconv.Quote(s)
	}
	if code != "" && color {
		return code + s + "\x1b[0m"
	}
	return s
}
```

There are **three** rendering paths that must resolve values, because values can
arrive bound (`WithAttrs`) or per-record:

```go
// appendLine, inside consoleLogHandler.appendLine  (:141-148)
	for _, a := range h.attrs {
		b.WriteByte(' ')
		writeAttr(b, a.key, a.val, color)
	}
	r.Attrs(func(a slog.Attr) bool {
		appendAttrLine(b, a, h.group, color)
		return true
	})
```

```go
// appendAttrLine  (:195-…) — recurses on groups, renders leaves
func appendAttrLine(b *strings.Builder, a slog.Attr, group string, color bool) {
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		g := a.Key
		if group != "" {
			g = group + "." + a.Key
		}
		for _, ia := range a.Value.Group() {
			appendAttrLine(b, ia, g, color)
		}
		return
	}
	key := a.Key
	if group != "" {
		key = group + "." + key
	}
	b.WriteByte(' ')
	writeAttr(b, key, a.Value, color)
}
```

Defect 2 — the message, written raw (`appendLine`, :133-140):

```go
	b.WriteByte(' ')
	if color && r.Level >= slog.LevelError {
		b.WriteString("\x1b[31m")
		b.WriteString(r.Message)
		b.WriteString("\x1b[0m")
	} else {
		b.WriteString(r.Message)
	}
```

**Reference behaviour to match** (standard library; recorded here because the
executor has not read it):

- `slog.Value.Resolve()` — in Go 1.26.6, `go doc log/slog.Value.Resolve`:
  "Resolve repeatedly calls LogValue on v while it implements LogValuer… If v
  resolves to a group, the group's attributes' values are not recursively
  resolved." The stdlib `TextHandler` calls it on every attribute in `Handle`
  (`$GOROOT/src/log/slog/handler.go`, `a.Value = a.Value.Resolve()`), i.e. at
  **render** time for both bound and record attributes.
- The stdlib quotes the message only when it needs quoting (embedded spaces are
  *fine* — messages are prose). This handler's own `attrValue` quotes values on
  `" =\"\t\r\n"`, which is right for `key=value` but **wrong for the message
  position**: quoting on spaces would turn `agent load` into `"agent load"` and
  reject the format this feature was designed around.

**Repo conventions that apply:**

- Golden tests assert the format **byte for byte** (SGR sequences included) and a
  separate sink carries no ESC byte — see `TestConsoleLogHandlerColorGolden` and
  `TestConsoleLogHandlerMultipleSinks` in `cmd/forebrain/console_log_test.go`.
  New tests must follow that shape.
- Comments in this file explain *why* a choice was made, not what the line does
  (match the existing `attrValue` doc comment).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Format | `gofmt -l cmd pkg` | only `pkg/tui/render.go` (pre-existing at `eafaf46`) |
| Focused tests | `CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain/... -count=1 -run 'ConsoleLog' -v` | all `PASS` |
| Package tests | `CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain/... -count=1` | `ok` |

## Scope

**In scope** (the only files you should modify):

- `cmd/forebrain/console_log.go`
- `cmd/forebrain/console_log_test.go` (add new tests; do not rewrite existing ones)

**Out of scope** (do NOT touch):

- The color policy table (`levelTag`, `statusColor`, `durationColor`,
  `humanBytes`) — its thresholds are a settled product decision.
- `attrValue`'s existing quoting rule for **values** (`" =\"\t\r\n"`). Changing
  it would rewrite every golden line in the test file.
- `consoleSink`, the multi-sink plumbing, and which sinks exist (plan 004 changes
  sink *failure* handling; plan 007 moves the whole file).
- The `sink`-write error handling (`Handle`'s early `return` on a write error) —
  that is plan 004's subject. Do not "tidy" it here.
- Any file outside `cmd/forebrain/`.

## Git workflow

- Branch: `fix/console-log-format-fidelity`
- One commit, Conventional Commits + `-s`. Suggested:
  `fix(gateway): render slog values and keep one log record per line`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Resolve values in all three rendering paths

Add resolution where each value is finally rendered. Do **not** resolve at bind
time (`appendConsoleAttrs`) — matching the stdlib means resolving at render time,
so a `LogValuer` is consulted once per record, not once per `WithAttrs`.

1. In `consoleLogHandler.appendLine`, the bound-attribute loop:

```go
	for _, a := range h.attrs {
		b.WriteByte(' ')
		writeAttr(b, a.key, a.val.Resolve(), color)
	}
```

2. In `appendAttrLine`, resolve **before** the group check, so a `LogValuer` that
   resolves to a group is flattened the same way a literal group is:

```go
func appendAttrLine(b *strings.Builder, a slog.Attr, group string, color bool) {
	if a.Equal(slog.Attr{}) {
		return
	}
	// Resolve before inspecting the kind: a LogValuer may produce a group, and
	// the stdlib resolves at render time for exactly this reason.
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		...
```

3. In `attrValue`, resolve defensively so the function is correct on its own
   (it is called from `writeAttr`, which callers also reach directly):

```go
func attrValue(key string, v slog.Value, color bool) string {
	s := v.Resolve().String()
	...
```

Add one comment above the resolution, in the file's existing why-not-what style:
values are resolved at render time because `slog.LogValuer` is the mechanism a
value uses to decide its own rendering, and the standard library resolves there
too.

**Verify**: `go build ./... && go vet ./cmd/forebrain/` → exit 0.

### Step 2: Escape control characters in the message

The message position is not a `key=value` value, so it must **not** inherit
`attrValue`'s space-quoting rule. Add a small helper next to `appendLine` and use
it in both branches (colored ERROR and plain):

```go
// messageText renders the record's message as a single physical line. The
// message is not a key=value value, so spaces stay as they are — prose reads
// badly quoted. Control characters do get escaped: a newline in a message
// (multi-line errors, embedded bodies) would otherwise turn one record into
// several lines and break both the file's one-record-per-line shape and any
// grep-by-record.
func messageText(msg string) string {
	if !strings.ContainsFunc(msg, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return msg
	}
	return strconv.Quote(msg)
}
```

then:

```go
	if color && r.Level >= slog.LevelError {
		b.WriteString("\x1b[31m")
		b.WriteString(messageText(r.Message))
		b.WriteString("\x1b[0m")
	} else {
		b.WriteString(messageText(r.Message))
	}
```

`strings.ContainsFunc` exists (Go 1.21+); the module targets Go 1.26.6.

**Verify**: `go build ./...` → exit 0.

### Step 3: Confirm existing goldens are unchanged

The two existing golden tests must pass **without modification** — a log message
with no control characters must render exactly as before (including the unquoted
`agent load` with its space).

**Verify**:
`CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain/... -count=1 -run 'ConsoleLogHandlerColorGolden|ConsoleLogHandlerPlainGolden|ConsoleLogHandlerValuePolicyByKey' -v`
→ all `PASS`. If any of these fail, your message change is too aggressive — stop
and re-read Step 2.

### Step 4: Full gates

**Verify**:
```
go build ./... && go vet ./... && gofmt -l cmd pkg
CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain/... -count=1
```
→ build/vet exit 0; `gofmt` lists only `pkg/tui/render.go`; package reports `ok`.

## Test plan

New tests in `cmd/forebrain/console_log_test.go`, using the file's existing
helpers `consoleRecord(level, msg, attrs...)` and
`renderConsole(t, color, record)` so the tests stay byte-exact:

1. `TestConsoleLogHandlerResolvesLogValuer` — define a test type implementing
   `slog.LogValuer` whose `LogValue()` returns a different value (e.g. a struct
   rendering as `redacted`), log it as `slog.Any("token", logValuer{})`, and
   assert the rendered line contains `token=redacted` and **not** `&{`.
   **Also cover the bound case**: `.With("k", logValuer{}).Info("m")` must
   resolve too (that is the `h.attrs` path in Step 1).
2. `TestConsoleLogHandlerKeepsOneLinePerRecord` — log a message containing
   `"\n"` and one containing `"\r\n"`; assert the rendered output splits into
   exactly **one** line per record (count `"\n"` occurrences: 1 per record, i.e.
   no interior newline) and that the escaped form contains `\n`/`\r`.
3. `TestConsoleLogHandlerDoesNotQuoteProseMessages` — a message with spaces
   (`"agent load"`) renders unquoted, while a message with a control character
   is quoted. This is the guard that keeps the format readable.

Structural pattern to follow: `TestConsoleLogHandlerPlainGolden`
(same file) — build a frozen record, render to a `bytes.Buffer`, assert the exact
string. For test 1's colour-free assertions use `renderConsole(t, false, rec)`.

**Verification**:
`CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain/... -count=1 -run 'ConsoleLogHandler' -v`
→ all pass, including the 3 new tests, and the pre-existing goldens unchanged.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `grep -n "Resolve()" cmd/forebrain/console_log.go` → at least 3 matches (bound path, `appendAttrLine`, `attrValue`)
- [ ] `grep -n "messageText" cmd/forebrain/console_log.go` → the helper plus 2 call sites
- [ ] `go build ./...` exits 0
- [ ] `go vet ./...` exits 0
- [ ] `gofmt -l cmd pkg` prints only `pkg/tui/render.go`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain/... -count=1` → `ok`
- [ ] The pre-existing golden tests pass **unmodified** (`git diff` shows no change to their bodies)
- [ ] `git status --short` shows no modified file outside the in-scope list
- [ ] `docs/plan/LOGGING_HARDENING/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- `cmd/forebrain/console_log.go` does not contain the excerpts above (the code is
  untracked work in progress — someone may have changed it since this plan).
- Step 3 fails: an existing golden line changed. The format is pinned by tests
  and by the approved design; do not update the goldens to match new output.
- `writeAttr`/`attrValue` turn out to have more callers than the three rendering
  paths named here (check with
  `grep -n "writeAttr(\|attrValue(" cmd/forebrain/console_log.go`) — extra
  callers need to be reasoned about, not blindly patched.
- You find yourself adding a `LogValue`-aware branch that *special-cases* one key
  name. The resolution belongs in the rendering path, for all keys.

## Maintenance notes

- **This file moves in plan 007.** Any follow-up work here should check whether
  plan 007 has landed and edit the new location (`pkg/telemetry`).
- **Resolving at render time is deliberate.** If someone moves resolution into
  `appendConsoleAttrs`/`WithAttrs` for "efficiency", a `LogValuer` that reads
  mutable state (a counter, a redaction toggle) will be frozen at bind time —
  diverging from stdlib behaviour. The comment on the resolution says why.
- **A `LogValuer` that resolves to a group** is flattened into dotted
  `key.subkey=value` pairs; the stdlib instead renders such a group as one
  quoted string. The dotted form is intentional here (it matches how the handler
  already renders `WithGroup`), but the divergence is worth a note if exact
  stdlib parity is ever required for a machine consumer.
- **Messenger escaping is escape-only, never reformatting**: messages are not
  wrapped, truncated, or quoted for spaces. If a future change wants to truncate
  very long messages, that belongs with `DefaultRecordBytes` policy in
  `pkg/telemetry`, not here.
- **Reviewer scrutiny**: the whole diff should be ~15 lines of production code
  plus tests; any change to the colour table or the value-quoting rule is out of
  scope.
