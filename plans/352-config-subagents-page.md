# /config › 主代理 Subagents 页：不能新建/编辑子代理（只能配 key/value）＋ 6 个内置类型不可见

> v4：吸收五轮独立冷读评审（其中两轮带 worktree 复现）、48 转达的两轮 owner 裁决，以及 48 的创建表单整批已合并进 dev（`9b5c4891`）这一新基线。执行者零上下文可读，每条事实带 file:line 与当场读到的源码/真机输出。
> **成品计划**（评审方 2026-11-22 落盘；本目录即 owner-local 档案）：实施走 deepseek-flash@max 执行器 + `.worktrees/config-subagents-page`，评审 APPROVE（一轮 REVISE）后按 owner 指令 **ff-only 合并进 dev `6cd9140c..6541b8ab`**（未 push），并随合并冻结原型 `a6bc85c1…`/`72140edc…`（owner 2026-11-22 确认）。

## 0. 元信息、基线、依赖

- **Planned at**：HEAD `9b5c4891`（2026-11-22 21:28 `refactor(config): drop the create form's unused name-check parameter`）。**树基线与原型哈希都要在 48 收工后重取**：实测此刻 `pkg/tui/overlays.go`、`pkg/tui/render.go` 是 48 正在改的 dirty 文件，且它正在把底栏文案改小写、会一起改到 `plans/config-tui-prototype.html`——所以下面记的 `eaa03b8b…`/`033ea112…` 只是**当前中间值**，Step 0 以那时的实际值为准；**别人文件从 dirty 集合消失不算漂移**，只判「当前 dirty 里新增的路径是否都在 §6」。这个 HEAD 已含两个前置批次：早前其他会话的 /config 面板改动（模型 picker filter 框、`configModelRowLabel` 去 host、`pkg/tui/config_panel_test.go` 里随之更新的用例——Step 4 要大改这个文件，别把别人刚钉的断言当旧钉删掉），以及 **48 的 agent 创建表单整批**（`plans/351-config-agent-create-form-first.md`）。**实施期间不要 revert/stash/commit 别人的改动**。
  - gitignored owner-local：`plans/config-tui-prototype.html` sha256 `eaa03b8b921a987b04815cc2f7517ae2840539e85430fc0314b67efcf6c3630d`（295 行；**48 那批已把 agent 半边同步进去**：`saveCreateForm`、`s` 键、footer 文案、`discard` 视图、`new-primary-detail`/`create-held` 场景、agent Name 帮助句）、`plans/config-tui-prototype.test.cjs` `033ea1121e3918a4d7d465aa1692aed4352151ec876822cd469c6980c5661eb6`。
- **漂移检查（内容比对；行号漂移不算）**
  ```bash
  git rev-parse HEAD                                    # 9b5c4891…（或 48 之后的任何 HEAD，见 §9 的漂移规则）
  shasum -a 256 plans/config-tui-prototype.html plans/config-tui-prototype.test.cjs   # eaa03b8b… / 033ea112…
  grep -c "case is(4, configDefinitionsPath, \"\", configSubagentsKey)" pkg/tui/config_panel.go   # 1
  grep -c "func (p \*configPanel) addRow" pkg/tui/config_panel.go                                # 1
  grep -c "func (p \*configPanel) editTemplatePage" pkg/tui/config_panel.go                      # 1
  grep -c "is dispatched by the system and cannot be configured" pkg/config/agents.go            # 现在 1；本计划 A1 之后 0
  grep -c "func (c SubagentCatalog) Model" pkg/run/subagent_catalog.go                           # 1
  ```
- **前置批次已在 HEAD（不再等 48；Step 0 只核对形状，不符才 STOP）**
  - **文案注意**：本计划下文出现的 `s Save`/`a Add`/`d Remove` 都是**对现状的引述**。owner 2026-11-22 已把底栏快捷键说明统一成小写短语（`s to save`/`a to add`/`d to remove`，「同类问题必须一并修复」），48 正在改。实施时：**键位不变（`s`/`a`/`d`），显示文案一律取 48 收工后的最终字符串/常量**，不要钉字面量。
  1. **创建表单机制（48，已合并）**
     - `configPanel.staged bool`（`assign` 与 `assignModel` 两处 `p.save` 之前早退）；
     - `pkg/tui/overlays.go` 的 `FooterActionSave`（`fireAction` 里 `kind == rawKeyRune && p.noFilter && a.Key != "" && string(r) == a.Key`）；
     - 常量：`configCreateSaveKey="s"`、`configCreateSaveLabel="Save"`、`configCreateSaveDesc="Writes the new definition"`、`configDiscardDraftLabel="Discard"`、`configKeepEditingLabel="Keep editing"`、`configCreateFormKeys = &configPageKeys{Enter:"open", Esc:"cancel"}`；
     - `templateItems(template, key string, settings map[string]any, tailRows, creating bool)`（5 参；本计划**不再给它加参数**，`doc/rowPath` 的扩展已撤销——内置页过滤只按名字）；
     - 可复用但**看清适用面**：`configCreateSave*`/`configCreateFormKeys`、尾部提交行模式（`saveRow := len(rows)`）、「有内容时 Discard / Keep editing（Keep 预选）」；**不要复用** `agentNameRefusal`（definition id 规则）与 `stageAgentDraft`/`discardAgentDraft`（挂在 `definitions[""]` 上——子代理草稿在列表里）。
     - 48 **不碰** `editTemplatePage`：创建表单在各自循环里渲染 → 子代理创建页**必须自建循环**（§4 B3）。
  2. **名字规则（48，已合并；owner 裁决 9）**：`pkg/config/load.go` 的 `validateDefinitionRoles` 已删掉子代理专属 id 检查（定义 id 两种角色同一句 `agents.definitions.%s: invalid agent id`）；`ValidateAgentSubagents` 的 name pattern 检查现在写作 `if !sharedReference && !subagentConfigNamePattern.MatchString(name)`（**共享定义引用条目豁免**）；**自定义条目仍 dashes-only**（`^[a-z][a-z0-9-]{1,62}$`）与原文案。
     - 对本计划的影响：① A1 要**叠在 48 已改好的 `ValidateAgentSubagents` 之上**（先读函数当前内容）；② 本页 shared 行名字可能含 `_`，行名/重名比较用原样字符串；③ 我创建的**自定义**条目仍用 dashes-only 与原型那句文案；④ `configKeyNameHint`（「Lowercase letters, numbers and hyphens. This is the configuration key.」）在 48 落地后**已无任何使用点**（agent 模板的帮助句落在 `config_panel_schema.go`）。按 owner 2026-11-22「死常量必须删掉」：**本计划顺手删掉它**；裁决 10 的 name 行 hint 用原型那句 `Lowercase letters, numbers and hyphens.`（不是复用它）。
  - `editTemplatePage`：**保持它的页调用（`p.page`）与尾行行为**，只在 B2 里删掉 system 只读那一段——按标识符删 `if configTemplateRowIsSystem(template, settings) { notice = configSystemSubagentNotice; continue }`（**行号 `config_panel.go:2353-2358` 之类在 48 合并后全部失效，照号删会删掉 `p.templateItems(...)`/`p.page(...)` 两行渲染调用**）。

## 1. 目标

owner 报告（2026-11-22 截图）：`/config` › Agents & tools › Agent definitions › main › Subagents 页**无法新建、无法编辑子代理**，按 `a` 只能配 key/value，与冻结原型 `plans/config-tui-prototype.html` 不一致，要求「必须补齐缺口」。追加裁决把范围定死（§3）：列全 6 个内置类型且只能覆盖 model + thinking level、列表只显示名字、字段页禁 `Agent` 状态、创建表单整表 + `s Save` 一次落库。

## 2. 复现与根因

### 2.1 真机复现（隔离 home；不污染真实 `~/.forebrain*`）

```bash
export FOREBRAIN_RUN_WORK=$TMPDIR/fb-cfg FOREBRAIN_RUN_SESSION=fb-cfg FOREBRAIN_RUN_PORT=8762
D=.claude/skills/run-forebrain/driver.sh
$D build && $D reset && $D start reply 'ok'
$D submit '/config'; $D key Right Right; $D key Enter; $D key Enter; $D key Enter
$D key Down Down Down Down Down Down Down Down; $D key Enter      # → main › Subagents
```
观察（逐屏 capture）：

1. 空态只有 `Nothing configured yet` / `Add the first entry.`（与 owner 截图一致），footer `a Add`。
2. 按 `a` 弹出通用取值类型菜单 `Text/Number/Boolean/Object/List`（不是子代理表单）。
3. 选 `Object` 兜底，写库被 loader 拒（页面下一行原样）：
   `config section agents: agents.definitions.main.subagents[3]: invalid name "": use lower-case letters, digits and dashes, starting with a letter`
   → 「从该页新建子代理」真机无可用路径。
4. 预置夹具（`subagents=[{name:explore,model:…},{name:db-migrator,…},{name:repo-auditor,enabled:true}]` + `definitions.repo-auditor{primary:false,…}`）后：列表只有 3 行 `#1 / #2 / repo-auditor`；另外 5 个内置类型无行；行名是 `#N`。
5. 打开 `#1`（explore override）= 3 行 `Type name / Model / Thinking level`；原型是 2 行（Model、Thinking level）。

### 2.2 源码链（逐条当场读过）

