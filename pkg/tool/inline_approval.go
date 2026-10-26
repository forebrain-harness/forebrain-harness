// The run-wide slot that keeps one run asking one approval question at a
// time, for gates answered where the call is (plan 127).
package tool

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// ErrApprovalRejectedStop is what an in-place approval hook returns when the
// user refused the call without words: the call reads RejectedStopText, the
// calls behind it never start, and the turn ends without another model
// request (plan 129's semantics, answered where the call is).
var ErrApprovalRejectedStop = errors.New("the user refused this call without words")

// ApprovalSlotHolder carries, for one tool call that may be asked about in
// place, the run's approval slot once the call took it. A run has one durable
// wait row, so it asks one question at a time: a second gate waits for the
// first to be answered and its call to finish.
type ApprovalSlotHolder struct {
	RunID string

	mu   sync.Mutex
	held bool
	slot *runApprovalSlot
}

// runApprovalSlot is one run's registry entry: a capacity-1 channel whose
// buffered token is the slot, plus the count of goroutines blocked acquiring
// it, so a release can drop the entry exactly when no one is left waiting.
type runApprovalSlot struct {
	ch      chan struct{}
	waiters int
}

var (
	approvalSlotMu sync.Mutex
	approvalSlots  = map[string]*runApprovalSlot{}
)

func WithApprovalSlotHolder(ctx context.Context, h *ApprovalSlotHolder) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if h == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyApprovalSlotHolder, h)
}

func ApprovalSlotHolderFromContext(ctx context.Context) *ApprovalSlotHolder {
	if ctx == nil {
		return nil
	}
	h, _ := ctx.Value(ctxKeyApprovalSlotHolder).(*ApprovalSlotHolder)
	return h
}

// AcquireInPlaceApprovalSlot takes runID's slot for the call ctx belongs to,
// waiting for it while another call of the run holds it. It is a no-op when
// ctx carries no holder, when the holder is for another run, or when the
// holder already holds the slot (a second gate inside the same call).
func AcquireInPlaceApprovalSlot(ctx context.Context, runID string) error {
	runID = strings.TrimSpace(runID)
	if ctx == nil || runID == "" {
		return nil
	}
	holder := ApprovalSlotHolderFromContext(ctx)
	if holder == nil || strings.TrimSpace(holder.RunID) != runID {
		return nil
	}
	holder.mu.Lock()
	if holder.held {
		holder.mu.Unlock()
		return nil
	}
	holder.mu.Unlock()

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
		// The last party to leave cleans up: the holder may already have
		// released while this waiter was still registered, so this is the
		// only place left that can drop the entry.
		dropApprovalSlotLocked(runID, slot)
		approvalSlotMu.Unlock()
		return ctx.Err()
	}
	approvalSlotMu.Unlock()

	holder.mu.Lock()
	if holder.held {
		// Another gate of this same call won the race to keep the slot: this
		// one needs nothing, and the token goes straight back.
		holder.mu.Unlock()
		releaseApprovalSlot(runID, slot)
		return nil
	}
	holder.held = true
	holder.slot = slot
	holder.mu.Unlock()
	return nil
}

// Release gives the slot back; safe to call more than once and on a holder
// that never took it.
func (h *ApprovalSlotHolder) Release() {
	if h == nil {
		return
	}
	h.mu.Lock()
	slot := h.slot
	h.slot = nil
	h.held = false
	h.mu.Unlock()
	if slot != nil {
		releaseApprovalSlot(h.RunID, slot)
	}
}

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
	dropApprovalSlotLocked(runID, slot)
}

// dropApprovalSlotLocked removes runID's entry when it is still slot, nobody
// waits on it and nobody holds its token. Locked by approvalSlotMu. The entry
// is matched by identity, not by key: a waiter that gave up may have caused
// the entry to be re-created, and deleting whatever the key now holds would
// hand the run's mutex to two callers at once. A held slot survives a
// waiter's exit for the same reason — deleting it would let the next acquire
// build a second slot while the holder still runs. len(ch) is stable under
// the mutex here: registration happens under it, release's receive happens
// under it, and with waiters == 0 no registered sender can be in flight.
func dropApprovalSlotLocked(runID string, slot *runApprovalSlot) {
	if slot == nil || slot.waiters != 0 || len(slot.ch) != 0 {
		return
	}
	key := strings.TrimSpace(runID)
	if current, ok := approvalSlots[key]; ok && current == slot {
		delete(approvalSlots, key)
	}
}
