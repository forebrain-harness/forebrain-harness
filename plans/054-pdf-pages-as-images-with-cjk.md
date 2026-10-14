# Plan 054: PDF 逐页转 PNG 交给模型，且中文（GB2312/Big5）必须渲染正确

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat fe3ee32..HEAD -- pkg/pdf pkg/tool pkg/llm go.mod go.sum Makefile Dockerfile npm scripts .github pkg/architecture`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: XL
- **Risk**: HIGH（引入**新的原生依赖**（PDFium）+ 跨 5 个平台的构建/打包 + 字体资产分发）
- **Depends on**: `plans/053-read-images-as-pixels.md` 的 **Step 1**（`llm.ImagePartWithin`）、
  **Step 4**（能力闸门 `ImageInputRefusal`）与 **Step 4c**（catalog 数据修正：`deepseek/deepseek-v4-flash`
  的 `attachment` 必须是 true，否则闸门会把 owner 默认模型的 PDF 读取也一并拦掉）；053 未落地前不要开工
- **Category**: bug（能力缺口：模型读不了 PDF）+ feature
- **Planned at**: commit `fe3ee32`, 2026-10-14

## Why this matters

owner 2026-10-14 要求把 053 的 Out of scope 全部并入本期，其中 PDF 是最容易被当成欠条的一条：
Claude Code 的 `Read` 能读 PDF，forebrain 今天只会回一句 `read_file cannot read PDF files as text`
（`pkg/tool/file_tools.go:242`）。

**并且 owner 追加了硬约束：中文必须能渲染**（`必须要支持中文字体`）。这一条直接决定选型：
中文 PDF 里「未内嵌字体的标准 CJK 字体」（`STSong-Light` / `SimSun` / `AdobeSongStd`，公文与报告里极常见）
渲染时需要**回退字体**，拿不到就是满屏豆腐块（tofu）——相当于没支持。

做完之后：模型对 `.pdf` 调 `read_file` 能拿到**前几页的页面图像**（外加页数与已渲染页数的说明），
**中文文档可读**，扫描件（本来就是图）同样可读。

## Current state

### 今天的 PDF 处理

- `pkg/tool/file_tools.go:242`：`.pdf` → `"read_file cannot read PDF files as text"`。
- 全仓库对 PDF 的其它处理只有一处：`pkg/mcp/result_processing.go:206` 把 `application/pdf` 映射成 `.pdf` 扩展名
  （MCP 二进制结果落盘用）。没有渲染、没有文本抽取。
- 既有测试：`pkg/tool/file_tools_test.go:252 TestFileReadRejectsPDFWithSpecificMessage`（本计划要改它的期望）。

### 选型（已核实，执行者**不要**换库）

| 候选 | 结论 |
|---|---|
| **PDFium**（Apache-2.0）+ `github.com/klippa-app/go-pdfium` v1.21.3（MIT 包装，`Imported by: 52`） | **采用（native/cgo 后端）** |
| MuPDF（`gen2brain/go-fitz`） | **排除**：MuPDF 与包装都是 **AGPL**，本仓库是 Apache-2.0（`LICENSE` 第 1 行），不可分发；社区也记录它 CJK 支持有坑 |
| go-pdfium 的 **WebAssembly/wazero** 后端 | **排除**：`webassembly.Config` 只有 `MinIdle/MaxIdle/MaxTotal/FSConfig`，**没有字体路径**；且 WASM 版 PDFium 编译为 Linux、拿不到系统字体 → 中文豆腐块 |
| 纯 Go 渲染库（`ajroetker/pdf/render` 等） | **排除**：CJK 正是这类「实现 PDF operator 子集」的库最容易翻车的地方；且不是知名成熟库 |

**采用它的决定性事实**（`pkg.go.dev/github.com/klippa-app/go-pdfium`，v1.21.3）：

```go
type LibraryConfig struct {
	UserFontPaths []string // Array of paths to scan in place of the defaults when using built-in FXGE font loading code.
	                       // The Array may be nil or empty itself to use the default paths. May be ignored entirely depending upon the platform.
}
```

`single_threaded.Config` 就是 `{ LibraryConfig *pdfium.LibraryConfig }` —— 也就是说**我们能把自带字体目录喂给
PDFium 的 FXGE 字体加载器**（`FPDF_LIBRARY_CONFIG.m_pUserFontPaths`）。README 同时写明
「`fpdf_sysfontinfo.h` 不实现」，所以 `UserFontPaths` 是唯一且正确的字体入口。

其余 API（**逐字来自官方 README / pkg.go.dev**）：

```go
pool = single_threaded.Init(single_threaded.Config{LibraryConfig: &pdfium.LibraryConfig{UserFontPaths: []string{fontDir}}})
instance, err := pool.GetInstance(30 * time.Second)
doc, err := instance.OpenDocument(&requests.OpenDocument{File: &pdfBytes})
count, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc})
page, err := instance.RenderPageInDPI(&requests.RenderPageInDPI{
	DPI: 150, Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc, Index: 0}}})
