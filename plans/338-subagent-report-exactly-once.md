# Plan 338: A subagent's final report reaches its dispatcher exactly once

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 9a3fba8c..HEAD -- pkg/run/subagent.go pkg/run/peer_tools.go pkg/process/one_shot.go pkg/run/subagent_test.go pkg/run/peer_tools_test.go pkg/process/peer_inbound_test.go`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED
- **Depends on**: plans/335-*.md, plans/336-*.md (same-file serial convention: all edit `pkg/run/subagent.go`; regions are disjoint so a reviewer may parallelize with a rebase, but default to serial)
- **Category**: bug
- **Planned at**: commit `9a3fba8c`, 2026-11-20

## Why this matters

When a dispatcher launches a subagent with `subagent_send` (fire-and-forget) and
then blocks on `subagent_wait`, the model receives the subagent's final report
**twice in full, plus a 600-rune truncated copy**: once inside the
`subagent_wait` tool result (`record.output` and `progress.last_message`), and
again as an automatic `<agent-message from="subagent-…">… Its final report: …`
peer message steered into the running turn. This was confirmed against a real
session in `~/.forebrain/state/forebrain.state.sqlite` (session
`cli-7a60b067-ffaf-4d04-b555-8d32b6619891`: tool row 539985 carries the full
report, user row 539992 carries the identical report, and the model reasons
about it twice — rows 539991 and 539993). It wastes context tokens and makes the
model re-integrate a result it already integrated.

The existing guard only covers *blocking dispatch* (`subagent_run`,
`subagent_fanout`, `subagent_continue` set `AwaitedByDispatcher` at dispatch
time). An async execution carries no such mark, and `subagent_wait` does not add
one, so the auto-report fires even while a wait is blocked. The owner's decided
semantics: **when a live `subagent_wait` is attached at execution end, the report
source must not emit the peer message at all** (the wait is the single channel);
the peer report is only for a dispatcher that is *not* waiting. A reverse
ordering (report delivered first, wait attaches afterwards) must also not
duplicate: the later wait omits the report body. The subagent's own voluntary
`peer_send` is explicitly **out of scope** and must keep working unchanged.

## Current state

Files and their role:

- `pkg/run/subagent.go` — subagent lifecycle: the channel, the execution loop,
  the end-report to the composition root, and the `subagent_wait` tool.
- `pkg/run/peer_tools.go` — `peer_send`/`peer_list`, the per-conversation peer
  inbox, and `awaitSubagent` (the blocking wait primitive).
- `pkg/process/one_shot.go` — `deliverSubagentReport`, the composition root's
  automatic peer report (builds the `<agent-message>` envelope and delivers it).
- `pkg/turn/submit.go` — the continuation scheduler; reads
  `AwaitedByDispatcher` to decide continuation ownership (must NOT change).

Excerpts as they exist at `9a3fba8c`:

`pkg/run/subagent.go:1222` and `:1305` (execution loop; `awaited` is fixed at
dispatch time from the context, never from a later wait):

```go
	awaited := subagentAwaitedFromContext(ctx)
	ch := subagentChannelFor(fac, record)
	...
		reportSubagentExecutionEnded(fac, record, err, awaited, out)
		return out, err
```

`pkg/run/subagent.go:1331-1346` (the end event handed to the composition root):

```go
func reportSubagentExecutionEnded(fac Factory, record agent.HistoryEntry, err error, awaited bool, output string) {
	exec := subagentExecutorOf(fac)
	if exec == nil {
		return
	}
	inline, _ := budgetSubagentReport(fac, strings.TrimSpace(record.TaskID), output, subagentReportInlineChars)
	exec.SubagentExecutionEnded(context.Background(), SubagentExecutionEnd{
		ConversationSessionID: strings.TrimSpace(record.SessionID),
		WorkerSessionID:       strings.TrimSpace(record.WorkerSessionID),
		AgentKey:              subagentRosterKey(record),
		RunID:                 strings.TrimSpace(record.RunID),
		Err:                   err,
		Output:                inline,
		AwaitedByDispatcher:   awaited,
	})
}
```

`pkg/run/subagent.go:744-790` — `subagentChannel` struct; its `mu sync.Mutex`
guards `running`, `surface`, `firstToken*`. There is **no** waiter count today.
`pkg/run/subagent.go:834` — `subagentChannelFor(fac, record)` returns (creating
on first use) the channel keyed by `record.WorkerSessionID`.

`pkg/run/subagent.go:3860-3888` — the `subagent_wait` tool. Registry-handle
branch blocks in `awaitSubagent`; the fallback branch reads the persisted ledger
record (this is the path a wait takes when the execution already finished and
its handle was removed from the registry):

```go
			if handle, ok := agent.RegistryFor(fac.subagentScopeRoot()).Get(query); ok && handle != nil {
				waitCtx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				wait := awaitSubagent(handle, waitCtx, tool.ConversationSessionIDFromContext(ctx))
				resp := map[string]any{
					"record":   wait.record,
					"progress": buildSubagentProgress(ctx, fac, wait.record),
				}
				if wait.timedOut { resp["timed_out"] = true }
				if wait.messaged { resp["message_from_subagent"] = true }
				b, _ := json.Marshal(resp)
				return string(b), nil
			}
			record, ok, err := lookupSubagentRecord(fac.subagentScopeRoot(), ctx, in)
			...
			b, _ := json.Marshal(map[string]any{
				"record":   record,
				"progress": buildSubagentProgress(ctx, fac, record),
			})
			return string(b), nil
