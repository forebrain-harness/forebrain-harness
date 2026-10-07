# Plan 002：/skills 四个列表改成 tabs + 名称/说明两列布局

> **执行者须知**：按步骤顺序执行。每一步都要运行对应的验证命令，确认结果符合预期后再进入下一步。一旦出现"STOP 条件"中的任何情况，立即停下来报告，不要自行发挥。完成后更新 `docs/plan/SKILLS_PANEL_TABS/README.md` 中本计划的状态行。**禁止 `git commit`**：所有改动留在工作区，由 owner 手动提交。
>
> **前置条件**：计划 001 状态必须为 DONE。先运行 `grep -n 'func (o Origin) Label' pkg/skill/origin.go`，必须有输出；没有输出就 STOP。
>
> **漂移检查（第一步先做）**：本计划基于"`88eb320` + 计划 001 的改动"制定。先运行 `git diff --stat 88eb320 -- pkg/tui/render.go pkg/tui/overlays.go pkg/tui/channels.go pkg/tui/chat_slash.go pkg/tui/commands_test.go pkg/tui/run_test.go pkg/tui/chat_session_test.go`——输出非空是**预期**的（制定计划时工作区里就有并行改动，实施 001 时还会再变）。`pkg/tui/chat_slash.go` 的行号会因为 001 删除了函数而前移，所以下文**一律按函数名定位**，行号仅供参考。逐一对照"现状"中的摘录与实际代码；只要有一处对不上，就按 STOP 条件处理。

## 状态

- **优先级**：P1
- **工作量**：M
- **风险**：LOW-MED（纯 TUI 呈现层；`render.go` 中有 owner 未提交的改动，必须小心只做最小编辑）
- **依赖**：`docs/plan/SKILLS_PANEL_TABS/001-skill-origin-one-rule.md`
- **类别**：dx（用户界面）
- **制定于**：commit `88eb320` + 计划 001，2026-10-07

## 为什么要做

`/skills` 面板把 300 多个 skill 排成一个长列表，按来源分组；每一行都是 `名称 — 说明`，长说明折回到行首。结果是名称和说明搅在一起，根本扫不出名字来（owner 截图："布局非常乱"）。「Enable / disable skills」多选更糟，每项是 `name - desc [source] (shadowed)` 拼成的一整串。「Import from Memory」多选同样如此，每项是 `name - desc [state] (adds /cmd, shadows <path>)`；被拦截的提案则以同样格式堆在标题下方。

owner 的要求：

- 分组改成 **tabs**；
- 名称和说明之间不要用 `—` / `-` 连接，而是**两列**：名称左对齐，说明左对齐，两列之间间距固定。

owner 已拍板（2026-10-07）：

- 四个列表都改：/skills 主面板、「Improve a skill」选择器、「Enable / disable skills」多选，以及 Manage →「Import from Memory」多选（最后这个是 owner 当天追加的）。Import from Memory 只有一类条目，所以不显示 tab 条，只用两列布局；
- skill tab 按加载优先级排列（Project · Agent · Shared · Cross-tool · Built-in），空 tab 隐藏；**Manage 放在最后**；
- 打开面板时停在**第一个非空的 skill tab**；Tab / Shift+Tab 切换 tab（与 /status 一致）。

批准的效果示意（当前 tab 用强调色加下划线，与 /status 相同）：

```
  Skills
  Run an installed skill, or add, create, and tune skills
  Project 51   Shared 1   Cross-tool 6   Built-in 18   Manage
› Type to filter

❯ golang-benchmark    Golang benchmarking, profiling, and
                      performance measurement. Use when …
  golang-cli          Golang CLI application development.
                      Use when building, modifying, or …

  ↓ 140 more
  Tab to switch · ↑/↓ to navigate · Enter to confirm · Esc to cancel
```

（示意里的 `…` 只表示"此处省略"。实际界面中说明必须在说明列内**完整折行**，禁止截断或省略号，这是 owner 规则 R-full。）

## 现状

### 相关文件

- `pkg/tui/channels.go:1395-1428`：`SelectItem`、`Selector` 接口，以及可选能力接口 `PagedRichSelector`（**照抄这个模式**）。
- `pkg/tui/overlays.go`：
  - `richPickerState`、`rebuildSelectable`、`panel`、`selectRichPaged`（约行 4373-4560）：通用富选择器，`panel` 里用 `" — "` 把名称和说明拼成一串；
  - `selectState` + `rawSelector.MultiSelect`（约行 3530-3610、3965-4070）：多选；
  - `pickerMatchRank`（约行 4282）：过滤打分；
  - `pickerHead`、`selectFooterHint`（约行 3589-3608）；
  - 字形常量（约行 3475-3477）：`selectorCursorGlyph = "❯"`、`selectorCheckedGlyph = "●"`、`selectorUncheckedGlyph = "○"`。
- `pkg/tui/render.go`（**有 owner 未提交的改动**）：`panelLine` 与 `rows`（约行 7325-7350）、`slashPanel.layout`（约行 7420-7490）、`panelBuilder` 与 `selectable`（约行 7523-7637）、`buildStatusPanel` 的 tab 行（约行 7639-7665）、`panelTabStyle`（约行 7305）、`panelIndent = "  "`、`panelCursor`、`composerPromptMarker = "› "`（行 87）、`displayLineWidth`（会忽略 SGR 转义）、`wrapPanelWords`、`wrapCardLine`。
- `pkg/tui/run.go:3143-3166`：`slashMenuPanel`。这是**现成的"名称列 + 说明列"先例**：名称按最宽者补齐、间距 2，说明折行后悬挂在说明列下。
- `pkg/tui/chat_slash.go`：`handleSkills`、`buildSkillsMenu`、`sortSkillEntries`、`skillEntryCategory`、`skillEntryDescription`、`handleSkillEntry`、`handleSkillToggle`、`handleSkillImprove`；以及 Import from Memory 相关的 `importFromMemoryAction`（约行 2059）、`handleMemorySkillImport`（约行 2061-2143）、`memorySkillLabel`（约行 2145-2167）、`memorySkillStateLabel`（约行 2169-2186，保留不动）。
- `pkg/tui/notify.go:1591-1611`：`MemorySkillOption`（`Name`、`Description`、`Status`、`SlashCommand`、`Shadows`（路径）、`Blocked`（原因）、`Promoted`、`LoadedInSession`）。只读，不修改。
- 测试：`pkg/tui/commands_test.go`（`scriptedSelector`、`richStep`、`skillsTestSession`，以及按下标驱动 /skills 的 4 个测试），`pkg/tui/run_test.go`（约行 3925 起，按下标驱动的后台安装测试；`assertRowsFit` 约在行 4187），`pkg/tui/chat_session_test.go` 中的 Import from Memory 测试（约行 20659-20870：`memoryImportSelector`、它的 `pick` 辅助函数、`TestMemoryImport*`、`TestSkillsMenuOffersMemoryImportOnlyWhenProposalsExist`、`TestMemorySkillLabelDistinguishesPromotedFromActive`）。**这个文件里有别人未提交的改动**（集中在约行 2108-16300），本计划只允许修改其中两个函数，见"范围"。

### 关键摘录

`pkg/tui/channels.go:1418-1427`（可选能力接口的先例）：

