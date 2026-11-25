# Plan 380: A process card's head names the command, its body the reason and the exit code's meaning

> **Supersedes** `process-card-names-command-reason-exit-signal.md` in this
> directory (an earlier draft whose body-carried command the owner rejected).
> This file is the plan to execute.
>
> **Executor instructions**: Follow this plan step by step. Run every verification
> command and confirm the expected result before moving to the next step. If
> anything in the "STOP conditions" section happens, stop and report — do not
> improvise. Do NOT commit: the owner reviews the working tree by hand (see
> §Git workflow). Add this plan's status row to `plans/README.md` when done.
> The owner also asked to see the fixed cards: Step 9 produces the image you hand
> over.
>
> **Drift check (run first)**: `git rev-parse --short HEAD` (planned at
> `7e6aee3b`, branch `feature/vscode-extension`) and
> `git status --porcelain -- pkg/tool pkg/safety pkg/run` (empty at planning
> time; any modification there is another session's or drift — see STOP
> conditions). Compare the excerpts in "Current state" with the live code.

## Status

- **Priority**: P1 (owner-reported, "本期必须一并优化")
- **Effort**: M
- **Risk**: MED (the process tool's result payload, a new argument, and the facts
  the engine puts on a process call's step events)
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `7e6aee3b`, 2026-11-24
- **Issue**: —

## Why this matters

A settled `process action=kill` card today answers the model, not the person:

```
● Killed process #30
  └ Exit code: 137
    Killed: true
    Pid: 16540
    Running: false
```

and a running wait card reads `⣾ Waiting for process #32 1m 18s` — or, on a
process that prints anything, `⣾ Calling process #1 3s`, because each live output
delta redraws the card from facts that name no action at all. The owner asked for
all of this in four messages (2026-11-24):

1. **The head names the command.** `Started` already does; `Waited`/`Killed` must
   stop printing `#N` (`Killed sleep 300`), and the **running** head must too
   (`Waiting for sleep 300 1m 18s`), not only once the call settles.
2. **The step's summary line is deleted** — the head carries it now.
3. **The body says why**: the model's own `reason` argument on kill
   ("this kill request stopped a process that was still running 这句文案不是废话吗？").
4. **The body says what `Exit code: 137` means**: 137 is `128 + 9` — the kernel
   ended the process with SIGKILL; it did not exit on its own.

`wait` cards get the same body treatment in this change. Finally, the owner wants
the fixed cards sent for confirmation (Step 9).

## Current state

### The card body — one reading, both surfaces

- `pkg/tool/format.go:4313-4346` — `readProcessAnswer(input, body)` builds the
  body for both surfaces: it reads the answer as generic `Field: value` rows and
  drops exactly one row the head already names (`command` for `start`, `id` for
  `wait`/`log`/`kill`).

```go
func readProcessAnswer(input map[string]any, body string) (ReadableAnswer, bool) {
	answer, ok := ReadToolAnswer(body)
	if !ok {
		return ReadableAnswer{}, false
	}
	repeated := ""
	switch strings.ToLower(strings.TrimSpace(stringFromAny(input["action"]))) {
	case "start":
		repeated = readableFieldLabel("command")
	case "wait", "log", "kill":
		repeated = readableFieldLabel("id")
	}
	if repeated == "" {
		return answer, true
	}
	rows := make([]ReadableRow, 0, len(answer.Rows))
	for _, row := range answer.Rows {
		// The label is derived from the payload's own key, so renaming the row
		// label cannot leave this filter behind; depth 0 keeps a nested field
		// of the same name.
		if row.Depth == 0 && row.Label == repeated {
			continue
		}
		rows = append(rows, row)
	}
	answer.Rows = rows
	return answer, true
}
```

- `pkg/tool/format.go:4111-4117` — a row is `{Depth, Bullet, Label, Value}`;
  `ReadableAnswer.String()` (`:4122`) paints `Label: Value`.
- `pkg/tool/format.go:4858-4885` — `readableRows` writes **one row per payload
  key**, so a new payload field (`exit_signal`) becomes a visible row unless the
  reading drops it.
- Consumers: `pkg/tui/render.go:3005-3011` (`tool.ReadableToolAnswer("process", content, input)`),
  `pkg/gateway/wsevents.go:975-982` `readableAnswerFor`, `pkg/gateway/api_extra.go:2450`
  for stored rows. The `input` is the call's own arguments, live
  (`pkg/tool/format.go:242-250`) and replayed (`pkg/tui/commands.go:2083`).

### The tool's answer is a JSON *string* inside an envelope

`process` answers with `marshalProcessResult(...)` — a JSON **string**. The runner
wraps a string result as `{"output": "<that string>"}`
(`pkg/run/orchestration_llm.go:2627-2649` `fallbackToolOutput`), so for a process
step `evt.Output == {"output": "{\"id\":1,...}"}` and `evt.Output["command"]` is
always empty. The stored/drawn body is that envelope rendered as text
(`FormatToolStepResult` has no `process` case → `formatGenericToolStep`), and the
reading unwraps it through `toolAnswerCandidates` / `GenericOutputText`
(`pkg/tool/format.go:4360-4370`, `:4641`).

**Consequence**: the signal must be parsed out of that payload text, and any new
*step fact* (the command a wait/kill drives) must ride the event's `Input` — the
one map a card header can read.

### The header — `pkg/tool/header_parts.go`

`DisplayHeaderParts` (`:25`) is the single header builder for both surfaces
(`pkg/tui/tool_header.go:35-45`, `pkg/gateway/wsevents.go:810-840`). The process
case (`:143-230`):

```go
	case lower == "process":
		act := strings.ToLower(strings.TrimSpace(InputString(meta, "action")))
		// id is a JSON number in Input (wait/log/kill require it). ToolMeta has
		// no Output field, so Input is the only source the header can read.
		id := intFromAny(meta.Input["id"])
		proc := "process"
		if id > 0 {
			proc = fmt.Sprintf("process #%d", id)
		}
		switch act {
		case "start":
			... target = InputString(meta, "command") (then meta.Invocation) ...
		case "wait":
			... action = "Waiting for" / "Waited for" ... target = proc
		case "log":
			... action = "Read log of" ... target = proc
		case "kill":
			... action = "Killing" / "Killed" ... target = proc
```

`start` names its command because the model's own arguments hold it. `wait`/`kill`
name none because their arguments hold only the id.

### Every event of a call redraws its card

`pkg/run/orchestration_llm.go:2164-2178` emits the started event with
`Input: stepInput` (the model's arguments). While a `wait` polls, the tool streams
its log through `startProcessLiveTail` (`pkg/tool/process_tool.go:571-609`):

```go
		step(ctx, StepEvent{
			Kind:      StepKindToolOutputDelta,
			StepID:    stepID,
			ToolName:  "process",
			StartedAt: startedAt,
			Input:     map[string]any{"id": rec.id},
			Output: map[string]any{
				"stream": string(stream),
				"chunk":  string(chunk),
			},
		})
```

The surfaces rebuild a running card's facts from each event
(`pkg/tui/tty_session.go:1330`, `pkg/tui/reducer.go:2638`, and the web's
`pkg/gateway/wsevents.go:354`), so **the last event wins** — and this one carries
neither `action` nor `command`. Measured on a real run: a wait on a process that
prints (`while true; do echo tick; sleep 1; done`) shows `Calling process #1 3s`
instead of `Waiting for …`. That is today's defect, and it is why the head's
facts must ride **every** event of the call, not just the first.

`pkg/tool/shell_tool.go:517-519` is the exemplar: the shell tool's own delta
events carry the call's command in `Input`, so its running card keeps naming it.

### The step's summary line

`pkg/tool/format.go:361` `SummarizeToolStep` returns `"ran " + invocation`
(`"running " + invocation` while running). It feeds the stored display summary
(`pkg/run/orchestration_llm.go:2407-2419` `toolDisplayState`), channel
notifications (`pkg/tool/notification.go:110`, `:119`, `:139`) and the web card's
fallback title (`frontend/src/components/chat/ToolCallCard.vue:475-477`). The
owner wants it gone for `process` steps. (`pkg/tui`'s own live-step summary falls
back to `"done"`/`"ok"` when this is empty — `pkg/tui/notify.go:776-779` — that
package is out of scope; note it in the report.)

### Where the payloads are built — `pkg/tool/process_tool.go`

`start` (`:407`), `log` (`:441`) and `list` (`:492`) already carry the command;
`kill` and `wait` do not:

```go
// :452-473
	res, _ := rec.handle.Result()
	return marshalProcessResult(map[string]any{
		"id": rec.id, "pid": rec.handle.PID(), "killed": !alreadyExited,
		"running": !rec.handle.Exited(), "exit_code": res.ExitCode,
	})

// :625-631  (wait, when the process had already exited)
	return marshalProcessResult(map[string]any{
		"id": rec.id, "ready": false, "exited": true, "exit_code": res.ExitCode,
		"tail": logTail(rec.logPath, defaultProcessTail), "elapsed_ms": 0,
	})

// :632-639  (the readiness poll — ready, timeout AND exit all return here)
	readiness, err := processReadiness(ctx, rec, in)
	if err != nil {
		return "", err
	}
	readiness["id"] = rec.id
	return marshalProcessResult(readiness)

// :689-696  (readiness' own exit branch)
	return map[string]any{
		"ready": false, "exited": true, "exit_code": res.ExitCode,
		"elapsed_ms": elapsed.Milliseconds(), "tail": logTail(rec.logPath, defaultProcessTail),
	}, nil
```

`processRecord` holds the command (`:68-77`, field `command`), and the registry
keeps a record after the process exits (`:176-177`, whose comment says a released
record stays so `list` can still say what ran) — so a `wait` on an
exited-but-listed process can name its command too.

```go
// :32-43
type ProcessInput struct {
	Action       string `json:"action" jsonschema_description:"..."`
	ID           int    `json:"id,omitempty" jsonschema_description:"The id start returned; required by log, wait and kill."`
	Command      string `json:"command,omitempty" jsonschema:"description=start: the command to run."`
	WorkDir      string `json:"work_dir,omitempty" jsonschema:"description=start: the directory to run it in. ..."`
	TimeoutMs    int    `json:"timeout_ms,omitempty" jsonschema:"description=..."`
	Wait         string `json:"wait,omitempty" jsonschema:"enum=tcp,enum=http,enum=log,..."`
	Address      string `json:"address,omitempty" jsonschema:"description=wait tcp: host:port. wait http: a URL."`
	ExpectStatus int    `json:"expect_status,omitempty" jsonschema:"description=wait http: the status that means ready. Default 200."`
	Pattern      string `json:"pattern,omitempty" jsonschema:"description=wait log: a regex in the output that means ready."`
	Tail         int    `json:"tail,omitempty" jsonschema:"description=log: last N lines. Default 200."`
}
```

`processToolDescription` (`:44`) is one long string whose kill clause reads
"`kill` ends it,". The schema is generated from the struct by `llm.NewTool`
(invopop/jsonschema); nothing pins the struct or the description except
`pkg/tool/process_tool.go` itself.

### Where 137 comes from

`pkg/safety/sandbox_config.go:196-210` holds `CommandResult` (`ExitCode` at
`:208`). `pkg/safety/process.go:465-472` (`finalizeExec`):

```go
	if exitErr, ok := err.(*exec.ExitError); ok {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			signal := int(status.Signal())
			res.ExitCode = 128 + signal
			return res, &SignalError{Signal: signal}
		}
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
```

The signal is known at exactly this place — and `CommandResult` drops it: only the
returned `*SignalError` carries it, and `ProcessHandle.reap` (`:350-358`) keeps
that error in an unexported `waitErr` while `processKill` reads
`res, _ := rec.handle.Result()` (`:468`). The kill's own signal is SIGKILL
(`pkg/safety/backend_unix.go:42-52`).

Windows never produces `128+n` (`pkg/safety/backend_windows.go:965-969`), and a
plain program can exit 137 by itself, so the explanation must come from a
recorded signal, never from `code >= 128` arithmetic. A cancelled or timed-out
command returns before the signalled branch (`pkg/safety/process.go:450-460`)
with exit code 1 / 124 and no signal — the card keeps the bare number there.

### The fold

`pkg/tui/render.go:626` `const toolOutputMaxLines = 3` — a body over three rows
folds behind `··· N more lines`. The kill body is three rows after this plan
(`Reason` / `Exit code` / `Pid`); a settled `wait` body is four and folds — that
is its normal shape, do not fight it.

### Tests that will move

- `pkg/tool/format_test.go:3828-3874` — `TestReadableToolAnswerDropsTheRowTheHeadNames`.
- `pkg/tool/display_header_test.go:105-164` — the process head test (`waitRunning`
  is already bound at `:121`; pick new names for new frames).
- **Every** `ProcessInput{Action: "kill", …}` in `pkg/tool/process_tool_test.go`:
  `:150`, `:238`, `:248`, `:302`, `:364`, `:392`, `:483`, `:823`. Three of them
  (`:150`, `:392`, `:483`) sit in `defer func() { _, _ = callProcess(...) }()` —
  a required reason without a `Reason:` there fails *silently* and leaves
  `sleep 30` running until the helper's `t.Cleanup(st.KillProcesses)`.
- `pkg/tool/process_tool_test.go`'s three wait tests (`:144-163` ready,
  `:167-194` timeout, `:198-227` already exited).
- `pkg/safety/procstatus_unix_test.go:118-158` — a real SIGKILL on a real
  process; `:148-151` already asserts `res.ExitCode == 128+int(syscall.SIGKILL)`.

### Project conventions that apply

- Comments explain *why*, in the surrounding voice; see
  `pkg/tool/format.go:4314-4318` and `pkg/tool/header_parts.go:143-147`.
- Human-facing wording lives in the reading/header layer, never per surface.
- Value sets are a whitelist plus a generic default (owner rule).
- Never write defensive code for unreachable states.

## Owner rulings (2026-11-24, this task)

1. `kill` body: the model's `reason`, the explained `Exit code`, `Pid`; the
   mechanical `Killed`/`Running` rows go.
2. `wait` cards are fixed in the same change.
3. The reason comes from the model (`reason` argument, required for `kill`). No
   engine-authored reason sentence.
4. The step's **summary line is deleted**; the heads of `Started`/`Waited`/
   `Killed` carry the command — `Started` already does (keep it), `Waited`/
   `Killed` replace `#N` with it, **including while the call still runs**.
5. Send the fixed cards to the owner for confirmation (Step 9).

## Mechanism (one fact, one carrier)

The command a `wait`/`kill` drives rides the **step event's `Input`** — the one
map the header reads — and the header reads it exactly the way `start` already
does (`InputString(meta, "command")`).

| When | What carries the command |
|---|---|
| dispatch (the started event) | `pkg/run` resolves the id against `tool.State` and adds `command` to a **copy** of the event's input |
| every live output delta | the process tool puts `action` + `command` on its own delta event's `Input` (today it puts only `id`, which is why a noisy wait degrades to `Calling process #1`) |
| the completion | the same copy — a settled card's facts are the last event's, and the head reads them the same way |
| the settled card's body | the result payload, parsed out of the doubled envelope in `Content` (the body no longer draws a `Command` row: the head names it) |

The model's own arguments are never touched: the resolution rides a copy used
only for the card events, and the tool result the model sees is unchanged.

No new persisted field, no `pkg/event`/`pkg/tui`/`pkg/gateway` change: the facts
travel on `ToolMeta.Input`, which every surface already mirrors and persists.

## Behaviour change matrix

| Scenario | Before | After | Note |
|---|---|---|---|
| `wait` **running**, quiet process | `Waiting for process #32 1m 18s` | `Waiting for sleep 300 1m 18s` | owner ruling 4 |
| `wait` **running**, process that prints | `Calling process #1 3s` (the delta dropped the action) | `Waiting for while true; do echo tick; sleep 1; done 3s` | today's defect, fixed here |
| `start` **running**, process that prints | `Calling process #1 3s` (the start emitter never carried the action) | `Starting background process while true; do echo tick; sleep 1; done 3s` | same defect, same fix |
| `wait` settled (ready / timeout / exited) | `Waited for process #3`; body `Address`/`Ready`/… | `Waited for npm run dev`; body drops `Command`/`Id`, keeps the rest, explains a reported `exit_code` | |
| `kill` settled (running process) | `Killed process #30`; body `Exit code: 137` / `Killed: true` / `Pid` / `Running: false`; summary `ran process {…}` | `Killed while true; do date; sleep 1; done`; body `Reason: …` / `Exit code: 137 — terminated by signal 9 (SIGKILL)` / `Pid: 16540`; no summary line | |
| `kill` of an already-exited process | `Killed: false` / `Running: false` / bare code | head names the command; `Reason` / `Exit code: 0 — exited cleanly` / `Pid` | |
| `kill` without a `reason` | succeeded | tool error `reason is required for kill: one line saying why the process is being stopped` | |
| an id this state does not hold (another runner's process) | `#N` | `#N` — no guessing | |
| a failed `wait`/`kill` | `Failed to kill process #30` | `Failed to kill sleep 300` when the dispatch resolution found it, else unchanged | |
| `log` card | head `Read log of process #3`, body keeps `Command` | unchanged | owner named only Started/Waited/Killed |
| `start` card | head `Started/Starting background process <cmd>`, body drops `command` | unchanged | |
| `list` card | one row per process | unchanged | |
| the step's summary line | `ran process {"action":"kill","id":30}` | none (`SummarizeToolStep` → `""` for `process`) | owner ruling 4; `pkg/tui`'s live fallback may show `done`/`ok` — out of scope, noted |
| `exit_signal` as a field | n/a | never a row | |
| unknown signal (e.g. 138 / signal 10) | — | `138 — terminated by signal 10`, no name | a wrong name is worse than none |
| Old rows replayed | body as stored | body re-read: `Killed`/`Running`/`Command` rows dropped, `Exit code: 0` reads `0 — exited cleanly`; no signal sentence (their payload has none); head stays `process #N` (their input has no command) | harmless |
| Web surface | same as TUI | same as TUI | same reading + header |

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| Focused tool tests | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"; FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'Process|ReadableToolAnswer|Summarize|DisplayParts' -count=1` | `ok …/pkg/tool` |
| Focused safety tests | `export PKG_CONFIG_PATH=…; FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/safety -run 'Kill|Signal|PipeHolder' -count=1` | `ok …/pkg/safety`, not `[no tests to run]` |
| Package regression | `export PKG_CONFIG_PATH=…; FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/safety ./pkg/run ./pkg/tui ./pkg/gateway -count=1` | all `ok` |
| Format | `gofmt -l cmd pkg third_party plugins` | prints nothing |
| Vet | `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0 |

Two traps in the `-run` patterns: write them as plain `Kill|Signal|PipeHolder`
inside single quotes (a backslashed `\|` matches a literal pipe, Go then runs
**no** test and still exits 0). `PKG_CONFIG_PATH` is needed whenever a direct
`go` command compiles `pkg/pdf`; keep it exported (`FOREBRAIN.md`). A Windows
cross-build is not a gate: `GOOS=windows CGO_ENABLED=0 go build ./pkg/tool` fails
inside go-pdfium's cgo layer for reasons unrelated to this change.

Known flakes, not yours (each passes alone; re-run before reporting):
`TestProcessLogSpoolGrowsUnderTheStateRoot` and
`TestReapOrphanProcessesKillsAMatchingOrphan` in `pkg/tool`.

## Scope

**In scope** (the only files you may modify):
- `pkg/safety/sandbox_config.go` (`CommandResult`)
- `pkg/safety/process.go` (`finalizeExec`)
- `pkg/tool/process_tool.go`
- `pkg/tool/format.go`
- `pkg/tool/header_parts.go`
- `pkg/run/orchestration_llm.go` (the started event's resolved command)
- `pkg/tool/format_test.go`, `pkg/tool/process_tool_test.go`,
  `pkg/tool/display_header_test.go`
- `pkg/safety/procstatus_unix_test.go`
- `plans/380-process-card-head-names-command.md` (this plan) and one appended
  status row in `plans/README.md`

**Out of scope** (do NOT touch, even though they look related):
- `pkg/event/*`, `pkg/tui/*`, `pkg/gateway/*`, `frontend/*`, `ai-elements-vue/*` —
  the facts ride `ToolMeta.Input`, which all of them already carry, and another
  agent is editing `pkg/tui` right now (project-type UI). A `ToolMeta` field would
  need all of them: the rejected route. If the head change turns out to need one
  of these packages, that is a STOP condition.
- `.worktrees/plan-review-*/` — reviewer sandboxes holding implementations and
  probe tests of earlier drafts. Do not copy or merge them; implement in the main
  tree.
- The `log` card's head and body; the `list` card's per-process `exit_code` rows.
- `start`/`log` cards that report `Running: false` but no exit code at all: the
  capability exists after this change, but reporting an exit code there is a
  payload change the owner did not ask for. Deferred.
- `pkg/tui`'s live-step summary fallback (`done`/`ok`): deferred with the
  package.
- Chinese body wording: bodies are English by today's convention
  (`readableFieldLabel`); the owner ruled on content, not language.

## Git workflow

- Work directly in this tree on `feature/vscode-extension`, **uncommitted**. The
  owner reviews the working tree and commits by hand; never commit, push, stash
  or revert, and never touch another agent's uncommitted files.
- `plans/` is tracked here (`.gitignore:77` has it *commented out*) and
  `plans/README.md` is already modified by other sessions: append this plan's row
  only, never reflow or revert the file.

## Steps

### Step 0: Reproduce on the real binary (before any source change)

The owner's screenshots are the bug; establish it yourself and prove the recipe
Step 8 uses — including the noisy variant, which is where today's head degrades.

```bash
D=.claude/skills/run-forebrain/driver.sh
export FOREBRAIN_RUN_WORK=$TMPDIR/forebrain-run-killcard FOREBRAIN_RUN_SESSION=fb-killcard FOREBRAIN_RUN_PORT=8765
$D reset; $D build
$D start tool '[{"name":"process","arguments":{"action":"start","command":"while true; do echo tick; sleep 1; done"}},{"name":"process","arguments":{"action":"wait","id":1,"wait":"log","pattern":"never-matches","timeout_ms":20000}},{"name":"process","arguments":{"action":"kill","id":1}}]'
$D submit 'start a noisy loop, wait for it, then kill it'
$D wait 'Tool approval' 30; $D key Enter     # the start call's approval
$D wait 'Tool approval' 30; $D key Enter     # the wait call's approval
sleep 3; $D screen | sed -n '/process #1/,+2p'   # today: "Calling process #1 3s"
$D wait 'Tool approval' 30; $D key Enter     # the kill call's approval
sleep 2; $D screen | sed -n '/Killed process/,+6p'
```

Expected (the red run): the running head says `Calling process #1 …` (the live
tail overwrote the action) and the kill card is `Killed process #1` with
`Exit code: 137` / `Killed: true` / `Pid` / `Running: false` — no command, no
reason, no explanation. Keep the captures as the "before" evidence.

Notes that make this recipe work (verify them here, once):
- The driver's provider is `.claude/skills/run-forebrain/fake_provider.py`; its
  `tool` mode takes the answer text as a JSON object **or array** of
  `{"name","arguments"}` and hands out one call per request, then answers `done`.
  (`scripts/acceptance/fake_provider.py` is a different provider with a
  `FAKE_TOOL_SCRIPT` env var — do not mix them up.)
- The driver's home is created with `approval_policy: on-request` and `process` is
  destructive: **each call raises a `Tool approval` overlay** and hangs until
  answered. Wait for the overlay, then Enter — never press Enter blind (that
  submits an empty turn).
- The id is `1` in a fresh home (`$D reset`). `$D provider-log` must show the
  expected request sequence; a background pass eating a script entry is the one
  known flakiness — re-run rather than changing product code.
- `$D stop` when done, then `pgrep -f 'echo tick'` (expect nothing).

### Step 1: Record the signal on `CommandResult`

`pkg/safety/sandbox_config.go:196-210` — add the field **at the end of the
struct**, so the aligned block above keeps its formatting and `gofmt` stays clean:

```go
	ExitCode                int
	ExecutionTiming         llm.ExecutionTiming
	// ExitSignal is the signal that ended the command, 0 when the command
	// exited by itself. ExitCode is 128+ExitSignal for such a death, and only
	// this field says which signal it was: the number alone is also a plain
	// exit code on a platform with no signals, or one a program chose.
	ExitSignal int
```

`pkg/safety/process.go:465-472` — set it beside the code:

```go
			signal := int(status.Signal())
			res.ExitSignal = signal
			res.ExitCode = 128 + signal
```

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/safety` → exit 0;
`gofmt -l pkg/safety` → prints nothing;
`grep -n 'ExitSignal' pkg/safety/sandbox_config.go pkg/safety/process.go` → one
declaration, one assignment.

### Step 2: `process` reports the command and the signal; kill needs a reason

`pkg/tool/process_tool.go`:

1. `ProcessInput` (`:32-43`) — add after `ID`:

```go
	Reason       string `json:"reason,omitempty" jsonschema:"description=kill: one line saying why the process is being stopped."`
```

2. `processToolDescription` (`:44`) — change the kill clause from
   "`kill` ends it, `list` shows what is still running" to
   "`kill` ends it and takes a one-line `reason` saying why (the card shows it),
   `list` shows what is still running".

3. `processKill` (`:452-473`) — require the reason and report the command and the
   signal:

```go
	if strings.TrimSpace(in.Reason) == "" {
		return "", fmt.Errorf("reason is required for kill: one line saying why the process is being stopped")
	}
	...
	payload := map[string]any{
		"id": rec.id, "command": rec.command, "pid": rec.handle.PID(),
		"killed": !alreadyExited, "running": !rec.handle.Exited(), "exit_code": res.ExitCode,
	}
	if res.ExitSignal > 0 {
		payload["exit_signal"] = res.ExitSignal
	}
	return marshalProcessResult(payload)
```

4. `processWait` — the already-exited branch (`:625-631`) and the merge after
   `processReadiness` (`:638-639`) carry the command; the branches reporting an
   exit code also carry the signal:

```go
	if rec.handle.Exited() {
		res, _ := rec.handle.Result()
		payload := map[string]any{
			"id": rec.id, "command": rec.command, "ready": false, "exited": true,
			"exit_code": res.ExitCode, "tail": logTail(rec.logPath, defaultProcessTail), "elapsed_ms": 0,
		}
		if res.ExitSignal > 0 {
			payload["exit_signal"] = res.ExitSignal
		}
		return marshalProcessResult(payload)
	}
	...
	readiness["id"] = rec.id
	readiness["command"] = rec.command
	return marshalProcessResult(readiness)
```

   and in `processReadiness`'s exit branch (`:689-696`) add the signal beside the
   code (the command arrives from the merge above):

```go
			result := map[string]any{
				"ready": false, "exited": true, "exit_code": res.ExitCode,
				"elapsed_ms": elapsed.Milliseconds(), "tail": logTail(rec.logPath, defaultProcessTail),
			}
			if res.ExitSignal > 0 {
				result["exit_signal"] = res.ExitSignal
			}
			return result, nil
```

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/tool` → exit 0;
`grep -n '"command": rec.command' pkg/tool/process_tool.go` → 4 matches (log,
list, kill, wait-exited); `grep -n 'readiness\["command"\]' pkg/tool/process_tool.go`
→ 1.

### Step 3: Every event of a process call carries the facts its card names

The tool has **two** delta emitters, and both must carry the action (the command
already rides both):

1. `processStart`'s emitter (`pkg/tool/process_tool.go:341-363`) sends
   `Input: {"command": command, "id": id}` — **no action**, so a `start` that
   streams its output degrades from `Starting background process <cmd>` to
   `Calling process #1` while it runs:

```go
				Input: map[string]any{
					"action":  "start",
					"command": command,
					"id":      id,
				},
```

2. `startProcessLiveTail` (`:571-609`) sends `Input: {"id": rec.id}` — neither
   action nor command, which is why a noisy `wait` shows `Calling process #1`. It
   is called from `processWait` only (`:632`), so pass the action in:

```go
func startProcessLiveTail(ctx context.Context, st *State, rec *processRecord, action string) func() {
```

   and inside the emitter:

```go
			step(ctx, StepEvent{
				Kind:      StepKindToolOutputDelta,
				StepID:    stepID,
				ToolName:  "process",
				StartedAt: startedAt,
				// The surfaces redraw a running card from each event's own
				// facts, so a delta that carried only the id left the head
				// saying "Calling process #1" over a wait (owner 2026-11-24).
				Input: map[string]any{"id": rec.id, "action": action, "command": rec.command},
				Output: map[string]any{
					"stream": string(stream),
					"chunk":  string(chunk),
				},
			})
```

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/tool` → exit 0;
`grep -n '"action":  "start"\|"action": action' pkg/tool/process_tool.go` → one
match each; `grep -n 'startProcessLiveTail' pkg/tool/process_tool.go` → definition
+ the call passing `in.Action`.

### Step 4: The engine resolves the command a starting wait/kill drives

`pkg/tool/process_tool.go` — a lookup on the state that owns the registry. An
exited-but-listed process still resolves (a `wait` may legitimately land on one):

```go
// ProcessStartedCommand is the command a process call acts on before it has a
// result: the id a wait or kill drives, resolved in this state's registry. "" for
// every other tool and action, and for an id this state does not hold — a process
// another runner started stays a number rather than becoming a guess. A record is
// kept after its process exits (so `list` can still say what ran), and a wait on
// such a process names its command too.
func (s *State) ProcessStartedCommand(toolName string, input map[string]any) string {
	if s == nil || !strings.EqualFold(strings.TrimSpace(toolName), "process") {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(stringFromAny(input["action"]))) {
	case "wait", "kill":
	default:
		return ""
	}
	rec, ok := s.processRegistryFor().get(intFromAny(input["id"]))
	if !ok {
		return ""
	}
	return rec.command
}
```

`pkg/run/orchestration_llm.go:2164-2178` — every event of the call carries it, on
a **copy** of the input (the call's arguments stay what the model sent):

```go
	startedAt := time.Now()
	// A process wait or kill names its process by id, so its card can only name
	// the command if the dispatch resolution rides the call's events — on a copy,
	// because the arguments are what the model sent. The started event, every
	// output delta (the tool's own, below) and the completion all carry it, so the
	// head names the same command from the first frame to the last.
	cardInput := stepInput
	if cmd := w.state.ProcessStartedCommand(name, stepInput); cmd != "" {
		cardInput = make(map[string]any, len(stepInput)+1)
		for key, value := range stepInput {
			cardInput[key] = value
		}
		cardInput["command"] = cmd
	}
	if step := tool.StepHookFromContext(ctx, w.state); step != nil {
		step(ctx, tool.StepEvent{
			Kind:            tool.StepKindToolStarted,
			...
			Input:           cardInput,
```

   The completion event (`:2193-2212`, `Input: stepInput`) uses `cardInput` too —
   otherwise a settled card's facts (the last event's) would lose the command
   again. The **attempt** events (`:2220-2232`) copy `completedEvent` and keep it.

```go
	completedEvent := tool.StepEvent{
		...
		Input:           cardInput,
```

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/run` → exit 0;
`grep -n 'ProcessStartedCommand' pkg/tool/process_tool.go pkg/run/orchestration_llm.go`
→ declaration + one call; `grep -n 'cardInput' pkg/run/orchestration_llm.go` → the
resolution and both events' `Input`.

### Step 5: Delete the summary line for `process` steps

`pkg/tool/format.go`, `SummarizeToolStep` (`:361`) — after the `evt.Error` /
`evt.Denied` early returns, before the generic wording:

```go
	// A process step carries no summary line of its own: its head already names
	// the command it started, waited on or killed (owner 2026-11-24), and a line
	// saying "ran <command>" beside that head repeats it.
	if strings.EqualFold(toolName, "process") {
		return ""
	}
```

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/tool` → exit 0;
`grep -n 'strings.EqualFold(toolName, "process")' pkg/tool/format.go` → 1.

### Step 6: The head names the command for `wait` and `kill`

`pkg/tool/header_parts.go`, process case (`:143-230`):

- Update the case comment (`:143-147`) — the head names the command a start runs,
  the command a wait/kill drives, the process id a log drives, the registry a list
  reads — and replace the stale "ToolMeta has no Output field, so Input is the
  only source" sentence (`:150-151`) with the fact: the tool and the engine put
  `action` and `command` on every event of a wait/kill, so the head can name them
  while the call runs.
- `wait` (`:177-185`) and `kill` (`:199-207`): replace `target = proc` with

```go
			target = proc
			if cmd := InputString(meta, "command"); cmd != "" {
				// The command this call drives: the engine resolves the id when
				// it dispatches, and every live delta repeats the facts, so a
				// running card names its command too. A call this state cannot
				// resolve keeps its id rather than guessing.
				target = cmd
			}
```

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/tool` → exit 0, and the head
test from Step 7 goes from red to green.

### Step 7: The body says why and what the exit code means

`pkg/tool/format.go`, replace `readProcessAnswer` (`:4313-4346`) — the same
"drop the row the head names" rule, plus the reason, the explained exit code, and
`exit_signal` kept off the card:

```go
func readProcessAnswer(input map[string]any, body string) (ReadableAnswer, bool) {
	answer, ok := ReadToolAnswer(body)
	if !ok {
		return ReadableAnswer{}, false
	}
	action := strings.ToLower(strings.TrimSpace(stringFromAny(input["action"])))
	repeated := ""
	switch action {
	case "start", "wait", "kill":
		// The head names the command for all three: a start's from its own
		// arguments, a wait's or kill's from the facts the engine put on the
		// call's events (header_parts.go).
		repeated = readableFieldLabel("command")
	case "log":
		repeated = readableFieldLabel("id")
	}
	// exit_signal is never a row of its own: the exit code's sentence already
	// names the signal that produced it.
	dropped := map[string]bool{readableFieldLabel("exit_signal"): true}
	if repeated != "" {
		dropped[repeated] = true
	}
	if action == "wait" || action == "kill" {
		// The id is the model's handle on the process, not the reader's.
		dropped[readableFieldLabel("id")] = true
	}
	if action == "kill" {
		// A kill card states the outcome once, in the exit code's own words:
		// "Killed: true / Running: false" beside that sentence only repeats it.
		dropped[readableFieldLabel("killed")] = true
		dropped[readableFieldLabel("running")] = true
	}
	signal := processExitSignal(body)
	exitLabel := readableFieldLabel("exit_code")
	rows := make([]ReadableRow, 0, len(answer.Rows)+1)
	for _, row := range answer.Rows {
		// The label is derived from the payload's own key, so renaming the row
		// label cannot leave this filter behind; depth 0 keeps a nested field of
		// the same name.
		if row.Depth == 0 && dropped[row.Label] {
			continue
		}
		if row.Depth == 0 && row.Label == exitLabel {
			row.Value = explainProcessExitCode(row.Value, signal)
		}
		rows = append(rows, row)
	}
	// The reason the model gave leads a kill card: its head names the command, so
	// this is the first line about it. A failed call carries no rows (its body is
	// the error), which is why the row list gates it.
	if action == "kill" && len(rows) > 0 {
		if reason := strings.TrimSpace(stringFromAny(input["reason"])); reason != "" {
			rows = append([]ReadableRow{{Label: readableFieldLabel("reason"), Value: reason}}, rows...)
		}
	}
	answer.Rows = rows
	return answer, true
}
```

Four helpers beside it:

```go
// processFacts are the two facts a process card needs from the tool's own answer:
// the command the call was about and the signal that ended the process.
type processFacts struct {
	Command    string `json:"command"`
	ExitSignal int    `json:"exit_signal"`
}

// processAnswerFacts reads a process payload. The tool answers with the payload
// as JSON *text*, and the runner wraps a text result once more as
// {"output": "<that text>"} — so both shapes are read, and nothing else is
// guessed at.
func processAnswerFacts(text string) (processFacts, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return processFacts{}, false
	}
	var envelope struct {
		Output string `json:"output"`
	}
	if json.Unmarshal([]byte(trimmed), &envelope) == nil && strings.HasPrefix(strings.TrimSpace(envelope.Output), "{") {
		trimmed = strings.TrimSpace(envelope.Output)
	}
	var facts processFacts
	if json.Unmarshal([]byte(trimmed), &facts) != nil {
		return processFacts{}, false
	}
	return facts, true
}

// processContentFacts reads the facts out of a card's body, which is the generic
// formatter's envelope on every path (toolAnswerCandidates also yields the raw
// answer, for a row that stored it unwrapped).
func processContentFacts(content string) (processFacts, bool) {
	for _, candidate := range toolAnswerCandidates(content) {
		if facts, ok := processAnswerFacts(candidate); ok {
			return facts, true
		}
	}
	return processFacts{}, false
}

// processExitSignal is the signal the answer recorded as having ended the
// command, 0 when it recorded none. It is never computed from the exit code: 137
// is 128+9 only on a platform that has signals, and a program may exit 137 by
// itself.
func processExitSignal(body string) int {
	facts, _ := processContentFacts(body)
	return facts.ExitSignal
}

// processSignalNames names a signal death for a reader. A number outside this
// table keeps the bare number: the same number is another signal (or no signal at
// all) on another platform, and a wrong name is worse than none. Every entry here
// carries the same number on Linux and macOS; the ones that do not (SIGBUS,
// SIGUSR1/2, SIGCHLD, …) are left out on purpose.
var processSignalNames = map[int]string{
	1: "SIGHUP", 2: "SIGINT", 3: "SIGQUIT", 4: "SIGILL", 5: "SIGTRAP",
	6: "SIGABRT", 8: "SIGFPE", 9: "SIGKILL", 11: "SIGSEGV", 13: "SIGPIPE",
	14: "SIGALRM", 15: "SIGTERM",
}

// explainProcessExitCode says what an exit code means to a person: a signal death
// names the signal that ended the process, a 0 says the command exited by itself,
// and anything else keeps the plain number the command chose.
func explainProcessExitCode(value string, signal int) string {
	code := strings.TrimSpace(value)
	if code == "" {
		return value
	}
	if signal > 0 {
		if name, ok := processSignalNames[signal]; ok {
			return fmt.Sprintf("%s — terminated by signal %d (%s)", code, signal, name)
		}
		return fmt.Sprintf("%s — terminated by signal %d", code, signal)
	}
	if code == "0" {
		return "0 — exited cleanly"
	}
	return value
}
```

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/tool` → exit 0;
`gofmt -l pkg/tool` → prints nothing.

### Step 8: Pin it with tests

Write the new assertions and record the red run before Steps 1-7 are complete (or
temporarily revert your own in-scope hunks — never another agent's work).

1. `pkg/tool/display_header_test.go` — edit
   `TestDisplayPartsNamesTheProcessCommandAndID` (`:105-164`) in place: give the
   existing `killDone` frame (`:128-133`) a `command` in its input and expect it
   in the target, then add the frames below (`waitRunning` is already bound at
   `:121` — use the new names):

```go
	// A settled kill names the command its facts carry, and so does a wait while
	// it runs: the engine resolves the id at dispatch and every delta repeats it.
	killSettled := DisplayFrame{Title: "process", Final: true, Meta: ToolMeta{
		Status: "completed",
		Input:  map[string]any{"action": "kill", "id": float64(7), "reason": "x", "command": "while true; do date; sleep 1; done"},
	}}
	action, target, _ = DisplayParts(killSettled, t.TempDir())
	require.Equal(t, "Killed", action)
	require.Equal(t, "while true; do date; sleep 1; done", target)

	waitStreaming := DisplayFrame{Title: "process", Meta: ToolMeta{
		Status: "running",
		Input:  map[string]any{"action": "wait", "id": float64(7), "command": "while true; do echo tick; sleep 1; done"},
	}}
	action, target, _ = DisplayParts(waitStreaming, t.TempDir())
	require.Equal(t, "Waiting for", action)
	require.Equal(t, "while true; do echo tick; sleep 1; done", target)

	// A call this state cannot resolve keeps its id.
	killUnresolved := DisplayFrame{Title: "process", Final: true, Meta: ToolMeta{
		Status: "completed", Input: map[string]any{"action": "kill", "id": float64(7), "reason": "x"},
	}}
	action, target, _ = DisplayParts(killUnresolved, t.TempDir())
	require.Equal(t, "Killed", action)
	require.Equal(t, "process #7", target)
```

   The existing `waitRunning` case (`:121-126`), the `log` case (`:151-156`) and
   the unknown-action case keep `process #7`.

2. `pkg/tool/format_test.go` — replace the kill block (`:3857-3861`) and add (a
   fresh variable name for the envelope — the function already binds `enveloped`
   at `:3842`):

```go
	// A kill card drops what its head now names (the command, the id) and the
	// mechanical booleans, and explains the signal-killed exit code; the reason
	// the model gave leads.
	killed := `{"command":"sleep 300","exit_code":137,"exit_signal":9,"id":3,"killed":true,"pid":42,"running":false}`
	killInput := map[string]any{"action": "kill", "id": 3, "reason": "releasing port 8080"}
	answer, ok = ReadableToolAnswer("process", killed, killInput)
	require.True(t, ok)
	body = answer.String()
	require.Equal(t, "Reason: releasing port 8080", strings.SplitN(body, "\n", 2)[0])
	require.Contains(t, body, "Exit code: 137 — terminated by signal 9 (SIGKILL)")
	require.Contains(t, body, "Pid: 42")
	require.NotContains(t, body, "Command:")
	require.NotContains(t, body, "Id: 3")
	require.NotContains(t, body, "Killed: true")
	require.NotContains(t, body, "Running: false")
	require.NotContains(t, body, "Exit signal")

	// The shape a stored step really has: the tool answers with JSON text and the
	// generic formatter wraps it once more. It reads the same.
	storedEvent := StepEvent{Kind: StepKindToolCompleted, ToolName: "process", Input: killInput,
		Output: map[string]any{"output": killed}}
	storedBody, _ := FormatToolStepResult(storedEvent, DefaultMaxFormattedBody)
	require.Contains(t, storedBody, "output:")
	answer, ok = ReadableToolAnswer("process", storedBody, killInput)
	require.True(t, ok)
	require.Contains(t, answer.String(), "Exit code: 137 — terminated by signal 9 (SIGKILL)")

	// An already-exited process: no signal, so the code says it exited itself.
	already := `{"command":"sleep 300","exit_code":0,"id":3,"killed":false,"pid":42,"running":false}`
	answer, ok = ReadableToolAnswer("process", already, map[string]any{"action": "kill", "id": 3, "reason": "tidying up"})
	require.True(t, ok)
	require.Contains(t, answer.String(), "Exit code: 0 — exited cleanly")

	// A signal the table does not name keeps the number.
	unnamed := `{"command":"sleep 300","exit_code":138,"exit_signal":10,"id":3,"killed":true,"pid":42,"running":false}`
	answer, ok = ReadableToolAnswer("process", unnamed, map[string]any{"action": "kill", "id": 3, "reason": "x"})
	require.True(t, ok)
	require.Contains(t, answer.String(), "Exit code: 138 — terminated by signal 10")
	require.NotContains(t, answer.String(), "SIGUSR1")

	// A wait card drops the command and the id too — its head names the command.
	waited := `{"address":"127.0.0.1:8080","command":"npm run dev","elapsed_ms":12,"id":3,"ready":true}`
	answer, ok = ReadableToolAnswer("process", waited, map[string]any{"action": "wait", "id": 3})
	require.True(t, ok)
	require.Contains(t, answer.String(), "Address: 127.0.0.1:8080")
	require.NotContains(t, answer.String(), "Command:")
	require.NotContains(t, answer.String(), "Id: 3")

	// A process step carries no summary line: its head says what it did.
	require.Equal(t, "", SummarizeToolStep(StepEvent{Kind: StepKindToolCompleted, ToolName: "process",
		Input: map[string]any{"action": "kill", "id": 3}, Output: map[string]any{"output": killed}}))
```

3. `pkg/tool/process_tool_test.go`:
   - add a `Reason:` to all eight kill calls (`:"test cleanup"`), the three
     `defer` ones included;
   - `TestProcessKillIsIdempotent` (`:230-255`), after the first kill:

```go
	if got, _ := first["command"].(string); got != "sleep 30" {
		t.Fatalf("a kill card does not name the command it ended: %+v", first)
	}
	if runtime.GOOS != "windows" {
		if got, _ := first["exit_signal"].(float64); int(got) != 9 {
			t.Fatalf("a kill does not record the signal that ended the process: %+v", first)
		}
	}
```

   - a new test beside it:

```go
// A kill without a reason is refused: the card has to be able to say why.
func TestProcessKillRequiresAReason(t *testing.T) {
	_, tl, _, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	started, err := callProcess(t, tl, ctx, ProcessInput{Action: "start", Command: "sleep 30", WorkDir: projectDir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := processTestID(t, started)
	if _, err := callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: id}); err == nil || !strings.Contains(err.Error(), "reason is required for kill") {
		t.Fatalf("a kill without a reason reported %v, want the reason error", err)
	}
	if _, err := callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: id, Reason: "test cleanup"}); err != nil {
		t.Fatalf("a kill with a reason failed: %v", err)
	}
}
```

   - the three wait tests assert the command their payload reports:
     `waited["command"]` is `sleep 30` in the ready test (`:151-159`),
     `echo no-server-here; sleep 30` in the timeout test (`:167-194`) and
     `echo done` in the already-exited test (`:198-227`) — the ready and timeout
     results come from the merge at `:638-639`, the path that used to lose it;
   - a test for the dispatch resolution (a killed record still resolves — a `wait`
     may land on an exited process):

```go
// A wait or kill card names the command before any result exists: the engine
// resolves the id against this state's registry when it dispatches the call.
func TestProcessStartedCommandResolvesTheID(t *testing.T) {
	st, tl, _, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	started, err := callProcess(t, tl, ctx, ProcessInput{Action: "start", Command: "sleep 30", WorkDir: projectDir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := processTestID(t, started)
	for _, action := range []string{"wait", "kill"} {
		if got := st.ProcessStartedCommand("process", map[string]any{"action": action, "id": float64(id)}); got != "sleep 30" {
			t.Fatalf("%s resolved to %q, want the command", action, got)
		}
	}
	if _, err := callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: id, Reason: "test cleanup"}); err != nil {
		t.Fatalf("kill: %v", err)
	}
	// The record outlives the process (list can still say what ran), so a wait on
	// an exited one still names it.
	if got := st.ProcessStartedCommand("process", map[string]any{"action": "wait", "id": float64(id)}); got != "sleep 30" {
		t.Fatalf("an exited process resolved to %q, want its command", got)
	}
	if got := st.ProcessStartedCommand("process", map[string]any{"action": "start", "command": "x"}); got != "" {
		t.Fatalf("start resolved to %q, want nothing (its command is its own argument)", got)
	}
	if got := st.ProcessStartedCommand("shell", map[string]any{"action": "wait", "id": float64(id)}); got != "" {
		t.Fatalf("another tool resolved to %q", got)
	}
	if got := st.ProcessStartedCommand("process", map[string]any{"action": "wait", "id": float64(999)}); got != "" {
		t.Fatalf("an unknown id resolved to %q", got)
	}
}
```

   - a test that a `wait`'s live deltas carry the facts the head needs. **Attach the
  hook to the wait call only** — the `start` call has its own delta emitter, and
  its deltas would otherwise be captured too (`action: "start"`):

```go
// A wait that streams output redraws its card from each delta's own facts: a
// delta carrying only the id left the head saying "Calling process #1".
func TestProcessWaitDeltasCarryTheActionAndCommand(t *testing.T) {
	_, tl, _, projectDir := newProcessToolForTest(t)
	command := "while true; do echo tick; sleep 0.2; done"
	plainCtx := processTestContext(projectDir)
	started, err := callProcess(t, tl, plainCtx, ProcessInput{Action: "start", Command: command, WorkDir: projectDir})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := processTestID(t, started)
	defer func() {
		_, _ = callProcess(t, tl, plainCtx, ProcessInput{Action: "kill", ID: id, Reason: "test cleanup"})
	}()
	var mu sync.Mutex
	var deltas []StepEvent
	waitCtx := WithStepHook(plainCtx, func(_ context.Context, evt StepEvent) {
		if evt.Kind != StepKindToolOutputDelta || evt.ToolName != "process" {
			return
		}
		mu.Lock()
		deltas = append(deltas, evt)
		mu.Unlock()
	})
	if _, err := callProcess(t, tl, waitCtx, ProcessInput{Action: "wait", ID: id, Wait: "log", Pattern: "never-matches", TimeoutMs: 600}); err != nil {
		t.Fatalf("wait: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := append([]StepEvent(nil), deltas...)
		mu.Unlock()
		if len(got) > 0 {
			for _, evt := range got {
				if action, _ := evt.Input["action"].(string); action != "wait" {
					t.Fatalf("a wait delta carries action %q, want wait: %+v", action, evt.Input)
				}
				if cmd, _ := evt.Input["command"].(string); cmd != command {
					t.Fatalf("a wait delta carries command %q, want %q", cmd, command)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the wait streamed no output delta to inspect")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
```

   and the same shape for `start`'s own emitter — **with a `wait`**, because a bare
   start returns in milliseconds and may emit no delta at all (measured: 2 of 6
   runs emitted none), which would make the test flap:

```go
// A start that streams during its readiness wait keeps its verb: its emitter
// carried the command but never the action, so the head fell to "Calling".
func TestProcessStartDeltasCarryTheAction(t *testing.T) {
	_, tl, _, projectDir := newProcessToolForTest(t)
	command := "while true; do echo tick; sleep 0.2; done"
	var mu sync.Mutex
	var deltas []StepEvent
	ctx := WithStepHook(processTestContext(projectDir), func(_ context.Context, evt StepEvent) {
		if evt.Kind != StepKindToolOutputDelta || evt.ToolName != "process" {
			return
		}
		mu.Lock()
		deltas = append(deltas, evt)
		mu.Unlock()
	})
	started, err := callProcess(t, tl, ctx, ProcessInput{
		Action: "start", Command: command, WorkDir: projectDir,
		Wait: "log", Pattern: "never-matches", TimeoutMs: 700,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := processTestID(t, started)
	defer func() {
		_, _ = callProcess(t, tl, ctx, ProcessInput{Action: "kill", ID: id, Reason: "test cleanup"})
	}()
	mu.Lock()
	got := append([]StepEvent(nil), deltas...)
	mu.Unlock()
	if len(got) == 0 {
		t.Fatal("the start streamed no output delta to inspect")
	}
	for _, evt := range got {
		if action, _ := evt.Input["action"].(string); action != "start" {
			t.Fatalf("a start delta carries action %q, want start: %+v", action, evt.Input)
		}
		if cmd, _ := evt.Input["command"].(string); cmd != command {
			t.Fatalf("a start delta carries command %q, want %q", cmd, command)
		}
	}
}
```

4. `pkg/safety/procstatus_unix_test.go` — in
   `TestKillIsBoundedWhenAPipeHolderEscapesTheGroup` (`:148-151`):

```go
	if res.ExitSignal != int(syscall.SIGKILL) {
		t.Fatalf("exit signal = %d, want %d: the card explains the code by the signal that produced it", res.ExitSignal, int(syscall.SIGKILL))
	}
```

**Verify**: the focused tool + safety tests pass, including the new cases, and the
safety run reports a test count (not "no tests to run"). Then prove the new tool
assertions are load-bearing: drop `"command": rec.command` from `processKill` and
`readiness["command"] = rec.command` from `processWait` and re-run the focused
tests → they must fail; restore.

### Step 9: Gates

- `gofmt -l cmd pkg third_party plugins` → prints nothing.
- `CGO_ENABLED=1 go vet -tags fts5 ./...` → exit 0.
- Regression: `./pkg/tool ./pkg/safety ./pkg/run ./pkg/tui ./pkg/gateway` → all `ok`.
- `git status --porcelain` → no modified **source** file outside the in-scope
  list (`plans/` files other sessions changed are expected; leave them alone).

Known pre-existing red, not caused by this plan: `pkg/architecture`'s
`TestTestFilesCorrespondToProductionFiles` (`pkg/architecture/cache_test.go:1522`;
`pkg/tui/deliverables_outcome_test.go` has no paired production file). Do not fix
it; mention it in the report.

### Step 10: Real-machine acceptance (tmux + the fake provider)

Same harness as Step 0 — a **noisy** process, so the head is read while live
output keeps redrawing it, plus a long enough wait to capture it mid-flight.

```bash
D=.claude/skills/run-forebrain/driver.sh
export FOREBRAIN_RUN_WORK=$TMPDIR/forebrain-run-killcard FOREBRAIN_RUN_SESSION=fb-killcard FOREBRAIN_RUN_PORT=8765
$D reset; $D build     # rebuild: a stale binary would look like the fix did nothing
$D start tool '[{"name":"process","arguments":{"action":"start","command":"while true; do echo tick; sleep 1; done"}},{"name":"process","arguments":{"action":"wait","id":1,"wait":"log","pattern":"never-matches","timeout_ms":20000}},{"name":"process","arguments":{"action":"kill","id":1,"reason":"freeing the port for the new build"}}]'
$D submit 'start a noisy loop, wait for it, then kill it'
$D wait 'Tool approval' 30; $D key Enter
$D wait 'Tool approval' 30; $D key Enter
sleep 5; $D screen | sed -n '/Waiting for/,+2p' > $TMPDIR/running-card.txt
$D wait 'Tool approval' 30; $D key Enter
sleep 2; $D screen | sed -n '/Killed while true/,+5p' > $TMPDIR/kill-card.txt
cat $TMPDIR/running-card.txt $TMPDIR/kill-card.txt
```

Expected:
- **while running, with output streaming**: `Waiting for while true; do echo tick;
  sleep 1; done  5s` — not `Calling process #1`, not `#1`. (The head may be
  clipped by the terminal width; the command's first words must be there.)
- **settled**: `Killed while true; do echo tick; sleep 1; done` with exactly three
  body rows — `Reason: freeing the port for the new build`,
  `Exit code: 137 — terminated by signal 9 (SIGKILL)`, `Pid: <n>` — no `Killed:`,
  no `Running:`, no `Exit signal:`, no fold.
- after the wait times out: `Waited for <command>` (its four-row body folds —
  expected).

Then (regression): `$D reset; $D start reply hello; $D submit hi; $D screen` → an
ordinary answer, no card damage. `$D stop`, then `pgrep -f 'echo tick'` → nothing.

Also read the **noisy start** window, the second half of the same defect: run the
same recipe with the first call as
`{"action":"start","command":"while true; do echo tick; sleep 1; done","wait":"log","pattern":"never-matches","timeout_ms":15000}`
and capture at `sleep 3` — the head must read `Starting background process while
true; …` while the output streams, not `Calling process #1`.

### Step 11: Render the fixed cards as an image for the owner

The owner asked to see the result ("修复后的消息卡的效果要发给我确认"). Turn the two
captures from Step 10 into one legible image and hand it over with
`present_files`:

```bash
cd "$TMPDIR"
{ printf 'before:  Calling process #1 3s   /  Killed process #1 + four machine rows\n\n'
  printf 'after:\n'; cat running-card.txt; printf '\n'; cat kill-card.txt; } > cards.txt
magick -background '#1e2124' -fill '#e6e6e6' -pointsize 15 \
  -size 1000x caption:@cards.txt "$FOREBRAIN_RUN_WORK/fixed-process-cards.png"
```

ImageMagick is `magick` (`convert` on older installs); the `image-edit` skill has
it. If neither exists, deliver `cards.txt` as the artifact and say so. Label the
image as a render of the real tmux capture — never a mockup.

### Step 12: Write the plan into the backlog

Copy this plan to `plans/380-process-card-head-names-command.md` and append one
row to the plan table in `plans/README.md` (the file is huge and already dirty:
append near the other TODO rows, never reflow it). Another session
(`forebrain-harness-29`, plan 381) is inserting its own rows into the same file:
write yours **after** it reports done, at a single anchor, and re-read the file
immediately before writing — never restore an older copy of it.

```
| [380-process-card-head-names-command](380-process-card-head-names-command.md) | process 卡片：Started/Waiting/Waited/Killed 卡头点名实际命令（运行中、含实时输出也不回退）、删掉步骤摘要行、正文显示模型给的 reason 并把退出码 137 解释成「被信号 9 SIGKILL 终止」；kill 新增必填 reason 参数 | Bug Fix | DONE（<date> 实施并真机验收；改动留工作区未 commit） | **P1** | M | MED |
```

**Verify**: `grep -n '380-process-card-head-names-command' plans/README.md` → the row.

## Test plan

- New/updated cases are in Step 8; follow the shape of
  `TestDisplayPartsNamesTheProcessCommandAndID` (one frame per state) and
  `TestReadableToolAnswerDropsTheRowTheHeadNames` (one block per action).
- Cases: a settled kill names the command; a running wait names it too, and its
  live deltas keep `action`+`command`; an unresolvable call keeps `#N`; the kill
  body leads with the reason, explains a signal death, drops
  command/id/killed/running/exit_signal; the real stored envelope reads the same as
  the raw payload; an unnamed signal keeps its number; a kill without a reason is
  refused; the three wait payloads carry the command; the dispatch resolution
  covers wait/kill, a live and an exited record, and nothing else; `pkg/safety`
  records the signal; a process step's `SummarizeToolStep` is empty.
- Regression: `start` and `log` cards keep today's head and body; old replayed rows
  keep `process #N`.
- Commands: the focused runs, then the five-package regression.

## Done criteria

ALL must hold:

- [ ] `gofmt -l cmd pkg third_party plugins` prints nothing.
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./...` exits 0.
- [ ] `go test -tags fts5 ./pkg/tool ./pkg/safety ./pkg/run ./pkg/tui ./pkg/gateway -count=1` → all `ok`.
- [ ] `grep -n 'ExitSignal' pkg/safety/sandbox_config.go pkg/safety/process.go pkg/tool/process_tool.go pkg/tool/format.go` → declaration, assignment, payload, explanation.
- [ ] `grep -c '"command": rec.command' pkg/tool/process_tool.go` → 4; `grep -c 'readiness\["command"\]' pkg/tool/process_tool.go` → 1.
- [ ] `grep -n 'ProcessStartedCommand' pkg/tool/process_tool.go pkg/run/orchestration_llm.go` → declaration + one call; `grep -n 'cardInput' pkg/run/orchestration_llm.go` → the copy and **both** events' `Input`.
- [ ] `grep -n 'startProcessLiveTail' pkg/tool/process_tool.go` → definition with the action parameter + the call passing `in.Action`; **both** delta emitters carry `action` (`grep -n '"action":  "start"\|"action": action' pkg/tool/process_tool.go` → 2 matches).
- [ ] `grep -n 'reason is required for kill' pkg/tool/process_tool.go` → 1 match.
- [ ] `grep -n 'InputString(meta, "command")' pkg/tool/header_parts.go` → the start, wait and kill branches.
- [ ] `grep -n 'func readProcessAnswer\|func explainProcessExitCode\|func processAnswerFacts\|func processContentFacts\|func processExitSignal' pkg/tool/format.go` → five matches; `grep -n 'if strings.EqualFold(toolName, "process")' pkg/tool/format.go` → 1.
- [ ] `grep -n 'Action: "kill"' pkg/tool/process_tool_test.go` → all eight lines also carry `Reason:`.
- [ ] Step 10's captures show `Waiting for <command>` while output streams and
  `Killed <command>` with the three rows and the SIGKILL sentence, no fold.
- [ ] The image from Step 11 exists (or `cards.txt`, with the reason stated).
- [ ] `plans/380-…md` exists and `plans/README.md` carries its row.
- [ ] `git status --porcelain` shows no modified source file outside the in-scope list.
- [ ] Nothing committed (`git log -1 --format=%H` still `7e6aee3b…`).

## STOP conditions

Stop and report (do not improvise) if:

- The excerpts in "Current state" do not match the live code (drift).
- The kill payload already carries `command`, `CommandResult` already has
  `ExitSignal`, or `ProcessInput` already has `Reason`.
- A non-test caller of the process tool sends `kill` without a reason, so making
  it required breaks a real path: report that caller instead of adding a fallback.
- **The running head still shows `#N` or `Calling process`** after Steps 3-6 — the
  facts are not reaching the card's input on that surface. Report it with the
  capture; do not "fix" it by editing `pkg/tui` or `pkg/gateway`.
- The head change turns out to need `pkg/event`, `pkg/tui`, `frontend` or
  `pkg/gateway`.
- On the real run the kill's exit code is not 137 — the signal is not reaching the
  payload.
- A step's verification fails twice after a reasonable fix attempt.
- `go test ./pkg/tui` fails for a reason unrelated to this change while another
  agent is editing that package: report it, do not fix it.

## Maintenance notes

- A process call's facts ride the **event's `Input`**: `ToolMeta.Input` is the one
  map the header reads and every surface already mirrors it. A future card fact
  for a tool with a result-only fact has the same constraint (or widens
  `event.ToolCallMeta` deliberately, together with `pkg/tui` and the gateway).
- `ProcessStartedCommand` resolves against the *runner's own* registry: a process
  started by another runner stays `#N`. That is the honest answer, not a defect.
  The record outliving its process is deliberate (see `processRegistry.release`).
- `processAnswerFacts` reads the payload out of the double envelope
  (`{"output": "<json text>"}`). If the tool framework ever returns maps
  (`fallbackToolOutput`'s first branch), that path reads the plain map — but the
  reading (`ReadToolAnswer`) and this helper must then be checked together.
- `processSignalNames` is deliberately partial (only numbers that mean the same
  signal on Linux and macOS); a platform-specific name belongs beside the
  `WaitStatus` decode in `pkg/safety`.
- Deferred and reported, not fixed here: the `log` card keeps `Read log of
  process #N` with the command in its body; the `list` card's per-process exit
  codes stay unexplained; `start`/`log` cards report `Running: false` with no exit
  code; the shell card's own bare exit codes; `pkg/tui`'s live-step summary
  fallback (`done`/`ok`) when a tool's summary is empty, and its replay path
  rebuilding `ran <invocation>` from `meta.Invocation` (`commands.go:2108-2113`).
- A reviewer should scrutinize: that the reason row comes from the tool *input*,
  that `exit_signal` never becomes a row, that the enriched event `Input` is a
  copy (the model's arguments must stay untouched), and that no surface-local
  rendering was added.
## Implementation Status

**Verdict: APPROVE** (tech-lead review, 2026-11-25), **landed on `dev`** by the
owner's instruction. Executed by `alibaba/deepseek-v4.1-flash-1017` in an
isolated worktree; two owner rulings arrived mid-flight and were folded in
through two revision rounds (below).

- **Landed**: commit `141dbb30` on `dev` (branch
  `.worktrees/process-card-head-names-command`, rebased from `7e6aee3b` onto
  `231782cf`, then fast-forwarded). A follow-up commit `b815396f` updates the one
  `pkg/tui` assertion that pinned the superseded log-card contract (its body kept
  `Command:` because its head named only the id) — caught by the landing suite.
- **Worktree**: `.worktrees/process-card-head-names-command`, branch
  `process-card-head-names-command` (kept; nothing pushes, nothing to delete).
- **Diff**: 10 files, +657/−81, all in the plan's in-scope list
  (`pkg/safety/{sandbox_config,process,procstatus_unix_test}.go`,
  `pkg/tool/{process_tool,format,header_parts,format_test,process_tool_test,display_header_test}.go`,
  `pkg/run/orchestration_llm.go`); no file outside scope changed.

### Owner rulings folded in after approval

1. *"calling process 消息卡头的 #35 也必须改成完整的 shell 命令…同类问题必须本期一并修复"* —
   the header's `default:` branch (an action this build does not name) now names
   the command when the call's facts carry one, and `log` names it too (its body
   drops `Command:`/`Id:`). `ProcessStartedCommand` therefore resolves `log` as
   well — without that the log head could never see a command in a real run.
2. *"read log消息卡头的 #35 也必须改成完整的 shell 命令，output区域 Command 字段删掉"* —
   same change as (1); confirmed on the real binary.
3. *"上面这次process实际是阻塞式的…卡头却说是background process，请定位根因并彻底修复"* —
   root cause: `process start` carrying `wait` runs the readiness poll inline, so
   the call really blocks (the owner's card: `Elapsed ms: 288173`, `Ready: true`,
   suffix `wait log`), while the head said `Started background process` and the
   tool description promises "start returns immediately". Owner chose the wait
   wording: a start with `wait` reads `Waiting for <cmd> to be ready Ns` /
   `Waited for <cmd> to be ready Ns` (failed: `Failed to wait for`), and the
   start delta emitter now carries `wait` so live chunks cannot redraw the card
   back into the background wording.

### Verification I ran myself (in the worktree, after the last revision)

| Check | Result |
|---|---|
| `gofmt -l cmd pkg third_party plugins` | prints nothing |
| `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0 |
| `go test -tags fts5 ./pkg/tool` (full) | `ok` (25s) |
| `go test ./pkg/tool ./pkg/safety ./pkg/run ./pkg/gateway` | all `ok` |
| `go test ./pkg/tool ./pkg/safety ./pkg/run ./pkg/tui ./pkg/gateway` (earlier run) | first run tripped the documented `TestReapOrphanProcessesKillsAMatchingOrphan` flake; isolated run `ok`, second parallel run all `ok` |
| `git status --porcelain` (worktree) | the ten in-scope files only |
| landing suite on `dev` (full `./pkg/... ./cmd/forebrain`) | one real red: `pkg/tui`'s log-card assertion (superseded contract) → fixed in `b815396f`, `pkg/tui` full run `ok 199s`; the only remaining red is the pre-existing `pkg/architecture` `TestTestFilesCorrespondToProductionFiles` |
| committed? | yes: `141dbb30` (feature) + `b815396f` (test contract), `dev` fast-forwarded, not pushed |

Real-machine captures (fake provider + tmux, isolated home): `Waiting for <cmd>
to be ready 2s` mid-block, `Waited for <cmd> to be ready 15s` settled, `Started
background process <cmd>` for a plain start, `Read log of <cmd>` with no
`Command:`/`Id:` rows, `Killed <cmd>` + `Reason` / `Exit code: 137 — terminated
by signal 9 (SIGKILL)` / `Pid`, no `ran process` summary line anywhere.

### Deviations (documented, accepted)

- Done criterion `grep -c '"command": rec.command' … → 4` is wrong: the delta
  input the plan itself prescribes makes it **5**. No payload site is missing.
- `ProcessStartedCommand` covers `log` (ruling 1 above), not only `wait`/`kill`.
- ImageMagick is absent on this machine, so the confirmation image was rendered
  from the real captures with PIL (`plans/380-process-cards.png` in the main
  tree); it is a render of real tmux output, not a mockup.
- First round implemented before writing the tests; the red evidence is the
  pre-fix binary's capture plus a revert-and-fail experiment on each new
  assertion (both quoted in the executor reports).

### Left open / follow-ups

- **Closed (owner asked for it, commit `65fdd571`)**: the web's Chinese wording now
  has the word for the new suffix — `headerSuffixWords('to be ready')` →
  `直到就绪`, so the composed head reads `正在等待 <cmd> 直到就绪 4m48s` /
  `已等待 <cmd> 直到就绪 15s`. Pinned twice: the word table test and a
  `ToolHeader` render test asserting the three spans. The old `wait <mode>` rule
  stays reachable for rows stored before the head change.
- `pkg/tui`'s live-step summary fallback (`done`/`ok`) and the replay path that
  rebuilds `ran <invocation>` still carry the deleted summary in those two
  places; out of scope here.
- The approval card above a process call still reads `calling process` (no
  command); not part of this plan's rulings.
