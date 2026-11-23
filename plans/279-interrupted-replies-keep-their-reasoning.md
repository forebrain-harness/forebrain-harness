# Plan 279: 流式中途被打断时，留在历史里的 assistant 消息也带上思考记录

> **Executor instructions**: 按步骤顺序执行，每步验证通过再做下一步。遇到「STOP conditions」就停下报告，
> 不要自己发挥。**不要 commit / push / merge**（仓库规矩：owner 手动提交）。做完后把 `plans/README.md`
> 里本计划那一行的状态改成 DONE 并附一句摘要——除非派你来的评审说索引由他维护。
>
> **本计划硬依赖 plan 275–278 全部落地**（执行序：275、276 → 277 → 278 → 279）。开工前四条都要满足，任一条不满足就**停下**：
> - `grep -n 'func EncodeThinking' pkg/llm/retry.go` 有命中（275）
> - `grep -n 'func responsesAssistantText' pkg/llm/openai/responses_llm.go` 有命中（276）
> - `grep -n 'func ThinkingFenceGoesTo' pkg/llm/thinking_fence.go` 有命中（277）
> - `grep -n 'func EncodeGeminiParts' pkg/llm/retry.go` 与 `grep -n 'ThoughtSignature' pkg/llm/google/stream.go` 都有命中（278）
>
> **Drift check**：`git status --porcelain -- pkg/llm/stream.go pkg/run/response_collector.go pkg/run/orchestration_llm.go pkg/turn/session.go pkg/tui/chat_turn.go pkg/tui/chat_session.go pkg/gateway/run_control.go`
> → 写计划时这些文件无改动（275–278 不碰它们）。有输出就逐条对照「Current state」：**结构对得上就继续**，对不上才 STOP。
> 适配器文件带 275–278 的改动属预期；行号以 `d9c45060` 为准，按符号找。

## Status

- **Priority**: P1（插话、Esc、流式重试收尾之后，被打断的那条回复丢掉了模型的思考；对 Anthropic 协议的思考模型，正是 275 修的那种「看不到自己上一步」）
- **Effort**: M–L
- **Risk**: MED（动引擎的回复收集器、两个界面的取消缓冲与四个适配器的流式回调；正常完成的回复不受影响）
- **Depends on**: plans 275、276、277、278
- **Category**: bug
- **Planned at**: commit `d9c45060`, 2026-11-08

## Why this matters

275–278 让四个适配器把思考记录（`llm.Message.Name` 里的 carry + `ReasoningModel`）挂在**适配器返回的那条消息**上。
但有几条路径里，进入会话历史的 assistant 消息**不是**适配器返回的那条，而是引擎或界面用流式回调**自己拼出来的**——它们只有正文和工具调用，没有思考记录：

1. **工具边流边执行时的临时消息**：模型还在输出，第一个工具已经开跑，引擎先拼一条临时 assistant 消息落库（`pkg/run/orchestration_llm.go:530`）。
   正常收尾时它会被带思考记录的最终消息补写覆盖；但调用若被打断，它就原样留在历史里。
2. **插话抢占（Esc 发送排队消息）**：被放弃的那次调用，已经说出的话用收集器拼成消息放进会话，然后带着插话**在同一轮里继续请求**（`:629`、`:640-651`）。
   下一次请求里，这条消息没有思考——对 Anthropic 协议的思考模型，等于在插话之后又回到「不知道自己刚才为什么这么做」。
3. **取消（已有工具在跑）**：`:691` 用收集器拼出的消息 + 工具结果落库后结束本轮。
4. **流式重试用已完成部分收尾**（`llm.PartialResponseError`）：`:575-582` 直接拿收集器的结果当这次回答。
5. **取消（还没有工具在跑）**：界面把已经显示出来的半截正文存成一条 assistant 消息（`pkg/turn/session.go:188-190`），同样没有思考记录。

