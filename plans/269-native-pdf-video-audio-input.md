# Plan 269: PDF / 视频 / 音频附件按模型能力原生发送

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行
> 的状态改成 DONE，并附一句执行摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git rev-parse --short HEAD`（计划基线 `ecc4e1aa`，2026-11-12 reconcile 刷新；后代提交亦可）
> `git diff --stat ecc4e1aa..HEAD -- pkg/llm/llm.go pkg/turn/input_parts.go pkg/run/image_input.go pkg/run/transcript.go pkg/llm/image_scale.go pkg/llm/openai/compat_llm.go pkg/llm/openai/responses_llm.go pkg/llm/anthropic/agent_llm.go pkg/llm/google/agent_llm.go pkg/state/message_parts.go pkg/llm/catalog.go pkg/llm/tokenizers.go`
> → 空输出即无漂移；有输出则对照「Current state」逐段核对（行号漂移不算，内容对不上 STOP）。
> 工作区里 290/293/294/295/297 与其它并行会话的未提交改动是预期前置，不要回退。
>
> **2026-11-12 reconcile 留痕**：原基线 `0a698bf2` 已大幅漂移——272/275–280（思考 carry）与 283 改了全部四个适配器、`llm.go`、`transcript.go`、`input_parts.go`、`message_parts.go`（合计 +1907/−314）；`pkg/llm/tokestimate.go` 已并入 `pkg/llm/tokenizers.go`（2026-11-08 文件收敛）；能力覆盖层 / 表列 / UI 开关已随 259/263/266 部分落地（见 Step 2）。核心发现仍成立：全仓无 `ContentTypeFileBase64`，非图片附件仍降级为文字。下文锚点已全部按 `ecc4e1aa` 复核刷新。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED（改用户消息的组装与三种协议的编码；关着开关的路径必须与今天逐字节一致）
- **Depends on**: plans/263-provider-registry-custom-models-and-caps.md（能力覆盖层；本计划给它加三个字段）
- **Category**: direction
- **Planned at**: commit `ecc4e1aa`（2026-11-12 reconcile 刷新锚点；原 `0a698bf2`, 2026-11-07）

## Why this matters

owner 2026-11-07：模型的高级配置里除「图片输入」外，加「PDF 输入」「视频输入」「音频输入」三个开关，并裁定**本期就做原生发送**——开关开着（模型支持）时，
用户附带的 PDF / 视频 / 音频按该协议的原生格式直接发给模型；关着时保持今天的处理。

今天只有图片会原样到模型：附件以 `file_reference` 部件存库，发送时图片读回成 base64 图片部件，各协议编码；其它附件被降级成文字（`pkg/turn/input_parts.go:173` 的注释：
「every attachment reaching here is an image or a file whose replay reports the same reason the live turn did」），PDF 只能靠模型自己调 `read_file`（`pkg/tool/file_tools.go` 起，pdfium 渲染页面）。

## Current state（2026-11-12 reconcile 按 `ecc4e1aa` 复核）

- 内容部件 `pkg/llm/llm.go:28-58`（275–280 之后仍只有三种类型；`CompactionState` 等新结构体与本计划无关）：

```go
const (
	ContentTypeText ContentType = "text"
	ContentTypeImageURL ContentType = "image_url"
	ContentTypeImageBase64 ContentType = "image_base64"
)
type ContentPart struct {
	Type ContentType
	Text string
	ImageURL string
	ImageBase64 string
	MIMEType string
	SourcePath string // persistence metadata; never reaches a provider
}
```

（现文件每个字段带 doc 注释，形状与上面一致；`llm.go` 另有 carry / CompactionState 相关新增，不要动。）

- 协议编码各自 `switch part.Type` / `switch p.Type`：`pkg/llm/openai/compat_llm.go:1728`、`:1799`（Chat Completions）、`pkg/llm/openai/responses_llm.go:1457`（Responses）、`pkg/llm/anthropic/agent_llm.go:760`、`:784`（Messages）、`pkg/llm/google/agent_llm.go:519`（Gemini）。
  其它按 `Type` 分支的地方也要认新类型：`pkg/llm/tokenizers.go:1239`（token 估算；**原 `tokestimate.go:142`，该文件已并入 tokenizers.go**）、`pkg/llm/image_heal.go:58`、`pkg/state/usage.go:120`、`pkg/telemetry/tool_debug.go:71`、`pkg/run/image_input.go:277`。
- 附件 → 部件：`pkg/turn/input_parts.go`（`BuildSurfaceUserPartsJSON`、`sniffImageMIMEType`、`SurfaceTurnContentParts` 的降级，注释在 :173）；存库序列化：`pkg/state/message_parts.go`（`FileReferencePartJSON`——该文件**没有**重建逻辑）；**回放重建图片部件在 `pkg/run/transcript.go`**（`rehydrateImageReferences` `:302`、`rehydrateMessageImages` `:329`；调用点 `pkg/run/image_input.go:277` 与 `transcript.go:126`；来源戳记 `pkg/llm/image_scale.go:233` `withSourcePath`）。
- 图片能力门：`pkg/run/image_input.go`（`ImageInputRefusal`、`ImageTurnRefusal`：模型不收图片时拒绝本回合而不是偷偷丢图；被服务商拒过记在 `fb_session_image_refusals`）。
- 能力来源（**部分已落地，见 Step 2**）：目录 `assets/models.json` 每条有 `modalities.input`（取值 `text`/`image`/`pdf`/`video`/`audio`），但 `pkg/llm/catalog.go` 的 `rawEntry`（:178，`Attachment bool` 在 :183）**仍未解析它**；`Hit` 只有 `SupportsAttachments`（:48/:73），没有 PDF/Video/Audio 字段。已落地的一半：`ModelOverride`（`catalog.go:602`）已有 `SupportsPDF, SupportsVideo, SupportsAudio *bool`，`config.LLMModelCaps`（`pkg/config/llm_models.go:39`）已有 `Images, PDF, Video, Audio, Reasoning, ReasoningOff *bool`，表列 `cap_pdf/cap_video/cap_audio` 已在（`pkg/state/schema.sql:724`、`schema_mysql.sql:623`、`config_store.go:560`），网页高级配置开关已在（`ModelFormDrawer.vue:413`）。但 `applyModelOverride`（`catalog.go:700` 起）今天只合并 `SupportsImages/CanReason/ContextWindow/MaxOutput`——**三个媒体覆盖值存了却没合并进 Hit**。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `go build ./...` | exit 0 |
| 相关包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/turn/... ./pkg/run/... ./pkg/state/... -count=1` | ok |
| 全量 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | ok |

