# Plan 006: Move the access-log middleware where every server can reach it, and use it

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `docs/plan/LOGGING_HARDENING/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat eafaf46..HEAD -- pkg/telemetry/ pkg/gateway/access_log.go pkg/gateway/http_server.go pkg/gateway/controlplane.go pkg/safety/managed_network.go pkg/mcp/oauth_listener.go pkg/architecture/testdata/graph.json`
> Several of these are untracked/modified working-tree files. Compare the
> "Current state" excerpts against the live code; on a mismatch, treat it as a
> STOP condition.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED (touches the sandbox's network proxy and three packages' import
  edges; the middleware itself is behaviour-preserving)
- **Depends on**:
  - `docs/plan/LOGGING_HARDENING/005-panic-request-observability.md` — this plan
    moves the middleware, so the panic handling and `ErrorLog` wiring must
    already be in it.
  - `docs/plan/LOGGING_HARDENING/003-console-format-fidelity.md` — for the same
    reason (the moved code includes the message/value fixes).
  - **Not** dependent on 002, but note: adopting the middleware on the sandbox
    proxy increases log volume, and 002 is what makes that volume cheap.
- **Category**: tech-debt / observability
- **Planned at**: commit `eafaf46`, 2026-10-08

## Why this matters

The per-request access log lives in `pkg/gateway/access_log.go`, so only the
gateway's HTTP chain has it. Three other HTTP servers run **inside the same
process** with no request logging and no `ErrorLog` wiring:

| Server | Where | What it serves | Visible today? |
|---|---|---|---|
| gateway chain | `pkg/gateway/controlplane.go` → `ServeHTTPChain` | REST, web UI, channels, `/ws/chat` | yes |
| **sandbox egress proxy** | `pkg/safety/managed_network.go:197` | **every network request a tool makes** | **no** (the file has *zero* `slog` calls) |
| MCP OAuth callback | `pkg/mcp/oauth_listener.go:39` | the OAuth redirect for an MCP server | no |
| provider OAuth callback | `pkg/llm/openai/auth.go:270` | the OpenAI login redirect | no — **cannot be covered by this seam**, see below |

The sandbox proxy is the significant one: in an agent that runs shell and network
tools, "which host did this tool contact, and what did the proxy answer" is
exactly the audit trail an operator needs, and today it does not exist. The
reason it has none is structural rather than deliberate — the only access log in
the repo is a private helper of one package.

This plan promotes the middleware to the package that already owns log plumbing
(`pkg/telemetry`), keeps its output **byte-identical**, and adopts it in the
three reachable servers. That also fixes the layering problem of a logging
concern living in a serving package (plan 007 does the same for the console
handler).

**Verified import constraints** (do not re-derive; re-check only if the tree has
moved):

- `go list -deps ./pkg/telemetry` → `{pkg/home, pkg/llm}` — so `telemetry` must
  **not** import `pkg/gateway`, `pkg/safety` or `pkg/mcp` (fine: it only needs
  `net/http`).
- `pkg/mcp` and `pkg/safety` do not import `telemetry` today, and neither the
  closure of `telemetry` (`home`, `llm`) nor of `llm` contains them → **adding
  `mcp → telemetry` and `safety → telemetry` is acyclic.**
- `telemetry` **imports `pkg/llm`** → `pkg/llm` **cannot** import `telemetry`.
  That is why the provider OAuth callback (`pkg/llm/openai/auth.go:270`) is out
  of reach here (see "Deferred").

## Current state

### The middleware to move (`pkg/gateway/access_log.go`, untracked)

```go
// withAccessLog reports every request the gateway answers, one INFO line each,
// with nginx access-log semantics: method, path, status, duration, bytes and
// remote address. It sits outermost in the chain so nothing is skipped — REST
// routes, static assets, channel webhooks, the websocket, and the rate
// limiter's own rejections all reach it.
func withAccessLog(next http.Handler) http.Handler { … }   // emits slog.Info("request", ...)
```

plus `statusRecorder` (status/bytes, with `Hijack`/`Flush`/`Unwrap`) and
`formatAccessDuration`. After plan 005 it also emits an ERROR record for a
panicking request and exposes `gatewayErrorLog()`.

The record's shape — which must not change, because it is already written to
`<home>/logs/gateway.log` and parsed by eye:

```go
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status(),
			"duration", formatAccessDuration(time.Since(start)),
			"bytes", rec.written,
			"remote", clientIP(r),
		)
```

### The client-IP rule (`pkg/gateway/http_server.go`)

```go
func clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}
```

