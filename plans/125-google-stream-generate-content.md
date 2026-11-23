# Plan 125：Google 适配器改为流式输出——走 `streamGenerateContent?alt=sse`，边收边推正文，补齐流式块信号

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在 dev 工作区。
> 工作区里若有与本计划无关的未提交改动，**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）**：`plans/122` 已完成：`grep -n "OnToolCallReady func" pkg/llm/stream.go`、`grep -n "OnMessageStop func" pkg/llm/stream.go`、
> `grep -n "func WithStreamProgress" pkg/llm/stream_progress.go` 各有结果。
>
> **Drift check**：按下面摘录里的符号定位；锚点找不到或内容不符即 STOP 1。

## Status

- **Priority**: P1（`123`、`124` 的行为要覆盖所有 provider；Google 不流式，就既看不到流式输出，也无法边流边执行工具）
- **Effort**: M
- **Risk**: MED（换了请求端点与响应解析；OpenCode 网关的 `/google` 入口是否支持 SSE 需真机/假服务验证）
- **Depends on**: plans/122
- **Category**: direction（owner 2026-10-25：Google 适配器必须改成流式输出）
- **Planned at**: dev `4d7a3c9` + 122，2026-10-25

## Why this matters

`pkg/llm/google` 是唯一不流式的适配器：它发一次 `generateContent`，等整段回答回来才返回，用户在等待期间什么也看不到，流式块信号（`OnToolCallReady` 等）也一个都不发，后续计划的“按进度收尾”和“边流边执行工具”在它上面全部失效。
这个适配器只在 OpenCode 的 Gemini 模型上使用（`pkg/run/config.go` 里 `case opencode.ProtocolGoogle:` 构造它；直接配置的 `google` provider 走的是 OpenAI 兼容端点，已经是流式）。

目标行为：
1. 每次请求都走 `POST {base}/v1beta/models/{model}:streamGenerateContent?alt=sse`，按 SSE 逐块解析。
2. 正文片段一到就 `OnDelta`；函数调用（Gemini 每个 `functionCall` part 在一个块里完整到达）一到就 `OnToolCallReady`；正文块在其后出现函数调用时 `OnTextBlockDone`；带 `finishReason` 的块到达时 `OnStopReason(finishReason)` 与 `OnMessageStop()`。
3. 用量按块里的累计值折算成增量 `OnUsage`，并 `OnUsageSnapshot`。
4. 流在输出之后、没有 `finishReason` 就结束 → `llm.ErrStreamTruncated`（与 compat 一致，交给网络重试层）。流里出现 `error` 对象 → 与非流式相同的 `newAPIError`。
5. 最终 `Result` 与现在的非流式解析等价：正文块（按函数调用分段）+ 函数调用（铸造的 ID 与 ready 时发出的完全相同）+ 用量。
6. 原来的非流式实现保留为 `executeUnary`（`plans/126` 会在“改用非流式重发”时调用它），本计划里生产路径不再调用它。

## Current state

- `pkg/llm/google/agent_llm.go`：
  - `func (c *agentLLM) Execute(ctx, messages, tools) (*llm.Result, error)`：`buildRequest` → `json.Marshal` → `endpoint := c.baseURL + "/v1beta/models/" + url.PathEscape(c.model) + ":generateContent"` →
    `req.Header.Set("Accept", "application/json")` → `c.client.Do(req)` → `io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))` → 非 200 时 `newAPIError(...)` → `parseResponse(resp.StatusCode, payload, resp.Header.Get("Retry-After"))`。
  - `type generateContentResponse struct { Candidates []struct { Content content; FinishReason string }; UsageMetadata struct { PromptTokenCount, CandidatesTokenCount, ThoughtsTokenCount int }; Error json.RawMessage }`。
  - `parseResponse`：`case p.FunctionCall != nil:`（`args` 由 `json.Marshal(p.FunctionCall.Args)` 得到，nil 时 `"{}"`，ID 用 `uuid.NewString()`）、`case p.Text != "":`（每个文本 part 追加一个 `llm.Text(p.Text)`）；
    空回答报 `google: response was empty (finish reason %q)`；用量 `OutputTokens: CandidatesTokenCount + ThoughtsTokenCount`。
