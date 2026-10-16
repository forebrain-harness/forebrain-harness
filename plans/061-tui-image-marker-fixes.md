# 061：TUI 图片标记行的两个小修——点击不再双重动作、0×0 不再出现

> **执行者须知（零上下文）**：本计划自包含。逐步执行，每步先跑"验证"确认预期再进入下一步。
> 命中 STOP condition 就停下报告。**不建 worktree、不 commit**。
>
> **Drift check（先做）**：本计划写于 `git rev-parse --short HEAD` = `fa4af8a`（2026-10-16）。
> `pkg/tui/image_open.go`、`reducer.go`、`render.go` 属已提交状态（c95e214）；工作区未提交改动
> 不含这些文件（只有 chat_slash.go / chat_session_test.go 等）。执行前核对摘录；对不上按 STOP 处理。

## Status

- **Priority**: P2（两个交互/一致性缺陷，无数据风险）
- **Effort**: S
- **Risk**: LOW（改动面小；红例测试绑定行为）
- **Depends on**: none
- **Category**: Correctness / TUI parity（审计发现 #11/#15）
- **Planned at**: commit `fa4af8a`，2026-10-16

## Why this matters

1. **#11 可折叠图片卡的标记行点击双重动作**：`toggleAtScreenRow` 对 `FrameTool` 块**任意 body 行**
   都是折叠切换（`fullBodyClickable`，reducer.go:2533-2537）；而 `clickImagePreviewRow`
   （image_open.go:67-75）先解析命中、再无条件调 `ViewportSelectEnd`（内部执行折叠切换）、
   isMarker 时又开图。长文件名让标记行换行、卡片超过 3 行（`toolOutputMaxLines = 3`，
   render.go:501）成为可折叠卡后，**点一下标记行 = 既折叠/展开卡片又打开查看器**——transcript
   在光标下跳动。提示行（"··· N more lines"）被精心排除在"开图"之外，标记行却没被排除在
   "折叠"之外：两类行的契约互相穿了帮。
2. **#15 标记行印 `0×0`**：`imagePreviewFrameLine`（image_open.go:93-94）无条件插值
   `meta.PreviewWidth/Height`；而 surface 契约（file_tools.go:403-404 注释）是"a surface omits
   them rather than showing 0×0"，web 卡已实现（ToolCallCard.vue 的 imageMetaLine 按值省略）。
   头解析失败的图（损坏头但魔数可嗅探、或 MCP 降级路径 `w,h=0`）在 TUI 显示 `▤ x.png · 0×0 ·
   N bytes`，web 卡省略该段——同一事实两端分叉。

## Current state（摘自本次亲自阅读的代码）

### pkg/tui/image_open.go

```go
// :67-75
func clickImagePreviewRow(renderer *Renderer, col, row int) {
	if renderer == nil { return }
	path, label, isMarker := renderer.ImagePreviewAtScreenRow(row)
	if renderer.ViewportSelectEnd(col, row) && isMarker {   // SelectEnd 无条件执行（含折叠切换）
		openImagePreview(renderer, path, label)             // 返回值语义见 Step 1 的核实点
	}
}
// :92-99
func imagePreviewFrameLine(meta tool.ToolMeta) string {
	line := fmt.Sprintf("▤ %s · %d×%d · %s",
		imagePreviewLabel(meta), meta.PreviewWidth, meta.PreviewHeight, humanBytesLabel(meta.PreviewBytes))
```

### pkg/tui/reducer.go

```go
// :2515-2541（节选）
func (m *viewModel) toggleAtScreenRow(vr viewportRender, screenRow int) bool {
	blockID, isHeader := vr.blockAtRow(screenRow)
	...
	} else {
		kind := b.frame.Kind
		fullBodyClickable := kind == FrameTool || kind == FrameMemoryCompact || (kind == FrameThinking && b.frame.Final)
		if !fullBodyClickable { return false }
	}
	b.expanded = !b.expanded
	return true
}
// :2582-2601 imageMarkerAtScreenRow / isImageMarkerRow：按 body 前缀 "  └ " 识别标记行，
// 已排除两处四空格提示行（streaming "… N earlier lines" 与 fold "··· N more lines"）。
```

### pkg/tui/render.go

`:501` `toolOutputMaxLines = 3`；web 对照实现：`frontend/src/components/chat/ToolCallCard.vue`
的 imageMetaLine（W×H 段仅在两值都 >0 时拼接）。

## 修复设计

### Step 1（#11，owner 2026-10-16 终裁）：图片卡不可折叠——output 区域默认全量展示

