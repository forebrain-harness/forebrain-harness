# Plan 118：网络重试层同时重试 HTTP 408/409/5xx（529 除外）并遵守 Retry-After；状态行对 HTTP 错误写“接口错误”

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在 dev 工作区。
> 工作区里若有与本计划无关的未提交改动，**不要碰、不要回滚、不要格式化**。
>
> **前置**：`115`、`116`、`117` 已完成：`grep -n "WithUpwardJitter" pkg/llm/retry.go`、`grep -n "func transportRetryStatusLine" pkg/tui/run.go`、
> `grep -n "export function formatTransportRetryLine" frontend/src/composables/useChatStream.ts` 都应有结果；否则 STOP 1。
>
> **Drift check（先跑）**：`git diff --stat cb4146e -- pkg/llm/error.go pkg/llm/error_test.go pkg/llm/ratelimit.go pkg/llm/anthropic/error.go pkg/llm/openai/error.go pkg/llm/google/agent_llm.go`
> 预期**无输出**（`retry.go`、`stream.go`、`llm_middleware.go`、`subagent.go`、`run_events.go`、TUI/web 文件已被 115–117 改过，按符号对照现场）。

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED（扩大了会被自动重试的错误集合）
- **Depends on**: plans/115、116、117
- **Category**: direction（owner 2026-10-24 定稿）
- **Planned at**: commit `cb4146e`，2026-10-24

## Why this matters

现在只有传输层错误（超时、连接重置、断流）会自动重试；服务端临时故障（HTTP 408、409、500、502、503、504 等）直接让回合失败——
compat（DeepSeek 等）、Google、OpenCode 适配器没有 SDK 自带重试兜底。服务端给了 `Retry-After` 也被忽略。目标：

1. 网络重试层的重试条件 = 现有传输层错误 ∪ HTTP `408`、`409`、`500–599` 中除 `529` 以外的状态码。`529`（过载）与 `429`（限流/用量）继续走既有路径，不进这一层。
2. 等待时间 = `max(退避计算值, Retry-After)`；Retry-After 超过 5 分钟时不再重试，直接返回该错误。
3. 事件携带 HTTP 状态码；状态行在第 1、2 次对 HTTP 错误写 `API error` / `接口错误`（传输层错误仍写 `Network error` / `网络错误`），第 3 次起都写错误原文。（2026-11-04 修订：错误原文含请求方法、URL、地址等底层细节，已取消；每次都只写通用原因，`API error` / `Network error`。）

## Current state

- `pkg/llm/error.go:30-77` — `IsRetryableNetworkError(err)`：排除 Canceled/DeadlineExceeded、DNS NXDOMAIN、三类 x509；`net.Error`、`io.ErrUnexpectedEOF`、`io.EOF`、
  `ECONNRESET`、`ECONNABORTED`、`ErrStreamTruncated` 返回 true；最后 `errors.As(err, &aerr)` 时递归 `IsRetryableNetworkError(aerr.Err)`。
- `pkg/llm/error.go:88-123` — `APIError{StatusCode, Code, Type, Message, Err, RateLimit}`；`:171-178` 未导出的 `statusOf(err)`。
- `pkg/llm/ratelimit.go:318-332` — 未导出的 `parseRetryAfter(header, receivedAt)`（秒数或 HTTP 日期）；`ParseRateLimit`（`:71`）只对 429 生效。
- 适配器构造 `APIError` 处：`pkg/llm/anthropic/error.go:25-48`（有 `retryAfterHeader(apiErr.Response)`）、`pkg/llm/openai/error.go:23-60`
  （Responses 分支有 `retryAfterHeader(respErr.Response)`；compat 分支的 go-openai 错误不带响应，拿不到 Retry-After）、`pkg/llm/google/agent_llm.go:405-418` `newAPIError(status, payload, retryAfter)`。
- `pkg/llm/retry.go`（115 之后）— `retryConfig`、`Execute` 里的 `sleep` 计算与睡前 `notify`。
- `pkg/llm/stream.go`（115 之后）— `TransportRetry{Retry, Total, Wait, Err, Cleared}`。
- `pkg/run/llm_middleware.go` `wrapNetworkRetryLLM`（115 之后）；`recoverableLLM.Execute` 的 529 分支（`isStatus529` 文本匹配，`:439-460` 一带）。
- `pkg/run/subagent.go` `EventStreamSink.OnTransportRetry`；`pkg/event/run_events.go` `TransportRetryPayload`。
- `pkg/tui/run.go` `transportRetryStatusLine`（116）；`frontend/src/composables/useChatStream.ts` `formatTransportRetryLine`、`TransportRetryStatus`（117）；
  `frontend/src/locales/index.ts` `chat.transportRetry.*`。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Go 测试 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" FOREBRAIN_HOME="$(mktemp -d)"; CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/run/... ./pkg/tui/... ./pkg/event/... -count=1` | 全部 `ok` |
