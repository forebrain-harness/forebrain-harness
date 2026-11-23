# 055：图像解码像素上限 + PDF 渲染/输入预算 + 修活 FOREBRAIN_IMAGE_MAX_BYTES

> **执行者须知（零上下文）**：本计划自包含。逐步执行，每步先跑"验证"命令确认预期再进入下一步。
> 命中 STOP condition 就停下报告。**不建 worktree、不 commit**（owner 裁决：改动留工作区由 owner 审阅）。
>
> **Drift check（先做）**：本计划写于 `git rev-parse --short HEAD` = `fa4af8a`（2026-10-16）。
> 工作区当时已有**未提交**改动（pkg/migrate、pkg/state、pkg/tui、pkg/gateway 测试、docs/plan 两份新计划），
> 均与本计划范围（pkg/llm、pkg/pdf、pkg/tool、pkg/mcp/result_processing.go）**不重叠**——除
> `pkg/gateway/api_extra_test.go`/`skills_download_test.go` 等测试文件不在本计划改动列表内。
> 执行前 `git rev-parse --short HEAD` 与 `git status --porcelain`，逐条对照下文 "Current state" 的
> file:line 摘录；对不上即按 STOP 处理。

## Status

- **Priority**: P1（进程级 OOM：模型/恶意 MCP 可用一个文件杀死整个 agent/gateway 进程）
- **Effort**: M
- **Risk**: MED（改动公共编码入口与 PDF 渲染循环；live 行为只有在超限时才变化）
- **Depends on**: none（建议先于 057 执行：057 的 rehydrate 会复用本计划修活的预算路径）
- **Category**: Security / Correctness / Perf
- **Planned at**: commit `fa4af8a`，2026-10-16（improve branch 审计发现 #1/#2/#5/#16）

## Why this matters

分支 bd2b8b8..fa4af8a 让模型能读图与 PDF，但所有上限都只管**编码字节数**，没有一处管**解码像素数**：

1. **解码炸弹**：`image.Decode` 前只查了 48 MiB 编码上限。Go 标准库（1.26.9，png `reader.go` 先按
   IHDR 声明的宽高 `image.NewNRGBA` 分配整幅像素缓冲、后才校验数据是否足够）——一个 3.1~48 MiB、
   IHDR 声明 60000×60000 的 PNG（载荷是高压缩零，甚至可截断）驱动 ~14 GB 分配 → fatal OOM，
   整个进程（agent 或 gateway，含所有在跑会话）死亡。可达路径：模型自己 `read_file`、恶意/故障
   MCP server 的图片块。
2. **PDF 页渲染无像素预算**：`RenderPageInDPI` 前不查页尺寸。go-pdfium v1.21.3 cgo 实现里位图是
   **调用方分配的 Go 缓冲**（`image.NewRGBA(totalWidth, totalHeight)`，pt×dpi/72，无上限）——库自身
   的"too large"守卫因此永不触发。规范上限 14400 pt 的页面 @150 dpi = 30000×30000 = 3.6 GB 分配；
   且渲染持有 pkg/pdf 进程级互斥、不可取消，其它 PDF 读取全部排队。
3. **`FOREBRAIN_IMAGE_MAX_BYTES` 是死旋钮**：`resolveImageBudget` 只在 `maxBytes <= 0` 时读 env，
   而三个生产调用方全部传正数常量 `llm.DefaultMaxImageBytes`。文档（image_scale.go:28-31）承诺的
   调节旋钮静默无效。
4. **PDF 整文件双读 + 输入无上限**：`os.ReadFile` 全量进内存（file_tools.go:79），`RenderFile`
   内部再 `os.ReadFile` 同一文件一遍，PDFium 还要持有自己的解析副本——500 MB PDF ≈ 1 GB+ RSS；
   5 页上限只管输出不管输入。

## Current state（执行者要看到的事实，全部摘自本次亲自阅读的代码）

### pkg/llm/image_scale.go（HEAD fa4af8a）

