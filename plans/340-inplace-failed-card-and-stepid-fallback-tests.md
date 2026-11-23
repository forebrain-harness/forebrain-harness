# Plan 340：给本分支新增的「就地审批失败卡」补持久化/子代理路由测试，给 `SubagentApprovalRequest` 补 stepID 回退测试

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
> **本计划只加测试，不改任何生产代码。**
>
> **工作目录**：`/Users/doudou/workspace/unionj-cloud/forebrain-harness/.worktrees/fix-user-interaction-subagent-ask`
> （分支 `fix-user-interaction-subagent-ask` 的 worktree，HEAD `4de94476`，写作时工作区干净）。下文所有相对路径都相对这个目录。
> **共用 worktree 提示**：339/340/341/342 可能并行在此 worktree 执行，兄弟计划留下的未提交改动属预期、不触发 STOP；
> 本计划的 drift check 与范围判据只按路径核对自己的 in-scope 文件。
>
> **Drift check（先跑）**：
> ```bash
> cd /Users/doudou/workspace/unionj-cloud/forebrain-harness/.worktrees/fix-user-interaction-subagent-ask
> git diff --stat 4de94476 -- pkg/tui/chat_surface.go pkg/tui/chat_surface_test.go pkg/turn/subagent_approval.go pkg/turn/subagent_approval_test.go   # 预期：无输出
> ```
> 有输出 → STOP。然后对照下方 "Current state" 摘录逐段核对；不一致即 STOP。
>
> **go 不在 PATH**：所有 go 命令用绝对路径 `/usr/local/go/bin/go`（go1.26.9）。cgo + fts5 标签必须带。

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: LOW（纯新增测试；若测试暴露生产缺陷，停下报告，不在本计划里改生产代码）
- **Depends on**: none（与 339/341/342 零文件交集，可并行）
- **Category**: tests
- **Planned at**: commit `4de94476`, 2026-11-20
- **Finding tag**: introduced（本分支新增的生产行为缺测试覆盖）

## Why this matters

本分支给「就地审批」加了两条新行为，但现有测试只覆盖了其中一半：

1. **失败卡的持久化半边没测**。`answerInPlaceApproval` 在审批 prompt 自身失败（overlay 构造错误，不是用户的回答）时
   调 `notifyInPlaceFailedCard`（`pkg/tui/chat_surface.go:1613-1646`），它既画 live 失败卡、**又把一条 failed 的
   `tool_completed` 规范事件落库**。现有 `TestInPlaceApprovalPromptFailureShowsFailedCard`（`chat_surface_test.go:1928-1947`）
   只断言了 **live 卡**。落库那半边没测——而它正是 replay 不再撒谎的关键：`replayOrphanToolCallMessage`
   （`pkg/tui/commands.go:1275-1302`）**只对「没有任何结果行」的调用**画 "canceled" 卡；只要 failed 完成行落了库，
   replay 就走结果行画 failed，而不是把这条用户毫无过错的调用扫进 "Canceled (no output)"。没有测试钉住「failed 行确实落库」，
   将来谁把 `persistRunEvent` 那步删了/改了，live 测试照样绿，replay 却悄悄回归成撒谎的 canceled。

2. **失败卡的子代理路由没测**。`notifyInPlaceFailedCard` 用 `stepCtx := tool.WithHookAgentID(ctx, gate.AgentID)`
   让卡落在**子代理自己的视图**（空 AgentID 才是主代理）。现有失败卡测试走的是主代理路径（`resolveInlineApproval`，rae 无 AgentID），
   没有一条测试验证子代理路径（`resolveSubagentApproval` + `rae.AgentID`）下失败卡带上了正确的 AgentID。

3. **`SubagentApprovalRequest` 的 stepID 回退没测**。本分支给 `pkg/turn/subagent_approval.go:92-113` 加了
   `stepID := gate.OriginatingToolCallID; if "" { stepID = PendingApprovalToolStepID(gate.Session, gate.ToolName) }`
   以及 `req.AskFormJSON = gate.AskFormJSON`。现有 `pkg/turn/subagent_approval_test.go` 的
   `TestSubagentApprovalRequestNamesTheWorker`（`:34-46`）只断言 SessionID/ActionID/Description，**没碰 ToolStepID 与 AskFormJSON**。
   回退分支（thrower 没能命名调用时靠 snapshot 推导）是 ask 卡能锚到正确调用的兜底，没测。

