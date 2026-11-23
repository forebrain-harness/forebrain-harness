# Plan 280: 思考回放的健壮性补齐——拒收兜底覆盖全部适配器、OpenAI 兼容按原字段名回放、估算只算实际发出的部分

> **Executor instructions**: 按步骤顺序执行，每步验证通过再做下一步。遇到「STOP conditions」就停下报告，
> 不要自己发挥。**不要 commit / push / merge**（仓库规矩：owner 手动提交）。做完后把 `plans/README.md`
> 里本计划那一行的状态改成 DONE 并附一句摘要——除非派你来的评审说索引由他维护。
>
> **本计划硬依赖 plan 275–279 全部落地**（执行序：275、276 → 277 → 278 → 279 → 280）。开工前五条都要满足，任一条不满足就**停下**：
> - `grep -n 'func isThinkingRejection' pkg/llm/anthropic/agent_llm.go` 有命中（275）
> - `grep -n 'func responsesAssistantText' pkg/llm/openai/responses_llm.go` 有命中（276）
> - `grep -n 'func ThinkingFenceGoesTo' pkg/llm/thinking_fence.go` 有命中（277）
> - `grep -n 'func EncodeGeminiParts' pkg/llm/retry.go` 有命中（278）
> - `grep -n 'OnReasoningCarry' pkg/llm/stream.go` 有命中（279）
>
> **Drift check**：`git status --porcelain -- pkg/llm/`。期望：带 275–279 的改动（属预期）。逐条对照「Current state」，**结构对得上就继续**，对不上才 STOP。
> 行号以 `d9c45060` 为准，依赖计划落地后会后移，按符号找。

## Status

- **Priority**: P1（provider 一旦拒收回放的思考，会话从下一次请求起每次都失败，只能新开会话）
- **Effort**: L
- **Risk**: MED（四个适配器的发送路径都加一层兜底；OpenAI 兼容的请求体在少数 provider 上改字段名）
- **Depends on**: plans 275、276、277、278、279
- **Category**: bug
- **Planned at**: commit `d9c45060`, 2026-11-08

## Why this matters

owner 2026-11-08 要求把四个适配器再查一遍；275–279 之后还剩三处缺口，owner 确认全部并入本期：

1. **拒收兜底只有 Anthropic 有**。275 给 Anthropic 加了「provider 以 400 拒收回放的 thinking → 去掉重发一次」（对齐 Claude Code）。另外三个没有：
   - OpenAI 兼容：从 11-07（`ba824f23`）起会把历史各轮的 `reasoning_content` 回放给同一模型。遇到不接受这个字段的 provider，第二个请求起每次 400。
   - Responses：加密思考失效（换了账号、端点不认）时同样每次失败。
   - Google：思考签名失效时同样每次失败（Gemini 3/4 还会对当前轮的签名做严格校验）。
2. **OpenAI 兼容收发的思考字段名不对称**。入站时 `reasoning` 和 `reasoning_content` 都认——`normalizeOpenAICompatReasoningRoundTripper`
   把响应里的 `reasoning` 改名成 `reasoning_content`（`compat_llm.go:204-252`、`:362-450`、`:455-495`）；
   回放时却一律用 `reasoning_content`（go-openai 的 `ChatCompletionMessage` 只会序列化这个名字）。用 `reasoning` 的 provider 会忽略它，
   模型又看不到自己的思考——与 275 修的问题同一类。现有测试 `TestOpenAICompatLLMSurfacesReasoningOnlyResponseAsContent` 的样例响应用的就是 `reasoning`。
3. **token 估算按字节计入整个 carry**（`pkg/llm/tokenizers.go:402-416` 的 `EstimateMessageWith`：`total += EstimateTextWith(e, msg.Name)`），
   包括 JSON 外壳；278 的 Gemini 布局里正文还会再存一份（回放时布局正文与 `Parts` 只发其一），估算偏高、会略早触发压缩。

## 已拍板的决定

