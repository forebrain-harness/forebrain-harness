# 计划 002：每个 subagent 的模型上下文落到它自己的 worker 会话里

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SUBAGENT_CONVERSATION/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"。
>
> **前置检查（先运行）**：`grep -n "| 002 \|| 003 \|| 005 " docs/plan/SCHEDULED_RUNS_TRANSCRIPT/README.md`。SRT-002 和 SRT-003 必须是 `DONE`，否则 STOP（本计划要用它们引入的 `SessionBirth`/`EnsureAt` 和"列表在 SQL 里按用途过滤"）。SRT-005 是否 DONE 决定第 1 步的做法。
>
> **漂移检查**：
> `git diff --stat 1a6d708 -- pkg/run/subagent.go pkg/run/run.go pkg/run/fork.go pkg/run/controller.go pkg/run/config.go pkg/process/worker_cli.go pkg/process/one_shot.go pkg/state/session_store.go pkg/state/message_sync.go pkg/memory/jobs.go pkg/turn/session.go pkg/tui/chat_slash.go`
> 这些文件有些本来就有不属于本计划的改动，SRT-002/003/005 也会改 `pkg/state/session_store.go`、`pkg/memory/jobs.go`。按函数名和注释原文核对"现状"摘录；对不上就 STOP。

## 状态

- **优先级**：P1（owner 第 1、3、4 点的共同根因）
- **工作量**：L
- **风险**：MED-HIGH（改变 subagent 的持久化和 `subagent_continue` 的上下文；必须证明首次请求逐字节不变）
- **依赖**：SRT-002、SRT-003（见前置检查）
- **类别**：bug
- **基线**：提交 `1a6d708`，2026-10-04

## 为什么要做

owner 的第 1 点：subagent 因为网络原因失败后，用户必须能在它的视图里发一句"continue"让它接着做。今天做不到，根本原因不在界面上：**subagent 的模型上下文从来没有落库**。

- typed subagent 通过 `process.RunSubagentSupervised` 用 `workerSessionID` 跑 `run.Run`，`Runner` 的会话构建器从会话存储里按这个 id 读历史。但没有任何代码往这个会话写消息。写计划时在本机状态库核实过：`SELECT count(*) FROM fb_messages WHERE session_id LIKE 'main:%'` 为 0，`fb_sessions` 里也没有这类会话，而 `<workspace>/state/subagent-history.jsonl` 里有大量 `worker_session_id` 形如 `main:<会话id>:<agentID>:<uuid>` 的记录。
- fork subagent 的上下文只在内存里，外加 `RunFork` 写的一个旁路 JSONL（供 `SubagentStop` 钩子读取）。

后果是：subagent 一结束（无论成功还是失败），它读过的文件、跑过的命令、拿到的结果全都丢了。`subagent_continue` 只能拼一段 "Continue the existing worker task… Original task… Previous result…"（`buildSubagentContinuePrompt`）从零开始；失败的 subagent 连"上次结果"都没有。第 3 点的 `/compact` 没有可压缩的历史；第 4 点的 footer 读不到这个 subagent 的上下文占用。

修复：每个 subagent 都把它的模型上下文写进**自己的 worker 会话**，写法和主会话一字不差（复用 `turn.PersistUserTurn` / `PersistAssistantTurn` / `PersistCancelledTurn`）；继续时从 worker 会话原样重放。worker 会话在出生时就标明"这是一个 subagent"（`source='subagent'`、父会话为所属对话），因此不出现在任何对话列表里，也不参与记忆抽取。

## 现状（2026-10-04 工作区的事实）

### typed subagent 的执行（`pkg/process/worker_cli.go:16-75`）

```go
func RunSubagentSupervised(ctx context.Context, env *Environment, task string, childRunID string, parentRunID string, sessionID string, workerSessionID string, subagentType string) (*agent.Result, hook.PreHookResult, error) {
	...
	sid := strings.TrimSpace(workerSessionID)
	if sid == "" {
		sid = strings.TrimSpace(sessionID)
	}
	...
	agBase := AgentContext(ctx, config.ActiveStateRoot(env.Root, env.Deps.AppCfg), sid)
	...
	hc := hook.HookContext{SessionID: sid, Channel: channel, Trigger: "subagent_run"}
	opts := run.Options{
		RunRT:        env.Deps.RunRT,
		Runner:       env.Runner,
		Hooks:        env.Hooks,
		AgBase:       agBase,
		HC:           hc,
		Input:        strings.TrimSpace(task),
		PreviewMax:   4096,
		CreateRunCtx: ctx,
	}
	if ch := strings.TrimSpace(childRunID); ch != "" {
		opts.SuperviseExistingRunID = ch
	} ...
	return run.Run(opts)
}
```

