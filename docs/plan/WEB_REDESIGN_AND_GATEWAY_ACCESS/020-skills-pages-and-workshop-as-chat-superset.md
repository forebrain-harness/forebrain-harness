# 计划 020：技能页与技能工坊——技能列表显示版本与状态、发布/对比/恢复/丢弃/拉取更新的弹窗；工坊页改成会话页的超集，工作台多出测试用例、评测、触发描述、版本四个标签

> **执行者须知**：逐步执行，每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"中的任何情况，立即停止并报告。
> 完成后更新本目录 README 中本计划的状态行。**不要提交代码。**
> **1:1 还原设计稿** `app-preview.html`（第 18 版）。"本轮新增"里依次点：
> - 技能页：
>   - "技能页"、"离线安装（弹窗）"、"在线安装（弹窗）"；
>   - "工坊 · 新建（空白）"、"工坊 · 新建进行中"、"工坊 · 改进已有技能"；
>   - "工坊 · 工作目录"、"工坊 · 变更文件"、"工坊 · 编辑 SKILL.md（属性 + 富文本）"、"工坊 · 编辑脚本（代码）"；
>   - "工坊 · 测试用例"、"工坊 · 评测"、"工坊 · 评测运行中"、"工坊 · 评测详情（弹窗）"、"工坊 · 触发描述"；
>   - "工坊 · 版本"、"发布（弹窗）"、"版本对比"、"上游有更新"、"工坊 · 发现并安装"、"工坊 · 项目入口"；
> - 项目空间："项目 · 技能"。
>
> 原型代码：
> - 技能页：`skillsBlock()`、`verChip()`、`versionsPane()`、`pubModal()`、`cmpModal()`、`verConfirmModal()`、`instModal()`；
> - 工坊：`wsEnter()`、`wsTopbar()`、`wsView()`、`wsThread()`、`bench(true)`、`wsChanges()`、`wsCases()`、`wsEvals()`、`wsTrigger()`、`wsForm()`。
>
> **漂移检查（先运行）**：以下各项都已完成，否则 STOP：
> - 015、016、017、019；
> - `plans/036`：技能版本接口。检查 `grep -n "publish" pkg/gateway/api_extra.go`。

## 状态

- **优先级**：P1
- **工作量**：XL
- **风险**：MED（工坊页整页重写）
- **依赖**：015、016、017、019、`plans/036`；项目入口依赖 `plans/029`（会话带项目）
- **类别**：设计还原 / 功能
- **基线**：提交 `005c1a8` + 主树未提交改动，2026-10-11

## 为什么要做

owner 2026-10-11：
- "技能工坊页面的功能过于简单，必须改成会话页面的超集，复用会话页面的composer，并在会话页面的工作台基础上增加……"；
- 工作台的文件标签拆成"工作目录""变更文件"；
- 技能工坊的入口以按钮形式放在主代理技能页和项目空间技能页，菜单栏与项目空间不再单列入口；
- "发现并安装"与"在线安装"都保留；
- 草稿 / 发布 / 版本的语义见 `docs/plan/SKILL_DRAFTS_AND_VERSIONS_DESIGN.md`；
- 技能页嵌套太多，015 已压平，本计划补列表内容。

## 现状（已核实）

- **工坊页** `views/WorkshopView.vue`（294 行）：
  - 三栏：任务列表（200px）、对话、`WorkshopSkillPanel`（320px）；
  - 标题区 h1 + `ScopeBadge`；
  - 自己画消息块，不是会话页的组件；
  - 新建任务调 `chatSessionCreate(..., 'workshop')`（`:258`）。
