# Plan 005: Make a panicking request visible and diagnosable

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `docs/plan/LOGGING_HARDENING/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat eafaf46..HEAD -- pkg/gateway/access_log.go pkg/gateway/http_server.go pkg/gateway/access_log_test.go`
> `access_log.go` and `access_log_test.go` are **untracked new files**;
> `http_server.go` is modified in the working tree. Compare the "Current state"
> excerpts against the live code; on a mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S–M
- **Risk**: LOW–MED
- **Depends on**: `docs/plan/LOGGING_HARDENING/003-console-format-fidelity.md`
  (a net/http panic report is inherently multi-line; plan 003's message escaping
  is what keeps it to a single log record, which this plan's step 4 relies on)
- **Category**: observability
- **Planned at**: commit `eafaf46`, 2026-10-08

## Why this matters

The gateway's access log promises "one line per request, nothing skipped" — the
plan that introduced it states the middleware sits outermost "so nothing is
skipped — REST routes, static assets, channel webhooks, the websocket, and the
rate limiter's own rejections all reach it." A request whose handler **panics**
is the one case that promise does not hold, and it is the case an operator needs
most:

1. `withAccessLog` emits its record *after* `next.ServeHTTP` returns. A panic
   unwinds through it, so **no access-log line is written at all** — the request
   leaves no trace of having happened.
2. `pkg/gateway` contains **no `recover()` anywhere**, and the gateway's
   `http.Server` sets no `ErrorLog`. net/http's own per-connection recovery then
   prints the panic and its stack through the **standard library `log` package**
   straight to raw stderr — bypassing the slog handler entirely, so the panic and
   its stack never reach `gateway.log` either. On a gateway run under systemd or
   in a container with stderr rotated away, a panicking endpoint is effectively
   invisible.

After this plan: a panicking request produces **one access-log record** (at ERROR
level, carrying the panic value) and the panic's **stack trace arrives through
slog**, so both land in the terminal and in `<home>/logs/gateway.log`.

## Current state

Files:

- `pkg/gateway/access_log.go` (untracked) — `withAccessLog` and `statusRecorder`.
- `pkg/gateway/http_server.go` (modified in the working tree) — `NewRestServer`
  builds the `*http.Server` the gateway runs. **Sets no `ErrorLog`.**
- `pkg/gateway/access_log_test.go` (untracked) — existing access-log tests.

Current `withAccessLog` (the record is emitted only on normal return):

```go
func withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		slog.Info("request",
			"method", r.Method,
			// The path alone: a query string is free text from the caller and
			// routinely carries credentials.
			"path", r.URL.Path,
			"status", rec.status(),
			"duration", formatAccessDuration(time.Since(start)),
			"bytes", rec.written,
			"remote", clientIP(r),
		)
	})
}
```

`NewRestServer`'s server literal (no `ErrorLog`; `http_server.go`):

```go
	return &RestServer{
		router: router,
		Server: &http.Server{
			Addr: addr,
			// No write timeout: streaming endpoints (SSE, websockets) must
			// not be cut off mid-response.
			ReadHeaderTimeout: 60 * time.Second,
			IdleTimeout:       120 * time.Second,
			Handler:           router,
		},
	}
```

Facts you should not have to re-derive:

- `grep -rn "recover()" pkg/gateway/` → **no matches** (verified): nothing in the
  gateway recovers, so a panic is net/http's to handle.
- `grep -rn "ErrorLog" pkg/gateway/` → **no matches** (verified).
- net/http's per-connection recovery logs through `Server.ErrorLog` when set, and
  otherwise through the package-level `log` output (raw stderr). Setting
  `ErrorLog` is the supported way to route it.
- `slog.NewLogLogger(h Handler, level Level) *log.Logger` bridges the legacy
  `log` API to a slog handler. In Go 1.26.6 its writer trims exactly one trailing
  newline (`$GOROOT/src/log/slog/logger.go:98-101`: `// Remove final newline.`);
  interior newlines are passed through untouched, which is why the panic report
  needs plan 003's message escaping to stay a single record.
- `slog.NewLogLogger` captures a source PC, so the console handler's trailing
  `· file:line` works for these records too.

**Repo conventions that apply:**

- `statusRecorder` exists to keep the wrapped writer's capabilities reachable
  (it implements `Hijack`, `Flush`, `Unwrap`); this plan must not break that.
- Access-log tests capture records by installing a capturing `slog` default in the
  test (`captureAccessLog`, `captureHandler`, `requestRecords`, `recordAttrs` in
  `access_log_test.go`) — reuse those helpers rather than inventing new ones.
- Middleware comments in `pkg/gateway` state the *reason* for the position in the
  chain; keep that style (see the comment above the `withAccessLog` call in
  `controlplane.go`).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Format | `gofmt -l cmd pkg` | only `pkg/tui/render.go` (pre-existing at `eafaf46`) |
| Focused tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -count=1 -run 'AccessLog|Panic' -v` | all `PASS` |
| Package tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... -count=1` | `ok` |
| Package graph | `scripts/package-graph.sh` | no diff |

## Scope

**In scope** (the only files you should modify):

- `pkg/gateway/access_log.go`
- `pkg/gateway/http_server.go` (one field in the server literal + a helper)
- `pkg/gateway/access_log_test.go` (add tests)

**Out of scope** (do NOT touch):

- **Do not add a recovery middleware.** Recovering here would mean writing a 500
  into a response that may already be partly sent, and would change net/http's
  documented behaviour (close the connection, log, keep serving). This plan
  *observes* the panic and lets it propagate unchanged.
- `pkg/gateway/controlplane.go` — the access log's position in the chain is
  settled; plan 006 moves the middleware itself.
- The gateway's other servers/handlers (`HandleChatWS`, channel webhooks) — a
  panicking websocket handler is a different problem; do not widen this plan.
- `ServeHTTPChain`'s rate limiter, auth, or routing logic.
- Any change to `statusRecorder`'s hijack/flush/unwrap behaviour.

## Git workflow

- Branch: `fix/gateway-panic-observability`
- One commit, Conventional Commits + `-s`. Suggested:
  `fix(gateway): report panicking requests and their stack through slog`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Emit the access-log record even when the handler panics

Restructure `withAccessLog` so the record is emitted from a `defer`, which runs
during unwinding as well as on normal return. Read the panic value with
`recover()` **and re-panic** — that preserves net/http's handling exactly while
letting the record carry the value. Do not swallow it.

```go
func withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		// The record is written from a defer so a panicking handler still leaves
		// one: a request that crashed an endpoint is the request an operator most
		// needs to see, and it is the one an emit-on-return path loses. The panic
		// is read with recover() only to name it in the record and is then
		// re-panicked, so net/http still closes the connection and keeps serving
		// exactly as it would have.
		defer func() {
			recovered := recover()
			attrs := []any{
				"method", r.Method,
				// The path alone: a query string is free text from the caller and
				// routinely carries credentials.
				"path", r.URL.Path,
				"status", rec.status(),
				"duration", formatAccessDuration(time.Since(start)),
				"bytes", rec.written,
				"remote", clientIP(r),
			}
			if recovered != nil {
				slog.Error("request panicked", append(attrs, "panic", recovered)...)
				panic(recovered)
			}
			slog.Info("request", attrs...)
		}()
		next.ServeHTTP(rec, r)
	})
}
```

Notes the executor must keep true:

- The `panic(recovered)` must come **after** the log call and must re-raise the
  same value.
- `recover()` returns nil on the normal path, which is how the two branches are
  distinguished — do not add a separate flag.
- `slog.Any`-style key `panic` is fine; the value is rendered by the handler
  (`fmt.Sprint` semantics), and plan 003's value quoting handles multi-line panic
  values.

**Verify**: `go build ./... && go vet ./pkg/gateway/` → exit 0.

### Step 2: Give the record a truthful status on the panic path

`statusRecorder.status()` returns `200` when nothing was written, which is what
net/http's recovery leaves behind for a panic before the first byte (the client
gets a reset connection, not a 200). Extend `status()` so a panic-only request is
not reported as success, without changing the normal paths:

