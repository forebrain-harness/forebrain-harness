# Plan 342：gateway  live subagent 审批请求补 `tool_step_id`（与 detached/inline/socket 及本分支的 TUI 一致）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **工作目录**：`/Users/doudou/workspace/unionj-cloud/forebrain-harness/.worktrees/fix-user-interaction-subagent-ask`
> （分支 `fix-user-interaction-subagent-ask` 的 worktree，HEAD `4de94476`，写作时工作区干净）。下文所有相对路径都相对这个目录。
> **共用 worktree 提示**：339/340/341/342 可能并行在此 worktree 执行，兄弟计划留下的未提交改动属预期、不触发 STOP；
> 本计划的 drift check 与范围判据只按路径核对自己的 in-scope 文件。
>
> **Drift check（先跑）**：
> ```bash
> cd /Users/doudou/workspace/unionj-cloud/forebrain-harness/.worktrees/fix-user-interaction-subagent-ask
> git diff --stat 4de94476 -- pkg/gateway/approval.go pkg/gateway/approval_test.go   # 预期：无输出
> grep -n 'withApprovalToolStepID(data, rae)' pkg/gateway/approval.go   # 预期：仅 :436（inline），subagent 路径还没有
> ```
> 第一条有输出 → STOP。然后对照下方 "Current state" 摘录逐段核对；不一致即 STOP。
>
> **go 不在 PATH**：所有 go 命令用绝对路径 `/usr/local/go/bin/go`（go1.26.9）。cgo + fts5 标签必须带。

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW（只给已有的审批数据多加一个字段；读它的转换代码已存在）
- **Depends on**: none（与 339/340/341 零文件交集，可并行）
- **Category**: bug（一致性残留）
- **Planned at**: commit `4de94476`, 2026-11-20
- **Finding tag**: introduced（本分支打破了 TUI↔gateway 的 tool_step_id 平价；gateway 文件本身未改，但它是被改的 `turn.SubagentApprovalGateFromError` 的直接调用方）

## Why this matters

审批请求事件 `approval_requested` 上的 `tool_step_id` 命名「被审批的那个调用」，让 surface 能把 gate 的记录（决定行、它要求的卡）
锚到该调用，而不是落到整轮末尾。gateway 已有三处审批发布都写它：
- detached 续跑又停审批：`pkg/gateway/approval.go:211` `data = withApprovalToolStepID(data, gate)`
- 主代理就地审批：`pkg/gateway/approval.go:436` `data = withApprovalToolStepID(data, rae)`
- 主会话 socket 停审批：`pkg/gateway/server.go:2769` `approvalData = withApprovalToolStepID(approvalData, rae)`

**唯独 live subagent 审批的 WS 发布路径 `promptGatewaySubagentApproval`（`approval.go:302-389`）不写**（data 块 `:334-338` 之后直接 `writeMsg`）。

历史上这是 by-design：plan 011 明确把 gateway subagent 审批列为 out-of-scope，理由是「与 TUI 的 subagent 审批一致，不写 ToolStepID」。
**但本分支 `fix-user-interaction-subagent-ask` 改变了前提**：它给 `turn.SubagentApprovalRequest`（`pkg/turn/subagent_approval.go:92-113`）
新增了 `ToolStepID`（修前没有），于是 TUI 的就地 subagent 审批现在**写** tool_step_id——
`answerInPlaceApproval`（`pkg/tui/chat_surface.go:1507-1513`）发布 `approval_requested` 时带 `ToolStepID: strings.TrimSpace(req.ToolStepID)`，
而 `req` 正是 `SubagentApprovalRequest` 造的。TUI 侧现在命名调用，gateway live subagent 侧仍不命名 → 平价被打破。

后果：web 发起的会话里，子代理 ask gate 的 `approval_requested` 缺调用 id，按 toolStepId 锚定的 surface
（TUI 回放 gateway 会话、web 的 approval_requested 锚点逻辑）无法把这条子代理 gate 的记录落到它的调用上——
与 plan 011 修的 exit_plan_mode review 落位同类。修法＝照三个兄弟调用点，给 subagent 路径补同一行。

（注意：子代理 ask 的**表单**本身不受影响——web 从 action 行 payloadJson 读表单，那是另一条已核实可用的路径，不在本计划范围。）

## Current state

### 要改的函数

`pkg/gateway/approval.go:302-342`（行号为 worktree 当前值）：

