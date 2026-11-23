# Plan 124：工具边流边执行——每个工具调用一写完就开始执行；需要审批的调用推迟到流结束；结果按调用顺序回填

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在 dev 工作区。
> 工作区里若有与本计划无关的未提交改动，**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）**：`plans/122`、`plans/125`、`plans/126`、`plans/123` 已完成：`grep -n "func WithStreamProgress" pkg/llm/stream_progress.go`、`grep -n "func (p \*StreamProgress) MarkToolDispatched" pkg/llm/stream_progress.go`、`grep -n "func NewStreamRetry" pkg/llm/stream_retry.go`、`grep -n "func ReplayToSink" pkg/llm/nonstreaming.go`、
> `grep -n "type PartialResponseError struct" pkg/llm/error.go`、`grep -n "func withResponseCollector" pkg/run/response_collector.go`、`grep -n "func steerPreemptedResult" pkg/run/orchestration_llm.go` 各有结果。
>
> **Drift check**：按下面摘录里的符号定位；锚点找不到或内容不符即 STOP 1。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: HIGH（改变工具执行时机，涉及审批、插话、取消、落库顺序）
- **Depends on**: plans/122、plans/125、plans/126、plans/123（`plans/127` 在本计划之后，把“审批推迟到流结束”换成当场审批）
- **Category**: direction（owner 2026-10-25 定稿：工具调用边流边执行）
- **Planned at**: dev `4d7a3c9` + 121 + 122 + 125 + 126 + 123，2026-10-25（同日补：并发安全按调用的具体输入判断）

## Why this matters

现在模型的整条回复返回后才开始执行工具；一次回复里有多个调用、或后面的调用参数很长（写大文件、派子代理）时，前面的调用白白等着。目标行为：

1. 流式请求里，适配器报告某个工具调用写完（`OnToolCallReady`）就把它放进执行队列，按下面规则启动：
   - 队列先进先出；一个调用能启动的条件：没有正在执行的调用，或者它是并发安全的且所有正在执行的也都并发安全；同时执行数不超过 `tool.MaxToolConcurrencyFromEnv()`。
   - 不能启动的调用挡住它后面的所有调用。
   - 并发安全按**这次调用的具体输入**判断（与 Claude Code 的 `isConcurrencySafe(input)` 一致，Step 4a 的 `(*tool.State).CallConcurrencySafe(call)`）：元数据 `ReadOnly && ConcurrencySafe && !Destructive` 的工具恒为安全；`shell` 在命令可证明只读（`safety.ShellCommandIsKnownSafe`）且没有请求沙箱放宽时安全；参数解析失败一律不安全。流结束后的分组（`executeToolCalls`）也改用同一判断。
2. **需要审批或需要用户回答的调用不在流中执行**：它在审批判定处被推迟（不创建审批、不问人）；从它被推迟起不再启动新的调用，它和其余没执行的调用都等流结束后按现有方式执行（审批照常停回合、续跑照常）。已经执行完的调用不会重跑。
3. 排队插话（121）：每次启动前检查；有插话就不再启动本轮剩下的调用，给它们“未执行”结果（121 的 `steerPreemptedResult`）。
4. 一旦本次请求有工具开始执行，就标记 `MarkToolDispatched`：之后这次请求只会按已完成部分收尾（123），绝不会整请求重发。
5. 落库顺序与崩溃恢复：每启动一个调用之前，先把“已写完的正文 + 截至该调用的工具调用”作为 assistant 消息写入（已存在就补全），保证调用落盘早于它的副作用；流结束后照常补全为最终消息，工具结果按调用顺序写在它后面。
6. 取消（Esc）：正在执行的工具随回合取消；已开始的调用都得到结果（完成的用真实结果，未完成/未开始的用中断结果），与 assistant 消息一起写入后再返回取消。
7. 两端界面无需新机制：工具卡在工具开始时出现（现有 `tool_call_started` 事件），TUI/web 在工具卡出现前已封存之前的正文（现有行为）。
8. 适配器没报 ready 的调用（例如 compat 流式时从推理里合成的调用）照现有方式在流结束后执行。非流式重发（`plans/126`）的结果经 `ReplayToSink` 回放，同样逐个报 ready，因此也走执行器（与 Claude Code 把非流式回答交给同一个执行器一致）。