```go
// status is what the access log reports. A handler that wrote nothing still
// answered 200, and a hijacked connection is no longer an HTTP response at
// all — for this gateway that is always a websocket upgrade, which answers
// 101. A handler that panicked before writing anything answered nothing.
func (r *statusRecorder) status() int {
	if r.hijacked {
		return http.StatusSwitchingProtocols
	}
	if r.code == 0 {
		return http.StatusOK
	}
	return r.code
}
```

Implement the panic case by adding a field and a setter that Step 1 calls before
logging, e.g.:

```go
	// panicked records that the handler crashed before a response was sent; the
	// access log reports 500 for it rather than the 200 an untouched recorder
	// would imply, because that is the outcome the client experienced.
	panicked bool
```

and in `status()`:

```go
	if r.panicked && r.code == 0 {
		return http.StatusInternalServerError
	}
```

Set `rec.panicked = true` in the deferred func when `recovered != nil` (before
reading `rec.status()`).

**Verify**: `go build ./... && go vet ./pkg/gateway/` → exit 0.

### Step 3: Route net/http's own panic report through slog

Add a helper and wire it into the server literal in `NewRestServer`
(`pkg/gateway/http_server.go`). The handler is captured when the server is built,
which is after the process installed its console+file handler
(`cmd/forebrain/serve.go` → `installGatewayConsoleLog`), so the stack lands in
both destinations.

