# 计划 006：上下文预算下沉到引擎，按 agent 计算并推送

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SUBAGENT_CONVERSATION/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"。
>
> **前置检查**：README 里计划 002、003 必须都是 `DONE`，否则 STOP。
>
> **漂移检查**：
> `git diff --stat 1a6d708 -- pkg/run/config.go pkg/run/subagent.go pkg/event/compact_events.go pkg/tui/chat_turn.go pkg/tui/chat_slash.go pkg/tui/chat_surface.go pkg/tui/run.go pkg/gateway/wsevents.go pkg/gateway/api_extra.go`
> 前置计划会改其中几个文件；按函数名核对"现状"。

## 状态

- **优先级**：P1（owner 第 4 点的引擎部分，外加一处顺带发现的重复实现）
- **工作量**：M
- **风险**：LOW-MED（主视图 footer 的数字必须一字不变）
- **依赖**：002、003
- **类别**：bug / tech-debt
- **基线**：提交 `1a6d708`，2026-10-04

## 为什么要做

owner 的第 4 点：subagent 视图 footer 的右侧必须像主视图一样显示"75%/1M"——**这个 subagent 自己的**上下文窗口剩余比例和窗口大小。

今天没有任何地方能算出这个数：

- 主视图的数来自 `ChatSession.contextOccupancy(sessionID)`（读会话最后一次 API 响应的用量，`state.TokenCountFromLastAPIResponse`）和 `tokenBudgetMessageFromUsage(usage)`（按 `run.PrimaryModel` 的窗口和 `compact.model_auto_compact_token_limit` 算预算）。subagent 的 worker 会话在计划 002 之前是空的；它的模型也不一定是主模型。
- 运行中的实时更新来自主回合流式输出的 `OnUsageSnapshot`；`subagentRunContext`（`pkg/run/subagent.go:529`）给子运行换了一个新的流式输出，**刻意丢弃**了 `OnUsageSnapshot`（注释："the composer footer tracks the parent session's context occupancy, not the child's"）。

另外，"占用"和"预算"这两段逻辑在 TUI 和 gateway 各写了一遍（顺带发现）：

- `pkg/tui/chat_turn.go:1199` `contextOccupancy` 与 `pkg/gateway/wsevents.go` `contextOccupancy`：同一段代码。
- `pkg/tui/chat_turn.go:1214` `tokenBudgetMessageFromUsage`、`:1242` `tokenBudgetResetMessage` 与 `pkg/gateway/wsevents.go:392` `tokenBudgetPayloadFromSession`：同一个计算。

按"共享语义下沉到引擎"的规矩，本计划把它们收成引擎里的一份，并让它按 agent 计算；subagent 的实时用量以带 `agent_id` 的事件推送。两个界面的接线在 007、008。

## 现状（2026-10-04 工作区的事实）

- `pkg/tui/chat_turn.go`：

  ```go
  func (s *ChatSession) contextOccupancy(ctx context.Context, sessionID string) (int, bool) {
  	...
  	turns, err := s.sessStore().ListRecentMessages(ctx, sessionID, 400)
  	...
  	return state.TokenCountFromLastAPIResponse(turns), true
  }

  func (s *ChatSession) tokenBudgetMessageFromUsage(usage int) (TokenBudgetUpdatedMsg, bool) {
  	if s == nil || usage <= 0 { return TokenBudgetUpdatedMsg{}, false }
  	provider, model := "", ""
  	if s.runner() != nil { provider, model = run.PrimaryModel(s.runner()) }
  	limits, _ := llm.Lookup(provider, model)
  	budget := state.CalculateTokenBudgetWithOptions(usage, model, limits, state.TokenBudgetOptions{ExplicitLimit: s.compactExplicitLimit()})
  	if budget.PercentLeft <= 0 && budget.TokenUsage <= 0 { return TokenBudgetUpdatedMsg{}, false }
  	return TokenBudgetUpdatedMsg{Model: budget.Model, TokenUsage: budget.TokenUsage, PercentLeft: budget.PercentLeft,
  		ContextWindow: budget.ContextWindow, EffectiveWindow: budget.EffectiveContextWindow, AutoCompactThreshold: budget.AutoCompactThreshold}, true
  }
  ```

  `tokenBudgetResetMessage()` 是 usage 为 0 时的版本（窗口未知时返回 false）；`SurfaceComposerTokenStats(usage)` 组合两者。
- `pkg/gateway/wsevents.go`：`contextOccupancy` 同上；`tokenBudgetPayloadFromSession(ctx, sid)` 用 `run.PrimaryModel(s.Runner)` 做同样的计算，返回 `event.TokenBudgetUpdatedPayload`。
- `pkg/event/compact_events.go`：`TokenBudgetUpdatedPayload{Model, TokenUsage, PercentLeft, ContextWindow, EffectiveWindow, AutoCompactThreshold}`；事件类型 `CompactEventBudgetUpdated = "token_budget_updated"`。
- `/context` 与 `/status` 的占用也读 `contextOccupancy`（`pkg/tui/chat_slash.go` `HandleContextSlash`、`statusSource` 的 `ContextUsage`），模型取 `run.PrimaryModel`，阈值来自 `s.compactExplicitLimit()`。
- 计划 002：`run.AgentModel(r, conversationSessionID, *agent.HistoryEntry)`。计划 003：压缩阈值按 `agentModelFor(ctx)` 算，并有一致性测试保证它与 `AgentModel` 相同。

