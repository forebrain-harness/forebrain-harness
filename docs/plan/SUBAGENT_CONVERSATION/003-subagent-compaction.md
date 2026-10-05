# 计划 003：subagent 的压缩——按它自己的模型算阈值，支持回合前自动压缩和 /compact

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SUBAGENT_CONVERSATION/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"。
>
> **前置检查**：README 里计划 002 的状态必须是 `DONE`，否则 STOP。
>
> **漂移检查**：
> `git diff --stat bda9505 -- pkg/run/runner.go pkg/run/config.go pkg/run/compaction.go pkg/run/llm_middleware.go pkg/run/subagent.go pkg/process/worker_cli.go pkg/assembly/service.go pkg/turn/compaction.go`
> 计划 002 会改其中几个文件，这是预期的；按函数名和注释原文核对下面的摘录。

## 状态

- **优先级**：P1（owner 第 3 点，外加一个顺带发现的既有缺陷）
- **工作量**：M
- **风险**：MED（压缩请求必须继续命中 subagent 自己的缓存前缀）
- **依赖**：002
- **类别**：bug
- **基线**：提交 `bda9505`，2026-10-04

## 为什么要做

owner 的第 3 点：subagent 必须支持自动压缩和 `/compact` 手动压缩。

今天的情况：

- **运行中途的自动压缩已经对 subagent 生效**，因为 typed 和 fork subagent 的 LLM 链都经过 `recoverableLLM`（`pkg/run/llm_middleware.go:220`），而它们的上下文里带着 `AgentSessionID = workerSessionID`。压缩的生命周期事件也已经带上 subagent 的 roster key（`pkg/assembly/compact_lifecycle.go:59` `agentID: tool.HookAgentIDFromContext(ctx)`），卡片会画进 subagent 的视图。
- **但阈值算错了模型（顺带发现的既有缺陷）。** 压缩用的"这个请求跑在哪个模型上"来自 `r.effectiveModelFor(ctx)`（`pkg/run/config.go:1676`），它只看会话的模型选择和主 agent 的模型。typed subagent 的请求却被 `typedSubagentProviderLLM`（`pkg/run/typedsubagent_llm.go`）路由到 `agents.definitions[<type>].llm_providers`，plan reviewer 被 `subagentModelOverrideLLM` 路由到审批时选的模型。窗口比主模型小的 subagent，会在阈值触发之前就被服务商以"上下文超长"拒绝；窗口更大的，又会过早压缩。
- **没有回合前的自动压缩。** 主会话在写入用户消息之前先检查要不要压缩（`pkg/tui/chat_turn.go:1149` `maybeAutoCompactBeforeAppend` → `assembly.RunPreflight` → `svc.AutoCompactSession`；gateway 同样，`pkg/gateway/run_control.go:527`）。subagent 的继续（模型的 `subagent_continue`，以及计划 005 之后用户在视图里发的消息）没有这一步。
- **没有手动压缩。** `/compact` 只能压缩主会话（`pkg/tui/chat_slash.go:37` `HandleCompactSlash` → `turn.ExecuteCompact(ctx, sessionID, run.CompactionService(...))`）。计划 002 之后 worker 会话里有了历史，才有东西可压。

## 现状（2026-10-04 工作区的事实）

