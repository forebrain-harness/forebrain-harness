# Plan 234: a leased channel still answers its webhook on every replica

> **Executor instructions**: Follow the steps in order; confirm each verification.
> If a STOP condition occurs, stop and report. When done, update this plan's row in
> `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 7d505aa3..HEAD -- pkg/channel/account.go pkg/channel/whatsapp.go pkg/channel/channel.go pkg/process/lease.go`
> Content mismatch against the excerpts is a STOP condition.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED — this touches the handler-wrapping boundary that keeps a
  long-connection adapter single-instance; getting it wrong reconnects twice.
- **Depends on**: plan 222 (the lease must actually be held by one replica, and a
  standby must take over, before this split is observable)
- **Category**: bug
- **Planned at**: commit `7d505aa3`, 2026-11-04
- **Finding**: branch audit 2026-11-04, finding 14 (`introduced` by plan 213)

## Why this matters

`NeedsLease` classifies a channel kind by whether its adapter keeps a long
connection. WhatsApp is listed as "poll loop, beside the webhook route it also
mounts" — and its `Start` does both in one call: it registers the inbound HTTP route
and then starts a poll loop.

`process.Leases.Guard` wraps that whole handler, so on a non-holder replica `Start`
is never called at all. In a multi-replica deployment behind a load balancer, any
WhatsApp callback delivered to a non-holder replica therefore hits **no route** and
is lost — while the holder's poll loop is the only part that needed the lease.

