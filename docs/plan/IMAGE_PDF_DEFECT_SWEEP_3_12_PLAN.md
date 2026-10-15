# 计划：修复 images/PDF 特性审计问题 3–12（dev 分支）· v2（评审修订版）

> 本计划由 `improve plan` 变体产出。所有 file:line 均已在 **dev @ a6bf864** 上逐一核实。
> v2 变更：并入评审意见（12 条）与 owner 两条裁决——**① $home 下被用户替换过的字体不得自动修复（用户自定义覆盖合法）**；**② oversized/webp 的 MCP 场景纳入真机验收**。
> 评审意见核查结果：#2（seed 既有测试冲突）、#3（surface 元数据造假）、#4（断言选择器）、#5（fixture 只有 64×64 PNG）、#6（PDF 页循环同形缺陷）、#9（前端测试已存在）成立并采纳；#1 的「现有签名已有 4 参」与 #6 的 `:516-521` 行号经核查不实（实际：`extractResultImageParts(resultsDir, serverName, raw string)` 3 参，PDF 页循环在 `file_tools.go:158-163`），但其实质要求（计划写明确切签名、穷举同形调用点）已采纳。
> 实施时第一步把本计划落到 `docs/plan/IMAGE_PDF_DEFECT_SWEEP_3_12_PLAN.md`（仓库惯例），本文件为审批正本。

## 1. 目标（Why this matters）

images/PDF 特性（c506cf4 + 6d1730e）落地后的分支审计发现 10 个缺陷：2 个正确性缺陷会让 MCP 图片工具结果整体丢失（#3/#4）、1 个安全上限缺失（#7）、3 个「文档/常量/路径撒谎」的正确性缺陷（#5/#9/#12）、1 个架构棘轮漏洞（#10）、2 个 web/TUI 一致性缺陷（#6/#11）、1 个验收脚本证明力缺陷（#8）。全部为 S 规模、LOW 修复风险，共享修复面集中，一次修完。

## 2. 根因（file:line 证据链，当前树已核实）

### #3 MCP 一个图片块失败 → 整条工具结果变错误，文本全丢

`pkg/mcp/result_processing.go:127-132`：

```go
case "image/jpeg", "image/png", "image/gif", "image/webp":
    part, res, err := llm.ImagePartWithin(path, mimeType, decoded, llm.DefaultMaxImageBytes)
    if err != nil {
        return "", nil, nil, err        // ← 整条结果作废
    }
```

`llm.ImagePartWithin`（pkg/llm/image_scale.go:58-135）有三类可复现失败：>48 MiB 拒收（:61-63）、webp 超预算无法降采样（:83-84）、全阶梯降采样后仍超预算（:133-135）。错误向上传播：`bridge.go:1143-1145` → `run/orchestration_llm.go:1504-1509` 把 parts 换成 `Error executing tool 'x': …`——**连文本信封一起丢**。

同一函数对「写盘失败」（:114-120，替换为 "could not be saved" 文本行）和「MIME 不支持」（switch default：已落盘、已命名、无 part）都刻意降级，唯此处不降级。且文件在 `:113` 已落盘，失败后模型永不知晓该路径（孤儿文件）。

**同形缺陷（评审 #6，穷举同类）**：`pkg/tool/file_tools.go:158-163` 的 PDF 页循环逐页调 `llm.ImagePartWithin`，任一页失败 `return nil, fmt.Errorf("render pdf page %d: …")` —— note 与全部已编码页部件一起丢弃，且各页 PNG 已在 `storePDFPages`（:152）落盘成孤儿。触发面：病态页（超噪/超大尺寸）降采样阶梯走尽仍超 3 MiB 预算。

### #4 图片闸门顺序错：文本模型拿到错误而不是信封

`pkg/mcp/result_processing.go:48-57`：先 `extractResultImageParts`（可因 #3 失败），`imageRefusal` 在 `:55-57` 才丢弃 parts。`bridge.go:1117-1119` 的注释承诺 "imageRefusal suppresses the image parts **without failing the call**"——当前实现撒谎：拒绝图片的会话遇到 #3 场景照样整条失败。

### #5 打包字体回退路径永远命中不了

`pkg/pdf/fonts.go:139`：`return filepath.Join(filepath.Dir(executable), "assets", "fonts")`。
而仓库所有打包都把字体放 `<binDir>/fonts`：`pkg/llm/assets.go:40` `assetEntries = […, "fonts"]` + `InstallAssets`（:67-75）装到 `filepath.Join(binDir, name)`；`llm.AssetsDir()`（:46-59）= binDir 本身（env 覆盖时 = env 目录）。Dockerfile:55 `COPY /out/fonts /app/fonts`；npm 打包与 install-assets.sh 同走 `cmd/installassets` → llm.InstallAssets。
⇒ 打包安装里 `$home/fonts` 播种失败（seed.go:44-46 只 slog.Warn）时，PDFium 回退平台扫描，未内嵌 CJK 字体的 PDF 渲染成豆腐块，而自带字体就在二进制旁边读不到。`assetsFontsDir()` 还重复实现了 `llm.AssetsDir`（含 FOREBRAIN_ASSETS_DIR 覆盖与 EvalSymlinks，env 分支语义碰巧一致）。

