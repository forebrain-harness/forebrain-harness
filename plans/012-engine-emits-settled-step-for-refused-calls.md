# Plan 012：用户拒绝的调用由运行时发出一条 settled "denied" 步骤（与落库的拒绝行同源），web live 不再缺 "Kept planning" 卡

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **工作目录**：`/Users/doudou/workspace/unionj-cloud/forebrain-harness-008`（owner 指定的 worktree，
> detached HEAD `9656051`，工作区里已有 plan 008、plan 011 的**未提交**改动——那是本计划的基础，不要还原、不要整理）。
> 下文所有相对路径都相对这个目录。
>
> **Drift check（先跑）**：
> ```bash
> cd /Users/doudou/workspace/unionj-cloud/forebrain-harness-008
> git diff --stat 9656051 -- pkg/tool pkg/run pkg/tui        # 预期：无输出
> grep -c 'func ApprovalToolStepID' pkg/turn/approval.go     # 预期：1（plan 011 已在工作区）
> ```
> 第一条有输出 → STOP；第二条不是 1 → STOP（011 还没执行）。然后对照 "Current state" 摘录逐段核对；不一致即 STOP。

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED（运行时多发一类步骤事件，经过所有表面；事件 ID 必须与"等待审批"那一帧区分，否则会被幂等日志吞掉——Step 1 专门处理）
- **Depends on**: `plans/011-gateway-approval-request-names-gated-call.md`（同一 worktree 顺序执行；代码上不依赖，但两者都改 `pkg/gateway/approval_test.go` 与 `frontend/src/composables/useChatStream.test.ts`）
- **Category**: bug
- **Planned at**: commit `9656051` + 008/011 工作区改动，2026-10-10（主树 HEAD `005c1a8` 的 `pkg/tool`、`pkg/run`、`pkg/tui/chat_turn.go`、`pkg/tui/run_test.go` 与 `9656051` 相同）

## Why this matters

用户在审批时选"拒绝"（可带反馈）后，运行时**不执行**那个调用，而是往会话里注入一条"拒绝结果"让模型继续。这条结果落库时带
显示用的半边（`tool_display`：状态 `denied`、正文是用户的原话），所以**重载/回放**能画出那张 settled 卡（TUI 叫
"◆ Kept planning └ 反馈"）。但注入时**不发任何步骤事件**：

- TUI live 能看到，是因为 TUI 在用户作答时**自己本地合成**了这张卡（`pkg/tui/chat_turn.go` 的 `notifyToolApprovalDenied`），
  不经事件日志；
- web（不论观看 TUI 会话还是 web 自己的会话）live **收不到**这张卡，重载才出现——live 与重载不一致；plan 008 的推演表写着
  "用户拒绝（带反馈）| … → gate → tool(Kept planning)"、live 与重载相同，实际 live 少这张卡；
- 非 exit 的调用（如 shell）被拒后，web live 那张卡一直停在 "awaiting approval"，重载才变成 denied。

探针实证（008 工作区前端，喂入真实的 TUI 事件形态）：live `['assistant','approval','gate','assistant']`，
重载 `['assistant','approval','gate','tool(denied)','assistant']`。再喂一条本计划将要发出的 denied 步骤后，live 与重载
完全一致（块序相同，卡的 `stepId/status/output/summary/toolName` 相同）——**前端不需要改代码**。

owner 拍板：**由运行时在注入拒绝结果的地方发出步骤事件**（而不是 web 自己补画）。为保证"live 画的 = 回放画的"，
落库的那半边显示改为**从同一个步骤事件推导**（与真正执行过的工具完全相同的做法：`toolDisplayState(completedEvent)`）。

**一个必须先处理的坑**：规范事件的 ID 是 `tool:<run>:<step>:<kind><boundary>`（`pkg/tool/notification.go:122-131`），
拒绝时没有 approved action id、也没有 attempt，所以 denied 完成事件的 ID 会和这个调用之前那帧"等待审批"完成事件
**一模一样**（真实数据：`tool:655c5559-…:call_51c6ce36…:tool_call_completed`），而会话事件日志按 ID 幂等
（`pkg/state/session_events.go:110-114`：`ON CONFLICT(session_id, event_id) DO NOTHING`）——denied 事件会被**静默丢弃**。
Step 1 给它加一个 `:denied` 边界。

## Current state

### 注入拒绝结果的地方（要改）

`pkg/run/orchestration_llm.go:108-146`：