The classification is right; the wrapping granularity is wrong. Mounting routes is
replica-local and idempotent (the same reasoning that makes a webhook kind need no
lease at all, per `NeedsLease`'s own comment).

## Current state

`pkg/channel/account.go:102-140` — the rationale and the classification, with
WhatsApp explicitly noted as mounting a route as well as polling.

`pkg/channel/whatsapp.go:48-70` — both duties in one `Start`:

```go
func (w *WhatsApp) Start(ctx context.Context, add RouteAdder, bus Bus) error {
	w.inboundAdd = add
	w.bus = bus
	if !w.cfg.Enabled {
		return nil
	}
	...
	path := strings.TrimSpace(w.cfg.InboundPath)
	if path != "" {
		add(http.MethodPost, path, w.handleInbound)
	}
	runCtx, cancel := context.WithCancel(ctx)
	...
	go w.pollLoop(runCtx)
	return nil
}
```

`pkg/process/lease.go:124-160` (`leasedHandler`) — the wrapper that makes `Start`
conditional; read it before changing anything. Today: it calls
`s.handler.Start(runCtx, add, bus)` only when the lease was won.

`pkg/gateway/channels.go:84` — where the wrapper is applied: `return s.Env.Leases.Guard(name, h)`.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/channel ./pkg/process -count=1` | `ok` |
| Gateway | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` | `ok` |

## Scope

**In scope**:
- `pkg/channel/account.go` (a new classification predicate only)
- `pkg/channel/whatsapp.go`
- `pkg/channel/channel.go` (the `Handler` interface, only if a second method is
  needed)
- `pkg/process/lease.go` (the wrapper's split)
- the matching `_test.go` files

**Out of scope**:
- Every other adapter's `Start` — the lease split must be opt-in per kind. If
  another kind turns out to mount routes too, report it; do not convert it here.
- `NeedsLease`'s list of kinds (it is right).
- The gateway's route table and `RouteAdder` implementation.
- Plan 222's CAS and takeover work.

## Steps

### Step 1 — record today's behaviour

Add `TestLeasedChannelMountsRoutesOnEveryReplica` to `pkg/process`: a fake
`channel.Handler` that implements `RouteMounter` (records `add(...)` and `bus`),
and a `Start` that only records that it ran. Wrap it with `Guard`, start two
replicas over one SQLite database **through the registry's bind path**, and assert
the **non-holder** mounted the route (and got a non-nil bus) while its `Start`
never ran. Record today's behaviour: the non-holder's fake records nothing,
because mounting lives inside `Start`, which the lease suppresses.

**Verify**: fails today. Record it.

### Step 2 — split mounting from starting

Introduce one optional interface in `pkg/channel`:

```go
// RouteMounter is implemented by an adapter that mounts inbound HTTP routes and
// keeps a connection of its own. Mounting is replica-local — every replica must
// serve the route — while the connection is singleton and belongs to the lease
// holder. An adapter that only mounts routes needs no lease at all (see
// NeedsLease); this interface exists for the kinds that do both.
type RouteMounter interface {
	MountRoutes(add RouteAdder, bus Bus)
}
```

The `bus` argument is load-bearing, not cosmetic: `handleInbound` publishes on
`w.bus` (`pkg/channel/whatsapp.go:176`), and `w.bus` is assigned only inside
`Start` today (`:50`). A non-holder that mounted a route without a bus would
panic on the first callback — worse than today's 404.

Implement it on `*WhatsApp` by moving the `path != ""` registration **and** the
`w.inboundAdd = add; w.bus = bus` assignments out of `Start` into `MountRoutes`
(mount only when `w.cfg.Enabled`, exactly as today). `Start` keeps the outbound
URL normalisation and the poll loop, and no longer mounts anything.

Implement it on `leasedHandler` (`pkg/process/lease.go`) by delegating to the
wrapped handler when that handler implements the interface. The registry sees the
**wrapper**, so the wrapper must expose the capability or the mount is lost.

Mount from one place: `Registry.BindProjects` (`pkg/channel/channel.go`), before
`bc.Handler.Start(...)`:

```go
if m, ok := bc.Handler.(RouteMounter); ok {
	m.MountRoutes(add, hb)
}
```

This runs on every replica, leased or not, and keeps `Start` meaning "connect /
poll" — still lease-gated. Do **not** also mount inside `Start` or inside
`leasedHandler.Start`: two mount points give the holder a duplicate route and
make the non-holder's bus assignment race itself.

**Verify**: step 1's test passes; `TestGuardedChannelConnectsOnOneReplicaOnly`
still passes (the loop is still single-instance); a non-leased handler still gets
its route (the `BindProjects` path is the one every kind uses).

### Step 3 — prove the webhook answers on both replicas

Extend step 1's test: after both replicas started, POST to the mounted path on the
non-holder's own mux/route table and assert the handler is reachable (no 404), while
the poll loop count stays `1` across both replicas.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run TestLeasedChannel -count=1`
→ `ok`. (The bare `go test ./pkg/process` form fails without fts5: the test opens
the state database through `state.OpenStateForTest`.)

### Step 4 — make the classification honest

Add to `NeedsLease`'s comment table a line saying which kinds implement
`RouteMounter`, so the next adapter author sees the split. Then grep every
lease-taking adapter for `add(http.Method` inside `Start`: each hit is either an
adapter that should implement `RouteMounter` or one whose route is genuinely
holder-only (say which, and why, per hit).

**Verify**: `grep -rn "add(http.Method" pkg/channel/*.go` → every hit is inside a
`MountRoutes` method, or listed in your report with its decision.

## Test plan

- New: `TestLeasedChannelMountsRoutesOnEveryReplica` (steps 1–3).
- Keep green: `TestGuardedChannelConnectsOnOneReplicaOnly`,
  `TestGuardLeavesWebhookKindsAndHalfConfiguredChannelsAlone`,
  `TestNeedsLeaseClassification`, and the WhatsApp adapter's own tests.
- Failure paths: non-holder mounts but does not poll; holder mounts once (not
  twice); a kind without `RouteMounter` is unchanged; a non-leased route still
  mounts through `BindProjects`.

## Done criteria

ALL must hold:

- [ ] `go vet ./...` exits 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/channel ./pkg/process ./pkg/gateway -count=1` passes
- [ ] The new test shows the non-holder's route mounted with a non-nil bus, and the poll loop started once
- [ ] `grep -n "add(http.Method" pkg/channel/whatsapp.go` is inside `MountRoutes` only
- [ ] `grep -n "MountRoutes(add, hb)" pkg/channel/channel.go` shows the single mount point in `BindProjects`
- [ ] Every lease-taking adapter that mounts a route is listed in the report with its decision
- [ ] Only in-scope files modified
- [ ] `plans/README.md` row for 234 updated

## STOP conditions

- `handleInbound` needs a field that mounting cannot set (the poll loop's
  `cancel`/`done` are created in `Start`): report the dependency instead of
  duplicating initialisation. The bus is not such a field — the interface passes
  it.
- Another lease-taking kind mounts a route whose delivery is **stateful** (it must
  only be served by the holder, e.g. it acknowledges a poll): report it — that kind
  needs "routes on the holder only" and this plan's default would be wrong.
- `pkg/architecture` forbids the new interface's placement (it lives in
  `pkg/channel`, which every surface may import): say so and propose the alternative
  (a method on the existing `Handler` interface) before changing the interface for
  every adapter.

## Maintenance notes

- The rule: **mounting is replica-local, connecting is singleton**. A new adapter
  that does both implements `RouteMounter`; one that only mounts takes no lease.
- A reviewer should check the holder does not mount twice, and that the non-holder's
  route uses the same handler function (same signature verification), so a webhook
  delivered anywhere is validated identically.
- Deliberately deferred: an adapter that needs holder-only routes needs a second,
  explicit interface (`HolderOnlyRoutes`) — none exists today.