补这三条测试，把本分支的新契约钉死。

## Current state

### 被测生产代码（不改，只供理解）

`pkg/tui/chat_surface.go:1613-1646` `notifyInPlaceFailedCard`（生产注释省略，其余逐字）：

```go
func (s *ChatSession) notifyInPlaceFailedCard(ctx context.Context, gate turn.SubagentApprovalGate, req turn.ToolApprovalRequest, promptErr error) {
	if s == nil || promptErr == nil {
		return
	}
	stepID := strings.TrimSpace(req.ToolStepID)
	toolName := strings.TrimSpace(req.ToolName)
	if stepID == "" || toolName == "" {
		return
	}
	input := map[string]any{}
	if raw := strings.TrimSpace(req.ToolInputJSON); raw != "" {
		if err := json.Unmarshal([]byte(raw), &input); err != nil {
			input = map[string]any{"raw": raw}
		}
	}
	failed := tool.StepEvent{
		Kind:     tool.StepKindToolCompleted,
		StepID:   stepID,
		ToolName: toolName,
		Input:    input,
		Error:    promptErr.Error(),
	}
	stepCtx := tool.WithHookAgentID(ctx, strings.TrimSpace(gate.AgentID))
	for _, canonical := range turn.CanonicalEventsFromStep(stepCtx, req.SessionID, gate.RunID, "tui", failed) {
		if !event.IsPushOnlyStreamEvent(canonical.Type) {
			s.persistRunEvent(stepCtx, canonical)
		}
	}
	s.notifyToolStepHooks(stepCtx, req.SessionID, gate.RunID, "tui", failed)
}
```

- `persistRunEvent`（`pkg/tui/notify.go:1327-1335`）→ `runSvc().AppendSessionEventOnce(ctx, state.SessionEvent{...})`。
  `publishRunEvent`（`notify.go:901+`）走**同一个** `AppendSessionEventOnce`。所以落库的事件能用
  `runs.ListSessionEventsOfType`（`pkg/state/session_events.go:376`）读回——现有
  `TestInlineApprovalApprovesWithoutASecondRun` 就是这么读 `approval_requested` 的。
- live 卡经 `notifyToolStepHooks`（`notify.go:598+`）→ `notifyUIForSession(sessionID, NewMessageMsg{Msg: Message{... AgentID: strings.TrimSpace(meta.AgentID) ...}})`，
  其中 `meta = tool.EnrichToolMeta(ctx, tool.BuildToolMeta(evt))`，`EnrichToolMeta`（`pkg/tool/format.go:326-352`）
  把 `meta.AgentID = HookAgentIDFromContext(ctx)`。所以 `stepCtx` 带了 `gate.AgentID` 时，live 卡的 `Msg.AgentID` 就是它。
  `PrependUINotify`（`chat_session.go:1025`）能捕获这些 `NewMessageMsg`（现有失败卡测试已证）。
- 落库的 `tool_completed` 载荷是 `event.ToolCallCompletedPayload`（`pkg/tool/notification.go:133-140`），关键字段：
  `StepID`、`ToolName`、`Error`（= `promptErr.Error()`）、`ToolMeta`（`event.ToolCallMeta`，含 `Status`、`AgentID`）。
  failed 步的 `ToolMeta.Status == "failed"`（`BuildToolMeta` 经 `stepStatusLabel`，现有 live 测试已断言 failed）。