它被 `(*Environment).RunSubagentExec`（`pkg/process/one_shot.go:30`）调用，后者实现 `run.SubagentExecutor` 接口（`pkg/run/subagent.go:2055-2057`）：

```go
type SubagentExecutor interface {
	RunSubagentExec(ctx context.Context, task string, superviseExistingRunID string, parentRunID string, sessionID string, workerSessionID string, subagentType string) (string, error)
}
```

`run.executeSubagent`（`pkg/run/subagent.go:478`）是所有走执行器的入口（typed 首次运行、`subagent_continue`、plan reviewer、goal check、fork 的回退路径）。

### 主会话是怎么落库的（要照抄的写法）

- 回合开始前写用户消息：`pkg/tui/chat_session.go:1682` `turn.PersistUserTurn(durable, s.sessStore(), turn.UserTurn{...})`。会话构建器在历史末尾已经是一条用户消息时，会用加工后的版本替换它（`pkg/run/transcript.go` `build`，注释 "The pre-pipeline write in dispatchUserTurnContent already stored a plain user message. Replace it with the pipeline-enriched version"），所以"先写用户消息再跑"不会让请求里出现两条用户消息。
- 成功后写助手输出：`pkg/tui/chat_turn.go:492` `turn.PersistAssistantTurn(context.Background(), s.sessStore(), turn.AssistantTurn{...})`，内部对 `Result.Session` 调 `AppendMessageSequenceForRun`。
- 失败或取消时写已完成的部分：`pkg/tui/chat_session.go:1530` `turn.PersistCancelledTurn(..., turn.CancelledTurn{Captured: capture.Snapshot(), ...})`，捕获来自 `run.NewPartialSessionCapture()` 装进上下文（`run.WithPartialSessionCapture`）。
- 三个函数都在 `pkg/turn/session.go`（`PersistUserTurn`、`PersistAssistantTurn`、`PersistCancelledTurn`）。`pkg/run` 不能 import `pkg/turn`（`TestLayer3PackagesDoNotImportEachOther`），`pkg/process` 可以。
- `pkg/state/message_sync.go` `AppendMessageSequenceForRun`：从尾部找"存储里最后一条、本次列表仍然带着的行"，只追加它之后的消息；`system` 行和 `Ephemeral` 消息不入库；压缩摘要不作为独立行入库。这正是让"已经写过的前缀不会重复写"的机制。

### fork subagent（`pkg/run/subagent.go:614` `runForkSubagent`，`pkg/run/run.go:41` `RunFork`）

- system：`cacheSafe.RenderedSystemPrompt`（父请求的 system，来自 `forkCaptureLLM` 捕获）加上 `"\n\n" + agent.FileAccessScopeSystemPrompt(projectRoot, wsRoot)`。
- 初始消息：`CreateSubagentContext`（`pkg/run/controller.go:495`）把父请求去掉 system 后的消息（`ParentMessages`）加上 `BuildForkedMessages(prep.task, last)` 产生的指令消息，再 `stripOrphanedToolCalls`。
- `RunFork` 把 `subctx.InitialMessages` 和之后的新消息追加到旁路 JSONL（`SidechainFilePath(WorkspaceRoot, SessionID, "subagent", agentID)`），会话构建器每次都从内存里的 `subctx.InitialMessages` 开始。
- 上下文里设了 `llm.WithAgentSessionID(agentCtx, prep.workerSessionID)`，所以运行中途的自动压缩会把检查点写进 worker 会话（`pkg/run/runner.go:1035-1047` 的 `TryCompact` 用 `AgentSessionIDFromContext`），但 worker 会话里没有任何其它行。

### 继续（`pkg/run/subagent.go:1200` `continueSubagentExecution`）

