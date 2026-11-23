# Plan 123：断流处理与 Claude Code 一致——按失败原因 × 请求进度决定重发、改用非流式重发、按已完成部分收尾或报错；重发时撤下这次尝试显示过的一切

> **2026-11-04 owner 修订**：静默重发（改用非流式重发、只完成思考后的重发、流停滞重发）对用户完全透明——不进重试行编号，也不占用 `network_retry_max` 共享预算（各自仍受自身计数器限制）；重试行编号只数已显示的重试，放弃句 `gave up after N` 的 N 即最后一张卡片的编号，只发生过静默重发时原样返回错误。静默手段用完、判定表要报错或要以“只有思考”收尾（用户拿不到回答）时，若是网络/服务端类失败（断连、睡眠、截断、停滞、服务端错误、超时）且重发不会重复已交付内容，剩余预算改为显示卡片的普通重试（重发同样撤下已写完的思考），直到 N/N 才按判定表原结局放弃或收尾；过载保留自身 3 次上限，非网络错误不重试。实现见 `pkg/llm/stream_retry.go` 的 `silentResend`、`resendableAfterFallbacks`。下文“共用预算”的描述以此为准。
>
> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在 dev 工作区。
> 工作区里若有与本计划无关的未提交改动，**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）**：`plans/122`、`plans/125`、`plans/126` 已完成：`grep -n "func WithStreamProgress" pkg/llm/stream_progress.go`、`grep -n "OnMessageStop func" pkg/llm/stream.go`、
> `grep -n "RunEventAssistantDone" pkg/event/run_events.go`、`grep -n "func (c \*agentLLM) executeStreaming" pkg/llm/google/stream.go`、`grep -n "func WithNonStreamingRequest" pkg/llm/nonstreaming.go`、`grep -n "func ReplayToSink" pkg/llm/nonstreaming.go` 各有结果；
> 115–119、121 已在 dev（`grep -n "func WithUpwardJitter" pkg/llm/retry.go`、`grep -n "func steerPreemptedResult" pkg/run/orchestration_llm.go`）。
>
> **Drift check**：按下面摘录里的符号定位；锚点找不到或内容不符即 STOP 1。

## Status

- **Priority**: P1（`124` 依赖它：工具一旦开始执行，这次请求就绝不能整请求重发）
- **Effort**: L
- **Risk**: HIGH（改变断流后的行为：部分情况不再自动重试；新增非流式重发；重发时会撤下已写完的思考并删除其落库行）
- **Depends on**: plans/122、plans/125、plans/126
- **Category**: direction（owner 2026-10-25 定稿：断流处理必须与 Claude Code 一致，含 keepPartial、非流式重发、流中服务器错误收尾、重发时连已写完的思考块一起撤下）
- **Planned at**: dev `4d7a3c9` + 121 + 122 + 125 + 126，2026-10-25（同日重写：判定表改为 Claude Code 的完整口径）

## Why this matters

请求在流中途失败时，现在一律整请求重发。owner 要求与 Claude Code 完全一致：**按失败原因与这次请求已经产出了什么，在四种结果里选一种**——

| 结果 | 含义 |
|---|---|
| 重发（流式） | 同一请求再发一次 |
| 改用非流式重发 | 同一请求改发一次非流式请求（`plans/126`），这次 `Execute` 之后的尝试都是非流式 |
| 收尾（keepPartial） | 不重发；按已写完的块收尾 |
| 报错 | 不重发；以错误结束（已写完的块照样保留） |

判定输入（全部来自 `plans/122` 的 `StreamProgressSnapshot` 与错误本身）：进度 `Kind`（Nothing / Started / PartialOutput / ThinkingOnly / Output）、`StopReasonReceived`、`Complete`、`Elapsed`、`Slept`，以及失败原因（Step 1 的 `ClassifyStreamFailure`）。
判定表就是 Claude Code 的表，Step 1 给出逐行移植的 Go 代码，**以代码为准**。要点：

- 已有正文或工具调用写完（`Output`）：原因是请求错误/未知 → 报错；其他原因 → 收尾。
- 只有思考写完（`ThinkingOnly`）：连接类断流最多重发 2 次后收尾；卡死重发 1 次后收尾；服务器错误/超时（未收到结束原因）重发 2 次/1 次后报错；收到结束原因时连接类收尾、服务器类报错。
- 其余（Nothing / Started / PartialOutput）：连接断开按总重试次数重发，用完改用非流式；卡死在 Nothing 时重发 1 次、否则直接改用非流式；截断重发 1 次后改用非流式；服务器错误、格式损坏、请求错误、未知 → 直接改用非流式；过载（529）在总次数内最多 3 次后改用非流式，PartialOutput 时直接改用非流式。
- 回答已完整到达（`Complete`）后才断的连接类错误、或已有输出后的服务器类错误 → 视为成功，不提示。
- 请求还没有任何产出就失败、且带 HTTP 状态码（≥400）或是 DNS/证书错误的，按请求级规则：408/409/5xx 重发（用完报错）；404 改用非流式（只一次）；529 与其他原样交给上层（现有 529 层、限流、压缩、图片降级）。
- 取消永远原样返回。

**收尾**：结果 = 已写完的正文块 + 已写完的工具调用。有工具调用 → 照常执行并继续下一次模型请求，对话里出一行落库的提示；没有 → 写完的正文落库，回合以提示文案结束。提示文案（与 Claude Code 一致，去掉其 `API Error:` 前缀）：

| 已有可见输出（正文或工具调用开始出现） | 没有可见输出 |
|---|---|
| 卡死：`The response stopped arriving. The response above may be incomplete.` | 卡死：`The response stalled before a response was produced. Try again.` |
| 服务器错误：`Server error mid-response. The response above may be incomplete.` | — |
| 格式损坏：`The response stream was malformed. The response above may be incomplete.` | 格式损坏：`The response stream was malformed and no response was produced. Try again.` |
| 电脑睡眠：`Your computer went to sleep mid-response. The response above may be incomplete.` | 电脑睡眠：`Your computer went to sleep before a response was produced. Try again.` |
| 其他：`Connection lost mid-response. The response above may be incomplete.` | 其他：`Connection lost before a response was produced. Try again.` |

**已有输出后报错**（Output + 请求错误/未知）：已写完的块保留；有工具调用 → 照常执行并继续，提示行的文字是错误本身；没有 → 正文落库，回合以原错误结束（现有错误显示）。

**重发与改用非流式时撤下这次尝试显示过的一切**：按判定表，重发只会发生在“还没有正文或工具调用写完”的时候，所以这次尝试显示过的只有进行中的块和**已写完的思考块**。各端撤下两者；已写完的思考块在写完时已单独落库成一行（`transcriptProgress.reasoningDone`），也要删掉。收尾与报错时不撤已写完的块。

**一次性警告**：因截断或“流打开后一个事件都没有”而改用非流式时，本进程第一次在对话里出一行警告（落库）：
`Streaming response ended before any complete data was received. Retrying without streaming. If this keeps happening, check any proxy or gateway between forebrain and your model provider.`

