# Plan 373: /config 创建 agent 时不再写入 `definitions[""]`（修复 `agents.definitions.: invalid agent id`）

> **Executor instructions**: 按步骤执行，每步跑完「验证」命令并确认结果再进下一步。遇到
> “STOP conditions” 里的情况立即停下上报，不要自行改方案。完成后更新
> `plans/README.md` 里本计划的状态行（owner 2026-11-21 裁决：计划目录为 `plans/`）。
>
> **编号**：本计划写作时为 370，落盘前发现 `370-process-start-wait-stale-running.md`、
> `371-process-live-output-deltas.md` 与 `372-process-card-header-names-its-command.md` 已占号（后者在落盘
> 过程中被别的会话抢走），故再顺延为 **373**（`ls plans | grep -E '^373'` 为空）。
> 重新落盘前仍要跑 `ls plans | grep -E '^373'`：若非空，取下一个空号并同步本文件标题、Step 8 的
> 文件名与 `plans/README.md` 的行。
>
> **Drift check（先跑）**: `git rev-parse --short HEAD` 应为 `dc93b1ac`（写计划时同一份文件在
> `80bb73b4` 与 `dc93b1ac` 之间 diff 为空，已核）；
> `git diff --stat dc93b1ac -- pkg/tui/config_panel.go pkg/tui/config_panel_test.go`
> 与 `git status --porcelain -- pkg/tui/config_panel.go pkg/tui/config_panel_test.go` 都应为空。
> 有内容就先读「Current state」摘录与现状对照；**内容不符 = STOP**（行号漂移不算）。
> 工作树里 `pkg/tui/zz_repro*_test.go` 三个临时复现文件是写计划时留下的（未跟踪，不在上面两条命令的
> 路径范围内），Step 1 第一件事就是删掉。
> **并发**：此刻本仓库还有别的 agent 在同一工作树里干活（一个在改 `pkg/tui` 的消息行/卡片渲染）。
> 开工前 `peer_list` / `peer_send` 通告你要改
> `pkg/tui/config_panel.go`、`pkg/tui/config_panel_test.go` 与 `plans/README.md`；索引表里只
> **追加**自己那一行，不动 `367`/`368`/`369` 的行。不要 revert/stash 别人的改动。

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW（改动只落在 `pkg/tui/config_panel.go` 的写入闸门与一处提交路径，外加测试）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `dc93b1ac`，2026-11-23（根因在 `80bb73b4` 上复现，两个 in-scope 文件在两
  commit 之间未变）
- **Issue**: —

## Why this matters

owner 截图：在 `/config` 的 Agents & tools 里**创建 agent** 时，页面顶部弹出
`config section agents: agents.definitions.: invalid agent id`，整份 agents section 因此一个字节
都没写进 store。用户填的 Name/Display name/Description/Model 全在表单里，但保存永远失败；
state DB 里的 agents 段是运行时配置的唯一来源，所以这个用户路径等于「新建 agent 功能不可用」。

修好后：在 agent 创建表单里填子代理条目、再回去改字段，都只是在草稿文档里搬动数据，
**只有底栏 `s`（Save）那一次写库**，且写出的 JSON 不含空的 definition key。

## Current state（执行者零上下文，全部内联）

### 相关文件

- `pkg/tui/config_panel.go`（4075 行）——`/config` 面板：section 页、行页、模板页、创建表单，以及
  面板**唯一的写库路径** `p.save` / `p.assign`。
- `pkg/tui/config_panel_schema.go`——section/字段/模板描述表（只读参考，本次不改）。
- `pkg/tui/config_panel_test.go`（5627 行）——面板测试；两种驱动器：
  `runConfigPanel`（`configPanelTestSession` 双替身，不校验）与 `runConfigPanelOn`（可传真实
  `*ChatSession`，写库走 store 真校验器）。注意 `scriptedSelector.SelectRich`（`pkg/tui/commands_test.go:138-150`）
  找不到 label 时**静默**返回 `(-1,false)`，所以步骤序列错位会表现成「某个定义不存在」，看着像产品 bug。
- `pkg/config/load.go:1052-1058`——loader 的 id 规则，报错句就在这里：
  ```go
  func validatePrimaryAgents(r *Root) error {
      ...
      for id, def := range r.Agents.Definitions {
          if !primaryAgentIDPattern.MatchString(id) {
              return fmt.Errorf("agents.definitions.%s: invalid agent id", id)
          }
  ```
  `id == ""` 就是截图里那两句点号之间什么都没有的错误。
- `pkg/state/config_store.go:133-144`——`config section <name>: <err>` 前缀的来源
  （`InvalidSectionError.Error()`，类型名就叫 `InvalidSectionError`），证明这句话来自 store 的写校验，
  不是面板自己编的。
- `pkg/config/agents.go:521-523`——自定义子代理条目**必须**有 system prompt
  （`%s: system_prompt required`），这决定了回归测试要填哪些字段（见 Step 5）。

