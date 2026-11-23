# Plan 172：审批槽在等待者放弃后必须删掉自己，且删除要认对象

> **Addendum（2026-10-27 执行批，REVISE 依据）**：执行者 200 次复现证明 Step 1 测试 1 的编排**确定性到不了**
> 目标路径——`releaseApprovalSlot` 的 `<-slot.ch` 会对已 park 的等待者 send 做**同步移交**（等待者成为新持有者、
> nil 返回），`Release()` 之后的 cancel 必然迟到。且复核发现 Step 2 的 `dropApprovalSlotLocked`（只查
> `waiters`）在"等待者取消时持有者仍持槽"（最常见的按停止路径）会把**被持有的槽**删掉——下一次 acquire 建
> 新槽 ⇒ 同一 run 两个持有者 ⇒ 互斥失效。修正：
> ① `dropApprovalSlotLocked` 的守卫改为 `slot.waiters != 0 || len(slot.ch) != 0`（"没人在等 **且** 没人持有"
> ——len(ch) 在 approvalSlotMu 下读是稳定的：注册在锁内、release 的收也在锁内、waiters==0 时不可能有在飞的
> 注册 sender）；
> ② 测试 1 改为两段确定性构造：(a) API 级——A 持槽 + 等待者 B cancel（B 的 send 被 full channel 挡住、
> ctx.Done 唯一就绪 ⇒ 确定性 `!took`）⇒ 断言**条目仍在且 channel 仍满**（持有着的 token 未被动过，互斥完好），
> 随后 `A.Release()` ⇒ 条目被删；(b) 单元级——同包直测 `dropApprovalSlotLocked` 三态（空 channel+0 等待⇒删；
> 有 token⇒不删；身份不符⇒不删）。测试 2（身份护栏）不变——其红已由执行者按计划原文复现。

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "func AcquireInPlaceApprovalSlot" pkg/tool/inline_approval.go` 为 `1`；
> `grep -c "func releaseApprovalSlot" pkg/tool/inline_approval.go` 为 `1`；
> `grep -c "if slot.waiters == 0" pkg/tool/inline_approval.go` 为 `1`；
> `grep -c "func TestInPlaceSlotSerializesTwoGatesOfOneRun" pkg/run/streaming_tools_test.go` 为 `1`。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/tool/inline_approval.go` 必须**无输出**。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 2）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1`。

## Status

- **Priority**: P3（进程级 map 缓慢长；单条开销极小）
- **Effort**: S（一处清理 + 一处身份校验 + 1 个测试）
- **Risk**: LOW（但**必须**同时加身份校验，否则会把并发的另一个会话的槽删掉——见 Why）
- **Depends on**: 无
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: bug（资源泄漏 + 一个必须一起修的竞态）

## Why this matters

`approvalSlots` 是包级 `map[runID]*runApprovalSlot`，它保证"一次运行同时只有一个原地审批在问"
（`pkg/run` 的 `TestInPlaceSlotSerializesTwoGatesOfOneRun` 就是钉这条）。槽的生命周期有两个终点：

- **持有者释放**：`releaseApprovalSlot` 把 token 还回去，然后 `if slot.waiters == 0 { delete(...) }`；
- **等待者被取消**：`AcquireInPlaceApprovalSlot` 里 `slot.waiters--` 之后**直接 return ctx.Err()**，没有任何清理。

于是这条序列会留下一个永不回收的空槽：

1. 调用 A 持有槽（`waiters == 0`）；
2. 调用 B 到达，注册为等待者（`waiters == 1`），阻塞在 `select`；
3. A 释放：`releaseApprovalSlot` 看到 `waiters == 1` ⇒ **不删**；
4. B 的 ctx 被取消（用户按了停止、回合结束）：`waiters--` ⇒ 0，**没有删除**；
5. `approvalSlots[runID]` 永久留着一个空 channel。

`runID` 每个 run 一个 key，gateway/TUI 长跑时这条 map 只增不减。危害不大（单个槽是一个 buffered chan + 计数），
但它让"谁还持有槽"无法从 map 判断，也让这条唯一互斥载体失去可观测性。

**危险点（本计划真正的技术核心）**：只加"`waiters == 0` 就 delete"是**不够**的。
`releaseApprovalSlot` 的删除只按 key，不校验对象身份；而等待者取消后如果立刻删了条目，紧接着
**同一 runID 的新一次 acquire 会创建新槽**，此时迟到的 `releaseApprovalSlot(旧槽)` 会把 `approvalSlots[runID]`
——**现在是新槽**——删掉。那会让互斥失效：两个调用同时认为自己持有槽。

所以本计划必须把"删条目"做成**按对象身份删**：

```go
if slot.waiters == 0 && approvalSlots[key] == slot {
	delete(approvalSlots, key)
}
```

并在等待者取消的路径上复用同一条规则。

## Current state

`pkg/tool/inline_approval.go:60-113`（获取）：

```go
func AcquireInPlaceApprovalSlot(ctx context.Context, runID string) error {
	…
	approvalSlotMu.Lock()
	slot, ok := approvalSlots[runID]
	if !ok {
		slot = &runApprovalSlot{ch: make(chan struct{}, 1)}
		approvalSlots[runID] = slot
	}
	// Registered as a waiter under the same lock the entry is deleted under,
	// so a release running after this point cannot drop the entry out from
	// under this acquisition.
	slot.waiters++
	approvalSlotMu.Unlock()

	took := false
	select {
	case slot.ch <- struct{}{}:
		took = true
	case <-ctx.Done():
	}
	approvalSlotMu.Lock()
	slot.waiters--
	if !took {
		approvalSlotMu.Unlock()          // ← 这里什么都不清理
		return ctx.Err()
	}
	approvalSlotMu.Unlock()
	…
}
```

`:137-151`（释放）：

```go
func releaseApprovalSlot(runID string, slot *runApprovalSlot) {
	if slot == nil {
		return
	}
	approvalSlotMu.Lock()
	defer approvalSlotMu.Unlock()
	select {
	case <-slot.ch: // give the token back
	default:
	}
	if slot.waiters == 0 {
		delete(approvalSlots, strings.TrimSpace(runID))   // ← 只按 key，不认对象
	}
}
```

注意 token 的语义：`slot.ch <- struct{}{}` 表示"被某个调用取走"，`<-slot.ch` 表示"还回来"。
所以**channel 里有值 == 有人持有**，空 == 空闲。

## Scope

**In scope（只改这些）**：
- `pkg/tool/inline_approval.go`（等待者取消时的清理 + 删除按对象身份）
- `pkg/tool/inline_approval_test.go`（若不存在则新建；新增 1 个测试）

**Out of scope（看着相关也不要动）**：
- `ApprovalSlotHolder` / `Release` / `WithApprovalSlotHolder` 的语义
- `pkg/run` 的 `TestInPlaceSlotSerializesTwoGatesOfOneRun` 与原地审批的调用链
- 把槽的生命周期挂到 run 结束（那要引入新的生命周期回调，本计划不需要）
- 把 map 换成 `sync.Map`（没有收益，且会让"加锁下判身份"变难）

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 定向测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run '^TestCancelledWaiterLeavesNoSlotBehind$' -count=1 -v` | PASS |
| **竞态** | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -race -tags fts5 ./pkg/tool -run '^(TestCancelledWaiterLeavesNoSlotBehind|TestApprovalSlotSerializesHoldersOfOneRun)$' -count=5` | PASS，无 `DATA RACE` |
| 本包回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` | 与基线相比无新增 FAIL |
| 引擎回归（真正的消费者） | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'InPlace\|Approval' -count=1` | PASS |
| 编译 / vet | `CGO_ENABLED=1 go build -tags fts5 ./...`；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tool` | exit 0 |
| gofmt | `gofmt -l pkg/tool` | 无输出 |

