# Plan 178：命令面板的 filter 输入框只有一种高度（根因：高度按"剩余空间"推断）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "func (p slashPanel) fieldRoomy" pkg/tui/render.go` 为 `1`；
> `grep -c "panelFieldRoomyBodyRows = 6" pkg/tui/render.go` 为 `1`；
> `grep -c "func (f panelField) padRow" pkg/tui/render.go` 为 `1`；
> `grep -c "func TestFilterBarIsRoomyOnlyWhereTheListKeepsItsRoom" pkg/tui/overlays_test.go` 为 `1`。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/tui/render.go pkg/tui/overlays_test.go` 必须**无输出**。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 2）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1`（超时给足 900 s）。

## Status

- **Priority**: P1（owner 截图指出：`/skills` 的 filter 框比其它命令面板高一截）
- **Effort**: S（删一套"宽松形态"的 4 处代码 + 改 3 个测试 + 1 个一致性测试）
- **Risk**: LOW（纯布局；最坏是把某个高面板的框变矮——那正是要求）
- **Depends on**: 无
- **Blocks**: `179`（同一个面板族；两者都改 `render.go` 的 panel 布局，建议 178 先做、179 后做以免 diff 冲突）
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: bug（UI 一致性）/ 根因定位

## 根因（已定位，不是猜的）

同一个 `panelField` 有**两种高度**，而用哪种由**剩余行数**算出来：

```go
// pkg/tui/render.go:6829-6833
// panelFieldRoomyBodyRows is how many rows of the body a panel must keep for
// its field to take the roomy form — a blank row above the bar and a padded
// row above and below its text. …
panelFieldRoomyBodyRows = 6
```

```go
// pkg/tui/render.go:6917-6934
func (p slashPanel) fieldRoomy(width, rows int) bool {
	if p.field == nil {
		return false
	}
	if rows <= 0 {
		return true                    // ← 未固定高度的面板：永远宽松
	}
	// The plain frame: head, the bar's one row, the blank under it, the scroll
	// note and the hint; the roomy form adds three rows to it.
	room := rows - p.headRows(width) - 1 - 1 - 1 - p.hintRows(width)
	return room-3 >= panelFieldRoomyBodyRows
}
```

宽松形态 = `padRow` + 文本行 + `padRow`（3 行，外加前面一个空行），紧凑形态 = 1 行
（`layout` 的 `render.go:6970-6986`）。

`rows` 有**三条互不相同的来源**，同一个 widget 于是有三种预算：

| 渲染路径 | `rows` | 出处 |
|---|---|---|
| 会话内的命令面板（overlay） | `slashPanelHeight(height) - 1 = height/2 - 1` | `pkg/tui/reducer.go:4454`（命中测试用同一公式：`pkg/tui/overlays.go:4197`） |
| 输入的斜杠菜单（SlashMenu） | `slashPanelHeight(termHeight) - 2` | `pkg/tui/reducer.go:3831` |
| **setup / 向导页** | `height - len(4 行页头)` ≈ `height - 4` | `pkg/tui/render.go:6352`（`pageLines`） |

按 `fieldRoomy` 反推（head 3 行含 tab bar、hint 1 行）：

| 终端行数 | `rows` | `room` | 形态 |
|---|---|---|---|
| 24 | 11 | 4 | 紧凑（1 行） |
| 30 | 14 | 7 | 紧凑（1 行） |
| **40** | 19 | 12 | **宽松（3 行）** |
| 50 | 24 | 17 | 宽松（3 行） |
| 40（setup 页） | 36 | 29 | 宽松（3 行） |

**所以**：框的高度不是这个 widget 的属性，而是"终端多高 + 面板 head 几行（`/skills` 的 tab bar 多一行）
+ hint 是否折行 + 走哪条渲染路径"的函数。同一个 filter 框，`/skills` 在你这条终端上算出宽松，
另一个面板（head 更高、hint 折行、或终端略矮）算出紧凑——**owner 看到的"不一致"就是这个**。
而且这条翻转**被现有测试写死成"设计"**：