### 面板的「写入闸门」现状（`pkg/tui/config_panel.go`）

结构体字段（`:178-185`）：

```go
	// staged holds every write: the create form edits its whole definition in
	// the section document and commits it in one write (owner 2026-11-22).
	// While it is set, assign/assignModel stage the value in the document and
	// never reach the store; the create form's submit clears it for that one
	// write.
	staged bool
```

两个读点（`:1401` 在 `assignModel`，`:1454` 在 `assign`）：

```go
	if p.staged {
		return updated, ""
	}
	if err := p.save(updated); err != nil {
```

三个写点（全仓 `p.save` 只有这三处；`p.write` 只被 `p.save` 调用，`p.session.UpdateConfigSection`
只出现在 `p.write` 的闭包里）：

```go
// :1404 (assignModel)      if err := p.save(updated); err != nil { ... }
// :1457 (assign)           if err := p.save(updated); err != nil { ... }
// :3355 (addSubagentEntry) if err := p.save(draft); err != nil { notice = err.Error(); continue }
```

agent 创建表单把草稿挂在**无名 key** 上（`:3113 stageAgentDraft`，提交是 `:3240 submitAgentDraft`）：

```go
func (p *configPanel) stageAgentDraft(doc any, path []string, draft map[string]any) any {
	defs, _ := configLookupValue(doc, path)
	rows, _ := defs.(map[string]any)
	...
	rows[""] = draft            // 草稿：definitions[""]
	updated, err := configAssign(doc, path, rows, false, "Agent definitions")
```

```go
func (p *configPanel) addAgentDefinition(doc any, path []string, field configField, container any) (any, string) {
	_ = container
	draft := map[string]any{"primary": true}
	doc = p.stageAgentDraft(doc, path, draft)
	p.staged = true                       // :3043
	defer func() { p.staged = false }()    // :3044
	...
	doc, notice = p.openTemplateField(doc, configJoinPath(path, ""), row.field, configTemplateAgent)  // :3101
```

```go
func (p *configPanel) submitAgentDraft(doc any, path []string, draft map[string]any, key string) (any, string, bool) {
	if notice := p.agentNameRefusal(doc, path, key); notice != "" { return doc, notice, false }
	defs, _ := configLookupValue(doc, path)
	rows, _ := defs.(map[string]any)
	delete(rows, "")                      // 唯一一次写库前丢掉占位 key
	rows[key] = draft
	previous := p.staged
	p.staged = false
	updated, note := p.assign(doc, path, rows, false, "Agent definitions")
	p.staged = previous
	...
```

子代理条目创建表单（`:3275 addSubagentEntry`）——**agent 表单里的 Subagents 行也可以开它**
（入口 `:2905-2911 addRow`：`if list && agentSubagentsPath(path) { return p.addSubagentEntry(doc, path, field) }`）：

```go
	rowPath := configJoinPath(path, strconv.Itoa(index))
	p.staged = true                       // :3298
	defer func() { p.staged = false }()   // :3299   ← 布尔开关：内层退出把外层一起清了
	...
		case res.Action == FooterActionSave || ...:
			if refusal := subagentNameRefusal(draft, path, name); refusal != "" { notice = refusal; continue }
			subagentDraftEntry(draft, rowPath)["name"] = name
			if err := p.save(draft); err != nil {    // :3355  ← 直写整份深拷贝
				notice = err.Error()
				continue
			}
			return p.sectionDoc(p.section), ""
```

其中 `draft` 是**整节文档的深拷贝**（`:3276-3296`：`configMarshalDoc(doc)` → `json.Unmarshal`），
所以外层创建表单挂在 `definitions[""]` 上的草稿也跟着被拷进去。

### 已复现的根因链（本会话在真实 store 校验器上跑出来的）

契约：`plans/351-config-agent-create-form-first.md:127` 与 `:612` 明写——
「任何一条写路径漏认 `p.staged`，会把 `definitions[""]` 写进 store，loader 以
`agents.definitions.: invalid agent id` 拒绝」。（那份计划把 `p.staged` 称作「开关」，本计划把它改成
计数后该措辞过时，见 Maintenance notes。）

1. 用户 `Agent definitions` → `a`（Add）→ agent 创建表单；草稿在 `definitions[""]`，`staged=true`。
2. 填 Name；打开 **Subagents** 行 → `+ Add item` → 填子代理 Name → Save。
3. `addSubagentEntry` 走 `p.save(draft)`：`draft` 是深拷贝，里面带着 `definitions[""]`
   ⇒ store 拒绝 `config section agents: agents.definitions.: invalid agent id`（store 未变）。
4. 用户 Esc 回 agent 表单，再点任意字段（例如 Model，选 `deepseek/deepseek-flash`）：
   此时 `p.staged` 已被内层表单的 `defer` 清成 `false`，于是 `assign` 直接写库，
   而文档里 `definitions[""]` 还在 ⇒ **同一句话第二次出现，就发生在 agent 表单上（截图）**。
   表单值仍显示，是因为表单从内存里的 `draft`/`doc` 画行，与写库成败无关。

