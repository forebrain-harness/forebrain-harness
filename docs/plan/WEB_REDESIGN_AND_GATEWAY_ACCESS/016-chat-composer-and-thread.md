# 计划 016：对话页——顶栏里的对话按钮、输入框（＋菜单 / 模式 / 审批 / 上下文圆环 / 模型 / 推理强度）、消息流（思考、Worked 行、MCP 提示、另一端运行中）、去掉输入框上方的诊断面板

> **执行者须知**：逐步执行，每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"中的任何情况，立即停止并报告。
> 完成后更新本目录 README 中本计划的状态行。**不要提交代码。**
> **1:1 还原设计稿** `app-preview.html`（第 18 版）。"本轮新增"里依次点：
> - "思考耗时 · 首 token"
> - "另一端运行中"
> - "目录已不存在"
> - "MCP 启动失败"
> - "加号菜单"
> - "Agent / 计划"
> - "推理强度"
>
> 对应原型函数：
> - `topbar()`：对话页分支；
> - `composerHtml()`、`plusPop()`、`modeChip()`、`effortChip()`、`ring()`、`presets` 菜单；
> - `threadHtml()`：`.think`、`.worked`、`.tool`、`.err-block`、`mcpNotice()`。
>
> **漂移检查（先运行）**：
> - 计划 015 已完成（`grep -rn "ForebrainModal" frontend/src/components/common` 有结果）；
> - `grep -n "data-testid=\"workbench-toggle\"" frontend/src/views/ChatView.vue` 有 1 行（按钮还在对话页自己的标题行里）；
> - `grep -n "data-field=\"mcp-failures\"" frontend/src/views/ChatView.vue` 有 1 行。
>
> 不符就停下核对。

## 状态

- **优先级**：P1
- **工作量**：L
- **风险**：MED（对话页是最常用的页面）
- **依赖**：015。`plans/015`（首 token）、`plans/016`（思考耗时）、`plans/021`（另一端运行中）提供数据；没做的部分按"数据缺失时不显示"处理，并在报告里列出
- **类别**：设计还原
- **基线**：提交 `005c1a8` + 主树未提交改动，2026-10-11

## 为什么要做

`plans/README.md` 的偏差清单：
1. 心跳、工作台按钮应在顶栏、语言按钮左侧，实现却在对话页单独一行；
2. 输入框上方的"会话上下文、待办与运行诊断"面板，设计稿没有，移进工作台（017）；
3. MCP 启动失败作为红色告警出现在历史记录最下方，而 MCP 只在新开对话时启动一次。设计稿改为对话开头的一条提示，原始错误收进"查看详情"，工作台概览同步状态；
4. 消息窗口要实时流式显示用户消息、思考、所有工具调用卡片，与 TUI 一致。

owner 2026-10-11 还要求：
- "+" 菜单参考 ZCode；
- 推理强度选择器；
- composer 里单独的 Agent / 计划模式切换，加号菜单里不放"计划模式"；
- 删掉"14 次 · read_file…"这类统计（它在诊断面板里，随面板一起移走）；
- 文案简洁。

## Owner 裁决（2026-10-12，评审方就 STOP 条件 3 提问后拍板）

**审批进消息流。** 删掉输入框上方的 `PendingActionsPanel`，把它的全部能力搬进消息流：

- 工具审批（批准一次 / 拒绝 + 反馈 / 权限记忆建议 `pending-decision-*`）；
- 代理提问（`pending-question`，单选 / 多选 / 自填）；
- exit-plan 审批（`exit-plan-approval`，批准 / 清空上下文后批准 / 继续计划 / 请求评审 / 停止评审 / 选评审模型 `plan-review-model`）；
- 加载失败与重试。

理由：消息流里已经有一张**只读**的 `ApprovalCard.vue`，TUI 的审批卡本来就在消息流里（owner 要求"与终端一致"），工作台是侧栏、收起时看不到审批。

**必须保住的既有行为与 testid**：`frontend/e2e/exit-plan-approval.spec.ts` 在对话页驱动 `[data-testid="exit-plan-approval"]` 等选择器，**这些 testid 与行为一个都不能丢**，改完该 spec 必须全绿（不得改测试来迁就）。`PendingActionsPanel.vue` 本身删除。

