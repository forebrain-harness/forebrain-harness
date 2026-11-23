# Plan 238: every leased channel keeps its outbound send path

> **Executor instructions**: Follow the steps in order; confirm each verification
> before the next. If a STOP condition occurs, stop and report — do not improvise.
> Do **not** commit, push or merge: leave the change in the working tree and report
> it. When done, update this plan's row in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 69ab0706..HEAD -- pkg/process/lease.go pkg/channel/channel.go pkg/process/lease_test.go`
> If any in-scope file changed, compare the "Current state" excerpts with the live
> code. Content mismatch is a STOP condition; line drift alone is not.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW — one forwarding method plus a capability table test; no routing,
  lease or start/stop behaviour changes.
- **Depends on**: none
- **Category**: bug (channel)
- **Planned at**: commit `69ab0706`, 2026-11-05
- **Finding**: branch audit 2026-11-05 (`origin/dev..HEAD`), finding 1
  (`pre-existing`, in a file this branch edited)

## Why this matters

A channel handler that keeps a long connection (telegram, discord, whatsapp, …) is
wrapped in `leasedHandler` so only the replica holding the account lease connects.
The registry, however, questions **the wrapper** about what the handler can do:
`Registry.Deliver` type-asserts `bc.Handler.(OutboundSender)`.

`leasedHandler` forwards `ID`, `Start`, `Stop` and `MountRoutes` — but not
`DeliverOutbound`. So on every deployment that configures one of those channel
kinds, the type assertion fails, `Deliver` returns `ErrNotOutbound`, and the
gateway treats that as a normal outcome and drops the reply **silently**. The
agent answers; nobody receives it; nothing is logged.

Plan 234 fixed exactly this class of bug for routes (`MountRoutes` added to the
wrapper). This plan closes the other half and adds the guard that makes a future
optional interface fail loudly instead of silently.

## Current state

`pkg/channel/channel.go:38-42` — the base interface every adapter implements:

```go
type Handler interface {
	ID() string
	Start(ctx context.Context, add RouteAdder, bus Bus) error
	Stop(ctx context.Context) error
}
```

`pkg/channel/channel.go:51-53` and `:263-266` — the two **optional** capabilities,
each asked for with a type assertion:

```go
type RouteMounter interface { MountRoutes(add RouteAdder, bus Bus) }

type OutboundSender interface {
	Handler
	DeliverOutbound(ctx context.Context, o Outbound) error
}
```

`pkg/channel/channel.go:313-318` — the outbound path, asserting on the bound handler:

```go
		sender, ok := bc.Handler.(OutboundSender)
		if !ok {
			return ErrNotOutbound
		}
```

`pkg/gateway/channels.go:79-85` — where the wrapper is put in front of the adapter:

```go
func (s *Server) wrapChannelLease(cfg channel.ProjectChannelConfig, h channel.Handler) channel.Handler {
	name := channel.LeaseName(cfg.Kind, cfg.Section)
	if name == "" {
		return h
	}
	return s.Env.Leases.Guard(name, h)
}
```

`pkg/process/lease.go:147-177` — the wrapper, with only two forwards:

```go
type leasedHandler struct {
	leases  *Leases
	name    string
	handler channel.Handler
	…
}

func (s *leasedHandler) ID() string { return s.handler.ID() }

// MountRoutes forwards the capability to the delegate. …
func (s *leasedHandler) MountRoutes(add channel.RouteAdder, bus channel.Bus) {
	if m, ok := s.handler.(channel.RouteMounter); ok {
		m.MountRoutes(add, bus)
	}
}
```

`pkg/channel/account.go:106-121` — the kinds that take a lease, i.e. the kinds this
bug silences: telegram, discord, mattermost, matrix, homeassistant, signal, weixin,
dingtalk, feishu, qq, whatsapp.

`pkg/gateway/server.go:612-618` — the silence, which is correct policy for a
genuinely inbound-only adapter and is what hides the bug here:

```go
	if err != nil && !errors.Is(err, channel.ErrNoSuchChannel) && !errors.Is(err, channel.ErrNotOutbound) {
		slog.Error("channel outbound", "channel", chID, "err", err)
	}
```

Convention to match: the `MountRoutes` forward directly above the new method —
same comment shape ("forwards the capability to the delegate …"), and a test in
`pkg/process/lease_test.go` modelled on `TestLeasedChannelMountsRoutesOnEveryReplica`
(`:365-…`), which wraps a fake adapter per replica.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/channel ./pkg/gateway -run 'Lease|Deliver' -count=1` | `ok` |
| Neighbours | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/channel ./pkg/gateway -count=1` | `ok` |

## Scope

**In scope**:
- `pkg/process/lease.go` (the wrapper)
- `pkg/process/lease_test.go` (new tests)
- optionally `pkg/gateway/channels.go` (only if step 3 needs a one-line comment)

**Out of scope**:
- `pkg/channel/channel.go` — `Deliver` and the interface stay as they are. Do not
  make `DeliverOutbound` part of `Handler`: inbound-only adapters are legitimate.
- `pkg/process/lease.go`'s acquire/renew/hold logic (that is plans 222/241-era
  work; a renewal change here would re-mix two concerns).
- Any channel adapter's own `DeliverOutbound` implementation.

## Steps

### Step 1: forward the send capability

In `pkg/process/lease.go`, immediately after `MountRoutes`, add:

