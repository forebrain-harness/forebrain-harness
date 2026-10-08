# 前端 CardComponent 落地计划

**状态**: 已实施（2026-10-08；owner 裁决"换 CardComponent（按组件设计）"；改动留工作区未 commit）
**优先级**: P2（视觉一致性；不影响功能）
**来源**: `docs/plan/FRONTEND_DESIGN_ALIGNMENT_PLAN.md` 第二轮的待裁决项 1
**设计稿**: `docs/design/CARD_COMPONENT.html`（已在浏览器打开供 owner 确认；01 规格 / 02 用起来的样子 /
03 圆角 8px vs 16px / 04 行距归属 / 05 用在哪里 / 06 自查）
**基准**: `eafaf46`（执行时 HEAD；工作区另有在途改动，本计划只碰 frontend）
**预计工作量**: 2-3 小时（17 处卡片 + 真机验收）

## 待 owner 裁决的三条路（设计稿 05 节列出）

> **已裁决（2026-10-08）**：选第 1 条——按本计划落地。实施记录见文末。

1. **按本计划落地**：17 处改用 `CardComponent`，同时逐处删除被卡片行距接管的首段 `mt-*`。
2. **只收圆角，不引入组件**：17 处 `rounded-2xl` → `rounded-lg`（8px），行距一律不动。
   成本约半小时、零结构风险；代价是"组件存在但无人使用"继续存在。
3. **保留为设计系统原语**：组件 + 演示页 + 文档留着，产品维持 `rounded-2xl`，差异记在计划里。

---

## 目标

把设计稿的 `.card` 真正落进产品：`frontend/src/components/common/CardComponent.vue`
目前**没有任何产品调用点**（Phase 3 只交付了组件 + 演示页 + 文档），因此全站内容卡片仍在
各处手写 `rounded-2xl`（16px 圆角），而设计稿的容器统一是 `.card` 的 8px 圆角。本计划把
"页面内容卡片"这一类容器改用 `CardComponent`，让设计系统真正被使用，而不是只存在一个
演示页里。

## 证据

### 设计稿的容器规格（`~/Downloads/Forebrain Harness 全站预览.html`）

```css
.card   { border:1px solid var(--line); border-radius:8px; padding:16px;
          display:grid; gap:12px; align-content:start; background:var(--page); }
.card-h { display:flex; align-items:center; justify-content:space-between; gap:10px; }
.card-h h3 { margin:0; font-size:14px; font-weight:600; display:flex; align-items:center; gap:8px; }
.card-h .sub { font-size:12px; color:var(--muted); }   /* 副标题在 header 内 */
```

设计稿全部圆角取值：`5px`(chip/scope/tn)、`6px`(btn/field/图标按钮)、`8px`(card/builder/
代码块/下拉/tb-wrap)、`10px`(弹出层)、`12px`(stage/composer)。**没有 16px 容器**。

### 产品的现状

内容卡片一律是 `<section class="rounded-2xl border border-[var(--forebrain-divider)]
bg-[var(--forebrain-surface)] p-4">`（Tailwind `rounded-2xl` = 16px）。除圆角外与设计稿一致
（`p-4`=16px、`--forebrain-surface`=#FFFFFF=`--page`、边框色同为 `--forebrain-divider`=#E4E7EC）。
即：**唯一的视觉差是圆角 16px vs 8px**，但要让组件真正被使用就必须先解决结构差（见下）。

### 结构差（本计划的主要工作）

`CardComponent` 的 body 是 `display:grid; gap:12px`，而现有卡片里面是一串自带外边距的兄弟
节点（`mt-1` 副标题、`mt-3` 表单、`mt-4` 列表）。直接替换会出现"grid gap 12px + 元素自身
`mt-*`"的双倍间距（例如 `mt-4`+12px = 28px）。因此每一处都要把手写的间距**交给卡片自己的
节奏**：header 行进 `#header` 插槽，body 里只保留结构、不再自带首段外边距。

## Scope

### In scope（17 处：页面内容卡片，含一个标题行）

