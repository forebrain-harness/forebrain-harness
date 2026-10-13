# Plan 014：TUI 主 agent 的 turn 生命周期落库——turn_started / turn_completed，turn_cancelled / turn_error 带上计划进度，与 gateway 同形

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **Drift check（先跑）**：
> ```bash
> git diff --stat 005c1a8 -- pkg/tui/chat_session.go pkg/tui/chat_turn.go pkg/tui/notify.go pkg/gateway/wsevents.go pkg/turn/events.go
> ```
> 写计划时只有 `pkg/tui/chat_turn.go` 有未提交改动（plan 012 在 `runAuditStepHook` 里加的 `if evt.Denied {` 那几行），其余与 `005c1a8` 相同。
> 出现别的 hunk → 对照 "Current state" 逐段核对，不一致即 STOP。

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW（只新增落库，不改界面；落库用"只落不画"的现成函数）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `005c1a8` + 主树工作区里 008/011/012/013 的未提交改动，2026-10-10

## Why this matters

owner 2026-10-10 的规则：**本机 web 端与 TUI 端共享同一份会话数据、全部落库；TUI 主 agent 的 `turn_started`/`turn_completed`
等也要入库**；文字只存 transcript，不在事件里重复存。

现状：TUI 调 `s.Core.Submit(..., nil)` 时事件 sink 传 `nil`（`pkg/tui/chat_session.go:1896`、`pkg/tui/chat_turn.go:897`），
TUI 自己也不发 `turn_started`/`turn_completed`——真实状态库里 TUI 会话没有一条这两类事件。gateway 自己发布它们
（`wsevents.go:71-79`、`run_control.go:845`、`api_extra.go:3298`），并经 `publishGatewayRunEvent`（`pkg/gateway/approval.go:126-146`）给
`turn_completed`/`turn_cancelled`/`turn_error` 补上 `RunPlanFacts`（计划清单进度）；TUI 已有的 `turn_cancelled`/`turn_error` 没带这个。

本计划让 TUI 会话的 turn 生命周期在库里与 gateway 会话同形。**回答文字不进生命周期事件**（文字只在 transcript 里）：TUI 的
`turn_completed` 不填 `Text`。（gateway 落库的那份去掉 `Text` 由 plan 017 处理。）

## Current state

`pkg/event/run_events.go:130-151`：

```go
type TurnStartedPayload struct{}

type RunPlanFacts struct {
	PlanDone   int    `json:"plan_done,omitempty"`
	PlanTotal  int    `json:"plan_total,omitempty"`
	PlanActive string `json:"plan_active,omitempty"`
}

type TurnCompletedPayload struct {
	Text      string `json:"text,omitempty"`
	ElapsedMS int64  `json:"elapsed_ms,omitempty"`
	RunPlanFacts
}

type TurnCancelledPayload struct {
	Message string `json:"message,omitempty"`
	RunPlanFacts
}
```

（`TurnErrorPayload` 同样嵌入 `RunPlanFacts`，以 `pkg/event` 实际定义为准。）

计划进度的计算在 gateway 包里、未导出：`pkg/gateway/wsevents.go:767-806`

```go
func (s *Server) runPlanFacts(ctx context.Context, runID string) event.RunPlanFacts {
	return lastPlanProgressOfRun(ctx, s.RunRT, runID).Facts()
}

type runPlanEventLister interface {
	ListRunEventsOfTypes(ctx context.Context, runID string, types ...string) ([]state.SessionEvent, error)
}

func lastPlanProgressOfRun(ctx context.Context, runs runPlanEventLister, runID string) event.PlanProgress {
	... 读该 run 最后一条不带 agent_id 的 plan_updated ...
	return event.PlanProgressOf(last.Items, last.Completed, last.Total, last.Explanation)
}
```

`pkg/gateway/wsevents_test.go:1512-1525` 有它的测试。