```go
// :23-32
const (
	DefaultMaxImageBytes = 3 << 20
	MaxImageDecodeBytes  = 48 << 20
	ImageMaxBytesEnv     = "FOREBRAIN_IMAGE_MAX_BYTES"   // 只在 resolveImageBudget 里读
)
// :58-90（节选）
func ImagePartWithin(path, mime string, data []byte, maxBytes int) (ContentPart, ImageReadResult, error) {
	budget := resolveImageBudget(maxBytes)
	if len(data) > MaxImageDecodeBytes { ... refuse ... }        // :62 唯一的解码前上限（编码字节）
	switch mime { case "image/jpeg","image/png","image/gif","image/webp": ... default: refuse }
	if len(data) <= budget {                                       // :72 直发路径，不本地解码（安全）
		w, h := DecodeImageConfig(data) ... return ImageBase64(...)
	}
	if mime == "image/webp" { ... refuse（无解码器） ... }
	src, _, err := image.Decode(bytes.NewReader(data))            // :87 ← 炸弹点：此前无任何 w*h 检查
	...
}
// :139-148
func resolveImageBudget(maxBytes int) int {
	if maxBytes > 0 { return maxBytes }                           // 生产调用方全传正数 → env 永不生效
	if v := os.Getenv(ImageMaxBytesEnv); ... { return n }
	return DefaultMaxImageBytes
}
```

生产调用方（三处全部传死常量）：

- `pkg/tool/file_tools.go:97` `imageReadPart(abs, raw, llm.DefaultMaxImageBytes)`（内部 :355 转传给 `ImagePartWithin`）
- `pkg/tool/file_tools.go:550` `appendPDFPagePart` → `llm.ImagePartWithin(path, "image/png", page.PNG, llm.DefaultMaxImageBytes)`
- `pkg/mcp/result_processing.go:178` `llm.ImagePartWithin(path, mimeType, decoded, llm.DefaultMaxImageBytes)`

### pkg/pdf/render.go（HEAD fa4af8a）

```go
// :69
func RenderFile(path string, start, maxPages int, opts Options) (RenderResult, error) {
	...
	data, err := os.ReadFile(path)        // :70 ← 第二次整文件读（调用方 file_tools.go:79 已读过）
	...
	for index := start - 1; index < start-1+maxPages && index < count.PageCount; index++ {
		rendered, err := instance.RenderPageInDPI(&requests.RenderPageInDPI{   // :123 ← 渲染前不查页尺寸
			DPI:  int(dpi), Page: requests.Page{ByIndex: ...},
		})
```

唯一调用方：`pkg/tool/file_tools.go:139` `pdf.RenderFile(abs, start, end-start+1, pdf.Options{FontDirs: ...})`。

go-pdfium v1.21.3 已提供按索引取页尺寸的 API（模块内已核实）：
`pdfium.go:222-223` `FPDF_GetPageSizeByIndex(request *requests.FPDF_GetPageSizeByIndex)`，
request 结构 `requests/fpdfview.go:85-88` `{Document references.FPDF_DOCUMENT; Index int}`（返回 pt 单位尺寸）。

### pkg/tool/file_tools.go PDF 分支（HEAD fa4af8a）

`os.ReadFile(abs)` 在 :79（对**所有**文件类型先整读，属既有行为）；`.pdf` 分支判定在 :112 起，
:139 调 `RenderFile`。`raw` 在分支内仅用于 `len(raw)`（:161 `pdfReadOutput(..., len(raw))`）。

## 修复设计

### Step 1（#1）：解码前像素上限

在 `pkg/llm/image_scale.go`：

1. 新常量（与 MaxImageDecodeBytes 并列，注释写清理由）：

```go
// MaxImageDecodePixels is the pixel ceiling on any image we decode to
// downscale: decode allocates w*h*4 bytes from the header's claim before
// validating pixel data, so an encoded-size cap alone cannot stop a
// crafted header from allocating gigabytes. ~64 megapixels (256 MiB of
// RGBA) is far above any real photo, and far below the bombs.
MaxImageDecodePixels = 64 << 20
```

