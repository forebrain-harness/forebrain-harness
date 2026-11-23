# Plan 226: a replica's internal address is reachable by the peers that must use it

> **Executor instructions**: Follow the steps in order, confirming each
> verification. If a STOP condition occurs, stop and report. When done, update
> this plan's row in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 7d505aa3..HEAD -- pkg/gateway/controlplane.go pkg/gateway/run_control.go deploy/compose/multi-replica.yaml deploy/docker/compose.yaml`
> Content mismatch against the excerpts is a STOP condition.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED — this is the authentication boundary between replicas; a
  mistake either locks replicas out or lets a browser in.
- **Depends on**: none. Land **before** plan 214's deployment docs are relied on
  for multi-replica installs.
- **Category**: security / bug
- **Planned at**: commit `7d505aa3`, 2026-11-04
- **Finding**: branch audit 2026-11-04, finding 5 (`introduced` by plan 213)

## Why this matters

Multi-replica gateways forward work between each other over an internal path on
the same listener the browser uses (`internal/run-op`, the WS relay, the control
plane). The caller authenticates with the shared gateway token **and** must come
from a host that a live replica registered — the second half exists precisely
because the token is not a secret between replicas (`controlplane.go:299-303`).

`internalSourceRegistered` compares `net.ParseIP` of the request's TCP peer
against `net.ParseIP` of the registered `internal_addr`. The tested multi-replica
compose registers `http://gateway-a:6060` / `http://gateway-b:6060` — hostnames.
`net.ParseIP("gateway-a")` is nil, the loop skips every row, and the check never
matches: **every internal request is 401**.

A 401 from the owner replica is not swallowed: `forwardRunOp` copies the peer's
status and body to the browser (`pkg/gateway/run_control.go:695-712`, the
`w.WriteHeader(status)` at `:710`). So on the deployment the plan itself ships as
"the tested two-replica form", a browser that starts a turn on replica A and sends
a follow-up that lands on replica B is told `unauthorized` — the web session
appears to log out, and cancelling a run reports failure while the run keeps
going.

## Current state

`pkg/gateway/controlplane.go:296-322` — the source check:

```go
func (s *Server) internalSourceRegistered(ctx context.Context, remoteAddr string) bool {
	if s == nil || s.RunRT == nil {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	addrs, err := s.RunRT.LiveReplicaAddrs(ctx)
	if err != nil {
		slog.Warn("gateway internal auth: read registered replicas", "err", err)
		return false
	}
	for _, addr := range addrs {
		u, parseErr := url.Parse(strings.TrimSpace(addr))
		if parseErr != nil || u.Host == "" {
			continue
		}
		if peer := net.ParseIP(strings.Trim(u.Hostname(), "[]")); peer != nil && peer.Equal(ip) {
			return true
		}
	}
	return false
}
```

`deploy/compose/multi-replica.yaml:100,114` — the addresses the tested form
registers (note the file's own comment at `:11`, "FOREBRAIN_INTERNAL_ADDR names the
pod/service address a peer reaches this"):

```yaml
      FOREBRAIN_INTERNAL_ADDR: http://gateway-a:6060
...
      FOREBRAIN_INTERNAL_ADDR: http://gateway-b:6060
```

`pkg/gateway/run_control.go:690-716` — the peer's answer is replayed to the
browser verbatim, including the status:

```go
	for k, vs := range answer.header {
		if strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Transfer-Encoding") {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(status)
	_, _ = w.Write(answer.body.Bytes())
	return true
```

`deploy/k8s/manifests/deployment.yaml` — the k8s form uses the pod IP
(`POD_IP`), which does parse as an IP; the compose form is the broken one. Check
the live file for the exact env name before relying on this.

### Verified today

- `net.ParseIP("gateway-a") == nil`, so the compose form's `LiveReplicaAddrs`
  rows can never match.
- The k8s form's `internal_addr` is an IP, so the same code works there: the bug
  is a function of *how the address was written*, not of the auth design.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'TestInternal|TestControlPlane|TestForward' -count=1` | `ok` |
| Package | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway ./pkg/process -count=1` | `ok` |
| Two-replica (if Docker is available) | `./scripts/multi-replica-test.sh` | `PASS: multi-replica forwarding on <engine>` |

## Scope

**In scope**:
- `pkg/gateway/controlplane.go`
- `pkg/gateway/run_control.go` (only the forwarding error path)
- `deploy/compose/multi-replica.yaml` (comments only)
- `deploy/docker/compose.yaml` (comments only — step 4 updates the two comment
  blocks that describe the internal address form; no environment value changes)
- The matching tests under `pkg/gateway` (`*_test.go` only where an assertion
  must change)

**Out of scope**:
- `deploy/k8s/manifests/*` — the pod-IP form already works; do not touch.
- The internal token's generation and comparison (constant-time compare is
  correct; `controlplane.go:49-59`).
