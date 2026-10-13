# Plan 037：TUI `/skills` 面板——草稿与版本状态、发布、查看改动、丢弃、版本记录、恢复为草稿、拉取上游更新

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
> **界面以设计为准**：
> - `docs/plan/SKILL_DRAFTS_AND_VERSIONS_DESIGN.md` 第 3 节（ASCII 画面）；
> - 可交互预览 `docs/plan/SKILL_TUI_PANEL_PREVIEW.html`（https://claude.ai/artifact/5kUZi6L8cKB2k8JQHW16p4 ），左侧目录可跳到每一屏，每屏有中文说明。
>
> 文案、项目顺序、配色角色（警告色、暗色、强调色）照它写。TUI 文案用英文。
>
> **Drift check（先跑）**：
> - plan 036 已完成：`grep -n "func (v VersionStore) Publish" pkg/skill/versions.go` 有 1 行。没有 → STOP。
> - `grep -n "func (c \*commandController) handleSkillEntry" pkg/tui/chat_slash.go`：1 行。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: LOW–MED（只改 TUI 面板与会话层的薄封装）
- **Depends on**: 036
- **Category**: feature
- **Planned at**: commit `005c1a8` + 主树未提交改动，2026-10-11

## Why this matters

owner 2026-10-11：
- "tui端不要这种方式发布 /skills publish <名称> ，必须设计/skills的面板的UI/UX，在面板里发布"；
- "技能子菜单每一项的UI/UX也需要设计和发给我确认"，预览页已发 owner。

两端共用 036 的版本记录。

## Current state（已核实）

- **主列表**：`handleSkills`（`pkg/tui/chat_slash.go:1558` 起）：
  - 用 `selectRichTabbed` 显示分层标签：Project、Agent、Shared、Cross-tool、Built-in、Manage；
  - 行数据来自 `c.session.AvailableSkillToggleOptions()`（`skill.Entry` 列表）；
  - 建菜单的是 `buildSkillsMenu`；
  - 每行说明是 `skillRowDescription`（`:1690` 一带），说明完整折行。
- **单个技能的子菜单**：`handleSkillEntry`（`:1708` 起）固定四项：
  - "Run it now"；
  - "Show details"：调 `ApplySkillSelection` 显示详情；
  - "Disable/Enable this skill"；
  - "Improve it"。

  标题是 `名称 · 层级` 加说明。
- **可用组件**：
  - `c.selector.SelectRich`（子菜单）；
  - `c.selector.Input`（单行输入，`runSkillNow` 用来问任务）；
  - 确认页组件 `Review`：信任提示用的同一个，`grep -rn "func.*Review(" pkg/tui/*.go | head`。
- **配色**：
  - 强调色 256 色 110；
  - 成功 70；失败 203；警告 214；
  - 符号 245，提示用 faint；
  - 光标 `❯`，缩进两个空格。

  见 `pkg/tui` 的样式常量：`grep -rn "Color(\"214\")\|Color(\"110\")" pkg/tui | head`。
- **diff 配色**：对话里 diff 卡的主题 `WithDiffTheme`，`grep -rn "WithDiffTheme" pkg/tui | head`。
- **草稿技能**：今天不会出现在列表里（扫描跳过 `.drafts`），需要从 036 的 `VersionStore` 补进来。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/... ./pkg/skill/... -count=1` | 全部 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui/...`；`gofmt -l pkg/tui` | 无输出 |

## Scope

**In scope**：
- `pkg/tui/chat_slash.go`（面板、子菜单、各子流程）；
- 会话层取版本状态、执行版本操作的方法：放在 `ChatSession` 上，调用 036 的 `VersionStore` 与 `skill.Service`；
- 工坊一轮结束后的提示行；
- 对应测试。

**Out of scope**：
- 引擎与存储（036）；
- web（web 020）。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：会话层的版本接口