```

`pkg/run/subagent.go:5039-5063` — `SubagentExecutionEnd` carries
`ConversationSessionID`, `WorkerSessionID`, `AgentKey`, `RunID`, `Err`, `Output`,
`AwaitedByDispatcher`. It has **no** execution id and no waiting flag.

`pkg/process/one_shot.go:86-118` — the delivery gate and the single delivery
attempt (refusal is logged and dropped, never retried):

```go
func (env *Environment) deliverSubagentReport(end run.SubagentExecutionEnd) {
	sessionID := strings.TrimSpace(end.ConversationSessionID)
	agentKey := strings.TrimSpace(end.AgentKey)
	switch {
	case end.AwaitedByDispatcher, sessionID == "":
		return
	case errors.Is(end.Err, context.Canceled):
		return
	case subagentEndWaitsForItsContinuation(end.Err):
		return
	}
	if env.peerDelivery == nil { ...; return }
	prompt := PeerPrompt{ SessionID: sessionID, Text: subagentReportEnvelope(end), Origin: state.MessageOriginPeer, MsgID: uuid.NewString(), FromName: agentKey }
	if refusal := env.peerDelivery.deliverPrimary(context.Background(), sessionID, prompt); refusal != "" {
		slog.Debug("subagent report was not delivered", ...)
	}
}
```

`pkg/turn/submit.go:688-694` — why `AwaitedByDispatcher` must NOT be overloaded:
it transfers continuation ownership to the dispatcher (`a.closeRun(...)`), which
is wrong for a merely-waiting async dispatcher (a usage-limit failure must still
arm the subagent's own continuation):

```go
	if end.AwaitedByDispatcher {
		a.closeRun(strings.TrimSpace(end.WorkerSessionID))
		return
	}