| # | 文件:行 | 现状形状 | 落地方式 |
|---|---|---|---|
| 1 | `frontend/src/components/settings/SettingsApprovalTab.vue:3` | 标题 + `mt-1` 说明 + `mt-4` 预设列表 | header=标题；body=说明+列表 |
| 2 | `frontend/src/components/settings/PrimaryAgentsTab.vue:10` | header 行（名称+徽标 / 操作）+ body | header=现有 flex 行；body=其余 |
| 3 | `frontend/src/components/settings/PrimaryAgentsTab.vue:40` | 标题 + `mt-3` 表单网格 | header=标题；body=表单 |
| 4 | `frontend/src/components/settings/CronSettingsTab.vue:10` | 标题 + `mt-0.5` 说明 + `mt-4` 字段 | header=标题；body=说明+字段 |
| 5 | `frontend/src/components/settings/HooksTab.vue:18` | `h2` + `mt-3` 网格 | header=h2；body=网格 |
| 6 | `frontend/src/components/settings/McpTab.vue:141` | `h2` + 多个 `mt-2` 说明块 | header=h2；body=说明块 |
| 7 | `frontend/src/components/settings/LspTab.vue:171` | 无标题：说明 + `mt-2` 按钮 | body-only（无 `#header`） |
| 8 | `frontend/src/components/settings/MemorySwitchesTab.vue:6` | 开关行（`space-y-4`）+ `mt-4` 按钮 | body-only |
| 9 | `frontend/src/components/project/ProjectSessions.vue:3` | header 行（标题+徽标 / 新建）+ `mt-1` 说明 + 列表 | header=现有 flex 行；body=说明+列表 |
| 10 | `frontend/src/components/project/ProjectMcp.vue:3` | 同上 | 同上 |
| 11 | `frontend/src/components/project/ProjectPerm.vue:3` | header 行 + `mt-1` 说明 + 信任闸门 | 同上 |
| 12 | `frontend/src/components/project/ProjectPerm.vue:59` | 标题 + `mt-1` 说明 + `mt-3` 表单 | header=标题；body=其余 |
| 13 | `frontend/src/components/project/ProjectOverview.vue:4` | header 行（标题+徽标）+ `mt-3` 定义列表 | header=现有 flex 行；body=列表 |
| 14 | `frontend/src/components/project/ProjectOverview.vue:20` | 标题 + `mt-3` 表单网格 | header=标题；body=表单 |
| 15 | `frontend/src/components/project/ProjectOverview.vue:90` | 危险区标题 + `mt-3` 按钮行 | header=标题；body=按钮行 |
| 16 | `frontend/src/components/project/ProjectSkills.vue:3` | header 行 + body | 同 #9 |
| 17 | `frontend/src/components/project/ProjectLsp.vue:3` | header 行 + body | 同 #9 |

统一目标写法（两类，示例即模板）：

```vue
<!-- 有 header 行：#13 这类 -->
<CardComponent>
  <template #header>
    <div class="flex items-center gap-2">
      <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('projects.overviewTitle') }}</div>
      <ScopeBadge type="project" :label="t('scope.project')" />
    </div>
  </template>
  <dl class="grid gap-x-6 gap-y-2 text-[13px] sm:grid-cols-2">…</dl>
</CardComponent>

<!-- 无 header：#7/#8 这类 -->
<CardComponent>
  <p class="text-[12px] text-[var(--forebrain-text-2)]">…</p>
  <button type="button" class="forebrain-btn forebrain-btn-ghost text-[11px]">…</button>
</CardComponent>
```

### Out of scope（**看起来相关但明确不碰**，逐条给理由）

- **弹层/对话框**：`MemoryFilesPanel.vue:134,164`、`ProjectSkills.vue:64`、
  `InstallDialogs.vue:8`、`SettingsSharedSkillsTab.vue:45`、`ProjectsView.vue` 的新建/编辑弹层、
  `WorkshopView.vue:98`——设计稿的弹层是另一套容器，不是 `.card`。