`ChatSession` 增加以下方法，都是对 036 的薄封装：
- `SkillVersionState(entry)`；
- `PublishSkill(entry, note)`；
- `DiscardSkillDraft(entry)`；
- `RestoreSkillVersion(entry, v, replaceDraft)`；
- `PullSkillUpdate(entry, replaceDraft)`；
- `SkillDiff(entry, from, to)`；
- `DraftOnlySkills()`：返回各层只有草稿、还没发布的技能，作为 `skill.Entry` 补进列表，并带上"草稿"标记。

发布、丢弃、恢复之后调一次技能刷新（今天启停之后怎么刷新就照样），列表立即反映新状态。

### Step 2：主列表的状态写在说明列开头

`skillRowDescription` 在说明前加状态段（设计文档 3.1）：
- 草稿：`Draft · `，警告色；
- 已发布：`v3 · `，暗色；
- 有草稿：`v3 · unpublished changes · `，"unpublished changes"用警告色；
- 有更新：`· update available`，强调色；
- 停用：`(off) `。
- 内置技能：`Built-in · `。

说明照旧完整折行，排序规则不变。草稿技能与已发布的列在同一个层级标签下。

### Step 3：子菜单随状态变化

照设计文档 3.2 与预览页"子菜单"各屏，`handleSkillEntry` 按状态生成项目：

| 状态 | 菜单项（顺序） |
|---|---|
| 从没发布的草稿 | `Publish v1` · `Show changes` · `Continue in workshop` · `Discard draft` |
| 已发布、有草稿 | `Publish vN+1` · `Show changes` · `Discard draft` · `Run it now` · `Improve it` · `Version history` · 启停 · `Show details` |
| 已发布、无草稿 | `Run it now` · `Improve it` · `Version history` · 启停 · `Show details` |
| 有上游更新 | 首项加 `Pull the update into a draft`，说明 `<rev> · <日期> · N files` |
| 内置 | `Run it now` · 启停 · `Show details` |

说明列文字照预览页，例如：
- `Publish v4` 的说明：`2 files changed since v3`；
- `Version history` 的说明只写个数：`3 versions · compare or restore`，只有一个版本时写 `1 version`。版本号不列在说明里（owner 2026-10-11）；
- `Run it now` 在有版本时写 `Load v3 into this turn and give it a task`。

标题：第一行 `名称 · 层级`，第二行状态段 + 说明。

- 启停：执行后**留在子菜单**，顶部显示一行状态，例如 `Disabled gateway-ops · new sessions skip it`，并刷新菜单。预览页"Disable / Enable"屏有样子。
- `Show details`：在面板里单独显示一页，内容照预览页"Show details"：
  - Version；
  - Source；
  - Path；
  - Allowed tools；
  - Esc 返回。
- `Improve it` / `Continue in workshop`：沿用今天的 `improveSkill`，交给工坊，在当前对话进行。

### Step 4：发布

照设计文档 3.3 与预览页"发布"三屏：
1. 确认页（`Review`）：
   - 标题 `Publish <名称> v<N+1>?`；
   - 一行 `New sessions use v<N+1>; sessions already running keep v<N>.`，首次发布写 `New sessions load it, enabled.`；
   - `Changes since v<N>` 下列出文件和 `+a −b`；
   - 选项 `Publish v<N+1>` / `Show changes` / `Cancel`。
2. 选 `Publish` → `Input`：`Release note for v<N+1> (optional)`，回车发布，Esc 返回确认页。
3. 完成：回到列表，列表顶部留一行成功色 `Published <名称> v<N+1> · new sessions use it`。

### Step 5：查看改动（分文件的 diff 页）

照设计文档 3.4 与预览页"Show changes"：
- 标题 `<名称> · v<N> → draft`；
- 文件名、`+a −b`、`1 of 2`；
- 下面是 diff，配色沿用 `WithDiffTheme`；
- 底部提示 `n/p next/previous file · ↑/↓ scroll · Esc to go back`。

