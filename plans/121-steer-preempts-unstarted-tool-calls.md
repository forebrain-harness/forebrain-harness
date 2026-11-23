# Plan 121：排队的插话在当前工具调用结束后立即送出——本轮还没开始的工具调用不再执行，对用户不可见

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在 dev 工作区。
> 工作区里若有与本计划无关的未提交改动，**不要碰、不要回滚、不要格式化**。
>
> **Drift check（先跑）**：
> `git diff --stat cb4146e -- pkg/run/orchestration_llm.go pkg/run/orchestration_llm_test.go pkg/tool/format.go pkg/tui/commands.go frontend/src/composables/useChatStream.ts`
> 预期**无输出**；若 115–119 已先执行，`orchestration_llm.go`、`useChatStream.ts` 会有它们的改动——按下面的符号与摘录对照现场，`executeToolCalls`、
> `shouldStopAfterToolResult`、`replayToolMessage`、`isPlanReviewDeliveredRow` 的现场与摘录不符即 STOP 1。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED（改变一轮工具调用的执行范围）
- **Depends on**: none（与 115–119 互不依赖）
- **Category**: direction（owner 2026-10-24 定稿）
- **Planned at**: commit `cb4146e`，2026-10-24

## Why this matters

模型一次回复常带多个工具调用。现在排队的插话（steer）要等这一轮**全部**工具调用跑完才被带进下一次模型请求，用户看到的是“等了好几个工具调用才出队”。目标行为：

1. 执行一轮工具调用时，**每启动一个调用（或一组并行调用）之前**检查本回合的插话队列；队列里有插话、且回合未被取消，就不再启动本轮剩下的任何调用。
   正在执行的调用（或正在并行执行的那一组）照常跑完，不中断。批次开始前队列里已有插话时，本轮一个调用都不启动。
2. 没启动的调用各自得到一条只给模型看的结果：`Not run: the user sent a new message before this tool call started. Read the message and call the tool again if it is still needed.`
   随后插话照现有逻辑接在全部工具结果之后发给模型。
3. **对用户完全不可见**：没启动的调用不发任何工具事件、不出卡片；落库的结果行带“回放时整行不画”的标记，TUI 与网页重载后也不画它们。
4. 插话出队与显示的时机（下一次请求开始产出时才移出队列预览）**保持现状**。审批续跑路径（批准后执行剩余调用）同样适用第 1 条。

## Current state

- `pkg/run/orchestration_llm.go:977-1012` — `executeToolCalls`：

```go
	plans := w.state.PartitionToolCalls(names)
	callIndex := 0
	for _, plan := range plans {
		batch := calls[callIndex : callIndex+len(plan.ToolNames)]
		for _, seg := range w.segmentToolCallPlan(ctx, plan, batch, callIndex) {
			if seg.concurrent {
				segResults := results[seg.offset : seg.offset+len(seg.calls)]
				groups := parallelToolCallSummaryGroups(seg.calls, toolMap)
				w.emitParallelBatchSummary(ctx, groups, tool.StepKindToolParallelStarted, nil)
				w.executeConcurrentBatch(ctx, seg.calls, seg.offset, toolMap, results)
				w.emitParallelBatchSummary(ctx, groups, tool.StepKindToolParallelCompleted, segResults)
				if w.settleConcurrentGates(segResults) {
					return results
				}
				continue
			}
			for i, call := range seg.calls {
				results[seg.offset+i] = w.executeOneToolCall(ctx, seg.offset+i, call, toolMap, toolUISuppression{})
				if isRequiresActionError(results[seg.offset+i].err) {
					return results
				}
			}
		}
		callIndex += len(plan.ToolNames)
	}
	return results
```

  调用方：`:488`（主循环，跑完后 `:496-507` 按 ToolCalls 顺序把结果接进会话、`:512` `transcript.flush`、`:532` `BeginSteerDelivery` 取插话）；`:776`（审批续跑，执行 `missing[1:]`）。
