# Plan 319: Every `reasoning_effort` row is a picker, not free text (TUI /config + web settings)

> **Executor instructions**: steps in order; run each verification command and
> compare the output before moving on. On a STOP condition, stop and report.
> Update this plan's row in `plans/README.md` when done unless your dispatcher
> maintains the index.
>
> **Drift check (run first)**:
> ```sh
> cd /Users/doudou/workspace/unionj-cloud/forebrain-harness
> git rev-parse --short HEAD        # planned at 1013b50f; start from the dispatch HEAD (316 landed aace268f)
> git log --oneline 1013b50f..HEAD -- pkg/tui/config_panel*.go frontend/src/components/settings frontend/src/lib/settingsSchema.ts   # expect 313/315/318 only
> git status --porcelain -- pkg/tui/config_panel.go pkg/tui/config_panel_schema.go pkg/tui/config_panel_test.go frontend/src/components/settings/GoalCheckModelGroup.vue frontend/src/lib/settingsSchema.ts frontend/src/components/settings/SettingsTabForm.vue frontend/src/locales/index.ts
> grep -n 'reasoning_effort' pkg/tui/config_panel_schema.go
> grep -n 'EFFORT_PATH\|kind:' frontend/src/components/settings/GoalCheckModelGroup.vue
> ```
> Expected: `reasoning_effort` rows are `configFieldText`; the web goal-check
> group declares `{ path: EFFORT_PATH, kind: 'text' }`. Content mismatch (not
> line drift) is the STOP condition. Plans 313/315 are landing in parallel:
> do not start before they are in `dev` (their schema rows are what this plan
> converts), and re-read their files when you start.

## Status

- **Priority**: P1 (owner 2026-11-17: thinking on/off and level semantics must be unified on every surface)
- **Effort**: M (two surfaces, one new field kind per surface, tests)
- **Risk**: LOW–MED (UI-only; storage shape and engine are untouched)
- **Depends on**: `plans/313` (the `reasoning_effort` rows and the web
  goal-check/plan-reviewer groups), `plans/315` (`agents.subagent_models`
  storage), `plans/318` (`turn.OrderedEfforts`, protocol-aware
  `turn.SetupReasoningEfforts`, gateway validation)
