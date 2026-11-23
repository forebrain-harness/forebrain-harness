# Plan 122：流式块信号与请求进度——适配器报告“正文块写完 / 工具调用写完 / 收到结束原因 / 收到结束帧”，引擎记录每次请求的进度，各端封存写完的正文块

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在 dev 工作区。
> 工作区里若有与本计划无关的未提交改动，**不要碰、不要回滚、不要格式化**。
>
> **前置（必须全部满足，否则 STOP 1）**：dev 工作区已包含 115–119（网络重试）与 121（插话抢占）：
> `grep -n "func WithUpwardJitter" pkg/llm/retry.go`、`grep -n "func WithStreamWatchdog" pkg/llm/*.go`、
> `grep -n "func steerPreemptedResult" pkg/run/orchestration_llm.go`、`grep -n "func transportRetryStatusLine" pkg/tui/run.go` 各有 1 行。
>
> **Drift check**：本计划按符号定位，不按行号。下面 "Current state" 每段摘录都给了可 grep 的锚点；锚点找不到或内容不符即 STOP 1。

## Status

- **Priority**: P1（`123`、`124`、`125`、`126` 都依赖它）
- **Effort**: M
- **Risk**: LOW–MED（只新增信号与记录，不改变重试或工具执行行为；唯一可见变化是 Anthropic/Responses 的正文块在块结束时就封存）
- **Depends on**: 115–119、121 已在 dev（115–120 已在 dev `4d7a3c9`；121 的剩余改动在 owner 的 `stash@{0}`，需先回到 dev）
- **Category**: direction（owner 2026-10-25 定稿：工具边流边执行 + keepPartial）
- **Planned at**: dev `4d7a3c9`（含 115–120）+ 121，2026-10-25（初稿写于 `bb8d116` + `exec/110-120`；10-25 按 owner 要求补了结束帧信号与耗时/睡眠记录）

## Why this matters

后续计划要在“流还没结束”的时候做决定：`123` 要按这次请求已经产出了什么来决定断流后是整请求重发还是按已完成部分收尾；`124` 要在每个工具调用一写完就开始执行。
两者都需要适配器在流中途报告四种边界，以及引擎按同一口径记录本次请求的进度。本计划只提供这些信号与记录，并让各端在正文块结束时封存它（否则后续撤下进行中的块时会误撤已写完的正文）。

进度口径（`123` 的判定表直接用它）：

| 进度 | 条件 |
|---|---|
| `Nothing` | 还没有任何事件 |
| `Started` | 有事件，但没有可见输出、没有写完的块 |
| `PartialOutput` | 已出现正文或工具调用，但没有任何块写完 |
| `ThinkingOnly` | 有写完的块，但都是思考块，没有正文/工具调用 |
| `Output` | 有写完的块，且已出现正文或工具调用 |

另记：`StopReasonReceived`（收到 provider 的结束原因）、`Complete`（收到 provider 的结束帧，即这次回答已完整到达）、`ToolDispatched`（`124` 开始执行过本次请求里的工具）、`Elapsed`（本次尝试开始至今的单调时钟时长）、`Slept`（本次尝试期间墙钟比单调时钟多走的时长，即电脑睡眠的时长）。`123` 的判定表按 Claude Code 的口径使用全部这些字段。

## Current state

- `pkg/llm/stream.go` `type StreamSink struct`：已有 `OnDelta`、`OnReasoningDelta`、`OnReasoningDone`、`OnResponseStarted`、`OnWebSearch`、`OnUsage`、`OnUsageSnapshot`、`OnResponseCompleted`、`OnTransportRetry`、`OnEnd`、`Streamed`。
  `WithMutedForegroundStream` 把可见类回调置 nil（`muted.OnDelta = nil` … `muted.OnTransportRetry = nil`）。
- **compat**（`pkg/llm/openai/compat_llm.go`）：`executeChatCompletionStream` 的 recv 循环里 `if len(d.ToolCalls) > 0 { mergeStreamToolCalls(&toolBuf, d.ToolCalls) }`；
  `ch.FinishReason` 非空时 `lastFinish = ch.FinishReason`；正文 `d.Content` 走 `sink.OnDelta`。最终消息由 `compatMessageFromOpenAI(openMsg)` 转换，工具调用在其 `if len(m.ToolCalls) > 0 {` 循环里逐个转成 `llm.ToolCall{ID: tc.ID, …}`。
  `mergeStreamToolCalls` 按 `d.Index` 累积（同一 index 的 ID/Name/Arguments 拼接）。OpenAI 兼容流按 index 顺序逐个发工具调用：**某 index 首次出现时，所有更小 index 的调用都已写完**；收到结束帧或正常收流时全部写完。
