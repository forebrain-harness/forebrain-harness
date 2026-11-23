# Plan 347: One structured pin list, one English refusal sentence, localized labels per surface

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)** — baseline `ca703193` (tip of branch
> `345-tui-delete-configured-model`, plan 345's as-built tip; the code this
> plan changes exists only on that branch). Working tree expected clean. Run
> BOTH commands (a two-commit diff is empty when baseline == HEAD and hides
> uncommitted edits):
>
> ```bash
> git status --porcelain -- pkg/config/llm_models.go pkg/config/llm_models_test.go \
>   pkg/process/llm_models.go pkg/process/llm_models_test.go \
>   pkg/gateway/models_api.go pkg/gateway/models_api_test.go \
>   pkg/tui/chat_slash.go pkg/tui/chat_session_test.go \
>   frontend/src/lib/api.ts frontend/src/lib/modelPins.ts \
>   frontend/src/components/settings/ModelsTab.vue \
>   frontend/src/locales/index.ts frontend/e2e/settings-models.spec.ts
> git diff ca703193 -- pkg/config/llm_models.go pkg/process/llm_models.go \
>   pkg/gateway/models_api.go pkg/tui/chat_slash.go frontend/e2e/settings-models.spec.ts
> ```
>
> If any in-scope file changed, compare the "Current state" excerpts against
> the live code before proceeding. **Content mismatch is a STOP condition;
> moved line numbers alone are not** — re-locate by the quoted symbol/text.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: LOW (a wire field nobody reads yet changes shape; every message
  and label below is pinned by a test at the end)
- **Depends on**: plan 345 must be on this HEAD (branch
  `345-tui-delete-configured-model`). `dev` is 8 commits ahead of the branch
  point: rebase onto `dev` **before** starting if the operator asks for it,
  then re-run the drift check.
- **Category**: tech-debt (i18n + one-source-of-truth)
- **Planned at**: `ca703193`, 2026-11-21

## Why this matters

Plan 345 shipped the delete guards and their messages, and a branch audit
found three problems that share one cause: **the pin list is rendered into a
string too early, and in three places.**

1. `pkg/process.LLMModelRefusal.Error()` is a third wording of the refusal and
   it is wrong: with defaults *and* pins set it prints only the default
   sentence and silently drops the pins (verified with an `-overlay` probe, no
   workspace change). No test covers the both case on either surface.
2. The same sentence is spelled again in `pkg/gateway/models_api.go`
   (`llmRefusalSentence`) and a third time in `pkg/tui/chat_slash.go`. A
   change to the facts (another owner kind, another remedy) needs three edits.
3. The pin labels (`subagent explore`, `plan review`, `goal check`) are
   English strings baked into the API payload and into the Chinese sentence,
   so the Chinese models page shows 「它仍被「subagent explore」引用」 and
   English badges. The page localizes from `localStorage['forebrain-locale']`,
   not from `Accept-Language`, so the server cannot localize for it — the
   payload must carry the *structure* and each surface renders its own
   language.

After this plan: one `config.ModelPin` value type, one English sentence
(`LLMModelRefusal.Error()`), a structured `pinned_by` payload the page
localizes, and the Chinese sentence rendered from the same structured list.
The e2e case that leaked an install-wide pin onto the shared test home also
clears it (branch-audit finding F2).

## Current state

Everything below was read at `ca703193`. Line numbers are anchors, not
contracts; the quoted text is the contract.

### 1. The pin projection (`pkg/config/llm_models.go:177-216`)

