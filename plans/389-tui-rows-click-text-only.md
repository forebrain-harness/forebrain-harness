# Plan 389: TUI 交付物行、图片 marker 行、子代理卡行只在文字范围内可点（hover 高亮同步）

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. **Do NOT commit**（owner 常设规则 no-git-commits）：
> 改动留在当前工作树（分支 `dev`）由 owner 审阅。按 `.forebrain/skills/plan-gated-bugfix/SKILL.md`
> 的流程**直接在当前源码实施**，不用 git worktree、不用快照。
>
> **Drift check (run first)**:
> `git rev-parse --short HEAD`（planned at `bfc3ac17`，branch `dev`；评审时 HEAD 已到 `041a9627`，
> 下述四个文件的 `git diff bfc3ac17 HEAD` 为空）；
> `git status --porcelain -- pkg/tui`（planning 时为空）；
> `git diff --stat bfc3ac17 -- pkg/tui/reducer.go pkg/tui/render.go pkg/tui/reducer_test.go pkg/tui/image_open_test.go pkg/tui/chat_session_test.go`。
> 行号漂移不算；**「Current state」摘录与现码内容不一致才算漂移**（STOP）。
> `pkg/tui/vscode_ext/`、`pkg/tui/vscode_open.go`、`editors/vscode/` 归并行 agent（forebrain-harness-d8）——只读，不碰。

## Status

- **Priority**: P2（owner 截图报障 2026-11-26，原话「交付物列表每一份文件的可点击区域都是该文件所在行整行，用户非常容易误点到可点击区域导致文件被打开，请将可点击区域限定到每份文件行的文字长度范围内」）
- **Effort**: S
- **Risk**: LOW（只动 TUI 视口的鼠标命中判定与 hover 高亮范围；不碰数据、协议、公共 API、web）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `bfc3ac17`, 2026-11-26
- **Owner 裁决（2026-11-26，user_interaction 三问）**：①折叠提示行「··· N more deliverable click to show / click to hide」**一并**收到文字范围；②图片步骤的 marker 行（工具卡里「└ ▤ xxx.png …」）**一并改**成只点文字才打开；③子代理生命周期卡行与 fanout 任务行（点整行任意格切到子代理视图）**一并改**。
- **Plan review（2026-11-26）**：独立冷读共四轮——glm-5.4 一轮（4 BLOCKER／4 SHOULD-FIX／3 NIT）与 owner 指定的 custom/qwen4-max-preview 三轮（approve with changes：测试 fixture 两处 BLOCKER＋范围漏列；末轮为计划文本错字）——全部到源码核实后折入本版（见 §12「评审记录」）。
- **Issue**: —

## 1. Why this matters

TUI 里一轮结束后的交付物列表，每一行（`≡ pkg/config/config.go +3 -0`）的点击热区是**整个终端行**：鼠标点在路径右侧的大片空白上也会把文件丢给 VS Code／系统打开器。hover 高亮同样铺满整行，等于在视觉上宣告整行可点，误触就更容易。根因是命中判定**只按行不按列**；同一假设还覆盖了图片 marker 行、子代理卡行。

修好后：这些行只在「该行画了文字的格子」内响应点击；hover 高亮只亮这些格子（高亮范围＝可点范围，不再骗人）。其它卡片（工具输出、thinking、compact 的「点预览任意行折叠」）行为不变。

## 2. 根因（file:line 证据链，基线 `bfc3ac17`）

### 2.1 点击：列坐标被丢掉

鼠标按下／松开 → `pkg/tui/input_events.go:930/935`（`inputEventMousePress/Release` 带 `mouseCol/mouseRow`）→ `pkg/tui/reducer.go:6922/6929` → `viewportRelease(col,row,at)`：

```go
// pkg/tui/reducer.go:8129-8132、:8149（现状）—— clickCol 算出来了，却只把行传下去
		s, clickCol, clickRow := r.screenAtLocked(at), col, row
		if press.armed {
			s, clickCol, clickRow = press.screen, press.col, press.row
		}
		...
		_, imagePath, imageLabel := r.transcriptClickLocked(s, clickRow)
```

```go
// pkg/tui/reducer.go:6982-7013（现状）
func (r *Renderer) transcriptClickLocked(s shownScreen, row int) (changed bool, imagePath, imageLabel string) {
	if row < 0 || row >= s.bodyHeight || s.view != r.activeView {
		return false, "", ""
	}
	vm := r.activeVMLocked()
	if !r.overlayActive {
		imagePath, imageLabel, _ = vm.imageMarkerAtScreenRow(s.render, row)   // :6992 只收 row
		if imagePath == "" {
			if label, ok := vm.deliverableAtScreenRow(s.render, row); ok {   // :6998 只收 row
				...
			}
		}
	}
	if r.openAgentViewAtRowLocked(s.render, row) {                            // :7007 只收 row
		return true, imagePath, imageLabel
	}
	if vm.toggleAtScreenRow(s.render, row) { ... }                              // :7010 折叠提示行切换，只收 row
```

另一条点击入口同样只传行：`pkg/tui/reducer.go:6968`（`viewportClickToggleAt`，终端上报「单次点击」事件时走它）：`r.transcriptClickLocked(s, row)`。

四个按行解析器都是「这一行是谁」的查询，本身没有错：
- `deliverableAtScreenRow`（`reducer.go:3201-3241`）：行 → 交付物标签（折叠提示行返回 false）。
- `toggleAtScreenRow`（`reducer.go:3053-3070`）：交付物块里只有提示行（`deliverableHintRow`，`reducer.go:3179-3185`，去样式后以 `···` 开头）切换折叠。
- `imageMarkerAtScreenRow`（`reducer.go:3127-3148`）：工具块 `PreviewKind=image` 且该行满足 `isImageMarkerRow`（`reducer.go:3166-3172`：`  └ ` 开头或四格缩进，排除 `    …`／`    ···` 提示行）→ 图片路径。
- `agentAtScreenRow`（`reducer.go:3103-3121`）→ `openAgentViewAtRowLocked`（`reducer.go:6110-6123`）：fanout 任务行按 `lineAgents` 映射到孙代理、或块自带 `AgentID`（子代理生命周期卡＝`FrameStatus`+`AgentID`）→ `setActiveViewLocked` 切到该子代理视图。

错在**分发层把列丢了**：整行任意格子都命中。

### 2.2 hover：高亮铺满整行

```go
// pkg/tui/reducer.go:7042-7047（现状）viewportHoverAt 只记行
	hoverRow := -1
	if target == hoverTargetNone && !(s.drawerOpen && col >= s.drawerLeft && row < s.bodyHeight) {
		if row >= 0 && row < r.vpHeight {
			hoverRow = row
		}
	}
```

```go
// pkg/tui/reducer.go:5588-5590（现状）paintViewportLocked
		} else if r.vpHoverRow == row && vr.isClickableRow(row, activeVM) {
			line = applyHoverHighlight(line, rowWidth, hoverBgSeq)
		}
```

`isClickableRow`（`reducer.go:3285-3301`）对 `FrameStatus`+`AgentID` 的卡、fanout 的 agent 行、`FrameDeliverables` 块的**每一行**返回 true；`applyHoverHighlight`（`reducer.go:7299-7318`）把 hover 背景铺到 `width`（终端整行宽）——即截图里的整行高亮。图片卡不可折叠（`frameCollapsible`，`tty_session.go:1381-1386` 返回 false），`isClickableRow` 在 `!b.collapsible` 处返回 false，所以 **marker 行今天没有 hover 高亮**（只有点击热区过大的问题）。

### 2.3 复现（已跑，修复前必红）

用 `go test -overlay` 把探针挂进 `pkg/tui`（零项目写入；探针源码见附录 A）：

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
CGO_ENABLED=1 go test -tags fts5 -overlay=/tmp/fb-repro-389/overlay.json ./pkg/tui \
  -run 'TestReproDeliverableClickPastTextOpensFile' -count=1 -v
```

```text
=== RUN   TestReproDeliverableClickPastTextOpensFile
    zz_repro_test.go:24: row 0 paints "  ≡ pkg/a.go": text ends at column 12 of 80
    zz_repro_test.go:39: click at col 70 (text ends at 12): opened=[/…/pkg/a.go]
    zz_repro_test.go:41: a click 58 cells right of the text opened [/…/pkg/a.go]
