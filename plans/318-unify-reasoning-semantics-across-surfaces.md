# Plan 318: One reasoning contract for engine, TUI, gateway and web

> **Executor instructions**: follow the steps in order; run every verification
> command and compare the output before moving on. On any STOP condition, stop
> and report — do not improvise. When done, update this plan's row in
> `plans/README.md` unless your dispatcher said they maintain the index.
>
> **Drift check (run first)**:
> ```sh
> cd /Users/doudou/workspace/unionj-cloud/forebrain-harness
> git rev-parse --short HEAD            # planned at 1013b50f; 316 landed aace268f — start from the dispatch HEAD
> git log --oneline 1013b50f..HEAD -- pkg/llm pkg/turn pkg/run/config.go pkg/gateway pkg/tui   # expect 311/313/305 only; re-anchor by symbol
> git status --porcelain -- pkg/llm pkg/turn pkg/run/config.go pkg/gateway/models_api.go pkg/gateway/api_extra.go pkg/gateway/subagents_api.go pkg/tui/run.go pkg/tui/reducer.go pkg/tui/setup.go pkg/config/agents.go frontend/src/lib/effortLabel.ts frontend/src/locales/index.ts
> grep -n 'func trimToSendable' -A 40 pkg/turn/models.go
> grep -n 'func llmModelCatalogCaps' -A 26 pkg/gateway/models_api.go
> grep -n 'Reasoning: shared.ReasoningParam' -A 5 pkg/llm/openai/responses_llm.go
> grep -n 'func sessionModelEfforts' -A 30 pkg/gateway/api_extra.go
> grep -n 'func requestReasoningEffort' -A 12 pkg/run/config.go
> ```
> Expected: the functions named above exist with the content quoted in
> "Current state". Line numbers may drift; **content mismatch is the STOP
> condition, moved lines are not**. Another agent is editing `pkg/tui/setup.go`
> and `pkg/tui/config_panel*` right now — this plan does **not** touch those
> files (only `pkg/tui/run.go` and `pkg/tui/reducer.go` for the footer label).
> The parallel `pkg/tui/setup.go` / `assets/models.json` / `scripts/test-db.sh`
> work is someone else's; never revert or commit it.

## Status

- **Priority**: P1 (owner 2026-11-17: "确认底层引擎、tui端、gateway和web端…语义是否完全统一…必须补齐")
- **Effort**: L (one engine helper + four consumers + footer label + validation; tests in 6 packages)
- **Risk**: MED (changes what a "Default" request sends on the Responses protocol, and makes the models page stop advertising levels the client cannot send)
- **Depends on**: `plans/311` (it introduces the Off three-state contract and the `Provider default` / `none` semantics this plan reuses; it also edits `llmModelCatalogCaps`), `plans/313` (config rows / subagent contract)
- **Category**: bug fix + consistency
- **Planned at**: `1013b50f`, 2026-11-17
- **Dispatch**: **owner approved on 2026-11-17** ("318/319 已批准派发").
  **Blocked by this plan's own STOP condition until 311 and 313 are in `dev`** —
  311 creates `turn.SetupReasoningEfforts` and edits `llmModelCatalogCaps`, 313
  edits the gateway/config surfaces this plan touches. Do not start before both
  land; do not create a second helper.

## Why this matters

The owner asked for one answer to two questions on every surface: **can this
model's thinking be turned off, and which discrete effort levels can it be set
to?** Today the answer is computed in five places with five different inputs,
and they disagree in ways a user can see:

