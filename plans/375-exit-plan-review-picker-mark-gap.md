# Plan 375: exit_plan_mode 审批面板的模型列表，`●` 后恒定保留 1 格

> **执行记录**：本计划已实施、评审 APPROVE，并按 owner 指令 **ff-only 合并进 dev**
> （`9552c46c..a6ba8b9f`，2026-11-23；分支与 worktree 已删）。验收证据见文末「验收记录」与
> 「Implementation Status」。

## Status

- **Priority**: P1（owner 直接报的 UI bug，TUI 面板视觉）
- **Effort**: S
- **Risk**: LOW（一处布局函数 + 测试 + 原型说明；无数据、无权限、无接口形状变化）
- **Depends on**: none（刻意取代 `plans/282-…md` 的一条护栏契约，见 §3.5）
- **Category**: bug
- **Planned at**: 首次起草于 `75d293f0`，2026-11-23；其后的 `9552c46c plan files` 只动
  `plans/`，五个 in-scope 文件内容未变（§0 已复核）。
- **Implemented in**: 已合并进 dev —— commit `a6ba8b9f`
  （`fix(tui): keep one cell between the review picker's ● and the endpoint`，
  dev `9552c46c..a6ba8b9f` ff-only；原 worktree `.worktrees/plan-375-exit-plan-mark-gap`
  与分支已删）。
- **Issue**: 无（owner 口头报障 + 截图）

## 0. Drift check（动手前先跑）

**基线只以「这五个文件的内容」为准**：`HEAD` 可以是 `75d293f0` 或它之后的任意提交
（起草之后 owner 又提交了 `9552c46c plan files`），`git status` 里那时遗留的
`plans/370/371/372` + `plans/README.md` 已经随该提交入库、现在是干净树；别的会话也可能在
`plans/` 下留改动。**这些都不算漂移，也不要据此重建基线。** 唯一判据是下面五个文件的
sha256 与表里一致（`9552c46c` 上已复核一致）。

```bash
git rev-parse --short HEAD
git status --porcelain
git diff --stat 75d293f0 -- pkg/tui/overlays.go pkg/tui/chat_session_test.go pkg/tui/review_filter_test.go pkg/tui/overlays_test.go docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html
shasum -a 256 pkg/tui/overlays.go pkg/tui/chat_session_test.go pkg/tui/review_filter_test.go pkg/tui/overlays_test.go docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html
```

起草时（`75d293f0`）的基线哈希，执行者动手前逐条比对：

| 文件 | 起草时 sha256 |
| --- | --- |
| `pkg/tui/overlays.go` | `06ee5a3f4c787da8b458bb227e042659a5357ca73b191e0b7b7ef25b81122e28` |
| `pkg/tui/chat_session_test.go` | `35f1f0ca6c1105821f2f7c9b2de83c684822c706c3ac54ff9715f4032314fd94` |
| `pkg/tui/review_filter_test.go` | `bd2334388890c393cccb789f899ec605d489cef532516d801af7efa9619a82e9` |
| `pkg/tui/overlays_test.go` | `ad6569e8c638b513a4dc85b51e2ea173388a30196cdfccd8e98288d601f9d82b` |
| `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html` | `aef1aa8cac4e86fd258706d56acb4b8b8c3507bf575356ddae4106bd51ac63b0` |

**只有内容不一致才 STOP**；行号漂移不算。（原型文件在本仓库没有冻结哈希契约，也没有测试
钉它 —— 与 `/config` 那对 `plans/config-tui-prototype.*` 不同 —— 所以本次不需要 owner
重新冻结，但基线 sha 仍记下来。）

## 1. 目标（Why this matters）

`exit_plan_mode` 审批面板选「ask another model to review the plan / hand this plan
to another model」后，模型列表是两列：名字（`provider/model`）与说明列（该行的完整请求
URL）。当前会话的模型在自己名字后面带一枚 accent 色的 `●`。

owner 2026-11-23 报障（截图：过滤后只剩两行，第一行就是当前模型）：

```
  deepseek/deepseek-flash ●https://api.deepseek.com/chat/completions
  qdercn/DeepSeek-Flash    https://gateway.qoder.com.cn/algo/api/v2/service/pro/sse/agent_chat_generation
```

`●` 与后面的 URL 之间**一格间距都没有**，图标像粘在 URL 上。要的效果：`●` 后面恒定至少
留 1 格，且第二列（端点列）在各行之间仍然对齐。纯显示缺陷，不影响选择与请求。

## 2. 根因（file:line 证据链 + 现场证据）

### 2.1 现象由三处叠加产生

**（a）带 `●` 的行把图标宽度从间距里扣掉** —— `pkg/tui/overlays.go:1738-1765`
（`planReviewList.rows`，两列非堆叠分支）：

```go
func (l planReviewList) rows(lead string, i int, model turn.PlanReviewModelOption, width int) []string {
	leadW := displayLineWidth(selectorCursorGlyph + " ")
	name := l.names[i]
	mark, markW := "", 0
	if model.Current {
		mark = " " + panelAccentStyle.Render(selectorCheckedGlyph)
		// Measured, never assumed: a locale may draw the ● two cells wide.
		markW = displayLineWidth(" " + selectorCheckedGlyph)
	}
	...
	row := lead + name + mark
	if endpoint != "" {
		pad := maxInt(0, l.colW-displayLineWidth(name)-markW+panelColumnGap)
		row += strings.Repeat(" ", pad) +
			elideEndpoint(endpoint, model.EndpointTail, maxInt(1, width-leadW-l.colW-panelColumnGap))[0]
	}
	return []string{row}
}
```

`panelColumnGap = 2`（`pkg/tui/panel_columns.go:14`）；`panelIndent` / `panelCursor`
都是 2 格（`pkg/tui/render.go:7776`/`:7779`）；`displayLineWidth` 是
`runewidth.StringWidth` 去 ANSI（`pkg/tui/render.go:5209`），且 `pkg/tui/render.go:59`
把 `runewidth.DefaultCondition.EastAsianWidth` 置为 `false`，所以 `●` 量出 1 格、
`markW == 2`（前导空格 + 图标）。

端点列的显示列恒等于 `lead(2) + colW + panelColumnGap(2)`；带 `●` 的行把 `markW(=2)`
从 `pad` 里扣掉 ⇒ `●` 连同它前面那格空格**正好吃满那 2 格间距**。
`pkg/tui/overlays.go:1700-1706` 的注释把这个当成了设计：
「…drawn inside the gap between the columns … flush after the ● when the name fills
its column」。

