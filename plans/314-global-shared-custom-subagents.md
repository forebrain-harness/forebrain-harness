# Plan 314: Global shared custom subagents — a "Subagent models" TUI section, a shared registry, and read-only + enable/disable consumption on every agent page

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: this plan is written on top of plan 313
> landing first. Run `git status --porcelain -- <the in-scope paths below>`
> and `git diff <baseline> -- <the same paths>`. The baseline is the commit
> 313 landed on (record it in `## Status` when you start; if 313 has not
> landed, STOP — this plan relocates a field 313 adds). Compare the
> "Current state" excerpts against the live code before proceeding; on a
> content mismatch, STOP. Line numbers may drift after 313; the excerpts and
> symbol names are the contract, not the line numbers.
>
> **2026-11-17 reconcile (HEAD `fcb241bc`, pre-313 anchors re-verified live)**:
> `ValidateAgentSubagents` agents.go:252 with its 3 production call sites
> (load.go:942, gateway subagents_api.go:601, tests) intact;
> `subagentKindFor` subagents_api.go:348, 5 call sites; `subagentNames` :151
> (single call :167); `withConfigSection` api_extra.go:5988;
> `validateLoadedRootForAgents` agents_api.go:650; routes api_extra.go:124-128;
> `subagentConfigNamePattern` agents.go:236; `SubagentCatalogFor`
> subagent_catalog.go:62, `ResolvePublic` :125, `Prompt` :153, `ToolPolicy`
> :199, `EnumDescription` :224; exemplars `newSubagentFixture` :40 /
> `TestSubagentBuiltinRewriteAndRestoreDefault` :338 /
> `TestSubagentModelOptions` :567 (subagents_api_test.go);
> `pkg/run/subagent_catalog_test.go` exists. Only drift since `7611d6cc` in
> this plan's surface: the merged catalog_provider commits' additive
> `api.ts`/`locales` diff (same exception as plan 313's drift check) —
> `SubagentConfigRow` untouched. Frozen prototype hash re-verified
> (`95e75ded…`; re-frozen by the 2026-11-17 reasoning audit — the effort
> picker no longer invents Off and labels empty `Row default`); its regression now reports **26 passed**, not 20.
> **Prototype gate cleared**: owner re-confirmed both revised prototypes on
> 2026-11-17 ("原型都已确认"); config `95e75ded…e688`, docs `aef1aa8c…63b0`.

## Status

- **Priority**: P1
- **Effort**: XL
- **Risk**: HIGH (loader validation + dispatch resolution + a new stored
  registry + two surfaces; a wrong default-enable rule silently dispatches
  subagents the user never turned on)
- **Depends on**: plans/315-delete-llm-providers-global-model-picking.md AND
  plans/313-shared-subagent-model-contract.md (must land
  first: it adds `agents.plan_reviewer`, the `SelectItem.Status` column, and
  the `plan_reviewer` pair in the `agents` section that this plan relocates).
  Do not run in parallel with 311/312 (they own `pkg/tui/setup.go`).
- **Category**: feature
- **Planned at**: commit `7611d6cc` + plan 313, 2026-11-16
- **Landed**: `f48047b2`（2026-11-18「feat(web): the settings page owns the shared subagent registry」）＋同批 TUI 侧。2026-11-24 reconcile 在 dev `cba1bfba` 复核：`frontend/src/components/settings/GlobalSubagentsGroup.vue` 与 `.test.ts` 在位，`pkg/tui/config_panel_test.go` 的 `TestConfigPanelAgentPageListsSharedSubagentsDisabled` 在位。

## Why this matters

The owner's ruling (2026-11-16, final): the web settings › subagents tab must
support creating **globally shared custom subagents** — one definition every
primary agent can consume. On each primary agent's subagent page, a globally
shared subagent (whether a custom one or a built-in type's global default) is
**read-only plus enable/disable**; the agent never edits the shared
definition itself. A globally shared custom subagent is **disabled for every
primary agent by default** and runs only after the user explicitly enables it
on that agent's subagent page. The TUI creation/management surface for these
shared subagents is the prototype's **"Subagent models"** section.

Ruling A (same day) keeps per-agent built-in overrides exactly as they are:
the read-only rule governs the *shared definition*, not this agent's own
override of a built-in. Plan 313 already shipped the model contract and the
`SelectItem.Status` column; this plan adds the shared registry, the projection
section, and the consumption semantics on both surfaces.

The frozen interaction contract is `plans/config-tui-prototype.html`
(sha256 `95e75dedd9479317e23464d1978efc6a9301e3dfead0efade0bcbee9c8e9e688`):
the `subagent_models` section (html:69) lists the four public per-type default
rows plus the `plan-reviewer` global row, with a right-aligned status column
(`Global default` / `Global row`). **Plan 315 deletes all `llm_providers`
storage**: per-type built-in defaults become global-table row references
`agents.subagent_models.<type>` = `{model, reasoning_effort}` (the same shape
as the plan-reviewer / goal-evaluator global rows). The section is therefore
a *projection* over the `agents` store (per-type rows read
`subagent_models.<type>`, plan-reviewer reads `plan_reviewer`, shared customs
read `shared_subagents`); the shared custom registry is the only NEW storage
this plan adds.

## Current state

All excerpts verified at `7611d6cc` (re-verify after 313 lands; 313 adds
`PlanReviewer` to `AgentsSection` and `Status` to `SelectItem`).

### Storage and validation (`pkg/config`)

- `AgentsSection` (`agents.go:13–21`, after 313 also holds `PlanReviewer`):
  ```go
  type AgentsSection struct {
      Defaults      AgentDefaults              `yaml:"defaults,omitempty" json:"defaults,omitempty"`
      Definitions   map[string]AgentDefinition `yaml:"definitions,omitempty" json:"definitions,omitempty"`
      GoalEvaluator SubagentConfig             `yaml:"goal_evaluator,omitempty" json:"goal_evaluator,omitempty"`
      PlanReviewer  SubagentConfig             `yaml:"plan_reviewer,omitempty" json:"plan_reviewer,omitempty"` // plan 313
  }
  ```
- `SubagentConfig` (`agents.go:67–87`): Name, Description, SystemPrompt,
  Model, ReasoningEffort, Tools (`read_only|edit|pick`), AllowedTools,
  Continuable `*bool`, Enabled `*bool`. A custom subagent's full definition.
- `ValidateAgentSubagents` (`agents.go:252–307`): for each `def.Subagents`
  entry — name must match `subagentConfigNamePattern`; system names refused
  (`"%q is dispatched by the system and cannot be configured"`, :273); public
  built-in entries restricted to model/effort/prompt/enabled (:275–278);
  **every other (custom) name requires `description` AND `system_prompt`**
  (:280–286), a valid `tools` tier, and `allowed_tools` for `pick`. This is
  the rule that must learn reference entries (a name-only enable marker for a
  shared subagent).
- `validateSubagentModel` (`agents.go:313–328`): empty model + non-empty
  effort → `"reasoning_effort requires model"`; a model without the
  `provider/model` shape must resolve via `refResolves`; else
  `"model %q must be provider/model"`. `ValidateGoalEvaluatorModel`
  (:334–336) is the two-line wrapper naming `agents.goal_evaluator`; 313 adds
  `ValidatePlanReviewerModel` beside it.
- `load.go`: `builtInSubagentDefinitionNames` (:21–30, the four public types
  plus `guardian` and three `cavecrew-*`); `IsBuiltInSubagentDefinition`
  (:913–917); `IsEffectivePrimaryAgent` (:902–910); `validatePrimaryAgents`
  (:919–955) calls `ValidateAgentSubagents` per definition with
  `ResolveModelRef(r, id, ref)`; the goal-evaluator/plan-reviewer global rows
  are validated outside it (:209–214 + 313's call) with
  `ResolveModelRef(r, "main", ref)`. `subagentConfigNamePattern`
  (`agents.go:236`, `^[a-z][a-z0-9-]{1,62}$`) is the
  loader's name rule (lower-case letters/digits/dashes, starting with a
  letter, 2–63 chars).

### Dispatch (`pkg/run/subagent_catalog.go`)

- `SubagentCatalogFor` (:64–86): builds `entries` (map by name) and `customs`
  (slice, config order) from `def.Subagents`; every non-public entry becomes a
  custom. `goalCheck` is carried separately (undispatchable).
  ```go
  for _, entry := range def.Subagents {
      entry.Name = subagentKey(entry.Name)
      catalog.entries[entry.Name] = entry
      if !isPublicSubagentName(entry.Name) {
          catalog.customs = append(catalog.customs, entry)
      }
  }
  ```
- `Public()` (:104–120): enabled built-ins (in `agent.PublicSubagentNames`
  order) then enabled customs (`subagentConfigEnabled(entry)`).
- `ResolvePublic` (:124–147): a disabled custom is refused
  (`"subagent_type %q is disabled for this agent"`); an unknown name lists
  `publicNames()`.
- `Model` (:175–195): reads `entries[name]`, resolves
  `ResolveModelRef(c.cfg, c.agentName, configured)`; a row id resolves
  globally (the id branch ignores the agent name), a legacy label resolves
  against the agent's view. `goalCheck` is the one global row consulted.
- `Prompt` (:152–163), `ToolPolicy` (:199–220), `EnumDescription` (:225–252),
  `configures` (:265–274), `customDefinition` (:288–300),
  `subagentConfigEnabled` (:318–321, **nil means enabled**),
  `isPublicSubagentName` (:306–315), `subagentKey` (:302–304).
- `ForProjectType` (:90–93) narrows office/batch built-ins to
  `agent.OfficeSubagentNames`; customs pass through.
- `refuseGoneCustomSubagent` (`pkg/run/subagent.go:3120–3128`) and
  `continueSubagentExecution` (:3153–3160) consult
  `SubagentCatalogFor(...).configures/ResolvePublic` — a shared custom that
  is enabled for the agent must answer `configures` true and resolve.

### Gateway (`pkg/gateway`)

- Routes (`api_extra.go:104–130`, chi-style `agents := api.Group("/agents")`):
  `agents.Get("/primary/:id/subagents", s.handleAgentSubagentsList)` (:124),
  `Post` create (:125), `Get/Put/Delete .../subagents/:name` (:127–129),
  `Put .../subagents/:name/enabled` (:130). New shared-registry routes go in
  this group (e.g. `agents.Get("/subagents/shared", ...)`), distinct from
  `/primary/:id/subagents`.
- `subagentKind` consts (`subagents_api.go:43–49`): `builtin`, `custom`,
  `system`. `subagentNames` (:151–163): this agent's custom names (non-public,
  non-system entries) then public then system. `subagentRows` (:165–176)
  renders in that order via `subagentRowFor` (:180–192).
- `subagentKindFor` (:348–360): system → builtin → custom (entry exists) →
  `""`. `subagentEntry`/`subagentEntryIndex` (:363–380).
- `subagentEnabled` (:383–385): **`entry.Enabled == nil || *entry.Enabled`**
  — absent means ENABLED. Shared customs need the OPPOSITE default.
- `subagentEnabledPointer` (:389–394): on is written as absent (nil), off as
  `false`. `setSubagentRowEnabled` (:442–455): built-in → override entry;
  custom → its own entry. `storeSubagentOverride` (:423–440).
- `customSubagentRow` (:215–240), `builtinSubagentRow` (:197–212),
  `systemSubagentRow` (:245–256, 313 adds the global plan-reviewer fields).
- `handleAgentSubagentUpdate` (:813–854): custom replaced whole; builtin takes
  `subagentBuiltinWrite` (model/effort/prompt/reset/enabled — **ruling A keeps
  this**); system refused. `handleAgentSubagentDelete` (:867–885): only custom
  deletable. `handleAgentSubagentEnabled` (:897–925): builtin/custom switch +
  the agent-switch linkage; system refused.
- `writeAgentSubagents` (:568–...) is the one read-modify-write wrapper for
  agent subagent writes; `subagentEntryFrom` (:763–...) builds a stored entry.
- `handlePrimaryAgentCreate` (`agents_api.go:545–585`): 313 adds the
  `IsBuiltInSubagentDefinition` guard. The shared-registry create handler
  mirrors this shape (id/name validation + duplicate refusal + seed +
  `validateLoadedRootForAgents`).

### TUI (`pkg/tui`)

- `configSectionSchema` (`config_panel_schema.go:77–95`): Name, Title,
  Summary, Tab, YAMLOnly, Fields. **No projection mechanism today.**
- `configSectionSchemas` (:108–...): the `agents` section (:185–220) holds
  `definitions` (rows), the defaults, the goal-check pair, and (after 313) the
  `plan_reviewer` pair. Tabs: `configTabAgents = "Agents & tools"` (:101).
- `panelSections` (`config_panel.go:232–244`), `sectionItems` (:262–275),
  `sectionDescription` (:277–289, reads `p.raw[schema.Name]`),
  `editSection` (:291–337), `sectionDoc` (:450–462, reads `p.raw[name]`),
  `save` (:418–432, `p.session.UpdateConfigSection(ctx, name, raw)` then
  updates `p.raw[name]`).
- Coverage tests (`config_panel_test.go`):
  `TestConfigPanelSchemaCoversEveryRootSection` (:568–605) requires every
  schema.Name to be a `config.Root` yaml section AND in
  `process.RootConfigSections()` (unless YAMLOnly);
  `TestConfigPanelSchemaFieldsMatchTheirSection` (:611–672) skips a schema
  with no Root type (`typ, ok := types[schema.Name]; if !ok { continue }`).
  `rootSectionTypes` (:528–540) reflects `config.Root` yaml tags.
  `process.RootConfigSections` (`pkg/process/config_db.go:465–470`) copies
  `rootConfigSections`; `MarshalRootSection` (:475–...) reflects the Root
  field by section name.
- Generic row editor: `editRows` (:665), `rowItems` (:692, 313 extends it with
  doc/path and sets `SelectItem.Status`), `editRow` (:748), `addRow`
  (:798–850), `configRowSummary` (313 Step 5). `SelectItem.Status`
  (`channels.go`, 313 Step 4b) is the right-aligned status column.
- The `agents` section doc is the whole `AgentsSection` JSON; `definitions`,
  `goal_evaluator`, `plan_reviewer`, and (after this plan) `shared_subagents`
  are keys in it.

### Web (`frontend/src`)

- `SettingsSubagentsTab.vue` (30 lines): `<SettingsTabForm :tab :form />` +
  `<GoalCheckModelGroup :form />` (+ 313's `<PlanReviewerModelGroup :form />`).
  Hand-written groups sit beside the schema form; `settingsSchema.ts`
  subagents tab (:171–177) carries only the defaults switches — do NOT add
  registry paths there.
- `GoalCheckModelGroup.vue` is the scalar-group exemplar (useModelChoices +
  useSettingsForm + saveGroup). A registry CRUD group is richer — model it on
  `SubagentsView.vue`'s list + drawer instead.
- `SubagentsView.vue`: rows via `forebrainApi.fetchSubagentConfigs`; actions
  (`:110–165`): switch for non-system; custom → Edit + Delete; builtin → Edit
  (ruling A keeps); system → View. `toggleRow` (:372), `openEdit` (:393),
  `openView` (:397), `askDelete` (:409). `systemModel` (:339–343, 313 extends).
- `SubagentConfigDrawer.vue`: props `open/agentId/name/mode`
  (`'create'|'edit'|'view'`, :300–307); `readonly = mode==='view'` (:331);
  `customBody()` (:603–614) / `builtinBody()` (:617–623); `save()` (:625–...)
  calls `createSubagentConfig`/`updateSubagentConfig`. `NAME_PATTERN`
  `/^[a-z][a-z0-9-]{1,62}$/` (:317, stricter than the loader — no underscore).
- `api.ts`: `SubagentKind = 'builtin'|'custom'|'system'` (:2377);
  `SubagentConfigRow` (:2385–2408); `SubagentConfigBody` (:2433);
  `fetchSubagentConfigs` (:2823–...), `createSubagentConfig` (:2849),
  `updateSubagentConfig` (:2858), `deleteSubagentConfig` (:2862),
  `setSubagentEnabled` (:2871).

### Conventions

- Go: table-driven tests beside the code. Exemplars:
  `TestValidateGoalEvaluatorModel` (`pkg/config/agents_test.go:614`) for
  validator tests; `TestSubagentBuiltinRewriteAndRestoreDefault`
  (`pkg/gateway/subagents_api_test.go:338`) + `newSubagentFixture` (:40) for
  gateway row/write tests; `TestPrimaryAgentCreateRejectsDuplicateID`
  (`pkg/gateway/agents_api_test.go:341`) for create-refusal tests;
  `TestConfigPanelSavesATriStateBoolean` (`pkg/tui/config_panel_test.go:187`)
  for panel tests. Comments explain WHY and cite "plan 314".
- Web: Vue SFC `<script setup lang="ts">`, i18n keys in
  `frontend/src/locales/index.ts` (zh block first, en second), component tests
  like `SubagentsView.test.ts`. TUI panel copy is English; web settings copy
  is Chinese via i18n.
- Frozen references stay untouched: `plans/config-tui-prototype.html`
  (sha256 `95e75dedd9479317e23464d1978efc6a9301e3dfead0efade0bcbee9c8e9e688`),
  `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html`
  (sha256 `aef1aa8cac4e86fd258706d56acb4b8b8c3507bf575356ddae4106bd51ac63b0`).

## Commands you will need

Environment setup (house rules — once per shell):

```bash
export CGO_ENABLED=1
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
```

**Every `go test` runs against a fresh scratch home** —
`FOREBRAIN_HOME="$(mktemp -d)"` prefixed. Never inherit the ambient value
(it points at the real install; FOREBRAIN.md house rules). Without
`PKG_CONFIG_PATH` the `pkg/tui`/`pkg/pdf` cgo build fails with
`Package 'pdfium' not found` (verified).

| Purpose | Command | Expected |
|---|---|---|
| Build | `go build -tags fts5 ./...` | exit 0 |
| Vet | `go vet -tags fts5 ./pkg/config ./pkg/run ./pkg/gateway ./pkg/tui` | exit 0 |
| Format | `gofmt -l pkg/config pkg/run pkg/gateway pkg/tui` | empty |
| Go tests (packages) | `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/config ./pkg/run ./pkg/gateway ./pkg/tui -count=1` | all pass |
| Architecture guard | `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/architecture -count=1` | all pass (fan-out ratchet; `pkg/run` already imports `config`/`agent`, no new import) |
| Web typecheck | `cd frontend && pnpm typecheck` | exit 0 |
| Web tests | `cd frontend && pnpm vitest run src/components/settings src/views/SubagentsView.test.ts src/components/subagents` | all pass |
| Web build | `cd frontend && pnpm run build` | exit 0 |
| Prototype regression | `cd plans && node config-tui-prototype.test.cjs` | `"passed": 20`, zero browser errors (needs `python3 -m http.server 8765 --bind 127.0.0.1 --directory plans`; Playwright from `frontend/node_modules`) |

Focused Go gate (fenced — a markdown table eats `|`; use BARE pipes):

```bash
FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/config ./pkg/run ./pkg/gateway ./pkg/tui \
  -run 'Shared|Subagent|ConfigPanel|PrimaryAgentCreate' -count=1 -v 2>&1 | tee /tmp/plan314-focused.log
grep -c '^--- PASS' /tmp/plan314-focused.log        # positive
grep -c 'no tests to run' /tmp/plan314-focused.log  # must be 0
```

`-run` patterns use bare `|`, never `\|` (an escaped pipe matches zero tests
and exits 0 — a false green). Every focused gate runs `-v` and checks the
named tests appear as `--- PASS`.

## Scope

**In scope** (the only files you should modify):

- `pkg/config/agents.go`, `pkg/config/load.go`, `pkg/config/agents_test.go`,
  `pkg/config/load_test.go` (or `config_test.go` if that is where load tests
  live — grep first)
- `pkg/run/subagent_catalog.go`, `pkg/run/subagent_catalog_test.go` (create if
  absent; grep `func TestSubagentCatalog` first), `pkg/run/subagent_test.go`
  (only if a dispatch case belongs there)
- `pkg/gateway/subagents_api.go`, `pkg/gateway/subagents_api_test.go`,
  `pkg/gateway/api_extra.go` (route registration only),
  `pkg/gateway/agents_api.go` (only if the shared create handler lives beside
  the primary-agent handlers — prefer a new `shared_subagents_api.go`),
  `pkg/gateway/shared_subagents_api.go` (create),
  `pkg/gateway/shared_subagents_api_test.go` (create)
- `pkg/tui/config_panel_schema.go`, `pkg/tui/config_panel.go`,
  `pkg/tui/config_panel_test.go`
- `frontend/src/components/settings/GlobalSubagentsGroup.vue` (create),
  `frontend/src/components/settings/GlobalSubagentsGroup.test.ts` (create),
  `frontend/src/components/settings/SettingsSubagentsTab.vue`,
  `frontend/src/components/subagents/SubagentConfigDrawer.vue`,
  `frontend/src/views/SubagentsView.vue`, `frontend/src/views/SubagentsView.test.ts`,
  `frontend/src/lib/api.ts`, `frontend/src/locales/index.ts`
- `plans/README.md` (status row only)

**Out of scope** (do NOT touch):

- `pkg/tui/setup.go` / `setup_test.go` — 311/312 own `/connect`.
- The built-in per-agent override path: `subagentBuiltinWrite`,
  `handleAgentSubagentUpdate`'s builtin branch, the drawer's `builtinBody()`,
  `builtinSubagentRow`'s model/effort/prompt fields — **ruling A keeps them**.
- Per-type provider lists (`llm_providers`): **deleted by plan 315**; this
  plan reads `agents.subagent_models.<type>` row references, never provider
  lists.
- `pkg/run/runner.go` dispatch order for built-ins (`SubagentOwnModel`,
  per-type clients) — unchanged; only `SubagentCatalogFor`'s custom merge
  changes.
- `frontend/src/lib/settingsSchema.ts` — registry CRUD is a hand-written
  group, not schema paths.
- `plans/*.html`, `plans/*.test.cjs`, `docs/plan/**` — frozen references.
- The whole-`/config` 4-tab visual redesign — deferred beyond this plan.

## Git workflow

- Branch: `advisor/314-global-shared-custom-subagents` from `dev` (after 313
  lands). Commit per step; conventional-commit style as in `git log`.
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: config — the shared registry field and its validator

In `pkg/config/agents.go` add to `AgentsSection`, beside `PlanReviewer`:
```go
// SharedSubagents are the install-wide custom subagents every primary agent
// may consume (plan 314). Each is a full custom definition; enablement is
// per-agent (a reference entry in that agent's Subagents), so a shared
// definition carries no Enabled of its own — it is disabled for every agent
// until explicitly enabled (owner 2026-11-16).
SharedSubagents []SubagentConfig `yaml:"shared_subagents,omitempty" json:"shared_subagents,omitempty"`
```

Add `ValidateSharedSubagents(entries []SubagentConfig, publicBuiltins, systemBuiltins []string, refResolves func(string) bool) error`
modelled on `ValidateAgentSubagents` (:252–307): for each entry — name matches
`subagentConfigNamePattern`; refuse a name in `publicBuiltins` or
`systemBuiltins` (`"%q is a built-in subagent type and cannot be a shared custom subagent"`);
refuse duplicates; require `description` and `system_prompt`; validate the
`tools` tier and `allowed_tools` exactly as the custom branch does; **refuse
`Enabled != nil`** (`"a shared subagent's enablement is per-agent; remove enabled"`);
call `validateSubagentModel("agents.shared_subagents[i]", entry, refResolves)`.

In `load.go`, call `ValidateSharedSubagents(r.Agents.SharedSubagents, builtInPublicSubagentNames, builtInSystemSubagentNames, func(ref string) bool { _, ok := ResolveModelRef(r, "main", ref); return ok })`
beside the goal-evaluator/plan-reviewer global calls (:209–214 area), so a
fresh install with no definitions is still checked. Use the `"main"` resolver
— a shared subagent's model is global (a row id), the same reason the
goal-evaluator row uses it.

Tests in `pkg/config/agents_test.go`, modelled on
`TestValidateGoalEvaluatorModel` (:614): a valid shared entry accepted;
missing description/prompt refused; a built-in/system name refused; a
duplicate refused; `enabled` set refused; effort-without-model refused naming
`agents.shared_subagents`; a row-id model accepted; a bare model name refused.
Add a load-level test (beside the goal-evaluator `FinishLoad` case,
`agents_test.go:643–658`) proving a bad shared entry fails `FinishLoad` on an
install with no definitions.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/config -run 'SharedSubagents|ValidateGoalEvaluator' -count=1 -v` → new tests `--- PASS`, no `[no tests to run]`; `go build -tags fts5 ./...` → exit 0.

### Step 2: config — reference entries in ValidateAgentSubagents

A per-agent enable marker is a `def.Subagents` entry `{name: <shared name>,
enabled: true}` with nothing else. Teach `ValidateAgentSubagents`
(`agents.go:252–307`) to accept it. It needs the shared names: add a
parameter `sharedNames []string` (or pass the whole `[]SubagentConfig` and
derive names) — update ALL its callers (verified call graph):
`load.go:942` (`validatePrimaryAgents`), `pkg/gateway/subagents_api.go:601`
(inside `writeAgentSubagents` — it passes `agent.PublicSubagentNames` /
`agent.SystemSubagentNames`; add the shared names from `cfg.Agents.SharedSubagents`),
and the tests `pkg/config/agents_test.go:584–604`. Grep
`ValidateAgentSubagents(` again before editing in case of drift.

In the custom branch (the `else` at :279), before requiring
description/prompt: if the entry's name is in `sharedNames`, it is a
**reference entry** — allow only `name` and `enabled`; refuse any other set
field (`description`, `system_prompt`, `model`, `reasoning_effort`, `tools`,
`allowed_tools`, `continuable`) with
`"%q references a shared subagent; only enabled may be set here"`. If the name
is NOT shared, the existing full-custom rules apply unchanged. Additionally
refuse a full per-agent custom whose name collides with a shared name (the
reference branch already covers an entry that sets fields; a name-only entry
for a shared name IS the reference, so the collision rule is: a shared name
may only appear as a reference, never as a full definition — the reference
branch's field refusal enforces this).

Tests: a reference entry `{name: "my-reviewer", enabled: true}` for a shared
name accepted; the same with `description` set refused; a full custom for a
non-shared name still requires description/prompt (regression); a built-in
override entry unchanged (ruling A).

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/config -run 'AgentSubagents|Shared' -count=1 -v` → new + existing tests `--- PASS`.

### Step 3: dispatch — merge enabled shared customs into the catalog

In `pkg/run/subagent_catalog.go` `SubagentCatalogFor` (:64–86):

1. Build a set of shared names from `cfg.Agents.SharedSubagents`.
2. In the per-agent loop, **skip reference entries** (a non-public entry whose
   name is a shared name) when appending to `customs`/`entries` — they are
   enable markers, not definitions. Record which shared names are enabled:
   a shared name is enabled iff its reference entry exists with
   `Enabled != nil && *Enabled` (absence or nil/false = disabled — the
   owner's default-disabled rule, the OPPOSITE of `subagentConfigEnabled`).
3. After the loop, for each shared definition in config order whose name is
   enabled for this agent, append it to `customs` and `entries` with
   `Enabled = config.BoolPtr(true)` (so `subagentConfigEnabled` passes). The
   definition's own fields (description/prompt/model/tools/continuable) are
   the shared ones.

`catalog.Model` (:175–195) then resolves a shared custom's model: store shared
models as row ids (Step 5 normalizes), so `ResolveModelRef(c.cfg, c.agentName,
id)` resolves globally regardless of agent. `Prompt`, `ToolPolicy`,
`EnumDescription`, `Public`, `ResolvePublic`, `configures` all read
`entries`/`customs` and need no change — the merged shared customs flow
through. `refuseGoneCustomSubagent` (`subagent.go:3120`) answers correctly
because `configures` sees the merged entry.

Tests in `pkg/run/subagent_catalog_test.go` (it EXISTS; model on
`TestSubagentCatalogListsEnabledBuiltinsThenCustom` :49 and
`TestSubagentCatalogModelResolvesTheConfiguredRow` :190 for the fixture
pattern): a shared custom with no
reference is NOT in `Public()`/`configures` (default disabled); with
`{name, enabled:true}` it IS, and `ResolvePublic` returns its definition;
with `{name, enabled:false}` it is NOT; a per-agent full custom of the same
name is impossible (loader refuses) — do not test an invalid config; a shared
custom's `Model` resolves its row id; office/batch `ForProjectType` still
includes enabled shared customs.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/run -run 'SubagentCatalog|Subagent' -count=1 -v` → new tests `--- PASS`, existing dispatch tests green, no `[no tests to run]`.

### Step 4: gateway — the shared kind, agent-page rows and enable-only writes

In `pkg/gateway/subagents_api.go`:

- Add `subagentKindShared = "shared"` beside the kind consts (:43–49).
- `subagentKindFor` (:348–360): before the custom case, if the name is a
  shared registry name (and not a per-agent full custom — the loader
  guarantees a shared name only appears as a reference), return
  `subagentKindShared`. It needs `cfg` to read the registry: change the
  signature to `subagentKindFor(cfg *config.Root, def config.AgentDefinition, name string)`
  and update ALL FIVE callers (verified): `subagentRowFor` (:183),
  `handleAgentSubagentCreate` (:748), `handleAgentSubagentUpdate` (:816),
  `handleAgentSubagentDelete` (:873), `handleAgentSubagentEnabled` (:905).
  Note :748 already refuses any non-custom kind
  (`kind != "" && kind != subagentKindCustom` → conflict), so once a shared
  name returns `subagentKindShared`, creating a per-agent custom of that name
  is refused automatically — refine its message to name the shared scope if
  you touch it, but the refusal itself is free.
- `subagentNames` (:151–163): the existing loop already lists every
  non-public/non-system `def.Subagents` name — which now INCLUDES reference
  entries (a shared name with a reference). After that loop, append the shared
  registry names **not already listed** (dedupe against `out`, so a referenced
  shared name is not added twice; an unreferenced shared name is added once,
  default-disabled). Order: agent's own entries (customs + references) first,
  then unreferenced shared names, then public, then system — pin this order in
  a test. It needs `cfg` too: change to
  `subagentNames(cfg *config.Root, def config.AgentDefinition)` and update
  its single caller `subagentRows` (:165–176).
- Add `sharedSubagentRow(cfg, agentID, name)`: fields from the shared
  definition (description/tools/allowed_tools/continuable/model/effort),
  `Kind: subagentKindShared`, `Enabled` from the reference entry
  (`false` when absent — default disabled), `ModelSource: "global"`. The model
  label resolves the shared row id via `config.ResolveModelRef(cfg, "main", ref)`
  + `llm.FormatProviderModelDisplay` (the helper `subagentRowModelFields`
  uses, :307). Read-only: the row carries the definition's values, never an
  agent override.
- `subagentRowFor` (:180–192): add `case subagentKindShared: return sharedSubagentRow(...), true`.
- `handleAgentSubagentUpdate` (:813–854): add `case subagentKindShared:` —
  refuse any model/effort/prompt edit
  (`"%q is a shared subagent; edit it in Settings → Subagents, only enable/disable here"`);
  if the body carries only `enabled`, delegate to the same logic as the
  enabled endpoint (or refuse and direct the client to the enabled endpoint —
  pick one and pin it; the web uses the enabled endpoint, so refusing a
  non-enabled PUT is acceptable and simpler).
- `handleAgentSubagentDelete` (:867–885): `case subagentKindShared:` refuse
  (`"%q is a shared subagent; delete it in Settings → Subagents"`).
- `handleAgentSubagentEnabled` (:897–925): `case subagentKindShared:` write the
  reference entry — enable stores `{name, Enabled: true}` in `def.Subagents`
  (reuse `subagentEntryIndex`; append if absent); disable REMOVES the reference
  entry (so absence = disabled). Keep the agent-switch linkage the endpoint
  already applies. Do NOT use `subagentEnabledPointer`'s on=nil convention
  here — a shared reference must carry `enabled:true` explicitly, because
  absence means disabled.
- `subagentAllRowsDisabled` (:459–...): shared rows count toward the linkage
  (a shared custom enabled for this agent is a row that is on). Include
  enabled shared references.

Tests in `subagents_api_test.go`, modelled on
`TestSubagentBuiltinRewriteAndRestoreDefault` (:338) / `newSubagentFixture`
(:40): the agent list includes a shared row with `kind:"shared"`,
`enabled:false` by default; enabling via the enabled endpoint writes a
reference and the row reads `enabled:true`; disabling removes it; a PUT with
model/prompt on a shared row is refused; DELETE refused; a built-in row's
override path is UNCHANGED (ruling A regression).

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/gateway -run 'Subagent' -count=1 -v` → new + existing `--- PASS`.

### Step 5: gateway — the shared registry CRUD endpoints

Create `pkg/gateway/shared_subagents_api.go` with handlers mirroring the
primary-agent create shape (`agents_api.go:545–585`) and the custom subagent
write shape:

- `handleSharedSubagentsList` (GET `/agents/subagents/shared`): list the
  registry in config order, each row a `subagentRow`-shaped object with
  `kind:"shared"`, the definition's fields, model resolved to a label, and
  NO per-agent enabled (this is the global management list, not an agent
  page). Reuse `sharedSubagentRow`'s field projection where it fits, or a
  dedicated global row struct.
- `handleSharedSubagentCreate` (POST): validate the name
  (`subagentConfigNamePattern` via the loader; refuse built-in/system names
  and duplicates with `config.IsBuiltInSubagentDefinition` + the registry
  scan), require description/system_prompt, normalize the model to a row id
  (reuse the `normalizeSystemModelPath` pattern 313 built, or
  `config.ResolveModelRef(cfg, "main", ref)` → store `entry.ID`), seed the
  definition, run `validateLoadedRootForAgents` (`agents_api.go:652`), write
  via the agents-section read-modify-write (`withConfigSection`,
  `api_extra.go:5988` — the wrapper `handlePrimaryAgentCreate` uses).
- `handleSharedSubagentUpdate` (PUT `/agents/subagents/shared/:name`): replace
  the definition whole (name immutable), same validation + model normalize.
- `handleSharedSubagentDelete` (DELETE): remove from the registry; **also
  remove every agent's reference entry for that name** (otherwise a dangling
  reference fails the loader's reference validation — a reference must name an
  existing shared subagent). Iterate `cfg.Agents.Definitions`, drop matching
  reference entries. This is the one cross-agent write; do it inside the same
  agents-section transaction.

Register the routes in `api_extra.go` beside the agents group (:104–130):
```go
agents.Get("/subagents/shared", s.handleSharedSubagentsList)
agents.Post("/subagents/shared", s.handleSharedSubagentCreate)
agents.Put("/subagents/shared/:name", s.handleSharedSubagentUpdate)
agents.Delete("/subagents/shared/:name", s.handleSharedSubagentDelete)
```
These do not collide with `/primary/:id/subagents` (different prefix).

Tests in `shared_subagents_api_test.go`, modelled on
`TestPrimaryAgentCreatePersistsDefinition` (:320) /
`TestPrimaryAgentCreateRejectsDuplicateID` (:341): create persists a
definition with a normalized row-id model; a built-in name refused; a
duplicate refused; missing description/prompt refused; update replaces;
delete removes the definition AND every agent's reference; list returns
config order.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/gateway -run 'SharedSubagent' -count=1 -v` → new tests `--- PASS`.

### Step 6: TUI — the projection section mechanism

In `pkg/tui/config_panel_schema.go` add to `configSectionSchema` (after
`YAMLOnly`, :90):
```go
// StoreSection, when non-empty, marks this section a PROJECTION: it presents
// a curated view of another stored section's document and saves back to that
// section. The panel reads/writes p.raw[StoreSection]; Name is only what the
// user sees and types. "Subagent models" projects the agents store
// (plan 314). A projection is not itself a config.Root section.
StoreSection string
```

In `pkg/tui/config_panel.go`:
- `sectionDoc` (:450–462): resolve the store key —
  `key := name; if s := configSectionSchemaFor(name); s.StoreSection != "" { key = s.StoreSection }`;
  read `p.raw[key]`. (Use `panelSectionSchemaFor` if the hidden-section
  semantics matter; match how `editSection` looks the schema up.)
- `save` (:418–432): write under the same resolved store key
  (`p.session.UpdateConfigSection(ctx, key, raw)`; update `p.raw[key]`). The
  doc is the WHOLE agents section, so keys the projection does not display
  are marshalled back untouched — no data loss.
- `sectionDescription` (:277–289): read `p.raw[storeKey]` for the set/default
  suffix.
- `editSection` (:291–337) needs no change: it calls `sectionDoc(name)` and
  `save(ctx, name, doc)`, which now resolve the store key.

Update `TestConfigPanelSchemaCoversEveryRootSection`
(`config_panel_test.go:568–605`): a schema with `StoreSection != ""` is a
projection — require its `StoreSection` to be a real Root section in
`process.RootConfigSections()`, and exempt the projection's own `Name` from
the "must be a Root section" rule. Keep every other assertion. Add a case:
a projection whose StoreSection is not stored fails the test.
`TestConfigPanelSchemaFieldsMatchTheirSection` (:611) already skips a schema
with no Root type, so the projection is skipped there — add a dedicated test
that the projection's field path heads are keys of the STORE section's type
(agents), so a typo'd path is caught.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/tui -run 'ConfigPanelSchema' -count=1 -v` → coverage tests `--- PASS` (the mechanism exists; the section is added in Step 7).

### Step 7: TUI — the "Subagent models" section

In `config_panel_schema.go` add a section to `configSectionSchemas`, in the
`configTabAgents` tab, after the `agents` section:
```go
{
    Name: "subagent_models", Title: "Subagent models", Tab: configTabAgents,
    Summary: "Install-wide defaults for built-in types, plan-reviewer and shared custom subagents",
    StoreSection: "agents",
    Fields: []configField{
        // The four public per-type defaults — projections of subagent_models.<type> (plan 315).
        {Path: "subagent_models.general-purpose", Label: "general-purpose", Kind: configFieldRows,
            Hint: "Install-wide per-type default model row from the global table. An agent's own override still wins."},
        {Path: "subagent_models.explore", Label: "explore", Kind: configFieldRows, Hint: "..."},
        {Path: "subagent_models.plan", Label: "plan", Kind: configFieldRows, Hint: "..."},
        {Path: "subagent_models.verification", Label: "verification", Kind: configFieldRows, Hint: "..."},
        // The plan-reviewer global row (relocated from the agents section).
        {Path: "plan_reviewer", Label: "plan-reviewer", Kind: configFieldRows,
            Hint: "Global row for a system type, the goal-evaluator pattern. Agent pages stay read-only."},
        // The shared custom registry (Step 1).
        {Path: "shared_subagents", Label: "Shared custom subagents", Kind: configFieldRows, List: true,
            Hint: "Install-wide custom subagents every agent may enable. Disabled per agent until enabled."},
    },
},
```
(Relocate the `plan_reviewer.model`/`plan_reviewer.reasoning_effort` pair 313
added to the `agents` section: REMOVE it from the agents section's Fields —
the prototype's agents section has only the goal-check pair. The projection's
`plan_reviewer` rows field opens the `{model, reasoning_effort}` map; the
generic editor labels the keys `Model`/`Reasoning effort`. The prototype's
bespoke `Thinking level` enum picker is part of the deferred visual redesign;
free text is the interim widget, exactly as the goal-check pair uses today.)

Row summaries/statuses (extend 313's `configRowSummary` + `rowItems` Status
assignment): for this section's per-type rows
(`len(path)==2 && path[0]=="definitions" && IsBuiltInSubagentDefinition(path[1])`),
Description = the prototype's `typeSummary` (`provider/model + " / " +
effortLabel`, `Follow conversation model` when no row; effortLabel empty →
`Row default`, `none` → `Off`), Status = `Global default` when the row's
`model` is non-empty, else empty. For the `plan_reviewer` row
(`path==["plan_reviewer"]`), Description = the model label + effort, Status =
`Global row` when `model` non-empty. For `shared_subagents` rows, Description
= the entry's description (or `› N items`), Status empty.

`shared_subagents` creation: `addRow` for `path==["shared_subagents"]` (a list)
seeds a full custom definition skeleton
`{name: "", description: "", system_prompt: "", tools: "read_only"}` and opens
it; the loader's `ValidateSharedSubagents` refusal surfaces on save (the
existing validator path). The name is edited as a field; the generic editor's
key prompt does not apply to list rows — the entry's `name` field is the
identity. (If the generic list-add flow cannot seed a named object cleanly,
STOP — do not invent a bespoke widget; report.)

Tests in `config_panel_test.go`: the section list includes
`subagent_models`; opening it reads the agents doc; a per-type row shows the
global default summary + `Global default` status; editing a per-type row's
model/effort and saving writes `agents.subagent_models.<type>` (the store key),
leaving other agents keys untouched; the `plan_reviewer` row edits
`agents.plan_reviewer`; adding a shared subagent seeds the skeleton and save
runs the loader validator; the agents section no longer lists the
`plan_reviewer` pair.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/tui -run 'ConfigPanel' -count=1 -v` → new + existing `--- PASS`; `gofmt -l pkg/tui` → empty.

### Step 8: TUI — agent-page shared rows are read-only + enable/disable

On a primary agent's subagent list (`definitions.<id>.subagents`,
`len(path)==3 && path[0]=="definitions" && path[2]=="subagents"`), the user
must see every shared custom (even disabled ones) and toggle it, without
editing the shared definition. This is the most involved panel change; keep
it inside the generic editor's row machinery:

- `rowItems` for that path merges the shared registry names into the row list
  (read `doc`'s `shared_subagents`), after the agent's own entries. A shared
  row's value is its reference entry `{name, enabled}` when present, else a
  virtual `{name, enabled:false}` placeholder (NOT written to the doc until
  the user toggles). Description = the shared definition's description +
  ` (shared)`; Status = `Shared`.
- `editRow` for a shared row opens a RESTRICTED editor: the shared
  definition's fields shown read-only (they are edited in the projection
  section), plus one `enabled` bool3 field. Toggling enabled writes/removes
  the reference entry in `def.Subagents` (enable → `{name, enabled:true}`;
  disable → remove). No other field of a shared row is editable here; the
  system guard (313) and the reference-entry rule (Step 2) back this on save.
- A per-agent FULL custom row (non-shared name) keeps today's full editing
  (ruling: per-agent customs are the agent's own). A built-in override row
  keeps today's editing (ruling A).

If the generic `editRow`/`editRows` cannot express a restricted read-only
row + single toggle without a bespoke widget, STOP and report — do not
redesign the editor engine. The fallback the owner may accept is documenting
that TUI enable/disable happens by adding/removing the reference entry via
the existing add/drop rows, with the shared definition read-only in the
projection section; but attempt the merged read-only list first.

Tests: a shared custom with no reference appears in the agent's subagent list
as disabled; enabling writes `{name, enabled:true}`; disabling removes it;
the shared definition's fields are not editable from the agent page; a
per-agent custom and a built-in override remain fully editable.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/tui -run 'ConfigPanel' -count=1 -v` → new tests `--- PASS`.

### Step 9: web — the settings tab creates/manages shared subagents

Create `frontend/src/components/settings/GlobalSubagentsGroup.vue`: a
hand-written group mounted in `SettingsSubagentsTab.vue` beside the model
groups (after `<PlanReviewerModelGroup>`), modelled on `SubagentsView.vue`'s
list + drawer (NOT the scalar `GoalCheckModelGroup` — a registry needs CRUD):
a table of shared subagents (name, description, model, tools) with Create /
Edit / Delete actions, reusing `SubagentConfigDrawer.vue` extended with a
`scope: 'agent' | 'global'` prop (default `'agent'`). When `scope==='global'`:
- the drawer omits `agentId`, calls the new global api functions (Step 10),
  uses `customBody()` (a shared subagent IS a custom definition), and the
  name field is editable on create only.
- the group's list calls `forebrainApi.fetchSharedSubagents()`.
- **Per-type global defaults (owner 2026-11-16: web 补齐 parity)**: the same
  group also lists the four public built-in types' global defaults
  (`agents.subagent_models.<type>`, plan 315) as read/edit rows with a global
  model-row picker + thinking-level picker (same shape as the TUI projection
  section's per-type rows). Editing writes `subagent_models.<type>` via the
  agents-section write path; no provider editing anywhere.
Add i18n keys (zh first, en second) beside the subagents keys:
`subagents.shared.title` = `全局共享子代理` / `Shared subagents`, plus the
group's create/edit/delete/empty wording mirroring `SubagentsView`'s keys.
Do NOT add registry paths to `settingsSchema.ts`.

`SubagentConfigDrawer.vue`: add the `scope` prop; when global, `save()` calls
`createSharedSubagent`/`updateSharedSubagent`, the title/footer wording names
the shared scope, and the per-agent enable switch is absent (enablement is
per-agent, on the agent page). The `builtinBody` path is untouched (ruling A).

Tests `GlobalSubagentsGroup.test.ts` (model on `SubagentsView.test.ts`):
renders the list; create opens the drawer in global scope and posts the
definition; edit puts; delete asks then deletes; a refusal renders inline.
Drawer test: global scope calls the global api, agent scope unchanged.

**Verify**: `cd frontend && pnpm typecheck && pnpm vitest run src/components/settings src/components/subagents` → exit 0, all pass.

### Step 10: web — api types and the agent page's read-only shared rows

In `frontend/src/lib/api.ts`:
- `SubagentKind` (:2377): add `'shared'`.
- `SubagentConfigRow` (:2385): no shape change needed (kind carries it);
  ensure `enabled` reflects the server's default-disabled for shared rows.
- Add `fetchSharedSubagents()`, `createSharedSubagent(body)`,
  `updateSharedSubagent(name, body)`, `deleteSharedSubagent(name)` hitting
  `/agents/subagents/shared` (model on `createSubagentConfig` :2849 etc.).
- The agent-page `setSubagentEnabled` (:2871) already targets the enabled
  endpoint — shared rows reuse it.

In `SubagentsView.vue` actions (:110–165): add a `row.kind === 'shared'`
branch — switch (enable/disable) + **View only** (no Edit, no Delete). The
switch calls `toggleRow` (:372) → `setSubagentEnabled`. `openView` (:397)
opens the drawer in `'view'` mode (read-only). The builtin branch keeps Edit
(ruling A); the custom branch keeps Edit + Delete.

`SubagentConfigDrawer.vue`: a shared row opens in `'view'` mode from the agent
page (read-only definition); the global scope (Step 9) is the only place it
is edited.

Tests `SubagentsView.test.ts`: a shared row renders a switch + View, no
Edit/Delete; toggling calls the enabled endpoint; a builtin row still has
Edit (ruling A regression); a custom row still has Edit + Delete.

**Verify**: `cd frontend && pnpm vitest run src/views/SubagentsView.test.ts && pnpm run build` → exit 0.

### Step 11: full gates and index

Run every command in the table once, in order, including the architecture
guard (the catalog merge adds no new package import — `pkg/run` already
imports `config`/`agent`; if the fan-out ratchet trips, STOP and report).
Run the prototype regression (start the static server first; read-only).
Then update the 314 row in `plans/README.md` to DONE with the evidence line
(test counts, the two frozen hashes re-verified with `shasum -a 256`).

**Verify**: all commands exit 0; `git status --short` shows only in-scope files.

## Test plan

- New Go tests: shared validator + load refusal (Step 1), reference entries
  (Step 2), catalog merge + default-disabled (Step 3), gateway shared kind +
  enable-only + delete-refused + builtin-unchanged (Step 4), registry CRUD +
  cross-agent reference cleanup (Step 5), projection mechanism + coverage
  (Step 6), projection section + summaries + save-to-store-key (Step 7),
  agent-page read-only + toggle (Step 8). Exemplars named per step.
- New web tests: `GlobalSubagentsGroup.test.ts` (Step 9), drawer global scope
  (Step 9), extended `SubagentsView.test.ts` (Step 10).
- Regression: full `pkg/config`, `pkg/run`, `pkg/gateway`, `pkg/tui` packages
  + `pkg/architecture`; frontend settings + subagents suites; the prototype
  suite (26 results: 25 checks + the nested-transition result) stays green untouched.
- Failure paths: default-disabled (no reference) must NOT dispatch; a dangling
  reference after a shared delete must not fail the loader (Step 5 cleans up);
  a shared-name collision refused; a reference entry with extra fields refused.
- No new e2e spec; existing subagent e2e specs stay green if run.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `go build -tags fts5 ./...` exit 0; `go vet -tags fts5 ./pkg/config ./pkg/run ./pkg/gateway ./pkg/tui` exit 0; `gofmt -l pkg/config pkg/run pkg/gateway pkg/tui` empty
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/config ./pkg/run ./pkg/gateway ./pkg/tui ./pkg/architecture -count=1` exit 0, including the new tests
- [ ] `cd frontend && pnpm typecheck && pnpm run build` exit 0; `pnpm vitest run src/components/settings src/components/subagents src/views/SubagentsView.test.ts` exit 0
- [ ] `grep -n "SharedSubagents" pkg/config/agents.go` shows the field; `grep -n "shared_subagents" pkg/config/load.go pkg/tui/config_panel_schema.go` shows the load call + the projection field
- [ ] `grep -n "subagentKindShared" pkg/gateway/subagents_api.go` shows the kind; `grep -n "subagents/shared" pkg/gateway/api_extra.go` shows the four routes
- [ ] `grep -n "StoreSection" pkg/tui/config_panel_schema.go pkg/tui/config_panel.go` shows the projection mechanism
- [ ] `grep -n "'shared'" frontend/src/lib/api.ts` shows the kind; `grep -n "fetchSharedSubagents" frontend/src/lib/api.ts` shows the api
- [ ] A shared custom with no per-agent reference is absent from `SubagentCatalogFor(...).Public()` (test proves default-disabled)
- [ ] `shasum -a 256 plans/config-tui-prototype.html` = `95e75dedd9479317e23464d1978efc6a9301e3dfead0efade0bcbee9c8e9e688`; `shasum -a 256 docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html` = `aef1aa8cac4e86fd258706d56acb4b8b8c3507bf575356ddae4106bd51ac63b0`
- [ ] `cd plans && node config-tui-prototype.test.cjs` reports 26 passed, zero browser errors
- [ ] No files outside the in-scope list modified (`git status --short`)
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- Plan 313 has not landed (the `plan_reviewer` pair / `SelectItem.Status` this
  plan relocates/reuses are absent), or the "Current state" excerpts no
  longer match the live code (content drift, not line drift).
- `ValidateAgentSubagents`, `subagentKindFor`, `subagentNames`, or
  `selectableColumns` have callers beyond those named here — the signature
  changes are only safe with the stated call graph (grep each before editing).
- The projection save loses agents-doc keys it does not display (the whole
  doc must round-trip); or the coverage-test exception cannot be made without
  weakening the guard for non-projection sections.
- The generic TUI row editor cannot express the agent-page read-only shared
  row + single enable toggle without a bespoke widget (Step 8) — report the
  limitation and the fallback, do not redesign the editor engine.
- The default-disabled rule cannot be kept distinct from the per-agent
  custom's absence-means-enabled rule without a per-kind branch — report;
  never make a shared custom dispatch by default.
- A shared delete leaves a dangling reference that fails the loader, and the
  cross-agent cleanup cannot be done in one agents-section transaction.
- The architecture fan-out ratchet trips (`pkg/architecture`) — report, do
  not raise a ceiling.
- A step's verification fails twice after a reasonable fix attempt.
- The fix appears to require touching `pkg/tui/setup.go`, the built-in
  override path (ruling A), `agents.definitions.<type>` storage location, or
  any frozen prototype/reference file.

## Maintenance notes

- The shared registry's single source of truth is `agents.shared_subagents`;
  per-agent enablement is a reference entry `{name, enabled:true}` in
  `def.Subagents`, absence = disabled. Any future surface that lists an
  agent's subagents must merge shared customs with this default-disabled rule
  (gateway `subagentNames`/`subagentKindFor`, dispatch `SubagentCatalogFor`,
  TUI Step 8, web `SubagentsView`). A new consumer that forgets the merge
  silently hides shared subagents; one that forgets default-disabled silently
  dispatches them.
- The projection section (`StoreSection`) is a general mechanism: a future
  curated view of another stored section reuses it. Reviewers must check the
  whole-doc round-trip (no key loss) and that the coverage guard still holds
  for non-projection sections.
- Per-type built-in defaults live in `agents.subagent_models.<type>`
  ({model, reasoning_effort}, plan 315 deleted the old provider lists). The
  projection section reads them. Per-agent built-in overrides stay (ruling
  A). If a future ruling revisits either, the projection's per-type rows and
  the builtin override path are the touch points.
- Shared subagent models are stored as row ids (global resolution, the
  goal-evaluator pattern). A legacy `provider/model` label on a shared entry
  is normalized on write; dispatch resolves the id globally regardless of the
  consuming agent.
- Review scrutiny points: default-disabled proven by a dispatch test; the
  reference-entry field refusal; the cross-agent reference cleanup on delete;
  the projection's whole-doc save; the builtin override path byte-for-byte
  unchanged; i18n key parity zh/en; the two frozen hashes.