--- FAIL: TestReproDeliverableClickPastTextOpensFile (0.00s)
```

行文字止于第 12 列，点第 70 列照样打开文件。`ViewportSelectStart/End(70,0)` 走的正是 §2.1 的 `viewportRelease` → `transcriptClickLocked` 链。第二轮评审用 overlay 独立复现了三处症状（交付物 col 70 打开／文字止 12；hover 铺 80 格；marker col 70 打开／文字止 46）。执行阶段以 Step 1 的红测试为正式复现（它进仓库、可反复跑）；附录 A 只用于修复前对照。

### 2.4 同一症状的全部路径（owner 裁决后本计划全覆盖）

| 路径 | 现状 | 本计划 |
| :-- | :-- | :-- |
| 交付物文件行（含长路径折行后的续行） | 整行可点、整行高亮 | 改：文字范围内可点＋高亮 |
| 交付物折叠提示行 | 整行可点、整行高亮 | 改（裁决①） |
| 图片步骤 marker 行（含折行续行） | 整行可点、无高亮 | 改：文字范围内可点（裁决②）；高亮维持「无」 |
| 子代理生命周期卡行（`FrameStatus`+`AgentID`） | 整行可点（切子代理视图）、整行高亮 | 改（裁决③） |
| fanout 任务行（`lineAgents` 映射到的行） | 整行可点、整行高亮 | 改（裁决③）；fanout 卡头行本就不是目标，不变 |
| 工具／thinking／compact 卡的预览行 | 整行可折叠、整行高亮 | 不改（刻意的「点预览任意行展开」交互，`toggleAtScreenRow` 文档注释 `reducer.go:3043-3047`） |
| web 端交付物列表 | 未调查 | 不改（owner 报的是 TUI） |

## 3. 修复设计

**一个判定、三处使用**，让点击、hover、高亮对「目标到哪一列为止」不可能有分歧：

```go
// pkg/tui/reducer.go —— 新增，放在 deliverableHintRow（:3185）之后

// textOnlyTarget reports whether a painted screen row is a click target that
// acts only from the cells its text is painted on, and the printed width of
// that text: a deliverables group's file row or fold hint (its wrapped
// continuations included), an image step's marker row, or a row that opens a
// subagent's view — a lifecycle card's row or one task row inside a fanout.
// Owner ruling 2026-11-26: these rows used to answer a click anywhere across
// the terminal row, so a click meant for the transcript beside a short path
// opened a file. The cells at and past end are the transcript's, not the
// target's. One predicate serves the click, the hover and the highlight, so
// the three cannot disagree about where the target ends. The width is read
// the way deliverableHintRow and isImageMarkerRow read the same rows — SGR
// stripped, trailing blanks dropped — which is exact for the rows named here:
// they carry SGR only and never an erase-to-end-of-line.
func (vr viewportRender) textOnlyTarget(screenRow int, vm *viewModel) (end int, ok bool) {
	if screenRow < 0 || screenRow >= len(vr.lines) {
		return 0, false
	}
	blockID, _ := vr.blockAtRow(screenRow)
	if blockID < 0 {
		return 0, false
	}
	b := vm.blockByID(blockID)
	if b == nil {
		return 0, false
	}
	switch b.frame.Kind {
	case FrameDeliverables:
		// Every row of the group: a file row, a wrapped continuation and the
		// fold hint alike.
	case FrameTool:
		if _, _, marker := vm.imageMarkerAtScreenRow(vr, screenRow); !marker {
			return 0, false
		}
	default:
		// The rows that open a subagent's view — the same rows
		// openAgentViewAtRowLocked answers.
		if _, agentRow := vm.agentAtScreenRow(vr, screenRow); !agentRow {
			return 0, false
		}
	}
	plain := strings.TrimRight(sgrPattern.ReplaceAllString(vr.lines[screenRow], ""), " ")
	return displayLineWidth(plain), true
}
```

复用：`displayLineWidth`（`render.go:5271-5273`，runewidth 口径，与 `fitRow` 同一把尺子）、`sgrPattern`、`imageMarkerAtScreenRow`、`agentAtScreenRow`（两个既有「这行是谁」判定，不复制其条件）。热区＝`[0, end)`：从行首（含缩进）到文字最后一格；`end` 之后（含 `end`）是空白，不属于目标。

**点击**——把列传进去并在分发层统一拒绝：

```go
// pkg/tui/reducer.go —— transcriptClickLocked 签名加 col；两个调用点同步
func (r *Renderer) transcriptClickLocked(s shownScreen, col, row int) (changed bool, imagePath, imageLabel string) {
	if row < 0 || row >= s.bodyHeight || s.view != r.activeView {
		return false, "", ""
	}
	vm := r.activeVMLocked()
	// A row whose target is its text (textOnlyTarget) acts only from the
	// cells that text is painted on: a click to the right of it lands on the
	// transcript, not on the file, the image, the fold or the subagent view.
	if end, ok := s.render.textOnlyTarget(row, vm); ok && col >= end {
		return false, "", ""
	}
	...（其余不变）
```

调用点：`:6968` → `r.transcriptClickLocked(s, col, row)`；`:8149` → `r.transcriptClickLocked(s, clickCol, clickRow)`。函数文档注释补一句列规则。早返回对这几类行是完备的：子代理自身的帧进 per-agent VM、不在这类块里；交付物块除提示行外没有折叠目标；marker 行不可折叠；子代理卡行上除「切视图」没有别的目标。

**hover**——指针在文字上才有 hover，高亮只铺文字格：

```go
// pkg/tui/reducer.go —— viewportHoverAt，:7044-7046
		if row >= 0 && row < r.vpHeight {
			hoverRow = row
			// A row whose target is its text highlights only while the pointer
			// is on that text: the highlight is the clickable area, nowhere else.
			if s.view == r.activeView {
				if end, ok := s.render.textOnlyTarget(row, r.activeVMLocked()); ok && col >= end {
					hoverRow = -1
				}
			}
		}
```

（`s.view == r.activeView` 守卫与同函数里 `hoverToken` 的守卫同款：离开的视图留下的旧屏不按当前 vm 解析块。）

```go
// pkg/tui/reducer.go —— paintViewportLocked，:5588-5590
		} else if r.vpHoverRow == row && vr.isClickableRow(row, activeVM) {
			if end, ok := vr.textOnlyTarget(row, activeVM); ok {
				// The row's target is its text, so its highlight is too: the
				// same painter as every other row, stopped at the text's end.
				line = applyHoverHighlight(line, end, hoverBgSeq)
			} else {
				line = applyHoverHighlight(line, rowWidth, hoverBgSeq)
			}
		}