**其他重发层**（空输出重试、529 重试、上下文超限压缩后重试、图片降级重试）：只有 `ResendSafe()` 为真时才重发，重发前重置进度；遇到 `PartialResponseError` 原样往上传；529 层只处理“还没有任何产出”的 529。

## Current state

- `pkg/llm/retry.go`：`type retryConfig struct`（`maxAttempts`、`initialBackoff`、`maxBackoff`、`upwardJitter`、`shouldRetry`、`retryAfter`、`maxRetryAfter`、`notify`），选项 `WithShouldRetry`、`WithMaxBackoff`、`WithUpwardJitter`、`WithRetryAfter(of, limit)`、`WithRetryNotify(fn)`；
  `func (r *retryLLM) Execute`：`clearPending` 闭包（`pendingRetry.Swap(0)` > 0 时发 `TransportRetry{Retry, Total, Cleared: true}`）、包装 sink 的 `OnDelta`/`OnReasoningDelta`（首个非空 delta 时 `clearPending()`）、循环里 `result, err := r.inner.Execute(...)`、`shouldRetry`、计算 `sleep`（`backoff` + 上浮抖动；`retryAfter` 超过 `maxRetryAfter` 时直接返回错误，大于 `sleep` 时取它）、`notify(TransportRetry{…})`、等待、`backoff` 翻倍封顶。
- `pkg/run/llm_middleware.go`：链为 `withImageHeal` → `wrapBlankOutputRetryLLM` → `wrapNetworkRetryLLM`（最外层重试）→ `recoverableLLM`。
  `func wrapNetworkRetryLLM(inner llm.LLM, maxRetries int, streamIdle time.Duration) llm.LLM`：`llm.NewRetry(1+maxRetries, llm.WithShouldRetry(llm.IsRetryableNetworkError), llm.WithMaxBackoff(32*time.Second), llm.WithUpwardJitter(), llm.WithRetryAfter(llm.RetryAfterOf, 5*time.Minute), llm.WithRetryNotify(…sink.OnTransportRetry…))`；
  `networkRetryLLM.Execute` 装看门狗并把 `MaxAttemptsExceededError` 改写为 `network error, gave up after N retries: …`。`maxRetries` 为 `agents.defaults.network_retry_max`（默认 10，上限 `appcfg.MaxNetworkRetryMax` = 15），与 Claude Code 相同。
  `wrapBlankOutputRetryLLM`：`llm.NewRetry(blankOutputRetryAttempts, llm.WithShouldRetry(llm.APIErrorIsBlankModelOutput))`。
  `func (r recoverableLLM) Execute`：`isContextWindowExceeded(lastErr)` → `r.forceCompact(...)` 后再 `r.inner.Execute(ctx, compacted, tools)`；`isStatus529(lastErr) && ShouldRetryStatus529(...)` → 循环重发。
  `pkg/run/runner.go` 里 `summaryChain := wrapNetworkRetryLLM(withImageHeal(llmClient), …)`（压缩摘要，嵌套请求，sink 已静音）。
- `pkg/llm/image_heal.go` `func (h *imageHealLLM) Execute`：`IsImageRejection(err)` 时降级图片后 `return h.inner.Execute(ctx, degraded, tools)`。
- `pkg/llm/error.go`：`ErrStreamTruncated`；`IsRetryableNetworkError`（`ErrStreamStalled`、`ErrNonStreamingTimeout`（126）→ true；取消/超时 → false；DNS NXDOMAIN 与 x509 证书错误 → false；`net.Error`、`io.ErrUnexpectedEOF`、`io.EOF`、`ECONNRESET`/`ECONNABORTED`、`ErrStreamTruncated` → true；`*APIError` 408/409/5xx（非 529）→ true）；`type APIError struct { StatusCode int; Code, Type, Message string; Err error; RateLimit; RetryAfter }`；`func statusOf(err error) int`。
- `pkg/llm/stream.go` `type TransportRetry struct { Retry, Total int; Wait time.Duration; Err error; Status int; Cleared bool }`。
- `pkg/run/orchestration_llm.go`：
  - `recordReasoningBlocks`：`OnReasoningDone`（以及思考后的第一个正文 delta）→ `transcript.reasoningDone(sinkCtx, text, startedAt, time.Now())`，即**每个写完的思考块立即落库为一行 `reasoning`**；`OnTransportRetry` → `transcript.dropInFlightReasoning()`。
  - 主循环：`callCtx = withSteerDeliveryResponseStart(callCtx, inFlightSteers)` → `res, err := w.inner.Execute(callCtx, session, tools)` → `abandonCall(nil)` → 成功时 `inFlightSteers.Commit()` → `if inFlightSteers.Retracted() {…}` → `if err != nil {…; return res, err}` → 无工具分支（`sessionHasToolExecution(session) && isEmptyAssistantResult(res)` 报错；`transcript.flush` 最终回答；`return endTurn(res, latestUsage)`）→ 有工具分支（`session = append(session, *res.Message)`、`noteResponseCompleted()`、`partialCapture.Set(session)`、`transcript.flush(...)`、`results := w.executeToolCalls(...)` …）。
- `pkg/run/transcript_progress.go`：`reasoningDone` 调 `t.store.AppendStructuredMessageForRun(ctx, …, "reasoning", text, …)`（返回 `(int64, error)`，第一个是行 ID，现在被丢弃）；`dropInFlightReasoning` 只清内存缓冲；`transcriptProgressStore` 接口有 `AppendMessageSequenceForRun`、`AppendStructuredMessageForRun`。
- 撤下进行中内容的现有机制（每条 `TransportRetry` 通知，含 `Cleared`）：TUI 前台 sink（`pkg/tui/chat_turn.go`）`accum.Reset()`；`pkg/tui/reducer.go` `case TransportRetryMsg:` 清 `buf.assistant`/`buf.reasoning`；`pkg/tui/run.go` 调 `(*Renderer).WithdrawDeadStreamBlocks(agentID)`（`reducer.go`：从 `vm.blocks` 末尾弹出**非 Final** 的 assistant/thinking 块，遇到 Final 或其他类型即停）；
  web 主对话 `case 'transport_retry': {`（裁掉 `state.fullAnswer` 末尾的进行中文本后撤块）与子代理 `case 'transport_retry':`（`dropOpenStreamedBlocks(entry.blocks)`），`function dropOpenStreamedBlocks(blocks)` 只滤掉 `open` 的 assistant/thinking；
  gateway `pkg/gateway/run_control.go` `partial.Reset()`；`pkg/gateway/stream_parts.go` `case event.RunEventTransportRetry:` 里 `if json.Unmarshal(evt.Payload, &p) != nil || p.Cleared { return }`（Cleared 时不清）；事件载荷 `event.TransportRetryPayload`（`pkg/event/run_events.go`），由 `pkg/run/subagent.go` `EventStreamSink` 的 `OnTransportRetry` 发布。
