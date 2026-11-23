# Plan 261: 选模型按行 id，选择器带出 base url 与 api path

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行
> 的状态改成 DONE，并附一句执行摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git diff --stat 0a698bf2..HEAD -- pkg/turn/models.go pkg/turn/model.go pkg/turn/executor.go pkg/turn/approval.go pkg/run/config.go pkg/run/subagent.go pkg/run/subagent_catalog.go pkg/tui/chat_slash.go pkg/tui/chat_session.go pkg/gateway/slash_handlers.go pkg/gateway/api_extra.go pkg/process/run_executor.go frontend/src/components/chat/ModelChip.vue frontend/src/lib/api.ts`
> → 只允许出现 plan 260 改过的 `pkg/turn/models.go`、`pkg/run/config.go`。其它文件有变化就把「Current state」摘录逐段对照，对不上即 STOP。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED（会话的模型选择、子代理钉模型、/model 都经过这里；旧行有回退）
- **Depends on**: plans/259-llm-model-and-variable-tables.md（`SessionModelSelection.ModelID`）、plans/260-runtime-reads-global-model-list.md（`ConfiguredLLMs`、`AgentLLMProviderConfig.ID`、`ResolveModelRef`）
- **Category**: migration
- **Planned at**: commit `0a698bf2`, 2026-11-07
- **Executed**: 2026-11-08, worktree `.worktrees/exec-259-268`, 基线 `d9c45060`（未提交）——**DONE**，判据实测：
  `go build ./...` 0、`pkg/turn`/`pkg/run`/`pkg/gateway`/`pkg/tui`/`pkg/process`/`pkg/config` 全包 ok、
  前端 `pnpm typecheck` 0 与 `pnpm test` 175 文件 / 1370 用例全过、`test-db.sh check` 0、gofmt 空、
  两条 done grep 无输出、`ModelID` 在五个文件里都在。
  新增用例（计划 Test plan 1–8）：`TestModelPickerTellsTwinRowsApart`、`TestChooseReasoningEffortKeepsTheTwinRowItPicked`、
  `TestChooseModelRefusesALabelThatNamesTwins`、`TestExecModelMarksALegacyRowByItsPair`（pkg/turn）、
  `TestSessionModelRowFallsBackWhenItsRowIsGone`（pkg/run）、`TestSubagentModelPinReachesItsRowsEndpoint`（pkg/run，两个真实 SSE 端点，
  断言只打中第二条）、`TestChatSessionModelEndpointsPickTheRow`（pkg/gateway：id 选中、只给 provider+model → 400
  `several models match this provider and model; send model_id`、未知 id → 400）、前端 ModelChip 的 twin 用例。
  **有意的断言变更**：① `pkg/gateway/api_extra_test.go` 的 `TestChatSessionModelEndpoints` 期望值加上解析出的 `ModelID`
  （Step 2 的回填语义）；② `pkg/run/subagent_catalog_test.go` 的 `…SplitsProviderAndModel` 改名 `…ResolvesTheConfiguredRow`
  并改为「引用必须解析到一行、解析不到就不钉」；③ `pkg/run/typedsubagent_llm_test.go` 五处调用适配 5 返回值；
  ④ `pkg/run/{subagent,config}_test.go` 的钉模型/最近使用断言带上 id；⑤ `pkg/tui/chat_session_test.go` 增加一条真实会话行
  `ModelID` 断言；⑥ `pkg/turn/slash_test.go` 的记录器改记整个 `ModelChoice`。
  **限制**：`pkg/turn` 内无法直接观察到会话行写入（写入在 surface），因此计划项 1 在 `pkg/turn` 只断言交给 `SelectModel`
  的 `ModelChoice.ID`，真实行写入由 pkg/tui 跨进程用例与 pkg/gateway 项 7 用例覆盖；`url_verbatim` 是表专属列（`yaml:"-"`），
  picker 的 verbatim 说明由 `ModelEndpointLabel` 直测覆盖。全量 `./...` 判据同 259/260：基线三条 architecture 红不可满足，
  受影响的包逐个跑全绿。worktree 里跑前端全量测试需要主树那份被 gitignore 的
  `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/config-db-settings-preview.html`（`settingsSchema.test.ts` 以 `?raw` 引入它）。

## Why this matters

owner 2026-11-07：同一 provider + model 可以配多条（不同 base url / api path / key），并且「模型下拉框和 picker 不能只
显示 provider/model，必须把 provider/model/base url/api path 都显示出来」。

今天一次「选模型」在每一层都只带 `provider` + `model`：`/model` 选择器的值是 `"provider/model"` 字符串
（`pkg/turn/executor.go:818`），会话行存 provider/model，网关 `PUT …/model` 按 provider+model 找选项，子代理钉模型
（`SubagentModelOverride`）也只有 provider/model。两条同 provider/model 的配置在这些地方**无法区分**，选了第二条也会跑成第一条。

本计划把「选中的是哪一行」改成**行 id**贯穿到底，provider/model 只留作显示和老数据回退；TUI 选择器每项的说明列显示
完整请求 URL（`base_url` 直拼 `api_path`，自定义协议只显示 `base_url`）。网页只做**看不见的**改动（发 id、按 id 判当前项），下拉框的新样式在 plan 266（需原型确认）。

## Current state

- `pkg/turn/models.go:652-666` 选项类型（plan 260 之后数据来自 `appcfg.ConfiguredLLMs(cfg)`）：

```go
type ModelChoice struct {
	// Label is what a picker shows and a choice names: "provider/model".
	Label    string
	Provider string
	Model    string
	ReasoningEfforts []string
	SupportsImages bool
}
```

- `/model` 选择器 `pkg/turn/executor.go:797-822`：`items[i] = PickerItem{Value: choice.Label, Label: choice.Label, Current: selectionMatches(currentSel, choice) || …}`；
  选中后 `chooseModel(ctx, label)`（`:868`）用 `choices[i].Label == label` 找回选项；`selectionMatches`（`:826-833`）按 provider+model 比。
  同文件 `:876`、`:942`、`:1084` 还有三处按 provider/model 组 `ModelChoice` 或比较（`grep -n "ModelChoice" pkg/turn/executor.go`）。
  `pkg/turn/approval.go:1086` 也调用 `ModelChoices`（计划审阅选审阅模型）。
- 思考强度选择器同样只带 label：`chooseModel` 给有档位的模型返回 `model-effort` 选择器，项值 `encodeEffortChoice(chosen.Label, effort, changed)`（`pkg/turn/executor.go:924`）；`chooseReasoningEffort`（`:933`）再用 `slices.IndexFunc(choices, func(c ModelChoice) bool { return c.Label == label })` 找行（`:943`）。两条同名配置时永远命中第一条——按 id 选中第二条、再选它的思考强度，会被切回第一条。
- `PickerItem`（`pkg/turn/model.go:390-395`）已有 `Description` 字段；TUI 把它渲染成第二列（`pkg/tui/overlays.go:5356`
  `b.selectableColumns(..., item.Label, nameWidth, strings.TrimSpace(item.Description))`），过滤也搜它（`:5305`）。
- 运行时选择状态 `pkg/run/config.go:1702-1707`：`type PrimaryModelState struct { Provider, Model, Effort string; Set bool }`；
  选择器 `type modelSelector struct { provider, model string }`（`:130-133`）；`resolveAgentProvider`（`:148-194`）按它匹配；
  `ResolveSessionModelChoice(r, provider, model string, effort *string)`（`:2189-2209`）；`ConfiguredDefaultSelection`（`:2455`）；
  `sessionModelRowState`（`:2475` 起，读会话行再解析）；`NewSessionModel`（`:2085-2100`，读 `LatestSessionModelSelection`）；
  `:2343` 一次 `SaveSessionModelSelection` 写入。
- 会话行写入的三个点：`pkg/tui/chat_slash.go:697`（TUI `ChatSession.SelectModel`，`:667`）、`pkg/gateway/slash_handlers.go:288`（网关 `Server.SelectModel`，`:270`）、`pkg/run/config.go:2343`。
- 子代理钉模型 `pkg/run/subagent.go:3899-3903` `type SubagentModelOverride struct { Provider, Model, Effort string }`；
  构造点 `:1798-1814`、`:2216`、`:3886`、`pkg/process/run_executor.go:440`；客户端 `ConfiguredModelClientWithEffort(cfg, agentType, provider, model, effort)`（`:4011`）。
  每个代理给子代理类型配的模型 `SubagentCatalog.Model(name)`（`pkg/run/subagent_catalog.go:154-165`）把 `entry.Model`（"provider/model"）按第一个 `/` 切开。
- 网关会话模型接口：选项 `sessionModelOptions`（`pkg/gateway/api_extra.go:1849-1866`，DTO `sessionModelOption{Provider, Model, Label, Efforts, SupportsImages}`），
  保存 `:1990-2030`：按 `strings.EqualFold(option.Provider, provider) && strings.EqualFold(option.Model, model)` 找选项。
- 网页 `frontend/src/components/chat/ModelChip.vue`：`isCurrent` 按 provider+model 比（`:79-83`），`choose` 发
  `forebrainApi.saveSessionModel(sessionId, { provider, model, effort: '' })`（`:96-102`）；类型 `SessionModelOption`、`SessionModelSelection` 在 `frontend/src/lib/api.ts`（`grep -n "SessionModelOption\|SessionModelSelection" frontend/src/lib/api.ts`）。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `go build ./...` | exit 0 |
| 后端测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/... ./pkg/run/... ./pkg/tui/... ./pkg/gateway/... ./pkg/process/... -count=1` | ok |
| 全量 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | ok |
| 前端类型 | `cd frontend && pnpm typecheck` | exit 0 |
| 前端单测 | `cd frontend && pnpm test -- ModelChip ChatView` | 全过 |