// 单线程/cgo：图片在 page.Image（*image.RGBA）
defer instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc})
```

- 平台注意（README）：`UserFontPaths` **"May be ignored entirely depending upon the platform"** ——
  macOS/Windows 上 PDFium 走系统字体接口（PingFang / 微软雅黑），Linux 走 fontconfig 路径。
  所以**两条路都要成立**：macOS/Windows 靠系统 CJK 字体，Linux 靠我们自带的字体目录。
- 原生库来源：`https://github.com/bblanchon/pdfium-binaries/releases`
  （mac arm64/x64、linux x64/arm64(glibc)、win x64/arm64 —— 覆盖本仓库 5 个发布目标）。
- cgo：本仓库**强制 CGO**（silk / sqlite / fts5，见 `AGENTS.md`），且已有**链接原生库的既有先例**：
  `scripts/fetch-libtokenizers.sh` + plan 040（`daulet/tokenizers` 五平台，Windows 现编）。本计划照它写。

### 中文字体的机制（决定 Step 3）

- PDFium 的字符集回退：`FPDF_GetDefaultTTFMap()` 返回 **charset → 字体名** 的表，
  相关常量 `FXFONT_GB2312_CHARSET = 134`、`FXFONT_CHINESEBIG5_CHARSET = 136`
  （还有 `SHIFTJIS = 128`、`HANGEUL = 129`）。它给的是**字体名**，最终仍需要一个字体**文件**——
  这正是 `UserFontPaths` 要解决的。
- 同生态的既有做法（可对照）：EmbedPDF（浏览器端 PDFium）就带 `@embedpdf/fonts-sc`（Noto Sans Hans / GB2312）
  与 `@embedpdf/fonts-tc`（Noto Sans Hant / Big5），按 charset 挂回退字体。
- 结论：本仓库**自带 Noto Sans SC + Noto Sans TC**（**SIL OFL 1.1**，可再分发；与仓库既有的
  asset 分发方式一致：像分词字典那样**装在 home 级、启动时播种**——plan 047 的
  `pkg/process/seed.go` + `memory.Seed` 就是这条路），并把它们的目录交给 `UserFontPaths`。

### 资产与打包先例（照抄）

- `assets/models.json`、`assets/tokenizers/`；`scripts/install-assets.sh <bin-dir>`；
  `Makefile:55` 调用它；`pkg/llm/assets.go:32 AssetsDirEnv = "FOREBRAIN_ASSETS_DIR"`。
- 启动播种进 home：`pkg/process/seed.go`（计划 047 落地）。
- 平台包：`npm/scripts/build-platform-packages.sh`；发布矩阵在 `.github/workflows/`。
- 架构闸门：`pkg/architecture/cache_test.go:657 maxPackageCount = 30`（当前 **28** 个包，
  本计划新增 `pkg/pdf` → 29，仍在限内）；`:659 TestPackageShape`、`:578 TestSameLayerEdgesAreEnumerated`、
  `scripts/package-graph.sh` + `pkg/architecture/testdata/graph.json`（**加包必须重生成并纳入本期 diff**）。

### 命令

| Purpose | Command | Expected |
|---|---|---|
| 原生库 | `scripts/fetch-pdfium.sh` | 产出 `third_party/pdfium/<triple>/{include,lib}` |
| 字体 | `scripts/fetch-pdfium-fonts.sh` | 产出 `assets/fonts/NotoSans{SC,TC}-Regular.otf` + 校验 SHA256 |
| 构建 | `CGO_ENABLED=1 go build -tags fts5 -o build/bin/forebrain ./cmd/forebrain` | exit 0 |
| 单包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/pdf/... ./pkg/tool/... -count=1` | ok |
| 专项 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/pdf/ -run 'TestChinese' -count=1 -v` | PASS（CJK 非 tofu） |
| 图 | `scripts/package-graph.sh` | 无未提交差异（graph.json 已同步） |
| 全量 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 所有包 ok |

