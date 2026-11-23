# Plan 300: 网页对话页选中模型时思考强度取「设置 · 模型」的配置值，并与模型一并落在会话行、打开旧会话原样回显

> **Executor instructions**：按步骤执行，每一步都跑完验证命令并确认预期输出再进入下一步。
> 遇到「STOP 条件」里任意一条就停下报告，不要自行发挥。完成后更新 `plans/README.md`
> 里本计划的状态行（除非派发你的评审者说他维护索引）。
>
> **Drift check（开工第一步）**：
> `cd /Users/doudou/workspace/unionj-cloud/forebrain-harness && git status --porcelain -- pkg/turn/models.go pkg/turn/models_test.go pkg/gateway/api_extra.go pkg/gateway/api_extra_test.go frontend/src/components/chat frontend/src/views/ChatView.vue frontend/src/views/ChatView.test.ts frontend/src/views/WorkshopView.vue frontend/src/lib frontend/src/locales/index.ts frontend/src/assets/main.css frontend/e2e/chat-composer.spec.ts`
> 写计划时（2026-11-12，`ecc4e1aa`）上述路径里**只有 `frontend/src/locales/index.ts` 有未提交改动**（别的工作留下的，不是本计划的）。
> 该文件里别人的改动一律保留，只动本计划点名的键。其余文件若已有改动，把「当前状态」的摘录与活代码比对；对不上即 STOP。
> 注意 `plans/` 被 `.gitignore` 忽略，不能用 `git status` 干净推断计划文件没动过。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: LOW（不改表结构、不改引擎解析；后端只改一个处理函数的入参语义并给选项加一个字段，其余在前端）
- **Depends on**: none
- **Category**: direction（owner 指定的行为改动）
- **Planned at**: commit `ecc4e1aa`, 2026-11-12
- **原型**: `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/llm-models-preview.html`（左栏点「对话」；说明栏「本次新增：对话页的思考强度选择器（2026-11-12）」）。**原型里列出的 5 个确认点 owner 尚未逐条回复**，见下文「待 owner 确认」；派发前由派发者确认已解除。

## Why this matters

owner 2026-11-12 的三条原话：

1. 「对话页输入框模型选择器右侧加上思考强度选择器，默认选中和回显设置-模型页给选中模型配置的思考强度」
2. 「用户在当前会话最后设置和使用的思考强度也必须跟模型一并落库」
3. 「打开之前聊过的会话，必须回显数据库里记录的当前会话最后设置和使用的模型和思考强度」

调研结论：**引擎和终端已经是这个语义，只有网页的模型按钮这一条路不是**。

- 会话行 `fb_session_model_state` 本来就把 provider / model / effort / model_id 存在一起，读会话模型的接口本来就回会话行（第 2、3 条的存储与读取已具备）。
- 终端与网页共用的 `/model` 命令选模型时传 `effort=nil`，引擎据此「取该模型配置的思考强度一次并钉进会话行」（第 1 条在 `/model` 上已成立）。
- **缺口**：网页输入框的模型按钮（`ModelChip`）切换模型时写死 `effort: ''`，接口又把「没传」也当成空字符串钉下去——结果是会话行的思考强度被清空（按服务商自己的默认跑），设置里给这个模型配的值被绕过；思考强度菜单还多一项终端没有的「默认」。

本计划把网页这条路改到与 `/model` 同一语义，并把思考强度菜单按原型重画。做完后：选中模型＝取设置值并落库；手动改＝落库；重开会话＝回显库里的；设置里之后再改不影响已有会话。

## 待 owner 确认（原型说明栏里的 5 点；本计划按下列取值写成）

| # | 取值 | 依据 | 若 owner 改口，动哪一步 |
|---|------|------|------------------------|
| ① | 设置里配了具体档位时，菜单不列「默认」；只有「当前会话存的是空」或「该模型配置值为空」时才列出 | 终端 `/model` 的思考强度选择器只列档位、没有「默认」项（`pkg/turn/executor.go:929-932`）；owner 既有规则「不要'默认'选项，默认值是哪个就预先选中哪个」 | Step 5 的 `showDefault` |
| ② | 模型打开「允许关闭思考」时菜单多一项「关闭思考」，排最前 | 引擎已如此：`pkg/llm/catalog.go:726-728` 把 `none` 放进档位首位，接口的 `efforts` 已带它。**无需新代码** | — |
| ③ | 模型关了「思考模式」或没有可选档位时，不显示选择器 | 引擎已让这类模型 `efforts` 为空（`pkg/llm/catalog.go:714-718`）；技能工坊页早已 `v-if="sessionEfforts.length"` | Step 6 的 `v-if` |
| ④ | 设置配的那一档在菜单里标「模型设置」 | 原型 | Step 5 的 tag 与 Step 2 的 `default_effort` 字段 |
| ⑤ | 新聊天沿用现有规则：取主代理最近使用的模型，思考强度跟它一并取，不取设置值 | 现有实现 `run.NewSessionModel`（`pkg/run/config.go:2159-2183`，owner 2026-10-23 定稿）。**无需新代码** | 若改成「新聊天一律取设置值」需另立计划（要动 `NewSessionModel`，终端同步受影响） |

## 当前状态

### 引擎（只读，本计划不改）

`pkg/run/config.go:2267-2296` —— 指针语义，全计划的支点：

```go
// The pointer carries the same semantics SetPrimaryModel gives an explicit
// selection: nil derives the entry's configured effort once, and an explicit
// pointer — including "" — pins that exact value.
func ResolveSessionModelChoiceRef(r *Runner, modelID, provider, model string, effort *string) (PrimaryModelState, error) {
	...
	entry, err := resolveAgentProvider(r.AppCfg, r.activeAgentNameForModel(), modelSelector{...})
	...
	if effort != nil {
		entry.Params = appcfg.WithReasoningEffort(entry.Params, *effort)
	}
	return PrimaryModelState{ ..., Effort: appcfg.ReasoningEffort(entry.Params), Set: true }, nil
}
```