| # | 事实 | 位置 |
|---|---|---|
| — | **下表行号是 48 合并前的快照**；一律按符号名定位（`grep -n "<符号>"`），内容不符才是问题 | — |
| 1 | `addRow` 对 list 元素没有 typed create：只有 hooks / 顶层 definitions / scalar-template 三个分支，其余落 `chooseNewValue` | `config_panel.go:2767-2875`（hooks 2769、primary 2773、scalar 2814、兜底 2842）；`chooseNewValue` 3343-3400 |
| 2 | list 新元素**先落库**（`p.assign` 2850-2874），所以缺 `system_prompt` 的自定义子代理必被拒 | `pkg/config/agents.go:502-505` |
| 3 | 条目名规则 `^[a-z][a-z0-9-]{1,62}$`；system 名整条拒 | `pkg/config/agents.go:351-354`、`:396-408` |
| 4 | 该页合成行只补 shared 定义（无内置） | `config_panel.go:1503-1508`、`:1949-1966` |
| 5 | 行名只有 shared 行按名字 | `config_panel.go:1664-1675` |
| 6 | 该页行带 `agent override:` / `global default:` / `Follow conversation` 与 Shared 状态 | `config_panel.go:1974-2016`、`:1630-1652` |
| 7 | public 内置页**已有** `CustomOnly`（2440-2442）与 `HideOnBuiltin`（2445-2447）过滤，只剩 3 行；**唯一没被过滤的是 `name` 行**。`HideOnBuiltin` 只判 `PublicSubagentNames`，system 名会露 `system_prompt`/`enabled` | `config_panel.go:2429-2485`；`config_panel_schema.go:500-523`（`name` 501、`system_prompt` 512、`enabled` 522） |
| 8 | `configTemplateRowIsSystem` 共 4 处用途：`editTemplatePage` 里的 `if configTemplateRowIsSystem(...) { notice = configSystemSubagentNotice; continue }`、`templateNotice` 里的 `joinNotice(notice, configSystemSubagentNotice)`、`templateItems` 里的 `system := configTemplateRowIsSystem(...)` → `status = configSystemStatus`、以及 `locked := … && !configTemplateRowIsSystem(...)`；另有 `configRowColumns` 返回 `configSystemSubagentNotice` 的一支 | **按标识符定位**（`grep -n "configTemplateRowIsSystem\|configSystemStatus\|configSystemSubagentNotice" pkg/tui/config_panel.go`）；48 合并后行号已全部失效 |
| 9 | loader 硬拒 agent 层 system 条目 | `pkg/config/agents.go:406-408` |
| 10 | 分派侧：非 public 条目一律进 `customs` → 会变成模型可派发的类型 | `pkg/run/subagent_catalog.go:87-100`、`:140-157`、`:162-184`、`:239-243` |
| 11 | `Model(name)` 先读 `entries`，再回落 goal-evaluator 全局行；回落条件是 `!found`；`ResolveModelRef` 失败即 `return ok=false` | `pkg/run/subagent_catalog.go:215-235` |
| 12 | plan-reviewer 有两条链路：审批卡 override 优先，否则 `cat.Model`；审批卡默认只读 `cfg.Agents.PlanReviewer` 且 agentName 硬编码 `"main"` | `pkg/run/subagent.go:2094-2101`；`pkg/turn/approval.go:1306-1360`、`:1362-1376`（`"main"` 在 1371） |
| 13 | **全局 plan-reviewer 的 thinking level 是死设置**：`cfg.Agents.PlanReviewer.ReasoningEffort` 在非测试代码里没有任何消费者（只有 `.Model` 被 `llm_models.go:226`、`approval.go:1371` 读；`subagents_api.go:307` 只做展示） | 同上 |
| 14 | `assign` 失败 → `return p.reloadSection(), err`（**把表单换成库里的真相**）；`p.save` 失败**不动** `p.raw` | `config_panel.go:1411-1421`、`:765-779` |
| 15 | `configAssign` 原地改容器；`[]any` 只认数字下标，非数字立刻报错（评审在 worktree 复现：`explore is not a list index`） | `config_panel.go:3501-3550`（查 3447-3452、写 3535-3541） |
| 16 | 占位行最危险的脏写：模型 picker 首项是空选项，`assignModel` 在 `picked==""` 时走 remove，但**随后无条件 `p.save`** | `config_panel.go:1291-1292`、`:1364-1373` |
| 17 | `configRoleHelp` 对 subagent 条目返回**主代理**的句子（条目没有 `primary` 键 → `configAgentRolePrimary` 为 true）；原型 edit 视图用同一个 `primary===false` 条件 | `config_panel.go:1095-1103`、`:2403-2410`；原型 `html:181` |
| 18 | `editTemplatePage` 走 `p.page(...)`（无 footer 动作），且被 5 处共用（`:2114`、`:2871`、`:663`、`:2954`、`:2981`）；尾行 `Up` 由 `templateItems(..., tailRows)` 产出（2498-2501） | `config_panel.go` 同行号 |
| 19 | subagent 模板的 `name` 行 Label 是 `Type name`、Hint 是过期的「Built-in overrides: general-purpose, explore, plan, verification.」（内置类型现在是本页的行，且创建内置名被拒） | `config_panel_schema.go:501` |

## 3. Owner 裁决（冲突时以它为准）

1. 该页**列全 6 个内置类型**（`agent.PublicSubagentNames` + `agent.SystemSubagentNames`），public + system **都只能覆盖 model 与 thinking level**，「其他不能覆盖的配置项都不显示」。
2. **列表只显示名字**，模型信息只在打开后的字段页。
3. **字段页不要 `Agent`/`Global` 状态标记**（owner 截图红框：「这里显示的agent无任何意义和价值，代码同步改」）。
4. 为了让 1 可用，**本计划一起打通** system 类型的 per-agent pin（pkg/config 放行 + pkg/run 隔离 + pkg/turn 审批卡默认）。
5. 创建表单统一约定（主代理与子代理同一约定）：按 `a` 显示完整表单；字段只持有、不写库；底栏 `s Save` 一次落库；Esc 零写入；被拒保留表单与已填值。
6. 提交键位/文案：键位是 `s`（`configCreateSaveKey`）；**显示文案以 48 收工后的最终值为准**——owner 2026-11-22 的新指令是把底栏快捷键说明统一成小写短语（`s Save` → `s to save`，`a Add` → `a to add`，`d Remove` → `d to remove`，「同类问题必须一并修复」）。本计划画的子代理页（行页 footer 与创建表单 footer）也用同一套最终文案；**不要自己钉字面量**，一律走常量/48 的最终字符串。
7. **shared 行的 `Shared` 状态与「…(shared)」摘要保留**（plan 314 语义），`d Remove` 对 shared 行仍不出现；内置行与已存自定义行不显示任何 desc/value/status。
8. **自定义子代理的 System prompt 帮助语修成子代理那句**（`Replaces the built-in system prompt whole.`），TUI 与原型一起改（原型重算冻结哈希并交 owner 确认）。
9. （48 转达）子代理**角色**的定义 id 规则与主代理一致；共享定义可被引用；**自定义**子代理条目规则不变。
10. **subagent 模板的 name 行三处统一改成原型的 `Name`**（Label）+ `Lowercase letters, numbers and hyphens.`（Hint）：创建表单、已存自定义条目页、内置行页（后者隐藏该行）共用同一个描述符。
11. **全局 plan-reviewer 的 Thinking level（`agents.plan_reviewer.reasoning_effort`，今天没人读）放进本计划**，与 agent pin 走同一条回落链。
12. **死代码、死常量、死配置必须删掉，清理干净**（owner 2026-11-22，原话）。本计划删掉的路径留下的任何常量/函数/字段/测试替身/原型 helper，都必须在同一次改动里删干净，不留「以后可能用到」的东西。

## 4. 修复设计

### Part A — runtime/config

**A1. `pkg/config/agents.go` `ValidateAgentSubagents`（叠在 48 的版本上）**
把 system 名字并入「内置类型只收 model+effort」分支：
```go
_, publicBuiltin := public[name]
_, systemBuiltin := system[name]
switch {
// 6 个内置类型（4 public + 2 system）都只收一个 model 引用与一个思考强度：
// system 类型由系统自己起跑，agent 层能配的只有它跑哪个模型（owner 2026-11-22）。
case publicBuiltin, systemBuiltin:
    if …Description/SystemPrompt/Tools/AllowedTools/Continuable/Enabled 任一非空… {
        return fmt.Errorf("%s: %q is a built-in: it takes a model and a reasoning effort, nothing else.", where, name)
    }
```
保留 `seen` 去重、48 的「名字检查移到 shared 分类之后」、`validateSubagentModel`。不碰 `IsReservedDefinitionID`/`builtInSubagentDefinitionNames`、`stripRetiredBuiltinEntryFields`（731-753）、`SubagentEntryCarriesBody`（441-449）。