**房规**：测试/脚本不得继承环境里的 `FOREBRAIN_HOME`；不建 worktree、不 commit；
**本计划是唯一允许新增依赖的计划**（`go-pdfium` 一条；测试生成 fixture 用的 writer 也必须是纯 Go、
且只出现在 `_test.go` 或生成脚本里）。

## Scope

**In scope**：

- `go.mod` / `go.sum`（+ `github.com/klippa-app/go-pdfium`，**只这一个**）
- `scripts/fetch-pdfium.sh`、`scripts/fetch-pdfium-fonts.sh`（新建）
- `third_party/pdfium/`（下载产物，**gitignore**；只提交脚本与校验和）
- `assets/fonts/`（缺失时由脚本填充；若仓库决定提交字体，则改 `assets/README` 说明来源与许可证）
- `pkg/pdf/`（新建：`doc.go`、`render.go`、`fonts.go`、`render_test.go`、`testdata/`、生成器脚本）
- `pkg/process/seed.go`（把字体目录播种进 home，照字典那条）
- `pkg/tool/file_tools.go`、`pkg/tool/file_tools_test.go`（PDF 分支 + 改既有 PDF 用例的期望）
- `pkg/llm/llm.go`（若需要 `ImageMIMEType` 之外的 MIME 帮助函数）
- `pkg/architecture/testdata/graph.json`（重生成）
- `Makefile`、`Dockerfile`、`npm/scripts/build-platform-packages.sh`、`.github/workflows/*`（构建/打包接线）
- `scripts/acceptance/read_pdf.sh`、`scripts/acceptance/make_zh_pdf.py`（**都新建**；后者生成两份中文 fixture，
  头注释写明来源与许可，见 Step 4）
- `pkg/pdf/testdata/`（上述脚本产出的两份 PDF；**全仓唯一的二进制 fixture**，理由写在脚本头注释里）
- `pkg/tool/format.go`（PDF 步骤的摘要与正文；与 053 的图片标记同形）

**Out of scope**：

- PDF 的**写入**/编辑/表单填写/数字签名；PDF 文本抽取的独立工具（本计划的文本只作为页面图像的补充说明）。
- 音频/视频/其它二进制；OCR（模型自己看图）。
- 替换 PDFium 为别的引擎；给 PDFium 加 `sysfontinfo` 的自定义实现（`UserFontPaths` 已够）。
- TUI 内联真像素（owner 已定「标记行 + 按键打开」，与 053 Step 7 同一套）。

## Git workflow

- 当前分支当前工作区直接改；**不 commit、不 push**。构建接线会动 CI/打包脚本，`git status` 要能逐一解释。
- 若 operator 明确要求提交：`git commit -s -F <file>`，标题
  `feat(pdf): render PDF pages as images for the model, with CJK fonts`，正文按 `.gitmessage` 三段。

## Steps

### Step 0: 先用最小 spike 证明三件事（**不写生产代码**）

在 `$TMPDIR` 建一个一次性模块（**不要**污染仓库 `go.mod`），`go get github.com/klippa-app/go-pdfium@v1.21.3`，
按 README 的 single-threaded 例子跑通：打开一个 PDF、`FPDF_GetPageCount`、`RenderPageInDPI` 写出 PNG。

必须证明：

1. **能编译链接**：本机（darwin/arm64）用 bblanchon 的 mac-arm64 原生库 + `pdfium.pc`（或
   `CGO_CFLAGS`/`CGO_LDFLAGS` 直给 include/lib）能 build 并运行；
2. **中文能渲染**：用 `scripts/acceptance/make_zh_pdf.py`（Step 5 会正式提交它；spike 阶段先临时写一个）
   生成两个 PDF：**内嵌** Noto Sans SC 的、**不内嵌**（引用 `STSong-Light`）的；
   两个都渲染出 PNG，人工看一眼：不内嵌那个在有 `UserFontPaths` 指向 Noto Sans SC 时不出现豆腐块；
   **并且要单独试一次"不设 `UserFontPaths`"**，看本机是否本来就有 CJK 系统字体——这件事决定 Step 2 的
   默认值（见那里的"替换语义"）。
3. **池的用法**：`single_threaded.Init` 是**进程级一次**（它内部用全局 mutex），`GetInstance` 后可多次
   `OpenDocument`；`Close()` 的资源释放路径可行。