**（b）`colW` 在非堆叠时恒等于可见行的最宽名字** —— `pkg/tui/overlays.go:1717-1728`：

```go
	widest := 0
	for i, model := range models {
		list.names[i] = planReviewModelDisplay(model.Provider, model.Model)
		widest = maxInt(widest, displayLineWidth(list.names[i]))
	}
	room := (width - displayLineWidth(selectorCursorGlyph+" ")) * 2 / 5
	list.colW = min(min(panelNameColumnMax, widest), maxInt(1, room))
	list.stack = widest > list.colW
```

`colW ≤ widest` 且 `stack = widest > colW` ⇒ 非堆叠时 `colW == widest`。所以**当前模型名
一旦等于最宽名字**，`pad = colW - colW - 2 + 2 = 0`，`●` 后 0 格。

**（c）「过滤时」最容易触发，因为度量只算过滤后的幸存行** ——
`pkg/tui/overlays.go:799` `models := o.planReviewVisible()`，而
`pkg/tui/review_filter.go:46-52` 只返回命中的行：

```go
func (o *approvalOverlay) planReviewVisible() []turn.PlanReviewModelOption {
	out := make([]turn.PlanReviewModelOption, 0, len(o.planReviewFiltered))
	for _, i := range o.planReviewFiltered {
		out = append(out, o.req.PlanReviewModels[i])
	}
	return out
}
```

过滤前列表里有更长的名字，`colW` 被撑大、`●` 后有富余；过滤到
`deepseek/deepseek-flash` + `qdercn/DeepSeek-Flash` 后最宽就是当前模型自己 → 归零。
owner 截图与算术逐格吻合（实测宽度）：
`deepseek/deepseek-flash` = **23** 格、`qdercn/DeepSeek-Flash` = 21 格，于是
`colW = 23`、`pad = 23-23-2+2 = 0`、URL 落在 `lead 2 + 23 + 2 = 27` 列。

### 2.2 现场证据（已在真实代码上复现，不改仓内文件）

两轮独立复核都用 `go test -overlay` / 独立 worktree 探针跑过现有代码，结论一致：

- `pkg/tui/chat_session_test.go:11023` 的 fixture `exitPlanReviewRequest` 名字宽
  `anthropic/claude-opus-4-1 = 25`、`openai/gpt-5.1 = 14`，当前模型即最宽者；
  实测 `colW=25 stack=false markW=2`、`glyphAt=28 urlAt=29`，
  **`●` 与 URL 之间 gap = 0**，渲染为
  `  anthropic/claude-opus-4-1 ●https://api.anthropic.com/v1/messages`。
- 用 owner 截图那组模型（9 行 + 输入 `flash` 过滤）实测 `colW=23 stack=false`，
  未修版本输出与截图逐字节相同
  （`  deepseek/deepseek-flash ●https://api.deepseek.com/chat/completions`）；
  按 §3.2 打补丁后是 `… ● https://…`，两行端点列同列。
- 打补丁后 `pkg/tui` 全量（198s）**只有一条既有测试变红** ——
  `chat_session_test.go:11317` 的 `endpoint column = 30, want 29`，即 Step 1 要改掉的那条旧
  护栏；其余用例全绿。这就是本计划的改动面。
- 既有护栏断言把缺陷钉成了契约：`pkg/tui/chat_session_test.go:11311-11334`

  ```go
	// The name column is the names alone: the endpoint column is the lead plus
	// the widest name plus the fixed gap. …
	if want := displayLineWidth(panelIndent) + displayLineWidth(longest) + panelColumnGap; column != want { … }
	// …and the mark fills that gap rather than pushing the endpoint away: on
	// the row it marks, the URL follows the ● immediately.
	…
	glyphAt := displayLineWidth(marked[:strings.Index(marked, selectorCheckedGlyph)])
	urlAt := displayLineWidth(marked[:strings.Index(marked, "https://")])
	if got := urlAt - glyphAt; got != displayLineWidth(selectorCheckedGlyph) { … }
  ```

  注意口径：`urlAt-glyphAt` 是「`●` 起点到 URL 起点」的位移（含图标自身宽度），
  不是「两者之间的空格数」；gap 要减掉 `displayLineWidth(selectorCheckedGlyph)`。

### 2.3 原始契约出处（本计划刻意取代）

`plans/282-tui-model-lists-match-prototype-01-and-18.md:334-338`：282 首次提交就是因为
「`●` 被算进名字列宽、端点列右移 2 格」被 REVISE，最终按原型 `columnRows` 的
`Math.max(0, colW-nameW-used+2)` 实现，并加了「`●` 与 URL 之间恰为 0 格」的护栏。
缺陷源自原型本身 ——
`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html:249-270`（`columnRows`）：

```js
      const lw=l.lead.w, colW=colWidthOf(l,width), nameW=sw(l.col);
      if(nameW>colW||stack)return stackedRows(l,width);
      const avail=Math.max(1,width-lw-colW-2);
      …
        lead=cat(l.lead,LB.of(l.col));
        const used=l.mark?2:0;
        if(l.mark)lead.add(' ').add(l.mark.glyph,l.mark.style);
        lead.add(rep(' ',Math.max(0,colW-nameW-used+2)));
```

以及 `exit-plan-pick` 场景文案（同文件 `:1660`）：「图标连同前面一格空格正好填进名字列与
说明列之间的 2 格间距，说明列照样对齐」。

### 2.4 竞争解释与本计划排除的理由

| 解释 | 排除依据 |
| --- | --- |
| `●` 实测 2 格、`pad` 算多了一格 | `render.go:59` 关闭 EastAsianWidth，实测 `markW==2`；若量成 2 格会整体右移而不是变 0。 |
| 端点被 `elideEndpoint` 截掉前导字符 | 截断只发生在尾部逻辑，且首字符是 `h`（`https`）；截图里 URL 完整。 |
| 过滤框自己的 pad 行挤压了列表 | 过滤条 3 行在列表之上独立绘制（`overlays.go:805-822`），不参与行内布局。 |
| `writeLine` 的 `wrapCardLine` 折行吃掉空格 | 折行只在超过 `width` 时发生；该行远未到 120 列。 |