**A2. `pkg/run/subagent_catalog.go`：system pin 只当 pin，且「解析到才用」**
- 新字段 `systemPins map[string]appcfg.SubagentConfig`（注释：系统自己起跑，pin 只选模型，永不让它变成可派发类型）。
- `SubagentCatalogFor` 初始化；循环（87-100）在 `entries` 之前分流：system 名 → `systemPins`，`continue`。
- `Model`（215-235）**先解析、失败即继续回落**（否则悬空 pin 会挡住 goal-evaluator 的全局行，现有回落条件 `!found`）：
  ```go
  entry, found := c.entries[subagentKey(name)]
  if !found {
      if pin, ok := c.systemPins[subagentKey(name)]; ok && strings.TrimSpace(pin.Model) != "" {
          if resolved, ok := appcfg.ResolveModelRef(c.cfg, c.agentName, strings.TrimSpace(pin.Model)); ok {
              return strings.TrimSpace(resolved.ID), strings.TrimSpace(resolved.Provider), strings.TrimSpace(resolved.Model), strings.TrimSpace(pin.ReasoningEffort), true
          }
      }
      if subagentKey(name) == GoalEvaluatorSubagentType {
          entry, found = c.goalEvaluator, true
      }
  }
  ```
  判据：**悬空/无 model 的 system pin 不得改变现有回落行为**。
- `Public`/`ResolvePublic`/`ToolPolicy`/`Prompt`/`customs` **一律不改**。

**A3. `pkg/turn/approval.go`：plan-reviewer 的 agent pin 成为审批卡默认；全局强度不再是死设置**
- `preferredPlanReviewerRow(cfg *appcfg.Root, agentName string) (rowID, effort string)`：
  1. 该 agent `subagents` 里 name == `plan-reviewer` 的 `model`（`ResolveModelRef(cfg, agentName, …)`，1371 的 `"main"` 换实参）+ 其 `reasoning_effort`；
  2. 解析不到 → 回落 `cfg.Agents.PlanReviewer`，**并把它的 `ReasoningEffort` 一起返回**（今天这个设置没人读，见 §2.2 #13）；
  3. 都空 → `""`（plan 315 悬空=未配）。
- `PlanReviewModelOptions`（1306-1360）：`Preferred`（1348）不变；`DefaultEffort`（1356）在 **`preferred` 命中的那一行**带 effort 时取该 effort，否则保持该行 `ConfiguredEffort`（这样「agent pin 的强度」与「全局行的强度」两条都生效）。**签名不变**（`agentName` 早已是形参；调用方 `pkg/tui/chat_surface.go:611-615`、`pkg/gateway/approval.go:53-58` 都传真实 agent 名）。

**A4. 两条链路**：plan-reviewer run 是「审批卡 override 优先 → 否则 `cat.Model`」（`pkg/run/subagent.go:2094-2101`）。A2 落地后，无卡片（headless/gateway）时 pin 也生效；注释写清这是同一条回落链。

### Part B — TUI

**B1. 列表行**
- `editRows`（1503-1508）：该页 `keys` = 已存条目（顺序不变）＋ 缺失的 6 个内置名 ＋ 缺失的 shared 名。
  - 6 个名字**只一处来源**：把 `editBuiltinSubagents` 内联的拼接（`config_panel.go:619-622`）抽成 `subagentTypeNames()`，两处共用。
- 占位值：内置 → `{name}`；shared → 现有 `{name, enabled:false}`（1601-1603 不动；shared 仍按名字寻址，因为它的写入走 `setSharedReference` 的按名路径，见 2613-2630）。
- 行名（1664-1675）：该页任何行按条目名，无名条目 `#N`。
- 列（**按标识符定位，行号已失效；删除范围别写宽**）：在 `configRowColumns` 里**保留** `if shared := sharedRowName(doc, key, value); shared != "" { return configSharedRowSummary(doc, shared), "" }` 那一支；只删 `name := …` 之后的三段（`slices.Contains(agent.SystemSubagentNames, name)` 支、`!slices.Contains(agent.PublicSubagentNames, name)` 守卫、以及 `agent override:`/`global default:`/`configFollowConversation` 的尾巴），该页路径直接 `return "", ""`。`configRowStatus` 里 shared 的 `Shared` 支同样**保留**，只去掉非 shared 行的状态。
- `configRowIsEntry`（1577-1583）：加「key 在文档里查得到」，**保留 shared 排除**：
  ```go
  value, exists := configLookupValue(doc, configJoinPath(path, key))
  if agentSubagentsPath(path) && (sharedRowName(doc, key, value) != "" || !exists) { return false }
  return true
  ```
- **subagent 模板的 name 行**（`config_panel_schema.go:501`）：Label `Type name` → `Name`，Hint 改成原型那句 `Lowercase letters, numbers and hyphens.`（现有 Hint 已过期，见 §2.2 #19）。创建页与已存页共用同一个描述符。

**B2. 打开一行（`editRow` 2105-2123）**
- shared → 现有 `editSharedSubagentRow`（2577-2611，不改）。
- **内置（public + system）→ 新 `editBuiltinSubagentRow`**：
  - **寻址**：该页 key 对占位行是**名字**，而 `configAssign`/`configLookupValue` 在 `[]any` 上只认数字下标 → 本函数自己算 `rowPath`：`slices.IndexFunc(rows, …)` 找 index（`pkg/tui` 无现成 helper，别照抄 `pkg/gateway` 的同名函数）；不存在则 `idx = len(rows)`（草稿追加后的位置），`rowPath = configJoinPath(path, strconv.Itoa(idx))`。**名字只作行标签**；测试必须断言该路径是数字。
  - 页面：在 section 的**深拷贝草稿**上追加 `{name}` 后渲染（`configMarshalDoc` + `json.Unmarshal`，无新 helper），只有 `Model` + `Thinking level`，无状态（裁决 3）；值为 pin 的行标签与强度，未 pin 留空。
  - 收尾（机械判据）：`base` = 入参 `doc`；进页面前记 `before := string(p.raw[p.section])`；返回后 `after := string(p.raw[p.section])`：`after != before` → `return p.sectionDoc(p.section)`（store 真相，`p.raw` 只在 save 成功后更新）；否则 `return base`（绝不把占位条目挂在调用方 doc 上）。
  - **空 pick 守卫（拦截点＝`editModel`，不新增描述符字段）**：`editBuiltinSubagentRow` 看不到 `picked`；内置页的 descriptor 由 `templateItems` 内部从全局 `configTemplates` 取（`config_panel.go:2430`、`:2484`），调用方拿不到那一份，加 `configField` 标记也无处落位。因此在 `editModel` 里按「路径存在性」判断：picker 返回后若 `picked == "" && model 键在 doc 里不存在` → 直接 `return doc, ""`（占位行本来就无物可删，正是要拦的脏写；已存 pin 的键存在 → 照旧走 remove + `p.save`，见 1364-1373）。`editModel` 不在 48 的落点上（别改 `assignModel`，那是 `staged` 的落点）。
  - 清空已有 pin → 既有 `configDropSiblingEffort`（1376-1389）连带删 effort。
  - 字段过滤：`name` 行对内置名隐藏；`HideOnBuiltin`（`system_prompt`/`enabled`）的判定从 `agent.PublicSubagentNames` 改为「6 个内置之一」。**不加 `templateItems` 形参**。
  - **删除 system 只读语义**（4 处，见 §2.2 #8；**全部按标识符定位，不要用行号——48 合并后行号已失效**）：删 `func configTemplateRowIsSystem` 本身；删 `editTemplatePage` 里的 `if configTemplateRowIsSystem(template, settings) { notice = configSystemSubagentNotice; continue }`；删 `templateNotice` 里的 `joinNotice(notice, configSystemSubagentNotice)`；删 `templateItems` 里的 `system := …`/`status = configSystemStatus` 与 `locked` 里的 `!configTemplateRowIsSystem(...)`；删 `configRowColumns` 返回 `configSystemSubagentNotice` 的那一支；最后删常量 `configSystemStatus`/`configSystemSubagentNotice`，`grep -rn` 保证零引用后再删依赖用例。
- 已存自定义条目 → 现有 `editTemplatePage`（全字段；**它的 `p.page` 与尾行行为不变**）。