| # | What the user sees | Where |
|---|---|---|
| F1 | The models page lists levels the request client will drop (a custom row with `api_path=/v1/messages`, or an OpenCode Google model) | `pkg/gateway/models_api.go:299-318` uses `hit.ReasoningEfforts()`; only `pkg/turn/models.go:206-237` trims by protocol |
| F2 | "Which client will send this" is decided from `api_path`, but "which levels can be sent" is decided from `provider` + `ProtocolFor` | client: `pkg/run/config.go:490-519`; trim: `pkg/turn/models.go:217-236` |
| F3 | `Default` sends nothing on chat/completions, but sends `reasoning.effort=medium` on the Responses protocol — the same model, only `api_path` differs | `pkg/llm/openai/responses_llm.go:991-994` vs `pkg/llm/openai/compat_llm.go:823-838` |
| F4 | A model the catalog knows with **no** discrete levels (toggle/budget, or a Google row) hides the models drawer's level checkboxes, although the row may legally fill them in (vendor-wins gap-filler) | `pkg/gateway/models_api.go:875-884` sets `EffortsKnown=true` for any successful `ResolveModelInfo`; `:459-466` does the same for the row list; engine rule at `pkg/llm/catalog.go:900-902` |
| F5 | The same model's levels appear in catalog file order in the subagent drawer and the /goal picker, and in `none,minimal,…,max` order in `/model`, the gate and the chat chip | `pkg/gateway/api_extra.go:1845-1871` returns unsorted; `pkg/turn/models.go:250-277` sorts; web chip sorts client-side (`frontend/src/components/chat/EffortChip.vue:72-81`), the drawer does not (`SubagentConfigDrawer.vue:415-425`) |
| F6 | With two rows naming the same provider/model, the session-model PUT validates against the **first** option's levels, not the selected row's | `pkg/gateway/api_extra.go:1913-1922`, `:2096`; `/model` uses the row id (`pkg/turn/executor.go:989-1000`) |
| F7 | The TUI footer shows `xhigh` / `none` while `/model` and the web show `xHigh` / `Off` | `summarizeComposerReasoningEffort` (`pkg/tui/run.go`), and the footer joins in `pkg/tui/reducer.go` (`composerFooterSidesLocked` / `subagentFooterLeftLocked`) use the raw `CurrentModelReasoningEffort()` |
| F8 | The "TUI labels equal web labels" test pins a **duplicated literal table**, so the web locale can drift silently | `pkg/turn/model_test.go:1153-1168` vs `frontend/src/lib/effortLabel.ts:16-38`, `frontend/src/locales/index.ts:2115-2124` / `:4643-4652` |
| F9 | A hand-written subagent level that the model cannot send is stored and then silently dropped (Anthropic) or sent verbatim (OpenAI-compatible) | write: `pkg/gateway/subagents_api.go:769`, `:837`; the only rule is `pkg/config/agents.go:313-328`; client drop: `pkg/llm/anthropic/agent_llm.go:88-92` |
| F10 | A Google-protocol row's `default_effort` / `reasoning.effort` is folded into params and then thrown away — the Google client takes no params | `pkg/run/config.go:686-689`; `pkg/llm/google/agent_llm.go:50` |

What is **not** a finding (documented decisions — do not "fix" these):

- `Default` is not offered when the row configures a level
  (`pkg/turn/executor.go:935-937`; plan 300 §"待 owner 确认" ①). Owner chose
  "pre-select the configured level, do not list a duplicate default".
- Anthropic-protocol models never offer `Off`: the Messages client has no
  thinking-disabled wire path (`pkg/llm/anthropic/agent_llm.go:110-133`).
  Plan 311 §2.1 records this as an honest boundary, not a bug.
- `Provider default` stays the label for "send no effort parameter" in the
  `/connect` row-default page (it is not the same thing as a subagent's
  "Row default"). Plan 311 §2.1 and the owner-confirmed prototype are
  authoritative.
- The caps write path replacing the whole `caps` object (omitted field = clear)
  is documented in plan 311 §2.2 item 11 and is out of scope here.
- A subagent row's `""` means "follow the model row's default" while a
  session's `""` means "pin no effort". They are different objects; plan 310
  §0.2 already settled that the gate always submits a concrete value. Do not
  collapse them.

## The contract this plan enforces

1. **One level set per (row, protocol).** Discrete levels come from
   `assets/models.json` `reasoning_options` (`effort.values`, whitelist
   `none/minimal/low/medium/high/xhigh/max`), a row's `cap_efforts` only when
   the catalog has none or the provider is login-backed (owner 2026-11-16),
   and are then trimmed to what the **actual client** can send.
2. **One protocol decision.** The decision that picks the client
   (`pkg/run/config.go`) and the decision that trims levels must be the same
   function, and it must consider `api_path`; `provider` alone is not enough.
