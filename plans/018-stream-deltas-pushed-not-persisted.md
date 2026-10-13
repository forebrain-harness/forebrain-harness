# Plan 018：回答/思考增量与工具输出增量只实时推送、不再落库；持久化的生命周期事件不带回答文字；断线重连与刷新时由 gateway 从内存补在途文字与在途工具输出

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **Drift check（先跑）**：
> - `grep -c 'transcript.flush(' pkg/run/orchestration_llm.go` ≥ 3（plan 016 已执行）
> - plan 017 已执行：`git diff --stat 005c1a8 -- pkg/tui/commands.go frontend/src/composables/useChatStream.ts` 显示 017 的改动，且 017 的报告状态为 DONE
> 任一不满足 → STOP（先停增量会让重载丢文字）。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: HIGH（改变事件总线的落库语义；web 断线重连与刷新的在途文字改由内存补发）
- **Depends on**: plan 016、plan 017
- **Category**: bug
- **Planned at**: commit `005c1a8` + 主树工作区未提交改动，2026-10-10

## Why this matters

owner 裁决：**流式文字只存 transcript，不与 transcript 重复存储**；`assistant_delta`/`reasoning_delta` 不再落库，web 自己的 run 仍逐 token 实时推送（推送不入库）；
旧数据由 plan 019 迁移清理。并要求补偿：web 断线重连由 gateway 从内存（`StreamPartial`）补在途文本；gateway 自己跑的 subagent 在途文本同理。
owner 2026-10-10 追加裁决：**工具输出增量（`tool_output_delta`）本期也改成只推不存**——工具完成后，完整输出已在完成步骤里（主 agent 在 transcript 的 tool 行，subagent 在 `tool_call_completed` 事件与 worker transcript），增量与之重复；在途的工具输出同样由 gateway 从内存补发。

本计划落地后：新产生的会话里，文字只在 transcript（016 保证每个文字块结束就写入，017 让 subagent 视图从 worker transcript 重建）。

## Current state

### 谁在落增量

- gateway：`runEventBus.Publish`（`pkg/gateway/wsevents.go:585-623`）**先落库再分发**：

```go
func (b *runEventBus) Publish(ctx context.Context, evt event.RunEvent) error {
	...
	if b.store != nil {
		persisted, err := b.store.AppendSessionEvent(ctx, sessionEventRecord(evt))
		switch {
		case errors.Is(err, state.ErrSessionNotStarted):
		case err != nil:
			return err
		default:
			evt = turn.RunEventFromRecord(persisted)
		}
	}
	if b.onRunEnded != nil { b.onRunEnded(evt) }
	... for _, sub := range subs { sub.deliver(session, evt) }
	return nil
}
```

  主 agent 的流（`withAnswerStream`，`pkg/gateway/run_control.go:415-434`，`run.EventStreamSink(..., s.RunEvents(), ...)`）与 gateway 跑的 subagent 的流
  （`pkg/run/subagent.go:943-970` `subagentRunContext` → `EventStreamSink(ctx, sink, ...)`）都经它；"没流式输出的回答补发一次整段增量"在
  `server.go:2109-2115`、`run_control.go:831`、`api_extra.go:3280`。
- TUI：`ChatSession.publishRunEvent`（`pkg/tui/notify.go:739-800`）**先落库再画**；TUI 跑的 subagent 的增量经它（subagent 的 `EventStreamSink` 的 sink）。
- `event.NewRunEvent` 传空 id 时 id 为空，落库时 `AppendSessionEventOnce` 才补 `evt-<uuid>`；TUI 的 `markEventShownOnce` 对空 id 直接放行（`notify.go:964-972`），所以不落库的增量照样会画。

### 工具输出增量（`tool_output_delta`）

- 产生者只有 shell 工具：`pkg/tool/shell_tool.go:427-450` 的 `shellOutputEmitter` 把 stdout/stderr 分块发成 `StepKindToolOutputDelta` 步骤；
  `pkg/tool/notification.go:45` 对这类步骤**不生成事件 id**（id 为空，落库时才补 `evt-<uuid>`），payload 为 `event.ToolCallOutputDeltaPayload{StepID, ToolName, Text, AgentID, Summary, ToolMeta}`（`notification.go:69-77`）。
- 落库路径：gateway 全部经 `runEventBus.Publish`（socket `step` op 在 `pkg/gateway/wsevents.go:404-418` 转成规范事件）；TUI 主 agent 在 `pkg/tui/chat_turn.go:277-291`
  （`tool.RunEventFromStep` → `s.persistRunEvent`）；TUI 跑的 subagent 在 `chat_turn.go:324-330`（`s.runner().Events.Publish` → `publishRunEvent`）。