「配置的思考强度」从哪来 —— `pkg/config/llm_models.go:338-343`（设置 · 模型页的「思考强度」存成行的 `cap_default_effort`，投影时折进 `Params`）：

```go
	if effort := strings.TrimSpace(row.Caps.DefaultEffort); effort != "" && ReasoningEffort(out.Params) == "" {
		out.Params = WithReasoningEffort(out.Params, effort)
	}
```

`pkg/run/config.go:315-317`：`resolvedAgentProviders` 就是 `appcfg.ConfiguredLLMs(cfg)`。所以对任何一行，**`appcfg.ReasoningEffort(configured.Params)` 就是 `effort=nil` 时引擎会钉下的值**。

`/model` 已经这么用 —— `pkg/turn/executor.go:902-904`：

```go
		// effort=nil: the runner derives and pins this entry's configured
		// effort.
		if err := ctx.Model.SelectModel(ctx.commandContext(), ctx.SessionID, chosen, nil); err != nil {
```

落库 —— `pkg/gateway/slash_handlers.go:271-306` 的 `(*Server).SelectModel` 把 `resolved.Effort` 连同 model_id / provider / model 写进会话行；回合入口 `pkg/run/config.go:2409` 起的 `injectSessionModelSelection` 对有行的会话原样解析并刷新 `updated_at`、对没行的会话把实际用的模型与思考强度写成行。读取 —— `handleChatSessionModelGet`（`pkg/gateway/api_extra.go:1913` 起）回的就是会话行解析结果。

### 后端缺口

`pkg/turn/models.go:660-685` —— `ModelChoice` 没有「配置的思考强度」字段：

```go
	// ReasoningEfforts are the thinking levels this model offers that this
	// build can send; empty means the picker has nothing to ask, either
	// because the model does not reason or because it has no discrete level.
	ReasoningEfforts []string
	// SupportsImages reports that the catalog knows this model accepts image
	// input. Unknown models report false: a picker marks only what it knows.
	SupportsImages bool
}
```

`pkg/turn/models.go:698-720` —— `ModelChoices` 的循环体（`configured` 是 `appcfg.AgentLLMProviderConfig`，带 `Params`）：

```go
		choice := ModelChoice{
			ID:          strings.TrimSpace(configured.ID),
			Label:       llm.FormatProviderModel(provider, model),
			...
		}
		if hit, ok := llm.LookupRow(choice.ID, provider, model); ok {
			choice.SupportsImages = hit.SupportsAttachments
		}
		choice.ReasoningEfforts = ModelReasoningEffortsRow(choice.ID, provider, model)
		out = append(out, choice)
```

`pkg/gateway/api_extra.go:1820-1829` —— 选项结构：

```go
type sessionModelOption struct {
	ID             string   `json:"id"`
	Provider       string   `json:"provider"`
	Model          string   `json:"model"`
	Label          string   `json:"label"`
	BaseURL        string   `json:"base_url,omitempty"`
	APIPath        string   `json:"api_path,omitempty"`
	Efforts        []string `json:"efforts"`
	SupportsImages bool     `json:"supports_images"`
}
```

`pkg/gateway/api_extra.go:2073-2084` —— `handleChatSessionModelPut` 末段，**没传 effort 也钉空字符串**：

```go
	effort := ""
	if body.Effort != nil {
		effort = strings.TrimSpace(*body.Effort)
	}
	if effort != "" && !slices.Contains(s.sessionModelEffortsFor(cfg, agentName, chosen.Provider, chosen.Model), effort) {
		http.Error(w, "reasoning effort is not offered for this model", http.StatusBadRequest)
		return
	}
	if err := s.SelectModel(r.Context(), sid, chosen, &effort); err != nil {
```

（`body.Effort` 已经是 `*string`，见同函数 `var body struct { ... Effort *string \`json:"effort"\` }`。）

### 前端缺口

`frontend/src/components/chat/ModelChip.vue:213-227` —— 切换模型写死空：

```ts
    await forebrainApi.saveSessionModel(sessionId, {
      modelId: option.id,
      provider: option.provider,
      model: option.model,
      // A model switch resets the effort to the model's own default — the
      // effort chip pins one for the model it belongs to.
      effort: '',
    })
    emit('saved')
```

`frontend/src/components/chat/EffortChip.vue`（全文约 95 行）—— 菜单首项恒为「默认」，每项两行（标签 + 说明），顶部与底部各一条小字：

```ts
const optionsList = computed(() => [
  { id: '', label: t('chat.effortDefault'), description: t('chat.effortDefaultHint') },
  ...props.efforts.map((id) => ({ id, label: effortLevelLabel(id), description: levelDescription(id) })),
])
```

`frontend/src/views/ChatView.vue`：

- 模板（约 :593-598）`<EffortChip :effort="sessionModelData?.effort ?? ''" :efforts="sessionEfforts" :disabled="composerLocked || !sessionModelData" @select="selectEffort" />` —— 恒显示。
- `draftModel`（约 :1862）类型 `{ modelId?: string; provider: string; model: string; effort: string } | null`。
- `pickDraftModel`（约 :1878-1884）新聊天选模型时 `effort: ''`：
  ```ts
  draftModel.value = { modelId: option.id, provider: option.provider, model: option.model, effort: '' }
  sessionModelData.value = { ...data, modelId: option.id, provider: option.provider, model: option.model, effort: '' }
  ```
