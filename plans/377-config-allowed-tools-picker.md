# /config Allowed tools 页改为整页工具多选 picker（TUI）

> **Status: DONE — 已合并**（2026-11-25 execute＋tech-lead 评审 APPROVE；执行模型 alibaba/deepseek-v4.1-flash-1017；
> owner 批准 ff-only 落 dev `9c964e8a`，主树门禁复跑绿，worktree/分支已删；原型新 sha 已获 owner 确认冻结；详见文末 Implementation Status）。

## Context（为什么做）

owner 截图报告：TUI `/config` 创建/编辑 subagent 的 **Allowed tools** 页只能手动输入工具名
（行页 `a` Add 后开文本 Input），必须改成工具选择器，让用户从真实工具里选。
owner 已裁决形态 **B：整页换成 web 式多选勾选页**（一页列全部运行时工具，空格勾选、回车一次保存整表、Esc 取消），
与 web 子代理抽屉（`frontend/src/components/subagents/SubagentConfigDrawer.vue` 的 checkbox + 搜索）同源同语义。
owner 另裁决（2026-11-24）：**TUI 所有 slash 面板 footer 统一全小写**；存量清扫拆为计划 378（见维护注），
本计划的新页 footer 生来全小写、与原型逐字一致。
候选名单来源与 web 一致：运行时工具表 `Env.Tools().ToolMetas()`，滤掉 `subagent_*` 前缀
（校验器 `pkg/config/agents.go` `validateCustomSubagentBody` 拒绝该前缀：子代理不能再派发子代理）。

本计划是 owner 裁决后的实施契约。/config 面板受冻结原型约束：`plans/config-tui-prototype.html` +
`plans/config-tui-prototype.test.cjs` 必须同步改，改完跑原型回归并把新 sha256 对报给 owner 重新确认冻结
（agent 不得自行宣布冻结；不留 `.bak`/`.pre-*` 备份）。

## Baseline 与漂移检查

- 起草基线：`dev` @ `db4f3f4a`（主树）。执行 worktree：`.worktrees/exec-allowed-tools-picker`，从 `dev` HEAD 出发
  （若 dev 已前进，从新 HEAD 出发并重跑下面的漂移检查）。
- 主树有其它会话未提交的 `plans/*.md` 编辑（forebrain-harness-ad 的 reconcile / 374 / 248）——**不在本计划范围，不动**。
- 漂移检查（内容锚点，行号仅参考；任一锚点内容不符 = STOP 报告，不得即兴改法）：
  - `pkg/tui/config_panel_schema.go`：两处 `{Path: "allowed_tools", Label: "Allowed tools", Kind: configFieldRows, List: true, ...}`
    （`configTemplateAgent` 内 `SubagentOnly: true`；`configTemplateSubagent` 内 `CustomOnly: true`）。
  - `pkg/tui/config_panel.go`：`func (p *configPanel) editRows(doc any, path []string, field configField, initialNotice ...string) (any, string)`；
    `addRow` 内 `if template := configTemplateFor(p.section, childPath); template == configTemplatePath || (template == "" && configScalarRowTemplate(p.section, childPath) == configTemplateString) {`；
    `func configTemplateFor(section string, path []string) string` 的 agents 分支 `is(2, configDefinitionsPath)` → `configTemplateAgent`、
    `is(4, configDefinitionsPath, "", configSubagentsKey)` → `configTemplateSubagent`。
  - `pkg/tui/overlays.go`：`type richPickerState struct`、`func (s *rawSelector) richKeyLoopResult(...) (ActionResult, error)`、
    `case rawKeySpace:`（richKeyLoopResult 内，当前把空格当过滤输入）、`type ActionResult struct`、
    `func (p *richPickerState) footerHint() string`、`selectorCheckedGlyph = "●"` / `selectorUncheckedGlyph = "○"`、
    `const pickerFilterMinRows = 8`。
  - `pkg/tui/tabbed_picker.go`：`type configPageSelector interface`（可选能力接口先例）。
  - `pkg/tui/config_panel.go`：`func (s *ChatSession) ConfigPanelSections(ctx context.Context) (configPanelSnapshot, error)`、
    `type configPanelSnapshot struct { Stored map[string]json.RawMessage; ClaudeCode bool; Models []appcfg.LLMModelConfig }`。
  - `pkg/process/run_executor.go`：`func (env *Environment) Tools() *tool.State`（env/Runner nil 时返回 nil）；
    `pkg/tool/state.go`：`func (s *State) ToolMetas() []event.ToolMeta`（nil 安全、按 Name 升序）。
  - `plans/config-tui-prototype.html`：`const C=(k,l,template,list=false,help='',extra={})=>F(k,l,'collection',help,{template,list,...extra})`；
    两处 `C('allowed_tools','Allowed tools','string',true,'Used when tool access is Pick.'...)`（agent 模板带 `{subagentOnly:true}`、subagent 模板无 extra）；
    `function openField`（collection 分支 `pushFrame(f,path)`）、`function startAdd()`、`function jump(id,trail=[])`（collection 也直接 pushFrame）。
  - 当前 sha256（起草时）：html `4f89ddbecae475a5b470947e55e6a3856870d1842082773733f677ff4f0908d4`、
    test `56101a65561eccc406d3f816d209a29d567382da1b1c0830bd7e792f1f02dfd4`。
    memory 里 11-22 冻结对（e5245ee3/6bfe73e1）已被后续 owner 确认波取代；以上面这对为当前工作契约起点。