**B3. 创建（`addRow` list 分支 + 新 `addSubagentEntry`，自建循环）**
- 插入点：`addRow` 的 list 分支算完 `childPath`（`if list { … childPath = configJoinPath(path, strconv.Itoa(len(rows))) … }` 那段）之后、调用 `p.chooseNewValue(...)` 之前。**按标识符定位，行号已失效。**
- **两个变量分开，别混**（`addAgentDefinition:2937-2975` 同款）：`sectionDraft` = 深拷贝的 section 文档（页面渲染与写入的对象）；`entry` = 追加进该列表的那条草稿条目。
- **名字不放进条目**：`createName string` 单独持有，只在提交时写进 `entry["name"]`。这是必需而非风格：`templateItems` 的 `CustomOnly`（2439-2444）、`HideOnBuiltin`（2445-2447）以及本计划的「内置名隐藏 name 行」都按 `settings["name"]` 过滤 —— 名字一旦落进条目，用户把名字敲成 `explore` 时 Name/System prompt/description/tools 会当场消失，整表没得填。
- 循环形状（照 `addAgentDefinition`，但**不碰**它）：
  1. `sectionDraft` 里该列表追加 `{}`（**不种空串键**：`editText`/`editAgentDraftField` 的既有约定是空值删键，种 `{"name":"","system_prompt":""}` 会把这些噪声写进配置）；`p.staged = true`；**`defer` 保证任何 return 都复位 `staged`**；**每轮绘制前 `p.tabStep = 0`**（照 48 的循环）。
  2. 行 = `p.templateItems(configTemplateSubagent, "", entry, false, true)`（48 的最终签名；本模板没有 `KeyName`/`CreateEditable`，`creating=true` 只是语义正确、行为不变）；**Name 行的值由 `createName` 填**（取到该行后覆写 Value）。页面顶部那行文本用局部 `notice` 变量（与 `editTemplatePage` 用 `templateNotice` 同一位置，见 2339-2340）。尾行由本循环自己加：有 footer 能力走 `p.actionPage(notice, configCreateFormKeys, items, -1, FooterAction{Label: configCreateSaveLabel, Description: configCreateSaveDesc, Key: configCreateSaveKey, Kind: FooterActionSave})`（`configCreateFormKeys` 让 footer 读 `Enter to open Esc to cancel`）；无 footer 能力（纯 picker/测试替身）走 `p.page(...)` + 尾部一行 `SelectItem{Label: configCreateSaveLabel}`，用 `saveRow := len(rows)` 记行号判提交（照 48 的 `addAgentDefinition`）。**不要给 `templateItems` 加尾行参数**。
  3. 字段编辑：**Name 行走专用路径**（`editScalar` + 四句校验 → 只改 `createName`，改名即校验，失败保留输入）；**其余字段一律走 `openTemplateField(sectionDraft, rowPath, field)`**（**按 B4 落地后的最终签名传参**——B4 会给它加角色/模板形参；`staged` 让模型/强度/集合/文本都只写内存），与已存页行为一致，不要给任何字段另写一条编辑路径。
  4. 提交（footer `s Save` 或尾部 `Save` 行）：
     - 名称校验四句（原型 `commitInput` 逐字；Name 行编辑与提交共用同一个校验器）：空 → `A name is required.`；不匹配 `^[a-z][a-z0-9-]{1,62}$` → `Use a lowercase name with letters, numbers and hyphens.`；6 个内置名之一 → `Those names are the 6 built-in subagents. Choose a different name.`；**与本列表已存条目同名** → `That name already exists. Choose a different one.`（只比兄弟条目：loader 明确允许与被 shared 定义同名的自定义子代理，`pkg/config/agents.go:370-377`、`:420`）。
     - 通过后 `entry["name"] = createName`；**不走 `assign`**：`if err := p.save(sectionDraft); err != nil { notice = err.Error(); continue }`（`p.save` 失败不动 doc → 表单与已填值原样保留，`staged` 仍为 true）。
     - 成功后：`staged = false`（defer 也会）、**`return p.sectionDoc(p.section)`**（`editRows` 用调用方 doc 重算行表；不返回新 doc 新行不会出现），**不加任何成功提示**（/config 的沉默成功裁决）。
     - 被拒句子用 loader 自己的话（现有语义；`config_panel_test.go:2841-2864` 钉住「validator's own words」）：常见 `system_prompt required`、`allowed_tools requires at least one tool with tools: pick`。
  5. **退出语义与 48 的循环逐条一致**（别自己发明；原型 footer 也是「Esc discards, and an arrow …」）：
     - **Esc 直接丢弃**（不问）：丢 `sectionDraft` 与 `createName`、零写入、`staged` 复位；
     - **`p.tabStep != 0`（←/→ 切页签）时**：表单未动 → 直接丢；动过 → 问 `Discard` / `Keep editing`（`configDiscardDraftLabel`/`configKeepEditingLabel`，**Keep 预选**），Keep 后把 `p.tabStep` 清零、留在表单；
     - 子代理草稿的「未动」判据：`len(entry) == 0 && createName == ""`（48 的 `agentDraftUntouched` 判 `len(draft)==1`，那是 agent 草稿专用，不能照抄）。
- 主代理创建（48 的 `addAgentDefinition`）不动。

**B4. `configRoleHelp` 的角色判定（裁决 8）**
`openTemplateField`（2535）把 `field.Hint` 设成 `configRoleHelp(field, settings)`；该函数用 `configAgentRolePrimary(settings)` 判角色，而 subagent 条目没有 `primary` 键 → 返回主代理句。修法：把角色显式传进来（`openTemplateField` 已知 `template`/`rowPath`，加 `subagent bool` 或传 `template`），子代理条目返回 `Replaces the built-in system prompt whole.`。**不要**改 `configTemplateSubagent` 的 `system_prompt` Hint（行上不渲染 Hint，2477-2483 是死改动）。原型同步见 Step 7。

## 5. 行为变化矩阵

| 场景 | 旧 | 新 | 备注 |
|---|---|---|---|
| 该页空态 | `Nothing configured yet / Add the first entry.`，零行 | 6 个内置名成行 | 刻意（裁决 1、2） |
| 行名 | `#1/#2`（只有 shared 按名） | 全部按子代理名；无名条目 `#N` | 刻意 |
| 行 desc/value | 内置行 `agent override:`/`global default:`/`Follow conversation` | 该页内置行与自定义行全空 | 刻意（裁决 2） |
| shared 行 | `Shared` + 「…(shared)」、`d` 不出现 | **不变** | 裁决 7 |
| 按 `a` | `Value type` 菜单；选 Object 被 loader 拒 | 完整表单 + `s Save` 一次落库；成功后**无任何提示** | 刻意（裁决 5、6；沉默成功） |
| 创建表单里把 Name 敲成内置名 | —（没有表单） | 四句拒绝之一，**表单不塌**（Name/System prompt/tools 仍在），零写入 | 必需（名字不落进条目，见 B3） |
| 打开内置 override | 3 行 `Type name`/Model/Thinking level | 2 行 Model/Thinking level，无状态 | 刻意（裁决 1、3） |
| system 类型 | 不开行；存量条目整页只读 + 拒开；loader 拒写 | 同 public 一样可配 model+effort；loader 接受 | 刻意（裁决 1、4） |
| plan-reviewer 的全局 Thinking level | 死设置（无人读） | 作为审批卡评审强度默认生效（无 agent pin 时） | 同源缺陷，一并修 |
| `d Remove` | 任何非 shared 行都出现 | 只有真存了条目的行出现 | 必需 |
| 自定义子代理 prompt 帮助语 | 主代理句（「Appended…never replaces」） | 「Replaces the built-in system prompt whole.」 | 裁决 8 |
| subagent 模板 name 行 | Label `Type name` + 过期 hint | Label `Name` + `Lowercase letters, numbers and hyphens.` | 原型一致 |
| 已存自定义条目 | 全字段模板页，Name 可编辑 | **字段集合不变**；name 行 Label/Hint 按 B1 变成 `Name` / `Lowercase letters, numbers and hyphens.` | 裁决 10 |
| 主代理创建页 | （48 在改） | 完整表单 + `s Save` | 48 范围 |

## 6. Scope

**In scope**
- `pkg/config/agents.go`（A1，叠在 48 之上）＋ `pkg/config/agents_test.go`（改写 `TestLoadRejectsInvalidAgentSubagents` 的 `"system subagent"` 用例，按测试名+case 名定位：want 从 `cannot be configured` 改 `is a built-in`；新增 system pin 收/拒矩阵）。
- `pkg/run/subagent_catalog.go`（A2）＋ `subagent_catalog_test.go`。
- `pkg/turn/approval.go`（A3）＋ `approval_test.go`。
- `pkg/tui/config_panel.go`（B1–B4：`subagentTypeNames()`、`configRowIsEntry`、该页列短路、`editBuiltinSubagentRow`、`addSubagentEntry`、（`editModel` 的空 pick 守卫）、`configRoleHelp`、删 system 只读四处）＋ `pkg/tui/config_panel_schema.go`（subagent 模板 name 行 label/hint）＋ `pkg/tui/config_panel_test.go`。
- `plans/352-config-subagents-page.md`、`plans/README.md` 索引、`plans/config-tui-prototype.html`（+ `.test.cjs` 若断言受影响）。
- 直接在当前源码实施（owner 裁决），改动留工作区。

**Out of scope**
- 48 的落点：`overlays.go` 的 `FooterActionKind`/`fireAction`、`configPanel.staged` 字段本体、`configCreateSave*`/`configDiscardDraftLabel`/`configKeepEditingLabel`/`configCreateFormKeys`、`templateItems` 的 `creating` 形参、`addAgentDefinition`、`pkg/config/load.go` 的角色 id 规则。
- `editTemplatePage` 的**页调用（`p.page`）与尾行行为**：不改（本计划只删其中的 system 只读段，见 B2）。
- `pkg/tui/chat_slash.go`、`skills_versions.go`、`commands_test.go`：另一会话未提交改动。
- **web/gateway**：三处 `subagentKindSystem` 硬拒（`subagents_api.go:1081-1085`、`:1119-1120`、`:1156-1157`）与只读全局行的 `systemSubagentRow` 不改，`frontend/` 一行不改。理由：本次是 TUI 面缺口；loader 是配置层唯一权威。**已知显示分歧**：A1 后 web 仍显示全局行或空（`subagentNames` 跳过 agent 自己那条 system 名条目，`:166-181`）——写进 §10，另开计划。
- 已存自定义子代理 `name` 行的锁死与字段顺序（该页另一类 parity 缺口）。
- `pkg/config` 的 `stripRetiredBuiltinEntryFields`、`IsReservedDefinitionID`、definitions 键规则。

## 7. Steps