```go
func (s *Server) promptGatewaySubagentApproval(
	ctx context.Context, rae *tool.RequiresActionError,
	requestID, sessionID string, writeMsg func(wsServerMsg),
) (context.Context, error) {
	gate, ok := turn.SubagentApprovalGateFromError(rae)
	if !ok {
		return nil, fmt.Errorf("not a subagent approval gate")
	}
	// ... s.Actions nil 检查、MarkWaitResumeOwner、ReleaseSubagentApprovalWait、defer Cancel ...
	data := approvalWSData(s.sessionPermissions(ctx, sessionID), sessionID, gate.ActionID, gate.ActionKind, gate.ToolName, gate.ToolInput)
	data["action_id"] = gate.ActionID
	data["action_kind"] = gate.ActionKind
	data["agent_id"] = gate.AgentID
	data["subagent_type"] = gate.SubagentType
	writeMsg(wsServerMsg{
		Op: "requires_action", RequestID: requestID, RunID: gate.RunID, SessionID: sessionID,
		Data: data, Message: turn.SubagentApprovalDescription(gate) + " Awaiting approval to continue.",
	})
	// ... 之后是 ticker 等待循环 ...
```

要在 `data["subagent_type"] = gate.SubagentType`（`:338`）之后、`writeMsg(...)`（`:339`）之前插一行：
`data = withApprovalToolStepID(data, rae)`。注意参数名是 **`rae`**（函数入参），不是 `gate`。

### 已有 helper（不改，直接调用）

`pkg/gateway/approval.go:195-200`：

```go
func withApprovalToolStepID(data map[string]any, gate *tool.RequiresActionError) map[string]any {
	if id := turn.ApprovalToolStepID(gate); id != "" {
		data["tool_step_id"] = id
	}
	return data
}
```

`turn.ApprovalToolStepID(rae)` = `rae.OriginatingToolCallID`（非空时）否则 `PendingApprovalToolStepID(rae.SessionSnapshot, rae.ToolName)`；
两者都空则返回 ""，helper 不写键。`approval.go` 已 import `turn` 与 `tool`，**无需新增 import**。

### 兄弟调用点（不改，作对照）

`approval.go:206-211`（detached）：
```go
	data := approvalWSData(...)
	data["action_id"] = gate.ActionID
	data["action_kind"] = gate.ActionKind
	data["agent_id"] = gate.AgentID
	data["subagent_type"] = gate.SubagentType
	data = withApprovalToolStepID(data, gate)   // ← subagent 路径要照这行
```

### wsServerMsg.Data 是 `any`（测试要类型断言）

`pkg/gateway/server.go:878-894`：`Data any \`json:"data,omitempty"\``。`approvalWSData` 返回 `map[string]any`，
所以 `writeMsg` 收到的 `m.Data` 动态类型是 `map[string]any`，断言时先 `m.Data.(map[string]any)`。

### 测试范例（照抄结构）

`pkg/gateway/approval_test.go:824-839` `TestGatewaySubagentApprovalCancelEndsTheChild`：

```go
func TestGatewaySubagentApprovalCancelEndsTheChild(t *testing.T) {
	s, actions := newApprovalHTTPServer(t)
	ctx := context.Background()
	action, err := actions.CreatePending(ctx, "session-1", "shell", map[string]any{"command": "ls", "session_id": "session-1"})
	require.NoError(t, err)
	rae := &tool.RequiresActionError{RunID: "child-run-1", ActionID: action.ID, ActionKind: "shell", ToolName: "shell"}
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, err := actions.Cancel(context.Background(), action.ID, "not now")
		require.NoError(t, err)
	}()
	_, err = s.promptGatewaySubagentApproval(ctx, rae, "req-1", "session-1", func(wsServerMsg) {})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cancelled while waiting for tool approval")
}
```

`newApprovalHTTPServer`（`approval_test.go:434-445`）返回 `&Server{Actions: actions, Runner: ...}`（**RunRT 为 nil**，
所以 `promptGatewaySubagentApproval` 跳过 MarkWaitResumeOwner，直接进 data 块 + writeMsg + 等待循环），并 Ensure 了 session "session-1"。
`writeMsg` 在等待循环**之前**同步调用一次——测试可在回调里捕获那条 `requires_action`，再靠并发 Cancel 让函数返回。

### 约定

- gateway 测试用 testify `require`；注释写"为什么"、英文完整句子（照范例上方的 doc 注释）。
- `approval_test.go` 已 import context/time/testing、tool/state/run/turn、require 等——本计划不需要新增 import。

## Commands you will need

