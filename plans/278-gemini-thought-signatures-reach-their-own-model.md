# Plan 278: Gemini 的思考签名与思考摘要原样回传给产出它的同一模型

> **Executor instructions**: 按步骤顺序执行，每步验证通过再做下一步。遇到「STOP conditions」就停下报告，
> 不要自己发挥。**不要 commit / push / merge**（仓库规矩：owner 手动提交）。做完后把 `plans/README.md`
> 里本计划那一行的状态改成 DONE 并附一句摘要——除非派你来的评审说索引由他维护。
>
> **本计划硬依赖 plan 275 与 plan 277**（执行序：275、276 → 277 → 278）。开工前三条都要满足，任一条不满足就**停下**：
> - `grep -n 'func StructuredCarry\|func DecodeThinking' pkg/llm/retry.go` 两个都命中（275）
> - `grep -n 'func ThinkingFenceOnly\|func ThinkingFenceGoesTo' pkg/llm/thinking_fence.go` 两个都命中（277，其前提 276）
> - `grep -n 'func buildRequest(messages \[\]llm.Message, tools \[\]\*llm.Tool, maxTokens int64, model string)' pkg/llm/google/agent_llm.go` 有命中（277 Step 4）
>
> **Drift check**：`git status --porcelain -- pkg/llm/retry.go pkg/llm/google/ pkg/llm/thinking_carry_test.go`
> → 期望：都带 275/277 的改动（属预期）。逐条对照「Current state」：**结构对得上就继续**，对不上才 STOP。
> 行号以 `d9c45060` 为准，依赖计划落地后会后移，按符号找。

## Status

- **Priority**: P1（按 Google 官方文档，Gemini 3 / 4 在工具循环第二步起就会因缺签名返回 4xx；Gemini 2.5 每一步都看不到自己上一步的思考）
- **Effort**: M–L
- **Risk**: MED（改 Google 适配器的响应解析与历史投影；产出者以外的模型收到的内容除「当前轮无签名调用补占位签名」外不变）
- **Depends on**: `plans/275-anthropic-thinking-reaches-its-own-model.md`、`plans/277-thinking-fence-goes-only-to-its-producer.md`
- **Category**: bug
- **Planned at**: commit `d9c45060`, 2026-11-08

## Why this matters

owner 2026-11-08 要求检查 Google 适配器有没有和 275（Anthropic 的 thinking 从不回传）同类的问题——**有，而且更严重**。

Gemini 的 generateContent 接口没有独立的 thinking 块，模型的推理状态以加密的 `thoughtSignature` 挂在响应的 part 上
（函数调用 part、最后一个文本 part，流式时可能是一个空文本 part）。Google 要求把这些 part 连同签名**原样**放回历史。
forebrain 的 Google 适配器（`pkg/llm/google/`）的 `part` 结构体里**根本没有** `thoughtSignature` 和 `thought` 字段：

1. **签名在解析响应时被丢掉，历史里从不回传**——即使还是同一个模型。
   - Gemini 3 / 4：官方文档明确，当前轮的函数调用缺签名会直接返回 400 / 4xx（错误原文形如
     `Function call FC1 in the 1. content block is missing a thought_signature.`）。也就是工具循环的第二个请求就会失败。
   - Gemini 2.5：不报错，但每一步都看不到自己上一步的推理——与 275 修的 Anthropic 无限循环同一类问题。
2. **思考摘要 part（`"thought": true`）会被当成正文**：适配器不认识 `thought` 字段，带文字的思考摘要 part 会进入回答正文、
   推给界面当答案、之后作为普通 assistant 正文发给任何模型（包括换过之后的新模型）。
3. **part 的原始顺序丢失**：回放时一律「全部正文在前、全部函数调用在后」，而 Google 要求签名留在它原来那个 part、保持原顺序。

受影响的是一切走 Google 协议的模型——目前就是 OpenCode 上所有 `gemini-*` 模型（`pkg/llm/opencode/endpoints.go:104`），
包括 owner 配置里的 `opencode-go/gemini-4-argon-contributor`；Google 适配器只有这一个入口（`pkg/run/config.go:596`）。

## 官方规则（2026-11-08 读取，逐字引用）

来源：V1 = https://docs.cloud.google.com/vertex-ai/generative-ai/docs/thought-signatures ，
V2 = https://docs.cloud.google.com/vertex-ai/generative-ai/docs/thinking ，
A1 = https://aistudio.google.com/docs/thought-signatures.md?generate-content=true （AI Studio 的 generateContent 版本）。

