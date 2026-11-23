# 计划：exit_plan_mode 审批面板的模型选择器补上 /model 面板的过滤输入框

> **执行者说明**：按步骤执行，每步跑完验证命令、对照预期再往下。遇到
> “STOP conditions” 中任一条，停下报告，不要自行发挥。改动只留在工作区，
> **禁止任何 git commit**（owner 手动审阅提交）。

> **Drift check（先跑）**：`git rev-parse --short HEAD` 应为 `624aea88`
> （`3dff52cd` 是本计划起草时的基线；`624aea88` 是随后把其他会话的 5 个文件
> `pkg/assembly/rules_hook.go`、`pkg/assembly/rules_test.go`、`pkg/tui/run_test.go`、
> `pkg/turn/model.go`、`pkg/turn/model_test.go` 本地提交后的 HEAD，**不含**
> `pkg/tui/overlays.go`）。起点只约束在范围文件：
> `git status --porcelain -- pkg/tui/overlays.go pkg/tui/plan_review_filter_test.go`
> 必须为空（本树里 forebrain-harness-2f 正在改其他文件，他们的改动不是漂移）；若 HEAD 又前进，
> 只要 `git diff 624aea88..HEAD -- pkg/tui/overlays.go` 为空即可继续（别的会话的提交
> 不是漂移）。若 `pkg/tui/overlays.go` 有改动或内容与下文摘录不符，按 STOP 处理
> （行号漂移不算）。
>
> 新测试文件复用别人文件里的助手，它们是**只读依赖**，只校验内容不校验行号。
> 逐条跑，每条都必须输出 `1`（合并成一条 `grep -c` 只给总数，证明不了哪一个缺了）：
>
> ```bash
> grep -c "^func exitPlanReviewRequest" pkg/tui/chat_session_test.go   # 1
> grep -c "^func runOverlayWithKeys" pkg/tui/chat_session_test.go      # 1
> grep -c "^func stripANSI" pkg/tui/chat_session_test.go               # 1
> grep -c "^func paintedScreen" pkg/tui/chat_session_test.go           # 1
> grep -c "^func newComposerRenderer" pkg/tui/reducer_test.go          # 1
> ```
>
> 任一不是 `1`（内容不符）即 STOP，不要改别人的测试文件去适配。

## 状态

- **优先级**：P2
- **工作量**：S（1 个文件 + 1 个新测试文件）
- **风险**：LOW（TUI 单面板交互；不改数据、不改线格式）
- **依赖**：无
- **类别**：direction（TUI 交互缺口）
- **制定基线**：`624aea88`（起草于 `3dff52cd`，`624aea88` 是其他会话 5 个文件的一次
  本地提交，不含 `pkg/tui/overlays.go`），2026-11-15，分支 `dev`
- **owner 原话**：“forebrain tui的exit_plan_mode审批面板里的模型列表上方没有 /model
  命令面板里的filter输入框和搜索过滤功能，请加上补齐缺口，包括 ask another model to
  review the plan 和 Yes, hand off to another model 两个选项的模型列表”

## 目标（Why this matters）

exit_plan_mode 审批面板选 “No, ask another model to review the plan” 或
“Yes, hand off to another model” 后展开的模型列表（同一份 `PlanReviewModels`、同一个
picker，只是 `planPickerPurpose` 不同），只有问题行 + 两列模型行：

```
Which model should review this plan? (Esc to go back)
❯ alibaba/deepseek-v4.1-flash-1017   https://dashscope.aliyuncs.com/...
  chatgpt/gpt-5.6-luna               https://chatgpt.com/backend-api/...
```

而 `/model` 面板在列表上方有一条 `⌕ Type to filter` 过滤栏，输入即按关键字收窄列表
（截图 2）。模型一多，exit_plan_mode 的评审/交接只能靠 ↑/↓ 一行行翻。

目标：把 `/model` 的过滤栏与过滤行为接到这个 picker 上，两个入口（review / hand-off）
共用同一实现。**长列表（≥8 行）与 `/model` 原样；短列表沿用本 picker 既有契约**
（无过滤栏、j/k 仍移动选中行、数字仍被忽略——现有 `TestExitPlanReviewPickerIgnoresDigitKeys`
与 `TestExitPlanReviewPickerPassesTheRowItChose` 钉住的行为）：

- 列表 ≥ `pickerFilterMinRows`（8）行才显示过滤栏，与 `/model` 同一阈值、同一造型
  （`pkg/tui/render.go` 的 `panelField`：两条底色的空行夹着文本行，⌕ + “Type to filter”
  占位，聚焦时高亮、失焦时变灰）。
- 输入任意可打印字符即过滤（大小写不敏感）：按显示名 `provider/model` 匹配，
  endpoint 作为次级匹配（`pickerMatchRank` 既有排序：完全相等 → 前缀 → 词前缀 →
  包含 → 仅 endpoint 包含）。命中按 rank 稳定排序，光标回到第一条命中。
  **owner 裁决（2026-11-15）**：不匹配 `ModelName`——列表只显示这两列，看到什么就能搜什么。
- ↑/↓ 在过滤栏与列表之间移动（`navigatePicker`：过滤栏 ↓ 落到第一行，第一行 ↑
  回到过滤栏）；过滤栏内 ←/→/Home/End/Backspace/Delete 编辑；Esc 回 verdict 列表
  （与现在一致）；Enter 取当前高亮行（review = 请求评审，hand-off = 批准并交给它）。
  **owner 裁决（2026-11-15）**：键盘停在过滤栏里时 Enter 等同 `/model`——选中当前
  第一条命中并返回，不要求先按 ↓ 让 ❯ 出现（列表已按匹配度排序）。
- 无命中时列表位置显示 `No matches`（与 `/model` 同一文案），Enter 不动作、不关闭
  picker（用户没看见任何模型时按 Enter 不应替它做决定）。**owner 裁决（2026-11-15）：
  保持 picker 打开**——`/model` 在空结果时 Enter 会关闭面板（`selectState` 的
  `if len(st.filtered) == 0 { return "", false, nil }`），本 picker 有意不跟：审批面板
  的 Enter 不能把用户退回 verdict 列表，继续输入/Backspace 就能找回列表。

