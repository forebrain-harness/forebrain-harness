# 计划 007：TUI——subagent 视图里的对话、队列、Esc、斜杠命令和 footer

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SUBAGENT_CONVERSATION/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"。
>
> **前置检查**：README 里计划 001、003、004、005、006 必须都是 `DONE`，否则 STOP。
>
> **漂移检查**：
> `git diff --stat bda9505 -- pkg/tui/ pkg/turn/slash.go pkg/turn/executor.go .claude/skills/run-forebrain/`
> 前置计划会改 `pkg/tui` 的许多文件，这是预期的；按函数名核对"现状"。

## 状态

- **优先级**：P1（owner 第 1–4 点的 TUI 部分）
- **工作量**：L
- **风险**：MED-HIGH（输入路由与按键优先级；主视图的行为必须一字不变）
- **依赖**：001、003、004、005、006
- **类别**：bug / direction
- **基线**：提交 `bda9505`，2026-10-04

## 为什么要做

owner 的原话："subagent视图里发送的用户消息会加入primary agent的message queue，只能回到primary agent视图，按下esc键将本应该发送给subagent的消息发送给primary agent"。原因在 TUI：composer 只有一个，提交路径完全不看当前视图——空闲时 `executeComposerSubmission` 起主回合，运行中 `handleActiveRunInput` 调 `session.SteerSurfaceRun(state.sessionID, …)` 或进主会话的队列。计划 002–006 已经在引擎里准备好了 subagent 的对话历史、压缩、输入通道和预算；本计划把 TUI 接上：在 subagent 视图里，**一切输入都属于这个 subagent**，行为与主视图一致。

## owner 已定的语义

- **D1 Esc**（subagent 视图）：先关浮层、再取消 roster 焦点（与今天相同）；然后：撤回窗口内 → 撤回发给它的消息；有发给它的待投递 steer → 中断它并立即发送；其余 → 返回主视图。Esc 永不直接取消 subagent。（计划 011 会在"中断并发送"之后插入"取消它的待自动继续"。）
- **D2**：所有类型的 subagent 都能对话，包括一次性和内部保留类型（plan-reviewer、goal-evaluator）。
- **D3**：结果不注入主会话。
- **D4** 斜杠命令三分类，逐条见下表。
- **第 4 点**：footer 右侧显示这个 subagent 自己的"N%/窗口"，格式与主视图相同（`formatComposerTokenStats`）。

### 斜杠命令在 subagent 视图里的分类（D4）

`pkg/turn/slash.go` 的全部内置命令（2026-10-04 核实）：`model fast permissions skills rename new resume fork init compact clear context memories plan agent diff status mcp lsp sandbox exit help subagents goal connect migrate`。

| 类别 | 命令 | 在 subagent 视图里的行为 |
| --- | --- | --- |
| ① 作用于该 subagent | `compact`、`context` | `/compact` 压缩它的 worker 会话（它在运行时拒绝，一句话说明）；`/context` 报告它的上下文 |
| ① 作为消息发给它 | 技能命令（动态命令） | 展开后作为发给该 subagent 的消息，带显式技能激活 |
| ② 照旧全局 | `help`、`status`、`mcp`、`lsp`、`permissions`、`sandbox`、`exit`、`diff`、`subagents`、`skills`、`connect`、`memories`、`migrate` | 与主视图完全相同 |
| ③ 隐藏 | `new`、`resume`、`fork`、`rename`、`init`、`clear`、`plan`、`agent`、`model`、`fast`、`goal`，以及 `!shell` | 不出现在斜杠菜单里；硬敲时回一句 `Run this from the main view (press esc to return).` |

owner 给出的例子是：①`/compact`、`/context`、技能；②`/help /status /mcp /lsp /permissions /sandbox /exit 等`；③`/new /resume /clear /goal /plan /model /fork /init 等` 和 `!shell`。`diff`、`subagents`、`skills`、`connect`、`memories`、`migrate` 归入②、`rename`、`agent`、`fast` 归入③，是按"是否改变主会话"推出的，owner 已于 2026-10-04 按此批准（README 决策 D9）。

## 现状（前置计划完成之后应有的样子）

