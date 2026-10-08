# Plan 001: Fix 入队消息在 runtime detach 后无法撤回

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat eafaf46..HEAD -- pkg/run/turn_input.go pkg/run/turn_input_test.go`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1（用户阻塞 bug：消息提交后无法撤回）
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `eafaf46`, 2026-10-08（reviewed 2026-10-08，同 commit 零漂移）

## Why this matters

TUI 的消息队列允许用户把一条已提交、模型尚未处理的消息撤回到 composer 编辑（Shift+←，`hotkeyEditLastQueued`）。但当 turn 的 steer runtime 被 `Detach()` 后（run 结束、surface-turn 边界等时刻），未交付的 steer 仍留在队列里、模型从未见过，`Recall()` 却因 `q.rt == nil` 直接跳过整条 steer lane——用户按键无响应，消息既撤不回、又只能等边界把它作为下一 turn 发送。修复后：只要消息未交付，无论 runtime 是否 attach，都可撤回。

## Current state

（全部摘录已在 commit `eafaf46` 逐行核实。）

- `pkg/run/turn_input.go` — 引擎侧输入队列与 steer runtime，本计划唯一改动的生产文件。
- `pkg/run/turn_input_test.go` — 上述文件的测试（已 import `context`/`reflect`/`strings`/`testing` 与 `pkg/llm`）。

关键事实链：

1. **入队双写**（`pkg/run/turn_input.go:502-541` `InputQueue.Steer`）：消息同时进入
   `q.steers`（surface 侧镜像，533 行）与 `q.rt.enqueueEntry(...)`（runtime 侧，534 行）。
2. **Detach 不清队列**（`pkg/run/turn_input.go:414-425` `InputQueue.Detach`）：只置
   `q.rt = nil`。其 doc 注释（409-413 行）明确："Undelivered steers stay in the queue —
   the turn's boundary decides what follows them"。
3. **交付才有转移**：只有 `BeginSteerDelivery` → `Commit` → change hook
   `steersDelivered`（431-456 行）才会把消息从 `q.steers` 移入 `q.delivered`。
4. **Bug 所在**（`pkg/run/turn_input.go:613-651` `InputQueue.Recall`，case 分支在
   627-636 行）：

```go
case laneSteer:
	if q.rt == nil {
		skipSteers = true
		continue
	}
	if _, ok := q.rt.RetractLastSteer(); !ok {
		skipSteers = true
		continue
	}
	laneEntries = &q.steers
