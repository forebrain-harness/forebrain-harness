# 计划 017：工作台——概览 / 上下文 / 工作目录 / 变更文件四个标签；目录树每个节点可编辑、删除，目录可新建文件；变更文件可对比、编辑、撤销变更

> **执行者须知**：逐步执行，每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"中的任何情况，立即停止并报告。
> 完成后更新本目录 README 中本计划的状态行。**不要提交代码。**
> **1:1 还原设计稿** `app-preview.html`（第 18 版）。"本轮新增"里依次点：
> - "工作台 · 概览"
> - "工作台 · 上下文"
> - "上下文接近上限"
> - "工作台 · 工作目录"
> - "工作台 · 变更文件"
> - "工作目录 · 编辑文件"
> - "工作目录 · 删除目录"
> - "变更文件 · 撤销变更"
>
> 对应原型函数：`bench()`、`benchOverview()`、`benchContext()`、`treeCard()`、`treeHtml()`、`tnActs()`、`chatChangesCard()`、`chgRow()`、`treeForm()`；CSS：`.bench`、`.bench-tabs`、`.tn-row`、`.tn-acts`、`.chg`、`.ctx-big`、`.meter`、`.acc`。
>
> **漂移检查（先运行）**：以下各项都已完成，否则 STOP：
> - 015：共用弹窗；
> - 019：文件编辑器组件；
> - `plans/022`：`filesRoot`。检查 `grep -n "func (s \*Server) filesRoot" pkg/gateway/session_files.go`；
> - `plans/033`：`GET /api/chat/sessions/:id/file-changes`。检查 `grep -n "file-changes" pkg/gateway/api_extra.go`；
> - `plans/036`：`ResolveSkillDraftTarget`。

## 状态

- **优先级**：P1
- **工作量**：L
- **风险**：MED（新增可写文件、删目录的接口）
- **依赖**：015、019、`plans/022`、`plans/033`、`plans/036`
- **类别**：设计还原 / 功能
- **基线**：提交 `005c1a8` + 主树未提交改动，2026-10-11

## 为什么要做

owner 2026-10-11：

> 会话页面和技能工坊页面的工作台的文件标签拆分出两个标签："工作目录"和"变更文件"，且工作目录树里的节点包括目录节点和文件节点的右侧都必须加上编辑按钮和删除按钮，编辑目录是给目录重命名，编辑文件包括重命名文件和修改文件内容，删除目录就把整个目录及其子目录和文件都删除，删除文件只删除该文件。目录节点多加一个加号图标，可以新建文件。变更文件就是现在的技能文件，删掉+新建文件按钮……这里每一份文件右侧多加一个撤销变更的按钮

此前还定了：
- 输入框上方的诊断面板移进工作台；
- 工作台不放工具调用，工具调用在消息窗口里实时显示；
- 不放 Working Set（`plans/032` 删除）。

技能正式目录的改动自动进草稿（`plans/036`）。

## 现状（已核实）

- `components/chat/ChatWorkbench.vue`：没有标签。宽 300px，内容是 `AgentRosterPanel`（有运行中代理时）、`WorkspaceTree`、一行"点击文件可插入引用"的提示。
- 可用的数据接口：
  - 待办 `GET /api/chat/sessions/:id/todos`；
  - 计划 `GET .../plan-md`；
  - 上下文 `GET .../context`（`ContextDebugPanel` 今天用的就是它）；
  - 用量 `GET .../cost-summary`（`pkg/gateway/api_extra.go:108-115`）；
  - 运行中的代理：现有的 roster。
- MCP 状态：运行时有 `Runner.MCPStartup()` 与 `MCPProjectStatus()`（`pkg/run/runner.go:2754`、`:3050`）。gateway 是否已有会话级 MCP 状态接口，用 `grep -rn "mcp" pkg/gateway/api_extra.go | grep "Get(" | head` 查；没有就在步骤 2 加。
- 目录树：`WorkspaceTree.vue` / `WorkspaceRow.vue` 与 `useWorkspaceTree`（`plans/022` 改成按会话或项目取根），后端只读。
- 没有新建、改名、删除、写入文件的接口。

## 命令

| 用途 | 命令 | 预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/skill/... -count=1` | `ok` |
| 前端 | `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 无输出 |
| 构建 | `make ui` | 成功 |
| 验收 | `scripts/acceptance/web_e2e.sh` | 全部 PASS |

## 范围

