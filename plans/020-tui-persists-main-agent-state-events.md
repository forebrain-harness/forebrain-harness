# Plan 020：TUI 主 agent 的其余会话状态也落库——turn diff、上下文预算、队列预览、steer 送达、模式切换；gateway 审批续跑的 turn diff 一并补上

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **Drift check（先跑）**：`git diff --stat 005c1a8 -- pkg/tui/chat_turn.go pkg/tui/chat_session.go pkg/tui/run.go pkg/gateway/approval.go pkg/gateway/wsevents.go pkg/turn/events.go`
> 只应出现已执行计划（012/014/018 等）的已知改动；对照 "Current state" 核对。

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: LOW-MED（只新增"只落不画"的事件；turn diff 会让 web 重载 TUI 会话时多出 diff 卡——这是要修的缺口）
- **Depends on**: plan 014（复用其"只落不画"的写法）；与 018 无冲突（本计划不涉及回答/思考增量）
- **Category**: bug
- **Planned at**: commit `005c1a8` + 主树工作区未提交改动，2026-10-10

## Why this matters

owner 规则：本机 web 与 TUI 共享同一份会话数据、全部落库（文件型数据入库暂不做）。真实库对比：TUI 主 agent 没有落 `turn_diff_updated`、`token_budget_updated`、
`pending_input_updated`、`input_delivered`、`mode_changed`，gateway 会话都有。可见后果：**web 重载 TUI 会话看不到 turn diff 卡片**（本机 TUI 会话 `turn_diff_updated` 为 0 条），
上下文用量表在重载后没有值。另外 gateway 在**审批后续跑**（detached 路径）里发布工具步骤时只发步骤事件、不拆 diff 伴随事件，同样缺 diff 卡。

## Current state

- gateway socket 路径：`pkg/gateway/wsevents.go:126-147`——`step` op 转规范事件后，`turn.ExtractTurnDiffPayload(payload)` 命中则再发一条
  `event.NewRunEvent(baseID+"-diff", ..., event.DiffEventTurnUpdated, diffPayload, now)`。
- gateway detached 路径：`pkg/gateway/approval.go:101-115` 钩子只 `tool.RunEventFromStep` → `Publish`，**没有** diff 伴随事件。
- TUI：`pkg/tui/chat_turn.go:277-293` 主 agent 分支 `tool.RunEventFromStep` → `persistRunEvent`，**没有** diff 伴随事件。
- `turn.ExtractTurnDiffPayload(payload any) (event.TurnDiffUpdatedPayload, bool)`（`pkg/turn/events.go:56`）。
- 上下文预算：TUI `tokenBudgetMessageFromUsage`（`pkg/tui/chat_turn.go:1265-1274`，`run.ContextBudget(...)` 返回 payload）与 `notifyTokenBudget`（`:1240-1249`）只发 UI 消息；
  gateway 发 `token_budget_updated`（`wsevents.go:119-125`；subagent 用 `publishEvent(..., event.CompactEventBudgetUpdated, payload)`，`pkg/run/subagent.go:961-967`）。
- 队列：TUI `pkg/tui/chat_session.go:1052-1057` 的 `q.SetChangeHook` 只发 `InputQueueChangedMsg`；送达的 steer 由 UI 循环用 `takeDeliveredSteers`（`pkg/tui/run.go:2697-2707`，**破坏性读取**）渲染。
  gateway 对照：`pkg/gateway/run_control.go:253-292` `appendPendingInputUpdated` 与 `watchRunQueue`（`input_delivered` payload `{Text, Attachments, MentionImages}`；`pending_input_updated` payload `{PendingSteers, RejectedSteers, QueuedMessages}`）。
- 模式：gateway 发 `mode_changed`（`ModeChangedPayload{Mode, Phase}`，`server.go:1270` 等）；TUI 切换模式的位置用 `grep -rn 'ModeChanged\|SwitchMode\|setMode' pkg/tui/*.go | grep -v _test` 定位。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/... ./pkg/gateway/... ./pkg/turn/... ./pkg/architecture/... -count=1` | 全部 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui/... ./pkg/gateway/... ./pkg/turn/...`；`gofmt -l pkg/tui pkg/gateway pkg/turn` | 无输出 |

## Scope