这些消息之后都会作为历史发给模型。本计划让它们也带上「被打断那一刻已经完整的思考」，四个协议一样。

## 已拍板的决定

- **owner 2026-11-08**：这个局限必须并入本期修掉。
- **适配器在流式过程中逐步交出思考记录**：`llm.StreamSink` 加一个回调 `OnReasoningCarry(carry, model string)`。
  适配器每当一段思考**完整**（可以安全回放）时就调用一次，传入「到此为止」的完整 carry（与它最终会挂在消息上的格式相同，后一次替换前一次）。
  只交完整的单元：Anthropic 是收到 `content_block_stop` 的 thinking / redacted_thinking 块（签名已到）；Responses 是 `output_item.done` 的 reasoning item（加密内容已到）；
  OpenAI 兼容是思考段结束（开始出正文或工具调用）；Gemini 是 278 布局里已封口的 part。半截的思考块不交——没签名的块回放会被拒。
- **收集器与取消缓冲都记录它**：引擎的 `responseCollector` 记录最新 carry，凡是用收集器拼消息的地方（上面 1–4）都挂上；
  界面的 `turn.StreamPartial` 也记录它，取消时存半截正文那条消息（5）挂上。传输层重发（`OnTransportRetry` 重发 / 撤回已完成思考）时清空，与正文一起作废。
- **回放规则不变**：这些消息带上 carry 之后，走的仍是 275–278 的投影规则（只给产出者、思考在前、Gemini 按布局）。
  Gemini 的布局对打断的消息只覆盖前缀，所以 Gemini 投影补一条规则：**布局之外的正文与工具调用按原顺序补在末尾、不带签名**（Step 6）。
- **非流式路径也交**：非流式结果整块回放给 sink（`llm.ReplayToSink`）之前先交 carry，保证回放出来的工具调用触发临时消息时 carry 已在。

## Current state

行号基于 `d9c45060`。

**① `llm.StreamSink`**（`pkg/llm/stream.go:12` 起）：回调有 `OnDelta`、`OnReasoningDelta`、`OnReasoningDone`、`OnResponseStarted`、`OnWebSearch`、
`OnUsage`、`OnUsageSnapshot`、`OnResponseCompleted`、`OnToolCallReady`、`OnTextBlockDone`、`OnStopReason` 等，每个字段都有说明注释。
引擎各层包装 sink 一律 `wrapped := *sink` 后改个别字段（`pkg/run/response_collector.go:50`、`pkg/run/orchestration_llm.go:539`），新字段会被原样带过去。
显式新建 sink 的只有三处：`pkg/tui/chat_turn.go`、`pkg/run/subagent.go:1272`（`EventStreamSink`，网关与子代理视图共用）、`pkg/assembly/compact_lifecycle.go:237`（压缩，不涉及会话历史）。

**② 引擎的回复收集器**（`pkg/run/response_collector.go`）：

```go
type responseCollector struct {
	mu        sync.Mutex
	current   strings.Builder // the text block being streamed
	completed []string        // finished text blocks, in order
	ready     []llm.ToolCall  // finished tool calls, in order
}
```

`withResponseCollector`（`:40-74`）包一层 sink：`OnDelta` 只在下游有时才串接；`OnTextBlockDone`、`OnToolCallReady`、`OnTransportRetry` 下游为 nil 时也装「只记录」版本。
`noteTransportRetry`（`:110` 起）在重发或 `DropFinishedThinking` 时清空全部、在追认 Cleared 时只丢当前块。
`result()`（`:127-138`）：`&llm.Result{Message: &llm.Message{Role: llm.RoleAssistant, Parts: parts, ToolCalls: calls}}`；`completedParts()` 返回已封口的正文块。
测试：`pkg/run/response_collector_test.go`（`TestResponseCollectorCollectsBlocksAndCalls` `:10`、`TestResponseCollectorTransportRetryWithdraws` `:70`、`TestResponseCollectorWorksWithoutASink` `:105`）。

