# Plan 358: A `process` tool — start a long-lived command, wait for it to be ready, read its log, kill it

> **Executor instructions**: Follow this plan step by step. Run every verification
> command and confirm the expected result before moving to the next step. If
> anything in the "STOP and report" section occurs, stop and report — do not
> improvise. When done, update the status row for this plan in `plans/README.md`.
>
> **Drift check (run first)**:
> `git status --porcelain -- pkg/safety pkg/tool pkg/run` and
> `git diff --stat 6541b8ab -- pkg/safety pkg/tool pkg/run`.
> A `6541b8ab..HEAD` range diff is **always empty here** (the baseline is HEAD);
> use the two commands above. If an in-scope file moved, compare the "Current
> state" anchors against the live code before proceeding; on a mismatch, STOP.

## Status

- **Category**: feature / tool
- **Priority**: P1
- **Effort**: L
- **Risk**: MED–HIGH (a new spawn path in `pkg/safety`, and processes that can outlive the turn that started them)
- **Depends on**: 357 (loopback) — a background server the driver cannot reach is useless. Can be *built* without 357 but not *used*.
- **Blocks**: 360 (a browser needs a process to drive), and the runtime-verification skill
- **Closes**: gap 1 (no background/async execution) and gap 3 (no readiness probe) from the 2026-11-22 tool audit
- **Planned at**: commit `6541b8ab`, 2026-11-23
- **Status**: **TODO — unblocked by owner rulings 2026-11-23** (after
  `review-plan plans/353-365` found two false premises). The findings stay here
  because they define the scope:
  ① **There is no spawn path to "return early from".** Spawn and wait are fused
  inside each of the four backends (`backend_darwin.go:39-50`,
  `backend_linux.go:33-43`, `backend_windows.go:68`, `manager.go:524-546`);
  `RunCommand` (`manager.go:257`) never calls `waitForCommandContext` itself, and
  `pkg/safety/process.go` holds the capture/wait/kill helpers, not the spawn.
  **Owner ruling: the split is authorized** — `pkg/safety/manager.go` and the
  three backend files are in scope (Step 1), with the two lifetime traps handled
  explicitly and **zero changes to existing `pkg/safety` tests**.
  ② **There is no session- or run-scoped teardown.** `tool.State` has no
  `Close`/`Shutdown`; `Runner.Close` (`pkg/run/runner.go:3554`) closes only the
  MCP hub/registry and runs at **runner** discard (`pkg/process/open.go:264/824/836`);
  the product has no interactive session-end event (`DeleteSessions`' only caller
  is `pkg/process/cron_service.go:316`), and several sessions share one Runner
  (`runner.go:242`). **Owner ruling: runner-level lifetime** — kill-all hangs on
  `Runner.Close`; the registry records which conversation session started each
  process so `list` shows it; the tool description states the lifetime honestly.
  ③ **Runner-level alone still leaks on a crash.** A clean TUI quit reaches
  `Env.Close()` (`pkg/tui/chat_session.go:986` → `open.go:264-266`), and pool
  eviction closes an idle Runner (`runner_pool.go:24-25`, 24h quiet at `:123`;
  the 30-minute idle release only frees MCP connections, it does not kill).
  But a SIGKILL/crash runs no teardown: Linux has `--die-with-parent`
  (`backend_linux.go:81`), **macOS seatbelt and Windows have no parent-death
  tie**, and nothing reaps orphans at startup today. **Owner ruling: PID ledger
  + reap-on-start** (Step 4b) — the crash window shrinks to "until the next
  launch", not "forever".

## Why this matters

The `shell` tool waits. Its default deadline is 120s
(`pkg/tool/shell_tool.go:85`, `defaultShellCommandTimeout`) and a command that
exceeds it is killed, keeping partial output plus `timed_out=true`. That is the
right contract for a build or a test and the wrong one for a server: `npm run
dev` does not finish, so it always times out, and the model learns to
background it by hand.