```go
	resumeAssistant, ok := w.consumeResumeSnapshot(ctx, messages)
	if ok {
		session = resumeAssistant.session
		if resumeAssistant.denied {
			// User denied the tool call — inject denial results without
			// re-executing. Keep already-completed tool results in context so
			// approval resume does not replay or drop earlier tool calls.
			for _, tc := range resumeAssistant.pendingMsg.ToolCalls {
				if msg, ok := resumeAssistant.completedResults[tc.ID]; ok {
					session = append(session, msg)
					continue
				}
				denial := llm.ToolResultMessage(tc.ID, llm.Text(
					buildDenialMessage(tc.Function.Name, resumeAssistant.denyReason),
				))
				// The refusal has two halves: the instruction text above is
				// what the model reads, and this display body is what the
				// card says — live, and on every replay of the row this
				// result persists to. Without it a replay printed the
				// model-facing text (and, for a denied plan, the reviews it
				// carried) as if it were the user's words.
				denialMeta, _ := json.Marshal(map[string]string{
					"tool_name": strings.TrimSpace(tc.Function.Name),
					"status":    "denied",
				})
				// A delivered review closed this gate: the card's body is the
				// delivery drop key, never "(no output)". The key is replay
				// plumbing, not user-facing words — every surface's replay
				// projection recognizes it and drops the row whole.
				denialBody := tool.DeniedToolDisplayBody(tc.Function.Name, resumeAssistant.denyFeedback)
				if resumeAssistant.deliveredReview {
					denialBody = tool.PlanReviewDeliveredDisplayKey
				}
				denial.ToolDisplay = &llm.ToolDisplayState{
					Body:         denialBody,
					ToolMetaJSON: string(denialMeta),
				}
				session = append(session, denial)
			}
		} else if rerr := w.replayPendingToolCalls(...)
```

执行过的工具怎么发步骤、怎么得到落库显示（照抄这个做法）：`executeOneToolCall`（`:1125` 起）用
`tool.StepHookFromContext(ctx, w.state)` 取钩子并 `step(ctx, completedEvent)`；结果的显示是
`display: toolDisplayState(completedEvent)`（`:1256`），而 `toolDisplayState`（`:1334-1345`）是：

```go
func toolDisplayState(evt tool.StepEvent) *llm.ToolDisplayState {
	body, _ := tool.FormatToolStepResult(evt, tool.DefaultMaxFormattedBody)
	metaJSON := ""
	if encoded, err := json.Marshal(tool.BuildToolMeta(evt)); err == nil {
		metaJSON = string(encoded)
	}
	return &llm.ToolDisplayState{
		Body:         strings.TrimSpace(body),
		Summary:      strings.TrimSpace(tool.SummarizeToolStep(evt)),
		ToolMetaJSON: metaJSON,
	}
}
```

`toolStepID(call)`（`:1455`）返回 `call.ID`；`toolInputMap(arguments string) map[string]any`（`:1444`）把参数解析成 map。

### `pkg/tool`（要改）

- `StepEvent`（`pkg/tool/state.go:159-202`），最后一个字段是 `Origin string \`json:"origin,omitempty"\``。
- `DeniedToolDisplayBody`（`pkg/tool/format.go:32-49`）：exit_plan_mode → 反馈原话或 `"(no output)"`；
  request_permissions → `"The user did not approve the requested permissions"`；其他 → `""`。
- `FormatToolStepResult`（`pkg/tool/format.go:55`）开头：

```go
	kind := strings.TrimSpace(evt.Kind)
	if kind == StepKindToolStarted || kind == StepKindToolParallelStarted {
		return "", false
	}
	if stepRequiresApproval(evt) {
		return "", false
	}
```

- `SummarizeToolStep`（`pkg/tool/format.go:245`）开头：toolName 为空返回 `""`；Error 非空返回 `"failed: …"`；之后对完成态返回 `"ran " + invocation` 等。
- `stepStatusLabel`（`pkg/tool/format.go:2282`）完成态分支：

```go
	case StepKindToolCompleted, StepKindToolParallelCompleted:
		if stepRequiresApproval(evt) {
			return "awaiting approval"
		}
		...
		return "completed"
```

- `toolStepBoundary`（`pkg/tool/notification.go:122-131`）：

```go
func toolStepBoundary(ctx context.Context, evt StepEvent) string {
	boundary := ""
	if approved := strings.TrimSpace(ApprovedActionIDFromContext(ctx)); approved != "" {
		boundary += ":approved-" + approved
	}
	if evt.Attempt > 0 {
		boundary += ":attempt-" + strconv.Itoa(evt.Attempt)
	}
	return boundary
}
```

`RunEventFromStep`（`pkg/tool/notification.go:19`）是 TUI 与 gateway 把步骤变成规范事件的**唯一**入口：状态取
`BuildToolMeta(evt).Status`，正文取 `FormatToolStepResult`，`Summary` 取 `SummarizeToolStep`，ID 见上。gateway 的 socket
路径（`pkg/gateway/server.go:1855-1880`）也用 `BuildToolMeta` / `SummarizeToolStep` / `BuildRenderedToolStepNotification`。

