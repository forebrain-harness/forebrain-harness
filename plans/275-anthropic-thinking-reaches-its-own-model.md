# Plan 275: Anthropic 协议的 thinking 块原样回传给产出它的同一模型

> **Executor instructions**: 按步骤顺序执行，每步验证通过再做下一步。遇到「STOP conditions」就停下报告，
> 不要自己发挥。**不要 commit / push / merge**（仓库规矩：owner 手动提交）。做完后把 `plans/README.md`
> 里本计划那一行的状态改成 DONE 并附一句摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git status --porcelain -- pkg/llm/retry.go pkg/llm/tokenizers.go pkg/llm/anthropic/ pkg/llm/openai/compat_llm.go pkg/llm/openai/compat_llm_test.go pkg/llm/openai/responses_llm_test.go pkg/state/message_parts.go pkg/state/message_parts_test.go`
> 与 `git diff --stat d9c45060 -- <同样这些路径>`。
> → 写计划时两条都为空。（不要用 `git diff d9c45060..HEAD`：比的是两个 commit，未提交改动看不见。）
> 有输出时逐条对照本计划「Current state」的摘录：**结构对得上就继续**，不要回退或改写别人的改动；对不上才 STOP。

## Status

- **Priority**: P1（owner 实测：走 Anthropic 协议、默认会思考的模型在工具循环里无限重复思考，一直不产出代码）
- **Effort**: M
- **Risk**: MED（改的是 Anthropic 协议每一个请求的请求体；provider 若拒收回放的 thinking，本计划自带「剥掉重发一次」兜底）
- **Depends on**: none（plan 272 的续写循环已在 HEAD，见 Current state ④；本计划在它上面改）
- **Category**: bug
- **Planned at**: commit `d9c45060`, 2026-11-08

## Why this matters

Anthropic 协议适配器（`pkg/llm/anthropic/agent_llm.go`）把模型返回的 thinking 块**只推给界面，从不放进消息**，
下一次请求也就从不带回去——**即使还是同一个模型**。工具循环的每一步，模型都看不到自己上一步想了什么：
它看到自己发出了一个工具调用、拿到了结果，却不知道为什么调、下一步打算做什么，于是从头再想一遍。

owner 2026-11-08 确认的真机现象：**不报错、不降级，而是无限循环**——反复思考相似的内容，一直不生成代码、不产出结果。
受影响的是一切走 Anthropic Messages 协议、默认会思考的模型：`provider: anthropic` / `minimax`、
任何 `api_path: /v1/messages` 的自定义接入（如智谱、火山方舟的 Anthropic 端点），以及 OpenCode 上走 Anthropic 协议的模型——
owner 自己在用的 `opencode-go/qwen3.8-max`、`opencode-go/qwen3.8-flash` 就在其中（`pkg/llm/opencode/endpoints.go:136-143`）。

另外两种协议早就没有这个问题：OpenAI 兼容（`reasoning_content`）和 Responses（加密 reasoning items）都经
「reasoning carry」机制把思考回传给**产出它的同一模型**。本计划让 Anthropic 协议也走这同一套机制，行为与 Claude Code 一致。

## 已拍板的决定

- **owner 2026-11-08**：不需要复现，直接修；必须彻底修复。根因由下方「Current state」的代码直接确定（三处丢弃点）。
- **owner 2026-11-08**：同一模型**回传所有历史轮**的 thinking（不是只回传当前轮），与 Claude Code 一致。
  依据（本机 `claude.exe` 2.1.301 逆向）：开启思考时它在请求里带 `context_management: {edits: [{type: "clear_thinking_20251015", keep: "all"}]}`，
  明确要求服务端保留全部轮的思考；历史消息里的 thinking 块只在 400 拒收或压缩时才剥。
  另一个理由是提示缓存：「只回传当前轮」会在每条新用户消息到来时改写上一轮所有 assistant 消息的字节，缓存从那里断开。
  **执行者不得**加任何「只保留最近 N 轮 / 当前轮」的裁剪。
- **复用已有的 reasoning carry，不另起机制**（仓库规矩「先复用再发明」）：carry 仍放在 `llm.Message.Name`（`llm.Prefix` 开头）、
  产出模型仍记在 `llm.Message.ReasoningModel`、落库仍走 `state` 的 `reasoning_carry` 部件、回放仍由 `llm.ReplaysReasoningTo` 判定。
  只是新增一种 payload 版本 `anthropic_thinking_v1`，专门装 thinking 块。
- **逐字节原样**：thinking 文本、`signature`、`redacted_thinking` 的 `data` 一个字节都不改（不 trim、不归一化）。
  SDK 自己的注释就是硬约束（`anthropic-sdk-go@v1.80.0/message.go:12887-12893`，`ThinkingBlockParam`）：
  > Thinking blocks must be passed back unmodified and in their original order; a modified block results in a 400 `invalid_request_error`.
- **只回传给同一模型**：签名绑定产出它的模型；换模型后旧 thinking 一律不发（沿用 `ReplaysReasoningTo`），
  且**不享受**「`ReasoningModel` 为空就照旧回放」的历史豁免——新格式没有历史数据，空就是不发。
- **对齐 Claude Code**（本机 `claude.exe` 2.1.301 逆向核实，函数名是混淆后的）：
  1. 历史里的 thinking / redacted_thinking 块随 assistant 消息原样回传（`Nvn` 统计 `blocksSent`/`blocksStripped`）；
  2. provider 以 400 拒收时，剥掉 thinking 块后**重发一次**，并打一条警告
     （日志原文 `[thinking] prefix-lock rejection: stripped N of M thinking block(s) (...) and retrying.`）；
  3. 末尾只剩 thinking 的 assistant 消息不发（`$Yn` 过滤 trailing thinking；`jYn` 把「全是 thinking 且有未签名块」的消息当作被放弃的）。
  本计划对应：①回传；②Step 4 的 400 兜底；③「只有 thinking 的 assistant 消息不发」保持现状，且 thinking 一律排在 text / tool_use **之前**，
  永远不会是消息的最后一块。
- **续写（撞输出上限）时**：最终那条合并消息只带**最后一轮**的 thinking；最后一轮本身撞了上限（续写用尽）就不带。
  理由：前几轮的 thinking 在后续轮的请求里本来就没发过（续写提示词也告诉了模型），带上反而和模型实际看到的对不上；
  撞上限被截断的 thinking 块可能没有签名，回放会被 Claude 官方端点拒收。

## Current state

所有行号基于 `d9c45060`。

**① 丢弃点 1——响应转消息时扔掉 thinking**（`pkg/llm/anthropic/agent_llm.go:707-730`）：

```go
func agentAnthropicMessageFromAPI(m *anthropicapi.Message) (*llm.Message, error) {
	if m == nil {
		return nil, &anthropicNilResponseError{}
	}
	parts := make([]llm.ContentPart, 0)
	toolCalls := make([]llm.ToolCall, 0)
	for _, b := range m.Content {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				parts = append(parts, llm.Text(b.Text))
			}
		case "tool_use":
			toolCalls = append(toolCalls, anthropicToolCallFromBlock(b))
		}
	}
	msg := &llm.Message{
		Role:      llm.RoleAssistant,
		Parts:     parts,
		ToolCalls: toolCalls,
	}
	return msg, nil
}
```

调用点：流式 `executeStream` 的 `:475`（`msg, err := agentAnthropicMessageFromAPI(&res)`）、非流式 `executeUnary` 的 `:509`。
流式路径里 thinking 只走 sink（`:403-438`，`OnReasoningDelta` / `OnReasoningDone`）；`res.Accumulate(event)` 已经把
`thinking_delta` 和 `signature_delta` 累积进 `res.Content`（SDK `messageutil.go:91-94`），`redacted_thinking` 的 `data`
在 `content_block_start` 时整块写入（`messageutil.go:63-73`）。`anthropicapi.ContentBlockUnion` 有 `Thinking`、`Signature`、`Data` 三个字段。
`:267` 的注释写明了现状：`thinking blocks reach only the sink, never the message.`

**② 丢弃点 2——消息转请求时只发 text / tool_use**（`agent_llm.go:556` 起，assistant 分支 `:571-604`）：

```go
func agentAnthropicMessagesToAPI(messages []llm.Message) ([]anthropicapi.TextBlockParam, []anthropicapi.MessageParam, error) {
	...
		case llm.RoleAssistant:
			blocks := make([]anthropicapi.ContentBlockParamUnion, 0, len(m.Parts)+len(m.ToolCalls))
			for _, p := range m.Parts {
				if p.Type == llm.ContentTypeText && strings.TrimSpace(p.Text) != "" {
					blocks = append(blocks, anthropicapi.NewTextBlock(p.Text))
				}
			}
			for _, tc := range m.ToolCalls {
				... // tool_use，畸形参数退化成 {}
				blocks = append(blocks, anthropicapi.NewToolUseBlock(id, input, tc.Function.Name))
			}
			// An assistant message with nothing this protocol can express contributes nothing and must not be sent: an empty content array is a request error.
			if len(blocks) == 0 {
				continue
			}
			out = append(out, anthropicapi.NewAssistantMessage(blocks...))
```

调用点：`Execute` 的 `:182`（`agentAnthropicMessagesToAPI(conversation)`），以及 `prompt_cache_test.go:24`、`:36`、`:51` 三处测试。

**③ 丢弃点 3——`Execute` 合成最终消息时明确不带 thinking**（`agent_llm.go:241-254`）：

```go
		conversation = append(append([]llm.Message(nil), messages...), recovery.Resume(answer.String())...)
	}
	// One assistant message carries the whole answer: each round was its own
	// request, but this call is one reply. Thinking never enters it — the
	// recovery prompt already tells the model its reasoning was not kept.
	parts := make([]llm.ContentPart, 0)
	if answer.Len() > 0 {
		parts = append(parts, llm.Text(answer.String()))
	}
	out := &llm.Result{
		Message:    &llm.Message{Role: llm.RoleAssistant, Parts: parts, ToolCalls: lastToolCalls},
		StopReason: stopReason,
		Usage:      usage,
	}
