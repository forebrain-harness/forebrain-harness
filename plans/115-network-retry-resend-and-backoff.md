# Plan 115：网络重试改为整请求重发——删除续写与中断落库，退避封顶 32s、次数上限 15，引擎发出“重试结束”信号

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在 dev 工作区。
> 工作区里若有与本计划无关的未提交改动，**不要碰、不要回滚、不要格式化**。
>
> **Drift check（先跑）**：
> `git diff --stat cb4146e -- pkg/llm/retry.go pkg/llm/retry_test.go pkg/llm/stream.go pkg/llm/llm.go pkg/llm/anthropic/agent_llm.go pkg/run/llm_middleware.go pkg/run/llm_middleware_test.go pkg/run/runner.go pkg/run/orchestration_llm.go pkg/run/orchestration_llm_test.go pkg/run/transcript_progress.go pkg/run/subagent.go pkg/run/subagent_test.go pkg/event/run_events.go pkg/config/agents.go pkg/config/config_test.go forebrain.yaml pkg/gateway/run_control.go pkg/gateway/stream_parts.go pkg/gateway/stream_parts_test.go pkg/tui/chat_turn.go pkg/tui/notify.go pkg/tui/reducer.go pkg/tui/run.go pkg/tui/transport_retry_test.go frontend/src/composables/useChatStream.ts frontend/src/composables/useChatStream.test.ts`
> 预期**无输出**。有输出就逐段对照下面 "Current state" 的摘录，不符即 STOP 1。

## Status

- **Priority**: P1（`116`–`119` 都建在它上面）
- **Effort**: L
- **Risk**: MED（改的是每一次 LLM 请求外层的重试循环）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `cb4146e`，2026-10-24

## Why this matters

当前网络重试（cb4146e）以“续写 prefill + 半截文字单独落库”的方式实现，退避不封顶、次数不设上限。本计划把它改为下面的最终方案（owner 2026-10-24 定稿）：

1. 断流时**整请求原样重发**；断掉那次**还没写完**的正文/思考从各端撤下、**不落库**；已经写完的思考块保留。
2. 退避：第 n 次重试等待 `min(32s, 500ms × 2^(n−1))`，再加 0–25% 的随机上浮。
3. 次数：`agents.defaults.network_retry_max`，缺省 10；`0` 关闭；负数加载报错；大于 15 按 15 执行并写一条告警日志。
4. 引擎在“重试后的那次请求开始产出内容”或“重试循环结束（成功/失败/取消）”时发出一次**重试结束**通知（`Cleared`），供状态行恢复（`116`/`117` 使用）。
5. 次数用完时回合错误文案为 `network error, gave up after N retries: <最后一次的错误>`。

本计划不改任何界面外观：TUI 与 web 的重试卡片暂时保留（`116`、`117` 会把它们换成状态行），本计划只把各端的“保留半截文字”改为“撤下半截文字”。

## Current state

### pkg/llm

- `pkg/llm/retry.go:95-197` — `retryLLM.Execute`。`:105-128` 包装 sink 累积 `continuation`；`:135-138` 第二次起把 prefill 塞进 ctx；
  `:153-160` 抖动是 ±25%（`sleep = backoff + rand(0, backoff/2) − backoff/4`）；`:162-169` 睡前 notify；`:177-180` 翻倍、`unboundedBackoff` 时不封顶；
  `:183-194` 用尽后补发 `Exhausted: true`；`:196` 返回 `&MaxAttemptsExceededError{...}`。
- `pkg/llm/retry.go:21-58` — `retryConfig`（含 `unboundedBackoff`、`notify` 字段）、`WithShouldRetry`、`WithUnboundedBackoff`、`WithRetryNotify`。
  默认值 `:12-15`：`defaultInitialBackoff = 500ms`、`defaultMaxBackoff = 30s`。测试辅助 `WithInitialBackoff` 定义在 `pkg/llm/retry_test.go:171`。
- `pkg/llm/stream.go:43-69` — `StreamSink.OnTransportRetry` 与 `TransportRetry{Retry, Total, Wait, Err, Exhausted}`；
  `:104-138` — `ContinuationPrefill`、`Blank`、`continuationPrefillCtxKey`、`WithContinuationPrefill`、`ContinuationPrefillFrom`；
  `:161-163` — `WithMutedForegroundStream` 里 `muted.OnTransportRetry = nil`（保留）。
- `pkg/llm/llm.go:281-299` — `ContinuationMessage`。
- `pkg/llm/anthropic/agent_llm.go:380-391` — 空 assistant 块跳过防护；该段缩进错误，`gofmt -l pkg/` 当前输出这个文件：

```go
				}
			blocks = append(blocks, anthropicapi.NewToolUseBlock(id, input, tc.Function.Name))
		}
		// An assistant message with nothing this protocol can express — a
		// continuation prefill that carried only thinking (this adapter has no
		// request-side thinking form) — contributes nothing and must not be
		// sent: an empty content array is a request error. Its words, if it
		// had any, are in the text blocks above.
		if len(blocks) == 0 {
			continue
		}
		out = append(out, anthropicapi.NewAssistantMessage(blocks...))
```