## Scope

**In scope**: Current state 里列出的 Go 文件与对应 `_test.go`；`frontend/src/components/chat/ModelChip.vue`、`frontend/src/lib/api.ts`（只加字段）、`frontend/src/views/ChatView.vue`/`WorkshopView.vue`（只在它们按 provider+model 比较或回填 pick 的地方改成 id）。

**Out of scope**:
- 网页下拉框的视觉/布局（plan 266，需原型）。
- 子代理配置页、记忆设置页的模型下拉（plan 265/266 改接口与界面）。
- `fb_session_image_refusals` 继续按 provider+model。
- `fb_messages.model`、usage 记账里的 provider/model 字符串。

## Steps

### Step 1: 选项与选择状态带上 id

1. `turn.ModelChoice` 加 `ID, BaseURL, APIPath string`；`ModelChoices` 从 `ConfiguredLLMs` 的条目填这三项（`entry.ID`、`entry.BaseURL`、`entry.APIPath`）。`Label` 仍是 `llm.FormatProviderModel(provider, model)`。
2. `turn.ModelSelectionState` 加 `ModelID string`；`run.PrimaryModelState` 加 `ModelID string`。所有构造它们的地方（`grep -rn "PrimaryModelState{\|ModelSelectionState{" --include='*.go' pkg | grep -v _test`）把 id 带上：来源是解析出的 `entry.ID`。
3. `modelSelector` 加 `id string`。`resolveAgentProvider`：`sel.id` 非空 → 在 `ConfiguredLLMs(cfg)` 里按 `ID` 精确找，找不到返回 `errModelNotConfigured`（**不**回退到 provider/model——id 指定了就是那一行）；`sel.id` 为空时保持现有逻辑。
4. 新增 `ResolveSessionModelChoiceRef(r *Runner, modelID, provider, model string, effort *string) (PrimaryModelState, error)`：`modelID` 非空用 id，否则走现有 provider/model。现有 `ResolveSessionModelChoice` 改为调用它（`modelID` 传 `""`），签名不变以免波及。
5. `sessionModelRowState`、`NewSessionModel`、`RowLessSessionModel` 读到会话行后：`row.ModelID` 非空 → 按 id 解析，**解析失败（行被删）再按 provider/model 回退**；回退成功的结果带上新解析出的 `ModelID`。

