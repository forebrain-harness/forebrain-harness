# Plan 179：两列列表在名字过长时"挂在自己名下"折行（Qwen 计划/区域选择器的乱因）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "func (l panelLine) columnRows" pkg/tui/panel_columns.go` 为 `1`；
> `grep -c "panelNameColumnMax = 40" pkg/tui/panel_columns.go` 为 `1`；
> `grep -c "func TestPanelColumnsLongNameTakesItsOwnRow" pkg/tui/panel_columns_test.go` 为 `1`；
> `grep -c "Standard API Key for China" pkg/tui/setup.go` 为 `1`。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/tui/panel_columns.go pkg/tui/render.go pkg/tui/setup.go pkg/tui/panel_columns_test.go` 必须**无输出**。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 2）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1`。
>
> **建议先做 `178`**（同一个 panel 布局族，178 删掉 bar 的宽松形态），再跑一次基线。

## Status

- **Priority**: P1（owner 截图：Qwen Cloud 计划/区域选择器的布局"太乱"）
- **Effort**: S–M（`columnRows` 的折行规则 + 每列表一次决策 + 4 条选项文案 + 测试）
- **Risk**: LOW–MED（改的是**所有**两列列表的折行，不只这一个选择器）
- **Depends on**: 建议在 `178` 之后（两者都改 panel 布局；178 先落地更省冲突）
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: bug（布局）+ 设计（这一个选择器的 UX）

## 根因（已定位）

两列行在"名字比名字列宽"时的兜底，把描述**挂到了描述列下面**，而不是名字自己下面：

```go
// pkg/tui/panel_columns.go:59-104（节选）
func (l panelLine) columnRows(width int) []string {
	…
	leadW := displayLineWidth(l.lead)
	colW := min(l.colWidth, maxInt(1, (width-leadW)*2/5))
	nameW := displayLineWidth(l.col)
	pad := strings.Repeat(" ", leadW+colW+panelColumnGap)      // ← 描述列的左边缘
	…
	if nameW <= colW { … 正常两列 … }
	// 名字太长：名字独占若干行
	rows := make([]string, 0, len(texts)+1)
	for i, chunk := range wrapPanelWords(l.col, maxInt(1, width-leadW)) { … }
	for _, chunk := range texts {
		rows = append(rows, pad+render(chunk))                // ← 描述仍然从描述列开始
	}
	return rows
}
```

`/skills` 那类列表还算凑合（描述短），但 **Qwen 计划/区域选择器**的四条 `Label` 是 40–56 字符的句子，
在一台 75 列的终端上 `colW = min(40, (75-2)*2/5) = 29`，于是每一行都变成：

```
❯ Standard API Key for China (pay-as-you-go)
                                 Endpoint: dashscope.aliyuncs.com        ← 33 空格缩进
  Standard API Key for Global/Intl (pay-as-you-go)
                                 Endpoint: dashscope-intl.aliyuncs.com
```

四组两行、第二行浮在屏幕中间偏右——这就是 owner 说的"太乱"。**这条行为被现成测试写死成"设计"**：

```go
// pkg/tui/panel_columns_test.go:45-57（节选）
// A name wider than the narrowed column takes rows of its own and the
// description starts under the column, so the column edge stays put at every
// width.
func TestPanelColumnsLongNameTakesItsOwnRow(t *testing.T) { … }
```

**修法的规则**：名字放不进名字列时，这一行退化成**纵向一对**——名字（必要时折行），
描述**挂在名字下面（`leadW` 缩进）**，永不缩进到描述列。并且**整张列表一起退化**（同一个节奏），
不是有的行两列、有的行纵向。

## 现成材料（照抄，别另造）

- 谁在用两列：`overlays.go:5114`（命令面板 picker）、`tabbed_picker.go:275`（`/skills` 这类带 tab 的）、
  `project_servers.go:132`（项目服务器确认）。
- 名字列宽：`richPickerState.nameWidth()`（`overlays.go:5131-5139`）= 最宽 label，上限 `panelNameColumnMax`（40）；
  tabbed 侧是 `tabbed_picker.go:121`。
- 折行：`wrapPanelWords`；缩进：`panelIndent`（2 格）；列间距：`panelColumnGap`（2 格）。
- 竖排的既有先例（**这就是要的形态**）：`layout` 里 `p.head` 与 `b.text` 走的 `panelLine.rows`——
  续行统一挂在 `leadWidth` 之下（`render.go:6748-6769`）。

## Scope

