# Plan 155：「拒绝并停下」必须真的停下——拒绝落在流结束之后也要结束回合（引擎）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置 1（否则 STOP 1）——目标文件必须干净**：
> `git status --porcelain pkg/run/streaming_tools.go pkg/run/orchestration_llm.go` 必须**无输出**。
>
> **前置 2（否则 STOP 1）——锚点仍在**（行号以本计划的基线为准，以**符号**为准）：
> `grep -c "func (e \*streamingToolExecutor) finish" pkg/run/streaming_tools.go` 为 `1`；
> `grep -c "func (e \*streamingToolExecutor) refusedStop" pkg/run/streaming_tools.go` 为 `1`；
> `grep -c "refused := exec != nil && exec.dispatched() && exec.refusedStop()" pkg/run/orchestration_llm.go` 为 `1`；
> `grep -c "errors.Is(err, tool.ErrApprovalRejectedStop)" pkg/run/orchestration_llm.go` 为 `1`。
>
> **Drift check（先跑）**：`git diff --stat bca0157 -- pkg/run/orchestration_llm.go pkg/run/streaming_tools.go`
> 必须**无输出**。有输出就按下文"现状"里的**函数名**逐段核对；被引用的代码含义变了即 STOP 1。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 5）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tui ./pkg/gateway ./pkg/turn -count=1`（超时给足 900 s）。
>
> 基线里已经红的是存量问题，**不是你要修的**；Done criteria 只要求"不新增红"。

## Status

- **Priority**: P1（129/127 的核心语义没有兑现；owner 2026-10-26 真机验收发现）
- **Effort**: S（引擎 8~15 行 + 1 个回归测试）
- **Risk**: MED（改的是"回合要不要继续"的判定；两个 surface 共用）
- **Depends on**: 无（`plans/145` 独立，可任意顺序）
- **Blocks**: 无
- **Planned at**: dev `bca0157`，2026-10-26（137–150 批次已落 dev，工作区干净）
- **Category**: bug（拒绝语义）

## Why this matters

129 定稿的语义：**不带说明**拒绝（`cancel`、Esc、Ctrl+C、空说明回车）= 这个调用读 STOP 句子、它后面的调用不再开始、
**回合就地结束，不再发模型请求**。真机上它没有发生。

2026-10-26 真机取证（终端与网页都复现）：

- 终端（`driver.sh` 的 `streamapprove` 场景，假模型）：按 `3`(No) → `Enter` 进说明行 → `Esc`。屏幕顺序是
  ```
  ● checking
  ✗ You canceled the request to run echo approved-run
  ● Denied echo approved-run
    └ (no output)
  ● tick 1 tick 2 tick 3 tick 4 tick 5 tick 6     ← 流照常跑完
  ● after approval                                 ← 引擎又发了一次模型请求
  ```
  状态库：`fb_actions.status=cancelled`，**一个** `turn_started`、**一个** `turn_completed`（**没有** `turn_cancelled`），
  debug 日志里两次模型请求。
- 网页（真 glm-5.3-flash）：同样的操作之后 `approval_resolved.decision=cancelled`，随后**又出现一次** assistant 回答
  （模型说"第二步被拒绝了，我已停止执行……请告诉我接下来如何处理"），事件序列以 `turn_completed` 收尾。

用一份 /tmp 打点副本（不动工作区）复现，日志顺序是**根因**的直证：

```
16:02:00.145 [stream-end] refused=false err=<nil>      ← 流读完之后只在这里看过一次拒绝标志
16:02:00.152 [reconcile] tools executed in-stream but no tool calls reported
16:02:02.958 [branch] which=REJECTED-STOP stop=true     ← 2.8s 后 hook 才回来：stop=true
16:02:02.962 [pump] which=STOP refused=false
（之后没有 [loop] STOPPED-TRUE —— 这个 stop 被丢掉了）
```

也就是说：用户思考几秒再点拒绝是非常正常的操作，但**只要这个拒绝是在响应正文已经结束之后才落地**（调用还在等审批），
`stop=true` 就再也没人读，回合继续、模型被再问一次。

## Current state

### 拒绝信号产生的地方（已经是对的，不要改）

- `pkg/run/orchestration_llm.go:1801` `runCallInPlace`：hook 返回 `tool.ErrApprovalRejectedStop` 时
  ```go
  case errors.Is(err, tool.ErrApprovalRejectedStop):
      // Refused without words (plan 129's semantics, answered here):
      // this call reads the stop sentence, the calls behind it never start,
      // and the turn ends without another model request.
      return w.deniedToolResult(ctx, index, call, tool.RejectedStopText, ""), true
  ```
  （`true` 就是"停回合"。这句注释是本计划的契约。）
- 两个 surface 都按这个返回：`pkg/tui/chat_surface.go` 的 `answerInPlaceApproval`（`decision.Cancelled` → `tool.ErrApprovalRejectedStop`）、
  `pkg/gateway/approval.go` 的 `promptGatewayInlineApproval`（轮询到 `state.ActionCancelled` → `tool.ErrApprovalRejectedStop`）。

### 信号被丢掉的地方（本计划要改）

- **唯一的读取点**：`pkg/run/orchestration_llm.go:559`
  ```go
  refused := exec != nil && exec.dispatched() && exec.refusedStop()
  if exec != nil && exec.dispatched() && (ctx.Err() != nil || errors.Is(err, context.Canceled) || refused) {
      ...
      if refused {
          return nil, ErrTurnStoppedByRejection
      }
      return nil, err
  }
  ```
  它在**流读取返回的那一刻**求值。流正常结束（`err == nil`、`ctx.Err() == nil`）时整段不进入。
- 流式执行器把"这个调用被拒绝"记在 `e.refused` 上（`pkg/run/streaming_tools.go:218-241`）：
  ```go
  if stop {
      e.refused = true
      e.halted = true
      for i, c := range e.calls { ... }   // 当时还在 queued 的调用改成 notRun + 未运行句子
      ...
      stopTurn()                          // 取消模型调用——流已经结束，这一步什么也没取消
      return
  }
  ```
- 然后主循环走到 `pkg/run/orchestration_llm.go:767-783`：
  ```go
  if exec != nil && exec.dispatched() {
      results, stopped = exec.finish(toolCtx, res.Message.ToolCalls)
  } else {
      results, stopped = w.executeToolCalls(toolCtx, res.Message.ToolCalls, toolMap)
  }
  if stopped {
      for _, result := range results { session = append(session, toolResultMessage(result)) }
      partialCapture.Set(session)
      transcript.flush(...)
      return nil, ErrTurnStoppedByRejection
  }
  ```
  这条 `if stopped` 分支**完全正确**，问题是 `finish` 不会把 `true` 交出来。
- `pkg/run/streaming_tools.go:326` `finish`：
  ```go
  func (e *streamingToolExecutor) finish(ctx context.Context, finalCalls []llm.ToolCall) ([]orchestratedToolResult, bool) {
      finalCalls = e.reconcile(finalCalls)
      ...
      for i, call := range finalCalls {
          c, ok := byID[call.ID]
          if ok && (c.state == streamedCallDone || c.state == streamedCallNotRun) {
              result := c.result          // 被拒绝的调用是 Done，结果照收……
              result.index = i
              results[i] = result
              continue                    // ……但没有任何地方把"它被拒绝了"变成 stopped=true
          }
          rest = append(rest, call)
          restAt = append(restAt, i)
      }
      if len(rest) > 0 {
          restResults, stopped := e.w.executeToolCalls(ctx, rest, e.toolMap)   // 只有这里能产出 stopped
          ...
          if stopped { return results, true }
      }
      return results, false
  }
  ```
  `stopped=true` 只在 `executeToolCalls` 里产生（其内部对 `runCallInPlace` 的第二个返回值 `stop` 汇总）；
  一个在流里就已经执行完（`streamedCallDone`）的调用不会走那条路，于是拒绝被降级成"一条失败的普通工具结果"。

### 顺带一条必须一起防的坑

`finish` 的 `rest` 列表包含 **`streamedCallQueued` 的调用**——也就是排在拒绝之后、本该"永不开始"的调用。
如果只在 `finish` 末尾补一句 `if e.refusedStop() { return results, true }`，那些调用**已经**被 `executeToolCalls`
执行掉了，129 的"后面的调用不再开始"仍然被违反。所以修复必须在跑 `rest` **之前**分流。

## 要做成的样子（行为变化矩阵）

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| 流**还在跑**时用户拒绝（无说明） | 流被取消 → `refusedStop()` → `ErrTurnStoppedByRejection`（**已正确**） | 不变 |
| 流**已结束**、某个已派发的调用仍在等审批，用户此时拒绝（无说明） | 拒绝被当成普通工具结果；回合继续；**又发一次模型请求**；`turn_completed` | 该调用读 STOP 句子；排在它后面的调用得到"未运行"句子且**从不执行**；回合以 `ErrTurnStoppedByRejection` 结束；**零新模型请求**；`turn_cancelled` |
| 拒绝时后面还有没派发的调用 | 取决于时序（有时被执行） | **一律不执行** |
| 带说明拒绝（`deny` / 有字的拒绝） | 只拒这一个调用，回合继续 | **不变**（`stop=false`，不要动） |
| 批准 | 原地重跑 | **不变** |
| 子代理的审批、exit_plan_mode | 各自既有路径 | **不变**（`runCallInPlace` 对它们原样返回） |

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 定向测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^<名字>$' -count=1 -v` | 看到 `--- PASS: <名字>`（**必须看到 PASS 行**） |
| 回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tui ./pkg/gateway ./pkg/turn -count=1` | 与基线相比无新增 FAIL |
| 编译 / vet | `CGO_ENABLED=1 go build -tags fts5 ./...`；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/run` | exit 0 |
| gofmt | `gofmt -l pkg` | 无输出 |
| 真机（终端） | `.claude/skills/run-forebrain/driver.sh` 的 `streamapprove`（见 Step 4） | 流被打断、没有 `after approval`、屏幕收尾为 cancel |