```go
func ModelPinnedBy(root *Root, id string) []string {
	...
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
	for key, def := range root.Agents.Definitions {
		if !IsEffectivePrimaryAgent(key, def) && strings.TrimSpace(def.Model) == id {
			add("subagent", key)
		}
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

Callers: `pkg/process/llm_models.go:806` and
`pkg/gateway/models_api.go:482`; tests: `pkg/config/llm_models_test.go`
(`TestModelPinnedByNamesTheSettingsThatMustResolve`,
`TestDanglingReferencesTheLoaderRefusesAreExactlyThePinnedOnes`). Nothing
else references it (`grep -rn 'ModelPinnedBy' pkg/` → these five sites).

The five strict shapes (a row id) are unchanged and must stay: subagent type
row, plan review, goal check, a non-primary definition's own `model`, a
per-agent `subagents[].model`. `ModelDefaultAgents` is **not** touched.

### 2. The refusal (`pkg/process/llm_models.go:764-781`)

```go
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
```

`DeleteLLMModel` (same file, `:796-810`) fills it:
`pins := appcfg.ModelPinnedBy(cfg, stored.ID)`.

Both surfaces intercept the typed refusal, so today the only reader of
`Error()` is a log or a 500 fallback — and it is the wrong wording.

### 3. The gateway wording and payload (`pkg/gateway/models_api.go`)

- `:117-125` the row DTO carries `PinnedBy []string \`json:"pinned_by,omitempty"\``
  next to `DefaultFor []string \`json:"default_for,omitempty"\``.
- `:482` `PinnedBy: config.ModelPinnedBy(scope.cfg, row.ID),` inside
  `llmModelRowOf` — this runs once per row of every list answer (`:552`).
- `:675-703` `deleteLLMModel` answers a refusal with
  `{"error": llmRefusalSentence(r, refusal), "default_for": refusal.Defaults,
  "pinned_by": refusal.Pins}` at `409`.
- `:704-719` `llmRefusalSentence` holds the English and Chinese sentences:

```go
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
```

**The default-only branch is the one existing test pin**
(`TestLLMModelsAPIDeleteRefusesTheDefaultModel`) asserts around: zh
`它是「…」的默认模型，请先把默认改成别的`, en `it is the default model of …;
choose another default first`. Keep both byte-identical.

### 4. The TUI wording (`pkg/tui/chat_slash.go:732-770`)

`DeleteConfiguredModel` builds the same three sentences itself, capitalized
and with a trailing period (`"It is the default model of …"`). Its test
`TestDeleteConfiguredModelRefusesTheDefaultAndRemovesTheRest`
(`pkg/tui/chat_session_test.go`, at the end of the file) asserts those exact
strings.

### 5. The page (`frontend/`)

- `src/components/settings/ModelsTab.vue:250-266` builds the refusal badges
  from the 409 payload: `cause.payload?.defaultFor` plus
  `cause.payload?.pinnedBy`, both `Array.isArray`-guarded, `map(String)`.
- `src/components/settings/ModelsTab.vue:92-96` renders one
  `BadgeComponent` per entry, `data-testid="models-refused-agent"`.
- `src/lib/api.ts:1479-1481` types the row: `defaultFor?: string[]` and
  **no** `pinnedBy` (plan 348 adds the badge that reads it).
- Locale keys (`src/locales/index.ts`): zh at `:2443-2448`, en at
  `:5001-5006` (`models.deleteTitle`, `models.deleteMessage`,
  `models.deleteRefusedTitle`, `models.deleteRefusedMessage`,
  `models.deleteRefusedOk`). The `t` in this codebase interpolates `{name}`
  — see `models.sourceLineWithPlan` (`' 订阅登录 {plan}'`).
- `e2e/settings-models.spec.ts:245-283` is the pinned-row case. `:276`
  asserts the badge text `['plan review']`; the case ends after clicking
  `models-refused-ok` and asserting the row is still listed. It PATCHes
  `agents.plan_reviewer.model` at `:259-261` and **never clears it**, while
  `e2e/settings-goal-check-model.spec.ts:186-191` clears the analogous
  goal-check row and asserts `''`. `scripts/acceptance/web_e2e.sh:108,272`
  gives the whole run one `FOREBRAIN_HOME` and one gateway process, so the
  leftover pin outlives this spec file.

### 6. The page's language is not the API's

`prefersEnglish` reads `Accept-Language` (`pkg/gateway/models_api.go:225-237`),
but `preferChinese` in the spec only sets `localStorage['forebrain-locale']`.
A server-side localized label therefore never reaches the Chinese page:
localization has to happen in the frontend from a structured payload.

## Commands you will need