- **子代理路径的 SessionID 来自派发台账（Test B 的关键前置）**：`resolveSubagentApproval`（`chat_surface.go:1393-1414`）用
  `req.SessionID = s.dispatchingSessionIDForRun(gate.RunID)`（`chat_surface.go:1690-1700`）→ `agent.GetMerged(stateRoot, Query{RunID})`
  读**持久化派发台账**；查不到返回 ""，而 `persistRunEvent` 对空 SessionID **直接跳过落库**。`inlineGateSession` 建的 run 没有台账记录，
  所以 Test B 必须先 `agent.AppendHistory(cs.stateRoot(), agent.HistoryEntry{RunID: runID, SessionID: "s1", Status: agent.StatusRunning})`
  种一条（先例：`chat_session_test.go:373-378` 的 restart 测试；`agent` 包已在 `chat_surface_test.go` import）。
  评审已用 `-overlay` 实证：不 seeding 时 Test B 落库腿必红（`found==nil`，live 腿仍绿），seeding 后全绿。

`pkg/turn/subagent_approval.go:92-113` `SubagentApprovalRequest`（被测，不改；生产注释省略，其余逐字）：

```go
func SubagentApprovalRequest(gate SubagentApprovalGate, sessionID string, eval ApprovalEvaluator) ToolApprovalRequest {
	stepID := strings.TrimSpace(gate.OriginatingToolCallID)
	if stepID == "" {
		stepID = PendingApprovalToolStepID(gate.Session, gate.ToolName)
	}
	req, _, _ := BuildToolApprovalRequest(ApprovalSource{
		SessionID:    strings.TrimSpace(sessionID),
		ActionID:     gate.ActionID,
		RunID:        gate.RunID,
		ToolName:     gate.ToolName,
		ActionKind:   gate.ActionKind,
		ToolInput:    gate.ToolInput,
		AgentID:      gate.AgentID,
		SubagentType: gate.SubagentType,
		ToolStepID:   stepID,
	}, eval)
	req.Description = SubagentApprovalDescription(gate)
	req.AskFormJSON = strings.TrimSpace(gate.AskFormJSON)
	return req
}
```

### 测试范例（照抄结构）

`pkg/tui/chat_surface_test.go:1528-1579` 的 helper（**已存在，直接复用，不要重写**）：

```go
func inlineGateSession(t *testing.T, kind string) (*ChatSession, *state.RunStore, *state.Action, string, *tool.RequiresActionError) {
	// ... 建 home/db、session "s1"、一个 run、snapshot（assistant 调用 id = "call-inline"）、
	//     kind=="user_interaction" 时 actions.CreateAsk(...) 否则 CreatePending(...)、
	//     runs.SetWaitingAction(... SessionSnapshot: snapshot)、cs := sessionEnv{...}.session()
	// 返回 cs, runs, act, runRow.ID, rae（rae.RunID/ActionID/ActionKind/ToolName/ToolInput/SessionSnapshot 已填，AgentID 空）
}

func inlineGateCtx(runID string) context.Context {
	return llm.WithAgentSessionID(tool.WithRunID(context.Background(), runID), "s1")
}
```

snapshot 里的调用（`chat_surface_test.go:1546-1549`）：

```go
	snapshot := []llm.Message{
		llm.UserMessage(llm.Text("run it")),
		llm.AssistantMessage(nil, llm.ToolCall{ID: "call-inline", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: kind, Arguments: "{}"}}),
	}
```

`failingApprovalSink`（`chat_surface_test.go:1919-1923`，**已存在，复用**）：

```go
type failingApprovalSink struct{ err error }

func (s *failingApprovalSink) PromptToolApproval(context.Context, turn.ToolApprovalRequest) (turn.ToolApprovalDecision, error) {
	return turn.ToolApprovalDecision{}, s.err
}
```

live 失败卡范例 `TestInPlaceApprovalPromptFailureShowsFailedCard`（`chat_surface_test.go:1928-1947`）：