**Verify**: `go build ./... && CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -run 'Session|Model|Config' -count=1` → ok。

### Step 2: 会话行写入 `model_id`

三个写入点都把解析出的 id 写进 `state.SessionModelSelection.ModelID`：
- `pkg/tui/chat_slash.go:697`（`ChatSession.SelectModel`）：`choice.ID`；
- `pkg/gateway/slash_handlers.go:288`（`Server.SelectModel`）：`choice.ID`；
- `pkg/run/config.go:2343`：它保存的是「实际用到的模型」，从同函数里的 `PrimaryModelState.ModelID` 取。

`choice.ID` 为空时（只可能来自测试或老调用方）：在写之前用 `ResolveModelRef(cfg, agentName, provider+"/"+model)` 补出 id；补不出就写空串（读时回退 provider/model）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/ ./pkg/gateway/ -run 'Model|Slash' -count=1` → ok。

### Step 3: `/model` 选择器按 id，说明列显示完整请求 URL

`pkg/turn/executor.go` `execModel`：

```go
items[i] = PickerItem{
	Value:       choice.ID,
	Label:       choice.Label,
	Description: modelEndpointLabel(choice.BaseURL, choice.APIPath, choice.URLVerbatim),
	Current:     selectionMatches(currentSel, choice) || (!currentSel.Set && i == defaultAt),
}
```

- `modelEndpointLabel(base, path, verbatim)`：`verbatim`（「自定义协议」，请求发到 base 本身）→ `base`；否则两段都非空 → `base + path`（**直拼、不加分隔**，例如 `https://api.deepseek.com/chat/completions`）；只有 base → base；都空（登录类 provider）→ `""`。`ModelChoice` 同时加 `URLVerbatim bool`。放在 `pkg/turn/models.go`，导出为 `ModelEndpointLabel`（网关与 TUI 之后也用）。
- `selectionMatches`：`sel.ModelID` 与 `choice.ID` 都非空 → 比 id；否则保持 provider+model 比较（老行）。
- `defaultModelIndex`（`pkg/turn/models.go`）：按 `entry.ID == choice.ID` 找。
- `chooseModel(ctx, value)`：先按 `c.ID == value` 找；找不到再按 `c.Label == value`（兼容未刷新的网页/旧客户端仍把选择器的 label 当 value 送回来的 `turn.Choose`）——
  **按 label 命中多条时**不要选第一条，回复：`"Several models are configured as <label>; pick one from /model."`。