**③ 用收集器拼消息的四处**（`pkg/run/orchestration_llm.go`）：
- `:530` 工具开跑前：`prov := llm.AssistantMessage(collector.completedParts(), readyUpTo...)`
- `:575-582` 流式重试用已完成部分收尾：`res, err = collector.result(), nil`
- `:629` 插话抢占：`answer := collector.result().Message`，随后放进会话并**继续本轮**
- `:691` 取消且已有工具在跑：`prov := collector.result().Message`

正常收尾时，最终消息补写临时行的逻辑在 `pkg/state/message_sync.go:297-301`（「首个调用开始时写的行没有思考，最终消息补上」）——不用改。
测试范例：`pkg/run/streaming_tools_test.go:877` `TestProvisionalAssistantRowIsCompletedNotDuplicated`、
`pkg/run/orchestration_llm_test.go:5720` `TestInterruptToSendStopsTheCallAndContinuesTheTurn`。

**④ 界面的取消缓冲**（`pkg/turn/session.go:544-627`）：`StreamPartial` 记 `content` 与 `reasoning`；`ResponseCompleted()` 清正文（完成的回复已进会话）、
`ResponseReasoningDone()` 清思考（已单独落库为思考行）、`Reset()` 两个都清（传输重试）。
`CancelledTurn`（`:130` 起）有 `Captured`、`PartialText`、`PartialReasoning`、`Model` 等字段；`PersistCancelledTurn`（`:162-214`）在 `:188-190`
把半截正文存成 `llm.AssistantMessage([]llm.ContentPart{llm.Text(partialText)})`。
`PersistCancelledTurn` 的调用方三处：`pkg/tui/chat_session.go:2219`、`pkg/gateway/run_control.go:988`、`pkg/process/one_shot.go:123`（只传 `Captured`，不涉及半截正文——由收集器那边覆盖）。
`StreamPartial` 的喂入：`pkg/tui/chat_turn.go:470-490`（`accum.AppendReasoning` / `accum.ResponseReasoningDone`）、`pkg/gateway/run_control.go:930-955`（`withAnswerStream`）。

**⑤ 各适配器可交出 carry 的位置**（275–278 落地后按符号找）：
- Anthropic `executeStream`：`res.Accumulate(event)` 之后那段（`d9c45060` 的 `:449-462`，已有 `content_block_stop` 时对 `tool_use` 调 `OnToolCallReady`）——在同一处对 `thinking` / `redacted_thinking` 交 carry；
  `executeUnary` 的 `llm.ReplayToSink`（`:547`）之前。
- Responses `executeStreaming`：`response.output_item.done` 里 `extractReasoningCarryItem(done.Item)` 之后（`d9c45060` `:1579` 一带）；非流式 `:740` 的 `ReplayToSink` 之前。
- OpenAI 兼容：三处 `sink.OnReasoningDone()`（`:909`、`:1043`、`:1078`）——思考段结束；非流式 `:1156` 的 `ReplayToSink` 之前。
- Google：278 Step 4 的各个封口点（thought 带签名封口、函数调用、带签名的正文封块、空文本 + 签名）；非流式 `executeUnaryRound` 的 `ReplayToSink`（`:191`）之前。

**⑥ 仓库约定**：注释写「为什么」、完整英文句子；`StreamSink` 每个字段都有注释说明何时触发、谁该实现；测试先读同文件的现有测试照写。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| llm 各包 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... -count=1` | ok（`pkg/llm` 本身约 2.5 分钟） |
| run | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1` | ok |
| turn / 界面 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/ ./pkg/tui/ ./pkg/gateway/ ./pkg/process/ -count=1` | ok |
| 回归面 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ ./pkg/agent/ ./pkg/memory/ -count=1` | ok |
| 静态检查 | `go vet ./pkg/llm/... ./pkg/run/ ./pkg/turn/ ./pkg/tui/ ./pkg/gateway/` | exit 0 |
| 格式 | `gofmt -l pkg/llm pkg/run pkg/turn pkg/tui pkg/gateway` | 无输出 |