```

为什么复用 `applyHoverHighlight` 而**不是** `highlightCells`（☑N/M token 用的那个）：交付物行的 `+N`／`-M` 计数各自以 `\x1b[0m` 收尾（`diffPalette.count`，`render.go:6891-6896`），`highlightCells` 原样拷贝转义、不重申背景（`plan_drawer.go:516-521`），高亮会在第一个计数后断掉；`applyHoverHighlight` 在每个改背景的序列后重申 hover 背景（`reducer.go:7311-7313`；`sgrSetsBackground` 对全复位返回 true，`:7231-7245`），且 `width=end` 时行文字恰占 `end` 列、补白为 0，不发 `\x1b[K`。marker 行 `isClickableRow` 为 false，走不进这个分支——高亮维持「无」，不新增 affordance。`hoverRow = -1` 同时让「同目标不重绘」判定（`:7052`）自然成立：文字↔空白横移触发一次重绘，文字内横移不重绘。

**文档注释同步**：`isClickableRow`（`:3294-3298`，补「热区另由 textOnlyTarget 收到文字」）；`render.go:468-472` `vpHoverRow`（补「也用于指针落在文字型目标行文字右侧空白时」）；`toggleAtScreenRow`（`:3049-3052`）／`deliverableAtScreenRow`（`:3187-3196`）／`imageMarkerAtScreenRow`（`:3123-3126`）／`agentAtScreenRow`（`:3096-3102`）各补一句「热区由调用方 transcriptClickLocked 按 textOnlyTarget 判」。

### 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 备注 |
| :-- | :-- | :-- | :-- |
| 点文件行文字格 `[0,end)`（含 `+N -M` 计数、2 格缩进） | 打开文件 | 打开文件 | 不变 |
| 点文件行文字右侧空白（`col ≥ end`） | **打开文件（bug）** | 什么都不做（等同点空白转录：清选区） | 刻意修订 |
| 点折行续行各自文字内／外 | 整行打开 | 文字内打开、文字外不打开 | 刻意修订 |
| 点提示行文字 | 切换折叠 | 切换折叠 | 不变 |
| 点提示行右侧空白 | 切换折叠 | 什么都不做 | 刻意修订（裁决①） |
| 点图片 marker 行文字（含续行） | 打开图片 | 打开图片 | 不变 |
| 点 marker 行右侧空白 | **打开图片** | 什么都不做 | 刻意修订（裁决②） |
| 点子代理生命周期卡行／fanout 任务行文字 | 切到该子代理视图 | 切到该子代理视图 | 不变 |
| 点这些行右侧空白 | **切视图** | 什么都不做 | 刻意修订（裁决③） |
| hover 文件行／提示行／子代理卡行／fanout 任务行文字 | 整行高亮 | 仅文字格高亮（计数后的 reset 处不断） | 刻意修订 |
| hover 这些行右侧空白 | 整行高亮 | 无高亮 | 刻意修订 |
| hover marker 行 | 无高亮 | 无高亮 | 不变 |
| 长工具／thinking／compact 卡预览行 hover／点击 | 整行高亮、任意行切换折叠 | 同旧 | 不变 |
| fanout 卡头行（无 agent） | 不可点、无高亮 | 同旧 | 不变 |
| 拖选穿过这些行、双击、三击 | 文本选择 | 同旧 | 按下／拖动路径未改 |
| 终端单次点击事件（`ViewportClickToggle`） | 同按行命中 | 同按列规则 | `:6968` 调用点 |
| web 端 | — | — | 不在范围 |

## 4. Scope

**In scope**（只改这些）：
- `pkg/tui/reducer.go`：新增 `textOnlyTarget`；`transcriptClickLocked` 加 `col` 与早返回；两个调用点；`viewportHoverAt`；`paintViewportLocked` hover 分支；相关注释。
- `pkg/tui/render.go`：仅 `vpHoverRow` 字段注释（`:468-472`）。
- `pkg/tui/reducer_test.go`：新增 `rowTextEnd`、`clickCell`、`textTargetScreen`、`paintedRowOf`、`TestDeliverableRowsAreTargetsOnlyOnTheirText`；`TestDeliverableRowsAreClickable` 注释（`:6913-6916`）补一句。
- `pkg/tui/image_open_test.go`：新增 `TestImageMarkerRowIsATargetOnlyOnItsText`。
- `pkg/tui/chat_session_test.go`：在 `TestClickingAFanoutTaskRowOpensThatTasksView`（`:24510` 起）之后新增 `TestSubagentCardRowsAreTargetsOnlyOnTheirText`。
- `plans/389-tui-deliverable-rows-click-text-only.md`（批准后落盘本计划）＋ `plans/README.md` 追加一行索引（只追加）。

**Out of scope**（看着相关，明确不碰）：
- `deliverableAtScreenRow`／`imageMarkerAtScreenRow`／`toggleAtScreenRow`／`agentAtScreenRow`／`openAgentViewAtRowLocked` 的签名与逻辑——它们是「行→目标」查询，保持按行；热区是分发层的事。
- 工具／thinking／compact 卡「点预览任意行折叠」与整行 hover——刻意交互。
- `applyHoverHighlight`／`highlightCells` 本体。
- `render.go:5265` 附近「lipgloss.Width undercounts…」那句注释（评审指出已过时）——与本修复无关，不改。
- `pkg/tui/vscode_open.go`、`pkg/tui/vscode_ext/`、`editors/vscode/`（并行 agent d8 的地盘；本计划只读它们来设计验收桩）。
- web 端（`frontend/`）交付物列表。
- `pkg/architecture` 预存红 `TestTestFilesCorrespondToProductionFiles`（`deliverables_outcome_test.go` 无配对生产文件）——与本修复无关，不修。
- 既有 `clickAt`（`reducer_test.go:3407`，24 处调用）不改——它的调用方里有连续两次点击的场景，加时钟重置会改变其语义。

## 5. Commands you will need

| Purpose | Command | Expected on success |
| :-- | :-- | :-- |
| cgo 环境 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"` | 无输出 |
| 定点测试 | 见下方代码块「定点测试命令」（`-run` 模式含 `|`，不放进表格：GFM 会把代码段里的 `|` 当列分隔，照抄即得到被截断的模式） | 全部 `--- PASS` |
| 包级回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | `ok`（本机约 200s） |
| 格式 | `gofmt -l pkg/tui` | 不打印任何文件 |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui/` | 无输出，exit 0 |
| 构建 | `CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` | exit 0 |
| 真机驱动 | `.claude/skills/run-forebrain/driver.sh`（`reset/build/start/send/click/submit/screen/wait/stop`；`click <col> <row>` 1-based 注入 SGR 按下＋松开，`driver.sh:180-183`） | 见 §8 |

定点测试命令（Step 1–3 的 Verify 都用它）：

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui \
  -run 'TestDeliverableRowsAreTargetsOnlyOnTheirText|TestImageMarkerRowIsATargetOnlyOnItsText|TestSubagentCardRowsAreTargetsOnlyOnTheirText' \
  -count=1 -v
```

## 6. Steps

### Step 0: 漂移检查

跑 Executor instructions 里的三条命令。`pkg/tui` 若有非本计划的未提交改动，记录文件名并确认它们不在 In scope 清单里（否则 STOP）。

**Verify**: §2 摘录逐条与现码一致。

### Step 1: 先写红测试（修复前必红）

**测试必须量「真正画出来的屏」**：`liveScreenLocked`（`reducer.go:7589-7617`）直接拿 `r.vpLastRender` 当点击屏，而首次点击的 paint 会按活宽重排——手搓 `renderViewport(…, 80, …)` 的 `vpLastRender` 与 paint 之后的屏不是同一份（评审实测续行文字末列 70→68），量出来的 `end` 对点击是错的。所以新增 fixture 一律先真 paint（`newPlanDrawerScreenWithOut` 的 forced 尺寸），再从 `r.vpPainted[row]` 量文字末列；`vpPainted[row]` 就是 `canvasRow(...)`（`reducer.go:5552`、`:5783`），不补白、不发 `\x1b[K`。

复用既有 helper：`newPlanDrawerScreenWithOut`（`plan_drawer_test.go:548`）、`clickAt`（`reducer_test.go:3407`）、`manyDeliverables`（`reducer_test.go:6531`）、`stubImageOpener`（`image_open_test.go:24`）、`imageStepFrame`（`:89`）、`firstRowWithPrefix`（`:108`）、`isImageMarkerRow`（`reducer.go:3166`）、`startsDeliverableRow`（`reducer.go:3721`）、`stripANSI`（`chat_session_test.go:17349`）、`rowCellBackgrounds`（`chat_session_test.go:26990`，逐格返回背景序列）、`rowOfPrimaryText`（`chat_session_test.go:23710`，读 `vpLastRender.lines`，真 paint 后它与 `vpPainted` 同行号）、`fanoutFrameForTest`、`retainFrameLocked`、`ensurePerAgentVM`（范式 `TestClickingAFanoutTaskRowOpensThatTasksView`，`chat_session_test.go:24510` 起）、hover 走 `r.HandleMouse(inputEvent{kind: inputEventMouseMove, …})`＋`r.vpPainted[row]`（范式 `plan_drawer_test.go:1073-1090`）。

```go
// pkg/tui/reducer_test.go

// rowTextEnd is the printed width of a painted row's text: its cells up to
// and including the last non-blank one.
func rowTextEnd(line string) int {
	return displayLineWidth(strings.TrimRight(stripANSI(line), " "))
}

// clickCell drives one fresh single click — the multi-click clock reset, then
// clickAt's press/release pair — so two calls in a row never read as a
// double-click (clickRow does the same for column 0).
func clickCell(r *Renderer, col, row int) {
	r.lastClickTime = time.Time{}
	clickAt(r, col, row)
}

// textTargetScreen paints frame into a renderer with a real terminal size and
// returns it with the opener stub's record. The rows a click resolves against
// are the painted ones: a hand-made vpLastRender is re-laid-out by the first
// paint at the live width, so a text end measured on it is wrong for the
// click (a wrapped continuation row ends two columns earlier there).
func textTargetScreen(t *testing.T, cwd string, frame Frame) (*Renderer, *[]string) {
	t.Helper()
	opened := stubImageOpener(t)
	r, _ := newPlanDrawerScreenWithOut(t, 80, 20)
	r.WithWorkingDir(cwd)
	r.RenderFrame(frame)
	return r, opened
}

// paintedRowOf returns the 0-based screen row whose painted text carries needle.
func paintedRowOf(t *testing.T, r *Renderer, needle string) int {
	t.Helper()
	for i, painted := range r.vpPainted {
		if strings.Contains(stripANSI(painted), needle) {
			return i
		}
	}
	t.Fatalf("no painted row carries %q", needle)
	return -1
}

// TestDeliverableRowsAreTargetsOnlyOnTheirText is the owner's report of
// 2026-11-26: every row of the group was a click target across the whole
// terminal row, so a click meant for the transcript beside a short path
// opened a file. A file row, its wrapped continuation and the fold hint now
// act only from the cells their text is painted on — the diff counts
// included — and the hover highlights exactly those cells.
func TestDeliverableRowsAreTargetsOnlyOnTheirText(t *testing.T) {
	cwd := t.TempDir()
	countsFrame := Frame{Kind: FrameDeliverables, Final: true,
		Deliverables: []tool.Deliverable{{ID: "a", Name: "a.go", Kind: "text", RelPath: "pkg/a.go"}},
		TurnDiffs:    []event.TurnDiffFile{{Path: filepath.Join(cwd, "pkg", "a.go"), Added: 3}},
	}

	t.Run("a file row: the last text cell opens, the first blank cell and the far right do not", func(t *testing.T) {
		r, opened := textTargetScreen(t, cwd, countsFrame)
		row := paintedRowOf(t, r, "pkg/a.go")
		plain := stripANSI(r.vpPainted[row])
		// Both counts are always painted (render.go:6315-6319), so the row's
		// last text cells are the pair.
		if !strings.HasSuffix(plain, "+3 -0") {
			t.Fatalf("the fixture must paint the counts as part of the text, got %q", plain)
		}
		end := rowTextEnd(r.vpPainted[row])
		want := filepath.Join(cwd, "pkg", "a.go")
		clickCell(r, end-1, row)
		if len(*opened) != 1 || (*opened)[0] != want {
			t.Fatalf("the last text cell opened %v, want [%s]", *opened, want)
		}
		clickCell(r, end, row)
		clickCell(r, 79, row)
		if len(*opened) != 1 {
			t.Fatalf("a click right of the text opened %v", *opened)
		}
		if changed := r.ViewportClickToggle(79, row); changed || len(*opened) != 1 {
			t.Fatalf("the single-click event path opened %v (changed=%v) right of the text", *opened, changed)
		}
	})

	t.Run("a wrapped continuation is a target on its own text", func(t *testing.T) {
		r, opened := textTargetScreen(t, cwd, Frame{Kind: FrameDeliverables, Deliverables: manyDeliverables(2), Final: true})
		first := paintedRowOf(t, r, "out/f00-")
		row := first + 1
		if row >= len(r.vpPainted) || startsDeliverableRow(stripANSI(r.vpPainted[row])) {
			t.Fatalf("the wrapping fixture painted no continuation row for f00:\n%s", stripANSI(strings.Join(r.vpPainted, "\n")))
		}
		end := rowTextEnd(r.vpPainted[row])
		if end >= 80 {
			t.Fatalf("the fixture wants a continuation that ends short of the row, got end=%d", end)
		}
		clickCell(r, end, row)
		if len(*opened) != 0 {
			t.Fatalf("a click right of the continuation's text opened %v", *opened)
		}
		clickCell(r, end-1, row)
		want := filepath.Join(cwd, "out", "f00-很长的文件名很长的文件名很长的文件名很长的文件名很长的文件名很长的文件名.txt")
		if len(*opened) != 1 || (*opened)[0] != want {
			t.Fatalf("opened = %v, want [%s]", *opened, want)
		}
	})

	t.Run("the fold hint toggles from its text and from nowhere else", func(t *testing.T) {
		r, opened := textTargetScreen(t, cwd, Frame{Kind: FrameDeliverables, Deliverables: manyDeliverables(5), Final: true})
		hint := paintedRowOf(t, r, "more deliverable")
		end := rowTextEnd(r.vpPainted[hint])
		shown := func() string { return stripANSI(strings.Join(r.vpPainted, "\n")) }
		clickCell(r, end, hint)
		if changed := r.ViewportClickToggle(79, hint); changed {
			t.Fatal("the single-click event path toggled the fold right of the hint's text")
		}
		if strings.Contains(shown(), "click to hide") {
			t.Fatal("a click right of the hint's text folded the group")
		}
		clickCell(r, end-1, hint)
		if !strings.Contains(shown(), "click to hide") {
			t.Fatal("the last cell of the hint's text did not expand the group")
		}
		if len(*opened) != 0 {
			t.Fatalf("the hint row opened %v", *opened)
		}
	})

	t.Run("hover highlights the text alone, and only while the pointer is on it", func(t *testing.T) {
		cases := []struct {
			name   string
			frame  Frame
			needle string
		}{
			// The owner's screenshot: the counts end the text, each closed by
			// a reset the highlight must survive.
			{"a file row with diff counts", countsFrame, "pkg/a.go"},
			{"the fold hint", Frame{Kind: FrameDeliverables, Deliverables: manyDeliverables(5), Final: true}, "more deliverable"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				r, _ := textTargetScreen(t, cwd, tc.frame)
				row := paintedRowOf(t, r, tc.needle)
				before := r.vpPainted[row]
				end := rowTextEnd(before)
				hoverSeq := "\x1b[48;5;" + hoverBackgroundColor + "m"
				move := func(col, row int) {
					r.HandleMouse(inputEvent{kind: inputEventMouseMove, mouseCol: col, mouseRow: row, at: time.Now()})
				}
				move(end-1, row)
				cells := rowCellBackgrounds(r.vpPainted[row])
				if len(cells) != end {
					t.Fatalf("the hover painted %d cells, want the row's %d text cells only: %q", len(cells), end, r.vpPainted[row])
				}
				for col, bg := range cells {
					if bg != hoverSeq {
						t.Fatalf("cell %d carries background %q, want the hover background: %q", col, bg, r.vpPainted[row])
					}
				}
				if stripANSI(r.vpPainted[row]) != stripANSI(before) {
					t.Fatalf("the hover changed the row's text:\n%s\n%s", before, r.vpPainted[row])
				}
				move(end, row)
				if r.vpHoverRow != -1 || r.vpPainted[row] != before {
					t.Fatalf("the highlight stayed once the pointer left the text (vpHoverRow=%d):\n%s", r.vpHoverRow, r.vpPainted[row])
				}
				move(70, row)
				if r.vpPainted[row] != before {
					t.Fatalf("hovering the blank cells right of the text painted a highlight:\n%s", r.vpPainted[row])
				}
				move(end-1, row)
				if r.vpPainted[row] == before {
					t.Fatal("moving back onto the text did not restore the highlight")
				}
			})
		}
	})
}
```

```go
// pkg/tui/image_open_test.go

// TestImageMarkerRowIsATargetOnlyOnItsText extends the owner's 2026-11-26
// ruling to the image step's marker row: the click that opens the image must
// land on the marker's text; the blank cells to its right open nothing. A
// long file name wraps the marker, and each row is a target on its own text.
func TestImageMarkerRowIsATargetOnlyOnItsText(t *testing.T) {
	long := strings.Repeat("shot-", 20) + ".png" // wraps the marker line at 80 columns
	for _, tc := range []struct{ name, path string }{{"one.png", "/tmp/one.png"}, {long, "/tmp/" + long}} {
		t.Run(tc.name, func(t *testing.T) {
			r, opened := textTargetScreen(t, "", imageStepFrame(tc.path))
			marker := firstRowWithPrefix(r.vpPainted, "  └ ▤ ")
			if marker < 0 {
				t.Fatalf("no marker row painted:\n%s", stripANSI(strings.Join(r.vpPainted, "\n")))
			}
			// Every row the marker wraps into is a target on its own text.
			last := marker
			for last+1 < len(r.vpPainted) && isImageMarkerRow(r.vpPainted[last+1]) {
				last++
			}
			for row := marker; row <= last; row++ {
				end := rowTextEnd(r.vpPainted[row])
				before := len(*opened)
				if end < 80 {
					clickCell(r, end, row)
					if changed := r.ViewportClickToggle(end, row); changed || len(*opened) != before {
						t.Fatalf("row %d: a click right of the text opened %v (changed=%v)", row, *opened, changed)
					}
				}
				clickCell(r, end-1, row)
				if len(*opened) != before+1 || (*opened)[before] != tc.path {
					t.Fatalf("row %d: opened = %v, want %s once more", row, *opened, tc.path)
				}
			}
		})
	}
}
```

```go
// pkg/tui/chat_session_test.go —— 放在 TestClickingAFanoutTaskRowOpensThatTasksView 之后

// TestSubagentCardRowsAreTargetsOnlyOnTheirText extends the owner's 2026-11-26
// ruling to the rows that open a subagent's view — a lifecycle card's row and
// a fanout task row: the click must land on the row's text; the blank cells to
// its right open nothing. The frames are painted for real (a hand-made render
// is re-laid-out by the first paint at another width).
func TestSubagentCardRowsAreTargetsOnlyOnTheirText(t *testing.T) {
	paint := func(t *testing.T, frames ...Frame) *Renderer {
		t.Helper()
		r, _ := newPlanDrawerScreenWithOut(t, 100, 30)
		for _, f := range frames {
			r.retainFrameLocked(f)
		}
		r.mu.Lock()
		r.paintViewportLocked()
		r.mu.Unlock()
		return r
	}

	t.Run("a lifecycle card row", func(t *testing.T) {
		r := paint(t,
			Frame{Kind: FrameStatus, Title: "subagent plan-reviewer spawned", Content: "plan-reviewer", AgentID: "subagent-42", SubagentLifecycleCard: true, Final: true},
			Frame{Kind: FrameAssistant, Content: "Verdict: rework. internal/x.go:12 says otherwise.", AgentID: "subagent-42", Final: true},
			Frame{Kind: FrameStatus, Title: "subagent plan-reviewer ended", Content: "plan-reviewer ok", AgentID: "subagent-42", SubagentLifecycleCard: true, Final: true},
		)
		row := rowOfPrimaryText(t, r, "subagent plan-reviewer ended")
		end := rowTextEnd(r.vpPainted[row])
		clickCell(r, end, row)
		if changed := r.ViewportClickToggle(99, row); changed || r.ActiveView() != "" {
			t.Fatalf("a click right of the card's text opened view %q (changed=%v)", r.ActiveView(), changed)
		}
		clickCell(r, end-1, row)
		if got := r.ActiveView(); got != "subagent-42" {
			t.Fatalf("the last text cell opened %q, want subagent-42", got)
		}
	})

	t.Run("a fanout task row", func(t *testing.T) {
		r := paint(t, fanoutFrameForTest())
		for _, agentID := range []string{"task-a", "task-b"} {
			r.ensurePerAgentVM(agentID).append(Frame{Kind: FrameAssistant, Content: "transcript of " + agentID, AgentID: agentID, Final: true})
		}
		row := rowOfPrimaryText(t, r, "check the sandbox profile")
		end := rowTextEnd(r.vpPainted[row])
		clickCell(r, end, row)
		if changed := r.ViewportClickToggle(99, row); changed || r.ActiveView() != "" {
			t.Fatalf("a click right of the task row's text opened view %q (changed=%v)", r.ActiveView(), changed)
		}
		clickCell(r, end-1, row)
		if got := r.ActiveView(); got != "task-b" {
			t.Fatalf("the last text cell opened %q, want task-b", got)
		}
	})
}
```

**Verify**: §5 的定点测试命令 → 编译通过；deliverable 的「文件行」「续行」「提示行」子测试 FAIL（报 `a click right of the text opened […]`／`… folded the group`／`… toggled the fold…`），hover 两个子测试 FAIL（`the hover painted 80 cells, want the row's N text cells only`），image 两个子测试 FAIL（`a click right of the text opened […]`），subagent 两个子测试 FAIL（`a click right of the … text opened view …`）。**任一通过＝fixture 没造出场景，先修 fixture 再继续。**

### Step 2: 点击按列命中

在 `pkg/tui/reducer.go` 按 §3 新增 `textOnlyTarget`（放 `deliverableHintRow` 之后）；`transcriptClickLocked` 签名改为 `(s shownScreen, col, row int)`，在 `vm := r.activeVMLocked()` 之后加早返回；两个调用点传列（`:6968` 传 `col`，`:8149` 传 `clickCol`）；函数文档注释补一句列规则。

**Verify**: 定点测试 → 点击类子测试（文件行、续行、提示行、image 两个、subagent 两个）全部 PASS；hover 两个子测试仍 FAIL。

### Step 3: hover 只在文字上、只亮文字格

按 §3 改 `viewportHoverAt`（`:7044-7046`）与 `paintViewportLocked`（`:5588-5590`）。

**Verify**: 定点测试 → 全部 `--- PASS`。

### Step 4: 注释同步

`isClickableRow`（`:3294-3298`）、`render.go:468-472` `vpHoverRow`、`toggleAtScreenRow`（`:3049-3052`）、`deliverableAtScreenRow`（`:3187-3196`）、`imageMarkerAtScreenRow`（`:3123-3126`）、`agentAtScreenRow`（`:3096-3102`）各补一句（见 §3 末段）；`TestDeliverableRowsAreClickable` 注释（`reducer_test.go:6913-6916`）补「热区另由 `textOnlyTarget` 收到文字，见 `TestDeliverableRowsAreTargetsOnlyOnTheirText`」。

**Verify**: `gofmt -l pkg/tui` 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui/` exit 0。

### Step 5: 包级回归

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`（约 200s）。既有 `TestDeliverableClickOpensTheClickedFile`、`TestImageMarker*`、`TestClickingTheImageMarkerRowLaunchesTheClickedFile`、`TestViewportIsClickableRow`、`TestClickingAFinishedSubagentCardOpensItsView`、`TestClickingAFanoutTaskRowOpensThatTasksView` 必须仍绿（它们都在 0 列或文字内点击）。若有测试因「文字外点击不再生效」而红：那是钉旧 bug 的断言，按新契约改断言并在报告里逐条列出——**除 §3 矩阵列出的几类行以外**任何测试变红＝STOP。

### Step 6: 真机验收（§8）

### Step 7: 收口

把本计划落盘为 `plans/389-tui-deliverable-rows-click-text-only.md`（状态行改「已实施并真机验收通过（日期）」＋新增「验收记录」章节）；`plans/README.md` 只**追加**一行 389 索引（TODO→DONE，含验收日期）；`"$D" stop`（它已 `tmux kill-session` 并杀 provider，`driver.sh:314-319`）；删除临时 `FOREBRAIN_RUN_WORK`。改动留工作区不 commit。

## 7. Test plan

- 新增：`TestDeliverableRowsAreTargetsOnlyOnTheirText`（文件行末格开／首空白格与最右格不开、单次点击事件路径不开；折行续行按自身文字；提示行文字内切／文字外不切且不开；hover 仅文字格——带计数的文件行与 Faint 提示行各一——离开文字即灭、回来即亮）、`TestImageMarkerRowIsATargetOnlyOnItsText`（短名、折行长名两例：每行文字外不开、文字内开）、`TestSubagentCardRowsAreTargetsOnlyOnTheirText`（生命周期卡行、fanout 任务行：文字外不切视图、文字内切）。
- 结构范式：`TestDeliverableClickOpensTheClickedFile`（`reducer_test.go:6974`）、`TestClickingTheImageMarkerRowLaunchesTheClickedFile`（`image_open_test.go:294`）、`TestClickingAFanoutTaskRowOpensThatTasksView`（`chat_session_test.go:24510` 起）、hover 断言范式 `plan_drawer_test.go:1073-1090`、逐格背景 `rowCellBackgrounds`（`chat_session_test.go:26990`）。
- 断言若因字节细节（如 `rowCellBackgrounds` 的格数口径）与实际差 1，允许调整**断言表达**，但「高亮格数＝文字格数」「计数后的 reset 不断高亮」「文字外零高亮」「文字外零打开／零切视图」四条属性不得放松；调整处在报告里写明。
- 回归：`pkg/tui` 全量；`go vet`；`gofmt`。
- 失败路径：文字外点击既不打开也不切换／切视图（`ViewportClickToggle` 返回 false）；hover 在文字外不残留高亮。

## 8. 真机验收（必做；测试绿≠真机可用）

构建必须带 `-tags fts5`（driver 的 `build` 已带）。全新 `FOREBRAIN_RUN_WORK`（stale-binary 陷阱）、**私有 tmux server**、隔离 `FOREBRAIN_HOME`、临时项目目录；**不碰真实 `~/.forebrain*`**。

两个必须先摆对的坑：
1. **PATH 桩要能到达 pane**。打开器用 **PATH 桩**取证而不真的拉起 VS Code／Preview：`vscodeCLIPath` 走 `exec.LookPath("code")`（`vscode_open.go:39-42`），图片走 `open`（`image_open.go:100-104`）。但本机默认 socket 上已有一个 tmux server（会话 `fb-dup`），tmux 的 `update-environment` 不含 PATH——`driver.sh:132-133` 新建的 pane 继承的是**server** 的 PATH，桩会被绕过、S1a 假通过，还会对 owner 真实的 VS Code 执行 `--install-extension`。所以先 `export TMUX_TMPDIR="$FOREBRAIN_RUN_WORK/tmux"`，让 driver 起一个继承了桩 PATH 的私有 server。
2. **阳性对照在前**。先证「点文字会开」（桩日志增行／视图切换），再做「点文字外不开」（日志不增／视图不变）——否则桩没生效时阴性用例会空过。打开器在 goroutine 里跑、git 仓库下是 5 个子进程的 SCM 阶梯（`vscode_open.go:81-123、163-171`），日志行要**轮询**等，不要点完立刻读。

```bash
D=.claude/skills/run-forebrain/driver.sh
export FOREBRAIN_RUN_WORK="$(mktemp -d)" FOREBRAIN_RUN_SESSION=fbt389 FOREBRAIN_RUN_PORT=8764
"$D" reset && "$D" build
PROJ="$FOREBRAIN_RUN_WORK/proj"; mkdir -p "$PROJ"; git -C "$PROJ" init -q   # 保险：按既往验收记忆避免项目类型被判成 office
export TMUX_TMPDIR="$FOREBRAIN_RUN_WORK/tmux"; mkdir -p "$TMUX_TMPDIR"       # 私有 tmux server，继承下面的桩 PATH
mkdir -p "$FOREBRAIN_RUN_WORK/stub"
for cmd in code open; do
  printf '#!/bin/sh\nprintf "%%s\\n" "$*" >> "%s/opener-stub.log"\n' "$FOREBRAIN_RUN_WORK" > "$FOREBRAIN_RUN_WORK/stub/$cmd"
  chmod +x "$FOREBRAIN_RUN_WORK/stub/$cmd"
done
: > "$FOREBRAIN_RUN_WORK/opener-stub.log"
python3 -c 'import base64,sys; open(sys.argv[1],"wb").write(base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="))' "$PROJ/tiny.png"
LONG="$(python3 -c 'print("\\n".join("line%02d" % i for i in range(1,31)))')"
# subagent_run 放最后：它的子代理请求拿到的是脚本用尽后的「done」，不会抢走主代理的脚本槽（tool 模式计数器进程共享，fake_provider.py:571）
PATH="$FOREBRAIN_RUN_WORK/stub:$PATH" ENABLE_SUBAGENT=1 "$D" start tool "[{\"name\":\"write_file\",\"arguments\":{\"file_path\":\"pkg/a.go\",\"content\":\"package a\\n\"}},{\"name\":\"write_file\",\"arguments\":{\"file_path\":\"pkg/b.go\",\"content\":\"package b\\n\"}},{\"name\":\"write_file\",\"arguments\":{\"file_path\":\"pkg/c.md\",\"content\":\"# c\\n\"}},{\"name\":\"write_file\",\"arguments\":{\"file_path\":\"pkg/long.txt\",\"content\":\"$LONG\\n\"}},{\"name\":\"read_file\",\"arguments\":{\"file_path\":\"tiny.png\"}},{\"name\":\"read_file\",\"arguments\":{\"file_path\":\"pkg/long.txt\"}},{\"name\":\"present_files\",\"arguments\":{\"files\":[{\"path\":\"$PROJ/pkg/a.go\"},{\"path\":\"$PROJ/pkg/b.go\"},{\"path\":\"$PROJ/pkg/c.md\"},{ \"path\":\"$PROJ/tiny.png\"}]}},{\"name\":\"subagent_run\",\"arguments\":{\"task\":\"report done\"}}]"
"$D" submit "ship the files" && "$D" wait "Worked for"
```

参数名以源码为准：`write_file`／`read_file` 的路径参数是 `file_path`（`file_tools.go:65、988`），`present_files` 的是 `path`（`artifacts.go:209`）。8 次调用后 fake provider 答「done」结束回合；交付物组 4 个文件折成 3 行＋提示行（`toolOutputMaxLines=3`，`render.go:659`），`.md` 的 glyph 是 `¶`、图片是 `▩`（`artifacts.go:432-433`）。`long.txt` 30 行让 `read_file` 卡成为**长卡**（有表头、可折叠、整行 hover），供 S4 回归用；`subagent_run` 留下一张子代理生命周期卡，供 S6 用。

辅助函数（每个 `$D` 调用都要带着上面 export 的变量；`target` 从**未 hover 的基线屏**量，因为 hover 补白会改变 `capture-pane` 的行尾）：

```bash
# eval "$(target <needle>)" → ROW（1-based 行）、END（文字右侧第一个空白格的 1-based 列）
target() { python3 - "$1" <<'PY'
import os, subprocess, sys, unicodedata
out = subprocess.run(["tmux", "capture-pane", "-t", os.environ["FOREBRAIN_RUN_SESSION"], "-p"],
                     capture_output=True, text=True).stdout.split("\n")
def width(s): return sum(2 if unicodedata.east_asian_width(c) in "WF" else 1 for c in s)
for i, line in enumerate(out, 1):
    text = line.rstrip()
    if sys.argv[1] in text:
        print("ROW=%d END=%d" % (i, width(text) + 1)); sys.exit(0)
sys.exit("no screen row carries %r" % sys.argv[1])
PY
}
stub_lines() { wc -l < "$FOREBRAIN_RUN_WORK/opener-stub.log" | tr -d ' '; }
await_stub() { # await_stub <起始行数> <needle>：等桩日志在起始行之后出现含 needle 的行（最多 10s）
  local i; for i in $(seq 1 50); do
    [ "$(tail -n +"$(( $1 + 1 ))" "$FOREBRAIN_RUN_WORK/opener-stub.log" | grep -c -- "$2")" -gt 0 ] && return 0; sleep 0.2
  done; return 1
}
rowbytes() { tmux capture-pane -t "$FOREBRAIN_RUN_SESSION" -e -p | sed -n "$1p"; }
```

逐格 hover 背景计数（保存为 `$FOREBRAIN_RUN_WORK/cells.py`，参数＝行字节）：

```python
import re, sys
line = sys.argv[1]; bg = ""; n = 0; i = 0
while i < len(line):
    if line[i] == "\x1b":
        m = re.match(r"\x1b\[([0-9;]*)m", line[i:]) or re.match(r"\x1b\[[0-9;?]*[A-Za-z]", line[i:])
        seq = m.group(0); p = m.group(1).split(";") if m.group(1) is not None else []
        if p == [] or p == ["0"] or p == ["49"]: bg = ""      # reset / default background
        elif "48" in p: bg = seq                              # any 48;x;y background
        i += len(seq); continue
    n += 1 if bg else 0; i += 1
print(n)
```

场景（每步前先 `rowbytes $ROW` 取基线；每个场景的证据＝行字节原文＋桩日志行数／屏上文案。`target` 的 needle 一律用**不含 `|` 的子串**——`line01` 只出现在长卡正文首行 ` 1|line01`）：

| # | 场景 | needle | 期望（修复后） |
| :-- | :-- | :-- | :-- |
| S1b | 阳性对照：文件行**文字内**点击仍开 | `pkg/a.go` | 桩日志新增含 `a.go` 的行（`-r …a.go` 或 `--open-url vscode://…a.go…`）。**不成立就停：桩没生效，不许继续** |
| S1a | 文件行**右侧空白**点击不开 | `pkg/a.go` | 桩日志行数不变 |
| S1c | 文件行 hover 只亮文字 | `pkg/a.go` | 文字末格 hover：行含 `48;5;236`（`hoverBackgroundColor="236"`，`plan_drawer.go:452`），`cells.py` 输出＝`$((END-1))`；文字外 hover：行字节回到基线、无 `48;5;236` |
| S2a | 提示行右侧空白不切换 | `more deliverable` | 屏上 `click to show` 计数仍为 1（未展开） |
| S2b | 提示行文字内切换 | `more deliverable` | 屏上 `click to hide` 计数为 1（展开）；桩日志行数不变 |
| S3b | 阳性对照：marker 行文字内仍开 | `▤ tiny.png` | 桩 `open` 记到含 `tiny.png` 的行（不成立就停） |
| S3a | marker 行右侧空白不开 | `▤ tiny.png` | 桩日志行数不变 |
| S4 | 回归：长工具卡不受影响 | `line01` | 右侧空白 hover：`cells.py` 输出＝120（整行高亮，同旧）；右侧空白点击：卡片展开（屏上出现 `line30`），再点一次折回（`line30` 消失） |
| S6b | 阳性对照：子代理卡行文字内点击切视图 | ` ended`（子代理生命周期卡尾行；以 `"$D" screen` 实际文案为准，卡头含 `subagent` 与 `ended`） | composer 页脚出现 `viewing … esc to return`（`grep -c "esc to return"`＝1）。**不成立就停并报告（卡不可开时 S6 真机不可达，不伪造）** |
| S6a | 子代理卡行右侧空白不切视图 | 同上（先 `"$D" key Escape` 回主视图再量） | 页脚无 `esc to return`（0） |
| S5 | 干净退出 | — | pane 出现 `EXIT=0`；`tui.err` 为空 |

逐条命令（`ROW`／`END` 来自 `eval "$(target <needle>)"`；S2b 展开后交付物组变高，S3/S4/S6 前必须重新 `target`）：

```bash
# S1b（阳性对照）
eval "$(target 'pkg/a.go')"; base="$(rowbytes "$ROW")"; n="$(stub_lines)"
"$D" click $((END-1)) "$ROW"; await_stub "$n" a.go && echo "S1b OK: opened via stub" || { echo "S1b FAILED: stub not reached"; "$D" stop; exit 1; }
# S1a
n="$(stub_lines)"; "$D" click $((END+20)) "$ROW"; sleep 1; [ "$(stub_lines)" = "$n" ] && echo "S1a OK" || echo "S1a FAILED"
# S1c
"$D" send "$(printf '\033[<35;%s;%sM' $((END-1)) "$ROW")"; sleep 0.3
rowbytes "$ROW" | grep -c '48;5;236'            # ≥1
python3 "$FOREBRAIN_RUN_WORK/cells.py" "$(rowbytes "$ROW")"   # = END-1
"$D" send "$(printf '\033[<35;%s;%sM' $((END+20)) "$ROW")"; sleep 0.3
[ "$(rowbytes "$ROW")" = "$base" ] && echo "S1c OK: blank hover paints nothing" || echo "S1c FAILED"
# S2a / S2b
eval "$(target 'more deliverable')"
"$D" click $((END+20)) "$ROW"; sleep 0.5; "$D" screen | grep -c "click to show"   # 1
n="$(stub_lines)"; "$D" click $((END-1)) "$ROW"; sleep 0.5; "$D" screen | grep -c "click to hide"  # 1
[ "$(stub_lines)" = "$n" ] && echo "S2 OK" || echo "S2 FAILED: hint opened a file"
# S3b（阳性对照）→ S3a
eval "$(target '▤ tiny.png')"; n="$(stub_lines)"
"$D" click $((END-1)) "$ROW"; await_stub "$n" tiny.png && echo "S3b OK: opened via stub" || { echo "S3b FAILED: stub not reached"; "$D" stop; exit 1; }
n="$(stub_lines)"; "$D" click $((END+20)) "$ROW"; sleep 1; [ "$(stub_lines)" = "$n" ] && echo "S3a OK" || echo "S3a FAILED"
# S4（长卡：read_file long.txt；展开后卡片 31 行、顶部滚出屏幕，所以折回时点展开后仍可见的 line20 行）
eval "$(target 'line01')"
"$D" send "$(printf '\033[<35;%s;%sM' 100 "$ROW")"; sleep 0.3
python3 "$FOREBRAIN_RUN_WORK/cells.py" "$(rowbytes "$ROW")"   # 120（整行高亮，同旧）
"$D" click 100 "$ROW"; sleep 0.5; "$D" screen | grep -c "line30"   # 1（展开）
eval "$(target 'line20')"; "$D" click 100 "$ROW"; sleep 0.5; "$D" screen | grep -c "line30"   # 0（折回）
# S6b（阳性对照）→ S6a（子代理生命周期卡；视图切换看 composer 页脚）
"$D" screen | grep -n "ended"            # 先肉眼确认卡行文案，必要时换 needle
eval "$(target ' ended')"; n="$(stub_lines)"
"$D" click $((END-1)) "$ROW"; sleep 0.5; "$D" screen | grep -c "esc to return"   # 1（切到子代理视图）
"$D" key Escape; sleep 0.5; "$D" screen | grep -c "esc to return"                 # 0（回主视图）
eval "$(target ' ended')"; "$D" click $((END+20)) "$ROW"; sleep 0.5
"$D" screen | grep -c "esc to return"   # 0（文字外不切视图）
[ "$(stub_lines)" = "$n" ] && echo "S6 OK" || echo "S6 FAILED: the card row opened a file"
# S5（空闲时第一次 Ctrl+C 只提示，第二次才退出；pkg/tui/run.go:2537）
"$D" key C-c; sleep 0.5; "$D" key C-c; "$D" wait "EXIT="; "$D" screen | grep -o "EXIT=[0-9]*"   # EXIT=0
cat "$FOREBRAIN_RUN_WORK/tui.err"   # 空
```

`click` 的坐标是 1-based：文字最后一格＝`END-1`，文字右侧第一个空白格＝`END`，`END+20` 是更靠右的空白。

**对照（可选）**：Step 1 的红测试已是「修复前」证据；若要真机 A/B，用 `go build -overlay` 另起一份修复前二进制（附录 A 探针同法）跑 S1a/S1c，预期桩日志出现 `a.go`、hover 铺满 120 格。

**验收坑位**（driver 文档与既往记忆）：`driver.sh start` 偶发立即退出（EXIT=0、tui.err 空）是 harness flake，`reset`+`start` 重跑（重跑前重新 `mkdir -p "$TMUX_TMPDIR"`）；信任页由 driver 自动 Down+Enter；`$D stop` 会连 provider 一起杀。

## 9. Done criteria

- [ ] Step 1 的红测试在修复前红（输出贴进验收记录），Step 3 后全绿。
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`；`gofmt -l pkg/tui` 空；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui/` exit 0。
- [ ] `grep -n "transcriptClickLocked(s, row)\|transcriptClickLocked(s, clickRow)" pkg/tui/*.go` 无匹配（旧的无列调用已清）。
- [ ] `git status --porcelain` 的改动只在 §4 In scope 清单内；未 commit。
- [ ] 真机 S1b→S1a/S1c、S2a/S2b、S3b→S3a、S4、S6b→S6a、S5 证据齐全（行字节＋桩日志行数／页脚文案）；三个阳性对照成立；S1a/S1c 与修复前行为相反。S6 若因卡不可开而不可达，以单测为证并在验收记录写明。
- [ ] `plans/389-…md` 落盘并回填验收记录；`plans/README.md` 追加 389 行。

## 10. STOP conditions

- §2 摘录与现码内容不一致（漂移），或 `pkg/tui/reducer.go`／`render.go` 有他人的未提交改动落在 In scope 区域。
- Step 1 任一子测试在修复前就通过（fixture 不成立），两次修正 fixture 仍如此。
- 修复需要改动 §4 Out of scope 列出的五个按行解析器的签名，或需要给 hover 新增字段／新增 `\x1b[K` 发射。
- `pkg/tui` 全量里出现矩阵几类行**以外**的行为变化（例如普通工具卡折叠、选择、composer 光标）。
- 真机 S1b／S3b 阳性对照不成立（桩没生效），或验收过程中真实 VS Code／浏览器／Preview 被拉起——立刻 `"$D" stop`，核对 `TMUX_TMPDIR` 与 PATH，不要让它再打开。
- `pkg/architecture` 的预存红（`TestTestFilesCorrespondToProductionFiles`）不算本计划的失败；其它包红先判断是否与本改动相关再决定。

## 11. Maintenance notes

- 以后任何「某行是点击目标」的新种类，问一句：热区是整行还是文字？是文字就加进 `textOnlyTarget` 的 switch，hover／高亮自动跟上；不要再写第二个按行的命中分支。
- `textOnlyTarget` 的宽度口径是 runewidth（`displayLineWidth`），与 `fitRow` 同尺；高亮的补白用 `lipgloss.Width`（`applyHoverHighlight` 内）——两类库里 `≡`／`·`／CJK 口径一致（`EastAsianWidth=false`，`render.go:59`；评审 `TestReviewWidthAgreement` 实测一致），hover 子测试的「格数＝文字格数」断言把这一点钉住；若将来换宽度库要三处同换。`textOnlyTarget` 剥样式只剥 SGR（与 `deliverableHintRow`／`isImageMarkerRow` 同约定），前提是这几类行不发 `\x1b[K`（今天成立）。
- 测试量文字末列必须量真 paint 出来的屏（`vpPainted`），不能量手搓的 `vpLastRender`——`liveScreenLocked` 只在第一次 paint 前用它，paint 之后按活宽重排。
- 评审重点：(1) `transcriptClickLocked` 早返回是否误伤这些块上的其它目标——今天没有（非 agent 自身帧、提示行之外无折叠、卡行上只有切视图）；(2) `applyHoverHighlight(line, end, …)` 在计数 `\x1b[0m` 之后重申背景，S1c 的逐格计数是它的真机证据。
- 明确延后：web 端交付物列表热区未调查；marker 行仍无 hover 高亮（未新增 affordance，owner 未要求）；`render.go:5265` 附近的 lipgloss 宽度注释已过时，不在本计划改。

## 12. 评审记录（独立冷读共四轮，2026-11-26；均已到源码核实）

| # | 级别 | 发现 | 出处 | 处置 |
| :-- | :-- | :-- | :-- | :-- |
| 1 | BLOCKER | 测试 fixture `HasSuffix(plain, "+3")` 永假：两个计数总是成对画出（`render.go:6315-6319`） | 冷读 1 | 改 `"+3 -0"` |
| 2 | BLOCKER | `highlightCells` 不重申背景，计数后的 `\x1b[0m` 会让高亮半途断掉（`plan_drawer.go:516-521`、`render.go:6891-6896`） | 冷读 1 | 改用 `applyHoverHighlight(line, end, …)`；hover 子测试加带计数的文件行 |
| 3 | BLOCKER | 验收脚本 `write_file`／`read_file` 参数名写成 `path`，实为 `file_path`（`file_tools.go:65、988`） | 冷读 1 | 已改 |
| 4 | BLOCKER | 默认 socket 已有 tmux server（`fb-dup`），pane 继承 server PATH，桩被绕过且可能真装扩展到 owner 的 VS Code | 冷读 1 | 私有 `TMUX_TMPDIR`；阳性对照前置；列入 STOP |
| 5 | BLOCKER | 折行续行子测试用计划自己的修复仍红：`end` 量自手搓的 80 列 `vpLastRender`，而点击屏在首次 paint 后按活宽重排（70→68） | 冷读 2 | 所有点击 fixture 改为真 paint（`textTargetScreen`），从 `vpPainted` 量 |
| 6 | BLOCKER | 图片测试 `opened` 跨行累加，第二行必假红 | 冷读 2 | 改「计数不增」断言 |
| 7 | 范围 | 子代理生命周期卡／fanout 任务行同族（`agentAtScreenRow`／`openAgentViewAtRowLocked`／`isClickableRow` 整行） | 冷读 2 | owner 裁决③一并改：`textOnlyTarget` default 分支、单测、S6 |
| 8 | SHOULD-FIX | `cells.py` 的 `startswith("48;")` 对已切分的参数恒假 | 冷读 1 | 改 `"48" in p` |
| 9 | SHOULD-FIX | S4 用 `write_file` 短卡做整行 hover 回归，但短卡本就无 hover（`isClickableRow` 的 `headerRow < 0` 出口） | 冷读 1 | 改用 30 行 `long.txt` 的 `read_file` 长卡 |
| 10 | SHOULD-FIX | `git init` 与项目类型无关（未设即 coding，`runner.go:3619-3623`）；git 仓库下打开器是异步多子进程 | 冷读 1 | 保留 `git init` 作保险（既往验收记忆），日志改为轮询 |
| 11 | SHOULD-FIX | `row_a/end_a…` 无命令赋值 | 冷读 1 | `target` 辅助函数 |
| 12 | NIT | 表格内占位 `sed -n '<row>p'`、`\|` 会被 driver 当参数、`≡ pkg/c.md` 的 glyph 实为 `¶`；`tmux kill-session` 多余 | 冷读 1 | `rowbytes` 函数、表格只写意图、glyph 更正、删多余步骤 |
| 13 | NIT | `TestDeliverableRowsAreClickable` 注释行号 6913-6916；`clickCell` 与 `clickAt`／`clickRow` 重复；§2.3 探针源码未附 | 冷读 1 | 行号更正；`clickCell`＝时钟重置＋`clickAt`；探针见附录 A |
| 14 | NIT | `textOnlyTarget` 只剥 SGR，行里若有 `\x1b[K` 会多算 2 列（今天不触发） | 冷读 1 | 注释写明前提（§11） |
| 15 | NIT | S5 引用 `run.go:2537` 路径不明 | 冷读 2 | 补全 `pkg/tui/run.go:2537` |
| 16 | BLOCKER | §8 `start tool` 的 JSON 在 `present_files` 与 `subagent_run` 之间多了 `]}`，严格 `json.loads` 报 `Extra data`，S1–S6 全跑不起来 | 冷读 3 | 删掉多余的 `]}`；已用 python 按_shell 交付形态解析复核（VALID，8 次调用顺序正确） |
| 17 | SHOULD-FIX | §5 表格单元格里的 `-run 'A\|B\|C'`：GFM 把代码段内的 `\|` 也当列分隔，照抄得到被截断的模式 | 冷读 3 | 命令移出表格放进代码块 |

冷读 3（qwen 第三轮）用 overlay 实测了修订后的三个测试：无修复时三测试全 FAIL 且报对原因（红门成立）；修复后全 PASS；fanout 卡头与 waiting 行 `textOnly=(0,false)`、任务行 `(end,true)`——default 分支无误伤。

四轮评审核对无误的部分：全部引用行号与摘录；根因与修复断在因上（冷读 2 overlay 实测：修复后每行 `end-1` 开、`end` 拒；hover 恰画 `end` 格）；`vpPainted[row]` 就是 `canvasRow(...)`（不补白、不发 `\x1b[K`）；长图名续行缩进 4 格且 `isImageMarkerRow` 接受；无 hover 重绘与 `before` 字节相同；`vpHoverRow` 读者只有 `:5588`／`:7052`；`transcriptClickLocked` 仅两调用点；带 `AgentID` 的帧进 per-agent VM；driver `start tool` JSON 契约、`click` 1-based、hover `\033[<35;col;rowM`；`file_path`／`path` 参数名。冷读 2 的 §8 真机部分标 unverified（其环境无 tmux／桩 PATH），断言逻辑与单测一致。

## 附录 A：修复前复现探针（`go test -overlay`，零项目写入）

`/tmp/fb-repro-389/zz_repro_test.go`：

```go
package tui

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

func TestReproDeliverableClickPastTextOpensFile(t *testing.T) {
	cwd := t.TempDir()
	opened := stubImageOpener(t)
	r := NewRenderer(io.Discard, io.Discard).WithWorkingDir(cwd)
	r.viewportMode = true
	r.vm.append(Frame{Kind: FrameDeliverables, Deliverables: []tool.Deliverable{{ID: "a", Name: "a.go", Kind: "text", RelPath: "pkg/a.go"}}, Final: true})
	r.vpHeight, r.vpBodyHeight = 10, 10
	r.vpLastRender = renderViewport(&r.vm, 80, 10, 0, DiffThemeDark, cwd)
	plain := strings.TrimRight(stripANSI(r.vpLastRender.lines[0]), " ")
	end := displayLineWidth(plain)
	t.Logf("row 0 paints %q: text ends at column %d of 80", plain, end)
	r.lastClickTime = time.Time{}
	r.ViewportSelectStart(70, 0)
	r.ViewportSelectEnd(70, 0)
	t.Logf("click at col 70 (text ends at %d): opened=%v", end, *opened)
	if len(*opened) != 0 {
		t.Fatalf("a click %d cells right of the text opened %v", 70-end, *opened)
	}
}
```

`/tmp/fb-repro-389/overlay.json`（把 `<repo>` 换成仓库绝对路径）：

```json
{"Replace":{"<repo>/pkg/tui/zz_repro_test.go":"/tmp/fb-repro-389/zz_repro_test.go"}}
```

命令与修复前输出见 §2.3。

## Implementation Status

**VERDICT: APPROVE**（tech lead 评审，1 轮 REVISE 后通过；2026-11-26）

- **执行器**：alibaba/deepseek-v4.1-flash-1017（owner 指定）。
- **WORKTREE（已合并并清理）**：执行时在 `.worktrees/tui-rows-click-text-only`（分支同名，基于 dev `041a9627`）。owner 指示「合并回主树dev分支并清理worktree」后：合成单提交 `6341ce52 fix(tui): make text-only rows click only on their text (plan 389)`，主树 `git merge --ff-only` 落 dev `041a9627..6341ce52`（5 个文件 +402／−13，全在 §4 In scope 内），未 push；dev 上门禁复跑：`gofmt -l pkg/tui` 空、`go vet` exit 0、`go build -tags fts5` exit 0、`go test -tags fts5 ./pkg/tui -count=1` → `ok 208.933s`；`git worktree remove`（仅剩 ignored 的 build/ 与 third_party 拷贝）＋ `git branch -d` 完成。合并前主树 `pkg/tui`／`frontend` 干净，已 peer_send 知会并行的 d8／54（dev HEAD 前移、plans/389 编号已占）。
- **任务清单**：Step 0 漂移检查 ✔；Step 1 红测试 ✔（8/8 子测试修复前红）；Step 2 点击按列 ✔；Step 3 hover ✔；Step 4 注释 ✔；Step 5 包级回归 ✔；Step 6 真机 ✔；Step 7 落盘／索引由评审完成（执行器按 hand-off 跳过）。

**偏差与处置**

1. **REVISE-1（评审探针发现，矩阵外回归）**：首版 `textOnlyTarget` 的 `default` 分支复用 `agentAtScreenRow`，它对「块自带 agent == 当前视图」也返回 ok，而子代理自己视图里的 thinking／memory-compact 帧都带本人 `AgentID`——于是这些行被误判成文字型目标：hover 从 80 格缩到文字宽、点「click to expand」行右侧空白不再折叠（探针实测 `textOnly=(36,true)`、`changed=false`）。修法：`textOnlyTarget(screenRow, view, vm)` 增 `view` 形参，`default` 分支 `!agentRow || agentID == view → false`；三处调用点传 `r.activeView`；新增子测试 `the agent's own view keeps row-wide targets`（修复前红：`row 4 of the view's own card reports a text-only target (end=36)`；修复后绿）。`agentAtScreenRow`／`openAgentViewAtRowLocked`／`isClickableRow` 本体未动。
2. **真机 harness 事实**（执行器发现，评审复现时沿用）：① driver 种子的 `deepseek/deepseek-chat` 被目录判为不接受图片输入，`read_file tiny.png` 被拒、无 marker 行——须在 `serve` 后 `"$D" db "UPDATE fb_llm_models SET model='deepseek/deepseek-chat-389vision'"`（目录不认识的模型 fail open，`pkg/run/image_input.go` 官方路径）并按 `cmd_start` 同形态手工起 TUI（`serve`/`start` 会重新种子覆盖模型名）；② `ENABLE_SUBAGENT=1` 必需（否则 `subagent_run` 为 unknown tool）；③ harness 对 `subagent_run` 画出的是 fanout 卡（`● Ran 1 fork task`／`  └ ✓ report done`）而非 `subagent … ended` 生命周期卡——S6 以 fanout 任务行取证，生命周期卡行由单测覆盖；④ `tmux capture-pane -e` 会裁掉行尾空白（即使带背景），S4 的「cells==120」不可测，改用「长卡文字右侧空白 hover 仍出现 `48;5;236`」作整行 hover 未变的证据；⑤ fake provider 端口可能被其它会话占用（本次 8765 被占，换 8791）；⑥ 计划里的可选真机 A/B 对照二进制未做，修复前行为由 Step 1 红测试承担。

**评审亲跑的验证（worktree 内）**

- `go test -overlay` 探针：修订前子代理视图 thinking/compact 行 `textOnly=true`、hover 16/36/11 格、click 不折叠；修订后 `textOnly=false`、hover 80 格、click 折叠。
- 定点：`TestDeliverableRowsAreTargetsOnlyOnTheirText`（4 子测试，hover 两例）／`TestImageMarkerRowIsATargetOnlyOnItsText`（2）／`TestSubagentCardRowsAreTargetsOnlyOnTheirText`（3）全部 PASS。
- `gofmt -l pkg/tui` 空；`go vet -tags fts5 ./pkg/tui/` exit 0；`go build -tags fts5 ./cmd/forebrain` exit 0；旧调用 `transcriptClickLocked(s, row)`／`(s, clickRow)` grep 无匹配。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok 212.473s`。
- 真机（全新 run root、私有 `TMUX_TMPDIR`、PATH 桩 `code`/`open`、隔离 `FOREBRAIN_HOME`；脚本 8 次调用 write×4→read×2→present_files→subagent_run）：S1b 点 `pkg/a.go` 行文字末格（ROW 30／END 47）→ 桩 `code` 5 行（含 `--open-url vscode://…pkg%2Fa.go`）；S1a 点 col 67 → 桩 5→5；S1c hover 文字末格 → 行字节以 `\x1b[48;5;236m` 起、逐格计数 46＝END-1，hover col 67 → 行字节回到基线、`48;5;236` 计数 0；S2a 点提示行（END 40）右侧 → `click to show`=1；S2b 点文字末格 → `click to hide`=1、桩不变；S3b 点 marker（`▤ tiny.png 1×1 70 bytes click to open`，END 42）文字末格 → 桩 `open …/tiny.png`；S3a 点右侧 → 桩不变；S4 长 `read_file` 卡正文行（`1| line01`，END 14）col 100 hover → `48;5;236` 出现（整行 hover 未变），col 100 点击 → 展开（`line30` 可见）再点 → 折回；S6b 点 fanout 任务行（`└ ✓ report done`，END 18）文字末格 → 页脚 `esc to return`=1，Esc 后 0，S6a 点右侧 → 0 且桩不变，hover 该行文字 → 17 格＝文字宽、文字外 0；S5 两次 Ctrl+C → `EXIT=0`，`tui.err` 空。隔离：桩日志 6 行全为桩所记、真实 `~/.forebrain` mtime 仍 11/21、无 VS Code 进程、私有 tmux server 随 `stop` 消失、默认 socket 的 `fb-dup` 未受影响。