- **Category**: consistency / UX
- **Planned at**: `1013b50f`, 2026-11-17
- **Dispatch**: **owner approved on 2026-11-17** ("318/319 已批准派发").
  **Blocked until 313 and 315 are in `dev` and 318 has landed** (it converts
  their rows and needs 318's helper/order/validation). Do not start earlier.

## Why this matters

The audit found one surface family where a thinking level is still free text:

- TUI `/config`: `agents.goal_evaluator.reasoning_effort`,
  `agents.plan_reviewer.reasoning_effort` (plan 313),
  `definitions.<id>.subagents[].reasoning_effort`, and the
  `subagent_models.<type>.reasoning_effort` rows all render as
  `configFieldText` (`pkg/tui/config_panel_schema.go:159`, `:210`,
  `:453`-class entries; `pkg/tui/config_panel.go:748-850` generic row editor).
- Web settings: `GoalCheckModelGroup.vue` declares `{ path: EFFORT_PATH,
  kind: 'text' }`; plan 313 adds the same shape for the plan reviewer.

Every other surface offers a list: `/model`, the exit gate, the chat chip, the
web subagent drawer, and `/connect` after 311. Free text lets a user store
`Highest`, `none` for a model that cannot turn thinking off, or a level the
row's protocol silently drops — and `pkg/config/agents.go:313-328` only checks
"effort requires model". The confirmed prototype already draws the target:
`plans/config-tui-prototype.html` uses `kind:'effort'` with candidates from the
chosen model row, `Off` only when the row is off-capable and the protocol can
send `none`, and `Row default` for the empty value (`effortLabel`, `:63`;
`choicesFor`, `:124`).

## Contract

For an effort row whose sibling `model` value is a configured model row
(`row id` for `default_model`/`subagent_models`, or a `provider/model` pair for
a legacy pair):

1. Levels = `turn.SetupReasoningEfforts(row.Provider, row.Model, row.APIPath, levelsFromRow)`
   (plan 318's shared helper), in `turn.OrderedEfforts` order.
2. `Off` (`none`) is offered **only** when the row is off-capable (its
   `cap_reasoning_off`, or the catalog's `none`) **and** `none` survives the
   sendable filter. Never infer it from the model name or provider.
3. The empty value is offered last and labelled `Row default` — it means "no
   override; follow that model row's default", not "provider default"
   (`/connect`'s row-level default page keeps `Provider default`; that is a
   different object).
4. No sibling model → the row refuses to open with the existing notice
   (`Choose a model before setting its thinking level.`); no candidates are
   invented. An unknown model (no row, no catalog entry) → only `Row default`
   plus any levels the row stores; do not guess.
5. Labels: TUI `turn.EffortEnglishLabel`; web `effortLevelLabel`. No new label
   table, no `Highest`, no `·`.

## Scope

**In scope**:
- `pkg/tui/config_panel_schema.go` (a `configFieldEffort` kind for the four row
  families above), `pkg/tui/config_panel.go` (open the choice editor with the
  candidates from §Contract; write the chosen string/empty), tests
- `frontend/src/components/settings/GoalCheckModelGroup.vue` (and the
  plan-reviewer group plan 313 adds) — `kind: 'effort'`
- `frontend/src/components/settings/SettingsTabForm.vue` and
  `frontend/src/lib/settingsSchema.ts` — only the minimum needed to support
  the new field kind and its option source
- `frontend/src/locales/index.ts` — reuse existing keys (`chat.effort*`,
  `subagents.effortNeedsModel`); add none unless a label is genuinely missing
- tests: `pkg/tui/config_panel_test.go`,
  `frontend/src/components/settings/*.test.ts`

**Out of scope**: engine/gateway/state (318 owns them), `/connect`
(`pkg/tui/setup.go`, plan 311), prototype files (already fixed by the audit),
`assets/models.json`, the config YAML shape (still `reasoning_effort: <string>`).

## Steps

### Step 1: TUI field kind

Current state: `configFieldText` renders an edit box through the generic row
editor; `config_panel_schema.go` has kinds `configFieldText`, `configFieldBool`,
`configFieldEnum`, `configFieldModel`, … (grep `configField` in that file).

1. Add `configFieldEffort`. In `config_panel.go`, where a field kind chooses
   `edit` vs `choice`, route it to the existing single-choice picker
   (`selectRich`, the same one `/model`'s effort step uses is not available in
   the panel — use the panel's own choice view `panelChoiceRows`, the one
   `configFieldEnum` uses).
2. Build candidates per §Contract. The sibling model value is read from the
   same section doc (`sectionDoc`/`raw`); resolve a row id through the
   configured model rows, and a `provider/model` pair through the same lookup
   `turn.ModelChoices` uses. Add a small helper in `pkg/tui` (not a new
   package) and unit-test it directly.
3. Write the selected value with the existing `UpdateSection` path; empty
   writes `""` (the config field must still be present/absent exactly as the
   row editor does today).
4. Change the schema rows to `configFieldEffort`:
   `agents.goal_evaluator.reasoning_effort`,
   `agents.plan_reviewer.reasoning_effort` (after 313 lands),
   `definitions.<id>.subagents[].reasoning_effort`,
   `subagent_models.<type>.reasoning_effort` (after 315/314 land).
   Keep the current hints' meaning, shortened to one line; do not reintroduce
   `·`.

**Verify**:
```sh
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'ConfigPanelEffort|ConfigPanelSchema' -count=1 -v
```
New tests: (a) a row whose model has `[low, medium, high, xhigh]` opens a
picker with exactly those + `Row default`; (b) an off-capable + sendable row
adds `Off` first; (c) an Anthropic-protocol row with `off:true` does **not**;
(d) a row without a model shows the existing notice and no picker; (e) the
chosen value is what `UpdateSection` writes (empty for `Row default`).

### Step 2: web field kind

Current state: `GoalCheckModelGroup.vue` builds `fields: [{ path: MODEL_PATH,
kind: 'model' }, { path: EFFORT_PATH, kind: 'text' }]` and clears the effort
when the model changes.

1. Add `kind: 'effort'` to the settings field kinds and render it with the same
   select component the subagent drawer uses (`SubagentConfigDrawer.vue`'s
   effort select), fed by the chosen model's `SessionModelOption.efforts`
   (plan 318 makes that list sorted and sendable-filtered). Empty option =
   `Row default` (`subagents.followModelDefault`-class key; reuse the existing
   one, do not add a synonym).
2. Do **not** add client-side level lists; the option list is the API's.
3. Keep the "clear effort when the model changes" behavior
   (the `onModel` watcher in `GoalCheckModelGroup.vue`) — it is the web mirror
   of the TUI's
   "unset follows the row".
4. Apply the same to the plan-reviewer group plan 313 adds.

**Verify**:
```sh
cd frontend && pnpm typecheck && pnpm vitest run src/components/settings/GoalCheckModelGroup.test.ts src/components/settings/SettingsTabForm.test.ts
```
New tests: the effort control renders options from the selected model; no model
→ disabled + `effortNeedsModel`; changing the model clears the effort; saving
sends the chosen string / `""`.

### Step 3: regression

```sh
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/config ./pkg/gateway -count=1
gofmt -l pkg/tui && CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui
cd frontend && pnpm typecheck && pnpm test
```
Expected: all green; no engine/gateway file changed (`git status` reviewed).

## Done criteria

- `grep -n "reasoning_effort" pkg/tui/config_panel_schema.go` shows no
  `configFieldText` for an effort path.
- Web settings has no `kind: 'text'` for an effort path.
- Both surfaces show `Row default`, `Off` only when sendable, and the shared
  label/order (tests above).
- No file outside Scope changed.

## STOP conditions

- The config panel's choice machinery cannot carry a per-row candidate list
  without a redesign of `panelChoiceRows` — report the shape instead of
  inventing a parallel picker.
- Plans 313/315 are not in `dev` yet, or their storage shape differs from the
  §Contract's "sibling model value" assumption.
- The web settings form has no way to read the selected model's options
  without a new API call — report; do not add client-side catalogs.

## Maintenance notes

- The effort field's candidate source is the model row, so the row order matters:
  the model field must be read before the effort field (the prototype's
  `requires` already encodes this).
- `Provider default` remains exclusive to `/connect`'s model-default page; do
  not reuse it here.
- If a future effort axis appears (e.g. token budget), it extends
  `turn.SetupReasoningEfforts` and the picker follows — never a new local list.