- 读者：web 重载主对话时它属于 `transcriptBackedEventTypes`（`frontend/src/composables/useChatStream.ts:2426-2435`），本来就跳过、用 transcript 的 tool 行；
  web subagent 视图把它追加到对应卡片的 `output`（`useChatStream.ts:2901-2913`），随后的 `tool_call_completed` 用完成步骤**整体替换**该卡片（`:2862-2868`）；
  TUI 回放 subagent（`pkg/tui/commands.go:1041-1049`）同理，完成时 reducer 丢弃累积的增量（`pkg/tui/reducer.go:1901-1916`）。
  → 工具完成后增量没有任何读者；只在"工具还在跑时刷新/重连"与"进程在工具完成前崩溃"两种情况下它是唯一记录。
- 本机真实库（只读统计）：主 agent 9,839 行（payload 约 31.7 MB），subagent 9,555 行（约 31.5 MB），涉及 15,425 个工具步骤，其中只有 6 个步骤没有完成事件。

### 生命周期事件里的回答文字

- gateway：`wsevents.go:74-80` 把 `run_completed` op 映射成 `TurnCompletedPayload{Text: m.Text, ...}`；`run_control.go:845`、`api_extra.go:3298` 发布时也带 `Text`。
- 读者：`pkg/turn/scheduler.go:76-86` `FireEndingFromRunEvent` 把 `TurnCompletedPayload.Text` 作为定时任务（cron/heartbeat）这一轮的 `Output`
  （`pkg/process/cron_service.go:160` 一带读运行结束事件）——**去掉 Text 之前必须让它改从 transcript 读**。
- web：live 自己的 run 用 legacy `run_completed` op 的 text（不落库，不受影响）；重载时 `turn_completed` 的 text 只在"没有 assistant 块"时兜底（`withSettledTurnText`），有 transcript 行就用不到。

### 断线重连与刷新

- web：`frontend/src/composables/useChatStream.ts:4415-4428` 断线后 `resume_connection`（带 `cursor`），注释："The durable replay starts after this cursor; clearing text/tool state here would make
  deduplication discard the only copy of the partial response"——今天断线期间的文字靠重放持久化增量补回。
- gateway：`server.go:1226-1250` `resume_connection` → `resumeRunEvents(requestID, runID, cursor)` 只重放持久化事件。
- 刷新/打开会话：`bind_session` → `writeSessionEventSnapshot`（`server.go:886` 起）补快照后转 live。
- 主 agent 的在途文字 gateway 已在内存里：`withAnswerStream` 的 `partial *turn.StreamPartial`（`AppendContent`/`AppendReasoning`/`ResponseCompleted`）。
  gateway 跑的 subagent 目前**没有**在途文字的内存副本。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/event/... ./pkg/gateway/... ./pkg/tui/... ./pkg/turn/... ./pkg/run/... ./pkg/process/... ./pkg/architecture/... -count=1` | 全部 `ok` |
| 前端 | `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 无输出 |
| 构建 | `make ui` | 成功 |

## Scope

**In scope**：`pkg/event`（共享判定函数）、`pkg/gateway/wsevents.go`（总线）、`pkg/gateway/server.go`（重连/绑定补发；整段回答补发改为只推）、`pkg/gateway/run_control.go`、
`pkg/gateway/api_extra.go`（发布处去 Text）、`pkg/run/subagent.go`（subagent 在途文字的内存副本）、`pkg/tui/notify.go`（`publishRunEvent`）、`pkg/turn/scheduler.go`、
`pkg/process/cron_service.go`（若 cron 输出读取需要改）、`pkg/state`（若需要"该 run 最后一条 assistant 文字"的读取方法）、`frontend/src/composables/useChatStream.ts`、对应测试、`pkg/gateway/dist`。

**Out of scope**：旧数据（plan 019）；首 token（plan 015）；工具输出的其他形态（完成步骤的 `DisplayBody`、spool 文件）——不改。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：一个判定，两端共用

`pkg/event/run_events.go` 加：

```go
// IsPushOnlyStreamEvent reports a fragment of a run as it happens — answer and
// reasoning text, the end of a reasoning block, a chunk of a tool's output.
// Each is kept once elsewhere: the words in the transcript (written as each
// block ends), the tool's output on its completed step. These events are
// pushed to whoever is watching and never stored.
func IsPushOnlyStreamEvent(eventType string) bool {
	switch eventType {
	case RunEventAssistantDelta, RunEventReasoningDelta, RunEventReasoningDone, RunEventToolOutputDelta:
		return true
	}
	return false
}
```

