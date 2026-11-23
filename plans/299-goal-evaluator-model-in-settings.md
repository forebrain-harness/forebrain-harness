# Plan 299: 设置 · 子代理 Tab 配置 goal-evaluator 的模型与思考强度

> **状态：READY TO EXECUTE —— 原型已确认（2026-11-12），原型自此冻结。**
> Steps / Done criteria / STOP 条件已补全：见 `## 8` / `## 9` / `## 10`；判据池是 §5。
> **裁决已清零 + 原型闸门已解除（2026-11-12）**：两级语义、全局字段、`/api/llm-models/choices`（`300` 为硬前置）、前端路线 A 自封装、强度不独立于模型、默认强度**实时读模型行**、不回显实际生效模型 —— 全部定案，依据见 `## Status` 与 §6。
> **唯一外部依赖是 plan 300**：它未落地时 Steps 1–8（Go 侧）可独立执行，前端阶段在 Step 9 报 BLOCKED（判据见 §8 的 Step 0 与 Step 9），不得自行给 `/choices` 加字段。
>
> **owner 的两条硬约束（2026-11-12 01:03）**
> 1. **原型稿就是 UI 稿**：应用内部不得出现给执行者/评审者看的说明（落库路径、白名单、引擎校验、file:line、待确认清单、实现注记等一律不写进原型）。
> 2. **原型稿的 UI 与 UX 就是执行者最终要实现的样子**：1:1 还原，不留占位、不留示意、不留「沿用现有页面」这类文字。所有说明只写在本文件里。
>
> **原型位置**：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/config-db-settings-preview.html`，默认打开「常规 / 子代理」Tab；区块挂在 `FORMS.subagents` 的 `sa` / `peers` 两个分组之后。模型选择器的版式复用 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/llm-models-preview.html`。
> **Planned at**：`ecc4e1aa`（dev）。`plans/`、`docs/plan/`、`.workbuddy/` 均被 `.gitignore` 忽略。

## Status

- **Priority**: P2
- **Effort**: M（Go 侧窄；前端要自封装一个新组件 + 新 e2e）
- **Risk**: MED（改的是模型解析链与设置写入链，两处都有既有回归钉住；守卫写宽会打死 283 的审批卡选模型）
- **Depends on**: **`plans/300-chat-effort-follows-model-setting-and-session-row.md`（硬前置）** —— 300 给 `/api/llm-models/choices` 加 `default_effort`，而本计划 §2 的思考强度默认值必须要它。**300 未落地时本计划前端部分 BLOCKED，不得自行给 `/choices` 加字段。**
- **Supersedes**: `plans/297-goal-check-model-row-spike.md` 的 ctx pin 路线（owner 2026-11-12 两级裁决；297 状态行已同步改写，其授权裁决 ③ 失去落点仅留痕）
- **Category**: direction
- **Planned at**: commit `ecc4e1aa`（dev），2026-11-12；同轮 `review-plan` 已按 owner 全部裁决（两级语义 / 全局字段 / 路线 A / `/choices`+300 / 强度不独立于模型 / 默认强度实时读模型行 / 不回显实际生效模型）改写 §1–§6
- **Gate**: **READY TO EXECUTE —— 原型已于 2026-11-12 确认并冻结。** 全部裁决同日定案：两级语义（owner）、落库 = 全局字段、端点 = `/api/llm-models/choices`、前端 = 路线 A 自封装、强度不独立于模型（不放宽）、默认强度**实时读模型行**、不回显实际生效模型（1:1 硬约束推得）。Steps / Done criteria / STOP 条件在 §8 / §9 / §10。**唯一外部依赖：plan 300** —— 未落地时 Steps 1–8 可独立执行，前端阶段在 Step 9 STOP 并报 BLOCKED；**§5.3 第 8 条是裁决 11 的唯一实证闸门，不得省**。

## 1. 范围（owner 逐轮定案，最终以此为准）

- **只配置 `goal-evaluator`**：`/goal` 每轮结束后检查目标是否达成的系统子代理。
- **只配 2 项**：模型、思考强度。系统提示词重写与启用开关**不开放**。
- **位置**：设置 →「常规 / 子代理」**已有 Tab** 内，现有「子代理」「对等通信」两个分组之后，**不新增 Tab**。
- **其余 9 个内置子代理一律不暴露**：`general-purpose` / `explore` / `plan` / `verification`（4 个 public）保持现状，仍在**侧栏「子代理」页** per-主代理配置；`plan-reviewer` / `guardian` / `cavecrew-investigator` / `cavecrew-builder` / `cavecrew-reviewer`（5 个 system）在 web 端不新增任何配置字段、API 或控件，**UI 上完全不出现**（原型 v5 已把只读名单整块移除）。
- **设置里的「子代理」Tab ≠ 侧栏「子代理」页**，两者同名但不是同一处，文案与代码都要区分。
- **不配置时跟随当前主代理模型**；系统子代理不能由主代理选模型：`list_usable_models` 与 `subagent_run` / `subagent_send` / `subagent_fanout` 的 `model` 参数只作用于 4 个 public 内置与自定义子代理，不作用于 `goal-evaluator`。`goal-evaluator` 的模型优先级只有两级：**本页配置 ＞ 主代理当前模型**。
- **两级语义已由 owner 定案（2026-11-12，覆盖 `plans/297` 的 ctx pin 路线）**：`goal-evaluator` 的模型只从本页配置来，**不接受任何派发期 ctx pin**。因此 297 设想的「用户 `/goal` 显式指定行 id → `WithSubagentModelOverride` 钉住检查」这条路**不立项**；297 Open questions 的 (a)「行 id 最终来源与命令面」由本计划答为「设置页配置，不是 `/goal` 参数」，(b)「是否立项实现」答为「否」。297 的授权裁决（选 ③：仅显式指定行 id 时绕过 `subagent_allowed`、仍查凭据）**随之失去落点**，保留作留痕、不再驱动任何代码。
  执行后果：`pkg/run/subagent.go:1979` 的 ctx pin 分支对 `goal-evaluator` 必须走不通（见 §4 的收窄守卫）；`plans/README.md` 里 297 的状态行要注明被本计划取代，防止后来的执行者按 297 把这条路接回来、静默回退本页配置。
- 审批卡上用户当场挑选的 `plan-reviewer` 模型属于**用户行为**（283 的 `WithSubagentModelOverride`），保持不变、不收归本页。

## 2. UI / UX 规格（1:1 还原原型）

### 区块结构（owner 2026-11-12 01:30 截图定稿：与「子代理」「对等通信」同一套分组版式）
- **不用卡片外框**：直接是第三个 `.grp` 分组（`data-grp="goal-check-model"`），与 `sa` / `peers` 并列，组间 36px 空白，沿用基础原型的 `.grp` / `.grp-h` / `.row` / `.tray` 样式，1:1 还原 owner 截图（分组标题 + 下方细横线、字段行左标题右控件、行间细线）。
- 分组头 `.grp-h > h3` 只有 `/goal 检查模型`，**标题下不设任何说明文案**（owner 01:24 删第二句、01:26 删最后一句 `设置 goal-evaluator 使用的模型与思考强度。`，**不要再补回来**）。产品默认行为仍是不配置时跟随主代理，只是不写在 UI 文案里。
- 两条 `.row` 字段行（左：`<b>标题</b>` + `<span class="h">说明</span>`；右：控件，右列 360px），未保存时左缘显示蓝色 dirty 条：
  - `使用的模型` / **无说明文案**（owner 01:52 删掉「同名模型可用接口地址区分」）/ 右侧 `combo` 模型选择器（32px 高、收起态只回显模型名 + 服务商）；**右侧默认只有向下箭头，无常驻清空按钮**（owner 01:55 删除），hover 到箭头区才变成清空 x，见下节；页面上没有「清除配置」链接。
  - `思考强度` / 未选模型时**无说明文案**；选中模型后才显示「模型页配置的默认强度：…」或「已改为…，仅影响目标检查子代理」/ 右侧 `select.fld`，**与模型下拉同宽：铺满 360px 右列（不写固定宽，靠 `.fld`/`.combo` 的 `width:100%`）**（owner 2026-11-12 截图反馈两下拉宽度不一致后定案；原型原有一条 `.ss-row .rs .fld{width:220px}` 覆盖已删除）；改过强度时说明下方有「恢复为模型默认」小链接。
- **去掉卡片头、状态徽标、独立大标题**；内部名 `goal-evaluator` 不出现在 UI 上。
- 组底沿用 `.tray` 保存条：**仅在有未保存改动或刚保存后展开**，左侧「✓ 已保存，下一轮对话生效」，右侧「放弃更改」「保存」。初始已保存态不显示保存条（与截图一致）。
- 模型下拉菜单 `.ss-menu` 右对齐（`right:0;top:38px`），避免溢出内容区。

### 模型选择器（复用 `llm-models-preview.html`）
- 不用原生 `<select>`。复用 `combo` + `mrel` + `mmenu` + `msrch` + `mlist` + `mopt` + `sig` 那套类名与结构。**两行签名只用于下拉候选项**：第一行 `模型名 + 服务商`，第二行 `接口地址 + 高亮的 API 路径`（与 owner 截图一致，用于区分同名行）。
- **combo 收起态（选中值回显）不显示 endpoint**（owner 2026-11-12 01:34 定稿）：只有 `模型名 + 服务商` 一行，用 `ssSigShort()`；不加 `combo.tall`，保持 32px 高度与字段行其它控件一致。未选择时显示 **placeholder「跟随主代理」**（`.combo .ph` 灰字）。下拉候选项仍保留 endpoint，**不要一起删掉**。
- 菜单含：可输入的搜索框（按模型名 / 服务商 / 接口地址过滤）+ **8 个示例模型行**（与 owner 截图逐行一致：两行 `deepseek-v4-flash`、`doubao-seed-2.1-pro`、火山 `glm-5.3`、智谱 `glm-5.3`、`LongCat-2.5`、`hy3`、`gpt-5.5`）。**「跟随主代理」不作为候选项**（owner 01:43 定稿，覆盖 01:38 中途的「作为第一位」方案）——它只是两个下拉的 placeholder；选中项左侧蓝色勾、名字染蓝。
- **箭头区 hover 切换清空图标**（owner 01:55 定稿）：combo 内右侧是 `.ss-caret-zone`（18×20），默认显示 `.ss-caret`（`icon('down')`）；`:hover` 时 `visibility:hidden` 隐藏箭头、`.ss-x` 显示 `icon('x')`，移开恢复。x 带 `data-ss="clear"` + `role="button"` + `tabindex="0"`，是事件委托里最近的可触发元素（点击不会误开菜单）；点击同步清空 `model` 与 `effort`，两个下拉回到 placeholder「跟随主代理」，思考强度回到 `disabled` 禁用态，菜单收起。CSS 四条规则见原型 `.ss-caret-zone*`。**组件库无内置，需自行开发**，调研见本节末。
- **同名两行用接口地址区分**（在下拉菜单里）；系统存的是模型行 `id`（`llm-<UUID>` 不面向用户）。
- 原型为展示选择器**默认展开菜单**，点击 combo 可收起/展开，点击外部或 Esc 收起；1:1 还原时保持这个初始态。
- 原型初始态就是**「从未配置」默认态**：`model=''`、`effort=''`，两个下拉都显示 placeholder「跟随主代理」（同为 `var(--quiet)`），思考强度为 `disabled` 禁用态，字段无说明文案，保存条收起；菜单默认展开以展示候选项。执行者不要把任何示例模型写成默认值。
- **候选数据源（owner 2026-11-12 裁决：改用 `/choices`，`300` 成为硬前置）**：读 `GET /api/llm-models/choices`（`pkg/gateway/models_api.go:605-635`），它是「the one list every model picker reads」（`:605-607` 注释原文），带 `id` / `provider` / `provider_name` / `model` / `base_url` / `api_path` / `endpoint` / `label` / `efforts` / `supports_images`，**不带任何凭据字段**。既有三个 picker（`ModelChip.vue` / `SubagentConfigDrawer.vue` / 记忆下拉）都走它，本区块不得偏离（266 已定该约定）。**不要用 row id 做列表标签。**
  **不用 `GET /api/llm-models`**（`models_api.go:438-467`）的三个已核实理由：① `llmModelRow` 明文带 `api_key`（字段注释自陈「Never logged, never in an error, never in `/choices` or an event」），`listLLMModels` 未剥离 —— 为一个下拉把明文密钥拉进设置页；② 它跑订阅发现探测（`newSubscriptionStates` + `modelsDiscoveryTimeout`，`:451-453`），打开子代理 Tab 即可能触发对厂商的网络调用；③ 它分页且 agent 作用域（`pageOf` / `llmModelScope`），不「列出全部行」。
  **前置依赖：`turn.ModelChoice` 今天没有 `DefaultEffort`**（只有 `ReasoningEfforts`），而 §2 的「默认选中该模型行的强度」必须要它。`plans/300` 正是给 `ModelChoice` 加 `ConfiguredEffort` / 给 choices 加 `default_effort`。**因此 `300` 必须先落地**；`300` 未落地时本计划的前端部分 **BLOCKED**，不得自行给 `/choices` 加字段（那是 300 的范围，重复实现会撞车）。
  `frontend/src/composables/useModelChoices.ts` 打的正是 `/choices`（`:6-7` 注释、`:19-20` 调用，页级缓存一份、失败不缓存），可直接复用；`300` 落地后需确认它把 `default_effort` 透传出来，若没有则在本计划范围内补透传（只加字段，不改缓存语义）。