```go
// PagedRichSelector is an optional Selector capability for pickers backed by a
// paginated store. ... Implemented by Forebrain Harness's
// interactive selector without widening the general Selector interface used by
// callers; line-based selectors keep the plain SelectRich single-page behavior.
type PagedRichSelector interface {
	SelectRichPaged(label string, items []SelectItem, defaultIdx int, fetchMore func() []SelectItem) (int, bool, error)
}
```

`Selector` 接口有 13 个实现（2 个生产实现 + 11 个测试替身），所以**不能**给 `Selector` 加方法。能力探测的写法见 `pkg/tui/run.go:4801` 一带：`if pagedSelector, canScroll := selector.(PagedRichSelector); canScroll && canPage { ... s.SelectRichPaged(...) }`；探测不中（或 `canPage` 为假）就走 `selector.SelectRich(...)`（约行 4823）。

`pkg/tui/overlays.go` 中 `richPickerState.panel`（问题所在）：

```go
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
```

`pkg/tui/render.go` 中 `panelLine`（行内容按宽度折行，续行悬挂在 `lead` 宽度之后；`lead` 本身**不折行**）：

```go
type panelLine struct {
	lead  string
	text  string
	style *lipgloss.Style
}

// rows wraps the line at width.
func (l panelLine) rows(width int) []string {
	leadWidth := displayLineWidth(l.lead)
	chunks := wrapPanelWords(l.text, maxInt(1, width-leadWidth))
	...
}
```

`pkg/tui/render.go` 中 `panelBuilder.selectable`：

```go
func (b *panelBuilder) selectable(selected bool, lead, text string) {
	if selected {
		b.focusStart, b.focusEnd = len(b.body), len(b.body)+1
		if b.heading >= 0 && b.grouped == 0 {
			b.focusStart = b.heading
		}
		b.text(panelCursor+lead, text, nil)
	} else {
		b.text(panelIndent+lead, text, nil)
	}
	b.grouped++
}
```

`buildStatusPanel` 的 tab 行（当前 tab 的样式先例）：`panelTabStyle.Render(tab)` 表示当前 tab，其余 tab 用普通文字，tab 之间用 3 个空格分隔；提示文字为 `"Esc to close · Tab to switch · PgUp/PgDn to scroll"`。

`pkg/tui/chat_slash.go` 中 `handleSkillToggle`（计划 001 之后的形态）：

```go
	for _, item := range sortSkillEntries(entries) {
		label := strings.TrimSpace(item.Name)
		if desc := strings.TrimSpace(item.Description); desc != "" {
			label += " - " + desc
		}
		if src := strings.TrimSpace(item.Origin.Label()); src != "" {
			label += " [" + src + "]"
		}
		...
	selected, ok, err := c.selector.MultiSelect("Enable / disable skills\nSpace toggles, enter saves. Applies to new sessions.", options, defaults)
```

`pkg/tui/chat_slash.go` 中 Import from Memory 现在的标签与提示（`memorySkillLabel` 与 `handleMemorySkillImport` 的片段）：

```go
func memorySkillLabel(item MemorySkillOption) string {
	label := strings.TrimSpace(item.Name)
	if desc := strings.TrimSpace(item.Description); desc != "" {
		label += " - " + desc
	}
	if state := memorySkillStateLabel(item); state != "" {
		label += " [" + state + "]"
	}
	if reason := strings.TrimSpace(item.Blocked); reason != "" {
		return label + " (blocked: " + reason + ")"
	}
	// ... 之后追加 " (adds /cmd, shadows <path>)"
}

	prompt := "Import from Memory\nSkills written by memory consolidation. Selected ones are copied into your workspace and become tools in a new session."
	if len(blocked) > 0 {
		prompt += "\n\nNot importable:\n" + strings.Join(blocked, "\n")
	}
	// Nothing is preselected: this is the approval step, so every import has to
	// be an explicit choice rather than a default the user forgot to clear.
	selected, ok, err := c.selector.MultiSelect(prompt, options, nil)
```

注意：`pickerHead` 会把标签第一行之后的所有行用空格拼成**一个**副标题段落，所以 `"\n\nNot importable:\n..."` 实际上会被压成一段。

### 必须遵守的约定

- **R-full**：长内容完整折行，禁止截断或 `…`。测试 `TestStatusPanelGoldenWidths`（`run_test.go`）是这一规则的写法范例：`assertRowsFit` + 断言不包含 `…`。
- 面板以逻辑行保存内容，每次绘制时按当时的宽度重新排版。所以任何依赖宽度的计算都必须放在 `rows(width)` 里，不能在构建面板时完成。
- 两列间距统一为 2 个字符，与 `slashMenuPanel` 一致，定义为常量 `panelColumnGap = 2`。tab 之间的间距与 /status 一致，为 3 个字符，定义为常量 `panelChipGap = 3`。
- 注释风格：说明"为什么"，用完整的英文句子，参考 `render.go` 中 `slashPanel` 一带的注释。

## 需要用到的命令