```go
	if record.OneShot && !record.Continuable {
		return agent.HistoryEntry{}, fmt.Errorf("subagent_type %q is one-shot and cannot continue", ...)
	}
	prompt := buildSubagentContinuePrompt(record, msg)
	hostCtx := WithoutTurnInputRuntime(ctx)
	if strings.TrimSpace(record.AgentKind) == "fork" {
		hostCtx = tool.WithForkChild(hostCtx, true)
	} else if ... "typed" {
		hostCtx = tool.WithSubagentType(...)
		...
	}
	...
	outText, runErr := runAcrossApprovals(hostCtx, approve, record.RunID, "subagent continuation",
		func(attemptCtx context.Context, superviseRunID string) (string, error) {
			return executeSubagent(attemptCtx, fac, prompt, superviseRunID, record.ParentRunID, record.SessionID, record.WorkerSessionID, subagentType)
		})
```

fork subagent 的继续也走 `executeSubagent`（typed 路径、只加了 fork-child 标记），它的前缀是主 agent 的 system 加空历史，和它原来的请求毫无关系。

`buildSubagentContinuePrompt`（`:1283`）：

```go
	b.WriteString("Continue the existing worker task with the follow-up instructions below.")
	if task := strings.TrimSpace(record.Task); task != "" {
		b.WriteString("\n\nOriginal task:\n")
		...
	if out := strings.TrimSpace(record.Output); out != "" {
		b.WriteString("\n\nPrevious result:\n")
		b.WriteString(truncatePreviewText(out, 6000))
	}
	b.WriteString("\n\nFollow-up instructions:\n")
```

### 会话的身份与列表

- `fb_sessions` 已有 `parent_session_id`、`source`、`memory_mode`、`memory_source` 列（无需改表）。
- SRT-002 引入 `state.SessionBirth{Cwd, GitBranch}` 和 `SessionStore.EnsureAt(ctx, id, title, birth)`；SRT-005 给 `SessionBirth` 加 `Source string`（"what the session is for (SessionSource*); born with it, never changed by later writes"），`ensureSession` 的 INSERT 写 `source` 列，冲突时不更新。
- SRT-003 定义 `state.SessionSourceConversation = ""`、`SessionSourceWorkshop = "workshop"`、`SessionSourceCron = "cron"`，并让 `ListSessionsRecent` / `ListSessionsRecentPaged` 排除 `cron`，`ListSessionsForProject` 只列 `source = ''`。
- `ListChildSessionsRecent`（`pkg/state/session_store.go:393`）按 `parent_session_id` 列子会话，`pkg/tui/chat_slash.go:779` `AgentRosterSnapshot` 把它们当成 `Status: "available"` 的 subagent 行放进 roster。
- 记忆的一阶段候选查询（`pkg/memory/jobs.go` 约 `:133-139`）：`WHERE agent_id=? AND memory_mode=? AND id<>? AND memory_source IN (?,?) AND TRIM(cwd)<>'' ...`；SRT-005 在这里加 `AND source <> ?`（本地常量 `sessionSourceCron`，不 import `pkg/state`）。

### 模型

- `run.SubagentOwnModel(r, agentType)`（`pkg/run/runner.go:1579`）：类型在 `agents.definitions[<type>]` 下有 `llm_providers` 时返回第一个 provider/model/effort。
- `run.PrimaryModelForSession(r, store, sessionID)`（`pkg/run/config.go:1721`）：对话自己的模型。

## 设计

### 1. worker 会话的出生身份

