# Plan 332: list_usable_models 的 TUI 卡片按 Markdown 分组渲染（无 Id、无 Models:、无 YAML 树）

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: baseline commit `4e8158e9` (dev). Run:
> ```bash
> git diff --stat 4e8158e9..HEAD -- pkg/tool/format.go pkg/tool/format_test.go pkg/tui/render.go pkg/tui/reducer.go pkg/tui/chat_session_test.go
> ```
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> **content** mismatch, treat it as a STOP condition (moved line numbers
> alone are not drift). **注意**：plan 331 先落地时 `pkg/tool/format.go` 的
> `usableModelRow`/`readUsableModels` 已变（endpoint 删除、efforts 加入）——
> 那是预期内的前序改动，不是漂移；本计划的摘录以 331 落地后的形态为准，
> 下面同时给出两种形态的识别方式。

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW（纯展示层；引擎 JSON 契约不动）
- **Depends on**: plans/331-usable-models-roster-contract.md（数据契约先落地；本计划读取其新字段）
- **Category**: direction（owner 2026-11-20 需求 1，原型已由 owner 确认）
- **Planned at**: commit `4e8158e9`, 2026-11-20

## Why this matters

owner 截图指出：TUI 里 `list_usable_models` 工具消息卡的 output 区域现在是
「Models:」开头、把 JSON 逐字段展开成 `Id: … / Provider: … / Model: …` 的
YAML 风格树，对用户不友好。owner 要求（原型已确认）：

1. 删掉「Models:」这行文案；
2. 不再显示 YAML 风格数据结构，**按 Markdown 渲染**；
3. 布局＝**按 provider 分组的列表**（确认过的原型选项 1）；
4. **Id 字段绝不向用户显示**——id 只是发给 LLM 的派发参数，对用户无意义
   （owner 原话）；它只存在于引擎 JSON 里。

根因（已核实）：TUI 通用路径 `formatToolContentWithInput`
（`pkg/tui/render.go:2876`）对没有专属分支的工具落到
`readableToolPayload`（`:3011-3017`）→ `tool.ReadToolAnswer` → 通用
「Field: value」树（`readableFieldLabel` 把 snake_case 键 capitalize）。
`tool.ReadableToolAnswer` 虽有 `list_usable_models` 专属读取器，但 TUI 通用
路径走的是 `ReadToolAnswer`，不是它。修法＝给这张卡一个专属 Markdown 分支，
完全照抄 MCP 卡与 peer_list 卡的既有机制。

## Current state

### 引擎应答形态（plan 331 落地后）

`list_usable_models` 的应答 JSON（`pkg/run/subagent.go` `listUsableModels`）：

```json
{"models":[
  {"id":"llm_a","provider":"zhipuai","model":"glm-5.3","current":true,"efforts":["low","medium","high"],"default_effort":"medium"},
  {"id":"llm_b","provider":"qodercn","model":"DeepSeek-Flash","handed_off":true}
],"note":""}
```

- `model` 已是给人读的 label（331）；`current`/`handed_off` 只在 true 时出
  现；`efforts`/`default_effort` 可缺省；空列表时 `models` 为空、`note` 带
  一句话（"No model is allowed for subagents you pick. Omit model to run on
  the conversation's model."）。
- 旧会话回放的卡片可能是旧形态（带 `endpoint`、无 efforts、`current:false`
  显式出现）——读取器必须两种都认。

### `pkg/tool/format.go` — 解析设施（复用，不重写）

331 落地后的行类型（若 331 未落地，见 STOP conditions）：

```go
type usableModelsAnswer struct {
	Models []usableModelRow `json:"models"`
	Note   string           `json:"note"`
}

type usableModelRow struct {
	ID            string   `json:"id"`
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	Current       bool     `json:"current"`
	HandedOff     bool     `json:"handed_off"`
	Efforts       []string `json:"efforts"`
	DefaultEffort string   `json:"default_effort"`
}
```

候选提取（`:4287-4297`，同时接受裸 JSON 与 live 卡的 `{"output": …}` 信封）：

```go
func toolAnswerCandidates(content string) []string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil
	}
	candidates := []string{trimmed}
	if text, ok := GenericOutputText(trimmed); ok {
		candidates = append(candidates, strings.TrimSpace(text))
	}
	return candidates
}
```