- 落库提示的先例：`image_rejected`——发布 `pkg/run/image_input.go` `withImageRefusalRecorder`；TUI 直播 `pkg/tui/notify.go` `imageRejectedMessage`/`case event.RunEventImageRejected:`，重放 `pkg/tui/commands.go` `replayImageRejectedMessage`；web `useChatStream.ts` `if (evt.type === 'image_rejected') { insertImageRejectedNotice(...) }`。
  TUI 帧：`reducer.go` 的 `FrameError`、`FrameSystem`。web：时间线块 `{ kind: 'error'; id?; text; detail? }`；纯文字提示消息 `role: 'notice'`、`content: text`（`insertImageRejectedNotice` 是插入位置的写法）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Go 测试 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" FOREBRAIN_HOME="$(mktemp -d)"; CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/run/... ./pkg/tui/... ./pkg/event/... ./pkg/gateway/... ./pkg/state/... -count=1` | 全部 `ok` |
| vet / fmt | 同上环境；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/...`；`gofmt -l pkg/` | 无输出 |
| 前端 | `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts && pnpm typecheck` | 通过 / exit 0 |
| 真机 | `.claude/skills/run-forebrain/driver.sh`（Step 10） | 见期望 |

## Scope

**In scope**：新建 `pkg/llm/stream_failure.go`(+test)、`pkg/llm/stream_retry.go`(+test)；`pkg/llm/retry.go`（抽出共用的等待计算、加重发守卫选项）、`pkg/llm/stream.go`、`pkg/llm/stream_progress.go`、`pkg/llm/error.go`(+test)、`pkg/llm/image_heal.go`(+test)；
四个适配器里“流打开后一个事件都没有”的那一处（`pkg/llm/openai/compat_llm.go`、`pkg/llm/openai/responses_llm.go`、`pkg/llm/anthropic/agent_llm.go`、`pkg/llm/google/stream.go`，各 +test）；
`pkg/run/llm_middleware.go`(+test)、`pkg/run/orchestration_llm.go`(+test)、`pkg/run/transcript_progress.go`(+test)、新建 `pkg/run/response_collector.go`(+test)、`pkg/run/subagent.go`、`pkg/event/run_events.go`、
`pkg/state/session_store.go`（只在没有现成删除方法时加一个，+test）、`pkg/gateway/stream_parts.go`(+test)、`pkg/tui/notify.go`、`pkg/tui/chat_turn.go`、`pkg/tui/reducer.go`、`pkg/tui/run.go`、`pkg/tui/commands.go`、TUI 测试、
`frontend/src/composables/useChatStream.ts`(+test)、`frontend/src/lib/forebrainGatewayRuntime.ts`、`.claude/skills/run-forebrain/fake_provider.py` 与 `SKILL.md`（Step 10 的开关）。

**Out of scope**：工具在流中途开始执行（`124`）；审批（`127`）；退避基数、次数上限、状态行文案（115–118）；限流（429）路径；`ExecuteStructured`/`Compact`；Claude Code 独有、forebrain 没有对应物的原因与结果（内容审查拒绝 `denied`、输出过滤 `outputFiltered`、备用模型切换 `useFallbackModel`、后台/持久模式）。

## Steps

### Step 1：失败原因与判定（`pkg/llm/stream_failure.go`）

1. 新哨兵（放 `error.go`）：

```go
// ErrStreamNoEvents reports a stream that closed cleanly before producing a
// single event: no output, no finish frame. It is retried without streaming.
var ErrStreamNoEvents = errors.New("stream closed before any event")
```

2. 原因分类（判断顺序即 Claude Code 的顺序）：

```go
type StreamFailureCause string

const (
	CauseOverloaded     StreamFailureCause = "overloaded"
	CauseServerError    StreamFailureCause = "server_error"
	CauseTimedOut       StreamFailureCause = "timed_out"
	CauseStalled        StreamFailureCause = "stalled"
	CauseTruncated      StreamFailureCause = "truncated"
	CauseSuspended      StreamFailureCause = "suspended"
	CauseConnectionLost StreamFailureCause = "connection_lost"
	CauseMalformed      StreamFailureCause = "malformed"
	CauseBadRequest     StreamFailureCause = "bad_request"
	CauseUnknown        StreamFailureCause = "unknown"
)

// suspendThreshold is how far the wall clock must outrun the monotonic one
// during an attempt before a dropped connection is put down to sleep.
const suspendThreshold = 5 * time.Second

func ClassifyStreamFailure(err error, snap StreamProgressSnapshot) StreamFailureCause {
	var aerr *APIError
	isAPI := errors.As(err, &aerr) && aerr != nil
	typ := ""
	if isAPI {
		typ = strings.ToLower(strings.TrimSpace(aerr.Type))
	}
	switch {
	case isAPI && (aerr.StatusCode == 529 || typ == "overloaded_error"):
		return CauseOverloaded
	case isAPI && (typ == "timeout_error" || typ == "deadline_exceeded"):
		return CauseTimedOut
	case isAPI && (aerr.StatusCode >= 500 || typ == "api_error" || typ == "server_error" || typ == "internal" || typ == "unavailable"):
		return CauseServerError
	case errors.Is(err, ErrStreamStalled):
		return CauseStalled
	case isTransportFailure(err):
		if errors.Is(err, ErrStreamTruncated) {
			return CauseTruncated
		}
		if snap.Slept >= suspendThreshold {
			return CauseSuspended
		}
		return CauseConnectionLost
	}
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
		return CauseMalformed
	}
	if isAPI {
		return CauseBadRequest
	}
	return CauseUnknown
}
```

   `isTransportFailure` 从 `IsRetryableNetworkError` 里抽出传输层那一段（`net.Error`、`io.ErrUnexpectedEOF`、`io.EOF`、`ECONNRESET`/`ECONNABORTED`、`ErrStreamTruncated`、`ErrNonStreamingTimeout`；**不含** DNS NXDOMAIN、x509 证书错误、取消/超时、`*APIError` 状态码；`*APIError` 的 `Err` 里包着传输错误时照原函数的写法递归），`IsRetryableNetworkError` 改为调用它，行为不变（现有测试必须全部照旧通过）。

3. 判定（逐行移植 Claude Code；计数按值传递，函数返回新计数）：

```go
type StreamDecision int

const (
	DecideFail StreamDecision = iota
	DecideRetry
	DecideRetryWithoutStreaming
	DecideKeepPartial
)

type streamRetryCounts struct {
	retries, sleptThrough, overloaded, stalls, truncations, afterThinkingOnly int
	triedWithoutStreaming bool
}

type streamRetryPolicy struct {
	maxRetries        int  // network_retry_max
	maxSleptThrough   int  // min(maxRetries, 10)
	maxOverloaded     int  // 3
	allowNonStreaming bool // true
}

type streamDecision struct {
	d StreamDecision
	c streamRetryCounts
}

func failWith(c streamRetryCounts) streamDecision { return streamDecision{DecideFail, c} }
func keepWith(c streamRetryCounts) streamDecision { return streamDecision{DecideKeepPartial, c} }

