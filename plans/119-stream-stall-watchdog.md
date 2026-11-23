# Plan 119：流式请求卡死检测——首字节/空闲超时后判定为可重试的网络错误

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在 dev 工作区。
> 工作区里若有与本计划无关的未提交改动，**不要碰、不要回滚、不要格式化**。
>
> **前置**：`115` 已完成（`grep -n "type networkRetryLLM struct" pkg/run/llm_middleware.go` 有结果）；否则 STOP 1。
>
> **Drift check（先跑）**：`git diff --stat cb4146e -- pkg/llm/request_context.go pkg/llm/openai/compat_llm.go pkg/llm/anthropic/agent_llm.go pkg/llm/openai/responses_llm.go pkg/config/load.go`
> 预期只可能列出 115 改过的 `pkg/llm/anthropic/agent_llm.go`；其余文件有输出就对照摘录，不符即 STOP 1。

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED（在所有 provider 的 HTTP 传输层加了一层；只对标记为流式、且经过网络重试层的请求生效）
- **Depends on**: plans/115-network-retry-resend-and-backoff.md
- **Category**: bug
- **Planned at**: commit `cb4146e`，2026-10-24

## Why this matters

provider 客户端没有超时（`pkg/llm/request_context.go:171` `&http.Client{Timeout: 0, …}`）：服务端或代理把流挂住不发数据时，请求要等操作系统的 TCP 超时
（实测十几分钟后才报 `read tcp …: operation timed out`），期间回合卡住。目标：

1. 经过网络重试层的**流式**请求，从发出到收到响应头超过 `T`，或收到响应头后连续 `T` 没有任何字节，即中止该请求并返回 `llm.ErrStreamStalled`（可重试），由网络重试层按既有规则重试并在状态行显示。
2. `T` 由 `agents.defaults.stream_idle_timeout_seconds` 配置，缺省 300；有效范围 10–1800，越界按边界值执行；`0` 关闭检测。
3. 用户取消（Esc、切会话、停 gateway）仍表现为取消，不会被误判为卡死。
4. 非流式请求（Google `generateContent`、compat `ExecuteStructured` 等）不受影响。

## Current state

- `pkg/llm/request_context.go:139-172`：

```go
var (
	httpTransportWrapper atomic.Pointer[func(http.RoundTripper) http.RoundTripper]
	debugLogger          atomic.Pointer[func(topic, message string)]
)
…
func WrapHTTPTransport(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	if w := httpTransportWrapper.Load(); w != nil {
		return (*w)(next)
	}
	return next
}

func NewHTTPClient() *http.Client {
	return &http.Client{Timeout: 0, Transport: WrapHTTPTransport(http.DefaultTransport)}
}
```

  所有 provider 都经过 `WrapHTTPTransport`：`anthropic/agent_llm.go:150`、`google/agent_llm.go:61`（`NewHTTPClient`）、`openai/responses_llm.go:83`、`openai/compat_llm.go:93`、`pkg/run/config.go:567`（OpenCode）。
- 流式调用点：`pkg/llm/openai/compat_llm.go:800` `c.client.CreateChatCompletionStream(ctx, req)`；`pkg/llm/anthropic/agent_llm.go:182` `c.client.Messages.NewStreaming(ctx, params)`；
  `pkg/llm/openai/responses_llm.go:1269` `c.client.Responses.NewStreaming(ctx, params)`。Google 只有非流式 `generateContent`。
- `pkg/llm/error.go` `IsRetryableNetworkError`：最前面 `errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)` 返回 false。
- `pkg/run/llm_middleware.go`（115 之后）：`wrapNetworkRetryLLM(inner, maxRetries)` 返回 `networkRetryLLM{inner, maxRetries}`；`maxRetries == 0` 时直接返回 `inner`。
  调用点：`WrapRecoverableLLM`（参数 `networkRetryMax int`）与 `runner.go` 的 `summaryChain`。
- `pkg/config/agents.go` `AgentDefaults`（`NetworkRetryMax *int` 及 `NetworkRetryMaxOrDefault` 是新字段的写法范本）；`pkg/config/load.go:134` 负数校验写法。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Go 测试 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" FOREBRAIN_HOME="$(mktemp -d)"; CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/run/... ./pkg/config/... -count=1` | 全部 `ok` |
| race | 同上环境；`CGO_ENABLED=1 go test -tags fts5 -race ./pkg/llm/ -run Watchdog -count=1` | `ok`，无 `DATA RACE` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/...`；`gofmt -l pkg/` | 无输出 |

## Scope

**In scope**：新建 `pkg/llm/stream_watchdog.go`、`pkg/llm/stream_watchdog_test.go`；`pkg/llm/request_context.go`、`pkg/llm/error.go`、`pkg/llm/error_test.go`、
`pkg/llm/openai/compat_llm.go`、`pkg/llm/anthropic/agent_llm.go`、`pkg/llm/openai/responses_llm.go`（各一行流式标记）、
`pkg/run/llm_middleware.go`、`pkg/run/llm_middleware_test.go`、`pkg/run/compaction_test.go`、`pkg/run/orchestration_llm_test.go`、`pkg/run/subagent_test.go`（如有 `wrapNetworkRetryLLM` 调用，只补参）、`pkg/run/runner.go`、`pkg/config/agents.go`、`pkg/config/load.go`、`pkg/config/config_test.go`、`forebrain.yaml`。

**Out of scope**：`http.Client.Timeout`（保持 0）；Google 适配器；非流式路径；SDK 自带的超时/重试；状态行文案（卡死错误按普通错误显示）。

## Steps

### Step 1：`pkg/llm/stream_watchdog.go`

```go
// ErrStreamStalled reports a streaming request that went silent: no response
// headers, or no body bytes, for the watchdog's window.
var ErrStreamStalled = errors.New("stream stalled")

