# Plan 250: 长度续写每轮只带一份累计回答，不再重复追加

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」就停下来报告，不要自己发挥。**不要 commit / push / merge**：改动留在工作区，
> 做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行状态改成 DONE 并附一句
> 执行摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git diff --stat 236fc32a..HEAD -- pkg/llm/openai/compat_llm.go pkg/llm/openai/compat_llm_test.go`
> → 必须为空。不为空就把「Current state」的摘录与实际代码逐段对照，对不上即 STOP。

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW-MED（改变第 3 轮起发给供应商的请求形状——变成正确的形状，但仍是行为变化）
- **Depends on**: none（plans/251 依赖本计划，两者改同一个文件，必须先做本计划）
- **Category**: bug
- **Planned at**: commit `236fc32a`, 2026-11-06

## Why this matters

OpenAI 兼容客户端在模型因 `finish_reason: "length"` 截断时会「长度续写」：把整段提示词重发一次，
后面带上已经生成的部分回答，让模型接着写，最多 16 轮。累计器 `reasoningAcc` / `contentAcc` 跨轮
**不清空**，可每一轮结束时代码又往请求里**追加**一条新的助手消息，内容是「到目前为止的全部输出」。
于是第 k 轮的请求里带着 k-1 条助手消息：第 1 轮的输出、第 1+2 轮的输出、第 1+2+3 轮的输出……
提示词按平方增长。费用跟着翻倍，也更容易撞上上下文窗口；模型看到的也是同一段话重复好几遍。
规划时实测（2 次 `length` 再 1 次 `stop`）抓到的第 3 次请求：

```
request 3: 3 messages
   [0] user content="hi" reasoning=""
   [1] assistant content="" reasoning="R1 "
   [2] assistant content="" reasoning="R1 R2 "
```

正确的形状是每轮 = 调用方原始消息 + **一条**累计助手消息。这段代码从初始导入（`4d4dea18`）起
就存在，不是本分支引入的，但 c7f8f5f4 正在修续写的用量统计，这个缺陷直接放大了它要解决的问题。

## Current state

`pkg/llm/openai/compat_llm.go`：

- `:25` `openAILengthContinueMax = 16`
- 流式路径 `executeChatCompletionStream`（:838 起）：
  ```go
  // :840
  baseMsgs := append([]openaiclient.ChatCompletionMessage(nil), req.Messages...)
  var reasoningAcc, contentAcc strings.Builder   // :841 — 跨轮累计，不清空
  ...
  for round := 0; round < openAILengthContinueMax; round++ {   // :866
  	roundIn, roundOut, roundCached := 0, 0, 0
  	req.Messages = baseMsgs                                    // :873
  	...
  	if !openAIReasoningLengthShouldContinue(lastFinish, reasoningAcc.Len(), contentAcc.Len(), len(toolBuf)) {
  		break
  	}
  	asst := openaiclient.ChatCompletionMessage{               // :1053
  		Role:             openaiclient.ChatMessageRoleAssistant,
  		ReasoningContent: reasoningAcc.String(),
  		Content:          contentAcc.String(),
  	}
  	if len(toolBuf) > 0 {
  		asst.ToolCalls = append([]openaiclient.ToolCall(nil), toolBuf...)
  	}
  	baseMsgs = append(append([]openaiclient.ChatCompletionMessage(nil), baseMsgs...), asst)  // :1061 ← 缺陷
  }
  ```
- 非流式路径 `executeChatCompletionUnaryParts`（:1267 起）：完全同构——`:1268` 定义 `baseMsgs`，
  `:1284` `req.Messages = baseMsgs`，`:1325-1333` 构造 `asst` 并在 `:1333` 追加到 `baseMsgs`。
- 现有唯一的续写测试：`pkg/llm/openai/compat_llm_test.go:521` `TestLengthContinueReportsSettledRequestPrompt`
  （2 轮，第 2 轮只有 1 条助手消息，不会暴露本缺陷）。测试构造客户端用同文件 `:901` 的
  `newOpenAICompatLLM(apiKey, baseURL, model, temperature, maxTokens, apiPath, paramsJSON)`（生产构造器
  `NewCompatLLMWithPromptCaching` 的薄包装）；`paramsJSON` 传 `[]byte(`{"stream":true}`)` 走流式，传
  `nil` 走非流式（`executeChatCompletionUnary`）。
- 约定：注释是完整英文句子、解释「为什么」，照 `compat_llm.go` 现有注释的口吻写。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 新测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/openai -run 'LengthContinue' -count=1 -v` | `ok`，列出新测试 PASS |
| 包测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... -count=1` | 全部 `ok` |
| vet | `PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" CGO_ENABLED=1 go vet -tags fts5 ./pkg/llm/...` | exit 0 |
| 格式 | `gofmt -l pkg/llm/openai` | 无输出 |

（`ld: warning: ... built for newer 'macOS' version` 是已知链接噪音，忽略。）

## Scope

**In scope**：
- `pkg/llm/openai/compat_llm.go`（只改上面两处 `baseMsgs` 的用法）
- `pkg/llm/openai/compat_llm_test.go`（新增测试）

**Out of scope**：
- 续写的用量统计（`settledIn`/`tokIn`/`lastAttemptUsage`/`finishUsage`）——那是 plans/251。
- `openAIReasoningLengthShouldContinue`、`openAILengthContinueMax`、`compatMessagesToOpenAI`：续写**是否**发生、
  助手消息怎么投影到线上，都不改。
- `pkg/llm/openai/responses_llm.go`（Responses 客户端没有这个循环）。

## Steps

### Step 1: 先写失败的测试