## Current state（现场证据，基线 `624aea88`，行号按 `3dff52cd` 的同一份内容）

全部产品代码在 `pkg/tui/overlays.go`（package `tui`），复用的过滤件也已在同包同文件。

1. `pkg/tui/overlays.go:61-79` —— 结构体只有这些 picker 字段，没有过滤状态：

```go
	planReviewPicking bool
	planPickerPurpose planPickerPurpose
	planReviewCursor  int
	planReviewShown  bool
	planReviewTopRow int
	focusStart, focusEnd int
```

2. `pkg/tui/overlays.go:746-765` —— picker 渲染分支：直接遍历全部模型、两列排版，
   没有任何过滤栏：

```go
	case o.planReviewPicking:
		models := o.req.PlanReviewModels
		list := newPlanReviewList(models, width)
		for i, model := range models {
			lead := panelIndent
			if i == o.planReviewCursor {
				lead = panelCursor
				o.focusStart = lineCount
				if i == 0 {
					o.focusStart = o.planReviewTopRow
				}
			}
			for _, row := range list.rows(lead, i, model, width) {
				writeLine("%s", row)
			}
			if i == o.planReviewCursor {
				o.focusEnd = lineCount
				focusToEnd = i == len(models)-1
			}
		}
```

   问题行在本函数 `:604-609` 画出，`:715` 的 `writeLine("")` 是问题行与 picker 正文
   之间那条空行（既有测试 `TestExitPlanReviewPickerQuestionTakesTheVerdictLine` 钉住了它）。

3. `pkg/tui/overlays.go:908-935` —— `renderViewport` 的 `cursorCol` 只服务 feedback
   编辑器；picker 目前永远把 `cursorRow` 传成 -1（隐藏硬件光标）。

4. `pkg/tui/overlays.go:1348-1432` —— picker 的开关与按键：

```go
func (o *approvalOverlay) enterPlanPickerMode(purpose planPickerPurpose) {
	o.planReviewPicking = true
	o.planPickerPurpose = purpose
	o.planReviewShown = false
	o.planReviewCursor = 0
	for i, model := range o.req.PlanReviewModels {
		if !model.Current {
			o.planReviewCursor = i
			break
		}
	}
}
...
func (o *approvalOverlay) handlePlanReviewKey(key parsedKey) (...) {
	// ↑/↓/PgUp/PgDn/Enter/j/k；没有任何输入键
}
```

5. 现成可复用（**不要新造**）：
   - `filterField` + `insert/backspace/deleteForward/move/moveTo`：`overlays.go:4421-4506`
   - `pickerFilterMinRows = 8`、`pickerShowsFilter(rows)`：`overlays.go:4509-4514`
   - `navigatePicker(&inFilter, &cursor, count, delta, hasFilter)`：`overlays.go:4526-4543`
     （它已经是 `/model`、tabbed picker 共用的一份实现）
   - `pickerMatchRank(label, description, query)`：`overlays.go:5411-5429`
   - `(*filterField).pickerField(inFilter)`：`overlays.go:4547-4551`
   - `panelField.row(width)` / `padRow(width)` / `caretCol()`：`pkg/tui/render.go:7699-7727`；
     `/model` 的 bar 就是 `slashPanel.layout`（`render.go:7792-7806`）画成
     “空行 → padRow → row → padRow → 空行”这 5 行的。
   - `selectState.panel()`（`overlays.go:4588-4614`）是 `/model` 的组装参考；
     `selectState.applyFilter()`（`:4562-4582`）是过滤语义的参考。

6. renderer 的滚动规则（`pkg/tui/reducer.go:4962-4980`）：`cursorRow >= 0` 时
   **跟光标行**滚动，否则跟 `focusStart/focusEnd`。所以只要 `inFilter` 时把返回的
   `cursorRow` 指向 bar 的文本行，过滤栏就一定会留在屏上；失焦时仍走原来的
   focus 范围（选中模型行）。

7. 现有测试基线（`pkg/tui/chat_session_test.go`）：
   - `exitPlanReviewRequest`（`:10764`）只造 **2** 个模型 → 不触发过滤栏，其余
     picker 用例（问题行、两列、窄屏堆叠、j/k、行 id）都不受影响。
   - `TestPlanReviewPickerKeepsTheSelectionOnScreen`（`:4418-4489`）用 **21** 个模型：
     本改动会让它首次画出过滤栏（多 4 行）。它的断言（最后一屏问题行滚出、按键提示
     进入；回到第 1 行问题行回来）在语义上应继续成立，但必须在 Step 5 实测通过，
     不许改断言来迁就（若真失败，按 STOP 报告，先判断是布局缺陷还是断言前提变了）。
   - `TestPlanReviewPickerScrollKeysReleaseTheOptionAnchor`（`:4381`）用 2 个模型。

仓库惯例：TUI 面板的渲染/按键都写在这一个文件里；断言用 `buildApprovalContent`
的去色行（`stripANSI`）或 `paintedScreen`，不做终端截图断言。注释用英文。

## 行为变化矩阵

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| 打开 picker，模型 < 8 个 | 问题行 + 模型行 | **不变**（无过滤栏；j/k 仍移动选中行） |
| 打开 picker，模型 ≥ 8 个 | 问题行 + 模型行 | 问题行 → 空行 → 过滤栏（3 行）→ 空行 → 模型行 |
| 过滤栏为空 | — | 全部模型，原顺序（`pickerMatchRank` 全 0，稳定排序） |
| 输入字符（键盘在列表上或栏内） | 只认 j/k、数字被忽略 | 进入过滤栏并过滤；j/k 也作为输入字符（与 `/model` 一致） |
| ↑/↓ | 只在列表内移动 | 过滤栏 ↓ → 第一行；第一行 ↑ → 过滤栏；其余同旧 |
| Backspace/Delete/←/→/Home/End | 无动作 | 只在过滤栏内编辑；无过滤栏时仍无动作；有过滤栏但键盘在列表且栏为空时 Backspace/Delete **也不动作**（不把键盘从 ❯ 上夺走） |
| 过滤后无命中 | — | 列表位置显示 `No matches`；Enter 不动作；Esc 仍返回 |
| Enter（键盘在过滤栏里） | — | 取第一条命中并返回（owner 2026-11-15：与 `/model` 一致，不必先 ↓） |
| Enter | 取 `planReviewCursor` 处模型 | 取**当前可见列表**光标处模型（review 请求 / hand-off 批准） |
| Esc / Ctrl+C | 返回 verdict / 取消 | **不变** |
| 选中行箭头 ❯ | 始终画在选中行 | 键盘在过滤栏时**不画**（与 `/model` 一致：同一时刻只有 ❯ 或光标），回到列表时恢复 |
| 硬件光标 | picker 内始终隐藏 | 过滤栏内显示在 bar 文本行的插入点；其余位置仍隐藏 |
| 过滤后两列排版 | — | 按**可见行**重新测量列宽（与面板既有“一张列表一个节奏”一致） |