**改**：
- `ChatWorkbench.vue`（重写为四个标签），新建：
  - `components/workbench/{OverviewTab.vue,ContextTab.vue,WorkingDirTab.vue,ChangedFilesTab.vue,TreeNodeActions.vue,FileEditDialog.vue}`；
- `WorkspaceTree.vue`、`WorkspaceRow.vue`；
- gateway 新增文件操作接口，必要时补会话 MCP 状态接口；
- 删除 `ContextDebugPanel.vue` 中只在旧面板里用的部分，或整个组件：它被 subagent 视图的 `/context` 复用，见步骤 3；
- locale；测试；`pkg/gateway/dist`。

**不改**：
- 技能工坊多出的四个标签（020）；
- 回到这里（`plans/033`）；
- 工具调用的显示。

## 步骤

### 步骤 1：标签外壳

- 照原型 `bench()`：头部"工作台"加关闭按钮；标签栏"概览 / 上下文 / 工作目录 / 变更文件"；默认"概览"。
- 宽度 300px，工坊页 420px，由 020 传参。
- 记住上次打开的标签，按会话存，存浏览器本地即可。属于界面状态，不落库。
- 打开 / 收起的行为不变，默认收起，计划 005 的规则。

### 步骤 2：概览

照 `benchOverview()`，五张小卡片，由上到下：

1. **运行中的代理**：没有就不显示这张卡片（计划 005 的规则）；右上角"全部停止"。
2. **待办**：标题旁 `3/5`，逐项图标为完成 / 进行中 / 未开始。
3. **计划**：有计划时显示标题和要点，右上角"查看全文"打开弹窗，用 `ForebrainModal`。
4. **MCP 服务**：右上角小字"开对话时启动"；每个服务一行，状态徽标为"已连接 · N 个工具"或"启动超时"等。数据来自会话的 MCP 状态，没有接口就加 `GET /api/chat/sessions/:id/mcp`。
5. **用量**：右上角"本对话累计"；输入 / 输出 tokens、模型请求次数、缓存命中率，以及命中率条。

### 步骤 3：上下文

照 `benchContext()`：

1. 卡片"上下文占用"：
   - 大号百分比，"已占可用窗口"，右侧"正常"或"接近上限"徽标，≥80% 为后者；
   - 三段比例条（会话历史 / 注入内容 / 输出预留）加图例；
   - 接近上限时提示"下一轮前会自动压缩较早的对话。"；
   - 行：估算输入、可用窗口、剩余预算、模式。
2. 折叠区：注入条目、已淘汰、压缩与恢复、大工具输出，每项带数量；默认展开"注入条目"。
3. 数据来自现有 `/context` 接口，字段对应关系对照 `ContextDebugPanel.vue` 现在怎么读。原型里没有的字段不显示。
4. `ContextDebugPanel.vue`：subagent 视图的 `/context` 仍在用（`ChatView.vue:64`），改成复用本标签的同一组展示组件，删掉旧样式。Working Set 区块由 `plans/032` 删除。

### 步骤 4：工作目录（树上的操作）

1. **界面**，照 `treeCard()` / `tnActs()`：
   - 卡片头"工作目录"，右侧"在根目录新建文件"（`file-plus`）与刷新；
   - 下面一行小字显示工作目录路径；
   - 树的每一行右侧：
     - 文件：编辑（`pencil`）、删除（`trash-2`）；
     - 目录：新建文件（`plus`）、重命名（`pencil`）、删除；
   - 图标常显，次要色；悬停加深；删除图标悬停变危险色。
   - 点文件名照旧插入 `@` 引用；底部提示"点击文件插入 @ 引用"。
2. **弹窗**，照 `treeForm()`，用 015 的 `ForebrainModal` / `ConfirmDialog`：
   - 新建文件：显示所在目录，输入"文件名"（可含子目录，如 `docs/a.md`）→"创建"，然后直接打开编辑文件弹窗；
   - 编辑文件（`FileEditDialog`）：标题"编辑文件"；"文件名"输入框（改名）与所在目录；下面是 019 的编辑器（按扩展名选富文本或代码）；"取消""保存"。改名与改内容在一次保存里完成，先写内容、再改名，失败时都不生效；
   - 重命名目录：输入"目录名"→"保存"；
   - 删除文件：`ConfirmDialog danger`，标题"删除文件 X？"，正文显示路径与"删除后不能恢复。"；
   - 删除目录：同上，正文"目录里的全部文件和子目录会一起删除，不能恢复。"。