It is used by **two** things: the access log and the gateway's rate limiter
(`rl.allow(clientIP(r))` in `ServeHTTPChain`). Moving the log without this helper
would leave two copies of "who is calling".

### The servers to adopt it in

`pkg/safety/managed_network.go` (the proxy; the handler is the proxy itself):

```go
	proxy.httpServer = &http.Server{Handler: proxy, ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = proxy.httpServer.Serve(httpListener) }()
```

`pkg/mcp/oauth_listener.go`:

```go
	srv := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
	}
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) { … })
	srv.Handler = mux
	go func() {
		_ = srv.Serve(ln)
	}()
```

`pkg/llm/openai/auth.go` (out of reach — recorded so nobody tries):

```go
	server := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	…
	if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
```

### Repo conventions that apply

- Package doc comments state each package's role and must stay accurate
  (`pkg/telemetry/doc.go`, and every package carries one — see `FOREBRAIN.md`
  "House rules").
- **The package graph is gated**: CI runs `scripts/package-graph.sh` and fails if
  `pkg/architecture/testdata/graph.json` differs. This plan *adds import edges*,
  so the regenerated graph **must be committed** with the change (unlike plans
  001–005, whose done criteria require no diff).
- `pkg/telemetry`'s tests are table-free, one behaviour per `Test…`, using
  `t.TempDir()` — see `pkg/telemetry/logfile_test.go`.
- Do not rewire the process's logging from a library package (no `slog.SetDefault`
  in `pkg/…`; the only legitimate callers are `cmd/forebrain` and the TUI).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Format | `gofmt -l cmd pkg` | only `pkg/tui/render.go` (pre-existing at `eafaf46`) |
