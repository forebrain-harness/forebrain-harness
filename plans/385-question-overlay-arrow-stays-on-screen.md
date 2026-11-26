# Plan 385: user_interaction 面板的视窗随箭头滚动——箭头与选中行始终在屏

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. **Do NOT commit**（owner 常设规则 no-git-commits）：
> 改动留在当前工作树（分支 `dev`）由 owner 审阅。按 `.forebrain/skills/plan-gated-bugfix/SKILL.md` 的流程**直接在当前源码实施**，
> 不用 git worktree、不用快照。
>
> **Drift check (run first)**:
> `git rev-parse --short HEAD`（planned at `364a11a2`，branch `dev`）；
> `git status --porcelain -- pkg/tui`（planning 时为空）；`git diff --stat 364a11a2 -- pkg/tui/reducer.go pkg/tui/render.go pkg/tui/overlays.go pkg/tui/overlays_test.go pkg/tui/reducer_test.go`。
> 行号漂移不算；**「Current state」摘录与现码内容不一致才算漂移**（STOP）。

## Status

- **Priority**: P1（owner 截图报障，原话「视窗必须可以随着箭头移动自动滚动，始终显示箭头和箭头选中的行」）
- **Effort**: S
- **Risk**: LOW（只动 TUI 视口渲染与 user_interaction 面板；不碰数据、协议、公共 API；web 端不受影响）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `364a11a2`, 2026-11-25
- **Approved**: owner「可以实施」（2026-11-25；此前 plan-review `custom/qwen4-max-preview` 两轮均 approve，并独立实证 6 个新测试 5 红→6 绿、`pkg/tui` 全量 `ok`、真机 S1 28/28）
- **Issue**: —

## 1. Why this matters

TUI 里 `user_interaction` 工具的提问面板以「底部 composer 覆盖层」形式渲染。选项多到超过覆盖层可用高度时，覆盖层自己可滚动，但 **❯ 指针往下走时视窗不动**：指针走出可见区后，屏幕上既没有箭头也看不到选中行，用户只能盲按。owner 截图即此场景（7 个带说明的选项、多选、首项 recommended）。

修好后：指针走到哪，视窗就以最小滚动量跟到哪——箭头行、该选项的说明、以及最后一行的按键提示始终可见；往上走再回到表单顶部。

## 2. 根因（file:line 证据链，基线 `364a11a2`）

覆盖层的自动跟随只有两个入口（`pkg/tui/reducer.go:5297-5341`，`paintViewportLocked` 内）：

```go
// pkg/tui/reducer.go:5297-5322（现状）
		visible := overlayLines
		if len(overlayLines) > maxContentHeight {
			maxOverlayScroll := len(overlayLines) - maxContentHeight
			...
			focusStart, focusEnd := cursorRow, cursorRow+1
			if cursorRow < 0 && r.overlayFocusPending {
				focusStart, focusEnd = r.overlayFocusRow, r.overlayFocusEnd
			}
			if focusStart >= 0 && focusEnd > focusStart {
				if focusEnd > r.overlayScrollOffset+maxContentHeight {
					r.overlayScrollOffset = focusEnd - maxContentHeight
				}
				if focusStart < r.overlayScrollOffset {
					r.overlayScrollOffset = focusStart
				}
			}
```

即：**硬件光标行**（`cursorRow >= 0`）或 **focus 范围**（`overlayFocusPending` 为真）。`user_interaction` 面板两条都没有：

1. `pkg/tui/overlays.go:3575-3578`，`questionOverlay.finishCollector` 永远传 `cursorRow = -1`（面板用 ❯ 指针，不用硬件光标）且不传任何 focus：
   ```go
   func (o *questionOverlay) finishCollector(c *qlineCollector) {
   	if o.rs != nil && o.rs.inViewportOverlay() {
   		o.rs.setSoftWrappingOverlayComposer(c.lines, -1, 0)
   		return
   	}
   ```
2. `pkg/tui/overlays.go:4324-4329` → `pkg/tui/render.go:4655-4662`，`SetSoftWrappingOverlayComposer` 把 focus **硬编码为无**：
   ```go
   func (r *Renderer) SetSoftWrappingOverlayComposer(lines []string, cursorRow, cursorCol int) {
   	r.setOverlayComposer(lines, cursorRow, cursorCol, true, overlayKeepScroll, 0, 0)
   }
   ```
3. `pkg/tui/render.go:4680-4681`，`setOverlayComposer` 据此得出 `overlayFocusPending = false`：
   ```go
   	r.overlayFocusRow, r.overlayFocusEnd = focusStart, focusEnd
   	r.overlayFocusPending = focusStart >= 0 && focusEnd > focusStart
   ```

⇒ `overlayScrollOffset` 停在打开时的 0，而 `o.cursor` 一路走到可见窗下方。**同一仓里语义已存在、唯独这条路径没用它**：审批面板（approvalOverlay / plan review picker）走 `setOverlayComposerAt(lines, cursorLine, cursorCol, top, o.focusStart, o.focusEnd)`（`pkg/tui/overlays.go:1015-1022`），所以它的列表会跟随；它有现成的样板测试 `TestPlanReviewPickerKeepsTheSelectionOnScreen`（`pkg/tui/chat_session_test.go:4635`）。

### 现场证据（修复前，均已亲跑）

**(a) 真机复现**（run-forebrain driver，`tool` 模式，harness 零改动；§8 有完整命令）：tmux 120×40 → 覆盖层内容区 26 行；12 个带说明的选项；打开时 ❯ 在选项 1；`Down`×10 箭头到选项 11（屏幕最后一行）；**`Down` #11 起屏幕上没有 ❯，画面停在 offset 0（仍是选项 1–11）**，#12–#14 同样。

**(b) 单元复现**（临时测试，已删除，Step 1 会以正式名字重写）：30 个选项、终端 100×30，`Down`×8 后断言「含 ❯ 的那一行是焦点选项」失败，`offset=0`、屏幕无 ❯。

### 为什么不是别的原因（已排除）

- *滚轮/键盘滚动键抢键*：`handleQuestionKey`（`overlays.go:2749-2788`）对 ↑/↓ 只做 `moveCursorUp/Down`，没有滚动分支；滚轮走 `dispatchMouse` 与本缺陷无关。
- *覆盖层高度算错*：`maxContentHeight` 与 `maxOverlayScroll` 对审批面板同一套算法，审批面板跟随正常。
- *软换行映射缺失*：软换行与行→逻辑行映射（`overlaySoftWrapRefs`，`reducer.go:6634`）本身正确（`TestOverlayClickPositionThroughSoftWrap` 在证）；缺的只是「把焦点交给 paint」。

## 3. 修复设计

**原则：把出错路径归一到既有语义**——复用 paint 已有的 focus 范围跟随与它的 clamp 规则（先对齐尾、再对齐头；范围比窗高时保头），不新造滚动机制、不改审批面板/斜杠面板路径。

唯一的新东西是**单位换算**：审批面板的 focus 以「视觉行」计（它的行已是最终行）；user_interaction 面板交给渲染器的是**逻辑行**，paint 时才按宽度软换行成视觉行（`overlayContentLinesLocked`，`reducer.go:6606-6627`），一条逻辑行可占多行。paint 已持有 `overlayRefs`（视觉行 → 逻辑行，`reducer.go:5273`），按它把 `[focusStart, focusEnd)` 映射成视觉行区间即可；**非软换行表的 refs 是恒等映射**（`refs[i] = {line: i}`，`reducer.go:6608-6612`），所以审批面板经同一映射结果不变。

### 3.1 行为变化矩阵

| # | 场景 | 旧行为 | 新行为 |
|---|------|--------|--------|
| 1 | 选项列表高于覆盖层，↓/j 走到窗下方 | ❯ 与选中行离开屏幕，offset 不变 | 窗以最小滚动量跟随：箭头行 **＋该选项的说明** 在屏（刻意：说明是「选中行」要一起读的部分，与 owner「完整展示选项说明」裁决同向） |
| 2 | ↑/k 往回走 | 不动 | 跟着回；**指针在第 1 行时窗回到表单顶部**（tab 栏＋问题在屏；问题长到顶部放不下时仍以箭头行为准——刻意，见 §3.2 C2 的说明） |
| 3 | Other 行 / Submit 按钮 / 「Chat about this」 | 指针走过最后一个可见选项后全部不可见 | 在屏；**指针在最后一行时把其后的按键提示一起带到屏内**（与审批面板 `focusToEnd` 同款，刻意） |
| 4 | Submit 页（Review your answers）问题多、回顾长 | Submit answers / Cancel 行可能在折叠下方，❯ 不可见 | 所选动作行在屏；Cancel 为末行时同样带出按键提示 |
| 5 | 数字键 1..9 跳转、Tab/←→ 切换问题 | 跳到屏外 | 同样跟随（同一条渲染路径） |
| 5b | 问题本身长于覆盖层、指针在第 1 行（打开时即此状态） | 箭头在折叠下方 | 窗落在箭头行（问题被滚到上方）——「始终显示箭头」对第 1 行同样成立 |
| 6 | 滚轮在列表上滚动（选择未变） | 可滚 | **不变**：单纯 repaint 不回吸；下一次按键（重装覆盖层）才重新对准指针（与审批面板 `keepOptionsInView` 语义一致） |
| 7 | 面板能整屏放下 | 无滚动 | 不变 |
| 8 | 面板打开期间改终端大小 | 重排后指针行是否在屏全凭运气 | 重排后重新对准指针（软换行表的焦点行会随宽度移动，刻意新增 §3.2 B3） |
| 9 | 审批面板 / plan review picker / 斜杠面板 | 跟随正常 | 不变（恒等映射；由既有 `TestPlanReviewPickerKeepsTheSelectionOnScreen` 等守卫） |
| 10 | 非 viewport 模式（无 TTY） | 直接打印行 | 不变（未触碰） |

### 3.2 目标代码形状

**(A) `pkg/tui/render.go`** —— `SetSoftWrappingOverlayComposer` 补齐 `SetOverlayComposerAt` 同款的 `top` 与两个 focus 参数（仅 1 个生产调用点、3 个测试调用点）：

```go
// SetSoftWrappingOverlayComposer installs logical modal-overlay lines that are
// reflowed to the current viewport width on every paint. This is intended for
// user_interaction, whose prompts and option descriptions are arbitrary model
// text. Keeping the unwrapped source here also makes terminal resize reflow the
// overlay without waiting for another key event. top and the focus range are
// SetOverlayComposerAt's, only the focus counts the logical lines installed
// here: the paint maps them onto the rows the reflow produced and keeps those
// on screen, so a list taller than the overlay never scrolls its pointer out
// of sight.
func (r *Renderer) SetSoftWrappingOverlayComposer(lines []string, cursorRow, cursorCol, top, focusStart, focusEnd int) {
	r.setOverlayComposer(lines, cursorRow, cursorCol, true, top, focusStart, focusEnd)
}
```

