# Plan 273: 「计费了输出却什么都没吐」交给已有的空输出重试层，不再丢弃整轮

> **Executor instructions**: 按步骤顺序执行，每步验证通过再做下一步。遇到「STOP conditions」就停下报告，
> 不要自己发挥。**不要 commit / push / merge**。做完后把 `plans/README.md` 里本计划那一行的状态改成 DONE
> 并附一句摘要——除非派你来的评审说索引由他维护。
>
> **本计划硬依赖 plan 272**（执行序：272 → 270 → 271 → 273）。开工前先确认 272 已落地，三条都要满足，
> 任一条不满足就**停下**，先跑 272：
> - `grep -n 'StopReason' pkg/llm/llm.go` 有命中（`Result.StopReason` 字段）
> - `grep -n 'func NormalizeStopReason\|func OutputLimitReached' pkg/llm/output_limit.go` 两个都命中
> - `grep -c 'openAILengthContinueMax' pkg/llm/openai/compat_llm.go` → `0`
>
> **Drift check (run first)**:
> `git status --porcelain -- pkg/llm/errors.go pkg/llm/google/ pkg/run/llm_middleware.go pkg/run/orchestration_llm.go`
> 与 `git diff --stat 0a698bf2 -- <同样这些路径>`。
> → 期望：`pkg/llm/google/` 与 `pkg/run/orchestration_llm.go` 带 plan 272 的改动（272 改了 Google 的 MAX_TOKENS
> 空响应、在 `orchestration_llm.go` 基线 `:836` 之前插了撞上限分流），可能还有并行会话的改动——都属预期。
> 写计划时 `pkg/llm/errors.go`、`pkg/run/llm_middleware.go` 无未提交改动。
> （不要用 `git diff 0a698bf2..HEAD`：比的是两个 commit，未提交改动看不见。）
> 核对方式：逐条 `git show 0a698bf2:<path>` 对照本计划「Current state」的摘录；**结构对得上就继续**，
> 不要回退或改写别人的改动；对不上才 STOP。

## Status

- **Priority**: P1（owner 用 zhipuai/glm-5.3 干活时整轮被丢弃，20 分钟工作作废）
- **Effort**: S–M
- **Risk**: MED（主链路上「成功但空」的结果从此会被重发，最终可能变成错误；对所有协议生效）
- **Depends on**: `plans/272-output-cap-and-cut-off-recovery.md`（需要它的 `Result.StopReason` 区分「撞上限」——
  撞上限由 272 的续写处理，本计划必须放行；272 必须先落地）
- **Category**: bug
- **Planned at**: commit `0a698bf2`, 2026-11-07（2026-11-07 review-plan 按 owner 裁决整份重写）

## Why this matters

owner 2026-11-07 真机：`zhipuai/glm-5.3`（`max` 思考档，`https://open.bigmodel.cn/api/coding/paas/v4/chat/completions`，
OpenAI Chat Completions 协议）跑了 20 分 20 秒后报 `empty assistant response after tool execution`，整轮作废。

`plans/272` 只覆盖「provider 明确说撞上限」那一族。本计划覆盖**另一族**：provider **把输出 token 计了费、
却什么都没交回来**——没有正文、没有 tool_call。今天这类响应在 compat / Anthropic / Responses 流式里是
「成功的空消息」，一路上没有任何一层重试它，直到引擎在 `pkg/run/orchestration_llm.go:836` 把它判成
`empty assistant response after tool execution` 并丢弃整轮。

仓库其实**已经有**一层专门重发空输出的中间件（`pkg/run/llm_middleware.go` 的 `wrapBlankOutputRetryLLM`，
1 次 + 3 次重发、用户不可见、记入 `fb_llm_attempts` 账本），只是它只认得 Responses 非流式那一句
`completed without output` **错误**；「成功的空消息」不是错误，于是从它旁边漏了过去。
本计划只做一件事：把「计费了却空」的成功结果变成这层认得的错误，交给它重发。

## 已拍板的设计决定（owner 2026-11-07，review-plan 时裁决）

1. **不另起恢复机制**：并入已有的空输出重试层，原样重发同一个请求（1 + 3 次），**不加任何模型可见的提示词**。
   （原稿的「客户端层 + thinking-only 提示词 ≤3 轮」作废：那句提示词以 `Output token limit hit` 开头，
   而这一族根本没撞上限；且会与已有那层叠成最多 16 次请求。）