- `sessionEfforts`（约 :1891-1898）按 `modelId` 优先、否则 provider+model 找到「当前生效的选项」取其 `efforts`。
- `applyDraftChoices`（约 :1424-1432）第一条消息建会话后 `forebrainApi.saveSessionModel(id, model)` 把暂存写成会话行。

`frontend/src/views/WorkshopView.vue`（技能工坊页是会话页超集，复用同两个组件）：约 :122-136 模板、:425-431 一份相同的 `sessionEfforts`、:434-441 `selectEffort`。

`frontend/src/lib/api.ts`：`SessionModelOption`（约 :1721-1740，有 `efforts: string[]`、没有配置值字段）；`saveSessionModel(sessionId, choice: { modelId?: string; provider: string; model: string; effort: string })`（约 :3013-3019）。接口应答经 `toCamelCase`，所以后端的 `default_effort` 到前端是 `defaultEffort`。

`frontend/src/lib/effortLabel.ts`：`effortLevelLabel(id)`（全站唯一的档位文案）、`EFFORT_LEVELS = ['none','minimal','low','medium','high','xhigh','max']`（注释：「Every level in the order a picker offers them: `none` first」）。

`frontend/src/assets/main.css`（约 :800-850）菜单的公共样式 `.forebrain-chip-menu` / `.forebrain-chip-menu-cap` / `.forebrain-chip-menu-opt`（`display:grid`、`b` 加粗 600、`span` 12px 次要色）/ `.forebrain-chip-menu-opt--on b`（品牌色）。**模式按钮、审批预设按钮也用这几个类，不能改它们的既有规则。**

`frontend/src/locales/index.ts` 中文段约 :1973-1988、英文段约 :4358-4373：`chat.effortChipTitle`（'推理强度'）、`chat.effortMenuTitle`、`chat.effortMenuFoot`、`chat.effortDefaultHint`、`chat.effortLowHint` / `MediumHint` / `HighHint` / `XhighHint` —— 这 7 个 `*Title/Foot/Hint` 键只有 `EffortChip.vue` 在用（写计划时已 grep 核实）。`chat.effortDefault` / `effortNone` / `effortLow` … 是档位文案，别处也用，**保留**。

### 约定