- `pkg/state`'s replica registration columns.
- The WS relay protocol (`ws_protocol.go`).

## Steps

### Step 1 — reproduce with a test, no Docker needed

Add `TestInternalSourceAcceptsARegisteredHostname` to `pkg/gateway`'s control
plane tests. Use the real harness, not a stub — `Server.RunRT` is a concrete
`*state.RunStore` (`pkg/gateway/server.go:53`), so a stub cannot compile. Build
the server with `newReplicaTestServer(t, db, home, "gateway-self", token,
"http://localhost:6060")` (`pkg/gateway/controlplane_test.go:542-559`; it
registers the replica with that `internal_addr`), then call
`internalSourceRegistered(ctx, "127.0.0.1:6060")` and assert true.
`localhost` resolves on every development machine and in CI, so the test needs no
DNS fixture and no injection point.

**Verify**: the test **fails** today (the registered host is not an IP literal).
Record the failure in your report.

### Step 2 — resolve a registered hostname to addresses

In `internalSourceRegistered`, replace the `net.ParseIP(u.Hostname())` comparison
with a resolver that accepts both forms:

1. if the registered host is an IP literal, compare as today;
2. otherwise resolve it — `net.DefaultResolver.LookupHost(ctx, host)` — and
   compare every returned IP;
3. on a resolution error, treat that row as a non-match and continue (never fail
   the whole check on one bad row).

Bound the work: the row set is small (live replicas only) and the lookup is
against the local resolver, but pass the request's `ctx` so a hung resolver
cannot outlive the request. Do **not** accept a bare DNS name from the request —
the direction is DB row → IP, never caller → IP, which keeps the check's promise:
the caller cannot talk its way in.

**Verify**: the step-1 test passes; the pre-existing negative case (an address no
replica registered) still returns false.

### Step 3 — never hand a peer's 401 to the browser

In `run_control.go`, where a peer's answer is replayed: if the peer answered
`401` or `403`, do not copy the status or the body. Treat it exactly like an
unreachable peer — `slog.Warn` with the peer's owner and address, and answer the
browser with the local result only. For the broadcast wrapper
(`forwardRunOpToEveryReplica`) this is already the behaviour for transport
errors; extend the same branch. For the single-owner forwarding path, a peer that
refuses must surface as `502` with a neutral message (no peer address, no token
detail), because the operation genuinely did not happen.

Add the neutral message as a constant next to the other forwarding text. It is
user-visible copy, so it is written in Chinese like the rest of the interface
(界面文案铁律); do not put a peer address, a token or an HTTP status detail in it.

**Verify**: a new test `TestForwardedPeerUnauthorizedIsNotForwarded` — a fake peer
answering 401 must produce `502` locally and must not contain the peer's body.
Run `go test ./pkg/gateway -run 'TestInternal|TestControlPlane|TestForward' -count=1`
→ green. (Go's `-run` takes a RE2 pattern; a backslash-escaped `\|` is a literal
pipe and would match no test name, printing `ok` after running nothing.)

