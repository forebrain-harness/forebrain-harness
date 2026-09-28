package event

import (
	"sync"
	"testing"
)

func TestNewNotifier(t *testing.T) {
	n := NewNotifier()
	if n == nil {
		t.Fatal("NewNotifier returned nil")
	}
	if n.subs == nil {
		t.Error("subs map is nil")
	}
	if n.pending == nil {
		t.Error("pending map is nil")
	}
}

func TestSubscribeAndPublish(t *testing.T) {
	n := NewNotifier()
	var mu sync.Mutex
	var events []TaskEvent

	handler := func(event TaskEvent) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}

	unsub := n.Subscribe("sess1", handler)
	if unsub == nil {
		t.Fatal("Subscribe should return a non-nil unsubscribe func")
	}

	n.Publish("sess1", TaskEvent{
		EventKind: EventStarted,
		Task:      Task{ID: "t1", Title: "Test"},
		Message:   "hello",
	})

	mu.Lock()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].EventKind != EventStarted {
		t.Errorf("expected EventStarted, got %q", events[0].EventKind)
	}
	if events[0].Message != "hello" {
		t.Errorf("expected message 'hello', got %q", events[0].Message)
	}
	mu.Unlock()

	// Unsubscribe
	unsub()

	n.Publish("sess1", TaskEvent{
		EventKind: EventDone,
		Task:      Task{ID: "t2"},
	})

	mu.Lock()
	if len(events) != 1 {
		t.Errorf("after unsubscribe, expected still 1 event, got %d", len(events))
	}
	mu.Unlock()
}

func TestSubscribeNilHandler(t *testing.T) {
	n := NewNotifier()
	unsub := n.Subscribe("sess1", nil)
	if unsub == nil {
		t.Fatal("Subscribe with nil handler should still return a no-op func")
	}
	// Should not panic
	unsub()
}

func TestSubscribeEmptySessionID(t *testing.T) {
	n := NewNotifier()
	unsub := n.Subscribe("", func(event TaskEvent) {})
	if unsub == nil {
		t.Fatal("Subscribe with empty session ID should return a no-op func")
	}
}

func TestPublishToNonExistentSession(t *testing.T) {
	n := NewNotifier()
	// Publishing to a session with no subscribers should not panic
	n.Publish("unknown-session", TaskEvent{
		EventKind: EventStarted,
		Task:      Task{ID: "t1"},
	})
}

func TestDrainPendingEvents(t *testing.T) {
	n := NewNotifier()
	// Publish to a session with no subscribers -> goes to pending
	n.Publish("drain-me", TaskEvent{
		EventKind: EventStarted,
		Task:      Task{ID: "t1"},
		Message:   "first",
	})
	n.Publish("drain-me", TaskEvent{
		EventKind: EventProgress,
		Task:      Task{ID: "t1"},
		Message:   "second",
	})

	evts := n.Drain("drain-me")
	if len(evts) != 2 {
		t.Fatalf("expected 2 drained events, got %d", len(evts))
	}
	if evts[0].Message != "first" {
		t.Errorf("expected first message 'first', got %q", evts[0].Message)
	}
	if evts[1].Message != "second" {
		t.Errorf("expected second message 'second', got %q", evts[1].Message)
	}

	// Drain again should return nil
	evts2 := n.Drain("drain-me")
	if evts2 != nil && len(evts2) != 0 {
		t.Errorf("expected empty after drain, got %d events", len(evts2))
	}
}

func TestDrainNonExistentSession(t *testing.T) {
	n := NewNotifier()
	evts := n.Drain("nonexistent")
	if evts != nil && len(evts) != 0 {
		t.Errorf("expected nil/empty for nonexistent session, got %d events", len(evts))
	}
}

func TestPublishDeliversToAllSubscribers(t *testing.T) {
	n := NewNotifier()
	var mu sync.Mutex
	var counts map[string]int
	counts = make(map[string]int)

	h1 := func(event TaskEvent) {
		mu.Lock()
		counts["h1"]++
		mu.Unlock()
	}
	h2 := func(event TaskEvent) {
		mu.Lock()
		counts["h2"]++
		mu.Unlock()
	}

	n.Subscribe("multi", h1)
	n.Subscribe("multi", h2)

	n.Publish("multi", TaskEvent{EventKind: EventStarted})
	n.Publish("multi", TaskEvent{EventKind: EventDone})

	mu.Lock()
	if counts["h1"] != 2 {
		t.Errorf("h1 called %d times; want 2", counts["h1"])
	}
	if counts["h2"] != 2 {
		t.Errorf("h2 called %d times; want 2", counts["h2"])
	}
	mu.Unlock()
}

