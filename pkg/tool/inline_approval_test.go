package tool

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func holderHolds(h *ApprovalSlotHolder) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.held
}

// Two holders of one run take the slot one after the other: the second only
// acquires once the first released it.
func TestApprovalSlotSerializesHoldersOfOneRun(t *testing.T) {
	runID := "run-slot-serial"
	h1 := &ApprovalSlotHolder{RunID: runID}
	h2 := &ApprovalSlotHolder{RunID: runID}
	ctx := context.Background()
	if err := AcquireInPlaceApprovalSlot(WithApprovalSlotHolder(ctx, h1), runID); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if !holderHolds(h1) {
		t.Fatal("first holder must hold the slot after acquiring")
	}
	acquired := make(chan error, 1)
	go func() {
		acquired <- AcquireInPlaceApprovalSlot(WithApprovalSlotHolder(ctx, h2), runID)
	}()
	select {
	case err := <-acquired:
		t.Fatalf("second holder acquired while the first held the slot: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	h1.Release()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("second acquire after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second holder never acquired after the first released")
	}
	if !holderHolds(h2) {
		t.Fatal("second holder must hold the slot after acquiring")
	}
	h2.Release()
	approvalSlotMu.Lock()
	_, entry := approvalSlots[runID]
	approvalSlotMu.Unlock()
	if entry {
		t.Fatal("the slot registry must drop a run with no holder and no waiter")
	}
}

// Holders of different runs never wait on each other.
func TestApprovalSlotKeepsRunsIndependent(t *testing.T) {
	ha := &ApprovalSlotHolder{RunID: "run-slot-a"}
	hb := &ApprovalSlotHolder{RunID: "run-slot-b"}
	ctx := context.Background()
	if err := AcquireInPlaceApprovalSlot(WithApprovalSlotHolder(ctx, ha), "run-slot-a"); err != nil {
		t.Fatalf("acquire a: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- AcquireInPlaceApprovalSlot(WithApprovalSlotHolder(ctx, hb), "run-slot-b")
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("acquire b while a holds its own run's slot: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("across runs the slot must not serialize")
	}
	ha.Release()
	hb.Release()
}

// A cancelled context returns its error without taking the slot, and the next
// acquirer is not blocked by the abandoned attempt.
func TestApprovalSlotCancelReturnsErrorAndTakesNothing(t *testing.T) {
	runID := "run-slot-cancel"
	keeper := &ApprovalSlotHolder{RunID: runID}
	ctx := context.Background()
	if err := AcquireInPlaceApprovalSlot(WithApprovalSlotHolder(ctx, keeper), runID); err != nil {
		t.Fatalf("keeper acquire: %v", err)
	}
	waiter := &ApprovalSlotHolder{RunID: runID}
	waitCtx, cancel := context.WithCancel(WithApprovalSlotHolder(ctx, waiter))
	returned := make(chan error, 1)
	go func() { returned <- AcquireInPlaceApprovalSlot(waitCtx, runID) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-returned:
		if err == nil {
			t.Fatal("cancelled acquire must return an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled acquire never returned")
	}
	if holderHolds(waiter) {
		t.Fatal("a cancelled acquire must not hold the slot")
	}
	keeper.Release()
	next := &ApprovalSlotHolder{RunID: runID}
	if err := AcquireInPlaceApprovalSlot(WithApprovalSlotHolder(ctx, next), runID); err != nil {
		t.Fatalf("acquire after cancelled attempt: %v", err)
	}
	next.Release()
}

// A holder that already holds the slot acquires again without blocking, and
// Release is idempotent.
func TestApprovalSlotReentrantAcquireAndIdempotentRelease(t *testing.T) {
	runID := "run-slot-reentrant"
	h := &ApprovalSlotHolder{RunID: runID}
	ctx := WithApprovalSlotHolder(context.Background(), h)
	if err := AcquireInPlaceApprovalSlot(ctx, runID); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	returned := make(chan error, 1)
	go func() { returned <- AcquireInPlaceApprovalSlot(ctx, runID) }()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("second gate of the same holder must not block: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the same holder blocked on its own slot")
	}
	var wg sync.WaitGroup
	wg.Add(3)
	for i := 0; i < 3; i++ {
		go func() {
			defer wg.Done()
			h.Release()
		}()
	}
	wg.Wait()
	if holderHolds(h) {
		t.Fatal("holder still holds after Release")
	}
	fresh := &ApprovalSlotHolder{RunID: runID}
	if err := AcquireInPlaceApprovalSlot(WithApprovalSlotHolder(context.Background(), fresh), runID); err != nil {
		t.Fatalf("slot must be free after release: %v", err)
	}
	fresh.Release()
}

// A waiter that gives up while the holder still holds must leave the slot in
// place — deleting a held slot would let the next acquire build a second one
// and break the run's mutex. The last party to leave (here: the release)
// drops the entry.
func TestCancelledWaiterLeavesNoSlotBehind(t *testing.T) {
	const runID = "run-cancel-waiter"
	holder := &ApprovalSlotHolder{RunID: runID}
	if err := AcquireInPlaceApprovalSlot(WithApprovalSlotHolder(context.Background(), holder), runID); err != nil {
		t.Fatalf("holder acquire: %v", err)
	}
	waiterCtx, cancelWaiter := context.WithCancel(WithApprovalSlotHolder(context.Background(), &ApprovalSlotHolder{RunID: runID}))
	waitErr := make(chan error, 1)
	go func() { waitErr <- AcquireInPlaceApprovalSlot(waiterCtx, runID) }()
	// 让等待者完成注册（waiters == 1）。
	deadline := time.Now().Add(2 * time.Second)
	for {
		approvalSlotMu.Lock()
		waiters := approvalSlots[runID].waiters
		approvalSlotMu.Unlock()
		if waiters == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the waiter never registered")
		}
		time.Sleep(time.Millisecond)
	}
	// 持有者仍持槽时取消：等待者的 send 被 full channel 挡住，ctx.Done 是唯一就绪分支 ⇒ 确定性 !took。
	cancelWaiter()
	if err := <-waitErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter err = %v, want context.Canceled", err)
	}
	approvalSlotMu.Lock()
	slot, still := approvalSlots[runID]
	held := still && len(slot.ch) == 1
	approvalSlotMu.Unlock()
	if !held {
		t.Fatal("a waiter's exit dropped the slot its holder still holds; the next acquire would build a second one")
	}
	// 最后离开的一方（这里是 release）删掉条目。
	holder.Release()
	approvalSlotMu.Lock()
	_, still = approvalSlots[runID]
	approvalSlotMu.Unlock()
	if still {
		t.Fatal("the last party to leave must drop the entry")
	}
}

// dropApprovalSlotLocked is the one deletion point: its contract is "nobody
// waits, nobody holds, and the entry is still this slot" — each clause guards
// one failure (a leak, a broken mutex, a stale release).
func TestDropApprovalSlotLockedContract(t *testing.T) {
	const runID = "run-drop-contract"
	free := &runApprovalSlot{ch: make(chan struct{}, 1)}
	approvalSlotMu.Lock()
	approvalSlots[runID] = free
	approvalSlotMu.Unlock()
	dropApprovalSlotLocked(runID, free)
	approvalSlotMu.Lock()
	_, still := approvalSlots[runID]
	approvalSlotMu.Unlock()
	if still {
		t.Fatal("a free slot with no waiters must be dropped")
	}

	held := &runApprovalSlot{ch: make(chan struct{}, 1)}
	held.ch <- struct{}{}
	approvalSlotMu.Lock()
	approvalSlots[runID] = held
	approvalSlotMu.Unlock()
	dropApprovalSlotLocked(runID, held)
	approvalSlotMu.Lock()
	_, still = approvalSlots[runID]
	approvalSlotMu.Unlock()
	if !still {
		t.Fatal("a held slot must survive: the next acquire would build a second one")
	}

	fresh := &runApprovalSlot{ch: make(chan struct{}, 1)}
	approvalSlotMu.Lock()
	approvalSlots[runID] = fresh
	approvalSlotMu.Unlock()
	dropApprovalSlotLocked(runID, held) // 迟到的持有者：身份不符
	approvalSlotMu.Lock()
	current := approvalSlots[runID]
	approvalSlotMu.Unlock()
	if current != fresh {
		t.Fatal("a stale drop must not touch the live slot")
	}
	// 收尾：把 fresh 摘掉，别把状态留给别的用例。
	approvalSlotMu.Lock()
	delete(approvalSlots, runID)
	approvalSlotMu.Unlock()
}

// A release that arrives after the entry was re-created must not delete the new
// slot: two live callers would then both believe they hold it.
func TestLateReleaseLeavesTheNewSlotAlone(t *testing.T) {
	const runID = "run-late-release"
	first := &ApprovalSlotHolder{RunID: runID}
	if err := AcquireInPlaceApprovalSlot(WithApprovalSlotHolder(context.Background(), first), runID); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	// Simulate "the entry was dropped after a waiter gave up, then a fresh
	// acquire built a new slot":
	firstSlot := first.slot // remember the old object (same package)
	first.Release()         // a normal release (this one drops the entry)
	second := &ApprovalSlotHolder{RunID: runID}
	if err := AcquireInPlaceApprovalSlot(WithApprovalSlotHolder(context.Background(), second), runID); err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if second.slot == firstSlot {
		t.Fatal("the second acquire must build a new slot")
	}
	releaseApprovalSlot(runID, firstSlot) // a late release
	approvalSlotMu.Lock()
	still := approvalSlots[runID] == second.slot
	approvalSlotMu.Unlock()
	if !still {
		t.Fatal("a stale release deleted the live slot; the run's mutex is gone")
	}
	second.Release()
}