| 用途 | 命令 | 成功时的预期 |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| vet | `go vet ./pkg/tui` | exit 0 |
| 新测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1 -run 'PanelColumns\|PanelChips\|TabbedPicker\|Skills\|SkillRow\|MemoryImport\|SlashMenuPanel\|StatusPanel\|RichPicker'` | ok |
| TUI 全量 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | ok |
| 真机 | 见第 8 步（`run-forebrain` driver） | 见第 8 步 |

## 建议执行者使用的工具

- `.claude/skills/run-forebrain/SKILL.md`：用 tmux 驱动真实 TUI（第 8 步）。先读一遍它的 "Run (agent path)" 和 "Reading the result" 两节。
- 如需对比改动前后的 SGR 字节（例如确认当前 tab 有下划线），可用 `.forebrain/skills/tui-render-ab-verify/SKILL.md`。这是可选项。

## 范围

**允许修改或新建的文件**：

- `pkg/tui/panel_columns.go`（新建）：`panelLine` 的 chips / 两列排版，`panelBuilder.tabs`、`panelBuilder.selectableColumns`。
- `pkg/tui/tabbed_picker.go`（新建）：`tabbedPickerState`、`rawSelector` 的两个新方法、回退辅助函数。
- `pkg/tui/panel_columns_test.go`、`pkg/tui/tabbed_picker_test.go`（新建）。
- `pkg/tui/render.go`：**只允许**三处最小编辑——①给 `panelLine` 加字段；②在 `rows` 开头加两行分派；③把 `selectable` 的聚焦逻辑抽成 `addSelectable`。
- `pkg/tui/channels.go`：在 `PagedRichSelector` 之后新增 `TabbedRichSelector` 接口与 `TabbedSelectOptions`。
- `pkg/tui/chat_slash.go`：上面列出的 /skills 相关函数。
- `pkg/tui/commands_test.go`、`pkg/tui/run_test.go`（只改按下标驱动 /skills 的测试，以及 `scriptedSelector` / `richStep`；第 7 步点名的**新增**替身、字段与测试也在这两个文件的允许范围内，除此之外的既有内容不动）。
- `pkg/tui/chat_session_test.go`：**只允许**修改 `memoryImportSelector.pick` 和 `TestMemorySkillLabelDistinguishesPromotedFromActive` 这两个函数，见第 7 步第 4 点。文件里的其他任何内容都不许碰。

**不在范围内**：

- 通用 `richPickerState.panel` 的渲染（/permissions、/migrate、/resume、setup 等）：由计划 003 负责，本计划不改。字符串选项的 `selectState` 不在本期范围内。
- per-skill 详情页（`handleSkillEntry` 的 4 个动作仍走 `SelectRich`），只改它的标题（第 6 步）。
- `buildStatusPanel` 的 tab 行：不迁移到 chips。
- 鼠标点击 tab、PgUp/PgDn 翻页：不做。
- `render.go` 中与上述三处无关的任何内容；`render_test.go`、`setup.go`（都有别人未提交的改动）；`chat_session_test.go` 中除上面两个函数以外的部分。
- `MemorySkillOption`（`notify.go`）、`memorySkillStateLabel`、`divergedAmong`、`PromoteMemorySkills`：Import from Memory 的数据与审批语义不变，只改呈现方式。

## Git 工作流

- 不建分支、不提交、不推送。开始前先执行 `git diff > /tmp/skills-002-before.diff` 和 `git diff pkg/tui/render.go > /tmp/render-before.diff` 留底；结束时执行 `git diff pkg/tui/render.go` 并与之比较，新增部分只能是本计划允许的三处编辑。

## 步骤

### 第 1 步：面板原语——tab 条（chips）与两列行

1. `pkg/tui/render.go`，给 `panelLine` 加三个字段（加在 `style` 之后）：

   ```go
   	// chips, when set, takes the place of text: items laid out left to right
   	// panelChipGap cells apart and wrapped whole, so a tab's name and its count
   	// never part across rows (the tab bar).
   	chips []string
   	// col, when colWidth > 0, is a column of its own between lead and text:
   	// the name of a two-column row. See columnRows.
   	col      string
   	colWidth int
   ```

   在 `rows` 函数体的最开头加上：

   ```go
   	if len(l.chips) > 0 {
   		return l.chipRows(width)
   	}
   	if l.colWidth > 0 {
   		return l.columnRows(width)
   	}
   ```

2. 新建 `pkg/tui/panel_columns.go`，实现以下内容（行为必须**完全符合**下面的描述，单元测试会逐字校验）：

   ```go
   // panelChipGap is the room between two tabs, as /status spaces its tabs.
   const panelChipGap = 3

   // panelColumnGap is the fixed room between a two-column row's name and its
   // description, as the slash menu spaces its commands.
   const panelColumnGap = 2

   // panelNameColumnMax caps a two-column list's name column, so one long name
   // cannot push every description to the right edge; the row layout narrows it
   // further to two fifths of the row.
   const panelNameColumnMax = 40
   ```

   `func (l panelLine) chipRows(width int) []string`：
   - `leadW := displayLineWidth(l.lead)`，`avail := maxInt(1, width-leadW)`；
   - 从左到右贪心排布：当前行为空时直接放入这个 chip；否则只有在 `当前宽度 + panelChipGap + chip 宽度 <= avail` 时才追加（中间补 `panelChipGap` 个空格），放不下就另起一行；
   - 某个 chip 单独比 `avail` 还宽时，单独占行，并用 `wrapCardLine(chip, avail)` 硬折（tab 名都很短，只有窄于约 16 列时才会发生）；
   - 第一行以 `l.lead` 开头，后续行以 `strings.Repeat(" ", leadW)` 开头；
   - 宽度一律用 `displayLineWidth` 计算（chip 本身带 SGR 样式）。

   `func (l panelLine) columnRows(width int) []string`：
   - `leadW := displayLineWidth(l.lead)`；
   - `colW := minInt(l.colWidth, maxInt(1, (width-leadW)*2/5))`——名称列最多占这一行的五分之二（如果包内没有 `minInt`，用内置 `min`）；
   - `nameW := displayLineWidth(l.col)`；
   - `pad := strings.Repeat(" ", leadW+colW+panelColumnGap)`——说明列的左边界；
   - `textW := maxInt(1, width-leadW-colW-panelColumnGap)`；
   - `texts` = `l.text` 去除首尾空白后非空时为 `wrapPanelWords(text, textW)`，否则为空切片；
   - **没有说明**（`strings.TrimSpace(l.text) == ""`）：直接返回 `panelLine{lead: l.lead, text: l.col}.rows(width)`。这样排出的结果与普通行逐字相同，计划 003 中通用选择器里没有说明的列表（/resume、模型名、推理强度）因此保持原样；
   - **名称放得下**（`nameW <= colW`）：
     - 第 0 行 = `l.lead + l.col + 空格×(colW-nameW+panelColumnGap) + texts[0]`；
     - 第 i≥1 行 = `pad + texts[i]`；
   - **名称放不下**（`nameW > colW`）：名称单独占行——`wrapPanelWords(l.col, maxInt(1, width-leadW))` 的第一块前接 `l.lead`，其余块前接 `strings.Repeat(" ", leadW)`；随后每个 `texts[i]` 都输出为 `pad + texts[i]`，说明从下一行的说明列开始；
   - `l.style` 不为 nil 时，用它渲染每一块说明（与 `rows` 一致）。

   `panelBuilder` 新方法：

   ```go
   // tabs adds the tab bar to the pinned head, one pre-styled chip per tab.
   func (b *panelBuilder) tabs(chips []string) {
   	b.head = append(b.head, panelLine{lead: panelIndent, chips: chips})
   }

   // selectableColumns adds one cursor-addressable row in two columns: name in
   // a column nameWidth cells wide (at most two fifths of the row), desc in the
   // column panelColumnGap cells right of it — each left-aligned, the
   // description wrapping inside its own column.
   func (b *panelBuilder) selectableColumns(selected bool, lead, name string, nameWidth int, desc string) {
   	b.addSelectable(selected, panelLine{lead: lead, col: name, colWidth: nameWidth, text: desc})
   }
   ```

3. `pkg/tui/render.go`：把 `selectable` 改写为下面的形状（行为不变，只是把聚焦逻辑抽出来供两种行共用）：

   ```go
   func (b *panelBuilder) selectable(selected bool, lead, text string) {
   	b.addSelectable(selected, panelLine{lead: lead, text: text})
   }

   // addSelectable puts the marker's margin before line's lead, making the line
   // the focus when it is the selected one. The first row of a group takes the
   // group's heading into its focus, so bringing it into view never leaves the
   // heading out.
   func (b *panelBuilder) addSelectable(selected bool, line panelLine) {
   	margin := panelIndent
   	if selected {
   		b.focusStart, b.focusEnd = len(b.body), len(b.body)+1
   		if b.heading >= 0 && b.grouped == 0 {
   			b.focusStart = b.heading
   		}
   		margin = panelCursor
   	}
   	line.lead = margin + line.lead
   	b.body = append(b.body, line)
   	b.grouped++
   }
   ```

   `selectable` 原有的注释保留，放在 `selectable` 上方。

4. 新建 `pkg/tui/panel_columns_test.go`，写法参考 `TestSlashMenuPanelGroupsCommandsAndSkills`（`chat_session_test.go` 约行 21280）和 `TestStatusPanelGoldenWidths`（`run_test.go` 约行 4200）。可以直接使用同包里已有的辅助函数 `stripANSI`、`assertRowsFit`。注意：包里还有一个**小写**开头的生产函数 `stripAnsi`（`reducer.go`，清洗终端输入用），两者都存在、都能编译；测试里统一用测试助手 `stripANSI`（`chat_session_test.go:16047`），不要当成笔误互相"纠正"。测试清单：
   - `TestPanelColumnsAlignDescriptions`：`panelLine{lead: "  ", col: "alpha", colWidth: 14, text: "First skill."}.rows(80)` 去除 ANSI 后，结果等于 `[]string{"  alpha" + strings.Repeat(" ", 11) + "First skill."}`。
   - `TestPanelColumnsWrapInsideTheDescriptionColumn`：`col: "beta-long-name", colWidth: 14, text: "A description long enough to wrap onto a second row at this width."`，`rows(80)` 等于 `{"  beta-long-name  A description long enough to wrap onto a second row at this", strings.Repeat(" ", 18) + "width."}`。
   - `TestPanelColumnsLongNameTakesItsOwnRow`：同一个 beta 行用 `rows(30)`，第 0 行等于 `"  beta-long-name"`，第 1 行以 `strings.Repeat(" ", 15) + "A description"` 开头，其余每一行都以 15 个空格开头；并调用 `assertRowsFit(t, rows, 30)`。
   - `TestPanelColumnsWithoutDescriptionMatchPlainRows`：对 `col` 分别取 `"2h ago   a short title"` 和一个 90 个字符的长标签（例如 `strings.Repeat("word ", 18)`），`colWidth: 40`、`text` 为空时，`rows(80)` 和 `rows(30)` 都必须与 `panelLine{lead: "  ", text: col}.rows(同一宽度)` 完全相等。
   - `TestPanelChipsWrapWhole`：`panelLine{lead: "  ", chips: []string{"Project 2", "Shared 1", "Manage"}}.rows(80)` 等于 `{"  Project 2   Shared 1   Manage"}`；`rows(30)` 等于 `{"  Project 2   Shared 1", "  Manage"}`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1 -run 'PanelColumns|PanelChips|SlashMenuPanel|StatusPanel'` → ok（已有的面板测试也必须仍然通过，证明 `selectable` 的重构没有改变行为）。

