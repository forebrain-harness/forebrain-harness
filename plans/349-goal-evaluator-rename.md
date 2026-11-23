# Plan 349: One name for the goal evaluator — a repository-wide mechanical rename

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)** — baseline `3f04228e` (branch
> `345-tui-delete-configured-model`, the tip plan 348 landed on). Working
> tree expected clean except for four pre-existing deletions under
> `.claude/skills/improve/` that are **not** yours: leave them untouched.
>
> ```bash
> git status --porcelain -- pkg/run pkg/tui pkg/config pkg/gateway pkg/turn \
>   pkg/agent pkg/process pkg/state frontend/src frontend/e2e
> git diff 3f04228e -- pkg/run/goal.go pkg/run/subagent_catalog.go \
>   frontend/src/lib/forebrainGatewayRuntime.ts frontend/src/locales/index.ts
> ```
>
> If any in-scope file changed, compare the "Current state" excerpts against
> the live code before proceeding. **Content mismatch is a STOP condition;
> moved line numbers alone are not** — re-locate by the quoted symbol/text.

## Status

- **Priority**: P2
- **Effort**: L (mechanical, 25 Go files + 20 frontend files + 2 prototypes; almost no design)
- **Risk**: LOW (no persisted id, no wire value and no user-visible copy
  changes — every value stays as it is today; the only prototype edits are
  two prototypes, one tracked and one owner-local (§3), whose gate is re-run
  and re-frozen)
- **Depends on**: none. Plan 347/348 already landed on this HEAD; they
  introduced the `goal evaluator` vocabulary this plan propagates.