// StreamStallError says which wait ran out.
type StreamStallError struct {
	Headers bool          // true: no response headers; false: no body bytes
	After   time.Duration
}

func (e *StreamStallError) Error() string // "stream stalled: no response within 5m0s" / "stream stalled: no data for 5m0s"
func (e *StreamStallError) Is(target error) bool { return target == ErrStreamStalled }

// WithStreamWatchdog sets the silence window for streaming requests made under ctx; 0 disables.
func WithStreamWatchdog(ctx context.Context, window time.Duration) context.Context
func streamWatchdogFrom(ctx context.Context) time.Duration

// WithStreamingRequest marks the request made under ctx as a streaming one; adapters call it right before their streaming SDK call.
func WithStreamingRequest(ctx context.Context) context.Context
func isStreamingRequest(ctx context.Context) bool
```

`watchdogTransport{next http.RoundTripper}` 的 `RoundTrip(req)`：

1. `window := streamWatchdogFrom(req.Context())`；`window <= 0 || !isStreamingRequest(req.Context())` 时直接 `return t.next.RoundTrip(req)`。
2. `ctx, cancel := context.WithCancelCause(req.Context())`；`headerTimer := time.AfterFunc(window, func() { cancel(&StreamStallError{Headers: true, After: window}) })`；
   `resp, err := t.next.RoundTrip(req.WithContext(ctx))`；`headerTimer.Stop()`。
3. `err != nil`：若 `stall, ok := context.Cause(ctx).(*StreamStallError); ok` 则 `cancel(nil)` 后返回 `nil, stall`；否则 `cancel(nil)` 返回原 `err`（父 ctx 被用户取消时原样是取消错误）。
4. 成功：`resp.Body = &watchdogBody{rc: resp.Body, window: window, cancel: cancel, ctx: ctx}`；body 的 `Read` 每次调用前 `Reset` 空闲计时器（第一次 Read 时用 `time.AfterFunc` 创建），
   计时器到期执行 `cancel(&StreamStallError{After: window})`；`Read` 返回错误时，若 `context.Cause(ctx)` 是 `*StreamStallError` 就返回它而不是底层错误；
   `Close` 停计时器、`cancel(nil)`、关闭底层 body。计时器与 Read 并发访问的状态用 `sync.Mutex` 保护。
5. 关键约束：**只取消派生的 ctx**，绝不取消 `req.Context()` 本身；返回给上层的错误必须是 `*StreamStallError`（`errors.Is(err, ErrStreamStalled)` 为 true、`errors.Is(err, context.Canceled)` 为 false）。

`request_context.go`：`WrapHTTPTransport` 先 `next = watchdogTransport{next: next}` 再套 `httpTransportWrapper`（看门狗在最内层，紧贴真实传输）。

**Verify**：`CGO_ENABLED=1 go build ./pkg/llm/...` → exit 0

### Step 2：谓词

`IsRetryableNetworkError` 在 Canceled/DeadlineExceeded 判断**之前**加：`if errors.Is(err, ErrStreamStalled) { return true }`。`error_test.go` 表里加
`&StreamStallError{Headers: true, After: time.Second}` → true、`fmt.Errorf("wrap: %w", &StreamStallError{After: time.Second})` → true。

### Step 3：适配器标记流式请求

在三个流式调用点之前各加一行 `ctx = llm.WithStreamingRequest(ctx)`（或把标记后的 ctx 直接传入调用）：`compat_llm.go:800`、`anthropic/agent_llm.go:182`、`responses_llm.go:1269`。
不要改 Google 与任何非流式调用。

**Verify**：`grep -n "WithStreamingRequest" pkg/llm/openai/compat_llm.go pkg/llm/anthropic/agent_llm.go pkg/llm/openai/responses_llm.go` → 3 行

### Step 4：配置与挂载

1. `agents.go`：`StreamIdleTimeoutSeconds *int \`yaml:"stream_idle_timeout_seconds,omitempty" json:"stream_idle_timeout_seconds,omitempty"\``；
   常量 `DefaultStreamIdleTimeoutSeconds = 300`、`MinStreamIdleTimeoutSeconds = 10`、`MaxStreamIdleTimeoutSeconds = 1800`；
   `func (d AgentDefaults) StreamIdleTimeout() time.Duration`：nil → 300s；`0` → 0；其余钳到 [10, 1800] 秒。