复现命令与失败输出（写计划时在临时测试里跑过；一次独立复核也在 `80bb73b4` 的临时 worktree 里
复跑过「修前红 → 修后绿 → 只回退提交分支再红」，正式版见 Test plan）：

```
$ CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestZZRepro' -count=1 -v
    zz_repro3_test.go:34: pages:
        ...
        config section agents: agents.definitions.: invalid agent id     ← 内层表单提交
        ...
        config section agents: agents.definitions.: invalid agent id     ← 外层表单选 Model
    zz_repro3_test.go:36: stored agents: {"definitions":{"main":{"primary":true}}, ...}   ← 一个字节都没写
```

对照：只填 Name/Display name/Description/Model 再 Save 的普通创建**写入正常**
（`{"definitions":{"frontend-expert":{"primary":true,"display_name":"前端专家",...,"default_model":"llm-…"},"main":{...}}}`），
所以 bug 只在「创建表单里再开内层创建」这条链上。

### 同类问题（owner 要求一并修，已穷尽核对）

- 唯一一条绕过闸门的写：`addSubagentEntry` 的 `p.save(draft)`（三处 `p.save` 里唯一非 `assign` 的）。
  它同时造成两个症状：内层提交被拒（V1）、外层表单失去闸门（V2）。
- `staged` 用**布尔**表达一个可以嵌套的状态（外层 agent 创建 + 内层子代理创建），是 V2 的机器原因；
  改成计数后「内层退出把外层解锁」这类问题整体消失。
- 其它同类面已核对为零：`stageAgentDraft` 是唯一的匿名占位键机制；`agentSubagentsPath`
  （`:1861-1862`，只查路径形状）是两个创建表单之间唯一的嵌套入口；`config_panel_schema.go` 无写点。
- 修完后写库只有 `assign`/`assignModel` 一条路（外加 `submitAgentDraft` 显式的「这一次写」）。

## Commands you will need

按项目 owner 裁决，本次**直接在当前源码树实施，不用 worktree**，下面命令在主树可直接跑。
若执行者被派进一个 fresh worktree，先按项目既有做法接线 cgo 原生依赖
（`third_party/pdfium` 的 `PKG_CONFIG_PATH`＋ `libtokenizers`）再跑。

| 用途 | 命令 | 成功预期 |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| 定点测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestConfigPanel' -count=1` | 全 PASS |
| 包级回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/config -count=1` | 全 PASS |
| 格式 | `gofmt -l pkg/tui` | 无输出 |
| vet（CI 同款） | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` | exit 0 |
| 真机验收构建 | `CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` | exit 0（**必须带 `-tags fts5`**，否则 TUI 起不来：`no such module: fts5`） |

## Scope

**In scope**（只准改这两个文件 + 计划索引）：
- `pkg/tui/config_panel.go`
- `pkg/tui/config_panel_test.go`
- `plans/README.md`（只追加本计划的状态行）

**Out of scope**（看起来相关但不要动）：
- `pkg/config/load.go` 及 `pkg/config/agents_test.go`：loader 的 id 规则是对的，本次只改面板写入。
- `pkg/gateway/agents_api.go`（`:579` 自己的 `invalid agent id` 拒绝）：web 端没有「草稿占位 key」
  这套机制（它直接校验 id），与本 bug 无关。
- `pkg/state/**`（store 校验器）、`pkg/process/**`：写校验与 adopt 链不动。
- `plans/config-tui-prototype.html` / 原型 `.test.cjs`：本次无面板文案变化，原型哈希不动。
- `plans/367-*`、`plans/368-*` 与别人正在改的 `pkg/tui` 渲染文件（消息行/卡片）：不碰、不 revert。
- `pkg/tui/zz_repro*_test.go`：写计划时的临时复现文件，**删除**，不要留在树里。

## Steps

### Step 1: 删掉临时复现文件，确认基线

```bash
rm -f pkg/tui/zz_repro_test.go pkg/tui/zz_repro2_test.go pkg/tui/zz_repro3_test.go
git status --porcelain -- pkg/tui/config_panel.go pkg/tui/config_panel_test.go
```
**Verify**: 上面 `git status` **输出为空**（路径限定到两个 in-scope 文件，别人的 WIP 不算）。

### Step 2: 把 `staged` 从布尔改成计数，并给出持有/归还一对函数

在 `pkg/tui/config_panel.go` 里：

1. 字段（`:178-185`）改成计数，注释说明它为什么必须计数（一个创建表单可以套在另一个创建表单里）：

```go
	// staged counts the staged pages open: a create form edits its whole
	// definition in the section document and commits it in one write (owner
	// 2026-11-22). While it is non-zero, assign/assignModel stage the value in
	// the document and never reach the store; a create form's submit spends its
	// own hold for that one write. It counts rather than switches because one
	// create form can open inside another (an agent's Subagents row, inside the
	// agent create form): the inner page's end must leave the outer form staged,
	// or the outer form's next edit would write the unnamed draft key and the
	// loader would refuse the whole section (agents.definitions.: invalid agent
	// id).
	staged int
```