2. **报错文案沿用已有那层的**：`model returned a blank output; this turn was stopped. Please retry or switch to another model.`；
   `orchestration_llm.go:836` 的 `empty assistant response after tool execution` **也换成同一句**（两处共用一个常量）。
   文案不带计费 token 数、不带 finish reason。
3. **判定用事实**：结果已正常返回（`err == nil`、`res.Message != nil`）、没有 tool_call、正文 trim 后为空、
   **计费的 output token > 0**（`res.Billed().OutputTokens`）。finish reason 只用来**排除**两类已知原因，
   不用来**匹配**触发（事故那次的 finish reason 无从得知）：
   - 撞上限（`llm.OutputLimitReached(res.StopReason)`）——归 272 的续写与它在引擎里的分流；
   - provider 明说的拒答/内容过滤（Anthropic `refusal`、OpenAI `content_filter`、智谱 `sensitive`）——
     原样重发只会再被拒一次，还会把拒答伪装成「空输出」。（这一条是评审补的，不改变 owner 的裁决。）

## 复现（已在当前代码上实跑，工作树零改动）

用 `go test -overlay` 把探针挂进 `pkg/llm/openai` 包（overlay 只替换测试文件，不改仓库）：

| 喂给 compat 客户端的合成流 | 客户端返回 | 引擎会看到 |
|---|---|---|
| 无 delta，`finish_reason:"stop"`，`usage.completion_tokens=688` | `textChars=0`, `toolCalls=0`, `outTokens=688`, **err=nil** | `empty=true` → `empty assistant response after tool execution` |
| 只有 `reasoning_content` 增量，然后 stop | `textChars=29`（```` ```thinking … ``` ````） | 非空，不报错（基线已有兜底，不受本计划影响） |
| 有 `content` 增量，然后 stop | `textChars=6` | 非空，正常收尾 |

第一行就是事故那一族。现场日志的取证边界：事故那次调用的 SSE 尾部被日志 telemetry 截掉
（`[telemetry omitted 3253 bytes]`，plan 274 修）且日志块被并行会话交错写入，无法从日志反推那次的
finish reason——所以判据一律用**合成数据**，不依赖日志。

## Current state

所有行号是基线 `0a698bf2`；272 落地后 `orchestration_llm.go` 与 Google 两个文件的行号会后移，按符号找。

- **已有的空输出重试层**（`pkg/run/llm_middleware.go`，写计划时无未提交改动）：
  - `:109-111` `const blankOutputRetryAttempts = 4`（注释：首次调用 + 3 次空输出重发）。
  - `:249`–`:254` `WrapRecoverableLLM` 里的套叠顺序：`withImageHeal` → `wrapBlankOutputRetryLLM` → `wrapNetworkRetryLLM`（最外层）。
  - `:263-281`：

```go
func wrapBlankOutputRetryLLM(inner llm.LLM, ledger *llmAttemptLedger) llm.LLM {
	retryMW, err := llm.NewRetry(
		blankOutputRetryAttempts,
		llm.WithShouldRetry(llm.APIErrorIsBlankModelOutput),
		llm.WithResendGuard(),
		llm.WithRetryNotify(func(ctx context.Context, tr llm.TransportRetry) {
			// Ledger only: blank-output resends stay invisible to the user
			// (plan 175's transparency contract); ...
			if ledger != nil {
				ledger.noteFailure(ctx, tr)
			}
		}),
	)
	if err != nil {
		return inner
	}
	return llm.Use(inner, retryMW)
}
```

  - `:463-465` 与 `:486-488`：`recoverableLLM.Execute` 在重发用尽后 `if llm.APIErrorIsBlankModelOutput(lastErr) { return nil, blankOutputError(lastErr) }`。
  - `:498-503`：

```go
func blankOutputError(err error) error {
	return llm.WithUsageError(
		errors.New("model returned a blank output; this turn was stopped. Please retry or switch to another model."),
		llm.UsageFromError(err),
	)
}
```

- **它认得的只有错误文本**（`pkg/llm/errors.go:435-444`，写计划时无未提交改动）：

