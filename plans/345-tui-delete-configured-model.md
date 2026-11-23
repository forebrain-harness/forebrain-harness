# Plan 345: Delete a configured model from the TUI /model picker — one shared delete semantics

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)** — baseline `c0d4ee16` (dev tip when this plan
> was written, working tree clean). Run BOTH commands (a two-commit diff is
> empty when baseline == HEAD and hides uncommitted edits):
>
> ```bash
> git status --porcelain -- pkg/config/llm_models.go pkg/config/llm_models_test.go \
>   pkg/process/llm_models.go pkg/process/llm_models_test.go \
>   pkg/gateway/models_api.go pkg/tui/chat_slash.go pkg/tui/run.go \
>   pkg/tui/commands_test.go pkg/tui/chat_session_test.go
> git diff c0d4ee16 -- pkg/config/llm_models.go pkg/process/llm_models.go \
>   pkg/gateway/models_api.go pkg/tui/chat_slash.go pkg/tui/run.go
> ```
>
> If any in-scope file changed, compare the "Current state" excerpts against
> the live code before proceeding. **Content mismatch is a STOP condition;
> moved line numbers alone are not** — re-locate by the quoted symbol/text.

## Status

- **Priority**: P2
- **Effort**: L
- **Risk**: HIGH (the web copy fix below repairs a data-losing defect: a delete that
  leaves the composed configuration invalid, with the row — and its key — gone)
- **Depends on**: none
- **Category**: bug + direction
- **Planned at**: commit `c0d4ee16`, 2026-11-21

## Why this matters

Owner report: **the TUI cannot delete a configured model.** The web surface
already has the whole feature (plan 265): `DELETE /api/llm-models/:id` with a
default-model guard (409 + the agents' names), immediate config adoption, and
references left dangling on purpose (plans 260/261) — a conversation's own
selection, an agent's default and the memory settings tolerate that; the pins
this plan's §7 covers do **not**, which is the defect the owner's copy ruling
uncovered. The TUI has no entry at all: `/model` only selects, `/connect` is a
sign-in wizard, and `/config` edits `fb_config` sections only — the model table
is state, not a section. A terminal-only user must open the web page (or
hand-edit the database) to remove a row they added by mistake.

Owner rulings that are this plan's contract (settled 2026-11-21, do not
re-litigate):

1. **Entry** = the `/model` list itself: cursor on a row, **Ctrl+D**, the
   `/config`-style removal confirmation (Keep preselected), then delete.
   No new slash command, no new tab, no new page.
2. **Default-model guard** = exactly the web semantics: a row some primary
   agent names as `default_model` is **refused** with the agents' names and
   "choose another default first". Never auto-rewrite a default.
3. **No interactive prototype this cycle** — this adds an existing,
   already-frozen interaction (`^D Remove` + confirmation, plan 326 D5) to an
   existing picker; it creates no new visual language.
4. Deleting the row the **current conversation** runs on keeps the web
   semantics: the stored session selection is left as written (stale), the
   next message is refused at turn entry with the existing "choose another
   with /model" diagnosis. That chain already exists — **no new code for it**.

