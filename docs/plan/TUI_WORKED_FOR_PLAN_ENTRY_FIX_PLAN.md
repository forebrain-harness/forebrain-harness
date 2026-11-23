# TUI：Worked for 行的 ☑N/M 也能打开右侧待办抽屉（bug 修复计划）

- 状态：**已实施并真机验收通过（2026-11-10）**
- 计划落盘：本文写在会话计划目录；**批准后第一步**把它复制为仓库惯例路径 `docs/plan/TUI_WORKED_FOR_PLAN_ENTRY_FIX_PLAN.md`（该目录已 gitignore，不入库）。
- 基线：`git rev-parse --short HEAD` = **4b3fa769**（2026-11-09，工作区 dev）。
- 工作区现状（规划时）：其它并行会话的未提交 WIP 只有 `pkg/process/peer_inbound.go`、`pkg/process/peer_inbound_test.go`、`pkg/tui/run_test.go`；**本计划 in-scope 的 4 个文件当前干净**（`git status --short -- pkg/tui/plan_drawer.go pkg/tui/reducer.go pkg/tui/render.go pkg/tui/plan_drawer_test.go` 输出为空）。不是我们的改动，绝不 revert/stash/commit。

---

## 1. 目标（Why this matters）

owner 报告：TUI 里只有 **Working 行**（运行中的瞬时状态行）的 `☑N/M`（checkbox 图标 + 完成数/总数）能点开右侧待办抽屉；**Worked for 行**（run 结束后写在转录里的收尾状态行）的同一个 token 点了没反应。

要求：两处 token 都能打开右侧抽屉。这正是既有抽屉合同里被明确延后的一项——原计划
`~/.forebrain/workspace/plans/-Users-doudou-workspace-unionj-cloud-forebrain-harness/cli-d27a3ff4-65f8-4bfa-b8af-72c3f44f92f3/tui-plan-progress-drawer.md`
§138–151 写死「run 结束后的历史状态帧（不是入口）……若产品后来要求 run 之外的常驻入口，那是一次单独设计」。owner 本次的要求就是那次产品决策：**把 Worked for 行补成抽屉入口**。

本轮只做这一件事，不扩到别的入口（历史 plan 卡、回放行等，见 §3.4）。

---

## 2. 现状与根因（已复现，不是读代码推测）

### 2.1 现场表现

- Working 行 token（composer 区、运行中）→ 点击开/合抽屉：既有测试
  `TestPlanDrawerProgressTokenIsTheEntry`（`pkg/tui/plan_drawer_test.go:816`）覆盖并通过。
- Worked for 行 token（转录区、run 收尾）→ 点击无反应、hover 无高亮、按下还会开始文本选择。

### 2.2 复现（修复前必须重跑一次并看到相同失败）

把下面的临时测试文件放进 `pkg/tui/zz_repro_worked_token_test.go`（**只用于取证，取证后删除**；正式回归测试在 §5 Step 5 以正式名字落地）：

```go
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
)

func workedTokenCell(t *testing.T, r *Renderer) (row, col int) {
	t.Helper()
	for i := 0; i < r.vpBodyHeight && i < len(r.vpPainted); i++ {
		plain := sgrPattern.ReplaceAllString(r.vpPainted[i], "")
		if idx := strings.Index(plain, "☑3/7"); idx >= 0 {
			return i, runewidth.StringWidth(plain[:idx])
		}
	}
	t.Fatalf("the Worked for row did not paint a ☑3/7 token; body rows: %#v", plainPlanDrawerRows(r.vpPainted))
	return -1, -1
}

func TestReproWorkedForTokenClick(t *testing.T) {
	r := newPlanDrawerScreen(t, 120, 40)
	fillPlanDrawerTranscript(r)
	r.RenderFrame(Frame{Kind: FrameStatus, Title: "Worked for 1m 02s · ☑3/7 · 修改 pkg/tui 的鼠标交互 · 12:34", Final: true})
	row, col := workedTokenCell(t, r)
	clickAt(r, col+1, row)
	if !r.planDrawer.open || !r.vpDrawerOpen {
		t.Fatalf("BUG: clicking the Worked for row's ☑3/7 (row %d col %d) did not open the todo drawer", row, col)
	}
}

func TestReproWorkedForTokenHoverAndPress(t *testing.T) {
	r, _ := newPlanDrawerScreenWithOut(t, 120, 40)
	fillPlanDrawerTranscript(r)
	r.RenderFrame(Frame{Kind: FrameStatus, Title: "Worked for 1m 02s · ☑3/7 · 修改 pkg/tui 的鼠标交互 · 12:34", Final: true})
	row, col := workedTokenCell(t, r)
	before := r.vpPainted[row]
	r.HandleMouse(inputEvent{kind: inputEventMouseMove, mouseCol: col + 1, mouseRow: row, at: time.Now()})
	if r.vpControlHover != hoverTargetProgress || r.vpPainted[row] == before {
		t.Fatalf("BUG: the Worked for row's ☑3/7 is not a drawer control (hover target %d)", r.vpControlHover)
	}
	r.ViewportSelectStart(col+1, row)
	if r.selectStartRow >= 0 {
		t.Fatalf("BUG: pressing the Worked for token started a text selection at row %d instead of arming the drawer control", r.selectStartRow)
	}
}
```

命令（本仓库硬性要求：CGO + `-tags fts5` + 临时 home，见 FOREBRAIN.md House rules）：

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui \
  -run 'TestReproWorkedForToken' -count=1 -v
```

规划时实跑输出（2026-11-09，HEAD 4b3fa769）：

```
=== RUN   TestReproWorkedForTokenClick
    zz_repro_worked_token_test.go:37: Worked for token drawn at screen row 31 col 22: "─ Worked for 1m 02s · ☑3/7 · 修改 pkg/tui 的鼠标交互 · 12:34 ───────…"
    zz_repro_worked_token_test.go:40: after click: drawer open=false paintedOpen=false
    --- FAIL: TestReproWorkedForTokenClick (0.00s)