### 2.5 同一缺陷的其他出现点（已逐个检查）

- `pkg/tui/panel_columns.go:135-160` 的共享 `columnRows` 也有 mark 分支，但它**只在
  `l.text` 为空（无说明列）时**才画 mark；`richPickerState.panel` 里唯一设 `Mark` 的列表
  （思考强度）在 `pkg/tui/run.go:5559-5568` 就把 `Description` 清空了 ⇒ 不存在「mark
  吃掉列间距」的形态。**不在本计划范围**。
- 堆叠分支（`overlays.go:1748-1757`）把 `●` 留在名字行、端点挂到下一行，无此缺陷。
- 交接（hand-off）选择器与评审选择器**共用**这套渲染（唯一的布局入口是
  `overlays.go:826` 的 `newPlanReviewList` + `:840` 的 `list.rows`），本修复对两者同时
  生效，不需要第二处改动。
- `planReviewVisible()` 还有第三个调用点 `overlays.go:1578`（按键/光标判定，不参与布局），
  不受影响。

## 3. 修复设计

### 3.1 规则（owner 2026-11-23 选定方案 A）

> `●` 收尾名字格：端点列起点取
> `max(名字列宽 + panelColumnGap, 当前模型名字宽 + ● 占位 + 1)`，
> **整列表共用这一个起点**，所以端点列仍然对齐，而 `●` 后面恒定留 1 格。
>
> 第二项只对「当前模型**且有端点**」的行计算（没有端点就什么都不画，别人的端点列不该被白抬）。

只在「带端点的当前模型行名字撑满名字列」时整体右移 1 格；其余情况（无 `●`、`●` 名字更窄、
`●` 行没有端点）完全不变。名字列宽 `colW` 本身不动（名字仍左对齐在同一列，`●` 不能撑宽它）。

### 3.2 代码形状（`pkg/tui/overlays.go`）

```go
type planReviewList struct {
	names  []string
	colW   int // the name column itself: the names alone
	endsAt int // cells from the lead to the endpoint column, marks included
	stack  bool
}
```

`newPlanReviewList` 末尾追加（注释同步改写，删掉「fills that gap」）：

```go
	// The ● closes the name cell of the model in force, so a name that fills
	// the column would hand it the whole gap and the endpoint would touch the
	// glyph: the endpoint column starts one cell past that name cell instead,
	// and every row — marked or not — keeps the one column.
	list.endsAt = list.colW + panelColumnGap
	for i, model := range models {
		if model.Current && strings.TrimSpace(model.Endpoint) != "" {
			list.endsAt = maxInt(list.endsAt, displayLineWidth(list.names[i])+planReviewMarkRoom()+1)
		}
	}
```

新增一个只测一次的小 helper（`rows` 与 `newPlanReviewList` 共用，避免两处重复测量）：

```go
// planReviewMarkRoom is the room the model-in-force mark takes: the space before
// the glyph plus the glyph itself. Measured, never assumed — a locale may draw
// the ● two cells wide.
func planReviewMarkRoom() int { return displayLineWidth(" " + selectorCheckedGlyph) }
```

`rows` 改用 `endsAt`（其余不动）：

```go
	if model.Current {
		mark = " " + panelAccentStyle.Render(selectorCheckedGlyph)
		markW = planReviewMarkRoom()
	}
	…
		pad := maxInt(0, l.endsAt-displayLineWidth(name)-markW)
		row += strings.Repeat(" ", pad) +
			elideEndpoint(endpoint, model.EndpointTail, maxInt(1, width-leadW-l.endsAt))[0]
```

### 3.3 行为变化矩阵

| # | 场景 | 旧行为 | 新行为 | 性质 |
| --- | --- | --- | --- | --- |
| 1 | 带端点的当前模型名 = 最宽名字（含过滤后只剩它） | `●` 后 0 格；端点列 = `lead+colW+2` | `●` 后 1 格；端点列 = `lead+colW+3` | **刻意修订（本次 bug）** |
| 2 | 当前模型名比最宽者窄 | `pad ≥ 1`，端点列 = `lead+colW+2` | 不变（`endsAt` 仍是 `colW+2`） | 不变 |
| 3 | 列表里没有当前模型（无 `●`） | 端点列 = `lead+colW+2` | 不变 | 不变 |
| 4 | 当前模型行没有 `Endpoint`（`overlays_test.go:3574` 的 `handOffReviewRequest` 形状） | 端点列 = `lead+colW+2` | 不变（抬升条件要求该行有端点，别人的端点列不被白抬） | 不变 |
| 5 | 端点列可用宽度（喂 `elideEndpoint`） | `width-leadW-colW-2` | 仅场景 1 少 1 格，其余不变 | 刻意：最宽时可能多省略 1 个字符 |
| 6 | 窄终端整列表堆叠 | 名字行 + 缩进端点行 | 不变（`stack` 判定与 `rows` 的堆叠分支都不碰） | 不变 |
| 7 | 端点为空的行（测试常见的无 `Endpoint` 行） | 只画名字（+`●`） | 不变 | 不变 |
| 8 | 交接选择器（pickHandOff） | 同评审列表 | 同评审列表一起生效 | 刻意 |
| 9 | 原型 `tui-prototype.html` 页 18 的实际渲染 | `●` 后 0 格 | `●` 后 1 格，端点对齐 | 刻意（与实现同步） |

### 3.4 原型同步（owner 2026-11-23 选定「一起改原型」）

不要靠「把 `colWidth` 加 1」来凑 —— `columnRows` 的 `colWidthOf` 会再把 `colWidth` 夹回
`floor((width-lead.w)*2/5)`，当 `nameW` 正好等于该上限时夹回原值、原型仍显示 0 格，而 Go
显示 1 格。**改成把「端点列起点」单独传下去**（默认值与今天逐字节相同，其它 scene 不受
影响；全文件 `colWidth:` 只有 `:312`（/model）与 `:1392` 两处，本轮只碰后者）：

1. `columnRows`（`:249-270`）开头取起点，并让说明列宽度与 pad 都用它：

```js
  const lw=l.lead.w, colW=colWidthOf(l,width), nameW=sw(l.col);
  const endAt=(l.endsAt||colW+2);
  if(nameW>colW||stack)return stackedRows(l,width);
  const avail=Math.max(1,width-lw-endAt);
  …
      lead.add(rep(' ',Math.max(0,endAt-nameW-used)));
```

   续行缩进 `lead=LB.of(rep(' ',lw+colW+2))` 同样换成 `lw+endAt`。