## 现状因果链（已核实，解释截图）

1. schema 把 `allowed_tools` 描述为字符串集合（`configFieldRows`+`List`），两个模板各一处（见锚点）。
2. 集合页走 `editRows`（config_panel.go:1556）：footer `a` Add / `d` Remove，Enter 开行；空态文案
   `rowsNotice`（:2120）= "Nothing configured yet\nAdd the first entry."（截图所见）。
3. Add：`addRow`（:2883）→ list 且 `configScalarRowTemplate == configTemplateString`（:2936）→ `p.editScalar(field, "")`（:2943）= 手动 Input。
4. Edit：`editRow`（:2135）→ `!container && configScalarRowTemplate(...) == configTemplateString`（:2163）→ `p.editText` = 手动 Input。
5. `configScalarRowTemplate`（:2313）对**任何**已知模板里的字符串集合都返回 `configTemplateString`
   （writable_roots、args、scopes 共用）——所以本改动必须按 `allowed_tools` 路径特判，不能改 kind 全局行为。
6. web 对照：`SubagentConfigDrawer.vue` `ensureTools()` 分页读全 gateway `/tools`
   （`pkg/gateway/api_extra.go:795` `toolMetas = s.Env.Tools().ToolMetas()`），滤 `subagent_*`，checkbox 多选 + 搜索框。
   TUI 的 `ChatSession.Env` 与 gateway 同为 `*process.Environment`，可直接读同一张表。

## 设计（B 方案）

### 1. 快照带工具名单（config_panel.go）

- 新类型 `configToolOption struct { Name, Description string }`（本文件内，无新 import）。
- `configPanelSnapshot` 增 `Tools []configToolOption`；`configPanel` 增 `tools []configToolOption`；
  拷贝点在 `handleConfig`（config_panel.go:218 起，`panel.models = snapshot.Models` 那一组赋值旁加 `panel.tools = snapshot.Tools`）。
  生命周期与 `models` 同：每次打开读一次，别的表面写的下次打开可见——沿用现有契约。
- 纯函数 `configToolOptions(metas []event.ToolMeta) []configToolOption`：跳过 `strings.HasPrefix(strings.ToLower(strings.TrimSpace(m.Name)), "subagent_")`（与校验器同口径），其余按入参顺序（ToolMetas 已按 Name 升序）映射 Name+Description。
  - `config_panel.go` 需**加一行 `pkg/event` import**（该文件现无此 import；包级 pkg/tui 已依赖 event——chat_session.go:21 等——故 **fan-out ratchet 不动**）。
- `ChatSession.ConfigPanelSections` 里：`if st := s.Env.Tools(); st != nil { snap.Tools = configToolOptions(st.ToolMetas()) }`（`Env.Tools()` 与 `ToolMetas()` 均 nil 安全；无 runner 时名单为空）。

### 2. selector 层：richPickerState 加多选模式（overlays.go）

复用现有 config 页渲染路径（chrome tab 栏、configPage  airy 行、filter、←/→ tab step、footer hint），不另起页型：

- `richPickerState` 增 `multi bool`、`checked map[int]bool`（indices into items）。
- `panel()`（:6334）：`multi` 时每行 lead 用 `selectorUncheckedGlyph`/`selectorCheckedGlyph`（照 `selectState.panel` :5147-5152 的写法），经 `selectableColumnsValue` 的 lead 参数传入；其余渲染不变。
- `richKeyLoopResult`（:6770）：
  - `case rawKeySpace:` 开头加：`if p.multi && !p.inFilter { idx := p.absoluteRow(); if idx >= 0 { toggle p.checked[idx] }; s.renderRichPicker(p, label); continue }`（在过滤框里时空格仍是输入，现状不变）。
  - `case rawKeyEnter:`：`if p.multi { return ActionResult{OK: true, Checked: p.checkedIndices()}, nil }` —— **即使过滤后 selectable 为空也返回已勾选集**（勾选跨过滤保留）；非 multi 分支一字不动。
  - 新增 `func (p *richPickerState) checkedIndices() []int`（升序，照 tabbedPickerState.checkedIndices tabbed_picker.go:261）。
