# Plan 005: gateway 的 run 队列 hook 一次只发一个变化；删掉已无人使用、会重新引入在途 steer 撤回 bug 的 `RetractLastSteer`

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> **Drift check (run first)**: 本计划写在 HEAD `71c0449` **加上工作区里尚未提交的 plan 003 实现**之上
> （003 新增了 `watchRunQueue`、`RetractSteer`、`input_delivered`）。所以漂移检查用锚点而不是 SHA：
>
> ```bash
> grep -n "func (s \*Server) watchRunQueue" pkg/gateway/run_control.go          # 恰好 1 行
> grep -n "func (r \*TurnInputRuntime) RetractSteer(seq int) bool" pkg/run/turn_input.go   # 恰好 1 行
> grep -n "func TestRunQueuePublishesDeliveredSteerOnce" pkg/gateway/run_control_test.go   # 恰好 1 行
> grep -rn "RetractLastSteer" --include='*.go' pkg cmd                           # 只命中 pkg/run/turn_input.go 与 pkg/run/turn_input_test.go
> ```
>
> 任何一条不符，先把「Current state」里的摘录和实码逐段对照；对不上就按 STOP 处理。
> 若 `git log --oneline 71c0449..HEAD` 已有提交（003 已被提交），这是预期内的，照常用上面的锚点核对。

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: LOW
- **Depends on**: `plans/003-recall-in-flight-steer.md`（必须已在工作区或已提交；本计划改的是 003 新增的代码）
- **Category**: bug（第一部分）+ tech-debt（第二部分）
- **Planned at**: commit `71c0449` + 未提交的 003 工作区，2026-10-09

## Why this matters

**第一部分（gateway hook 串行化）**：plan 003 让 gateway 在 steer 送达模型时发一条持久事件 `input_delivered`，web 收到它就
「收尾当前回答气泡 → 插入用户气泡 → 开新回答气泡」，之后的 `assistant_delta` 写进新气泡。这要求 `input_delivered`
**先于**该回答的首个 `assistant_delta` 落库。正常情况下成立：送达在 provider 的流式 goroutine 里 Commit，Commit 同步调用队列的
change hook，hook 发完 `input_delivered` 才返回，provider 才转发第一个 delta。但 hook 会在**任意改动队列的 goroutine** 上触发
（用户在 web 上排队/撤回的 HTTP 处理函数也会触发），而 hook 体没有任何互斥：另一个 goroutine 的 hook 若恰好在
「steer 被移入 delivered 列表」与「Commit 所在 goroutine 调 `TakeDelivered`」之间执行，就会把这条 steer 拿走、由它去发；
Commit 那边拿到空列表、直接返回并开始转发 delta——`input_delivered` 可能晚于回答开头落库，web 实时视图把回答开头几个字画进上一个气泡
（刷新后以 transcript 为准，恢复正常）。同一个缺口也让「旧预览晚于新预览落库」成为可能（web 队列浮层残留一条已送达的消息，直到下一次队列变化）。
窗口很窄，但修法只是给 hook 体加一把锁。

**第二部分（删 `RetractLastSteer`）**：003 之后 `TurnInputRuntime.RetractLastSteer` 已没有任何生产调用方，只剩测试。它只查 runtime 的
`pending`、看不到「已取走但模型还没开始回答」的在途交付——003 的 Maintenance notes 明确写着「新代码不要用它撤回 steer（它看不到在途交付，
会重新引入本 bug）」。留着一个「看起来能用、用了就回归」的公开方法是陷阱；删掉它、把测试改成用 `RetractSteer(seq)`。

## Current state

### 相关文件

