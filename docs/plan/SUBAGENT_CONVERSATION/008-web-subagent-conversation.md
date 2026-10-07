# 计划 008：网页——与 TUI 对齐的 subagent 对话

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SUBAGENT_CONVERSATION/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"，尤其是第 8 条（文案）、第 10 条（不碰 `pkg/gateway/dist`）、第 11 条（真机）。
>
> **前置检查**：README 里计划 003、005、006、007 必须都是 `DONE`，否则 STOP。TUI（计划 007）定下的交互就是本计划的规格。
>
> **漂移检查**：
> `git diff --stat bda9505 -- pkg/gateway/ frontend/src/ frontend/e2e/ scripts/acceptance/`
> 基线 `bda9505` 对应干净树；diff 列出文件即有新改动（前置计划会改其中一部分），按函数名和组件名核对"现状"。

## 状态

- **优先级**：P1（owner 第 1–4 点的网页部分；网页必须与 TUI 一一对应）
- **工作量**：L
- **风险**：MED
- **依赖**：003、005、006、007
- **类别**：direction（与 TUI 对齐）
- **基线**：提交 `bda9505`，2026-10-04

## 为什么要做

网页的硬性要求是"与 TUI 完全对齐：TUI 上能看到、能做的，网页上都要能看到、能做"（项目长期规则）。计划 007 让 TUI 的 subagent 视图成了一个可以对话的会话；网页的 subagent 视图（`SubagentConversation.vue`，从 `AgentViewTabs` 或卡片进入）今天只能看，页面底部唯一的 composer 永远发给主 agent，Esc 只会返回主对话。本计划让网页的 subagent 视图具备与 TUI 相同的语义：发消息给它、它独享的排队与召回、D1 的 Esc、D4 的斜杠命令、`/compact`、它自己的上下文预算。

所有语义都在引擎里（计划 003、005、006）；gateway 只做传输，网页只做呈现。

## 现状（2026-10-04 工作区的事实；前置计划会改其中一部分）

- `frontend/src/views/ChatView.vue`：`activeAgentView`（`''` 为主对话）、`openAgentView(agentId)`、`handleAgentViewKeydown`（Esc 且在 subagent 视图 → `openAgentView('')`，约 `:932-937`）、`handleSubmit`（约 `:1360`，上传附件后调 `useChatStream` 的 `send`；运行中按 `nextSubmissionDisposition` 决定 steer 或排队）。
- `frontend/src/components/chat/SubagentConversation.vue`：只渲染 `record.blocks`（prompt、thinking、tool、plan、approval、compaction、goal…），头部有返回按钮、标题、状态、`record.inputTokens/outputTokens`。
- `frontend/src/components/chat/AgentViewTabs.vue`、`SubagentCard.vue`（计划 010 已改标题）。
- `frontend/src/composables/useChatStream.ts`：`send`（约 `:2757`）、`queueActiveRunInput`（约 `:2140`）、事件处理（`queued_input_released` 约 `:2565`、`token_budget_updated` 约 `:1137`、`pending_input_updated`）。
- `frontend/src/lib/api.ts`：`runInput(runId, …)`（`/runs/:id/input`）、`/runs/:id/queued-input`、`subagentCancel`、`sessionSubagentHistory`（GET `/chat/sessions/:id/subagent-history`）。
- `pkg/gateway/api_extra.go`：路由表（`chatSessions` 组、`runs` 组、`/subagents/:id/cancel`、`/slash/commands`、`chatSessions.Post("/:id/compact", s.handleSessionCompact)`）；`handleSessionCompact`（`:1558`）直接 `run.CompactionService(...).ManualCompactSession`。
- `pkg/gateway/run_control.go`：`continueAfterUsageLimit` 是 gateway 已有的"分离运行"（没有任何 websocket 拥有它，事件经事件总线到达所有打开的页面）的构造方式——它是 subagent 用户执行的框架范本。`pkg/gateway/server.go:1748` 一带是 WS 回合给工具步骤装钩子的写法：带 `HookAgentIDFromContext` 的步骤直接 `tool.RunEventFromStep` 后 `s.RunEvents().Publish`。
- 网页真机：`scripts/acceptance/web_e2e.sh`（构建二进制和前端到临时目录，`forebrain gateway start`，Playwright 驱动真实 Chrome，用例在 `frontend/e2e/*.spec.ts`），假模型 `scripts/acceptance/fake_provider.py`（以 `reply` 模式启动）。