把三件事的原始输出（命令、退出码、PNG 的字节数与尺寸）写进报告。**任一不成立就 STOP**（见 STOP conditions）。

**Verify**: `go run .` 打印页数、写出 2 张 PNG、`file *.png` 显示 PNG 且尺寸合理（A4@150dpi ≈ 1240×1754）

### Step 1: `scripts/fetch-pdfium.sh` + 构建接线（先只做宿主平台）

照 `scripts/fetch-libtokenizers.sh` 的骨架写：

- 参数/环境：默认宿主三元组（`darwin/arm64` 等）；`FOREBRAIN_PDFIUM_TARGETS="darwin/arm64 linux/amd64 …"`
  可覆盖；产物落 `third_party/pdfium/<GOOS>-<GOARCH>/{include,lib}`（加入 `.gitignore`）。
- 下载 bblanchon 的对应归档，**校验 SHA256**（脚本里写死期望值；不匹配即失败退出）。
- 生成 `third_party/pdfium/<triple>/pdfium.pc`（照 README 的 pkgconfig 模板），
  并 extra 提供 `CGO_CFLAGS`/`CGO_LDFLAGS` 的等价导出（pkgconfig 在某些 CI 镜像里不可靠）。
- 幂等：已存在且校验通过就不重复下载。

构建接线（宿主 → 全平台分两步走，先让本机绿）：

- `Makefile`：新增 `pdfium` 目标（跑 fetch），`build` 依赖它；把 include/lib 传进 `CGO_CFLAGS/CGO_LDFLAGS`。
- 本机验证：`make build`（或直接 `CGO_ENABLED=1 go build -tags fts5 ./cmd/forebrain`）成功，
  并且**只改构建环境**就能让 `go build` 找到头文件与库（写进 `docs/plan/` 不必；写进 `Makefile` 注释即可）。

**Verify**: `scripts/fetch-pdfium.sh && CGO_ENABLED=1 go build -tags fts5 -o /tmp/fb-pdf ./cmd/forebrain` → exit 0；
`ls third_party/pdfium/*/lib` 有文件

### Step 2: `pkg/pdf` —— 进程级渲染池

新建 `pkg/pdf`（`doc.go` + `render.go` + `fonts.go`，满足 `TestPackageShape` 的「≥2 个生产文件」）：

```go
// Package pdf renders PDF pages to PNG for the model to look at.
// Renderer 在进程内只初始化一次 PDFium；每次 Page 调用取一个 instance。
type Page struct {
	Number   int    // 1-based
	PNG      []byte // 页面图像（PNG）
	Width    int
	Height   int
	Rendered bool   // false = 该页超出页数上限，未渲染
}

type RenderResult struct {
	PageCount   int
	Pages       []Page // 只含已渲染的页
	RenderedMax int    // 本次的上限（默认 5）
}

// RenderFile 渲染 path 的前 maxPages 页（maxPages<=0 → DefaultMaxPages=5）。
// 加密/损坏/零页 → 可执行的错误。
func RenderFile(path string, maxPages int, opts Options) (RenderResult, error)

type Options struct {
	DPI       float64  // 默认 150
	FontDirs  []string // 交 UserFontPaths；空 → 用默认探测（见下）
}
```

- 池初始化（`fonts.go`）：`sync.Once` + `single_threaded.Init(single_threaded.Config{LibraryConfig: …})`，
  `GetInstance(30s)`；进程退出不显式 Close（进程级单例；若发现泄漏再加 `CloseAll()`）。