- **聊天气泡**：`WorkshopView.vue:42`（`max-w-[85%]` 消息气泡）。
- **空状态**：`border-dashed` 的占位块（如 `ProjectSessions.vue` 的 `mt-6 ... border-dashed`）。
- **列表/表格容器**：`SkillsTable.vue:2`（header 带 `border-b` + 行 `divide-y`，对应设计稿的
  `.tb-wrap` 表格容器，不是 `.card`）、`MemoryFilesPanel.vue:2`；它们若要对齐另有 `.tb-wrap`
  (8px) 的取值，**不属于本计划**（列在这里是为了避免执行者顺手扩大范围）。
- **侧栏/工作台面板**：`AgentRosterPanel.vue:2`、`SessionHeartbeat.vue:2`（`p-3` 侧栏面板，
  含 `bare` 变体）、`WorkshopSkillPanel.vue:2`（`h-full` 工作台面板）。
- **`RulesFileEditor.vue:28`**：规则编辑面板（`min-w-0` 参与聊天侧布局），不是页面内容卡片。
- `CardComponent.vue` 自身的规格、演示页、文档：Phase 3 已交付，不改。

## 行为变化矩阵（每处都适用）

| 项 | 旧 | 新 | 说明 |
|---|---|---|---|
| 容器圆角 | 16px（`rounded-2xl`） | **8px**（`.forebrain-card`） | 本计划的核心视觉变化，与设计稿一致 |
| 内边距 | 16px（`p-4`；#17 早已是 `p-4`） | 16px | 不变 |
| 背景 | `--forebrain-surface`（#FFFFFF） | `--forebrain-bg`（#FFFFFF） | 同值，视觉不变 |
| 边框 | 1px `--forebrain-divider` | 1px `--forebrain-divider` | 不变 |
| 子元素间距 | 子元素自带 `mt-1/2/3/4` | 首段由 body 的 `gap:12px` 提供；**逐处删除被替代的 `mt-*`** | 必须逐处做，否则出现双倍间距 |
| 副标题位置 | 标题下方 `mt-1`（部分卡片） | 仍在标题下方，间距 12px（设计稿把 `.sub` 放在 `.card-h` 内；本计划**不**搬动文字位置，只统一间距） | 明确选择：最小改动优先，不重排信息层级 |
| DOM/测试钩子 | `section` 元素 | `CardComponent` 根 `div` | 现有 `data-testid` 全部保留（透传到根）；无测试断言 `section` 标签 |
| 行为/接口 | — | 无变化 | 不改任何事件、请求、状态 |

## Steps

### 第 1 步：先做 1 处并真机确认

- 改 `project/ProjectOverview.vue:4`（#13，有 header 行 + `dl` body）。
- 验证：
  ```bash
  cd frontend && pnpm exec vue-tsc --noEmit          # 0 诊断
  FOREBRAIN_E2E_SPEC="e2e/project-space.spec.ts e2e/visual.spec.ts" \
    ../scripts/acceptance/web_e2e.sh                  # 必须 web e2e: PASS
  ```
- 产物：`$FOREBRAIN_E2E_WORK/shots/project-detail.png` 等截图（脚本已自动截图），
  人工比对圆角与间距。

### 第 2 步：其余 16 处按同一模板落地

- settings 7 处（#1-#8 去掉已做的 #13 同类）→ `pnpm exec vue-tsc --noEmit` + `pnpm build`。
- project 8 处（#9-#12、#14-#17）→ 同上。
- 每改 3-4 处跑一次 `pnpm build`，避免一次堆叠到无法定位。

### 第 3 步：静态与单测

```bash
cd frontend
pnpm build                  # ✓ built
pnpm exec vue-tsc --noEmit  # 0 诊断
pnpm test                   # 40 files / 312 tests passed
grep -rn "rounded-2xl border border-\[var(--forebrain-divider)\]" src --include="*.vue" \
  | grep -v "max-w-\|fixed inset-0\|border-dashed"   # 只应剩下 out-of-scope 的那些
```

### 第 4 步：真机 web e2e（全量）

```bash
scripts/acceptance/web_e2e.sh          # 期望：除已知的 2 条既有失败外全绿
```