### 思考强度（默认选中与回显）
- 档位与默认值**取自「设置 · 模型」页给当前选中模型行的配置**（模型页高级配置里的「思考强度」下拉，值来自官方接口 / models.dev 的 `reasoning_options` 与 `default`；见 `llm-models-preview.html` 的 `official()` / `autoCaps()` / `advancedHTML()`）。
- 原型数据示例：`SS_MODELS[*].efforts`（该行支持的档位）与 `SS_MODELS[*].defaultEffort`（该行配置的默认强度）。
- **应用侧数据来源（对应到真实接口）**：档位 = `/api/llm-models/choices` 每项的 `efforts`（来自 `turn.ModelChoice.ReasoningEfforts`，`pkg/gateway/models_api.go:630`）；默认强度 = **`300` 新加的 `default_effort`**（今天 `turn.ModelChoice` 只有 `ReasoningEfforts`、没有 `DefaultEffort`，故 **`300` 是硬前置**，见 `## Status` 的 Depends on）。`300` 未落地时本节无法实现，**BLOCKED 并报告，不得自行给 `/choices` 加字段**。
  注意 `efforts` 为空是合法状态（该模型不思考、或没有离散档位，见 `ModelChoice.ReasoningEfforts` 注释），此时思考强度下拉应只剩首项「跟随模型设置」且不报错 —— 这条原型没有示例数据覆盖，补 Steps 时要显式写清。
- **落库映射（owner 2026-11-12 裁决「实时读模型行」，详见 §6 第 11 项）**：UI 选的强度 **等于该行 `default_effort`** ⇒ 存 `reasoning_effort = ""`（实时跟随，模型页日后改默认会跟着变）；**不等于** ⇒ 存具体档位（覆盖）。判据就是原型已算好的 `effortReset`（原型 `:697`，「恢复为模型默认」链接的显示条件）。**不得把原型的回显值（选中模型行后置成的具体 `defaultEffort`，原型 `:759`）直接写库** —— 那会把实时读静默变成快照。引擎侧无需任何改动即可实现实时读：`llmProviderFromModelRow`（`pkg/config/llm_models.go:338-343`）本就在行 `Params` 未写 effort 时折入 `Caps.DefaultEffort`，而 pin 的 effort 为空时 `ConfiguredModelClientWithEffort` 恰好不覆盖。
- **默认选中 + 回显**：选中某模型行后，思考强度下拉立即选中该行的 `defaultEffort`；打开已保存配置时同样回显保存值。字段下方 hint 显示「模型页配置的默认强度：高 high」。
- **切换模型行**时，档位选项与选中值一起换成新行的 `efforts` / `defaultEffort`（不同模型档位不同，例如 `glm-5.3` 火山行是 `low/high/max` 且默认 `极致 max`）。
- 档位标签沿用模型页的中英双写：`关闭思考 none / 极低 minimal / 低 low / 中 medium / 高 high / 最高 xhigh / 极致 max`。
- **下拉首项文案是「跟随模型设置」**（owner 2026-11-12 01:26 定稿；不要用「跟随模型默认」或「跟随模型配置」），**只在已选中模型行时出现**：它是 `value=""` 的空值项，表示不在 `goal-evaluator` 上覆盖强度、沿用模型行的配置。注意与「恢复为模型默认」这个操作链接区分开。
- **未选模型 / 清空后**：思考强度下拉只有一个 **placeholder「跟随主代理」**（`selected`）且整个 select 是 `disabled` **禁用态**（owner 01:52 明确「置灰」= 禁用，不是只改颜色）：`background-color:var(--alt)`、`color:var(--quiet)`、`cursor:not-allowed`；**两个下拉的 placeholder 字体颜色一致**（模型下拉 `.combo .ph` 与思考强度 `select.fld.unset` 都用 `var(--quiet)`）。该状态**不显示任何字段说明文案**（已删「跟随主代理时不可单独设置」）。`ssEffortOptions()` 在 `modelId` 为空时直接返回这一项。选中模型行后才换成该行的档位列表并默认选中 `defaultEffort`。
- 用户手动改了强度：hint 变成「已改为 …，仅影响目标检查子代理」，并出现「恢复为模型默认」；点它回到该行的 `defaultEffort`。
- **hover 出现的 x 同步清空两个下拉**：模型回到 placeholder「跟随主代理」，思考强度清空、只剩 placeholder「跟随主代理」且 select 回到 `disabled` 禁用态，字段说明整行消失，「恢复为模型默认」链接隐藏；菜单同时收起。不要把「清空 x」与「恢复为模型默认」混成一个语义。
- 引擎硬校验 `reasoning_effort requires model`（`pkg/config/agents.go:310-313`），现有抽屉也是 `canPickEffort = form.model !== ''`（`SubagentConfigDrawer.vue:415`）。因此「模型跟随主代理 + 只改思考强度」当前不可用；是否放宽见 §6。

### 交互清单（原型已可跑，执行者需 1:1 实现）
点 combo 开合菜单 → 输入搜索即时过滤 → 点某行回填签名、重置强度默认值、打勾、关闭菜单 → 改强度 → 恢复为模型默认 → 保存（写入 + 显示已保存）→ 放弃更改（回到上次保存值）→ **鼠标移到右侧向下箭头（变成清空 x）→ 点击 x**（两个下拉同步清空、显示 placeholder「跟随主代理」、强度禁用、菜单收起）→ 鼠标移开恢复向下箭头 → 点击外部 / Esc 关闭菜单。

### 组件库能力调研（owner 01:55 要求先确认）

**结论：组件库没有内置该交互，需要自行开发。**

- 前端 UI 组件库是 workspace 包 `@repo/shadcn-vue`（`frontend/package.json:33`、`frontend/pnpm-workspace.yaml:3-4`），底层 `reka-ui@^2.11.0`（`ai-elements-vue/packages/shadcn-vue/package.json:16`）；`frontend/src/components/ui/` 不存在，封装都在 `ai-elements-vue/packages/shadcn-vue/components/ui/`。
- `Select` 的箭头是硬编码 `SelectIcon` + `ChevronDown`（`.../ui/select/SelectTrigger.vue:29-31`），且 `:24` 的 `[&_svg]:pointer-events-none` 让 svg 收不到指针事件；`Select.vue:5-8` / `SelectValue.vue:5` 无 `clearable` prop。
- `Combobox` 有搜索（`.../ui/combobox/ComboboxInput.vue:30-40`）与触发区 `<slot/>`（`ComboboxTrigger.vue:22`），`combobox/index.ts:12` 导出 `ComboboxCancel` 原语；但 reka-ui `ComboboxCancel.vue:19-32` 只清输入框 / 置 `modelValue=null` / 重置搜索，**没有 X 图标、没有 hover、没有箭头↔x 互换**，且 `resetModelValueOnClear` 默认 `false`（`ComboboxRoot.vue:83,107`）。
- 全仓 grep `clearable` / `ClearButton` / `XButton` / `data-clear` 在 frontend 中 0 命中；`.../ui/sidebar/SidebarMenuAction.vue:8,26-27` 的 `showOnHover` 是「整块 hover 时显示独立按钮」，不是图标互换。
- 最接近的成品：`frontend/src/components/chat/ModelChip.vue`（ChevronDown + 搜索 + 两行 `ModelSignature`）、`frontend/src/components/models/ProviderCombobox.vue`（Popover + Command + 搜索）、`frontend/src/components/settings/form/SettingsField.vue:70-97`（`kind:'model'` 的 Select + placeholder）；三者都没有清空 x / hover 互换。
- **实现路线（owner 2026-11-12 裁决：走 ①）**：在 `frontend/src/components/settings/` 下**自封装**一个 clearable 模型下拉（新组件，建议 `GoalCheckModelSelect.vue` 或 `settings/form/ClearableModelCombo.vue`），复用 `frontend/src/components/models/ModelSignature.vue` 与 `useModelChoices.ts`。**不改全局组件库** —— 路线 ②（扩展 `SelectTrigger.vue` 加 trailing icon slot 并去掉 `[&_svg]:pointer-events-none`）与路线 ③（`ComboboxTrigger.vue:22` slot + `ComboboxCancel` 原语）**均被否决**：② 会影响所有用 `Select` 的页面，③ 的 `ComboboxCancel` 语义（清输入框 / `modelValue=null` / `resetModelValueOnClear` 默认 false）与本页要的「同时清空 model 与 effort 两个控件」不是一回事，硬套会引入隐性耦合。
- **前端整体路线（owner 2026-11-12 裁决：路线 A 自封装分组）**：本区块**不进 `SETTINGS_FORMS`**，由 `frontend/src/components/settings/SettingsSubagentsTab.vue`（今天只渲染 `<SettingsTabForm :tab="tab" :form="form" />`，`:4`）在 `<SettingsTabForm>` **之后手写**第三个分组。这样 `frontend/src/lib/settingsSchema.test.ts` 保持 9/9 常绿（见 §3 的机器闸门说明）。
  因此下面这些引用是**「模仿对象」而非「扩展点」**，执行者不得去改它们：`SettingsField.vue:70-97`（`kind:'model'` 的朴素 Select + placeholder，学它的空值语义与 `ModelSignature` 用法）、`settingsSchema.ts:291-292`（既有两个 `kind:'model'` 字段 `memories.extract_model` / `consolidation_model`，学它的 path/值形状；注意 `:162-169` 是 `sa` 分组的 bool/num 字段，**不是** model 样板）、`selectValue.ts:8-18`（`EMPTY_CHOICE = '@empty'` 哨兵，学它怎么区分「未选」与「选了空」）。
