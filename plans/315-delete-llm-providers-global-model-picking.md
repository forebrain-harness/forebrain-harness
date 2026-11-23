# Plan 315: Delete llm_providers entirely — every model choice picks a row from the global model table

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If a "STOP conditions" item occurs, stop and report — do not improvise.
> When done, update this plan's row in `plans/README.md` unless a reviewer
> told you they maintain the index.
>
> **Drift check (run first)**: `git status --porcelain -- <in-scope paths>`
> and `git diff <baseline> -- <same paths>`. Baseline = the commit you start
> from (record in Status). Compare "Current state" excerpts to live code;
> content mismatch = STOP (line drift alone is not).
>
> **READ §"2026-11-17 execute amendment" AT THE END OF THIS FILE FIRST.** It
> corrects Scope, adds pkg/process + the fixture sweep + the `/config`
> retired-key rule, and overrides the sections above where they disagree.
>
> **2026-11-17 reconcile (HEAD `fcb241bc`)**: drift since `7611d6cc` in scope
> is exactly the merged catalog_provider / home-models.json work, all
> additive to this plan's contract: `pkg/config/llm_models.go` +5 (new
> `LLMModelConfig.CatalogProvider` field; `EffectiveDefaultLLMFor` /
> `ownDefaultModel` / `ownLegacyEntries` / `HasOwnModelChoice` bodies
> unchanged, lines +5), `pkg/gateway/models_api.go` +84 (catalog_provider
> read/write + `llmModelCatalogCaps` now
> `(provider, model, catalogProvider string) (llmModelCaps, string)` via
> `llm.LookupCatalogFor`; hunks confined to caps/catalog-provider plumbing —
> row/write types, `llmModelCapsOfRow`, `llmModelRowOf`, the catalog-caps
> handler, `llmModelFromWrite`, `llmModelCapsValid`; the row-listing endpoint
> and its ordering are untouched), `pkg/run/config.go` +5/−2 cumulative
> (`agentModelRowFor` uses `llm.LookupCatalogFor(provider, model,
> override.Caps.CatalogKey)`; `modelHitFor` passes provider to
> `OverlayModelOverride` — see the merged-fix note below;
> `NewLLMForAgentType` unchanged), `frontend/src/lib/api.ts` +22 (additive
> `catalogProvider`/`catalogId` types + query param). No in-scope file has
> uncommitted changes (the parallel session's WIP sits in `pkg/tui/setup.go`
> — out of scope here — `assets/models.json`, `scripts/test-db.sh`).
> `pkg/config/agents.go`, `load.go`, `agent_llm.go`, `pkg/run/runner.go`,
> `subagent_catalog.go`, gateway `subagents_api.go`/`agents_api.go`, TUI
> `config_panel*.go`, `SubagentsView.vue`, `SubagentConfigDrawer.vue`: zero
> drift. Frozen prototype hash re-verified (`95e75ded…`; re-frozen by the
> 2026-11-17 reasoning audit — effort picker sendability + `Row default`
> label); regression now
> reports **26 passed**, not 20.
> **Merged parallel fix (verified 2026-11-17, dev@`9b646893`)**: `9b646893`
> (owner-approved ff-only merge of `.worktrees/fix-custom-effort-vendor-wins`;
> worktree/branch deleted after the merge per owner's standing rule) makes
> `applyModelOverride` vendor-wins
> (catalog levels beat a row's stale `cap_efforts` unless the catalog has
> none or the provider is login-backed; `OverlayModelOverride` gains a
> provider param — `pkg/run/config.go`'s `modelHitFor` call updated, so that
> file's cumulative drift since `7611d6cc` is now +5/−2). This plan pins no
> row-wins assertion (grep-verified); the global-row pickers' effort
> candidates inherit the new priority through the existing read paths —
> re-verify on this HEAD before starting. The uncommitted main-tree
> setup.go / models.json / test-db.sh changes are **someone else's/owner's
> WIP** (forebrain-harness-fb disclaimed them), not that session's; leave
> them untouched.

## Status

- **Priority**: P1 (本期)
- **Effort**: L
- **Risk**: HIGH (deletes a stored config field with NO migration; old
  per-agent / per-type provider models are dropped and fall back to the
  global table / main — owner explicitly accepted "删干净，不做迁移")
- **Depends on**: none hard. **313 and 314 depend on THIS plan** (they read
  the new per-type storage and global-row pickers). Land 315 first.
  Do not run in parallel with 311/312 (`pkg/tui/setup.go`).
- **Category**: migration / feature
- **Planned at**: 2026-11-16, on top of `7611d6cc`

## Why this matters

Owner ruling (2026-11-16, final, supersedes the earlier "keep legacy
per-type storage" withdrawal): **primary agents no longer carry
`llm_providers`, AND `definitions.<built-in type>.llm_providers` is deleted
too — clean, no migration. Everywhere you only PICK a model (a row of the
global model table); you never configure an llm provider.** The global model
table (`config.Root.LLMModels`) is the single provider/credential store,
configured only via `/connect` (TUI) and Settings → Models (web). The
"subagent model config is global, shared by all primary agents" semantics
survive, but as a global-row reference (model row id + reasoning effort),
not a provider list.

The design contract is the updated prototype
`plans/config-tui-prototype.html`
(sha256 `95e75dedd9479317e23464d1978efc6a9301e3dfead0efade0bcbee9c8e9e688`):
agent template has NO Model providers row and a `globalModel` default-model
picker; the `subagent_models` section's per-type rows are model+effort global
pickers (shape `{model, reasoning_effort}`, same as the plan-reviewer /
goal-evaluator global rows); subagent override model pickers list global
rows. Regression `plans/config-tui-prototype.test.cjs` is green (26 results: 25 checks + the nested-transition result; frozen hash re-verified 2026-11-17).

## Current state

Verified at `7611d6cc` (re-verify on your baseline):

- `pkg/config/agents.go`: `AgentDefinition` carries `LLMProviders
  []AgentLLMProviderConfig` (yaml `llm_providers`) and `DefaultModel string`.
  `SubagentConfig` (:67–87) = Name/Description/SystemPrompt/Model/
  ReasoningEffort/Tools/AllowedTools/Continuable/Enabled — the shape reused
  for global rows. `AgentsSection` (:13–21) holds Defaults/Definitions/
  GoalEvaluator (+313's PlanReviewer).
- `pkg/config/llm_models.go`: `LLMModelConfig` (:17) = the global table row
  (ID/Provider/Model/BaseURL/APIPath/Caps/Params…). `ConfiguredLLMs(root)`
  projects the global table (+ any agent view). `EffectiveDefaultLLMFor`
  (:138) order: agent default_model → agent own legacy first → main
  default_model → main legacy first → view first. `ownDefaultModel` (:191)
  and `ownLegacyEntries` (:277) read `def.LLMProviders`;
  `HasOwnModelChoice` (:163) = default_model non-empty OR own legacy entries.
  `expandLLMProviderConfigs` (agent_llm.go:93) expands a `models` list.
- `pkg/config/load.go`: `validatePrimaryAgents` (:919–955): a `default_model`
  containing "/" must resolve via `ResolveModelRef(r, id, dm)` against the
  agent's own list (:935–940); a row id is skipped (plan 260).
  `ResolveModelRef`/`resolveModelRef`: the **row-id branch is global**
  (ignores agent name); the legacy-label branch matches the view.
- `pkg/run/runner.go`: per-type clients built at :982–996 from
  `HasOwnModelChoice`+`NewLLMForAgentType` for `agent.PublicTypeNames()`;
  `SubagentOwnModel` (:1845) = catalog entry → public+HasOwnModelChoice+
  EffectiveDefaultLLMFor. `pkg/run/config.go` `NewLLMForAgentType` (:247)
  resolves via EffectiveDefaultLLMFor (:327).
- `pkg/gateway/subagents_api.go`: `subagentRowModel` (:270–301) global branch
  = `HasOwnModelChoice(cfg,name)` + `EffectiveDefaultLLMFor(cfg,name)`;
  `subagentRowModelFields` (:303) labels via
  `llm.FormatProviderModelDisplay`. `handleAgentModelOptions` (:692) serves
  the drawer picker from `sessionModelOptions`.
- `pkg/tui/config_panel.go`: the generic editor exposes whatever keys the
  section doc holds — `definitions.<id>.llm_providers` is reachable today;
  `default_model` is a scalar text row. `pkg/tui/setup.go` `/connect` writes
  the global table (do NOT touch setup.go here).
- Web: `frontend/src/components/settings/ModelsTab.vue` manages the global
  table (`forebrainApi.llmProviders()` :4651). There is NO per-agent
  provider-editing surface on web today (only ModelsTab + chat ModelChip read
  the global listing) — web already single-entry.

Conventions: table-driven Go tests beside code; comments cite "plan 315";
TUI copy English, web copy i18n; **no `·` connectors in any new copy (use
spaces; plan 316 sweeps existing ones)**.

## Commands you will need

```bash
export CGO_ENABLED=1
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
```
Every `go test` prefixed `FOREBRAIN_HOME="$(mktemp -d)"` (never inherit the
ambient real-install home). Focused gate (bare pipes, `-v`, check `--- PASS`,
never `[no tests to run]`):
```bash
FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/config ./pkg/run ./pkg/gateway ./pkg/tui \
  -run 'Subagent|DefaultLLM|OwnModel|PrimaryAgent|ConfigPanel|ModelOptions' -count=1 -v 2>&1 | tee /tmp/315.log
grep -c '^--- PASS' /tmp/315.log; grep -c 'no tests to run' /tmp/315.log
```
Full: `go build -tags fts5 ./...`; `go vet -tags fts5 ./pkg/config ./pkg/run ./pkg/gateway ./pkg/tui`;
`gofmt -l pkg/config pkg/run pkg/gateway pkg/tui`;
`FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/config ./pkg/run ./pkg/gateway ./pkg/tui ./pkg/architecture -count=1`;
`cd frontend && pnpm typecheck && pnpm vitest run src/components/settings src/views/SubagentsView.test.ts && pnpm run build`;
prototype: `cd plans && node config-tui-prototype.test.cjs` (needs
`python3 -m http.server 8765 --bind 127.0.0.1 --directory plans`) → 26 passed.

## Scope

**In scope**: `pkg/config/agents.go`, `pkg/config/llm_models.go`,
`pkg/config/agent_llm.go`, `pkg/config/load.go` + their tests;
`pkg/run/runner.go`, `pkg/run/config.go`, `pkg/run/subagent_catalog.go` +
tests; `pkg/gateway/subagents_api.go`, `pkg/gateway/models_api.go`,
`pkg/gateway/agents_api.go` + tests; `pkg/tui/config_panel.go`,
`pkg/tui/config_panel_schema.go` + tests; `frontend/src/lib/api.ts`,
`frontend/src/views/SubagentsView.vue`(+test),
`frontend/src/components/subagents/SubagentConfigDrawer.vue`(+test);
`plans/README.md` (status row).

**Out of scope**: `pkg/tui/setup.go` (/connect, 311/312); the global table's
own CRUD (ModelsTab / /connect already own it); per-agent built-in override
*prompt/enabled* semantics (ruling A keeps; only the override's model
*picker* becomes global-row); shared-custom registry (314); `·` sweep of
unrelated copy (316); frozen prototypes.

## Steps

### Step 1: config — new per-type global row storage; delete LLMProviders

Add to `AgentsSection`:
```go
// SubagentModels holds the install-wide default model row for each public
// built-in subagent type (plan 315): a global-table row id + reasoning
// effort, shared by every primary agent. Replaces the deleted
// definitions.<type>.llm_providers provider list. An agent's own override
// (def.Subagents) still wins.
SubagentModels map[string]SubagentConfig `yaml:"subagent_models,omitempty" json:"subagent_models,omitempty"`
```
Delete `LLMProviders` from `AgentDefinition`. Delete `ownLegacyEntries` and
the legacy branches of `ownDefaultModel`/`EffectiveDefaultLLMFor`/
`HasOwnModelChoice`; rewrite them to read, for a TYPE key,
`root.Agents.SubagentModels[type]` (Model row id + ReasoningEffort), and for a
PRIMARY agent only `default_model` (a global row id). Keep the
main-fallback and view-first fallbacks of `EffectiveDefaultLLMFor`.
`expandLLMProviderConfigs` stays only if the global table still uses a
`models` list; if unused after deletion, remove it and its callers (grep).

Add `ValidateSubagentModels(map[string]SubagentConfig, refResolves)` mirroring
`ValidateGoalEvaluatorModel`: key must be a public built-in type; Model a
resolvable global row id or empty; effort requires model; reuse
`validateSubagentModel`. Call it in `load.go` beside the goal-evaluator call
with the `"main"` resolver.

`validatePrimaryAgents`: `default_model` is now a GLOBAL row id — drop the
"must name one of this agent's llm_providers" rule; a dangling row id is
skipped (plan 260 semantics); a legacy `provider/model` label on a primary
agent (pointing at a now-deleted own row) is treated as UNSET — the agent
follows the global default — so old installs still boot with no migration
(owner 2026-11-16: legacy 标签视为未设置).

**No migration**: old `llm_providers` keys in stored YAML/JSON are ignored by
the loader (unknown field) and dropped on next write. Do NOT write a
migration; do NOT read old llm_providers anywhere.

Tests: validator cases; `EffectiveDefaultLLMFor`/`HasOwnModelChoice` for a
type with/without a SubagentModels row; primary default_model row id;
dangling id skipped; legacy label on primary treated as unset (follows
global); a load with old
`llm_providers` keys still loads (ignored).

**Verify**: focused config gate `--- PASS`; `go build` exit 0.

### Step 2: run — dispatch reads the new storage

`runner.go` per-type clients (:982–996) and `SubagentOwnModel` (:1845) and
`config.go NewLLMForAgentType` (:327) now resolve through the rewritten
`HasOwnModelChoice`/`EffectiveDefaultLLMFor` (Step 1) — mostly no change
needed beyond compiling against the deleted field; fix any direct
`def.LLMProviders` reads (grep `LLMProviders` across pkg/). A type's effort
comes from `SubagentModels[type].ReasoningEffort` (not params.reasoning).
`subagent_catalog.go` `Model` for a type override unchanged (agent entry
first); the global fallback now reads SubagentModels.

Tests: a type with a SubagentModels row dispatches on that row + effort; a
type without follows conversation; an agent override wins; office/batch
narrowing unchanged.

**Verify**: focused run gate `--- PASS`.

### Step 3: gateway — rows, pickers and writes use global rows

`subagentRowModel` global branch reads `SubagentModels[name]` (row id →
`ResolveModelRef(cfg,"main",id)` → label + effort). `model_source:"global"`
unchanged. `handleAgentModelOptions`/`sessionModelOptions` already list the
global table — the drawer/override picker now offers global rows (it already
does via model-options); ensure the override write stores a row id
(`subagentModelRef` normalizes label→id against the global view).
`agents_api.go` create/update: drop any llm_providers handling; default_model
accepted as a global row id.

Tests: builtin row global source label+effort from SubagentModels; override
row id wins; model-options lists global rows; create with default_model row
id persists.

**Verify**: focused gateway gate `--- PASS`.

### Step 4: TUI — hide provider lists; default_model becomes a global picker

`config_panel.go`: under `definitions.<id>` (a primary agent) do NOT expose
`llm_providers` (the field is gone from the struct, so the generic doc no
longer carries it — verify nothing re-adds it). Make `default_model` a
picker over the global table rather than free text: add a field kind or an
enum-like options source listing `ConfiguredLLMs` rows (id + display label),
empty = "Follow the global default". Under the `subagent_models` projection
(314) the per-type rows are model+effort pickers; here ensure the agents
section does not offer provider editing.

Tests: definitions.<id> page has no Model providers row; default_model
picker lists global rows and stores a row id; empty follows global default.

**Verify**: focused tui gate `--- PASS`; `gofmt -l pkg/tui` empty.

### Step 5: web — override/default pickers global; no provider surface

`SubagentConfigDrawer.vue` builtin/override model picker already uses
model-options (global) — confirm it stores a row id and effort belongs to
the chosen row; remove any own-provider assumptions. `SubagentsView.vue`
builtin global-source tooltip/label reads the new storage via the gateway
row (no frontend storage change). `api.ts`: drop any llm_providers typed
field on agent definitions if present.

Tests: drawer override picks a global row; builtin row shows global label;
no provider editing anywhere.

**Verify**: `pnpm typecheck && pnpm vitest run … && pnpm run build` exit 0.

### Step 6: gates + index

Run every command; prototype regression 26 passed; update README row.

## Done criteria

- [ ] build/vet/gofmt clean; full package tests + architecture green
- [ ] `grep -rn "LLMProviders" pkg/ --include=*.go | grep -v _test` returns no matches (field deleted; tests may reference old shape only if updated)
- [ ] `grep -n "SubagentModels" pkg/config/agents.go pkg/config/load.go` shows field + validator call
- [ ] a type with a SubagentModels row dispatches on it (test); without follows conversation (test)
- [ ] primary default_model stores a global row id (test); old llm_providers keys load ignored (test)
- [ ] frontend typecheck/build/vitest green; prototype 26 passed
- [ ] hashes: plans/config-tui-prototype.html = `95e75dedd9479317e23464d1978efc6a9301e3dfead0efade0bcbee9c8e9e688`
- [ ] no out-of-scope files modified; README row updated

## STOP conditions

- Deleting LLMProviders breaks a caller you cannot resolve within scope
  (grep the full call graph first: `grep -rn "LLMProviders\|ownLegacyEntries\|expandLLMProviderConfigs" pkg/`).
- The loader FAILS on an old config carrying llm_providers (it must ignore
  unknown keys); if it uses strict/KnownFields unmarshal, report — do not
  add a migration.
- A model picker cannot list the global table without a new gateway endpoint
  beyond model-options (report; reuse model-options).
- The architecture fan-out ratchet trips.
- A verification fails twice after a reasonable fix.
- You need to touch setup.go, frozen prototypes, or add a migration.

## Maintenance notes

- Single source of truth for provider/credential config = global table
  (`LLMModels`), via /connect + Settings → Models. Every other model choice
  is a row-id pick. A future surface that lets users type a provider/endpoint
  outside those two entries violates the owner ruling.
- Per-type global defaults live in `agents.subagent_models.<type>`
  ({model, reasoning_effort}); per-agent built-in overrides in
  `def.Subagents` (ruling A). Resolution order stays agent → global →
  conversation.
- No migration was written (owner: 删干净不做迁移). Old installs lose
  per-agent/per-type provider models and fall back to global/main — accepted.
- Reviewers: confirm no residual llm_providers read/write, the dangling-id
  skip, and that effort now comes from the row reference not params.

---

## 2026-11-17 execute amendment — scope correction found at dispatch

> This section wins over Status / Scope / Done criteria / Steps above where
> they disagree. Everything here was verified live at dispatch on top of
> `1013b50f` (main tree clean).

### A. Why the amendment (the plan as written cannot run)

1. **`pkg/process` reads the field and is not in Scope.** `hasLegacyProviderLists`
   (`pkg/process/llm_models.go:241-248`) and the strip inside
   `rewriteLegacyModelSections` (`:549-552`) touch `def.LLMProviders`. Deleting
   the field breaks the build in a package the Scope never listed — the plan's
   own STOP condition. The one-time legacy *model* migration lives in this file
   (entry `migrateLegacyLLMModels`:131, called from `pkg/process/open.go:506`
   and `pkg/process/dotenv_migrate.go:124`).
2. **52 `_test.go` files use the field or seed `llm_providers`** as the only way
   an agent gets a model: `pkg/run` 12, `pkg/process` 12, `pkg/gateway` 11,
   `pkg/config` 6, `pkg/tui` 5, `pkg/turn` 4, `pkg/state` 1, `pkg/home` 1.
   Literal constructions stop compiling; seeded YAML fixtures lose their only
   model and fail at run time with `errModelNotConfigured`.
3. **The TUI `/config` panel never reads the typed struct.** It decodes the
   *stored* section row into an `any` tree (`pkg/tui/config_panel.go:448-458`
   `sectionDoc`), lists whatever keys the document holds
   (`configContainerKeys` :973-990, and see the comment at :662-664 "a provider
   inside an agent's llm_providers is reachable") and marshals the tree back on
   save (`configMarshalDoc` :1058-1070). So an old install's `llm_providers`
   stays visible and writable under `agents → definitions → <agent>`. Step 4's
   assumption ("the field is gone from the struct, so the generic doc no longer
   carries it") is wrong.
4. **A legacy `default_model: provider/model` is fatal twice.**
   `validatePrimaryAgents` (`pkg/config/load.go:933-940`) refuses to load it,
   and `danglingDefaultModelLabel` (`pkg/run/config.go:338-352`) converts it to
   `errModelNotConfigured` at run time. Step 1 mentions the first; the second is
   absent from the plan.

### B. Decisions — apply exactly, do not re-litigate

1. **Legacy labels mean "unset", enforced at load.** In `normalize`
   (`pkg/config/load.go:690`, called from :170 load, :324 Save, :964
   ParseRootYAML, `file_keys.go:122`): clear `definitions.<id>.default_model`
   when the value contains `/` and `ResolveModelRef(root, id, value)` does not
   resolve. Delete the label branch of `validatePrimaryAgents` (:933-940).
   Delete `danglingDefaultModelLabel` (`pkg/run/config.go:338-352`) and the
   branch reporting it inside `effectiveDefaultProvider` (:326-335); keep
   `errModelNotConfigured` for "nothing configured at all". The placeholder
   block in `normalize` that forces `LLMProviders: []{{}}` on main
   (`load.go:705-717`) and `NormalizeLLMProviderEntries` / `dropPlaceholderLLMProviders`
   (`load.go:337`, :352-372) all go with the field.
2. **The legacy *model* migration keeps its reference half, loses its provider
   half.** Do NOT delete `migrateLegacyLLMModels` wholesale: rewriting stored
   `default_model` / `subagents[].model` / `memories.*_model` legacy labels to
   row ids, and the `fb_session_model_state.model_id` backfill, still matter and
   are what keeps old sessions/memories pointing at real rows. Delete exactly:
   - `hasLegacyProviderLists` (:238-248) and the early-return branch that calls
     `rewriteLegacyModelSections` for it (:165-172) → plain `return nil`;
   - the `def.LLMProviders != nil` strip inside `rewriteLegacyModelSections`
     (:549-552);
   - `createLegacyModelRows` (:398-457), `loginModelEndpoint` (:466-493),
     `firstOwnLegacyModelID` (:356-369) and the "empty default_model → own
     first legacy entry" branch of `planModelReferenceEdits` (:313-320) — they
     only iterate `llm-legacy-` view entries, which no longer exist;
   - the `ResolvedLLMConfigs` leg of `legacyModelDataPresent` (:220-224).
   Keep `readLegacyDotEnv` and `resolveLegacySecret` — `migrateLegacyDotEnv`
   (`pkg/process/dotenv_migrate.go:56-95`) uses both and is independent of
   models (its six test cases in `dotenv_migrate_test.go` carry no
   `llm_providers`).
3. **`/config` never shows or writes a retired key.** In
   `configPanel.sectionDoc` (`pkg/tui/config_panel.go:448`), after decoding,
   delete `definitions.*.llm_providers` from the `agents` document before it is
   returned. The key then cannot appear as a row and cannot survive
   `configMarshalDoc`, so a save drops it. Everything else in the document must
   stay untouched.
4. **Fixtures move to the global table.** A fixture that exists only to give an
   agent a model becomes a `fb_llm_models` row plus `default_model: <row id>`
   (or `agents.subagent_models.<type>`), seeded with what already exists:
   `seedModelRowForTest` (`pkg/process/agent_switch_test.go:156`) in
   `pkg/process`, `state.LLMModelStore{DB: db}.Create` elsewhere
   (`pkg/tui/setup_test.go:2083` is an exemplar). A block that carries no
   assertion can simply be dropped. A test that asserts the old *behavior*
   (`TestEffectiveDefaultLLM`, `TestEffectiveDefaultLLMFor`,
   `TestLLMModelsMigrateMovesLegacyEntries*`, `…StripsThePlaceholder`,
   `TestSaveDropsPlaceholderLLMProviderAndLoadRestoresIt`,
   `TestSaveDoesNotLendAnAPIKeyToANewProviderEntry`,
   `TestPrimaryAgentFallsBackToMainAndNil`,
   `TestRunnerLoadsPrimaryAgentWithoutItsOwnProvider`,
   `TestAvailableModelOptionsUseConfiguredLLMProvidersOnly`, …) is deleted or
   rewritten to the new contract — never left asserting the old shape, never
   skipped.

### C. Scope (replaces the Scope section above)

In scope: everything the Scope lists, **plus**
`pkg/process/llm_models.go` (+ `open.go` / `dotenv_migrate.go` only if a call
shape must change), `pkg/run/config.go`, `pkg/tui/config_panel.go`, and every
`_test.go` file under `pkg/{config,run,gateway,tui,turn,process,state,home}`
carrying `LLMProviders` / `llm_providers`, plus their fixtures'
non-test helpers when a helper is the only way to seed a row.

Out of scope (unchanged, plus): `pkg/process/config_import.go`'s typed-import
behavior (it already drops the key — assert it, do not change it);
`pkg/state` migrations; `scripts/test-db.sh`; frozen prototypes; `.worktrees/`.

### D. Step order

Steps 1-5 as written (production code), each gated on `go build -tags fts5 ./...`
(non-test) plus the focused test gate; **Step 1b, Step 3b, Step 4b, Step 0'**
below are new and slot in at those points; the fixture sweep (Step 0') runs
after Step 5 because it is the only way `go vet`/`go test` compiles again; then
Step 6.

- **Step 1b (after Step 1): pkg/process provider half.** Apply B2 exactly.
  Verify: `go build -tags fts5 ./...` exit 0; `grep -n "LLMProviders\|LLMProviders != nil" pkg/process/*.go`
  empty; `grep -n "createLegacyModelRows\|loginModelEndpoint\|firstOwnLegacyModelID\|hasLegacyProviderLists" pkg/process/`
  empty.
- **Step 3b (after Step 3): gateway/system-row guard untouched** — no new work
  here; only confirm `subagentRowModelFields` still takes
  `config.AgentLLMProviderConfig` (the *type* stays).
- **Step 4b (after Step 4): `/config` retired key.** Apply B3. Test: seed a raw
  `agents` row whose `definitions.main` carries both `llm_providers` and other
  keys; assert (a) no rows for the retired key under a definition, (b) after a
  save the stored section JSON no longer contains `llm_providers`, (c) the
  other keys survive unchanged.
- **Step 0' (after Step 5): fixture sweep.** Apply B4 across the 52 files.
  Verify: `go vet -tags fts5 ./...` exit 0, then
  `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/config ./pkg/run ./pkg/gateway ./pkg/tui ./pkg/turn ./pkg/process ./pkg/state ./pkg/home ./pkg/architecture -count=1`
  → all `ok`.

### E. Done criteria (add to the list above)

- [ ] `grep -rn "LLMProviders" pkg/ --include=*.go | grep -v _test` empty
- [ ] `grep -rn "llm_providers" pkg/ --include=*.go | grep -v _test` empty
      (comments included — say "retired" without naming the key)
- [ ] `go vet -tags fts5 ./...` exit 0 (every test package compiles)
- [ ] full `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./... -count=1` green
- [ ] `pkg/process/llm_models.go` no longer names `createLegacyModelRows`,
      `loginModelEndpoint`, `firstOwnLegacyModelID`, `hasLegacyProviderLists`
- [ ] `/config` test proves the retired key is neither shown nor written back
- [ ] a config carrying `default_model: provider/model` still loads (test) and
      the agent runs on the global default

### F. STOP conditions (add)

- The work starts reworking `migrateLegacyLLMModels` beyond B2 (deleting the
  label rewriting or the session backfill).
- Any *new* migration appears, or stored `llm_providers` is read anywhere.
- `pkg/architecture` fan-out ratchet trips.
- More than two verification commands fail for the same reason.

## 2026-11-17 reasoning-semantics constraint (audit)

> Prototype gate: **owner re-confirmed both revised prototypes on 2026-11-17**
> ("原型都已确认"); config prototype hash `95e75ded…e688`, regression 26/26.
> The scope amendment above still governs.

> Scope is still governed by §"2026-11-17 execute amendment — scope correction
> found at dispatch" above (line 14's pointer now lands here; read that section
> too). This section only constrains the effort values in the new
> `{model, reasoning_effort}` storage.

This plan replaces provider lists with global-table row references and stores
`{model, reasoning_effort}` pairs. The pair's effort is **not** a free-form
string: it must be one of the referenced row's `turn.SetupReasoningEfforts` (plan
318 Step 1), or `""` meaning "follow the row's default". When Step 4/5 build
the model+effort picker:

- candidates come from the chosen row's sendable levels, in
  `turn.OrderedEfforts` order, plus empty (`Row default` / `Follow
  model row`); `Off` appears only when the row is off-capable **and** the
  protocol can send `none` (plan 311 §2.1 three-state);
- the picker must not invent a label table: `turn.EffortEnglishLabel` and the
  web's `effortLevelLabel` are the only spellings (`xHigh`, never `Highest`);
- plan 313 keeps `reasoning_effort` as a text row for now and plan 319
  replaces it with this picker. If 313 lands before 319, this plan's picker
  may ship first only if it uses the same helper; otherwise keep the text
  row and let 319 unify both. Do not add a third behavior.
- Same rule for the web `SubagentConfigDrawer` model-options path: it already
  reads `SessionModelOption.efforts`; plan 318 makes that list sorted and
  sendable-filtered, so no frontend list is built locally.