### Step 2：gateway 总线只推不存

`runEventBus.Publish`：`if event.IsPushOnlyStreamEvent(evt.Type) { 跳过 AppendSessionEvent 与 onRunEnded，直接分发 }`。分发的事件没有 sequence（为 0）、id 为空——
确认 web 端 `rememberObservedEvent` 对 sequence 0 / 空 id 不推进游标、不去重（读 `useChatStream.ts` 的 `rememberObservedEvent`），否则 STOP。
测试：`pkg/gateway` 加 `TestStreamDeltasAreDeliveredNotStored`——订阅一个会话，分别发布 `assistant_delta` 与 `tool_output_delta`，订阅者都收到，`ListSessionEvents` 里都没有；发布 `tool_call_completed` 仍落库。

### Step 3：TUI 只画不存

1. `publishRunEvent`：对 `IsPushOnlyStreamEvent` 的事件跳过 `AppendSessionEventOnce`，直接走绘制（覆盖 TUI 跑的 subagent 的回答/思考/工具输出增量）。
2. 主 agent 工具步骤（`pkg/tui/chat_turn.go:289-291` 的 `s.persistRunEvent(stepCtx, canonical)`）：`canonical.Type` 为 `IsPushOnlyStreamEvent` 时不调用 `persistRunEvent`；其后画卡片的逻辑不变（主 agent 的工具卡片由同一分支末尾的 `s.notifyToolStepHooks(stepCtx, sid, rid, "tui", evt)`（`:301`）画出，与是否落库无关；若执行时发现画面依赖落库后的回调 → STOP）。若 plan 020 已执行（这里已改为 `turn.CanonicalEventsFromStep` 循环），对循环里的每条事件做同样判断。

测试：`pkg/tui` 加 `TestSubagentStreamIsDrawnNotStored`（照 `run_test.go:297` 搭建，
发布一条带 `agent_id` 的 `assistant_delta` → UI 收到 `NewMessageMsg`，库里没有该事件）；再加 `TestToolOutputDeltaIsDrawnNotStored`：主 agent 与 subagent 各一条 shell 输出增量 → 画面收到、库里没有，随后的完成步骤照常落库。

### Step 4：生命周期事件不带回答文字（先改读者）

1. 定时任务输出：`FireEndingFromRunEvent` 不再读 `payload.Text`；改为从 transcript 读该 run 的最终回答（state 层加 `LastAssistantTextForRun(ctx, runID) (string, error)`：
   该 run 最后一条 assistant 行的文字；用 `turn.AssistantOutcomeText` 同样的"思考回显"判断——若无法在 state 层复用，在 turn 层组合）。
   找到所有调用 `FireEndingFromRunEvent` 的地方（`grep -rn 'FireEndingFromRunEvent' pkg`），确保它们拿得到 run id 与存储；拿不到 → STOP。
   测试：cron/heartbeat 既有测试（`grep -rln 'FireEnding' pkg --include=*_test.go`）保持通过；新增一条"turn_completed 不带 Text 时 Output 来自 transcript"。
2. 再去掉写入：`wsevents.go` 的 `run_completed` 映射不填 `Text`；`run_control.go:845`、`api_extra.go:3298` 发布时不填 `Text`（legacy `run_completed` op 自身的 `Text` 保留——它只发给本 socket，不落库）。
3. 整段回答补发（`server.go:2109-2115`、`run_control.go:831`、`api_extra.go:3280`）保持调用，Step 2 已让它只推不存。

### Step 5：断线重连补在途文字（主 agent）

1. gateway 维护"run id → 在途文字"的登记：`withAnswerStream` 创建的 `partial *turn.StreamPartial` 在 run 存续期间登记到 `Server` 上的一个并发安全 map（run 结束时移除）。
   需要一个读取"当前未完成块"的方法：`StreamPartial` 在 `ResponseCompleted()` 时清空已完成部分——若它没有"读当前内容"的方法，加 `Snapshot() (content, reasoning string)`。
   **在途工具输出**也进同一登记表：在 `runEventBus.Publish` 里（gateway 的主 agent、subagent、审批续跑三条路径都经它），`tool_output_delta` 按 `(run_id, agent_id, step_id)` 追加文字，同一步骤的 `tool_call_completed`（或该 run 结束）时移除；每个步骤只保留最后 256 KiB（超出从头截掉，前端照常显示）。