### 第 2 步：可选能力接口与回退

`pkg/tui/channels.go`，紧接在 `PagedRichSelector` 之后：

```go
// TabbedSelectOptions shapes a tabbed picker.
type TabbedSelectOptions struct {
	// DefaultIdx is the row the picker opens on, an index into items; one that
	// names no selectable row opens the first tab's first row.
	DefaultIdx int
	// Checked are the rows checked when a multi-select opens.
	Checked []int
	// Uncounted are tabs shown without a row count: a tab of actions rather
	// than of things.
	Uncounted []string
}

// TabbedRichSelector is an optional Selector capability for a long list that
// sorts into a few kinds: each item's Category is a tab, one tab is shown at a
// time, and every row is two columns — the label, then the description, each
// left-aligned, a fixed gap apart. Tabs come in the order their category first
// appears in items. Implemented by the interactive selector without widening
// Selector; selectRichTabbed and multiSelectRichTabbed fall back to the plain
// primitives for every other selector.
type TabbedRichSelector interface {
	// SelectRichTabbed returns the index into items of the row chosen.
	SelectRichTabbed(label string, items []SelectItem, opts TabbedSelectOptions) (int, bool, error)
	// MultiSelectRichTabbed returns the indices into items checked when the
	// user confirms, ascending.
	MultiSelectRichTabbed(label string, items []SelectItem, opts TabbedSelectOptions) ([]int, bool, error)
}
```

新建 `pkg/tui/tabbed_picker.go`，先写两个回退辅助函数：

- `func selectRichTabbed(sel Selector, label string, items []SelectItem, opts TabbedSelectOptions) (int, bool, error)`：`sel` 实现了 `TabbedRichSelector` 就调用它；否则调用 `sel.SelectRich(label, items, opts.DefaultIdx)`。
- `func multiSelectRichTabbed(sel Selector, label string, items []SelectItem, opts TabbedSelectOptions) ([]int, bool, error)`：`sel` 实现了能力接口就调用它；否则：
  - 用 `tabbedFallbackLabels(items)` 生成字符串选项。规则：每项为 `Label + " (" + Category + ")"`；同一字符串第 2、3……次出现时，依次追加 `" #2"`、`" #3"`……，保证唯一；
  - 默认值为 `opts.Checked` 对应的选项；
  - 调用 `sel.MultiSelect(label, options, defaults)`；
  - 把返回的字符串映射回下标，按升序返回。`err != nil` 或 `!ok` 时原样返回 `nil, ok, err`。

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` → exit 0。

### 第 3 步：`tabbedPickerState`——纯状态，可单测

在 `pkg/tui/tabbed_picker.go` 中实现以下内容。字段名可以调整，但行为必须完全符合描述：

```go
type tabbedPickerState struct {
	items     []SelectItem
	tabs      []string     // categories, in first-appearance order
	tabOf     []int        // per item, its index in tabs
	uncounted map[string]bool
	tab       int          // the tab shown
	matches   []int        // per tab, its selectable rows matching the filter
	visible   []int        // indices into items: the shown tab's matching rows, best match first
	cursor    int          // index into visible
	nameWidth int          // widest label of all items, at most panelNameColumnMax
	filter    filterField
	multi     bool
	checked   map[int]bool // indices into items (multi-select only)
	esc       string
}

```

- `newTabbedPickerState(items []SelectItem, opts TabbedSelectOptions, multi bool, esc string) *tabbedPickerState`：
  - 按 `Category` 首次出现的顺序建立 `tabs`；
  - `nameWidth = min(所有 items 中 Label 的最大显示宽度, panelNameColumnMax)`。按全部 items 计算，所以输入过滤词和切换 tab 时说明列都不会左右跳动；
  - `checked` 由 `opts.Checked` 初始化；
  - 调用 `rebuild()`；
  - 如果 `opts.DefaultIdx` 指向一个未禁用的 item，就调用 `focus(opts.DefaultIdx)`。
- `rebuild()`：
  - `query := strings.ToLower(strings.TrimSpace(p.filter.text))`；
  - 对每个未禁用的 item，计算 `rank := pickerMatchRank(item.Label, item.Description, query)`，`rank < 0` 时跳过；匹配的 item 计入 `matches[tabOf[i]]`；
  - `query != ""` 且 `matches[p.tab] == 0` 时，把 `p.tab` 移到第一个 `matches > 0` 的 tab（找不到就保持不变）；
  - `visible` = 当前 tab 中匹配的 item，按 rank 稳定排序；
  - `cursor = 0`（与 `richPickerState.rebuildSelectable` 一致：过滤之后箭头总在第一项）。
- `switchTab(step int)`：从当前 tab 沿 `step`（+1 或 -1）方向**循环**寻找下一个 `matches > 0` 的 tab；找到就切换过去、重算 `visible`（不改动过滤词），并把 `cursor` 置 0；找不到就什么也不做。
- `focus(idx int)`：`p.tab = p.tabOf[idx]`，重算 `visible`，让 `cursor` 指向 `idx` 在 `visible` 中的位置。
- `current() int`：`visible` 为空时返回 -1，否则返回 `visible[cursor]`。
- `toggle()`：仅在多选时生效，翻转 `checked[current()]`（`current() < 0` 时不做任何事）。
- `checkedIndices() []int`：返回升序的下标。
- `panel(label string) slashPanel`：
  - `pickerHead(b, label)`；
  - 仅当 `len(p.tabs) > 1` 时调用 `b.tabs(p.tabChips())`——只有一类条目时（如 Import from Memory，或没有任何 skill 时只剩 Manage）不显示 tab 条；
  - 对 `visible` 中的每一行：多选时 `lead` 为 `selectorCheckedGlyph + " "` 或 `selectorUncheckedGlyph + " "`，单选时为 `""`；然后调用 `b.selectableColumns(ci == p.cursor, lead, item.Label, p.nameWidth, strings.TrimSpace(item.Description))`；
  - `visible` 为空时调用 `b.text(panelIndent, "No matches", nil)`；
  - `b.hint(tabbedFooterHint(len(p.tabs) > 1, p.multi, p.esc))`；
  - 最后 `panel.field = p.filter.panelField()`。
- `tabChips() []string`：每个 tab 的文字为 `name`；不在 `uncounted` 中的 tab 为 `name + " " + strconv.Itoa(matches[t])`。当前 tab 用 `panelTabStyle.Render(...)`；`matches == 0` 的 tab 用 `panelHintStyle.Render(...)`（变暗，Tab 键会跳过它）；其余 tab 用普通文字。
- `tabbedFooterHint(switchable, multi bool, esc string) string`：`esc == ""` 时取 `"Esc to cancel"`；`switchable` 时以 `"Tab to switch · "` 开头；接着是 `"↑/↓ to navigate · "`；多选时再加 `"Space to select · "`；最后是 `"Enter to confirm · " + esc`。

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` → exit 0。