```go
func APIErrorIsBlankModelOutput(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(strings.TrimSpace(err.Error()))
	if s == "" {
		return false
	}
	return strings.Contains(s, "completed without output") ||
		strings.Contains(s, "empty output")
}
```

  今天只有 `pkg/llm/openai/responses_llm.go:1237-1246`（Responses 非流式 `completed` 且无输出）产出这种错误。
  `llm.WithUsageError`（`errors.go:345`）、`llm.UsageFromError`（`:362`）、`ExecuteError.Unwrap`（`:214`）、
  `MaxAttemptsExceededError.Unwrap`（`:468`）都已存在——`errors.Is` 能穿过它们。
- **重发保护**：`llm.WithResendGuard()` 用 `llm.ResendSafe(ctx)`（`pkg/llm/stream_progress.go:255`）：
  已派发工具或已展示过完整正文块就不重发。「空」的结果两者都没有，所以会被重发。
- **各协议「成功的空消息」从哪来**（这些都不用改，下面的中间件统一接住）：compat 流式/非流式经
  `compatMessageFromOpenAI`（`pkg/llm/openai/compat_llm.go:1477`）组装；Anthropic `agentAnthropicMessageFromAPI`
  （`pkg/llm/anthropic/agent_llm.go:614`，只收 `text`/`tool_use`，thinking 只进 sink）；Responses 流式
  `executeStreaming` 收尾（`responses_llm.go:1580`）。
  **思考有流出来的那一半今天已经不会是空**：compat（`compat_llm.go:1520-1533`）与 Responses 流式
  （`responses_llm.go:1568-1570`）都会把只有思考的回复包成 ```` ```thinking … ``` ```` 正文——不要动它们。
- **Google 是例外：空响应直接报错**，而不是返回空消息——`pkg/llm/google/agent_llm.go:421-425`（非流式
  `parseResponse`）与 `pkg/llm/google/stream.go:90-95`（默认的流式路径）都是
  `google: response was empty (finish reason %q)`，文本不含 `empty output`，所以已有那层不重发它。
  272 已把其中 `MAX_TOKENS` 的情形改成返回带 `StopReason` 的结果；`SAFETY`/`RECITATION` 等拒答保持报错。
- **引擎的硬错误**（`pkg/run/orchestration_llm.go:836-838`，无 tool_call 收尾分支内；272 在它之前插了撞上限分流）：

```go
			if sessionHasToolExecution(session) && isEmptyAssistantResult(res) {
				return nil, fmt.Errorf("empty assistant response after tool execution")
			}
```

  `isEmptyAssistantResult` 在 `:2675`。断言旧文案的既有测试：`pkg/run/orchestration_llm_test.go:6475`
  `TestToolOrchestrationRejectsEmptyFinalAssistantAfterToolUse`，`:6495`
  `strings.Contains(err.Error(), "empty assistant response")`。
- **中间件写法的范例**：`pkg/llm/image_heal.go:117-150` 的 `NewImageHeal` / `imageHealLLM`——只实现 `Execute` 的
  `LLMMiddleware`。照它写。`ExecuteStructured` 不经过 `recoverableLLM`（它没有 `ExecuteStructured` 方法），
  所以结构化调用不受本计划影响。
- **测试范例**：`pkg/run/llm_middleware_test.go:437` `TestBlankOutputRetryHonoursTheResendGuard`
  （直接测 `wrapBlankOutputRetryLLM`，用 `failingLLM`）；同文件 `TestRecoverableLayerPassesPartialResponsesThrough`
  （经整条 `WrapRecoverableLLM(inner, nil, &CompactChainDeps{TryCompact: …}, 3, 0)`）；
  `pkg/run/attempt_ledger_test.go:301` `stackedRetryLLM`（按调用次数返回不同结果的假 LLM）。
- 文案约定：用户/模型可见文本 provider-neutral、逐字节稳定（`FOREBRAIN.md` House rules）→ 用 `const` + 断言。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| llm 包 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... -count=1` | ok |
| run 包 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1` | ok |
| 格式 | `gofmt -l pkg/llm pkg/run` | 无输出 |

（`FOREBRAIN_HOME` 必须显式指向 mktemp 目录：不继承外层环境，仓库硬规矩。）

## Scope

**In scope**：
- `pkg/llm/blank_output.go`（新建）+ `pkg/llm/blank_output_test.go`（新建）
- `pkg/llm/errors.go`（只改 `APIErrorIsBlankModelOutput` 一个函数）
- `pkg/llm/stream_failure.go`（**2026-11-07 执行中止后扩入**：只在 `requestLevelDecision` 加空输出直通，见 Step 2b）+ 对应测试
- `pkg/llm/google/agent_llm.go`、`pkg/llm/google/stream.go`（只改空响应报错那两处）+ 对应 `_test.go`
- `pkg/run/llm_middleware.go`（`wrapBlankOutputRetryLLM` 与 `blankOutputError`）+ `pkg/run/llm_middleware_test.go`
- `pkg/run/orchestration_llm.go`（只改 `:836` 那一行错误）+ `pkg/run/orchestration_llm_test.go`（只改 `:6495` 断言）

