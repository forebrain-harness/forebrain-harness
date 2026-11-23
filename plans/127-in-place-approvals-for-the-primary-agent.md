# Plan 127：主代理当场审批——在 TUI 和 web 上，审批与提问在回合内当场问、回合不结束；流中启动的调用也当场问；答复后在原处继续

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在 dev 工作区。
> 工作区里若有与本计划无关的未提交改动，**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）**：`plans/124`、`plans/129` 已完成：`grep -n "RejectedStopText" pkg/tool/format.go`、`grep -n "ErrTurnStoppedByRejection" pkg/run/orchestration_llm.go`、`grep -n "ErrDeferToStreamEnd" pkg/run/runner.go pkg/tool/registry.go`、`grep -n "type streamingToolExecutor struct" pkg/run/streaming_tools.go`、
> `grep -n "func (s \*State) CallConcurrencySafe" pkg/tool/orchestration.go` 各有结果。
>
> **Drift check**：按下面摘录里的符号定位；锚点找不到或内容不符即 STOP 1。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: HIGH（改变主代理审批的生命周期：从“停回合 → 续跑”变为“当场等答复”；涉及落库等待记录、租约、执行栅栏、两端）
- **Depends on**: plans/124、plans/129（拒绝语义）
- **Category**: direction（owner 2026-10-25 定稿：与 Claude Code 一致，审批在流中当场询问；范围为主代理所有审批与提问）
- **Planned at**: dev `4d7a3c9` + 121 + 122 + 125 + 126 + 123 + 124，2026-10-25

## Why this matters

Claude Code 的工具调用碰到权限询问时，当场弹出询问、在原处等答复，回合不结束，模型的流照常往下走；答复后该调用在原处执行或得到拒绝结果，排在它后面的调用随后继续。
forebrain 主代理现在的审批会结束回合（`RequiresActionError` → 回合停在 waiting_action → 答复后另起续跑重放）；`plans/124` 为此把流中碰到审批的调用推迟到流结束。owner 定稿：**改成与 Claude Code 一致**。

目标行为：
1. 在有交互端的运行上（TUI；web 的 WS 对话与后台续跑），主代理自己的调用产生的审批（`actionHook` 创建的待审批，含 `request_permissions`）与提问（`user_interaction`）都**当场**问：创建待审批、在对话里显示审批卡/提问表单、该调用在原处等答复；回合保持运行，流（若还在流）照常继续。
2. 答复（拒绝语义见 `plans/129`，与 Claude Code 一致）：允许 → 该调用以批准身份在原处重新执行，得到真实结果；**带说明拒绝** → 只有该调用得到拒绝结果（`tool.RejectionText`），同一回答的其他调用照常执行，回合继续；**不带说明拒绝**（“No”空着提交、Esc、Ctrl+C）→ 该调用得到 `tool.RejectedStopText`，**回合中止**：流（若还在流）被取消，还没开始的调用得到同一句结果且显示为隐藏键（`RejectionStoppedDisplayKey`），正在执行的被中断，落库后回合以 `ErrTurnStoppedByRejection` 结束（两端按取消显示）；回合被用户 Esc 中止 → 中断结果。
3. 排队：等答复的调用算“正在执行”。它不是并发安全的 → 挡住后面所有调用（Claude Code 的规则）；是并发安全的 → 其他并发安全的调用照常同时执行。同一运行同一时刻只问一个：第二个需要审批的调用在创建待审批之前排队，等第一个结束（含批准后的执行）再问（落库等待表每个运行只有一行）。
4. 流中：`plans/124` 的“推迟到流结束”只在**没有当场审批通道**时生效（IM 渠道、定时任务等无人值守的运行），以及 `exit_plan_mode`。
5. 保留“停回合 → 续跑”的情况：`exit_plan_mode`（需要“清空上下文重开”等选项）、没有当场审批通道的运行、通道明确拒绝当场处理（钩子把原 gate 原样返回）。
6. 崩溃恢复：当场等待期间，落库等待行带完整快照（会话 + 本回答 + 已完成调用的结果），并由当前进程持有租约；进程退出后租约过期，另一个进程/重启后的同一会话能按现有续跑路径恢复这一个审批。批准后的执行受现有执行栅栏保护（不会被二次执行）。
7. 子代理的当场审批（现有 `SubagentApprovalHook`）不变；两者共用同一个审批弹层的串行锁。

## Current state