// retryOr spends one retry from the shared budget, or falls back.
func retryOr(c streamRetryCounts, p streamRetryPolicy, fallback streamDecision) streamDecision {
	if c.retries < p.maxRetries {
		c.retries++
		return streamDecision{DecideRetry, c}
	}
	return fallback
}

type retryCounter int

const (
	counterStalls retryCounter = iota
	counterTruncations
	counterAfterThinkingOnly
)

func (c streamRetryCounts) get(k retryCounter) int {
	switch k {
	case counterStalls:
		return c.stalls
	case counterTruncations:
		return c.truncations
	default:
		return c.afterThinkingOnly
	}
}

func (c streamRetryCounts) inc(k retryCounter) streamRetryCounts {
	switch k {
	case counterStalls:
		c.stalls++
	case counterTruncations:
		c.truncations++
	default:
		c.afterThinkingOnly++
	}
	return c
}

// limitedRetry retries while this kind of failure has happened fewer than
// limit times (and the shared budget allows), then falls back.
func limitedRetry(c streamRetryCounts, k retryCounter, limit int, p streamRetryPolicy, fallback streamDecision) streamDecision {
	if c.get(k) < limit {
		return retryOr(c.inc(k), p, fallback)
	}
	return fallback
}

func withoutStreaming(c streamRetryCounts, p streamRetryPolicy) streamDecision {
	if !p.allowNonStreaming {
		return failWith(c)
	}
	if c.triedWithoutStreaming && c.retries >= p.maxRetries {
		return failWith(c)
	}
	c.retries = min(c.retries+1, p.maxRetries)
	c.triedWithoutStreaming = true
	return streamDecision{DecideRetryWithoutStreaming, c}
}

func overloadedDecision(c streamRetryCounts, p streamRetryPolicy) streamDecision {
	c.overloaded++
	fallback := withoutStreaming(c, p)
	if c.overloaded < p.maxOverloaded {
		return retryOr(c, p, fallback)
	}
	return fallback
}

func sleptThroughRetry(c streamRetryCounts, p streamRetryPolicy, fallback streamDecision) streamDecision {
	if c.sleptThrough < p.maxSleptThrough {
		c.sleptThrough++
		return streamDecision{DecideRetry, c}
	}
	return retryOr(c, p, fallback)
}

// decideStreamFailure is Claude Code's stream-failure table.
func decideStreamFailure(cause StreamFailureCause, snap StreamProgressSnapshot, outlasted bool, c streamRetryCounts, p streamRetryPolicy) streamDecision {
	stop := snap.StopReasonReceived
	q := withoutStreaming(c, p) // the fallback, from the counts on entry
	switch snap.Kind {
	case ProgressOutput:
		if cause == CauseBadRequest || cause == CauseUnknown {
			return failWith(c)
		}
		return keepWith(c)
	case ProgressThinkingOnly:
		switch cause {
		case CauseStalled:
			if stop {
				return keepWith(c)
			}
			return limitedRetry(c, counterStalls, 1, p, keepWith(c))
		case CauseConnectionLost, CauseSuspended, CauseTruncated, CauseMalformed:
			if stop {
				return keepWith(c)
			}
			return limitedRetry(c, counterAfterThinkingOnly, 2, p, keepWith(c))
		case CauseServerError, CauseTimedOut:
			if stop {
				return failWith(c)
			}
			limit := 2
			if cause == CauseTimedOut {
				limit = 1
			}
			return limitedRetry(c, counterAfterThinkingOnly, limit, p, failWith(c))
		case CauseOverloaded:
			if stop {
				return failWith(c)
			}
			return overloadedDecision(c, p)
		default:
			return failWith(c)
		}
	}
	switch cause {
	case CauseOverloaded:
		if snap.Kind == ProgressPartialOutput {
			c.overloaded++
			return withoutStreaming(c, p)
		}
		return overloadedDecision(c, p)
	case CauseStalled:
		if snap.Kind == ProgressNothing {
			return limitedRetry(c, counterStalls, 1, p, q)
		}
		return q
	case CauseTruncated:
		if stop {
			return q
		}
		return limitedRetry(c, counterTruncations, 1, p, q)
	case CauseSuspended:
		if stop {
			return q
		}
		if snap.Kind == ProgressPartialOutput {
			return retryOr(c, p, q)
		}
		return sleptThroughRetry(c, p, q)
	case CauseConnectionLost:
		if stop {
			return q
		}
		return retryOr(c, p, q)
	case CauseServerError:
		if outlasted && !stop && snap.Kind != ProgressPartialOutput {
			return limitedRetry(c, counterAfterThinkingOnly, 2, p, q)
		}
		return q
	default: // timed out, malformed, bad request, unknown
		return q
	}
}

// completeDespiteError: the response had fully arrived before the failure.
func completeDespiteError(cause StreamFailureCause, snap StreamProgressSnapshot) bool {
	if !snap.Complete {
		return false
	}
	switch cause {
	case CauseConnectionLost, CauseSuspended, CauseTruncated, CauseStalled:
		return true
	case CauseOverloaded, CauseServerError, CauseTimedOut:
		return snap.Kind == ProgressOutput
	}
	return false
}
```

   `outlasted` = `snap.Elapsed >= NonStreamingTimeout`（126）。

4. 请求级规则：

```go
// requestLevelDecision handles a request that failed before it produced
// anything, from its HTTP status. ok is false when the error is not one;
// pass means "return the error untouched": the layers above own it.
func requestLevelDecision(err error, snap StreamProgressSnapshot, c streamRetryCounts, p streamRetryPolicy) (dec streamDecision, pass, ok bool)
```
   只在 `snap.Kind == ProgressNothing` 且（`statusOf(err) >= 400`，或错误是 DNS NXDOMAIN / x509 证书错误）时 `ok`：529 → `pass`；408、409、≥500 → `retryOr(c, p, failWith(c))`；404 且 `!c.triedWithoutStreaming` → `withoutStreaming(c, p)`；DNS/证书错误与其他状态 → `pass`。

5. 收尾错误（`error.go`）：

```go
// PartialResponseError says a request failed after it had produced something
// that must not be produced twice. The caller finishes from what completed.
type PartialResponseError struct {
	Err      error
	Progress StreamProgressSnapshot
	Cause    StreamFailureCause
	// Complete: the response had fully arrived; finish as a success, no notice.
	Complete bool
	// Failed: Claude Code's "fail" after output. The completed part stays; the
	// notice (or the turn's error) is Err itself, not an incomplete sentence.
	Failed bool
}

func (e *PartialResponseError) Error() string { return e.Err.Error() }
func (e *PartialResponseError) Unwrap() error { return e.Err }

func IsPartialResponse(err error) bool { var p *PartialResponseError; return errors.As(err, &p) }