### #6 技能工坊卡片缺 `:scope` → 缩略图静默消失

`frontend/src/views/WorkshopView.vue:39`：

```html
<ToolCallCard v-else-if="block.kind === 'tool'" class="my-2" :step="block.step" :default-open="false" />
```

`ChatView.vue:218-222` 传 `:scope="workbenchScope"`、`SubagentConversation.vue:76` 传 `:scope="scope"`。`ToolCallCard.vue:87-88`：无 scope → `previewUrl` 返回 `''` → 缩略图静默不渲染。**WorkshopView 自己已有 `workbenchScope`**（:312 定义，:78/:132 已给其他组件传过），只是 :39 漏传。

### #7 解码前没有任何长度上限

`pkg/mcp/result_processing.go:104`（图片块）与 `:277`（resource blob，既有同类）都是 `base64.StdEncoding.DecodeString` 直接解码后整份写盘（:113/:283）；48 MiB 天花板（`llm.MaxImageDecodeBytes`，image_scale.go:27）在那之后才生效。被配置的第三方 MCP 服务端可用一个超大 `data` 撑内存（解码副本翻倍）+ 磁盘（state/mcp-results 落盘）。

### #8 验收脚本证明不了「像素真到了模型」

`scripts/acceptance/read_image.sh`：
- MCP phase（:315-359）把「磁盘出现 .png」当真值（:321-325 等 `ls $MCP_RESULTS/*.png`、:348-353 断言 `ws_saved` 非空）——而文件在部件构造**之前**就写出（正是 #3 的场景：落盘成功 + ImagePartWithin 失败 = 模型没拿到像素，脚本仍 PASS）。**从未断言 MCP 工具行的 parts 含 `{"type":"image"}`**（Phase 1 的 `db_last_image_tool_parts` :195-198/:270-285 只覆盖 read_file）。
- `assert_no_approval_card`（:224-232）用 `pane_all`（:159 `capture-pane -S -5000` 全 scrollback）判「无审批卡」——模型自跑 shell 留下的旧审批卡会误诊；`read_pdf.sh:215-227` 的 `assert_no_card` 已改成**按完成时刻的 capture** 判定。

### #9 pkg/pdf 文档/常量撒谎 + 死字段

- `pdf/doc.go:7-8` 仍写 "RenderFile renders the first DefaultMaxPages pages"——`render.go:57-62` 已支持 `start` 起始页。
- `fonts.go:22-25` `GetInstanceTimeout = 30s` 注释承诺 "bounds how long RenderFile waits for a PDFium instance… a concurrent render waits here"——库层实测（模块缓存 `go-pdfium@v1.21.3/single_threaded/single_threaded.go:58-60`）：`GetInstance(timeout)` 直接 `GetInstanceWithContext(goctx.Background())`，timeout 被丢弃；且实例获取本就不等待（真正串行化在页渲染的进程级互斥里，不可取消）。
- `render.go:31-37` `Page.Rendered` 恒为 `true`（循环只追加渲染成功的页，超帽页由 `PageCount` 报告；:27-30 的注释自己承认这一点）。全仓 grep 无外部消费方（仅 `render_test.go:364`）。
- `render.go:46-55` `Options` 文档说 "tunes one RenderFile call"，而 `FontDirs` 实际只在进程首次渲染生效（`:52-53` 有说明但与 Options 总述自相矛盾）。

### #10 新包 pkg/pdf 未进 fan-out 棘轮

`pkg/architecture/cache_test.go:169-226` `fanOutBudgets` 表**没有 `"pdf"` 键**，而 `:366-374` `packageLayer` 已有 `"pdf": 2`；`TestFanOutOnlyShrinks` 只遍历 `fanOutBudgets` ⇒ pdf 的依赖入口不受任何棘轮约束。其 `packageLayer` 注释还断言 "It imports no other in-module package"——本计划 #5 修复后该断言失真。
顺带：`cache_test.go:146` "Four packages are over their §3.7 target today"——tool 升到 9/8 后实际 **五** 个超限（tui、gateway、tool、assembly、run）；`"tool": {current: 9, target: 8}`（:180）缺 assembly（:222）那样的 `// over by one` 注记。

### #11 web 卡片丢「已缩放/字节数」，缺尺寸时 alt 变 (0×0)