## 设计

### 1. gateway：subagent 对话的 HTTP 入口

在 `pkg/gateway/api_extra.go` 的 `chatSessions` 组下加（每个处理函数都先 `s.sessionOwned(ctx, sid)`，不属于本主 agent 返回 404；`agentKey` 不属于这个对话时引擎返回 `run.ErrSubagentNotFound`，映射为 404）：

| 路由 | 处理 | 引擎调用 |
| --- | --- | --- |
| `POST /:id/subagents/:agent/input` body `{message, attachments, mention_images, mode: "steer"\|"follow_up"}` | 发消息 | `run.SendToSubagent`，返回 `{delivery, preview}` |
| `POST /:id/subagents/:agent/queued-input` body `{action: "edit_last"}` | 召回 | `run.RecallSubagentInput`，返回被召回的消息（文字、附件 id、@ 图片）和预览 |
| `POST /:id/subagents/:agent/interrupt-send` | Esc：中断并发送 | `run.InterruptSubagentToSend`，返回 `{interrupted}` |
| `POST /:id/subagents/:agent/withdraw` | Esc：撤回 | `run.WithdrawSubagentInput`，返回被撤回的全部消息 |
| `POST /:id/subagents/:agent/compact` | `/compact` | `run.SubagentCompactTarget` + `CompactionService(...).ManualCompactSession`；响应体与 `handleSessionCompact` 相同（把那段组装响应体的代码提成一个函数，两处共用）；运行中返回 409 和稳定代码 `subagent_running` |
| `GET /:id/subagents/:agent/context` | `/context` | `run.SubagentContextGauge` + `turn.ContextReport`，与主对话的 `/:id/context` 返回同样的结构 |
| `GET /:id/subagents/:agent/budget` | 打开视图时取预算 | `run.SubagentContextBudget` |

- 消息里的技能命令：`message` 以 `/` 开头且命中技能时，用 WS 路径今天展开技能命令的同一个共享函数展开（执行者找到 WS `send` 里处理技能斜杠命令的调用，复用它，不另写），把技能名和路径放进 `run.Input.SkillName/SkillPath`（计划 007 加的字段）。命中 ③ 类内置命令时返回 400 和稳定代码 `subagent_view_command`；命中 `compact`/`context` 时返回 400 和代码 `use_dedicated_endpoint`（网页本来就不该这样发）。
- gateway 的 `run.SubagentSurface`：
  - `Frame`：与 `continueAfterUsageLimit` 构造分离运行时相同的审批与工具步骤框架（步骤一律以 canonical 事件发布到会话的事件总线，因为 subagent 的步骤都带 `HookAgentID`）。把这段框架提成一个函数，`continueAfterUsageLimit` 和 subagent 共用。
  - `OnBoundary`：在对话的事件总线上发布 `queued_input_released`，载荷 `{agent_id, next, inputs}`（计划 004、005 加的字段）。
- 斜杠菜单：`GET /slash/commands` 接受 `view=subagent`，传 `DiscoveryOptions{SubagentView: true}`（计划 007 加的过滤），网页在 subagent 视图里用它。

### 2. 网页：subagent 视图有自己的 composer