`FOREBRAIN_HOME` 必须显式指向 mktemp 目录（仓库硬规矩）。

## Scope

**In scope**：
- `pkg/llm/stream.go` —— 新回调字段
- `pkg/llm/anthropic/agent_llm.go`、`pkg/llm/openai/compat_llm.go`、`pkg/llm/openai/responses_llm.go`、`pkg/llm/google/agent_llm.go`、`pkg/llm/google/stream.go` 及各自测试文件 —— 交出 carry；Google 投影的前缀规则
- `pkg/run/response_collector.go` + `pkg/run/response_collector_test.go`
- `pkg/run/orchestration_llm.go` —— 只改 `:530` 那一处临时消息（其余三处走 `collector.result()`，改收集器即可）
- `pkg/run/streaming_tools_test.go`、`pkg/run/orchestration_llm_test.go` —— 新测试
- `pkg/turn/session.go` + 测试 —— `StreamPartial` 记 carry、`CancelledTurn` 加字段、半截正文消息挂 carry
- `pkg/tui/chat_turn.go`、`pkg/tui/chat_session.go`、`pkg/gateway/run_control.go` —— 接线

**Out of scope**：
- `pkg/run/subagent.go` 的 `EventStreamSink` —— 它只把流式内容发成运行事件，不拼历史消息；子代理的打断由引擎的收集器覆盖（与主代理同一套 orchestration）。
- `pkg/assembly/compact_lifecycle.go` —— 压缩不产生会话里的 assistant 消息。
- `pkg/state/` 的补写逻辑、`pkg/process/one_shot.go` —— 不用改。
- 275–278 定下的回放规则本身——只在 Google 投影上加「布局之外的补在末尾」一条。

## Steps

### Step 1: `llm.StreamSink` 加回调

在 `OnReasoningDone` 附近加：

```go
	// OnReasoningCarry hands over the reasoning carry of the response still
	// being streamed, as it stands now: the same carry the finished message
	// will hold in Name, limited to the reasoning that is already complete
	// and safe to replay — a thinking block whose signature arrived, a
	// reasoning item whose encrypted content arrived. model is the model that
	// produced it. Each call replaces the previous one. A reply that is cut
	// off is finished from what the stream delivered; this is what keeps that
	// reply's reasoning with it.
	OnReasoningCarry func(carry, model string)
```

**Verify**: `CGO_ENABLED=1 go build ./...` → exit 0。

### Step 2: 四个适配器在思考完整时交 carry

每个适配器加一个小 helper（各自包内，例如 `publishReasoningCarry(sink *llm.StreamSink, carry, model string)`：`sink == nil || sink.OnReasoningCarry == nil || carry == ""` 时什么都不做）。调用点：

- **Anthropic `executeStream`**：在 `res.Accumulate(event)` 之后、对 `content_block_stop` 的处理里，若该块是 `thinking` 或 `redacted_thinking`：
  从 `res.Content` 里按顺序取出到目前为止的全部 thinking / redacted_thinking 块，用 275 的 `llm.EncodeThinking` 编码后交出（模型 `strings.TrimSpace(c.model)`）。
  这样交出的块都已收到 `content_block_stop`，签名已在 `res.Content` 里。
  **`executeUnary`**：`ReplayToSink` 之前交 `message.Name`（275 已让它带 carry）。
  **`Execute` 续写多轮**：不用另交——每轮 `executeStream` 自己交，后一轮替换前一轮，与 275「最终消息只带最后一轮」一致。