- `pkg/run/runner.go` `Load` 里（约 `:999-1063`）：

  ```go
  	compactSvc := assembly.Service{
  		Sessions: r.SessionStore,
  		PrimaryModel: func(ctx context.Context) (string, string) {
  			return r.effectiveModelFor(ctx)
  		},
  		CompactLLM: r.sessionClientFor,
  		...
  	}
  	compactDeps := &CompactChainDeps{
  		...
  		ActiveModel: func(ctx context.Context) (string, string) {
  			return r.effectiveModelFor(ctx)
  		},
  		TryCompact: func(ctx context.Context, msgs []llm.Message, tools []*llm.Tool, reactive bool) ([]llm.Message, bool, error) {
  			// Compaction owns model history, so it remains scoped to the worker
  			// session. Only permission decisions use ConversationSessionID.
  			sessionID := llm.AgentSessionIDFromContext(ctx)
  			...
  			conversation := func(ctx context.Context, instruction llm.Message) (*llm.Result, error) {
  				return summaryChain.Execute(ctx, append(append([]llm.Message(nil), msgs...), instruction), tools)
  			}
  			return compactSvc.TryCompactOnMessages(ctx, msgs, sessionID, reactive, conversation)
  		},
  	}
  ```

  同一个 `Load` 里，`typedSubagentProviderLLM` 用到的按类型的 LLM 映射是局部变量 `typeLLMs`（约 `:934-955`）。`Runner` 结构体现在有 25 个字段，正好是 `pkg/architecture` 的上限（`runnerFields = 25`）。

- `pkg/run/compaction.go` `CompactionService(r, sessions)`（显式 `/compact` 用）：`out.PrimaryModel = func(ctx) { return r.effectiveModelFor(ctx) }`，`out.CompactLLM = r.ContextCompactLLM`（即 `sessionClientFor`），`out.ConversationSummary = r.conversationSummarizer`。
- `(*Runner).conversationSummarizer(sessionID)`：用 `transcript.head(ctx)`（主 agent 的 system + 项目说明）+ `transcript.history(ctx, sessionID)` + `main.Tools()`，经 `summaryChain` 发出。typed subagent 的请求里，system 会被 `wrapTypedSubagentPromptLLM` 按上下文里的 subagent 类型替换，工具会被 `wrapTypedSubagentToolFilterLLM` 过滤——这两个包装都在 `summaryChain` 里，所以只要上下文里带着和原执行相同的 subagent 类型、定义来源、项目根、工作区根，摘要请求的前缀就与 subagent 自己的请求相同。fork subagent 的 system 是冻结在 `fb_session_prompt_state` 里的那一份（计划 002 的 `forkSystemPromptKey`），`transcript.head` 给不出它。
- `pkg/assembly/service.go`：`ManualCompactSession` / `AutoCompactSession` → `autoCompactDecision` 用 `s.PrimaryModel(ctx)` 算预算，`compactSession` 用 `s.ConversationSummary(sid)` 发摘要请求，`compactMessages` 在没有 `conversation` 时用 `s.CompactLLM(ctx)`。
- `pkg/turn/compaction.go:31` `ExecuteCompact(ctx, sessionID, svc Compactor) CompactResult`：TUI 和 gateway 的手动压缩回复都由它产出（gateway 的 HTTP 接口 `handleSessionCompact` 直接调 `ManualCompactSession`，见 `pkg/gateway/api_extra.go:1558`）。
- `pkg/turn/slash.go` `runDisallowedCommands` 里有 `"compact": true`：主运行进行中时拒绝 `/compact`。
- 计划 002 之后：`run.AgentModel(r, conversationSessionID, *agent.HistoryEntry)` 按"覆盖 → 类型自有模型 → 对话模型"解析；`continueSubagentExecution` 里构造了 subagent 的宿主上下文（fork-child 标记、subagent 类型、定义来源、roster key、模型覆盖）。

## 设计

### 1. 一个"这次调用的 agent 跑在哪个模型上"的上下文解析（修既有缺陷）

在 `pkg/run/config.go`，紧挨 `effectiveModelFor` 加私有方法：

```go
// agentModelFor is the model the call in ctx is routed to: the dispatch-time
// override of the subagent making it, else that subagent type's own chain,
// else the conversation's model. It reads exactly what the routing wrappers
// read (subagentModelOverrideLLM, typedSubagentProviderLLM), so a compaction
// sizes itself by the window of the model that will actually receive the
// request.
func (r *Runner) agentModelFor(ctx context.Context) (provider, model string)
```

