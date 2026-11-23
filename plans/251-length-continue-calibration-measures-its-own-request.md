# Plan 251: 长度续写的校准只用「同一个请求」的实测与估算

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」就停下来报告，不要自己发挥。**不要 commit / push / merge**：改动留在工作区，
> 做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行状态改成 DONE 并附一句
> 执行摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> 1. plans/250 必须已是 DONE（`grep -n '250-length-continue' plans/README.md` 那一行写着 DONE）。不是 → STOP。
> 2. `git diff --stat 236fc32a..HEAD -- pkg/run/llm_middleware.go pkg/llm/observed.go pkg/llm/errors.go`
>    → 必须为空。
> 3. `pkg/llm/openai/compat_llm.go` 允许有 plans/250 的改动（`roundMsgs`）；用
>    `grep -n 'lastAttemptUsage\|func finishUsage\|sink.OnUsage(result.Usage' pkg/llm/openai/compat_llm.go`
>    定位下文提到的代码，内容必须与「Current state」摘录一致（行号会因 250 后移约 10 行，以内容为准）。

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW（只改校准输入与一处空指针守卫；请求形状、计费总额不变）
- **Depends on**: plans/250-length-continue-carries-one-answer.md（同一文件；且「被拒请求大小」的投影
  以 250 之后「每轮只带一份累计回答」为前提）
- **Category**: bug
- **Planned at**: commit `236fc32a`, 2026-11-06

## Why this matters

forebrain 用「实测提示词大小 − 本地估算」学一个**固定开销**（`pkg/llm/observed.go` 的 `overheads`），
之后每次估算都加上它，压缩阈值、上下文占用表都靠它。这个值是**只升不降的棘轮**，在进程生命周期内
一直有效（网关一跑就是好几天）。c7f8f5f4 修了续写把多轮提示词加总成「占用」的问题，但校准仍然
拿错了一对数：估算的是调用方发来的 `messages`，实测的却是续写**最后一轮**的提示词，而那一轮里带着
前几轮生成的回答（通常就是 max_tokens 那么多，几万 token）。一次续写就把这几万 token 永久学成
「固定开销」，之后这个供应商/模型的每次请求都提前这么多触发压缩。在 128k 窗口上，这相当于少用
四分之一的窗口。

同一个函数还有第二个错：第 N（≥2）轮请求被拒（例如上下文超限）时，传给超限校准的「被拒那一轮的
用量」其实是**上一轮、已被接受**的用量。`RecordOverflow` 把它记成「第一个放不下的大小」，于是有效
窗口被压到一个明明放得下的大小以下。另有两处小问题一并修：`finishUsage` 在一个罕见组合下会
解引用空指针（panic）；非流式带 sink 的路径把「结算用量」当成增量计费报给实时计数器，续写时实时
计数比最终 "Worked for" 少。

## Current state

### 校准的入口（`pkg/run/llm_middleware.go`）

```go
// :422 — 估算的是调用方传进来的 messages
sent := llm.EstimateRequestTokens(est, messages, tools)
res, err := r.inner.Execute(ctx, messages, tools)
if err == nil {
	r.observeSuccess(ctx, res, est, sent)
	return res, nil
}
...
// :562-569
func (r recoverableLLM) observeSuccess(ctx context.Context, res *llm.Result, est llm.Estimator, estimated int) {
	if res == nil || res.Usage == nil {
		return
	}
	provider, model := r.compactDeps.activeModel(ctx)
	realInput := res.Usage.InputTokens + res.Usage.CacheReadInputTokens + res.Usage.CacheCreationInputTokens
	llm.RecordSuccessFor(provider, model, est.Name(), realInput, estimated)
}
```