## 设计

### 1. 引擎里的一份

在 `pkg/run/config.go` 加包级函数：

```go
// ContextOccupancy is how much of the context window a session fills right
// now: the whole prompt of its last API response. Every gauge reads it — the
// footer of either surface, /status, /context — so they never disagree.
func ContextOccupancy(ctx context.Context, store *state.SessionStore, sessionID string) (int, bool)

// ContextBudget is the context gauge of one agent of a conversation holding
// usage tokens: the window of the model that agent runs on, and how much of
// it is left before auto-compaction. subagent is nil for the primary agent.
// Usage 0 is a fresh context, which shows the whole window.
func ContextBudget(r *Runner, conversationSessionID string, subagent *agent.HistoryEntry, usage int) (event.TokenBudgetUpdatedPayload, bool)
```

- `ContextOccupancy`：原样搬 `contextOccupancy` 的实现（`ListRecentMessages(…, 400)` + `TokenCountFromLastAPIResponse`）。
- `ContextBudget`：模型用 `AgentModel(r, conversationSessionID, subagent)`；`ExplicitLimit` 用 `r.AppCfg.Compact.ModelAutoCompactTokenLimit`；`usage > 0` 时的返回条件与今天 `tokenBudgetMessageFromUsage` 相同，`usage == 0` 时与 `tokenBudgetResetMessage` 相同（窗口未知返回 false）。返回 `event.TokenBudgetUpdatedPayload`，`AgentID` 填 subagent 的 roster key（主 agent 为空）。
- `TokenBudgetUpdatedPayload` 加 `AgentID string \`json:"agent_id,omitempty"\``。

### 2. 主视图改用它（行为不变）

- TUI：`contextOccupancy` 删除，调用点改 `run.ContextOccupancy(ctx, s.sessStore(), sid)`；`tokenBudgetMessageFromUsage(usage)`、`tokenBudgetResetMessage()` 改为带会话 id 的 `tokenBudgetMessageFromUsage(sessionID, usage)`、`tokenBudgetResetMessage(sessionID)`，内部调用 `run.ContextBudget(s.runner(), sessionID, nil, usage)` 并把载荷转成 `TokenBudgetUpdatedMsg`。今天的调用点都拿得到会话 id（`prepareTUIAgentBase` 的 `sessionID` 参数、`notifyTokenBudget(ctx, sessionID)`、`ClearSurfaceSession` 的 `sid`）；`Session.SurfaceComposerTokenStats(usage)` 改为 `SurfaceComposerTokenStats(sessionID string, usage int)`，调用方（`pkg/tui/run.go` 的 `initialComposerTokenStats`、`pkg/tui/commands.go:128`）传 `state.sessionID`。`ContextBudget` 对主 agent 用的是 `PrimaryModelForSession`，与今天的 `run.PrimaryModel(s.runner())` 在"会话模型已激活到 runner 上"时相同——第 1 步的测试钉住这一点；若不同，STOP 报告，不要悄悄换口径。
- gateway：`contextOccupancy`、`tokenBudgetPayloadFromSession` 改为调用同两个函数。
- `/context`、`/status` 的占用读 `run.ContextOccupancy`。

### 3. subagent 的实时用量

- `subagentRunContext` 的流式输出加回 `OnUsageSnapshot`，但它说的是**子运行自己**的占用：`func(in, out int)` → `ContextBudget(own, conversationSessionID, &record, in+out)` → 发布 `token_budget_updated` 事件（`AgentID` = roster key，run id 是子运行的）。为此 `subagentRunContext` 需要拿到记录；执行者把它的参数从 `(ctx, sink, runID, sessionID, rosterKey)` 扩成带上记录（或带上一个已经绑定好记录的回调），所有调用点同步。把注释改成："the child's own usage snapshot is published tagged with its roster key, so the gauge in its view shows its context — the parent's footer never sees it"。
- subagent 一次执行结束、或它被压缩之后：发布一次按 worker 会话占用重算的预算（`ContextOccupancy(worker)` → `ContextBudget`）。主会话在回合结束时调 `notifyTokenBudget`（`pkg/tui/chat_turn.go:504`），压缩后的刷新时机执行者先在主会话路径里找到（例如压缩完成事件之后是否刷新），subagent 照同样的时机做；在报告里写明主会话是在哪里刷新的。
- 给界面"打开某个 subagent 视图时取它当前的预算"一个入口：

  ```go
  // SubagentContextBudget is the gauge a subagent's view opens with.
  func SubagentContextBudget(ctx context.Context, r *Runner, conversationSessionID, agentKey string) (event.TokenBudgetUpdatedPayload, bool)
  ```

  找记录（同计划 005 的方式）→ `ContextOccupancy(worker)` → `ContextBudget`。