Go runs need CGO + FTS5 and the fetched PDFium triple; the module's Makefile
sets `PKG_CONFIG_PATH` only inside make, so export it yourself:

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
```

| Purpose | Command | Expected on success |
|---|---|---|
| Go tests (touched packages) | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/config ./pkg/process ./pkg/gateway -count=1` | `ok` for all three |
| Go tests (TUI, focused) | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestDeleteConfiguredModel' -count=1 -v` | every `--- PASS` |
| Formatting | `gofmt -l cmd pkg third_party plugins` | prints nothing |
| Vet | `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0 |
| Frontend types | `cd frontend && corepack pnpm typecheck` | exit 0 |
| Frontend unit tests | `cd frontend && corepack pnpm test -- --run src/lib/modelPins src/components/settings` | all pass |
| Browser e2e | `FOREBRAIN_E2E_SPEC=e2e/settings-models.spec.ts scripts/acceptance/web_e2e.sh` | ends with `web e2e: PASS` |

**Disk**: at planning time the data volume had ~5 GiB free (98% used). The e2e
harness builds the binary and the UI. If a build fails with `ENOSPC`, stop and
report — do not delete anything outside the repo.

## Scope

**In scope** (the only files you should modify):

- `pkg/config/llm_models.go` — `ModelPin` + `ModelPins`, delete `ModelPinnedBy`
- `pkg/config/llm_models_test.go` — re-assert the two pin tests on the new shape
- `pkg/process/llm_models.go` — `Pins []appcfg.ModelPin`; one `Error()`
- `pkg/process/llm_models_test.go` — refusal sentences (three cases) + existing tests
- `pkg/gateway/models_api.go` — structured `pinned_by`, zh labels, en from `Error()`
- `pkg/gateway/models_api_test.go` — append: both-case 409, row-shape test, updated decode struct
- `pkg/tui/chat_slash.go` — reply = `refusal.Error()`
- `pkg/tui/chat_session_test.go` — updated expectations + the both case
- `frontend/src/lib/api.ts` — `LlmModelPinRef`; `LlmModelRow.pinnedBy?`
- `frontend/src/lib/modelPins.ts` (create) — `modelPinLabel` / `modelPinLabels`
- `frontend/src/lib/modelPins.test.ts` (create)
- `frontend/src/components/settings/ModelsTab.vue` — badges via `modelPinLabels`
- `frontend/src/locales/index.ts` — three `models.pin*` keys, zh + en
- `frontend/e2e/settings-models.spec.ts` — zh badge, and the pin cleared at the end
- `plans/README.md` — this plan's status row

**Out of scope** (do NOT touch, even though they look related):

- `pkg/gateway/models_api.go`'s `llmModelDefaultFor` replacement
  (`ModelDefaultAgents`) and every default-model sentence — those are settled
  by plans 265/345 and their tests are regression pins.
- `frontend/src/components/models/ModelsTable.vue` — plan 348 puts the row
  badge there; this plan only makes the data usable.
- `pkg/turn/**`, `pkg/state/**`, `pkg/run/**` — nothing here reaches the engine.
- The Chinese wording of `models.deleteMessage` / `models.deleteRefusedMessage`
  — owner copy from plan 345; only *new* keys are added.
- `plans/config-tui-prototype.html` and `plans/311-connect-prototype.html` —
  frozen contracts this change does not touch.

## Git workflow

- Worktree `.worktrees/347-pin-labels-and-refusal-copy` in the repo's main
  worktree, branch `347-pin-labels-and-refusal-copy` from the current HEAD
  (must be `ca703193` or a rebase of it). **Record `BASE=$(git rev-parse
  HEAD)` right after creating the worktree, before the first edit** — the
  scope gates below compare against `$BASE`.
- A fresh worktree lacks gitignored build deps; copy them in:

```bash
MAIN="$(git worktree list | head -1 | awk '{print $1}')"
mkdir -p third_party
cp -R "$MAIN/third_party/pdfium" third_party/
cp -R "$MAIN/third_party/libtokenizers" third_party/
mkdir -p pkg/gateway
cp -R "$MAIN/pkg/gateway/dist" pkg/gateway/
```

- Commit per step, conventional style matching `git log` (e.g.
  `refactor(config): name a model pin by its kind and name (plan 347)`).
- Do **NOT** push, merge, or touch the user's branch — the owner decides
  landing. Remove the worktree and branch after an owner-approved merge.

## Steps

### Step 1: `config.ModelPin` and `ModelPins`, and delete `ModelPinnedBy`

In `pkg/config/llm_models.go`, replace `ModelPinnedBy` with a structured
projection. Keep the same five shapes, the same `!IsEffectivePrimaryAgent`
skip, the same `strings.TrimSpace` guards, and the same **order** the strings
had (sort by the English label, so today's expectations keep their order):

```go
// ModelPin is one setting whose reference the loader requires to resolve: the
// row cannot be deleted behind it (plan 345). Kind is a machine word a surface
// localizes; Name is the subagent's name and empty for the two install-wide
// rows.
type ModelPin struct {
	Kind string
	Name string
}

const (
	ModelPinSubagent   = "subagent"
	ModelPinPlanReview = "plan_review"
	ModelPinGoalCheck  = "goal_check"
)

// English is the label every English surface shows — the terminal, the API's
// English sentence, this package's tests. It is the only English rendering.
func (p ModelPin) English() string {
	switch p.Kind {
	case ModelPinPlanReview:
		return "plan review"
	case ModelPinGoalCheck:
		return "goal check"
	default:
		if name := strings.TrimSpace(p.Name); name != "" {
			return "subagent " + name
		}
		return "subagent"
	}
}

// ModelPins names the settings that pin this row, sorted by their English
// label. (Keep the rest of ModelPinnedBy's doc comment above it.)
func ModelPins(root *Root, id string) []ModelPin { ... }
```

`ModelPins` returns `nil` for a nil root / blank id (same as today), and sorts
with:

```go
sort.Slice(pins, func(i, j int) bool { return pins[i].English() < pins[j].English() })
```

Delete `ModelPinnedBy` entirely — nothing else may keep a `[]string` copy.

**Verify**: `grep -rn 'ModelPinnedBy' pkg/` → no matches;
`CGO_ENABLED=1 go build ./pkg/config` → exit 0 (the rest of the tree does not
compile yet — that is Step 2 and 3).

### Step 2: The refusal carries pins, and words them once

In `pkg/process/llm_models.go`:

```go
type LLMModelRefusal struct {
	Defaults []string
	Pins     []appcfg.ModelPin
}

// Error words a refusal once for every English surface (plan 347): the default
// clause, the pin clause, or both. The terminal prints this sentence verbatim
// and the gateway's English branch returns it; only the gateway's Chinese
// wording is its own (llmRefusalSentence).
func (e *LLMModelRefusal) Error() string {
	defaults := strings.Join(e.Defaults, ", ")
	pins := modelPinLabels(e.Pins)
	switch {
	case len(e.Defaults) > 0 && len(e.Pins) > 0:
		return "it is the default model of " + defaults + " and is referenced by " + pins + "; point those settings elsewhere first"
	case len(e.Pins) > 0:
		return "it is still referenced by " + pins + "; point those settings elsewhere first"
	default:
		return "it is the default model of " + defaults + "; choose another default first"
	}
}

// modelPinLabels joins the pins' English labels the way every sentence lists
// them.
func modelPinLabels(pins []appcfg.ModelPin) string {
	out := make([]string, 0, len(pins))
	for _, pin := range pins {
		out = append(out, pin.English())
	}
	return strings.Join(out, ", ")
}
```

`DeleteLLMModel`: `pins := appcfg.ModelPins(cfg, stored.ID)`.

Then update the two assertions that name a pin as a string, so the package
still compiles and the tests still mean what they say:

- `pkg/process/llm_models_test.go`, `TestDeleteLLMModelRefusesAPinnedRow`:
  `if len(refusal.Pins) != 1 || refusal.Pins[0] != (appcfg.ModelPin{Kind: appcfg.ModelPinSubagent, Name: "explore"}) || len(refusal.Defaults) != 0`

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/config ./pkg/process -count=1` → both `ok` (a `ModelPin` compared to a bare string does not compile, so this step must update that assertion in the same commit).

### Step 3: The gateway sends structure and localizes the sentence

In `pkg/gateway/models_api.go`:

1. Replace the row field:

```go
	// PinnedBy names the settings whose reference the loader requires to
	// resolve: a subagent type's row, the plan reviewer, the goal check, a
	// subagent definition's own model (plan 345). A machine kind plus the name
	// it applies to, so each surface writes its own language (plan 347).
	PinnedBy  []llmModelPin `json:"pinned_by,omitempty"`
```

```go
// llmModelPin is one pin on the wire.
type llmModelPin struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}

// llmModelPins projects the config pins into the wire shape; nil in, nil out.
func llmModelPins(pins []config.ModelPin) []llmModelPin { ... }
```

2. `llmModelRowOf`: `PinnedBy: llmModelPins(config.ModelPins(scope.cfg, row.ID)),`
3. `deleteLLMModel`'s 409 body: `"pinned_by": llmModelPins(refusal.Pins),`
4. `TestLLMModelsAPIDeleteRefusesAPinnedRow` (same commit — the payload it
   decodes changed): replace
   `PinnedBy []string \`json:"pinned_by"\`` with
   `PinnedBy []struct{ Kind, Name string } \`json:"pinned_by"\`` and assert
   `len(conflict.PinnedBy) == 1 && conflict.PinnedBy[0].Kind == "plan_review" && conflict.PinnedBy[0].Name == ""`.
5. `llmRefusalSentence`: the English branch is `refusal.Error()`; the Chinese
   branch builds its labels with

```go
// modelPinLabelZH is the Chinese label for one pin (plan 347). The page
// localizes from the payload with its own copy; this is for the API's Chinese
// sentence alone.
func modelPinLabelZH(pin config.ModelPin) string {
	switch pin.Kind {
	case config.ModelPinPlanReview:
		return "计划评审"
	case config.ModelPinGoalCheck:
		return "目标评估"
	default:
		if name := strings.TrimSpace(pin.Name); name != "" {
			return "子代理 " + name
		}
		return "子代理"
	}
}

// modelPinLabelsZH is modelPinLabelZH over a list, in the payload's order.
func modelPinLabelsZH(pins []config.ModelPin) []string {
	out := make([]string, 0, len(pins))
	for _, pin := range pins {
		out = append(out, modelPinLabelZH(pin))
	}
	return out
}
```

and the three branches become (zh spelled with `、` joins, **default-only
byte-identical to today**):

```go
case len(refusal.Defaults) > 0 && len(refusal.Pins) > 0:
	return llmText(r,
		"它是「"+strings.Join(refusal.Defaults, "、")+"」的默认模型，也被「"+strings.Join(modelPinLabelsZH(refusal.Pins), "、")+"」引用；先把这些引用改成别的",
		refusal.Error())
case len(refusal.Pins) > 0:
	return llmText(r,
		"它仍被「"+strings.Join(modelPinLabelsZH(refusal.Pins), "、")+"」引用；先把这些引用改成别的",
		refusal.Error())
default:
	return llmText(r,
		"它是「"+strings.Join(refusal.Defaults, "、")+"」的默认模型，请先把默认改成别的",
		refusal.Error())
```

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'TestLLMModelsAPI' -count=1` → `ok` (the existing default-case pin must pass untouched; item 4 keeps the pinned test green on the new payload).

### Step 4: The terminal prints the shared sentence

In `pkg/tui/chat_slash.go`, `DeleteConfiguredModel`: replace the three-case
`switch` with

```go
	if errors.As(err, &refusal) {
		return refusal.Error(), nil
	}
```

(the `switch`'s three `return` statements and nothing else). The TUI prints
answers verbatim, the same way `renderer.PrintError` prints an error string.

In the same commit, update the two expectations this changes in
`pkg/tui/chat_session_test.go`,
`TestDeleteConfiguredModelRefusesTheDefaultAndRemovesTheRest`:

- `"It is the default model of main; choose another default first."` →
  `"it is the default model of main; choose another default first"`
- `"It is still referenced by subagent explore; point those settings elsewhere first."` →
  `"it is still referenced by subagent explore; point those settings elsewhere first"`

**Verify**: `grep -n 'It is the default model of' pkg/tui/chat_slash.go` → no
matches; `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestDeleteConfiguredModel' -count=1 -v` → `--- PASS`.

### Step 5: Pin the three sentences in Go

- `pkg/config/llm_models_test.go`:
  - rename `TestModelPinnedByNamesTheSettingsThatMustResolve` →
    `TestModelPinsNamesTheSettingsThatMustResolve` and assert
    `[]ModelPin{{Kind: config.ModelPinGoalCheck}, {Kind: ModelPinPlanReview}, {Kind: ModelPinSubagent, Name: "explore"}, ...}`
    in the same order the old strings had (`goal check`, `plan review`,
    `subagent explore`, `subagent repo-auditor`);
  - `TestDanglingReferencesTheLoaderRefusesAreExactlyThePinnedOnes` uses
    `ModelPins` and keeps both directions;
  - add `TestModelPinEnglish` for the three kinds (with and without a name).
- `pkg/process/llm_models_test.go`: `Pins` assertions become
  `appcfg.ModelPin` comparisons; add

```go
func TestLLMModelRefusalSentences(t *testing.T) {
	// default only, pin only, both — the both case is the one the branch audit
	// found missing (Error() dropped the pins).
}
```

  asserting the exact three strings, including the both case's
  `it is the default model of main and is referenced by plan review, subagent explore; point those settings elsewhere first`.
- `pkg/tui/chat_session_test.go`: extend the same test with the **both** case:
  seed `default_model` *and* `agents.plan_reviewer.model` on one row, then
  `DeleteConfiguredModel` answers
  `"it is the default model of main and is referenced by plan review; point those settings elsewhere first"`
  and the row survives. (The two existing expectations were already updated in
  Step 4.)

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/config ./pkg/process -count=1` → `ok`;
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestDeleteConfiguredModel' -count=1 -v` → every `--- PASS`.

### Step 6: The gateway tests (append to `models_api_test.go`)

1. Append `TestLLMModelsAPIDeleteRefusesARowThatIsBoth` — seed a row, make it
   `main`'s `default_model` **and** `agents.plan_reviewer.model`, DELETE →
   409, `default_for` names main, `pinned_by` has one `plan_review` entry,
   `error` contains both clauses.
2. Append `TestLLMModelsAPIListNamesTheSettingsThatPinARow` — pin one row and
   leave another unpinned, `GET /api/llm-models` → the pinned row carries one
   `pinned_by` entry (`kind: plan_review`, `name` omitted), the other row
   carries none. Assert with `Accept-Language: zh-CN` that the **row payload
   stays structured** (the page localizes; the server never bakes a label
   into the row).

`TestLLMModelsAPIDeleteRefusesAPinnedRow`'s decode struct was already updated
in Step 3; every other existing test in this file stays byte-identical.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'TestLLMModelsAPI' -count=1 -v` → every `--- PASS`, including `TestLLMModelsAPIDeleteRefusesTheDefaultModel`.

### Step 7: The page localizes the labels

1. `frontend/src/lib/api.ts`: next to the row type add

```ts
/** One pin the API sends: a machine kind, and the name it applies to. */
export interface LlmModelPinRef {
  kind: string
  name?: string
}
```

   and add to `LlmModelRow`, beside `defaultFor?: string[]`:

```ts
  /** The settings that pin this row by id — a kind plus a name. */
  pinnedBy?: LlmModelPinRef[]
