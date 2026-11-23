# /config › Agent definitions：`a` 直接出整表，底栏 `s Save` 一次落库（Primary 创建态可改）

- **Status:** **LANDED on dev**（`824c386b` → `9b5c4891`，ff-only，2026-11-22，未 push）+ **原型 agent-create 语义已同步**（`plans/config-tui-prototype.html` 新 sha256 `6234536d2ed5771828dfcd0ff9acfefcdeb7a9981bc5e0819723e90ca54093f9`、`.test.cjs` `033ea1121e3918a4d7d465aa1692aed4352151ec876822cd469c6980c5661eb6`）。**parity 已签收（2026-11-22）**：peer 落地的子代理半边与 agent-create 半边一并复核，最终冻结哈希 html `a6bc85c1…`、test `72140edc…`，owner 已确认。见文末 §Implementation Status 与「parity 签收」。
- **Priority:** P1（owner 报的交互 bug + owner 指定的一条跨端取名规则统一）
- **Category:** Bug / UX parity + 规则统一
- **Effort:** L（TUI 创建表单 + loader 规则统一 + web 抽屉 global scope + gateway 测试）
- **Risk:** MED（规则统一是**放宽**：定义 id 与共享子代理名允许下划线；自定义子代理条目名、web agent-scope 校验、存储格式、已保存页锁定契约都不动。新增校验没有变严的路径）
- **Planned at:** 工作区基线 = `dev` HEAD `7517a2810e6dee5a7ab508cd28b83dcbe8450897` + peer（`forebrain-harness-af`）在 `pkg/tui/*` 的**未提交 WIP**（含 `plainPage`/`actionPage` 的 `filter` 参数与 `configModelRowLabel`）。本计划不依赖 SHA；Step 1 记录 11 个 in-scope 文件的 `shasum` 作为漂移基线（§5）。
- **阻塞依赖：** 设计稿 `plans/config-tui-prototype.html` + `.test.cjs` 由 peer 统一改并重算冻结哈希（含本计划的 `s Save`、Esc 取消、共享子代理名规则与 Name 帮助句）；本计划的 DONE 需要「原型 agent-create 场景与 §4.2 矩阵一致」。peer **已解除**（2026-11-22：peer 落地 + owner 冻结，parity 见文末签收）。
- **执行方式:** 按 `plan-gated-bugfix` 技能，owner 批准后**直接在当前工作区源码实施**（不用 worktree、不 commit）。动手前按 `shasum` 与 peer 对齐（peer 的 WIP 正好在 `actionPage`/`plainPage`，本计划的切页签逻辑读这两个函数）。

---

## 0. owner 裁决（2026-11-22，全部已定，不再提问）

1. **创建表单交互**（owner 原话，经 peer 转达）：「创建主代理或子代理时，底部加表示提交的快捷键提示，按下快捷键所有字段一起落库。」即 `a` 直接出完整表单 → 所有字段只持有（内存，全程零写入）→ 底栏提交一次整段落库 → Esc 取消零写入 → loader 拒绝时保留表单与所有已填值并提示。
2. **提交键位/文案**：`s` + `Save`。`configCreateSaveKey = "s"`、`configCreateSaveLabel = "Save"` 是两个创建页的唯一来源。
3. **提交成功后落点**：回 **Agent definitions 列表**（新行带 Primary/Subagent 状态）。
4. **子代理角色的名字规则必须改成与主代理的名字规则一致**（owner 原话）。落法：loader 里定义 id 只留一个规则 `primaryAgentIDPattern`（`^[a-z][a-z0-9_-]{0,62}$`，允许下划线）；`validateDefinitionRoles` 的子代理专属 id 检查**删除**（`validatePrimaryAgents` 已经对每个 definition key 用这个规则检查过，见 §4.3a）；TUI 创建页两种角色同一句 `configPrimaryIDRefusal`；共享定义的引用条目豁免条目名 pattern（否则 `my_agent` 定义无法被任何 agent 启用）。
5. **切页签**：创建表单**有内容**时按 ←/→ 先确认 `Discard` / `Keep editing`；空表单直接离开；owner 已接受「Discard 后先回列表、页签切换延后到离开 section」。
6. **web 的 global scope 一并改**（owner 裁决）：`SubagentConfigDrawer` 的「新建共享子代理」也用主代理规则；agent scope（自定义条目）保持 dashes-only。
7. **Name 帮助句改成含 underscores**（owner 裁决）：TUI `config_panel_schema.go:446` 与原型那句同步改；peer 改原型。

---

## 1. 目标（Why this matters）

owner 原话：「Agent definitions 配置页的 agent 创建页面的 Primary 配置项无法修改，且交互 UX 与原型稿不一致，原型稿的设计是按下 a 快捷键，显示完整表单，现状是按下 a 快捷键，先显示 name 输入框，输入 name 按下 enter 键后显示完整表单，且 Primary 配置项无法修改。」

要修的三件事：

1. **`a` 之后第一屏就是完整表单**（Name 空、可编辑；Primary 显示 True、可切换），不再先弹名字输入框。
2. **创建会话里 Primary 可以改**（True ↔ False 决定新建的是主代理还是子代理定义），两种角色的取名规则一致（owner 裁决 4），并落到 TUI / loader / gateway / web 四个执行同一规则的地方。
3. **提交前零写入**：底栏 `s Save` 把所有字段一次落库；Esc 丢弃；loader 拒绝时表单与已填值原样保留。

**交付边界：** 本计划交付 **Agent definitions 的 `a` 创建页** + 它依赖的取名规则统一（loader / gateway 测试 / web global scope / TUI 提示）。主代理 Subagents 列表里的子代理 create/edit 是 peer 的同日计划，复用本计划的 `configCreateSaveKey`/`configCreateSaveLabel`、`templateItems(..., creating=true)` 与**自定义条目**的 dashes-only 规则；两个创建页的键位/文案必须一致。

原型依据 `plans/config-tui-prototype.html`（原型与 `.test.cjs` 由 peer 改并重算冻结哈希）：
- `startAdd()`（html:158，agent 分支）：`draft[key='']={primary:true}` → `state.view='fields'` → `pushFrame({...row.f, label:'New agent', createMode:true})` —— **整表立即出现**，不是输入框（末尾 `focusEditor()` 在 fields 视图找不到 `#valueInput`，是空操作）。
- `rowMarkup()`（html:164）：`locked` 对 `createMode` 下的 `keyName` / `createEditable` 有例外 —— 创建态下 Name 与 Primary 可编辑。
- `renameDefinition()`（html:150）：那句 `Use a lowercase agent ID with letters, numbers and hyphens.` 按 owner 裁决 7 由 peer 改成含 underscores 的版本；TUI 用现成的 `configPrimaryIDRefusal`（已含 underscores）。
- 草稿整条挂在空 key 上（`draft['']`）—— 本计划沿用同一形状：草稿挂在 section doc 的 `definitions[""]`。

---

## 2. 现场复现（当前源码，真机；本计划的第一个验证步骤）

```bash
export FOREBRAIN_RUN_WORK=$TMPDIR/fb-cfg-create FOREBRAIN_RUN_SESSION=fb-cfg-create FOREBRAIN_RUN_PORT=8764
.claude/skills/run-forebrain/driver.sh build          # 必须带 -tags fts5（driver 已带）
.claude/skills/run-forebrain/driver.sh reset; .claude/skills/run-forebrain/driver.sh start reply 'ok'
.claude/skills/run-forebrain/driver.sh send '/config'; .claude/skills/run-forebrain/driver.sh key Enter
# Agents & tools: Right ×2；Agents: Enter；Agent definitions: Enter；然后按 a
git status --porcelain > $TMPDIR/fb-cfg-create/baseline-status.txt
shasum pkg/tui/config_panel.go pkg/tui/config_panel_schema.go pkg/tui/config_panel_test.go pkg/tui/overlays.go \
       pkg/config/agents.go pkg/config/load.go pkg/config/agents_test.go pkg/gateway/subagents_api_test.go \
       frontend/src/components/subagents/SubagentConfigDrawer.vue frontend/src/components/subagents/SubagentConfigDrawer.test.ts \
       frontend/src/locales/index.ts \
  | tee $TMPDIR/fb-cfg-create/baseline-shasum.txt
```

实测（`driver.sh screen`）。按 `a` 后出现的是**编辑器页**而不是整表：

```
─────────────────────────────────────────────────────────────
  Approval & sandbox 3   Session & memory 4   Agents & tools 4
  New value

    ›

  Manage each primary agent and its own configuration.
  Enter to save Esc to cancel
```

输入 `ddd` + Enter 后直接落到**已保存定义页**（Name/Primary 都已锁定）：

```
❯ Name                                                       ddd
  Display name
  Description
  Primary                                                   True
  System prompt
  Model
```

在 Primary 上 Enter：