## Scope

**In scope**（产品代码只改两个文件）：

- `pkg/tui/overlays.go`
- `pkg/tui/plan_review_filter_test.go`（**新建**；另一会话正在改 `chat_session_test.go`
  等 tui 测试文件，新文件让两边改动面不重叠）
- `docs/plan/PLAN_REVIEW_MODEL_FILTER_PLAN.md`（收尾时把本计划复制一份；`docs/plan/`
  已 gitignore，不进 `git status`，也不是产品代码）

**Out of scope**（明确不碰）：
- `pkg/tui/render.go`：`panelField`/`navigatePicker`/`pickerMatchRank` 全部复用，不新增渲染件。
- web 的 plan gate（`frontend/`、`pkg/gateway/api_extra.go` 的 `plan_review_models`）：
  owner 只要求 TUI；web 由同一语义的另一 surface 承担，属于另一单。
- `pkg/turn/approval.go` 的 `PlanReviewModelOptions` / `g.ReviewModels`：本改动不动数据。
- `pkg/tui/chat_session_test.go` / `overlays_test.go` / `run_test.go`：不动（除非 Stop）。
- `docs/plan/tui-prototype.html`（原型，plan 目录 gitignore）。
- 其他会话工作区里的 dirty 文件（见 Drift check）。

## Commands you will need

| 用途 | 命令 | 预期 |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 定点测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'PlanReview|ExitPlan|HandOff' -count=1` | ok，且输出里没有 `[no tests to run]` |
| 包级回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | ok |
| 竞态（改到共享状态时） | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 -race ./pkg/tui -run 'PlanReview' -count=1` | ok |
| 格式 | `gofmt -l pkg/tui` | 无输出 |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` | exit 0 |
| 真机验收构建 | `(cd <repo> && CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain)` | exit 0 |

（`FOREBRAIN_HOME` 必须是 mktemp 目录：测试会写 home。）

## Steps

### Step 1：picker 的过滤状态与过滤函数

`pkg/tui/overlays.go`，在结构体 `planReviewTopRow` 之后（`:71` 附近）加三个字段（英文注释，
风格照抄相邻字段）：

```go
	// planReviewFilter narrows the reviewer list and planReviewInFilter is true
	// while the keyboard is in it: the list keeps no ❯ then, exactly like every
	// other picker's filter. planReviewFiltered indexes req.PlanReviewModels in
	// the order the list draws them — positions, not copies, so a row keeps its
	// id and endpoint.
	planReviewFilter   filterField
	planReviewInFilter bool
	planReviewFiltered []int
```

在 `enterPlanPickerMode`（`:1353`）之前加三个方法：

（同时**改掉两处过期注释**：结构体 `:61-63` 现在写着“planReviewCursor indexes
req.PlanReviewModels”，改为“indexes planReviewFiltered — the models the filter leaves,
in the order the list draws them”；`handlePlanReviewKey` 上方 `:1381-1383` 的文档注释
补一句“a filter narrows the list; the cursor indexes the rows it leaves”，勿留旧语义。）

```go
// planReviewShowsFilter reports whether the reviewer list is long enough to need
// a filter: the same threshold every picker in this terminal uses, counted over
// the rows the picker opens with so the bar does not vanish as the list narrows.
func (o *approvalOverlay) planReviewShowsFilter() bool {
	return pickerShowsFilter(len(o.req.PlanReviewModels))
}

// applyPlanReviewFilter narrows the list to the models whose shown name — or
// endpoint — contains the filter, best match first, and re-anchors the cursor on
// the first match, the way every other picker re-anchors after a filter change.
func (o *approvalOverlay) applyPlanReviewFilter() {
	query := strings.ToLower(strings.TrimSpace(o.planReviewFilter.text))
	o.planReviewFiltered = o.planReviewFiltered[:0]
	ranks := make(map[int]int, len(o.req.PlanReviewModels))
	for i, model := range o.req.PlanReviewModels {
		name := planReviewModelDisplay(model.Provider, model.Model)
		if rank := pickerMatchRank(name, model.Endpoint, query); rank >= 0 {
			o.planReviewFiltered = append(o.planReviewFiltered, i)
			ranks[i] = rank
		}
	}
	sort.SliceStable(o.planReviewFiltered, func(a, b int) bool {
		return ranks[o.planReviewFiltered[a]] < ranks[o.planReviewFiltered[b]]
	})
	o.planReviewCursor = 0
}