There is no alternative. Verified by grep across `pkg/tool`, `pkg/run` and
`pkg/turn`: **no `run_in_background`, no `BackgroundShell`, no `ProcessHandle`**.
So plans 353/354 make the model write a driver script that does
`nohup … &` or drives tmux, and then:

- nothing records what the process printed, so the model re-reads a log file it
  guessed the path of;
- nothing knows the process exists, so it survives the turn, the session and
  sometimes the machine's reboot, holding its port;
- nothing waits for readiness, so every driver hand-rolls a `curl --retry` loop
  with a magic sleep;
- the model cannot kill what it started, because it never got a handle.

This plan adds the missing primitive. It closes **two** of the five audited gaps
at once, because "start it" and "know when it is ready" are the same object: the
thing that holds the handle is the thing that can wait on it.

## Current state

Anchors verified at `6541b8ab`:

| File | Line | Role |
|---|---|---|
| `pkg/safety/process.go` | `:209-221` | `waitForCommandContext(ctx, cmd)` — the wait, already a separate function |
| `pkg/safety/process.go` | `:185-197` | `commandCapture.result()` — builds `CommandResult` from streamed buffers and the spool |
| `pkg/safety/process.go` | `:174-176` | `OnOutput` is already called synchronously from the pipe readers while the command runs |
| `pkg/safety/process.go` | `:219` | `killProcessGroup(cmd)` — already exists and is used on cancellation; it is **SIGKILL-only** (`backend_unix.go:41-47`), there is no TERM path |
| `pkg/safety/manager.go` | `:257`, `:597` | `RunCommand` (policy resolution) → `runSandboxCommand` (backend dispatch, and `runCtx, cancel := context.WithTimeout(ctx, req.Timeout); defer cancel()` at `:598-599`) |
| `pkg/safety/backend_darwin.go` | `:19-75` | `runSeatbeltCommand`: writes a temp profile, `defer os.Remove(profPath)` (`:35`), `defer cancel()` (`:38`), `cmd.Start()` (`:46`) then `waitForCommandContext` (`:50`) — **one body** |
| `pkg/safety/backend_linux.go` | `:20-45` | `runBubblewrapCommand`: same fusion (`Start` `:39`, wait `:43`), plus `defer cleanupLinuxMissingDenyMarkers` (`:29`) |
| `pkg/safety/manager.go` | `:524-546` | `runHostCommand`: the unsandboxed path, same fusion |
| `pkg/safety/sandbox_config.go` | `:174-181` | `OutputSpoolDir` and the `OnOutput` field + its contract comment |
| `pkg/safety/backend_linux.go` | `:81` | bubblewrap runs with `--die-with-parent` |
| `pkg/tool/shell_tool.go` | `:592-595` | `spoolDir = filepath.Join(rt.StateRoot(), "state", "tool-output")` — the spool convention to mirror |
| `pkg/tool/shell_tool.go` | `:596-609` | `commandRequest(...)` — how a `CommandRequest` is assembled |
| `pkg/tool/shell_tool.go` | `:613` | `manager.RunCommand(ctx, cfg, commandRequest(...))` — the waiting call |
| `pkg/tool/shell_output.go` | `:286-299` | `retrieve_output` — the existing "go get the big thing later" tool, and the registration pattern to copy |
| `pkg/tool/session_tools.go` | `:24-54` | `registerSessionAndMemoryTools` — `llm.NewTool` + `registerToolMeta` + `st.Register` |
| `pkg/run/skills.go` | `:171-210` | `RegisterSkillTool` — `st.GuardTool(ctx, "skill")` at the top of a handler (`:186`) |
| `pkg/architecture/cache_test.go` | `:1461-1486`, `:1532` | `maxProductionFilesPerPackage = 34` |

## The hard constraint: `pkg/tool` has one file slot left