并把 `Renderer` 字段注释（`render.go:418-425`）末尾补一句（说明单位）：

```go
	// A modal's option list has no cursor row, so it names the rows
	// [overlayFocusRow, overlayFocusEnd) to keep in view the same way.
	// The range counts the lines the overlay installed — rows, or logical
	// lines while overlaySoftWrap — and the paint maps it onto rows.
```

**(B) `pkg/tui/reducer.go`** —— 三处：

(B1) paint 里把 focus 经 refs 映射（替换 `reducer.go:5310-5312` 的 if 体）：

```go
			focusStart, focusEnd := cursorRow, cursorRow+1
			if cursorRow < 0 && r.overlayFocusPending {
				// The focus names lines of the installed table, and a table
				// that soft-wraps spreads one line over several rows: map it
				// through the refs. An unwrapped table's refs are the identity.
				focusStart, focusEnd = overlayFocusRows(overlayRefs, r.overlayFocusRow, r.overlayFocusEnd)
			}
```

(B2) 新增包级函数，紧跟 `type overlayRowRef struct{…}`（`reducer.go:6570-6574`）之后：

```go
// overlayFocusRows maps the focus range an overlay named in the lines it
// installed onto the rows those lines occupy: first, and one past the last,
// row whose line lies in [start, end), or (-1, -1) when no row's does. The
// refs run in line order, so the scan stops at the first row past the range;
// an unwrapped table's refs are the identity, and its range comes back as it
// went in.
func overlayFocusRows(refs []overlayRowRef, start, end int) (int, int) {
	first, last := -1, -1
	for i, ref := range refs {
		if ref.line < start {
			continue
		}
		if ref.line >= end {
			break
		}
		if first < 0 {
			first = i
		}
		last = i + 1
	}
	return first, last
}
```

(B3) `ViewportResize`（`reducer.go:6961-6978`）在 `r.dropPaintShadowLocked()` 之后、`r.paintViewportLocked()` 之前加：

```go
	// A soft-wrapped overlay reflows with the width, so the rows its focus
	// covers move under a selection that did not: aim the window at them
	// again, as the key that moved the selection would have.
	if r.overlayActive && r.overlaySoftWrap {
		r.overlayFocusPending = r.overlayFocusRow >= 0 && r.overlayFocusEnd > r.overlayFocusRow
	}
```

**(C) `pkg/tui/overlays.go`** —— 仅 `questionOverlay` 一族与 `rawSelector` 包装：

(C1) collector 记录焦点逻辑行范围（`overlays.go:3549-3557`）：

```go
type qlineCollector struct {
	lines []string
	width int
	// focusStart/focusEnd are the logical lines [start, end) the pointer is
	// on with whatever is read along with it, focusStart -1 until a row
	// claims them: what the painter keeps on screen, named in the lines the
	// collector writes the way the approval overlay names its focus in rows.
	focusStart int
	focusEnd   int
}

func (o *questionOverlay) newCollector() *qlineCollector {
	width, _ := o.termSize()
	return &qlineCollector{width: width, focusStart: -1}
}

// markFocusStart opens the focus range at the next line written;
// markFocusEnd closes it after the row and whatever is read along with it.
func (c *qlineCollector) markFocusStart() { c.focusStart = len(c.lines) }
func (c *qlineCollector) markFocusEnd()   { c.focusEnd = len(c.lines) }
```

(C2) `renderQuestionView`（`overlays.go:3205-3302`）在各焦点行前后打标；行渲染函数 `renderOptionRow`/`renderOtherRow` **不改**：

```go
	// Options（:3218-3224）
	sel := o.answers[o.current]
	maxIdx := len(q.Options)                     // for number width
	idxWidth := len(fmt.Sprintf("%d", maxIdx+1)) // width of the largest option number (Other = maxIdx+1)
	for i, opt := range q.Options {
		if o.cursor == i {
			c.markFocusStart()
		}
		o.renderOptionRow(c, i, opt, sel, q.AllowMultiple, idxWidth)
		if o.cursor == i {
			c.markFocusEnd()
		}
	}

	// Other row（:3226-3227）——编辑态的提示行写在 renderOtherRow 内部，一并入范围
	otherFocused := o.cursor == o.otherIdx()
	if otherFocused {
		c.markFocusStart()
	}
	o.renderOtherRow(c, q, idxWidth)
	if otherFocused {
		c.markFocusEnd()
	}

	// Submit button (multi-select only)（:3229-3241）
	if si := o.submitIdx(); si >= 0 {
		btnFocused := o.cursor == si
		pointer := "  "
		if btnFocused {
			pointer = "\x1b[36m❯\x1b[0m "
			c.markFocusStart()
		}
		label := "Next"
		if o.current == len(o.form.Questions)-1 {
			label = "Submit"
		}
		c.writeLinef("%s\x1b[1m%s\x1b[0m", pointer, label)
		if btnFocused {
			c.markFocusEnd()
		}
	}
```

「Chat about this」行（:3260-3271）——在 `chatColor = "\x1b[36m"` 之后加 `c.markFocusStart()`：

```go
	if chatFocused {
		chatPointer = "\x1b[36m❯\x1b[0m "
		chatColor = "\x1b[36m"
		c.markFocusStart()
	}
```

`renderQuestionView` 末尾、`o.finishCollector(c)`（:3301）换成：

```go
	// The pointer on the form's last row means its end, key hint included. On
	// its first row it means its start: the window opens at the tab bar and the
	// question when they fit above the row, and the focus below still brings
	// the row on screen when a longer question does not.
	top := overlayKeepScroll
	if o.cursor == o.chatIdx() {
		c.focusEnd = len(c.lines)
	}
	if o.cursor == 0 {
		top = 0
	}

	o.finishCollector(c, top)
```

> 为什么首行用 `top = 0` 而不是把 focus 范围的起点扩到 0：范围比窗高时 paint 保头（§2 摘录的 clamp 顺序），而 tab 栏与问题排在选项 0 之前——问题一长（行数 ≥ 窗高 − 2），扩范围会让箭头在打开时就被挤出窗外。`top` 是既有机制（`setOverlayComposer` 的 `top >= 0` 分支，`render.go:4682-4686`）：先把窗放到顶，再由 focus clamp 兜底——问题短则停在顶（tab 栏与问题回来），问题长则滚到箭头行。T6 钉住这条边界。

(C3) `renderSubmitView`（`overlays.go:3498-3513`）：

```go
	labels := []string{"Submit answers", "Cancel"}
	for i, label := range labels {
		pointer := "  "
		color := ""
		if i == o.submitCursor {
			pointer = "\x1b[36m❯\x1b[0m "
			color = "\x1b[36m"
			c.markFocusStart()
		}
		c.writeLinef("%s%s%s\x1b[0m", pointer, color, label)
		if i == o.submitCursor {
			c.markFocusEnd()
		}
	}

	c.writeLine("")
	c.writeLine("\x1b[2menter to confirm ↑/↓ to choose esc to cancel\x1b[0m")
	// Cancel is the view's last row: the pointer there means the end of the
	// form, its key hint included.
	if o.submitCursor == len(labels)-1 {
		c.focusEnd = len(c.lines)
	}

	o.finishCollector(c, overlayKeepScroll)
```

(C4) `finishCollector`（`overlays.go:3575-3584`）多收一个 `top` 并把范围与 `top` 一起交给渲染器：

```go
// finishCollector installs what the collector wrote as the overlay: top is the
// content row the window opens at (overlayKeepScroll to leave it), and the
// collector's focus range travels with it for the paint to keep on screen.
func (o *questionOverlay) finishCollector(c *qlineCollector, top int) {
	if o.rs != nil && o.rs.inViewportOverlay() {
		o.rs.setSoftWrappingOverlayComposer(c.lines, -1, 0, top, c.focusStart, c.focusEnd)
		return
	}
	// Non-viewport fallback: write lines directly.
	for _, line := range c.lines {
		_, _ = fmt.Fprintln(o.out, line)
	}
}
```

(C5) `rawSelector.setSoftWrappingOverlayComposer`（`overlays.go:4321-4329`）：

```go
// setSoftWrappingOverlayComposer is the long-form text variant used by
// user_interaction. The renderer retains logical rows and reflows them at the
// current terminal width, including after resize events. top and the focus
// range are setOverlayComposerAt's, the focus counted in logical lines.
func (s *rawSelector) setSoftWrappingOverlayComposer(lines []string, cursorRow, cursorCol, top, focusStart, focusEnd int) {
	if !s.inViewportOverlay() {
		return
	}
	s.renderer.SetSoftWrappingOverlayComposer(lines, cursorRow, cursorCol, top, focusStart, focusEnd)
}
```

> **参考实现（可选对照，不是抄写源）**：审批前的独立评审已按本节逐字在隔离 worktree `.worktrees/plan-review-598287185`（@`364a11a2`）落过一份参考实现，并实证 6 个新测试 5 红 → 6 绿、`pkg/tui` 全量 `ok`（197.6s）、真机箭头全程在屏。若该 worktree 届时仍在，可在自己按本节定点编辑落完之后，用 `git -C .worktrees/plan-review-598287185 diff -- pkg/tui/<file>` 与主树的 `git diff -- pkg/tui/<file>` 逐 hunk 对照。**不要 cherry-pick、不要整文件拷贝、不要改动那个 worktree**（它不是你的产物）。