2. 在结构体下面、`newConfigPanel` 之前加一对函数：

```go
// stage holds the panel's writes for one staged page and answers the function
// that gives the hold back. Every page that stages takes its own hold, so a
// page opened inside another (an agent's Subagents row, inside the agent create
// form) hands back only its own.
func (p *configPanel) stage() func() {
	p.staged++
	return func() { p.staged-- }
}

// unstage spends the open page's own hold for the one write its submit makes,
// and answers the function that gives it back. A page sitting inside another
// keeps the outer hold, which is what leaves the outer create form staged.
func (p *configPanel) unstage() func() {
	p.staged--
	return func() { p.staged++ }
}
```

3. 顶层注释「the only page that stages is the create form (staged)」随事实更新
   （现在是两个创建表单：agent 与 subagent 条目）。

**Verify**: `gofmt -l pkg/tui` 无输出。此时 `CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` 会因
布尔赋值处报 `cannot use true (untyped bool constant) as int`，这是预期的（Step 3 收尾），不要中途提交。

### Step 3: 三处「拉闸/放手」改成借还自己那一层（**本步结束时 build 必须绿**）

- `:1401`（`assignModel`）与 `:1454`（`assign`）：`if p.staged {` → `if p.staged > 0 {`。
- `addAgentDefinition`（`:3043-3044`）：两行换成
  ```go
  defer p.stage()()
  ```
- `addSubagentEntry`（`:3298-3299`）：两行换成
  ```go
  defer p.stage()()
  ```
  （**必须在本步做**：不改这两行，`staged` 变成 `int` 后编译不过。）
- `submitAgentDraft`（`:3248-3251`）：把「清空 / 还原」换成借出 / 归还自己那一层：
  ```go
  restore := p.unstage()
  updated, note := p.assign(doc, path, rows, false, "Agent definitions")
  restore()
  ```
  （agent 创建表单永远是最外层，`unstage()` 后计数为 0，这就是「提交那一次写」。）

**Verify**:
```bash
CGO_ENABLED=1 go build -tags fts5 ./pkg/tui          # exit 0
grep -n "p.staged" pkg/tui/config_panel.go           # 只有 stage()/unstage() 实现 + 两处 "p.staged > 0"
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestConfigPanelCreatesAnAgentDefinition|TestConfigPanelCreatesASubagentEntry' -count=1   # PASS
```

### Step 4: 子代理条目的提交改走 `p.assign`（先借出自己那一层）

`addSubagentEntry` 的提交分支（`:3349-3359`，Step 3 之后行号会略移）改成：

```go
		case res.Action == FooterActionSave || (saveRow >= 0 && res.OK && res.Row == saveRow):
			if refusal := subagentNameRefusal(draft, path, name); refusal != "" {
				notice = refusal
				continue
			}
			subagentDraftEntry(draft, rowPath)["name"] = name
			// The entry is the form's own element in a copy of the section; the
			// submit applies it to the document the caller holds, through the
			// panel's one write path. This form's own hold is spent first, so a
			// create form this one sits inside (an agent's Subagents row, inside
			// the agent create form) keeps its hold: there the entry lands in
			// that form's document and that form's one write carries it.
			list, _ := configLookupValue(draft, path)
			restore := p.unstage()
			updated, note := p.assign(doc, path, list, false, field.Label)
			restore()
			if note != "" {
				notice = note
				continue
			}
			return updated, ""
```

（`list` 是拷贝里已经带上具名新条目的那张表；`p.assign` 用 `configAssign` 原地把它替换回调用方的
文档——`configAssign` 对已存在的容器是原地改写（`:3792-3820`），所以外层创建表单持有的
`draft` 变量与文档里的 `definitions[""]` 是同一个 map，条目会被外层那次提交一起写出去。
`restore()` 必须在 `continue`/`return` 之前执行。）

同时：

1. 函数头注释「the copy dies with the page, so the submit is the only write」改成事实：
   拷贝随页面消亡，提交把条目**应用到调用方的文档**，写不写库由面板的闸门决定。
2. 成功时 `return p.sectionDoc(p.section), ""` 换成 `return updated, ""`，理由写进注释：
   `sectionDoc` 对 agents 段只多一步 `dropRetiredAgentKeys`（`:830-836`），而传进
   `addSubagentEntry` 的 doc 早已过这一步，两者等价；返回 `updated` 是嵌套时唯一正确的选择
   （store 里还没有这次编辑）。

**Verify**:
```bash
CGO_ENABLED=1 go build -tags fts5 ./pkg/tui        # exit 0
grep -n "p.save(" pkg/tui/config_panel.go          # 只有两处，都在 assign/assignModel 里
grep -n "p.save(draft)" pkg/tui/config_panel.go    # 无匹配
gofmt -l pkg/tui                                   # 无输出
```