`recoverableLLM` 位于工具编排层之下、每次模型调用一层；它与供应商之间的包装（图片降级、空回复重试、
网络重试、`usageAccountingLLM`）都不改写 `Result.Usage`/`BilledUsage`。`Result.BilledUsage` 只有两处
会设置：兼容客户端的长度续写（`compat_llm.go`）与工具编排层（在 `recoverableLLM` 之上）。所以在
`observeSuccess` 里 `res.BilledUsage != nil` **恰好**表示「这次调用是多轮续写」。

### 校准本身（`pkg/llm/observed.go`，不改）

```go
// :120-150 RecordSuccessFor
	if realInput > obs.ProvenInput { obs.ProvenInput = realInput }       // 被接受的大小：窗口下界
	if obs.RejectedInput > 0 && realInput >= obs.RejectedInput { obs.RejectedInput = 0 }
	if estimated > 0 {                                                     // estimated == 0 则不学开销
		ok := overheadKey(key, estimator)
		if gap := realInput - estimated; gap > overheads[ok] { overheads[ok] = gap }  // 棘轮
	}
// :155-181 RecordOverflow(provider, model, realInput, estimated)
//   size = realInput（<=0 时用 estimated）；RejectedInput = min(已有, size)；ProvenInput >= RejectedInput 时清零
```

### 兼容客户端（`pkg/llm/openai/compat_llm.go`，行号为 `236fc32a`，250 落地后后移）

流式 `executeChatCompletionStream`：
```go
// :856-862
var settledIn, settledOut, settledCached int
// lastAttemptUsage is the settled round's own usage: nil until a round
// completed, and never a sum. Overflow calibration reads it to bound the
// window by the size of the request that was refused.
lastAttemptUsage := func() *llm.Usage {
	return openAIUsage(int64(settledIn), int64(settledOut), int64(settledCached))
}
...
for round := 0; ... {
	roundIn, roundOut, roundCached := 0, 0, 0
	...
	stream, err := c.client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		...
		return nil, llm.NewExecuteErrorWithLastAttempt(err, openAIUsage(int64(tokIn), int64(tokOut), int64(tokCached)), lastAttemptUsage())   // :882 打开失败
	}
	...
			// 流中途出错
			return nil, llm.NewExecuteErrorWithLastAttempt(err, openAIUsage(int64(tokIn), int64(tokOut), int64(tokCached)), lastAttemptUsage())   // :910
	...
	settledIn, settledOut, settledCached = roundIn, roundOut, roundCached   // :1012 一轮结束
	tokIn += roundIn; tokOut += roundOut; tokCached += roundCached
	...  // :1038 ErrStreamTruncated、:1044 ErrStreamNoEvents —— 此时 settled 已是本轮自己的，正确，不改
}
```
`:882` 与 `:910` 在第 N≥2 轮时，`lastAttemptUsage()` 返回的是**第 N−1 轮**（已被接受）的用量。
`:910` 的计费总额 `tokIn` 也漏了本轮已收到的 `roundIn`（usage chunk 可能在出错前到达）。

非流式 `executeChatCompletionUnaryParts`：`:1278-1281` 同样的 `lastAttemptUsage`；`:1289`
`CreateChatCompletion` 失败时同样返回上一轮用量；`:1291-1302` 无 choices 分支用的是本次响应自己的用量（正确，不改）。

`Execute` 的非流式带 sink 分支（`llm.NonStreamingRequestFrom(ctx)` 为真时）：
```go
// :1136-1148
result := &llm.Result{Message: &msg, Usage: usage, BilledUsage: billed}
result = finishUsage(result, finish)
prefix.Finish(resultBilledUsage(result))
if sink != nil && result.Usage != nil && (result.Usage.InputTokens > 0 || result.Usage.OutputTokens > 0) {
	if sink.OnUsage != nil {
		sink.OnUsage(result.Usage.InputTokens, result.Usage.OutputTokens)          // ← 增量计费却给了结算值
	}
	if sink.OnUsageSnapshot != nil {
		sink.OnUsageSnapshot(result.Usage.InputTokens, result.Usage.OutputTokens)  // 占用快照：结算值，正确
	}
}
```
`pkg/llm/stream.go:26-34`：`OnUsage` 是「per-run/session token counters 的增量」，`OnUsageSnapshot` 是
「当前请求的绝对占用」。`pkg/run/llm_middleware.go` 的 `internalLLMUsageObserver`：供应商一旦调过
`OnUsage`（`seen=true`），最终的计费补报 `reportFinalIfNeeded(billed)` 就不再发——所以这里报错值不会被纠正。