```
  Set when the definition was created; it cannot be changed.
❯ Name                                                       ddd
  ...
```

两个症状同源：名字一提交定义就已落库并跳到「已保存页」，而那一页按设计把 Name/Primary 锁死。**复现成立。**

---

## 3. 根因（file:line 证据链，行号为当前工作区）

全部在 `pkg/tui/config_panel.go`：

1. `:2936 addAgentDefinition` 是 agent 创建的唯一入口（`addRow:2773` → `configPrimaryCreationPath:2882`；agent 模板 `config_panel_schema.go:445`、`:449`）。
2. `:2943` 函数**第一条语句**就调用 `p.commitAgentName(...)`，在任何表单渲染之前。
3. `:2996` `commitAgentName` 内的 `p.editScalar(field, "")` 就是屏幕上那个 "New value" 名字编辑器（`editScalar:1127`；help 取**外层 definitions 字段**的 Hint "Manage each primary agent and its own configuration."）。→ **症状 1**。
4. `:3004-3024` 名字合法后：`rows[key] = draft` 再 `p.assign(doc, path, rows, ...)` —— 定义此时**整条落库**（`assign:1417` → `save:765`）。
5. `:2944-2946` `landed` 为真 → `return p.editTemplatePage(updated, configJoinPath(path, key), configTemplateAgent, key)` —— 交给**已保存定义页**的循环。
6. `:2363-2366` `editTemplatePage`：agent 模板上打开 `KeyName`/`CreateEditable` 行只回 `configCreatedFieldsNotice`（`:1716` "Set when the definition was created; it cannot be changed."），不打开编辑器、不写库。
7. `:2476` `locked := !configTemplateRowIsSystem(...) && (field.KeyName || field.CreateEditable) && key != ""` —— key 一旦非空，Name/Primary 渲染为 locked 行。→ **症状 2**。

对照原型：`startAdd()` 先出整表（`createMode` 让两行不锁），整条表单都是草稿；本实现把「取名」和「落库」绑成第一步，随后又把用户送进按设计锁死的页面。

**竞争解释（已用代码、现场与两轮独立评审探针排除）：**
- 不是渲染层把 `Locked` 当只读光标：`Locked` 只影响样式/可选中（`overlays.go:6384-6390`），打开被拒是 `:2363` 的显式分支。
- 不是 loader 拒绝改 `primary`：`pkg/config/load.go:1017` 让裸 `{primary:false}` 继续加载；真机上 Primary 根本没发写入（`:2363` 直接 continue）。
- 不是创建表单本身锁行：创建循环传给 `templateItems` 的 key 恒为 `""`（`:2949`），`locked` 的 `key != ""` 不成立；只是这个表单在名字提交后从未继续显示（`:2944` 提前 return）。
- 评审探针（`.worktrees/plan-review-1827214835`、`.worktrees/plan-review-1994318238` 的 `zz_review_probe_test.go`）复证了同一链条（首个编辑器 label "New value"、名字确认即 1 次写、随后 Name/Primary `locked`）；**本计划不依赖这些 worktree**，§2 的 driver 复现是权威入口。

---

## 4. 修复设计

### 4.1 语义（一句话）

`a` → 把草稿挂到 section doc 的 `definitions[""]`（原型 `draft['']`）并画出**完整表单**；Name / Primary / 其余每个字段都只改内存里的草稿（`p.staged` 让 `assign`/`assignModel` 跳过写库）；底栏 **`s Save`**（纯 picker 上退化为表单尾部的 `Save` 行）一次把整条定义写进 store，然后回列表；Esc 丢弃草稿（`p.sectionDoc` 重新解码，零写入）；loader 拒绝时把草稿挂回 `[""]` 并保留全部已填值；有内容时按 ←/→ 先确认 Discard / Keep editing。

**承重墙（唯一不变式）：** 创建会话期间不许有任何 store 写入；提交是**唯一**一次写。`p.staged` 是面板级开关，只由创建循环打开/关闭，`assign` 与 `assignModel` 两处 `p.save` 都必须认它。
**漏网的失败签名（响亮，不静默）：** 任何一条写路径漏认 `p.staged`，会把 `definitions[""]` 写进 store，loader 以 `agents.definitions.: invalid agent id` 拒绝——不会静默落库，但单测必须钉住「提交前 `session.writes` 为空」。

### 4.2 行为变化矩阵

| 场景 | 旧行为（当前） | 新行为（本计划） | 备注 |
|---|---|---|---|
| `a` 第一屏 | "New value" 名字编辑器 | 完整 agent 表单（Name 空、Primary True、其余字段空） | 原型 html:158 |
| 创建期间写库 | 名字一提交整条落库 | **零写入**；`fb_config.value_json` 在提交前不变 | 裁决 1 |
| Name 行 | 编辑后立刻落库并跳到已保存页 | 行内编辑器（help 用 keyName 的提示，预填当前 key）；校验（空 / 定义 id 规则 / 6 个保留名 / 重名）通过后**只持有** | 不再有「先命名」的门 |
| 定义 id 规则 | 每个 definition key 过 `primaryAgentIDPattern`；**带正文的子代理角色**再过一条更窄的 `subagentConfigNamePattern`（dashes-only、≥2） | 只剩 `primaryAgentIDPattern`（允许下划线、1–63 字符）；子代理专属那条删除 | 裁决 4 |
| 共享子代理名（web global scope / gateway） | web 抽屉与 gateway 测试都把 `repo_scout` 当非法 | 与定义 id 同规则：`repo_scout` 合法 | 裁决 4/6 |
| 自定义子代理条目（`subagents[]` 带 body）名字 | `subagentConfigNamePattern`（dashes-only、≥2） | **不变**（模型看到的 `subagent_type` 命名空间不变；web agent scope 也不变） | §0.4 |
| Name 帮助句 | `Lowercase letters, numbers and hyphens. This is the configuration key.` | `Lowercase letters, numbers, hyphens or underscores. This is the configuration key.` | 裁决 7 |
| 其余字段（Model/Thinking level/MCP servers/Subagents/Description/tools/…） | 名字落库前不可用（`Confirm the name first…`；落库后又进锁定页） | 与已保存页**同一套编辑器**，从一开始就能编辑，全部落进草稿 | 草稿挂在 `definitions//…`；元素路径照常解析模板 |
| Primary | 不可达（已保存页只回 created-fields 句子） | True/False 选择页；切换清空对方角色的已持值，提示 `configCreateRoleNotice`；纯内存 | 原型 createEditable |
| 提交（rich picker） | 无 | 底栏 `s Save`（新 footer action 种类），一次整段写 | 裁决 1/2 |
| 提交（无 footer 能力的 picker / 测试替身） | 无 | 表单尾部一行 `Save`（与 `↑ Back up one level` 并列），同一提交函数 | 既有 `tailRows` 模式 |
| 提交成功 | 名字一提交就进已保存页 | **回 Agent definitions 列表**，新行带 Primary/Subagent 状态 | 裁决 3 |
| 提交时 name 为空/非法/保留/重名 | 不由（名字先落库） | 表单顶部 notice 拒绝，0 写入，草稿保留 | `A key is required.` / `configPrimaryIDRefusal` / `configReservedIDRefusal` / `That key already exists. Choose a different key.` |
| 提交被 loader 拒绝 | 不可达 | 表单保留、所有已填值保留、显示 loader 原文；store 不变 | 裁决 1；机制由注入写错误的单测钉住 |
| Esc | 未命名：丢弃；已命名：定义已存在 | **任何情况都零写入**，草稿丢弃，列表无新行 | 裁决 1 |
| ←/→（切页签） | 直接离开、草稿丢弃 | 有内容 → 确认 `Discard` / `Keep editing`；Keep 留在表单；空表单直接离开；Discard 后先回列表、页签延后到离开 section | 裁决 5 |
| 离开后重开该定义 | Name/Primary 锁定 | 不变（锁定契约只属于已保存页） | `TestConfigPanelSavedDefinitionLocksItsCreatedFields` 保持绿 |
| 创建态改名 | 不可达 | Name 再提交只改持有的 key（同名不报错、不写库）；重名检查看 `definitions` 里除 `""` 外是否有该键 | |
| 取消某个字段编辑器 | **整个创建表单退出**（草稿丢弃） | 回到表单，草稿保留 | `openTemplateField` 的编辑器本来就只返回原 doc |

**被删除的旧语义（不保留兼容）：** `editAgentDraftField`、`configCreateHeldNotice`、`configCreateNameFirstNotice`、「名字一提交即落库 + 跳 `editTemplatePage`」的分支。**`configKeyNameHint`（`config_panel.go:2891`）保留不动**：它写的是自定义子代理条目的 dashes-only 规则（"Lowercase letters, numbers and hyphens. This is the configuration key."），peer 的子代理创建页要复用它。

### 4.3 实现细节

**(a) loader 取名规则统一（`pkg/config`）**