| 用途 | 命令（在 worktree 根目录） | 成功预期 |
|---|---|---|
| 单条测试 | `/usr/local/go/bin/go test -tags fts5 ./pkg/gateway/ -count=1 -run '<Name>' -v` | PASS |
| gateway 整包 | `CGO_ENABLED=1 /usr/local/go/bin/go test -tags fts5 ./pkg/gateway/... -count=1` | `ok` |
| 架构 sanity | `CGO_ENABLED=1 /usr/local/go/bin/go test -tags fts5 ./pkg/architecture/... -count=1` | `ok`（预期不变：无新 import/扇出） |
| vet | `CGO_ENABLED=1 /usr/local/go/bin/go vet -tags fts5 ./pkg/gateway/...` | 无输出 |
| gofmt | `gofmt -l pkg/gateway/approval.go pkg/gateway/approval_test.go` | 无输出 |

## Scope

**In scope**：
- `pkg/gateway/approval.go`（`promptGatewaySubagentApproval` 的 data 块加一行 `data = withApprovalToolStepID(data, rae)`）
- `pkg/gateway/approval_test.go`（新增正向 + 负向两个测试）

**Out of scope**：
- `withApprovalToolStepID` helper 本身、`turn.ApprovalToolStepID`、`SubagentApprovalRequest`——已存在且正确，不改。
- 三个兄弟调用点（detached `:211`、inline `:436`、socket `server.go:2769`）——已写，不动。
- `pkg/gateway/wsevents.go` 的 `requires_action`→`approval_requested` 转换——plan 011 已让它读 `tool_step_id`
  （`ToolStepID: stringOr(approvalData["tool_step_id"])`），本计划无需改它；Step 4 会核实它在位。
- gateway 的 `approval_resolved` 发布、前端源码、`pkg/tui/**`、`pkg/turn/**` 生产代码。
- plan 339/340/341 的文件。

## Git workflow

不建分支、不 commit、不 push、不 stash（owner 手动提交）。改动留在 worktree 工作区。

## Steps

### Step 1：subagent 路径补一行

在 `pkg/gateway/approval.go` 的 `promptGatewaySubagentApproval` 里，`data["subagent_type"] = gate.SubagentType`（`:338`）之后、
`writeMsg(wsServerMsg{`（`:339`）之前插入：

```go
	data = withApprovalToolStepID(data, rae)
```

改后 data 块尾应为：
```go
	data["agent_id"] = gate.AgentID
	data["subagent_type"] = gate.SubagentType
	data = withApprovalToolStepID(data, rae)
	writeMsg(wsServerMsg{
		Op: "requires_action", RequestID: requestID, RunID: gate.RunID, SessionID: sessionID,
		Data: data, Message: turn.SubagentApprovalDescription(gate) + " Awaiting approval to continue.",
	})
```

**Verify**：`CGO_ENABLED=1 /usr/local/go/bin/go build -tags fts5 ./pkg/gateway/...` → 无输出；
`grep -n 'withApprovalToolStepID(data, rae)' pkg/gateway/approval.go` → 恰好 2 行（新增的 subagent 行 `:339` + inline 行——插入使 inline 从 `:436` 移到 `:437`；按计数核对，不按旧行号）；
`gofmt -l pkg/gateway/approval.go` → 无输出。

### Step 2：正向测试——live subagent 请求命名被审批调用

在 `pkg/gateway/approval_test.go` 的 `TestGatewaySubagentApprovalCancelEndsTheChild`（`:839` 结束）之后新增：

```go
// The live subagent gate names its gated call on the request, the same call the
// detached and inline paths and the terminal now name (the ask branch made the
// terminal's subagent approval carry tool_step_id). A surface anchors the gate's
// records to that call; without it a web-dispatched subagent gate cannot place them.
func TestGatewaySubagentApprovalRequestNamesTheGatedCall(t *testing.T) {
	s, actions := newApprovalHTTPServer(t)
	ctx := context.Background()
	action, err := actions.CreatePending(ctx, "session-1", "shell", map[string]any{"command": "ls", "session_id": "session-1"})
	require.NoError(t, err)
	rae := &tool.RequiresActionError{
		RunID: "child-run-1", ActionID: action.ID, ActionKind: "shell", ToolName: "shell",
		OriginatingToolCallID: "call-sub-1",
	}
	var captured wsServerMsg
	writeMsg := func(m wsServerMsg) { captured = m }
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, err := actions.Cancel(context.Background(), action.ID, "not now")
		require.NoError(t, err)
	}()
	_, err = s.promptGatewaySubagentApproval(ctx, rae, "req-1", "session-1", writeMsg)
	require.Error(t, err)
	require.Equal(t, "requires_action", captured.Op)
	data, ok := captured.Data.(map[string]any)
	require.True(t, ok, "requires_action data must be a map")
	require.Equal(t, "call-sub-1", data["tool_step_id"], "the live subagent gate must name its gated call")
}
```