- **字体目录解析顺序**（纯 Go，写成可单测的纯函数 `FontDirs(home string) []string`）：
  ① `pdf.FontDirsEnv = "FOREBRAIN_PDF_FONT_DIRS"`（冒号分隔）→ ② `$FOREBRAIN_HOME/fonts`（Step 3 播种的位置）
  → ③ 二进制旁的 `assets/fonts`（`llm.AssetsDirEnv` 同款探测思路，见 `pkg/llm/assets.go:43`）
  → ④ 空（交给平台默认：macOS/Windows 的系统 CJK 字体）。

  > **替换语义（review 2026-10-14，照上游文档原文，别搞反）**：`LibraryConfig.UserFontPaths` 的注释是
  > 「Array of paths to scan **in place of the defaults**」。也就是说在 Linux 上**一旦设了它，
  > PDFium 就不再扫平台默认字体路径**——只喂我们自带的 Noto Sans SC/TC，会让所有依赖系统字体的 PDF
  > 一起变豆腐块（不只中文）。所以：**非空时要把平台默认目录也一并放进去**
  > （Linux：`/usr/share/fonts`、`/usr/local/share/fonts`、`$XDG_DATA_HOME/fonts`、`~/.fonts` 里存在的那些；
  > macOS/Windows：注释说该字段可能被整个忽略，那就交给系统字体接口）。这一条要有单测
  > （`TestFontDirsIncludePlatformDefaults`：注入一个假的 home + 环境变量，断言返回的列表**同时**含我们的目录与
  > 存在的平台目录），否则"中文能渲染了、英文 PDF 却坏了"这种回归没人会发现。
  > 若 Step 0 第 2 条实测发现本机不设 `UserFontPaths` 也有 CJK，就把默认收窄为"只在自带字体存在时才设"，
  > 并把实测结果写进报告。
- `RenderFile`：`os.ReadFile` → `OpenDocument(&requests.OpenDocument{File: &pdfBytes})`（**用字节，不用路径**，
  避免 WASM 挂载那类路径规则）→ `FPDF_GetPageCount` → 逐页 `RenderPageInDPI` → `png.Encode`
  → `defer` 关文档。页数上限默认 5；`Page.Rendered=false` 的页只报数量。
- 错误文案（可执行）：加密 `"this PDF is password-protected; unlock it first"`；
  损坏 `"this PDF could not be parsed (<底层原文>)"`；零页 `"this PDF has no pages"`。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/pdf/ -run 'TestRender' -count=1` → PASS

### Step 3: 中文字体资产 + 启动播种

1. `scripts/fetch-pdfium-fonts.sh`：下载 **Noto Sans SC Regular** 与 **Noto Sans TC Regular**
   （Google Fonts 官方来源，SIL OFL 1.1），落到 `assets/fonts/`，**校验 SHA256**；
   同时把来源 URL、版本、许可证写进 `assets/fonts/README.md`（**不提交字体本身**，与 tokenizers 同口径；
   若 owner 要求随 npm 包分发，由 Step 6 的打包脚本负责带上）。
2. `pkg/process/seed.go`：照字典/`models.json` 那条路，把字体**播种到 `$FOREBRAIN_HOME/fonts`**
   （启动时、缺失才拷、失败只 `slog.Warn`，与既有播种同一条规则）。
3. `pkg/pdf/fonts.go` 的探测顺序（Step 2）保证：**有 home 字体就用它，没有就让平台兜**。

**Verify**: `scripts/fetch-pdfium-fonts.sh` → `assets/fonts/` 有 2 个 `.otf` 且 SHA256 匹配；
`FOREBRAIN_HOME=$(mktemp -d) CGO_ENABLED=1 go test -tags fts5 ./pkg/process/ -count=1` → PASS

### Step 4: 中文渲染的**机器可判定**验收（tofu 检测）

这是本计划的核心测试。在 `pkg/pdf/render_test.go`：

1. `pkg/pdf/testdata/zh-embedded.pdf` 与 `zh-notembedded.pdf`：由 `scripts/acceptance/make_zh_pdf.py`
   生成（脚本随计划提交，PDF 也提交——**这是全仓唯一的二进制 fixture，理由写在脚本头注释里**）。
   两页内容相同：`中文渲染测试：甲乙丙丁戊己庚辛壬癸`，甲=内嵌字体，乙=引用 `STSong-Light` 不内嵌。
   **两页都用同一个字号与同一行**（后面按坐标切字形格要靠它稳定）。
2. `TestChineseTextIsExtracted`：`instance.GetPageText` 的结果包含 `中文渲染测试`（证明文字层完好）。
   **注意**：文字层与渲染是两条独立的路径——文字能取到**不代表**渲染正确，所以这条只是前置护栏。
3. `TestChineseGlyphsAreNotTofu`（**决定性**）。**不要靠"猜 10 个等宽列"来切字**（字宽、字距、
   kerning 任何一点偏差都会切歪，测试就会假通过/假失败）；用 PDFium 自己给的坐标：
   `instance.GetPageTextStructured`（`Pdfium` 接口里的方法，见官方 API：`GetPageTextStructured`）
   取出第 1 页每个字符的矩形（含 `甲乙丙丁戊己庚辛壬癸` 这 10 个），把渲染出来的 PNG
   按每个字符的矩形裁一格，算**列投影墨迹掩码**（`[]bool`，每列"有墨"为真），
   然后断言这 10 个掩码里**互不相同的至少有 6 个**。
   理由：豆腐块的特征就是"所有缺字都画成同一个方框"——那时 10 个掩码全都相同。
   把"互不相同数"打印出来（报告要这个数字）。
   若某一版 PDFium 的 structured text 拿不到矩形，**退回**等宽切列（把 `中文渲染测试：` 的 6 个字符
   排除掉，只切后面 10 个汉字），并在报告里写明用了哪条路。
4. `TestFontDirsIncludePlatformDefaults`（Step 2 的替换语义）：注入假 home + `FOREBRAIN_PDF_FONT_DIRS`，
   断言 `FontDirs(home)` 的返回值**同时**含我们的目录与（存在的）平台默认目录；平台目录都不存在时
   只返回我们的。
5. `TestRenderCapsPages`：10 页 PDF（脚本再生成一份）→ `Pages` 长度 5、`PageCount` 10。
6. `TestEncryptedPDFIsActionable`：加密 fixture → 错误含 `password-protected`。
7. `TestRenderFileWithoutFontDirsDoesNotPanic`：`Options{}`（不设字体目录）→ 正常返回或返回可执行错误，
   **不 panic**。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/pdf/ -run 'TestChinese' -count=1 -v` → PASS，且把