`ls pkg/tool/*.go | grep -v _test` → **33 files**. The cap is **34**
(`cache_test.go:1532`; note the doc comment at `:1461-1462` still says "cap of
30" — it is stale, the constant was raised and the comment was not updated. Do
not fix the comment in this plan; it is out of scope, but mention it in your
completion report so the owner can).

So this plan may add **exactly one** production file to `pkg/tool`:
`process_tool.go`. Everything else goes into files that already exist —
`pkg/safety/process.go`, `pkg/tool/state.go`, `pkg/tool/registry.go`, and, per the
owner's 2026-11-23 ruling ①, **`pkg/safety/manager.go`, `pkg/safety/backend_darwin.go`,
`pkg/safety/backend_linux.go`, `pkg/safety/backend_windows.go`** (the spawn/wait
split), plus **`pkg/run/runner.go`** (kill-all on `Runner.Close`) and
**`pkg/process/open.go`** (one reap-on-start call, ruling ③). Plan 360
is written against the same wall and must not add a `pkg/tool` file either.

If your change needs a second file in `pkg/tool`, **STOP and report**. The
alternatives are merging two existing files, splitting `pkg/tool` into two
packages, or raising the cap — all three are owner decisions, not executor
decisions.

## Design decisions the executor must not re-litigate

**1. One tool with five actions, not five tools.** The tool array renders ahead
of the system prompt and sits in the cached prefix — `pkg/skill/catalog.go:43-47`
spells out why a growing tool array is expensive. Adding `process_start`,
`process_log`, `process_wait`, `process_kill`, `process_list` would add five
entries to every cached prefix in the product. Add one.

**2. The process is sandboxed exactly like a shell command.** Same
`CommandRequest`, same `manager`, same policy. A background server is not a
reason to weaken containment. Pass `AllowLoopback` from plan 357 through the
same plumbing `shell` uses.

**3. `--die-with-parent` is satisfied, not defeated.** bubblewrap kills the
sandboxed child when its bwrap parent exits (`backend_linux.go:81`). That parent
is a child of the **harness process**, so the flag ties the server to the harness
process's death — not to the Go call stack: returning from `StartCommand` exits
no process, and the server keeps running as long as the harness does (review
2026-11-23 correction; the earlier wording claimed the return itself was the
risk). Holding the handle keeps bwrap unreaped and therefore alive. Do not add
`--no-die-with-parent` or any equivalent; the leak this plan fixes is the reason
that flag exists. What the handle does **not** give you is a session boundary —
see decision 5.

**4. Logs spool to disk, never into memory unbounded.** Mirror the shell
convention: `<StateRoot>/state/process/<id>.log`. Feed it from the existing
`OnOutput` callback, whose contract (`sandbox_config.go:177-181`) already says it
is called synchronously from the pipe readers and must return quickly — so write,
do not format.

**5. Killing is the tool's job, the runner's job, and the next launch's job
(owner rulings ②③, 2026-11-23).** A `kill` action is not enough. Three layers,
each bounded:

- **Runner teardown**: kill-all hangs on `Runner.Close`
  (`pkg/run/runner.go:3554`), which a clean TUI quit reaches through
  `Env.Close()` (`pkg/tui/chat_session.go:986` → `pkg/process/open.go:264-266`)
  and pool eviction reaches for an idle runner (`runner_pool.go:24-25`, 24h quiet
  at `:123`). The registry records the owning conversation session ID
  (`tool.ConversationSessionIDFromContext`) so `list` can show it; the tool
  description states that a process lives until it is killed, its runner is
  closed, or the harness exits — **not** "until the session ends". Several
  sessions share one Runner (`runner.go:242`), so a server started in
  conversation A is visible and killable from conversation B. That is the
  accepted tradeoff; do not invent a session-end event the product does not have.
- **Crash backstop**: a SIGKILL/crash runs no teardown, and only Linux's
  `--die-with-parent` (`backend_linux.go:81`) ties the child to the harness's
  death. Step 4b's PID ledger + reap-on-start covers macOS and Windows on the
  next launch.