`useChatStream.ts:373-381` `previewFieldsFromMeta` 已解析 `preview_mime/preview_bytes`，但 **不解析 `preview_scaled`**（事件侧存在：`pkg/event/tool_events.go:92`、gateway `wsevents.go:504`、`server.go:374`；TUI 侧 `pkg/tui/image_open.go:101-107` 显示 `▤ 名字 · WxH · 字节 · scaled`）。`ToolCallCard.vue` 只消费 4 个 preview 字段；`:96-103` previewAlt 的 `?? 0` 让未知尺寸渲染成 `shot.png (0×0)`（locale `chat.toolImageAlt`：`'读取的图片 {name}（{width}×{height}）'`，index.ts:1048/2417）。

### #12 字体播种只 stat、不校验、非原子写

`pkg/process/seed.go:74-81`：`os.Stat(out)` 成功即跳过；`copyFont`（:96-113）直接 `os.Create` 非原子写。一次中断（磁盘满/被杀）留下截断 .otf，此后**永久**视为已播种且无提示。对照既有模式：词典按校验和刷新 + 原子写（`pkg/memory/dictionary.go:242-258` CreateTemp+Rename）。
**既有契约（评审 #2，seed_test.go:36-49 钉住）**：「A home copy that already exists is left alone」——用户替换过的字体是合法自定义覆盖，**不得**被自动「修复」。

## 3. 修复设计（含行为变化矩阵）

### F1（#3+#4）`pkg/mcp/result_processing.go`：闸门前移 + 图片块失败降级

确切签名变更（当前 `result_processing.go:77`）：

```go
// 现在
func extractResultImageParts(resultsDir, serverName, raw string) (string, []llm.ContentPart, map[string]any, error)
// 改为（新增最后一个参数；调用点仅 :48 一处）
func extractResultImageParts(resultsDir, serverName, raw string, wantParts bool) (string, []llm.ContentPart, map[string]any, error)
```

`ToolResultContent` 内 `wantParts := strings.TrimSpace(imageRefusal) == ""`。`processToolResultJSON` 签名不变。每个 image 块的处理顺序：

1. **F2 守卫**（见下）先行；
2. base64 解码（失败 continue，现状不变 ：105-107）；MIME 解析（不变）；
3. 落盘 `persistMCPResult`（失败 → "could not be saved" 降级行，现状不变 :114-120）；
4. `wantParts == true`：调 `llm.ImagePartWithin`；**失败不再 `return err`**，替换文本行写
   `Binary content saved to %s (%s, %d bytes) but was not sent as an image: %v.`，surface 用新的 `tool.ImageFileOutput`（文件事实），继续处理其余块；
   `wantParts == false`：**不调用** `ImagePartWithin`（不跑降采样/编码），替换文本行保持现有 `Binary content saved to %s (%s, %d bytes).`，surface 用 `tool.ImageFileOutput`。

**`tool.ImageFileOutput`（新增于 pkg/tool/file_tools.go，ImageReadOutput 旁）——评审 #3 采纳，不再伪造 ImageReadResult**：

```go
// ImageFileOutput is the surface-facing map for an image file that was saved
// but not sent as a part: a refused or degraded block. It states file facts
// only — the on-disk size and the header dimensions when they parsed — and
// claims neither a send nor a scale. The keys are the surface contract
// (event → gateway → TUI/web all read them); the values are what is honest.
func ImageFileOutput(abs, filePath, mime string, width, height, size int) map[string]any
```

键同 `ImageReadOutput`（preview_kind/preview_text/abs_path/file_path/image_mime/image_bytes/image_source_bytes/image_width/image_height/image_scaled/result_lines），`image_bytes`/`image_source_bytes` = 落盘字节数（真实），`image_scaled`=false（真实：我们没做缩放），尺寸来自新导出的 **`llm.DecodeImageConfig(data []byte) (width, height int)`**（包装 image_scale.go 已有的 `decodeImageConfig`，仅解析头、不解码像素；webp 等标准库不认识的格式返回 0,0——web 卡片由 F6 优雅降级）。成功块仍用 `tool.ImageReadOutput(path, path, res)` 不变。字段名不改为新名族：`image_*` 键是事件→网关→TUI/web 的既有共享契约，改名会波及四面；诚实性由值与文档注释承载。