- **Category**: tech-debt (naming)
- **Planned at**: `3f04228e`, 2026-11-22
- **Executed**: 2026-11-22 → **APPROVE**. Worktree
  `.worktrees/goal-evaluator-rename` (branch `goal-evaluator-rename`, base
  `3f04228e`), commits `758ab6b2` (Go) + `54d21580` (frontend); 45 files
  changed, 5 renames. Landing: first ff `3f04228e..54d21580` on
  `345-tui-delete-configured-model`, then the owner's 「合并回主树dev分支」 →
  rebased onto `dev` and fast-forwarded `a207700f..fb4fd294` (20 commits;
  the two 349 commits are now `20b7c2d0` + `fb4fd294`; the landed tree is
  byte-identical to the reviewed one). One rebase conflict: dev's new
  `TestGoalCheckEndIsAwaitedByDispatcher` was renamed to
  `TestGoalEvaluatorEndIsAwaitedByDispatcher` with its `goalCheck{}` /
  `GoalCheckSubagentType` uses; dev's `withSubagentAwaited(...)` inside
  `runGoalEvaluator` auto-merged. Gates were re-run on the rebased tree
  (not reused): full `./...` 45 packages, zero FAIL — the first attempt hit
  ENOSPC (linker errno 28) and passed after clearing the go-build/gopls
  caches per the disk-reclaim skill; gofmt silent, vet exit 0, frontend
  typecheck / 1579 vitest / build, renamed e2e 2/2 + `settings-models` 7/7.
  Main-tree smoke after the ff: `git diff fb4fd294` empty, gofmt silent,
  `go build ./...` exit 0, typecheck 0. Landing worktree/branch removed;
  `345-tui-delete-configured-model` still points at the pre-rebase
  `54d21580` and is now redundant. Not pushed (dev is 21 ahead of
  `origin/dev`).
  **Reviewer's own run**: `go build -tags fts5 ./...` exit 0; nine-package
  gate all `ok`; `gofmt` silent; `go vet` exit 0; `pnpm typecheck` 0;
  vitest 195 files / 1579 tests; `pnpm build` exit 0; renamed e2e 2/2 and
  `settings-models` 7/7 `web e2e: PASS`; prototype regression at the same
  known Ctrl+U stop (an instrumented copy showed the six checks before it
  all pass). Rename map applied to `3f04228e` content reproduces every
  changed file byte-for-byte except the documented judgment edits and one
  gofmt realignment. Prototype copy installed in the main worktree, sha256
  `b22e4ddb266fa49350273c40b1f55e98e66da41b931a36008656f3a4bf22e3a5`.
  **Deviations accepted**: two commits instead of one per step (Step 3's
  typecheck cannot pass before Step 5's file renames); `/goal checks` →
  `/goal evaluations` in two comments; bare "the check" → "the evaluator"
  in `pkg/run/goal.go`'s four doc comments; fixture `llm-goal-check` →
  `llm-goal-evaluator`; ~12 comment lines now 81–86 columns (no line-width
  gate); the local `checking` parameter kept. **Not run**: full `./...`,
  real-machine TUI, `settings-prototype-parity` (needs the owner's
  gitignored original).

## Why this matters

On 2026-11-22 the owner ruled that the system subagent that judges a `/goal`
round is called **goal evaluator** in English and **目标评估** in Chinese. Plan
347 renamed the pin vocabulary (`config.ModelPinGoalEvaluator`,
`models.pinGoalEvaluator`, the English `models.deleteMessage`), and
`pkg/run.GoalCheckSubagentType` has always carried the *id*
`"goal-evaluator"`. Everything else still says **goal check**: 178 hits in 25
Go files, 210 in 20 frontend files, plus old Chinese wording in two tracked
prototypes. Two names for one thing is how a codebase drifts: the next reader
has to learn that `GoalCheckSubagentType`, `goalCheck`, `subagents.goalCheck.*`,
`GoalCheckModelGroup.vue`, `data-goal-check-*` and `FOREBRAIN_GOAL_CHECK_AGENT_TYPE`
all mean the evaluator, and a search for the real name misses them.

After this plan the repository has exactly one spelling of the name. **No
behaviour changes**: the agent-type id stays `goal-evaluator`, no config key,
no database value, no API field and no user-visible sentence changes (the copy
already reads 目标评估 / goal evaluator everywhere the user sees it — verified:
`grep -rn 目标检查` returns nothing in `cmd pkg frontend`). This is a rename,
not a copy revision; wording that is merely *about* the act of checking
(`checkAgentId`, `openCheck`, `goalRound`, `verdict`, `/goal 每轮结束后检查目标是否达成`)
stays as it is.

## Current state

Everything below was read at `3f04228e`. Counts are grep hits of
`GoalCheck|goalCheck|goal_check|goal check|goal-check`, line numbers anchors.

### 1. Go — 178 hits, 25 files, all internal

Measured with the exact pattern the done criteria use
(`GoalCheck|goalCheck|goal_check|goal check|goal-check`); `git grep -c`:

| file | hits | what |
|---|---|---|
| `pkg/run/goal_test.go` | 67 | identifier uses + `func TestGoalCheck…` ×8 + fixture literals `llm-goal-check-row`, `sk-goal-check` |
| `pkg/tui/reducer.go` | 18 | `Counts.GoalChecking`, `Tracker.goalChecking`, `ObserveGoalCheck`, `withGoalCheck` |
| `pkg/tui/config_panel_test.go` | 18 | test struct field `GoalCheck map[string]any \`json:"goal_evaluator"\`` + identifier uses |
| `pkg/run/goal.go` | 17 | const + type + error strings (below) |
| `pkg/run/subagent.go` | 7 | `runGoalCheck`, type compare |
| `pkg/run/subagent_catalog.go` | 7 | catalog field `goalCheck` |
| `pkg/config/agents.go` | 7 | comments |
| `pkg/config/agents_test.go` | 7 | comments + the `llm-goal-check…` fixture + a `t.Fatalf` message |
| `pkg/tui/reducer_test.go` | 5 | |
| `pkg/tui/config_panel.go` | 3 | comments (`configGoalEvaluatorKey` already says Evaluator) |
| `pkg/gateway/api_extra.go` | 3 | comments |
| `pkg/gateway/api_extra_test.go` | 3 | comments |
| `pkg/state/schema_migrations_test.go` | 3 | **comments only** |
| `pkg/gateway/subagents_api.go` | 2 | comments |
| `pkg/turn/submit.go` | 1 | comment |
| `pkg/tui/run.go` | 1 | `case counters.GoalChecking:` |
| `pkg/tui/config_panel_schema.go` | 1 | comment |
| `pkg/state/schema_migrations.go` | 1 | **comment only** — a migration's identity must not move |
| `pkg/run/supervisor.go` | 1 | `goalCheck{runner: …}` |
| `pkg/run/subagent_catalog_test.go` | 1 | |
| `pkg/process/llm_models.go` | 1 | comment |
| `pkg/process/config_defaults_test.go` | 1 | comment |
| `pkg/gateway/models_api.go` | 1 | comment |
| `pkg/config/load.go` | 1 | comment (`// The settings page's goal-check row is global…`, `:199`) |
| `pkg/agent/subagent_defs.go` | 1 | comment |

The table counts lines (`git grep -c`); by spelling occurrence the Go tree
holds 58 `GoalCheck`, 64 `goalCheck`, 61 `goal check` and 11 `goal-check`, and
all four must go. The hyphen form is six comments plus the test fixture
literals `llm-goal-check-row` and `sk-goal-check` — those are names, they
change too.

**Five identifiers are easy to miss** because they are neither the type nor the
constant, and the first sweep in Step 1 below must name them explicitly:
`TestGoalCheck…` (eight functions in `pkg/run/goal_test.go`),
`dispatchGoalCheckPin` (5 uses), `stubGoalCheckEffortError` (4 uses), the bare
test field `GoalCheck map[string]any` (3 uses in `pkg/tui/config_panel_test.go`,
its `json:"goal_evaluator"` tag stays as it is), and `goalCheckConfig`.

The four definitions that matter (`pkg/run/goal.go`):

```go
// GoalCheckSubagentType is the reserved definition the goal check runs as.
const GoalCheckSubagentType = "goal-evaluator"

const goalCheckRecentRounds = 3

type goalCheck struct { runner *Runner }
func (c goalCheck) Evaluate(ctx context.Context, objective string, rounds []RoundOutcome) (Verdict, error) {
	if c.runner == nil {
		return Verdict{}, errors.New("the goal check needs a runner")
	}
	...
}

func goalCheckTask(objective string, rounds []RoundOutcome) string { ... }
```

and the four English error strings it words:

- `"the goal check needs a runner"`
- `"the goal check ended without a verdict: %s"`
- `"the goal check's verdict is not valid JSON: %w"`
- `"the goal check's verdict has no status it can act on: %q"`

No test asserts those four sentences. (The phrase `the goal check` does appear
in eight `t.Fatal` messages across `pkg/run/goal_test.go` and
`pkg/tui/config_panel_test.go`; those are failure text and change with the
rename in Step 2 — they are not assertions on the production wording.)

**The value is already right**: `GoalCheckSubagentType = "goal-evaluator"`, so
renaming the identifier changes no stored, configured or transmitted id. Same
for `pkg/run/subagent_catalog.go:217` (`subagentKey(name) == GoalCheckSubagentType`).

### 2. Frontend — 191 hits in 16 files by that pattern; 210 in 20 files once `GOAL_CHECK` and `goal check` are counted

The extra four files are in the union only through the uppercase constant and
prose: `frontend/src/lib/forebrainGatewayRuntime.ts` (1, the constant),
`frontend/src/views/ChatView.vue` (2, uses it), `frontend/src/lib/api.ts` (1,
comment `plan review or the goal check pins by id`),
`frontend/src/components/settings/SettingsSubagentsTab.vue` (2, comments).

| file | hits | what |
|---|---|---|
| `frontend/e2e/settings-goal-check-model.spec.ts` | 49 | file name, `data-group="goal-check-model"`, `data-goal-check-*`, screenshot names, `sk-e2e-goal-check-0001` |
| `frontend/src/components/settings/__tests__/GoalCheckModelSelect.spec.ts` | 33 | file name + all of the above |
| `frontend/src/locales/index.ts` | 30 | key namespace `subagents.goalCheck.*` (15 keys × 2 languages) + comments |
| `frontend/src/components/settings/GoalCheckModelGroup.vue` | 21 | file name, `id`/`data-group`/`data-goal-check-*`, `t('subagents.goalCheck.*')` |
| `frontend/src/components/settings/GoalCheckModelGroup.test.ts` | 17 | file name + identifiers |
| `frontend/src/components/settings/GoalCheckModelSelect.vue` | 9 | file name, `data-goal-check-*`, `t('subagents.goalCheck.*')` |
| `frontend/src/composables/useChatStream.ts` | 7 | `FOREBRAIN_GOAL_CHECK_AGENT_TYPE`, `setGoalChecking` |
| `frontend/src/components/settings/PlanReviewerModelGroup.test.ts` | 7 | imports the component; comments call the reviewer "the goal check's sibling" |
| `frontend/src/components/settings/GlobalSubagentsGroup.vue` | 4 | imports both components |
| `frontend/src/composables/useChatStream.test.ts` | 3 | `runtimeStatus.goalChecking` |
| `frontend/src/components/settings/GlobalSubagentsGroup.test.ts` | 3 | imports |
| `frontend/src/components/chat/GoalLine.vue` | 3 | CSS class `.goal-check` (the button that opens the evaluator's conversation) |
| `frontend/src/components/settings/PlanReviewerModelGroup.vue` | 2 | imports |
| `frontend/src/lib/testdata/config-db-settings-preview.html` | 1 | `data-grp="goal-check-model"` (see §3) |
| `frontend/src/lib/settingsSchema.ts` | 1 | comment |
| `frontend/src/components/settings/SettingsSubagentsTab.test.ts` | 1 | the `data-group` selector |
| `frontend/src/lib/forebrainGatewayRuntime.ts` | 1 (union only) | `export const FOREBRAIN_GOAL_CHECK_AGENT_TYPE = 'goal-evaluator'` |
| `frontend/src/views/ChatView.vue` | 2 (union only) | imports and compares the constant |
| `frontend/src/lib/api.ts` | 1 (union only) | comment |
| `frontend/src/components/settings/SettingsSubagentsTab.vue` | 2 (union only) | comments |

The 15 locale keys (`frontend/src/locales/index.ts`, zh block at `:717-731`,
en block at `:3300-3314`): `title`, `modelLabel`, `effortLabel`,
`effortFollowRow`, `effortReset`, `hintRowDefault`, `hintRowNone`,
`hintOverridden`, `search`, `searchLabel`, `empty`, `clear`, `discard`,
`save`, `saved`. **Their values are already right** (`'/goal 评估模型'`,
`'/goal evaluator model'`, `'…仅影响目标评估子代理'`) — only the namespace moves.

The ten test hooks drawn by the two components: `data-goal-check-model`,
`-clear`, `-search`, `-option`, `-effort`, `-hint`, `-reset`, `-save`,
`-saved`, `-discard`, plus the container `data-group="goal-check-model"` and
the group's own `id: 'goal-check-model'`
(`GoalCheckModelGroup.vue:116`).

Four code comments still call it 「/goal 检查模型」 and no spelling-based grep
finds them: `frontend/src/locales/index.ts:715`,
`frontend/src/components/settings/GoalCheckModelSelect.vue:2`,
`frontend/src/components/settings/GoalCheckModelGroup.vue:2`,
`frontend/src/lib/settingsSchema.ts:26`. They change to 目标评估 in Step 5.

### 3. Two prototypes still say the old name

- `frontend/src/lib/testdata/config-db-settings-preview.html` (the tracked
  copy of the owner's settings-page prototype, read by
  `frontend/src/lib/settingsSchema.test.ts` as the acceptance standard):
  line 382 `/* 子代理 Tab 内的「/goal 检查模型」分组沿用 .grp / .row 版式 */`,
  line 643 `/* 「/goal 检查模型」区块的数据 */`,
  line 698 `…仅影响目标检查子代理`,
  line 702 `data-grp="goal-check-model"` (the only ASCII hit the greps see),
  line 703 `<h3>/goal 检查模型</h3>`,
  line 733 `/* 「/goal 检查模型」区块的交互 */`.
  **Line 645 (`when:'/goal 每轮结束后检查目标是否达成'`) is the verb — do not touch it.**
- `plans/config-tui-prototype.html` line 245, inside the `scene(...)` note for
  `global-plan-reviewer-effort`: `候选与 goal check 同款 enum（none/minimal/…）`.
  Its regression is `plans/config-tui-prototype.test.cjs`; the frozen sha256 is
  recorded in `plans/README.md` (2026-11-21 entry: `ad6277ab…`), not in the test.

`plans/` is owner-local and gitignored (`.gitignore:77`, "Plan documents are
owner-local, never committed"), so `plans/config-tui-prototype.html` and its
test are **absent from a fresh worktree**. That one is edited on a copy inside
the worktree (Step 8 copies both files in from the main worktree) and handed
back: the reviewer installs it in the main worktree and records the new
sha256 — an executor never writes the main worktree's `plans/`.

`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/config-db-settings-preview.html`
(the owner's own prototype, gitignored, already divergent from the tracked
copy) was **out of scope** here — the owner syncs it by hand. **Synced
2026-11-22 at the owner's instruction**: backed up as
`…html.bak-20261122T100415`, then overwritten with the tracked copy's current
bytes (sha256 `764c53c4…`); `e2e/settings-prototype-parity.spec.ts` then ran
3/3 `web e2e: PASS`.

### 4. Keep these (they name the act, not the subagent)

`CheckAgentID` / `checkAgentId` (the id of the evaluator's conversation, the
field `withGoalEvaluator` carries), the locale key `chat.goal.openCheck`,
`goalRound`, `Verdict`, `RoundOutcome`, and `pkg/state/schema_migrations.go`'s
migration identity. Renaming those would change what a thing *means*, which is
not this plan.

## Rename table

Apply exactly this table; invent no other spelling.

**Go identifiers**

| now | after |
|---|---|
| `GoalCheckSubagentType` (value stays `"goal-evaluator"`) | `GoalEvaluatorSubagentType` |
| `goalCheck` (struct type) | `goalEvaluator` |
| `goalCheckTask` | `goalEvaluatorTask` |
| `goalCheckRecentRounds` | `goalEvaluatorRecentRounds` |
| `goalCheckConfig` | `goalEvaluatorConfig` |
| `goalCheckRowModel` / `goalCheckRowProvider` / `goalCheckRowEffort` / `goalCheckRowID` | `goalEvaluatorRowModel` / `goalEvaluatorRowProvider` / `goalEvaluatorRowEffort` / `goalEvaluatorRowID` |
| `runGoalCheck` | `runGoalEvaluator` |
| `ObserveGoalCheck` | `ObserveGoalEvaluator` |
| `withGoalCheck` | `withGoalEvaluator` |
| `GoalChecking` (field on `Counts`/`Frame`) | `GoalEvaluating` |
| `goalChecking` (Tracker field) | `goalEvaluating` |
| `GoalCheck` (the bare test field `GoalCheck map[string]any` in `pkg/tui/config_panel_test.go`) | `GoalEvaluator` — its `json:"goal_evaluator"` tag stays |
| `TestGoalCheck…` (eight functions in `pkg/run/goal_test.go`) | `TestGoalEvaluator…` |
| `dispatchGoalCheckPin` | `dispatchGoalEvaluatorPin` |
| `stubGoalCheckEffortError` | `stubGoalEvaluatorEffortError` |

**Go comments, the four error strings, and the two fixture literals** — same
words, new name: `goal check` → `goal evaluator`, `goal-check` → `goal-evaluator`
(`llm-goal-check-row` → `llm-goal-evaluator-row`, `sk-goal-check` →
`sk-goal-evaluator`). Running text that means the act ("checks whether the
goal is met") keeps its verb.

**Frontend**

| now | after |
|---|---|
| `frontend/src/components/settings/GoalCheckModelGroup.vue` (+ `.test.ts`) | `GoalEvaluatorModelGroup.vue` (+ `.test.ts`) — `git mv` |
| `frontend/src/components/settings/GoalCheckModelSelect.vue` (+ `__tests__/GoalCheckModelSelect.spec.ts`) | `GoalEvaluatorModelSelect.vue` (+ `__tests__/GoalEvaluatorModelSelect.spec.ts`) — `git mv` |
| identifiers `GoalCheckModelGroup` / `GoalCheckModelSelect` | `GoalEvaluatorModelGroup` / `GoalEvaluatorModelSelect` |
| `FOREBRAIN_GOAL_CHECK_AGENT_TYPE` (value stays `'goal-evaluator'`) | `FOREBRAIN_GOAL_EVALUATOR_AGENT_TYPE` |
| `goalChecking` / `setGoalChecking` (runtime status) | `goalEvaluating` / `setGoalEvaluating` |
| locale namespace `subagents.goalCheck.*` (15 keys) | `subagents.goalEvaluator.*` — values unchanged |
| `data-group="goal-check-model"` | `data-group="goal-evaluator-model"` |
| `id: 'goal-check-model'` (`GoalCheckModelGroup.vue:116`) | `id: 'goal-evaluator-model'` |
| `data-goal-check-{model,clear,search,option,effort,hint,reset,save,saved,discard}` | `data-goal-evaluator-{…}` |
| CSS class `.goal-check` in `frontend/src/components/chat/GoalLine.vue` | `.goal-evaluator` |
| `frontend/e2e/settings-goal-check-model.spec.ts` | `frontend/e2e/settings-goal-evaluator-model.spec.ts` — `git mv` |
| in that spec: `shot(page, 'settings-goal-check-…')`, `sk-e2e-goal-check-0001` | `settings-goal-evaluator-…`, `sk-e2e-goal-evaluator-0001` |

**Prototypes** — the six old-name strings listed in §3 of the tracked
settings prototype, and the one scene note in `plans/config-tui-prototype.html`.

## Commands you will need

Go runs need CGO + FTS5 and the fetched PDFium triple:

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
```

| Purpose | Command | Expected on success |
|---|---|---|
| Build everything | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 (catches every missed Go identifier) |
| Go tests (touched packages) | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tui ./pkg/config ./pkg/gateway ./pkg/turn ./pkg/agent ./pkg/process ./pkg/state ./pkg/architecture -count=1` | every package `ok` |
| Formatting | `gofmt -l cmd pkg third_party plugins` | prints nothing |
| Vet | `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0 |
| Frontend types | `cd frontend && corepack pnpm typecheck` | exit 0 (catches a missed locale key: `t()` is keyed by `I18nKey`) |
| Frontend unit tests | `cd frontend && corepack pnpm test` | all pass |
| Frontend build | `cd frontend && corepack pnpm build` | exit 0 |
| Browser e2e | `FOREBRAIN_E2E_SPEC=e2e/settings-goal-evaluator-model.spec.ts scripts/acceptance/web_e2e.sh` | ends with `web e2e: PASS` |
| Prototype regression | `cd plans && python3 -m http.server 8765` in the background, then `node plans/config-tui-prototype.test.cjs` | see Step 8 — the Ctrl+U case times out on this baseline too |

`pkg/process` takes ~4 minutes and `pkg/tui` ~3.5 on this machine; run the
package gate once, at the end, not per step.

**Disk**: ~3 GiB free (99% used). `pnpm build` and the e2e harness need room;
on `ENOSPC` stop and report — delete nothing outside the repo.

## Scope

**In scope** — exactly the files in §1, §2 and §3 above: 25 Go files, 20
frontend files (`frontend/src/**` and `frontend/e2e/**`), the tracked testdata
prototype, the owner-local `plans/config-tui-prototype.html` (edited on a copy
inside the worktree, installed by the reviewer), and `plans/README.md`
(reviewer only).

**Out of scope** (do NOT touch):

- `docs/plan/**` — the owner's prototype original and every historical design
  document. Project rule: historical records are not migrated or rewritten.
- `plans/*.md` other than this plan's row in `plans/README.md` — old plans are
  records; they keep the old word where it was written.
- Any user-visible copy *value*: `subagents.goalEvaluator.*`'s strings,
  `models.deleteMessage`, `chat.goal.*`, TUI labels, gateway sentences. The
  rename moves names, not words a person reads (the two prototypes in §3 are
  the exception the owner approved).
- `checkAgentId` / `CheckAgentID` / `chat.goal.openCheck` / `goalRound` /
  `Verdict` — the act, not the actor.
- `pkg/state`'s migration identity (`schema_migrations.go`'s migration keys,
  ordering, DDL) — only the two comments there change.
- `pkg/gateway/dist`, `frontend/dist` — generated, gitignored.
- `.claude/skills/improve/**` — four deletions that predate this plan.

## Git workflow

- Worktree `.worktrees/goal-evaluator-rename` in the repo's main worktree,
  branch `goal-evaluator-rename`, from the current HEAD (`3f04228e`).
  **Record `BASE=$(git rev-parse HEAD)` before the first edit** — the scope
  gate compares against `$BASE`.
- A fresh worktree lacks gitignored build inputs; copy them in:

```bash
MAIN="$(git worktree list | head -1 | awk '{print $1}')"
mkdir -p third_party
cp -Rc "$MAIN/third_party/pdfium" third_party/
cp -Rc "$MAIN/third_party/libtokenizers" third_party/
mkdir -p pkg/gateway && cp -Rc "$MAIN/pkg/gateway/dist" pkg/gateway/
cp -Rc "$MAIN/frontend/node_modules" frontend/node_modules
cp -Rc "$MAIN/ai-elements-vue/packages/elements/node_modules" ai-elements-vue/packages/elements/node_modules
cp -Rc "$MAIN/ai-elements-vue/packages/shadcn-vue/node_modules" ai-elements-vue/packages/shadcn-vue/node_modules
```

  (`cp -c` clones on APFS; the workspace members' `node_modules` are needed or
  `pnpm typecheck` fails with ~1100 unrelated TS errors.)
- Commit per step, conventional style matching `git log`
  (`refactor(run): name the goal evaluator, not the goal check (plan 349)`).
  Use `git mv` for the four renamed files so history follows them.
- Do **NOT** push, merge, or touch the user's branch — the owner decides
  landing.

## Steps

### Step 1: Go identifiers

Take the file list from the done criterion, not from a shorter pattern — the
first draft of this plan selected files by a five-token grep and would have
skipped 17 files that carry only prose or the hyphen spelling. Order matters:
`GoalChecking`/`goalChecking` must be rewritten before the general rule, or
they turn into `GoalEvaluatoring`.

```bash
git grep -l -e GoalCheck -e goalCheck -e goal_check -e 'goal check' -e goal-check -- '*.go' \
  | xargs perl -pi -e '
    s/GoalChecking/GoalEvaluating/g;
    s/goalChecking/goalEvaluating/g;
    s/GoalCheck/GoalEvaluator/g;
    s/goalCheck/goalEvaluator/g;
  '
```

That one sweep covers every identifier in the rename table, including
`TestGoalCheck…`, `dispatchGoalCheckPin`, `stubGoalCheckEffortError` and the
bare `GoalCheck` test field — a `\b`-anchored script would **not**, because
`GoalCheck` inside `TestGoalCheck…` and `GoalCheckSubagentType` has no word
boundary. Dry-run result on `3f04228e`: zero `GoalCheck|goalCheck` hits left,
and `goalCheckRowModel` → `goalEvaluatorRowModel`, `goalChecking` →
`goalEvaluating` as intended.

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./...` → exit 0;
`grep -rn 'GoalCheck\|goalCheck\|GoalChecking\|goalChecking' --include='*.go' cmd pkg` → nothing.

### Step 2: Go prose, the four error strings and the two fixture literals

Now the same 25 files' running text: `goal check` → `goal evaluator` and
`goal-check` → `goal-evaluator` — in comments, in the four `errors.New`/
`fmt.Errorf` sentences in `pkg/run/goal.go`, in the eight `t.Fatal` messages,
and in the two fixture names (`llm-goal-check-row` → `llm-goal-evaluator-row`,
`sk-goal-check` → `sk-goal-evaluator`). Two cases need judgement rather than a
swap — `pkg/run/goal.go`'s doc comments about what the evaluator *does*
("checks whether the goal is met"), and `pkg/agent/subagent_defs.go`'s
"(plan review, /goal checks)" → `(/goal evaluations)` reads better than a bare
word swap; say what you chose in NOTES.

**Verify**: `grep -rn 'goal check\|goal-check\|goal_check' --include='*.go' cmd pkg`
→ nothing; `grep -rn 'goal-evaluator' --include='*.go' cmd pkg` still contains
`const GoalEvaluatorSubagentType = "goal-evaluator"` (the id must not change).

### Step 3: Frontend identifiers and the runtime constant

- `FOREBRAIN_GOAL_CHECK_AGENT_TYPE` → `FOREBRAIN_GOAL_EVALUATOR_AGENT_TYPE` in
  `frontend/src/lib/forebrainGatewayRuntime.ts` and its three consumers
  (`useChatStream.ts`, `ChatView.vue`, `useChatStream.test.ts`).
- `goalChecking` / `setGoalChecking` → `goalEvaluating` / `setGoalEvaluating`
  (`useChatStream.ts`, `useChatStream.test.ts`, `ChatView.vue`).
- `GoalCheckModelGroup` / `GoalCheckModelSelect` identifiers in
  `GlobalSubagentsGroup.vue`, `PlanReviewerModelGroup.vue` and their tests.

**Verify**: `cd frontend && corepack pnpm typecheck` → exit 0.

### Step 4: The locale namespace

`frontend/src/locales/index.ts`: rename all 15 `subagents.goalCheck.*` keys to
`subagents.goalEvaluator.*` in the zh block (`:717-731`) and the en block
(`:3300-3314`), and update every `t('subagents.goalCheck.…')` caller (the two
components, `PlanReviewerModelGroup.vue` where it reuses them). **Keep key
order and every value byte-identical** — this is the half a reviewer checks
first.

**Verify**: `grep -rn 'subagents\.goalCheck' frontend/src` → nothing;
`cd frontend && corepack pnpm typecheck` → exit 0.

### Step 5: The two component files

```bash
git mv frontend/src/components/settings/GoalCheckModelGroup.vue \
       frontend/src/components/settings/GoalEvaluatorModelGroup.vue
git mv frontend/src/components/settings/GoalCheckModelGroup.test.ts \
       frontend/src/components/settings/GoalEvaluatorModelGroup.test.ts
git mv frontend/src/components/settings/GoalCheckModelSelect.vue \
       frontend/src/components/settings/GoalEvaluatorModelSelect.vue
git mv frontend/src/components/settings/__tests__/GoalCheckModelSelect.spec.ts \
       frontend/src/components/settings/__tests__/GoalEvaluatorModelSelect.spec.ts
```

then fix every importer (`GlobalSubagentsGroup.vue`, `PlanReviewerModelGroup.vue`,
`SettingsSubagentsTab.*`, `settingsSchema.ts`'s comment), the `data-group`
container attribute and the group's own `id: 'goal-check-model'` in the group
component, and the four Chinese comments that still call it 「/goal 检查模型」
(`locales/index.ts:715`, `GoalCheckModelSelect.vue:2`,
`GoalCheckModelGroup.vue:2`, `settingsSchema.ts:26`) — they become 目标评估.
No grep in the done criteria finds those four; only reading the files does.

**Verify**: `cd frontend && corepack pnpm typecheck` → exit 0;
`corepack pnpm test -- --run src/components/settings` → all pass.

### Step 6: The test hooks

`data-goal-check-*` → `data-goal-evaluator-*` (all ten, in the two components
and every test that selects them: the group test, the select spec,
`SettingsSubagentsTab.test.ts`, the e2e spec), and `data-group="goal-check-model"`
→ `data-group="goal-evaluator-model"`. Keep the attribute *shape* identical —
these are quotes in selectors, so a missed one is a failing test, not a silent
skip.

**Verify**: `grep -rn 'data-goal-check\|goal-check-model' frontend/src frontend/e2e`
→ nothing.

### Step 7: The e2e spec

```bash
git mv frontend/e2e/settings-goal-check-model.spec.ts \
       frontend/e2e/settings-goal-evaluator-model.spec.ts
```

Rename inside it: the `data-group`/`data-goal-check-*` selectors, the
`screenshot` names (`settings-goal-check-*` → `settings-goal-evaluator-*`) and
the seeded key `sk-e2e-goal-check-0001` → `sk-e2e-goal-evaluator-0001`. Test
titles may say "goal-check block" → "goal-evaluator block".

**Verify** (this is the real proof that the page still works):
`FOREBRAIN_E2E_SPEC=e2e/settings-goal-evaluator-model.spec.ts scripts/acceptance/web_e2e.sh`
→ `web e2e: PASS`.

### Step 8: The two tracked prototypes

1. `frontend/src/lib/testdata/config-db-settings-preview.html`: the six
   old-name strings listed in "Current state" §3 (five Chinese, one
   `data-grp` attribute) → 目标评估 / 评估 in the same register the app uses
   (`/goal 评估模型`, `仅影响目标评估子代理`, `data-grp="goal-evaluator-model"`).
   Leave line 645's verb alone. This file is the tracked copy the
   settings-schema test reads; the owner approved syncing it.
2. `plans/config-tui-prototype.html` line 245: `候选与 goal check 同款 enum`
   → `候选与 goal evaluator 同款 enum`. Nothing else in that file changes.
   `plans/` is gitignored, so it is not in your worktree: copy both
   `plans/config-tui-prototype.html` and `plans/config-tui-prototype.test.cjs`
   from the main worktree into your worktree at the same relative paths, edit
   the copy there, and report its sha256. Do not touch the main worktree and
   do not edit `plans/README.md`.

**Verify**:
- The test fetches `http://127.0.0.1:8765/config-tui-prototype.html`: serve
  the directory first (`cd plans && python3 -m http.server 8765` in the
  background, stop it afterwards), then `node plans/config-tui-prototype.test.cjs`.
  On this baseline the suite already stops at the pre-existing
  `Multiline text, cancel and restore default` timeout (the prototype never
  binds Ctrl+U and has no Default button; recorded in `plans/README.md`'s
  2026-11-21 entry). Required result: **the same stop point — a Playwright
  timeout waiting for the `Default` button — and no new failure before it**.
  If it fails earlier or later, STOP and report which case.
- Recompute the copy's frozen hash and report it:
  `shasum -a 256 plans/config-tui-prototype.html` (the worktree copy). The
  reviewer installs the file in the main worktree and replaces the `ad6277ab…`
  value in `plans/README.md`'s 2026-11-21 prototype sentence.
- `grep -c 'goal check' plans/config-tui-prototype.html` → 0 (worktree copy;
  the reviewer re-runs it on the installed main-tree file).

### Step 9: Full gates and the done criteria

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
CGO_ENABLED=1 go build -tags fts5 ./...
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tui ./pkg/config ./pkg/gateway ./pkg/turn ./pkg/agent ./pkg/process ./pkg/state ./pkg/architecture -count=1
gofmt -l cmd pkg third_party plugins
CGO_ENABLED=1 go vet -tags fts5 ./...
cd frontend && corepack pnpm typecheck && corepack pnpm test && corepack pnpm build
```

Expected: build exit 0, nine `ok` packages, `gofmt` silent, vet exit 0,
typecheck 0, all vitest files pass, build exit 0.

## Test plan

No new test is written: a rename is proved by the existing suite still passing
and by the repository no longer containing the old name. The load-bearing
checks are the compiler and the typechecker — a missed Go identifier fails
`go build`, a missed locale key or component path fails `pnpm typecheck`, a
missed `data-*` hook fails its unit test, and a missed selector fails the e2e
spec.

| Where | Check | Covers |
|---|---|---|
| `go build ./...` | exit 0 | every Go identifier |
| `pkg/run` tests | `TestGoalCheck*`… renamed with the code; all pass | the evaluator's own behaviour unchanged |
| `pkg/tui` tests | config-panel + reducer suites pass | the renamed `Counts`/`Tracker` fields |
| `frontend` typecheck + vitest | exit 0 / all pass | component paths, locale keys, `runtimeStatus` field |
| `e2e/settings-goal-evaluator-model.spec.ts` | `web e2e: PASS` | the settings page really draws the renamed block |
| `plans/config-tui-prototype.test.cjs` | same stop point, no new failure | the frozen /config prototype |
| `pnpm build` | exit 0 | nothing referenced by name at runtime |

Regression pins that must pass unchanged: the copy rules from plans 347/348
(`models.pinGoalEvaluator`, `models.deleteMessage`'s English sentence) and the
whole `settings-models` e2e spec.

## Done criteria

ALL must hold:

- [ ] `grep -rn 'GoalCheck\|goalCheck\|goal_check\|goal check\|goal-check' --include='*.go' cmd pkg third_party plugins` → nothing.
- [ ] `grep -rn 'GoalCheck\|goalCheck\|goal-check' frontend/src frontend/e2e` → nothing.
- [ ] `grep -rn 'GOAL_CHECK' frontend/src frontend/e2e` → nothing (the uppercase constant is invisible to the two greps above; this is the one criterion that catches it).
- [ ] `grep -rn 'goal check' --include='*.vue' --include='*.ts' frontend/src frontend/e2e` → nothing.
- [ ] `grep -rn '检查模型\|目标检查' frontend/src pkg cmd` → nothing.
- [ ] `grep -rn 'goal check' plans/config-tui-prototype.html` → nothing in the worktree copy, and the reviewer re-checks the installed main-tree file and records the new sha256 in `plans/README.md`.
- [ ] `grep -rn 'data-goal-check\|goal-check-model\|subagents\.goalCheck' frontend/src frontend/e2e` → nothing.
- [ ] `const GoalEvaluatorSubagentType = "goal-evaluator"` and `export const FOREBRAIN_GOAL_EVALUATOR_AGENT_TYPE = 'goal-evaluator'` — both values byte-identical to today.
- [ ] `git diff "$BASE" -- frontend/src/locales/index.ts` shows only key names and the two comments changed on each line (no value changes) — reviewer's check, the executor reports it.
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0; the nine-package gate `ok`; `gofmt` silent; `go vet` exit 0.
- [ ] `cd frontend && corepack pnpm typecheck` 0, `corepack pnpm test` all pass, `corepack pnpm build` 0.
- [ ] `FOREBRAIN_E2E_SPEC=e2e/settings-goal-evaluator-model.spec.ts scripts/acceptance/web_e2e.sh` → `web e2e: PASS`.
- [ ] `git diff --name-only "$BASE"` ⊆ the in-scope list; the four renamed files show as renames, not add+delete where git can see it. `plans/README.md` and the owner-local prototype never appear in that diff — `plans/` is gitignored — so the prototype copy and the README row are handed back separately and reported in NOTES.
- [ ] No `docs/plan/**`, no other `plans/*.md`, no `pkg/state` migration identity touched.
- [ ] `plans/README.md` status row updated (DONE / BLOCKED with reason).

## STOP conditions

Stop and report (do not improvise) if:

1. The drift check finds an in-scope file whose **content** no longer matches
   the excerpts.
2. A rename would change a **value** rather than a name: any `goal_check`
   spelling found in a config key, a database value, an API field or a
   user-visible sentence. Pre-flight: `git grep -n 'goal_check'` at `3f04228e`
   is empty (it is the snake_case key spelling, and no config key, column or
   field uses it), so every hit you meet is naming. Test fixture literals
   (`llm-goal-check-row`, `sk-goal-check`) are names and **do** change — they
   are not a stop.
3. `pkg/state`'s migration list, ordering or DDL would have to change.
4. The prototype regression fails anywhere **other** than its known Ctrl+U
   stop point, or the settings-schema test starts comparing the group title.
5. The e2e harness cannot find the renamed spec (`web e2e` aborts with
   "FOREBRAIN_E2E_SPEC names no spec") — that means the file rename and the
   command disagree; fix the command, not the spec name.
6. A step's verification fails twice after a reasonable fix attempt.
7. The change appears to require a file outside the scope list.

## Maintenance notes

- **One name, one meaning.** The subagent is the *goal evaluator*; the act is
  *checking whether the goal is met* and its result is a `Verdict`. New code
  uses `GoalEvaluator…` for the actor and keeps `check`/`evaluate` for the act.
  A future rename of the act is a different plan with its own copy review.
- **The id is `goal-evaluator`** and must stay: it is what `agentType` carries,
  what `subagentKey` compares, and what the frontend constant compares. The
  Go/frontend identifier names were the only stale part.
- **Owner's own prototype** (`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/config-db-settings-preview.html`,
  gitignored): synced 2026-11-22 to the tracked copy byte-for-byte, with the
  pre-sync file kept as `…html.bak-20261122T100415`. Future edits belong in
  `frontend/src/lib/testdata/config-db-settings-preview.html` first and are
  copied over afterwards; the owner's copy still has no git history to
  protect a mistake.
- **Historical records keep the old word** (`docs/plan/**`, `plans/001…348`).
  That is deliberate: a plan is a record of what was decided then, not a live
  document.
- **Reviewer scrutiny**: locale diffs change keys only; the two prototype
  edits are exactly the strings listed; `checkAgentId`/`openCheck`/`goalRound`
  untouched; `pkg/state` migration identity untouched; the e2e spec still
  asserts the same behaviour under the new file name.