- `pkg/tool/state.go`：`type SubagentApprovalHook func(ctx context.Context, rae *RequiresActionError) (context.Context, error)`、`WithSubagentApprovalHook(ctx, hook)`、`SubagentApprovalHookFromContext(ctx, fallback *State)`、`(*State).SetSubagentApprovalHook` / `SubagentApprovalHook()`（上下文上的钩子优先，State 上的是 TUI 单会话的兜底）。
- `pkg/tool/request_permissions.go`：`type RequiresActionError struct { RunID, AgentID, SubagentType, ActionID, ActionKind, ToolName string; ToolInput any; OriginatingToolCallID string; SessionSnapshot []llm.Message }`；`ToolApprovalResumeState{Session, Denied, DenyReason, DenyFeedback, DeliveredReview, BeginContinuation, EndContinuation}`；`WithToolApprovalResume`、`ToolApprovalResumeFromContext`、`BeginApprovalContinuation(ctx)`、`EndApprovalContinuation(ctx)`；`WithApprovedActionID`、`ApprovedActionIDFromContext`。
- `pkg/run/runner.go` `func (r *Runner) actionHook(ctx, kind, payload) (string, bool, error)`：已带 `ApprovedActionIDFromContext` 时放行；纯策略判定；`124` 加的 `if tool.StreamingDispatchFromContext(ctx) { return "", false, tool.ErrDeferToStreamEnd }`（在 `permissionRequestHookApplies` 之前）；外部钩子；guardian；然后 `a, err := r.Actions.CreatePending(context.Background(), sessionID, kind, payload)` 与 `r.RunRT.SetWaitingAction(... state.Wait{RunID: rid, ActionID: a.ID, ToolName, ToolInputJSON, AgentID, SubagentType, SandboxProfile, RequestedProfile, ProfileElevation})`；`return a.ID, true, nil`。`rid := r.policyRunIDForTool(ctx)`。
- `pkg/tool/registry.go` `newUserInteractionTool`：已带 `ApprovedActionIDFromContext` 时读答案；否则（124 之后先查 `StreamingDispatchFromContext`）`rt.Actions.CreateAsk(ctx, form)` + `rt.RunRT.SetWaitingAction(...)` → `&RequiresActionError{ActionID, ActionKind: "user_interaction", …}`。
- `pkg/state/runrt.go`：`fb_run_waits` 以 `run_id` 为主键（每运行一行）；`SetWaitingAction`（整行 upsert，并把 `fb_runs.status` 置 waiting_action）、`MarkWaitResumeOwner`、`WaitResumeOwnedBy`、`ClaimWaitResume`、`BeginWaitResumeExecution`、`ClearWait`、`SetStatus`；`WaitResumeLease = 5 * time.Second`。
- `pkg/turn/approval.go`：`(*PendingApprovalGate).Pending(ctx, sessionID)`（先由 `RunIDHint`/`FirstWaitingRunIDForSession` 定 runID，再从等待行 + action 构造与停回合路径完全相同的 `ToolApprovalRequest`，含 `AskFormJSON`、`RequestedPermissions`、`ToolStepID`）；`BindApprovalContinuationFence(resume, runs, runID, actionID, owner)`；`ApprovalToolStepID(rae)`。
  `pkg/turn/subagent_approval.go`：`SubagentApprovalGate`、`SubagentApprovalGateFromError`、`ReleaseSubagentApprovalWait`、`SubagentApprovalResumeContext(ctx, gate, resume, who)`（批准 → `WithApprovedActionID` + `WithToolApprovalResume{Session, Denied, DenyReason, DenyFeedback}`；既未批准也未拒绝 → 错误）。
- TUI `pkg/tui/chat_surface.go`：`syncToolApprovalHooks`（State 级安装 `SetSubagentApprovalHook(s.resolveSubagentApproval)`）；`resolveSubagentApproval`（持 `s.subagentApprovalMu`；sink 为 nil 时 `return nil, rae` 交回停回合路径）→ `answerSubagentApproval(ctx, sink, gate, req, who)`：
  `MarkWaitResumeOwner` 抢租约 → 后台续租 → 发布 `approval-requested` 事件（`event.ApprovalRequestedPayload{ActionID, ActionKind, AgentID, SubagentType, Message: turn.SubagentApprovalDescription(gate)}`）→ `sink.PromptToolApproval(promptCtx, req)` → `WaitResumeOwnedBy` → `turn.ReleaseSubagentApprovalWait(...)` → `s.completeSurfaceToolApprovalDecision(ctx, gate.ActionID, decision)` → `turn.SubagentApprovalResumeContext(...)` → `SetStatus(running)` + `BindApprovalContinuationFence(...)`。
  `pkg/tui/chat_turn.go` 每轮的上下文级安装：`if s.toolApprovalSinkGet() != nil { agBase = tool.WithNetworkApprovalPromptHook(...); agBase = tool.WithSubagentApprovalHook(agBase, s.resolveSubagentApproval) }`。
  `pkg/tui/channels.go` `(*ApprovalSink).PromptToolApproval`：`user_interaction` 走提问表单，其余走审批弹层；画之前 `RecordApprovalGate(req)` 落一条显示记录。
