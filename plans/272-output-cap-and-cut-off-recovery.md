# Plan 272: 输出上限不再是 8192，撞上限自动续写（思考被截断不再整轮报错）

> **Executor instructions**: 按步骤顺序执行，每步验证通过再做下一步。遇到「STOP conditions」就停下报告，
> 不要自己发挥。**不要 commit / push / merge**。做完后把 `plans/README.md` 里本计划那一行的状态改成 DONE
> 并附一句摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git status --porcelain -- pkg/run/config.go pkg/run/orchestration_llm.go pkg/llm/` 以及
> `git diff --stat 0a698bf2 -- pkg/run/config.go pkg/run/orchestration_llm.go pkg/llm/`
> → **大概率非空**：`pkg/llm/{llm.go,stream.go,retry.go,stream_retry.go,openai/*,anthropic/agent_llm.go}`
> 上有未提交改动——并行会话的 `ResponseStarted`/`ReasoningModel` 那条线，或本计划已被先行执行过一部分。
> （`git diff 0a698bf2..HEAD` 比的是两个 commit，**看不到未提交改动**，基线即 HEAD 时实测恒为空——不要用它下结论。）
> 核对方式：逐条用 `git show 0a698bf2:<path>` 取基线，与「Current state」的摘录比对；**结构对得上就可以继续**，
> 不要回退或改写别人的改动，也不要因为文件"有改动"就 STOP。
> 出现下列之一才 STOP：`llm.Result` 已有人加了 `StopReason` 字段；`compat_llm.go` 里的
> `openAILengthContinueMax` 循环已被删除或语义改变；`pkg/run/config.go` 的 `maxTokens` 计算点数量变了。

## Status

- **Priority**: P0（主代理当前无法用 opencode-go/qwen3.8-max 干活）
- **Effort**: L
- **Risk**: MED–HIGH（四个协议客户端 + 引擎的错误语义；关掉续写时行为必须与今天逐字节一致）
- **Depends on**: 无（与 plan 263 的行级 `MaxOutputTokens` 组合，见 Step 1 的注）
- **Category**: bug
- **Planned at**: commit `0a698bf2`, 2026-11-07

## Why this matters

owner 2026-11-07 真机：主代理用 `opencode-go/qwen3.8-max`（`xhigh`），
endpoint `https://opencode.ai/inference/go/anthropic/v1/messages`（Anthropic Messages 协议）跑了 6 分钟后：

```
Thought for 3m 43s        ← 思考流式输出，界面还在涨
...
error
empty assistant response after tool execution
— Worked for 50m 15s · first token 4.9s · 18:51
```

另一次同样（`Worked for 6m 05s`）。思考过程被截断、随后整轮报错，工作全部作废。

已定位的完整因果链（每一环都在当前代码里核实过）：

1. **输出上限写死 8192**。`pkg/run/config.go:590` 的 OpenCode 分支用
   `openai.DefaultMaxCompletionTokens`（`pkg/llm/openai/compat_llm.go:24` = 8192）；
   `:415`（chatgpt）与 `:433`（其它）也是先取这个默认，查不到目录再回落 8192。
2. **思考 token 计入 `max_tokens`**。Anthropic 协议里 thinking 与正文共用输出预算；
   `xhigh` 把 8192 全用在思考上 → `stop_reason = max_tokens`，没有 text，也没有 tool_call。
3. **thinking 块被丢弃**。`pkg/llm/anthropic/agent_llm.go:618` `agentAnthropicMessageFromAPI` 只收集
   `text` / `tool_use`，thinking 只经 sink 显示（`:317-352`），不进入 `llm.Message`。
   于是这条响应在引擎眼里是"空回复"。
4. **引擎把它当错误**。`pkg/run/orchestration_llm.go:836`：
   `if sessionHasToolExecution(session) && isEmptyAssistantResult(res) { return nil, fmt.Errorf("empty assistant response after tool execution") }`。
   `isEmptyAssistantResult`（`:2675`）只看 text 与 toolCalls。
5. **引擎无从判断是"撞上限"**。`llm.Result`（`pkg/llm/llm.go:303`）没有 stop reason 字段；
   Anthropic 客户端只在 `:301-305` 把 `stop_reason` 交给 `sink.OnStopReason`（只影响进度显示）。
6. **只有 Chat Completions 会自救**。`pkg/llm/openai/compat_llm.go:23`
   `openAILengthContinueMax = 16` + `:887`/`:1318` 两处预填续写循环；Anthropic / Google / Responses
   都没有 —— 同一件事在四个协议上的行为完全分裂。

对照本机 Claude Code 2.1.301（逆向 `claude.exe` 核实）：输出上限默认取模型表（不认识则 **32000**，
上限 128000）；撞上限不算失败，而是带一句固定的续写提示再请求一次，最多 **3** 次
（`Output token limit hit. Resume directly — no apology, no recap of what you were doing. Pick up mid-thought
if that is where the cut happened. Break remaining work into smaller pieces.`）；
若是在思考阶段就撞线，换成另一句（"…that reasoning was not kept and is not part of this conversation,
so there is nothing to resume. Continue from the conversation above: reason much more briefly…"）。

## 已拍板的设计决定（owner 2026-11-07）

1. **默认上限**：`min(目录 limit.output, 32000)`；目录查不到 → **32000**。
   行级 `MaxOutputTokens`（plan 263 引入）**优先**，并且**同样适用于 OpenCode 分支**。
2. **续写层在 LLM 客户端内部**（不是 orchestration 主循环）：四个协议共用一个「续写状态机」，
   最多 3 轮，**删除** compat 那套 16 轮预填。
   理由（写进代码注释）：`llm.IsMeta` 的 user 消息**永不到达 API**（`pkg/llm/llm.go:177`），
   所以在主循环里追加的提示词会作为真实 user 消息显示在 TUI / 网页；而且每轮是独立 assistant 消息，
   `pkg/process/one_shot.go:20` 的子代理输出、`agent.Result.Parts` 的主代理最终回复都只取最后一条，
   多轮会把前半截丢掉。放在客户端里则只产生一条合并后的 `llm.Result`：界面零可见噪音、文本天然完整、
   用量按轮累加（compat 今天就是这么做的）。

## Current state

- `pkg/run/config.go:415` / `:433` / `:590` —— 三处 `maxTokens` 计算：

```go
	maxTokens := openai.DefaultMaxCompletionTokens
	if hit, ok := llm.Lookup(prov, model); ok && hit.DefaultMaxTokens > 0 {
		maxTokens = int(hit.DefaultMaxTokens)
	}
```

  `:590`（OpenCode）**不做目录查询**，注释说"OpenCode 表只有上下文窗口"——
  该理由已过时：`llm.Lookup` 按 `JoinProviderModel(provider, model)` 拼 `opencode-go/<model>` 键查
  「出厂目录 + home 覆盖层」的合并结果（`pkg/llm/catalog.go:600`、`:468`、`:520`——home 覆盖层按字段合并、
  缺的模型回落出厂行，不存在"home 副本过旧查不到"），而 `assets/models.json` 里 `opencode-go/qwen3.8-max`
  这行就带 `limit.output: 131072`（`opencode*` 参考行共 164 条，大多带 output 上限）。
- `pkg/llm/llm.go:303` `type Result struct { Message *Message; Usage *Usage; BilledUsage *Usage; Attempts int; Session []Message }`
  —— 没有 stop reason。
- `pkg/llm/anthropic/agent_llm.go:163-175` 请求构造（`MaxTokens: c.maxTokens`、`OutputConfig.Effort`）；
  `:300-306` `stop_reason` 只给 sink；`:614-636` thinking 不进 `Message`。
- `pkg/llm/openai/compat_llm.go:25` `openAILengthContinueMax = 16`（`:24` `DefaultMaxCompletionTokens = 8192`）；
  `:33-41` `isOpenAILengthTruncatedFinish`；`:43-50` `openAIReasoningLengthShouldContinue`；
  `:887`/`:1315` 两个续写循环（`req.Messages = roundMsgs` 就是这个预填，两个 helper 的调用点
  `:1071`/`:1354` 在循环体内）。
- **每个协议都有「流式 + 非流式」两条执行路径，续写必须两条都覆盖**（`llm.NonStreamingRequestFrom(ctx)`
  为真时走非流式，是流式失败后的重发路径；默认走流式）：
  - Anthropic：`Execute`（`:155`）在 `:182-184` 遇非流式开关就 `return c.executeUnary(ctx, params)`（`:403`），
    否则在 `Execute` 体内直接跑流式；两条路径各自 `defer sink.OnEnd()`（`:191`、`:408`）。
    非流式路径的 stop reason 是 `msg.StopReason`（`:458` 已用于 replay）。
  - Chat Completions：`Execute`（`:1121`）三个分支——非流式开关（`:1139-1181`，调
    `executeChatCompletionUnaryParts`）、`req.Stream`（`:1182-1187`，调 `executeChatCompletionStream` `:838`）、
    其余（`:1188`，调 `executeChatCompletionUnary` `:1277`）。**`ExecuteStructured`（`:1233`，记忆抽取
    `pkg/memory/pipeline.go:287`、`pkg/run/memory_llm.go:132` 等在用）也走 `:1265` `executeChatCompletionUnary`
    → `executeChatCompletionUnaryParts`**，所以今天的结构化输出同样带着 16 轮预填续写。
    流式路径成功时在整个续写循环结束后只调一次 `sink.OnEnd()`（`:1085-1087`）——多轮续写的 sink 约定以它为准。
  - Responses：`Execute`（`:611`）三个分支——流式 `executeStreaming`（`:617-619`，`:1293`）、非流式开关
    `executeSyncDetail`（`:620-658`）、配置为非流式 `executeSync`（`:660`）；`ExecuteStructured`（`:664`）
    直接调 `executeSync`（`:688`）。`executeSyncDetail`（`:1183`）返回 `responses.ResponseStatus`；`:1237`
    只在 `completed` 且无输出时报 `completed without output`。**流式路径的事件 switch（`:1464` 起）只处理
    `error`/`response.failed`/`response.completed`，没有 `response.incomplete` 分支**：撞上
    `max_output_tokens` 时流以 `response.incomplete` 结束，`sawCompleted` 为假、`usage` 只在 completed 分支赋值
    （`:1478-1497`），于是今天要么带着 nil usage 返回半截正文（`:1580`），要么在 `:1547` 判成
    `ErrStreamNoEvents`。SDK（`github.com/openai/openai-go v1.12.0`）的 `Response.IncompleteDetails.Reason`
    取值为 `"max_output_tokens"` 或 `"content_filter"`——**只有前者是撞上限**。
  - Google：`Execute`（`pkg/llm/google/agent_llm.go:71`）遇非流式开关走 `executeUnary`（`:89-117`，
    内含 `defer sink.OnEnd()` `:91`）→ `parseResponse`（`:386`）；**否则 `:119` 走
    `pkg/llm/google/stream.go:21` 的 `executeStreaming`——这才是默认主路径**（`:35` 也 `defer sink.OnEnd()`；
    `:183` `st.finish = candidate.FinishReason`；`:90-95` 无内容时：无 finish → `ErrStreamNoEvents`，有 finish →
    `google: response was empty (finish reason %q)`）。非流式 `parseResponse` 把 `candidate.FinishReason`
    返回给调用方（`:429`），空响应时 `:421-425` 同样报 `response was empty`。
- **用量口径**（`pkg/llm/llm.go:303` `Result` 的字段注释）：`Usage` 是「对话此刻站在的那次请求」（占用率、压缩阈值读它），
  `BilledUsage` 是「本次调用的全部物理请求」（账本、计费读 `Billed()`）。compat 多轮续写的现行做法
  （`pkg/llm/openai/compat_llm.go:1114-1117`）：`Usage = (最后一轮 input, 各轮 output 之和, 最后一轮 cached)`，
  `BilledUsage = 各轮全部相加`（只在多于一轮时设置）。
- `pkg/llm/stream.go:59-66` `OnStopReason`；`:120-121` `ResponseNoticeIncomplete`；
  `pkg/llm/stream_progress.go:225` 包了一层。
- `pkg/run/orchestration_llm.go:817-843` no-tool-call 收尾分支（`incomplete` 处理 + 空回复硬错误）；
  `:2675` `isEmptyAssistantResult`。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| llm 包 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... -count=1` | ok |
| run 包 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1` | ok |
| 格式 | `gofmt -l pkg/llm pkg/run` | 无输出 |

## Scope

**In scope**：
- `pkg/llm/output_limit.go`（新建）+ `pkg/llm/output_limit_test.go`（新建）
- `pkg/llm/llm.go`（`Result` 加字段）
- `pkg/llm/anthropic/agent_llm.go`、`pkg/llm/openai/compat_llm.go`、`pkg/llm/openai/responses_llm.go`、
  `pkg/llm/google/agent_llm.go`、`pkg/llm/google/stream.go`（各自接续写；Google 的默认流式路径在 `stream.go`）
- `pkg/run/config.go`（上限解析）
- `pkg/run/orchestration_llm.go`（撞上限时的收尾语义）
- 对应 `_test.go`

**Out of scope**：
- `IsMeta` 语义、主循环的消息注入、`pkg/agent` 的 `Result.Parts` 合并。
- `ExecuteStructured`（记忆抽取等结构化调用）——**不续写**。注意这是一处**有意的行为变化**：compat 的
  `ExecuteStructured` 今天经 `executeChatCompletionUnaryParts` 带 16 轮预填续写（见 Current state）；删掉预填后
  它只跑一轮、把 `StopReason` 记下来交回调用方。理由：结构化 JSON 只能靠「预填同一段 JSON 往下接」续上，
  「assistant 片段 + 续写提示」会让模型重起一段 JSON，拼起来必然解析失败；而上限从 8192 抬到 32000 后，
  结构化输出撞线已极少见。Responses 的 `ExecuteStructured` 直接调 `executeSync`，续写循环放在 `Execute`
  里（Step 4）它就天然不受影响。
- 思考档位本身的实现、`/model` 选择器、`pkg/tui` 与 `pkg/gateway` 的渲染。
- plan 263 的表结构/覆盖层（本计划只调用它的 `MaxOutputTokens`，若 263 未落地就传 0）。

## Steps

### Step 1: 上限解析（一处判定）

`pkg/llm/output_limit.go` 新建：

```go
// DefaultOutputTokenCap is the output budget when neither the model's row nor
// the catalog names one. Claude Code uses the same number for models its table
// does not describe; 8192 (the old default here) leaves too little room for a
// reasoning model, whose thinking shares the output budget with its answer.
const DefaultOutputTokenCap = 32000

// OutputTokenCap resolves the max_tokens one call may ask for. rowMax is the
// per-row override a user set (0 when none): an explicit setting always wins.
// Otherwise the catalog's limit.output is used, capped at the default — a
// 128k-output model still gets 32k, because the cap protects the context
// window and the provider's own concurrency limits, not the model's ceiling.
func OutputTokenCap(provider, model string, rowMax int64) int {
	if rowMax > 0 {
		return int(rowMax)
	}
	if hit, ok := Lookup(provider, model); ok && hit.DefaultMaxTokens > 0 {
		return min(int(hit.DefaultMaxTokens), DefaultOutputTokenCap)
	}
	return DefaultOutputTokenCap
}
```

（Go 1.26 有内建 `min`；仓库已用。）

`pkg/run/config.go` 三处替换：

- `:415`（chatgpt）：`maxTokens := llm.OutputTokenCap("openai", model, cfg.MaxOutputTokens)`
  ——**查找键保持基线 `:415` 的 `llm.Lookup("openai", model)` 不变**：`JoinProviderModel` 是纯
  `provider + "/" + model` 拼接（`pkg/llm/catalog.go:468`），models.json 没有 `chatgpt/` 前缀的行，
  传 `"chatgpt"` 会让查找永远 miss；而 `openai/gpt-4o` 的 `limit.output` 是 16384，miss 后回落 32000
  会**超出该模型的文档输出上限**。用 `"openai"` 键则与今天同源：16384 这类小上限行原样保留，
  128000 这类大上限行才被 `min(…, 32000)` 压到 32000（正是拍板语义）。
- `:433`（其它）：`maxTokens := llm.OutputTokenCap(prov, model, cfg.MaxOutputTokens)`
- `:590`（OpenCode）：**改成同样的调用**，删掉"OpenCode 表没有输出上限"那段注释，改为：

```go
	// The cap comes from the same resolver every other provider uses. The old
	// note here said OpenCode's table carried windows but no output limits;
	// that stopped being true once the shipped catalog (assets/models.json,
	// refreshed with each build) carried limit.output for these ids.
	maxTokens := llm.OutputTokenCap(prov, model, cfg.MaxOutputTokens)
```

`cfg.MaxOutputTokens` 字段由 plan 263 引入（`pkg/run/config.go` 的 `LLMProviderYAML`）。
**若 263 尚未落地**：本步先传 `0`，并在同一行留 `TODO(plan 263): pass cfg.MaxOutputTokens`；
263 落地后由它的 Step 4 改成上面的写法（263 已写明，无需你重复实现）。

**Verify**: `CGO_ENABLED=1 go build ./...` → exit 0；
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -run 'NewLLMFromYAML|MaxOutput|Opencode' -count=1` → ok。

### Step 2: `Result.StopReason` + 归一化

`pkg/llm/llm.go` 的 `Result` 加：

```go
	// StopReason is why the provider stopped this response, normalized:
	// StopReasonOutputLimit when the output budget ran out (the response may
	// then be a fragment), otherwise the provider's own finish reason, "" when
	// it named none. Adapters set it so the engine can tell "the model chose
	// to say nothing" apart from "the model was cut off".
	StopReason string
```

`pkg/llm/output_limit.go` 再加：

```go
// StopReasonOutputLimit is the normalized reason every adapter reports when the
// provider cut the response because the output budget ran out.
const StopReasonOutputLimit = "output_limit"

// NormalizeStopReason maps each protocol's vocabulary onto StopReasonOutputLimit.
// Anthropic says max_tokens, Chat Completions length, Gemini MAX_TOKENS, and an
// incomplete Responses response names max_output_tokens as its reason. The
// Responses status "incomplete" is deliberately not on the list: a response
// cut by the content filter is incomplete too, and re-asking it is wrong.
func NormalizeStopReason(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "max_tokens", "length", "max_output_tokens":
		return StopReasonOutputLimit
	}
	return strings.TrimSpace(reason)
}

// OutputLimitReached reports whether a response was cut by the output budget.
func OutputLimitReached(stopReason string) bool {
	return NormalizeStopReason(stopReason) == StopReasonOutputLimit
}
```

四个适配器各自把已有的 finish/stop reason 经 `NormalizeStopReason` 写进 `Result.StopReason`，
**流式与非流式两条路径都要写**：

| 协议 | 流式路径取值 | 非流式路径取值 |
|---|---|---|
| Anthropic | `:300-306` 的 `event.Delta.StopReason`（存一个局部变量） | `executeUnary` 里的 `msg.StopReason` |
| Chat Completions | `executeChatCompletionStream` 的 `lastFinish` | `executeChatCompletionUnaryParts` 的 `lastFinish`（它已作为第二个返回值交出） |
| Responses | **新增** `case "response.incomplete":` 分支（见下）：`evt.Response.IncompleteDetails.Reason`；`response.completed` → `"completed"` | `resp.Status == incomplete` 时取 `resp.IncompleteDetails.Reason`，否则 `string(resp.Status)` |
| Google | `stream.go` 的 `st.finish` | `parseResponse` 的第二个返回值 |

Responses 流式的 `response.incomplete` 分支照抄 `response.completed`（`:1477-1540`）的用量与收尾处理
（赋 `usage`、`OnUsage`/`OnUsageSnapshot`、补正文与工具调用、`OnStopReason`、`OnMessageStop`），并把
`sawCompleted` 置真（它也是终止帧），只是 stop reason 取 `IncompleteDetails.Reason`。
不要传 status 字符串 `"incomplete"`：内容过滤也会让响应 incomplete（`IncompleteDetails.Reason == "content_filter"`）。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... -count=1` → ok
（此时除 Responses 流式多了 `response.incomplete` 收尾外没有行为变化，只多一个字段）。

### Step 3: 续写状态机（唯一实现）

`pkg/llm/output_limit.go`：

```go
// OutputLimitContinuations is how many times one call re-asks after the output
// budget ran out. Three is what Claude Code uses: enough for a long answer that
// was cut mid-sentence, small enough that a model stuck in a thinking loop
// cannot spend an unbounded budget.
const OutputLimitContinuations = 3

const (
	// outputLimitResumePrompt is the message the model is asked to continue on.
	// The wording is deliberately blunt and provider-neutral: an apology or a
	// recap costs exactly the tokens this recovery is trying to buy back.
	outputLimitResumePrompt = "Output token limit hit. Resume directly — no apology, no recap of what you were doing. Pick up mid-thought if that is where the cut happened. Break remaining work into smaller pieces."
	// outputLimitThinkingOnlyPrompt is the resume message when the cut happened
	// before any answer text existed. There is nothing to continue from, so the
	// model is told that and asked to spend less on thinking.
	outputLimitThinkingOnlyPrompt = "Output token limit hit while you were still reasoning, before this response produced any text or tool call. That reasoning was not kept and is not part of this conversation, so there is nothing to resume. Continue from the conversation above: reason much more briefly and produce your answer or next tool call well within the limit."
)

// OutputLimitRecovery is the one state machine every adapter runs. It is not
// shared code by accident: the same failure used to be handled four different
// ways (a 16-round prefill loop in Chat Completions, nothing at all in the other
// three protocols), which is how a cut-off answer became a hard error on
// Anthropic and a silent fragment on OpenAI.
type OutputLimitRecovery struct{ rounds int }

// ShouldContinue decides whether the response just received is re-asked. Calls
// are never continued: they are work the model already decided to do, and
// re-asking would duplicate them. A response that produced nothing and was not
// cut off is not a continuation case either.
func (r *OutputLimitRecovery) ShouldContinue(stopReason string, producedChars, toolCalls int) bool {
	if toolCalls > 0 || !OutputLimitReached(stopReason) {
		return false
	}
	return r.rounds < OutputLimitContinuations
}

// Resume turns the cut-off response into the two messages the next request
// carries: the fragment as the assistant's own turn (so the model continues
// instead of restarting), and the resume instruction as a user turn. The
// fragment is omitted when it is empty — an assistant turn with no content is
// rejected by every protocol, and in the thinking-only case there is nothing to
// continue from anyway.
func (r *OutputLimitRecovery) Resume(fragment string) []Message {
	r.rounds++
	out := make([]Message, 0, 2)
	if strings.TrimSpace(fragment) != "" {
		out = append(out, AssistantMessage([]ContentPart{Text(fragment)}))
	}
	prompt := outputLimitResumePrompt
	if strings.TrimSpace(fragment) == "" {
		prompt = outputLimitThinkingOnlyPrompt
	}
	return append(out, UserMessage(Text(prompt)))
}

// Exhausted reports whether the recovery budget is spent: the caller then hands
// back the fragment it has, with StopReason set, instead of asking again.
func (r *OutputLimitRecovery) Exhausted() bool { return r.rounds >= OutputLimitContinuations }
```

注意：**不要用 assistant 预填**（把 fragment 作为最后一条消息）。Anthropic 在开启思考时不接受
assistant 预填，而"assistant fragment + user 提示"在四个协议上都是普通的对话历史，安全。

**Verify**: `CGO_ENABLED=1 go build ./...` → exit 0；`go vet ./pkg/llm/` → 无输出。
（`-run OutputLimit` 此刻匹配 0 个测试——测试在 Step 6 才写，`go test -run` 零匹配也 exit 0，
不要拿它当门；Step 6 的包级门会覆盖这些测试。）

### Step 4: 四个协议各自接线

每个客户端都是同一个形状：循环 → 请求 → 若 `ShouldContinue(...)` 就 `Resume(fragment)` 拼进本轮消息再请求 →
最后把各轮**正文拼成一条** `Message.Parts`（只有一个 text part）、`Result.StopReason` 记最后一轮的归一化值。

**五条共同规则**（每个协议都必须满足，评审逐条查）：

1. **循环放在 `Execute` 这一层，包住该协议的流式与非流式两条路径**（Current state 列了各自的分支）。
   不要放进某个只服务一条路径的 helper 里（例如 Responses 的 `executeSyncDetail`、Google 的 `parseResponse`）：
   那样默认的流式主路径没有续写，而 `ExecuteStructured` 反倒被续写。
2. **`ExecuteStructured` 不续写**：Responses 的 `ExecuteStructured` 直接调 `executeSync`，循环在 `Execute`
   里就不会碰到它；compat 的 `ExecuteStructured` 与 `Execute` 共用 `executeChatCompletionUnary` →
   `executeChatCompletionUnaryParts`，给这两个函数加一个形参（例如 `continueOnLimit bool`），
   `Execute` 的路径传 `true`、`ExecuteStructured` 传 `false`（只跑一轮、照样写 `StopReason`）。
3. **用量照抄 compat 的口径**（`compat_llm.go:1114-1117`）：`Usage = (最后一轮 input, 各轮 output 之和,
   最后一轮 cached)`；多于一轮时 `BilledUsage = 各轮全部相加`，一轮时保持 nil。**不要**把各轮 input 加进
   `Usage`——它是占用率与压缩阈值读的数，加了会让上下文看起来翻倍。
4. **sink 的 `OnEnd` 每次 `Execute` 只触发一次**（照 compat 流式路径 `:1085-1087`）：Anthropic（`:191`、`:408`）
   与 Google（`agent_llm.go:91`、`stream.go:35`）今天都在单次请求里 `defer sink.OnEnd()`，接循环后要把它
   挪到 `Execute` 层、整个循环结束后调一次，否则界面会在续写之间把回复当成已结束。
5. **续写请求只带「一份」累计答案，每轮替换、不追加**：第 N 轮请求 = 调用方原始消息 +
   `Resume(到目前为止各轮正文拼起来的文本)` 返回的那 1–2 条。不要把每轮的 (fragment, 提示) 对一路追加下去——
   compat 的 `TestLengthContinueCarriesOneAnswerPerRound`（`compat_llm_test.go:590`）钉的就是这条
   （旧循环曾因逐轮追加让请求随轮数平方增长）。累计答案只含正文，**不带** reasoning（thinking-only 提示词
   本身就告诉模型「那段思考没有保留」）。

续写请求怎么拼：`Resume` 返回的是 `llm.Message`（assistant fragment + user 提示）。最省事且不会出错的做法是
把它们追加到 **llm 层的消息切片**上，再用该客户端现成的转换函数重新渲染请求——Anthropic
`agentAnthropicMessagesToAPI` + `ApplyPromptCache`、Responses `c.newResponseParams`、Google `buildRequest`；
compat 的循环本来就在 OpenAI 消息上操作，用 `compatMessagesToOpenAI` 转这两条再接到 `baseMsgs` 后面即可。

- **Anthropic**（`pkg/llm/anthropic/agent_llm.go`）：把 `Execute` 里 `:185` 起的流式主体抽成
  `executeStream(ctx, params) (*llm.Result, error)`，`Execute` 构造好请求后循环调用 `executeUnary` 或
  `executeStream`（按 `llm.NonStreamingRequestFrom(ctx)`）；每轮把 `res.Content` 的 text 块拼起来作为 fragment；
  `thinking` 块照旧只送 sink（不进消息）。只有 text 与 tool_use 进入 fragment/返回值。
- **Chat Completions**（`pkg/llm/openai/compat_llm.go`）：**删掉** `:887` 与 `:1315` 两个
  `for round := 0; round < openAILengthContinueMax; round++` 里的预填续写语义，改为调用
  `OutputLimitRecovery`（`openAILengthContinueMax` 常量删除；`isOpenAILengthTruncatedFinish` 与
  `openAIReasoningLengthShouldContinue` 一并删除或改写成 `NormalizeStopReason` 的调用）。
  fragment 取 `contentAcc`（原始正文增量），**不要**取 `compatMessageFromOpenAI` 产出的消息文本——
  只有思考时那里会是 ```` ```thinking … ``` ```` 包装（`:1521-1533`），拿它当 fragment 会把思考当正文回送、
  且选错提示词（应走 thinking-only 那句）。
  **轮数与语义变化要写进测试**：旧行为最多 16 轮、且不做 thinking-only 特判。
- **Responses**（`pkg/llm/openai/responses_llm.go`）：循环放在 `Execute`（`:611`）里，包住三个分支
  （`executeStreaming` / `executeSyncDetail` 的 replay 分支 / `executeSync`）；`StopReason` 为 `output_limit`
  （即 `IncompleteDetails.Reason == "max_output_tokens"`）时续写，`content_filter` 不续写。
  每轮用 `c.newResponseParams(ctx, append(messages, resume...), tools, false)` 重建请求。
- **Google**（`pkg/llm/google/agent_llm.go` + `pkg/llm/google/stream.go`）：两条路径在 `finishReason == MAX_TOKENS`
  且无内容时都**不再返回错误**（`agent_llm.go:421-425` 与 `stream.go:90-95` 里有 finish 的那一支），而是返回
  `&llm.Result{Message: 空 assistant 消息, Usage: …, StopReason: "output_limit"}`；其它 finish reason
  （`SAFETY`、`RECITATION` 等）的空响应保持今天的 `response was empty` 错误不变。循环放在 `Execute`
  （`agent_llm.go:71`），每轮用 `buildRequest(append(messages, resume...), tools, c.maxTokens)` 重建请求体
  （`contents` 自然得到 `role:"model"` 的 fragment 与 `role:"user"` 提示）。

**Verify**: `CGO_ENABLED=1 go build ./...` → exit 0；`go vet ./pkg/llm/...` → 无输出。
此时 `go test ./pkg/llm/openai/` **预期红**：`compat_llm_test.go` 里钉预填形状/16 轮的既有测试
（`TestLengthContinueReportsSettledRequestPrompt` `:522`、`TestLengthContinueCarriesOneAnswerPerRound` `:590`、
`TestLengthContinueRefusedRoundReportsTheRequestItSent` `:684`、`TestLengthContinueWithoutASettledFigureDoesNotPanic` `:753`、
`TestNonStreamingLengthContinueReportsTheBillToTheCounters` `:798` 中断言请求形状/轮数的那些）——Step 6 按「改写口径」修；
**这 5 条以外**的红才是问题，同一处修两次不过就 STOP。其余包 `go test ./pkg/llm/anthropic/ ./pkg/llm/google/ -count=1` 应保持 ok。

### Step 5: 引擎侧收尾语义（思考撞线与续写用尽）

`pkg/run/orchestration_llm.go` 的 no-tool-call 分支（`:817` 起）。在 `:836` 那条硬错误**之前**插入：

```go
		if res != nil && llm.OutputLimitReached(res.StopReason) {
			// The response is what the output budget left: possibly a prefix of
			// the answer, possibly nothing at all (a reasoning model can spend
			// the whole budget thinking). Either way it is the model's budget
			// that ran out, not the model saying nothing — saying "empty
			// response" here was the bug that threw a whole turn away.
			// The enclosing branch's guard is `res == nil || res.Message ==
			// nil || len(res.Message.ToolCalls) == 0`, so both are checked
			// before use; a nil res falls through to the empty-response error
			// below, which is the right outcome for "no response at all".
			text := ""
			if res.Message != nil {
				text = strings.TrimSpace(res.Message.TextContent())
			}
			if text != "" {
				if sink := llm.StreamSinkFrom(ctx); sink != nil && sink.OnResponseNotice != nil {
					sink.OnResponseNotice(llm.ResponseNotice{Kind: llm.ResponseNoticeIncomplete, Text: llm.OutputLimitExhaustedNotice})
				}
				// falls through to the ordinary end of turn with the fragment
			} else {
				return nil, fmt.Errorf("%s", llm.OutputLimitEmptyAnswerError)
			}
		}
```

`pkg/llm/output_limit.go` 再加两个常量（provider-neutral、逐字节固定）：

```go
// OutputLimitExhaustedNotice is shown when a turn ends on a cut-off answer: the
// text is kept, but the user has to know it is a prefix.
const OutputLimitExhaustedNotice = "The answer was cut off by this model's output limit and could not be continued further. Raise the model's output limit, lower its reasoning effort, or ask for a smaller piece of work."

// OutputLimitEmptyAnswerError is the terminal error when the budget ran out
// before any answer text existed at all (thinking consumed it). It replaces the
// old "empty assistant response after tool execution", which described a
// symptom and hid the cause.
const OutputLimitEmptyAnswerError = "the model's output budget ran out while it was still thinking, so no answer was produced; lower its reasoning effort or raise its output limit"
```

顺手把 `isEmptyAssistantResult` 的调用点语义写清：撞上限**不再**走到它（在它之前就分流了），
注释里写一句"a capped response is handled above; reaching here means the model really said nothing"。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1` → ok。

### Step 6: 测试

`pkg/llm/output_limit_test.go`（纯逻辑，无网络）：

- `NormalizeStopReason`：`max_tokens`/`length`/`MAX_TOKENS`/`max_output_tokens` → `output_limit`；
  `incomplete` → `incomplete`（**不是** output_limit）；`content_filter` → `content_filter`；`end_turn` → `end_turn`；空 → `""`。
- `ShouldContinue`：有 toolCalls → false；未撞上限 → false；第 4 轮 → false（`OutputLimitContinuations` 为 3）。
- `Resume("")`：只返回 1 条 user 消息、文案是 thinking-only 那条；`Resume("abc")`：2 条、assistant 在前。
- `OutputTokenCap`：目录有 131072 且未设行级 → 32000；行级 50000 → 50000；目录无记录 → 32000。

各客户端（沿用已有的 `httptest` SSE 夹具，见 `pkg/llm/anthropic/agent_llm_test.go:31`）：

| 文件 | 测试 | 断言 |
|---|---|---|
| `pkg/llm/anthropic/agent_llm_test.go` | `TestAnthropicContinuesAfterOutputLimit` | 第一次 SSE 回 `text_delta` 半句 + `stop_reason: max_tokens`；第二次回 `stop_reason: end_turn`。断言：共 2 次请求；第 2 次请求体里**最后两条消息**是 assistant(半句) + user(续写提示)、且**没有** assistant 预填尾巴；返回文本 = 两段拼接；`Usage.OutputTokens` = 两轮之和、`Usage.InputTokens` = **第 2 轮**的 input（不是两轮之和）、`Billed().InputTokens` = 两轮之和；sink 的 `OnEnd` 恰好被调 1 次 |
| 同上 | `TestAnthropicThinkingOnlyLimitContinues` | 第一次只有 `thinking_delta` + `max_tokens`；断言第 2 次请求**只有** user(thinking-only 提示)、没有空 assistant；返回文本 = 第 2 次正文 |
| 同上 | `TestAnthropicGivesUpAfterThree` | 连续 4 次 `max_tokens`；断言共 4 次请求（首轮 + 3 次续写）、`Result.StopReason == output_limit`；第 4 次请求里 assistant 消息只有 1 条（累计答案每轮替换，规则 5） |
| 同上 | `TestAnthropicNonStreamingContinues` | 用非流式开关（`llm.NonStreamingRequestFrom` 为真的 ctx，照该文件已有非流式测试的造法）回 JSON `stop_reason: max_tokens` + 半句，再回 `end_turn` → 共 2 次请求、文本拼接（证明 `executeUnary` 路径也在循环里） |
| `pkg/llm/openai/compat_llm_test.go` | `TestCompatLengthContinueUsesThreeRounds` | 连续 `finish_reason: length`；断言请求次数 = 1+3；第 2 次起请求末两条是 assistant(累计正文) + user(提示)、没有预填尾巴 |
| 同上 | `TestCompatStructuredDoesNotContinue` | `ExecuteStructured` 收到 `finish_reason: length` → **恰 1 次**请求、`Result.StopReason == output_limit`（Scope 里写明的有意变化） |
| 同上 | 改写既有 5 条 `TestLengthContinue*`（`:522`、`:590`、`:684`、`:753`、`:798`） | 用量/计费/拒绝轮/无 settled 数字不 panic 的断言**保留**；只把「请求里带预填 assistant(reasoning)」的形状断言改成规则 5 的新形状、把 16 轮相关的轮数改成 ≤3。不得删掉整条测试 |
| `pkg/llm/openai/responses_llm_test.go` | `TestResponsesStreamingIncompleteContinues` | **流式**：第一次以 `response.incomplete`（`incomplete_details.reason: max_output_tokens`，带 usage）结束、第二次 `response.completed` → 共 2 次请求、文本拼接、`Usage` 非 nil |
| 同上 | `TestResponsesSyncIncompleteContinues` | 非流式开关下 `status: incomplete` + `max_output_tokens` → 续写一轮后 `completed` |
| 同上 | `TestResponsesContentFilterDoesNotContinue` | `response.incomplete` + `reason: content_filter` → 恰 1 次请求、`StopReason == "content_filter"` |
| `pkg/llm/google/stream_test.go` | `TestGoogleStreamingMaxTokensContinues` | **流式**（默认路径）：第一次 SSE 只有 `finishReason: MAX_TOKENS`、无 parts → 不再报 `response was empty`、续写一轮；用尽后返回 fragment + `output_limit` |
| `pkg/llm/google/agent_llm_test.go` | `TestGoogleMaxTokensContinues` | 非流式开关下同上 |
| 同上 | `TestGoogleSafetyEmptyStillErrors` | `finishReason: SAFETY` 且无内容 → 仍是 `response was empty (finish reason "SAFETY")` 错误、恰 1 次请求 |
| `pkg/run/orchestration_llm_test.go` | `TestEmptyAssistantAfterOutputLimitNamesTheCause` | 假 LLM 返回 `Result{Message: 空, StopReason: output_limit}` 且 session 里有工具结果 → 错误文案 = `llm.OutputLimitEmptyAnswerError`，**不是** `empty assistant response after tool execution` |
| 同上 | `TestFragmentAfterOutputLimitEndsTurnWithNotice` | 假 LLM 返回带半句正文 + `output_limit` → 不报错、`OnResponseNotice` 收到 `llm.OutputLimitExhaustedNotice`、最终文本 = 那半句 |

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/run/ -count=1` → ok。

### Step 7: 真机验收（必做，fake 模型证明不了这件事）

按仓库验收规矩：真机模型用 `zhipuai/glm-5.3`，凭据从 state DB `fb_config` 取（不要读 `.env`，已删）。
但**本次事故的现场原件是 `opencode-go/qwen3.8-max` + `xhigh`**，验收必须用它把这两个场景各跑一次：

1. 一个会产出长答案的只读任务（例如"把 plans/README.md 里 2026-11-07 那条完整复述一遍，不要改写"）：
   确认回复**完整结束**、没有 `empty assistant response`、TUI 底部不再出现半截思考后报错。
2. 刻意把该行的输出上限设成很小（如 1000）再问一个长问题：
   确认出现 `output_limit` 的续写（日志里能看到 2 次以上请求）、续写用尽后 TUI 显示
   `OutputLimitExhaustedNotice` 那句话，而不是 `empty assistant response after tool execution`。

把两次的 TUI 截图/日志片段记进执行报告。

**Verify**: 两条都不再出现 `empty assistant response after tool execution`。

## Done criteria

- [ ] `CGO_ENABLED=1 go build ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/run/ -count=1` ok
- [ ] `gofmt -l pkg/llm pkg/run` 无输出
- [ ] `grep -rn "openAILengthContinueMax" pkg/` 无命中
- [ ] `grep -rn "openai.DefaultMaxCompletionTokens" pkg/run/config.go` 无命中（三处都已改走 `llm.OutputTokenCap`）
- [ ] `grep -n "OutputTokenCap" pkg/run/config.go` 命中 3 处
- [ ] `grep -rn "empty assistant response after tool execution" pkg/run/ | grep -v _test.go` 恰 1 行（`:836` 那条，
      位于 Step 5 插入的撞上限分流**之后**）；测试文件里可以有它（只作否定断言），不计入。
      （plan 273 随后会把这一行也换掉——那是 273 的判据，不影响本计划判 DONE。）
- [ ] `grep -n 'response.incomplete' pkg/llm/openai/responses_llm.go` 有命中（流式 incomplete 分支已加）
- [ ] `grep -n 'OutputLimitRecovery' pkg/llm/google/stream.go pkg/llm/google/agent_llm.go` 至少 1 处命中
      （Google 循环在 `Execute`，`stream.go` 至少要改 MAX_TOKENS 的返回，二者合计有引用即可）
- [ ] Step 7 两个真机场景通过，证据在报告里
- [ ] `git status` 只列出 Scope 里的文件（并行会话在 `pkg/llm` 里的改动保持原样）

## STOP conditions

- `llm.Result` 上已经有别人加的 `StopReason`/`Truncated` 字段 → 停下，改用它，不要并存两套。
- `compat_llm.go` 的续写循环在 drift 核对时已经不存在（别人先做了）→ 停下，先看它的实现再决定。
- Anthropic 真机拒绝"assistant fragment + user 提示"这种历史（400）→ 停下报告，**不要**改成预填硬顶。
- Google/Responses 的续写需要改动超出 Scope 的文件（例如 provider 请求类型在别处定义）。
- 真机验收连续两次失败。
- 同一处验证连续失败两次。

## Maintenance notes

- **上限与上下文预算的关系**：占用率/压缩用的是目录的 `Limit.output` 作为输出预留
  （`pkg/llm/catalog.go:141` `EffectiveOutputReserve`）。本计划把实际请求的上限压到 32000，
  于是"预留"可能大于"实发"——这在安全方向（不会超窗），但若以后有人把 `EffectiveOutputReserve`
  改成等于 `OutputTokenCap`，要同时改压缩阈值，否则上下文会算多。
- **思考吃预算**：`xhigh` 会让长答案更早撞上限。若再次出现"续写 3 次还不够"，正确做法是降低该行的
  思考档位或抬该行的 `MaxOutputTokens`，**不是**把 `OutputLimitContinuations` 调大
  （多了只会让一次卡住的思考循环烧掉更多预算）。
- **协议一致性**：以后新增协议客户端，必须接 `OutputLimitRecovery`（流式与非流式两条路径都要），
  并在测试里钉住"toolCalls>0 不续写"。
- **与 plan 273 的边界**：273 把「没撞上限却计费了、什么都没交回来」的结果交给 `pkg/run/llm_middleware.go` 已有的
  空输出重试层原样重发，靠的是 `llm.OutputLimitReached(res.StopReason)` 把撞上限的结果**放行**给本计划。
  改 `NormalizeStopReason` 的映射表，就是在改这两条路径的分界线。
- **评审要点**：①每轮 fragment 是否被拼进返回文本（而不是只剩最后一轮）；②usage 是否按轮累加而不是逐块相加；
  ③撞上限但仍有 toolCalls 时是否照常执行（不续写）；④`IsMeta` 未被用来传提示词。
