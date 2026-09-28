package event

import (
	"sync"
)

type NotifyHandler func(event TaskEvent)

type Notifier struct {
	mu      sync.RWMutex
	subs    map[string][]NotifyHandler
	pending map[string][]TaskEvent
}

func NewNotifier() *Notifier {
	return &Notifier{
		subs:    make(map[string][]NotifyHandler),
		pending: make(map[string][]TaskEvent),
	}
}

func (n *Notifier) Subscribe(sessionID string, h NotifyHandler) func() {
	if n == nil || sessionID == "" || h == nil {
		return func() {}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.subs[sessionID] = append(n.subs[sessionID], h)
	idx := len(n.subs[sessionID]) - 1
	return func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		list := n.subs[sessionID]
		if idx < len(list) {
			list[idx] = nil
			n.subs[sessionID] = list
		}
	}
}

func (n *Notifier) Publish(sessionID string, event TaskEvent) {
	if n == nil {
		return
	}
	n.mu.Lock()
	var handlers []NotifyHandler
	if sessionID != "" {
		handlers = append([]NotifyHandler(nil), n.subs[sessionID]...)
	}
	delivered := false
	for _, h := range handlers {
		if h != nil {
			delivered = true
			break
		}
	}
	if !delivered && sessionID != "" {
		n.pending[sessionID] = append(n.pending[sessionID], event)
	}
	n.mu.Unlock()

	for _, h := range handlers {
		if h != nil {
			h(event)
		}
	}
}

func (n *Notifier) Drain(sessionID string) []TaskEvent {
	if n == nil || sessionID == "" {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	evts := n.pending[sessionID]
	delete(n.pending, sessionID)
	return evts
}

func (n *Notifier) PublishAll(event TaskEvent) {
	if n == nil {
		return
	}
	n.mu.RLock()
	var all []NotifyHandler
	for _, list := range n.subs {
		all = append(all, list...)
	}
	n.mu.RUnlock()
	for _, h := range all {
		if h != nil {
			h(event)
		}
	}
}