| Package graph | `scripts/package-graph.sh` | rewrites `graph.json`; the diff must contain **only** the new edges |
| Telemetry tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/... -count=1` | `ok` |
| Gateway tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... -count=1` | `ok` |
| Safety tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/safety/... -count=1` | `ok` |
| MCP tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/mcp/... -count=1` | `ok` |
| Architecture test | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/... -count=1` | `ok` |

## Scope

**In scope**:

- `pkg/telemetry/accesslog.go` (create) — the middleware, `ClientIP`, `SlogErrorLog`
- `pkg/telemetry/accesslog_test.go` (create)
- `pkg/telemetry/doc.go` (one line, if the role description needs it)
- `pkg/gateway/access_log.go` — **delete** what moved
- `pkg/gateway/access_log_test.go` — delete the tests that moved, keep gateway-specific ones
- `pkg/gateway/controlplane.go` — call the shared middleware
- `pkg/gateway/http_server.go` — delete the private `clientIP`, use `telemetry.ClientIP`
- `pkg/safety/managed_network.go` — adopt the middleware (+ `ErrorLog`) on the proxy server
- `pkg/mcp/oauth_listener.go` — adopt the middleware (+ `ErrorLog`) on the callback server
- `pkg/architecture/testdata/graph.json` — **regenerated, intentional**

**Out of scope** (do NOT touch):

- `pkg/llm/openai/auth.go` — cannot import `pkg/telemetry` (cycle; see "Why this
  matters"). Leave it alone; the deferred item records what it would take.
- The record's message, field names, field order, level, or the
  path-not-query rule. It is a shipped format; this plan is a move, not a
  redesign.
- `pkg/telemetry`'s existing log-file primitives (`logfile.go`, `levels.go`,
  `slog_handler.go`) — plan 002 owns those.
- Do **not** add a log-level knob, a sampling rate, or a per-server toggle. The
  existing `LOG_LEVEL`/`FOREBRAIN_LOG_LEVEL` lever already silences INFO access
  lines (`slog.Info`), and that is the documented way to shed this volume.
- Do not change the gateway's rate limiter *semantics* (same IP rule, same
  limits); only its helper's location changes.

## Git workflow

- Branch: `refactor/access-log-middleware-seam`
- Two commits are acceptable and preferred here: (1) move + rewire the gateway,
  (2) adopt in the other servers. Conventional Commits + `-s`:
  `refactor(telemetry): own the access-log middleware`, then
  `feat(safety,mcp): log requests to the sandbox proxy and OAuth callback`.
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Move the middleware into `pkg/telemetry` (behaviour-preserving)

Create `pkg/telemetry/accesslog.go` containing, moved verbatim where possible:

- `AccessLogMiddleware(next http.Handler) http.Handler` — the body of today's
  `withAccessLog`, including the plan-005 deferred emission and the panicking-
  request ERROR record.
- `statusRecorder` (unexported) with `WriteHeader`, `Write`, `Hijack`, `Flush`,
  `Unwrap`, `status`.
- `formatAccessDuration` (unexported).
- `ClientIP(r *http.Request) string` — exported, moved from
  `pkg/gateway/http_server.go`'s `clientIP` unchanged.
- `SlogErrorLog() *log.Logger` — the plan-005 helper, exported for the other
  servers (`slog.NewLogLogger(slog.Default().Handler(), slog.LevelError)`).

The doc comment must state the contract, so a future adopter knows what it is
buying:

```go
// AccessLogMiddleware reports every request the handler it wraps answers, one
// record each, with nginx access-log semantics: method, target, status,
// duration, bytes and remote address. It is meant to sit outermost in a server's
// chain, so nothing is skipped — including the rate limiter's own rejections and
// a hijacked websocket upgrade (reported as 101) — and it reports a panicking
// request at Error level with the panic value before letting the panic continue.
//
// Only the request target is recorded, never a query string: a query is free
// text from the caller and routinely carries credentials. The target is the
// path, or the authority for the forms a forward proxy sees (CONNECT and
// absolute-URI requests), which have no path.
```

Implement that target rule (it is the one behaviour addition, and it exists so the
sandbox proxy logs something useful for `CONNECT host:443`):

```go
// requestTarget is what the record reports for a request. An origin server's
// requests always have a path; a forward proxy sees CONNECT and absolute-URI
// forms whose path is empty and whose authority is the interesting part.
func requestTarget(r *http.Request) string {
	if r.URL == nil {
		return ""
	}
	if r.URL.Path != "" {
		return r.URL.Path
	}
	return r.URL.Host
}
```

and use `"path", requestTarget(r)` in place of `r.URL.Path`. **This keeps every
existing gateway record byte-identical** (origin requests always have a path) and
is covered by a test in step 5.

Note on the key name: the field stays `path` even when it carries an authority
for a proxy request. Renaming it would break the shipped format; the doc comment
above (and a code comment at the call site) is where the exception is explained.

**Verify**: `go build ./pkg/telemetry/ && go vet ./pkg/telemetry/` → exit 0.

### Step 2: Move the middleware's tests, then rewire the gateway

1. Move the middleware-level tests from `pkg/gateway/access_log_test.go` to
   `pkg/telemetry/accesslog_test.go`, keeping them semantically identical:
   the one-record-per-request table, the query-string test, the hijack→101 test,
   and the panicking-request tests. Rename `withAccessLog` → `AccessLogMiddleware`
   in them, rename plan 005's `TestGatewayErrorLogCarriesAPanicStackToSlog` to
   `TestSlogErrorLogRoutesToSlog` (it now exercises the exported
   `telemetry.SlogErrorLog`), and move the helpers (`captureHandler`,
   `captureAccessLog`, `requestRecords`, `recordAttrs`, `serveOnce`) with them as
   unexported helpers of `pkg/telemetry` (mind collisions: check
   `grep -rn "captureHandler\|recordAttrs\|serveOnce" pkg/telemetry/` first).

   Note the console handler's own sink/fan-out tests are **not** here: those live
   in `cmd/forebrain/console_log_test.go` and are moved by plan 007, not by this
   plan.
2. Leave in `pkg/gateway/access_log_test.go` only the tests that exercise the
   **chain** (`ServeHTTPChain`), and have them assert against the shared
   middleware now that it is installed there.
3. In `pkg/gateway/controlplane.go`, replace `withAccessLog(…)` with
   `telemetry.AccessLogMiddleware(…)` (the file already imports `pkg/telemetry`?
   check — if not, add it; `pkg/gateway` already depends on `pkg/telemetry`).
4. In `pkg/gateway/http_server.go`, delete the private `clientIP` and call
   `telemetry.ClientIP(r)` at the rate limiter's call site. Keep the limiter's
   behaviour identical.
5. Delete `pkg/gateway/access_log.go` entirely and `gatewayErrorLog()` from it,
   switching `NewRestServer` to `telemetry.SlogErrorLog()`.

This repo deletes a replaced semantic completely: leaving `pkg/gateway/access_log.go`
as a thin wrapper would be dead code.

**Verify**:
```
grep -rn "withAccessLog\|func clientIP\|gatewayErrorLog" pkg/ cmd/   # expect no matches
go build ./... && go vet ./...
CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/... ./pkg/gateway/... -count=1
```
→ both packages `ok`, which is the real evidence the move was behaviour-preserving.

### Step 3: Adopt it on the sandbox egress proxy

In `pkg/safety/managed_network.go` (`startManagedNetworkProxy`):

```go
	proxy.httpServer = &http.Server{
		Handler:           telemetry.AccessLogMiddleware(proxy),
		ErrorLog:          telemetry.SlogErrorLog(),
		ReadHeaderTimeout: 30 * time.Second,
	}