```go
func TestInPlaceApprovalPromptFailureShowsFailedCard(t *testing.T) {
	cs, _, _, runID, rae := inlineGateSession(t, "user_interaction")
	cs.SetToolApprovalSink(&failingApprovalSink{err: errors.New("overlay boom")})
	failed := make(chan Message, 4)
	cs.PrependUINotify(func(msg any) {
		if m, ok := msg.(NewMessageMsg); ok && m.Msg.Kind == MsgKindTool &&
			m.Msg.StepID == "call-inline" && strings.EqualFold(m.Msg.ToolMeta.Status, "failed") {
			failed <- m.Msg
		}
	})
	_, _ = cs.resolveInlineApproval(inlineGateCtx(runID), rae)
	select {
	case m := <-failed:
		if !strings.Contains(m.Content, "overlay boom") {
			t.Fatalf("failed card body = %q, want the prompt error", m.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("failed prompt left parked ask pending (swept Canceled); want failed card")
	}
}
```

子代理路径范例 `TestSubagentAskGateCarriesTheForm`（`chat_surface_test.go:1895-1917`）——注意它怎么设 `rae.AgentID` 并走 `resolveSubagentApproval`：

```go
	cs, _, act, runID, rae := inlineGateSession(t, "user_interaction")
	rae.AgentID = "worker-1"
	rae.SubagentType = "explore"
	rae.AskFormJSON = act.PayloadJSON
	sink := &scriptedApprovalSink{decisions: []turn.ToolApprovalDecision{{Approved: true}}}
	cs.SetToolApprovalSink(sink)
	if _, err := cs.resolveSubagentApproval(inlineGateCtx(runID), rae); err != nil { ... }
```

持久化读回范例 `TestInlineApprovalApprovesWithoutASecondRun`（`chat_surface_test.go:1602-1615`）：

```go
	requested, err := runs.ListSessionEventsOfType(context.Background(), "s1", event.RunEventApprovalReq, 10)
	// ... len 检查 ...
	var reqPayload event.ApprovalRequestedPayload
	if err := json.Unmarshal(requested[0].Payload, &reqPayload); err != nil { ... }
	if reqPayload.ToolStepID != "call-inline" { ... }
```

turn 测试范例 `TestSubagentApprovalRequestNamesTheWorker`（`pkg/turn/subagent_approval_test.go:34-46`）：

```go
func TestSubagentApprovalRequestNamesTheWorker(t *testing.T) {
	gate := SubagentApprovalGate{
		ActionID: "act-1", RunID: "child-1", ToolName: "shell", ActionKind: "shell",
		SubagentType: "plan", ToolInput: map[string]any{"command": "git grep -l todo"},
	}
	req := SubagentApprovalRequest(gate, "session-1", nil)
	require.Equal(t, "session-1", req.SessionID)
	require.Equal(t, "act-1", req.ActionID)
	require.Equal(t, "Requested by plan.", req.Description)
	// ...
}
```

### 约定