在 `compat_llm_test.go` 里 `TestLengthContinueReportsSettledRequestPrompt` 之后新增
`TestLengthContinueCarriesOneAnswerPerRound`，两个子测试 `stream` 和 `unary`，共用一个 httptest 服务器
工厂：

- 服务器把每次请求的 body 解码成
  `struct{ Messages []struct{ Role string `json:"role"`; ReasoningContent string `json:"reasoning_content"` } `json:"messages"` }`，
  记下（加锁）每次请求的消息列表。
- 第 n 次请求（n=1,2,3）：回 `reasoning_content` = `fmt.Sprintf("R%d ", n)`；n<3 时 `finish_reason` 为
  `"length"`，n=3 为 `"stop"`；`usage` 给 `prompt_tokens: 100*n, completion_tokens: 10`。
  - 流式回包照 `TestLengthContinueReportsSettledRequestPrompt`（:521）的 SSE 写法：一个 delta chunk、
    一个带 `finish_reason` 与 `usage` 的 chunk、`data: [DONE]`。
  - 非流式回包：`Content-Type: application/json`，
    `{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"R1 "},"finish_reason":"length"}],"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110}}`（按 n 替换）。
- 客户端：`newOpenAICompatLLM("sk-test", server.URL+"/v1", "m", 0, 16, "", params)`，`stream` 子测试
  `params = []byte(`{"stream":true}`)`，`unary` 子测试 `params = nil`；
  `Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)` 无错误。
- 断言（两个子测试相同）：
  - 一共 3 次请求；
  - 第 1 次请求 1 条消息；第 2、3 次请求都是 **2 条**消息（`user` + 一条 `assistant`）；
  - 第 2 次请求最后一条 `reasoning_content == "R1 "`；第 3 次请求最后一条 `reasoning_content == "R1 R2 "`；
  - `res.Billed().OutputTokens == 30`（三轮都被读到、都计费）。**不要**断言 `res.Usage.OutputTokens`：
    plans/252 可能会改它的语义。

**Verify**: 运行「新测试」命令 → 两个子测试都 **FAIL**，失败信息显示第 3 次请求有 3 条消息。
（如果这一步就 PASS，说明代码已被别人修过：STOP。）

### Step 2: 修流式路径

在 `:840` 之后加一个每轮用的消息列表，把 `:873` 和 `:1061` 改成用它：

```go
baseMsgs := append([]openaiclient.ChatCompletionMessage(nil), req.Messages...)
// roundMsgs is what one round sends: the caller's messages, plus — from the
// second round on — the one assistant message carrying everything the earlier
// rounds produced. reasoningAcc and contentAcc already hold every round so far,
// so each round replaces that message rather than appending another: an
// appended copy per round sent round 1's output twice by round 3 and grew the
// prompt with the square of the rounds.
roundMsgs := baseMsgs
...
	req.Messages = roundMsgs
...
	roundMsgs = append(append([]openaiclient.ChatCompletionMessage(nil), baseMsgs...), asst)
```

`baseMsgs` 之后不再被赋值（`grep -n 'baseMsgs =' pkg/llm/openai/compat_llm.go` 在流式函数内应无结果）。

**Verify**: 「新测试」命令 → `stream` 子测试 PASS，`unary` 仍 FAIL。

### Step 3: 修非流式路径

对 `executeChatCompletionUnaryParts` 做与 Step 2 完全相同的改动（`:1268` 后加 `roundMsgs`、`:1284`、`:1333`）。
注释可写一行 "See executeChatCompletionStream: each round replaces the carried answer."，不要复制整段。

**Verify**: 「新测试」命令 → 两个子测试 PASS；`TestLengthContinueReportsSettledRequestPrompt` 仍 PASS。

### Step 4: 全包与静态检查

**Verify**: 包测试全部 `ok`；vet exit 0；`gofmt -l pkg/llm/openai` 无输出；
`grep -n 'baseMsgs = ' pkg/llm/openai/compat_llm.go` → 无输出（只剩 `baseMsgs :=` 两处定义）。

## Test plan

- 新增 `TestLengthContinueCarriesOneAnswerPerRound`（流式 + 非流式），按 Step 1 断言请求形状。
- 回归：`./pkg/llm/...` 全部通过，尤其 `TestLengthContinueReportsSettledRequestPrompt`。

## Done criteria

- [ ] 新测试在修复前红（Step 1 观察到）、修复后绿
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... -count=1` 全部 `ok`
- [ ] vet exit 0；`gofmt -l pkg/llm/openai` 无输出
- [ ] `grep -n 'baseMsgs = ' pkg/llm/openai/compat_llm.go` 无输出
- [ ] `git status --short` 只列出 `pkg/llm/openai/compat_llm.go`、`pkg/llm/openai/compat_llm_test.go`（`plans/` 被 `.gitignore` 忽略，不会出现）
- [ ] `plans/README.md` 本计划行状态更新

## STOP conditions

- Drift check 不为空且摘录对不上。
- Step 1 的新测试在修复前就通过。
- 发现有代码或测试依赖「每轮一条新助手消息」的形状（例如某个供应商适配要求多条助手消息）——停下报告，
  不要为了让它过而改测试。
- 修复需要改 Scope 以外的文件。

## Maintenance notes

- plans/251 紧接着改同一个函数里的用量变量（`settledIn`/`lastAttemptUsage` 等）；本计划只动消息列表，
  两者不重叠，但 251 的摘录行号以本计划落地后的文件为准。
- 续写后第 k 轮的提示词 = 原始提示词 + 前 k-1 轮输出（线性），这是 plans/251 计算「被拒请求大小」的前提。
- 评审重点：两条路径都改了；`baseMsgs` 只在定义处赋值；没有动 `asst` 的构造。
