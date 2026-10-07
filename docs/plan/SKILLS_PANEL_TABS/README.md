# /skills 面板 tabs + 两列布局、通用选择器两列化、skill 来源分层统一：实施计划索引

由 improve skill 于 2026-10-07 生成，基于 commit `88eb320`。制定时工作区里另有与本计划无关、尚未提交的并行改动：吉祥物相关（`pkg/tui/render.go`、`render_test.go`、`setup.go`、`chat_session_test.go`），以及移除 `/diff` 命令等（`pkg/tui/chat_slash.go`、`commands.go`、`commands_test.go`、`run.go`、`run_test.go`、`pkg/turn/*`、`pkg/tool/*`、`pkg/gateway/slash_handlers*.go`）。计划里引用的行号以制定时的工作区为准。

## 背景与 owner 要求

owner 原话：

- "forebrain harness tui 的 /skills 面板的布局非常乱，请把现在的分组布局重新设计成 tabs 标签布局，且 skill 名称和说明之间不要用中横线 - 连接，而是分成两列布局，名称左对齐，说明左对齐，名称和说明之间固定间距"
- "项目的所有 skills 都必须归到 project skill 里，包括但不限于 .forebrain/skills、.claude/skills 和 .codex/skills 等，Local 组的 local 命名必须优化，容易误导用户"

owner 已拍板的决策（2026-10-07）：

| 决策点 | 结论 |
|---|---|
| 来源分类命名 | 沿用 Web 现有的五层词汇，并下沉到引擎，两端共用：**Project / Agent / Shared / Cross-tool / Built-in**（Web 中文：项目 / 主代理 / 共享 / 跨工具 / 内置）。对应关系：local→Cross-tool，global→Shared，workspace→Agent，`.system`→Built-in |
| 扫描规则 | 统一成一条：**扫描器永不进入点目录**；内置根 `$FOREBRAIN_HOME/skills/.system` 作为独立的最低优先级根单独扫描。接受两个后果：① 用户自装的同名 skill 压过内置副本；② `~/.codex/skills/.system` 下 Codex 自带的内置 skill 不再被加载 |
| 改造范围 | /skills 的四个列表：主面板、「Improve a skill」选择器、「Enable / disable skills」多选、Manage →「Import from Memory」多选（Import from Memory 为当天追加）；外加**通用 `SelectRich` 选择器**（/permissions、/migrate、/resume、setup、/model 等）统一改成两列（当天追加） |
| Tab 排列 | skill tab 按加载优先级排在前面，空 tab 隐藏；Manage 放在最后；打开时停在第一个非空的 skill tab；Tab / Shift+Tab 切换 |

## 实测事实（advisor 在 owner 的真实环境里用 `go test -overlay` 探针跑出的结果，未改任何文件）

- 项目已信任时，`.forebrain/skills`、`.claude/skills` 下的 51 个 skill **全部正确归为 project**，包括 `.claude/skills/improve`。项目未信任时，项目 skill 根本不会被扫描，**不存在"兜底成 local"的情况**。
- Local 组里那个 improve，是用户级目录里的另一份副本 `~/.agents/skills/improve`。
- 让画面看起来不对的真正原因是**遮蔽标注写反了**（`pkg/skill/state.go:205-208`、`skillmeta.go:97-98`）：面板显示"项目 improve 被遮蔽"，而运行时实际加载的恰恰是项目那份。
- 内置的 `.system` skill 被归为 `global`，所以 TUI 的 System 组从不出现；`~/.codex/skills/.system` 下的 5 个 Codex 内置 skill 被扫进来并显示为 local。
- **优先级 bug**：扫描 `$FOREBRAIN_HOME/skills` 根时会先进入 `.system`，导致内置 frontend-design 压过了用户自装的 `~/.forebrain/skills/frontend-design`。
- 同一个概念目前有五套分类器：引擎 `Source`、引擎 trust 标签、网关 `skillOrigin`、TUI 分组名 `skillEntryCategory`、TUI `skillSourceLabel`。