## Scope

**In scope**：Current state 列出的文件及其测试；`pkg/llm/catalog.go`（解析 `modalities.input`）；`docs/plan/NATIVE_MEDIA_INPUT_FACTS.md`（新建，Step 1 的调研结论）。
**Out of scope**：网页/TUI 的附件选择器（它们已能附任意文件；本计划只改「附了之后怎么发给模型」）；高级配置开关的界面（plan 266）；表结构（plan 259 加列）。

## Steps

### Step 1: 调研各协议的原生格式（只写事实，不写代码）

按官方 API 文档查清下表每一格，写进 `docs/plan/NATIVE_MEDIA_INPUT_FACTS.md`（每格带官方出处 URL；**第三方转载不算**）：

| 协议 / 服务商方言 | PDF | 音频 | 视频 | 大小/格式上限 |
|---|---|---|---|---|
| OpenAI Chat Completions（官方） | 例：`{"type":"file","file":{"filename":…,"file_data":"data:application/pdf;base64,…"}}` | 例：`input_audio`（wav/mp3） | ？ | ？ |
| OpenAI Responses | 例：`input_file` | ？ | ？ | ？ |
| Anthropic Messages | 例：`document` 块（base64 PDF） | ？ | ？ | ？ |
| Gemini（`pkg/llm/google`） | 例：`inline_data` | `inline_data` | `inline_data` | ？ |
| 走 Chat Completions 的服务商方言（火山方舟/豆包、通义千问、智谱、Kimi、MiniMax、腾讯、美团） | 各自官方文档里的写法（例如 `video_url`），没有就写「不支持」 | | | |

「例」只是提示去核对的方向，以官方文档为准。查不到的格写「不支持」。

### Step 2: 能力字段（一半已落地，只做剩下的一半）

- `pkg/llm/catalog.go`：`rawEntry`（:178）解析 `modalities.input`；`Model`/`Hit` 加 `SupportsPDF, SupportsVideo, SupportsAudio bool`；`applyModelOverride`（:700 起）把**已存在**的 `override.SupportsPDF/SupportsVideo/SupportsAudio`（:602）合并进 Hit，规则同图片（行覆盖 > 官方接口实时结果 > 目录）。
- **不要重复添加**：`ModelOverride` 的三个 `*bool` 字段、`config.LLMModelCaps` 的 `PDF, Video, Audio *bool`（`pkg/config/llm_models.go:39`）、网页高级配置开关（`ModelFormDrawer.vue:413`，plan 266 落地）都已存在。
- **无需迁移**：`cap_pdf`、`cap_video`、`cap_audio` 列已随 plan 259 落地（`pkg/state/schema.sql:724`、`schema_mysql.sql:623`、`config_store.go:560`）。`stateSchemaVersion` 现为 34（283 占用、292 修过其 PG CHECK）；本计划预期零迁移——若实施中发现确需新列，STOP 报告，版本号从 35 起另议，不要自行加迁移。