- `ChatView.vue`：底部 composer 的提交按 `activeAgentView` 路由：`''` 走今天的 `send`，否则走新的 `sendToSubagent(agentId, …)`（放在 `useChatStream.ts`，调用第 1 条的 `input` 接口；附件先按今天的方式上传，再把 id 交给接口）。
- composer 草稿按视图保存与恢复（文字、已上传附件、@ 图片——遵守"退回 composer 必须完整"的规则）：在 `openAgentView` 里切换，复用 `frontend/src/lib/composerSubmission.ts` 的 `ComposerSubmission`。
- 队列预览：主对话今天显示 `serverPendingInput`；subagent 视图显示按 `agent_id` 存的那份（`pending_input_updated` 带 `agent_id` 时存到对应视图）。召回快捷键在 subagent 视图里调 `queued-input` 的 `edit_last`。
- `subagent_input_delivered`：在该 subagent 的 `record.blocks` 里加一条用户消息块（新 block 种类 `user`，用用户头像，区别于派发 prompt 的 `prompt` 块）；`subagent_spawned` 的 `origin === 'user'` 时同样渲染成 `user` 块。两者都会被持久化，刷新页面后由同一投影函数重建（遵守"live 与 replay 同一投影"的既有约定）。
- `queued_input_released` 带 `agent_id`：`next` 非空 → 合并后 `sendToSubagent`；`inputs` 非空 → 放回该视图的 composer 草稿（该视图未打开就存起来）。
- Esc（`handleAgentViewKeydown`，subagent 视图）：先 `withdraw`，有返回就把消息放回 composer；否则 `interrupt-send`，`interrupted` 为 true 就结束；否则 `openAgentView('')`。浮层打开时 Esc 先关浮层（今天的行为不变）。
- 斜杠：subagent 视图里斜杠菜单用 `view=subagent` 的列表；`/compact` 调 `compact` 接口，结果由带 `agent_id` 的压缩事件画进该视图（409 时显示一句 `chat.subagentCompactRunning`）；`/context` 调 `context` 接口，用主对话 `/context` 相同的组件呈现；③ 类和 `!shell` 不发请求，直接显示一句 `chat.subagentViewCommand`。
- 上下文预算：subagent 视图里，原本显示主对话预算的位置显示该 subagent 的（打开视图时 `GET budget`，之后由带 `agent_id` 的 `token_budget_updated` 更新），格式与主对话相同。

### 3. 文案（`frontend/src/locales/index.ts`，中英文同时加，一句话）

- `chat.subagentComposerPlaceholder`：中 `给这个 subagent 发消息…`，英 `Message this subagent…`
- `chat.subagentViewCommand`：中 `请先按 Esc 回到主对话再执行这个命令。`，英 `Run this from the main view (press Esc to return).`
- `chat.subagentCompactRunning`：中 `等这个 subagent 完成后再压缩它。`，英 `Wait for this subagent to finish before compacting it.`
- 稳定代码 `subagent_running`、`subagent_view_command` 由网页翻译成上面两句，Go 不拼面向网页的句子。

## 缓存影响

无新的请求变化；请求内容由计划 002、005 决定。

## 范围

**只改这些文件：** `pkg/gateway/api_extra.go`、`pkg/gateway/run_control.go`、`pkg/gateway/server.go`（如需提取框架函数）及其测试；`frontend/src/views/ChatView.vue`、`frontend/src/components/chat/SubagentConversation.vue`、`frontend/src/composables/useChatStream.ts`、`frontend/src/lib/api.ts`、`frontend/src/lib/composerSubmission.ts`（如需）、`frontend/src/locales/index.ts` 及它们的测试；`frontend/e2e/subagent-conversation.spec.ts`（新建）；`scripts/acceptance/fake_provider.py`、`scripts/acceptance/web_e2e.sh`。

**不要动：** 主对话的任何可观察行为；`pkg/gateway/dist`；TUI。

## 步骤

### 第 1 步：gateway 接口