事实先摆正：`validatePrimaryAgents:1049-1056` 对**每个** definition key 都跑 `primaryAgentIDPattern`；`validateDefinitionRoles:1021` 只是对「带子代理正文」的定义再加一条更窄的规则。所以「裸 `{primary:false}` 存 `my_agent` 已经能存」是因为主代理规则允许下划线，而不是没有校验；死路的成因是**之后加正文**时才被窄规则拒，而 Name 已锁定。

1. `load.go` 的 `validateDefinitionRoles`：**删除** id 检查那一段（`if !subagentConfigNamePattern.MatchString(id) { … }`），留一句注释说明「定义 id 由 `validatePrimaryAgents` 用同一个 `primaryAgentIDPattern` 判，两种角色没有第二条规则」。旧句 `invalid subagent id …` 随之消失。
2. `load.go:1091` `ValidPrimaryAgentID` 注释改成「the one id rule: agents.definitions keys — primary and subagent role alike」。
3. `agents.go` 的 `ValidateAgentSubagents`：把 name pattern 检查移到分类之后，共享引用豁免：
   ```go
   name := strings.ToLower(strings.TrimSpace(entry.Name))
   if _, dup := seen[name]; dup { … }        // 原样
   seen[name] = struct{}{}
   if _, ok := system[name]; ok { … }        // 原样
   _, publicBuiltin := public[name]
   sharedReference := slices.Contains(sharedNames, name) && !SubagentEntryCarriesBody(entry)
   if !sharedReference && !subagentConfigNamePattern.MatchString(name) {
       return fmt.Errorf("%s: invalid name %q: use lower-case letters, digits and dashes, starting with a letter", where, entry.Name)
   }
   ```
   其余分支（built-in pin / shared reference / custom body）不变。`subagentConfigNamePattern` 保留（自定义条目的规则）。
4. 测试（`pkg/config/agents_test.go`）：`repo_auditor`（`:981`）由拒绝改为**合法**；真正非法用 `Repo_Auditor`（大写，两个规则都拒）→ `agents.definitions.Repo_Auditor: invalid agent id`；加 `my_agent` 子代理角色定义可加载；共享引用用例加在既有的 `TestValidateAgentSubagentsSharedReferenceEntries`（`:784`）：`{name: my_agent, enabled: true}` 可加载；保留「自定义条目 `my_agent`（带 body）仍被 `invalid name … digits and dashes` 拒绝」。

**(b) gateway 测试（`pkg/gateway/subagents_api_test.go`）**
`TestSharedSubagentCreateRefusals` 的「a name the loader rejects」用例：`repo_scout` 现在是合法名，换成 `repo.scout`（两个规则都拒）并期望 `invalid agent id`；后续「成功创建 repo-scout」与列表顺序断言不变（该用例未成功创建过 `repo_scout`）。

**(c) web 抽屉（global scope 用主代理规则，agent scope 不变）**

`frontend/src/components/subagents/SubagentConfigDrawer.vue`：
```ts
const AGENT_NAME_PATTERN = /^[a-z][a-z0-9-]{1,62}$/   // 自定义条目：不变
const SHARED_NAME_PATTERN = /^[a-z][a-z0-9_-]{0,62}$/ // 共享定义：与主代理规则一致
const namePattern = computed(() => (globalScope.value ? SHARED_NAME_PATTERN : AGENT_NAME_PATTERN))
const nameValid = computed(() => namePattern.value.test(form.name.trim()))
```
- 名字输入框的 placeholder 与 invalid 提示按 scope 取 key：`creating ? t(globalScope.value ? 'subagents.sharedNamePlaceholder' : 'subagents.namePlaceholder') : ''`；invalid 同理。
- `frontend/src/locales/index.ts`（zh 与 en 各两条）：
  - zh：`'subagents.sharedNamePlaceholder': '小写字母、数字、短横线或下划线，如 repo_scout'`、`'subagents.sharedNameInvalid': '只能用 a–z、0–9、短横线与下划线，以字母开头，最长 63 个字符'`
  - en：`'Lowercase letters, digits, hyphens or underscores, e.g. repo_scout'`、`'Use a–z, 0–9, hyphens or underscores, starting with a letter, up to 63 characters'`
- `SubagentConfigDrawer.test.ts`：加一条 global-scope create 用例——`my_agent` 不出 `subagent-name-invalid`，agent scope 的 `my_agent` 出提示（证明两 scope 规则不同）；既有 `DB_Migrator` 用例（两个规则都拒）不变。

**(d) TUI Name 帮助句（`config_panel_schema.go:446`）**
`Hint: "Lowercase letters, numbers, hyphens or underscores. This is the configuration key."`。`config_panel.go:2889-2891` 的 `configKeyNameHint` **保留**（自定义条目的规则，peer 复用）；`TestConfigPanelPrototypeCopy` 不 pin 这一句（已核），无需改。

**(e) `configPanel` 加 `staged bool`**（`config_panel.go:151` 结构体；修掉「the panel holds no draft of its own」注释）
```go
// staged holds every write: the create form edits its whole definition in
// the section document and commits it in one write (owner 2026-11-22).
// While it is set, assign/assignModel stage the value in the document and
// never reach the store; the create form's submit clears it for that one
// write.
staged bool
```

**(f) 两处 `p.save` 认 staged**：`assign:1411` 在 `configAssign` 之后、`p.save` 之前加 `if p.staged { return updated, "" }`；`assignModel:1354` 在 `configAssign` + `configDropSiblingEffort` 之后同样处理。（全仓只有这两处 `p.save(`。）

**(g) `templateItems` 加 `creating` 参数**（`:2429`）
```go
func (p *configPanel) templateItems(template, key string, settings map[string]any, tailRows, creating bool) ([]SelectItem, []configTemplateRow)
...
locked := !configTemplateRowIsSystem(template, settings) && (field.KeyName || field.CreateEditable) && key != "" && !creating
```
调用点共 3 处（生产 2 处 + 测试 1 处）：`editTemplatePage:2339` → `false`；`addAgentDefinition` → `false, true`；`config_panel_test.go:1943`（`panel.templateItems(`）→ `false`。

**(h) 新 footer action 种类 `FooterActionSave`**（`overlays.go`）：`FooterActionKind` 常量组加一个值（注释：表单提交，键由 action 自己的 `Key` 决定）；`fireAction` 的 switch 加：
```go
		case FooterActionSave:
			if kind != rawKeyRune || !p.noFilter || a.Key == "" || string(r) != a.Key {
				continue
			}
```
`footerHint` 已通用渲染 `Key + " " + Label`；`/config` 页 `noFilter` 为 true。

**(i) 创建循环 `addAgentDefinition`（`:2936`）**
```go
func (p *configPanel) addAgentDefinition(doc any, path []string, field configField, container any) (any, string) {
	_ = container // 仍由 addRow 的签名传入；草稿一律从 doc 的 definitions[""] 读
	draft := map[string]any{"primary": true}
	doc = p.stageAgentDraft(doc, path, draft)
	p.staged = true
	defer func() { p.staged = false }()
	actions := p.configActionSelector()
	key, notice := "", ""
	for {
		items, rows := p.templateItems(configTemplateAgent, key, draft, false, true)
		saveRow := -1
		if actions == nil {
			// A picker without footer actions: the submit and the way out
			// are rows, the way the plain rows pages carry them.
			saveRow = len(rows)
			items = append(items,
				SelectItem{Label: configCreateSaveLabel, Description: configCreateSaveDesc},
				SelectItem{Label: configUpRowLabel, Description: "Back up one level"})
		}
		// Only an arrow this page takes counts: a nested page (rows/editor)
		// may leave its own tab step behind, and that must not read as the
		// user leaving the form.
		p.tabStep = 0
		var res ActionResult
		var err error
		if actions == nil {
			var idx int
			var ok bool
			idx, ok, err = p.page(notice, configCreateFormKeys, items, -1)
			res = ActionResult{Row: idx, OK: ok}
		} else {
			res, err = p.actionPage(notice, configCreateFormKeys, items, -1,
				FooterAction{Label: configCreateSaveLabel, Key: configCreateSaveKey, Kind: FooterActionSave})
		}
		if err != nil {
			p.printError(err)
			return p.discardAgentDraft()
		}
		notice = ""
		if p.tabStep != 0 {
			// A tab arrow must not throw a filled form away unasked.
			if p.agentDraftUntouched(draft, key) || p.confirmDiscardAgentDraft() {
				return p.discardAgentDraft() // p.tabStep stays set: the panel steps
			}
			p.tabStep = 0 // Keep editing: stay on the form
			continue
		}
		switch {
		case res.Action == FooterActionSave || (saveRow >= 0 && res.OK && res.Row == saveRow):
			var landed bool
			doc, notice, landed = p.submitAgentDraft(doc, path, draft, key)
			if landed {
				return doc, "" // back to the Agent definitions list (owner 2026-11-22)
			}
		case res.OK && res.Row >= 0 && res.Row < len(rows):
			row := rows[res.Row]
			switch {
			case row.field.KeyName:
				key, notice = p.holdAgentName(doc, path, row.field, draft, key)
			case row.field.CreateEditable:
				notice = p.editAgentDraftPrimary(draft)
			default:
				doc, notice = p.openTemplateField(doc, configJoinPath(path, ""), row.field)
			}
		default:
			// Esc (or the plain page's back-up row): nothing was written.
			return p.discardAgentDraft()
		}
	}
}
```
- `row.unknown` 不处理：草稿只含模板命名的键，create 会话长不出未知行；真机若出现 → STOP（§8.7）。
- 嵌套页里按 ←/→ 留下的 `p.tabStep` 由每轮开头清零吃掉（今天这些嵌套页的箭头本来也不切页签），只有表单页自己的箭头触发确认。

