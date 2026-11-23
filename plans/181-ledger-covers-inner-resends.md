# Plan 181：账本补全——内层重发入账 + `TransportRetry` 带非流式位

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **基线说明**：本计划写在 `exec/156-165` 分支 tip（`d9d0b4d`，owner ff-merge 后即 dev 祖先）。
> 它依赖该分支已落地的 plan 165（`fb_llm_attempts` 账本）与 plan 175（失败尝试带用量）。
>
> **前置（否则 STOP 1）——锚点仍在**（BRE 下含 `*` 的模式用 `grep -cF` 复核）：
> `grep -c "type TransportRetry struct" pkg/llm/stream.go` 为 `1`；
> `grep -c "func WithNonStreamingRequest" pkg/llm/nonstreaming.go` 为 `1`；
> `grep -c "nonStreamingRequestKey" pkg/llm/nonstreaming.go` 为 `3`；
> `grep -c "blankOutputRetryAttempts = 4" pkg/run/llm_middleware.go` 为 `1`；
> `grep -c "func wrapBlankOutputRetryLLM" pkg/run/llm_middleware.go` 为 `1`；
> `grep -c "func WithImageRefusalObserver" pkg/llm/image_heal.go` 为 `1`；
> `grep -c "withImageRefusalRecorder" pkg/run/image_input.go` ≥ `2`（定义+调用）；
> `grep -c "lastFailureRetry" pkg/run/attempt_ledger.go` ≥ `3`；
> `grep -c "attemptLedger := newLLMAttemptLedger" pkg/run/runner.go` 为 `1`。
>
> **Drift check（先跑，两步）**：
> ① 基线可达：`git merge-base --is-ancestor d9d0b4d HEAD && echo OK`——不可达（exec/156-165 尚未被 owner
> 合并进当前分支）即 **STOP** 报告"等待 owner 合并 exec/156-165"，**不要在旧 dev 上执行**：旧 dev 上
> `pkg/run/attempt_ledger.go` 等锚点文件根本不存在，锚点计数必失败。
> ② `git diff --stat d9d0b4d -- pkg/llm/stream.go pkg/llm/nonstreaming.go pkg/llm/retry.go pkg/llm/stream_retry.go pkg/llm/image_heal.go pkg/run/llm_middleware.go pkg/run/attempt_ledger.go pkg/run/runner.go pkg/run/image_input.go pkg/state/llm_attempts.go` 必须**无输出**。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 2）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/run ./pkg/state ./pkg/gateway -count=1`（超时给足 900 s）。

## Status

- **Priority**: P2（owner 2026-10-27 对 plan 165 开放问题 1 与 3 的裁决：立项补内层重发盲区；动 pkg/llm 带非流式位）
- **Effort**: M（一个结构字段 + 一个导出探针 + 账本计数重构 + 两个内层重发写入点 + 测试）
- **Risk**: MED（改 `TransportRetry` 公共结构与账本行编号；**不改**任何用户可见行为——重试对用户保持透明）
- **Depends on**: plan 165/175 已落地（在基线分支内）
- **Blocks**: 无（与 182 正交）
- **Planned at**: `d9d0b4d`（exec/156-165 tip），2026-10-27
- **Category**: observability（账本完整性）/ bug（数据缺口）

## Why this matters

plan 165 的账本承诺"一行 = 一次物理模型请求"，但它今天只看得见 transport 层：**blank-output 重试与
image-heal 降级重发**发生在 networkRetry/streamRetry 包装的**内层**（`llm_middleware.go` 的链序：
adapter ← imageHeal ← blankOutputRetry ← streamRetry ← networkRetry），既不产生 transport 通知、也没有别的写入点——于是"这次
回答为什么又发了一遍"在这两种最常见的重发上**没有账本行**，而它们恰是 175 实测确认的**真实重复计费来源**
（被丢弃那次的全量输入 token 已进 accumulator，重发再付一遍全量输入）。同一裁决还要求补上非流式位：
`fb_llm_attempts.streaming` 列自 165 起就存在但**恒 NULL**，因为 pkg/run 在通知里观测不到那次 attempt
是否非流式。本计划两件事一起做：`TransportRetry` 带上该位，两个内层重发点各入一行。

## Current state（全部摘自 `d9d0b4d` 现场）

**账本行编号（`pkg/run/attempt_ledger.go`）**：`llmAttemptCall` 现有 `lastFailureRetry int` 字段；
`noteFailure` 写 `Attempt: tr.Retry`，`settle` 写 `Attempt: call.lastFailureRetry + 1`。问题：blank-output
的 `NewRetry` 有自己的 1 起序号（`retry.go:261` 发 `Retry: attempt + 1`），与 streamRetry 的序号在同一
call 内**会撞号**。修法（见 Steps）：`llmAttemptCall` 改持一个**每 call 单调递增的物理请求计数器**，
所有行（transport 失败、内层重发、终局）都从它取号。**等价性的准确边界**：今天的 `tr.Retry` 只在
"该 call 的每次重试决策都发了通知"时才恰是 1,2,…连续——streamRetry 的撤回类重试（thinking-only/
stalled 分支，`stream_retry.go:216-249`）不发通知但 `counts.retries` 已 +1（`stream_failure.go:97-103`
的 `retryOr`），所以今天的 attempt 列**本来就会跳号**；改后是稠密的 1..N，这正是"不撞号"的目的。
165 的两条既有测试走的都是 overloaded→通知路径（每步都通知），数字不变、断言不用改。

**通知的 ctx（关键事实）**：两个重试循环的 notify 都拿**外层** ctx（`stream_retry.go:61`、`retry.go:187`
都是 `notifyCtx := ctx`）——streamRetry 的 `WithNonStreamingRequest` 标记只挂在 attempt 自己的
`attemptCtx` 上（`stream_retry.go:151-156`），所以 notify ctx 里读不到它；这就是 165 说"pkg/run 观测不到
非流式"的原因。但注意：**内层拿到的 ctx 就是 attemptCtx**——非流式回退的那次 attempt 走
`r.inner.Execute(attemptCtx)`，blankOutput/imageHeal 在其下游，它们的 ctx **带着标记**。

**链序（`pkg/run/llm_middleware.go:288-297`，WrapRecoverableLLM 内）**：

```go
	inner = withImageHeal(inner)                                  // :292 → llm.Use(inner, llm.NewImageHeal())
	inner = wrapBlankOutputRetryLLM(inner)                        // :293
	inner = wrapNetworkRetryLLM(inner, networkRetryMax, streamIdle, withLedger...)