| vet / fmt | 同上；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/...`；`gofmt -l pkg/` | 无输出 |
| 前端 | `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts && pnpm typecheck` | 通过 / exit 0 |

## Scope

**In scope**：`pkg/llm/error.go`、`pkg/llm/error_test.go`、`pkg/llm/ratelimit.go`、`pkg/llm/retry.go`、`pkg/llm/retry_test.go`、`pkg/llm/stream.go`、
`pkg/llm/anthropic/error.go`、`pkg/llm/openai/error.go`、`pkg/llm/google/agent_llm.go`、`pkg/run/llm_middleware.go`、`pkg/run/llm_middleware_test.go`、
`pkg/run/subagent.go`、`pkg/event/run_events.go`、`pkg/tui/notify.go`、`pkg/tui/chat_turn.go`、`pkg/tui/reducer.go`（只改 Tracker/消息里多带一个字段）、
`pkg/tui/run.go`、`pkg/tui/transport_retry_test.go`、`frontend/src/composables/useChatStream.ts`、`frontend/src/composables/useChatStream.test.ts`、`frontend/src/locales/index.ts`。

**Out of scope**：`recoverableLLM` 的 529 分支；429/用量限制/自动继续；各 SDK 自带的重试配置（`MaxRetries` 不改）；compat 适配器的 Retry-After（go-openai 不保留响应）；空输出重试链。

## Steps

### Step 1：`APIError.RetryAfter` 与导出的工具函数

1. `error.go` `APIError` 增加字段：

```go
	// RetryAfter is the wait the provider asked for with Retry-After on any
	// status, zero when it sent none.
	RetryAfter time.Duration
```

2. 新增导出函数（`error.go`）：

```go
// HTTPStatusOf returns the HTTP status of a normalized provider error, 0 when it carries none.
func HTTPStatusOf(err error) int { return statusOf(err) }

// RetryAfterOf returns the Retry-After wait a normalized provider error carries, 0 when none.
func RetryAfterOf(err error) time.Duration
```

3. `ratelimit.go`：新增导出包装 `func ParseRetryAfter(header string, receivedAt time.Time) time.Duration { return parseRetryAfter(header, receivedAt) }`。
4. 三个适配器填字段：anthropic `normalizeError` 与 openai Responses 分支加 `RetryAfter: llm.ParseRetryAfter(retryAfterHeader(<resp>), receivedAt)`（anthropic 那里用一次 `time.Now()` 变量，给 `ParseRateLimit` 与它共用）；
   google `newAPIError` 加 `RetryAfter: llm.ParseRetryAfter(retryAfter, time.Now())`（同样共用一个时间变量）。

**Verify**：`CGO_ENABLED=1 go build ./pkg/llm/...` → exit 0

### Step 2：扩大重试条件

`IsRetryableNetworkError`：在最后的 `errors.As(err, &aerr)` 分支里，递归之前先判状态码：

```go
	var aerr *APIError
	if errors.As(err, &aerr) && aerr != nil {
		switch status := aerr.StatusCode; {
		case status == http.StatusRequestTimeout, status == http.StatusConflict:
			return true
		case status >= 500 && status <= 599 && status != 529:
			return true
		}
		if aerr.Err != nil {
			return IsRetryableNetworkError(aerr.Err)
		}
	}
	return false
```

更新函数 doc 注释：传输层失败与服务端临时状态码（408、409、5xx 但不含 529）。

`error_test.go`：在谓词表驱动测试里加：`APIError{StatusCode:408}`、`409`、`500`、`502`、`503`、`504` → true；`529`、`429`、`400`、`401`、`404` → false。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ -run IsRetryable -count=1` → `ok`

### Step 3：Retry-After 进入等待计算

1. `retry.go`：`retryConfig` 加 `retryAfter func(error) time.Duration` 与 `maxRetryAfter time.Duration`；新选项：

```go
// WithRetryAfter makes each wait at least the server-requested one; a request
// for longer than limit ends the retries and returns the error.
func WithRetryAfter(of func(error) time.Duration, limit time.Duration) RetryOption
```