- `state.SessionSourceSubagent = "subagent"`，加在 SRT-003 定义的三个常量旁边，注释："the private conversation of one subagent; reached only through that subagent's view, never listed".
- `state.SessionBirth` 加 `ParentSessionID string`（注释："the conversation this session belongs to; born with it, never changed by later writes"）。`ensureSession` 的 INSERT 写 `parent_session_id = NULLIF(birth.ParentSessionID, '')`，冲突时不更新。
- 新增 `run.ensureSubagentSession(fac Factory, entry agent.HistoryEntry) error`：`fac.Owner.SessionStore.EnsureAt(ctx, entry.WorkerSessionID, <标题>, state.SessionBirth{Source: state.SessionSourceSubagent, ParentSessionID: entry.SessionID})`。标题用 `entry.Title`（为空时用 `entry.AgentType`）。cwd 和分支取存储默认值（subagent 和它的对话在同一个目录里工作）。在 `prepareSubagentExecutionResolved`（`pkg/run/subagent.go:770`）生成 `entry` 之后、返回之前调用，失败就和 `CreateSubagentRun` 失败一样返回错误（取消 `cancel()`）。
- 列表排除：`ListSessionsRecent`、`ListSessionsRecentPaged` 的 `WHERE` 把 SRT-003 的 `source <> ?`（cron）扩成 `source NOT IN (?, ?)`，绑定 `SessionSourceCron`、`SessionSourceSubagent`。`ListChildSessionsRecent` 加 `AND source <> ?` 绑定 `SessionSourceSubagent`。`ListSessionsForProject`、`ListSessionsOfSource` 已按 `source = ?` 过滤，不用改。
- 记忆（README 决策 D5：不参与，owner 已批准）：`pkg/memory/jobs.go` 的候选查询把 SRT-005 加的 `source <> ?` 扩成 `source NOT IN (?, ?)`，再加一个本地常量 `sessionSourceSubagent = "subagent"`（注释写明与 `state.SessionSourceSubagent` 同值），在 `jobs.go` 对应的测试里断言两者相等。不要给 `pkg/memory` 的生产代码新增 import。

### 2. 落库只有一个实现，在组合根

`pkg/run` 不能 import `pkg/turn`，所以落库函数放在 `pkg/process`，通过 `pkg/run` 已有的执行器接口交给 fork 路径使用。`run.SubagentExecutor` 增加一个方法：

```go
	// PersistSubagentTurn writes what one subagent execution produced into the
	// subagent's own worker session, exactly as a primary turn is written: the
	// rows of the run's message list the session does not hold yet. err is the
	// execution's error; a failed or cancelled execution writes the partial
	// session it captured and answers the calls it interrupted.
	PersistSubagentTurn(ctx context.Context, turn SubagentTurn)
```

`run.SubagentTurn`（新类型，放在 `pkg/run/subagent.go`）：`WorkerSessionID`、`RunID`、`Model`、`Result *agent.Result`、`Partial []llm.Message`、`Err error`、`Started time.Time`。`process.(*Environment).PersistSubagentTurn` 的实现：

- `Err == nil`：`turn.PersistAssistantTurn(ctx, store, turn.AssistantTurn{SessionID: WorkerSessionID, RunID: RunID, Result: Result, Model: Model, End: ...})`。
- `Err` 是审批闸门（`run.IsRequiresAction(Err)`）：**什么都不写**。审批之后同一个执行会在 `runAcrossApprovals` 里重放并最终走到成功或失败分支，那时的消息列表包含全部内容；在闸门处写会留下悬空的 tool_calls 行。
- 其它错误（含取消）：`turn.PersistCancelledTurn(ctx, store, turn.CancelledTurn{SessionID: WorkerSessionID, RunID: RunID, Captured: Partial, Model: Model, End: ...})`。

所有测试替身（`grep -rn "func (.*) RunSubagentExec" pkg` 列出的全部类型）补一个空实现或记录调用的实现。

### 3. typed subagent：先写用户消息，结束时写结果

在 `RunSubagentSupervised` 里：

- 运行前：如果上下文里**没有**审批续跑状态（`tool.ToolApprovalResumeFromContext(ctx) == nil`），调用 `turn.PersistUserTurn(ctx, env.Deps.SessionStore, turn.UserTurn{SessionID: sid, ModelInput: task, RunID: childRunID})`。审批续跑时不写（第一次尝试已经写过）。
- 装上部分捕获：`capture := run.NewPartialSessionCapture()`，`agBase = run.WithPartialSessionCapture(agBase, capture)`。
- 运行后：`env.PersistSubagentTurn(ctx, run.SubagentTurn{WorkerSessionID: sid, RunID: <run.Run 返回的 run id>, Model: <见第 5 条>, Result: res, Partial: capture.Snapshot(), Err: err})`。run id 用 `opts.RunIDOut` 取。

### 4. fork subagent：出生时冻结 system，写入初始消息

在 `runForkSubagent` 调 `RunFork` 之前：