```

`wrapNetworkRetryLLM` 内部还要再叠一层：`llm.NewStreamRetry(...)`（`:363`）把 retryCore 包在 inner 外。
所以请求真正走过的完整链有**四层**：**networkRetryLLM → streamRetryLLM → blankOutput retryLLM →
imageHealLLM → adapter**。`networkRetryLLM.Execute` 自己 `ctx = n.ledger.arm(ctx)`（`:404`）再调内层、
返回时 `settle`（`:406`）——armed ctx 先交给 streamRetry，streamRetry 的回退 attempt 把
`WithNonStreamingRequest` 标记放进 `attemptCtx` 再传给 blankOutput/imageHeal。这就是"streamRetry 的
notifyCtx 读不到标记、retry.go 的 notifyCtx 读得到"的结构原因（notify ctx 一节的两条 :61/:187 证据
都在这四层里）。

`wrapBlankOutputRetryLLM`（`:306-316`）今天**不注册 notify**（`retry.go:43` 的注释自己写着 "retry chains
that keep no visible state (blank-output retries) leave it unset"）：

```go
func wrapBlankOutputRetryLLM(inner llm.LLM) llm.LLM {
	retryMW, err := llm.NewRetry(
		blankOutputRetryAttempts,                                  // = 4（:111）
		llm.WithShouldRetry(llm.APIErrorIsBlankModelOutput),
		llm.WithResendGuard(),
	)
```

**image heal（`pkg/llm/image_heal.go`）**：`imageHealLLM.Execute` 在降级重发前调
`reportImageRefusal(ctx, oneLineReason(err.Error()))`；观察者经
`WithImageRefusalObserver(ctx, fn func(reason string))`（`:169`）注册，全仓唯一注册者是
`pkg/run/image_input.go:167` 的 `withImageRefusalRecorder`（闭包里已写 `RecordImageRefusal` 行 +
`RunEventImageRejected` 事件）。**缺口**：闭包捕获的是回合级 ctx，而账本的 per-call 状态
（`llmAttemptCall`）挂在 networkRetry.arm 派生的 ctx 上——闭包拿不到。recorder 的调用点在
`pkg/run/runner.go:1535`（`ctx = withImageRefusalRecorder(ctx, r, sid)`），与 loadLocked 的账本构造
（`runner.go` 后文 `attemptLedger := newLLMAttemptLedger(r.RunRT)`）同在 Runner 装配路径上——Step 4 的
`r.attemptLedger` 字段与它相邻。

**ledger 的构造（`pkg/run/runner.go` loadLocked）**：`attemptLedger := newLLMAttemptLedger(r.RunRT)` 是
局部变量，服务主链与 summary 链两处 `wrapNetworkRetryLLM(..., attemptLedger)`。

**retry.go 的发射形状（`:259-264`）**：`TransportRetry{Retry: attempt + 1, Total: maxRetries, Wait: sleep,
Err: err, Status: HTTPStatusOf(err)}`——`Retry ≥ 1`，满足账本 `noteFailure` 的 `tr.Retry > 0` 门槛。

**streaming 列已存在**（`pkg/state/llm_attempts.go` 的 `LLMAttempt.Streaming *bool`，schema v18，
nullable）——本计划**零迁移**。

## 契约（owner 裁决定死）

1. 账本一行仍 = 一次物理模型请求；行号（`call`, `attempt`）在一个 call 内**单调且不重复**，由账本自己的
   计数器分配（不再用 tr.Retry 当行号）。
2. blank-output 重发、image-heal 降级重发各留一行：`outcome='failed'`、`error` 为真实原因文本
   （blank 为 APIError 文本，image 为 refusal reason）、`status`/`wait_ms` 取通知携带值（image 行
   status=0、wait=0）、`streaming` 如实填写。**不加新列、不做迁移、不改界面**（重试对用户透明，175 契约）。
3. `TransportRetry.NonStreaming bool`：该次失败的 attempt 是否以非流式发出；仅信息位，不改变任何决策。
4. 终局行语义不变；成功行不带重发痕迹。

## Scope

**In scope（只改这些）**：
- `pkg/llm/stream.go`（`TransportRetry` 增 `NonStreaming bool` 字段 + 注释）
- `pkg/llm/retry.go`、`pkg/llm/stream_retry.go`（发射点带位；notify 语义其余不动；探针复用既有
  `NonStreamingRequestFrom`，`nonstreaming.go` 零改动）
- `pkg/llm/image_heal.go`（observer 签名 `func(reason string)` → `func(ctx context.Context, reason string)`，
  调用处传请求 ctx）
- `pkg/run/attempt_ledger.go`（计数器重构 + `noteDiscarded` + streaming 填写）
- `pkg/run/llm_middleware.go`（`wrapBlankOutputRetryLLM` 增 ledger 参数并注册**仅记账**的 notify；
  `WrapRecoverableLLM` 穿参）
- `pkg/run/runner.go`（`attemptLedger` 存为 Runner 字段，两条链与 recorder 共用同一实例）
- `pkg/run/image_input.go`（recorder 闭包改签名并补 `noteDiscarded`）
- 测试：`pkg/llm/retry_test.go`、`stream_retry_test.go`、`image_heal_test.go`（若有）、
  `pkg/run/attempt_ledger_test.go`、`pkg/run/llm_middleware_test.go`

**Out of scope（看着相关也不要动）**：
- 任何界面/文案/重试状态行（blank 重发对用户**继续**不可见：新 notify **不得**转发 `sink.OnTransportRetry`）
- schema 与 `pkg/state` 的列（streaming 列已存在；不加 kind/outcome 新值）
- `fb_runs` 的 token 口径（175 已定；本计划只让重复计费**可见**，不改数）
- `TransportRetry` 的既有字段与推送语义（Cleared 帧不带新位也无妨——账本跳过 Cleared）
- plan 182 的读取端（run 树合并）

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| llm 定向 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -run 'NonStreaming|ImageHeal' -count=1 -v` | PASS |
| run 定向 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'Ledger' -count=1 -v` | 全 PASS（含既有两条） |
| 回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/run ./pkg/state ./pkg/gateway -count=1` | 与基线相比无新增 FAIL |
| 编译/vet/gofmt | `CGO_ENABLED=1 go build -tags fts5 ./...`；`go vet -tags fts5 ./pkg/llm ./pkg/run`；`gofmt -l pkg/llm pkg/run` | exit 0 / 无输出 |

## Steps

### Step 1：`TransportRetry` 带位 + 导出探针（pkg/llm，自包含）

1. `pkg/llm/stream.go` 的 `TransportRetry` 末尾加：

```go
	// NonStreaming marks that the attempt this failure came from was sent
	// without streaming (the one-shot fallback plan 156 allows, or a retry
	// chain running under a non-streaming request). Informational only: the
	// retry decisions never read it; the attempt ledger does, to fill its
	// streaming column.
	NonStreaming bool
```

2. **不新增探针**：`pkg/llm/nonstreaming.go:29-35` 已有导出的 `NonStreamingRequestFrom(ctx) bool`——
   语义与下面要写的读法完全相同，且 4 个 adapter 已在用（`anthropic/agent_llm.go:182`、
   `google/agent_llm.go:83`、`openai/compat_llm.go:1078`、`openai/responses_llm.go:617`）。后续步骤
   一律直接 `NonStreamingRequestFrom(...)`（pkg/run 侧写 `llm.NonStreamingRequestFrom(...)`）。

3. `pkg/llm/stream_retry.go`：**每轮循环开头**（`for {` `:147` 之后、`attemptCtx := ctx` `:151` 之前）
   捕获局部 `attemptNonStreaming := nonStreaming`——此刻它正是"本 attempt 将以非流式发出"的待决标记；
   `:256` 的发射字典里加 `NonStreaming: attemptNonStreaming`。**不要**把捕获放进 `if nonStreaming`
   分支（`:152-156`）里：那里只在回退轮执行，回退把 `nonStreaming` 消费成 false（`:156`）之后再来一次
   **流式** attempt 的失败通知会带着陈旧的 true。序列自检：A1 流式失败→notify false；A2 非流式失败→
   notify true；A3 流式失败→notify false。
4. `pkg/llm/retry.go:260` 的发射字典里加 `NonStreaming: NonStreamingRequestFrom(notifyCtx)`（blankOutput
   的 retryLLM 收到的 ctx 就是 streamRetry 的 attemptCtx——非流式回退那次带着标记，见 Current state
   链序）。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -run 'TestNonStreamingFallback|TestRetryAttemptAccounting' -count=1` → 既有测试全 PASS（新位默认零值不破坏）。

### Step 2：账本计数器重构 + streaming 填写（pkg/run）

`pkg/run/attempt_ledger.go`：

1. `llmAttemptCall` 的 `lastFailureRetry int` 换成：

```go
	// nextAttempt numbers the physical requests of this call, monotonically,
	// whatever layer made them: a transport failure, a blank-output resend or
	// an image-heal discard each take the next number, so (call, attempt)
	// names one physical request with no collisions across the nested retry
	// chains. Every writer runs on the request's own goroutine — the retry
	// loops fire their notifications synchronously — so no locking is needed.
	nextAttempt int
```

加方法 `func (c *llmAttemptCall) takeAttempt() int { c.nextAttempt++; return c.nextAttempt }`。
（字段删掉后，`noteFailure` 里 `call.lastFailureRetry = tr.Retry` 的赋值行——`:88`——一并删除；
Done criteria 的 `lastFailureRetry` 计数 0 兜底。）
2. `noteFailure`：`Attempt: tr.Retry` → `Attempt: call.takeAttempt()`；并在构造 rec 处加：

```go
	streamed := !tr.NonStreaming
	rec.Streaming = &streamed
```

3. `settle`：`Attempt: call.lastFailureRetry + 1` → `Attempt: call.takeAttempt()`（其余不动）。
4. 新增（image-heal 的丢弃行）：

```go
// noteDiscarded records a request a recovery layer threw away before asking
// again — today an image the provider refused, healed by degrading it. The
// row explains a resend the transport layer never announced: status 0 (no
// HTTP answer to record), the refusal text as the error.
func (l *llmAttemptLedger) noteDiscarded(ctx context.Context, reason string) {
	call := llmAttemptCallFrom(ctx)
	if l == nil || l.store == nil || call == nil || strings.TrimSpace(reason) == "" {
		return
	}
	streamed := !llm.NonStreamingRequestFrom(ctx)
	rec := state.LLMAttempt{
		RunID:    call.runID,
		Call:     call.call,
		Attempt:  call.takeAttempt(),
		Error:    reason,
		Outcome:  state.LLMAttemptFailed,
		Streaming: &streamed,
	}
	if p := llm.StreamProgressFrom(ctx); p != nil {
		rec.ElapsedMs = p.Snapshot().Elapsed.Milliseconds()
	}
	l.record(ctx, rec)
}
```

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestLLMAttemptLedger' -count=1 -v` →
既有两条 PASS **且 attempt 值不变**（1=503 失败、2=成功）——这是"无内层重发时数字一致"的证明。若任何
既有断言的 attempt 值变了，STOP 4。

### Step 3：blank-output 重发入账

`pkg/run/llm_middleware.go`：

1. `wrapBlankOutputRetryLLM(inner llm.LLM)` → `wrapBlankOutputRetryLLM(inner llm.LLM, ledger *llmAttemptLedger)`；
   options 里加（放在 `WithResendGuard()` 之后）：

```go
		llm.WithRetryNotify(func(ctx context.Context, tr llm.TransportRetry) {
			// Ledger only: blank-output resends stay invisible to the user
			// (plan 175's transparency contract); the row is where the
			// double-billed input of a discarded attempt becomes explainable.
			if ledger != nil {
				ledger.noteFailure(ctx, tr)
			}
		}),
```

（**不得**在这里转发 `sink.OnTransportRetry`。）`WrapRecoverableLLM` 的调用点穿 ledger（variadic
`withLedger` 已有该实例）。
2. 注意 `noteFailure` 的 `tr.Retry > 0 && !tr.Cleared` 门槛会自然滤掉 retry.go 的 Cleared 帧（`:191`、`:299`）。

**Verify**：新增 `TestBlankOutputResendLeavesALedgerRow`（`pkg/run/attempt_ledger_test.go`）：fake inner
第一次返回 `&llm.APIError{StatusCode: 200, Message: "completed without output"}`（`APIErrorIsBlankModelOutput`
认这个形状——先例在 `pkg/run/llm_middleware_test.go:381`；`pkg/llm/retry_test.go` 里**没有** blank-output
用例）、第二次成功；**驱动整条链**：`wrapped := wrapNetworkRetryLLM(wrapBlankOutputRetryLLM(inner, ledger), 3, 0, ledger)`
后 `wrapped.Execute(callCtx, ...)`——造数与武装照抄 `attempt_ledger_test.go:35-59` 的形状（arm 由
networkRetry 自己做，**不要手动 arm**；`settle` 的成功行只在 `networkRetryLLM.Execute` 里写
（`llm_middleware.go:405-406`），单驱动 blankOutput 只会得到一行失败）→ `ListLLMAttempts` 得两行：
attempt 1 `outcome=failed` 且 error 含 "completed without output"、attempt 2 `outcome=success`，两行
`streaming` 非 NULL 且为 true。定向命令跑 PASS。

### Step 4：image-heal 丢弃行（observer 带 ctx）

1. `pkg/llm/image_heal.go`：`WithImageRefusalObserver` 与 `ImageRefusalObserverFromContext` 的函数类型
   `func(reason string)` → `func(ctx context.Context, reason string)`；`reportImageRefusal` 调用处传**请求
   ctx**（imageHealLLM.Execute 的那个 ctx——它派生自 armed ctx，账本状态可达）。
2. `pkg/run/image_input.go:167` 的闭包改签名；在 `RecordImageRefusal` 写行**之前**加
   `r.attemptLedger.noteDiscarded(ctx, reason)`（顺序无所谓，但放前面让"账本先于事件"与别处一致）。
3. `pkg/run/runner.go`：`loadLocked` 里 `attemptLedger := newLLMAttemptLedger(r.RunRT)` 之后存
   `r.attemptLedger = attemptLedger`（Runner 增一个字段，注释写明"同一实例服务两条链与 image recorder，
   call 序号才连续"）。

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -run 'ImageHeal' -count=1` →
既有 image heal/observer 测试（签名改后同步改调用形状，语义断言不动）全 PASS（测试名是 `TestImageHeal*`，
`image_heal_test.go:139/166/198/236`）；
`pkg/run` 新增 `TestImageRefusalLeavesALedgerRow`：fake inner 第一次返回 image-rejection 错误——形状照抄
`image_heal_test.go:43` 的 `fieldImageRejection` 常量（`&llm.APIError{StatusCode: 400, Message: fieldImageRejection}`）、
降级后成功；把 observer（`func(ctx context.Context, reason string){ ledger.noteDiscarded(ctx, reason) }`）
注册到 callCtx 上（context 值查找向上走祖先，armed ctx 能看到），**驱动整条链**
`wrapNetworkRetryLLM(withImageHeal(inner), 3, 0, ledger)`——成功行仍由 settle 写 →
账本两行，第一行 error 为 refusal reason、`streaming` 如实。PASS。

### Step 5：回归

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/run ./pkg/state ./pkg/gateway -count=1`
→ 与基线相比无新增 FAIL（特别：175 的 `TestFailedAttemptUsageReachesTheAccumulator`、165 的
`TestLLMAttemptLedgerFillsFromASupervisedRun`、156 的两条回退测试、172/159 的审批链测试）。
build/vet/gofmt 全过。

## Test plan

- `pkg/llm`：`TestTransportRetryCarriesTheNonStreamingBit`（挂 `WithRetryNotify` 到 NewStreamRetry 驱动，
  fake 形状照抄 156 的 `TestNonStreamingFallbackReturnsToStreamingAfterOneAttempt`；序列至少
  [false, true]——流式失败、回退失败；若驱动到第三次流式失败则为 [false, true, false]，用它钉死捕获
  位置在循环开头而非 `if nonStreaming` 分支内）；既有探针的单元已有
  `TestNonStreamingRequestContextSwitch`（`nonstreaming_test.go:139`），不新增；observer 新签名的既有
  测试改形状。
- `pkg/run`：Step 2 的计数器等价（既有断言不变即证）；Step 3/4 的两条新用例；
  `TestLedgerNumbersInnerResendsWithoutCollision`（一次 call 里 transport 失败 1 次 + blank 重发 1 次：
  attempt = 1,2,3 无重复——用两层 fake 叠加驱动，或分别驱动两次 call 后断言序号连续性）。
- 不新增界面测试（界面零变化）；`grep -rn "OnTransportRetry" pkg/run/llm_middleware.go` 仍只有
  networkRetry 一处转发。

## Done criteria

机器可检查，全部满足：

- [ ] `grep -c "NonStreaming bool" pkg/llm/stream.go` 为 `1`；`grep -c "NonStreaming: NonStreamingRequestFrom(notifyCtx)" pkg/llm/retry.go` 为 `1`；`grep -rn "IsNonStreamingRequest" pkg/` 为 `0`（不新增探针，复用 `NonStreamingRequestFrom`）
- [ ] `grep -c "NonStreaming: attemptNonStreaming" pkg/llm/stream_retry.go` 为 `1`；`grep -c "attemptNonStreaming := nonStreaming" pkg/llm/stream_retry.go` 为 `1`（捕获语句在，且位于循环开头）
- [ ] `grep -c "takeAttempt" pkg/run/attempt_ledger.go` ≥ `4`（定义 + 三个写入点）；`grep -c "lastFailureRetry" pkg/run/attempt_ledger.go` 为 `0`
- [ ] `grep -c "func (l \*llmAttemptLedger) noteDiscarded" pkg/run/attempt_ledger.go` 为 `1`（`-F` 复核）
- [ ] `grep -c "WithRetryNotify" pkg/run/llm_middleware.go` 为 `2`（networkRetry 既有 + blankOutput 新增）
- [ ] `grep -c "func(ctx context.Context, reason string)" pkg/run/image_input.go` 为 `1`
- [ ] 新测试全 PASS；四包回归与基线相比无新增 FAIL；build/vet/gofmt 全过
- [ ] 既有 165 两条账本测试的 attempt 断言**未被修改**（数字天然一致）
- [ ] `git status --porcelain` 只显示 Scope 里的文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点计数不符，或 drift check 有输出且"Current state"摘录与现场对不上。
2. 基线跑不起来（编译失败/环境错误）。
3. `WithImageRefusalObserver` 出现第二个注册者（`grep -rn "WithImageRefusalObserver(" --include=*.go pkg/ | grep -v _test | grep -v image_heal.go` 非
   image_input.go 一处）：签名变更波及面变大，先报告。
4. Step 2 之后既有账本测试的 attempt 值变了（计数器等价性不成立——大概率 notify 不在请求 goroutine 上
   同步触发；把失败原文贴回来）。
5. `retry.go:260` 的发射不带 `Retry ≥ 1`（门槛滤掉了 blank 行）：报告现场形状，不要改门槛。
6. 你想让 blank 重发也显示重试卡（转发 sink）：**不要**——175 定了重试对用户透明。
7. 你想加 `kind` 列区分行来源：**不要**——本计划零迁移；stats 需求出现时另立计划。
8. Runner 上无法干净地持有 `attemptLedger`（比如 loadLocked 的调用频率/并发让字段语义不清）：报告
   runner 的装配时序，先别加字段。

## Maintenance notes

- 行号语义变了：`attempt` 不再是"用户看到的 n/total"，而是 call 内物理请求序号。读取端（cost-summary、
  未来的 182 run 树合并）按行序回放即可；谁要 n/total 去读实时通知，不读账本。
- 谁将来新增"重发一层"的恢复包装（又一个 WithShouldRetry 链）：要么注册 ledger-only 的
  WithRetryNotify，要么像 imageHeal 一样给观察者传请求 ctx——**不通知账本的重发就是下一处盲区**。
- `NonStreaming` 位与 `streaming` 列：列的 NULL 从此只剩"行写在探针之前的历史行"。评审重点：blankOutput
  的 notify lambda 里**没有** sink 转发；stream_retry 的位取自消费前捕获的局部量。