`readUsableModels`（331 版）的识别规则照抄：unmarshal 失败 → 下一候选；
`raw.Models == nil && strings.TrimSpace(raw.Note) == ""` → 下一候选。

### `pkg/tui/render.go` — 三个接入点

**(a) 专属早退分支的位置**：`renderCompactFrame` 内 peer_list 分支
（`:2340-2349`）——这是「JSON 应答画成专属卡片、full-body 与 compact 两模
式通用」的既有范式：

```go
// peer_list's answer is a roster in JSON: the card draws it as one
// aligned row per agent, in full-body and compact mode alike, and never
// as the JSON itself.
if strings.EqualFold(title, "peer_list") && !toolStatusFailed(f) {
	if block, ok := renderPeerListCard(f.Content, r.layoutTermWidth()); ok {
		_, _ = fmt.Fprintln(r.out, block)
		_, _ = fmt.Fprintln(r.out)
		return
	}
}
```

紧随其后的 MCP 分支（`:2359-2383`）是 Markdown 渲染的范式（宽度、前缀、
fallback 逐字照抄）：

```go
contentWidth := maxCardContentWidth(r.layoutTermWidth()) - 2
if contentWidth < 20 {
	contentWidth = 20
}
md := renderAssistantMarkdownWithWidth(textContent, contentWidth, r.diffTheme)
if md == "" {
	md = textContent
}
_, _ = fmt.Fprintln(r.out, prefixLines(md, "  └ ", "    "))
_, _ = fmt.Fprintln(r.out)
return
```

**(b) `formatToolContentWithInput`**（`:2876` 起）——其它管线（viewport 重
建、复制等）格式化内容时走这里；peer_send 分支（`:2889-2894`）是「专属读取
器成功就返回、失败落通用」的范式：

```go
if lower == "peer_send" {
	return content
}
```

通用尾段（`:2948-2963`）就是产生 YAML 树的地方（`readableToolPayload` →
`tool.ReadToolAnswer`）——新分支必须在它之前。

**(c) 折叠预览**：`pkg/tui/reducer.go:3298-3309`
`toolPreviewUsesRenderedBody`——凡有富渲染器的工具都要登记，否则折叠态预览
会用纯文本重建、把 Markdown 降级：

```go
func toolPreviewUsesRenderedBody(f Frame) bool {
	if f.Kind != FrameTool {
		return false
	}
	title := strings.TrimSpace(f.Title)
	return strings.EqualFold(title, intermediateToolName) ||
		strings.EqualFold(title, "memories_add_ad_hoc_note") ||
		strings.EqualFold(title, "retrieve_output") ||
		isMemoryQueryTool(title) ||
		isMCPToolTitle(title) || isDiffTool(title) ||
		shellFrameHasDiffBlocks(f)
}
```

**不要**把 `list_usable_models` 加进 `isMarkdownBodyTool`（`:3945-3954`）：
早退分支 (a) 已在它之前返回；MCP 卡同样不在该列表里（它走早退分支＋(b)＋
(c) 三件套）。照抄 MCP 的接线，不要发明第四种。

### 测试范式（照抄）

- 渲染器整卡测试：`pkg/tui/chat_session_test.go:20993-21025`
  `TestRendererMemoryNoteUsesSemanticHeaderAndMarkdownBody`：