```

2. Create `frontend/src/lib/modelPins.ts`:

```ts
/**
 * One pin as a person reads it (plan 347). The API sends the structure, not a
 * label, because the page's language lives in localStorage and never reaches
 * the request's Accept-Language: each surface writes its own words.
 */
import type { I18nKey } from '@/locales'
import type { LlmModelPinRef } from './api'

/** The app's own translator (src/locales/index.ts:5197), so `t` from useI18n passes. */
type Translate = (key: I18nKey, params?: Record<string, string | number>) => string

export function modelPinLabel(t: Translate, pin: LlmModelPinRef): string {
  switch (pin.kind) {
    case 'plan_review':
      return t('models.pinPlanReview')
    case 'goal_check':
      return t('models.pinGoalCheck')
    default:
      return t('models.pinSubagent', { name: pin.name ?? '' }).trim()
  }
}

export function modelPinLabels(t: Translate, pins?: LlmModelPinRef[]): string[] {
  return (pins ?? []).map((pin) => modelPinLabel(t, pin))
}
```

   Keep the subagent fallback simple: the config never emits a nameless
   subagent pin today (`ModelPin.English` returns `subagent` for it), so
   `models.pinSubagent` with an empty name may render `子代理 ` — trim it.

3. `frontend/src/locales/index.ts`, next to `models.deleteRefusedOk` (zh and
   en blocks; keep key order):

```ts
  'models.pinSubagent': '子代理 {name}',
  'models.pinPlanReview': '计划评审',
  'models.pinGoalCheck': '目标评估',
