# Plan 277: 思考围栏只发给产出它的模型——OpenAI 兼容协议记下产出者，四个适配器统一执行

> **Executor instructions**: 按步骤顺序执行，每步验证通过再做下一步。遇到「STOP conditions」就停下报告，
> 不要自己发挥。**不要 commit / push / merge**（仓库规矩：owner 手动提交）。做完后把 `plans/README.md`
> 里本计划那一行的状态改成 DONE 并附一句摘要——除非派你来的评审说索引由他维护。
>
> **本计划硬依赖 plan 275 与 plan 276**（执行序：275、276 任意先后 → 277）。开工前三条都要满足，任一条不满足就**停下**，先做依赖计划：
> - `grep -n 'func StructuredCarry' pkg/llm/retry.go` 有命中（275 Step 1）
> - `grep -n 'func agentAnthropicMessagesToAPI(messages \[\]llm.Message, replayTo string)' pkg/llm/anthropic/agent_llm.go` 有命中（275 Step 4）
> - `grep -n 'func ThinkingFence\|func IsThinkingFence' pkg/llm/thinking_fence.go` 两个都命中（276 Step 1）
>
> **Drift check**：`git status --porcelain -- pkg/llm/thinking_fence.go pkg/llm/thinking_fence_test.go pkg/llm/openai/compat_llm.go pkg/llm/openai/compat_llm_test.go pkg/llm/anthropic/agent_llm.go pkg/llm/anthropic/agent_llm_test.go pkg/llm/google/agent_llm.go pkg/llm/google/agent_llm_test.go pkg/turn/model.go`
> → 期望：前六个文件带 275/276 的改动（属预期）；`pkg/llm/google/agent_llm.go`、`pkg/turn/model.go` 无改动。
> 逐条对照「Current state」的摘录：**结构对得上就继续**，对不上才 STOP。行号以 `d9c45060` 为准，275/276 落地后会后移，按符号找。

## Status

- **Priority**: P1（换模型后，旧模型的思考会以正文形式发给新模型）
- **Effort**: M
- **Risk**: MED（四个适配器的请求投影都要改；产出者本身收到的字节与今天完全相同）
- **Depends on**: `plans/275-anthropic-thinking-reaches-its-own-model.md`、`plans/276-responses-history-keeps-every-assistant-item.md`
- **Category**: bug
- **Planned at**: commit `d9c45060`, 2026-11-08

## Why this matters

模型只思考、没有正文也没有工具调用时，OpenAI 兼容客户端与 Responses 客户端都会把思考包成一段
```` ```thinking\n…\n``` ```` 放进正文，好让界面显示出来（下称「围栏消息」）。这段围栏随消息落库，之后的每个请求都会带上它。

问题在于**谁能收到它**：
- OpenAI 兼容客户端生成围栏时**顺手清掉了 carry**（`compat_llm.go:1521` `msg.Name = ""`），于是这条消息不再记录是哪个模型产出的；
  之后不管换到哪个模型，它都作为一条普通的 assistant 正文发出去——旧模型的思考就这样当成「答案」交给了新模型。
- 即便消息记得产出者（Responses 产出的围栏消息带 carry），**Anthropic 与 Google 适配器也不看**：围栏就是一段文本，照发。

owner 2026-11-08 的问题（「换模型后之前的思考会不会进新模型的请求」）得到的回答里，这是唯一的例外；owner 要求本期必须修复。
plan 276 已让 Responses 适配器不再把围栏当正文发；本计划补齐其余三个适配器，并让 OpenAI 兼容客户端记下产出者。

## 已拍板的决定

- **owner 2026-11-08**：OpenAI 兼容协议的同类问题必须修复，加入本期计划。
- **规则（四个适配器一个口径，与 275/276 的「思考只给产出它的模型」一致）**：围栏消息只发给记录为其产出者的模型；
  别的模型、或者消息没有记录产出者，就**整条不发**。
- **产出者本身收到的字节与今天完全相同**：OpenAI 兼容协议下仍是 `content` = 围栏、不带 `reasoning_content`、不带 `name`
  （它只思考没回答，协议又不接受「只有 reasoning_content、正文为空」的 assistant 消息——`pkg/llm/output_limit.go:86-88` 的注释：
  `an assistant turn with no content is rejected by every protocol`）。