```

**④ `Execute` 的循环头**（`agent_llm.go:179-215`，plan 272 的续写循环）：每轮先 `agentAnthropicMessagesToAPI(conversation)`，
再组 `anthropicapi.MessageNewParams{Model, MaxTokens, Messages, System}`、设 `OutputConfig.Effort` / `ServiceTier` / `Tools`、
`ApplyPromptCache(params.System, params.Messages)`，然后 `executeUnary` 或 `executeStream`，出错直接 `return nil, err`。
`res.StopReason` 已是归一化后的值（撞上限时为 `llm.StopReasonOutputLimit`），`llm.OutputLimitReached(stopReason)` 可直接判。
整个 `Execute` 的错误在 `defer normalizeError` 里才转成 `*llm.APIError`（`pkg/llm/anthropic/error.go:25-46`），
所以**在 `Execute` 内部** `errors.As(err, &*anthropicapi.Error)` 拿得到 SDK 原始错误（`StatusCode`、`RawJSON()`）；
`llm.NewExecuteError` 有 `Unwrap`（`pkg/llm/errors.go:214`），不挡 `errors.As`。

**⑤ 已有的 carry 机制**（要复用的那一套）：

- `pkg/llm/retry.go:314-387`：`const Prefix = "!!FOREBRAIN_RCv1!!"`、`payloadVersion = "openai_responses_reasoning_v1"`、
  `Item`/`Payload`/`Encode`/`Decode`（`Decode` 只认 `payloadVersion`，且 `normalizeItems` 会 trim `ID` 与 `EncryptedContent`
  ——**不能拿来装 thinking**）、`ReplaysReasoningTo`、`ExtractSummary`：

```go
// ReplaysReasoningTo reports whether the reasoning m carries may be sent back
// to model: m carries some, and either the model that produced it is the one
// being asked or it was recorded without a model. ...
func ReplaysReasoningTo(m Message, model string) bool {
	if !strings.HasPrefix(m.Name, Prefix) {
		return false
	}
	producer := strings.TrimSpace(m.ReasoningModel)
	return producer == "" || strings.EqualFold(producer, strings.TrimSpace(model))
}

func ExtractSummary(name string) string {
	if !strings.HasPrefix(name, Prefix) {
		return ""
	}
	if payload, ok := Decode(name); ok {
		return SummaryText(payload.Items)
	}
	return strings.TrimPrefix(name, Prefix)
}
```

  注意 `ExtractSummary` 的兜底：任何认不出的 payload 都会把**原始 JSON** 当思考文本返回——新 payload 不处理就会在界面上显示 JSON。
- `pkg/llm/openai/compat_llm.go:1396-1404`（OpenAI 兼容协议的回放；`openAIReasoningCarryPrefix` 与 `llm.Prefix` 是同一个字符串）：

```go
	if strings.HasPrefix(m.Name, openAIReasoningCarryPrefix) {
		// The carry is reasoning, never a participant name. It goes back as
		// reasoning_content only to the model that wrote it, and only when it
		// is plain text: a Responses payload is another protocol's encrypted
		// items, not something this model ever said.
		if _, isPayload := llm.Decode(m.Name); !isPayload && llm.ReplaysReasoningTo(m, model) {
			msg.ReasoningContent = strings.TrimPrefix(m.Name, openAIReasoningCarryPrefix)
		}
		msg.Name = ""
	}