```go
// pkg/tui/overlays_test.go:1946-1960（节选）
func TestFilterBarIsRoomyOnlyWhereTheListKeepsItsRoom(t *testing.T) {
	…
	need := 1 + 3 + 1 + 3 + panelFieldRoomyBodyRows
	for _, tc := range []struct{ rows int; roomy bool }{
		{need, true}, {need - 1, false}, {0, true}, {11, false}, {19, true},
	} { … }
```

**修法的规则**（owner 的要求就是这条）：**一个 filter 框只有一种高度 —— 1 行，任何面板、任何终端、
任何路径都一样**。删掉"按剩余空间变高"的整套机制；表单的答案框（`panelAnswerGlyph`，无列表）也走同一高度，
"高框"若将来真需要，作为**显式的字段属性**另立（见 Maintenance notes）。

## Current state（要改的四处 + 两个测试）

1. `render.go:6829-6833` 常量与注释（`panelFieldRoomyBodyRows`）。
2. `render.go:6866-6871` `padRow`（只为宽松形态存在）。
3. `render.go:6917-6934` `fieldRoomy`；`6926-6943` `fieldRow` / `fieldBar` 里的 `if p.fieldRoomy(...)` 分支。
4. `render.go:6968-6986` `layout` 里的 `roomy := p.fieldRoomy(width, rows)` 与两处 `padRow` 追加。
5. 测试：`overlays_test.go:1946` 的翻转表（必须改写成本计划的规则）；
   `overlays_test.go:1712` 的 `filterBarPlain` 注释与 `:1976` 的 `last-first==2` 断言（宽松形态）；
   以及任何断言面板行数 == `rows` 的用例（**要保留**：面板仍必须填满给定高度）。

## Scope

**In scope（只改这些）**：
- `pkg/tui/render.go`（删宽松形态；`fieldRow`/`fieldBar`/`layout` 简化）
- `pkg/tui/overlays_test.go`（改写受影响的 3 处断言 + 新增 1 个"跨面板一致"测试）

**Out of scope（看着相关也不要动）**：
- `slashPanelHeight`（`termH/2`）与三条 `rows` 来源的**数值**——它们决定面板多高，与本计划无关
- `pickerHead` / `b.tabs` / hint 文案
- `panelField` 的配色、背景、`glyph`、`placeholder`、`caretCol`
- **`179` 的两列布局**（另一个缺陷，另一个计划）
- setup 向导的页头（`pageHeader`/`pageProgressRule`）

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 定向 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'FilterBar\|FieldBar\|PanelLayout' -count=1 -v` | PASS |
| 本包回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | 与基线相比无新增 FAIL |
| 编译 / vet | `CGO_ENABLED=1 go build -tags fts5 ./...`；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` | exit 0 |
| gofmt | `gofmt -l pkg/tui` | 无输出 |
| 真机（Step 4） | `.claude/skills/run-forebrain/SKILL.md` 的 harness（fake provider + tmux），`capture-pane -e` | 见 Step 4 |

## Steps

### Step 1：先把规则写成会红的测试

改 `pkg/tui/overlays_test.go` 的 `TestFilterBarIsRoomyOnlyWhereTheListKeepsItsRoom`，名字与断言都换成本计划的规则：

