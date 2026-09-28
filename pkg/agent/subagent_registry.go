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
	mu    sync.RWMutex
	byKey map[string]*Handle
}

func NewRegistry() *Registry {
	return &Registry{byKey: make(map[string]*Handle)}
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
		r.byKey[k] = h
	}
	return h
}

// remove deletes the given keys from the live index, but only when they still
// point at this handle (so a later Start that reused a key is not clobbered).
func (r *Registry) remove(keys []string, h *Handle) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range keys {
		if r.byKey[k] == h {
			delete(r.byKey, k)
		}
	}
}

func (r *Registry) Get(q Query) (*Handle, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var h *Handle
	switch {
	case strings.TrimSpace(q.TaskID) != "":
		h = r.byKey["task:"+strings.TrimSpace(q.TaskID)]
	case strings.TrimSpace(q.RunID) != "":
		h = r.byKey["run:"+strings.TrimSpace(q.RunID)]
	default:
		// Require an explicit task_id or run_id; the live index is not scanned
		// for a "latest" match here.
		return nil, false
	}
	if h == nil {
		return nil, false
	}
	// Enforce the session / parent-run scope carried by the query so an id from
	// one session cannot resolve a handle owned by another.
	if !matchesQuery(h.Snapshot(), q) {
		return nil, false
	}
	return h, true
}

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
	seen := make(map[*Handle]struct{}, len(r.byKey))
	for _, handle := range r.byKey {
		if handle == nil {
			continue
		}
		if _, ok := seen[handle]; ok {
			continue
		}
		seen[handle] = struct{}{}
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

func (r *Registry) Cancel(q Query) bool {
	handle, ok := r.Get(q)
	if !ok || handle == nil {
		return false
	}
	// Do not report a successful cancel for a subagent that has already finished.
	if handle.IsDone() {
		return false
	}
	return handle.Cancel()
}

func sortHistory(out []HistoryEntry) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].StartedAt > out[j].StartedAt
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
}