**In scope（只改这些）**：
- `pkg/tui/panel_columns.go`（`columnRows` 的兜底 + 一个"整表是否退化"的判定）
- `pkg/tui/render.go`（`panelLine.rows` 增加竖排模式；`slashPanel.layout` 里对 body 预扫一次决定退化）
- `pkg/tui/setup.go`（Qwen Cloud 四条选项的 `Label`/`Hint` 改写，见 Step 4）
- `pkg/tui/panel_columns_test.go`（改写长名字用例 + 新增 2 个）
- `pkg/tui/overlays_test.go`（若因布局变化需要更新既有 `filterBarPlain` 等断言，只改到等价语义）

**Out of scope（看着相关也不要动）**：
- `panelNameColumnMax` / `panelColumnGap` / `panelIndent` / `slashPanelHeight` 的数值
- `178` 的 filter bar 高度
- 其它 provider 的 `Label`/`Hint`（它们短，不触发退化）
- 网页（工作台/聊天）的任何布局
- `providerGroupBlurb`（"4 plans by region and billing" 已经说清了）

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 定向 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'PanelColumns' -count=1 -v` | PASS |
| 本包回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | 与基线相比无新增 FAIL |
| 编译 / gofmt | `CGO_ENABLED=1 go build -tags fts5 ./...`；`gofmt -l pkg/tui` | exit 0 / 无输出 |
| 真机 | `.claude/skills/run-forebrain/SKILL.md`（隔离 home + fake provider + tmux）+ `capture-pane -e` | 见 Step 5 |

## Steps

### Step 1：先改测试，把"挂在描述列下面"判成错

把 `TestPanelColumnsLongNameTakesItsOwnRow` 改成：

```go
// A name that does not fit its column turns the row into a stacked pair: the
// name on its own rows, the description hung under the NAME — never indented
// to the description column, which would float it in the middle of the panel.
func TestPanelColumnsLongNameStacksUnderTheName(t *testing.T) {
	rows := panelLine{
		lead:     "  ",
		col:      "Standard API Key for China (pay-as-you-go)",
		colWidth: 29,
		text:     "dashscope.aliyuncs.com",
	}.rows(75)
	want := []string{
		"  Standard API Key for China (pay-as-you-go)",
		"  dashscope.aliyuncs.com",
	}
	…
}
```

再加两条：

```go
// The whole list stacks when any name cannot fit: a list must not mix a
// two-column row with a stacked one, or its left edge stops being one edge.
func TestPanelColumnsStackAsOneList(t *testing.T) { … }

// Short names keep the two columns untouched at the same width.
func TestPanelColumnsKeepColumnsWhenEveryNameFits(t *testing.T) { … }
```

**Verify**：`… -run 'PanelColumns' -count=1 -v` → 第一条与"整表"那条 FAIL，第三条 PASS。

### Step 2：`columnRows` 的兜底改成挂在名字下

`pkg/tui/panel_columns.go`：

- 拆出 `columnRowsStacked(width int) []string`：名字按 `width-leadW` 折行（续行缩进 `leadW`），
  描述按 `width-leadW` 折行、**同样缩进 `leadW`**、用 `l.style`（若有）；
- `columnRows`：`nameW <= colW` 走原两列（**一个字节都不改**），否则 `return l.columnRowsStacked(width)`；
- 把文件头那段 "the description starts under the column" 的注释改成本计划的新规则。

**Verify**：`… -run 'PanelColumns'` 的前两条仍红（还差"整表"决策）。

### Step 3：整表一次决策

在 `slashPanel.layout`（`render.go`）里，画 body 之前扫一遍：

```go
	// A list picks one rhythm: when any row's name cannot fit its column at this
	// width, every row stacks, so the left edge of each row is the same edge.
	stackColumns := false
	for _, l := range p.body {
		if l.colWidth <= 0 || l.col == "" {
			continue
		}
		leadW := displayLineWidth(l.lead)
		colW := min(l.colWidth, maxInt(1, (width-leadW)*2/5))
		if displayLineWidth(l.col) > colW {
			stackColumns = true
			break
		}
	}
```

`panelLine` 增加 `stackColumns bool`（或给 `rows` 加一个参数，二选一，**不要两种都做**），
body 渲染时把它传下去：`l.rowsStacks(width, stackColumns)`。

**Verify**：`… -run 'PanelColumns'` → 三条全 PASS。

### Step 4：Qwen Cloud 四条选项的文案（这一个选择器的 UX）

现在四条 `Label` 是四句近义长句，两个正交维度（**计费方式** × **区域**）被压进散文，
`Hint` 又在每行重复 "Endpoint:"。改成：

```go
{Label: "Standard · China",              Hint: "dashscope.aliyuncs.com",        …},  // pay-as-you-go
{Label: "Standard · Global",             Hint: "dashscope-intl.aliyuncs.com",   …},
{Label: "Coding Plan · China",           Hint: "coding.dashscope.aliyuncs.com", …},  // subscription
{Label: "Coding Plan · Global",          Hint: "coding-intl.dashscope.aliyuncs.com", …},
```

并把面板的第二行（`SelectRich` 的 label 参数）改成一句话，说明两个维度各是什么：

```go
idx, ok, err = s.cfg.Selector.SelectRich(
	group.Label+"\nStandard is pay-as-you-go; Coding Plan is a subscription.\nThe endpoint is shown under each.",
	methods, method)