## Scope

**In scope（只改这些）**：
- `pkg/run/streaming_tools.go`（`finish` 的拒绝分流）
- `pkg/run/streaming_tools_test.go`（新增 1 个回归测试）
- 必要时 `pkg/run/orchestration_llm.go` 的一行注释（说明 `finish` 现在也报告拒绝）——不改逻辑

**Out of scope（看着相关也不要动）**：
- `runCallInPlace` 的 `stop` 语义、`deniedToolResult`、`rejectionStoppedResult`、`RejectedStopText`
- `orchestration_llm.go:559` 的流结束分支（它负责"流还在跑就被拒绝"，已正确；不要把它删掉或搬走）
- `pkg/tui`、`pkg/gateway`（两个 surface 的接线都对；本计划只改引擎）
- `pkg/turn` 的 `ResumeRejectedStop` / 拒绝续跑路径（parked 路径）
- 带说明拒绝（`deny`）的任何行为
- 网页前端（`frontend/`）

## Steps

### Step 1：先写会红的回归测试

在 `pkg/run/streaming_tools_test.go` 里，照已有的 `TestInPlaceRejectionWithoutWordsStopsTheStream`
（同文件，搜 `inlineGateHook`）加一个**拒绝晚于流结束**的用例，名字取
`TestRejectionAfterTheStreamEndsStillStopsTheTurn`。要点（用文件里已有的 `inlineGateHook`/`waitInlineGate`/
`answerInlineGate`、`newProgressStore`、`streamHookRunner`、`streamFailLLM` 等helper）：