## Current state

- `pkg/run/orchestration_llm.go`：
  - `type orchestratedToolResult struct { index int; call llm.ToolCall; parts []llm.ContentPart; timing llm.ExecutionTiming; display *llm.ToolDisplayState; err error; notRun bool }`。
  - 主循环：`callCtx = withSteerDeliveryResponseStart(callCtx, inFlightSteers)`；`123` 之后紧接 `llm.WithStreamProgress` 与 `withResponseCollector`；`res, err := w.inner.Execute(callCtx, session, tools)`；
    `123` 的 `PartialResponseError` 处理块；有工具分支：`session = append(session, *res.Message)` → `noteResponseCompleted()` → `partialCapture.Set(session)` → `transcript.flush(ctx, session, modelForTranscript(), latestUsage)` →
    `toolCtx := withForkRuntimeSnapshot(w.state.ContextWithRuntimeSessionMode(ctx), session)` → `results := w.executeToolCalls(toolCtx, res.Message.ToolCalls, toolMap)` → `firstControlFlowToolError(results)`（审批停回合并附快照）→ 按 `res.Message.ToolCalls` 顺序把结果接进会话。
  - `func (w *toolOrchestrationLLM) executeToolCalls(ctx, calls, toolMap) []orchestratedToolResult`：分组执行，121 的 `steerWaiting()` 预检，审批错误（`isRequiresActionError`）时提前返回。
  - `func (w *toolOrchestrationLLM) executeOneToolCall(ctx, index, call, toolMap, suppress toolUISuppression) orchestratedToolResult`：先发 `tool.StepKindToolStarted`，再 `executeToolHandler`，再发 `tool.StepKindToolCompleted`（含 attempts），最后返回结果。
  - `type toolUISuppression struct { started, completed bool }`。
  - `executeConcurrentBatch` 里 `limit := tool.MaxToolConcurrencyFromEnv()`。
- `pkg/run/fork.go` `withForkRuntimeSnapshot(ctx, messages)`；`pkg/run/subagent.go` 派 fork 子代理时要求快照**最后一条是带工具调用的 assistant 消息**（`last.Role == llm.RoleAssistant && len(last.ToolCalls) > 0`）。
- 审批：`pkg/run/runner.go` `func (r *Runner) actionHook(ctx, kind, payload) (string, bool, error)`：前段是纯策略判定（deny/allow/yolo/never/granular），
  然后依次是 `if !exitPlanMode && !isRequestPermissions && r.permissionRequestHookApplies(kind) {`（外部钩子）→ guardian 审查 → `r.Actions.CreatePending(...)`（创建待审批）。
  各工具在需要审批时调用 `st.ActionHook()` 返回的函数，拿到 `(id, true, nil)` 就返回 `*tool.RequiresActionError`。
  例外：`pkg/tool/registry.go` 的提问工具（`userInteractionToolName = "user_interaction"`）直接 `rt.Actions.CreateAsk(ctx, form)`，不经钩子。
- 插话：`pkg/run/turn_input.go` `(*TurnInputRuntime).HasSteers()`、`(*SteerDelivery).Commit()`/`Retracted()`；插话在下一次请求“开始产出”时提交（`withSteerDeliveryResponseStart`），工具 ready 必然晚于开始产出。
- 落库：`pkg/state/message_sync.go` `planTranscriptAppend` 对齐已存行与本次消息序列；`func assistantGainedToolCalls(stored, finished llm.Message) bool`（只认“已存行没有调用、最终有调用”）时用
  `s.UpdateMessageParts(ctx, tx, completion.rowID, MessagePartsJSON(completion.message, text))` 补全；`UpdateMessageParts`（`pkg/state/session_store.go`）只更新 `parts` 列。`fb_messages` 没有 FTS 触发器。