### TUI（要改一处）

`pkg/tui/chat_turn.go:277-293`（`runAuditStepHook` 的主 agent 分支）：

```go
			case event.RunEventToolStarted, event.RunEventToolOutputDelta, event.RunEventToolCompleted:
				if strings.TrimSpace(evt.ToolName) == "" || !shouldNotifyToolStep(evt) || tool.ToolStepRendersAsPlan(evt) {
					break
				}
				canonical, canonicalOK := tool.RunEventFromStep(stepCtx, sid, rid, "tui", evt)
				// An explicit skill step ...
				if strings.EqualFold(strings.TrimSpace(evt.Category), "skill") && canonicalOK && s.runner().Events != nil {
					_ = s.runner().Events.Publish(stepCtx, canonical)
					return
				}
				if canonicalOK {
					s.persistRunEvent(stepCtx, canonical)
				}
				s.notifyToolStepHooks(stepCtx, sid, rid, "tui", evt)
			}
```

TUI 在用户作答时已经自己画了拒绝卡（`notifyToolApprovalDenied`，`pkg/tui/chat_turn.go:1026` 起：嵌套 request_permissions
时状态写 `canceled`，其余 `denied`，正文 `DeniedToolDisplayBody`，**不带 Summary**）。如果再让引擎事件进 TUI 的 UI，
`notifyToolStepHooks` 用的 `toolStepSummary`（`pkg/tui/notify.go:591`）在 `SummarizeToolStep` 为空时会退回 `"ok"`/`"done"`
之类的字样，卡片会多出这些字——所以**主 agent 分支只落库、不画**。subagent 分支（`:316` 起）发布规范事件，由 TUI 的事件订阅画进
subagent 视图（那里今天没有任何拒绝卡，等待卡会被同 StepID 的 denied 卡原地替换——`pkg/tui/tty_session.go:1207`
`replaceOrAppendBlock` 按 StepID 替换），**不改**。

TUI 回放主 agent 的工具卡来自 transcript 行，主 agent 的工具事件在回放中被跳过（`pkg/tui/commands.go:1118`
`replayToolEventSkipped`），所以多出的事件不会在 TUI 回放里重复画。

### 约定与样板

- Go 注释写"为什么"、英文完整句子（看上面摘录的风格）。
- `pkg/run` 测试样板：`pkg/run/orchestration_llm_test.go:4502` `TestDeniedToolResultCarriesItsDisplay`（造一个会抛
  `RequiresActionError` 的 exit_plan_mode 工具 → 第一次 Execute 得到 RAE → 带 `ToolApprovalResumeState{Denied: true, DenyFeedback: …}`
  再 Execute → 在 `inner.sessions[1]` 里找拒绝结果）；delivered 样板：`:4587` `TestDeliveredReviewDenialCarriesTheHandoffLine`
  （`DeliveredReview: true`）。记录步骤用 `st.SetStepHook(func(_ context.Context, evt toolpkg.StepEvent) {...})`（见 `:703`）。
- `pkg/tui` 测试样板：`pkg/tui/run_test.go:297` `TestRunAuditStepHookEmitsPrimaryToolMessagesDirectly`。
- `pkg/gateway` 测试样板：`pkg/gateway/approval_test.go:35` `TestDetachedApprovalResumePublishesToolAndNestedApprovalHistory`
  （`s.withDetachedGatewayApprovalHooks` 取钩子、喂步骤、`runs.ListSessionEvents` 读回）。
- `pkg/tool` 测试放 `pkg/tool/format_test.go`（已有 `TestFormatToolStepResult_*` 等）。

## Commands you will need

| 用途 | 命令（在 worktree 根目录） | 成功预期 |
|---|---|---|
| Go 单包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/... -count=1` | `ok` |
| | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run/... -count=1` | `ok` |
| | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/... -count=1` | `ok`（`TestCharacterizationNetworkApproval` 会真实访问网络、偶发失败；单跑通过即可，报告里注明） |
| | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/architecture/... -count=1` | `ok` |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tool/... ./pkg/run/... ./pkg/tui/... ./pkg/gateway/...` | 无输出 |
| gofmt | `gofmt -l pkg/tool pkg/run pkg/tui pkg/gateway` | 无输出 |
| 前端 | `cd frontend && npx vitest run src/composables/useChatStream.test.ts` / `pnpm test` / `npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 全绿 / 无输出 |
| 真机 | `.claude/skills/run-forebrain/driver.sh`（见 Step 7） | 见 Step 7 |

## Scope