### 4. `/context` 的 subagent 版本（供 007/008 接线）

```go
// SubagentContextGauge is what /context reports about a subagent's context
// when it is run from that subagent's view.
func SubagentContextGauge(ctx context.Context, r *Runner, conversationSessionID, agentKey string) (workerSessionID, provider, model string, used, explicitLimit int, err error)
```

界面拿它调用现有的 `turn.ContextGaugeOf(provider, model, used, explicitLimit)` 和 `turn.ContextReport(ctx, src, workerSessionID)`——和主会话的 `HandleContextSlash` 用同样的报告生成，不另写。

## 缓存影响

无。只读用量、算比例、发事件；不改任何请求。`OnUsageSnapshot` 是流式输出的回调，不影响请求内容。

## 范围

**只改这些文件：** `pkg/run/config.go`、`pkg/run/subagent.go`、`pkg/event/compact_events.go`、`pkg/tui/chat_turn.go`、`pkg/tui/chat_slash.go`、`pkg/tui/chat_surface.go`（`tokenBudgetResetMessage` 的调用点）、`pkg/tui/notify.go`（`Session.SurfaceComposerTokenStats` 的签名）、`pkg/tui/run.go`、`pkg/tui/commands.go`（调用点）、`pkg/gateway/wsevents.go`、`pkg/gateway/api_extra.go`（若有调用点），以及它们的测试；Go/TS 共享事件 fixture。

**不要动：** footer 的绘制（007）；网页的显示（008）；`state.CalculateTokenBudgetWithOptions` 的算法。

## 步骤

### 第 1 步：钉住主视图今天的数字

`pkg/tui` 加 `TestPrimaryFooterBudgetIsUnchangedByTheEngineMove`：给定一个会话、最后一条助手消息的 `usage_json`、主模型和 `ExplicitLimit`，记录今天 `SurfaceComposerTokenStats(usage)` 和 `tokenBudgetMessage(ctx, sid)` 的结果（写成期望值常量）。`pkg/gateway` 同样钉住 `tokenBudgetPayloadFromSession`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/gateway -run 'BudgetIsUnchanged' -count=1` → `ok`。

### 第 2 步：引擎函数

按"设计"第 1 条。`pkg/run` 加表驱动测试 `TestContextBudgetUsesEachAgentsOwnModel`：主 agent、fork、无自有模型的 typed、有自有模型的 typed（窗口不同）、带覆盖的 plan reviewer，分别得到各自模型的窗口；`usage == 0` 返回整窗口；窗口未知返回 false。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run ContextBudget -count=1` → `ok`。

### 第 3 步：主视图改用引擎

按"设计"第 2 条。

**验证**：第 1 步两条测试仍通过（期望值不改）；`grep -n "func (s \*ChatSession) contextOccupancy\|func (s \*Server) contextOccupancy\|func (s \*Server) tokenBudgetPayloadFromSession" pkg/tui/*.go pkg/gateway/*.go` → 无输出（或只剩一行转调，按 `deadcode` 结果删干净）；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/gateway -count=1` → `ok`。

### 第 4 步：subagent 的实时用量与入口

按"设计"第 3、4 条。`pkg/run` 加：
- `TestSubagentUsageSnapshotIsPublishedForItsOwnView`：子运行的测试 LLM 报一次用量 → 发布一条 `token_budget_updated`，`agent_id` 是 roster key，数字按该 subagent 的模型算；主会话没有收到 `OnUsageSnapshot`。
- `TestSubagentContextBudgetReadsItsWorkerSession`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → `ok`。

### 第 5 步：全量

**验证**：`gofmt -l pkg cmd` 无输出；`go vet -tags fts5 ./...` 退出码 0；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全部 `ok`；架构测试、包依赖图、死代码检查通过（`SubagentContextBudget`、`SubagentContextGauge` 在 007 接上之前会被 `deadcode` 报出，在报告里列出，007 完成后必须清零）。

## 完成标准

- [ ] 第 1 步的期望值在全程未被修改且通过
- [ ] 占用与预算在 TUI、gateway 里不再各有一份实现
- [ ] subagent 的用量快照以带 `agent_id` 的事件发布，主会话的 footer 不受影响
- [ ] 全量测试、架构测试通过
- [ ] README 状态行已更新

## STOP 条件

- 主 agent 用 `PrimaryModelForSession` 与今天的 `run.PrimaryModel(runner)` 在某个已有测试场景下得出不同的数字。
- 需要改 `state.CalculateTokenBudgetWithOptions`。
- 需要给 `Runner` 加字段或导出方法。

## 维护说明

- footer 的百分比和自动压缩的阈值现在用同一个模型解析（`AgentModel` / `agentModelFor`，一致性测试见计划 003）。改其中一个而不改另一个，"还剩 10%"和"马上要压缩了"就会说两件不同的事。