- 冻结 system：`fac.Owner.SessionStore.FreezeSessionPromptState(ctx, prep.workerSessionID, forkSystemPromptKey, <最终的 RenderedSystemPrompt，即已拼上 FileAccessScopeSystemPrompt 的那一份>)`。`forkSystemPromptKey = "fork_system_prompt"` 是 `pkg/run` 里的私有常量，注释说明它是这个 fork 一生不变的 system，继续时原样重放，所以缓存前缀不变。
- `RunFork` 增加参数 `RunParams.OnInitialMessages func([]llm.Message)`：`RunFork` 在得到 `subctx.InitialMessages` 后调用它。`runForkSubagent` 传入的回调用 `fac.Owner.SessionStore.AppendMessageSequenceForRun(ctx, workerSessionID, runID, initial, model, "")` 写入（这是纯存储操作，不属于 `pkg/turn` 的回合语义，可以在 `pkg/run` 里直接调用）。这样一来，执行中途失败时，worker 会话至少已经有完整的前缀和指令。
- 部分捕获：给 `agentCtx` 装 `run.NewPartialSessionCapture()`。
- `RunFork` 返回后（无论成败）：`fac.Owner.SubagentExecutor.PersistSubagentTurn(ctx, run.SubagentTurn{WorkerSessionID: prep.workerSessionID, RunID: prep.entry.RunID, Model: …, Result: outcome.Result, Partial: capture.Snapshot(), Err: err})`。`AppendMessageSequenceForRun` 的尾部比对会跳过已经写入的初始消息。

### 5. 每个 agent 用的模型：一个解析函数

plan reviewer 的模型是用户在审批浮层里当场选的（`WithSubagentModelOverride`，`pkg/run/subagent.go` 约 `:1906-1935`），今天这个选择没有记进 subagent 记录。owner 要求所有类型（含 plan-reviewer）都能被用户继续（README 决策 D2），继续时必须用它原来的模型，所以要把这个选择记下来：

- `agent.HistoryEntry` 加 `ModelProvider string \`json:"model_provider,omitempty"\``、`Model string \`json:"model,omitempty"\``，注释："the model a dispatch-time override put this agent on; empty when it runs on its definition's or the conversation's model"。账本是 JSONL，加字段不需要迁移。
- 派发时上下文里有 `subagentModelOverrideFromContext` 的值，就把它写进 `entry` 的这两个字段（在 `prepareSubagentExecutionResolved` 生成 `entry` 处）。
- 继续时（第 6 条）记录里有这两个字段，就用 `WithSubagentModelOverride` 把它装回上下文。

新增包级函数（`pkg/run/config.go`，紧挨 `PrimaryModelForSession`）：

```go
// AgentModel names the model one agent of a conversation runs on, the way
// the runtime routes its calls. subagent is nil for the primary agent. A
// subagent dispatched with a model override runs on it; a typed subagent
// whose definition has llm_providers runs on the first of them; the primary
// agent, a fork and a typed subagent without a chain of its own run on the
// conversation's model.
func AgentModel(r *Runner, conversationSessionID string, subagent *agent.HistoryEntry) (provider, model string)
```

实现按注释的顺序：记录里的覆盖 → `SubagentOwnModel(r, subagent.AgentType)`（只对 `AgentKind == "typed"`）→ `PrimaryModelForSession(r, r.SessionStore, conversationSessionID)`。第 3、4 条写入的 `Model` 用它（存的是 model id，和主会话写 `cliResultModel` 的口径一致；执行者先读 `pkg/tui/chat_turn.go` 的 `cliResultModel` 确认口径，不一致就 STOP）。计划 003、006 也用它。

### 6. 继续：从 worker 会话原样重放

改 `continueSubagentExecution`：

- **一次性类型的拒绝保留。** 这个函数是模型调用 `subagent_continue` 的路径，`OneShot`/`Continuable` 仍然约束模型（README 决策 D2）。用户在视图里直接对话不受这个限制，那条路在计划 005 里另有入口，不经过这里的拒绝。
- 判断这个 subagent 的 worker 会话里有没有历史：`fac.Owner.SessionStore.ListTranscriptMessages(ctx, record.WorkerSessionID, 1)`（或同等的最小查询）。
  - **有历史**（本计划之后产生的 subagent）：继续的输入就是消息原文 `msg`，不再拼 "Original task / Previous result"。
  - **没有历史**（本计划之前产生的 subagent，以及 worker 会话因为外部原因不存在）：继续的输入用今天的 `buildSubagentContinuePrompt(record, msg)`，这一次执行会把它作为第一条用户消息写进 worker 会话，之后就和新 subagent 一样。这是对旧数据（外部状态）的处理，不是兜底；在函数注释里写清楚这一点。