- Go：注释与标识符用英文，注释写「为什么」；测试用 `require`；新测试照 `pkg/gateway/api_extra_test.go` 的 `TestChatSessionModelEndpoints`（:6318 起）与 `pkg/turn/models_test.go` 的 `TestModelChoicesKeepCatalogEffortLevels`（:518 起）的结构写。
- 前端：先用 ai-elements-vue、再用 shadcn-vue 的现成组件，都没有才自己写——本计划只改已有的两个自写按钮，不引入新组件。文案只走 `frontend/src/locales/index.ts`，中英两段同步。中文界面叫「思考强度」。
- 界面文案必须简洁：能用标签就不写句子，不加任何说明句。
- 不提交、不推送：改动留在工作区，owner 手动提交。

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Go 定向测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/gateway -run 'ModelChoices|ChatSessionModel|ChatModelDefault|SessionModelOptions' -count=1` | 两个包都 `ok` |
| Go 回归包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/gateway ./pkg/run -count=1` | 全部 `ok` |
| 架构规则 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | `ok` |
| 构建 / Vet | `CGO_ENABLED=1 go build -tags fts5 ./... && CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0，无输出 |
| 格式 | `gofmt -l pkg cmd` | 无输出 |
| 前端类型检查 | `cd frontend && pnpm typecheck` | exit 0，0 errors |
| 前端定向单测 | `cd frontend && pnpm exec vitest run src/components/chat src/views/ChatView.test.ts src/lib src/locales` | 全部 PASS，exit 0 |
| 网页 e2e | 仓库根：`FOREBRAIN_E2E_SPEC=e2e/chat-composer.spec.ts scripts/acceptance/web_e2e.sh` | 全部 PASS |

> `pnpm test -- <路径>` **不会**过滤（脚本是 `vitest run`，`--` 后的路径被丢弃），且全量今天因 10 个既有错误以 exit 1 结束（2026-11-11 实测）。定向单测必须用 `pnpm exec vitest run <路径>`，不要拿「全量绿」当证据。

## Scope

**In scope**（只允许改这些文件）：

- `pkg/turn/models.go`、`pkg/turn/models_test.go`
- `pkg/gateway/api_extra.go`、`pkg/gateway/api_extra_test.go`
- `frontend/src/lib/api.ts`
- `frontend/src/lib/sessionModel.ts`（新建）、`frontend/src/lib/sessionModel.test.ts`（新建）
- `frontend/src/components/chat/ModelChip.vue`、`frontend/src/components/chat/ModelChip.test.ts`
- `frontend/src/components/chat/EffortChip.vue`、`frontend/src/components/chat/ComposerBar.test.ts`（`EffortChip` 的用例在这个文件里）
- `frontend/src/views/ChatView.vue`、`frontend/src/views/ChatView.test.ts`
- `frontend/src/views/WorkshopView.vue`
- `frontend/src/assets/main.css`（只追加新类，不改既有规则）
- `frontend/src/locales/index.ts`（只动点名的键）
- `frontend/e2e/chat-composer.spec.ts`
- `plans/README.md`（状态行）

**Out of scope**（看着相关也不要碰）：

- `pkg/run/**`、`pkg/state/**`、`pkg/config/**`、`pkg/llm/**`、`pkg/tui/**`、`pkg/turn/executor.go` —— 引擎、会话行存取、终端的 `/model` 已经是目标语义，本计划靠它们、不改它们。**不改表结构、不写迁移。**
- `run.NewSessionModel` —— 新聊天取最近使用的模型与思考强度是 owner 2026-10-23 的定稿（确认点 ⑤）。
- 历史会话行里已经存成空的思考强度 —— 不回填、不迁移。空值如实回显为「默认」（它当时就是按服务商默认跑的）。
- `frontend/src/components/subagents/SubagentConfigDrawer.vue` 的思考强度下拉、`frontend/src/components/models/**`（设置 · 模型页）—— 另外的功能。
- 输入框工具栏的排列顺序 —— 原型里的输入框是简化示意，真实页面「模型在左、思考强度紧随其右、然后发送」已经是这个顺序，不要照原型把模型按钮挪到左侧。
- 原型里的会话抽屉 —— 真实页面已有，不动。
- `pkg/gateway/dist` 等构建产物。

## Git workflow

- 在 `dev` 的当前工作区（或基于 `dev` HEAD 的 worktree）里改；**不要 commit、不要 push、不要开 PR**——owner 手动提交。
- 工作区里已有与本计划无关的未提交改动（见 Drift check），不要还原、不要格式化它们。

## Steps

### Step 1：`ModelChoice` 带上该行配置的思考强度

`pkg/turn/models.go`：

1. 在 `ModelChoice` 的 `ReasoningEfforts` 字段后加：

   ```go
   	// ConfiguredEffort is the reasoning effort this row is configured with —
   	// the model page's 「思考强度」, folded into the row's params — which is
   	// exactly what a pick that names no effort pins (SelectModel's nil). ""
   	// means the row configures none and the provider applies its own.
   	ConfiguredEffort string
   ```

2. 在 `ModelChoices` 循环里、`choice.ReasoningEfforts = ...` 之后加一行：

   ```go
   		choice.ConfiguredEffort = appcfg.ReasoningEffort(configured.Params)
   ```

   （`appcfg` 在本文件已导入；`configured` 是循环变量。）

`pkg/turn/models_test.go`：照 `TestModelChoicesKeepCatalogEffortLevels` 的结构新增 `TestModelChoicesCarryTheRowsConfiguredEffort`：两条 `LLMProviders`，第一条带 `Params: appcfg.LLMRequestParams(\`{"reasoning":{"effort":"high"}}\`)`，第二条不带；断言 `choices[0].ConfiguredEffort == "high"`、`choices[1].ConfiguredEffort == ""`。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run 'TestModelChoices' -count=1 -v` → 新用例 `--- PASS`，其余 `TestModelChoices*` 仍 PASS。

### Step 2：选项带 `default_effort`；没传 effort 的写入改为「取配置值」

`pkg/gateway/api_extra.go`：

1. `sessionModelOption` 在 `Efforts` 后加字段，并在结构体注释里补一句它的含义：

   ```go
   	// DefaultEffort is the effort a switch to this row pins when the request
   	// names none: the row's configured effort ("" when it configures none).
   	DefaultEffort string `json:"default_effort"`
   ```

2. `sessionModelOptions` 构造每个选项时加 `DefaultEffort: choice.ConfiguredEffort,`。

3. `handleChatSessionModelPut` 末段（「当前状态」里那 9 行）改成指针语义——**没传就是 nil，传了才钉**：

   ```go
   	// A request that names no effort is a model switch: the runner derives
   	// the row's configured effort once and pins it, exactly as /model does.
   	// One that names an effort — including "" — pins that value.
   	var effort *string
   	if body.Effort != nil {
   		pinned := strings.TrimSpace(*body.Effort)
   		if pinned != "" && !slices.Contains(s.sessionModelEffortsFor(cfg, agentName, chosen.Provider, chosen.Model), pinned) {
   			http.Error(w, "reasoning effort is not offered for this model", http.StatusBadRequest)
   			return
   		}
   		effort = &pinned
   	}
   	if err := s.SelectModel(r.Context(), sid, chosen, effort); err != nil {
   ```

4. 同函数上方的文档注释末句「An empty effort is the explicit "model default" choice — …」保留，并在其后补一句：omitted effort derives and pins the row's configured effort。

`pkg/gateway/api_extra_test.go`：

- 既有 `TestChatSessionModelEndpoints` 里所有 PUT 都显式带了 `"effort"`（:6421、:6423、:6431、:6447），语义不变，**应保持全绿、不要改它的断言**。
- 新增 `TestChatSessionModelPutWithoutAnEffortPinsTheRowsConfiguredEffort`，照 `TestChatSessionModelEndpoints` 的夹具与 `modelCall` 帮手写（可直接复制那段搭建代码），配置为：

  ```go
  {Provider: "zhipuai", Model: "glm-5.3-flash", APIKey: "${...}", BaseURL: "http://a.test/v1",
   Params: appcfg.LLMRequestParams(`{"reasoning":{"effort":"high"}}`)},
  {Provider: "deepseek", Model: "deepseek-v4-pro", APIKey: "${...}", BaseURL: "http://c.test/v1"},
  ```

  断言（按顺序）：
  1. GET：`options[0].default_effort == "high"`、`options[1].default_effort == ""`。
  2. PUT `{"provider":"zhipuai","model":"glm-5.3-flash"}`（**不带 effort 键**）→ 200，应答含 `"effort":"high"`，`sess.SessionModelSelection` 读回 `row.Effort == "high"`。
  3. PUT 同模型 `"effort":"low"` → 行为 `low`；随后 GET 回 `effort == "low"`（**回显的是会话行**）。
  4. 把配置里这一行的 `Params` 改成 `{"reasoning":{"effort":"max"}}`（改 `cfg.Agents.Definitions["main"].LLMProviders[0].Params` 后再调一次 `s.Env.AdoptConfig(&cfg)`），GET：`options[0].default_effort == "max"` 且顶层 `effort` **仍是 `"low"`**（设置里之后再改，不影响已有会话）。
  5. PUT 同模型、不带 effort → 行变成 `max`（再次选中该模型时取新值）。
  6. PUT `"effort":""` → 行为 `""`（显式的「默认」仍可钉）。
  7. PUT `{"provider":"deepseek","model":"deepseek-v4-pro"}` 不带 effort → 200，行 `Effort == ""`（该行没配）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'TestChatSessionModel|TestChatModelDefault|TestSessionModelOptions' -count=1 -v` → 新用例与既有用例全部 `--- PASS`。

### Step 3：前端类型与请求

`frontend/src/lib/api.ts`：

1. `SessionModelOption` 在 `efforts: string[]` 后加：

   ```ts
     /** The effort a switch to this row pins when the request names none: the
      *  row's configured effort (the model page's 「思考强度」), '' when it
      *  configures none. Absent from a list that predates the field. */
     defaultEffort?: string
   ```

2. `saveSessionModel` 的 `choice` 类型把 `effort: string` 改成 `effort?: string`，并把上方注释改成：省略 effort 是「切换模型」——服务端取该行配置的思考强度并钉进会话行；给出 effort（含 `''`）则钉这个值。
3. `SessionModelSelection` 的注释（「An empty effort is the explicit "model default" pin」）保留。

新建 `frontend/src/lib/sessionModel.ts`（两个页面现在各抄了一份「找当前生效的选项」，收成一处）：

```ts
import type { SessionModelOption, SessionModelResponse } from './api'