```

（`providerChoice.Provider` / `DefaultBaseURL` / `SupportsAPIPath` / `APIKeyLabel` **一个字节都不改**——
本步只动给人看的字。）

**Verify**：`grep -c "Standard API Key for China" pkg/tui/setup.go` 为 `0`；
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` 与基线相比无新增 FAIL。

### Step 5（强制）：真机三个宽度

同一台机器，把终端调到 **60 / 75 / 120 列**，各打开一次 Connect → Qwen Cloud 的计划/区域页，
`tmux capture-pane -e` 存盘。判定：

- 60 列与 75 列：四条 = **8 行**（名字 1 行 + 端点 1 行），**每个续行都从第 3 列开始**（`panelIndent` + 1），
  没有任何一行以 ≥10 个空格开头；
- 120 列：四条 = **4 行**两列（名字列 40 + 间隙 2 + 端点），左边缘对齐；
- 三个宽度都**没有**折行到半个单词、也没有超出面板宽度（`assertRowsFit` 的等价检查：每行显示宽度 ≤ 宽度）。

把三份 capture 的行数与首列偏移写进执行报告。可用 `.claude/skills/tui-render-ab-verify/SKILL.md`
的 SGR 取证手法做 A/B（改动前后各一份）。

## Test plan

- `TestPanelColumnsLongNameStacksUnderTheName`（本计划的规则）。
- `TestPanelColumnsStackAsOneList`（同一 width 下短名字与长名字混排 → 全部竖排）。
- `TestPanelColumnsKeepColumnsWhenEveryNameFits`（120 列下仍是两列，防止"一律竖排"的假修复）。
- 既有 `TestPanelColumnsAlignDescriptions` / `…WrapInsideTheDescriptionColumn` / `…WithoutDescriptionMatchPlainRows` 保持不动且必须仍 PASS。

## Done criteria

机器可检查，全部满足：

- [ ] `grep -rc "columnRowsStacked" pkg/tui/panel_columns.go` 为 `1`
- [ ] `grep -c "Standard API Key for China" pkg/tui/setup.go` 为 `0`；`grep -c "Coding Plan · Global" pkg/tui/setup.go` 为 `1`
- [ ] `… -run 'PanelColumns'` 全 PASS；`./pkg/tui` 与基线相比无新增 FAIL
- [ ] Step 5 的三份 capture 符合 60/75 → 8 行且续行第 3 列、120 → 4 行两列（写进报告）
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0；`gofmt -l pkg/tui` 无输出
- [ ] `git status --porcelain` 只显示 Scope 里的文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数 ≠ 1，或 drift check 有输出且"Current state"摘录与现场代码对不上。
2. 基线命令本身跑不起来（编译失败/环境错误），拿不到失败清单。
3. 120 列下也退化竖排（`colW` 计算与 `nameWidth` 上限不匹配）：先报告算术，再改——一律竖排不是修复。
4. 你把 `stackColumns` 的判定放进 `panelBuilder`（build 期）却发现那里**拿不到 width**：那就按 Step 3
   放在 `layout`（paint 期）。**不要**为它引入一个假的宽度常量。
5. 项目服务器确认面板（`project_servers.go:132`）因为整表退化而变得难读：报告它的实际 capture，
   由 owner 决定是给它单独保留两列（显式属性）还是接受竖排。
6. 你想顺手改 `panelNameColumnMax` 到 60 来"让长名字放得下"：**不要**——那只是把这个缺陷推到更长的标签上。

## Maintenance notes

- 规则一句话：**两列只在"每一个名字都放得下"时成立；否则整表竖排，续行挂在名字下。**
  谁将来加"第三列"（例如行尾的计数），同样要走"整表一个节奏"的判定。
- `columnRows` 的"名字放得下"分支是既有行为（对齐的描述列），本计划只改兜底分支；
  评审时确认那一段 diff 为空。
- 与 `178` 的关系：178 让 filter bar 只有一行，179 让两列行只有一种折行；两者合起来是"面板的几何由
  内容与宽度决定，不由剩余空间决定"。