- `pkg/llm/errors.go:333-350` — `MaxAttemptsExceededError{Attempts, Err}`，`Error()` 为 `middleware: all %d attempts failed: %v`（不要改它，空输出重试链也用它）。

### pkg/run

- `pkg/run/llm_middleware.go:257-284` — `continuationPrefillLLM` / `wrapContinuationPrefillLLM`。
- `pkg/run/llm_middleware.go:286` — 注释换行被吞：`// withImageHeal attaches the one retry a refused image gets: when the provider// rejects a request because of an image, the request is re-sent once with every`。
- `pkg/run/llm_middleware.go:321` — `inner = withImageHeal(wrapContinuationPrefillLLM(inner))`；`:326` — `inner = wrapNetworkRetryLLM(inner, networkRetryMax)`。
- `pkg/run/llm_middleware.go:357-381` — `wrapNetworkRetryLLM`：`llm.NewRetry(1+maxRetries, WithShouldRetry(IsRetryableNetworkError), WithUnboundedBackoff(), WithRetryNotify(...))`，
  notify 回调取 `llm.StreamSinkFrom(ctx).OnTransportRetry` 调用。
- `pkg/run/runner.go:1036-1037`：

```go
	networkRetryMax := r.AppCfg.Agents.Defaults.NetworkRetryMaxOrDefault()
	summaryChain := wrapNetworkRetryLLM(withImageHeal(wrapContinuationPrefillLLM(llmClient)), networkRetryMax)
```

- `pkg/run/orchestration_llm.go:118-143` — `recordReasoningBlocks`：`OnDelta` 里多了 `transcript.appendAssistant(text)`（`:121`）；
  `OnTransportRetry` 包装调用 `transcript.flushInterrupted(sinkCtx, model)`（`:132-142`）。
- `pkg/run/transcript_progress.go:37-41` 字段 `assistantText strings.Builder`；`:117-169` `appendAssistant`、`flushInterrupted`。
  同文件 `takeReasoning`（`:101-115`）是清空 reasoning 缓冲的写法，新函数照它写。
- `pkg/run/subagent.go:1024-1036` — `EventStreamSink` 的 `OnTransportRetry` 发布 `event.TransportRetryPayload`，其中 `Error: tr.Err.Error()`（`Err` 为 nil 时会 panic）、`Exhausted: tr.Exhausted`。

### pkg/event、pkg/config、forebrain.yaml

- `pkg/event/run_events.go:603-610` — `TransportRetryPayload{Retry, Total, WaitMs, Error, AgentID, Exhausted}`（json：`retry,total,wait_ms,error,agent_id,exhausted`）。
- `pkg/config/agents.go:117-136` — `NetworkRetryMax *int`、`DefaultNetworkRetryMax = 10`、`NetworkRetryMaxOrDefault()`。
- `pkg/config/load.go:134-136` — 负数报 `agents.defaults.network_retry_max must be non-negative`（保留）。
- `forebrain.yaml:183-187` — 注释写着“any larger value is allowed — retry waits grow exponentially and without a ceiling”。

### pkg/gateway

- `pkg/gateway/run_control.go:448-458` — 包装 `stream.OnTransportRetry`：先 `partial.Reset()` 再链调发布。
- `pkg/gateway/stream_parts.go:90-170` — `observe`：只处理 `assistant_delta/reasoning_delta/reasoning_done/tool_*`，其余 `default: return`；
  `reasoning_done` 分支是“按 agent 清缓冲”的写法（`run.textFor(p.AgentID).reasoning = ""`）。
- `pkg/gateway/stream_parts_test.go:260-271` — `TestStreamPartsTransportRetryKeepsAccumulating` 断言重试后文本继续拼接（本计划要反过来）。

### pkg/tui（只改语义，不改外观）

- `pkg/tui/chat_turn.go:498-501`：

```go
		OnTransportRetry: func(tr llm.TransportRetry) {
			accum.Reset()
			s.notifyUIForSession(sessionID, TransportRetryMsg{turn: foreground, Retry: tr.Retry, Total: tr.Total, Wait: tr.Wait, Err: tr.Err, Exhausted: tr.Exhausted})
		},
```

- `pkg/tui/notify.go:94-109` — `TransportRetryMsg{turn, AgentID, Retry, Total, Wait, Err, Exhausted}`；`:884-895` 事件路径把 payload 转成该消息。
- `pkg/tui/reducer.go:1383-1420` — `case TransportRetryMsg:` 清 `agentBuf`，产出 `FrameError` + `FrameRetrying` 两帧；`:1414` `if m.Exhausted { card.Final = true }`。
- `pkg/tui/run.go:348-354` — 渲染前调用 `renderer.SealDeadStreamBlocks(retry.AgentID)`。
- `pkg/tui/reducer.go:5352-5392` — `SealDeadStreamBlocks`：从尾部往前，把非 Final 的 `FrameAssistant`/`FrameThinking` 块标成 Final 保留。
- `pkg/tui/transport_retry_test.go` — `TestTransportRetryExhaustedFreezesTheCard`（`:51`）、`TestSealDeadStreamBlocksKeepsTrailingLiveBlocksSealed`（`:95`）。

