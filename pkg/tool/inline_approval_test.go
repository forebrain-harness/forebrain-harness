package tool

import (
	"context"
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