- 进度：`pkg/llm/stream_progress.go` `StreamProgress.MarkToolDispatched()`。工具调用写完时进度已是 `Output`，所以 123 的 `NewStreamRetry` 此后只会返回 `PartialResponseError`（收尾，或请求错误/未知时 `Failed: true`），两种情况编排层都保留已完成部分、执行工具并继续（123 Step 6）。
- 并发：`pkg/tool/orchestration.go` `func PartitionToolCalls(toolNames []string, metas map[string]event.ToolMeta) []ToolCallPlan`（`safe := ok && meta.ReadOnly && meta.ConcurrencySafe && !meta.Destructive`，相邻的安全调用并成一组）与 `func (s *State) PartitionToolCalls(toolNames []string) []ToolCallPlan`；`executeToolCalls` 里 `plans := w.state.PartitionToolCalls(names)`。`shell` 工具元数据为 `event.ToolMeta{Destructive: true, InterruptBehavior: "interrupt"}`（`pkg/tool/registry.go`），参数结构 `ShellInput{Command; SandboxPermissions; AdditionalPermissions; …}`（`pkg/tool/shell_tool.go`）；`pkg/safety/shell.go` `func ShellCommandIsKnownSafe(command string) bool`。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Go 测试 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" FOREBRAIN_HOME="$(mktemp -d)"; CGO_ENABLED=1 go test -tags fts5 ./pkg/run/... ./pkg/tool/... ./pkg/state/... ./pkg/tui/... ./pkg/gateway/... -count=1` | 全部 `ok` |
| race | 同上环境；`CGO_ENABLED=1 go test -tags fts5 -race ./pkg/run/ -run 'Streaming|Stream' -count=1` | `ok`，无 `DATA RACE` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/...`；`gofmt -l pkg/` | 无输出 |
| 真机 | `.claude/skills/run-forebrain/driver.sh`（Step 8） | 见期望 |

## Scope

**In scope**：`pkg/tool/`（新增推迟信号；`registry.go` 提问工具一处；`orchestration.go` 按输入判定并发安全，+test）、`pkg/run/runner.go`（`actionHook` 一处）、`pkg/run/orchestration_llm.go`(+test)、新建 `pkg/run/streaming_tools.go` 与 `pkg/run/streaming_tools_test.go`、
`pkg/run/response_collector.go`（加一个读取“已写完正文块”的方法 `completedParts() []llm.ContentPart`）、
`pkg/state/message_sync.go`、`pkg/state/session_store.go`（新增一个更新方法）及其测试、`.claude/skills/run-forebrain/fake_provider.py` 与 `SKILL.md`（Step 8 的模式）。

**Out of scope**：适配器（122/125/126）；重试/收尾判定（123）；TUI/web 渲染（工具卡沿用现有事件）；`executeToolCalls` 的审批停回合逻辑与 `segmentToolCallPlan`（127 改）。

## Steps

### Step 1：推迟信号（`pkg/tool`）

新增（放在 `request_permissions.go` 旁的合适文件，英文注释）：

```go
// ErrDeferToStreamEnd is returned instead of asking anyone when a tool call
// started while its response was still streaming reaches an approval or a
// question. Nothing was created; the call runs again once the stream ends.
var ErrDeferToStreamEnd = errors.New("tool call deferred to the end of the stream")