**(j) 新助手（替换 `commitAgentName` / `editAgentDraftPrimary` / `editAgentDraftField`）**
```go
// stageAgentDraft puts the create draft under the unnamed key the prototype
// uses (startAdd's draft['']), without a write: every editor then works on a
// real path inside the document, and the submit is the only write.
func (p *configPanel) stageAgentDraft(doc any, path []string, draft map[string]any) any {
	defs, _ := configLookupValue(doc, path)
	rows, _ := defs.(map[string]any)
	if rows == nil { rows = map[string]any{} }
	rows[""] = draft
	updated, err := configAssign(doc, path, rows, false, "Agent definitions")
	if err != nil {
		p.printError(err)
		return doc
	}
	return updated
}

// discardAgentDraft leaves the create with nothing written: the section is
// read back from the store, so the unnamed draft never reaches a page.
func (p *configPanel) discardAgentDraft() (any, string) {
	return p.sectionDoc(p.section), ""
}

// agentDraftUntouched reports a form nobody typed into: no name and the seed
// alone. A role flip on its own counts as untouched — it costs one keystroke
// to redo, and an accidental arrow must not raise a dialog.
func (p *configPanel) agentDraftUntouched(draft map[string]any, key string) bool {
	return key == "" && len(draft) == 1
}

// confirmDiscardAgentDraft asks before a tab arrow throws a filled create form
// away; Keep editing (preselected) stays on the page (owner 2026-11-22).
func (p *configPanel) confirmDiscardAgentDraft() bool {
	items := []SelectItem{
		{Label: configDiscardDraftLabel, Danger: true},
		{Label: configKeepEditingLabel},
	}
	idx, ok, err := p.page("Discard the new definition?\nNothing was written.", configConfirmKeys, items, 1)
	if err != nil { p.printError(err); return false }
	return ok && idx == 0
}

// agentNameRefusal is the one place a create's name is judged: the empty key,
// the one id rule every definition id follows (the loader's own, owner
// 2026-11-22), the six reserved names, and a duplicate.
func (p *configPanel) agentNameRefusal(doc any, path []string, name string, draft map[string]any) string {
	if name == "" { return "A key is required." }
	if !appcfg.ValidPrimaryAgentID(name) { return configPrimaryIDRefusal }
	if appcfg.IsReservedDefinitionID(name) { return configReservedIDRefusal }
	defs, _ := configLookupValue(doc, path)
	if rows, ok := defs.(map[string]any); ok {
		if _, dup := rows[name]; dup { return "That key already exists. Choose a different key." }
	}
	return ""
}

// holdAgentName asks for the definition's name and holds it with the form —
// no write (owner 2026-11-22). The editor opens on the key the form already
// holds, so re-opening it is how a rename is made.
func (p *configPanel) holdAgentName(doc any, path []string, field configField, draft map[string]any, key string) (string, string) {
	name, ok, _, err := p.editScalar(field, key)
	if err != nil { p.printError(err); return key, "" }
	if !ok { return key, "" }
	next := strings.TrimSpace(name)
	if notice := p.agentNameRefusal(doc, path, next, draft); notice != "" { return key, notice }
	return next, ""
}

// editAgentDraftPrimary flips the draft's role in place and clears the other
// role's values (the prototype's stageValue): held with the form, no write.
func (p *configPanel) editAgentDraftPrimary(draft map[string]any) string {
	primary := configAgentRolePrimary(draft)
	items := []SelectItem{{Label: configBoolOnLabel}, {Label: configBoolOffLabel}}
	defaultIdx := 1
	if primary {
		items[0].Status = configChoiceCheckStatus
		defaultIdx = 0
	} else {
		items[1].Status = configChoiceCheckStatus
	}
	idx, ok, err := p.page("", configChoiceKeys, items, defaultIdx)
	if err != nil { p.printError(err); return "" }
	if !ok || idx < 0 || idx > 1 { return "" }
	next := idx == 0
	if next == primary { return "" }
	draft["primary"] = next
	for _, k := range configPrimaryOnlyKeys  { if !next { delete(draft, k) } }
	for _, k := range configSubagentOnlyKeys { if next { delete(draft, k) } }
	return configCreateRoleNotice
}

// submitAgentDraft is the create's one write (owner 2026-11-22: the footer's
// save lands every field together). A refusal from the loader puts the draft
// back under its unnamed key, so the form keeps every value the user typed.
func (p *configPanel) submitAgentDraft(doc any, path []string, draft map[string]any, key string) (any, string, bool) {
	if notice := p.agentNameRefusal(doc, path, key, draft); notice != "" {
		return doc, notice, false
	}
	defs, _ := configLookupValue(doc, path)
	rows, _ := defs.(map[string]any)
	delete(rows, "")
	rows[key] = draft
	previous := p.staged
	p.staged = false
	updated, note := p.assign(doc, path, rows, false, "Agent definitions")
	p.staged = previous
	if note == "" {
		return updated, "", true
	}
	// Reload first: a configAssign failure (never for a map path) would have
	// left the old tree half-moved, and re-staging on top of it would keep
	// both keys.
	return p.stageAgentDraft(p.reloadSection(), path, draft), note, false
}
```

**(k) 常量**（`config_panel.go` 顶部 const 区）
```go
	// configCreateSaveKey / configCreateSaveLabel are the create forms'
	// submit: the footer shortcut that writes the whole definition in one go
	// (owner 2026-11-22), and the plain picker's row for the same act. Both
	// create forms (agent and subagent) share these.
	configCreateSaveKey   = "s"
	configCreateSaveLabel = "Save"
	configCreateSaveDesc  = "Writes the new definition"
	// configDiscardDraftLabel / configKeepEditingLabel answer a tab arrow on a
	// filled create form.
	configDiscardDraftLabel = "Discard"
	configKeepEditingLabel  = "Keep editing"
```
```go
// configCreateFormKeys is the create form's footer: Enter opens a row, Esc
// discards the whole draft — nothing is written until Save.
var configCreateFormKeys = &configPageKeys{Enter: "open", Esc: "cancel"}
```
删除 `configCreateHeldNotice`、`configCreateNameFirstNotice`；**不加** `configSubagentIDRefusal`（裁决 4：两种角色同一句 `configPrimaryIDRefusal`）。

---

## 5. Scope

**In scope（11 个文件）：**
- `pkg/tui/config_panel.go`：`staged` 字段 + 结构体注释、`assign`/`assignModel` 的 staged 分支、`templateItems`（+`creating`）、`addAgentDefinition` 重写、六个助手、删除 `commitAgentName`/`editAgentDraftField`/两个旧 notice、新常量（`configKeyNameHint` 保留给 peer）。
- `pkg/tui/config_panel_schema.go`：Name 字段 Hint 改含 underscores。
- `pkg/tui/overlays.go`：`FooterActionKind` 加一个值 + `fireAction` 加一个 case（只这两处）。
- `pkg/tui/config_panel_test.go`：§6 Step 6 的用例；`templateItems` 测试调用点补参；`actionStep` 加 `tabStep int` 与替身回填。
- `pkg/config/load.go`：删 `validateDefinitionRoles` 的子代理 id 检查 + `ValidPrimaryAgentID` 注释。
- `pkg/config/agents.go`：`ValidateAgentSubagents` 的 name 检查移到分类之后并对共享引用豁免。
- `pkg/config/agents_test.go`：§4.3(a)-4 的用例调整。
- `pkg/gateway/subagents_api_test.go`：`TestSharedSubagentCreateRefusals` 的非法名用例改 `repo.scout` / 期望 `invalid agent id`。
- `frontend/src/components/subagents/SubagentConfigDrawer.vue`：按 scope 的两条 name pattern + 按 scope 的文案 key。
- `frontend/src/components/subagents/SubagentConfigDrawer.test.ts`：global scope 的 `my_agent` 用例 + agent scope 的对照。
- `frontend/src/locales/index.ts`：zh/en 各两条 `subagents.sharedNamePlaceholder` / `subagents.sharedNameInvalid`。