```

  **今天一个 `anthropic_thinking_v1` payload 会被 `Decode` 判成「不是 payload」，于是整段 JSON 被当成 `reasoning_content` 发出去**
  （只在同名模型跨协议时才会发生，但必须堵上）。
- `pkg/llm/openai/responses_llm.go:1861-1868`：`responseReasoningInputItemsFromMessage` 先 `ReplaysReasoningTo` 再 `llm.Decode`；
  `Decode` 只认 Responses 版本，所以 thinking payload 天然不会被回放——**不用改代码，只补测试**。
- 落库：`pkg/state/message_parts.go:324-330` 把 `Prefix` 开头的 `msg.Name` 原样存进 `{"type":"reasoning_carry","carry":…,"model":…}`；
  `:458-487` 的 `parseReasoningCarryPart` 原样读回。`pkg/state/message_sync.go:163-168` 的 `withReasoningModel` 在适配器没打模型时用行的模型补；
  `:300` 在「首个工具调用开始时已写的行」缺 carry 时补写。MySQL 的 `fb_messages.parts` 是 `LONGTEXT`（`schema_mysql.sql:163`），无长度问题。
  **这些都对 payload 内容无感知，不用改**；只有 `message_parts.go:93-97` 的注释过时（说 Anthropic thinking 被转成 fenced 文本）。
- 记忆管线 `pkg/memory/pipeline.go:366-371` 已按 `Prefix` 剥掉所有 carry——不用改。
- 估算 `pkg/llm/tokenizers.go:404-413`：`EstimateMessageWith` 把整个 `msg.Name` 计入 token 估算，注释说
  「Other adapters only put a participant name here」——Anthropic 改完后这句不再成立，只改注释。

**⑥ 仓库约定**：

- 注释写「为什么」，完整英文句子，密度参照 `agent_llm.go` 现有注释；新增导出符号必须有 doc comment。
- 日志用 `log/slog`（`pkg/llm/openai/responses_llm.go`、`pkg/llm/tokestimate.go:59` 的 `slog.Warn("...", "key", value)` 形式）。
- 测试：Anthropic 端到端测试用 `httptest.NewServer` + `writeAnthropicSSEEvent` 发 SSE、解码请求体断言
  （范例 `agent_llm_test.go:19` `TestAnthropicAgentLLMAlwaysStreamsAndAggregates`；多轮脚本服务器 `anthropicLimitScriptServer`、
  `anthropicRoundScript` 在 `:630-690`；非流式响应体常量 `anthropicNonStreamingBody` 在 `:434`）。
  carry 回放规则的范例测试：`pkg/llm/openai/compat_llm_test.go:72` `TestCompatReasoningReplaysOnlyToItsOwnModel`、
  `pkg/llm/openai/responses_llm_test.go:1472`、`pkg/state/message_parts_test.go:198`。
- 每个 Anthropic 测试开头 `t.Setenv("FOREBRAIN_LLM_HTTP_DEBUG", "0")`。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| llm 各包 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... -count=1` | 全部 ok（`pkg/llm` 本身约 2.5 分钟） |
| 单包快测 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/anthropic/ -count=1 -run '<名字>' -v` | 指定测试 PASS |
| state / memory | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ ./pkg/memory/ -count=1` | ok |
| 回归面 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/agent/ ./pkg/turn/ ./pkg/run/ ./pkg/tui/ ./pkg/gateway/ -count=1` | ok |
| 静态检查 | `go vet ./pkg/llm/... ./pkg/state/` | exit 0 |
| 格式 | `gofmt -l pkg/llm pkg/state` | 无输出 |

`FOREBRAIN_HOME` 必须显式指向 mktemp 目录（不继承外层环境，仓库硬规矩）；`pkg/state` 不加 `-tags fts5` 会报
`built without cgo or SQLite FTS5`。写计划时基线实测：`./pkg/llm/... ./pkg/state/ ./pkg/memory/` 全部 ok。
链接时的 `ld: warning: ... built for newer 'macOS' version` 是已知噪声，忽略。

## Scope

**In scope**（只改这些文件）：
- `pkg/llm/retry.go` — 新增 thinking payload 的编解码，`ExtractSummary` 认它，新增 `StructuredCarry`
- `pkg/llm/thinking_carry_test.go`（新建）
- `pkg/llm/tokenizers.go` — **只改** `:404-413` 那段注释
- `pkg/llm/anthropic/agent_llm.go` — 三处丢弃点 + 400 兜底
- `pkg/llm/anthropic/agent_llm_test.go` — 新测试
- `pkg/llm/anthropic/prompt_cache_test.go` — 三处调用补参数
- `pkg/llm/openai/compat_llm.go` — **只改** `:1401` 那一行的判定
- `pkg/llm/openai/compat_llm_test.go`、`pkg/llm/openai/responses_llm_test.go` — 各补一条测试
- `pkg/state/message_parts.go` — **只改** `:93-97` 那段注释
- `pkg/state/message_parts_test.go` — 补一条往返测试

**Out of scope**（看着相关，不要动）：
- `pkg/llm/openai/responses_llm.go` —— `Decode` 只认 Responses 版本，thinking payload 天然不回放；只补测试，不改代码。
- `pkg/state/message_sync.go`、`pkg/memory/pipeline.go` —— 对 payload 内容无感知，已经正确。
- `llm.ReplaysReasoningTo` 本身的语义（含「空模型照旧回放」的历史豁免）—— OpenAI 兼容协议的旧数据依赖它；
  Anthropic 的「空模型不发」在适配器里多判一句，不改这个函数。
- 往请求里加 `thinking` 参数（启用 / 预算 / adaptive）—— 本计划只修「模型已经思考了却被丢掉」，不改「要不要让模型思考」。
- `ApplyPromptCache` / `markMessages` —— thinking 块的 `GetCacheControl()` 返回 nil（SDK `message.go:7426-7457` 不含 `OfThinking`），
  缓存标记天然跳过它们，而 `since++` 把它们计入 20 块回看窗口正是 Anthropic 的计数方式；不用改。
- 任何 UI、网页、TUI 渲染代码 —— 思考的显示走 sink 与落库的 reasoning 行，与本计划无关。
- 估算器 `EstimateMessageWith` 的计算逻辑 —— 见 Maintenance notes。

## Steps

### Step 1: 在 `pkg/llm/retry.go` 加 thinking payload

紧挨着 `Decode`（`:344-364`）之后加下面这些（可调整措辞，**语义不可变**）：

```go
// thinkingPayloadVersion marks a carry holding Anthropic-protocol thinking
// blocks. It is its own payload rather than an Item list: a thinking block's
// text and signature must go back byte for byte, and the Responses items'
// normalization trims the fields an Item would hold them in.
const thinkingPayloadVersion = "anthropic_thinking_v1"