3. **`none` is Off, `""` is Default.** `none` is a wire value that is sent
   literally when the protocol takes it. `""` means no effort parameter at
   all — on every protocol, including Responses.
4. **One order, one label table.** `none, minimal, low, medium, high, xhigh,
   max` (unknown levels appended in file order); `Off/Minimal/Low/Medium/
   High/xHigh/Max` and `Default` for `""`. Every surface uses
   `turn.OrderedEfforts` / `turn.EffortEnglishLabel` (or, on the web, the
   locale keys those labels are pinned to).
5. **A capability answer equals the picker.** For the same row, the models
   page, the chat chip, `/model`, the gate and the subagent picker must show
   the same levels.

## Scope

**In scope**:
- `pkg/llm/catalog.go` (exported protocol helper only if it cannot live in
  `pkg/turn`), a new small `pkg/llm/protocol.go` if needed
- `pkg/turn/models.go` (`SetupReasoningEfforts` exported; ordering helper exported or
  reused)
- `pkg/run/config.go` (client switch uses the shared protocol helper; Responses
  construction unchanged otherwise)
- `pkg/llm/openai/responses_llm.go` (no hardcoded effort)
- `pkg/gateway/models_api.go` (sendable caps, `efforts_known`)
- `pkg/gateway/api_extra.go` (ordered options, row-id validation)
- `pkg/gateway/subagents_api.go` (level validation)
- `pkg/tui/run.go`, `pkg/tui/reducer.go` (footer label)
- tests in the packages above + `pkg/turn/model_test.go` cross-language pin

**Out of scope** (do not touch):
- `pkg/tui/setup.go`, `pkg/tui/config_panel*` (plans 311/313 own them; another
  agent is editing them now)
- `frontend/src/**` production code (the web already renders whichever list
  the API returns; only its tests may be extended if a shared fixture is
  needed — prefer no frontend change)
- The caps write path's whole-object replace, the Anthropic off runtime,
  ChatGPT/OpenCode account listings, `assets/models.json`, prototypes

## Steps

### Step 1: one protocol decision, one sendability filter

Current state (`pkg/turn/models.go:206-237`): `trimToSendable(provider, model,
levels)` decides "Anthropic" from `provider ∈ {anthropic, minimax}` or
`opencode.ProtocolFor(provider, model)`, and "Google" from the same
`ProtocolFor`. It never looks at the row's `api_path`.

Current state (`pkg/run/config.go:490-519`): the client switch decides from
`prov == "anthropic" || prov == "minimax"`, then `isAnthropicMessagesPath(apiPath)`,
then `strings.Contains(apiPath, "/responses")`, else Chat Completions.

1. Add the shared predicate. Preferred home: `pkg/llm` (layer 0, both `turn`
   and `run` may import it). Signature:
   ```go
   // ClientProtocol names the request shape a configured row speaks: the
   // provider's own protocol first (anthropic/minimax, opencode's per-model
   // table), then the row's api_path (/v1/messages, /responses), else Chat
   // Completions. "" means "no opinion" and callers keep their existing
   // default.
   func ClientProtocol(provider, model, apiPath string) Protocol
   ```
   with `Protocol` constants `ProtocolAnthropic`, `ProtocolResponses`,
   `ProtocolGoogle`, `ProtocolChatCompletions` (names are illustrative; do not
   duplicate `pkg/llm/opencode`'s existing constants — alias or reuse them).
   Reuse
   `pkg/llm/opencode.ProtocolFor` for the opencode providers and
   `isAnthropicMessagesPath`'s rule (move that helper to `pkg/llm` and keep a
   thin `run` wrapper, or re-implement it there once — no duplicated string
   checks).