- **应用侧分组属性用 `data-group`，不是 `data-grp`**：`data-grp` 只存在于原型 HTML（原型 `:700`）；应用侧既有约定是 `:data-group="group.id"`（`frontend/src/components/settings/form/SettingsGroup.vue:11`）。手写分组时用 `data-group="goal-check-model"`，与 `sa` / `peers` 同一口径，e2e / vitest 选择器也按它写。§5 的判据分「原型侧」与「应用侧」两组，不得混用。
- 文案落点：`frontend/src/locales/index.ts:727`（`'subagents.followPrimary': '跟随主代理'`，裸 placeholder 用这条；**不要用 `:755` 的 `'subagents.modelFollowPrimary': '跟随主代理（{model}）'`**，它带插值会渲染出括号里的模型名，与原型不符）。分组标题「/goal 检查模型」、「跟随模型设置」、「恢复为模型默认」、两条动态 hint 都是新文案，需在中英两份 locale 里各加一条新 key，不得复用带插值的旧 key。

## 3. 落库与配置链（不需要新表 / 迁移，但**不是**全现成：需新加 1 个配置字段 + 1 个导出校验入口 + 1 个 normalizer + 2 条 `settingsFieldList`）

- 运行时配置读自 `fb_config` 的 `agents` section（`pkg/process/config_db.go:150-195`）。
- web 写入：`PATCH /api/settings`（`pkg/gateway/api_extra.go:401-402`、`:9604`）→ `settingsFieldList`（`:9188`）按 yaml tag 逐段定位（`resolveSettingsPath` `:9318-9340`）→ `UpdateRootSection` 落库。
- **落库形状（owner 2026-11-12 裁决：全局字段）**：在 `AgentsSection`（`pkg/config/agents.go:13-16`，今天只有 `Defaults` 与 `Definitions` 两个字段）上**新加一个与它们同级的字段**承载 `goal-evaluator` 的 `model` + `reasoning_effort`，全局生效、与当前主代理无关。复用既有 `SubagentConfig` 类型即可（它已有 `Model` / `ReasoningEffort`），建议形状 `GoalEvaluator SubagentConfig \`yaml:"goal_evaluator,omitempty" json:"goal_evaluator,omitempty"\``，于是两个 settings path 是 `agents.goal_evaluator.model` 与 `agents.goal_evaluator.reasoning_effort`，`resolveSettingsPath`（`api_extra.go:9316-9340`）按 yaml tag 逐段走得到。
  **不走 per-agent entry 路线**（即不放宽 `ValidateAgentSubagents` 的 `system` map）：那会让配置变成 per 主代理、「全局生效」为假，还会让它出现在 §1 明令保持不变的侧栏「子代理」页上。因此 `pkg/config/agents.go:267-269` 与 `pkg/gateway/subagents_api.go:848-849` 的两处 `"is dispatched by the system and cannot be configured"` **一个字节都不动**。
  `agents` 已在 `rootConfigSections`（`pkg/process/config_db.go:40-53`），普通 json 字段自动进 `configSnapshotFingerprint`，**无需建表 / 无需 schema 迁移**（此结论已核实成立）。
- **必须存 row id，不得存 `provider/model` 标签**（这是全局语义成立的前提，已核实）：`resolveModelRef`（`pkg/config/llm_models.go`）对 row id 走 `entry.ID == ref` 分支，匹配 `ConfiguredLLMs(root)`（全局 `root.LLMModels` 表），**`agentName` 完全不参与**；只有 legacy `provider/model` 标签回退才去查 `root.Agents.Definitions[agentName]`。所以存 row id 时全局值对任何主代理都解析一致；一旦存了标签，解析就变成 per-agent，全局语义静默泄漏。网关写入侧的 normalizer 必须把值规整成 row id（样板 `normalizeMemoryModelPath`，`api_extra.go:9738-9760`：空 / `current` 放行 → `ResolveModelRef` 解析 → 取 `entry.ID` 回写 → 解析不到就拒绝）。
- **校验：本节原先写的「沿用既有校验、不新造规则」不可达，必须新加一个调用点（规则本身不新造）。** 已核实：`validateSubagentModel`（`pkg/config/agents.go:308-313`，含 `reasoning_effort requires model`）是 `config` 包私有，唯一调用点在 `ValidateAgentSubagents` 的 per-agent entries 循环内（`:297`），而后者只被 `pkg/config/load.go:929` 与 `pkg/gateway/subagents_api.go:601` 调用 —— 两者都遍历 `def.Subagents`，**全局字段今天没有任何校验入口**。
  落地形状：在 `pkg/config` 里新加一个**导出**的窄入口（例如 `ValidateGoalEvaluatorModel(entry SubagentConfig, refResolves func(string) bool) error`），内部直接调既有的 `validateSubagentModel`，**不复制规则文本**；挂载点**只有一个**：`validateLoadedRoot`（`load.go:174`，紧邻 `:202` 的 `validatePrimaryAgents` 调用）—— 它同时覆盖「装载」与「settings 写入」两条路：装载走 `Load`（`:53/:68`），settings 写入走 `settingsCandidateLoads`（`api_extra.go:9765-9771`）→ `FinishLoad`（`:80-98`）→ `validateLoadedRoot`，`Save`（`:952/:967`）亦经此。**不得挂进 `validatePrimaryAgents`**：它对 `r.Agents.Definitions == nil` 提前 return（`:907-909`），而那正是必须校验的全新安装形态。这样「不新造规则」在语义上成立（同一条规则、同一个函数），但本计划必须承认**新增了调用点与一个导出符号**。
  副作用需注意：`validateSubagentModel` 带 `reasoning_effort requires model`，所以照此挂载即等于 §6 第 2 项「不放宽、强度不能独立于模型」；若 owner 后来裁决要放宽，改的是这个新入口，不是既有函数。
- **`settingsFieldList`（`api_extra.go:9188`）必须加两条**（`agents.goal_evaluator.model` / `agents.goal_evaluator.reasoning_effort`）。它是**独立于前端 `FORMS` 的 Go 注册表**，加它不触碰 `settingsSchema.test.ts`。
- **机器闸门（路线 A 的核心保障，已核实）**：`frontend/src/lib/settingsSchema.test.ts` 直接 `import` 原型 HTML（`:10`，`config-db-settings-preview.html?raw`），`:176` 是 `expect(groups.map(g => g.id), tab).toEqual(prototypeGroups.map(g => g.id))` —— 每个 Tab 的分组 id **顺序全等**；`:188-203` 逐字比字段，`:211-217` 比 hint。原型侧 `FORMS.subagents` 只有 `sa` / `peers`，`goal-check-model` 由独立的 `systemSubagentsHTML()` 在原型 `:1088` 追加、**刻意不在 `FORMS` 里**。
  所以：**只要本区块不进 `SETTINGS_FORMS`，该测试恒 9/9 绿**（这正是路线 A 被选中的原因）。反过来说 —— 一旦有人把它加进 `SETTINGS_FORMS.subagents`，`:176` 立刻红；而通用 `kind:'model'` 只是个朴素 Select，表达不了两行签名下拉 / hover 变 x / 强度依赖模型行 / 动态 hint，逐字比对也过不了。**该测试变红即 STOP：既不要改测试、也不要改原型 `FORMS`、更不要把它塞进 `SETTINGS_FORMS`，回来报告。**
  `settingsSchema.ts:5-23` 声明的「原型 → 本文件 → 组件」流程对**走 FORMS 的字段**仍然成立；本区块是 FORMS 之外的手写分组，属于该流程的例外，需在 `settingsSchema.ts` 顶部注释里补一行说明例外原因，免得后来者以为漏了同步。
- 前端落点：`frontend/src/components/settings/SettingsSubagentsTab.vue`（目前只渲染 `SettingsTabForm`，`:4`）、新组件（clearable 模型下拉）、`frontend/src/views/SettingsView.vue:95`（`subagents` Tab，**不加项**）、`frontend/src/composables/useModelChoices.ts`。`TABS` / `SETTINGS_FORMS` / `SettingsTabForm.vue` / `SettingsField.vue` / `settingsSchema.ts` 的字段表**均不改**。

## 4. 引擎侧约束

- 现状：`goal-evaluator` 是 system 类型，被 `pkg/config/agents.go:267-269` 与 `pkg/gateway/subagents_api.go:848-849` 的 `"is dispatched by the system and cannot be configured"` 硬禁；`subagentRowModel`（`subagents_api.go:284-286`）对非 public 直接 return；`SubagentOwnModel`（`pkg/run/runner.go:1815`）以 public 列表为界。因此它今天必然回退到 `AgentModel` → `PrimaryModelStateForSession`（`pkg/run/config.go:2718`），即会话主模型。
- **读取路径：不动 `SubagentCatalog.Model`，改 `SubagentCatalogFor` 播种。** 本节原先写「只把 `goal-evaluator` 加入可配置白名单、`Model()` 无需改」—— 那是 per-agent 存储下的结论，已被 §3 的全局裁决取代；「加入白名单」这个动作本身**取消**（两处 `cannot be configured` 拒绝一个字节不动）。
  已核实：`SubagentCatalogFor`（`pkg/run/subagent_catalog.go:57-73`）的 `entries` 只来自 `cfg.Agents.Definitions[agentName].Subagents`，而 `Model()`（`:162-179`）只读 `c.entries` —— 全局字段它看不见。**落地形状（补 Steps 时复读更正，推翻早前「播种进 `entries`、`Model()` 不动」的写法）**：给 `SubagentCatalog` 加一个**私有字段**（如 `goalCheck appcfg.SubagentConfig`）承载 `cfg.Agents.GoalEvaluator`，在 `SubagentCatalogFor` 的 `if cfg == nil { return catalog }` 提前 return **之后立即**赋值；`Model()` 开头加一个分支：`entries` 未命中且 `subagentKey(name) == GoalCheckSubagentType` 时改读该字段（其下既有的 `configured == ""` 即 return false，「未配置跟随主代理」原样成立）。`prepareSubagentExecutionResolved` 的 `cat.Model(agentType)` 分支因此原样生效。
  **为什么不能播种进 `entries`（已核实反例）**：`entries` 并非 `Model()` 私有 —— `ResolvePublic`（`:116-138`）把「entries 命中且非 public」当**自定义子代理**返回 `customDefinition`，播种会让 `subagent_run(subagent_type="goal-evaluator")` 从 `unknown subagent_type` 变成可派发，正是 §1 明令禁止的派发入口；`ToolPolicy`（`:183-186`）与 `configures`（`:249-258`）也会由 false 变 true。私有字段只碰 `Model()` 一处，三条对外路径一个字节不动。
  **两个已核实的坑**：① 赋值必须放在 `cfg == nil` 提前 return **之后**、且**不得**放在 `def, ok := cfg.Agents.Definitions[...]; if !ok { return catalog }` 之后 —— 私有字段虽不依赖 `Definitions`，但写错位置就会吞掉「没有任何 `Definitions` 条目的主代理」这一最常见全新安装形态（§5.3 第 5 条测试专钉它）。② `Model()` 内部调 `ResolveModelRef(c.cfg, c.agentName, configured)`，`agentName` 在 row id 路径下不参与解析（见 §3），所以对任何主代理都一致 —— 但**前提是存的必须是 row id**，这条约束在 §3 已定为硬性要求。
  `SubagentOwnModel`（`pkg/run/runner.go:1815`）以 `agent.PublicTypeNames()` 为界、`subagentRowModel`（`subagents_api.go:284-286`）对非 public 直接 return：这两处是**对外报告**路径，本计划不改，也不要求它们报告本页配置（是否要让侧栏/接口回显生效中的模型，属 §6 第 6 项）。其余 9 个内置子代理的校验、派发与读取路径一个字节不动。