| 场景 | 旧行为 | 新行为（刻意修订） |
|---|---|---|
| 多模态 + 一个坏块（>48MiB/webp 超预算/降采样失败）+ 其余好块 | 整条结果变 `Error executing tool`，文本全丢，落盘文件成孤儿 | 文本信封 + 其余好块的 image part；坏块变「已存盘但未发送+原因」文本行 |
| 文本模型（imageRefusal）+ 坏图片 | **整条失败**（#4 bug） | 信封正常送达（F2 守卫在头部解析前；坏载荷不解码不落盘） |
| 文本模型（imageRefusal）+ 正常图片 | 信封（但先付了整条抽取成本，且失败场景同上） | 信封；只做头部解析（无降采样/编码开销），surface 给文件事实 |
| surface（缩略图元数据） | 仅成功块；失败=无 surface | 成功块照旧（ImageReadOutput）；坏块/拒绝块给文件事实（ImageFileOutput，尺寸 0 时 web 端省略） |

`bridge.go:1115-1119` 的注释承诺从此为真（bridge 不改）。

### F1b（#3 同形，评审 #6）`pkg/tool/file_tools.go:158-163`：PDF 页循环降级

抽出小函数（可测）：

```go
// appendPDFPagePart appends page's image part, or — when the part cannot be
// sent — a text line naming the stored PNG and the reason, so one bad page
// never costs the note or the pages that did encode.
func appendPDFPagePart(parts []llm.ContentPart, path string, page pdf.Page) []llm.ContentPart
```

`ImagePartWithin` 失败 → append `llm.Text(fmt.Sprintf("Page %d was rendered to %s but was not sent as an image: %v.", page.Number, path, err))`，循环继续。`read_file` 单图路径（:350-359）**保持 error-as-is**：那个调用的全部目的就是这一张图，错误文案本身可操作（"convert it to png first"），且没有其他内容会被连累——与 MCP「多块结果被单块毁掉」不同形，不改（Scope 表中注明）。

### F2（#7）解码前长度上限（图片块 + resource blob 同修，穷举同类）

`pkg/mcp/result_processing.go` 新增守卫（两处调用点 `:104`、`:277` 之前）：

```go
// maxDecodedBlockBytes is the largest single MCP block we materialise; the
// image ceiling matches llm.MaxImageDecodeBytes, the largest image we decode.
const maxDecodedBlockBytes = 48 << 20

func decodedSizeWithin(encoded string) bool {
    return base64.StdEncoding.DecodedLen(len(encoded)) <= maxDecodedBlockBytes
}
```

超限块不解码、不落盘，替换文本行 `Binary content (about %d MiB) exceeds the 48 MiB per-block limit and was not saved.`（估算值 = DecodedLen；精确值只有解码后才知道——而解码正是要避免的）。
诚实边界（写进代码注释）：`raw` 本身已被 MCP 传输层缓冲，本守卫消除的是**解码副本翻倍**与**无上限落盘**，不是传输层内存（SDK 的领域，不在本期）。

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| 单块 data 解码后 >48 MiB | 解码 96MiB 级内存副本 + 整份落盘 | 不解码不落盘，信封给降级行 |
| resource blob >48 MiB | 同上（既有同类） | 同上 |

**决策点 D1（默认：不在本期）**：state/mcp-results 目录的保留/回收策略（影响所有结果包括文本，属产品生命周期策略）。本守卫已把单块爆炸半径封顶；回收策略建议另立计划，owner 裁定。

### F3（#5）`pkg/pdf/fonts.go`：回退路径归一到 `llm.AssetsDir`

删除 `assetsFontsDir()`（:124-140），`FontDirs` 改用 `llm.AssetsDir()`：

```go
func FontDirs(home string) []string {
    assets, err := llm.AssetsDir()
    if err != nil { assets = "" }
    return fontDirs(runtime.GOOS, home, os.Getenv, joinFonts(assets))
}
```

（`joinFonts("")` 返回 `""`，保持 ownFontDirs 的空串语义。）`pkg/pdf` 由此 import `pkg/llm`：fan_out 0→1，Layer 2→0 合法（`TestLayerCeiling` 允许严格低层）。**与 F7 联动**（棘轮补 pdf 条目 + graph.json 重生成）。

### F4（#6）`WorkshopView.vue:39` 补 `:scope="workbenchScope"`

一行改动，复用 :312 已有 computed。

### F5（#9）`pkg/pdf` 文档对齐 + 死代码删除

- `doc.go` 重写 RenderFile 段：`renders up to DefaultMaxPages pages starting at the 1-based start page`。
- 删 `GetInstanceTimeout`（fonts.go:22-25）；`render.go:86` 改 `renderPool(dirs).GetInstanceWithContext(ctx)`，注释如实陈述：单线程池的实例获取不等待；页渲染由 PDFium 进程级互斥串行化且**不可取消**——一个病态 PDF 会阻塞本进程内所有 PDF 读取，这是库属性（cgo 调用无法被 context 打断），如实记录而非假装有超时。
- 删 `Page.Rendered` 字段（render.go:36 及 :137 赋值；`render_test.go:364` 断言同步删）。
- `Options` 文档改为明确哪些是 per-call（DPI）、哪些进程级仅首次生效（FontDirs）；`render_test.go:521` 的 `GetInstanceTimeout` 引用同步改。
- pkg/pdf 的 TestMain 已无条件 `os.Setenv("FOREBRAIN_HOME", …)`（覆盖继承值，符合 House rules）；新增 fonts_test.go 沿用 t.Setenv/t.TempDir（覆盖式），不引入新的补空式 env。

