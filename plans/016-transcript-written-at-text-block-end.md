# Plan 016：回答与思考在"文字块结束"时就写进 transcript（引擎层，两端与 subagent 共用）；每个思考块一行并记录思考耗时

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **Drift check（先跑）**：
> ```bash
> git diff --stat 005c1a8 -- pkg/run/orchestration_llm.go pkg/run/runner.go pkg/turn/session.go pkg/state/message_sync.go pkg/state/message_parts.go frontend/src/composables/useChatStream.ts pkg/tui/commands.go
> ```
> 写计划时 `pkg/run/orchestration_llm.go` 有 plan 012 的未提交改动（拒绝注入循环，`:108-160`），`frontend/src/composables/useChatStream.ts` 有 008/013 的改动，
> 其余与 `005c1a8` 相同。若 plan 014/015 已执行，`pkg/tui/commands.go` 可能有 015 的 Worked 行改动——不重叠，核对后继续。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED（transcript 写入时机提前：从"turn 结束一次写"变成"每个文字块结束就写"；依赖已有的幂等对账；思考行从每轮一条变为每块一条）
- **Depends on**: none；**plan 017（增量不落库）必须在本计划之后**——本计划先保证文字及时进 transcript，017 才能停止写增量
- **Category**: bug
- **Planned at**: commit `005c1a8` + 主树工作区 008/011/012/013 未提交改动，2026-10-10

## Why this matters

owner 2026-10-10 的裁决：**流式文字只存 transcript，不与 transcript 重复存储**——回答/思考在**文字块结束**时写入 transcript
（文字块结束 = 转入工具调用、思考转回答、回答完成、取消/出错；**不按空行切**）；主 agent 与 subagent 本期一并改；增量事件之后由 plan 017 停止落库。

现状：transcript 只在 turn 结束时由 surface 一次写入（`turn.PersistAssistantTurn` → `AppendMessageSequenceForRun(Result.Session)`），
审批停靠、取消时各有一次部分写入。一个多步 turn 跑了十几分钟，期间已完成的每段回答都只在进程内存里；TUI 崩溃就全丢。思考只在 turn 结束写**一条**
`reasoning` 行（最后一次回答的思考，`pkg/turn/session.go:373-375`），多步 turn 中间各次的思考只有取消路径会落库，所以 web live 看到的多段思考，重载后变少。

已有的基础让这件事风险可控：`AppendMessageSequenceForRun`（`pkg/state/message_sync.go:16-66`）是**幂等对账**——每次只写 transcript 还没有的那段后缀，
专门处理过"部分写入者先写、turn 结束再写全量"的情况；`reasoning` 行不进模型历史（`ParseMessage` 对 `reasoning` 角色返回 false，`pkg/state/message_parts.go:240-275`）。

## Current state

### orchestration 主循环（挂载点）

`pkg/run/orchestration_llm.go`（`Execute`，节选，`:340-420`）：

```go
		latestUsage := usageCopy(res)
		accumulateUsage(&totalUsage, latestUsage)
		if res == nil || res.Message == nil || len(res.Message.ToolCalls) == 0 {
			...
			if res != nil && res.Message != nil {
				if delivery := TurnInputRuntimeFromContext(ctx).BeginSteerDelivery(ctx); delivery != nil {
					session = append(session, *res.Message)
					noteResponseCompleted()
					heldAnswer = res
					sessionBeforeSteers = len(session)
					for _, entry := range delivery.Entries() {
						session = append(session, llm.UserMessage(entry.Parts...))   // ← 暂定的 steer：可能被撤回
					}
					inFlightSteers = delivery
					partialCapture.Set(session)
					continue
				}
			}
			return endTurn(res, latestUsage)                                      // ← 挂载点 C（最终回答）
		}
		session = append(session, *res.Message)
		noteResponseCompleted()
		partialCapture.Set(session)                                               // ← 挂载点 A（转入工具调用）
		toolCtx := ...
		results := w.executeToolCalls(toolCtx, res.Message.ToolCalls, toolMap)
		if err := firstControlFlowToolError(results); err != nil { ... return nil, err }   // 审批停靠：surface 自己写快照
		...
		for _, tc := range res.Message.ToolCalls {
			...
			session = append(session, toolResultMessage(result))
		}
		partialCapture.Set(session)                                               // ← 挂载点 B（工具结果，steer 投递之前）
		...
		if delivery := TurnInputRuntimeFromContext(ctx).BeginSteerDelivery(ctx); delivery != nil {
			... session = append(session, llm.UserMessage(entry.Parts...))         // ← 暂定的 steer
```