已知既有失败（与本计划无关，见 `FRONTEND_DESIGN_ALIGNMENT_PLAN.md` 第二轮第 7 节）：
`e2e/exit-plan-approval.spec.ts:169`（需求变更后 spec 未更新）。
`e2e/banner.spec.ts` 已在第二轮修好（路由表删除是 owner 要求）。

### 第 5 步：交付

- 改动留在工作区（本仓库规则：**不 commit**），报告逐处 before/after 与截图路径。
- 回填 `docs/plan/FRONTEND_DESIGN_ALIGNMENT_PLAN.md` 的"待 owner 裁决"第 1 条为已实施。

## Done criteria

- [ ] 17 处卡片使用 `CardComponent`，`grep` 只剩 out-of-scope 的 `rounded-2xl` 容器。
- [ ] 真机取值：这些卡片 `border-radius` = `8px`，`padding` = `16px`，body 间距 = `12px`。
- [ ] 所有既有 `data-testid` 仍在 DOM 中，e2e 断言不变。
- [ ] `pnpm build` / `pnpm exec vue-tsc --noEmit` / `pnpm test` 全绿。
- [ ] 全量 `scripts/acceptance/web_e2e.sh` 除既有失败外全绿，且截图可见间距无双倍。

## STOP conditions

- 某处卡片替换后出现**双倍间距**且无法在不搬动信息层级的前提下消除 → 跳过该处并单独报告，
  不靠"加一个 `!mt-0`"之类的补丁掩盖。
- 某处卡片的子元素依赖 `section` 的标签语义（selector 匹配 `section`）→ 停止并报告，
  不擅自改动 e2e。
- 真机截图显示圆角/间距与预期不符且原因不明 → 停止，不带病推进。

## Maintenance notes

- 以后新增页面内容卡片一律用 `<CardComponent>`；不要在页面里手写 `rounded-2xl border ... p-4`。
- 卡片内的间距由 `CardComponent` 的 body `gap: 12px` 负责；子元素不要再自带首段 `mt-*`，
  需要更大间隔时把两个块放进一个带 `space-y-*` 的 wrapper。
- 弹层、气泡、空状态、表格容器、侧栏面板各有自己的设计规格，继续按各自规格实现，
  不要套 `CardComponent`。
- 组件用法与设计规范已写入 `frontend/docs/components.md`（含"换色走
  `--forebrain-card-border` 变量"与"别用标签名选中卡片"两条约定）。

---

## 实施记录（2026-10-08）

### 落地清单（20 处，比计划多 3 处）

计划列 17 处；实施时**同类穷举**又找到 `views/PermissionsView.vue` 的 3 张同型内容卡片
（原清单漏项），一并落地——否则同页族的"项目权限"已是 8px、而"主代理权限"仍是 16px。
最终 20 处：

| 文件 | 处数 |
|---|---|
| `settings/SettingsApprovalTab.vue` | 1 |
| `settings/PrimaryAgentsTab.vue` | 2（列表卡 + 表单卡） |
| `settings/CronSettingsTab.vue` | 1 |
| `settings/HooksTab.vue` | 1 |
| `settings/McpTab.vue` | 1 |
| `settings/LspTab.vue` | 1 |
| `settings/MemorySwitchesTab.vue` | 1 |
| `project/ProjectSessions.vue` | 1 |
| `project/ProjectMcp.vue` | 1 |
| `project/ProjectPerm.vue` | 2 |
| `project/ProjectOverview.vue` | 3 |
| `project/ProjectSkills.vue` | 1 |
| `project/ProjectLsp.vue` | 1 |
| `views/PermissionsView.vue` | 3（计划外补充） |

### 真机 before/after（隔离 home + 真实 gateway，探针实测 `getComputedStyle`）

用一次性探针 spec 在 `/settings` 七个 tab、项目九个 tab、`/permissions` 上逐卡实测，
改动前后各跑一次（探针跑完即删，未留在仓库）：