2. `Execute`：算出 `sleep` 后、通知前：

```go
		if r.cfg.retryAfter != nil {
			if ra := r.cfg.retryAfter(err); ra > 0 {
				if r.cfg.maxRetryAfter > 0 && ra > r.cfg.maxRetryAfter {
					return nil, err
				}
				if ra > sleep {
					sleep = ra
				}
			}
		}
```

3. `wrapNetworkRetryLLM` 加 `llm.WithRetryAfter(llm.RetryAfterOf, 5*time.Minute)`。
4. `retry_test.go`：`TestRetryHonoursRetryAfter`（APIError 503 带 `RetryAfter: 40ms`，退避 1ms → 通知的 `Wait >= 40ms`）；
   `TestRetryStopsOnLongRetryAfter`（`RetryAfter` 超过 limit → 只调用 1 次、无通知、返回原错误）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ -run 'Retry' -count=1` → `ok`

### Step 4：状态码进入通知与事件

1. `stream.go` `TransportRetry` 加 `Status int`（HTTP status of the failure, 0 for transport errors）。
2. `retry.go` 睡前通知里填 `Status: HTTPStatusOf(err)`。
3. `event/run_events.go` `TransportRetryPayload` 加 `Status int \`json:"status,omitempty"\``；`subagent.go` 发布时填 `Status: tr.Status`。
4. TUI：`notify.go` `TransportRetryMsg` 加 `Status int`（前台 `chat_turn.go` 与事件路径都填）；`reducer.go` 的 `TransportRetryStatus` 加 `Status int`，
   `Tracker.ObserveTransportRetry` 增加 `status int` 参数并存下（`pkg/tui/transport_retry_test.go` 里已有的调用机械补 `0`）；`transportRetryStatusLine` 的泛称：`s.Status > 0` 时为 `API error`，否则 `Network error`。
5. web：`TransportRetryStatus` 加 `status: number`（`Number(payload.status ?? 0)`）；locale 加 `chat.transportRetry.apiError`（zh `接口错误` / en `API error`）；
   `formatTransportRetryLine` 的泛称按 `status > 0` 选择。
6. 测试：`pkg/tui/transport_retry_test.go` 加 `Status: 503` 第 1 次 → `API error · Retrying in …`；`useChatStream.test.ts` 加 `status: 503` → reason `API error`。

**Verify**：全部 Go 测试命令 `ok`；前端单测通过、`pnpm typecheck` exit 0；`gofmt -l pkg/` 无输出。

### Step 5：中间件层测试

`pkg/run/llm_middleware_test.go` 加：
- `TestNetworkRetryRetriesServerErrors`：inner 第 1 次返回 `&llm.APIError{StatusCode: 503}`，第 2 次成功 → 调用 2 次、通知 `Status == 503`。
- `TestNetworkRetryLeaves529ToRecovery`：`WrapRecoverableLLM` 包一个第 1 次返回 `&llm.APIError{StatusCode: 529, Message: "overloaded"}`、第 2 次成功的 inner，
  带 `OnTransportRetry` 记录的 sink；断言没有任何 `OnTransportRetry` 通知（529 由 recoverable 分支重试）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -run NetworkRetry -count=1` → `ok`

## Done criteria

- [ ] Step 2 的状态码表全部通过；`529`、`429` 不被网络重试层重试
- [ ] Retry-After 两个测试通过
- [ ] TUI 与 web 在 `status > 0` 的第 1 次重试显示 `API error` / `接口错误`
- [ ] 全部 Go 测试 `ok`、`go vet`/`gofmt` 干净；前端单测与 `pnpm typecheck` 通过
- [ ] `git status --short` 只含 Scope 内文件

## STOP conditions

1. 前置检查不满足或 Drift check 摘录与现场不符。
2. 某个适配器的 5xx 错误没有被规范化成带 `StatusCode` 的 `*llm.APIError`（用 httptest 返回 503 时谓词为 false）——回报适配器名，不要用错误文本匹配补救。
3. `TestNetworkRetryLeaves529ToRecovery` 发现 529 同时被两层重试。
4. 某一步验证连续两次修正后仍失败。

## Maintenance notes

- 重试条件只有 `IsRetryableNetworkError` 一处；以后新增可重试状态码改这里。
- anthropic、openai-go（Responses）SDK 自带的重试仍然在我们这一层之下生效，一次“attempt”可能包含 SDK 内部的若干次请求。
- `Status` 字段只用于状态行选择泛称，不参与重试判断。
