# Plan 163：流式执行器两个没人管的兜底分支——补测试、把日志降到 WARN、把语义写准

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "func (e \*streamingToolExecutor) reconcile" pkg/run/streaming_tools.go` 为 `1`；
> `grep -c 'slog.Error("tools executed in-stream but the response reports no tool calls")' pkg/run/orchestration_llm.go` 为 `1`；
> `grep -c "baseCtx := e.beforeStart(readyUpTo)" pkg/run/streaming_tools.go` 为 `1`；
> `grep -c "func wrapToolOrchestrationLLMWithProgress" pkg/run/orchestration_llm.go` 为 `1`。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/run/streaming_tools.go pkg/run/orchestration_llm.go pkg/run/streaming_tools_test.go` 必须**无输出**。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 2）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1`（超时给足 900 s）。

## Status

- **Priority**: P3（两处都是"能自愈"的兜底，但一处会进告警、两处都没有护栏）
- **Effort**: S（2 个测试 + 1 行日志级别 + 2 处注释）
- **Risk**: LOW（**不改任何行为**；本计划明确禁止改行为）
- **Depends on**: 无
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: tests（+ 日志级别 / 文档）

## Why this matters

流式工具执行器（`pkg/run/streaming_tools.go`）是"调用在流里就跑"的核心，它有两条**没人测**的兜底：

**1. `reconcile`：流里跑过的调用没出现在最终 response 的 tool_calls 里。**
不同 provider 的 ready 列表与 final 列表不总一致（compat 适配器会从最后一帧合成调用）。这条分支把缺失的调用
**补进** response，使"已经产生副作用的调用"仍然能被回答成 tool result——否则下一轮请求里 `tool_calls` 悬空，
模型看不到自己那次调用的结果（只能靠 `RepairDanglingToolResults` 把整行剥掉）。

它是可自愈的，却**用 `slog.Error` 记录**（`pkg/run/orchestration_llm.go:682`）——线上出现即进告警，
而同一个动作在 `reconcile` 内部只打 `slog.Warn`（`streaming_tools.go:313`）。而且 `pkg/run` 的所有测试里
没有任何用例覆盖这条分支（`grep -rn 'reconcile(' pkg/run/*_test.go` 无输出）。

**2. `beforeStart` 返回 nil：调用不能开始。**
唯一的真实来源是"用户撤回了在飞的 steer"（`orchestration_llm.go:491-497`：`Commit()` 后 `Retracted()` 为真）。
这时分支把 `state` 从 `executing` 改回 `queued` 并 `halted = true`——**但不清 `started`**，
于是 `dispatched()`（`:273-282`）对一个从未运行过的调用返回 `true`。`started` 的注释写的是
"set once … when the call moves to executing: dispatched reports it even after the call settles"，
读起来像"真的开始过"，实际含义是"提供方已经看见了这个调用"（`MarkToolDispatched()` 已经为此写进了
进度快照——重发不会重复它）。

**本计划不改行为**：只补两个护栏、把 ERROR 降到 WARN、把这两处注释改成与代码一致。

## Current state

`pkg/run/streaming_tools.go:294-320`（`reconcile`）：

```go
func (e *streamingToolExecutor) reconcile(final []llm.ToolCall) []llm.ToolCall {
	e.hold()
	e.mu.Lock()
	defer e.mu.Unlock()
	known := make(map[string]bool, len(final))
	for _, call := range final { known[call.ID] = true }
	for _, c := range e.calls {
		if !c.started && c.state != streamedCallNotRun { continue }
		if known[c.call.ID] { continue }
		slog.Warn("streaming tool execution outran the response's tool calls; appending the call it ran",
			"tool_name", c.call.Function.Name, "call_id", c.call.ID)
		final = append(final, c.call)
		known[c.call.ID] = true
	}
	return final
}
```

`pkg/run/orchestration_llm.go:676-684`（调用点，**ERROR 在这里**）：

```go
		// A response that reports no tool calls cannot have dispatched any; if
		// one somehow did, the calls it already ran are still its own.
		if exec != nil && exec.dispatched() && res != nil && res.Message != nil {
			slog.Error("tools executed in-stream but the response reports no tool calls")
			res.Message.ToolCalls = exec.reconcile(res.Message.ToolCalls)
		}
```

`pkg/run/streaming_tools.go:165-190`（`beforeStart` 的 nil 分支）：

```go
		call.state = streamedCallExecuting
		call.started = true
		readyUpTo := e.readyUpToLocked(call)
		e.wg.Add(1)
		e.mu.Unlock()
		e.progress.MarkToolDispatched()
		baseCtx := e.beforeStart(readyUpTo)
		if baseCtx == nil {
			e.mu.Lock()
			call.state = streamedCallQueued
			e.halted = true
			e.mu.Unlock()
			e.wg.Done()
			return
		}
```

`started` 的注释（`:56-58`）：

```go
	// started is set once, under the executor's lock, when the call moves to
	// executing: dispatched reports it even after the call settles.
	started bool
```

测试脚手架（`pkg/run/streaming_tools_test.go`，全部现成）：`newProgressStore(t, sid)`、
`wrapToolOrchestrationLLMWithProgress(inner, st, "", store, func(context.Context) string { return "gpt-main" })`、
`streamFailLLM{attempts: []func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error)}`（记录
`inner.received[]` 的请求历史）、`streamCall(id, name)`、`streamTestSink()`、`toolResultMessages(msgs)`、
`toolpkg.NewState(t.TempDir())`。范例：`TestCallsNotSeenAsReadyRunAfterTheStream`（`:670`）。

## Scope

**In scope（只改这些）**：
- `pkg/run/streaming_tools.go`（**只改注释**：`started` 的说明 + nil 分支的说明）
- `pkg/run/orchestration_llm.go`（**只改一行日志级别** + 它上面那句注释里"cannot have dispatched any"的措辞）
- `pkg/run/streaming_tools_test.go`（新增 2 个测试）

**Out of scope（看着相关也不要动）**：
- `reconcile` 的补行逻辑、`finish`/`abort` 的拒绝分流（`plans/155` 已定稿）
- `started` 的值本身（**不要**在 nil 分支里清它——见 Maintenance notes，那会改变取消路径选哪条分支）
- 进度快照的 `MarkToolDispatched()`（它保证重发不会重复已派发的调用，是正确的）
- `RepairDanglingToolResults`
- 任何日志的**文案**（只改级别；文案被别处的 grep/告警规则引用时改文案是破坏性变更）

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 定向测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^(TestACallTheResponseDroppedStillGetsItsResult\|TestRetractedCallNeverStartsAndNeverRuns)$' -count=1 -v` | 两个 PASS |
| 本包回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` | 与基线相比无新增 FAIL |
| 编译 / vet | `CGO_ENABLED=1 go build -tags fts5 ./...`；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/run` | exit 0 |
| gofmt | `gofmt -l pkg/run` | 无输出 |

## Steps

### Step 1：钉住 `reconcile`，并证明它今天打的是 ERROR

在 `pkg/run/streaming_tools_test.go` 新增（import 需补三个：`bytes`、`log/slog`、`strings`——
该文件目前没有它们；`context`/`sync/atomic`/`llm`/`toolpkg` 已有）：

```go
// A provider whose final frame lists fewer calls than its stream reported ready:
// the call that already ran must still be answered, and the self-healed case is a
// warning, not an error — nothing is broken when this happens.
func TestACallTheResponseDroppedStillGetsItsResult(t *testing.T) {
	ctx := context.Background()
	const sid = "streamtools-reconcile"
	store, runID := newProgressStore(t, sid)
	st := toolpkg.NewState(t.TempDir())

	var runsA, runsB int32
	probeA, err := llm.NewTool("probe_a", "first", func(context.Context, *struct{}) (string, error) {
		atomic.AddInt32(&runsA, 1)
		return "a ok", nil
	})
	if err != nil { t.Fatalf("probe_a: %v", err) }
	probeB, err := llm.NewTool("probe_b", "second", func(context.Context, *struct{}) (string, error) {
		atomic.AddInt32(&runsB, 1)
		return "b ok", nil
	})
	if err != nil { t.Fatalf("probe_b: %v", err) }
	callA := streamCall("call-a", "probe_a")
	callB := streamCall("call-b", "probe_b")

	inner := &streamFailLLM{attempts: []func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error){
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			sink.OnDelta("working")
			sink.OnTextBlockDone()
			sink.OnToolCallReady(callA)
			sink.OnToolCallReady(callB)
			// 最终 response 只列 A：B 已经在流里跑过。
			msg := llm.AssistantMessage(nil, callA)
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
		func(ctx context.Context, sink *llm.StreamSink) (*llm.Result, error) {
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
	}}
	w := wrapToolOrchestrationLLMWithProgress(inner, st, "", store, func(context.Context) string { return "gpt-main" })
	execCtx := llm.WithStreamSink(llm.WithAgentSessionID(toolpkg.WithRunID(ctx, runID), sid), streamTestSink())

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	res, err := w.Execute(execCtx, []llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{probeA, probeB})
	if err != nil { t.Fatalf("execute: %v", err) }
	if res == nil || res.Message == nil || res.Message.TextContent() != "done" { t.Fatalf("result = %+v", res) }
	if runsA != 1 || runsB != 1 { t.Fatalf("runs = %d/%d, want 1/1", runsA, runsB) }

	// 第二轮请求必须带上两个 tool result（含被 response 漏掉的 B）。
	results := toolResultMessages(inner.received[1])
	if len(results) != 2 {
		t.Fatalf("tool results = %+v, want one per call", results)
	}
	joined := logs.String()
	if !strings.Contains(joined, "appending the call it ran") {
		t.Fatalf("the self-healed call must be logged: %s", joined)
	}
	if strings.Contains(joined, "level=ERROR") {
		t.Fatalf("a self-healed response must not log at ERROR: %s", joined)
	}
}
```

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestACallTheResponseDroppedStillGetsItsResult$' -count=1 -v`
→ **FAIL**，且失败信息是"must not log at ERROR"（`level=ERROR` 来自
`orchestration_llm.go:682`）。如果它因为别的原因失败（例如 `inner.received[1]` 没有两个 tool result），
**先停下报告**——那说明 `reconcile` 今天其实没在工作，本计划的判断要重做。

### Step 2：钉住"撤回的调用不会开始、也不会运行"

同一个文件新增（`NewInputQueue`/`NewTurnInputRuntime`/`q.Attach` 的用法见 `orchestration_llm_test.go`
的 `TestRetractingEveryReopeningSteerEndsTurnOnTheAnswer`（`:5753`））：

```go
// retractDuringTheStreamLLM emits output, then retracts the in-flight steer, then
// ignores the abort the retraction caused and reports a call ready anyway: the
// branch that refuses to start a call must keep the call from running at all.
type retractDuringTheStreamLLM struct {
	queue *InputQueue
	calls int
}

func (m *retractDuringTheStreamLLM) Execute(ctx context.Context, _ []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	if m.calls > 1 {
		msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("done")})
		return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
	}
	if sink := llm.StreamSinkFrom(ctx); sink != nil && sink.OnDelta != nil {
		sink.OnDelta("starting")
	}
	_, _ = m.queue.Recall()
	if sink := llm.StreamSinkFrom(ctx); sink != nil && sink.OnToolCallReady != nil {
		sink.OnToolCallReady(streamCall("call-retracted", "probe_retracted"))
	}
	msg := llm.AssistantMessage(nil, streamCall("call-retracted", "probe_retracted"))
	return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
}

func TestRetractedCallNeverStartsAndNeverRuns(t *testing.T) {
	st := toolpkg.NewState(t.TempDir())
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	q.Steer(Input{Text: "also do X", Parts: []llm.ContentPart{llm.Text("also do X")}})

	var runs int32
	probe, err := llm.NewTool("probe_retracted", "must never run", func(context.Context, *struct{}) (string, error) {
		atomic.AddInt32(&runs, 1)
		return "ran", nil
	})
	if err != nil { t.Fatalf("probe: %v", err) }

	inner := &retractDuringTheStreamLLM{queue: q}
	res, err := wrapToolOrchestrationLLM(inner, st).Execute(
		llm.WithStreamSink(WithTurnInputRuntime(context.Background(), rt), streamTestSink()),
		[]llm.Message{llm.UserMessage(llm.Text("go"))},
		[]*llm.Tool{probe},
	)
	if err != nil { t.Fatalf("execute: %v", err) }
	if runs != 0 {
		t.Fatalf("a call that could not start must not run: runs = %d", runs)
	}
	if res == nil || res.Message == nil || res.Message.TextContent() != "done" {
		t.Fatalf("result = %+v", res)
	}
}
```

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestRetractedCallNeverStartsAndNeverRuns$' -count=1 -v`
→ 如果 PASS：这条分支的**行为**已经是对的（本计划预期如此），继续；
如果 FAIL（`runs = 1`）：说明撤回没有挡住启动，**停下报告**——那是行为缺陷，不是本计划的范围。

### Step 3：把自愈的 ERROR 降到 WARN

`pkg/run/orchestration_llm.go:676-684`：把 `slog.Error(...)` 改成 `slog.Warn(...)`（**文案一个字节都不改**），
并把上面那句注释改准确：

```go
		// A response can report fewer calls than its stream announced — a compat
		// adapter synthesizes them from its final frame. The calls that already
		// ran are still this response's own: append the ones it left out so each
		// is answered. Self-healing, so it is a warning, never an error.
```

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestACallTheResponseDroppedStillGetsItsResult$' -count=1 -v` → PASS。

### Step 4：把 `started` 与 nil 分支的语义写准（只改注释）

`pkg/run/streaming_tools.go`：

1. `started` 的注释改成：

```go
	// started is set once, under the executor's lock, when the call is handed to
	// beforeStart — the provider has already been told this call exists
	// (progress.MarkToolDispatched), so a resend must not repeat it. It therefore
	// stays true for a call that never ran: a retracted steer makes beforeStart
	// return nil and the call goes back to queued. dispatched() reads it as "this
	// response dispatched anything", not as "a tool executed".
	started bool
```

2. nil 分支加一行说明：

```go
		if baseCtx == nil {
			// beforeStart refused to start this call (a retracted steer):
			// nothing behind it may start in-stream. started stays set on
			// purpose — see its comment — and finish runs the call after the
			// stream, or drops it with the retracted response.
			e.mu.Lock()
			call.state = streamedCallQueued
			e.halted = true
			e.mu.Unlock()
			e.wg.Done()
			return
		}
```

**Verify**：`gofmt -l pkg/run` → 无输出；`CGO_ENABLED=1 go build -tags fts5 ./...` → exit 0。

### Step 5：本包回归

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1`
→ 与基线相比无新增 FAIL。特别确认 `TestRejectionAfterTheStreamEndsStillStopsTheTurn`、
`TestInPlaceRejectionWithoutWordsStopsTheStream`、`TestQueuedSteerStopsFurtherStarts`、
`TestRetractingEveryReopeningSteerEndsTurnOnTheAnswer` 仍然 PASS。

## Test plan

- `TestACallTheResponseDroppedStillGetsItsResult`：流里 ready 两个调用、最终 response 只列一个 →
  两个都执行一次、第二轮请求带两个 tool result、日志里有自愈 warning 且**没有** ERROR。
- `TestRetractedCallNeverStartsAndNeverRuns`：流中撤回 steer 且提供方无视取消仍报 ready →
  该调用一次都没跑，回合以第二次请求的答案结束。
- 两个测试都用文件里现成的脚手架，不新增 mock 框架、不改 helper。

## Done criteria

机器可检查，全部满足：

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run` exit 0
- [ ] `gofmt -l pkg/run` 无输出
- [ ] 两个新测试 PASS
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` 与基线相比无新增 FAIL
- [ ] `grep -c 'slog.Error("tools executed in-stream' pkg/run/orchestration_llm.go` 为 `0`
- [ ] `grep -c 'slog.Warn("tools executed in-stream' pkg/run/orchestration_llm.go` 为 `1`
- [ ] `grep -c "if baseCtx == nil" pkg/run/streaming_tools.go` 为 `1`，且该分支里**没有** `started = false`
- [ ] `git status --porcelain` 只显示 Scope 里的三个文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数 ≠ 1，或 drift check 有输出且被引用函数的语义变了。
2. 基线命令本身跑不起来，拿不到失败清单。
3. Step 1 的测试因为"只有 1 个 tool result"而失败——说明 `reconcile` 没在工作，**那是真 bug**，
   本计划从"护栏"升级为"缺陷修复"，需要评审重新定范围。
4. Step 2 的测试失败（`runs != 0`）——行为缺陷，同上。
5. 你想在 nil 分支里加 `call.started = false`：**不要**。`dispatched()` 决定取消/拒绝路径选哪条分支
   （`orchestration_llm.go:559`），改它会让"撤回 + 取消"这类组合走另一条结算路径；本计划明确不改行为。
6. 你发现别处也依赖 `slog.Error` 的那条文案（例如告警规则、`grep` 断言）：把位置贴出来，先报告。

## Maintenance notes

- 这两条都是"跨 provider 兼容"的兜底：将来接新的适配器时，如果它的 ready 列表与 final 列表不一致，
  日志里会出现 `streaming tool execution outran the response's tool calls` 的 WARN —— 那是信号，不是噪音。
- `started` 这个名字与它的真实语义有偏差（"provider 已看见"而非"已经执行"）。将来若真要重命名，
  务必同时改 `dispatched()` 的注释与 `reconcile` 的过滤条件，并重跑本计划的 2 个测试 +
  所有 steer 相关测试。
- 评审重点：本计划的 diff 里**只能**出现两处注释、一行日志级别、两个测试文件内的新增；
  任何对 `finish`/`abort`/`reconcile` 逻辑的改动都是越界。
