# QODERCN DeepSeek-Flash 工具轮 400 与流式错误被吞 — 修复计划

状态：**已实施并真机验收通过（2026-11-14）** —— 见文末《Implementation Status》
Planned at：当前工作区（无 commit）。基线 = 本文件记录的 file:line 内容；`git status --porcelain` 在执行时**可能非空**（并行会话 `forebrain-harness-81` / `forebrain-harness-e9` 正在同一棵树改 `pkg/`），不作为本计划的判据，见 §6 判据 4。
落地位置：本计划批准后把同一内容拷到 `docs/plan/QODERCN_TOOL_ROUND_AND_STREAM_ERROR_PLAN.md`（本仓惯例，`docs/plan/` 已 gitignore）

> 真机（真模型）验收的授权来自 owner 本次会话原话：**"可以用qocoder cn的deepseek-flash模型做真机验证"**（2026-11-13，本会话）。这是对 2026-11-07「agent 验收一律 fake」裁决的**本单豁免**，不推广到其它任务；未获同类授权的执行者按 §5 的 fallback（5b′ 手动验收清单）收口。

---

## 1. 为什么修（用户可见症状）

TUI 截图（owner 报告，会话 `cli-a68b7865-8482-4846-8ffb-697f2d8d914f`，19:42）：

```
system: Streaming response ended before any complete data was received. Retrying without streaming.
        If this keeps happening, check any proxy or gateway between forebrain and your model provider.
error:  invalid character 'd' looking for beginning of value
footer: full access · qodercn/DeepSeek-Flash · max
```

用户只是让模型查代码（需要工具），第一次尝试模型正常发了工具调用、工具也执行了；
**工具结果回填后的第二次请求被上游拒绝**，而宿主把这次拒绝一路变形，最终吐出一条与真实原因毫无关系的 JSON 语法错误。
本期要修的是这条链的每一环：真实 400 不再被吞、不再回落成非流式垃圾、且工具轮请求本身能被上游接受。

---

## 2. 复现（改代码之前先看它失败）

### 2.1 现场证据（真实用户会话，只读）

| 证据 | 位置 |
|---|---|
| turn 失败、错误链尾部是 `*json.SyntaxError` | `~/.forebrain/logs/2026-11-13/tui-doudoudeMacBook-Air.local-28444-d8585a.jsonl` 第 33 行 `turn started`（19:42:54.194）→ 第 34/41/43 行三次 `llm request`（x=`d8585a-000013/14/15`）→ 第 45 行 `turn failed`（19:42:58.224，`err_chain: [*llm.ExecuteError ×4, *json.SyntaxError]`）。行号按 1-based。 |
| 网关对第二次请求的真实回答 | `~/.forebrain/logs/2026-11-13/x/d8585a-000014.resp.gz`（HTTP 200 + `text/event-stream`，见下） |
| 第一次尝试成功（reasoning + 2 个 tool_calls + finish_reason=tool_calls + usage） | `~/.forebrain/logs/2026-11-13/x/d8585a-000013.resp.gz` |

注：**请求**体抓包 `d8585a-000014.req.gz` 被 telemetry 截断（正文中段有 `[179449 log bytes omitted]`，Encode=1 正文因此不可解码），所以"线缆上缺 `content` 字段"这个结论**不依赖该抓包**，而由三件事共同确立：SDK tag `content,omitempty` + `compat_llm.go:1707-1726` 的赋值条件 + Step 0 的离线复现（解码真实发出的 `RemoteChatAsk.Messages` 看这条 assistant 消息）。评审已独立复现同一事实（其 scratch 测试原始输出：`TestZZReviewToolRoundWire FAIL: assistant tool-call message carries no content field on the wire`）。

`d8585a-000014.resp.gz` 的第一帧（原文，已解开信封）：

```
HTTP/2.0 200 OK
Content-Type: text/event-stream;charset=UTF-8

data:{"headers":{"Content-Type":["application/json"]},"body":"{\"code\":\"provider_error\",\"message\":\"Error in upstream response\",\"request_id\":\"718be7b7-…\",\"type\":\"provider_error\",\"details\":\"{\\\"error\\\":{\\\"message\\\":\\\"Messages with role 'tool' must be a response to a preceding message with 'tool_calls' (request_id: ac2e305a-…)\\\",\\\"type\\\":\\\"invalid_request_error\\\",\\\"param\\\":null,\\\"code\\\":\\\"invalid_request_error\\\"}}\"}","statusCodeValue":400,"statusCode":"BAD_REQUEST"}
```

即：**上游 400 被网关包成 HTTP 200 + SSE 信封**（后面还有 `event:error` 帧和一行 `{"success":false,…}` 纯 JSON，均非 chunk）。

对照：同一 TUI 进程另一个会话（`cli-e65fc0d4-…`，模型 `qmodel_latest`）对 qoder 网关发了 153 次请求、138 次 ok、跑了 187 个工具调用（日志 `~/.forebrain/logs/2026-11-13/tui-192.168.2.162-59352-b38c7d.jsonl`）。
→ **工具轮协议整体可用，问题只出在 `dfmodel`（DeepSeek-Flash）这条上游路径上**。

### 2.2 真机探针（owner 已批准：可以用 qodercn 的 deepseek-flash 做真机验证）

用 `go test -overlay` 把探针挂进插件包（零仓库改动），走真实插件 `Transport.send` + 真实登录 + 真实网关，只换对话内容：

```bash
cd /Users/doudou/workspace/unionj-cloud/forebrain-harness
CGO_ENABLED=1 go test -tags fts5 -overlay=/tmp/zz_overlay3.json \
  ./plugins/subscriptions/qodercn -run 'TestZZLiveProbe$' -v -count=1
```