- **工坊技能面板** `components/workshop/WorkshopSkillPanel.vue`：用 `skillFilesList`、`skillFileRead`、`skillFileWrite`、`skillDownload`。
- **技能列表**：`views/SkillsView.vue`、`components/skills/SkillsTable.vue`、`components/project/ProjectSkills.vue`、`components/settings/SettingsSharedSkillsTab.vue`；安装弹窗 `components/skills/InstallDialogs.vue`。
- **工坊评测产物**的格式由内置技能 `pkg/skill/system_assets/skill-workshop/SKILL.md` 约定：
  - 测试用例：技能里的 `evals/evals.json`（`:154`）；
  - 评测结果：技能目录旁的 `<名称>-workspace/iteration-N/eval-<名>/{with_skill,without_skill|old_skill}/outputs/`、各运行目录的 `grading.json`、`iteration-N/benchmark.json`（`:176-240`）；
  - 用户反馈：`feedback.json`，结构 `{run_id, feedback, timestamp}`（`:256-283`）；
  - 触发优化：评测集 `[{query, should_trigger}]`（`:350-353`），由 `scripts.run_loop` 跑，输出里有 `best_description` 与分数（`:384-415`）。
- 评测执行仍由模型跑技能自带脚本（计划 011 的原则：不新增 Go 评测流水线）。

## 命令

| 用途 | 命令 | 预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/skill/... -count=1` | `ok` |
| 前端 | `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 无输出 |
| 构建 | `make ui` | 成功 |
| 验收 | `scripts/acceptance/web_e2e.sh` | 全部 PASS |

## 范围

**改**：
- `views/WorkshopView.vue`（重写）；
- 删除 `components/workshop/WorkshopSkillPanel.vue`，功能并入工作台标签；
- 新建 `components/workshop/{WorkshopTopbar.vue,CasesTab.vue,EvalsTab.vue,TriggerTab.vue,VersionsTab.vue,PublishDialog.vue,CompareDialog.vue,EvalCaseDialog.vue}`；
- `ChatWorkbench.vue`：接受额外标签；
- 技能列表四处；
- gateway 新增工坊产物读取接口；
- `pkg/skill/system_assets/skill-workshop/SKILL.md`：触发评测集的存放位置；
- locale；测试；`pkg/gateway/dist`。

**不改**：
- 版本引擎（`plans/036`）；
- TUI（`plans/037`）。

## 步骤

### 步骤 1：技能列表

照 `skillsBlock()`（015 已把页面压平）：
1. **标题行按钮**：离线安装、在线安装、发现并安装、新建技能（主按钮）、刷新。共享技能库只有前两个。
2. **表格列**：
   - 勾选；
   - 技能：名称，名称旁可能有"有未发布的修改"或"有更新"徽标，下一行说明；
   - 来源；
   - 状态：版本号徽标 + 启用开关；草稿只显示"草稿"徽标，没有开关；
   - 操作："详情"、下载、删除。
3. **规则**：
   - 草稿不能启用；
   - 继承来的行只读（计划 010 的规则）；
   - 上游有更新的共享技能也显示"详情"。
4. "详情"打开工坊并改进该技能；"新建技能""发现并安装"打开工坊对应模式。主代理技能页打开主代理级工坊，项目空间技能标签打开项目级工坊。

### 步骤 2：工坊页 = 会话页 + 差异

1. 路由 `/workshop` 与项目空间里的工坊入口，统一打开整页工坊。
   - 菜单栏高亮来源页：主代理入口高亮"技能"，项目入口高亮"项目"；
   - 左上角返回来源页。
2. **顶栏**（`wsTopbar()`）：
   - 返回按钮，面包屑"技能 › 名称"（新建时为"新建技能"，发现时为"发现并安装"）；
   - 版本状态（`verChip()`）；
   - 项目入口时显示"项目 · 名称"标签，主代理入口不显示；
   - 模型名；
   - 右侧：心跳、"新会话"图标按钮、工作台按钮、"发布 vN"主按钮（没有未发布的修改时禁用，提示"没有未发布的修改"）、新建主代理、语言。
3. **主区域**：直接复用会话页的消息流与输入框（016 之后的组件），不另写消息块。
   - 输入框不显示项目选择；
   - 空白状态照原型：
     - 新建："想做一个什么技能？"加三个快捷入口；
     - 改进："想怎么改 <名称>？"加三个快捷入口；
     - 发现："想找一个什么样的技能？"，没有快捷入口。
4. **会话**：
   - "详情"接着这个技能最近的工坊会话；没有就开新会话。"最近"按该技能名查 `source='workshop'` 的会话，在会话标题或元数据里记技能名，看今天怎么记（`grep -n "workshop" pkg/gateway/api_extra.go | head`）；
   - "新会话"按钮开一个新的工坊会话；
   - 主代理入口的会话不带项目（`source='workshop'`，工作目录为主代理技能目录，`plans/027`）；
   - 项目入口的会话带项目。