**与 017 的分工**：`session-workspace` 那个 `<details>`（会话上下文、待办、上下文调试、计划 Markdown、工具审计）以及运行状态行由本计划移出对话页，内容归工作台"概览""上下文"（web 017）。本计划只保证它们不再出现在输入框上方，**不实现工作台的内部**。

## 现状（已核实；行号以 2026-10-12 的工作区为准，计划撰写时的行号已漂移）

- **对话页标题行**：`frontend/src/views/ChatView.vue:7-45` 一带，依次是：
  - `AgentViewTabs`；
  - 项目标签 `chat-project-tag`（`:21-26`）；
  - `SessionHeartbeat`（`:32`）；
  - 工作台按钮（`:33-44`，带运行中代理数）。
- **输入框上方**（`:477-513`）：
  - MCP 启动中的一行（`mcpStartupLabel`，`:477`）；
  - MCP 失败告警（`:486`，`data-field="mcp-failures"`）；
  - 运行状态行（`runtimeStatusLabel`，`:496`）；
  - `PendingActionsPanel`（`:507`）。**它只装审批与提问**（工具审批、权限记忆建议、代理提问、exit-plan 审批）；计划撰写时说的"含待办、上下文用量"是错的，那些在下面那个 `<details>` 里。
- **消息列里的诊断块**（`:329` 起，`data-testid="session-workspace"` 的 `<details>`）：会话上下文摘要、待办列表、`ContextDebugPanel`（`:359`）、计划 Markdown、工具审计。"14 次 · read_file…"这类统计在这里。内容归工作台（017），本计划负责让它不再出现在对话页。
- **输入框底栏**（`:549-593`）：
  - 左：`ApprovalPresetPicker`（`:551`）、模式菜单按钮（`modeMenuOpen`）；
  - 右：`PromptInputActionMenu`（`:585-590`，只有"选择文件"）、`PromptInputSubmit`（`:591`）。
  - 没有上下文圆环、模型、推理强度。
- **会话的模型与推理强度**：存储有 `fb_session_model_state(provider, model, effort)`（`pkg/state/session_store.go:2398-2440`、`:2465`、`:2518`）。gateway 没有读写接口；TUI 的 `/model` 用 `SelectModel`（`pkg/tui/chat_slash.go:651` 一带）。
- **Worked 行**：`RunWorkedLine`（`ChatView.vue:276`），数据来自 `workedLineOf`。思考块的渲染在 `:186-200`。

## 命令

| 用途 | 命令 | 预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/state/... -count=1` | `ok` |
| 前端 | `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 无输出 |
| 构建 | `make ui` | 成功 |
| 验收 | `scripts/acceptance/web_e2e.sh` | 全部 PASS |

## 范围

**改**：
- `ChatView.vue`；
- 顶栏组件（015 之后的位置）；
- `ForebrainPromptTextarea.vue` 的外围；
- 新建 `components/chat/{ComposerPlusMenu.vue,ModeChip.vue,EffortChip.vue,ModelChip.vue,ContextRing.vue,McpNotice.vue}`；
- `components/chat/ApprovalCard.vue`（只读状态行 → 可交互审批卡，承接 `PendingActionsPanel` 的全部能力）；
- `ApprovalPresetPicker.vue` 的外观；
- gateway 新增会话模型接口；
- locale；
- 测试；
- `pkg/gateway/dist`。

**不改**：
- 工作台内容（017）；
- 新对话页与项目选择（`plans/029`）；
- 回到这里（`plans/033`）；
- `/` 与 `@` 选择器的触发规则（`plans/034`）。

## 步骤

### 步骤 1：对话按钮进顶栏

1. 对话页自己的标题行删掉，内容移进全局顶栏（015 的顶栏），照原型 `topbar()` 的对话页分支：
   - 左边：会话标题；
     - 项目标签：黑底，`folder-git-2` 图标，"项目 · 名称"，点击进项目空间；
     - 模型名，次要色；
   - 右边依次：心跳（`SessionHeartbeat`，新对话未命名时不显示）、工作台按钮（带运行中代理数）、新建主代理（015）、语言。