探针文件（执行者需重建，内容如下，写入 `/tmp/zz_live_probe_test.go`；overlay 见 2.4）：

```go
package qodercn

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

// summarize 只留信封 body 里的要点：错误帧、可见文本、工具调用、finish_reason。
func summarize(body string) string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var env struct {
			StatusCode      string          `json:"statusCode"`
			StatusCodeValue int             `json:"statusCodeValue"`
			Body            json.RawMessage `json:"body"`
		}
		if err := json.Unmarshal([]byte(payload), &env); err != nil {
			out = append(out, "RAWFRAME "+payload)
			continue
		}
		inner := string(env.Body)
		var s string
		if json.Unmarshal(env.Body, &s) == nil {
			inner = s
		}
		trimmed := strings.TrimSpace(inner)
		switch {
		case trimmed == "" || trimmed == "[DONE]":
			continue
		case strings.HasPrefix(trimmed, "{"):
			var chunk map[string]any
			if json.Unmarshal([]byte(trimmed), &chunk) == nil {
				if code, ok := chunk["code"].(string); ok {
					out = append(out, "ERRORFRAME code="+code+" rest="+short(trimmed, 700))
					continue
				}
				if choices, ok := chunk["choices"].([]any); ok {
					for _, c := range choices {
						m, _ := c.(map[string]any)
						if d, ok := m["delta"].(map[string]any); ok {
							if txt, ok := d["content"].(string); ok && txt != "" {
								out = append(out, "TEXT "+short(txt, 300))
							}
							if tc, ok := d["tool_calls"]; ok {
								b, _ := json.Marshal(tc)
								out = append(out, "TOOLCALL "+short(string(b), 300))
							}
						}
						if fr, ok := m["finish_reason"].(string); ok && fr != "" {
							out = append(out, "FINISH "+fr)
						}
					}
				}
				continue
			}
			out = append(out, "BODY "+short(trimmed, 700))
		default:
			out = append(out, "PLAIN "+short(trimmed, 300))
		}
	}
	return strings.Join(out, "\n    ")
}

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func TestZZLiveProbe(t *testing.T) {
	root, err := home.Root()
	if err != nil {
		t.Fatal(err)
	}
	tr := &Transport{Home: root}
	tools := `[{"type":"function","function":{"name":"get_weather","description":"Get the weather of a city","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]`
	call := `{"id":"call_00_jFzXjynoCDwNfjaogr9r0880","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}`
	call2 := `{"id":"call_01_MzeiBd2ofdipDwQiWCXY8041","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Rome\"}"}}`
	sys := `{"role":"system","content":"You are a helpful assistant. Answer in one short sentence."}`
	usr := `{"role":"user","content":"What is the weather in Paris? Use the get_weather tool."}`
	toolMsg := `{"role":"tool","tool_call_id":"call_00_jFzXjynoCDwNfjaogr9r0880","content":"sunny, 21C"}`
	toolMsg2 := `{"role":"tool","tool_call_id":"call_01_MzeiBd2ofdipDwQiWCXY8041","content":"rainy, 12C"}`

	cases := []struct {
		name     string
		messages string
		tools    bool
	}{
		{"plain-no-tools", "[" + sys + "," + usr + "]", false},
		{"tool-round-no-content", "[" + sys + "," + usr + `,{"role":"assistant","tool_calls":[` + call + `]},` + toolMsg + "]", true},
		{"tool-round-with-content", "[" + sys + "," + usr + `,{"role":"assistant","content":"Let me check.","tool_calls":[` + call + `]},` + toolMsg + "]", true},
		{"tool-round-parallel", "[" + sys + "," + usr + `,{"role":"assistant","tool_calls":[` + call + "," + call2 + `]},` + toolMsg + "," + toolMsg2 + "]", true},
		{"orphan-tool-control", "[" + sys + "," + usr + `,{"role":"assistant","content":"Let me check."},` + toolMsg + "]", true},
	}
	for _, c := range cases {
		chat := &openAIChatRequest{Model: "dfmodel", Messages: []byte(c.messages)}
		if c.tools {
			chat.Tools = []byte(tools)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		resp, err := tr.send(ctx, chat, "dfmodel")
		if err != nil {
			cancel()
			t.Logf("CASE %-26s -> send error: %v", c.name, err)
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 200000))
		resp.Body.Close()
		cancel()
		t.Logf("CASE %-26s -> status %d ctype=%q err=%v\n    %s", c.name, resp.StatusCode, resp.Header.Get("Content-Type"), readErr, summarize(string(body)))
	}
}
```

结果（2026-11-13 实测，本次会话；评审未独立复核这一段——标注 unverified，但它与 §2.1 现场 400 及 Step 0 离线复现互洽）：

| 变体 | assistant 消息 | 结果 |
|---|---|---|
| `plain-no-tools` | 无工具 | ✅ 正常回答 |
| `tool-round-no-content` | 只有 `tool_calls`，**没有 `content` 字段** | ❌ `ERRORFRAME code=provider_error … Messages with role 'tool' must be a response to a preceding message with 'tool_calls'` |
| `tool-round-with-content` | `"content":"Let me check."` + `tool_calls` | ✅ "The weather in Paris is currently sunny at 21°C." |
| `tool-round-parallel` | 2 个 `tool_calls`，无 `content` | ❌ 同上 400 |
| `orphan-tool-control` | 无 `tool_calls`（对照，故意制造孤儿） | ❌ 同上 400 |
| `"content":""` + `tool_calls` | 空字符串 | ✅ 正常回答 |
| `"content":null` + `tool_calls` | null | ❌ 同上 400 |
| `"content":" "` + `tool_calls` | 单空格 | ✅ 正常回答 |
| `stream:false` 探针（直接改 RemoteChatAsk 的 `stream` 字段） | 网关仍回 `text/event-stream` | 网关**恒流式**，不认 `stream:false` |

### 2.3 因果链（三环，均有 file:line + 现场证据）

1. **根因 — 工具轮的 assistant 消息缺 `content` 字段**
   宿主 compat 客户端只在"单一 text part"时设 `Content`（`pkg/llm/openai/compat_llm.go:1707-1726`），纯工具调用的回复（`Parts` 为空）导致 `Content == ""`；
   go-openai v1.43.0 的 `ChatCompletionMessage` 是 `Content string \`json:"content,omitempty"\``（`~/go/pkg/mod/github.com/sashabaranov/go-openai@v1.43.0/chat.go`），于是线缆上这条 assistant 消息**完全没有 `content` 字段**。
   插件把 `messages` 原样放进 `RemoteChatAsk.Messages`（`plugins/subscriptions/qodercn/transport.go:359`），qoder 网关转发给上游（DeepSeek）时，无 `content`（或 `null`）的 assistant 消息不被认作 `tool_calls` 的归属 → 紧随其后的 `role:"tool"` 消息成孤儿 → 上游 400。
   现场：`d8585a-000014` 正是"第一次工具轮成功（`000013`）→ 回填两条 tool 结果 → 第二次请求被拒"。
2. **错误被吞** — `transport.go:450-469 unwrapSSEPayload` 只判断信封体是不是合法 JSON 对象；`statusCodeValue:400` 的错误体（`{"code":"provider_error",…}`）合法，于是被当成普通 chunk 以 `data: {…}` 发下去（`transport.go:425-429`）。
   宿主流里没有任何 delta → "无完整数据" → `pkg/llm/stream_retry.go` 走 `DecideRetryWithoutStreaming` 并打出 `pkg/llm/stream.go:133` 的 `StreamingFallbackWarning`（截图里的 system 行）。
3. **二次伤害（用户看到的最后一条错误）** — 回落后的非流式请求同样拿到 200+SSE（2.2 实测网关照旧流式），而 `transport.go:88-94` 只做信封解包、把 `data:` 开头的流交给"期望一个 JSON 文档"的客户端 → `json.Unmarshal` 直接报 `invalid character 'd' looking for beginning of value`。

### 2.4 overlay 文件（探针用，写 /tmp 即可，不落仓库）

`/tmp/zz_overlay3.json`：

```json
{
  "Replace": {
    "/Users/doudou/workspace/unionj-cloud/forebrain-harness/plugins/subscriptions/qodercn/zz_live_probe_test.go": "/tmp/zz_live_probe_test.go",
    "/Users/doudou/workspace/unionj-cloud/forebrain-harness/plugins/subscriptions/qodercn/zz_live_probe3_test.go": "/tmp/zz_live_probe3_test.go"
  }
}
```

（`zz_live_probe3_test.go` 是 `stream:false` 探针；只跑 `-run 'TestZZLiveProbe$'` 时可不建，overlay 里删掉对应行即可。）

---

## 3. 修复设计（三处，全部在闭源插件内）

改动文件只有两个：`plugins/subscriptions/qodercn/transport.go` 与 `plugins/subscriptions/qodercn/qodercn_test.go`。
插件目录是 owner 裁决的闭源插件（gitignore，宿主 `pkg/` 不得出现插件名字符串），所以修在插件层而不是宿主层。

### Fix 1（根因）：带 `tool_calls` 的 assistant 消息必须带字符串 `content`

在 `buildRemoteChatAsk`（`transport.go:258-365`）里，把要写进 `ask.Messages` 的数组过一道：

```go
// ensureToolCallContents 给只调用了工具的 assistant 回合补一个字符串
// content。上游（qoder 网关后面的 DeepSeek）不把没有 content 字段（或
// content 为 null）的 assistant 消息当作 tool_calls 的归属，紧随其后的
// role:"tool" 消息就成了孤儿，整轮被 400 拒绝：
//   Messages with role 'tool' must be a response to a preceding message with 'tool_calls'
// 宿主的线缆里恰好缺这个字段（go-openai 的 content 是 omitempty，纯工具
// 回合的 Content 为空）。空字符串能被上游接受（实测），null 不行。
func ensureToolCallContents(raw json.RawMessage) json.RawMessage
```

- 输入输出都是 `json.RawMessage`；解析成 `[]map[string]json.RawMessage`，逐条：
  - 有非空 `tool_calls` 且（无 `content` 键 或 `content == null`）→ 置 `content = ""`；
  - 其它消息的**字段集合与值完全不变**（`reasoning_content`、`name`、`tool_call_id`、图片 part 等全部原样保留）。注意：用 `map` 序列化会把每条消息的**键序**排成字典序，所以不是逐字节相同——语义等价、无害（这条请求的字节本来就因补 `content` 变化）。
- 解析失败 / 空数组 → 原样返回（不改写、不报错，保持今天的透传语义）。
- 输出用 `json.Marshal`（键按字典序，稳定、可预期），返回后赋给 `ask.Messages`；`wireMessage` 只读逻辑（system 提升、last user）不受影响。

**为什么不改宿主** `pkg/llm/openai/compat_llm.go`：那会改掉**所有** OpenAI 兼容 provider 的请求字节（前缀缓存分叉、跨 provider 风险），而当前只有这条上游路径拒收；插件本来就拥有这条协议的全部改写权。此判断作为"同一缺陷族"的观察项记在 §9。

### Fix 2：SSE 信封里的 provider 错误 → `*llm.APIError`

新增：

```go
// qoderEnvelopeFailure 把网关"HTTP 200 + 信封"里的上游失败翻成 APIError：
// 状态取信封的 statusCodeValue，消息优先取 details.error.message（上游原文），
// 其次 message，再次信封体原文。
func qoderEnvelopeFailure(payload string) error
```

- 触发条件：`statusCodeValue >= 400`（信封体是 JSON 对象且带 `code`/`error`）。
- 组装 `&llm.APIError{StatusCode: env.StatusCodeValue, Code: <obj.code 或 details.error.code>, Type: <details.error.type 或 obj.type>, Message: <优选消息>}`，外层 `fmt.Errorf("qodercn: %w", …)`（`errors.As` 仍可取到 APIError，渲染文本带插件前缀）。
- 在 `unwrapQoderSSEStream` 的扫描循环里、`unwrapSSEPayload` 之前调用；命中即 `pw.CloseWithError(err); return`（defer 关上游 body）。流消费者因此拿到错误而不是空流。
- 非流式路径（Fix 3）命中同一个错误时，从 `RoundTrip` 返回该错误（`http.Client` 会包成 `*url.Error`，`errors.As` 仍可取到）。
- 宿主侧语义（不改）：`pkg/llm/stream_failure.go:281-312 requestLevelDecision` 对 status 400 是 `pass=true`（原样上抛、**不再回落非流式**）；`ClassifyStreamFailure` 得到 `CauseBadRequest`。所以用户会直接看到上游原话，截图里那条 JSON 语法错误与回退警告都不再出现。

### Fix 3：客户端要非流式时，把网关的 SSE 答案组装成一个 `chat.completion`

网关恒流式（2.2 实测 `stream:false` 无效），而 `transport.go:88-94` 今天把 `data:` 流直接交给非流式客户端。修改 `RoundTrip`：

```go
if !isEventStream(resp) { return resp, nil }          // 现状：非 SSE 原样返回
if chat.Stream { resp.Body = unwrapQoderSSEStream(resp.Body); return resp, nil }
return assembleQoderCompletion(resp)                   // 非流式：读完整流，回一份 JSON
```

`assembleQoderCompletion(resp *http.Response) (*http.Response, error)`：

- **帧迭代语义与流式路径完全一致**（今天的这套逻辑在 `unwrapQoderSSEStream` 的扫描循环里，`transport.go:405-430`，不在 `unwrapSSEPayload` 里——后者对 `[DONE]` 只返回空串）。落地方式：把"逐行扫描 → 跳过非 `data:` 行 → 跳过空 payload → 心跳 `[NOT_EXCEED_QUOTA]`/`[EXCEED_QUOTA]…`/`[NOTIFICATIONS]…` 丢弃 → `[DONE]` 结束 → 否则 `unwrapSSEPayload` 取 chunk"这段抽成一个共用的小迭代器（例如 `scanQoderFrames(upstream io.Reader, onFrame func(chunk string) error) error`），流式与非流式两个消费者都用它，**不要复制两份判定**。
- 逐帧解析 chunk；遇到 Fix 2 的错误信封立即返回错误；`[DONE]` 结束。
- **上游 body 没有 `[DONE]` 就结束**：与流式路径今天的收尾一致（`transport.go:435-437` 在缺 `[DONE]` 时补发），组装侧在 body 自然结束时把已收到的帧收尾成一份响应；一帧有效 chunk 都没收到才报错。
- 合并：`content` 累加、`reasoning_content` 累加、`tool_calls` 按 `index` 合并（首个非空 `id`/`name` 生效，`arguments` 累加）；`finish_reason` 取最后一个非空值；`usage` 取最后一个携带它的 chunk 并**原样**保留（网关多出的 `billable/credits` 等字段 SDK 会忽略，标准字段名 `prompt_tokens/completion_tokens/total_tokens/…_details` 与 SDK 一致）。
- 输出形如标准非流式响应：`{"id","object":"chat.completion","created","model","choices":[{"index":0,"message":{"role":"assistant","content":…,"reasoning_content":…,"tool_calls":[…]},"finish_reason":…}],"usage":{…}}`，`Content-Type: application/json` + 正确的 `Content-Length`；上游 body 关闭。
- 一帧 chunk 都没解析到 → 返回 `fmt.Errorf("qodercn: the answer stream carried no chunk")`（运输层失败，交给宿主的既有重试语义）。

### 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 是否刻意 |
|---|---|---|---|
| `dfmodel` 工具轮（assistant 只有 `tool_calls`，无 `content`） | 上游 400 → 回退 → `invalid character 'd'` | 请求补 `"content":""`，上游正常回答 | ✅ 修复根因 |
| 任意模型的上游 provider_error（200+SSE 信封） | 当 chunk 透传 → 空流 → 回退警告 → JSON 语法错 | 立刻以 `*llm.APIError`(400) 上抛，界面显示上游原话（如 tool 消息孤儿），不触发回退 | ✅ 刻意修订 |
| 网关 200+SSE 且客户端要非流式（任何回落/重试路径） | `json.Unmarshal` 遇 `data:` → `invalid character 'd'` | 组装成单个 `chat.completion`（含 reasoning/tool_calls/usage） | ✅ 刻意修订 |
| 正常流式（文本/思考/工具调用/usage/`[DONE]`、心跳 `[NOT_EXCEED_QUOTA]` 丢弃） | — | 不变 | — |
| 401/403 强制刷新后重发 | — | 不变 | — |
| 非 SSE 的普通响应（多为 HTTP 错误体） | 原样交给客户端 | 不变（Fix 2 只处理信封错误，不改 HTTP 层） | — |

---

## 4. Scope

**In scope（只这两个文件）**
- `plugins/subscriptions/qodercn/transport.go`（Fix 1/2/3）
- `plugins/subscriptions/qodercn/qodercn_test.go`（三个新测试 + 复用既有 `withQoderHome`/`withGateway`/`testState`/`storedLogin`/`sseFrame` 装置）
- 批准后：本计划内容拷到 `docs/plan/QODERCN_TOOL_ROUND_AND_STREAM_ERROR_PLAN.md` 并在末尾补验收记录

**Out of scope（明确不碰）**
- `pkg/llm/**`（尤其 `openai/compat_llm.go` 的线缆形状、`stream_failure.go`、`stream.go` 的警告文案）
- `pkg/subscription/**`、`pkg/state/**`、`pkg/gateway/**`、`pkg/tui/**`（并行会话正在改这些）
- 其它 provider / 插件；`plugins/subscriptions/qodercn/{auth,models,crypto,identity,plugin}.go` 除非 Fix 需要常量（预期不动）
- 已存在的 `docs/plan/QODERCN_MODEL_DISPLAY_NAME_PLAN.md`
- 不改宿主里"compat 客户端省略 content"的通用行为（见 §9 观察项）

---

## 5. Steps（每步带验证与预期）

> 起始基线检查：`plugins/subscriptions/qodercn/transport.go` 应为 469 行、含 `func unwrapSSEPayload`（第 450 行）与 `Stream: true`（第 339 行），`qodercn_test.go` 应为 1448 行。内容对不上 = STOP（只允许行号漂移）。

**Step 0 — 复现（离线先行）**
新测试 `TestToolCallTurnCarriesAContentString`（假网关 httptest）：客户端 `Execute` 一条**不带 `Parts`（纯 `tool_calls`）**的 assistant 消息——`llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call_1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "get_weather", Arguments: "{\"city\":\"Paris\"}"}}}}`——再加一条 `llm.ToolResultMessage("call_1", llm.Text("sunny"))`；断言解码出的 `RemoteChatAsk.Messages` 里那条 assistant 对象含 `"content":""`。
**形态必须是 `Parts` 为 nil**：`pkg/llm/openai/compat_llm.go:1719-1726` 遇到单个 text part 会设 `Content`，那样这条消息本来就有 `content`，测试会假通过并触发下面的 STOP。
现在跑必定失败（消息里没有 `content` 键）——这就是本 bug 的离线复现。

```bash
CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/qodercn -run TestToolCallTurnCarriesAContentString -count=1
```
预期：FAIL，报 assistant 消息缺 `content`。

**Step 1 — Fix 1**（`ensureToolCallContents` + 在 `buildRemoteChatAsk` 里接线）
重跑 Step 0：PASS。再跑整包：
```bash
CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/qodercn -count=1
```
预期：ok（既有 1448 行测试全部仍绿，尤其 `TestTransportRewritesToRemoteChatAskAndUnwrapsSSE` 对 `Messages` 的断言）。

**Step 2 — Fix 2** + 新测试 `TestStreamSurfacesTheEnvelopeProviderError`
假网关：`Content-Type: text/event-stream`，第一帧就是 400 信封（用 `sseFrame` 的 `statusCodeValue:200` 同构写法造一个 `statusCodeValue:400`、`body` 为 provider_error 对象的错误帧），随后 `event:error` 帧与 `{"success":false,…}` 行。
断言：`client.Execute` 返回错误；`llm.HTTPStatusOf(err) == 400`；`err.Error()` 含 `Messages with role 'tool' must be a response to a preceding message with 'tool_calls'`；**不含** `invalid character`。
```bash
CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/qodercn -run TestStreamSurfacesTheEnvelopeProviderError -count=1
```
预期：PASS。

**Step 3 — Fix 3** + 新测试 `TestNonStreamingRequestAssemblesTheStreamedAnswer`
假网关：无论请求都回 SSE（复刻真实网关）：**一帧心跳 `data: [NOT_EXCEED_QUOTA]`** + reasoning 帧 + 文本两帧 + tool_calls 帧 + `finish_reason` 与 `usage` 帧 + `[DONE]`。
客户端用 `llm.WithNonStreamingRequest(ctx)` 执行，断言：`result.Message` 文本、`ToolCalls`、`result.Usage`（12/7 之类）与流式用例一致；假网关收到的 `RemoteChatAsk.Stream` 仍为 true（伪装不变）；心跳没有污染正文。
同一测试再加子用例 `no-DONE`：帧序列相同但**不发 `[DONE]`** 就关连接，断言组装结果与前一子用例等价（收尾语义与流式路径一致）。
验证命令：`CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/qodercn -run TestNonStreamingRequestAssemblesTheStreamedAnswer -count=1`
预期：PASS（两个子用例）。

**Step 4 — 包级回归 + gofmt + vet**
```bash
CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/qodercn -count=1
gofmt -l plugins/subscriptions/qodercn
CGO_ENABLED=1 go vet -tags fts5 ./plugins/subscriptions/qodercn
```
预期：ok；gofmt 无输出；vet 无输出。
（宿主包不在改动范围，不做全量 `go test ./...`。）

**Step 5 — 真机复验（真模型，owner 已批准）**
5a 真机探针（真实网关 + 真实 `dfmodel`，走宿主完整路径）：`/tmp/zz_live_tool_round_test.go` + overlay（与 §2.4 同法）：

```go
package qodercn

import (
	"context"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
)

func TestZZLiveToolRound(t *testing.T) {
	root, err := home.Root()
	if err != nil {
		t.Fatal(err)
	}
	// 与 plugin.go 的 newQoderCNLLM 完全同构：client 的 transport 就是插件。
	client := openai.NewCompatLLMWithTransport("", "https://gateway.qoder.com.cn", "dfmodel", 0, 32000, "", nil, &Transport{Home: root})
	tool, err := llm.NewTool("get_weather", "Get the weather of a city",
		func(context.Context, struct {
			City string `json:"city"`
		}) (string, error) {
			return "sunny, 21C", nil
		})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	msgs := []llm.Message{
		llm.SystemMessage("You are a helpful assistant. Answer in one short sentence."),
		llm.UserMessage(llm.Text("What is the weather in Paris? Use the get_weather tool.")),
	}
	first, err := client.Execute(ctx, msgs, []*llm.Tool{tool})
	if err != nil {
		t.Fatalf("round 1: %v", err)
	}
	if first.Message == nil || len(first.Message.ToolCalls) == 0 {
		t.Fatalf("round 1 carried no tool call: %+v", first.Message)
	}
	call := first.Message.ToolCalls[0]
	msgs = append(msgs,
		llm.AssistantMessage(first.Message.Parts, first.Message.ToolCalls...),
		llm.ToolResultMessage(call.ID, llm.Text("sunny, 21C")),
	)
	second, err := client.Execute(ctx, msgs, []*llm.Tool{tool})
	if err != nil {
		t.Fatalf("round 2 (the failing case before the fix): %v", err)
	}
	if second.Message == nil || len(second.Message.Parts) == 0 || second.Message.Parts[0].Text == "" {
		t.Fatalf("round 2 answered nothing: %+v", second.Message)
	}
	t.Logf("round 2 answer: %s", second.Message.Parts[0].Text)
}
```

```bash
CGO_ENABLED=1 go test -tags fts5 -overlay=/tmp/zz_overlay_round.json ./plugins/subscriptions/qodercn -run TestZZLiveToolRound -v -count=1
```
预期：修复前 round 2 失败（`provider_error … Messages with role 'tool' must be a response to a preceding message with 'tool_calls'` / `invalid character 'd'`），修复后 round 2 打印一句天气回答。
（overlay 内容：`{"Replace": {"<repo>/plugins/subscriptions/qodercn/zz_live_tool_round_test.go": "/tmp/zz_live_tool_round_test.go"}}`）
5b TUI 真机：`go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain`，在 tmux 里用真实 `~/.forebrain` 启动（登录在真实 home 的 `qodercn.json`；验收项目目录用临时目录，避免污染仓库），`/model` 选 `qodercn/DeepSeek-Flash`，发一句需要读文件的问题（例如 "用 shell 看一下 /tmp 下有多少文件，然后告诉我"）。
   取证：`tmux capture-pane -p` 逐屏，断言：工具卡出现并执行 → 回合正常收尾；屏幕**不出现** `invalid character 'd'`、**不出现** `Streaming response ended before any complete data…`。

5b′ 若执行者**没有**本单的真模型授权（本计划 §头部记录的 owner 原话只对本会话生效）：跳过 5a/5b 的真模型调用，改为：
   - agent 侧用 fake provider 跑 Step 0/2/3 的 httptest 用例 + 一次 TUI 冒烟（fake 模型，证明流式/工具轮 UI 路径不回归）；
   - 把 5a/5b 写成《用户手动验收清单》附在计划末尾（命令、模型、要看的两个失败标志、取证方式），由 owner 自己跑真模型，不得由 agent 代跑。

**Step 6 — 收口**
- 计划文件状态改"已实施并真机验收通过（日期）"，补《验收记录》章节（场景 → 证据）。
- 清理：`tmux kill-session`，删临时项目目录与 `/tmp/zz_*` 探针；按 §6 判据 4 核对"本任务没在 `pkg/**` 写任何一行"（并行会话的 WIP 不算，也不得回滚）。
- 报告：根因链、最小改动清单、验证证据、遗留（§9）。

---

## 6. Done criteria（机器可查）

1. `CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/qodercn -count=1` → `ok`，且包含三条新测试：`TestToolCallTurnCarriesAContentString`、`TestStreamSurfacesTheEnvelopeProviderError`、`TestNonStreamingRequestAssemblesTheStreamedAnswer`。
2. `gofmt -l plugins/subscriptions/qodercn` 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./plugins/subscriptions/qodercn` 无输出。
3. 真机（Step 5b；无本单真模型授权时按 Step 5b′ 走 fake 冒烟 + 用户手动清单）：一轮含工具的对话走完，屏幕无 `invalid character 'd'`、无 streaming-fallback 警告；`capture-pane`（或用户手跑记录）证据贴进验收记录。
4. **本任务 authored 的改动只有 `plugins/subscriptions/qodercn/transport.go` 与 `qodercn_test.go`（插件目录 gitignore，`git status` 里本来看不到它们）**，外加 `docs/plan/` 下的计划副本；`pkg/**` 里**没有本任务写下的任何一行**。判断方式：用 `git diff --stat`/`git status --porcelain` 看当前树，若出现 `pkg/**` 改动，用 `git diff -- <路径>` 确认是不是并行会话（`forebrain-harness-81`/`-e9`）的 WIP；**别人的 WIP 不是本任务的失败**，也不得回滚。只有"本任务自己动了 `pkg/**`"才算违规。
5. 计划文件已回填状态与《验收记录》。

---

## 7. STOP conditions

- Step 0 的离线复现**不失败**（assistant 消息已带 `content`）→ 计划的因果链与代码不符，停下重新取证，不要改代码。
- Fix 1 之后真机工具轮仍 400（探针原样复现）→ 根因判断错误，停下重推因果链，不要叠加补丁。
- 需要改 `pkg/**` 才能让真机通过 → 越界，停下报告（本仓架构要求修在插件层）。
- Fix 3 组装出的非流式响应让宿主丢字段（usage/tool_calls/reasoning 任一），或让既有流式测试变红 → 停下修正设计，不接受回归。
- 其它包的测试因本改动变红（并行会话正在 `pkg/**` 里改的东西）→ 不是本计划范围，报给对应会话，不动别人的文件。
- 真机验收时若发现"工具轮成功但无 reasoning 回传/usage 归零"等新缺陷 → 记录并停下问 owner 是否并入本期。

---

## 8. 风险与非功能要求

- **延迟**：Fix 1 只在请求构造时多一次 messages 的反序列化加序列化。现场请求体是 190 到 245KB（req_bytes 193492 / 244204），一次纯内存往返，无网络、无额外等待，且不在流式逐帧热路径上。Fix 3 只影响非流式回落路径（本来就整包读取）。
- **资源**：`assembleQoderCompletion` 读完整流后才分配，受与流式相同的 16MB 扫描上限；上游 body 必关。
- **鲁棒性**：错误信封 ≠ chunk 的判定以"JSON 对象且带 code/error 且 statusCodeValue>=400"为白名单，避免把正常 chunk（`choices`）误判；空流、非法帧、`[DONE]` 缺失都有明确结果。
- **可维护性**：三处各有注释说明"为什么"，测试名直接描述契约；闭源插件内的修复同时留一份 `docs/plan` 记录。
- **用户体验**：上游错误原话直出（不再有误导读者的 proxy/gateway 提示与 JSON 语法错误）。

---

## 9. Maintenance notes / 观察项

1. **同一缺陷族的宿主观察项（本期不改）**：`go-openai` 的 `content` 是 `omitempty`，宿主 `compatMessageToOpenAI` 对"纯工具调用"的 assistant 回合不设 `Content` → **所有** OpenAI 兼容 provider 的线缆都缺这个字段。只有 qoder 网关后的 DeepSeek 拒收（实测 `qmodel_latest`/opencode 均接受）。若将来有第二家 provider 拒收，再评估在宿主层统一补 `content`（届时需接受所有 provider 请求字节变化/前缀缓存一次分叉）。
2. **网关恒流式**是修复 Fix 3 的前提（2026-11-13 实测 `stream:false` 仍回 SSE）。若网关改为支持非流式，`assembleQoderCompletion` 会自然不再被走到，届时可删。
3. **插件闭源**：本修复只改 gitignore 的插件目录；宿主树不得出现 `qodercn` 字符串（本计划不触碰宿主）。
4. **复验用真模型**：owner 在**本单**明确允许（原话"可以用qocoder cn的deepseek-flash模型做真机验证"）。这是单次豁免，不构成新裁决：其它任务仍按 2026-11-07"agent 验收一律 fake 模型"执行，除非 owner 再次明示。
5. 评审者重点看：Fix 1 是否真的只动"带 `tool_calls` 且缺/`null` content"的 assistant 消息；Fix 2 的错误判定是否可能误吞正常 chunk；Fix 3 的合并语义是否与宿主流式解析一致（尤其 tool_call 的 id/name 首值、arguments 累加）。

---

## Implementation Status

状态：**COMPLETE — 已实施并真机验收通过（2026-11-14）**
基线：执行时 `git rev-parse --short HEAD` = `c32d23d5`；`transport.go` 469 行 / `qodercn_test.go` 1448 行 / `unwrapSSEPayload` 在 450 行 / `Stream: true` 在 339 行 —— 与计划 §5 起始基线逐项一致，无 STOP。

### 落地内容（只有两个文件，都在 gitignore 的闭源插件目录 `/plugins/`）

`plugins/subscriptions/qodercn/transport.go`

| 计划项 | 落地符号 | 说明 |
|---|---|---|
| Fix 1 | `ensureToolCallContents(json.RawMessage) json.RawMessage`，接在 `buildRemoteChatAsk` 的 `Messages:` 字段 | 只给"有非空 `tool_calls` 且无 `content`/`content:null`"的消息补 `""`；**没有任何消息需要补正时原样返回输入字节**（不重排键序、不改其它字段） |
| Fix 2 | `qoderEnvelopeFailure(payload string) error` + `unwindJSONString` + `qoderFailure`/`qoderFailureDetails` | 信封 `statusCodeValue >= 400` 即失败；消息优先 `details.error.message`，`*llm.APIError` 由 `fmt.Errorf("qodercn: %w", …)` 包裹 |
| Fix 3 | `scanQoderFrames(upstream io.Reader, onChunk func(string) error) (sawDone bool, err error)`，`unwrapQoderSSEStream` 与 `assembleQoderCompletion` 共用；`RoundTrip` 在 SSE 且 `!chat.Stream` 时走组装 | 逐帧判定只写一份（`[DONE]` 终止、`[NOT_EXCEED_QUOTA]`/`[EXCEED_QUOTA]`/`[NOTIFICATIONS]` 丢弃、错误信封上抛、`unwrapSSEPayload` 取 chunk）；组装 `id/created/model/choices/message(content,reasoning_content,tool_calls)/finish_reason/usage`，tool_calls 按 `index` 合并（首个非空 id/type/name 生效、arguments 累加），usage 原样透传，输出 `application/json` + `Content-Length`；上游 body 关闭 |

`plugins/subscriptions/qodercn/qodercn_test.go`

- `TestToolCallTurnCarriesAContentString`（Step 0 的离线复现，`Parts` 为 nil 的纯工具轮）
- `TestStreamSurfacesTheEnvelopeProviderError`（Step 2）
- `TestNonStreamingRequestAssemblesTheStreamedAnswer`（Step 3，`with-DONE` / `no-DONE` 两个子用例，tool_call arguments 跨帧累加）
- 装置：`captureAsk`（一次 `Execute` 同时取回假网关收到的 `RemoteChatAsk` 与结果）、`sseFailureFrame`（复刻网关的 200 + 400 信封帧）、`mustReadAll`

### 逐步验证（命令 → 实际结果）

| 步骤 | 命令 | 结果 |
|---|---|---|
| 0 复现 | `CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/qodercn -run TestToolCallTurnCarriesAContentString -count=1` | **FAIL**（修复前）：`the tool-call assistant message carries no content field on the wire: …{"role":"assistant","tool_calls":[…]}…` —— 根因在离线侧成立 |
| 1 Fix 1 | 同上 | **PASS**；整包 `… ./plugins/subscriptions/qodercn -count=1` → `ok` |
| 2 Fix 2 | `-run TestStreamSurfacesTheEnvelopeProviderError` | **PASS**（`llm.HTTPStatusOf(err)==400`、错误文本带上游原文、不含 `invalid character`）。反向证明：把 `qoderEnvelopeFailure` 调用短路后 → FAIL `status = 0, want the provider's 400 (err = qmodel_38max: stream closed before any event)` |
| 3 Fix 3 | `-run TestNonStreamingRequestAssemblesTheStreamedAnswer` | **PASS**（两个子用例）。反向证明：把 `if !chat.Stream` 分支短路后 → 两个子用例都 FAIL，`execute: invalid character 'd' looking for beginning of value`（与现场症状逐字一致） |
| 4 包级 | 整包 `-count=1`；`gofmt -l plugins/subscriptions/qodercn`；`CGO_ENABLED=1 go vet -tags fts5 ./plugins/subscriptions/qodercn` | `ok`；无输出；无输出 |
| 5a 真机探针 | `go test -overlay … -run TestZZLiveToolRound`（真实网关 + 真 `dfmodel`） | **PASS**：`round 2 answer: The weather in Paris is sunny at 21°C.`。反向证明：仅把 `Messages:` 改回 `chat.Messages` → **FAIL** `round 2: qodercn: Messages with role 'tool' must be a response to a preceding message with 'tool_calls' (request_id: de4bf87d-…)`，恢复后再次 PASS |
| 5b TUI 真机 | 真实 `~/.forebrain`（真登录 `qodercn.json`）+ 临时项目目录 `/tmp/zz-fb-proj`，tmux 驱动 | 修复前二进制（`build/bin/forebrain`，09:22 前构建，不含本修复）在同一场景复现截图原症状：`● Ran ls …` → `● system Streaming response ended before any complete data…` → `● error invalid character 'd' looking for beginning of value`。修复后二进制（同树构建）同一场景：`● Ran ls -la /tmp/zz-fb-proj` → `▲ Thought for 1s` → 正常中文回答，`tmux capture-pane` 里 `invalid character` 0 次、`Streaming response ended` 0 次 |

### 偏差与判断（均不改变意图/范围/公开行为）

1. **Step 0 测试装置首跑失败在别处**：第一版假网关只发了 content 帧、没有 `finish_reason`/usage 帧，运行报 `stream ended without a finish frame`。补齐帧后按计划失败在"缺 content"。属测试装置修正。
2. **`ensureToolCallContents` 无改动时原样返回**：计划只要求"字段集合与值不变"，这里进一步做到"无需补正的请求字节完全不动"，比 map 重排更保守。
3. **错误信封判定放宽**：计划写"信封体是 JSON 对象且带 code/error 且 `statusCodeValue>=400`"；实现只要求"可解析为信封且 `statusCodeValue>=400`"。理由：400 信封即失败，body 缺失时旧代码会把它当 chunk 透传（再次吞掉）；正常 chunk 永远不带 `>=400` 的 `statusCodeValue`，白名单本身不变。
4. **tool_calls 合并用 `map[index]` + 排序**而非按 index 扩容切片：异常/超大 index 不会造成巨型分配或 panic（网络输入）。
5. **5b 的"修复前"证据来自仓库既有旧二进制** `build/bin/forebrain`（构建于 2026-11-13 23:50，已验证不含本修复），没有回滚工作树。
6. **真机 5b 用了 owner 真实 home**（计划 §5b 明确要求）：新建了一个验收会话 `cli-f160005e-b724-499e-bc38-0b99983964d7`，其模型状态记录为 `qodercn/dfmodel/high`（`fb_session_model_state`）。若"新会话默认模型=最近使用"按最新 `updated_at` 取值，新会话的默认模型会变成 `qodercn/DeepSeek-Flash`（此前是 `opencode-go/deepseek-v4.1-flash`）；模型选择器在输入过滤词后没有可见光标（属并行会话 e9 的 /connect 面板改动范围），故未再切回。
7. 计划 §5 的行号（`transport.go:88-94`、`405-430` 等）在实施后已漂移；记录以符号名为准。

### 遗留 / 交接

- 宿主 `pkg/**` 一行未改；`git status --porcelain` 里的 `pkg/**` 改动全部是并行会话（`forebrain-harness-81` / `forebrain-harness-e9`）的 WIP，未触碰、未回滚。
- 修复只存在于工作树的闭源插件目录（`/plugins/` 已 gitignore），不会进 git；发布时随插件包一起编译。
- §9 的观察项不变：宿主 `compatMessageToOpenAI` 仍对纯工具轮的 assistant 省略 `content`（所有 OpenAI 兼容 provider 的线缆都如此），只有本上游拒收，故仍按计划修在插件层。
- §9.4 的单次真模型豁免已用完，后续任务仍按 2026-11-07「agent 验收一律 fake 模型」执行。