`TestChineseGlyphsAreNotTofu` 的两个掩码数量（互不相同数 ≥6）贴进报告

### Step 5: `read_file` 的 PDF 分支

改 `pkg/tool/file_tools.go`（与 053 的图片分支并列，**顺序在图片分支之后**）：

```go
if strings.EqualFold(filepath.Ext(abs), ".pdf") {
	// 闸门（053 Step 4）先问一句：会话模型不收图时不渲染，只回文本提示。
	if reason := st.ImageInputRefusal(ctx); reason != "" {
		return "", fmt.Errorf("read_file cannot read PDF %q: %s", abs, reason)
	}
	res, err := pdf.RenderFile(abs, 0, pdf.Options{})
	if err != nil {
		return nil, err // 加密/损坏/零页：可执行错误
	}
	parts := []llm.ContentPart{llm.Text(pdfReadNote(abs, res))}
	for i := range res.Pages {
		part, _, err := llm.ImagePartWithin(abs, "image/png", res.Pages[i].PNG, llm.DefaultMaxImageBytes)
		if err != nil {
			return nil, fmt.Errorf("render pdf page %d: %w", res.Pages[i].Number, err)
		}
		parts = append(parts, part)
	}
	CaptureToolOutput(ctx, pdfReadOutput(abs, in.FilePath, res))
	return parts, nil
}
```

> **闸门与 catalog（与 053 Step 4/4c 同一件事）**：`st.ImageInputRefusal(ctx)` 读的是本会话模型在 catalog 里的
> 条目。owner 2026-10-14 已确认 `deepseek/deepseek-v4-flash` 实际能理解图片（官方把它路由到
> `deepseek-flash`），而 `assets/models.json` 原来写着 `attachment: false` → 053 Step 4c 会把它改对。
> **本计划不要自己再改 catalog**（一处权威副本，改在 053）；但要知道：Step 7 的真机验收只有在
> **053 Step 4c 已落地 + 用全新 scratch home**（才会播种修正后的 catalog）时才会通过。

- `pdfReadNote`：`pdf <abs>: 12 pages, rendered pages 1-5 at 150 dpi (7 not rendered; pass a page range to see more)`
  —— 页数、已渲染范围、未渲染数量都要说清（模型要知道自己没看到全部）。
- `pdfReadOutput`：与 053 的 `imageReadOutput` **同形**，加 `pdf_pages`/`pdf_rendered`/`pdf_page_count`，
  **不含 PNG 字节**。
- 删掉 `validateTextReadableFile` 里的 `.pdf` 拒绝分支（已在上游处理；留着就是不可达代码）。
- 更新既有测试 `file_tools_test.go:252`：改为断言「PDF 返回 parts，且文本 part 里有页数」。
- 模型可见描述：把 053 那句再扩一个词（**一次改完，不要分两次**）：
  `... or an image (png/jpeg/gif/webp) as an image, or a PDF as page images. ...`
