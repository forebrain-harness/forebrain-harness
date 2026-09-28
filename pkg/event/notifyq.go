// Package notifyq provides an unbounded, lossless FIFO queue that bridges
// concurrent producers to a single consumer channel.
//
// It exists to replace bounded "chan + drop-on-full" send patterns, which
// silently discard notifications when the consumer falls behind. For control
// messages that fire exactly once (compaction banners, budget refreshes, run
// lifecycle events) a dropped notification has no automatic recovery: the UI
// stays stuck on stale state forever. notifyq guarantees that every pushed item
// is delivered to the consumer in FIFO order, never dropped, while the producer
// never blocks on the consumer.
package event

import (
	"context"
	"sync"
)

// Queue is an unbounded, lossless FIFO of items of type T.
//
// Guarantees:
//   - Push always succeeds, is O(1), and never blocks on the consumer.
//   - Ordering is preserved (FIFO, single mutex).
//   - The consumer reads via the channel returned by Start, which is closed
//     when the pump exits (after ctx is cancelled). When the consumer is busy,
//     items accumulate in the queue (natural backpressure) instead of being
//     lost.
type Queue[T any] struct {
	mu     sync.Mutex
	buf    []T
	signal chan struct{}
}

// New returns a new empty Queue.
func New[T any]() *Queue[T] {
	return &Queue[T]{
		signal: make(chan struct{}, 1),
	}
}

// Push appends item to the end of the queue. It never blocks on the consumer
// and never drops item. Safe to call from any goroutine.
func (q *Queue[T]) Push(item T) {
	q.mu.Lock()
	q.buf = append(q.buf, item)
	q.mu.Unlock()
	// Non-blocking signal to the pump. The signal channel is buffered (cap 1)
	// so the first push after the pump drains always wakes it; extra pushes
	// coalesce harmlessly because the pump re-checks the buffer length under
	// the lock.
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

// Start launches the pump goroutine that forwards queued items to a newly
// created output channel, which it returns. The pump exits after ctx is
// cancelled; on shutdown it makes a best-effort, non-blocking delivery of any
// still-queued items so in-flight notifications are not silently lost. The
// returned channel is closed when the pump exits.
//
// Start must be called at most once per Queue.
func (q *Queue[T]) Start(ctx context.Context) <-chan T {
	out := make(chan T, 1)
	go q.pump(ctx, out)
	return out
}

func (q *Queue[T]) pump(ctx context.Context, out chan<- T) {
	defer close(out)
	for {
		// Block until there is something to deliver or the run is over.
		select {
		case <-ctx.Done():
			q.drainRemaining(out)
			return
		case <-q.signal:
		}

		// Forward every currently-queued item. The lock is held only long
		// enough to dequeue each item, then released for the (potentially
		// blocking) send so producers are not blocked on the consumer.
		for {
			q.mu.Lock()
			if len(q.buf) == 0 {
				q.mu.Unlock()
				break
			}
			item := q.buf[0]
			q.buf = q.buf[1:]
			q.mu.Unlock()

			select {
			case <-ctx.Done():
				// Shutting down: deliver this and the rest without blocking,
				// then stop. The consumer (main loop / WS writer) is tearing
				// down and may already be gone.
				q.bestEffortSend(out, item)
				q.drainRemaining(out)
				return
			case out <- item:
			}
		}
	}
}

// drainRemaining forwards all buffered items with a non-blocking best effort.
// Used only during shutdown so the pump never hangs the process.
func (q *Queue[T]) drainRemaining(out chan<- T) {
	for {
		q.mu.Lock()
		if len(q.buf) == 0 {
			q.mu.Unlock()
			return
		}
		item := q.buf[0]
		q.buf = q.buf[1:]
		q.mu.Unlock()
		q.bestEffortSend(out, item)
	}
}

func (q *Queue[T]) bestEffortSend(out chan<- T, item T) {
	select {
	case out <- item:
	default:
	}
}