## 全局规则（每个计划都适用）

1. **禁止提交**：不得运行 `git commit` 或任何会产生提交、改写历史的命令（amend、merge、rebase）。所有改动留在工作区，由 owner 手动审阅和提交。
2. **不要碰与本计划无关的未提交改动**：开始前先执行 `git diff > /tmp/skills-<计划号>-before.diff` 留底。上面列出的文件里都有别人进行中的工作。只改计划点名的函数，不格式化或重排整个文件。`gofmt` 只对你改过的文件运行，并确认 diff 只包含你的改动。如果执行期间发现别人仍在同时修改同一个文件，STOP 并报告。
3. 修根因不修症状；同类组件一起修；死代码彻底删除；不加防御代码。
4. 长内容滚动完整呈现，禁止截断或省略号（owner 规则 R-full）。
5. 改动完成后在本表更新状态列。

## 执行顺序与状态

| 计划 | 标题 | 优先级 | 工作量 | 依赖 | 状态 |
|------|------|--------|--------|------|------|
| [001](001-skill-origin-one-rule.md) | skill 来源分层统一：一张层表同时驱动扫描与分类，Origin 词汇下沉引擎，修正遮蔽标注与内置优先级 | P1 | M | — | DONE |
| [002](002-skills-panel-tabs-two-column.md) | /skills 四个列表改成 tabs + 名称/说明两列布局 | P1 | M | 001 | DONE |
| [003](003-rich-picker-two-column.md) | 通用 `SelectRich` 选择器改成名称/说明两列布局 | P2 | S | 002 | DONE |

状态取值：TODO | IN PROGRESS | DONE | BLOCKED（附一行原因）| REJECTED（附一行理由）

## 依赖说明

- 002 依赖 001：tab 名称来自 `skill.Origin.Label()`，tab 顺序来自 `skill.Origin.Rank()`；行说明里的遮蔽提示要求 001 已经修正 `Shadows` / `ShadowedBy` 的方向。
- 003 依赖 002：两列排版原语 `panelBuilder.selectableColumns`、名称列上限 `panelNameColumnMax`，以及"没有说明的行与普通行逐字相同"的规则都由 002 交付；003 只把通用 `richPickerState.panel` 切换过去。

## 本期明确不做（已评估，记录在此避免重复审计）

- **per-skill 详情页「Show details」/「Run it now」按名字而非路径查找**（`Service.Inspect(name)`、`runSkillNow`）：同名的多份副本里，选中被遮蔽的那份时，看到和运行的都是胜出的那份。001 修正遮蔽标注之后，面板会如实标出哪份被遮蔽；改成按路径操作属于行为变更，留给后续单独立项。
- **计划审阅批准浮层的模型行**（`pkg/tui/overlays.go` 的 `planReviewModelRow`，在模型名后用 `" — "` 接模型目录条目的名字 `Label`，当前项还会再追加 `" (current)"`）：它不是 `SelectRich`，没有被点名，保持原样。
- **/resume 标签中补齐时间列的空格被压缩**：`"%-8s %s"` 补出的空格会被 `wrapPanelWords` 压成一个，这是现有行为，003 逐字保持原样。若要修复，应把时间放进 `Description`、标题放进 `Label`，属于行为变更，需另行立项。
- **给项目 skill 新增目录**（如 `.cursor/skills`）：001 之后，加进 `projectSkillDirs` 的任何目录都会自动归为 Project，不需要额外的分类代码。是否新增目录是产品决策，不在本期范围内。
- **`TestGlobalSkillWinsOverWorkspaceShadow`（`pkg/skill/install_source_test.go`）**：它测的是仅在测试里使用的 `Registry.Lookup`，其"按路径字母序胜出"的结果与运行时优先级相反。001 只删除其中对 `Source` 的断言；该测试本身的去留留给后续处理。