=== RUN   TestReproWorkedForTokenHoverAndPress
    hover target after moving onto the token: 0 (hoverTargetProgress=1)
    row bytes changed by hover: false
    press on the token: selectStartRow=31 (>=0 means it started a text selection)
    release: drawer open=false
    --- FAIL: TestReproWorkedForTokenHoverAndPress (0.00s)
```

> 规划时 `pkg/process/peer_inbound.go` 正被并行会话改到编译不过，上面这次运行用了 `-overlay` 把该文件换回 HEAD 版本编译（不写他人文件）：
> ```bash
> mkdir -p /tmp/fb-repro && git show HEAD:pkg/process/peer_inbound.go > /tmp/fb-repro/peer_inbound_head.go
> printf '%s' '{"Replace":{"/Users/doudou/workspace/unionj-cloud/forebrain-harness/pkg/process/peer_inbound.go":"/tmp/fb-repro/peer_inbound_head.go"}}' > /tmp/fb-repro/overlay.json
> FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -overlay=/tmp/fb-repro/overlay.json -tags fts5 ./pkg/tui -run 'TestReproWorkedForToken' -count=1 -v
> ```
> 正式收尾的测试**必须不带 overlay**（对方落地后重跑）。

### 2.3 因果链（每环 file:line + 现场证据）

1. **Worked for 行确有 token。** run 结束时 `pkg/tui/run.go:415`
   ```go
   renderer.RenderFrame(Frame{Kind: FrameStatus, Title: ev.WorkedStatus, Final: true})
   ```
   `ev.WorkedStatus` 来自 `Reducer.formatWorkedStatusTitle`（`pkg/tui/reducer.go:1900–1918`），它把 `formatWorkingCounters(snapshot)` 拼进标题；`formatWorkingCounters`（`pkg/tui/run.go:4812–4825`）产出 `"☑" + done + "/" + total`。复现输出里的 `─ Worked for 1m 02s · ☑3/7 · …` 就是它在转录里画出的样子（`renderStatus` → `workedStatusLine`，`pkg/tui/render.go:5539`、`render.go:6158`）。

2. **抽屉热区只在 composer 的 Working 行注册。** 全仓只有一处写 `vpProgressSpan`：`pkg/tui/reducer.go:5085`
   ```go
   r.vpProgressSpan = composer.progressScreenSpan(bodyHeight)
   ```
   它的来源是 composer 行：`render.go:4955–4970 composerStatusLine` → `composerBlock.progressScreenSpan`（`reducer.go:3656–3670`）。转录行（`renderViewport` 产出的 `vr.lines`）从不参与这段注册——复现第二例里 `vpControlHover=0`（`hoverTargetNone`）就是证据。

3. **命中测试只认这一个 span。** `shownScreen`（`pkg/tui/reducer.go:6740–6760`）只有一个 `progress screenSpan`；三处判定都只查它：
   - 点击：`planDrawerConsumeClickLocked`（`plan_drawer.go:850–868`）`if s.progress.contains(col,row) { toggle }`；
   - 按下：`planDrawerPressLocked`（`plan_drawer.go:874–879`）；
   - hover：`planDrawerHoverTarget`（`plan_drawer.go:882–890`）。

4. **于是点击落到转录的普通命中路径。** `viewportClickToggleAt`（`reducer.go:6306–6330`）→ `transcriptClickLocked`（`reducer.go:6340–6354`）→ 图片标记 / 打开子代理视图 / `toggleAtScreenRow`；FrameStatus 块不可折叠（`b.collapsible == false`，`toggleAtScreenRow` 直接返回 false），所以什么也不发生。按下时则走 `viewportPressLocked`（`reducer.go:7154–7229`，复现里 `selectStartRow=31`），开始文本选择。

**根因一句话**：`☑N/M` 这个“抽屉入口”的热区是在 composer 状态行构建时注册的（`reducer.go:5085`），而 Worked for 行是转录块（`FrameStatus`），从未注册热区，命中测试自然对它无效。

### 2.4 竞争解释与排除

| 候选解释 | 排除证据 |
|---|---|
| 抽屉打不开是因为没有 plan frame | 同一 fixture 里 `latestPlanFrameLocked()` 能找到 `fillPlanDrawerTranscript` 放的 plan frame；且 composer token 在同一屏幕可开（既有测试）；复现里点击前 `planDrawer.open=false`、点击后仍 false，而直接 `openPlanDrawerLocked` 可以打开（`planDrawerOpenForTest` 在其它测试里通过）。 |
| 点击坐标算错（CJK/宽字符列偏移） | 复现里用 `drawnCellAt` 同源的列计算（`sgrPattern` + `runewidth.StringWidth`），点的是 `☑3/7` 正中间（col+1）；同一套 `clickAt` 在 composer token 测试里有效。 |
| 点击被抽屉以外的东西吞了 | `planDrawerConsumeClickLocked` 第一分支就不满足（span 是 `row:-1`），随后 `transcriptClickLocked` 对不可折叠块返回 false；按下证据 `selectStartRow=31` 说明事件确实到达转录。 |
| 是渲染问题、token 其实没画出来 | 复现输出打印了整行 `─ Worked for 1m 02s · ☑3/7 · …`，token 完整可见（宽度 4 个 cell）。 |

---

## 3. 修复设计

### 3.1 原则：复用既有热区机制，不造第二套

抽屉的全套交互（点击 toggle、按下 arm、hover 局部高亮、“只见即所点”）已经在 composer token 上实现。修法是把**同一套注册**扩展到转录里“run 收尾行”的 token：

- 在 `paintViewportLocked` 画完转录行的**最终字节**（`canvasRow` 之后）时，识别出 Worked for 收尾行（`activeVMLocked()` 里只有当前视图自己的收尾行），用已有的 `planProgressToken`（`plan_drawer.go:897`）量出 token 的显示列 span，记进 renderer 的 `vpHistoryProgress []screenSpan`。
- `liveScreenLocked` 把它复制进 `shownScreen.historyProgress`；点击/按下/hover 三处判定复用同一个 `historyProgressSpan` 小助手。
- hover 高亮沿用 `highlightCells`（`plan_drawer.go:471`，与 composer token 同一实现、同一 `hoverBackgroundColor`）。
- 因为热区是在“最终绘制字节”上量的，天然满足既有合同：截断的 token（宽度切掉）**不注册**。

**只认 run 收尾行，不认任意 `☑` 文本**：命中行必须属于一个 `Kind == FrameStatus`、标题以 `Worked for ` 开头的块。祖父级风险（工具输出/模型正文里恰好出现 `☑9/9`）因此保持惰性。

> 归属不需要额外判断：`retainFrameLocked`（`reducer.go:5841–5853`）把带 `AgentID` 的帧只放进该代理自己的 view model，`subagentWorkedFrames` 的收尾行因此只出现在子代理视图里（父视图根本不显示），而子代理视图的 `activeView` 就是这个 AgentID。不要再加 `frame.AgentID == activeView` 这类不可达分支。

**多行都算入口**：屏幕上可见的每个“本视图 Worked for 行”的完整 token 都是入口（抽屉内容永远是 `latestPlanFrameLocked` 的最新 plan，不是某行当时的历史快照）。这与“看见的 token 就能点”一致；只让最后一行可点会留下“长得一样却点不动”的同类缺陷。

### 3.2 具体改动（4 个文件）

#### A. `pkg/tui/render.go`（只动 `type Renderer` 字段，已与并行会话对齐：对方只改 `ComposerPendingInputPreview` 结构与 `composerPreviewHasEditableQueuedInput`，不碰 483 行区域）

在 `vpProgressSpan`（`render.go:483–485`）之后新增两个字段；并扩写 `vpControlHover` 的注释（`render.go:453–459`，现在只说 composer token 与 close 控件）：

```go
	// vpProgressSpan is the screen cell the last paint drew the composer's
	// ☑N/M plan-progress token on, or a span on no row.
	vpProgressSpan screenSpan
	// vpHistoryProgress are the ☑N/M tokens the last paint drew on the
	// transcript rows that closed a run ("Worked for …") of the view on
	// screen, in screen cells. They are the drawer's second entry: a run's
	// closing line keeps its checklist reachable after the live Working line
	// is gone. Only the token of such a row is an entry; the rest of the row
	// still folds, selects and opens a subagent view exactly as before.
	vpHistoryProgress []screenSpan
	// vpHoverProgress names the transcript token the pointer is on, as the
	// paint that showed it named it. A repaint resolves the highlight against
	// this name, not the screen index, so a streamed delta that scrolls the
	// line to another row cannot move the highlight onto a row the pointer
	// never touched.
	vpHoverProgress planDrawerHoverToken