### frontend

- `frontend/src/composables/useChatStream.ts:1350-1380` — `applyTransportRetryBlocks`：`closeStreamedBlocks(blocks, ['assistant','thinking'])` 把半截块封存保留，然后放错误块与重试块。
- `frontend/src/composables/useChatStream.ts:3359-3366` — 子代理 `case 'transport_retry'`。
- `frontend/src/composables/useChatStream.ts:4659-4671` — 主对话 `case 'transport_retry'`：`state.fullAnswer = ''`、`answer.value = ''`（`fullAnswer` 是整个回合累加的，清空会丢掉前几轮已说完的话）。
- `TimelineBlock` 的 `assistant`/`thinking` 形状（`:1175-1176`）都有 `open: boolean`；进行中的块 `open === true`。
- payload 在前端经 `toCamelCase` 转成驼峰（`wait_ms` → `waitMs`，`cleared` 不变）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Go 测试（单包） | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" FOREBRAIN_HOME="$(mktemp -d)"; CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... -count=1` | `ok` |
| Go 测试（本计划全部包） | 同上环境变量；`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/run/... ./pkg/tui/... ./pkg/gateway/... ./pkg/config/... ./pkg/turn/... ./pkg/event/... -count=1` | 全部 `ok` |
| vet | 同上环境变量；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/...` | 无输出 |
| gofmt | `gofmt -l pkg/ cmd/` | 无输出 |
| 前端单测 | `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts` | 全部通过 |
| 前端类型检查 | `cd frontend && pnpm typecheck` | exit 0 |

不设 `PKG_CONFIG_PATH` 时 `pkg/run`、`pkg/tui`、`pkg/gateway`、`pkg/turn` 会因找不到 pdfium 编译失败，这是环境问题，不是代码问题。

## Scope

**In scope**：Drift check 里列出的全部文件。

**Out of scope**：
- TUI 的 `FrameRetrying` 卡片、web 的 `TransportRetryCard.vue` / `retry` 块的外观与去留——由 `116`、`117` 处理，本计划不删不改它们的渲染。
- `IsRetryableNetworkError` 的判定范围、HTTP 状态码重试、Retry-After——由 `118` 处理。
- 流空闲/首字节超时——由 `119` 处理。
- `recoverableLLM` 的 529 重试分支、空输出重试链（`wrapBlankOutputRetryLLM`）的行为。
- `MaxAttemptsExceededError` 的定义与文案。
- 用户库里已有的“中断消息”行（cb4146e 期间写入的普通 assistant 行），不迁移、不删除。

## Steps

### Step 1：`pkg/llm/retry.go` —— 去掉续写，加封顶与上浮抖动，用“重试结束”替换“用尽”通知

1. 删除 `retryConfig.unboundedBackoff` 字段与 `WithUnboundedBackoff`。
2. 新增两个选项（放在 `WithRetryNotify` 旁边，带英文 doc 注释）：

```go
// WithMaxBackoff caps each back-off step before jitter is added.
func WithMaxBackoff(d time.Duration) RetryOption {
	return func(c *retryConfig) { c.maxBackoff = d }
}

// WithUpwardJitter replaces the default ±25% jitter with an upward-only one:
// each wait is the back-off step plus 0–25% of it.
func WithUpwardJitter() RetryOption {
	return func(c *retryConfig) { c.upwardJitter = true }
}
```

   `retryConfig` 增加 `upwardJitter bool`。
3. 重写 `Execute`（`:95-197`）为下面的形状（保持原有缩进与注释风格，注释用英文）：
   - 删除 `continuation` 变量、`:105-128` 的 sink 包装、`:135-138` 的 `attemptCtx`；每次都用同一个 `messages` 调 `r.inner.Execute(ctx, messages, tools)`。
   - 仅当 `r.cfg.notify != nil` 时安装“重试结束”检测：

```go
	notifyCtx := ctx
	var pendingRetry atomic.Int64 // retry number awaiting its Cleared notice, 0 = none
	clearPending := func() {
		if n := pendingRetry.Swap(0); n > 0 {
			r.cfg.notify(notifyCtx, TransportRetry{Retry: int(n), Total: maxRetries, Cleared: true})
		}
	}
	if r.cfg.notify != nil {
		defer clearPending()
		if sink := StreamSinkFrom(ctx); sink != nil {
			wrapped := *sink
			if down := wrapped.OnDelta; down != nil {
				wrapped.OnDelta = func(text string) {
					if text != "" {
						clearPending()
					}
					down(text)
				}
			}
			if down := wrapped.OnReasoningDelta; down != nil {
				wrapped.OnReasoningDelta = func(text string) {
					if text != "" {
						clearPending()
					}
					down(text)
				}
			}
			ctx = WithStreamSink(ctx, &wrapped)
		}
	}