2. 在 `ImagePartWithin` 的 `image.Decode` 调用**之前**（现 :86 行附近，webp 拒绝之后）加：

```go
if w, h := DecodeImageConfig(data); w > 0 && h > 0 && w*h > MaxImageDecodePixels {
	return ContentPart{}, res, fmt.Errorf(
		"image is %dx%d (%d MP); the decode limit is %d MP — crop or resize it first",
		w, h, w*h>>20, MaxImageDecodePixels>>20)
}
```

   注意：**直发路径（len(data) <= budget，:72）不加**——那条路不本地解码，发给 provider 的小字节
   无本地风险；给直发加限制只会无谓拒绝。

**验证**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -run TestImagePartWithin -count=1`（既有测试全绿）。

### Step 2（#2）：PDF 页渲染像素预算

在 `pkg/pdf/render.go` 渲染循环内、`RenderPageInDPI` 之前：

1. `size, err := instance.FPDF_GetPageSizeByIndex(&requests.FPDF_GetPageSizeByIndex{Document: document.Document, Index: index})`
   （字段名以 v1.21.3 responses 类型为准，执行时 `go doc` 核对一次）。
2. 计算 `wPx = ceil(size.Width * dpi / 72)`、`hPx` 同理（float64 → int）。
3. `wPx*hPx > llm.MaxImageDecodePixels` → 返回可执行错误（不渲染该页、终止本次读）：

```
pdf: page %d is %.0fx%.0f pt; at %d dpi it would rasterize to %dx%d pixels (over the %d MP limit) — open this page in a viewer instead
```

   复用 `llm.MaxImageDecodePixels` 单一预算（pkg/pdf→pkg/llp 边已在架构图里，fonts.go 已 import llm）。
   **语义选择：拒绝，不降 DPI**（拒绝是可预期、可解释的第一刀；自动降 DPI 改变输出事实，留给将来）。

**验证**：新增单测见 Test plan T3。

### Step 3（#5）：修活 env 旋钮

三个生产调用点把 `llm.DefaultMaxImageBytes` 改为 `0`（`resolveImageBudget` 的既有语义：0 = 默认+env）：

- `pkg/tool/file_tools.go:97` → `imageReadPart(abs, raw, 0)`
- `pkg/tool/file_tools.go:550` → `llm.ImagePartWithin(path, "image/png", page.PNG, 0)`
- `pkg/mcp/result_processing.go:178` → `llm.ImagePartWithin(path, mimeType, decoded, 0)`

同步修注释：`imageReadPart` 的 doc 若提及"budget 常量"改为"0 = 默认 + $FOREBRAIN_IMAGE_MAX_BYTES"。

**验证**：T4。

### Step 4（#16）：消灭 PDF 双读 + 输入上限

1. `pkg/pdf/render.go` 签名改为 `RenderFile(path string, data []byte, start, maxPages int, opts Options)`：
   `path` 仅用于错误消息，`data` 由调用方传入；删除内部 `os.ReadFile`（:70）。`OpenDocument` 本就吃字节
   （`File: &data`），直接用参数。
2. `pkg/tool/file_tools.go:139` 调用改为 `pdf.RenderFile(abs, raw, start, end-start+1, ...)`。
3. 新常量 `pkg/pdf/render.go`（与 DefaultMaxPages 并列）：

```go
// MaxPDFInputBytes is the largest PDF read_file will open. PDFium keeps its
// own parse copy beside the caller's bytes, so an unbounded input costs
// multiples of the file size before the 5-page cap limits anything.
MaxPDFInputBytes = 128 << 20
```

4. `file_tools.go` PDF 分支入口（ext 判定后、RenderFile 前）：`if len(raw) > pdf.MaxPDFInputBytes`
   → 返回可执行错误（"PDF is %d MiB; the input limit is %d MiB — split it or extract the pages you need"）。
   128 MiB 取舍：5 页渲染上限下 128 MiB 已覆盖几百页文本 PDF；更大的多为整本扫描件，明确拒绝优于
   三副本进内存。owner 评审时可调。
5. 既有 `render_test.go` 调用点跟随签名更新。

**验证**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/pdf ./pkg/tool -count=1`。