```
```ts
  'models.pinSubagent': 'subagent {name}',
  'models.pinPlanReview': 'plan review',
  'models.pinGoalCheck': 'goal check',
```

4. `frontend/src/components/settings/ModelsTab.vue:250-266` — `refusedFor`
   becomes the defaults plus the localized pin labels:

```ts
      const defaults = cause.payload?.defaultFor
      const pins = cause.payload?.pinnedBy
      refusedFor.value = [
        ...(Array.isArray(defaults) ? defaults.map(String) : []),
        ...modelPinLabels(t, Array.isArray(pins) ? (pins as LlmModelPinRef[]) : []),
      ]
```

   Update the comment above the refusal modal to say the API sends the kinds
   and the page writes the words.

**Verify**: `cd frontend && corepack pnpm typecheck` → exit 0.

### Step 8: The spec's zh badge and the pin it must clear

In `frontend/e2e/settings-models.spec.ts:245-283`:

1. `:276` becomes `await expect(page.locator('[data-testid="models-refused-agent"]')).toHaveText(['计划评审'])`.
2. After the refusal is acknowledged and the row is asserted still listed,
   clear the pin and prove the refusal was the pin:

```ts
  // The refusal was the pin, not the row: clearing it lets the same DELETE
  // land. The pin is install-wide and this run shares one home, so leaving it
  // would outlive this spec (plan 347).
  const cleared = await page.request.patch('/api/settings', {
    data: { values: { 'agents.plan_reviewer.model': '' } },
  })
  expect(cleared.ok(), await cleared.text()).toBeTruthy()
  const removed = await page.request.delete(`/api/llm-models/${id}`)
  expect(removed.ok(), await removed.text()).toBeTruthy()
  await page.goto(MODELS, { waitUntil: 'networkidle' })
  await expect(rowOf(page, 'e2e-pinned-model')).toHaveCount(0)