### F6（#11）web 卡片补 scaled/bytes + alt 优雅降级

- `useChatStream.ts`：`SubagentToolStep` 增加 `previewScaled?: boolean`（:184-189 区域）；`previewFieldsFromMeta` 的 Pick（:373）加 `'previewScaled'`，解析 `meta['preview_scaled']`（布尔）。
- `ToolCallCard.vue`：缩略图下加一行 muted 元数据（复用既有 muted-text 原子，不引入新视觉元素）：`{width}×{height} · {bytes} · scaled`——尺寸/字节未知则省略对应段，`scaled` 仅在 true 时出现（对齐 TUI `▤ 名字 · WxH · 字节 · scaled`）。`image_bytes` 语义按块类型诚实：成功块=已发送编码字节，拒绝/降级块=落盘字节。
- `previewAlt`：尺寸未知（0/undefined）时用无尺寸变体 locale `chat.toolImageAltNoDims`（`读取的图片 {name}` / `Read image {name}`），已知时用现有 key。
- 字节格式化：复用前端既有人类可读字节 helper（若无则写一个 B/KB/MB 的纯函数并测试，不引新依赖）。
- 测试：扩展现有 `ToolCallCard.test.ts` 与 `useChatStream.test.ts`（两者均已存在，评审 #9）——preview_scaled 解析、bytes/scaled 渲染与省略、0 尺寸 alt 用新 locale。

### F7（#10）`pkg/architecture/cache_test.go`：pdf 进棘轮 + 注释对齐

- `fanOutBudgets` 增加 `"pdf": {current: <F3 后实测, 预期 1>, target: 8}`（Layer-2 同侪口径），条目注释说明 pdf 是工具层的枚举渲染依赖、经 F3 依赖 llm 的资产位置契约。
- `packageLayer` 的 pdf 注释 "It imports no other in-module package" 改为如实（imports pkg/llm for the shared asset-location contract）。
- `:146` "Four packages" → "Five packages"；`"tool"` 条目补 `// over by one`。

### F8（#12，按 owner 裁决修订）字体播种：原子写 + 截断签名修复，用户替换永不触碰

owner 裁决：**$home 下被用户替换过的字体是合法自定义覆盖，不得自动修复**。据此，修复目标收窄为「我们自己中断的写」——它有一个可检验的签名：**截断副本是源字节的真前缀**；用户换入自己的字体文件则与源字节以其他方式不同（非前缀）。设计：

- **原子写根修**：`copyFont` 改 `os.CreateTemp(dest, ".font-*")` + `os.Rename`（对齐 `dictionary.go:242-258`）——今后我们的写要么完整要么不存在，不再产生新的截断文件。
- **每文件判定**（替代 :74-81 的 stat-跳过）：
  1. 目的缺失 → 原子写入；
  2. 与源字节相同（尺寸+内容）→ 跳过，**mtime 不动**；
  3. `len(dst) < len(src)` 且 `dst == src[:len(dst)]`（截断签名）→ 我们中断的写 → 原子重写修复；
  4. 其他任何差异 → **用户自定义覆盖，永不触碰、不告警**（沉默是正确行为）。
- 既有测试 `TestSeedFontsCopiesShippedFontsOnce` 的 "user replaced" 用例（内容 "user replaced"，非源前缀）走分支 4，**保持绿不改**；新增用例：「截断前缀文件被修复为完整源」「相同内容第二次播种 mtime 不变」。

### F9（#8 + owner 裁决②）read_image.sh：断言「像素真到了模型」+ 按时刻判审批卡 + oversized/webp 真机场景

**脚本断言修复：**
- MCP phase 关闭判定后新增断言：取 **MCP 行本身**的 parts（此时 read-back 尚未提交，`mcp_tool_id` 即 MCP 行）：
  `dbq "SELECT COALESCE(parts,'') FROM fb_messages WHERE role='tool' AND id = $mcp_tool_id;"` 必须含 image 块。断言用**空白容忍正则** `'"type"[[:space:]]*:[[:space:]]*"image"'`（与 Phase 1 :276 相同；不用精确串 `{"type":"image"}`——JSON 键序/空白不定，评审 #4），计数用 `grep -o | wc -l`。