2. `exit-plan-pick` 列表页（`:1384-1396`）算一次并传下去：

```js
    const colW=Math.min(nameW,nameRoom);
    // 当前模型的 ● 收尾名字格：名字撑满列时它不能把 2 格间距吃光，端点列整体
    // 右移一格，让 ● 后面恒定留 1 格（与 pkg/tui/overlays.go 的 planReviewList 同步）。
    let endsAt=colW+2;
    visible.forEach(m=>{if(m.current&&endpoint(m.base,m.path))
      endsAt=Math.max(endsAt,Math.min(sw(modelLabel(m)),colW)+3);});
```

   每行 `lineRows({… colWidth:nameW, endsAt:endsAt …})`；`stack` 判定仍只用 `nameW`。

3. 场景文案（`:1660`）把「图标连同前面一格空格正好填进…2 格间距」换成第 4 条给出的统一
   措辞。

4. 同一份文件里还有两处同源旧契约文案（复核发现，改代码不改它们会自相矛盾）。
   两处都用下面这句统一措辞，别自己另编，免得三处口径不一：

   > 当前会话模型在名字后面标 `●`（accent 110）：名字列不因它变宽；端点列在它后面恒定留
   > 1 格，其余行仍与它同列对齐。

   - `:119` 附近「渲染契约」卡片（契约正本）—— 把「图标连同前面一格空格吃进…2 格间距」
     整句换成上面这句；
   - `:265-266` `columnRows` 里 mark 分支的注释（本轮正在改这段代码）—— 同上。

**不动** `plans/config-tui-prototype.html` / `plans/config-tui-prototype.test.cjs`
（冻结对 `e5245ee3…` / `6bfe73e1…`）。

**顺带留痕（不改）**：原型 `nameRoom=(width-4)*2/5`（`:1385`）与 Go
`room=(width-2)*2/5`（`overlays.go:1724`）本就差 2 格，属既有偏差，不在本计划处理。

### 3.5 与 plan 282 的关系（owner 2026-11-23 裁决 A：只声明取代，不改历史计划）

`plans/282-…md:334-338` 的「`●` 与 URL 之间恰为 0 格」护栏，以及 `:43-48`、`:328` 的
`●https://…` 样例渲染，**自本计划起失效**。282 原文一字不改（执行记录是历史事实），
并把「哪几行已失效」写进 §11 维护说明。282 的其余部分
（两列、无序号、`●` 标当前、堆叠规则）仍然有效。

## 4. Commands you will need