```

`pkg/run/peer_tools.go:516-535` — `awaitSubagent(handle, waitCtx, convSID)`
blocks on `handle.Wait` or the conversation inbox; it does not touch the channel.
`pkg/agent/subagent_registry.go:32-49` — `Handle.Finish` removes the handle from
the registry, which is why a finished execution's wait reads the ledger, and why
any "already delivered" fact must NOT live on the handle.

Conventions to match (exemplars):

- Comment style: doc comments explain WHY and name the owner decision / plan,
  e.g. `pkg/process/one_shot.go:74-85` and `pkg/run/peer_tools.go:476-479`.
  Model-visible text is provider-neutral and byte-stable; do not invent new
  model-visible sentences beyond the one flag/note this plan specifies.
- Mutex-guarded channel state: follow the existing `ch.mu.Lock()/Unlock()`
  accessors on `subagentChannel` (e.g. `setRunning`, `paused`).
- Process-local keyed maps with a package mutex: follow `peerInbox` /
  `peerInboxes` in `pkg/run/peer_tools.go:471-490`.
- Tests: table/leg style with `t.Fatalf` carrying got/want, fixtures
  `newPeerFixture` / `f.record` / `f.call` / `decodePeerAnswer`
  (`pkg/run/peer_tools_test.go:312-360`), and
  `newPersistenceFixture` + `recordingSchedulerExecutor` +
  `reportSubagentExecutionEnd` (`pkg/run/subagent_test.go:5954-6036`), and
  `startPeerTestService` / `peerTestView` / `oneWake`
  (`pkg/process/peer_inbound_test.go:1431-1479`).

## Commands you will need

Run from the repo root. CGO and the fts5 tag are mandatory; carry
`PKG_CONFIG_PATH` yourself when calling `go` directly (the Makefile sets it only
inside `make`). Never inherit an ambient `FOREBRAIN_HOME`; use a scratch home.

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Env | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"` | export ok |
| Build | `CGO_ENABLED=1 go build -tags fts5 ./pkg/run ./pkg/process ./pkg/turn` | exit 0 |
| Focused tests | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/process ./pkg/turn -count=1` | all ok |
| Single test | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '<Name>' -count=1 -v` | PASS |
| Format | `gofmt -l cmd pkg third_party plugins` | prints nothing |
| Vet | `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0 |
| Full suite (final gate) | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | exit 0 (a known pkg/process LSP timing flaky, `TestLSPRecommendationPublishedToSurface`, is pre-existing and unrelated; if it is the only failure, re-run that test alone to confirm and note it) |

## Scope

**In scope** (the only files you should modify):

- `pkg/run/subagent.go`
- `pkg/run/peer_tools.go`
- `pkg/process/one_shot.go`
- `pkg/run/subagent_test.go` (add tests)
- `pkg/run/peer_tools_test.go` (add tests)
- `pkg/process/peer_inbound_test.go` (add tests)

**Out of scope** (do NOT touch, even though they look related):

- `pkg/turn/submit.go`, `pkg/gateway/serve_run.go`, `pkg/tui/notify.go` — they
  consume `AwaitedByDispatcher` for continuation ownership / surface notices.
  This plan adds a *separate* flag; their behavior must stay byte-identical.
- `peer_send` / `peerSendToDispatcher` / `peerMessageText` in
  `pkg/run/peer_tools.go` — the subagent's voluntary messaging is explicitly
  excluded by the owner; only add the new latch helpers to this file.
- `subagent_status` / `subagent_list` / `buildSubagentProgress` semantics — they
  also return `output`/`last_message`; deduplicating those is a separate
  decision, deferred. Only the `subagent_wait` closure may zero
  `progress.LastMessage` on a redacted answer.
- `pkg/agent/subagent_registry.go` — do not put state on `Handle`; it is removed
  at `Finish`.
- Any surface rendering (TUI/web) of the peer card or the wait card.

## Git workflow

- Branch: `338-subagent-report-exactly-once`, worktree `.worktrees/338-subagent-report-exactly-once`, started at `9a3fba8c` (or rebased onto the current stack tip if 335/336 landed first; re-run the drift check after any rebase).
- Conventional commits, small steps, e.g. `run: a live subagent_wait suppresses the execution-end peer report` (match `git log --oneline -10`).
- Do NOT push, merge, or open a PR unless the operator instructed it.

## Steps

### Step 1: Count live waits on the subagent channel

In `pkg/run/subagent.go`, add a `waiters int` field to `subagentChannel` inside
the `mu`-guarded group (beside `running`/`surface`), with a doc comment saying
it counts the dispatcher's `subagent_wait` calls blocked on this subagent's
running execution and that the execution-end report reads it (plan 338). Add
three mutex-guarded accessors next to the other channel accessors:
`addWaiter()`, `removeWaiter()` (clamp at 0), `hasLiveWait() bool`.

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/run` → exit 0.

### Step 2: Register the wait for its whole blocking lifetime

In the `subagent_wait` closure's registry-handle branch
(`pkg/run/subagent.go:3860`), immediately after the handle is obtained and
before `awaitSubagent`, add:

```go
			ch := subagentChannelFor(fac, handle.Snapshot())
			ch.addWaiter()
			defer ch.removeWaiter()
```

The `defer` guarantees every return path (done, inbox early-return, timeout)
unregisters. Do not register in the ledger fallback branch (no live execution).

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/run` → exit 0.

### Step 3: Carry "dispatcher waiting" and the execution id on the end event

In `pkg/run/subagent.go`:

- Add two fields to `SubagentExecutionEnd` (:5039): `ExecutionID string` (the
  execution this end reports; the latch key) and `DispatcherWaiting bool` (a
  `subagent_wait` was still blocked at the moment the execution ended; the wait
  receives the final record so the source emits no peer message; unlike
  `AwaitedByDispatcher` it does NOT transfer continuation ownership). Document
  both in the existing comment style.
- Change `reportSubagentExecutionEnded` (:1331) to take an extra
  `dispatcherWaiting bool` parameter and set `ExecutionID:
  strings.TrimSpace(record.ExecutionID)` and `DispatcherWaiting:
  dispatcherWaiting` on the event.
- At the call site (:1305) pass `ch.hasLiveWait()` as that argument:
  `reportSubagentExecutionEnded(fac, record, err, awaited, ch.hasLiveWait(), out)`.

Do NOT change how `awaited` / `AwaitedByDispatcher` is computed.

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/run ./pkg/process ./pkg/turn`
→ exit 0, and
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestEverySubagentExecutionIsReportedToItsScheduler|TestSubagentExecutionEndCarriesTheBudgetedReport' -count=1`
→ PASS (scheduler semantics unchanged).