```go
var out bytes.Buffer
r := NewRenderer(&out, &out)
r.renderCompactFrame(Frame{
	Kind:    FrameTool,
	Title:   "memories_add_ad_hoc_note",
	Content: note,
	Final:   true,
	ToolMeta: tool.ToolMeta{ToolName: "memories_add_ad_hoc_note", Status: "completed", Input: map[string]any{...}},
}, "tool", "214")
plain := stripANSI(out.String())
// 断言渲染后的正文在场、raw 传输文本（## 原文、JSON 键、```）不在场
```

- live/回放等价范式：`pkg/tui/render_test.go:1183-1209`
  `TestPeerListCardDrawsAnAlignedRoster`（live 信封与裸 JSON 两路渲染逐行相
  等，且泄露检查含 `"{"`、```` ``` ````）。live 信封的构造见
  `pkg/tool/format_test.go:3661`：
  `"output:\n\n```json\n{\"output\":" + strconv.Quote(roster) + "}\n```"`。
- tool 包读取器测试范式：`pkg/tool/format_test.go:3644-3665`
  `TestListUsableModelsCard`（331 已重写其夹具）。

## Commands you will need

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
export FOREBRAIN_HOME="$(mktemp -d)"
```

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| 聚焦测试（tool） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` | ok |
| 聚焦测试（tui） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | ok |
| 单测复跑 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '^TestRendererUsableModels' -count=1 -v` | PASS |
| 格式 | `gofmt -l cmd pkg third_party plugins` | 无输出 |
| Vet | `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0 |

## Scope

**In scope**:
- `pkg/tool/format.go`（只新增 `FormatUsableModelsMarkdown`，不改既有函数）
- `pkg/tool/format_test.go`（新增测试）
- `pkg/tui/render.go`（早退分支＋formatToolContentWithInput 分支）
- `pkg/tui/reducer.go`（toolPreviewUsesRenderedBody 登记）
- `pkg/tui/chat_session_test.go`（新增渲染测试）

**Out of scope**:
- `pkg/run/**` —— 引擎 JSON 契约是 331 的产物，本计划只读。
- `readUsableModels` / `ReadableToolAnswer` —— gateway web 走的纯文本读取器
  保持 331 形态；本计划新增的是**并列**的 markdown 格式化器。
- `isMarkdownBodyTool`、`summaryToolBody`、`readableToolPayload`、
  `ReadToolAnswer`、`readableFieldLabel` —— 通用管线不动（其它工具还在用）。
- web 前端、gateway —— owner 需求 1 只针对 TUI。
- `renderPeerListCard`、MCP 分支 —— 范式只抄不改。

## Git workflow

默认 in-tree 零 commit（owner 手动提交）；如 operator 明示提交，conventional
风格（例：`tui: render the usable-models roster as grouped Markdown`）。不
push、不 merge。

## Steps

### Step 1: pkg/tool 新增 FormatUsableModelsMarkdown

在 `pkg/tool/format.go` 的 `readUsableModels` 之后新增（导出，TUI 调用）：

```go
// FormatUsableModelsMarkdown is list_usable_models' answer as the TUI card's
// Markdown source: one bold heading per provider in first-seen order, one
// bullet per model under it — the label a person reads, its thinking levels
// and default, and the current/handed-off marks. The row id is the LLM's
// dispatch argument, never a person's reading, so it never appears here
// (owner 2026-11-20). ok is false for content that is not this answer.
func FormatUsableModelsMarkdown(body string) (string, bool) {
	for _, candidate := range toolAnswerCandidates(body) {
		var raw usableModelsAnswer
		if json.Unmarshal([]byte(candidate), &raw) != nil {
			continue
		}
		if raw.Models == nil && strings.TrimSpace(raw.Note) == "" {
			continue
		}
		if len(raw.Models) == 0 {
			note := strings.TrimSpace(raw.Note)
			if note == "" {
				return "", false
			}
			return note, true
		}
		var groups []string
		seen := map[string]int{}
		for _, row := range raw.Models {
			provider := strings.TrimSpace(row.Provider)
			idx, ok := seen[provider]
			if !ok {
				idx = len(groups)
				seen[provider] = idx
				groups = append(groups, "**"+provider+"**")
			}
			bullet := "- " + strings.TrimSpace(row.Model)
			if len(row.Efforts) > 0 {
				bullet += " — thinking: " + strings.Join(row.Efforts, ", ")
				if def := strings.TrimSpace(row.DefaultEffort); def != "" {
					bullet += " (default " + def + ")"
				}
			}
			if row.Current {
				bullet += " *(current)*"
			}
			if row.HandedOff {
				bullet += " *(handed off)*"
			}
			groups[idx] += "\n" + bullet
		}
		return strings.Join(groups, "\n\n"), true
	}
	return "", false
}
```

（分组用「首见顺序的 provider 索引＋字符串累加」即可，不必引入新类型；若
你选择用结构体分组，输出必须逐字一致。）

在 `pkg/tool/format_test.go` 新增 `TestFormatUsableModelsMarkdown`：
1. 分组夹具（两 provider、三行，含 current/handed_off/efforts/default）：

```go
roster := `{"models":[` +
	`{"id":"llm_a","provider":"zhipuai","model":"glm-5.3","current":true,"efforts":["low","medium","high"],"default_effort":"medium"},` +
	`{"id":"llm_b","provider":"qodercn","model":"DeepSeek-Flash","handed_off":true},` +
	`{"id":"llm_c","provider":"zhipuai","model":"glm-5.3-flash"}` +
	`]}`
want := "**zhipuai**\n" +
	"- glm-5.3 — thinking: low, medium, high (default medium) *(current)*\n" +
	"- glm-5.3-flash\n\n" +
	"**qodercn**\n" +
	"- DeepSeek-Flash *(handed off)*"
```

   断言输出逐字等于 want，且不含 `llm_a`/`llm_b`/`llm_c`（id 永不出现）。
2. 空列表＋note → 返回 note 原文、true。
3. 旧形态回放（带 endpoint、wire model、无 efforts、`"current":false`）→
   bullet 只有 model＋flags，无 thinking 段，endpoint 不出现。
4. live 信封（`"output:\n\n```json\n{\"output\":" + strconv.Quote(roster) + "}\n```"`）
   → 与裸 JSON 输出逐字相等。
5. 非应答内容（`"not json"`、`{"foo":1}`、空串）→ false。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run '^TestFormatUsableModelsMarkdown$' -count=1 -v` → PASS。

### Step 2: TUI 早退分支（两模式通用）

在 `pkg/tui/render.go` 的 peer_list 分支（`:2343-2349`）之后、MCP 分支之前
插入：

```go
// list_usable_models' answer is a model roster in JSON: the card draws it as
// grouped Markdown — a provider heading, one bullet per model label with its
// thinking levels — in full-body and compact mode alike, never as the JSON
// or the generic "Field: value" tree. The row id is the LLM's argument and
// never reaches the card (owner 2026-11-20).
if strings.EqualFold(title, "list_usable_models") && !toolStatusFailed(f) {
	if md, ok := tool.FormatUsableModelsMarkdown(f.Content); ok {
		contentWidth := maxCardContentWidth(r.layoutTermWidth()) - 2
		if contentWidth < 20 {
			contentWidth = 20
		}
		rendered := renderAssistantMarkdownWithWidth(md, contentWidth, r.diffTheme)
		if rendered == "" {
			rendered = md
		}
		_, _ = fmt.Fprintln(r.out, prefixLines(rendered, "  └ ", "    "))
		_, _ = fmt.Fprintln(r.out)
		return
	}
}
```

解析失败（ok=false）时不 return，落既有管线——与 peer_list 分支同款容错。

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` → exit 0（渲染测试在 Step 4）。

### Step 3: formatToolContentWithInput 分支＋折叠预览登记

`pkg/tui/render.go` `formatToolContentWithInput`：在 peer_send 分支
（`:2892-2894`）之后加：

```go
// list_usable_models: the card's body is the grouped Markdown roster, not the
// generic "Field: value" tree the fall-through would build from the JSON.
if lower == "list_usable_models" {
	if md, ok := tool.FormatUsableModelsMarkdown(content); ok {
		return md
	}
}
```

`pkg/tui/reducer.go` `toolPreviewUsesRenderedBody`：在返回链里加一行
（`strings.EqualFold(title, "list_usable_models") ||`，位置放在
`isMCPToolTitle(title)` 之前即可），并更新该函数 doc 注释无需改动（语义未变）。

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` → exit 0。

### Step 4: 渲染器整卡测试

在 `pkg/tui/chat_session_test.go` 新增
`TestRendererUsableModelsCardRendersMarkdownRoster`（结构照抄
`TestRendererMemoryNoteUsesSemanticHeaderAndMarkdownBody` :20993）：

```go
var out bytes.Buffer
r := NewRenderer(&out, &out)
roster := `{"models":[` +
	`{"id":"llm_a","provider":"zhipuai","model":"glm-5.3","current":true,"efforts":["low","medium","high"],"default_effort":"medium"},` +
	`{"id":"llm_b","provider":"qodercn","model":"DeepSeek-Flash","handed_off":true}` +
	`]}`
r.renderCompactFrame(Frame{
	Kind:    FrameTool,
	Title:   "list_usable_models",
	Content: roster,
	Final:   true,
	ToolMeta: tool.ToolMeta{ToolName: "list_usable_models", Status: "completed"},
}, "tool", "214")
plain := stripANSI(out.String())
```

断言：
- 在场：`"zhipuai"`、`"glm-5.3"`、`"DeepSeek-Flash"`、`"thinking: low, medium, high"`、
  `"default medium"`、`"current"`、`"handed off"`、`"└"`（输出区前缀）。
- 不在场（逐个 `strings.Contains` 反断言）：`"Models:"`、`"Id:"`、`"llm_a"`、
  `"llm_b"`、`"endpoint"`、`"\"models\""`、`"{"`、```` ``` ````、`"Provider:"`。
- live 信封腿：同一 roster 包成
  `"output:\n\n```json\n{\"output\":" + strconv.Quote(roster) + "}\n```"`
  再渲染一次，两次 `stripANSI` 输出逐字相等（范式：
  `TestPeerListCardDrawsAnAlignedRoster` 的 live/replayed 等价）。
- 空 roster 腿：`{"models":[],"note":"No model is allowed for subagents you pick. Omit model to run on the conversation's model."}`
  → note 文案在场、无 `"Models:"`。

另新增 `TestUsableModelsPreviewUsesRenderedBody`：
`toolPreviewUsesRenderedBody(Frame{Kind: FrameTool, Title: "list_usable_models"})`
为 true；`Kind: FrameAssistant` 的同名 frame 为 false。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestRendererUsableModelsCardRendersMarkdownRoster|TestUsableModelsPreviewUsesRenderedBody' -count=1 -v` → 全 PASS。

### Step 5: 门禁

**Verify**:
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/tui -count=1` → 全部 ok
- `gofmt -l cmd pkg third_party plugins` → 无输出
- `CGO_ENABLED=1 go vet -tags fts5 ./...` → exit 0

## Test plan

见 Steps 1/4 内联清单：tool 包 1 个新测试（5 腿）、tui 包 2 个新测试
（整卡 3 腿＋预览登记）。范式文件均已点名。不改动任何既有测试的断言。

## Done criteria

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/tui -count=1` exit 0
- [ ] 新增 3 个测试存在且 PASS
- [ ] `grep -n 'list_usable_models' pkg/tui/render.go` 命中早退分支与 format 分支两处；`grep -n 'list_usable_models' pkg/tui/reducer.go` 命中预览登记
- [ ] `gofmt -l cmd pkg third_party plugins` 无输出
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./...` exit 0
- [ ] `git status --porcelain` 只显示 in-scope 五个文件（加 plans/README.md 若由你更新）
- [ ] `plans/README.md` 状态行已更新（除非 reviewer 维护索引）

## STOP conditions

- "Current state" 摘录与活代码内容不符（尤其：`usableModelRow` 既无
  `Endpoint` 也无 `Efforts`——说明 331 未落地且代码漂移，STOP；331 未落地
  本身也是 STOP：本计划依赖其契约）。
- `renderAssistantMarkdownWithWidth`/`prefixLines`/`maxCardContentWidth` 的
  签名或行为与摘录不符。
- 渲染测试里 provider 粗体标题或 bullet 被 markdown 渲染器吞掉（MCP 注释警
  告过通用管线会删独立粗体行——但那是 `summaryToolBody`/generic cleaner，
  不是 `renderAssistantMarkdownWithWidth`；若后者也吞，STOP 报告，不要自行
  换渲染器）。
- 某步验证失败两次。
- 需要触碰 out-of-scope 文件（尤其 `pkg/run` 或通用管线函数）。

## Maintenance notes

- 本卡与 MCP 卡共用三件套接线（早退分支＋format 分支＋预览登记）：未来改
  折叠/预览机制时两处同查。`FormatUsableModelsMarkdown` 与
  `readUsableModels` 是同一 JSON 的两种读法（markdown vs 纯文本），改引擎
  契约（331/333）时两者同步。
- model label 来自厂商 builtin 表，未做 markdown 转义：若未来 label 含
  `*`/`_` 等字符导致渲染异常，在 bullet 构造处转义，不要改渲染器。
- 评审重点：id 是否真的零泄露（含折叠预览、窄终端换行后）；live 与回放两路
  是否逐字一致；失败/拒绝态卡片是否仍走原管线（早退分支带
  `!toolStatusFailed(f)` 守卫）。