**Out of scope（明确不碰）：**
- 主代理 Subagents 列表的创建/编辑、内置类型 synthetic 行、`editRows`/`rowItems`/`configRowIsEntry`/`configRowLabelAt`/`configRowColumns`/`addRow` —— peer 领地（§9）。
- `subagentConfigNamePattern` 本身、自定义子代理条目的名字规则、web agent scope 的 `NAME_PATTERN` 语义（值保留）。
- `plans/config-tui-prototype.html` / `.test.cjs`：peer 统一改并重算冻结哈希（含 `s Save`、Esc 取消、共享名规则、Name 帮助句）。
- 已保存定义页的锁定契约、`configCreatedFieldsNotice` 文案、`configTemplates` 字段表、存储/迁移、其它 web 设置页。
- 其它 footer action 的键位（Add/Remove/Default 保持 Ctrl+A/Ctrl+D/Ctrl+U 与既有字母规则）。

**不可改变的公共形状：** `addRow(doc, path, field)` 签名与调用、`configTemplateRow` 结构、`configAssign`/`configLookupValue` 语义、`SelectItem.Locked` 的渲染含义、既有 `FooterActionAdd/Remove/Default` 的触发键、`subagentConfigNamePattern` 的取值、`ValidPrimaryAgentID` 的名字。

**构建产物说明：** `pkg/gateway/dist/*` 是 `frontend/vite.config.ts` 的 `outDir` 且被 `.gitignore` 忽略；Step 7 的 `pnpm build` 会刷新它，但不会出现在 `git status`，也不影响 §7「源码改动 ⊆ 11 个 in-scope 文件」的判据。

**Drift check（每次动手前，按内容不按行号；Step 1 的 `shasum` 对不上即 STOP）：**
- `addAgentDefinition` 首句仍是 `draft := map[string]any{"primary": true}` 且紧跟 `p.commitAgentName(`；
- `commitAgentName` 里有 `rows[key] = draft`；`editAgentDraftPrimary` 里有 `p.page(configCreatedFieldsNotice, ...)`；`editAgentDraftField` 仍含 `configCreateNameFirstNotice`；
- `templateItems` 仍是 4 参且 `locked :=` 行含 `key != ""`；`grep -n "p\.templateItems(" pkg/tui/*.go` 恰好 **2 处**（`config_panel.go:2339`、`:2949`；测试那行是 `panel.templateItems(`，由 `grep -n "panel\.templateItems(" pkg/tui/config_panel_test.go` 单独锚定 1 处）；`grep -n "p\.save(" pkg/tui/*.go` 恰好 **2 处**；
- `overlays.go` 的 `FooterActionKind` 仍是三个值、`fireAction` 的 switch 仍是三个 case；
- `pkg/config/agents.go` 逐字仍含 ``var subagentConfigNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)``，且 `grep -c "subagentConfigNamePattern\." pkg/config/*.go` 现在是 **2**（`agents.go` 的条目检查、`load.go` 的定义检查）——改完应为 **1**（只剩条目检查）；
- `grep -rn "invalid subagent id" pkg` 今天 **3 处**（`pkg/config/load.go:1022` 将删的那句、`pkg/config/agents_test.go:981` 将改写的 want、`pkg/gateway/subagents_api_test.go:1230`），改完为 **0 处**；
- `ValidateAgentSubagents` 的 name pattern 仍在 shared 分类之前；
- `frontend/src/components/subagents/SubagentConfigDrawer.vue` 仍是单一 `const NAME_PATTERN = /^[a-z][a-z0-9-]{1,62}$/`。

---

## 6. Steps（每步都以验证结束）

**Step 1 — 复现 + 基线（改代码前，硬性）**
按 §2 跑一遍并保存每屏 `driver.sh screen`、`git status --porcelain`、11 个文件的 `shasum`。
预期：按 `a` 出现 "New value"；输入名字后跳到已保存页；Primary 上 Enter 回 "Set when the definition was created; it cannot be changed."。
不符 → STOP（§8.1）。与 peer 对齐 shasum（他们的 WIP 在 `actionPage`/`plainPage`）。

**Step 2 — loader 取名规则统一（§4.3 a）**
删 `validateDefinitionRoles` 的 id 检查；改 `ValidateAgentSubagents` 顺序与豁免；`ValidPrimaryAgentID` 注释；两个测试文件里的用例。
验证：
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/config ./pkg/gateway -run 'Test.*Definition|TestValidPrimaryAgentID|TestValidateAgentSubagents|TestSharedSubagentCreateRefusals' -count=1   # 预期 ok
gofmt -l cmd pkg third_party plugins    # 空
```

**Step 3 — staged 写入开关（§4.3 e/f）**
验证：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` exit 0；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestConfigPanelCreatesAnAgentDefinition -count=1` 仍 **PASS**（行为未变）。

**Step 4 — `FooterActionSave` + Name 帮助句（§4.3 d/h）**
验证：`go build ./pkg/tui` exit 0；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestConfigPanel|TestPicker|TestSelect' -count=1` → ok。

**Step 5 — 重写创建循环（§4.3 g/i/j/k）**
验证：`gofmt -l cmd pkg third_party plugins` 空；`CGO_ENABLED=1 go build -tags fts5 ./...` exit 0。

**Step 6 — TUI 单测（`pkg/tui/config_panel_test.go`）**

先给替身加 `tabStep`：`actionStep` 加 `tabStep int`，`actionPanelSelector.SelectRichActions` 在 `step.action == footerActionNone` 时返回 `ActionResult{Row: -1, OK: step.ok, TabStep: step.tabStep}`（`:142-164`；in-scope）。

**显式处置旧子用例：**
- `:2371`「the name lands the draft as the definition」→ **删除**（被「the create form comes first」+「the save writes it whole」取代）。
- `:2414`「a definition created through the subagent role carries its body alone」→ **改写**为「the save writes the subagent body whole」（名字 + 正文字段一起提交，一次写含 `"repo-auditor":{"description":…,"primary":false,"system_prompt":"You audit repositories."}`）。
- `:2447`「flipping the role clears the other role's held values」→ **改写**为「Primary flips the role in memory」：脚本必须真的填过子代理正文字段再翻回 True——`{label:"Primary"}, {label:configBoolOffLabel}, {label:"Tool access"}, {label:"Pick"}, {label:"Primary"}, {label:configBoolOnLabel}, {label:configCreateSaveLabel}`，断言「被清掉的 `"tools"` 不出现在写库内容里」。
- `:2482`「a field held before the name…」、`:2494`「a field that needs the saved section…」→ **删除**（语义已不存在，连同两个旧 notice 的断言）。

改写/新增（`newCase` → 纯 picker，提交靠尾部 `Save` 行；`selectorInput` 是位置脚本）：

| 子用例 | richStep 序列（`openDefinitions, addRow` 之后） | inputs | 断言 |
|---|---|---|---|
| the create form comes first | 无（随后 Esc、Esc） | 无 | 按行标签找含 `Primary` 的那一页（`findRowLoose` 写法，不用下标）：存在，`Name` 行 `Value==""` 且 `Locked==false`；0 次写 |
| the save writes it whole | `{label:"Name"}`, `{label:"Primary"}`, `{label:configBoolOffLabel}`, `{label:"Description"}`, `{label:"System prompt"}`, `{label:configCreateSaveLabel}` | `"repo-auditor"`, `"Audits repos."`, `"You audit repositories."` | 1 次写，raw 含 `"repo-auditor":{"description":"Audits repos.","primary":false,"system_prompt":"You audit repositories."}`（全程只此一次，即证明提交前零写入） |
| Primary flips the role in memory | 见上（含 Tool access/Pick 与翻回 True） | `"review-agent"` | `pageLabels` 含 `configCreateRoleNotice`；1 次写含 `"review-agent":{"primary":true}`，不含 `"tools"` |
| a name is required to save | `{label:configCreateSaveLabel}` | 无 | `A key is required.`；0 次写 |
| an id the loader refuses is refused here | `{label:"Name"}`, `{label:configCreateSaveLabel}` | `"Bad ID"` | `configPrimaryIDRefusal`；0 次写；另加子代理角色 `"my_agent"` → 提交成功、1 次写含 `"my_agent"` |
| a reserved subagent name is refused in both roles | 主代理与子代理两种角色 | `"explore"` | `configReservedIDRefusal`；0 次写 |
| a duplicate key is refused | `{label:"Name"}`, `{label:configCreateSaveLabel}` | `"main"` | 重名句子；0 次写 |
| Esc discards without a write | 无（Esc、Esc） | 无 | 0 次写；列表页 `definitions` 行数不变 |
| a tab arrow asks before discarding a filled form | `{label:"Name"}`, `actionStep{tabStep:1, ok:true}` → 确认页 `{label:configKeepEditingLabel}` → `{label:configCreateSaveLabel}` | `"worker"` | 选 Keep 后仍在表单（`pageLabels` 含 `Discard the new definition?`），之后提交成功：1 次写含 `"worker"` |
| a rejected submit keeps the form and every value | **不用 `newCase`**：自己 `session := newConfigPanelSession(...)`，先 `session.writeErr = errors.New("agents.definitions.worker: invalid agent id")`，再 `runConfigPanel`；步骤 `{label:"Name"}`, `{label:"Description"}`, `{label:configCreateSaveLabel}`, Esc, Esc | `"worker"`, `"Audits repos."` | 0 次记录写；`pageLabels` 含 loader 错误句；错误后那一页 `Name` 行 `Value=="worker"`、`Description` 行 `Value=="Audits repos."` |