`toolOrchestrationLLM`（`:24-28`）字段：`inner llm.LLM; state *tool.State; home string`；构造：`wrapToolOrchestrationLLM(inner, state)`（`:49`），
唯一调用点 `pkg/run/runner.go:1281` `orchestrated = wrapToolOrchestrationLLM(wrapped, r.tools)`（runner 上有 `r.SessionStore`）。
会话 id：`llm.AgentSessionIDFromContext(ctx)`（subagent 时为它的 worker 会话）；run id：`tool.RunIDFromContext(ctx)`（subagent 时为子 run）。

### 写 transcript 的现有函数

- `SessionStore.AppendMessageSequenceForRun(ctx, sessionID, runID, messages, model, usageJSON)`（`pkg/state/message_sync.go:34`）：幂等对账，丢弃 system/ephemeral，处理压缩替换头部。
- `turn.PersistAssistantTurn`（`pkg/turn/session.go:350-360`）→ `appendAssistantRows`（`:363-400`）：

```go
	resolved, reasoning := AssistantOutcomeText(in.Result)
	...
	if reasoning != "" {
		_, _ = store.AppendMessageForRun(ctx, in.SessionID, in.RunID, "reasoning", reasoning, "", "", state.MessageExecTiming{})
	}
	if in.Result != nil && len(in.Result.Session) > 0 {
		err := store.AppendMessageSequenceForRun(ctx, in.SessionID, in.RunID, in.Result.Session, in.Model, state.MarshalTokenUsage(in.Result.LastResponseUsageCopy()))
		...
		return
	}
```

  注意：这条 `reasoning` 行写在本轮所有新行**之前**，且只有最后一段思考。
- `fb_messages` 的 `exec_*` 列只允许 `tool`/`user` 行（`schema.sql` 的 CHECK），思考耗时要放进 `parts` JSON。
- `MessagePartsJSON(msg, content)` / `ParseMessageParts`（`pkg/state/message_parts.go`）定义 parts 的 JSON 形状；已有 part 类型常量（如 `PartTypeIsMeta`）可作样板。

### 思考流的回调

`llm.StreamSink`（`pkg/llm/stream.go:10-44`）：`OnReasoningDelta(text)`、`OnReasoningDone()`、`OnResponseStarted()`。surface 把自己的 sink 放进 ctx
（TUI `pkg/tui/chat_turn.go:446`；gateway `pkg/gateway/run_control.go:415-434`；subagent `pkg/run/subagent.go:943-970`）。
`WithMutedForegroundStream`（`:82-102`）给内部嵌套调用静音——它们不产生思考行（正确）。

### 显示端（读 reasoning 行）

- web：`frontend/src/composables/useChatStream.ts` 的 `conversationFromTranscript` 把 `reasoning` 行画成 `{kind:'thinking', text, open:false, startedAt: 0}`（约 `:629`）；
  thinking 块类型 `{ kind:'thinking'; ...; startedAt: number; durationMs?: number }`（约 `:946`）。
