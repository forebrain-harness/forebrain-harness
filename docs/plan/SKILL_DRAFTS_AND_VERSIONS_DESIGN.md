# 技能的草稿、发布与版本管理——设计（web 与 TUI）

> 状态：owner 2026-10-11 已定语义与存储，web 部分见设计稿，TUI 面板见第 3 节，待 owner 确认后写实现计划。
> web 界面以 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html` 为准，在页面顶部"本轮新增"里依次看"技能页""技能详情""草稿 → 发布""版本记录""发布（弹窗）""版本对比""上游有更新"。

## 1. 语义（owner 已定）

- **版本**：每个技能（主代理、项目、共享三层）有一串已发布版本 v1、v2…，只往后加，不改写；外加最多一份草稿。
- **状态**：
  - 草稿：还没有任何版本，不被加载，不能启用；
  - 已发布 vN：有版本、没有草稿；
  - 有未发布的修改：在 vN 之上有一份草稿，加载的仍是 vN。
- **所有改动只进草稿**：
  - 技能工坊里的编辑；
  - 把某个旧版本"恢复为草稿"；
  - 把上游更新"拉取到草稿"。

  只有"发布"会生成新版本，并把它的文件写进正式技能目录。发布后默认启用，只有已发布的技能才能启用、停用。
- **会话**：会话开始时冻结技能清单，同时记下每个技能的版本号，之后用到技能时都读这个版本。中途发布新版本，不影响已经开始的会话。
- **安装**：离线安装、在线安装、发现并安装进来的技能，安装时就是 v1。在线和发现安装的技能记下来源；上游有新提交时标"有更新"，要先拉取到草稿，审过后再发布。
- **存储**（owner 已定）：
  - 历史版本：放在各层技能目录旁的隐藏目录，例如 `<技能目录>/.versions/<名称>/v3/`，另加一份记录版本元数据的清单文件，包括时间、来源、说明、上游提交号、改动统计。项目里的版本历史可以随仓库提交。
  - 草稿：放在 `<技能目录>/.drafts/<名称>/`。
  - 加载器必须跳过 `.versions/` 和 `.drafts/`。
- **正式目录的改动自动进草稿**（owner 2026-10-11）：工作台"工作目录"树或会话文件工具对正式目录 `<技能目录>/<名称>/` 的编辑、新建、删除，一律落到 `.drafts/<名称>/`（没有草稿先从当前版本复制），发布后才生效；删除整个技能目录走"删除技能"确认；`.versions/` 不显示、不可写。
- **两端一致**：TUI 的 skill-workshop / skill-generator 也是先写草稿，在 `/skills` 面板里发布，不另加子命令。两端共用同一份版本记录。

## 2. web

见设计稿：
- **技能列表**：状态列显示版本号；草稿不能启用；名称旁标"有未发布的修改"或"有更新"。
- **技能工坊**（会话页的超集）：顶栏显示版本与状态和"发布 vN"；工作台的"版本"标签有下载 zip，草稿那一行有对比、丢弃。
- **发布弹窗**：列出改动、可填发布说明，并写明新会话用新版本、进行中的会话用原版本。
- **其他弹窗**：对比弹窗；恢复为草稿、丢弃草稿的确认框。

## 3. TUI `/skills` 面板

沿用现有面板：
- 分标签列表（`selectRichTabbed`），两列：名称、说明；
- 单个技能的子菜单（`SelectRich`）；
- 确认页（`Review`，信任提示用的同一个组件）。

所有操作都在面板里完成，不新增子命令。TUI 文案沿用英文。

### 3.1 主列表：状态写在说明列开头

```
  Skills
  Run an installed skill, or add, create, and tune skills
  Project 3   Agent 3   Shared 2   Built-in 18   Manage
› Type to filter

❯ gateway-5xx         Draft · Gateway 5xx triage and recovery
  gateway-ops         v3 · unpublished changes · Runbook for this
                      agent's gateway
  incident-review     v1 · (off) Incident review template and steps

  Tab to switch · ↑/↓ to navigate · Enter to confirm · Esc to cancel
```

- `Draft`、`unpublished changes` 用警告色，版本号用暗色，`update available` 用强调色。说明照旧完整折行（R-full）。
- 草稿和已发布的技能列在同一个层级标签下，排序规则不变。

### 3.2 单个技能的子菜单：项目随状态变化

已发布、有未发布的修改：

```
  gateway-ops · Agent
  v3 · unpublished changes · Runbook for this agent's gateway