新增（rich picker / footer action 路径，用 `runActionConfigPanel` + `actionPanelSelector`）：
- **底栏 `s Save` 提交**：`actionStep{label:"Name", ok:true}` → `inputActionStep{value:"worker", ok:true}` → `actionStep{action: FooterActionSave, ok:true}` → 1 次写含 `"worker":{"primary":true}`；并断言该页 `opts.FooterActions` 含 `{Label: configCreateSaveLabel, Key: configCreateSaveKey, Kind: FooterActionSave}`。
- **`creating` 直接单测**：仿 `config_panel_test.go:1943`：`panel.templateItems(configTemplateAgent, "worker", map[string]any{"primary":true}, false, true)` → `Name`/`Primary` 两行 `Locked==false`；`creating=false` → 两行 `Locked==true`。
- **保存页仍锁定**（可选端到端）：创建 `"worker"` 提交后回列表、重开该行、打开 `Name` → `configCreatedFieldsNotice`。

验证：
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestConfigPanelCreatesAnAgentDefinition|TestConfigPanelSavedDefinitionLocksItsCreatedFields' -count=1   # 预期 ok
```

**Step 7 — web 抽屉（§4.3 c）**
改 drawer 的两条 pattern + 按 scope 的文案 key + zh/en 四条 + 抽屉测试。
验证：
```bash
cd frontend && pnpm typecheck && pnpm vitest run src/components/subagents/SubagentConfigDrawer.test.ts && pnpm build   # 预期全绿
```

**Step 8 — 包级回归**
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/config -count=1         # 预期 ok
CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1        # 预期 ok（含 TestSharedSubagentCreateRefusals）
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1            # 预期 ok（全包 ~200s）
CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1   # 预期 ok
gofmt -l cmd pkg third_party plugins                           # 预期无输出
CGO_ENABLED=1 go vet -tags fts5 ./...                          # 预期 exit 0
```
归因规则：`pkg/tui` 若红在**未触碰的代码**（peer WIP：`plainPage` 的 filter 参数、`configModelRowLabel`、model picker 用例），把失败原样发给 peer，不算本计划失败；本计划的门是 Step 6 的定点用例 + 其余用例。禁止为了让全包变绿去改 peer 的改动。

**Step 9 — 真机验收（tmux，隔离 home/project；重新 `driver.sh build`，防陈旧二进制）**
1. `a` → 第一屏即整表（`Name` 空、`Display name`、`Description`、`Primary True`、`System prompt`、`Model`…），**没有 "New value" 编辑器页**；底栏出现 `s Save`。
2. 打开 `Name` → 输入 `review-agent` → Enter → 表单仍在，`Name` 行显示 `review-agent`；help 是含 underscores 的那句。
3. 仍未写库：`driver.sh db "SELECT value_json FROM fb_config WHERE section='agents'"` 与 Step 1 的对照一致。
4. 打开 `Primary` → `True`/`False` 选择页（无 created-fields 句子）→ 选 `False` → 表单切到子代理字段；仍 0 写入。
5. 打开 `MCP servers`（或 `Description`）→ 真实编辑器/rows 页可用（不是 "Confirm the name first"）→ 填一个值后 Esc 回表单，值仍在草稿里。
6. 按 `s` → 一次落库：`value_json` 里 `review-agent` = `primary:false` + 已填字段；只有一条；**回 Agent definitions 列表**，新行带 `Subagent` 状态。
7. 重开该定义 → `Name`/`Primary` 锁定，Primary 上 Enter 回 created-fields 句子。
8. 取消：再按 `a`，填名字后 Esc → 列表无新行，`value_json` 不变。
9. 名字规则一致：按 `a` → `Primary` 选 `False` → `Name` 输入 `my_agent` → Enter 通过 → `s` → 成功落库；再按 `a` → `Name` 输入 `Bad ID` → 拒绝句、0 写入。
10. 切页签：按 `a` → 填 Name → `→` → `Discard`/`Keep editing` 问句；`Keep editing` 留在表单；再 `→` → `Discard` → 回列表、`value_json` 不变。
11. 真实 loader 拒绝（best-effort）：创建页里 `MCP servers` 加一个没有 command/url 的 server → `s` → 若 loader 拒绝，表单保留全部值并显示 loader 原文；若该形状被接受，记录实际结果并改用 `subagents` 里一个 system 分派类型名触发。**机制本身的权威证据是 Step 6 的注入写错误单测**（同一条 `assign → reloadSection → 重新挂草稿` 路径）。
每屏 `driver.sh screen` 存证；结束 `driver.sh stop`。

**Step 10 — 收口**
- 计划（批准后落 `plans/351-config-agent-create-form-first.md`）回填状态与「验收记录」；`plans/README.md` 加索引行（TODO → DONE 或 BLOCKED + 验收日期）。
- `git status --porcelain` 与 Step 1 基线比对：不得出现新增路径；源码改动 ⊆ 11 个 in-scope 文件；**不 commit**。
- **原型核对**：peer 改完原型 + `.test.cjs` 后，逐条核对 agent-create 场景与 §4.2 矩阵（整表、Name/Primary 可编辑、底栏 `s Save`、Esc 取消零写入、切页签确认、Name 帮助句、共享名规则），新冻结哈希记入验收记录；不一致 → STOP（§8.8）。
- `peer_send` 通知 `forebrain-harness-af`：`templateItems` 最终签名、四个常量名、`overlays.go` 改动区间、loader/网关/web 的新规则与两句文案（他们页面若引用旧句要改）。

---

## 7. Done criteria（机器可查）

- [x] `CGO_ENABLED=1 go test -tags fts5 ./pkg/config -count=1` → `ok`（2026-11-22 合并后评审亲跑 1.8s）
- [x] `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` → `ok`（评审亲跑 10.4s）
- [x] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestConfigPanelCreatesAnAgentDefinition|TestConfigPanelSavedDefinitionLocksItsCreatedFields' -count=1` → `ok`（评审亲跑 0.7s）
- [x] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`（评审亲跑 197.9s，含 peer 的 352 批次）
- [x] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`（评审亲跑 2.2s；全新 worktree 需先拷 gitignored 的 `pkg/gateway/dist`）
- [x] `gofmt -l cmd pkg third_party plugins` → 空（评审亲跑）
- [x] `CGO_ENABLED=1 go vet -tags fts5 ./...` → exit 0（评审亲跑）
- [x] `cd frontend && pnpm typecheck && pnpm vitest run src/components/subagents/SubagentConfigDrawer.test.ts && pnpm build` → 全绿（评审亲跑：typecheck 无输出、抽屉 14/14 passed、build 9.43s）
- [x] 真机验收 11 个场景逐屏留证：`a` 第一屏无 "New value"；提交前 `value_json` 不变；`s` 一次落库且回列表；Esc 零写入；`my_agent` 子代理定义可建；`→` 先确认（执行器 2026-11-22 逐屏取证，记录见 §Implementation Status；评审另用 12 项 Playwright 探针对冻结原型复核同一批语义，见文末「parity 签收」）
- [x] `git status --porcelain` 相对 Step 1 基线无新增路径；源码改动 ⊆ 11 个 in-scope 文件（“无 commit”一条被 owner 后续指令取代：2026-11-22 要求执行器提交并由评审合并）
- [x] `plans/351-config-agent-create-form-first.md` 存在且 `plans/README.md` 有索引行（2026-11-22 评审补：索引状态行已刷新为 parity 已签收）
- [x] 原型 agent-create 场景与 §4.2 矩阵一致、新冻结哈希已记录（2026-11-22 签收：12 项 Playwright 探针复核 §4.2 的 agent-create 行；冻结 html `a6bc85c1…` / test `72140edc…`，owner 确认）

## 8. STOP conditions