- `footerHint()`（:6413）：`multi` 分支产出**全小写**串（owner 2026-11-24 裁决：TUI 所有 slash 面板 footer 统一全小写；
  存量 112 处清扫另立计划 378，见维护注）：`"↑/↓ to navigate space to toggle enter to " + p.keys.Enter + " esc to " + p.keys.Esc`
  （本页 `configPageKeys{Enter: "save", Esc: "cancel"}`）。非 multi 分支一字不动（大写 Enter/Esc 现状保留到 378）。
  最终 footer 与原型**逐字一致（含大小写）**：`↑/↓ to navigate space to toggle enter to save esc to cancel`。
- `ActionResult` 增 `Checked []int`（多选确认时勾选的 items 下标，升序；其余调用方不读它，零影响）。
- `ActionOptions` 增 `Checked []int`（开页时已勾选下标）。**不加 Multi 标志**——`SelectConfigMultiPage` 自身即多选语义，
  加了无人读就是死字段（owner 死代码规则）。
- `rawSelector` 新方法：
  ```go
  func (s *rawSelector) SelectConfigMultiPage(label string, items []SelectItem, opts ActionOptions) (ActionResult, error)
  ```
  构造 `richPickerState{configPage: true, chrome: opts.Chrome, keys: opts.Keys, multi: true, checked: from opts.Checked}`，
  `rebuildSelectable()`，`noFilter = !pickerShowsFilter(len(selectable))`（opts.Filter 语义同 SelectConfigPage：本页传 Filter=true），
  cursor 落 0，走 `richKeyLoopResult`。空 items 也开页（与 SelectRichActions 带 action 时同：空态页是合法页面）。
- 可选能力接口（tabbed_picker.go，紧挨 configPageSelector，照其注释风格）：
  ```go
  type configMultiPageSelector interface {
      SelectConfigMultiPage(label string, items []SelectItem, opts ActionOptions) (ActionResult, error)
  }
  var _ configMultiPageSelector = (*rawSelector)(nil)
  ```
  **不 widen** `configPageSelector`/`Selector`——现有 double（scriptedSelector 等）保持编译。

### 3. 面板层：整页多选（config_panel.go）

- 路径解析 `configToolsFieldAt(section string, path []string) (configField, bool)`：
  `path` 末元素 == `"allowed_tools"` 且 `configTemplateFor(section, path[:len(path)-1])` 的模板字段里有
  `Path == "allowed_tools" && Kind == configFieldRows && List` → 返回该字段。
  （已核实：`["definitions","X","allowed_tools"]` 的父路径解析到 configTemplateAgent；
  `["definitions","main","subagents","N","allowed_tools"]` 解析到 configTemplateSubagent。创建表单的 staged doc 走同路径。）
- **路由拦截一处**：`editRows` 函数体最前（notice 初始化之后、for 循环之前）：
  ```go
  if f, ok := configToolsFieldAt(p.section, path); ok {
      return p.editAllowedTools(doc, path, f)   // 用解析出的模板字段，不用入参 field（页头标签以模板为准）
  }
  ```
  覆盖全部入口：`editField`(:937)、`openTemplateField`(:2549/:2584)、创建表单字段页。
  拦截后 allowed_tools 不再列元素行，`editRow`/`addRow` 的字符串分支对它不可达——**那些分支不改**（writable_roots/args/scopes 仍走文本编辑器）。
  不写防御性兜底分支（owner 常设规则：不可达状态不写防御代码）。
- `editAllowedTools(doc any, path []string, field configField) (any, string)`：
  1. 读当前列表 `cur, _ := configLookupValue(doc, path)`，`stored []string`（`[]any` 元素断言 string，TrimSpace）。
  2. `items, names := p.toolPickItems(stored)`：先 `p.tools` 全量（Label=Name，Description=Description），
     再把 stored 里不在表中的名字**追加为额外行**（dangling，Description 空）。去重 = **精确匹配**（工具名是精确标识符，不折叠大小写）。
     `checked` = stored 名字在 items 的下标集。
  3. 循环开页：
     ```go
     res, err := p.multiPage(notice, items, checked)   // 见下
     ```
     - err → `p.printError(err); return doc, ""`。
     - `res.TabStep != 0` → `p.tabStep = res.TabStep; return doc, ""`（←/→ 步 tab，同 plainPage）。
     - `!res.OK`（Esc/Ctrl+C）→ `return doc, ""`，零写入。
     - OK → `next := names[res.Checked...]`；**空表必须传 nil 删键**（已核实 `configAssign` :3825 叶子层无条件返回 value、
       父层仅 `next == nil && remove` 才 delete——非 nil 空切片会存成 `[]`，全仓删除调用均传 nil）：
       ```go
       var updated any; var note string
       if len(next) == 0 {
           updated, note = p.assign(doc, path, nil, true, field.Label)
       } else {
           updated, note = p.assign(doc, path, next, false, field.Label)
       }
       ```
       （staged 模式下 assign 只持有不写。）
       - `note != ""`（保存被校验器拒绝，非 staged）→ `notice = note; checked = res.Checked; continue`
         **带着用户刚勾的状态重开页**，拒绝词显示在 tab 栏下（notice 行），section 原样。
       - 否则 `return updated, ""`（回父页，值列刷新）。
  4. `multiPage` helper：有能力 `configMultiPageSelector` → `SelectConfigMultiPage(label, items, ActionOptions{Page: true, Chrome: p.chrome(), Keys: &configPageKeys{Enter: "save", Esc: "cancel"}, Filter: true, Checked: checked})`；
     无能力（piped/脚本 double）→ 退回 `p.sel.MultiSelect(label, names, checkedNames)`，名字映射回下标（照 `multiSelectRichTabbed` 的 fallback 写法 tabbed_picker.go:29-52）。