### Step 5: 回归测试（真实 store 校验器 + 双替身两条腿）

写在 `pkg/tui/config_panel_test.go` 末尾（新函数，别改既有用例）。

**(a) 真实 store：`TestConfigPanelSubagentInsideAgentCreateStaysStaged`**
建场照抄 `TestConfigPanelModelPickerWritesPassTheStoreValidator`（`:4716`）：
`t.TempDir()` + 写 `forebrain.yaml`（`gateway:\n  http_addr: 127.0.0.1:9\n`）+ `state.OpenStateForTest`
+ `state.LLMModelStore.Create`（`deepseek/deepseek-flash`）+ `process.InstallConfigSectionValidator(cfgPath, db)`
+ `defer state.SetSectionValidator(nil)` + `env := &process.Environment{Root: home, SQL: db}`、
`env.ConfigPath = cfgPath`、`session := &ChatSession{Env: env}`；先
`session.UpdateConfigSection(ctx, "agents", json.RawMessage(`{"definitions":{"main":{"primary":true}}}`))` 播种。
用 `runConfigPanelOn(t, session, selector, steps, inputs, "agents")` 驱动这条序列（**注意 Esc 次数**：
内层提交成功后 `editRows` 继续画子代理列表页，所以只需 1 次 Esc 回到 agent 表单）：

```
Agent definitions → + Add item → Name(输入 frontend-expert)
  → Subagents → + Add item → Name(输入 helper) → System prompt(输入 You help.) → Save   // 内层：不得写库
  → Esc                                        // 回到 agent 创建表单（1 次，不要两次）
  → Model → deepseek/deepseek-flash → Save     // 外层：整份配置唯一一次写库
  → Esc → Esc                                  // 收尾退出
```

（自定义子代理条目**必须**有 system prompt，否则修好后外层提交会被 loader 以
`... subagents[0]: system_prompt required` 拒——见 `pkg/config/agents.go:521-523`。）

断言：
1. `pageLabels(selector)` 与 `frameText(renderer)` 全程不含 `invalid agent id`，
   也不含 `system_prompt required`；
2. store 里 `Stored["agents"]`：`definitions["frontend-expert"]` 存在、
   `definitions[""]` 不存在、`definitions["frontend-expert"]["subagents"]` 是长度为 1 的列表且
   `subagents[0]["name"] == "helper"`、`subagents[0]["system_prompt"] == "You help."`、
   **`definitions["frontend-expert"]["default_model"]` 非空且等于刚建的模型行 id**
   （这条钉子必要：Model 步骤若静默 miss，前面的断言照样绿，V2 就没被钉住）；
3. `definitions["main"]` 仍在（整节没被覆盖）。

**(b) 双替身：`TestConfigPanelNestedSubagentCreateWritesOnce`**
用 `session := newConfigPanelSession(map[string]string{"agents": `{"definitions":{"main":{"primary":true}}}`})`
+ `runConfigPanel` 驱动同一序列。**双替身的 `snapshot.Models` 是空的**（`newConfigPanelSession`
只填 `Stored`），而 `panel.models = snapshot.Models`（`config_panel.go:212`）——不填模型行，Model 步骤
会静默 miss（`scriptedSelector.SelectRich` 找不到 label 返回 `(-1,false)`），这一腿就白跑。所以先补：

```go
session.snapshot.Models = []appcfg.LLMModelConfig{{ID: "llm-1", Provider: "deepseek", Model: "deepseek-flash"}}
```

picker 的行标签由 `llm.FormatProviderModelDisplay(provider, model)` 给出，即 `deepseek/deepseek-flash`
（与真实 store 那一腿、与 owner 截图里的值一致）。断言：
1. 外层 Save 之前零写入——用「内层 Save 之后、外层 Save 之前截断成一个子用例」来观测
   （`{ok:false}` 收尾后断言 `writes` 为空），这条钉住 V1 的回归；
2. 完整序列跑完 `len(session.writes) == 1`，且 raw 里同时出现 `"frontend-expert":{`、
   `"subagents":[{`、`"name":"helper"` 与 **`"default_model":"llm-1"`**，且 `"":` 一次都不出现
   （`default_model` 那条同样是「Model 步真的走到了」的钉子）。

**(c) 既有用例必须原样绿**（不改断言）：`TestConfigPanelCreatesASubagentEntry`（`:2607`，保存定义页里
的普通子代理创建仍立即写一次并回到列表）、`TestConfigPanelCreatesAnAgentDefinition`（`:2901`）、
`TestConfigPanelModelPickerWritesPassTheStoreValidator`（`:4716`）。

