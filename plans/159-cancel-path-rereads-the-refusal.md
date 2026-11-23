# Plan 159：cancel 路径也要在 join 之后再判一次「无字拒绝」

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "refused := exec != nil && exec.dispatched() && exec.refusedStop()" pkg/run/orchestration_llm.go` 为 `1`；
> `grep -c "results := exec.abort(ctx)" pkg/run/orchestration_llm.go` 为 `1`；
> `grep -c "func (e *streamingToolExecutor) abort" pkg/run/streaming_tools.go` 为 `1`；
> `grep -c "if errors.Is(err, context.Canceled) {" pkg/run/goal.go` 为 `2`。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/run/orchestration_llm.go pkg/run/goal.go pkg/run/streaming_tools.go pkg/run/streaming_tools_test.go pkg/run/goal_test.go` 必须**无输出**。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 2）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1`（超时给足 900 s）。

## Status

- **Priority**: P3（今天没有用户可见后果；但 155 立的不变式只覆盖了一条入口，且**盲目照搬修法会让 /goal 状态回退**）
- **Effort**: S（引擎 3 行 + goal 1 个条件 + 2 个测试）
- **Risk**: LOW（只改 cancel 收尾时返回哪个 sentinel；两处消费者已按"任一"处理）
- **Depends on**: 无（`plans/155` 已完成并落 dev）
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: bug（契约保真 + 一个必须一起改的邻接语义）

## Why this matters

`plans/155` 立的语义：**不带说明的拒绝（`stop=true`）让回合以 `ErrTurnStoppedByRejection` 结束**，不是普通取消。
它修的是"流已结束、调用还在等审批"那条路（`finish` 在 reconcile 之后**重读** `refusedStop()`）。

但同一个拒绝标志还有**第二条入口没被覆盖**：请求被取消时的收尾分支。那里在读标志的**同一行**就做了判定：

```go
refused := exec != nil && exec.dispatched() && exec.refusedStop()        // :559
if exec != nil && exec.dispatched() && (ctx.Err() != nil || errors.Is(err, context.Canceled) || refused) {
	...
	results := exec.abort(ctx)                                           // :564  ← 这里才 join 在飞调用
	...
	if refused {                                                         // :573 用的是 :559 的旧值
		return nil, ErrTurnStoppedByRejection
	}
	return nil, err
}
```

而 `abort` 正是**等待在飞调用落定**的地方（`streaming_tools.go` 的 `abort` → `hold()` → `e.closed = true` +
`wg.Wait()`），拒绝就在那段时间里被记录（`e.refused = true` 在调用 goroutine 里、`stopTurn()` 之前）。
于是：**拒绝落在 cancel 结算窗口内**时，`refused` 读到 `false`，回合以 `context.Canceled` 结束。

今天为什么看不见：三个消费者都把两者当同一件事
（`pkg/run/supervisor.go:78`、`pkg/tui/chat_turn.go:1051`、`pkg/gateway/server.go:2335` 都是
`errors.Is(err, context.Canceled) || errors.Is(err, ErrTurnStoppedByRejection)`）。**但 `pkg/run/goal.go` 不是**：

```go
	status := event.GoalStatusFailed
	if errors.Is(err, context.Canceled) {          // :261 只认 cancel
		status = event.GoalStatusInterrupted
	}
```

所以这条路径今天把 goal 记成 `interrupted`（因为错误是 Canceled）。**一旦只把 sentinel 换成拒绝而不动 goal，
同一个场景就会被记成 `failed`**——那才是真的用户可见回退（/goal 的结束行会显示失败）。
本计划因此有两半：**（A）把判定挪到 join 之后；（B）让 goal 的"用户把回合停下"判定也接受拒绝**。

## Current state

### A) 要改的判定（`pkg/run/orchestration_llm.go:552-580`）

```go
		// A cancelled request whose tools already started settles them here: ...
		// A refusal without words reaches this the same way — its stopTurn
		// cancels the model call with the refusal as the cause, and the stream
		// read can surface that cause itself rather than context.Canceled — so
		// the refusal is matched on the executor's own marker, not the error's
		// shape.
		refused := exec != nil && exec.dispatched() && exec.refusedStop()
		if exec != nil && exec.dispatched() && (ctx.Err() != nil || errors.Is(err, context.Canceled) || refused) {
			if err == nil {
				err = ctx.Err()
			}
			results := exec.abort(ctx)
			prov := collector.result().Message
			...
			if refused {
				// A streamed call the user refused without words: ...
				return nil, ErrTurnStoppedByRejection
			}
			return nil, err
		}
```

### B) 拒绝被记录的位置（`pkg/run/streaming_tools.go:216-241`，已经是对的，不要改）

```go
			if stop {
				e.refused = true
				e.halted = true
				for i, c := range e.calls { ... }     // queued → notRun + 未运行句子
				stopTurn := e.stopTurn
				e.mu.Unlock()
				if stopTurn != nil { stopTurn() }
				return
			}