// IncompleteSentence is what the user reads about a response that was cut.
func IncompleteSentence(cause StreamFailureCause, shown bool) string
```
   `IncompleteSentence` 按 Why 里的表：`shown` 为真时按 卡死 / 服务器类（`CauseServerError`、`CauseOverloaded`、`CauseTimedOut`）/ 格式损坏 / 睡眠 / 其他；为假时按 卡死 / 格式损坏 / 睡眠 / 其他（服务器类落到“其他”，与 Claude Code 相同）。
   `shown` 由调用方算：`Progress.Kind == ProgressOutput || Progress.Kind == ProgressPartialOutput || Progress.ToolDispatched`。

6. `stream_progress.go` 加 `func ResendSafe(ctx context.Context) bool`（无记录为 true，否则 `Snapshot().ResendSafe()`）与 `func ResetStreamProgress(ctx context.Context)`。

测试 `stream_failure_test.go`：
- `ClassifyStreamFailure`：每个原因至少一例（529 状态、`overloaded_error` 类型、`timeout_error`、503、`api_error` 类型且状态 0、`StreamStallError`、`net.OpError`、`ErrStreamTruncated`、`Slept = 6s` 的连接错误、`*json.SyntaxError`、400 的 `APIError`、`errors.New("x")`）。
- `decideStreamFailure`：表驱动，**逐个分支**覆盖上面的代码（包括“共用预算用完时的回退”“`triedWithoutStreaming` 且预算用完 → 报错”“`allowNonStreaming=false` → 报错”）。
- `completeDespiteError`、`requestLevelDecision`、`IncompleteSentence` 每个文案各一例。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ -run 'StreamFailure|Retryable|Classify|Decide|Incomplete' -count=1` → `ok`；`./pkg/llm/` 全部现有测试 `ok`

### Step 2：适配器报 `ErrStreamNoEvents`

流正常关闭、**一个产出事件都没有**（没有思考、正文、工具调用）且**没有结束帧**时，返回 `fmt.Errorf("<adapter>: %w", llm.ErrStreamNoEvents)`：
- compat `executeChatCompletionStream`：现在 `lastFinish == ""` 且三者都为空时落到“空回答”路径；改为返回该错误（有输出无结束帧仍是 `ErrStreamTruncated`）。
- Anthropic：循环结束时没有收到任何 `content_block_start`，也没有 `message_delta` 的 stop_reason。
- Responses `executeStreaming`：没有任何输出项，也没有 `response.completed`。
- Google `executeStreaming`（125）：`finish == ""` 且没有 part（原来报 `response was empty (finish reason "")` 的那条路径）。
有结束帧但零输出的“空回答”保持原样（交给空输出层）。各适配器加一例测试。

### Step 3：按进度重试层（`pkg/llm/stream_retry.go`）

1. 把 `retryLLM.Execute` 里“由 `backoff` 与错误算出这次等待（含上浮抖动、Retry-After、超过上限放弃）”抽成 `func (c *retryConfig) waitFor(backoff time.Duration, err error) (sleep time.Duration, giveUp bool)`，`retryLLM` 改用它（行为不变）。
2. 新增：

```go
// NewStreamRetry is the network retry for streamed model requests. Each
// failure is classified and decided from the request's StreamProgress the way
// Claude Code decides it: re-send as a stream, re-send without streaming,
// finish from what completed, or fail.
func NewStreamRetry(maxRetries int, opts ...RetryOption) LLMMiddleware
```

   `Execute` 的骨架（`clearPending` 与 sink 包装照 `retryLLM` 原样保留）：

```text
counts := streamRetryCounts{}; policy := {maxRetries, min(maxRetries, 10), 3, true}
nonStreaming := false; backoff := initialBackoff
loop:
  if ctx.Err() → return ctx.Err()
  attemptCtx := ctx；nonStreaming 时 attemptCtx = WithNonStreamingRequest(ctx)
  result, err := inner.Execute(attemptCtx, …)；err == nil → return result
  ctx.Err() != nil 或 errors.Is(err, context.Canceled) → return err          // 取消永远原样
  IsPartialResponse(err) → return err                                          // 内层已收尾
  snap := 进度快照（无记录视为 Nothing）
  if dec, pass, ok := requestLevelDecision(err, snap, counts, policy); ok {
      pass → return err
      apply(dec)
  } else {
      cause := ClassifyStreamFailure(err, snap)
      completeDespiteError(cause, snap) → finish(&PartialResponseError{Err: err, Progress: snap, Cause: cause, Complete: true})
      apply(decideStreamFailure(cause, snap, snap.Elapsed >= NonStreamingTimeout, counts, policy))
  }
apply(dec):
  counts = dec.c
  DecideFail：snap.Kind == ProgressOutput 或 snap.ToolDispatched → finish(&PartialResponseError{…, Failed: true})
              否则 → 返回用尽/原错误（见 5）
  DecideKeepPartial → finish(&PartialResponseError{Err: err, Progress: snap, Cause: cause})
  DecideRetryWithoutStreaming → withdraw(snap)；原因为 Truncated 或 errors.Is(err, ErrStreamNoEvents) 时 warnOnce()；
                nonStreaming = true；ResetStreamProgress(ctx)；不等待，继续循环
  DecideRetry：snap.Kind == ProgressThinkingOnly（只有思考的重发）→ withdraw(snap)；连接类原因等待 100ms × counts.afterThinkingOnly，卡死不等待，服务器类照 waitFor；不显示状态行
               原因为 CauseStalled（看门狗重发，任何进度）→ withdraw(snap)；不等待；不显示状态行
               其余 → sleep, giveUp := waitFor(backoff, err)；giveUp → return err
                      notify(TransportRetry{Retry: counts.retries, Total: maxRetries, Wait: sleep, Err: err, Status: statusOf(err)})，pendingRetry 记下（115 的状态行）
               等待（ctx 取消即返回 ctx.Err()）；ResetStreamProgress(ctx)；backoff 翻倍封顶；继续循环
finish(p)：clearPending()；若没有待清的重试 → notify(TransportRetry{Total: maxRetries, Cleared: true})（撤下进行中的块、收起状态行）；return nil, p
withdraw(snap)：clearPending()；notify(TransportRetry{Total: maxRetries, Cleared: true, DropFinishedThinking: snap.Kind == ProgressThinkingOnly})
```

3. `TransportRetry`（`stream.go`）加字段：

```go
	// DropFinishedThinking: also withdraw the thinking blocks the dead attempt
	// finished. A resend only ever follows an attempt whose finished blocks
	// are all thinking, and Claude Code withdraws them with the unfinished one.
	DropFinishedThinking bool
```
   普通重发的非 Cleared 通知也要撤下这次尝试已写完的思考：`notify` 前若 `snap.Kind == ProgressThinkingOnly` 就已走了上面 thinking-only 分支，所以普通重发的 `DropFinishedThinking` 恒为假（这次尝试没有写完的块）。
