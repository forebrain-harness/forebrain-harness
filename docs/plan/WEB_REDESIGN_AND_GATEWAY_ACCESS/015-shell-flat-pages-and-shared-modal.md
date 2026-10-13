# 计划 015：外壳与页面结构——去掉页面标题层、共用弹窗与确认框、菜单栏底部一行、顶栏"新建主代理"、主代理下拉里删除、设置去掉"主代理"标签、代码高亮一律 monokai-light

> **执行者须知**：逐步执行，每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"中的任何情况，立即停止并报告。
> 完成后更新本目录 README 中本计划的状态行。**不要提交代码。**
> **1:1 还原设计稿**：`app-preview.html`（本目录，第 18 版，https://claude.ai/artifact/KKfPW8dXWScuRdTzonsk2F ）。本计划涉及的原型代码与跳转：
> - 页面第一行：`fh()` 函数与 `.flat-h` 样式。"本轮新增"里点"技能页""模型服务""权限""设置 · MCP"都能看到；
> - 弹窗：`.mask`、`.modal`、`.modal-h`、`.modal-b`、`.modal-f`，以及 `fmModal()` 的尺寸规则：普通 560px，`sm` 440px，编辑器 860px；
> - 列表：`.tb-wrap`、`.tb`、`.list.boxed`；
> - 菜单栏底部：`rail()` 的 `.r-foot`（左"设置"，右方形图标按钮，深色菜单栏显示太阳、浅色显示月亮）；
> - 顶栏：`topbar()`，以及"新建主代理（顶栏）""删除主代理（菜单栏下拉）"两个跳转；
> - 主代理下拉：`pops()` 的 `.t-menu`、`.t-row`、`.t-del`。
>
> 原型的 CSS 变量与前端 `frontend/src/assets/main.css` 的 token 一一对应（计划 003 建立的那套），按 token 写，不要写死色值。
>
> **漂移检查（先运行）**：
> - `grep -rn "ScopeBadge type=\"agent\"" frontend/src/views | wc -l` 大于 0（各页标题区仍带主代理标识）；
> - `grep -n "PrimaryAgentsTab" frontend/src/views/SettingsView.vue` 有结果；
> - `grep -n "forebrain-rail-scheme" frontend/src/App.vue` 有 1 行（底部两行文字的实现）。
>
> 不符就先对照"现状"，差别大则 STOP。

## 状态

- **优先级**：P1
- **工作量**：L
- **风险**：LOW–MED（每个页面的标题区都动；行为不变）
- **依赖**：无。其余 web 计划 016–020 都用本计划的共用组件，本计划先做
- **类别**：设计还原 / 结构
- **基线**：提交 `005c1a8` + 主树未提交改动，2026-10-11

## 为什么要做

owner 2026-10-11 的原话（逐字见本目录 README 第 36–44 条）：
- "所有页面的页面标题层都去掉，太多余"；"检查所有页面，都要减少层级"；
- "不需要每个页面都显示主代理 · main……都删掉"；
- "设置页的主代理页直接删掉，把新建主代理按钮移到顶部导航条，新建主代理后，自动切换当前主代理为新建的主代理"；"新建主代理弹窗只需要设置名称即可"；
- "web端不论主题色是什么，代码高亮统一使用monokai-light主题"；
- 菜单栏底部、表单聚焦与设计稿不一致，必须 1:1 还原（`plans/README.md` 偏差清单第 6、7、9、11 条）。

## 现状（已核实）

- **页面标题区**：每个页面都自己写一段 `<header>`，内容为 h1 + `ScopeBadge` + 说明 + 右侧按钮。例子是 `frontend/src/views/ProvidersView.vue:4-20`，同样结构在：
  - `RulesView`、`SkillsView`、`MemoriesView`、`PermissionsView`、`ToolsView`、`SubagentsView`、`ProjectsView`、`SettingsView`、`CronView`、`ChannelsView`；
  - 项目空间各标签组件 `components/project/*.vue`。

  用 `grep -rln "ScopeBadge" frontend/src --include='*.vue'` 找全。没有共用的标题组件。
- **弹窗**：没有共用组件，各处自己写遮罩加卡片，例如 `components/skills/InstallDialogs.vue:2-8`（`fixed inset-0 z-50 ... bg-black/30`）。
- **菜单栏底部**：`App.vue:139-168`。"设置"链接和"菜单栏切换为深色"按钮各占一行，按钮里带文字 `forebrain-rail-scheme`。
- **表单聚焦**：`main.css:705-716`（`:focus-visible` 规则）、`:743`（`.forebrain-field:focus`）。设计稿不写单独的聚焦样式，用浏览器默认外框。
- **主代理**：
  - 设置页"主代理"标签：`components/settings/PrimaryAgentsTab.vue`，由 `SettingsView.vue` 引入；
  - 后端：`POST /api/agents/primary` 只要求合法的 ID（`pkg/gateway/agents_api.go:485` 起，`config.ValidPrimaryAgentID`），工作区由 ID 推导，可选的 `workspace_root` 只做一致性校验；
  - 删除：`DELETE /api/agents/primary/:id`；切换：`POST /api/agents/primary/switch`（`pkg/gateway/api_extra.go:73-76`）；
  - 菜单栏顶部的主代理下拉在 `App.vue` 里，`grep -n "tenant\|primary" frontend/src/App.vue | head` 找。