// planReviewVisible returns the models the filter leaves, in the order the list
// draws them.
func (o *approvalOverlay) planReviewVisible() []turn.PlanReviewModelOption {
	out := make([]turn.PlanReviewModelOption, 0, len(o.planReviewFiltered))
	for _, i := range o.planReviewFiltered {
		out = append(out, o.req.PlanReviewModels[i])
	}
	return out
}
```

`sort` 已在 import 里；不需要新依赖。

`enterPlanPickerMode` 改成先算过滤列表、再在**可见列表**里找第一个非 Current 的行：

```go
func (o *approvalOverlay) enterPlanPickerMode(purpose planPickerPurpose) {
	o.planReviewPicking = true
	o.planPickerPurpose = purpose
	o.planReviewShown = false
	o.planReviewFilter = filterField{}
	o.planReviewInFilter = false
	o.planReviewFiltered = o.planReviewFiltered[:0]
	o.applyPlanReviewFilter()
	for i, modelIdx := range o.planReviewFiltered {
		if !o.req.PlanReviewModels[modelIdx].Current {
			o.planReviewCursor = i
			break
		}
	}
}
```

`exitPlanReviewMode`（`:1374`）逐项清空：`planReviewFilter = filterField{}`、
`planReviewInFilter = false`、`planReviewFiltered = o.planReviewFiltered[:0]`（其余保留）。

**Verify**：`CGO_ENABLED=1 go build ./pkg/tui` → exit 0（此时渲染/按键未接，行为不变）。

### Step 2：画过滤栏，列表按可见行渲染

把 “Current state” 摘录的 `case o.planReviewPicking:` 整个替换为：

```go
	case o.planReviewPicking:
		models := o.planReviewVisible()
		// The reviewer list carries the same filter bar every picker in this
		// terminal does, in the same shape: air above it, a padded row, the
		// text's row, a padded row, then air before the list it narrows.
		if o.planReviewShowsFilter() {
			field := o.planReviewFilter.pickerField(o.planReviewInFilter)
			lines = append(lines, field.padRow(width))
			lineCount++
			if o.planReviewInFilter {
				// Only the field's own keyboard shows the hardware caret; on
				// the rows the list's ❯ is the indicator, and the bar is
				// dimmed. Handing the painter the row only while it is focused
				// is also what keeps the bar on screen as the user types —
				// the paint follows the caret row.
				cursorLine = lineCount
			}
			lines = append(lines, field.row(width))
			lineCount++
			lines = append(lines, field.padRow(width))
			lineCount++
			writeLine("")
		}
		if len(models) == 0 {
			writeLine("%sNo matches", panelIndent)
		}
		list := newPlanReviewList(models, width)
		for i, model := range models {
			lead := panelIndent
			if i == o.planReviewCursor {
				// The list keeps its ❯ only while the keyboard is on it: a
				// picker shows its cursor or its ❯, never both.
				if !o.planReviewInFilter {
					lead = panelCursor
				}
				o.focusStart = lineCount
				if i == 0 {
					o.focusStart = o.planReviewTopRow
				}
			}
			for _, row := range list.rows(lead, i, model, width) {
				writeLine("%s", row)
			}
			if i == o.planReviewCursor {
				o.focusEnd = lineCount
				focusToEnd = i == len(models)-1
			}
		}
```

注意：问题行与过滤栏之间的空行由 `:715` 既有的 `writeLine("")` 提供，**不要**再加
一个。`cursorLine` 只在“过滤栏存在且键盘在栏内”时指向 bar 文本行：/model 的 bar 在
失焦时只变灰、不显示插入点，这里用同一个 `pickerField(inFilter)` 的 `blurred` 做底色/
配色，插入点则靠“失焦时不给 `cursorLine`”关掉；同时 renderer 的滚动跟随（`reducer.go`
的 `cursorRow >= 0` 分支）正好让输入期间 bar 常驻屏上。

`renderViewport`（`:908-914`）在 feedback 分支后补：

```go
	if cursorLine >= 0 && o.planReviewPicking && o.planReviewInFilter {
		cursorCol = o.planReviewFilter.pickerField(o.planReviewInFilter).caretCol()
	}
```

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'PlanReview|ExitPlan|HandOff' -count=1`
→ ok，且输出里没有 `[no tests to run]`（实际应跑约 62 个用例；2 模型的既有用例不画栏，
21 模型那例现在画栏，必须仍绿）。

### Step 3：按键接过滤（与 `/model` 一致的语义）

把 `handlePlanReviewKey`（`:1384-1432`）整体替换为：

```go
func (o *approvalOverlay) handlePlanReviewKey(key parsedKey) (turn.ToolApprovalDecision, bool) {
	o.keepOptionsInView = true
	models := o.planReviewVisible()
	filterShown := o.planReviewShowsFilter()
	switch key.kind {
	case rawKeyEscape:
		o.exitPlanReviewMode()
	case rawKeyCtrlC:
		return turn.ToolApprovalDecision{Cancelled: true}, true
	case rawKeyUp:
		navigatePicker(&o.planReviewInFilter, &o.planReviewCursor, len(models), -1, filterShown)
	case rawKeyDown:
		navigatePicker(&o.planReviewInFilter, &o.planReviewCursor, len(models), 1, filterShown)
	case rawKeyPageUp:
		o.scrollOverlay(-10)
	case rawKeyPageDown:
		o.scrollOverlay(10)
	case rawKeyBackspace:
		if !filterShown {
			return turn.ToolApprovalDecision{}, false
		}
		// An empty filter the keyboard is not in has nothing to delete: taking
		// the keyboard to edit nothing would trade the list's ❯ for a caret
		// that changes no query (the guard in selectState's rawKeyBackspace /
		// rawKeyDelete cases).
		if !o.planReviewInFilter && o.planReviewFilter.text == "" {
			return turn.ToolApprovalDecision{}, false
		}
		o.planReviewInFilter = true
		if o.planReviewFilter.backspace() {
			o.applyPlanReviewFilter()
		}
	case rawKeyDelete:
		if !filterShown {
			return turn.ToolApprovalDecision{}, false
		}
		if !o.planReviewInFilter && o.planReviewFilter.text == "" {
			return turn.ToolApprovalDecision{}, false
		}
		o.planReviewInFilter = true
		if o.planReviewFilter.deleteForward() {
			o.applyPlanReviewFilter()
		}
	case rawKeyLeft:
		if filterShown && o.planReviewInFilter {
			o.planReviewFilter.move(-1)
		}
	case rawKeyRight:
		if filterShown && o.planReviewInFilter {
			o.planReviewFilter.move(1)
		}
	case rawKeyHome:
		if filterShown && o.planReviewInFilter {
			o.planReviewFilter.moveTo(0)
		}
	case rawKeyEnd:
		if filterShown && o.planReviewInFilter {
			o.planReviewFilter.moveTo(len(o.planReviewFilter.runes()))
		}
	case rawKeyPaste:
		if !filterShown {
			return turn.ToolApprovalDecision{}, false
		}
		o.planReviewInFilter = true
		o.planReviewFilter.insert(strings.ReplaceAll(key.text, "\n", " "))
		o.applyPlanReviewFilter()
	case rawKeySpace:
		if !filterShown {
			return turn.ToolApprovalDecision{}, false
		}
		o.planReviewInFilter = true
		o.planReviewFilter.insert(" ")
		o.applyPlanReviewFilter()
	case rawKeyEnter:
		// A filter that matches nothing is not an answer: the picker stays open
		// on it rather than closing on a model the user never saw.
		if len(models) == 0 {
			return turn.ToolApprovalDecision{}, false
		}
		selected := models[o.planReviewCursor]
		purpose := o.planPickerPurpose
		o.exitPlanReviewMode()
		// A hand-off pick approves the exit and names the executor; the
		// conversation's own model stays as the tech lead. The current model
		// may be picked: handing off to it is odd but sound.
		if purpose == pickHandOff {
			return turn.ToolApprovalDecision{Approved: true, HandOffModel: &selected}, true
		}
		return turn.ToolApprovalDecision{RequestPlanReview: &selected}, true
	case rawKeyRune:
		// A picker with a filter types into it, the way every other picker
		// does — j/k included — and a short list with no filter keeps its
		// one-key navigation.
		if filterShown {
			o.planReviewInFilter = true
			o.planReviewFilter.insert(string(key.r))
			o.applyPlanReviewFilter()
			return turn.ToolApprovalDecision{}, false
		}
		switch {
		case key.r == 'j':
			if o.planReviewCursor < len(models)-1 {
				o.planReviewCursor++
			}
		case key.r == 'k':
			if o.planReviewCursor > 0 {
				o.planReviewCursor--
			}
		}
	}
	return turn.ToolApprovalDecision{}, false
}
```