5. **工作台**：宽 420px，八个标签：概览、上下文、工作目录、变更文件、测试用例、评测、触发描述、版本。
   - 前四个复用 017。工坊的"变更文件"列草稿相对当前版本的改动，接口 `plans/036` 的 `diff?from=vN&to=draft`；不是会话的文件工具记录；
   - 后四个是本计划新增的；
   - 还没有技能（空白新建、发现阶段）时，后四个禁用；
   - 工坊的"工作目录"：主代理入口根为主代理技能目录，项目入口为项目根（`plans/022` 的 `filesRoot`）。

### 步骤 3：四个工坊标签

1. **测试用例**（`wsCases()`）：
   - 标题行右侧"添加用例"；
   - 每个用例显示序号、提问、"期望：…"，操作为编辑、删除；
   - 新建、编辑在弹窗里：提问、期望结果两个字段；
   - 底部一行 `evals/evals.json`；
   - 读写技能草稿里的 `evals/evals.json`，用已有的技能文件接口，写入落在草稿里（`plans/036`）。
2. **评测**（`wsEvals()`）：
   - 标题行：轮次下拉（`iteration-N`，新的在上）、"运行评测"；
   - 汇总徽标"使用技能 a/n""不用技能 b/n"，加用时；
   - 表格列：用例、使用技能（通过 / 未通过）、不用技能、反馈（有反馈时显示图标，否则"—"）、"查看"；
   - "查看"打开 `EvalCaseDialog`（`wsForm()` 的 `wseval`）：提问与期望；两栏对比两次运行的输出与判定；"你的反馈"文本框。保存写进该轮的 `feedback.json`，格式照 SKILL.md 约定；
   - "按反馈再改一轮"只把"按第 N 轮评测的反馈再改一轮。"填进输入框，不直接发送；
   - "运行评测"在输入框里填"跑一轮评测"并**直接发送**，由工坊模型执行。
   - 运行中（本会话有运行中的 eval subagent）显示进度条"运行中 x/y"与"停止"。进度取该轮已有的运行目录数与用例数之比。
   - 数据来自新接口：步骤 4。
3. **触发描述**（`wsTrigger()`），三张卡片：
   - "触发描述"：当前草稿的 `description`，右上角"编辑"打开弹窗改，写回 SKILL.md 的 frontmatter；下方"决定何时自动加载这个技能。"；
   - "触发测试"：
     - 当前描述与建议描述的准确率（大号数字，中间箭头）；
     - 提问列表，每行"应触发 / 不应触发"徽标、提问、判对或判错图标，判错的排前面，默认显示 5 条加"显示全部 N 条"；
     - 右上角"添加提问"（弹窗：提问、应触发与否）、"重新测试"（填"重新跑一遍触发测试"并发送）；
     - 没测过时显示"还没测过。"与"运行触发测试"；
   - "建议描述"：有结果时显示 `best_description`，"应用到草稿"把它写进草稿的 SKILL.md；"重新生成"填一句话发送。
   - **触发评测集的位置**改为技能草稿里的 `evals/trigger_queries.json`，随版本走，原型如此。修改 `skill-workshop/SKILL.md` 第 350–380 行一带：让模型把评测集存这里、跑 `run_loop` 时用它，结果 JSON 存进 `<名称>-workspace/trigger-results.json`。改后跑 `pkg/skill` 的内置资源测试。
4. **版本**（`versionsPane()`）：
   - 卡片头右侧"下载 zip"；
   - 上游有更新时顶部一行提示加"拉取到草稿"；
   - 草稿行在最上方：基于 vN、改了几个文件；有"对比""丢弃"；
   - 每个版本一行：版本号、来源与说明、"当前"徽标、时间与改动统计；"对比"（与上一版）、"恢复为草稿"（非当前版本）、下载图标；
   - 版本多时一次显示 20 条，底部"显示更早的版本"（owner 2026-10-11）。