❯ Publish v4            2 files changed since v3
  Show changes          Compare the draft with v3
  Discard draft         Keep v3, drop the unpublished changes
  Run it now            Load v3 into this turn and give it a task
  Improve it            Continue in the skill workshop
  Version history       3 versions · compare or restore
  Disable this skill    Stop loading it in new sessions
  Show details          Source, trust, path, and allowed tools

  ↑/↓ to navigate · Enter to confirm · Esc to go back
```

从没发布过的草稿：

```
  gateway-5xx · Agent
  Draft · not published yet

❯ Publish v1            Load it in new sessions, enabled
  Show changes          3 files
  Continue in workshop  Pick up where the workshop left off
  Discard draft         Delete the draft
```

- 已发布、没有草稿：沿用今天的四项，再加 `Version history`。
- `Version history` 的说明只写版本个数（只有一个版本时写 `1 version`），不列版本号，版本再多也是一行。
- 上游有更新：第一项是 `Pull the update into a draft`，说明写 `d41c8e0 · Oct 10`；拉取后变成"有未发布的修改"。

### 3.3 发布：确认页 → 发布说明 → 完成

```
  Publish gateway-ops v4?
  New sessions use v4; sessions already running keep v3.

  Changes since v3
    SKILL.md                   +6 −2
    references/runbook.md      +3 −0

❯ Publish v4
  Show changes
  Cancel
```

选 `Publish v4` 后：

```
  Release note for v4 (optional)
› Tighten the trigger description

  Enter to publish · Esc to go back
```

完成后回到列表，并留一行 `Published gateway-ops v4 · new sessions use it`。

### 3.4 查看改动：分文件的 diff 页

```
  gateway-ops · v3 → draft
  SKILL.md   +6 −2                                  1 of 2
  ──────────────────────────────────────────────────
    ## When to use
  - Any gateway endpoint returns 5xx.
  + A gateway endpoint returns 5xx and no deploy or config
  + change caused it.

  n/p next/previous file · ↑/↓ scroll · Esc to go back
```

增删行的配色沿用对话里 diff 卡的主题（`WithDiffTheme`）。

### 3.5 版本记录

```
  gateway-ops · versions
  v3 is current

❯ Draft     Based on v3 · 2 files changed
  v3        Current · Oct 9 18:20 · Published · Tighten the trigger
            · +12 −3
  v2        Sep 30 10:02 · Published · Add the health-check script
            · +40 −0
  v1        Sep 12 09:15 · Created · First version · +86 −0
```

- 新的在上。版本多时一次显示 20 条，往下翻到底自动加载更早的，用现有的 `SelectRichPaged`（`pkg/tui/overlays.go:4481`）。web 的版本标签同样先显示最近 20 条，底部"显示更早的版本"再加载。
- 在某个旧版本上按 Enter，弹出三项：`Compare with v3`（当前版本）、`Restore as a draft`、`Back`。
- 恢复前有确认页：

```
  Restore v2 as a draft?
  The draft becomes v2's content; publish it to make it current.
  The current draft will be replaced.

❯ Restore as a draft
  Cancel
```

最后一行"The current draft will be replaced."只在已有草稿时出现。

### 3.6 工坊结束后的提示

`Create a skill` 和 `Improve it` 仍然交给技能工坊，在当前对话里进行。工坊写完草稿、这一轮结束后，在对话里留一行 `Draft gateway-5xx saved · /skills to publish`，这是唯一的提示，不自动弹出面板。

## 4. 实现要点（写计划时展开）

- 技能加载（`pkg/skill`）跳过 `.versions/` 与 `.drafts/`。草稿的状态由目录推出：`.drafts/<名称>/` 存在就是有草稿，`.versions/<名称>/` 为空就是从没发布过。
- 发布要原子完成：先写 `.versions/<名称>/vN+1/` 和清单，再替换正式目录，最后删除草稿。中途失败时正式目录保持原样。
- 会话版本固定：冻结技能清单时（`pkg/run/skills.go:27-36`）记下每个技能的版本号；用到技能时（`pkg/run/skills.go:845`）读 `.versions/<名称>/v<k>/`，不再读正式目录。
- 工坊会话的写入目标：
  - web 菜单入口：主代理技能目录的 `.drafts/<名称>/`；
  - web 项目入口和 TUI：项目技能目录的 `.drafts/<名称>/`。

  skill-workshop / skill-generator 的提示词里，落点要改成草稿目录。
- 共享技能库没有工坊，只有安装、更新，以及在版本记录里恢复为草稿再发布。