- Do not invent an unowned goroutine tied to `context.Background()` to do any of
  this.

## Git workflow

- Branch: `advisor/358-process-tool`
- Conventional Commits, lowercase imperative, optional `(scope)`, matching recent
  history (`feat(config): a footer action that submits a form, and the name
  hint`). Use e.g. `feat(tool): a process tool that outlives the turn that started it`.
- Do NOT push and do NOT open a PR unless the operator instructed it.
- Dispatched into an isolated workspace, your branch lives in
  `.worktrees/<short-kebab-name>` under the project's main worktree; never push
  or merge into the user's branch.

## Steps

### Step 0: The teardown hook — searched, and settled by owner ruling ②

Requirement 5 needs a place to hang "kill everything." The review ran the search
on `6541b8ab`:

```bash
grep -rn 'func.*Close()\|func.*Shutdown()\|func.*Teardown' pkg/tool/state.go pkg/run/runner.go pkg/turn/session.go | head -20
grep -rn 'DeleteSessions(' pkg/ --include='*.go' | grep -v _test
```

Result: `tool.State` has **no** `Close`/`Shutdown`; `Runner.Close`
(`pkg/run/runner.go:3554`) exists but closes only the MCP hub/registry and runs at
**runner** discard; `pkg/turn/session.go` has none; and the product has **no
interactive session-end event** — `DeleteSessions`' only caller is the cron
cleanup (`pkg/process/cron_service.go:316`). Several sessions share one Runner
(`runner.go:242`).

**Owner ruling ②: runner-level.** Hang kill-all on `Runner.Close`. Do not build a
session-end event, and do not invent a teardown goroutine tied to
`context.Background()`. Record the hook's `file:line` in the completion report.
The crash window this leaves is closed by Step 4b (ruling ③), not by a new
lifecycle.

### Step 1: A non-waiting spawn path in `pkg/safety`

Read `pkg/safety/manager.go:257-321` (`RunCommand`) and `:597-610`
(`runSandboxCommand`) first, then the backend it dispatches to. **The spawn path
is not in `process.go`** — that file (798 lines) holds the capture, wait and kill
helpers the backends call.

Add, beside `waitForCommandContext` (`:209`):

```go
// ProcessHandle is a command that was started and not waited for.
type ProcessHandle struct { … }

func (h *ProcessHandle) Wait(ctx context.Context) (CommandResult, error)
func (h *ProcessHandle) Kill() error
func (h *ProcessHandle) PID() int
func (h *ProcessHandle) Exited() bool
```

and a manager entry point parallel to `RunCommand`:

```go
func (m *Manager) StartCommand(ctx context.Context, cfg *appcfg.Root, req CommandRequest) (*ProcessHandle, error)
```

`StartCommand` must reuse the **existing** policy resolution, backend selection
and `commandCapture`. **Review 2026-11-23: the "return before the wait" seam this
step originally assumed does not exist.** Each backend does
`cmd.Start()` → `waitForCommandContext` → `capture.result()` in one body
(`backend_darwin.go:39-50`, `backend_linux.go:33-43`, `backend_windows.go:68`,
`runHostCommand` `manager.go:524-546`), and `RunCommand` itself never waits.
`process.go` holds the helpers, not the spawn. So the smallest extraction is:

1. split each backend into a **build-and-start** half returning the `*exec.Cmd`,
   the capture and the backend's own cleanup obligations, and a **wait-and-finalize**
   half (the existing `finalizeExec` at `backend_linux.go:47` is already that shape);
2. have `RunCommand` call both, unchanged in behavior;
3. have `StartCommand` call only the first, and own the cleanup the `defer`s used
   to do.

That touches `manager.go` and all three backend files — **authorized by owner
ruling ① (2026-11-23)**; they are in the file list above. Two lifetime traps the
extraction must carry explicitly, because today they are `defer`s that fire when
the function returns:

- `runCtx, cancel := context.WithTimeout(ctx, req.Timeout); defer cancel()`
  (`manager.go:598-599`, `backend_darwin.go:37-38`): returning early from a
  `StartCommand` that keeps this shape **cancels the process immediately**. The
  handle must own the cancel and the deadline instead.
- `defer os.Remove(profPath)` (`backend_darwin.go:35`) and
  `defer cleanupLinuxMissingDenyMarkers(markers)` (`backend_linux.go:29`): the
  seatbelt profile and the Linux deny markers must outlive the call that started
  the process, and be removed when the handle is reaped.

`Kill` calls the existing `killProcessGroup` — note it is **SIGKILL-only**
(`backend_unix.go:41-47`); see Step 4 for what that means for a grace period.

**This is the riskiest step in the plan.** The split is authorized (ruling ①),
but the bar is unchanged: `RunCommand`'s observable behavior must be identical,
proven by **zero changes to existing `pkg/safety` tests** plus the full suite. If
you cannot keep it identical, **STOP and report** the shape that blocked you — do
not weaken a test to make the extraction pass. A botched refactor here breaks
every sandboxed command in the product.

**Verify**: `go build ./pkg/safety/...` → exit 0;
`CGO_ENABLED=1 go test -tags fts5 ./pkg/safety/... -count=1` → PASS with **zero
changes to existing tests**. If an existing test breaks, your extraction changed
behavior; revert and report.

### Step 2: `pkg/tool/process_tool.go` — the one new file

Input schema (one struct, `Action` selects the branch):

```go
type ProcessInput struct {
	Action     string `json:"action" jsonschema:"enum=start,enum=log,enum=wait,enum=kill,enum=list,description=…"`
	ID         int    `json:"id,omitempty" jsonschema:"description=The id start returned; required by log/wait/kill"`
	Command    string `json:"command,omitempty" jsonschema:"description=start: the command to run"`
	WorkDir    string `json:"work_dir,omitempty"`
	TimeoutMs  int    `json:"timeout_ms,omitempty" jsonschema:"description=start: kill after this long. wait: give up after this long. Default 120000"`
	Wait       string `json:"wait,omitempty" jsonschema:"enum=tcp,enum=http,enum=log,description=start: optionally wait for readiness before returning"`
	Address    string `json:"address,omitempty" jsonschema:"description=wait tcp: host:port. wait http: a URL"`
	ExpectStatus int  `json:"expect_status,omitempty" jsonschema:"description=wait http: the status that means ready. Default 200"`
	Pattern    string `json:"pattern,omitempty" jsonschema:"description=wait log: a regex in the output that means ready"`
	Tail       int    `json:"tail,omitempty" jsonschema:"description=log: last N lines. Default 200"`
}
```

Behavior:

- **`start`** — build the same `CommandRequest` `shell` builds
  (`shell_tool.go:596-609`), with `OutputSpoolDir` pointed at
  `<StateRoot>/state/process/` and `OnOutput` appending to `<id>.log`. Call
  `manager.StartCommand`. Register the handle, return `{"id": N, "pid": P}`. If
  `wait` is also set, run the wait before returning — one call that starts a
  server and returns when it answers is the whole point.
- **`log`** — read the tail of `<id>.log`. Report byte counts the way `shell`
  does. Do not return the whole file; if it is huge, say so and point at
  `retrieve_output`'s pattern.
- **`wait`** — poll until ready or `timeout_ms` expires:
  - `tcp`: `net.DialTimeout("tcp", Address, …)` succeeds.
  - `http`: a GET to `Address` returns `ExpectStatus`. Use `net/http` with a
    short per-attempt timeout; do not shell out to `curl`.
  - `log`: `regexp.MatchString(Pattern, …)` against the spool file, re-read each
    poll.
  Poll at 100ms, back off to 500ms after 2s. Return
  `{"ready": true, "elapsed_ms": E}` or `{"ready": false, …}` plus the last
  log lines — **a failed wait must carry the log tail**, because that is the
  only evidence of why the server did not come up.
  If the process already exited, return `ready: false` with `exited: true` and
  the exit code. Do not keep polling a corpse.
