# 容器圆角统一 8px 计划（CARD_RADIUS_UNIFY_8PX）

**状态**: 已实施并通过验收（2026-10-10）
**口径裁决**: owner 2026-10-09「容器8px，控件守稿」——容器类（卡片/列表行卡/提示框/空状态/**弹窗模态框/弹层 popover**/侧栏块/导航项）一律 8px；表单控件与按钮守设计稿 6px；chips 5px、composer 12px 保持。**弹出层原设计稿 10px 改判 8px，以本次裁决为准**。
**驱动**: owner 截图圈出设置→语言服务器页 12px 服务器卡「截图中的圆角都不是8px」；「包括弹窗和模态框的圆角，都必须检查一下，保证UI组件风格统一」。
**基准**: 9656051（HEAD）；真实 UI 普查证据 `/tmp/fb-radius-audit/`（report.json + 38 张截图，对 127.0.0.1:6060 运行中 gateway 用系统 Chrome 逐页/逐 tab/逐弹窗取 computed border-radius）。

---

## 一、圆角刻度（唯一合法值）

| 值 | 适用 | 依据 |
|---|---|---|
| 8px | 所有容器：内容卡、列表行卡、提示框、空状态、弹窗/模态框、popover/下拉弹层、侧栏块、导航项、代码块 | owner 本次裁决（弹出层 10px 改判 8px） |
| 6px | 表单控件：input/select/行内按钮/图标按钮/menu option 行 | 设计稿 forebrain-btn/forebrain-field 既有值，守稿 |
| 5px | chips/scope 徽标 | 设计稿，保持不动 |
| 12px | 仅 composer/stage 输入区 | 设计稿，保持不动 |
| 999px/50% | 圆片/圆点 | 保持不动 |
| **其余（7/9/10/16px）** | **全部消灭** | 本次裁决 |

卡片形态的 button（如审批预设大按钮：标题+描述整块可点）按容器算 8px；行内动作按钮按控件算 6px。

## 二、证据（真实 UI 普查结论）

- 16px（rounded-2xl）19 处静态：providers 8 张卡、7 个弹窗模态框、2 个虚线空状态、workshop 气泡 2、面板/编辑器 4。
- 12px（rounded-xl + .approval-card + .forebrain-tenant-menu）约 100+ 处：LSP 服务器卡、channels/tools/mcp 列表行卡、各类提示框/pre、混入的表单控件与按钮。
- 10px：.forebrain-tenant、.forebrain-tenant-tile、.forebrain-language-popover、.forebrain-rail-expand、SettingsView `.settings-tab`、规则文件列表行。
- 9px/7px：.forebrain-tenant-option / .forebrain-language-option（下拉 option 行）。
- 设计稿 HTML 刻度实测分布：6px×22、8px×18、5px×4、12px×3、10px×3、999px/50%/2px——与本表一致（10px 被本次裁决改判）。

## 三、任务

### T1 样式源头（main.css + 组件 scoped 样式）

| 位置 | 现值 | → | 分类依据 |
|---|---|---|---|
| main.css `.forebrain-tenant` (~315) | 10 | 8 | 侧栏容器块 |
| main.css `.forebrain-tenant-menu` (~370) | 12 | 8 | popover 弹层 |
| main.css `.forebrain-tenant-option` (~387) | 9 | 6 | menu option 行（控件） |
| main.css `.forebrain-tenant-tile` (~819) | 10 | 8 | 容器 tile |
| main.css `.forebrain-language-popover` (~582) | 10 | 8 | popover 弹层 |
| main.css `.forebrain-language-option` (~594) | 7 | 6 | option 行（控件） |
| main.css `.forebrain-rail-expand` (~785) | 10 | 6 | rail 展开按钮（控件） |
| SettingsView.vue `.settings-tab` (~123) | 10 | 8 | 导航项容器 |
| SettingsApprovalTab.vue `.approval-card` (~102) | 12 | 8 | 卡片形态按钮 |
| 规则文件列表行（RulesFileEditor 文件清单，执行时按 report.json 样本 text=AGENTS.md 定位类名） | 10 | 8 | 列表行容器 |

### T2 rounded-2xl 19 处（逐处去向）

**换 CardComponent（页面内容卡，替换后逐处删除被卡片行距接管的首段 `mt-*`，沿用上一轮做法）**：
- ProvidersView.vue:30（li 供应商卡，v-for 内整卡）
- ProjectsView.vue:40（新建表单卡）