4. 用尽：`DecideFail` 且进度不是 Output 时，`counts.retries > 0` → `clearPending()` 后返回 `&MaxAttemptsExceededError{…}`（字段照 `retryLLM` 的用法，`networkRetryLLM.Execute` 会改写成用户读到的句子），否则返回原错误。
5. `warnOnce()`：包级 `var streamingFallbackWarned atomic.Bool`，`CompareAndSwap(false, true)` 成功时调 `sink.OnResponseNotice(ResponseNotice{Kind: ResponseNoticeStreamingFallback, Text: StreamingFallbackWarning})`（Step 7；sink 或回调为 nil 时跳过，且不占用这次机会：先判断回调非 nil 再 CAS）。
6. `pkg/run/llm_middleware.go` `wrapNetworkRetryLLM`：`llm.NewRetry(1+maxRetries, llm.WithShouldRetry(llm.IsRetryableNetworkError), …)` 换成 `llm.NewStreamRetry(maxRetries, llm.WithMaxBackoff(32*time.Second), llm.WithUpwardJitter(), llm.WithRetryAfter(llm.RetryAfterOf, 5*time.Minute), llm.WithRetryNotify(…原样…))`；`networkRetryLLM.Execute` 对 `PartialResponseError` 原样返回。函数注释按新语义改写。
   `summaryChain` 等嵌套用法不用改：嵌套请求的 sink 已静音、进度记录已摘掉（122），判定按 Nothing 进行。

测试 `stream_retry_test.go`（stub inner 在 `Execute` 里按脚本调 sink 回调模拟进度，ctx 带 `WithStreamProgress`）：
- 思考 + 正文 delta 后连接断 → inner 1 次，返回 `PartialResponseError`（非 Complete、非 Failed），恰好一条 Cleared 通知、无 `DropFinishedThinking`。
- 只有正文 delta 后断 → 重发（一条非 Cleared 通知），第 2 次成功。
- 思考 + `OnReasoningDone` 后断，三次 → 共 3 次调用后 `PartialResponseError`；前两次重发前各一条 `Cleared` + `DropFinishedThinking` 通知；没有非 Cleared 通知。
- 截断两次 → 第 2 次后改用非流式：第 3 次调用的 ctx 满足 `NonStreamingRequestFrom`，并收到一次 `streaming_fallback` 提示；同进程再跑同场景不再提示。
- Nothing 时 `ErrStreamNoEvents` → 第 2 次即非流式。
- 503 的 `APIError` 且 Nothing → 重发；404 且 Nothing → 非流式；400 且 Nothing → 原样返回；529 且 Nothing → 原样返回。
- `Output` 后 400 → `PartialResponseError{Failed: true}`。
- `Complete` 后连接断 → `PartialResponseError{Complete: true}`，没有提示。
- ctx 已取消 → 原样返回取消（即使 `ToolDispatched`）。
- 每次重发前进度被重置（第二次尝试的快照从 Nothing 开始）。
- 连接断开每次都在 Nothing → 共 `maxRetries+1` 次流式 + 1 次非流式后返回用尽错误。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ -count=1` → `ok`

### Step 4：其他重发层的守卫

1. `image_heal.go`：`IsImageRejection(err)` 分支开头加 `if IsPartialResponse(err) || !ResendSafe(ctx) { return result, err }`，重发前 `ResetStreamProgress(ctx)`。
2. `llm_middleware.go` `recoverableLLM.Execute`：`res, err := r.inner.Execute(...)` 失败后、`errors.Is(err, context.Canceled)` 判断旁加 `if llm.IsPartialResponse(err) { return nil, err }`；
   `forceCompact` 之后的重发与 529 循环里的重发，各自在调用 `r.inner.Execute` 前加 `if !llm.ResendSafe(ctx) { return nil, lastErr }` 与 `llm.ResetStreamProgress(ctx)`；529 分支另加条件：只在进度为 Nothing 时处理（`llm.StreamProgressFrom(ctx)` 为 nil 视为 Nothing），否则原样返回。
3. 空输出层：`retry.go` 加选项 `WithResendGuard()`——`retryLLM` 在 `shouldRetry` 判断之前查 `ResendSafe(ctx)`，为假直接返回错误；每次重发前 `ResetStreamProgress(ctx)`。`wrapBlankOutputRetryLLM` 加上该选项。
4. 测试：各层一例——进度为 `Output` 时不重发、原错误返回；`PartialResponseError` 不被 529/压缩/图片层再处理；Started 进度下的 529 原样返回。

### Step 5：收集已写完的部分（`pkg/run/response_collector.go`）

```go
// responseCollector keeps what one model request has completed so far: the
// finished answer-text blocks and the tool calls whose arguments are complete.
// It is what a request that failed mid-stream is finished from.
type responseCollector struct {
	mu        sync.Mutex
	current   strings.Builder // the text block being streamed
	completed []string        // finished text blocks, in order
	ready     []llm.ToolCall  // finished tool calls, in order
}

func withResponseCollector(ctx context.Context) (context.Context, *responseCollector)
func (c *responseCollector) result() *llm.Result // AssistantMessage(one Text part per completed block, ToolCalls: ready)
func (c *responseCollector) sealCurrent()        // a fully-arrived response's last block counts as finished
```

`withResponseCollector` 链式包装 sink（只包装原本非 nil 的 `OnDelta`；`OnTextBlockDone`、`OnToolCallReady`、`OnTransportRetry` 原本为 nil 时装只记录的版本）：
`OnDelta` → `current` 追加；`OnTextBlockDone` → `current` 非空白时移入 `completed` 并清空；`OnToolCallReady` → 先把非空 `current` 移入 `completed`，再追加调用；
`OnTransportRetry` → 非 Cleared（即将重发）或带 `DropFinishedThinking` 时清空全部；仅 Cleared 时只清空 `current`。测试覆盖这些回调、`sealCurrent` 与 `result()`。

### Step 6：编排层

在 `toolOrchestrationLLM.Execute` 主循环里：
1. `callCtx = withSteerDeliveryResponseStart(...)` 之后加：`callCtx, _ = llm.WithStreamProgress(callCtx)` 与 `callCtx, collector := withResponseCollector(callCtx)`；并在每次模型调用前 `transcript.beginAttempt()`（Step 8.4）。
2. `res, err := w.inner.Execute(callCtx, session, tools)` 之后、`abandonCall(nil)` 之后，**在 `inFlightSteers.Retracted()` 判断之前**：

```go
		var incomplete *llm.PartialResponseError
		if errors.As(err, &incomplete) {
			// What reached the user is the answer that happened: finish the
			// turn from it instead of asking the model the same thing again.
			if incomplete.Complete {
				collector.sealCurrent()
			}
			res, err = collector.result(), nil
			inFlightSteers.Commit()
			if incomplete.Complete {
				incomplete = nil // a response that fully arrived is an ordinary one
			}
		}