- **owner 2026-11-08**：三处缺口全部并入本期。
- **一个兜底口径，四个适配器共用**：
  - 共享判定 `llm.ReasoningReplayRefused(err, statuses, keywords)`：`errors.As` 到 `*llm.APIError`，状态码在给定集合里，且 `Message`/`Code`/`Type` 小写后含任一关键词。
  - 共享剥离 `llm.WithoutReasoningCarries(messages)`：返回副本，所有 assistant 消息上 `llm.Prefix` 开头的 `Name` 清空（`ReasoningModel` 保留——277 的围栏规则只看它）。
  - 触发条件三者同时成立：本次请求**确实回放了**思考、错误被判为拒收、本次 `Execute` 还没用过兜底。重发一次，打一条 `slog.Warn`。
  - 关键词与状态码：

    | 适配器 | 状态码 | 关键词 | 「确实回放了」的判据 |
    |---|---|---|---|
    | Anthropic（275，改用共享判定，行为不变） | 400 | `thinking`、`signature` | 请求含 thinking / redacted_thinking 块 |
    | OpenAI 兼容 | 400 | `reasoning` | 任一出站消息 `ReasoningContent != ""` |
    | Responses | 400、404 | `reasoning`、`encrypted`、`rs_` | 任一 input item 是 reasoning |
    | Google | 400 | `thought_signature`、`thoughtsignature`、`signature` | 任一 part 带来自 carry 的签名（不是占位签名）或是 thought part |

  - 只重发一次：去掉思考后还失败，就是别的原因，按原错误返回。
- **OpenAI 兼容按原字段名回放**：
  - 记录：同一个 RoundTripper 在把响应里的 `reasoning` 改名时，往请求上下文里的观察对象记一笔；客户端据此知道这次回复的思考字段名。
  - 存储：字段名是 `reasoning_content`（绝大多数 provider）时 carry **保持今天的纯文本格式，字节不变**；只有别的名字才用新 payload `openai_compat_reasoning_v1`（`{field, text}`）。
  - 回放：出站消息照常填 `ReasoningContent`；需要别的名字的消息，下标记进请求上下文，由同一个 RoundTripper 在发出前把请求体里该条消息的 `reasoning_content` 键改名。
  - 279 的流式逐步交 carry 改用同一个编码函数，否则被打断的回复会记成默认名字。
- **估算只算实际会发出的部分**：新增 `llm.CarryEstimateText(name)`——纯文本 / OpenAI 兼容 payload：思考原文；Responses：各条目摘要 + 加密内容；
  Anthropic：thinking 文本 + 签名 + redacted 的 data；Gemini：thought 文本 + 各签名（**不含** `text` 类 part 的正文，那与 `Parts` 重复）；不是 carry 的 `Name` 照旧原样计。

## Current state

**① 275 的 Anthropic 兜底**（275 落地后 `pkg/llm/anthropic/agent_llm.go`）：`isThinkingRejection(err)` 在 SDK 原始错误上 `errors.As(*anthropicapi.Error)`、要求 400、
`RawJSON()` 小写含 `thinking` 或 `signature`；`Execute` 循环里若请求带 thinking 且命中，就把 `replayTo` 置空重建参数重发一次。
本包 `normalizeError`（`error.go:25-46`）把 SDK 错误转成 `*llm.APIError`，`Message` 是整段响应体。

**② OpenAI 包的错误**（`pkg/llm/openai/error.go:23` 起，`normalizeError`）：`*openaiclient.APIError` → `llm.APIError{StatusCode: HTTPStatusCode, Message: Message, …}`；
`*openaigo.Error`（Responses）→ `llm.APIError{StatusCode, Message, …}`。两个客户端的 `Execute` 都在 `defer` 里统一 `normalizeError`。

**③ Google 的错误**：`newAPIError(status, payload, retryAfter)`（`agent_llm.go:542-560`）直接返回 `*llm.APIError`（`Message` 为解析出的错误消息或截断的原文，另有 `Code`、`Type`）；
流式读取中途的错误包在 `llm.NewExecuteError` 里（有 `Unwrap`）。`Execute` 在开头 `defer endSink.OnEnd()`（`:80-83`）。