## 4. Commands you will need

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"   # 每个 shell 会话先设（直接调 go 编译 pkg/pdf 需要）
```

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0（stderr 的 `ld: warning` 是本机预存噪音） |
| Format | `gofmt -l cmd pkg third_party plugins` | 不打印任何文件 |
| Vet | `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0 |
| 定点测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '<正则>' -count=1 -v` | `--- PASS` / `ok` |
| 包级回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | `ok  github.com/forebrain-harness/forebrain-harness/pkg/tui`（全量约 200s） |
| 结构门禁 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | **仅**预存红 `TestTestFilesCorrespondToProductionFiles`（`pkg/tui/deliverables_outcome_test.go` 无配对生产文件，早于本计划）；不得出现别的 FAIL |
| 全仓回归 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | `ok`／`?   … [no test files]`；任何 FAIL 行须 ⊆ Step 1 记下的全仓基线 FAIL 清单（`.forebrain/skills/plan-gated-bugfix/SKILL.md` 步骤 6 规定的包级回归；约十余分钟） |

真机（run-forebrain driver，`.claude/skills/run-forebrain/SKILL.md`）：见 §8。

## 5. Scope

**In scope**（只改这些文件）：

- `pkg/tui/render.go` —— `SetSoftWrappingOverlayComposer` 签名（补 `top`、`focusStart`、`focusEnd`）与注释；`Renderer.overlayFocusRow` 字段注释。
- `pkg/tui/reducer.go` —— `paintViewportLocked` 的 focus 映射（B1）；新增 `overlayFocusRows`（B2）；`ViewportResize` 里的重置（B3）。
- `pkg/tui/overlays.go` —— `qlineCollector`/`newCollector`/两个 mark 方法、`renderQuestionView`、`renderSubmitView`、`finishCollector`（多收 `top`）、`rawSelector.setSoftWrappingOverlayComposer`。
- `pkg/tui/overlays_test.go`、`pkg/tui/reducer_test.go` —— 既有测试文件内追加/微调（**不新增测试文件**：`pkg/architecture` 要求测试文件有配对生产文件）。
- `plans/385-question-overlay-arrow-stays-on-screen.md`（批准后落盘；原定 383 与同期 VS Code 计划撞号后顺延）、`plans/README.md`（定点追加一行＋顶部一条 bullet）。

**Round 2（owner：预存缺口必须本期一并修复）追加的范围**，同属上面这三个生产文件与两个测试文件：

- `pkg/tui/overlays.go` —— `renderQuestionView` 的焦点选项预览改到该选项下方（新增 `renderFocusedPreview`）、`renderOtherRow` 记录字段 caret、`heldPeerOverlay.lines` 返回答案行焦点、`heldPeerOverlay.render` 传焦点、`qlineCollector.caret`、`finishCollector`/`setSoftWrappingOverlayComposer` 传 `softWrapFocus`。
- `pkg/tui/render.go` —— `softWrapCaret`/`softWrapFocus` 类型、`SetSoftWrappingOverlayComposer` 收 `softWrapFocus`、`setOverlayComposer` 收 caret、`overlayFocusCaret` 字段与复位。
- `pkg/tui/reducer.go` —— caret clamp、`overlayCaretRow`、note 行移出内容表并常驻窗下、`overlayMaxScrollLocked` 计入 note 高度。

**Out of scope**（看起来相关但明确不碰）：

- `pkg/tui/overlays.go` 的 `approvalOverlay` / plan review picker（`:91-96`、`:760-866`、`:988-1025`）与 `SetOverlayComposer`/`SetOverlayComposerAt`/`SetOverlayPanel` —— 已有跟随，恒等映射下行为不变。
- 斜杠面板（`slashPanel.layout`）的滚动 —— 独立分支。
- `heldPeerOverlay.render`（`overlays.go:3724-3733`，同样 `setOverlayComposer(lines, -1, 0)` 无 focus）—— 它的两个答案（Release/Deny）在同一行、❯ 不纵向走，预览上限 3 行（`heldPeerPreviewMaxLines`，`:3623`），本症状不成立；见 §13 延后项。
- web 端 `user_interaction` 卡片（`frontend/`）—— 另一 surface，无此缺陷报告。
- 不 commit、不 push、不开 PR。

> 原第一轮列为「Out of scope / 明确延后」的三项——`AskOption.Preview` 预览块、Other 字段的软光标、排队的审批提示行——以及 `heldPeerOverlay.render` 不传焦点，已按 owner 裁决「预存缺口必须本期一并修复」在 Round 2 全部修掉，见 §13 与 Implementation Status。

## 6. Git workflow

- 直接在当前工作树 `dev` 上改，改动**留在工作区不 commit**（owner 审阅；no-git-commits）。
- **同一工作树里有多个会话在并行**（都不 commit），且名单在变——**以执行时的 `peer_list` 为准**：开工前 `peer_send` 通知当时在树里活动的每个会话「开始动 pkg/tui 源码」，并请对方报出它在 `pkg/tui` 的区域；只要不落在本计划的函数区段（§5 In scope）内就是对方的在途改动，不 STOP、不还原。计划时已知的分工（仅供对照，可能过期）：
  - `forebrain-harness-fd`（composer 拖选自动复制＋/config 开关；已直接回信确认无同函数冲突）：`reducer.go` 只改 `viewportRelease` 闸口（基线 :8052-8059）及其 doc 注释；`render.go` 只改 `newRendererAt`（:678-685）与 `SetFullAccess` 附近（:4350，新增 `SetCopyOnSelect`）；`overlays_test.go` 只改写 :2544/:2690/:2732/:2879 与 :2574/:2611 六个测试；`reducer_test.go` 只改 :5251/:5296 并新增测试；另改 `config_panel*.go`、`chat_session.go`、`run.go`、`pkg/config`、`pkg/process/config_defaults.go`。
  - `forebrain-harness-86`（subagent 卡头失败颜色）：`reducer.go` 的 `Frame` 结构体（约 :128-131）与 `emitFanoutFrame`（约 :1155-1168）、`render.go` 的 `fanoutHeaderLine`（约 :5613-5638）、`reducer_test.go` 在 `TestFailedDispatchCardSaysWhyOnceOnTheTaskRow` 之后新增测试。
  - `forebrain-harness-a7`（IME 截图缺陷）：区域未报，开工时问。
  - `forebrain-harness-4c`（execute plans/382）：在隔离 worktree `.worktrees/exit-plan-review-verdict` 里改 `overlays.go` 的 `buildApprovalContent`（approvalOverlay 区域）等，**不动主树**。
  规则：① 只用定点 `edit_file`、每轮编辑前重读锚点，让各方改动在同一文件里共存；② 对方的半成品让 build/test 红在**其区域**时，`peer_send` 问对方、不自己修、不 STOP；③ `plans/README.md` 多方都追加：每次 edit 前重读现状，只做定点插入，不重排他行。

## 7. Steps

### Step 0: 前置检查

1. 跑顶部 Drift check 三条命令。对照 §2/§3.2 的摘录逐处确认现码（`sed -n` 读对应段落）。
2. `git status --porcelain -- pkg/tui` 的输出：为空最好；**非空不 STOP**——先 `git diff -- <那些文件> | grep '^@@'` 看改动的函数区段，只要不落在本计划的函数区段（§5 In scope：`paintViewportLocked` 的 focus 段、`overlayRowRef` 之后、`ViewportResize`、`SetSoftWrappingOverlayComposer`、`overlayFocusRow` 注释、`questionOverlay` 一族、`setSoftWrappingOverlayComposer`、三个测试调用点）内，就是别的会话的在途改动。`peer_list` 列出当时在树里活动的会话，`peer_send` 通知「开始动 pkg/tui 源码」并（对未报区域的）问清其 pkg/tui 区域（§6 的分工表可能已过期，以对方当场答复为准）。此后 **每一轮编辑前**都重跑这条命令并重读要改的锚点（`sed -n`）；只用定点 `edit_file`（精确匹配旧串），绝不整文件重写、绝不 `git stash/checkout/restore`。
3. `lsof -nP -iTCP:8791 -sTCP:LISTEN` 不输出（§8 用的端口；被占就换一个并全文替换 8791）。

**Verify**: 摘录全部吻合；`pkg/tui` 的未提交改动（若有）已登记并已与对方会话对过区域；端口空闲。

### Step 1: 基线 + 回归测试先红（改任何生产代码之前）

1. **基线**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`（约 200s）。再跑 `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → 记下 FAIL 清单（预期只有预存那一条：`TestTestFilesCorrespondToProductionFiles`，因 `pkg/tui/deliverables_outcome_test.go` 无配对生产文件）。最后跑一次全仓 `CGO_ENABLED=1 go test -tags fts5 ./... -count=1`（技能规定的包级回归；约十余分钟），把全部 FAIL 行记成「全仓基线 FAIL 清单」——只改 `pkg/tui` 的改动不可能让别的包变红，这张清单是 Step 4 的对照。
2. 在 `pkg/tui/overlays_test.go` 的 `TestQuestionOverlayReopenResetsScrollOffset`（:742-810）之后追加 §9.1 的 5 个辅助函数与 5 个测试（T1–T4、T6）。它们只用既有 API，**在旧代码上可编译**。

**Verify**:
```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
mkdir -p "$TMPDIR/fb-askscroll"
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestQuestionOverlayArrowKeysKeepTheFocusedRowOnScreen|TestQuestionOverlayFocusFollowsAcrossAResize|TestQuestionOverlayWheelScrollHoldsUntilTheNextKey|TestQuestionOverlaySubmitViewKeepsTheChosenActionOnScreen|TestQuestionOverlayLongQuestionKeepsTheFirstRowOnScreen' -count=1 -v > "$TMPDIR/fb-askscroll/step1.log" 2>&1
grep -c '^--- FAIL: ' "$TMPDIR/fb-askscroll/step1.log"; grep -c '^--- PASS: ' "$TMPDIR/fb-askscroll/step1.log"; grep -c 'no tests to run' "$TMPDIR/fb-askscroll/step1.log"
grep -E '^\s+\S+_test\.go:[0-9]+: ' "$TMPDIR/fb-askscroll/step1.log"
```
→ 三个计数依次为 **5、0、0**（5 个 FAIL、0 个 PASS、没有「no tests to run」——零匹配也会 exit 0，所以要数行）；失败信息逐条为：
- T1 `9: the pointer is not on the row "Option 08"`；
- T2 `Down to option 12 lost the pointer`；
- T3 `following the pointer down to option 12 must have scrolled the overlay`；
- T4 `the review opened with its pointer off screen`；
- T6 `the pointer on the first row is off screen under a long question`。

**任何一个是 PASS = 复现不成立 = STOP**（根因未被证实）。

### Step 2a: 签名接线（行为不变）+ 渲染层测试 T5 先红

1. `pkg/tui/render.go`：按 §3.2(A) 改 `SetSoftWrappingOverlayComposer`（新签名，`top` 与 focus 原样转交 `setOverlayComposer`）与字段注释。
2. `pkg/tui/overlays.go`：按 §3.2(C5) 改 `rawSelector.setSoftWrappingOverlayComposer` 的签名并转交；`finishCollector`（:3577）**暂时**改为 `o.rs.setSoftWrappingOverlayComposer(c.lines, -1, 0, overlayKeepScroll, 0, 0)`（签名仍是不带 `top` 的旧 `finishCollector(c)`，Step 2b 再换成真值）。
3. `pkg/tui/reducer_test.go` 三个既有调用点按 §9.3 改（按 `grep -n 'SetSoftWrappingOverlayComposer(' pkg/tui/reducer_test.go` 找全，此刻应为 3 处），并在 `TestViewportPaintedRowsAreSelfContained` 之后追加 §9.2 的 T5。

**Verify**:
- `CGO_ENABLED=1 go build -tags fts5 ./...` → exit 0。
- Step 1 的命令加上 `|TestSoftWrappedOverlayFocusMapsLogicalLinesOntoRows`（日志存 `step2a.log`）→ 三个计数依次为 **6、0、0**：T1–T4、T6 信息同 Step 1；T5 为 `the focused line is off screen`（focus 已能传到渲染器，但 paint 还没用它）。

### Step 2b: paint 映射 + 面板打标（T1、T3、T4、T5、T6 转绿）

1. `pkg/tui/reducer.go`：按 §3.2(B1)(B2) 改 paint 并新增 `overlayFocusRows`。
2. `pkg/tui/overlays.go`：按 §3.2(C1)–(C5) 给 collector 加范围、给各行打标、`renderQuestionView`/`renderSubmitView` 末尾的 `top` 规则、`finishCollector(c, top)` 把范围与 `top` 一起交给渲染器。

**Verify**: 同 Step 2a 的定点测试命令（日志存 `step2b.log`）→ 三个计数依次为 **1、5、0**：T1、T3、T4、T5、T6 PASS；**T2 仍 FAIL**，信息变为 `after the resize the pointer is off screen`（跟随已生效、但重排后未重新对准——这正是 Step 3 要补的）。

### Step 3: 重排后重新对准（T2 转绿）

`pkg/tui/reducer.go` 的 `ViewportResize` 按 §3.2(B3) 加 4 行。

**Verify**: 同一定点测试命令（日志存 `step3.log`）→ 三个计数依次为 **0、6、0**。

### Step 4: 门禁

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
gofmt -l cmd pkg third_party plugins
CGO_ENABLED=1 go build -tags fts5 ./...
CGO_ENABLED=1 go vet -tags fts5 ./...
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1
CGO_ENABLED=1 go test -tags fts5 ./... -count=1 2>&1 | grep -E '^(FAIL|--- FAIL|panic)'    # 全仓 FAIL 清单
grep -rn 'func overlayFocusRows\|func arrowRow\|func flattenedScreen\|func tallQuestionForm\|func manyQuestionForm\|func openQuestionOverlay' pkg/tui/
git status --porcelain -- pkg/tui/reducer.go pkg/tui/render.go pkg/tui/overlays.go pkg/tui/overlays_test.go pkg/tui/reducer_test.go
git diff --stat -- pkg/tui
```