```

#### B. `pkg/tui/plan_drawer.go`

1. 文件头注释（`plan_drawer.go:3–17`）：把“the composer's ☑N/M plan-progress token opens”改成“the composer's Working line and a run's closing Worked for line”两处入口。
2. 新增类型与两个助手：

```go
// planDrawerHoverToken names one transcript ☑N/M token across repaints: the
// view it belongs to and its absolute transcript row. A "Worked for" line is a
// single row, so the row is the token.
type planDrawerHoverToken struct {
	found bool
	view  string
	row   int
}

func (t planDrawerHoverToken) on(view string, absRow int) bool {
	return t.found && t.view == view && t.row == absRow
}

// workedForTokenSpan reports the cells a run-closing "Worked for" row drew its
// ☑N/M token on. It is the drawer's transcript entry, and only the token is:
// the row's other cells keep their ordinary click meaning, and a token the
// row's width cut in half was never on screen.
func workedForTokenSpan(vr viewportRender, vm *viewModel, row int, painted string) (screenSpan, bool) {
	if vm == nil {
		return screenSpan{row: -1}, false
	}
	blockID, _ := vr.blockAtRow(row)
	if blockID < 0 {
		return screenSpan{row: -1}, false
	}
	b := vm.blockByID(blockID)
	if b == nil || b.frame.Kind != FrameStatus ||
		!strings.HasPrefix(strings.TrimSpace(b.frame.Title), workedForPrefix) {
		return screenSpan{row: -1}, false
	}
	start, end, ok := planProgressToken(stripAnsi(painted))
	if !ok {
		return screenSpan{row: -1}, false
	}
	return screenSpan{row: row, startCol: start, endCol: end}, true
}