**④ OpenAI 兼容的发送路径**：`Execute`（`compat_llm.go:1103` 起，`:1112` `msgs := compatMessagesToOpenAI(messages, c.model)`，之后续写多轮用 `roundMsgs`）；
`ExecuteStructured`（`:1215` 起，`:1228` 同样构造）。`OnEnd` 在流读完处触发（`:884`、`:911`、`:1045`）——连接阶段就 400 的请求不会触发它。
请求体由 go-openai 序列化；所有 HTTP 流量经过 `normalizeOpenAICompatReasoningRoundTripper.RoundTrip`（`:204`），它能拿到 `req.Context()`、能改写请求与响应体。

**⑤ Responses 的发送路径**：`Execute`（`responses_llm.go:611` 起，`:646` `c.newResponseParams(ctx, conversation, tools, false)`）、`ExecuteStructured`（`:771` 起）。
远端压缩 `Compact`（`:988` 起）失败时上层已有本地压缩兜底（`pkg/assembly/service.go:429` `localFallbackAfterRemoteFailure`），本计划不加。

**⑥ carry 编解码**（275–278 之后的 `pkg/llm/retry.go`）：`Prefix`；Responses `Encode`/`Decode`；`EncodeThinking`/`DecodeThinking`；`EncodeGeminiParts`/`DecodeGeminiParts`；
`StructuredCarry`；`ExtractSummary`。OpenAI 兼容的纯文本 carry 由 `compatMessageFromOpenAI` 拼成 `openAIReasoningCarryPrefix + reasoning`；
279 让它在流式里于 `OnReasoningDone` 处用同样拼法交 `OnReasoningCarry`。投影端 `compatMessageToOpenAI` 用 `!llm.StructuredCarry(m.Name) && …` 决定是否填 `ReasoningContent`。

**⑦ 估算**（`pkg/llm/tokenizers.go:402-430`）：`EstimateMessageWith` 计 `TextContent()`、整个 `msg.Name`、工具调用、压缩项的加密内容、AgentMessage。注释说 Name 是「把模型自己的思考还给它」的载体。

**⑧ 仓库约定**：注释写「为什么」；`pkg/llm` 导出函数必须有 doc comment；日志用 `slog.Warn("…", "key", value)`。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| llm 各包 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... -count=1` | ok（`pkg/llm` 本身约 2.5 分钟） |
| 单包快测 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/<包>/ -count=1 -run '<名字>' -v` | 指定测试 PASS |
| 回归面 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ ./pkg/turn/ ./pkg/state/ ./pkg/assembly/ -count=1` | ok |
| 静态检查 | `go vet ./pkg/llm/...` | exit 0 |
| 格式 | `gofmt -l pkg/llm` | 无输出 |

## Scope

**In scope**：
- `pkg/llm/reasoning_replay.go`（新建）+ `pkg/llm/reasoning_replay_test.go`（新建）—— 共享判定与剥离
- `pkg/llm/retry.go` + `pkg/llm/thinking_carry_test.go` —— OpenAI 兼容字段名 payload、`StructuredCarry`/`ExtractSummary` 认它、`CarryEstimateText`
- `pkg/llm/tokenizers.go` + `pkg/llm/tokenizers_test.go` —— 估算改用 `CarryEstimateText`
- `pkg/llm/anthropic/agent_llm.go` —— `isThinkingRejection` 改用共享判定
- `pkg/llm/openai/compat_llm.go`、`pkg/llm/openai/responses_llm.go` 及测试
- `pkg/llm/google/agent_llm.go` 及测试

**Out of scope**：
- Responses 的 `Compact`——失败已有本地压缩兜底。
- 拒收之后「记住这个 provider 不收思考、以后都不发」——本计划只做每次请求的一次性兜底（与 Claude Code 相同），不引入跨请求状态。
- Codex 会话导入丢弃加密思考、内联 `<think>` 标签、同 provider 换模型时远端压缩检查点的可读性——owner 2026-11-08 决定本期不做。

## Steps

### Step 1: 共享判定与剥离

新建 `pkg/llm/reasoning_replay.go`：

```go
// ReasoningReplayRefused reports a request the provider refused because of
// the reasoning it replayed: an APIError whose status is one of statuses and
// whose message, code or type names one of keywords (case-insensitive). The
// adapter that sent the reasoning decides the statuses and keywords, and
// resends once without it — a refusal that otherwise fails every request of
// the conversation from here on.
func ReasoningReplayRefused(err error, statuses []int, keywords ...string) bool