```

     要点：只包装**原本非 nil** 的回调（muted ctx 下 `OnDelta` 为 nil，必须保持 nil）；`notify` 一律用 `notifyCtx`（包装前的 ctx）。
   - 抖动：

```go
		sleep := backoff
		if r.cfg.upwardJitter {
			if quarter := int64(backoff) / 4; quarter > 0 {
				sleep += time.Duration(rand.Int63n(quarter + 1)) //nolint:gosec
			}
		} else if half := int64(backoff) / 2; half > 0 {
			// existing ±25% branch, unchanged
		}
```

   - 睡前通知不变（`Retry: attempt + 1, Total: maxRetries, Wait: sleep, Err: err`），紧接着 `pendingRetry.Store(int64(attempt + 1))`（仅 `notify != nil` 时）。
   - 翻倍：`backoff *= 2; if backoff > r.cfg.maxBackoff { backoff = r.cfg.maxBackoff }`（去掉 `unboundedBackoff` 条件）。
   - 删除 `:183-194` 的 Exhausted 补发块；末尾仍 `return nil, &MaxAttemptsExceededError{Attempts: r.cfg.maxAttempts, Err: lastErr}`。
   - 更新 `Execute` 的 doc 注释：重发原请求；带 notify 时在重试后首个非空 delta 或返回时发一次 `Cleared`。
4. `import` 增加 `sync/atomic`。

**Verify**：`grep -n "continuation\|unboundedBackoff\|Exhausted" pkg/llm/retry.go` → 无输出（`Exhausted` 若出现在注释里也要改掉）。

### Step 2：`pkg/llm/stream.go`、`pkg/llm/llm.go`、`pkg/llm/anthropic/agent_llm.go`

1. `stream.go`：`TransportRetry` 删除 `Exhausted`，新增：

```go
	// Cleared marks the end of a retry: the retried request started producing
	// output, or the retry loop returned (success, exhaustion or cancel). Only
	// Retry and Total are set on it.
	Cleared bool
```

   把 `OnTransportRetry` 的注释改为：一次通知 = 一次重试即将发生（`Cleared` 为 false）或重试结束（`Cleared` 为 true）；
   非 `Cleared` 通知意味着**进行中**的正文/思考作废（已写完的思考块不受影响），持有进行中缓冲的各层要清掉它。
2. 删除 `stream.go:104-138` 的 `ContinuationPrefill` 整段（类型、`Blank`、ctx key、`WithContinuationPrefill`、`ContinuationPrefillFrom`）。
3. `llm.go`：删除 `ContinuationMessage`（`:281-299`）。若删除后 `strings` 等 import 变成未使用，一并删掉。
4. `anthropic/agent_llm.go`：保留 `len(blocks) == 0` 防护，把注释改成通用说法
   （`// An assistant message with nothing this protocol can express contributes nothing and must not be sent: an empty content array is a request error.`），然后 `gofmt -w pkg/llm/anthropic/agent_llm.go`。

**Verify**：
- `grep -rn "ContinuationPrefill\|ContinuationMessage\|WithContinuationPrefill" pkg cmd` → 无输出
- `gofmt -l pkg/llm` → 无输出
- `CGO_ENABLED=1 go build ./pkg/llm/...` → exit 0

### Step 3：`pkg/llm/retry_test.go`

1. `TestNewRetry_NotifyPayloadAndExhausted` 改名 `TestNewRetry_NotifyPayloadAndCleared`：4 次全失败 → 通知共 4 条：前 3 条 `Retry=1..3, Total=3, Cleared=false, Err=boom`，第 4 条 `Cleared=true, Retry=3, Total=3`。
2. `TestRetryBackoffUnboundedVersusCapped` 改为 `TestRetryBackoffCapsWithUpwardJitter`：`initialBackoff=1ms, maxBackoff=2ms, upwardJitter=true, maxAttempts=4`；
   断言 3 个非 Cleared 通知的 `Wait` 分别落在 `[1ms,1.25ms]`、`[2ms,2.5ms]`、`[2ms,2.5ms]`。
3. 新增 `TestNewRetry_ClearedOnFirstDeltaAfterRetry`：inner 第 1 次返回 `&net.OpError{...}`，第 2 次先 `sink.OnDelta("hi")` 再成功；
   ctx 带一个 `OnDelta` 记录到共享切片的 sink；notify 也写入同一切片。断言顺序为 `retry(1)`、`cleared`、`delta "hi"`，且之后不再有通知。