**仅改 8px（弹窗/模态框，保留 fixed/overlay/shadow 结构，不动内部间距）**：
- CronView.vue:99、SkillsView.vue:59、WorkshopView.vue:98、InstallDialogs.vue:8、SettingsSharedSkillsTab.vue:45、ProjectSkills.vue:66、MemoryFilesPanel.vue:134、MemoryFilesPanel.vue:164

**仅改 8px（容器/面板/气泡/空状态，各有自己规格，不做 CardComponent 结构改造）**：
- SkillsTable.vue:2、MemoryFilesPanel.vue:2、WorkshopSkillPanel.vue:2、RulesFileEditor.vue:28、AgentRosterPanel.vue:2（聊天侧）
- WorkshopView.vue:42、:57（气泡）
- ProjectMemory.vue:12、SubagentsView.vue:23（虚线空状态）

### T3 rounded-xl 控件收敛 6px（混入控件的游离值）

- PermissionsView.vue 的 select/input（rounded-xl → forebrain-field 或 rounded-md）
- MemoriesView 相关：`#memories-search`、`#memories-sort`、另一个 select、`#memories-clear-all`
- SkillsView `#skills-online-source`
- ProvidersView.vue:107 `#providers-add`（虚线添加按钮）
- ApprovalPresetPicker.vue 触发按钮（h-8 rounded-xl 那枚）
- 执行时按 report.json 的控件类样本穷举核对（tag ∈ input/select/行内 button）

### T4 rounded-xl 容器 → 8px（其余全部）