- `pkg/run/orchestration_llm.go:42-49` — `orchestratedToolResult{index, call, parts, timing, display, err}`；`:1521-1541` `toolResultMessage`：`err == nil` 时用 `parts`，`display` 写进 `msg.ToolDisplay`。
- `pkg/run/orchestration_llm.go:610-625` — `shouldStopAfterToolResult`：结果里有名为 stop 工具且 `err == nil` 的调用就结束回合（没启动的调用不能触发它）。
- `pkg/run/turn_input.go:380-392` — `(*TurnInputRuntime).HasSteers()`（nil 安全）；`TurnInputRuntimeFromContext(ctx)` 取本回合（子代理是它自己的）运行时。
- “回放时整行不画”的先例：`pkg/tool/format.go:24-30` `PlanReviewDeliveredDisplayKey`；
  `pkg/run/orchestration_llm.go:226-243` 用 `llm.ToolDisplayState{Body: tool.PlanReviewDeliveredDisplayKey, ToolMetaJSON: …}` 落库；
  TUI 重放 `pkg/tui/commands.go:1611-1616`（`replayToolMessage` 里 `isPlanReviewDeliveredDisplay(storedDisplay.Body)` 时返回 `ok=false`），判定函数 `:882-888`；
  网页重放 `frontend/src/composables/useChatStream.ts:279-288`（`PLAN_REVIEW_DELIVERED_KEY`、`isPlanReviewDeliveredRow`），使用点 `:800`（tool 行分支）与 `:833`（只用于 exit-plan 调用）；
  普通调用在 assistant 行分支 `:843-849` 会用 `toolStepFromRow(answer, call)` 画卡——**被跳过的调用在这里也必须不画**。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Go 测试 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" FOREBRAIN_HOME="$(mktemp -d)"; CGO_ENABLED=1 go test -tags fts5 ./pkg/run/... ./pkg/tui/... ./pkg/tool/... -count=1` | 全部 `ok` |
| vet / fmt | 同上环境；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/run/ ./pkg/tui/ ./pkg/tool/`；`gofmt -l pkg/` | 无输出 |
| 前端 | `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts && pnpm typecheck` | 通过 / exit 0 |
| 真机 | `.claude/skills/run-forebrain/driver.sh`（见 Step 6） | 见期望 |

## Scope

**In scope**：`pkg/run/orchestration_llm.go`、`pkg/run/orchestration_llm_test.go`、`pkg/tool/format.go`、`pkg/tui/commands.go`、`pkg/tui/chat_session_test.go`（或 TUI 重放测试所在文件）、
`frontend/src/composables/useChatStream.ts`、`frontend/src/composables/useChatStream.test.ts`、`.claude/skills/run-forebrain/fake_provider.py`（只在 Step 6 需要时加一个模式）。

**Out of scope**：插话的出队/显示时机与 `SteerDelivery` 的提交、回滚、取回逻辑；`segmentToolCallPlan`、`PartitionToolCalls` 的分组规则；
正在执行的工具的中断；TUI/网页的队列预览文案；审批门本身的行为。

## Steps

### Step 1：标记常量与“未执行”结果

1. `pkg/tool/format.go` 在 `PlanReviewDeliveredDisplayKey` 下方加：

```go
// SteerPreemptedDisplayKey is the display body persisted for a tool call the
// run never started because a queued user message took the turn first. No
// surface renders it: the TUI's and the web's replay projections drop the row
// whole, the same way they drop PlanReviewDeliveredDisplayKey.
const SteerPreemptedDisplayKey = "Not run — a queued user message took the turn first."
```

2. `orchestration_llm.go`：`orchestratedToolResult` 加字段 `notRun bool`；新增常量与构造函数（放在 `toolResultMessage` 旁）：

```go
// steerPreemptedResultText is what the model reads for a call skipped because
// the user's queued message went out first.
const steerPreemptedResultText = "Not run: the user sent a new message before this tool call started. Read the message and call the tool again if it is still needed."

func steerPreemptedResult(index int, call llm.ToolCall) orchestratedToolResult {
	metaJSON, _ := json.Marshal(map[string]string{"tool_name": strings.TrimSpace(call.Function.Name), "status": "not_run"})
	return orchestratedToolResult{
		index:   index,
		call:    call,
		parts:   []llm.ContentPart{llm.Text(steerPreemptedResultText)},
		display: &llm.ToolDisplayState{Body: tool.SteerPreemptedDisplayKey, ToolMetaJSON: string(metaJSON)},
		notRun:  true,
	}
}
```

3. `shouldStopAfterToolResult` 的判断改为 `if result.err == nil && !result.notRun && …`。

**Verify**：`CGO_ENABLED=1 go build ./pkg/run/ ./pkg/tool/` → exit 0

### Step 2：`executeToolCalls` 在每次启动前检查插话

在函数开头加：

```go
	// A queued steer takes the turn at the next call boundary: whatever has
	// not started yet is answered "not run" so the steer goes out now instead
	// of after the whole batch.
	preempted := false
	steerWaiting := func() bool {
		if !preempted && ctx.Err() == nil && TurnInputRuntimeFromContext(ctx).HasSteers() {
			preempted = true
		}
		return preempted
	}