- `:876`、`:1084` 以及 `pkg/turn/approval.go:1086` 附近按 provider/model 组 `ModelChoice` 或比较的地方，同样改成「有 id 比 id」。`:1084` 构造 `ModelChoice{Provider, Model}` 的地方补 `ID: settings.LiveSelection.ModelID`。
- **思考强度选择器的值也带 id**（否则上面「有 id 比 id」那处在 `:942` 无从执行）：`encodeEffortChoice`/`decodeEffortChoice`（`pkg/turn/executor.go:967-978`）改成四段 `id\nlabel\neffort\nchanged`，解码仍接受三段旧格式（未刷新的网页/旧客户端会送）；`chooseModel` 生成 effort 项时改调 `encodeEffortChoice(chosen.ID, chosen.Label, effort, changed)`（`:924`）；`chooseReasoningEffort`（`:933`）先按 `c.ID == id` 找行，id 为空或找不到再按 `c.Label == label`，**按 label 命中多条时**不取第一条，回与 `chooseModel` 同一句 `"Several models are configured as <label>; pick one from /model."`，不切换。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/... -count=1` → ok，并新增测试（见 Test plan 1–3）通过。

### Step 4: 子代理钉模型带 id

1. `SubagentModelOverride` 加 `ModelID string`；`valid()` 不变（仍以 Model 非空为准）。
2. `SubagentCatalog.Model(name)`（`subagent_catalog.go:154`）改成返回 `(modelID, provider, model, effort string, ok bool)`：
   用 `appcfg.ResolveModelRef(cfg, agentName, entry.Model)` 解析（`entry.Model` 可能是 id，也可能是旧的 "provider/model"）。
   `SubagentCatalog` 若没有持有 cfg 与代理名，就在构造它的 `SubagentCatalogFor(cfg, agentName)` 里存下来。所有调用点
   （`grep -rn "\.Model(agentType)\|cat.Model(" --include='*.go' pkg`）跟着改。解析不到时 `ok=false`（与今天「没配」同义，回落会话的模型）。
3. 构造 override 时带上 `ModelID`，来源按构造点：`subagent.go:1798-1814`（catalog 解析结果；`AgentModel(...)` 那支用 `ResolveModelRef` 补）；`:2216`（`record.ModelProvider`/`record.Model`）与 `:3886`（`prep.entry.ModelProvider`/`prep.entry.Model`）——record/roster 条目今天**没有** id 字段，统一用 `appcfg.ResolveModelRef(cfg, agentName, provider+"/"+model)` 现场解析补出 id（解析不到就留空，读时回退 provider/model）；`pkg/process/run_executor.go:440` 同法。
4. `ConfiguredModelClientWithEffort` 加参数 `modelID`（放在 `agentType` 之后），非空时按 id 解析；`wrapSubagentModelOverrideLLM` 的回调（`pkg/run/runner.go` 里 `ConfiguredModelClientWithEffort(r.AppCfg, …, override.Provider, override.Model, override.Effort)`）传 `override.ModelID`。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -run 'Subagent' -count=1` → ok。