1. 两个工具：`gated_a`（走 `st.ActionHook()`，pending 时返回 `*tool.RequiresActionError`）与
   `queued_b`（计数，永远不该被调用）。
2. 假的流：`sink.OnToolCallReady(streamCall("call-a", "gated_a"))` 之后 `waitInlineGate`（确认调用已开始、正在等审批），
   再 `sink.OnToolCallReady(streamCall("call-b", "queued_b"))`，然后**直接返回**（流到此结束）；
   用一个 `streamDone := make(chan struct{})` 在返回前 `close`，让测试知道"流已经结束"。
3. 测试体：`<-streamDone`（必要时 `time.Sleep(100*time.Millisecond)` 确保主循环已走过 `orchestration_llm.go:559`），
   然后 `answerInlineGate(g, "refuse")`。
4. 断言：
   - `Execute` 返回的错误满足 `errors.Is(err, ErrTurnStoppedByRejection)`（同包，直接比）；
   - 假 LLM 只被请求**一次**（`streamFailLLM` 的 attempts 用完后应报错或计数——按该 helper 现有能力断言）；
   - `queued_b` 的执行计数为 `0`；
   - `queued_b` 的 tool result 文本是 `tool.RejectionStoppedDisplayKey` 那一类"未运行"句子
     （按文件里既有断言方式取 session 里的 tool 消息）；
   - `gated_a` 的结果文本是 `tool.RejectedStopText`。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestRejectionAfterTheStreamEndsStillStopsTheTurn$' -count=1 -v`
