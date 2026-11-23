# Plan 141：子代理视图里的技能命令真实生效（含插话里的技能激活）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——目标文件必须干净**：
> `git status --porcelain pkg/tui/run.go pkg/tui/run_test.go pkg/gateway/api_extra.go pkg/gateway/api_extra_test.go pkg/run/turn_input.go pkg/run/skills.go pkg/run/orchestration_llm.go pkg/run/orchestration_llm_test.go pkg/run/subagent.go pkg/run/subagent_test.go`
> 必须**无输出**。写计划时（`82ab168`）`pkg/gateway/api_extra.go` 上挂着 135–138 批次未提交的改动，由 owner 提交后才能开工。
>
> **Drift check（先跑）**：`git diff --stat 82ab168 -- pkg/tui/run.go pkg/gateway/api_extra.go pkg/run/turn_input.go pkg/run/skills.go pkg/run/orchestration_llm.go pkg/run/subagent.go pkg/turn/slash.go`
> 有输出就按下文"现状"里的**函数名**逐段核对；被引用代码的含义变了即 STOP 1。下文行号是 `82ab168` 时的近似值。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 5）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/turn ./pkg/tui ./pkg/gateway -count=1`（超时给足 600 s）。
> 基线里已经红的是存量问题，**不是你要修的**；Done criteria 只要求"不新增红"。

## Status

- **Priority**: P1（owner 2026-10-26 报的现网 bug，"必须一并修复，加入本期计划"）
- **Effort**: M
- **Risk**: MED–HIGH（Step 4 改的是所有代理共用的编排循环里插话注入那三处；只对**带技能选择的插话**生效，普通插话与主代理行为不变）
- **Depends on**: 无（与 139/140 互不依赖）；**142 依赖本计划**
- **Category**: bug
- **Planned at**: dev `82ab168`，2026-10-26

## Why this matters

owner 的原话：

> 用户在子代理视图发送/frontend-design 这个技能的激活命令，没有触发激活，llm没有读到这个技能，必须一并修复，加入本期计划，子代理视图中用户插话中的技能激活命令必须真实生效

终端里的现象：在子代理视图输入 `/frontend-design`，得到一行
`frontend-design · Run this from the main view (press esc to return).`，技能根本没发给子代理。

查下来是**三层**问题，本计划全部修掉：

1. **路由错判（直接原因，终端与网页都有）**：`turn.Find(name)`（`pkg/turn/slash.go:199`）遍历的是 `All()`，而 `All()`
   把已注册的技能命令也算进去（`:136-153`，技能命令的 `SubagentView` 被设成 `SubagentViewActs`）。
   - 终端 `subagentViewSlash`（`pkg/tui/run.go` 约 `:2410-2447`）用 `builtin, isBuiltin := turn.Find(name); if !isBuiltin { …技能… }`
     判断"是不是技能"——真实注册的技能 `isBuiltin` 为 true，于是落进 `switch builtin.SubagentView` 的 `SubagentViewActs`
     分支，名字不是 `compact`/`context`，走 `default` 回 `subagentViewMainOnlyReply`。
   - 网关 `subagentCommandInput`（`pkg/gateway/api_extra.go` 约 `:2880-2905`）同样 `if _, isBuiltin := turn.Find(name); isBuiltin { return subagentViewCommandCode, false }`，
     网页子代理标签里发技能命令被拒为"这个命令属于主对话"。
   - 现有测试没发现，是因为它们没往动态注册表里注册真实技能，`turn.Find` 找不到才走到了技能分支。
2. **子代理运行中的插话丢掉技能**：用户在运行中的子代理视图里发消息，走的是插话通道 `InputQueue.Steer`
   （`pkg/run/turn_input.go` 约 `:579-617`），它只把 `Parts`/`Text` 放进 `TurnInputEntry`，`Input.SkillName/SkillPath` 被丢弃；
   编排循环在工具边界把插话注入成 `llm.UserMessage(entry.Parts...)`（`pkg/run/orchestration_llm.go` 三处，约 `:575-580`、`:690-697`、`:803-809`），
   技能不会加载，模型读不到。owner 2026-10-26 裁定：**插话也要真实激活技能**。
3. **fork 子代理续跑时不加载技能**：显式技能的加载只在 `Runner.RunContent`（`pkg/run/runner.go:1594` `r.preloadExplicitSkills(ctx)`）里做，
   typed 子代理经执行器走的是这条路；fork 子代理的续跑 `continueForkSubagent`（`pkg/run/subagent.go` 约 `:2536-2615`）直接调 `RunFork`，
   上下文里即使带着技能选择，也既不加载、也不把技能说明放进请求。

## Current state

### 显式技能是怎么生效的（主代理的样板）

- `pkg/run/skills.go`：
  - `WithExplicitSkillSelection(ctx, name, path)` / `WithExplicitSkillSelections` 把"用户点名的技能"放进 ctx；`explicitSkillSelectionsFromContext(ctx)` 取出。
  - `(*Runner).preloadExplicitSkills(ctx)`（约 `:516-576`）：对每个选择——画一张 Skill 卡（`tool.StepEvent{ToolName: "skill", Category: "skill", SkillName, SkillPath, Origin: "explicit"}`
    的 started/completed）、`skill.LoadActivation(skillPath)` 读 SKILL.md、校验名字一致；成功后 `WithExplicitSkillActivations(ctx, activations)`；
    失败画失败卡并返回 `*ExplicitSkillLoadError`。用的是 `r.tools`（`*tool.State`）：`tool.StepHookFromContext(ctx, r.tools)`、`loadedSkillByName(r.tools, name)`（约 `:269`，取本会话钉住的版本）。
  - `explicitSkillActivationMessage(activation)`（约 `:480-499`）：一条 `IsMeta = true`、`Ephemeral = true` 的用户消息，正文
    "The user selected the `X` skill for this request. Its full instructions are included below; follow them for this turn. …" + SKILL.md 内容。
- `pkg/run/transcript.go` 约 `:138`：`transcriptSession.build` 在本回合用户消息前为每个 activation 插一条上面那种消息。

### 插话注入（编排循环）

`pkg/run/orchestration_llm.go` 的 `(*toolOrchestrationLLM).Execute`（`:158` 起，接收者 `w` 带 `w.state *tool.State`）里有三处一模一样的循环：

```go
for _, entry := range delivery.Entries() {
    session = append(session, llm.UserMessage(entry.Parts...))
}
```

分别在：被撤回插话后重发（约 `:575-580`）、最终回答后有待送插话（约 `:690-697`）、工具边界（约 `:803-809`）。
`sessionBeforeSteers = len(session)` 记在循环之前；回滚是 `session = session[:sessionBeforeSteers]`；中途压缩的分支按
`steerCount := len(session) - sessionBeforeSteers` 计数（约 `:561-571`）。**所以插进去的每条消息都必须落在 `[sessionBeforeSteers, len(session))` 这一段里**，计数和回滚才对。
fork 子代理也走这里：`r.forkLLM = r.wrapExecutionLLM(...)`（`runner.go:1235`），`wrapExecutionLLM` 内部用 `wrapToolOrchestrationLLMWithProgress` 包装（约 `:1429`）。

`TurnInputEntry`（`pkg/run/turn_input.go:21-27`）：`Mode`、`Parts`、`Seq`。`InputQueue.Steer` 末尾：

```go
parts := append([]llm.ContentPart(nil), in.Parts...)
if len(parts) == 0 {
    parts = []llm.ContentPart{llm.Text(in.Text)}
}
q.steers = append(q.steers, in)
q.rt.enqueueEntry(TurnInputEntry{Mode: TurnInputModeSteer, Parts: parts, Seq: in.Seq})
```

子代理视图里"插话已送达"的显示用的是 `entry.Parts`（`subagentChannel.publishInputDelivered`，`pkg/run/subagent.go` 约 `:749-770`）——**不要把技能说明塞进 `entry.Parts`**，否则视图里会显示整份 SKILL.md。

### 技能命令怎么到达子代理

- 终端：`subagentViewSlash` → `expandSubagentSkillCommand`（`run.go` 约 `:2450-2470`，调 `session.ExecuteSurfaceSlash` 得到 `ContinueInput`/`SkillName`/`SkillPath`）
  → `sendSubagentViewSubmission` → `ChatSession.SendToSubagent`（`pkg/tui/chat_session.go` 约 `:1526-1546`，把 `submission.SkillName/SkillPath` 放进 `run.Input`）
  → `run.SendToSubagent`：空闲则 `startUserSubagentExecution`（`subagent.go` 约 `:2142`，`hostCtx = WithExplicitSkillSelection(...)`），运行中则 `ch.queue.Steer(in)`（第 2 层的丢失点）。
- 网关：`handleSubagentInput` → `subagentCommandInput`（第 1 层的错判点）→ 展开后设 `in.SkillName/SkillPath` → `run.SendToSubagent`。

### 测试范例

- 终端：`pkg/tui/run_test.go` `TestSubagentViewSlashCommandClasses`（约 `:5905`）：`fakeSession{...}`、`renderer.NoteSubagentSpawned("task-b", ...)`、`renderer.SetActiveView("task-b")`、
  直接调 `subagentViewSlash(context.Background(), nil, nil, nil, nil, session, renderer, nil, state, nil, "task-b", "/new")`。
  `fakeSession.streamSlashReply map[string]SlashOutcome` 决定 `ExecuteSurfaceSlash` 的回答（`run_test.go:1182`）；`fakeSession.subagentSent` 记录 `SendToSubagent` 调用（`:1311`）。
  注册真实技能命令用 `registerTUISkill(t, name)`（`pkg/tui/chat_session_test.go` 约 `:22487`）。
- 网关：`pkg/gateway/api_extra_test.go` `TestSubagentInputRefusesConversationCommands`（约 `:5250`）：`newSubagentViewTestServer(t)`、`seedSubagentRecord`、`subagentViewRequest`、`s.handleSubagentInput(w, req)`。
- 编排循环：`pkg/run/orchestration_llm_test.go` `TestRetractedInFlightSteerIsResentWithoutIt`（约 `:5537`）：`toolpkg.NewState`、`NewInputQueue()`、`NewTurnInputRuntime()`、`q.Attach(rt)`、`q.Steer(...)`、
  `wrapToolOrchestrationLLM(inner, st).Execute(WithTurnInputRuntime(ctx, rt), msgs, []*llm.Tool{readFileTestTool(t)})`，自写一个按调用次数回答的假 LLM 并记录每次收到的消息。
- fork 续跑：`pkg/run/subagent_test.go` `TestForkContinuationReplaysItsOwnPrefixByteForByte`（约 `:3830`）：`newPersistenceFixture`、`fix.seedWorker`、`fix.store.FreezeSessionPromptState`、`fix.llm.gotRequests()`。
- 代码注释：英文、讲"为什么"。

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 定向测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./<pkg> -run '^<名字>$' -count=1 -v` | 看到 `--- PASS: <名字>`（**必须看到 PASS 行**） |
| 回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/turn ./pkg/tui ./pkg/gateway -count=1` | 与基线相比无新增 FAIL |
| 编译 / vet / gofmt | `CGO_ENABLED=1 go build -tags fts5 ./...`；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/run ./pkg/tui ./pkg/gateway`；`gofmt -l pkg/run pkg/tui pkg/gateway` | exit 0 / exit 0 / 无输出 |

