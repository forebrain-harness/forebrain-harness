package event

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestQueueNeverDrops(t *testing.T) {
	q := New[int]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := q.Start(ctx)

	const n = 50000 // far exceeds any bounded channel cap
	// Producer: push n items as fast as possible from one goroutine, while the
	// consumer drains. With a bounded-drop channel this lost items once full.
	go func() {
		for i := 0; i < n; i++ {
			q.Push(i)
		}
	}()

	seen := make(map[int]bool, n)
	deadline := time.After(30 * time.Second)
	for count := 0; count < n; count++ {
		select {
		case m, ok := <-out:
			if !ok {
				t.Fatalf("channel closed after %d/%d items", count, n)
			}
			if seen[m] {
				t.Fatalf("duplicate item %d", m)
			}
			seen[m] = true
		case <-deadline:
			t.Fatalf("timeout after receiving %d/%d items (lost in flight)", count, n)
		}
	}
	for i := 0; i < n; i++ {
		if !seen[i] {
			t.Fatalf("item %d was lost", i)
		}
	}
}

func TestQueuePreservesOrder(t *testing.T) {
	q := New[int]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := q.Start(ctx)

	const n = 1000
	go func() {
		for i := 0; i < n; i++ {
			q.Push(i)
		}
	}()

	prev := -1
	deadline := time.After(10 * time.Second)
	for count := 0; count < n; count++ {
		select {
		case m := <-out:
			if m != prev+1 {
				t.Fatalf("out of order at index %d: got %d, want %d", count, m, prev+1)
			}
			prev = m
		case <-deadline:
			t.Fatalf("timeout after %d items", count)
		}
	}
}

func TestQueueConcurrentProducersNoLoss(t *testing.T) {
	q := New[int]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := q.Start(ctx)

	const producers = 8
	const perProducer = 5000
	var wg sync.WaitGroup
	wg.Add(producers)
	for p := 0; p < producers; p++ {
		go func(p int) {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				q.Push(p*perProducer + i)
			}
		}(p)
	}

	total := producers * perProducer
	seen := make(map[int]bool, total)
	deadline := time.After(30 * time.Second)
	for count := 0; count < total; count++ {
		select {
		case m, ok := <-out:
			if !ok {
				t.Fatalf("closed after %d/%d", count, total)
			}
			if seen[m] {
				t.Fatalf("duplicate %d", m)
			}
			seen[m] = true
		case <-deadline:
			t.Fatalf("timeout after %d/%d (lost in flight)", count, total)
		}
	}
	for i := 0; i < total; i++ {
		if !seen[i] {
			t.Fatalf("item %d lost", i)
		}
	}
}

func TestQueueShutdownDrainsBestEffort(t *testing.T) {
	q := New[int]()
	ctx, cancel := context.WithCancel(context.Background())
	out := q.Start(ctx)

	for i := 0; i < 10; i++ {
		q.Push(i)
	}
	cancel()

	// Drain whatever is available; the channel must close.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-out:
			if !ok {
				return // closed cleanly
			}
		case <-deadline:
			t.Fatalf("output channel did not close after shutdown")
		}
	}
}
