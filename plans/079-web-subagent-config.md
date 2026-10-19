# Plan 079：子代理配置（含工坊工作台标签分页等全站统一）——每个主代理可自定义子代理、改写内置子代理（模型 / 思考强度 / 停用 / 提示词，运行时真正生效），子代理页改为配置的增删改查；设置页子代理全局开关；MCP 表与全站表格的几处统一

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不建 worktree、不 commit、不 push、不 stash；改动留在当前工作区。
> 先跑 `scripts/fetch-libtokenizers.sh`；Go 测试要 pdfium：
> ```
> P=$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)
> export PKG_CONFIG_PATH=$P CGO_CFLAGS="-I$P/include" CGO_LDFLAGS="-L$P/lib -lpdfium"
> ```
>
> **前置**：plan 078 已执行完（它也改 `locales/index.ts`、`ChatView.vue`、`lib/api.ts`；本计划必须在它之后串行执行）。
> 行号按 2026-10-18 工作区（HEAD `f890b0e` + 未提交改动），**一律用给出的代码片段 grep 定位**，行号只作参考。
>
> **Drift check（先跑）**：每条都必须各有输出，否则 STOP：
> ```
> test -f frontend/src/components/chat/SubagentDrawer.vue && echo 078-done
> grep -n "func ResolvePublicSubtype(name string)" pkg/agent/subagent_defs.go
> grep -n "func SystemPromptForSubtype(name string)" pkg/agent/subagent_defs.go
> grep -n "if prompt, ok := agent.SystemPromptForSubtype(subtype); ok {" pkg/run/typedsubagent_llm.go
> grep -n "var typedSubagentDisallowedTools = map" pkg/tool/permissions.go
> grep -n "func SubagentsEnabled(cfg \*Root) bool" pkg/config/agents.go
> grep -n "if !appcfg.SubagentsEnabled(fac.appConfig()) {" pkg/run/subagent.go
> grep -c "resolve:    agent.ResolvePublicSubtype," pkg/run/subagent.go
> grep -n "func subagentTypeEnumValues() \[\]any" pkg/run/subagent.go
> grep -n "type SubagentModelOverride struct" pkg/run/subagent.go
> grep -n "func (s \*Server) saveAndReload(cfg config.Root) error" pkg/gateway/api_extra.go
> grep -n "func (s \*Server) rebindAgentRunners()" pkg/gateway/mcp_write_api.go
> grep -n "agentRosterPage" frontend/src/views/SubagentsView.vue
> ```
> 倒数第 6 条（`grep -c`）应输出 `2`。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED–HIGH（改动子代理派发的类型解析、系统提示词注入、工具权限与模型选择；配置文件结构新增字段）
- **Depends on**: plans/078（串行）
- **Category**: direction（新功能 + 设计稿落地）
- **Planned at**: commit `f890b0e` + 工作区未提交改动，2026-10-18

## Why this matters

owner 2026-10-18：菜单栏“子代理”页的需求是**增删改查当前主代理的子代理配置**，不是展示运行中的子代理列表。运行中的子代理已经在对话卡片、右侧抽屉（078）和工作台概览里了。

**现状完全没有这个能力**（已核实）：
- 子代理类型只有 `pkg/agent/subagent_defs.go` 里写死的 10 个内置项。
- 模型派发时只认其中 4 个公开类型：`ResolvePublicSubtype` 加 `PublicTypeNames()`，也就是 general-purpose、explore、plan、verification。
- 唯一可配的是这 4 个类型的模型：写在全局的 `agents.definitions.<类型>.llm_providers`，不分主代理。
- 提示词、工具权限、能否续聊全部固定。
- “允许模型派发子代理”只有一个全局开关 `agents.defaults.enable_subagent`，默认关，而且 web 上根本没有入口。

**owner 已裁定**（记忆 `web-subagent-config-page`、`ui-forms-in-modals`、`web-ui-structure-rules`）：
1. 列出 harness 内置的全部 10 个子代理和当前主代理自定义的子代理。内置分两类：
   - “内置”：模型可派发的 4 个；
   - “系统”：由系统在特定场合调用的 6 个，即 plan-reviewer、goal-evaluator、guardian、cavecrew-investigator / builder / reviewer，**只能查看**。
2. **自定义子代理写进 forebrain.yaml 的主代理定义下**。
3. **内置（可派发）子代理**：
   - 可以为当前主代理单独指定模型与思考强度；
   - 可以停用（模型不能派发它）；
   - 可以**改写提示词，改写后运行时必须真正覆盖内置提示词**，并可恢复默认；
   - 不能删除，工具权限固定。
4. 内置与系统子代理的提示词框**必须显示内置提示词完整原文**，不能写“见代码”。
5. 自定义子代理的工具权限三档：
   - 只读：读文件、搜索、只读命令；
   - 可编辑：全部工具，但不能再派子代理；
   - 指定工具：从工具列表勾选。
6. 两级开关：
   - 设置页“常规 → 子代理”是全局总开关，对应 `agents.defaults.enable_subagent`；
   - 子代理页工具栏右侧、“新建子代理”左边是**当前主代理自己的开关**，标签只写“子代理功能”。没改过时跟随全局，在这里切换只对这个主代理生效。
   - 页面上**不显示**“跟随全局 / 已覆盖全局 / 恢复跟随”这类文字和交互。
   - 这个开关**只和各行的启用开关联动**：关闭时所有子代理（自定义 + 内置可派发）的启用批量改为禁用，开启时批量改为启用；单独打开某一行会把开关打开，最后一行被关掉时开关随之关闭；表格**不变灰**。
7. 列表顺序：自定义 → 内置 → 系统，新建的排最前。
8. 表格只有：子代理、类型、模型、工具、操作。**没有状态列**；启用开关放在操作列最前面。
9. 新建、编辑、查看都在**右侧抽屉**里，与 MCP 一样；删除用确认框。
10. 思考强度只跟着明确指定的模型生效：模型选“跟随主代理”时，思考强度也跟随（下拉禁用，显示“跟随主代理”）。
11. 全站规则（本计划一并落地）：
    - MCP 表的启用开关移到操作列最前面，不再单独一列；
    - MCP 表“类型”列只写 `stdio` / `http`，不加“请求头”“OAuth”；
    - MCP 抽屉“类型”下拉的选项只写 `stdio` / `http`；
    - **所有表格的“操作”列头文字居中**；
    - **两个工作台宽度统一为 340px，标签的样式与交互一致**；技能工坊的 8 个标签一次显示 5 个，右侧按钮每点一次向右滚动一个（滚动过后左侧出现返回按钮），标签靠左紧凑排列；
    - 分页默认每页 10 条（已是，不改）。

## 设计稿（验收依据，实施须 1:1 还原）

- 原型 https://claude.ai/artifact/KKfPW8dXWScuRdTzonsk2F（本地 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`，v83）。
- 状态按钮：
  - 子代理：**子代理 · 配置列表**、**子代理 · 功能关闭**、**子代理 · 新建**、**子代理 · 指定工具**、**子代理 · 编辑内置**、**子代理 · 查看系统**；
  - 设置：**设置 · 子代理（全局开关）**；
  - MCP：**项目 · MCP**（操作列里的开关、类型列）、MCP 抽屉（类型下拉）。
- 原型代码（文件里搜函数名）：
  - `pages.subagents`：表格、工具栏、开关 `.sa-tsw`；
  - `subDefs` / `subBuiltinPrompts`：10 个内置子代理的完整提示词，由 `agent.ActiveDefinitions()` 原样导出；
  - `subForm`：抽屉表单字段；
  - `subToolsLabel`、`subModelCell`、`swSlot`；
  - `fmModal` 里 `subnew/subedit/subview` 的抽屉分支，以及 `subdel` 的确认框；
  - `pages.settings` 的 `t === 'subagents'`；
  - `mcpSwCell`、`mcpType`。
- 原型 CSS：`.sa-tsw`、`.sa-nm`、`.sa-ds`、`.sa-pick*`、`.sa-plab`、`.sa-prompt`、`.acts-r .sw-slot`、`.tb th.c`、`.dw*`（抽屉）。

## Current state（已核实）

**内置定义**（`pkg/agent/subagent_defs.go`）
- `type Definition struct { Name, Description, Source string; OneShot, Continuable bool; SystemPrompt string }`；`var builtins = map[string]Definition{...}` 有 10 项。
- 函数：
  - `ActiveDefinitions()`：全部，按名排序；
  - `ResolveSubtype(name)`：全部 10 个可解析；
  - `ResolvePublicSubtype(name)`：只解析 `PublicTypeNames()` 的 4 个；
  - `SystemPromptForSubtype(name)`。
