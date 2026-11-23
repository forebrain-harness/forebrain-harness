# Plan 126：非流式请求能力——四个适配器都能按上下文开关改发非流式请求，带单次超时，并把整段结果按流式信号回放给 sink

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在 dev 工作区。
> 工作区里若有与本计划无关的未提交改动，**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）**：`plans/122`、`plans/125` 已完成：`grep -n "OnMessageStop func" pkg/llm/stream.go`、`grep -n "func anthropicToolCallFromBlock" pkg/llm/anthropic/agent_llm.go`、
> `grep -n "func compatToolCallFromOpenAI" pkg/llm/openai/compat_llm.go`、`grep -n "func (c \*agentLLM) executeUnary" pkg/llm/google/agent_llm.go`、`grep -n "func (c \*agentLLM) executeStreaming" pkg/llm/google/stream.go` 各有结果。
>
> **Drift check**：按下面摘录里的符号定位；锚点找不到或内容不符即 STOP 1。

## Status

- **Priority**: P1（`123` 的“改用非流式重发”依赖它）
- **Effort**: M
- **Risk**: LOW（只加能力：没有任何调用方设置开关之前，生产行为不变）
- **Depends on**: plans/122、plans/125
- **Category**: direction（owner 2026-10-25：断流后的处理与 Claude Code 一致，包括改用非流式请求重发）
- **Planned at**: dev `4d7a3c9` + 122 + 125，2026-10-25

## Why this matters

`plans/123` 的断流判定里有一种结果是“这次请求改用非流式重发”：流式重发次数用完、流卡在已开始/部分输出、流里报服务器错误、流格式损坏、流式端点 404、流打开后一个事件都没有等情况，都要换成一次（可重复的）非流式请求。
这需要每个适配器都能在同一次 `Execute` 里按上下文开关改发非流式请求，并且：

1. 非流式请求有单次超时（5 分钟），超时是可重试的网络类错误——非流式请求没有空闲看门狗，不加超时会无限挂起。
2. 拿到整段结果后，按流式信号的顺序**回放**给 sink（思考 → 正文 → 工具调用 → 结束原因 → 结束帧），这样两端照常显示、进度记录照常更新、`plans/124` 的执行器照常收到 `OnToolCallReady`——与 Claude Code 把非流式结果当作一条完整消息交给同一套下游一致。

本计划只提供能力，不设置开关；判定与重试在 `123`。

## Current state

- `pkg/llm/stream.go` `type StreamSink struct`（122 之后）有 `OnDelta`、`OnReasoningDelta`、`OnReasoningDone`、`OnResponseStarted`、`OnToolCallReady`、`OnTextBlockDone`、`OnStopReason`、`OnMessageStop`、`OnUsage`、`OnUsageSnapshot`、`OnEnd`、`Streamed` 等；`func NotifyResponseStarted(sink *StreamSink, started *bool)`。
- `pkg/llm/stream_watchdog.go`：`func WithStreamingRequest(ctx) context.Context` 只给流式请求打标记，看门狗只看带标记的请求。
- `pkg/llm/error.go` `func IsRetryableNetworkError(err error) bool`：先 `errors.Is(err, ErrStreamStalled)` → true，再 `errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)` → false。
- **compat**（`pkg/llm/openai/compat_llm.go`）：`Execute` 里 `if req.Stream { … executeChatCompletionStream … }`，否则 `executeChatCompletionUnary(ctx, req)`（循环 `c.client.CreateChatCompletion`，累积 `reasoningAcc`/`contentAcc`/`toolBuf`/`lastFinish`，最后 `compatMessageFromOpenAI(openMsg)`）。
  `normalizeOpenAIStreamUsage` 保证 `stream_options` 与 `stream` 一致。