2. `resume_connection` 成功重放后，若该 run 在登记表里，发一条 **legacy op**（不落库）：
   `wsServerMsg{Op: "stream_partial", RequestID: m.RequestID, RunID: runID, SessionID: sid, Data: {"content": ..., "reasoning": ..., "tool_outputs": [{"step_id": ..., "agent_id": ..., "text": ...}]}}`。
3. 前端：`handleOp` 处理 `stream_partial`：把当前 assistant 消息里**最后一个未关闭**的 assistant 块文字替换为 `content`（无未关闭块且 content 非空时新开一个），thinking 块同理用 `reasoning`；
   这是"替换"不是"追加"，避免与重连前已显示的部分重复。`tool_outputs` 的每一项：找到对应 `step_id` 的工具卡片（`agent_id` 非空时在该 subagent 记录里找），把 `output` **替换**为 `text`；找不到卡片就忽略。
4. 已完成、断线期间写入 transcript 的文字块：重连成功后前端调用一次该会话的 transcript 重载（`loadMessages` 的既有逻辑在 `isStreaming` 时会保留本地 turn，
   读 `useChatStream.ts` 中 `loadMessages` 的 `isStreaming` 分支确认合并行为）；若该合并会造成重复或丢块 → STOP 报告实际表现。

### Step 6：刷新/打开会话时补在途文字（gateway 自己在跑的主 agent 与 subagent）

1. subagent：`subagentRunContext` 用的 `EventStreamSink` 外面包一层，把该执行的在途文字累积到同一个登记表（键为 run id + agent id），执行结束移除。
2. `bind_session` 写完快照后，对该会话在登记表里的每个在途流各发一条 `stream_partial`（带 `agent_id`，主 agent 为空），在途工具输出放在 `tool_outputs` 里一并补发。
3. 前端：`stream_partial` 带 `agent_id` 时写入对应 subagent 记录的未关闭块（同样是替换）。
   TUI 托管的 run 不在 gateway 的登记表里——刷新后只看到已完成的块（与 owner"不做跨端实时推送"一致），报告里写明。

### Step 7：测试与真机

- 前端：`stream_partial` 替换而非追加（主对话、subagent 各一条）；`tool_outputs` 替换对应卡片的输出（主对话、subagent 各一条）；重连后不重复。
- gateway：`resume_connection` 后收到 `stream_partial`；`bind_session` 期间有在途 subagent 时收到带 `agent_id` 的 `stream_partial`；工具在跑时 `bind_session` 收到含 `tool_outputs` 的 `stream_partial`，该步骤完成后登记表里没有它。
- 真机：gateway + 浏览器或 vitest 探针，确认新会话的 `fb_session_events` 里**没有** `assistant_delta`/`reasoning_delta`/`reasoning_done`
  （`sqlite3 ... "select count(*) from fb_session_events where session_id='<sid>' and event_type in ('assistant_delta','reasoning_delta','reasoning_done','tool_output_delta')"` = 0；用 `$D start tool '{"name":"shell","arguments":{"command":"echo hi; sleep 1; echo bye"}}'` 让会话里有一次带输出的 shell），
  而 web 重载、TUI resume 画面与改动前一致（对照方法同 plan 008 末尾验收记录）。TUI 会话同样 0 条。

## Test plan

见 Step 2、3、4.1、7；共至少 10 个新测试。

## Done criteria

- [ ] Commands 表全部通过；`make ui` 成功
- [ ] 新会话（web 与 TUI 各一）事件表里回答/思考/工具输出增量为 0 条，重载/回放画面不变（含 shell 卡片的输出）
- [ ] `grep -rn 'Text: *m.Text' pkg/gateway/wsevents.go` 无输出；`FireEndingFromRunEvent` 不读 `payload.Text`
- [ ] 断线重连与刷新的补发测试通过
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check：016 或 017 未完成。
2. web 端对 sequence 0 / 空 id 的事件会推进游标或去重。
3. `FireEndingFromRunEvent` 的调用方拿不到 run id 或存储。
4. 重连后的 transcript 重载与本地 turn 合并会重复或丢块。
5. 任何既有测试断言"增量事件落库"（例如数事件条数）——停下报告是哪条。

## Maintenance notes

- 新增"流式片段"类事件（包括新工具的输出分块）时，加进 `event.IsPushOnlyStreamEvent`；它们只推不存，且必须保证完成时有一份完整记录（transcript 行或完成步骤）。
- 在途文字的内存登记只服务于"同一 gateway 进程"的重连/刷新；跨进程（TUI 托管）按 owner 裁定不做实时。
- 工具在跑时进程崩溃：已输出的部分不再有记录（本机历史 15,425 个步骤中 6 个属于这种情况），这是 owner 接受的取舍。