- 模型优先级链 `prepareSubagentExecutionResolved`（`pkg/run/subagent.go:1978-2000`）今天有三级：ctx pin（`:1979`）＞ `cat.Model(agentType)`（`:1981`）＞ `AgentModel(...)`（`:1983`）。owner 定案的两级语义要求 `goal-evaluator` **跳过第 1 级**，只留「本页配置 ＞ 主代理当前模型」。
- **守卫必须按类型收窄到 `GoalCheckSubagentType`（`pkg/run/goal.go:100`），不能写成「system 分支一律忽略 ctx pin」。** 已核实的反例：`plan-reviewer` 同样是 system 类型（`pkg/agent/subagent_defs.go:26` 的 `SystemSubagentNames[0]`），而它的审批卡用户选模型正是**派发前**把 pin 放上 ctx 的 —— `RunPlanReviewSubagent`（`pkg/run/subagent.go:4792-4807`，注释明写「pinned before the dispatch … so the pinning reaches the whole run」）。按 system 类型设 guard 会直接打死 §1 要求「保持不变」的 283 行为。
  落地形状：在 `:1978` 读 ctx pin 之前加一个只认 `agentType == GoalCheckSubagentType` 的判断，命中则不读 ctx pin、直接进 `cat.Model(agentType)`；其余类型的三级链一个字节不动。`agentType` 在该函数内已可用（由 `d.subtype` 经 `resolve` 得出）。
  **必须有回归测试双向钉住**：① `goal-evaluator` 即使 ctx 上带 pin 也走本页配置（新用例，样板见 297 已产出的 `pkg/run/goal_test.go:743` `TestGoalCheckCanBePinnedToAConfiguredRow`，按本计划语义反向改写）；② `plan-reviewer` 的 ctx pin 仍然生效（既有 `TestPlanReviewSpawnEventNamesTheChosenModel`，`pkg/run/subagent_test.go:430-461`，必须保持 PASS 且断言不得放宽）。
- `ResolvePublic`（`subagent_catalog.go:116-134`）已只允许 public + 自定义，主代理本来就没有 `goal-evaluator` 的派发入口；本计划不给它开任何入口。
- 不碰 `list_usable_models` 工具定义、准入规则与三种拒绝文案（owner 逐字定稿）；不碰审批卡用户当场挑选模型的 `WithSubagentModelOverride` 路径。
- `guardian` 不走 `cat.Model`（`runner.go:932` 的 `NewLLMForAgentType`，注释称整条路径是 dead code），与本计划无关。

## 5. 验证（判据池：Steps 见 §8，Done criteria 见 §9，STOP 见 §10）

> **本节按三侧分组，执行者不得混用**：5.1 只对原型 HTML 成立（`data-grp` / `.grp` / `.ss-caret-zone` 这些选择器只存在于原型），5.2 只对应用成立（应用侧是 `data-group`，见 §2），5.3 是 Go。**本节原先完全没有应用侧判据**（无新组件 vitest、无 e2e），补 Steps 时必须把 5.2 逐条落成可跑命令。

### 5.1 原型侧
- `node --check` 抽取原型 `<script>` 通过；`cd frontend && pnpm exec vitest run src/lib/settingsSchema.test.ts` **9/9 恒绿**（路线 A 下本区块不进 `SETTINGS_FORMS`，该测试与本计划无耦合；**变红即 STOP**，见 §3 的机器闸门）。
- 原型自检（DOM stub 真跑）：区块是第三个 `.grp` 分组、排在 `sa` / `peers` 之后，两条 `.row` 字段行、无卡片外框 / 状态徽标、`.tray` 初始收起且改动后展开；应用内部无执行说明字样；**「同名模型可用接口地址区分」「跟随主代理时不可单独设置」两句说明均为 0 残留、初始态两个字段都没有 `.h`**；初始 `model=''` / `effort=''`、两个下拉都显示 placeholder「跟随主代理」、思考强度 select 有 `disabled` + `unset` class（与 `.combo .ph` 同为 `var(--quiet)`）、无「清除配置」链接、**无常驻清空按钮**、`.ss-caret-zone` 的 `:hover` 规则齐备（箭头隐藏 / x 显示 / x hover 变色）且 x 是 `data-ss="clear"` 的最近可触发元素；菜单默认展开、8 个候选项且无「跟随主代理」项、搜索真过滤（「火山」命中 2 行）、选中打勾；点选模型后强度默认回显模型页默认值、select 启用且去掉 `unset`、回显说明出现；点 hover 出现的 x 后两个下拉同步回到 placeholder、强度禁用、说明消失、菜单收起；保存 / 放弃 / 回显均生效。
### 5.2 应用侧（Vue —— 本节原先完全缺失，补 Steps 时必须逐条落成命令）
- `cd frontend && pnpm typecheck` exit 0、`pnpm build` exit 0。
- **新组件 vitest**（新文件，建议 `frontend/src/components/settings/__tests__/GoalCheckModelSelect.spec.ts`；结构样板参考既有 `frontend/src/lib/settingsSchema.test.ts` 的 `describe`/`it` 口径与仓库现有组件测试）逐条钉住 §2 的交互清单：初始未配置态（两个下拉都是 placeholder「跟随主代理」、强度 `disabled`、无 hint、保存条收起）→ 打开菜单 / 搜索过滤 / 点选回填两行签名并打勾 → **选中后强度自动回显该行的 `default_effort`** → 切换模型行时档位与选中值一起换 → 改强度后 hint 变「已改为…」且出现「恢复为模型默认」→ 点它回到 `default_effort` → hover 箭头区变 x、点 x 同步清空两个控件并收起菜单 → 保存 / 放弃更改 / 重新打开回显。
- **同名两行必须各测一次**：`/choices` 里同 `provider/model` 不同 `id` 的两行，选中后落库的是各自的 row id（不是标签），且回显时能区分（§2 的两行签名就是为此存在）。
- **e2e**（新 spec，建议 `frontend/e2e/settings-goal-check-model.spec.ts`；跑法沿用仓库既有的 `FOREBRAIN_E2E_SPEC="<spec>" scripts/acceptance/web_e2e.sh`，样板见 295 的两个 approval spec）：设置 →「常规 / 子代理」Tab 里能看到 `data-group="goal-check-model"` 分组、排在 `data-group="sa"` / `"peers"` 之后；选一个模型行 + 强度 → 保存 → 刷新页面回显一致。
- **顺带修一条过时文案（§6 第 6 项）**：`frontend/src/locales/index.ts:709` 的「保存在 forebrain.yaml，新会话生效」与实现不符（实际落 `fb_config`），中英两份都改；**同段的「系统子代理只能查看」不改**（对侧栏子代理页仍成立）。改完 `grep -rn "保存在 forebrain.yaml" frontend/src` 应 0 命中。
- **不得改动的文件（改了即 STOP）**：`frontend/src/lib/settingsSchema.ts` 的字段表、`frontend/src/lib/settingsSchema.test.ts`、`SettingsTabForm.vue`、`SettingsField.vue`、`ai-elements-vue/packages/shadcn-vue/**`（全局组件库，见 §2 路线裁决）、原型 HTML（闸门解除后原型即冻结）、**侧栏子代理页及其组件**（§6 第 6 项已定：不回显本页配置，零改动）。

### 5.3 Go 侧
- 既有回归：`CGO_ENABLED=1 go test -tags fts5 ./pkg/config ./pkg/gateway ./pkg/run ./pkg/process -count=1`、`go build -tags fts5 ./...`、`go vet -tags fts5 ./...`、`gofmt -l pkg cmd` 无输出、`pkg/architecture` 绿。
- **本计划必须新增的定向测试（缺一条都不算 DONE —— 这是唯一能证明「配置真的生效」的闸门，既有套件全绿也证明不了）**：
  1. **两级语义 · 正向**：全局配了 model 行时，`goal-evaluator` 的派发 pin = 该行的 id/provider/model/effort。写在 `pkg/run/goal_test.go`；样板是 297 已产出的 `TestGoalCheckCanBePinnedToAConfiguredRow`（`goal_test.go:743`，ctx 注入即可钉到行），**按本计划语义改写**成「配置注入而非 ctx 注入」，断言口径参考 `pkg/run/subagent_test.go:2267-2283` 的 `executor.pinnedModel` / `executor.model`。
  2. **两级语义 · ctx pin 被忽略**：ctx 上带 `WithSubagentModelOverride` 的另一个行 id 时，`goal-evaluator` 仍走本页配置（不是 ctx 的值）。这是 owner 两级裁决的直接断言，**必须有**。
  3. **不误伤 `plan-reviewer`**：既有 `TestPlanReviewSpawnEventNamesTheChosenModel`（`pkg/run/subagent_test.go:430-461`）保持 PASS，**断言不得放宽**；它证明 §4 的守卫确实按 `GoalCheckSubagentType` 收窄了，而不是按 system 类型。
  4. **未配置时跟随主代理**：全局字段为空时，`goal-evaluator` 的 pin 来自 `AgentModel` → `PrimaryModelStateForSession`（`pkg/run/config.go:2718`），即会话主模型（现状不得回归）。
  5. **播种不被提前 return 吞掉**：主代理在 `cfg.Agents.Definitions` 里**没有任何条目**时（全新安装的常见形态），全局配置仍然生效 —— 这条专钉 §4 坑 ① 的两处提前 return。
  6. **新校验入口**：`ValidateGoalEvaluatorModel`（或最终命名）对「有 effort 无 model」拒绝、对解析不到的 ref 拒绝、对合法 row id 放行；规则文本与 `validateSubagentModel` 同源（不得复制粘贴出第二份）。
  7. **settings 写入链**：`PATCH /api/settings` 写 `agents.goal_evaluator.model` 时把值规整成 row id、写不存在的行被拒；样板参考既有 `TestSettingsPatchKeepsMemoryModelsOnConfiguredRows`（265 产出）。同一条链上钉住裁决 10：写 `reasoning_effort` 而 `model` 为空必须被拒（`reasoning_effort requires model`）。
  8. **实时读语义（裁决 11 的核心断言，缺它则「实时读」只是 UI 幻觉）**：`agents.goal_evaluator.model` = 某行 id、`reasoning_effort` = **空**时，`goal-evaluator` 实际发出的请求带的是**该行的 `caps.default_effort`**；把该行的 `default_effort` 改成另一个档位、配置重新采纳后，再派发一次应带**新值**（不是旧值、也不是空）。依据是 `llmProviderFromModelRow`（`pkg/config/llm_models.go:338-343`）的折算 + `ConfiguredModelClientWithEffort`（`subagent.go:4588-4604`）的「effort 为空即不覆盖」。
     对照组：`reasoning_effort` = 具体档位时，发出的必须是该档位、**不随行的 `default_effort` 变化**（覆盖语义）。
     **若第二个断言（改默认后跟随）因为客户端缓存而不成立** —— `clientFor` 按 `ModelID/Provider/Model` 缓存，而 `cat.Model()` 这条路的 pin 不带 `Entry`/`Version`（`subagent.go:1981`），缓存键无行版本 —— **不要自行加缓存失效、也不要改测试断言**；把实测到的生效边界（第几轮 / 是否需要重建 runner）如实写进报告，交 owner 决定是否另立计划。§2 的保存条文案「下一轮对话生效」在裁决前不得改动。