In `pkg/gateway/access_log.go`:

```go
// gatewayErrorLog routes the HTTP server's own error reports — a panicking
// handler's stack, TLS handshake failures, a superfluous WriteHeader — through
// slog, so they reach the console and the gateway's log file instead of the
// standard library's raw stderr.
func gatewayErrorLog() *log.Logger {
	return slog.NewLogLogger(slog.Default().Handler(), slog.LevelError)
}
```

In `pkg/gateway/http_server.go`, inside the `NewRestServer` literal:

```go
		Server: &http.Server{
			Addr: addr,
			// The server's own error reports go through slog; see gatewayErrorLog.
			ErrorLog: gatewayErrorLog(),
			// No write timeout: streaming endpoints (SSE, websockets) must
			// not cut off mid-response.
			ReadHeaderTimeout: 60 * time.Second,
			IdleTimeout:       120 * time.Second,
			Handler:           router,
		},
```

Add `"log"` to the imports of `access_log.go`.

Do **not** touch the package-level `log.SetOutput` or install a `slog` default
from a library package: `pkg/gateway` must not rewire the process's logging.

**Verify**:
`grep -n "ErrorLog" pkg/gateway/http_server.go` → one match;
`go build ./... && go vet ./...` → exit 0.

### Step 4: Full gates

**Verify**:
```
go build ./... && go vet ./... && gofmt -l cmd pkg
CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... -count=1
scripts/package-graph.sh
```
→ build/vet exit 0; `gofmt` lists only `pkg/tui/render.go`; gateway tests `ok`;
no diff in `pkg/architecture/testdata/graph.json`.

## Test plan

New tests in `pkg/gateway/access_log_test.go`, reusing the file's existing
helpers (`captureAccessLog`, `requestRecords`, `recordAttrs`, `serveOnce`):

1. `TestWithAccessLogReportsPanickingRequest` — drive `withAccessLog` with a
   handler that panics (via `httptest.NewRecorder`, so no server needed), wrapped
   in a `defer/recover` in the test. Assert:
   - the panic still propagates (the test's recover sees it) — this is the
     "don't swallow it" contract;
   - **exactly one** record was emitted, at `slog.LevelError`, with
     `msg` = `request panicked`, and the fields `method`/`path`/`remote` correct,
     `status` = `500`, and a `panic` value present.
2. `TestWithAccessLogPanicAfterWriteKeepsTheRealStatus` — the handler writes
   `418` and *then* panics; assert the record's `status` is `418` (the response
   that was actually sent) and that the panic still propagates. This pins the
   `r.code != 0` branch of Step 2.
3. `TestNewRestServerRoutesHTTPErrorsThroughSlog` — assert the wiring:
   `NewRestServer("127.0.0.1:0").ErrorLog` is non-nil.