**修订记录（2026-11-07，执行中止后）**：执行者按 STOP 条款 2 停在 Step 5——`TestBlankBilledResultStopsWithTheBlankOutputMessage`
内层被调 8 次 > 4。根因（评审已核实）：空输出重试层用尽后返回的错误无 HTTP 状态，
`requestLevelDecision`（`stream_failure.go:276`）的 `status < 400` 早退门不触发，落进
`decideStreamFailure` 的 CauseUnknown/ProgressNothing default 分支 → `withoutStreaming` 一次**非流式整链重发**
→ 内链（含 4 次空输出重发）整体再跑一遍 = 8 次物理请求。该叠层**先于本计划存在**：既有 Responses
`completed without output`（`APIError{StatusCode:200}`）走同一条路，今天全栈下同样是 8 次。
修法＝Step 2b（`requestLevelDecision` 对空输出错误直通，类比 529 的「自有一层」pass），
与 owner 设计决定 1「不叠成 16 次请求」的精神一致。

**Out of scope**（不要碰）：
- compat / Anthropic / Responses 客户端——它们返回的「成功的空消息」由新中间件统一接住，客户端不改。
- `compat_llm.go:1520-1533` 与 `responses_llm.go:1568-1570` 的 ```` ```thinking``` ```` 兜底。
- 272 的续写状态机、`OutputLimitEmptyAnswerError` 那条撞上限分流、`pkg/run/config.go`。
- `blankOutputRetryAttempts` 的次数、`WithResendGuard` 的语义、`wrapNetworkRetryLLM`。
- TUI / 网关渲染、子代理报告与 `read_file`（270/271 的边界）。

## Steps

### Step 1: 空输出哨兵错误 + 「成功但空」转错误的中间件

新建 `pkg/llm/blank_output.go`：

```go
package llm

// ErrBlankModelOutput marks a response that settled with no text and no tool
// call although the provider billed output tokens for it. Its text contains
// "empty output" on purpose: APIErrorIsBlankModelOutput recognised that phrase
// before this sentinel existed, and errors.Is now recognises the sentinel.
var ErrBlankModelOutput = errors.New("model returned empty output")

// stopReasonIsRefusal reports a finish reason with which the provider said it
// declined or filtered the answer. Re-sending the same request would be
// refused again, and calling the refusal a blank output would hide it.
func stopReasonIsRefusal(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "refusal", "content_filter", "sensitive":
		return true
	}
	return false
}

// BlankResultError returns ErrBlankModelOutput, carrying the billed usage, when
// res is a settled response that delivered nothing the turn can use: no tool
// call, no text, yet output tokens billed. A response cut by the output budget
// is not blank — its recovery is the output-limit continuation — and neither is
// a refusal. Otherwise it returns nil.
func BlankResultError(res *Result) error {
	if res == nil || res.Message == nil || len(res.Message.ToolCalls) > 0 {
		return nil
	}
	if strings.TrimSpace(res.Message.TextContent()) != "" {
		return nil
	}
	billed := res.Billed()
	if billed == nil || billed.OutputTokens <= 0 {
		return nil
	}
	if OutputLimitReached(res.StopReason) || stopReasonIsRefusal(res.StopReason) {
		return nil
	}
	return WithUsageError(ErrBlankModelOutput, billed)
}

// NewBlankResultAsError turns a blank settled response into ErrBlankModelOutput
// so the blank-output retry above it re-sends the request. Without it such a
// response is a success that nothing retries, and the engine later throws the
// whole turn away as an empty answer.
func NewBlankResultAsError() LLMMiddleware {
	return func(next LLM) LLM { return &blankResultAsErrorLLM{inner: next} }
}

type blankResultAsErrorLLM struct{ inner LLM }

func (b *blankResultAsErrorLLM) Execute(ctx context.Context, messages []Message, tools []*Tool) (*Result, error) {
	res, err := b.inner.Execute(ctx, messages, tools)
	if err != nil {
		return res, err
	}
	if blank := BlankResultError(res); blank != nil {
		return nil, blank
	}
	return res, nil
}
```

