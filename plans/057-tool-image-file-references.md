# 057：工具结果图片改走 file_reference——把 base64 从库里和 messages API 里赶出去

> **执行者须知（零上下文）**：本计划自包含。逐步执行，每步先跑"验证"确认预期再进入下一步。
> 命中 STOP condition 就停下报告。**不建 worktree、不 commit**。
>
> **Drift check（先做）**：本计划写于 `git rev-parse --short HEAD` = `fa4af8a`（2026-10-16），
> 且**依赖 055 已执行**（rehydrate 复用 `ImagePartWithin(…, 0)` 的预算语义；若 055 未执行，
> Step 4 的 0-budget 仍可用，但 env 旋钮为死——先做 055）。执行前逐条核对 "Current state" 的
> file:line 摘录；对不上按 STOP 处理。

## Status

- **Priority**: P1（分支自己的不变量被违反：`files_write.go:78-80`"no event or JSON response
  ever carries base64"，而每张 MCP 截图以 ~4 MB base64 落库、messages API 原样外带）
- **Effort**: M/L（跨 llm/state/run/mcp/tool 五个包的持久化缝改造，有缓存前缀稳定性硬约束）
- **Risk**: MED-HIGH（改的是 transcript 持久化与 rehydrate；live 行为必须逐字节不变）
- **Depends on**: 055（强烈建议先行）
- **Category**: Correctness / Storage（审计发现 #4）
- **Planned at**: commit `fa4af8a`，2026-10-16

## Why this matters

图片进入模型的三条路（read_file 图片、PDF 渲染页、MCP 图片块）产出的都是 `ImageBase64` part，
随工具结果消息进 transcript 落库：

- `pkg/state/message_sync.go:139` `PartsJSON: MessagePartsJSON(msg, content)` →
  `message_parts.go:249` `contentBlocksJSON(msg.Parts, …)`（定义在 `pkg/state/usage.go:92-112`）
  把 `ContentTypeImageBase64` 序列化为 `{"type":"image","source":{"type":"base64","data":<全量>}}`；
- `stripUnpersistedMessages`（message_sync.go:382-396）只剥 system/ephemeral，工具行原样持久化；
- `pkg/gateway/api_extra.go:1730,1814` `handleChatMessages` 把 `parts_json` 逐行原样返回 web。

后果：每张 MCP 截图存**两份**（mcp-results 文件 + 库里 base64），`/api/chat/sessions/:id/messages`
每图膨胀 ~4 MB，每轮历史构建反复解析多 MB 字符串。而用户附件路径早有正确模式：持久化为
`file_reference`（file_id），请求构建时 rehydrate 成图片 part——图片被拒之门外纯属遗漏。

**硬约束（产品铁律）**：replay 与 live 1:1；prompt 缓存前缀只升不降。改造后 resume/replay 重建的
请求必须与 live 轮次**逐字节相同**（确定性降采样阶梯保证可复现）。

## Current state（摘自本次亲自阅读的代码）

### 既有 file_reference 机制（用户附件路径，本计划复用）

- `pkg/state/message_parts.go:14` `PartTypeFileReference = "file_reference"`；
  `:100` `FileReferencePartJSON(fileID, label, mimeType)`；
  load 侧 `:478-490`：file_reference part 解析为文本占位符 `"[attachment] label (mime_type)"`。
- `pkg/run/transcript.go:216-240`：`FileReferenceResolver` 接口 + `PathResolver`——**file_id 可以就是
  绝对路径**（"a surface attachment is persisted by path rather than by copying its bytes"）。
- `pkg/run/transcript.go:285-291` `rehydrateImageReferences`（逐 entry）→ `:307-346`
  `rehydrateMessageImages`：**`msg.Role != llm.RoleUser` 直接返回**（:308，工具行不 rehydrate 的原因）；
  rehydrate 用 `llm.ImageFile(absPath)`（:324，直发无降采样）；失败 best-effort 留占位符。
- `pkg/state/file_service.go:742-744` `ExtractFileRefInfos` 从任意 parts_json 提取 file_reference
  （`ListTranscriptMessagesWithRefs` 对**所有角色**的行都提 FileRefs——工具行改型后自动可用）。
- `pkg/run/factory.go:205` `ownerFileResolver()` 返回 `f.Owner.FileResolver`——runner 侧 resolver
  链已装好（执行时核对链里含 PathResolver：`pkg/run/runner.go:149` 附近的组装处）。

### 图片 part 的生产点（三个，全部知道自己的落盘路径）