- 定向回归（不得变红）：`TestGoalCheck*`（`goal_test.go:597-633`）、`TestSubagentModelOverride*`（`subagent_test.go:1268-1320`）、`TestUsableModelsListAndDispatchAgree`（293 产出）。
- **`list_usable_models` 工具定义字节稳定**：`grep -c 'Optional id of a model from list_usable_models'` 命中数恒定（297 记为 3），且工具定义 / 系统提示的字节对比不变 —— 前缀缓存依赖它。
- **注意（基线本来就红，判定口径 = 等价而非全绿）**：干净基线 `d715dafd` 上 Go 全包回归本来就是红的（permission / approval gate 类，日志为 `no sandbox available; every conversation runs with full access platform=darwin`，以及测试临时 home 缺 model catalog / 词典资产；`FOREBRAIN_ASSETS_DIR` 与移除仓库根 `forebrain.yaml` 均不能转绿）。已知 FAIL 面：`pkg/run` 8 条、`pkg/tui` 20 条、`pkg/process` 3 条、`pkg/gateway` 7 条、`pkg/turn` 3 条，另 `pkg/tool` 的 `TestBatchShellIsConfinedToTheTaskWorkspace`。按「FAIL 集合与基线等价、无新增失败」判定，**不要修这些测试、不要放宽任何断言、不要把基线红写成通过**。
  基线名单不要依赖 `/tmp` 里的旧文件（换机器即失效）；需要时按下面就地重生成，再与执行环境的 FAIL 集合逐条比对：
  ```
  rm -rf /tmp/fb-baseline && mkdir -p /tmp/fb-baseline
  git archive d715dafd | tar -x -C /tmp/fb-baseline
  cp third_party/**/libtokenizers.a /tmp/fb-baseline/<同路径>/   # 未跟踪产物，需补上才能链接
  cd /tmp/fb-baseline && CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tui ./pkg/process ./pkg/gateway ./pkg/turn ./pkg/tool -count=1 2>&1 | tee /tmp/fb-baseline-failures.txt
  ```
  若 `libtokenizers.a` 的实际路径与上面不符，以本仓库 `git status --ignored` 里该产物的真实位置为准；**找不到就 STOP 报告**，不要跳过链接去改判据。
- Drift check（开工第一步）：`git rev-parse --short HEAD`（应 `ecc4e1aa`）、`git status --porcelain -- <Scope>`、`git diff --stat ecc4e1aa..HEAD -- <Scope>`、`git diff -- <Scope>` 逐段比对当前状态摘录。工作区里 290/293/294/295/297 与其它并行会话的未提交改动是预期前置，不要回退。
- 执行约定：**in-tree、执行者零 commit**（不改 `git add` / commit / merge / rebase / push / worktree）；**跳过**「更新 `plans/README.md` 状态行」，由评审方维护索引。

## 6. 裁决记录（原「仍需 owner 裁决」；2026-11-12 全部定案，开放问题清零）

> 1–6 是原型迭代期间逐轮记下的问题，7–9 是 `review-plan` 新增的三项，10–12 是 1–6 里真正需要 owner 拍板的三项。**当前状态：2/3/4/5/7/8/9/10/11/12 全部已决**（各项就地标注结论与依据）；**1 随原型确认一并生效**（原型即定稿：第三个分组、排在 `sa`/`peers` 之后、标题「/goal 检查模型」，owner 01:30 截图定稿后未再改动）；**6 不是决策而是一条待做的修改**（过时文案），已并入 §5.2 的范围。

1. **区块位置与标题**：**已随原型确认生效（2026-11-12 owner 确认，确认时未另作指示）**：第三个 `.grp` 分组、排在 `sa`/`peers` 之后、标题「/goal 检查模型」，原型自此冻结（§5.2 不得改动清单已含原型 HTML）。
2. **思考强度能否独立于模型 → 已决：不放宽**（见第 10 项）。引擎硬校验 `reasoning_effort requires model`（`pkg/config/agents.go:310-313`）原样保留，新校验入口直接复用它；原型按现状表现（两个下拉处于「跟随主代理」placeholder 时，思考强度为 `disabled` 禁用态）。
3. **审批卡用户挑选的 `plan-reviewer` 模型**确认不受本页影响（原型按不变处理）。**评审已用代码核实成立**：`RunPlanReviewSubagent`（`pkg/run/subagent.go:4792-4807`）在派发前把 pin 放上 ctx，与本页配置走的是 §4 链上不同分支；两级语义的守卫按 `GoalCheckSubagentType` 收窄后即互不干扰。此项不再需要 owner 裁决，但 §4 要求的 `TestPlanReviewSpawnEventNamesTheChosenModel` 回归必须绿。
4. **校验与生效边界 → 已决**（见第 12 项）：保存时走新 normalizer，解析不到即拒；派发时 `Model()` 解析不到则 `ok=false`、静默回退主代理模型（现状语义不改）；**不显示「当前实际生效的模型」**（1:1 硬约束 + §2 已定稿去掉状态徽标）。生效提示只有 `.tray` 的「✓ 已保存，下一轮对话生效」；该文案是否严格成立由 §5.3 第 8 条实测钉住，**测出不成立也不得改文案**，如实报告交 owner。
5. **思考强度默认值的读取口径 → 已决：实时读模型行**（见第 11 项）。落库映射：UI 选值等于该行 `default_effort` ⇒ 存空；不等于 ⇒ 存具体档位。引擎侧零改动即成立（`pkg/config/llm_models.go:338-343` 已折入 `Caps.DefaultEffort`）。
6. **过时文案（不是决策，是一条待做的修改）**：`frontend/src/locales/index.ts:709` 的「保存在 forebrain.yaml，新会话生效」与实现不符（实际落 `fb_config`），本计划一并修；「系统子代理只能查看」对**侧栏子代理页**仍成立（5 个 system 保持只读，`goal-evaluator` 的模型改在设置页配），**不改这句**，但侧栏页是否要回显设置页配的模型 —— 按 §4 的结论**不回显**（`SubagentOwnModel` / `subagentRowModel` 是对外报告路径，本计划不改），故侧栏页零改动。

### 评审新增的三项 —— 已由 owner 裁决（2026-11-12），结论已写进 §2/§3/§4/§5，此处留裁决依据

7. **落库形状 → 裁决：全局字段**（`AgentsSection` 新增与 `Defaults` / `Definitions` 同级的字段）。依据与代价已核实并写入 §3：
   - `SubagentCatalogFor`（`pkg/run/subagent_catalog.go:57-73`）的 `entries` 只来自 per-agent 的 `Definitions[agentName].Subagents`，`Model()`（`:162-179`）只读 `c.entries` —— 全局字段它看不见，所以 **`SubagentCatalogFor` 必须改、`Model()` 加一个只读私有字段的分支**（见 §4；早前「播种进 `entries`、`Model()` 一个字节不动」的写法在补 Steps 时被复读推翻 —— 播种进 `entries` 会让 `ResolvePublic`（`:130-137`）把 `goal-evaluator` 当自定义子代理开出派发入口，正是 §1 禁止的）。
   - `validateSubagentModel` 只有一个调用点（`pkg/config/agents.go:297`，per-agent 循环内），故 §3 原写的「沿用既有校验、不新造规则」**不可达**：必须新加一个导出的窄入口并挂载。规则文本同源、不复制。挂载点经复读收敛为**一个**：`validateLoadedRoot`（`settingsCandidateLoads` → `FinishLoad` → `validateLoadedRoot`，装载与写入同经此）。
   - 被否决的选 B（per-agent）：会让「全局生效」为假、配置出现在 §1 明令不动的侧栏子代理页、且要同时放宽 `subagents_api.go:848-849`。
   - 裁决带出的**新硬约束**（评审核实）：**必须存 row id，不得存 `provider/model` 标签** —— row id 走 `resolveModelRef` 的 `entry.ID == ref` 分支、匹配全局 `ConfiguredLLMs(root)`、`agentName` 不参与；标签回退才查 `Definitions[agentName]`，一存标签全局语义就静默变成 per-agent。
8. **候选数据源 → 裁决：改用 `GET /api/llm-models/choices`，`300` 成为硬前置**。依据已核实并写入 §2：`/api/llm-models` 明文带 `api_key`（字段注释自陈「never in `/choices`」）、跑订阅发现探测、分页且 agent 作用域，且与 266 定下的「所有 picker 走 `/choices`」约定相反。代价：`turn.ModelChoice` 今天无 `DefaultEffort`，须等 `300` 给 choices 加 `default_effort`；**`300` 未落地时本计划前端部分 BLOCKED**，不得自行给 `/choices` 加字段（撞 300 的范围）。
9. **前端实现路线 → 裁决：路线 A 自封装**。依据已核实并写入 §2/§3/§5：`settingsSchema.test.ts:176` 要求每个 Tab 的分组 id 顺序全等、`:188-203` 逐字比字段、`:211-217` 比 hint；原型侧 `FORMS.subagents` 只有 `sa`/`peers`，`goal-check-model` 由独立的 `systemSubagentsHTML()` 在原型 `:1088` 追加、刻意不在 `FORMS` 里。路线 A（手写分组、不进 `SETTINGS_FORMS`）下该测试恒 9/9；路线 B 会撞全等与逐字比对，且通用 `kind:'model'` 只是朴素 Select，表达不了两行签名 / hover 变 x / 强度依赖模型行。
   裁决带出的两处修正：**应用侧属性是 `data-group` 而非原型的 `data-grp`**（`SettingsGroup.vue:11`）；§5 已按「原型侧 / 应用侧 / Go 侧」三组重写，补齐原先完全缺失的 Vue 判据（新组件 vitest + e2e）与「不得改动的文件」清单。

### 10/11/12 —— 已由 owner 裁决（2026-11-12），下面是裁决与依据，不再是开放问题

10. **强度能否独立于模型 → 裁决：不放宽。** 沿用 `validateSubagentModel` 的 `reasoning_effort requires model`（`pkg/config/agents.go:310-313`）：`model` 为空时 `reasoning_effort` 必须也为空。落地后果已在 §3 定形 —— 新加的导出校验入口直接复用同一条规则，**不改既有模型文本、不给全局字段开例外**。UI 侧对应的就是 §2 已定稿的表现：未选模型时思考强度是 `disabled` 禁用态、只剩 placeholder「跟随主代理」。
11. **默认强度实时读 vs 快照 → 裁决：实时读模型行。** 评审核实到**这条裁决零引擎改动即可成立**，因为引擎今天就是这个语义：
    - `llmProviderFromModelRow`（`pkg/config/llm_models.go:297`，折算在 `:338-343`）把模型行解析成 provider 配置时，**若行自己的 `Params` 没有 `reasoning.effort`，就把 `row.Caps.DefaultEffort` 折进去**（注释原文：「The row's own default effort wins only when the params do not already name one … a row-level default is what fills its absence」）。
    - `resolveAgentProvider` 按 row id 命中的正是这个折算后的 entry；而 `ConfiguredModelClientWithEffort`（`pkg/run/subagent.go:4588-4604`）与 `modelClientFromEntry` 都是 **`if effort != "" { entry.Params = WithReasoningEffort(...) }`** —— effort 为空即不覆盖，行默认值原样生效。
    - **于是落库映射唯一确定（执行者不得自行发明）**：UI 上选的强度 **等于该行 `default_effort`** 时存 `reasoning_effort = ""`（= 实时跟随）；**不等于**时才存具体档位（= 覆盖）。原型已经算好了这个 bit —— `effortReset`（原型 `:697`，即「恢复为模型默认」链接的显示条件）为真 ⇒ 存空、为假 ⇒ 存具体值，对应 hint 的「模型页配置的默认强度：…」与「已改为…，仅影响目标检查子代理」两种文案。**注意这与原型的内存状态不同**：原型选中模型行后把 `effort` 置成具体的 `defaultEffort`（`:759`）以便回显，落库时必须按上面的映射归一成空值，**不得把回显值直接写库**（否则实时读被静默变成快照）。
    - **实时读的口径必须写清（评审补充，防止误解为「每次请求查库」）**：读的是 **runner 当前已采纳配置**里该行的 `caps.default_effort`（配置经 `process.SyncConfigFromDB` 采纳），不是每轮查库。且 `subagentModelOverrideLLM.clientFor`（`subagent.go:4544-4566`）按 `ModelID/Provider/Model` 缓存客户端，而 `cat.Model()` 这条路构造的 pin **不带 `Entry` / `Version`**（`subagent.go:1981`），缓存键里就没有行版本 —— 所以模型页改了默认强度后，**已存在的 runner 可能仍复用旧客户端**，直到配置重新采纳 / 客户端重建。这与原型保存条那句「✓ 已保存，下一轮对话生效」是否严格一致，取决于 runner 与配置采纳的生命周期；**执行者必须用测试把实际口径钉住**（§5.3 第 8 条），若测出「下一轮不生效」，如实报告，**不要改文案、也不要自行加缓存失效逻辑**（超出本计划范围）。