- `pkg/llm/google/doc.go` 写明 “The client is deliberately non-streaming … a stream sink observes nothing from this provider.”
- 流式惯例（照着写）：`pkg/llm/openai/compat_llm.go` `executeChatCompletionStream`：`ctx = llm.WithStreamingRequest(ctx)`（让 `WrapHTTPTransport` 里的空闲看门狗生效）、`*sink.Streamed = true`、`llm.NotifyResponseStarted(sink, &responseStarted)`（在第一个产出事件转发前）、
  `OnUsage(deltaIn, deltaOut)` + `OnUsageSnapshot(tokIn, tokOut)`、无结束帧但有输出时 `return nil, fmt.Errorf("%s: %w", c.model, llm.ErrStreamTruncated)`、所有返回路径都调 `sink.OnEnd()`。
- 传输：OpenCode 路径 `transport := &openCodeAuthTransport{inner: auth}`，`auth.Base = llm.WrapHTTPTransport(nil)`（看门狗已装）；非 OpenCode 路径 `llm.NewHTTPClient()` 同样包了。
- `plans/122` 提供的 sink 回调：`OnToolCallReady(llm.ToolCall)`、`OnTextBlockDone()`、`OnStopReason(string)`、`OnMessageStop()`。
- 测试：`pkg/llm/google/agent_llm_test.go` 用 `httptest.NewServer` 回 JSON；断言 `r.URL.Path`、请求体投影、混合 part 解析、错误归一化、限流、空回答等。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Go 测试 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" FOREBRAIN_HOME="$(mktemp -d)"; CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/run/... -count=1` | 全部 `ok` |
| vet / fmt | 同上环境；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/llm/...`；`gofmt -l pkg/` | 无输出 |
| 真机 | `.claude/skills/run-forebrain/driver.sh`（Step 5） | 见期望 |

## Scope

**In scope**：`pkg/llm/google/agent_llm.go`、`pkg/llm/google/doc.go`、`pkg/llm/google/agent_llm_test.go`、新建 `pkg/llm/google/stream.go` 与 `pkg/llm/google/stream_test.go`、
`.claude/skills/run-forebrain/fake_provider.py`、`.claude/skills/run-forebrain/driver.sh`、`.claude/skills/run-forebrain/SKILL.md`（Step 5 的模式与开关）。

**Out of scope**：其他适配器；`pkg/run/config.go` 的协议路由；重试/收尾判定（`123`）；非流式重发的开关（`126`）；思考摘要（请求里不开 `includeThoughts`，保持现状）；`thoughtSignature`。

## Steps

### Step 1：保留非流式实现

把现在 `Execute` 里从 `endpoint := …` 到 `return parseResponse(...)` 的部分原样移进 `func (c *agentLLM) executeUnary(ctx context.Context, raw []byte) (*llm.Result, error)`（`raw` 是已编码的请求体），`Execute` 暂时仍调用它。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/google/ -count=1` → `ok`（纯重构，现有测试不变）

### Step 2：SSE 解析（`pkg/llm/google/stream.go`）

```go
// executeStreaming runs one streamGenerateContent call, forwarding text, calls,
// usage and the finish reason to the stream sink as they arrive.
func (c *agentLLM) executeStreaming(ctx context.Context, raw []byte) (*llm.Result, error)
```