- **老数据**：本计划之前落库的 OpenAI 兼容围栏消息都没有产出者记录，按规则**对任何模型都不再发送**——无法证明产出者就不发，
  与 275「没记录产出者的 thinking 不回放」同一原则。这些会话的请求前缀在升级后变一次。
- **丢掉一条 assistant 消息会让两条 user 消息相邻**：这不是新形状——每个请求开头本来就是「项目说明 meta user 消息 + 环境上下文 meta user 消息 + 真正的用户消息」
  三条相邻（`pkg/run/transcript.go:19-37`），所有适配器、所有在用的 provider 都已经在接受它；围栏消息没有工具调用，丢掉它也不会让任何工具结果失配。
- **围栏的字面量收口**：`compat_llm.go:1520` 与 `pkg/turn/model.go:1217` 改用 276 新建的 `llm.ThinkingFence`（字节不变）。

## Current state

行号基于 `d9c45060`。

**① OpenAI 兼容客户端生成围栏并清掉 carry**（`pkg/llm/openai/compat_llm.go:1510-1526`，`compatMessageFromOpenAI` 结尾；三条结果路径 `:1091`、`:1140`、`:1268` 都经过它）：

```go
	// If the model returned only reasoning content — no real content, no native
	// tool calls, and no synthesizable tool calls — surface the reasoning as
	// visible content so the user sees the model's thinking instead of getting
	// an "empty assistant response" error. Clear the carry so this text is not
	// also re-sent as reasoning_content on the next API call. Wrap in a fenced
	// code block (language "thinking") so the CLI renderer preserves the
	// original whitespace and newlines instead of collapsing them as Markdown
	// inline text would.
	if len(msg.Parts) == 0 && len(msg.ToolCalls) == 0 && hasReasoning {
		msg.Parts = []llm.ContentPart{llm.Text("```thinking\n" + reasoning + "\n```")}
		msg.Name = ""
	}
	if strings.HasPrefix(msg.Name, openAIReasoningCarryPrefix) {
		msg.ReasoningModel = model
	}
	return msg
}
```

**② OpenAI 兼容客户端的投影**（`:1383-1404`；275 落地后 `:1401` 的判定已是 `!llm.StructuredCarry(m.Name) && llm.ReplaysReasoningTo(m, model)`）：

```go
func compatMessagesToOpenAI(messages []llm.Message, model string) []openaiclient.ChatCompletionMessage {
	out := make([]openaiclient.ChatCompletionMessage, 0, len(messages))
	for _, m := range messages {
		out = append(out, compatMessageToOpenAI(m, model))
	}
	return out
}

func compatMessageToOpenAI(m llm.Message, model string) openaiclient.ChatCompletionMessage {
	...
	if strings.HasPrefix(m.Name, openAIReasoningCarryPrefix) {
		...
		if <275 之后的判定> {
			msg.ReasoningContent = strings.TrimPrefix(m.Name, openAIReasoningCarryPrefix)
		}
		msg.Name = ""
	} else {
		msg.Name = m.Name
	}
```

`compatMessagesToOpenAI` 也用于续写的 `recovery.Resume(...)`（`:1063`、`:1349`），那些消息不含围栏，不受影响。

**③ Anthropic 适配器**（275 落地后）：`agentAnthropicMessagesToAPI(messages []llm.Message, replayTo string)` 的 `case llm.RoleAssistant:`
先放 `anthropicThinkingParams(m, replayTo)`，再放 text、tool_use；除了 thinking 什么都没有就 `continue`。围栏是一段 text，今天照发。

**④ Google 适配器**（`pkg/llm/google/agent_llm.go`）：`Execute` 在 `:101` 调 `buildRequest(conversation, tools, c.maxTokens)`；
`buildRequest(messages []llm.Message, tools []*llm.Tool, maxTokens int64)`（`:286`）的 assistant 分支（`:307-311`）：

```go
		case llm.RoleAssistant:
			parts := assistantParts(m, toolNames)
			if len(parts) > 0 {
				req.Contents = append(req.Contents, content{Role: roleModel, Parts: parts})
			}
```

`agentLLM` 结构体有 `model string` 字段（`:39`）。测试文件里没有直接调用 `buildRequest` 的地方。

**⑤ 围栏字面量还在**：`pkg/turn/model.go:1211-1218`（`isReasoningOnlyAssistantEcho`，两边先 `TrimSpace`）：

