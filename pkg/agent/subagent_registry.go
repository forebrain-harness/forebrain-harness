package agent

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Handle struct {
	mu     sync.RWMutex
	entry  HistoryEntry
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once

	reg  *Registry
	keys []string
}

func (h *Handle) Snapshot() HistoryEntry {
	if h == nil {
		return HistoryEntry{}
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return normalizeEntry(h.entry)
}

func (h *Handle) Finish(entry HistoryEntry) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.entry = normalizeEntry(entry)
	reg := h.reg
	keys := h.keys
	h.mu.Unlock()
	h.once.Do(func() {
		close(h.done)
		// Drop the handle from the registry once it has finished so the live
		// index does not grow without bound and stale ids cannot be cancelled.
		if reg != nil {
			reg.remove(keys, h)
		}
	})
}

func (h *Handle) Wait(ctx context.Context) (HistoryEntry, error) {
	if h == nil {
		return HistoryEntry{}, context.Canceled
	}
	select {
	case <-h.done:
		return h.Snapshot(), nil
	case <-ctx.Done():
		return h.Snapshot(), ctx.Err()
	}
}

func (h *Handle) Cancel() bool {
	if h == nil || h.cancel == nil {
		return false
	}
	h.cancel()
	return true
}

// IsDone reports whether the subagent has already finished (its done channel is
// closed). Cancelling a finished subagent is a no-op, so callers use this to
// avoid reporting a successful cancel for work that already completed.
func (h *Handle) IsDone() bool {
	if h == nil {
		return true
	}
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

type Registry struct {
	mu sync.RWMutex
	// byKey files every live handle under each key it answers to, oldest
	// first. One subagent can have more than one execution in flight — a
	// subagent_continue waiting behind the execution it continues — and they
	// share its task and run ids, so a key holds them all: a cancel by that id
	// stops the subagent, not only the execution started last.
	byKey map[string][]*Handle
}

func NewRegistry() *Registry {
	return &Registry{byKey: make(map[string][]*Handle)}
}

// registries holds one live index per agent workspace root. The in-memory index
// must carry the same tenancy as the ledger stored beside it: a single
// process-wide map lets one agent's roster see another agent's in-flight work
// whenever a query does not pin a session — and lets "cancel all agents", which
// scans the index unscoped, cancel subagents belonging to a different tenant.
var (
	registriesMu sync.Mutex
	registries   = map[string]*Registry{}
)

// RegistryFor returns the live subagent index owned by one agent workspace
// root. Pass the same root used for AppendHistory and ListMerged; the empty
// root is the unscoped bucket for callers that have no agent context yet.
//
// The key is normalized the way the ledger normalizes its path, so the two
// tenancies cannot diverge: writer and reader reach the same bucket whether one
// of them carries a trailing separator or an unresolved relative root. A
// relative root is not a workspace anyone can write a ledger for (AppendHistory
// refuses it), so it collapses to the unscoped bucket rather than minting a
// private index nobody else would find.
func RegistryFor(workspaceRoot string) *Registry {
	key := registryKey(workspaceRoot)
	registriesMu.Lock()
	defer registriesMu.Unlock()
	reg, ok := registries[key]
	if !ok {
		reg = NewRegistry()
		registries[key] = reg
	}
	return reg
}

func registryKey(workspaceRoot string) string {
	root := strings.TrimSpace(workspaceRoot)
	if root == "" || !filepath.IsAbs(root) {
		return ""
	}
	return filepath.Clean(root)
}

func (r *Registry) Start(entry HistoryEntry, cancel context.CancelFunc) *Handle {
	if r == nil {
		return nil
	}
	entry = normalizeEntry(entry)
	if entry.Status == "" {
		entry.Status = StatusRunning
	}
	if entry.StartedAt <= 0 {
		now := time.Now().Unix()
		entry.StartedAt = now
		if entry.UpdatedAt <= 0 {
			entry.UpdatedAt = now
		}
	}
	keys := []string{entryKey(entry)}
	if entry.RunID != "" {
		runKey := "run:" + entry.RunID
		if runKey != keys[0] {
			keys = append(keys, runKey)
		}
	}
	h := &Handle{
		entry:  entry,
		cancel: cancel,
		done:   make(chan struct{}),
		reg:    r,
		keys:   keys,
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range keys {
		r.byKey[k] = append(r.byKey[k], h)
	}
	return h
}

// remove takes this handle out from under the given keys, leaving every other
// handle filed there (so a later Start that reused a key is not clobbered).
func (r *Registry) remove(keys []string, h *Handle) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range keys {
		kept := make([]*Handle, 0, len(r.byKey[k]))
		for _, other := range r.byKey[k] {
			if other != h {
				kept = append(kept, other)
			}
		}
		if len(kept) == 0 {
			delete(r.byKey, k)
			continue
		}
		r.byKey[k] = kept
	}
}

// matching returns the live handles the query's explicit id names, oldest
// first, within the session / parent-run scope the query carries — so an id
// from one session cannot resolve a handle owned by another.
func (r *Registry) matching(q Query) []*Handle {
	if r == nil {
		return nil
	}
	var key string
	switch {
	case strings.TrimSpace(q.TaskID) != "":
		key = "task:" + strings.TrimSpace(q.TaskID)
	case strings.TrimSpace(q.RunID) != "":
		key = "run:" + strings.TrimSpace(q.RunID)
	default:
		// Require an explicit task_id or run_id; the live index is not scanned
		// for a "latest" match here.
		return nil
	}
	r.mu.RLock()
	handles := append([]*Handle(nil), r.byKey[key]...)
	r.mu.RUnlock()
	out := handles[:0]
	for _, h := range handles {
		if h != nil && matchesQuery(h.Snapshot(), q) {
			out = append(out, h)
		}
	}
	return out
}

// Get returns the newest live handle the query names.
func (r *Registry) Get(q Query) (*Handle, bool) {
	handles := r.matching(q)
	if len(handles) == 0 {
		return nil, false
	}
	return handles[len(handles)-1], true
}

// List returns one entry per subagent execution identity: the newest live
// handle filed under each handle's own primary key, so an execution waiting
// behind another of the same subagent does not list the subagent twice.
func (r *Registry) List(q Query) []HistoryEntry {
	if r == nil {
		return nil
	}
	limit := q.Limit
	unbounded := limit < 0
	if limit == 0 {
		limit = 20
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]HistoryEntry, 0, len(r.byKey))
	for key, handles := range r.byKey {
		// A handle is filed under its run key too; it is listed once, under
		// the key that names it first.
		var handle *Handle
		for i := len(handles) - 1; i >= 0; i-- {
			if h := handles[i]; h != nil && h.keys[0] == key {
				handle = h
				break
			}
		}
		if handle == nil {
			continue
		}
		entry := handle.Snapshot()
		if !matchesQuery(entry, q) {
			continue
		}
		out = append(out, entry)
	}
	sortHistory(out)
	if !unbounded && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Cancel stops every live execution the query names: a subagent's id answers
// for all of its executions in flight, so stopping it leaves none of them
// running. It reports false when none was left to stop.
func (r *Registry) Cancel(q Query) bool {
	cancelled := false
	for _, handle := range r.matching(q) {
		// A subagent that has already finished is not reported as cancelled.
		if handle.IsDone() {
			continue
		}
		if handle.Cancel() {
			cancelled = true
		}
	}
	return cancelled
}

func sortHistory(out []HistoryEntry) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].StartedAt > out[j].StartedAt
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
}
