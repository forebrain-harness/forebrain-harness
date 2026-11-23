# Plan 053: 模型能读图 —— read_file 交出像素、MCP 图片透传、surface 出图、能力闸门

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat fe3ee32..HEAD -- pkg/llm pkg/tool pkg/run pkg/mcp pkg/gateway pkg/state pkg/turn frontend/src scripts/acceptance`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: HIGH（改 read_file 返回形状、provider 投影、MCP 返回形状；每一处都有既有测试兜底）
- **Depends on**: none（**054 依赖本计划的 Step 1**：`llm.ImagePartWithin`）
- **Category**: bug（能力缺口：模型看不到任何图片）
- **Planned at**: commit `fe3ee32`, 2026-10-14

## Why this matters

今天模型**读不到任何图片**——它自己 `screencapture` 出的 PNG、自己生成的图、playwright 截的屏、
MCP 工具回传的截图，全部拿不到像素。三条互为印证的事实：

1. `read_file` 对图片扩展名一律拒读（`pkg/tool/file_tools.go:240-260`）；
2. `pkg/tool/` 下**没有任何工具**返回 `[]llm.ContentPart`（只有 pkg/agent 有），所以没有第二条路；
3. 代码自己把结论写在注释里：`pkg/turn/compaction.go:366` —— "**No tool can hand the model pixels**,
   so an image is attached to the turn instead of named in it."

owner 2026-10-14 拿 Claude Code 的 `Read(...corner_zoom.png)` → `Read image (24.2KB)` 对照，
要求补齐；并明确「Out of scope 里的必须加入本期计划」，因此本计划把**四类缺口全部并入**：

- **A. `read_file` 读图片**（Step 1–3）：图片文件返回「一行说明 + 真图」，不再拒读。
- **B. MCP 工具结果的图片**（Step 5 + **5b**）：`pkg/mcp` 现在把 MCP 返回的图片落盘成
  `$FOREBRAIN_HOME/state/mcp-results/*.png`（**在 home 内、不在主代理 workspace 内**）再回一行路径文本
  （`result_processing.go:120-180`），模型既看不到图、`read_file` 又拒图/要审批 → 截图类 MCP 工具
  （playwright screenshot）等于废掉。两半都要做：**5 让图片随工具结果直接进模型**；
  **5b 把结果目录搬进主代理 workspace**（owner 2026-10-14 裁决：全局/主代理/项目级三层的 mcp-results
  都必须落在主代理 workspace 内）——主代理 workspace 本来就是读豁免根，于是审批这一环自然消失，
  `ProtectedReadReason` 一个字都不用改。
- **C. surface 出图**（Step 7）：web 工具卡显示缩略图（今天全站没有一处 `<img>` 显示内容图片），
  TUI 显示标记行 + `Ctrl+O` 用系统查看器打开（owner 选定「标记行 + 按键打开」这一档；真像素内联是新的图形协议子系统，不在本期。**用 Ctrl+O 而不是裸 `o`**，理由见 Step 7c）。
- **D. 模型能力闸门**（Step 4）+ **catalog 数据修正**（Step 4c）：`supports_attachments` 今天只是 catalog 里的
  一个字段、没有任何消费者；在不支持图片输入的会话模型上发图会让请求 400，要让它在**发送前**
  变成一句可执行提示（fail-open：未知模型放行）。**而 catalog 本身有一处是错的**：
  `deepseek/deepseek-v4-flash` 写着 `attachment: false`，owner 2026-10-14 确认官方会把它路由到
  `deepseek-flash`、它实际能理解图片——不修数据，闸门就会拦住 owner 自己的默认模型（功能等于没做）。

做完之后：模型对图片文件调 `read_file` 能答出「这张图里有什么颜色/什么报错/界面对不对」，
MCP 截图工具真正可用，web/TUI 都看得到那张图的存在，文本模型不会因为附件而拿到 400。

## Current state

### 唯一的拒读点

`pkg/tool/file_tools.go:240-266`：

```go
func validateTextReadableFile(abs string, raw []byte) error {
	ext := strings.ToLower(filepath.Ext(abs))
	if ext == ".pdf" {
		return fmt.Errorf("read_file cannot read PDF files as text")
	}
	if isImageFileExtension(ext) {
		return fmt.Errorf("read_file cannot read image files as text")
	}
	...
}

func isImageFileExtension(ext string) bool {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".ico", ".tiff", ".tif", ".heic", ".avif":
		return true
	...
```

read_file 的成功路径在 `file_tools.go:54-143`：`authorizeRead`（`:151`，`:168` 是 FOREBRAIN_HOME 的
「问一下再读」闸门）→ `os.ReadFile` → `validateTextReadableFile` → `st.RememberRead` → `st.ReadObserver()`
→ 行号分页 → `CaptureToolOutput(ctx, outMap)`（`:142`）→ `return out, nil`。
handler 的返回类型是 `string`（`file_tools.go:50-56`）。工具卡正文由 `formatReadFileStep`（`format.go:509`）
读 `Output["preview_text"]` 渲染，读不到就把整个 `Output` 当 JSON 打出来。

### 管道已经通了（这是本计划只做「一个敢交出像素的工具」的依据）

- `pkg/agent/agent.go:467 anyToContentParts`：工具返回值若是 `[]llm.ContentPart` → **原样透传**。
- `pkg/run/orchestration_llm.go:1537 toolResultContentParts`：编排层同一条规则；
  `:1554 governedContentParts` 只重写文本 part，**跳过图片**。
- `pkg/llm/llm.go:89 ImageFile(path)`：读文件 → `detectImageMIMEType`（`:111`，前 512 字节嗅探 + 扩展名兜底）
  → 只接受 `image/jpeg|png|gif|webp` → `ImageBase64`。
- provider 翻译：compat `pkg/llm/openai/compat_llm.go:1106-1124`（base64 → `data:<mime>;base64,…` → `MultiContent`）；
  Responses `pkg/llm/openai/responses_llm.go:1190-1206`；Anthropic `pkg/llm/anthropic/agent_llm.go:377-398`
  且 `:368 agentAnthropicToolResultContent` 在 **tool_result 内容块里也支持图片**。
- 持久化双向：写 `pkg/state/usage.go:81-124 contentBlocksJSON`（url + base64 两种 image block）、
  读 `pkg/state/message_parts.go:435-479 ParseMessageParts`（不限角色）；
  回归测试 `pkg/state/message_parts_test.go:106`。
- 估算：`pkg/llm/tokestimate.go:89 ImageTokens = 2000`、`:134 estimateImageTokens`（图片已计入占用）。
- 注册：`pkg/tool/registry.go:123-127`。

### MCP 这条链的确切形状

```
pkg/run/runner.go:345-358  newMCPDelegatingTool(..., call func(ctx, json.RawMessage) (string, error))
pkg/run/runner.go:2534      call := mcpToolCaller(reg, t.name)
pkg/run/telemetry.go:128-142 mcpToolCaller → sess.CallToolJSON  → (string, error)
pkg/mcp/bridge.go:981        Session.CallToolJSON
pkg/mcp/bridge.go:1009-1033  callToolAttempt → json.Marshal(res) → processToolResultJSON(...)
pkg/mcp/result_processing.go:24  processToolResultJSON(home, server, tool, raw, acct) (string, error)
pkg/mcp/result_processing.go:120-180  persistBinaryResourceContent：把每个 image/blob 块
                                      **替换**成一行文本「Binary content saved to <path>」
pkg/mcp/result_processing.go:91  resultJSONContainsImage：只看「还有没有 image 块」，用来跳过截断
```

`llm.NewRawTool` 的 handler 本身已经返回 `any`（`pkg/llm/tool.go:319-323`），
所以加宽的是 `call`/`mcpToolCaller`/`CallToolJSON` 这条签名链，不是工具框架。

### 能力闸门的落点

- catalog：`pkg/llm/catalog.go:180 LookupModel(model) (Model, bool)`、`:33 Hit.SupportsAttachments`、
  `:136/:344` 从 `models.json` 的 `attachment` 字段填充；`assets/models.json:1519` 有
  `deepseek/deepseek-v4-flash-vision-exp`（`"input": ["text","image"]`）。
- 会话模型在 context 里：`pkg/run/config.go:1190 PrimaryModelState`、`:1500-1525
  WithSessionModelSelection / SessionModelSelectionFromContext`。
- 工具层装谓词的既有先例（**照这个写**）：`pkg/run/runner.go:1088-1265` 一串 `r.tools.SetXxx(...)`
  （`SetActionHook`、`SetReadObserver`、`SetFileChangeRecorder`…），落点是 `pkg/tool/state.go` 的 setter。
- 今天的消费者：**没有**。`pkg/turn/models.go:31/123/250` 把它投影给前端，
  `frontend/src/lib/api.ts:148` 有字段但没人用。

### Surface

- **web**：工具步骤渲染在 `frontend/src/components/chat/ToolCallCard.vue`；全站**没有任何一处**
  用 `<img>` 显示内容图片（用户附件的缩略图标是 lucide 图标）。
- **取图端点**：既有 `pkg/gateway/files_write.go:30 handleWorkspaceFileRead`（GET `/v1/workspace/file`），
  根解析唯一入口是 `s.resolveFilesPath(ctx, session_id, project_id, path)`（`files_write.go:41`，
  路由注册在 `pkg/gateway/api_extra.go:128-140` 的 `workspace` 组）——**新端点必须复用它**，
  不要另写一套根解析（仓库既有规则：文件视图只有一个根解析）。
- **TUI**：`pkg/tui/render.go:1618 case *mdast.Image` 只处理 markdown 图片的 alt 文本；
  仓库**没有** sixel/kitty/OSC 1337 任何图形协议代码。owner 已选定「标记行 + 按键打开」。

### 命令

| Purpose | Command | Expected |
|---|---|---|
| 单包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/tool/... ./pkg/mcp/... ./pkg/run/... -count=1` | ok |
| 按名 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run 'TestFileRead' -count=1 -v` | 全 PASS |
| 全量 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 所有包 ok |
| vet / fmt | `go vet ./...` / `gofmt -l cmd pkg third_party` | 无输出 |
| 前端 | `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 |
| 图包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1` | PASS |
| 真机探测 | `FOREBRAIN_E2E_DEEPSEEK_KEY=… CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/openai/ -run TestCompatToolResultImage -count=1 -v` | 两个子用例都 PASS（未设 key 时 SKIP） |

**房规**（`AGENTS.md`）：测试/脚本**不得继承环境里的 `FOREBRAIN_HOME`**（用 `t.Setenv` / 自命名变量）；
不建 worktree、不 commit；不新增依赖（本计划只用标准库 `image{,/png,/jpeg,/gif}`）；`plans/` 之外不改文档站。

## Scope

**In scope**：

- `assets/models.json`（**只有一处改动**：`deepseek/deepseek-v4-flash` 的 `attachment`/`modalities`，见 Step 4c；
  这是 owner 2026-10-14 确认的数据修正，不是"顺手改配置"）
- `pkg/llm/llm.go`、`pkg/llm/image_scale.go`（新建）、`pkg/llm/image_scale_test.go`（新建）、
  `pkg/llm/assets_test.go` 或 `catalog_test.go`（Step 4c 的数据完整性测试）
- `pkg/tool/file_tools.go`、`pkg/tool/file_tools_test.go`、`pkg/tool/format.go`、`pkg/tool/format_test.go`、
  `pkg/tool/state.go`（**只加**能力闸门的 setter/读取器；**不碰** `ProtectedReadReason`）、
  `pkg/tool/mcp.go`（MCP 结果转 parts）
- `pkg/mcp/bridge.go`（`SetResultsDir` + `callToolAttempt` 传结果根）、`pkg/mcp/result_processing.go`
  （`home` 参数改为结果根）、对应测试
- `pkg/run/runner.go`（`SetResultsDir` 一行 + 闸门注入）、`pkg/run/image_input.go`（新建闸门判定）
- `pkg/run/telemetry.go`（MCP round-trip 签名加宽）
- `pkg/mcp/result_processing.go`（+ 其测试）、`pkg/mcp/bridge.go`（+ 其测试）
- `pkg/gateway/api_extra.go`（取图端点 + 附件校验）、`pkg/gateway/files_write.go`（若需复用则只读）、
  `pkg/gateway/run_control.go`（附件闸门）、`pkg/gateway/*_test.go`
- `pkg/state/message_parts_test.go`（新增 tool 角色图片往返）
- `pkg/turn/input_parts.go`（附件闸门，与 web 同口径）
- `pkg/tui/input_events.go`（`hotkeyOpenImage` 常量 + `0x0f` 映射）、`pkg/tui/run.go`（两处分发）、
  `pkg/tui/render.go`（标记行）、`pkg/tui/tool_view*.go` 或渲染该行的地方、`pkg/tui/*_test.go`
- `frontend/src/components/chat/ToolCallCard.vue`（+ 测试）、`frontend/src/lib/api.ts`、`frontend/src/locales/*`
- `pkg/llm/openai/{compat_llm.go,responses_llm.go,tool_image_integration_test.go}`（只在 Step 6 要求时）
- `scripts/acceptance/read_image.sh`（新建）
- `scripts/acceptance/fake_mcp_stdio.py`（**扩展**：加一个参数让它返回 image content block——
  今天的 `tools/call` 只回文本，见 Step 9 断言 ⑤）

**Out of scope**（真的不相关，不是欠条）：

- PDF：那是 **054** 的内容（本期同一批次，依赖本计划 Step 1）。
- TUI 内联真像素（图形协议）：owner 2026-10-14 明确选定「标记行 + 按键打开」。
- 音频/视频/其它二进制；PDF 表单填写、数字签名；图片 OCR（模型自己看即可）。
- 在 TUI/web 里编辑图片、图片标注 UI。

## Git workflow

- 当前分支当前工作区直接改（owner 规则：禁止 worktree）。
- **不 commit、不 push**；改动留工作区由 owner 审阅。若 operator 明确要求：
  `git commit -s -F <file>`，标题 `fix(tool): hand the model the pixels of a read image`，
  正文按 `.gitmessage` 的 `Why:` / `What:` / `Verification:`。

## Steps

### Step 1: `pkg/llm` 的「预算内图片」编码器（054 也复用它）

在 `pkg/llm/llm.go` 里把 `detectImageMIMEType` 抽成导出的 `ImageMIMEType(path string, data []byte) string`
（规则不变：先嗅探前 512 字节，再按扩展名兜底），内部调用点同步改。

新建 `pkg/llm/image_scale.go`：

```go
const DefaultMaxImageBytes = 3 << 20      // 单图原始字节上限
const MaxImageDecodeBytes = 48 << 20      // 超过就不解码，直接报错
const ImageMaxBytesEnv = "FOREBRAIN_IMAGE_MAX_BYTES" // 十进制字节；解析失败/≤0 用默认（仿 pkg/llm/assets.go:43 的 AssetsDirEnv 写法）

type ImageReadResult struct {
	MIMEType                   string
	SourceBytes, SentBytes     int
	SourceWidth, SourceHeight  int
	Width, Height              int
	Scaled                     bool
	Scale                      int // 整数缩小倍数；1 = 原样
}

// ImagePartWithin 把 data 编成不超过 maxBytes 的图片 part；maxBytes<=0 用默认/环境变量。
func ImagePartWithin(path, mime string, data []byte, maxBytes int) (ContentPart, ImageReadResult, error)
```

**编码阶梯（顺序固定 → 同一输入恒等输出 → 提示前缀不抖）**：

1. 预算解析（见上）。
2. `len(data) > MaxImageDecodeBytes` → `"image is too large to read (%d MiB); resize it first"`。
3. `mime` 不在 `image/{jpeg,png,gif,webp}` → 交由调用方给可执行错误（Step 2 第 3 点）。
4. `len(data) <= budget` → **原样** `ImageBase64(mime, base64(data))`，`Scale: 1`；
   尺寸用 `image.DecodeConfig`（失败留 0，不算错误）。
5. 否则 `image.Decode`（blank import `image/gif`、`image/jpeg`、`image/png`），对 `s = 2,3,4,6,8`
   依次做**整数倍块平均**缩小（目标像素 = 源 `s×s` 块均值，边界取实际存在的像素），
   源 png/gif → PNG，其它 → JPEG q85；第一个不超预算的胜出。
6. 全超 → 最大那次缩小改 JPEG q75；仍超 → `"image is %d MiB and cannot be reduced below %d MiB; crop or resize it first"`。
7. webp 且超预算（标准库无 webp 解码器）→ `"image is %d MiB and webp cannot be downscaled here; convert it to png first"`。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ -run 'TestImage' -count=1` → PASS

### Step 2: `read_file` 把像素交出去

改 `pkg/tool/file_tools.go`：

1. handler 返回类型 `(string, error)` → `(any, error)`（`llm.NewTool` 的 `R any` 支持；
   文本路径仍 `return out, nil` 返回 `string`，既有 `got.(string)` 断言继续成立）。
2. `os.ReadFile` 成功之后、`validateTextReadableFile` **之前**加图片分支：

```go
// 图片不是文本：它有自己的通道——像素直接进模型，文字只留一行说明。
// 图片不记账本、不通知 ReadObserver（见第 5 点）。
part, res, err := imageReadPart(abs, raw, llm.DefaultMaxImageBytes)
if err != nil {
	return nil, err // 是图片但读不了：给可执行错误，不退回文本拒读
}
if res.MIMEType != "" {
	CaptureToolOutput(ctx, imageReadOutput(abs, in.FilePath, res))
	return []llm.ContentPart{llm.Text(imageReadNote(abs, res)), part}, nil
}
// res.MIMEType == ""：不是图片，继续走下面的文本路径，一行都不要改。
```

3. `imageReadPart(abs, raw, budget) (llm.ContentPart, llm.ImageReadResult, error)`：
   ① `mime := llm.ImageMIMEType(abs, raw)`；② 受支持四种 → `llm.ImagePartWithin(abs, mime, raw, budget)`，
   错误原样上抛；③ `mime == ""` 且 `isImageFileExtension(ext)`（`.bmp/.heic/.tiff/.avif/.ico`）→
   `"read_file cannot decode %s images; convert it to png or jpeg first (macOS: sips -s format png <path>)"`；
   ④ 其余 → 零值（不是图片）。
   配套：把 `validateTextReadableFile` 里那段 `isImageFileExtension` 分支**删掉**（图片已在上游处理，留着就是
   不可达分支）；`.pdf`、二进制扩展名、二进制内容的拒绝**不动**。
4. `imageReadNote(abs, res)` 给模型看的一行，**必须带尺寸与是否缩放**（模型要算坐标）：
   `image 1512x982 (scaled 1/2 to fit 3 MiB)` / `image 3024x1964`，前面是绝对路径。
5. `imageReadOutput(abs, filePath, res)` 给 surface 的 map，**绝不含 base64**：

```go
map[string]any{
	"preview_kind": "image", "preview_text": "", "abs_path": abs, "file_path": filePath,
	"image_mime": res.MIMEType, "image_bytes": res.SentBytes, "image_source_bytes": res.SourceBytes,
	"image_width": res.Width, "image_height": res.Height, "image_scaled": res.Scaled,
	"result_lines": 0,
}
```

6. 图片**不要**调用 `st.RememberRead`、**不要**通知 `st.ReadObserver()`（那是「文本读过了、可以改它」的
   账本与 LSP 索引入口；照 `file_tools_test.go:203` 那条既有断言的思路）。
   **闸门（Step 4）在 Step 4 才接到这个分支上**；未安装钩子时 `State.ImageInputRefusal(ctx)` 返回 `""`（放行），
   所以本步可以独立落地并自测，不会因为 Step 4 还没做而失败。
7. read_file 的模型可见描述（`file_tools.go:53`）**逐字**改成：

```go
"Use when you need a file's contents, or before any write/edit (required first). Read a text file with a 0-based offset and limit, or an image (png/jpeg/gif/webp) as an image. Read the smallest useful span."
```

（`registry.go:127` 的短描述不含 "text file"，不改。改这一句会让提示前缀一次性移位——**这是有意的产品改动**。）

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run 'TestFileRead' -count=1 -v` → 旧用例全 PASS

### Step 3: 工具卡与 TUI 文案只显示一行

改 `pkg/tool/format.go`：

1. `formatReadFileStep`（`:509`）：在取 `preview_text` 之前先看 `Output["preview_kind"] == "image"`，
   是则返回一行：`` `path: <abs> · image <W>x<H> · <bytes>[ · scaled]` ``，
   **绝不**落到下面 `json.MarshalIndent(evt.Output)` 的兜底。
2. `SummarizeToolStep`（`:262`）：在 read_file 分支之前加图片分支 → `Read shot.png · image 1.4 MB`
   （复用 `formatBytes`（`:2899`）与 `pathBaseOrPath`（`:2965`）；路径提示见 `readFilePathHint`（`:542`））。
3. 其它工具渲染不变；没有图片标记的 read_file 行为**保持不变**。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run 'TestSummarize|TestFormatToolStep' -count=1` → 全 PASS

### Step 4: tool 角色的图片往返 + 能力闸门

**4a. 回归护栏**（`pkg/state/message_parts_test.go`，照 `:106` 那条写，但消息是工具结果）：

```go
msg := llm.ToolResultMessage("call-1", llm.Text("image 4x4"), llm.ImageBase64("image/png", "iVBORw0KGgo="))
partsJSON := MessagePartsJSON(msg, msg.TextContent())
parsed, ok := ParseMessage(llm.RoleTool, msg.TextContent(), partsJSON)
// 断言 ok、ToolCallID == "call-1"、Parts 里有 ContentTypeImageBase64 且数据原样
```

**4b. 闸门**——新增 `pkg/run/image_input.go`：

```go
// ImageInputRefusal 返回不允许把图片交给本会话模型的原因；允许时返回 ""。
// fail-open：catalog 不认识这个模型（例如自定义 provider）时不拦。
func (r *Runner) ImageInputRefusal(ctx context.Context) string
```

实现（**照抄这个口径，不要自己发明**）：

1. **取本会话的模型**：`sel, ok := SessionModelSelectionFromContext(ctx)`（`pkg/run/config.go:1519`）。
   **注意它可能不在 ctx 里**——`injectSessionModelSelection`（`config.go:1640`）只在会话**有存储行**时才
   写入 ctx，而它唯一的调用点是运行入口 `pkg/run/runner.go:1381`。没有 ctx 选择时用运行时的当前选择：
   `sel = PrimaryModelSelection(r)`（`config.go:1464`，包级函数，收 `*Runner`）。两条路都取不到 →
   返回 `""`（放行）。
2. **查 catalog**：`hit, ok := llm.Lookup(sel.Provider, sel.Model)`（`pkg/llm/catalog.go:327` ——
   **带 provider 的那个**，内部走 `JoinProviderModel` 拼成 `provider/model` 查全局表；
   `InitGlobalCatalog` 在启动时装好：`pkg/process/runtime.go:104`、`pkg/process/seed.go:22`）。
   **不要用 `LookupModel(model)`**（`catalog.go:180`）：它忽略 provider、还会按短名后缀兜底匹配，
   能把另一个 provider 的同名模型当成依据。
3. `!ok`（catalog 不认识这个模型，例如自定义 base_url + 自定义模型名）→ `""`（**fail-open**）。
4. `ok && !hit.SupportsAttachments` → 返回一句可执行文案，例如
   `"the session model <provider>/<model> does not accept image input; switch model with /model, then read the image again"`。

**这条闸门必须知道的一个事实（review+owner 2026-10-14，直接决定验收怎么跑）**：
`assets/models.json:1023` 的 `deepseek/deepseek-v4-flash` 现在写的是 **`"attachment": false`、
`"modalities": {"input": ["text"]}`**，但 **owner 已确认这是错的**：DeepSeek 官方会把
`deepseek-v4-flash` 路由到 `deepseek-flash`，**`deepseek-v4-flash` 实际能理解图片**。

也就是说：**闸门按现在的数据会把 owner 的默认模型拦掉**（读图直接给拒绝文案），
这是"数据错→闸门错"的典型，本步骤必须连带把它修对（Step 4c），否则功能在 owner 的机器上等于没做。
`deepseek/deepseek-v4-flash-vision-exp`（`assets/models.json:1519`，`attachment: true`、`input: ["text","image"]`）
是同一条 lane 的另一条已知收图条目，可作交叉验证。

**反例（owner 2026-10-14 同日补充，恰好证明闸门不能拆）**：`deepseek/deepseek-v4-pro`
（`assets/models.json:1129`）**是纯文本模型、不能识图**，而 catalog 里它本来就是
`"attachment": false`、`input: ["text"]` —— **这条数据是对的**，且它在 owner 的
`forebrain.yaml` 主代理 provider 列表里**可以直接选**（`/model` 能切到它）。
所以两个事实合起来给出本步骤的完整口径：

| 模型 | catalog 现状 | owner 事实 | 期望行为 |
|---|---|---|---|
| `deepseek/deepseek-v4-flash` | `attachment: false`（**错**） | 官方路由到 `deepseek-flash`、**能识图** | 改数据（Step 4c）→ 放行 |
| `deepseek/deepseek-v4-pro` | `attachment: false`（**对**） | **纯文本、不能识图** | 闸门**拒绝**并给可执行文案 |
| `deepseek/deepseek-v4-flash-0731` | `attachment: false`（**对**） | **纯文本**（owner 2026-10-14 确认） | 闸门**拒绝**；**不要**改这条 |
| 未知模型（自定义 base_url/名） | 无此项 | —— | fail-open 放行 |

原则（这条要写进代码注释与维护说明）：

- 闸门本身**保持**（它的价值是"真·纯文本模型上给出可执行提示，而不是让请求 400"），
  `deepseek-v4-pro` 就是它存在的理由，**不要**为了让某个模型能用就把它改成"永远放行"——那等于没有闸门；
- **数据错了就改数据**（Step 4c），不要改判定逻辑去迁就过期数据；
- catalog 是**可能会滞后**的静态表：一次错误的 `attachment: false` 与一次正确的无法区分，
  所以拒绝文案必须是**一步可恢复的**（点名模型 + 指路 `/model`），
  且维护说明里要写下"用户报'读不了图'时先查他的模型在 catalog 里的条目"这条排查顺序。

在 `pkg/tool/state.go` 加 setter（**照 `SetReadObserver`（`:1890`）的写法**）：
`func (s *State) SetImageInputGate(h func(ctx context.Context) string)` + 一个 `ImageInputRefusal(ctx)` 读取器
（未安装钩子 → 返回 `""`）；
在 `pkg/run/runner.go` 的实例化区（`:1088-1265` 那一串 `r.tools.SetXxx`）注入 `r.ImageInputRefusal`。
消费点三处（**本步骤把闸门接到既有分支上**）：`imageReadPart` 的入口（Step 2 第 3 点调用之前）、
MCP 的图片转换（Step 5）、以及附件路径（Step 8：web 上传与 TUI `@` 图片）；
三处都在拒绝时返回该文案而不是像素。`State` 里没装钩子 → 一律放行（这也是 Step 2/5 能先落地的前提）。

> **4a 的代码块是在测试里构造消息**，不是生产代码；`ParseMessage` 的签名是
> `ParseMessage(role, content, partsJSON string) (llm.Message, bool)`（`pkg/state/message_parts.go:312`），
> 第一个参数是角色字符串 `llm.RoleTool`。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ ./pkg/run/ -count=1` → PASS；
再跑 Step 4b 新增的两条（Test plan 19/20：catalog 里 `attachment:false` 的模型 → 拒绝；未知模型 → 放行）
——`pkg/llm.SetGlobalCatalogForTest`（`catalog.go:381`）是装测试 catalog 的既有入口。

### Step 4c: 把 catalog 里错的条目改对（owner 2026-10-14 确认）

1. `assets/models.json`（**唯一权威副本**；`build/bin/models.json` 是构建产物，不要手改）：
   把 `deepseek/deepseek-v4-flash`（第 1023 行起）的
   `"attachment": false` → `"attachment": true`，
   并把 `"modalities": {"input": ["text"]}` → `["text", "image"]`。
   **只改这一条**，其余三条同 family / 同 provider 的条目**一律不要动**——owner 2026-10-14 已逐条确认它们
   现在都是对的（见 Step 4 的表）：`deepseek-v4-pro` 与 `deepseek-v4-flash-0731` 都是**纯文本模型**，
   它们的 `attachment: false` 保持原样。

   注意这里有一个**不能靠 family 推断**的坑（写进 Maintenance notes）：
   `deepseek-flash` 这个 family 里既有能识图的 `deepseek-v4-flash`，也有纯文本的 `deepseek-v4-flash-0731`。
   所以**永远不要**用 family 批量推定 `attachment`，能力是**逐模型**的。
2. `pkg/llm` 加**两条**数据完整性测试（用现成的 `RepoModelsJSONForTest`，`pkg/llm/assets.go:133`）：

```go
// 产品文档与 owner 的默认配置都把这个模型当多模态用；数据表滞后一次就够痛的了（2026-10-14）。
func TestCatalogMarksTheOwnerDefaultAsMultimodal(t *testing.T)
// 断言 assets/models.json 里 deepseek/deepseek-v4-flash 的 attachment 为 true 且 input 含 image

// 同 provider / 同 family 的纯文本条目保持 false——owner 逐条确认过，别让后人"顺手统一 family"。
// （deepseek-flash 这个 family 本身就同时含多模态与纯文本成员，family 不能作为能力代理。）
func TestCatalogKeepsTextOnlyDeepseekModelsAsTextOnly(t *testing.T)
// 断言 deepseek/deepseek-v4-pro 与 deepseek/deepseek-v4-flash-0731 的 attachment 为 false 且 input 不含 image
```

3. **知道修复的生效路径**（写进报告）：`assets/models.json` 会被 `llm.Install`（`catalog.go:435-438`）
   **只在新 home 里播种、永不覆盖已存在的 home 副本**（用户自定义被保留）；
   无本地 catalog 的发行包按 tag 从 `assets.go:153` 的 URL 下载。所以：
   - 验收必须用**全新 scratch home**（Step 9 就是，见其 home 规则）；
   - **已经装了旧 home 的机器不会自动变**：报告里要写明"现有 home 保留旧副本（设计如此），
     需要时删除 `<home>/models.json` 让下次启动重新播种"——**不要**为此改 `Install` 的覆盖语义。



### Step 5: MCP 工具结果的图片进模型

1. `pkg/mcp/result_processing.go`：把「替换成一行文本」改成「**既**落盘（保留审计与 `retrieve_output` 的既有行为）
   **又**把图片作为 part 交出去」。新增：

```go
// ToolResultContent 返回这次工具结果要交给模型的内容：文本 envelope（图片块换成一行路径说明，
// 与今天一致）+ 每个图片块一个 image part（受 llm.DefaultMaxImageBytes 预算，超出按 Step 1 缩小或报错）。
//
// 第一个参数是**结果根**（Step 5b 起由 Session 注入），不是 home；本步骤先按收到的目录用，
// Step 5b 把调用点改成注入值——两步之间不要出现第二处路径拼接。
func ToolResultContent(resultsDir, serverName, toolName, raw string, acct accounting) ([]llm.ContentPart, error)
```

   `Session.CallToolJSON`（`bridge.go:981`）旁新增
   `Session.CallToolContent(ctx, toolName string, args any) ([]llm.ContentPart, error)`：
   复用 `callToolAttempt` 的重连/超时/错误语义（`bridge.go:1009-1033`），把最后两步换成 `ToolResultContent`。
   **`res.IsError` 的行为要与 `callToolAttempt` 一致**（`bridge.go:1030-1032`：把行文本当错误返回），
   否则 MCP 工具的失败会从"报错"退化成"看起来成功"。
   `CallToolJSON` 保留（discovery 工具仍用文本）。
2. `pkg/run/telemetry.go:128-142`：`mcpToolCaller` 的签名加宽为
   `func(ctx, toolName, arguments) (any, error)`，内部改调 `CallToolContent`；
   `pkg/run/runner.go:345-358 newMCPDelegatingTool` 的 `call` 参数同宽（handler 本来就返回 `any`；
   `newMCPDelegatingTool` 只在 `run.go:2534` 被调用一次）。
   `mcpDiscoveryCaller`（`:144+`）保持 `string`，不要动。
3. 图片 part 的产出必须**受 Step 4 的闸门约束**：闸门拒绝时只回文本（envelope 里那行
   `Binary content saved to <path>` 保留），不让请求 400。
4. MCP 结果里的 base64 **不进** tool 步骤事件的 `Output`（事件是 UTF-8 JSON、会被 TUI/web 渲染）；
   事件里只留 `preview_kind: "image"`、路径、MIME、尺寸、字节数（与 Step 2 的 map 同形）。
5. `processToolResultJSON` 里那两个守卫 `strings.TrimSpace(home) == ""`（`result_processing.go:48`）
   与 `largeOutputFilesEnabled()` 要跟着改名/改判到结果根上（留 `home` 字样会让 Step 5b 之后
   这个判断永久为真/为假而没人发现）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/mcp/... ./pkg/run/... -count=1` → 全 PASS（含既有 MCP 测试）

### Step 5b: mcp-results 目录搬进主代理 workspace（owner 2026-10-14 裁决）

**owner 原话**：「全局 MCP、主代理 MCP 和项目级 MCP 的 mcp-results 目录都必须放在主代理的 workspace 目录范围内。」

**问题（源码核实，这是 Step 5 的另一半，不做这步 MCP 图片只解决一半）**：

MCP 的图片/二进制结果今天落在 `filepath.Join(home, "state", "mcp-results")`
（`pkg/mcp/result_processing.go:178`，文件 `0600`、目录 `0700`，`home` = `$FOREBRAIN_HOME`）。
它在 FOREBRAIN_HOME 内、但**不在主代理 workspace 内**，于是 `read_file` 走 `authorizeRead`
（`pkg/tool/file_tools.go:150-190`）会命中 `ProtectedReadReason`（`pkg/tool/state.go:2086`）——
它的豁免清单只有三项（主工作区、已加载技能根、已批准写过的文件），`state/mcp-results/<file>` 不在其中：

- 交互式会话：弹一次审批（「Reading … reads forebrain's own settings and state under FOREBRAIN_HOME,
  which can hold credentials.」）；
- 无人可问的 run（embedder 无 `ActionHook`）：**直接拒**（`ErrPathNotAllowed`）。

模型仍会拿到这个路径的**三种**情形（所以不能只靠 Step 5 的 parts 绕过去）：

1. Step 5 第 3 点：闸门拒绝（会话模型不收图）时只回文本 → 文本里就是 `Binary content saved to <path>`；
2. `processToolResultJSON` 的 offload 分支（`result_processing.go:48-59`）：结果过大时把整个 envelope
   转存成 `state/mcp-results/mcp-…json`，返回「Large MCP result (… characters) saved to <path>.
   Read this file if you need the full output」——**这行字就是在叫模型去 `read_file`**；
3. 后续轮次重看早先的截图：路径在 transcript 里，模型会再次 `read_file` 它。

**改法（owner 选的方案：搬目录，**不动** `ProtectedReadReason`）**：

主代理 workspace **本来就是**读豁免根，所以把结果目录搬进去，审批这一环自然消失——
豁免清单一个字都不用改（这也正是 owner 这条裁决的用意：不要靠加豁免去放开 FOREBRAIN_HOME）。

1. **目标位置**：`<主代理 workspace>/state/mcp-results/`。这与同类「模型产出」目录完全对齐：
   `stateDir := strings.TrimSpace(r.StateDir)`，为空则 `filepath.Join(r.workspaceRoot(), "state")`
   （`pkg/run/runner.go:1071-1074`），而 `SetHistoryDir(<stateDir>/file-history)`（`:1089`）、
   `SetToolResultDir(<stateDir>/tool-outputs)`（`:1090`）都落在这里；
   真机上 `~/.forebrain/workspace/state/` 已有 `tool-outputs`、`file-history`、`intermediate`、`mcp`（同意记录）等同级目录。
2. **三层都进同一个目录（owner 明确要求）**：全局 / 主代理 / 项目级 MCP 的**结果**一律写主代理 workspace。
   不要因为「项目级 MCP 属于项目」就写进项目根：项目根是用户的代码目录（不该沉积工具产出），
   而且一份结果一个归属地才能一处可查、一个豁免根覆盖。
   注意项目级 MCP 的 **stdio 工作目录**仍是项目根（`mcpServerWorkingDir`，`runner.go:1855-1862`，`t.dir`）——
   **工作目录**与**结果落盘目录**是两件事，别把 `t.dir` 当结果根。
3. `pkg/mcp`：把结果根从 `home` 换成可注入的 `resultsDir`（照 `SetStateRoot` 的既有写法，`bridge.go:70-78`）：
   - `Session.resultsDir string` + `func (s *Session) SetResultsDir(dir string)`（与字段同锁）；
   - `callToolAttempt`（`bridge.go:1015`）把 `s.resultsDirValue()` 传给新的
     `processToolResultJSON(resultsDir, serverName, toolName, raw, acct)`；
   - `persistBinaryResourceContent` / `persistMCPResult` 的 `home` 参数改名为 `resultsDir`，
     `filepath.Join(strings.TrimSpace(resultsDir), "state", "mcp-results")`
     → 直接把收到的目录当**根**用（`filepath.Join(resultsDir, …)`），避免两处各拼一次路径；
   - 未设置时回退 `filepath.Join(home, "state", "mcp-results")`（现行为，给不经 Runner 的嵌入者），
     注释写明「生产路径总是显式设置」。
4. `pkg/run/runner.go:2357` 那一行旁边照 `SetStateRoot` 加一句：
   `cli.SetResultsDir(filepath.Join(ld.r.StateRoot(), "state", "mcp-results"))`
   （`r.StateRoot()` = `r.workspaceRoot()`，`runner.go:3179-3181`，就是主代理 workspace）。
5. **不要**动 `ProtectedReadReason`、`PathUnderPrimaryWorkspace`、`SetPrimaryWorkspaceBoundary`。
   顺带说明为什么这一条自动覆盖**项目**会话：`activeWorkspace := r.workspaceRoot()`（`runner.go:1079`）
   对项目会话同样是主代理 workspace（项目根是 `ProjectRoot` 那条独立的 allowed root），
   所以 `PathUnderPrimaryWorkspace(<agent workspace>/state/mcp-results)` 恒为真。
6. **旧目录**（`$FOREBRAIN_HOME/state/mcp-results/`）**不写迁移**：那里是**瞬时工具产出**
   （每次调用按时间戳新建文件），改动后没有任何代码再读它，也没有任何索引指向它——
   写迁移只会给一个缓存目录增加永久代码。计划要求：README/报告里写一句「owner 可自行删除该目录」；
   但**已经存进 transcript 的旧路径**（早先那条 `Binary content saved to <旧路径>`）如果文件还在，
   模型去读仍会走审批——这是可接受的（一次性），不要为它加豁免。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/mcp/... ./pkg/run/... ./pkg/tool/ -count=1` → PASS
（见 Test plan 15/17/24/28–30：结果落进注入的目录；**项目**会话下读它**不触发**审批；home 下别的文件仍触发）

### Step 6: 问 provider 一个问题——工具消息里能不能带图

**只写测试、读结论**，先不改 provider 代码。新建 `pkg/llm/openai/tool_image_integration_test.go`
（env-gated：`FOREBRAIN_E2E_DEEPSEEK_KEY` 未设 → `t.Skip`；key 由 operator 以环境变量提供，
`~/.forebrain/e2e-zhipu.env` 那条既有惯例，chmod 600，**永不提交、绝不写进计划或日志**）：

**两件事一起探，做成一个表驱动测试（两个子用例）：**

| 子用例 | base_url | model | 探什么 |
|---|---|---|---|
| a（**主**） | `https://api.deepseek.com` | `deepseek-v4-flash`（owner 默认；官方路由到 `deepseek-flash`，owner 已确认收图） | 确认它可以收图 → 这就是 Step 4c 那次数据修正的证据 |
| b（交叉） | `https://api.deepseek.com` | `deepseek-v4-flash-vision-exp`（`assets/models.json:1519`，`attachment:true`） | 同一条 lane 的已知收图条目，**同一条请求形状**它必须答对 |

每个子用例都构造真实请求：`assistant` 带一个 `tool_call`，其后是**工具结果消息**
`[llm.Text("image 64x64"), llm.ImageBase64("image/png", <测试里现生成的纯红 64×64 PNG>)]`，
再一条 user 消息 `"What colour is the shape in that image? Answer with one word."`；
断言请求成功（无 400）且回复含 `red`/`红色`。

**结论有三种，按实测填（A 是预期结果）：**

- **A：两个都接受**（**预期结果**）→ Step 4c 的数据修正被实测证实；把证据（请求没被拒 + 回复复述出颜色）
  写进报告。Step 9 用 `deepseek-v4-flash`（owner 的真实默认）跑验收。
- **B：只有 vision-exp 接受、`deepseek-v4-flash` 被拒**→ **停下报告**：这与 owner 2026-10-14 的说明矛盾
  （他说官方会路由到 `deepseek-flash` 且能理解图片）。**此时不要**把 Step 4c 的改动留着——
  它没有证据支撑；把两个子用例的原始响应（状态码 + 服务端原文）交给 owner 判定，
  并由 owner 决定 catalog 到底该写什么。
- **C：两个都被 400/422 拒** → 按下面改，**只在发往线上的投影里**翻译、保持「每条消息只由它自己决定长什么样」
  （`compat_llm.go:1049-1060` 的前缀缓存铁律）：

  - `compat_llm.go:1059 compatMessagesToOpenAI`：`RoleTool` 且含非文本 part → 投影两条：
    ① 工具消息 `Content = llm.TextContent(parts...)`；② 紧跟一条 `role:"user"`，内容 = 那些图片 part，
    前面加 `llm.Text("image read from tool result")`。
  - `responses_llm.go:683`：同样拆两条（第一条 `FunctionCallOutput` 用文本，
    第二条 `EasyInputMessageRoleUser` + `openAIResponsesUserContent(图片 parts)`）。
    **今天这条路径会把 tool 消息里的图静默丢掉**（`responses_llm.go:683-689` 只取 `TextContent()`），
    无论结论是 A 还是 B，都要在报告里记一笔（owner 的 chatgpt 主代理走 Responses）。
  - Anthropic **不改**（`agent_llm.go:368-398` 已支持）。

**Verify**: 集成测试 PASS，日志（是否 400、回复原文）贴进报告

### Step 7: surface —— web 缩略图、TUI 标记行与 `Ctrl+O`

**7a. 取图端点**（`pkg/gateway/api_extra.go`，`workspace` 组里加一条）：

```
workspace.Get("/file/raw", s.handleWorkspaceFileRaw)   // GET /v1/workspace/file/raw?session_id=&project_id=&path=
```

必须复用 `s.resolveFilesPath`（`files_write.go:41`）解析根与包含性；只服务图片 MIME
（用 `llm.ImageMIMEType` 判断），设 `Content-Type`、`Cache-Control: private, max-age=60`，
大小上限用 `llm.DefaultMaxImageBytes * 2`（超限 413，提示改成看正文摘要）；非图片 415。

**7b. web**：`frontend/src/components/chat/ToolCallCard.vue` —— `preview_kind === "image"` 时渲染
`<img :src="rawUrl" loading="lazy">`（缩略图高度上限约 160px，点击在新标签打开），
`alt` 用文件名与尺寸；`frontend/src/lib/api.ts` 加 `workspaceFileRawUrl(...)`；
文案进 `locales/`（中英双份）。**绝不**把 base64 塞进 API 响应或 DOM。

**7c. TUI（review 2026-10-14 修正：**不要**用裸 `o`）**

- **为什么不用裸字母**：TUI 的按键词汇表里**没有任何裸字母快捷键**——`pkg/tui/input_events.go:51-74` 的
  `inputHotkey` 常量全部由控制键或覆盖层键产生，裸键 `switch b` 里映射的是
  `0x02/0x03/0x09/0x0b/0x0c/0x16/0x18/0x1b`（`input_events.go:1325-1370`）。裸 `o` 会被 composer 吃掉，
  写成裸键等于"在输入框里打 o 就弹图片"。
- **用 `Ctrl+O`（`0x0f`）**：`0x0f` 目前**未被占用**（`grep -o "case 0x[0-9a-f][0-9a-f]:" pkg/tui/input_events.go`），
  与现有"全控制键"约定一致，且不抢字符。加常量 `hotkeyOpenImage inputHotkey = "open_image"`。
- **目标**：TUI **没有**"选中的步骤"这个概念（`grep -rn "focusedStep\|selectedStep" pkg/tui/` 为空），
  所以行为定义为「打开**本会话最近一次**图片/PDF 步骤的绝对路径」；没有这样的步骤时**只提示一行**、
  不 spawn 进程。
- **接线点（三处，都要改）**：
  1. 常量：`pkg/tui/input_events.go:51-74`；
  2. 产生：同一个 `switch b` 里、`case 0x0c:`（Ctrl+L → `hotkeyRedraw`，`input_events.go:1355-1362`）旁边加
     `case 0x0f:`（照它的 `flushBeforeHotkey()` 写法）；
  3. 分发：`grep -n "case hotkeyRedraw:" pkg/tui/run.go`（当前是 `:1400` 与 `:4635`）**每一处**都加
     `case hotkeyOpenImage:`——漏掉任一处就会出现"空闲时能用、跑动时不能用"。
- 打开方式照 `pkg/tui/tty_session.go:659` 那类 `exec.CommandContext`（macOS `open`、Linux `xdg-open`、
  Windows `start`），失败只提示不崩。
- 标记行文案用实际按键：`▤ shot.png · 1512×982 · 1.4 MB · Ctrl+O 打开`。

**Verify**: `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` 全绿；
`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ ./pkg/tui/ -count=1` PASS（新端点与按键各有用例）

### Step 8: 用户上传/@ 图片通道的回归护栏（今天就是通的，钉住它）

这条通道本来就通（`pkg/turn/input_parts.go:19-49 SurfaceTurnContentParts`、
`pkg/gateway/run_control.go:497-536 prepareWebTurnInput`、`pkg/run/transcript.go:277-340
rehydrateImageReferences`、TUI `pkg/tui/run.go:600`），本步骤**只加测试**，不重写：

- `pkg/gateway`: 上传一张 PNG → 请求体里出现 `FileReferencePartJSON(...)`（image/* 分支）而不是
  `AttachmentPartJSON`（`run_control.go:530-535`）；
- `pkg/run`: 回放时 `UserInputParts` 把 `file_reference` 还原成 `ContentTypeImageBase64`；
- 附件闸门（Step 4）：会话模型不支持图片时，**上传**也被拒并给同一句提示（web 与 TUI 同口径）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ ./pkg/run/ ./pkg/turn/ -count=1` → PASS

### Step 9: 真机端到端验收

新建 `scripts/acceptance/read_image.sh`（骨架照 `scripts/acceptance/web_e2e.sh`：`set -euo pipefail`、
`trap cleanup`、`pkill -f "$BIN"` 只清自己的路径）：

- home 由脚本自己命名：`HOME_DIR="${FOREBRAIN_ACCEPT_HOME:-$(mktemp -d)/home}"`，
  **绝不读 `FOREBRAIN_HOME`、不用 `$HOME` 兜底**（房规；2026-10-14 真实 home 事故的教训）。
- 用 python3 生成一张 `1200x600` 的 PNG：白底、左边红色实心圆、右边蓝色实心方块 → `$WORK/shape.png`。
- **模型由 Step 6 的实测结论决定，脚本里必须显式写出来并在输出里回显**：
  `FOREBRAIN_E2E_MODEL="${FOREBRAIN_E2E_MODEL:-deepseek-v4-flash}"`（owner 的真实默认模型），
  配 `provider: deepseek` / `base_url: https://api.deepseek.com`，**并且用全新 scratch home**
  （这样才会播种 Step 4c 修正后的 catalog；现有 home 保留旧副本，见 Step 4c 第 3 点）。
  脚本首行把选用的模型与 home 打印出来。
  这条验收同时证明三件事：图片能读、catalog 修正生效、闸门不再拦 owner 的默认模型。
- 真模型 + 真 TUI：`CGO_ENABLED=1 go build -tags fts5 -o "$BIN" ./cmd/forebrain`，
  写 scratch `forebrain.yaml`（key 用 `${FOREBRAIN_E2E_*_KEY}` 引用），
  tmux 起 `FOREBRAIN_HOME=$HOME_DIR` 的 TUI，输入：
  `read_file $WORK/shape.png 然后告诉我图里有哪些图形、颜色、各自在左右哪边`。
- 断言：① 渲染出的步骤行含 `image`（Step 3 的摘要）；② 回复同时提到红/蓝与左右；
  ③ 库里那条工具消息 `parts_json` 含 `"type":"image"`（`sqlite3` 查 `fb_messages`，只打印片段）；
  ④ 按 `Ctrl+O` 后（有 `open` 的机器）不报错、TUI 不崩；在输入框里打字母 `o` **不得**触发打开；
  ⑥ **闸门的真机正证**：用 `/model` 切到 `deepseek-v4-pro`（owner 配置里可选、纯文本），
  再 `read_file` 同一张 `shape.png` → 必须看到**那句可执行文案**（点名模型 + 指路 `/model`），
  而**不是** provider 的 400/500，也不是"读到了但模型答不出颜色"；TUI 不崩。
  这条是"闸门在该拦的时候真的拦住了"的机器化证明（与断言 ② 的"该放行时放行"成对）。
  ⑤ **MCP 落盘位置与免审批**（Step 5b）：**先扩展** `scripts/acceptance/fake_mcp_stdio.py`——
  今天的 `tools/call` 只回 `{"type":"text"}`（第 49-57 行），要加一个参数（例如 `image: true`）
  让它回 `{"type":"image","data":"<base64 png>","mimeType":"image/png"}`（**就是
  `resultJSONContainsImage`/`containsImageBlock` 认的那个形状**，`result_processing.go:99-109`）。
  然后在 scratch home 里配一个指向该 fixture 的项目级/全局 MCP server，让模型调用它，断言：
  ① 新增的图片文件出现在 `$HOME_DIR/workspace/state/mcp-results/` 下、**不**出现在
  `$HOME_DIR/state/mcp-results/`；② 模型 `read_file` 那个路径时**不出现任何审批提示**
  （交互式会话里审批会渲染成可见的卡片/提示行）——这正是 Step 5b 的效果。
- 失败时 `tail` 出 gateway/TUI 日志。

**Verify**: `FOREBRAIN_E2E_DEEPSEEK_KEY=<operator 提供> scripts/acceptance/read_image.sh` → 打印 `read image: PASS`
（key 由 operator 以环境变量提供，chmod 600，**永不提交、绝不写进计划或日志**）

### Step 10: 全量闸门 + 索引

1. `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → 所有包 ok
   （唯一已知既有失败 `pkg/mcp` 的 `TestStartStdioConnectsRealMCPServerProcess`；出现**别的**失败 → 停下报告）。
2. `go vet ./...`、`gofmt -l cmd pkg third_party`、`./pkg/architecture` 全干净。
3. 更新 `plans/README.md` 状态行，并在「最新审核信息」记：**Step 4c 的 catalog 修正与它的实测证据**、
   Step 6 的结论（A/B/C 哪种）、Step 9 的 PASS 证据、改动文件清单、遗留
   （若 Step 5 因闸门只回文本，写清触发条件；不再有 catalog 开放问题——
   `deepseek-v4-pro` 与 `deepseek-v4-flash-0731` 的能力已由 owner 逐条确认，两条都保持 false）。

## Test plan

`pkg/llm/image_scale_test.go`（图片**在测试里现生成**：`image.NewRGBA` + `png.Encode`，不提交二进制 fixture）：

1. `TestImageMIMETypeSniffsAndFallsBack`：png+`.png`→`image/png`；png+`.bin`→`image/png`（嗅探优先）；
   文本+`.png`→`image/png`（扩展名兜底）；文本+`.txt`→`""`。
2. `TestImagePartWithinKeepsSmallImagesVerbatim`：1 KB png、预算 1 MiB → `Scale==1`、`SentBytes==len(data)`、
   base64 与原 data 相等。
3. `TestImagePartWithinScalesDownOversizeImages`：2000×2000 噪点 PNG + 刚好放不下的预算 →
   `Scaled==true`、`SentBytes<=预算`、报告 W/H 与解码后的图一致（防坏图）。
4. `TestImagePartWithinIsDeterministic`：同一输入两次 → `ImageBase64` 逐字节相同。
5. `TestImagePartWithinRejectsUnsupportedMime`：`ImagePartWithin(p,"image/bmp",…)` → 错误含 `convert`。
6. `TestImagePartWithinRejectsAbsurdSize`：> `MaxImageDecodeBytes` → 错误含 `too large`。
7. `TestCatalogMarksTheOwnerDefaultAsMultimodal`（Step 4c；用 `RepoModelsJSONForTest`，`pkg/llm/assets.go:133`）：
   `assets/models.json` 里 `deepseek/deepseek-v4-flash` 的 `attachment` 为 true 且 `input` 含 `image`；
   同一条测试文件里加 `TestCatalogKeepsTextOnlyDeepseekModelsAsTextOnly`：`deepseek/deepseek-v4-pro` 与
   `deepseek/deepseek-v4-flash-0731` 仍为 `attachment: false`、`input` 不含 `image`
   （防"顺手统一 family"的回归）。

`pkg/tool/file_tools_test.go`（照 `:290` 断言结果、`:534` 用 `WithToolCompletionCapture`）：

8. `TestFileReadReturnsImagePartsForImageFile`：4×4 PNG → 结果是 `[]llm.ContentPart`，含一个文本（带尺寸）与一个
   `ContentTypeImageBase64`（mime `image/png`，解码后等于文件字节）。
9. `TestFileReadImageDoesNotEnterReadLedger`：`st.GetReadState` 不存在且 `SetReadObserver` 未被调用。
10. `TestFileReadImageCapturesSurfaceOutputWithoutPixels`：`Output` 有 `preview_kind=image`、尺寸、字节数，
   且 `json.Marshal(Output)` **不含** `iVBOR`。
11. `TestFileReadUndecodableImageExtensionIsActionable`：`.bmp` → 错误含 `png`/`convert`。
12. `TestFileReadStillReadsText`（既有）保持 PASS。

`pkg/tool/format_test.go`（照 `:14`/`:369`/`:474`）：
13. `TestSummarizeToolStepNamesAReadImage`：摘要含 `image` 与字节数、不含 `lines`；
14. `TestFormatToolStepResultImageBodyHasNoJSONDump`：正文含路径与尺寸、不含 `"preview_kind"` 这种 JSON 键。

`pkg/mcp/result_processing_test.go`：
15. 一个含 `{"type":"image","data":…}` 的 envelope → 返回 parts 里有图片 part **且** envelope 里图片块
    变成一行路径文本；**路径必须落在注入的结果根下**（断言 `strings.HasPrefix(path, resultsDir)`，不是 `home`）；
16. base64 不出现在返回的文本 part 里（防泄漏进事件/日志）；
17. 未注入结果根时回退到 `<home>/state/mcp-results`（嵌入者的既有行为单独钉住，避免生产路径悄悄退回 home）；
18. `IsError` 的 envelope → 仍返回错误（与 `callToolAttempt` 的 `bridge.go:1030-1032` 一致）。

`pkg/run`：
19. 闸门：**真实案例** `deepseek/deepseek-v4-pro`（`assets/models.json:1129`，owner 确认纯文本）
    → `ImageInputRefusal` 非空、MCP 只回文本（再补一条合成的 `attachment:false` 条目，避免依赖数据文件的内容）；
20. 未知模型（catalog 无此项）→ 放行；
21. **ctx 里没有 `SessionModelSelection` 时**用 `PrimaryModelSelection(r)` 判定（照 `runner.go:1381`
    那条注入路径只有会话有存储行时才成立的事实）。

`pkg/gateway`：
22. 新端点：会话根内的 PNG → 200 + `Content-Type: image/png`；越界路径 → 400；
23. 非图片 → 415；
24. 上传 image/* 走 `FileReferencePartJSON` 分支。

`pkg/tui`：
25. 图片步骤渲染出标记行（含尺寸与字节数）；
26. `Ctrl+O`（`0x0f`）在没有图片步骤时只提示、**不 spawn** 进程（用一个可注入的 opener 断言没被调用）；
27. 在 composer 里输入字母 `o` **不产生** `hotkeyOpenImage`（防裸键回归）。

`pkg/tool/file_tools_test.go`（Step 5b；照 `:558 TestFileReadOutsideAllowedRootsReadsWithoutApproval`
与 `:203` 那类负向断言的写法）：
28. `TestFileReadMCPResultsNeedsNoApproval`：文件在 `<主代理 workspace>/state/mcp-results/` 下 →
    `read_file` **不产生** `RequiresActionError`（装一个会报错的 `SetActionHook`，断言它**不被调用**）；
29. `TestFileReadStillAsksInsideHomeOutsideWorkspace`：`<home>/auth.json` 与
    `<home>/state/forebrain.state.sqlite` → **仍然**触发审批/拒绝（证明这次改动没有放宽 home 保护）；
30. `TestFileReadMCPResultsNeedsNoApprovalInProjectSession`（**关键**）：同一条断言在
    「workspace boundary 的主 workspace = 主代理 workspace、另有独立的 `ProjectRoot`」的会话里也成立
    （`SetPrimaryWorkspaceBoundary(home, agentWorkspace, siblings)` 之后的项目根；
    见 `runner.go:1079` 的 `activeWorkspace := r.workspaceRoot()`）——
    这条正是「三层 MCP 的结果都进主代理 workspace 才免审批」的机器化证明。

`frontend/src/components/chat/ToolCallCard.test.ts`：
31. `preview_kind=image` 渲染 `<img>` 且 `src` 指向 `/v1/workspace/file/raw?...`；
32. 非图片步骤不渲染 `<img>`。

`scripts/acceptance/read_image.sh`：Step 9 的真机链路。

## Done criteria

- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 通过（除已知 `pkg/mcp` 既有失败）
- [ ] `go vet ./...`、`gofmt -l cmd pkg third_party` 干净；`./pkg/architecture` PASS
- [ ] `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` 干净
- [ ] `grep -n "cannot read image files as text" pkg/tool/file_tools.go` → 无匹配
- [ ] `grep -c "or an image (png/jpeg/gif/webp) as an image" pkg/tool/file_tools.go` → 1
- [ ] `grep -rn "img" frontend/src/components/chat/ToolCallCard.vue` → 命中（缩略图已落地）
- [ ] `grep -rn "ProtectedReadReason" pkg/tool/state.go` → **未新增豁免分支**（与改动前逐字一致）
- [ ] `grep -rn "mcp-results" pkg/mcp/` → 只剩注释、注入路径的拼接与「未注入时回退 home」一处；
      不得存在第二处 `home` 拼接
- [ ] `grep -n "SetResultsDir" pkg/run/runner.go` → 命中（生产路径显式注入主代理 workspace）
- [ ] `grep -n "hotkeyOpenImage" pkg/tui/input_events.go pkg/tui/run.go` → 常量 1 处 + **每一处**
      `case hotkeyRedraw:` 的 switch 各 1 处（当前是 2 处；用 `grep -c "case hotkeyRedraw:" pkg/tui/run.go` 对数）
- [ ] `grep -n '"attachment"' assets/models.json | sed -n '/deepseek-v4-flash/p'` → 该条目为 `true`
      （配套：`python3 -c` 打印 `deepseek/deepseek-v4-flash` 的 `attachment` 与 `modalities.input`，
      与 `deepseek-v4-flash-vision-exp` 一致）
- [ ] Step 6 的三种结论之一 + Step 9 的 `read image: PASS` 写进报告
- [ ] Step 9 断言 ⑥ 的证据（v4-pro 下读图给的是可执行拒绝文案、不是 provider 错误）写进报告
- [ ] 报告里含断言 ⑤ 的 mcp-results 位置证据，以及**验收用的是哪个模型、为什么**
- [ ] `plans/README.md` 状态行已更新；工作区**没有** commit

## STOP conditions

Stop and report（不要自己发挥）:

- Step 6 实测：两个模型都把工具消息里的图 400/422 拒掉，**且** Step 6 的拆分翻译实现后仍失败。
- Step 6 **跑不起来**（operator 给不出 `FOREBRAIN_E2E_DEEPSEEK_KEY`）：停下报告，不要跳过它去猜 provider 行为，
  也不要把拆分翻译「反正无害」提前做掉。
- **Step 6 的结论与 owner 2026-10-14 的说明矛盾**（`deepseek-v4-flash` 被端点拒绝收图）：
  Step 4c 那次数据修正就**没有证据支撑**——停下报告，把两个子用例的原始响应（状态码 + 服务端原文）交给
  owner 判定，**不要**留着改动、也不要为了让它"能用"去放宽闸门。
- Step 5 发现 MCP 结果的 `any` 签名加宽波及了 Scope 之外的调用方（例如 `pkg/gateway` 或 `pkg/tui`
  直接把工具结果当字符串用）——那需要重新划界，别硬改。
- Step 7a 发现取图端点无法复用 `resolveFilesPath`（根解析要重写）→ 停下，这条仓库级规则不能破。
- **Step 5b 发现结果目录搬进主代理 workspace 之后仍然要审批**（例如 workspace boundary 在某条路径上不是
  主代理 workspace）→ 停下报告，**不要**转而给 `ProtectedReadReason` 加豁免——那正是 owner 这条裁决要避免的。
- 你的改动让 **任何既有** provider/state/tui 测试变红（说明返回形状改动漏了消费点）。

## Maintenance notes

- **提示前缀**：read_file 描述改了一句，所有会话缓存前缀一次性移位——有意的产品改动；以后**不要**再顺手改
  （`AGENTS.md`：LLM-facing prompt text 必须字节稳定）。
- **每行消息的体积**：图片以 base64 落在 `fb_messages.parts_json`（`contentBlocksJSON` 的 image block），
  预算（默认 3 MiB、`FOREBRAIN_IMAGE_MAX_BYTES` 可调）就是**每行上限**。读同一张图 N 次会有 N 行。
  若将来库体积成问题，正确方向是「存引用、请求时再读」——用户附件已是这个形状
  （`transcript.go:277-340` 只认 user 角色）；工具结果没走这条，因为**当前这一轮**就要把像素发出去，
  而写进历史能保证回放逐字节一致（对前缀缓存更友好）。
- **能力是逐模型的，别用 family 推断**（owner 2026-10-14 逐条确认）：`deepseek-flash` 这个 family 里
  `deepseek-v4-flash` **能识图**、`deepseek-v4-flash-0731` **不能**；`deepseek-v4-pro` 也不能。
  改 `assets/models.json` 时只动被确认为错的那一条，不要"顺手统一"；两条纯文本条目的现状由
  `TestCatalogKeepsTextOnlyDeepseekModelsAsTextOnly` 钉住。用户报"读不了图/附件发不出去"时，
  **先查他的模型在 catalog 里的 `attachment`**，再怀疑代码。
- **闸门是 fail-open 的**：catalog 不认识的模型（自定义 base_url + 自定义模型名）一律放行，
  否则会拦掉本来能用的多模态端点。要收紧就改 `pkg/run/image_input.go`，别改消费点。
- **MCP**：`CallToolJSON` 保留给 discovery 工具；新增的 `CallToolContent` 是新会话路径。
  以后加 MCP 能力时注意这两条岔路都要照顾。
- **MCP 结果落盘位置**（owner 2026-10-14 裁决）：**一律**在 `<主代理 workspace>/state/mcp-results/`，
  三层作用域（全局 / 主代理 / 项目级）共用同一个目录。原因有二：主代理 workspace 本来就是
  `ProtectedReadReason` 的豁免根，所以模型 `read_file` 自己刚拿到的产出**不需要审批**；
  而项目根是用户的代码目录，不该沉积工具产出。
  **不要**为了「项目级 MCP 属于项目」把结果挪进项目根，也**不要**回头给 `ProtectedReadReason`
  加 `state/mcp-results` 豁免——搬目录才是这条裁决的解法。
  项目级 MCP 的 **stdio 工作目录**仍是项目根（`mcpServerWorkingDir`），两者别混。
  旧目录 `$FOREBRAIN_HOME/state/mcp-results/` 是瞬时产出，**不写迁移**，owner 可自行删除；
  早先 transcript 里记下的旧路径若文件还在，读它仍会走一次审批（一次性，不加豁免）。
- **surface**：事件与 API 里**永远**不放 base64（TUI 是 UTF-8 画布，web 是 JSON）。
  要显示就通过 `/v1/workspace/file/raw` 按路径取（同一个根解析规则）。
- **review 重点**：`compat`/`responses` 两条投影是否仍然「逐条独立」（前缀缓存），
  闸门拒绝时 MCP 是否**只回文本**而不是让请求 400，
  以及 `pkg/mcp` 里是否只剩**一处**结果根拼接（生产注入 + 一处回退）。
- **054 接续**：PDF 逐页转 PNG 复用 Step 1 的 `ImagePartWithin` 与 Step 4 的闸门；
  本计划不碰 PDF（`validateTextReadableFile` 的 `.pdf` 拒绝原样保留，由 054 改）。