```go
	return outText == "```thinking\n"+reasoningText+"\n```"
```

**⑥ 276 留下的工具**（`pkg/llm/thinking_fence.go`）：`ThinkingFence(reasoning string) string`、`IsThinkingFence(text string) bool`。
Responses 适配器在 276 之后已经「围栏一律不当正文发、reasoning items 只给同模型」——满足本计划的规则，**不用再改**。

**⑦ 一条现有测试钉着旧行为**：`pkg/llm/openai/compat_llm_test.go:313` `TestOpenAICompatLLMSurfacesReasoningOnlyResponseAsContent` 结尾：

```go
	if strings.HasPrefix(res.Message.Name, openAIReasoningCarryPrefix) {
		t.Fatalf("expected reasoning carry cleared once promoted, got %q", res.Message.Name)
	}
```

（同一测试前面对围栏前后缀的断言保留。）

**⑧ 保留 carry 的连带效果（已核对，都与 Responses 今天的行为一致）**：
- `pkg/agent/agent.go:309-313` 用 `llm.ExtractSummary(msg.Name)` 取 `Result.Reasoning` → OpenAI 兼容的围栏回复从此也有 reasoning；
  `pkg/turn/model.go:1192-1205` `AssistantOutcomeText` 见到「正文恰为自己的思考围栏」会把正文判为回声置空——这条规则本来就是为 Responses 的围栏消息写的，
  测试 `pkg/turn/model_test.go:282` / `:299` 覆盖。流式界面不受影响；落库仍走会话序列（`pkg/turn/session.go:384-391`），不受影响。
- 记忆管线 `pkg/memory/pipeline.go:366-371` 剥掉所有 carry——不受影响。
- 估算 `pkg/llm/tokenizers.go` 把 `Name` 计入——围栏消息的思考会被计两次（正文一次、carry 一次），与 Responses 今天相同，可接受。

**⑨ 仓库约定**：注释写「为什么」、完整英文句子；`pkg/llm` 导出函数必须有 doc comment；测试范例：
`compat_llm_test.go:72` `TestCompatReasoningReplaysOnlyToItsOwnModel`（直接调 `compatMessageFromOpenAI` / `compatMessageToOpenAI` 的单元写法）。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 相关包 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/turn/ -count=1` | ok（`pkg/llm` 本身约 2.5 分钟） |
| 单测 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/<包>/ -count=1 -run '<名字>' -v` | 指定测试 PASS |
| 回归面 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/agent/ ./pkg/run/ ./pkg/tui/ ./pkg/gateway/ ./pkg/memory/ -count=1` | ok |
| 静态检查 | `go vet ./pkg/llm/... ./pkg/turn/` | exit 0 |
| 格式 | `gofmt -l pkg/llm pkg/turn` | 无输出 |

`FOREBRAIN_HOME` 必须显式指向 mktemp 目录（仓库硬规矩）。

## Scope

**In scope**：
- `pkg/llm/thinking_fence.go` + `pkg/llm/thinking_fence_test.go` —— 加两个函数与测试
- `pkg/llm/openai/compat_llm.go` + `pkg/llm/openai/compat_llm_test.go`
- `pkg/llm/anthropic/agent_llm.go` + `pkg/llm/anthropic/agent_llm_test.go`
- `pkg/llm/google/agent_llm.go` + `pkg/llm/google/agent_llm_test.go`
- `pkg/turn/model.go` —— **只改** `:1217` 那一行

**Out of scope**：
- `pkg/llm/openai/responses_llm.go` —— 276 已满足本规则。
- 是否生成围栏、围栏长什么样——界面要显示它，不动。
- `AssistantOutcomeText` 的回声规则、`pkg/agent`、`pkg/memory`、估算器——见 ⑧，行为与 Responses 一致，不改。
- 老会话数据迁移——不需要：库结构不变，老的围栏消息按规则不再发送即可。

## Steps

### Step 1: `pkg/llm/thinking_fence.go` 加两个判定

```go
// ThinkingFenceOnly reports an assistant message that is a thinking fence and
// nothing else: no tool calls, no non-text parts, and every text part — at
// least one — a fence. It is a reply in which the model only reasoned.
func ThinkingFenceOnly(m Message) bool

