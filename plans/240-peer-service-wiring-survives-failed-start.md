# Plan 240: a peer service that retried its way in is still wired to the process

> **Executor instructions**: Follow the steps in order and confirm each
> verification. If a STOP condition occurs, stop and report — do not improvise.
> Do **not** commit, push or merge: leave the change in the working tree and
> report it. When done, update this plan's row in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 69ab0706..HEAD -- pkg/process/peers.go pkg/process/peer_inbound.go pkg/process/peers_test.go`
> Compare the "Current state" excerpts with the live code; content mismatch is a
> STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW — moves one assignment; the retry loop and the socket lifecycle
  are untouched. `stop` is already safe when no registration was ever entered
  (`removeOwnFiles` returns early on an empty dir).
- **Depends on**: plan 237 (the retry loop this plan relies on)
- **Category**: bug (peer)
- **Planned at**: commit `69ab0706`, 2026-11-05
- **Finding**: branch audit 2026-11-05 (`origin/dev..HEAD`), finding 3
  (`introduced` by plan 237)

## Why this matters

Plan 237 made the peer service survive a first registration attempt that fails:
`start` launches the tick loop, and `enterPendingModeLocked` retries every tick
until the socket directory becomes usable.

But `StartPeers` still wires the service into the environment only when that
**first** attempt succeeds:

```go
	if err := svc.start(ctx); err != nil {
		slog.Warn("peer service did not start; this process is invisible to other local peers", "err", err)
		return stop
	}
	env.setPeerService(svc)
```

So the process that exactly the retry loop was built for ends up half-wired:
the tick loop later registers it, writes its record and opens its socket — the
process **is** reachable, receives frames, and holds inbound peer messages — but
`env.peers` is nil forever. Consequences, all permanent for the process's life:

- `HeldPeerMessages` returns nil and `ReleasePeerMessage` answers "no peer service
  is running" (`pkg/process/peer_inbound.go:1050-1088`; the not-found answer is
  `peer: no held message %q` at `:1082`): a held message can never be released or denied —
  the sender waits out its expiry.
- `peer_list` / `peer_send` (`peer_outbound.go:126-130`, `service()` returns nil)
  are dead for this process.
- The agent-touch ledger is not written, so other agents' `peer_list` recent-files
  and the web "who is editing this directory" view silently lose this agent.

## Current state

`pkg/process/peers.go:300-320` — the one-shot wiring:

```go
	stop = func() { svc.stop() }
	if env == nil || !peer.Enabled() {
		return stop
	}
	if err := svc.start(ctx); err != nil {
		slog.Warn("peer service did not start; this process is invisible to other local peers", "err", err)
		return stop
	}
	env.setPeerService(svc)
	return stop
```

`pkg/process/peers.go:350-378` — `start` launches the loop **before** the attempt,
so a failed start leaves a live loop behind:

```go
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.running = true
	go svc.tickLoop()
	if err := svc.enterModeLocked(target, false); err != nil {
		return err
	}
	svc.modeEntered = true
	return nil
```

`pkg/process/peers.go:428-445` — the retry that later succeeds:

```go
func (svc *peerService) enterPendingModeLocked(target bool) {
	if err := svc.enterModeLocked(target, false); err != nil {
		…
		return
	}
	svc.modeEntered = true
}
```

`pkg/process/peers.go:686-704` — `stop` is safe before any registration: it only
acts when `running`, and `removeOwnFiles("")` returns immediately.

`pkg/process/peer_inbound.go:1153-1164` — the accessor that answers "no service"
purely from `env.peers` (and from a nil `env`):

```go
func (env *Environment) peerInboxState() *peerInbox {
	…
	svc := env.peers
	…
	if svc == nil {
		return nil
	}
	return svc.inbox
```

The existing regression test is `pkg/process/peers_test.go:577`
`TestPeerServiceRetriesAFailedStart` — it already builds exactly this situation
(a regular file where the socket directory belongs, removed later) and asserts the
retried registration; extend it.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'Peer' -count=1` | `ok` |
| Neighbours | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/run ./pkg/tui -count=1` | `ok` |

Peer tests need the four directory overrides; the file's helper
`peerTestDirsFor` (`pkg/process/peers_test.go:35-60`) sets them — use
`peerTestEnvFor(t, "tui", …)`, never a bare `&Environment{}`.

## Scope

**In scope**:
- `pkg/process/peers.go` (the wiring order and its comment)
- `pkg/process/peers_test.go` (extend the existing test, plus one new assertion)