### Step 4: The report source emits nothing while a wait is live; mark successful deliveries

In `pkg/run/peer_tools.go`, beside `peerInbox`, add a process-local latch keyed
by execution id (mirror the `peerInboxes` map+mutex shape):

```go
var (
	subagentReportHandedMu sync.Mutex
	subagentReportHanded   = map[string]struct{}{}
)

// MarkSubagentReportHanded records that one execution's final report reached
// its dispatcher as a peer message, so a later subagent_wait omits the report
// body instead of repeating it (plan 338).
func MarkSubagentReportHanded(executionID string) { ... }   // trim; no-op on empty

// SubagentReportHanded reports whether the report for executionID was handed
// to the dispatcher as a peer message.
func SubagentReportHanded(executionID string) bool { ... }
```

In `pkg/process/one_shot.go` `deliverSubagentReport`:

- Add `case end.DispatcherWaiting:` to the skip switch (:89-96), with a comment
  in the style of the existing three: a dispatcher blocked on `subagent_wait`
  receives the final record from that call, so the source emits no peer report.
- After the delivery attempt, mark handed **only on success**:

```go
	if refusal := env.peerDelivery.deliverPrimary(context.Background(), sessionID, prompt); refusal != "" {
		slog.Debug("subagent report was not delivered", ...)
	} else {
		run.MarkSubagentReportHanded(end.ExecutionID)
	}
```

  On refusal do NOT mark: the wait must still return the full report so a
  refused delivery never loses the result.

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/run ./pkg/process` → exit 0,
and `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'TestSubagentEnd' -count=1` → PASS.

### Step 5: A wait that finds the report already delivered omits the body

In `pkg/run/subagent.go` add:

```go
// redactHandedSubagentReport drops the report body from a finished record whose
// report already reached the dispatcher as a peer message, so the wait answers
// with status and a pointer instead of the same words twice (plan 338).
func redactHandedSubagentReport(record agent.HistoryEntry) (agent.HistoryEntry, bool) {
	switch record.Status {
	case agent.StatusOK, agent.StatusFailed, agent.StatusCancelled:
	default:
		return record, false
	}
	if !SubagentReportHanded(record.ExecutionID) {
		return record, false
	}
	record.Output = ""
	return record, true
}
```

In BOTH `subagent_wait` branches, run the record through it before marshalling;
when redacted, also zero `progress.LastMessage` (the 600-rune copy of the same
report) and set `resp["report_already_delivered_as_message"] = true`. Keep
`timed_out` / `message_from_subagent` exactly as today. A non-terminal record
(running / empty status) is never redacted. Do NOT mark handed when a wait
consumes a terminal record — the end has already passed, so no delivery can
follow it.

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/run` → exit 0.

### Step 6: Regression tests, then the full gates

Add the tests in the Test plan below, run them red-then-green where practical
(Step 5's redaction test fails before Step 5; Step 3/4's end-flag test fails
before Step 3/4), then run the focused packages, `gofmt`, `vet`, and the full
suite with a scratch home.

**Verify**: the Commands table rows "Focused tests", "Format", "Vet", "Full suite".

## Test plan

- `pkg/run/subagent_test.go` — `TestSubagentExecutionEndSkipsReportWhenAWaitIsLive`:
  model after `reportSubagentExecutionEnd` (:6012) for the
  `newPersistenceFixture` + `recordingSchedulerExecutor` wiring, and after
  `TestSubagentWaitReturnsWhenASubagentWrites` (`peer_tools_test.go:312`) for
  attaching a blocking wait. Shape: create a running ledger entry
  (`newPeerFixture`/`f.record`), `agent.RegistryFor(...).Start(entry, func(){})`;
  launch `f.call(..., "subagent_wait", {task_id, timeout_ms:8000})` in a
  goroutine; poll `subagentChannelFor(f.fac, entry).hasLiveWait()` until true;
  then drive `runSubagentExecution(ctx, f.fac, entry, func(...) (string, error) { return "the report", nil })`;
  assert the recorded end has `DispatcherWaiting == true`,
  `AwaitedByDispatcher == false`, `Output == "the report"`; then
  `handle.Finish(terminalEntry)` to release the wait and assert the wait answer
  carries `record.output == "the report"` and NO
  `report_already_delivered_as_message` flag (the wait is the single channel).