- typed：照旧 `executeSubagent(...)`，但传入上一条决定的输入。会话构建器会从 worker 会话读出全部历史。
- fork：新增 `continueForkSubagent(ctx, fac, record, input string) (string, error)`，**不再**走 `executeSubagent`：
  - system 取 `SessionPromptState(ctx, record.WorkerSessionID, forkSystemPromptKey)`；取不到（旧 fork）就按"没有历史"的规则处理：用 `buildSubagentContinuePrompt`，走今天的 `executeSubagent` fork-child 路径，并在注释里写明这是旧数据的唯一出路。
  - 历史取 worker 会话的模型上下文（与 `transcriptSession.history` 同一个读取：`ListTranscriptMessagesWithRefs` 后 `rehydrateImageReferences`；读法放进一个 `transcriptSession` 的方法里复用，不要复制一份）。它自动遵守压缩边界。
  - 调 `RunFork(ctx, RunParams{LLM: own.ForkLLM(), CacheSafe: &CacheSafeParams{RenderedSystemPrompt: frozen, SystemPrompt: frozen, ParentMessages: history}, PromptMessages: []llm.Message{llm.UserMessage(llm.Text(input))}, Tools/RegisterTools 与 runForkSubagent 相同, OnInitialMessages: <写入，同第 4 条>, ...})`。`RunFork` 会把 `ParentMessages + PromptMessages` 作为初始消息；写入时尾部比对只追加那条新用户消息。
  - 结束后同样 `PersistSubagentTurn`。
- 旁路 JSONL：`RunFork` 每次开始都把 `subctx.InitialMessages` 整段追加到旁路文件。继续时这会让旁路文件里重复出现整段历史。给 `RunParams` 加 `SidechainFrom int`（初始消息里从第几条开始写旁路；首次运行为 0，继续时为 `len(history)`），保持旁路文件"每条消息只出现一次"。

### 7. 不改的

- `mergeContinuationOutput` 和 `subagent_continue` 返回给主 agent 的 JSON 形状不变。
- 计划 005 之前，`continueSubagentExecution` 的上下文仍然 `WithoutTurnInputRuntime`。

## 缓存影响（必须留下证据）

1. **首次执行的请求逐字节不变。** typed：以前 worker 会话为空，构建器产出 `head + [用户消息]`；现在先写了一条用户消息，构建器读到它并用加工版替换，产出必须完全相同。fork：首次请求由 `RunFork` 在内存里构建，落库只是旁观者，请求不变。两者都写金样测试（见测试计划第 1、2 条）。
2. **继续时命中率上升。** 以前 `subagent_continue` 发出的是"空历史 + 一段新拼的提示"，除了工具和 system 以外整段都是新的；现在发出的是"上次的完整请求 + 一条新用户消息"，在 1 小时 TTL 内整段前缀都能命中。fork 的继续用冻结的 system 和原样的消息，前缀与它上次请求逐字节相同（测试计划第 3 条）。
3. 用 `.claude/skills/run-forebrain` 的 `usage` 模式无法区分前缀，所以真机数字在计划 009 里用真实模型测量。本计划只交金样测试。

## 范围

**只改这些文件：**
- `pkg/state/session_store.go`（常量、`SessionBirth.ParentSessionID`、列表过滤）及其测试
- `pkg/memory/jobs.go` 及其测试
- `pkg/agent/subagent_history.go`（`HistoryEntry` 的两个模型字段）及其测试
- `pkg/run/subagent.go`、`pkg/run/run.go`、`pkg/run/config.go`、`pkg/run/transcript.go` 及其测试
- `pkg/process/worker_cli.go`、`pkg/process/one_shot.go` 及其测试
- 所有实现 `run.SubagentExecutor` 的测试替身所在的测试文件
- `pkg/state` 的测试（列表过滤）

**不要动：**
- `pkg/tui/*`、`pkg/gateway/*`、`frontend/*`（界面行为在 005–008）。
- `pkg/run/orchestration_llm.go`、`pkg/run/llm_middleware.go` 的压缩逻辑（计划 003）。
- 表结构（不加列、不加表、不写迁移）。
- `SubagentStop` 钩子读旁路 JSONL 的契约。