## Steps

### Step 1：先写会红的测试

新文件 `pkg/tool/inline_approval_test.go`（同包，可直接读 `approvalSlots`）：

```go
// A waiter whose context dies after the holder released must not leave an empty
// slot behind: the entry is deleted by the last party to leave.
func TestCancelledWaiterLeavesNoSlotBehind(t *testing.T) {
	const runID = "run-cancel-waiter"
	holder := &ApprovalSlotHolder{RunID: runID}
	ctxHolder := WithApprovalSlotHolder(context.Background(), holder)
	if err := AcquireInPlaceApprovalSlot(ctxHolder, runID); err != nil {
		t.Fatalf("holder acquire: %v", err)
	}

	waiterCtx, cancelWaiter := context.WithCancel(WithApprovalSlotHolder(context.Background(), &ApprovalSlotHolder{RunID: runID}))
	waitErr := make(chan error, 1)
	go func() { waitErr <- AcquireInPlaceApprovalSlot(waiterCtx, runID) }()

	// 让等待者完成注册（waiters == 1）再释放持有者。
	deadline := time.Now().Add(2 * time.Second)
	for {
		approvalSlotMu.Lock()
		waiters := approvalSlots[runID].waiters
		approvalSlotMu.Unlock()
		if waiters == 1 { break }
		if time.Now().After(deadline) { t.Fatal("the waiter never registered") }
		time.Sleep(time.Millisecond)
	}
	holder.Release()          // waiters == 1 ⇒ 此时不删
	cancelWaiter()            // 等待者放弃 ⇒ 必须是它把条目删掉
	if err := <-waitErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter err = %v, want context.Canceled", err)
	}
	approvalSlotMu.Lock()
	_, still := approvalSlots[runID]
	approvalSlotMu.Unlock()
	if still {
		t.Fatal("the cancelled waiter left an empty slot in the registry")
	}
}
```