- 保存语义：一次确认 = 一次 `UpdateConfigSection` 整表写入（「the write is the save」plan 321 不变）。
  结果列表顺序 = items 顺序（表序 + dangling 追加）；集合语义，顺序无副作用（web 保留插入序，此差异可接受，记入维护注）。
- `tools` 字段（Tool access enum）与校验器**不动**：`pick` + 空表的拒绝词由现有 `p.save` 路径原样带上页面。

### 4. 原型同步（plans/config-tui-prototype.html + .test.cjs）

- 新 fixture `const TOOLS=[{name:'read_file',desc:'…'},…]`（≥8 行触发 filter，建议 ~16 个真实内置名：
  read_file/write_file/edit_file/shell/web_fetch/web_search/lsp/present_files/memories_list/memories_read/memories_search/…；
  **不含任何 `subagent_*`，也不含 `glob`/`code_search`**——FIXTURE 里这两个名字是故意的 dangling 练例）。放在 `GLOBAL_ROWS`（html:45）附近。
  现有 FIXTURE 的 `allowed_tools:['read_file','glob']`（code-reviewer）保留——`glob` 不在 TOOLS 里，天然练 dangling 行。
- 两处 `C('allowed_tools',...)` 的 extra 加 `toolPick:true`（agent 模板 `{subagentOnly:true,toolPick:true}`；subagent 模板 `{toolPick:true}`）。
  `C` 的 extra 直接展开进字段对象（html:38），故 `f.toolPick` 可读。
- 新视图 `state.view='toolPick'`：filterBox + 勾选行（复用 `.item.option` 结构，checked 行画 check glyph，❯ 光标独立）；
  `state.editor={f,path,checked:Set(当前名单)}`。空格 toggle；Enter 保存（见下）；Esc 回退零写入。
  dangling 名字（在 draft 不在 TOOLS）追加为已勾选行。
- **键盘/返回集成（已核实现状，必须同改，否则页面不可用）**：
  - `back()`（html:156）的视图列表 `['choice','edit','add','discard']` 加入 `'toolPick'` → Esc 回 fields、零写入（不然会 stack.pop 弹掉父帧）。
  - keydown（html:234）通用 Enter 走 `visibleRows[selected].run()`——在其前加 `state.view==='toolPick'` 专用 Enter 分支（整表保存，见下）。
  - 空格现状被过滤输入分支吞掉——在过滤分支前加 `key===' ' && view==='toolPick' && !state.inFilter` 的 toggle 分支。
  - `render()` 加 toolPick 的 markup 分派（照 choice 视图 :183 的结构）。
- **Enter 保存语义（已核实 `stageValue` html:150-151）**：`stageValue` 对非创建帧**立即 persist 并关页回 fields**——
  与 TUI「the write is the save」一致，已存定义没有独立 section save 步。故 toolPick Enter：
  1. 先校验 `tools==='pick' && 勾选为空` → 置 notice、tone='error'、**留在 toolPick 视图**，不调 stageValue
     （`validateDraft` html:159 不查这条，persist 失败会从 saved 还原草稿并关页，不能靠它）。
     拒绝文案钉死为校验器句去掉 where 前缀：`allowed_tools requires at least one tool with tools: pick`
     （test.cjs:396 禁 UI 文案出现 `agents.` 原始配置路径；TUI 侧仍显示校验器完整原话——面板既有规则「validator's own words」，两边各自沿用现有口径）。
  2. 否则 `stageValue(勾选名单数组)`：已存定义 = 一次 persist + 回 fields；创建帧 = 只持有，s Save 才落。
- **两个拦截点**（collection 默认 pushFrame 行页；已核实 pushFrame 全部调用点 = openField/jump/startAdd 创建分支/addEntry/个别 scene，
  后三者只推**行**帧不推 collection 帧，故 toolPick collection 帧只可能经 openField/jump 产生）：
  - `openField`（html:144）：`f.kind==='collection' && f.toolPick` → 开 toolPick 视图（在 pushFrame 分支之前）。
  - `jump`（html:238）：collection 分支里 `row.f.toolPick` → 开 toolPick 视图（deepRoutes 用）。
  - **`startAdd` 不加拦截**：toolPick collection 永不进帧栈，'a'/空态 Add 到不了它——加分支即不可达死代码（owner 常设规则禁）。