- **Anthropic**（`pkg/llm/anthropic/agent_llm.go` `for stream.Next() {` 循环）：`content_block_start` / `content_block_delta` / `content_block_stop` 分支，`content_block_stop` 目前只处理思考块（`thinkingBlocks[event.Index]` → `OnReasoningDone`）；
  循环末尾 `if err := res.Accumulate(event); err != nil`（SDK 累积到 `res.Content[index]`）。最终转换 `agentAnthropicMessageFromAPI` 里 `case "tool_use":` 把块转成 `llm.ToolCall{ID: b.ID, Type: llm.ToolTypeFunction, Function: {Name: b.Name, Arguments: args}}`（`args` 为空时用 `"{}"`）。
  `message_delta` 分支目前只读 usage（`event.Delta.StopReason` 未使用）。
- **Responses**（`pkg/llm/openai/responses_llm.go` `executeStreaming`）：`response.output_item.added`（function_call 建 `toolCallsMap[callID]`）、`response.function_call_arguments.done`（写入最终参数）、`response.output_item.done`、`response.completed`。
- **Google**（`pkg/llm/google`）目前是非流式 `generateContent`；`plans/125` 把它改为流式并补齐本计划的四个信号，**本计划不改它**。
- 事件：`pkg/event/run_events.go` 的 `RunEventReasoningDone` 与 `IsPushOnlyStreamEvent`（push-only 列表）；发布点 `pkg/run/subagent.go` `EventStreamSink` 的 `OnReasoningDone: func() { publishEvent(…, event.RunEventReasoningDone, event.ReasoningDonePayload{AgentID: agentID}) }`。
- TUI：`pkg/tui/notify.go` `ReasoningDoneMsg{turn, AgentID, Timestamp}` 与事件分支 `case event.RunEventReasoningDone:`；前台 sink 在 `pkg/tui/chat_turn.go` `prepareTUIAgentBase` 里（`OnReasoningDone:` 一项）；
  `pkg/tui/reducer.go` `case ReasoningDoneMsg:` → `r.flushReasoningFinal(r.agentBuf(…), m.Timestamp)`；同文件已有 `func (r *Reducer) flushAssistantFinal(buf *agentBuffer) []Frame`。
- web：`frontend/src/composables/useChatStream.ts` 主对话 `case 'reasoning_done':`（`closeStreamedBlocks(message.blocks ?? [], ['thinking'], evt.createdAt)`）与子代理 `case 'reasoning_done':`（同样写法）；事件类型联合在 `frontend/src/lib/forebrainGatewayRuntime.ts` `ForebrainRunEventType`。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Go 测试 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" FOREBRAIN_HOME="$(mktemp -d)"; CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/run/... ./pkg/tui/... ./pkg/event/... ./pkg/gateway/... -count=1` | 全部 `ok` |
| vet / fmt | 同上环境；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/...`；`gofmt -l pkg/` | 无输出 |
| 前端 | `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts && pnpm typecheck` | 通过 / exit 0 |

## Scope

**In scope**：`pkg/llm/stream.go`、新建 `pkg/llm/stream_progress.go` 与 `pkg/llm/stream_progress_test.go`、`pkg/llm/openai/compat_llm.go`(+test)、`pkg/llm/anthropic/agent_llm.go`(+test)、
`pkg/llm/openai/responses_llm.go`(+test)、`pkg/event/run_events.go`、`pkg/run/subagent.go`、`pkg/tui/notify.go`、`pkg/tui/chat_turn.go`、`pkg/tui/reducer.go`、TUI 相关测试、
`frontend/src/composables/useChatStream.ts`(+test)、`frontend/src/lib/forebrainGatewayRuntime.ts`。

**Out of scope**：任何重试判定、工具执行时机（`123`、`124`）；Google 适配器（`125`）；`StreamPartial`、gateway `stream_parts`（不处理新事件）；落库。

## Steps

### Step 1：`StreamSink` 新回调

在 `OnTransportRetry` 之前加（英文注释照下面的意思写）：

```go
	// OnToolCallReady fires once per tool call, as soon as that call's
	// arguments are complete inside the stream — before the response ends.
	// The call is identical (ID, name, arguments) to the one the final
	// Result carries.
	OnToolCallReady func(ToolCall)
	// OnTextBlockDone fires when an answer-text block ended inside the
	// response. Providers without an in-stream text boundary fire it when the
	// first tool call begins.
	OnTextBlockDone func()
	// OnStopReason fires when the provider sent the response's finish reason.
	OnStopReason func(reason string)
	// OnMessageStop fires when the provider's terminal frame for the response
	// arrived: nothing of this response is still missing. Providers whose
	// finish reason is itself the terminal frame fire it right after
	// OnStopReason.
	OnMessageStop func()