### 第 4 步：`rawSelector` 实现能力接口

在 `pkg/tui/tabbed_picker.go` 中为 `*rawSelector` 实现 `SelectRichTabbed` 和 `MultiSelectRichTabbed`，二者共用一个按键循环。循环结构**照抄** `overlays.go` 中的 `selectRichPaged`：

- `items` 为空：单选返回 `-1, false, nil`，多选返回 `nil, false, nil`，并且不获取输入；
- `release, err := s.acquireComposerInput()` 后 `defer release()`；
- 状态由 `newTabbedPickerState(items, opts, multi, s.escHint())` 建立，用 `s.showPanel(p.panel(label))` 渲染；
- `defer s.withCaretPlacer(...)` 的写法与 `selectRichPaged` 相同（用于点击定位过滤框里的光标）；
- 按键处理：

| 按键 | 动作 |
|---|---|
| `rawKeyNone` | `continue` |
| `rawKeyMouse` | `s.dispatchMouse(key.mouse)`；`continue` |
| `rawKeyUp` / `rawKeyDown` | 在 `visible` 内移动 `cursor`，不越界 |
| `rawKeyTab` / `rawKeyShiftTab` | `p.switchTab(+1)` / `p.switchTab(-1)` |
| `rawKeyEnter` | 单选：`current() < 0` 时返回 `-1, false, nil`，否则返回 `current(), true, nil`；多选：返回 `checkedIndices(), true, nil` |
| `rawKeyEscape` / `rawKeyCtrlC` | 返回 `s.dismissed(key.kind)`（单选时下标为 -1，多选时为 nil） |
| `rawKeyBackspace` | `p.filter.backspace()` 返回 true 时 `p.rebuild()` |
| `rawKeyLeft` / `rawKeyRight` / `rawKeyHome` / `rawKeyEnd` | 移动过滤框光标（同 `selectRichPaged`） |
| `rawKeyPaste` | 把换行替换为空格后插入，然后 `rebuild()` |
| `rawKeyRune` | 插入字符，然后 `rebuild()` |
| `rawKeySpace` | 单选：插入空格并 `rebuild()`；多选：`p.toggle()`（与 `MultiSelect` 一致） |

每次处理完按键都重新 `s.showPanel(p.panel(label))`。

最后，在 `tabbed_picker.go` 末尾加一行编译期断言：`var _ TabbedRichSelector = (*rawSelector)(nil)`。

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` → exit 0。

### 第 5 步：`tabbedPickerState` 的单元测试

新建 `pkg/tui/tabbed_picker_test.go`。固定夹具如下（各测试共用）：

```go
func tabbedFixture() []SelectItem {
	return []SelectItem{
		{Label: "alpha", Description: "First skill.", Category: "Project"},
		{Label: "beta-long-name", Description: "A description long enough to wrap onto a second row at this width.", Category: "Project"},
		{Label: "gamma", Description: "Shared one.", Category: "Shared"},
		{Label: "Add a skill", Description: "Install one.", Category: "Manage"},
	}
}
```

1. `TestTabbedPickerPanelGolden`：`p := newTabbedPickerState(tabbedFixture(), TabbedSelectOptions{Uncounted: []string{"Manage"}}, false, "")`。逐行去除 ANSI 后，`p.panel("Skills\nRun or manage skills").layout(80, 0, 0, false).lines` 必须**完全等于**：

   ```go
   []string{
   	"  Skills",
   	"  Run or manage skills",
   	"  Project 2   Shared 1   Manage",
   	"› Type to filter",
   	"",
   	"❯ alpha" + strings.Repeat(" ", 11) + "First skill.",
   	"  beta-long-name  A description long enough to wrap onto a second row at this",
   	strings.Repeat(" ", 18) + "width.",
   	"",
   	"  Tab to switch · ↑/↓ to navigate · Enter to confirm · Esc to cancel",
   }
   ```

   并断言：所有行都不包含 `" — "`，所有行都不包含 `"…"`。
2. `TestTabbedPickerRowsFitAtEveryWidth`：对宽度 120、80、50、30 分别调用 `assertRowsFit(t, lines, width)`。在 120 和 80 下，断言 `First skill.` 和 `A description` 在各自行里的起始列相同（说明列左对齐）。
3. `TestTabbedPickerFilterMovesToATabWithMatches`：把 `p.filter = filterField{text: "sha", cursor: 3}` 后调用 `p.rebuild()`，断言 `p.tabs[p.tab] == "Shared"`、`p.visible == []int{2}`、`p.matches == []int{0, 1, 0}`；再调用 `p.switchTab(1)`，tab 仍为 Shared（其他 tab 都没有匹配）；`p.panel("Skills\nRun or manage skills").layout(80, 0, 0, false).lines[2]` 去除 ANSI 后等于 `"  Project 0   Shared 1   Manage"`。
4. `TestTabbedPickerSwitchWrapsAround`：从 tab 0 调用 `switchTab(-1)` 到达 Manage（下标 2）；再 `switchTab(1)` 回到 Project（下标 0）；每次切换后 `cursor == 0`。
5. `TestTabbedPickerOpensOnTheDefaultRow`：`DefaultIdx: 2` → `p.tabs[p.tab] == "Shared"` 且 `p.current() == 2`；`DefaultIdx: -1` → tab 0、`current() == 0`；把夹具第 2 项（gamma）设为 `Disabled: true` 并以 `DefaultIdx: 2` 打开 → tab 0、`current() == 0`。
6. `TestTabbedPickerMultiSelectChecksAcrossTabs`：`newTabbedPickerState(tabbedFixture(), TabbedSelectOptions{Checked: []int{0, 2}}, true, "")`。先在初始状态下检查面板：`layout(80, 0, 0, false)` 去除 ANSI 后，alpha 行以 `"❯ ● alpha"` 开头，beta 行以 `"  ○ beta-long-name"` 开头，提示行包含 `"Space to select"`。然后依次检查：`checkedIndices() == [0 2]`；`toggle()` 后为 `[2]`；`switchTab(1)` 再 `toggle()` 后为 `[]`；再 `switchTab(1)` 再 `toggle()` 后为 `[3]`。
7. `TestTabbedFallbackLabelsAreUnique`：两个 `{Label: "improve", Category: "Cross-tool"}` 加一个 `{Label: "improve", Category: "Project"}` → 得到 `["improve (Cross-tool)", "improve (Cross-tool) #2", "improve (Project)"]`。
8. `TestTabbedPickerSingleTabHasNoTabBar`：items 只有两个 `Category: "Memory"` 的条目，`layout(80, 0, 0, false)` 去除 ANSI 后：`lines[0]` 是标题，`lines[1]` 是副标题，`lines[2] == "› Type to filter"`（标题与过滤框之间没有 tab 条），提示行不包含 `"Tab to switch"`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1 -run 'TabbedPicker|TabbedFallback'` → ok。