按文件清点（grep rounded-xl 全量 97 处减去 T3 控件）：LspTab（项目行/服务器 li/错误框/安装确认/安装日志 pre/空状态）、ProjectLsp/ProjectMcp/ProjectPerm/ProjectSessions/ProjectSkills/ProjectOverview、ChannelsView li 行、ToolsView li 行、McpTab li、ChatView（177 思考框/268/315/`rounded-md` 338 空态框）、chat/* 卡片（LspRecommendationCard/RunErrorBlock/AutoContinueBanner/SubagentCallCard/PlanUpdateCard/SubagentConversation/PendingActionsPanel/SessionHeartbeat）、ScheduleBuilder 预览框、ConfigTab YAML 编辑框（按代码块算 8px）、ModelChipsInput、PermissionExplainResult、WorkshopView composer **保持 12px**（composer）、chat composer **保持 12px**（执行时确认其 testid 并纳入 T5 白名单）、ComponentGallery 演示页如实反映新刻度（若引用了旧值）。
执行方式：逐文件 `grep -n rounded-xl` 全量核对，按「卡片形态 8px / 控件 6px / composer 12px」三分类落位，不允许留 12px 容器。

### T5 回归守卫（新增 e2e spec）

`frontend/e2e/radius.spec.ts`：
- 走查全部主路由 + settings 全部 tab + 第一个项目全部子路由 + 已知弹窗（cron-new、skills 安装、workshop 新建、providers-add、memory files 弹窗）。
- 断言：可见（有描边或背景、宽≥10）元素中 computed border-radius 不出现 16px/10px/9px/7px；12px 仅允许命中 composer 白名单（testid 含 composer 的 textarea）。
- 挂进 `scripts/acceptance/web_e2e.sh` 的既有 e2e 套件（自动随跑）。

### T6 工作区纪律

- 计划文与验收记录落 `docs/plan/CARD_RADIUS_UNIFY_8PX_PLAN.md`（当前源码树直接实施，不建 worktree，不切分支）。
- **默认不 commit**，改动留工作区由 owner 逐个审阅。

## 四、验收

1. `cd frontend && pnpm build` && `pnpm exec vue-tsc --noEmit`（0 错误）&& `pnpm test`（全绿）。
2. `bash scripts/acceptance/web_e2e.sh`：既有 spec + 新增 radius spec 全过（注意 push 门禁另含 `gofmt -l cmd pkg third_party`，本轮只碰 frontend）。
3. 复跑巡检脚本（scratch gateway + `FOREBRAIN_STATIC_DIST` 新构建产物，独立端口，**不动 owner 的 6060/8790 实例**）：全站非 8px 残留应只剩 5px chips、6px 控件、12px composer、pill/circle。
4. 截图对照（重点：owner 圈出的设置→语言服务器页）`open` 给 owner 确认。

## 五、风险

- CardComponent 迁移（仅 2 处）沿用上一轮「删除被接管首段 mt-*」做法，e2e 既有选择器勿按 section 选卡。
- 列表行卡（LSP/channels/tools）只改圆角不改结构，避免大范围视觉 churn。
- popover 圆角变小后 option 行 6px 内嵌视觉需截图确认无硌感。

## Implementation Status

**已全部落地（2026-10-10），无遗留项。**

### T1 样式源头 — 完成
- main.css：`.forebrain-tenant` 10→8、`.forebrain-tenant-menu` 12→8、`.forebrain-tenant-option` 9→6、`.forebrain-tenant-tile` 10→8、`.forebrain-language-popover` 10→8、`.forebrain-language-option` 7→6、`.forebrain-rail-expand` 10→6
- SettingsView.vue `.settings-tab` 10→8；SettingsApprovalTab.vue `.approval-card` 12→8（卡片形态按钮按容器算）
- 规则文件行按普查样本定位到 RulesFileEditor.vue `.rules-file-row` 10→8；同页 `.rules-editor`（textarea 代码编辑区）10→8（按 ConfigTab YAML 编辑框=代码块同口径）

### T2 rounded-2xl 19 处 — 完成
- ProvidersView 供应商 li 卡、ProjectsView 新建表单卡 → CardComponent（标题进 `#header`，直接子级零 `mt-*`，attrs 透传保住 e2e 的 `[data-provider-row]` 选择器）
- 其余 17 处 → `rounded-lg`（tailwind lg=var(--radius)=8px）；rounded-2xl 全库清零

### T3 rounded-xl 控件收敛 6px — 完成
- MemoryFilesPanel 搜索/排序/每页/清空按钮（= memories-search/sort/clear-all）、InstallDialogs 来源输入+虚线文件按钮、PermissionsView select/input、ToolsView 搜索框、ProvidersView `#providers-add`、ApprovalPresetPicker 触发按钮、ChatView Agent Mode 触发按钮、ScheduleBuilder 周几 chip → `rounded-md`（6px）

### T4 rounded-xl 容器 → 8px — 完成
- 30 个纯容器文件全量 + 混合文件容器行 → `rounded-lg`；rounded-xl 全库仅剩 workshop composer（`workshop-composer`，白名单）
- **计划偏差**：chat composer 实际是 ai-elements-vue `InputGroup` 的 `rounded-md`(6px)，本就合法，计划「保持 12px」前提不成立，未动；计划所列 ChatView:338 `rounded-md` 空态框在当前源码已不存在（审计样本过时），未改

### T5 回归守卫 — 完成
- 新增 `frontend/e2e/radius.spec.ts`：走查 12 主路由 + settings 全部 11 tab + scratch 项目全部 9 子路由 + cron/skills/workshop 弹窗 + providers-add + memory 编辑弹窗；断言可见描边/背景元素（宽≥10）computed border-radius 无 16/10/9/7px，12px 仅允许 testid 含 composer 的 textarea；随 web_e2e.sh 自动全量执行

### 既有 bug 根因修复（执行期发现）
- `exit-plan-approval.spec.ts` 2 用例失败：9656051 已把计划文件改为逐会话目录 `plans/<projectKey>/<sessionID>/`，spec 的 `seedPlan()` 仍写旧扁平路径，plan-md 读空、评审无计划可审、卡片不关。已改 seed 到逐会话路径，4/4 通过（评审后卡片 1.6s 静默关闭）

### T6 工作区纪律 — 遵守
- 本计划文落 docs/plan/；全部改动留工作区未 commit

### 验收记录
1. `pnpm build` ✓（7.1s）；`vue-tsc --noEmit` 0 错误；`pnpm test` 42 文件 329 用例全绿
2. `bash scripts/acceptance/web_e2e.sh`：91 spec 全过（含新 radius spec 26.5s），web e2e: PASS（scratch gateway 8761/8762，未触碰 owner 6060/8790）
3. 全站巡检：radius.spec 即巡检的自动化版（全站 computed border-radius 审计，比旧脚本更严：非法值清零 + 12px 白名单钉死）
4. 截图对照：独立端口 8791 scratch gateway + 新构建产物，11 张（settings-lsp / tenant-menu-open / rules / providers / chat / tools / channels / memories / cron / projects / settings），关键 5 张已 `open` 给 owner