func TestPublishAll(t *testing.T) {
	n := NewNotifier()
	var mu sync.Mutex
	var received []string

	h1 := func(event TaskEvent) {
		mu.Lock()
		received = append(received, "h1")
		mu.Unlock()
	}
	h2 := func(event TaskEvent) {
		mu.Lock()
		received = append(received, "h2")
		mu.Unlock()
	}

	n.Subscribe("s1", h1)
	n.Subscribe("s2", h2)

	n.PublishAll(TaskEvent{EventKind: EventStarted})

	mu.Lock()
	if len(received) != 2 {
		t.Fatalf("expected 2 received, got %d", len(received))
	}
	mu.Unlock()
}

func TestNilNotifierPublish(t *testing.T) {
	var n *Notifier
	// Should not panic
	n.Publish("sess", TaskEvent{})
	n.PublishAll(TaskEvent{})
	n.Drain("sess")
}

func TestMultipleSubscribeSameSession(t *testing.T) {
	n := NewNotifier()
	var mu sync.Mutex
	var order []string

	h1 := func(event TaskEvent) {
		mu.Lock()
		order = append(order, "h1")
		mu.Unlock()
	}
	h2 := func(event TaskEvent) {
		mu.Lock()
		order = append(order, "h2")
		mu.Unlock()
	}

	u1 := n.Subscribe("batch", h1)
	u2 := n.Subscribe("batch", h2)

	n.Publish("batch", TaskEvent{EventKind: EventStarted})

	mu.Lock()
	if len(order) != 2 || order[0] != "h1" || order[1] != "h2" {
		t.Errorf("expected [h1, h2], got %v", order)
	}
	mu.Unlock()

	u1()
	u2()

	n.Publish("batch", TaskEvent{EventKind: EventDone})

	mu.Lock()
	if len(order) != 2 {
		t.Errorf("after unsubscribing both, expected still 2 calls, got %d", len(order))
	}
	mu.Unlock()
}

func TestPendingThenSubscribe(t *testing.T) {
	n := NewNotifier()
	var mu sync.Mutex
	var events []TaskEvent

	// Publish before subscribing -> goes to pending
	n.Publish("later", TaskEvent{EventKind: EventStarted, Message: "early"})

	// Now subscribe
	handler := func(event TaskEvent) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}
	n.Subscribe("later", handler)

	// Publish again -> delivered immediately
	n.Publish("later", TaskEvent{EventKind: EventDone, Message: "late"})

	mu.Lock()
	if len(events) != 1 {
		t.Fatalf("expected 1 delivered event, got %d", len(events))
	}
	if events[0].Message != "late" {
		t.Errorf("expected message 'late', got %q", events[0].Message)
	}
	mu.Unlock()

	// The early event should be in pending
	pending := n.Drain("later")
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending event, got %d", len(pending))
	}
	if pending[0].Message != "early" {
		t.Errorf("expected pending message 'early', got %q", pending[0].Message)
	}
}

func TestConcurrentSubscribePublish(t *testing.T) {
	n := NewNotifier()
	var mu sync.Mutex
	var count int

	handler := func(event TaskEvent) {
		mu.Lock()
		count++
		mu.Unlock()
	}

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			n.Subscribe("concurrent", handler)
		}
		close(done)
	}()
	<-done

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n.Publish("concurrent", TaskEvent{EventKind: EventProgress})
		}()
	}
	wg.Wait()

	mu.Lock()
	c := count
	mu.Unlock()
	expected := 100 * 50
	if c != expected {
		t.Errorf("expected %d deliveries, got %d", expected, c)
	}
}

func BenchmarkSubscribe(b *testing.B) {
	n := NewNotifier()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n.Subscribe("bench", func(event TaskEvent) {})
	}
}

func BenchmarkPublish(b *testing.B) {
	n := NewNotifier()
	n.Subscribe("bench", func(event TaskEvent) {})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n.Publish("bench", TaskEvent{EventKind: EventProgress})
	}
}