### Step 5: 网关会话模型接口带 id 与端点

1. `sessionModelOption` DTO 加 `ID string json:"id"`、`BaseURL string json:"base_url,omitempty"`、`APIPath string json:"api_path,omitempty"`；`sessionModelOptions` 从 `choice` 填。会话当前选择**没有独立 DTO**——`sessionModelSelection` 是 `Server` 的方法（`slash_handlers.go:256`），序列化点是两处内联 map：GET 选项响应（`api_extra.go:1909-1914`，含 `"options"`）与 PUT 保存响应（`api_extra.go:2032-2036`）；两处都加 `"model_id": sel.ModelID`（同文件 `handleChatModelDefault` `:1922` 起的同形状响应一并加）。
2. 保存接口（`api_extra.go:1990-2030`）的请求体加 `ModelID string json:"model_id"`：
   - `model_id` 非空 → 按 id 在选项里找，找不到 400 `model is not offered for this agent`；
   - 为空 → 按 provider+model：命中 0 条 400（原文案），**命中多条 400 `several models match this provider and model; send model_id`**，命中 1 条照旧。
3. 网关测试：找到现有的会话模型保存测试（`grep -rln "saveSessionModel\|/model\"" pkg/gateway/*_test.go`），加：两条同 provider/model 不同 base_url → 按 id 能分别选中、会话行 `model_id` 对；只给 provider+model → 400 多条。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -count=1` → ok。

### Step 6: 网页只改「看不见的」部分

- `frontend/src/lib/api.ts`：`SessionModelOption` 加 `id: string; baseUrl?: string; apiPath?: string`，`SessionModelSelection` 加 `modelId?: string`（此仓库经 `frontend/src/lib/case.ts` 的 `toCamelCase`/`toSnakeCase`（api.ts 顶部 import）把响应键转小驼峰，按 api.ts 既有写法）；`saveSessionModel` 的参数类型加 `modelId?: string`，请求体发 `model_id`（照同文件其它请求体的 snake_case 写法）。
- `ModelChip.vue`：`isCurrent` 先比 `option.id === props.selection?.modelId`（两边都有时），否则沿用 provider+model；`choose` 发 `{ modelId: option.id, provider, model, effort: '' }`；`v-for` 的 `:key` 改成 `option.id`；`data-testid` 不变。
- `ChatView.vue` / `WorkshopView.vue` 里接 `@pick` 后在首条消息时写模型的地方，带上 `modelId`（`grep -n "saveSessionModel\|onModelPick\|pick" frontend/src/views/ChatView.vue | head`）。
- **不改任何可见文案和布局。**

**Verify**: `cd frontend && pnpm typecheck && pnpm test -- ModelChip ChatView` → 全过（既有用例按需补 `id` 字段到测试数据里）。

### Step 7: 全量

**Verify**: `go build ./... && CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → ok；`cd frontend && pnpm typecheck && pnpm test` → ok；`./scripts/test-db.sh check` → exit 0。