- gateway `pkg/gateway/approval.go`：`promptGatewaySubagentApproval(ctx, rae, requestID, sessionID, writeMsg)`（抢租约 → `ReleaseSubagentApprovalWait` → 发 `requires_action` WS 消息（`approvalWSData` + action/agent 字段）→ 每 50ms 轮询 action 状态并续租 → 答复后 `SubagentApprovalResumeContext` + `SetStatus(running)` + `BindApprovalContinuationFence`；未答复退出时 `Actions.Cancel`）；
  `publishDetachedGatewayApprovalRequest` 用 `withApprovalToolStepID(data, gate)` 带上 `tool_step_id`；安装点：`pkg/gateway/server.go` WS 对话（`agCtx = tool.WithSubagentApprovalHook(agCtx, func(...) { return s.promptGatewaySubagentApproval(approvalCtx, rae, m.RequestID, sid, writeMsg) })`）与 `withDetachedGatewayApprovalHooks`（后台续跑）。
  web 前端：`requires_action` 只刷新待审批列表；`approval_requested` 按 `toolStepId` 把审批卡挂到工具卡上（`upsertConversationApproval`）。
- `pkg/run/orchestration_llm.go`：
  - 续跑拒绝路径（`consumeResumeSnapshot` 之后的 `if resumeAssistant.denied {`）：每个缺结果的调用 → `llm.ToolResultMessage(tc.ID, llm.Text(buildDenialMessage(tc.Function.Name, denyReason)))`，`denial.ToolDisplay = toolDisplayState(tool.DeniedToolStep(toolStepID(tc), tc.Function.Name, toolInputMap(tc.Function.Arguments), denyFeedback))`，并 `step(ctx, refused)`。
  - `executeToolCalls`：`plans := w.state.PartitionToolCallsFor(calls)`（124）→ `segmentToolCallPlan`（有 action hook 时，并发安全的调用除子代理派发外一个一段，因为“一次运行只有一个等待行，续跑把批准交给第一个重放的调用”）→ 并发段 `executeConcurrentBatch` + `settleConcurrentGates`；串行段 `executeOneToolCall`，`isRequiresActionError` 时提前返回。
  - `firstControlFlowToolError(results)`：停回合的出口。
  - `pkg/run/fork.go` `withForkRuntimeSnapshot(ctx, messages)` / `forkRuntimeSnapshotFromContext(ctx) ([]llm.Message, bool)`。
- `pkg/run/streaming_tools.go`（124）：`streamingToolExecutor`（`add`/`pump`/`finish`/`abort`/`reconcile`/`dispatched`），调用以 `tool.WithStreamingDispatch(callCtx)` 执行；`res.deferred` → 状态 deferred、`halted = true`。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Go 测试 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" FOREBRAIN_HOME="$(mktemp -d)"; CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/... ./pkg/state/... ./pkg/turn/... ./pkg/run/... ./pkg/tui/... ./pkg/gateway/... -count=1` | 全部 `ok` |
| race | 同上环境；`CGO_ENABLED=1 go test -tags fts5 -race ./pkg/run/ ./pkg/tool/ -run 'InPlace|Inline|ApprovalSlot|Streaming' -count=1` | `ok`，无 `DATA RACE` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/...`；`gofmt -l pkg/` | 无输出 |
| 前端 | `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts && pnpm typecheck` | 通过 / exit 0 |
| 真机 | `.claude/skills/run-forebrain/driver.sh`（Step 9） | 见期望 |

## Scope

**In scope**：`pkg/tool/state.go`、新建 `pkg/tool/inline_approval.go`(+test)、`pkg/tool/registry.go`（提问工具一处）、`pkg/state/runrt.go`(+test)、`pkg/turn/approval.go`(+test)、`pkg/run/runner.go`（`actionHook`）、`pkg/run/orchestration_llm.go`(+test)、`pkg/run/streaming_tools.go`(+test)、
`pkg/tui/chat_surface.go`、`pkg/tui/chat_turn.go`、TUI 测试、`pkg/gateway/approval.go`、`pkg/gateway/server.go`（安装一处）、gateway 测试、`frontend/src/composables/useChatStream.ts`(+test，仅当 Step 8 的测试暴露问题时)、
`.claude/skills/run-forebrain/fake_provider.py` 与 `SKILL.md`（Step 9 的模式）。

**Out of scope**：子代理审批（不变）；网络放行（`NetworkApprovalPromptHook`，已是当场）；`exit_plan_mode` 与计划评审流程；拒绝的文字、审批框的 “No” 输入行与停回合路径上的拒绝（`129` 已做）；审批弹层/卡片的外观；IM 渠道；`process/run_executor.go` 等无人值守执行器（没有钩子，自然走停回合路径）。

## Steps

### Step 1：钩子与审批槽位（`pkg/tool`）

1. `state.go` 照 `SubagentApprovalHook` 的写法加：

```go
// InlineApprovalHook answers one of the primary agent's own approval gates
// where the call is, while the turn keeps running, and returns the context the
// call continues under: the approved action id and the resume state. Returning
// the gate itself (errors.Is(err, rae)) hands it back to the unwind-and-resume
// path.
type InlineApprovalHook func(ctx context.Context, rae *RequiresActionError) (context.Context, error)