1. **Step 1 复现与 §2 不符**（按 `a` 已直接出整表，或 Primary 已可改）→ 前提失效，停下来回报实测。
2. **§5 的 Drift check 任一锚点不匹配 / `shasum` 与 Step 1 基线不符**（peer 已改同一段）→ 停下来对齐后重锚；**不要顺手合别人的改动**。
3. **`grep -n "p\.templateItems(" pkg/tui/*.go` 不是 2 处**（peer 新增调用点）→ 先与 peer 对齐签名，再动手。
4. **修复必须改到 `editRows`/`configRowIsEntry`/`addRow` 或 Subagents 页**（peer 的领地）→ 停下来，先与 peer 对齐或缩小本计划。
5. **任何一次「创建期间写库」**（提交前 `fb_config` 变了、单测里 `session.writes` 非空）→ 视为未修好，不得以「测试绿」收口。
6. **需要放宽本计划之外的校验或绕过 `p.assign` 直接写库** 才能提交 → 停，回报；这是契约问题，不是实现问题。
7. **创建表单上出现模板未命名的行（`row.unknown`）** → 停止（本计划假定不可达）。
8. **peer 改出的原型 agent-create 场景与本计划 §4.2 矩阵冲突**（提交键/文案、字段可编辑范围、Esc 语义、切页签确认、Name 帮助句）→ 停，先对齐再落。
9. **`grep -rn "a-z0-9-]{1,62}" pkg frontend/src --include=*.go --include=*.vue --include=*.ts` 出现第 3 处**（除 `pkg/config/agents.go` 的 `subagentConfigNamePattern` 与 web 抽屉的 `AGENT_NAME_PATTERN` 外；`--include` 保证不扫 gitignored 的 `pkg/gateway/dist` 构建产物）→ 停，决定是否一并统一。
10. 两次 edit-and-review 后定点测试仍红 → 停，回报差异与证据。

## 9. Maintenance notes / 交接

- **唯一的 Primary 可编辑窗口是创建表单**；已保存定义页的 Name/Primary 永远锁定（`configCreatedFieldsNotice`），这是 2026-11-19 owner 裁决 + 原型 `readOnly` 的契约。
- **`p.staged` 是这份设计的承重墙**：创建会话里任何一处 `p.save` 被漏掉（新增写路径、复制 `assign` 的逻辑）都会破坏「提交前零写入」；漏网签名是 loader 报 `agents.definitions.: invalid agent id ""`。以后新增写库路径必须走 `p.assign`/`p.assignModel`，或显式认 `p.staged`。
- 草稿挂在 `definitions[""]` 上（原型 `draft['']`）：所有既有编辑器不用改就能工作；**元素路径**的模板解析靠 `configTemplateFor` 的 `has` 通配，集合本身不需要模板。不要在 create 会话里给 `definitions` 增加别的匿名键。
- **改名规则有三处、但只有两条规则**（owner 2026-11-22）：定义 id（两种角色）与共享子代理名 = `primaryAgentIDPattern`（允许下划线）；自定义子代理条目名 = `subagentConfigNamePattern`（dashes-only，模型看到的 `subagent_type` 命名空间）。web 抽屉按 scope 用两条 pattern，TUI 创建页只用第一条。若以后要让自定义条目也允许下划线，必须同时改 web agent-scope、locale、文档与 `pkg/run` 的文档，属于另一次改动。
- **两个创建页共用的常量：** `configCreateSaveKey`/`configCreateSaveLabel`（提交）、`configDiscardDraftLabel`/`configKeepEditingLabel`（切页签确认）。owner 改口径只改这些常量；`fireAction` 认 action 自己的 `Key`。
- 嵌套页（rows/editor）里按 ←/→ 不会切页签（今天也不会）；创建循环每轮开头清零 `p.tabStep`，只有表单页自己的箭头触发 Discard 确认。
- **peer 交接：** 原型 `plans/config-tui-prototype.html` + `.test.cjs` 由 peer 统一改（agent create、subagent create、删内置子代理的 `Agent` 状态标记、Name 帮助句、共享名规则）并重算冻结哈希；本计划只核对语义。peer 还会在 `templateItems` 上追加 `doc/rowPath` 做内置行过滤，并复用上述常量与尾部 `Save` 行。落地后把最终签名、`overlays.go` 区间、loader/网关/web 的新规则与文案发给他们。
- 评审 worktree（`.worktrees/plan-review-1827214835`、`.worktrees/plan-review-1994318238`，含 `zz_review_probe_test.go`）是评审者的证据，不在本计划改动面内；清理与否由 owner 决定。

---

## Implementation Status

- **Verdict:** APPROVE（代码侧，评审亲跑）；**parity 已签收（2026-11-22）** — 冻结 html `a6bc85c1…` / test `72140edc…`；12 项 Playwright 探针对 §4.2 的 agent-create 行（整表/零写入/`s` 一次落库/Esc/切页签确认/id 规则/帮助句/空选项）与子代理行全过，见文末「parity 签收」。
- **Worktree / branch:** `/Users/doudou/workspace/unionj-cloud/forebrain-harness/.worktrees/config-agent-create-form-first`，分支同名，base `824c386b`；**7 提交**。→ **已按 owner 指令 FF 合并回主树 `dev`（`824c386b` → `9b5c4891`，2026-11-22），未 push**；落地树与已验证树逐字节相同，主树冒烟 gofmt 净／build 0／config+gateway ok／聚焦 tui ok／architecture ok。worktree 与分支保留，删除由 owner 决定。
- **Commits:** `84f3a376` fix(config): one id rule for agent definitions, both roles → `2c5abdf9` feat(config): stage a create form's edits instead of writing each one → `71dca337` feat(config): a footer action that submits a form, and the name hint → `0c144219` feat(config): `a` draws the whole agent form, and `s Save` writes it once → `cc702fb1` fix(web): judge a subagent name by the scope it is written to → `31dba8a9` test(config): pin the empty create form leaving on a tab arrow → `9b5c4891` refactor(config): drop the create form's unused name-check parameter.

### 逐项状态

| 计划项 | 状态 | 证据 |
|---|---|---|
| Step 1 复现 + 基线 | done | 执行器真机复现（`New value` 编辑器、名字即落库、随后 locked）；评审用同一路径复核 |
| Step 2 loader 规则统一 | done | `pkg/config` 全量 ok；`subagentConfigNamePattern.` 2→1、`invalid subagent id` 3→0；共享引用豁免用例 `TestValidateAgentSubagentsSharedReferenceEntries` 绿 |
| Step 3 staged | done | `assign`/`assignModel` 两处早退；否定验证：删掉 `assign` 守卫后 "the save writes it whole" 变 3 次写 |
| Step 4 FooterActionSave + Name 帮助句 | done | footer action 用例断言 `Key=s`/`Label=Save`/`Kind=FooterActionSave`；帮助句真机可见含 underscores |
| Step 5 创建循环重写 | done | `go build -tags fts5 ./...` 0；真机 `a` → 整表、`s` 一次落库、回列表 |
| Step 6 TUI 单测 | done | 定点 + 全包 `pkg/tui` ok（评审亲跑 196.190s） |
| Step 7 web 抽屉 | done | typecheck ok、抽屉测试 14/14、build ok（评审亲跑） |
| Step 8 包级回归 | done | `pkg/config` ok、`pkg/gateway` ok、`pkg/tui` ok、`pkg/architecture` ok、gofmt 净、vet 0（评审亲跑） |
| Step 9 真机验收 | done | 执行器 11 场景逐屏留证（`$TMPDIR/fb-cfg-create-exec/evidence/`）；评审独立复跑核心 5 步（见下） |
| Step 10 收口 | done（原型 agent-create 部分） | 代码/范围/peer 通知完成；`plans/351-…md` 与 README 索引已落；`plans/config-tui-prototype.html` 的 agent-create 语义（`s Save`、创建期零写入、Esc 丢弃、切页签确认、id 规则/帮助句含 underscores、`globalModel` 空选项）已同步并有定向 Playwright 证据；子代理 create 场景与 `Agent` 状态删除留 peer，最终冻结哈希由 peer 出、待 owner 确认 |

### 评审亲跑的验证（不只是采信报告）

```bash
# 在 worktree 内，CGO_ENABLED=1、PKG_CONFIG_PATH 指向 worktree 的 pdfium
gofmt -l cmd pkg third_party plugins                         # 空
go vet -tags fts5 ./...                                      # exit 0
go test -tags fts5 ./pkg/config -count=1                     # ok 1.159s
go test -tags fts5 ./pkg/gateway -count=1                    # ok 10.081s
go test -tags fts5 ./pkg/tui -count=1                        # ok 196.190s
go test -tags fts5 ./pkg/architecture -count=1               # ok 2.024s
cd frontend && pnpm typecheck                                # exit 0
pnpm vitest run src/components/subagents/SubagentConfigDrawer.test.ts   # 14/14 passed
pnpm build                                                   # built in 9.39s
go test -tags fts5 ./pkg/tui -run 'TestConfigPanelCreatesAnAgentDefinition|TestConfigPanelCreateFormLeavesTheCreatedFieldsOpen|TestConfigPanelSavedDefinitionLocksItsCreatedFields' -count=1  # ok（最终 HEAD 9b5c4891 复跑 0.650s）
```