TUI 的落库函数（`pkg/tui/notify.go`）：
- `persistRunEvent(ctx, evt)`（`:1050-1058`）：**只落库、不画**——本计划用它。
- `publishTUIRunEvent(ctx, sessionID, runID, type, payload)`（`:1025-1034`）→ `publishRunEvent`（`:739`）：落库**并**画。已有的
  `turn_cancelled`（`pkg/tui/chat_turn.go:1151`）、`turn_error`（`chat_session.go:680`、`:718`、`chat_turn.go:871`）走它。
- `persistRunTurnError(sessionID, runID, text)`（`:1039-1045`）：只落库的 `turn_error`（`chat_session.go:2024`、`chat_turn.go:986`）。

run 的开始（引擎建好 run 后回调，`runID` 可用）：
- 新 turn：`pkg/tui/chat_session.go:1879-1893` 的 `s.turnBeforeAgent = func(agentCtx context.Context, runID string) error { ... s.notifyUI(RunStartedMsg{RunID: runID, turn: foreground}); return nil }`
- 审批后续跑：`pkg/tui/chat_turn.go:889-894` 同形（`RunStartedMsg{RunID: runID}`）。

run 的成功结束：`appendAssistantOutcome(sessionID, runID, channel, userInput, completion turnCompletion, res, streamAssistantViaTUI, out)`
（`pkg/tui/chat_turn.go:538`），只在两处成功分支调用：`chat_session.go:2028`（新 turn）、`chat_turn.go:990`（续跑）——两处都在错误分支
`return` 之后。`completion.Duration` 是本轮耗时（`RunEndedMsg.WorkedDuration` 用的就是它，见 `chat_session.go:1744-1750`）。

回放与重载不受影响的依据：TUI 回放（`pkg/tui/commands.go`）没有 `turn_started`/`turn_completed` 分支（只处理 `turn_error`，`:476`）；
web 重载会回放这两类事件——gateway 会话今天就有，web 正常。

## Commands you will need

| 用途 | 命令（仓库根目录） | 成功预期 |
|---|---|---|
| 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/... ./pkg/turn/... ./pkg/gateway/... ./pkg/architecture/... -count=1` | 全部 `ok`（`pkg/tui` 的 `TestCharacterizationNetworkApproval` 访问网络、偶发超时，单跑通过即可，报告注明） |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui/... ./pkg/turn/... ./pkg/gateway/...`；`gofmt -l pkg/tui pkg/turn pkg/gateway` | 无输出 |

## Scope

**In scope**：`pkg/turn/events.go`、`pkg/turn/events_test.go`（无则新建）、`pkg/gateway/wsevents.go`（只改为转调）、
`pkg/tui/chat_session.go`、`pkg/tui/chat_turn.go`、`pkg/tui/notify.go`（只改 `persistRunTurnError` 与 `publishTUIRunEvent` 的计划进度）、
新测试文件 `pkg/tui/turn_lifecycle_record_test.go`。

**Out of scope**：TUI 界面与回放；`frontend/**`；gateway 的发布行为；回答文字（不进生命周期事件）；首 token 延迟（plan 015）。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：计划进度下沉到引擎

把 `runPlanEventLister` 与 `lastPlanProgressOfRun` 的实现搬到 `pkg/turn/events.go`，导出为：

```go
// RunPlanEventLister is the slice of the run store a run's checklist lookup needs.
type RunPlanEventLister interface {
	ListRunEventsOfTypes(ctx context.Context, runID string, types ...string) ([]state.SessionEvent, error)
}

// LastPlanProgressOfRun folds the run's final checklist update into the
// worked line's facts. Both surfaces close a run with these facts through this.
func LastPlanProgressOfRun(ctx context.Context, runs RunPlanEventLister, runID string) event.PlanProgress { /* 原函数体 */ }
```