- 视图：`Renderer.activeView`（主视图 `""`，subagent 为 roster key）；计划 001 之后视图只有一个入口 `assignActiveViewLocked`，roster 光标跟随视图。
- 输入：`pkg/tui/run.go` 主循环（`for {` 之后的空闲分支，约 `:625-990`）、`handleActiveRunInput`（约 `:3770`）、`handleIdleHotkey`（约 `:4267`）。召回键 `hotkeyEditLastQueued`、显式排队键 `hotkeyQueueFollowUp`（`pkg/tui/input_events.go`）。计划 004 之后主视图的队列操作都经由 `Session` 接口作用于引擎的 `run.InputQueue`。
- composer 状态：`streamState.composer`（`Text`、`DraftText`、`Cursor`、`Attachments`、`PendingPastes`、浮层）；原始输入读取器的行缓冲由 `seedInteractiveInput(text, cursor)` 重设。
- footer：`pkg/tui/reducer.go` `composerFooterSidesLocked`：subagent 视图时返回 `subagentFooterLeftLocked(view), ""`，右侧为空（注释说"它不回答第二个问题"——本计划推翻这一点，注释一起改）；`Renderer.composerTokens` 只有一份。
- 斜杠：`turn.ListSlashCommands(surface, query, opts DiscoveryOptions)`；`runDisallowedCommands`；`HandleCompactSlash`、`HandleContextSlash`（`pkg/tui/chat_slash.go`）；`/compact` 在主循环里走 `dispatchCancelableStreamSlashCommand`。
- 引擎入口（005、006、003）：`run.SendToSubagent`、`SubagentInputPreview`、`RecallSubagentInput`、`InterruptSubagentToSend`、`WithdrawSubagentInput`、`DiscardSubagentInput`、`SubagentRunning`、`SubagentCompactTarget`、`SubagentContextBudget`、`SubagentContextGauge`；事件 `subagent_input_delivered`、`pending_input_updated`（带 `agent_id`）、`token_budget_updated`（带 `agent_id`）、`subagent_spawned`（带 `origin`）。
- 主回合给工具步骤装的审计钩子：`installRunAuditStepHook(sessionID)`（`pkg/tui/chat_turn.go:224`），并在 `prepareTUIAgentBase` 里用 `tool.WithStepHook` 冻结到运行上下文上。

## 设计

### 1. `Session` 接口：subagent 对话的入口

`pkg/tui/notify.go` 的 `Session` 接口新增（`ChatSession` 实现，全部是对引擎函数的转调）：

```go
	// SendToSubagent hands what the user typed in a subagent's view to that
	// subagent: it starts its next execution when it is idle, and reaches it at
	// its next tool boundary (or after it) when it is running.
	SendToSubagent(sessionID, agentKey string, submission ComposerSubmission, followUp bool) (run.SubagentDelivery, error)
	SubagentInputPreview(sessionID, agentKey string) ComposerPendingInputPreview
	RecallSubagentInput(sessionID, agentKey string) (ComposerSubmission, bool)
	InterruptSubagentToSend(sessionID, agentKey string) bool
	WithdrawSubagentInput(sessionID, agentKey string) ([]ComposerSubmission, bool)
	CompactSubagent(ctx context.Context, sessionID, agentKey string) (string, bool)
	SubagentContextReport(sessionID, agentKey string) (string, bool)
	SubagentComposerTokenStats(sessionID, agentKey string) ComposerTokenStats
```

（`pkg/tui` 可以 import `pkg/run`：今天已经在用 `run.PrimaryModel` 等。）

- `ComposerSubmission` 进引擎时装进 `run.Input.Payload`，模型侧内容放 `Parts`，预览文本放 `Text`（与计划 004 对主视图的做法相同）；出引擎时从 `Payload` 取回，所以撤回、召回、退回都是完整的（文字、附件、@ 图片、折叠粘贴）。
- 技能命令：`ComposerSubmission.SkillName/SkillPath` 非空时，`ChatSession` 在调用 `SendToSubagent` 前把它们放进 `run.Input` 的新字段 `SkillName`、`SkillPath`（在 `pkg/run/controller.go` 的 `Input` 上加），`startUserSubagentExecution`（计划 005）据此 `run.WithExplicitSkillSelection(ctx, name, path)`。这是本计划对引擎唯一的改动，写进计划 005 的测试文件补一条 `TestUserSkillCommandActivatesTheSkillInTheSubagent`。
- `ChatSession` 构造 `run.SubagentSurface`：
  - `Frame`：给上下文装上与主回合相同的工具审计钩子。把 `installRunAuditStepHook` 里"构造钩子函数"的部分提成 `runAuditStepHook(sessionID) func(context.Context, tool.StepEvent)`，主回合照旧安装它，subagent 执行用 `tool.WithStepHook(ctx, s.runAuditStepHook(sessionID))`。审批钩子由 `syncToolApprovalHooks` 绑定在工具状态上，对 subagent 执行同样生效，无需额外处理。
  - `OnBoundary`：`s.notifyUI(SubagentInputBoundaryMsg{AgentKey, Send, Restore})`（新消息类型，`Send`/`Restore` 是 `[]ComposerSubmission`）。