（`import` 补 `context`、`errors`、`strings`。`res.Billed()` 在 `pkg/llm/llm.go:335`：`BilledUsage` 为 nil 时回落 `Usage`——
用它而不是 `Usage`，因为多轮续写的计费在 `BilledUsage`。）

`pkg/llm/errors.go:435` 的 `APIErrorIsBlankModelOutput` 开头（`err == nil` 判断之后）加一行：

```go
	if errors.Is(err, ErrBlankModelOutput) {
		return true
	}
```

**Verify**: `CGO_ENABLED=1 go build ./...` → exit 0；`go vet ./pkg/llm/` → 无输出。

### Step 2: 接进已有的空输出重试层

`pkg/run/llm_middleware.go` 的 `wrapBlankOutputRetryLLM`（`:263`）最后一行：

```go
	// The blank-result check sits directly under the resend, so a response
	// that settled empty after billing output is re-sent like the Responses
	// "completed without output" error always was (plans/273).
	return llm.Use(inner, retryMW, llm.NewBlankResultAsError())
```

（`llm.Use(base, m1, m2)` = `m1(m2(base))`：重发层在外、转换层在内，见 `pkg/llm/llm.go:390-403`。）
`retryMW` 构造失败的回退分支 `return inner` 保持不变。

**Verify**: `CGO_ENABLED=1 go build ./...` → exit 0；
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -run 'BlankOutput|Ledger' -count=1` → ok
（既有的空输出重试与账本测试保持绿）。

### Step 2b: 空输出的耗尽错误对流式重试层是终局（不叠非流式整链重发）

`pkg/llm/stream_failure.go` 的 `requestLevelDecision`（`:276`）：在 `snap.Kind != ProgressNothing` 门之后、
`status < 400` 早退门**之前**加（`APIErrorIsBlankModelOutput` 经 `errors.Is` 能穿透
`MaxAttemptsExceededError`/`ExecuteError` 的 Unwrap）：

```go
	// A blank-output error is already owned by the blank-output retry layer
	// directly under this one (plans/273): it re-sent the request itself, and
	// this exhausted error is its verdict. Falling through to the unknown-
	// cause path would re-send the whole inner chain once more without
	// streaming — doubling every blank turn's physical requests for a
	// failure that streaming cannot fix.
	if APIErrorIsBlankModelOutput(err) {
		return streamDecision{}, true, true
	}
```

（返回形状照 529 那条 `pass` 先例：决策已定、原样上抛。）测试（`stream_failure_test.go` 若有则加表条目，
否则在 `stream_retry_test.go`）：①`MaxAttemptsExceededError` 包 `ExecuteError` 包 `ErrBlankModelOutput`
→ pass、不再进入 `withoutStreaming`；②既有 CauseUnknown（非空输出）错误在 ProgressNothing 下**仍走**
非流式回退（不得误伤）。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ -run 'StreamFailure|StreamRetry' -count=1` → ok；
随后 `TestBlankBilledResultStopsWithTheBlankOutputMessage` 内层恰 4 次（Step 5 门）。

### Step 3: Google 的「STOP 但空」也走同一条路

`pkg/llm/google/agent_llm.go`（`parseResponse` 空响应那段，基线 `:421-425`）与 `pkg/llm/google/stream.go`
（`executeStreaming` 无内容且有 finish 那段，基线 `:90-95`；272 已在这两处为 `MAX_TOKENS` 加了返回结果的分支）：
当 finish reason **等于 `STOP`（不区分大小写）且计费 output > 0** 时，错误改为包住哨兵：

```go
		return nil, llm.NewExecuteError(fmt.Errorf("google: response was empty (finish reason %q): %w", finish, llm.ErrBlankModelOutput), usage)
```