12. **校验时机与生效边界 → 由前两项裁决 + owner 的 1:1 硬约束共同定案，无需再问。**
    - 校验时机：保存时走新 normalizer，解析不到就拒（样板 `normalizeMemoryModelPath` 的「解析不到拒绝」分支，`api_extra.go:9752-9758`）；派发时 `SubagentCatalog.Model()` 解析不到则 `ok=false`，静默回退主代理模型（现状语义，不改）。
    - 是否显示「当前实际生效的模型」：**不显示**。owner 的硬约束是「原型稿的 UI 与 UX 就是执行者最终要实现的样子，1:1 还原、不留占位」，而 §2 已定稿「去掉卡片头、状态徽标、独立大标题」，页面里没有任何生效值回显控件。生效提示只有 `.tray` 里那句「✓ 已保存，下一轮对话生效」。

## 7. 维护说明

- **2026-11-12 `improve review-plan`（本轮）**：owner 裁决「**保留 `goal-evaluator` 模型优先级只有两级：本页配置 ＞ 主代理当前模型**」，据此改了 §1（新增定案条目，并记明它取代 `plans/297` 的 ctx pin 路线：297 的 (a) 行 id 来源答为「设置页配置」、(b) 是否立项答为「否」、授权裁决 ③ 失去落点仅留痕）与 §4（守卫**按 `GoalCheckSubagentType` 收窄**，不得写成「system 分支一律忽略 ctx pin」；给出双向回归测试要求）。
  评审同时修掉 3 处引用勘误（§2 落点：`settingsSchema.ts` 的 model 样板在 `:291-292` 而非 `:162-169`；placeholder 文案用 `locales:727` 而非带插值的 `:755`；§5 基线名单改为就地重生成，不再依赖 `/tmp` 旧文件），把 §6 第 3 项标为「已由代码核实成立、不再需要裁决」，并新增 §6 的 7/8/9 三个阻塞项（落库形状全局 vs per-agent、候选端点与 plan 300 前置、前端路线 A/B 与 `settingsSchema.test.ts:176` 的分组全等）。
  **本轮未改**：§2 的 UI/UX 规格（原型 21 项断言经独立冷读全部 CONFIRMED，`node --check` 通过，应用内无执行说明泄漏）、组件库能力调研 7 项断言（全部 CONFIRMED，「需自行开发」结论成立）、§3 的「不新增表 / 不改 schema」（`rootConfigSections` 确含 `"agents"`，`config_db.go:34-53`，结论成立）。
- **2026-11-12 同轮 owner 追加三项裁决（评审提问后）**，已全部写进正文并新增 `## Status` 块：
  ① **落库形状 = 全局字段**（`AgentsSection` 上与 `Defaults`/`Definitions` 同级的新字段）→ 改写 §3（含「必须存 row id、不得存 `provider/model` 标签」这条新硬约束，依据是 `resolveModelRef` 的 row id 分支匹配全局 `ConfiguredLLMs(root)`、`agentName` 不参与）与 §4（改为「`SubagentCatalogFor` 播种、`Model()` 不动」，并写明它的两处提前 return 是坑）；「加入可配置白名单」这个动作**取消**，两处 `cannot be configured` 一个字节不动；同时承认「沿用既有校验、不新造规则」不可达，改为「新加一个导出的窄入口复用同一条规则、两处挂载」。
  ② **候选端点 = `/api/llm-models/choices`** → 改写 §2 候选数据源整段（并写明不用 `/api/llm-models` 的三条已核实理由），把 **`300` 列为硬前置**（`turn.ModelChoice` 今天无 `DefaultEffort`）；`useModelChoices.ts` 由「拿不到 default_effort」变成可复用。
  ③ **前端路线 = A 自封装** → 改写 §2 实现路线（②③ 明确否决并给理由）与落点措辞（`settingsSchema.ts` / `SettingsField.vue` / `EMPTY_CHOICE` 降为「模仿对象」而非「扩展点」）、修正 `data-grp` → 应用侧 `data-group`（`SettingsGroup.vue:11`）、§3 增「机器闸门」段（`settingsSchema.test.ts` 恒 9/9 的成立条件 + 变红即 STOP）。
  §5 按「5.1 原型侧 / 5.2 应用侧 / 5.3 Go 侧」三组重写：**补齐原先完全缺失的 Vue 判据**（新组件 vitest 逐条钉交互、同名两行各测一次、新 e2e spec、「不得改动的文件」清单），Go 侧新增 **7 条必须新写的定向测试**（两级语义正向 / ctx pin 被忽略 / 不误伤 `plan-reviewer` / 未配置时跟随主代理 / 播种不被提前 return 吞掉 / 新校验入口 / settings 写入链）+ `list_usable_models` 工具定义字节稳定判据。§6 的 7/8/9 转为「已裁决 + 依据留痕」，并新增 10/11/12 三项待裁决（**这三项已在下一条全部定案**）。
- **2026-11-12 同轮 owner 追加裁决「确认第10项不放宽，默认强度实时读模型行」**，§6 的 10/11/12 全部转为已裁决，落点如下：
  ⑩ **不放宽** —— 新校验入口直接复用 `validateSubagentModel`，`reasoning_effort requires model`（`pkg/config/agents.go:310-313`）对全局字段同样生效；不为「全局字段可以只填强度」开任何例外。
  ⑪ **默认强度实时读模型行、不快照** —— 评审核实这条**零引擎改动**即可成立：`llmProviderFromModelRow` 只在 params 未点名强度时才折入 `row.Caps.DefaultEffort`（`pkg/config/llm_models.go:338-343`，注释自陈「a row-level default is what fills its absence」），而 `ConfiguredModelClientWithEffort`（`pkg/run/subagent.go:4588-4604`）对空 effort 是 no-op。故**库里存空 = 实时跟随模型页配置**，落库映射由此唯一确定：UI 选的强度**等于**该行 `default_effort`（判据就是原型已算好的 `effortReset`，原型 `:697`）→ 存 `reasoning_effort = ""`；**不等于** → 存具体档位（覆盖）。**不得把回显值直接写库**，否则实时读会静默退化成快照。⑪ 带出一条已核实的口径缺口：`subagentModelOverrideLLM.clientFor` 的缓存键只有 `ModelID/Provider/Model`，而 `cat.Model()` 钉入的 override 不带 `Entry`/`Version`（`pkg/run/subagent.go:1981`），所以模型页改 `default_effort` 后**同一进程内可能命中旧 client**；§5.3 第 8 条测试是它唯一的经验闸门，且明令执行器**不得自行加缓存失效、不得放宽断言，遇到就报**。
  ⑫ **关闭** —— 保存时新 normalizer 拒（解析不到即拒），派发时 `Model()` 解析不到则 `ok=false`、静默回退主代理模型；**不向用户回显「实际生效模型」**，因为 §1 的原型 1:1 硬约束不允许加原型没有的元素。
- **开放裁决已清零，只剩原型闸门一道**：owner 确认 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/config-db-settings-preview.html` 之后即可把 §5 展开成有序 Steps（每步带 Verify 命令）、Done 判据清单与 STOP 条件；**补 Steps 时不需要再问 owner 任何事**。
- **2026-11-12 原型迭代（owner 截图反馈）**：「使用的模型」下拉与「思考强度」下拉宽度不一致 —— 根因是原型里一条 bespoke 覆盖 `.ss-row .rs .fld{width:220px}`，而模型 combo 走 `.combo{width:100%}` 铺满 `.row` 的 360px 右列。**已删该覆盖**，两个控件统一为铺满右列（与本页其它 select 的 `.fld{width:100%}` 惯例一致）；**不反向收窄 combo**，因为 `.sig b` 是 `white-space:nowrap` 且无 ellipsis，220px 会截断长模型名 + 服务商的两行签名。§2 对应条目已同步（原「宽 220px」作废）。窄屏（`.stage.narrow`）下两控件同样同为整行宽，不再错位。
- 本轮（2026-11-12 00:29 → 02:02）owner 连续收窄需求：6 个 system → 只剩 `goal-evaluator`；新增 Tab → 放进已有「子代理」Tab；新增思考强度；模型选择器改截图那种两行签名下拉；原型清成纯 UI（标题下说明全删、首项「跟随模型设置」）；思考强度默认回显模型页配置；区块按截图改成第三个 `.grp` 分组、弃用卡片；combo 回显去掉 endpoint；最后定稿「跟随主代理」只是两个下拉的 placeholder、不作为候选项，删掉「清除配置」链接、改用 combo 右侧清空图标按钮同步清空两个下拉，初始态回到未配置的默认态（**覆盖 01:38 中途的「作为第一位选项」方案**）；01:52 删掉两句字段说明、把「置灰」明确为 `disabled` 禁用态并统一两个 placeholder 的字体颜色；01:55 删掉常驻 x、改成 hover 箭头变 x 的清空交互，并按 owner 要求调研组件库后确认**需自行开发**（结论与扩展点在 §2）。**以 `plans/README.md` 最顶部条目与本文件 §1 为准**，下面同名条目是留痕。
- **2026-11-12 owner 确认原型 → 闸门解除**，补全 `## 8 执行步骤` / `## 9 Done criteria` / `## 10 STOP 条件`（15 个 Step、每步带 Verify），原型自此冻结。补 Steps 时的复读更正两处：① **全局字段不得播种进 `catalog.entries`** —— `ResolvePublic`（`subagent_catalog.go:130-137`）会把命中 entries 的非 public 名当自定义子代理返回 `customDefinition`，等于给 `goal-evaluator` 开出 §1 明令禁止的派发入口（`ToolPolicy` `:183-186`、`configures` `:249-258` 同理由 false 变 true）；改为 catalog 私有字段 + `Model()` 一个分支，§4 与 §6-⑦ 已同步改写。② **校验入口只需挂载一次**（`validateLoadedRoot`）：`settingsCandidateLoads`（`api_extra.go:9765-9771`）→ `FinishLoad`（`load.go:80-98`）→ `validateLoadedRoot`，装载与 settings 写入同经此；且不得挂进 `validatePrimaryAgents`（`Definitions == nil` 提前 return，`load.go:907-909`）。另核实 **300 尚未落地**（`pkg/turn/models.go` 的 `ModelChoice` 无 `DefaultEffort`、`handleLLMModelChoices` 不出 `default_effort`、`LlmModelChoice` 无 `defaultEffort`），故 Step 0 / Step 9 的三条 grep 是前端阶段硬闸门：缺失则只跑 Steps 1–8 后报 BLOCKED。
- 全程源码零改动；改动的只有原型 HTML、`plans/README.md`、本文件与 `.workbuddy/memory/2026-11-12.md`。