**Step 0 — 基线、依赖闸门**
```bash
mkdir -p "$TMPDIR/fb-cfg"
git rev-parse HEAD && git status --porcelain > "$TMPDIR/fb-cfg/baseline.status" && wc -l "$TMPDIR/fb-cfg/baseline.status"
shasum -a 256 plans/config-tui-prototype.html
grep -n "staged bool" pkg/tui/config_panel.go
grep -n "configCreateSaveKey\|configCreateSaveLabel\|configCreateSaveDesc\|configCreateFormKeys\|configDiscardDraftLabel" pkg/tui/config_panel.go
grep -n "func (p \*configPanel) templateItems" pkg/tui/config_panel.go   # 期望含 tailRows, creating
grep -n "FooterActionSave" pkg/tui/overlays.go pkg/tui/config_panel.go
grep -c "subagentConfigNamePattern" pkg/config/load.go                              # 期望 0
grep -n "subagentConfigNamePattern" pkg/config/agents.go                            # 读上下文：唯一的 MatchString 调用在 shared 分类之后
```
预期：HEAD `9b5c4891`（或之后的 HEAD，见 §9 的漂移规则）、原型哈希 `eaa03b8b…`/`033ea112…`、`baseline.status` 行数记下来（收口时用它比对，见 Done 9）；§0 那两条前置形状全在 HEAD（48 已合并）。
- 前置形状不符（`templateItems` 不是 5 参、缺 `staged`/常量/`FooterActionSave`、`load.go` 里还有 `subagentConfigNamePattern`、`agents.go` 的 pattern 检查没有 `!sharedReference` 守卫）→ **STOP**：不要自己补 48 的活，也不要按旧形状写调用。

**Step 1 — pkg/config（A1）**
测试先行：system 名 `{name:"plan-reviewer"}`、`{name:"plan-reviewer",model:<可解析>}`、`{name:"goal-evaluator",model:…,reasoning_effort:"high"}` 通过；带 `system_prompt`/`enabled`/`description`/`tools` 仍拒且句子 `… is a built-in: it takes a model and a reasoning effort, nothing else.`；effort 无 model 仍 `thinking level requires a model`；48 的引用条目用例保持绿。
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/config -run 'Subagent|DefinitionRole' -count=1
```
再改实现（§4 A1）。

**Step 2 — pkg/run（A2）**
测试：`plan-reviewer` pin（model+effort）→ `Model` 返回它；`Public()` 不含它；`ResolvePublic("plan-reviewer")` unknown；`ToolPolicy` 不给自定义策略；**只带 name 的 goal-evaluator pin 仍回落全局行**；**悬空 model 的 goal-evaluator pin 仍回落全局行**。
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'SubagentCatalog|SubagentOwnModel' -count=1
```

**Step 3 — pkg/turn（A3）**
测试：agent pin（model+effort）→ `Preferred:true` 且 `DefaultEffort`=pin effort；无 pin 但有全局 `agents.plan_reviewer.reasoning_effort` → 该 effort 成为默认（今天被忽略，见 §2.2 #13）；悬空 pin → 不 Preferred、不 panic。
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run 'PlanReview' -count=1
```

**Step 4 — TUI 一整步（B1 + B2，一次到绿）**
> 行名与页内过滤互相牵连（同一批用例既用 `#N` 定位又用 `Type name` 认页），拆两步会让 `pkg/tui` 中间态必红，所以合一步。
- 实现 §4 B1 + B2（含 `editModel` 的空 pick 守卫、删 system 只读四处、schema name 行 label/hint）。**B4 给 `openTemplateField` 加形参时会顺带改 `addAgentDefinition` 里的那一处实参调用**（`grep -n "openTemplateField(" pkg/tui/config_panel.go` 只有两处：`editTemplatePage` 与 `addAgentDefinition`）——这是实参适配，不碰 48 的循环逻辑，属本计划 In scope。
- **必须同时改写的既有用例**（grep 名字定位）：
  - `TestConfigPanelSubagentRowsNameTheirModelSource`（`agent override:`/`global default:`/`Follow conversation`、`#5`）
  - `TestConfigPanelSystemSubagentReadonly`（断言每行 `configSystemStatus`、页面含 `Type name`+`Enabled`、出现 `configSystemSubagentNotice`）→ 场景已被裁决 1/4 取消：改写为「system pin 页 = Model/Thinking、可写、无状态」的正向用例
  - `TestConfigPanelCustomOnlyHidden`（用「页面含 `Type name`」认 built-in 页）→ 改用行集合/「不含 `System prompt`」辨认
  - `TestConfigPanelAgentPageListsSharedSubagentsDisabled` 与 `TestConfigPanelAgentPageSharedDefinitionIsReadOnly`：Shared 状态与摘要**仍成立**，只适配行名与空 desc
  - 所有 `agentSubagentsSteps(...)` 里用 `#N` 定位的脚本 → 改条目名
  （**按测试名 grep 定位，别用行号**——48 合并后全部漂移。）
- 新增测试（每条都要显式断言页面来源是 main › Subagents › `<name>`；全局 Built-in subagents 页今天就是 2 行，容易假绿）：
  - 空 fixtures → 6 行、标签为 6 个内置名、`Description`/`Value` 皆空、无状态；
  - 打开占位 `explore` → 行集合 `{Model, Thinking level}`，显式断言 `Type name`/`System prompt`/`Enabled` 不在；
  - 选 model → 恰好 1 次写入、条目 `{name:"explore",model:<id>}`、路径是数字下标；
  - 占位行 picker 选空 → **零写入**；
  - Esc → 零写入，重开仍无该条目；
  - 清空已有 pin → effort 一并消失；
  - `plan-reviewer` 可写（system）；
  - 占位行按 `d` 不出现 `Remove …?`（footer 路径；`RowActive` 是闭包，无直接钩子）。
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestConfigPanel.*(Subagent|CustomOnly|Builtin)' -count=1
```
- `scriptedSelector.SelectRich` 未命中 label 返回 `(-1,false)`＝**Esc**（`commands_test.go:133-141`）→ 每条断言都要显式 `t.Fatalf` 页面存在。

**Step 5 — TUI 创建（B3）**
- `addRow` 插入点、`addSubagentEntry` 自建循环、footer Save + 尾部 Save 行、`p.save(sectionDraft)`、`defer` 复位 `staged`、成功后 `return p.sectionDoc(p.section)`、四句名称校验（只比兄弟条目）。
- 测试：`a` 后**表单行在场**（含 `System prompt`）且零写入；有 footer 的 selector 走 `s Save` 动作、纯 picker 走尾部 `Save` 行；**Name 编辑里输入 `explore` → 弹内置名拒绝，且表单行仍在（Name/System prompt/tools 不消失）**；非法/保留/重名各弹对应句子且零写入；名+prompt → 恰好 1 次写入、raw 含新条目、**页面回到列表且新行可见**、**不带任何成功提示文案**；只有名没 prompt → **表单值与 name 仍在**、`session.writes` 为空、出现 `system_prompt required`；Esc → 零写入；**Esc 之后再改另一个条目的字段仍会写库**（`staged` 已复位）。
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestConfigPanel.*Subagent' -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1
```

**Step 6 — gofmt / vet / 跨包回归（A1 改 loader，gateway/process 必须跑）**
```bash
gofmt -l cmd pkg third_party plugins
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
CGO_ENABLED=1 go vet -tags fts5 ./...
CGO_ENABLED=1 go test -tags fts5 ./pkg/config ./pkg/run ./pkg/turn ./pkg/tui ./pkg/gateway ./pkg/process ./pkg/architecture -count=1
```

**Step 6.5 — 死代码/死常量清扫（owner 硬规则，裁决 12；与行为改动同一次交付）**
Go 只对未使用的局部变量与 import 报错，**函数/常量/结构字段死了不会让编译失败**，所以必须逐个 grep。本计划至少要清掉：
```bash
# 必须归零（本次删掉的三件事 + 48 批次留下的死常量）
grep -rn "configTemplateRowIsSystem\|configSystemStatus\|configSystemSubagentNotice\|configKeyNameHint" pkg/
# 删掉 configRowColumns 的尾巴后，逐个确认是否还有别的调用者；没有就一起删
grep -rn "configAgentOverridePrefix\|configGlobalDefaultPrefix\|configSubagentModelsKey\|configEffortSummary\|p.configModelLabel(" pkg/
# 语义改过后可能变成死字段的模板标志（结构体字段与所有使用点）
grep -rn "CustomOnly\|HideOnBuiltin\|SubagentOnly\|PrimaryOnly" pkg/tui/
```
- 前两条必须 0 命中（或只剩仍然合法的调用者，逐条在计划里说明）。
- 第三条：`HideOnBuiltin` 保留（语义扩到 6 个内置），`CustomOnly`/`SubagentOnly`/`PrimaryOnly` 若某个失去全部使用点就把字段本身删掉，别只删调用。
- 原型（Step 7）同样适用：删掉 `Agent` 状态后若 `effectiveFor`/`effortLabel` 之类失去唯一调用点，一并删（原型不是死代码的垃圾桶）。
- `go build ./...` 与 `gofmt -l` 必须仍然干净；`pkg/architecture` 的文件/扇出预算不受影响。