```

3. Add a unit test for the helper: `frontend/src/lib/modelPins.test.ts`
   (pattern: `src/lib/api.test.ts`), asserting the zh and en label for each
   kind under `setLocale('zh')` / `setLocale('en')`.

**Verify**: `cd frontend && corepack pnpm test -- --run src/lib/modelPins src/components/settings` → all pass;
`FOREBRAIN_E2E_SPEC=e2e/settings-models.spec.ts scripts/acceptance/web_e2e.sh` → `web e2e: PASS`.

### Step 9: Full gates

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/config ./pkg/process ./pkg/gateway ./pkg/tui ./pkg/architecture -count=1
gofmt -l cmd pkg third_party plugins
CGO_ENABLED=1 go vet -tags fts5 ./...
cd frontend && corepack pnpm typecheck && corepack pnpm test && corepack pnpm build
```

Expected: five `ok` packages (no `FAIL`), `gofmt` prints nothing, vet exit 0,
frontend typecheck exit 0, vitest all pass, build exit 0.

## Test plan

| Where | Test | Covers |
|---|---|---|
| `pkg/config/llm_models_test.go` | `TestModelPinsNamesTheSettingsThatMustResolve` (renamed) | five shapes, primary skip, nil/blank |
| `pkg/config/llm_models_test.go` | `TestModelPinEnglish` (new) | the one English rendering |
| `pkg/config/llm_models_test.go` | `TestDanglingReferencesTheLoaderRefusesAreExactlyThePinnedOnes` (updated) | pins ⇔ loader refusal, both directions |
| `pkg/process/llm_models_test.go` | `TestLLMModelRefusalSentences` (new) | default-only, pin-only, **both** |
| `pkg/process/llm_models_test.go` | the four `TestDeleteLLMModel*` (updated) | structured `Pins` |
| `pkg/gateway/models_api_test.go` | `TestLLMModelsAPIDeleteRefusesAPinnedRow` (updated), `…RefusesARowThatIsBoth` (new), `…ListNamesTheSettingsThatPinARow` (new) | 409 shape, both-case sentence, row shape |
| `pkg/tui/chat_session_test.go` | `TestDeleteConfiguredModelRefusesTheDefaultAndRemovesTheRest` (updated + both case) | the terminal's shared sentence |
| `frontend/src/lib/modelPins.test.ts` | new | zh/en labels per kind |
| `frontend/e2e/settings-models.spec.ts` | the pinned case (updated) | zh badges, the pin cleared, the row deleted |