`pkg/gateway/wsevents.go` 保留 `type runPlanEventLister = turn.RunPlanEventLister` 和
`func lastPlanProgressOfRun(...) event.PlanProgress { return turn.LastPlanProgressOfRun(ctx, runs, runID) }`，测试不改。
在 `pkg/turn/events_test.go` 照 `wsevents_test.go:1490-1525` 写一个 `TestLastPlanProgressOfRun`（有清单 / 无清单 / nil store 三种）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/... ./pkg/gateway/... ./pkg/architecture/... -count=1` → 全部 `ok`。

### Step 2：TUI 的计划进度辅助函数

`pkg/tui/notify.go` 加：

```go
// runPlanFacts is the run's final checklist state, the facts every way a run
// ends carries — the same lookup the gateway closes its runs with.
func (s *ChatSession) runPlanFacts(ctx context.Context, runID string) event.RunPlanFacts {
	if s == nil || s.runSvc() == nil {
		return event.RunPlanFacts{}
	}
	return turn.LastPlanProgressOfRun(ctx, s.runSvc(), runID).Facts()
}
```

（`s.runSvc()` 若不满足 `turn.RunPlanEventLister` → STOP。）

然后：
- `publishTUIRunEvent`：在调用 `publishRunEvent` 前，若 `payload` 是 `event.TurnCancelledPayload` 或 `event.TurnErrorPayload`，补上
  `RunPlanFacts = s.runPlanFacts(ctx, runID)`（照 `pkg/gateway/approval.go:132-143` 的 type switch 写法）。
- `persistRunTurnError`：构造的 `TurnErrorPayload` 同样补 `RunPlanFacts`。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tui/...` → 无输出。

### Step 3：turn_started

在两处 `turnBeforeAgent` 回调里、`s.notifyUI(RunStartedMsg{...})` 之前加：

```go
		// The run's start, in the log both surfaces read: the gateway records
		// its own turns the same way. The fixed id keeps a continuation past an
		// approval — the same run — from recording a second start.
		s.persistRunEvent(agentCtx, event.NewRunEvent("turn-started:"+runID, runID, sessionID, event.RunEventTurnStarted, event.TurnStartedPayload{}, time.Now()))
```

（续跑路径里会话 id 变量是 `p.SessionID`。）**不要**给 `!` 用户 shell 命令（`chat_session.go:2294`，`userShellRunID`）加——那不是 agent run。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tui/...` → 无输出。

### Step 4：turn_completed

`appendAssistantOutcome` 里，`turn.PersistAssistantTurn(...)` 之后加：

```go
	// The run's end, as the gateway records its own: the clock the worked line
	// reports and the checklist facts. The answer itself is the transcript's,
	// recorded above; the event does not carry a second copy of it.
	s.persistRunEvent(context.Background(), event.NewRunEvent("turn-completed:"+runID, runID, sessionID, event.RunEventTurnCompleted, event.TurnCompletedPayload{
		ElapsedMS:    completion.Duration.Milliseconds(),
		RunPlanFacts: s.runPlanFacts(context.Background(), runID),
	}, time.Now()))
```

（`runID` 为空时跳过——没有 run 就没有结束事件。）

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tui/...` → 无输出。

### Step 5：测试

新建 `pkg/tui/turn_lifecycle_record_test.go`，照 `pkg/tui/run_test.go:297` 搭临时 DB、会话 `sid`、一个 run，并在该 run 下先落一条
`plan_updated`（`event.PlanUpdatedPayload`，两项、完成一项，不带 `AgentID`）。
1. `TestTurnLifecycleIsRecordedLikeTheGateway`：调用 `s.persistRunEvent` 写 turn_started（照 Step 3 的写法，调两次），再按 Step 4 的写法
   构造一次 turn_completed（`ElapsedMS: 1200`）。断言：`runSvc.ListSessionEvents(ctx, "sid", 0, 0, 20)` 里 `turn_started` **恰好 1 条**；
   `turn_completed` 1 条，payload `elapsed_ms == 1200`、`plan_done == 1`、`plan_total == 2`、**没有** `text` 字段。
   （若直接调用 `appendAssistantOutcome` 搭建成本过高，可把 Step 4 的事件构造抽成一个小函数 `turnCompletedEvent(sessionID, runID, elapsed, facts)`，
   测试调它；这是允许的结构调整，报告里写明。）