```go
// DeliverOutbound forwards the send capability to the delegate. The registry
// asks the handler it holds — this wrapper — whether a channel can send, so
// without the forwarding every reply on a leased kind answers ErrNotOutbound
// and is dropped by the caller. The holder is not the only replica that
// answers: a reply is produced by whichever replica runs the conversation.
func (s *leasedHandler) DeliverOutbound(ctx context.Context, o channel.Outbound) error {
	sender, ok := s.handler.(channel.OutboundSender)
	if !ok {
		return channel.ErrNotOutbound
	}
	return sender.DeliverOutbound(ctx, o)
}
```

Returning `channel.ErrNotOutbound` for a delegate that cannot send keeps today's
answer byte-identical for an inbound-only adapter.

**Verify**: `go build ./...` → exit 0

### Step 2: send through the wrapper, end to end

In `pkg/process/lease_test.go` add `TestLeasedChannelSendsOutbound`:

1. Build one `Leases` from `testLeases(t, "webchat-a")` (already in the file).
2. A fake adapter implementing `Handler` + `OutboundSender` that records the
   `channel.Outbound` it is handed (model it on `fakeOutboundChannel` in
   `pkg/process/cron_service_test.go:242-267`).
3. `guarded := leases.Guard("channel:acct", fake)`.
4. `reg := channel.NewRegistry()`; `reg.Bind(ctx, "agent", []channel.Handler{guarded}, nil)`
   (`Bind` is `pkg/channel/channel.go:96`).
5. `err := reg.Deliver(ctx, channel.Outbound{ChannelID: "fake", Text: "hello"})`
   — the fake's `ID()` must be the id used here.
6. Assert `err == nil` and the fake recorded `hello`.
7. Second case: a delegate that does **not** implement `OutboundSender` (plain
   `Handler`) — `reg.Deliver` must return `channel.ErrNotOutbound`, exactly as an
   unwrapped inbound-only adapter does.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run TestLeasedChannelSendsOutbound -count=1` → `ok`

### Step 3: keep a future capability from being lost the same way

Same file: `TestLeasedChannelKeepsItsDelegateCapabilities` — for each optional
interface, assert the wrapper implements it **whenever** the delegate does (one
direction only: the wrapper answers both optional interfaces unconditionally, so
"iff" can never hold — `MountRoutes` on a delegate without routes is a no-op and
`DeliverOutbound` answers `ErrNotOutbound`, i.e. answering *more* than the
delegate is not capability loss, while answering *less* is the bug):

```go
	for name, check := range map[string]func(channel.Handler) bool{
		"RouteMounter":   func(h channel.Handler) bool { _, ok := h.(channel.RouteMounter); return ok },
		"OutboundSender": func(h channel.Handler) bool { _, ok := h.(channel.OutboundSender); return ok },
	} { … }
```

Wrap a delegate implementing both, and a delegate implementing neither; assert
`check(wrapper) == check(delegate)` for both. This table is the place a fourth
optional interface gets added later.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'TestLeasedChannel' -count=1` → `ok`

## Test plan

- New: `TestLeasedChannelSendsOutbound` (the regression: deliver through
  `Registry` with the wrapper bound — fails with `ErrNotOutbound` before step 1).
- New: `TestLeasedChannelKeepsItsDelegateCapabilities` (the structural guard).
- Keep green: `TestLeasedChannelMountsRoutesOnEveryReplica`, the `Guard`
  pass-through cases at `pkg/process/lease_test.go:440-465`, and every
  `pkg/channel` / `pkg/gateway` test.
- Negative control for step 1: temporarily comment the new method out and confirm
  `TestLeasedChannelSendsOutbound` fails with `ErrNotOutbound`; restore it.

## Done criteria

ALL must hold:

- [ ] `go build ./...` and `go vet ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/channel ./pkg/gateway -count=1` passes
- [ ] `grep -n 'func (s \*leasedHandler) DeliverOutbound' pkg/process/lease.go` returns one match
- [ ] The negative control was observed red before the fix
- [ ] Only in-scope files modified (`git status --porcelain`)
- [ ] `plans/README.md` row for 238 updated

## STOP conditions

- `Registry.Bind` refuses a handler whose `Start` fails in a way that makes the
  registry-level test impossible to set up: fall back to asserting
  `guarded.(channel.OutboundSender)` directly, and report that the registry-level
  case could not be written.
- Some leased kind's adapter turns out not to implement `OutboundSender` at all
  (`grep -n 'func (.*) DeliverOutbound' pkg/channel/*.go` for the ten kinds named
  above): report the list. It changes the impact statement, not the fix.
- A test fails because it pinned "a leased handler is not an `OutboundSender`":
  report the test and its rationale instead of editing it to fit.

## Maintenance notes

- The invariant: **a wrapper must not subtract capability from its delegate**.
  Any new optional interface on `channel.Handler` gets a forward in
  `leasedHandler` and a row in the step-3 table.
- A reviewer should check that the plan-234 `MountRoutes` behaviour is untouched
  and that an inbound-only delegate still answers `ErrNotOutbound`.
- Deliberately deferred: `pkg/gateway/server.go`'s silent swallow of
  `ErrNotOutbound`. With the forward in place it is again a genuine
  inbound-only outcome; making it noisy belongs with the channel-level logging
  work, not here.