- 两个测试文件都已 import 所需包（`pkg/tui/chat_surface_test.go`：context/errors/strings/time/testing/encoding/json、
  llm/state/tool/event/turn/**agent**、testify/require；`pkg/turn/subagent_approval_test.go`：context/errors/path/filepath/testing、
  llm/state/tool、testify/require）。**本计划不需要新增任何 import**（Test B 的 seeding 用的 `agent` 包已在 import 块里）。
- 注释写"为什么"、英文完整句子，照范例上方那种说明性 doc 注释。
- 测试命名照仓库风格：`Test<行为><期望>`，长名可接受（见范例）。

## Commands you will need

| 用途 | 命令（在 worktree 根目录） | 成功预期 |
|---|---|---|
| 单条 tui 测试 | `CGO_ENABLED=1 /usr/local/go/bin/go test -tags fts5 ./pkg/tui/ -count=1 -run '<Name>' -v` | `--- PASS: <Name>`（无 `no tests to run`） |
| 单条 turn 测试 | `CGO_ENABLED=1 /usr/local/go/bin/go test -tags fts5 ./pkg/turn/ -count=1 -run '<Name>' -v` | `--- PASS: <Name>`（无 `no tests to run`） |
| tui 整包 | `CGO_ENABLED=1 /usr/local/go/bin/go test -tags fts5 ./pkg/tui/... -count=1` | `ok` |
| turn 整包 | `CGO_ENABLED=1 /usr/local/go/bin/go test -tags fts5 ./pkg/turn/... -count=1` | `ok` |
| vet | `CGO_ENABLED=1 /usr/local/go/bin/go vet -tags fts5 ./pkg/tui/... ./pkg/turn/...` | 无输出 |
| gofmt | `gofmt -l pkg/tui/chat_surface_test.go pkg/turn/subagent_approval_test.go` | 无输出 |

（pkg/tui 整包较慢，约 200s；耐心等真实退出码，不要用管道 tail 掩码退出码。）

## Scope

**In scope**：
- `pkg/tui/chat_surface_test.go`（新增 Test A、Test B 两个函数）
- `pkg/turn/subagent_approval_test.go`（新增 Test C 一个函数）

**Out of scope**：
- 任何生产代码：`pkg/tui/chat_surface.go`、`pkg/tui/notify.go`、`pkg/turn/subagent_approval.go`、`pkg/tool/**`、`pkg/state/**`——
  本计划只加测试。若测试暴露生产缺陷，STOP 报告，不要顺手改。
- 现有测试函数与 helper（`inlineGateSession`/`inlineGateCtx`/`failingApprovalSink`/`scriptedApprovalSink`）——复用，不改。
- 完整的 replay-render 测试（把落库事件喂进 reducer 画卡）——本计划用「failed 行确实落库」作机器可检的代理判据
  （理由见 Why：`replayOrphanToolCallMessage` 只对无结果行的调用画 canceled）。render 级 replay 测试延期，见 Maintenance。
- plan 339/341/342 的文件。

## Git workflow

不建分支、不 commit、不 push、不 stash（owner 手动提交）。改动留在 worktree 工作区。

## Steps

### Step 1：Test A — 失败卡持久化（主代理路径）

在 `pkg/tui/chat_surface_test.go` 的 `TestInPlaceApprovalPromptFailureShowsFailedCard`（`:1947` 结束）之后新增：

```go
// The failed card is not only painted live: its failed completion is filed on
// the run, because that row is what a resumed conversation replays. Without it
// the call has no result row and replayOrphanToolCallMessage sweeps it into a
// lying "Canceled (no output)" — the error the user caused nothing of.
func TestInPlaceApprovalPromptFailurePersistsFailedCompletion(t *testing.T) {
	cs, runs, _, runID, rae := inlineGateSession(t, "user_interaction")
	cs.SetToolApprovalSink(&failingApprovalSink{err: errors.New("overlay boom")})
	_, _ = cs.resolveInlineApproval(inlineGateCtx(runID), rae)

	completed, err := runs.ListSessionEventsOfType(context.Background(), "s1", event.RunEventToolCompleted, 10)
	require.NoError(t, err)
	var found *event.ToolCallCompletedPayload
	for i := range completed {
		var p event.ToolCallCompletedPayload
		require.NoError(t, json.Unmarshal(completed[i].Payload, &p))
		if p.StepID == "call-inline" && strings.EqualFold(p.ToolMeta.Status, "failed") && strings.Contains(p.Error, "overlay boom") {
			found = &p
			break
		}
	}
	require.NotNil(t, found, "no failed tool_completed row for call-inline; replay would draw Canceled")
}
```

要点：遍历所有 `tool_completed`（可能不止一条），找 `StepID=="call-inline"` 且 `ToolMeta.Status=="failed"` 且 `Error` 含 "overlay boom" 的那条；
**不要**断言总数==1（gate 自身也可能有一条完成事件）。

**Verify**：`CGO_ENABLED=1 /usr/local/go/bin/go test -tags fts5 ./pkg/tui/ -count=1 -run TestInPlaceApprovalPromptFailurePersistsFailedCompletion -v` → 输出含 `--- PASS: TestInPlaceApprovalPromptFailurePersistsFailedCompletion`（且无 `no tests to run`）。

### Step 2：Test B — 失败卡路由到子代理视图

紧跟 Test A 之后新增：

```go
// A subagent's gate fails its in-place prompt: the failed card must land in the
// child's own view (AgentID on the live message and on the filed completion),
// not the primary agent's. An empty AgentID is the primary's gate; this one is
// the child's, so the routing fact is the child id.
func TestInPlaceApprovalPromptFailureRoutesFailedCardToChild(t *testing.T) {
	cs, runs, act, runID, rae := inlineGateSession(t, "user_interaction")
	rae.AgentID = "worker-1"
	rae.SubagentType = "explore"
	rae.AskFormJSON = act.PayloadJSON
	// The subagent path files the completion on the dispatching conversation,
	// recovered from the durable dispatch ledger (startSubagent writes it at
	// spawn). Seed that record so the gate's run resolves to "s1"; without it
	// req.SessionID is empty and persistRunEvent silently skips the row.
	if err := agent.AppendHistory(cs.stateRoot(), agent.HistoryEntry{
		RunID: runID, SessionID: "s1", Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("AppendHistory error: %v", err)
	}
	cs.SetToolApprovalSink(&failingApprovalSink{err: errors.New("overlay boom")})
	failed := make(chan Message, 4)
	cs.PrependUINotify(func(msg any) {
		if m, ok := msg.(NewMessageMsg); ok && m.Msg.Kind == MsgKindTool &&
			m.Msg.StepID == "call-inline" && strings.EqualFold(m.Msg.ToolMeta.Status, "failed") {
			failed <- m.Msg
		}
	})
	_, _ = cs.resolveSubagentApproval(inlineGateCtx(runID), rae)
	select {
	case m := <-failed:
		require.Equal(t, "worker-1", strings.TrimSpace(m.AgentID), "live failed card must route to the child view")
		require.Contains(t, m.Content, "overlay boom")
	case <-time.After(2 * time.Second):
		t.Fatalf("subagent failed prompt drew no failed card")
	}

	completed, err := runs.ListSessionEventsOfType(context.Background(), "s1", event.RunEventToolCompleted, 10)
	require.NoError(t, err)
	var found *event.ToolCallCompletedPayload
	for i := range completed {
		var p event.ToolCallCompletedPayload
		require.NoError(t, json.Unmarshal(completed[i].Payload, &p))
		if p.StepID == "call-inline" && strings.EqualFold(p.ToolMeta.Status, "failed") {
			found = &p
			break
		}
	}
	require.NotNil(t, found, "no failed completion filed for the child's call")
	require.Equal(t, "worker-1", strings.TrimSpace(found.ToolMeta.AgentID), "filed completion must carry the child agent id")
}
```

**Verify**：`CGO_ENABLED=1 /usr/local/go/bin/go test -tags fts5 ./pkg/tui/ -count=1 -run TestInPlaceApprovalPromptFailureRoutesFailedCardToChild -v` → 输出含 `--- PASS: TestInPlaceApprovalPromptFailureRoutesFailedCardToChild`（且无 `no tests to run`）。

### Step 3：Test C — `SubagentApprovalRequest` 的 stepID 回退与 form 传播

在 `pkg/turn/subagent_approval_test.go` 的 `TestSubagentApprovalRequestNamesTheWorker`（`:46` 结束）之后新增：

```go
// The request anchors to the call the gate names; when the thrower could not
// name it, the snapshot's pending call of the gated tool is the same identity.
// The ask form rides along so an in-place surface can render the question.
func TestSubagentApprovalRequestFallsBackToSnapshotCall(t *testing.T) {
	snapshot := []llm.Message{
		llm.UserMessage(llm.Text("ask me")),
		llm.AssistantMessage(nil, llm.ToolCall{ID: "call-ask-9", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "user_interaction", Arguments: "{}"}}),
	}
	form := `{"kind":"user_interaction","questions":[]}`

	// No originating id: fall back to the snapshot's pending user_interaction call.
	fallback := SubagentApprovalRequest(SubagentApprovalGate{
		ActionID: "act-1", RunID: "child-1", ToolName: "user_interaction",
		Session: snapshot, AskFormJSON: form,
	}, "session-1", nil)
	require.Equal(t, "call-ask-9", fallback.ToolStepID)
	require.Equal(t, form, fallback.AskFormJSON)

	// An originating id wins over the snapshot.
	named := SubagentApprovalRequest(SubagentApprovalGate{
		ActionID: "act-1", RunID: "child-1", ToolName: "user_interaction",
		OriginatingToolCallID: "call-orig", Session: snapshot, AskFormJSON: form,
	}, "session-1", nil)
	require.Equal(t, "call-orig", named.ToolStepID)
}
```

**Verify**：`CGO_ENABLED=1 /usr/local/go/bin/go test -tags fts5 ./pkg/turn/ -count=1 -run TestSubagentApprovalRequestFallsBackToSnapshotCall -v` → 输出含 `--- PASS: TestSubagentApprovalRequestFallsBackToSnapshotCall`（且无 `no tests to run`）。

### Step 4：反证 + 全量

**反证（确认测试真的钉住本分支行为）**：本计划不改生产代码，故反证从代码逻辑确立，而非实际回退生产文件。
Test C 的回退腿断言 `call-ask-9`——`stepID` 仅来自 `gate.OriginatingToolCallID` 或 `PendingApprovalToolStepID` 回退；
去掉回退分支则 stepID 为空，测试必红。Test A/B 同理：`notifyInPlaceFailedCard` 的 `persistRunEvent` 循环是唯一落库点，
去掉它则 Test A 的 `found` 为 nil、Test B 的落库断言为 nil。在报告里逐条说明这层依赖。
（若评审方要求经验性反证，可用仓库惯用的 `go test -overlay` 注入临时修改的生产文件副本复现红态，仓库树零改动——非执行者必做项。）

全量门禁：

```bash
cd /Users/doudou/workspace/unionj-cloud/forebrain-harness/.worktrees/fix-user-interaction-subagent-ask
CGO_ENABLED=1 /usr/local/go/bin/go test -tags fts5 ./pkg/tui/... ./pkg/turn/... -count=1
CGO_ENABLED=1 /usr/local/go/bin/go vet -tags fts5 ./pkg/tui/... ./pkg/turn/...
gofmt -l pkg/tui/chat_surface_test.go pkg/turn/subagent_approval_test.go
```

**Verify**：两包 `ok`（取真实退出码，别用管道 tail）；vet、gofmt 无输出。

## Test plan

- `pkg/tui/chat_surface_test.go`：
  - `TestInPlaceApprovalPromptFailurePersistsFailedCompletion`（Test A）：主代理就地审批 prompt 失败 → 落库一条 failed `tool_completed`（StepID=call-inline、Error 含 overlay boom）。
  - `TestInPlaceApprovalPromptFailureRoutesFailedCardToChild`（Test B）：先 `agent.AppendHistory` 种派发记录（RunID=gate 的 run、SessionID "s1"），子代理路径失败 → live 卡 `AgentID==worker-1` 且落库完成 `ToolMeta.AgentID==worker-1`。
- `pkg/turn/subagent_approval_test.go`：
  - `TestSubagentApprovalRequestFallsBackToSnapshotCall`（Test C）：无 OriginatingToolCallID 时回退到 snapshot 的 pending 调用 id；有则优先；AskFormJSON 传播。
- 结构范例：Test A/B 照 `TestInPlaceApprovalPromptFailureShowsFailedCard` + `TestSubagentAskGateCarriesTheForm` + `TestInlineApprovalApprovesWithoutASecondRun` 的持久化读回；Test C 照 `TestSubagentApprovalRequestNamesTheWorker`。
- 验证：三条点名 PASS + tui/turn 整包 `ok` + vet/gofmt 净。

## Done criteria

- [ ] 三条新测试点名 `-run` 各见 `--- PASS: <Name>`（无 `no tests to run`）
- [ ] `CGO_ENABLED=1 /usr/local/go/bin/go test -tags fts5 ./pkg/tui/... ./pkg/turn/... -count=1` 两包 `ok`（真实退出码 0）
- [ ] `CGO_ENABLED=1 /usr/local/go/bin/go vet -tags fts5 ./pkg/tui/... ./pkg/turn/...` 无输出
- [ ] `gofmt -l pkg/tui/chat_surface_test.go pkg/turn/subagent_approval_test.go` 无输出
- [ ] 范围合规：`git diff --name-only 4de94476` 里本计划只改 `pkg/tui/chat_surface_test.go`、`pkg/turn/subagent_approval_test.go`（纯新增测试函数，无生产代码改动）；若出现其它文件，只能是兄弟计划的 in-scope 文件（339：`.claude/skills/run-forebrain/fake_provider.py`；341：`.claude/skills/run-forebrain/SKILL.md`、`driver.sh`；342：`pkg/gateway/approval.go`、`approval_test.go`），否则 STOP
- [ ] 报告里说明三条测试各自钉住的生产行为（持久化 / 子代理路由 / stepID 回退）

## STOP conditions

1. Drift check 有输出（四个 in-scope 文件相对 `4de94476` 已改动）。
2. "Current state" 摘录与活体代码不符（helper 签名、`notifyInPlaceFailedCard` 结构、`SubagentApprovalRequest` 回退逻辑、payload 字段名）——停下报告实际代码。
3. Test A 找不到 failed 完成行（`found==nil`）：可能 `persistRunEvent` 没写、或事件 ID 与已有完成事件撞了被 `AppendSessionEventOnce` 去重。
   **不要改生产代码**——停下报告 `ListSessionEventsOfType` 实际返回了哪些 `tool_completed`（StepID/Status/Error/ID），让评审方判断是测试问题还是生产缺陷。
4. Test B 的 live 卡 `AgentID` 不是 worker-1，或落库 `ToolMeta.AgentID` 不是 worker-1，或落库腿 `found==nil`——停下报告实际值与
   `ListSessionEventsOfType` 返回。`found==nil` 先核对派发台账 seeding 是否在位（`req.SessionID` 来自 `dispatchingSessionIDForRun`，
   台账查不到即为空、落库被跳过）；AgentID 不符则可能 `resolveSubagentApproval` 没把 rae.AgentID 传进 gate，或路由另有机制。
5. Test C 回退腿得到空 stepID（`PendingApprovalToolStepID` 没从 snapshot 推出调用）——停下报告 snapshot 结构与 `PendingApprovalToolStepID` 的实际行为。
6. 任何既有 tui/turn 测试因新增测试而失败（例如全局状态污染）——停下报告是哪条。
7. 需要改生产代码才能让测试通过——本计划范围只加测试，STOP 报告。

## Maintenance notes

- Test A 用「failed 完成行落库」作 replay 契约的机器可检代理：`replayOrphanToolCallMessage`（`commands.go:1275-1302`）只对无结果行的调用画 canceled，
  所以 failed 行在场 ⇒ replay 画 failed 而非 canceled。若将来想加 render 级 replay 断言，入口在 `commands.go:685`（消费 `item.tool`/`item.toolStepID` 的 replay 管线），
  不是 `replayTurnsToRenderer`（那条吃 transcript `[]state.Message`，不吃 run 事件）。
- Test A/B 遍历而非计数 `tool_completed`：一个调用在审批前后可能有多条完成边界（见 `notification.go:147-164` `toolStepBoundary` 的注释），
  断言「存在一条 failed 且带错误」比断言总数稳健。
- 若将来 `notifyInPlaceFailedCard` 改成不落库（例如改用别的持久化通道），Test A 会红——那是契约变更，需同步更新测试与 replay 路径，不是测试 flaky。
- 子代理路由断言依赖 `EnrichToolMeta` 从 ctx 读 `HookAgentID`（`format.go:330-331`）。若路由机制改为别的载体，Test B 的 `AgentID` 断言要跟着改。
- Test B 的落库腿依赖派发台账（`dispatchingSessionIDForRun` → `agent.GetMerged`）。若将来子代理审批的 SessionID 改由别的载体推导（如 ctx 或 wait 行），
  seeding 与断言要跟着改；「live 腿绿、落库腿 found==nil」是该依赖断裂的特征信号。