1. `pkg/tool/file_tools.go:97→355` `imageReadPart(abs, raw, …)` → `ImagePartWithin(abs, mime, raw, …)`
   ——路径 = 被读文件自身（`abs`）。
2. `pkg/tool/file_tools.go:550` `appendPDFPagePart` → `ImagePartWithin(path, "image/png", page.PNG, …)`
   ——路径 = `storePDFPages` 刚写下的 PNG 路径（稳定，在 agent workspace `state/tool-outputs/`）。
3. `pkg/mcp/result_processing.go:152,178` `persistMCPResult` 先落盘得 `path`，再
   `ImagePartWithin(path, mimeType, decoded, …)`——路径 = mcp-results 文件（稳定，同样在 workspace）。

`ImagePartWithin` 的第一参数 `path` **已经**是落盘路径——只是今天没人把它带进 part。

### 关键不对称（本计划必须处理的核心事实）

落盘文件里存的是**原始字节**；part 里可能是**降采样重编码后的字节**（阶梯 2/3/4/6/8，PNG/JPEG q85，
最后 q75——`image_scale.go:50-57` 注释明言"fixed so the same input always produces the same part"）。
因此 **rehydrate 工具行不能照抄用户附件的 `ImageFile`（直发原始字节）**：那会把 48 MiB 原图直发、
且与 live 轮次曾发送的降采样 part 不同（前缀漂移）。rehydrate 必须用同一
`ImagePartWithin(path, mime, 原始字节, 0)` 重走阶梯——确定性阶梯保证输出与 live 逐字节一致。

## 修复设计

### Step 1：`llm.ContentPart` 增加 `SourcePath`

`pkg/llm/llm.go` 的 `ContentPart` struct 加字段：

```go
// SourcePath names the file whose bytes this image part was built from, when
// there is one. It is metadata for persistence: a part that knows its source
// is stored as a file_reference to that path instead of as base64, and a
// reload re-derives the exact same part from the file. It never reaches a
// provider — every projection switches on Type and ignores it.
SourcePath string
```

构造点：`ImagePartWithin` 的**每个成功返回处**（直发、阶梯命中、q75 兜底）在返回的 part 上设
`part.SourcePath = path`（`path` 是首参；`path == ""` 时跳过）。`ImageBase64` 构造函数不动
（不带 path 的调用方保持零值）。

**验证**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -count=1`。
既有 provider 投影测试全绿（兼容性证明：多余字段不进投影）。

### Step 2：持久化缝——base64 part 且有 SourcePath 时写 file_reference

`pkg/state/usage.go` `contentBlocksJSON` 的 `ContentTypeImageBase64` 分支（:102-112）改为：

```go
case llm.ContentTypeImageBase64:
	if src := strings.TrimSpace(part.SourcePath); src != "" {
		// A part that knows its file is stored by reference: the bytes stay
		// on disk (the tool already wrote them there), the row and the API
		// stay base64-free, and a reload re-derives the same part.
		blocks = append(blocks, map[string]any{
			"type":      PartTypeFileReference,
			"file_id":   src,
			"label":     filepath.Base(src),
			"mime_type": strings.TrimSpace(part.MIMEType),
		})
		break // 或等价的结构化返回
	}
	... 现有 base64 序列化原样保留（无 SourcePath 的 part 不变）...
```

注意：`contentBlocksJSON` 同时供 `parts_json`（message_parts.go:249）与 usage 记录使用——两处
一起变小是正确结果。**无 SourcePath 的 ImageBase64（用户附件 rehydrate 后的内存态若被再次落库、
测试构造等）行为不变**。

load 侧无需改动：`ParseMessageParts` 已把 file_reference 解析为文本占位符，
`ListTranscriptMessagesWithRefs` 已对所有角色提取 FileRefs。

**验证**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1`。

### Step 3：rehydrate 扩展到工具行 + 用阶梯重推

`pkg/run/transcript.go` `rehydrateMessageImages`：

1. 角色门 :308 改为 `msg.Role != llm.RoleUser && msg.Role != llm.RoleTool`。
2. 按角色分流重建方式：

```go
imgPart, err := llm.ImageFile(absPath)                    // RoleUser：现状不变
if msg.Role == llm.RoleTool {
	// A tool row's image was budgeted and possibly downscaled when it was
	// sent; re-derive the identical part from the file instead of sending
	// the raw bytes (deterministic ladder => byte-identical to the live turn).
	data, rerr := os.ReadFile(absPath)
	if rerr == nil {
		imgPart, _, err = llm.ImagePartWithin(absPath, mime, data, 0)
	} else {
		err = rerr
	}
}
```

   （mime 用 ref.MIMEType；`ImagePartWithin` 会重查嗅探，mismatch 时以其为准。）