**In scope**：
- `pkg/tool/state.go`（`StepEvent` 加两个字段）
- `pkg/tool/format.go`（`DeniedToolStep` 构造函数；`stepStatusLabel` / `FormatToolStepResult` / `SummarizeToolStep` 认 `Denied`）
- `pkg/tool/notification.go`（`toolStepBoundary` 加 `:denied`）
- `pkg/tool/format_test.go`（新增测试）
- `pkg/run/orchestration_llm.go`（只改上面摘录的拒绝注入循环）
- `pkg/run/orchestration_llm_test.go`（新增测试）
- `pkg/tui/chat_turn.go`（只在 `runAuditStepHook` 主 agent 分支加一个提前返回）
- `pkg/tui/run_test.go`（新增测试）
- `pkg/gateway/approval_test.go`（新增测试，不改 gateway 源码）
- `frontend/src/composables/useChatStream.test.ts`（新增一个契约用例，不改前端源码）

**Out of scope**：
- `pkg/tui/chat_turn.go` 的 `notifyToolApprovalDenied`（TUI 本地画卡保持不变，包括嵌套 request_permissions 显示 `canceled` 的措辞）。
- `pkg/tui/notify.go`（`toolStepSummary` 的回退字样）、`pkg/tui/commands.go`（回放）、`pkg/tui/chat_session.go`（主树已被 007 改动）。
- `pkg/gateway/**` 源码（detached 钩子与 socket 钩子都走 `pkg/tool` 的函数，自动得到新行为）。
- `frontend/src/**` 源码。
- review 交付关闭的 gate（`deliveredReview`）：**行为与落库内容保持逐字节不变**，也不发步骤。

## Git workflow

不建分支、不 commit、不 push、不 stash（owner 手动提交）。改动留在 worktree 工作区。

## Steps

### Step 1：`pkg/tool`——一个"被拒绝的调用"步骤，以及它的状态、正文、标签、事件 ID

1. `pkg/tool/state.go` 的 `StepEvent` 末尾（`Origin` 之后）加：

```go
	// Denied marks the completion of a call the user refused. The call never
	// ran: the runtime answered it with a denial result, and this event is
	// that answer's card - status "denied", body DeniedToolDisplayBody - so a
	// surface that only sees events draws exactly what the persisted row
	// replays.
	Denied bool `json:"denied,omitempty"`
	// DenyFeedback is the user's own words from that refusal, if any.
	DenyFeedback string `json:"deny_feedback,omitempty"`
```

2. `pkg/tool/format.go`，在 `DeniedToolDisplayBody` 之后加：

```go
// DeniedToolStep is the completed step of a call the user refused (see
// StepEvent.Denied). The runtime emits it where it injects the denial result
// and derives that result's persisted display from it, so the card a live
// surface draws and the card every replay of the row draws are one card.
func DeniedToolStep(stepID, toolName string, input map[string]any, feedback string) StepEvent {
	return StepEvent{
		Kind:         StepKindToolCompleted,
		StepID:       strings.TrimSpace(stepID),
		ToolName:     strings.TrimSpace(toolName),
		Input:        input,
		Denied:       true,
		DenyFeedback: strings.TrimSpace(feedback),
	}
}
```

3. `stepStatusLabel` 完成态分支的**第一行**加 `if evt.Denied { return "denied" }`（在 `stepRequiresApproval` 判断之前）。
4. `FormatToolStepResult`：在"started 返回空"之后、`stepRequiresApproval` 之前加：

```go
	// A refused call never ran; its body is the refusal's own sentence.
	if evt.Denied {
		return DeniedToolDisplayBody(toolName, evt.DenyFeedback), false
	}
```

5. `SummarizeToolStep`：在 `toolName == ""` 返回之后、`subagentStepSummary` 之前加：

```go
	// A refused call never ran, so "ran ..." would claim it did. It carries
	// no summary of its own - its status and body say what happened - and
	// surfaces label it by its invocation, exactly as they label the
	// persisted denial row.
	if evt.Denied {
		return ""
	}
```

6. `pkg/tool/notification.go` 的 `toolStepBoundary`，在 attempt 判断之后加：

```go
	// A refused call's answer is a completion of its own. Without its own
	// boundary it would share the id of the completion that reported the
	// call waiting for approval, and the idempotent event log would keep
	// that one and drop the refusal.
	if evt.Denied {
		boundary += ":denied"
	}
```

7. `pkg/tool/format_test.go` 新增 `TestDeniedToolStepReadsAsTheRefusalEverywhere`（表驱动）：

| case | toolName | feedback | 期望正文 |
|---|---|---|---|
| exit 带反馈 | `exit_plan_mode` | `split milestones` | `split milestones` |
| exit 无反馈 | `exit_plan_mode` | `` | `(no output)` |
| request_permissions | `request_permissions` | `` | `The user did not approve the requested permissions` |
| shell | `shell` | `no` | `` |