The design converges the delete semantics into one implementation shared by
both surfaces: the guard moves to `pkg/config`, the delete orchestration to
`pkg/process` (the model table's existing process-level write API), the
gateway handler delegates, and the TUI grows a thin session capability plus
the picker affordance. The turn engine (`pkg/turn`) is **not touched**: a
delete is an install-wide settings write (the `/config` panel's category),
not a conversational choice.

### The web copy is wrong, and it is hiding a destructive defect (owner: fix this cycle)

Owner ruling 2026-11-21: *"先于本计划存在的 web 文案不准确 问题必须本期修复"*.
Investigating that copy turned up a real defect, reproduced below (evidence in
Current state §7): **deleting a row that a subagent, plan review or goal check
references as a row id leaves the composed configuration invalid.** The delete
succeeds — the row and its API key are gone — and then every configuration
compose fails, which means:

- the DELETE's own adapter (`SyncConfigFromDB`) fails, so the page answers
  500 after the row was already destroyed;
- every later turn fails closed at `SyncConfigFromDB` ("sync config: … model
  "llm-…" must be provider/model");
- every `/config` and web settings save is refused by the same validator.

The web copy says those agents merely "会回落" / "fall back" — the exact opposite
of what the code does. Fixing only the sentence would ship a truthful-looking
lie, so this plan fixes the cause and then the copy:

- **Guard, additively**: a row whose reference the loader *requires* to resolve
  (subagent type pin, plan review, goal check, a custom subagent's pin) is
  **refused**, exactly like a `default_model`. Nothing is silently rewritten.
- **Copy**: the confirm dialog states what actually happens (a conversation
  that used the row must re-choose with `/model`), the refusal dialog speaks
  about references in general, and the page badges name the owners — defaults
  and pins together.

## Current state

Everything below was read verbatim at `c0d4ee16`. Line numbers are anchors,
not contracts; the quoted text is the contract.

### 1. The web delete chain (the semantics to share)

`pkg/gateway/models_api.go` — the models page API. Route registration is
`pkg/gateway/api_extra.go:438` (`api.Delete("/llm-models/:id", s.handleLLMModelByID)`);
`handleLLMModelByID` dispatches DELETE to `deleteLLMModel` (`models_api.go:684-716`):

```go
// deleteLLMModel removes one row. A model an agent names as its default is
// refused: the reference is an explicit choice, and deleting through it would
// move that agent to another model without saying so. Sessions, subagents and
// memory settings that name the row are not rewritten — the runtime falls back
// (plans 260/261).
func (s *Server) deleteLLMModel(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.llmModelScope(w, r)
	if !ok {
		return
	}
	stored, err := scope.store.Get(r.Context(), pathParam(r, "id"))
	if err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	if names := llmModelDefaultFor(scope.cfg, stored.ID); len(names) > 0 {
		joined := strings.Join(names, "、")
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error": llmText(r,
				"它是「"+joined+"」的默认模型，请先把默认改成别的",
				"it is the default model of "+strings.Join(names, ", ")+"; choose another default first"),
			"default_for": names,
		})
		return
	}
	if err := scope.store.Delete(r.Context(), stored.ID); err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	if err := s.adoptLLMModelWrite(r.Context()); err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

The guard, `models_api.go:390-412` (private; **moves to `pkg/config` in Step 1**):

```go
// llmModelDefaultFor names the agents whose default_model is this row, by
// display name. The reference is an explicit choice, so a row one of them runs
// on cannot be deleted behind its back. A subagent definition's own Model is not
// a default and this helper does not look at it: nothing falls back to that row
// when it goes, the subagent simply follows the conversation's model.
func llmModelDefaultFor(cfg *config.Root, id string) []string {
	if cfg == nil || strings.TrimSpace(id) == "" {
		return nil
	}
	names := make([]string, 0, 1)
	for key, def := range cfg.Agents.Definitions {
		if strings.TrimSpace(def.DefaultModel) != id {
			continue
		}
		if name := strings.TrimSpace(def.DisplayName); name != "" {
			names = append(names, name)
			continue
		}
		names = append(names, key)
	}
	sort.Strings(names)
	return names
}
```

It has exactly **two** call sites (grep-verified): the delete handler above
and the row DTO at `models_api.go:501` — `DefaultFor: llmModelDefaultFor(scope.cfg, row.ID),`.
`sort.` appears nowhere else in `models_api.go` (grep-verified: only `:410`),
so the `sort` import goes away with the helper.

Adoption after a table write, `models_api.go:440-446`:

```go
func (s *Server) adoptLLMModelWrite(ctx context.Context) error {
	if s == nil || s.Env == nil {
		return nil
	}
	return s.Env.SyncConfigFromDB(ctx)
}
```

`llmModelScope` (`models_api.go:448-470`) reads `scope.cfg` from
`s.persistedConfig(r.Context())` → `Env.ComposedPersistedConfig` and
`scope.store = &state.LLMModelStore{DB: s.Env.SQL}`. `failLLMModelWrite`
(`models_api.go:1230-1256`) already maps `state.ErrLLMModelNotFound` → 404
("这个模型已经不在配置里了" / "this model is no longer configured") and anything
else → 500.

### 2. The store (unchanged by this plan)

`pkg/state/config_store.go`:

```go
// ErrLLMModelNotFound reports a row id that no configuration carries.
var ErrLLMModelNotFound = errors.New("model configuration not found")   // :545-546

// Get returns one configuration by id.                                  // :653-669
func (s *LLMModelStore) Get(ctx context.Context, id string) (LLMModel, error) {
	// trims id; "" or a missing row → ErrLLMModelNotFound
}

// Delete removes the row, reporting ErrLLMModelNotFound when it is not there. // :761-778
func (s *LLMModelStore) Delete(ctx context.Context, id string) error { ... }
```

`Create` (`:695`) requires provider+model, normalizes identity, and does not
require an API key at the store level (the gateway handler enforces keys).

### 3. The process-level write API (where the shared delete goes)

`pkg/process/llm_models.go` header:

```go
// The model table's process-level write API, the one-time migration that
// rewrites a pre-table install's model references into row ids, and the legacy
// reader the migrations of the retired home .env need (plans 262, 268).
// ... The write API is what a configuration surface calls to configure a
// model: the table is the only place a provider and its credential live (plan 315).
```

It already holds `LLMModelHeld` (:544), `UpsertLLMModel` (:582),
`SetAgentDefaultModel` (:730), `ProbeLLMModel` (:778). Imports already include
`context`, `database/sql`, `fmt`, `strings`, `appcfg ".../pkg/config"`,
`".../pkg/state"`. `DeleteLLMModel` belongs beside them.

`pkg/process/config_db.go`:

```go
// ComposedPersistedConfig reads the persisted form of the runtime            // :497-510
// configuration — the YAML's startup sections plus the stored sections ...
func (env *Environment) ComposedPersistedConfig(ctx context.Context) (appcfg.Root, error) {
	if env == nil || strings.TrimSpace(env.ConfigPath) == "" { ... }
	if env.SQL == nil { ... }
	return composeSectionsFrom(ctx, env.ConfigPath, env.SQL, nil)
}

// SyncConfigFromDB re-reads the composed configuration and adopts it when it  // :825-848
// changed. ... The steady state is one sqlite read, one file read, and a
// hash comparison; the full re-application runs only when the hash moved.
func (env *Environment) SyncConfigFromDB(ctx context.Context) error {
	if env == nil {
		return nil
	}
	if !env.runtimeConfigFromDB || env.SQL == nil || strings.TrimSpace(env.ConfigPath) == "" {
		// An environment that does not compose — the OpenOptions.Config test
		// bypass, or a hand-built one with no database — has nothing to sync;
		// the production surfaces always have all three.
		return nil
	}
	...
}
```

Note for tests: a hand-built `&process.Environment{Root: home, SQL: db}` with
`ConfigPath` set has `runtimeConfigFromDB == false`, so `SyncConfigFromDB` is a
**no-op** there — the fixture tests below assert the table write, not adoption.
Adoption itself is the gateway's existing, tested path.

### 4. The TUI /model flow today (select-only)

Engine side, `pkg/turn/executor.go:777-806` — `execModel` builds
`Picker{Command: "model", Title: "Model", Hint: "The model this agent runs on", Items: ...}`
from `ModelChoices(settings.Config, settings.AgentName)`; each item's `Value`
is the row id, `Description` the endpoint, `EndpointTail` the api path,
`Current` marks the selection. Empty table →
`Result{Handled: true, Reply: "This agent has no models configured."}`.
**This file is out of scope; nothing here changes.**

Surface side, `pkg/tui/run.go:5540-5590` — the whole current function:

```go
// runSlashPicker shows the picker a slash command offered and applies what
// is picked through the engine — the same choices, from the same data, the
// web shows as a card — following any picker the choice offers in turn.
func runSlashPicker(ctx context.Context, cmds *commandController, renderer *Renderer, tracker *Tracker, state *streamState, picker *turn.Picker) (handled bool, continueRun bool, submission ComposerSubmission, exitRequested bool) {
	// The picker opens on the item in force.
	items := make([]SelectItem, len(picker.Items))
	current := -1
	for i, item := range picker.Items {
		items[i] = SelectItem{Label: item.Label, Description: item.Description}
		if picker.Command == "model" {
			// The model list names a request URL, and a narrow column must not
			// cut its path off the end: the path is what tells two rows of one
			// provider apart (plan 267).
			items[i].ElideTail = item.EndpointTail
		}
		// The effort list draws what the web's effort chip draws: the level's
		// English name alone — never the raw level id beside it (owner
		// 2026-11-18: "Off none" is verbose) — and the gear on the level the
		// model row is configured with, against the name, never out in the
		// description column (owner 2026-11-15). An item drawn before the list
		// carried a display name keeps its plain label.
		if item.ValueLabel != "" {
			items[i].Label = item.ValueLabel
			items[i].Description = ""
			if item.FromSetting {
				items[i].Mark = selectorGearGlyph
			}
		}
		if item.Current {
			current = i
		}
	}
	label := picker.Title
	if hint := strings.TrimSpace(picker.Hint); hint != "" {
		label += "\n" + hint
	}
	idx, ok, err := cmds.selector.SelectRich(label, items, current)
	if err != nil {
		renderer.PrintError(err)
		return true, false, ComposerSubmission{}, false
	}
	if !ok || idx < 0 || idx >= len(picker.Items) {
		return true, false, ComposerSubmission{}, false
	}
	outcome := cmds.session.ChooseSurfaceSlash(ctx, state.sessionID, turn.SlashChoice{Command: picker.Command, Value: picker.Items[idx].Value})
	// A model, an effort or an agent may have changed under the footer.
	refreshSessionFooter(renderer, cmds.session, state.sessionID)
	// A picker choice authors a turn of its own: it has no draft, so nothing
	// is attached to it.
	return applySlashOutcome(ctx, cmds, renderer, tracker, state, strings.TrimSuffix(picker.Command, "-effort"), "", ComposerSubmission{}, outcome)
}
```

Call sites (grep-verified, all keep working unchanged): `run.go:537`
(`SlashPickerRequestedMsg` from the event loop — this is also how `/model`
opens during an active run), `run.go:5697` (`applySlashOutcome` follows a
choice's next picker, e.g. the effort list). Session plumbing:
`ChatSession.ExecuteSurfaceSlash` / `ChooseSurfaceSlash`
(`pkg/tui/chat_session.go:1985-1997`) route to `turn.Execute` / `turn.Choose`.

`/model` reads the **live runtime config**, `pkg/tui/chat_slash.go:1310-1318`:

```go
func modelConfigFromChatSession(s *ChatSession) *appcfg.Root {
	if s == nil {
		return nil
	}
	if s.runner() != nil && s.runner().Config() != nil {
		return s.runner().Config()
	}
	return s.cfg()
}
```

**Consequence**: after a delete the TUI must adopt
(`Env.SyncConfigFromDB`) before re-opening `/model`, or the refreshed list
would still show the deleted row — the exact defect the gateway's
`adoptLLMModelWrite` comment describes. The TUI already has this precedent:
`Session.SyncConfig` is in the interface (`pkg/tui/notify.go:1927-1933`, "so a
provider written by /connect governs this session without a restart") and
`/connect` calls it right after its write (`pkg/tui/setup.go:2808`).

### 5. The footer-action + confirmation machinery (reused as-is, out of scope)

`pkg/tui/overlays.go`:

- `ActionRichSelector` (:6482-6489): `SelectRichActions(label, items, defaultIdx, opts ActionOptions) (ActionResult, error)`; `rawSelector` implements it (`var _ ActionRichSelector = (*rawSelector)(nil)`).
- `ActionOptions` (:6446-6457): `FooterActions []FooterAction`, `Page bool` (the /config page style — **do not set it here**; `/model` keeps its plain picker look and its filter), `Multiline bool`.
- `ActionResult` (:6462-6466): `Row int`, `Action FooterActionKind`, `OK bool`; `Row` is -1 and `OK` false for Esc/Ctrl+C.
- `FooterAction` carries `Label`, `Key` (display string only), `Kind`, `RowActive func(itemIdx int) bool`. Exemplar, `config_panel.go:1327-1336`:

```go
FooterAction{Label: configAddActionLabel, Key: "a", Kind: FooterActionAdd},
FooterAction{
	Label: configRemoveActionLabel, Key: "d", Kind: FooterActionRemove,
	// Remove serves the entry rows alone (plan 326 D2) ...
	RowActive: func(itemIdx int) bool {
		return itemIdx >= 0 && itemIdx < len(keys) && p.configRowIsEntry(doc, path, keys[itemIdx])
	},
},
```

- **Gotcha (verified)** — `fireAction` (:6360-6392) reports the cursor's row
  **only when `RowActive != nil`**:

```go
row := -1
if a.RowActive != nil {
	row = p.absoluteRow()
}
return ActionResult{Row: row, Action: a.Kind, OK: true}, true
```

  So the `/model` action **must** set `RowActive` (every row is a removable
  entry: `func(i int) bool { return i >= 0 && i < len(items) }`), or `res.Row`
  arrives as -1 and the delete never knows which row.

- Key binding (same function): `FooterActionRemove` fires on `rawKeyCtrlD`
  **always**, and on a bare `'d'` rune only when `p.noFilter`.
  `SelectRichActions` (:6536-6567) sets `p.noFilter = !pickerShowsFilter(len(p.selectable))`
  when `opts.Page` is false, and `pickerFilterMinRows = 8` (:5045). The
  `/model` list shows a filter from 8 rows on, where bare `d` types into the
  filter — therefore the footer key string here is **"Ctrl+D"** (the key that
  works in both modes). `Key` is display-only (`footerHint`, :6338-6352);
  the binding switches on `Kind`. `/config` pages use `"d"` because they are
  `Page: true` (no filter) — do not copy that string here.
- `absoluteRow()` (:6262-6267) is `p.selectable[p.cursor]` — the index into
  the **original items slice**, correct even while a filter is active.

Confirmation exemplar, `pkg/tui/config_panel.go:1980-2002`:

```go
// confirmRemoveRow asks before any removal (plan 326 D5): the heading names
// the row and shows what it holds ... and the rows are
// Remove item and Keep item with Keep preselected, so Enter alone changes
// nothing. Esc keeps the row too. The Remove row is a danger row (prototype
// .item.danger): the choice that discards the row is the red one.
func (p *configPanel) confirmRemoveRow(label string, doc any, rowPath []string, field configField) bool {
	value, _ := configLookupValue(doc, rowPath)
	preview := configRowDescription(value)
	if field.Secret || configIsSecretPath(rowPath...) {
		preview = configSecretDisplay(value, true)
	}
	items := []SelectItem{
		{Label: configRemoveConfirmLabel, Danger: true},
		{Label: configKeepConfirmLabel},
	}
	idx, ok, err := selectRichPage(p.sel, "Remove "+label+"?\n"+preview, items, 1)
	if err != nil {
		p.printError(err)
		return false
	}
	return ok && idx == 0
}
```

Constants to reuse (`config_panel.go:41-63`, same package):
`configRemoveActionLabel = "Remove"`, `configRemoveConfirmLabel = "Remove item"`,
`configKeepConfirmLabel = "Keep item"`. `SelectItem.Danger`
(`channels.go:641-645`) is the red destructive label; `selectRichPage`
(`tabbed_picker.go:520-525`) falls back to `SelectRich` on selectors without
the page capability.

### 6. The dangling-reference chain (exists; out of scope; no new code)

- Turn entry, `pkg/run/config.go:2371-2381` (`injectSessionModelSelection`):
  a stored selection whose row is gone →
  `"the model chosen for this conversation (%s/%s) is no longer configured; choose another with /model"`,
  the stored row left as written.
- Session open, `pkg/tui/chat_session.go:676-690`: opens on the config
  default and warns `"The model saved for this conversation (%s / %s) is no longer configured; your next message will be refused until you choose one with /model."`.
- `/config` labels a pin whose row was deleted as the reference as written
  (`config_panel.go:1817-1835`); `EffectiveDefaultLLMFor` skips a dangling
  `default_model` with a Warn (`pkg/config/llm_models.go:112-118`, plan 315).

### 7. The destructive defect behind the inaccurate copy (reproduced 2026-11-21)

**Every strict model reference** (a value the loader must resolve) is one of
these five sites — grep-verified, closed set:

| Site in `pkg/config/load.go` | Reference | Owner label to show |
|---|---|---|
| `:207` `ValidateGoalEvaluatorModel` | `agents.goal_evaluator.model` | goal check |
| `:216` `ValidatePlanReviewerModel` | `agents.plan_reviewer.model` | plan review |
| `:224` `ValidateSubagentModels` | `agents.subagent_models.<type>.model` | subagent `<type>` |
| `validateDefinitionRoles` | `agents.definitions.<role>.model` — a **subagent role's own** model field, judged only when the definition is not an effective primary (the function skips primaries first) | subagent `<role>` |
| `:1066` `ValidateAgentSubagents` | `agents.definitions.<agent>.subagents[].model` | subagent `<name>` |

**Correction (found in execution, 2026-11-21)**: this table first attributed
`:1040` to `definitions.<role>.subagents[].model`. That site actually builds a
`SubagentConfig` from the definition's **own** fields and judges those; the
nested `subagents[]` list is validated by `ValidateAgentSubagents` (`:1066`).
A probe proved the gap: `definitions.code-auditor: {system_prompt: audit, model: llm-deleted-row}`
is refused by the loader, so the four shapes this plan first enumerated would
have left that reference unprotected — the very defect being fixed. The
shipped guard therefore covers **five** shapes. It also mirrors the validator's
other half: a primary definition's own `model` field is never read
(`IsEffectivePrimaryAgent` skips it), so `ModelPinnedBy` skips it too — without
that, a stale `model` on a primary would make a row undeletable for a reference
nothing resolves (probe: loader accepts, guard named `subagent main`).

All five end in `validateSubagentModel` (`pkg/config/agents.go:461-476`):

```go
func validateSubagentModel(where, levelNeedsModel string, entry SubagentConfig, refResolves func(string) bool) error {
	model := strings.TrimSpace(entry.Model)
	if model == "" { ... return nil }
	if subagentModelShape(model) { return nil }      // legacy "provider/model"
	if refResolves != nil && refResolves(model) { return nil }
	return fmt.Errorf("%s: model %q must be provider/model", where, entry.Model)
}
```

A **row id** has no slash (`llm-7825d03d-…`), so once the row is gone nothing
resolves it and the loader **refuses the whole configuration**. Reproduced
with an overlay probe (no repo file touched), scratch home:

```
$ go test -overlay prose.json ./pkg/process -run TestProbeDeleteBricksComposedConfig
    before delete: composes fine; row=llm-7825d03d-aefa-448e-a53e-fa50997aca90
    after delete: err=compose config: agents.subagent_models.explore: model "llm-7825d03d-…" must be provider/model
```

and at the YAML level (`config.ParseRootYAML`), one line per kind:

```
subagent_models pin  err=agents.subagent_models.explore: model "llm-deleted-row" must be provider/model
plan_reviewer        err=agents.plan_reviewer: model "llm-deleted-row" must be provider/model
goal_evaluator       err=agents.goal_evaluator: model "llm-deleted-row" must be provider/model
custom subagent pin  err=agents.definitions.main.subagents[0]: model "llm-deleted-row" must be provider/model
```

**Tolerated** dangling references (do not block; the runtime already handles
them): `agents.definitions.<agent>.default_model` (`llm_models.go:112-118`,
skipped with a Warn, plan 315) and `memories.extract_model` /
`consolidation_model` (both parse clean; a memory ref that resolves to nothing
makes `resolveMemoryLLMProvider` return nil, so memory work simply stops until
re-picked — deliberately **not** claimed in the user-facing copy).

`SyncConfigFromDB` is where the failure lands (`config_db.go:825-848`):
`composeConfig` → `FinishLoadWith` (`load.go:86-96`) → `validateLoadedRoot`.
So the delete's own `adoptLLMModelWrite` fails **after** the row is gone, and
every later turn fails closed. Current web behavior therefore destroys data.

### 8. The web copy and the page that renders it

`frontend/src/locales/index.ts` (zh `:2443-2447`, en `:5000-5005`):

```ts
'models.deleteTitle': '删除这个模型？',
'models.deleteMessage': '删除后，仍用它作默认的主代理会回落；会话里的引用也会失效。',
'models.deleteRefusedTitle': '不能删除',
'models.deleteRefusedMessage': '它是这些主代理的默认模型，先把默认改成别的再删。',
'models.deleteRefusedOk': '知道了',
```
```ts
'models.deleteTitle': 'Delete this model?',
'models.deleteMessage':
  'Agents that still default to it fall back, and every reference from a conversation goes stale.',
'models.deleteRefusedTitle': 'Cannot delete',
'models.deleteRefusedMessage': 'It is these agents’ default model. Choose another default first.',
'models.deleteRefusedOk': 'Got it',
```

Rendered by `frontend/src/components/settings/ModelsTab.vue:77-97`:

```vue
<ConfirmDialog
  :open="removeRow !== null"
  :title="t('models.deleteTitle')"
  :message="t('models.deleteMessage')"
  :confirm-label="t('models.delete')"
  danger
  @cancel="removeRow = null"
  @confirm="confirmRemove"
/>

<!-- A default model cannot be deleted: the row names the agents that would
     lose their default, and the page says which instead of a bare refusal. -->
<ForebrainModal :open="refusedFor.length > 0" size="sm" :title="t('models.deleteRefusedTitle')" @close="refusedFor = []">
  <p class="text-[13px] text-[var(--forebrain-text-2)]">{{ t('models.deleteRefusedMessage') }}</p>
  <div class="mt-2 flex flex-wrap gap-1.5">
    <BadgeComponent v-for="agent in refusedFor" :key="agent" data-testid="models-refused-agent">{{ agent }}</BadgeComponent>
```

`confirmRemove` (`ModelsTab.vue:245-262`) reads the 409's `defaultFor` into
`refusedFor`. `frontend/src/lib/api.ts:2600` documents the same claim in a
comment ("a delete names the agents that still default to it").

**Test coverage today**: no test pins the copy (grep over `frontend/src` and
`frontend/e2e` finds nothing) — the e2e case
`frontend/e2e/settings-models.spec.ts:205-234` clicks through the confirm
dialog (`confirm-ok`) and asserts the refusal title `不能删除` plus the agent
badge, never the sentence. That is why the false copy survived.

**Not in scope**: the `providers.*` locale keys (`index.ts:177`, `:2754`) are
leftovers of the deleted per-agent provider list (plan 315) with no consumer
left in `frontend/src` — dead strings, reported here, deleted separately if
the owner wants.

### 9. Conventions and architecture constraints
- **TUI copy is English-only**, concise, no middle-dot connectors (owner
  2026-11-15/11-18). Comments explain *why*, in the surrounding voice.
- **Optional capability interfaces** are the house pattern for session
  powers: `configPanelSession` (`config_panel.go:89-95`) is asserted at the
  call site so test doubles without the power keep compiling. Follow it.
- **No new production files**: `pkg/tui` holds 33 of the 34-file cap and
  `pkg/gateway` 34 of 34 (`maxProductionFilesPerPackage = 34`,
  `pkg/architecture/cache_test.go:1532`). All new code goes into existing
  files. Test files must pair with a production file
  (`TestTestFilesCorrespondToProductionFiles`) and names carry ≤2 underscores.
- **Fan-out ratchet** (`fanOutBudgets`, `cache_test.go:237-240`): every import
  this plan adds is already present at package level — `pkg/tui` imports
  `process`/`config`/`state`/`llm`, `pkg/gateway` imports `process`/`config`,
  `pkg/process` imports `config`/`state`. Only `pkg/config/llm_models.go`
  gains a stdlib import (`sort`). No budget moves.
- Layering: `config` (L1) ← `process` (L4) ← `tui`/`gateway` (L5). The guard
  is a pure config projection and belongs in `config` beside
  `EffectiveDefaultLLMFor`; the orchestration belongs in `process`'s write
  API. No sibling imports, no new edges.

## Commands you will need

Run from the repo root (or the worktree root). CGO + PDFium are mandatory;
the Makefile sets `PKG_CONFIG_PATH` only inside make, so carry it yourself:

```bash
export CGO_ENABLED=1
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
```

Focused tests (scratch home per house rules; `-v` + `--- PASS:` counts —
`-run` alternation uses a bare `|`, and a pattern that matches nothing exits
0 with `[no tests to run]`, so always count the PASS lines):

```bash
FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/config -run 'TestModelDefaultAgents' -count=1 -v
FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/process -run 'TestDeleteLLMModel' -count=1 -v
FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/gateway -run 'TestLLMModelsAPI' -count=1 -v
FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/tui -run 'TestDeleteConfiguredModel|TestModelPicker|TestSlashPicker' -count=1 -v
```

Full gates (the whole suite needs `-timeout 30m`: `pkg/tui` alone runs
~200s and the 10m default package timeout false-reds under load):

```bash
FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/config ./pkg/process ./pkg/gateway ./pkg/tui ./pkg/architecture -count=1 -timeout 30m
gofmt -l cmd pkg third_party plugins        # must print nothing
go vet -tags fts5 ./...                     # exit 0, no output
```

Frontend and the browser gate for Step 6 (Node 22, pnpm 10 via corepack;
`frontend/node_modules` already exists in the main checkout, a fresh worktree
needs one `corepack pnpm install --frozen-lockfile` first):

```bash
cd frontend && corepack pnpm typecheck                 # exit 0
cd frontend && corepack pnpm test                      # vitest, all pass
cd .. && FOREBRAIN_E2E_SPEC=e2e/settings-models.spec.ts scripts/acceptance/web_e2e.sh   # "web e2e: PASS"
```

The e2e harness builds the binary and the UI itself and serves the fresh build
through `FOREBRAIN_STATIC_DIST`; it never writes `pkg/gateway/dist`, and its
logs/shots land under `$TMPDIR/forebrain-web-e2e`.

Expected: every listed package `ok`; the focused runs print one
`--- PASS: <Name>` per new test (counts given per step).

## Scope

**In scope** (the only files you should modify):

- `pkg/config/llm_models.go` — add `ModelDefaultAgents` + `ModelPinnedBy` (+ `sort` import)
- `pkg/config/llm_models_test.go` — their unit tests
- `pkg/process/llm_models.go` — add `DeleteLLMModel` + `LLMModelRefusal`
- `pkg/process/llm_models_test.go` — four tests (three delete + one reproduction)
- `pkg/gateway/models_api.go` — delegate the delete, switch the DTO call site, drop the private helper + `sort` import, add `pinned_by`
- `pkg/gateway/models_api_test.go` — **append** one test, `TestLLMModelsAPIDeleteRefusesAPinnedRow`; its existing tests stay byte-identical (see Step 3; a separate `*_test.go` would break `pkg/architecture`)
- `pkg/tui/chat_slash.go` — `modelRemoveSession` interface + `ChatSession.DeleteConfiguredModel`
- `pkg/tui/run.go` — `runSlashPicker` action branch + `confirmModelRemove`
- `pkg/tui/commands_test.go` — picker-flow tests + one session double
- `pkg/tui/chat_session_test.go` — real-store session test
- `frontend/src/locales/index.ts` — the four `models.delete*` strings, zh + en
- `frontend/src/components/settings/ModelsTab.vue` — badges from defaults ∪ pins
- `frontend/src/lib/api.ts` — the `deleteLlmModel` doc comment
- `frontend/e2e/settings-models.spec.ts` — pin the confirm copy + a pinned-row refusal
- `plans/README.md` — this plan's status row (executor or reviewer)

**Out of scope** (do NOT touch, even though they look related):

- `pkg/gateway/models_api_test.go` — the existing tests are the **regression
  pin** for the shared semantics and must stay byte-identical (append-only
  file, Step 3). If `TestLLMModelsAPIDeleteRefusesTheDefaultModel` goes red,
  your refactor changed behavior — STOP.
- `pkg/turn/**` — the engine (`execModel`, `Choose`, `ModelSlashHandler`) is
  untouched; delete is a settings write, not a conversational choice.
- `pkg/tui/overlays.go`, `channels.go`, `config_panel.go`,
  `config_panel_schema.go`, `tabbed_picker.go` — the action/confirmation
  machinery is reused exactly as it stands. Needing a change here is a STOP.
- `pkg/tui/setup.go` (`/connect`), `pkg/state/config_store.go`,
  `pkg/run/config.go`, `assets/models.json`, the frozen prototypes
  (`plans/config-tui-prototype.html`, `plans/311-connect-prototype.html`).
- `pkg/gateway/dist/**` — gitignored build output (`git ls-files` shows only
  `.gitkeep`); `make ui` regenerates it. Never stage it.
- The `providers.*` locale keys — dead strings from the deleted provider list
  (plan 315), no consumer left; report, do not delete here.

## Git workflow

- Worktree: `.worktrees/345-tui-delete-configured-model` in the repo's main
  worktree, branch `345-tui-delete-configured-model` from the current dev tip
  (`c0d4ee16` at planning; rebase if dev moved, then re-run the drift check).
  **Record `BASE=$(git rev-parse HEAD)` right after creating the worktree,
  before the first edit** — every scope check below compares against `$BASE`,
  so a rebase onto a moved dev never false-reds the file-set gate.
- **Fresh worktree wiring** (these are gitignored build deps a new checkout
  lacks; `go:embed` and cgo fail without them):

```bash
MAIN="$(git worktree list | head -1 | awk '{print $1}')"
mkdir -p third_party
cp -R "$MAIN/third_party/pdfium" third_party/
cp -R "$MAIN/third_party/libtokenizers" third_party/
mkdir -p pkg/gateway
cp -R "$MAIN/pkg/gateway/dist" pkg/gateway/
```

- Commit per step, conventional style matching `git log` (e.g.
  `feat(tui): delete a configured model from the /model picker (plan 345)`,
  `refactor(gateway): share the model delete semantics through process`).
- Do **NOT** push, merge, or touch the user's branch — the owner decides
  landing. After an owner-approved merge, remove the worktree and branch
  (house rule).

## Steps

### Step 0: Reproduce the destructive defect (before any edit)

The defect this plan's guard fixes must be seen failing first. In the worktree
root, create two scratch files **outside** the repo tree and run the probe
through `-overlay` (nothing is written into the repo; `git status` stays
clean):

```bash
mkdir -p /tmp/fb345 && cat > /tmp/fb345/probe_test.go <<'EOF'
package process

import (
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

func TestProbeDeleteBricksComposedConfig(t *testing.T) {
	env, ctx := composeEnvForTest(t, "approval_policy: \"on-request\"\n")
	row, err := (&state.LLMModelStore{DB: env.SQL}).Create(ctx, state.LLMModel{
		Provider: "openai", Model: "gpt-x", BaseURL: "https://api.openai.com", APIPath: "/v1/chat/completions",
	})
	if err != nil {
		t.Fatal(err)
	}
	storeSectionForTest(t, env, ctx, "agents",
		`{"definitions":{"main":{"primary":true}},"subagent_models":{"explore":{"model":"`+row.ID+`"}}}`)
	if _, err := composeConfig(ctx, env); err != nil {
		t.Fatalf("before delete the config must compose: %v", err)
	}
	if err := (&state.LLMModelStore{DB: env.SQL}).Delete(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	_, err = composeConfig(ctx, env)
	t.Logf("after delete: err=%v", err)
	if err == nil {
		t.Fatal("EXPECTED the composed config to be refused after deleting a pinned row")
	}
}
EOF
cat > /tmp/fb345/overlay.json <<'EOF'
{"Replace": {"$PWD/pkg/process/zz_probe345_test.go": "/tmp/fb345/probe_test.go"}}
EOF
CGO_ENABLED=1 go test -overlay /tmp/fb345/overlay.json -tags fts5 ./pkg/process \
  -run TestProbeDeleteBricksComposedConfig -count=1 -v 2>&1 | grep -v '^ld: warning'
```

**Expected before the fix** (this is the evidence, not a failure of your work):

```
--- PASS: TestProbeDeleteBricksComposedConfig
    zz_probe345_test.go:…: after delete: err=compose config: agents.subagent_models.explore: model "llm-…" must be provider/model
```

If the probe instead reports `composeConfig` succeeding after the delete, the
premise is gone — **STOP** and report. Keep `/tmp/fb345` for Step 2's
`TestDeleteLLMModelRefusesAPinnedRow` (same shape, asserted as a test) and
delete it at the end.

### Step 1: Move the default-model guard into `pkg/config`, and add the pin guard

In `pkg/config/llm_models.go`, beside `EffectiveDefaultLLMFor`, add the
exported projection — the gateway helper's body **verbatim**, with `cfg`
renamed `root` and the doc comment adjusted (keep the subagent sentence):

```go
// ModelDefaultAgents names the agents whose default_model is this row, by
// display name (the definition key when no display name is set), sorted. The
// reference is an explicit choice, so a row one of them runs on cannot be
// deleted behind its back. A subagent definition's own Model is not a default
// and this helper does not look at it: nothing falls back to that row when it
// goes, the subagent simply follows the conversation's model.
func ModelDefaultAgents(root *Root, id string) []string {
	if root == nil || strings.TrimSpace(id) == "" {
		return nil
	}
	names := make([]string, 0, 1)
	for key, def := range root.Agents.Definitions {
		if strings.TrimSpace(def.DefaultModel) != id {
			continue
		}
		if name := strings.TrimSpace(def.DisplayName); name != "" {
			names = append(names, name)
			continue
		}
		names = append(names, key)
	}
	sort.Strings(names)
	return names
}
```

Add `"sort"` to the file's imports (currently `log/slog`, `strings`, `sync`).
The gateway helper stays for now (Step 3 removes it) — the tree compiles
between steps.

**Second function, same file** — the pin guard, whose owner labels are the
five strict sites of Current state §7 in five shapes (a subagent role's own
`model`, guarded only when the definition is not an effective primary; custom
subagent entries appear under primary agents and under roles, and one loop
over `Definitions` covers both):

```go
// ModelPinnedBy names the settings that reference this row where the loader
// requires the reference to resolve — a built-in subagent type's install-wide
// row, the plan reviewer, the goal check, or a subagent definition's own model.
// Deleting such a row would leave the composed configuration invalid (the
// loader refuses a dangling row id: `model %q must be provider/model`), so the
// row cannot be deleted behind those settings. A conversation's own selection
// and an agent's default_model are not listed: the runtime refuses that turn
// with a re-choose diagnosis, or skips the stale default (plans 260/261/315).
func ModelPinnedBy(root *Root, id string) []string {
	if root == nil || strings.TrimSpace(id) == "" {
		return nil
	}
	id = strings.TrimSpace(id)
	var owners []string
	add := func(kind, name string) {
		if name = strings.TrimSpace(name); name == "" {
			owners = append(owners, kind)
			return
		}
		owners = append(owners, kind+" "+name)
	}
	for name, entry := range root.Agents.SubagentModels {
		if strings.TrimSpace(entry.Model) == id {
			add("subagent", name)
		}
	}
	if strings.TrimSpace(root.Agents.PlanReviewer.Model) == id {
		add("plan review", "")
	}
	if strings.TrimSpace(root.Agents.GoalEvaluator.Model) == id {
		add("goal check", "")
	}
	for _, def := range root.Agents.Definitions {
		for _, entry := range def.Subagents {
			if strings.TrimSpace(entry.Model) == id {
				add("subagent", entry.Name)
			}
		}
	}
	sort.Strings(owners)
	return owners
}
```

Field names are confirmed at `pkg/config/agents.go:15-35` (`AgentsSection`:
`Definitions map[string]AgentDefinition`, `GoalEvaluator`, `PlanReviewer`,
`SubagentModels map[string]SubagentConfig`) and `:183-201`
(`SubagentConfig{Name, Model, …}`). Live rows carry no name, so the two
global kinds use their own labels.

Tests (same file, beside the existing table tests):

- `TestModelPinnedByNamesTheSettingsThatMustResolve` — a root carrying all four
  shapes pointing at `row-1` (plus one pin on `row-2` and one agent
  `default_model` on `row-1`) → sorted
  `["goal check", "plan review", "subagent explore", "subagent repo-auditor"]`;
  the `default_model` contributes nothing.
- `TestDanglingReferencesTheLoaderRefusesAreExactlyThePinnedOnes` — the
  characterization test that keeps the two lists honest. For each of the four
  shapes, `ModelPinnedBy` names it **and** `ParseRootYAML` refuses the same
  document with the row removed (assert `err != nil` and that the message
  contains `must be provider/model`); for `default_model` and
  `memories.extract_model`, `ModelPinnedBy` is empty **and**
  `ParseRootYAML` accepts the dangling id. Build the documents as YAML strings
  (the probe in Current state §7 is the pattern).

**Verify**:

Test in `pkg/config/llm_models_test.go` (match the file's existing table-test
voice), `TestModelDefaultAgentsNamesTheAgentsThatRunOnTheRow`:

- two agents default to `row-1` (one with `DisplayName: "Main agent"`, one
  without) → `[]string{"Main agent", "<key>"}` sorted;
- an agent defaulting to `row-2` is not named;
- a **subagent** definition with `Model: "row-1"` (not `DefaultModel`) is not named;
- nil root and empty/whitespace id → nil.

Build the root as `&Root{Agents: AgentsSection{Definitions: map[string]AgentDefinition{...}}}`.

**Verify**:

```bash
FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/config -run 'TestModelPinnedBy|TestDanglingReferences|TestModelDefaultAgents' -count=1 -v
```

→ exactly 3 `--- PASS:` lines —
`TestModelDefaultAgentsNamesTheAgentsThatRunOnTheRow`,
`TestModelPinnedByNamesTheSettingsThatMustResolve`,
`TestDanglingReferencesTheLoaderRefusesAreExactlyThePinnedOnes` — package `ok`.

### Step 2: Add the shared delete orchestration to `pkg/process`

In `pkg/process/llm_models.go`, beside `SetAgentDefaultModel`:

```go
// LLMModelRefusal reports a delete the guards refused: Defaults lists the
// agents whose default_model is the row, Pins the settings whose reference the
// loader requires to resolve — both by a reader-facing label, sorted. The
// web models page answers it as a 409 with both lists; the terminal says the
// same sentence.
type LLMModelRefusal struct {
	Defaults []string
	Pins     []string
}

func (e *LLMModelRefusal) Error() string {
	if len(e.Defaults) > 0 {
		return "it is the default model of " + strings.Join(e.Defaults, ", ") + "; choose another default first"
	}
	return "it is still used by " + strings.Join(e.Pins, ", ") + "; point those settings elsewhere first"
}

// DeleteLLMModel removes one configured row — the one delete semantics both
// surfaces share (plan 345). Two kinds of reference refuse it, because neither
// may be rewritten behind the user's back:
//
//   - a row some agent names as its default_model (the reference is an explicit
//     choice; deleting through it would move that agent to another model);
//   - a row a subagent type, the plan reviewer, the goal check or a subagent
//     definition pins by id. The loader requires those references to resolve,
//     so deleting the row would leave the composed configuration invalid and
//     fail every later turn (see plan 345's reproduction).
//
// Sessions, subagents' conversations and memory settings that merely named the
// row are not rewritten — the runtime refuses that turn with a re-choose
// diagnosis or skips the stale value (plans 260/261/315). A missing row reports
// state.ErrLLMModelNotFound. Adoption is the caller's: the surface that wrote
// re-reads through Environment.SyncConfigFromDB, the turn boundary's own path.
func DeleteLLMModel(ctx context.Context, db *sql.DB, cfg *appcfg.Root, id string) error {
	if db == nil {
		return fmt.Errorf("no state database")
	}
	store := &state.LLMModelStore{DB: db}
	stored, err := store.Get(ctx, id)
	if err != nil {
		return err
	}
	defaults := appcfg.ModelDefaultAgents(cfg, stored.ID)
	pins := appcfg.ModelPinnedBy(cfg, stored.ID)
	if len(defaults) > 0 || len(pins) > 0 {
		return &LLMModelRefusal{Defaults: defaults, Pins: pins}
	}
	return store.Delete(ctx, stored.ID)
}
```

No new imports (all already in the file). Tests in
`pkg/process/llm_models_test.go`, following
`TestSetAgentDefaultModelCreatesMainAndRefusesAnUnknownAgent` (:502) and the
`migrateEnvForTest` helper at the file's head (it installs the section
validator and cleans it up):

1. `TestDeleteLLMModelRemovesARowNothingReferences` —
   `env, ctx := migrateEnvForTest(t, "", "")`; create a row via
   `(&state.LLMModelStore{DB: env.SQL}).Create(ctx, state.LLMModel{Provider: "openai", Model: "gpt-x", BaseURL: "https://api.openai.com", APIPath: "/v1/chat/completions"})`;
   `cfg, err := env.ComposedPersistedConfig(ctx)`;
   `DeleteLLMModel(ctx, env.SQL, &cfg, row.ID)` → nil; `store.Get` afterwards →
   `state.ErrLLMModelNotFound`.
2. `TestDeleteLLMModelRefusesTheDefaultModel` — same fixture; create the row;
   `SetAgentDefaultModel(ctx, env.SQL, "main", row.ID)`; re-read cfg;
   `DeleteLLMModel` → `errors.As(err, &refusal)` with
   `refusal.Defaults == []string{"main"}` and `refusal.Pins` empty; **the row
   survives** (`store.Get` → nil error).
3. `TestDeleteLLMModelRefusesAPinnedRow` — the Step 0 reproduction as a test:
   create the row, `env.UpdateRootSection(ctx, "agents", func(cfg *appcfg.Root) error { … cfg.Agents.SubagentModels = map[string]appcfg.SubagentConfig{"explore": {Model: row.ID}}; return nil })`,
   re-read cfg through `ComposedPersistedConfig`, then `DeleteLLMModel` →
   refusal with `Pins == []string{"subagent explore"}`; the row survives; and
   `composeConfig(ctx, env)` still succeeds afterwards (the whole point).
4. `TestDeleteLLMModelReportsAMissingRow` — `DeleteLLMModel(ctx, env.SQL, &cfg, "llm-nope")`
   → `errors.Is(err, state.ErrLLMModelNotFound)`.

Add `"errors"` to the test file's imports if absent.

**Verify**:

```bash
FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/process -run 'TestDeleteLLMModel' -count=1 -v
```

→ 4 `--- PASS:` lines, package `ok`.

### Step 3: Make the gateway delegate, adding `pinned_by` (the default case stays byte-identical)

In `pkg/gateway/models_api.go`:

1. Rewrite `deleteLLMModel` to call the shared core; the 409 body stays
   **word-for-word** what it is today:

```go
// deleteLLMModel removes one row through the shared delete semantics
// (process.DeleteLLMModel, plan 345). Two kinds of reference refuse it: a row
// an agent names as its default_model — the reference is an explicit choice,
// and deleting through it would move that agent to another model without
// saying so — and a row a subagent type, the plan reviewer, the goal check or a
// subagent definition pins by id, which the loader requires to resolve.
// Sessions and memory settings that named the row are not rewritten; the
// runtime refuses that turn with a re-choose diagnosis or skips the stale value
// (plans 260/261/315). The refusal carries both lists so the page can name every
// owner at once.
func (s *Server) deleteLLMModel(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.llmModelScope(w, r)
	if !ok {
		return
	}
	err := process.DeleteLLMModel(r.Context(), s.Env.SQL, scope.cfg, pathParam(r, "id"))
	var refusal *process.LLMModelRefusal
	if errors.As(err, &refusal) {
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error":       llmRefusalSentence(r, refusal),
			"default_for": refusal.Defaults,
			"pinned_by":   refusal.Pins,
		})
		return
	}
	if err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	if err := s.adoptLLMModelWrite(r.Context()); err != nil {
		s.failLLMModelWrite(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// llmRefusalSentence words a refusal in the request's language: the default
// case names the agents (the sentence the page has always shown), the pin case
// names the settings, and a row that is both names both.
func llmRefusalSentence(r *http.Request, refusal *process.LLMModelRefusal) string {
	switch {
	case len(refusal.Defaults) > 0 && len(refusal.Pins) > 0:
		return llmText(r,
			"它是「"+strings.Join(refusal.Defaults, "、")+"」的默认模型，也被「"+strings.Join(refusal.Pins, "、")+"」引用；先把这些引用改成别的",
			"it is the default model of "+strings.Join(refusal.Defaults, ", ")+" and is referenced by "+strings.Join(refusal.Pins, ", ")+"; point those settings elsewhere first")
	case len(refusal.Pins) > 0:
		return llmText(r,
			"它仍被「"+strings.Join(refusal.Pins, "、")+"」引用；先把这些引用改成别的",
			"it is still referenced by "+strings.Join(refusal.Pins, ", ")+"; point those settings elsewhere first")
	default:
		return llmText(r,
			"它是「"+strings.Join(refusal.Defaults, "、")+"」的默认模型，请先把默认改成别的",
			"it is the default model of "+strings.Join(refusal.Defaults, ", ")+"; choose another default first")
	}
}
```

The default-only sentence stays **word-for-word** what it is today, and
`default_for` keeps carrying the same list, so the existing pin
(`TestLLMModelsAPIDeleteRefusesTheDefaultModel`, out of scope, unchanged) and
the web page's badge code keep working while `pinned_by` is added.

2. Switch the DTO call site (`:501`) to
```go
		DefaultFor:      config.ModelDefaultAgents(scope.cfg, row.ID),
		PinnedBy:        config.ModelPinnedBy(scope.cfg, row.ID),
```
   and add `PinnedBy []string `json:"pinned_by,omitempty"`` to `llmModelRow`
   beside `DefaultFor` (read `type llmModelRow struct` first — grep
   `DefaultFor` to find the field).
3. Delete the private `llmModelDefaultFor` and the `"sort"` import (its only
   use was the moved helper — re-grep `sort\.` in the file to confirm before
   removing). `process`, `config`, `errors`, `strings` are already imported.

**Verify** (the existing pin must pass **unchanged** — do not edit it):

```bash
FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/gateway -run 'TestLLMModelsAPI' -count=1 -v
```

→ every `TestLLMModelsAPI*` PASSes, including
`TestLLMModelsAPIDeleteRefusesTheDefaultModel` (409 + `default_for:["main"]`,
204 for the other row, 404 on the second delete). Any red here means the
semantics moved wrong — STOP.

**New test, existing file** — `pkg/gateway/models_api_test.go`, appended (the
architecture gate forbids a new `models_pins_api_test.go`: every test file must
pair with a `models_pins_api.go`, and its 4 underscores break the ≤2 rule).
Write `TestLLMModelsAPIDeleteRefusesAPinnedRow` at the end of the file: build
the fixture the same way `modelsPageServer(t)` does, POST one row through
`createModel(t, do, …)`, pin it by writing the `agents` section through the
same path the page uses (read `modelsPageServer`/`rulesServer` and
`settings-goal-check-model`'s server-side path to pick the shortest existing
route — `Env.UpdateRootSection` is fine), then `DELETE /api/llm-models/<id>` →
**409**, body `error` mentions the pin label, `pinned_by` is
`["subagent explore"]`, `default_for` empty, and `GET /api/llm-models` still
lists the row. This is the regression pin for the destructive defect: before
Step 2's guard it would answer 500 and the row would be gone.

**Do not modify a single line of the existing tests in that file** — appending
a function is the only change allowed there (the done criterion checks the
diff has no deletions).

### Step 4: The TUI session capability

In `pkg/tui/chat_slash.go`, right after the `SelectModel` /
`restoreRunnerSelection` / `selectModelTimeout` block (~:712), add:

```go
// modelRemoveSession is what /model's Remove action needs from a session —
// an optional capability, the way configPanelSession is, so a session double
// with no model table behind it keeps compiling and the picker keeps its
// select-only behavior.
type modelRemoveSession interface {
	// DeleteConfiguredModel removes one row of the install-wide model table
	// and adopts the change, returning the sentence the surface says. A
	// refusal (the row is some agent's default) and an already-gone row are
	// replies, not errors; error is reserved for a write that failed.
	DeleteConfiguredModel(ctx context.Context, modelID string) (string, error)
}

// DeleteConfiguredModel removes one configured row through the shared delete
// semantics (process.DeleteLLMModel, plan 345) and adopts the result the way
// /connect does (SyncConfig, the turn boundary's own path) — without adoption
// the re-opened /model list would read the live snapshot and still show the
// deleted row. The stored selection of a conversation that ran on the row is
// left as written: its next message is refused at turn entry with the
// existing re-choose diagnosis, exactly as after a web delete.
func (s *ChatSession) DeleteConfiguredModel(ctx context.Context, modelID string) (string, error) {
	if s == nil || s.Env == nil || s.Env.SQL == nil {
		return "", fmt.Errorf("the model table is unavailable: no runtime environment")
	}
	ctx, cancel := context.WithTimeout(ctx, selectModelTimeout)
	defer cancel()
	cfg, err := s.Env.ComposedPersistedConfig(ctx)
	if err != nil {
		return "", err
	}
	label := strings.TrimSpace(modelID)
	if entry, ok := appcfg.ResolveModelRef(&cfg, "main", label); ok {
		label = llm.FormatProviderModelDisplay(entry.Provider, entry.Model)
	}
	err = process.DeleteLLMModel(ctx, s.Env.SQL, &cfg, modelID)
	var refusal *process.LLMModelRefusal
	if errors.As(err, &refusal) {
		switch {
		case len(refusal.Defaults) > 0 && len(refusal.Pins) > 0:
			return "It is the default model of " + strings.Join(refusal.Defaults, ", ") +
				" and is referenced by " + strings.Join(refusal.Pins, ", ") + "; point those settings elsewhere first.", nil
		case len(refusal.Pins) > 0:
			return "It is still referenced by " + strings.Join(refusal.Pins, ", ") + "; point those settings elsewhere first.", nil
		default:
			return "It is the default model of " + strings.Join(refusal.Defaults, ", ") + "; choose another default first.", nil
		}
	}
	if errors.Is(err, state.ErrLLMModelNotFound) {
		return "This model is no longer configured.", nil
	}
	if err != nil {
		return "", err
	}
	if err := s.Env.SyncConfigFromDB(ctx); err != nil {
		return "", err
	}
	return "Removed " + label + ".", nil
}
```

Every import (`context`, `errors`, `fmt`, `strings`, `appcfg`, `llm`,
`process`, `state`) is already in `chat_slash.go`. `selectModelTimeout`
(30s, `chat_slash.go:710`) bounds the DB write + sync; the sync never waits
on a network (`config_db.go:216`).

Test in `pkg/tui/chat_session_test.go`,
`TestDeleteConfiguredModelRefusesTheDefaultAndRemovesTheRest`, on the
**real store** fixture proven at `config_panel_test.go:3985-4010`:

```go
ctx := context.Background()
home := t.TempDir()
cfgPath := filepath.Join(home, "forebrain.yaml")
// write a minimal yaml (os.WriteFile, e.g. "gateway:\n  http_addr: 127.0.0.1:9\n"),
// os.MkdirAll(filepath.Join(home, "state"), 0o755)
db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state", "forebrain.state.sqlite"))
// defer db.Close()
models := &state.LLMModelStore{DB: db}
kept, err := models.Create(ctx, state.LLMModel{Provider: "openai", Model: "gpt-main", BaseURL: "https://api.openai.com", APIPath: "/v1/chat/completions"})
other, err := models.Create(ctx, state.LLMModel{Provider: "deepseek", Model: "r1", BaseURL: "https://api.deepseek.com", APIPath: "/v1/chat/completions"})
process.InstallConfigSectionValidator(cfgPath, db)
t.Cleanup(func() { state.SetSectionValidator(nil) })
env := &process.Environment{Root: home, SQL: db}
env.ConfigPath = cfgPath
session := &ChatSession{Env: env}
```

Seed the default through the panel's own write path (exemplar
`config_panel_test.go:4053`):

```go
if err := session.UpdateConfigSection(ctx, "agents",
	json.RawMessage(`{"definitions":{"main":{"primary":true,"default_model":"`+kept.ID+`"}}}`)); err != nil {
	t.Fatalf("the seed must be a definition the loader accepts: %v", err)
}
```

Then assert, in order:

1. `session.DeleteConfiguredModel(ctx, kept.ID)` → err nil, reply
   `"It is the default model of main; choose another default first."`;
   `models.Get(ctx, kept.ID)` still succeeds (the refused row survives).
2. Pin `other` through the panel's own write path and delete it →
   reply `"It is still referenced by subagent explore; point those settings elsewhere first."`,
   `models.Get(ctx, other.ID)` still succeeds, and
   `env.ComposedPersistedConfig` + a compose still validate (the row was never
   removed, so the config never broke). Clear the pin again with
   `UpdateConfigSection(ctx, "agents", json.RawMessage(`{"definitions":{"main":{"primary":true,"default_model":"`+kept.ID+`"}}}`))`.
3. Create a third row nothing references, then
   `session.DeleteConfiguredModel(ctx, third.ID)` → err nil, reply
   `"Removed deepseek/r1."` (the display label — check
   `llm.FormatProviderModelDisplay("deepseek", "r1")`'s actual output and
   assert against that function's result, not a hand-guessed string);
   `models.Get(ctx, third.ID)` → `state.ErrLLMModelNotFound`.
4. Repeat the same call → reply `"This model is no longer configured."`, err nil.

(`SyncConfigFromDB` is a no-op on this hand-built env — §3 of Current state;
do not assert adoption here.) If the loader **refuses the seed**, that is a
STOP condition, not something to work around by bypassing the validator.

**Verify**:

```bash
FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/tui -run 'TestDeleteConfiguredModel' -count=1 -v
```

→ 1 `--- PASS:`, package `ok`.

### Step 5: Arm Remove on the /model picker

In `pkg/tui/run.go`, restructure `runSlashPicker` into a loop. The
items/current/label build block and the tail (`ChooseSurfaceSlash` →
`refreshSessionFooter` → `applySlashOutcome`) stay **verbatim, comments
included**; what changes is (a) the whole body sits in `for { ... }`, (b) the
plain `SelectRich` call becomes the fallback of an action-aware dispatch used
only for `Command == "model"` when both capabilities are present, (c) a
Remove action confirms, deletes, says the reply, refreshes the footer and
re-opens `/model` on the table as it now stands:

```go
		idx, ok := -1, false
		picked := false
		if picker.Command == "model" {
			remover, canRemove := cmds.session.(modelRemoveSession)
			actions, hasActions := cmds.selector.(ActionRichSelector)
			if canRemove && hasActions {
				res, err := actions.SelectRichActions(label, items, current, ActionOptions{
					FooterActions: []FooterAction{{
						Label: configRemoveActionLabel,
						// The list shows a filter from 8 rows on, where a bare
						// 'd' types into it; Ctrl+D fires in both modes, so the
						// footer names the key that always works (plan 345).
						Key:  "Ctrl+D",
						Kind: FooterActionRemove,
						// Every row is a configured model — all removable.
						// RowActive must be set: fireAction reports the cursor's
						// row only for an action that serves rows.
						RowActive: func(i int) bool { return i >= 0 && i < len(items) },
					}},
				})
				if err != nil {
					renderer.PrintError(err)
					return true, false, ComposerSubmission{}, false
				}
				if res.Action == FooterActionRemove {
					if res.Row < 0 || res.Row >= len(picker.Items) {
						continue
					}
					if !confirmModelRemove(cmds.selector, renderer, items[res.Row]) {
						continue
					}
					reply, err := remover.DeleteConfiguredModel(ctx, picker.Items[res.Row].Value)
					if err != nil {
						renderer.PrintError(err)
						return true, false, ComposerSubmission{}, false
					}
					renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "model", Content: reply, Final: true})
					refreshSessionFooter(renderer, cmds.session, state.sessionID)
					// Re-open the list on the table as it now stands; an empty
					// table answers as the engine's own reply and closes.
					outcome, reran := cmds.session.ExecuteSurfaceSlash(ctx, state.sessionID, "/model")
					if reran && outcome.Picker != nil {
						picker = outcome.Picker
						continue
					}
					if text := strings.TrimSpace(outcome.Reply); text != "" {
						renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "model", Content: text, Final: true})
					}
					return true, false, ComposerSubmission{}, false
				}
				if !res.OK || res.Row < 0 || res.Row >= len(picker.Items) {
					return true, false, ComposerSubmission{}, false
				}
				idx, ok, picked = res.Row, true, true
			}
		}
		if !picked {
			var err error
			idx, ok, err = cmds.selector.SelectRich(label, items, current)
			if err != nil {
				renderer.PrintError(err)
				return true, false, ComposerSubmission{}, false
			}
			if !ok || idx < 0 || idx >= len(picker.Items) {
				return true, false, ComposerSubmission{}, false
			}
		}
```

The confirmation helper, beside `runSlashPicker` in `run.go` — the
`confirmRemoveRow` shape with the model row's endpoint as the preview (the
picker item carries no key material; never print one):

```go
// confirmModelRemove asks before a configured model is deleted (plan 345),
// the /config removal confirmation's shape (plan 326 D5): the heading names
// the row and where its requests go, Keep item is preselected so Enter alone
// changes nothing, and Esc keeps the row too. Remove item is the danger row.
func confirmModelRemove(sel Selector, renderer *Renderer, item SelectItem) bool {
	items := []SelectItem{
		{Label: configRemoveConfirmLabel, Danger: true},
		{Label: configKeepConfirmLabel},
	}
	idx, ok, err := selectRichPage(sel, "Remove "+item.Label+"?\n"+strings.TrimSpace(item.Description), items, 1)
	if err != nil {
		renderer.PrintError(err)
		return false
	}
	return ok && idx == 0
}
```

Update `runSlashPicker`'s doc comment to name the Remove affordance (one
sentence, plan 345). Behavior guarantees:

- Every **other** picker (`model-effort`, `agent`, `resume`, …) and every
  session/selector without the two capabilities takes the byte-identical
  plain path — the existing tests pin this and must pass unchanged.
- A follow-up effort picker after a model **selection** still flows through
  `applySlashOutcome` → a fresh `runSlashPicker` call (no double loop).
- Keep/Esc on the confirmation re-opens the same list; a landed delete
  re-opens the refreshed list; the last row's delete says the engine's
  `"This agent has no models configured."` and closes.

Tests in `pkg/tui/commands_test.go`. Reuse `actionPanelSelector`
(`config_panel_test.go:90-160`, same package — it embeds `*scriptedSelector`,
plays `actionSteps []actionStep` for `SelectRichActions` and `richSteps
[]richStep` for the confirmation's `SelectRich`, and records `defaults` so a
test can assert the preselected row). Add one session double:

```go
// modelRemoveTestSession carries the /model Remove capability over the
// fakeSession base, the way configPanelTestSession carries /config's.
type modelRemoveTestSession struct {
	*fakeSession
	removes      []string
	reply        string
	err          error
	nextPicker   *turn.Picker // answered once by ExecuteSurfaceSlash("/model")
	nextReply    string
	executeCalls int
}

func (s *modelRemoveTestSession) DeleteConfiguredModel(_ context.Context, id string) (string, error) {
	s.removes = append(s.removes, id)
	return s.reply, s.err
}

func (s *modelRemoveTestSession) ExecuteSurfaceSlash(ctx context.Context, sid, line string) (SlashOutcome, bool) {
	s.executeCalls++
	if line == "/model" {
		if p := s.nextPicker; p != nil {
			s.nextPicker = nil
			return SlashOutcome{Handled: true, Picker: p}, true
		}
		return SlashOutcome{Handled: true, Reply: s.nextReply}, true
	}
	return s.fakeSession.ExecuteSurfaceSlash(ctx, sid, line)
}
```

Drive `runSlashPicker` directly, following
`TestSlashPickerModelRowStillReachesTheEngine` (`commands_test.go:589-616`)
for the fixture shape (`NewRenderer`, `newCommandController`,
`&streamState{sessionID: "s1"}`, a two-row `turn.Picker{Command: "model"}`
with ids `llm-1`/`llm-2` and endpoint descriptions). Cases:

1. `TestModelPickerRemoveKeepChangesNothing` — `actionSteps: [{action:
   FooterActionRemove, row: 0, ok: true}]`, confirmation `richSteps:
   [{label: "Keep item", ok: true}]`. Assert: `removes` empty, no
   `ChooseSurfaceSlash` choice (`fakeSession.choices` empty), the
   confirmation opened on default idx **1** (recorded in
   `actionPanelSelector.defaults`) with items `[{Remove item, Danger:true},
   {Keep item}]`, and the list re-opened (a second `SelectRichActions` call,
   which exhausts the script and closes).
2. `TestModelPickerRemoveDeletesAndReopensTheList` — confirmation
   `{label: "Remove item", ok: true}`; double `reply: "Removed deepseek/r1."`,
   `nextPicker`: a one-row picker (`llm-2`). Assert: `removes == ["llm-1"]`,
   a `FrameSystem` block with the reply, `executeCalls == 1`, the second
   `SelectRichActions` shows exactly the refreshed row, no model choice was
   applied. Also assert the armed action's `Key == "Ctrl+D"` and `RowActive != nil`
   from the recorded `opts`.
3. `TestModelPickerRemoveRefusalKeepsTheList` — double `reply: "It is the
   default model of main; choose another default first."`, `nextPicker` set.
   Assert: the refusal text is said, the list re-opens, `removes == ["llm-1"]`,
   no choice applied.
4. `TestModelPickerRemoveErrorExits` — double `err: errors.New("boom")`.
   Assert: `executeCalls == 0`, no choice, the picker closed (error path).
5. `TestModelPickerRemoveLastRowSaysTheListIsEmpty` — one-row picker,
   confirmation Remove, double `nextPicker: nil`, `nextReply: "This agent has
   no models configured."`. Assert: both frames said, picker closed.
6. `TestModelPickerEffortListArmsNoRemove` — `picker.Command = "model-effort"`,
   the same double + `actionPanelSelector` with a `richSteps` pick. Assert:
   `SelectRichActions` was **never** called (`actionPanelSelector.labels`
   empty), the plain path applied the choice.

**Verify**:

```bash
FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/tui -run 'TestModelPicker|TestSlashPicker|TestActiveRunSlashNativePicker' -count=1 -v
```

→ 6 new `--- PASS:` lines **plus** every existing `TestSlashPicker*` and
`TestActiveRunSlashNativePickerDoesNotQueueTurn` PASS unchanged.

### Step 6: Fix the web copy and let the page name pins too

This is the owner's requirement for this cycle: the confirm dialog must stop
claiming deleted defaults "fall back", and the refusal must speak about every
owner (defaults **and** pins). Three source files plus the e2e spec.

1. `frontend/src/locales/index.ts` — replace the four strings (zh block at
   `:2443-2447`, en at `:5000-5005`). Keep the same keys and order; the copy
   says exactly what the code does and nothing more:

```ts
// zh
'models.deleteTitle': '删除这个模型？',
'models.deleteMessage':
  '删除后，用过它的会话下一条消息会被拒绝，需要用 /model 重新选。被主代理默认、子代理、计划评审或目标评估引用的模型不能删除，请先改掉那些引用。',
'models.deleteRefusedTitle': '不能删除',
'models.deleteRefusedMessage': '它仍被下列设置引用，先把这些引用改成别的再删。',
'models.deleteRefusedOk': '知道了',
```
```ts
// en
'models.deleteTitle': 'Delete this model?',
'models.deleteMessage':
  'A conversation that used it must re-choose with /model, or its next message is refused. A model an agent defaults to, or one a subagent, plan review or goal check references, cannot be deleted — point those settings elsewhere first.',
'models.deleteRefusedTitle': 'Cannot delete',
'models.deleteRefusedMessage': 'These settings still reference it. Point them elsewhere first.',
'models.deleteRefusedOk': 'Got it',
```

Two things to keep true: (a) the sentence names the **turn refusal**, which is
what the runtime really does (`pkg/run/config.go:2371-2381`); (b) it does not
mention memory settings, which neither fall back nor block (see Current state
§7). No middle-dot separators anywhere.

2. `frontend/src/components/settings/ModelsTab.vue` — `refusedFor` becomes the
   union of both lists, so the badges name pins as well:

```ts
    if (cause instanceof GatewayHttpError && cause.status === 409) {
      const defaults = cause.payload?.defaultFor
      const pins = cause.payload?.pinnedBy
      refusedFor.value = [
        ...(Array.isArray(defaults) ? defaults.map(String) : []),
        ...(Array.isArray(pins) ? pins.map(String) : []),
      ]
      return
    }
```

   and update the comment above the refusal modal, which currently says only
   "A default model cannot be deleted". The payload is camel-cased by
   `GatewayHttpError` (`api.ts:137-140` → `toCamelCase`), so `pinned_by`
   arrives as `pinnedBy`.

3. `frontend/src/lib/api.ts:2600` — the `deleteLlmModel` doc comment: say the
   delete names the agents that default to the row **and** the settings that
   pin it.

4. `frontend/e2e/settings-models.spec.ts` — pin the corrected copy and the new
   refusal in the existing cases:

   - In `the default model cannot be deleted, and the page names the agent that
     holds it` (`:205-234`), before clicking `confirm-ok`, assert the confirm
     dialog's message:
     ```ts
     await expect(page.locator('.forebrain-confirm-message')).toHaveText(
       '删除后，用过它的会话下一条消息会被拒绝，需要用 /model 重新选。被主代理默认、子代理、计划评审或目标评估引用的模型不能删除，请先改掉那些引用。',
     )
     ```
     and after the refusal opens, assert
     `.forebrain-modal`/the page shows `它仍被下列设置引用，先把这些引用改成别的再删。`
     plus the `models-refused-agent` badge (unchanged).
   - Add `test('a model a subagent type pins cannot be deleted either', …)`:
     seed a row with `seedModel`, pin it through `PATCH /api/settings` copying
     the save helper in `frontend/e2e/settings-goal-check-model.spec.ts`
     (it writes the plan-review row; use the model group it sets), open
     Settings → Models, click that row's `models-remove-*`, confirm, and assert
     409 refusal badges name `plan review`; the row is still listed.

**Verify**:

```bash
cd frontend
corepack pnpm typecheck
corepack pnpm test -- --run src/locales
cd ..
FOREBRAIN_E2E_SPEC=e2e/settings-models.spec.ts scripts/acceptance/web_e2e.sh
```

→ typecheck exit 0; the locale tests pass (there is no copy test to break, but
the file must still parse and every key keep its both-language pair); the e2e
run ends with `web e2e: PASS` and no `test.fail`/`skip`. (The harness builds
the binary and the UI itself and serves the fresh build — it never touches
`pkg/gateway/dist`.)

### Step 7: Full gates

```bash
FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/config ./pkg/process ./pkg/gateway ./pkg/tui ./pkg/architecture -count=1 -timeout 30m
gofmt -l cmd pkg third_party plugins
go vet -tags fts5 ./...
git diff --name-only "$BASE"   # only the in-scope files (BASE recorded at worktree creation)
```

Expected: 5 packages `ok`; gofmt prints nothing; vet exits 0; the diff's file
set ⊆ Scope (plus `plans/README.md` when you update the row). Also confirm no
new production files appeared: `ls pkg/tui/*.go | grep -vc _test` stays 33,
`ls pkg/gateway/*.go | grep -vc _test` stays 34. No new `*_test.go` either —
a `models_pins_api_test.go` would fail the pairing test, which is why Step 3
appends to `models_api_test.go`.

Then the web gate from Step 6 once more if any Go/JSON shape changed since
(`FOREBRAIN_E2E_SPEC=e2e/settings-models.spec.ts scripts/acceptance/web_e2e.sh`
→ `web e2e: PASS`).

### Step 8: Real-machine acceptance (owner or attended executor)

Use the `run-forebrain` skill (`.claude/skills/run-forebrain/SKILL.md`) to
build and drive the real binary in tmux against a scratch home with two
configured rows (add them via `/connect` or seed the store). Legs:

1. `/model` → cursor on a non-default row → `C-d` → confirmation shows
   `Remove <provider/model>?` + endpoint, **Keep item** preselected → Enter →
   row still listed; the table unchanged.
2. `C-d` → select `Remove item` → Enter → `Removed <label>.` frame; the list
   re-opens without the row; `sqlite3 <home>/state/forebrain.state.sqlite
   'SELECT id FROM fb_llm_models'` confirms the row is gone.
3. Make a row the default (`/config` → agents → definitions → main → Model,
   or web Settings → Models → set default) → `C-d` → Remove → the refusal
   sentence names the agent; the row survives.
4. Pin a third row somewhere the loader requires (e.g. `/config` → Tool &
   memory settings → Built-in subagents → explore → Model) → `C-d` → Remove →
   the refusal names `subagent explore`; the row survives; **send a message and
   confirm the turn still runs** (this is the leg the old behavior broke: before
   the fix the delete went through and every later turn failed with
   `sync config: … must be provider/model`).
5. Web (Step 6's copy): Settings → Models → Delete on a row → the confirm
   sentence reads the corrected consequences; Delete the default row → the
   refusal modal shows `它仍被下列设置引用…` with the agent badge.
6. Select the remaining non-default row as the conversation's model
   (`/model` → Enter), then delete it → next message is refused with
   "…no longer configured; choose another with /model"; `/model` re-choose
   recovers the conversation.
7. Delete the last row → "This agent has no models configured." and the
   picker closes.

If running unattended, mark Step 8 **NOT RUN** in the execution record and
say so — do not claim it passed.

## Test plan

New tests (names grep-verified collision-free at `c0d4ee16`):

| Where | Test | Covers |
|---|---|---|
| `pkg/config/llm_models_test.go` | `TestModelDefaultAgentsNamesTheAgentsThatRunOnTheRow` | display name vs key, sorted, subagent pin excluded, nil/empty |
| `pkg/config/llm_models_test.go` | `TestModelPinnedByNamesTheSettingsThatMustResolve` | four pin shapes, labels, `default_model` excluded |
| `pkg/config/llm_models_test.go` | `TestDanglingReferencesTheLoaderRefusesAreExactlyThePinnedOnes` | characterization: pins ⇒ `ParseRootYAML` refuses; `default_model`/memory ⇒ loads |
| `pkg/process/llm_models_test.go` | `TestDeleteLLMModelRemovesARowNothingReferences` | happy path + NotFound after |
| `pkg/process/llm_models_test.go` | `TestDeleteLLMModelRefusesTheDefaultModel` | typed refusal (`Defaults`), row survives |
| `pkg/process/llm_models_test.go` | `TestDeleteLLMModelRefusesAPinnedRow` | Step 0's reproduction as a test: refusal (`Pins`), row survives, config still composes |
| `pkg/process/llm_models_test.go` | `TestDeleteLLMModelReportsAMissingRow` | `state.ErrLLMModelNotFound` |
| `pkg/gateway/models_api_test.go` (append) | `TestLLMModelsAPIDeleteRefusesAPinnedRow` | 409 + `pinned_by`, `default_for` empty, row still listed |
| `pkg/tui/chat_session_test.go` | `TestDeleteConfiguredModelRefusesTheDefaultAndRemovesTheRest` | real store: default refusal, pin refusal, success + row gone, already-gone |
| `pkg/tui/commands_test.go` | the six `TestModelPicker*` cases of Step 5 | Keep/Remove/refusal/error/empty/effort-list, Key+RowActive, preselected Keep |
| `frontend/e2e/settings-models.spec.ts` | confirm-copy assertion in the existing default case + `a model a subagent type pins cannot be deleted either` | the corrected copy, the union badges, the pinned 409 |

Regression pins that must pass **unchanged**:
`TestLLMModelsAPIDeleteRefusesTheDefaultModel` (gateway 409/204/404 bodies),
`TestSlashPickerCancelAppliesNothing`, `TestSlashPickerModelRowStillReachesTheEngine`,
`TestSlashPickerOpensOnTheItemInForce`, `TestSlashPickerFollowsTheNextPicker`,
`TestActiveRunSlashNativePickerDoesNotQueueTurn` (plain path untouched).

For the fix-proves-the-cause rule: `TestDeleteLLMModelRefusesAPinnedRow` and
`TestLLMModelsAPIDeleteRefusesAPinnedRow` must be written so they **fail**
against Step 2 before the guard exists — the pinned row is deleted and the
config stops composing — and pass after.

## Done criteria

ALL must hold:

- [ ] Step 0's probe prints the failing reproduction before any edit.
- [ ] Step 1–6 focused commands each print the stated `--- PASS:` counts
      (Step 6: typecheck 0, vitest pass, `web e2e: PASS`).
- [ ] `TestLLMModelsAPIDeleteRefusesTheDefaultModel` passes and its lines are
      untouched: `git diff "$BASE" -- pkg/gateway/models_api_test.go | grep '^-'`
      returns nothing (append-only; a separate test file would break
      `pkg/architecture`'s pairing and underscore rules).
- [ ] The five-package gate, `gofmt -l`, and `go vet -tags fts5 ./...` all clean.
- [ ] `grep -rn 'llmModelDefaultFor' pkg/` returns nothing (helper fully moved).
- [ ] `grep -rn '会回落\|fall back, and every reference' frontend/src` returns
      nothing (the false copy is gone), and `grep -c "pinned_by" frontend/src/lib/api.ts
      pkg/gateway/models_api.go` ≥ 1 each.
- [ ] `git diff --name-only "$BASE"` ⊆ the in-scope list (+ `plans/README.md`),
      and `git status` shows no `pkg/gateway/dist/**` entry.
- [ ] No new production files in `pkg/tui` (33) or `pkg/gateway` (34);
      `pkg/architecture` ok.
- [ ] `plans/README.md` status row updated (DONE / BLOCKED with reason).
- [ ] Step 8 either passed leg-by-leg or is explicitly recorded NOT RUN.

## STOP conditions

Stop and report (do not improvise) if:

1. The drift check finds an in-scope file whose **content** no longer matches
   the excerpts (moved lines alone are fine — re-locate by symbol).
2. `TestLLMModelsAPIDeleteRefusesTheDefaultModel` goes red after Step 3 —
   the shared semantics diverged from the web's; do not "fix" the test.
3. `fireAction`/`SelectRichActions` behave contrary to §5 (e.g. `res.Row`
   arrives -1 with `RowActive` set, or Esc arrives as `OK: true`) — the
   machinery drifted; the affordance design rests on it.
4. The Step 4 seed (`definitions.main.default_model` → a real row) is refused
   by the section validator — the fixture premise is false; report the
   validator's words, do not bypass it.
5. The change appears to require an out-of-scope file (`overlays.go`,
   `pkg/turn/**`, the store, `/connect`) — the Scope list names the only
   frontend files this plan may touch; needing another one is a STOP.
6. A step's verification fails twice after a reasonable fix attempt.
7. Step 0's probe passes the "still composes after delete" assertion — the
   destructive premise is gone and the pin guard has no cause to fix; report
   before writing it.
8. `ParseRootYAML` refuses a document this plan's `ModelPinnedBy` does **not**
   enumerate (a strict reference outside Current state §7's five sites) — the
   enumeration is incomplete; report the document and the validator's words
   instead of widening the guard by guesswork.
9. The web page (e2e) still shows a sentence this plan did not write — a
   second copy source exists; find it before changing tests to match it.

## Maintenance notes

- **One semantics, three consumers**: `config.ModelDefaultAgents` +
  `config.ModelPinnedBy` (guards) ←
  `process.DeleteLLMModel` (orchestration) ← gateway handler / TUI session.
  A future surface (channel bot, CLI) deletes through `process.DeleteLLMModel`
  and adopts via `SyncConfigFromDB` — never re-spell the guard.
- **Guard/delete race**: the guard reads, then the delete writes — no
  transaction spans both (the store API has none). A concurrent default-set or
  pin between them can still leave a dangling reference; for `default_model`
  the loader skips it with a Warn (plan 315), for a pin it would refuse the
  config — so if that window ever matters, the fix is a store-level
  transaction (check-then-delete in one BEGIN IMMEDIATE), not a surface patch.
  The window is exactly the web's existing one; this plan does not widen it.
- **Mid-run adoption**: `SyncConfigFromDB` after a delete is the same call
  the web's `adoptLLMModelWrite` makes mid-run today; the running turn's
  client is already built, and the next turn reads the new snapshot. No new
  concurrency surface is introduced.
- **Line-mode selectors** (no `ActionRichSelector`) keep `/model` select-only
  — deliberate, the same degradation `/config` has; do not add a fake
  "Delete" row among the models (its `Value` would collide with row ids).
- **Key display**: `/model` says `Ctrl+D Remove` because the list grows a
  filter at 8 rows; `/config` pages say `d Remove` because they never filter.
  If the filter threshold or the action key binding changes, revisit both.
- **The copy that was wrong is now pinned by a test**: before this plan,
  `models.deleteMessage` promised defaults "fall back" while the guard refuses
  them — and, worse, a delete the loader could not survive went through. The
  e2e assertion added in Step 6 is the copy's regression pin; do not reword it
  without changing the behavior it describes.
- **A guard exists only where the loader is strict**: the pin list is the five
  sites in Current state §7. A new strict reference added to `pkg/config`
  must add its shape to `ModelPinnedBy` — `TestDanglingReferencesTheLoaderRefusesAreExactlyThePinnedOnes`
  covers the known five; a sixth site is caught in review, or by re-running the
  Step 0 probe shape against the new reference. The guard mirrors the loader's
  **scope** as well as its sites: a primary definition's own `model` field is
  never validated, so `ModelPinnedBy` skips it (`!IsEffectivePrimaryAgent`).
- Reviewer scrutiny points: the default-only 409 sentence must stay
  word-for-word (zh `、` join, en `, ` join); `RowActive` non-nil; the
  confirmation preview is the endpoint, never key material; the plain picker
  path byte-identical; `pinned_by` additive and camel-cased on the wire.

## As built (2026-11-21, executor `deepseek-flash` + reviewer, worktree `.worktrees/345-tui-delete-configured-model`)

Seven commits `402531ff..ca703193` on branch `345-tui-delete-configured-model`,
unpushed, nothing merged, main tree untouched. 14 files, all in scope
(+1014/−97).

- **Deviation accepted (guard widened, evidence-backed)**: the fifth strict
  shape (`definitions.<role>.model`) and the primary-skip, both documented
  above; both were proven with overlay probes and are pinned by
  `TestDanglingReferencesTheLoaderRefusesAreExactlyThePinnedOnes` (8 sub-cases,
  both directions) and by a negative assertion in
  `TestModelPinnedByNamesTheSettingsThatMustResolve`.
- **Deviation accepted (test rename)**: the e2e case ships as `a model the plan
  review pins cannot be deleted either` — `agents.subagent_models.*` is not a
  `PATCH /api/settings` path, so the plan-review row is the reachable pin.
- **Reviewer review**: REVISE once (the two items above — the first was a real
  false-refusal the reviewer reproduced), then APPROVE.
- **Reviewer-run gates (independently re-run, not trusted from the report)**:
  `pkg/config` focused 3 PASS; `pkg/process` 4 PASS; `pkg/gateway`
  `TestLLMModelsAPI*` 19 PASS (existing delete pin untouched, content removals
  `grep -c '^-[^-]'` = 0); `pkg/tui` focused 14 PASS; `pkg/architecture` ok
  2.073s; `gofmt -l` empty; `go vet -tags fts5 ./...` exit 0; production file
  counts unchanged (tui 33, gateway 34); no `A` files; no stale copy
  (`grep '会回落'` → 0).
- **Red proof (reviewer)**: with `ModelPinnedBy` neutralised through
  `-overlay`, `TestDeleteLLMModelRefusesAPinnedRow` fails
  (`delete = <nil>, want a refusal`) and
  `TestLLMModelsAPIDeleteRefusesAPinnedRow` fails (`expected: 409, actual:
  204`) — the guard is what breaks the chain.
- **Executor-run gates**: `pnpm typecheck` 0, vitest 194 files / 1574 tests,
  `FOREBRAIN_E2E_SPEC=e2e/settings-models.spec.ts scripts/acceptance/web_e2e.sh`
  → 7 passed, `web e2e: PASS`. The reviewer did **not** re-run the browser
  suite (disk at 99%, 1.9 GiB free; the string is verified in source and the
  spec asserts it).
- **Not run**: Step 8 real-machine acceptance (unattended executor), and the
  full `./...` suite on this tip — `pkg/run` failed to *link* once under ENOSPC
  at 100% disk and passed alone; every other package was green in that run.
- **Known environment problem at hand-off**: the data volume is at 99%
  (1.9 GiB free), which is what broke the full-suite link; reclaim before any
  further full-suite run.