```

### C) 会因此回退的邻接语义（`pkg/run/goal.go:255-266`）

```go
func (in ctxGoalContinuationInput) roundEnded(round int, err error) {
	var rae *tool.RequiresActionError
	if !in.goal.active() || errors.As(err, &rae) {
		return
	}
	status := event.GoalStatusFailed
	if errors.Is(err, context.Canceled) {
		status = event.GoalStatusInterrupted
	}
	in.endGoal(round, status, "", "")
}
```

## Scope

**In scope（只改这些）**：
- `pkg/run/orchestration_llm.go`（`refused` 在 `exec.abort(ctx)` 之后重读；注释说明为什么）
- `pkg/run/goal.go`（`roundEnded` 也接受 `ErrTurnStoppedByRejection`）
- `pkg/run/streaming_tools_test.go`（新增 1 个回归测试）
- `pkg/run/goal_test.go`（表格里加 1 行）

**Out of scope（看着相关也不要动）**：
- `finish` 的拒绝分流（`plans/155` 已经修好）
- `abort()` / `hold()` / `wg` 的等待语义
- `supervisor.go`、`pkg/tui`、`pkg/gateway`（它们已经按"任一"处理；改成只认一种就是回退）
- `runGoalContinuation` 里 `Evaluate` 失败分支的 `errors.Is(err, context.Canceled)`（那是 check agent
  自己的错误，不是被拒绝的调用；本计划不动它，理由记在 Maintenance notes）
- `ErrTurnStoppedByRejection` 的文案与 resume 路径（`turn.ResumeRejectedStop`）

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 新回归（streaming） | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestRefusalRecordedWhileAbortJoinsStillStopsTheTurn$' -count=3 -v` | 3 次都 `--- PASS` |
| 新回归（goal） | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestGoalWhoseFirstRoundEndsEarlyIsRecordedEnded$' -count=1 -v` | PASS，且新增的 `refused` 子用例在内 |
| 本包回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` | 与基线相比无新增 FAIL |
| 下游回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/gateway ./pkg/turn -count=1` | 与基线相比无新增 FAIL |
| 编译 / vet | `CGO_ENABLED=1 go build -tags fts5 ./...`；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/run` | exit 0 |
| gofmt | `gofmt -l pkg/run` | 无输出 |

## Steps

### Step 1：先写两个会红的测试

**1a. goal 的状态表（确定性红）** —— `pkg/run/goal_test.go` 的
`TestGoalWhoseFirstRoundEndsEarlyIsRecordedEnded`（`:419` 起，用例表在 `:425` 附近）里加一行：

```go
		{"refused without words", ErrTurnStoppedByRejection, event.GoalStatusInterrupted},
```

**1b. 结算窗口里的拒绝（概率性红，见说明）** —— `pkg/run/streaming_tools_test.go` 新增
`TestRefusalRecordedWhileAbortJoinsStillStopsTheTurn`。要点（全部用文件里现成的 helper）：

1. 一个 gated 工具（`streamHookTool` + `inlineGateHook`，照抄 `TestInPlaceRejectionWithoutWordsStopsTheStream`
   的搭法），它开始时返回 `*tool.RequiresActionError`，然后在原地等审批。
2. 假 LLM 的一次尝试：
   - `sink.OnToolCallReady(streamCall("call-a", "gated_a"))`；
   - `waitInlineGate(t, gates, "call-a")`（确认调用已在等审批）；
   - `close(streamReturned)`，然后 **返回 `context.Canceled`**（模拟"流先收尾、拒绝还没落地"）。
3. 另起一个 goroutine：
   ```go
   go func() {
       <-streamReturned
       // 让主循环先走过 refused 的第一次判定（那是 bug 的位置），再落地拒绝。
       time.Sleep(300 * time.Millisecond)
       answerInlineGate(g, "refuse")
   }()
   ```
4. 断言：`Execute` 返回的错误满足 `errors.Is(err, ErrTurnStoppedByRejection)`。

**为什么带 sleep**：这个窗口的构造必须让"拒绝"晚于第一次判定；生产代码里从 `Execute` 返回到那行判定只隔
几条语句，300 ms 是数量级上的保险。所以这是**回归护栏**（修好后稳定 PASS），不是精确的时序证明。

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestGoalWhoseFirstRoundEndsEarlyIsRecordedEnded$' -count=1 -v`
→ FAIL（`refused without words` 子用例得到 `failed`）。
`… -run '^TestRefusalRecordedWhileAbortJoinsStillStopsTheTurn$' -count=3 -v`
→ 3 次都 FAIL（返回的是 `context.Canceled`）。若 3 次都 PASS，STOP 3：说明这条路径已经不可达，
把 `-v` 输出与 `git log -1 --format=%H -- pkg/run/streaming_tools.go` 一起贴回来，不要强行加断言。

### Step 2：判定挪到 join 之后