要点：`rawKeyEnter` 不再有“光标越界就退出 picker”的分支——游标始终落在可见列表内，
唯一可能为空的是过滤结果。

**Verify**：
- `grep -n "navigatePicker(&o.planReviewInFilter" pkg/tui/overlays.go` → 2 处（↑/↓）。
- `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'PlanReview|ExitPlan|HandOff' -count=1` → ok 且没有 `[no tests to run]`（约 62 个用例）。

### Step 4：新增测试（新文件）

新建 `pkg/tui/plan_review_filter_test.go`（package tui），复用 `newApprovalOverlay`、
`stripANSI`、`exitPlanReviewRequest`、`runOverlayWithKeys`、`displayLineWidth`。
至少覆盖：

1. `TestPlanReviewPickerFilterBarNeedsALongList`：7 个模型 → 渲染里没有 `⌕`；8 个 →
   有 `⌕` + `Type to filter`，且问题行与 bar 之间恰好一行空行、bar 与第一行模型之间
   一行空行（对照 `/model` 的 5 行造型）。
2. `TestPlanReviewPickerFilterMatchesNameAndEndpoint`：12 个模型——3 行 `deepseek/*`；
   **其余 4 行用别的 provider（`openai`/`zhipuai`/`anthropic`…），display 名一律不含
   `deep`**（否则 `deep` 会命中全部，断言空转）；endpoint 含 `anthropic` 的那一行名字里
   不含 `anthropic`。输入 `deep` → 只剩 3 行 deepseek、**另外 4 行逐一断言已从渲染里
   消失**；输入 `anthropic` → 只命中 endpoint 那一行；输入 `zzz` → 只剩 `No matches`。
3. `TestPlanReviewPickerFilterKeys`：用 `handlePlanReviewKey` 直接驱动——
   输入 `abc` 后 `planReviewInFilter` 为真且 `planReviewFilter.text == "abc"`；
   ↓ 离开过滤栏落到第一行、↑ 回到过滤栏；Backspace 删字符并重算列表；
   Esc 退出 picker 且不返回决定。
4. `TestPlanReviewPickerFilterEnterReturnsTheVisibleRow`：过滤后 Enter 返回
   **命中行**的 id/endpoint（不是被过滤掉的行）；键盘仍在栏内时 Enter 同样返回第一条
   命中（owner 裁决），且无命中时 Enter 不关 picker。
5. `TestPlanHandOffPickerFiltersModels`：`enterHandOffMode()` + 过滤 + Enter →
   `Approved == true` 且 `HandOffModel` 是命中行。
6. `TestPlanReviewPickerFilterTakesTheCaret`：`buildApprovalContent` 只返回
   `(lines, cursorLine)`（overlays.go:513），所以断言 cursorLine 在过滤栏内等于 bar
   文本行；caretCol 另取 `o.planReviewFilter.pickerField(true).caretCol()`（overlays.go
   的 `pickerField` → render.go 的 `caretCol`），断言它随输入右移；键盘在列表上
   （失焦）时 cursorLine 为 -1（不画插入点，与 `/model` 的 blurred bar 一致）。
7. `TestPlanReviewPickerFilterShortListKeepsJK`：小列表保持旧行为——3 个模型时没有
   `⌕`，`j`/`k` 仍移动光标、`1`/`9` 仍被忽略（防止回归）。
8. `TestPlanReviewPickerFilterEmptyKeepsTheKeyboardOnTheRows`：8 个模型（画栏）、键盘
   仍在列表、过滤栏为空时按 Backspace 与 Delete → `planReviewInFilter` 仍为 false、
   选中行仍画 ❯、列表不变（对应 `selectState` 的同一条守卫）。**反方向也要测**：先
   输入 `deep`、↓ 回到列表，再按 Backspace → 键盘回到过滤栏、文本变为 `dee`、列表
   按新查询重算（守卫只挡“栏为空”那一种，不能连这条回程路一起挡掉）。
9. `TestPlanReviewPickerFilterTypesFromTheRows`：键盘在列表上直接打字（不先 ↓/点击），
   喂 `rawKeyRune` 后进入过滤栏且列表收窄（用户最常见的路径）。
10. `TestPlanReviewPickerFilterBarStaysOnScreen`：80×24 + 长计划正文 + 12 个模型，用
   `paintedScreen` 断言打字期间 `⌕` 一直在屏、选中行不画 ❯（把“输入时 renderer 跟着
   caret 行滚动”这条推理钉成测试，别只留在注释里）。