```

`WithMutedForegroundStream` 里把这四个也置 nil（嵌套内部调用不参与外层的进度与工具执行）。

**Verify**：`CGO_ENABLED=1 go build ./pkg/llm/` → exit 0

### Step 2：`pkg/llm/stream_progress.go`

```go
type StreamProgressKind int

const (
	ProgressNothing StreamProgressKind = iota
	ProgressStarted
	ProgressPartialOutput
	ProgressThinkingOnly
	ProgressOutput
)

// StreamProgressSnapshot is what one request had produced when it was read.
type StreamProgressSnapshot struct {
	Kind               StreamProgressKind
	StopReasonReceived bool
	// Complete: the provider's terminal frame arrived (OnMessageStop).
	Complete       bool
	ToolDispatched bool
	// Elapsed is the monotonic time since this attempt started.
	Elapsed time.Duration
	// Slept is how much longer the wall clock ran than the monotonic clock
	// over this attempt: the time the machine spent asleep.
	Slept time.Duration
}

// ResendSafe reports whether re-sending the whole request cannot duplicate
// anything the user already has: no tool started and no finished block shown.
func (s StreamProgressSnapshot) ResendSafe() bool { return !s.ToolDispatched && s.Kind != ProgressOutput }

// StreamProgress records one model request's progress. Safe for concurrent use.
type StreamProgress struct { /* mu sync.Mutex; anyEvent, sawReasoning, anyBlockFinished, anyOutputShown, stopReason, complete, toolDispatched bool; startedAt time.Time */ }

func (p *StreamProgress) Snapshot() StreamProgressSnapshot
func (p *StreamProgress) Reset()               // a resend starts a new attempt: clears every flag, startedAt = time.Now()
func (p *StreamProgress) MarkToolDispatched()

// WithStreamProgress installs a fresh progress record for the request made
// under the returned context, observing the context's stream sink.
func WithStreamProgress(ctx context.Context) (context.Context, *StreamProgress)
func StreamProgressFrom(ctx context.Context) *StreamProgress
```

`Kind` 计算：`anyBlockFinished` → `anyOutputShown ? Output : ThinkingOnly`；否则 `anyOutputShown ? PartialOutput : (anyEvent ? Started : Nothing)`。
`startedAt` 在 `WithStreamProgress` 与 `Reset` 时取 `time.Now()`（带单调读数）；`Snapshot` 里 `Elapsed = time.Since(startedAt)`，`Slept = max(0, time.Now().Round(0).Sub(startedAt.Round(0)) - Elapsed)`（`Round(0)` 去掉单调读数即墙钟差；macOS/Linux 的单调时钟在睡眠期间不走）。

`WithStreamProgress` 复制 sink（`wrapped := *sink`），对**原本非 nil** 的回调链式包装（先记录再调用下游），对原本为 nil 的 `OnToolCallReady`/`OnTextBlockDone`/`OnStopReason`/`OnMessageStop`/`OnResponseStarted` 也装上只记录的版本：
- `OnResponseStarted` → `anyEvent`；非空 `OnReasoningDelta` → `anyEvent` + `sawReasoning`
- 非空 `OnDelta` → `anyEvent` + `anyOutputShown`；此前 `sawReasoning` 为真时同时 `anyBlockFinished`（思考在正文开始时就已写完——compat 没有单独的思考结束帧，三家统一按此口径）
- `OnReasoningDone` → `anyBlockFinished`
- `OnTextBlockDone` → `anyBlockFinished` + `anyOutputShown`
- `OnToolCallReady` → `anyEvent` + `anyBlockFinished` + `anyOutputShown`
- `OnStopReason` → `stopReason`
- `OnMessageStop` → `complete`
没有 sink 时返回 `ctx` 与一个不会被任何回调更新的记录（Snapshot 为 `Nothing`）。注意 `OnDelta`/`OnReasoningDelta` 原本为 nil 时**保持 nil**（muted 语义）。

`WithMutedForegroundStream`（`pkg/llm/stream.go`）同时摘掉进度记录：在返回的 ctx 上放一个 nil 的 `*StreamProgress`（`StreamProgressFrom` 对它返回 nil）。嵌套的内部请求（回合中途的压缩、评审器等）与前台请求共用 ctx 链，不摘掉的话它们的重试层会读到前台请求的进度。测试加一例：`StreamProgressFrom(WithMutedForegroundStream(ctxWithProgress)) == nil`。

`stream_progress_test.go`：表驱动覆盖五种 Kind（含“思考 delta 后出现正文 delta ⇒ `Output`”、“只有正文 delta ⇒ `PartialOutput`”、“只有思考 delta ⇒ `Started`”、“思考 + `OnReasoningDone` ⇒ `ThinkingOnly`”）、`Complete`、`Reset`（清空且 `Elapsed` 从 0 起）、`MarkToolDispatched`、`ResendSafe`、`Elapsed` 单调增长、未睡眠时 `Slept` 为 0（睡眠本身无法在单测里模拟，只测为 0 与非负），以及“muted ctx 下 OnDelta 仍为 nil”。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ -run StreamProgress -count=1` → `ok`