4. 新增 `TestNewRetry_LeavesSinkAloneWithoutNotify`：不带 `WithRetryNotify`，inner 里断言 `StreamSinkFrom(ctx)` 与调用方传入的是**同一个指针**。
5. 新增 `TestNewRetry_ResendsTheSameMessages`：inner 记录每次收到的 `messages`，第 1 次失败第 2 次成功；断言两次收到的切片长度与内容相同。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ -run 'TestNewRetry|TestRetryBackoff' -count=1` → `ok`

### Step 4：`pkg/config` 与 `forebrain.yaml`

1. `agents.go`：新增 `const MaxNetworkRetryMax = 15`（紧挨 `DefaultNetworkRetryMax`，英文注释：values above it are run at it）；
   `NetworkRetryMaxOrDefault()` 对非 nil 值返回 `min(*d.NetworkRetryMax, MaxNetworkRetryMax)`；更新字段与函数注释（“0 disables; above 15 runs at 15”）。
2. `load.go` 不改（负数仍报错）。
3. `config_test.go`：在 `TestNetworkRetryMaxDefaultsToTenAndRoundTrips` 旁新增 `TestNetworkRetryMaxCapsAtFifteen`：值 12 → 12，值 99 → 15，值 0 → 0。
4. `forebrain.yaml:183-187` 注释改为：

```yaml
    # Default: 10. Retry cap for LLM transport failures (a dropped stream
    # mid-answer). 0 disables network retries; values above 15 run as 15.
    # Waits start at 0.5s and double up to 32s.
    # network_retry_max: 10
```

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/config/ -run NetworkRetryMax -count=1` → `ok`

### Step 5：`pkg/run` —— 链路、记录器、发布点、用尽文案

1. `llm_middleware.go`：
   - 删除 `continuationPrefillLLM`、`wrapContinuationPrefillLLM`（`:257-284`）。
   - 修复 `:286` 被吞掉的换行，恢复为两行注释。
   - `WrapRecoverableLLM`：`inner = withImageHeal(inner)`，删除注释里关于 continuation prefill 的句子。
   - `wrapNetworkRetryLLM`：把 `llm.WithUnboundedBackoff()` 换成 `llm.WithMaxBackoff(32*time.Second), llm.WithUpwardJitter()`；
     `maxRetries > config.MaxNetworkRetryMax` 时按上限执行（`pkg/run` 已 import `appcfg "…/pkg/config"`，用同一别名；若该文件尚未 import，按 `runner.go` 的写法加）；
     把返回值包一层，把次数用尽的错误改写成用户可读文案：

```go
type networkRetryLLM struct {
	inner      llm.LLM
	maxRetries int
}

func (n networkRetryLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	res, err := n.inner.Execute(ctx, messages, tools)
	var exhausted *llm.MaxAttemptsExceededError
	if err != nil && errors.As(err, &exhausted) {
		return nil, fmt.Errorf("network error, gave up after %d retries: %w", n.maxRetries, exhausted.Err)
	}
	return res, err
}
```

     `wrapNetworkRetryLLM` 最后 `return networkRetryLLM{inner: llm.Use(inner, retryMW), maxRetries: maxRetries}`。更新函数 doc 注释（整请求重发、封顶 32s、上限 15）。
2. `runner.go:1036-1037`：`summaryChain := wrapNetworkRetryLLM(withImageHeal(llmClient), networkRetryMax)`；更新上方注释，删掉“continue from the partial summary”。
   紧接着加告警（只在配置值超上限时）：

```go
	if v := r.AppCfg.Agents.Defaults.NetworkRetryMax; v != nil && *v > appcfg.MaxNetworkRetryMax {
		slog.Warn("agents.defaults.network_retry_max above the limit; running with the limit", "configured", *v, "limit", appcfg.MaxNetworkRetryMax)
	}
```

   （用 `runner.go` 已有的 config 包别名与 `slog` import；没有就按文件现有风格补。）
3. `transcript_progress.go`：删除 `assistantText` 字段、`appendAssistant`、`flushInterrupted`；新增：

```go
// dropInFlightReasoning discards the reasoning block still being streamed
// without writing it: the attempt that streamed it died on the network and the
// retry re-streams from the first token. Blocks that already ended were written
// when they ended and stay.
func (t *transcriptProgress) dropInFlightReasoning() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.reasoning.Reset()
	t.reasoningAt = time.Time{}
	t.mu.Unlock()
}
```

4. `orchestration_llm.go:118-143`：删除 `transcript.appendAssistant(text)`；`OnTransportRetry` 包装改为：

```go
	downstreamTransportRetry := wrapped.OnTransportRetry
	wrapped.OnTransportRetry = func(tr llm.TransportRetry) {
		if !tr.Cleared {
			transcript.dropInFlightReasoning()
		}
		if downstreamTransportRetry != nil {
			downstreamTransportRetry(tr)
		}
	}
```

   相应改写上方注释（进行中的思考作废、不落库；已写完的块早已落库）。如果 `w.transcriptModel` / `sinkCtx` 因此在该闭包里不再使用，不要动它们在别处的用法。