每个 case：`evt := DeniedToolStep("call-1", toolName, map[string]any{}, feedback)`；断言
`BuildToolMeta(evt).Status == "denied"`；`FormatToolStepResult(evt, DefaultMaxFormattedBody)` 的正文等于期望；
`SummarizeToolStep(evt) == ""`；`RunEventFromStep(context.Background(), "s1", "r1", "tui", evt)` 返回 ok，
`ID == "tool:r1:call-1:tool_call_completed:denied"`，payload 反序列化为 `event.ToolCallCompletedPayload` 后
`ToolMeta.Status == "denied"`、`DisplayBody` 等于期望、`Summary == ""`。
再加一个断言：同一 StepID 的"等待审批"完成帧
`StepEvent{Kind: StepKindToolCompleted, StepID: "call-1", ToolName: "exit_plan_mode", ActionID: "act-1", ActionKind: "exit_plan_mode", Output: map[string]any{"requires_action": true}}`
经 `RunEventFromStep` 的 ID 是 `"tool:r1:call-1:tool_call_completed"`，与 denied 的 ID **不同**。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/... -count=1 -run TestDeniedToolStepReadsAsTheRefusalEverywhere -v` → PASS；
`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/... -count=1` → `ok`。

### Step 2：运行时在注入拒绝结果的地方发出这一步，并从它推导落库显示

改 `pkg/run/orchestration_llm.go` 的拒绝注入循环（摘录见 Current state）。目标形状：

```go
			for _, tc := range resumeAssistant.pendingMsg.ToolCalls {
				if msg, ok := resumeAssistant.completedResults[tc.ID]; ok {
					session = append(session, msg)
					continue
				}
				denial := llm.ToolResultMessage(tc.ID, llm.Text(
					buildDenialMessage(tc.Function.Name, resumeAssistant.denyReason),
				))
				if resumeAssistant.deliveredReview {
					// <保留原注释：A delivered review closed this gate ... drops the row whole.>
					// No surface draws it, so no step announces it either.
					denialMeta, _ := json.Marshal(map[string]string{
						"tool_name": strings.TrimSpace(tc.Function.Name),
						"status":    "denied",
					})
					denial.ToolDisplay = &llm.ToolDisplayState{
						Body:         tool.PlanReviewDeliveredDisplayKey,
						ToolMetaJSON: string(denialMeta),
					}
					session = append(session, denial)
					continue
				}
				// The refusal has two halves: the instruction text above is
				// what the model reads, and the refused call's own step is what
				// the card says. The step is announced like any completed call's,
				// and the row it persists to carries the display derived from
				// that same step, so the card a live surface draws and the card
				// every replay of the row draws are one card. Without it a
				// replay printed the model-facing text as if it were the user's
				// words, and a live surface that only sees events drew nothing.
				refused := tool.DeniedToolStep(toolStepID(tc), tc.Function.Name, toolInputMap(tc.Function.Arguments), resumeAssistant.denyFeedback)
				denial.ToolDisplay = toolDisplayState(refused)
				if step := tool.StepHookFromContext(ctx, w.state); step != nil {
					step(ctx, refused)
				}
				session = append(session, denial)
			}
```

要求：
- delivered 分支的落库内容与改动前**逐字节相同**（Body 为 `PlanReviewDeliveredDisplayKey`，meta 只有 `tool_name` / `status`），不发步骤。
- 非 delivered 分支的 `Body` 必须与改动前相同（`DeniedToolDisplayBody(name, denyFeedback)`——Step 1 的 `FormatToolStepResult` 保证）。
- `ctx` 用这个函数里已有的 ctx（同一个 ctx 后面传给 `tool.EndApprovalContinuation(ctx)`）。

新增测试（`pkg/run/orchestration_llm_test.go`，紧跟 `TestDeliveredReviewDenialCarriesTheHandoffLine` 之后）：

1. `TestDeniedToolCallAnnouncesItsSettledStep`：照 `TestDeniedToolResultCarriesItsDisplay` 搭建，额外
   `var mu sync.Mutex; var steps []toolpkg.StepEvent; st.SetStepHook(func(_ context.Context, evt toolpkg.StepEvent) { mu.Lock(); steps = append(steps, evt); mu.Unlock() })`。
   resume 后：
   - `Denied == true` 的步骤**恰好 1 条**，`StepID == "call-1"`、`ToolName == "exit_plan_mode"`、`Kind == toolpkg.StepKindToolCompleted`、`DenyFeedback == "改成先写测试"`；
   - 拒绝结果的 `ToolDisplay` 与 `toolDisplayState(那条步骤)` 的 `Body`、`Summary`、`ToolMetaJSON` 三个字段分别相等；
   - `ToolDisplay.Body == toolpkg.DeniedToolDisplayBody("exit_plan_mode", "改成先写测试")`（与改动前一致）。
2. `TestDeliveredReviewDenialAnnouncesNoStep`：照 `TestDeliveredReviewDenialCarriesTheHandoffLine`（`DeliveredReview: true`）搭建并记录步骤；
   resume 后 `Denied == true` 的步骤 **0 条**；拒绝结果 `ToolDisplay.Body == toolpkg.PlanReviewDeliveredDisplayKey`，
   `ToolDisplay.Summary == ""`，`ToolMetaJSON` 反序列化后只有 `tool_name` 与 `status` 两个键。

**Verify**：
`CGO_ENABLED=1 go test -tags fts5 ./pkg/run/... -count=1 -run 'TestDenied|TestDelivered' -v` → 新旧用例全部 PASS（含未改动的
`TestDeniedToolResultCarriesItsDisplay`、`TestDeliveredReviewDenialCarriesTheHandoffLine`）；
`CGO_ENABLED=1 go test -tags fts5 ./pkg/run/... -count=1` → `ok`。

### Step 3：TUI 主 agent 分支——落库，不画

`pkg/tui/chat_turn.go` 的 `runAuditStepHook` 主 agent 分支（摘录见 Current state），把

```go
				if canonicalOK {
					s.persistRunEvent(stepCtx, canonical)
				}
				s.notifyToolStepHooks(stepCtx, sid, rid, "tui", evt)