### Step 3：compat 适配器

在 `executeChatCompletionStream` 中：
1. 维护 `readyCount := 0`（index `< readyCount` 的调用都已发过 ready）与 `textDone := false`。
2. 处理 `d.ToolCalls` 时：首个工具 delta 到来且 `contentAcc.Len() > 0 && !textDone` → 先发一次 `OnTextBlockDone`、`textDone = true`。
   然后对每个 delta 的 index `j`（`d.Index` 为 nil 视为 0）：若 `j >= len(toolBuf)`（这个 index 首次出现），在 `mergeStreamToolCalls` **之前**把 `readyCount .. j-1` 逐个发 ready 并把 `readyCount` 设为 `j`。
3. 收到结束帧（`lastFinish` 由空变非空）时：把 `readyCount .. len(toolBuf)-1` 逐个发 ready，然后 `OnStopReason(string(lastFinish))`、`OnMessageStop()`（OpenAI 兼容流的结束原因帧就是结束帧）。正常收流退出循环且未截断时，同样补发剩余 ready（已发过的不再发）。
4. 发 ready 前若 `toolBuf[i].ID` 为空，就地写入 `fmt.Sprintf("call_%d_%s", i, <8 位随机十六进制>)`，保证最终消息里同一调用 ID 相同。
5. 抽出 `func compatToolCallFromOpenAI(tc openaiclient.ToolCall) llm.ToolCall`，`compatMessageFromOpenAI` 的工具调用循环与 ready 都用它（字段完全一致）。
6. 长度续写轮（`openAIReasoningLengthShouldContinue`）沿用同一组计数，不重复发。从推理里合成的工具调用（`synthesizeToolCallsFromReasoning`）**不发** ready。

测试（`compat_llm_test.go`，用现有 httptest 假 SSE 的写法）：两个工具调用分段到达 → ready 依次触发两次、第一次在第二个调用的首个分片到达时、第二次在结束帧时；ready 的 ID/参数与最终 `Result.Message.ToolCalls` 逐字相同；无 ID 时补的 ID 与最终一致；先正文后工具 → `OnTextBlockDone` 在首个工具分片前触发一次；`OnStopReason("tool_calls")` 与 `OnMessageStop` 各触发一次、顺序在最后一个 ready 之后。

### Step 4：Anthropic 适配器

1. 抽出 `func anthropicToolCallFromBlock(b anthropicapi.ContentBlockUnion) llm.ToolCall`（与 `agentAnthropicMessageFromAPI` 的 `case "tool_use":` 完全一致，`agentAnthropicMessageFromAPI` 改为调用它）。
2. 循环里在 `res.Accumulate(event)` **之后**：若 `event.Type == "content_block_stop"` 且 `int(event.Index) < len(res.Content)`：块类型 `tool_use` → `OnToolCallReady(anthropicToolCallFromBlock(res.Content[event.Index]))`；`text`（且文本非空白）→ `OnTextBlockDone()`。
3. `message_delta` 分支里 `event.Delta.StopReason` 非空 → `OnStopReason(string(event.Delta.StopReason))`。
4. `event.Type == "message_stop"` → `OnMessageStop()`。