// WithoutReasoningCarries returns a copy of messages in which no assistant
// message carries reasoning for the model: every Prefix carry in Name is
// cleared. ReasoningModel is kept — it records who produced the message, which
// rules other than reasoning replay still read.
func WithoutReasoningCarries(messages []Message) []Message
```

`WithoutReasoningCarries` 只浅拷贝切片与被改动的消息（不改入参）。测试 `pkg/llm/reasoning_replay_test.go`：
- `TestReasoningReplayRefused`：400 + 含关键词 → true；状态码不在集合 → false；不含关键词 → false；非 `APIError` → false；包在 `NewExecuteError` 里也能识别。
- `TestWithoutReasoningCarries`：四种 carry 都被清空、`ReasoningModel` 保留、非 carry 的 `Name`（参与者名）保留、user/tool 消息不变、入参未被修改。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ -count=1 -run 'ReasoningReplayRefused|WithoutReasoningCarries' -v` → PASS。

### Step 2: Anthropic 改用共享判定（行为不变）

`isThinkingRejection(err)` 改为 `llm.ReasoningReplayRefused(normalizeError(err), []int{http.StatusBadRequest}, "thinking", "signature")`
（`normalizeError` 后 `Message` 就是 `RawJSON()`，判定结果与 275 相同）。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/anthropic/ -count=1` → ok（275 的 `TestAnthropicThinkingRejectionResendsWithoutIt`、`TestAnthropicUnrelatedBadRequestIsNotResent` 原样通过）。

### Step 3: OpenAI 兼容的拒收兜底

把 `Execute` 的现有函数体挪进 `func (c *openAICompatLLM) execute(ctx, messages, tools) (*llm.Result, bool, error)`——第二个返回值表示这次请求是否回放了思考（`msgs` 里任一条 `ReasoningContent != ""`）。
`Execute`（保留 `defer normalizeError`）：

```go
	res, replayed, err := c.execute(ctx, messages, tools)
	if err != nil && replayed && llm.ReasoningReplayRefused(normalizeError(err), []int{http.StatusBadRequest}, "reasoning") {
		slog.Warn("openai compat: provider refused the replayed reasoning; resending without it", "model", c.model, "err", err)
		res, _, err = c.execute(ctx, llm.WithoutReasoningCarries(messages), tools)
	}
	return res, err
```

`ExecuteStructured` 照同样写法包一层。`OnEnd` 不会重复：连接阶段的 400 在读流之前返回（④）——用 Step 8 的测试钉住「两次尝试只触发一次 `OnEnd`」。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/openai/ -count=1` → ok。

### Step 4: Responses 的拒收兜底

同 Step 3 的结构：`Execute` 与 `ExecuteStructured` 各包一层；「回放了」= 构造出的 `params` 里任一 input item 是 reasoning（`OfReasoning != nil`）；
判定 `llm.ReasoningReplayRefused(normalizeError(err), []int{http.StatusBadRequest, http.StatusNotFound}, "reasoning", "encrypted", "rs_")`；剥离后重发一次、`slog.Warn`。
`Compact` 不包（见 Scope）。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/openai/ -count=1` → ok。

### Step 5: Google 的拒收兜底

同样结构包 `Execute`；把 `defer endSink.OnEnd()` 移到外层（每次 `Execute` 只触发一次）。「回放了」= `buildRequest` 产出的任一 part 带来自布局的签名（不等于 278 的占位常量）或是 thought part——
让 `buildRequest` 顺带返回这个布尔值，或在外层扫描请求体结构，选改动小的；判定 `llm.ReasoningReplayRefused(err, []int{http.StatusBadRequest}, "thought_signature", "thoughtsignature", "signature")`。
剥离后，278 的规则自动生效：没有 carry 的历史按旧投影，当前轮第一个无签名调用补占位签名。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/google/ -count=1` → ok。

### Step 6: OpenAI 兼容按原字段名回放

1. `pkg/llm/retry.go`：