5. `subagent.go:1024-1036`：

```go
		OnTransportRetry: func(tr llm.TransportRetry) {
			errText := ""
			if tr.Err != nil {
				errText = tr.Err.Error()
			}
			publishEvent(ctx, sink, runID, sessionID, event.RunEventTransportRetry, event.TransportRetryPayload{
				Retry:   tr.Retry,
				Total:   tr.Total,
				WaitMs:  tr.Wait.Milliseconds(),
				Error:   errText,
				AgentID: agentID,
				Cleared: tr.Cleared,
			})
		},
```

**Verify**：`grep -rn "appendAssistant\b\|flushInterrupted\|assistantText\|wrapContinuationPrefillLLM\|WithUnboundedBackoff" pkg/run` → 无输出；`CGO_ENABLED=1 go build ./pkg/run/` → exit 0

### Step 6：`pkg/event/run_events.go`

`TransportRetryPayload` 删除 `Exhausted`，新增 `Cleared bool \`json:"cleared,omitempty"\``；改写 `RunEventTransportRetry` 与 payload 的注释
（非 cleared = 一次重试即将发生、进行中的输出作废；cleared = 重试结束）。`IsPushOnlyStreamEvent` 不变。

**Verify**：`grep -rn "Exhausted" pkg/event pkg/run pkg/llm` → 无输出

### Step 7：`pkg/run` 测试

1. `llm_middleware_test.go`：删除 `continuationCaptureLLM` 与 `TestNetworkRetryContinuesFromTheStreamedText`；新增：
   - `TestNetworkRetryResendsTheOriginalRequest`：inner 记录收到的 messages；第 1 次流出 `OnDelta("par")` 后返回 `&net.OpError{...}`，第 2 次成功返回 `"full answer"`；
     断言两次收到的 messages 完全相同（长度 1、user）、结果文本为 `"full answer"`。
   - `TestNetworkRetryExhaustionMessage`：`wrapNetworkRetryLLM(inner, 2)`，inner 永远返回 `&net.OpError{...}`；断言 `err.Error()` 以 `network error, gave up after 2 retries: ` 开头，且 `var ne net.Error; errors.As(err, &ne)` 为 true。
     （等待 0.5s+1s 级别可接受；若嫌慢，用 `netRetryTestLLM` 并把 maxRetries 设 1。）
   - `TestNetworkRetryCapsAtFifteen`：`wrapNetworkRetryLLM(inner, 99)` 返回的 `networkRetryLLM.maxRetries == 15`。
   - 改 `TestNetworkRetryMountOrder`：两处 `notes` 现在都是 2 条——`notes[0]` 为 `Retry==1 && !Cleared && Total==3`，`notes[1].Cleared == true`。
2. `orchestration_llm_test.go`：`TestTransportRetryDropsBufferedReasoningFromTheTranscript` 断言恢复为与函数名一致：
   reasoning 行只有 1 行且含 `live attempt`；`surfaceNotes` 为 2 条（`Retry 1/3` 与 `Cleared`）。删除注释里关于 flushInterrupted 的句子。
   新增 `TestTransportRetryPersistsNoInterruptedAnswer`：inner 第 1 次 `OnDelta("dead words")` 后网络错误，第 2 次 `OnDelta("answer")` 并返回 `"answer"`；
   跑完后 `store.ListAllMessages` 的 assistant 行里没有任何一行包含 `dead words`（写法照抄上一个测试的 `newProgressStore` / `wrapToolOrchestrationLLMWithProgress` 用法）。
3. `subagent_test.go:6184-6216` `TestSubagentTransportRetryIsPublishedAndRetried`：`probe.transportRetry` 现在 2 条；
   第 1 条断言 `AgentID=="task-search-1" && Retry==1 && Total==3 && !Cleared && Error!="" && WaitMs>0`，第 2 条 `Cleared==true`；删去对 `Exhausted` 的引用。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -run 'NetworkRetry|TransportRetry' -count=1` → `ok`

### Step 8：`pkg/gateway`

1. `run_control.go:452-457`：`if !tr.Cleared { partial.Reset() }`，再链调 `publishTransportRetry(tr)`；注释同步。
2. `stream_parts.go` `observe`：在第二个 `case`（累积事件列表）里加入 `event.RunEventTransportRetry`；在后面的 `switch` 加分支：

```go
	case event.RunEventTransportRetry:
		var p event.TransportRetryPayload
		if json.Unmarshal(evt.Payload, &p) != nil || p.Cleared {
			return
		}
		if run.ownsPrimaryText(p.AgentID) {
			return
		}
		text := run.textFor(p.AgentID)
		text.content = ""
		text.reasoning = ""