1. `endpoint := c.baseURL + "/v1beta/models/" + url.PathEscape(c.model) + ":streamGenerateContent?alt=sse"`；`ctx = llm.WithStreamingRequest(ctx)` 后再 `http.NewRequestWithContext`；头 `Content-Type: application/json`、`Accept: text/event-stream`，鉴权头同 `executeUnary`。
2. `sink := llm.StreamSinkFrom(ctx)`；`if sink != nil && sink.OnEnd != nil { defer sink.OnEnd() }`。
3. 非 200：读最多 `maxResponseBytes+1` 字节后 `return nil, newAPIError(resp.StatusCode, payload, resp.Header.Get("Retry-After"))`。200：`if sink != nil && sink.Streamed != nil { *sink.Streamed = true }`。
4. 用 `bufio.Reader` 逐行读（不要用默认 64KB 上限的 `bufio.Scanner`；单行上限 `maxResponseBytes`，超出报 `google: stream event exceeds %d bytes`）。规则：以 `data:` 开头的行，去掉前缀与一个可选空格后作为一个事件的 JSON；空行、`:` 开头的注释行、`event:`/`id:` 行忽略；`data: [DONE]` 视为结束。读错误（含 `io.ErrUnexpectedEOF`、网络错误、看门狗的 `*llm.StreamStallError`）原样返回（`fmt.Errorf("google: read stream: %w", err)`，保持 `errors.Is/As` 可用）。正常 `io.EOF` 结束循环。
5. 每个事件 `json.Unmarshal` 成 `generateContentResponse`，失败 → `return nil, fmt.Errorf("google: decode stream event: %w", err)`；`Error` 非空且不是 `null` → `return nil, newAPIError(http.StatusOK, eventBytes, "")`。
6. 取 `Candidates[0]`（没有候选的事件只处理用量），按 part 顺序：
   - `p.FunctionCall != nil`：`llm.NotifyResponseStarted(sink, &started)`；若当前正文块非空 → `OnTextBlockDone()` 并把该块收进 `parts`、清空；`call := googleToolCallFromPart(p)`（见 7）追加到 `calls`，`OnToolCallReady(call)`。
   - `p.Text != ""`：`llm.NotifyResponseStarted(sink, &started)`；追加到当前正文块；`OnDelta(p.Text)`。
   - 其他 part 忽略（与非流式一致）。
7. 抽出 `func googleToolCallFromPart(p part) llm.ToolCall`：与 `parseResponse` 的 `case p.FunctionCall != nil:` 完全一致（同样的 `args` 规则，`ID: uuid.NewString()`），`parseResponse` 改为调用它。ready 发出的就是追加进 `calls` 的同一个值（ID 只铸造一次）。
8. 用量：`UsageMetadata` 是累计值。`in := PromptTokenCount`、`out := CandidatesTokenCount + ThoughtsTokenCount`；与上次已报的值比较，增量 > 0 时 `OnUsage(dIn, dOut)`；每次有用量都 `OnUsageSnapshot(in, out)`；最后一次的值写进 `Result.Usage`（三者全 0 时不写，同 `parseResponse`）。
9. 事件带 `FinishReason`（非空）：先处理完该事件的 part；当前正文块非空 → `OnTextBlockDone()` 并收进 `parts`；然后 `OnStopReason(finishReason)`、`OnMessageStop()`；记下 `finish`。之后的事件只处理用量。
10. 循环结束：
    - 当前正文块非空 → 收进 `parts`（不再发 `OnTextBlockDone`，除非第 9 条已发）。
    - `finish == ""` 且（`len(parts) > 0 || len(calls) > 0`）→ `return nil, fmt.Errorf("google: %w", llm.ErrStreamTruncated)`。
    - `len(parts) == 0 && len(calls) == 0` → `return nil, fmt.Errorf("google: response was empty (finish reason %q)", finish)`（与非流式同一句）。
    - 否则 `&llm.Result{Message: &llm.Message{Role: llm.RoleAssistant, Parts: <每个正文块一个 llm.Text>, ToolCalls: calls}, Usage: …}`。

**Verify**：`CGO_ENABLED=1 go build ./pkg/llm/google/` → exit 0

### Step 3：`Execute` 切到流式