> **裁决记录**：执行期发现既有测试 `TestClickingTheFoldHintOnAnImageCardExpandsWithoutOpening`
> 的注释记载"标记行也是折叠目标"是 c95e214 的有意设计（展开态无提示行、标题行无动作，标记行是
> 唯一鼠标收拢路径）。三个选项（保留现状 / 标记行只开图 / 新设计）呈 owner 后，owner 终裁：
> **"图片卡 output 区域取消折叠功能，默认全量展示"**——图片预览卡永不折叠，替代本节原设计
> （原"toggleAtScreenRow 排除标记行"方案作废；执行者 NOTE：裸 isImageMarkerRow 判定会误伤
> 文本卡 body 行，也不需要了）。

实施：

1. 折叠资格判定处（块构建/`collapsible` 赋值处，reducer.go 内 `toolOutputMaxLines` 相关逻辑）：
   `FrameTool` 且 `ToolMeta.PreviewKind == "image"` 的块 **永远 `collapsible=false`、`expanded=true`**
   （或等价：根本不进入折叠判定）。图片卡 body 只有标记行（含换行续行），全量展示就是它的
   完整形态——没有"更多行"可折叠。
2. `toggleAtScreenRow` 无需针对图片卡改动（不可折叠的块本就 return false）；`clickImagePreviewRow`
   维持现状（标记行命中即开图）。
3. 重写既有测试 `TestClickingTheFoldHintOnAnImageCardExpandsWithoutOpening`（其折叠段钉的是被
   裁决废除的行为）：改为钉住新语义——长文件名 wrapped 图片卡**始终展开**、无折叠提示行出现、
   点标记行只开图不改变布局、点任意 body 行不再有折叠副作用。测试名随之改为表达新语义
   （如 `TestImageCardIsNeverCollapsibleAndItsMarkerOpensTheFile`）。
4. 若 `isImageMarkerRow` 对折叠提示行的排除注释因此过时（图片卡不再产生提示行），只改注释，
   谓词逻辑不动（文本卡的提示行仍在）。

**验证**：T1'（重写后的测试）+ 既有 `TestImageMarker*` 全绿 + `./pkg/tui` 整包闸门。

### Step 2（#15）：0×0 省略

`imagePreviewFrameLine` 对齐 web 契约：

```go
dims := ""
if meta.PreviewWidth > 0 && meta.PreviewHeight > 0 {
	dims = fmt.Sprintf("%d×%d · ", meta.PreviewWidth, meta.PreviewHeight)
}
line := fmt.Sprintf("▤ %s · %s%s", imagePreviewLabel(meta), dims, humanBytesLabel(meta.PreviewBytes))
```

**验证**：T3。

## Test plan（`pkg/tui/image_open_test.go`，跟随既有用例风格）

- **T1（#11 红例）**：构造一个标记行换行、卡片 >3 行的可折叠图片卡（长文件名 fixture，参照
  既有 wrapped-marker 用例的构造方式）→ 对标记行坐标调 `clickImagePreviewRow` → 断言
  (a) 打开了该行自己的文件（fake opener，既有模式）；(b) 该卡 `expanded` 状态**未变**（未折叠）。
  反证：还原 `toggleAtScreenRow` 的排除后 (b) 变红。
- **T2（#11 不回归）**：同一卡上点提示行 → 只折叠不开图；点文本卡 body 行 → 只折叠（既有用例
  已覆盖则不重复造）。
- **T3（#15）**：`imagePreviewFrameLine(tool.ToolMeta{PreviewKind:"image", PreviewPath:"/x/a.png",
  PreviewBytes:123})`（宽高零值）→ 输出**不含** `0×0`；宽高 7×3 的既有断言不变。

## STOP conditions

1. `ViewportSelectEnd` 的返回值语义两种读法都说不通（读定义后仍无法确定）——贴出定义停下报告。
2. `isImageMarkerRow` 的识别与前缀摘录不符（例如已改为按块属性而非行前缀）——按实际机制等价
   实现，语义不变即可；无法等价实现则停。
3. 既有测试钉住了"标记行点击也折叠"（预期没有；c95e214 的测试只钉了提示行）——列出测试名
   停下让 owner 确认（那类测试钉的是缺陷本身）。

## Out of scope（不要做）

- web 缩略图 `@error` 兜底（审计 #20 的 investigate 项之一，未获 owner 批入本期）。
- `toolOutputMaxLines` 调整、折叠交互重设计、render perf（owner 已整体否决 TUI 性能优化方向）。
- 标记行文案改动（"· click to open" 是定稿文案）。

## 全量验证闸门

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1
CGO_ENABLED=1 go vet -tags fts5 ./...
gofmt -l cmd pkg third_party
```
