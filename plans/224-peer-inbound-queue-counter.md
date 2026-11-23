# Plan 224: a peer message is refused only while the queue is really full

> **Executor instructions**: Follow this plan step by step and confirm each
> verification result before moving on. If a STOP condition occurs, stop and
> report. When done, update this plan's row in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 7d505aa3..HEAD -- pkg/process/peer_inbound.go pkg/process/peer_inbound_test.go`
> Content mismatch against the excerpts is a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `7d505aa3`, 2026-11-04
- **Finding**: branch audit 2026-11-04, finding 3 (`introduced` by plan 196)

## Why this matters

Cross-process peer messaging admits at most `MaxQueuedPeerMessages` (50)
outstanding messages per process — a queue bound that keeps one busy agent from
drowning another. The counter behind it (`peerInbox.handed`) only ever grows: it
is incremented for every message handed to a conversation **and** for every
delivery note, and it is cleared only for sessions that hold a
`notify_when_idle` subscription. forebrain never sends `notify_when_idle`, so in
practice nothing clears it.

After 50 cumulative peer messages in the life of a process, every further message
from any agent on the machine is answered `dropped / queue-full` until the process
restarts or switches peer mode. The sender is woken just to be told its message
was dropped, and the two agents silently stop talking.

## Current state

`pkg/process/peer_inbound.go:186-205` — the field and its comment:

```go
	// handed counts the messages this process gave to a conversation since that
	...
	handed map[string]int
	...
		handed:        map[string]int{},
```

`pkg/process/peer_inbound.go:324-336` — the counter is summed into the admission
queue length:

```go
// queued is how many peer messages this process has taken and not yet handed to
// a model: everything held, plus everything given to a conversation whose run
// has not ended since.
func (in *peerInbox) queued() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	total := len(in.held)
	for _, count := range in.handed {
		total += count
	}
	return total
}
```

`pkg/process/peer_inbound.go:248-262` — that number is what `Admit` tests against
`MaxQueuedPeerMessages` (`pkg/peer/guard.go:149-151`):

```go
	queued := in.queued()
	senderKey := in.senderKey(a.Frame.From, a.PeerPID)
	if ok, reason := in.admission.Admit(senderKey, envelope, parsed.HopChain, in.ownHopTokens(), queued, time.Now()); !ok {
```

`pkg/process/peer_inbound.go:747-755` — every increment site (called from both
the delivery path and `deliverNote` at `:690-696`):

```go
// noteHanded records that one more peer message is now the conversation's turn
// to run.
func (in *peerInbox) noteHanded(sessionID string) {
	in.mu.Lock()
	in.handed[sessionID]++
	in.mu.Unlock()
}
```

`pkg/process/peer_inbound.go:915-968` — the tick. The only reset is inside the
`idleNow` loop, and `idleNow` is built **only** from
`in.subscriptions` (i.e. `notify_when_idle` subscriptions):

```go
	idleNow := map[string][]*peerIdleSubscription{}
	for id, sub := range in.subscriptions {
		if in.runningHere(sub.sessionID) {
			delete(in.idleSince, sub.sessionID)
			continue
		}
		delete(in.subscriptions, id)
		idleNow[sub.sessionID] = append(idleNow[sub.sessionID], sub)
	}
	...
	for sessionID, subs := range idleNow {
		...
		// The conversation is between turns: whatever it was handed has been
		// run, so it no longer owes the protocol a queue.
		in.mu.Lock()
		delete(in.handed, sessionID)
		in.mu.Unlock()
	}
```

The other reset is `teardown` (`:986`, `in.handed = map[string]int{}`).

Verified: `grep -rn "notify_when_idle\|NotifyWhenIdle" pkg --include=*.go` shows the
flag is only *read* (`:796`); forebrain never sets it when sending, so in a
forebrain-only fleet `idleNow` is always empty.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run TestPeerInbox -count=1` | `ok` |
| Package | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/peer -count=1` | `ok` |

## Scope

**In scope**:
- `pkg/process/peer_inbound.go`
- `pkg/process/peer_inbound_test.go`

**Out of scope**:
- `pkg/peer/guard.go` — the admission table and its limits are correct; only the
  number handed to it is wrong.
- `pkg/peer/envelope.go` and the frame/admission encoding.
- Making forebrain send `notify_when_idle` — that is a protocol feature with its
  own acceptance (plan 201 scenario 10). This plan must not depend on it.
- The TUI wake path (`pkg/tui/notify.go`) — a separate finding.

## Steps

### Step 1 — reproduce: 51 messages across idle gaps are refused

Add `TestPeerInboxQueueCounterDrainsWhenTheRunEnds` to
`pkg/process/peer_inbound_test.go`, modelled on the existing inbox tests in that
file (`startPeerTestService` + `peerTestEnvFor` + a fake `runningHere`; there is
no `newTestPeerInbox`). Three mechanics decide whether the test can observe
anything:

- **the tick**: `startPeerTestService` pins `peerTickInterval = time.Hour`
  (`pkg/process/peer_inbound_test.go:190`), so pass the existing `fastPeerTick`
  option (`:353-355`) or call `inbox.tick(ctx)` directly;
- **the bucket**: admission allows 30 messages per sender and refills 0.5/s
  (`pkg/peer/guard.go:41-49`), checked **before** the queue depth
  (`:145-151`), so 51 messages from one sender are refused `rate-limited` at
  #31. Drive the deliveries from **two** sender identities (30 + 21 messages;
  the bound is `peer.DefaultLimits.MaxQueuedPeerMessages`, 50) with distinct
  bodies — a repeated body inside 30 s is `duplicate`;
- **the session**: declare the session **not running** between deliveries
  (reuse the test file's seam for `runningHere`; if there is none, stop and
  report before inventing one).

The 51st message must be admitted. The bound is
`peer.DefaultLimits.MaxQueuedPeerMessages` (50, `pkg/peer/guard.go:37,48`) — there
is no package-level constant of that name.

**Verify**: the new test **fails** with a queue-full refusal at message 51.
Record the exact failure line in your report.

### Step 2 — clear the count for a session no run is using

In `tick`, after the `idleNow` bookkeeping and independent of subscriptions,
drain the counter for every session that `!in.runningHere(sessionID)` — that is
exactly the condition under which `queued`'s own doc comment says the count is not
owed to anyone ("everything given to a conversation whose run has not ended
since"). Iterate **`in.handed` itself**, not `in.idleSince`: `idleSince` only
holds sessions with an idle subscription (`pkg/process/peer_inbound.go:930-946`),
and a conversation that was merely handed messages has no key there — a deletion
placed in that loop never reaches the sessions this plan is about.

```go
in.mu.Lock()
for sessionID := range in.handed {
	if !in.runningHere(sessionID) {
		delete(in.handed, sessionID)
	}
}
in.mu.Unlock()
```

Keep the existing `idleNow` deletion (it also fires the notices; the two must not
be merged).

**Verify**: the step-1 test passes;
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'TestPeerInbound|TestPeerInbox' -count=1`
→ `ok`, including the pre-existing inbox tests.

### Step 3 — keep the bound meaningful

Add a second test, `TestPeerInboxQueueCounterHoldsWhileTheRunIsLive`: deliver
`peer.DefaultLimits.MaxQueuedPeerMessages` messages to a session that stays
running and assert the next one is still refused `queue-full`. Use the same
two-sender trick (the per-sender bucket, not the queue, is the first limit to hit)
and the same `fastPeerTick`/direct-`tick` cadence if the test needs a tick to run
at all. This is the property the bound exists for; without it step 2 could be
"fixed" by deleting the counter entirely.

**Verify**: passes, and reverting step 2 makes step 1 red while this one stays
green (do that check once, then restore).

### Step 4 — check what a receipt costs

`deliverNote` also increments `handed`. With step 2 in place a note for a
finished session is cleared on the next tick; confirm by reading `deliverNote`
and the `noteHanded` call inside `deliverPrimary` and noting in your report
whether a note for a **running** session should count against the queue. Do not
change the answer — only record what the code now does, so the reviewer sees the
intent.

## Test plan

- New: `TestPeerInboxQueueCounterDrainsWhenTheRunEnds` (step 1),
  `TestPeerInboxQueueCounterHoldsWhileTheRunIsLive` (step 3).
- Structural pattern: the existing `pkg/process/peer_inbound_test.go` tests —
  same fake arrival construction, same fake run state.
- Failure paths: the 51st message on an idle session is admitted; the 51st on a
  live session is refused; `held` (messages not yet handed) still counts.

## Done criteria

ALL must hold:

- [ ] `go vet ./...` exits 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/peer -count=1` passes
- [ ] Both new tests exist and the live-run one still refuses the 51st message
- [ ] Reverting step 2's drain makes the step-1 test red again (checked once, then restored); the live-run test stays green either way
- [ ] Only the two in-scope files are modified
- [ ] `plans/README.md` row for 224 updated

## STOP conditions

- `runningHere` has no seam in the test file and the inbox cannot be driven
  without starting a real run: stop and report — do not add a production seam
  only for the test without saying so.
- `queued()` turns out to be read anywhere else (grep) with a different meaning
  (e.g. persisted or shown in the UI): report before changing the counter.
- Clearing on `!runningHere` makes an existing test fail (a test may currently
  pin the never-decreasing behaviour): report the test and why it exists rather
  than editing its assertion.

## Maintenance notes

- If forebrain ever sends `notify_when_idle` (plan 201 scenario 10), the
  subscription-driven reset and the new run-state reset become two paths to the
  same clearing. Keep both: the subscription one also sends notices.
- A reviewer should check there is no path where a **live** session's count is
  cleared — that would remove the flood protection entirely.
