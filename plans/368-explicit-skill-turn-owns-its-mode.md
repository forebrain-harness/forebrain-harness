# Plan 368: An explicitly invoked skill keeps the session in its current mode — `enter_plan_mode` is not offered on that turn

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 80bb73b4..HEAD -- pkg/run/config.go pkg/run/skills.go pkg/run/config_test.go pkg/run/skills_test.go`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.
>
> **Baseline note**: this plan was researched against `80bb73b4`. While it was
> being written, another session executed plan 367 and fast-forwarded `dev` to
> `dc93b1ac` (`fix(memory): anchor tool paths and refuse traversal`, touching
> `pkg/memory/backend.go`, `pkg/memory/backend_test.go`, `pkg/memory/instruction.go`,
> `pkg/tool/request_permissions_test.go`, `pkg/tool/session_tools.go`). None of
> those are in scope here: the drift check above was run against `dc93b1ac` and
> is **empty**, and the two out-of-scope anchors this plan cites in
> `pkg/tool/session_tools.go` were re-verified after the merge
> (`enterPlanModeDescription` still at `:348`, "When unsure, plan." still at
> `:356`). Note for the `git status --porcelain` criterion in Step 5: 367's
> plan files and `plans/README.md` are uncommitted work from that other session
> and are **expected** in the output alongside your own five files; anything
> else is yours to explain.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW (one per-request tool removal on an existing pattern + one prompt sentence; no state, schema, storage or permission change)
- **Depends on**: none (no file overlap with plans 353–367; see "Dependency notes" below)
- **Category**: bug (agent behavior / skill system)
- **Planned at**: commit `80bb73b4`, 2026-11-23

## Why this matters

When a user explicitly invokes a skill whose own workflow produces plans —
the built-in `audit` skill, or an installed `improve`-style skill — and the
conversation is in agent mode, the primary agent reliably calls
`enter_plan_mode`, writes exactly one plan file, and calls `exit_plan_mode`.
The skill's real deliverable is lost: `audit` is specified to write a
numbered backlog (`plans/NNN-short-slug.md` plus a `README.md` index) at the
**project root**, and to produce several plans after the user picks which
findings to plan. Plan mode makes that impossible, because it confines every
write to `<workspaceRoot>/plans/<projectKey>/<sessionID>/` — a directory
under the harness state root, never the user's repository.

The user gets one approval card for a mode switch they never asked for, then
one session plan file instead of the backlog the skill promises. The fix is a
deterministic gate: on a turn that carries an explicit skill activation, the
session mode is already the user's choice, so `enter_plan_mode` is not
offered. This is the same mechanism the runtime already uses to hide
`exit_plan_mode` from an office conversation that is not in plan mode.

## Root cause (verified at `80bb73b4`)

Six links, in order. The first two are why the mode switch happens at all;
the last four are why the outcome is always a single plan file.

1. **The invitation.** `enterPlanModeDescription`
   ([pkg/tool/session_tools.go:348-361](../pkg/tool/session_tools.go)) tells
   the model to use the tool when "requirements you must explore before you
   understand the scope", "several valid approaches", or "likely more than
   2-3 files" — and closes with **"When unsure, plan."** Its skip list is
   only "few-line fixes, a single function with clear requirements, tasks the
   user specified in detail, and pure research". An audit request satisfies
   the first three bullets and matches none of the skips.
2. **No counter-signal.** `explicitSkillActivationMessage`
   (`pkg/run/skills.go:649-668`) tells the model the user picked a skill and
   inlines its body, but says nothing about session mode, and nothing that
   the skill's own deliverable rules govern the turn.
3. **Hard confinement.** In plan mode `planModeLLM.Execute` calls
   `tool.WithAllowedPlanPath(ctx, planDir)` (`pkg/run/config.go:816`) and
   `isSanctionedPlanWrite` (`pkg/tool/state.go:1191-1203`) permits only files
   directly inside `planDir`. `planDir` is
   `state.PlanDirForSession(...)` = `<workspaceRoot>/plans[/<projectKey>]/<sessionID>`
   (`pkg/state/plan.go:39-41,50`) — the **state root**, not the project root.
   The full reminder states it: "All write_file/edit_file calls to paths
   outside this directory are blocked by GuardWrite" (`pkg/run/config.go:1160`).
4. **Single-plan instruction.** `SinglePlanModePrompt`
   (`pkg/agent/advisor_prompts.go:51-55`) is appended to the full plan-mode
   reminder at `pkg/run/config.go:1200` (office variant at `:1307`):
   "Write one plan … into this session's plan file - no other path is
   writable in plan mode".
5. **The skill complies.** The built-in `audit` SKILL.md
   (`pkg/skill/system_assets/audit/SKILL.md`, "Boundaries and evidence")
   reads: "Write plans only where this session's plan rules allow. Inside
   plan mode that is the session's plan file and nothing else."
6. **Workflow override.** The reminder's "Plan-mode workflow (Phase 1-5)"
   (`pkg/run/config.go:1170-1180`) replaces the skill's own
   recon → audit → vet → select → plan-many workflow with the generic one,
   ending in "Phase 5 - Exit: call exit_plan_mode".

Fixing only 1 and 2 (prose) is not enough — models ignore prose, and the
report is that this happens *always*. Fixing only 3 is not acceptable either:
plan mode's write confinement is a documented security guarantee
(`pkg/run/config.go:1195`, "This is a hard guarantee") and must not be
widened. The reliable seam is the tool array, which the runtime already
edits per request.

## Current state

Files and their roles:

- `pkg/run/config.go` — holds `planModeLLM`, the innermost LLM wrapper
  (`pkg/run/runner.go:1023` wraps the raw client with it; `wrapExecutionLLM`
  then wraps fork-capture and tool-orchestration *around* it). It owns
  plan-mode reminder injection, the write-path sanction, and the existing
  per-request tool removals. **This is where the gate goes.**
- `pkg/run/skills.go` — owns explicit skill invocation: the trusted
  selection, the real load, and the activation message that carries the
  SKILL.md body into the turn.
- `pkg/run/config_test.go`, `pkg/run/skills_test.go` — where the new tests
  go. House rule: `X_test.go` must sit beside `X.go`
  (`./pkg/architecture -run TestTestFilesCorrespondToProductionFiles`); do
  not create a new test file.

### Excerpt A — the existing per-request removal pattern to copy

`pkg/run/config.go:750-760`:

```go
// withoutTool returns a new slice with the named tool removed. The caller's
// slice is never modified.
func withoutTool(tools []*llm.Tool, name string) []*llm.Tool {
	out := make([]*llm.Tool, 0, len(tools))
	for _, t := range tools {
		if t == nil || t.Name() != name {
			out = append(out, t)
		}
	}
	return out
}
```

Its existing caller, `pkg/run/config.go:762-796` (the top of `Execute`, with
the office `exit_plan_mode` removals and the mode branch):

```go
func (w *planModeLLM) Execute(ctx context.Context, msgs []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if w.stateRoot == "" {
		// Office hides exit_plan_mode unless plan mode is proven; every early
		// exit below fails closed the same way.
		if w.office {
			tools = withoutTool(tools, "exit_plan_mode")
		}
		return w.inner.Execute(ctx, msgs, tools)
	}
	sid := llm.AgentSessionIDFromContext(ctx)
	if sid == "" {
		if w.office {
			tools = withoutTool(tools, "exit_plan_mode")
		}
		return w.inner.Execute(ctx, msgs, tools)
	}
	st, err := state.Get(w.stateRoot, sid)
	if err != nil {
		if w.office {
			tools = withoutTool(tools, "exit_plan_mode")
		}
		return w.inner.Execute(ctx, msgs, tools)
	}
	// The user puts an office conversation into plan mode explicitly, so the
	// tool is offered only once that state has been read and says plan.
	if w.office && st.Mode != state.ModePlan {
		tools = withoutTool(tools, "exit_plan_mode")
	}
	projectKey := w.projectKey
	if ctxProjectKey := tool.ProjectKeyFromContext(ctx); ctxProjectKey != "" {
		projectKey = ctxProjectKey
	}
	if st.Mode != state.ModePlan {
		return w.executeImplementationPhase(ctx, msgs, tools, projectKey, st)
	}
```

Note the three degenerate early returns (`stateRoot == ""`, `sid == ""`,
`state.Get` error). They are *fail-closed* for office. The new gate must sit
**above all of them**, or an explicit-skill turn whose mode state happens to
be unreadable would still be offered `enter_plan_mode`.

### Excerpt B — the activation reader the gate keys on

`pkg/run/skills.go:507-527`:

```go
// explicitSkillActivationsFromContext returns every seeded activation, in load
// order.
func explicitSkillActivationsFromContext(ctx context.Context) ([]explicitSkillActivation, bool) {
	if ctx == nil {
		return nil, false
	}
	activations, ok := ctx.Value(explicitSkillActivationsContextKey{}).([]explicitSkillActivation)
	if !ok || len(activations) == 0 {
		return nil, false
	}
	return activations, true
}

// explicitSkillActivationFromContext returns the first seeded activation.
func explicitSkillActivationFromContext(ctx context.Context) (explicitSkillActivation, bool) {
	activations, ok := explicitSkillActivationsFromContext(ctx)
	if !ok {
		return explicitSkillActivation{}, false
	}
	return activations[0], true
}
```

Same package as `config.go`, so the gate needs no new import and no export.

**The ctx propagation this relies on is already verified** — do not
re-architect it:

- `pkg/run/runner.go:1631` — `preloaded, err := r.preloadExplicitSkills(ctx)`,
  then `res, err := main.Run(preloaded, parts...)` (line 1637).
- `pkg/agent/agent.go:333` — `msg, err := tracedLLM.Execute(ctx, session, a.tools)`:
  the same ctx reaches the LLM chain on every loop iteration.
- `pkg/run/transcript.go:178` — the session builder already reads
  `explicitSkillActivationsFromContext(ctx)` during a real run, which is
  independent proof the value survives to run-internal callees.
- `pkg/run/subagent.go:2627` and `:4531` — children are dispatched with
  `withoutExplicitSkills(ctx)`, so a subagent never inherits the gate. This
  is the existing, intended behavior; do not change it.

### Excerpt C — the activation message to extend

`pkg/run/skills.go:649-668`:

```go
func explicitSkillActivationMessage(activation explicitSkillActivation) llm.Message {
	// The skills catalog tells the model to read a skill's SKILL.md before
	// acting on it. Here the user picked the skill by name and its body is
	// already inlined, so this says exactly that and nothing more — the model
	// needs no extra protocol for a skill it did not have to find.
	text := "The user selected the `" + activation.SkillName + "` skill for this request. " +
		"Its full instructions are included below; follow them for this turn. " +
		"You do not need to read its SKILL.md again.\n\n" +
		strings.TrimSpace(activation.Content)
	msg := llm.UserMessage(llm.Text(text))
	msg.IsMeta = true
	// Rebuilt from the slash-command context on every request, so it must never
	// be persisted. … (comment continues)
	msg.Ephemeral = true
	return msg
}
```

### Excerpt D — the constraint on any wording you add to that message

`pkg/run/skills_test.go:296-320`, `TestExplicitSkillActivationMessageCarriesNoToolVocabulary`,
bans three case-insensitive substrings from the whole message text:

```go
	// The message delivers instructions; it must not read as a description of
	// some callable mechanism, which would send the model looking for one.
	for _, banned := range []string{"tool", "call", "invoke"} {
		if strings.Contains(strings.ToLower(text), banned) {
			t.Fatalf("message reads as an invocation protocol (%q) rather than instructions:\n%s", banned, text)
		}
	}
```

The sentence proposed in Step 2 was checked against this list and contains
none of `tool`, `call`, `invoke`. Do not reword it into something that does —
in particular, do **not** write "do not call enter_plan_mode": the tool is
absent from the array on that turn, so naming it would send the model looking
for a mechanism that is not there, which is exactly what this test exists to
prevent.

### Excerpt E — the test exemplar to model the new tests on

`pkg/run/config_test.go:1911-1930`, `TestOfficeAgentModeRequestHasNoExitPlanMode`:

```go
func TestOfficeAgentModeRequestHasNoExitPlanMode(t *testing.T) {
	home := t.TempDir()
	sid := "office-agent"
	setMode(t, home, sid, state.ModeAgent, 0)

	inner := &capturingLLM{}
	w := wrapPlanModeLLMFor(inner, home, "", state.ProjectTypeOffice)
	ctx := llm.WithAgentSessionID(context.Background(), sid)
	tools := []*llm.Tool{newPlanToolForTest(t, "write_file"), newPlanToolForTest(t, "exit_plan_mode")}
	if _, err := w.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hello"))}, tools); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if hasToolNamed(inner.gotTools, "exit_plan_mode") {
		t.Fatalf("office agent mode must not offer exit_plan_mode, got %v", toolNames(inner.gotTools))
	}
	if !hasToolNamed(inner.gotTools, "write_file") {
		t.Fatalf("unrelated tool dropped: %v", toolNames(inner.gotTools))
	}
}
```

Reuse its helpers as-is — they are all in package `run` but spread over three
files, so do not go looking for them in `config_test.go` alone:

| Helper | Defined at |
|---|---|
| `capturingLLM` (fields `gotMsgs`, `gotCtx`, `gotTools`, `calls`) | `pkg/run/orchestration_llm_test.go:2874-2879`; its `Execute` at `:2899-2905` |
| `setMode(t, home, sid, mode, turn)` | `pkg/run/orchestration_llm_test.go:2907` |
| `toolNames(in)` | `pkg/run/run_test.go:344` |
| `newPlanToolForTest(t, name)` | `pkg/run/config_test.go:1892` |
| `allReminderText(msgs)` | `pkg/run/config_test.go:1903` |
| `hasToolNamed(tools, want)` | **production code**, `pkg/run/orchestration_llm.go:2845` — not a test helper; do not redefine it |

`setMode` writes `Phase: "plan"` and `PrePlanMode: state.ModeAgent` whatever
mode you ask for; that is what the office tests already run against, so pass
`state.ModeAgent` / `state.ModePlan` as the fourth argument and ignore the rest.

For the coding (non-office) wrapper use `wrapPlanModeLLM(inner, home, "")`
(`pkg/run/config.go:722`): it leaves `office == false`, which is what this plan
needs. `wrapPlanModeLLMFor` (:741) also works if you pass a non-office project
type, but it returns `nil` on a failed type assertion, so `wrapPlanModeLLM` is
the simpler choice.

The degenerate-path exemplar is `TestOfficeModeReadFailureHidesExitPlanMode`
(`pkg/run/config_test.go:1951-1971`): it makes `state.Get` fail by creating a
**directory** where the mode file should be:

```go
	if err := os.MkdirAll(filepath.Join(home, "state", "modes", sid+".json"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
```

### Conventions to match

- Comments explain **why**, in prose, at the site of the non-obvious
  decision. Excerpt A is the local style: every removal carries a sentence
  saying which guarantee it serves. Match it — write one comment block for
  the gate, not one per line.
- No new exported symbol is needed. Keep everything unexported and inside
  `pkg/run`.
- `run` must not import `turn` or `session` (`pkg/run/skills.go:761-768`
  records this). The gate uses only `pkg/run` and the already-imported
  `pkg/tool`, `pkg/state`, `pkg/llm`, so this holds.

## Commands you will need

`make test` depends on `libtokenizers pdfium pdfium-fonts` (native fetches
into `third_party/`). Prior runs of this repo's plans verified with plain
`go test -tags fts5` per package, which is what you should use. If linking
fails because `third_party/` is empty, run `make libtokenizers pdfium
pdfium-fonts` once and report it — do not work around it by dropping
`-tags fts5`.

| Purpose | Command | Expected on success |
|---|---|---|
| Targeted new tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1 -run 'TestExplicitSkillTurn\|TestOrdinaryTurnKeeps\|TestExplicitSkillGate'` | `--- PASS` for each named test; no `[no tests to run]` |
| Whole package | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1` | `ok  github.com/forebrain-harness/forebrain-harness/pkg/run` (≈45s in prior runs) |
| Message wording guard | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1 -run 'TestExplicitSkillActivationMessage'` | all PASS, including the pre-existing `…CarriesNoToolVocabulary` **unmodified** |
| Vet | `go vet -tags fts5 ./pkg/run/` | exit 0, no output |
| Format | `gofmt -l pkg/run/config.go pkg/run/skills.go pkg/run/config_test.go pkg/run/skills_test.go` | no output |
| Test-file house rule | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1 -run TestTestFilesCorrespondToProductionFiles` | PASS |
| Full suite (final gate) | `FOREBRAIN_HOME=scratch CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 30m` | exit 0, zero FAIL |

## Scope

**In scope** (the only files you should modify):

- `pkg/run/config.go` — the gate (Step 1)
- `pkg/run/config_test.go` — the four gate tests (Step 3)
- `pkg/run/skills.go` — one sentence in `explicitSkillActivationMessage` (Step 2)
- `pkg/run/skills_test.go` — one new test for that sentence (Step 4)
- `plans/README.md` — the status row and the run bullet (Step 6)

**Out of scope** (do NOT touch, even though they look related — each was
examined and deliberately left alone):

- `pkg/tool/session_tools.go`, specifically `enterPlanModeDescription`
  (:348-361). It is the *invitation* that causes this bug, but it sits in the
  cached tools+system prefix. `pkg/run/skills.go:145-152` records why that
  matters: "the tool array renders ahead of the system prompt and the whole
  conversation, so a schema that listed the skills would re-bill the entire
  cached prefix on every install". Editing this description re-bills every
  session's prefix for a fix the gate already makes deterministic. **Do not
  "also narrow the skip list while you are in there."**
- `pkg/tool/state.go` — `isSanctionedPlanWrite` (:1191-1203) and `GuardWrite`.
  Plan mode's write confinement is a security guarantee
  (`pkg/run/config.go:1195`). Widening it so a skill could write a project
  backlog from inside plan mode would let any plan-mode turn write anywhere
  the skill text names. Rejected.
- `pkg/agent/advisor_prompts.go` — `SinglePlanModePrompt` (:51-55) and its
  golden `pkg/agent/testdata/advisor_prompts.golden.json`. Both are correct
  for a plan mode the *user* chose. Changing them would also churn the golden.
- `pkg/run/config.go`'s reminder builders — `buildPlanModeReminder`
  (:1083-1201), `officeMainPlanFullFormat` (:1207+), and
  `executeImplementationPhase` (:898-937). The reminders stay as they are;
  the gate is above them. If you find yourself editing reminder text, stop —
  that is a different plan.
- `pkg/skill/system_assets/audit/SKILL.md`. Its clause "Inside plan mode that
  is the session's plan file and nothing else" remains true and remains the
  right behavior for a user-chosen plan mode. Its other branch ("Otherwise
  write under plans/ at the project root") is what this plan makes reachable
  again. Editing an embedded asset also re-extracts every system skill on the
  next launch (`pkg/skill/system.go:52-71`) — an unnecessary side effect.
- `pkg/run/subagent.go:2627,4531` (`withoutExplicitSkills`) and
  `pkg/run/transcript.go:178`. Both already do the right thing.
- `pkg/state/plan.go`. The plan directory location is not the bug.

## Git workflow

- This repo's plans are executed in-tree on `dev` with **zero commits** unless
  the owner says otherwise (recorded repeatedly in `plans/README.md`, e.g. the
  2026-11-20 batch: "零 commit／零 stash／零分支，改动全部留在工作区").
  Follow that: make the edits, run the gates, leave the changes in the working
  tree, and report.
- Do **not** commit, stash, push, create a branch, or open a PR unless the
  operator explicitly instructs it.
- If you are dispatched into a disposable worktree instead, commit there with
  the repo's style (`git log --oneline -10` shows Conventional Commits with a
  scope, e.g. `docs(plans): …`, `fix`, `style(ui): lowercase footer key hints`).
  A fitting title: `fix(run): an explicit skill turn is not offered plan mode`.
  Note `.githooks` enforces a commit-title check plus DCO sign-off
  (`Makefile:106-109`); never bypass hooks.

## Steps

### Step 1: Add the gate at the top of `planModeLLM.Execute`

In `pkg/run/config.go`, insert the gate as the **first statement** of
`Execute` (currently line 762), above the `if w.stateRoot == ""` early return,
so it also covers the three degenerate fail-closed paths.

Target shape (comment is load-bearing — it records why the gate exists and
what the user still has):

```go
func (w *planModeLLM) Execute(ctx context.Context, msgs []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	// An explicitly invoked skill is the user's own choice of workflow for this
	// turn, so the sign-off plan mode exists to obtain has already been given.
	// Offering it here costs an approval round-trip and then rewrites the
	// skill's deliverable: plan mode confines every write to the session plan
	// directory under the state root (WithAllowedPlanPath below), so a skill
	// specified to write a numbered backlog at the project root can only ever
	// produce one file. The gate is per-request and the activation lives on
	// this turn alone, so the next turn offers the tool again, and the user who
	// does want plan mode still has the hotkey and /plan. Subagents never
	// inherit it: withoutExplicitSkills clears the activation before dispatch.
	if _, ok := explicitSkillActivationFromContext(ctx); ok {
		tools = withoutTool(tools, "enter_plan_mode")
	}
	if w.stateRoot == "" {
		// … unchanged from here down
```

Do not change anything else in `Execute`. In particular leave the office
`exit_plan_mode` removals and the `st.Mode != state.ModePlan` branch exactly
as they are.

`tools` is a parameter, not a field, so reassigning it affects only this
request — the same thing the office removals already do. The caller's slice is
not modified (`withoutTool` allocates).

**Verify**: `go vet -tags fts5 ./pkg/run/` → exit 0.
**Verify**: `gofmt -l pkg/run/config.go` → no output.
**Verify** (the gate compiles against a real reader, not a guessed symbol):
`grep -n "explicitSkillActivationFromContext" pkg/run/skills.go pkg/run/config.go`
→ one definition in `skills.go` (:521) and one use in `config.go`.

### Step 2: Say what the turn's contract is, in the activation message

In `pkg/run/skills.go`, extend the `text` built by
`explicitSkillActivationMessage` (:654-657) with one sentence. Insert it after
"You do not need to read its SKILL.md again." and before the `"\n\n"`:

```go
	text := "The user selected the `" + activation.SkillName + "` skill for this request. " +
		"Its full instructions are included below; follow them for this turn. " +
		"You do not need to read its SKILL.md again. " +
		"The workflow those instructions describe is this turn's contract: run it in the session's current mode, and produce the deliverables it names where it names them.\n\n" +
		strings.TrimSpace(activation.Content)
```

Also update the function's doc comment (:650-653) so it still describes what
the message does — it currently ends "the model needs no extra protocol for a
skill it did not have to find", which stays true; append a clause recording
that the message also fixes the turn's mode contract.

Wording constraints, both checked:

- Must not contain `tool`, `call` or `invoke` (case-insensitive) — Excerpt D.
  The sentence above contains none.
- Must not name `enter_plan_mode` or plan mode. The tool is absent on this
  turn; naming an absent mechanism is the failure mode Excerpt D guards.

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1 -run 'TestExplicitSkillActivationMessageCarriesNoToolVocabulary'`
→ `--- PASS`. This is the pre-existing test, **unmodified**; if it fails, your
wording is wrong, not the test.

### Step 3: Four gate tests in `pkg/run/config_test.go`

Add them beside the office removal tests (after
`TestOfficeEmptySessionIDHidesExitPlanMode`, ≈:1983) so the family reads
together. Model each on Excerpt E. Coding runtime, not office.

1. `TestExplicitSkillTurnHidesEnterPlanMode` — agent mode
   (`setMode(t, home, sid, state.ModeAgent, 0)`), ctx =
   `llm.WithAgentSessionID(...)` then `WithExplicitSkillActivation(ctx, "audit", "<skill>body</skill>")`,
   tools = `write_file` + `enter_plan_mode`. Assert `enter_plan_mode` is
   **absent** from `inner.gotTools` and `write_file` is **still present**.
   (The second assertion is what proves `withoutTool` did not drop the array.)
2. `TestOrdinaryTurnKeepsEnterPlanMode` — identical but **without** the
   activation. Assert `enter_plan_mode` **is present**. This is the negative
   control: without it, test 1 passes even if the wrapper drops every tool.
3. `TestExplicitSkillTurnInPlanModeStillGetsTheReminder` — plan mode
   (`setMode(t, home, sid, state.ModePlan, 0)`) **with** an activation. Assert
   `allReminderText(inner.gotMsgs)` contains `PLAN MODE ACTIVE`, and that
   `exit_plan_mode` is still offered (`hasToolNamed(inner.gotTools, "exit_plan_mode")`)
   when it was in the input array. This pins that the gate does not suppress
   planning the user asked for: a user who switched to plan mode and then
   invoked a skill still gets plan mode and its reminder. Both field names are
   confirmed on `capturingLLM` (`orchestration_llm_test.go:2874-2879`).
4. `TestExplicitSkillGateAppliesWhenModeStateIsUnreadable` — the degenerate
   path: create a directory at `filepath.Join(home, "state", "modes", sid+".json")`
   so `state.Get` errors (Excerpt E), **with** an activation, agent-mode
   intent. Assert `enter_plan_mode` is absent. This pins that the gate sits
   above the early returns; if you placed it inside
   `executeImplementationPhase` instead, this test fails — that is the point.

All four must use the same helper set as the office tests. Do not add a new
fake LLM.

**Verify**: the targeted command in the Commands table → `--- PASS` × 4.
**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1` → `ok`.

### Step 4: One test for the contract sentence, in `pkg/run/skills_test.go`

Add `TestExplicitSkillActivationMessageNamesTheTurnContract` next to
`TestExplicitSkillActivationMessageCarriesNoToolVocabulary` (:296). Assert the
message text contains `this turn's contract` and `session's current mode`, and
that it still contains the skill body verbatim (so the sentence was prepended
to the instructions, not substituted for them).

Do not weaken or edit the existing banned-word test.

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1 -run 'TestExplicitSkillActivationMessage'`
→ both tests PASS.

### Step 5: Prove the fix did not move anything else

**Verify**: `go vet -tags fts5 ./pkg/run/` → exit 0.
**Verify**: `gofmt -l pkg/run/config.go pkg/run/skills.go pkg/run/config_test.go pkg/run/skills_test.go` → no output.
**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1 -run TestTestFilesCorrespondToProductionFiles` → PASS.
**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ ./pkg/agent/ ./pkg/skill/ -count=1` → all `ok`.
(These three packages hold the deliberately-untouched description, prompt
goldens and embedded skill assets; green here is the evidence that the
out-of-scope list was honored.)
**Verify**: `FOREBRAIN_HOME=scratch CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 30m` → exit 0, zero FAIL.
**Verify**: `git status --porcelain` → only the five in-scope files.

### Step 6: Update `plans/README.md`

Add a row for 368 immediately after the 367 row (currently line 2102), matching
the existing column order `| # | 标题 | 类别 | 状态 | 优先级 | 工作量 | 风险 |`,
and add one bullet at the top of `## 最新审核信息` recording this run, in the
style of the 2026-11-23 entries already there (baseline SHA, what was
investigated read-only, the root cause, what the plan does, and that no source
was changed by the advisor). Set the row's status to `DONE` with the gate
commands you actually ran and their results, or leave it `TODO` if you were
dispatched by a reviewer who maintains the index.

**Verify**: `grep -c "368-explicit-skill-turn-owns-its-mode" plans/README.md` → ≥ 2 (row + bullet link).

## Test plan

New tests, all in existing files:

- `pkg/run/config_test.go`: the four from Step 3 — gate fires on an
  explicit-skill turn in agent mode; negative control on an ordinary turn;
  plan mode with an explicit skill still gets the plan-mode reminder and
  `exit_plan_mode`; gate fires on the unreadable-mode-state path.
- `pkg/run/skills_test.go`: the one from Step 4 — the activation message names
  the turn contract and still carries the body.

Structural pattern to follow: `TestOfficeAgentModeRequestHasNoExitPlanMode`
and its three siblings (`pkg/run/config_test.go:1911-1983`) for the wrapper
tests; `TestExplicitSkillActivationMessageCarriesNoToolVocabulary`
(`pkg/run/skills_test.go:296-320`) for the message test.

Regression paths that must stay green unchanged:
`TestExplicitSkillActivationMessageCarriesNoToolVocabulary`,
`TestOfficeAgentModeRequestHasNoExitPlanMode`,
`TestOfficePlanModeRequestHasExitPlanMode`,
`TestOfficeModeReadFailureHidesExitPlanMode`,
`TestOfficeEmptySessionIDHidesExitPlanMode`, `TestOfficePlanPromptsGolden`,
`TestOfficeRuntimeRegistersNoEnterPlanMode` (`pkg/tool/session_tools_test.go:595`),
and the `pkg/agent` prompt goldens.

A mutation check worth doing before you report (cheap, and it is the evidence
that the new tests are load-bearing): temporarily comment out the gate's
`withoutTool` line, re-run the Step 3 command, and confirm tests 1 and 4 go
**red** while test 2 stays green. Then restore the line and confirm all four
are green again. Report both observations.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1` exits 0 with `ok`
- [ ] The five new tests exist and pass by name (`-run` output shows `--- PASS` for each, not `[no tests to run]`)
- [ ] Mutation check performed and reported: gate removed → tests 1 and 4 red, test 2 green; gate restored → all green
- [ ] `go vet -tags fts5 ./pkg/run/` exits 0
- [ ] `gofmt -l` over the four in-scope `.go` files produces no output
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ ./pkg/agent/ ./pkg/skill/ ./pkg/architecture/ -count=1` all `ok`
- [ ] `FOREBRAIN_HOME=scratch CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 30m` exits 0, zero FAIL
- [ ] `git status --porcelain` lists only `pkg/run/config.go`, `pkg/run/skills.go`, `pkg/run/config_test.go`, `pkg/run/skills_test.go`, `plans/README.md`
- [ ] `grep -n "enterPlanModeDescription" pkg/tool/session_tools.go` shows the description **unchanged** (`git diff --stat pkg/tool/session_tools.go` empty)
- [ ] `git diff --stat pkg/tool/state.go pkg/agent/ pkg/skill/ pkg/state/` is empty
- [ ] `plans/README.md` carries the 368 row and the run bullet

## STOP conditions

Stop and report back (do not improvise) if:

- The code at the locations in Excerpts A–E does not match (drift). Line
  numbers moving is fine; the *content* of `Execute`'s three early returns,
  `withoutTool`, `explicitSkillActivationFromContext`, or
  `explicitSkillActivationMessage` differing is not.
- `explicitSkillActivationFromContext` turns out to be unreadable from
  `config.go` (a build error naming it). It is same-package at
  `pkg/run/skills.go:521`; if that is no longer true, the gate needs a
  different seam and this plan's shape is wrong.
- You find that the activation is **not** present in the ctx that reaches
  `planModeLLM.Execute` during a real run. Excerpt B lists the three verified
  links (`runner.go:1631`, `agent.go:333`, `transcript.go:178`). If a real-run
  observation contradicts them, report it with the evidence — do not switch
  the gate to key on `explicitSkillSelectionFromContext` or on loaded-skill
  state without a ruling; that is a wider blast radius than what was approved.
- Any pre-existing test in `pkg/run`, `pkg/tool`, `pkg/agent` or `pkg/skill`
  fails. Green-before is assumed; a red baseline is the owner's to explain.
- `TestExplicitSkillActivationMessageCarriesNoToolVocabulary` fails after
  Step 2. That means your wording contains `tool`, `call` or `invoke`. Fix the
  wording; never edit the banned list.
- Making test 3 pass seems to require changing `buildPlanModeReminder`,
  `SinglePlanModePrompt`, or the office reminder format. Those are out of
  scope; report instead.
- Linking fails because `third_party/libtokenizers` or `third_party/pdfium` is
  missing. Run `make libtokenizers pdfium pdfium-fonts` once and report it; do
  not drop `-tags fts5` to make the build go.
- You are tempted to widen `isSanctionedPlanWrite` or `GuardWrite` so the skill
  can write its backlog *from inside* plan mode. That is a security regression
  and explicitly rejected; stop.

## Maintenance notes

- **The gate keys on "explicit activation", not on which skill it is.** That
  is deliberate and was ruled on by the owner (2026-11-23): a user naming a
  skill has already made the choice plan mode exists to surface, so no
  per-skill declaration is needed and third-party skills benefit with no
  change on their side. If a future skill genuinely needs plan mode *and* is
  invoked explicitly, the user's routes are the plan hotkey and `/plan`
  (`pkg/turn/executor.go:419-458`), both of which set the mode before the turn
  starts — so the gate is not in their way. Resist adding a frontmatter opt-out
  unless a real skill needs it; `plans/README.md` (2026-11-23, item ⑥ on plan
  356) already records the cost of letting one concept acquire three spellings.
- **Not covered, by decision:** a skill the *model* loads on its own initiative
  through the `skill` tool. That path has no explicit activation to key on, so
  the gate does not fire and the model can still enter plan mode mid-turn.
  Covering it would mean judging "is this a planning skill" from loaded-skill
  state, which is a much wider blast radius. Owner ruling 2026-11-23: record
  it, do not build it. If telemetry later shows model-loaded `audit` runs
  degrading into single-plan plan-mode turns, that is the trigger to reopen it.
- **Also not covered:** a session the user *already* put into plan mode before
  invoking the skill. There the confinement is correct and intended, and the
  built-in `audit` SKILL.md already tells the model to write only the session
  plan file. Test 3 pins that this plan leaves that behavior alone.
- **What a reviewer should scrutinize:** (1) that the gate sits *above* the
  three degenerate early returns in `Execute` — placing it inside
  `executeImplementationPhase` looks equivalent and is not, because that
  function returns early at four points (:906-921) and would leave the gate
  applying on some paths only; test 4 is the guard. (2) That the activation
  message sentence does not name a tool that is absent from the array.
  (3) That `enterPlanModeDescription` is genuinely untouched — it is the
  tempting "real fix" and is rejected for cache-prefix reasons, not oversight.
- **Interactions to watch:** plan 356 (`model-invocation: false`) adds a
  frontmatter axis for skill exposure; if it lands first, a skill hidden from
  the catalog can still be invoked explicitly by slash command, so the gate
  still applies to it — the two are orthogonal and need no coordination. Plan
  358 (`process` tool) and 361 (`skill-publish`) touch `pkg/tool`, not
  `pkg/run/config.go`. No plan in 353–367 has `pkg/run/config.go` or
  `pkg/run/skills.go` in scope, so 368 can be executed in any order relative to
  them; if a sibling lands first and moves `Execute`'s early returns, re-run
  the drift check rather than assuming the excerpts.

## Implementation Status

状态：**已执行并通过评审（APPROVE）**，2026-11-23。执行器在独立 worktree 落一笔提交，**未合并**（合不合并、何时合并由 owner 决定）。

- 落地位置：`.worktrees/plan-368-explicit-skill-gate`，分支 `plan-368-explicit-skill-gate`，基线 `e1abd2f4`（`dev` 尖），提交 `7ccfd239 fix(run): an explicit skill turn is not offered plan mode`（含 DCO `Signed-off-by`）。
- 漂移检查（派发前由 advisor 亲跑）：`git diff --stat 80bb73b4..HEAD -- pkg/run/{config,skills,config_test,skills_test}.go` **为空**；工作树当时干净。

### 复现与根因（advisor 自测，先于任何改动）

在一次性 worktree（已删除）里写 4 条对照测试跑未修改的树，`CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1 -run 'TestZZRepro368'`：

- `TestZZRepro368ExplicitSkillTurnHidesEnterPlanMode` **FAIL** — `tools offered: [write_file enter_plan_mode]`
- `TestZZRepro368ExplicitSkillGateAppliesWhenModeStateIsUnreadable` **FAIL** — 同上
- 普通轮次对照 PASS、plan 模式提醒用例 PASS

即症状＝显式技能轮次仍向模型提供 `enter_plan_mode`。六环因果链在 `e1abd2f4` 上逐环读过并经此跑验证：`session_tools.go:46-56` 为 coding 运行时装该工具 → `runner.go:1631` 的 `preloadExplicitSkills(ctx)` 返回的是 **ctx** 并交给 `main.Run(preloaded, parts...)`（:1637）→ `agent.go:206` 的 `Run` → `agent.go:333` 每轮 `tracedLLM.Execute(ctx, session, a.tools)` → `runner.go:1023` 把 `planModeLLM` 套在最内层，故激活 ctx 与完整工具数组同时到达。竞争解释已排除：`pkg/assembly` 等系统提示来源里没有第二处 \"plan mode\" 文本，邀请只随工具本身走。

### 落点

| Step | 落点 | 结果 |
|---|---|---|
| 0（编排偏差，见下） | 先写 Step 3/4 的五条测试取红 | 红：test 1、test 4、`…NamesTheTurnContract`；test 2、test 3、原 `…CarriesNoToolVocabulary` 绿 |
| 1 | `pkg/run/config.go` `planModeLLM.Execute` 第一条语句：`if _, ok := explicitSkillActivationFromContext(ctx); ok { tools = withoutTool(tools, "enter_plan_mode") }`，连同 10 行 why 注释；三个 fail-closed 早退与 office 摘除原样未动 | done |
| 2 | `pkg/run/skills.go` `explicitSkillActivationMessage`：加 \"The workflow those instructions describe is this turn's contract…\" 一句；doc 注释补 mode-contract 从句 | done |
| 3 | `pkg/run/config_test.go`：`TestExplicitSkillTurnHidesEnterPlanMode`、`TestOrdinaryTurnKeepsEnterPlanMode`、`TestExplicitSkillTurnInPlanModeStillGetsTheReminder`、`TestExplicitSkillGateAppliesWhenModeStateIsUnreadable`（`// --- explicit skill turns (plan 368) ---` 分组，紧跟 office 族） | done |
| 4 | `pkg/run/skills_test.go`：`TestExplicitSkillActivationMessageNamesTheTurnContract`（断言 `this turn's contract`、`session's current mode` 与技能正文逐字仍在）；原禁词测试未改 | done |
| 5 | 门禁复核 | 见下 |
| 6 | `plans/README.md` | 由 advisor 完成（执行器按派发指示跳过） |

### 评审亲跑的门禁（advisor 在 worktree 内自己跑，不采信执行器的报告）

| 命令 | 结果 |
|---|---|
| `-run 'TestExplicitSkillTurn\|TestOrdinaryTurnKeeps\|TestExplicitSkillGate\|TestExplicitSkillActivationMessage' -v` | 七条 `--- PASS`（含原 `IsEphemeral`／`CarriesNoToolVocabulary`），无 `[no tests to run]` |
| `./pkg/run/ -count=1` | `ok … 45.755s` |
| `go vet -tags fts5 ./pkg/run/` | exit 0，无输出 |
| `gofmt -l` 四个 in-scope 文件 | 无输出 |
| `./pkg/tool/ ./pkg/agent/ ./pkg/skill/ -count=1` | `ok` 19.658s／0.303s／13.376s |
| `git diff --stat e1abd2f4..HEAD` | 仅 4 个 in-scope 文件（+134/−2）；`git status --porcelain` 空 |
| `git diff --stat … pkg/tool/ pkg/agent/ pkg/skill/ pkg/state/` | 空；`enterPlanModeDescription` 仍在 `session_tools.go:348` 未改 |
| **变异检查** | 注释掉闸门那行 → test 1、test 4 红、test 2 绿；`git checkout` 还原 → 全绿、树干净 |
| `./... -timeout 30m`（`FOREBRAIN_HOME=$(mktemp -d)`） | 唯一红＝`pkg/architecture` 的 `TestTestFilesCorrespondToProductionFiles`，点名**与本计划无关的**四个既有 `pkg/tui` 探针测试文件（`deliverables_outcome_test.go` 由 `80bb73b4`、`zz_repro{,2,3}_test.go` 由 `e1abd2f4` 提交，均无同名生产文件）；其余全 `ok` |

### 偏差与判断（评审已接受）

1. **Step 0 与 Step 3/4 前移**：派发时授权先写测试取红再改生产代码，用红证据当 reproduction；原计划的 Step 1→3 顺序会先落修复、丢掉\"改前必红\"的证据。
2. **`pkg/architecture` 的既有红**：上述四个 `pkg/tui` 探针测试文件在基线 `e1abd2f4` 就已让该测试红，非本改动引入，也不在本计划 scope 内（同目录另一会话正在清理）。因此 done criteria 的这一条以\"唯一红、且红因在范围外\"结案。
3. **环境补齐（不改源码，均被 gitignore）**：worktree 的 `third_party/libtokenizers`、`third_party/pdfium` 软链到主检出；`pkg/gateway/dist` 只有 `.gitkeep`，`TestWebBuildLoadsNoExternalAsset` 需要构建产物，故从主检出复制。
4. **普通轮次的 pkg/run flake**：执行器第一次全量遇到 `TestSubagentToolResultsKeepTheirShape` 的 `t.TempDir` 清理竞态（`unlinkat … directory not empty`，无断言失败），隔离 `-count=10` 绿、advisor 复核整包绿，记为已知 macOS flake，未改任何代码。

### 后续（reviewer 需知道）

- 分支与 worktree **保留**：合并（或删除）由 owner 决定，本会话未 push、未合并、未碰 `dev`。
- 若要真机验证，可跑 `/run-forebrain` 以 `/audit` 开一轮，确认不再出现 `enter_plan_mode` 审批卡，且技能能把计划写到项目根 `plans/`。