再补一个**身份校验**的测试（这是本计划真正的回归护栏：迟到的 release 不许删掉新槽）：

```go
// A release that arrives after the entry was re-created must not delete the new
// slot: two live callers would then both believe they hold it.
func TestLateReleaseLeavesTheNewSlotAlone(t *testing.T) {
	const runID = "run-late-release"
	first := &ApprovalSlotHolder{RunID: runID}
	if err := AcquireInPlaceApprovalSlot(WithApprovalSlotHolder(context.Background(), first), runID); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	// 模拟"条目已被等待者取消删掉，随后新的一次 acquire 建了新槽"：
	firstSlot := first.slot                 // 记下旧对象（同包可见）
	first.Release()                         // 正常释放（此时会删）
	second := &ApprovalSlotHolder{RunID: runID}
	if err := AcquireInPlaceApprovalSlot(WithApprovalSlotHolder(context.Background(), second), runID); err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if second.slot == firstSlot {
		t.Fatal("the second acquire must build a new slot")
	}
	releaseApprovalSlot(runID, firstSlot)   // 迟到的一次释放
	approvalSlotMu.Lock()
	still := approvalSlots[runID] == second.slot
	approvalSlotMu.Unlock()
	if !still {
		t.Fatal("a stale release deleted the live slot; the run's mutex is gone")
	}
	second.Release()
}
```

**Verify**：`… -run '^(TestCancelledWaiterLeavesNoSlotBehind|TestLateReleaseLeavesTheNewSlotAlone)$' -count=1 -v`
→ **两个都 FAIL**（第一个 "the cancelled waiter left an empty slot"；第二个 "a stale release deleted the live slot"）。
若有一个直接 PASS，STOP 3。

### Step 2：按对象身份删，并让最后一个离开的人删

在 `pkg/tool/inline_approval.go` 新增一个私有函数（**唯一**的删除点）：

```go
// dropApprovalSlotLocked removes runID's entry when it is still slot and nobody
// waits on it. Locked by approvalSlotMu. The entry is matched by identity, not
// by key: a waiter that gave up may have caused the entry to be re-created, and
// deleting whatever the key now holds would hand the run's mutex to two callers
// at once.
func dropApprovalSlotLocked(runID string, slot *runApprovalSlot) {
	if slot == nil || slot.waiters != 0 {
		return
	}
	key := strings.TrimSpace(runID)
	if current, ok := approvalSlots[key]; ok && current == slot {
		delete(approvalSlots, key)
	}
}
```