// historyProgressSpan returns the run-closing ☑N/M token drawn at this cell of
// s, in screen cells. ok is false on every other cell, including the composer's
// own token (s.progress) and the drawer's close control (s.drawerClose).
func (s shownScreen) historyProgressSpan(col, row int) (screenSpan, bool) {
	for _, span := range s.historyProgress {
		if span.contains(col, row) {
			return span, true
		}
	}
	return screenSpan{row: -1}, false
}
```

3. 点击/按下/hover 三处补上历史 token：
   - `planDrawerConsumeClickLocked`：`if _, ok := s.historyProgressSpan(col, row); ok || s.progress.contains(col, row) {` → `return true, r.togglePlanDrawerLocked()`。
   - `planDrawerPressLocked`：`if _, ok := s.historyProgressSpan(col, row); ok { return true }`（与 composer token/close 控件同义：按下 arm 抽屉控件，绝不开始选中）。
   - `planDrawerHoverTarget`：`if _, ok := s.historyProgressSpan(col, row); ok { return hoverTargetProgress }`。
   - 相应注释把“the progress token”改成“either progress token（composer Working 行 / 转录的 Worked for 行）”。
4. `repaintAfterDrawerToggleLocked`（`plan_drawer.go:615–622`）在清 `vpControlHover/vpHoverRow` 的同时清 `r.vpHoverProgress = planDrawerHoverToken{}`（三处字段描述同一份 hover 状态）。

#### C. `pkg/tui/reducer.go`

1. `workedForLabel`（`reducer.go:1942–1945`）提取前缀常量，供渲染与命中判定共用同一拼写：

```go
// workedForPrefix opens the line that closes a run — live, replayed and in a
// subagent's own view. The plan drawer's transcript entry is recognised by it.
const workedForPrefix = "Worked for "

func workedForLabel(elapsed time.Duration) string {
	return workedForPrefix + formatElapsedDuration(elapsed)
}
```

2. `shownScreen`（`reducer.go:6740–6760`）新增字段（注释说明它和 `progress` 同属抽屉控件几何）：

```go
	progress        screenSpan
	// historyProgress are the ☑N/M tokens the paint drew on the run-closing
	// rows of the view it showed, in the same screen cells; a click, a press
	// and a hover resolve against them exactly like the composer's token.
	historyProgress []screenSpan
```

3. `liveScreenLocked`（`reducer.go:6916–6941`）加 `historyProgress: r.vpHistoryProgress,`。

4. `paintViewportLocked` 的转录行循环（`reducer.go:4987–5033`）：

```go
	var historyProgress []screenSpan
	for row := 0; row < bodyHeight; row++ {
		...
		line := canvasRow(vr.lines[row], rowWidth)
		// A "Worked for" row's ☑N/M is the drawer's transcript entry. Measured
		// on the row as it will be drawn (after the width cut), so the hot zone
		// is exactly the token the user can see; a row with no ☑ pays one
		// substring check and nothing else.
		token := screenSpan{row: -1}
		if strings.Contains(line, "☑") {
			if span, ok := workedForTokenSpan(vr, activeVM, row, line); ok {
				token = span
				historyProgress = append(historyProgress, span)
			}
		}
		...（原有 selection / 整行 hover 分支不动）...
		if token.row >= 0 && r.vpControlHover == hoverTargetProgress &&
			r.vpHoverProgress.on(r.activeView, vr.firstRow+row) {
			// Only the token's own cells take the hover background, the same
			// layer the composer token uses.
			line = highlightCells(line, token.startCol, token.endCol, hoverBgSeq)
		}
		if drawerOpen {
			line = planDrawerSplitRow(...)
		}
		rows[row] = line
	}
	r.vpHistoryProgress = historyProgress
```

（`vpHistoryProgress` 每次 paint 新建切片，和 `viewportRender.spans` 一样，避免 `shownScreen` 里保存的切片被下一次 paint 原地改写。）

5. `viewportHoverAt`（`reducer.go:6370–6393`）：

```go
	s := r.screenAtLocked(at)
	target := planDrawerHoverTarget(s, col, row)
	hoverRow := -1
	if target == hoverTargetNone && !(s.drawerOpen && col >= s.drawerLeft && row < s.bodyHeight) {
		if row >= 0 && row < r.vpHeight {
			hoverRow = row
		}
	}
	hoverToken := planDrawerHoverToken{}
	if span, ok := s.historyProgressSpan(col, row); ok && s.view == r.activeView {
		hoverToken = planDrawerHoverToken{found: true, view: s.view, row: s.render.firstRow + span.row}
	}
	if target == r.vpControlHover && hoverRow == r.vpHoverRow && hoverToken == r.vpHoverProgress {
		return
	}
	r.vpControlHover = target
	r.vpHoverRow = hoverRow
	r.vpHoverProgress = hoverToken
	r.paintViewportLocked()
```

> 早退条件必须带上 `hoverToken`：从一条 Worked for 行的 token 移到另一条时 `target` 都是 `hoverTargetProgress`、`hoverRow` 都是 -1，只比前两者会把高亮留在旧行。同一 token 内换 cell 不重绘（`hoverToken` 不变），保持既有“同一 cell 重复上报零字节输出”的性能合同。

#### D. `pkg/tui/plan_drawer_test.go`（新增测试，不删改既有用例）

1. `TestPlanDrawerWorkedForTokenIsTheEntry`：`fillPlanDrawerTranscript` + `RenderFrame(Frame{Kind: FrameStatus, Title: "Worked for 1m 02s · ☑3/7 · 修改 pkg/tui 的鼠标交互 · 12:34", Final: true})`；断言
   - `len(r.vpHistoryProgress) == 1`，span 在 body 内（`< r.vpBodyHeight`），`drawnCellAt(r.vpPainted[row], startCol) == "☑"`，宽 4，`endCol` 后一格不是 token 内容；
   - `ViewportSelectStart` 落在 token 上：`selectStartRow < 0`（不开始选中）、`vpPress.onPlanDrawer == true`；
   - 同一次 press 的 release（`ViewportSelectEnd`）打开抽屉；再点同一 token 关闭；抽屉开着时 token 仍有热区（`vpHistoryProgress` 仍含它）。
2. `TestPlanDrawerWorkedForRowHoverHighlightsOnlyTheToken`：两条 Worked for 行（`☑1/7`、`☑3/7`）+ plan frame；移到第一条 token 上，该行出现 `48;5;236` 且只包住 `☑1/7`，第二条行字节不变；同一 cell 连发 20 次 move，输出 0 字节；移到第二条 token，高亮随之移动（第一条恢复）；移开，两行都恢复。
3. `TestPlanDrawerPlanTokenOutsideRunClosingRowsIsInert`：
   - assistant 正文里带 `☑9/9` 的行、`FrameTool` 内容里带 `☑9/9` 的行：`len(r.vpHistoryProgress) == 0`，点击 token 格子抽屉不打开；
   - 子代理视图：`FrameStatus{Title:"Worked for 2s · ☑1/2", AgentID:"sub-1"}` 只进 sub-1 的 view model（`reducer.go:5841`），`SetActiveView("sub-1")` 后该视图有自己的 plan frame + 这条收尾行：注册热区，点击打开的是该视图的抽屉（列表含子代理事项）。
4. `TestPlanDrawerWorkedForTruncatedTokenHasNoHotZone`：40 列宽 + 长标题把 token 切掉 → `len(r.vpHistoryProgress)==0`；同宽度短标题（`Worked for 3s · ☑3/7`）→ 有热区。

### 3.3 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 性质 |
|---|---|---|---|
| 点 composer Working 行 `☑N/M` | 开/合抽屉 | 不变 | 回归 |
| 点转录 Worked for 行 `☑N/M` | 无反应（按下还会开始选中） | 开/合抽屉 | **刻意修订**（owner 要求） |
| hover Worked for 行 `☑N/M` | 无高亮 | 只在 token cell 上 `48;5;236`，无下划线 | **刻意修订** |
| 在 Worked for token 上按下 | 开始文本选择 | arm 抽屉控件，不选择 | **刻意修订**（与 composer token 同义） |
| 点 Worked for 行的其他格子 | 折叠/选择/打开子代理视图 | 不变 | 回归 |
| 正文/工具输出里的 `☑N/M` 字面量 | 惰性 | 惰性（新增守卫） | 回归 |
| 子代理视图自己的 Worked for 行 token（该行只存在于子代理视图） | 无反应 | 打开该视图的抽屉 | **刻意修订**（三面一致） |
| 宽度截断的 token | 无入口 | 无入口 | 回归（既有合同） |
| 抽屉开着时点 Worked for token | n/a | 关闭抽屉 | **刻意修订** |
| 回放（/resume）的 Worked for 行 | 行上没有 token | 不变（没有 token → 没有入口，见 §3.4） | 已知限制 |

### 3.4 刻意不做（Scope 纪律）

- **不改** `formatWorkedStatusTitle` / `subagentWorkedFrames` / `workedStatusLine` / `planText` / `renderFrameLinesWithAgents`：行文字与渲染保持原样，只在绘制后量 token。
- **不给回放行补 token**：`turn.RunWorkedLine`（`pkg/turn/compaction.go:183–192`）不存 plan 计数，`replayWorkedFrame`（`pkg/tui/commands.go:735–746`）因此画不出 `☑N/M`；给回放补入口需要改存储事件，属另一个功能。列为遗留（§8）。
- **不给历史 plan 卡新增入口**（原计划同样明确不做）。
- **不动 web/gateway surface**：抽屉是 TUI 专有。
- **不把 `render.go:5554`、`reducer.go:5814–5816` 的 `"Worked for "` 字面量一并收敛**：与本修复无关，避免扩大 diff（新代码用 `workedForPrefix`，不改既有拼写）。

---

## 4. Scope

**In scope（只允许改这 4 个文件）**

- `pkg/tui/plan_drawer.go`
- `pkg/tui/reducer.go`
- `pkg/tui/render.go`（仅 `type Renderer` 两个字段 + 一处字段注释）
- `pkg/tui/plan_drawer_test.go`
- `docs/plan/TUI_WORKED_FOR_PLAN_ENTRY_FIX_PLAN.md`（本计划，gitignore，不入库）

**Out of scope（明确不碰）**

- `pkg/turn/*`、`pkg/tui/commands.go`、`pkg/tui/run.go`、`pkg/tui/chat_session.go`、`pkg/gateway/*`、`pkg/process/*`
- 抽屉布局/宽度/滚动/关闭控件、`vpProgressSpan`（composer）既有逻辑、折叠命中语义
- 其它并行会话的 WIP 文件（`pkg/process/peer_inbound*.go`、`pkg/tui/run_test.go` 等）
- 任何 git commit

---

## 5. Steps（按序执行，每步以检查收尾）

**Step 0 — 基线与漂移检查（内容不符即 STOP，行号漂移不算）**

```bash
git rev-parse --short HEAD                      # 期望 4b3fa769（或记录实际基线）
git status --short -- pkg/tui/plan_drawer.go pkg/tui/reducer.go pkg/tui/render.go pkg/tui/plan_drawer_test.go
```

期望输出为空（我们的 in-scope 文件干净）。若非空：逐段 `git diff` 阅读，确认是他人 WIP 且不覆盖 §2.3 引用的代码；引用代码变了 → STOP 报告；仅行号漂移 → 继续。

**Step 1 — 先落地复现测试并看它失败**

把 §2.2 的临时测试放到 `pkg/tui/zz_repro_worked_token_test.go`，跑：

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestReproWorkedForToken' -count=1 -v
```

期望：两条用例 FAIL，失败信息与 §2.2 一致（drawer open=false / hover target 0 / selectStartRow≥0）。若通过 → 根因不成立，STOP。
若编译失败且原因在他人 WIP（`pkg/process/peer_inbound.go` 等）：用 §2.2 的 `-overlay` 技巧，不修改他人文件。

**Step 2 — 实现 §3.2 A（render.go 字段）**

改完立即：

```bash
CGO_ENABLED=1 go build ./pkg/tui
```

期望 exit 0（若因他人 WIP 的依赖包失败，先确认失败点不在我们改的文件）。

**Step 3 — 实现 §3.2 B/C（plan_drawer.go、reducer.go）**

```bash
CGO_ENABLED=1 go build ./... && go vet ./pkg/tui
```

期望 exit 0、无 vet 输出。**注意**：`pkg/process` 若仍被并行会话改到编译不过，`go build ./...` 会失败；用 `go build ./pkg/tui` + `go vet ./pkg/tui` 验证本包，并在收尾时（对方落地后）重跑全量。

**Step 4 — 用临时复现测试确认修复**

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestReproWorkedForToken' -count=1 -v
```

期望：两条 PASS（点击开抽屉；hover 目标为 `hoverTargetProgress`、行字节改变、按下不再开始选中）。然后**删除** `pkg/tui/zz_repro_worked_token_test.go`。

**Step 5 — 落地正式回归测试（§3.2 D）**

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestPlanDrawer' -count=1 -v
```

期望：全部 PASS（含既有 `TestPlanDrawerProgressTokenIsTheEntry`、`TestPlanDrawerHoverHighlightsOnlyTheToken`、`TestPlanDrawerSplitSurvivesAnOverlay` 等）。

**Step 6 — 包级回归 + vet**

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1
CGO_ENABLED=1 go build ./... ; go vet ./...
```

期望：`pkg/tui` ok；build/vet 在他人 WIP 落地后 clean。（`pkg/tui` 包测试可能较长，属正常。）

**Step 7 — tmux 真机验收（fake provider，必做）**

```bash
D=.claude/skills/run-forebrain/driver.sh
$D reset
$D build
$D start tool '{"name":"session_todo","arguments":{"items":[
  {"id":"1","content":"归档计划","status":"completed"},
  {"id":"2","content":"新增回归测试","status":"completed"},
  {"id":"3","content":"修改 pkg/tui 的鼠标交互并验证 hover","status":"in_progress","title":"Writing tests"}]}}'
$D submit '把待办列出来'
$D wait 'Worked for' 25
$D screen | grep '☑'          # 期望 Worked for 行带 ☑2/3，标题保持短（抽屉开在 120 列时左栏 80 列，token 不能被截断）
$D col '☑2/3'                 # 打印 1-based row=… col=…（token 首字符）
$D click <col> <row>          # 点 Worked for 行的 token
$D screen                     # 期望右侧出现 Updated Plan 抽屉、左侧转录变窄重排、列表含“修改 pkg/tui 的鼠标交互并验证 hover”
$D col '☑2/3'                 # 抽屉开着时重新取坐标（左栏宽度变了，token 可能移位）
$D click <col> <row>          # 再点一次
$D screen                     # 期望抽屉消失、转录恢复全宽
$D stop
```

验收记录（场景→证据）回填 §9。失败时先 `$D provider-log` 确认 `session_todo` 调用真的发生；`no such module: fts5` 说明 `$D build` 没带 tag（driver 已带），不是产品 bug。第二条“再点关闭”若在 80 列左栏里 token 已被截断（屏幕上不再完整），说明验收用的标题太长：换短标题重跑，不要据此改产品。

**Step 8 — 收口**

- 删除临时文件与 `/tmp/fb-repro`；`git status --short` 应只剩本计划 4 个文件的改动 + 他人既有 WIP，无 commit。
- `git diff` 逐文件核对与 §3.2 一致，未出现 §4 out-of-scope 文件。
- 把本计划复制到 `docs/plan/TUI_WORKED_FOR_PLAN_ENTRY_FIX_PLAN.md`，状态改“已实施并真机验收通过（日期）”，补 §9。
- 向 owner 报告：根因链、最小改动清单、验证证据、遗留（回放无 token）。

---

## 6. Done criteria（机器可查）

- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestPlanDrawer' -count=1` → `ok`
- [ ] 新增用例覆盖：Worked for token 点击开/合、按下不选中、hover 只高亮 token、两条 Worked for 行之间高亮移动且重复上报零字节、正文/工具输出 `☑` 惰性、父视图子代理收尾行惰性、子代理视图自己的收尾行可用、截断 token 无热区
- [ ] 修复前 §2.2 复现失败、修复后通过（证据在 §9）
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`
- [ ] `CGO_ENABLED=1 go build ./...`、`go vet ./...` exit 0
- [ ] `git status --short -- pkg/tui/plan_drawer.go pkg/tui/reducer.go pkg/tui/render.go pkg/tui/plan_drawer_test.go` 只显示这 4 个文件；`git diff` 未触及 §4 out-of-scope 文件；无 commit
- [ ] `git diff -- pkg/tui | grep -c 'formatWorkedStatusTitle\|subagentWorkedFrames\|workedStatusLine\|planText\|renderFrameLinesWithAgents'` → 0（这些函数体不出现；`workedForLabel` 的常量提取除外）
- [ ] tmux 真机：点 Worked for 行 `☑2/3` → 抽屉打开；再点 → 关闭；屏幕上 token 与抽屉同在

---

## 7. STOP conditions

1. **复现不成立**：Step 1 的测试在未改代码时通过，或失败原因与 §2.3 的因果链不符（如 `vpProgressSpan` 已覆盖转录行）→ 停止，带证据报告，不得改代码。
2. **in-scope 文件的内容漂移**：§2.3 引用的代码（函数体/字段/判定）被并行会话改动，导致方案落点不成立 → 停止并重新定位（行号漂移本身不算）。
3. **需要改 out-of-scope 文件**（`pkg/turn`、网关、web、`pkg/tui/commands.go`、`pkg/tui/run.go` 等）才能实现 → 停止，回到计划审批。
4. **`workedForPrefix` 之外的判定需要改行文字**（`formatWorkedStatusTitle`/`subagentWorkedFrames`/`workedStatusLine` 必须变化）→ 说明方案选错，停止。
5. **正文/工具输出里的 `☑` 变成可点**（测试 3 失败）→ 停止，不准放宽容错（例如“只要行里有 ☑ 就算”）。
6. **既有测试出现与“Worked for 行 token 惰性”相关的断言冲突** → 停止并报告合同冲突（规划时全仓 grep 未发现此类断言）。
7. **`pkg/tui` 因他人 WIP 编译不过**：只允许用 `-overlay` 做本地验证，绝不改/回退他人文件；若对方长时间未落地，把未完成的全量验证写进报告的“遗留”。
8. **render.go 的 `type Renderer` 区域被他人改动**（与规划时的对齐失效）→ 先读对方改动再追加字段；不得覆盖。
9. 真机验收点击后抽屉不出现 → 先查 token 是否完整绘制、`vpHistoryProgress` 是否为空（截断/视图），带屏幕证据回到计划，不得改点其它隐藏逻辑“碰运气”。

---

## 8. Maintenance notes

- **抽屉的入口合同更新**：原计划（`tui-plan-progress-drawer.md` §138–151）规定“只有 composer 的 Working 行 token 是入口、Worked for 行不是”；本计划按 owner 2026-11-09 的要求把“本视图自己 run 收尾行的完整 token”加入合同。后续如果再加入口（历史 plan 卡、回放行），继续走“绘制后量最终字节 → 注册 span → 三处判定共用”的同一路径，不要另写坐标计算。
- **已知不一致（明确不在本期修）**：`/resume` 回放的 Worked for 行没有 `☑N/M`（`turn.RunWorkedLine` 不存 plan 计数），因此回放里 run 结束后没有抽屉入口。要让回放入口与实时一致，需要扩展存储的 run 收尾记录并在 `replayWorkedFrame` 重建计数——单独设计。
- **评审重点**：
  1. 热区确实落在“最终绘制字节”上（截断不注册、CJK 前列列宽用 `planProgressToken` 的显示列）；
  2. 判定只用 `FrameStatus` + `Worked for ` 前缀（没有把“任意含 ☑ 的行”当入口），也没有新增不可达的归属分支；
  3. hover 的早退比较包含 token 身份（多条 Worked for 行之间移动必须重绘）；
  4. `vpHistoryProgress` 每次 paint 新建切片（`shownScreen` ring 保存旧切片，不能原地复用）。
- **性能**：每行一次 `strings.Contains(line,"☑")`；只有含 ☑ 的行才 `stripAnsi`+`planProgressToken`+块查找。正常转录零额外分配（`historyProgress` 只在有 token 时 append）。hover 早退保证同 token 内移动不重绘。
- **可扩展点**：`workedForTokenSpan` 是唯一的“哪一行算收尾行”判定；将来若还有别的“进度状态行”（例如 goal 收尾）要入口，在这里收敛，不要在 paint 里复制判定。

---

## 9. 验收记录（实施后回填）

### 9.1 修复前复现（Step 1，HEAD 4b3fa769 + 计划内临时测试）

`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestReproWorkedForToken' -count=1 -v`

```
=== RUN   TestReproWorkedForTokenClick
    zz_repro_worked_token_test.go:30: BUG: clicking the Worked for row's ☑3/7 (row 31 col 22) did not open the todo drawer
--- FAIL: TestReproWorkedForTokenClick (0.00s)
=== RUN   TestReproWorkedForTokenHoverAndPress
    zz_repro_worked_token_test.go:42: BUG: the Worked for row's ☑3/7 is not a drawer control (hover target 0)
--- FAIL: TestReproWorkedForTokenHoverAndPress (0.00s)
```

与 §2.2 规划时一致：token 画在第 31 行第 22 列，点击不打开抽屉，hover 目标为 `hoverTargetNone`。

### 9.2 修复后（同一临时测试，改完代码立即跑，随后删除临时文件）

```
=== RUN   TestReproWorkedForTokenClick
--- PASS: TestReproWorkedForTokenClick (0.00s)
=== RUN   TestReproWorkedForTokenHoverAndPress
--- PASS: TestReproWorkedForTokenHoverAndPress (0.00s)
ok  github.com/forebrain-harness/forebrain-harness/pkg/tui  0.651s
```

### 9.3 正式测试

- `go test -tags fts5 ./pkg/tui -run 'TestPlanDrawer' -count=1` → `ok … 0.71s`
  （含新增 `TestPlanDrawerWorkedForTokenIsTheEntry`、`TestPlanDrawerWorkedForRowHoverHighlightsOnlyTheToken`、`TestPlanDrawerPlanTokenOutsideRunClosingRowsIsInert`、`TestPlanDrawerWorkedForTruncatedTokenHasNoHotZone`，以及既有 `TestPlanDrawerProgressTokenIsTheEntry` 等）
- `go test -tags fts5 ./pkg/tui -count=1` → `ok … 82.7s`（整包回归）
- `CGO_ENABLED=1 go build ./...` → exit 0；`go vet ./...` → exit 0

### 9.4 tmux 真机（fake provider，隔离 work/session/port，未触碰并行会话）

`start tool` 让 fake provider 调用 `session_todo`（3 项，2 完成），提交一轮后：

```
$D screen | grep -n '☑'
27:─ Worked for 1s · first token 0.0s · ☑2/3 · Writing tests · 09:14 ──────────────…
$D col '☑2/3' 27
row=27 col=38  line='─ Worked for 1s · first token 0.0s · ☑2/3 · Writing tests · 09:14 …'
$D click 39 27
# 抽屉打开：右栏标题「● Updated Plan ×」，列表 ✓ 归档计划 / ✓ 新增回归测试 / ▣ 修改 pkg/tui 的鼠标交互并验证 hover；左侧转录变窄重排
$D screen | sed -n '1,4p'
│ ● Updated Plan                     ×
│ ┄┄… Forebrain Harness ┄┄…
$D col '☑2/3' 28      # 抽屉开着时 token 移到第 28 行、列仍为 38
row=28 col=38  line='─ Worked for 1s · first token 0.0s · ☑2/3 · Writing tests · 09:14 ──────│'
$D click 39 28
# 抽屉关闭：无「×」、转录恢复全宽（同一行 120 列横线）
```

hover 真机证据（`tmux capture-pane -e`，鼠标移到 token）：

```
─ Worked for 1s · first token 0.0s · \u001b[48;5;236m☑2/3\u001b[49m · Writing tests · 09:14 ──…
```

移开指针后该行恢复无背景；高亮只包住 `☑2/3` 三格 token。

### 9.5 收尾范围

`git status --short` 中本计划改动只有 4 个文件：`pkg/tui/plan_drawer.go`、`pkg/tui/reducer.go`、`pkg/tui/render.go`、`pkg/tui/plan_drawer_test.go`；其余修改为并行会话既有 WIP，未触碰、未 commit。临时复现文件 `pkg/tui/zz_repro_worked_token_test.go` 已删除。

---

## 10. 追加范围（owner 2026-11-10：「遗留问题必须本期一并修复」）：回放 Worked for 行恢复 `☑N/M`

**现状与根因**：`turn.RunWorkedLine` 只从消息行读时钟（`RunWorkedMs` / `RunStartedAtMs` / `RunFinishedAtMs` / `RunFirstTokenMs`），不含清单；`replayWorkedFrame` 因而画不出 `☑N/M`，回放行没有 token、也就没有抽屉入口（§3.4/§8 的遗留）。但清单事实**已经在会话事件日志里**：每个 run 的 `RunEventPlanUpdated` 事件带 `Completed` / `Total` / `Items` / `Active`，`SurfaceSessionEvents` 全量返回；web 历史正是用同一事实给 worked 行补计数（`api_extra.go` → `lastPlanProgressOfRun`）。缺的只是 TUI 回放没读它。**不改存储、不加迁移。**

**设计**（与 live 同义：会话自己的清单，子代理清单不计）：

1. `pkg/turn/events.go`
   - 抽出 `planUpdateOfEvent(evt) (event.PlanUpdatedPayload, bool)`：只认 `RunEventPlanUpdated` 且 `AgentID == ""`（子代理清单是它自己的，live tracker 同样跳过）。
   - `LastPlanProgressOfRun` 改用它（该 run 自己的最后一条，语义不变）。
   - 新增 `PlanProgressAtRunEnds(events []event.RunEvent) map[string]event.PlanProgress`：按事件顺序累积会话清单状态，并在每个带 RunID 的事件上记下「该 run 结束时」的快照。没有自己清单更新的 run 继承前一次状态——正是 live 的 `tracker.SnapshotRun` 在 run 收尾时读到的全局计数。
2. `pkg/tui/commands.go`
   - `replayTimelineWithWorkers` 先算 `planAtRun := turn.PlanProgressAtRunEnds(events)`，`replayWorkedFrame(line, planAtRun[line.RunID])`。
   - `replayWorkedFrame(line, plan)` 在 first-token 段与完成时间之间插入 `formatWorkingCounters(Counters{PlanDone, PlanTotal, PlanActive})`（与 live 同一格式化，顺序同 live：时长 · first token · ☑N/M [· 在办标题] · 时刻）。
3. 回放行的 token 自动复用 §3.2 已落地的热区机制（`FrameStatus` + `Worked for ` 前缀 → `vpHistoryProgress`），因此回放里的 `☑N/M` 与实时一样可点开抽屉。
4. 测试
   - `pkg/turn`：折叠语义——累积、最后一个 run 继承、子代理更新不计、无 RunID / 非 plan 事件忽略。
   - `pkg/tui`：回放一条含 `session_todo` 行 + plan 更新的会话 → 回放标题含 `☑2/3 · Writing tests`，`vpHistoryProgress` 含该 token，点击打开抽屉；既有无事件回放的标题断言保持不变（零值不加段）。

**风险与边界**：旧会话里 plan 更新事件若没有 RunID（早于 run 绑定），`planAtRun` 查不到 → 该行不加段（与今天一致）；不改存储、不改 web、不新增迁移。

**实施与验收（2026-11-10，已完成）**

- 改动：`pkg/turn/events.go`（`planUpdateOfEvent` + `PlanProgressAtRunEnds`，`LastPlanProgressOfRun` 复用同一解码与跳过规则）、`pkg/tui/commands.go`（回放按 run 折叠清单；`replayWorkedFrame` 增加 `☑N/M [· 在办标题]` 段）、`pkg/turn/events_test.go`、`pkg/tui/commands_test.go`。
- 单测：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/tui -count=1` → 两包 `ok`（`pkg/turn` 3.0s、`pkg/tui` 83.0s），含新增 `TestPlanProgressAtRunEnds` 与 `TestResumedWorkedLineCarriesItsChecklistAndIsTheDrawerEntry`；既有 `TestResumeClosesEveryRunWithItsWorkedLine` / `TestReplayWorkedLinePerRun` / `TestReplayWorkedLineCarriesFirstToken` 等不带事件断言保持不变。
- 网关回归：`go test -tags fts5 ./pkg/gateway -run Plan` → `ok`（`LastPlanProgressOfRun` 语义未变）。
- `go build ./...`、`go vet ./...` → exit 0。
- 真机（fake provider，隔离 work/session/port）：
  1. 新会话提交一轮（`session_todo` 3 项 / 2 完成）：`─ Worked for 1s · first token 0.0s · ☑2/3 · Writing tests · 09:24 ─…`；
  2. `$D sid` 取 `cli-eed877af-…`，`stop` 后 `RESUME_ID=<sid> $D start …` 重开：回放行同样是 `Worked for 1s · first token 0.0s · ☑2/3 · Writing tests · 09:24`（修复前没有 `☑2/3`）；
  3. `$D col '☑2/3' 27` → `row=27 col=38`；`$D click 39 27` → 右侧「● Updated Plan ×」抽屉打开，列表 ✓ 归档计划 / ✓ 新增回归测试 / ▣ 修改 pkg/tui 的鼠标交互并验证 hover。