```go
// compatReasoningPayloadVersion marks the reasoning of an OpenAI-compatible
// reply whose provider named the field something other than
// reasoning_content. The common case stays a plain carry, byte for byte as
// before; only another field name needs recording, so it can be replayed
// under the name the provider reads.
const compatReasoningPayloadVersion = "openai_compat_reasoning_v1"

// EncodeCompatReasoning returns the carry for reasoning an OpenAI-compatible
// provider returned under field: a plain carry for reasoning_content (or ""
// field), a payload naming the field otherwise; "" when text is empty.
func EncodeCompatReasoning(field, text string) string

// DecodeCompatReasoning reads an OpenAI-compatible reasoning carry: the payload
// above, or a plain carry (field reasoning_content). It reports false for every
// other carry.
func DecodeCompatReasoning(name string) (field, text string, ok bool)
```

   `DecodeCompatReasoning` 对纯文本 carry 的判定：以 `Prefix` 开头、且 `Decode`/`DecodeThinking`/`DecodeGeminiParts`/新 payload 都不认 → 纯文本。
   `StructuredCarry` 把新 payload 也算作结构化（别的协议不得把它当纯文本）；`ExtractSummary` 对新 payload 返回 `text`。

2. 记录入站字段名：`compat_llm.go` 加一个请求级观察对象（如 `type compatReasoningField struct{ mu sync.Mutex; field string }`），由客户端在每次请求前放进 `ctx`。
   `normalizeReasoningAliases` 把 `reasoning` 改名成 `reasoning_content` 时，通过 `RoundTrip` 能拿到的 `req.Context()` 记下 `field = "reasoning"`
   （非流式 `:248` 与流式逐行 `:438` 两条路径都要记；需要把观察对象从 `RoundTrip` 传进这两个函数）。
   `compatMessageFromOpenAI` 加一个参数 `field string`，三处调用（`:1091`、`:1140`、`:1268`）传观察到的字段名（未观察到就 `""`）；其中拼 carry 的地方改用 `llm.EncodeCompatReasoning(field, reasoning)`。
   279 在 `OnReasoningDone` 处交 carry 的三处，也改用 `llm.EncodeCompatReasoning(<观察到的字段名>, <累积思考>)`。
3. 出站：`compatMessageToOpenAI` 的回放分支改为——`llm.DecodeCompatReasoning(m.Name)` 成功且 `llm.ReplaysReasoningTo(m, model)` 且不是围栏消息（277）时填 `ReasoningContent = text`；
   字段名不是 `reasoning_content` 时，把**该条消息在请求 `messages` 数组里的下标**记进请求级的「改名表」（同样经 `ctx` 交给 `RoundTrip`）。
   `compatMessagesToOpenAI` 因 277 会跳过某些消息，下标要按**实际输出数组**算。`RoundTrip` 发请求前，若改名表非空：解析请求 JSON，把这些下标处消息的 `reasoning_content` 键改名为记录的名字，再发出。
4. 注释写清：大多数 provider 用 `reasoning_content`，那条路径字节不变；这里只为用别的名字的 provider 回放到它会读的字段上。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ ./pkg/llm/openai/ -count=1` → ok。

### Step 7: 估算只算实际发出的部分

`pkg/llm/retry.go` 新增：

```go
// CarryEstimateText is the part of a Name that a request can actually carry,
// for estimating its size: a carry's replayable content without its JSON
// envelope, and without answer text a Gemini layout repeats from Parts.
// A Name that is not a carry is returned as it is.
func CarryEstimateText(name string) string
```

按「已拍板的决定」的规则逐种实现（各部分用 `"\n"` 连接即可，只为估算）。`pkg/llm/tokenizers.go` 的 `total += EstimateTextWith(e, msg.Name)` 改成 `EstimateTextWith(e, CarryEstimateText(msg.Name))`，并更新那段注释。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ -count=1` → ok。

### Step 8: 测试

见「Test plan」。

**Verify**: Test plan 末尾命令 → 全部 PASS。

### Step 9: 回归面

**Verify**: 「Commands」的 llm 各包与回归面命令 → ok；`go vet ./pkg/llm/...` → exit 0；`gofmt -l pkg/llm` → 无输出。
`pkg/run` / `pkg/assembly` 里若有测试的压缩触发点因估算变化而移动：**仅当**差异恰是「carry 不再按 JSON 全文计」导致的数值变化时才可以更新期望并逐条列出；其他失败 → STOP。