真机（评审自己的 driver 会话 `fb-lead-check`，隔离 home/project）：
1. 定义列表按 `a` → **第一屏即整表**（`Name` 空、`Primary True`），底栏 `↑/↓ to navigate Enter to open Esc to cancel s Save`，无 `New value` 页。
2. `Name` 编辑器 help = `Lowercase letters, numbers, hyphens or underscores.`；输入 `lead-check` + Enter 后 **表单仍在**，`fb_config.value_json` 与输入前**逐字节相同**（零写入）。
3. `Primary` → True/False 选择页（无 created-fields 句子）→ 选 False → 子代理字段 + role-switched notice，`value_json` 仍不变。
4. 按 `s` → 一次落库；回列表，新行 `lead-check Subagent`；库里恰一条 `"lead-check":{"primary":false}`。
5. `a` 后直接 Esc → 列表无新行、`value_json` 不变（零写入）。
6. 保存页锁定由 `TestConfigPanelSavedDefinitionLocksItsCreatedFields`（全包绿）+ 执行器证据 `s9-07b-primary-locked.txt`（"Set when the definition was created; it cannot be changed."）钉住。

### 偏差与判断（均有据）

1. **执行模型**：计划写的「不用 worktree、不 commit」被本次派发覆盖——实现与提交都在独立 worktree/分支，未 push/merge。
2. **测试替身 `TabStep`**：计划要求「仅在 `step.action == footerActionNone` 时回填」；执行器改为在 label 查找之前返回，避免箭头步进误匹配行标签，行为等价。
3. **多一个用例**：`a tab arrow leaves an empty form unasked`（裁决 5 的另一半，矩阵隐含但 Step 6 未列）。
4. **真机场景 5 的取证改成 Display name**：Description 是多行字段（Ctrl+Enter 保存），单行 Enter 会换行；两种行为都实测过。
5. **真机场景 11 的真实 loader 拒绝**：计划首选「MCP server 无 command/url」被 loader 接受并存库（已如实记录），按计划的 fallback 改用 `subagents` 里一个 system 分派类型名 → `"goal-evaluator" is dispatched by the system and cannot be configured.`，表单与全部值保留、DB 不变。
6. **评审第 1 轮的唯一改动**：`agentNameRefusal` 去掉已无用的 `draft` 参数（`holdAgentName` 随之去掉），提交 `9b5c4891`；行为不变，定点/包级门重跑绿。
7. **原型同步（2026-11-22 补完 agent-create 部分）**：`plans/config-tui-prototype.html` 已按裁决改：`stageValue` 在 create 帧上不 `persist`；新增 `creatingFrame`/`creatingDirty`/`discardCreateDraft`/`saveCreateForm`/`discardCreateForm`；底栏 create 页为 `Esc to cancel s Save`、`s` 提交（`saveCreateForm`，空名拒绝、校验失败保留草稿与表单、成功一次写入并回列表）；`back()` 在 create 帧弹出时丢弃草稿；`←/→` 在填过的表单上进入 `discard` 页（Discard / Keep editing，Keep 预选）；`renameDefinition`/`addEntry` 的定义 id 规则改为 `^[a-z][a-z0-9_-]{0,62}$` 且句子含 underscores；agent 模板 Name 帮助句同步；`choicesFor` 的 `globalModel` 补上字段声明的空选项（原先缺失，域内模型选择器与 TUI 不一致）。场景：`new-primary-detail` 改为「填表 → `s` → 回列表」，新增 `create-held`/`create-esc`/`create-tab-confirm`；`.test.cjs` 的陈旧 `create-agent` 块改写为 `create-held`+`new-primary-detail` 流程、截图场景改 `create-held`。**验证**：`node --check` 通过；定向 Playwright（8 个 create 相关场景 × 120/88/68 渲染非空 + 提交前 `saved` 不变 + `s` 一次写入 + Esc 零写入 + 切页签确认 + 空选项/模型挑选）全绿；整套 `node plans/config-tui-prototype.test.cjs` 仍停在同一处先于本次改动的已知缺口（第 167 行 `Default` 按钮不存在，Ctrl+U 未绑定），未到达 create 块。**新哈希**：html `eaa03b8b921a987b04815cc2f7517ae2840539e85430fc0314b67efcf6c3630d`、test `033ea1121e3918a4d7d465aa1692aed4352151ec876822cd469c6980c5661eb6`。**已完成（2026-11-22）**：子代理 create 场景与内置子代理 `Agent` 状态删除由 peer 落地；最终冻结哈希 `a6bc85c1…`/`72140edc…` 经 owner 确认，「原型与 §4.2 矩阵一致」已正式签收（见文末）。
9. **底部文案约定（owner 2026-11-23 裁决）**：底栏整行**小写**；footer action 一律渲染成 `key to verb`（`a to add` / `d to remove` / `s to save` / `ctrl+u to default`）。实现：`panelBuilder.hint` 统一 `strings.ToLower`（一处收口）；overlay 里不经 builder 的 dim 脚注与 roster hint 逐字小写；`footerHint`/`footerActionKeys` 改成 `key + " to " + label`。真实 TUI 取证（隔离 home，`/config`）：定义列表 `↑/↓ to navigate enter to open esc to go back a to add d to remove`，创建表单 `↑/↓ to navigate enter to open esc to cancel s to save`。原型 `footerHTML` 同规则；html 新 sha256 `6234536d2ed5771828dfcd0ff9acfefcdeb7a9981bc5e0819723e90ca54093f9`（test.cjs 未再变，仍 `033ea112…`）。`pkg/tui` 全包测试绿（31 处脚注断言随约定更新，0 FAIL）、`gofmt` 净、`go build -tags fts5 ./...` 0、`go vet ./pkg/tui` 0。
8. ENOSPC 处理：执行器按 `macos-disk-reclaim` 清理 go build cache / gopls cache / 6h 以上孤儿 `$TMPDIR/go-build*`，未动 Docker 与用户数据。

## parity 签收（2026-11-22，peer 落地后）

peer（plan 352 的评审方）把子代理半边与 `Agent` 状态删除落进原型后，owner 2026-11-22 确认采用并**冻结**：

- `plans/config-tui-prototype.html` sha256 `a6bc85c18dc580d4d7c8ed2b04bf5444e3a4355f95e7c4d902aa773ff8ba9ad1`
- `plans/config-tui-prototype.test.cjs` sha256 `72140edcbb68bbc2a6a676cf974fd10e2505c8d1ac075575bbeb9de943aeeeb7`

**签收证据（评审方亲跑，Playwright + `python3 -m http.server 8765`，每项独立重载 fixture，12/12 PASS）**：`a` 开整表且零写入（含 Name/Primary/System prompt/Model/MCP/Subagents 九行齐备）｜Name 只进草稿（`saved` 逐字节不变）｜Primary 只改内存｜`s` 一次落库 `{primary:false}` 并回列表面板（无空 key 残留、新行可见）｜Esc 零写入｜填过的表单按 → 进 `discard` 且 **Keep editing 预选**、Enter 回表单草稿仍在｜`Bad ID` 拒、`repo_scout` **合法**（下划线规则生效）、`main` 重名拒｜Name 帮助句含 `underscores`｜`default_model` 选择器含 `Follow global default` 空项｜子代理列表是 6 个内置名且**无 Agent/Global 状态**｜内置类型页恰 `Model`/`Thinking level` 两行无状态｜子代理创建表单把 Name 敲成 `explore` 时按内置名拒绝、**表单不塌**（System prompt/Tool access 仍在）。

**哈希已被后续版本取代（2026-11-22 晚）**：owner 拍板补上 Ctrl+U/Default 并把原型回归跑到全绿后，冻结对先更新为 html `525fd4e6e02a23d3f0ca65217d696805893fed72c132366604a9725d750ba405`、test `14c53549ad1b0fc25cb4832650582ec198b54db573f044a16f1a6c8388985848`，随后又因「pin 页空选项统一成 `Follow primary agent`」更新为 html `e5245ee322aeb97262c608e9cda92f7b755968f2338dd1095ced9e3b39c52964`、test `6bfe73e15549f6ce5482ce8683da215d2186471ac34fa6a336df22f279c4566e`（本轮改动与证据见 `plans/352-config-subagents-page.md` 的「追加」节）。本计划的 parity 结论不受影响——12 项探针在新版本上复跑仍全过。owner 2026-11-22 晚已确认冻结这一对新哈希。

**两处如实留痕（不影响签收）**：① 原型在「拒绝」后不恢复键盘焦点（`document.activeElement` 为空），探针须先点一下面板再按键——原型脚本层的瑕疵，设计语义（拒绝后表单仍在）成立；② 拒绝后 `notice` 文本不会在下次成功操作时自动清空（TUI 每轮清），属原型表现层小差异。