**Verify**: gofmt 无输出；build/vet exit 0；`pkg/tui` `ok`；`pkg/architecture` 的 FAIL 清单 == Step 1 记下的清单；**全仓 FAIL 清单 ⊆ Step 1 的「全仓基线 FAIL 清单」**（多出来的 FAIL 若在别的包，属其它会话的在途改动或环境抖动——`peer_send` 问对应会话，不自己修；若在 `pkg/tui` 则是本改动引入，STOP）；6 个新函数各恰好 1 处；`git status` 列出的正是 §5 In scope 的 5 个 Go 文件（其它路径的未提交改动属别的会话：`git diff --stat -- pkg/tui` 里若有不在这 5 个文件内的条目，确认是 fd 会话的、不动它）。

### Step 5: 真机验收

按 §8 跑 S1–S4，逐屏 `$D screen` 留证（把关键 capture 贴进计划「验收记录」）。**构建必须 `$D build`（带 `-tags fts5`）并确认二进制是新的**——漏重建会跑旧二进制、看起来"没修"。

**Verify**: S1 的 28 步全部 `OK`、S2/S3/S4 各自的预期成立。

### Step 6: 收口

1. 把本计划落盘为 `plans/385-question-overlay-arrow-stays-on-screen.md`（`382` 是 `forebrain-harness-4c` 的、`383` 被 `forebrain-harness-d8` 的 VS Code 计划占了、`384` 是同期的卡头颜色计划；若 `385` 在落盘时也被占，顺延并**同步改**本计划标题、§5 文件名与下面 Verify 的命令）。
2. `plans/README.md` 两处定点插入（**每次 edit 前重读文件现状**——4c 与 fd 也在往里写；只插入、不重排、不改他行）：
   (a)「最新审核信息」小节标题下、现有第一条 bullet 之前，加一条 bullet（一句话：owner 截图报障 → 根因 → 新立 385，含复现证据要点）；
   (b) 计划表的**最后一行之后**（基线编辑时末行是 `378-tui-slash-footer-lowercase`，其后是空行与 `## web 实现与设计稿的偏差` 小节；别插在 377 之后）加一行：`| [385-question-overlay-arrow-stays-on-screen](385-question-overlay-arrow-stays-on-screen.md) | **DONE（<实际验收日> 真机验收）—user_interaction 面板视窗随 ❯ 指针滚动**（根因：`SetSoftWrappingOverlayComposer` 写死无 focus，paint 无从跟随；证据：…） | P1 | S | LOW |`。
3. 计划文件回填「验收记录」章节（场景→证据→踩过的坑）。
4. 清理：`$D stop`；`rm -rf "$FOREBRAIN_RUN_WORK" "$TMPDIR/fb-askscroll"`。

**Verify**: `ls plans/385-*`；`grep -n '385-question-overlay' plans/README.md` 恰好 2 处；tmux 会话 `fb-askscroll` 不存在（`tmux has-session -t fb-askscroll` 报 `can't find`）。

## 8. 真机验收脚本（run-forebrain driver）

驱动脚本 `D=.claude/skills/run-forebrain/driver.sh`；全部用隔离 home，不碰 `~/.forebrain`。`$D` 命令一律 `</dev/null`（防 heredoc 被吞）。

**准备（一次性）**

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"   # $D build 走 cgo（pkg/pdf 的 pdfium），新 shell 必须先设
export FOREBRAIN_RUN_WORK="$TMPDIR/forebrain-run-askscroll" FOREBRAIN_RUN_SESSION=fb-askscroll FOREBRAIN_RUN_PORT=8791
D=.claude/skills/run-forebrain/driver.sh
mkdir -p "$TMPDIR/fb-askscroll"
cat > "$TMPDIR/fb-askscroll/args.json" <<'EOF'
{"name":"user_interaction","arguments":{"questions":[{"header":"清理范围","question":"磁盘已满（数据卷 189G/228G，仅剩 1.8G）。我扫了整个主目录，把可以清理的项目列在下面，请勾选本次要清理的范围（可多选），不确定的留空即可。","multiSelect":true,"options":[
{"label":"构建/IDE 缓存（≈17.5G）","description":"go clean -cache 等，随用随重建，不影响任何项目","recommended":true},
{"label":"构建产物 node_modules + target（≈2.9G）","description":"需要时可重新 npm/pnpm install、mvn package"},
{"label":"本地 Maven 依赖仓（≈3.5G）","description":"删后下次构建需联网重新下载依赖"},
{"label":"Downloads/logs 的 rar 压缩包（2.7G）","description":"37_1.rar、37_2.rar、38.rar 三个日志包；如需保留原始导出请先移走"},
{"label":"下载目录杂项（≈250M）","description":"dmg 安装包、122M 文本日志、PDF 报告"},
{"label":"应用数据：微信/Docker/.unisandbox（≈19G）","description":"会先列出细项再逐个确认，不动聊天记录本体"},
{"label":"forebrain 旧会话（7.5G，只清 3 个月前）","description":"只删过期会话与事件，保留最近三个月"},
{"label":"系统缓存 ~/Library/Caches（≈6G）","description":"浏览器与 IDE 的下载缓存，可安全重建"},
{"label":"Xcode 派生数据（≈8G）","description":"DerivedData 与模拟器缓存，下次构建重建"},
{"label":"Docker 镜像与构建缓存（≈5G）","description":"只清理悬空镜像与已停止的容器"},
{"label":"Homebrew 旧版本（≈1.2G）","description":"brew cleanup 可回收的旧酒瓶"},
{"label":"临时文件 /tmp 与 $TMPDIR（≈900M）","description":"重启后系统本来就会清理"}
]}]}}
EOF
python3 -c "import json,sys; d=json.load(open(sys.argv[1])); print('options', len(d['arguments']['questions'][0]['options']))" "$TMPDIR/fb-askscroll/args.json"   # → options 12
$D reset && $D build </dev/null && $D start tool "$(cat "$TMPDIR/fb-askscroll/args.json")" </dev/null
$D submit '开始清理' </dev/null
$D wait 'recommended' 30 </dev/null        # 面板已打开、指针在选项 1
```

（`$D wait 'Chat about this'` 会超时——该行在折叠下方，别等它。）

**S1 复现→修复（本 bug）**

```bash
check() { local row; row="$($D screen </dev/null | grep '❯')"; case "$row" in *"$2"*) echo "$1 OK   → $row";; *) echo "$1 FAIL → ${row:-NO ARROW ON SCREEN}";; esac; }
$D screen </dev/null | sed -n '14,41p'      # 打开态：←  ☐ 清理范围  ✔ Submit  → ／问题／❯ 在选项 1（构建/IDE 缓存）／选项 1–11
# 打开时指针已在选项 1：第 k 次 Down 落在选项 k+1（#12 → Other，#13 → Submit，#14 → Chat about this）
i=0; while IFS= read -r want; do i=$((i+1)); $D key Down </dev/null; sleep 0.4; check "Down #$i" "$want"; done <<'EOF'
构建产物 node_modules
本地 Maven 依赖仓
rar 压缩包
下载目录杂项
应用数据
forebrain 旧会话
系统缓存
Xcode 派生数据
Docker 镜像
Homebrew 旧版本
临时文件 /tmp
Other
Submit
Chat about this
EOF
$D screen </dev/null | sed -n '14,41p'      # Down #14 后：❯ Chat about this ＋ 其下按键提示行在屏
i=0; while IFS= read -r want; do i=$((i+1)); $D key Up </dev/null; sleep 0.4; check "Up #$i" "$want"; done <<'EOF'
Submit
Other
临时文件 /tmp
Homebrew 旧版本
Docker 镜像
Xcode 派生数据
系统缓存
forebrain 旧会话
应用数据
下载目录杂项
rar 压缩包
本地 Maven 依赖仓
构建产物 node_modules
构建/IDE 缓存
EOF
$D screen </dev/null | sed -n '14,41p'      # Up #14 后：tab 栏与问题回到屏内，❯ 在选项 1
```

预期：**28 行全 `OK`**（Down 14 + Up 14）。从 `Down #10`（选项 11）起 tab 栏「←  ☐ 清理范围」滚出屏幕（证明视窗真的在动，而不是停在 offset 0）；`Down #14` 后 `esc to cancel` 在屏；`Up` 的前几步指针行本来就在屏内、窗不动（最小滚动），越往上窗跟着回，最后一步回到顶部。**修复前的对照已记录在 §2（#11–#14 `NO ARROW ON SCREEN`）**；若 Step 0 的 `git diff --stat 364a11a2 -- pkg/tui` 非空，需先用旧二进制重跑一次留对照（不要 `git stash`——用 `go build -overlay` 出旧包，或请 owner 确认沿用 §2 记录）。

