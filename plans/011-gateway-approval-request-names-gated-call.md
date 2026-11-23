# Plan 011：gateway 发出的审批请求带上被审批调用的 `tool_step_id`（与 TUI 一致），web 发起的会话重载后 review 卡不再落到整轮末尾

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **工作目录**：`/Users/doudou/workspace/unionj-cloud/forebrain-harness-008`（owner 指定的 worktree，
> detached HEAD `9656051`，工作区里已有 plan 008 的**未提交**前端改动——那是本计划的基础，不要还原、不要整理）。
> 下文所有相对路径都相对这个目录。
>
> **Drift check（先跑）**：
> ```bash
> cd /Users/doudou/workspace/unionj-cloud/forebrain-harness-008
> git diff --stat 9656051 -- pkg/gateway pkg/turn ':!pkg/gateway/dist'   # 预期：无输出（dist 是 008 的构建产物，排除）
> grep -c 'export function placeBeforeGateAnchor' frontend/src/composables/useChatStream.ts   # 预期：1（008 已在工作区）
> ```
> 第一条有输出、或第二条不是 1 → STOP。然后对照下方 "Current state" 摘录逐段核对；不一致即 STOP。

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW（只给事件多加一个字段；读这个字段的代码今天已经存在）
- **Depends on**: plan 008 的未提交实现（已在工作区）
- **Category**: bug
- **Planned at**: commit `9656051` + 008 工作区改动，2026-10-10（主树 HEAD `005c1a8` 的 `pkg/gateway`、`pkg/turn` 与 `9656051` 相同）

## Why this matters

plan 008 让 web 用一个不画任何东西的"锚点块"标记 exit_plan_mode 调用的位置，这个 gate 的记录（决定行、它要求的 plan review 卡）
都插在锚点之前。重载时锚点来自 transcript，只知道调用 id（`stepId`）；它要靠 `approval_requested` 事件里的 `toolStepId`
才能学到 `actionId`，`plan_review_started` 也只能按 `actionId` 找锚点。

**TUI 发出的 `approval_requested` 带 `tool_step_id`，gateway（web 发起的会话）发出的不带。** 结果是：web 发起的会话在
live 时一切正常，**重载后 review 卡落到整轮回答的末尾**（排在修订内容和后面的批准卡之后），live 与重载不一致——这正是
008 要修的问题 3，在 web 会话里原样存在。TUI 回放 gateway 会话时也因为同一原因找不到落位（`pkg/tui/commands.go`
的 `approvalEventAnchorPosition` 遇到空 `tool_step_id` 返回 -1）。

探针实证（008 工作区的前端，gateway 形态的事件：approval_requested 无 toolStepId、web 发起 review 无确认行）：

```
live   可见块序: [assistant(here is the plan), subagent(plan-review), assistant(revised plan), tool(call-b completed)]
reload 可见块序: [assistant(here is the plan), assistant(revised plan), tool(call-b completed), subagent(plan-review)]
```

给 `approval_requested` 补上 `toolStepId` 后（同一探针）live 与重载都是第一行的顺序。修法：gateway 用与 TUI 同一条规则
推导被审批调用的 id，写进 `approval_requested`。owner 已拍板走这条路（改 Go，而不是前端启发式兜底）。

## Current state

### TUI 怎么推导（标准，不改）

`pkg/tui/chat_session.go:1599-1607`（worktree 行号；主树此文件已被 007 改动，**本计划不碰它**）：

```go
func originatingApprovalToolStepID(rae *tool.RequiresActionError) string {
	if rae == nil {
		return ""
	}
	if id := strings.TrimSpace(rae.OriginatingToolCallID); id != "" {
		return id
	}
	return turn.PendingApprovalToolStepID(rae.SessionSnapshot, rae.ToolName)
}
```

TUI 的 `RecordApprovalGate`（`pkg/tui/chat_session.go`）把它写进 `event.ApprovalRequestedPayload.ToolStepID`。
TUI 的 subagent 审批（`pkg/tui/chat_surface.go` 的 `approval-requested:` 发布）**不写** ToolStepID。