规则必须与路由包装一致：主线程查询来源（`explicitMainThreadQuerySource(QuerySourceFromContext(ctx))`）时直接 `effectiveModelFor(ctx)`；否则先看 `subagentModelOverrideFromContext(ctx)`，再看 `SubagentOwnModel(r, tool.SubagentTypeFromContext(ctx))`，最后 `effectiveModelFor(ctx)`。

替换：
- `Load` 里 `compactSvc.PrimaryModel` 和 `compactDeps.ActiveModel` 改为 `r.agentModelFor(ctx)`。
- 压缩用的客户端（`compactSvc.CompactLLM`，只在没有"按对话原样发送"的摘要函数时使用：被服务商以超长拒绝后的反应式压缩、远端压缩）改为 `Runner` 的私有方法 `agentCompactClientFor(ctx) llm.LLM`：主线程或没有 subagent 路由时 `r.sessionClientFor(ctx)`；否则用 `agentModelFor(ctx)` 得到的 provider/model 调 `ConfiguredModelClient(r.AppCfg, tool.SubagentTypeFromContext(ctx), provider, model)`（`pkg/run/subagent.go` 约 `:2010`，plan reviewer 的覆盖客户端就是这样构造的）。**不要**复用 `Load` 里的局部变量 `typeLLMs`（`pkg/run/runner.go` 约 `:934`）：`Runner` 现在正好 25 个字段，到了上限，不能为了存它加字段。
- `Load` 里的 `compactSvc` 和 `CompactionService(r, sessions)` 的 `PrimaryModel`、`CompactLLM` 都改用 `agentModelFor` / `agentCompactClientFor`，两处不再各写一份。`CompactLLMForModel`（`compaction.go:271` 设的 `r.ContextCompactLLMForModel(model)`）**不在其列**：它按显式传入的 model（会话换模型前的上一个模型，"旧模型总结自己的历史"用，`assembly/service.go:158-159`）建客户端，不是"当前 agent 跑在哪个模型"的解析，保持不动。
- 一致性测试：对计划 002 `TestAgentModelResolvesEachKindOfAgent` 的每一种 agent，按它真实执行时的方式构造上下文，断言 `agentModelFor(ctx) == AgentModel(r, sid, entry)`。

### 2. 摘要请求认得 fork 的冻结 system

`conversationSummarizer(sessionID)` 里：若 `SessionPromptState(ctx, sessionID, forkSystemPromptKey)` 有值，head 用 `[]llm.Message{llm.SystemMessage(frozen)}`，history 用 worker 会话的模型上下文（与计划 002 `continueForkSubagent` 读的是同一个方法），工具用 fork 运行时用的那一套（与 `runForkSubagent` 的 `RegisterTools` 同源：`own.LoadedTools()`）；否则沿用今天的 `transcript.head(ctx)`。这样主会话、typed subagent、fork subagent 的手动/回合前压缩都只经过这一个摘要函数。

### 3. subagent 的宿主上下文只构造一次

把计划 002 之后 `continueSubagentExecution` 里构造宿主上下文的那段提成私有函数：

```go
// subagentRecordContext rebuilds the context a subagent's own calls run
// under from its record: its kind, type, definition source, roster key and
// model override, its worker session, and the conversation it belongs to.
// Everything that shapes the prefix of the subagent's requests is here, so a
// compaction or a continuation built from it reuses that prefix.
func subagentRecordContext(ctx context.Context, fac Factory, record agent.HistoryEntry) context.Context
```

它还要设：`tool.WithConversationSessionID(ctx, record.SessionID)`、`llm.WithAgentSessionID(ctx, record.WorkerSessionID)`、项目根和工作区根（与 `prepareSubagentExecutionResolved` 相同的来源）、`llm.WithPromptCacheKey`（与首次派发时相同的取值规则：派发上下文里已有的 key，否则对话 id）。`continueSubagentExecution`、`continueForkSubagent` 和下面的压缩入口都用它。

### 4. 回合前自动压缩