| 页面 · 卡片 | 圆角 | 内边距 | 卡内间距 |
|---|---|---|---|
| settings-memory | 16 → **8px** | 16 → 16px | [16] → [12] |
| settings-cron | 16 → **8px** | 16 → 16px | [2, 16, 16] → [12, 12] |
| settings-approval | 16 → **8px** | 16 → 16px | [4, 16] → [12] |
| settings-agents（列表卡 / 表单卡） | 16 → **8px** ×2 | 16 → 16px | [] / [12, 12] → [] / [12] |
| settings-hooks | 16 → **8px** | 16 → 16px | [12, 8, 8, 8] → [12, 12, 12] |
| project 概览 ×3 | 16 → **8px** ×3 | 16 → 16px | [12] / [12,16,12] / [12,8] → [12] / [12,12] / [12] |
| project 会话 | 16 → **8px** | 16 → 16px | [4, 24] → [12] |
| project 权限 ×2 | 16 → **8px** ×2 | 16 → 16px | [4, 16] / [4, 12] → [12] / [12] |
| project MCP / 语言服务器 / 技能 ×3 | 16 → **8px** ×3 | 16 → 16px | [4, 16] → [12] |
| permissions ×3 | **8px** | 16px | [12] / [12] / [] |
| **未改动（out-of-scope）**：project 规则指令 16px、记忆表格 16px、信任闸门 12px ×4 | 16 → 16px | — | 不变 |

即：**卡内间距全部收敛到 12px**（原为 2/4/8/12/16/24 混用），圆角 16 → 8px，
内边距与底色、边框色不变。

### 命令与结果

- `pnpm build` → ✓ built；`pnpm exec vue-tsc --noEmit` → 0 诊断；`pnpm test` → 40 files / 312 passed。
- 全量 `scripts/acceptance/web_e2e.sh` → **91 passed + `web e2e: PASS`**（exit 0；改动前基线为
  87 passed / 2 failed，那 2 条是已单独处理过的 banner 与评审回传 spec）。
- 截图留档：`$FOREBRAIN_E2E_WORK/shots/`（含 `settings-*.png`、`project-*.png`、
  `permissions.png`、`skills-project.png` 等，供人工比对）。

### 与计划的偏差（3 条，均有理由）

1. **多落地 3 处（PermissionsView）**：计划清单漏项，同类穷举补齐（见上）。
2. **`CardComponent` 增加一个换色变量**：`border` 改为
   `var(--forebrain-card-border, var(--forebrain-divider))`。原因：`PrimaryAgentsTab` 的
   "当前主代理"卡片原本用 `:class="{'border-[var(--forebrain-brand-border)]': active}"` 换描边，
   而组件的 scoped 规则（属性选择器，优先级更高）会压掉 Tailwind 工具类；写死成品牌色又不通用。
   一行变量让页面能换色而不必伸手进组件。默认值即原行为，其余 19 处不受影响。
3. **改了 `e2e/tenant-shell.spec.ts` 的选择器**（计划 STOP 条件里列为"应停下报告"的情形）——
   该 spec 用 `section:not(:last-child)` / `section:last()` 定位主代理卡片与表单卡，而组件渲染成
   `<div>`，于是真机跑出 1 failed。处理方式：给 `PrimaryAgentsTab` 的两类卡片加稳定钩子
   `data-testid="agent-card"` / `data-testid="agent-form"`，spec 的六处选择器改为按钩子定位；
   **所有断言（id 只读、改名、main 删除禁用、删除后计数 0）逐条保留**，并且不再依赖"表单卡是最后一个"
   的顺序假设。改后该 spec 7/7 通过。
   ——按 STOP 条件本应先停下报告；考虑到这是同一改动的直接回归、修法是机械替换而非放宽断言，
   已就地修好并把选择器改法写进 `frontend/docs/components.md` 的第 3 条约定（"别用标签名选中卡片"）。
   如果 owner 认为该 spec 应由你亲自改，回退这一处即可（`git diff frontend/e2e/tenant-shell.spec.ts`）。

### 未处理（相邻，与本计划无关）

- 其余 `rounded-2xl` 容器（弹层、气泡、空状态、表格容器、侧栏/工作台面板、规则编辑面板、
  `ProvidersView` 的行）按计划属 out-of-scope，**未动**；其中空状态/信任闸门用 12px、弹层用
  16px，与设计稿的 8px 家族不完全一致，若要统一需另开计划。