Regression pins that must pass unchanged: `TestLLMModelsAPIDeleteRefusesTheDefaultModel`
(409 + `default_for:["main"]`, 204, 404), `TestModelDefaultAgentsNamesTheAgentsThatRunOnTheRow`,
`TestSlashPickerCancelAppliesNothing`, `TestModelPickerRemove*`.

## Done criteria

ALL must hold:

- [ ] `grep -rn 'ModelPinnedBy' pkg/` → no matches.
- [ ] `grep -rn 'still used by' pkg/` → no matches (the old `Error()` wording is gone).
- [ ] `grep -n 'It is the default model of' pkg/tui/chat_slash.go` → no matches.
- [ ] `grep -rn '"subagent " +' pkg/` → exactly one match, in
      `pkg/config/llm_models.go` (`ModelPin.English`), and nothing in
      `pkg/gateway` or `pkg/tui` builds that label.
- [ ] The five-package Go gate, `gofmt`, `go vet -tags fts5 ./...` all clean.
- [ ] `cd frontend && corepack pnpm typecheck` exit 0; `corepack pnpm test` all pass.
- [ ] `FOREBRAIN_E2E_SPEC=e2e/settings-models.spec.ts scripts/acceptance/web_e2e.sh`
      → `web e2e: PASS`.