**In scope**：`pkg/turn/events.go`（共享"步骤 → 规范事件（含 diff 伴随）"函数）、`pkg/gateway/approval.go`、`pkg/gateway/wsevents.go`（改用共享函数，行为不变）、
`pkg/tui/chat_turn.go`、`pkg/tui/chat_session.go`、`pkg/tui/run.go`（只加落库调用）、TUI 模式切换处、对应测试。

**Out of scope**：界面；回答/思考增量（018）；文件型数据（owner 暂不做）。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：步骤的规范事件（含 diff 伴随）共享一份

`pkg/turn/events.go` 加 `func CanonicalEventsFromStep(ctx context.Context, sessionID, runID, channel string, evt tool.StepEvent) []event.RunEvent`：
`tool.RunEventFromStep` 的结果，加上 `ExtractTurnDiffPayload` 命中时的伴随事件（id 为步骤事件 id + `"-diff"`；步骤事件 id 为空时伴随事件 id 也为空）。
gateway detached 钩子（`approval.go:101-115`）与 TUI 主 agent 分支改用它（TUI 每条都 `persistRunEvent`；gateway 每条都 `Publish`）。
`wsevents.go` 的 socket 路径行为不变（它从 ws 数据构造，保持原样）。

测试：`pkg/turn` 一条带 `turn_diff` 输出的完成步骤 → 返回两条事件、第二条类型 `turn_diff_updated`；gateway detached 钩子喂这样的步骤 → 库里两条。

### Step 2：上下文预算

TUI 两处（`tokenBudgetMessageFromUsage` 的调用方 `OnUsageSnapshot`、`notifyTokenBudget`）在发 UI 消息的同时，`persistRunEvent` 一条 `event.CompactEventBudgetUpdated`，
payload 与 gateway 相同（`run.ContextBudget` 的返回值）；需要 run id（plan 015 的 `foregroundTurn.currentRunID()` 若已存在则复用，否则照 014 在 `turnBeforeAgent` 记下）。
**频率**：每次模型响应一次（不是每个 token），与 gateway 一致。

### Step 3：队列预览与 steer 送达

1. `q.SetChangeHook` 里在发 UI 消息后，`persistRunEvent` 一条 `pending_input_updated`，payload 来自 `q.Preview()`（**只读**，不要调 `TakeDelivered`）。
2. `input_delivered`：在 UI 循环渲染送达 steer 的地方（`takeDeliveredSteers` 的调用方）逐条 `persistRunEvent`，payload `{Text, Attachments, MentionImages}` 与 gateway 一致。
   若该处拿不到 `ChatSession` 或 run id → 在 `ChatSession` 上加一个 `recordDeliveredSteers(sessionID, runID, items)` 由调用方传入；做不到 → STOP。

### Step 4：模式切换

TUI 每次模式切换（Shift+Tab、`/plan`、工具进入/退出 plan 模式后的切换）`persistRunEvent` 一条 `mode_changed{Mode, Phase}`；无 run 时 run id 为空（与 gateway 一致：`mode_changed` 可不属于 run）。

### Step 5：真机

driver（隔离 home）跑一轮带文件编辑的 turn（`toolcall` 模式会写计划文件——有 turn diff）+ 一次 Shift+Tab 切换模式：`$D db "select event_type, count(*) from fb_session_events where session_id='<sid>' group by event_type"` 含
`turn_diff_updated`、`token_budget_updated`、`mode_changed`；用同一 home 起 gateway 取 events，确认 web 重载能拿到 diff（vitest 探针或 `curl` 检查事件存在即可）。收尾清理。

## Test plan

- `pkg/turn`：`CanonicalEventsFromStep` 带/不带 diff。
- `pkg/gateway`：detached 钩子落 diff 伴随事件。
- `pkg/tui`：预算、队列预览、steer 送达、模式切换各一条"只落不画"测试（照 plan 014 的测试搭建）。

## Done criteria

- [ ] Commands 表全部通过
- [ ] 上述测试存在且 PASS
- [ ] Step 5 断言满足
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符。
2. `takeDeliveredSteers` 的调用方无法在不改变"谁先读谁渲染"的前提下落库。
3. 某类事件在 web 重载时会改变既有显示（例如历史 `pending_input_updated` 让队列预览显示陈旧内容）——停下报告实际表现。

## Maintenance notes

- 步骤事件与 diff 伴随事件只在 `turn.CanonicalEventsFromStep` 一处生成；新增 surface 用它。
- 这些事件都是"只落不画"：TUI 界面仍由本地消息驱动。