- `pkg/gateway/run_control.go` — `watchRunQueue`：给 gateway run 的会话队列装 change hook，发 `input_delivered` 与 `pending_input_updated`（第 264–284 行）。
- `pkg/gateway/run_control_test.go` — 队列 hook 的测试；`trackedQueue` 辅助函数（693–703 行）、范本测试 `TestRunQueuePublishesDeliveredSteerOnce`（708–746 行）。
- `pkg/gateway/wsevents.go` — 事件总线 `runEventBus.Publish`（587 行起）：先 `AppendSessionEvent` 落库（分配 sequence），再同步调用每个订阅的 `deliver`；`deliver`（728 行起）持有订阅自己的 `sub.mu` 调用 `send`。
- `pkg/run/turn_input.go` — `TurnInputRuntime` / `InputQueue`；`RetractLastSteer`（271–291 行）、`RetractSteer`（293 行起）、`steersDelivered` / `TakeDelivered`。
- `pkg/run/turn_input_test.go` — 用到 `RetractLastSteer` 的三个测试（12、50、67 行起）。

### `watchRunQueue` 现状（`pkg/gateway/run_control.go:264-284`）

```go
// watchRunQueue republishes a run's queue after every change to it, and draws
// each steer the run hands its model into the conversation as the user's own
// message, ahead of the preview that no longer lists it. The delivered list is
// drained here — the way the terminal takes it to render — so each steer is
// published exactly once and the conversation's queue does not keep growing.
func (s *Server) watchRunQueue(q *run.InputQueue, runID, sessionID string) {
	if s == nil || q == nil {
		return
	}
	q.SetChangeHook(func() {
		ctx := context.Background()
		for _, in := range q.TakeDelivered() {
			_ = s.publishGatewayRunEvent(ctx, sessionID, runID, event.RunEventInputDelivered, event.InputDeliveredPayload{
				Text:          in.Text,
				Attachments:   in.Attachments,
				MentionImages: in.MentionImages,
			})
		}
		s.appendPendingInputUpdated(ctx, runID, sessionID, toPendingInputPreview(q.Preview()))
	})
}
```

调用方只有两处：`pkg/gateway/server.go`（`HandleChatWS`，约 1706 行）与 `pkg/gateway/run_control.go`（`startDetachedTurn`，约 690 行），
都是 `s.watchRunQueue(q, runID, sid)`。`pkg/gateway/run_control.go` 的 import 目前**没有** `"sync"`（有 `"time"`、`"strings"` 等）。

### hook 为什么会并发（`pkg/run/turn_input.go`）

- `SteerDelivery.Commit()` 释放 runtime 锁后调用 `notifyDelivered` → `InputQueue.steersDelivered`：在 `q.mu` 下把 steer 从 `q.steers`
  移到 `q.delivered`，**释放 `q.mu` 之后**调用 `q.hook`（即上面的闭包）。
- `InputQueue.Steer` / `FollowUp` / `Recall` / `Discard` / `Next` / `TakeAll` / `Attach` / `Detach` 同样在释放 `q.mu` 后调用 `q.hook`。
- 这些方法分别在 provider 流式 goroutine（Commit）和 HTTP 处理函数 goroutine（排队、`edit_last` 撤回）上执行，所以闭包会并发运行。
- `TakeDelivered()` 本身是原子的（`q.mu` 下取走并清空），所以每条 steer 只会被发一次——问题只在**由哪个 goroutine 发、发在什么时刻**。

### `RetractLastSteer` 现状（`pkg/run/turn_input.go:271-291`）

```go
// RetractLastSteer removes and returns the most recently enqueued steer entry
// that has not yet been drained. It returns false when no steer is pending
// (already delivered at a tool boundary, or never queued), which lets callers
// distinguish a still-editable steer from one the agent has already consumed.
func (r *TurnInputRuntime) RetractLastSteer() ([]llm.ContentPart, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	for i := len(r.pending) - 1; i >= 0; i-- {
		...
	}
	r.mu.Unlock()
	return nil, false
}
```

替代者（已存在，不要改）：`func (r *TurnInputRuntime) RetractSteer(seq int) bool` —— 按队列时钟 `Seq` 撤回，先查 `pending`（只匹配
`Mode == TurnInputModeSteer`），再查未结算的在途交付；`r == nil` 或 `seq <= 0` 返回 false。
包内测试可以用未导出的 `rt.enqueueEntry(TurnInputEntry{Mode: ..., Parts: ..., Seq: n})` 给条目打上 `Seq`
（`rt.Enqueue(mode, parts)` 打的是 `Seq: 0`，`RetractSteer` 永远找不到它）。