## 步骤

### 第 1 步：会话身份与列表

- 如果 SRT-005 未 DONE：照 SRT-005 原文给 `SessionBirth` 加 `Source string`，`ensureSession` 的 INSERT 写 `source` 列、冲突时不更新；并在 `docs/plan/SCHEDULED_RUNS_TRANSCRIPT/README.md` 的 005 行备注"`SessionBirth.Source` 已由 SUBAGENT_CONVERSATION/002 加上"。
- 按"设计"第 1 条加 `SessionSourceSubagent`、`ParentSessionID`，改四处列表和记忆查询。SRT-005 未 DONE 时，记忆查询里还没有 `source <> ?`：直接加 `AND source <> ?` 只绑定 `sessionSourceSubagent`（SRT-005 以后会把它扩成 `NOT IN`，在 SRT README 的 005 行备注这一点）。

**验证**：在 `pkg/state` 的测试里加 `TestSubagentSessionsAreBornHiddenFromEveryList`：`EnsureAt` 一个普通会话 `c1` 和一个 `SessionBirth{Source: SessionSourceSubagent, ParentSessionID: "c1"}` 的 `w1`；断言 `ListSessionsRecent`、`ListSessionsRecentPaged`、`ListChildSessionsRecent("c1")` 都不含 `w1`；`SELECT parent_session_id, source FROM fb_sessions WHERE id='w1'` 为 `c1`、`subagent`；再对 `w1` 调一次 `Ensure`，两列不变。`pkg/memory` 测试断言 `sessionSourceSubagent == state.SessionSourceSubagent`，并加一条：`source='subagent'` 的会话不进入一阶段候选。
`CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/memory -count=1` → `ok`。

### 第 2 步：模型解析与落库端口

按"设计"第 2、5 条加 `HistoryEntry` 的模型字段、`run.AgentModel`、`run.SubagentTurn`、接口方法和 `process` 的实现，补全测试替身。`pkg/run` 加表驱动测试 `TestAgentModelResolvesEachKindOfAgent`：主 agent（nil）、fork、无自有模型的 typed、有 `llm_providers` 的 typed、记录里带覆盖模型的 plan reviewer，各自得到预期的 provider/model。

**验证**：`go build -tags fts5 ./...` → 成功；`CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/process -count=1` → `ok`；`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1` → `ok`（没有新增 import，`Runner` 方法数不变）。

### 第 3 步：worker 会话出生

在 `prepareSubagentExecutionResolved` 里调用 `ensureSubagentSession`。

**验证**：加 `TestSubagentDispatchBirthsItsWorkerSession`（`pkg/run/subagent_test.go`，用该文件已有的 `fixedSubagentExecutor` 之类替身和测试存储）：派发一个 typed subagent 后，worker 会话存在，`source='subagent'`、`parent_session_id` 是对话 id。`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestSubagentDispatchBirthsItsWorkerSession' -count=1` → `ok`。

### 第 4 步：typed subagent 落库

按"设计"第 3 条改 `RunSubagentSupervised`。

**验证**：在 `pkg/process` 的测试（`worker_cli` 对应的测试文件；若不存在，用覆盖 `worker_cli.go` 的 `pkg/process/worker_cli_test.go`）加：
- `TestTypedSubagentWritesItsConversationToTheWorkerSession`：用测试 LLM 让 subagent 调一次工具再回答；结束后 worker 会话的行依次是 user（任务原文）、assistant（tool_calls）、tool、assistant（答案），没有重复。
- `TestFailedTypedSubagentKeepsWhatItDid`：第二次模型调用返回网络错误；worker 会话里有 user、assistant（tool_calls）、tool 三行（`PersistCancelledTurn` 写入的部分），没有悬空的 tool_calls。
- `TestApprovalResumeDoesNotRewriteTheSubagentPrompt`：第一次尝试在工具审批处返回 `RequiresActionError`，带审批续跑上下文再跑一次并成功；用户消息只有一行。

`CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'TypedSubagent|ApprovalResumeDoesNotRewrite' -count=1` → `ok`。

### 第 5 步：fork subagent 落库

按"设计"第 4 条改 `runForkSubagent`、`RunFork`（`OnInitialMessages`、`SidechainFrom`）。