**Verify**:
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestConfigPanelSubagentInsideAgentCreateStaysStaged|TestConfigPanelNestedSubagentCreateWritesOnce|TestConfigPanelCreatesASubagentEntry|TestConfigPanelCreatesAnAgentDefinition|TestConfigPanelModelPickerWritesPassTheStoreValidator' -count=1 -v
```
→ 全 PASS。

**反向验证（必做，是「修复真断了因果链」的证据）**：只把 Step 4 的提交分支临时改回修复前的样子
（`if err := p.save(draft); err != nil { notice = err.Error(); continue }` + `return p.sectionDoc(p.section), ""`），
**类型不要动**（现在是 `staged int`，写 `p.staged = false` 编译不过）：新用例 (a)(b) 必须变红并打印
`agents.definitions.: invalid agent id`；再改回来恢复绿。把这次红/绿写进验收记录。

### Step 6: 包级回归 + 门禁

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/config -count=1
CGO_ENABLED=1 go build -tags fts5 ./...
gofmt -l pkg/tui
CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui
```
**Verify**: 两个测试包全 PASS；build exit 0；gofmt 无输出；vet exit 0。

### Step 7: tmux 真机验收（测试绿 ≠ 真机可用）

**在 worktree 里跑 worktree 的二进制**（`$PWD/build/bin/forebrain`）。⚠ 主树
`/Users/doudou/workspace/unionj-cloud/forebrain-harness/build/bin/forebrain` 是旧构建
（`go version -m` 显示 `vcs.revision=63f26ce8…`，11-21），跑它验的是别的代码。

```bash
CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain
go version -m build/bin/forebrain | grep -E "vcs.revision|vcs.modified"   # 记下这两行作为「跑的是哪个构建」的证据
BIN="$PWD/build/bin/forebrain"
H=$(mktemp -d); P=$(mktemp -d)
tmux new-session -d -s cfg370 -x 130 -y 42 "cd $P && FOREBRAIN_HOME=$H $BIN; echo EXIT_CODE=\$?; sleep 600"
tmux capture-pane -t cfg370 -p > /tmp/cfg370-evidence/0-start.txt   # 过 onboarding（6 步）后再进 /config
tmux send-keys -t cfg370 '/config' Enter
```

按屏取证：每段都必须 `tmux capture-pane -t cfg370 -p > /tmp/cfg370-evidence/<n>-<what>.txt`，
**存下来的文件里要能看见 `/config` 面板本身**（tab 栏 + 行），只有启动 banner 的截图不算证据。
方向键按实际屏面调整行数：

1. **happy path 回归**：进 `Agents & tools` → `Agents` → `Agent definitions` → `a` → 填 Name
   `frontend-expert` → `s`（Save）→ 成功，屏上无 `invalid agent id`。
2. **本 bug 路径**：再 `a` → Name `probe-agent` → 进 `Subagents` → `+ Add item` → 填 Name `helper`、
   System prompt 一句 → `s`（Save）→ **屏上不得出现 `invalid agent id`**，返回子代理列表能看到
   `helper` → Esc 回 agent 表单 → 打开 `Model` 选一行 → `s`（Save）→ 返回 Agent definitions
   列表并出现 `probe-agent`。
3. **落库核对**（不靠界面；`fb_config` 的列是 `value_json`，不是 `raw`）：
   ```bash
   sqlite3 "$H/state/forebrain.state.sqlite" "select value_json from fb_config where section='agents';"
   ```
   期望：`definitions` 下有 `probe-agent`（含 `subagents:[{name:"helper",system_prompt:...}]`）与
   `frontend-expert`，`"":` 一次都不出现。
4. **中断自救**：`a` 开一个新表单 → 填 Name → Esc → 再进 Agent definitions，列表里不得多出半条定义
   （Esc 不写库）；Ctrl+C 退出后 `EXIT_CODE=0`。

收尾：`tmux kill-session -t cfg370`；`rm -rf "$H" "$P"`；不得污染真实 `~/.forebrain*`。

### Step 8: 收口

- 计划落盘 `plans/373-config-create-agent-invalid-id.md`（内容即本计划；若 373 已被占用按文件头的
  「编号」段取下一个空号），并在 `plans/README.md` 的「计划列表」表**追加**一行，列序与该表一致
  （表头在 `plans/README.md:1696`：`# | 标题 | 类别 | 状态 | 优先级 | 工作量 | 风险`，# 列是链接）：
  ```markdown
  | [373-config-create-agent-invalid-id](373-config-create-agent-invalid-id.md) | /config 创建 agent 写 `definitions[""]` 致 `agents.definitions.: invalid agent id`（创建表单里的子代理创建直写整节深拷贝＋`staged` 布尔开关被内层清掉） | Bug Fix | DONE（2026-11-23 真机验收） | **P1** | S | LOW |
  ```
- 回填「验收记录」：Step 7 的四段证据 + Step 5 的反向验证红/绿。
- 改动留工作区，不 commit（owner 手动提交）。
- 报告：根因链、最小改动清单、验证证据、遗留限制。

## Test plan

- 新用例：`TestConfigPanelSubagentInsideAgentCreateStaysStaged`（真实 store）、
  `TestConfigPanelNestedSubagentCreateWritesOnce`（双替身，须填 `snapshot.Models`），
  位置 `pkg/tui/config_panel_test.go` 末尾。