**Step 7 — 原型同步（裁决 2/3/7/8 + 评审指出的三处）**
`plans/config-tui-prototype.html`：
1. `fieldsAt` 内置行块删 `row.status=…'Agent'|'Global'…`（裁决 3）；该页列表行（内置/自定义）不画 value/status，**shared 行保留**（裁决 7）。
2. **内置合成行寻址**：现在 `path:[...frame.path,name]` 在数组上写不进值（`setAt` 写非下标属性，`clone`/`persist` 后丢失）。内置行改成「先追加实体条目 `{name: <名字>}`，再用**数字下标**写入」——注意 `setAt` 对不存在的下标会先建 `{}` 再写 `model`，得到不合法的 `{model:…}`（loader 要求 name，`pkg/config/agents.go:399-400`）。
   **shared 合成行同样要实体化**（`{name, enabled: true}` 再按下标写开关）——否则原型里 shared 开关的写入照样丢；这与 TUI 的 `setSharedReference`（`config_panel.go:2616-2630` 追加 `{name, enabled:true}`）同形。**关掉时也与 TUI 一致：删掉该条目**（不是写 `enabled:false`）；两种存储形状 loader 都收（`agents_test.go:796`），但 TUI 只产生「删除」这一种。
3. **`rowField` 的 `entry:true`**：**内置与 shared 合成行都必须不是 entry**，否则 `d` 会给占位行弹删除确认（与裁决 7 / TUI 的 `configRowIsEntry` 相反）。
4. **`system-stray-entry` 场景**：数据 `{name:'plan-reviewer',enabled:false}` 里的 `enabled` 仍被 loader 拒（A1 只放行 model+effort）→ 改数据与说明：system 条目可配 model + thinking level，其余字段被拒。
5. **子代理 create 的底栏/提交**：`startAdd()` 的 subagent 分支已经自动继承 48 新做的 create 机制（`creatingFrame`/`saveCreateForm`/`discard` 视图），所以**子代理半边只需**：① 在 `saveCreateForm` 里加子代理形状的提交（现有实现只跑 agent 的 `validateDraft` 与 `frame.path.at(-1)` 取 key；子代理草稿在列表里、按下标定位、名字在条目里）——四句名称校验（§4 B3.4）+ 把条目实体化（先 `{name}` 再按下标写其余字段）+ 成功后回列表；② 子代理条目名字的 dashes-only 文案。**不要重画 agent 半边**（footer 文案、`discard` 页、`creatingDirty` 都已在里面）。
6. **同一个塌表坑**：`startAdd` 把 `name` 直接放进条目，`fieldsAt` 的 `customOnly && BUILTIN_SUBAGENTS.includes(value?.name)` 会在名字敲成内置名时把表单过滤塌——TUI 用 `createName` 绕开，原型必须同样「创建期名字不落进条目」或让 create 帧跳过该过滤。
7. **prompt 帮助语**（裁决 8）：edit 视图的选择条件从「行值 `primary===false`」扩展成「子代理条目也取 helpSubagent」。
8. 补一条「子代理创建表单」场景（场景名发 48 一份），并把 48 的 `create-held`/`create-esc`/`create-tab-confirm` 三个场景的语义在子代理页面上覆盖到（提交前只在草稿、Esc 丢弃、←/→ 进 discard 且 Keep 预选）。
- 重算哈希，把**旧 → 新**写进 `plans/352-config-subagents-page.md`：html 旧 `eaa03b8b921a987b04815cc2f7517ae2840539e85430fc0314b67efcf6c3630d`、test.cjs 旧 `033ea1121e3918a4d7d465aa1692aed4352151ec876822cd469c6980c5661eb6`（48 的中间哈希，记在它的 `plans/351-config-agent-create-form-first.md` 里）；**不自行宣布冻结**，交 owner 确认。48 那边 parity 仍记 BLOCKED，解阻点就是「子代理半边改完 + owner 确认新哈希」。
- 定向验证：`python3 -m http.server 8765`（在 plans/）+ Playwright 打开该 HTML 逐场景渲染；`node --check` 两个文件；`config-tui-prototype.test.cjs` 跑到已知停点（第 167 行等 `Default` 按钮超时——`Ctrl+U` 既存缺口，先于本次改动），记录停点与计数。

**Step 8 — 真机验收（tmux；`-tags fts5`；临时 `FOREBRAIN_HOME` + 临时项目目录）**
1. **复现→修复**：`$D build && $D reset && $D start reply 'ok'` → `/config` → … → Subagents：6 行名字；`a` → 完整表单（`System prompt` 在场）→ Name `repo-scout` + prompt → **按提交键 `s`**（footer 显示 48 收工后的最终文案）→ 回列表且出现 `repo-scout`；`$D db "SELECT value_json FROM fb_config WHERE section='agents'"` 恰好一条 `{"name":"repo-scout","system_prompt":…}`。再开 `explore` → 只有 Model/Thinking level → 选模型 → 存储出现 `{"name":"explore","model":…}`。
2. **system pin 生效**：`$D start toolcall`（`fake_provider.py` 的 `TOOL_SCRIPTS` 发 `enter_plan_mode` → `exit_plan_mode`，出真实审批卡）；模型行由 `driver.sh start` 的 `model_seed` 步已种（需要手工种时：`.claude/skills/run-forebrain/model_seed.sh`，用法 `printf <key> | model_seed.sh <bin> <home> <provider> <model> <base_url> [id]`）。给 main 配 `{"name":"plan-reviewer","model":<row id>,"reasoning_effort":"high"}` 后重启：审批卡评审选择器里该行 Preferred（默认高亮），第二屏强度开在 High。驱动不到卡片时用 Step 3 的定点测试 + `$D db` 存储证据代替，并写明原因。
3. **中断自救**：创建表单填一半 Esc → 零写入、回列表；`Ctrl+C` 两次退出 `EXIT_CODE=0`；再启动 `/config` 正常。
4. **回归**：shared 行 switch 开/关；Defaults › Built-in subagents 六个全局行仍可配；Agent definitions 创建页（48 的新表单）按 `s`（显示文案取最终值）仍能建主代理。

**Step 9 — 收口**
复制成 `plans/352-config-subagents-page.md`（状态「已实施并真机验收通过（日期）」+ 验收记录）→ 更新 `plans/README.md` 索引（DONE + 日期）→ 清理 `$TMPDIR/fb-cfg` 与 tmux session → `git status` 核对只出现 In scope 文件、其他人改动原样保留。

## 8. Done criteria（机器可查）