11. `TestPlanReviewPickerFilterBarWidth`：`buildApprovalContent(120/80/68/40)`（过滤栏
   为空、显示占位符）下，**先找含 `⌕` 的那一行**，再取它 ±1（不能用固定行号：40 列时
   问题行会折成两行，bar 不在 4/5/6），断言这三行 `displayLineWidth` 都恰好等于 width。
   长查询（超过可用列宽）的 bar 行会溢出，这是 `/model` 同一份 `panelField.row` 的既有
   行为，本单**不修也不断言**（见维护节）。

断言方式照抄 `TestPlanReviewPickerKeepsTheSelectionOnScreen` 的去色行/`paintedScreen`
风格；不要用真实终端。

**Verify（必须数 PASS 行，`[no tests to run]` 是假绿）**：

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui \
  -run 'TestPlanReviewPickerFilter|TestPlanHandOffPickerFiltersModels' -count=1 -v \
  | tee /tmp/plan-review-filter-tests.log | grep -c '^--- PASS'
```

→ 输出 `11`（上面 11 条用例各一行 `--- PASS`；`--- PASS: TestPlanReviewPickerFilter`
这类子测试会被同一条正则命中，若加了子测试请按实际条数核对，且不许出现
`no tests to run`）。

### Step 5：包级回归与格式

**Verify**：
- `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → ok
- `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 -race ./pkg/tui -run 'PlanReview' -count=1` → ok
- `gofmt -l pkg/tui` → 无输出
- `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` → exit 0
- `CGO_ENABLED=1 go build ./...` → exit 0

### Step 6：tmux 真机验收（必做）

用仓库既有 fake provider 驱动真实 TUI（**不烧真模型**）：

```bash
WORK="${FOREBRAIN_RUN_WORK:-${TMPDIR:-/tmp}/forebrain-run}"
cd .claude/skills/run-forebrain
./driver.sh build
./driver.sh serve toolcall          # 隔离 home + 配置 + fake provider（toolcall 模式发 enter_plan_mode/exit_plan_mode）
# serve 之后给隔离 home 的配置补到 ≥8 个模型（同一 provider/base_url，不同 model 名），
# 让评审选择器越过 pickerFilterMinRows：
python3 - "$WORK/home/forebrain.yaml" <<'PY'
import sys
path = sys.argv[1]
lines = open(path).read().splitlines()
out, inserted = [], 0
for line in lines:
    out.append(line)
    if line.strip().startswith("- provider:"):
        indent = line[:len(line) - len(line.lstrip())]
        base = indent + "  "
        for i in range(2, 10):
            out += [f"{indent}- provider: deepseek", f"{base}model: deepseek-extra-{i}",
                    f"{base}api_key: ${{FAKE_LLM_KEY}}", f"{base}base_url: http://127.0.0.1:8731"]
        inserted += 1
if inserted != 1:
    raise SystemExit("expected exactly one llm_providers entry, found %d" % inserted)
open(path, "w").write("\n".join(out) + "\n")
PY
# hand-off 行要 git 仓库（turn/approval.go 的 MainWorktreeRoot 判定）：scratch 项目先 init。
git -C "$WORK/proj" init -q
git -C "$WORK/proj" -c user.email=a@b -c user.name=a commit -q --allow-empty -m init
# 按 driver.sh cmd_start 的方式启动（serve 已写好 home，别再调 start，它会重写配置）：
tmux new-session -d -s forebrain-run -x 120 -y 40 -c "$WORK/proj" \
  "FAKE_LLM_KEY=sk-local-fake FOREBRAIN_HOME=$WORK/home $WORK/forebrain; echo EXIT=\$?; exec bash"
```

然后（以下命令都在 `.claude/skills/run-forebrain` 目录里跑，`driver.sh` 是 `./driver.sh`）：
信任页答案是 `./driver.sh key Down` 再 `./driver.sh key Enter`（等 composer 出现）→ `./driver.sh submit "go"` 让 fake provider 发出第一轮
（enter_plan_mode 审批 → Enter 批准）→ 第二轮 `exit_plan_mode` 审批面板出现 → 先验证
模型确实进了评审列表：`grep -c "model:" "$WORK/home/forebrain.yaml"` ≥ 9（少于 9 就按
STOP 报告，不要继续）→ 选 “Ask another model to review this plan” → 断言屏幕出现
`⌕ Type to filter`（模型行数不要用“一屏能看几行”判断：overlay 高度受计划正文影响；
需要数行就用 ↓ 翻到底，看最后一屏出现 `deepseek-extra-9`）→ `./driver.sh send "extra-9"`
（**别用 `submit`**：那个会追加 Enter，在过滤栏里等于立刻选中并关掉 picker；也**不要用
`deep`**：脚本造的模型全是 `deepseek/*`，`deep` 会命中全部，证明不了收窄）→ 断言屏上
只剩 `deepseek/deepseek-extra-9` 一行、`deepseek-extra-8` 与 `deepseek-chat` 都不在屏、
`No matches` 不出现 → 断言 `⌕` 所在行右侧文本为 `extra-9`（`./driver.sh
caret <bar行号>` 的 `line=`，bar 行号从 `./driver.sh screen` 数出来；该探针默认找
composer 行，**必须显式传行号**）→ `./driver.sh send "zzz"` → 断言 `No matches`，且
键盘仍在栏内 `./driver.sh key Enter` 后屏幕不变（owner 裁决：无命中不关 picker）→
`./driver.sh key BSpace` ×10 清空（`extra-9` 7 个字符 + `zzz` 3 个）→ 再采样一次
`./driver.sh caret
<bar行号>`：此时栏是空的、画的是占位符，插入点落在 `Type to filter` 的首字符上——
预期 `caret='T'`（不是空白）且 `line=` 里能看到 `Type to filter`；只要不是
`(none in 6 samples...)` 就说明插入点在 bar 上
→ `./driver.sh key Down` 进列表后断言 ❯ 回到选中行、`./driver.sh caret <bar行号>` 报
`(none ...)` → `./driver.sh key Up` 回过滤栏 → `./driver.sh key Escape` 回到 verdict 列表
→ 再选 “Yes, hand off to another model” → 重复一次过滤 → `./driver.sh key Enter` →
断言确认行交给的是命中模型。