**S2 回归（面板能整屏放下，harness 自带 `ask` 模式）**

```bash
$D reset && $D start ask </dev/null && $D submit '问吧' </dev/null && $D wait '轮询方案' 30 </dev/null
$D screen </dev/null | sed -n '20,41p'       # 预期全在屏、未滚动：←  ☐ 轮询方案  ☐ 等待上限  ✔ Submit  → ／问题／❯ 在「60 秒」(recommended)／三个选项各带说明／「Other」行／分隔线／「Chat about this」行（单选没有 Next 按钮）／按键提示
$D key Enter </dev/null; sleep 0.6; $D screen </dev/null | grep -n '等待上限\|❯'   # 预期：第二个 chip「等待上限」在屏（多选题），❯ 在「原始报错」——行为与修复前一致
```

**S3 滚轮不回吸 ＋ 重排后对准**

```bash
$D reset && $D start tool "$(cat "$TMPDIR/fb-askscroll/args.json")" </dev/null && $D submit '开始清理' </dev/null && $D wait 'recommended' 30 </dev/null
for n in $(seq 1 14); do $D key Down </dev/null; sleep 0.3; done   # 指针到「Chat about this」，窗在底部
$D screen </dev/null | grep -n '❯\|esc to cancel'            # 预期：❯ Chat about this（约第 38 行）、esc to cancel（第 40 行）
for n in 1 2 3; do $D wheel 60 30 up </dev/null; sleep 0.2; done; sleep 2     # 滚轮上翻 3 格（每格 3 行，共 9 行）
$D screen </dev/null | grep -n '❯\|esc to cancel'            # 预期：无输出 —— 指针是用户自己滚出屏幕的，画面保持（没有被拉回指针）
$D key Up </dev/null; sleep 0.4; $D screen </dev/null | grep -n '❯'          # 预期：❯ Submit 在第 40 行 —— 下一次按键把窗带回指针
$D key Down </dev/null; $D key Down </dev/null; sleep 0.4; $D screen </dev/null | grep -n '❯\|esc to cancel'   # 预期：❯ Chat about this（约第 38 行）＋ esc to cancel（第 40 行）
tmux resize-window -t fb-askscroll -x 90 -y 22; sleep 1.2
$D screen </dev/null | grep -n '❯\|esc to cancel'            # 预期：22 行终端里仍有且只有一行 ❯（Chat about this），esc to cancel 在屏 —— 重排后重新对准
```

**S4 中断自救**

```bash
$D key Escape </dev/null; sleep 2.5; $D screen </dev/null | tail -8      # 面板关闭、composer 回来、回合以 done 收尾，无残留覆盖层
$D key C-c </dev/null; $D key C-c </dev/null; sleep 1.2; $D screen </dev/null | grep 'EXIT='   # EXIT=0
$D stop </dev/null
```

非 TTY 场景不跑：本改动不触及任何非 viewport 代码路径（`inViewportOverlay()` 为假时的直接打印分支未动）。

## 9. 测试代码（整段可粘贴）

### 9.1 `pkg/tui/overlays_test.go`（追加在 `TestQuestionOverlayReopenResetsScrollOffset` 之后）

```go
// tallQuestionForm returns a single-select form whose option list is taller
// than the overlay area of a 16-row terminal: n options, each a one-row label
// over a one-row description. Numbers are zero-padded so no label is a prefix
// of another.
func tallQuestionForm(n int) state.AskForm {
	opts := make([]state.AskOption, n)
	for i := range opts {
		opts[i] = state.AskOption{
			ID:          fmt.Sprintf("opt-%d", i),
			Label:       fmt.Sprintf("Option %02d", i),
			Description: fmt.Sprintf("note %02d", i),
		}
	}
	return state.AskForm{
		Title: "Pick one",
		Questions: []state.AskQuestion{{
			ID: "q1", Prompt: "Choose one of the options below", Options: opts,
		}},
	}
}

// manyQuestionForm returns an n-question single-select form: once every
// question is answered its review lists each prompt over its answer, which
// for n of them is taller than the overlay area of a 16-row terminal.
func manyQuestionForm(n int) state.AskForm {
	questions := make([]state.AskQuestion, n)
	for i := range questions {
		questions[i] = state.AskQuestion{
			ID:      fmt.Sprintf("q%d", i+1),
			Prompt:  fmt.Sprintf("Question %d", i+1),
			Options: []state.AskOption{{ID: "x", Label: "X"}, {ID: "y", Label: "Y"}},
		}
	}
	return state.AskForm{Title: "Survey", Questions: questions}
}

// openQuestionOverlay wires a question overlay to a viewport renderer the way
// Run does before its first key, and renders it once.
func openQuestionOverlay(r *Renderer, form state.AskForm) *questionOverlay {
	o := newTestOverlay(form)
	o.rs = newRawSelector(nil, r)
	o.resetFocusForOpen()
	o.rs.resetOverlayViewportState()
	o.render()
	return o
}

// arrowRow is the painted row carrying the question overlay's pointer, or ""
// when the pointer is off screen.
func arrowRow(r *Renderer) string {
	for _, row := range strings.Split(paintedScreen(r), "\n") {
		if strings.Contains(row, "❯") {
			return row
		}
	}
	return ""
}

// flattenedScreen is a painted screen with its row breaks and repeated spaces
// folded, so a phrase the overlay wrapped across rows is found whole.
func flattenedScreen(screen string) string {
	return strings.Join(strings.Fields(screen), " ")
}

// The reported bug: a user_interaction list taller than the overlay area kept
// its window at the top while the pointer walked down, so the ❯ and the row it
// was on left the screen. The window must follow the pointer, carrying the
// focused option's description with it, and come back up with it.
func TestQuestionOverlayArrowKeysKeepTheFocusedRowOnScreen(t *testing.T) {
	r := newComposerRenderer(t, 40, 16)
	o := openQuestionOverlay(r, tallQuestionForm(30))

	press := func(key parsedKey) {
		t.Helper()
		if _, done := o.handleKey(key); done {
			t.Fatalf("key %v ended the overlay", key.kind)
		}
		o.render()
	}
	// focused asserts the pointer is on screen on the row naming want, and
	// that note — the focused option's description, "" for none — is on
	// screen with it.
	focused := func(step, want, note string) {
		t.Helper()
		if row := arrowRow(r); !strings.Contains(row, want) {
			t.Fatalf("%s: the pointer is not on the row %q (pointer row %q):\n%s", step, want, row, paintedScreen(r))
		}
		if note != "" && !strings.Contains(paintedScreen(r), note) {
			t.Fatalf("%s: the focused option's description %q is off screen:\n%s", step, note, paintedScreen(r))
		}
	}

	focused("open", "Option 00", "note 00")
	// A row already on screen moves nothing above it.
	press(parsedKey{kind: rawKeyDown})
	focused("Down to option 1", "Option 01", "note 01")
	if screen := flattenedScreen(paintedScreen(r)); !strings.Contains(screen, "Choose one of the options below") {
		t.Fatalf("Down to a row already on screen scrolled the question away:\n%s", screen)
	}

	// 9 jumps to the ninth option, far below the first screen; 1 comes back.
	press(parsedKey{kind: rawKeyRune, r: '9'})
	focused("9", "Option 08", "note 08")
	press(parsedKey{kind: rawKeyRune, r: '1'})
	focused("1", "Option 00", "note 00")
	if screen := paintedScreen(r); !strings.Contains(screen, "q1") {
		t.Fatalf("the first row must bring the tab bar back:\n%s", screen)
	}

	// Down through every option, then Other, then "Chat about this".
	for i := 1; i < 30; i++ {
		press(parsedKey{kind: rawKeyDown})
		focused(fmt.Sprintf("Down to option %d", i), fmt.Sprintf("Option %02d", i), fmt.Sprintf("note %02d", i))
	}
	press(parsedKey{kind: rawKeyDown})
	focused("Down to Other", "Other", "")
	press(parsedKey{kind: rawKeyDown})
	focused("Down to Chat about this", "Chat about this", "")
	r.mu.Lock()
	lowest := r.overlayScrollOffset
	r.mu.Unlock()
	if lowest == 0 {
		t.Fatal("the list is taller than the overlay area, so following the pointer must have scrolled it")
	}
	// The form's last row carries its key hint along.
	if screen := flattenedScreen(paintedScreen(r)); !strings.Contains(screen, "esc to cancel") {
		t.Fatalf("the pointer on the last row left the key hint off screen:\n%s", screen)
	}

	// And back up: each row in turn, then the top of the form.
	press(parsedKey{kind: rawKeyUp})
	focused("Up to Other", "Other", "")
	for i := 29; i >= 0; i-- {
		press(parsedKey{kind: rawKeyUp})
		focused(fmt.Sprintf("Up to option %d", i), fmt.Sprintf("Option %02d", i), fmt.Sprintf("note %02d", i))
	}
	if screen := paintedScreen(r); !strings.Contains(screen, "Choose one of the options below") {
		t.Fatalf("the first row must bring the question back:\n%s", screen)
	}
	r.mu.Lock()
	offset := r.overlayScrollOffset
	r.mu.Unlock()
	if offset != 0 {
		t.Fatalf("the first row is the top of the form, offset=%d", offset)
	}
}

// A narrower terminal wraps the overlay's long rows onto more of them, so the
// pointer's rows move down under a selection that did not move: the window
// must be aimed at them again on the repaint the resize causes.
func TestQuestionOverlayFocusFollowsAcrossAResize(t *testing.T) {
	r := newComposerRenderer(t, 40, 16)
	form := tallQuestionForm(30)
	// A question long enough to take more rows the narrower the terminal gets.
	form.Questions[0].Prompt = strings.Repeat("Choose wisely ", 6)
	o := openQuestionOverlay(r, form)
	for i := 0; i < 12; i++ {
		if _, done := o.handleKey(parsedKey{kind: rawKeyDown}); done {
			t.Fatalf("Down %d ended the overlay", i)
		}
	}
	o.render()
	if row := arrowRow(r); !strings.Contains(row, "Option 12") {
		t.Fatalf("Down to option 12 lost the pointer (pointer row %q):\n%s", row, paintedScreen(r))
	}

	setForcedTermSize(20, 16)
	r.ViewportResize()
	if row := arrowRow(r); !strings.Contains(row, "Option 12") {
		t.Fatalf("after the resize the pointer is off screen (pointer row %q):\n%s", row, paintedScreen(r))
	}
	if screen := paintedScreen(r); !strings.Contains(screen, "note 12") {
		t.Fatalf("after the resize the focused option's description is off screen:\n%s", screen)
	}
}

// The wheel is the user's own scroll through the list: a repaint alone must
// not pull the window back to the pointer over it, but the next key, which
// re-renders the overlay, does re-anchor on the pointer.
func TestQuestionOverlayWheelScrollHoldsUntilTheNextKey(t *testing.T) {
	r := newComposerRenderer(t, 40, 16)
	o := openQuestionOverlay(r, tallQuestionForm(30))
	for i := 0; i < 12; i++ {
		if _, done := o.handleKey(parsedKey{kind: rawKeyDown}); done {
			t.Fatalf("Down %d ended the overlay", i)
		}
	}
	o.render()
	offset := func() int {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.overlayScrollOffset
	}
	followed := offset()
	if followed == 0 {
		t.Fatal("following the pointer down to option 12 must have scrolled the overlay")
	}

	// Wheel up three rows to read what the pointer scrolled past.
	if !r.ViewportScrollOverlay(-3) {
		t.Fatal("the overlay had room to scroll up")
	}
	if got := offset(); got != followed-3 {
		t.Fatalf("wheel up by 3 moved the overlay to %d, want %d", got, followed-3)
	}
	// A status tick repaints without re-installing the overlay: the user's
	// scroll stands.
	r.mu.Lock()
	r.paintViewportLocked()
	r.mu.Unlock()
	if got := offset(); got != followed-3 {
		t.Fatalf("a repaint snapped the window back over the user's scroll: offset=%d, want %d", got, followed-3)
	}

	// The next key moves the pointer, and the window returns to it.
	if _, done := o.handleKey(parsedKey{kind: rawKeyDown}); done {
		t.Fatal("Down ended the overlay")
	}
	o.render()
	if row := arrowRow(r); !strings.Contains(row, "Option 13") {
		t.Fatalf("Down after a wheel scroll lost the pointer (pointer row %q):\n%s", row, paintedScreen(r))
	}
	if got := offset(); got <= followed-3 {
		t.Fatalf("the window did not come back down to the pointer: offset=%d", got)
	}
}

// The review behind the Submit tab is the same list in another shape: with
// enough questions its Submit/Cancel rows start below the overlay area, and
// the pointer on them must still be seen.
func TestQuestionOverlaySubmitViewKeepsTheChosenActionOnScreen(t *testing.T) {
	r := newComposerRenderer(t, 60, 16)
	o := openQuestionOverlay(r, manyQuestionForm(10))
	// Enter picks each question's first option and advances; the last one
	// opens the review.
	for i := 0; i < 10; i++ {
		if _, done := o.handleKey(parsedKey{kind: rawKeyEnter}); done {
			t.Fatalf("Enter on question %d ended the overlay", i+1)
		}
	}
	if !o.inSubmitView() {
		t.Fatalf("expected the review after the last question, current=%d", o.current)
	}
	o.render()
	if row := arrowRow(r); !strings.Contains(row, "Submit answers") {
		t.Fatalf("the review opened with its pointer off screen (pointer row %q):\n%s", row, paintedScreen(r))
	}
	if _, done := o.handleKey(parsedKey{kind: rawKeyDown}); done {
		t.Fatal("Down ended the overlay")
	}
	o.render()
	if row := arrowRow(r); !strings.Contains(row, "Cancel") {
		t.Fatalf("Down to Cancel lost the pointer (pointer row %q):\n%s", row, paintedScreen(r))
	}
	if screen := flattenedScreen(paintedScreen(r)); !strings.Contains(screen, "esc to cancel") {
		t.Fatalf("the pointer on the last row left the key hint off screen:\n%s", screen)
	}
}

// A question longer than the overlay area leaves no room above the first row
// for the tab bar and the question: the pointer must still be seen there, so
// the window opens on the row instead of the form's top.
func TestQuestionOverlayLongQuestionKeepsTheFirstRowOnScreen(t *testing.T) {
	r := newComposerRenderer(t, 40, 16)
	form := tallQuestionForm(3)
	// Twelve rows of question at this width, against ten rows of overlay area.
	form.Questions[0].Prompt = strings.Repeat("Choose wisely ", 30)
	openQuestionOverlay(r, form)
	if row := arrowRow(r); !strings.Contains(row, "Option 00") {
		t.Fatalf("the pointer on the first row is off screen under a long question (pointer row %q):\n%s", row, paintedScreen(r))
	}
	if screen := paintedScreen(r); !strings.Contains(screen, "note 00") {
		t.Fatalf("the first option's description is off screen under a long question:\n%s", screen)
	}
}
```