若现有组件里没有分页查看器：
- 先找现成的长文本查看组件（`grep -rn "pager\|Pager" pkg/tui/*.go | head`），能复用就复用；
- 都没有 → 用 `SelectRich` 的长内容加 `n`/`p` 快捷键实现，在报告里说明。

### Step 6：丢弃、版本记录、恢复、拉取

1. `Discard draft`：确认页，默认停在 `Cancel`。
   - 有版本时写 `Keep v<N>, drop the unpublished changes?`；
   - 纯草稿写 `Delete the draft?`。
2. `Version history`：照设计文档 3.5，列表项依次为：
   - 草稿：`Draft · Based on v3 · 2 files changed`；
   - 每个版本：`v3 · Current · Oct 9 18:20 · Published · <说明> · +12 −3`。
   - 新的在上。
   - **版本多时一次显示 20 条，往下翻到底再加载**：用现有的 `SelectRichPaged`（`pkg/tui/overlays.go:4481`），owner 2026-10-11。
   - 选草稿 → `Show changes`；
   - 选旧版本 → 子菜单 `Compare with v<当前>` · `Restore as a draft` · `Back`；
   - 选当前版本 → `Compare with v<上一版>` · `Back`。
3. `Restore as a draft` 确认页：
   - 标题 `Restore v2 as a draft?`；
   - 一行 `The draft becomes v2's content; publish it to make it current.`；
   - 已有草稿时再加一行 `The current draft will be replaced.`；
   - 选项 `Restore as a draft` / `Cancel`。
4. `Pull the update into a draft` 确认页：
   - 列出上游改动的文件；
   - 已有草稿时提示会替换；
   - 完成后状态变为"有未发布的修改"。

### Step 7：工坊结束后的提示

`Create a skill` 和 `Improve it` 交给工坊之后，在当前对话里进行。工坊写完草稿、这一轮结束时，在对话里留一行 `Draft <名称> saved · /skills to publish`（设计文档 3.6），这是唯一的提示，不自动弹出面板。

判断"这一轮写了某个技能的草稿"：
- 用 033 的 `fb_file_changes` 里本轮写到 `.drafts/<名称>/` 的路径；
- 033 还没做时，用工具结果里 036 加的"已写入技能 X 的草稿"标记。

### Step 8：测试

照现有面板测试的写法（`grep -rn "handleSkillEntry\|buildSkillsMenu" pkg/tui/*_test.go | head`，用 `recordingSelector` 一类的桩）：
- 四种状态的子菜单项与顺序；
- 发布流程（确认 → 说明 → 完成行）；
- 丢弃默认 Cancel；
- 版本记录分页（25 个版本时第一页 20 条）；
- 恢复确认页在有草稿时多一行；
- 启停后留在子菜单并显示状态行；
- 主列表的状态段与配色角色。

真机（driver，隔离 home）：
1. 让工坊新建一个技能，确认对话里出现 `Draft … saved · /skills to publish`；
2. `/skills` 里能看到 `Draft · …`；
3. 发布 v1；再改一次，发布 v2；
4. 版本记录里两条；恢复 v1 为草稿，再丢弃。

把每屏的要点写进报告。

## Test plan

见 Step 8。

## Done criteria

- [ ] Commands 表全部通过
- [ ] Step 8 的测试存在且 PASS；真机流程走通
- [ ] 与预览页逐屏对照，差异写进报告
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符。
2. `selectRichTabbed` 不支持在说明列里给一段文字单独着色，又必须改选择器组件。
3. 确认页组件 `Review` 不支持"默认停在 Cancel"。

## Maintenance notes

- 菜单项的顺序与文案以设计文档为准，改动前先改设计、再改代码。
- web 020 与本计划共用 036 的状态定义，两端显示的状态名必须对应：
  - 草稿 / Draft；
  - 有未发布的修改 / unpublished changes；
  - 有更新 / update available。