```

Add the `pkg/telemetry` import. This is the change that turns "a tool contacted
this host" from invisible into an access-log line. Check for an import cycle
before compiling: `go list -deps ./pkg/telemetry | grep 'pkg/safety'` must print
nothing (it does not — but if it does, STOP: the seam belongs in a lower package,
not here).

Volume note to record in a comment or the commit body: an `npm install` through
the proxy produces one line per request. That is the intended audit trail; the
documented lever to shed it is `FOREBRAIN_LOG_LEVEL=warn`, which suppresses INFO
records.

**Verify**:
```
go build ./... && go vet ./pkg/safety/
CGO_ENABLED=1 go test -tags fts5 ./pkg/safety/... -count=1
```
→ exit 0 and `ok`.

### Step 4: Adopt it on the MCP OAuth callback listener

In `pkg/mcp/oauth_listener.go`, wrap the mux and give the server an `ErrorLog`:

```go
	srv := &http.Server{
		Handler:           telemetry.AccessLogMiddleware(mux),
		ErrorLog:          telemetry.SlogErrorLog(),
		ReadHeaderTimeout: 5 * time.Second,
	}
```

Remove the now-redundant `srv.Handler = mux` line. Verify no cycle first:
`go list -deps ./pkg/telemetry | grep 'pkg/mcp'` must print nothing.

**Verify**:
```
go build ./... && go vet ./pkg/mcp/
CGO_ENABLED=1 go test -tags fts5 ./pkg/mcp/... -count=1
```
→ exit 0 and `ok`.

### Step 5: Regenerate the package graph (intentional diff)

Two new import edges appeared (`safety → telemetry`, `mcp → telemetry`). The
repo's gate requires the committed graph to match the computed one:

```
scripts/package-graph.sh
git diff --stat pkg/architecture/testdata/graph.json
git diff pkg/architecture/testdata/graph.json
```

**Verify**: the diff contains **only** the expected edges and the `fan_in`/
`fan_out`/`loc` consequences of them — specifically, new `{"from": ".../pkg/safety", "to": ".../pkg/telemetry"}` and
`{"from": ".../pkg/mcp", "to": ".../pkg/telemetry"}` entries, and `telemetry`'s
`fan_in` increasing by 2. Any other edge change means something imported a
package it should not have — STOP and report.

Then:
```
CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/... -count=1
```
→ `ok`.

### Step 6: Full gates

```
go build ./... && go vet ./... && gofmt -l cmd pkg
CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry/... ./pkg/gateway/... ./pkg/safety/... ./pkg/mcp/... ./pkg/architecture/... -count=1
CGO_ENABLED=1 go test -tags fts5 ./... -count=1
git diff --stat -- pkg/agent pkg/assembly pkg/llm pkg/turn/prompt* internal/agentrun
```

**Verify**: build/vet exit 0; `gofmt` lists only `pkg/tui/render.go`; every
package `ok`; the prompt-cache diff is **empty**.

## Test plan

New `pkg/telemetry/accesslog_test.go`, holding the moved middleware tests plus:

1. `TestAuthLogMiddlewareReportsOneRecordPerRequest` — the moved table
   (200 / 404 / 429 / handler-writes-nothing), asserting exactly one INFO record
   with the six fields. Pattern: today's `TestWithAccessLogReportsOneRecordPerRequest`.
2. `TestAccessLogMiddlewareNeverRecordsTheQueryString` — moved.
3. `TestAccessLogMiddlewareReportsHijackedUpgradeAs101` — moved; needs a real TCP
   server (`httptest.NewServer`) because `httptest.ResponseRecorder` cannot hijack.
4. `TestAccessLogMiddlewareReportsPanickingRequest` + the "real status when the
   handler already wrote" case — moved from plan 005's tests.
5. **`TestRequestTargetUsesTheAuthorityWhenThereIsNoPath`** — new, and the only
   behaviour addition: a request with `URL.Path == ""` and
   `URL.Host == "example.com:443"` (a proxy's CONNECT) logs `path=example.com:443`;
   a request with a path logs the path. Table-driven is fine here (two cases).
6. **`TestClientIPPrecedence`** — new: `X-Forwarded-For: "203.0.113.7, 10.0.0.1"`
   → `203.0.113.7`; no XFF + `RemoteAddr: "198.51.100.9:41234"` → `198.51.100.9`;
   unparseable `RemoteAddr` → returned as-is; `nil` request → empty string. This
   pins the rule that moved out of `pkg/gateway`.
7. `TestSlogErrorLogRoutesToSlog` — `SlogErrorLog().Print("boom")` emits exactly
   one ERROR record whose message is `boom` (no trailing newline — the stdlib
   trims one).

`pkg/safety` and `pkg/mcp`: no new tests. The middleware's behaviour is covered in
its own package; asserting it again through the proxy would duplicate coverage
this repo does not duplicate. If `pkg/safety` has an existing test that starts the
proxy, no change is needed — its assertions do not concern logging.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `grep -rn "withAccessLog\|func clientIP(\|gatewayErrorLog" pkg/ cmd/` → no matches
- [ ] `test -f pkg/gateway/access_log.go` → **false** (file deleted)
- [ ] `grep -n "AccessLogMiddleware" pkg/telemetry/accesslog.go pkg/gateway/controlplane.go pkg/safety/managed_network.go pkg/mcp/oauth_listener.go` → 4 matches
- [ ] `grep -n "SlogErrorLog" pkg/gateway/http_server.go pkg/safety/managed_network.go pkg/mcp/oauth_listener.go` → 3 matches
- [ ] `grep -rn "pkg/telemetry" pkg/llm/` → no matches (the cycle stays unbroken)
- [ ] `go build ./...` exits 0
- [ ] `go vet ./...` exits 0
- [ ] `gofmt -l cmd pkg` prints only `pkg/tui/render.go`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → all packages `ok`
- [ ] `git diff pkg/architecture/testdata/graph.json` shows **only** the two new edges and their fan_in/loc consequences
- [ ] `git diff --stat -- pkg/agent pkg/assembly pkg/llm pkg/turn/prompt* internal/agentrun` → empty
- [ ] `docs/plan/LOGGING_HARDENING/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- The excerpts do not match the live files (this plan was written against
  uncommitted work).