→ `--- FAIL`（当前实现返回 nil 错误 / `queued_b` 被执行 / 有第二次请求）。若直接 PASS，STOP 2。

### Step 2：`finish` 在跑剩余调用之前先看拒绝

`pkg/run/streaming_tools.go` 的 `finish`，在 `byID` 收集完、`rest`/`restAt` 分流**之前**插入：

```go
	// A refusal without words can land after the response body ended: the
	// call was streamed, it was started in-stream, and it waited on its
	// approval while the rest of the stream arrived. By the time it answers,
	// the stream-end check above (orchestration_llm's refusedStop read) has
	// already run, so this is the only place left that can report it. Nothing
	// behind the refused call may start — that is what "refuses and stops"
	// means (plan 129) — and the caller ends the turn on this bool without
	// asking the model again.
	if e.refusedStop() {
		for i, call := range finalCalls {
			c, ok := byID[call.ID]
			if ok && (c.state == streamedCallDone || c.state == streamedCallNotRun) {
				result := c.result
				result.index = i
				results[i] = result
				continue
			}
			results[i] = rejectionStoppedResult(i, call)
		}
		return results, true
	}
```

（这段必须放在 `e.mu.Unlock()` 之后、`if len(rest) > 0 {` 之前；`rejectionStoppedResult` 同文件已有。）

**Verify**：
- Step 1 的用例 `--- PASS`；
- `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^(TestInPlaceRejectionWithoutWordsStopsTheStream|TestRejectionAfterTheStreamEndsStillStopsTheTurn)$' -count=1 -v`
  → 两条 `--- PASS`（前者是"流还在跑就被拒绝"，**必须仍然绿**）；
- `gofmt -l pkg` 无输出。

### Step 3：变异验证（证明新用例有区分度）

临时把新加的那段删掉（只留原 `finish`），跑 Step 1 → 必须 `--- FAIL`；**立刻还原**，再跑 → `--- PASS`。
再做一个反向变异：把 `if e.refusedStop()` 改成 `if false && e.refusedStop()`，同样必须红。

**Verify**：还原后 `git diff pkg/run/streaming_tools.go` 只含 Step 2 的新增块。

### Step 4：真机验收（终端，假模型）

刻意复现"慢慢想再拒绝"的时序（拒绝落在 tick 流结束之后）：

```bash
D=.claude/skills/run-forebrain/driver.sh
$D build
$D reset
$D start streamapprove 'ok'
$D submit 'go'
$D wait 'needs approval' 30
sleep 8                      # 关键：等 tick 流（6 秒）先结束，再回答
$D key 3; $D key Enter; $D key Escape
sleep 3
$D screen > "$TMPDIR/p146-screen.txt"; cat "$TMPDIR/p146-screen.txt"
grep -c 'after approval' "$TMPDIR/p146-screen.txt"
grep -c 'tick 6' "$TMPDIR/p146-screen.txt"
$D provider-log | grep -c 'REQUEST'
$D stop
```