- footer 逐字：`↑/↓ to navigate space to toggle enter to save esc to cancel`（全小写，owner 裁决；TUI footerHint 的 multi 分支必须产出同一串）。
- **已有路由**：html:242 deepRoutes 已有 `['tool-pick','子代理工具白名单','agents',['definitions','main','subagents',…]]`
  → 场景 `deep-tool-pick` 已存在（现状开字符串行页）。jump 拦截后它自动开勾选页；其通用 caption「集合用条目列表」对 allowed_tools 失效，更新该条 caption。
  新场景用 `tool-pick-*` 前缀，不与 `deep-tool-pick` 撞名。
- 鼠标：原型 rowMarkup 点击 run=toggle 会自带鼠标勾选（保留）；TUI 鼠标 toggle 本期不做（见维护注）。
- 新场景（中文 caption，禁 middle-dot `·`）：
  - `tool-pick`：jump 到 repo-auditor 的 allowed_tools → 勾选页（read_file ✓ + dangling code_search ✓）。
  - `tool-pick-toggle`：空格勾/取消 + Enter 一次保存（已存定义即 persist；创建帧只持有）。
  - `tool-pick-filter`：过滤框 narrowing。
  - `tool-pick-empty-refused`：全取消 + Enter（tools=pick）→ 拒绝 notice、草稿不变。
  - `tool-pick-create`：创建 subagent 表单里开 allowed_tools → 勾选只持有，s Save 才落。
- `.test.cjs` 新增 pin（照现有 `check(name, fn)` + `scene(id)` 模式）：
  勾选页打开且**无文本输入框**；空格 toggle；Enter 一次写、Esc 零写；`subagent_*` 不出现；dangling 行已勾选；
  filter narrowing；空+pick 拒绝（notice 在、视图留在 toolPick、saved 不变）；创建表单持有语义。
- **范围内授权编辑现有 pin**：`test.cjs:71-90`「Every visible nested field opens its own editor」断言
  `['edit','choice'].includes(view) || stack 增长`——toolPick 两者都不是，`deep-custom-subagent` 点 allowed_tools 必破。
  把合法转移集合加入 `'toolPick'`（这是本计划唯一允许动的现有 pin）。其余现有项全过、`browserErrors=[]`。

### 5. 不做的事（边界）

- 不改 `configFieldRows` 通用机制、不改 writable_roots/args/scopes 等其它字符串集合（仍文本编辑器）。
  **点名排除**：MCP 模板的 `tools`（Tool overrides，`configTemplateToolApproval`，schema.go:420/567）属 MCP 服务器命名空间的审批覆盖，
  不是运行时工具表——`configToolsFieldAt` 只认 `allowed_tools`，天然不碰它。