```

改为

```go
				if canonicalOK {
					s.persistRunEvent(stepCtx, canonical)
				}
				// The terminal drew this refusal when the user answered it
				// (notifyToolApprovalDenied), in words only it knows - a nested
				// gate reads as canceled. The runtime's account of the refusal is
				// recorded for the other surfaces and for resume, and drawn by
				// nothing here.
				if evt.Denied {
					return
				}
				s.notifyToolStepHooks(stepCtx, sid, rid, "tui", evt)
```

新增测试 `pkg/tui/run_test.go`：`TestRunAuditStepHookRecordsARefusalWithoutDrawingIt`，照 `TestRunAuditStepHookEmitsPrimaryToolMessagesDirectly`（`:297`）搭建
（同样的 DB / runSvc / runner / session / `installRunAuditStepHook("sid")`），UI 回调把 `MsgKindTool` 消息追加到一个加锁的切片。依次调用 hook：
1. "等待审批"帧：`tool.StepEvent{Kind: event.RunEventToolCompleted, StepID: "call-exit-1", ToolName: "exit_plan_mode", ActionID: "act-1", ActionKind: "exit_plan_mode", Output: map[string]any{"requires_action": true}}`；
2. 拒绝帧：`tool.DeniedToolStep("call-exit-1", "exit_plan_mode", map[string]any{}, "split milestones")`；
3. 哨兵帧：一条普通 `read_file` 完成帧（照样板的 `step`，StepID `call-sentinel`）。

用 `require.Eventually` 等到切片里出现 StepID 为 `call-sentinel` 的消息（UI 分发是 FIFO 的：拒绝帧若产生了 UI 消息，一定排在哨兵之前）。断言：
- 切片里**没有** `ToolMeta.Status == "denied"` 的消息、也没有 Content 为 `split milestones` 的消息；
- `runSvc.ListSessionEvents(ctx, "sid", 0, 0, 20)` 里 `tool_call_completed` 且 StepID 为 `call-exit-1` 的事件有 **2 条**：一条 ID 以
  `:tool_call_completed` 结尾（等待），一条 ID 以 `:tool_call_completed:denied` 结尾，后者的 payload（`event.ToolCallCompletedPayload`）
  `ToolMeta.Status == "denied"`、`DisplayBody == "split milestones"`。

（若 `SessionEventPage` 的字段名与此不同，以 `pkg/state/session_events.go` 为准；样板用法见 `pkg/gateway/approval_test.go:63`。）

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/... -count=1 -run 'TestRunAuditStepHook' -v` → 全部 PASS。

### Step 4：gateway 侧契约测试（不改 gateway 源码）