### 9.2 `pkg/tui/reducer_test.go`（追加在 `TestViewportPaintedRowsAreSelfContained` 之后）

```go
// A soft-wrapped overlay names its focus in logical lines, and the width
// decides how many rows one line covers: the paint must bring those rows, not
// the line's number, on screen — the way an unwrapped overlay's rows are
// brought.
func TestSoftWrappedOverlayFocusMapsLogicalLinesOntoRows(t *testing.T) {
	r := newComposerRenderer(t, 40, 16)
	// Each long line wraps onto two rows at this width (38 columns hold seven
	// of its twelve words), so the table runs: the head at row 0, six long
	// lines over rows 1-12, the focus line at row 13. Its number in the
	// table, 7, is no row at all.
	long := strings.Repeat("word ", 12)
	lines := []string{"head", long, long, long, long, long, long, "focus line", "tail line", "tail line"}
	painted := func() []string {
		r.mu.Lock()
		defer r.mu.Unlock()
		return append([]string(nil), r.vpPainted...)
	}

	// The window ends on the focused line's last row, whatever row that is.
	r.BeginComposerOverlay()
	r.SetSoftWrappingOverlayComposer(lines, -1, 0, overlayKeepScroll, 7, 8)
	rows := painted()
	if screen := paintedScreen(r); !strings.Contains(screen, "focus line") {
		t.Fatalf("the focused line is off screen:\n%s", screen)
	}
	if last := strings.TrimSpace(stripANSI(rows[len(rows)-1])); last != "focus line" {
		t.Fatalf("the focused line must end the window (minimal scroll), got %q:\n%s", last, paintedScreen(r))
	}

	// A range taller than the window keeps its start, as a panel's does: line
	// 1's first row opens the window and the head is scrolled away. Ten rows
	// are the overlay area of a 16-row terminal, so the row ten from the end
	// is the window's first.
	r.SetSoftWrappingOverlayComposer(lines, -1, 0, overlayKeepScroll, 1, 15)
	rows = painted()
	screen := paintedScreen(r)
	if strings.Contains(screen, "head") {
		t.Fatalf("a range taller than the window must scroll its start, not the head, to the top:\n%s", screen)
	}
	if first := strings.TrimSpace(stripANSI(rows[len(rows)-10])); !strings.HasPrefix(first, "word") {
		t.Fatalf("the window must open on the focused range's first row, got %q:\n%s", first, screen)
	}
}
```

### 9.3 既有调用点

`pkg/tui/reducer_test.go` 三处 `r.SetSoftWrappingOverlayComposer(X, -1, 0)` → `r.SetSoftWrappingOverlayComposer(X, -1, 0, overlayKeepScroll, 0, 0)`（`overlayKeepScroll` = 不动滚动位置；`0, 0` = 无 focus，与 `SetOverlayComposer` 的既有写法一致；`overlayKeepScroll` 是包内常量 `render.go:4635`，测试同包可用）。

## 10. Test plan 汇总

| 测试 | 文件 | 覆盖 | 修复前（Step 1） | Step 2a 后 | Step 2b 后 | Step 3 后 |
|---|---|---|---|---|---|---|
| T1 `TestQuestionOverlayArrowKeysKeepTheFocusedRowOnScreen` | overlays_test.go | ↓/↑ 全程、数字跳转、说明随行、首行回顶、末行带提示 | FAIL | FAIL | PASS | PASS |
| T2 `TestQuestionOverlayFocusFollowsAcrossAResize` | overlays_test.go | 改终端大小后重新对准 | FAIL | FAIL | **仍 FAIL**（证明重排这一半确有必要） | PASS |
| T3 `TestQuestionOverlayWheelScrollHoldsUntilTheNextKey` | overlays_test.go | 滚轮不被 repaint 回吸；下一次按键重新对准 | FAIL | FAIL | PASS | PASS |
| T4 `TestQuestionOverlaySubmitViewKeepsTheChosenActionOnScreen` | overlays_test.go | Submit 页的同症状路径 | FAIL | FAIL | PASS | PASS |
| T6 `TestQuestionOverlayLongQuestionKeepsTheFirstRowOnScreen` | overlays_test.go | 问题长于覆盖层时第 1 行的箭头仍在屏（`top=0` 而非扩范围的理由） | FAIL | FAIL | PASS | PASS |
| T5 `TestSoftWrappedOverlayFocusMapsLogicalLinesOntoRows` | reducer_test.go | 逻辑行→视觉行映射、最小滚动、超高范围保头 | （尚未加入：用了新签名） | FAIL | PASS | PASS |
| 既有 `TestPlanReviewPickerKeepsTheSelectionOnScreen`、`TestApprovalOverlay*`、`TestQuestionOverlay*`、`TestOverlayClickPositionThroughSoftWrap`、`TestOverlaySelectionMapsToLogicalLines` | — | 审批面板恒等映射不变；软换行点击/选择不变 | PASS | PASS | PASS | PASS |

样板：`TestPlanReviewPickerKeepsTheSelectionOnScreen`（`chat_session_test.go:4635`）的 `press` + `paintedScreen` 写法；夹具复用 `newComposerRenderer`（`reducer_test.go:3296`）、`newTestOverlay`（`chat_session_test.go:14886`）。

## 11. Done criteria