**Verify**：
- `after approval` 计数为 `0`（修复前是 1）；
- `provider-log` 的 `REQUEST` 计数为 `1`（只有第一回合的那一次；修复前是 2）；
- 屏幕上该调用是拒绝卡，后面没有新的 assistant 回答；`$D stop` 正常退出。
- 若驱动起不来，按 `SKILL.md` 的 Troubleshooting 处理；仍不行 STOP 4 报告，**不要**跳过。

## Test plan

- 新增 `pkg/run` `TestRejectionAfterTheStreamEndsStillStopsTheTurn`：拒绝晚于流结束 → 回合结束、零新请求、
  后面的调用不执行且读"未运行"句子、被拒调用读 `RejectedStopText`。
- 回归：`TestInPlaceRejectionWithoutWordsStopsTheStream`（流内拒绝仍绿）、`./pkg/run ./pkg/tui ./pkg/gateway ./pkg/turn` 整包。
- 变异：删掉新块 / 把条件改成 `false &&` 都必须变红。
- 真机：Step 4（这才是 owner 报的那个时序）。

## Done criteria

- [ ] 新用例 `--- PASS`，`TestInPlaceRejectionWithoutWordsStopsTheStream` 仍 `--- PASS`
- [ ] 两次变异各自变红、还原后全绿
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tui ./pkg/gateway ./pkg/turn -count=1` 与基线相比无新增 FAIL
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0；`go vet -tags fts5 ./pkg/run` exit 0；`gofmt -l pkg` 无输出
- [ ] `grep -c 'e.refusedStop()' pkg/run/streaming_tools.go` 为 `1`（`finish` 里新增的那个读取点；`refusedStop` 自己的定义不算）
- [ ] Step 4 的 `after approval` 计数 0、`REQUEST` 计数 1
- [ ] `git status --porcelain` 里本计划新增的改动只落在 In scope 的文件
- [ ] `plans/README.md` 状态行已更新（除非评审方说由其维护）

## STOP conditions

1. 前置锚点不满足，或"现状"里按函数名找到的代码含义已变（例如 `finish` 已经自己汇总拒绝、或 `runCallInPlace` 不再返回 stop）。
2. Step 1 的新用例在改代码前就通过（说明这条时序已被覆盖或测试没造出"拒绝晚于流结束"）。
3. `streamFailLLM` / `inlineGateHook` 这套 helper 无法造出该时序（例如 hook 只能同步回答）——回报，不要自己另起一套测试骨架。
4. Step 4 的驱动反复起不来，或屏幕上仍有 `after approval`、`REQUEST` 仍为 2。
5. 基线跑不起来（编译失败、依赖缺失）。
6. 需要改 In scope 以外的文件（尤其 `pkg/tui`/`pkg/gateway`/`frontend/`）。

## Maintenance notes

- "拒绝要不要停回合"现在有两个判定点，按**时序**分工：`orchestration_llm.go` 的流结束读取负责
  "流还在跑就被拒绝"，`streamingTools.finish` 的读取负责"流已结束、调用仍在等审批时才被拒绝"。
  以后新增流式执行入口时，两处都要覆盖，否则又会出现"用户想了几秒，拒绝就不生效"。
- `stop` 是**粘性事实**（`e.refused`），不要在 `finish` 里清掉它：`finish` 之后 `abort`/快照路径仍会读。
- 带说明的拒绝（`deny`）必须保持 `stop=false`：它的语义是"只拒这个调用、回合继续"，
  卡上的两行正文由 `plans/145` 负责。
- 排在被拒调用后面的调用，结果一律是 `rejectionStoppedResult`（隐藏行 + "未运行"句子），
  不要改成真正的执行错误——那会让模型以为调用跑过失败。