func WithStreamingDispatch(ctx context.Context) context.Context
func StreamingDispatchFromContext(ctx context.Context) bool
```

`registry.go` 提问工具：在 `form, err := humanInputToAskForm(in)` 之前加 `if StreamingDispatchFromContext(ctx) { return nil, ErrDeferToStreamEnd }`（已带 `ApprovedActionIDFromContext` 的续跑分支在它前面，保持不变）。

### Step 2：审批钩子里推迟

`runner.go` `actionHook`：在 `if !exitPlanMode && !isRequestPermissions && r.permissionRequestHookApplies(kind) {` **之前**加：

```go
	// A call started while its response still streams never asks anyone and
	// never runs a reviewer: it waits for the stream to end and asks then,
	// through the ordinary path, with the turn able to stop on it.
	if tool.StreamingDispatchFromContext(ctx) {
		return "", false, tool.ErrDeferToStreamEnd
	}
```

前面的纯策略判定（拒绝、允许、YOLO、never、granular）保持在它之前，照常生效。

**Verify**：`CGO_ENABLED=1 go build ./pkg/run/ ./pkg/tool/` → exit 0；`pkg/run` 里加一个单测：带 `WithStreamingDispatch` 的 ctx 调 `actionHook` 时，策略为询问 → 返回 `ErrDeferToStreamEnd` 且 `Actions` 里没有新建待审批；策略为允许 → 照常放行。

### Step 3：单个调用的推迟结果

`orchestratedToolResult` 加 `deferred bool`。`executeOneToolCall` 在 `rawResult, parts, timing, err := executeToolHandler(...)` 之后：

```go
	if errors.Is(err, tool.ErrDeferToStreamEnd) {
		// Not a completion: the call re-runs after the stream. Its started
		// card stays up and the re-run updates it in place (same step id).
		return orchestratedToolResult{index: index, call: call, deferred: true}
	}
```

（不发 completed 事件、不记 tracer 结果。）

### Step 4a：按输入判定并发安全（`pkg/tool/orchestration.go`）

```go
// CallConcurrencySafe reports whether this call, with these arguments, may run
// alongside other concurrency-safe calls. A tool whose metadata says so always
// may; shell may when its command is provably read-only and it asks for no
// sandbox override. Arguments that do not parse never may.
func (s *State) CallConcurrencySafe(call llm.ToolCall) bool

// PartitionToolCallsFor is PartitionToolCalls by call: adjacent calls that are
// each CallConcurrencySafe form one group.
func (s *State) PartitionToolCallsFor(calls []llm.ToolCall) []ToolCallPlan
```

- `CallConcurrencySafe`：元数据判断与 `PartitionToolCalls` 的 `safe` 同式；否则名字为 `shell` 时把 `call.Function.Arguments` 解析成 `struct{ Command string \`json:"command"\`; SandboxPermissions string \`json:"sandbox_permissions"\`; AdditionalPermissions json.RawMessage \`json:"additional_permissions"\` }`：
  解析失败 → false；`SandboxPermissions` 非空且不是 `use_default` → false；`AdditionalPermissions` 非空且不是 `null`/`{}` → false；否则 `safety.ShellCommandIsKnownSafe(Command)`。其余 → false。
- `PartitionToolCallsFor` 的分组规则与 `PartitionToolCalls` 相同，只是 `safe` 用 `CallConcurrencySafe`；`ToolNames` 照填（`executeToolCalls` 用它的长度切批次）。
- `executeToolCalls`：`plans := w.state.PartitionToolCalls(names)` 改为 `plans := w.state.PartitionToolCallsFor(calls)`（`names` 不再需要就删掉）。
- 测试（`orchestration_test.go` 或同目录现有测试文件）：`ls -la`、`cat a.txt`、`git status` 为安全；`rm a`、`echo x > f`、`ls; rm a`、带 `"sandbox_permissions":"require_escalated"` 的 `ls`、参数不是 JSON 为不安全；`read_file` 恒安全；两个只读 shell 相邻并成一组、中间夹一个写命令拆成三组。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run 'Concurrency|Partition' -count=1` → `ok`；`./pkg/run/...` 现有测试 `ok`（若某个现有测试因“只读 shell 现在并发”而失败，回报用例名——STOP 7）

### Step 4：流中执行器（`pkg/run/streaming_tools.go`）

```go
// streamingToolExecutor starts tool calls while their response streams. It is
// fed by OnToolCallReady and finished by finish once the response is over.
type streamingToolExecutor struct {
	w        *toolOrchestrationLLM
	toolMap  map[string]*llm.Tool
	progress *llm.StreamProgress
	// beforeStart runs, under no lock, right before a call starts: it commits
	// the steers riding on the request, persists the partial assistant message
	// and returns the context the call runs under (nil = do not start).
	beforeStart func(readyUpTo []llm.ToolCall) context.Context
	limit       int

	mu      sync.Mutex
	calls   []*streamedCall // ready order
	halted  bool            // a deferred, preempted or not-startable-in-stream call stops further starts
	closed  bool            // the stream ended; finish took over
	wg      sync.WaitGroup
}

type streamedCall struct {
	call   llm.ToolCall
	safe   bool
	state  int // queued, executing, done, deferred, notRun
	result orchestratedToolResult
}
```

方法：
- `add(call llm.ToolCall)`：`closed` 后忽略；追加为 queued（`safe: e.w.state.CallConcurrencySafe(call)`），调用 `pump()`。
- `pump()`（持锁挑选，锁外启动）：从头找第一个 queued 调用；`halted` 则停；插话检查——`ctx` 未取消且 `TurnInputRuntimeFromContext(ctx).HasSteers()` 为真时，把它和后面所有 queued 标 `notRun`（结果用 `steerPreemptedResult(index, call)`）、`halted = true`、停；
  能否启动按 Why 第 1 条；不能启动则停（挡住后面的）。能启动：状态改 executing，`progress.MarkToolDispatched()`，锁外调 `beforeStart(截至该调用的 ready 列表)` 得到 `callCtx`；为 nil 则恢复 queued、`halted = true` 并停；
  否则 `wg.Add(1)` 起 goroutine：`res := e.w.executeOneToolCall(tool.WithStreamingDispatch(callCtx), index, call, e.toolMap, toolUISuppression{})`，持锁记录：`res.deferred` → 状态 deferred、`halted = true`；否则 done、存结果；锁外再 `pump()`。
  一次 `pump` 可以连续启动多个（并发安全的）调用。
- `finish(ctx, finalCalls []llm.ToolCall) []orchestratedToolResult`：`closed = true`；`wg.Wait()` 等正在执行的全部结束；
  按 `finalCalls` 的顺序建立结果：ID 相同且状态 done/notRun 的取已有结果（**绝不重跑**）；其余调用（deferred、queued、或未曾 ready 的）**保持原顺序**组成 `rest`，交给 `e.w.executeToolCalls(ctx, rest, e.toolMap)`（现有逻辑：分组、121 预检、审批停回合），
  结果按调用 ID 填回对应位置（`index` 改为在 `finalCalls` 中的位置）。`executeToolCalls` 因审批提前返回时，`rest` 里没拿到结果的调用保持“无结果”，由现有的审批快照/续跑逻辑处理（续跑按“缺结果的调用”重放）。
  `finalCalls` 里找不到的已执行调用（不应发生）：`slog.Warn` 并把它追加到 `finalCalls` 末尾以保持“每个已执行调用都有结果”。返回与 `finalCalls` 等长、顺序一致的结果。
- `abort(ctx) []orchestratedToolResult`（取消用）：`closed = true`；`wg.Wait()`；对每个已 ready 的调用给结果：done 用真实结果，其余用 `orchestratedToolResult{call: c, err: context.Canceled}` 形式（`toolResultMessage` 会写成错误文本）。
- `dispatched() bool`：是否启动过任何调用。

### Step 5：接入主循环

在 `toolOrchestrationLLM.Execute` 主循环里：
1. 有流式 sink 时（`llm.StreamSinkFrom(callCtx) != nil`）建执行器：`progress` 取 123 安装的那个（保留 `llm.WithStreamProgress` 的返回值），`collector` 取 123 的收集器；
   `beforeStart` 做三件事：
   - `inFlightSteers` 非 nil 时 `inFlightSteers.Commit()`；若 `inFlightSteers.Retracted()` 返回 nil（不启动）。
   - `prov := llm.AssistantMessage(<collector 已写完的正文块>, ToolCalls: readyUpTo)`，`provSession := append(append([]llm.Message(nil), session...), prov)`；`partialCapture.Set(provSession)`；`transcript.flush(ctx, provSession, modelForTranscript(), nil)`。
   - 返回 `withForkRuntimeSnapshot(w.state.ContextWithRuntimeSessionMode(ctx), provSession)`。
   给 `callCtx` 的 sink 加一层：`OnToolCallReady` → `exec.add(call)` 后再调下游（下游原本为 nil 也要装上）。
2. `res, err := w.inner.Execute(...)` 返回后：
   - 取消（`ctx.Err() != nil` 或 `errors.Is(err, context.Canceled)`）且 `exec.dispatched()`：`results := exec.abort(ctx)`；把 `prov`（最后一次的 provisional assistant）与这些结果按顺序接进 `session`，`partialCapture.Set`、`transcript.flush`，然后 `return nil, err`。
   - 123 的 `PartialResponseError` 块之后，`res` 是收集器结果（其 `ToolCalls` 就是 ready 列表）；照常走有工具分支（123 已发提示），执行器里已完成的调用不重跑。
3. 有工具分支里把 `results := w.executeToolCalls(toolCtx, res.Message.ToolCalls, toolMap)` 改为：`exec != nil && exec.dispatched()` 时 `results := exec.finish(toolCtx, res.Message.ToolCalls)`，否则保持原调用。
   若 `finish` 追加了 `finalCalls` 外的调用，同步把它加进 `res.Message.ToolCalls`（在 `session = append(session, *res.Message)` 之前完成，以便最终消息与结果一致——为此把 `finish` 的“对齐”部分拆成 `exec.reconcile(final []llm.ToolCall) []llm.ToolCall` 先调用）。
4. 无工具分支：`exec.dispatched()` 不可能为真（有 ready 必有工具调用）；若为真，`slog.Error` 并按有工具处理（防御）。

### Step 6：落库补全（`pkg/state`）

1. `message_sync.go`：把 `assistantGainedToolCalls` 改为 `assistantRowNeedsCompletion(stored, finished llm.Message) bool`：assistant，且（`len(stored.ToolCalls) == 0 && len(finished.ToolCalls) > 0`，或 `stored` 的调用 ID 依次是 `finished` 的前缀且 `len(stored.ToolCalls) < len(finished.ToolCalls)`，或调用 ID 完全相同但 `messageText(stored) != messageText(finished)`）。
2. 补全时同时更新正文：`session_store.go` 新增 `func (s *SessionStore) UpdateMessageContentAndParts(ctx context.Context, q dbtx, rowID int64, content, partsJSON string) error`（归属校验与 `UpdateMessageParts` 相同），`AppendMessageSequenceForRun` 的补全循环改调它（`content` 用 `strings.TrimSpace(completion.message.TextContent())`）。
3. 测试（照 `message_sync` 现有测试）：先写 provisional（正文 + 1 个调用）→ 再写最终（正文 + 2 个调用 + 2 条结果）→ 库里该 assistant 行变为 2 个调用、只有一行、结果在其后；纯文本行、旧的“无调用 → 有调用”补全仍通过。

### Step 7：测试（`pkg/run/streaming_tools_test.go` 与 `orchestration_llm_test.go`）

写法照 `orchestration_llm_test.go` 里脚本化模型 + 测试工具的用例（例如 121 的 `TestQueuedSteerPreemptsUnstartedSerialCalls`）。脚本化模型在 `Execute` 内部调用 `sink.OnToolCallReady(call)`，并阻塞在测试控制的 channel 上，从而能断言“工具在 Execute 返回前已开始”。新增：
1. `TestToolStartsBeforeTheResponseEnds`：ready 第 1 个调用后模型阻塞，测试等到该工具执行（工具里关闭一个 channel），再放行模型结束；最终结果与会话顺序正确、工具只执行一次。
2. `TestUnsafeCallBlocksTheOnesBehindIt`：非并发安全调用 A 执行中 ready 了并发安全调用 B → B 在 A 结束后才开始。
3. `TestSafeCallsRunTogether`：两个并发安全调用同时执行（用计数器断言并发度为 2）。
4. `TestGatedCallIsDeferredToStreamEnd`：测试 action hook 对 C 返回待审批；流中 C 不创建审批（`ErrDeferToStreamEnd`）、其后的 D 不启动；流结束后走 `executeToolCalls`，回合停在 C 的审批上（`RequiresActionError`），快照里 C 之前的调用都有结果、D 没有执行。
   另加一例：B（并发安全、需要审批）与 C（并发安全、无需审批）同时启动，B 被推迟、C 已完成 → 流结束后只重跑 B，C 只执行过一次。
5. `TestQueuedSteerStopsFurtherStarts`：A 执行中入队插话 → 后续 ready 的调用都得到“未执行”结果。
6. `TestStreamFailureAfterDispatchKeepsPartialAndNeverResends`：A 已开始后网络错误 → inner 只调用 1 次；A 的结果在会话里；回合继续（123 的收尾 + 提示）。
7. `TestCancelWhileToolsRunWritesInterruptedResults`：A 执行中取消 → 返回取消；会话/落库里有 assistant（含 A）与 A 的结果（中断）。
8. `TestCallsNotSeenAsReadyRunAfterTheStream`：模型只在最终结果里给出调用（不报 ready）→ 与现在一样在流结束后执行。
9. `TestProvisionalAssistantRowIsCompletedNotDuplicated`：带进度存储跑第 1 条 → 库里只有一行 assistant，最终含全部调用。

**Verify**：Go 测试命令全部 `ok`；race 命令无 `DATA RACE`；`gofmt`、`go vet` 干净。

### Step 8：真机验收（TUI，run-forebrain）

1. `fake_provider.py` 加模式 `streamtools`（写法照现有 `shell`/`multishell` 模式；compat 流式工具调用分片格式照 `serve_netdrop` 附近的 `chunk(...)` 工具）：第 1 次请求先发正文 delta，再发工具调用 index 0（`shell`，命令 `touch "$FAKE_MARKER"`），
   然后**轮询等待** `$FAKE_MARKER` 文件出现（最长 10 秒；超时就把下一个调用的命令换成 `echo MARKER MISSING`），再发 index 1（`echo two`）与结束帧；之后的请求回答 `done`。`FAKE_MARKER` 由环境变量传入，`driver.sh` 透传。
   在 `SKILL.md` 模式表加一行说明。
2. 跑：

```bash
D=.claude/skills/run-forebrain/driver.sh
FAKE_MARKER="$(mktemp -u)" $D start streamtools
$D submit 'run them'
$D wait 'done' 40
$D screen    # 期望：两张工具卡（touch …、echo two）；任何地方都没有 "MARKER MISSING"
$D messages  # 期望：一行 assistant（两个调用）、两条工具结果、最后 done
$D stop
```

   （没有 MARKER MISSING 说明第一个工具在响应结束前就已执行。）
3. 真模型冒烟（`real-model-acceptance` 规则：主树、隔离 home、glm-5.3、key 从 `.env` 注入）：让模型“依次运行 `echo a` 和 `echo b` 两个命令并汇报”，回合正常完成、两张工具卡、结果正确；附 `screen` 输出。

**Verify**：两段期望满足，输出附在报告里。

## Done criteria

- [ ] Step 2、6、7 的测试全部通过；Go 测试命令全部 `ok`；race 无 `DATA RACE`
- [ ] `grep -n "ErrDeferToStreamEnd" pkg/run/runner.go pkg/tool/registry.go pkg/run/orchestration_llm.go` 各有结果
- [ ] 真机 `streamtools` 无 `MARKER MISSING`；真模型冒烟通过
- [ ] `git status --short` 只含 Scope 内文件

## STOP conditions

1. 前置不满足或锚点找不到。
2. 某个工具在调用 action hook **之前**就已产生副作用（写文件、执行命令、发网络请求）——推迟会让它在流结束后再做一次；回报工具名与位置。
3. 审批续跑（approval resume）的既有测试因“流中已执行的调用”而失败（快照里调用与结果对不上）——回报用例名，不要改续跑逻辑去迁就。
4. `planTranscriptAppend` 的既有测试因补全规则扩展而失败。
5. race 检测报出执行器与 sink 回调之间的数据竞争，两次修正无效。
6. 某一步验证连续两次修正后仍失败。
7. 现有测试因“只读 shell 命令现在可以并发”而失败（Step 4a）——回报用例名，不要为迎合测试把 shell 改回恒不安全。

## Maintenance notes

- 新增会询问用户或创建审批的工具，必须经过 action hook，或像提问工具一样在流式派发下返回 `ErrDeferToStreamEnd`，否则它会在流中途停住回合。
- 流中执行的调用与最终消息按 ID 对齐；适配器转换工具调用的函数必须是 ready 与最终结果共用的那一个（122）。
- `MarkToolDispatched` 之后这次请求只会收尾；任何新增的重发层都要遵守 `llm.ResendSafe`。
- 并发安全只有 `CallConcurrencySafe` 一个口径，流中与流后共用；新工具若有“按输入才安全”的情况，在这里加分支，不要在执行器里另判。
- `plans/127` 会把 Step 1–2 的“推迟到流结束”限定为没有当场审批通道的运行（IM 渠道、定时任务）；有通道时流中直接当场审批。