- TUI 回放：`pkg/tui/commands.go` 回放 transcript 行时如何画 `reasoning` 行——`grep -n '"reasoning"' pkg/tui/commands.go` 定位；思考耗时文字由
  `formatThoughtDurationLabel`（`pkg/tui/reducer.go:1768-1776`，"Thought for 1m 10s"）生成。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... ./pkg/run/... ./pkg/turn/... ./pkg/tui/... ./pkg/gateway/... ./pkg/architecture/... -count=1` | 全部 `ok`（`TestCharacterizationNetworkApproval` 偶发网络超时，单跑通过即可） |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/...`；`gofmt -l pkg` | 无输出 |
| 前端 | `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 无输出 |

## Scope

**In scope**：`pkg/run/orchestration_llm.go`（挂载点与思考记录）、`pkg/run/runner.go`（只改 `:1281` 的构造传入 store）、新文件 `pkg/run/transcript_progress.go` 与测试、
`pkg/turn/session.go`（去掉 turn 结束那条单独的 reasoning 行，见 Step 4）、`pkg/state/message_parts.go`（思考耗时 part）、`pkg/state/*_test.go`、
`pkg/tui/commands.go`（回放思考行带耗时）、`frontend/src/composables/useChatStream.ts`（重载思考块带耗时、共用标签函数）、
`frontend/src/locales/index.ts`（两个文案键改中性名）、`frontend/src/components/chat/SubagentConversation.vue`（改用共用函数）、
`frontend/src/views/ChatView.vue`、`frontend/src/views/WorkshopView.vue`（主视图思考块加标签行）、`frontend/src/views/ChatView.test.ts`、相关测试、`pkg/gateway/dist`（`make ui`）。

**Out of scope**：增量事件是否落库（plan 017）；subagent 视图的数据来源（plan 018）；旧数据迁移（plan 019）；TUI/web 的 live 绘制；
取消路径的 `StreamPartial` 部分写入（保留——它负责"在途块"在取消时落库）。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：transcript 进度写入器

新建 `pkg/run/transcript_progress.go`：

```go
// transcriptProgress writes a run's conversation into its transcript as it
// happens — each text block when it ends — instead of only when the turn
// ends. The transcript is the one place a conversation's words are kept, so a
// block that ended is on disk even if the process stops before the turn does.
// It reconciles through AppendMessageSequenceForRun, which writes only the
// suffix the transcript lacks: the surface's own write at the turn's end, an
// approval gate's snapshot and a cancel's partial write stay idempotent with it.
type transcriptProgressStore interface {
	AppendMessageSequenceForRun(ctx context.Context, sessionID, runID string, messages []llm.Message, model, usageJSON string) error
	AppendStructuredMessageForRun(ctx context.Context, sessionID, runID, role, content, messageID, partsJSON, model, usageJSON, toolStepID, toolMetaJSON string, exec state.MessageExecTiming) (int64, error)
}
```

方法（都容忍 nil store、空 session/run id——直接返回）：
- `flush(ctx, session []llm.Message, model string, usage *llm.Usage)`：`AppendMessageSequenceForRun(ctx, sid, runID, session, model, state.MarshalTokenUsage(usage))`；
  错误只记日志（`slog.Debug`/`Warn`，照该包既有日志风格），**不**中断回答。
- `reasoningDone(ctx, text string, startedAt, endedAt time.Time)`：写一条 `reasoning` 行，`content` 为思考文字，`parts` 为
  `state.ReasoningPartsJSON(text, endedAt.Sub(startedAt))`（Step 2 新增），空文字不写。

`toolOrchestrationLLM` 加字段 `transcript transcriptProgressStore`，新增构造参数或 setter；`runner.go:1281` 传入 `r.SessionStore`（为 nil 时不启用）。
model 名用 orchestration 已知的当前模型（若拿不到，传 `""`——与 `PersistAssistantTurn` 最终那次写入会对账；写入后行的 model 以首次写入为准，
**若发现 model/usage 列因此变得不准确（例如都变成空），STOP 报告**）。

### Step 2：思考耗时 part

`pkg/state/message_parts.go` 加 part 类型 `PartTypeReasoningTiming = "reasoning_timing"` 与：

```go
// ReasoningPartsJSON is a reasoning row's parts: its text and how long the
// model spent thinking it, so a replay says "Thought for 12s" as live did.
func ReasoningPartsJSON(text string, spent time.Duration) string
// ParseReasoningTiming reads that duration back; false when the row has none.
func ParseReasoningTiming(partsJSON string) (time.Duration, bool)
```

（形状照该文件其他 part 的 JSON 写法；`ParseMessage` 对 reasoning 行仍返回 false——它不进模型历史。）

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... -count=1` → `ok`（含 Test plan 1）。

### Step 3：挂到主循环

在 `Execute` 里：
1. **思考块结束**：在 `Execute` 开头，若 ctx 里有 `llm.StreamSinkFrom(ctx)` 且 `w.transcript != nil`，用一个包装 sink 替换 ctx 里的 sink（复制结构体，链式调用原回调）：
   `OnReasoningDelta` 累积文字并记下首个增量时间；`OnReasoningDone` 时调用 `transcript.reasoningDone(...)` 并清空累积。
   **同一次响应里思考转回答时 provider 会调 `OnReasoningDone`**——这就是"思考转回答"的块结束。若某 provider 不调 `OnReasoningDone` 而直接开始 `OnDelta`，
   在首个 `OnDelta` 到来且累积非空时也按"块结束"写一次（防漏）。被 `WithMutedForegroundStream` 静音的嵌套调用拿不到回调，不会写。
2. **挂载点 A**（转入工具调用，`session = append(session, *res.Message); noteResponseCompleted(); partialCapture.Set(session)` 之后）：`transcript.flush(ctx, session, model, latestUsage)`。
3. **挂载点 B**（工具结果追加完、`partialCapture.Set(session)` 之后、steer 投递之前）：`transcript.flush(ctx, session, model, latestUsage)`。
4. **挂载点 C**（`return endTurn(res, latestUsage)` 之前，**仅当**没有进入 `heldAnswer` + 暂定 steer 分支）：把 `*res.Message` 加到一个临时切片
   `append(append([]llm.Message(nil), session...), *res.Message)` 后 flush（不改 `session` 本身，`endTurn` 照旧）。
5. **不要**在暂定 steer（`inFlightSteers`）已追加进 `session` 的位置 flush：暂定 steer 可能被撤回（plan 003 的语义）；它们在下一次响应开始产出后才算送达，
   届时挂载点 A/B/C 会把它们与那次回答一起写入。若读代码发现某挂载点处 `session` 里可能含未确认的暂定 steer → STOP 报告。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run/... -count=1` → `ok`（含 Test plan 2、3）。

### Step 4：turn 结束不再补那条单独的思考行

`pkg/turn/session.go` 的 `appendAssistantRows`：每个思考块已在结束时写入，turn 结束那条"只含最后一段思考"的 `reasoning` 行会造成重复——
在 `in.Result.Session` 非空、走 `AppendMessageSequenceForRun` 的路径上**不再**写它；`Result.Session` 为空的旧路径（`:386-400`，只有一段文字的回答）保持原样。
同时检查 TUI 取消路径（`turn.StreamPartial` 持久化处，`grep -rn 'StreamPartial' pkg/tui pkg/turn pkg/gateway | grep -v _test`）是否也写 `reasoning` 行：
在途思考块（还没 `OnReasoningDone`）由它负责，已结束的块不要再写——若它写的是整轮累积的全部思考，改为只写"最后一个已结束块之后"的部分；
判断不了 → STOP 报告。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/... ./pkg/tui/... ./pkg/gateway/... -count=1` → `ok`（既有测试若断言"每轮一条 reasoning 行"而失败 → STOP，不要直接改断言）。

### Step 5：显示思考耗时——重载/回放读行上的耗时；web 主视图也显示"思考了 N 秒"，标签与 subagent 视图共用

现状：TUI 主视图与 subagent 视图都显示 "Thought for N"（同一个 reducer 函数）；web 只有 subagent 视图显示"思考了 N 秒"
（`frontend/src/components/chat/SubagentConversation.vue:263-268` 的 `thinkingLabel`：`durationMs >= 1000` 时 `t('chat.subagentThoughtFor', { seconds })`，否则 `t('chat.subagentThinking')`；
文案 `frontend/src/locales/index.ts:782,785`（中）与 `:1762,1765`（英））。web 主视图（`frontend/src/views/ChatView.vue:192-197` 与 `frontend/src/views/WorkshopView.vue:45-48`）
把 thinking 块画成纯文本，不显示耗时——与 TUI 主视图不一致。live 时主视图的 thinking 块已经有 `durationMs`（与 subagent 共用 `closeStreamedBlocks`），只是没画。

1. **重载读耗时**：`conversationFromTranscript` 对 `reasoning` 行解析 `reasoning_timing`（Step 2 的 part），写到 thinking 块的 `durationMs`。
2. **共用标签**：把 `thinkingLabel` 的逻辑移到 `frontend/src/composables/useChatStream.ts`，导出
   `export function thinkingBlockLabel(block: Extract<TimelineBlock, { kind: 'thinking' }>, tr: TranslateFn = translate): string`（规则不变：`durationMs >= 1000` →
   "思考了 N 秒"，否则 "思考过程"；未结束的块没有 `durationMs`，显示"思考过程"）。文案键改为中性名：`chat.subagentThoughtFor` → `chat.thoughtFor`、
   `chat.subagentThinking` → `chat.thinkingBlock`（中英两份都改，值不变；**不要用 `chat.thinking`——它已存在，是 `ChatView.vue:178` 打字点的读屏文案"思考中"**；改名前先 `grep -n "'chat.thoughtFor'\|'chat.thinkingBlock'" frontend/src/locales/index.ts` 确认无输出；`grep -rn "subagentThoughtFor\|subagentThinking" frontend/src` 改完应无输出）。
   `SubagentConversation.vue` 改为调用 `thinkingBlockLabel`，显示与今天完全相同。
3. **web 主视图显示**：`ChatView.vue` 与 `WorkshopView.vue` 的 thinking 块，在思考文字上方加一行标签 `{{ thinkingBlockLabel(block, t) }}`，字号与颜色照
   `SubagentConversation.vue` 的 `<summary>`（`text-[11px] text-[var(--forebrain-muted-text)]`）；思考文字本身的渲染（ChatView 的 `reasoningDisplayText`、
   Workshop 的纯文本）与是否折叠**保持不变**——只加标签行，不改成可折叠。
4. **TUI 回放**：画 `reasoning` 行时若有耗时，用 `formatThoughtDurationLabel` 作为标题（与 live 一致，修好今天主视图回放丢秒数的问题——
   今天回放主 agent 的思考用的是行的秒级 `created_at`，一整轮的行同一秒写入，算出来是 0）。

**Verify**：前端测试与 `./pkg/tui/...` 通过（含 Test plan 4、5、6）；`grep -rn "subagentThoughtFor\|subagentThinking" frontend/src` 无输出。

### Step 6：真机

```bash
export FOREBRAIN_RUN_WORK=$TMPDIR/fb-016 FOREBRAIN_RUN_SESSION=fb-016 FOREBRAIN_RUN_PORT=8819
D=.claude/skills/run-forebrain/driver.sh; $D build && $D reset
$D start toolcall; $D submit 'plan something'; $D wait 'Yes, proceed' 25
$D messages     # 此刻（enter_plan_mode 审批停靠中）已能看到 assistant 行，而不是等 turn 结束
$D key Enter; $D wait 'Ask another model' 25; $D messages; $D key Down; $D key Enter; sleep 4; $D messages
```

断言：每个阶段 `$D messages` 的行数单调增加、没有重复行（同一段 assistant 文字只出现一次）；回放（`RESUME_ID=$($D sid) $D start reply`）画面与 live 一致。
若 driver 的 fake provider 有带思考输出的模式（`.claude/skills/run-forebrain/SKILL.md` 查），再跑一次，确认每段思考一条 `reasoning` 行、回放标题为 "Thought for …"。
收尾删除临时目录。

## Test plan

1. `pkg/state`：`ReasoningPartsJSON` / `ParseReasoningTiming` 往返；reasoning 行仍不进 `ListTranscriptMessages`。
2. `pkg/run`：`TestTranscriptIsWrittenAsEachBlockEnds`——照 `pkg/run/orchestration_llm_test.go` 的假 LLM（如 `exitPlanCallLLM`，`:4480`）写一个"先调工具、再回答"的假模型，
   用真实 `SessionStore`（临时 DB，照该文件里建 store 的测试样板）。在工具执行的 handler 里查询 transcript：已有本次回答的 assistant 行（挂载点 A 生效）；
   turn 结束后再调用一次 `PersistAssistantTurn`（模拟 surface），行数不变（幂等）。
3. `pkg/run`：`TestEachReasoningBlockIsItsOwnRow`——假 LLM 两次响应各带一段思考（通过 ctx 里的 sink 调 `OnReasoningDelta`/`OnReasoningDone`），transcript 里两条 `reasoning` 行、
   顺序在各自 assistant 行之前、各带耗时；turn 结束的 `PersistAssistantTurn` 不再追加第三条。
4. 前端：重载含 `reasoning_timing` 的行 → thinking 块 `durationMs` 正确。
5. TUI：回放含耗时的 reasoning 行 → 标题 "Thought for …"。
6. 前端：`thinkingBlockLabel` 单测（≥1s → "思考了 N 秒" / 英文 "Thought for Ns"；<1s 或无耗时 → "思考过程" / "Thinking"）；ChatView 组件测试（照 plan 013 的
   `ChatView.test.ts` 'ChatView conversation bubbles' 搭建）：一条含 `{kind:'thinking', text:'…', open:false, durationMs: 12000}` 的 assistant 消息渲染出"思考了 12 秒"；
   subagent 视图的既有测试（`grep -rln 'subagentThoughtFor\|Thought for' frontend/src --include=*.test.ts`）改用新键后仍通过、显示不变。

## Done criteria

- [ ] Commands 表全部通过
- [ ] Test plan 6 组测试存在且 PASS；`make ui` 成功
- [ ] web 主视图（ChatView、WorkshopView）的思考块显示与 subagent 视图同一个标签（`thinkingBlockLabel`）
- [ ] `grep -n 'transcript.flush(\|transcript.reasoningDone(' pkg/run/orchestration_llm.go` 至少 4 处（A、B、C、思考）
- [ ] Step 6 断言满足，输出摘要写进报告
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符。
2. 某挂载点处 `session` 可能含未确认的暂定 steer（Step 3.5）。
3. 提前写入使 assistant 行的 `model`/`usage_json` 变空或错误，而无法在不改 `AppendMessageSequenceForRun` 语义的情况下补正。
4. 既有测试断言"每轮只有一条 reasoning 行"或"transcript 只在 turn 结束才有行"——停下报告是哪条。
5. 取消路径的部分写入与思考块写入会重复，且无法按 Step 4 区分。

## Maintenance notes

- transcript 是文字的唯一存放处：之后的 plan 017 会停止落增量事件。新增 surface 或新的写入点，都经 `AppendMessageSequenceForRun` 对账，不要直接 `AppendNewMessages` 写回答。
- 思考耗时在 `reasoning` 行的 `parts` 里（`reasoning_timing`），不要放进 `exec_*` 列（CHECK 只允许 tool/user）。
- 写入频率：每个文字块一次、每批工具结果一次，不按 token。