`Execute` 在 `json.Marshal` 之后调用 `c.executeStreaming(ctx, raw)`。`executeUnary` 暂无生产调用者，保留并加注释（“used by the non-streaming resend path, plans/126”）。`doc.go` 改写那一段：客户端发 `streamGenerateContent?alt=sse` 并把正文、函数调用、结束原因按到达顺序推给 stream sink；`executeUnary` 是同一请求的非流式形式。

### Step 4：测试

1. `agent_llm_test.go`：加一个助手 `func serveSSE(w http.ResponseWriter, events ...string)`（`Content-Type: text/event-stream`，每个事件写 `"data: " + event + "\n\n"` 并 `Flush`）。所有通过 `Execute` 的现有测试改为：假服务器按路径分流——`strings.HasSuffix(r.URL.Path, ":streamGenerateContent")` 回 SSE（把原 JSON 回答作为**一个**事件，或按需拆成多个），断言里原来的 `:generateContent` 路径改为 `:streamGenerateContent`，并断言 `r.URL.Query().Get("alt") == "sse"`、`Accept` 头为 `text/event-stream`；其余断言（投影、错误归一化、限流、空回答、图片 URL 拒绝、角色校验）保持不变。
   另加 `TestExecuteUnaryParsesTheJSONBody`：直接调用 `executeUnary`，覆盖原来的 JSON 解析（可以搬一个原测试的 JSON）。
2. `stream_test.go`（新）：
   - `TestStreamForwardsTextCallsAndFinishInOrder`：事件依次为 `text "Hel"`、`text "lo"`、`functionCall weather{city:Paris}`、`text "after"` + `finishReason STOP` + 用量。断言 sink 收到的序列：`OnResponseStarted` 一次、`OnDelta("Hel")`、`OnDelta("lo")`、`OnTextBlockDone`、`OnToolCallReady(weather)`、`OnDelta("after")`、`OnTextBlockDone`、`OnStopReason("STOP")`、`OnMessageStop`；最终 `Result` 有两个正文 part（`Hello`、`after`）与一个调用，调用与 ready 的值逐字相同（含 ID）；`Usage` 为最后一次的累计值。
   - `TestStreamUsageIsReportedAsDeltas`：两次累计用量 → `OnUsage` 收到两次增量，和等于最终值。
   - `TestStreamWithoutFinishAfterOutputIsTruncated`：只有正文事件、无 `finishReason` → `errors.Is(err, llm.ErrStreamTruncated)`。
   - `TestStreamInBandErrorIsNormalized`：事件 `{"error":{"code":503,"status":"UNAVAILABLE","message":"overloaded"}}` → `*llm.APIError`，`Type == "UNAVAILABLE"`、`Message == "overloaded"`。
   - `TestStreamMalformedEventIsReported`：事件 `{not json` → 错误，`errors.As(err, new(*json.SyntaxError))` 为真。
   - `TestStreamCutMidBodyKeepsTheReadError`：服务器写一个事件后 `Hijack` 关连接（照仓库里已有的 hijack 写法；没有就用 `http.ResponseController` 关），断言错误满足 `llm.IsRetryableNetworkError`。
   - `TestStreamEmptyAnswerKeepsTheEmptyError`：只有 `finishReason SAFETY`、无 part → 错误文本含 `response was empty (finish reason "SAFETY")`。
   - `TestStreamSetsStreamedAndEnds`：`Streamed` 被置 true、`OnEnd` 恰好一次（成功与失败各一例）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/google/ -count=1` → `ok`；`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/run/... -count=1` → `ok`

### Step 5：真机验收（TUI，run-forebrain，假 Gemini 服务）

1. `fake_provider.py` 加模式 `gemini`：`do_POST` 最前面，若 `MODE == "gemini"` 且路径含 `:streamGenerateContent`，按 SSE 回答：
   第 1 次轮请求（按现有 `_is_turn_request` 判定的轮请求计数）先发两个正文事件 `"checking "`、`"the weather"`，再发一个 `functionCall`（`shell`，`{"command": "echo gemini-tool", "description": "echo"}`），最后一个事件带 `finishReason: "STOP"` 与 `usageMetadata`；
   带工具结果的后续请求回答正文 `"gemini done"` + `finishReason: "STOP"`。非轮请求（压缩、记忆）回一个只有正文 `"ok"` 的 SSE。路径含 `:generateContent`（非流式）时回同内容的 JSON（`plans/126` 会用到）。每个请求在 stderr 打印 `REQUEST <path>`（沿用现有日志行）。
   注意：Gemini 请求体里没有 OpenAI 的 `tools` 数组，`_is_turn_request`/`_has_tool_result` 若依赖 OpenAI 字段，就为 `gemini` 模式各写一个按 `contents`/`tools[].functionDeclarations`/`functionResponse` 判断的版本。
2. `driver.sh`：`cmd_serve` 写配置时支持 `FAKE_PROVIDER_KIND=opencode-gemini`：provider 段写成
   `provider: opencode`、`model: gemini-4-argon-contributor`、`api_key: ${FAKE_LLM_KEY}`、`base_url: http://127.0.0.1:$PORT`（其余配置不变；默认仍是现在的 deepseek 段）。
   `SKILL.md` 模式表加 `gemini` 一行（说明需配合 `FAKE_PROVIDER_KIND=opencode-gemini`）。