### 2. composer 按视图保存

- `streamState` 新增 `composerView string`（composer 当前属于哪个视图）和 `viewDrafts map[string]composerDraft`（`composerDraft` 存 `DraftText`、`Cursor`、`Attachments`、`PendingPastes`，以及计划 004 之后召回相关的 `restoredQueuedSubmission`）。
- 唯一的同步点 `state.syncComposerToView(renderer)`：若 `renderer.ActiveView() != state.composerView`，把当前 composer 存进 `viewDrafts[state.composerView]`，取出 `viewDrafts[新视图]`（没有就空），`seedInteractiveInput` 重设读取器的行缓冲，更新 `composerView`。在主循环每一轮顶部（`renderComposerWithState` 之前）和 `handleActiveRunInput` 处理完每个事件之后调用。
- 切换会话时清空 `viewDrafts`（新会话的 subagent 是另一批）。

### 3. 提交按视图路由

在空闲分支和 `handleActiveRunInput` 的 `inputEventLine` 分支，**在任何主视图逻辑之前**：

```go
if view := renderer.ActiveView(); view != "" {
	handleSubagentViewSubmission(ctx, session, renderer, state, view, line, followUp)
	return / continue
}
```

`handleSubagentViewSubmission`：
1. 斜杠命令按上面的分类表处理（第 5 条）。
2. `!` 开头 → 回一句 `Run this from the main view (press esc to return).`，草稿保留。
3. 其余：`submissionFromCurrentDraft()` 得到提交，`session.SendToSubagent(...)`：
   - `Started`：清空 composer；用户消息由引擎的 `subagent_spawned(origin=user)` 事件画进该 subagent 视图。
   - `Steered` / `Queued`：清空 composer；预览由 `pending_input_updated(agent_id)` 刷新。
   - 错误（例如 `ErrSubagentNotFound`）：在该视图里画一条一句话的错误，草稿保留。
4. 显式排队键 `hotkeyQueueFollowUp` 在 subagent 视图里调 `SendToSubagent(..., followUp=true)`；召回键 `hotkeyEditLastQueued` 调 `RecallSubagentInput` 并把结果放回 composer（复用 `restoreRecalledSubmission`）。

主视图（`ActiveView() == ""`）的路径一行不改。

### 4. Esc

在 `handleActiveRunInput` 和 `handleIdleHotkey` 的 Esc 分支里，把今天"subagent 视图时 Esc 返回主视图"的那一段替换为：

```go
if view := renderer.ActiveView(); view != "" {
	if restored, ok := session.WithdrawSubagentInput(state.sessionID, view); ok {
		// 把 restored 合并（mergeComposerSubmissions）后放回这个视图的 composer，草稿接在后面（与 restoreWithdrawnWork 的顺序一致）
		return
	}
	if session.InterruptSubagentToSend(state.sessionID, view) {
		return
	}
	renderer.SetActiveView("")
	return
}
```

浮层和 roster 焦点的处理保持在它前面（今天的顺序）。

### 5. 斜杠命令

- 在 `pkg/turn/slash.go` 的命令定义上加字段 `SubagentView SubagentViewScope`（`SubagentViewActs`、`SubagentViewGlobal`、`SubagentViewHidden` 三个值），按上表给每个内置命令赋值；动态命令（技能）一律 `SubagentViewActs`。`DiscoveryOptions` 加 `SubagentView bool`：为 true 时 `ListSlashCommands` 排除 `SubagentViewHidden`。分类放在共享的命令表里，网页（计划 008）用同一份，不各写一张表。
- TUI 的斜杠浮层在 subagent 视图里用 `SubagentView: true` 取列表。
- 分发：
  - `compact`：`session.CompactSubagent(ctx, sid, view)`，走与主视图 `/compact` 相同的"可取消的阻塞斜杠命令"路径（`dispatchCancelableStreamSlashCommand`），压缩卡片由带 `agent_id` 的压缩事件画进该视图。subagent 正在运行时回一句 `Wait for this subagent to finish before compacting it.`。
  - `context`：`session.SubagentContextReport(sid, view)`，用与主视图 `/context` 相同的面板/文本呈现。
  - ③ 类：回一句 `Run this from the main view (press esc to return).`
  - ② 类：走今天的分发，不变。

### 6. 事件的呈现