subagent 的每次**继续**在写入新的用户消息之前，先对 worker 会话跑一次 `run.CompactionService(r, store).AutoCompactSession(ctx, workerSessionID, "")`，`ctx` 是第 3 条构造的上下文：

- typed：在 `process.RunSubagentSupervised` 里、计划 002 加的 `PersistUserTurn` 之前（同样只在"不是审批续跑"时）。
- fork：在 `continueForkSubagent` 写入新用户消息之前。
- 首次派发时 worker 会话为空，`autoCompactDecision` 自然返回不压缩，不需要特判。
- 自动压缩失败只记日志，不阻止这次继续（与主会话 `maybeAutoCompactBeforeAppend` 的做法一致：TUI 里 `chat_session.go:1636` 失败时只写日志）。执行者先读那一处确认；不一致就 STOP。

### 5. 手动压缩的引擎入口

在 `pkg/run/subagent.go` 加包级函数：

```go
// ErrSubagentRunning refuses an operation that rewrites a subagent's history
// while one of its executions is still appending to it.
var ErrSubagentRunning = errors.New("subagent is running")

// SubagentCompactTarget names what /compact compacts when it is run from a
// subagent's view: that subagent's worker session, under the context its own
// requests are made in. It refuses while the subagent is running, the way
// /compact is refused while the conversation's own run is.
func SubagentCompactTarget(ctx context.Context, r *Runner, conversationSessionID, agentKey string) (context.Context, string, error)
```

- 记录：`agent.GetMerged(r.subagentFactory().subagentScopeRoot(), agent.Query{SessionID: conversationSessionID, TaskID: agentKey})`（roster key 就是 task id，见 `agent.RosterKey`）。找不到返回一个 `fmt.Errorf("subagent %s not found", agentKey)`。
- 运行中：`agent.RegistryFor(...).Get(agent.Query{TaskID: agentKey})` 有句柄且 `!IsDone()` → `ErrSubagentRunning`。
- 返回 `subagentRecordContext(ctx, fac, record)` 和 `record.WorkerSessionID`。

两个 surface 都这样用：`subCtx, worker, err := run.SubagentCompactTarget(...)`；`err == nil` 时 `turn.ExecuteCompact(subCtx, worker, run.CompactionService(r, store))`。界面接线在计划 007（TUI）和 008（网页），本计划只交引擎和测试。

## 缓存影响（必须留下证据）

- **手动 / 回合前压缩的摘要请求必须命中 subagent 自己的前缀。** 第 5 步的两条手动压缩测试用捕获请求的测试 LLM 断言：typed subagent 的摘要请求 = 它最后一次请求的 system、工具、消息 + 摘要指令；fork subagent 的摘要请求以它冻结的 system 开头，消息与它最后一次请求相同。
- **阈值改按真实模型算**：对"有自有模型"的 typed subagent，压缩触发点变了（这是修正，不是回退）；对其它 agent（主 agent、fork、无自有模型的 typed），`agentModelFor` 与 `effectiveModelFor` 结果相同，行为不变——第 2 步的一致性测试覆盖。
- 压缩本身会开启新的缓存代（与主会话相同的既有语义），不在本计划改变。

## 范围

**只改这些文件：** `pkg/run/runner.go`、`pkg/run/config.go`、`pkg/run/compaction.go`、`pkg/run/subagent.go`、`pkg/process/worker_cli.go` 及它们的测试。

**不要动：** `pkg/assembly/*`（服务本身不变，只是喂给它的模型解析和摘要函数变了）；`pkg/run/llm_middleware.go` 的压缩流程；`pkg/tui`、`pkg/gateway`、`frontend`（接线在 007、008）。

## 步骤

### 第 1 步：先写失败的测试