- `general-purpose` 与 `plan` 的提示词拼接了 `ScopeDisciplinePrompt`（`scope_discipline.go`），plan 另拼 `planScopeDisciplineAddendum`。
- `pkg/config/load.go` 另有一份 `builtInSubagentDefinitionNames`（8 个，不含 goal-evaluator、plan-reviewer），用来保证这些名字不会被当成主代理（`IsBuiltInSubagentDefinition`）。

**配置**（`pkg/config/agents.go`）
- `AgentDefinition{Primary, DisplayName, Description, MCPServers, LLMProviders}`，没有子代理字段。
- `AgentDefaults.EnableSubagent *bool`；`SubagentsEnabled(cfg *Root) bool` 只看全局。
- 全局的 `agents.definitions.<类型>.llm_providers` 给 4 个公开类型指定模型。读这些配置的地方：
  - `pkg/run/runner.go` 建 `typeLLMs`（搜 `for _, typeName := range agent.PublicTypeNames()`）；
  - `SubagentOwnModel(r, agentType)`（搜 `func SubagentOwnModel`）。
- `appcfg.WithReasoningEffort(params, effort)`（`pkg/config/agent_llm.go`）。

**派发**（`pkg/run/subagent.go`）
- `RegisterSubagentTool(a, fac)`：`if !appcfg.SubagentsEnabled(fac.appConfig()) { return nil }`。
- `subagent_run` 的结果元数据调 `agent.ResolvePublicSubtype(subType)` 取 `OneShot/Continuable/Source`。
- `subagentTypeEnumValues()` 返回 `PublicTypeNames()`。
- `patchSubagentTypeEnum` 把 `subagent_type` 的 `enum` 与一段固定英文 `description` 写进工具 schema（搜 `Optional public typed subagent. general-purpose:`）。
- `applySubagentTypeEnumToTool(t, fieldPath)` 被 subagent_* 各工具调用。
- `subagentDispatch.resolve`：两处 `resolve: agent.ResolvePublicSubtype`（模型派发），两处 `resolve: agent.ResolveSubtype`（计划审阅、目标检查）。
- `prepareSubagentExecutionResolved` 里 `def, derr := d.resolve(t)`，之后：
  - 写 `agentType/OneShot/Continuable/defSource`；
  - 用 `tool.TypedSubagentBlocksFileWrites(rawSubtype)` 决定计划模式继承；
  - `SubagentModelOverrideFromContext(baseCtx)` 存在时写进 `entry.ModelProvider/Model`。
- 续跑时（搜 `A dispatch-time model choice is a property of the record now`）按 `record.Model` 重新 `WithSubagentModelOverride`。
- `SubagentModelOverride{Provider, Model}`，没有思考强度。
- `ConfiguredModelClient(cfg, agentType, provider, model)` 建覆盖模型的客户端。
- `runner.go` 里依次包上：`wrapSubagentModelOverrideLLM(llmClient, func(override) { return ConfiguredModelClient(...) })` → `wrapTypedSubagentPromptLLM(llmClient)` → …… → `wrapTypedSubagentToolFilterLLM(llmClient)`。
- `sessionModelClient(sel PrimaryModelState)`（`pkg/run/config.go`）演示了“provider/model + effort → 客户端”的标准写法：`resolveAgentProvider` → `appcfg.WithReasoningEffort` → `llmYAMLsFromResolved` → `NewLLMFromYAML`。

**提示词注入**（`pkg/run/typedsubagent_llm.go`）
- `typedSubagentPromptLLM.Execute`：`if prompt, ok := agent.SystemPromptForSubtype(subtype); ok {`，再追加 `agent.FileAccessScopeSystemPrompt(projectRoot, wsRoot)`，最后 `replaceSystemPrompt`。
- **这就是“改写的提示词要真正生效”的唯一注入点。**

**工具权限**（`pkg/tool/permissions.go`、`pkg/tool/registry.go`）
- `typedSubagentDisallowedTools` 按类型名列出禁用的工具：只读类禁 `write_file`、`edit_file` 和 8 个 `subagent_*`；cavecrew-builder 只禁 `subagent_*`；general-purpose 没有条目，即不限制。
- 这张表被三处读取：
  - `GuardTool`：强制拦截；
  - `VisibleToolsForSubagentSubtype` / `filterToolsForSubagentSubtype`：决定发给模型的工具列表；
  - `TypedSubagentBlocksFileWrites(subagentType)`：被 `pkg/run/subagent.go`（计划模式继承）、`pkg/tool/request_permissions.go`、`pkg/process/worker_cli.go` 调用。
- 子代理类型通过 `tool.WithSubagentType(ctx, name)` / `tool.SubagentTypeFromContext(ctx)`（`pkg/tool/state.go`）在上下文里传递。

**派发记录**（`pkg/agent/subagent_history.go`）
- `HistoryEntry` 有 `ModelProvider/Model`，没有思考强度；`AppendHistory` 写 JSONL，加 `omitempty` 字段向后兼容。

**gateway**
- 写配置的标准路径：`s.persistedConfig()` 读到原样的 forebrain.yaml（不展开 `${ENV}`），改完后 `s.saveAndReload(cfg)` 保存并重新加载，再 `s.rebindAgentRunners()` 重建该主代理的运行实例，让下一个会话生效。范例是 `mcp_write_api.go` 的 `saveMCPLayer`。
- 主代理路由在 `api_extra.go` 的 `agents := api.Group("/agents")` 下，如 `agents.Put("/primary/:id/mcp/servers/:name", …)`。
- 解析路由里的主代理：`s.primaryAgentResolver()` 的 `Resolve(id)`，范例 `resolveMCPAgentID`。
- 分页约定：`readListQuery(r)` / `pageOf` / `writeListPage`（`pkg/gateway/list_page.go`）。
- 模型选项：`s.sessionModelOptions(cfg, agentName)` 返回 `[]sessionModelOption{Provider, Model, Label, Efforts, SupportsImages}`（`api_extra.go`），与会话模型菜单同源。

**前端**
- `views/SubagentsView.vue` 现在是运行中子代理表（`forebrainApi.agentRosterPage`）。本计划**整页重写**，并删掉它对 `agentRosterPage` 的使用；`agentRosterPage` 本身保留，078 的工作台概览可能还在用，删前 grep。
- 共用件（071–076 落地）：
  - `ListToolbar`、`TablePager`、`ListNote`、`RowActions` / `RowAction` / `RowActionSlot`；
  - `useServerList`（默认 `pageSize` 10）、`ForebrainDrawer`（props `open/title/readonly`）、`ConfirmDialog`；
  - `SwitchComponent`、`SwitchRow`（props `title/hint/modelValue/disabled`）、`v-tip`。
- MCP 表 `components/mcp/McpServersTable.vue`：
  - 有单独一列启用开关：表头 `<th>{{ t('mcp.colEnabled') }}</th>`（对应 `<col style="width: 56px" />`），单元格 `<SwitchComponent v-else-if="!entry.pending" …>`；操作列 `<col style="width: 88px" />`；
  - 类型格 `<span v-if="entry.type.sub" class="mcp-ty-sub">`，来自 `mcpRows.ts` 的 `mcpTypeLabel()` / `mcpSubLabel()`；
  - 抽屉 `McpServerDrawer.vue` 的类型下拉用 `t('mcp.transportStdio')` / `t('mcp.transportHttp')`，值为“stdio（本地命令）”“http（远程地址）”。