### Step 3: 新部件类型

`pkg/llm/llm.go` 加：

```go
// ContentTypeFileBase64 carries a non-image file (PDF, audio, video) as raw
// base64 bytes for a model that takes it natively. MIMEType says which.
ContentTypeFileBase64 ContentType = "file_base64"
```

`ContentPart` 复用 `MIMEType`、`SourcePath`，新增 `FileBase64 string`、`FileName string`。构造函数 `llm.FileBase64(name, mime, b64)`、`llm.FileFromPath(path)`（读文件 + 嗅探 MIME：`application/pdf`、`audio/*`、`video/*`）。
所有按 `part.Type` 分支的地方（Current state 列出的）都处理新类型：token 估算按字节数折算（照图片的做法给出保守估计）；`image_heal` 不碰它；usage/telemetry 记类型与大小，**不记内容**。

### Step 4: 组装——按模型能力决定原生还是降级

`pkg/turn/input_parts.go` 与回放（`pkg/run/transcript.go` 的两个 rehydrate 及其调用点 `pkg/run/image_input.go:277`）重建部件处：附件 MIME 是 PDF/音频/视频时——
- 当前模型（行能力 → `llm.Lookup` 的 `SupportsPDF/Video/Audio`）支持该类型，**且**该协议（及服务商方言）在 Step 1 的表里有原生格式 → 构造 `ContentTypeFileBase64` 部件；
- 否则保持今天的降级文字，**逐字节不变**（加回归测试钉住）。
存库仍只存 `file_reference`（不内联字节），回放时按同一判断重建——换了模型的会话下一回合自动按新模型的能力决定。
超过 Step 1 记录的大小上限 → 降级并在降级文字里说明「超过模型可接收的大小」（短句，不暴露内部文件名）。

### Step 5: 各协议编码

按 Step 1 的表，在 Current state 列出的各编码器里加 `ContentTypeFileBase64` 分支；方言差异（例如某服务商用 `video_url`）按 provider 选择，provider 信息从 `LLMProviderYAML` 传到客户端构造（若客户端目前拿不到 provider，就给构造函数加一个 `dialect` 参数，调用点只有 `NewLLMFromYAML`）。
表里写「不支持」的组合永远不会走到这里（Step 4 已降级）；防御性地在编码器里遇到不支持的组合返回明确错误而不是静默丢弃。

### Step 6: 与图片同样的拒收记忆

服务商以「不支持该输入类型」拒绝时，照 `fb_session_image_refusals` 的做法记下（复用同一表，`reason` 里标明类型；若需要区分类型而表结构不够，停下报告，不要改表），下一回合该会话对该模型改走降级，不再重发。

### Step 7: 测试与全量

- 每个（协议 × 类型）在表里为支持的组合：`httptest` 断言请求体里出现对应原生结构、base64 与 MIME 正确；
- 不支持/开关关着：请求体与今天逐字节相同（用今天的输出做 golden）；
- 回放：同一会话换到不支持的模型，下一回合降级；
- 超大文件降级文案；拒收后下一回合不再原生发送。

**Verify**: `go build ./... && CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → ok。

## Done criteria

- [ ] `docs/plan/NATIVE_MEDIA_INPUT_FACTS.md` 每格有官方出处或写明「不支持」
- [ ] `grep -n "ContentTypeFileBase64" pkg/llm/llm.go pkg/llm/openai/compat_llm.go pkg/llm/openai/responses_llm.go pkg/llm/anthropic/agent_llm.go pkg/llm/google/agent_llm.go` 每个文件都有
- [ ] 开关关着的路径有 golden 回归测试且通过
- [ ] 全量测试 exit 0
- [ ] 报告含手动验收清单：用真 Key 各发一次（支持 PDF 的 Anthropic/OpenAI 模型、支持视频的豆包或 Gemini 模型、支持音频的模型）
- [ ] `plans/README.md` 本行已更新

## STOP conditions

- Step 1 某协议的原生格式只在第三方资料里出现。
- 原生发送需要先把文件上传到服务商的文件接口（拿 file_id 再引用）而非内联 base64——停下报告哪些组合需要上传，由 owner 定是否接入文件接口。
- 回放重建/改写部件的位置不止 `pkg/run/transcript.go` 的两个 rehydrate 与 `pkg/run/image_input.go:277`（实施中再发现别的 rebuild 点）——停下列出。

## Maintenance notes

- 能力判断与图片同源（行覆盖 > 官方接口 > 目录）；新增一种媒体只需：catalog 字段、caps 字段、Step 1 表、各编码器分支。
- 字节从不入库：会话里只有 `file_reference`；文件被删时回放按「文件已不存在」降级（与图片一致）。