2. `load.go`：负数报 `agents.defaults.stream_idle_timeout_seconds must be non-negative`（照 `network_retry_max` 的写法）。
3. `forebrain.yaml` 在 `network_retry_max` 注释块之后加：

```yaml
    # Default: 300. Seconds a streaming LLM request may stay silent (no response
    # headers, or no new data) before it is treated as a dropped connection and
    # retried. 0 disables; values run between 10 and 1800.
    # stream_idle_timeout_seconds: 300
```

4. `llm_middleware.go`：`wrapNetworkRetryLLM(inner llm.LLM, maxRetries int, streamIdle time.Duration)`；`networkRetryLLM` 加字段 `streamIdle`，`Execute` 开头
   `if n.streamIdle > 0 { ctx = llm.WithStreamWatchdog(ctx, n.streamIdle) }`；`maxRetries == 0` 时也返回 `networkRetryLLM`（`inner` 直接是原 LLM、不包重试），保证关掉重试时卡死仍能报错。
   `WrapRecoverableLLM` 增加参数 `streamIdle time.Duration` 并透传。调用点补参：`runner.go` 的 `WrapRecoverableLLM(...)` 与 `summaryChain := wrapNetworkRetryLLM(...)` 两处用
   `r.AppCfg.Agents.Defaults.StreamIdleTimeout()`；测试里的调用全部传 `0`——`pkg/run/compaction_test.go`（9 处）、`pkg/run/orchestration_llm_test.go`（5 处）、
   `pkg/run/llm_middleware_test.go`（`WrapRecoverableLLM` 1 处 + 全部 `wrapNetworkRetryLLM(...)` 调用）。只机械补参，不改测试逻辑。

**Verify**：`CGO_ENABLED=1 go build ./... 2>&1 | grep -v "ld: warning\|pdfium" | head` → 无编译错误

### Step 5：测试

`pkg/llm/stream_watchdog_test.go`（`httptest.NewServer`）：
1. `TestWatchdogHeadersTimeout`：handler 永不写头（阻塞到请求 ctx 结束）；window 50ms、ctx 标记流式；`client.Do` 返回的错误 `errors.Is(err, ErrStreamStalled)` 且 `!errors.Is(err, context.Canceled)`，耗时 < 1s。
2. `TestWatchdogBodyIdle`：handler 写头 + 一行 `data: x\n\n` 并 Flush 后阻塞；读 body 先读到数据，随后 `Read` 返回 `ErrStreamStalled`。
3. `TestWatchdogKeepsALiveStreamAlive`：handler 每 20ms 写一行共 10 行，window 50ms → 完整读完无错误。
4. `TestWatchdogParentCancelIsCancel`：父 ctx 在 20ms 后取消（window 1s）→ 错误 `errors.Is(err, context.Canceled)` 且不是 `ErrStreamStalled`。
5. `TestWatchdogSkipsNonStreaming`：ctx 有 window 但未标记流式 → handler 延迟 100ms 再回（window 50ms）→ 成功。
6. `TestWatchdogDisabledWithoutWindow`：未设 window → 同上成功。

`pkg/run/llm_middleware_test.go`：`TestNetworkRetryRetriesAStalledStream`——inner 第 1 次返回 `&llm.StreamStallError{After: time.Second}`，第 2 次成功 → 调用 2 次、有 1 次非 Cleared 通知。
`pkg/config/config_test.go`：`StreamIdleTimeout()` 对 nil/0/5/300/5000 分别得 300s/0/10s/300s/1800s；负数加载报错。

**Verify**：Go 测试命令全部 `ok`；race 命令无 `DATA RACE`；`gofmt -l pkg/` 无输出。

## Done criteria

- [ ] 6 个 watchdog 测试与中间件、配置测试通过；`-race` 干净
- [ ] `grep -n "WithStreamingRequest" …`（Step 3）恰好 3 行；Google 适配器无改动
- [ ] `http.Client{Timeout: 0` 仍在 `request_context.go`
- [ ] `git status --short` 只含 Scope 内文件

## STOP conditions

1. 前置检查不满足或 Drift check 摘录与现场不符。
2. 某个 SDK 在看门狗中止后返回的是 `context.Canceled`（而不是我们返回的 `*StreamStallError`）——用对应适配器 + httptest 复现后回报，不要放宽谓词去重试 Canceled。
3. 需要改 `http.Client.Timeout` 或 Google 适配器才能通过测试。
4. 某一步验证连续两次修正后仍失败。

## Maintenance notes

- 看门狗只认 `WithStreamWatchdog` + `WithStreamingRequest` 同时存在的请求；新加流式适配器时要在调用点加 `WithStreamingRequest`。
- 卡死后的重试、次数、状态行显示全部复用网络重试层，不要在看门狗里重试。
- 默认 300 秒；调小前确认推理模型长时间思考时也会持续发字节（思考 delta），否则会被误判。