- 结构范例：真实 store 用例照抄 `TestConfigPanelModelPickerWritesPassTheStoreValidator`（`:4716`）；
  双替身用例照抄 `TestConfigPanelCreatesAnAgentDefinition`（`:2901`）与
  `TestConfigPanelCreatesASubagentEntry`「one save lands the entry」子用例（`:2645`，含
  `agentSubagentsSteps`/`savedAgentsDoc` 这类既有辅助）。
- 回归：`TestConfigPanelCreatesASubagentEntry`、`TestConfigPanelCreatesAnAgentDefinition`、
  `TestConfigPanelModelPickerWritesPassTheStoreValidator` 必须零改动通过。
- 失败路径：反向验证（Step 5 末）——只回退提交分支，新用例必须变红且报 `invalid agent id`。

## Done criteria

全部成立才算完成：

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/config -count=1` 全 PASS
- [ ] `gofmt -l pkg/tui` 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` exit 0
- [ ] `grep -n "p.save(" pkg/tui/config_panel.go` 只有两处，且都在 `assign`/`assignModel` 内
- [ ] `grep -n "p.save(draft)" pkg/tui/config_panel.go` 无匹配
- [ ] `grep -n "staged bool" pkg/tui/config_panel.go` 无匹配；`grep -c "p.staged" pkg/tui/config_panel.go`
      = **6**（`stage()` 2 行 + `unstage()` 2 行 + 两处 `p.staged > 0`）
- [ ] 两个新用例存在且 PASS；反向验证红过（记录在验收记录里）
- [ ] `git status --porcelain -- pkg/tui/config_panel.go pkg/tui/config_panel_test.go` 只列出这两个文件；
      `pkg/tui/zz_repro*_test.go` 已删（`git status --porcelain | grep zz_repro` 无输出）
- [ ] tmux 真机四段证据齐全，`select value_json from fb_config where section='agents'` 里没有 `"":`
- [ ] `plans/README.md` 状态行已追加，列序与表头一致

## STOP conditions

- Drift check 的两条命令有非空输出，且 `pkg/tui/config_panel.go` 的「Current state」摘录与现状
  **内容**不符（行号漂移不算）。
- Step 3 结束时 `CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` 仍红：说明还有别的布尔赋值处没改到
  （`grep -n "p.staged = " pkg/tui/config_panel.go` 应为空），先查清再继续。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestConfigPanelCreatesASubagentEntry' -count=1`
  在**未改它断言**的前提下变红：说明 Step 4 改变了已定义页里的普通子代理创建语义，停下来上报。
- 反向验证里新用例**没有**变红：说明没钉住根因，不要以「测试反正绿」收工。
- 发现修这条链需要动 `pkg/config/**`、`pkg/state/**` 或 gateway：那是把 id 规则搬走，属于另一件事，
  停下来上报。
- 真机验收时 `capture-pane` 显示 `no such module: fts5`：那是构建漏了 `-tags fts5`，按计划命令重做，
  不要把它当产品 bug 去修。
- `pkg/tui` 里别人正在改的文件导致包级测试红：不要去修别人的代码，用 `peer_send` 找对应 agent，
  或只跑本计划相关的 `-run 'TestConfigPanel'` 子集并如实上报。

## Maintenance notes

- 本仓现在有**两条**创建表单会拉闸：agent 创建（`addAgentDefinition`）与子代理条目创建
  （`addSubagentEntry`），后者可以从前者里面开。以后再加任何「提交时写库」的页面，必须走
  `p.assign`/`p.assignModel`（或用 `p.stage()`/`p.unstage()` 借还自己那一层），**不得**直接调 `p.save`；
  否则签名依旧是 `agents.definitions.: invalid agent id`，而双替身单测抓不到——只有真实 store
  校验器那一腿能抓到，所以新写路径要同时加一条真实 store 用例。
- 评审要盯三点：`stage()`/`unstage()` 的借还配对（每个 `defer p.stage()()` 与每次
  `restore := p.unstage()` 都要有对应归还，且 `restore()` 在 `continue`/`return` 之前执行）、
  嵌套时「借出的只是自己那一层」、以及删掉 `p.save(draft)` 之后 `p.raw` 是否仍与 store 同步
  （由 `p.assign` 的 `p.save` 保证）。
- 明确不做的：loader 的 id 规则（`pkg/config/load.go`）、web 端 agent 创建（没有占位 key 机制）、
  原型 HTML 与面板文案（本次无文案变化，原型哈希不动）。
- 文档口径：`plans/351-config-agent-create-form-first.md:127,612` 把 `p.staged` 称作「开关」，
  计数化后该措辞过时；本计划不改那份历史计划，但下一位读者应以本计划的计数语义为准。