func WithInlineApprovalHook(ctx context.Context, hook InlineApprovalHook) context.Context
func InlineApprovalHookFromContext(ctx context.Context, fallback *State) InlineApprovalHook
func (s *State) SetInlineApprovalHook(h InlineApprovalHook)
func (s *State) InlineApprovalHook() InlineApprovalHook
```

2. `inline_approval.go`：

```go
// ApprovalSlotHolder carries, for one tool call that may be asked about in
// place, the run's approval slot once the call took it. A run has one durable
// wait row, so it asks one question at a time: a second gate waits for the
// first to be answered and its call to finish.
type ApprovalSlotHolder struct {
	RunID string
	// mu, release func(), held bool
}

func WithApprovalSlotHolder(ctx context.Context, h *ApprovalSlotHolder) context.Context
func ApprovalSlotHolderFromContext(ctx context.Context) *ApprovalSlotHolder

// AcquireInPlaceApprovalSlot takes runID's slot for the call ctx belongs to,
// waiting for it while another call of the run holds it. It is a no-op when
// ctx carries no holder, when the holder is for another run, or when the
// holder already holds the slot (a second gate inside the same call).
func AcquireInPlaceApprovalSlot(ctx context.Context, runID string) error

// Release gives the slot back; safe to call more than once and on a holder
// that never took it.
func (h *ApprovalSlotHolder) Release()
```
   槽位登记是包级的：`map[runID]chan struct{}`（容量 1，受互斥锁保护；没有持有者时删除条目）。获取 = 向 channel 发送，`ctx.Done()` 时返回 `ctx.Err()`。
   测试：同一运行两个持有者串行（第二个在第一个 `Release` 后才拿到）；不同运行互不影响；ctx 取消时返回错误且不占槽；同一持有者重复获取不阻塞；`Release` 幂等。

### Step 2：只更新等待行的快照（`pkg/state/runrt.go`）

```go
// SetWaitSessionSnapshot replaces the session snapshot of runID's wait on
// actionID, leaving the rest of the row — its owner lease among it — as it is.
func (s *RunStore) SetWaitSessionSnapshot(ctx context.Context, runID, actionID string, snapshot []llm.Message) error
```
   `UPDATE fb_run_waits SET session_snapshot_json=?, updated_at=? WHERE run_id=? AND action_id=?`；没有匹配行返回 `ErrNoWaitContinuation`。测试一例（写入后 `GetWaitForRun` 读回同一快照，租约字段不变）。

### Step 3：审批与提问在当场模式下取槽，不再推迟

1. `runner.go` `actionHook`：
   - `124` 的推迟判断改为：`if tool.StreamingDispatchFromContext(ctx) && (exitPlanMode || tool.InlineApprovalHookFromContext(ctx, r.tools) == nil) { return "", false, tool.ErrDeferToStreamEnd }`（`r.tools` 若不是该字段名，用 `actionHook` 所在 Runner 持有的 `*tool.State`；找不到即 STOP 1）。
   - 在 `a, err := r.Actions.CreatePending(...)` **之前**：`if !exitPlanMode { if err := tool.AcquireInPlaceApprovalSlot(ctx, r.policyRunIDForTool(ctx)); err != nil { return "", false, err } }`。
2. `registry.go` 提问工具：推迟判断同样改为“流中且没有当场钩子”（`InlineApprovalHookFromContext(ctx, st)`）；`rt.Actions.CreateAsk` 之前同样 `AcquireInPlaceApprovalSlot(ctx, rid)`（`rid` 与随后 `SetWaitingAction` 用的同一个）。
3. 测试（`pkg/run`）：带钩子 + 流式派发的 ctx 调 `actionHook` → 创建了待审批（不再 `ErrDeferToStreamEnd`）；不带钩子 → 仍推迟；`exit_plan_mode` 带钩子 → 仍推迟；两个持有同一运行持有者的调用先后创建待审批时第二个阻塞到第一个 `Release`。

### Step 4：`PendingForRun`（`pkg/turn/approval.go`）

把 `(*PendingApprovalGate).Pending` 里“runID 已确定之后”的部分抽成 `func (g *PendingApprovalGate) PendingForRun(ctx context.Context, sessionID, runID string) (*ToolApprovalRequest, error)`，`Pending` 改为解析出 runID 后调用它（行为不变，现有测试照旧）。

### Step 5：编排层——在原处回答调用自己的 gate

`orchestration_llm.go` 新增：

```go
// runCallInPlace executes one call; when the call raises its own approval gate
// and the surface can answer it in place, the gate is answered here and the
// call re-runs approved, or is refused, without ending the turn. snapshot
// returns the session a crash-recovery resume would replay: the history, this
// response's assistant message and every result this response has so far.
func (w *toolOrchestrationLLM) runCallInPlace(
	ctx context.Context, index int, call llm.ToolCall, toolMap map[string]*llm.Tool,
	suppress toolUISuppression, snapshot func() []llm.Message,
) (res orchestratedToolResult, stop bool)
```

逻辑：
1. `hook := tool.InlineApprovalHookFromContext(ctx, w.state)`；为 nil → 直接 `return w.executeOneToolCall(ctx, …), nil`。
2. `holder := &tool.ApprovalSlotHolder{RunID: tool.RunIDFromContext(ctx)}`（若编排层另有“当前运行 ID”的取法、且与 `actionHook` 的 `policyRunIDForTool` 一致，用那个；两者不一致即 STOP 4）；`defer holder.Release()`；`callCtx := tool.WithApprovalSlotHolder(ctx, holder)`。
3. 循环（最多 5 次，防止无穷）：`res := w.executeOneToolCall(callCtx, index, call, toolMap, suppress)`；取 `rae`（`errors.As`）；没有 → 返回 `res`。
   `rae.AgentID != ""`（子代理的 gate 冒上来）或 `rae.ActionKind == "exit_plan_mode"` → 返回 `res`（走现有停回合路径）。
   补全 gate：`rae.OriginatingToolCallID` 为空时设为 `toolStepID(call)`；`rae.SessionSnapshot = snapshot()`。
   `resumeCtx, err := hook(ctx, rae)`：`errors.Is(err, rae)` → 返回 `res`（交回停回合）；其他 `err` → `ctx.Err() != nil` 时返回 `orchestratedToolResult{index, call, err: ctx.Err()}`，否则 `orchestratedToolResult{index, call, err: err}`。
   `st := tool.ToolApprovalResumeFromContext(resumeCtx)`：
   - `errors.Is(err, tool.ErrApprovalRejectedStop)`（不带说明拒绝，见 Step 6/7）→ 返回 `w.deniedToolResult(ctx, index, call, tool.RejectedStopText, "")` 与 `stop = true`。
   - `st.Denied`（带说明拒绝，或子代理/decline 的无说明拒绝）→ 用与续跑拒绝路径**同一段代码**生成拒绝结果（把那段抽成 `func (w *toolOrchestrationLLM) deniedToolResult(ctx, index int, call llm.ToolCall, text, feedback string) orchestratedToolResult`，`text` 由调用方用 `tool.RejectionText(...)` 算好，续跑路径也改用它，结果消息与卡片不变）；`tool.EndApprovalContinuation(resumeCtx)`；返回该结果，`stop = false`。
   - 批准 → `if err := tool.BeginApprovalContinuation(resumeCtx); err != nil { return 错误结果 }`；`callCtx = tool.WithApprovedActionID(callCtx, rae.ActionID)`（**不要**把 `resumeCtx` 当执行上下文，它带着续跑快照，嵌套的编排会误用）；继续循环（重新执行；若又出现新的 gate 再走一遍）；本轮结束（无论结果）时 `tool.EndApprovalContinuation(resumeCtx)`，下一轮前清掉 `callCtx` 里上一个批准 ID（`tool.WithApprovedActionID(callCtx, "")` 再设新的）。
4. 中止：调用方拿到 `stop == true` 后：本回答里**还没开始**的调用都用 `rejectionStoppedResult(i, c)`（129）作为结果、不执行；正在执行的调用随回合级取消被中断（结果为中断错误）；编排层取消本次模型调用的上下文（见“接入”），落库后 `return nil, ErrTurnStoppedByRejection`（129）。带说明拒绝不影响其他调用。

接入：
- `executeToolCalls`：串行段与 `executeConcurrentBatch` 里的 `w.executeOneToolCall(...)` 换成 `w.runCallInPlace(...)`；`snapshot` = `forkRuntimeSnapshotFromContext(ctx)` 的会话 + 本批到目前为止已有结果的调用按调用顺序生成的结果消息（`toolResultMessage`）；中止按上面第 4 条（串行段直接给后续调用中止结果；并发段取消同段其他调用并等它们结束，后续段全部给中止结果）；`executeToolCalls` 需要把“中止”告诉主循环（加一个返回值或结果标记，主循环据此落库并返回 `ErrTurnStoppedByRejection`）。
- `segmentToolCallPlan`：有当场钩子（`tool.InlineApprovalHookFromContext(ctx, w.state) != nil`）时，并发安全的整组作为一个并发段（槽位保证同一时刻只问一个），不再一个一段；没有钩子时保持原样。
- 流中执行器（`streaming_tools.go`）：goroutine 里 `executeOneToolCall(tool.WithStreamingDispatch(callCtx), …)` 换成 `runCallInPlace(tool.WithStreamingDispatch(callCtx), …, snapshot)`，`snapshot` = `beforeStart` 返回的 provisional 会话 + 已完成调用的结果；`stop == true` → 持锁把所有 queued 调用标为中止结果（不启动）、`halted = true`，并调用主循环给的 `stopTurn()`：主循环在调 `w.inner.Execute` 前用 `context.WithCancelCause` 包一层，`stopTurn` 即 `cancel(ErrTurnStoppedByRejection)`；`inner.Execute` 因此返回后，主循环见 `errors.Is(context.Cause(callCtx), ErrTurnStoppedByRejection)` 就走 124 的取消落库（provisional assistant + 各调用结果，中止结果用 `rejectionStoppedResult`）并 `return nil, ErrTurnStoppedByRejection`（不是普通取消）；
  有钩子时 `res.deferred` 不会再出现（只剩 `exit_plan_mode` 与无钩子的运行）。
- 测试（`orchestration_llm_test.go`、`streaming_tools_test.go`，照 124 的脚本化模型与测试工具写法；测试钩子是一个可控的 `InlineApprovalHook`，返回 `turn.SubagentApprovalResumeContext` 同构的 ctx）：
  1. `TestInPlaceApprovalRunsTheCallWithoutEndingTheTurn`：调用 A 需要审批，钩子批准 → A 执行一次，回合不返回 `RequiresActionError`，继续下一次模型请求。
  2. `TestInPlaceRejectionWithWordsRefusesOnlyThatCall`：A、B 串行，A 被带说明拒绝 → A 的结果为 `To tell you how to proceed, the user said:\n…`，B 照常执行，回合继续。
  2b. `TestInPlaceRejectionWithoutWordsStopsTheTurn`：A、B 串行，A 被不带说明拒绝（钩子返回 `tool.ErrApprovalRejectedStop`）→ A 为 `RejectedStopText`、B 为中止结果（隐藏键）、没有下一次模型请求、返回 `ErrTurnStoppedByRejection`；流中版本：A 在流中被拒 → 模型调用的上下文被取消、之后 ready 的调用都是中止结果。
  3. `TestInPlaceGateIsAskedWhileTheStreamContinues`：流中 ready A（需审批）→ 钩子阻塞；模型继续发正文并 ready B（并发安全、无需审批）→ A 不安全时 B 等到 A 答复后才执行；A 安全时 B 立即执行。
  4. `TestOnlyOneInPlaceQuestionPerRun`：A、B 都安全且都需审批 → 钩子先只看到 A；A 批准并执行完后才看到 B。
  5. `TestNoHookFallsBackToTheUnwindPath`：不装钩子 → 行为与现在一致（`RequiresActionError` 停回合）。
  6. `TestExitPlanModeStillUnwinds`：装钩子，`exit_plan_mode` 的 gate 仍停回合。
  7. `TestCancelWhileAnsweringInPlace`：钩子阻塞时取消 → 返回取消；该调用得到中断结果。
  8. `TestInPlaceSnapshotCarriesCompletedResults`：A 完成、B 需审批 → 钩子收到的 `rae.SessionSnapshot` 含 assistant（A、B）与 A 的结果。

### Step 6：TUI 实现钩子

1. `chat_surface.go`：把 `answerSubagentApproval` 改名为 `answerInPlaceApproval(ctx, sink, gate, req, who, message string)`，发布的 `ApprovalRequestedPayload` 用参数 `message`，并加上 `ToolStepID: req.ToolStepID`；两个子代理调用点传 `turn.SubagentApprovalDescription(gate)`（行为不变）。
2. `pkg/tool` 加哨兵 `var ErrApprovalRejectedStop = errors.New("the user refused this call without words")`；`answerInPlaceApproval` 在 `decision.Cancelled` 且是主代理的 gate（`gate.AgentID == ""`）时，完成决定后返回 `(nil, tool.ErrApprovalRejectedStop)`（子代理照旧返回现有的取消错误）。
   完成决定时不得触发停回合路径的处理：给 `completeSurfaceApproval` 传一个“当场回答”的标记（`ApprovalSubmit` 加 `InPlace bool`，或 ctx 标记，取现有代码里更顺的一种），带标记时既不走 `abortPendingApproval`，也不走 129 的“拒绝并中止”续跑，也不分派批准/拒绝续跑。
3. 新增 `func (s *ChatSession) resolveInlineApproval(ctx context.Context, rae *tool.RequiresActionError) (context.Context, error)`：
   - 持 `s.subagentApprovalMu`（与子代理共用，弹层一次一个）；`sink := s.toolApprovalSinkGet()` 为 nil → `return nil, rae`；`rae.ActionKind == "exit_plan_mode"` 或 `rae.AgentID != ""` → `return nil, rae`。
   - `s.runSvc().SetWaitSessionSnapshot(ctx, rae.RunID, rae.ActionID, rae.SessionSnapshot)`（失败 → `return nil, rae`，交回停回合路径）。
   - `sessionID := tool.ConversationSessionIDFromContext(ctx)`；`gate := s.approvalGate()`，把它的 `RunIDHint` 换成返回 `rae.RunID`；`req, err := gate.PendingForRun(ctx, sessionID, rae.RunID)`；`req == nil` → `return nil, rae`；`req.ToolStepID` 为空时用 `turn.ApprovalToolStepID(rae)`。
   - `g, _ := turn.SubagentApprovalGateFromError(rae)`；`return s.answerInPlaceApproval(ctx, sink, g, *req, "the tool call", firstNonEmpty(req.Description, req.Reason))`。
4. 安装：`syncToolApprovalHooks` 里与 `SetSubagentApprovalHook` 并列 `s.Env.Tools().SetInlineApprovalHook(s.resolveInlineApproval)`（sink 为 nil 时置 nil）；`chat_turn.go` 每轮的上下文级安装处并列 `agBase = tool.WithInlineApprovalHook(agBase, s.resolveInlineApproval)`。
5. 答复后不另起续跑：确认带“当场回答”标记时 `resumeAfterApproval`/`resumeAfterDenial`/`resumeAfterRejectedStop`（129）/提问的 `submitAskUserQuestionApproval` 都不启动续跑；若仍会启动 → STOP 5。
6. 测试（照 `pkg/tui` 里子代理当场审批的现有测试，用假的 `ToolApprovalDecisionSink`）：主代理 gate 当场批准 → 运行未结束、没有第二个运行、事件里有 `approval_requested`（带 `tool_step_id`）与 `approval_resolved`；提问（`user_interaction`）当场回答 → 工具拿到答案；sink 为 nil → 交回停回合。

### Step 7：gateway 实现钩子

1. `approval.go`：新增 `promptGatewayInlineApproval(ctx, rae, requestID, sessionID string, writeMsg func(wsServerMsg)) (context.Context, error)`，结构照 `promptGatewaySubagentApproval`，差别：`exit_plan_mode` 或 `rae.AgentID != ""` → `return nil, rae`；轮询到 action 为 cancelled（用户不带说明拒绝）→ 返回 `(nil, tool.ErrApprovalRejectedStop)`；抢租约之前先 `s.RunRT.SetWaitSessionSnapshot(...)`（失败 → `return nil, rae`）；
   WS 消息的数据照 `publishDetachedGatewayApprovalRequest`（`approvalWSData` + action 字段 + `withApprovalToolStepID(data, rae)`），`Message: "Awaiting approval or authorization to continue"`；另外照 TUI 发布一条 `approval_requested` 运行事件（带 `ToolStepID`），让 web 把审批卡挂到工具卡上（若 `canonicalRunEventsFromWS` 已从 `requires_action` 派生出它，就不要重复发——先读该函数确认）。
2. 安装：`server.go` WS 对话里与 `WithSubagentApprovalHook` 并列 `agCtx = tool.WithInlineApprovalHook(agCtx, func(approvalCtx context.Context, rae *tool.RequiresActionError) (context.Context, error) { return s.promptGatewayInlineApproval(approvalCtx, rae, m.RequestID, sid, writeMsg) })`；`withDetachedGatewayApprovalHooks` 里同样并列（用 `writeDetached`）。
3. 不另起续跑、不另行取消：确认 approve/deny 端点（`resolveGatewayApproval` → 续跑分派）以及取消处理（`abortGatewayRunForAction`，含 129 加的“拒绝并中止”续跑）对“主代理 gate、等待行租约由存活进程持有（`resume_owner` 非空且租约未过期）”的情况既不启动续跑、也不取消运行（交给当场的轮询循环处理）；若仍会 → STOP 5。
4. 测试（照 gateway 里子代理当场审批的现有测试）：WS 对话中主代理 gate → 收到 `requires_action`（带 `tool_step_id`）；通过 approve 端点批准 → 同一运行继续、只有一个运行、工具执行一次；deny + reason → 只有该调用被拒、回合继续；cancel → 该调用 `RejectedStopText`、运行以取消结束、没有续跑运行；超时（`expireGatewayApproval`）→ 该调用得到错误结果、回合继续或按现有过期语义结束（照子代理测试的期望）。

### Step 8：web 回归

不改前端逻辑，只加测试（`useChatStream.test.ts`）：主对话流式中途收到 `requires_action` 与 `approval_requested`（带 `toolStepId`），之后继续收到 `assistant_delta` 与工具完成事件 → 正文只出现一次、审批卡挂在对应工具卡上、流式状态未被结束。若测试失败（例如 `requires_action` 让后续事件被当成镜像丢弃或重复渲染）→ 最小修正该分支并在报告里说明；修正超过 20 行 → STOP 6。

### Step 9：真机验收（TUI，run-forebrain）

1. `fake_provider.py` 加模式 `streamapprove`：第 1 次轮请求先发正文 `checking`，再发一个 `shell` 调用 `{"command": "echo approved-run", "description": "needs approval", "sandbox_permissions": "require_escalated", "justification": "test escalation"}`（index 0），然后**每秒发一个正文分片 `tick N`，共 6 个**，最后结束帧；
   带工具结果的请求回答 `after approval`。`SKILL.md` 模式表加一行。
2. 跑：

```bash
D=.claude/skills/run-forebrain/driver.sh
$D start streamapprove
$D submit 'go'
sleep 2; $D screen      # 期望：审批弹层已出现（命令 echo approved-run），上方正文仍在增长（出现 tick 1/tick 2），状态行仍是 Working
$D key Enter            # 选默认的允许
$D wait 'after approval' 40
$D screen               # 期望：一张 echo approved-run 工具卡（结果 approved-run）、tick 1..6、最后 after approval
$D db 'select status from fb_runs order by created_at desc limit 2'   # 期望：只有本轮一个运行（没有续跑运行），状态 completed
$D stop
$D start streamapprove
$D submit 'go'
sleep 2; $D key n; $D key Enter   # 不带说明拒绝
$D wait 'STOP what you are doing|Cancel|Interrupt' 20
$D screen               # 期望：拒绝卡；tick 不再增长（流已取消）；没有 after approval；回合结束等输入
$D messages             # 期望：该调用的结果为 STOP… 文字；没有后续 assistant 回答
$D stop
```

3. 真模型冒烟（`real-model-acceptance` 规则：主树、隔离 home、glm-5.3、key 从 `.env` 注入，approval_policy on-request）：让模型“用 require_escalated 跑 `echo hi`，然后告诉我结果”，当场批准 → 回合在同一运行内完成；附 `screen`。

**Verify**：两段期望满足，输出附在报告里。

## Done criteria

- [ ] Step 1–8 的测试全部通过；Go 测试命令全部 `ok`；race 无 `DATA RACE`；前端单测与 `pnpm typecheck` 通过
- [ ] `grep -n "WithInlineApprovalHook" pkg/tui/chat_turn.go pkg/gateway/server.go pkg/gateway/approval.go` 各有结果
- [ ] 真机 `streamapprove` 与真模型冒烟通过
- [ ] `git status --short` 只含 Scope 内文件

## STOP conditions

1. 前置不满足或锚点找不到（含 `actionHook` 拿不到 `*tool.State`）。
2. 某个工具在调用 action hook **之前**就产生了副作用——批准后重新执行会让它做两次；回报工具名与位置。
3. 现有的审批续跑测试因“主代理 gate 不再停回合”而失败，且该测试没有装当场钩子——说明钩子被意外装上了；回报。装了钩子的现有测试按新语义失败的，回报用例名，不要改续跑逻辑去迁就。
4. 编排层能拿到的运行 ID 与 `actionHook` 的 `policyRunIDForTool` 不是同一个（槽位会失效）——回报两处的取法。
5. 当场答复后，TUI 或 gateway 的续跑分派仍会为同一运行启动第二次执行。
6. web 的修正超过 20 行。
7. 某一步验证连续两次修正后仍失败。

## Maintenance notes

- 主代理审批现在有两条路：有当场钩子 → 原处等答复；没有（或钩子交回）→ 停回合续跑。新增会问人的工具必须经 `actionHook` 或像提问工具一样取槽与判断推迟，否则会绕过“每运行一次一个”的约束。
- 当场等待期间等待行的快照必须是完整的（含已完成调用的结果），否则崩溃后的续跑会重复执行已完成的调用。
- 拒绝语义与 Claude Code 一致（`129`）：带说明只拒这一个调用、回合继续；不带说明写 `RejectedStopText` 并中止回合。当场回答与停回合续跑两条路必须给出相同的结果与卡片。
