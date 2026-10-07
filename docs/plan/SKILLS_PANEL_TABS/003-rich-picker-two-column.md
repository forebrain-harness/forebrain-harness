# Plan 003：通用 `SelectRich` 选择器改成名称/说明两列布局

> **执行者须知**：按步骤顺序执行。每一步都要运行对应的验证命令，确认结果符合预期后再进入下一步。一旦出现"STOP 条件"中的任何情况，立即停下来报告，不要自行发挥。完成后更新 `docs/plan/SKILLS_PANEL_TABS/README.md` 中本计划的状态行。**禁止 `git commit`**：所有改动留在工作区，由 owner 手动提交。
>
> **前置条件**：计划 002 状态必须为 DONE。先运行 `grep -n 'func (b \*panelBuilder) selectableColumns' pkg/tui/panel_columns.go && grep -n 'panelNameColumnMax = 40' pkg/tui/panel_columns.go`，两条都必须有输出；任何一条没有输出就 STOP。
>
> **漂移检查（第一步先做）**：`git diff --stat 88eb320 -- pkg/tui/overlays.go`。制定计划时 `overlays.go` 相对 `88eb320` 没有改动，本计划只改其中的 `richPickerState.panel`。如果输出非空，对照下文"现状"中的摘录，按函数名定位并确认 `panel` 的内容与摘录一致；不一致就 STOP。开始前执行 `git diff > /tmp/skills-003-before.diff` 留底。

## 状态

- **优先级**：P2
- **工作量**：S
- **风险**：LOW（只改一个渲染函数；没有说明的列表逐字保持原样）
- **依赖**：`docs/plan/SKILLS_PANEL_TABS/002-skills-panel-tabs-two-column.md`
- **类别**：dx（用户界面）
- **制定于**：commit `88eb320` + 计划 001/002，2026-10-07

## 为什么要做

owner 2026-10-07 要求："把通用 `SelectRich` 选择器（/permissions、/migrate、/resume、setup 等）也改成两列布局纳入本期计划"。

通用富选择器现在把每一行渲染为 `名称 — 说明`，说明折行后回到行首，与 /skills 改造前是同一个问题。计划 002 已经提供了两列排版原语 `panelBuilder.selectableColumns`，以及名称列上限 `panelNameColumnMax`。本计划让通用选择器也使用它们，使所有斜杠选择器呈现一致：名称左对齐，说明左对齐，两列间距固定为 2 格，说明在自己的列内完整折行。

## 现状

### 受影响的调用方

只需改 `richPickerState.panel` 一处，调用方不用改。下面是所有经过这个函数的生产调用点（按函数名定位，行号仅供参考）：

| 调用点 | 列表 | 有无说明 |
|---|---|---|
| `pkg/tui/commands.go` `handlePermissions` | /permissions 预设 | 有 |
| `pkg/tui/commands.go` `handleMigrate`（两处） | /migrate 来源、会话范围 | 有 |
| `pkg/tui/run.go` `runSlashPicker` | 引擎提供的斜杠选择器：/model、推理强度、/agent（说明为 workspace 路径）、/skills 的"Pick one to run it"等 | 视 `turn.PickerItem` 而定：模型名和推理强度没有说明 |
| `pkg/tui/run.go` resume 选择器（`SelectRichPaged` / `SelectRich`） | /resume | **没有**（标签为 `"%-8s %s"` 格式的"时间 + 标题"） |
| `pkg/tui/setup.go`（3 处） | 首次设置：Channels、Provider、计划/区域 | 有 |
| `pkg/tui/chat_slash.go` | per-skill 详情页的 4 个动作、Add a skill 来源、安装范围 | 有 |

（`setup.go` 中有别人未提交的改动；本计划**不修改** `setup.go`，它只是渲染路径的受益者。）

### 关键摘录

`pkg/tui/overlays.go` 中 `richPickerState.panel`（制定时约在行 4431-4460）：