`pkg/gateway/approval_test.go` 新增 `TestDetachedResumePublishesARefusedCall`：照 `TestDetachedApprovalResumePublishesToolAndNestedApprovalHistory`（`:35`）
取 `stepHook := tool.StepHookFromContext(runCtx, nil)`，先喂上面的"等待审批"帧（StepID `call-exit-1`），再喂
`tool.DeniedToolStep("call-exit-1", "exit_plan_mode", map[string]any{}, "split milestones")`；`runs.ListSessionEvents` 读回后断言：
有两条 `tool_call_completed`（ID 不同），其中 denied 那条 `ToolMeta.Status == "denied"`、`DisplayBody == "split milestones"`。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... -count=1 -run TestDetachedResumePublishesARefusedCall -v` → PASS。

### Step 5：web 契约用例（不改前端源码）

`frontend/src/composables/useChatStream.test.ts`，在 `describe('subagent_* call cards', …)` 里（plan 011 新增用例之后）新增
`it('a refused gate draws its settled card live where the reload draws it', …)`。用该 describe 的 `liveStream()`：

```ts
runEvent('assistant_delta', { text: 'here is the plan' })
runEvent('tool_call_started', { tool_name: 'exit_plan_mode', step_id: 'call-a', summary: 'running exit_plan_mode', tool_meta: { tool_name: 'exit_plan_mode', status: 'running', invocation: 'exit_plan_mode' } })
runEvent('approval_requested', { action_id: 'act-a', action_kind: 'exit_plan_mode', tool_step_id: 'call-a' })
runEvent('approval_resolved', { action_id: 'act-a', action_kind: 'exit_plan_mode', decision: 'denied', reason: 'split milestones', tool_step_id: 'call-a', confirmation: '✗ You did not approve forebrain to exit plan mode' })
// The runtime's own account of the refusal (plan 012): a completed step, status denied, no summary.
runEvent('tool_call_completed', { tool_name: 'exit_plan_mode', step_id: 'call-a', display_body: 'split milestones', tool_meta: { tool_name: 'exit_plan_mode', status: 'denied', invocation: 'exit_plan_mode' } })
runEvent('assistant_delta', { text: 'ok, splitting' })
```

重载：transcript 为 user → assistant `here is the plan` + 调用 `call-a`（`assistantParts` 形状同 008 的用例）→ tool 行 `call-a`：
`partsJson` 为 `[{type:'tool_result_meta', tool_call_id:'call-a'}, {type:'tool_display', body:'split milestones', summary:'', tool_meta_json: JSON.stringify({tool_name:'exit_plan_mode', status:'denied', invocation:'exit_plan_mode'})}]`
→ assistant `ok, splitting`（`runId:'run-1'`）；事件打桩为 approval_requested、approval_resolved（camelCase，同上内容）以及那条
`tool_call_completed`（camelCase：`toolName/stepId/displayBody/toolMeta`——重载时它被当作 transcript 已承载的事件跳过）。

断言：live 与重载该轮块序都等于 `['assistant', 'approval', 'gate', 'tool', 'assistant']`；两边 `tool` 块的
`{stepId, status, output, summary, toolName}` 相等，且等于 `{stepId:'call-a', status:'denied', output:'split milestones', summary:'exit_plan_mode', toolName:'exit_plan_mode'}`。
用例 doc 注释写明：这是 web 对运行时事件形态的契约；真正的修复在运行时（Step 2）。

**Verify**：`cd frontend && npx vitest run src/composables/useChatStream.test.ts -t 'a refused gate draws its settled card'` → 1 passed。

### Step 6：全量

```bash
cd /Users/doudou/workspace/unionj-cloud/forebrain-harness-008
CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/... ./pkg/run/... ./pkg/gateway/... ./pkg/turn/... ./pkg/event/... ./pkg/architecture/... -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/... -count=1
CGO_ENABLED=1 go vet -tags fts5 ./pkg/tool/... ./pkg/run/... ./pkg/tui/... ./pkg/gateway/...
gofmt -l pkg/tool pkg/run pkg/tui pkg/gateway
cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json
```

**Verify**：全部 `ok` / 全绿；vet、gofmt、vue-tsc 无输出（`pkg/tui` 若仅 `TestCharacterizationNetworkApproval` 因网络失败，单跑
`-run TestCharacterizationNetworkApproval` 通过即可，报告里写明）。

### Step 7：真机（driver，fake provider，独立 WORK/SESSION/PORT）

```bash
cd /Users/doudou/workspace/unionj-cloud/forebrain-harness-008
export FOREBRAIN_RUN_WORK=$TMPDIR/fb-012 FOREBRAIN_RUN_SESSION=fb-012 FOREBRAIN_RUN_PORT=8812
D=.claude/skills/run-forebrain/driver.sh
$D build
$D reset
$D start toolcall; $D submit 'plan something'; $D wait 'Yes, proceed' 25; $D key Enter     # 批准 enter_plan_mode
$D wait 'Ask another model' 25                                                                # exit_plan_mode 的审批 overlay
$D key Down Down; $D key Enter; $D send '先拆分里程碑'; $D key Enter; sleep 3; $D screen        # 选"拒绝"并给反馈
```

断言：
- `$D screen | grep -c 'Kept planning'` = **1**（没有重复卡）；
- `$D db "select count(*) from fb_session_events where event_type='tool_call_completed' and event_id like '%:denied'"` = **1**；
- `$D db "select payload_json from fb_session_events where event_id like '%:denied'"` 的 JSON 里 `tool_meta.status` 为 `denied`、`display_body` 为 `先拆分里程碑`；
- 回放：`SID=$($D sid); $D stop; RESUME_ID=$SID $D start reply; sleep 3; $D screen | grep -c 'Kept planning'` = **1**。

完成后 `$D stop; rm -rf $TMPDIR/fb-012`。driver 的按键序列若与上面不符（overlay 选项顺序不同），以屏幕上 "No" / 反馈输入框的实际位置为准调整按键，并在报告里写明实际序列。

## Test plan

- `pkg/tool/format_test.go`：`TestDeniedToolStepReadsAsTheRefusalEverywhere`——四种工具的状态/正文/空 summary、规范事件的字段，以及 denied 与"等待审批"完成帧 ID 不同。
- `pkg/run/orchestration_llm_test.go`：`TestDeniedToolCallAnnouncesItsSettledStep`（发且只发一条、落库显示与步骤同源）、`TestDeliveredReviewDenialAnnouncesNoStep`（交付关闭的 gate 不发、落库不变）。
- `pkg/tui/run_test.go`：`TestRunAuditStepHookRecordsARefusalWithoutDrawingIt`（TUI 只落库不画；两条完成事件都在日志里）。
- `pkg/gateway/approval_test.go`：`TestDetachedResumePublishesARefusedCall`（detached 路径发布、不被等待帧吞掉）。
- `frontend/src/composables/useChatStream.test.ts`：live 与重载画同一张 denied 卡（契约用例）。
- 真机：Step 7。

## Done criteria

- [ ] Step 6 的命令全部通过（Go `ok`、前端全绿；vet/gofmt/vue-tsc 无输出）
- [ ] 上述 6 个新测试存在且 PASS
- [ ] `grep -n 'boundary += ":denied"' pkg/tool/notification.go` 有 1 行
- [ ] `grep -n 'tool.DeniedToolStep(' pkg/run/orchestration_llm.go` 有 1 行；`grep -n 'if evt.Denied {' pkg/tui/chat_turn.go` 有 1 行
- [ ] `git diff 9656051 -- pkg/run/orchestration_llm.go` 中 delivered 分支落库的 Body/meta 与原来相同（评审核对）
- [ ] Step 7 四条断言全部满足，输出摘要写进报告
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件（008/011 已有改动与 `pkg/gateway/dist` 除外）

## STOP conditions

1. Drift check 不符。
2. 既有的 `TestDeniedToolResultCarriesItsDisplay` 或 `TestDeliveredReviewDenialCarriesTheHandoffLine` 失败——说明落库内容变了，停下报告差异。
3. 任何既有测试因为"多了一条完成事件 / 多了一条 UI 消息"而失败（例如按条数断言事件或消息）——停下报告是哪条、断言了什么，不要改既有断言。
4. `RunEventFromStep` 对 denied 步骤返回 `ok == false`，或 `ToolStepUserVisible` 为 false。
5. 真机里 `Kept planning` 出现 0 次或 2 次，或 DB 里 denied 事件为 0 条。
6. 需要改 Out of scope 里的文件才能完成。

## Maintenance notes

- **事件 ID 的 `:denied` 边界不能删**：它是拒绝完成事件与"等待审批"完成事件区分开的唯一依据；删掉后拒绝事件会被幂等日志静默丢弃，web live 又会缺卡（shell 卡会被打回 awaiting approval）。
- TUI 仍在用户作答时自己画拒绝卡（`notifyToolApprovalDenied`），运行时的拒绝事件在 TUI 主视图**只落库不画**。若将来改成 TUI 也从事件画，
  必须先让 `pkg/tui/notify.go` 的 `toolStepSummary` 认识 `Denied`（否则 `SummarizeToolStep` 为空时会退回 `"ok"`/`"done"` 字样），并决定嵌套
  request_permissions 时 `canceled` 与落库 `denied` 的分歧。
- 跨表面：web 上对 TUI 托管 run 作出的拒绝，TUI 今天就不画拒绝卡（既有行为，本计划不变）。
- 新落库的拒绝行的 `tool_meta_json` 现在是完整的 `BuildToolMeta`（含 `purpose`/`invocation`/`input`），与执行过的工具一致；旧行仍只有
  `tool_name`/`status`。web 重载对新行的卡片标题会是调用的 invocation（与 live 相同），对旧行是工具名。
- 外部通知钩子（gateway 的 `NotificationHook`、TUI 渠道通知）现在也会收到拒绝完成事件。
- 评审重点：delivered 分支逐字节不变；拒绝事件与落库显示同源（同一个 `StepEvent` 喂给 `toolDisplayState` 和钩子）。