- `assert_no_approval_card` 改成 `read_pdf.sh` 的 `assert_no_card <desc> <capture>` 形态：MCP phase 用 `04-mcp-image.txt` capture、read-back phase 用 `05-read-mcp-image.txt` capture 判定（capture 已存在于 :340/:380，只是没用于判定）。
- 磁盘断言保留（仍验证落盘位置），但不再作为「像素到了模型」的真值。

**owner 裁决②——oversized/webp 的 MCP 场景纳入真机验收：**

`scripts/acceptance/fake_mcp_stdio.py` 新增两个 mode（现只有 text/image；image 模式返回固定的 64×64 纯色 PNG，评审 #5 属实——所以降级/守卫路径需要新 fixture）：

- `image_mixed`：`[text 块 "mixed shot", image/webp 约 4MiB（RIFF/WEBP 头 + 填充字节，超 3MiB 预算 → ImagePartWithin 在 webp 分支报错，标准库无 webp 解码器故不需要真实可解码 webp）, image/png 好块 64×64]` → 证明 #3 降级：坏块不毁整条。
- `image_oversize`：`[text 块, image/png base64 解码后 >48MiB（确定性零字节填充）, image/png 好块]` → 证明 #7 守卫：不解码不落盘，好块照常送达。

read_image.sh 相应扩展（scratch config 增加两个 server 条目 `fake-shots-mixed`/`fake-shots-oversize`，同样 default_tools_approval_mode: approve）：

- **Phase 3b（多模态会话，mixed）**：调用 mixed 工具 → 断言：无 `Error executing tool`；该 MCP 行 parts 恰含 **1** 个 image 块（好 png）；行 content 含 `was not sent as an image`；webp 已落盘（磁盘断言）但模型只拿到 1 个 part。
- **Phase 3c（多模态会话，oversize）**：调用 oversize 工具 → 断言：无 `Error executing tool`；parts 恰含 1 个 image 块；行 content 含 `exceeds the 48 MiB per-block limit`；`$MCP_RESULTS` 下**没有**新增的超大文件（只 ls 出小文件）。
- **Phase 5b（文本模型会话，#4 的端到端）**：在现有 TEXT_ONLY_MODEL 会话上调用 image 模式的 MCP 工具 → 断言：无 provider 400/错误行；该行 parts **不含** image 块；行 content 含 `Binary content saved to`（信封送达）。
- 新 phase 的审批卡判定一律用按时刻 capture（上述 assert_no_card 形态）；信封都很小（几百字节），不会触发 MAX_MCP_OUTPUT_TOKENS 截断干扰断言。

## 4. Scope

**In scope：**
`pkg/mcp/result_processing.go`、`pkg/mcp/result_processing_test.go`、`pkg/llm/image_scale.go`（仅导出 DecodeImageConfig）、`pkg/tool/file_tools.go`（ImageFileOutput + appendPDFPagePart）、`pkg/tool` 对应测试、`pkg/pdf/fonts.go`、`pkg/pdf/render.go`、`pkg/pdf/doc.go`、`pkg/pdf/render_test.go`、`pkg/pdf/fonts_test.go`（新增）、`pkg/process/seed.go`、`pkg/process/seed_test.go`、`pkg/architecture/cache_test.go`、`pkg/architecture/testdata/graph.json`（重生成）、`frontend/src/views/WorkshopView.vue`、`frontend/src/components/chat/ToolCallCard.vue`（+现有 ToolCallCard.test.ts）、`frontend/src/composables/useChatStream.ts`（+现有 useChatStream.test.ts）、`frontend/src/locales/index.ts`、`scripts/acceptance/read_image.sh`、`scripts/acceptance/fake_mcp_stdio.py`。

**Out of scope（附理由）：**
- `pkg/mcp/bridge.go`、`pkg/run/orchestration_llm.go`——F1 在源头降级后其语义不变、注释变真，无需改动。
- **`read_file` 单图路径的 error-as-is（file_tools.go:350-359）**——单图调用无其他内容可连累，错误文案可操作，与 MCP 多块毁整条不同形（评审 #6 只指认 PDF 页循环）。
- state/mcp-results 回收策略——见决策点 D1。
- read_file 本地大图/大 PDF 的读取上限——本地文件是用户显式指定路径，威胁模型不同（MCP 是被配置的第三方服务端）。
- go-pdfium 库本身的超时/取消（third_party，cgo 属性）——如实文档化（F5）。
- read_pdf.sh、make_zh_pdf.py——已是按时刻判定，无需改。

## 5. Steps（按序，每步带验证）