- 不改 web 端、不改 gateway `/tools`、不改校验器、不改 schema 描述（Kind/List/Hint 原样）。
- 不加自由输入兜底（owner：让用户做选择）；dangling 已存值通过额外勾选行保留/移除。
- 不动 `Selector`/`configPageSelector` 接口形状（新能力是独立可选接口）。
- 不新增**包级**依赖（仅 config_panel.go 文件级加一行既有 pkg/event import；fan-out ratchet 不变，pkg/architecture 无需跑）。
- 主树其它会话的未提交 plans/*.md、.worktrees/exec-374-*、.worktrees/exec-248-* 一律不动。

## Files to change（执行 worktree 内）

| 文件 | 改动 |
| :--- | :--- |
| `pkg/tui/config_panel.go` | configToolOption/configToolOptions、快照+面板 tools 字段、ConfigPanelSections 读表、configToolsFieldAt、editRows 拦截、editAllowedTools+toolPickItems+multiPage |
| `pkg/tui/overlays.go` | richPickerState multi/checked、panel() lead、richKeyLoopResult space/enter、checkedIndices、footerHint（multi 全小写分支）、ActionResult.Checked、ActionOptions.Checked、SelectConfigMultiPage |
| `pkg/tui/tabbed_picker.go` | configMultiPageSelector 可选接口 + 编译断言 |
| `pkg/tui/config_panel_test.go` | 新测试（见下）+ 测试 double 实现新能力 |
| `plans/config-tui-prototype.html` | TOOLS fixture、toolPick 标记、toolPick 视图、两拦截点（openField/jump）、键盘/返回集成、新场景 |
| `plans/config-tui-prototype.test.cjs` | 新 pin |

范围外：`config_panel_schema.go`（描述不变）、`channels.go`（SelectItem 不变）、web/gateway/config 校验器、pkg/architecture。

## Steps（有序，每步带检查）

0. **worktree 原生依赖接线**（评审实测阻断点）：`third_party/libtokenizers/`、`third_party/pdfium/` 被 gitignore
   （.gitignore:15/19），新 worktree 里不存在，直接跑测试会 `ld: library 'tokenizers' not found`。
   照既有做法（`.worktrees/exec-374-account-logout/third_party/libtokenizers` 即主树符号链接）：
   ```bash
   ln -s /Users/doudou/workspace/unionj-cloud/forebrain-harness/third_party/libtokenizers <worktree>/third_party/libtokenizers
   ln -s /Users/doudou/workspace/unionj-cloud/forebrain-harness/third_party/pdfium <worktree>/third_party/pdfium
   ```
   （pdfium.pc 的 prefix 是主树绝对路径，经符号链接解析仍有效；`PKG_CONFIG_PATH` 指 worktree 内 `third_party/pdfium/$(go env GOOS)_$(go env GOARCH)`。）
   检查：`PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestNothing -count=1` 过（**必须用会 link 测试二进制的命令**——
   评审实测 `go build ./pkg/tui` 只编译不 link，裸 worktree 下 exit 0 而 `go test` 才报 `ld: library 'tokenizers' not found`；
   用 build 做检查会把 link 错误漏进步骤 1 的「编译错误=脚手架没落对」误判区）。主树若缺依赖则先在主树跑对应 fetch 脚本。
1. **复现先行（编译安全的红）**：先给测试 double `actionPanelSelector` 加方法
   `SelectConfigMultiPage(label string, items []SelectItem, opts ActionOptions) (ActionResult, error)`——
   Go 允许 struct 实现尚无任何接口要求的方法，故**当前代码下可编译**；它把每次调用记入 `multiAsks`（label/items/opts），
   从本地队列 `multiSteps []struct{ok bool; checked []int}` 应答，**耗尽默认 `ActionResult{OK:false}`**（照 SelectRichActions :145-170 的安全耗尽，防拒绝-重开测试死循环）。
   此步返回 `ActionResult{OK: step.ok}` 即可（`Checked` 字段步骤 2 才加，届时补 `Checked: step.checked`）。
   再写 `TestConfigPanelAllowedToolsOpensToolPicker`：saved subagent 定义 tools=pick，快照注入 Tools（含一个 `subagent_run`）——
   快照 `Tools` 字段与 `configToolOption` 类型也在本步先落（纯数据脚手架，无行为），使测试可编译。
   断言：`multiAsks` 恰 1 次、items 滤掉 `subagent_*`、**零 Input ask**。
   跑 `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestConfigPanelAllowedToolsOpensToolPicker -count=1` → **必须以行为失败**
   （现状走 editRows 行页：multiAsks=0；Add 还会记 Input ask）。若以编译错误失败 = 脚手架没落对，修脚手架；若直接绿 = STOP。
   （命令需 `PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`，见 FOREBRAIN.md；下同。）
2. **selector 层**：overlays.go + tabbed_picker.go 按设计 §2。检查：`go build -tags fts5 ./pkg/tui` 过；
   现有 `go test -tags fts5 ./pkg/tui -run 'TestSelect|TestRich|TestTabbed|TestConfigPanel' -count=1` 全绿（非 multi 路径零变化）。
3. **面板层**：config_panel.go 按设计 §1+§3。检查：步骤 1 的测试转绿。
4. **补全测试矩阵**（见下节）全绿。
5. **原型**：html + test.cjs 按设计 §4。检查：`node plans/config-tui-prototype.test.cjs` 全过、browserErrors=[]
   （需 frontend/node_modules 含 @playwright/test；缺则 `cd frontend && pnpm install`）。
6. **门禁**：`gofmt -l pkg/tui` 空；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui`；
   `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` 全包绿。
7. **真机验收**（run-forebrain skill 配方：fake provider + tmux + 真二进制）：
   `/config` → Agents & tools → definitions → 开一个 tools=pick 的 subagent 定义（或创建表单）→ Allowed tools：
   - 勾选页渲染（tab 栏在、checkbox 行、footer 逐字「↑/↓ to navigate space to toggle enter to save esc to cancel」，全小写）；
   - 空格 toggle、filter narrowing、Enter 一次保存（值列刷新）、Esc 零写入；
   - ←/→ 仍步 tab；dangling 名可见可取消；
   - `capture-pane -e` 留证据。
8. **落地**：照本仓 execute 标准 git 工作流（worktree 分支提交 → 门禁绿 → ff-only 落 dev；dev 被并行会话推进时先 rebase 重跑门禁；
   主树有他人未提交 WIP 时按既有 ff-merge 配方协调，**不得 stash/discard 他人改动**）。落地前 peer_send 通告 forebrain-harness-ad。
9. **收尾（主树，仅 docs）**：**先核下一个空号**（`ls plans | grep -E '^[0-9]{3}-' | sort | tail`——起草时 376 已被并行会话
   forebrain-harness-76 的 vscode-extension-chat-panel 占用，故本计划预期 377、清扫预期 378；收尾时以实盘为准，撞号再顺延），
   写 `plans/377-config-allowed-tools-picker.md`（本计划自包含副本 + Status: DONE + landed SHA），
   `plans/README.md` 追加索引行（动手前核对 forebrain-harness-ad 是否又在写 README）；
   把原型新 sha256 对报给 owner 重新确认冻结。删除执行 worktree（`git worktree remove`）。

## 测试矩阵（config_panel_test.go，照 TestConfigPanelModelPickerFilters:2099 / effort 族:3986+ 的 scripted 模式）

测试 double：`actionPanelSelector` 的 `SelectConfigMultiPage` 按步骤 1 的**队列 + 耗尽默认 OK=false** 实现
（拒绝-重开用例需要两次应答：首次拒绝、二次 Esc 或合法勾选；固定应答会死循环）；
无能力 fallback 路径用 scriptedSelector 现有 MultiSelect（commands_test.go:112，固定应答只用于单次确认用例）驱动一例。

1. 开页即多选：items = 快照 Tools 滤 `subagent_*`（大小写不敏感前缀）、checked = stored 下标；无 Input ask。（步骤 1）
2. Enter 改勾 → 恰一次 UpdateConfigSection，列表 = items 序的勾选名。
3. Esc → 零写入。
4. 全取消 + tools≠pick → 写删键（allowed_tools 不在 JSON）；全取消 + tools=pick → 校验器拒绝词上页、**重开页勾选保留**、section 原样
   （用 double `writeErr` 模拟拒绝一例；若现有 runConfigPanelOn 真校验器模式可达，再加真校验器一例）。
5. dangling 已存名 → 额外已勾选行；取消它 → 保存列表不含它。
6. 两模板都路由：`definitions.X`（primary=false）与 `definitions.main.subagents.N`（custom）；内置类型 pin 页不出现 allowed_tools（现状不变）。
7. 创建表单 staged：勾选只持有（零写入），s Save 一次落库含 allowed_tools。
8. 回归：writable_roots 的 Add 仍开 Input（文本编辑器不变）。
9. `configToolOptions` 纯函数单测：滤 subagent_ 前缀（含大小写/空白）、保序、nil 入参 → nil。
10. 空工具表（double 快照 Tools=nil）+ 无 stored → 页可开、Esc 退出零写入；Enter 亦安全（删不存在的键 = 无操作写，不报错）。
11. 渲染断言：多选页 footer 串与原型**逐字一致（含大小写，全小写）**；checkbox glyph（●/○）出现在勾选/未勾选行（renderer 帧断言，照现有帧检查写法）。

## Done criteria（机器可查）

- [ ] 步骤 1 测试先红后绿；测试矩阵 1-11 全过。
- [ ] `go test -tags fts5 ./pkg/tui -count=1` 全包绿；`gofmt -l pkg/tui` 空；`go vet -tags fts5 ./pkg/tui` 净。
- [ ] `node plans/config-tui-prototype.test.cjs` 全过、browserErrors=[]。
- [ ] 真机验收 7 的每条有 capture 证据。
- [ ] 范围合规：`git diff --stat` 只含 Files-to-change 表内文件（+ 落地后的 plans/377 与 README 行）。
- [ ] grep 归零检查：无新增死常量/死函数（owner 常设规则）；`configScalarRowTemplate` 通用路径未被改坏（回归 8）。
- [ ] 原型新 sha256 对已报 owner；worktree 已删。

## STOP 条件

- 漂移检查任一锚点内容不符（行号漂移不算）。
- 步骤 1 复现不以「multiAsks=0（开了行页）」行为失败（编译错误先修脚手架；直接绿 = STOP）。
- 同一验证失败两次修不绿 → 停下报告，不即兴扩scope。
- 需要改范围外文件（schema/web/gateway/校验器/Selector 接口形状）才能完成 → 停下问 owner。
- dev 落地冲突涉及他人未提交 WIP → 协调，不 stash/discard。

## 维护注 /  deferred

- **计划 378（owner 2026-11-24 裁决，已确认拆分）**：「TUI 端所有 slash 命令面板 footer 统一全小写」的存量清扫——
  盘点 112 处 hint/footer 串，分布于 overlays.go、render.go、run.go、setup.go、tabbed_picker.go、project_servers.go、workspace_trust.go，
  另多个测试文件的断言（chat_session_test.go、overlays_test.go、setup_test.go 等；计数随正则口径变，378 起草时以精确正则重新盘点）。原型已全小写（含 `ctrl+enter to save`、`ctrl+u to default`），
  清扫后两面逐字一致。**排在 377 与 374 都落地之后**（setup.go/setup_test.go/chat_session_test.go 正被 374 在途编辑，避开撞文件）。
  377 的新页 footer 生来全小写，是 TUI 首个小写 footer；377→378 之间的短暂不一致为 owner 认可的过渡态。
  378 由本会话在 377 收尾后起草（plans/378-tui-slash-footer-lowercase.md），不并入 377。
- 结果列表顺序 = 工具表序（web 保留插入序）：集合语义无副作用；若 owner 将来要保序，改 toolPickItems/结果组装一处。
- 工具名单每次开面板读一次：MCP 后启动的工具下次打开才出现（与 models 表同契约）。
- 鼠标点击 toggle 未做（键盘空格为准）；如要，照 tabbedPickerState 的鼠标路径加，属后续。
- 评审重点：richKeyLoopResult 的 space/enter 分支只影响 multi=true；非 multi 调用方（/model、effort、enum、行页）行为必须零变化——测试矩阵外靠全包回归兜底。
- 并行会话：forebrain-harness-ad 在 .worktrees/exec-374-account-logout / exec-248-rebind 动 setup.go/setup_test.go/chat_session_test.go，与本计划零文件交集；go build 缓存并发安全，无需独占窗口。

## Implementation Status

- **VERDICT: APPROVE**（tech-lead 评审，零修订轮）。执行器：owner 指定的 alibaba/deepseek-v4.1-flash-1017。
- **Worktree**：`.worktrees/exec-allowed-tools-picker`，分支 `exec-allowed-tools-picker`，提交 `9c964e8a`（父 `db4f3f4a`）。
  **已 ff-only 落 dev `9c964e8a`**（owner 2026-11-25 批准）；`git diff 9c964e8a..HEAD` 六文件空（landed==tested）；
  主树门禁复跑绿（gofmt 空、vet 0、pkg/tui ok 197.6s、原型 33/33 browserErrors=[]）；worktree/分支已删；他人 17 个未提交 plans/*.md 不受影响。
- **评审方亲跑验证**（不采信执行器报告，均在 worktree 内）：
  - 范围：`git diff --stat db4f3f4a..HEAD` 恰为 6 个 in-scope 文件（+913/−16）；`status --porcelain` 干净。
  - `gofmt -l pkg/tui` 空；`go vet -tags fts5 ./pkg/tui` exit 0。
  - 新测试矩阵：12 条全 PASS（TestConfigPanelAllowedTools* ×10、TestConfigToolOptions、TestSelectConfigMultiPageOnNoRows），
    含真 state DB + InstallConfigSectionValidator 的校验器拒绝一例；复现测试断言实质（multiAsks=1、滤 subagent_*、零 Input）。
  - 全包：`go test -tags fts5 ./pkg/tui -count=1` → ok 197.9s。
  - 原型：`node plans/config-tui-prototype.test.cjs` → passed 33、browserErrors=[]（该测试约定需外部静态服务器供 plans/ 于 8765；评审方起 server 后复跑）。
  - 真机：证据 `/tmp/377-picker-rf-sgr.txt` 含 footer `↑/↓ to navigate space to toggle enter to save esc to cancel`（SGR `\e[2m`）与 `● read_file` 勾选行；
    执行器 tmux 跑覆盖 toggle/filter/Enter 一次保存/Esc 零写/dangling。
  - 原型集成点逐读核实：`back()` 含 toolPick；`saveToolPick` 对 pick+空表以校验器原句（无 where 前缀）拒绝并留页；
    恰两拦截点（openField/jump），**无 startAdd 死分支**；test.cjs 授权 pin 编辑（转移集加 toolPick）+ 5 新 pin。
- **文档化偏差（逐条核实，接受）**：
  1. Keys 复用既有 `configChoiceKeys`（{Enter:save, Esc:cancel}）而非新字面量——footer 相同，复用更优。
  2. fallback 复用 `multiSelectRichTabbed` 而非手写 MultiSelect 映射——行为相同，复用更优。
  3. 结果列表按 names 遍历装配（items 序）——契合计划「顺序=items 序」，对乱序应答稳健（checked:[2,1] 测试钉住）。
  4. 原型 click handler 对 toolPick 视图重新聚焦终端——鼠标点行后键盘可用的必要修复，范围内、已说明。
  5. `deep-tool-pick` caption 更新——计划内。
  6. worktree 符号链接 third_party/{libtokenizers,pdfium} + frontend/node_modules → 主树（gitignore，未提交）；原型回归截图写后即删，未提交。
- **未做 / deferred**：合并 dev（owner 决定）；plans/378 footer 全小写清扫（377+374 落地后）；TUI 鼠标 toggle（计划 deferred）。
- **范围外既有问题（一行报备，未改）**：真机 ←/→ 步页签不生效——`editSection` 的 defer 先清 `p.tab`（config_panel.go:581-583），
  `leaveSection`→`stepTab` 再按空 `p.tab` 查（:358-371）返回 ""；`TestConfigPanelSectionArrowStepsTabs` 因手工设 panel.tab 而绿。属 plan 350 既有缺口。
- **Nit（非阻断）**：`editAllowedTools` 检 `p.tabStep`（面板字段）而非 `res.TabStep`；因 multiPage 设置它且 section 循环复位，功能无碍。
- **原型新 sha256 对（待 owner 重新确认冻结）**：
  html `6bb1eeee3d23f68fea4c6c2c8d9cbad4f179acee6cc8bbe206368b919bc4c7b6`、
  test `10f89daf95836b1ee16bad77cad9c5caa1f04cf7e8ae4ef90810fc79e0ef4e02`。