package session

import (
	"context"
	"errors"
	"strings"
	"sync"
)

var ErrID = errors.New("session: empty session ID")

// Locker serializes foreground work per session. Different sessions never
// share a lock and can proceed concurrently.
type Locker struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

// NewLocker creates an empty per-session lock set.
func NewLocker() *Locker {
	return &Locker{locks: make(map[string]chan struct{})}
}

// Lock waits for a session slot and returns an idempotent release function.
func (l *Locker) Lock(ctx context.Context, id string) (func(), error) {
	if l == nil {
		return nil, errors.New("session: nil locker")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrID
	}
	if ctx == nil {
		ctx = context.Background()
	}
	l.mu.Lock()
	ch := l.locks[id]
	if ch == nil {
		ch = make(chan struct{}, 1)
		ch <- struct{}{}
		l.locks[id] = ch
	}
	l.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-ch:
	}
	var once sync.Once
	return func() { once.Do(func() { ch <- struct{}{} }) }, nil
}