1. `CGO_ENABLED=1 go test -tags fts5 ./pkg/config ./pkg/run ./pkg/turn ./pkg/tui ./pkg/gateway ./pkg/process ./pkg/architecture -count=1` 全绿。
2. `gofmt -l cmd pkg third_party plugins` 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./...` exit 0。
3. `grep -rn "is dispatched by the system and cannot be configured" pkg/config --include=*.go` = 0；`grep -rn … pkg/gateway --include=*.go | grep -v _test.go` = 1。
3b. **死代码归零**（裁决 12）：`grep -rn "configTemplateRowIsSystem\|configSystemStatus\|configSystemSubagentNotice\|configKeyNameHint" pkg/` = 0；`configRowColumns` 尾巴删掉后**确实失去全部调用者**的只剩 `configAgentOverridePrefix`/`configGlobalDefaultPrefix`（`configModelLabel` 仍被 `configSharedModelSummary` 用、`configEffortSummary` 仍被 `configTypePinRow` 等用、`configSubagentModelsKey` 仍被 `configBuiltinRowPath` 用——**别误删**）与失去使用点的模板标志字段**已删除而不是留空**；原型的死 helper 同样删净。
4. 该页空 fixtures：6 行、标签为 6 个内置名、`Description`/`Value` 皆空、无 `Agent`/`Global` 状态；shared 行仍有 `Shared` + 摘要。
5. 打开内置行的行集合 == `{Model, Thinking level}`（显式断言 `Type name`/`System prompt`/`Enabled`/`Description`/`Tool access`/`Allowed tools`/`Allow continuation` **都不在该页**——内置页隐藏 name 行；name 行的 Label 断言见 Done 8）。
6. 创建测试：`a` 后表单行在场且零写入；Name 敲成内置名**表单不塌**；提交成功恰好 1 次写入、raw 含新条目、**返回的 doc 让列表出现新行**、无成功提示；提交被拒后表单值仍在且零写入；Esc 后零写入；Esc 之后再改别的条目仍会写（`staged` 复位）。
7. 占位行按 `d` 不出现 `Remove …?`；已存条目仍出现。
8. subagent 模板 name 行的 Label 为 `Name`、Hint 为 `Lowercase letters, numbers and hyphens.`（创建表单与已存自定义条目页都断言）。
9. 收口时把 `git status --porcelain` 与 Step 0 的 `$TMPDIR/fb-cfg/baseline.status` 比对：**新增/修改的路径全部落在 §6 In scope**（基线里已有的行原样保留）；`git rev-parse HEAD` 为 `9b5c4891` 或其后；若期间他人提交过，按 §9 的漂移规则重记基线并说明。
10. 原型哈希记录在案（旧→新）且 owner 已确认（执行者只报告，不自行冻结）。

## 9. STOP conditions

- §0 的前置形状（48 那一批）与 HEAD 不符 → **STOP**：不补 48 的活、不按旧形状写调用；`pkg/config` 里若又出现第二处拒绝 system 条目的入口 → 也 STOP。
- **只有内容不符才 STOP**：判据见 §0 的内容 grep 与原型 sha256。HEAD 变了（例如 48 合并）但五条内容 grep 与原型哈希全过、`git status --porcelain` 与基线快照的差异只在 §6 路径 → **记录新基线继续**；原型 sha256 变了或任一内容 grep 不符 → **STOP** 并重读现场。
- A1 之后仍有第二处拒绝 system 条目（`grep -rn "cannot be configured\|builtInSystemSubagentNames" pkg --include=*.go`）→ **STOP**，先把入口列全。
- A2 之后 `ResolvePublic("plan-reviewer")` 能返回定义（pin 泄漏成可派发类型）→ **STOP**（安全问题）。
- 该页写入出现第二条绕过 `staged` 的 `p.save`（`grep -n "p.save(" pkg/tui/config_panel.go` 应只有 `assign`/`assignModel` + 创建提交一处）→ **STOP**，先收敛。
- `go test ./pkg/tui` 出现与本计划无关的失败（其他会话在同文件改）→ **STOP**，问对应 agent，不顺手修。

## 10. Maintenance notes

- **web 端会误报**：A1 后 `subagentNames` 跳过 agent 自己那条 system 名条目（`subagents_api.go:166-181`），`systemSubagentRow` 只读全局行（`:295-318`）→ 配了 system pin 的 agent 在 web 上仍显示全局行或空。要让 web 可读可写需另开计划（三分支 + 表单 + `subagents_api_test.go:682`）。
- **`ModelPins` 措辞**：per-agent 条目会被记成 `ModelPinSubagent` + 条目名（`pkg/config/llm_models.go:238-244`），system pin 显示成「subagent plan-reviewer」。仅措辞。
- **同名不是错误**：自定义子代理可与某个 shared 定义同名（`pkg/config/agents.go:370-377`、`:420`，agent 自己的定义优先）。创建时的重名检查只比本列表兄弟条目。
- **名字规则两套是刻意的**：定义/共享引用走主代理规则（允许 `_`，裁决 9），自定义子代理条目走 dashes-only（模型看到的 `subagent_type` 命名空间）。TUI 创建**自定义**子代理用后者与原型那句文案。
- **已存自定义子代理 `name` 仍可编辑**、字段顺序与原型不同：该页已知 parity 缺口，本计划不动。
- **一个概念一处实现**：6 个内置类型名只从 `agent.PublicSubagentNames` + `agent.SystemSubagentNames` 派生，本页与 `editBuiltinSubagents` 共用 `subagentTypeNames()`；`ResolvePublic`/`Public`/`ToolPolicy` 永远看不到 system pin（安全边界，别为了「统一」并进 `entries`）。
- **`staged` 的纪律**：它覆盖 `assign`/`assignModel` 两处 `p.save`；创建提交直接 `p.save(sectionDraft)`。以后新增写入路径必须尊重 `staged`，且创建出口必须复位（`defer`）。
- **`editTemplatePage` 的页调用保持 `p.page`**：它被 5 处共用，给创建页加 footer Save 会污染已保存页与 MCP/hook/OAuth/LSP 页；创建表单只在各自的循环里接 footer。本计划只从它里面删掉 system 只读那一段（删 `configTemplateRowIsSystem` 后不删它就不编译）。
- **创建期名字单独持有**：`createName` 不落进条目，是为了让 `templateItems` 的名字相关过滤（`CustomOnly`/`HideOnBuiltin`/隐藏 name 行）在创建期看不到用户输入的内置名。若日后有人「顺手」把名字直接写进草稿条目，创建表单会在敲到内置名时塌掉——这是本计划刻意绕开的坑。
- **删路径就要删干净（裁决 12）**：`configTemplateRowIsSystem` / `configSystemStatus` / `configSystemSubagentNotice` / `configKeyNameHint` 是这条规则在本计划里的第一批；`configRowColumns` 的尾巴删掉后**预计只有** `configAgentOverridePrefix`/`configGlobalDefaultPrefix` 会死（`configModelLabel`/`configEffortSummary`/`configSubagentModelsKey` 都还有别的调用者，见 Done 3b）。Go 不会替你发现死函数/死常量/死字段（只查局部变量与 import），所以 Step 6.5 的 grep 是必做项：**只删 grep 证明无调用者的**，别按猜测连坐。

## 11. 验收记录（执行后回填）

- 实施日期 / 执行者：2026-11-22，执行器（deepseek/deepseek-flash，effort max）在 worktree `.worktrees/config-subagents-page`（branch `config-subagents-page`）。
- 成品计划：`plans/352-config-subagents-page.md`（评审方维护；执行器按简报跳过此步）。
- 场景 1（复现→修复）证据：执行器真机（隔离 home）：该页 6 个内置名成行、只有名字；`a` 开整表（Name/Model/Thinking level/System prompt/Description/Tool access/Allowed tools/Allow continuation，footer `… esc to cancel s to save`）；`repo-scout` + prompt 按 `s` → 回列表出现新行、**无成功提示**，存储 `{"name":"repo-scout","system_prompt":"You scout repositories.\n\n"}`；`explore` 页只有 Model/Thinking level，选模型写入恰好一条 `{"model":"…","name":"explore"}`；占位行 footer 没有 `d to remove`，按 `d` 无反应；提示词编辑器显示「Replaces the built-in system prompt whole.」。
- 场景 2（system pin 生效）证据：`plan-reviewer` 页可写并落 `{"model":"harness-reasoner","name":"plan-reviewer","reasoning_effort":"high"}`（页面 `Model openai/gpt-5.1 / Thinking level High`）。**出口计划审批卡未能驱动**：本构建 `enter_plan_mode` 报 `(no output)` 且 `exit_plan_mode` 拒绝（先于本次改动的既有问题，shift+tab 手动进 plan 模式后同样），按计划的 fallback 用 Step 3 的定点测试（`pkg/turn` 的 preferred/DefaultEffort 断言）+ 上述存储证据代替。
- 场景 3（中断自救）证据：创建表单填一半 Esc → 回列表、存储字节不变；`←/→` 走 discard 确认（Keep 预选）；双 Ctrl+C → `EXIT=0` + resume 提示；重启 `/config` 回到同样的行列表。
- 场景 4（回归）证据：Defaults › Built-in subagents 六个全局行仍可配；48 的 agent 创建仍能用 `s` 落 `review-agent`；shared 行仍显示 `repo-auditor (shared) … Shared` + 只有 `a to add`，开启后存 `{"enabled":true,"name":"repo-auditor"}`。
- 原型：旧 `6234536d…`（html）/ `033ea112…`（test.cjs）→ **新 `a6bc85c18dc580d4d7c8ed2b04bf5444e3a4355f95e7c4d902aa773ff8ba9ad1`（html）/ `72140edcbb68bbc2a6a676cf974fd10e2505c8d1ac075575bbeb9de943aeeeb7`（test.cjs）**；**owner 2026-11-22 已确认并冻结**，旧版备份 `plans/*.pre-352` 按 owner 指令删除。
- 工具链坑：worktree 需 `third_party/{libtokenizers,pdfium}` 符号链接（否则 39MB 慢下载）；`pkg/architecture` 需要 gitignored 的 `pkg/gateway/dist`（从主树拷）；多行设置用 `$D key C-j` 存（不是 `C-Enter`/`submit`）；`enter_plan_mode` 在本构建失败（见场景 2）；一次 `go test` 撞 disk full（`clang: linker command failed`，`go clean -cache` 恢复）。

## Implementation Status

**VERDICT: APPROVE**（一轮 REVISE 后）。worktree `/Users/doudou/workspace/unionj-cloud/forebrain-harness/.worktrees/config-subagents-page`，branch `config-subagents-page`；**合并由 owner 决定**（未 push、未 merge、未 rebase）。

**提交**：`a8d61199 fix(config): create and pin subagents on the Subagents page`（实施）＋ `6541b8ab fix(config): one subagent name rule, and its copy on the page`（修订轮）。

**逐任务状态**
- Step 0 基线/依赖闸门 — 完成。执行器按简报用 `6cd9140c` + html `6234536d…`/cjs `033ea112…` 取基线（计划里的 `9b5c4891`/`eaa03b8b…` 是历史值），内容漂移 grep 全过。
- Step 1 pkg/config（A1）— 完成。system 名并入内置分支；`TestLoadRejectsInvalidAgentSubagents` 的 `"system subagent"` 用例改判；新增收/拒矩阵。
- Step 2 pkg/run（A2）— 完成。`systemPins` 隔离 + `Model` 先解析后回落；`Public`/`ResolvePublic`/`ToolPolicy`/`customs` 未动。
- Step 3 pkg/turn（A3）— 完成。`preferredPlanReviewerRow(cfg, agentName) (rowID, effort)`，全局行的 `reasoning_effort` 从死设置变成默认强度；A4 注释记两条链路。
- Step 4 TUI 行投影 + 内置页（B1+B2+B4）— 完成。六行成行、只有名字、shared 保留状态/摘要、`d` 只对已存条目；内置页两行、无状态；空 pick 零写入；system 只读语义四处删净。
- Step 5 TUI 创建（B3）— 完成。自建循环 + footer `s`/尾部 Save、`createName` 不落条目、四句拒绝、`p.save` 失败保留表单、Esc 直丢 + 页签箭头才问 Discard/Keep、`staged` defer 复位。
- Step 6 gofmt/vet/跨包回归 — 完成（详见下方自跑）。
- Step 6.5 死件清扫 — 完成且超出预期：`configTemplateRowIsSystem`/`configSystemStatus`/`configSystemSubagentNotice`/`configKeyNameHint`/`configAgentOverridePrefix`/`configGlobalDefaultPrefix` 全删；另删掉**既有**死函数 `configSystemSubagentRow` 与原型死 helper `nextSubagentName`。
- Step 7 原型同步 — 完成（8 项 + 5 个新场景 + 两处旧说明更新）；另把原型的名字规则两处副本折成一个谓词。
- Step 8 真机验收 — 完成三面，场景 2 用计划允许的 fallback（审批卡驱动不了，见 §11）。
- Step 9 收口 — 执行器只 commit 并清理了 run 目录；`plans/352-*.md` 与 README 索引由评审方维护（本文件 + README 一行）。

**评审方自跑（在 worktree，最终树 `6541b8ab`）**
- `go test -tags fts5 ./pkg/config ./pkg/run ./pkg/turn ./pkg/tui ./pkg/gateway ./pkg/process ./pkg/architecture -count=1` → 全 ok（config 1.5s / run 43.7s / turn 4.1s / tui 196.5s / gateway 10.6s / process 227.0s / architecture 2.4s）。
- `gofmt -l cmd pkg third_party plugins` → 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./...` → exit 0。
- Done 3：`pkg/config` 命中 0、`pkg/gateway` 非测试 1。Done 3b：四个死符号 0 命中、两个前缀常量 0、四个模板标志仍各有读者、`subagentEntryNamePattern` 0、名字规则全仓仅 1 处（`pkg/config`）。
- scope：`git diff --stat 6cd9140c..HEAD` = 10 文件（+ 修订轮 6 文件）；`git status` 干净。

**偏离与判断（按 merit 接受）**
1. `pkg/run/subagent.go` 只改注释——§4 A4 明确点名，属 in-scope 意图；无行为变化。
2. `pkg/tui/commands_test.go` 加 `inputHeads`（测试替身记录编辑器页头行）——Done 8 的 Hint 断言必需；特意不进 `richLabels` 以免打断别的测试按位置索引，理由写在代码里。
3. 修订轮把 `pkg/config` 的 `subagentConfigNamePattern` 导出成 `ValidSubagentEntryName` 并让 TUI 复用（评审要求，去掉第二份规则）；原型里同样的两处副本一并折成一个谓词。
4. `pkg/architecture` 在全新 worktree 会因缺 gitignored 的 `pkg/gateway/dist` 失败（环境问题，非本次改动）；补上后绿。

**未完成 / 未验证**
- ~~原型两份文件未冻结~~ → **已冻结**（owner 2026-11-22 确认，哈希见 §11；备份已删）。
- web/gateway 面仍只读且会误报 system pin（计划 §10 已记），需另开计划。
- 场景 2 的审批卡链路只能以定点测试 + 存储证据代替（`enter_plan_mode` 在本构建失败，先于本次改动）。
- 原型 `.test.cjs` 第 167 行（`Ctrl+U` 已知缺口）之后的断言仍不执行，包括本次为 system pin 改写的那条；文件已更新，但不是证明。
- 已存自定义子代理 `name` 仍可编辑、字段顺序与原型不同（计划 §10 的既有 parity 缺口，未并）。

**合并与清理（owner 2026-11-22「合并回主树dev分支，清理掉worktree和其他分支」）**
- `git merge --ff-only config-subagents-page` → `dev` 由 `6cd9140c` 前进到 **`6541b8ab`**（未 push）。
- 合并后主树重跑门禁（评审方亲跑）：`pkg/config` 1.8s / `pkg/run` 44.5s / `pkg/turn` 4.1s / `pkg/tui` 197.9s / `pkg/gateway` 10.4s / `pkg/process` 235.5s / `pkg/architecture` 2.2s 全 `ok`；`go vet -tags fts5 ./...` exit 0；`gofmt -l` 无输出。
- worktree 与分支：`.worktrees/config-subagents-page`（已合并）、`.worktrees/config-agent-create-form-first`（`9b5c4891` 已在 dev 祖先）、`.worktrees/config-panel-prototype-parity`（`7517a281` 同样已合并）、三个 `plan-review-*` scratch worktree 全部移除；分支 `config-subagents-page` / `config-agent-create-form-first` / `config-panel-prototype-parity` 全删（`-d`，均已在 dev 里）。现在只剩主 worktree 与 `dev`。
- 原型采用：主树 `plans/config-tui-prototype.html` = `a6bc85c1…`、`.test.cjs` = `72140edc…`（worktree 版覆盖）；owner 2026-11-22 **确认采用并冻结**，旧版备份 `plans/*.pre-352` 已删除。
- 证据保留：plan 350 的真机取证从被移除的 parity worktree 移到 `plans/350-config-panel-parity-evidence/`（30 文件，120K，含 REPORT.md/REPRO.md）。
- 被移除的 scratch worktree 里还有若干未提交的探针文件（`zz_review_probe_test.go`、`config_panel_review_test.go` 及被改的 `chat_slash.go`/`config_panel.go` 等），按 owner 清理指令丢弃；其结论已进本计划与 351 计划的评审记录。

### 追加：Ctrl+U/Default 缺口收口 + 原型回归全绿（2026-11-22，owner 拍板「补原型」）

owner 问「Ctrl+U/Default 缺口是什么」并拍板**补原型**（不是删能力）：TUI 的编辑页有 `FooterActionDefault`（`configDefaultActionKey = "Ctrl+U"`、label `Default`，仅字段有默认值时提供），而原型只印了提示 `ctrl+u to default`、从未绑定该键，`plans/config-tui-prototype.test.cjs` 第 167 行找 `button "Default"` 必超时——整套原型回归因此长期停在那里。

**原型修补（4 处，都是「追上 TUI/loader 契约」，不是发明设计）**
1. 绑定 Ctrl+U：面板级 keydown（Escape 分支之后）→ 条件 `view==='edit'`、`editor.f.def!==undefined`、`f.kind!=='secret'` → 清空输入并走既有提交路径（= unset）；`focusEditor` 改为**同步聚焦**再补一帧 rAF（否则点开编辑器后键盘落空，Ctrl+U/输入都丢）。
2. 内置 pin 页清空/改模型时**连同删掉 `reasoning_effort`**（`stageValue` 的 sibling-drop 条件补 `topFrame().field?.subagentEntry`）——原来会留下一个 loader 拒绝的「有强度无模型」行。
3. `choicesFor` 的 `memoryModel` 补上空选项 `Follow conversation`（value `current`），与 TUI 的记忆模型 picker 一致。
4. `choicesFor` 的 `effort` 补上末位空选项 `Follow the model default`（与 TUI `configRowDefaultEffort` 同名同序，Off 仍按行的 off-capable 排在第一）。

**回归测试重写（9 条陈旧断言，全部按当前冻结契约重写并逐条探针取证）**：① Global per-type（段页 7 行、内置行在 Built-in subagents 页下、goal-evaluator 选 xHigh 落库、builtin-effective 的 pin/清除与「无模型则强度随模型消失」）② plan-reviewer 全局行（`draft.plan_reviewer`、清模型连坐删强度、无模型时拒绝开强度页、回显靠内置页状态列）③ Tab switch（`.tabs button` 按文本点击、布尔 True/False）④ Model picker filter（点击 `#filterInput` 再输入，断言 `state.query` 与收窄结果）⑤ Memory model pickers（空选项 `Follow conversation` + 落 `row-fast` + 值列）⑥ Config inventory（11 段：`subagent_models` 由段变字段；agents 7 字段）⑦ Model dependency（未 pin 的 public 类型拒绝开强度页 → 选模型后可设 → 清模型连坐删强度）+ secret 掩码 ⑧ No title line（`.tabs button` 计数 3、`sections[].tab` 无 file）⑨ Effort pickers（Anthropic 行无 Off、空选项叫 `Follow the model default` 不再叫 Row/Provider default、off-capable 行 Off 在首位）。

**结果**：`node plans/config-tui-prototype.test.cjs` → **exit 0，28 项全过，`browserErrors: []`**（首次跑到底）；8 张 `config-tui-prototype-*.png` 由套件自动重生成（此前一直停留在旧版）。评审另跑的 12 项 parity 探针在新版本上仍全过。

**新冻结对**：html `525fd4e6…`、test `14c53549…`（owner 已确认；随后因文案统一再次更新，见本节末）。

**文案统一（owner 2026-11-22 拍板）**：内置 pin 页 Model 的空选项由 `Follow conversation` 统一成 **`Follow primary agent`**——两个产品面（TUI `config_panel_schema.go:508`、web 的 `subagents.followPrimary` 8+ 处）本来就是这个词，改原型让三面一致；同一条描述符的帮助句也对齐成 TUI 的「This agent's override. Empty follows the install-wide type default, then the conversation.」。类型行/自定义条目仍写 `Follow conversation`（另一档语义：安装级默认 → 会话），不并。套件与 12 项探针在改后仍全绿。**最终冻结对**：html `e5245ee322aeb97262c608e9cda92f7b755968f2338dd1095ced9e3b39c52964`、test `6bfe73e15549f6ce5482ce8683da215d2186471ac34fa6a336df22f279c4566e`（owner 2026-11-22 晚确认为终稿）。