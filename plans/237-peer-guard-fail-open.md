# Plan 237: the same-tree git guard never fails open

> **Executor instructions**: Follow the steps in order; confirm each verification.
> If a STOP condition occurs, stop and report. When done, update this plan's row in
> `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 7d505aa3..HEAD -- pkg/process/peers.go pkg/process/peer_worktree.go pkg/process/peer_outbound.go`
> Content mismatch against the excerpts is a STOP condition.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED — the guard must still let ordinary commands through when no peer
  is in the tree; a wrong default here turns every `git` command into an approval.
- **Depends on**: plan 223 (the classifier must actually recognise the command
  before this guard's availability matters)
- **Category**: security
- **Planned at**: commit `7d505aa3`, 2026-11-04
- **Finding**: branch audit 2026-11-04, finding 17 (`introduced` by plans 195/198)

## Why this matters

Plan 198's rule is that a git command which would discard another agent's
uncommitted work asks the person **even in YOLO**. The approval is raised when
`peer.WorkTreePeers` reports a peer in the same work tree.

That answer comes from this process's peer service. If the service failed to start —
the socket directory is owned by someone else (`/tmp/fb-socks-<uid>` is a predictable
path on macOS, where `XDG_RUNTIME_DIR` is unset), a transient `EMFILE`, a full disk —
the failure is logged and **never retried**, `env.peers` is never set, and:

- `view()` returns an error,
- `WorkTreePeers` returns nil,
- the shell gate sees "no peers in this tree" and stays silent.

So a local user who pre-creates the socket directory, or any one-off startup failure,
turns the cross-agent guard off for the life of the process — and the process's own
agents keep running `git reset --hard` in a tree another agent is editing.

## Current state

`pkg/process/peers.go:298-312` — the one-shot start, with the failure logged and
dropped:

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

`pkg/process/peer_outbound.go:133-160` — `view()` errors when there is no service,
which is the answer the guard consumes.

`pkg/process/peer_worktree.go:455-465` — `WorkTreePeers` returns nil without a view.

`pkg/tool/shell_tool.go:212-226` — the gate: a recognised sweep **and** peers present
⇒ `ask_even_in_yolo`; otherwise the command proceeds.

`pkg/peer/paths.go:160-180` — `vetSocketDir` refusing a pre-created directory is
correct (it stops another user from intercepting frames); the bug is that refusal
being permanent and silent.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/tool -run 'Peer|WorkTree|Sweep' -count=1` | `ok` |

## Scope

**In scope**:
- `pkg/process/peers.go`
- `pkg/process/peer_worktree.go`
- `pkg/process/peer_outbound.go` (only the "service unavailable" answer)
- the matching `_test.go` files

**Out of scope**:
- `pkg/tool/shell_tool.go` — the port is where the answer is produced; the tool
  needs no change and adding an error channel to `tool.PeerWorkTree` would touch a
  second package for nothing.
- `pkg/peer/paths.go`'s vetting rules — they are correct; do not relax them.
- The socket path choice (`XDG_RUNTIME_DIR` vs `/tmp`) — a user-visible default, and
  changing it is its own decision.
- Plan 194's protocol, frames, admission.
- The TUI/web surfaces that display peer state.

## Steps

### Step 1 — record the fail-open

Add `TestGitSweepAsksWhenThePeerServiceIsDown` to `pkg/process`. The shell tool
cannot see service health (its port is `WorkTreePeers(ctx, root) []WorkTreePeer`
with no error channel — `pkg/tool/peer_worktree.go:14-26`), so test the port that
the tool actually calls:

- build the environment with `peerTestEnvFor` (`pkg/process/peers_test.go:71`) and
  install the port by hand: `env.Deps.Peers = env.peerDirectory()`
  (`peerDirectory` returns the `*peerOutbound`; `peer.Enabled()` needs both
  `FOREBRAIN_PEERS_DIR` and `FOREBRAIN_PEER_SOCKET_DIR`, which the helper sets);
- **do not** call `StartPeers` — that is the "service never started" condition;
- plant one real peer record under the FB rendezvous directory with a different
  pid/procStart and the same work tree (use the writer `pkg/peer/record.go`
  reads; copy the shape `peers_test.go` builds);
- call `env.Deps.Peers.WorkTreePeers(ctx, root)` and assert the peer is reported.

**Verify**: fails today (`peerOutbound.WorkTreePeers` returns nil when
`view()` errors, `pkg/process/peer_worktree.go:458-461`). Record the result.

### Step 2 — answer "who else is in this tree" without this process's service

Make the port's answer independent of whether **this** process registered. The
rendezvous directory is readable by any process of the user (that is how the
candidate list is built), so teach `peerOutbound.WorkTreePeers`
(`pkg/process/peer_worktree.go:446`) to fall back to it when `o.view()` fails:

- read the records of the current mode's directory with
  `peer.ReadRecords(peer.NewLayout(cc).Dir)` — the same reader
  `peerOutboundView.candidates()` uses (`pkg/process/peer_outbound.go:191-193`).
  `cc` is the same decision `targetMode` makes (`pkg/process/peers.go:405-440`):
  the `peers.claude_code` toggle **and** `detectClaudeCode()`. Reuse that rule;
  do not read both directories (cross-mode visibility is settled as "different
  toggles cannot see each other");
- keep only live records using a **socket-free** check. `peer.Alive` is not it:
  it ends with `socketAlive(r.MessagingSocketPath)` (a `net.DialTimeout`,
  `pkg/peer/liveness.go:123-155`) and every record this package writes carries
  that path. Add to `peer_worktree.go`:

  ```go
  // recordLiveWithoutSocket answers the only question the fallback needs —
  // does the process still exist — without dialling anything.
  func recordLiveWithoutSocket(r peer.Record) bool {
      if r.PIDDomain != "" && r.PIDDomain != peer.PIDDomain() {
          return true // another pid domain: take its pid at its word
      }
      if r.ProcStart == "" {
          return true
      }
      current, ok := peer.ProcStart(r.PID)
      return ok && current == r.ProcStart
  }
  ```

- drop this process's own record by pid + procStart (`os.Getpid()` +
  `peer.ProcStart`), and keep only records whose work tree matches;
- return the count.

Do **not** reuse `view()`: it queries the database for agent status, which is not
needed to answer "is anyone else here". Do **not** change `pkg/tool`: the port
already returns `nil` for "no peers", and moving the fallback below it is what
makes the guard see the truth without a new error channel.

**Verify**: step 1's test passes; the healthy path is unchanged (an existing peer
test still passes).

### Step 3 — retry a failed start, visibly

There is no loop after a failed first start: `start`
(`pkg/process/peers.go:344-366`) returns the `enterModeLocked` error **before**
`svc.running = true` / `go svc.tickLoop()`, and `tick()` returns immediately while
`!svc.running` (`:393-395`). The "retry next tick" promise lives in the mode-switch
branch (`:399`), which only runs once the loop exists. Restructure:

1. set `svc.running = true` and start `tickLoop` **before** the first
   `enterModeLocked`; add a `modeEntered bool` to the service;
2. in `tick`, while `!modeEntered`, attempt `enterModeLocked(target, false)`; on
   success set the flag, on failure log `Warn` once and then `Debug` on the same
   cadence as the mode switch (`peerTickInterval`);
3. `stop` must cancel the loop even when `modeEntered` is false (today it
   early-returns while `!running` — `:651-658`).

Keep `stop` idempotent.

**Verify**: a test starts the service against a rendezvous dir that is made
unusable on the first attempt and usable on the second, and asserts the service
becomes available without a restart (use whatever seam the tests already have for
the directory; if there is none, drive `start` twice and assert the second
succeeds).

### Step 4 — say it once, in the surface that can act on it

When the fallback in step 2 runs because the service is unavailable, log **one**
`Warn` naming the reason (a `loggedUnavailable bool` on `peerOutbound`, set under
its mutex, is enough), so a permanently broken directory costs one warning rather
than one per command. Do not add UI copy in this plan.

**Verify**: `go test ./pkg/process ./pkg/tool -count=1` → `ok`.

## Test plan

- New: the port test with no service (step 1), the directory-only reader's unit
  test (step 2: a planted peer record is found, a dead one is not, this process's
  own record is excluded), the retry test (step 3).
- Keep green: every existing `peer_worktree` / `peers` / shell-gate test, including
  "no peers ⇒ no approval" (ordinary `git status` must still run silently).
- Failure paths: no service; unreadable directory; a peer record that is dead; a
  peer in a different work tree.

## Done criteria

ALL must hold:

- [ ] `go vet ./...` exits 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/tool -count=1` passes
- [ ] A git sweep with a live peer in the tree is guarded **even when this process's peer service never started**: the port reports the peer (asserted), and the existing `pkg/tool` test shows the approval follows from it
- [ ] `git status` in a tree with no peers still runs with no approval (asserted)
- [ ] A failed service start is retried on the existing cadence (asserted)
- [ ] Only in-scope files modified (`pkg/tool` is untouched)
- [ ] `plans/README.md` row for 237 updated

## STOP conditions

- The directory-only reader would need a socket dial to answer liveness on some
  platform: report it. `peer.Alive` is disqualified for exactly that reason (it
  dials `MessagingSocketPath`); the fallback must stay non-blocking.
- Reading another mode's directory is required to answer correctly (a process in FB
  mode, a peer in CC mode): report it — cross-mode visibility is settled as
  "different toggles cannot see each other" (`plans/README.md`, 2026-10-30 ⑬), so
  the answer for that case is "no peers", and you must confirm that is intended
  rather than widen the read.
- Making the guard independent of the service makes an existing test fail because it
  pinned "no service ⇒ no guard": report the test and its rationale.

## Maintenance notes

- The invariant: **the guard's answer depends on the filesystem, not on this
  process's own registration**. Registration is for being *reached*; the guard is
  about *knowing*.
- A reviewer should check the healthy path is unchanged, that the fallback cannot
  block (no dials), and that a permanently broken directory costs one warning, not a
  warning per command.
- Deliberately deferred: showing the degraded state in the TUI footer / web peer
  drawer — the log line is enough until a surface has a place for it.