3. 跑：

```bash
D=.claude/skills/run-forebrain/driver.sh
FAKE_PROVIDER_KIND=opencode-gemini $D start gemini
$D submit 'weather?'
$D wait 'gemini done' 40
$D screen          # 期望："checking the weather" 正文、一张 echo gemini-tool 工具卡（结果 gemini-tool）、最后 "gemini done"
$D provider-log    # 期望：REQUEST 行的路径都是 /google/v1beta/models/gemini-4-argon-contributor:streamGenerateContent?alt=sse
$D messages        # 期望：assistant 行（正文 + 1 个 shell 调用）、tool 结果行、最后 "gemini done"
$D stop
```

   另跑一次 `drip` 式观察：把第 1 个正文事件后 `sleep 2` 再发其余（只在 `FAKE_GEMINI_SLOW=1` 时），`$D screen` 在 1 秒时截一次，期望已看到 `checking`、工具卡尚未出现——证明正文是边收边显示的。

**Verify**：两段期望满足，`screen`/`provider-log` 输出附在报告里。

## Done criteria

- [ ] Step 4 的测试全部存在并通过；`pkg/llm/...`、`pkg/run/...` 测试 `ok`；`go vet`、`gofmt -l pkg/` 干净
- [ ] `grep -n "streamGenerateContent?alt=sse" pkg/llm/google/stream.go` 有结果；`grep -n "deliberately non-streaming" pkg/llm/google/doc.go` 无结果
- [ ] 真机 `gemini` 模式两段期望满足
- [ ] `git status --short` 只含 Scope 内文件

## STOP conditions

1. 前置不满足或锚点找不到。
2. 现有某个测试断言了非流式行为本身（例如断言 `Accept: application/json` 或某个只在一次性 JSON 回答里才有的字段）且无法用 SSE 等价表达——回报用例名。
3. 真机时 OpenCode 网关（不是假服务）对 `:streamGenerateContent` 返回 404/405——不要回退成非流式；回报（`plans/126`/`123` 的“流式端点 404 改用非流式”会兜住它，但需要 owner 知道）。本计划只用假服务验收，不要求连真网关。
4. 某一步验证连续两次修正后仍失败。

## Maintenance notes

- ready 的调用必须与最终 `Result` 逐字一致：两处都用 `googleToolCallFromPart` 的同一个返回值，不要在组装最终消息时重新铸造 ID。
- `executeUnary` 是非流式重发（`plans/126`）的入口，改请求投影时两条路径共用 `buildRequest`，不要分叉。
- Gemini 若将来支持函数参数分片（partial args），ready 必须改到该调用最后一片之后再发。