- `pkg/run/peer_tools_test.go` — `TestSubagentWaitOmitsReportAlreadyDeliveredAsMessage`:
  two legs on a finished entry (`StatusOK`, `Output:"r"`, distinct
  `ExecutionID`): leg A calls `MarkSubagentReportHanded(execID)` first → wait
  answer has `record.output == ""`, `progress.last_message == ""`,
  `report_already_delivered_as_message == true`; leg B (no mark) →
  `record.output == "r"` and no flag. Add a running-record leg asserting no
  redaction.
- `pkg/process/peer_inbound_test.go` — `TestSubagentEndMarksReportHandedOnlyOnDelivery`:
  model after `TestSubagentEndLeavesAwaitedCancelledAndUsageLimitAlone` (:1431).
  A tracked running dispatcher + a non-awaited end with
  `ExecutionID:"exec-338-delivered"` → one steer AND
  `run.SubagentReportHanded("exec-338-delivered") == true`; an
  `AwaitedByDispatcher` end and a `DispatcherWaiting` end (distinct ids) → no
  steer and not marked.
- Keep `TestSubagentEndLeavesAwaitedCancelledAndUsageLimitAlone` and
  `TestEverySubagentExecutionIsReportedToItsScheduler` green unchanged.
- Verification: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/process -count=1` → all pass including the new tests.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./pkg/run ./pkg/process ./pkg/turn` exits 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/process ./pkg/turn -count=1` exits 0; the three new tests exist and pass
- [ ] `gofmt -l cmd pkg third_party plugins` prints nothing
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./...` exits 0
- [ ] `grep -n "DispatcherWaiting" pkg/run/subagent.go pkg/process/one_shot.go` shows the field, the call-site argument, and the skip case
- [ ] `grep -n "AwaitedByDispatcher" pkg/turn/submit.go pkg/gateway/serve_run.go pkg/tui/notify.go` unchanged from `9a3fba8c` (no overload)
- [ ] Full suite with scratch home exits 0 (or only the pre-existing unrelated pkg/process LSP flaky, confirmed by a solo re-run)
- [ ] No files outside the in-scope list are modified (`git status`)
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- The code at the locations in "Current state" doesn't match the excerpts (drift).
- A step's verification fails twice after a reasonable fix attempt.
- The fix appears to require touching an out-of-scope file (in particular
  `pkg/turn/submit.go`, `pkg/gateway/serve_run.go`, `pkg/tui/notify.go`, or the
  `peer_send` path).
- You find yourself setting `AwaitedByDispatcher` for the waiting case: that
  transfers continuation ownership and would drop usage-limit auto-continue for
  async dispatches. Use `DispatcherWaiting` instead.
- The test fixtures (`newPeerFixture`, `newPersistenceFixture`,
  `recordingSchedulerExecutor`, `startPeerTestService`) differ from the
  exemplars cited; adapt only within the in-scope test files, and report if a
  fixture cannot express a live wait deterministically.
- You discover a delivery path that reports a finished execution besides
  `deliverSubagentReport` (the latch would then be incomplete).

## Maintenance notes

- The latch is process-local and keyed by execution id, cleared on restart.
  That is acceptable because auto-report delivery is also process-local; a
  post-restart wait reads the ledger and returns the report once (no peer
  message was delivered post-restart).
- `subagent_status` / `subagent_list` still return `output`/`last_message`; a
  dispatcher that polls status after a delivered report can still see a second
  copy. Deliberately deferred (owner scope: wait + auto-report source only). If
  that is ever closed, consult `SubagentReportHanded` the same way.
- Any NEW blocking read of a terminal subagent record must consult
  `SubagentReportHanded` or it reintroduces the duplicate.
- Reviewers should scrutinize: the `defer ch.removeWaiter()` placement (every
  wait return path), the success-only `MarkSubagentReportHanded` (refusal must
  not mark), and that `DispatcherWaiting` never reaches the continuation
  scheduler.