`RetractLastSteer` 在测试里的三处用法（`pkg/run/turn_input_test.go`）：

- 12–36 行 `TestRetractLastSteerRemovesMostRecentSteer`：入队 steer「first steer」、follow-up「a follow up」、steer「second steer」，撤回最新 steer，断言剩下两条且顺序不变。
- 50–65 行 `TestRetractLastSteerReturnsFalseWhenNoSteers`：只有 follow-up 时撤回失败、follow-up 不动；nil runtime 返回 false。
- 67 行起 `TestTurnInputRuntimeChangeHookFiresWhenPendingChanges` 第 90 行：`if _, ok := rt.RetractLastSteer(); ok {` —— 断言 drain 之后没有 steer 可撤、且撤回失败不触发 hook。

### 仓库约定

- 注释风格：完整英文句子，解释「为什么」，与 `watchRunQueue` 现有注释同一口吻（见上方摘录）。不要写中文注释。
- 测试用 `github.com/stretchr/testify/require`（gateway 包）与标准 `testing`（run 包，`t.Fatalf`），分别照各自文件已有写法。
- gateway 测试服务器：`newRunInputTestServer(t)`（会话 id 固定为 `"sid"`）+ `trackedQueue(t, s, runID)`（内部已调用 `s.watchRunQueue`）。
- 事件读取：`s.RunRT.ListRunEvents(context.Background(), runID, 100)`，按 sequence 升序返回，元素有 `.Type` 与 `.Payload`。
- 订阅总线：`sub := s.RunEvents().Subscribe(func(evt event.RunEvent) {...})`，`sub.Bind("sid")` 之后才收事件，`sub.Close()` 退订。
  `send` 回调在 `Publish` 里**同步**调用，且在事件**已落库之后**。生产里的 WS 订阅回调是非阻塞的（带缓冲 channel，满了就丢），见 `pkg/gateway/server.go` 约 723 行。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| gateway 队列相关测试 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/gateway -run 'TestRunQueue\|TestHandleRunQueuedInput\|TestDeliveredSteer\|TestSubagentSurface'` | `ok` |
| gateway 全包 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/gateway` | `ok` |
| run 包（带 race） | `CGO_ENABLED=1 go test -race -tags fts5 -count=1 ./pkg/run` | `ok`，无 `DATA RACE` |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run/... ./pkg/gateway/...` | 无输出，exit 0 |
| gofmt | `gofmt -l pkg/run pkg/gateway` | 无输出 |

（以上命令 2026-10-09 在本仓实跑过：`./pkg/run` 带 `-race` 约 20s 通过，`./pkg/gateway` 约 6s 通过。）

## Scope

**In scope**（只允许改这些文件）：

- `pkg/gateway/run_control.go`（`watchRunQueue` 与 import）
- `pkg/gateway/run_control_test.go`（新增一个测试）
- `pkg/run/turn_input.go`（删 `RetractLastSteer`）
- `pkg/run/turn_input_test.go`（改写三处用法）
- `plans/README.md`（状态行）

**Out of scope**（看着相关，但不要碰）：

- `pkg/run/turn_input.go` 里 `InputQueue` 调用 hook 的方式——**不要**在引擎层给所有 hook 加锁。那会影响 TUI（`pkg/tui/chat_session.go` 的 hook）与 subagent 通道（`pkg/run/subagent.go` 的 `queueChanged`）的加锁关系，后者的 hook 会在持有通道锁时被调用，改引擎层要重新论证死锁；本计划只修 gateway 一处。
- `pkg/run/subagent.go` 的 `OnQueueChanged` / `queueChanged`：subagent 的送达事件 `subagent_input_delivered` 由 runtime 的 delivery hook 在 Commit 所在 goroutine 同步发出，不走这里，不受本问题影响。
- `DrainSteers` / `DrainAll` / `HasSteers` 等其它只在测试里用的 runtime 方法：被大量 TUI/gateway 测试当作「模拟送达」的辅助用，删它们收益小、改动大，不在本计划内。
- `frontend/**`：前端逻辑不变。
- `plans/003-*.md` 里提到 `RetractLastSteer` 的文字：那是历史计划，不改。

## Git workflow

- **不建分支、不 commit、不 push、不开 PR**：改动留在当前工作区，由 owner 手动审阅提交（本仓既定工作流）。
- 如 owner 需要 commit message 建议：`fix(gateway): publish run queue changes one at a time` 与 `refactor(run): drop RetractLastSteer, which cannot see in-flight steers`（Conventional Commits，subject 小写、无句号）。

## Steps

### Step 1: 跑基线

运行 Drift check 里的四条 grep，确认结果与描述一致；再跑：

```bash
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/gateway -run 'TestRunQueue|TestHandleRunQueuedInput|TestDeliveredSteer|TestSubagentSurface'
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run 'RetractLastSteer|TurnInputRuntime|RetractSteer|InputQueue|Recall'
```

**Verify**: 两条都输出 `ok`。

### Step 2: 先写会失败的回归测试

在 `pkg/gateway/run_control_test.go` 中，紧接 `TestRunQueuePublishesDeliveredSteerOnce`（以 `require.Empty(t, q.TakeDelivered(), "the hook drains what it publishes")` 和 `}` 结束）之后，新增：

```go
// The queue's change hook runs on whichever goroutine changed the queue: the
// run's stream when a steer is committed, a request handler when the user
// queues or recalls. A second invocation slipping in while the first is still
// publishing could take the steer the first one moved and publish it after the
// run had gone on to the answer, so the web would draw the answer's first words
// before the message it answers. Invocations publish one change at a time.
func TestRunQueueHookPublishesOneChangeAtATime(t *testing.T) {
	s := newRunInputTestServer(t)
	runID := "run-serialized"
	q := trackedQueue(t, s, runID)
	require.True(t, q.Steer(run.Input{Text: "also run lint"}))
	delivery := q.Runtime().BeginSteerDelivery(context.Background())
	require.NotNil(t, delivery)

	// Hold the commit's hook in the middle of its publishing: its
	// input_delivered event is stored, the preview after it is not yet.
	held := make(chan struct{})
	release := make(chan struct{})
	var holdOnce sync.Once
	sub := s.RunEvents().Subscribe(func(evt event.RunEvent) {
		if evt.Type != event.RunEventInputDelivered {
			return
		}
		holdOnce.Do(func() {
			close(held)
			<-release
		})
	})
	require.NotNil(t, sub)
	sub.Bind("sid")
	t.Cleanup(sub.Close)

	countEvents := func() int {
		events, err := s.RunRT.ListRunEvents(context.Background(), runID, 100)
		require.NoError(t, err)
		return len(events)
	}

	committed := make(chan struct{})
	go func() {
		defer close(committed)
		delivery.Commit()
	}()
	<-held
	before := countEvents()

	queued := make(chan struct{})
	go func() {
		defer close(queued)
		q.FollowUp(run.Input{Text: "a change from another request"})
	}()
	time.Sleep(100 * time.Millisecond)
	stored := countEvents()
	close(release)
	<-committed
	<-queued

	require.Equal(t, before, stored, "another change was published while the commit's hook was still publishing")
	events, err := s.RunRT.ListRunEvents(context.Background(), runID, 100)
	require.NoError(t, err)
	var order []string
	for _, evt := range events {
		switch evt.Type {
		case event.RunEventInputDelivered:
			order = append(order, "delivered")
		case event.RunEventPendingInputUpdated:
			order = append(order, "preview")
		}
	}
	require.GreaterOrEqual(t, len(order), 3, "order: %v", order)
	require.Equal(t, []string{"delivered", "preview", "preview"}, order[len(order)-3:],
		"the commit's preview follows its delivered steer before the next change: %v", order)
}
```

说明（不要写进代码）：`close(release)` 放在断言之前，是为了断言失败时也不会让后台 goroutine 永久阻塞。`run_control_test.go` 已 import
`"sync"`、`"time"`、`"context"`、`event`、`run`、`require`，无需新增 import——若编译报缺 import，按报错补上即可。

**Verify**（修复前必须失败）:

```bash
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/gateway -run TestRunQueueHookPublishesOneChangeAtATime
```

→ `FAIL`，失败信息包含 `another change was published while the commit's hook was still publishing`。

### Step 3: 给 hook 体加锁

修改 `pkg/gateway/run_control.go` 的 `watchRunQueue`：在 `q.SetChangeHook(...)` 之前声明一把互斥锁，闭包一进来就加锁、`defer` 解锁。
目标形态：

```go
func (s *Server) watchRunQueue(q *run.InputQueue, runID, sessionID string) {
	if s == nil || q == nil {
		return
	}
	// The hook runs on whichever goroutine changed the queue — the run's
	// stream when a steer is committed, a request handler when the user queues
	// or recalls — so changes are published one at a time. A delivered steer
	// is then always published by the invocation that moved it, before the
	// commit returns to the stream whose next event is the answer, and the
	// last preview stored is one read after the last change.
	var publishing sync.Mutex
	q.SetChangeHook(func() {
		publishing.Lock()
		defer publishing.Unlock()
		ctx := context.Background()
		for _, in := range q.TakeDelivered() {
			... // 原样保留
		}
		s.appendPendingInputUpdated(ctx, runID, sessionID, toPendingInputPreview(q.Preview()))
	})
}
```

并在 `pkg/gateway/run_control.go` 的标准库 import 组里按字母序加入 `"sync"`（放在 `"strings"` 与 `"time"` 之间）。
不要改 `watchRunQueue` 上方已有的 doc 注释，不要改闭包里的发布逻辑。

**Verify**:

```bash
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/gateway -run TestRunQueueHookPublishesOneChangeAtATime
CGO_ENABLED=1 go test -race -tags fts5 -count=1 ./pkg/gateway -run 'TestRunQueue|TestHandleRunQueuedInput|TestDeliveredSteer|TestSubagentSurface'
```

→ 两条都 `ok`，无 `DATA RACE`。再把第一条连跑 5 次：
`CGO_ENABLED=1 go test -tags fts5 -count=5 ./pkg/gateway -run TestRunQueueHookPublishesOneChangeAtATime` → `ok`。

### Step 4: 改写 `RetractLastSteer` 的三处测试用法

在 `pkg/run/turn_input_test.go`：

1. 把 `TestRetractLastSteerRemovesMostRecentSteer`（12–36 行）整体替换为：

```go
func TestRetractSteerRemovesThePendingSteerItNames(t *testing.T) {
	rt := NewTurnInputRuntime()
	rt.enqueueEntry(TurnInputEntry{Mode: TurnInputModeSteer, Parts: []llm.ContentPart{llm.Text("first steer")}, Seq: 1})
	rt.enqueueEntry(TurnInputEntry{Mode: TurnInputModeFollowUp, Parts: []llm.ContentPart{llm.Text("a follow up")}, Seq: 2})
	rt.enqueueEntry(TurnInputEntry{Mode: TurnInputModeSteer, Parts: []llm.ContentPart{llm.Text("second steer")}, Seq: 3})

	if !rt.RetractSteer(3) {
		t.Fatalf("expected the named steer to be retracted")
	}

	remaining := rt.Snapshot()
	if len(remaining) != 2 {
		t.Fatalf("expected two entries left, got %#v", remaining)
	}
	if remaining[0].Mode != TurnInputModeSteer || llm.TextContent(remaining[0].Parts...) != "first steer" {
		t.Fatalf("expected earlier steer preserved, got %#v", remaining[0])
	}
	if remaining[1].Mode != TurnInputModeFollowUp || llm.TextContent(remaining[1].Parts...) != "a follow up" {
		t.Fatalf("expected follow-up preserved, got %#v", remaining[1])
	}
}
```

2. 把 `TestRetractLastSteerReturnsFalseWhenNoSteers`（50–65 行）整体替换为：

```go
func TestRetractSteerLeavesFollowUpsAlone(t *testing.T) {
	rt := NewTurnInputRuntime()
	rt.enqueueEntry(TurnInputEntry{Mode: TurnInputModeFollowUp, Parts: []llm.ContentPart{llm.Text("just a follow up")}, Seq: 1})

	if rt.RetractSteer(1) {
		t.Fatalf("a follow-up is not a steer to retract")
	}
	if remaining := rt.Snapshot(); len(remaining) != 1 {
		t.Fatalf("expected follow-up untouched, got %#v", remaining)
	}

	var nilRT *TurnInputRuntime
	if nilRT.RetractSteer(1) {
		t.Fatalf("expected nil runtime to report no steer")
	}
}
```

3. 在 `TestTurnInputRuntimeChangeHookFiresWhenPendingChanges` 里，把

```go
	if _, ok := rt.RetractLastSteer(); ok {
```

改为

```go
	if rt.RetractSteer(1) {
```

（该测试的条目用 `rt.Enqueue` 入队，`Seq` 为 0，所以没有 `Seq == 1` 的条目，撤回必然失败；断言的意图「撤回没改变任何东西时不触发 hook」不变。）
其余断言、注释、错误信息一律不动。

**Verify**:

```bash
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run 'TestRetractSteer|TestTurnInputRuntime'
```

→ `ok`（此时 `RetractLastSteer` 还在，测试已不依赖它）。

### Step 5: 删除 `RetractLastSteer`

删除 `pkg/run/turn_input.go` 中 `RetractLastSteer` 的 doc 注释（4 行，以 `// RetractLastSteer removes and returns` 开头）和整个函数体。
删除后若 `llm` 包在该文件中不再被引用，编译器会报 unused import——**预期不会**（文件里 `TurnInputEntry.Parts []llm.ContentPart` 等仍在用），若真报了按报错处理。

**Verify**:

```bash
grep -rn "RetractLastSteer" --include='*.go' pkg cmd     # 无输出
CGO_ENABLED=1 go build ./...                              # exit 0
CGO_ENABLED=1 go test -race -tags fts5 -count=1 ./pkg/run # ok，无 DATA RACE
```

### Step 6: 全量校验

```bash
CGO_ENABLED=1 go vet -tags fts5 ./pkg/run/... ./pkg/gateway/...
gofmt -l pkg/run pkg/gateway
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/gateway ./pkg/run ./pkg/tui
```

**Verify**: vet 与 gofmt 无输出；三个包 `ok`。
注意：`./pkg/tui` 的 `TestCharacterizationNetworkApproval` 会真实连接 `https://example.com`，在网络不稳时偶发以
`curl: (35) ... SSL_ERROR_SYSCALL` 失败，与本计划无关——单独重跑
`CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/tui -run 'TestCharacterizationNetworkApproval$'` 通过即可；若单跑仍失败，记录输出并报告，不要改该测试。

### Step 7: 更新索引

在 `plans/README.md` 的计划列表里，把 `005-serialize-run-queue-hook-and-drop-retract-last-steer` 一行的状态改为 `DONE`。

**Verify**: `grep -n "005-serialize-run-queue-hook" plans/README.md` 命中的表格行包含 `DONE`。

## Test plan

- **新增**：`TestRunQueueHookPublishesOneChangeAtATime`（`pkg/gateway/run_control_test.go`）——用阻塞订阅把 Commit 的 hook 卡在
  「`input_delivered` 已落库、预览未落库」之间，另一个 goroutine 改队列；断言卡住期间没有新事件落库，放行后顺序为
  delivered → preview（Commit 的）→ preview（另一个改动的）。修复前失败、修复后通过（Step 2 / Step 3 各验证一次）。
  结构范本：同文件 `TestRunQueuePublishesDeliveredSteerOnce`。
- **改写**：`TestRetractSteerRemovesThePendingSteerItNames`、`TestRetractSteerLeavesFollowUpsAlone`（替换原 `RetractLastSteer` 的两个测试，覆盖同样的行为：按名撤回只动那一条、follow-up 不被当作 steer、nil runtime 安全）；`TestTurnInputRuntimeChangeHookFiresWhenPendingChanges` 改一行。
- 既有测试必须原样通过，尤其 `TestRunQueuePublishesDeliveredSteerOnce`、`TestHandleRunQueuedInputEditLastRetractsInFlightSteer`、`TestDeliveredSteerPublishesRefreshedPendingInputPreview`、`TestRetractSteer*`、`TestInputQueue*`。

## Done criteria

- [ ] `grep -n "publishing.Lock()" pkg/gateway/run_control.go` 恰好 1 行，位于 `watchRunQueue` 的闭包内
- [ ] `grep -rn "RetractLastSteer" --include='*.go' pkg cmd` 无输出
- [ ] `CGO_ENABLED=1 go test -tags fts5 -count=5 ./pkg/gateway -run TestRunQueueHookPublishesOneChangeAtATime` → `ok`
- [ ] Step 2 记录了修复前该测试的 FAIL 输出（贴在完成报告里）
- [ ] `CGO_ENABLED=1 go test -race -tags fts5 -count=1 ./pkg/run` → `ok`，无 `DATA RACE`
- [ ] `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/gateway` → `ok`
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run/... ./pkg/gateway/...` 与 `gofmt -l pkg/run pkg/gateway` 均无输出
- [ ] `git status --short -- pkg` 相对执行前只多出 Scope 内的 4 个 Go 文件的改动（003 留下的改动不算）
- [ ] `plans/README.md` 中 005 状态为 `DONE`

## STOP conditions

满足任一条即停止并回报，不要即兴发挥：

- Drift check 的锚点 grep 有任何一条不符（尤其 `watchRunQueue` 不存在：说明 003 不在工作区/未提交，本计划无从执行）。
- Step 2 的新测试在**修复前就通过了**：说明它没打到并发窗口（例如总线的投递方式与本计划描述不同）。报告 `runEventBus.Publish` / `deliver` 的实际行为，不要改测试去迁就。
- Step 3 之后新测试或任何 gateway 测试**卡死/超时**：说明有 hook 调用发生在另一次 hook 调用内部（重入）或订阅回调回调进了队列。用 `-timeout 60s` 重跑拿到 goroutine 栈，报告栈，不要去掉锁。
- `-race` 报 `DATA RACE`，且涉及本计划改动的代码。
- `grep -rn "RetractLastSteer"` 在 `pkg/run/turn_input.go` / `pkg/run/turn_input_test.go` 之外有命中（例如生产代码或其它包的测试）：报告位置，不要擅自改那些文件。
- 修复看起来必须改 Out of scope 里的文件（尤其是想在 `InputQueue` 引擎层加锁）。

## Maintenance notes

- **锁是按 `watchRunQueue` 调用建的**：每个 gateway run 开始时（`HandleChatWS` / `startDetachedTurn`）都会对同一个会话队列重新 `SetChangeHook`，换一把新锁。上一个 run 的 hook 若还在执行，与新 hook 理论上可重叠——两次 run 之间队列几乎不变，接受这一点。若以后同一会话会有并发 run（目前没有），要改成按队列（`*run.InputQueue`）共享一把锁。
- **hook 里不能再改队列**：hook 持锁期间调用任何会触发 hook 的 `InputQueue` 方法（`Steer`、`FollowUp`、`Recall`、`Discard`、`Next`、`TakeAll`、`Attach`、`Detach`）都会自锁死。目前只调 `TakeDelivered` 与 `Preview`（都不触发 hook）。审阅时盯住这一点。
- **持锁期间会做一次 SQLite 写**：Commit 所在的 provider 流式 goroutine 可能因此等待另一个 HTTP 请求的 hook 写完一条事件（毫秒级）。生产里的 WS 订阅回调不阻塞（带缓冲 channel），所以不会被慢客户端拖住；若以后有阻塞式订阅者，要重新评估。
- **引擎层串行化被有意拒绝**：TUI 的 hook 只投递一条 UI 消息、subagent 通道的 hook 在持有通道锁时被调用，统一在 `InputQueue` 里加锁要重新论证这些锁序，收益不抵风险。
- **撤回在途 steer 一律用 `RetractSteer(seq)`**：`RetractLastSteer` 已删除；以后若需要「撤回最新 steer」，在 `InputQueue` 层按 `Seq` 选出最新那条，再调 `RetractSteer`（`InputQueue.Recall` 就是这么做的）。