- 已知相邻但不在本计划范围（各一行，不要顺手改）：
  `addAgentDefinition` 里 Primary→subagent 角色切换会清掉刚 staged 的 `subagents`
  （`:3224-3228`，plan 351 既定语义）；双替身的「内层 Save 时零写入」只能由分段子用例观测，
  完整序列只能反推总写入次数为 1。
- 如果以后把草稿从 `definitions[""]` 改成别的载体（例如面板自己持有 map），
  `stageAgentDraft`/`submitAgentDraft` 与上面两条测试要一起改。
## Implementation Status

**Verdict: APPROVE**（2026-11-23，评审方＝advisor 会话自己复核，未合并、未 push）

| 项 | 值 |
|---|---|
| Worktree / 分支 | `.worktrees/config-agent-create-invalid-id` ／ `config-agent-create-invalid-id`（基线 `dc93b1ac`） |
| 提交 | `0e51ab56 fix(tui): stage the subagent create through the panel's one write path`（2 文件 +229/−25） |
| 执行器 | 模型 `alibaba/deepseek-v4.1-flash-1017`（owner 指定），0 轮改写 + 1 轮补验收证据 |

**逐项（每项都是我本人在 worktree 里亲自跑出来的，不是转述执行器）**

| Done criterion | 结果 |
|---|---|
| `CGO_ENABLED=1 go build -tags fts5 ./...` | PASS（exit 0） |
| `go test -tags fts5 ./pkg/tui ./pkg/config -count=1` | PASS（`pkg/tui 206.198s`、`pkg/config 1.467s`） |
| 五条定点用例（2 新 + 3 既有） | 全 PASS |
| `gofmt -l pkg/tui` / `go vet -tags fts5 ./pkg/tui` | 无输出 / exit 0 |
| `grep -n "p.save("` | 只有 2 处（`assignModel` :1427、`assign` :1480） |
| `grep -n "p.save(draft)"` / `"staged bool"` / `"p.staged = "` | 均无匹配 |
| `grep -c "p.staged"` | 6（`stage()` 2 + `unstage()` 2 + 两处 `> 0`） |
| 反向验证（`-overlay` 只回退提交分支） | 新用例 (a)(b) 全红并打印 `config section agents: agents.definitions.: invalid agent id`；撤 overlay 恢复绿 |
| 真机 tmux 四段 | PASS：pane 截图含面板本体（`/tmp/cfg370-evidence/` 17 份），`exited` 前后 `value_json` 对照、`grep -c '"":' = 0`、`EXIT_CODE=0` |
| 「跑的是哪个构建」 | worktree 二进制（sha256 `2c2bd7d5…`、`CGO_LDFLAGS` 指向本 worktree、符号表含修复后独有的 `.stage.func1`）；主树 11-21 二进制同一 grep 0 命中 |
| Scope | `git diff --stat dc93b1ac..HEAD` 仅 `pkg/tui/config_panel.go`、`pkg/tui/config_panel_test.go`；`git status` 干净 |

**真机关键证据（可复核）**：外层 Save **之前** `select value_json …` → `definitions` 键
`['frontend-expert','main']`、`probe-agent` 为 `null`、`"":` 计数 0（＝选完 Model 后闸门没放）；
**之后** → 键 `['frontend-expert','main','probe-agent']`，`probe-agent` 带
`default_model` 与 `subagents:[{"name":"helper","system_prompt":"You help."}]`，`"":` 仍为 0；
Esc 后键集不变（无半条定义）。

**偏差与判断（均已接受）**

1. 计划 Step 7 首版把 tmux 启动命令写成**主树**二进制路径，而构建输出在 worktree —— 是计划缺陷，
   不是执行器偷懒；已就地改为 `$PWD/build/bin/forebrain`，并要求用「构建命令 + sha256 + mtime + 符号表」
   证明构建身份（linked worktree 的 `vcs.revision` 不可靠，实测报的是主树 revision 且 `modified=true`）。
2. 执行器首轮的 `/tmp/cfg370-evidence/` 四份捕获只有启动 banner（无面板），故退回补做一轮；
   第二轮每份文件都含面板本体与 DB 命令+输出，才被接受。
3. 真机 onboarding 用全新 HOME（6 步），模型行为 onboarding 落下的 `deepseek/deepseek-chat`
   （计划文里写的 `deepseek/deepseek-flash` 是测试替身那张表的行）；同一 picker、不影响结论。
4. 计划编号 370 → 372 → **373**（落盘期间 370/371 已被 process 两个计划占用，372 又被另一个
   process 计划抢走）。
5. 本机 env：fresh worktree 需软链 `third_party/{pdfium,libtokenizers}`，且 `CGO_LDFLAGS` 要带
   `-Wl,-rpath,<PDFIUM_DIR>/lib`，否则 `pkg/config` 测试二进制 dyld 起不来（与本次改动无关）。

**未做 / 待 owner**：合并该分支（连同 worktree 的删除）由 owner 决定；`plans/` 落盘与
`plans/README.md` 索引行由评审方在合并前补。