`pkg/turn/approval.go:1751-1757`（已有，gateway 已 import `pkg/turn`）：

```go
func PendingApprovalToolStepID(snapshot []llm.Message, toolName string) string {
	call, ok := PendingApprovalToolCall(snapshot, toolName, "")
	if !ok {
		return ""
	}
	return strings.TrimSpace(call.ID)
}
```

`pkg/turn/approval.go` 已 import `github.com/forebrain-harness/forebrain-harness/pkg/tool`（第 19 行）。

### gateway 的三处（要改两处 + 一处转换）

1. 主会话 socket 上 run 停在审批：`pkg/gateway/server.go:1947`

```go
				approvalData := approvalWSData(s.sessionPermissions(r.Context(), sid), sid, rae.ActionID, rae.ActionKind, rae.ToolName, rae.ToolInput)
```

之后 `:2004-2011` 用它发 `requires_action`：

```go
				writeMsg(wsServerMsg{
					Op:        "requires_action",
					RequestID: m.RequestID,
					RunID:     runID,
					SessionID: sid,
					Data:      approvalData,
					Message:   "Awaiting approval or authorization to continue",
				})
```

2. 审批后继续跑（detached）又停在审批：`pkg/gateway/approval.go:148-165`

```go
func (s *Server) publishDetachedGatewayApprovalRequest(ctx context.Context, sessionID string, gate *tool.RequiresActionError) {
	if s == nil || gate == nil {
		return
	}
	data := approvalWSData(s.sessionPermissions(ctx, sessionID), sessionID, gate.ActionID, gate.ActionKind, gate.ToolName, gate.ToolInput)
	data["action_id"] = gate.ActionID
	data["action_kind"] = gate.ActionKind
	data["agent_id"] = gate.AgentID
	data["subagent_type"] = gate.SubagentType
	for _, evt := range canonicalRunEventsFromWS(wsServerMsg{
		Op: "requires_action", RunID: gate.RunID, SessionID: sessionID,
		Data: data, Message: "Awaiting approval or authorization to continue",
	}) {
		...
```

3. ws 消息 → 规范事件的转换：`pkg/gateway/wsevents.go:92-103`

```go
	case "requires_action":
		typeName = event.RunEventApprovalReq
		approvalData := anyMap(m.Data)
		payload = event.ApprovalRequestedPayload{
			ActionID:             firstNonEmptyString(stringOr(approvalData["action_id"]), stringOr(approvalData["id"])),
			ActionKind:           firstNonEmptyString(stringOr(approvalData["action_kind"]), stringOr(approvalData["kind"])),
			AgentID:              stringOr(approvalData["agent_id"]),
			SubagentType:         stringOr(approvalData["subagent_type"]),
			RequiresAction:       approvalRequiresActionPayload(m.Data),
			Message:              strings.TrimSpace(m.Message),
			PermissionSuggestion: approvalPermissionSuggestionPayload(m.Data),
		}
```

`event.ApprovalRequestedPayload.ToolStepID`（`pkg/event/approval_events.go`，json `tool_step_id`）已存在，只是 gateway 从不填。

### 前端（不改源码，只加回归测试）

`frontend/src/composables/useChatStream.ts` 的 conversation `approval_requested` 分支（008 已写好）：exit gate 时
`ensureGateAnchor(assistantMessageId, String(payload.toolStepId ?? '').trim(), actionId)`——有 toolStepId 就能找到 transcript
放下的锚点并写上 actionId。payload 键在进入前端前已转成 camelCase（`tool_step_id` → `toolStepId`）。

### 约定

- Go 注释写"为什么"、英文完整句子；新导出函数带 doc 注释（看 `pkg/turn/approval.go:1680-1694` 的风格）。
- gateway 测试用 testify `require`，样板：`pkg/gateway/approval_test.go:35` `TestDetachedApprovalResumePublishesToolAndNestedApprovalHistory`
  （建临时 DB、`publishDetachedGatewayApprovalRequest`、`runs.ListSessionEvents` 读回事件、反序列化 payload 断言）。