- **Responses `executeStreaming`**：`response.output_item.done` 收到 reasoning item 并并入 `reasoningItems` 之后，交 `llm.Encode(<当前全部 reasoning items>)`。
  只在 done 时交（`added` 时加密内容还没到）。**非流式**：`ReplayToSink`（`:740`）之前交结果消息的 `Name`。
- **OpenAI 兼容**：三处 `sink.OnReasoningDone()` 紧挨着交 `openAIReasoningCarryPrefix + <到目前为止累积的思考原文>`（不 trim，与 `compatMessageFromOpenAI` 的拼法一致）。
  **非流式**：`ReplayToSink`（`:1156`）之前交结果消息的 `Name`（若是 carry）。
- **Google 流式**：278 Step 4 每次往布局里追加了带签名的 part 或 thought part 之后，交 `llm.EncodeGeminiParts(<当前布局>)`（同 278 的条件：布局里至少有一个签名或 thought）。
  **非流式**：`executeUnaryRound` 的 `ReplayToSink`（`:191`）之前交结果消息的 `Name`。

每个调用点写一句注释说明为什么在这里交（这一刻什么已经完整）。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... -count=1` → ok（只加了回调，既有测试不受影响）。

### Step 3: 引擎的收集器记录 carry，拼出的消息挂上

`pkg/run/response_collector.go`：
1. `responseCollector` 加 `carry, carryModel string`。
2. `withResponseCollector` 按 `OnTextBlockDone` 的同一写法包 `OnReasoningCarry`：先 `collector.noteReasoningCarry(carry, model)`，下游非 nil 再转发；下游为 nil 也装「只记录」版本。
3. `noteTransportRetry`：清空全部的那一支同时清空 carry（重发会重新交）；只丢当前块的那一支不动 carry。
4. 新增 `func (c *responseCollector) reasoningCarry() (carry, model string)`；`result()` 组消息时设 `Name`/`ReasoningModel`（carry 为空就不设）。
5. 收集器结构体注释里补一句：拼出的消息带着打断那一刻已完整的思考。

`pkg/run/orchestration_llm.go:530`：`prov` 组好后设 `prov.Name, prov.ReasoningModel = collector.reasoningCarry()`（为空不设），注释说明：
工具在模型还在输出时就开跑，这条先落库的临时消息若因打断留在历史里，就是模型那一步的记录，思考必须跟着它。
其余三处（`:575-582`、`:629`、`:691`）用的是 `collector.result()`，Step 3.4 已覆盖，**不要**再改。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1` → ok。

### Step 4: 界面的取消缓冲记录 carry，半截正文挂上

`pkg/turn/session.go`：
1. `StreamPartial` 加 `carry, carryModel string`；新增 `SetReasoningCarry(carry, model string)` 与 `ReasoningCarry() (string, string)`（都加锁，nil 安全，照现有方法写）。
   `ResponseCompleted()` 与 `Reset()` 同时清空 carry（完成的回复带着自己的 carry 进了会话；重试会重新交）；`ResponseReasoningDone()` **不清** carry（那正是思考完整、刚交出 carry 的时候）。
2. `CancelledTurn` 加 `PartialCarry`、`PartialCarryModel string`（注释：半截正文那次回复已完整的思考，随那条消息落库）。
3. `PersistCancelledTurn` `:188-190` 组半截正文消息时，若 `in.PartialCarry` 以 `llm.Prefix` 开头，就设 `Name`/`ReasoningModel`。