```go
// panel lays the rich picker out as a slash panel: the label and the filter
// in the head, the matching items grouped under their categories' headings,
// each with its description, the ❯ on the focused one, and the keys.
func (p *richPickerState) panel(label string) slashPanel {
	b := newPanelBuilder()
	pickerHead(b, label)
	category := ""
	for ci, idx := range p.selectable {
		item := p.items[idx]
		if item.Category != category && item.Category != "" {
			b.group(item.Category)
			category = item.Category
		}
		text := item.Label
		if desc := strings.TrimSpace(item.Description); desc != "" {
			text += " — " + desc
		}
		b.selectable(ci == p.cursor, "", text)
	}
	if len(p.selectable) == 0 {
		b.text(panelIndent, "No matches", nil)
	}
	b.hint(selectFooterHint(false, p.esc))
	panel := b.panel()
	panel.field = p.filter.panelField()
	return panel
}
```

计划 002 交付的原语（`pkg/tui/panel_columns.go`）：

- `func (b *panelBuilder) selectableColumns(selected bool, lead, name string, nameWidth int, desc string)`；
- `panelLine.columnRows` 的规则：
  - 名称列宽 = `min(nameWidth, (width-leadW)*2/5)`；
  - 名称放得下时与说明并排，说明在说明列内折行；名称放不下时单独占行，说明从下一行的说明列开始；
  - **没有说明时与普通行逐字相同**。
- `const panelNameColumnMax = 40`。

### 必须保持的既有行为（已有测试锁定）

- `pkg/tui/overlays_test.go` 的 `TestPickersShowEveryRowWhole`（约行 1417，断言在 1462-1471）：40 列宽下，富选择器不得截断任何一行（不出现 `...`），长说明里的 `without asking first` 必须完整可读。本计划之后它必须**不加修改**地通过。
- 分组标题（`Category`）继续保留为组标题，**不改成 tab**。tab 只用于 /skills（计划 002）。
- 过滤、排序、光标、分页加载（`SelectRichPaged` 的 `fetchMore`）的行为都不变。

## 需要用到的命令

| 用途 | 命令 | 成功时的预期 |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| vet | `go vet ./pkg/tui` | exit 0 |
| 新测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1 -run 'RichPicker'` | ok |
| TUI 全量 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | ok |

## 范围

**允许修改或新建的文件**：

- `pkg/tui/overlays.go`：只改 `richPickerState.panel` 及其注释。
- `pkg/tui/rich_picker_columns_test.go`（新建）。

**不在范围内**：

- 所有调用方（`commands.go`、`run.go`、`setup.go`、`chat_slash.go`）：不需要改。
- 字符串选项的 `Select` / `MultiSelect`（`selectState`）：没有说明字段，不受影响。
- 计划审阅批准浮层中的模型行（`overlays.go` 的 `planReviewModelRow`，它在模型名后用 `" — "` 接模型目录条目的名字 `Label`，当前项还会再追加 `" (current)"`）：它不是 `SelectRich`，没有被点名，保持原样。
- `slashMenuPanel`（`run.go`）：它本来就是两列，不迁移。
- /resume 标签 `"%-8s %s"` 中的补齐空格会被 `wrapPanelWords` 压成一个。这是现有行为，本计划保持原样，不修。

## Git 工作流

- 不建分支、不提交、不推送。结束时用 `git diff -- pkg/tui/overlays.go` 自查：新增改动只在 `richPickerState.panel` 内。

## 步骤

### 第 1 步：`richPickerState.panel` 改用两列

把循环和注释改为下面的形状：

```go
// panel lays the rich picker out as a slash panel: the label and the filter
// in the head, the matching items grouped under their categories' headings,
// each in two columns — the label, then its description, each left-aligned a
// fixed gap apart — the ❯ on the focused one, and the keys.
func (p *richPickerState) panel(label string) slashPanel {
	b := newPanelBuilder()
	pickerHead(b, label)
	nameWidth := p.nameWidth()
	category := ""
	for ci, idx := range p.selectable {
		item := p.items[idx]
		if item.Category != category && item.Category != "" {
			b.group(item.Category)
			category = item.Category
		}
		b.selectableColumns(ci == p.cursor, "", item.Label, nameWidth, strings.TrimSpace(item.Description))
	}
	... // 其余不变
}