## Test plan

1. `pkg/turn`：两条同 `deepseek/deepseek-v4-flash`、不同 base_url 的配置 → `/model` 选择器两项 `Value` 不同、`Description` 分别是各自的完整请求 URL（`base + path`）；选第二项后会话行 `model_id` 是第二条。
2. `pkg/turn`：同一对配置 → 先 `Choose` 用**第二条的 id** 选中它（触发 `model-effort` 选择器），再用该选择器的 `Value`（四段、带 id）选 `high` → 会话行 `model_id` 仍是第二条、`effort=high`，不是第一条。负向对照：用三段旧格式（无 id）且 label 命中两条 → 回 `Several models are configured as …`，不切换。
3. `pkg/turn`：`turn.Choose` 收到 label 值（未刷新的网页/旧客户端）且 label 命中两条 → 回复包含 `pick one from /model`，不切换。
4. `pkg/turn`：会话行只有 provider/model（`model_id` 为空，模拟老行）→ 当前项按 provider/model 标出。
5. `pkg/run`：会话行 `model_id` 指向被删的行 → 回退 provider/model 成功，结果 `ModelID` 是新行 id。
6. `pkg/run`：子代理类型配置 `model: <id>` → 钉住的 override `ModelID` 是它，客户端请求打到该行的 base_url（用 `httptest.Server` 当两个 base_url，断言请求落在第二个）。
7. `pkg/gateway`：Step 5 的两例。
8. 前端：`ModelChip` 两个同 provider/model 选项，`selection.modelId` 为第二个 → 只有第二个是 current；点选发出的请求体带 `model_id`。

模式：`pkg/turn` 照 `grep -ln "execModel\|chooseModel" pkg/turn/*_test.go` 找到的现有用例写；前端照 `frontend/src/views/ChatView.test.ts` 里 ModelChip 相关用例写。

## Done criteria

- [ ] `go build ./...` 与 `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` exit 0
- [ ] `cd frontend && pnpm typecheck && pnpm test` exit 0
- [ ] `grep -n "Value: choice.Label" pkg/turn/executor.go` 无输出
- [ ] `grep -n "encodeEffortChoice(chosen.Label" pkg/turn/executor.go` 无输出（思考强度值带 id）
- [ ] `grep -n "ModelID" pkg/state/session_store.go pkg/run/config.go pkg/run/subagent.go pkg/gateway/slash_handlers.go pkg/tui/chat_slash.go` 每个文件都有
- [ ] 新增测试 1–8 存在并通过
- [ ] `git status --short` 只有 Scope 内文件
- [ ] `plans/README.md` 本行已更新

## STOP conditions

- `SubagentCatalogFor` 的构造点拿不到当前代理名（需要改很多签名才能拿到）。
- 会话行的写入点比上面三个多（例如 fork/copy 路径另有一处拼 SQL 写 provider/model）。
- 网页 `saveSessionModel` 的请求体在别处还有调用方且语义不同。

## Maintenance notes

- 「有 id 比 id，没 id 比 provider/model」的双轨只为老会话行存在；plan 262 会把老行的 `model_id` 回填，之后回退分支只在「行被删」时触发。
- 选择器的 `Value` 现在是不透明的行 id；思考强度选择器的 `Value` 是 `id\nlabel\neffort\nchanged` 的私有编码（`encodeEffortChoice`/`decodeEffortChoice` 一手编一手解），任何把 `Value` 当 "provider/model" 解析的新代码都是 bug。
- `ModelEndpointLabel` 是 TUI 与网关共用的「端点」写法；网页显示由 plan 266 定，两边保持同一信息量（provider/model + 完整请求 URL：`base_url` 直拼 `api_path`）。