（`finish`/`usage` 用该处已有的变量：非流式是 `candidate.FinishReason`/`billed`，流式是 `st.finish`/`st.usage`；
计费判断用 `usage != nil && usage.OutputTokens > 0`——Google 的 `OutputTokens` 已含 `thoughtsTokenCount`，
`agent_llm.go:395-401`。）其它 finish reason（`SAFETY`、`RECITATION`、`BLOCKLIST`、`PROHIBITED_CONTENT`、`SPII` 等）
与计费为 0 的情形**原样不变**。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/google/ -count=1` → ok。

### Step 4: 引擎那句匿名错误换成已有文案

`pkg/run/llm_middleware.go` 把 `blankOutputError` 里的字符串提成包级常量（名字照下面写），函数改为引用它：

```go
// blankOutputMessage is what a turn ends with when the model delivered nothing
// usable: after the blank-output resends ran out, and at the engine's own
// empty-answer check. One sentence for both, so the user meets one wording.
const blankOutputMessage = "model returned a blank output; this turn was stopped. Please retry or switch to another model."
```

`pkg/run/orchestration_llm.go` 基线 `:836-838` 改为：

```go
			if sessionHasToolExecution(session) && isEmptyAssistantResult(res) {
				// A blank response that billed output was already re-sent below
				// (plans/273) and arrives here as an error, not a result. What
				// reaches this check is a response that billed nothing or was a
				// refusal; it ends the turn with the same sentence.
				return nil, errors.New(blankOutputMessage)
			}
```

（`orchestration_llm.go` 基线已导入 `errors`（`:9`），二者同在 `package run`。）

**Verify**: `CGO_ENABLED=1 go build ./...` → exit 0。此时
`go test ./pkg/run/ -run TestToolOrchestrationRejectsEmptyFinalAssistantAfterToolUse` **预期红**（它断言旧文案，
Step 5 修）——不要为了让它变绿回退本步；也不要拿整包测试当本步的门。

### Step 5: 改既有断言 + 新增测试

`pkg/run/orchestration_llm_test.go:6495`：断言改为 `err.Error() == blankOutputMessage`
（同包可直接引用常量），并加一句注释：此处的假 LLM 返回 `Usage{}`（未计费），所以不会被重发、直接到引擎。

新增 `pkg/llm/blank_output_test.go`（纯逻辑，无网络）：

| 测试 | 断言 |
|---|---|
| `TestBlankResultErrorFlagsBilledEmpty` | `Result{Message: 空 assistant, Usage: &Usage{OutputTokens: 688}, StopReason: "stop"}` → 非 nil、`errors.Is(err, ErrBlankModelOutput)`、`APIErrorIsBlankModelOutput(err)` 为真、`UsageFromError(err).OutputTokens == 688` |
| `TestBlankResultErrorUsesBilledUsage` | `Usage{OutputTokens: 0}` 但 `BilledUsage{OutputTokens: 50}` → 非 nil（多轮续写的计费在 BilledUsage） |
| `TestBlankResultErrorLeavesOthersAlone` | 逐条返回 nil：有正文；有 tool_call；`OutputTokens == 0` 且无 BilledUsage；`StopReason == "output_limit"`；`StopReason` 为 `refusal`/`content_filter`/`sensitive`；`res == nil`；`res.Message == nil` |
| `TestBlankResultAsErrorPassesErrorsAndAnswersThrough` | 内层返回错误 → 原样返回同一个错误；内层返回有正文的结果 → 原样返回同一个指针 |

`pkg/run/llm_middleware_test.go` 新增（假 LLM 照 `attempt_ledger_test.go:301` 的 `stackedRetryLLM` 写：按调用次数返回）：

| 测试 | 断言 |
|---|---|
| `TestBlankBilledResultIsResentThenAnswered` | 第 1 次返回「空 + `Usage{OutputTokens: 688}` + `StopReason: "stop"`」、第 2 次返回正文 → `wrapBlankOutputRetryLLM(inner, nil).Execute` 无错、正文是第 2 次的、内层被调 **2** 次 |
| `TestBlankBilledResultStopsWithTheBlankOutputMessage` | 内层永远返回上述空结果 → 经整条 `WrapRecoverableLLM`（构造照同文件 `TestRecoverableLayerPassesPartialResponsesThrough`，`TryCompact` 返回 `nil, false, nil`）：错误文本 == `blankOutputMessage`、内层被调 **恰 4 次**（`blankOutputRetryAttempts`；多于 4 次说明外层网络重试也在重发它，见 STOP） |
| `TestBlankUnbilledResultIsNotResent` | 内层返回「空 + `Usage{}`」→ 无错原样返回、内层被调 1 次（留给引擎判） |
| `TestOutputLimitResultIsNotResent` | 内层返回「空 + `Usage{OutputTokens: 688}` + `StopReason: "output_limit"`」→ 无错原样返回、内层被调 1 次（归 272） |

Google（照各自文件已有的 httptest 夹具）：

| 文件 | 测试 | 断言 |
|---|---|---|
| `pkg/llm/google/stream_test.go` | `TestGoogleStreamingStopEmptyIsBlankOutput` | SSE 只有 `finishReason: "STOP"` + `usageMetadata.thoughtsTokenCount: 688`、无 parts → `errors.Is(err, llm.ErrBlankModelOutput)`、错误文本仍含 `response was empty` |
| 同上 | `TestGoogleStreamingSafetyEmptyIsNotBlankOutput` | `finishReason: "SAFETY"` + 计费 → 错误**不** `Is` 哨兵 |
| `pkg/llm/google/agent_llm_test.go` | `TestGoogleStopEmptyIsBlankOutput` | 非流式同第一条 |

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/run/ -count=1` → ok。