（`captured` 无竞态：`writeMsg` 在测试 goroutine 内、等待循环之前同步写它；Cancel goroutine 只碰 action。函数返回后再读。）

**Verify**：`CGO_ENABLED=1 /usr/local/go/bin/go test -tags fts5 ./pkg/gateway/ -count=1 -run TestGatewaySubagentApprovalRequestNamesTheGatedCall -v` → 输出含 `--- PASS: TestGatewaySubagentApprovalRequestNamesTheGatedCall`（且无 `no tests to run`）。

### Step 3：负向测试——无名调用则不写键

紧跟 Step 2 之后新增（守 helper 的「无名则不带」语义，防把键恒设）：

```go
// A gate whose call nothing names carries no tool_step_id: neither an
// originating id nor a snapshot pending call. The key is absent, not empty.
func TestGatewaySubagentApprovalRequestOmitsUnnamedCall(t *testing.T) {
	s, actions := newApprovalHTTPServer(t)
	ctx := context.Background()
	action, err := actions.CreatePending(ctx, "session-1", "shell", map[string]any{"command": "ls", "session_id": "session-1"})
	require.NoError(t, err)
	rae := &tool.RequiresActionError{RunID: "child-run-1", ActionID: action.ID, ActionKind: "shell", ToolName: "shell"}
	var captured wsServerMsg
	writeMsg := func(m wsServerMsg) { captured = m }
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, err := actions.Cancel(context.Background(), action.ID, "not now")
		require.NoError(t, err)
	}()
	_, err = s.promptGatewaySubagentApproval(ctx, rae, "req-1", "session-1", writeMsg)
	require.Error(t, err)
	data, ok := captured.Data.(map[string]any)
	require.True(t, ok)
	_, present := data["tool_step_id"]
	require.False(t, present, "an unnamed call must not carry a tool_step_id key")
}
```

**Verify**：`CGO_ENABLED=1 /usr/local/go/bin/go test -tags fts5 ./pkg/gateway/ -count=1 -run TestGatewaySubagentApprovalRequestOmitsUnnamedCall -v` → 输出含 `--- PASS: TestGatewaySubagentApprovalRequestOmitsUnnamedCall`（且无 `no tests to run`）。

### Step 4：反证 + 转换核实 + 全量

**反证（确认正向测试钉住本修复）**：临时把 Step 1 加的那行 `data = withApprovalToolStepID(data, rae)` 注释掉，重跑
`CGO_ENABLED=1 /usr/local/go/bin/go test -tags fts5 ./pkg/gateway/ -count=1 -run TestGatewaySubagentApprovalRequestNamesTheGatedCall -v`
→ 应见 `--- FAIL: TestGatewaySubagentApprovalRequestNamesTheGatedCall`（`data["tool_step_id"]` 为 nil ≠ "call-sub-1"）。然后恢复该行。把反证结果写进报告。
（评审已在基线 `4de94476` 上用 `-overlay` 实证：修前正向 FAIL〔actual <nil>〕、负向 PASS；按计划插入该行后两条均 PASS。）

**核实转换已读该字段**（plan 011 的产物，应在位）：
`grep -n 'ToolStepID:.*stringOr(approvalData\["tool_step_id"\])' pkg/gateway/wsevents.go` → 应有 1 行。
若没有 → STOP 报告（说明本 worktree 的 wsevents.go 与预期不符，不要自行补）。

全量门禁：
```bash
cd /Users/doudou/workspace/unionj-cloud/forebrain-harness/.worktrees/fix-user-interaction-subagent-ask
CGO_ENABLED=1 /usr/local/go/bin/go test -tags fts5 ./pkg/gateway/... ./pkg/architecture/... -count=1
CGO_ENABLED=1 /usr/local/go/bin/go vet -tags fts5 ./pkg/gateway/...
gofmt -l pkg/gateway/approval.go pkg/gateway/approval_test.go
```

**Verify**：gateway/architecture 两包 `ok`（真实退出码）；vet、gofmt 无输出。

## Test plan