- **`kill`** — `handle.Kill()`, then report exit status. Idempotent: killing an
  already-dead process is a success, not an error.
- **`list`** — every registered process: id, pid, command, running/exited, log
  path, byte count. This is the answer to "what did I leave running."

Handler starts with `st.GuardTool(ctx, "process")`, matching
`pkg/run/skills.go:186`.

Registry: a `processRegistry` type in this same file, attached to `*State`. Read
`pkg/tool/state.go` and use whatever attachment mechanism it already offers
(`SetRuntimeValue` is used at `registry.go:102`, but a typed field is better if
`State` has one for similar per-session objects). Guard it with a mutex; a
`wait` polls while a `kill` may land.

**Verify**: `ls pkg/tool/*.go | grep -v _test | wc -l` → **34** (not 35).
`gofmt -l pkg/tool` → no output.

### Step 3: Register it

`pkg/tool/registry.go`, in `RegisterDefaultTools`, next to the `retrieve_output`
registration (`:145-149`):

```go
registerMeta(tProcess, processToolDescription, "shell", event.ToolMeta{Destructive: true, InterruptBehavior: "interrupt"})
```

`Destructive: true` — `start` and `kill` change the machine. Register it
unconditionally, like `retrieve_output`: a tool array whose shape depends on
project type re-bills the cached prefix.

Write `processToolDescription` as one paragraph in the register of
`shellToolDescription` (`shell_tool.go:83`) — that description already teaches
the model how to read a timeout, and this one must teach it that **`start`
returns immediately and `wait` is how you know it came up**.

**Verify**: `go build ./...` → exit 0.

### Step 4: Teardown on `Runner.Close` (owner ruling ②)

Hang the kill-all on `Runner.Close` (`pkg/run/runner.go:3554`), beside the MCP
hub/registry close it already does. It must:

- kill every still-running process this runner's registry holds, in reverse start
  order;
- not block teardown longer than a bounded grace. **Note what exists**:
  `killProcessGroup` is SIGKILL-only (`backend_unix.go:41-47`), so "2s grace then
  SIGKILL" needs a TERM path that does not exist today — either add
  `syscall.Kill(-pid, syscall.SIGTERM)` to the group, wait up to 2s, then call
  `killProcessGroup`, or drop the grace and SIGKILL immediately and say so in the
  report. Do not imply a grace the code does not implement;
- be safe to call twice (`Runner.Close` already guards with `r.closed`);
- remove each handle's ledger entry (Step 4b) and its seatbelt profile / Linux
  deny markers;
- log what it killed, so an orphaned server is diagnosable after the fact.

**Verify**: a test that starts a `sleep 30`, closes the runner, and asserts the
process is gone within the grace period.

### Step 4b: PID ledger + reap-on-start (owner ruling ③)

A SIGKILL/crash runs no teardown, and only Linux's `--die-with-parent` ties the
child to the harness's death. Close that window on the next launch:

- **Write** `<StateRoot>/state/process/ledger.json` (beside the `<id>.log` spool)
  on every `start`, and remove the entry on kill/exit/teardown. One record per
  live process: `pid`, `pgid`, the OS process **start time**, the command, the
  owning conversation session ID, and the harness's own PID. Write it atomically
  (temp file + rename) — a crash mid-write must not leave a half ledger.
- **Reap at startup**: one call from `pkg/process/open.go` (the composition root
  already imports `pkg/tool`) to an exported `tool.ReapOrphanProcesses(stateRoot)`.
  For each record whose harness PID is not this process: verify the PID still
  exists **and its OS start time matches the record** before killing the process
  group — a recycled PID must never be killed. Then drop the record.
  - Start time source: Linux `/proc/<pid>/stat` field 22 (clock ticks since boot);
    macOS `ps -p <pid> -o lstart=`. Record it in the same form you will compare.
  - **Windows: no reap in this plan.** There is no cheap start-time check here;
    record it as a known limitation in the report and in the tool description's
    lifetime sentence, and file it as a follow-up. Do not kill by PID alone.