### 真机验收：不做

三项都无法在真机上稳定触发：拒收要 provider 主动拒绝回放；字段名差异要恰好用到返回 `reasoning` 的 provider（owner 当前配置里是否有无法确认）；估算是纯计算。
全部由单测确定，按仓库惯例只验有意义的项。

## Test plan

- `pkg/llm/reasoning_replay_test.go`：`TestReasoningReplayRefused`、`TestWithoutReasoningCarries`（Step 1）。
- `pkg/llm/thinking_carry_test.go`：
  - `TestCompatReasoningCarryKeepsTheCommonCaseBytes`：`EncodeCompatReasoning("reasoning_content", "x") == llm.Prefix+"x"`，`EncodeCompatReasoning("", "x")` 同；
    `EncodeCompatReasoning("reasoning", " a\nb ")` 解码回 `("reasoning", " a\nb ")`，文本逐字节不变。
  - `TestCompatReasoningPayloadIsItsOwn`：新 payload 对 `Decode`/`DecodeThinking`/`DecodeGeminiParts` 都为 false、`StructuredCarry` 为 true、`ExtractSummary` 返回原文；
    `DecodeCompatReasoning` 对 Responses / Anthropic / Gemini payload 为 false、对纯文本 carry 为 `("reasoning_content", 文本, true)`。
  - `TestCarryEstimateText`：五种 carry 各一例——不含 JSON 外壳、Gemini 不含 `text` part 的正文、非 carry 的 `Name` 原样返回。
- `pkg/llm/tokenizers_test.go`：`TestEstimateMessageCountsOnlyWhatACarrySends`——同一条 Gemini 消息，估算值等于「正文 + thought 文本 + 签名」的估算，而不是「正文 + 整段 JSON」。
- `pkg/llm/openai/compat_llm_test.go`：
  - `TestCompatResendsWithoutReasoningWhenRefused`：带同模型 carry 的历史；服务器对含 `reasoning_content` 的请求回 400（消息含 `reasoning_content`）、对不含的正常回答 →
    恰 2 个请求、第 2 个无 `reasoning_content`、成功、`OnEnd` 只触发一次。
  - `TestCompatDoesNotResendUnrelatedFailures`：400 不含关键词 → 1 个请求；请求本身没回放思考时的 400（含关键词）→ 1 个请求。
  - `TestCompatReplaysReasoningUnderTheFieldItCameIn`：第一次响应用 `reasoning` 字段返回思考 + 工具调用 → 结果 carry 解出 `field == "reasoning"`；
    用它组历史再请求 → 请求体里该 assistant 消息有 `reasoning` 键、没有 `reasoning_content` 键；同一测试里另一条以 `reasoning_content` 返回的历史消息仍是 `reasoning_content`（字节与改动前相同）。
  - `TestCompatStreamRecordsTheReasoningField`：流式 SSE 用 `reasoning` 字段 → 结果 carry 与 279 交出的 carry 都记成 `reasoning`。
- `pkg/llm/openai/responses_llm_test.go`：`TestResponsesResendsWithoutReasoningWhenRefused`（404，消息形如 `Item with id 'rs_x' not found`）与
  `TestResponsesDoesNotResendUnrelatedFailures`。
- `pkg/llm/google/agent_llm_test.go`：`TestGoogleResendsWithoutSignaturesWhenRefused`——带同模型布局的历史，服务器对含 carry 签名的请求回 400（消息含 `thought_signature`）→
  第 2 个请求里没有布局签名、当前轮第一个调用带占位签名、成功、`OnEnd` 只触发一次；`TestGoogleDoesNotResendUnrelatedFailures`。

```
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... -count=1 -v \
  -run 'ReasoningReplayRefused|WithoutReasoningCarries|CompatReasoningCarry|CompatReasoningPayload|CarryEstimateText|CountsOnlyWhatACarrySends|ResendsWithout|DoesNotResendUnrelated|UnderTheFieldItCameIn|RecordsTheReasoningField|ThinkingRejection|UnrelatedBadRequest' \
  2>&1 | grep -E '^(--- |ok|FAIL)'
```

→ 本计划的新测试与 275 的两条 Anthropic 兜底测试全部 `--- PASS`，没有 `FAIL`。