// ThinkingFenceGoesTo reports whether a ThinkingFenceOnly message may be sent
// to model. Only the model recorded as having produced its reasoning gets it:
// to any other model the fence would pass that reasoning off as an answer
// this conversation gave. A message that records no producer — every such
// message stored before producers were kept — goes to no model.
func ThinkingFenceGoesTo(m Message, model string) bool
```

实现要点：`ThinkingFenceOnly`——`m.Role == RoleAssistant`、`len(m.ToolCalls) == 0`、`len(m.Parts) > 0`、每个 part 都是 `ContentTypeText` 且 `IsThinkingFence(p.Text)`。
`ThinkingFenceGoesTo`——`producer := strings.TrimSpace(m.ReasoningModel)`；`producer != "" && strings.TrimSpace(model) != "" && strings.EqualFold(producer, strings.TrimSpace(model))`。
**不要**复用 `ReplaysReasoningTo`：它对空产出者放行（OpenAI 兼容旧数据的历史豁免），这里恰恰相反。

测试（`thinking_fence_test.go`）：
- `TestThinkingFenceOnly`：围栏单段 → true；围栏 + 工具调用 → false；围栏 + 另一段普通正文 → false；普通正文 → false；user 角色的围栏 → false；
  没有 part → false；围栏 + 图片 part → false。
- `TestThinkingFenceGoesTo`：产出者相同（大小写、首尾空格不同）→ true；不同 → false；产出者为空 → false；请求模型为空 → false。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ -count=1 -run 'TestThinkingFence' -v` → 全部 PASS。

### Step 2: OpenAI 兼容客户端记下产出者，投影按规则放行

`pkg/llm/openai/compat_llm.go`：

1. ① 那段改为：围栏用 `llm.ThinkingFence(reasoning)`；**删掉 `msg.Name = ""`**（carry 保留，随后那句 `if strings.HasPrefix(msg.Name, openAIReasoningCarryPrefix) { msg.ReasoningModel = model }` 会记下产出者）。
   注释里「Clear the carry so this text is not also re-sent as reasoning_content…」改成如实描述：carry 留着是为了记录哪个模型产出了这段思考；
   投影时只有它会收到这条消息，而且只收到围栏正文、不再重复带 `reasoning_content`（见 3）。
2. `compatMessagesToOpenAI` 的循环开头加：

```go
		// A reply that was only reasoning goes back to the model that produced
		// it, and to no other: the fence would hand that model's reasoning to
		// another one as an answer this conversation gave.
		if llm.ThinkingFenceOnly(m) && !llm.ThinkingFenceGoesTo(m, model) {
			continue
		}
```

3. `compatMessageToOpenAI` 里设 `msg.ReasoningContent` 的判定前面加 `!llm.ThinkingFenceOnly(m) &&`——围栏正文已经就是这段思考，再带 `reasoning_content` 就是重复；
   `msg.Name = ""` 那句不动。这样产出者收到的仍是：`role: assistant`、`content` = 围栏、无 `reasoning_content`、无 `name`——与今天逐字节相同。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/openai/ -count=1` → 只有
`TestOpenAICompatLLMSurfacesReasoningOnlyResponseAsContent` 因 ⑦ 那句断言失败（预期，Step 6 改）；其余全绿。

### Step 3: Anthropic 适配器按规则放行

`agentAnthropicMessagesToAPI` 的 `case llm.RoleAssistant:` 开头加：

```go
			if llm.ThinkingFenceOnly(m) && !llm.ThinkingFenceGoesTo(m, replayTo) {
				continue
			}
```

（注释同 Step 2 的意思，说明为什么。）`replayTo == ""`（275 的拒收兜底那次重发）时围栏消息也不发——兜底本来就是「这次不回放任何思考」，一致。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/anthropic/ -count=1` → ok。

### Step 4: Google 适配器按规则放行

1. `buildRequest(messages []llm.Message, tools []*llm.Tool, maxTokens int64)` 加第四个参数 `model string`；`Execute` 里 `:101` 传 `c.model`。
2. assistant 分支开头加同样的 `if llm.ThinkingFenceOnly(m) && !llm.ThinkingFenceGoesTo(m, model) { continue }` 与注释。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/google/ -count=1` → ok。

### Step 5: `pkg/turn/model.go` 的字面量收口

`:1217` `return outText == "```thinking\n"+reasoningText+"\n```"` 改成 `return outText == llm.ThinkingFence(reasoningText)`（`llm` 已在该文件 import；字节不变）。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/ -count=1 -run 'AssistantOutcomeText'` → ok；
`grep -rn 'thinking\\n' pkg/llm/openai/compat_llm.go pkg/turn/model.go` → 无输出。