```

`q.rt == nil` 被当成了"不可撤回"，但它只表示当前无 attached runtime——留在
`q.steers` 里的 steer 恰恰是**从未交付**的那批。

**用户可见窗口真实存在**：TUI 的 `tuiFinish`（`pkg/tui/chat_session.go:928-947`）在
run 结束即 `q.Detach()`，而 boundary 决策（`Next`）在 `DispatchSurfaceTurn` 稍后才执行；
`pkg/tui/chat_session_test.go:12313-12327`（`TestReconcilePendingSteersNoopWhenInactive`）
证实了"inactive 队列 + 滞留 steer"这个状态的存在。

**为什么放宽是安全的**（executor 需要理解的论证，改前请确认仍然成立）：

- `SteerDelivery` 的生命周期（`pkg/run/turn_input.go:129-208`）保证
  `BeginSteerDelivery` 之后必然在同一次 run-loop 迭代内 `Commit` 或 `Rollback`
  （调用点 `pkg/run/orchestration_llm.go:245,327`）——不存在跨 run 存活的在途交付。
- `Detach()` 的全部四个调用点都在 run 结束之后：
  `pkg/run/controller.go:299`（`Release`，Detach 后立刻 `Next`）、
  `pkg/tui/chat_session.go:940`（`tuiFinish`）、`pkg/tui/chat_session.go:1070`
  （`discardTUITurnInput`）、`pkg/run/subagent.go:803`（subagent 执行返回后）。
- 因此 `q.rt == nil` 时留在 `q.steers` 的 steer 从未交付、也永不会被 retired
  runtime 交付；`Recall` 与 `steersDelivered` 都持 `q.mu`，无数据竞争；
  `Next()` 的 boundary 语义完全不动。

**仓库约定**：注释必须与行为一致（Recall 的 doc 注释 604-612 行需要同步更新）；
测试风格参照 `pkg/run/turn_input_test.go` 现有 Q 系列 characterization 测试。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 单包测试（现网行为） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run TestInputQueue -count=1` | all pass |
| 新测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run TestRecallSteerAfterRuntimeDetach -count=1 -v` | PASS |
| 全量测试 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | all pass |
| Vet | `go vet ./...` | no findings |
| 格式（CI gate） | `gofmt -l pkg/run` | 无输出 |
| 构建（真机验收用） | `go build -o ./build/bin/forebrain ./cmd/forebrain` | exit 0 |

## Scope

**In scope**（只允许改这两个文件）：

- `pkg/run/turn_input.go` — `InputQueue.Recall()` 的 `case laneSteer` 分支 + 同步更新 `Recall` 的 doc 注释
- `pkg/run/turn_input_test.go` — 新增 `TestRecallSteerAfterRuntimeDetach`

**Out of scope**（看起来相关，也不要动）：

- `pkg/run/orchestration_llm.go`、`pkg/run/controller.go`、`pkg/run/subagent.go` — 交付与 Detach 调用点，本修复的安全论证依赖它们现状
- `pkg/tui/**`、`pkg/gateway/**` — surface 层撤回入口（`pkg/tui/run.go:2791` 等）语义已正确，无需感知本改动；subagent 通道的 `ch.queue.Recall()`（`pkg/run/subagent.go:1886`）自动继承放宽后的语义，这是期望方向，不需要改它
- `Next()`、`Discard()`、`TakeAll()` 及任何 boundary 决策逻辑

## Git workflow

- **不建分支、不 commit、不 push、不开 PR**：改动直接落在当前工作区，由 owner 手动逐个审阅提交（本仓既定工作流）。
- 如 owner 事后需要 commit message 建议：`fix(run): recall queued steers after the turn runtime detaches`。

## Steps

### Step 1: 放宽 `InputQueue.Recall()` 的 steer lane 条件

**文件**: `pkg/run/turn_input.go`，`Recall()` 的 `case laneSteer:` 分支（613-651 行内，
627-636 行处）。

**现有代码**（见 "Current state" 第 4 条摘录）。

**修改为**：

```go
case laneSteer:
	// When a runtime is attached, retract from it first: a failed
	// retraction means the run already drained the steer for a model
	// call — and since the runtime drains FIFO, every older steer is
	// gone too. When the runtime is detached, the steer never left the
	// queue (delivery settles inside the run loop before the run can
	// end, and Detach runs after that), so it can be recalled directly.
	if q.rt != nil {
		if _, ok := q.rt.RetractLastSteer(); !ok {
			skipSteers = true
			continue
		}
	}
	laneEntries = &q.steers
```

同步更新 `Recall` 的 doc 注释（604-612 行）：在现有"A pending steer also lives in
the attached runtime…"段落后补一句，说明 detached runtime 下 steer 只存在于队列、
从未交付、可直接撤回（措辞与上面代码注释一致即可）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run TestInputQueue -count=1` → all pass（行为向后兼容：attached 路径未变）。

### Step 2: 新增 `TestRecallSteerAfterRuntimeDetach`

**文件**: `pkg/run/turn_input_test.go`（追加在 `TestInputQueueRecallPicksNewestByLane`
之后；**不要**放进 `turn_input.go`）。文件已 import `testing` 与 `pkg/llm`，无需新增 import。

```go
// A steer still queued when the turn's runtime detached was never
// delivered — the run's deliveries settle before it can end — so it must
// stay editable, and once recalled the boundary must not send it either.
func TestRecallSteerAfterRuntimeDetach(t *testing.T) {
	q := NewInputQueue()
	rt := NewTurnInputRuntime()
	q.Attach(rt)
	if !q.Steer(Input{Text: "test message", Parts: []llm.ContentPart{llm.Text("test message")}}) {
		t.Fatal("Steer failed")
	}

	q.Detach()
	if q.Runtime() != nil {
		t.Fatal("expected runtime to be nil after Detach")
	}

	in, ok := q.Recall()
	if !ok {
		t.Fatal("Recall failed after runtime detach, but the steer was never delivered")
	}
	if in.Text != "test message" {
		t.Fatalf("recalled %q, want %q", in.Text, "test message")
	}
	if preview := q.Preview(); len(preview.Steers) != 0 {
		t.Fatalf("expected 0 steers after recall, got %d", len(preview.Steers))
	}
	if send, _ := q.Next(BoundaryCompleted); len(send) != 0 {
		t.Fatalf("a recalled steer must not be sent by the boundary: %#v", send)
	}
}
```

（最后一条断言是用户可见结果：撤回的消息不会再被 boundary 自动发送。）

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run TestRecallSteerAfterRuntimeDetach -count=1 -v` → `PASS`。

### Step 3: TUI 真机验收

1. `go build -o ./build/bin/forebrain ./cmd/forebrain && ./build/bin/forebrain interactive`
2. 提交一条消息（如 "test recall after delay"），等 run 结束（`tuiFinish` 触发 Detach，消息仍在 pending preview）。
3. 按 **Shift+←**。
4. 预期：消息从 pending preview 消失、回到 composer 可编辑重新提交。
   修复前：按键无响应、消息滞留 preview。

（可用 `.claude/skills/run-forebrain/driver.sh` 在 tmux 里驱动同样的按键序列留证。）

## Test plan

- 新增：`TestRecallSteerAfterRuntimeDetach`（上述 Step 2 全文），覆盖本 bug 的核心场景：detached + 未交付 → 可撤回；撤回后 boundary 不发送。
- 结构范本：`pkg/run/turn_input_test.go` 的 `TestInputQueueRecallPicksNewestByLane`（同样的 NewInputQueue/Attach/Steer/Recall/Preview 用法）。
- 既有测试必须原样通过（已核实没有任何现有测试以"detach 后撤回必须失败"为主题；`TestInputQueueNextBoundaries`、`TestCharacterization*`、`TestReconcilePendingSteersNoopWhenInactive` 均不受影响）。
- 验证：`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → all pass。

## Done criteria

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run TestInputQueue -count=1` 通过
- [ ] `TestRecallSteerAfterRuntimeDetach` 存在且通过
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全量通过
- [ ] `go vet ./...` 无发现
- [ ] `gofmt -l pkg/run` 无输出（CI 有 gofmt gate）
- [ ] `git status` 显示只有 `pkg/run/turn_input.go`、`pkg/run/turn_input_test.go` 两个文件被改
- [ ] TUI 真机验收：提交 → run 结束 → Shift+← 撤回成功
- [ ] `plans/README.md` 状态行更新为 DONE

## STOP conditions

满足任一条即停止并回报，不要即兴发挥：

- Drift check 显示 in-scope 文件自 `eafaf46` 后有改动，且 "Current state" 摘录与实码不一致。
- 发现某个 `Detach()` 调用点存在**仍可 Commit 的在途 `SteerDelivery`**（即交付可以跨过 Detach 存活）——本修复的安全论证失效，需重新设计。
- 任何一步的验证命令失败两次且原因不明。
- 修复看起来必须触碰 out-of-scope 文件（如需要改 `orchestration_llm.go`、`pkg/tui/**`）。
- 任何现有测试因本改动失败——尤其 `TestInputQueueNextBoundaries` 或 TUI 侧 characterization 测试（说明 detach 窗口语义比本计划论证的更复杂）。

## Maintenance notes

- **为什么 `q.rt == nil` 不等于"已交付"**：`Detach()` 只断开引用不移动消息；真正的交付只会经 `BeginSteerDelivery → Commit → steersDelivered` 把消息从 `steers` 移到 `delivered`。Runtime detach 发生在 run 结束之后，此时未 drain 的 steer 仍在 `steers`，理应可撤回。
- **设计决策**：选择在 `Recall()` 放宽条件而非在 `Detach()` 做清理，因为 `Recall()` 是撤回语义的唯一入口，`InputQueue.steers` 的存在本身就是"未交付、可撤回"的标志；`Detach()` 调用点多且各有后续 boundary 决策，不宜加逻辑。
- **审阅重点**：`case laneSteer` 只放宽 `q.rt == nil` 一条路径；attached 路径（`RetractLastSteer` 失败即跳过整条 lane）必须原样保留。
- **未来交互**：若有人给 `Next()`/boundary 或 subagent 通道（`subagent.go:1886`）改队列语义，注意 Recall 与 `steersDelivered` 都依赖 `q.mu` 串行这一前提。
- 明确不在本计划内：gateway/web 端同场景验证（引擎层修复对两个 surface 同时生效，web 无独立撤回入口改动需求）。