// nameWidth is the name column for every row the picker could show: the
// widest label of all its items, not just the ones matching the filter, so the
// description column holds still while the user types; at most
// panelNameColumnMax.
func (p *richPickerState) nameWidth() int {
	width := 0
	for _, item := range p.items {
		if item.Disabled {
			continue
		}
		width = maxInt(width, displayLineWidth(item.Label))
	}
	return minInt(width, panelNameColumnMax)
}
```

要点：

- `nameWidth` 在每次 `panel` 调用时重新计算。这样 `SelectRichPaged` 追加新的一页之后列宽会随之更新，计算量可以忽略。
- 包内没有 `minInt` 就用内置 `min`；`maxInt` 已经存在（`reducer.go:3174`，同包可直接用）。
- 不要保留 `" — "` 分支，也不要再加"没有说明时走 `selectable`"的特判，因为 `columnRows` 已经保证没有说明的行与普通行逐字相同。

**验证**：
- `CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` → exit 0
- `grep -n 'text += " — " + desc' pkg/tui/overlays.go` → 无输出

### 第 2 步：测试

新建 `pkg/tui/rich_picker_columns_test.go`，写法参考 `overlays_test.go` 约行 1462 的富选择器测试（构造 `richPickerState`、调用 `rebuildSelectable()`、对 `panel(label).layout(width, 0, 0, false).lines` 用 `stripANSI` 去除 ANSI 后断言；注意包里同时存在测试助手 `stripANSI`（`chat_session_test.go:16047`）和生产函数 `stripAnsi`（`reducer.go`），都能编译，测试统一用 `stripANSI`）：

1. `TestRichPickerLaysRowsInTwoColumns`：items 为 `{Label: "Default", Description: "Asks before edits and commands outside the workspace."}` 和 `{Label: "Read only", Description: "Never writes."}`，`cursor = 0`，label 为 `"Permissions\nWhat may run without asking"`，宽度 80。逐行必须**完全等于**：

   ```go
   []string{
   	"  Permissions",
   	"  What may run without asking",
   	"› Type to filter",
   	"",
   	"❯ Default    Asks before edits and commands outside the workspace.",
   	"  Read only  Never writes.",
   	"",
   	"  ↑/↓ to navigate · Enter to confirm · Esc to cancel",
   }
   ```

   （`Default` 后补 4 个空格 = 列宽 9 − 名称宽 7 + 间距 2。）并断言没有任何一行包含 `" — "`。
2. `TestRichPickerAlignsDescriptionsAcrossGroups`：items 为 `{Label: "gpt-5", Description: "the default model", Category: "OpenAI"}` 和 `{Label: "deepseek-chat", Description: "fast and cheap", Category: "DeepSeek"}`，label 为 `"Model"`，宽度 80。逐行必须完全等于：

   ```go
   []string{
   	"  Model",
   	"› Type to filter",
   	"",
   	"  OpenAI",
   	"❯ gpt-5" + strings.Repeat(" ", 10) + "the default model",
   	"",
   	"  DeepSeek",
   	"  deepseek-chat  fast and cheap",
   	"",
   	"  ↑/↓ to navigate · Enter to confirm · Esc to cancel",
   }
   ```

   这验证了两个组共用同一个名称列（列宽 13），说明从同一列开始。
3. `TestRichPickerWithoutDescriptionsKeepsItsRows`：模拟 /resume，两个没有说明的 items：`"just now New conversation"`，以及 `"2h ago   " + strings.Repeat("a long conversation title ", 4)`。在宽度 80 和 30 下，body 中每个条目的行都必须与计划 002 之前的写法 `panelLine{lead: <同一 lead>, text: item.Label}.rows(width)` 逐字相等。lead 取值：被选中行为 `panelCursor`，其余为 `panelIndent`。比较时两边都用 `stripANSI`。
4. `TestRichPickerColumnHoldsStillWhileFiltering`：沿用测试 1 的 items，把 `p.filter = filterField{text: "read", cursor: 4}` 后调用 `rebuildSelectable()`。`Read only` 那一行必须等于 `"❯ Read only  Never writes."`——列宽仍按全部 items 计算，为 9，而不是只按剩下的这一项。
5. `TestRichPickerRowsFitAtEveryWidth`：用测试 1 和测试 2 的 items，在宽度 120、80、50、30 下调用 `assertRowsFit`（`run_test.go` 中已有这个辅助函数），并断言不出现 `"…"`、`"..."`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1 -run 'RichPicker'` → ok。测试名里包含 `RichPicker` 的已有测试（例如 `TestArrowStartsOnFirstOption_RichPickerFilterReset`）也在其中，必须通过。

