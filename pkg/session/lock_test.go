package session

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestLockerSerializesOneSession(t *testing.T) {
	l := NewLocker()
	a, err := l.Lock(context.Background(), "same")
	if err != nil {
		t.Fatal(err)
	}
	defer a()

	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		b, err := l.Lock(context.Background(), "same")
		if err != nil {
			t.Errorf("lock: %v", err)
			close(done)
			return
		}
		close(started)
		b()
		close(done)
	}()
	select {
	case <-started:
		t.Fatal("same session acquired twice")
	case <-time.After(20 * time.Millisecond):
	}
	a()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second lock did not proceed")
	}
}

func TestLockerAllowsDifferentSessionsConcurrently(t *testing.T) {
	l := NewLocker()
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			u, err := l.Lock(context.Background(), id)
			if err != nil {
				t.Errorf("lock %s: %v", id, err)
				return
			}
			u()
		}(id)
	}
	wg.Wait()
}