2. `TestCancelledAndFailedTurnsCarryPlanFacts`：调 `s.publishTUIRunEvent(ctx, "sid", run.ID, "turn_cancelled", event.TurnCancelledPayload{Message: "cancelled"})`
   与 `s.persistRunTurnError("sid", run.ID, "boom")`，两条落库事件的 payload 都带 `plan_done == 1`、`plan_total == 2`。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/ -run 'TestTurnLifecycleIsRecordedLikeTheGateway|TestCancelledAndFailedTurnsCarryPlanFacts' -count=1 -v` → PASS。

### Step 6：真机

```bash
export FOREBRAIN_RUN_WORK=$TMPDIR/fb-014 FOREBRAIN_RUN_SESSION=fb-014 FOREBRAIN_RUN_PORT=8816
D=.claude/skills/run-forebrain/driver.sh
$D build && $D reset
$D start reply; $D submit 'hello'; sleep 4; $D screen > $TMPDIR/fb-014-a.txt; SID=$($D sid)
$D db "select event_type, count(*) from fb_session_events where session_id='$SID' and event_type like 'turn_%' group by event_type"
$D stop; RESUME_ID=$SID $D start reply; sleep 4; $D screen > $TMPDIR/fb-014-b.txt; $D stop
$D start toolcall; $D submit 'plan something'; $D wait 'Yes, proceed' 25; $D key Enter; $D wait 'Ask another model' 25; $D key Down; $D key Enter; sleep 4
SID2=$($D sid); $D db "select event_type, count(*) from fb_session_events where session_id='$SID2' and event_type like 'turn_%' group by event_type"
$D stop; rm -rf $TMPDIR/fb-014
```

（`toolcall` 模式：批准 enter_plan_mode 后，在 exit_plan_mode 的 overlay 里 `Down` + `Enter` 选 "Yes, proceed"；若选项顺序不同，按屏幕调整并在报告写明。）

断言：第一个会话 `turn_started|1`、`turn_completed|1`；`fb-014-a.txt` 与 `fb-014-b.txt` 的对话部分一致（回放不受影响）；
第二个会话（经过一次审批续跑）`turn_started|1`、`turn_completed|1`。

## Test plan

- `pkg/turn`：`TestLastPlanProgressOfRun`；gateway 既有测试保持通过。
- `pkg/tui`：`TestTurnLifecycleIsRecordedLikeTheGateway`、`TestCancelledAndFailedTurnsCarryPlanFacts`。
- 真机：Step 6。

## Done criteria

- [ ] Commands 表全部通过
- [ ] 3 个新测试存在且 PASS
- [ ] `grep -n 'func LastPlanProgressOfRun' pkg/turn/events.go` 有 1 行；`grep -n '"turn-started:"\|"turn-completed:"' pkg/tui/*.go | grep -v _test` 至少 3 行（两处 started、一处 completed）
- [ ] Step 6 断言满足，输出摘要写进报告
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符。
2. `s.runSvc()` 不满足 `turn.RunPlanEventLister`。
3. `appendAssistantOutcome` 还有 Current state 没列出的调用点（`grep -n 'appendAssistantOutcome(' pkg/tui/*.go`）。
4. 任何既有测试因为"多了一条事件"失败——停下报告，不改既有断言。

## Maintenance notes

- 生命周期事件只落库、不画；TUI 界面的开始/结束由 `RunStartedMsg`/`RunEndedMsg` 驱动，两者独立。
- 固定 id（`turn-started:<run>`、`turn-completed:<run>`）靠幂等防重复，不要换随机 id。
- 首 token 延迟（plan 015）会写在 `fb_runs` 上，不进这些事件。