/**
 * The option a conversation's selection names: the configured row by id when
 * the selection carries one, else the provider+model pair (a row written before
 * ids existed). The effort chip reads its levels and its configured effort.
 */
export function sessionModelOptionInForce(data: SessionModelResponse | null): SessionModelOption | undefined {
  if (!data) return undefined
  return data.modelId
    ? data.options.find((candidate) => candidate.id === data.modelId)
    : data.options.find((candidate) => candidate.provider === data.provider && candidate.model === data.model)
}
```

新建 `frontend/src/lib/sessionModel.test.ts`：三例——按 id 命中（两行同 provider/model 时取 id 那一行）、无 id 时按 provider+model 命中、`null` 返回 `undefined`。

**Verify**：`cd frontend && pnpm typecheck` → 此时会因 `ModelChip.vue` / `ChatView.vue` 尚未调整而**可能**报错；只要求 `pnpm exec vitest run src/lib/sessionModel.test.ts` → 3 passed。类型检查在 Step 6 结束时统一过。

### Step 4：模型按钮切换模型时不再带 effort

`frontend/src/components/chat/ModelChip.vue`：

1. `choose()` 里的 `saveSessionModel` 调用**删掉 `effort: ''` 这一键及其上方两行注释**，换成一行注释：`// No effort: the server pins the row's configured one, as /model does.`
2. 文件头注释里「Switching resets the effort to the model's default, exactly as a /model switch does.」改成「Switching pins the effort the model is configured with, exactly as a /model switch does.」

`frontend/src/components/chat/ModelChip.test.ts`：`names the configured row, not just the pair, for twin models`（约 :153-178）末尾的 `toMatchObject({... effort: '' })` 去掉 `effort` 一行，并加 `expect(save.mock.calls[0][1]).not.toHaveProperty('effort')`。

**Verify**：`cd frontend && pnpm exec vitest run src/components/chat/ModelChip.test.ts` → 全部 PASS。

### Step 5：思考强度菜单按原型重画

`frontend/src/components/chat/EffortChip.vue`：

1. props 增加 `defaultEffort?: string`（注释：the effort the model in force is configured with; '' or absent when it configures none）。
2. `optionsList` 改为：

   ```ts
   const optionsList = computed(() => {
     // The order a picker offers levels in is the project's own (EFFORT_LEVELS,
     // `none` first); a level a source adds later keeps the server's place after them.
     const known: string[] = EFFORT_LEVELS.filter((level) => props.efforts.includes(level))
     const extra = props.efforts.filter((level) => !(EFFORT_LEVELS as readonly string[]).includes(level))
     const configured = props.defaultEffort ?? ''
     // 「默认」 is a row only when it is the truth to show: the conversation
     // stores no level, or the model is configured with none.
     const showDefault = props.effort === '' || configured === ''
     const ids: string[] = showDefault ? ['', ...known, ...extra] : [...known, ...extra]
     return ids.map((id) => {
       const label = effortLevelLabel(id)
       return {
         id,
         label,
         // The level's own value name beside a translated label; not beside one that already reads the same.
         value: id !== '' && label.toLowerCase() !== id.toLowerCase() ? id : '',
         fromSetting: id !== '' && id === configured,
       }
     })
   })
   ```

   import 改成 `import { EFFORT_LEVELS, effortLevelLabel } from '@/lib/effortLabel'`；删掉 `levelDescription`。

3. 模板的菜单部分改成一行一项（原型：打勾列固定宽、标签、等宽小字的取值名、右侧「模型设置」）：

   ```vue
   <div v-if="open" class="forebrain-chip-menu right-0 w-[220px] max-w-[calc(100vw-2rem)]" role="listbox" :aria-label="t('chat.effortChipTitle')">
     <div class="forebrain-chip-menu-cap">{{ t('chat.effortChipTitle') }}</div>
     <button
       v-for="option in optionsList"
       :key="option.id"
       type="button"
       class="forebrain-chip-menu-opt forebrain-chip-menu-opt--line"
       :class="{ 'forebrain-chip-menu-opt--on': option.id === effort }"
       role="option"
       :aria-selected="option.id === effort"
       :data-testid="`effort-option-${option.id || 'default'}`"
       @click="choose(option.id)"
     >
       <i class="forebrain-chip-menu-lead" aria-hidden="true"><Check v-if="option.id === effort" class="size-3.5" /></i>
       <b>{{ option.label }}</b>
       <code v-if="option.value" class="forebrain-chip-menu-val">{{ option.value }}</code>
       <em v-if="option.fromSetting" class="forebrain-chip-menu-tag" data-testid="effort-setting-tag">{{ t('chat.effortModelSetting') }}</em>
     </button>
   </div>
   ```

   底部那条 `chat.effortMenuFoot` 小字删除。触发按钮（脑图标 + 当前档位 + 箭头）不变。