- [x] Step 1 的 T1–T4、T6 在修复前全 FAIL；Step 2a 的 T5 FAIL（计数与信息见各 Step 的 Verify）。
- [x] Step 3 后：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`（含 6 个新测试 PASS）。
- [x] `gofmt -l cmd pkg third_party plugins` 无输出；`CGO_ENABLED=1 go build -tags fts5 ./...`、`CGO_ENABLED=1 go vet -tags fts5 ./...` exit 0。
- [x] `./pkg/architecture` 的 FAIL 清单与 Step 1 基线完全一致（仅预存红）；全仓 FAIL 清单 ⊆ Step 1 的全仓基线 FAIL 清单。
- [x] `grep -c 'setSoftWrappingOverlayComposer(c.lines, -1, 0, top, c.focusStart, c.focusEnd)' pkg/tui/overlays.go` = 1；`grep -rn 'SetSoftWrappingOverlayComposer(' pkg/tui/` = 7 处（1 生产定义 + 1 生产调用 + 既有 3 个测试调用 + T5 的 2 个调用）；`grep -rnE 'SetSoftWrappingOverlayComposer\([^)]*, -1, 0\)' pkg/tui/` 无输出（旧三参调用零残留）。
- [x] `git status --porcelain -- pkg/tui/reducer.go pkg/tui/render.go pkg/tui/overlays.go pkg/tui/overlays_test.go pkg/tui/reducer_test.go` 恰好列出这 5 个文件；其它路径的未提交改动不属于本计划、未被动过；本计划零 commit：`git log --oneline 364a11a2..HEAD -- pkg/tui/reducer.go pkg/tui/render.go pkg/tui/overlays.go pkg/tui/overlays_test.go pkg/tui/reducer_test.go` 无输出。
- [x] §8 S1 的 28 步全 `OK`、S2/S3/S4 预期成立，证据贴入计划「验收记录」。

**Round 2（四处预存缺口）追加的判据**：

- [x] 四个新测试 `TestQuestionOverlayPreviewStaysWithItsOption`、`TestQuestionOverlayOtherCaretStaysOnScreenWhileTyping`、`TestQueuedApprovalNoteStaysOutOfTheScrolledWindow`、`TestHeldPeerOverlayKeepsItsAnswersOnScreenInANarrowTerminal` 全部 PASS（`pkg/tui` 全量 `ok`）。
- [x] 四处**先红**可复现：B1/B2/B4 在「只有 Round 1 改动」的生产文件上 FAIL、B3 在单独回退 note pin 后 FAIL（评审方独立复跑，命令与信息见 Implementation Status）。
- [x] 第一轮的 T1–T6 在 Round 2 之后仍全 PASS；`TestQuestionOverlayWheelScrollsTallOverlay`、`TestApprovalOverlay*`、`TestPlanReviewPickerKeepsTheSelectionOnScreen` 未变。
- [x] `gofmt`／`build`／`vet`／`pkg/tui`／`architecture`／全仓回归仍与基线一致；`git status` 仍只有那 5 个文件、0 commit。
- [x] `plans/385-question-overlay-arrow-stays-on-screen.md` 存在且含验收记录；`plans/README.md` 有 385 的 bullet 与表行。
- [x] tmux 会话 `fb-askscroll` 已停、临时目录已删。

## 12. STOP conditions

- Drift：§2/§3.2 的摘录与现码内容不一致（行号漂移不算）。
- `git status --porcelain -- pkg/tui` 非空**且**当时在树里活动的对方会话（以 `peer_list` 为准）答复说正在改与本计划相同的函数区段，或答复不到：停下等对方到稳定点再动（不要还原/stash/checkout 别人的改动）。
- Step 1 的 T1–T4、T6 任一在修复前 PASS：根因未被证实，停下汇报。
- Step 2a 后 T5 已 PASS（说明 paint 其实已用上 focus，§2 的根因链有误），或 Step 2b 后 T2 不是「仍 FAIL 于 `after the resize`」，或 Step 3 后 T2 仍 FAIL：都与 §3 的分析不符，停下汇报。
- 修复需要触碰 §5 Out of scope 的文件。
- `pkg/architecture` 出现预存红以外的 FAIL；或全仓回归里 `pkg/tui` 之外的新 FAIL 对应会话确认与己无关（那才可能是本改动的间接影响）。
- 真机 S1 出现任一 `FAIL`：先确认 `$D build` 确实重建了二进制（`ls -l "$FOREBRAIN_RUN_WORK/forebrain"` 的时间戳），再判定；仍是 FAIL 则停下汇报。
- 8791 端口被占且找不到空闲端口、或 `$D start` 报 `no such module: fts5`（构建漏 tag，属工具链错误不是产品 bug）。

## 13. Maintenance notes

- **以后给覆盖层加新的「指针行」**（新的行类型）：必须在 `renderQuestionView`/`renderSubmitView` 里给它打 `markFocusStart/End`，否则那一行不跟随。审批面板同理（它的 `focusStart/focusEnd` 在 `buildApprovalContent`）。
- **单位约定**：`overlayFocusRow/End` 始终是「覆盖层装入的行的单位」——非软换行表是视觉行、软换行表是逻辑行；paint 统一经 `overlayFocusRows` 映射（恒等表零成本）。新增软换行调用方照此传逻辑行。
- **首行「回顶」走 `top` 不走扩范围**：`top = 0` 先把窗放到顶，再由 focus clamp 兜底；若改成把 `focusStart` 扩到 0，问题长于「窗高 − 2 行」时范围比窗高、paint 保头，第 1 行的箭头在打开时就被挤出窗外（T6 即此边界，会变红）。末行「带出按键提示」可以扩 `focusEnd`：范围保头＝保住箭头行，尾部被裁只是提示少一行。
- **评审重点**：① `overlayFocusRows` 对非软换行表确为恒等（`refs[i] = {line: i}`，`overlayContentLinesLocked` 的 `!r.overlaySoftWrap || width < 1` 分支）；② `ViewportResize` 的重置只在 `overlaySoftWrap` 下做，不改变审批面板/斜杠面板的 resize 行为（`EndOverlay`/`BeginComposerOverlay` 会把 `overlaySoftWrap`、focus 与 caret 字段一起复位，resize 不会读到陈旧值）；③ 滚轮不被 repaint 回吸由 T3 钉死——任何「每次 paint 都跟随**范围**」的改法都会让 T3 与既有 `TestQuestionOverlayWheelScrollsTallOverlay` 变红；**caret 是唯一例外**：`overlayFocusCaret` 每次 paint 都夹紧到屏内（编辑字段时滚轮会被拉回，与主 composer 的 caret 相同），因此别把它也做成「只在移动时跟随」，否则长字段会再次把 caret 丢到窗外；④ `setOverlayComposer` 在 `top >= 0` 时会调 `overlayMaxScrollLocked()` → 再包一遍全部逻辑行：指针停在第 1 行时每次渲染多一次全表软换行（几十行量级，可忽略）。
- **Round 2 四条不变式（预存缺口修复，别回退）**：
  1. **预览贴着它自己的选项**：`renderFocusedPreview` 必须在 `<markFocusStart…markFocusEnd>` 之内调用；预览一旦挪回列表之后，`overlayFocus` 的单一连续区间就再也带不出它（`TestQuestionOverlayPreviewStaysWithItsOption` 会红）。
  2. **caret 用「逻辑行 + 显示列」表达**：`softWrapCaret{Set, Line, Col}`，paint 经 `overlayCaretRow` 映射到视觉行。列的口径必须与 `overlaySoftWrapRefs` 的 `logicalCol` 一致（去 ANSI 的显示列，即 `lipgloss.Width`）。Other 缓冲区不含换行（粘贴把 `\n` 折成空格、`< 0x20` 的字节不成为 rune），所以 `Line` 就是 `o.otherRow`；哪天允许字段内换行，这里要按拆分后的最后一段重算。
  3. **排队的审批提示行常驻**：`overlayComposerLinesLocked` 只返回内容；note 行在 paint 时追加在可见窗**下方**，并从 `maxContentHeight` 里扣掉自己的行数（`overlayMaxScrollLocked` 同步），因此它没有 ref、滚不到、点不到（`TestQueuedApprovalNoteStaysOutOfTheScrolledWindow` 钉住两头：内容在顶和在底时 note 都在屏，且点它映射不到行）。极矮终端下 note 会把内容区挤到 1 行——这是刻意取舍，别改成让 note 参与滚动。
  4. **heldPeerOverlay 一样交焦点**：`lines()` 返回答案行的 `[start,end)`，`render` 走 `setOverlayComposerAt`；它的行已是视觉行，不需要 `softWrapFocus`。`lines()` 的返回值签名变过，新增调用点别再按单返回值写。
- **已无「明确延后」项**：第一轮列的四条预存缺口（预览块、Other 软光标、排队提示行、heldPeerOverlay 焦点）已按 owner「必须本期一并修复」全部修掉，不变式见上。
- run-forebrain driver 的 `tool` 模式可直接喂任意 `user_interaction` 参数（`{"name":"user_interaction","arguments":{…}}`），无需改 harness 即可复现多选项表单——以后同类验收照用。

## Implementation Status

**Verdict: APPROVE（Round 1 + Round 2）**（2026-11-25/26，评审方＝本会话；执行器＝`alibaba/deepseek-v4.1-flash-1017`，owner 在审批面板交接）

**Round 2 裁决**：owner「预存缺口必须本期一并修复」——第一轮列为延后的四条全部在本期修掉（B1 预览、B2 Other 软光标、B3 排队提示行、B4 heldPeerOverlay）。

- **Worktree**：`.worktrees/overlay-arrow-stays-on-screen`，分支 `overlay-arrow-stays-on-screen`，基线 `c3176812`（派发时 dev 的 HEAD；相对 `364a11a2` 只多了 plans/382 在 `overlays.go` `buildApprovalContent` 的两个 hunk——行号 +19，非本计划区域）。
- **提交 / 落地**：owner 指令「合并回主树 dev 分支」后，5 个文件在 worktree 分支上提交为 `40b8c5f4`（`fix(tui): keep a question overlay's pointer, caret and note on screen (plan 385)`），rebase 到当时的 dev tip `c237bec1`（零冲突）后 ff-only 落 dev，**dev tip `0540a22f`**（`c237bec1..0540a22f`，5 文件 +715/−44）。分支 `overlay-arrow-stays-on-screen` 与 worktree 保留（未删）；同日后续提交 `23e4afe1`（另一计划的 copy_on_select）已叠在其上，未动本计划的文件。落 dev 前在该 rebase 后的分支上复跑全门禁；**落 dev 后在主树复跑**：`gofmt -l` 无输出、`go build -tags fts5 ./...` 与 `go vet -tags fts5 ./...` 均 0、`go test ./pkg/tui -count=1` → `ok 199.920s`、`./pkg/architecture` 只剩**预存红** `TestTestFilesCorrespondToProductionFiles`（worktree 里多出的 `TestWebBuildLoadsNoExternalAsset` 在主树通过，因为主树有 `pkg/gateway/dist`）。
- **偏离（均已判定可接受）**：
  1. owner 选择交接给执行模型 → 实施在隔离 worktree 而非计划正文写的「直接在当前源码」；因此 Step 0.2／§6 的同树会话协调不适用，Step 6.1–6.2（计划落盘与索引）由评审方在主树完成。**计划号顺延为 385**：原定 383，同期另一会话（`forebrain-harness-d8`）先占了 `plans/383-vscode-secondary-sidebar-key.md`。
  2. 8791 端口被占：执行器真机用 8792，评审复核用 8793（各自独立的 work 目录与会话）。
  3. 执行器曾多加一个 `markFocusEnd()`（「Chat about this」行后），随即按计划形状移除；最终 diff 与 §3.2 逐点一致。
  4. 新 worktree 的环境红：`pkg/architecture TestWebBuildLoadsNoExternalAsset`（`pkg/gateway/dist` 只有 `.gitkeep`），在基线与终态同样出现；构建输入用主树 `third_party/{libtokenizers,pdfium}` 的符号链接（gitignored，未跑下载脚本）。