`pkg/run/orchestration_llm.go`：保留 `:559` 的 `refused`（它是**进入**分支的条件之一），在
`results := exec.abort(ctx)` 之后**重读一次**：

```go
			results := exec.abort(ctx)
			// abort joined every call that was still in flight, so a refusal
			// that landed while it waited is recorded now — and this is the
			// only place left that can report it. Read it again: the value
			// above is what decided to enter this branch, not what happened
			// inside it.
			refused = exec != nil && exec.dispatched() && exec.refusedStop()
```

（`refused` 已在同一作用域声明，**不要**用 `:=`。）

同时把 `:573` 那个 `if refused` 上方的注释补一句"在 abort 之后重读"，不要删掉原有说明。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestRefusalRecordedWhileAbortJoinsStillStopsTheTurn$' -count=3 -v` → 3 次 PASS。

### Step 3：goal 的"用户把回合停下"也接受拒绝

`pkg/run/goal.go` 的 `roundEnded`：

```go
	status := event.GoalStatusFailed
	// A refusal without words is the user stopping the turn, the same way a
	// cancel is (supervisor.settleRunError draws the same line): the round did
	// not fail on its own.
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrTurnStoppedByRejection) {
		status = event.GoalStatusInterrupted
	}
```

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestGoalWhoseFirstRoundEndsEarlyIsRecordedEnded$' -count=1 -v` → PASS（三个子用例都在）。

### Step 4：全包与下游回归

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tui ./pkg/gateway ./pkg/turn -count=1`
→ 与基线相比无新增 FAIL。`plans/155` 的
`TestRejectionAfterTheStreamEndsStillStopsTheTurn`、`TestInPlaceRejectionWithoutWordsStopsTheStream`、
`TestCancelWhileToolsRunWritesInterruptedResults` 必须仍然 PASS。

## Test plan

- `pkg/run/goal_test.go`：表格新增 `{"refused without words", ErrTurnStoppedByRejection, event.GoalStatusInterrupted}`。
- `pkg/run/streaming_tools_test.go`：新增 `TestRefusalRecordedWhileAbortJoinsStillStopsTheTurn`，
  构造"流先返回 Canceled、拒绝在 `abort` 等待期间落地"，断言 `ErrTurnStoppedByRejection`。
- 结构照抄同文件的 `TestInPlaceRejectionWithoutWordsStopsTheStream`（`inlineGateHook` / `waitInlineGate` /
  `answerInlineGate` / `streamCall`）。

## Done criteria

机器可检查，全部满足：

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run` exit 0
- [ ] `gofmt -l pkg/run` 无输出
- [ ] `… -run '^TestRefusalRecordedWhileAbortJoinsStillStopsTheTurn$' -count=3` 3 次 PASS
- [ ] `… -run '^TestGoalWhoseFirstRoundEndsEarlyIsRecordedEnded$'` PASS（含新子用例）
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tui ./pkg/gateway ./pkg/turn -count=1` 与基线相比无新增 FAIL
- [ ] `grep -c "refused = exec != nil && exec.dispatched() && exec.refusedStop()" pkg/run/orchestration_llm.go` 为 `1`
- [ ] `grep -c "errors.Is(err, ErrTurnStoppedByRejection)" pkg/run/goal.go` 为 `1`
- [ ] `git status --porcelain` 只显示 Scope 里的四个文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数 ≠ 1，或 drift check 有输出且被引用函数的语义变了。
2. 基线命令本身跑不起来，拿不到失败清单。
3. 1b 在 Step 1 三次都 **PASS**（说明窗口已不存在或已被别处修好）——不要改 sleep、不要加断言硬凑；
   把三次输出与相关 commit 贴回来。
4. 1a 在 Step 1 直接 PASS（`roundEnded` 已经被改）——重复实施会让语义分叉。
5. 你为了让 1b 变红而去改生产代码里的等待顺序（例如给 `abort` 加 sleep 或测试钩子）——那是把测试塞进生产。
6. goal 的 `Evaluate` 分支（`pkg/run/goal.go:218`）看上去也需要改：**不要顺手改**，先读清"check agent 的错误"
   与"回合自己的错误"的区别，报告你的判断后由评审决定。

## Maintenance notes

- `errors.Is(err, context.Canceled)` 在本仓是"用户把回合停下"的常用简写。任何新增这种判定点时，
  都要问一句"拒绝也算停下吗"；本计划只把 `goal.roundEnded` 补上，其余判定点（`subagent.go:2478`、
  `runner.go:1574`）读的是**别的错误来源**，不要机械扩散。
- 评审重点：Step 2 的 `refused` 重读**不能**删掉第一次判定——它是"流还在跑时就被拒绝、模型调用以拒绝为
  cause 取消"这条路径的入口条件（那时 `ctx.Err()` 可能为 nil）。
- 已知限制：1b 依赖 300 ms 的时序保险，严格意义上不是时序证明。若将来这条分支被重构掉，
  该测试会变成"永远绿"的摆设——重构时必须重新判断它是否还在测东西。