- **Anthropic**（`pkg/llm/anthropic/agent_llm.go`）：`Execute` 只有流式：`ctx = llm.WithStreamingRequest(ctx)`；`stream := c.client.Messages.NewStreaming(ctx, params)`；转换 `agentAnthropicMessageFromAPI(m *anthropicapi.Message)`（text / tool_use；122 抽出了 `anthropicToolCallFromBlock`）。
- **Responses**（`pkg/llm/openai/responses_llm.go`）：`Execute` 里 `if c.stream { executeStreaming } else { executeSync }`；`executeSync` 调 `c.client.Responses.New(ctx, params)`，从 `resp.Output` 取正文、`reasoningItems`（`llm.SummaryText(reasoningItems)` 为思考摘要文本）、`toolCalls`。
- **Google**（125 之后）：`Execute` 调 `executeStreaming(ctx, raw)`；`executeUnary(ctx, raw)` 是 `:generateContent` 的非流式实现，暂无调用者。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Go 测试 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" FOREBRAIN_HOME="$(mktemp -d)"; CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... -count=1` | 全部 `ok` |
| vet / fmt | 同上环境；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/llm/...`；`gofmt -l pkg/` | 无输出 |

## Scope

**In scope**：新建 `pkg/llm/nonstreaming.go` 与 `pkg/llm/nonstreaming_test.go`、`pkg/llm/error.go`(+test)、`pkg/llm/openai/compat_llm.go`(+test)、`pkg/llm/openai/responses_llm.go`(+test)、`pkg/llm/anthropic/agent_llm.go`(+test)、`pkg/llm/google/agent_llm.go`、`pkg/llm/google/stream.go`、`pkg/llm/google/*_test.go`。

**Out of scope**：谁在什么时候打开开关（`123`）；重试层、编排层、两端界面；`ExecuteStructured`、`Compact` 等其他入口（保持原样）。

## Steps

### Step 1：`pkg/llm/nonstreaming.go`

```go
// NonStreamingTimeout bounds one non-streaming request. A non-streaming
// request has no idle watchdog, so without a bound a stuck one never returns.
var NonStreamingTimeout = 5 * time.Minute

// ErrNonStreamingTimeout reports a non-streaming request that ran out of
// NonStreamingTimeout. It is retryable like any dropped connection.
var ErrNonStreamingTimeout = errors.New("non-streaming request timed out")

// WithNonStreamingRequest asks the adapter serving a request made under ctx to
// send it as one non-streaming call instead of a stream.
func WithNonStreamingRequest(ctx context.Context) context.Context
func NonStreamingRequestFrom(ctx context.Context) bool

// WithNonStreamingDeadline applies NonStreamingTimeout to one non-streaming
// attempt; NonStreamingError turns that deadline into ErrNonStreamingTimeout.
func WithNonStreamingDeadline(ctx context.Context) (context.Context, context.CancelFunc) // context.WithTimeoutCause(ctx, NonStreamingTimeout, ErrNonStreamingTimeout)
func NonStreamingError(ctx context.Context, err error) error                              // err != nil && errors.Is(context.Cause(ctx), ErrNonStreamingTimeout) → fmt.Errorf("%w: %v", ErrNonStreamingTimeout, err)；否则原样

// ReplayedResponse is a whole response, in the order a stream would have
// delivered it.
type ReplayedResponse struct {
	Reasoning  string     // the model's visible thinking text, "" when none
	Text       []string   // answer-text blocks, in order
	ToolCalls  []ToolCall // identical to the Result's calls
	StopReason string     // the provider's finish reason, "" when unknown
}

// ReplayToSink delivers a non-streaming response through the stream sink so
// every consumer sees it exactly as it would a streamed one.
func ReplayToSink(sink *StreamSink, r ReplayedResponse)
```

`ReplayToSink` 的顺序（每个回调为 nil 时跳过；sink 为 nil 直接返回）：
1. 有任何内容（思考、正文、调用之一非空）时 `NotifyResponseStarted(sink, &started)`（局部 `started := false`）；`Streamed != nil` 时置 true。
2. `Reasoning` 非空白 → `OnReasoningDelta(Reasoning)`、`OnReasoningDone()`。
3. 每个非空白正文块 → `OnDelta(text)`、`OnTextBlockDone()`。
4. 每个调用 → `OnToolCallReady(call)`。
5. `OnStopReason(StopReason 为空时用 "stop")`、`OnMessageStop()`。
（用量不在这里报，见 Step 3 各适配器。）

测试 `nonstreaming_test.go`：回放顺序（用记录回调的 sink 断言整个序列）、nil sink 安全、`WithNonStreamingDeadline` + 超时后 `NonStreamingError` 包成 `ErrNonStreamingTimeout`（测试里把 `NonStreamingTimeout` 临时改成 10ms，`t.Cleanup` 恢复）、未超时的错误原样返回。

### Step 2：超时是可重试错误

`error.go` `IsRetryableNetworkError`：在 `errors.Is(err, ErrStreamStalled)` 判断旁加 `if errors.Is(err, ErrNonStreamingTimeout) { return true }`（必须在 `context.Canceled`/`DeadlineExceeded` 判断之前）。函数注释的列举里补上它。测试加一例。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ -run 'NonStreaming|Retryable' -count=1` → `ok`

### Step 3：四个适配器

每个适配器在 `llm.NonStreamingRequestFrom(ctx)` 为真时：`ctx, cancel := llm.WithNonStreamingDeadline(ctx)`（`defer cancel()`）→ 发非流式请求（**不要**调用 `llm.WithStreamingRequest`）→ 错误经 `llm.NonStreamingError(ctx, err)` 返回 → 成功时报用量（`OnUsage(in, out)` 一次 + `OnUsageSnapshot(in, out)`，有用量时）并 `llm.ReplayToSink(sink, …)` → 所有返回路径 `OnEnd()`（与流式路径一致）。

1. **compat**：`Execute` 里在 `if req.Stream {` 之前：`if llm.NonStreamingRequestFrom(ctx) { req.Stream = false; req.StreamOptions = nil; return <非流式分支> }`。非流式分支调用 `executeChatCompletionUnary`。为了拿到回放所需的原始思考与结束原因，把 `executeChatCompletionUnary` 拆成返回 `(openMsg openaiclient.ChatCompletionMessage, finish openaiclient.FinishReason, usage …, err)` 的内部函数 + 原签名的包装（原调用者行为不变）。
   回放：`Reasoning: openMsg.ReasoningContent`、`Text: [openMsg.Content]`、`ToolCalls: result.Message.ToolCalls`（即 `compatMessageFromOpenAI` 的结果，含从推理里合成的调用）、`StopReason: string(finish)`。
2. **Anthropic**：新增 `func (c *anthropicAgentLLM) executeUnary(ctx context.Context, params anthropicapi.MessageNewParams) (*llm.Result, error)`：`msg, err := c.client.Messages.New(ctx, params)` → `agentAnthropicMessageFromAPI(msg)` → 用量照流式路径的口径（读 `msg.Usage`，与流式最终 `res.Usage` 的换算一致——照搬流式路径构造 `llm.Usage` 的代码）。
   `Execute` 在 `ctx = llm.WithStreamingRequest(ctx)` 之前：`if llm.NonStreamingRequestFrom(ctx) { return c.executeUnary(ctx, params) }`。回放：`Reasoning` = 所有 `thinking` 块文本按序拼接、`Text` = 各 `text` 块、`ToolCalls` = 结果的调用、`StopReason: string(msg.StopReason)`。
3. **Responses**：`Execute` 改为 `if c.stream && !llm.NonStreamingRequestFrom(ctx) { …executeStreaming… }`，否则 `executeSync`；仅在“流式被开关关掉”的情况下（`c.stream && NonStreamingRequestFrom(ctx)`）套超时并回放：`Reasoning: llm.SummaryText(reasoningItems)`、`Text` = 结果正文（不含 executeSync 为纯思考回答补的 ```` ```thinking ```` 块——那种情况 `Text` 为空，思考已在 `Reasoning` 里）、`ToolCalls`、`StopReason: string(resp.Status)`。`c.stream == false` 的配置保持原样（不回放、不加超时）。
4. **Google**：`Execute` 里 `if llm.NonStreamingRequestFrom(ctx) { return <executeUnary + 超时 + 回放> }`，否则 `executeStreaming`。回放：`Text` = 结果的正文 part、`ToolCalls`、`StopReason` = 候选的 `finishReason`（`parseResponse` 需把它带出来：加一个内部返回值即可）。

**Verify**：`CGO_ENABLED=1 go build ./pkg/llm/...` → exit 0

### Step 4：测试（各适配器的测试文件，照各自现有的 httptest 写法）

每个适配器各三例：
1. `…NonStreamingRequestUsesTheUnaryEndpoint`：带 `WithNonStreamingRequest` 的 ctx 调 `Execute` → 假服务器收到的是非流式请求（compat：请求体 `"stream"` 缺省或 false、无 `stream_options`；Anthropic：请求体没有 `"stream": true`；Responses：`"stream"` 缺省或 false；Google：路径以 `:generateContent` 结尾、没有 `alt=sse`）；结果与原非流式解析一致。
2. `…NonStreamingResultIsReplayedToTheSink`：记录型 sink 收到的序列与 Step 1 的顺序一致（思考 → 正文 + 块结束 → ready → 结束原因 → 结束帧）；ready 的调用与 `Result.Message.ToolCalls` 逐字相同；`Streamed` 为 true；`OnEnd` 恰好一次。
3. `…NonStreamingTimeoutIsRetryable`：`NonStreamingTimeout` 临时设为 50ms，假服务器 sleep 1s → 错误 `errors.Is(err, llm.ErrNonStreamingTimeout)` 且 `llm.IsRetryableNetworkError(err)` 为真。
另：不带开关时四个适配器的现有测试全部照旧通过（即生产路径未变）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... -count=1` → `ok`；`go vet`、`gofmt` 干净

## Done criteria

- [ ] Step 1–4 的测试全部存在并通过；`pkg/llm/...` 测试 `ok`
- [ ] `grep -rn "NonStreamingRequestFrom" pkg/llm/openai/compat_llm.go pkg/llm/openai/responses_llm.go pkg/llm/anthropic/agent_llm.go pkg/llm/google/agent_llm.go` 每个文件都有结果
- [ ] `grep -rn "WithNonStreamingRequest" pkg --include='*.go' | grep -v _test | grep -v "pkg/llm/nonstreaming.go"` 无结果（本计划不设置开关）
- [ ] `git status --short` 只含 Scope 内文件

## STOP conditions

1. 前置不满足或锚点找不到。
2. anthropic-sdk-go 的 `Messages.New` 对当前 `params`（含 prompt cache 标记、`OutputConfig.Effort`、`ServiceTier`）报“需要流式”之类的本地错误（SDK 会对预计超过 10 分钟的非流式请求直接拒绝）——回报错误文本，不要擅自改 `MaxTokens`。
3. compat 的长度续写循环（`openAIReasoningLengthShouldContinue`）在非流式下与流式下产生不同的消息结构，导致回放的正文与结果不一致——回报样例。
4. 某一步验证连续两次修正后仍失败。

## Maintenance notes

- 非流式路径的结果必须经 `ReplayToSink` 交给 sink，不要让它“静默返回”：下游（进度、收尾收集器、流中执行器、两端显示）都只认 sink 信号。
- 新增适配器要同时实现：流式路径的四个块信号（122）+ 本计划的非流式开关、超时与回放。
- `NonStreamingTimeout` 与 Claude Code 的默认（300 秒）一致；改它要同时看 `123` 里“请求已超过非流式超时”的判定（`outlastedNonStreamingTimeout`）。

## Execution addendum（2026-10-26，执行时新增）

STOP 条件 2 真的触发了，且比计划预期的范围更大：anthropic-sdk-go v1.80.0 的
`CalculateNonStreamingTimeout`（`client.go:297-320`）在**没有** caller-stated `RequestTimeout` 时按
`1h × maxTokens / 128000` 估算，`maxTokens > ~21333` 就本地拒绝
`"streaming is required for operations that may take longer than 10 minutes"`；而本仓库从
`models.json` 的 `limit.output` 取 `maxTokens`（`pkg/run/config.go:432-438`），现行 Claude 模型全是
32000/64000/128000 —— 也就是说，不改这个调用点，Anthropic 的“改用非流式重发”在生产参数下永远不可用。

修法（`client.go:305-307`：caller-stated `RequestTimeout` 直接返回、跳过估算与拒绝）：

```go
msg, err := c.client.Messages.New(ctx, params, option.WithRequestTimeout(llm.NonStreamingTimeout))
```

只加在新 `executeUnary` 的这一个调用上，并在代码里写明理由：`NonStreamingTimeout` 就是
`WithNonStreamingDeadline` 已经给这次尝试套的同一个上界，显式声明它等于用我们的界替换 SDK 的估算。
`MaxTokens` 不动、流式路径不动、这个选项不加在别处。三个 Anthropic 测试按生产形状的
`maxTokens`（64_000）跑；超时测试仍把 `NonStreamingTimeout` 临时调成 50ms（该选项在调用时读取它，
所以 SDK 超时与 ctx 截止同时生效）。