## Scope

**In scope**：`pkg/tui/run.go`、`pkg/tui/run_test.go`、`pkg/gateway/api_extra.go`、`pkg/gateway/api_extra_test.go`、`pkg/run/turn_input.go`、`pkg/run/skills.go`、
`pkg/run/orchestration_llm.go`、`pkg/run/orchestration_llm_test.go`、`pkg/run/subagent.go`、`pkg/run/subagent_test.go`。

**Out of scope**：`turn.Find` 本身的语义（很多地方依赖它"能找到技能命令"）；主对话的技能命令路径；`explicitSkillActivationMessage` 的文字；网页前端（网关修好后网页同一条路自动通，143 会顺带验收）；
子代理视图"只在运行中可输入"的限制（那是 142）。

## Steps

### Step 1：路由——用 `turn.IsBuiltinName` 判断内建命令（终端 + 网关）

1. 先写会红的终端测试。在 `pkg/tui/run_test.go` 的 `TestSubagentViewSlashCommandClasses` 之后新增：

```go
// A skill command typed in a subagent's view goes to that subagent with its
// skill selected. The command registry lists skills alongside the built-ins,
// so finding the name is not what tells the two apart: a real, registered
// skill once fell through to "Run this from the main view".
func TestSubagentViewSendsARegisteredSkillCommandToTheSubagent(t *testing.T) {
	registerTUISkill(t, "probe-skill")
	session := &fakeSession{streamSlashReply: map[string]SlashOutcome{
		"/probe-skill tighten it": {ShouldContinueRun: true, ContinueInput: "use probe-skill: tighten it", SkillName: "probe-skill", SkillPath: "/skills/probe-skill/SKILL.md"},
	}}
	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.NoteSubagentSpawned("task-b", "explore", ComposerFooter{})
	renderer.SetActiveView("task-b")
	state := &streamState{sessionID: "conv", session: session, composerView: "task-b"}

	subagentViewSlash(context.Background(), nil, nil, nil, nil, session, renderer, nil, state, nil, "task-b", "/probe-skill tighten it")

	if len(session.subagentSent) != 1 {
		t.Fatalf("sent to the subagent %d times, want once", len(session.subagentSent))
	}
	sent := session.subagentSent[0]
	if sent.AgentKey != "task-b" || sent.Submission.SkillName != "probe-skill" || sent.Submission.SkillPath != "/skills/probe-skill/SKILL.md" {
		t.Fatalf("sent %+v, want the skill selected for task-b", sent)
	}
	if vm := renderer.ensurePerAgentVM("task-b"); vm != nil {
		for _, b := range vm.blocks {
			if strings.Contains(b.frame.Content, subagentViewMainOnlyReply) {
				t.Fatal("the skill command was answered as a main-view command")
			}
		}
	}
}
```