```

3. `stream_parts_test.go`：`TestStreamPartsTransportRetryKeepsAccumulating` 改为 `TestStreamPartsTransportRetryDropsTheDeadAttempt`：
   delta `"partial words"` → transport_retry → delta `"full answer"` 后 `child.Content == "full answer"`；再加一个 `Cleared: true` 事件，断言不改变内容。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -run 'StreamParts|TransportRetry' -count=1` → `ok`

### Step 9：`pkg/tui`（只改语义）

1. `notify.go`：`TransportRetryMsg` 删 `Exhausted`、加 `Cleared bool`；注释同步（Cleared = 重试结束）。
   事件分支 `:884-895`：`Cleared: p.Cleared`；`Err` 只在 `p.Error != ""` 时 `errors.New(p.Error)`。
2. `chat_turn.go:498-501`：

```go
		OnTransportRetry: func(tr llm.TransportRetry) {
			if !tr.Cleared {
				accum.Reset()
			}
			s.notifyUIForSession(sessionID, TransportRetryMsg{turn: foreground, Retry: tr.Retry, Total: tr.Total, Wait: tr.Wait, Err: tr.Err, Cleared: tr.Cleared})
		},
```

3. `reducer.go` `case TransportRetryMsg:`：在 `isWithdrawn` 判断之后加 `if m.Cleared { return EventResult{} }`；删除 `if m.Exhausted { card.Final = true } else` 只留 `if m.Wait > 0 { ... }`；
   把注释“The words themselves stay: run.go seals … persisted them as the interrupted message”改为“run.go withdraws the dead attempt's live blocks”。
4. `reducer.go:5352-5392`：`SealDeadStreamBlocks` 改名 `WithdrawDeadStreamBlocks`，从尾部往前**删除**连续的非 Final `FrameAssistant`/`FrameThinking` 块（遇到 Final 块或其他 kind 即停）：

```go
	removed := false
	for n := len(vm.blocks); n > 0; n-- {
		f := vm.blocks[n-1].frame
		if f.Final || (f.Kind != FrameAssistant && f.Kind != FrameThinking) {
			break
		}
		vm.blocks[n-1] = nil
		vm.blocks = vm.blocks[:n-1]
		removed = true
	}
	if !removed {
		return
	}
```

   其余（加锁、选 vm、`activeView` 判断、重绘）保持原样；doc 注释改为“撤下死掉那次请求还在进行中的块；已写完（Final）的思考块保留”。
5. `run.go:348-354`：条件加 `!retry.Cleared`，调用改为 `renderer.WithdrawDeadStreamBlocks(retry.AgentID)`，注释同步。
6. `transport_retry_test.go`：删除 `TestTransportRetryExhaustedFreezesTheCard`；`TestSealDeadStreamBlocksKeepsTrailingLiveBlocksSealed` 改为
   `TestWithdrawDeadStreamBlocksRemovesOnlyLiveBlocks`：视图里依次有 Final thinking、非 Final assistant；调用后只剩 Final thinking。
   再加 `TestTransportRetryClearedIsANoOp`：reducer 收到 `TransportRetryMsg{Cleared: true}` 返回零帧且不清 `agentBuf`。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/ -run 'TransportRetry|WithdrawDead|RetryCard' -count=1` → `ok`

### Step 10：前端（只改语义）

1. `useChatStream.ts` 新增纯函数（放在 `closeStreamedBlocks` 旁）：

```ts
/** Removes the assistant/thinking blocks still being streamed: the attempt that streamed them died. */
function dropOpenStreamedBlocks(blocks: TimelineBlock[]): TimelineBlock[] {
  return blocks.filter((block) => !((block.kind === 'assistant' || block.kind === 'thinking') && block.open))
}
```

2. `applyTransportRetryBlocks`：开头 `if (payload.cleared) return blocks`；把 `closeStreamedBlocks(blocks, ['assistant', 'thinking'])` 换成 `dropOpenStreamedBlocks(blocks)`；改写 doc 注释（半截块撤下）。
3. 子代理 `case 'transport_retry'`（`:3359`）：开头 `if (payload.cleared) return true`。
4. 主对话 `case 'transport_retry'`（`:4659`）：

```ts
      case 'transport_retry': {
        if (payload.cleared) return
        const current = messages.value.find((message) => message.id === assistantMessageId)
        const dead = (current?.blocks ?? []).find((block) => block.kind === 'assistant' && block.open)
        if (dead && dead.kind === 'assistant' && dead.text && state.fullAnswer.endsWith(dead.text)) {
          state.fullAnswer = state.fullAnswer.slice(0, -dead.text.length)
        }
        answer.value = state.fullAnswer
        updateMessageById(assistantMessageId, (message) => ({
          ...message,
          blocks: applyTransportRetryBlocks(message.blocks ?? [], payload as Record<string, unknown>),
        }))
        return
      }