2. Make `trimToSendable` delegate to `turn.SetupReasoningEfforts` — the
   exported helper plan 311 Step 2 creates, with this signature:
   ```go
   func SetupReasoningEfforts(provider, model, apiPath string, levels []string) []string
   ```
   This plan owns only its body, not a second function: if 311 has not landed
   yet, create it exactly as 311 specifies; if it has, use it unchanged.
   Anthropic → keep only `anthropic.AcceptsEffort`; Google → `nil`; else copy.
   Keep `ModelReasoningEffortsRow` calling it with the row's `api_path`:
   `ModelChoice` already carries `APIPath` (`pkg/turn/models.go:768-776`) —
   pass it through. `ModelReasoningEfforts(provider, model)` keeps `apiPath=""`
   (pure catalog callers keep today's answer).
3. Update the client switch in `pkg/run/config.go` to call
   `llm.ClientProtocol(prov, model, apiPath)` and branch on the result, so the
   trim and the client can never disagree. The ChatGPT branch (`prov ==
   "chatgpt"`) and the subscription-plugin factory stay first, unchanged.
4. Feed `api_path` into the models API's capability answers (Step 3).

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/llm ./pkg/run -run 'Reasoning|Effort|Protocol' -count=1` → PASS. New table test: a custom row with `api_path=/v1/messages` and catalog levels `[minimal,low,medium,high]` yields `[low,medium,high]`; the same row with `/chat/completions` yields all four; a Google row yields `nil`.

STOP if `pkg/llm/opencode.ProtocolFor` already returns Anthropic for providers
this plan would now classify differently — report before changing the client
switch (a behavior change there is bigger than this plan).

### Step 2: `Default` sends no effort on the Responses protocol

Current state (`pkg/llm/openai/responses_llm.go:991-994`):
```go
Reasoning: shared.ReasoningParam{
    Effort:  shared.ReasoningEffortMedium,
    Summary: shared.ReasoningSummaryAuto,
},
```
and `:1012-1021` overlays `paramsOverlay` via `SetExtraFields`.

1. Build `Reasoning` with `Summary: auto` only; set `Effort` **only** when the
   row's params name one (`paramsOverlay["reasoning"]["effort"]`, or a top-level
   `reasoning_effort` if that is how the row is written — check
   `pkg/config.ParseLLMRequestParamsJSON`), and do not let an empty string
   produce `effort:""`.
2. Keep the overlay so `summary`, `encrypted_content` and other keys still
   apply. Do not add a second default effort anywhere.
3. If a provider requires `effort`, that is the provider's error to report; do
   not silently substitute `medium`.

**Verify**: new test in `pkg/llm/openai/responses_llm_test.go` capturing the
request body: with params `{}` the body has `reasoning.summary=="auto"` and
**no** `reasoning.effort`; with params `{"reasoning":{"effort":"high"}}` the
body has `effort=="high"`; with `{"reasoning":{"effort":""}}` no effort key.
Run `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/openai -run 'Responses' -count=1`.

STOP if an existing test asserts the hardcoded `medium` as a product promise
(grep `ReasoningEffortMedium` in non-test and test files) — report it instead
of editing the assertion.

### Step 3: the models page reports only what the client can send

Current state: `pkg/gateway/models_api.go:299-318` (`llmModelCatalogCaps`)
returns `hit.ReasoningEfforts()` untrimmed and takes `(provider, model,
catalogProvider)`; `:459-466` overlays live caps for a row; `:875-884` sets
`EffortsKnown = true` whenever `ResolveModelInfo` succeeds, even with
`len(info.Efforts)==0`.

1. Give `llmModelCatalogCaps` the row's `api_path` (a fifth argument or a small
   input struct — mirror how plan 311 adds its `apiPath` argument; if 311 has
   already landed, extend its signature rather than adding a parallel
   function). Return the levels through `turn.SetupReasoningEfforts`.
2. Set `EffortsKnown` only when the resulting level list is **non-empty**.
   A model the catalog knows with no sendable discrete levels must answer
   `efforts_known=false` + empty `efforts`, which is exactly the state the web
   drawer renders as "tick the levels" (`ModelCapsFields.vue:61`), matching
   the engine's gap-filler rule (`pkg/llm/catalog.go:900-902`).
3. The row-list endpoint (`GET /api/models`) must use the same answer for
   `catalog_caps` and for `llmModelApplyLiveCaps` (`:343-351`): live info's
   levels win, but only after the sendability filter, and `EffortsKnown`
   follows the non-empty rule.
4. `llmModelCapsValid` (`:1078-1110`) must validate `caps.default_effort` and
   `caps.efforts` against the sendable set for the row's `api_path` (plan 311
   already adds a sendable filter for `none`; extend it to every level).

**Verify**: extend `pkg/gateway/models_api_test.go`:
- an Anthropic-protocol row whose catalog lists `minimal` answers without it;
- a Google-protocol row answers `efforts=[]`, `efforts_known=false`;
- a toggle-only catalog row answers `efforts=[]`, `efforts_known=false`, and a
  `PUT` with `caps.efforts=["low"]` is accepted (the engine gap-filler);
- a vendor-with-levels row still refuses a hand-written different set.
Run `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'LLMModel|Caps' -count=1`.

### Step 4: gateway options are ordered and row-addressed

Current state: `sessionModelEfforts` (`pkg/gateway/api_extra.go:1845-1871`)
returns record or fallback levels in file order; `sessionModelEffortsFor`
(`:1913-1922`) matches the first option by provider+model; the PUT
(`:2088-2101`) validates the level against that.

1. Sort every returned list through the shared order helper. Export
   `turn.OrderedEfforts([]string) []string` (thin wrapper over the existing
   `orderedEffortLevels`) and use it in `sessionModelOptions` and
   `sessionModelEfforts`.
2. When the PUT body names `model_id`, validate the level against **that**
   option's levels; only fall back to provider+model when no id was sent, and
   refuse ambiguity instead of picking the first.
3. Keep the empty-string rule (`\"\"` = explicit Default) unchanged.

**Verify**: new test with two rows on one provider/model (different
`base_url`, different levels): PUT with `model_id` of the second row accepts a
level only the second row has, and rejects a level only the first row has.
Plus an ordering test: options come back `none,low,high`, not file order.
`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'SessionModel' -count=1`.

### Step 5: the TUI footer uses the same label table

Current state: `summarizeComposerReasoningEffort` (`pkg/tui/run.go`) returns
`strings.TrimSpace(session.CurrentModelReasoningEffort())`; the footer joins in
`pkg/tui/reducer.go` (`composerFooterSidesLocked` and
`subagentFooterLeftLocked`) use it as-is. 316 moved the lines — anchor by
symbol, not line.

1. At the two footer call sites (main and subagent view), pass the value
   through `turn.EffortEnglishLabel`. Keep `Default` for `""` (the footer
   currently drops empty segments via `joinFooterSegments`, so `Default` will
   not appear unless a caller opts in — preserve today's "segment disappears
   when empty" behavior; only map non-empty levels: `none→Off`,
   `xhigh→xHigh`, others unchanged).
2. Do not change the stored value, the spawn payload, or `AgentModel`.

**Verify**: update/extend the footer tests (`pkg/tui/run_test.go` /
`reducer_test.go` where the footer is asserted) with `xhigh` and `none`; the
rendered footer must contain `xHigh` / `Off` and must not contain the raw ids.
`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Footer|Composer' -count=1`.

### Step 6: a subagent level must be one the model can send

Current state: `pkg/gateway/subagents_api.go:769` and `:837` store
`strings.TrimSpace(in.ReasoningEffort)`; `pkg/config/agents.go:313-328` only
requires a model.

1. On both write paths, when the entry names a model that resolves to a
   configured row, resolve the row's sendable levels (`turn.SetupReasoningEfforts`
   with the row's `api_path`) and refuse a non-empty level outside that set
   with the existing refusal shape (zh + en). Empty stays valid.
2. When the model cannot be resolved (a bare `provider/model` not in the row
   table), do not guess: keep today's behavior and leave validation to the
   dispatch client. Record that case in the code comment.
3. Do not add load-time validation in `pkg/config` (it has no row table);
   the gateway write path is the one place with both facts.

**Verify**: `pkg/gateway/subagents_api_test.go`: a level outside the row's
levels is a 400 with the refusal; the same level on a model with no row is
accepted (today's behavior); empty is accepted.
`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'Subagent' -count=1`.

### Step 7: pin the label table against the real web source

Current state: `pkg/turn/model_test.go:1153-1168` compares
`EffortEnglishLabel` with a literal Go map.

1. Replace the literal map with a test that reads
   `../../frontend/src/locales/index.ts` and extracts
   `'chat.effortDefault'`, `'chat.effortNone'`, `'chat.effortMinimal'`,
   `'chat.effortLow'`, `'chat.effortMedium'`, `'chat.effortHigh'`,
   `'chat.effortXhigh'`, `'chat.effortMax'` from both the zh block and the en
   block (a small regexp over the file is enough; if the key names differ, use
   `grep -n "chat.effort" frontend/src/locales/index.ts` first and pin what is
   there).
2. Assert the en values equal `EffortEnglishLabel` for
   `""`/`none`/`minimal`/`low`/`medium`/`high`/`xhigh`/`max`, and the order
   in `frontend/src/lib/effortLabel.ts` `EFFORT_LEVELS` equals
   `turn.OrderedEfforts`'s base order.
3. Keep the test's failure message naming the file and the key.

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run 'EffortEnglishLabel' -count=1`
→ PASS; temporarily changing a locale value must fail the test (do this once,
then revert).

### Step 8: regression sweep

```sh
CGO_ENABLED=1 go build -tags fts5 ./...
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/turn ./pkg/run ./pkg/gateway ./pkg/tui -count=1
gofmt -l pkg/llm pkg/turn pkg/run pkg/gateway pkg/tui
CGO_ENABLED=1 go vet -tags fts5 ./pkg/llm/... ./pkg/turn ./pkg/run ./pkg/gateway ./pkg/tui
cd frontend && pnpm typecheck && pnpm vitest run src/lib/effortLabel.test.ts src/components/chat/EffortChip.test.ts
```
Expected: build exit 0; all listed packages PASS; gofmt prints nothing; vet
exit 0; frontend typecheck 0 and both test files PASS. No frontend production
file changed — if `git status` shows one, STOP and explain.

## Done criteria

- `llm.ClientProtocol` is the only place that maps (provider, model, api_path)
  to a protocol; `pkg/run`'s client switch and `turn.SetupReasoningEfforts` both call
  it (grep: no second `isAnthropicMessagesPath`/`/responses` switch).
- The Responses client sends no `reasoning.effort` when the row names none;
  `Default` and `High` produce visibly different bodies (test).
- Models page, session options, `/model`, gate and subagent picker agree on
  the level list for the four fixture shapes: Anthropic with catalog levels,
  OpenAI-compatible with catalog levels, Google, toggle-only (test matrix).
- `efforts_known` is false for a known model with no sendable discrete levels
  (test).
- Session PUT validates against the selected `model_id` when one is sent
  (test).
- TUI footer renders `xHigh`/`Off` (test).
- The Go label test reads the frontend locale file (test fails if the file's
  value changes).
- No file outside Scope changed (`git status --porcelain` reviewed hunk by
  hunk).

## STOP conditions

- Any in-scope function's current content differs from the excerpts above in a
  way that changes the contract (moved lines are fine).
- `pkg/llm/opencode.ProtocolFor` and `api_path` disagree for a provider that
  already ships (e.g. an opencode model with an explicit Messages path): report
  the case before picking a winner.
- A test asserts the Responses `medium` default as intended behavior.
- The fix would change what an existing configured row sends without the user
  having touched it in a way this plan does not name (e.g. a Google row that
  relied on its params — Step 3 must make that visible, not silent).
- The parallel plans 311/313 change the same signature this plan extends:
  rebase on their landed form first; do not create a second helper.

## Maintenance notes

- The protocol decision lives in `pkg/llm` on purpose: `pkg/turn` may not
  import `pkg/run` (layer 3 sibling), and `pkg/gateway` already imports
  `pkg/turn`/`pkg/llm`.
- `assets/models.json` remains the only level source; this plan never writes
  data into it.
- Anthropic "off" is still not expressible. If that changes, `AcceptsEffort`
  grows `none` and every surface follows automatically — do not special-case
  it here.
- The web chip's client-side sort can stay (it is idempotent once the server
  sorts); the subagent drawer inherits the server order and needs no change.
- The `/config` and web-settings effort **pickers** are plan 319, not this
  plan: 318 makes the list they will show (sendable, ordered, Off-aware) and
  validates writes; 319 changes the field kind. Run 319 after this plan.
- Reviewer must scrutinize: the Responses body change (F3) and the
  `efforts_known` flip (F4), because both change what a user can select
  without any UI edit.