## 8. 执行步骤（Steps，有序；每步自带 Verify）

> 执行约定（§5.3 末条）：**in-tree、零 commit**（不 `git add` / commit / merge / rebase / push / worktree）；**跳过**「更新 `plans/README.md`」，索引由评审方维护。工作区里 290/293/294/295/297 与其它并行会话的未提交改动是预期前置，**不要回退**。
> 顺序原则：Go 侧先落地并可独立验证（Steps 1–8），前端阶段（Steps 9–14）以 plan 300 为硬闸门；Step 15 收口。任何一步 Verify 不过，先修本步再前进；同一 Verify 连续两次合理修复后仍失败 → §10 STOP。
>
> **In-scope 文件清单（§9 的 `git status` 判定以此为准）**：
> - Go：`pkg/config/agents.go`、`pkg/config/load.go`、`pkg/run/subagent_catalog.go`、`pkg/run/subagent.go`、`pkg/gateway/api_extra.go`；测试：`pkg/run/goal_test.go`、承载 `ValidateAgentSubagents` 用例的 pkg/config 测试文件（`grep -rn "ValidateAgentSubagents" pkg/config/*_test.go` 定位）、承载 `TestSettingsPatchKeepsMemoryModelsOnConfiguredRows` 的 pkg/gateway 测试文件（同法定位）。
> - 前端：`frontend/src/locales/index.ts`、`frontend/src/components/settings/SettingsSubagentsTab.vue`、`frontend/src/lib/settingsSchema.ts`（**仅顶部注释一行**）、`frontend/src/lib/api.ts`（**仅当** Step 9 发现 `defaultEffort` 透传缺失）、新文件 `frontend/src/components/settings/GoalCheckModelSelect.vue` / `GoalCheckModelGroup.vue` / `frontend/src/components/settings/__tests__/GoalCheckModelSelect.spec.ts` / `frontend/e2e/settings-goal-check-model.spec.ts`。
> - 其余一切文件 out-of-scope；§5.2 的不得改动清单是其子集且优先级更高。

### Step 0：开工 drift 与前置检查
1. `git rev-parse --short HEAD` 记录 SHA。计划写在 `ecc4e1aa`；不同则必须做第 2 条比对。
2. `git diff --stat ecc4e1aa..HEAD -- pkg/config pkg/run pkg/gateway pkg/process frontend/src frontend/e2e` 与 `git status --porcelain -- pkg/config pkg/run pkg/gateway pkg/process frontend/src frontend/e2e`，逐段对照本文件摘录：`pkg/config/agents.go:13-16`（`AgentsSection` 仅 `Defaults`/`Definitions` 两字段）、`:308-313`（`validateSubagentModel`）、`pkg/run/subagent_catalog.go:57-74` 与 `:162-179`、`pkg/run/subagent.go:1978-2000`（三级链）、`pkg/gateway/api_extra.go:9188-9199` / `:9686-9697` / `:9737-9760`、`pkg/config/load.go:906-938`、`frontend/src/components/settings/SettingsSubagentsTab.vue:1-20`、`frontend/src/composables/useModelChoices.ts`。**对不上即 §10 STOP**。
3. **300 硬前置三条 grep**：`grep -n "DefaultEffort\|ConfiguredEffort" pkg/turn/models.go`；`grep -n "default_effort" pkg/gateway/models_api.go`；`grep -n "defaultEffort" frontend/src/lib/api.ts`。三条都命中 → 前端阶段可执行；**任一缺失 → 只执行 Steps 1–8，然后 STOP 并报 BLOCKED-on-300**（附三条 grep 输出；不得自行给 `/choices` 或 `ModelChoice` 加字段）。

**Verify**：三条命令的输出留存在执行报告里；第 3 条的命中/缺失结论写明。

### Step 1：全局配置字段
`pkg/config/agents.go:13-16` 的 `AgentsSection` 加第三个同级字段：

```go
	// GoalEvaluator is the one system subagent the settings page configures:
	// the /goal round's check. It is global on purpose — every primary agent's
	// goal check runs on the same row — which is why it sits beside Defaults
	// and Definitions rather than inside them (plan 299).
	GoalEvaluator SubagentConfig `yaml:"goal_evaluator,omitempty" json:"goal_evaluator,omitempty"`
```

**Verify**：`go build -tags fts5 ./...` exit 0；`gofmt -l pkg` 无输出。

### Step 2：导出窄校验入口 + 单点挂载
1. `pkg/config/agents.go`，`validateSubagentModel` 之后加：

```go
// ValidateGoalEvaluatorModel puts the settings page's one global subagent row
// under the same rule every per-agent entry gets: validateSubagentModel,
// unmodified. The where string is the configuration path the settings page
// writes, so a refusal names the row the user was editing.
func ValidateGoalEvaluatorModel(entry SubagentConfig, refResolves func(string) bool) error {
	return validateSubagentModel("agents.goal_evaluator", entry, refResolves)
}
```

2. `pkg/config/load.go` 的 `validateLoadedRoot`（`:174`）里、紧邻 `:202` 的 `validatePrimaryAgents` 调用加：

```go
	if err := ValidateGoalEvaluatorModel(r.Agents.GoalEvaluator, func(ref string) bool {
		_, ok := ResolveModelRef(r, "main", ref)
		return ok
	}); err != nil {
		return err
	}
```

   **只挂这一处**（§3）：装载走 `Load`（`:53/:68`），settings 写入走 `settingsCandidateLoads`（`api_extra.go:9765-9771`）→ `FinishLoad`（`:80-98`）→ `validateLoadedRoot`。**不要**挂进 `validatePrimaryAgents`（`Definitions == nil` 提前 return，`:907-909`）；**不要**在 `api_extra.go` 加第二个调用点。

**Verify**：`go build -tags fts5 ./...` exit 0；`CGO_ENABLED=1 go test -tags fts5 ./pkg/config -count=1` 的 FAIL 集合与基线等价（§5.3 口径，无新增失败）。

### Step 3：网关 settings 注册表 + normalizer
1. `pkg/gateway/api_extra.go` 的 `settingsFieldList`（`:9188`）「子代理」注释块（`:9196-9199`）内加两条：

```go
	{path: "agents.goal_evaluator.model", section: "agents", kind: settingsText},
	{path: "agents.goal_evaluator.reasoning_effort", section: "agents", kind: settingsText},
```

2. 紧邻 `normalizeMemoryModelPath`（`:9737-9760`）加：

```go
const goalEvaluatorModelPath = "agents.goal_evaluator.model"

// normalizeGoalEvaluatorModelPath puts the goal check's model into the form the
// engine resolves globally: the id of a configured model row (plan 299). Empty
// is "follow the primary agent" and stays empty; a legacy "provider/model"
// label resolves to the row it names; anything else is refused here rather
// than by a goal check much later.
func normalizeGoalEvaluatorModelPath(cfg *config.Root, path string) error {
	if cfg == nil || path != goalEvaluatorModelPath {
		return nil
	}
	ref := strings.TrimSpace(cfg.Agents.GoalEvaluator.Model)
	if ref == "" {
		return nil
	}
	entry, ok := config.ResolveModelRef(cfg, "main", ref)
	if !ok {
		return fmt.Errorf("这个设置项要选一个已配置的模型")
	}
	cfg.Agents.GoalEvaluator.Model = strings.TrimSpace(entry.ID)
	return nil
}
```

3. PATCH 循环里紧跟 `normalizeMemoryModelPath` 的调用（`:9686-9689`）加同形调用与同形拒绝（`settingsPatchError{Path: path, Message: err.Error()}`）。

**Verify**：`go build -tags fts5 ./...` exit 0；`go vet -tags fts5 ./pkg/gateway` exit 0。

### Step 4：catalog 私有字段 + `Model()` 分支
`pkg/run/subagent_catalog.go`：
1. `SubagentCatalog` struct（`:37-52`）加私有字段：

```go
	// goalCheck is the settings page's global row for the /goal check (plan
	// 299). It is kept out of entries on purpose: entries is what ResolvePublic
	// and ToolPolicy read as "this agent configures that type", and the goal
	// check must stay undispatchable from a primary agent.
	goalCheck appcfg.SubagentConfig
```

2. `SubagentCatalogFor`（`:57-74`）在 `if cfg == nil { return catalog }` **之后立即**加 `catalog.goalCheck = cfg.Agents.GoalEvaluator`（不得放到 `!ok` 提前 return 之后，§4 坑 ①）。
3. `Model()`（`:162-179`）开头改为：

```go
	entry, found := c.entries[subagentKey(name)]
	if !found && subagentKey(name) == GoalCheckSubagentType {
		entry, found = c.goalCheck, true
	}
	if !found {
		return "", "", "", "", false
	}
```

   其下既有的 `configured == ""` 即 return false：未配置跟随主代理原样成立。**不得**把全局字段写进 `entries` / `customs`（§4 反例）。

**Verify**：`go build -tags fts5 ./...` exit 0。

### Step 5：两级语义守卫（只认 `GoalCheckSubagentType`）
`pkg/run/subagent.go:1978-1981`，第一个分支条件加类型收窄：

```go
	pin := SubagentModelOverride{}
	// The /goal check takes its model from the settings page alone (plan 299):
	// a dispatch-time pin is a caller's choice, and the check is not the
	// caller's. Every other type keeps the three-level chain untouched.
	if override, ok := SubagentModelOverrideFromContext(baseCtx); ok && agentType != GoalCheckSubagentType {
		pin = override
	} else if modelID, provider, model, effort, ok := cat.Model(agentType); ok {
```

**不得**写成「system 类型一律忽略 ctx pin」（§4 的 `plan-reviewer` 反例）。

**Verify**：`go build -tags fts5 ./...` exit 0；`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run TestPlanReviewSpawnEventNamesTheChosenModel -count=1` → PASS（断言不得放宽）。

### Step 6：Go 定向测试 1–7（§5.3）
按 §5.3 第 1–7 条写测试，命名建议：
1. `TestGoalCheckRunsOnTheConfiguredRow`（`pkg/run/goal_test.go`；把 297 的 `TestGoalCheckCanBePinnedToAConfiguredRow`（`:743`）**改写**为配置注入而非 ctx 注入；断言口径参考 `subagent_test.go:2267-2283`）。
2. `TestGoalCheckIgnoresADispatchTimeModelPin`（ctx 带另一个行 id 的 pin，仍走本页配置）。
3. 既有 `TestPlanReviewSpawnEventNamesTheChosenModel` 不动、保持 PASS。
4. `TestGoalCheckFollowsTheSessionModelWhenUnconfigured`。
5. `TestGoalCheckConfigAppliesToAgentsWithoutDefinitions`（主代理在 `Definitions` 无任何条目）。
6. `TestValidateGoalEvaluatorModel`（放在已测 `ValidateAgentSubagents` 的同一个 pkg/config 测试文件：`grep -rn "ValidateAgentSubagents" pkg/config/*_test.go` 定位；三条断言：有 effort 无 model 拒、解析不到的 ref 拒、合法 row id 放行）。
7. `TestSettingsPatchNormalizesGoalEvaluatorModelToRowID`（`pkg/gateway`；样板 `TestSettingsPatchKeepsMemoryModelsOnConfiguredRows`；含「写不存在的行被拒」与「`reasoning_effort` 非空而 `model` 空被拒」两条）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/config ./pkg/run ./pkg/gateway -count=1 -run 'TestGoalCheck|TestValidateGoalEvaluatorModel|TestSettingsPatch|TestPlanReviewSpawnEventNamesTheChosenModel'` → 上述用例全部 PASS、0 FAIL。

### Step 7：实时读测试 8 + 对照组（裁决 11 的实证闸门）
`pkg/run/goal_test.go` 加 `TestGoalCheckEffortFollowsTheRowDefaultUntilOverridden`，按 §5.3 第 8 条：
- 断言 A：`model` = 行 id、`reasoning_effort` = 空 ⇒ 请求带该行 `caps.default_effort`。
- 断言 B：把该行 `default_effort` 改档、配置重新采纳后再派发 ⇒ 带**新值**。
- 对照组：`reasoning_effort` = 具体档位 ⇒ 带该档位、不随行默认变化。
若断言 B 因客户端缓存（§6-⑪ 口径缺口）在「同一 runner 的下一轮」不成立：**不得加缓存失效、不得改 tray 文案**；把测试调整到实测成立的边界（如配置重新采纳 / runner 重建后），并在最终报告里如实写明「下一轮对话生效」的实测边界交 owner。

**Verify**：该测试 PASS；报告里记明断言 B 的实测生效边界。

### Step 8：Go 回归等价 + 字节稳定
1. `CGO_ENABLED=1 go test -tags fts5 ./pkg/config ./pkg/run ./pkg/gateway ./pkg/process -count=1` → FAIL 集合与基线等价（基线按 §5.3 配方就地重生成；`libtokenizers.a` 找不到即 §10 STOP）。
2. `go vet -tags fts5 ./...` exit 0；`gofmt -l pkg cmd` 无输出；`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → ok。
3. `grep -rn 'Optional id of a model from list_usable_models' pkg | wc -l` → `3`（字节稳定，前缀缓存）。