```

- 并行段：进入 `if seg.concurrent {` 后第一件事：`if steerWaiting() { for i, call := range seg.calls { results[seg.offset+i] = steerPreemptedResult(seg.offset+i, call) }; continue }`（不发 parallel started/completed 摘要）。
- 串行段：`for i, call := range seg.calls {` 循环体第一行：`if steerWaiting() { results[seg.offset+i] = steerPreemptedResult(seg.offset+i, call); continue }`。
- 其余逻辑不变。`preempted` 一旦为 true 就保持，后续段全部走“未执行”。

**Verify**：`CGO_ENABLED=1 go build ./pkg/run/` → exit 0；`grep -n "steerWaiting()" pkg/run/orchestration_llm.go` → 2 处调用

### Step 3：引擎测试（`pkg/run/orchestration_llm_test.go`）

写法参照同文件 `TestToolOrchestrationConsumesPendingSteerBeforeNextSampling`（`:5266` 一带：脚本化模型 + 工具 + `WithTurnInputRuntime`）。新增：

1. `TestQueuedSteerPreemptsUnstartedSerialCalls`：模型第 1 次回复 3 个**串行**工具调用（用非只读的测试工具，确保不被并行分组）；第 1 个工具执行时向运行时 `Enqueue(TurnInputModeSteer, …)`。
   断言：只有第 1 个工具真正执行；第 2 次模型请求收到的消息依次是 assistant（3 个 tool_calls）、3 条 tool 结果（第 2、3 条内容等于 `steerPreemptedResultText`）、插话 user 消息。
2. `TestQueuedSteerLetsTheRunningParallelGroupFinish`：一组 2 个只读可并行调用 + 1 个串行调用；插话在并行组执行中入队 → 并行组 2 个都执行完，第 3 个未执行。
3. `TestSteerQueuedBeforeTheBatchSkipsEveryCall`：批次开始前已入队 → 一个工具都不执行，3 条结果都是“未执行”。
4. `TestCancelledRunDoesNotPreempt`：ctx 已取消时不产生“未执行”结果（沿用现有取消行为）。
5. `TestNotRunResultNeverStopsTheTurn`：stop 工具（`stopAfterToolResultKey` 指定的名字）被跳过时回合不提前结束。
6. `TestNotRunResultPersistsTheDropKey`：带进度存储（照 `newProgressStore` 用法）跑第 1 条场景后，被跳过调用的 tool 行 `ParseToolDisplayPart` 的 Body 等于 `tool.SteerPreemptedDisplayKey`。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -run 'Steer|NotRun|Preempt' -count=1` → `ok`，且同文件既有的 steer 测试（`TestToolOrchestrationConsumesPendingSteer*`、`TestFailedSamplingReturns*`、`TestRetract*` 等）全部仍通过

### Step 4：TUI 重放不画被跳过的调用

`pkg/tui/commands.go`：新增

```go
// isHiddenToolDisplay reports whether a stored tool display is one no surface
// draws: a delivered review's handoff, or a call a queued message took the
// turn from before it started.
func isHiddenToolDisplay(body string) bool {
	body = strings.TrimSpace(body)
	return body == tool.PlanReviewDeliveredDisplayKey || body == tool.SteerPreemptedDisplayKey
}
```

`replayToolMessage` 里 `isPlanReviewDeliveredDisplay(storedDisplay.Body)` 改为 `isHiddenToolDisplay(storedDisplay.Body)`（`:912` 审批确认行那处保持 `isPlanReviewDeliveredDisplay` 不变）。

测试：照 `pkg/tui/commands_test.go:2540-2580` 一带的写法（`toolRowWithDisplay(3, "call-exit-a", tool.PlanReviewDeliveredDisplayKey, …)`）加一例：
assistant 行带 2 个 tool_calls、其中 1 个的 tool 行用 `toolRowWithDisplay(…, tool.SteerPreemptedDisplayKey, `{"tool_name":"shell","status":"not_run"}`)` → 重放只画另一个调用的卡，被跳过的调用**没有任何卡**（包括“canceled”卡）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/ -run 'Replay|PlanReview|Preempt' -count=1` → `ok`

### Step 5：网页重放不画被跳过的调用

`useChatStream.ts`：

1. 在 `PLAN_REVIEW_DELIVERED_KEY` 旁加 `export const STEER_PREEMPTED_KEY = 'Not run — a queued user message took the turn first.'`（注释：必须与 `tool.SteerPreemptedDisplayKey` 逐字一致），
   以及 `function isSteerPreemptedRow(row): boolean`（照 `isPlanReviewDeliveredRow` 写法比较 body）。
2. tool 行分支（`:800` 一带）：`if (isSteerPreemptedRow(row)) return`。
3. assistant 行的调用循环（`:843` 一带普通调用分支，在 `drawn.add(call.id)` 之后、画卡之前）：`if (answer && isSteerPreemptedRow(answer)) continue`。
4. `useChatStream.test.ts`：照已有的 delivered-review 重放用例（`grep -n "PLAN_REVIEW_DELIVERED_KEY" frontend/src/composables/useChatStream.test.ts`）加一例：
   历史行里一个调用的 tool 行 parts 带 `STEER_PREEMPTED_KEY` → 时间线没有它的 tool 块；同一 assistant 行的另一个调用照常有卡。

**Verify**：前端单测通过；`pnpm typecheck` exit 0

### Step 6：真机验收（TUI）

1. 若 `.claude/skills/run-forebrain/fake_provider.py` 没有“一次回复多个 shell 调用”的模式，新增 `multishell`：第 1 次请求回复 3 个 `shell` 调用（命令依次 `sleep 3; echo one`、`echo two`、`echo three`），之后的请求回复固定文本 `steer received`；
   写法照该文件 `shell` 模式。并在 `SKILL.md` 的模式表加一行说明。
2. 跑：

```bash
D=.claude/skills/run-forebrain/driver.sh
$D start multishell
$D submit 'run the three commands'
$D wait 'sleep 3' 15          # 第 1 个调用开始执行
$D submit 'stop and say hi'   # 运行中排队一条插话
$D wait 'steer received' 30
$D screen    # 期望：只有 "sleep 3; echo one" 一张工具卡；没有 echo two / echo three 的卡；插话作为用户消息出现在它之后
$D messages  # 期望：有 3 条 tool 行，后 2 条内容为 "Not run: the user sent a new message…"
$D stop
```

3. 重载验证：`$D start reply` 后用 `/resume` 打开刚才的会话（按 SKILL.md 的会话切换方法），`$D screen` 仍只有一张工具卡。

**Verify**：上面期望全部满足，`screen` 输出附在报告里。

## Done criteria

- [ ] Step 3 的 6 个测试与 Step 4、5 的重放测试通过；`pkg/run`、`pkg/tui`、`pkg/tool` 全部测试 `ok`
- [ ] `go vet`、`gofmt -l pkg/` 干净；前端单测与 `pnpm typecheck` 通过
- [ ] 真机：被跳过的调用在运行中与重载后都没有卡片
- [ ] `git status --short` 只含 Scope 内文件

## STOP conditions

1. Drift check 摘录与现场不符。
2. 现有 steer 相关测试（Step 3 Verify 列出的）因本改动失败，且原因不是“本轮剩余调用不再执行”这一预期变化——回报失败用例名与原因。
3. TUI 或网页重放对被跳过的调用仍画出卡片（例如 TUI 在别处为“有 call 无可画结果”的调用补画 canceled 卡）——回报画卡的代码位置，不要另加第三处判定。
4. 审批续跑路径（`:776`）的既有测试失败。
5. 某一步验证连续两次修正后仍失败。

## Maintenance notes

- “未执行”只在启动前判断；以后若加入“流式中途就开始执行工具”，判断点要跟着移到执行队列。
- `SteerPreemptedDisplayKey` 与前端 `STEER_PREEMPTED_KEY` 必须逐字一致；新增重放面（例如别的客户端）时要同样整行丢弃。
- 模型看到的“未执行”文案是唯一提示；如果发现模型频繁不重发被跳过的调用，调这句文案即可。