## Test plan（全部新增进既有测试文件，跟随既有命名风格）

- **T1（#1 红例）** `pkg/llm/image_scale_test.go`：构造 `[]byte` = PNG signature + IHDR 声明
  60000×60000 + 填充到 3.5 MiB（后面塞任意字节即可，断言在解码前发生）。`ImagePartWithin`
  必须返回含 "decode limit" 的错误，而**不是** "could not be decoded"（证明守卫先于 Decode 生效）。
  反证：临时把守卫删掉，测试应转为失败或（在 CI 内存受限时）暴露真正的 Decode 路径——按仓库惯例
  只需证明红例绑住守卫文案即可。
- **T2（#1 边界）**：声明 60000×60000 但总长 ≤3 MiB（直发路径）→ **不**报像素错（照常直发），
  钉住"直发不设像素限制"的语义。
- **T3（#2 红例）** `pkg/pdf/render_test.go`：用字节模板手工构造最小合法单页 PDF，
  `/MediaBox [0 0 14400 14400]`（对照组 `[0 0 612 792]` 正常渲染）→ 大页必须返回含
  "over the ... limit" 的错误且不产出 PNG。若 PDFium 拒绝打开该手工 PDF（对照组也开不了）→
  命中 STOP-2。
- **T4（#5）** `pkg/tool/file_tools_test.go`：`t.Setenv("FOREBRAIN_IMAGE_MAX_BYTES", "1048576")`
  后 read_file 一张 2 MiB PNG → 结果 `res.Scaled == true`（env 生效）；未设 env 时同一文件行为不变。
- **T5（#16）**：`RenderFile` 新签名编译绿 + 既有 `render_test.go`/`file_tools_test.go` 全绿；
   新增：>`MaxPDFInputBytes` 的输入在 file_tools 层被拒（可用 `bytes.Repeat` 造大 buffer 直接调
   分支函数，或最小重构使判定可测）。

## STOP conditions

1. `FPDF_GetPageSizeByIndex` 在 v1.21.3 的返回字段名/单位与本文摘录不符且无等价 API（含
   `FPDF_GetPageSizeByIndexF`）——停下报告，不要改用"渲染后再查尺寸"方案。
2. 手工构造的 MediaBox PDF 连对照组都无法被 PDFium 打开——停下报告，换成
   `scripts/acceptance/make_zh_pdf.py` 同源的构造方式后再试。
3. Step 3 改动后出现**其它**调用方依赖"显式常量预算"的语义（如测试外代码传 DefaultMaxImageBytes
   期望屏蔽 env）——列出调用方停下报告。
4. `pkg/architecture` 测试因 import 变化变红（不该发生：pdf→llm 边已存在）——跑
   `scripts/package-graph.sh` 再生成并核对 diff 只含无关既有内容，否则停下。

## Out of scope（不要做）

- 用户附件/粘贴路径的预算（`llm.ImageFile` 直发无降采样）——审计发现 #18，涉及用户自交内容的
  产品语义，待 owner 单独拍板。
- 直发路径（≤budget）加像素上限；PDF 自动降 DPI；read_file 对所有文件类型的预读 stat 优化。
- `workspaceFileRawMaxBytes`（gateway 侧 6 MiB 预览上限）不动。

## 全量验证闸门（最后一次跑）

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/pdf ./pkg/tool ./pkg/mcp ./pkg/run -count=1
CGO_ENABLED=1 go vet -tags fts5 ./...
gofmt -l cmd pkg third_party   # 必须为空
```