### 第 6 步：/skills 的四个列表接入 tab 选择器

`pkg/tui/chat_slash.go`：

1. 新增常量和辅助函数（放在 `skillWorkshopSkillName` 常量附近）：

   ```go
   // skillsManageTab is the tab the /skills actions sit in, after every layer's
   // skills.
   const skillsManageTab = "Manage"

   // skillOriginsByPath maps every listed copy to its layer, so a row can name
   // the layer of the copy that shadows it.
   func skillOriginsByPath(entries []skill.Entry) map[string]skill.Origin

   // skillRowDescription is a skill's description column: its description,
   // "(off)" before it when withState is set and the skill is disabled, and
   // after it which copy wins when another root offers the same name.
   func skillRowDescription(entry skill.Entry, origins map[string]skill.Origin, withState bool) string
   ```

   `skillRowDescription` 的规则：
   - 依次拼接以下非空部分，用一个空格分隔：`withState && !entry.Enabled` 时加 `"(off)"`；然后是 `strings.TrimSpace(entry.Description)`；最后是遮蔽提示；
   - 遮蔽提示：`len(ShadowedBy) > 0` → `"· shadowed by the " + origins[entry.ShadowedBy[0]].Label() + " copy"`；`len(Shadows) == 1` → `"· shadows the " + origins[entry.Shadows[0]].Label() + " copy"`；`len(Shadows) > 1` → `"· shadows " + strconv.Itoa(n) + " other copies"`。

   删除 `skillEntryDescription`，它的调用方改用 `skillRowDescription`。`skillEntryCategory` 保留（计划 001 后它返回 `entry.Origin.Label()`），作为每个 skill 的 tab 名。

2. `buildSkillsMenu(entries []skill.Entry, origins map[string]skill.Origin, hasMemorySkills bool)`：
   - **先**按 `sortSkillEntries` 的顺序放入所有 skill 行：`Label` 为名称，`Description` 为 `skillRowDescription(entry, origins, true)`，`Category` 为 `skillEntryCategory(entry)`；
   - **再**放入 Manage 行：4 个动作，有记忆提案时再加上 `importFromMemoryAction`，每行的 `Category` 都是 `skillsManageTab`；
   - `rows` 与 `items` 保持一一对应；
   - 更新函数注释："the installed skills first, one tab per layer in load order, then the actions in a Manage tab"。
3. `handleSkills`：
   - 循环外声明 `defaultIdx := 0`；
   - 每轮先计算 `origins := skillOriginsByPath(entries)`，再调用 `buildSkillsMenu(entries, origins, ...)`；
   - 把 `c.selector.SelectRich(...)` 换成 `selectRichTabbed(c.selector, "Skills\nRun an installed skill, or add, create, and tune skills", items, TabbedSelectOptions{DefaultIdx: defaultIdx, Uncounted: []string{skillsManageTab}})`；
   - 拿到合法的 `idx` 之后，令 `defaultIdx = idx`——子流程按 Esc 返回时，用户会回到原来的那个 tab 和那一行；
   - `defaultIdx` 越界时，选择器自己会回到第一个 tab（第 3 步已实现），这里不需要额外判断；
   - `handleSkillEntry(row.entry)` 改为 `handleSkillEntry(row.entry, origins)`。
4. `handleSkillEntry(entry skill.Entry, origins map[string]skill.Origin)`：只把标题改为 `name + " · " + entry.Origin.Label() + "\n" + skillRowDescription(entry, origins, true)`，其余不变。
5. `handleSkillImprove(entries)`：
   - `sorted := sortSkillEntries(entries)`，`origins := skillOriginsByPath(entries)`；
   - items 的 `Label`、`Description`、`Category` 与第 2 点中的 skill 行相同；
   - 调用 `selectRichTabbed(c.selector, "Improve a skill\nWhich skill should the workshop work on?", items, TabbedSelectOptions{})`。
6. `handleSkillToggle(entries)` 整体重写：
   - `sorted := sortSkillEntries(entries)`，`origins := skillOriginsByPath(entries)`；
   - 每个 skill 一行：`Label` 为名称，`Description` 为 `skillRowDescription(entry, origins, false)`（复选框已表示开关状态，所以不加 `"(off)"`），`Category` 为 `skillEntryCategory(entry)`；
   - `Checked` 为 `Enabled` 项的下标；
   - 调用 `multiSelectRichTabbed(c.selector, "Enable / disable skills\nSpace toggles, Enter saves. Applies to new sessions.", items, TabbedSelectOptions{Checked: checked})`；
   - 返回的下标映射为 `strings.TrimSpace(sorted[i].Path)`，再交给 `ApplySkillEnabledSelection`；
   - 删除 `pathByLabel` 以及拼接 `" - "`、`" ["`、`"(shadowed)"`、`"(active, shadows"` 的全部代码。
7. Import from Memory（`handleMemorySkillImport`）改用两列多选，审批语义不变（不预选、diverged 仍需二次确认、`PromoteMemorySkills` 调用不变）：
   - 新增常量与辅助函数，放在 `importFromMemoryAction` 附近：

     ```go
     // memoryImportTab is the one kind of row the import picker lists, so the
     // picker shows no tab bar.
     const memoryImportTab = "Memory"

     // memoryImportRows splits the proposals into the rows the picker offers —
     // the name, and everything the decision needs in the description column —
     // and the blocked ones, each named with its reason, which are explained
     // rather than hidden.
     func memoryImportRows(items []MemorySkillOption) (rows []SelectItem, names []string, blocked []string)

     // memorySkillDescription is a proposal's description column.
     func memorySkillDescription(item MemorySkillOption) string
     ```

   - `memoryImportRows`：`Blocked` 非空的提案，加入 `blocked`，内容为 `strings.TrimSpace(item.Name) + " (" + reason + ")"`；其余的生成 `SelectItem{Label: 名称, Description: memorySkillDescription(item), Category: memoryImportTab}`，并把 `item.Name` 按相同顺序放入 `names`。
   - `memorySkillDescription`：依次拼接以下非空部分，用一个空格分隔——`strings.TrimSpace(item.Description)`；`"· " + memorySkillStateLabel(item)`（状态为空时省略）；`"· adds " + SlashCommand`；`"· shadows " + Shadows`。例如 `"Verify a release · new · adds /release-check"`。`Shadows` 是完整路径，必须原样完整显示（R-full）。
   - `handleMemorySkillImport`：
     - `len(rows) == 0` 时，输出与原来相同的 frame `"skills: no memory skill can be imported"`，后面每个 `blocked` 各占一行；
     - 提示改为单个副标题句：`"Import from Memory\nSkills written by memory consolidation. Selected ones are copied into your workspace and become tools in a new session."`；有 `blocked` 时追加 `" Not importable: " + strings.Join(blocked, "; ") + "."`；
     - 用 `multiSelectRichTabbed(c.selector, prompt, rows, TabbedSelectOptions{})` 代替 `MultiSelect`，并保留"Nothing is preselected"那段注释；
     - 返回的下标映射为 `names[i]`，后续的 `sort.Strings`、`divergedAmong` 确认、`PromoteMemorySkills` 保持不变；
     - 删除 `nameByLabel` 和 `memorySkillLabel`，同步更新函数注释中"Everything the decision needs is on the label"的说法（改为 "on the row"）。