4. 文件头注释第一段改成与新行为一致：the chip shows the level the conversation's row stores; the levels are the ones the model in force offers; 「默认」 is listed only when the row stores none or the model is configured with none; the level the model page configured is marked.

`frontend/src/assets/main.css`：在 `.forebrain-chip-menu-opt--on b { ... }` 规则**之后**追加（只加不改）：

```css
/* A one-line option (the effort menu): a fixed lead column for the check, the
   label, the level's own value name, and a trailing tag. */
.forebrain-chip-menu-opt--line {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 10px;
  font-size: 13px;
}
.forebrain-chip-menu-opt--line b {
  font-weight: 400;
}
.forebrain-chip-menu-opt--line.forebrain-chip-menu-opt--on b {
  font-weight: 500;
}
.forebrain-chip-menu-lead {
  width: 14px;
  flex: none;
  display: grid;
  place-items: center;
  color: var(--forebrain-brand-1);
}
.forebrain-chip-menu-val {
  font-family: var(--forebrain-mono-font);
  font-size: 11px;
  color: var(--forebrain-muted-text);
}
.forebrain-chip-menu-tag {
  margin-left: auto;
  font-size: 11.5px;
  font-style: normal;
  color: var(--forebrain-muted-text);
  white-space: nowrap;
}
```

`frontend/src/locales/index.ts`（中英两段都改；该文件有别人的未提交改动，只动下面这些键）：

- 改：中文 `'chat.effortChipTitle': '思考强度'`（英文保持 `'Reasoning effort'`）。
- 加：`'chat.effortModelSetting'` —— 中文 `'模型设置'`、英文 `'Model setting'`，放在 `chat.effortDefault` 附近。
- 删（两段各 7 个）：`chat.effortMenuTitle`、`chat.effortMenuFoot`、`chat.effortDefaultHint`、`chat.effortLowHint`、`chat.effortMediumHint`、`chat.effortHighHint`、`chat.effortXhighHint`。删前跑 `grep -rn "effortMenuTitle\|effortMenuFoot\|effortDefaultHint\|effortLowHint\|effortMediumHint\|effortHighHint\|effortXhighHint" frontend/src frontend/e2e`，除 `locales/index.ts` 自身外应无输出；有输出则那个键不删并在报告里说明。

`frontend/src/components/chat/ComposerBar.test.ts`：把 `describe('EffortChip', …)` 里那一例换成下面四例（文件其它 describe 不动）：

1. **配了具体档位**：`{ effort: 'high', efforts: ['high', 'low'], defaultEffort: 'high' }` → 选项 testid 依次为 `effort-option-low`、`effort-option-high`（按项目顺序重排）；不存在 `effort-option-default`；`effort-setting-tag` 只出现一次且在 `high` 那一行；`high` 行 `aria-selected="true"`；点 `low` 行 → `emitted('select')` 为 `[['low']]`。
2. **模型没配**：`{ effort: '', efforts: ['low', 'high'], defaultEffort: '' }` → 首项是 `effort-option-default` 且选中；没有 `effort-setting-tag`。
3. **历史空值如实回显**：`{ effort: '', efforts: ['low', 'high'], defaultEffort: 'high' }` → `effort-option-default` 存在且选中；`effort-setting-tag` 在 `high` 行。
4. **关闭思考排最前**：`{ effort: 'low', efforts: ['low', 'none'], defaultEffort: 'low' }` → 首项 testid 为 `effort-option-none`，文本含「关闭思考」。

**Verify**：`cd frontend && pnpm exec vitest run src/components/chat/ComposerBar.test.ts src/locales` → 全部 PASS。

### Step 6：两个页面接上配置值；新聊天选模型即回显配置值

`frontend/src/views/ChatView.vue`：

1. `import { sessionModelOptionInForce } from '@/lib/sessionModel'`。把 `sessionEfforts` 改成基于它，并新增 `sessionDefaultEffort`：

   ```ts
   /** The option the conversation's selection names, per the shared catalog. */
   const sessionModelOption = computed(() => sessionModelOptionInForce(sessionModelData.value))
   /** The reasoning levels the model in force offers. */
   const sessionEfforts = computed(() => sessionModelOption.value?.efforts ?? [])
   /** The effort the model page configured for the model in force. */
   const sessionDefaultEffort = computed(() => sessionModelOption.value?.defaultEffort ?? '')
   ```

2. 模板里的 `<EffortChip …>` 加 `v-if="!sessionModelData || sessionEfforts.length"` 与 `:default-effort="sessionDefaultEffort"`。含义：数据还没到时照旧画一个禁用的按钮占位（首帧不跳），数据到了且模型没有可选档位就不画。把它上方那段注释里「Every chip is drawn whether or not its data has landed yet」一句改成与此一致（the effort chip is the one exception: once the data says the model has no levels, it is not drawn）。

3. `draftModel` 的类型把 `effort: string` 改成 `effort?: string`。

4. `pickDraftModel` 改成「暂存里不带 effort（发出第一条消息时由服务端取配置值），界面上立即回显配置值」：

   ```ts
   /** A model picked on a new chat: held without an effort — the server pins the
    *  one the model is configured with when the first message opens the session —
    *  and the chip shows that configured effort meanwhile. */
   function pickDraftModel(option: SessionModelOption) {
     const data = sessionModelData.value
     if (!data) return
     draftModel.value = { modelId: option.id, provider: option.provider, model: option.model }
     sessionModelData.value = { ...data, modelId: option.id, provider: option.provider, model: option.model, effort: option.defaultEffort ?? '' }
   }
   ```

   `selectEffort` 的新聊天分支不变（它写入带 `effort` 的暂存，优先级自然高于上面）。`applyDraftChoices` 不变（它把暂存原样交给 `saveSessionModel`，没有 `effort` 键时即省略）。