- `subagent_input_delivered(agent_id)` → reducer 产出该 agent 的 `FrameUser`（与主视图 `renderDeliveredSteerMessages` 的样式相同），重放时同样。
- `pending_input_updated(agent_id)` → 该视图的预览；`composerPendingInputPreview` 在 subagent 视图里改为取 `session.SubagentInputPreview(sid, view)`。
- `SubagentInputBoundaryMsg`：`Send` 非空 → 合并后 `SendToSubagent`（它必然是 `Started`）；`Restore` 非空 → 合并后放进该视图的 composer（该视图在屏幕上就直接放进 composer，否则存进 `viewDrafts[agentKey]`，接在已有草稿前面——与主视图"退回的消息放在当前内容之前"的顺序一致）。

### 7. footer 右侧

- `Renderer.composerTokens` 改为按视图键保存（`map[string]ComposerTokenStats`，主视图键 `""`）；`SetComposerTokenStats`、`RefreshComposerTokenUsage`、`ComposerTokenStats` 加视图键参数，主视图的调用点传 `""`。
- `composerFooterSidesLocked`：subagent 视图时返回 `subagentFooterLeftLocked(view), formatComposerTokenStats(r.composerTokens[view])`。更新注释：subagent 视图的 footer 右侧回答的是"这个 subagent 还剩多少上下文"。
- 数据来源：打开 subagent 视图时（`syncComposerToView` 发现视图变化时）调 `session.SubagentComposerTokenStats(sid, view)` 填入；`token_budget_updated(agent_id)` 到达时更新对应键（reducer 的 `TokenBudgetUpdatedMsg` 带上 `AgentID`，`EventResult` 带上它，`run.go` 里按它写）。主视图的更新路径不变。

### 8. 假模型模式（真机验证用）

在 `.claude/skills/run-forebrain/fake_provider.py` 加模式 `subagent-net`。按**请求内容**而不是请求顺序分辨主 agent 和 subagent（LLM 客户端可能对断开的连接重试，按顺序发放会错位）：

- system 消息里含 `You are a general-purpose subagent`（`pkg/agent/subagent_defs.go` 里 general-purpose 的系统提示开头）的是 subagent 的请求：最后一条用户消息是 `continue` 时按 `reply` 回答（文本为命令行给的答案），否则**直接断开连接**、不回任何字节，模拟网络中断（重试同样会被断开，直到它放弃）。
- 其余是主 agent 的请求：第一个以 `subagent_run`（`{"title":"network probe","task":"answer briefly","subagent_type":"general-purpose"}`）回答，之后的一律回答固定文本 `primary noted the failure`。

`driver.sh` 的用法和 `SKILL.md` 的模式表各加一行。

## 缓存影响

本计划只改 TUI 的路由和呈现；请求内容由计划 002、005 决定，已有金样测试。没有新的前缀变化。

## 范围

**只改这些文件：** `pkg/tui/*.go`（含测试）、`pkg/turn/slash.go` 及其测试（分类字段与过滤）、`pkg/run/controller.go`（`Input.SkillName/SkillPath`）、`pkg/run/subagent.go`（显式技能激活一行）及其测试、`.claude/skills/run-forebrain/fake_provider.py`、`.claude/skills/run-forebrain/driver.sh`、`.claude/skills/run-forebrain/SKILL.md`。

**不要动：** 主视图的任何可观察行为；网页（008）；自动继续（011）。

## 步骤

### 第 1 步：先写失败的测试（复现 owner 的问题）

`pkg/tui/run_test.go` 加 `TestMessageTypedInASubagentViewGoesToThatSubagent`：用测试用的假 `Session`（照 `TestAgentRosterXUsesSelectedRowCancel` 的假 session 写法，记录调用），渲染器的视图设为 `task-b`，主运行正在进行；提交一行 `continue`。断言：`SendToSubagent` 收到 `("task-b", "continue")`；`SteerSurfaceRun` 和主会话的排队方法**没有**被调用。空闲时（无主运行）同样断言没有起主回合。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestMessageTypedInASubagentViewGoesToThatSubagent -count=1` → 失败（编译失败也算，因为接口方法还不存在）。

### 第 2 步：`Session` 接口与 `ChatSession` 实现

按"设计"第 1 条。

**验证**：`go build -tags fts5 ./...` 成功；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/run -count=1` → `ok`（第 1 步仍失败，因为路由还没改）。

### 第 3 步：composer 按视图保存、提交按视图路由

按"设计"第 2、3 条。