2. `AgentViewTabs`（主视图与 subagent 视图切换）的位置：原型没有画 subagent 视图。保持它在消息区上方，紧贴顶栏下沿，不改样式，并在报告里注明。

### 步骤 2：去掉输入框上方的面板；审批进消息流（owner 2026-10-12 裁决）

1. **审批与提问进消息流**：`ApprovalCard.vue` 从只读状态行改为可交互卡片，把 `PendingActionsPanel` 的全部能力搬进来：
   - 工具审批：批准一次 / 拒绝 + 可选反馈；权限记忆建议（`pending-decision-*`）照旧；
   - 代理提问：单选 / 多选 / 自填（`pending-question`）；
   - exit-plan 审批（`exit-plan-approval`）：计划文本、批准 / 清空上下文后批准 / 继续计划 / 请求评审（`plan-review-model`）/ 停止评审；
   - 加载失败与重试。

   **testid 与行为一个都不能丢**（`frontend/e2e/exit-plan-approval.spec.ts` 在对话页驱动它们，该 spec 不得改动）；数据源与提交动作沿用面板现有的组合式与接口，只换容器。审批在消息流里的位置：锚在它对应的工具步骤/回合之后，与 TUI 的审批卡同义。`PendingActionsPanel` 从 `ChatView.vue` 移除；**组件文件保留**——`WorkshopView.vue` 仍在用，web 020 把工坊改成会话超集时再删。
2. **诊断块出对话页**：消息列里 `session-workspace` 那个 `<details>`（会话上下文摘要、待办、`ContextDebugPanel`、计划 Markdown、工具审计）整块删除。内容归工作台"概览""上下文"（web 017，本计划不实现其内部）。`ContextDebugPanel.vue` 若再无使用方则一并删除。
3. **MCP 启动中**那一行保留，但移到消息流顶部，与 TUI 同义。
4. **MCP 启动失败**：在**消息流开头**显示一条 `McpNotice`，照 `mcpNotice()`：
   - 插头图标；
   - 文字"MCP 服务 **名字** 启动超时（30 秒），本对话不能用它的工具。"；
   - 下一行"修好后新开对话即可。"和"查看详情"，点开显示原始错误，再点收起。
   - 只在会话开始时出现一次，数据来自 MCP 启动结果；不随后续轮次重复。多个服务失败时合成一条，逐个列名字。
5. 运行状态（进行中的计划进度）不再单列一行：Worked 行里已有 `☑ 3/5`，原型如此。

### 步骤 3：输入框底栏

照原型 `composerHtml()` 的 `.bar`：

左侧依次：

1. **"＋" 菜单 `ComposerPlusMenu`**（`plusPop()`）：
   - 附件；
   - 目标：插入 `/goal `；
   - 分隔线；
   - "技能"小标题与本会话可用的技能列表：选中插入 `/技能名 `。
   - 原有的 `PromptInputActionMenu` 移除。
2. **模式 `ModeChip`**（`modeChip()`）：
   - "Agent" / "计划" 两项；
   - 运行中禁用；
   - 切换调用今天 `selectMode` 的逻辑；
   - 原模式菜单删除。
3. **审批 `ApprovalPresetPicker`**：外观改成原型的 chip（`shield` 图标，"审批：默认"，下拉为三档预设，标题"本会话的审批模式（不影响其他会话）"），逻辑不变。

右侧依次：

4. **上下文圆环 `ContextRing`**（`ring()`）：
   - 18px 圆环，数据来自 `contextUsedPercent`；
   - ≥80% 变警告色并显示百分比；
   - 点击打开工作台"上下文"标签；
   - 新对话不显示。
5. **模型 `ModelChip`**：显示当前会话模型名，点开选择本主代理可用的模型。写入新接口（步骤 4）。
6. **推理强度 `EffortChip`**（`effortChip()`）：
   - 档位：默认、低、中、高、最高。可选档位随模型变化，取模型目录里该模型支持的档位，不支持的不显示；
   - 菜单标题"推理强度（本会话）"，底部"可选档位随模型变化"。
7. **发送**。

