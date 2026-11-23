# Plan 230: every peer message that was accepted is either run, or dropped with a record — never silently

> **Executor instructions**: Follow the steps in order; confirm each verification.
> If a STOP condition occurs, stop and report. When done, update this plan's row in
> `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 7d505aa3..HEAD -- pkg/tui/notify.go pkg/tui/run.go pkg/tui/chat_surface.go`
> Content mismatch against the excerpts is a STOP condition.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `7d505aa3`, 2026-11-04
- **Finding**: branch audit 2026-11-04, finding 10 (`introduced` by plans 196/199)

## Why this matters

When another agent messages this terminal, the engine decides "idle → wake it and
run a turn". The wake succeeds, the engine records the message as **delivered**
and tells the sender so. `pkg/tui` then parks the prompt in a **single slot**: the
second message that arrives before the first has been consumed overwrites it, and
the first is never run — even though its sender was told it was.

The slot is worse than discarded on a conversation switch: nothing clears it. The
switch path drops queued input, subagent queues and auto-continue
(`pkg/tui/run.go:1950-1969`) but leaves `peerDue` alone, so the parked prompt runs
on the next idle pass and `executePeerTurn` dispatches it to the **old**
`prompt.SessionID` (`pkg/tui/run.go:4215-4218`, `pkg/tui/chat_surface.go:68-85`) —
a peer turn for a conversation the person is no longer looking at. The idle loop
also skips the slot entirely while a slash panel is open. In every case the sender
believes the message landed. Two agents coordinating over peer messages can
therefore lose exactly the message that says "stop, don't do that".

## Current state

`pkg/tui/run.go:1729-1732` — the slot:

```go
	// peerDue is a peer message waiting for a turn of this conversation: the
	// idle loop runs it, the way it runs a due continuation.
	peerDue *process.PeerPrompt
```

`pkg/tui/notify.go:2673-2689` — a single-slot setter/taker:

```go
func (s *streamState) setPeerDue(prompt process.PeerPrompt) {
	if s == nil {
		return
	}
	s.peerDue = &prompt
}

func (s *streamState) takePeerDue() (process.PeerPrompt, bool) { ... }
```

`pkg/tui/notify.go:2465-2480` — the wake handler: a message for another session is
dropped silently; otherwise it overwrites whatever was parked:

```go
	if !sameSessionID(msg.SessionID, state.sessionID) {
		return true
	}
	state.setPeerDue(msg.Prompt)
	return true
```

`pkg/tui/run.go:765-777` — the idle loop consumes one prompt per pass, and only
when no panel is open:

```go
		if state.panel == nil {
			if prompt, due := state.takePeerDue(); due {
				...
			}
		}
```

`pkg/tui/notify.go:2781` — the loop's wake condition reads the same slot:
`return s.autoContinueWake() || s.heldPeersHeld() || s.peerDue != nil`.