## Done criteria

全部满足：

- [ ] `CGO_ENABLED=1 go build ./...` exit 0
- [ ] 「Commands」的全部测试命令 ok；`go vet` exit 0；`gofmt -l` 无输出
- [ ] Test plan 的测试全部 PASS
- [ ] `grep -rn 'ReasoningReplayRefused(' pkg/llm/anthropic pkg/llm/openai pkg/llm/google --include='*.go' | grep -v _test.go` 在 anthropic、compat、responses、google 四个文件里都有命中
- [ ] `grep -n 'EstimateTextWith(e, msg.Name)' pkg/llm/tokenizers.go` 无输出
- [ ] `grep -rn 'openAIReasoningCarryPrefix + ' pkg/llm/openai/compat_llm.go` 无输出（拼 carry 一律经 `llm.EncodeCompatReasoning`）
- [ ] `git status --porcelain` 只多出 Scope 里的文件（依赖计划的文件属预期）
- [ ] `plans/README.md` 本计划那一行已更新

## STOP conditions

- 开头五条依赖检查任一不满足。
- go-openai 的流式请求不经过 `normalizeOpenAICompatReasoningRoundTripper`（出站改名就做不到）——报告，不要改用别的 HTTP 客户端。
- 某个适配器的 400 在读流途中才出现、且此前已经推过内容给 sink——兜底会让界面看到两次输出；报告该适配器与场景。
- 回归面出现 Step 9 允许范围之外的失败。
- 修复需要动 Scope 之外的文件。

## 非功能要求（风险 / 对策 / 验证）

| 维度 | 风险 | 对策 | 验证 |
|---|---|---|---|
| 鲁棒性 | provider 拒收回放的思考 → 会话从此每步失败 | 四个适配器同一兜底：判为拒收即去掉思考重发一次 | 各适配器的 `ResendsWithout…` 测试 |
| 正确性 | 误判把无关的 400 也重发 | 状态码 + 关键词 + 「本次确实回放了」三条同时成立；只重发一次 | `DoesNotResendUnrelatedFailures` 系列 |
| 延迟 | 拒收时多一次往返 | 只在被拒时发生，且只一次 | 同上 |
| 体验 | 兜底重发让界面看到两次输出 | 拒收发生在连接阶段，未推任何内容；`OnEnd` 每次 `Execute` 只一次 | `OnEnd` 计数断言 |
| 正确性 | 用 `reasoning` 字段的 provider 看不到自己的思考 | 按入站字段名回放 | `TestCompatReplaysReasoningUnderTheFieldItCameIn` |
| 缓存 / 兼容 | 改动波及绝大多数 provider 的请求字节 | `reasoning_content` 路径的 carry 与请求体逐字节不变 | `TestCompatReasoningCarryKeepsTheCommonCaseBytes` |
| 性能 | 出站改名要解析一次请求 JSON | 只在改名表非空时发生 | — |
| 准确性 | 估算偏高、过早压缩 | 只计实际可发出的内容 | `TestEstimateMessageCountsOnlyWhatACarrySends` |
| 可维护性 | 四个适配器各写一套兜底 | 判定与剥离都是 `pkg/llm` 的共享函数，各适配器只填状态码与关键词 | Done criteria 的 grep |

## Maintenance notes

- **新增适配器时**：最终消息挂 carry（275–278）、流式逐步交 carry（279）、拒收兜底用共享函数（本计划）、估算规则加一支（`CarryEstimateText`）——四件事缺一不可。
- **兜底只管「这一次请求」**：若某 provider 持续拒收，日志会每步一条 `slog.Warn`。真遇到时再考虑记住该 provider 不收思考，那需要跨请求状态与 owner 拍板，另立计划。
- **字段名只认 `reasoning` 与 `reasoning_content`**：入站别名表在 `normalizeReasoningAliases`。以后若加别的别名（例如 `thinking`），入站、存储、出站三处要一起加。
- **评审重点**：①`reasoning_content` 路径确实逐字节不变；②出站改名的下标按 277 跳过消息之后的实际数组计算；③Google 的 `OnEnd` 已移到外层；④Anthropic 改用共享判定后 275 的两条测试原样通过。