```go
// The filter bar is one row in every panel, at every height, on every path:
// its height is a property of the widget, not of how many rows are left over.
func TestFilterBarIsOneRowWhateverThePanelHeight(t *testing.T) {
	st := &selectState{options: manyOptions(), label: "pick", inFilter: true}
	st.applyFilter()
	panel := st.panel()
	const width = 60
	for _, rows := range []int{11, 14, 19, 24, 36, 0} {   // 0 = 未固定高度
		first, last := panel.fieldBar(width, rows)
		if first < 0 || last != first {
			t.Fatalf("rows %d: bar spans %d-%d, want exactly one row", rows, first, last)
		}
		layout := panel.layout(width, rows, 0, false)
		if layout.caretRow != first {
			t.Fatalf("rows %d: caret on %d, bar on %d", rows, layout.caretRow, first)
		}
		if rows > 0 && len(layout.lines) != rows {
			t.Fatalf("rows %d: laid out %d rows, the panel must fill the height it is given", rows, len(layout.lines))
		}
		// 紧邻下一行必须是空行（原本的"bar 下一个空行"规则保留），
		// 且 bar 上方不再有 padRow：上一行不得带 bar 背景。
		if rows > 0 && first+1 < len(layout.lines) && layout.lines[first+1] != "" {
			t.Fatalf("rows %d: the row under the bar must stay blank, got %q", rows, stripANSI(layout.lines[first+1]))
		}
	}
}
```

再加一个**跨面板一致**的测试（这是 owner 抱怨的那个"不一致"的回归护栏）：

```go
// Two command panels opened in the same terminal show the same bar: the tab bar
// a picker adds to its head must not change the field's height.
func TestEveryPanelShowsTheSameFilterBar(t *testing.T) {
	const width, rows = 60, 19
	plain := &selectState{options: manyOptions(), label: "Model\nWhich model", inFilter: true}
	plain.applyFilter()
	tabbed := &tabbedPickerState{ /* 用 tabbed picker 的构造方式造两个 tab，见 tabbed_picker_test.go */ }
	… 断言两种情况下的 fieldBar 都是 (n, n) 且 caretRow 都等于 headRows
}
```

（`tabbedPickerState` 的构造照抄 `pkg/tui/tabbed_picker_test.go` 里既有的写法。）

**Verify**：`… -run 'FilterBar|EveryPanelShowsTheSameFilterBar' -count=1 -v` → **两条都 FAIL**
（今天 `rows=19` 返回 3 行）。若有一条直接 PASS，STOP 3。

### Step 2：删掉"宽松形态"

- 删 `panelFieldRoomyBodyRows` 常量与其注释；
- 删 `func (f panelField) padRow`；
- 删 `func (p slashPanel) fieldRoomy`；
- `fieldRow` 变成：`return p.headRows(width)`（有 field 时）；
- `fieldBar` 变成：`text := p.fieldRow(...); if text < 0 { return -1, -1 }; return text, text`；
- `layout` 里删掉 `roomy`、`padRow` 与"head 与 bar 之间那行空行"的两处追加，只保留：
  ```
  if p.field != nil {
      if !p.field.blurred { caretRow/caretCol = … }
      out.lines = append(out.lines, p.field.row(width))
  }
  out.lines = append(out.lines, "")
  ```
- 在 `panelField` 的类型注释上写死这条规则（一句话）：*A picker's bar is one row: its height is a property of
  the widget, never of the rows left over.*

**Verify**：`… -run 'FilterBar|EveryPanelShowsTheSameFilterBar' -count=1 -v` → 两条 PASS。

### Step 3：改掉剩下两处依赖宽松形态的断言

`grep -n "last-first\|roomy\|padRow\|RoomyBodyRows" pkg/tui/*_test.go`：
- `filterBarPlain`（`:1712`）的注释与实现若假设 3 行 bar，改成 1 行；
- `TestClickAnywhereOnTheFilterBarReachesTheFilter`（`:1976`）的 `last-first != 2` 断言改成"点 bar 那一行命中、
  点它**上一行**不命中"（上边界现在是 head/hint，不再是 padRow）。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → 与基线相比无新增 FAIL。

### Step 4（强制）：真机证明"每个面板一样高"