4. `TestGatewayErrorLogCarriesAPanicStackToSlog` — end-to-end proof that the
   helper actually reaches slog: install a capturing default logger
   (`captureAccessLog`), build `httptest.NewUnstartedServer(panicHandler)` and set
   `srv.Config.ErrorLog = gatewayErrorLog()` **before** `srv.Start()`, then issue
   one request and assert a record was captured whose message contains both the
   panic text and `goroutine` (the stack header). Use `httptest.NewUnstartedServer`
   because `TestNewRestServerRouting`-style tests cannot start `NewRestServer`
   without blocking on `Run`.
   **If plan 003 has landed**, additionally assert the captured message's record
   is emitted as **one** record (the stack's interior newlines are escaped) — that
   is the interaction the dependency notes.

Structural pattern: `TestWithAccessLogReportsHijackedUpgradeAs101` (same file) is
the closest existing shape for "drive the middleware and assert the record".

**Verification**:
`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -count=1 -run 'AccessLog|Panic|RestServer' -v`
→ all pass, including the 4 new tests.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `grep -n "defer func()" pkg/gateway/access_log.go` → present inside `withAccessLog`
- [ ] `grep -n "panic(recovered)" pkg/gateway/access_log.go` → one match
- [ ] `grep -n "ErrorLog" pkg/gateway/http_server.go` → one match
- [ ] `grep -rn "recover()" pkg/gateway/ | grep -v _test` → only the `withAccessLog` re-panic site (no recovery middleware added)
- [ ] `go build ./...` exits 0
- [ ] `go vet ./...` exits 0
- [ ] `gofmt -l cmd pkg` prints only `pkg/tui/render.go`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... -count=1` → `ok`
- [ ] `scripts/package-graph.sh` leaves `graph.json` unchanged
- [ ] `git status --short` shows no modified file outside the in-scope list
- [ ] `docs/plan/LOGGING_HARDENING/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- The excerpts do not match the live files (untracked work in progress may have
  changed).
- You find yourself making the middleware **recover** the panic (writing a 500,
  calling `http.Error`, or returning early). That is explicitly out of scope: it
  changes response semantics. Stop and report.
- `TestWithAccessLogReportsHijackedUpgradeAs101` or the `ServeHTTPChain` test
  fails. The deferred emission must not alter hijack/flush behaviour; a failure
  means the restructure changed the writer contract.
- `slog.NewLogLogger` proves unavailable or its trimming behaviour differs from
  what this plan documents (re-check with `go doc log/slog.NewLogLogger` and
  `$GOROOT/src/log/slog/logger.go`) — the one-record-per-line reasoning in step 4
  depends on it.
- Routing net/http's `ErrorLog` through slog produces duplicate panic reports
  through *two* paths (e.g. something else in the tree also recovers and logs).
  Report the duplication rather than silencing one side.

## Maintenance notes

- **The panic is re-raised deliberately.** net/http owns the recovery: it closes
  the connection and keeps the server serving. Anything that starts recovering in
  this middleware changes that contract for every endpoint at once.
- **`gatewayErrorLog()` captures the handler at server-construction time.** If a
  future change swaps the default slog logger *while a gateway is serving*
  (nothing does today — the TUI's swap happens only in the interactive path),
  net/http's reports would keep going to the old handler. If that ever becomes
  possible, resolve the handler at write time instead.
- **`status` for a panicking request is a chosen convention**: `500` when nothing
  was written, the real status when the handler had already responded. A reader
  parsing the access log should not assume a 500 means "a 500 body was sent".
- **Related, deliberately not in this plan**: the gateway's *other* in-process
  servers (the managed-network proxy, the MCP/provider OAuth callback listeners)
  have the same "no ErrorLog, no access log" hole. Plan 006 introduces the seam
  that lets them be covered; covering them is a step there.
- **Reviewer scrutiny**: confirm the deferred function cannot double-log (it must
  not run twice), that the panic value is re-raised unchanged, and that
  `statusRecorder`'s hijack path (101) is untouched.