取证：每步 `./driver.sh screen`（等价 `tmux capture-pane -t forebrain-run -p`），caret
用 `./driver.sh caret <bar行号>` 的输出，把关键屏粘进“验收记录”。
收口：`./driver.sh stop`、`tmux kill-session -t forebrain-run`、清理 `$WORK`。

> 若 `toolcall` 场景下 “Ask another model to review this plan” 行不出现，或配置里的
> `model:` 条目少于 9 条，说明隔离 home 的模型没进 `ReviewModels`——**不要**靠换端口
> 重试（端口只影响 fake provider 的监听口，与模型列表无关）。停下按 STOP 报告，附
> `forebrain.yaml` 的 `llm_providers` 段、`$WORK/provider.log` 与屏幕截图，不要伪造验收。

## Test plan

- 新文件 `pkg/tui/plan_review_filter_test.go`，用例见 Step 4；结构参考
  `chat_session_test.go` 的 `TestPlanReviewPickerKeepsTheSelectionOnScreen`（去色行 +
  逐个按键断言屏上内容）。
- 回归面：`pkg/tui` 全包（尤其 21 模型那例、`TestExitPlanReviewPicker*`、
  `TestExitPlanOverlay*`、`TestPlanReviewPickerScrollKeysReleaseTheOptionAnchor`）。
- 失败注入/边界：空过滤、空过滤时 Backspace/Delete、栏非空时从列表 Backspace 回程、
  无命中、无命中时 Enter、全命中、过滤后 Enter、过滤后 Esc、小列表无过滤栏、键盘在列表上
  直接打字、80×24 打字期间栏常驻、栏三行在 120/80/68/40 下恰好等于 width。

## Done criteria

- [x] `CGO_ENABLED=1 go build ./...` exit 0
- [x] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` ok
- [x] 新用例（Step 4 的 11 类）存在且全绿（`grep -c '^--- PASS'` 输出 11）
- [x] `gofmt -l pkg/tui` 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` exit 0
- [x] tmux 真机：review 与 hand-off 两个入口都能看到 `⌕ Type to filter`、输入即收窄、
      无命中显示 `No matches`、Esc 返回、Enter 交给命中行；`/caret` 证明插入点落在 bar
      文本行且随输入右移，↓ 进列表后消失（截图为证）
- [x] `git status --porcelain -- pkg/tui/overlays.go pkg/tui/plan_review_filter_test.go`
      只出现这两个文件（其他会话的脏文件/提交不算，保持原样）
- [x] 计划文件回填“验收记录”，并复制到 `docs/plan/PLAN_REVIEW_MODEL_FILTER_PLAN.md`

## STOP conditions

- `pkg/tui/overlays.go` 与“Current state”摘录内容不符（别人的会话已改过 picker）。
- Drift check 里提到的测试助手（`exitPlanReviewRequest`、`runOverlayWithKeys`、
  `stripANSI`、`paintedScreen`、`newComposerRenderer`）签名/存在性变化（内容不符）。
- `TestPlanReviewPickerKeepsTheSelectionOnScreen` 在 Step 2 后失败，且原因不是“多出的
  4 行让某个断言前提变了”这种可解释的布局差异——不许直接改它的断言迁就。
- 需要改 `pkg/tui/render.go`、`pkg/turn/*`、`pkg/gateway/*` 才能完成（说明复用的件不够，
  这属于方案变更）。
- 真机验收无法进入带 ≥8 模型的评审 picker（见 Step 6 的 STOP 说明）。
- 同一验证命令连续两次失败后仍无解释。

## Maintenance notes

- 阈值 `pickerFilterMinRows = 8` 是全局常量：改它等于同时改 `/model`、tabbed picker 与
  本 picker，属于产品级决策，不要在这个文件里局部覆盖。
- `planReviewCursor` 从此索引**可见列表**（`planReviewFiltered`），不再直接索引
  `req.PlanReviewModels`；任何新代码读它都必须经 `planReviewVisible()`。结构体注释与
  `handlePlanReviewKey` 的文档注释必须与此一致。
- 过滤栏造型现在有**两份实现**：`slashPanel.layout`（`render.go:7792-7806`，`/model`
  用）和本 picker 的渲染分支（扁平行，无法直接复用 layout）。改栏高、留白或配色时
  两处必须同改，否则两个表面会漂。
- bar 的三行是**直接 append 进 `lines`** 的：`panelField.row/padRow` 已经自己把宽度
  铺满到 width，`writeLine` 只会把它们再送进 `approvalOverlayVisualLines` 重做一次换行
  测量（正常宽度下恰好是 1 段，不是必需，但多一层无谓的测量；长查询时 `writeLine` 会
  把行拆开，反而与 `/model` 的表现不同）。`TestPlanReviewPickerFilterBarWidth` 钉住
  120/80/68/40 下三行都恰好等于 width。
- 已知继承缺陷（本单不修）：`panelField.row` 只补 pad、不截断，过滤文本长于可用列宽时
  bar 行会**超出 width**（实测 width=40、60 字符查询 → 67 列）。`/model` 走的是同一个
  `panelField.row`，行为相同；要做截断就两个表面一起做，属另一单。
- 过滤栏与列表的列宽各自按可见行测量：过滤后 endpoint 列可能左移，这是“一张列表一个
  节奏”的既有规则，不要为了跨过滤稳定列宽去缓存全量布局。
- 有意未做：过滤栏的鼠标点选/拖动选择（approval overlay 目前整体不做行/栏命中测试，
  加它要另开一套 overlay→field 的坐标探测，属另一单）。
- 有意未做：匹配 `ModelName`（owner 2026-11-15 裁决：列表只显示 provider/model 与
  endpoint，看到什么就能搜什么）。
- 无命中时 Enter 保持 picker 打开，是 owner 2026-11-15 的裁决，与 `/model`（空结果
  Enter 关闭面板，`selectState` 的 `if len(st.filtered) == 0 { return "", false, nil }`）
  **有意不同**：审批面板的 Enter 不把用户退回 verdict 列表。不要为了一致性改回去。
- web plan gate 未同步：若 owner 后续要求两 surface 一致，改的是 web 的 model 选择组件，
  与本 picker 无关。

## Implementation Status

**VERDICT: APPROVE**（2026-11-15，reviewer = tech lead 会话；执行 = opencode-go/deepseek-v4.1-flash）