接线：
- `pkg/tui/chat_turn.go`（`:470-490` 一带构造 sink 处）：加 `OnReasoningCarry: func(carry, model string) { accum.SetReasoningCarry(carry, model) }`（若该 sink 上已有同名回调则串接）。
- `pkg/gateway/run_control.go` `withAnswerStream`：照 `OnReasoningDelta` 的串接写法加 `stream.OnReasoningCarry`，调 `partial.SetReasoningCarry`，再转发原回调（若非 nil）。
- `pkg/tui/chat_session.go:2219` 与 `pkg/gateway/run_control.go:988` 构造 `CancelledTurn` 时，从同一个 `StreamPartial` 取 `ReasoningCarry()` 填两个新字段。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/ ./pkg/tui/ ./pkg/gateway/ -count=1` → ok。

### Step 5: 确认三类打断都走到了

只读核对，不改代码：
- 插话抢占（`:629`）、取消（`:691`）、流式重试收尾（`:575-582`）都经 `collector.result()`；
- 工具开跑的临时消息（`:530`）经 Step 3；
- 无工具时的取消经 `StreamPartial` → `PersistCancelledTurn`；
- 子代理与主代理走同一个 orchestration，收集器是同一个实现。

**Verify**: `grep -n 'collector.result()' pkg/run/orchestration_llm.go` 恰 3 处（`:575-582`、`:629`、`:691` 一带）；`grep -n 'collector.reasoningCarry()' pkg/run/orchestration_llm.go` 恰 1 处。

### Step 6: Gemini 投影——布局之外的部分补在末尾

278 的 `geminiPartsFromLayout` 只按布局重建。被打断的消息，布局只覆盖它的前缀（最后一段正文在打断时才由引擎封口，适配器还没来得及记进布局）。改为：
按布局重建之后，消息里**布局没有覆盖到**的正文段（按 `msg.Parts` 顺序，跳过已被布局 `text` part 覆盖的那几段）与工具调用（布局 `call` 没引用到的下标）依次补在末尾，不带签名。
对应关系：布局里非空的 `text` part 与 `msg.Parts` 的正文段一一按顺序对应（278 Step 3/4 保证二者同一次遍历产出），布局 `call` part 用 `Call` 下标对应 `msg.ToolCalls`。
补在末尾的调用若落在当前轮、且是该 model 内容的第一个调用，278 的占位签名规则照常生效。注释写清为什么（打断的回复只有前缀被记录）。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/google/ -count=1` → ok。

### Step 7: 测试

见「Test plan」。

**Verify**: Test plan 末尾命令 → 全部 PASS。

### Step 8: 回归面

**Verify**: 「Commands」全部命令 → ok / exit 0 / 无输出。
若有测试逐字比较了收集器或取消路径拼出的 `llm.Message` 而失败：**仅当**差异恰是「现在多了 `Name`/`ReasoningModel`」时才可以更新期望并在报告里列出；其他失败 → STOP。

### Step 9: 真机验收（主代理与子代理都要验）

用 275 Step 9 的同一套环境（主树构建、隔离 `FOREBRAIN_HOME`、`zhipuai/glm-5.3` 走 `https://open.bigmodel.cn/api/anthropic` + `api_path: /v1/messages`，key 只从 `~/.forebrain/.env` 拷进隔离目录、不上命令行），在 TUI 里：

1. **插话抢占**：发一个多步写代码任务；在模型调用工具、工具正在执行时输入一句插话并按 Esc 发送。
   判据：本轮继续完成；`sqlite3 "$H/forebrain.state.sqlite" "select id, parts from fb_messages where role='assistant' order by id"` 里，被打断的那条 assistant 行带 `anthropic_thinking_v1`；
   插话之后的下一个请求体里，那条 assistant 消息以 `"type":"thinking"` 块开头。
2. **取消**：再发一个任务，模型输出正文途中按 Esc 取消；随后发一句「继续」。判据：半截正文那条 assistant 行带 carry；「继续」的请求体里它带 thinking 块。
3. **子代理**：让主代理派一个同模型子代理执行多步任务，在子代理视图里用同样方式插话（子代理视图仅运行中可插话）。判据同 1，查子代理会话的行。
4. 全程没有 `refused the replayed thinking` 警告（有 → 记下 400 原文并 STOP）。
5. 若 275 Step 9 当时已改为 owner 手动验收（端点不出思考），这里同样写成 owner 手动清单。验收完删掉隔离目录。

