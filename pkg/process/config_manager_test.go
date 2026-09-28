package process

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/run"
)

func TestConfigManagerDefersUntilIdle(t *testing.T) {
	probe := &activeProbe{n: 1}
	var applied atomic.Int32
	manager := NewConfigManager(probe, func() error {
		applied.Add(1)
		return nil
	})
	if err := manager.Request(); err != nil || applied.Load() != 0 {
		t.Fatalf("request = %v, applied = %d", err, applied.Load())
	}
	probe.n = 0
	if err := manager.Idle(); err != nil || applied.Load() != 1 {
		t.Fatalf("idle = %v, applied = %d", err, applied.Load())
	}
	if err := manager.Idle(); err != nil || applied.Load() != 1 {
		t.Fatalf("second idle = %v, applied = %d", err, applied.Load())
	}
}

type activeProbe struct{ n int }

func (p *activeProbe) Active() int { return p.n }

func TestConfigManagerAppliesRequestQueuedDuringApply(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	m := NewConfigManager(nil, func() error {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil
	})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := m.Request(); err != nil {
			t.Errorf("first request: %v", err)
		}
	}()
	<-started
	if err := m.Request(); err != nil {
		t.Fatalf("second request: %v", err)
	}
	close(release)
	wg.Wait()
	if got := calls.Load(); got != 2 {
		t.Fatalf("apply calls = %d, want 2", got)
	}
}

func TestClearConfigHotReloadKeepsNewWatcher(t *testing.T) {
	env := &Environment{}
	oldStop := func() {}
	newStop := func() {}
	env.watchID = 1
	env.watchStop = oldStop
	env.watchID = 2
	env.watchStop = newStop

	env.clearConfigHotReload(1)
	if env.watchStop == nil {
		t.Fatal("old watcher cleared new watcher")
	}
	env.clearConfigHotReload(2)
	if env.watchStop != nil {
		t.Fatal("current watcher not cleared")
	}
}

func TestConfigManagerDefersForRunController(t *testing.T) {
	control := run.NewController()
	if !control.Track("run-1", "session-1", func() {}) {
		t.Fatal("track run")
	}
	var applied atomic.Int32
	m := NewConfigManager(control, func() error {
		applied.Add(1)
		return nil
	})
	if err := m.Request(); err != nil || applied.Load() != 0 {
		t.Fatalf("request = %v, applied = %d", err, applied.Load())
	}
	if !control.Cancel("run-1", context.Canceled) || !control.Untrack("run-1") {
		t.Fatal("finish run")
	}
	if err := m.Idle(); err != nil || applied.Load() != 1 {
		t.Fatalf("idle = %v, applied = %d", err, applied.Load())
	}
}