`frontend/src/views/WorkshopView.vue`：同样引入 `sessionModelOptionInForce`，把本地那份 `sessionEfforts` 换成上面第 1 点的三个 computed，并给 `<EffortChip>` 加 `:default-effort="sessionDefaultEffort"`（它已有 `v-if="sessionModelData && sessionEfforts.length"`，不改）。

`frontend/src/views/ChatView.test.ts`：照 `shows the server-computed most recently used model on a new chat`（约 :797）的搭建方式新增一例 `a model picked on a new chat shows its configured effort and is saved without one`：`chatModelDefault` 回 `{ modelId:'m-a', provider:'p', model:'a', effort:'', options:[{id:'m-a',…,efforts:[]},{id:'m-b',provider:'p',model:'b',label:'b',efforts:['low','high'],defaultEffort:'high'}] }`；点开模型按钮选 `m-b` → `[data-testid="effort-chip"]` 文本含「高」；若该文件已有「发送第一条消息」的帮手，再断言 `saveSessionModel` 被调用且第二个参数 `not.toHaveProperty('effort')`；没有现成帮手就只做前半段断言，并在报告里说明。

**Verify**：`cd frontend && pnpm typecheck` → exit 0；`pnpm exec vitest run src/components/chat src/views/ChatView.test.ts src/lib src/locales` → 全部 PASS。

### Step 7：e2e——选中、落库、重开回显

`frontend/e2e/chat-composer.spec.ts`：

1. 既有 `a reasoning effort picked on the chip survives a reload`：把 `await expect(page.locator('[data-testid="effort-option-default"]')).toBeVisible()` 换成 `await expect(page.locator('[data-testid="effort-option-high"]')).toBeVisible()`（「默认」不再恒在）。其余不动。
2. 新增 `each conversation shows the model and effort its own row stores`：用 `seedProjectSession` 建两个会话 A、B；进 A：选 `glm-5.3-flash`、思考强度选 `high`；进 B：选 `glm-5.3-flash`、思考强度选 `low`；回到 A（`page.goto('/?session=A')`）→ `effort-chip` 含「高」、`model-chip` 含 `glm-5.3-flash`；再到 B → `effort-chip` 含「低」。照第一例的等待与选择器写法。

**Verify**：仓库根 `FOREBRAIN_E2E_SPEC=e2e/chat-composer.spec.ts scripts/acceptance/web_e2e.sh` → 该文件全部用例 PASS。

### Step 8：全量门禁

按「Commands you will need」逐条跑：Go 回归包、架构规则、构建 / Vet、`gofmt -l`、前端类型检查、前端定向单测。

**Verify**：全部符合表中预期。

## Test plan

| 层 | 文件 | 新增 / 修改 | 覆盖 |
|----|------|-------------|------|
| Go | `pkg/turn/models_test.go` | 新增 1 | 选项的配置值＝行 `Params` 里的思考强度 |
| Go | `pkg/gateway/api_extra_test.go` | 新增 1（7 个断言段） | 选项 `default_effort`；不带 effort 的写入取配置值并落行；回显读会话行；设置改后已有会话不变、再选取新值；显式空仍可钉；没配的行落空 |
| 前端 | `src/lib/sessionModel.test.ts` | 新增 3 | 当前生效选项的查找 |
| 前端 | `src/components/chat/ComposerBar.test.ts` | 1 例换成 4 例 | 菜单：不列「默认」/ 列「默认」/ 历史空值 / 关闭思考在前 / 「模型设置」标记 / 顺序 / 选择事件 |
| 前端 | `src/components/chat/ModelChip.test.ts` | 改 1 | 切换模型的请求不带 effort |
| 前端 | `src/views/ChatView.test.ts` | 新增 1 | 新聊天选模型即回显配置值、暂存不带 effort |
| e2e | `e2e/chat-composer.spec.ts` | 改 1、新增 1 | 真实页面上两个会话各自回显自己存的模型与思考强度；重载后仍在 |

终端与主视图 / 回放的一致性：模型与思考强度按钮只存在于「对话页」与「技能工坊页」两个输入框（已 grep 核实），子代理标签页与回放没有这两个按钮，因此本计划涉及的界面只有这两处，Step 6 两处都改。终端的 `/model` 不改，由 Step 2 的 Go 测试证明网页写入与它走同一个 `SelectModel(nil)`。

## 非功能要求（风险、对策、验证）