**Verify**：三项如上；新增失败 0 条。

### Step 9：300 闸门复核 + choices 透传（前端阶段入口）
1. 重跑 Step 0 第 3 条的三条 grep。**任一缺失 → STOP，报 BLOCKED-on-300**（Go 侧 Steps 1–8 的成果保留在报告里）。
2. 命中则检查 `frontend/src/lib/api.ts` 的 `LlmModelChoice`（`:1465-1476`）与 `llmModelChoices()` 的读取映射是否含 `defaultEffort` / 原始 `default_effort`；缺则**只加字段与映射**（不改 `useModelChoices.ts` 的缓存语义：页级一份、失败不缓存）。

**Verify**：`cd frontend && pnpm typecheck` exit 0；`grep -n "defaultEffort" frontend/src/lib/api.ts` 命中。

### Step 10：locales（修过时文案 + 新 key）
1. `frontend/src/locales/index.ts:709`（zh）与其 en 对应行：把子句「保存在 forebrain.yaml，新会话生效。」/ en 对应句替换为「修改已保存，新会话生效。」/ 同义英文；**同段「系统子代理……只能查看」一个字节不动**（§6 第 6 项）。
2. 中英两份各加 5 条新 key（命名可微调，语义不得变）：`subagents.goalCheck.title`＝「/goal 检查模型」；`subagents.goalCheck.effortFollowRow`＝「跟随模型设置」；`subagents.goalCheck.effortReset`＝「恢复为模型默认」；`subagents.goalCheck.hintRowDefault`＝「模型页配置的默认强度：{effort}」；`subagents.goalCheck.hintOverridden`＝「已改为 {effort}，仅影响目标检查子代理」。placeholder 复用既有 `subagents.followPrimary`（`:727`）；**不得**复用带插值的 `:755`。

**Verify**：`grep -rn "保存在 forebrain.yaml" frontend/src` → 0 命中；`cd frontend && pnpm typecheck` exit 0。

### Step 11：clearable 模型下拉组件（自封装，路线 ①）
新建 `frontend/src/components/settings/GoalCheckModelSelect.vue`，1:1 实现 §2「模型选择器」节：32px combo、收起态只回显模型名 + 服务商、`.ss-caret-zone` 的 hover 箭头↔x 互换（自行开发，组件库无内置）、菜单右对齐含搜索框与两行签名候选（第二行 endpoint + 高亮 API 路径）、选中行打勾染蓝、**无「跟随主代理」候选项**、未选时 placeholder 走 `subagents.followPrimary`。选项来自 `useModelChoices()`（mount 时 `ensureChoices()`）；props `modelValue: string`（row id）、emits `update:modelValue`；点 x 时 emits 空串（清空语义由父组件同步 effort，见 Step 12）。

**Verify**：`cd frontend && pnpm typecheck` exit 0。

### Step 12：分组组件 + Tab 接线 + schema 注释
1. 新建 `frontend/src/components/settings/GoalCheckModelGroup.vue`：`data-group="goal-check-model"`；分组头 h3 只有标题、**标题下无说明**；两条字段行（左标题右控件，控件铺满右列、与页面其它 select 同宽）；思考强度 select 的选项 = 选中行的 `efforts` + 首项「跟随模型设置」（`value=""`，**仅已选模型时出现**）；未选模型时只剩 placeholder「跟随主代理」且 `disabled` + `unset` 灰态；hint 与「恢复为模型默认」链接按 §2 两条文案；组底 tray 保存条仅 dirty / 刚保存时展开。
   **状态与落库映射（裁决 11，执行者不得自行发明）**：draft 里存**落库形态** —— 选中模型行时 effort 写 `''`，界面选中值显示为 `draft || 该行 defaultEffort`；用户改档位时，等于该行 `defaultEffort` 写 `''`、不等于写具体档位；保存即把 draft 原样发出。**不得把界面回显值直接写库**。
   读写复用页面传入的 `form`（`useSettingsForm` 的返回值）：初值读 `form.saved` / `form.draft` 的两条 path，改动走 `form.setValue`，保存走 `form.saveGroup(group)`、放弃走 `form.cancelGroup(group)`，`group` 是本地构造的分组描述符（两条 field，形状照 `frontend/src/lib/settingsSchema.ts` 的 `SettingsGroup` / `SettingsField` 接口**只读**补齐所需属性）。
2. `frontend/src/components/settings/SettingsSubagentsTab.vue`：在 `<SettingsTabForm :tab="tab" :form="form" />` **之后**渲染 `<GoalCheckModelGroup :form="form" />`。
3. `frontend/src/lib/settingsSchema.ts` 顶部注释加一行例外说明：goal-check 分组按 plan 299 路线 A 手写于 FORMS 之外，**不得**加进 `SETTINGS_FORMS`。**字段表一个字节不动**。

**Verify**：`cd frontend && pnpm typecheck` exit 0；`cd frontend && pnpm exec vitest run src/lib/settingsSchema.test.ts` → **9/9 全绿**（变红即 §10 STOP）。

### Step 13：新组件 vitest
新建 `frontend/src/components/settings/__tests__/GoalCheckModelSelect.spec.ts`，逐条钉 §5.2 的交互清单（初始未配置态 → 开菜单 / 搜索过滤 / 点选回填两行签名并打勾 → 选中后强度自动回显该行 `default_effort` → 切行换档位与选中值 → 改强度出 hint 与「恢复为模型默认」→ 点它回 `default_effort` → hover 变 x、点 x 同步清空并收起菜单 → 保存 / 放弃 / 重开回显），加**同名两行各测一次**（同 `provider/model` 不同 `id`：落库为各自 row id、回显可区分）与 **`efforts` 为空的合法态**（强度下拉只剩首项、不报错）。

**Verify**：`cd frontend && pnpm exec vitest run src/components/settings/__tests__/GoalCheckModelSelect.spec.ts` → 全 PASS。

### Step 14：e2e
新建 `frontend/e2e/settings-goal-check-model.spec.ts`（样板：295 的两个 approval spec）：设置 →「常规 / 子代理」Tab 看到 `data-group="goal-check-model"` 分组、排在 `data-group="sa"` / `"peers"` 之后；选模型行 + 强度 → 保存 → 刷新回显一致。

**Verify**：仓库根 `FOREBRAIN_E2E_SPEC="settings-goal-check-model" scripts/acceptance/web_e2e.sh` exit 0。

### Step 15：收口总验证
1. `cd frontend && pnpm typecheck && pnpm build` exit 0。
2. `cd frontend && pnpm exec vitest run src/lib/settingsSchema.test.ts src/components/settings/__tests__/GoalCheckModelSelect.spec.ts` 全 PASS。
3. 重跑 Step 8 的三条命令，结论不变。
4. `git status --porcelain` 只含 Scope 内文件与本计划预期前置的既有脏文件；`git log -1 --format=%h` 与 Step 0 记录的 SHA 相同（零 commit）。

**Verify**：四项如上；任一不符 → 先修再报 DONE。

## 9. Done criteria（机器可判定，ALL 必须成立）

- [ ] Step 0–15 的每条 Verify 均按预期通过（含 Step 7 报告里记明的断言 B 生效边界）。
- [ ] `go build -tags fts5 ./...`、`go vet -tags fts5 ./...` exit 0；`gofmt -l pkg cmd` 无输出；`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` ok。
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/config ./pkg/run ./pkg/gateway ./pkg/process -count=1` 的 FAIL 集合与基线等价，**无新增失败**；§5.3 的 8 条定向测试全部存在且 PASS；`TestPlanReviewSpawnEventNamesTheChosenModel` PASS 且断言未放宽。
- [ ] `grep -rn 'Optional id of a model from list_usable_models' pkg | wc -l` → `3`。
- [ ] `cd frontend && pnpm typecheck`、`pnpm build` exit 0；`pnpm exec vitest run src/lib/settingsSchema.test.ts` 9/9；新组件 spec 全 PASS；e2e spec 通过。
- [ ] `grep -rn "保存在 forebrain.yaml" frontend/src` → 0 命中。
- [ ] 两处 `"is dispatched by the system and cannot be configured"`（`pkg/config/agents.go:268`、`pkg/gateway/subagents_api.go:848-849`）字节未变；`list_usable_models` 工具定义与三种拒绝文案字节未变。
- [ ] §5.2「不得改动的文件」清单零改动：`settingsSchema.ts` 字段表、`settingsSchema.test.ts`、`SettingsTabForm.vue`、`SettingsField.vue`、`ai-elements-vue/packages/shadcn-vue/**`、原型 HTML、侧栏子代理页及其组件。
- [ ] `git status --porcelain` 无 Scope 外文件被本计划修改；执行者零 commit（`git log -1` 与 Step 0 同 SHA）；`plans/README.md` 未被执行者改动。

## 10. STOP 条件（命中即停并报告，不得 improvis）

- Step 0 的 drift 比对与摘录对不上（代码库已漂移）。
- Step 0 / Step 9 的 300 三条 grep 任一缺失 → 报 **BLOCKED-on-300**（Steps 1–8 成果保留）；不得自行给 `/choices` 或 `turn.ModelChoice` 加字段。
- `settingsSchema.test.ts` 任何时刻变红 → STOP：**不改测试、不改原型 `FORMS`、不塞 `SETTINGS_FORMS`**，回来报告。
- 需要触碰 Scope / §5.2 不得改动清单之外的文件才能继续（含全局组件库、侧栏子代理页、原型 HTML、两处 `cannot be configured`、`list_usable_models` 文案）。
- 回归 FAIL 集合相对基线**新增**失败 → STOP：不修无关测试、不放宽任何断言、不把基线红写成通过。
- 基线重生成时 `libtokenizers.a` 找不到（§5.3）→ STOP 报告，不得跳过链接改判据。
- 同一步的 Verify 连续两次合理修复后仍失败 → STOP。
- Step 7 断言 B 的实测边界与「下一轮对话生效」不符：**不是 STOP**，按 Step 7 写明边界如实报告；但**不得**改 tray 文案、不得加缓存失效逻辑。