- `pkg/gateway/wsevents_test.go` 是表驱动（`name / msg / wantType / wantOK / needField / wantValue`），照 `"requires action carries message"` 那条加。
- 前端测试样板见 Step 4。

## Commands you will need

| 用途 | 命令（在 worktree 根目录） | 成功预期 |
|---|---|---|
| Go 单包测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... -count=1` | `ok` |
| | `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/... -count=1` | `ok` |
| 架构测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/... -count=1` | `ok` |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/gateway/... ./pkg/turn/...` | 无输出 |
| gofmt | `gofmt -l pkg/gateway pkg/turn` | 无输出 |
| 前端单文件 | `cd frontend && npx vitest run src/composables/useChatStream.test.ts` | 全部通过 |
| 前端全量 | `cd frontend && pnpm test` | 全部通过 |
| 类型检查 | `cd frontend && npx vue-tsc --noEmit -p tsconfig.json` | exit 0、无输出 |

## Scope

**In scope**：
- `pkg/turn/approval.go`（新增一个导出函数）
- `pkg/turn/approval_test.go`（新增测试）
- `pkg/gateway/approval.go`（新增一个小 helper，`publishDetachedGatewayApprovalRequest` 调用它）
- `pkg/gateway/server.go`（只在 `:1947` 那行之后加一行调用）
- `pkg/gateway/wsevents.go`（`requires_action` 转换加一个字段）
- `pkg/gateway/approval_test.go`、`pkg/gateway/wsevents_test.go`（新增测试）
- `frontend/src/composables/useChatStream.test.ts`（新增一个回归用例）

**Out of scope**：
- `pkg/tui/**`——TUI 已经写 ToolStepID；`chat_session.go` 在主树已被 007 改动，这里改它会制造并回冲突。
- gateway 的 subagent 审批（`pkg/gateway/approval.go` 里 `promptGatewaySubagentApproval` 发的 `requires_action`，约 `:328-336`）——
  与 TUI 的 subagent 审批一致，不写 ToolStepID。
- gateway 的 `approval_resolved` 发布（`api_extra.go`、`approval.go:474`）——前端按 actionId 能找到锚点，不需要。
- `server.go:1937` 合成的 `tool:<toolName>` 等待步骤（会在 web live 多出一个看不见的锚点，见 Maintenance notes）。
- `frontend/src/**` 的任何源码（`.ts`/`.vue`，测试文件除外）。
- `pkg/gateway/dist`（本计划不需要重新构建前端）。

## Git workflow

不建分支、不 commit、不 push、不 stash（owner 手动提交）。改动留在 worktree 工作区。

## Steps

### Step 1：`pkg/turn` 新增 `ApprovalToolStepID`

在 `pkg/turn/approval.go` 的 `PendingApprovalToolStepID`（`:1751`）之后加：

```go
// ApprovalToolStepID names the tool call a gate is holding: the call that
// raised it when the gate says so, otherwise the pending call of the gated
// tool in the snapshot the gate carried. Every surface that records a gate
// names its call through this, so the record anchors to the same call
// whichever surface wrote it. Empty when nothing names the call.
func ApprovalToolStepID(rae *tool.RequiresActionError) string {
	if rae == nil {
		return ""
	}
	if id := strings.TrimSpace(rae.OriginatingToolCallID); id != "" {
		return id
	}
	return PendingApprovalToolStepID(rae.SessionSnapshot, rae.ToolName)
}
```

在 `pkg/turn/approval_test.go` 加 `TestApprovalToolStepIDNamesTheGatedCall`（表驱动或三个子断言均可）：
- `nil` → `""`；
- `OriginatingToolCallID: "call-orig"` 且 snapshot 里有别的调用 → `"call-orig"`；
- 无 `OriginatingToolCallID`，`ToolName: "exit_plan_mode"`，`SessionSnapshot` 为
  `[]llm.Message{llm.UserMessage(llm.Text("plan")), llm.AssistantMessage(nil, llm.ToolCall{ID: "call-exit-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "exit_plan_mode", Arguments: "{}"}})}`
  → `"call-exit-1"`。

（`llm.AssistantMessage(parts, calls...)` 的用法见 `pkg/run/orchestration_llm_test.go:4489`。）

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/... -count=1 -run TestApprovalToolStepIDNamesTheGatedCall -v` → PASS；
`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/... -count=1` → `ok`。

### Step 2：gateway 把调用 id 写进审批数据

1. `pkg/gateway/approval.go`，在 `publishDetachedGatewayApprovalRequest` 上方加：

```go
// withApprovalToolStepID names, on a gate's approval data, the tool call the
// gate is holding (turn.ApprovalToolStepID) - the same call the terminal
// records for its own gates - so a surface can place the gate's records at
// that call. A gate whose call nothing names carries none.
func withApprovalToolStepID(data map[string]any, gate *tool.RequiresActionError) map[string]any {
	if id := turn.ApprovalToolStepID(gate); id != "" {
		data["tool_step_id"] = id
	}
	return data
}
```

2. `publishDetachedGatewayApprovalRequest`：在 `data["subagent_type"] = gate.SubagentType` 之后加一行
   `data = withApprovalToolStepID(data, gate)`。
3. `pkg/gateway/server.go:1947` 那行之后加一行 `approvalData = withApprovalToolStepID(approvalData, rae)`。
4. `pkg/gateway/wsevents.go` 的 `requires_action` 分支，在 `SubagentType:` 一行之后加
   `ToolStepID:           stringOr(approvalData["tool_step_id"]),`（对齐由 gofmt 决定）。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/gateway/...` → 无输出；`gofmt -l pkg/gateway pkg/turn` → 无输出；
`grep -n 'withApprovalToolStepID' pkg/gateway/*.go | grep -v _test` → 恰好 3 行（定义 1 + 调用 2）。

### Step 3：gateway 测试

1. `pkg/gateway/wsevents_test.go`：照 `"requires action carries message"` 加一条
   `name: "requires action names the gated call"`，`msg: wsServerMsg{Op: "requires_action", Data: map[string]any{"id": "a1", "kind": "exit_plan_mode", "tool_step_id": "call-7"}}`，
   `wantType: event.RunEventApprovalReq`，`wantOK: true`，`needField: "tool_step_id"`，`wantValue: "call-7"`。
   （该表在 `TestCanonicalRunEventFromWS` 里，断言循环 `:181-210` 把 payload 反序列化成 `map[string]any` 后按 `needField` 取值比较。）
2. `pkg/gateway/approval_test.go`：照 `TestDetachedApprovalResumePublishesToolAndNestedApprovalHistory`（`:35`）新增
   `TestDetachedApprovalRequestNamesTheGatedCall`：建临时 DB、会话 `conversation-1`、一个 run；调用
   `s.publishDetachedGatewayApprovalRequest(ctx, "conversation-1", &tool.RequiresActionError{RunID: run.ID, ActionID: "action-exit", ActionKind: "exit_plan_mode", ToolName: "exit_plan_mode", ToolInput: map[string]any{}, SessionSnapshot: <同 Step 1 第三种情况的 snapshot>})`；
   用 `runs.ListSessionEvents(ctx, "conversation-1", 0, 0, 10)` 读回，断言唯一一条 `approval_requested` 的
   `ApprovalRequestedPayload.ToolStepID == "call-exit-1"`、`ActionKind == "exit_plan_mode"`。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... -count=1 -run 'TestDetachedApprovalRequestNamesTheGatedCall|TestCanonicalRunEventFromWS' -v`
→ 新用例与子测试 `requires_action_names_the_gated_call` PASS；`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... -count=1` → `ok`。

### Step 4：前端回归用例（不改源码）

在 `frontend/src/composables/useChatStream.test.ts` 的 `describe('subagent_* call cards', …)` 里，紧跟 008 新增的
`'a reloaded review that failed reads like the live one'` 之后，新增
`it('a gate the web decided reloads like the live one once its request names the call', …)`。

**live 部分**用该 describe 的 `liveStream()` 辅助（自增 id/sequence、run `run-1`、session `s1`；payload 用 snake_case）。
事件依次（这就是 gateway 的真实形态：web 发起的 review 不写确认行；解决事件不带 toolStepId；gateway 还会补一帧合成的等待步骤）：

```ts
runEvent('assistant_delta', { text: 'here is the plan' })
runEvent('tool_call_started', { tool_name: 'exit_plan_mode', step_id: 'call-a', tool_meta: { tool_name: 'exit_plan_mode', status: 'running' } })
runEvent('tool_call_completed', { tool_name: 'exit_plan_mode', step_id: 'tool:exit_plan_mode', action_id: 'act-a', action_kind: 'exit_plan_mode', output: { requires_action: true }, tool_meta: { tool_name: 'exit_plan_mode', status: 'awaiting approval' } })
runEvent('approval_requested', { action_id: 'act-a', action_kind: 'exit_plan_mode', tool_step_id: 'call-a' })
runEvent('plan_review_started', { action_id: 'act-a', review_id: 'rev-1', provider: 'zhipuai', model: 'glm-5.3' })
runEvent('plan_reviewed', { action_id: 'act-a', review_id: 'rev-1', outcome: 'done', text: 'tighten' })
runEvent('approval_resolved', { action_id: 'act-a', action_kind: 'exit_plan_mode', decision: 'denied', reason: 'plan-review:delivered' })
runEvent('assistant_delta', { text: 'revised plan' })
runEvent('tool_call_started', { tool_name: 'exit_plan_mode', step_id: 'call-b', tool_meta: { tool_name: 'exit_plan_mode', status: 'running' } })
runEvent('approval_requested', { action_id: 'act-b', action_kind: 'exit_plan_mode', tool_step_id: 'call-b' })
runEvent('approval_resolved', { action_id: 'act-b', action_kind: 'exit_plan_mode', decision: 'approved' })
runEvent('tool_call_completed', { tool_name: 'exit_plan_mode', step_id: 'call-b', display_body: 'Exited plan mode.', tool_meta: { tool_name: 'exit_plan_mode', status: 'completed' } })
```

**重载部分**照 008 的 `'a reloaded delivered review reads like the live one'`（同 describe 内）：用同样的 `assistantParts` / `gateRow`
形状造 transcript（user → assistant `here is the plan` + 调用 `call-a` → tool 行 `call-a`，body 为
`'Plan review delivered — the planner is revising the plan.'`、status `denied` → assistant `revised plan` + 调用 `call-b` →
tool 行 `call-b`，body `'Exited plan mode.'`、status `completed`），`sessionEvents` 打桩为与 live 同序、**去掉**
`assistant_delta` 与 `tool_call_*` 的事件（payload 用 camelCase：`actionId / actionKind / toolStepId / reviewId …`），
`loadMessages('s1')` 后取 `messages.value[1]`。

**断言**（定义 `const visible = (blocks) => (blocks ?? []).filter((b) => b.kind !== 'gate').map((b) => b.kind === 'subagent' ? `subagent:${b.id}` : b.kind)`）：
- live 与重载的 `visible(...)` 都等于 `['assistant', 'subagent:plan-review:rev-1', 'assistant', 'tool']`；
- 两边相等（`expect(visible(reloaded)).toEqual(visible(live))`）。

（只比可见块：gateway 合成的 `tool:exit_plan_mode` 等待步骤会在 live 多出一个看不见的锚点，这是已知且无害的，见 Maintenance notes。）

在用例上方写一段英文 doc 注释说明：web 发起的 gate 不留确认行、它的解决事件不带调用 id，所以落位全靠请求事件里的调用 id。

**Verify**：`cd frontend && npx vitest run src/composables/useChatStream.test.ts -t 'once its request names the call'` → 1 passed；
**再确认它确实依赖本计划的修复**：临时把用例里两条 `approval_requested` 的 `tool_step_id`（及重载事件里的 `toolStepId`）删掉再跑一次 → 该用例**失败**
（重载顺序变成 review 在末尾）；然后恢复。

### Step 5：全量

```bash
cd /Users/doudou/workspace/unionj-cloud/forebrain-harness-008
CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/turn/... ./pkg/architecture/... -count=1
CGO_ENABLED=1 go vet -tags fts5 ./pkg/gateway/... ./pkg/turn/...
gofmt -l pkg/gateway pkg/turn
cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json
```

**Verify**：Go 三个包 `ok`；vet、gofmt 无输出；前端全绿；vue-tsc 无输出。

## Test plan

- `pkg/turn/approval_test.go`：`TestApprovalToolStepIDNamesTheGatedCall`（nil / OriginatingToolCallID 优先 / snapshot 推导）。
- `pkg/gateway/wsevents_test.go`：`requires_action` 的 `tool_step_id` 进入 `approval_requested` payload。
- `pkg/gateway/approval_test.go`：`TestDetachedApprovalRequestNamesTheGatedCall`（detached 路径落库的事件带 ToolStepID）。
- `frontend/src/composables/useChatStream.test.ts`：gateway 形态的 review 交付流程 live 与重载可见块序一致（Step 4，并做"去掉字段即失败"的反证）。
- socket 停审批路径（`server.go:1947`）只加一行调用、无独立单测，靠评审核对 + helper 的单测覆盖。

## Done criteria

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/turn/... ./pkg/architecture/... -count=1` 全部 `ok`
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/gateway/... ./pkg/turn/...` 与 `gofmt -l pkg/gateway pkg/turn` 无输出
- [ ] `cd frontend && pnpm test` 全绿；`npx vue-tsc --noEmit -p tsconfig.json` 无输出
- [ ] `grep -n 'withApprovalToolStepID' pkg/gateway/*.go | grep -v _test | wc -l` = 3
- [ ] `grep -n 'ToolStepID: *stringOr(approvalData\["tool_step_id"\])' pkg/gateway/wsevents.go` 有 1 行
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件（008 已有的前端改动与 `pkg/gateway/dist` 构建产物除外）
- [ ] Step 4 的反证做过且结果为"去掉字段即失败"，写进报告

## STOP conditions

1. Drift check 不符（`pkg/gateway` / `pkg/turn` 相对 `9656051` 有改动，或 008 的前端改动不在工作区）。
2. `approvalWSData` 的返回值在 `server.go:1947` 之后被其他代码重新赋值或拷贝，导致加的字段到不了 `requires_action`——停下报告实际数据流。
3. Step 4 的用例在**带** `tool_step_id` 时仍然失败（说明前端还有别的落位问题）——不要改前端源码，停下报告实际块序。
4. 任何既有 gateway 测试因为多了 `tool_step_id` 字段而失败（例如断言 payload 完全相等）——停下报告是哪条、断言了什么。
5. 需要改 `pkg/tui/**` 或前端源码才能完成。

## Maintenance notes

- **调用 id 的推导现在有两份**：TUI 的私有 `originatingApprovalToolStepID`（`pkg/tui/chat_session.go`）与新的
  `turn.ApprovalToolStepID`，逻辑相同。并回主树后可以让 TUI 那份直接调用 `turn.ApprovalToolStepID`（本计划为避免与 007 的
  `chat_session.go` 改动冲突没有动它）。改推导规则时两处一起改。
- gateway 的 `approval_resolved` 仍不带 `tool_step_id`；前端 exit gate 记录按 actionId 落位，TUI 回放的
  `planReviewAnchorByAction` 取同一 action 的第一条可定位记录（就是 `approval_requested`），都够用。
- `server.go:1937` 给停审批的调用补发一帧 `step_id = "tool:<toolName>"` 的合成等待步骤；web live 遇到 exit gate 的这帧会多建一个
  看不见的锚点（`gate:tool:exit_plan_mode`），重载没有它。可见内容不受影响；将来若有逻辑依赖"锚点数目"，先把这帧换成真实调用 id。
- 已经落库、没有 `tool_step_id` 的旧 gateway 会话不会被修复（显示历史，不迁移）；本机状态库里没有这类 exit gate 记录。