- `pkg/tool/format.go` / TUI / web：PDF 步骤沿用 053 的 `preview_kind: "image"` 标记
  （多页时摘要写 `Read report.pdf · PDF 12 页 · 渲染 1-5 页`），**不要**新增一套渲染路径。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run 'TestFileRead' -count=1 -v` → 全 PASS

### Step 6: 打包与跨平台（最费时的一步，按顺序做）

1. **Makefile / Dockerfile**：`fetch-pdfium.sh` + 字体脚本接进构建；容器镜像里 PDFium 是运行时依赖
   （`libpdfium.so` 需随镜像或在镜像内构建），**明确写进 Dockerfile 注释**。
2. **npm 平台包**：`npm/scripts/build-platform-packages.sh` 对每个目标三元组：
   先 `FOREBRAIN_PDFIUM_TARGETS=<triple>` 取对应 PDFium，再交叉编译（cgo 交叉需要对应 CC 工具链，
   照 tokenizers 那条既有做法），并把 `libpdfium` 动态库与 `assets/fonts` 放进包内。
3. **CI/发布**：`.github/workflows/` 的 release 任务加 fetch 步骤；Windows 目标若无法交叉（需要 MSVC/MinGW），
   照 040 的先例处理（Windows 单独 job / 现编），并把结论写进报告。
4. **架构图**：`scripts/package-graph.sh` 重生成 `pkg/architecture/testdata/graph.json`（新包 `pkg/pdf`）；
   若 `TestSameLayerEdgesAreEnumerated`（`cache_test.go:578`）因新边失败，**按测试给出的说明**更新它的枚举表。

**Verify**: 宿主 `make build` 成功；`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1` PASS；
`git diff --stat pkg/architecture/testdata/graph.json` 显示新包与 `pkg/tool → pkg/pdf` 的边

### Step 7: 真机验收（中文 PDF 端到端）

新建 `scripts/acceptance/read_pdf.sh`（骨架同 053 的 `read_image.sh`：`set -euo pipefail`、`trap cleanup`、
home 用 `FOREBRAIN_ACCEPT_HOME`/`mktemp -d`，**绝不读 `FOREBRAIN_HOME`**）：

- 用 `scripts/acceptance/make_zh_pdf.py` 生成两份中文 PDF（内嵌 / 不内嵌）到 `$WORK`。
- 真模型 + 真 TUI（tmux，`FOREBRAIN_HOME=$HOME_DIR`），模型显式取
  `FOREBRAIN_E2E_MODEL="${FOREBRAIN_E2E_MODEL:-deepseek-v4-flash}"`（owner 的真实默认；配
  `provider: deepseek` / `base_url: https://api.deepseek.com`），并在脚本首行回显模型与 home；输入：
  `read_file $WORK/zh-notembedded.pdf 然后逐字念出页面上的中文内容`。
- 断言：① 步骤摘要含 `PDF` 与页数；② 回复里的中文与 PDF 上的字符串**逐字一致**
  （模型若只看到豆腐块，不可能复述出 `甲乙丙丁戊己庚辛壬癸`）；③ 库里那条工具消息 `parts_json` 含
  `"type":"image"`；④ 内嵌那份同样通过。
- 失败时 `tail` 出日志；`$WORK` 里的渲染 PNG 保留路径打印出来供人工看。

**Verify**: `FOREBRAIN_E2E_DEEPSEEK_KEY=<operator 提供> scripts/acceptance/read_pdf.sh` → `read pdf: PASS`

### Step 8: 全量闸门 + 索引

1. `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → 所有包 ok
   （已知既有失败：`pkg/mcp` 的 `TestStartStdioConnectsRealMCPServerProcess`；出现别的失败 → 停下报告）。
2. `go vet ./...`、`gofmt -l cmd pkg third_party` 干净；
   `git status` 里每个改动都能对上 Scope。
3. `plans/README.md`：054 状态行 + 「最新审核信息」记录 Step 0 的三条结论、Step 4 的掩码数、
   Step 7 的 PASS、以及**二进制体积变化**（`ls -l build/bin/forebrain` 前后对比，PDFium 是动态库时记 `.so` 大小）。

## Test plan

- `pkg/pdf/render_test.go`：Step 4 的 6 条（文本抽取、**非 tofu**、无字体目录不 panic、页数上限、加密可执行错误、
  零页可执行错误）。
- `pkg/tool/file_tools_test.go`：PDF 返回 parts（文本含页数与已渲染范围）+ 加密 PDF 的错误文案 +
  闸门拒绝时**只回文本**（不渲染）。
- `pkg/process`：字体播种（home 里出现 `fonts/NotoSansSC-Regular.otf`；已存在则不覆盖）。
- `scripts/acceptance/make_zh_pdf.py`：可重复生成两份 fixture（`--embed` / `--no-embed`），
  头注释写明来源、许可（Noto OFL）与「为什么必须提交二进制 fixture」。
- 全量：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1`。