`finishUsage`（:1179-1196）：
```go
settled, billed := result.Usage, result.BilledUsage
result = finish(result)
if result == nil || billed == nil {
	return result
}
raw := result.Usage
result.Usage = settled
if raw != nil && *raw != *settled {      // ← settled 为 nil 时 panic
	result.BilledUsage = raw
}
return result
```
`settled` 为 nil 而 `billed` 非 nil 的组合：至少两轮、最后一轮没报 prompt、所有轮 completion 都报 0
（`openAIUsage` 全零时返回 nil，见 `pkg/llm/openai/responses_llm.go:323-341`）。罕见，但一旦出现就是 panic。

### 现有测试范例

- 校准：`pkg/run/compaction_test.go:731` `TestObservationRecordedUnderActiveEstimator`——
  `llm.ResetObservedForTest()`、`llm.SetEstimatorForTest(provider, model, fakeNamedEstimator{})`、
  `WrapRecoverableLLM(&observedUsageLLM{input: real}, nil, &CompactChainDeps{ActiveModel: ...}, 0, 0)`、
  用 `llm.ProjectFor(provider, model, "fake", x) - x` 读学到的开销。`observedUsageLLM` 定义在 `:664`，
  `fakeNamedEstimator` 在 `:723`。
- 超限：`pkg/run/llm_middleware_test.go:572` `TestObserveOverflowLearnsTheRejectedAttemptNotTheMergedTotal`
  （用 `llm.Get(...)` 读 `RejectedInput`）。
- 续写 httptest：`pkg/llm/openai/compat_llm_test.go:521` `TestLengthContinueReportsSettledRequestPrompt`，
  以及 plans/250 新增的 `TestLengthContinueCarriesOneAnswerPerRound`（服务器写法可复用）。
  `promptOf(u)`（同文件，紧随 :521 的测试之后）= `InputTokens + CacheReadInputTokens + CacheCreationInputTokens`。
- 约定：注释为完整英文句子、解释「为什么」；测试名用句子式驼峰（见上面几个）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| llm 测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... -count=1` | 全部 `ok` |
| run 聚焦 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'Observ\|Overflow\|LengthContinue\|Usage' -count=1` | `ok` |
| run 全包 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` | `ok` |
| vet | `PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" CGO_ENABLED=1 go vet -tags fts5 ./pkg/llm/... ./pkg/run` | exit 0 |
| 格式 | `gofmt -l pkg/llm pkg/run` | 无输出 |

注意 Go 正则里 `-run` 的「或」直接写 `|`（在单引号里）；上表的 `\|` 是 Markdown 表格转义，**命令行里写
`-run 'Observ|Overflow|LengthContinue|Usage'`**。`ld: warning: ... newer 'macOS' version` 是已知噪音。

## Scope

**In scope**：
- `pkg/run/llm_middleware.go`（只改 `observeSuccess`）
- `pkg/run/compaction_test.go` 或 `pkg/run/llm_middleware_test.go`（新增测试，放在对应范例旁）
- `pkg/llm/openai/compat_llm.go`（`lastAttemptUsage` 的两处使用点、`:910` 的计费总额、`finishUsage`、非流式带 sink 分支的 `OnUsage`）
- `pkg/llm/openai/compat_llm_test.go`（新增测试）