用 `.claude/skills/run-forebrain/SKILL.md` 的 harness（隔离 `FOREBRAIN_HOME` + fake provider + tmux），
在**两个终端高度**（例如 40 行与 24 行）分别打开 `/skills` 与任一有 filter 的面板（例如 `/connect` 或 `/model`），
`tmux capture-pane -e` 后统计**带 bar 背景色的行数**（背景码是 `panelFieldBg` 里的
`\x1b[48;5;<sharedBlockBackgroundColor>m`）：

```bash
# 期望：每个面板每个高度都是 1
grep -c $'\x1b\[48;5;'"$SHARED_BG"'m' skills-40.txt
```

把四组数字（2 面板 × 2 高度）写进执行报告。**任何一组 ≠ 1 就是没修好。**

## Test plan

- 改写 `TestFilterBarIsRoomyOnlyWhereTheListKeepsItsRoom` → `TestFilterBarIsOneRowWhateverThePanelHeight`
  （6 个高度、含 `rows=0` 未固定情形、含"bar 下一行仍是空行"）。
- 新增 `TestEveryPanelShowsTheSameFilterBar`（rich picker 与 tabbed picker 在同一 `width/rows` 下几何一致）。
- 改 `TestClickAnywhereOnTheFilterBarReachesTheFilter` 的边界断言。

## Done criteria

机器可检查，全部满足：

- [ ] `grep -rc "fieldRoomy\|panelFieldRoomyBodyRows\|padRow" pkg/tui/*.go` 全部为 `0`
- [ ] `grep -c "A picker's bar is one row" pkg/tui/render.go` 为 `1`（规则写在类型注释上）
- [ ] 三个测试 PASS；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` 与基线相比无新增 FAIL
- [ ] Step 4 的四组 capture 数字全部为 `1`（写进报告）
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0；`gofmt -l pkg/tui` 无输出
- [ ] `git status --porcelain` 只显示 `pkg/tui/render.go` 与 `pkg/tui/overlays_test.go`
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数 ≠ 1，或 drift check 有输出且"Current state"摘录与现场代码对不上。
2. 基线命令本身跑不起来（编译失败/环境错误），拿不到失败清单。
3. Step 1 的两条新测试里有直接 PASS 的（基线已变）。
4. 有面板**依赖** bar 的 3 行高度做点击命中或光标定位（`grep -n "fieldBar\|fieldRow\|caretRow" pkg/tui/*.go`
   找出全部调用点后判断）：先报告，不要只改一半——半改会让点击落到错的格子。
5. 你发现 `layout` 在删掉 padRow 之后**填不满** `rows`（`len(layout.lines) != rows`）：说明填充发生在更下游，
   报告你看到的补偿逻辑，别在那里加 `" "` 占位糊过去。
6. 表单的答案框（`overlays.go:4814` 的 `panelField{…}`，`panelAnswerGlyph`）看起来确实需要高框：
   **不要**在本计划里给它开例外——先按统一 1 行改完、把它的截图与理由报告给 owner，作为后续决定。
7. 真机 capture 里某面板的行数 ≠ 1 且原因不在 `fieldRoomy`（例如背景被别的东西复用）：
   报告那个第二来源，不要扩大本计划的范围。

## Maintenance notes

- 这条规则的方向是"**UI 的尺寸属于 widget，不属于可用空间**"。谁将来想给某个字段要"高框"，
  必须做成**显式属性**（例如 `panelField.form = true`，由表单页在构造时设置），而不是再按剩余行数推断——
  按空间推断正是本计划的根因。
- 删掉 padRow 之后，面板在"head 与 bar 之间"少一行空气；如果真机上读起来挤，正确做法是给 `layout` 加一个
  **固定**的空行（所有面板一样加），不是恢复按空间变化的形态。
- 与 `179` 的关系：179 改的是同一 panel 布局里"两列行如何折行"，两者都动 `layout`/`columnRows` 附近；
  同一执行者连续做时先做 178，再跑一次 `./pkg/tui` 基线。
