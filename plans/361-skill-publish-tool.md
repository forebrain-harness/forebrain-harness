# Plan 361: The model can ask to publish a skill draft — `skill_publish`, gated on an in-place approval

> **Executor instructions**: Follow this plan step by step. Run every verification
> command and confirm the expected result before moving to the next step. If
> anything in the "STOP and report" section occurs, stop and report — do not
> improvise. When done, update the status row for this plan in `plans/README.md`.
>
> **Drift check (run first)**:
> `git status --porcelain -- pkg/run pkg/tool` and
> `git diff --stat 6541b8ab -- pkg/run pkg/tool`.
> A `6541b8ab..HEAD` range diff is **always empty here** (the baseline is HEAD);
> use the two commands above. If an in-scope file moved, compare the "Current
> state" anchors against the live code before proceeding; on a mismatch, STOP.

## Status

- **Category**: feature / skill system
- **Priority**: P2
- **Effort**: S
- **Risk**: MED (a model-initiated write into the repository's skill tree)
- **Depends on**: **356** (the `model-invocation: false` gate) and **358** (it
  spends the `pkg/tool` file budget the batch is rationing). Plan 360 adds a file
  to `pkg/run` before this one runs, so `pkg/run` is **33** production files when
  you start, not 32.
- **Closes**: gap 5 from the 2026-11-22 tool audit
- **Planned at**: commit `6541b8ab`, 2026-11-23; wiring corrected at the same
  commit by `review-plan plans/353-362` (the original assumed a `skill.Service`
  on `pkg/run`; there is none — see Current state).

## Why this matters

`PublishSkill` exists and works — `pkg/skill/service.go:824`, with
`validateDraftForPublish` at `:852`, `enableSkillDir` at `:841` and the refresh at
`:844-846`. It has **two** callers, both user-driven: the TUI
(`pkg/tui/skills_versions.go:141`, whose comment at `:134-136` calls it "the one
operation that writes the formal directory") and the gateway's HTTP API
(`pkg/gateway/skill_versions.go:87`). Review 2026-11-23 corrected an earlier
"exactly one caller: the TUI" claim — the gateway already publishes with no TUI
involved, which also answers the STOP below about TUI-only side effects.

So every skill-authoring flow in forebrain ends the same way: the model writes a
draft under `.drafts/`, reports that it is inert, and stops. Plans 353/354 encode
this as a rule — `run-skill-generator`'s asset says **"Never publish it
yourself."** That is the correct rule for what exists today, and it is also a
dead end: the user has to leave the conversation, find the skills panel, locate
the draft, and publish it, with no guarantee the model's report described the
right one.

The fix is not to let the model publish. It is to let the model **ask**, through
the approval mechanism forebrain already has for exactly this shape of
question:

```go
// pkg/tool/state.go:2002-2007
// InlineApprovalHook answers one of the primary agent's own approval gates
type InlineApprovalHook func(ctx context.Context, rae *RequiresActionError) (context.Context, error)
```

A tool raises `RequiresActionError`, the user answers where the call is, and
`ErrApprovalRejectedStop` (`pkg/tool/inline_approval.go:11-16`) already defines
what a wordless refusal does. Nothing new is invented here.

Two properties make this safe rather than merely convenient:

- **The model cannot do it through `shell`.** The sandbox denies every command
  write under `.forebrain` (`pkg/tool/git_worktrees.go:23-25`), where skills
  live. This tool is the only path, and it is the gated one.
- **Publishing is a versioned, validated operation**, not a file copy:
  `validateDraftForPublish` runs, `VersionStore.Publish` (`pkg/skill/versions.go:364`)
  cuts a version, and a bad draft is refused by code the TUI already relies on.

## Current state

Anchors verified at `6541b8ab`:

| File | Line | Role |
|---|---|---|
| `pkg/skill/service.go` | `:824` | `func (s *Service) PublishSkill(name, note string) (int, error)` |
| `pkg/skill/service.go` | `:852` | `validateDraftForPublish(store, name)` |
| `pkg/skill/service.go` | `:841-846` | `enableSkillDir` (`:841`) + refresh after publish (`:844-846`) |
| `pkg/skill/versions.go` | `:364` | `VersionStore.Publish(name, note)` |
| `pkg/tui/skills_versions.go` | `:134-141` | one of **two** callers (`ChatSession.PublishSkill` → `skillLifecycleService().PublishSkill`) |
| `pkg/gateway/skill_versions.go` | `:87` | the other caller: the HTTP API publishes with no TUI involved |
| `pkg/run/runner.go` | `:1348-1356` | **the service this plan calls**: `Load` already builds `skill.NewServiceForWorkspace(r.Home, r.workspaceRoot())` (`:1348`), sets `ProjectRoot`/`OnRefresh`/`IsBuiltin`, and `Refresh()`es it (`:1354-1356`). It is a local variable today |
| `pkg/run/runner.go` | `:1301` | `_ = RegisterSkillTool(a, r.tools)` — where skill tools are registered, **before** the service exists |
| `pkg/run/skills.go` | `:171-208` | `RegisterSkillTool` — the registration pattern: `llm.NewTool`, `st.GuardTool(ctx, "skill")` at **`:186`**, the byte-identical refusal at `:191`, the pinned-copy comment at `:193-194`, `"skill_name"` in the output map at **`:202`** |
| `pkg/tool/state.go` | `:2002-2007`, `:320` | `InlineApprovalHook` and `ApprovedActionIDFromContext` |
| `pkg/tool/inline_approval.go` | `:11-16` | `ErrApprovalRejectedStop`, and the one-question-at-a-time slot |
| `pkg/tool/file_tools.go` | `:324` | an exemplar of building `&RequiresActionError{...}` for a gate (see also `shell_tool.go:242`, `session_tools.go:409`) |
| `pkg/tool/artifacts.go` | `:477-482` | `PrimaryOnlyToolName` — the existing "primary agent only" predicate |
| `pkg/tool/permissions.go` | `:864` | `GuardTool` refuses a primary-only tool inside a subagent |
| `pkg/run/subagent.go` | `:636` | a subagent's tool set is filtered by `tool.PrimaryOnlyToolName` |
| `pkg/run/permissions.go` | `:903` | `r.ProjectType() == state.ProjectTypeBatch` — the batch predicate, available from `pkg/run` |
| `pkg/skill/catalog.go` | `:47-53` | the catalog is frozen once per session |

**The wiring fact the original version of this plan got wrong.** `pkg/run` has no
`skill.Service` field — `SkillCommandHooks` (`pkg/run/skills.go:585`) carries two
callbacks and nothing else. The service is constructed inside `Runner.Load` at
`pkg/run/runner.go:1348`, **after** the tool registration at `:1301`. So the tool
must be registered from that later point, with the service passed in; do not add a
`Service` field to `Runner` and do not reach for `pkg/tui`'s `ChatSession`.

## Scope

**In scope**: `pkg/run/skills.go` (the tool), `pkg/run/runner.go` (the
registration line, after `:1348`), their tests (`pkg/run/skills_test.go` or
`pkg/run/runner_test.go` where the existing registration tests live), and
`pkg/tool/artifacts.go` **plus its test** for the one-line
`PrimaryOnlyToolName` extension (Step 3). **This plan adds zero files** —
`pkg/run` is at 32 today but plan 360 makes it 33; do not spend the last slot.

**Out of scope — do not touch**

- `pkg/skill/service.go`, `versions.go`. The publish operation is correct and
  the TUI depends on it. Call it; do not change it.
- `pkg/tui/skills_versions.go`. The TUI path stays exactly as it is.
- Adding a `Service` field to `Runner` or changing `Runner.Load`'s tool
  registration order. The service already exists at `:1348`; use it there.
- `pkg/skill/system_assets/`. Plan 354's "Never publish it yourself" wording is
  a **follow-up**, not an edit here — see Maintenance.
- Unpublishing, deleting, or editing a published version. Publish only.

## Design decisions the executor must not re-litigate

**1. It asks; it never decides.** The handler raises the approval gate before
calling `PublishSkill`. There is no configuration in which it publishes without
an answer, and no `--yes` equivalent. If `InlineApprovalHookFromContext` is nil
(no surface available to ask), **refuse** — do not fall back to publishing, and
do not fall back to silently succeeding.

**2. Primary agents only.** A subagent has its own hook
(`SubagentApprovalHook`, `state.go:2000`) but a subagent installing a skill into
the user's repository is a surprise nobody asked for, and the subagent's
approval surface is the parent agent, not the user. Withhold the tool from
subagents the way `registry.go:216-220` withholds the ask-the-user tools from
batch conversations.

**3. Not registered for batch conversations.** A batch task runs unattended;
`batchProject`'s own comment says the tools that exist to talk to a person are
not offered at all because "the model cannot ask a question nobody will answer."
This is that case exactly.

**4. The result must say it is not visible yet.** The catalog is frozen for the
session (`catalog.go:47-53`) and a skill's pinned copy is what the session loaded
(`pkg/run/skills.go:193-194` — "a version published after this session started is
the next session's skill"). A publish that reports bare success teaches the model
to claim the skill is now usable. The result text must name the version
**and** state that it takes effect next session.

**5. One skill per call, named explicitly.** No batch publish, no
"publish everything under `.drafts/`". The approval prompt must show the user
exactly one name and one note.

## Git workflow

- Branch: `advisor/361-skill-publish-tool`
- Conventional Commits, lowercase imperative, optional `(scope)`, matching recent
  history (`feat(skill): a run skill that drives the app through the shell`). Use
  e.g. `feat(skill): ask before publishing a skill draft`.
- Do NOT push and do NOT open a PR unless the operator instructed it.
- Dispatched into an isolated workspace, your branch lives in
  `.worktrees/<short-kebab-name>` under the project's main worktree; never push
  or merge into the user's branch.

## Steps

### Step 1: Read the two mechanisms you are joining

Before writing anything, read `pkg/run/runner.go:1295-1360` (tool registration,
then the `skill.NewServiceForWorkspace` block the tool will be handed) and
`pkg/tool/state.go:1985-2010` (both approval hooks and `RequiresActionError`).
Then find one existing tool that raises an approval gate and read it end to end:

```bash
grep -rn 'RequiresActionError{' pkg/tool/*.go | grep -v _test | head
```

Copy that tool's shape — how it builds the error, what it puts in the prompt,
how it detects an already-approved replay via `ApprovedActionIDFromContext`
(`state.go:320`; `shell_tool.go:626` shows the pattern). Do not invent a second
convention.

**Verify**: you can name the `file:line` of the exemplar in your report.

### Step 2: The tool

In `pkg/run/skills.go`, beside `RegisterSkillTool`:

```go
const skillPublishToolDescription = "Ask the user to publish one skill draft as its next version. " +
	"Raises an approval the user answers in place; nothing is written if they refuse. " +
	"A published skill takes effect in the next session, not this one."

func RegisterSkillPublishTool(a tool.ToolAdder, st *tool.State, svc *skill.Service) error
```

The third parameter is the service `Load` already builds — take it by argument,
do not invent a field for it.

Input:

```go
type SkillPublishInput struct {
	Name string `json:"name" jsonschema:"description=The exact skill name whose draft to publish"`
	Note string `json:"note" jsonschema:"description=One line for the version history, shown to the user in the approval"`
}
```

Handler order — each step matters:

1. `st.GuardTool(ctx, "skill_publish")`.
2. Trim and require both fields; an empty `note` is refused, not defaulted,
   because the note is what the user reads in the approval prompt.
3. If `svc` is nil, return the same byte-identical unavailability register
   `RegisterSkillTool` uses (`pkg/run/skills.go:191`) — a missing service and a
   missing skill should not be distinguishable.
4. Confirm a draft exists for `name` **before** asking, without creating one.
   `svc.Inspect(name)` (`pkg/skill/service.go:162`) fails for an unknown skill;
   the draft itself is the directory `VersionStore` uses —
   `<versioned root>/.drafts/<name>/SKILL.md` (`pkg/skill/versions.go:208-210`),
   over `skill.VersionedSkillRoots(...)` (`pkg/skill/versions.go:837-845`).
   Refuse when that file is absent, with the same refusal register. Prompting the
   user to approve a publish that `validateDraftForPublish` will then refuse
   wastes their attention and teaches them to refuse.
5. Raise the approval gate. The prompt carries: the skill name, the note, the
   draft's path, and the line "takes effect next session."
6. On approval, call `svc.PublishSkill(name, note)`.
7. Return the version number plus decision 4's sentence.

On `ErrApprovalRejectedStop` let it propagate — `orchestration_llm.go:2088-2092`
(`deniedToolResult`) already turns a refusal into `tool.RejectedStopText` and ends
the turn; `:2363` (`rejectionStoppedResult`) is the `not_run` helper for the calls
queued behind the refused one (`rejectionStoppedResult`, `:2358`).

**Verify**: `go build ./pkg/run/...` → exit 0; `gofmt -l pkg/run` → clean.

### Step 3: Register it where the service exists, and withhold it where nobody can answer

In `pkg/run/runner.go`, register the tool **after** the service block at
`:1348-1358` (after `svc.Refresh()`), not beside `:1301` — at `:1301` there is no
service to pass:

```go
if r.ProjectType() != state.ProjectTypeBatch {
	_ = RegisterSkillPublishTool(a, r.tools, svc)
}
```

- **Batch conversations** get nothing: `r.ProjectType()` is the same fact
  `pkg/tool`'s `batchProject` reads (`pkg/tool/registry.go:216-220`), and a batch
  task cannot ask a question nobody will answer (`pkg/run/permissions.go:903`
  already branches on it).
- **Subagents** get nothing: a subagent's tool set is filtered by
  `tool.PrimaryOnlyToolName` (`pkg/run/subagent.go:636`), and `GuardTool`
  refuses a primary-only tool inside a subagent as a second line
  (`pkg/tool/permissions.go:864`). Extend that predicate in
  `pkg/tool/artifacts.go:480-482` to also name `skill_publish`; that is the
  existing signal, not a new one. **Update its doc comment (`:477-479`) in the
  same edit** — it currently explains only `present_files` ("the deliverable
  action speaks for the conversation"); a second name with no explanation leaves
  the next reader guessing. One clause naming the skill-publish reason is enough.

**Verify**: tests asserting the tool is absent from a batch conversation's tool
set and from a subagent's, and present for a primary coding agent.

### Step 4: Tests

`pkg/run/skills_test.go`, following the existing `RegisterSkillTool` tests in
that file:

1. No draft for the name → refused **before** any approval is raised (assert the
   hook was never called).
2. Draft exists, hook approves → `PublishSkill` called with the exact name and
   note; result contains the version number and "next session".
3. Hook returns `ErrApprovalRejectedStop` → propagates; `PublishSkill` never
   called.
4. Hook is nil → refused; `PublishSkill` never called.
5. Empty `note` → refused before the hook.
6. `skill.Service` nil → the same unavailability text as `RegisterSkillTool`'s
   missing-skill case (assert byte-identity, the way plan 356 does).
7. Batch conversation → tool not registered.
8. Subagent → tool not registered.
9. An approved replay (`ApprovedActionIDFromContext` set) does not ask twice.

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run/... -count=1` → PASS.

### Step 5: Full gates

1. `gofmt -l pkg/run pkg/tool` → no output
2. `go vet ./pkg/run/... ./pkg/tool/...` → exit 0
3. `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/... -count=1` → PASS, and
   `ls pkg/run/*.go | grep -v _test | wc -l` is **33** (unchanged by this plan —
   no new file; 32 before plan 360 landed, 33 after)
4. `make test` → PASS
5. Live, in a real session: have the model draft a trivial skill via
   `/skill-generator`, then ask it to publish. Confirm the approval prompt
   appears in place, that approving publishes, that the TUI's skills panel shows
   the new version, and that the model's report says it is not usable this
   session. Then repeat and refuse, and confirm nothing was written
   (`git status` clean under `.forebrain/skills`).

## Done criteria

- [ ] `grep -n "RegisterSkillPublishTool" pkg/run/skills.go pkg/run/runner.go` finds the definition and the call
- [ ] `ls pkg/run/*.go | grep -v _test | wc -l` → 33; `ls pkg/tool/*.go | grep -v _test | wc -l` → **34** when plan 358 has landed (it adds `process_tool.go`, taking the last slot), or **33** if you are executing 361 before 358. This plan's own `artifacts.go` edit changes no count either way — record which order you ran in
- [ ] `git diff --stat pkg/skill/ pkg/tui/` is **empty**
- [ ] The result text contains both the version and "next session" — `grep -n "next session" pkg/run/skills.go`
- [ ] Refusal path: svc nil, no draft, empty note, batch, subagent — all five refuse **without** calling `PublishSkill`
- [ ] All 9 tests present and passing
- [ ] The Step 5.5 live transcript (approve **and** refuse) is in the report
- [ ] `make test` green, `gofmt -l` clean, `go vet` exit 0

## STOP and report instead of improvising

- `svc.Refresh()` at `pkg/run/runner.go:1354-1356` turns out to need a TUI-side
  callback (`r.SkillCommands.Refresh`) that is nil on a non-TUI surface, and the
  publish path depends on it. Report what is nil and which surface. (The gateway
  already calls `PublishSkill` without a TUI, so this is unlikely — verify rather
  than assume.)
- `PublishSkill` turns out to have a side effect that assumes a TUI is present
  (a notification, a refresh that needs a session object). Report it —
  `:841-846`'s enable + "refresh after publish" is the likely place.
- Raising `RequiresActionError` from a tool registered in `pkg/run` does not
  reach the inline approval hook (every current raiser lives in `pkg/tool`).
  Report the coupling rather than working around it.
- Withholding from subagents cannot be done through `tool.PrimaryOnlyToolName`
  without also changing what a subagent may do with `present_files`. Report the
  interaction instead of widening the predicate.

## Maintenance notes

- **Plan 354's asset wording becomes wrong after this lands, in one specific
  sentence.** `run-skill-generator/SKILL.md` says "Never publish it yourself."
  With an approval-gated tool, the right instruction is "ask the user to publish
  it; never publish it without an approval." Write that as a small follow-up plan
  and **do not edit the asset here** — plan 354 pins byte-level counts
  (`Never publish it yourself` = 1) and your edit would fail its own test.
- **The same follow-up should reconsider plan 353's closing restraint clause.**
  If the model can ask to publish, the recommendation it makes at the end of a
  `run` session can offer to do the whole thing rather than only naming a
  command.
- **The approval prompt is the security boundary.** Everything else is
  convenience. If a future change makes the prompt terser, or adds a "remember
  this choice" that persists across skills, that is a widening of what a model
  can install into a repository — treat it as a security review, not a UX tweak.
- **`skill_publish` joins the tool array**, which is in the cached prefix
  (`pkg/skill/catalog.go:43-47`). One entry, registered unconditionally for
  eligible conversations so the array's shape does not depend on the skill set —
  the same reasoning `RegisterSkillTool`'s comment gives at `pkg/run/skills.go:159-170`.