## Done criteria

全部满足：

- [ ] `CGO_ENABLED=1 go build ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/run/ -count=1` ok
- [ ] `gofmt -l pkg/llm pkg/run` 无输出
- [ ] `grep -rn "empty assistant response after tool execution" pkg/ | grep -v _test.go` 无输出
      （测试文件里若有 272 留下的否定断言，不计入；**2026-11-07 修订**：`pkg/llm/output_limit.go:112`
      注释里引用旧文案的一处也不计入——272 写的历史说明，非任何代码在发这句，该文件不在本计划 Scope）
- [ ] `grep -c 'model returned a blank output' pkg/run/llm_middleware.go` → `1`（只剩常量定义）；
      `grep -n 'blankOutputMessage' pkg/run/orchestration_llm.go` 有命中
- [ ] `grep -n 'NewBlankResultAsError' pkg/run/llm_middleware.go` 恰 1 处
- [ ] `grep -rn 'ErrBlankModelOutput' pkg/llm/google/ | grep -v _test.go | wc -l` → `2`（流式 + 非流式）
- [ ] `grep -rnw -e StopReasonNoOutput -e ShouldRecoverNoOutput -e EmptyAnswerError pkg/` 无输出（原稿的方案未被实现；
      `-w` 按整词匹配，272 的 `OutputLimitEmptyAnswerError` 不会误中）
- [ ] `git status` 只列出 Scope 里的文件与 272 已改的文件（并行会话在 `pkg/llm` 的其它改动保持原样）

## STOP conditions

- plan 272 未落地（开头三条检查任一不满足）→ 停下先跑 272，**不要**在本计划里补 `StopReason`。
- `TestBlankBilledResultStopsWithTheBlankOutputMessage` 里内层被调用次数 > 4：说明外层（`wrapNetworkRetryLLM` /
  流式重试）也把空输出错误当成可重发——两层叠加，停下报告，由评审定谁负责。
- 既有测试除 `TestToolOrchestrationRejectsEmptyFinalAssistantAfterToolUse` 以外出现新的红
  （说明「空」的判定波及了正常收尾，例如某个协议的正常回复 `Billed().OutputTokens > 0` 却没有 `TextContent()`）
  → 停下，把那条回复的形状报告回来。
- 发现主链路以外（例如子代理、记忆抽取）也必须改才能让它们的空回复被重发——超出 Scope，报告即可。
- 同一处验证连续失败两次。

## Maintenance notes

- 判定的事实（无正文、无工具调用、计费 output>0）是刻意选定的；finish reason **只用于排除**撞上限与拒答，
  **不要**改成「按 finish reason 匹配触发」——事故那次的 finish reason 是缺失的。
- 新增一种「provider 明说拒答」的 finish reason 时，加进 `stopReasonIsRefusal`，并补一条测试。
- 与 272 的分工：272 管「撞上限 → 续写 → 用尽后引擎报 `OutputLimitEmptyAnswerError` 或带提示收尾」；
  本计划管「没撞上限却什么都没交回来 → 原样重发 → 用尽后报 `blankOutputMessage`」。两条路径互斥，
  靠的是 `BlankResultError` 对 `OutputLimitReached` 的排除——改任何一边都要看另一边。
- 重发对用户不可见（plan 175 的透明契约），只在 `fb_llm_attempts` 账本里留行；排查「为什么这一轮调用了 4 次」看账本。
- 评审要点：①`NewBlankResultAsError` 在 `retryMW` **之内**（顺序反了就不会被重发）；②计费判断用 `Billed()`；
  ③撞上限与拒答被放行；④Google 只有 `STOP` 包哨兵。