- Plans 003/005 have **not** landed: the middleware you are moving would then be
  the old one, and the move would silently drop their fixes. Check for
  `messageText` in `cmd/forebrain/console_log.go` and `panic(recovered)` in
  `pkg/gateway/access_log.go` before starting; if either is missing, STOP.
- `go list -deps ./pkg/telemetry` contains `pkg/safety`, `pkg/mcp` or
  `pkg/gateway` — the seam would create a cycle. STOP and report; the middleware
  would have to move lower than `pkg/telemetry`.
- You are tempted to also cover `pkg/llm/openai/auth.go`. It is impossible
  without a new leaf package (see "Deferred"); adding that package is a separate
  decision with a package-graph consequence.
- The regenerated `graph.json` contains an edge you did not intend (that means an
  unintended import crept in).
- Adopting the middleware on the proxy changes any `pkg/safety` test's outcome.
  The proxy's routing/blocking behaviour must be untouched by wrapping its
  handler — a failure means the wrapper interfered (e.g. through `Hijack`).

## Maintenance notes

- **`AccessLogMiddleware` is now the single definition of the format.** Adding a
  field means changing it once for four servers (gateway, sandbox proxy, MCP
  callback, and any future one) — that is the point of the move. Anything that
  parses the line must be updated in the same change.
- **The `path` field can carry an authority.** For proxy-form requests
  (`CONNECT`, absolute URI) it holds the target host:port, not a path. A log
  reader that assumes a leading `/` will be surprised; the middleware's doc
  comment says so.
- **Volume comes with the proxy adoption.** One line per proxied request is the
  audit trail; `FOREBRAIN_LOG_LEVEL=warn` is the lever. If the owner later wants
  the proxy lines separately routed or sampled, that is a new decision, not a
  refinement of this plan.
- **Deferred: the provider OAuth callback** (`pkg/llm/openai/auth.go:270`).
  Covering it requires the middleware to live in a package that `pkg/llm` may
  import — i.e. a new leaf package (the middleware has no dependencies beyond
  `net/http` and `log/slog`). That adds a package to the graph and a `doc.go`,
  which is why it is not smuggled into this plan. Its listener is also
  short-lived and binds loopback, so the value is lower than the proxy's.
- **Reviewer scrutiny**: diff the moved middleware against
  `git show :pkg/gateway/access_log.go` (or the untracked original) line by line
  — a move that also "tidies" is where behaviour changes hide. Confirm the
  `ClientIP` move left the rate limiter's behaviour identical.