// ThinkingBlock is one thinking or redacted_thinking block of an
// Anthropic-protocol response, exactly as the model returned it. Nothing in it
// is ever trimmed or rewritten: the provider verifies the block against its
// signature, and a changed byte is a rejected request.
type ThinkingBlock struct {
	// Redacted marks a redacted_thinking block, whose whole content is Data.
	Redacted  bool   `json:"redacted,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`
}

type thinkingPayload struct {
	Version string          `json:"version"`
	Blocks  []ThinkingBlock `json:"blocks"`
}

// EncodeThinking packs blocks into a carry, keeping their order. It returns ""
// when no block carries anything, and never falls back to plain text: a plain
// carry is what the OpenAI-compatible client replays as reasoning_content.
func EncodeThinking(blocks []ThinkingBlock) string {
	kept := make([]ThinkingBlock, 0, len(blocks))
	for _, b := range blocks {
		if b.Redacted {
			if b.Data != "" {
				kept = append(kept, ThinkingBlock{Redacted: true, Data: b.Data})
			}
			continue
		}
		if b.Thinking != "" || b.Signature != "" {
			kept = append(kept, ThinkingBlock{Thinking: b.Thinking, Signature: b.Signature})
		}
	}
	if len(kept) == 0 {
		return ""
	}
	data, err := json.Marshal(thinkingPayload{Version: thinkingPayloadVersion, Blocks: kept})
	if err != nil {
		return ""
	}
	return Prefix + string(data)
}

// DecodeThinking reads the blocks EncodeThinking packed. It reports false for
// every other carry, Responses payloads and plain reasoning text included.
func DecodeThinking(name string) ([]ThinkingBlock, bool) {
	if !strings.HasPrefix(name, Prefix) {
		return nil, false
	}
	var payload thinkingPayload
	if err := json.Unmarshal([]byte(strings.TrimPrefix(name, Prefix)), &payload); err != nil {
		return nil, false
	}
	if payload.Version != thinkingPayloadVersion || len(payload.Blocks) == 0 {
		return nil, false
	}
	return payload.Blocks, true
}