（`SlashOutcome`、`subagentSendCall` 的字段名以 `run_test.go` 里的定义为准；名字不同就按定义改用例，**不要**改断言的意思。）

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '^TestSubagentViewSendsARegisteredSkillCommandToTheSubagent$' -count=1 -v` → `--- FAIL`（sent 0 次）。

2. 改 `pkg/tui/run.go` `subagentViewSlash` 开头：

```go
	fields := strings.Fields(strings.TrimSpace(line))
	name := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	// The registry lists skill commands beside the built-ins, so a name it
	// finds may still be a skill: only a built-in's own name says otherwise.
	if !turn.IsBuiltinName(name) {
		…原来 !isBuiltin 分支的内容原样搬过来…
	}
	builtin, _ := turn.Find(name)
	switch builtin.SubagentView {
	…原样…
```

（`turn.IsBuiltinName` 对已移除的内建名也返回 true，`turn.Find` 找不到时 `builtin` 是零值、`SubagentView` 为空，落进 `default` 的"回主视图"——与今天对已移除命令的回答相同。）

3. 网关同样的测试先红后绿。在 `pkg/gateway/api_extra_test.go` 的 `TestSubagentInputRefusesConversationCommands` 之后新增 `TestSubagentInputSendsARegisteredSkillCommand`：
   `turn.ResetDynamic(); t.Cleanup(turn.ResetDynamic)`，用 `turn.ReplaceDynamicSource("gw-test", []turn.DynamicCommand{{Command: turn.Command{Name: "probe-skill", Description: "probe", Category: turn.SkillCategory, AllowedSurfaces: []turn.Surface{turn.SurfaceTUI, turn.SurfaceWebChat}, Visibility: turn.VisibilityPublic}, SkillPath: "/skills/probe-skill/SKILL.md", Handler: func(turn.Context, string, []string) turn.Result { return turn.Result{Handled: true, ShouldContinueRun: true, ContinueInput: "use probe-skill: go", SkillName: "probe-skill", SkillPath: "/skills/probe-skill/SKILL.md"} }}})`
   注册技能；POST `{"message":"/probe-skill go"}`；断言 `w.Code == http.StatusOK`（今天是 400 + `subagent_view_command`）。
   然后把 `subagentCommandInput` 里的 `if _, isBuiltin := turn.Find(name); isBuiltin {` 改为 `if turn.IsBuiltinName(name) {`（注释补一句同样的理由）。

**Verify**：两个新测试 `--- PASS`；`TestSubagentViewSlashCommandClasses`、`TestSubagentInputRefusesConversationCommands` 仍 `--- PASS`。
若网关新测试在改代码后仍是 400：读 `parseSlashCommandWithOptions` 看动态命令的 `Result.SkillName` 是否透传到 `outcome`；不透传即 STOP 3。

### Step 2：插话带上技能选择

1. `pkg/run/turn_input.go` 的 `TurnInputEntry` 加两个字段：
   ```go
   	// SkillName and SkillPath carry the trusted explicit skill selection of a
   	// steer typed as a skill command in a subagent's view: delivering the
   	// steer activates that skill. Empty for every other entry.
   	SkillName string
   	SkillPath string
   ```
2. `InputQueue.Steer` 末尾的 `q.rt.enqueueEntry(TurnInputEntry{Mode: TurnInputModeSteer, Parts: parts, Seq: in.Seq})` 改为同时带上
   `SkillName: strings.TrimSpace(in.SkillName), SkillPath: strings.TrimSpace(in.SkillPath)`。
   检查 `TurnInputRuntime` 里有没有复制/重建 entry 的地方（`grep -n 'TurnInputEntry{' pkg/run/turn_input.go`）：凡是从旧 entry 构造新 entry 的，都要带上这两个字段（回滚、撤回后重排）。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/run` → exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TurnInput|Steer|Retract' -count=1` → 与基线一致。

### Step 3：把"加载一个显式技能"抽成可复用的函数

在 `pkg/run/skills.go` 里把 `preloadExplicitSkills` 循环体里"画卡 + 读文件 + 校验 + 画完成/失败卡"的那段抽成：

```go
// loadExplicitSkill runs one explicit skill's real load: the Skill card while
// the file is read, the true duration, and a failure card instead of a
// generic error. It is the one load both a turn's explicit skills and a
// skill-carrying steer go through, so the two are indistinguishable to the
// user and the model.
func loadExplicitSkill(ctx context.Context, st *tool.State, selection explicitSkillSelection, stepID string) (explicitSkillActivation, error)
```

`preloadExplicitSkills` 改成循环调用它（行为逐字不变：失败仍返回 `*ExplicitSkillLoadError` 并停在第一个失败处）。`loadedSkillByName(st, …)` 与 `tool.StepHookFromContext(ctx, st)` 用参数 `st`。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'Explicit|Skill' -count=1` → 与基线一致（这一步是纯重构）。

### Step 4：编排循环——送达带技能的插话时加载技能

1. 先写会红的测试。在 `pkg/run/orchestration_llm_test.go` 的 `TestRetractedInFlightSteerIsResentWithoutIt` 之后新增
   `TestSteerTypedAsASkillCommandActivatesTheSkill`：
   - 在 `t.TempDir()` 下写 `probe-skill/SKILL.md`，内容：
     ```
     ---
     name: probe-skill
     description: probe
     ---
     PROBE-SKILL-BODY-7f3a
     ```
   - `q.Steer(Input{Text: "tighten it", Parts: []llm.ContentPart{llm.Text("tighten it")}, SkillName: "probe-skill", SkillPath: <那个文件>})`；
   - 假 LLM：第 1 次回一个 `read_file` 工具调用（照 `orchestration_llm_test.go` 里已有的写法），第 2 次回 `done`；记录每次收到的 `[]llm.Message`；
   - 用 `tool.WithStepHook(ctx, …)`（或该文件里已有的记录 step 事件的办法）收集 step 事件；
   - 断言：第 2 次请求的最后两条消息依次是 ①`IsMeta == true`、正文含 `The user selected the `+"`probe-skill`"+` skill` 与 `PROBE-SKILL-BODY-7f3a` 的用户消息，②正文为 `tighten it` 的用户消息；
     step 事件里有一对 `ToolName == "skill"`、`SkillName == "probe-skill"` 的 started/completed，completed 无 `Error`；
     `q.TakeDelivered()` 恰好是 `tighten it` 那一条。
   - 再加一个失败分支用例 `TestSteerWhoseSkillFailsToLoadIsStillDelivered`：`SkillPath` 指向不存在的文件；断言第 2 次请求以 `tighten it` 结尾、前面**没有**激活消息，
     step 事件里那张 skill 卡的 completed 带 `Error`，`Execute` 不返回错误。

   **Verify**：两个新测试 `--- FAIL`。

2. 在 `orchestration_llm.go` 里新增：

```go
// steerMessages turns delivered steers into the messages the model reads. A
// steer typed as a skill command in a subagent's view first loads that skill,
// with its own Skill card, and the skill's instructions go in just ahead of
// the steer — the same activation a turn that starts with the command gets.
// Every message lands after the delivery's start, so the rollback and the
// steer count of an abandoned call cover the activation with its steer. A
// skill that fails to load shows its failure card and the steer is still
// delivered: the user's words reach the model either way.
//
// The activation is a persisted meta message, not an ephemeral one: the
// steer that brought it is persisted at that point of the conversation, and
// a mid-turn compaction counts the delivery's messages by position.
func (w *toolOrchestrationLLM) steerMessages(ctx context.Context, entries []TurnInputEntry) []llm.Message {
	out := make([]llm.Message, 0, len(entries))
	for i, entry := range entries {
		name, path := strings.TrimSpace(entry.SkillName), strings.TrimSpace(entry.SkillPath)
		if name != "" && path != "" {
			stepID := "skill-steer-" + strconv.Itoa(i) + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
			if activation, err := loadExplicitSkill(ctx, w.state, explicitSkillSelection{SkillName: name, SkillPath: path}, stepID); err == nil {
				msg := explicitSkillActivationMessage(activation)
				msg.Ephemeral = false
				out = append(out, msg)
			}
		}
		out = append(out, llm.UserMessage(entry.Parts...))
	}
	return out
}
```

3. 三处 `for _, entry := range delivery.Entries() { session = append(session, llm.UserMessage(entry.Parts...)) }` 都换成
   `session = append(session, w.steerMessages(ctx, delivery.Entries())...)`。**只换这三行循环**，周围的 `sessionBeforeSteers`、`inFlightSteers`、`partialCapture.Set` 一律不动、顺序不变。

**Verify**：
- 两个新测试 `--- PASS`；
- `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^(TestRetractedInFlightSteerIsResentWithoutIt|TestRetractingEveryReopeningSteerEndsTurnOnTheAnswer|TestAbandonedCallOutputNeverReachesTheSurface|TestDiscardRetractsTheInFlightSteer|TestOverflowCompactionKeepsProvisionalSteersOutOfTheCheckpoint|TestCompactionKeepsInFlightSteersOutOfTheCheckpoint|TestRetractionDuringCompactionKeepsTheCheckpoint|TestExplicitSkillActivationSurvivesFailedRetryWithoutDuplication|TestExplicitSkillActivationMessageCarriesNoToolVocabulary)$' -count=1 -v` → 全部 `--- PASS`
  （任何一个变红：**STOP 4**，不要去改压缩/回滚逻辑来迁就）。

### Step 5：fork 子代理续跑也加载技能

在 `continueForkSubagent`（`pkg/run/subagent.go`）里，`RunFork(agentCtx, RunParams{…PromptMessages: []llm.Message{llm.UserMessage(llm.Text(input))}…})` 之前：

```go
	prompt := []llm.Message{llm.UserMessage(llm.Text(input))}
	// A fork continues through RunFork, not the runner's RunContent, so the
	// explicit skills its message selected are loaded here — the same load,
	// card and activation a typed subagent's execution gets.
	if _, selected := explicitSkillSelectionsFromContext(agentCtx); selected {
		preloaded, perr := own.preloadExplicitSkills(agentCtx)
		if perr != nil {
			return "", perr
		}
		agentCtx = preloaded
		activations, _ := explicitSkillActivationsFromContext(agentCtx)
		head := make([]llm.Message, 0, len(activations)+1)
		for _, activation := range activations {
			head = append(head, explicitSkillActivationMessage(activation))
		}
		prompt = append(head, prompt...)
	}
```

并把 `PromptMessages` 改用 `prompt`。（这里沿用主代理回合开头的做法：激活消息 `Ephemeral`，不落库；失败时与主代理一样以 `*ExplicitSkillLoadError` 结束这次执行。）
`agentCtx` 的变量名以函数里实际的为准；若该函数里调 `RunFork` 用的 ctx 不是从带技能选择的 ctx 派生的，STOP 3。

新增测试 `TestForkContinuationLoadsTheSkillItsMessageSelected`（照 `TestForkContinuationReplaysItsOwnPrefixByteForByte` 搭建）：
用同样的 SKILL.md 临时文件，以 `WithExplicitSkillSelection(ctx, "probe-skill", path)` 包装 ctx 后调用 `continueForkSubagent(ctx, fix.fac, record, "go on")`
（`record` 用该范例里写进账本的那条），断言 `fix.llm.gotRequests()` 最后一次请求里，`go on` 那条用户消息之前紧挨着含 `PROBE-SKILL-BODY-7f3a` 的 `IsMeta` 消息；
并断言 worker session 落库的消息（`fix.store.ListTranscriptMessages(ctx, workerID, 0)` 或范例里查库的写法）里**没有** `PROBE-SKILL-BODY-7f3a`。

**Verify**：新测试 `--- PASS`；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'Fork' -count=1` 与基线一致。

### Step 6：变异验证（每次一个，验证后立刻还原）

| # | 临时改动 | 必须变红 |
|---|---|---|
| M1 | 终端 `subagentViewSlash` 改回 `turn.Find` 判断 | `TestSubagentViewSendsARegisteredSkillCommandToTheSubagent` |
| M2 | 网关 `subagentCommandInput` 改回 `turn.Find` 判断 | `TestSubagentInputSendsARegisteredSkillCommand` |
| M3 | `InputQueue.Steer` 不带技能字段 | `TestSteerTypedAsASkillCommandActivatesTheSkill` |
| M4 | `continueForkSubagent` 去掉 Step 5 那段 | `TestForkContinuationLoadsTheSkillItsMessageSelected` |

### Step 7：真机验收（run-forebrain 假模型驱动）

用 `subagent-steer` 模式：主代理派一个 general-purpose 子代理，子代理先跑一条 shell（答案文本就是命令），这期间在子代理视图里输入技能命令，
它作为插话在工具边界送达；子代理的下一次请求（`FAKE_DUMP_DIR` 留档）里必须带着技能正文。先读 `.claude/skills/run-forebrain/SKILL.md` 开头与 `subagent-steer` 那一行。

```bash
D=.claude/skills/run-forebrain/driver.sh
$D build
$D reset
W="${FOREBRAIN_RUN_WORK:-${TMPDIR:-/tmp}/forebrain-run}"
mkdir -p "$W/home/skills/probe-skill"
printf -- '---\nname: probe-skill\ndescription: probe skill for plan 141\n---\nPROBE-SKILL-BODY-7f3a\n' > "$W/home/skills/probe-skill/SKILL.md"
rm -rf "$TMPDIR/p141-dump"
ENABLE_SUBAGENT=1 FAKE_DUMP_DIR="$TMPDIR/p141-dump" $D start subagent-steer 'sleep 20'
$D submit 'go'
$D wait 'running the slow step' 30
$D key Down; $D key Down; $D key Enter          # 打开子代理视图（以 SKILL.md 里 subagent-steer 那行的说明为准）
$D submit '/probe-skill tighten it'
$D wait 'steer received' 40
$D screen > "$TMPDIR/p141-screen.txt"
grep -l 'PROBE-SKILL-BODY-7f3a' "$TMPDIR/p141-dump"/request-*.json
grep -c 'Run this from the main view' "$TMPDIR/p141-screen.txt"
$D stop
```

**Verify**：`grep -l` 至少列出 1 个文件（子代理带着插话的那次请求）；屏幕上子代理视图里有一张 `probe-skill` 的 Skill 卡、插话 `/probe-skill tighten it`（或其展开文字）显示为一条用户消息；
`grep -c 'Run this from the main view'` 为 `0`。驱动起不来按 SKILL.md 的 Troubleshooting；仍不行 STOP 6，**不要**跳过。

> 说明：本计划不改"空闲时能否发消息"。在 142 落地前，往**空闲**的子代理视图发技能命令会新开一次执行（typed 走 `RunContent` 的预加载，fork 走 Step 5），同样生效；142 落地后空闲时不再接受输入。

## Test plan

- 新增：`TestSubagentViewSendsARegisteredSkillCommandToTheSubagent`（tui）、`TestSubagentInputSendsARegisteredSkillCommand`（gateway）、
  `TestSteerTypedAsASkillCommandActivatesTheSkill`、`TestSteerWhoseSkillFailsToLoadIsStillDelivered`、`TestForkContinuationLoadsTheSkillItsMessageSelected`（run）。
- 回归：Step 4 列的 9 个插话/压缩/激活测试；`TestSubagentViewSlashCommandClasses`、`TestSubagentInputRefusesConversationCommands`、`TestUserSkillCommandActivatesTheSkillInTheSubagent`；四包整包。
- 变异 M1–M4；真机 Step 7。

## Done criteria

- [ ] 5 个新测试 `--- PASS`；Step 4 的 9 个回归 `--- PASS`
- [ ] M1–M4 各自变红、还原后全绿
- [ ] 四包整包与基线相比无新增 FAIL；build / vet / gofmt 通过
- [ ] `grep -n 'turn.Find(name)' pkg/tui/run.go pkg/gateway/api_extra.go` 里不再有"用 Find 判断是否内建"的写法（`builtin, _ := turn.Find(name)` 取元数据的那处可以留）
- [ ] `grep -c 'w.steerMessages(ctx, delivery.Entries())' pkg/run/orchestration_llm.go` 为 `3`
- [ ] Step 7 结果如 Verify 所述
- [ ] `git status --porcelain` 里本计划新增的改动只落在 In scope 的文件；`plans/README.md` 状态行已更新（除非评审方说由其维护）

## STOP conditions

1. 前置不满足，或"现状"里按函数名找到的代码含义已变。
2. 任一新测试在改代码前就通过。
3. 网关的动态命令结果不透传 `SkillName`；或 `continueForkSubagent` 调 `RunFork` 的 ctx 不带技能选择。
4. Step 4 的回归里任何插话/压缩/撤回测试变红。
5. 基线跑不起来。
6. Step 7 驱动反复起不来，或技能正文没有出现在子代理的请求里。
7. 任何一步需要改 In scope 以外的文件。

## Maintenance notes

- "是不是内建命令"只能用 `turn.IsBuiltinName`：`turn.Find` 会找到技能命令。以后新增"子代理视图里按命令类别分流"的地方都要用前者。
- 插话里的技能激活是**落库的** meta 消息（与插话同位置），而回合开头的激活是 ephemeral（每次请求从 ctx 重建）。两者都只出现在模型请求里、不在界面上显示；不要把激活文字放进 `TurnInputEntry.Parts`（视图会显示它）。
- 142 会把"中断并立即发送"改成在同一次执行里续跑，那条路径对 fork 用 `continueForkSubagent`、对 typed 用执行器，二者的技能加载都已由本计划覆盖。