- 设置页 `views/SettingsView.vue` 的分组数组：`key: 'general'` 组里有 `{ key: 'appearance' }`、`{ key: 'approval' }`；内容区是 `<AppearanceTab v-if="activeTab === 'appearance'" />`、`<SettingsApprovalTab v-else-if=…>` 一串。
- 操作列头：18 处 `<th class="text-right">{{ t('….colActions') }}</th>`，用 `grep -rn "<th class=\"text-right\">{{ t('[a-zA-Z.]*colActions') }}</th>" frontend/src` 找全。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|------|------|----------|
| Go 相关包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/agent ./pkg/config ./pkg/tool ./pkg/run ./pkg/gateway ./pkg/process -count=1` | 每行 `ok` |
| Go 单个用例 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'SubagentCatalog' -count=1 -v 2>&1 \| grep -c '^--- PASS'`（**shell 里 `\|` 写成单个 `\|` 管道符；`-run` 正则里多个名字用单个 `\|`**） | 数字 ≥ 新增用例数 |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/agent ./pkg/config ./pkg/tool ./pkg/run ./pkg/gateway`；`gofmt -l pkg cmd` | 均无输出 |
| 前端单测 | `cd frontend && pnpm test` | 全绿 |
| 类型检查 | `cd frontend && npx vue-tsc --noEmit -p tsconfig.json` | exit 0 |
| e2e | `FOREBRAIN_E2E_SPEC=e2e/<文件> scripts/acceptance/web_e2e.sh` / 全套 `scripts/acceptance/web_e2e.sh` | 末行 `web e2e: PASS` |

> 上表里的 `\|` 只是为了表格排版；在 shell 里输入时用单个 `|`。Go 的 `-run` 是正则，**不要**写成 `\|`，否则一个用例都不匹配却显示 ok。

开工前 `git status --short > $TMPDIR/079-before.txt`。

## Scope

**In scope**
- Go：
  - `pkg/config/agents.go`（新字段、`SubagentsEnabledFor`、子代理配置校验）、`pkg/config/load.go`（调用校验、内置名单）及 `pkg/config` 测试；
  - `pkg/agent/subagent_defs.go`（导出内置名单常量）、`pkg/agent/subagent_history.go`（`ReasoningEffort` 字段）及测试；
  - `pkg/tool/permissions.go`、`pkg/tool/registry.go`、`pkg/tool/state.go`、`pkg/tool/request_permissions.go`（工具策略的上下文传递）及测试；
  - `pkg/process/worker_cli.go`（只改 `TypedSubagentBlocksFileWrites` 的调用）；
  - `pkg/run/subagent_catalog.go`（新建）+ `pkg/run/subagent_catalog_test.go`（新建），以及 `pkg/run/subagent.go`、`pkg/run/typedsubagent_llm.go`、`pkg/run/runner.go`、`pkg/run/config.go` 及其测试；
  - `pkg/gateway/subagents_api.go`（新建）+ `pkg/gateway/subagents_api_test.go`（新建）、`pkg/gateway/api_extra.go`（只加路由）。
- 前端：
  - `frontend/src/lib/api.ts` + `api.test.ts`；
  - `frontend/src/views/SubagentsView.vue` + `.test.ts`（重写）；
  - `frontend/src/components/subagents/SubagentConfigDrawer.vue` + `.test.ts`（新建）；
  - `frontend/src/components/settings/SettingsSubagentsTab.vue` + `.test.ts`（新建）、`frontend/src/views/SettingsView.vue`；
  - `frontend/src/components/mcp/{McpServersTable.vue,McpServersTable.test.ts,mcpRows.ts,mcpRows.test.ts,McpServerDrawer.vue}`；
  - `frontend/src/components/chat/ChatWorkbench.vue` + `ChatWorkbench.test.ts`（宽度统一 340px、标签栏样式与分页，Step 9.6）；`frontend/src/views/WorkshopView.vue`（只去掉传给工作台的 `wide`）；工坊工作台各标签组件里为适应 340px 必须改的样式（只改样式）；
  - 18 个含操作列头的 `.vue`（只改那一个 `<th>` 的类）；
  - `frontend/src/locales/index.ts`；
  - `frontend/e2e/subagent-config.spec.ts`（新建）、`frontend/e2e/mcp-pages.spec.ts`（只改受影响的断言）。

**Out of scope**
- 运行中子代理的展示与操作（对话卡片、抽屉、工作台概览，见 078）。
- 系统子代理的任何修改（只读）。
- 项目级子代理（owner 只要求主代理级）。
- 终端界面：终端没有子代理配置界面；派发走同一套引擎，自动获得自定义子代理。
- 旧的全局 `agents.definitions.<类型>.llm_providers`：保留并继续生效，按 Step 3.6 的优先级处理，不迁移、不删。

## Steps

### Step 1：配置结构与校验

1. `pkg/agent/subagent_defs.go` 导出两份名单：
   ```go
   // PublicSubagentNames are the built-in subagents a model may dispatch, in the order the subagent page lists them.
   var PublicSubagentNames = []string{"general-purpose", "explore", "plan", "verification"}
   // SystemSubagentNames are the built-in subagents the system itself dispatches (plan review, /goal checks, automatic approval, the caveman skill); they cannot be configured.
   var SystemSubagentNames = []string{"plan-reviewer", "goal-evaluator", "guardian", "cavecrew-investigator", "cavecrew-builder", "cavecrew-reviewer"}
   ```
   `PublicTypeNames()` 改为返回 `PublicSubagentNames` 的副本，函数签名不变。
   测试 `TestSubagentNameListsCoverEveryBuiltin`：两份名单的并集等于 `ActiveTypeNames()`，且没有重复。
2. `pkg/config/agents.go` 新增类型，加在 `AgentDefinition` 之后：
   ```go
   // SubagentConfig is one entry of a primary agent's subagent configuration.
   // Name a built-in the model may dispatch (general-purpose, explore, plan, verification) and the entry overrides that built-in for this agent:
   // only Model, ReasoningEffort, SystemPrompt and Enabled apply. Any other name is a custom subagent of this agent.
   type SubagentConfig struct {
       Name            string   `yaml:"name" json:"name"`
       Description     string   `yaml:"description,omitempty" json:"description,omitempty"`
       SystemPrompt    string   `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
       // Model is "provider/model" from this agent's own model list; empty follows the conversation.
       Model           string   `yaml:"model,omitempty" json:"model,omitempty"`
       // ReasoningEffort applies only with Model set.
       ReasoningEffort string   `yaml:"reasoning_effort,omitempty" json:"reasoning_effort,omitempty"`
       // Tools is read_only | edit | pick (custom only; empty means read_only).
       Tools           string   `yaml:"tools,omitempty" json:"tools,omitempty"`
       AllowedTools    []string `yaml:"allowed_tools,omitempty" json:"allowed_tools,omitempty"`
       // Continuable false makes a custom subagent one-shot; nil means continuable.
       Continuable     *bool    `yaml:"continuable,omitempty" json:"continuable,omitempty"`
       // Enabled false keeps the model from dispatching it; nil means enabled.
       Enabled         *bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
   }
   ```
   `AgentDefinition` 加两个字段，带注释：
   - `EnableSubagent *bool \`yaml:"enable_subagent,omitempty" json:"enable_subagent,omitempty"\``：这个主代理自己的子代理开关，nil 时跟随 `agents.defaults.enable_subagent`；
   - `Subagents []SubagentConfig \`yaml:"subagents,omitempty" json:"subagents,omitempty"\``。
   常量：`SubagentToolsReadOnly = "read_only"`、`SubagentToolsEdit = "edit"`、`SubagentToolsPick = "pick"`。
3. 新增 `func SubagentsEnabledFor(cfg *Root, agentName string) bool`：
   - 先看 `cfg.Agents.Definitions[小写 agentName].EnableSubagent`，非 nil 就用它；
   - 否则 `SubagentsEnabled(cfg)`。
   - 原 `SubagentsEnabled` 保留，语义是全局开关。
4. 新增 `func ValidateAgentSubagents(agentID string, def AgentDefinition, publicBuiltins, systemBuiltins []string) error`，规则如下，错误信息以 `agents.definitions.<id>.subagents[<i>]` 开头：
   - `name` 去空白、小写后必须匹配 `^[a-z][a-z0-9-]{1,62}$`，同一主代理内不重名；
   - 是 `systemBuiltins` 里的名字 → 报错“系统子代理不能配置”；
   - 是 `publicBuiltins` 里的名字（覆盖项）→ `description`、`tools`、`allowed_tools`、`continuable` 必须为空，否则报错“内置子代理只能改模型、思考强度、提示词与启用”；
   - 自定义项 → `description` 与 `system_prompt` 必填；`tools` 只能是空 / `read_only` / `edit` / `pick`；`pick` 时 `allowed_tools` 至少一个，且不能含 `subagent_` 开头的名字；
   - `model` 非空时必须是 `provider/model` 形式（恰好一个 `/`，两边非空）；`reasoning_effort` 非空时 `model` 必须非空。**不**校验模型是否在模型列表里：提供方被删后配置仍要能加载，派发时再报错（Step 3.6）。
5. `pkg/config/load.go`：
   - 在 `validatePrimaryAgents` 里对每个 definition 调 `ValidateAgentSubagents`；为避免 import 环，名单用本包的副本 `builtInPublicSubagentNames` / `builtInSystemSubagentNames`。
   - 在 `pkg/agent` 的测试里断言副本与 `agent.PublicSubagentNames` / `SystemSubagentNames` 相同：
     - `pkg/agent` 不能 import `pkg/config` 时，就放在 `pkg/config` 的测试里，硬编码同一份列表；
     - 再加一条注释，说明改名单要两边一起改。
   - 现有 `builtInSubagentDefinitionNames`（防止当成主代理）保持不变。
6. 测试（`pkg/config`，照现有 `load_test.go` 读 YAML 的写法）：
   - 一份含 `enable_subagent: false` 与两条 `subagents`（一条覆盖 explore 的 `model` + `reasoning_effort` + `system_prompt`，一条自定义 `pick`）的配置能加载，字段读回正确；
   - `SubagentsEnabledFor`：主代理 nil → 跟随全局；主代理 false 而全局 true → false；
   - 校验的每条规则各一个反例。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/config ./pkg/agent -count=1` 两行 `ok`；`gofmt -l pkg` 无输出。

### Step 2：`pkg/run/subagent_catalog.go`——一个主代理看到的子代理目录

新建文件，导出：
```go
// SubagentCatalog is one primary agent's view of the subagent types: the built-ins it keeps enabled and its overrides of them, and its own custom subagents.
type SubagentCatalog struct { /* agentName, cfg entries by name, ... */ }

func SubagentCatalogFor(cfg *appcfg.Root, agentName string) SubagentCatalog

// Public lists what the model may dispatch: enabled built-ins (in agent.PublicSubagentNames order), then enabled custom subagents (in config order).
func (c SubagentCatalog) Public() []agent.Definition
// ResolvePublic is ResolvePublicSubtype for this agent: a disabled built-in answers "subagent_type %q is disabled for this agent", an unknown name lists Public() names.
func (c SubagentCatalog) ResolvePublic(name string) (agent.Definition, error)
// Prompt is the system prompt a typed run of name uses: a custom subagent's own, this agent's rewrite of a built-in, else the built-in's.
func (c SubagentCatalog) Prompt(name string) (string, bool)
// Model is the model this agent pins name to; ok false when it follows the conversation.
func (c SubagentCatalog) Model(name string) (provider, model, effort string, ok bool)
// ToolPolicy is a custom subagent's tool policy; ok false for built-ins (their policy stays the static table).
func (c SubagentCatalog) ToolPolicy(name string) (tool.SubagentToolPolicy, bool)
// EnumDescription is the subagent_type schema description: the existing built-in sentence for the enabled built-ins, then "<name>: <description>." for each enabled custom one, then the fork sentence.
func (c SubagentCatalog) EnumDescription() string
```
- 自定义子代理的 `agent.Definition`：`Name`、`Description`、`Source: "custom"`、`SystemPrompt`；`Continuable` 为 `*Continuable` 或 true；`OneShot = !Continuable`。
- `EnumDescription` 里内置部分**逐字沿用**现有 `patchSubagentTypeEnum` 中的英文句子，只列出启用的那几个；末尾“Omit subagent_type to fork…”那句原样保留。
- `Model` 拆分 `provider/model`；`Prompt` 对内置先看覆盖项的 `SystemPrompt`（去空白后非空才算改写），再退回 `agent.SystemPromptForSubtype`。
- 测试 `pkg/run/subagent_catalog_test.go`，用例名都带 `SubagentCatalog`：
  - `TestSubagentCatalogListsEnabledBuiltinsThenCustom`；
  - `TestSubagentCatalogDisabledBuiltinIsRefusedWithReason`；
  - `TestSubagentCatalogPromptPrefersRewriteThenBuiltin`：改写为非空就用改写，空串退回内置原文，并与 `agent.SystemPromptForSubtype` 逐字相等；
  - `TestSubagentCatalogCustomDefinitionFields`；
  - `TestSubagentCatalogEnumDescriptionNamesCustomSubagents`；
  - `TestSubagentCatalogModelSplitsProviderAndModel`。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'SubagentCatalog' -count=1 -v 2>&1 | grep -c '^--- PASS'` 输出 ≥ 6。

### Step 3：接入运行时——派发、提示词、工具、模型、开关全部按主代理的目录走

1. **开关**：`RegisterSubagentTool` 里把 `appcfg.SubagentsEnabled(fac.appConfig())` 改为 `appcfg.SubagentsEnabledFor(fac.appConfig(), fac.AgentName)`。其上方注释同步写明“主代理自己的开关覆盖全局”。
2. **类型枚举与说明**：
   - `subagentTypeEnumValues()` 与 `patchSubagentTypeEnum` 改为接收 `cat SubagentCatalog`：`enum` 取 `cat.Public()` 的名字，`description` 取 `cat.EnumDescription()`；
   - `applySubagentTypeEnumToTool(t, fieldPath)` 增加参数 `cat`；所有调用处（`grep -n "applySubagentTypeEnumToTool(" pkg/run/subagent.go`）传入在 `RegisterSubagentTool` 开头算好的 `cat := SubagentCatalogFor(fac.appConfig(), fac.AgentName)`；
   - `subagentSchemaRaw(...)`（工具目录元数据）同理。
   - `Public()` 为空时（全部停用），整组 subagent_* 工具**不注册**，与开关关闭相同。
3. **解析**：
   - 两处 `resolve: agent.ResolvePublicSubtype,` 改为 `resolve: SubagentCatalogFor(fac.appConfig(), fac.AgentName).ResolvePublic,`；
   - `subagent_run` 结果元数据那处 `agent.ResolvePublicSubtype(subType)` 同样改。
   - 计划审阅、目标检查的 `agent.ResolveSubtype` **不动**：系统子代理不可配置。
   - 续跑（`subagent_continue` 一类）读的是历史记录里的 `AgentType`。自定义子代理被删后续跑，要返回清楚的错误：`subagent type %q no longer exists for this agent`。在解析该记录类型处加判断，`grep -n "record.AgentType" pkg/run/subagent.go` 定位。
4. **提示词（必须真正生效）**：
   - `typedsubagent_llm.go`：`wrapTypedSubagentPromptLLM(inner llm.LLM)` 改为 `wrapTypedSubagentPromptLLM(inner llm.LLM, prompt func(subtype string) (string, bool))`；`Execute` 里的 `agent.SystemPromptForSubtype(subtype)` 改为 `w.prompt(subtype)`；
   - `prompt` 为 nil 时退回 `agent.SystemPromptForSubtype`，旧测试不用改；
   - 后面追加 `FileAccessScopeSystemPrompt` 的逻辑**不变**。
   - `runner.go` 调用处传 `func(subtype string) (string, bool) { return SubagentCatalogFor(r.AppCfg, r.AgentName).Prompt(subtype) }`。`r.AgentName` 为空时用 `"main"`，照 `activeAgentNameForModel()` 的取法。
5. **工具权限**：
   - `pkg/tool/state.go` 新增：
     ```go
     // SubagentToolPolicy is a custom subagent's tool policy, carried on its run's context (built-ins keep the static table).
     type SubagentToolPolicy struct { Mode string; Allowed map[string]struct{} }
     func WithSubagentToolPolicy(ctx context.Context, p SubagentToolPolicy) context.Context
     func SubagentToolPolicyFromContext(ctx context.Context) (SubagentToolPolicy, bool)
     func (p SubagentToolPolicy) Blocks(toolName string) bool
     // SubagentBlocksFileWrites is TypedSubagentBlocksFileWrites that also reads a custom policy on ctx.
     func SubagentBlocksFileWrites(ctx context.Context) bool
     ```
   - `Blocks` 的规则：
     - 任何 `subagent_` 开头的工具都拦；
     - `read_only` 再拦 `write_file`、`edit_file`，与内置 explore 同一套；
     - `edit` 只拦 `subagent_*`；
     - `pick` 拦所有不在 `Allowed` 里的工具。
   - `GuardTool`：先看 `SubagentToolPolicyFromContext`，有就按它拦，错误文本照现有格式 `subagent_type %q: tool %q blocked by policy`；没有才查静态表。
   - `filterToolsForSubagentSubtype`（`typedsubagent_llm.go`）同样先看上下文策略：`typedSubagentToolFilterLLM.Execute` 把 `ctx` 传进去，签名改为 `filterToolsForSubagentSubtype(ctx, subtype, tools)`。
   - `TypedSubagentBlocksFileWrites` 的三个调用方都改用 `SubagentBlocksFileWrites(ctx)`：
     - `pkg/run/subagent.go` 的计划模式继承，这里还没有子代理上下文，就直接用 `cat.ToolPolicy(rawSubtype)` 的 `Mode == read_only` 判断；
     - `pkg/tool/request_permissions.go`；
     - `pkg/process/worker_cli.go`。
   - **在哪里放策略**：`typedSubagentExecContext(prep)` 与续跑的 `hostCtx`（`tool.WithSubagentType(hostCtx, …)` 那里）都加上 `WithSubagentToolPolicy`，策略来自 `SubagentCatalogFor(fac.appConfig(), fac.AgentName).ToolPolicy(agentType)`。
6. **模型与思考强度**：
   - `SubagentModelOverride` 加 `Effort string`；
   - `HistoryEntry` 加 `ReasoningEffort string \`json:"reasoning_effort,omitempty"\``。
   - `prepareSubagentExecutionResolved` 里，在“dispatch-time override 写进 entry”那段之后加：没有 dispatch-time override，且 `cat.Model(agentType)` 有值时，写进 `entry.ModelProvider / Model / ReasoningEffort`。
   - 优先级：派发时指定（计划审阅）> 主代理的子代理配置 > 旧的全局 `agents.definitions.<类型>.llm_providers` > 对话的模型。
   - 续跑处 `WithSubagentModelOverride(hostCtx, SubagentModelOverride{Provider: record.ModelProvider, Model: record.Model})` 加上 `Effort: record.ReasoningEffort`；首次执行的上下文同样带上。实际在哪里设置，`grep -n "WithSubagentModelOverride(" pkg/run/subagent.go` 全部看一遍。
   - `runner.go` 里 `wrapSubagentModelOverrideLLM` 的构造函数改为按 `sessionModelClient` 的写法建客户端：`resolveAgentProvider(r.AppCfg, agent, modelSelector{…})` → `entry.Params = appcfg.WithReasoningEffort(entry.Params, override.Effort)` → `NewLLMFromYAML`。`ConfiguredModelClient` 加 `effort` 参数，或新增一个带 effort 的版本，二选一，保持现有调用方编译通过。模型不在该主代理模型列表里时，返回现有的 “model … is not configured for this session” 错误。
   - `SubagentOwnModel(r, agentType)`：先看 `SubagentCatalogFor(r.AppCfg, r.AgentName).Model(agentType)`，有就返回它；没有再走旧逻辑。它被终端和 web 用来显示子代理所用模型。
   - 派发事件里的 `reasoning_effort` 取 `entry.ReasoningEffort`（非空时），`grep -n "reasoning_effort" pkg/run/*.go` 定位。
7. 测试（`pkg/run`），照 `subagent_test.go` 的 `TestRegisterSubagentToolOmitsTheFamilyWhenSubagentsAreDisabled`、`TestPlanReviewSpawnEventNamesTheChosenModel` 与 `typedsubagent_llm_test.go` 的 `TestTypedSubagentPromptReplacesSystemPrompt` 写：
   - `TestRegisterSubagentToolHonoursThePrimaryAgentSwitch`：全局开、主代理关 → 不注册；全局关、主代理开 → 注册。
   - `TestSubagentTypeEnumListsCustomAndHidesDisabled`：注册后 `subagent_run` 的 schema 里 `enum` 含自定义名、不含被停用的 verification，`description` 含自定义子代理的说明。
   - `TestTypedSubagentPromptUsesTheAgentsRewrite`：explore 被改写，发给内层 LLM 的系统消息以改写内容开头，后面跟 `FILE ACCESS SCOPE`。
   - `TestTypedSubagentPromptUsesCustomPrompt`：同上，对象换成自定义子代理。
   - `TestCustomReadOnlySubagentCannotWrite`：上下文带 `read_only` 策略时，`GuardTool(ctx, "write_file")` 报错，`read_file` 不报错，过滤后的工具列表不含 `write_file`。
   - `TestCustomPickSubagentSeesOnlyChosenTools`：`pick` 只留下勾选的工具，`subagent_run` 永远不在其中。
   - `TestCustomSubagentModelAndEffortReachTheEntry`：派发自定义子代理后，历史记录里 `Model/ReasoningEffort` 等于配置，派发事件里也有。
   - `TestDispatchOverrideOutranksAgentSubagentModel`。
   - `TestContinueAfterCustomSubagentDeletedSaysSo`。

**Verify**：
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tool ./pkg/process -count=1` 三行 `ok`；
- `-v` 跑上面 9 个新用例，`grep -c '^--- PASS'` 输出 9；
- `gofmt -l pkg` 无输出。

### Step 4：gateway 接口（新建 `pkg/gateway/subagents_api.go`）

在 `api_extra.go` 的 `agents` 组里注册：
```go
agents.Get("/defaults/subagents", s.handleSubagentsDefaultGet)   // {"enabled": bool}
agents.Put("/defaults/subagents", s.handleSubagentsDefaultPut)   // body {"enabled": bool} → agents.defaults.enable_subagent
agents.Get("/primary/:id/subagents", s.handleAgentSubagentsList)
agents.Post("/primary/:id/subagents", s.handleAgentSubagentCreate)
agents.Put("/primary/:id/subagent-switch", s.handleAgentSubagentSwitch)    // body {"enabled": bool}
agents.Get("/primary/:id/subagents/:name", s.handleAgentSubagentGet)
agents.Put("/primary/:id/subagents/:name", s.handleAgentSubagentUpdate)
agents.Delete("/primary/:id/subagents/:name", s.handleAgentSubagentDelete)
agents.Put("/primary/:id/subagents/:name/enabled", s.handleAgentSubagentEnabled) // body {"enabled": bool}
```
- 若路由器对 `/defaults/...` 与已有的 `/:agentId/cancel` 冲突（启动时 panic 或测试 404），改用 `api.Get("/subagent-defaults", …)`，并在报告里写明。

**行的形状**（列表与详情共用）：
```json
{ "name": "explore", "kind": "builtin|custom|system", "description": "...",
  "model_provider": "deepseek", "model": "deepseek-v4-flash", "reasoning_effort": "low",
  "model_source": "agent|global|", "tools": "read_only|edit|pick|none", "allowed_tools": [],
  "continuable": false, "enabled": true, "prompt_overridden": false }
```
- `enabled`：系统子代理为 `null`，内置与自定义为布尔。
- `model_source`：`agent` 表示这个主代理的子代理配置，`global` 表示旧的 `agents.definitions.<类型>.llm_providers`，空表示跟随对话。
- 内置的 `tools`：在 `typedSubagentDisallowedTools` 里禁了 `write_file` 的为 `read_only`；guardian 为 `none`；其余为 `edit`。新增导出函数 `tool.BuiltinSubagentToolsTier(name string) string` 实现这条规则，并写测试。
- 内置的 `description` 用 `Definition.Description`（英文），前端会按名字显示中文。

**列表** `GET …/subagents`：
- 遵守 071 的分页约定：`q` 匹配名称与说明，筛选参数 `kind` 取值 custom / builtin / system，带 `page` 时 `writeListPage(w, "rows", …)`；
- 顶层额外字段 `{"switch": <SubagentsEnabledFor 的结果>, "global": <SubagentsEnabled 的结果>}`；
- 顺序：自定义（配置顺序）→ 内置（`agent.PublicSubagentNames` 顺序）→ 系统（`agent.SystemSubagentNames` 顺序）。

**详情** `GET …/subagents/:name`：在行的字段之外加：
- `system_prompt`：生效的全文，即自定义的、改写的或内置原文；
- `default_prompt`：仅内置与系统子代理，为 `Definition.SystemPrompt` **原文**。

**写入**：一律 `persistedConfig()` → 改 `cfg.Agents.Definitions[id]` → `ValidateAgentSubagents` → `saveAndReload` → `rebindAgentRunners()`，写法照 `saveMCPLayer`。校验失败返回 400 和错误原文，名字冲突返回 409。
- **新建**：只能建自定义；body 为 `SubagentConfig` 的 JSON。名字与内置或已有项冲突时返回 409。**插到列表最前面**，即“新建的排最前”。
- **更新**：
  - 自定义：整条替换，`name` 不可改；
  - 内置：只接受 `model`、`reasoning_effort`、`system_prompt`、`reset_prompt: true`（清空改写，即“恢复默认”）、`enabled`。覆盖项不存在就新建；覆盖项变成全空时删除这一条，保持配置干净；
  - 系统：返回 400。
- **删除**：只能删自定义，内置与系统返回 400。
- **单行启用** `…/:name/enabled`：
  - 写入这一行的 `Enabled`（内置写进覆盖项）；
  - 然后按 owner 的联动规则：打开且这个主代理当前 `SubagentsEnabledFor` 为 false → 主代理 `EnableSubagent = true`；关闭后所有内置与自定义都已禁用 → `EnableSubagent = false`；
  - 返回 `{"switch": bool}`。
- **主代理开关** `…/subagent-switch`：
  - 写 `EnableSubagent = &enabled`；
  - 并把这个主代理的 4 个内置（没有覆盖项就新建）和全部自定义的 `Enabled` 都设为 `enabled`；
  - 返回 `{"switch": enabled}`。
- **全局开关** `PUT /defaults/subagents`：写 `cfg.Agents.Defaults.EnableSubagent`，不改任何主代理的覆盖。保存后对所有主代理 rebind；没有现成函数就逐个 rebind，或调用 pool 的全量重建。

**测试**（`subagents_api_test.go`，配置造法照 `api_extra_test.go` 里 MCP 写接口测试的 `newMCPWriteFixture`）：
- 列表顺序与 `kind` 筛选、分页 `total`；
- 详情的 `default_prompt` 与 `agent.SystemPromptForSubtype` 逐字相等；
- 新建插在最前、重名 409、改内置的提示词后 `prompt_overridden=true`；
- `reset_prompt` 后覆盖项被删除；
- 停用内置后配置里 `enabled: false`；
- 主代理开关批量改写所有行；
- 单行联动的两条规则；
- 删除内置返回 400；
- 全局开关写 `agents.defaults.enable_subagent`；
- 每次写入后 `config.LoadPersisted` 读回的文件里有对应字段。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'Subagent' -count=1` 结果 `ok`，`-v` 统计新用例都在 `--- PASS` 里；`gofmt -l pkg` 无输出。

### Step 5：前端 API（`lib/api.ts`）

新增类型：
```ts
export type SubagentKind = 'builtin' | 'custom' | 'system'
export type SubagentTools = 'read_only' | 'edit' | 'pick' | 'none'
export interface SubagentConfigRow { name; kind; description; modelProvider; model; reasoningEffort; modelSource: 'agent' | 'global' | ''; tools: SubagentTools; allowedTools: string[]; continuable: boolean; enabled: boolean | null; promptOverridden: boolean }
export interface SubagentConfigDetail extends SubagentConfigRow { systemPrompt: string; defaultPrompt?: string }
```
新增方法：
- `subagentConfigPage(agentId, query & { kind? })` 返回 `{ rows, total, page, pageSize, switch, global }`；
- `subagentConfig(agentId, name)`、`createSubagentConfig(agentId, body)`、`updateSubagentConfig(agentId, name, body)`、`deleteSubagentConfig(agentId, name)`；
- `setSubagentEnabled(agentId, name, enabled)`、`setAgentSubagentSwitch(agentId, enabled)`；
- `subagentDefaults()` / `setSubagentDefaults(enabled)`。

要求：
- 走现有的 axios 实例即可。字段名 snake → camel 由拦截器完成；提示词、工具名都在“值”里，不受影响。**写入**的 body 手写 snake_case（`system_prompt`、`reasoning_effort`、`allowed_tools`、`reset_prompt`），或用文件里已有的 `toSnakeCase`。
- `api.test.ts` 加一例：`updateSubagentConfig` 发出的 body 键名是 snake_case。

**Verify**：`cd frontend && pnpm test -- src/lib/api.test.ts` 通过；`npx vue-tsc --noEmit -p tsconfig.json` exit 0。

### Step 6：子代理页（`views/SubagentsView.vue` 整页重写）

照原型 `pages.subagents`，写法照 071–076 落地的表格页（例如 `views/ProvidersView.vue`、`components/mcp/McpServersTable.vue`）。

**数据**：`useServerList((q) => forebrainApi.subagentConfigPage(activeId, { ...q, kind }), { pageSize: 10, filters: { kind: '' } })`。`activeId` 取自 `usePrimaryAgents()`，切换主代理时重新加载。从响应里取 `switch`，写法照 `ToolsView.vue` 取 `categories`。

**工具栏**（`ListToolbar`）：
- 左：搜索框 `搜索子代理`（`forebrain-field h-[34px]`，240px）+ 类型筛选 `<select>`（全部类型 / 内置 / 自定义 / 系统）。
- 右：
  - `子代理功能` 开关：`SwitchComponent`，旁边只写“子代理功能”，悬停提示“关闭会把下面所有子代理设为禁用” / “开启会把下面所有子代理设为启用”；
  - `新建子代理`（主色，`Plus` 图标）；
  - `刷新`。
- 开关切换调 `setAgentSubagentSwitch` 后 `reload()`。

**表格**（`forebrain-table--fixed`，列宽照原型）：
- **子代理**：等宽粗体名称；`promptOverridden` 时加黄徽标“已改写”（`v-tip` 写“提示词已为当前主代理改写”）；下一行灰字说明，截断加 `v-tip.trunc`。
  - 内置与系统的说明用 locale `subagents.desc.<name>`（中文见原型 `subDefs`），没有对应键就用后端的英文说明；
  - 自定义直接用配置里的说明。
- **类型**：徽标，自定义 `自定义`、内置 `内置`（成功色）、系统 `系统`（灰）。
- **模型**：
  - 有模型：等宽 `provider/model`，有思考强度时后接灰字 ` · ` 加强度名（`effortLevelLabel`，078 新增）；
  - `modelSource === 'global'` 时加 `v-tip` “来自全局配置 agents.definitions.<name>”；
  - 没有模型：灰字“跟随主代理”；
  - 系统子代理：plan-reviewer 写“审批时选择”，其余“跟随主代理”（locale `subagents.modelBySystem.<name>`，缺省“跟随主代理”）。
- **工具**：只读 / 可编辑 / 指定 N 个 / 无工具。
- **操作**（列头居中，见 Step 9）：
  - 第一个是启用开关：内置与自定义用 `SwitchComponent`，调 `setSubagentEnabled` 后 `reload()`，因为可能联动主开关；系统子代理放一个与开关等宽的空位（新增 `RowActionSlot` 的 `wide` 变体，或在本页用一个 32px 的 `span`）；
  - 然后：自定义是 `编辑`（Pencil）+ `删除`（Trash2，危险色）；内置是 `编辑` + 空位；系统是 `查看`（Eye）+ 空位。
- **没有状态列。**

**分页与说明**：
- 分页用 `TablePager`；
- `ListNote` 写：“内置与自定义的子代理启用后由模型按说明派发；系统子代理在计划审阅、/goal、自动审批等场合由系统调用，只能查看。保存在 forebrain.yaml，新会话生效。”

**删除**：`ConfirmDialog`，标题“删除子代理 {name}？”，正文“从 forebrain.yaml 里删除它的配置；进行中的派发不受影响，之后模型不能再派它。”，危险按钮“删除”。

**测试** `SubagentsView.test.ts`：
- 表头只有 5 列，没有“状态”；
- 三种类型的操作栏组成正确，开关与图标对齐；
- 切换主开关调用 `setAgentSubagentSwitch`；
- 单行开关调用 `setSubagentEnabled`；
- 删除先弹确认框。

**Verify**：`cd frontend && pnpm test -- src/views/SubagentsView.test.ts` 通过。

### Step 7：子代理抽屉（新建 `components/subagents/SubagentConfigDrawer.vue`）

**外壳**：`ForebrainDrawer`（`:readonly="mode === 'view'"`，宽 560px，与 MCP 抽屉相同）。标题：
- 新建：“新建子代理”；
- 编辑自定义：“编辑子代理 · {name}”；
- 编辑内置：“编辑内置子代理 · {name}”；
- 查看系统：“查看系统子代理 · {name}”，并标“只读”。

打开时调 `subagentConfig(agentId, name)` 取详情。

**字段**（顺序照原型 `subForm`，两列网格的放在同一行）：
1. 名称 | 模型。
   - 名称：新建时可填，占位“小写字母、数字与短横线，如 db-migrator”，前端校验与后端正则一致；编辑时只读。
   - 模型：下拉，第一项“跟随主代理（{对话模型}）”，其余是 `provider/model` 列表。列表来自 `sessionModelOptions` 同源的新接口，或复用会话模型接口的 `options`；没有不依赖会话的接口时，在 Step 4 加 `GET /api/agents/primary/:id/model-options` 返回 `s.sessionModelOptions(cfg, id)`，并补测试。
2. 思考强度 | 运行方式。
   - 思考强度：只有选了具体模型才可选，选项是“跟随模型默认”加该模型的 `efforts`（用 `effortLevelLabel`）。模型是“跟随主代理”时禁用，显示“跟随主代理”，悬停提示“先指定模型再选思考强度”。
   - 运行方式：自定义可选“可继续对话 / 完成即结束”；内置只读显示。
3. 说明：自定义可编辑，占位“主代理靠这句话决定什么时候派它，写清用途”；内置与系统只读，显示中文说明。
4. 系统子代理多一行“何时调用”，只读，locale `subagents.when.<name>`，内容照原型。
5. 工具权限：
   - 自定义：`RadioField` 竖排三项：只读（读文件、搜索、只读命令）/ 可编辑（全部工具，但不能再派子代理）/ 指定工具（只用下面勾选的工具）。
   - 选“指定工具”时下面出现勾选区：搜索框 + 三列复选框。工具来自 `forebrainApi.toolsPage`：每次 `pageSize: 100`，循环取完所有页。排除 `subagent_` 开头的工具。区域下面写“已选 N 个。subagent_* 不在可选范围内：子代理不能再派子代理。”
   - 内置与系统：只读文本“{只读/可编辑/无工具}（内置，不能改）”。
6. 系统提示词：等宽多行文本框，最小高 320px，可竖向拉伸。
   - 内置与系统显示 `systemPrompt`，也就是**完整原文**或改写后的内容；
   - 内置改写过时，标签旁有黄徽标“已改写”，右侧链接“恢复默认”。点击后文本框换成 `defaultPrompt`，并在保存时发 `reset_prompt: true`。
   - 系统子代理只读。
7. 底部灰字：
   - 内置：“只影响当前主代理 {id}；保存在 forebrain.yaml，新会话生效。”
   - 自定义：“保存在 forebrain.yaml 的主代理 {id} 下，新会话生效。”
   - 系统：“系统子代理只能查看。”

**底部按钮**：取消 / 创建（或保存）；查看时只有“关闭”。保存失败时在底部显示“没有保存：”加服务端原文，抽屉不关，写法照 MCP 抽屉。

**测试** `SubagentConfigDrawer.test.ts`：
- 内置详情的提示词框值等于后端给的完整原文；
- 选“跟随主代理”时思考强度禁用；
- 选“指定工具”时出现勾选区，且不含 `subagent_run`；
- 内置“恢复默认”后保存发出 `reset_prompt: true`；
- 系统子代理所有输入只读、底部只有“关闭”；
- 409 时显示错误且不关闭。

**Verify**：`cd frontend && pnpm test -- src/components/subagents` 通过。

### Step 8：设置页“子代理”标签

- `SettingsView.vue`：`general` 组在 `approval` 之后加 `{ key: 'subagents', label: t('settings.tabSubagents') }`；内容区加 `<SettingsSubagentsTab v-else-if="activeTab === 'subagents'" />`。
- 新建 `components/settings/SettingsSubagentsTab.vue`，照 `MemorySwitchesTab.vue`：
  - 一条 `SwitchRow`：标题“启用子代理功能”，提示“允许模型派发子代理。对所有主代理生效；单个主代理可在它的“子代理”页覆盖。”；
  - `ListNote`：“写在 forebrain.yaml · agents.defaults.enable_subagent；新会话生效。”；
  - 底部“保存”，调 `setSubagentDefaults`。
- 测试：读到 `subagentDefaults()` 的值；保存调用一次。

**Verify**：`cd frontend && pnpm test -- src/components/settings/SettingsSubagentsTab.test.ts src/views/SettingsView` 通过。

### Step 9：全站表格与 MCP 的统一

1. **操作列头居中**：
   - 用 `grep -rn "<th class=\"text-right\">{{ t('[a-zA-Z.]*colActions') }}</th>" frontend/src` 找全，2026-10-18 有 18 处；
   - 每处把 `text-right` 改成 `text-center`，本计划新写的子代理表也用 `text-center`；
   - 操作单元格本身的对齐不改。
2. **MCP 表的启用开关进操作列**（`McpServersTable.vue`）：
   - 删掉启用那一列（表头、`<col>`、单元格）；
   - 操作单元格 `RowActions` 的第一个元素放原来的 `SwitchComponent`（同样的禁用条件与 `v-tip`）；
   - 没有开关的行（待确认、已拒绝、其它层级的行）放一个与开关等宽的空位，保证图标纵向对齐；
   - 操作列宽照原型 `.mcp-tb` 的 132px；
   - 更新 `McpServersTable.test.ts` 里找启用列的断言。
3. **MCP 类型列只写传输方式**：
   - `mcpRows.ts` 的 `mcpTypeLabel()` 去掉 `sub`，或让它永远为空，并删掉 `mcpSubLabel()`；
   - 表格里删掉 `mcp-ty-sub` 那个 `span` 与样式；
   - 抽屉里如有用到 OAuth / 请求头的信息保持不变；
   - `mcpRows.test.ts` 相应改；
   - locale `mcp.type.headers` 不再使用就删除。
4. **MCP 抽屉类型选项**：locale `mcp.transportStdio` 改为 `stdio`、`mcp.transportHttp` 改为 `http`（中英文都改）。
5. 跑一遍 `grep -rn "colEnabled\|mcp-ty-sub" frontend/src`，确认 MCP 部分没有残留。
6. **两个工作台统一：宽度、标签的样式与交互都一样**（owner 2026-10-18，原型 v82，状态按钮“工作台 · 概览”与“工坊 · 改进已有技能”，代码 `benchTabsHtml`、`.bench`、`.bench-tabs`）。改的是 `components/chat/ChatWorkbench.vue`，对话页与技能工坊共用它：
   - **宽度**：
     - `.chat-workbench` 的 `width: 300px` 与 `.chat-workbench--wide` 的 `420px` **统一为 340px**；
     - 删掉 `--wide` 变体，以及 `WorkshopView.vue` 传的 `wide`。若删 prop 牵动的文件超出 Scope，就保留 prop 但让两种宽度都等于 340px，并在报告里写明；
     - 窄屏覆盖层 `@media (max-width: 1023px)` 里同样用 `min(340px, calc(100vw - 64px))`。
   - **标签样式**：
     - `.bench-tab-btn` 的左右内边距从 7px 改为 **6px**，两个工作台一样，更紧凑；
     - 加 `white-space: nowrap; flex: none`，标签永不折行；
     - 标签**靠左紧挨着排**，间距 2px，与对话页一致，**不要两端对齐或均分撑满**。
   - **标签多于 5 个时（技能工坊的 8 个）一次显示 5 个**（340px、6px 内边距下，任意相邻 5 个加左右两个按钮最宽约 333px，放得下；显示 4 个会在最后一个标签与右键之间空出约 70px，owner 指出过这个空白）：
     - 右侧 `ChevronRight` 图标按钮（18px 宽，`v-tip:top="右边的标签"`，`aria-label` 同），用 `margin-left: auto` 贴在最右；点一次窗口右移一个，到最后一个时禁用；
     - 窗口移动过（第一个不是“概览”）时，最左出现 `ChevronLeft`（`v-tip:top="左边的标签"`），这时标签栏左内边距从 10px 收到 2px，给按钮让位；
     - **选中的标签变化时**（点击、或别处把 `active` 改到窗口外，如工坊进入时默认“变更文件”），窗口自动移到能看到它的位置；点左右按钮只移动窗口、不改选中；
     - 对话页只有 4 个标签，不显示任何按钮。
     - 右键固定贴最右（不要紧跟在最后一个标签后面），连续点击时位置不动；最后一个标签与右键之间的余量在 5–30px 之间。
   - **实现**：
     - 用 `offset` ref 加计算属性 `shownTabs = allTabs.slice(offset, offset + 5)`，不要靠横向滚动条；
     - 分页时去掉 `overflow-x: auto`；
     - 样式照原型 `.bench-tabs.paged`、`.bench-tabs.paged.moved`、`.bench-tab-strip`、`.bench-tab-nav`。
   - **内容区在 340px 下不能挤坏**（原型 `.bench-b .card-h`、`.bench-b .tb`）：
     - 工坊各标签卡片的标题不折行，标题右侧的控件放不下时整体换到下一行；
     - 卡片里的表格表头不折行，单元格左右内边距 6px。
     - 逐个打开测试用例、评测、触发描述、版本、变更文件、工作目录，截图确认没有竖排的标题、没有横向滚动。
   - **测试**（`ChatWorkbench.test.ts`）：
     - 8 个标签时只渲染 5 个；
     - 点右键 3 次后显示最后 5 个且右键禁用；
     - 点左键回退一个；
     - 把 `active` 设为最后一个标签时窗口自动包含它；
     - 4 个标签时没有左右键；
     - 根元素没有 `chat-workbench--wide` 类（或宽度两者一致）。

**Verify**：
- `cd frontend && pnpm test -- src/components/mcp src/components/chat/ChatWorkbench.test.ts` 通过；
- `grep -rn "<th class=\"text-right\">{{ t('[a-zA-Z.]*colActions') }}</th>" frontend/src | wc -l` 输出 `0`。

### Step 10：文案

`locales/index.ts` 中英文新增本计划用到的键。**已存在的键只改值**（先 `grep -n "'键名'"`），避免 TS1117。键的分组如下：

- `subagents.*`：
  - 工具栏、列名、类型名、工具三档与“无工具”、“跟随主代理”、“已改写”、“恢复默认”；
  - 抽屉标题与字段标签、底部说明、删除确认；
  - `subagents.desc.<10 个内置名>`、`subagents.when.<6 个系统名>`、`subagents.modelBySystem.plan-reviewer`。
  - 中文照原型 `subDefs` 与 `subForm`；英文写简洁对应。
- `settings.tabSubagents` 与设置标签的两句。

删除旧的运行中列表专用键（`subagents.colTask`、`subagents.colSession`、`subagents.startedAt`、`subagents.cancelAll`、`subagents.openSession`、`subagents.empty` 等）。**先 grep 确认 078 的工作台和抽屉没有在用**，在用的保留。

**Verify**：`cd frontend && npx vue-tsc --noEmit -p tsconfig.json` exit 0；`pnpm test` 全绿。

### Step 11：e2e

新建 `frontend/e2e/subagent-config.spec.ts`，写法照 `e2e/mcp-pages.spec.ts`：用真 gateway；配置的读写走接口，并用 `page.request` 读回原始 JSON 核对。用例：

1. 打开 `/subagents`：
   - 表头依次是 子代理 / 类型 / 模型 / 工具 / 操作；
   - 前 10 行里先是自定义（种了一条），然后 4 个内置，然后系统；
   - 第 2 页有剩下的系统子代理。
2. 打开 explore 的编辑抽屉：提示词框的值等于 `GET …/subagents/explore` 的 `default_prompt`。改写提示词、指定一个模型和思考强度，保存后：
   - 行上出现“已改写”和 `provider/model · 强度`；
   - forebrain.yaml 里（`GET /api/config` 原文，或直接读 E2E_HOME 下的文件）有对应字段。
3. “恢复默认”后保存：“已改写”消失，配置里的 `system_prompt` 被删除。
4. 新建自定义子代理（选“指定工具”，勾两项），保存后它排在第一行，类型“自定义”，工具“指定 2 个”。
5. 主开关关闭 → 所有行的开关都是关；再打开一行 → 主开关变开；关掉所有行 → 主开关变关。
6. 系统子代理“查看”：抽屉标“只读”、底部只有“关闭”、提示词非空。
7. 设置 → 子代理：切换并保存后，`GET /api/agents/defaults/subagents` 返回新值。
8. MCP 页：操作列第一个是开关；类型列只有 `stdio` / `http`；抽屉类型下拉的选项文字是 `stdio`、`http`。
9. 任一表格的“操作”列头 `text-align` 是 `center`。
10. 技能工坊（`/workshop` 打开一个已有技能）：工作台宽 340px（与对话页相同）、标签栏只显示 5 个且每个标签单行、标签栏没有横向溢出，点右键 3 次后看到“版本”，右键禁用；左键出现；每个窗口下最后一个标签与右键的间距 ≤ 30px。对话页工作台宽 340px、没有左右键。

截图对照原型 v80 的六个子代理状态与“设置 · 子代理”。

**运行时生效的验收**：Go 测试 Step 3.7 已经覆盖。另外用 run-forebrain 技能的 driver 真机跑一次：
- 在 driver 的隔离 home 的 forebrain.yaml 里给 main 加一条改写 explore 提示词的覆盖，并加 `agents.defaults.enable_subagent: true`；
- `FAKE_DUMP_DIR=$TMPDIR/fb-079 .claude/skills/run-forebrain/driver.sh start tool '{"name":"subagent_run","arguments":{"title":"probe","task":"say hi","subagent_type":"explore"}}'`；
- 提交一条消息后，在 dump 出来的请求里找到子代理那次请求，断言 system 消息以改写内容开头。

driver 的配置文件路径与写法见技能说明；做不到时报告原因，**不要**跳过 Go 测试。

**Verify**：
- `FOREBRAIN_E2E_SPEC=e2e/subagent-config.spec.ts scripts/acceptance/web_e2e.sh` 末行 `web e2e: PASS`；
- `FOREBRAIN_E2E_SPEC=e2e/mcp-pages.spec.ts scripts/acceptance/web_e2e.sh` 末行 `web e2e: PASS`；
- 全套 `scripts/acceptance/web_e2e.sh` 末行 `web e2e: PASS`。

## Test plan

- Go：
  - Step 1 的配置与校验用例，以及名单一致性；
  - Step 2 的 6 个目录用例；
  - Step 3 的 9 个运行时用例（开关、枚举、两种提示词覆盖、三档工具、模型与思考强度、优先级、删后续跑）；
  - Step 4 的接口用例，以及 `BuiltinSubagentToolsTier`。
- 前端：`api.test.ts`、`SubagentsView.test.ts`、`SubagentConfigDrawer.test.ts`、`SettingsSubagentsTab.test.ts`、MCP 相关测试。
- e2e：`subagent-config.spec.ts`（新）、`mcp-pages.spec.ts`（改），然后全套。
- 真机：driver dump 请求核对改写的提示词。

## Done criteria

- [ ] Drift check 全部有输出（记在报告里）
- [ ] Go：上面 6 个包 `ok`；`go vet` 与 `gofmt -l pkg cmd` 无输出
- [ ] `grep -n "resolve:    agent.ResolvePublicSubtype," pkg/run/subagent.go` 无输出
- [ ] `grep -n "agent.SystemPromptForSubtype(subtype)" pkg/run/typedsubagent_llm.go` 只剩 `prompt` 为 nil 时的回退那一处
- [ ] 前端 `pnpm test` 全绿、`vue-tsc` exit 0
- [ ] `grep -rn "<th class=\"text-right\">{{ t('[a-zA-Z.]*colActions') }}</th>" frontend/src | wc -l` 为 `0`
- [ ] 两个 e2e 单文件与全套都 `web e2e: PASS`；真机 dump 核对通过（或写明做不到的原因）
- [ ] 截图与原型 v83 的子代理六个状态、设置 · 子代理、两个工作台（宽度与标签栏）一致（报告里列差异，没有写“无”）
- [ ] `git status --short | diff $TMPDIR/079-before.txt -` 里新出现或变化的路径都在 Scope 内

## STOP conditions

- Drift check 任一条没有输出（078 未执行，或代码已变）。
- `pkg/config` 与 `pkg/agent` 之间出现 import 环，且 Step 1.5 的“副本 + 一致性测试”方案也行不通。
- 模型覆盖加思考强度后，计划审阅（`TestPlanReviewSpawnEventNamesTheChosenModel` 等）的现有测试失败，而且原因不是测试本身在断言旧行为。
- 自定义 `read_only` 子代理在 `GuardTool` 之外，还有其它路径能写文件（例如 shell 的 sandbox 档位按子代理类型选择，而自定义类型落到了可写档）。报告具体路径，不要自行放宽或收紧 sandbox。
- 路由器不允许 `/agents/defaults/subagents` 与 `/agents/:agentId/cancel` 并存，且换成 `/subagent-defaults` 也不行。
- 要改 `useChatStream.ts`、078 的抽屉组件或 Scope 外的文件才能通过测试。

## Maintenance notes

- **子代理类型现在按主代理解析**：模型能派发什么、用什么提示词、能用哪些工具、跑在哪个模型上，都以 `SubagentCatalogFor(cfg, agentName)` 为准。新增内置子代理时：
  - 改 `pkg/agent` 的 `builtins` 与 `PublicSubagentNames` / `SystemSubagentNames`；
  - 同步 `pkg/config` 的副本名单；
  - 前端补 `subagents.desc.<name>`。
- **提示词覆盖是整段替换**：改写会替换内置原文的全部内容（包括 Scope discipline 段）；运行时追加的“文件访问范围”仍由引擎追加。以后内置原文更新时，已改写的主代理不会自动拿到新内容，这是有意的；“恢复默认”可以重新跟随。
- **模型优先级**：派发时指定 > 主代理的子代理配置 > 全局 `agents.definitions.<类型>.llm_providers` > 对话模型。旧的全局配置是兼容层，页面上标“来自全局配置”，以后若要删它，先迁移。
- **开关联动在服务端**（单行接口里实现），前端只负责重新加载；不要在前端再算一遍。
- 子代理配置写在 forebrain.yaml，配置写入后要 rebind 运行实例，否则“新会话生效”不成立。