**验证**：
- `CGO_ENABLED=1 go build -tags fts5 ./...` → exit 0
- `grep -n '" - "\|" — "\|(active, shadows\|skillEntryDescription\|memorySkillLabel\|nameByLabel' pkg/tui/chat_slash.go` → 无输出

### 第 7 步：更新按下标驱动的测试，新增控制器测试

1. `pkg/tui/commands_test.go`：
   - `richStep` 加字段 `label string`。`scriptedSelector.SelectRich` 中，`step.label != ""` 时返回 `items` 里第一个 `Label == step.label` 的下标（附带 `step.ok`、`step.err`）；找不到就返回 `-1, false, nil`。
   - 把以下测试中的顶层 /skills 选择改为按标签选择（子菜单步骤保持按下标）：
     - `TestSkillsRunNowSubmitsSkillSelection`：`{label: "repo", ok: true}`，然后 `{idx: 0, ok: true}`；同时删除"four manage rows precede it"那行注释；
     - `TestSkillsCreateHandsOffToWorkshop`：`{label: "Create a skill", ok: true}`；
     - `TestSkillsInstallFromSourceAsksOnlyForSourceAndScope`：第一步改为 `{label: "Add a skill", ok: true}`；
     - `TestSkillsSubmenuCancelReturnsToTopLevel`：第一步 `{label: "Add a skill", ok: true}`，第三步 `{label: "Create a skill", ok: true}`。
   - `TestSkillsMenuOffersManageActionsAndInstalledSkills` 补充断言：`repo` 的 `Category == "Project"`，`skill-workshop` 的 `Category == "Built-in"`，4 个动作的 `Category == "Manage"`，并且所有 skill 行都排在第一个 Manage 行之前。
2. `pkg/tui/run_test.go`（约行 3925，后台安装测试）：第一步改为 `{label: "Add a skill", ok: true}`。
3. 在 `commands_test.go` 中新增一个测试替身 `tabbedRecordingSelector`：内嵌 `*scriptedSelector`，并实现 `TabbedRichSelector`——记录每次调用的 `label`、`items`、`opts`；`SelectRichTabbed` 返回预置的 `idx` / `ok`；`MultiSelectRichTabbed` 返回预置的下标切片。新增以下测试：
   - `TestSkillsMenuUsesTabsWithManageLast`：在 `skillsTestSession()` 下直接取消（返回 `-1, false`）。断言只调用了一次，`opts.Uncounted == []string{"Manage"}`，`opts.DefaultIdx == 0`，`items[0].Label == "repo"`，并且每个 skill 行的 `Label` 都恰好等于 skill 名称（不含拼接的说明）。
   - `TestSkillsMenuReopensOnTheRowItLeft`：第一次选中 `repo`（下标 0）进入详情页，详情页（`SelectRich`）取消返回，第二次打开 /skills 时断言 `opts.DefaultIdx == 0`；再换成选中 "Add a skill"、子菜单取消，断言再次打开时 `DefaultIdx` 等于 "Add a skill" 所在的下标。
   - `TestSkillsToggleUsesTabbedMultiSelect`：通过顶层选中 "Enable / disable skills"，`MultiSelectRichTabbed` 返回 `[]int{0}`。断言 `opts.Checked` 包含所有启用项，`session.appliedEnabledPaths` 等于 `[]string{"/skills/repo"}`（`fakeSession.ApplySkillEnabledSelection` 把参数记在这个字段，见 `run_test.go:919-922`）。顶层选择 "Enable / disable skills" 时，按标签找到它在 `items` 中的下标再返回。
   - `TestSkillsToggleFallbackShowsUniqueLabels`：用普通的 `scriptedSelector`（没有能力接口）走 toggle，断言 `MultiSelect` 收到的选项为 `["repo (Project)", "skill-workshop (Built-in)"]`。为此需要给 `scriptedSelector.MultiSelect` 增加记录 `options` 的字段，例如 `multiOptions []string`。
   - `TestSkillRowDescriptionNamesTheShadowingLayer`：两个 `skill.Entry`，项目那份（`Origin: skill.OriginProject`，`Shadows: [cross 路径]`）和跨工具那份（`Origin: skill.OriginCrossTool`，`ShadowedBy: [项目路径]`，`Enabled: false`）。断言前者的描述以 `"· shadows the Cross-tool copy"` 结尾；后者在 `withState=true` 时以 `"(off) "` 开头并以 `"· shadowed by the Project copy"` 结尾，在 `withState=false` 时不包含 `"(off)"`。

4. `pkg/tui/chat_session_test.go`（只改这两个函数）：
   - `memoryImportSelector.pick`：保留注释的本意（"a test selects what a user would have clicked rather than a hand-built string"），改为用生产代码计算：`rows, _, _ := memoryImportRows(session.memorySkillOpts)`，`labels := tabbedFallbackLabels(rows)`，返回 `Label == name` 那一行对应的 `labels[i]`。原因：`memoryImportSelector` 没有实现能力接口，走的是回退路径，`MultiSelect` 收到的就是这些回退标签。
   - `TestMemorySkillLabelDistinguishesPromotedFromActive` 改名为 `TestMemorySkillDescriptionDistinguishesPromotedFromActive`，三处 `memorySkillLabel(...)` 改为 `memorySkillDescription(...)`，断言内容（`"active next session"`、`"adds /triage"`、`"shadows /home/u/.forebrain/skills/triage"`）不变；再补一条断言：结果不包含 `" - "`。
   - 其余 `TestMemoryImport*` 与 `TestSkillsMenuOffersMemoryImportOnlyWhenProposalsExist` **不修改**，必须原样通过：它们断言的是"被拦截的提案不在选项里""原因出现在提示里""只提升选中项""取消不提升""diverged 需要确认"，这些语义都没有变。
5. 在 `commands_test.go` 中新增 `TestMemoryImportRowsUseTwoColumns`：
   - 给定一个可导入提案 `{Name: "release-check", Description: "Verify a release", Status: "new", SlashCommand: "/release-check"}` 和一个被拦截的提案 `{Name: "compact", Description: "Squeeze things", Blocked: "name collides with the built-in /compact command"}`；
   - 断言 `memoryImportRows` 返回一行，`Label == "release-check"`、`Description == "Verify a release · new · adds /release-check"`、`Category == "Memory"`；
   - `names == ["release-check"]`，`blocked == ["compact (name collides with the built-in /compact command)"]`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → ok（全量）。