按"设计"第 1 条。`pkg/gateway` 加测试：每个接口一条正常路径、一条"别的对话的 subagent → 404"、一条"别的主 agent 的会话 → 404"；`compact` 运行中 → 409 和 `subagent_running`；`input` 发 ③ 类命令 → 400 和 `subagent_view_command`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` → `ok`。

### 第 2 步：网页逻辑

按"设计"第 2、3 条。`useChatStream.test.ts` 加：发给 subagent 的消息不进入主对话的发送路径；`subagent_input_delivered` 和 `origin=user` 的 spawned 渲染成 `user` 块且刷新后重建一致；plan-reviewer 的 subagent 视图与其它 subagent 视图用同一个组件、同一套头部和预算显示（喂一个 `agent_type: 'plan-reviewer'` 的 spawned 和带它 `agent_id` 的 `token_budget_updated`，断言视图显示 `N%/窗口`，窗口来自事件，不来自主对话；owner 2026-10-05 的要求见 README）；`queued_input_released(agent_id)` 的 `next`/`inputs` 分别被发送/退回到该视图；预算按 `agent_id` 分开。`ChatView` 的组件测试覆盖 Esc 的三种情况和草稿按视图保存。

**验证**：`cd frontend && corepack pnpm test` → 全部通过；`cd frontend && corepack pnpm exec vue-tsc --noEmit -p tsconfig.json` → 退出码 0。

### 第 3 步：网页真机（Playwright）

- `scripts/acceptance/fake_provider.py`：在 `reply` 模式里加一条按内容分辨的分支——主 agent 的最后一条用户消息含标记 `[[e2e:subagent-net]]` 时，按计划 007 第 8 条 `subagent-net` 的规则回答（subagent 的请求按 system 提示识别，未收到 `continue` 前断开连接）。这样一个假模型进程能同时服务所有用例。
- 新建 `frontend/e2e/subagent-conversation.spec.ts`：发送 `delegate the probe [[e2e:subagent-net]]` → 等 subagent 卡片 → 打开它的视图 → 断言预算显示为 `N%/窗口` 格式 → 在视图里发 `continue` → 断言回答出现在 subagent 视图、不出现在主对话 → 在视图里输入 `/new` 只显示一句提示 → `/compact` 出现压缩卡片 → 按 Esc 回到主对话；每一步截图。再加一段：用计划 016 的网页审批卡片请另一个模型评审计划，打开时间线里的 plan-reviewer 卡片进入它的视图，断言预算显示为 `N%/窗口` 格式、头部有评审模型。
- `scripts/acceptance/web_e2e.sh` 把这个用例纳入（若它按目录自动收集 `frontend/e2e/*.spec.ts`，就不用改）。

**验证**：`scripts/acceptance/web_e2e.sh` → 最后一行 `web e2e: PASS`；截图路径写进报告。

## 完成标准

- [ ] Go、前端单测、类型检查全部通过
- [ ] `scripts/acceptance/web_e2e.sh` 通过，新用例的截图已附
- [ ] 主对话的已有前端测试断言未改
- [ ] `pkg/gateway/dist` 没有任何改动（`git status pkg/gateway/dist` 无输出）
- [ ] 文案中英文齐全，Go 没有拼面向网页的句子
- [ ] README 状态行已更新

## STOP 条件

- 计划 007 尚未定下某个交互（例如召回键在 subagent 视图里的行为），网页无从对齐。
- 需要改主对话的发送路径才能让 subagent 的发送工作。
- 找不到 WS 路径里展开技能命令的共享函数（意味着技能展开在两处各有一份，需要先回来问怎么处理）。

## 维护说明

- 网页 subagent 视图里的每一个交互都应能在 TUI 的 subagent 视图里找到对应项；新增一边时同时加另一边。
- gateway 的新接口都只是引擎函数的转调；如果哪个处理函数里开始出现"判断该不该发、发给谁"的逻辑，那是语义回流到了传输层，评审时退回。