```

3. 无工具分支（`len(res.Message.ToolCalls) == 0`）开头：`if incomplete != nil {`——正文非空白时 `session = append(session, *res.Message)`、`noteResponseCompleted()`、`partialCapture.Set(session)`、`transcript.flush(...)`；
   然后 `incomplete.Failed` → `return nil, incomplete.Err`（现有错误显示）；否则 `return nil, &responseIncompleteError{sentence: llm.IncompleteSentence(incomplete.Cause, shown), err: incomplete.Err}`（不走插话、不走 `endTurn`、不触发“empty assistant response after tool execution”）。
4. 有工具分支：`incomplete != nil` 时，在执行工具前发提示：`text := llm.IncompleteSentence(...)`，`incomplete.Failed` 时 `text = incomplete.Err.Error()`；`if sink := llm.StreamSinkFrom(ctx); sink != nil && sink.OnResponseNotice != nil { sink.OnResponseNotice(llm.ResponseNotice{Kind: llm.ResponseNoticeIncomplete, Text: text}) }`；其余照常（执行工具、继续下一次请求）。
5. 新增（同文件或 `response_collector.go`）：

```go
type responseIncompleteError struct{ sentence string; err error }

func (e *responseIncompleteError) Error() string { return e.sentence }
func (e *responseIncompleteError) Unwrap() error { return e.err }
```

### Step 7：提示事件与两端显示

1. `stream.go`：

```go
type ResponseNoticeKind string

const (
	ResponseNoticeIncomplete        ResponseNoticeKind = "incomplete"
	ResponseNoticeStreamingFallback ResponseNoticeKind = "streaming_fallback"
)

const StreamingFallbackWarning = "Streaming response ended before any complete data was received. Retrying without streaming. If this keeps happening, check any proxy or gateway between forebrain and your model provider."

// ResponseNotice is a line about the response itself that belongs in the
// conversation.
type ResponseNotice struct {
	Kind ResponseNoticeKind
	Text string
}
```
   `StreamSink` 加 `OnResponseNotice func(ResponseNotice)`，`WithMutedForegroundStream` 置 nil。
2. `run_events.go`：`RunEventResponseNotice = "response_notice"`、`type ResponseNoticePayload struct { Kind string \`json:"kind"\`; Text string \`json:"text"\`; AgentID string \`json:"agent_id,omitempty"\` }`（**不**加入 push-only，需落库）；
   `TransportRetryPayload` 加 `DropFinishedThinking bool \`json:"drop_finished_thinking,omitempty"\``。
3. `subagent.go` `EventStreamSink`：`OnResponseNotice` 发布该事件（带 `AgentID`）；`OnTransportRetry` 的载荷带上 `DropFinishedThinking`。
4. TUI：`notify.go` 加 `ResponseNoticeMsg{turn *foregroundTurn; AgentID, Kind, Text string}` 与事件分支；`chat_turn.go` 前台 sink 的 `OnResponseNotice` 发 `ResponseNoticeMsg` **并**像 `recordDeliveredSteers` 那样 `s.persistRunEvent(...)` 落一条事件；
   `reducer.go` `case ResponseNoticeMsg:`：`incomplete` → `r.withFlushedBuffers(r.agentBuf(agentID), Frame{Kind: FrameError, Title: "Response incomplete", Content: m.Text, AgentID: agentID, Final: true})`；`streaming_fallback` → 同样但 `Kind: FrameSystem`、无 Title；
   `commands.go` 重放：照 `replayImageRejectedMessage` 加 `replayResponseNoticeMessage`，产出同一帧。
5. web：事件联合加 `'response_notice'`；主对话照 `image_rejected` 的处理位置加分支：`incomplete` → 向当前 assistant 消息追加 `{ kind: 'error', id: evt.id, text }`；`streaming_fallback` → 照 `insertImageRejectedNotice` 的插入位置插一条 `{ id: 'response-notice:' + evt.id, role: 'notice', content: text }`（同 id 只插一次）；
   子代理分支：`incomplete` 向该子代理 blocks 追加 error 块，`streaming_fallback` 忽略（只在主对话出现）；历史回放走同一分支。（两者都复用现有块与提示消息，不新增界面元素。）
6. `stream_parts.go`：`case event.RunEventTransportRetry:` 里去掉 `|| p.Cleared`，Cleared 时同样清该 agent 的 content/reasoning；改测试 `TestStreamPartsTransportRetryDropsTheDeadAttempt` 的 Cleared 断言为“也被清空”。

### Step 8：重发时撤下已写完的思考

1. TUI `reducer.go`：`WithdrawDeadStreamBlocks(agentID string)` 改为 `WithdrawDeadStreamBlocks(agentID string, finishedThinking bool)`：`finishedThinking` 为真时 Final 的 `FrameThinking` 也弹出（遇到其他类型的帧仍停）；所有调用点更新（`run.go` 传 `m.DropFinishedThinking`，其余传 false）。`TransportRetryMsg` 定义处加字段，前台 sink 与事件分支都填。
2. web：新增 `dropDeadAttemptBlocks(blocks, finishedThinking: boolean)`：先滤掉 `open` 的 assistant/thinking；`finishedThinking` 为真时再从末尾连续弹出 `kind === 'thinking'` 的块，遇到其他块停。主对话与子代理的 `transport_retry` 分支改用它（读 `payload.dropFinishedThinking`）。
3. gateway：`stream_parts.go` 每条通知都清 reasoning（Step 7.6 之后已满足）；`run_control.go` 的 `partial.Reset()` 不变。
4. 落库：`transcriptProgress` 加 `attemptRows []int64`（受 `mu` 保护）；`reasoningDone` 把 `AppendStructuredMessageForRun` 返回的行 ID 记入；加 `beginAttempt()`（清空 `attemptRows`，Step 6.1 每次模型调用前调用）与 `dropFinishedReasoning(ctx)`（逐个删除 `attemptRows` 里的行后清空）。
   删除用会话存储里已有的“按行 ID 删除本运行消息”方法（先 `grep -n "func (s \*SessionStore) Delete" pkg/state/session_store.go` 找）；没有就新增 `func (s *SessionStore) DeleteRunMessageRow(ctx context.Context, sessionID, runID string, rowID int64) error`（`WHERE id=? AND session_id=? AND run_id=?`），并把它加进 `transcriptProgressStore` 接口。
   `recordReasoningBlocks` 的 `OnTransportRetry`：先 `dropInFlightReasoning()`；`tr.DropFinishedThinking` 为真时再 `dropFinishedReasoning(sinkCtx)`；收尾用的 Cleared（无该标志）不删。
   `dropInFlightReasoning` 的注释（“Blocks that already ended were written when they ended and stay”）改为说明：收尾时它们保留，重发时由 `dropFinishedReasoning` 撤下。
5. 测试：TUI 一例（思考块 Final 后收到带 `DropFinishedThinking` 的通知 → 该块消失；不带 → 保留）；web 一例（同上）；`transcript_progress` 一例（写完一个思考块 → 带标志的通知 → 库里该 `reasoning` 行被删；`beginAttempt` 之后的通知不删上一次尝试的行）。

### Step 9：编排层测试（`orchestration_llm_test.go`）

写法照同文件 `TestTransportRetryDropsBufferedReasoningFromTheTranscript`（`newProgressStore`、`wrapToolOrchestrationLLMWithProgress`、`wrapNetworkRetryLLM`）。新增：
1. `TestStreamFailureAfterThinkingAndTextEndsTurnWithoutResend`：inner 第 1 次发思考 delta、正文 delta 后返回 `&net.OpError{…}` → inner 只被调用 1 次；错误文本 `Connection lost mid-response. The response above may be incomplete.`；transcript 里没有那段正文。
2. `TestStreamFailureWithOnlyTextIsResent`：只发正文 delta 后断 → 重发，第 2 次成功。
3. `TestStreamFailureAfterCompletedToolCallRunsItAndContinues`：第 1 次发正文、`OnTextBlockDone`、`OnToolCallReady(call)` 后断 → 不重发；该工具执行一次；第 2 次模型调用收到 assistant（正文 + 该调用）与工具结果；sink 收到一次 `incomplete` 提示。
4. `TestThinkingOnlyFailureRetriesTwiceWithdrawingThinkingThenEnds`：每次只发思考 + `OnReasoningDone` 后断 → 共 3 次调用；库里只剩最后一次尝试的那一行 `reasoning`；以 `Connection lost before a response was produced. Try again.` 结束。
5. `TestStalledStreamUsesTheStallSentence`：失败错误 `&llm.StreamStallError{After: time.Second}`、已有输出 → `The response stopped arriving…`。
6. `TestServerErrorAfterOutputKeepsPartial`：输出后 `&llm.APIError{StatusCode: 0, Type: "api_error"}` → `Server error mid-response. The response above may be incomplete.`。
7. `TestBadRequestAfterOutputFailsButKeepsTheText`：输出（正文块写完）后 400 → 返回原错误；正文已落库。
8. `TestTruncatedTwiceFallsBackToNonStreaming`：两次截断 → 第 3 次调用为非流式（inner 断言 ctx），用 `llm.ReplayToSink` 回放一个回答 → 回合成功；对话里有一次 `streaming_fallback` 提示。

TUI/web 测试：reducer 收 `ResponseNoticeMsg`（两种 Kind）各产出对应帧；重放函数同帧；web 单测 `response_notice` 两种 Kind 的显示，历史回放同样出现。

**Verify**：全部 Go 测试 `ok`；前端单测与 `pnpm typecheck` 通过；`go vet`、`gofmt` 干净

### Step 10：真机验收（TUI，run-forebrain）

1. `fake_provider.py` 的 `netdrop` 模式加开关（写法照现有 `FAKE_NETDROP_EVERY`/`FAKE_NETDROP_SILENT`）：
   - `FAKE_NETDROP_THINKING=1`：先发若干 `reasoning_content` delta，再发 `content` delta `partial answer that never`，然后无结束帧关流；
   - `FAKE_NETDROP_THINKING_ONLY=1`：每个流式请求只发若干 `reasoning_content` delta（内容带请求序号，如 `thinking #N`），再发一个 `content` 为空、无结束帧的块后关流；非流式请求（`"stream": false`）正常回答 `after fallback`；
   - `FAKE_NETDROP_TOOL=1`：先发 `content` delta，再发一个完整的 `shell` 工具调用（index 0，命令 `echo kept`），再发 index 1 的首个分片后关流；之后的请求正常回答 `after tool`；
   - `FAKE_NETDROP_EMPTY=1`：流式请求回 200 + `text/event-stream` 后立即关流（零事件）；非流式请求回答 `after fallback`。
   `SKILL.md` 的 `netdrop` 行补这些开关的说明。
2. 跑：

```bash
D=.claude/skills/run-forebrain/driver.sh
FAKE_NETDROP_THINKING=1 $D start netdrop
$D submit 'hello'; $D wait 'may be incomplete' 20
$D screen    # 期望：思考块在；没有 "partial answer that never"；错误 "Connection lost mid-response. The response above may be incomplete."；状态行无重试
$D stop      # 期望 provider stderr 只有 1 条 REQUEST
FAKE_NETDROP_THINKING_ONLY=1 $D start netdrop
$D submit 'hello'; $D wait 'before a response' 40
$D screen    # 期望：屏上只有一段思考（"thinking #3"）；错误 "Connection lost before a response was produced. Try again."
$D provider-log  # 期望：3 条流式 REQUEST
$D messages  # 期望：只有一行 reasoning（thinking #3）
$D stop
FAKE_NETDROP_TOOL=1 $D start netdrop
$D submit 'hello'; $D wait 'after tool' 30
$D screen    # 期望：一张 "echo kept" 工具卡（只执行一次）、一行 "Response incomplete"、随后 "after tool"
$D messages  # 期望：assistant 行带 1 个 tool_call、对应 tool 结果行、最后 "after tool"
$D stop
FAKE_NETDROP_EMPTY=1 $D start netdrop
$D submit 'hello'; $D wait 'after fallback' 30
$D screen    # 期望：一行 "Streaming response ended before any complete data was received. Retrying without streaming…"，随后 "after fallback"
$D provider-log  # 期望：1 条 stream=True、1 条 stream=False
$D stop
$D start netdrop                 # 回归：只有正文的断流仍自动重试并成功
$D submit 'hello'; $D wait 'Worked for' 30
$D stop
```

**Verify**：各段期望全部满足，`screen`/`provider-log` 输出附在报告里。

## Done criteria

- [ ] Step 1–9 的测试全部通过；`pkg/llm`、`pkg/run`、`pkg/tui`、`pkg/gateway`、`pkg/event`、`pkg/state` 测试 `ok`
- [ ] `go vet`、`gofmt -l pkg/` 干净；前端单测与 `pnpm typecheck` 通过
- [ ] `grep -n "WithShouldRetry(llm.IsRetryableNetworkError)" pkg/run/llm_middleware.go` 无结果；`grep -n "NewStreamRetry" pkg/run/llm_middleware.go` 有结果
- [ ] 真机各段符合期望
- [ ] `git status --short` 只含 Scope 内文件

## STOP conditions

1. 前置不满足或锚点找不到。
2. 现有网络重试测试里，有用例断言“已有正文 + 思考后仍重发”或“重发后旧的已写完思考仍在”——不要改成新行为去迎合，先回报用例名（owner 确认后再改断言）。
3. `recoverableLLM` 的 529 或压缩重发路径里，发现有请求在流中途才报错的现有测试——回报，不要放宽 `ResendSafe`。
4. compat 的现有测试断言“零输出、无结束帧 → 空回答”（Step 2 会改变它）——回报用例名。
5. 会话存储里删除消息行会触发 FTS/触发器或级联删除其他行——回报，不要绕过。
6. TUI 的 `FrameError` 在子代理视图里被错误镜像导致提示出现两次——回报，不要另造新的帧类型。
7. 某一步验证连续两次修正后仍失败。

## Maintenance notes

- 判定只读进度与错误类型，不读错误文本；新增流式信号时更新 `stream_progress.go` 的口径。`decideStreamFailure` 与 Claude Code 保持逐行一致，改它要附 Claude Code 对应版本的依据。
- 撤下的唯一信号是 `TransportRetry`：任何新的“进行中内容 / 已写完思考”的持有者都要在每条通知上按 `Cleared`/`DropFinishedThinking` 清空自己。
- `124` 会在开始执行工具时调用 `MarkToolDispatched`；工具调用写完必然是 `Output`，所以从此这次请求只会收尾或报错，不会重发。
- 非流式重发的结果经 `llm.ReplayToSink`（126）回放，下游无需区分流式与非流式。