3. **接口**，新文件 `pkg/gateway/files_write.go`，根一律取 `plans/022` 的 `filesRoot(ctx, session_id, project_id)`：

   | 方法 | 路径 | body | 说明 |
   |---|---|---|---|
   | GET | `/api/workspace/file` | `?path=` | 读文本内容；超过 1 MiB 拒绝，编辑器只处理文本 |
   | PUT | `/api/workspace/file` | `{path, content, create?: bool}` | 写入或新建；`create` 时已存在返回 409 |
   | POST | `/api/workspace/rename` | `{from, to}` | 文件或目录改名，目标已存在返回 409 |
   | DELETE | `/api/workspace/entry` | `?path=` | 删除文件或目录（递归） |

   规则：
   - 路径必须在根内，与 022 同样的 `ResolveWithinRoots`，防符号链接逃逸。不允许删除或改名根本身；
   - 拒绝 `.git` 及其内部；树本来就隐藏 `.git`（计划 006）；
   - 路径落在技能正式目录内 → 调 `plans/036` 的 `ResolveSkillDraftTarget` 转进草稿；
   - 删除整个技能目录 → 400"删除技能请在技能列表里操作"；
   - 指向 `.versions/` 的一律拒绝；
   - 会话正在运行时也允许操作。界面上的编辑与模型的文件工具可能冲突，这与在编辑器里改文件一样，不加锁。在维护说明里写明。
4. 操作成功后刷新树里受影响的目录。改名后若打开着该文件的编辑器，标题同步。

### 步骤 5：变更文件

照 `chatChangesCard()` / `chgRow()`：

- 卡片头"变更文件"，右侧"N 个"。**没有**"新建文件"按钮（owner）。
- 每个文件两行：
  - 第一行路径；
  - 第二行左边增删行数或"新增"徽标，右边"对比""编辑""撤销变更"；
  - 新增的文件没有"对比"。
- 底部一行"本会话用文件工具改过的文件。"
- 数据：`plans/033` 的 `GET /api/chat/sessions/:id/file-changes`。
- "对比"：`ForebrainModal` 显示 `.../file-changes/diff?path=` 的统一 diff，样式沿用 `DiffView.vue`。
- "编辑"：打开步骤 4 的 `FileEditDialog`。
- "撤销变更"：`ConfirmDialog danger`：
  - 标题"撤销 X 的变更？"；
  - 正文为路径，以及"恢复到本会话修改之前的内容。"（新建的文件写"这个文件是本会话新建的，撤销即删除。"）；
  - 确认后调 `.../file-changes/revert`，列表与树同步刷新。

### 步骤 6：测试

- gateway：
  - 读写、新建 409、改名、递归删除；
  - 越界、符号链接逃逸、`.git`、删除根，都被拒绝；
  - 技能正式目录写入落进草稿；删除整个技能目录与写 `.versions` 被拒。
- 前端：
  - 四个标签的切换与记忆；
  - 概览在没有运行中代理时不显示那张卡片；
  - 上下文 ≥80% 显示"接近上限"；
  - 树节点操作按钮：文件两枚，目录三枚；
  - 新建 → 编辑弹窗；删除目录的确认文案；
  - 变更文件三个按钮，新增文件无"对比"。
- e2e：
  - 在项目里新建 `notes/a.md`，写内容、改名为 `b.md`，再删除 `notes/`，确认磁盘上都对；
  - 让模型改一个文件后，"变更文件"出现它，撤销后磁盘恢复。

## 测试计划

见步骤 6。

## 完成标准

- [ ] 命令表全部通过
- [ ] 与原型 8 个跳转状态的并排截图写进报告
- [ ] `git status --short` 中本计划新增的改动只涉及范围列出的文件

## STOP 条件

1. 漂移检查列出的依赖未完成。
2. 会话的 MCP 状态在 gateway 进程里拿不到，例如只在 TUI 里有：停下报告，概览的 MCP 卡片先不做。
3. `ContextDebugPanel` 被本计划范围之外的地方复用，而且样式改动会影响那里。

## 维护说明

- 树上的编辑与模型的文件工具没有互斥：用户在工作台改文件的同时模型也在写，后写的生效，与在外部编辑器里改文件相同。
- 树上的编辑不进"变更文件"，也不进"回到这里"：那两者只记文件工具的改动（owner 定的范围）。