### 第 3 步：全量回归与真机核对

1. `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → ok。`overlays_test.go` 中约行 1462 的富选择器测试必须不加修改地通过。
2. 真机核对，用 `.claude/skills/run-forebrain/driver.sh`（先读它的 SKILL.md）：

   ```bash
   D=.claude/skills/run-forebrain/driver.sh
   $D reset; $D start hang
   $D submit '/permissions'; $D screen; $D key Escape
   $D submit '/resume';      $D screen; $D key Escape
   $D stop
   ```

   **预期**：
   - /permissions 的每个预设名都在左列，说明从同一列开始，续行也从这一列开始，没有 ` — `；
   - /resume 的行与改动前一样（新会话里可能只有一条或显示为空，以实际为准）；
   - 把两次 `$D screen` 的输出附在报告里。

   setup 向导需要全新的 `FOREBRAIN_HOME`，driver 不会进入它，所以不做真机核对，由第 2 步的面板测试覆盖。在报告里注明这一点。

## 测试计划

- 新增 `pkg/tui/rich_picker_columns_test.go`，含 5 个测试（第 2 步）。
- 不修改任何已有测试；已有测试必须原样通过。

## 完成标准

以下全部成立：

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0
- [ ] `go vet ./pkg/tui` exit 0
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` ok
- [ ] `grep -n 'text += " — " + desc' pkg/tui/overlays.go` 无输出
- [ ] 相比 `/tmp/skills-003-before.diff`，新增改动只在 `pkg/tui/overlays.go` 的 `richPickerState.panel` / `nameWidth` 内，外加新建的测试文件
- [ ] 第 3 步的真机截屏已附在报告中
- [ ] README 状态行已更新

## STOP 条件

遇到以下情况，停下来报告，不要自行处理：

- 计划 002 未完成（`selectableColumns` 或 `panelNameColumnMax` 不存在）。
- `overlays_test.go` 中已有的富选择器测试，或任何 setup / permissions / migrate / resume 相关测试失败。不要通过修改这些测试来让它们通过——这说明两列排版改变了某个列表的既有语义。
- `TestRichPickerWithoutDescriptionsKeepsItsRows` 失败。这说明计划 002 中"没有说明时与普通行逐字相同"的规则没有正确实现，应回到 002 修复，不要在本计划里加特判。
- 需要修改任何调用方文件，或修改 `render.go` / `panel_columns.go`。

## 维护说明

- 以后新增的 `SelectRich` 调用方会自动获得两列布局，只要提供 `Label` / `Description`，不需要自己拼接。
- 审阅重点：说明很长的列表（例如 /agent 的说明是 workspace 路径）在窄屏下应在说明列内完整折行；名称超过列宽（约为行宽的五分之二）的行，说明会从下一行的说明列开始。
- 已知未做：/resume 标签的补齐空格被折行函数压缩（现有行为）。如需修复，应让 resume 把时间放进 `Description`、标题放进 `Label`，这属于行为变更，需另行立项。