| 维度 | 风险 | 对策 | 验证 |
|------|------|------|------|
| 延迟 / 性能 | 选项多一个字段，每行多一次对 `Params` 的 JSON 读取 | 行数是用户配置的模型数（个位到几十），内存内解析，无 I/O、无 SQL | 不需要专门测量；Go 回归包耗时无明显变化即可 |
| 鲁棒 | 配置值不在可选档位里（如协议裁掉了该档） | 不带 effort 的写入不做档位校验，与 `/model` 一致（引擎按配置发）；菜单里该值不出现在列表，但按钮文字照实显示会话行的值 | Step 2 断言 7 覆盖「没配」；按钮文字走 `effortLevelLabel`，未知档位原样显示 |
| 鲁棒 | 会话行存的档位后来被设置取消 | 不改行、不替用户换值；按钮照实显示存的值，用户可在菜单里另选 | `effortLevelLabel` 既有行为 |
| 兼容 | 接口语义变化：以前「不带 effort」＝钉空，现在＝取配置值 | 仓库内唯一调用方是 `ModelChip`（本计划同步改）；显式 `""` 语义不变 | 既有 `TestChatSessionModelEndpoints` 不改且保持绿 |
| 兼容 | 历史会话行里的空值 | 不迁移、不回填；如实回显「默认」（它当时就按服务商默认跑） | `ComposerBar.test.ts` 第 3 例 |
| 多库 / 多副本 | — | 不改 SQL、不改表、不加进程内状态；会话行仍是唯一来源 | 无 SQL 变更（`git diff --stat -- pkg/state` 为空） |
| 可维护 | 「配置值」出现两套算法 | 只有一处：`turn.ModelChoice.ConfiguredEffort` ＝ `appcfg.ReasoningEffort(configured.Params)`，与引擎 nil 派生同一表达式；前端「当前生效选项」收成 `sessionModelOptionInForce` 一处 | Step 2 断言 1+2 把「选项声明的值」与「引擎实际钉下的值」对上 |
| 可扩展 | 以后出现新档位 | 菜单把不认识的档位排在已知档位之后、文字原样显示 | `optionsList` 的 `extra` 分支 |
| 体验 | 数据未到时按钮闪现又消失 | 数据未到照旧画禁用占位，只有确知没有档位才不画 | Step 6 第 2 点 |
| 体验 | 文案噪音 | 菜单只有一个小标题「思考强度」与一个标签「模型设置」，无任何说明句 | `ComposerBar.test.ts` |

## Done criteria

全部成立才算完成：

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/gateway ./pkg/run -count=1` 全部 `ok`；新用例 `TestModelChoicesCarryTheRowsConfiguredEffort`、`TestChatSessionModelPutWithoutAnEffortPinsTheRowsConfiguredEffort` 存在且 PASS
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./... && CGO_ENABLED=1 go vet -tags fts5 ./...` exit 0；`gofmt -l pkg cmd` 无输出
- [ ] `cd frontend && pnpm typecheck` exit 0
- [ ] `cd frontend && pnpm exec vitest run src/components/chat src/views/ChatView.test.ts src/lib src/locales` 全部 PASS
- [ ] `FOREBRAIN_E2E_SPEC=e2e/chat-composer.spec.ts scripts/acceptance/web_e2e.sh` 全部 PASS
- [ ] `grep -n "effort: ''" frontend/src/components/chat/ModelChip.vue` 无输出
- [ ] `grep -rn "effortMenuFoot\|effortDefaultHint\|effortLowHint" frontend/src` 无输出
- [ ] `git diff --stat -- pkg/run pkg/state pkg/config pkg/llm pkg/tui pkg/turn/executor.go` 相对开工时无新增改动（开工前先记一份 `git status --porcelain` 作基线，因为工作区本来就不干净）
- [ ] `plans/README.md` 状态行已更新

### owner 手动验收清单（真实网页，执行者不必跑）

1. 设置 · 模型里把某模型的思考强度配成「高」。对话页切到该模型 → 思考强度按钮显示「高」，菜单里「高」打勾并标「模型设置」，没有「默认」项。
2. 在该会话把思考强度改成「低」，发一条消息；切到别的会话再切回、或刷新页面 → 仍是该模型 +「低」。
3. 回设置把该模型改成「中」→ 上面那个会话仍是「低」；在它里面切到别的模型再切回该模型 → 变成「中」。
4. 终端 `forebrain` 打开同一个会话 → 状态里的模型与思考强度与网页一致。
5. 切到一个关了「思考模式」的模型 → 思考强度按钮不出现。

## STOP conditions

停下并报告（不要自行发挥）：

- 「当前状态」里的摘录与活代码对不上（尤其是 `handleChatSessionModelPut` 末段、`ModelChoices` 循环体、`ResolveSessionModelChoiceRef` 的 nil 语义）。
- Step 2 断言 2 失败（不带 effort 的写入没有落成配置值）——说明「`ConfiguredLLMs` 的 `Params` ＝ 引擎派生值」这个假设不成立，不要通过在网关里自己算一遍来绕过。
- Step 2 断言 4 里重新 `AdoptConfig` 后 `default_effort` 读不到新值——报告夹具行为，不要删掉这段断言。
- 既有 `TestChatSessionModelEndpoints` 或 `pkg/turn` 的 `/model` 相关用例变红。
- 需要改 Out of scope 里的任何文件（特别是 `pkg/run`、`pkg/state`、`pkg/tui`）才能继续。
- `frontend/src/locales/index.ts` 里要删的键还有别处在用。
- 某一步的验证在一次合理修正后仍失败。

## Maintenance notes

- **不变式**：会话行里的 effort 是该会话「最后设置或使用的具体值」；设置 · 模型里的「思考强度」只在「选中模型」那一刻被取一次（请求不带 effort）。以后任何新入口（快捷键、命令面板、新的选择器）切换模型时都必须不带 effort 调同一个接口或直接 `SelectModel(nil)`，不要再出现写死 `''` 的调用。
- 评审重点：`handleChatSessionModelPut` 的 nil / 非 nil 分支；`EffortChip` 的 `showDefault` 条件；`main.css` 只追加未改既有规则（模式按钮、审批预设按钮共用这些类）。
- 明确延后：① 新聊天是否改为「一律取设置值」（确认点 ⑤，要动 `run.NewSessionModel`，终端同步受影响，需另立计划）；② 历史空值会话行不回填；③ 终端 `/model` 的思考强度选择器是否也标出「模型设置」那一档（owner 没提，未做）。
- 与 `plans/299`（设置页里 `goal-evaluator` 的模型与思考强度）无文件交集，但两者都读「模型行配置的思考强度」；299 若需要同一个值，应复用 `turn.ModelChoice.ConfiguredEffort`，不要另算。