| 用途 | 命令 | 成功预期 |
| --- | --- | --- |
| 编译 | `CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` | exit 0 |
| 定点测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestExitPlanReviewPicker|TestPlanReviewPicker|TestPlanReviewList' -count=1` | 修复前上述用例 FAIL；修复后 `ok`（**不要**写成 `'A\|B'`：RE2 里 `\|` 是字面竖线，会得到 `[no tests to run]` 的假绿） |
| 包级回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | `ok`（约 200s，`pkg/tui` 全量含 200s 级用例） |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` | exit 0 |
| gofmt | `gofmt -l cmd pkg third_party plugins` | 不打印任何文件 |
| 全量 vet（可选） | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" && CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0 |

（`third_party/pdfium` 在本工作区已存在；`make test` 会先自检原生依赖。）

## 5. Scope

**In scope（只允许改这些文件）**

- `pkg/tui/overlays.go` —— `planReviewList` 加 `endsAt`、`newPlanReviewList` 算它、
  `rows` 用它、新增 `planReviewMarkRoom()`，并改写三处 doc comment。
- `pkg/tui/chat_session_test.go` —— 只改 `TestExitPlanReviewPickerRendersTwoColumns`
  （`:11284-11335`）的两条断言与注释。
- `pkg/tui/review_filter_test.go` —— 新增「过滤到只剩当前模型」回归测试（该文件已有
  `planReviewFilterModels` / `planReviewFilterRequest` / `planReviewFilterOpen` /
  `planReviewFilterType` / `planReviewFilterLines` 助手）。
- `pkg/tui/overlays_test.go` —— 新增 `newPlanReviewList` 的 `endsAt` 单元断言。
- `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html` —— §3.4 的原型同步。
- `plans/375-exit-plan-review-picker-mark-gap.md`（本计划）与 `plans/README.md` 索引行
  （七列：`| [slug](file) | 标题 | 类别 | 状态 | 优先级 | 工作量 | 风险 |`，照
  `README.md:2123-2125` 的格式）。

**Out of scope（明确不碰）**

- `pkg/tui/panel_columns.go` —— `panelColumnGap` 与共享 `columnRows` 的 mark 分支
  （只服务无说明列的行，无此缺陷，§2.5）。
- `pkg/tui/overlays.go` 的思考强度第二屏（`planEffortPicking`，`"High ⚙"` 紧贴名字）——
  owner 2026-11-15 明确要求「tight, never out in the description column」。
- `plans/config-tui-prototype.html` 与 `plans/config-tui-prototype.test.cjs`（冻结原件）。
- `frontend/**` 与 `pkg/gateway/**` —— web 审批卡的模型列表**不是两列**：端点带着
  （`pkg/gateway/api_extra.go:4587` 的 `Endpoint: option.Endpoint`）但挂在名字**下面**
  `frontend/src/components/chat/PlanApprovalDock.vue:125-127`，当前模型用右侧 chip（`.cur`）
  而不是列内 `●` ⇒ 没有列，就没有「列间距」可保留，本缺陷在 web 不存在。
  （`plans/282-…md:314` 那句「web 卡不显示端点」已过时，别再引它。）
- `review_filter.go` 的过滤/排序逻辑与 `planReviewVisible` 语义。
- 原型里 `nameRoom` 与 Go `room` 的既有的 2 格差异（§3.4 留痕）。
- 任何 git commit（owner 规则：改动留工作区，计划文件禁止 commit）。

## 6. Steps

### Step 1（先复现，红态）：把护栏断言改成正确契约，并加过滤场景回归测试

> 本步**只用现有符号**（`displayLineWidth(" "+selectorCheckedGlyph)` 写字面量），
> 不要引用 `planReviewMarkRoom()`／`list.endsAt` —— 它们在 Step 2 才存在，否则编译不过、
> 拿不到红态。

1. `pkg/tui/chat_session_test.go` 的 `TestExitPlanReviewPickerRendersTwoColumns`
   （`:11284`）第一条断言（`:11311-11319`）改成「lead + 带标记的名字格 + 1 格」：

```go
	// The endpoint column clears the model-in-force mark by one cell: the ●
	// closes the name cell, and a name that fills the column would otherwise
	// hand it the whole gap.
	if want := displayLineWidth(panelIndent) + displayLineWidth(longest) +
		displayLineWidth(" "+selectorCheckedGlyph) + 1; column != want {
		t.Fatalf("endpoint column = %d, want %d (lead + marked name cell + 1)\n%s", column, want, strings.Join(lines, "\n"))
	}
```

2. 第二条断言（`:11326-11334`）改成显式 gap（口径见 §2.2），注释删掉
   「the URL follows the ● immediately」：

```go
	// …and the ● keeps one cell of its own before the endpoint: the mark ends
	// the name cell instead of eating the column gap.
	glyphAt := displayLineWidth(marked[:strings.Index(marked, selectorCheckedGlyph)])
	urlAt := displayLineWidth(marked[:strings.Index(marked, "https://")])
	if gap := urlAt - glyphAt - displayLineWidth(selectorCheckedGlyph); gap < 1 {
		t.Fatalf("%d cells stand between the ● and the URL, want at least 1: %q", gap, marked)
	}
```

3. `pkg/tui/review_filter_test.go` 新增 owner 截图那条路径的回归测试（9 行 ⇒ 过滤条出现；
   输入 `flash` ⇒ 两行，且带标记的那行是最宽者）：

```go
// 9 models so the filter bar is shown (pickerFilterMinRows == 8); typing
// "flash" leaves the two rows the owner's screenshot shows, and the marked row
// is the widest of them — the case where the ● used to touch the endpoint.
func TestPlanReviewPickerKeepsASpaceAfterTheMarkWhenTheFilterNarrowsToIt(t *testing.T) {
	const (
		currentEndpoint = "https://api.deepseek.com/chat/completions"
		otherEndpoint   = "https://gateway.qoder.com.cn/algo/api/v2/service/pro/sse/agent_chat_generation"
	)
	models := planReviewFilterModels(7) // short opencode-go fillers
	models = append(models,
		turn.PlanReviewModelOption{ID: "current", Provider: "deepseek", Model: "deepseek-flash",
			Current: true, Endpoint: currentEndpoint, EndpointTail: "/chat/completions"},
		turn.PlanReviewModelOption{ID: "other", Provider: "qdercn", Model: "DeepSeek-Flash",
			Endpoint: otherEndpoint},
	)
	overlay := planReviewFilterOpen(t, planReviewFilterRequest(t, models))
	planReviewFilterType(t, overlay, "flash")
	lines := planReviewFilterLines(t, overlay, 120)
	marked, plain := "", ""
	for _, line := range lines {
		if strings.Contains(line, currentEndpoint) {
			marked = line
		}
		if strings.Contains(line, otherEndpoint) {
			plain = line
		}
	}
	if marked == "" || plain == "" {
		t.Fatalf("both filtered rows must render:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(marked, selectorCheckedGlyph+" "+currentEndpoint) {
		t.Fatalf("the ● must clear the endpoint by at least one cell: %q", marked)
	}
	at := func(line, endpoint string) int {
		return displayLineWidth(line[:strings.Index(line, endpoint)])
	}
	if a, b := at(marked, currentEndpoint), at(plain, otherEndpoint); a != b {
		t.Fatalf("endpoint columns differ: %d and %d\n%s", a, b, strings.Join(lines, "\n"))
	}
}
```

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestExitPlanReviewPicker|TestPlanReviewPicker' -count=1`
→ **红**，且红的内容必须是：

```
endpoint column = 29, want 30 (lead + marked name cell + 1)
0 cells stand between the ● and the URL, want at least 1: "  anthropic/claude-opus-4-1 ●https://api.anthropic.com/v1/messages"
the ● must clear the endpoint by at least one cell: "  deepseek/deepseek-flash ●https://api.deepseek.com/chat/completions"
```

（前两条来自 `chat_session_test.go` 的 fixture：`lead 2 + 名字 25 + gap 2 = 29`；
修好后是 `2 + 25 + 2 + 1 = 30`。）若红的内容不是这三条，停下核对 —— 说明现场已漂移。

### Step 2：实现（`pkg/tui/overlays.go`）

按 §3.2 改：加 `endsAt` 字段、`newPlanReviewList` 算它（含「该行有端点」条件）、
`rows` 用 `endsAt` 做 pad 与 `elideEndpoint` 预算、新增 `planReviewMarkRoom()` 并把
`markW` 换成它、改写 `planReviewList` / `newPlanReviewList` / `rows` 三处 doc comment
（删掉「drawn inside the gap / flush after the ●」）。

同时把 Step 1 里第 1 条的 `displayLineWidth(" "+selectorCheckedGlyph)` 换回
`planReviewMarkRoom()`（保持一份测量）。

**Verify**:
`CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` → exit 0；
`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestExitPlanReviewPicker|TestPlanReviewPicker' -count=1` → 全绿。

### Step 3：`endsAt` 单元断言（`pkg/tui/overlays_test.go`）

断言里**不要数字符数**（起草时就把 `deepseek/deepseek-flash` 数成 22，实际 23）：
用 `displayLineWidth(名字)` 作独立基准，规则写成 `名字宽 + panelColumnGap + 1`。

```go
// The endpoint column clears the ● by one cell, and only a marked row with an
// endpoint widens it; the name column itself never grows.
func TestPlanReviewListReservesOneCellAfterTheMark(t *testing.T) {
	const marked = "deepseek/deepseek-flash" // 23 cells
	models := []turn.PlanReviewModelOption{
		{Provider: "deepseek", Model: "deepseek-flash", Current: true, Endpoint: "https://api.deepseek.com"},
		{Provider: "qdercn", Model: "DeepSeek-Flash", Endpoint: "https://gateway.qoder.com.cn"},
	}
	list := newPlanReviewList(models, 120)
	if list.colW != displayLineWidth(marked) {
		t.Fatalf("colW = %d, want the widest name alone (%d)", list.colW, displayLineWidth(marked))
	}
	if want := displayLineWidth(marked) + panelColumnGap + 1; list.endsAt != want {
		t.Fatalf("endsAt = %d, want %d (one cell after the ●)", list.endsAt, want)
	}

	// A marked row that is not the widest name leaves the column where it was.
	const widest = "anthropic/claude-sonnet-4-6"
	narrow := []turn.PlanReviewModelOption{
		{Provider: "qdercn", Model: "DeepSeek-Flash", Current: true, Endpoint: "https://gateway.qoder.com.cn"},
		{Provider: "anthropic", Model: "claude-sonnet-4-6", Endpoint: "https://api.anthropic.com"},
	}
	if got, want := newPlanReviewList(narrow, 120).endsAt, displayLineWidth(widest)+panelColumnGap; got != want {
		t.Fatalf("endsAt = %d, want the name column plus the gap (%d)", got, want)
	}

	// A marked row without an endpoint draws nothing after its name, so it must
	// not move anyone else's endpoint either.
	noEndpoint := []turn.PlanReviewModelOption{
		{Provider: "deepseek", Model: "deepseek-flash", Current: true},
		{Provider: "qdercn", Model: "DeepSeek-Flash", Endpoint: "https://gateway.qoder.com.cn"},
	}
	if got, want := newPlanReviewList(noEndpoint, 120).endsAt, displayLineWidth(marked)+panelColumnGap; got != want {
		t.Fatalf("endsAt = %d, want the name column plus the gap (%d)", got, want)
	}
}
```

（`provider/model` 的显示串是 `planReviewModelDisplay` 拼的 `provider+"/"+model`，
所以上面 `widest`/`marked` 常量与列表里那一行的名字逐字相同。）

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestPlanReviewList -count=1` → ok。

### Step 4：原型同步（`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html`）

按 §3.4 改**五处**：`columnRows` 用 `l.endsAt||colW+2`（连同它 `:265-266` 的注释）、
页 18 算 `endsAt` 并逐行传下去、场景文案（`:1660`）、渲染契约卡片（`:119`）。

**Verify**（可机器复跑，不依赖肉眼）：

1. 语法：把内嵌 `<script>` 抽出来做 `node --check`
   （`python3 -c` 抽出 `<script>` 之间的文本到临时文件即可），退出码 0。
2. `grep -c "endsAt" docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html` ≥ 4
   （`columnRows` 里 2 处 + 页 18 的计算与传参）。
3. `grep -n "吃进\|填进" docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html`
   → **无命中**（旧契约文案三处都已改写）。
4. 视觉确认由 owner 侧做（这是 docs 原型，无 CI 测试）：在浏览器切到 `exit-plan-pick`，
   当前模型行是 `… ● https://…`（1 格）、两行端点列对齐，`exit-plan` / `exit-plan-filter`
   / `exit-plan-handoff` / `exit-plan-effort` 与 `/model` 页渲染不变。

### Step 5：包级回归 + vet + gofmt

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1
CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui
gofmt -l cmd pkg third_party plugins
```

**Verify**: 测试 `ok`；vet exit 0；gofmt 无输出。（复核已实测：打完补丁后 `pkg/tui` 全量
只有 `chat_session_test.go:11317` 那条断言会红 —— 就是 Step 1 改掉的那条。）

### Step 6：清理复核残留（条件式，可跳过）

复核过程在 `.worktrees/` 下留过两个临时 worktree（`plan-review-3073747284`、
`plan-review-1155587840`），里面是探针测试 `zz_*_test.go`、打过补丁的 `overlays.go` 与两处
`third_party` 软链。**它们含未跟踪/已修改文件，`git worktree remove` 不带 `--force` 会拒绝
（实测 `exit=128`）**；而且这两个目录在执行前很可能已经不存在 —— 所以写成条件式：

```bash
for wt in plan-review-3073747284 plan-review-1155587840; do
  if git worktree list | grep -q "/.worktrees/$wt "; then
    git worktree remove --force ".worktrees/$wt"
  fi
done
git worktree list
```

删除前先确认 §2.2 的探针输出已抄进验收记录 —— 目录一删，那些现场就只剩会话转录了。

**Verify**：`git worktree list` 里若上面两个目录在执行前存在，现在已不出现（原本就不存在则
跳过，不算失败）；主工作区 `git status --porcelain` 与 Step 5 之前一致（主树 `third_party/`
未被改动）。**不要动**别的会话留下的 `plan-review-1613339929` / `-2965910376`。

## 7. Test plan

- **改写**：`TestExitPlanReviewPickerRendersTwoColumns`（`chat_session_test.go:11284`）
  —— 现有 fixture 的当前模型就是最宽名字，天然覆盖 bug 前置状态；两条断言按 §3.1 新契约。
- **新增**：`TestPlanReviewPickerKeepsASpaceAfterTheMarkWhenTheFilterNarrowsToIt`
  （`review_filter_test.go`）—— owner 报障的完整用户路径（9 行 → 过滤框 → 输入 `flash`
  → 两行，当前模型最宽）→ `●` 后 1 格且两端点同列。
- **新增**：`TestPlanReviewListReservesOneCellAfterTheMark`（`overlays_test.go`）——
  纯布局三例：带端点的最宽当前模型、当前模型更窄、当前模型无端点
  （后两例都等于「最宽名字 + `panelColumnGap`」）。断言按 §6 Step 3 的表达式写，
  不要在测试里写死数字。
- **回归靠既有用例**：`TestExitPlanReviewPickerMarksCurrentWithGlyph`（`:11343`）、
  `TestExitPlanOverlayRendersPickerAndCollectedReviews`（`:11202` 的
  `"claude-opus-4-1 ●"`）、`TestExitPlanReviewPickerStacksWhenNarrow`、
  `TestPlanReviewPickerKeepsTheSelectionOnScreen`（21 行无端点）、
  `review_filter_test.go` 的过滤/键位族、`overlays_test.go` 的评审/交接决策族 —— 必须保持绿。
- 结构范式：新测试照 `review_filter_test.go` 现有用例（助手 + 渲染 + `stripANSI`）。

## 8. 真机验收（tmux，必做；测试绿不等于真机对）

用 `.claude/skills/run-forebrain/` 的驱动（`driver.sh` + `fake_provider.py`）与隔离
`FOREBRAIN_HOME`；`toolcall` 模式会真的走 `enter_plan_mode → exit_plan_mode` 并弹出审批面板。

1. **A/B 取证（先旧后新）**
   - 旧二进制（修复前 `$D build` 的 `75d293f0` 版）：`$D start toolcall` → 在「请其他模型
     评审」行上按 `m` 进选择器 → 过滤到 `deepseek-flash` → `$D screen`：应出现
     `●https://api.deepseek.com/chat/completions`（缺陷形态）。
   - 新二进制（Step 2 后 `$D build`）：同一路径同一过滤 → 必须是
     `● https://api.deepseek.com/chat/completions`（1 格），两行端点起始列相同。
   - 模型行用 `model_seed.sh` 预置 ≥8 行（`pickerFilterMinRows` 阈值，有过滤条才会出现
     过滤框）：当前会话模型 `deepseek/deepseek-flash` + `qdercn/DeepSeek-Flash` + 6 个短名
     filler。
   - **前置校验**：先确认 `●` 真的落在当前会话模型那行（`Current` 来自会话模型行）。
     若不是，改抓那个实际带 `●` 的行：把它做成过滤后最宽的名字（过滤词选只命中它的子串）
     即可复现同一缺陷 —— 缺陷条件是「带 `●` 的行是最宽名字」，与具体名字无关。
2. **回归场景**：不输入过滤（列表含更长名字）时，带 `●` 的行端点列与修复前一致。
3. **交接场景**（同一渲染的另一 purpose）：从 verdict 行进交接选择器，同样确认 `●` 后有
   1 格。
4. 验收结束 `$D stop`，删临时 home 与临时项目目录，**不污染真实 `~/.forebrain*`**。

> 若驱动无法让 fake provider 走到 exit_plan_mode（工具不同步），按 skill 的「失败即报告」
> 处理：**判 BLOCKED，把阻断点、已经跑到的屏幕、以及手跑步骤写进报告上报**，不改用别的
> 证据顶替、不伪造、也不用真实 key 的 home。`go test -overlay` 探针只能证明渲染函数的输出
> 字符串，它证不了 120 列终端里的实际排布 —— 可以作为补充证据附上，但**不能当成真机验收
> 的替代**。

## 9. Done criteria（全部机器可查）

- [x] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestExitPlanReviewPicker|TestPlanReviewPicker|TestPlanReviewList' -count=1` 全绿，且输出**不含** `[no tests to run]`、含 `TestPlanReviewListReservesOneCellAfterTheMark` 的 `--- PASS`。
- [x] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` ok（198.6s）。
- [x] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` exit 0；`gofmt -l cmd pkg third_party plugins` 无输出。
- [x] `grep -n "flush after the ●\|fills that gap" pkg/tui/overlays.go` 无命中。
- [x] `grep -c "planReviewMarkRoom" pkg/tui/overlays.go` = 4（定义 + `newPlanReviewList` + `rows` + doc）。
- [x] `grep -n "endsAt" pkg/tui/overlays.go` 命中 5 处（字段、赋值、抬升、pad、elide 预算）。
- [x] `grep -n "used=l.mark?2:0" docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html` 仍存在（共享 `columnRows` 的 mark 语义不动）。
- [x] `grep -c "endsAt" docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html` = 5，页 18 已把 `endsAt` 传给 `lineRows`。
- [x] `grep -n "吃进\|填进" docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html` **无命中**。
- [x] 抽取内嵌 `<script>` 后 `node --check` 退出码 0（一个 `<script>` 块）。
- [x] `git worktree list` 里那两个复核 worktree 执行前就不存在，未触碰别的会话的 `plan-review-1613339929` / `-2965910376`。
- [x] `git status --porcelain` 只有 §5 In scope 的 5 个文件（无未跟踪文件）。
- [x] 真机 A/B 证据已记进文末「验收记录」（tmux 实跑；列号为 `capture-pane` 实测）。
- [x] `plans/README.md` 的 375 行已写；`ls plans | grep -E '^375-'` 只有本计划一份。

## 10. STOP conditions

- §0 的 in-scope 基线哈希与现场不符（内容漂移）→ 停下来重新取证，不要凭行号判断。
- Step 1 跑出的红不是 §6 Step 1 列出的那几条（数字或文案不同）→ 现场已漂移，停下核对。
- 修完后场景 1 之外的行端点列也右移 → 说明把 mark 算进了 `colW` 而不是 `endsAt`，回 §3.2 重做。
- `planReviewMarkRoom()` 在本机量出 ≠ 2（`●` 被当宽字符）→ 停产报告：断言必须按实测宽度
  写，「1 格」的语义要重新确认。
- 窄终端堆叠用例转红 → 说明 `stack` 判定被连带改动，回滚到只动 `endsAt` 的方案。
- 修复需要改 `panel_columns.go`、`review_filter.go`、`frontend/**` → 超出 Scope，停下来问。
- `git worktree remove` 报 `exit=128`（残留含未跟踪/已修改文件）→ 用 §6 Step 6 的
  `--force`，但**只删**那两个 `plan-review-*` 复核 worktree。
- 若执行者额外跑了全量 `./...`（本计划不要求），会看到另一包 `pkg/architecture` 的既有红
  `TestTestFilesCorrespondToProductionFiles`（`pkg/tui/deliverables_outcome_test.go` 无配对
  产物，自 `3ff6b134` 起就红，不是本计划引入）→ 记录、不改、不并进本计划的判据。
- 真机驱动无法到达 exit_plan_mode 审批面板 → 判 BLOCKED 上报，不伪造验收证据、不用
  overlay 顶替。

## 11. Maintenance notes

- `planReviewMarkRoom()` 是「`●` 占多少格」的**唯一**测量点：`newPlanReviewList` 与 `rows`
  都必须用它，不要在任何地方另写 `displayLineWidth(" "+…)` 或写死 2 格。
- 评审列表与**交接列表共用**一套 `planReviewList` 渲染；以后新增第三种 purpose 仍只改这一处，
  别复制第二份布局。
- 端点列的预算常量从 `colW+panelColumnGap` 变成列表级的 `endsAt`。以后若给这两列再加右侧
  track（value/status，参照 `panelLine.trailingTrackWidth`），预算必须一起扣 `endsAt`，
  `elideEndpoint` 的 width 也要同步。
- 改 `panelColumnGap`（`pkg/tui/panel_columns.go:14`）会**同时**影响 `/model` 与本列表 ——
  这是既有「一份列规则」约定（`plans/282-…md:315`），不要为本列表单开常量。
- **已失效的旧契约**（owner 2026-11-23 裁决 A：只声明取代、不改历史计划）：`plans/282-…md`
  的 `:338`（「`●` 与 URL 之间恰为 0 格」护栏）与 `:43-48`、`:328` 的 `●https://…` 样例渲染。
  读 282 的人看到 `●https://` 要按本计划的口径理解，别照抄。
- 原型 `columnRows` 的 `used=2` 语义**保留**（它服务无说明列的行，当前无缺陷），页 18 靠
  传入 `endsAt` 得到正确间距。若将来出现第二处「mark + 说明列」，必须把这条 1 格规则搬进
  helper 本身，而不是再传一个参数。
- 原型里描述这条规则的文案有三处（渲染契约卡片 `:119`、`columnRows` 注释 `:265-266`、
  `exit-plan-pick` 场景 `:1660`）；以后改规则要三处一起改，否则契约源自相矛盾。
- 原型 `nameRoom=(width-4)*2/5` 与 Go `room=(width-2)*2/5` 的 2 格差异是既有偏差，未处理。
- web 侧没有这条缺陷的结构基础（卡片是「名字 + 端点上下两行 + 右侧 current chip」，
  `pkg/gateway/api_extra.go:4587` + `frontend/src/components/chat/PlanApprovalDock.vue:125-127`）；
  若以后 web 也改成两列，这条 1 格规则要一起搬过去。

## 验收记录

### 渲染层 A/B（reviewer 亲自复跑，`-overlay` 把整包换回 `9552c46c`）

同一个探针用例（owner 报障那两行模型），只切换 `pkg/tui` 的四个文件：

| 版本 | 渲染出的行 | `●` 与 URL 之间 |
| --- | --- | --- |
| 旧（`9552c46c` 的 `overlays.go` + 旧测试） | `  deepseek/deepseek-flash ●https://api.deepseek.com/chat/completions` | **gap=0**（owner 截图形态） |
| 新（worktree 实现） | `  deepseek/deepseek-flash ● https://api.deepseek.com/chat/completions` | **gap=1** |

命令（旧侧）：`go test -tags fts5 -overlay <map> ./pkg/tui -run TestZZGapProbe -v`，
`map` 把 `pkg/tui/{overlays.go,chat_session_test.go,overlays_test.go,review_filter_test.go}`
换成 `9552c46c` 版（探针文件本身不入库，用完即删）。

### 真机（tmux，执行器实跑；`driver.sh` + fake provider，隔离 `FOREBRAIN_HOME`）

10 行模型表，当前模型 `deepseek/deepseek-chat`：

| 场景 | 旧二进制 | 新二进制 |
| --- | --- | --- |
| 不过滤 | 端点列 @31，带 `●` 行 gap=5 | **与旧完全一致**（矩阵第 2 行：名字不是最宽时不变） |
| 过滤 `deepseek` | `  deepseek/deepseek-chat ●http://127.0.0.1:8741/chat/completions`，**gap=0** | `● http://…`，**gap=1**，两行端点同列 @27 |
| 交接选择器（过滤 `deepseek`） | — | `● http://…`，gap=1，两行 @27 |

列号为 `capture-pane` 的 0-based 显示列（含面板 body 的 4 格缩进），两侧可逐格对比。
首次真机被阻断的原因（已解决、不改产品代码）：driver 的 scratch 项目不在任何 git 仓库内，
被 `DefaultProjectTypeForRoot` 判为 **office**，于是 `enter_plan_mode` 未注册
（`unknown tool "enter_plan_mode"`）；在 scratch 项目里 `git init` 后即判 coding。

### 未做的验证

- 原型页 18 的浏览器目视确认（docs 原型无 CI 测试）；已机器核对语法与三处文案改写。
- reviewer 未重跑 tmux A/B（上表来自执行器实跑；渲染层 A/B 由 reviewer 亲自复跑）。

## Implementation Status

- **Verdict**: APPROVE（执行模型 `alibaba/deepseek-v4.1-flash-1017`，本会话以 tech lead 身份
  派发并评审）
- **Merged**: commit `a6ba8b9f`，dev `9552c46c..a6ba8b9f` **ff-only**
  （owner 指令「合并回主树 dev 分支」），未 push；原 worktree
  `.worktrees/plan-375-exit-plan-mark-gap` 与分支已删（`git branch -d` 报 FULLY_MERGED）。
  合并后主树门禁复跑：定点 `pkg/tui` 用例 ok、`go build ./cmd/forebrain` BUILD_OK、
  `go vet ./pkg/tui` exit 0、`gofmt -l` 无输出。
- **Steps**: §0 drift check 通过（五个文件 sha256 全等基线）；Step 1 红态复现（两条预期红，
  第三条因同一用例先 `t.Fatalf` 中止而未打印，内容与计划逐字相符）；Step 2 实现 + 定点测试转绿；
  Step 3 单元断言；Step 4 原型五处同步；Step 5 包级回归 198.6s 全绿 + vet + gofmt；
  Step 6 条件式跳过（两个复核 worktree 执行前已不存在）。
- **Deviation（已记录，判可接受）**: 执行器未提交（计划 §5 把「任何 git commit」列进
  Out of scope，与派工的「按计划 git 流程提交」冲突时以计划为准）；执行器未动
  `plans/375-…md` 与 `plans/README.md`（派工要求 reviewer 维护 backlog）。
- **Files changed**: `pkg/tui/overlays.go`、`pkg/tui/chat_session_test.go`、
  `pkg/tui/review_filter_test.go`、`pkg/tui/overlays_test.go`、
  `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html`（+141/−40，5 文件，零越界）。
- **Checks run by reviewer**: 定点 21 条用例全绿（含新回归）；`./pkg/tui -count=1` ok
  198.6s；`go vet`、`gofmt` 干净；done-criteria 的 grep 全部符合；原型 `<script>`
  `node --check` 退出码 0；`git status` 仅 5 个 in-scope 文件；渲染层 A/B（见验收记录）
  旧 gap=0 / 新 gap=1。
- **Not done / left to owner**: 原型页 18 的目视确认（docs 原型，无 CI 测试）。