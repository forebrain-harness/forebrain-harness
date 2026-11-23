# Plan 276: Responses 协议——回放历史时思考、正文、工具调用一样都不丢；响应里的多段正文全部保留

> **Executor instructions**: 按步骤顺序执行，每步验证通过再做下一步。遇到「STOP conditions」就停下报告，
> 不要自己发挥。**不要 commit / push / merge**（仓库规矩：owner 手动提交）。做完后把 `plans/README.md`
> 里本计划那一行的状态改成 DONE 并附一句摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git status --porcelain -- pkg/llm/openai/responses_llm.go pkg/llm/openai/responses_llm_test.go pkg/llm/thinking_fence.go pkg/llm/thinking_fence_test.go`
> 与 `git diff --stat d9c45060 -- <同样这些路径>`。
> → 写计划时两条都为空（`pkg/llm/thinking_fence*.go` 还不存在）。若 plan 275 已先执行，`responses_llm_test.go` 会多一条
> `TestOpenAIResponsesNeverReplaysAnthropicThinking`——属预期，不是 STOP。
> 其余情况逐条对照「Current state」的摘录：**结构对得上就继续**，对不上才 STOP。
> （不要用 `git diff d9c45060..HEAD`：比的是两个 commit，未提交改动看不见。）

## Status

- **Priority**: P1（ChatGPT 订阅与一切 `/responses` 接入：同一模型续聊时看不到自己上一轮的回答；工具循环里每一步的思考都不回传；非流式响应只留第一段正文）
- **Effort**: M
- **Risk**: MED（改的是 Responses 协议每个请求里 assistant 历史的投影和响应正文的提取；只有出 bug 的那几类消息字节会变，其余逐字节不变）
- **Depends on**: none。**plan 277 依赖本计划**新增的 `llm.ThinkingFence` / `llm.IsThinkingFence`。
- **Category**: bug
- **Planned at**: commit `d9c45060`, 2026-11-08

## Why this matters

**问题一：历史投影三选一。** Responses 客户端把历史里的一条 assistant 消息投影成请求的 input items 时：
有工具调用就**只**发 function_call；否则有同模型的思考就**只**发 reasoning items；否则才发正文。于是：

1. **带工具调用的步骤，思考从来不回传**——连当前这一轮也不传；伴随工具调用的那段正文也一起丢。
   工具循环里最需要连续性的恰恰是这些步骤：模型每一步都看不到自己上一步为什么调这个工具。
2. **带思考的最终回答，正文被丢掉**（同一模型时）：只剩加密的 reasoning item，
   模型在下一轮看不到自己上一轮回答了什么。11-07 的 `ba824f23` 让思考随落库保留之后，
   从库里重建的历史（重开会话、续聊）也会走到这个分支。

**问题二：响应里只保留第一段正文。** 非流式请求只取第一个 message 输出项里的第一段文字，后面的 message 项、
同一项里后面的文字段全部丢掉；流式请求在「一个文本增量都没收到」时走的两个兜底分支（`response.completed` / `response.incomplete`）也一样。
而流式主路径把所有文本增量依次拼接——同一个响应，流式和非流式拿到的正文不一样。

正确的投影：每条 assistant 消息按模型产出的顺序，依次发出同模型的 reasoning items、正文、function_call，三样都不能少。
正确的提取：响应里所有 message 项的所有文字段，按顺序拼接，与流式增量拼出来的完全一致。

## 复现（owner 2026-11-08 要求随计划附上；已在当前代码上实跑，仓库文件零改动）

### 问题一（历史投影）

用 `go test -overlay` 把一个临时测试挂进 `pkg/llm/openai` 包（overlay 只是让 go 工具链把 scratch 里的文件当成包内文件，不写仓库）。
历史为：第 1 轮带思考的工具调用 → 工具结果 → 带思考的最终回答 → 第 2 轮用户消息；所有思考都由同一模型产出。

临时测试文件（放在任意 scratch 目录，例如 `$S/probe/zz_probe_test.go`）：

```go
package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func TestZZProbeResponsesHistory(t *testing.T) {
	var captured map[string]any
	c := newTestOpenAIResponsesLLMWithStream(t, false, func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(body, &captured)
		resp := `{"id":"resp_1","object":"response","created":1,"model":"gpt-test","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(resp)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
	})
	model := c.(*openAIResponsesLLM).model
	carry := func(id string) string { return llm.Encode([]llm.Item{{ID: id, Summary: []string{"why " + id}, EncryptedContent: "enc_" + id}}) }
	call := llm.AssistantMessage([]llm.ContentPart{llm.Text("I will run ls")}, llm.ToolCall{ID: "call_1", Type: "function", Function: llm.FunctionCall{Name: "shell", Arguments: `{"cmd":"ls"}`}})
	call.Name, call.ReasoningModel = carry("rs_call"), model
	answer := llm.AssistantMessage([]llm.ContentPart{llm.Text("Final answer of turn one")})
	answer.Name, answer.ReasoningModel = carry("rs_answer"), model
	msgs := []llm.Message{
		llm.UserMessage(llm.Text("turn one")),
		call,
		{Role: llm.RoleTool, ToolCallID: "call_1", Parts: []llm.ContentPart{llm.Text("a.txt")}},
		answer,
		llm.UserMessage(llm.Text("turn two")),
	}
	if _, err := c.Execute(context.Background(), msgs, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("model=%s", model)
	for i, it := range captured["input"].([]any) {
		b, _ := json.Marshal(it.(map[string]any))
		t.Logf("input[%d] %s", i, b)
	}
}
```

运行（在仓库根目录；`$S` 为放探针的 scratch 目录）：

```bash
printf '{"Replace":{"%s":"%s"}}' "$PWD/pkg/llm/openai/zz_probe_test.go" "$S/probe/zz_probe_test.go" > "$S/probe/overlay.json"
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 -overlay "$S/probe/overlay.json" \
  ./pkg/llm/openai/ -run TestZZProbeResponsesHistory -count=1 -v 2>&1 | grep -E 'input\[|model='
```

`d9c45060` 上的实际输出：

```
model=gpt-test
input[0] {"content":[{"text":"turn one","type":"input_text"}],"role":"user"}
input[1] {"arguments":"{\"cmd\":\"ls\"}","call_id":"call_1","name":"shell","type":"function_call"}
input[2] {"call_id":"call_1","output":"a.txt","type":"function_call_output"}
input[3] {"encrypted_content":"enc_rs_answer","id":"rs_answer","summary":[{"text":"why rs_answer","type":"summary_text"}],"type":"reasoning"}
input[4] {"content":[{"text":"turn two","type":"input_text"}],"role":"user"}
```

对照期望：
- `input[1]` 之前应有 reasoning `rs_call` 和 assistant 正文 `I will run ls`——**都没有**；
- `input[3]` 之后应有 assistant 正文 `Final answer of turn one`——**没有**。

修复后同一探针应输出 8 项：user / reasoning `rs_call` / assistant `I will run ls` / function_call / function_call_output /
reasoning `rs_answer` / assistant `Final answer of turn one` / user。Step 6 把它转成正式回归测试。

### 问题二（多段正文）

由代码直接确定，并且**已有测试把这个行为钉住了**：`pkg/llm/openai/responses_llm_test.go:324`
`TestExtractTextFromOutputItem_MultipleContent` 的注释写着 `multiple content items, should return first matching one`，
断言一个 message 项里 `output_text "This should be extracted"` 之后的 `text "This should be skipped"` 被丢掉。
Step 4 会把这条断言改成两段都保留（它钉的就是 bug）。

## 已拍板的决定

- **owner 2026-11-08**：为 Responses 的历史投影问题单独立计划、复现随计划附上；**多段正文只保留第一段的问题必须修复，并入本计划**。
- **投影规则**（与 plan 275 对 Anthropic 协议的规则同构，三种协议一个口径）：一条 assistant 消息 →
  ① 同模型的 reasoning items（`responseReasoningInputItemsFromMessage`，判定不变）→ ② 正文（若有）→ ③ function_call（若有）。
  这正是 Responses 接口产出它们的顺序（reasoning 先于它引出的 message / function_call）。
- **展示用的 thinking 围栏不当正文发**：Responses 客户端在「只有思考、没有正文也没有工具调用」时，会把思考摘要包成
  ```` ```thinking\n…\n``` ```` 塞进正文给界面显示（`responses_llm.go:1326-1328`、`:1763-1766`）。投影时这段**一律跳过**：
  同一模型时它与 reasoning items 重复；换了模型时它就是把旧模型的思考当正文泄露给新模型。
  围栏的生成与判定提成 `pkg/llm` 的 `ThinkingFence` / `IsThinkingFence` 两个函数，本计划先用上，plan 277 让另外三个适配器也用同一对函数。
- **多段正文的拼法与流式完全一致**：流式主路径把每个 `response.output_text.delta` 原样追加、段与段之间不加任何分隔（`:1451-1459`）；
  非流式与流式兜底分支按同样顺序直接拼接所有 message 项的所有 `output_text` / `text` 段，不加分隔。`refusal` 段不是正文，照旧不取。
- **没有加密内容的思考条目不回放**（owner 2026-11-08 复核追加）：这个客户端每个请求都设 `Store: false`（`:900-911` 的注释写明从不引用已存储的响应），
  服务端不保存任何条目，回放时只能靠条目自带的 `encrypted_content` 还原；只有 `id` 和摘要的条目服务端无从解析。
  今天的 `responseReasoningInputItemsFromMessage` 会把「有摘要、无加密内容」的条目照发（只带 `id` 与摘要），本计划又让工具步骤的思考也回放，暴露面更大——一并改掉。
- **每条消息的投影只取决于它自己和请求模型**：不引入「看后面有没有新用户消息」之类的规则，保持请求前缀逐字节稳定（提示缓存）。
  不带 carry、不带围栏的消息投影与今天**逐字节相同**（Step 6 有测试钉住）。
- **同模型回传所有历史轮的思考**：与 plan 275、Claude Code 一致（owner 2026-11-08 已确认）。

## Current state

所有行号基于 `d9c45060`，未注明文件的都是 `pkg/llm/openai/responses_llm.go`。

**① 投影：三选一的 `else if` 链**（`newResponseParams`，`:799` 起；assistant 分支 `:836-849`）：

```go
		case llm.RoleAssistant:
			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					if strings.TrimSpace(tc.Function.Name) == "" || strings.TrimSpace(tc.ID) == "" {
						continue
					}
					input = append(input, responses.ResponseInputItemParamOfFunctionCall(llm.SanitizeToolCallArguments(tc.Function.Arguments), tc.ID, tc.Function.Name))
				}
			} else if items := responseReasoningInputItemsFromMessage(msg, c.model); len(items) > 0 {
				input = append(input, items...)
			} else if content := msg.TextContent(); content != "" {
				input = append(input, responses.ResponseInputItemParamOfMessage(content, responses.EasyInputMessageRoleAssistant))
			}
```

分支之后（`:884-889`）有一段把 `input[before:]` 逐项 JSON 往返进 `rawInput` 的循环——它对新增的项同样生效，不用改。
`newResponseParams` 有三个调用点：`Execute` 的续写循环 `:646`、`ExecuteStructured` `:783`、远端压缩 `:989`——三处都应得到修正后的投影，不需要分别改。

**② 回放思考的函数**（`:1861-1891`）：`responseReasoningInputItemsFromMessage(msg, model)` 先 `llm.ReplaysReasoningTo(msg, model)`，
再 `llm.Decode(msg.Name)`，把每个 item 投影成带 `id`、`summary`、`encrypted_content` 的 reasoning input item；不是同一模型或不是 Responses payload 时返回 nil。
同模型判定不变；但循环里的跳过条件（`:1881-1883`）是「摘要和加密内容**都**为空才跳过」，所以只有摘要、没有加密内容的条目会被照发——Step 3b 改这一条：

```go
		if len(summaryParts) == 0 && strings.TrimSpace(item.EncryptedContent) == "" {
			continue
		}
		param := responses.ResponseInputItemParamOfReasoning(item.ID, summaryParts)
		if param.OfReasoning != nil && strings.TrimSpace(item.EncryptedContent) != "" {
			param.OfReasoning.EncryptedContent = openaigo.String(strings.TrimSpace(item.EncryptedContent))
		}
```

**③ 消息是怎么造出来的**——非流式 `executeSyncDetail`（`:1290` 起）与流式 `executeStreaming`（结尾 `:1760-1775`）同形：

```go
	parts := []llm.ContentPart{}
	if content != "" {
		parts = append(parts, llm.Text(content))
	} else if len(toolCalls) == 0 && strings.TrimSpace(reasoning) != "" {
		parts = append(parts, llm.Text("```thinking\n"+reasoning+"\n```"))
	}

	msg := llm.Message{
		Role:      llm.RoleAssistant,
		Parts:     parts,
		ToolCalls: toolCalls,
	}
	if carry := llm.Encode(reasoningItems); carry != "" {
		msg.Name = carry
		msg.ReasoningModel = c.model
	}
```

（非流式在 `:1323-1328`，变量名 `content` / `reasoning`；流式在 `:1761-1766`，变量名 `textBuf` / `reasoningText`。）
所以「正文 + 工具调用 + 思考」三者可以同时出现在一条消息上——正是 ① 的三选一会丢东西的形状。

**④ 已有的围栏判定**（`responsesAnswerText`，`:750-770`，续写循环 `:676` 用它取「不含围栏的正文」）：

```go
func responsesAnswerText(m *llm.Message) string {
	...
	for _, p := range m.Parts {
		if p.Type != llm.ContentTypeText {
			continue
		}
		if strings.HasPrefix(p.Text, "```thinking\n") && strings.HasSuffix(p.Text, "\n```") {
			continue
		}
		b.WriteString(p.Text)
	}
	return b.String()
}
```

注意它把各段正文**直接拼接**，而 ① 用的 `msg.TextContent()`（`pkg/llm/llm.go:240-256`）在段与段之间插 `\n`。
投影必须继续用 `TextContent` 的拼法（否则多段正文的消息字节会变、语义也变），所以只复用**围栏判定**，不复用 `responsesAnswerText` 本身。
同一个围栏字面量还出现在 `pkg/llm/openai/compat_llm.go:1520` 与 `pkg/turn/model.go:1217`——那两处由 plan 277 收口，本计划不碰。

**⑤ 正文提取只取第一段**：

```go
// :1831-1844
func extractTextFromOutputItem(item responses.ResponseOutputItemUnion) string {
	// Check if this is a message type output
	if item.Type == "message" && len(item.Content) > 0 {
		// Extract text from the first content item
		for _, content := range item.Content {
			// Content is also a union type, check for text or output_text
			llm.LogDebug("openai_responses", fmt.Sprintf("content.Type=%s text_length=%d", content.Type, len(content.Text)))
			if content.Type == "text" || content.Type == "output_text" {
				return content.Text
			}
		}
	}
	return ""
}
```

调用点三处，都只要第一个非空结果：
- 非流式 `executeSyncDetail` `:1300-1304`：`if text := extractTextFromOutputItem(item); text != "" && content == "" { content = text }`；
- 流式 `response.completed` 兜底 `:1624-1634`、`response.incomplete` 兜底 `:1697-1707`：`if textBuf.Len() == 0 { for _, item := range …Output { if content := extractTextFromOutputItem(item); content != "" { …; textBuf.WriteString(content); …OnDelta(content); break } } }`。

流式主路径 `:1451-1459`：`case "response.output_text.delta": … textBuf.WriteString(delta)`——所有段、所有项的增量依次拼接，不加分隔。
`executeSyncDetail` 返回的 `content` 也是非流式回放给 sink 的正文（`ReplayToSink`），所以修复后界面上也能看到完整正文。

**⑥ 现有测试**（`pkg/llm/openai/responses_llm_test.go`）：
- 投影类，修复后应**原样通过**（都只用「只有工具调用」或「只有思考」的 assistant 消息）：
  `TestOpenAIResponsesLLMEncodesToolHistoryAsFunctionCallItems`（`:1008`）、`TestOpenAIResponsesLLMReplaysReasoningCarryAsReasoningInputItems`（`:1428`，
  assistant 只有 carry、无正文 → 2 项）、`TestOpenAIResponsesReasoningCarryReplaysOnlyToItsOwnModel`（`:1472`）、
  `TestResponsesToolResultImageProjectsAsFollowingUserMessage`（`:1751`）。
- 提取类：`TestExtractTextFromOutputItem_OutputText`（`:253`）、`_Text`（`:273`）、`_EmptyContent`（`:293`）、`_WrongType`（`:306`）原样通过；
  `_MultipleContent`（`:324`）钉的是 bug，Step 4 改期望。
- 围栏类：`TestOpenAIResponsesSyncSurfacesReasoningOnlyOutput`（`:1137`）、`TestOpenAIResponsesStreamingSurfacesReasoningOnlyOutput`（`:1270`）断言围栏的前后缀，原样通过。
- 测试 helper：`newTestOpenAIResponsesLLMWithStream(t, stream bool, fn)`。

**⑦ 仓库约定**：注释写「为什么」、完整英文句子，密度参照 `newResponseParams` 里已有注释（如 `:861-870` 的图片投影说明）；
新增函数都写 doc comment；`pkg/llm` 的导出函数必须有 doc comment。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| openai 包 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/openai/ -count=1` | ok（写计划时基线实测 ok） |
| llm 包（约 2.5 分钟） | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ -count=1` | ok |
| 单测 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/openai/ -count=1 -run '<名字>' -v` | 指定测试 PASS |
| 回归面 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/run/ ./pkg/turn/ -count=1` | ok |
| 静态检查 | `go vet ./pkg/llm/ ./pkg/llm/openai/` | exit 0 |
| 格式 | `gofmt -l pkg/llm` | 无输出 |

`FOREBRAIN_HOME` 必须显式指向 mktemp 目录（仓库硬规矩）。链接时的 `ld: warning: ... built for newer 'macOS' version` 是已知噪声。

## Scope

**In scope**（只改这些文件）：
- `pkg/llm/thinking_fence.go`（新建）+ `pkg/llm/thinking_fence_test.go`（新建）
- `pkg/llm/openai/responses_llm.go` —— 围栏生成与判定改用新函数、`newResponseParams` 的 assistant 分支、正文提取
- `pkg/llm/openai/responses_llm_test.go` —— 新测试 + `TestExtractTextFromOutputItem_MultipleContent` 改期望

**Out of scope**（看着相关，不要动）：
- `responseReasoningInputItemsFromMessage` 的同模型判定——它是对的，只是之前被 `else if` 挡住了。（该函数只改 Step 3b 那一条跳过条件。）
- `pkg/llm/openai/compat_llm.go` 与 `pkg/turn/model.go` 里的围栏字面量、OpenAI 兼容 / Anthropic / Google 三个适配器对围栏的处理——全部归 plan 277。
- 消息构造处的「是否合成围栏」的条件——界面要显示它，保留；本计划只把字面量换成 `llm.ThinkingFence`。
- `Store: false`、`include: reasoning.encrypted_content` 等请求参数——不动。
- `pkg/llm/retry.go` 的 carry 编解码——不动。

## Steps

### Step 1: 在 `pkg/llm` 新建围栏的生成与判定

新建 `pkg/llm/thinking_fence.go`：

```go
package llm

import "strings"

// thinkingFenceOpen and thinkingFenceClose bracket the block a client shows
// when a reply held reasoning and nothing else.
const (
	thinkingFenceOpen  = "```thinking\n"
	thinkingFenceClose = "\n```"
)

// ThinkingFence wraps reasoning as the ```thinking block a client puts in a
// reply's text when the model returned reasoning and nothing else, so the
// person sees the thinking instead of an empty answer. The block is a display
// form of the reasoning, never an answer the model wrote.
func ThinkingFence(reasoning string) string {
	return thinkingFenceOpen + reasoning + thinkingFenceClose
}

// IsThinkingFence reports text that is such a block.
func IsThinkingFence(text string) bool {
	return strings.HasPrefix(text, thinkingFenceOpen) && strings.HasSuffix(text, thinkingFenceClose)
}
```

新建 `pkg/llm/thinking_fence_test.go`：`TestThinkingFenceRoundTrip`——`IsThinkingFence(ThinkingFence(x))` 对 `"a"`、`"多行\n思考"`、`""` 都为 true；
`IsThinkingFence` 对 `"plain answer"`、`"```thinking\nno close"`、`"```go\nx\n```"` 为 false；`ThinkingFence("a") == "```thinking\na\n```"`（字节级钉住，老会话里存的就是这个形状）。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ -count=1 -run TestThinkingFence -v` → PASS。

### Step 2: Responses 客户端改用这对函数，并加投影用的正文 helper

`pkg/llm/openai/responses_llm.go`：
1. `:1327` 与 `:1765` 的 `"```thinking\n"+…+"\n```"` 改成 `llm.ThinkingFence(reasoning)` / `llm.ThinkingFence(reasoningText)`（字节不变）。
2. `responsesAnswerText` 里 `if strings.HasPrefix(p.Text, "```thinking\n") && strings.HasSuffix(p.Text, "\n```")` 改成 `if llm.IsThinkingFence(p.Text)`（行为不变）。
3. 新增投影用的正文 helper（**保持 `TextContent` 的拼法**）：

```go
// responsesAssistantText is the text an assistant message sends back: its
// text parts joined as llm.TextContent joins them, without the thinking fence.
// The fence duplicates the reasoning items the same model is replayed, and
// handing it to another model would pass this model's reasoning off as an
// answer.
func responsesAssistantText(m llm.Message) string {
	kept := make([]llm.ContentPart, 0, len(m.Parts))
	for _, p := range m.Parts {
		if p.Type == llm.ContentTypeText && llm.IsThinkingFence(p.Text) {
			continue
		}
		kept = append(kept, p)
	}
	return llm.TextContent(kept...)
}
```

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/openai/ -count=1` → ok（到这里是纯重构，既有测试全绿）；
`grep -c 'thinking\\n' pkg/llm/openai/responses_llm.go` → `0`（围栏字面量只剩 `pkg/llm/thinking_fence.go` 一处，本文件不再手写）。

### Step 3: 投影改成「思考 → 正文 → 工具调用」，三样都发

把 ① 的 assistant 分支整段替换为：

```go
		case llm.RoleAssistant:
			// An assistant message goes back as the items the model produced,
			// in the order it produced them: its reasoning (to the model that
			// wrote it only), its text, then its calls. These used to be
			// alternatives — a message with calls lost its reasoning and its
			// text, and an answer with reasoning lost the answer — so a model
			// re-derived every step of a tool loop and never saw what it had
			// replied the turn before.
			input = append(input, responseReasoningInputItemsFromMessage(msg, c.model)...)
			if content := responsesAssistantText(msg); content != "" {
				input = append(input, responses.ResponseInputItemParamOfMessage(content, responses.EasyInputMessageRoleAssistant))
			}
			for _, tc := range msg.ToolCalls {
				if strings.TrimSpace(tc.Function.Name) == "" || strings.TrimSpace(tc.ID) == "" {
					continue
				}
				input = append(input, responses.ResponseInputItemParamOfFunctionCall(llm.SanitizeToolCallArguments(tc.Function.Arguments), tc.ID, tc.Function.Name))
			}
```

逐字节兼容性自查（评审会核）：
- 只有正文、无 carry、无围栏的消息：之前走第三支 `TextContent()` → 现在 `responsesAssistantText` 对无围栏消息等于 `TextContent()` → **相同**。
- 只有工具调用、无正文、无 carry：之前只发 function_call → 现在也只发 function_call → **相同**。
- 只有 carry、无正文（`:1428` 那条测试的形状）：之前只发 reasoning items → 现在也只发 → **相同**。
- 变化的只有 bug 本身那几类：工具调用 + 正文、工具调用 + 同模型 carry、正文 + 同模型 carry，以及带围栏的消息（围栏不再当正文发）。

**Verify**: 「复现 · 问题一」的探针再跑一次 → 输出 8 项，顺序为 user / reasoning `rs_call` / assistant `I will run ls` / function_call /
function_call_output / reasoning `rs_answer` / assistant `Final answer of turn one` / user；
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/openai/ -count=1` → ok。跑完删掉探针文件与 overlay.json（它们在 scratch 目录，不在仓库里）。

### Step 3b: 没有加密内容的思考条目不回放

`responseReasoningInputItemsFromMessage` 循环里，把 ② 那段跳过条件改成：**`strings.TrimSpace(item.EncryptedContent) == ""` 就跳过**（不论有没有摘要）。
补一句注释说明为什么：请求总是 `Store: false`，服务端不保存条目，只凭 `id` 与摘要无法还原，发出去只会让整个请求失败；摘要本身对模型没有可回放的状态。
其余（`id`、摘要、加密内容的写法）不变。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/openai/ -count=1` → ok
（既有的 `TestOpenAIResponsesLLMReplaysReasoningCarryAsReasoningInputItems` 与 `...ReplaysOnlyToItsOwnModel` 用的条目都带加密内容，不受影响）。

### Step 4: 正文提取保留所有段

1. `extractTextFromOutputItem` 改成返回该 message 项里**所有** `text` / `output_text` 段按顺序直接拼接的结果（去掉 `return content.Text` 的提前返回，改为写进 `strings.Builder`；
   保留那行 `llm.LogDebug`；把「Extract text from the first content item」注释改成如实描述）。函数名不改。
2. 紧挨着新增：

```go
// responseOutputText is a response's whole answer text: every text segment of
// every message item, in order, joined exactly as the streamed text deltas
// join — with nothing between them — so a response reads the same whether it
// arrived streamed or not. Only the first segment used to be kept.
func responseOutputText(items []responses.ResponseOutputItemUnion) string {
	var b strings.Builder
	for _, item := range items {
		b.WriteString(extractTextFromOutputItem(item))
	}
	return b.String()
}
```

3. 三个调用点：
   - `executeSyncDetail`：循环前 `content := responseOutputText(resp.Output)`（替换原来的 `var content string`），删掉循环里 `:1301-1304` 那段「只取第一个」；
     reasoning 与 function_call 的收集不动。
   - 流式 `response.completed` 兜底（`:1624-1634`）与 `response.incomplete` 兜底（`:1697-1707`）：把「`for` + `extractTextFromOutputItem` + `break`」换成
     `if content := responseOutputText(<同一个 Output>); content != "" { llm.NotifyResponseStarted(sink, &responseStarted); textBuf.WriteString(content); if sink != nil && sink.OnDelta != nil { sink.OnDelta(content) } }`
     ——`if textBuf.Len() == 0` 的外层守卫不变（收到过增量就以增量为准）。
4. `TestExtractTextFromOutputItem_MultipleContent`（`:324`）：期望改成 `"This should be extractedThis should be skipped"`，
   测试里的注释改成「multiple content items: every text segment is kept, refusal is not text」（refusal 段仍被跳过）。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/openai/ -count=1` → ok；
`grep -n 'content == ""' pkg/llm/openai/responses_llm.go` 不再出现在 `executeSyncDetail` 里；
`grep -c 'extractTextFromOutputItem(item); content != ""' pkg/llm/openai/responses_llm.go` → `0`。

### Step 5: 回归面

**Verify**: 「Commands」的回归面命令 → ok；`go vet ./pkg/llm/ ./pkg/llm/openai/` → exit 0；`gofmt -l pkg/llm` → 无输出。
若 `pkg/run` / `pkg/turn` 有测试逐字比较 Responses 请求的 input 列表或非流式正文而失败：**仅当**差异恰是 Step 3 自查里「变化的那几类」或「多段正文现在全部保留」时，
才可以更新期望并在报告里逐条列出；其他失败 → STOP。

### Step 6: 写回归测试

见「Test plan」。

**Verify**: Test plan 末尾的命令 → 新测试全部 `--- PASS`。

### Step 7: 真机验收（fake provider 驱动不了：需要真实的 Responses 端点接受回放的 reasoning items + 正文 + function_call 组合）

仓库规矩：主树构建、隔离 `FOREBRAIN_HOME`、key 不上命令行。

1. 构建：`CGO_ENABLED=1 go build -tags fts5 -o "$D/forebrain" ./cmd/forebrain && scripts/install-dictionary.sh "$D"`。
2. `H=$(mktemp -d)`；`grep '^OPENAI_API_KEY=' ~/.forebrain/.env > "$H/.env" && chmod 600 "$H/.env"`（**不要** cat / echo）；
   拷 `~/.forebrain/models.json` 到 `$H/`；写 `$H/forebrain.yaml`：

```yaml
agents:
  defaults:
    enable_subagent: true
  definitions:
    main:
      primary: true
      llm_providers:
      - provider: openai
        model: gpt-5.4-mini
        api_key: ${OPENAI_API_KEY}
        base_url: https://api.openai.com/v1
        api_path: /responses
```

   `api_path: /responses` 让它走 Responses 客户端（`pkg/run/config.go:449`）。
3. 在空 scratch 项目目录里 `FOREBRAIN_HOME="$H" "$D/forebrain"` 启动，依次发：
   - 第 1 轮：「随便想一个 4 位数字，直接把它告诉我，不要解释。」（这个数字只存在于模型自己的回答正文里）
   - 第 2 轮：「你上一条回复里的那个数字是多少？只回答数字。」
   - 第 3 轮：「在当前目录用 Go 写一个 `wordfreq` 包和表驱动测试，运行 go test 直到通过。」
4. 判据（全部满足）：
   - 第 2 轮答对第 1 轮的数字（修复前同一模型看不到自己的回答正文，只能瞎猜）。
   - 第 3 轮在合理步数内完成、`go test` 通过。
   - `$H/logs/debug.log*` 里第 3 轮后续请求的 input 中，`"type":"reasoning"` 项出现在对应的 `"type":"function_call"` 项之前；第 2 轮请求的 input 里有第 1 轮的 assistant 正文。
   - 全程没有 400（尤其是 `Item with id … not found`、`provided without its required following item` 一类）。
   - 退出后 `FOREBRAIN_HOME="$H" "$D/forebrain" resume <sid>` 再问一句「第 1 轮的数字是多少？」→ 答对（验证从库重建的历史同样完整）。
5. 问题二（多段正文）走的是非流式与流式兜底分支，真机无法稳定触发，**不做真机验收**，由 Step 6 的单测覆盖（仓库规矩：只验有意义的项）。
6. 拿不到可用的 `OPENAI_API_KEY`（或该 key 无此模型权限）时：**不要**去读或拷贝 owner 的 ChatGPT 登录；按仓库惯例把第 3–4 条写成
   owner 手动验收清单（owner 用自己的 ChatGPT 订阅模型照做），其余判据全过即可标 DONE。
7. 验收完删掉 `$H`（含那份 `.env`）。

## Test plan

**`pkg/llm/thinking_fence_test.go`**：`TestThinkingFenceRoundTrip`（Step 1）。

**`pkg/llm/openai/responses_llm_test.go`**，投影类结构仿照 `TestOpenAIResponsesLLMReplaysReasoningCarryAsReasoningInputItems`（`:1428`）——
用 `newTestOpenAIResponsesLLMWithStream(t, false, …)` 抓请求体、断言 `input` 列表：

- `TestResponsesHistoryKeepsEveryAssistantItem`：即「复现 · 问题一」的探针转正，断言 8 项的 `type`/`role`/关键字段逐项相等
  （reasoning 的 `id` 与 `encrypted_content`、assistant 正文原文、function_call 的 `call_id`/`name`/`arguments`）。
- `TestResponsesHistoryToAnotherModelKeepsTextAndCalls`：同一份历史，但把两条 assistant 的 `ReasoningModel` 设成别的模型 →
  没有任何 reasoning 项；`I will run ls`、function_call、`Final answer of turn one` 都在，顺序同上。
- `TestResponsesReasoningOnlyMessageNeverSendsItsFence`：assistant 消息 Parts 为 `llm.ThinkingFence("why")`、带同模型 carry、无工具调用 →
  同模型：只有 1 个 reasoning 项、没有 assistant message 项；另一模型：这条 assistant 消息不产生任何 input 项（不泄露围栏）。
- `TestResponsesPlainHistoryProjectionIsUnchanged`：不带 carry 的历史——纯正文 assistant（两段正文，验证 `\n` 拼法）、只有工具调用的 assistant、工具结果——
  投影出的 `input` JSON 与**按旧规则手写**的期望逐字节相等（钉住老会话请求前缀不变；不许拿新代码的输出回填期望）。
- `TestResponsesAssistantTextAndCallsWithoutReasoning`：正文 + 工具调用、无 carry → assistant message 在前、function_call 在后（修复前正文丢失）。
- `TestResponsesNeverReplaysReasoningWithoutEncryptedContent`（Step 3b）：同模型 carry 里两个条目——`{ID:"rs_a", Summary:["x"]}`（无加密内容）与
  `{ID:"rs_b", Summary:["y"], EncryptedContent:"enc_b"}` → 请求里只有 `rs_b` 一个 reasoning 项，且带 `encrypted_content`。

正文提取类：

- `TestResponsesSyncKeepsEveryTextSegment`：非流式响应 `output` 为 `[reasoning, message{output_text "A", refusal "R", output_text "B"}, message{output_text "C"}]`
  → `res.Message.TextContent() == "ABC"`；同时 sink 的 `OnDelta` 回放（若该文件已有非流式 sink 回放测试，照它的抓法）拿到的正文也是 `"ABC"`。
- `TestResponsesStreamingFallbackKeepsEveryTextSegment`：流式里**不发任何** `response.output_text.delta`，只在 `response.completed` 的 `response.output`
  里带两个 message 项（`"A"`、`"BC"` 两段）→ 正文 `"ABC"`；再来一份用 `response.incomplete` 收尾的同形用例 → 同样 `"ABC"`（照 `:1270` 那条流式测试的 SSE 写法）。
- `TestResponsesStreamingAndSyncReadTheSameText`：同一组两 message 项三段正文，流式（逐段发 `output_text.delta`）与非流式各跑一次 → 两边 `TextContent()` 相等。

验证：

```
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ ./pkg/llm/openai/ -count=1 -v \
  -run 'TestThinkingFence|TestResponsesHistory|TestResponsesReasoningOnlyMessage|TestResponsesPlainHistory|TestResponsesAssistantTextAndCalls|TestResponsesNeverReplaysReasoningWithout|TestResponsesSyncKeepsEvery|TestResponsesStreamingFallbackKeepsEvery|TestResponsesStreamingAndSyncRead|ReplaysReasoningCarry|ReasoningCarryReplaysOnly|EncodesToolHistory|ToolResultImageProjects|TestExtractTextFromOutputItem|SurfacesReasoningOnlyOutput' \
  2>&1 | grep -E '^(--- |ok|FAIL)'
```

→ 10 个新测试，加上既有的 4 个投影测试、5 个 `TestExtractTextFromOutputItem_*`、2 个围栏测试，全部 `--- PASS`，没有 `FAIL`。

## Done criteria

全部满足：

- [ ] `CGO_ENABLED=1 go build ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/run/ ./pkg/turn/ -count=1` 全部 ok
- [ ] Test plan 的 10 个新测试全部 PASS；4 个既有投影测试与 2 个围栏测试未改一行仍 PASS；`_MultipleContent` 只改了期望与注释
- [ ] `go vet ./pkg/llm/ ./pkg/llm/openai/` exit 0；`gofmt -l pkg/llm` 无输出
- [ ] `grep -n "} else if items := responseReasoningInputItemsFromMessage" pkg/llm/openai/responses_llm.go` 无输出
- [ ] `grep -c 'thinking\\n' pkg/llm/openai/responses_llm.go` → `0`
- [ ] `grep -c 'extractTextFromOutputItem(item); content != ""' pkg/llm/openai/responses_llm.go` → `0`
- [ ] 「复现 · 问题一」探针在修复后输出 8 项（Step 3 的 Verify）
- [ ] `git status --porcelain` 只列出 Scope 里的文件（plan 275 若同时在做，另加它自己的文件）
- [ ] Step 7 真机判据全过，或（无可用 key 时）手动验收清单已附在报告里
- [ ] `plans/README.md` 本计划那一行已更新

## STOP conditions

停下报告，不要自己发挥：

- 「Current state」① 的 `else if` 链或 ⑤ 的三处「只取第一个」已经不在，或 `responseReasoningInputItemsFromMessage` 的签名不是 `(msg llm.Message, model string)`。
- 「复现 · 问题一」探针在**修复前**的输出与本计划记录的 5 项不同（说明代码已漂移，计划的前提要重核）。
- `pkg/llm` 里已经存在 `ThinkingFence` / `IsThinkingFence`（说明 plan 277 或别人先建了；对照语义一致就复用、不一致就停）。
- `TestResponsesPlainHistoryProjectionIsUnchanged` 无法在不改投影规则的前提下通过（说明有老会话的请求字节会变）。
- 回归面出现 Step 5 允许范围之外的失败。
- 真机验收出现 400，且错误指向回放的 reasoning item（`not found`、`required following item`、`encrypted_content` 无效等）——记下原文报告，
  **不要**通过去掉 `id`、改顺序或删 reasoning 项来「让它过」：那会改变回放语义，需要 owner 决定。
- 修复需要动 Scope 之外的文件。

## 非功能要求（风险 / 对策 / 验证）

| 维度 | 风险 | 对策 | 验证 |
|---|---|---|---|
| 正确性 | 历史投影仍漏项或乱序 | 固定「思考 → 正文 → 工具调用」 | `TestResponsesHistoryKeepsEveryAssistantItem`、真机第 2 轮数字题 |
| 正确性 | 流式与非流式拿到的正文不同 | 非流式与流式兜底都用 `responseOutputText`，拼法与流式增量相同 | `TestResponsesStreamingAndSyncReadTheSameText` |
| 鲁棒性 | 端点不接受新组合（reasoning 后跟 message / function_call） | 这正是端点自己产出的顺序；真机验收专门看 400 | Step 7 |
| 隐私 / 跨模型 | 换模型后旧思考以围栏正文形式泄露 | 投影一律跳过围栏；reasoning items 仍只给同模型 | `TestResponsesReasoningOnlyMessageNeverSendsItsFence`、`TestResponsesHistoryToAnotherModelKeepsTextAndCalls` |
| 延迟 / 缓存 | 请求前缀变化导致缓存失效 | 不带 carry 的消息逐字节不变；变化只发生在出 bug 的消息上，升级后每个会话一次性未命中，之后重新稳定（规则只依赖消息自身与模型） | `TestResponsesPlainHistoryProjectionIsUnchanged` |
| 性能 / 成本 | 请求变大（多了正文与工具步骤的 reasoning items） | 这是修复本身：之前丢掉的就是模型应该看到的内容；压缩照常兜底 | 真机看用量 |
| 可维护性 | 围栏字面量散落四处 | 收进 `pkg/llm/thinking_fence.go` 一对函数；本计划收 Responses，plan 277 收其余 | Done criteria 的 grep |
| 体验 | 非流式回复的后半段之前在界面上不见了 | sink 回放用完整正文 | `TestResponsesSyncKeepsEveryTextSegment` |
| 兼容 | 旧会话 | 库结构不变、无需迁移；旧行没有 carry 的照旧投影 | 同上 |

## Maintenance notes

- **与 plan 275 / 277 的关系**：三份计划把各协议的历史回放统一成同一口径——同模型回放全部历史轮的思考，思考排在正文与工具调用之前，正文与工具调用永不丢，
  思考围栏只给产出它的模型（277）。以后改任一协议的投影，都要按这个口径对照另外几个。
- **多段正文之间没有分隔**：这是流式增量本来的拼法，本计划只让非流式与它一致。若以后要在 message 项之间加换行，流式也得同步改（流式增量已经推给界面了），另立计划。
- **一条消息里各项的相对顺序**：`llm.Message` 只分别记了正文、工具调用、思考，没记它们在响应里的原始交错顺序；本计划按 Responses 的规范顺序重建。
  若以后出现「正文在两个 function_call 之间」这类需要原样还原的情况，要在消息上记录顺序，那会动 `pkg/llm` 与落库，另立计划。
- **评审重点**：①Step 3 的三类「不变」是否真不变（看 `TestResponsesPlainHistoryProjectionIsUnchanged` 的期望是不是按旧规则手写的）；
  ②投影的正文拼法是否仍是 `TextContent` 的 `\n`，而提取的拼法是否与流式增量一致（无分隔）——两者刻意不同，别「统一」错；
  ③三处 `newResponseParams` 调用点都自动受益，没有被单独改动；④流式兜底仍只在 `textBuf.Len() == 0` 时触发。