### Step 4 — make the deployment self-consistent

Pick **one** and record which in your report:

- **Option A (preferred, smallest)**: keep the hostname in compose (it is
  portable and the k8s form stays as it is). No environment value changes —
  only the two comment blocks below, because step 2 makes hostnames work.
- **Option B**: change `deploy/compose/multi-replica.yaml` to literal IPs with a
  comment that peer auth compares source IPs. This only works with a fixed
  network, which compose does not guarantee; do not choose it.

If you take option A, update the comment at the top of
`deploy/compose/multi-replica.yaml` (and the parallel comment in
`deploy/docker/compose.yaml:25-35,165-178`) to say a **hostname or IP** is
accepted and that the source IP is what is compared after resolution. Do not
change any environment value.

**Verify**: `grep -n "INTERNAL_ADDR" deploy/compose/multi-replica.yaml deploy/docker/compose.yaml`
→ unchanged values; only comments differ.

### Step 5 — the real two-replica run

**Verify**: `./scripts/multi-replica-test.sh postgres` → `PASS: multi-replica
forwarding on postgres`. (The script requires the engine argument —
`scripts/multi-replica-test.sh:24-29` — and expects `./scripts/test-db.sh up` to
have started `fb-postgres`.) If Docker is unavailable in your environment, say so
explicitly and mark this step unverified — do not claim it. In that case also
state which of the two compose files a human must run to close the loop.

## Test plan

- New: `TestInternalSourceAcceptsARegisteredHostname` (step 1),
  `TestForwardedPeerUnauthorizedIsNotForwarded` (step 3).
- Keep green: every existing `TestInternal*` / control-plane test, and the
  negative case "an unregistered source is refused".
- Structural pattern: `newReplicaTestServer` (`pkg/gateway/controlplane_test.go:542`)
  — a real `*state.RunStore` with one registered replica.
- Failure paths: unregistered source refused; unresolvable registered row skipped
  without failing the check; peer 401 not replayed.

## Done criteria

ALL must hold:

- [ ] `go vet ./...` exits 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway ./pkg/process -count=1` passes
- [ ] `TestInternalSourceAcceptsARegisteredHostname` passes
- [ ] `TestForwardedPeerUnauthorizedIsNotForwarded` passes and the response is 502 with no peer body
- [ ] `grep -n "net.ParseIP(strings.Trim(u.Hostname" pkg/gateway/controlplane.go` returns nothing
- [ ] `./scripts/multi-replica-test.sh postgres` passes, or the report names it unverified with the reason
- [ ] Only in-scope files modified
- [ ] `plans/README.md` row for 226 updated

## STOP conditions

- `LiveReplicaAddrs` returns more than the replica set (e.g. includes the local
  address of the requesting browser): report before adding resolution.
- The resolver returns an address equal to the **browser's** proxy address in a
  deployment where a reverse proxy fronts the gateway: then a browser request
  would pass as internal. In that case stop and report — the fix has to be
  "internal endpoints live on a second listener", which is a design change.
- Any existing test asserts that a peer's 401 reaches the caller: report it and
  why before changing the assertion.
- Docker is unavailable and step 5 is the only evidence you have for the compose
  form: still do steps 1–4, but say plainly in the report that the two-replica
  run is unverified.

## Maintenance notes

- The check's invariant is "source IP ∈ IPs a live replica registered". Adding a
  resolver must not weaken it to "any address that resolves to something".
- A reviewer should check: the lookup direction (row → IP), the ctx passed to the
  resolver, that one bad row cannot fail the whole check, and that no peer status
  or body reaches a browser on the auth-failure path.
- Deliberately deferred: moving internal endpoints to a second listener so a
  reverse proxy in front of the gateway cannot blur "internal" and "browser".
  That is the structurally correct answer and a separate design.