### Step 6: 测试

见「Test plan」。

**Verify**: Test plan 末尾命令 → 全部 PASS。

### Step 7: 回归面

**Verify**: 「Commands」的回归面命令 → ok；`go vet ./pkg/llm/... ./pkg/turn/` → exit 0；`gofmt -l pkg/llm pkg/turn` → 无输出。
若有测试因为「OpenAI 兼容的纯思考回复现在带 carry / `Result.Reasoning` 非空 / 正文被判为回声」而失败：**仅当**失败恰是 ⑧ 所列连带效果时，
才可以更新期望（并在报告里逐条列出）；其他失败 → STOP。

### 真机验收：不做

本计划只改请求投影与一个字段的保留，行为完全由单测确定；而「模型只思考不回答」无法在真机上稳定触发。按仓库惯例只验有意义的项，不写手动清单。

## Test plan

- `pkg/llm/thinking_fence_test.go`：`TestThinkingFenceOnly`、`TestThinkingFenceGoesTo`（Step 1）。
- `pkg/llm/openai/compat_llm_test.go`：
  - 改 `TestOpenAICompatLLMSurfacesReasoningOnlyResponseAsContent` 结尾：断言 `res.Message.Name` **以** `openAIReasoningCarryPrefix` 开头、
    `res.Message.ReasoningModel` 等于该测试 client 的模型（用测试 helper 建 client 时的模型名；不确定就读 `newTestOpenAICompatLLMWithParams` 的实现）。
  - `TestCompatThinkingFenceGoesOnlyToItsProducer`：`compatMessageFromOpenAI(ChatCompletionMessage{Role: assistant, ReasoningContent: "weigh A against B"}, "glm-5.3")` 得到围栏消息；
    `compatMessagesToOpenAI([user1, fence, user2], "glm-5.3")` → 3 条，中间那条 `Content == llm.ThinkingFence("weigh A against B")`、`ReasoningContent == ""`、`Name == ""`；
    同一历史给 `"deepseek-v4-flash"` → 2 条、没有 assistant。
  - `TestCompatProducerFenceBytesUnchanged`：上面产出者那条的 JSON 与**手写的**今天的形状 `{"role":"assistant","content":"```thinking\n…\n```"}` 逐字节相等（`json.Marshal` 后比对）。
  - `TestCompatDropsUnattributedAndForeignFences`：①老数据形状——围栏消息 `Name == ""`、`ReasoningModel == ""` → 对任何模型都不发；
    ②Responses 产出的围栏消息（`Name = llm.Encode([]llm.Item{{ID: "rs_1", Summary: []string{"x"}}})`、`ReasoningModel = "gpt-5.4-mini"`）→ 给 `"glm-5.3"` 不发。
- `pkg/llm/anthropic/agent_llm_test.go`：`TestAnthropicSendsThinkingFenceOnlyToItsProducer`——围栏消息 `ReasoningModel` 等于 client 模型时，
  `agentAnthropicMessagesToAPI(…, 同模型)` 里有这条 assistant（一个 text 块，内容为围栏）；`ReasoningModel` 为别的模型、为空、或 `replayTo == ""` 时，这条 assistant 不在请求里。
- `pkg/llm/google/agent_llm_test.go`：`TestGoogleSendsThinkingFenceOnlyToItsProducer`——直接调 `buildRequest(msgs, nil, 0, model)`，同上三种情况断言 `req.Contents` 里有 / 没有那条 `roleModel` 内容。
- 既有：`pkg/turn/model_test.go` 的 `TestAssistantOutcomeTextDropsAReasoningOnlyEcho`、`TestAssistantOutcomeTextKeepsARealAnswer` 原样通过。

```
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ ./pkg/llm/openai/ ./pkg/llm/anthropic/ ./pkg/llm/google/ ./pkg/turn/ -count=1 -v \
  -run 'TestThinkingFenceOnly|TestThinkingFenceGoesTo|SurfacesReasoningOnlyResponseAsContent|TestCompatThinkingFenceGoesOnly|TestCompatProducerFenceBytes|TestCompatDropsUnattributed|TestAnthropicSendsThinkingFence|TestGoogleSendsThinkingFence|TestAssistantOutcomeText' \
  2>&1 | grep -E '^(--- |ok|FAIL)'
```