## Done criteria

- [ ] `scripts/fetch-pdfium.sh` 与 `scripts/fetch-pdfium-fonts.sh` 幂等、带 SHA256 校验
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/pdf/ -count=1` 全 PASS，**含 `TestChineseGlyphsAreNotTofu`**
- [ ] `grep -n "cannot read PDF files as text" pkg/tool/file_tools.go` → 无匹配
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1` PASS，`graph.json` 已重生成
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 通过（除已知 `pkg/mcp` 失败）
- [ ] `scripts/acceptance/read_pdf.sh` 真机 `PASS`（内嵌与非内嵌两份中文 PDF 都被逐字复述）
- [ ] 报告含：Step 0 三条证据、Step 4 掩码数、二进制/动态库体积、Windows 目标的处理方式
- [ ] `plans/README.md` 状态行已更新；工作区**没有** commit

## STOP conditions

Stop and report（不要自己 improvis）:

- **Step 0 第 2 条不成立**：带上 `UserFontPaths` 后，不内嵌中文的 PDF **仍然**渲染成豆腐块
  （说明 `UserFontPaths` 在该平台被忽略、且平台又没有 CJK 系统字体）。这条是本计划的立命之本，
  立刻停下把 PNG 与 `FC_DEBUG`/字体探测结果交给 owner，不要换库硬凑。
- **Step 0 第 1 条不成立**：本机无法链接 PDFium（缺 `pdfium.pc`/符号不匹配）→ 停下问 owner
  是否接受别的字体方案或别的引擎，不要临时改用 AGPL 的 MuPDF。**MuPDF/AGPL 一律不可引入**（本仓库 Apache-2.0）。
- **任一发布目标拿不到 PDFium 二进制**（例如 Windows arm64）→ 停下报告该目标的现状，
  不要在 CI 里下架目标或静默跳过。
- Step 6 发现交叉编译 cgo 需要本机没有的工具链 → 停下报告，给出「哪些目标能在 CI 上做」的清单。
- 你的改动让 `pkg/tool` 的既有文本/图片用例变红（说明 PDF 分支插错位置）。
- **真机验收里 `read_file` 对 PDF 返回的是"模型不收图"的拒绝文案**（而不是页图）：那是 053 的闸门在生效
  ——先确认 053 Step 4c 已落地、用的 home 是全新的、且会话模型确实是 `deepseek-v4-flash`；
  三条都对还拒绝，**停下报告**，不要在本计划里改闸门或 catalog。

## Maintenance notes

- **许可证**：`go-pdfium`（MIT）、PDFium（Apache-2.0）、Noto Sans SC/TC（SIL OFL 1.1）。
  **永远不要**因为「MuPDF 更方便」而引入 AGPL——本仓库是 Apache-2.0（`LICENSE`）。
- **字体分发**：home 级播种（plan 047 的路），与字典/models.json 同一条规则：
  脚本只负责**打包时带上**，产品**启动时自己播种**；安装脚本不写 `$FOREBRAIN_HOME`。
- **页数上限**（默认 5）与 DPI（默认 150）是**成本旋钮**：一页 A4@150dpi ≈ 1240×1754、约 200–400 KB，
  与 053 的 3 MiB 单图预算在同一量级。要改就走 `pdf.Options`，不要在调用点硬编码。
- **二进制体积**：PDFium 是原生库（动态链接时随包分发，静态链接时进二进制）。若体积成为问题，
  正确方向是**按需下载 / 平台包分离**（像字典那样），不是把它塞进 `go:embed`。
- **架构**：新增 `pkg/pdf` 让包数到 29/30，`maxPackageCount` 只剩一个空位；
  再加包前先谈，或在 `pkg/pdf` 内扩展。
- **与 053 的边界**：图片编码、能力闸门、surface 标记都在 053；本计划只做「PDF → 页图」，
  并复用它们。改 PDF 相关文案时**同时**看 053 的 `imageReadNote`/`imageReadOutput` 是否仍同形。