**Out of scope**：
- `pkg/llm/observed.go`：棘轮语义、`RecordOverflow` 的「取最小」都不改。
- 续写后 `Result.Usage.OutputTokens` 是否该只算最后一轮——那是 plans/252 的测量问题，本计划**不碰**
  `out.Usage = openAIUsage(int64(settledIn), int64(tokOut), int64(settledCached))` 这两行。
- `:1038`/`:1044` 两个返回：那时本轮已结算，`lastAttemptUsage()` 正确。
- 流式路径的 `OnUsage`（按 chunk 增量报，本来就是计费口径）。

## Steps

### Step 1: 多轮调用不学开销（`observeSuccess`）

把 `observeSuccess` 改成：

```go
func (r recoverableLLM) observeSuccess(ctx context.Context, res *llm.Result, est llm.Estimator, estimated int) {
	if res == nil || res.Usage == nil {
		return
	}
	provider, model := r.compactDeps.activeModel(ctx)
	realInput := res.Usage.InputTokens + res.Usage.CacheReadInputTokens + res.Usage.CacheCreationInputTokens
	if res.BilledUsage != nil {
		// A length-continue made several requests and the usage it settled on
		// is the last one, which carries the answer the earlier rounds produced
		// on top of messages. estimated sized messages alone, so the gap between
		// the two is that answer, not the provider's framing — and the overhead
		// is a ratchet that would keep it for every later request of this model.
		// The settled size was still accepted, which is all the window needs.
		estimated = 0
	}
	llm.RecordSuccessFor(provider, model, est.Name(), realInput, estimated)
}
```

在 `TestObservationRecordedUnderActiveEstimator` 旁新增
`TestLengthContinueTeachesTheWindowButNoOverhead`：
- 照它的准备方式（同一 provider/model 常量换个名字避免串扰，如 `"lc-provider"`/`"lc-model"`；
  `ResetObservedForTest` + `t.Cleanup`；`SetEstimatorForTest(..., fakeNamedEstimator{})`）。
- 新 fake：`type lengthContinueUsageLLM struct{ settled, billed int }`，`Execute` 返回
  `Usage: &llm.Usage{InputTokens: m.settled}`、`BilledUsage: &llm.Usage{InputTokens: m.billed}`。
- `real := 90000`，`WrapRecoverableLLM(&lengthContinueUsageLLM{settled: real, billed: 2 * real}, nil, &CompactChainDeps{ActiveModel: ...}, 0, 0).Execute(ctx, msgs, nil)`。
- 断言：`llm.ProjectFor(provider, model, "fake", 1000) - 1000 == 0`（没学开销）；
  `obs, _ := llm.Get(provider, model); obs.ProvenInput == real`（窗口下界照记）。

**Verify**: run 聚焦命令 → `ok`。变异检查：注释掉 `estimated = 0` 那行 → 新测试红；还原 → 绿。

### Step 2: 第 N 轮被拒时报「被拒请求」的大小

流式函数里，在 `lastAttemptUsage` 定义之后新增：

```go
// refusedRoundUsage is the size of the request the current round sent, for a
// round that failed: its own reported usage when the provider sent some, or
// else what it re-sent — the previous round's prompt plus the answer that
// round produced, which this round carries. Nil on a first round that reported
// nothing, so overflow calibration falls back to its estimate. It is never the
// previous round's own usage: that request was accepted, and recording it as
// refused would cap the window below a size that fit.
refusedRoundUsage := func(roundIn, roundOut, roundCached int) *llm.Usage {
	if roundIn > 0 {
		return openAIUsage(int64(roundIn), int64(roundOut), int64(roundCached))
	}
	return openAIUsage(int64(settledIn+settledOut), 0, int64(settledCached))
}
```

- 打开失败（原 `:882`）：最后一个参数改为 `refusedRoundUsage(roundIn, roundOut, roundCached)`（此时三者为 0，即走投影）。
- 流中途出错（原 `:910`）：计费总额改为 `openAIUsage(int64(tokIn+roundIn), int64(tokOut+roundOut), int64(tokCached+roundCached))`，
  最后一个参数改为 `refusedRoundUsage(roundIn, roundOut, roundCached)`。