测试：照现有流式测试的假事件序列写：text 块 → tool_use 块 → message_delta → message_stop；断言触发顺序为 `OnTextBlockDone`、`OnToolCallReady`、`OnStopReason`、`OnMessageStop`，ready 与最终消息的工具调用逐字相同；另一例在 message_delta 之后断开（无 message_stop）→ 有 `OnStopReason`、没有 `OnMessageStop`。

### Step 5：Responses 适配器

1. `response.function_call_arguments.done` 写入参数后：若该 callID 未发过 → `OnToolCallReady(*tc)` 并记入 `readyEmitted[callID]`。
2. `response.output_item.done` 的 `done.Item.Type == "function_call"`：若未发过，用 `done.Item.CallID/Name/Arguments` 构造并发出（兜底）。`done.Item.Type == "message"` → `OnTextBlockDone()`。
3. `response.completed` → `OnStopReason("completed")`、`OnMessageStop()`。

测试：照现有 Responses 流式测试写，断言 ready 次数、顺序与最终工具调用一致；message 项结束触发 `OnTextBlockDone`。

**Verify（Step 3–5）**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... -count=1` → `ok`

### Step 6：`assistant_done` 事件与两端封存

1. `run_events.go`：`RunEventAssistantDone = "assistant_done"`、`type AssistantDonePayload struct { AgentID string \`json:"agent_id,omitempty"\` }`，加入 `IsPushOnlyStreamEvent`。
2. `subagent.go` `EventStreamSink`：`OnTextBlockDone: func() { publishEvent(ctx, sink, runID, sessionID, event.RunEventAssistantDone, event.AssistantDonePayload{AgentID: agentID}) }`。
3. TUI：`notify.go` 加 `AssistantDoneMsg{turn *foregroundTurn; AgentID string}` 与事件分支；`chat_turn.go` 前台 sink 加 `OnTextBlockDone: func() { s.notifyUIForSession(sessionID, AssistantDoneMsg{turn: foreground}) }`；
   `reducer.go` 加 `case AssistantDoneMsg:`（`isWithdrawn` 判断照 `ReasoningDoneMsg`）→ `return EventResult{Frames: r.flushAssistantFinal(r.agentBuf(strings.TrimSpace(m.AgentID)))}`。
4. web：`forebrainGatewayRuntime.ts` 事件联合加 `'assistant_done'`；主对话与子代理各加 `case 'assistant_done':`，写法照各自的 `reasoning_done`，把 `['thinking']` 换成 `['assistant']`。
5. 测试：TUI reducer 收 `AssistantDoneMsg` 后当前 assistant 块变 Final、之后的正文开新块；web 单测 `assistant_delta` → `assistant_done` → `assistant_delta` 得到两个 assistant 块，第一个 `open: false`。

**Verify**：全部 Go 测试 `ok`；前端单测与 `pnpm typecheck` 通过

## Done criteria

- [ ] Step 2–6 的测试全部存在并通过；`go vet`、`gofmt -l pkg/` 干净
- [ ] 三个适配器的 ready 调用与最终 `Result` 的工具调用逐字一致（测试断言）
- [ ] 没有任何重试/工具执行行为变化：`git diff --stat` 不含 `pkg/llm/retry.go`、`pkg/run/llm_middleware.go`、`pkg/run/orchestration_llm.go`
- [ ] `git status --short` 只含 Scope 内文件

## STOP conditions

1. 前置检查不满足或锚点找不到。
2. 某个 SDK 的事件结构里拿不到 `content_block_stop` 对应块的完整参数（Anthropic）或 `function_call_arguments.done`（Responses）——回报字段情况。
3. compat 的某个现有测试显示工具调用分片不按 index 顺序到达——回报样例，不要改成“按结束帧统一发”以外的猜测规则。
4. 某一步验证连续两次修正后仍失败。

## Maintenance notes

- ready 的调用必须与最终 `Result` 逐字一致：`124` 用 ID 把流中启动的调用与最终消息对上；改工具调用转换逻辑时两处共用同一个函数。
- 新增流式适配器时要补这四个信号；没有它们，`123` 的进度会偏保守（判为 `PartialOutput`），`124` 只能在流结束后执行工具。