- `pkg/gateway/approval_test.go`：
  - `TestGatewaySubagentApprovalRequestNamesTheGatedCall`（正向）：rae 带 OriginatingToolCallID → live subagent `requires_action` 的 `Data["tool_step_id"]` 等于它。
  - `TestGatewaySubagentApprovalRequestOmitsUnnamedCall`（负向）：无名调用 → 键不存在。
- 结构范例：`TestGatewaySubagentApprovalCancelEndsTheChild`（newApprovalHTTPServer + CreatePending + 并发 Cancel + 捕获 writeMsg）。
- 反证：去掉 Step 1 那行 → 正向测试 FAIL。
- 验证：两条点名 PASS + gateway/architecture 整包 `ok` + vet/gofmt 净 + wsevents.go 转换字段在位。

## Done criteria

- [ ] `grep -n 'withApprovalToolStepID(data, rae)' pkg/gateway/approval.go` 恰好 2 行（subagent 新增 + inline；inline 行号因插入 +1，按计数核对）
- [ ] 两条新测试点名 `-run` 各见 `--- PASS: <Name>`（无 `no tests to run`）
- [ ] 反证做过：去掉新增行后正向测试 FAIL，结果写进报告
- [ ] `grep -n 'ToolStepID:.*stringOr(approvalData\["tool_step_id"\])' pkg/gateway/wsevents.go` 有 1 行（转换已读该字段）
- [ ] `CGO_ENABLED=1 /usr/local/go/bin/go test -tags fts5 ./pkg/gateway/... ./pkg/architecture/... -count=1` 两包 `ok`
- [ ] `CGO_ENABLED=1 /usr/local/go/bin/go vet -tags fts5 ./pkg/gateway/...` 与 `gofmt -l pkg/gateway/approval.go pkg/gateway/approval_test.go` 无输出
- [ ] 范围合规：`git diff --name-only 4de94476` 里本计划只改 `pkg/gateway/approval.go`（+1 行）与 `pkg/gateway/approval_test.go`（+2 测试）；若出现其它文件，只能是兄弟计划的 in-scope 文件（339：`.claude/skills/run-forebrain/fake_provider.py`；340：`pkg/tui/chat_surface_test.go`、`pkg/turn/subagent_approval_test.go`；341：`.claude/skills/run-forebrain/SKILL.md`、`driver.sh`），否则 STOP

## STOP conditions

1. Drift check 第一条有输出（approval.go/approval_test.go 相对 `4de94476` 已改动）。
2. `promptGatewaySubagentApproval` 的 data 块/writeMsg 结构与摘录不符（例如已有人补了 tool_step_id、或参数名不是 rae）——停下报告实际代码。
3. Step 4 的 wsevents.go 转换字段不在位——停下报告（本 worktree 可能不含 plan 011 的转换改动），不要自行补 wsevents.go。
4. 正向测试在**带**新增行时仍 FAIL（captured.Data 不是 map、或 tool_step_id 不等于 call-sub-1）——停下报告 captured 的实际内容。
5. 任何既有 gateway 测试因多了 tool_step_id 字段而失败（例如断言 payload 完全相等）——停下报告是哪条、断言了什么。
6. 需要改 `withApprovalToolStepID`/`turn.ApprovalToolStepID`/wsevents.go/前端才能完成——超出本计划范围，STOP 报告。

## Maintenance notes

- 修后 gateway 四处审批发布（detached/inline/socket/live-subagent）统一经 `withApprovalToolStepID` 命名调用。
  将来新增审批发布路径，照这四处补同一行；推导规则集中在 `turn.ApprovalToolStepID`，改规则只改那一处。
- 本计划依赖 plan 011 已让 `wsevents.go` 的 `requires_action` 转换读 `tool_step_id`（Step 4 核实）。若并回主树时 011 尚未落地，
  需先有该转换字段，否则加了 data 键也进不了 `approval_requested` payload——届时 STOP 报告依赖关系。
- 与 plan 011 的关系：011 修的是 exit_plan_mode review 的落位（detached/inline/socket + 转换），并当时把 subagent 路径列为 by-design 不写；
  本分支让 TUI subagent 侧开始写 tool_step_id，使该豁免过期，本计划补上 live subagent 这最后一处。两者不冲突（011 不动 subagent 路径）。
- 负向测试守住「无名则不带键」：若将来 `withApprovalToolStepID` 改成恒设键（哪怕空串），负向测试会红——那是契约变更，需同步评估 surface 对空 tool_step_id 的处理。