`releaseApprovalSlot` 尾段换成：

```go
	dropApprovalSlotLocked(runID, slot)
```

等待者取消的分支换成：

```go
	approvalSlotMu.Lock()
	slot.waiters--
	if !took {
		// The last party to leave cleans up: the holder may already have
		// released while this waiter was still registered, so this is the
		// only place left that can drop the entry.
		dropApprovalSlotLocked(runID, slot)
		approvalSlotMu.Unlock()
		return ctx.Err()
	}
	approvalSlotMu.Unlock()
```

**Verify**：`… -run '^(TestCancelledWaiterLeavesNoSlotBehind|TestLateReleaseLeavesTheNewSlotAlone)$' -count=1 -v` → 两个 PASS。

### Step 3：竞态与消费者回归

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -race -tags fts5 ./pkg/tool -run '^(TestCancelledWaiterLeavesNoSlotBehind|TestLateReleaseLeavesTheNewSlotAlone)$' -count=5`
→ 无 `DATA RACE`；
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run -count=1`
→ 与基线相比无新增 FAIL，特别确认 `TestInPlaceSlotSerializesTwoGatesOfOneRun`（`pkg/run/streaming_tools_test.go`）
与 `pkg/tool` 里所有审批相关用例仍绿。

## Test plan

- `TestCancelledWaiterLeavesNoSlotBehind`：持有者先释放、等待者后取消 ⇒ 条目被删。
- `TestLateReleaseLeavesTheNewSlotAlone`：迟到的 release 不许删掉新槽（互斥的回归护栏）。
- 两者都用 `-race -count=5` 跑一遍，确保加锁路径无竞态。

## Done criteria

机器可检查，全部满足：

- [ ] 两个新测试 PASS，且 `-race -count=5` 无 `DATA RACE`
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run -count=1` 与基线相比无新增 FAIL
- [ ] `grep -c "func dropApprovalSlotLocked" pkg/tool/inline_approval.go` 为 `1`
- [ ] `grep -c "delete(approvalSlots," pkg/tool/inline_approval.go` 为 `1`（**只有**那个函数里一处删除）
- [ ] `grep -c "current == slot" pkg/tool/inline_approval.go` 为 `1`
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0；`gofmt -l pkg/tool` 无输出
- [ ] `git status --porcelain` 只显示 Scope 里的两个文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数 ≠ 1，或 drift check 有输出且"Current state"摘录与现场代码对不上。
2. 基线命令本身跑不起来（编译失败/环境错误），拿不到失败清单。
3. 两个新测试里有一个在 Step 1 直接 PASS（说明泄漏或身份校验已经存在）。
4. 你只做了"`waiters == 0` 就 delete"而没做身份校验：**这会让互斥失效**（两条并发调用同时认为自己持有槽）。
   Step 2 的两个函数必须一起改，Done criteria 里的 `current == slot` 就是这条的机器检查。
5. `-race` 报出与本次改动相关的竞态：把报告原文贴出来，**不要**用"再加一把锁"糊过去。
6. 你发现 `slot.ch` 的读写还有别处在锁外进行（`Acquire` 里的 `slot.ch <- struct{}{}` 是刻意的，不能移进锁内）：
   报告你的分析，不要重构那段 select。

## Maintenance notes

- 这条 map 的唯一删除点现在是 `dropApprovalSlotLocked`。谁将来要"按 run 结束清理槽"，也走这个函数，
  并保证在 `approvalSlotMu` 下调用。
- 身份校验不是洁癖：`releaseApprovalSlot` 是从 `ApprovalSlotHolder.Release` 调来的，而 holder 可能在条目
  重建之后才被执行到——没有身份校验就是互斥失效。
- 评审重点：`acquire` 里的 `slot.ch <- struct{}{}` 必须在锁外（否则等待者持锁阻塞，持有者的 release 进不来）。
  本计划的 diff 不应移动那段 select。