**验证**：T1/T2。

### Step 4：三条生产路径确认 + web/TUI 附件面核查

1. 三个生产点无需改动（SourcePath 由 Step 1 在 `ImagePartWithin` 内统一设置）——执行时逐一
   grep 确认没有别的 `ImageBase64` 构造点绕过（`grep -rn "ImageBase64(" pkg/ --include=*.go | grep -v _test`）。
2. **附件面核查**：`pkg/gateway/api_extra.go:1820` 对工具行也调 `state.MessageAttachments(t.PartsJSON)`
   ——工具行改型后这里会返回 file_reference 条目。核查前端（`frontend/src` 中 attachments 的消费）
   与 TUI 对**工具行** attachments 的渲染：若出现"工具消息挂附件"的可视变化，在 api_extra 该
   call site 按角色过滤（只 user 行返回），并在测试钉住。这是行为面变化，**不许静默扩大**。
3. 回放/replay 路径（TUI 与 web 的历史重载）走的就是 `ListTranscriptMessagesWithRefs` +
   rehydrate —— 与 resume 同链路，T2 覆盖。

**验证**：T3；`cd frontend && pnpm exec vue-tsc --noEmit && pnpm test`。

### Step 5：迁移说明（不写数据迁移）

存量库里的 base64 行**不迁移**（改写历史行违反 transcript 不可变原则，且旧行读取路径不变：
load 侧 base64 part 照常解析为 ImageBase64 part）。在 `plans/README.md` 对应条目记一句遗留：
"存量工具行 base64 保持原样读取；新行落 file_reference"。

## Test plan

- **T1（核心不变量）** `pkg/run/transcript_test.go`：构造带 SourcePath 图片工具行 → 落库 →
  `ListTranscriptMessagesWithRefs` + `rehydrateImageReferences(PathResolver{})` 重建 →
  断言 (a) 库里 `parts_json` **不含** `"base64"` 子串、含 `"file_reference"`；(b) 重建 part 与
  live 曾持有的 part（同一 `ImagePartWithin` 调用的产物）**逐字段相等**（含 base64 字符串与
  MIME——缩放过的图也必须一致，钉住确定性阶梯）；(c) 文件删除后重建 best-effort 留
  `[attachment]` 占位符、不报错。
- **T2（replay/live 1:1）** `pkg/mcp/result_processing_test.go`：跑一次 extract → 模拟 persist →
  reload+rehydrate → 断言请求级消息序列与 live 内存态一致（重点：降采样 part 的复现）。
- **T3（API 面）** `pkg/gateway/api_extra_test.go`：含图片工具结果的会话，`/messages` 响应的
  `parts_json` 无 base64、体积有上限量级；工具行 attachments 字段的表现与 Step 4.2 的决定一致。
- **T4（用户路径不回归）**：既有用户附件 rehydrate 测试全绿（`pkg/run`、`pkg/turn` 既有用例）。

## STOP conditions

1. 发现 `contentBlocksJSON` 的 base64 形状被**其它**消费方按字面解析（不止 parts_json/usage 两处，
   如导出/迁移代码 grep 到 `"base64"` 字面量依赖）——列出清单停下报告，扩大改造需 owner 批准。
2. T1(b) 失败且原因在阶梯不确定性（同输入两次 `ImagePartWithin` 输出不同）——这是缓存前缀级
   问题，立即停下报告，不许绕过。
3. Step 4.2 发现工具行 attachments 在前端/TUI 产生不可接受的可视变化且无法用小过滤解决——
   停下报告方案。
4. `pkg/architecture` 图测试变红（不应：无新 import 边；`pkg/run`→`pkg/llm` 已存在）。

## Out of scope（不要做）

- 存量 base64 行的数据迁移/清洗（读路径兼容即可）。
- 用户附件路径的预算/降采样（审计 #18，另议）。
- MCP 无效 base64 块穿透（审计 #8，另议——本计划不碰 result_processing 的 decode 失败分支）。
- `workspaceFileRawMaxBytes`、缩略图 `@error` 兜底（审计 #20 项 4，另议）。

## 全量验证闸门

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/state ./pkg/run ./pkg/mcp ./pkg/tool ./pkg/turn ./pkg/gateway -count=1
CGO_ENABLED=1 go vet -tags fts5 ./...
gofmt -l cmd pkg third_party
cd frontend && pnpm exec vue-tsc --noEmit && pnpm test
```