- 字段名：响应 JSON 里是 `thoughtSignature`（V1、A1 的响应示例：`"thoughtSignature": "<SIGNATURE_A>"`）。
- 出现位置：V1「If the model response contains one or more `functionCall` parts, a `thought_signature` is required for correct processing.」
  「In cases of *parallel* function calls in a single response, only the *first* `functionCall` part will contain the `thought_signature`.」
  「In cases of *sequential* function calls across multiple steps in a turn, *each* `functionCall` part will contain a `thought_signature`.」
  「it might include a `thought_signature` in the *last* `part` of the response (for example, the last `text` part).」
- 流式：V1「When streaming, this signature might be returned in a part with empty text content, so be sure to parse all parts until `finish_reason` is returned by the model.」
- 合并：V1「Don't merge a `Part` containing a signature with one that does not.」「Don't combine two `Part`s that both contain signatures, because the signature strings cannot be merged.」
  「Always send the `thought_signature` back to the model inside its original `Part`.」
- 回传：V1「include all `Part`s from all previous messages in the conversation history when sending a new request, exactly as they were returned by the model.」
- 强制：V1「If a required thought signature is not returned when using Gemini 3 models, the model will return a `400` error.」
  A1「When using Gemini 4 and Gemini 3 models, you must pass back thought signatures during function calling, otherwise you will get a validation error (4xx status code).」
  A1「Strict validation is enforced for all function calls within the current turn. (Only current turn is required; we don't validate on previous turns)」
  V1「A turn begins with the most recent user message that is not a `functionResponse`.」
  V1「The API does **not** strictly enforce validation for signatures in non-`functionCall` parts.」
- 不是模型生成的调用：A1「you can set the following dummy signatures of either `"context_engineering_is_the_way_to_go"` or `"skip_thought_signature_validator"` in the thought signature field to skip validation.」
  V1「You can set `thought_signature` to `skip_thought_signature_validator`, but, this should be a last resort as it will negatively impact model performance.」
- 思考摘要：V2「Your application must handle empty or missing thought summary fields gracefully while preserving any accompanying thought signatures.」

文档**没有**说签名是否绑定产出它的模型；本计划按 owner 已定的口径（275/276/277）只回传给同一模型——这对 Google 是安全的：
换模型只发生在两轮之间（每轮开始固定模型，`pkg/run/config.go:2150-2160`），而上一轮的签名不做校验。

## 已拍板的决定

- **owner 2026-11-08**：Google 适配器有同类问题就必须加入本期一并修复。
- **口径与 275–277 一致**：同一模型回传全部历史轮的思考（签名与思考摘要）；只给产出它的模型；未记录产出者就不回放。
- **原样回放**：给产出者的 model 内容按响应里的 part 顺序逐个重建，签名留在原 part 上；不重排、不合并带签名的 part。
- **思考摘要 part 是思考不是正文**：解析时走 reasoning（`OnReasoningDelta` / `OnReasoningDone`），不进 `msg.Parts`；只随 carry 回放给产出者。
- **当前轮里没有签名的函数调用补 Google 文档给的占位签名** `skip_thought_signature_validator`（只补「当前轮、该 model 内容的第一个 functionCall、本身没签名」这一种情况）。
  正常路径（同模型、带签名）永远不会走到它；它兜的是升级前存下、又在同一轮里续跑的老会话，以及任何不是 Gemini 生成的调用——否则 Gemini 3 / 4 直接 4xx。
- **续写（MAX_TOKENS 后续写多轮）**：合并出的那条消息回放「合并后的正文（无签名）+ 最后一轮的函数调用 part（带各自签名）」；
  思考摘要与正文签名不带（与 275 的「续写只带最后一轮」同理；函数调用签名必须保住，否则 Gemini 3 / 4 当前轮 4xx）。
  最后一轮本身撞上限就不带 carry。

## Current state

行号基于 `d9c45060`，文件都在 `pkg/llm/google/`。

**① part 结构没有签名与思考字段**（`agent_llm.go:249-254`）：

```go
type part struct {
	Text             string            `json:"text,omitempty"`
	InlineData       *inlineData       `json:"inlineData,omitempty"`
	FunctionCall     *functionCall     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponse `json:"functionResponse,omitempty"`
}
```

请求与响应共用这一个结构（`generateContentResponse` 的 candidate content 也解码成它）。

**② 非流式解析**（`parseResponse`，`agent_llm.go:463` 起，循环 `:488-497`）：

```go
	msg := &llm.Message{Role: llm.RoleAssistant}
	// Text and calls are collected in the order they arrived, so a response that
	// interleaves them keeps its sequence within each of the two shapes.
	for _, p := range candidate.Content.Parts {
		switch {
		case p.FunctionCall != nil:
			msg.ToolCalls = append(msg.ToolCalls, googleToolCallFromPart(p))
		case p.Text != "":
			msg.Parts = append(msg.Parts, llm.Text(p.Text))
		}
	}
```

（之后 `:498-523` 判空：既无 part 也无调用时按 finish reason 分流——MAX_TOKENS 交给续写、计费了却 STOP 的走 `ErrBlankModelOutput`、其余报错。）
`executeUnaryRound`（`:165-198`）把 `result.Message.Parts` 的文字回放给 sink（`llm.ReplayToSink`，`ReplayedResponse` 有 `Reasoning` 字段可用，见 `pkg/llm/anthropic/agent_llm.go:546-551` 的用法）。

**③ 流式解析**（`stream.go`，`consumeSSEEvent` 的 part 循环 `:170-192`，`streamState` `:121-131`，`endTextBlock` `:207-213`，收尾 `:79-118`）：

```go
	for _, p := range candidate.Content.Parts {
		switch {
		case p.FunctionCall != nil:
			llm.NotifyResponseStarted(sink, &st.started)
			// A call ends the answer text that preceded it, the same seal the
			// finish frame applies to the final block.
			if st.text.Len() > 0 {
				st.endTextBlock(sink)
			}
			call := googleToolCallFromPart(p)
			st.calls = append(st.calls, call)
			if sink != nil && sink.OnToolCallReady != nil {
				sink.OnToolCallReady(call)
			}
		case p.Text != "":
			llm.NotifyResponseStarted(sink, &st.started)
			st.text.WriteString(p.Text)
			if sink != nil && sink.OnDelta != nil {
				sink.OnDelta(p.Text)
			}
		}
	}
```

空文本 part（流式里可能只带签名）在这里被直接跳过；收尾在 `:116` 组 `&llm.Message{Role: llm.RoleAssistant, Parts: st.parts, ToolCalls: st.calls}`。
`:84` 的「`st.finish == ""` 且已有内容 → `ErrStreamTruncated`」与 `:88-111` 的判空逻辑不动。

**④ 投影**（`agent_llm.go`；277 之后 `buildRequest` 已有 `model` 参数、assistant 分支开头已有围栏规则）：

```go
		case llm.RoleAssistant:
			parts := assistantParts(m, toolNames)
			if len(parts) > 0 {
				req.Contents = append(req.Contents, content{Role: roleModel, Parts: parts})
			}
```

`assistantParts`（`:358-378`）先放全部非空文本、再放全部有名字的函数调用，并把 `tc.ID → name` 记进 `toolNames`（工具结果靠它配对名字）。
工具结果（`RoleTool`）投影成 `content{Role: roleUser, Parts: []part{resultPart}}`（`:312-318`）。

**⑤ 续写合并**（`Execute`，`agent_llm.go:139-157`）：`rounds == 1` 时直接返回该轮结果（`lastResult`）；多轮时新组一条
`&llm.Message{Role: llm.RoleAssistant, Parts: parts, ToolCalls: lastToolCalls}`——不带任何签名。

**⑥ 275 留下的 carry 机制**（`pkg/llm/retry.go`）：`Prefix`、Responses 的 `Encode`/`Decode`、275 新增的 `anthropic_thinking_v1`
（`EncodeThinking`/`DecodeThinking`）、`StructuredCarry`（判断「是不是结构化 payload」的唯一入口——OpenAI 兼容客户端靠它拒绝把结构化 payload 当 `reasoning_content`）、
`ExtractSummary`（返回可显示的思考文本，认不出的 payload 会把原始 JSON 当文本返回）。落库（`pkg/state/message_parts.go` 的 `reasoning_carry` 部件）对 payload 内容无感知。

**⑦ 仓库约定**：注释写「为什么」、完整英文句子；`pkg/llm` 导出符号必须有 doc comment；Google 测试范例见 `pkg/llm/google/agent_llm_test.go` 与 `stream_test.go`
（httptest 服务器回 JSON / SSE、断言请求体）。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| google 包 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/google/ -count=1` | ok |
| llm 各包 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... -count=1` | ok（`pkg/llm` 本身约 2.5 分钟） |
| 回归面 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ ./pkg/turn/ ./pkg/run/ ./pkg/memory/ -count=1` | ok |
| 静态检查 | `go vet ./pkg/llm/...` | exit 0 |
| 格式 | `gofmt -l pkg/llm` | 无输出 |

`FOREBRAIN_HOME` 必须显式指向 mktemp 目录（仓库硬规矩）。

## Scope

**In scope**：
- `pkg/llm/retry.go` —— 新增 Gemini part 布局 payload，`StructuredCarry` 与 `ExtractSummary` 认它
- `pkg/llm/thinking_carry_test.go` —— 补 Gemini payload 的测试（275 建的文件）
- `pkg/llm/google/agent_llm.go`、`pkg/llm/google/stream.go`
- `pkg/llm/google/agent_llm_test.go`、`pkg/llm/google/stream_test.go`

**Out of scope**：
- 往请求里加 `thinkingConfig`（`includeThoughts`、`thinkingLevel` 等）——本计划只修「模型返回的签名与思考被丢掉」，不改「要不要、怎么思考」。
- 并行函数调用的工具结果分成多条 user 内容（`:312-318`）——与签名无关，现状不动。
- OpenAI 兼容 / Responses / Anthropic 三个适配器——分别归 275/276/277。
- `pkg/run` 里流式中途被打断时由回调拼出的临时 assistant 消息（见 Maintenance notes）。

## Steps

### Step 1: `pkg/llm/retry.go` 加 Gemini part 布局 payload

紧挨着 275 的 `EncodeThinking`/`DecodeThinking` 之后加（措辞可调，**语义不可变**）：

```go
// geminiPartsPayloadVersion marks a carry holding a Gemini response's parts
// as the model returned them: their order, the thought-summary parts, and the
// thoughtSignature each part carried. Gemini has no thinking blocks of its
// own; its reasoning rides those signatures, and the API wants every part
// back exactly as it came — the signature in its original part.
const geminiPartsPayloadVersion = "gemini_parts_v1"

// GeminiPart is one part of a Gemini response, as replayed to the model that
// produced it.
type GeminiPart struct {
	// Kind is "text" (answer text, possibly empty when the part only
	// carried a signature), "thought" (a thought summary) or "call".
	Kind string `json:"kind"`
	// Text is the part's text, byte for byte; empty for a call.
	Text string `json:"text,omitempty"`
	// Call indexes the message's ToolCalls for a "call" part.
	Call int `json:"call,omitempty"`
	// Signature is the part's thoughtSignature, byte for byte.
	Signature string `json:"signature,omitempty"`
}

// EncodeGeminiParts packs parts into a carry; "" when parts is empty. It
// never falls back to plain text.
func EncodeGeminiParts(parts []GeminiPart) string

// DecodeGeminiParts reads what EncodeGeminiParts packed, and reports false for
// every other carry.
func DecodeGeminiParts(name string) ([]GeminiPart, bool)
```

实现照 275 的 `EncodeThinking`/`DecodeThinking`（`json.Marshal` 一个 `{version, parts}` 外壳、`Prefix` 开头、版本不符或空就 false；**不 trim 任何字段**）。
然后：
- `StructuredCarry` 加一支：`DecodeGeminiParts` 认得的也算结构化 payload。
- `ExtractSummary` 加一支：Gemini payload 返回各 `thought` part 的 `Text` 用 `"\n"` 连接（没有思考摘要就返回 `""`）；**绝不能**把 JSON 原文当文本返回。

测试（`pkg/llm/thinking_carry_test.go`）：
- `TestGeminiPartsCarryRoundTripsByteForByte`：四种 kind 混排（含空文本带签名的 text、`Call` 为 0 与 2 的 call、文本含首尾空格/换行/中文/emoji）→ 编码再解码逐字段相等、顺序不变。
- `TestGeminiPartsCarryIsStructured`：`StructuredCarry` 为 true；`Decode`、`DecodeThinking` 都为 false；`DecodeGeminiParts` 对 Responses / Anthropic payload 与纯文本 carry 为 false；
  `ExtractSummary` 只返回 thought 文本、不含签名与 JSON。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ -count=1 -run 'GeminiParts|ThinkingCarry' -v` → 全部 PASS。

### Step 2: part 结构加字段，并能写出「空文本 + 签名」

`part` 加两个字段：

```go
	// Thought marks a thought-summary part: the model's reasoning, never its answer.
	Thought bool `json:"thought,omitempty"`
	// ThoughtSignature is the encrypted reasoning state the model attached to
	// this part. It goes back in this same part, to the model that wrote it.
	ThoughtSignature string `json:"thoughtSignature,omitempty"`
```

`Text` 带 `omitempty`，而流式里「空文本 + 签名」的 part 回放时必须写成 `{"text":"","thoughtSignature":"…"}`（只有签名、没有数据字段的 part 不是合法 part）。
给 `part` 加一个 `MarshalJSON`：当 `Text == ""`、`ThoughtSignature != ""` 且其余数据字段（`InlineData`、`FunctionCall`、`FunctionResponse`）都为空时，显式输出 `"text":""`；
其余情况与今天的序列化**逐字节相同**（用一个不带方法的别名类型做默认序列化，避免递归）。

**Verify**: 新增 `TestGooglePartJSON`（`agent_llm_test.go`）：
`part{ThoughtSignature: "S"}` → `{"text":"","thoughtSignature":"S"}`；`part{Text: "hi"}` → `{"text":"hi"}`；
`part{FunctionCall: &functionCall{Name: "f"}, ThoughtSignature: "S"}` → `{"functionCall":{"name":"f"},"thoughtSignature":"S"}`；
`part{Text: "t", Thought: true, ThoughtSignature: "S"}` → `{"text":"t","thought":true,"thoughtSignature":"S"}`（字段顺序以实际 `json.Marshal` 为准，测试断言用 `json.Unmarshal` 回 `map[string]any` 比较，**另外**断言空文本那条的原始字节含 `"text":""`）。
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/google/ -count=1` → ok。

### Step 3: 非流式解析保留布局

`parseResponse` 的 part 循环改成按收到的顺序同时产出三样：`msg.Parts`、`msg.ToolCalls`、布局 `layout []llm.GeminiPart`，以及思考摘要文本（给 sink 回放用）：

- `p.FunctionCall != nil` → 照旧 `ToolCalls = append(…)`；布局追加 `{Kind: "call", Call: <刚追加的下标>, Signature: p.ThoughtSignature}`。
- `p.Thought` → **不进** `msg.Parts`；布局追加 `{Kind: "thought", Text: p.Text, Signature: p.ThoughtSignature}`；文本记入思考摘要。
- 其余文本 part：`p.Text != ""` → 照旧进 `msg.Parts`；布局追加 `{Kind: "text", Text: p.Text, Signature: p.ThoughtSignature}`。
  `p.Text == ""` 但 `p.ThoughtSignature != ""` → **不进** `msg.Parts`，布局追加 `{Kind: "text", Signature: …}`。空文本且无签名的 part 照旧忽略。
- 布局里**至少有一个签名或一个 thought part** 时：`msg.Name = llm.EncodeGeminiParts(layout)`、`msg.ReasoningModel = <该 client 的模型>`。
  `parseResponse` 现在没有模型参数——加一个 `model string` 参数，`executeUnary` 传 `c.model`。

判空逻辑（`:498-523`）照旧只看 `msg.Parts` 与 `msg.ToolCalls`：只有思考摘要、没有正文也没有调用的响应，仍按原规则分流（不因为有思考就当成有回答）。

`executeUnaryRound` 回放给 sink 时把思考摘要放进 `llm.ReplayedResponse.Reasoning`（文本用 `""` 连接，与 Anthropic 非流式 `:548` 一致）。
`parseResponse` 需要把思考摘要文本交给调用方——在返回值上加一项或通过 `Result` 携带均可，选改动最小的写法，但**不要**把它放进 `msg.Parts`。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/google/ -count=1` → ok（既有测试不受影响）。

### Step 4: 流式解析保留布局

`streamState` 加 `layout []llm.GeminiPart`、`thought strings.Builder`（当前打开的思考摘要）以及记录「当前打开的是正文还是思考」的状态。`consumeSSEEvent` 的 part 循环：

- `p.Thought`：若有打开的正文块先 `endTextBlock`；文本追加到 `st.thought` 并 `sink.OnReasoningDelta(p.Text)`（非空时）；
  若该 part 带签名：把当前思考摘要连同签名封成布局里的一个 `thought` part 并 `sink.OnReasoningDone()`，关闭它。
- 函数调用：先封掉打开的思考（无签名的 `thought` part，`OnReasoningDone`）与打开的正文块；照旧追加调用与 `OnToolCallReady`；布局追加 `call`（带该 part 的签名）。
- 非空正文：先封掉打开的思考；照旧追加到 `st.text`、`OnDelta`。若该 part 带签名：立刻 `endTextBlock`（正文块到此为止），并把这一块连同签名记进布局。
- **空文本 + 签名**：先封掉打开的思考与正文块；布局追加 `{Kind: "text", Signature: …}`（不进 `st.parts`、不推 sink）。
- `endTextBlock` 每封一块正文，就往布局追加一个**不带签名**的 `text` part（带签名的那种由上一条负责，别重复追加）。
- finish 帧与流结束时：封掉仍打开的思考（`OnReasoningDone`）与正文块（沿用现有逻辑）。

收尾组消息时，布局里至少有一个签名或 thought part 就设 `Name`/`ReasoningModel`（`executeStreaming` 是方法，用 `c.model`；把它传进 `streamState` 或在收尾处设置均可）。
`:84` 的截断判定、`:88-111` 的判空判定只看正文与调用，不因思考摘要改变。

要点：**不合并带签名的 part 与不带签名的 part**——正文流片段在遇到签名时就此封块；两个都带签名的片段永远落在两个布局 part 里。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/google/ -count=1` → ok。

### Step 5: 投影——产出者拿到原样布局；当前轮无签名调用补占位签名

1. `buildRequest` 的 assistant 分支（277 的围栏规则之后）：

```go
			if layout, ok := geminiReplayLayout(m, model); ok {
				// The model that wrote this reply gets its parts back as it
				// returned them: in their order, thought summaries included,
				// each signature in its own part. The signatures are its
				// reasoning; without them Gemini 3 and later refuse the
				// current turn's calls, and earlier models re-derive it.
				parts := geminiPartsFromLayout(m, layout, toolNames)
				...append model content...
				continue
			}
			// 否则照旧 assistantParts
```

   - `geminiReplayLayout(m, model)`：`strings.TrimSpace(m.ReasoningModel) != ""` 且 `llm.ReplaysReasoningTo(m, model)` 且 `llm.DecodeGeminiParts(m.Name)` 成功。
     （与 275 一样，**空产出者不放行**，不享受 `ReplaysReasoningTo` 的历史豁免。）
   - `geminiPartsFromLayout`：逐个布局 part 重建——`thought` → `part{Text, Thought: true, ThoughtSignature}`；`text` → `part{Text, ThoughtSignature}`（空文本靠 Step 2 的序列化）；
     `call` → 用 `m.ToolCalls[Call]` 按 `assistantParts` 同样的规则建 `functionCall`（没名字的跳过、记 `toolNames`）并挂上签名。
     `Call` 越界（数据损坏）→ 放弃布局、整条退回 `assistantParts`（并 `llm.LogDebug` 一句），不发残缺内容。
2. 当前轮占位签名：`buildRequest` 组完 `req.Contents` 后，找到最后一条 `RoleUser` 消息对应的内容位置（Google 的定义：一轮从最近一条「不是 functionResponse」的 user 消息开始；
   在 `llm` 层就是最后一条 `RoleUser` 消息，`RoleTool` 不算）；在它之后的每个 `roleModel` 内容里，若含函数调用、且**第一个**函数调用 part 的 `ThoughtSignature` 为空，
   就设为常量 `geminiSkipSignatureValidator = "skip_thought_signature_validator"`（常量注释写清：Google 文档给的占位值、只给非 Gemini 生成或签名已丢失的当前轮调用用、会降低效果所以只作兜底）。
   带签名的不动；上一轮及更早的不动。
3. 多轮续写合并（`Execute`，⑤）：`rounds > 1`、最后一轮不是撞上限、且最后一轮的消息带 Gemini carry 时，合并消息的 carry = 布局 `[{Kind:"text", Text: 合并后的正文}]`（正文为空则省略）
   + 最后一轮布局里全部 `call` part（下标不变，对应 `lastToolCalls`），`ReasoningModel` 照抄。其余情况不带 carry。在那段 `// One assistant message carries the whole answer` 注释里补一句为什么。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/google/ -count=1` → ok。

### Step 6: 测试

见「Test plan」。

**Verify**: Test plan 末尾命令 → 全部 PASS。

### Step 7: 回归面

**Verify**: 「Commands」的 llm 各包与回归面命令 → ok；`go vet ./pkg/llm/...` → exit 0；`gofmt -l pkg/llm` → 无输出。
若有测试因为 Gemini 回复现在带 carry（`Name`/`ReasoningModel`）而失败：**仅当**失败恰是「逐字比较了 Google 回复转出的 `llm.Message`」时才可以更新期望并在报告里列出；其他失败 → STOP。

### Step 8: 真机验收（owner 手动）

执行环境没有 Gemini 凭据（`~/.forebrain/.env` 里没有 Google 的 key；Gemini 只经 owner 的 OpenCode 登录可用），且不得读取或拷贝 owner 的登录——
按仓库惯例写成 owner 手动验收清单附在报告里，其余判据全过即可标 DONE。清单：

1. 用本分支构建的 forebrain，选 `opencode-go/gemini-4-argon-contributor`（或任一 `gemini-3*`/`gemini-4*` 模型），在空目录发：
   「用 Go 写一个 `wordfreq` 包和表驱动测试，运行 go test 直到通过。」
2. 期望：整轮完成、`go test` 通过；**没有** `missing a thought_signature` / 4xx 一类报错（修复前按官方文档会在工具循环第二步出现）。
3. `debug.log` 里第二个及以后的请求，`contents` 中 model 内容的第一个 `functionCall` part 带 `thoughtSignature`，且不是 `skip_thought_signature_validator`。
4. `/model` 换到别的模型续聊一句：请求里没有任何 `thoughtSignature` 与 `"thought":true`。
5. 退出后 `forebrain resume <sid>` 再切回同一个 Gemini 模型续聊：请求里历史 model 内容的签名仍在（从库重建的历史同样完整）。

## Test plan

- `pkg/llm/thinking_carry_test.go`：`TestGeminiPartsCarryRoundTripsByteForByte`、`TestGeminiPartsCarryIsStructured`（Step 1）。
- `pkg/llm/google/agent_llm_test.go`：
  - `TestGooglePartJSON`（Step 2）。
  - `TestGoogleUnaryKeepsThoughtSignatures`：响应 parts 为 `[thought("plan", sig A), text("ok"), functionCall a (sig B), functionCall b, text("", sig C)]` →
    `TextContent() == "ok"`、两个调用、carry 布局 5 项逐项相等、`ReasoningModel` = client 模型；sink 回放的 reasoning 为 `"plan"`、正文为 `"ok"`。
  - `TestGoogleReplaysLayoutOnlyToItsProducer`：上面的消息 + 两条工具结果组成历史，`buildRequest(…, 同模型)` → model 内容 parts 依次为
    `{text:"plan",thought:true,thoughtSignature:A}`、`{text:"ok"}`、`{functionCall a, thoughtSignature:B}`、`{functionCall b}`、`{text:"",thoughtSignature:C}`；
    给别的模型 → `{text:"ok"}`、`{functionCall a}`、`{functionCall b}`，且因为在当前轮，`functionCall a` 带 `skip_thought_signature_validator`；
    `ReasoningModel` 为空 → 同「别的模型」。
  - `TestGoogleCurrentTurnUnsignedCallGetsPlaceholder`：历史 `[user, model(无 carry 的调用), tool, user, model(无 carry 的调用), tool]` →
    只有最后一个 model 内容的第一个调用补占位签名；第一个不补；同一 model 内容的第二个调用不补。
  - `TestGoogleThoughtSummaryIsNeverAnswer`：只含 thought part 与一个函数调用的响应 → `msg.Parts` 为空、思考不出现在 `TextContent()`；
    换模型投影时思考文本不出现在请求里。
  - `TestGoogleContinuationKeepsCallSignatures`：两轮——第 1 轮 `text("Half")` 撞 `MAX_TOKENS`，第 2 轮 `text(" rest")` + `functionCall f (sig S)` 以 `STOP` 结束 →
    结果 carry 布局为 `[text "Half rest", call 0 sig S]`；最后一轮撞上限（续写用尽）的用例 → 无 carry。
- `pkg/llm/google/stream_test.go`（照该文件现有 SSE 写法）：
  - `TestGoogleStreamingKeepsThoughtSignatures`：chunk 1 `[thought "pla"]`、chunk 2 `[thought "n" sig A]`、chunk 3 `[text "Hel"]`、chunk 4 `[text "lo"]`、
    chunk 5 `[functionCall f sig B]`、chunk 6 `[text "" sig C]` + `finishReason: STOP` →
    布局 `[thought "plan" A, text "Hello", call 0 B, text "" C]`；sink 收到 reasoning `"pla"`、`"n"` 与一次 `OnReasoningDone`，正文 `"Hel"`、`"lo"`；`TextContent() == "Hello"`。
  - `TestGoogleStreamingNeverMergesSignedParts`：chunk 1 `[text "A" sig S1]`、chunk 2 `[text "B" sig S2]` → 布局两个 text part 各带自己的签名，`TextContent()` 仍为 `"A"` 与 `"B"` 两段的拼接（按 `TextContent` 规则）。

```
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ ./pkg/llm/google/ -count=1 -v \
  -run 'GeminiPartsCarry|TestGooglePartJSON|TestGoogleUnaryKeepsThought|TestGoogleReplaysLayout|TestGoogleCurrentTurnUnsigned|TestGoogleThoughtSummaryIsNever|TestGoogleContinuationKeepsCall|TestGoogleStreamingKeepsThought|TestGoogleStreamingNeverMerges' \
  2>&1 | grep -E '^(--- |ok|FAIL)'
```

→ 10 个新测试全部 `--- PASS`，没有 `FAIL`。

## Done criteria

全部满足：

- [ ] `CGO_ENABLED=1 go build ./...` exit 0
- [ ] 「Commands」的 google 包、llm 各包、回归面命令全部 ok
- [ ] Test plan 的 10 个新测试全部 PASS
- [ ] `go vet ./pkg/llm/...` exit 0；`gofmt -l pkg/llm` 无输出
- [ ] `grep -n 'ThoughtSignature' pkg/llm/google/agent_llm.go pkg/llm/google/stream.go` 两个文件都有命中
- [ ] `grep -n 'skip_thought_signature_validator' pkg/llm/google/agent_llm.go` 恰 1 处（常量定义）
- [ ] `grep -n 'gemini_parts_v1' pkg/llm/retry.go` 恰 1 处
- [ ] `git status --porcelain` 只多出 Scope 里的文件（依赖计划的文件属预期）
- [ ] 手动验收清单已附在报告里
- [ ] `plans/README.md` 本计划那一行已更新

## STOP conditions

- 开头三条依赖检查任一不满足。
- 「Current state」① 的 `part` 已经有 `ThoughtSignature`/`Thought` 字段（说明有人先做了），或 ②③ 的解析循环结构已变。
- `generateContentResponse` 的 candidate content 不是用 `part` 解码的（Step 2 的字段加了也读不到签名）。
- 回归面出现 Step 7 允许范围之外的失败。
- owner 手动验收里仍出现 `missing a thought_signature` / 4xx，或请求里签名位置与本计划不符——记下请求与报错原文报告；**不要**把占位签名扩大到同模型的正常路径来「让它过」。
- 修复需要动 Scope 之外的文件。

## 非功能要求（风险 / 对策 / 验证）

| 维度 | 风险 | 对策 | 验证 |
|---|---|---|---|
| 正确性 | Gemini 3 / 4 当前轮函数调用缺签名 → 4xx | 同模型原样回放；无签名的当前轮调用补官方占位签名 | `TestGoogleReplaysLayoutOnlyToItsProducer`、`TestGoogleCurrentTurnUnsignedCallGetsPlaceholder`、手动验收 |
| 正确性 | 签名换了 part 或被合并 → 失效 | 布局按收到的 part 逐个记录；流式遇签名即封块；不合并两个带签名的片段 | `TestGoogleStreamingKeepsThoughtSignatures`、`TestGoogleStreamingNeverMergesSignedParts` |
| 隐私 / 跨模型 | 思考摘要当正文发给别的模型 | thought part 不进正文；只随 carry 给产出者 | `TestGoogleThoughtSummaryIsNeverAnswer` |
| 鲁棒性 | 布局损坏（下标越界） | 放弃布局退回旧投影，不发残缺内容 | Step 5 的越界分支（评审核对） |
| 延迟 | 无额外请求 | — | — |
| 性能 / 存储 | 带签名的 Gemini 回复，正文在 carry 里再存一份；签名本身也不短 | 只在有签名或思考摘要时才存 carry；`parts` 列是 LONGTEXT / TEXT，无长度问题 | — |
| 缓存 | 回放内容变化导致前缀变一次 | 每条消息的投影只取决于它自己与请求模型，升级后稳定 | 同上测试 |
| 可维护性 | carry 第四种格式 | 全部集中在 `pkg/llm/retry.go`，`StructuredCarry` 与 `ExtractSummary` 一处收口 | `TestGeminiPartsCarryIsStructured` |
| 兼容 | 老会话 | 库结构不变、无需迁移；老行没有 carry → 旧投影 + 当前轮占位签名兜底 | `TestGoogleCurrentTurnUnsignedCallGetsPlaceholder` |

## Maintenance notes

- **四个适配器的口径**（275–278 合起来）：同模型回放全部历史轮的思考；思考排在正文与工具调用之前（Gemini 是「保持原 part 顺序」）；正文与工具调用永不丢；
  只思考没回答的消息只给产出它的模型。新增或修改任一适配器的投影时，按这四条对照。
- **流式中途被打断时的临时消息**：`pkg/run/orchestration_llm.go:530` 在工具边流边执行时，用流式回调拼出一条临时 assistant 消息先落库；
  正常结束时会被带 carry 的最终消息覆盖（`pkg/state/message_sync.go:297-301`）。只有调用被打断（插话抢占、取消、流式重试用已完成部分收尾）时临时消息才留下来，
  它不带任何协议的思考 carry——四个适配器都一样。owner 2026-11-08 要求并入本期：由 **plan 279** 修复（适配器在流式中逐步交出 carry，收集器与取消缓冲挂到被打断的消息上；
  Gemini 投影补「布局之外的正文与调用补在末尾」）。本计划不用为此做任何事，但 Step 4 的布局与 `msg.Parts` 一一对应关系是 279 依赖的前提，不要破坏。
- **占位签名只是兜底**：如果真机日志里在同模型的正常路径上看到 `skip_thought_signature_validator`，说明布局在某处丢了——那是 bug，不要习以为常。
- **评审重点**：①布局与 `msg.Parts`/`msg.ToolCalls` 是同一次遍历产出的（下标对得上）；②流式「遇签名即封块」没有破坏既有的 `OnTextBlockDone` 次序测试；
  ③`ReasoningModel` 为空时不回放；④占位签名只落在「当前轮、每个 model 内容的第一个调用、本身无签名」上。