### 步骤 4：工坊产物的读取接口

新接口，按工坊所在层定位技能根，与 `plans/036` 的路径规则一致：
- `GET .../skills/:name/workshop/evals` →
  - 轮次列表；
  - 每轮每个用例两种配置的通过情况（读 `grading.json` 的 `expectations[].passed`）、输出文本，截断到 64 KiB；
  - 用时（`benchmark.json`）；
  - 已有反馈（`feedback.json`）。
- `PUT .../skills/:name/workshop/evals/:iteration/feedback` `{run_id, feedback}` → 合并写进 `feedback.json`。
- `GET .../skills/:name/workshop/trigger` → 评测集（草稿里的 `evals/trigger_queries.json`）与最近一次结果（`trigger-results.json`）。
- 路径越界检查同 `plans/022`；workspace 目录不存在时返回空列表。
- 文件格式与 SKILL.md 的 `references/schemas.md` 一致：先读它，以它为准。字段名不一致 → STOP。

### 步骤 5：弹窗

- `PublishDialog`（`pubModal()`）：
  - 标题"发布 <名称> vN+1"；
  - 改动文件列表；"发布说明（可选）"；
  - 一行"新会话用 vN+1，进行中的会话继续用 vN。"（首次发布为"发布后新会话即可加载，默认启用。"）；
  - "取消""发布"。
- `CompareDialog`（`cmpModal()`）：左边文件列表加增删行数，右边 diff，宽 820px。
- 恢复为草稿、丢弃草稿：`ConfirmDialog`，文案照 `verConfirmModal()`；已有草稿时恢复要写"当前草稿会被替换"。
- 安装弹窗沿用 `InstallDialogs.vue`，外观换成 `ForebrainModal`。

### 步骤 6：发现并安装

- 发现模式的对话里，候选技能直接列在模型的回复里：卡片列表，每项为名称、说明、来源，以及"安装"或"已安装"。照 `wsThread()` 的 `.cand-list`。
- 候选从模型的回复中识别：约定模型在回复里输出一个结构化块，在 skill-workshop 的发现流程说明里约定，例如一个 fenced 块 `skill-candidates`，内容为 JSON 数组。前端渲染成卡片。
- 点"安装"调用在线安装接口，装进入口所在层。成功后：
  - 消息流里出现一行"已安装 X v1"；
  - 顶栏与工作台切换为这个技能，变为改进模式；
  - 工作台四个技能标签可用。
- 约定结构化块需要改 `skill-workshop/SKILL.md` 的发现部分，一并改并说明。

### 步骤 7：测试

- 单测：
  - 列表的状态与开关（草稿无开关）；
  - 工坊顶栏的发布按钮在没有修改时禁用；
  - 八个标签，无技能时后四个禁用；
  - 测试用例的增删改写回 `evals.json`；
  - 评测表与反馈写入；
  - "按反馈再改一轮"只填不发；
  - 触发描述"应用到草稿"写回 frontmatter；
  - 版本列表分页；
  - 发布弹窗文案。
- gateway：评测产物接口读样例目录，在测试里按 SKILL.md 的格式造一个 `<名称>-workspace`；反馈合并写入。
- e2e：
  - 从技能页"新建技能"进工坊；用假模型或真模型让它写草稿；"变更文件"出现 SKILL.md；
  - 发布 v1；列表显示 v1 与开关；
  - 再改一次，出现"有未发布的修改"，版本标签里能对比。

## 测试计划

见步骤 7。

## 完成标准

- [ ] 命令表全部通过
- [ ] 与原型全部跳转状态的并排截图写进报告
- [ ] `git status --short` 中本计划新增的改动只涉及范围列出的文件

## STOP 条件

1. 依赖未完成。
2. 工坊产物的实际文件格式与 `references/schemas.md` 不一致。
3. "最近的工坊会话"无法按技能名找到，今天的会话没有记录技能名：停下报告，由 owner 决定存哪里。

## 维护说明

- 工坊页与会话页共用组件：改会话页时工坊页同步受影响，这是有意的（owner：工坊是会话页的超集）。
- 评测与触发测试由模型执行；页面只读写产物文件、往对话里发话，不直接跑脚本。