## Test plan

- `pkg/llm/anthropic/agent_llm_test.go`：`TestAnthropicStreamHandsOverCompleteThinking`——SSE：thinking 块（带 `signature_delta`）→ `content_block_stop` → tool_use。
  断言 `OnReasoningCarry` 恰在 thinking 块封口后、`OnToolCallReady` 之前被调用一次，carry 解出的块与签名逐字相等、model 为 client 模型；
  另一用例：thinking 块在收到 `content_block_stop` 之前流就断了 → 从未交出包含它的 carry。
- `pkg/llm/openai/responses_llm_test.go`：`TestResponsesStreamHandsOverReasoningItemsWhenDone`——reasoning item 的 `added` 不交、`done`（带加密内容）交，且先于后续 function_call 的 `OnToolCallReady`。
- `pkg/llm/openai/compat_llm_test.go`：`TestCompatStreamHandsOverReasoningWhenItEnds`——思考增量之后出现工具调用增量：在 `OnReasoningDone` 时交 `前缀 + 全部思考原文`，早于 `OnToolCallReady`。
- `pkg/llm/google/stream_test.go`：`TestGoogleStreamHandsOverSignedParts`——带签名的函数调用 part 到达后交出的 carry 解出的布局含该 call 与签名。
- 四个适配器各一条非流式：`Test<Adapter>UnaryHandsOverCarryBeforeReplay`——`OnReasoningCarry` 早于回放出的 `OnToolCallReady`。
- `pkg/run/response_collector_test.go`：
  - `TestResponseCollectorKeepsTheReasoningCarry`：交两次 carry 后 `result().Message` 的 `Name`/`ReasoningModel` 为后一次；下游 sink 为 nil 时也记录。
  - `TestResponseCollectorTransportRetryDropsTheCarry`：重发 → carry 清空；追认 Cleared（非重发）→ carry 保留。
- `pkg/run/streaming_tools_test.go`（照 `:877` `TestProvisionalAssistantRowIsCompletedNotDuplicated`）：`TestProvisionalAssistantRowCarriesTheReasoning`——
  假模型在流里先交 carry、再交一个工具调用并阻塞；工具开跑时落库的临时 assistant 行带 `reasoning_carry` 部件、内容为那份 carry。
- `pkg/run/orchestration_llm_test.go`（照 `:5720` `TestInterruptToSendStopsTheCallAndContinuesTheTurn`）：`TestInterruptToSendKeepsTheAbandonedReplysReasoning`——
  被插话放弃的调用已交 carry → 插话之后的下一次请求里，那条 assistant 消息的 `Name` 等于该 carry、`ReasoningModel` 为产出模型。
- `pkg/turn/session_test.go`：
  - `TestStreamPartialKeepsTheReasoningCarry`：`SetReasoningCarry` 后可读；`ResponseReasoningDone` 不清、`ResponseCompleted` 与 `Reset` 清。
  - `TestPersistCancelledTurnGivesThePartialAnswerItsCarry`：`PartialText` + `PartialCarry` → 落库的半截正文消息带该 carry；`PartialCarry` 为空或不是 carry → 不带。
- `pkg/llm/google/agent_llm_test.go`：`TestGoogleReplaysALayoutPrefixAndAppendsTheRest`——消息有两段正文、两个调用，布局只含第一段正文与第一个调用（带签名）→
  投影为「布局两项（带签名）+ 第二段正文 + 第二个调用（不带签名）」。

```
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/run/ ./pkg/turn/ -count=1 -v \
  -run 'HandsOver|ReasoningCarry|TransportRetryDropsTheCarry|ProvisionalAssistantRowCarries|InterruptToSendKeepsTheAbandoned|PartialAnswerItsCarry|LayoutPrefixAndAppends' \
  2>&1 | grep -E '^(--- |ok|FAIL)'
```

→ 上面列出的新测试全部 `--- PASS`，没有 `FAIL`。