- **未做**：无（Step 0–6 全部完成；非 TTY 场景按计划不跑）。修订轮数：0。

### 任务清单

| 任务 | 状态 |
|---|---|
| Step 0 前置检查（漂移、端口、构建输入） | 完成 |
| Step 1 基线 + T1–T4、T6 先红 | 完成 |
| Step 2a 签名接线 + T5 先红 | 完成 |
| Step 2b paint 映射 + 面板打标 | 完成 |
| Step 3 重排后重新对准（T2 转绿） | 完成 |
| Step 4 门禁 | 完成 |
| Step 5 真机 S1–S4 | 完成（执行器 + 评审方各自独立跑过） |
| Step 6 收口 | 完成（1–2 由评审方在主树完成；3 证据见下；4 清理完成） |
| **Round 2** B1 预览贴着它自己的选项 | 完成（先红 → 绿，评审方独立复验） |
| **Round 2** B2 Other 字段软光标跟随 | 完成（先红 → 绿，评审方独立复验） |
| **Round 2** B3 排队提示行常驻窗下 | 完成（先红 → 绿，评审方独立复验） |
| **Round 2** B4 heldPeerOverlay 答案行跟随 | 完成（先红 → 绿，评审方独立复验） |
| **Round 2** 门禁 + 真机 S1／S5／S6 | 完成 |

### 评审方亲跑的判据（不采信执行器报告）

| 判据 | 结果 |
|---|---|
| 范围：`git status --porcelain` | 恰好 5 个在范围内的文件；无其它改动；`git log c3176812..HEAD` 为空 |
| 与计划评审方独立落过的参考实现（`.worktrees/plan-review-598287185`）比对 | `reducer.go`／`render.go`／`overlays_test.go`／`reducer_test.go` **逐字节相同**；`overlays.go` 仅差 plans/382 自己的 hunk |
| `gofmt -l cmd pkg third_party plugins` | 无输出 |
| `go build -tags fts5 ./...`／`go vet -tags fts5 ./...` | exit 0／exit 0 |
| 6 个新测试（`-count=1 -v`） | 6 PASS |
| 修复前先红（`go test -overlay` 把 `render.go`／`reducer.go`／`overlays.go`／`reducer_test.go` 换回 `c3176812` 版本，跑 T1–T4、T6） | 5 FAIL／0 PASS，信息逐条为 `9: the pointer is not on the row "Option 08"`、`Down to option 12 lost the pointer`、`following the pointer down to option 12 must have scrolled the overlay`、`the review opened with its pointer off screen`、`the pointer on the first row is off screen under a long question` |
| `go test -tags fts5 ./pkg/tui -count=1` | `ok … 198.867s` |
| `./pkg/architecture` | FAIL 2 条＝基线：`TestTestFilesCorrespondToProductionFiles`（预存）、`TestWebBuildLoadsNoExternalAsset`（新 worktree 环境红） |
| 全仓 `go test -tags fts5 ./... -count=1` | 除上述 2 条外，`pkg/run` 的 `TestRosterStopReachesEveryExecutionOfTheSubagent` 在评审方这一次全量里失败 1 次；`go list -deps ./pkg/run` 不含 `pkg/tui`，单独重跑 25/25 通过 → 满负载时序抖动，与本改动无关，未修（执行器的基线与终态两次全量均无此条） |
| grep 计数 | `finishCollector` 的 focus 调用＝1；`SetSoftWrappingOverlayComposer(`＝7；旧三参残留＝0；6 个新函数各 1 处；`heldPeerOverlay.render` 未被触碰；本 diff 的 hunk 全部落在 `:3239` 之后（审批面板区域零改动） |

### 验收记录（评审方亲跑；worktree 自己的 driver，会话 `fb-askscroll-lead`、端口 8793、120×40）

- **修复前对照**（owner 批准前，主树 `364a11a2` 二进制，会话 `fb-askscroll`）：12 选项表单，`Down`×10 箭头到选项 11（屏幕最后一行）；**`Down` #11–#14 屏幕上没有 ❯**，画面停在 offset 0。
- **S1**（本 bug）：`Down` 14/14、`Up` 14/14 全 `OK`。到底时 tab 栏「←  ☐ 清理范围」已滚出屏幕（计数 0），第 38 行 `❯ 14. Chat about this`、第 40 行按键提示；回到顶部时 tab 栏、问题、`❯ 1. 构建/IDE 缓存 (recommended)` 及其说明全部回来（tab 栏计数 1）。
- **S2**（回归，harness `ask` 模式）：三选项单选题整屏放下、未滚动——tab 栏／问题／三个选项各带说明／`4. Other`／分隔线／`5. Chat about this`／按键提示；`Enter` 后第二题「等待上限」在屏、❯ 在「原始报错」。（收尾时第一个 Ctrl+C 被仍开着的第二题面板吃掉，属预期行为；随后 `$D stop`。）
- **S3**（滚轮不回吸＋重排后对准）：`Down`×14 后 ❯ 在第 38 行、提示在第 40 行；滚轮上翻 3 格并等 2 秒后**屏幕无 ❯ 也无提示**（用户自己的滚动成立）；`Up` → `❯ Submit` 出现在第 40 行（下一次按键把窗带回指针）；`Down`×2 → ❯ 回到第 38 行＋提示；`tmux resize-window 90×22` 后有且只有一行 ❯（第 20 行，Chat about this）＋提示（第 22 行）。
- **S4**（中断自救）：`Escape` 后面板关闭、无残留（❯ 与 `Asking user` 计数均为 0），回合以 `Worked for 36s` 收尾、composer 回来；`C-c`×2 → `EXIT=0`。
- **工具链备注**：新 worktree 缺 gitignored 的 `third_party/libtokenizers`、`third_party/pdfium`，用主树的符号链接补齐（下载脚本在本机网络下约 41 分钟，不要跑）；`$D build` 带 `-tags fts5`。

### Round 2 验收记录（评审方亲跑）

**先红反证**（`go test -overlay`，worktree 树零改动）：

- 变体 r1＝把 `render.go`／`reducer.go`／`overlays.go`／两个测试文件换成「只有 Round 1 改动」的版本（取自计划评审方的 `.worktrees/plan-review-598287185`，仅把两处 `lines()` 调用点改回单返回值以便编译），跑 Round 2 的四个测试＋T1–T6：
  - FAIL：`TestQuestionOverlayPreviewStaysWithItsOption`（`the focused option's preview is off screen`）、`TestQuestionOverlayOtherCaretStaysOnScreenWhileTyping`（`the Other caret is off screen after typing a long text`）、`TestHeldPeerOverlayKeepsItsAnswersOnScreenInANarrowTerminal`（`the answers are off screen in a narrow terminal`）；
  - PASS：T1–T6 全部（说明 Round 2 没有破坏第一轮语义）。
- 变体 b3＝只把 note 拼回内容表（还原 `maxContentHeight`、`overlayMaxScrollLocked` 与块拼装三处）：`TestQueuedApprovalNoteStaysOutOfTheScrolledWindow` FAIL（`the note or the focused row scrolled away at the top`）。
- 树内（Round 2 代码）：四个测试 PASS，加上 T1–T6 共 **10 PASS**。

**门禁**（Round 2 之后）：`gofmt -l` 无输出；`go build -tags fts5 ./...` exit 0；`go vet -tags fts5 ./...` 无输出；`go test ./pkg/tui -count=1` → `ok 198.998s`；`./pkg/architecture` FAIL 仅那 2 条基线；全仓 `./...` FAIL 同样只有那 2 条（这一轮没有 `pkg/run` 抖动）；`git status` 仍只有 5 个文件、`rev-list --count c3176812..HEAD` = 0。

**真机**（worktree 自己的 driver，会话 `fb-askscroll-lead2`、端口 8795、120×40）：

- **S1 回归**：`Down` 14/14、`Up` 14/14 全 `OK`（`14 OK` + `14 OK`）。
- **S5（B1）**：把 `preview` 放在第 12 个选项上——指针到该项时屏幕第 34 行是 `❯ 12. [ ] 临时文件 /tmp …`、37 行 `--- Preview ---`、38–40 行预览正文（含 `PREVIEW_TAIL`），即预览就在它自己选项下方；再按一次 Down 后 `PREVIEW_TAIL` 计数 0（跟着指针消失），按回 Up 后回到 1。
- **S6（B2）**：Down 到 `❯ 13. [ ] Other:`，窗口缩到 40×20，键入 520 字符 → `tmux capture-pane -e` 里反显单元恰好 1 个（第 19 行＝屏幕倒数第二行），且该 capture 里 `Other:` 标签行出现 0 次——说明窗已被 caret 拉到字段尾部，正是 caret clamp 在起作用（修复前窗会停在字段首行、caret 在窗外）。
- **S7（B3/B4 真机不可达）**：排队提示行需要「另一模态占着输入时又来一个审批」，held peer 需要一条真实入站 peer 消息——`driver.sh` 都造不出来（无排队模式、无双引擎 socket 会话）。两者以单元测试为证据：`TestQueuedApprovalNoteStaysOutOfTheScrolledWindow`（含「点提示行映射不到行」）与 `TestHeldPeerOverlayKeepsItsAnswersOnScreenInANarrowTerminal`，各自先红后绿见上。
- 收尾：`Escape`（先丢字段编辑、再关面板）→ `C-c`×2 → `EXIT=0`；`$D stop` → stopped，临时 work 目录与会话清理完毕。