`pkg/run` 加 `TestCompactionSizesATypedSubagentByItsOwnModel`：配置主模型窗口 200k、`agents.definitions.explore.llm_providers` 指向一个窗口 32k 的模型；构造 explore subagent 的执行上下文，断言 `r.agentModelFor(ctx)` 返回 32k 的那个模型（同包测试直接调私有方法；第 1 步它尚不存在，测试以编译失败的形式失败，同样算失败）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run TestCompactionSizesATypedSubagentByItsOwnModel -count=1` → 失败（此刻是编译失败：`agentModelFor` 还不存在；第 2 步加上方法后若返回的是主模型，同样算失败）。不失败就 STOP。

### 第 2 步：agent 感知的模型与压缩客户端

按"设计"第 1 条。

**验证**：第 1 步转绿；`TestAgentModelAndRoutingAgree`（"设计"第 1 条末尾的一致性测试）通过；`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → `ok`；架构测试 `ok`（`Runner` 字段和导出方法数不变）。

### 第 3 步：摘要认得 fork；宿主上下文提成一个函数

按"设计"第 2、3 条。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'Continuation|ForkSubagent|Summar' -count=1` → `ok`（计划 002 的继续测试仍然通过，证明提取没有改变继续的前缀）。

### 第 4 步：回合前自动压缩

按"设计"第 4 条。

**验证**：
- `pkg/process` 加 `TestTypedSubagentContinuationCompactsFirstWhenItNoLongerFits`：把 `compact.model_auto_compact_token_limit` 设得很小，让 worker 会话超过阈值，再继续；断言写入新用户消息之前 worker 会话出现了压缩边界，新用户消息在边界之后，压缩事件的 `AgentID` 是 roster key。
- `pkg/run` 加同样的 fork 版 `TestForkSubagentContinuationCompactsFirstWhenItNoLongerFits`。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/run -count=1` → `ok`。

### 第 5 步：手动压缩入口

按"设计"第 5 条。

**验证**：`pkg/run` 加：
- `TestSubagentCompactTargetRefusesARunningSubagent`：注册表里有未完成的句柄 → `errors.Is(err, run.ErrSubagentRunning)`。
- `TestSubagentManualCompactionReusesTheSubagentsPrefix`（typed）和 `TestForkSubagentManualCompactionReusesItsFrozenPrefix`（fork）：先跑一次 subagent，记下它最后一次请求；再 `turn.ExecuteCompact(subCtx, worker, CompactionService(r, store))`；捕获摘要请求，断言它 = 最后一次请求的 system + 工具 + 消息 + 摘要指令（逐字节比较 JSON）。注意：这条测试在 `pkg/run` 里不能 import `pkg/turn`，改为直接调 `CompactionService(r, store).ManualCompactSession(subCtx, worker, "manual")`。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → `ok`。

### 第 6 步：全量

**验证**：`gofmt -l pkg cmd` 无输出；`go vet -tags fts5 ./...` 退出码 0；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全部 `ok`；架构测试、包依赖图、死代码检查通过。

## 完成标准

- [ ] 第 1–5 步列出的测试全部通过，全量测试通过
- [ ] `grep -n "effectiveModelFor(ctx)" pkg/run/runner.go pkg/run/compaction.go` 不再出现在压缩相关的闭包里
- [ ] `Runner` 字段数、导出方法数不变（架构测试通过即可证明）
- [ ] README 状态行已更新

## STOP 条件

- 第 1 步测试没有失败。
- 摘要请求与 subagent 最后一次请求的前缀不能逐字节相同，且原因不是测试写错。
- 需要给 `Runner` 加字段或导出方法。
- `maybeAutoCompactBeforeAppend` 在失败时的行为与"只记日志、不阻止回合"不同。

## 维护说明

- 以后新增任何"按 agent 路由到别的模型"的包装（例如新的覆盖方式），必须同时更新 `agentModelFor` 和 `AgentModel`，一致性测试会抓住两者分叉。
- 计划 006 的 footer 百分比用同一个模型解析，所以"footer 显示 10% 剩余"和"即将自动压缩"永远说的是同一件事。