- 更新 `lastAttemptUsage` 上方注释：它现在只用于「本轮已结算后」的失败（`ErrStreamTruncated`/`ErrStreamNoEvents`），删掉
  "Overflow calibration reads it to bound the window by the size of the request that was refused" 这句（那是 `refusedRoundUsage` 的职责）。

非流式函数里新增同样的 `refusedRoundUsage`（无参数版即可：`func() *llm.Usage { return openAIUsage(int64(settledIn+settledOut), 0, int64(settledCached)) }`，
注释写 "See executeChatCompletionStream."），`CreateChatCompletion` 失败（原 `:1289`）改用它。无 choices 分支不动。
若非流式函数里 `lastAttemptUsage` 因此只剩无 choices 分支一个调用点，保留它。

在 `compat_llm_test.go` 新增 `TestLengthContinueRefusedRoundReportsTheRequestItSent`，子测试 `stream` / `unary`：
- 服务器第 1 次请求：回 `reasoning_content: "R1 "`、`finish_reason: "length"`、`usage: {prompt_tokens: 1000, completion_tokens: 200}`
  （流式按 SSE、非流式按 JSON，写法同 plans/250 的测试）。
- 第 2 次请求：`w.Header().Set("Content-Type", "application/json")`、`w.WriteHeader(http.StatusBadRequest)`、
  body `{"error":{"message":"This model's maximum context length is exceeded","type":"invalid_request_error","code":"context_length_exceeded"}}`。