### 第 8 步：真机验证（run-forebrain）

先读 `.claude/skills/run-forebrain/SKILL.md` 的 "Run (agent path)" 一节。driver 使用隔离的 `FOREBRAIN_HOME`（`$WORK/home`）和一次性项目目录（`$WORK/proj`），首次启动时会自动信任该项目，tmux 窗口为 120×40。`WORK` 的默认值是 `${FOREBRAIN_RUN_WORK:-${TMPDIR:-/tmp}/forebrain-run}`。

```bash
D=.claude/skills/run-forebrain/driver.sh
W="${FOREBRAIN_RUN_WORK:-${TMPDIR:-/tmp}/forebrain-run}"
$D reset
$D start hang
for d in .forebrain .claude .codex; do
  mkdir -p "$W/proj/$d/skills/demo-${d#.}"
  printf -- '---\nname: demo-%s\ndescription: A deliberately long description for the %s project skill directory so that it has to wrap inside the description column at one hundred and twenty columns and again at eighty.\n---\nbody\n' "${d#.}" "$d" > "$W/proj/$d/skills/demo-${d#.}/SKILL.md"
done
$D submit '/skills'
$D screen
$D key Tab;  $D screen
$D key BTab; $D screen
$D key Escape
$D stop
```

（如果 `$D key BTab` 不被识别，运行不带参数的 `$D` 查看 `key` 子命令的用法，tmux 中 Shift+Tab 的键名是 `BTab`。）

**预期**（逐条核对 `$D screen` 的输出）：

- tab 行包含 `Project 3`（`demo-forebrain`、`demo-claude`、`demo-codex` 三个都在 Project 下）。tab 顺序固定为 Project · Agent · Shared · Cross-tool · Built-in · Manage，**空 tab 隐藏**，所以 `Built-in N` 和 `Manage` 一定在最后两位，中间的 Agent / Shared / Cross-tool 视环境可能全部或部分出现（本机 `~/.agents|.claude|.codex/skills` 里有 skill 时会出现 `Cross-tool N`）——出现或缺失都不是问题。tab 行中**不出现** `Local` 或 `Global`；
- 打开时光标位于 Project tab 的第一行；名称列左对齐；说明从同一列开始，续行也从这一列开始；没有 `—`，没有 `…`；
- 按 Tab 后，正文换成下一个 tab 的 skill；按 BTab 后回到 Project；
- 把三次 `$D screen` 的输出粘贴到给 owner 的报告里作为证据。

**可选**：driver **没有** `resize` 子命令（其子命令为 `build|start|send|key|submit|click|screen|wait|db|messages|title|sid|stop|reset`，另有 `provider-log`）。要看 80 列的表现，直接用 tmux：`tmux resize-window -t forebrain-run -x 80`（会话名默认 `forebrain-run`，可用环境变量 `FOREBRAIN_RUN_SESSION` 覆盖），截一张 `$D screen`，然后 `tmux resize-window -t forebrain-run -x 120` 复原。

## 测试计划

- 新增 `pkg/tui/panel_columns_test.go`（5 个测试）和 `pkg/tui/tabbed_picker_test.go`（8 个测试），见第 1、5 步。
- 新增控制器测试 6 个，见第 7 步第 3、5 点。
- 修改 `chat_session_test.go` 中的 2 个函数（`pick`、Import from Memory 的描述测试），见第 7 步第 4 点；其余 Import from Memory 测试不改，必须原样通过。
- 修改：`richStep` / `scriptedSelector`，以及 5 个按下标驱动的测试，见第 7 步第 1、2 点。
- 回归：`TestSlashMenuPanel*`、`TestStatusPanel*` 必须不加修改地通过，以证明 `selectable` 的重构没有改变行为。

## 完成标准

以下全部成立：

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0
- [ ] `go vet ./pkg/tui` exit 0
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` ok
- [ ] `grep -n '" - "\|" — "\|(active, shadows\|skillEntryDescription\|memorySkillLabel\|nameByLabel' pkg/tui/chat_slash.go` 无输出
- [ ] `grep -n 'var _ TabbedRichSelector = (\*rawSelector)(nil)' pkg/tui/tabbed_picker.go` 有输出
- [ ] `git diff pkg/tui/render.go` 相对 `/tmp/render-before.diff` 只多出第 1 步允许的三处编辑；对 `pkg/tui/render_test.go`、`setup.go` 以及其他范围外、开始前就有改动的文件，`git diff -- <文件>` 与开始前的留底（`git diff > /tmp/skills-002-before.diff`）中该文件的部分完全一致；`chat_session_test.go` 相对留底新增的 hunk 只能落在 `memoryImportSelector.pick` 和（改名后的）`TestMemorySkillDescriptionDistinguishesPromotedFromActive` 两个函数内
- [ ] 第 8 步的三次截屏已附在报告中，并逐条满足"预期"
- [ ] README 状态行已更新

## STOP 条件

遇到以下情况，停下来报告，不要自行处理：

- 计划 001 未完成（`skill.Origin` 不存在）。
- "现状"里的摘录与实际代码对不上。
- `selectable` 抽出 `addSelectable` 之后，任何已有的面板测试（`SlashMenuPanel*`、`StatusPanel*`、MCP / LSP 面板测试）失败。这说明重构改变了行为，不要通过修改这些测试来让它通过。
- `TestTabbedPickerPanelGolden` 的实际输出与期望不同，而差异来自本计划没有描述的已有行为（例如 `layout` 在 head 和 field 之间插入了别的行）。报告实际输出，不要直接把期望改成实际值。
- 需要修改 `render.go` 中本计划允许的三处之外的内容，或需要修改 `render_test.go`、`setup.go`，或需要修改 `chat_session_test.go` 中除 `memoryImportSelector.pick`、`TestMemorySkillLabelDistinguishesPromotedFromActive` 以外的任何内容（包括其他 `TestMemoryImport*` 测试失败、需要改它们的断言才能通过）。
- 真机验证中 tab 行仍然出现 `Local`/`Global`，或者项目里 `.claude/skills` / `.codex/skills` 中的 skill 没有出现在 Project tab 下（说明计划 001 没有生效）。

## 维护说明

- **两列与 chips 是通用面板原语**：计划 003 会让通用 `richPickerState.panel` 也改用 `selectableColumns`，所以 `columnRows` 中"没有说明时与普通行逐字相同"的规则是 003 的前提，不能删。/status 的 tab 行也可以改用 `b.tabs(...)`，以获得窄屏折行能力。
- **审阅重点**：①说明列在任何宽度下都必须完整折行，不能截断；②`handleSkills` 的 `defaultIdx` 记忆只在"返回菜单"时生效，子流程执行完（`done`）就会离开 /skills；③过滤词跨 tab 生效，当前 tab 没有匹配时会自动跳到第一个有匹配的 tab。
- **已知限制**：tab 不支持鼠标点击；长 tab 内没有 PgUp/PgDn 翻页（只能靠 ↑/↓ 和滚轮）。这两项都未被要求，若要做需另行立项。