"计划模式"不在"＋"菜单里（owner 2026-10-11）。

### 步骤 4：会话模型与推理强度的接口

1. `GET /api/chat/sessions/:id/model` → `{provider, model, effort, options: [{provider, model, efforts: [...]}]}`：
   - 可选项来自本主代理的模型服务；
   - 档位来自 TUI `SelectModel` 用的同一份目录，用 `grep -rn "Efforts\|EffortLevels\|ReasoningEffort" pkg/llm pkg/turn | head` 找。
2. `PUT /api/chat/sessions/:id/model {provider, model, effort}` → 写 `fb_session_model_state`，复用 `session_store.go` 现有写函数（`:2430` 一带）。不在可选项里 → 400。
3. 运行中可以改，下一轮生效：与 TUI 的 `/model` 一致，核对 TUI 的行为后照做。

### 步骤 5：消息流的几处

1. **思考块**：`plans/016` 做完后，主视图显示"思考了 N 秒"，与 subagent 视图共用 `thinkingBlockLabel`。本计划只保证样式照原型 `.think`：小号次要色标签加正文，左侧无边框。
2. **Worked 行**：`plans/015` 做完后显示"已工作 1m 12s · 首 token 1.2s · ☑ 3/5 · 09:41"，照原型 `.worked`，两侧细线、等宽字。没有首 token 数据时省略那一段。
3. **另一端运行中**：`plans/021` 返回的拒绝原因显示为 `.err-block`，文字"这个对话正在另一端运行（终端或网页），等它结束后再发送。"，下一行"消息已放回输入框。"，同时把消息放回输入框。
4. **目录已不存在**：`plans/027` 的 `session_directory_missing` 显示为 `.err-block`，文字"目录已不存在：<路径>，无法继续。"，下一行"目录恢复后可继续。"，输入框锁定（原型 `.composer.locked`）。
5. **流式一致性核对**（偏差 4）：
   - 用 driver 在 TUI 跑一轮，包含思考、两个工具调用、一个 subagent，web 同时打开该会话；
   - 对照用户消息、思考、每张工具卡是否都实时出现、顺序一致。本计划只要求列出差异，修复另立。

### 步骤 6：测试

- 单测：
  - 顶栏对话按钮的位置与显隐（新对话无心跳）；
  - `ComposerPlusMenu` 插入 `/goal ` 与 `/技能名 `；
  - `ModeChip` 运行中禁用；
  - `ContextRing` 80% 阈值；
  - `EffortChip` 只显示模型支持的档位；
  - `McpNotice` 只出现一次且"查看详情"可展开。
- gateway：会话模型读写与非法值 400。
- e2e：
  - 切换推理强度后刷新仍保持；
  - MCP 失败（配置一个必然失败的 stdio 服务）时提示出现在消息流开头。

## 测试计划

见各步。

## 完成标准

- [ ] 命令表全部通过
- [ ] 与原型 7 个跳转状态的并排截图写进报告
- [ ] 步骤 5 第 5 点的差异清单写进报告
- [ ] `grep -n "data-field=\"mcp-failures\"\|PendingActionsPanel" frontend/src/views/ChatView.vue` 无输出
- [ ] `frontend/e2e/exit-plan-approval.spec.ts` **未改动**且全绿（审批进消息流后行为与 testid 无损）
- [ ] `git status --short` 中本计划新增的改动只涉及范围列出的文件

## STOP 条件

1. 015 未完成。
2. 模型目录里查不到"每个模型支持哪些推理强度档位"，需要新建数据源。（评审方 2026-10-12 已核实不成立：`turn.ModelRecord.ReasoningEfforts` 存在且 `GET /api/models` 已返回它。）
3. 删除 `PendingActionsPanel` 会丢掉一项能力——**已由 owner 2026-10-12 裁决化解**（审批进消息流，见"Owner 裁决"与步骤 2）。若实现中发现审批/提问之外还有只有面板携带、消息流与工作台都没有的能力，仍停下报告。

## 维护说明

- 输入框底栏的顺序与原型一致；以后新增按钮先改原型。
- MCP 提示只在会话开始时出现：MCP 只在开对话时启动一次，不要做成每轮提醒。