- **Worktree**：`.worktrees/plan-review-model-filter`，分支 `plan-review-model-filter`，
  基线 `63f915ad`（计划写的 `624aea88` 之后其他会话又提交了 workbuddy 两个 commit；
  `git diff 624aea88..HEAD -- pkg/tui/overlays.go` 为空，按计划规则不算漂移）。
- **Commits**：`ad6f26c7 feat(tui): filter the plan-review model picker like /model`、
  `1c824c33 refactor(tui): give the reviewer filter its own file`。未 push，未动其他分支。
  **owner 2026-11-15 批准合并**：主树 `git merge --ff-only plan-review-model-filter`，
  dev `63f915ad → 1c824c33`；合并后主树复跑 build + 11 个新用例全绿；执行 worktree 已删除
  （分支 `plan-review-model-filter` 仍在，可随时删除）。
- **改动文件（3 个，与计划的 2 个不同——见 Deviations）**：
  `pkg/tui/overlays.go`(+142/−16)、`pkg/tui/review_filter.go`(新, +52)、
  `pkg/tui/review_filter_test.go`(新, +539)。

### Reviewer 自己复跑的结果（worktree 内，非转述执行者报告）

| 门禁 | 结果 |
|---|---|
| `CGO_ENABLED=1 go build ./...` | exit 0 |
| `go vet -tags fts5 ./pkg/tui` | exit 0 |
| `gofmt -l pkg/tui` | 无输出 |
| 定点 `-run 'PlanReview\|ExitPlan\|HandOff' -count=1 -v` | ok，**73** 个 `--- PASS`，0 个 `no tests to run` |
| 新增 11 例 `-run 'TestPlanReviewPickerFilter\|TestPlanHandOffPickerFiltersModels'` | **11** 个 `--- PASS`，名称与计划 Step 4 一一对应 |
| 全包 `go test -tags fts5 ./pkg/tui -count=1` | ok（91.7s），21 模型那例未改断言仍绿 |
| `pkg/architecture` 三条命名门（≤2 下划线 / 测试文件须有同名生产文件 / ≤30 生产文件） | PASS（这是本单新增 `review_filter.go` 的原因，见 Deviations） |
| `pkg/architecture` 全包 | 仅 `TestWebBuildLoadsNoExternalAsset` 失败：fresh worktree 里 `pkg/gateway/dist` 只有 `.gitkeep`（未跟踪的前端产物），与本改动无关；补上主树的 dist 后通过（执行者已如此验证） |

### Reviewer 自己跑的真机验收（tmux + fake provider，9 个模型，`toolcall` 场景）

1. 信任页 → `enter_plan_mode` 审批批准 → `exit_plan_mode` 面板出现 5 行（含 hand-off 与 review 行）。
2. `m` 打开 review picker：问题行 → 空行 → `⌕  Type to filter` 栏（上下各一空行）→ 9 行模型，选中行画 ❯。
3. `send "extra-9"` → 屏上只剩 `deepseek/deepseek-extra-9`（`deepseek-chat`、`extra-2..8` 全部消失，无 `No matches`）。
4. `caret <bar行号>` → `caret=' ' x3 line='    ⌕  extra-9'`（插入点在查询尾部）。
5. `send "zzz"` → `No matches`；`key Enter` → **屏幕逐字节不变**（picker 保持打开，owner 裁决生效）。
6. `key BSpace`×10 → 空栏，`caret <bar行号>` → `caret='T' x3 line='    ⌕  Type to filter'`（占位符首字符上的插入点）。
7. `key Down` → ❯ 回到首行、`caret` 报 `(none in 6 samples)`；`key Escape` → 回到 verdict 列表。
8. hand-off 入口：`3`+Enter 打开 “Which model should carry out this plan?” → 同一过滤栏 → `extra-9` 收窄到 1 行 → 栏内 Enter → `└ Handed off to deepseek/deepseek-extra-9`。
9. 收口：`driver.sh stop`、`tmux kill-session`、临时 home/项目目录已删除。

（review 入口的确认行 `✔ You asked deepseek/deepseek-extra-9 to review the plan` 由执行者真机跑到；
reviewer 用 hand-off 入口 + 单测 `TestPlanReviewPickerFilterEnterReturnsTheVisibleRow` 覆盖同一 Enter 路径。）

### Deviations（已接受，理由）

1. **文件拆分为 `review_filter.go` + `review_filter_test.go`**（计划写的是只改 `overlays.go`
   并新建 `plan_review_filter_test.go`）。原因：`pkg/architecture` 两条硬门——
   `TestTestFilesCorrespondToProductionFiles`（X_test.go 必须有同名 X.go）与
   `TestFileNamesHaveAtMostTwoUnderscores`（`plan_review_filter_test` 有 3 个下划线，超限）。
   执行者把三个过滤方法原样搬进 52 行的 `review_filter.go`（无逻辑变化），测试随之命名。
   判定：最小、可解释、语义仍在范围的适配，接受。
2. **Step 6 的 YAML 补丁脚本有缺陷**：计划里的 python 在每个 `- provider:` 行后追加条目，
   会造成 provider 行缺自己的 `model:`、末条多两份键；执行者改为整块重写 `llm_providers`
   （9 条、同 base_url、不同 model 名），`model:` 计数 9 满足 STOP 门。判定：执行正确，
   计划脚本应在下次修订时改掉（本文件保留原文以便追溯）。
3. **未回填“验收记录”与 `docs/plan` 副本**：按 hand-off 协议由 reviewer 维护——本
   `## Implementation Status` 即验收记录，副本已写入 `docs/plan/PLAN_REVIEW_MODEL_FILTER_PLAN.md`。

### 遗留（不阻塞合并，供 owner 知悉）

- `pkg/architecture` 的 `TestWebBuildLoadsNoExternalAsset` 在“未构建前端”的 fresh worktree
  里必然失败（读不到 `pkg/gateway/dist`），是测试环境前提，不是本改动引入。
- 计划 Step 6 的配置补丁脚本（上文 deviation 2）建议下次修订时替换为整块重写。
- 长查询（超过可用列宽）时 bar 行会溢出，与 `/model` 同一份 `panelField.row` 的既有行为，
  本单按计划不修（维护节已记录）。