→ 7 个新测试 + 改过的 1 个 + 既有的 2 个 `TestAssistantOutcomeText*` 全部 `--- PASS`，没有 `FAIL`。

## Done criteria

全部满足：

- [ ] `CGO_ENABLED=1 go build ./...` exit 0
- [ ] 「Commands」的相关包与回归面命令全部 ok
- [ ] Test plan 的测试全部 PASS
- [ ] `go vet ./pkg/llm/... ./pkg/turn/` exit 0；`gofmt -l pkg/llm pkg/turn` 无输出
- [ ] `grep -rn 'thinking\\n' pkg/llm/openai pkg/llm/anthropic pkg/llm/google pkg/turn --include='*.go' | grep -v _test.go` 无输出（围栏字面量只在 `pkg/llm/thinking_fence.go`）
- [ ] `grep -c 'ThinkingFenceGoesTo' pkg/llm/openai/compat_llm.go pkg/llm/anthropic/agent_llm.go pkg/llm/google/agent_llm.go` 每个文件 ≥ 1
- [ ] `grep -n 'msg.Name = ""' pkg/llm/openai/compat_llm.go` 不再出现在 `compatMessageFromOpenAI` 里
- [ ] `git status --porcelain` 只多出 Scope 里的文件（275/276 的文件属预期）
- [ ] `plans/README.md` 本计划那一行已更新

## STOP conditions

- 开头三条依赖检查任一不满足。
- 「Current state」① 的 `msg.Name = ""` 已经不在，或 ② 的投影已不是逐条 `compatMessageToOpenAI`。
- `pkg/llm/google/agent_llm.go` 的 `buildRequest` 被其他地方（测试以外）调用，签名改动会波及 Scope 外文件。
- 回归面出现 Step 7 允许范围之外的失败。
- 发现有 provider 或测试依赖「OpenAI 兼容的纯思考回复不带 carry」（例如逐字比较落库 parts JSON 且期望里没有 `reasoning_carry`）而又不属于 ⑧——停下报告。
- 修复需要动 Scope 之外的文件。

## 非功能要求（风险 / 对策 / 验证）

| 维度 | 风险 | 对策 | 验证 |
|---|---|---|---|
| 正确性 / 隐私 | 旧模型的思考以正文形式进入新模型的上下文 | 四个适配器统一「围栏只给产出者」 | 四个适配器各自的测试 |
| 兼容 | 产出者收到的内容变了 | 产出者的投影逐字节不变 | `TestCompatProducerFenceBytesUnchanged` |
| 鲁棒性 | 丢掉 assistant 后两条 user 相邻被 provider 拒 | 每个请求开头本就有相邻 user 消息（`pkg/run/transcript.go:19-37`），不是新形状；围栏消息无工具调用，不会让工具结果失配 | 回归面 |
| 延迟 / 缓存 | 老会话里没有产出者记录的围栏消息不再发送，前缀变一次 | 一次性；规则只依赖消息自身与模型，之后稳定 | `TestCompatDropsUnattributedAndForeignFences` |
| 性能 | 每条消息多两次廉价判定 | 只看 role、工具调用数与 part 前后缀 | — |
| 可维护性 | 围栏字面量散落 | 全部收口到 `pkg/llm/thinking_fence.go` | Done criteria 的 grep |
| 体验 | 非流式界面上纯思考回复改为按「思考」显示而非「正文」 | 与 Responses 今天的显示一致（⑧） | 回归面 |

## Maintenance notes

- **四个适配器的口径**（275/276/277 合起来）：同模型回放全部历史轮的思考；思考排在正文与工具调用之前；正文与工具调用永不丢；
  只思考没回答的那条消息（围栏消息）只给产出它的模型。新增适配器或改投影时，按这四条对照。
- **产出者只比模型 ID、不比 provider**——与 `ReplaysReasoningTo` 同一个已知局限，见 plan 275 的 Maintenance notes。
- **评审重点**：①`ThinkingFenceGoesTo` 对空产出者返回 false（别误用 `ReplaysReasoningTo`）；②OpenAI 兼容的围栏消息给产出者时没有多出 `reasoning_content`；
  ③Google 的 `buildRequest` 签名改动只有 `Execute` 一个调用点。