- The reap is best-effort and must never block startup on a missing/ malformed
  ledger: log and continue.

**Verify**: a test that writes a ledger record for a live `sleep 30` with a
*different* harness PID, runs the reaper, and asserts the process is gone and the
record dropped; a second test with a **mismatched start time** asserts the process
is **not** killed (the PID-reuse guard); a third with a malformed ledger asserts
startup continues.

### Step 5: Tests

`pkg/tool/process_tool_test.go` and `pkg/safety/process_test.go` additions.
Follow the existing test style in each file — read one test before writing yours.

1. `start` a trivial command (`sh -c 'echo hi'`), `wait` for exit, `log`
   contains `hi`.
2. `start` a command that never opens a port, then `wait` with `wait: tcp`
   against a listener **the test itself opened** (`net.Listen("tcp",
   "127.0.0.1:0")` in the test process): the harness's poll is what dials, so the
   assertion needs no loopback from inside the sandbox and does not depend on
   plan 357. Assert it reports `ready: true` while the process is still running.
3. `wait` on a port nothing listens on → `ready: false` after `timeout_ms`, and
   the result carries the log tail.
4. `wait` on an already-exited process → `exited: true`, returns immediately
   rather than polling to the timeout.
5. `kill` twice → second is a success.
6. `list` reflects start/exit/kill transitions.
7. Teardown kills a running `sleep` (Step 4's test).
8. A `wait` polling concurrently with a `kill` does not race →
   `go test -race ./pkg/tool/ -run TestProcess -count=1` clean.
9. Log spool path is under `<StateRoot>/state/process/` and the file grows.
10. Reap-on-start kills a ledger record whose harness PID differs and whose OS
    start time matches (Step 4b).
11. Reap-on-start does **not** kill when the start time mismatches — the PID-reuse
    guard (Step 4b).
12. A malformed/missing ledger never blocks startup (Step 4b).

**Verify**:
`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/... ./pkg/safety/... ./pkg/run/... ./pkg/process/... -count=1` → PASS.
`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run TestProcess -race -count=1` → PASS.

### Step 6: Full gates

1. `gofmt -l pkg/tool pkg/safety pkg/run pkg/process` → no output
2. `go vet ./pkg/tool/... ./pkg/safety/... ./pkg/run/... ./pkg/process/...` → exit 0
3. `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/... -count=1` → PASS
   (this is the file-count gate; it fails if you added a 35th `pkg/tool` file)
4. `make test` → PASS
5. Manual, on a real session: start the repo's own acceptance driver
   (`scripts/acceptance/driver.sh` — see its usage block at `:145-160`), `wait`
   on it, `log` it, `kill` it, `list` after. Then the crash path: start a
   `sleep 300`, SIGKILL the harness, relaunch, and confirm the reap killed it
   (Linux: confirm `--die-with-parent` already did). Paste both transcripts into
   the completion report.

## Done criteria

- [ ] `ls pkg/tool/*.go | grep -v _test | wc -l` returns exactly **34**
- [ ] `grep -rn "func (m \*Manager) StartCommand" pkg/safety/` finds it (in `manager.go` beside `RunCommand`, unless the authorized design puts it elsewhere), and `RunCommand`'s existing behavior is unchanged (zero existing `pkg/safety` tests modified)
- [ ] `grep -n '"process"' pkg/tool/process_tool.go pkg/tool/registry.go` finds the tool name in both
- [ ] All five actions implemented; `wait` supports `tcp`, `http` and `log`
- [ ] A failed `wait` returns the log tail — `grep -n "tail" pkg/tool/process_tool.go` shows it in the not-ready branch, not only in the `log` action
- [ ] Teardown kills every registered process on `Runner.Close`; the hook's `file:line` is named in the report
- [ ] `<StateRoot>/state/process/ledger.json` is written on start and cleared on kill/exit/teardown; `tool.ReapOrphanProcesses` is called once from `pkg/process/open.go`; the PID-reuse guard is proven by test 11
- [ ] All 12 tests present and passing, including the `-race` one
- [ ] `git status --porcelain` lists only the authorized files: `pkg/tool/process_tool.go` (new), `pkg/tool/state.go`, `pkg/tool/registry.go`, `pkg/safety/{process,manager,backend_darwin,backend_linux,backend_windows}.go`, `pkg/run/runner.go`, `pkg/process/open.go`, and the test files beside them
- [ ] `make test` green, `gofmt -l` clean, `go vet` exit 0, architecture gate PASS
- [ ] The Step 6.5 manual transcript is in the completion report

## STOP and report instead of improvising

- Step 1's extraction cannot keep `RunCommand`'s behavior identical (an existing
  `pkg/safety` test would have to change). Report the shape that blocked you; do
  not weaken the test.
- `Runner.Close` turns out not to be reached on a clean TUI quit (the
  `Env.Close()` path at `pkg/tui/chat_session.go:986` → `pkg/process/open.go:264-266`
  is what this plan relies on). Report the actual exit path; do not add a second
  teardown owner.
- You need a 35th file in `pkg/tool`.
- `OnOutput` turns out to be called from a goroutine that dies when the
  initiating call returns, so a background process's output stops being
  captured. That is a real possibility given its "invoked synchronously from the
  process pipe readers" contract — verify the pipe readers' lifetime belongs to
  the command, not to the caller, before relying on it.
- `--die-with-parent` turns out to kill the server while the harness is still
  running (for example because bwrap's parent is reaped when the starting call
  returns). The flag ties the child to its bwrap parent's death, so this would
  mean the handle is not keeping that parent alive — report the observed
  lifetime, do not remove the flag.

## Maintenance notes

- **The lifetime is runner-level, and that is a settled tradeoff (owner
  2026-11-23).** A server outlives the conversation that started it: several
  sessions share one Runner, and the product has no interactive session-end
  event. `list` shows the owning session so a later conversation can kill it. If
  a real "end session" action is ever added to the surfaces, that is the moment to
  revisit this — not before.
- **The crash window is "until the next launch", not zero.** Reap-on-start covers
  macOS and Windows after a SIGKILL/crash; between the crash and the next launch
  the orphan still holds its port. Linux is covered immediately by
  `--die-with-parent`. **Windows has no reap in this plan** (no cheap start-time
  check) — a named follow-up, not a silent gap.
- **The ledger is a kill list, so its guard is load-bearing.** A recycled PID must
  never be killed: the OS start-time match is the only thing standing between
  reap-on-start and killing an unrelated process. Any future change to the record
  format must keep that comparison exact.

- **Plans 353/354 should be updated after this lands.** Their assets tell the
  model to build a tmux/nohup driver because nothing better existed. Once
  `process` is real, `pkg/skill/system_assets/run/SKILL.md` and
  `run-skill-generator/references/drivers.md` should name it as the first
  choice and keep tmux as the fallback for a UI that needs a real terminal. That
  is a separate, small plan — do not edit those assets here.
- **This tool is the substrate for the runtime-verification skill.** "Drive the
  smallest path that executes the change, then render a verdict" needs a handle
  that survives the turn. Keep `list` cheap and honest: a verification skill
  will call it to prove nothing was left running.
- **Port collisions will be the most common user-visible failure.** Two sessions
  starting the same dev server on a fixed port. Do not add automatic port
  allocation — the project's own config chooses its port — but make sure a
  failed `wait` surfaces `address already in use` from the log tail, which
  requirement 3 and the log-tail-in-failure rule already guarantee.
- **Reviewer scrutiny**: the diff to `pkg/safety/process.go` is the part to read
  line by line. Everything else is additive. A regression there breaks every
  sandboxed command in the product.