**Out of scope**:
- `pkg/process/peer_inbound.go` — the accessor's semantics stay; the fix is that
  `env.peers` is set.
- `svc.start`'s order (`go svc.tickLoop()` before the attempt). It is deliberate:
  the loop is what retries. Do not "fix" it by starting the loop after a
  successful attempt.
- The teardown/receipt bookkeeping.

## Steps

### Step 1: wire the service before the first attempt

Replace the tail of `StartPeers` (`peers.go:311-318`) with:

```go
	// The service is wired before the first registration attempt, not after it:
	// start() launches the loop that retries a failed attempt, so a process that
	// registers on a later tick must already be reachable through the port —
	// held messages released, peer_list/peer_send live, the write ledger
	// written. Wiring only a successful first attempt left exactly that process
	// listening and holding messages nothing could release (plan 237's retry,
	// plan 240's wiring).
	env.setPeerService(svc)
	if err := svc.start(ctx); err != nil {
		slog.Warn("peer registration did not start; retrying every tick", "err", err)
	}
	return stop
```

`stop` already calls `svc.stop()` and `svc.stop()` is a no-op until `running` is
set, so a `start` that fails before the loop is launched still stops cleanly.

**Verify**: `go build ./...` → exit 0

### Step 2: pin the wiring in the failing-start test

In `pkg/process/peers_test.go`, inside `TestPeerServiceRetriesAFailedStart`, after
`StartPeers` returns and while the socket directory is still blocked, add:

```go
	// The port is wired even though the first attempt failed: the loop will
	// register this process on a later tick, and it must be reachable then.
	if env.peerInboxState() == nil {
		t.Fatal("the peer inbox is unwired after a failed first attempt")
	}
	if err := env.ReleasePeerMessage("no-such-message"); err == nil ||
		strings.Contains(err.Error(), "no peer service is running") {
		t.Fatalf("release reached no inbox: %v", err)
	}
```

The second assertion is the point: the error must come from the inbox
("no held message …"), not from the "no service" guard. Keep the existing
assertions (no record while blocked, record after removal) unchanged.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run TestPeerServiceRetriesAFailedStart -count=1` → `ok`

### Step 3: after the retry, the port and the registration agree

Still in the same test, after `waitFor(t, "the retried registration", …)`:

```go
	// Registered and reachable are the same process here: the record says so and
	// the port answers from the same service.
	if env.peerInboxState() == nil {
		t.Fatal("the peer inbox is unwired after the retried registration")
	}
```

**Verify**: same command → `ok`

## Test plan

- Extended: `TestPeerServiceRetriesAFailedStart` — the regression (before step 1
  it fails at "the peer inbox is unwired"). Negative control: revert step 1's
  assignment order temporarily and confirm it fails.
- Keep green: every other `pkg/process` peer test
  (`TestPeerInbound*`, `TestClaudeCode*`, the mode-switch cases) — they all call
  `StartPeers` and must keep working with the service wired earlier.

## Done criteria

ALL must hold:

- [ ] `go build ./...` and `go vet ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/run ./pkg/tui -count=1` passes
- [ ] `grep -n 'env.setPeerService(svc)' pkg/process/peers.go` shows the call
      **before** the `svc.start` error branch
- [ ] `TestPeerServiceRetriesAFailedStart` fails when the assignment is moved back
      after the branch (negative control observed)
- [ ] `stop()` after a failed first attempt leaves no socket and no record
      (covered by the test's existing cleanup plus `t.Cleanup(stop)`)
- [ ] Only in-scope files modified (`git status --porcelain`)
- [ ] `plans/README.md` row for 240 updated

## STOP conditions

- `svc.stop()` turns out to touch an uninitialised field when `start` failed
  before `running = true` (i.e. `svc.ctx` nil): report it instead of adding a nil
  guard — the guard would hide a second wiring bug.
- Wiring the service earlier changes an existing test's answer (e.g. a test
  asserting "no service ⇒ peer_list errors"): report the test and its rationale.
  The intended new answer is "the service exists and is not registered yet".
- `peerOutbound.guardIdentity` starts writing ledger lines with an empty address
  (a marker row with no socket): report it. The expected behaviour is still "no
  ledger line while unregistered".

## Maintenance notes

- The invariant to keep: **`env.peers` is set whenever the tick loop is running**,
  not whenever the first attempt succeeded. Anything that answers "is peer
  messaging available" reads that pointer.
- A reviewer should check `stop`'s behaviour for the never-registered case, and
  that the warning text no longer claims the process is invisible (it is invisible
  *until* the retry succeeds).