## Done criteria

全部满足：

- [ ] `CGO_ENABLED=1 go build ./...` exit 0
- [ ] 「Commands」的全部测试命令 ok；`go vet` exit 0；`gofmt -l` 无输出
- [ ] Test plan 的新测试全部 PASS
- [ ] `grep -n 'OnReasoningCarry' pkg/llm/stream.go` 恰 1 处字段定义
- [ ] 四个适配器文件里都有对 `OnReasoningCarry` 的调用（`grep -ln 'OnReasoningCarry' pkg/llm/anthropic/agent_llm.go pkg/llm/openai/compat_llm.go pkg/llm/openai/responses_llm.go pkg/llm/google/stream.go pkg/llm/google/agent_llm.go` 列出 5 个文件）
- [ ] Step 5 的两条 grep 结果符合
- [ ] `git status --porcelain` 只多出 Scope 里的文件（依赖计划的文件属预期）
- [ ] Step 9 真机判据全过（或按 275 的结论写成 owner 手动清单）
- [ ] `plans/README.md` 本计划那一行已更新

## STOP conditions

- 开头四条依赖检查任一不满足。
- 「Current state」③ 的四处拼消息位置不止四处，或已有地方不经 `collector` 拼 assistant 消息进会话——列出来报告，别自行扩大范围。
- Anthropic 流式里无法在 `content_block_stop` 时从 `res.Content` 拿到签名（SDK 行为与 275 的结论不符）。
- 某个适配器找不到「思考已完整」的确定时刻（例如兼容端点从不发出思考结束的边界）——报告该适配器与端点，不要用「定时交」之类的近似。
- 回归面出现 Step 8 允许范围之外的失败。
- 修复需要动 Scope 之外的文件。

## 非功能要求（风险 / 对策 / 验证）

| 维度 | 风险 | 对策 | 验证 |
|---|---|---|---|
| 正确性 | 交出半截思考块 → 回放被拒 | 只在单元完整时交（签名 / 加密内容已到） | 各适配器的 `HandsOver` 测试 |
| 正确性 | 重发后旧 carry 挂到新回复上 | 传输重发时收集器与取消缓冲一并清空 | `TestResponseCollectorTransportRetryDropsTheCarry`、`TestStreamPartialKeepsTheReasoningCarry` |
| 一致性 | 主代理与子代理行为不同 | 两者共用 orchestration 与收集器；真机两边都验 | Step 9 第 3 条 |
| 延迟 | 每段思考结束多一次编码 | 编码量与最终消息一次编码同级；只在单元边界发生，不在每个增量上 | 回归面耗时无明显变化 |
| 鲁棒性 | Gemini 打断的回复布局只覆盖前缀 | 投影补「布局之外补在末尾」 | `TestGoogleReplaysALayoutPrefixAndAppendsTheRest` |
| 可维护性 | 又一个 sink 回调 | 字段注释写清时机与语义；引擎包装一律 `*sink` 复制，不会漏传 | Step 1 / Step 3 |
| 兼容 | 老会话 | 不改库结构；老的被打断消息没有 carry，照旧投影 | — |

## Maintenance notes

- **新增适配器时**：除了在最终消息上挂 carry（275–278），还要在流式里于「思考单元完整」时调用 `OnReasoningCarry`，否则被打断的回复会丢思考。
- **新增「拼 assistant 消息进会话」的路径时**：必须从收集器（或 `StreamPartial`）取 carry 挂上；评审时 grep `llm.AssistantMessage(` 与 `Role: llm.RoleAssistant` 的新出现处。
- **评审重点**：①Anthropic 交 carry 的时机在 `Accumulate` 之后；②`ResponseReasoningDone` 没有清 carry；③插话抢占之后同一轮的下一次请求确实带上了被打断回复的思考（`TestInterruptToSendKeepsTheAbandonedReplysReasoning`）。