- `_, err := client.Execute(...)` 必须非 nil。
- 断言：`promptOf(llm.LastAttemptUsageFromError(err)) == 1200`（第 1 轮提示词 + 第 1 轮输出）；
  `promptOf(llm.UsageFromError(err)) == 1000` 且 `llm.UsageFromError(err).OutputTokens == 200`（计费 = 已完成的第 1 轮）。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/openai -run 'LengthContinue' -count=1` → `ok`。
修改前先跑一次新测试，应红（`LastAttemptUsageFromError` 给出 1000）。

### Step 3: `finishUsage` 空指针守卫

```go
raw := result.Usage
result.Usage = settled
if raw != nil && (settled == nil || *raw != *settled) {
	result.BilledUsage = raw
}
```

新增 `TestLengthContinueWithoutASettledFigureDoesNotPanic`（流式）：第 1 次请求 SSE 回
`reasoning_content: "x"`、`finish_reason: "length"`、`usage: {prompt_tokens: 500, completion_tokens: 0}`；第 2 次请求回
`content: "done"`、`finish_reason: "stop"`、**不带 usage**。客户端用 `newOpenAICompatLLM(..., []byte(`{"stream":true}`))`（生产构造器，
装有 `llm.CacheUsageTransport`）。断言：`Execute` 不 panic、无错误；`promptOf(res.Billed()) == 500`。
修改前先跑一次：预期 panic（`nil pointer dereference`）。若修改前就不 panic，保留测试与守卫，在报告里注明「未复现 panic」。

**Verify**: `... go test -tags fts5 ./pkg/llm/openai -run 'LengthContinue' -count=1` → `ok`。

### Step 4: 非流式带 sink 分支按计费口径报增量

把 `Execute` 非流式带 sink 分支（原 `:1141-1148`）改为：

```go
if sink != nil && result.Usage != nil && (result.Usage.InputTokens > 0 || result.Usage.OutputTokens > 0) {
	if sink.OnUsage != nil {
		// OnUsage feeds the run's additive counters, which are a bill: a
		// length-continue's every round. The snapshot below is the request the
		// conversation stands at.
		billed := result.Billed()
		sink.OnUsage(billed.InputTokens, billed.OutputTokens)
	}
	if sink.OnUsageSnapshot != nil {
		sink.OnUsageSnapshot(result.Usage.InputTokens, result.Usage.OutputTokens)
	}
}
```

（单轮调用 `Billed()` 就是 `Usage`，行为不变。）新增 `TestNonStreamingLengthContinueReportsTheBillToTheCounters`：
非流式服务器两轮（`length` 后 `stop`，`prompt_tokens` 100 / 150、`completion_tokens` 10 / 20）；
`ctx := llm.WithStreamSink(llm.WithNonStreamingRequest(context.Background()), &llm.StreamSink{OnUsage: func(in, out int){...累加...}, OnUsageSnapshot: func(in, out int){...记最后一次...}})`；
`client := newOpenAICompatLLM(..., nil)`。断言：`OnUsage` 累加的输入 == 250、输出 == 30；`OnUsageSnapshot` 最后一次的输入 == 150。

**Verify**: `... ./pkg/llm/openai -run 'LengthContinue|NonStreaming' -count=1` → `ok`。

### Step 5: 全量

**Verify**: llm 测试全 `ok`；run 全包 `ok`；vet exit 0；`gofmt -l pkg/llm pkg/run` 无输出。

## Test plan

- `pkg/run`：`TestLengthContinueTeachesTheWindowButNoOverhead`（含变异检查）。
- `pkg/llm/openai`：`TestLengthContinueRefusedRoundReportsTheRequestItSent`（流式 + 非流式）、
  `TestLengthContinueWithoutASettledFigureDoesNotPanic`、`TestNonStreamingLengthContinueReportsTheBillToTheCounters`。
- 回归：`TestObservationRecordedUnderActiveEstimator`（单轮仍学开销）、`TestObserveOverflowLearnsTheRejectedAttemptNotTheMergedTotal`、
  `TestLengthContinueReportsSettledRequestPrompt`、plans/250 的 `TestLengthContinueCarriesOneAnswerPerRound` 全部不改、全绿。

## Done criteria

- [ ] llm 全包、run 全包测试 `ok`
- [ ] Step 1 变异检查观察到红→还原后绿；Step 2 修改前新测试红
- [ ] vet exit 0；`gofmt -l pkg/llm pkg/run` 无输出
- [ ] `git diff --stat -- pkg/llm/observed.go pkg/llm/errors.go` → 空
- [ ] `grep -n 'out.Usage = openAIUsage(int64(settledIn), int64(tokOut)' pkg/llm/openai/compat_llm.go` 与
      `grep -n 'settled := openAIUsage(int64(settledIn), int64(tokOut)' pkg/llm/openai/compat_llm.go` 各仍命中 1 行（本计划没碰 252 的范围）
- [ ] `git status --short` 只列出 Scope 内文件（`plans/` 被 `.gitignore` 忽略，不会出现）
- [ ] `plans/README.md` 本计划行状态更新

## STOP conditions

- plans/250 未完成，或 drift check 对不上。
- `grep -rn 'BilledUsage *=' pkg --include='*.go' | grep -v _test` 显示除 `compat_llm.go` 与 `orchestration_llm.go` 之外还有地方设置
  `BilledUsage`（那样 Step 1 的「`BilledUsage != nil` 恰好表示续写」不再成立，需重新设计判据）。
- Step 2 的 400 响应没有让 `Execute` 返回带 `LastAttemptUsageFromError` 的错误（例如被包成别的错误类型）——报告实际错误链。
- 任何现有测试因本计划变红。

## Maintenance notes

- 若将来别的适配器也做「一次调用多次请求」，必须同样设置 `BilledUsage`，否则 Step 1 的保护对它无效。
- 若将来想让续写也参与开销学习，正确做法是让客户端把**第 1 轮**的实测随 Result 带出来、与 `sent` 配对，
  而不是拿最后一轮去比。
- 评审重点：`refusedRoundUsage` 永远不返回「已被接受的请求」的大小；`:1038/:1044` 没被改。