**验证**：第 1 步通过；新增 `TestDraftsBelongToTheirViews`（主视图打一半 `abc`，进 subagent 视图打 `xyz`，回主视图看到 `abc`，再进去看到 `xyz`，附件随草稿走）；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`。

### 第 4 步：Esc

按"设计"第 4 条。新增：
- `TestEscInASubagentViewWithdrawsTheMessageItJustStarted`
- `TestEscInASubagentViewWithPendingSteersInterruptsAndSends`
- `TestEscInASubagentViewWithNothingInFlightReturnsToTheMainView`
- `TestEscInTheMainViewIsUnchanged`（主视图的撤回 / 中断并发送 / 取消运行三种情况，断言与今天相同）

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Esc' -count=1` → `ok`。

### 第 5 步：斜杠命令

按"设计"第 5 条。`pkg/turn` 加 `TestEveryBuiltinCommandHasASubagentViewScope`（遍历 `All()`，每个内置命令都显式设置了分类，且与上表一致）和 `TestSubagentViewHidesConversationCommands`。`pkg/tui` 加 `/compact`、`/context`、③类、`!shell` 在 subagent 视图里的测试。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/tui -count=1` → `ok`。

### 第 6 步：事件呈现与 footer

按"设计"第 6、7 条。新增 `TestSubagentFooterShowsItsOwnContextBudget`（主视图 `75%/1M`、subagent 视图 `40%/128k` 互不影响；切换视图时各自正确）、`TestPlanReviewerFooterShowsItsOwnContextBudget`（plan-reviewer 视图与其它 subagent 视图同样有右侧的 `N%/窗口`，窗口是用户选的评审模型的，不是主模型的；owner 2026-10-05 的要求见 README）、`TestDeliveredSteerIsDrawnInTheSubagentsView`、`TestRestoredInputGoesBackToItsOwnViewsComposer`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`。

### 第 7 步：真机（tmux，假模型）

按"设计"第 8 条加 `subagent-net` 模式，然后：

```bash
D=.claude/skills/run-forebrain/driver.sh
$D reset; $D start subagent-net 'subagent finished after continue'
$D submit 'delegate the probe'
$D wait 'network probe' 30
# 主 agent 会在 subagent 失败后结束它的回合；进入 subagent 视图：
$D key Down; $D key Down; $D key Enter     # 或用计划 001 加的 click 点 subagent 卡片
$D screen                                   # footer 左：viewing general-purpose …；右：N%/窗口
$D submit 'continue'
$D wait 'subagent finished after continue' 30
$D screen
$D db "SELECT session_id, role, substr(content,1,40) FROM fb_messages ORDER BY id;"
```

**验证**：
- 进入视图后 footer 右侧显示 `N%/<窗口>`（与主视图同一格式）。
- plan-reviewer 视图同样如此：用智谱真机（隔离环境，两条模型）在计划模式里请另一个模型评审，进入 reviewer 视图，footer 左侧是评审模型、右侧是它自己的 `N%/<评审模型的窗口>`，评审进行中数字会变。
- 发出 `continue` 后，它出现在 subagent 视图里，回答 `subagent finished after continue` 也在 subagent 视图里；主视图里没有这两条。
- 数据库里这两条在 worker 会话（`main:<对话id>:…`）下，主会话没有新增行。
- 在 subagent 运行中再发一条、按 Esc：它作为下一次执行立即发出（D1）。
- 在 subagent 视图里输入 `/compact`：压缩卡片出现在该视图；输入 `/new`：只回一句提示，会话不变。

把关键的屏幕输出和 SQL 结果贴进报告。

## 完成标准

- [ ] 第 1–6 步测试全部通过；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全部 `ok`
- [ ] 主视图的已有测试断言未改
- [ ] 死代码检查与基线一致（计划 005、006 留下的未使用导出函数在此清零）
- [ ] 架构测试通过（`pkg/tui` 没有新增内部包 import；`run` 已在其 import 列表中）
- [ ] 第 7 步的真机证据已附在报告里
- [ ] README 状态行已更新

## STOP 条件

- 主视图的任何已有测试需要改断言才能通过。
- `syncComposerToView` 无法在不改 `pkg/tui/input_events.go` 读取器内部状态的情况下正确恢复草稿（`seedInteractiveInput` 不够用）。
- 需要给 `pkg/tui` 新增生产文件。

## 维护说明

- subagent 视图里新增的任何输入能力，先问"主视图有没有同样的能力"：有就复用同一段代码，只把目标换成 subagent；没有就回来问 owner。
- 斜杠命令的分类在 `pkg/turn/slash.go` 的命令表里；新增内置命令时必须设置它，`TestEveryBuiltinCommandHasASubagentViewScope` 会抓住遗漏。