// StructuredCarry reports whether name holds a protocol's structured reasoning
// payload — Responses items or Anthropic thinking blocks — rather than plain
// reasoning text. Only plain text is something every protocol can replay.
func StructuredCarry(name string) bool {
	if _, ok := Decode(name); ok {
		return true
	}
	_, ok := DecodeThinking(name)
	return ok
}
```

再改 `ExtractSummary`（`:379-387`）：在 `Decode` 分支之后、`return strings.TrimPrefix(...)` 之前加一支——
thinking payload 返回各**非 redacted** 块的 `Thinking` 用 `"\n"` 连接的文本（redacted 块是密文，不显示），
**绝不能**落到最后那句把 JSON 原文当思考返回。

**Verify**: `CGO_ENABLED=1 go build ./pkg/llm/` → exit 0；`go vet ./pkg/llm/` → exit 0。

### Step 2: OpenAI 兼容客户端不得把 thinking payload 当 `reasoning_content`

`pkg/llm/openai/compat_llm.go:1401`：

```go
		if _, isPayload := llm.Decode(m.Name); !isPayload && llm.ReplaysReasoningTo(m, model) {
```

改成

```go
		if !llm.StructuredCarry(m.Name) && llm.ReplaysReasoningTo(m, model) {
```

并把上方注释的最后一句改成同时提到 Anthropic thinking 块（例如「a Responses payload or an Anthropic thinking payload is
another protocol's blocks, not something this model ever said」）。`msg.Name = ""` 那行不动。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/openai/ -count=1 -run 'Compat'` → ok。

### Step 3: Anthropic 响应转消息时保留 thinking（丢弃点 1）

`pkg/llm/anthropic/agent_llm.go`：

1. `agentAnthropicMessageFromAPI(m *anthropicapi.Message)` 改成 `agentAnthropicMessageFromAPI(m *anthropicapi.Message, model string)`。
   遍历 `m.Content` 时按**原顺序**收集：
   - `case "thinking"`：`llm.ThinkingBlock{Thinking: b.Thinking, Signature: b.Signature}`
   - `case "redacted_thinking"`：`llm.ThinkingBlock{Redacted: true, Data: b.Data}`

   组好 `msg` 后：

```go
	// The thinking rides the reasoning carry, so the next request gives this
	// model its own reasoning back (agentAnthropicMessagesToAPI). Without it a
	// thinking model saw only its calls and their results, never why it made
	// them, and re-derived the same plan on every step of a tool loop.
	if carry := llm.EncodeThinking(thinking); carry != "" {
		msg.Name = carry
		msg.ReasoningModel = strings.TrimSpace(model)
	}
```

2. 两个调用点传 `c.model`：`executeStream` 的 `:475` 与 `executeUnary` 的 `:509`。
3. 改 `:266-268` 的 `executeStream` doc comment：thinking 仍流给 sink，同时随 carry 进入消息（删掉 `never the message`）。
4. `executeUnary` 里 `:538-549` 拼 `ReplayedResponse.Reasoning` 的循环**不动**（那是显示用的）。

**Verify**: `CGO_ENABLED=1 go build ./pkg/llm/anthropic/` → exit 0。

### Step 4: 消息转请求时只给同一模型回放 thinking（丢弃点 2）+ 400 兜底

1. `agentAnthropicMessagesToAPI(messages []llm.Message)` 改成 `agentAnthropicMessagesToAPI(messages []llm.Message, replayTo string)`。
   `replayTo` 是本次请求的模型；**传 `""` 表示一块 thinking 都不回放**。新增：

```go
// anthropicThinkingParams renders the thinking m carries for replayTo, in the
// order the model wrote it, or nothing. Only the model that produced the
// blocks gets them back: their signatures are bound to it. A carry recorded
// without a producer is not replayed either — this format never existed
// without one.
func anthropicThinkingParams(m llm.Message, replayTo string) []anthropicapi.ContentBlockParamUnion {
	if strings.TrimSpace(replayTo) == "" || strings.TrimSpace(m.ReasoningModel) == "" || !llm.ReplaysReasoningTo(m, replayTo) {
		return nil
	}
	blocks, ok := llm.DecodeThinking(m.Name)
	if !ok {
		return nil
	}
	out := make([]anthropicapi.ContentBlockParamUnion, 0, len(blocks))
	for _, b := range blocks {
		if b.Redacted {
			out = append(out, anthropicapi.NewRedactedThinkingBlock(b.Data))
			continue
		}
		out = append(out, anthropicapi.NewThinkingBlock(b.Signature, b.Thinking))
	}
	return out
}
```

   assistant 分支（`:571-604`）：`blocks` 先放 `anthropicThinkingParams(m, replayTo)`，再放 text，再放 tool_use；
   把「什么都没有就不发」那句判定改成「**除了 thinking 什么都没有**就不发」——即 text 与 tool_use 一块都没加进来时 `continue`
   （只有 thinking 的 assistant 消息不发：Claude Code 同样不发末尾只剩 thinking 的消息，而且它绝不能成为消息的最后一块）。
   补一句注释说明 thinking 为什么排最前（模型先想后写；Anthropic 要求原样、保持原相对顺序）。
   OpenAI 兼容客户端写的纯文本 carry（`Prefix` + 文本）与 Responses payload 都会被 `DecodeThinking` 拒绝，所以永远不会被当成 thinking 发出去。

2. 把 `Execute` 循环里「组参数」与「发请求」各抽成一个方法，**逐字搬移**现有逻辑（Effort、`llm.Fast`、Tools、`ApplyPromptCache` 的顺序与注释都不变）：

```go
// messageParams builds one Messages request for conversation. replayTo is the
// model the conversation's thinking is replayed to; "" replays none.
func (c *anthropicAgentLLM) messageParams(ctx context.Context, conversation []llm.Message, tools []*llm.Tool, replayTo string) (anthropicapi.MessageNewParams, error)

func (c *anthropicAgentLLM) send(ctx context.Context, params anthropicapi.MessageNewParams, nonStreaming bool) (*llm.Result, error) {
	if nonStreaming {
		return c.executeUnary(ctx, params)
	}
	return c.executeStream(ctx, params)
}
```

3. `Execute` 循环头改成（`replayTo` 在循环外声明，**一旦降级，本次 `Execute` 剩下的续写轮也不再回放**）：

```go
	replayTo := c.model
	var lastRound *llm.Message
	rounds := 0
	for {
		params, err := c.messageParams(ctx, conversation, tools, replayTo)
		if err != nil {
			return nil, err
		}
		res, err := c.send(ctx, params, nonStreaming)
		if err != nil && replayTo != "" && anthropicParamsCarryThinking(params.Messages) && isThinkingRejection(err) {
			// Claude Code does the same: a provider that refuses the replayed
			// thinking gets the request again without it, rather than a
			// conversation that fails on every request from here on.
			slog.Warn("anthropic: provider refused the replayed thinking; resending without it", "model", c.model, "err", err)
			replayTo = ""
			if params, err = c.messageParams(ctx, conversation, tools, replayTo); err != nil {
				return nil, err
			}
			res, err = c.send(ctx, params, nonStreaming)
		}
		if err != nil {
			return nil, err
		}
		rounds++
		... // 原有的用量累计不动
		lastRound = res.Message // 无条件赋值：某轮 Message 为 nil 时不能沿用上一轮的 thinking
		if res.Message != nil {
			answer.WriteString(res.Message.TextContent())
			lastToolCalls = res.Message.ToolCalls
		}
		...
```

   两个新 helper：

```go
// anthropicParamsCarryThinking reports whether a request replays any thinking.
func anthropicParamsCarryThinking(messages []anthropicapi.MessageParam) bool {
	for _, m := range messages {
		for _, b := range m.Content {
			if b.OfThinking != nil || b.OfRedactedThinking != nil {
				return true
			}
		}
	}
	return false
}

// isThinkingRejection reports a 400 that names the thinking or its signature —
// the provider refusing replayed blocks. A 400 that only mentions thinking for
// another reason costs one extra request that fails the same way.
func isThinkingRejection(err error) bool {
	var apiErr *anthropicapi.Error
	if !errors.As(err, &apiErr) || apiErr == nil || apiErr.StatusCode != http.StatusBadRequest {
		return false
	}
	body := strings.ToLower(apiErr.RawJSON())
	return strings.Contains(body, "thinking") || strings.Contains(body, "signature")
}
```

   需要新 import `log/slog`。400 出现在 `stream.Err()` 首次检查时（`executeStream` 在 `*sink.Streamed = true` 之前就返回），
   非流式路径直接返回错误不回放 sink——所以失败的那次不会往界面推任何东西，不需要额外清理。

**Verify**: `CGO_ENABLED=1 go build ./pkg/llm/anthropic/` → exit 0（`prompt_cache_test.go` 此时编译失败属预期，Step 6 修）。

### Step 5: 续写合成的最终消息带上最后一轮的 thinking（丢弃点 3）

`Execute` 末尾（`:243-254`）改成：

```go
	// One assistant message carries the whole answer: each round was its own
	// request, but this call is one reply. Its thinking is the last round's —
	// the reasoning behind the calls it hands back. An earlier round's thinking
	// was never sent again in the rounds after it, and a round cut by the
	// output limit may hold a block cut before its signature arrived, which a
	// provider refuses on replay.
	parts := make([]llm.ContentPart, 0)
	if answer.Len() > 0 {
		parts = append(parts, llm.Text(answer.String()))
	}
	msg := &llm.Message{Role: llm.RoleAssistant, Parts: parts, ToolCalls: lastToolCalls}
	if lastRound != nil && !llm.OutputLimitReached(stopReason) {
		msg.Name, msg.ReasoningModel = lastRound.Name, lastRound.ReasoningModel
	}
	out := &llm.Result{
		Message:    msg,
		StopReason: stopReason,
		Usage:      usage,
	}
```

（`recovery.Resume(answer.String())` 那条续写会话**不变**：续写轮的请求里不带前一轮的 thinking，与续写提示词的说法一致。）

**Verify**: `CGO_ENABLED=1 go build ./pkg/llm/... ` → exit 0；
`grep -n "Thinking never enters it\|thinking blocks reach only the sink" pkg/llm/anthropic/agent_llm.go` → 无输出。

### Step 6: 修既有测试调用 + 改两处过时注释

1. `pkg/llm/anthropic/prompt_cache_test.go:24`、`:36`、`:51`：`agentAnthropicMessagesToAPI([]llm.Message{...})` 补第二个参数 `""`（这些测试不涉及 thinking）。
2. `pkg/state/message_parts.go:93-97` 的 `ExtractReasoningText` 注释：把「Anthropic thinking blocks are converted to fenced text parts upstream…」
   改成如实描述——Anthropic thinking 块也走同一个 carry（`anthropic_thinking_v1` payload），`llm.ExtractSummary` 读出它的文本。
3. `pkg/llm/tokenizers.go:404-413` 的注释：在「compatible clients … Responses client」后补上 Anthropic 客户端（thinking 块及其签名），
   删掉「Other adapters only put a participant name here」中不再成立的部分。**代码不改。**

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... -count=1` → 全部 ok（此时新测试还没写，既有测试必须全绿）。

### Step 7: 写新测试

见「Test plan」，逐条写完。

**Verify**: Test plan 末尾的命令全部 ok，且 `-v` 输出里每个新测试名都出现 `--- PASS`。

### Step 8: 回归面

**Verify**: 「Commands」表里的「回归面」命令 → ok；`go vet ./pkg/llm/... ./pkg/state/` → exit 0；`gofmt -l pkg/llm pkg/state` → 无输出。

若回归面有失败：**仅当**失败原因是某个断言逐字比较了 Anthropic 回复转出的 `llm.Message`、而它现在多了 `Name`/`ReasoningModel` carry 时，
才可以把该断言改成包含 carry（并在报告里逐条列出改了哪些）；其他任何失败 → STOP。

### Step 9: 真机验收（fake provider 驱动不了：需要真实模型产出带签名的 thinking 并验证端点接受回放）

仓库规矩：真机验收在主树构建、隔离 `FOREBRAIN_HOME`、key 不上命令行。

1. 构建：`CGO_ENABLED=1 go build -tags fts5 -o "$D/forebrain" ./cmd/forebrain && scripts/install-dictionary.sh "$D"`（`D` 为 scratch 目录）。
2. 隔离 home `H=$(mktemp -d)`；只把 key 那一行搬进去：`grep '^ZHIPUAI_API_KEY=' ~/.forebrain/.env > "$H/.env" && chmod 600 "$H/.env"`
   （**不要** cat / echo 它）；拷 `~/.forebrain/models.json` 到 `$H/`。写 `$H/forebrain.yaml`（首次启动时会被导入库）：

```yaml
agents:
  defaults:
    enable_subagent: true
  definitions:
    main:
      primary: true
      llm_providers:
      - provider: zhipuai
        model: glm-5.3
        api_key: ${ZHIPUAI_API_KEY}
        base_url: https://open.bigmodel.cn/api/anthropic
        api_path: /v1/messages
```

   `api_path: /v1/messages` 让它走 Anthropic 适配器（`pkg/run/config.go:446` 的 `isAnthropicMessagesPath`）。
3. 在一个空的 scratch 项目目录里 `FOREBRAIN_HOME="$H" "$D/forebrain"` 启动（tmux 高窗口，或按 `run-forebrain` 技能的驱动方式），发：
   「在当前目录用 Go 写一个 `wordfreq` 包：统计文本词频并按频次降序返回前 N 个；写表驱动测试；运行 go test 直到通过。然后派一个子代理给它加一个忽略大小写的选项和对应测试，同样跑通。」
4. 判据（全部满足）：
   - 先确认模型确实在思考：`$H/logs/debug.log*` 里有 `"type":"thinking"` 的响应事件。**若没有**——这个端点没开思考，换下一条路径（见 5）。
   - 本轮与子代理都在合理步数内结束，产出了代码且 `go test` 通过；没有出现连续多步重复相似思考而不调用写文件工具的情况。
   - 第二次及以后的请求体里，assistant 消息以 `"type":"thinking"` 块开头且带 `signature`：`grep -c '"signature"' $H/logs/debug.log*` 远大于 1；
     没有 `prefix-lock` / `refused the replayed thinking` 警告（有 → 记录端点返回的 400 原文并 STOP 报告）。
   - 落库：`sqlite3 "$H/forebrain.state.sqlite" "select count(*) from fb_messages where parts like '%anthropic_thinking_v1%'"` → 大于 0。
   - 退出后 `FOREBRAIN_HOME="$H" "$D/forebrain" resume <sid>` 续聊一句「再加一个基准测试」：首个请求体里历史 assistant 消息仍带 thinking 块，且请求成功。
   - 用 `/model` 切到一个**非** Anthropic 协议的模型（或另一 Anthropic 协议模型）再续聊一句：请求体里**没有**任何 `"type":"thinking"` 块、`reasoning_content` 里也没有 `anthropic_thinking_v1`。
5. 若 glm-5.3 的 Anthropic 端点不返回 thinking：改用 owner 自己配置里的 `opencode-go/qwen3.8-max`（Go 订阅上走 Anthropic 协议，
   `pkg/llm/opencode/endpoints.go:140-143`）。它需要 owner 的 OpenCode 登录；执行环境拿不到时，**不要**去读或拷贝 owner 的凭据，
   而是按仓库惯例把第 3–4 条写成 owner 手动验收清单附在报告里，其余判据全过即可标 DONE。
6. 验收完删掉 `$H`（含那份 `.env`）。

## Test plan

新测试（名字照写，便于评审 grep）：

**`pkg/llm/thinking_carry_test.go`（新建，package `llm`）**
- `TestThinkingCarryRoundTripsByteForByte`：两块 thinking（文本含首尾空格、`\n`、`"`、`\\`、`<>&`、中文、emoji；签名含 `+/=`）+ 一块 redacted（data 任意）
  → `EncodeThinking` → `DecodeThinking` 得到的切片与输入逐字段相等、顺序不变。
- `TestEncodeThinkingDropsEmptyBlocks`：空文本空签名的 thinking、空 data 的 redacted 被丢弃；全空 → 返回 `""`；只有签名没有文本的块保留。
- `TestThinkingCarryIsNotAResponsesPayload`：`Decode(EncodeThinking(...))` → false；`DecodeThinking(Encode([]Item{{ID:"rs_1",EncryptedContent:"x"}}))` → false；
  `DecodeThinking(Prefix+"plain")` → false；`StructuredCarry` 对两种 payload 为 true、对纯文本 carry 与 `""` 为 false。
- `TestExtractSummaryReadsThinkingText`：返回非 redacted 块文本以 `\n` 连接；结果不含 `anthropic_thinking_v1`、不含签名、不含 redacted 的 data。

**`pkg/llm/anthropic/agent_llm_test.go`**
- `TestAnthropicThinkingReplaysOnlyToItsOwnModel`（单元级，直接调 `agentAnthropicMessageFromAPI` / `agentAnthropicMessagesToAPI`）：
  用 JSON 反序列化一个 `anthropicapi.Message`，content 为 `[thinking(sig), redacted_thinking(data), text, tool_use]`；
  转出的消息 `ReasoningModel == "claude-opus-4-6"`、`DecodeThinking` 得到两块；同模型转回请求 → assistant content 依次为 thinking（签名、文本逐字）、redacted（data 逐字）、text、tool_use；
  另一模型 → 只有 text、tool_use；`replayTo == ""` → 只有 text、tool_use；把 `ReasoningModel` 清空 → 只有 text、tool_use。
- `TestAnthropicForeignCarriesAreNeverThinking`：`Name = llm.Encode([]llm.Item{{ID:"rs_1",EncryptedContent:"opaque"}})` 与 `Name = llm.Prefix+"plain reasoning"`
  （各配同名 `ReasoningModel`）→ 请求里没有 thinking / redacted_thinking 块。
- `TestAnthropicThinkingAloneIsNotSent`：一条只有 thinking carry、没有 text / tool_use 的 assistant 消息 → 请求里没有这条 assistant 消息。
- `TestAnthropicStreamedThinkingGoesBackVerbatim`（端到端，范例 `:19`）：第一次 SSE 为 `thinking`（`content_block_start` 文本为空，
  `thinking_delta` 发 `" Plan:\n  step one "`，`signature_delta` 发 `"sig-abc=="`）+ `tool_use` + `stop_reason: tool_use`；
  拿到的 `res.Message` 再与一条 tool 结果组成历史，用**同一个 client** 第二次 `Execute`；解码第二个请求体，
  assistant 消息 `content[0]` 恰为 `{"type":"thinking","thinking":" Plan:\n  step one ","signature":"sig-abc=="}`、`content[1]` 为 tool_use。
  再用 `NewAgentLLM(..., "glm-5.3", ...)` 的 client 发同一历史 → 请求体里没有 thinking 块。
- `TestAnthropicNonStreamingKeepsThinkingCarry`：非流式（`llm.WithNonStreamingRequest` 或该文件现有非流式测试的开关方式，范例 `:472`/`:510`）
  返回含 thinking（带 `signature`）的响应体 → `res.Message` 的 carry 解出同样的块，`ReasoningModel` 为该 client 的模型。
- `TestAnthropicContinuationCarriesLastRoundThinking`（范例 `:693` 的脚本服务器）：第 1 轮 `thinking("first")+text` 撞 `max_tokens`，
  第 2 轮 `thinking("second", 带签名)+text` 以 `end_turn` 结束 → 结果 carry 只含 `"second"`；且第 2 个请求体里没有任何 thinking 块。
- `TestAnthropicExhaustedCutCarriesNoThinking`：四轮全部带 thinking 且都撞 `max_tokens`（续写用尽）→ 结果 `Name == ""`。
- `TestAnthropicThinkingRejectionResendsWithoutIt`：历史里有同模型的 thinking carry；服务器对**含 thinking 的请求**回
  `400 {"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.0: Invalid `signature` in `thinking` block"}}`，
  对不含 thinking 的请求正常回 SSE → 恰好 2 个请求、第 2 个请求体无 thinking 块、`Execute` 成功。
- `TestAnthropicUnrelatedBadRequestIsNotResent`：① 400 消息不含 thinking / signature 字样 → 恰好 1 个请求、返回错误；
  ② 请求本身不带 thinking 时的 400（消息含 thinking 字样）→ 也恰好 1 个请求。

**`pkg/llm/openai/compat_llm_test.go`**
- `TestCompatNeverProjectsAnthropicThinking`：`Name = llm.EncodeThinking(...)`、`ReasoningModel` 与请求模型相同 → `compatMessageToOpenAI` 的
  `ReasoningContent == ""` 且 `Name == ""`。

**`pkg/llm/openai/responses_llm_test.go`**
- `TestOpenAIResponsesNeverReplaysAnthropicThinking`：同上的消息 → `responseReasoningInputItemsFromMessage(msg, 同模型)` 返回 0 项。

**`pkg/state/message_parts_test.go`**（范例 `:198`）
- `TestMessagePartsRoundTripPreservesThinkingCarry`：Name 为 `llm.EncodeThinking` 的结果（文本含 `"`、`\\`、`\n`、中文、emoji）、`ReasoningModel = "qwen3.8-max"`
  → `MessagePartsJSON` → `ParseMessage` 后 `Name` 与原值逐字节相等、`ReasoningModel` 相等、正文与工具调用不受影响。

验证：

```
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/state/ -count=1 -v \
  -run 'ThinkingCarry|EncodeThinking|ExtractSummaryReadsThinking|AnthropicThinking|AnthropicForeignCarries|AnthropicStreamedThinking|AnthropicNonStreamingKeepsThinking|AnthropicContinuationCarries|AnthropicExhaustedCut|AnthropicUnrelatedBadRequest|CompatNeverProjectsAnthropic|ResponsesNeverReplaysAnthropic|PreservesThinkingCarry' \
  2>&1 | grep -E '^(--- |ok|FAIL)'
```

→ 上面列出的 16 个测试全部 `--- PASS`，没有 `FAIL`。

## Done criteria

全部满足：

- [ ] `CGO_ENABLED=1 go build ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/state/ ./pkg/memory/ -count=1` 全部 ok
- [ ] 回归面 `./pkg/agent/ ./pkg/turn/ ./pkg/run/ ./pkg/tui/ ./pkg/gateway/` 全部 ok
- [ ] Test plan 的 16 个新测试全部 PASS
- [ ] `go vet ./pkg/llm/... ./pkg/state/` exit 0；`gofmt -l pkg/llm pkg/state` 无输出
- [ ] `grep -n "Thinking never enters it\|thinking blocks reach only the sink\|converted to fenced text parts" pkg/llm/anthropic/agent_llm.go pkg/state/message_parts.go` 无输出
- [ ] `grep -c "agentAnthropicMessagesToAPI(conversation)" pkg/llm/anthropic/agent_llm.go` → `0`
- [ ] `grep -n "isPayload" pkg/llm/openai/compat_llm.go` 无输出
- [ ] `git status --porcelain` 只列出 Scope 里的文件
- [ ] Step 9 真机判据全过，或（无凭据时）手动验收清单已附在报告里
- [ ] `plans/README.md` 本计划那一行已更新

## STOP conditions

停下报告，不要自己发挥：

- 「Current state」的摘录与代码对不上（drift check 有输出且结构变了）。
- `anthropicapi.ContentBlockUnion` 没有 `Data` 字段，或 `anthropicapi.NewThinkingBlock` / `NewRedactedThinkingBlock` 不存在（SDK 版本不是 `v1.80.0`）。
- 流式累积后 `res.Content` 里的 thinking 块拿不到 `signature_delta` 的内容（`TestAnthropicStreamedThinkingGoesBackVerbatim` 的签名断言失败且原因在 SDK）。
- 回归面出现 Step 8 允许范围之外的失败。
- 真机验收里端点对**带签名、未改动**的回放仍返回 400（兜底生效、出现 `refused the replayed thinking` 警告）——记下 400 原文，报告，不要为了让它通过而改回放格式。
- 真机验收里端点不接受 `"signature":""`（第三方端点返回无签名 thinking 时，SDK 会把签名序列化成空串）——同上，记下原文报告。
- 修复需要动 Out of scope 里的文件。

## 非功能要求（风险 / 对策 / 验证）

| 维度 | 风险 | 对策 | 验证 |
|---|---|---|---|
| 正确性 | 回放内容被改一个字节 → 400 | 不 trim、不归一化，JSON 往返 | `TestThinkingCarryRoundTripsByteForByte`、`TestAnthropicStreamedThinkingGoesBackVerbatim`、`TestMessagePartsRoundTripPreservesThinkingCarry` |
| 鲁棒性 | 端点拒收回放（签名失效、换了端点、不认空签名）导致会话从此每步都失败 | 400 且提到 thinking/signature → 剥掉重发一次 + `slog.Warn`（对齐 Claude Code） | `TestAnthropicThinkingRejectionResendsWithoutIt`、`TestAnthropicUnrelatedBadRequestIsNotResent`、真机 |
| 鲁棒性 | 别的模型收到这份 thinking | 只回放给 `ReasoningModel` 相同的模型；空模型不回放；OpenAI 兼容 / Responses 两路都拒绝该 payload | `TestAnthropicThinkingReplaysOnlyToItsOwnModel`、`TestCompatNeverProjectsAnthropicThinking`、`TestOpenAIResponsesNeverReplaysAnthropicThinking`、真机 `/model` 判据 |
| 延迟 | 正常路径零额外请求；只有拒收时多一次快速 400 往返 | 每次 `Execute` 最多重发一次 | 同上测试的请求计数 |
| 性能 / 成本 | 请求体变大（历史 thinking 全部回传），上下文更快涨满、更早压缩；第三方端点可能把历史 thinking 计入输入 token | 这正是修复本身（与 Claude Code、本仓库 OpenAI 兼容路径一致）；压缩照常兜底；每条消息的渲染只依赖它自己，请求前缀逐字节稳定，提示缓存照常命中 | 真机看用量；`ApplyPromptCache` 测试照旧全绿 |
| 性能 | 每个请求对每条带 carry 的 assistant 消息做一次 JSON 解码 | 与 Responses 路径同量级，远小于请求体本身的 JSON 序列化 | 回归面测试时长不显著变化 |
| 可维护性 | carry 有了第三种格式 | 三种格式全部集中在 `pkg/llm/retry.go`，各自的 decode 互斥；`StructuredCarry` 是「这是不是结构化 payload」的唯一判定 | `TestThinkingCarryIsNotAResponsesPayload` |
| 可扩展性 | 以后再有协议要回传结构化思考 | 照 `anthropic_thinking_v1` 加一个版本 + 在 `StructuredCarry` 与 `ExtractSummary` 各加一支 | — |
| 体验 | 界面上思考显示出 JSON | `ExtractSummary` 认新 payload，只返回思考文本 | `TestExtractSummaryReadsThinkingText` |
| 兼容 | 旧会话 | 旧行没有 carry，读出来就是不回放，和今天一样；不需要迁移，库结构不变 | `TestMessagePartsRoundTripPreservesThinkingCarry` 外加既有往返测试 |

## Maintenance notes

- **主代理与子代理**：两边都走同一个 Anthropic 适配器，修一处两边都生效；真机判据里同时看了子代理的请求。
- **回放判定只比模型 ID、不比 provider**：两个 provider 用完全相同的模型 ID 时，thinking 会互相回放。签名通常绑定的是模型本身；
  若某端点因此拒收，400 兜底会接住。需要更严时，可以把 carry 的生产者从模型 ID 扩成 provider+模型——那会牵动三种协议，另立计划。
- **`EstimateMessageWith` 把整个 carry（含 JSON 外壳与签名）计入估算**：Claude 的签名较长，估算会偏高一些、可能略早触发压缩；
  估算的开销校准会吸收常量偏差。若真机发现压缩明显提前，再单独改估算只计 thinking 文本。
- **续写**：只带最后一轮 thinking 是有意的（见「已拍板的决定」）。若以后续写改成把前一轮的 thinking 也送进下一轮请求，
  这里要同步改成带全部轮次。
- **评审重点**：①`agentAnthropicMessagesToAPI` 的 assistant 分支里 thinking 是否排在最前、「只有 thinking 就不发」是否保住；
  ②`Execute` 的兜底是否只在「请求确实带了 thinking + 400 + 提到 thinking/signature」时触发、且每次 `Execute` 至多一次；
  ③`lastRound` 是否无条件赋值；④任何地方有没有对 thinking 文本、签名、data 做 trim。
- **刻意不做**：往请求里加 `thinking` 参数（是否启用思考、预算多少）——本计划只修丢弃；Claude 官方模型在不传该参数时本来就不思考，改完对它是无操作。