```

5. `useChatStream.test.ts`：
   - `renders the transport retry …`（`:4872`）：重试后断言**没有** `text === 'half an an'` 的 assistant 块、已关闭的 thinking 块仍在、`stream.answer.value === ''`；
     恢复后断言只有一个 assistant 块且文本为 `'and this is the rest'`；`final?.content` **不含** `'half an an'`。把用例名与注释改成“dead text withdrawn”。
   - 新增一例：先 `assistant_delta 'first round '`、再 `tool_call_started` 与 `tool_call_completed`（payload 照同文件 `:1336` 一带 `step_id: 'call-1', tool_name: 'shell'` 的写法）、
     再 `assistant_delta 'dead'`、`transport_retry`，断言 `stream.answer.value === 'first round '`（前一轮的话不丢）。
   - 子代理用例（`:4989`）：断言改为 `'dead words'` 的 assistant 块已不在。
   - 新增：`transport_retry` 带 `cleared: true` 时 blocks 不变。

**Verify**：`cd frontend && pnpm vitest run src/composables/useChatStream.test.ts` → 全部通过；`pnpm typecheck` → exit 0

### Step 11：全量回归

**Verify**：
- 本计划全部 Go 包测试（见 Commands）→ 全部 `ok`
- `go vet` → 无输出
- `gofmt -l pkg/ cmd/` → 无输出
- `grep -rn "Exhausted" pkg/llm pkg/run pkg/event pkg/gateway pkg/tui/notify.go pkg/tui/chat_turn.go` → 无输出
- `git status --short` 只列出 Scope 里的文件

## Test plan

见 Step 3、4、7、8、9、10 列出的测试。结构参照：
- `pkg/llm/retry_test.go` 现有 `stubLLM` 写法；
- `pkg/run/orchestration_llm_test.go` 的 `TestTransportRetryDropsBufferedReasoningFromTheTranscript`（`newProgressStore`、`wrapToolOrchestrationLLMWithProgress`）；
- `pkg/gateway/stream_parts_test.go` 的 `observe` 用例；
- `frontend/src/composables/useChatStream.test.ts:4872` 的 `FakeChatWebSocket` + `runEvent` 写法。

## Done criteria

- [ ] `grep -rn "ContinuationPrefill\|ContinuationMessage\|continuationPrefill\|flushInterrupted\|appendAssistant(\|WithUnboundedBackoff\|unboundedBackoff" pkg cmd` 无输出
- [ ] `gofmt -l pkg/ cmd/` 无输出
- [ ] 本计划全部 Go 包测试 `ok`，`go vet` 干净
- [ ] `pnpm vitest run src/composables/useChatStream.test.ts` 通过，`pnpm typecheck` exit 0
- [ ] 新测试存在且通过：`TestNewRetry_ClearedOnFirstDeltaAfterRetry`、`TestNewRetry_LeavesSinkAloneWithoutNotify`、`TestNewRetry_ResendsTheSameMessages`、
      `TestRetryBackoffCapsWithUpwardJitter`、`TestNetworkRetryMaxCapsAtFifteen`、`TestNetworkRetryResendsTheOriginalRequest`、`TestNetworkRetryExhaustionMessage`、
      `TestTransportRetryPersistsNoInterruptedAnswer`、`TestStreamPartsTransportRetryDropsTheDeadAttempt`、`TestWithdrawDeadStreamBlocksRemovesOnlyLiveBlocks`、`TestTransportRetryClearedIsANoOp`
- [ ] `git status --short` 只含 Scope 内文件

## STOP conditions

1. Drift check 有输出且摘录与现场不符。
2. 删除续写后，有 Scope 外的代码仍引用 `ContinuationPrefill`/`ContinuationMessage`/`Exhausted`（例如 `scripts/`、`cmd/`）——列出位置回报，不要自行扩大范围。
3. 某个 provider 适配器在回调里从**另一个 goroutine** 调 `OnDelta`/`OnReasoningDelta`，且 `go test -race ./pkg/llm/...` 报 `DATA RACE`——回报，不要加锁绕过。
4. `pkg/run` 里找不到 config 包的 import 别名，或 `slog` 未被 `runner.go` 使用且加入它会引入新依赖方向——回报。
5. 某一步验证连续两次修正后仍失败。
6. 改动需要碰 Out of scope 列出的文件。

## Maintenance notes

- `Cleared` 是状态行恢复的唯一信号来源（`116`、`117` 依赖它）；以后若给 sink 增加新的“有产出”回调（例如工具参数流），也要在 `retryLLM` 里让它触发 `clearPending`。
- `retryLLM` 只在挂了 `WithRetryNotify` 时才包装 sink；空输出重试链不挂，它的行为与 sink 完全不受本计划影响。
- 次数上限 15 是 `config.MaxNetworkRetryMax` 的唯一定义；退避上限 32s 只写在 `wrapNetworkRetryLLM`。
- 评审重点：`retryLLM.Execute` 的 `defer clearPending()` 是否覆盖所有返回路径；muted ctx 下 `OnDelta` 是否仍为 nil；web 主对话的 `fullAnswer` 只去掉死掉那段。