**验证**：`pkg/run` 加 `TestForkSubagentWritesItsInheritedPrefixAndItsWork`：worker 会话的行 = 父请求去掉 system 的消息 + 指令 + fork 的产出；`SessionPromptState(worker, "fork_system_prompt")` 等于 fork 第一次请求的 system 文本（用捕获请求的测试 LLM 读出来比对）。`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'ForkSubagent' -count=1` → `ok`。

### 第 6 步：继续从 worker 会话重放

按"设计"第 6 条改 `continueSubagentExecution`，加 `continueForkSubagent`。

**验证**：`pkg/run` 加：
- `TestTypedContinuationReplaysTheWorkerConversation`：先跑一次 typed subagent（会调工具），再 `subagent_continue` 发 `"continue"`；捕获继续的第一次请求，断言它的消息 = 上一次执行最后一次请求的消息 + 上一次的答案 + `user("continue")`，且不含 "Continue the existing worker task"。
- `TestForkContinuationReplaysItsOwnPrefixByteForByte`：同上，fork 版；system 与上次逐字节相同。
- `TestContinuationOfALegacySubagentSeedsItsWorkerSession`：worker 会话为空的旧记录，继续时第一条用户消息是 `buildSubagentContinuePrompt` 的结果，并被写入 worker 会话；再继续一次时输入是原文。

`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'Continuation' -count=1` → `ok`。

### 第 7 步：全量与架构

**验证**：`gofmt -l pkg cmd` 无输出；`go vet -tags fts5 ./...` 退出码 0；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全部 `ok`；`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1` → `ok`；`scripts/package-graph.sh && git diff --exit-code pkg/architecture/testdata/graph.json` → 退出码 0；死代码与基线一致。

## 测试计划

除各步列出的测试外，**金样测试**两条（缓存证据）：

1. `TestTypedSubagentFirstRequestIsUnchangedByPersistence`（`pkg/process`）：同一个任务，分别在"worker 会话为空、不写用户消息"（模拟改动前：直接调 `run.Run`）和"按本计划先写用户消息"两种情况下捕获第一次请求，`json.Marshal` 后逐字节相等。
2. `TestForkSubagentFirstRequestIsUnchangedByPersistence`（`pkg/run`）：fork 首次请求在加入落库前后逐字节相等（落库前的基线用一个不装 `OnInitialMessages`、不调 `PersistSubagentTurn` 的调用得到）。

结构参照 `pkg/run/subagent_test.go` 里已有的执行器替身和 `pkg/run/orchestration_llm_test.go` 里捕获请求的测试 LLM 写法。

## 完成标准

- [ ] 第 1–6 步列出的测试和两条金样测试全部通过
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全部 `ok`
- [ ] 架构测试、包依赖图、死代码检查通过
- [ ] `grep -rn "Original task:" pkg/run/subagent.go` 只出现在 `buildSubagentContinuePrompt` 里，且它只被"worker 会话没有历史"的分支调用
- [ ] README 状态行已更新

## STOP 条件

- SRT-002 或 SRT-003 未 DONE。
- 金样测试显示首次请求有任何字节变化，且原因不是测试本身写错。
- fork 的继续请求与它上次请求不能逐字节相同，原因是某类消息无法经 `fb_messages` 原样往返（例如某个字段不入库）。报告是哪类消息、哪个字段，不要改成"近似相同"。
- 需要改表结构才能完成。
- 写入 worker 会话导致 `AppendMessageSequenceForRun` 重复追加（说明尾部比对的前提不成立）。

## 维护说明

- 以后任何新的 subagent 执行入口，都必须在结束时调用 `PersistSubagentTurn`，否则这个 subagent 就又回到"失败即失忆"。评审时检查 `RunFork` 和 `executeSubagent` 的所有调用点。
- 删除对话的功能（例如 SRT-006 的定时任务对话保留期）删除一个会话时，必须同时删除 `parent_session_id` 指向它、`source='subagent'` 的 worker 会话；外键是 `ON DELETE SET NULL`，不会自动级联。
- worker 会话的 system 冻结在 `fb_session_prompt_state` 里；改 fork 的 system 组成（例如 `FileAccessScopeSystemPrompt` 的文案）只影响新 fork，旧 fork 的继续仍用它出生时的 system。这是缓存铁律要求的，不是缺陷。
