# Plan 313: Ship the shared subagent-model contract in TUI /config, gateway and web — reusing the existing semantics, plus primary-agent creation in the TUI

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: the baseline `7611d6cc` IS the current `dev`
> HEAD, so a `baseline..HEAD` diff would always be empty and would not see
> uncommitted work. Run both:
> `git status --porcelain -- pkg/config/agents.go pkg/config/load.go pkg/config/agents_test.go pkg/config/config_test.go pkg/gateway/api_extra.go pkg/gateway/api_extra_test.go pkg/gateway/subagents_api.go pkg/gateway/subagents_api_test.go pkg/tui/config_panel.go pkg/tui/config_panel_schema.go pkg/tui/config_panel_test.go frontend/src/components/settings/SettingsSubagentsTab.vue frontend/src/views/SubagentsView.vue frontend/src/views/SubagentsView.test.ts frontend/src/lib/api.ts frontend/src/locales/index.ts`
> and `git diff 7611d6cc -- <the same paths>`.
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition. Exception: `frontend/src/lib/api.ts`
> and `frontend/src/locales/index.ts` carry an **already-merged additive diff**
> (2026-11-17 reconcile at `fcb241bc`: the catalog_provider commits
> `1433b024`/`ea7ea671`/`2c0c248f` added `catalogProvider`/`catalogId` to
> `LlmModelRow`/`LlmModelWrite`/`LlmModelCatalogCaps`, an optional
> `catalogProvider` query param on `llmModelCatalogCaps`, and `models.catalog*`
> locale keys). That diff is expected, not drift — `SubagentConfigRow`
> (api.ts:2385) is untouched by it and the excerpt below must still match.
> Plan 311 also edits `llmModelCatalogCaps` (an additive `apiPath` param, a
> different type); if 311 landed first, expect that too. No other in-scope
> file changed since `7611d6cc` (reconcile ran the full drift check: clean).
> The frozen prototype regression now reports **26 passed** (24 checks + the
> nested-transition result), not 20.
> **Merged parallel fix (verified 2026-11-17, dev@`9b646893`)**: `9b646893`
> (owner-approved ff-only merge of `.worktrees/fix-custom-effort-vendor-wins`;
> worktree/branch deleted after the merge per owner's standing rule) makes
> `applyModelOverride` vendor-wins — a
> catalog-documented model's levels come from the catalog; a row's stale
> `cap_efforts` only answers when the catalog has no discrete levels or the
> provider is login-backed. This plan pins no row-wins assertion
> (grep-verified); its effort candidates flow through the existing
> turn/gateway read paths and inherit the new priority. Re-run the focused
> gates on this HEAD before starting; the uncommitted main-tree setup.go /
> models.json / test-db.sh changes are **someone else's/owner's WIP**, not
> that session's — leave them untouched.

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED
- **Depends on**: **plan 315 must land first** (it deletes `llm_providers`
  and moves per-type defaults to `agents.subagent_models`; this plan's TUI
  global-default summary and gateway global source read that new storage).
  Plan 316 (· sweep) is independent but same-period; do not introduce `·`.
  311/312 own `pkg/tui/setup.go`; 311 also makes an
  additive edit to `frontend/src/lib/api.ts` (`llmModelCatalogCaps`, not
  `SubagentConfigRow`) — if both run, land 311's api.ts hunk first or expect
  it in the drift check. Still do not run in parallel with them.
  **Plan 314 depends on THIS plan** (it relocates the `plan_reviewer` pair
  into the "Subagent models" section and adds the global custom
  registry); land 313 first.
- **Category**: feature
- **Planned at**: commit `7611d6cc`, 2026-11-16

## Why this matters

The owner requires the TUI `/config` panel to configure the model and thinking
level of the four public built-in subagent types (`general-purpose`, `explore`,
`plan`, `verification`) and of the system-dispatched `plan-reviewer`, as
install-wide (global) configuration, and to create new primary agents. The
owner's ruling (2026-11-16) is explicit: **reuse the semantics web and gateway
already implement — do not invent a new storage or a new precedence.** Today
the gateway resolves a built-in's model as: this agent's own subagent entry →
the per-type global default row `agents.subagent_models.<type>`
(`{model, reasoning_effort}`, a global-table row id — plan 315 deletes the
old `definitions.<type>.llm_providers` provider list) →
the conversation's model; a system subagent carries no per-agent configuration
at all; and the one system type with a global row (`goal-evaluator`) stores it
as `agents.goal_evaluator` (`SubagentConfig{Model, ReasoningEffort}`) validated
by `validateSubagentModel`. The only missing pieces are: a global row for
`plan-reviewer` on the same pattern, surfaces for it in TUI/web, source-aware
display of built-in rows in the TUI, and a guided primary-agent creation flow
in the TUI. The interactive design contract is already frozen in
`plans/config-tui-prototype.html` (scenes `global-*`, `builtin-*`,
`system-stray-entry`, `create-*`, `provider-pick`, `default-model-pick`,
`primary-listed`); this plan implements that contract on the real code.

**Owner rulings folded in (2026-11-16, latest wins):**
- The earlier "delete the legacy per-type storage semantics" requirement is
  **withdrawn**: `agents.definitions.<type>` stays the store for built-in
  per-type defaults. The `Agents` entry keeps its name.
- **Per-agent built-in overrides stay** (ruling A): the read-only +
  enable/disable rule governs *globally shared definitions* (the new global
  custom subagents and the built-ins' global defaults), NOT this agent's own
  override of a built-in. The gateway builtin PUT, the web drawer's
  `builtinBody()`, dispatch's agent-entry-first order and the prototype's
  `builtin-override` scenes are all unchanged. Step 7's `agent override:`
  summary branch stays.
- **Primary marker per the prototype**: a right-aligned `Primary` status on
  the definitions row (prototype `primary-listed`), NOT a description prefix.
  The real `SelectItem` has no status column, so this plan adds one (Step 4b).
- **Gateway create type-name guard this plan** (Step 3b):
  `handlePrimaryAgentCreate` must refuse a key naming a built-in subagent
  type, the same verdict the TUI creation flow gives.
- **Global shared custom subagents + the "Subagent models" TUI
  section are plan 314**, which depends on this plan. This plan keeps the
  `plan_reviewer` pair in the `agents` section as the interim TUI surface;
  314 builds the projection section and relocates the pair there. Do not
  build that section or the global custom registry here.

## Current state

Files and roles (all excerpts verified at `7611d6cc`):

- `pkg/config/agents.go` — `AgentsSection` (lines 13–21) holds `Defaults`,
  `Definitions`, and the one global system row:
  ```go
  type AgentsSection struct {
      Defaults    AgentDefaults              `yaml:"defaults,omitempty" json:"defaults,omitempty"`
      Definitions map[string]AgentDefinition `yaml:"definitions,omitempty" json:"definitions,omitempty"`
      // GoalEvaluator is the one system subagent the settings page configures:
      // ... (plan 299).
      GoalEvaluator SubagentConfig `yaml:"goal_evaluator,omitempty" json:"goal_evaluator,omitempty"`
  }
  ```
  `validateSubagentModel` (lines 313–328) is the shared rule: empty model +
  non-empty effort → `"reasoning_effort requires model"`; a model without the
  `provider/model` shape must resolve as a row id via `refResolves`; else
  `"model %q must be provider/model"`. `ValidateGoalEvaluatorModel`
  (lines 334–336) is a two-line wrapper naming `agents.goal_evaluator`.
  `ValidateAgentSubagents` (lines 252–307) refuses system names
  (`"%q is dispatched by the system and cannot be configured"`, line 273) and
  restricts a public built-in entry to model/effort/prompt/enabled
  (lines 275–278).
- `pkg/config/load.go` — line 19: `var primaryAgentIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)`
  (unexported). Lines 209–214 call the goal-evaluator validator with
  `ResolveModelRef(r, "main", ref)` as the resolver, deliberately outside
  `validatePrimaryAgents` so a fresh install with no definitions is still
  checked. **An exported id check already exists** — lines 971–975:
  ```go
  // ValidPrimaryAgentID reports whether id is a legal tenant key: the same
  // pattern the loader enforces on agents.definitions keys.
  func ValidPrimaryAgentID(id string) bool {
      return primaryAgentIDPattern.MatchString(id)
  }
  ```
  The gateway already uses it (`pkg/gateway/agents_api.go:555`). Reuse it;
  do NOT add a second helper or a second regexp. It has no test today
  (`grep -rn "TestValidPrimaryAgentID" pkg/config/` is empty).
  Two more facts the creation flow needs: `definitions` keys are not all
  primary agents — a key naming a built-in subagent type
  (`builtInSubagentDefinitionNames`, lines 21–30: the four public types plus
  `guardian` and the three `cavecrew-*`) is that type's install-wide default
  definition, and `IsEffectivePrimaryAgent` (lines 902–910) returns false for
  it **regardless of its `primary` flag**. The exported check is
  `IsBuiltInSubagentDefinition(id)` (lines 913–917). So creating a "primary
  agent" under such a key would land a row that is never primary — the
  creation flow must refuse those keys (step 5).
- `pkg/gateway/api_extra.go` — settings path whitelist, lines 9267–9268:
  ```go
  {path: "agents.goal_evaluator.model", section: "agents", kind: settingsText},
  {path: "agents.goal_evaluator.reasoning_effort", section: "agents", kind: settingsText},
  ```
  `normalizeGoalEvaluatorModelPath` (const `goalEvaluatorModelPath` at
  line 9839, function at lines 9848–9862) rewrites a stored legacy
  `provider/model` label to the resolved row id (only the id branch of
  `ResolveModelRef` ignores the agent name, which is what makes the row
  global); called from the settings write path at line 9761 — its only
  caller, and no test references the name either
  (`grep -rn normalizeGoalEvaluatorModelPath pkg/` shows exactly three
  lines: the call at 9761, the doc comment at 9841, the definition at 9848).
  `settingsCandidateLoads` (line 9867) runs the loader over the candidate.
- `pkg/gateway/subagents_api.go` — `subagentRow` struct (lines ~100–145) carries
  `Kind`, `ModelSource` (`"agent"` / `"global"`), `Enabled *bool` (null for
  system), `PromptOverridden`. `subagentRowFor` line 184–185:
  `case subagentKindSystem: return systemSubagentRow(key), true`.
  `systemSubagentRow(name string)` (lines 245–256) carries **no model and no
  switch**. `subagentRowModel` (lines 270–300) is the resolution order quoted
  above; `pkg/run/runner.go:1845-1871` (`SubagentOwnModel`) answers dispatch
  with the same order, and `runner.go:982-996` builds per-type clients only for
  `agent.PublicTypeNames()` from the legacy per-type definitions.
  PUT/DELETE for system names are refused (lines 848–849, 880–881); the enabled
  endpoint refuses system names (line 909).
- `pkg/gateway/agents_api.go` — `handlePrimaryAgentCreate` (lines 545–585)
  validates only the id shape (`config.ValidPrimaryAgentID`, line 555) and
  duplicate keys (line 569), then seeds `Primary: true` (line 572). It does NOT refuse a
  key naming a built-in subagent type, so creating `explore` lands a row
  `IsEffectivePrimaryAgent` never treats as primary — the same defect the TUI
  creation flow guards (Step 3b fixes it here too).
- The rich-picker rendering chain (for the Primary status column, Step 4b):
  `SelectItem` (`pkg/tui/channels.go:616–631`) has
  Label/Description/Category/Disabled/ElideTail/Mark — **no status field**.
  `rawSelector.SelectRich` (`overlays.go:5861`) → `richPickerState.panel`
  (`overlays.go:5806–5843`) draws each row via
  `b.selectableColumns(ci == p.cursor, "", item.Label, nameWidth, strings.TrimSpace(item.Description), item.ElideTail, mark)`
  (line 5832). `selectableColumns` (`panel_columns.go:184–190`) builds a
  `panelLine{lead, col, colWidth, text, elideTail, mark}`; `panelLine`
  (`render.go:7588–7611`) has no status field. `columnRows`
  (`panel_columns.go:109–143`) lays the name column then the description
  column; `columnRowsStacked` (`:145`) is the narrow fallback. `Mark` is a
  bare glyph drawn tight against the name (the /model effort gear), NOT a
  right-aligned column. `lineSelector.SelectRich` (`channels.go:1032`) is the
  non-TTY fallback and draws only `Label`. The prototype's status column is
  `.item-status{min-width:8ch;text-align:right;color:var(--quiet)}`, hidden
  when narrow (`.terminal.narrow .item-status{display:none}`); the
  `primary-listed` scene sets `row.status = value.primary ? 'Primary' : ''`.
- `pkg/tui/config_panel_schema.go` — pure-data section table. The `agents`
  section (lines 185–220) already has `definitions` as `configFieldRows` and
  the goal-check pair as two `configFieldText` fields (lines 211–218):
  ```go
  {Path: "goal_evaluator.model", Label: "Goal check model", Kind: configFieldText,
      Hint: "A model row id (the row chosen in Settings → Subagents); empty follows the primary agent"},
  {Path: "goal_evaluator.reasoning_effort", Label: "Goal check thinking level", Kind: configFieldText,
      Hint: "Empty follows that model row's default level; low, high and so on override it"},
  ```
  `TestConfigPanelSchemaCoversEveryRootSection` ties this table to `config.Root`.
- `pkg/tui/config_panel.go` — generic row editor. `editRows` (line 665,
  method `func (p *configPanel) editRows(doc any, path []string, field configField) (any, string)`)
  lists rows via `p.rowItems(container, keys, list)` — `rowItems` is a
  **method**, `func (p *configPanel) rowItems(container any, keys []string, list bool) ([]SelectItem, []int)`
  (line 692, **single caller** at line 670). Crucially, `editRows` passes the
  SAME `field` down at every nesting level (line 686: `p.editRow(doc, path, keys[owners[idx]], field)`
  and line 682: `p.addRow(doc, path, field)`), so inside `definitions.<id>`
  the field's `Path` is still `"definitions"` — any guard keyed on
  `field.Path` alone must also check the actual `path`. `configRowDescription(value)`
  (lines 718–726) is value-only (`"› N items"` / `"= scalar"` / `"(empty)"`).
  `editRow` (line 748) has signature `(doc any, path []string, key string, field configField) (any, string)`.
  `addRow` (lines 798–850) asks a key for map rows, refuses empty and
  duplicate keys, then `chooseNewValue` (type menu incl. "Empty object; open
  it to edit") and opens the new object. `SelectItem` (pkg/tui/channels.go:616)
  has Label/Description/Category/Disabled/ElideTail/Mark — **no status column
  today**; Step 4b adds `SelectItem.Status` for the prototype's right-aligned
  `Primary` marker. The subagent source summaries (`agent override:` /
  `global default:`) stay description text (Step 5). Saving goes through
  `ConfigStore.UpdateSection` with the process-wide section validator
  (file header comment line 6 and line 133), i.e. **loader refusals already
  surface in the panel** (`TestConfigPanelShowsTheValidatorsRefusal`,
  `config_panel_test.go:284`).
- `frontend/src/components/settings/GoalCheckModelGroup.vue` — the hand-written
  global group beside the tab form (lines 107–122):
  ```ts
  const MODEL_PATH = 'agents.goal_evaluator.model'
  const EFFORT_PATH = 'agents.goal_evaluator.reasoning_effort'
  const group: SettingsGroup = { id: 'goal-check-model', titleKey: 'subagents.goalCheck.title',
    fields: [{ path: MODEL_PATH, kind: 'model' }, { path: EFFORT_PATH, kind: 'text' }] }
  ```
  It writes via `props.form.setValue(...)`, saves via `form.saveGroup(group)`,
  and clears effort when the model changes (`onModel`, lines 172–176; the
  clear is line 174). Mounted by
  `SettingsSubagentsTab.vue` beside `<SettingsTabForm>` (whole file, 30 lines).
  Groups self-register their paths with the form; `frontend/src/lib/settingsSchema.ts`
  has **no** goal_evaluator entries — do not add any there.
- `frontend/src/views/SubagentsView.vue` — system rows show
  `systemModel(row)` (lines 339–343): i18n `subagents.modelBySystem.<name>`
  falling back to `subagents.followPrimary`; system rows have no switch
  (line 113 `v-if="row.kind !== 'system'"`) and only a View action.
  `frontend/src/locales/index.ts:805` (zh) and `:3336` (en) hold
  `'subagents.modelBySystem.plan-reviewer'`.
- `frontend/src/lib/api.ts` — `SubagentConfigRow` type mirrors `subagentRow`.
- `plans/config-tui-prototype.html` — the frozen interaction contract. The
  display strings and creation rules this plan implements are verbatim there
  (line numbers in that file):
  - `effortLabel` (:50): empty → `Row default`, `none` → `Off`, else
    title-cased level.
  - `typeRowOf` (:53): the global-table row named by
    `subagent_models.<type>.model` (a row id), or null when unset.
  - `typeSummary` (:54): no row → `Follow conversation model`; else
    `rowLabel(row) + " / " + effortLabel(reasoning_effort)` — the effort part
    is ALWAYS shown, `Row default` when absent.
  - system-entry notice (:118/:121, `openField`): `Dispatched by the system.
    Not configurable per agent; remove this entry.`
  - primary marker (:116): a right-aligned `Primary` status on the definitions
    row — Step 4b adds `SelectItem.Status` and the picker's status column for it.
  - `titleCase` (:109): `String(s).replace(/_/g,' ').replace(/^./,c=>c.toUpperCase())`
    — underscores become spaces, first character upper-cased, rest as written.
  - creation (`addEntry` :135, scenes `create-agent`/`new-primary-detail`):
    an Agent ID input plus a Display name input ("Leave empty to use the
    title-cased ID."), seed
    `{display_name, primary:true, mcp_servers:[], subagents:[]}` (no provider
    list — plan 315),
    then open the new agent's detail. The prototype's ID regex
    `/^[a-z][a-z0-9-]*$/` is STRICTER than the loader's (no underscore); this
    plan follows the loader (`config.ValidPrimaryAgentID`) per the owner's
    reuse ruling — do not "fix" the validation to the prototype's regex.
  - the prototype forbids the middle dot `·` in its own HTML
    (`config-tui-prototype.test.cjs:10`). That assert governs the prototype
    file; the real panel already uses `" · "` in its own prompts
    (`configFieldDescription` :651, `editRow` prompt :760), and the owner's
    no-`·` ruling is about deliverable rows, not this panel.

Conventions to match:

- Go: table-driven tests beside the code (`pkg/config/agents_test.go:614`
  `TestValidateGoalEvaluatorModel` is the exemplar for validator tests;
  `pkg/gateway/subagents_api_test.go:520` `TestSubagentSystemUpdateIsRefused`
  for refusal tests; `pkg/tui/config_panel_test.go:187`
  `TestConfigPanelSavesATriStateBoolean` for panel tests with a fake store).
- Comments explain WHY and cite plan numbers, e.g. the goal-evaluator comment
  in `config_panel_schema.go:207-210`. Match that voice; cite "plan 313".
- Web: Vue SFC `<script setup lang="ts">`, i18n via `useI18n()` keys in
  `frontend/src/locales/index.ts` (zh block first, en block second), component
  tests like `frontend/src/components/settings/CronSettingsTab.test.ts` and
  `frontend/src/views/SubagentsView.test.ts`.
- TUI panel copy is English; the web settings copy is Chinese via i18n keys.
- The frozen design reference `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html`
  (sha256 `aef1aa8cac4e86fd258706d56acb4b8b8c3507bf575356ddae4106bd51ac63b0`)
  must stay untouched; `plans/config-tui-prototype.html`
  (sha256 `95e75dedd9479317e23464d1978efc6a9301e3dfead0efade0bcbee9c8e9e688`)
  is the accepted interaction contract for this plan (read-only reference).

## Commands you will need

Environment setup (house rules — run once per shell before the Go commands):

```bash
export CGO_ENABLED=1
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
```

**Every `go test` below must run against a fresh scratch home** —
`FOREBRAIN_HOME="$(mktemp -d)"` prefixed on the command line. An agent
session exports `FOREBRAIN_HOME` pointing at the REAL install
(`~/.forebrain`); `pkg/tui` tests drive the real terminal entrypoint and an
unpinned home writes into the user's install (FOREBRAIN.md house rules,
2026-10-14 incident). Never inherit the ambient value.

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build -tags fts5 ./...` | exit 0 |
| Vet | `go vet -tags fts5 ./pkg/config ./pkg/gateway ./pkg/tui` | exit 0 |
| Format check | `gofmt -l pkg/config pkg/gateway pkg/tui` | empty output |
| Go tests (focused) | the fenced block below the table | named tests `--- PASS`; never `[no tests to run]` |
| Go tests (packages) | `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/config ./pkg/gateway ./pkg/tui -count=1` | all pass |
| Web typecheck | `cd frontend && pnpm typecheck` | exit 0 |
| Web tests (focused) | `cd frontend && pnpm vitest run src/components/settings src/views/SubagentsView.test.ts` | all pass |
| Web build | `cd frontend && pnpm run build` | exit 0 |
| Prototype regression (reference only) | `cd plans && node config-tui-prototype.test.cjs` | `"passed": 20`, zero browser errors (needs `python3 -m http.server 8765 --bind 127.0.0.1 --directory plans` running; Playwright chromium comes from `frontend/node_modules`, so `pnpm install` must have run) |

Focused Go gate (kept out of the table because a markdown table eats `|`
characters — copy this block verbatim, with BARE pipes inside the quoted
`-run` pattern):

```bash
FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/config ./pkg/gateway ./pkg/tui \
  -run 'PlanReviewer|Subagent|ConfigPanel' -count=1 -v 2>&1 | tee /tmp/plan313-focused.log
grep -c '^--- PASS' /tmp/plan313-focused.log   # positive count
grep -c 'no tests to run' /tmp/plan313-focused.log   # must be 0
```

**`-run` patterns use bare `|`, never `\|`.** In a Go `-run` regex `\|` is a
literal escaped pipe: it matches zero tests and `go test` still exits 0 with
`ok ... [no tests to run]` — a false green (verified on this repo). Every
focused gate in this plan therefore runs with `-v` and checks that the named
tests actually appear as `--- PASS` lines; `[no tests to run]` means the
pattern is wrong, not that the step passed.

Go toolchain: the repo's cached Go 1.26.9 with the `fts5` build tag (the
build, vet and focused-test commands above were run successfully at review
time with the scratch-home prefix). If `go` is missing or the tag fails to
build for an unrelated reason, STOP.

## Scope

**In scope** (the only files you should modify):

- `pkg/config/agents.go`, `pkg/config/load.go`, `pkg/config/agents_test.go`,
  `pkg/config/config_test.go` (load-refusal test only if that is where load
  tests live; otherwise `pkg/config/load_test.go` if it exists)
- `pkg/gateway/api_extra.go`, `pkg/gateway/api_extra_test.go`,
  `pkg/gateway/subagents_api.go`, `pkg/gateway/subagents_api_test.go`,
  `pkg/gateway/agents_api.go`, `pkg/gateway/agents_api_test.go` (create
  type-name guard, Step 3b)
- `pkg/run/subagent.go` (plan-review approval pre-select only, Step 3c),
  `pkg/run/subagent_test.go`
- `pkg/tui/config_panel_schema.go`, `pkg/tui/config_panel.go`,
  `pkg/tui/config_panel_test.go`
- `pkg/tui/channels.go` (`SelectItem.Status`), `pkg/tui/render.go`
  (`panelLine.status`), `pkg/tui/panel_columns.go` (`selectableColumns` /
  `columnRows` status rendering), `pkg/tui/overlays.go`
  (`richPickerState.panel` passes `item.Status`) — the Primary status column,
  Step 4b. Their tests: `pkg/tui/panel_columns_test.go` /
  `pkg/tui/overlays_test.go` if a status-rendering case is added (create the
  case beside the existing column tests).
- `frontend/src/components/settings/PlanReviewerModelGroup.vue` (create),
  `frontend/src/components/settings/SettingsSubagentsTab.vue`,
  `frontend/src/components/settings/PlanReviewerModelGroup.test.ts` (create)
- `frontend/src/views/SubagentsView.vue`, `frontend/src/views/SubagentsView.test.ts`,
  `frontend/src/lib/api.ts`, `frontend/src/locales/index.ts`
- `plans/README.md` (status row only)

**Out of scope** (do NOT touch, even though they look related):

- `pkg/tui/setup.go` / `pkg/tui/setup_test.go` — plans 311/312 own the
  `/connect` flow; this plan changes nothing there.
- `pkg/run/runner.go`, `pkg/run/config.go`, `pkg/run/subagent_catalog.go` —
  built-in dispatch resolution stays as-is; the ONLY runtime change here is
  Step 3c's plan-review approval pre-select in `subagent.go`. (Plan 314 touches
  `subagent_catalog.go` for the shared-custom merge — not here.)
- `frontend/src/components/subagents/SubagentConfigDrawer.vue` — ruling A
  keeps `builtinBody()` / `modelChoices` / `effortChoices` exactly as they
  are. (Plan 314 adds read-only global-custom rows to the drawer — not here.)
- `frontend/src/lib/settingsSchema.ts` — global groups self-register; adding
  paths there would double-register them.
- The "Subagent models" projection section and the global shared
  custom subagent registry — **plan 314**. The whole-`/config` 4-tab visual
  redesign stays deferred beyond both.
- `plans/*.html`, `plans/*.test.cjs`, `docs/plan/**` — frozen references.
- Provider-row prefill picker ("provider-pick" in the prototype) — deferred
  design nicety, not part of the contract; note it in the PR as deferred.

## Git workflow

- Branch: `advisor/313-shared-subagent-model-contract` from `dev`.
- Commit per step; conventional-commit style as in `git log`
  (e.g. `feat(approval): pick a reasoning effort after the plan approval's model`).
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: config — the plan-reviewer global row and one exported id check

In `pkg/config/agents.go` add to `AgentsSection`, beside `GoalEvaluator`, with
a comment citing plan 313 and the goal-evaluator precedent:
`PlanReviewer SubagentConfig `yaml:"plan_reviewer,omitempty" json:"plan_reviewer,omitempty"``.
Add `ValidatePlanReviewerModel(entry SubagentConfig, refResolves func(string) bool) error`
as the same two-line wrapper naming `agents.plan_reviewer` (mirror
`ValidateGoalEvaluatorModel`, lines 334–336). Do NOT refactor the existing
wrapper away. **Do NOT add a new id-check helper**: `config.ValidPrimaryAgentID`
already exists (`pkg/config/load.go:971–975`, quoted in "Current state") and
is already the gateway's check (`agents_api.go:555`). Step 5 reuses it; one
pattern, one helper, nothing new here.

In `pkg/config/load.go`, immediately after the goal-evaluator call
(lines 209–214), add the identical call for `r.Agents.PlanReviewer` with the
same `ResolveModelRef(r, "main", ref)` resolver and the same comment rationale
(global row must be checked on a fresh install).

Tests in `pkg/config/agents_test.go`, modelled on
`TestValidateGoalEvaluatorModel` (line 614): effort-without-model refusal
naming `agents.plan_reviewer`; bare model name refusal; row-id acceptance;
`provider/model` acceptance; empty entry acceptance. Add a load-level test
(where the existing goal-evaluator load test lives — find it with
`grep -n "GoalEvaluator" pkg/config/*_test.go`) proving a candidate with
`agents.plan_reviewer.reasoning_effort` set and no model fails to load.
Add a `ValidPrimaryAgentID` table test (the function exists but has no test
today): `main`, `review-agent`, `a_b` accepted;
`Main`, `-x`, `1x`, `` refused (pattern allows `_`, 63 chars max).

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/config -run 'PlanReviewer|ValidPrimaryAgentID' -count=1 -v` → the new tests appear as `--- PASS` lines, no `[no tests to run]`; `go build -tags fts5 ./...` → exit 0.

### Step 2: gateway settings — whitelist and normalize the new global row

In `pkg/gateway/api_extra.go` add beside lines 9267–9268:
```go
{path: "agents.plan_reviewer.model", section: "agents", kind: settingsText},
{path: "agents.plan_reviewer.reasoning_effort", section: "agents", kind: settingsText},
```
Generalize `normalizeGoalEvaluatorModelPath` (function at lines 9848–9862,
its path const at 9839) into
`normalizeSystemModelPath(cfg *config.Root, path string) error` handling both
`agents.goal_evaluator.model` and `agents.plan_reviewer.model`: pick the
`*SubagentConfig` by path, keep the empty-stays-empty and
resolve-to-row-id behavior verbatim, keep the Chinese refusal string
`"这个设置项要选一个已配置的模型"`. Update the single caller (line 9761) and
the doc comment to name both rows and plan 313. Keep the old function name
only if some other caller exists (`grep -rn normalizeGoalEvaluatorModelPath pkg/`);
otherwise rename fully — no compatibility shim.

Tests in `pkg/gateway/api_extra_test.go`: mirror
`TestSettingsPatchNormalizesGoalEvaluatorModelToRowID` (line 9940; the
goal-evaluator settings cases run from line 9969 on): a legacy label on
`agents.plan_reviewer.model` normalizes to the row id;
an unresolvable ref is refused with the same message; empty stays empty; the
settings write path accepts the two new paths and rejects an unknown sibling
(`agents.plan_reviewer.tools`).

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/gateway -run 'Settings|PlanReviewer|Normalize' -count=1 -v` → the new tests appear as `--- PASS` lines, no `[no tests to run]`.

### Step 3: gateway subagent rows — surface the global plan-reviewer row

In `pkg/gateway/subagents_api.go` add two additive fields to `subagentRow`:
`GlobalModelLabel string `json:"global_model_label,omitempty"`` and
`GlobalReasoningEffort string `json:"global_reasoning_effort,omitempty"``.
Change `systemSubagentRow(name string)` to `systemSubagentRow(cfg *config.Root, name string)`
and update its single caller (line 185). For `name == "plan-reviewer"` only,
read `cfg.Agents.PlanReviewer`; when its `Model` is non-empty resolve with
`config.ResolveModelRef(cfg, "main", ref)` and fill
`GlobalModelLabel = llm.FormatProviderModelDisplay(resolved.Provider, resolved.Model)`
from the resolved row (`config.AgentLLMProviderConfig` — the same helper
`subagentRowModelFields` uses, line 307) and
`GlobalReasoningEffort = strings.TrimSpace(cfg.Agents.PlanReviewer.ReasoningEffort)`; when the
ref does not resolve, leave the label empty (the row then reads as before —
never claim a model the engine cannot use). All other system names and all
builtin/custom rows keep today's shape byte-for-byte. Comment: the fields are
display-only; dispatch for system types is unchanged (plan 313, owner ruling).

Tests in `pkg/gateway/subagents_api_test.go`, modelled on
`TestSubagentListOrderKindsAndPaging` (line 126): with
`agents.plan_reviewer.model` set to a configured row id, the plan-reviewer row
carries the label and effort; with no global row, both fields are absent;
`guardian`/`goal-evaluator` rows never carry them; builtin rows'
`model_source` precedence (agent → global → conversation) is unchanged.
For the precedence case: `TestSubagentModelOptions` (line 567) does NOT
cover precedence — it tests the model-options endpoint (verified). The
file's only `ModelSource` assertion today is in
`TestSubagentBuiltinRewriteAndRestoreDefault` (line 338, assertion at 369).
Add one new test, modelled on that test and using `newSubagentFixture`
(line 40): a built-in with no own entry but a per-type definition
(`agents.subagent_models.<type>`, plan 315) reports `model_source: "global"`,
and an own entry wins over it (`"agent"`).

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/gateway -run 'Subagent' -count=1 -v` → the new tests appear as `--- PASS` lines, no `[no tests to run]`.

### Step 3b: gateway create — refuse a built-in type name as a primary agent id

In `pkg/gateway/agents_api.go` `handlePrimaryAgentCreate`, immediately after
the `config.ValidPrimaryAgentID(id)` check (line 555) and before the
read-modify-write, add the same verdict the TUI creation flow gives:

```go
if config.IsBuiltInSubagentDefinition(id) {
    http.Error(w, fmt.Sprintf("%q names a built-in subagent type, not a primary agent; choose a different id", id), http.StatusBadRequest)
    return
}
```

(`fmt` is already imported in this file; match the existing `http.Error`
style of the id-shape refusal at line 556.) This closes the web-side hole
where creating `explore`/`plan`/`guardian` lands a definition
`IsEffectivePrimaryAgent` never treats as primary (owner ruling: fix this
period, both surfaces).

Tests in `pkg/gateway/agents_api_test.go`, modelled on
`TestPrimaryAgentCreateRejectsDuplicateID` (line 341) and
`TestPrimaryAgentCreatePersistsDefinition` (line 320): creating `explore`
(and one system name, `guardian`) is refused with 400 and the type-name
message, and writes no definition; creating `review-agent` still succeeds.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/gateway -run 'PrimaryAgentCreate' -count=1 -v` → the new refusal test appears as `--- PASS`, no `[no tests to run]`.

### Step 3c: plan-reviewer approval picker pre-selects the global row

Owner ruling (2026-11-16): the approval-time user choice has the HIGHEST
priority; `agents.plan_reviewer` only supplies the DEFAULT pre-selection.
In `pkg/run/subagent.go` where the plan-review approval model decision is
built (grep the plan-review approval / `SubagentModelOverride` construction),
pre-select the resolved `cfg.Agents.PlanReviewer` row (id + effort) when the
user has not chosen otherwise; an empty/unresolvable global row leaves
today's approval default untouched. The stored approval decision still wins
at dispatch. Gateway: the plan-review approval payload exposes the global row
as the suggested default (additive field), web/TUI approval picker shows it
pre-selected.

Tests: with a global row set, the approval default equals it; a user pick
overrides it; empty global row keeps the old default; dispatch uses the
approval decision.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/run ./pkg/gateway -run 'PlanReview|PlanReviewer|Approval' -count=1 -v` → new tests `--- PASS`, no `[no tests to run]`.

### Step 4: TUI schema — the global pair and honest hints

In `pkg/tui/config_panel_schema.go`, in the `agents` section after the
goal-check pair (lines 211–218), add the mirror pair:
```go
{Path: "plan_reviewer.model", Label: "Plan reviewer model", Kind: configFieldText,
    Hint: "A model row id (the row chosen in Settings → Subagents); empty follows the primary agent"},
{Path: "plan_reviewer.reasoning_effort", Label: "Plan reviewer thinking level", Kind: configFieldText,
    Hint: "Empty follows that model row's default level; low, high and so on override it"},
```
with a comment citing plan 313 and the plan-299 precedent (system-dispatched,
global on purpose, two settings not a whole entry). Update the `definitions`
field Hint (line 190) to say that a definition keyed by a public built-in type
name is that type's install-wide default model, while an agent's own subagent
entry overrides it. Do not add a new section: the store's section list is
`config.Root`'s and the coverage test enforces that.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/tui -run 'ConfigPanelSchema|ConfigPanelListsEverySection' -count=1 -v` → `--- PASS: TestConfigPanelSchemaCoversEveryRootSection` and `--- PASS: TestConfigPanelListsEverySection` both appear (both exist today, at config_panel_test.go:568 and :120).

### Step 4b: TUI rich picker — a right-aligned status column (Primary marker)

The prototype marks a primary agent with a right-aligned `Primary` status
(`primary-listed` scene); the real picker has no status column. Add one,
faithful to the prototype's placement (right edge, quiet colour, dropped when
narrow). Four files, all additive:

1. `pkg/tui/channels.go` — add to `SelectItem` (after `Mark`, line 630):
   ```go
   // Status is a short right-aligned label drawn at the row's right edge —
   // the /config definitions list's "Primary" marker (plan 313, prototype
   // primary-listed). "" is no status. The renderer owns its colour; this
   // type keeps no lipgloss dependency.
   Status string
   ```
2. `pkg/tui/render.go` — add `status string` to `panelLine` (after `mark`,
   line 7610), with a comment mirroring `mark`'s voice.
3. `pkg/tui/panel_columns.go` —
   - `selectableColumns` (line 184): add a trailing variadic `status ...string`
     after `mark ...string`. It has THREE callers today
     (`overlays.go:5833`, `project_servers.go:132`, `tabbed_picker.go:282`);
     a trailing variadic is backward-compatible, so all three compile
     unchanged — only the overlays picker passes the new argument. Set
     `panelLine.status` from `status[0]` when present.
   - `columnRows` (line 109): when `l.status != ""`, reserve
     `statusW := displayLineWidth(l.status) + panelColumnGap` at the right
     edge, shrink the description column width by `statusW`, and append the
     status right-aligned on the FIRST row only (pad between the description
     and the status so the status sits at the row's right edge). Render it
     with `panelHintStyle` (`render.go:7564`, `Faint(true)` — the panel's
     quiet style, matching the prototype's `--quiet` status colour); the
     description column keeps its existing `l.style` rendering. When the row
     falls back to
     `columnRowsStacked` (narrow), DROP the status — the prototype hides
     `.item-status` when narrow. A row with no description but a status draws
     the name then the right-aligned status on one line.
4. `pkg/tui/overlays.go` — `richPickerState.panel` (line 5832): pass
   `item.Status` as the new trailing argument to `b.selectableColumns`.

`lineSelector.SelectRich` (channels.go:1032) is the non-TTY fallback and
draws only `Label`; it ignores `Status` — do not change it.

5. **Group spacing + spacious rhythm (owner 2026-11-16)**: consecutive
   category groups must be separated by ONE blank row (first group flush);
   the owner prefers a generous, un-cramped layout. Match the prototype:
   group label one blank line above (non-first), field rows carry modest
   vertical padding (`calc(var(--lh)/4)`), the context bar a full line of
   vertical padding, and the page title a larger presence (15px/700). In
   `panelBuilder.group` / row emission insert the blank row before non-first
   groups and the row/context breathing room. Add a picker test asserting the
   blank row between groups.
6. **Right-aligned value, no field-row arrow, right-aligned check (owner
   2026-11-16)**: the current value hugs the row's RIGHT edge
   (`margin-left:auto; text-align:right`), never floating mid-row; the status
   is a fixed right-aligned track that collapses when empty
   (`:empty{display:none}`); field rows carry NO trailing `›` arrow; in choice
   rows the ✓ check sits at the row's right end (after the status), right-
   aligned. Match the prototype (`.field-table .item-value{flex:0 1 auto;
   margin-left:auto;text-align:right}`, `.field-table .item .arrow{display:none}`,
   `.item.option .check{margin-left:auto}`). Add a picker test asserting the
   value's right edge is flush and equal across rows.
7. **Quiet success, no "Read only", no arrows, no effort-option descriptions,
   single-line subagent rows (owner 2026-11-16)**: a successful immediate save
   shows NO "Saved." notice (silence = success); only failures and clamp
   adjustments speak. Delete "Read only" labels (overview status, file heading
   / footer); read-only is conveyed by absent inputs + the file note. Delete
   the trailing `›` arrow on field AND overview rows. Thinking-effort (and
   other plain string-enum) choice options carry no description line.
   Subagent-model rows are SINGLE-line: type name left, model value right
   (rowLabel, host parenthetical only when provider/model is ambiguous, value
   track wide enough not to truncate), effort status right on one line; the
   endpoint lives in the context help, not as a second line under the label.
8. **No filler copy (owner 2026-11-16)**: delete explanatory filler such as
   "Enter saves immediately.", "Saves on Enter", "Enter to open this item.",
   "Use the keyboard or click an item to continue." — the interaction is
   self-evident from the key hints; only field-specific help, errors and clamp
   notices speak.
9. **No permanent context/help strip (owner 2026-11-16)**: delete the bottom
   context bar (selected-row help + meta + `n / m` counter) and the scroll
   above/below counter entirely. Errors / clamp / guard notices render as a
   TRANSIENT line at the top of the panel body only while present; success is
   silent. Field-specific help belongs in the editor, not a permanent strip.
10. **Bottom region = key hints only (owner 2026-11-16)**: the area below the
    panel shows ONLY the necessary operation shortcuts (Esc / Enter / a / d /
    ↑↓ per view). Delete the simulated composer strip ("Configuration is
    open", "project / dev", "Ready") and any other non-shortcut info there.
    The fields view shows NO right-hand "Enter Open / save" hint; only the
    left action keys (Esc / a / d) remain.
11. **Adaptive choice-row height (owner 2026-11-16)**: now that effort/enum
    options carry no description, choice rows are ONE line tall (plus the
    standard row breathing padding); rows that still carry a description
    (model endpoints, confirmations) grow to two lines naturally. Do not keep
    a fixed 3-line option height.
12. **Restore-default only on non-choice editors (owner 2026-11-16)**: choice
    pickers (bool/enum/model/effort) have NO "Default"/restore-default button
    or unset operation; text/number/multiline/secret editors KEEP the footer
    `Default` button (and empty-input = unset) as the restore-default path.
13. **No option descriptions, no implicit-default jargon (owner 2026-11-16,
    enumerate all)**: EVERY choice option (bool On/Off, model rows, enum
    values, effort levels, confirmation items) carries NO description line.
    Delete implicit-default jargon users cannot parse — "Row default",
    "Follow model row": an empty effort renders as blank (no override), and
    the effort picker has no "Row default" option. Model disambiguation stays
    in the label (rowLabel host parenthetical), not a description.
14. **No editor facts block (owner 2026-11-16)**: the edit view shows only the
    input + its one-line help + inline error; delete the Current / When saved
    / Range facts table. (The read-only file view keeps its facts table — it
    has no input to convey values.)
15. **Memory model pickers = the global model picker (owner 2026-11-16)**:
    `memories.extract_model` / `consolidation_model` use the SAME choice UI as
    primary/subagent model config: a global-model-row list (rowLabel, host
    parenthetical when ambiguous) plus a "Follow conversation" option
    (`current`); value column shows the model label / "Follow conversation",
    right-aligned; no free-text "current" input.
16. **Model & effort pickers = the 311 /connect prototype design (owner
    2026-11-16, no second semantics/design)**: match
    `plans/311-connect-prototype.html` and the real TUI exactly — effort page
    titled "Default reasoning effort" (accent) with subtitle "Choose the
    default for <provider/model>."; options Off (wire `none`, only when the
    model supports off) + supported levels (Low/Medium/High/xHigh/Max) +
    "Provider default" (wire `''`) last; side-column annotations Off→`none`,
    Provider default→`no effort parameter sent`; model pickers show the
    endpoint in a side column with subtitle "Choose one model; each row shows
    where requests go."; fixed label column + single-line rows + right check.
    levelLabel mapping: none→Off, ''→Provider default, xhigh→xHigh, max→Max.
17. **Status never wraps (owner 2026-11-16)**: the row status column is wide
    enough for the longest status ("Provider default", 16ch) and
    `white-space:nowrap`; a status must always render on ONE line.

Tests: add a case beside the existing `columnRows` tests in
`pkg/tui/panel_columns_test.go` (grep `func Test.*olumn` there for the
exemplar): a row with a status renders it at the right edge on the first
line; a narrow row (stacked) omits it; a row without a status is unchanged
byte-for-byte. The `SelectItem.Status` → picker wiring is covered by Step 5's
panel test (a definitions row with `primary: true` carries `Status ==
"Primary"`).

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/tui -run 'Column|Panel|Picker|Rich' -count=1 -v` → the new status case appears as `--- PASS`, existing column/picker tests stay green, no `[no tests to run]`; `gofmt -l pkg/tui` → empty.

### Step 5: TUI panel — source-aware rows, system refusal, guided creation

Three changes in `pkg/tui/config_panel.go`, all inside the generic editor.
Remember the nesting fact from "Current state": `editRows` passes the same
`field` down at every level, so **every guard below keys on the actual
`path []string`, never on `field.Path` alone**.

1. **Row summaries.** Extend the method `rowItems` (line 692) to
   `func (p *configPanel) rowItems(doc any, path []string, container any, keys []string, list bool) ([]SelectItem, []int)`
   (update its single caller, line 670, which has `doc` and `path` in scope)
   and replace the `configRowDescription(value)` call with
   `configRowSummary(doc, path, key, value)`: default to today's
   `configRowDescription`; then the subagent special cases below, all pure
   over the doc so tests need no terminal. **Separately**, when building each
   `SelectItem`, set its `Status` field (Step 4b): for
   `len(path) == 1 && path[0] == "definitions"` and a row map with
   `primary == true`, `Status = "Primary"` (the prototype's right-aligned
   `primary-listed` marker — NOT a description prefix). All other rows leave
   `Status` empty. `configRowSummary` itself does not handle the primary
   case; the marker lives in `Status`, the description stays value-only
   (`› N items`) for definitions rows.
   The subagent summary cases:
   - `len(path) == 3 && path[0] == "definitions" && path[2] == "subagents"`
     (the row list of one agent's subagents; each row value is a map with a
     `name`). Read the name as the loader does:
     `strings.ToLower(strings.TrimSpace(...))`. For a public built-in
     (`agent.PublicSubagentNames`) answer with the resolution order read
     from the same section doc:
     - the entry's own `model` non-empty → `"agent override: <ref>"`;
     - else look up `subagent_models.<name>` in the doc (plan 315's per-type
       global row: `{model, reasoning_effort}`, model a global-table row id).
       Resolve the row id against the global table for the display label.
       Found →
       `"global default: <row label> / <effort>"` where `<effort>` is
       `reasoning_effort`, or `Row default` when absent and `Off`
       when it is `none` (the prototype's `effortLabel`, :50 — the effort
       part is always shown);
     - neither → `"Follow conversation model"` (prototype verbatim, :54).
     For a system name (`agent.SystemSubagentNames`) answer
     `"Dispatched by the system. Not configurable per agent; remove this entry."`
     (prototype verbatim, :118).
   The `"agent override: "` / `"global default: "` prefixes are this plan's
   adaptation of the prototype's `Agent` / `Global` status column (:119) —
   keep them; do not invent a status column.
2. **System guard.** In `editRow` (line 748), before building the menu:
   when `len(path) == 3 && path[0] == "definitions" && path[2] == "subagents"`
   and the row map's lower-cased `name` is in `agent.SystemSubagentNames`,
   return `(doc, "Dispatched by the system. Not configurable per agent; remove this entry.")`
   without writing — the prototype's refusal (:121), earlier than the
   loader's save-time verdict (which still applies with its own words).
3. **Guided primary creation.** In `addRow` (line 798), for map rows at
   **the top level only**: `field.Path == "definitions"` AND
   `len(path) == 1 && path[0] == "definitions"` (without the path check the
   guard would hijack adding any key inside an existing agent definition,
   because the same field travels down). There:
   - validate the key with `config.ValidPrimaryAgentID` (refusal:
     `"use a lowercase agent id: letters, digits, hyphens or underscores, starting with a letter"`;
     the loader's pattern allows `_` — the prototype's stricter regex is
     NOT the rule here, see "Current state");
   - refuse a key naming a built-in subagent type:
     `config.IsBuiltInSubagentDefinition(key)` → refusal
     `"\"<key>\" names a built-in subagent type, not a primary agent; choose a different id"`
     (owner ruling 2026-11-16: the definitions page creates primary agents
     only; such a key would land a row `IsEffectivePrimaryAgent` never
     treats as primary — see "Current state");
   - keep the existing empty-key and duplicate refusals;
   - ask an optional display name with one more
     `p.sel.Input("Display name (empty uses the title-cased id)", "")`
     (cancel → write nothing, like the key prompt);
   - skip `chooseNewValue` and seed
     `map[string]any{"display_name": <name or title-cased key>, "primary": true, "mcp_servers": []any{}, "subagents": []any{}}`
     (the prototype's seed, :135). Title-case = the prototype's `titleCase`
     (:109): underscores become spaces, first character upper-cased, rest
     as written (`review-agent` → `Review-agent`, `a_b` → `A b`); a small
     local helper;
   - assign and open `editRows` on the new row exactly as the object branch
     does today, **with the prototype's guidance notice** (owner ruling
     2026-11-16: 补上). Extend `editRows` (line 665) to
     `func (p *configPanel) editRows(doc any, path []string, field configField, initialNotice ...string) (any, string)`
     — variadic, so every existing caller stays unchanged; initialize its
     `notice` from `initialNotice` (first element, or `""`). The creation
     branch calls
     `p.editRows(updated, childPath, field, "Primary agent created in your draft. Add a model provider, then choose its default model.")`
     (prototype verbatim, `addEntry` :135); the loop already clears the
     notice after the first render, so it shows once. Other collections
     (and every nested level under `definitions`) keep today's type menu
     untouched.

Tests in `pkg/tui/config_panel_test.go`, modelled on
`TestConfigPanelSavesATriStateBoolean` (line 187) and
`TestConfigPanelShowsTheValidatorsRefusal` (line 284): the agents section
lists the two new fields; a definitions row with `primary: true` carries
`Status == "Primary"` (and its description stays value-only, no prefix); a
built-in subagent row with no override shows the
global default from a type definition in the same doc (including the
` / Row default` suffix when the entry sets no effort, and the
`default_model`-named entry winning over the first), with an override shows
`agent override`, with neither shows `Follow conversation model`; a
system-named row refuses to open with the exact notice above and writes
nothing; adding `review-agent` under definitions seeds `primary: true`,
the title-cased display name and the three empty collections, opens it,
and the new page shows the guidance notice verbatim;
a typed display name wins over the title-cased key; adding `Bad ID` is
refused; adding `explore` (a built-in type name) is refused with the
type-name message and writes nothing; adding `main` again is refused; adding a key INSIDE
`definitions.main` still shows the ordinary type menu (the guard does not
fire there); saving a section containing a system-named subagent entry
surfaces the loader refusal (existing validator path).

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/tui -run 'ConfigPanel' -count=1 -v` → the new tests appear as `--- PASS` lines, no `[no tests to run]`; `gofmt -l pkg/tui` → empty.

### Step 5b: no raw config keys on any /config page (owner 2026-11-16)

The owner forbids exposing raw dotted config fields (e.g.
`agents.defaults.stream_idle_timeout_seconds`) to users on ANY /config page.
The prototype enforces this (context bar + review diff use friendly labels via
a path→label resolver; regression check "No raw config keys on any page").
Bring the real panel in line:

- `pkg/tui/config_panel.go:1053` — `fmt.Errorf("%s is not an object or a list",
  strings.Join(path, "."))` leaks a dotted path. Replace the path with the
  friendly label of the row/field being edited (resolve via the same
  label logic the row list uses: field `Label`, or the entry's
  display_name/name, or `Item N` for list indices).
- Sweep `config_panel.go` for every other place a dotted path or `field.Path`
  reaches user-visible text (prompts, notices, save review, errors):
  `grep -n 'strings.Join(path\|field.Path\|configFieldPath' pkg/tui/config_panel.go`.
  Internal lookups may keep paths; anything rendered must use labels.
- Data-row identities (agent id, server name, list index) stay as row labels;
  never concatenate them into a dotted config path for display.
- **Concise copy (owner 2026-11-16)**: the inherit option reads `Default`
  (not "Use default"); the section is titled `Subagent models` (no
  "(global)"); the shared server field is `MCP servers` (no "Shared");
  help/hint/caption text carries no raw config paths and no redundant
  qualifiers. Match the prototype's wording verbatim.
- **No restore-default row in choice lists (owner 2026-11-16)**: bool/enum
  pickers list only real values (On/Off; the enum's options). The
  "Default / inherit / remove this override" third row is DELETED. Returning
  to unset is a footer `Default` action on the choice page (bool/enum only),
  mirroring the edit page's Default button; model/effort pickers keep their
  concrete "Follow …" / "Row default" options (those are values, not
  restore-default rows). Update `editBool3`/enum picker accordingly.
- **Approval preset label (owner 2026-11-16)**: `config_panel_schema.go:117`
  `OptionLabels["workspace_write"]` reads **"Workspace write"** (not
  "Default"/"Workspace access"), matching `/permissions` and web (plan 316
  Step 6 unifies all surfaces).
- **Enter saves immediately (owner 2026-11-16, replaces draft/stage/review)**:
  every confirmed edit (Enter on edit/choice/add/delete) persists to the store
  at once; there is NO draft, NO "Unsaved changes"/"Modified"/"Not saved yet",
  NO review/&-save page, NO leave-with-unsaved prompt, NO Ctrl+S. A transient
  "Saved." notice confirms; a failed write keeps the previous value and shows
  an error. **Cross-field constraints under immediate save** (hook type http
  needs a url; effort needs a model): commit the edited field together with
  its companions atomically, or guard the dependent picker (as the prototype
  guards effort via `requires`); never silently roll back a type switch the
  user just made. The prototype drops hook cross-field refusal from its
  validator and puts required-field guidance in field help; the real panel
  must pick one of the two strategies and pin it with a test.
- **No success-confirmation copy, no "Read only", no row arrows, no effort
  option descriptions (owner 2026-11-16)**: a successful immediate save shows
  NO "Saved." notice (silence = success); only failures and clamp adjustments
  speak. Delete "Read only" labels (overview status, file heading/footer) —
  read-only is conveyed by the absence of inputs + the file note. Delete the
  trailing `›` arrow on field AND overview rows. Thinking-effort choice
  options carry no description line (nor do other plain string-enum options).

Tests: a golden/panel test asserting that opening a nested field and staging a
change renders the friendly label and that no rendered string matches
`[a-z_]+\.[a-z_]+` config-path shape (model on the existing panel tests).

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/tui -run 'ConfigPanel' -count=1 -v` → new test `--- PASS`; `gofmt -l pkg/tui` empty.

### Step 6: web settings — the plan-reviewer group beside the goal check

Create `frontend/src/components/settings/PlanReviewerModelGroup.vue` as a
structural copy of `GoalCheckModelGroup.vue` (read it fully first): same
`useModelChoices` + `useSettingsForm` machinery, `MODEL_PATH =
'agents.plan_reviewer.model'`, `EFFORT_PATH = 'agents.plan_reviewer.reasoning_effort'`,
`group.id = 'plan-reviewer-model'`, `titleKey = 'subagents.planReviewer.title'`,
same clear-effort-on-model-change and same `saveGroup`/`cancelGroup` wiring,
same refusal rendering. Mount it in `SettingsSubagentsTab.vue` directly after
`<GoalCheckModelGroup :form="form" />` with a one-line comment citing plan 313.
Add i18n keys in `frontend/src/locales/index.ts` beside the goal-check keys
(grep `subagents.goalCheck.title`): zh `subagents.planReviewer.title` =
`/plan 复核模型`, plus the group's row labels/hints mirroring the goal-check
wording; en block mirrors (`Plan review model`, …). Reuse existing effort
label keys; do not invent a second effort vocabulary.

Create `PlanReviewerModelGroup.test.ts` modelled on
`frontend/src/components/settings/CronSettingsTab.test.ts`: renders the two
rows; choosing a model writes the row id and clears effort; choosing effort
writes it; save calls the settings API with exactly the two paths; a refusal
renders inline and keeps the draft.

**Verify**: `cd frontend && pnpm typecheck && pnpm vitest run src/components/settings` → exit 0, all pass.

### Step 7: web subagent list — show the global pin on the system row

In `frontend/src/lib/api.ts` add the two optional fields to
`SubagentConfigRow`: `globalModelLabel?: string`, `globalReasoningEffort?: string`.
In `SubagentsView.vue` change `systemModel(row)` (lines 339–343): when
`row.globalModelLabel` is non-empty return it plus
`' ' + effortLevelLabel(row.globalReasoningEffort)` only when the effort is
non-empty (import `effortLevelLabel` already present, line 229); otherwise keep
today's i18n fallback unchanged. **Use a SPACE, not `·`**: the owner's
2026-11-16 ruling bans `·` connectors in ALL copy (plan 316 sweeps the
existing ones, including `SubagentsView.vue:103`); this plan must not
introduce any new `·`. The Primary marker is a `Status` column (Step 4b),
not a `·` prefix. Update
`SubagentsView.test.ts` (existing system-row case): with the global fields the
cell shows label + effort; without them the old `审批时选择` text stays.

**Verify**: `cd frontend && pnpm vitest run src/views/SubagentsView.test.ts && pnpm run build` → exit 0.

### Step 8: full gates and index

Run every command in the table once, in order, including the two full-package
Go runs and the prototype regression (start the static server first; it is
read-only). Then update the 313 row in `plans/README.md` to DONE with the
evidence line (test counts, hashes of the two frozen references re-verified
with `shasum -a 256`).

**Verify**: all commands exit 0; `git status --short` shows only in-scope files.

## Test plan

- New Go tests: validator + load refusal + id pattern (step 1), settings
  whitelist/normalize (step 2), system-row global fields + unchanged
  precedence (step 3), panel summaries/guard/creation (step 5). Exemplars
  named per step.
- New web tests: `PlanReviewerModelGroup.test.ts` (step 6), extended
  `SubagentsView.test.ts` (step 7).
- Regression: the full `pkg/config`, `pkg/gateway`, `pkg/tui` packages and the
  frontend settings + subagents suites must stay green; the prototype suite
  (26 results: 25 checks + the nested-transition result) is the visual/interaction contract and must stay green untouched.
- No new e2e spec in this plan; the existing exit-plan e2e specs must stay
  green if run (`FOREBRAIN_E2E_SPEC="e2e/exit-plan-approval.spec.ts e2e/office-plan-approval.spec.ts"`), because plan-reviewer dispatch is their subject.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `go build -tags fts5 ./...` exits 0; `go vet -tags fts5 ./pkg/config ./pkg/gateway ./pkg/tui` exits 0; `gofmt -l pkg/config pkg/gateway pkg/tui` empty
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/config ./pkg/gateway ./pkg/tui -count=1` exits 0, including the new tests named above
- [ ] `cd frontend && pnpm typecheck && pnpm run build` exit 0; `pnpm vitest run src/components/settings src/views/SubagentsView.test.ts` exits 0
- [ ] `grep -rn "plan_reviewer" pkg/config/agents.go pkg/config/load.go pkg/gateway/api_extra.go pkg/tui/config_panel_schema.go` shows the field, the load call, the two whitelist paths and the two schema fields
- [ ] `grep -n "normalizeGoalEvaluatorModelPath" pkg/gateway/*.go` returns no matches (renamed, single caller updated)
- [ ] `grep -n "Status string" pkg/tui/channels.go` shows the new field; `grep -n "item.Status" pkg/tui/overlays.go` shows the picker wiring; `grep -n "status" pkg/tui/panel_columns.go` shows the right-aligned rendering
- [ ] `grep -n "IsBuiltInSubagentDefinition" pkg/gateway/agents_api.go` shows the create type-name guard (Step 3b)
- [ ] no raw dotted config path rendered by the panel (Step 5b test green; `strings.Join(path, ".")` gone from user-visible text)
- [ ] `shasum -a 256 docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html` = `aef1aa8cac4e86fd258706d56acb4b8b8c3507bf575356ddae4106bd51ac63b0`
- [ ] `shasum -a 256 plans/config-tui-prototype.html` = `95e75dedd9479317e23464d1978efc6a9301e3dfead0efade0bcbee9c8e9e688`
- [ ] `cd plans && node config-tui-prototype.test.cjs` reports 26 passed, zero browser errors
- [ ] No files outside the in-scope list modified (`git status --short`)
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- The code at the locations in "Current state" doesn't match the excerpts
  (drift since `7611d6cc`), in particular `subagentRowModel`'s precedence or
  `systemSubagentRow`'s shape.
- `rowItems` or `normalizeGoalEvaluatorModelPath` turns out to have more than
  the one caller named here — the rename/signature change is only safe with
  the stated call graph.
- `selectableColumns` turns out to have callers that would break under the
  new trailing variadic `status` argument (variadic is backward-compatible,
  but verify `grep -rn "selectableColumns(" pkg/tui/` — every existing call
  must still compile unchanged); or the right-aligned status cannot be drawn
  without restructuring `columnRows`' wrap/elide logic beyond the additive
  first-row suffix described — report, do not redesign the column engine.
- The settings form machinery rejects a self-registering group (the
  goal-check group proves it works; if the new group's paths never reach
  `form.draft`, do not patch `settingsSchema.ts` — report).
- A step's verification fails twice after a reasonable fix attempt.
- The fix appears to require touching `pkg/run` (that is plan 314's scope),
  `pkg/tui/setup.go`, the web drawer's `builtinBody` (ruling A keeps it), or
  any frozen prototype/reference file.
- You discover the assumption "the loader's section validator runs on
  `ConfigStore.UpdateSection`" is false (then the TUI system guard and the
  refusal surfacing need a different home — report, do not invent one).

## Maintenance notes

- The contract's single source of truth for precedence stays
  `subagentRowModel` / `run.SubagentOwnModel`; the TUI summary helper
  (step 5) re-reads the same order from the section doc for display. If
  dispatch ever gains a new source (e.g. a session pin surfaced in config),
  update the helper and the prototype together.
- `agents.plan_reviewer` is the **default pre-selection** of the plan-review
  approval model picker (owner 2026-11-16): the approval-time user choice has
  the HIGHEST priority; the global row only pre-selects the default. Step 3c
  wires this. Reviewers should check the approval decision still wins over
  the global row and that an empty global row leaves today's approval default.
- The prototype's `provider-pick` prefill and the whole-panel visual redesign
  remain deferred; the prototype stays the accepted design reference for
  both.
- **Plan 314 follows this plan**: it builds the "Subagent models"
  projection section (a `StoreSection` mechanism over the `agents` store),
  relocates the `plan_reviewer` pair from the `agents` section into it, adds
  the per-type default rows there, and adds the global shared custom
  subagent registry (`agents.shared_subagents`) with per-agent enable
  (default disabled) and read-only consumption on agent pages. The
  `SelectItem.Status` column added here is what 314's section rows reuse for
  their `Global default` / `Global row` markers. Reviewers of 314 should
  check the projection saves the whole `agents` doc without losing keys it
  does not display.
- Review scrutiny points: additive JSON fields only (no renamed/removed
  keys), the normalize rename's single caller, the seeded `primary: true`
  (a created agent must never land as non-primary), the create type-name
  guard on BOTH surfaces (TUI Step 5 + gateway Step 3b), the status column's
  backward-compatible variadic and narrow-screen drop, and i18n key parity
  zh/en.

## 2026-11-17 reasoning-semantics correction (read before Step 4)

> Prototype gate: **owner re-confirmed both revised prototypes on 2026-11-17**
> ("原型都已确认"). New hashes: config-tui-prototype
> `95e75ded…e688`, docs reference `aef1aa8c…63b0`; the regression is
> 26/26. Proceed with the hashes above; the old `1d494d90…`/`12f9fd82…`
> values are history.

The owner's audit of "thinking on/off + thinking levels" across engine, TUI,
gateway and web found this plan's `reasoning_effort` rows (`configFieldText`
in Steps 4–5, and the gateway `settingsText` entries in Step 2) are the only
places a level is free text. Everything else (`/model`, the exit gate, the web
chat chip, the web subagent drawer, `/connect` after plan 311) offers a list.
A free-text row lets someone store `Highest`, `none` for a model that cannot
turn thinking off, or a level its protocol silently drops — and no validator
catches it (`pkg/config/agents.go:313-328` only requires a model).

Keep this plan's steps as written, with three constraints:

1. **Do not invent a fourth label/order table.** Display levels through
   `turn.EffortEnglishLabel` and in `turn.OrderedEfforts` order.
   `Highest` is not a label in this repository; `xHigh` is.
2. **Keep the text field, but mark it as a stopgap.** Add a comment on both
   `reasoning_effort` schema rows citing this section and plan 319, which
   replaces them with a picker sourced from the selected model row's
   `turn.SetupReasoningEfforts` + `Off` when off-capable and sendable + empty
   `Row default`. Do not build the picker here: it needs 318's helper and
   319's field kind, and would collide with the parallel 311/316 work on
   `setup.go`.
3. **Leave validation to 318 Step 6** for the gateway subagent write path;
   do not silently normalize a stored level in this plan. The config
   prototype's effort rows (`plans/config-tui-prototype.html`,
   `kind:'effort'`) remain the interaction target: 318's picker must match
   them (`Row default` for empty, `Off` only when sendable).

Nothing else in this plan changes.