- [ ] No new Go production files in `pkg/tui` (33) or `pkg/gateway` (34);
      `./pkg/architecture` ok.
- [ ] `git diff --name-only "$BASE"` ⊆ the in-scope list (+ `plans/README.md`).
- [ ] `plans/README.md` status row updated (DONE / BLOCKED with reason).

## STOP conditions

Stop and report (do not improvise) if:

1. The drift check finds an in-scope file whose **content** no longer matches
   the excerpts (moved lines alone are fine — re-locate by symbol).
2. `TestLLMModelsAPIDeleteRefusesTheDefaultModel` goes red and its own file
   has no diff in your change: the default sentence drifted — restore it,
   never edit that test.
3. The `pinned_by` payload turns out to have a consumer this plan did not
   know about (`grep -rn 'pinned_by\|pinnedBy' frontend/src pkg/ docs/`) —
   the shape change is then a contract change; report the consumer instead.
4. The page's locale turns out to arrive as `Accept-Language` after all
   (`grep -rn 'Accept-Language' frontend/src`) — the design premise of
   client-side labels is false; report before changing the payload.
5. A step's verification fails twice after a reasonable fix attempt.
6. The change appears to require an out-of-scope file.

## Maintenance notes

- **One meaning, three renderings**: `config.ModelPin` is the fact;
  `ModelPin.English()` is the only English label; `llmRefusalSentence`'s
  Chinese branch and `src/lib/modelPins.ts` are the two localized renderings.
  A new pin kind needs: a constant, `English()`, `modelPinLabelZH`, the two
  locale keys, and one sub-case in `TestDanglingReferences…`. Nothing else.
- **The wire carries structure, never a label** — the page's language is
  client state. Do not "simplify" `pinned_by` back to strings.
- **The terminal prints `Error()` verbatim**, lowercase and without a final
  period, like every other Go error string the TUI shows. If a surface needs
  prose, it styles the sentence; it must not re-spell the facts.
- **The e2e home is shared by the whole run**: anything a spec writes
  install-wide must be cleared by that spec. `settings-goal-check-model.spec.ts`
  is the exemplar.
- Reviewer scrutiny: the Chinese default-only sentence must stay
  byte-identical; `name` must be omitted (not empty) for the two install-wide
  kinds; `pinned_by` must stay sorted by the English label; the page must
  show 子代理/计划评审/目标评估 in zh and the API's English words in en.
- Deferred: the row-level badge that reads this payload is plan 348.