0. 本计划落盘 `docs/plan/IMAGE_PDF_DEFECT_SWEEP_3_12_PLAN.md`（内容=本文件）。
1. **F1+F2**（同一文件）：改 `result_processing.go`（wantParts 第 4 参、降级、守卫、`llm.DecodeImageConfig` 导出、`tool.ImageFileOutput` 新增）；新增回归测试：
   - `TestToolResultContentDegradesUnsendableImageBlock`：`[text 块, image/webp+4MiB 假数据(超 3MiB 预算、mime 分支短路不解码), 合法 1px PNG]` → 无 error、webp 块行含 "was not sent as an image"、PNG 块产出 1 个 image part、文本块保留、surface 为 ImageFileOutput（bytes=落盘字节数）。
   - `TestToolResultContentRefusalNeverFails`：同 webp 载荷 + imageRefusal 非空 → 无 error、无 image part、信封含 saved-path 行、surface 尺寸为 0（假 webp 头不可解析）。
   - `TestToolResultContentRefusalParsesHeaderForSurface`：imageRefusal + 合法 PNG → surface image_width/height = 真实头尺寸（证明没伪造）。
   - `TestToolResultContentRefusesOversizedBlock`：base64 使 DecodedLen > 48MiB（`bytes.Repeat` ~64MiB 字符串，CI 可承受）→ 无落盘文件、信封含 "exceeds the 48 MiB per-block limit"、无 part；resource blob 路径同构用例。
   - 验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/mcp -run 'ToolResultContent' -count=1` → ok。
2. **F1b**：`appendPDFPagePart` 抽取 + 降级；测试：合法小 PNG → image part；4MiB 垃圾字节标 image/png（ImagePartWithin 在 decode 分支报错）→ 文本 part 含 "was not sent as an image" 与已存路径。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'PDFPage' -count=1` → ok。
3. **F3+F5**（pkg/pdf）：fonts.go 删 assetsFontsDir 用 llm.AssetsDir；删 GetInstanceTimeout、Page.Rendered；doc.go/Options 文档重写；`GetInstanceWithContext(ctx)`；render_test.go 同步（:364、:521）+ 新增 `fonts_test.go` 表测（env > home > binDir/fonts 顺序、binDir/fonts 可命中）。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/pdf -count=1` → ok。
4. **F8**：seed.go 原子写 + 四分支判定；seed_test.go 新增「截断前缀被修复」「相同内容 mtime 不变」两用例，既有 "user replaced" 用例保持不改且绿。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run Seed -count=1` → ok。
5. **F7**：cache_test.go 棘轮 + 注释；随后 `scripts/package-graph.sh` 重生成 graph.json（pdf fan_out 0→1）。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → ok；重跑脚本幂等（`git diff` 稳定）。
6. **F4+F6**（前端）：WorkshopView :39 补 scope；useChatStream previewScaled；ToolCallCard 元数据行 + alt 无尺寸变体；locales zh/en 两个 key；扩展 ToolCallCard.test.ts 与 useChatStream.test.ts。
   验证：`cd frontend && pnpm exec vue-tsc --noEmit && pnpm test` → 全绿。
7. **F9**：fake_mcp_stdio.py 增 image_mixed/image_oversize 两 mode；read_image.sh 断言改造 + Phase 3b/3c/5b。
   验证：`bash -n scripts/acceptance/read_image.sh` 与 `python3 -m py_compile scripts/acceptance/fake_mcp_stdio.py` → 无输出。