- **代码高亮**：`ai-elements-vue/packages/elements/src/code-block/CodeBlockContent.vue:77`、`:87` 用 Tailwind 的 `dark:` 变体，在系统暗色时切到 monokai 深底。浅色主题本身来自同目录的 `theme.ts` / `monokai-light.ts`，与 TUI 的 chroma `monokailight` 同色。
- 主代理工作区路径固定：main 为 `~/.forebrain/workspace`，其余为 `~/.forebrain/workspaces/<名称>`（`pkg/config/primary_agent.go:304-306`）。

## 命令

| 用途 | 命令 | 预期 |
|---|---|---|
| 前端 | `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 无输出 |
| 组件库 | `cd ai-elements-vue && pnpm -r test`（若该包有测试；先 `cat ai-elements-vue/package.json` 看脚本） | 全绿 |
| 构建 | `make ui` | 成功 |
| 验收 | `scripts/acceptance/web_e2e.sh` | 全部 PASS |

## 范围

**改**：
- 新建 `frontend/src/components/common/{PageTopRow.vue,ForebrainModal.vue,ConfirmDialog.vue}`；
- `frontend/src/assets/main.css`；
- `App.vue`；
- 所有页面与项目空间标签组件的标题区；
- `SettingsView.vue`，删除 `PrimaryAgentsTab.vue`；
- `CodeBlockContent.vue`；
- locale；
- 测试；
- `pkg/gateway/dist`。

**不改**：
- 各页面标题行以下的内容。列表改弹窗在 018；对话页在 016；工作台在 017；
- 后端接口。

## 步骤

### 步骤 1：三个共用组件

1. `PageTopRow.vue`：
   - props `title`、`description?`，插槽 `actions`；
   - 结构照 `.flat-h`：标题 18px、650 字重；说明 13px、次要色；按钮靠右，可换行。
2. `ForebrainModal.vue`：
   - props `open`、`title`（可传插槽）、`size: 'sm'|'md'|'xl'`，宽度分别为 440、560、860px，高度上限照原型；
   - 插槽 `default`（`.modal-b`）、`footer`（`.modal-f`）；
   - 右上角关闭按钮（`x` 图标，`aria-label="关闭"`）；
   - 遮罩点击、Esc 都只关闭**最上层**的弹窗，原型的 `closeTopModal()`。用一个模块级的栈记录打开顺序；
   - 打开时焦点移进弹窗，关闭后还给触发者。
3. `ConfirmDialog.vue`：
   - 基于 `ForebrainModal size="sm"`；
   - props `title`、`message`、`confirmLabel`、`danger?`；
   - 可选 `typedConfirm`：要求输入一段文字才可确认，删除项目用；
   - 确认按钮在 `danger` 时用危险色。
4. 在 `components/gallery/ComponentGallery.vue`（计划 003 建的组件展示页）里加三个组件的示例。

**验证**：每个组件一份单测：
- 栈式关闭：开两层时 Esc 只关上层；
- `typedConfirm` 未输入时确认按钮禁用。

### 步骤 2：去掉页面标题层

1. 每个页面的 `<header>` 换成 `PageTopRow`，**去掉 `ScopeBadge`**；
   - 设置页也不再显示"全局"；
   - 顶栏已显示页面名，页面里不再放 h1 大标题。
2. "单一内容不套卡片"：标题行下面只有一个列表或表格的页面，去掉外层的卡片边框和卡片标题，原卡片标题与页面标题重复。按钮从卡片头移到 `PageTopRow` 的 `actions`。有多个分组的页面，每组仍可用一层卡片或 `.flat` 段。逐页对照原型的结构：
   - 技能、模型服务、权限、记忆、规则指令、子代理、工具、项目；
   - 设置的各标签；
   - 项目空间的各标签。
3. 项目空间头部：照原型 `pages.project` 的 `.proj-head`，一行面包屑"项目 › 名称"加目录与"已信任"标记，下面是标签栏。去掉大标题和黑底"项目"标签（计划 008 做的深色横条一并去掉）。
4. 表格、列表外观统一用 `.tb-wrap` / `.tb`、`.list.boxed` 对应的类，写进 `main.css`，命名沿用仓库的 `forebrain-` 前缀。

**验证**：
- `grep -rn "ScopeBadge" frontend/src --include='*.vue'` 只剩表格里的"来源"列，例如技能列表；
- 1440×900 下每个页面截图，与原型同一页并排，放进报告。

### 步骤 3：菜单栏底部、表单聚焦

1. `App.vue:139-168` 改成同一行：左边"设置"链接，右边 28px 方形图标按钮（深色菜单栏显示太阳、浅色显示月亮，不带文字，`aria-label` 与提示文字照旧）。折叠态照原型 `[data-rc="1"] .r-foot`，竖排、居中。删掉 `forebrain-rail-scheme` 及其样式。
2. 删除 `main.css:705-716` 与 `:743` 的自定义聚焦样式，回到浏览器默认外框（owner 2026-10-11 试过自定义样式后要求改回原稿效果）。改之前先 `grep -rn "focus-visible\|:focus" frontend/src --include='*.css' --include='*.vue'`，列出全部聚焦样式，只删输入控件与按钮的那几条，键盘导航用的组件内部样式保留，在报告逐条写明。

### 步骤 4：主代理——顶栏新建、下拉里删除、设置去掉标签

1. **顶栏**（各页面共用的顶栏，在 `App.vue` 或其布局组件里）：
   - 语言按钮左侧加"＋ 新建主代理"按钮，样式同顶栏其他按钮，每个页面都有；
   - 点击打开 `ForebrainModal size="sm"`：只有一个"名称"输入框，占位"小写字母、数字与短横线，如 docs"，下方一行提示"创建后切换到这个主代理。"，按钮"取消""创建"；
   - 创建：`POST /api/agents/primary {id: 名称}`，成功后立即调用切换接口，并执行今天切换主代理时的前端范围重置（计划 007 的做法，`grep -rn "switchPrimary\|primarySwitch" frontend/src | head`）；
   - 失败：409 显示"这个名称已存在"，400 显示"名称只能用小写字母、数字与短横线"。
2. **主代理下拉**：每一行右侧，对**不是 main、也不是当前主代理**的行，显示删除图标按钮（`trash-2`）。点击打开 `ConfirmDialog danger`：
   - 标题"删除主代理 X？"；
   - 正文"删除后不能恢复；工作区目录里的文件不会删除。"。

   删除规则与后端一致：main 与当前主代理不能删。行的结构照 `.t-row`：选项按钮占满，删除按钮另放，不要把按钮嵌在按钮里。
3. **设置页**：删掉"主代理"标签与 `PrimaryAgentsTab.vue`（"所有主代理共享的目录"那块信息一并去掉）。设置的默认标签改为"外观"。
4. 原 `PrimaryAgentsTab` 用到、别处已不再用的 API 封装与 locale 键一并删除。

**验证**：
- 单测：顶栏新建 → 调用创建与切换；下拉对 main 和当前主代理不显示删除按钮。
- e2e：新建一个主代理 `e2e-docs` → 菜单栏顶部显示它 → 下拉里删掉它，删除前先切回 main。

### 步骤 5：代码高亮一律 monokai-light

1. `CodeBlockContent.vue:77`、`:87` 去掉 `dark:` 两个类，代码块只用浅色主题的变量。
2. `theme.ts` 的 `codeThemePair` / `markdownShikiOptions` 仍为两套主题，渲染时永远取 light：
   - 若组件库别处还按暗色选主题（`grep -rn "shiki-dark\|codeThemeDark" ai-elements-vue/packages frontend/src | grep -v test`），一并改成只用 light；
   - `frontend/src/lib/codeTheme.test.ts` 的断言按新规则改：monokai 深色不再参与 web 渲染。
3. 文件编辑器的高亮（计划 019）同一套颜色，本步不涉及。

**验证**：把系统切到暗色、刷新对话页，代码块仍是浅底（e2e 里用 `page.emulateMedia({ colorScheme: 'dark' })`）。

### 步骤 6：构建与验收

`make ui`；`scripts/acceptance/web_e2e.sh` 全绿；新增用例：
- 主代理新建与删除；
- 暗色系统下代码块浅底；
- 菜单栏底部一行。

## 测试计划

见各步验证。

## 完成标准

- [ ] 命令表全部通过
- [ ] 每个页面与原型的并排截图写进报告，差异逐条说明
- [ ] `grep -rn "PrimaryAgentsTab\|forebrain-rail-scheme" frontend/src` 无输出
- [ ] `git status --short` 中本计划新增的改动只涉及范围列出的文件

## STOP 条件

1. 漂移检查差别大。
2. 某个页面去掉外层卡片后，有不属于本计划的内容无处可放：例如卡片头里有一个本计划没提到的开关。停下报告。
3. 删除自定义聚焦样式会让某个组件完全没有聚焦指示：浏览器默认外框被别处 `outline: none` 盖掉了。停下报告是哪一处。

## 维护说明

- 新页面一律用 `PageTopRow` + 内容，不写 h1 标题区，不加主代理标识。
- 新弹窗一律用 `ForebrainModal` / `ConfirmDialog`。新建、编辑表单都在弹窗里，不做"点按钮页内展开、点取消收起"的组件（owner 规则）。