`pkg/process/peer_inbound.go:705-737` — the engine side: a `Wake` that returns
nil is treated as delivered (receipt + `noteHanded`), so the drop is invisible to
the sender.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Peer|Notify' -count=1` | `ok` |
| Package | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/process -count=1` | `ok` |

## Scope

**In scope**:
- `pkg/tui/notify.go`
- `pkg/tui/run.go` (`streamState` and the switch path live here; there is no
  `pkg/tui/stream_state.go`)
- `pkg/tui/chat_surface.go` (only if the dispatch check needs the same guard)
- the matching `_test.go` files

**Out of scope**:
- `pkg/process/peer_inbound.go` — the engine's delivery contract stays
  "Wake == delivered". Do not turn Wake into a queue.
- The held-message (approval) path (`heldPeers`, `askHeldPeerMessage`) — a
  different mechanism with its own per-message question.
- `pkg/gateway`'s detached-turn wake path.

## Steps

### Step 1 — reproduce: two wakes, one turn

Add `TestTwoPeerWakesBothRun` to `pkg/tui`: build a `streamState` for one session,
post two `PeerWakeMsg` values through `handlePeerWakeNotification`, then drain with
`takePeerDue` until empty and assert **two** prompts come out, in arrival order.

**Verify**: fails today (the second drain finds nothing). Record the failure.

### Step 2 — make the slot a queue

Replace `peerDue *process.PeerPrompt` with a slice drained oldest-first
(`peerDue []process.PeerPrompt`). `setPeerDue` appends; `takePeerDue` pops the
first. Bound it at the transport's own limit with a **local** constant — `pkg/tui`
must not import `pkg/peer` (the rule and its reason are stated at
`pkg/process/peers.go:54-58`), and there is no package-level
`peer.MaxQueuedPeerMessages` anyway: it is a field of `peer.DefaultLimits`
(`pkg/peer/guard.go:37,48`). Write `const maxPeerWakeQueue = 50` with a comment
naming the engine bound, and drop the **newest** message with an `slog.Warn` when
full — an accepted message that cannot be run must be visible.

**Verify**: step 1's test passes; the wake condition still returns true while the
queue is non-empty.

### Step 3 — one turn per message, and a refusal when one cannot run

The idle loop currently consumes one prompt and `continue`s, which already gives
one turn per message — keep that. Two silent-drop paths need a visible record:

- session mismatch in `handlePeerWakeNotification`: the message belongs to a
  conversation this terminal no longer shows. There is **no** engine refusal hook
  reachable from `pkg/tui` — `RefusePeer`/`refusePeer` have no hits, and
  `peerInbox.refuse` (`pkg/process/peer_inbound.go:759-760`) is unexported. This
  plan therefore records the drop: `slog.Warn` with the message id and the
  session, and does **not** claim a `refused` receipt. Say so in the report; an
  engine callback is its own plan.
- a full queue (step 2): same rule.

**Verify**: a new test asserts the mismatch path logs and does not consume the
slot; `go test ./pkg/tui -count=1` → `ok`.

### Step 4 — a panel must not strand the queue

A prompt parked while `state.panel != nil` stays parked until the panel closes —
that is intended. Add a test asserting it is still there after the panel closes
and runs on the next idle pass (the property the current code already has, pinned
so step 2 cannot regress it).

**Verify**: `go test ./pkg/tui -run TestPeer -count=1` → `ok`.

### Step 5 — a conversation switch must not run the old conversation's peer turn

In the switch path (`pkg/tui/run.go`, beside `discardQueuedInput()`), drain
`peerDue` and log each dropped prompt with its message id and session, the same
way the other three queues report what they dropped. Do not carry the prompts to
the new conversation and do not leave them for the next idle pass: either would
run a turn in the wrong session.

**Verify**: a test parks a peer prompt, switches to another session, and asserts
(a) no turn is dispatched for the old session and (b) one log record names the
dropped message id.

## Test plan

- New: `TestTwoPeerWakesBothRun`, the mismatch-path test, the panel-deferral test,
  the conversation-switch test (step 5).
- Structural pattern: the existing peer notification tests in `pkg/tui` (grep
  `PeerWakeMsg`).
- Failure paths: second wake before the first runs; wake for another session;
  wake while a panel is open; queue full; switch while a prompt is parked.

## Done criteria

ALL must hold:

- [ ] `go vet ./...` exits 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/process -count=1` passes
- [ ] `grep -n "peerDue \*process.PeerPrompt" pkg/tui/run.go` returns nothing
- [ ] Two consecutive wakes produce two turns (asserted)
- [ ] No peer message is dropped without either running or a log line naming its id
- [ ] Only in-scope files modified
- [ ] `plans/README.md` row for 230 updated

## STOP conditions

- `pkg/architecture` forbids the import you would need for the bound: use the
  local constant and say so.
- The engine has no refusal hook reachable from `pkg/tui`: do steps 1, 2, 4, log
  the mismatch case, and report the missing hook as a follow-up.
- Draining a queue changes turn ordering relative to auto-continue: report the
  precedence you observed (`autoContinueWake` may be checked first) rather than
  reordering them.

## Maintenance notes

- The engine's contract is "Wake returned nil ⇒ delivered". Any new surface that
  accepts a wake inherits the duty to run it; the queue bound is the only place a
  message may be refused, and refusing must be visible.
- A reviewer should check the drop direction (newest, not oldest) and that the
  panel-deferral still holds.