8. **全量回归**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` + `go vet ./...` + `gofmt -l cmd pkg third_party`（空）+ `git status` 改动范围与 Scope 一致。
9. **真机验收（真实 provider）**：key 从 `~/.forebrain/.env` 取（FOREBRAIN_E2E_DEEPSEEK_KEY）：
   - `FOREBRAIN_E2E_DEEPSEEK_KEY=… scripts/acceptance/read_image.sh` → `read image: PASS`（含 Phase 3b/3c/5b——#3/#4/#7/#8 的端到端证明）；
   - `FOREBRAIN_E2E_DEEPSEEK_KEY=… scripts/acceptance/read_pdf.sh` → PASS（#5/#9 不破坏 PDF 读取、中文页非豆腐块）。
   隔离 home/project，结束清理；不污染真实 `~/.forebrain*`。

## 6. Done criteria（机器可查）

- `go test -tags fts5 ./pkg/mcp ./pkg/tool ./pkg/pdf ./pkg/process ./pkg/architecture` 全绿（含新增用例）；全量 `./...` 绿。
- `grep -rn 'GetInstanceTimeout\|Rendered bool' pkg/pdf/` → 空。
- `grep -n ':scope=' frontend/src/views/WorkshopView.vue` 命中 ToolCallCard 行的 `workbenchScope`。
- `grep -n '"pdf"' pkg/architecture/cache_test.go` → 命中 fanOutBudgets；`grep -c 'Four packages' pkg/architecture/cache_test.go` → 0。
- `scripts/package-graph.sh` 幂等；graph.json 变更只含 pdf fan_out 0→1。
- `grep -c 'image_mixed\|image_oversize' scripts/acceptance/fake_mcp_stdio.py` ≥ 2；`bash -n` + `py_compile` 通过。
- read_image.sh 真机 PASS（含 3b/3c/5b 新 phase）；read_pdf.sh 真机 PASS。
- `cd frontend && pnpm exec vue-tsc --noEmit && pnpm test` 全绿。
- 既有 `TestSeedFontsCopiesShippedFontsOnce`（user-replaced 不触碰）未被修改且绿。

## 7. STOP conditions

- F3 使 `TestLayerCeiling`/`sameLayerEdges` 出现非预期违规（pdf→llm 之外被牵连）→ 停，报告依赖图事实。
- 真机验收中任一 parts 断言在**修复后**仍失败（说明还有第二传播路径）→ 停，回到定位，不打脚本补丁。
- Phase 3c 的 48MiB 级载荷在 stdio 传输上超时/不稳（环境能力问题而非产品缺陷）→ 如实报告并降级为「webp 真机 + oversized 单测双证」，由 owner 裁定是否必须真机。
- `read_image.sh`/`read_pdf.sh` 因 provider/key 失败（与本次改动无关的环境问题）→ 如实报告，不伪造 PASS。
- 前端测试若现有 ToolCallCard.test.ts 的注入模式不适配新字段 → 就地扩展示例对象，不新造测试框架。

## 8. Maintenance notes

- `fanOutBudgets["pdf"]` 的 current 必须随实测下降而降（棘轮纪律）；pdf 再增依赖需 owner（同 tool 条目先例）。
- `llm.DecodeImageConfig` 导出后属 llm 的公共面：仅头部解析、错误返回 0,0，别在其中加解码。
- `tool.ImageFileOutput` 与 `ImageReadOutput` 共享 `image_*` 键契约（event→gateway→TUI/web）；改键名是四面联动，改值语义先读 F1 的诚实性说明。
- MCP 降级行文案是模型可见契约（与 "Binary content saved to" 家族同款），改动需过 result_processing_test 的断言。
- 字体播种的四分支判定（含「用户替换永不触碰」）是 owner 裁决，改判定顺序前先重读 F8；截断签名 = 源字节真前缀。
- read_image.sh 的 MCP parts 断言依赖 fb_messages.parts 的 JSON 形态（空白容忍正则）；状态库 schema 变更时同步。
- graph.json 在 F3 后已含 pdf→llm 边；后续任何 pkg import 变更照常重跑 package-graph.sh。

## 9. 验收后收口

计划文件回填状态行 + 验收记录（场景→证据）；改动全部留在 dev 工作区，**不 commit**（owner 手动审阅）；清理 tmux/临时目录。

## Implementation Status

**状态：全部完成（Steps 0–9 ✅）。改动留在 dev 工作区未提交，tmux/临时目录已清理。**详细记录见审批正本的 Implementation Status（`~/.forebrain/workspace/plans/…/fix-audit-issues-3-12.md`）；要点：

- Steps 0–8 全部落地：F1+F2（wantParts 闸门前移+坏块降级+48MiB 解码前守卫+DecodeImageConfig/ImageFileOutput）、F1b（appendPDFPagePart）、F3+F5（FontDirs 归一 llm.AssetsDir、删 GetInstanceTimeout/Page.Rendered、文档对齐）、F8（原子写+四分支判定）、F7（pdf 进棘轮 current=1+graph.json 重生成）、F4+F6（WorkshopView scope+previewScaled+卡片元数据行+toolImageAltNoDims）、F9（fake_mcp_stdio 两新 mode+read_image.sh 断言改造+Phase 3b/3c/5b）。
- 计划外修复（脚本层根因）：read_image.sh 的 pipefail 静默自杀（image_block_count grep 无匹配退出 1）、MCP 行锚定错误（改按 invocation 标签选行）、Phase 1 抓屏时机（标记出现即刻抓）。
- **STOP #3 触发并降级**：stdio 传输 16 MiB 帧上限使 64 MiB oversize 载荷在守卫前被传输层拒收（环境能力边界，非产品缺陷；websocket 路径守卫可达）→ 按「webp 真机 + oversized 单测双证」执行，脚本如实 SKIP+原因；是否必须真机送达由 owner 裁定。
- 验证：目标包单测全绿；全量 `./...` 绿（gateway TempDir 清理竞态偶发、复跑即绿、本期零改动）；vue-tsc 干净 + pnpm test 465 全绿；**真机 read_image.sh PASS（31 断言）+ read_pdf.sh PASS（21 断言）**（DeepSeek 真实 provider）；gofmt/vet/机器可查项全过。
