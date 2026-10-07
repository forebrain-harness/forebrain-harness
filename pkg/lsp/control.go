package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// recState is <StateDir>/recommendations.json (spec §5.4): what the
// recommendation flow remembers between sessions.
type recState struct {
	Disabled        bool     `json:"disabled"`
	DisabledReason  string   `json:"disabled_reason"`
	DismissedStreak int      `json:"dismissed_streak"`
	Never           []string `json:"never"`
}

// recDismissLimit turns recommendations off after this many "not now" in a
// row (spec §10.2); the number is the spec's, not a tuning knob.
const recDismissLimit = 5

// recStatePath names <StateDir>/recommendations.json.
func recStatePath(agentWorkspace string) string {
	return filepath.Join(StateDir(agentWorkspace), "recommendations.json")
}

// loadRecState reads recommendations.json; a missing or unreadable file is
// the zero value, the same unreadable-as-empty rule enabled.json resolves by.
func loadRecState(agentWorkspace string) recState {
	data, err := os.ReadFile(recStatePath(agentWorkspace))
	if err != nil {
		return recState{}
	}
	var st recState
	if json.Unmarshal(data, &st) != nil {
		return recState{}
	}
	return st
}

// saveRecState writes recommendations.json atomically; Never is sorted and
// deduplicated, so the file one surface reads is the one another wrote.
func saveRecState(agentWorkspace string, st recState) error {
	never := make([]string, 0, len(st.Never))
	seen := make(map[string]bool, len(st.Never))
	for _, id := range st.Never {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		never = append(never, id)
	}
	sort.Strings(never)
	st.Never = never
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeFileAtomic(recStatePath(agentWorkspace), data)
}

// ResetRecommendationState turns recommendations back on and forgets every
// "never" (spec §10.2); forebrain lsp recommendations reset and the /lsp
// panel's reset both end here.
func ResetRecommendationState(agentWorkspace string) error {
	return saveRecState(agentWorkspace, recState{})
}

// detectTTL is how long a binary detection answers before a refresh.
const detectTTL = 60 * time.Second

// notifyDebounce coalesces subscriber notifications.
const notifyDebounce = 100 * time.Millisecond

// Snapshot reports what /lsp shows: every configured server with its state,
// the instances' roots, pids and open documents. A closed manager serves no
// surface and answers empty.
func (m *Manager) Snapshot() event.LSPSnapshot {
	snap := event.LSPSnapshot{Servers: []event.LSPServerStatus{}}
	if m == nil {
		return snap
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return snap
	}
	snap.ProjectRoot = m.opts.ProjectRoot
	snap.Trusted = m.opts.Trusted
	snap.ToolRegistered = m.opts.ToolRegistered
	snap.FeatureEnabled = m.pool.config().EffectiveFeatures().LSP
	rec := loadRecState(m.opts.AgentWorkspace)
	snap.RecommendationsDisabled = rec.Disabled
	snap.RecommendationsDisabledReason = rec.DisabledReason
	merged, project := m.resolveCached()
	snap.ProjectNotes = append([]string(nil), project.Notes...)
	for _, sc := range merged {
		snap.Servers = append(snap.Servers, m.serverStatus(sc))
	}
	// Project entries without a decision fail closed (spec §5.2). An
	// undecided override of a known server keeps that server's row but
	// flags it; an undecided or declined entry the merged set does not
	// know gets its own blocked row.
	byID := make(map[string]int, len(snap.Servers))
	for i := range snap.Servers {
		byID[snap.Servers[i].ID] = i
	}
	for _, p := range project.Pending {
		if i, ok := byID[p.ID]; ok {
			snap.Servers[i].Note = "project settings await confirmation; " + snap.Servers[i].Note
			continue
		}
		snap.Servers = append(snap.Servers, event.LSPServerStatus{
			ID:          p.ID,
			DisplayName: p.ID,
			Scope:       "project",
			State:       event.LSPStateBlocked,
			Note:        "project entry awaiting confirmation: start forebrain in this project, or confirm it on the project page",
		})
	}
	for _, id := range project.Denied {
		if _, ok := byID[id]; ok {
			continue
		}
		snap.Servers = append(snap.Servers, event.LSPServerStatus{
			ID:          id,
			DisplayName: id,
			Scope:       "project",
			State:       event.LSPStateBlocked,
			Note:        "project entry declined; it asks again when .forebrain/lsp_servers.yaml changes",
		})
	}
	// Enabled servers first, then by id.
	sort.SliceStable(snap.Servers, func(a, b int) bool {
		if snap.Servers[a].Enabled != snap.Servers[b].Enabled {
			return snap.Servers[a].Enabled
		}
		return snap.Servers[a].ID < snap.Servers[b].ID
	})
	return snap
}

// serverStatus builds one server's slice of the snapshot.
func (m *Manager) serverStatus(sc ServerConfig) event.LSPServerStatus {
	st := event.LSPServerStatus{
		ID:            sc.ID,
		DisplayName:   sc.DisplayName,
		Languages:     sc.Languages,
		Role:          sc.Role,
		Scope:         sc.Scope,
		Enabled:       sc.Enabled,
		Command:       sc.Command,
		ProjectWrites: sc.ProjectWrites,
	}
	var det *DetectResult
	if d := m.detectInfo(sc); d != nil {
		det = d
		st.BinaryPath = d.Path
		st.Version = d.Version
		st.InstallCommand = d.InstallCommand
	}
	m.mu.Lock()
	// The install state is written under the same mutex (Install appends
	// log lines and clears `running` while Snapshot can run), so the copy
	// stays inside the critical section.
	install := m.installs[sc.ID]
	if install != nil {
		st.Installing = install.running
		st.InstallLog = append([]string(nil), install.log...)
		st.InstallError = install.err
	}
	m.mu.Unlock()
	entries := m.pool.instancesOfProject(sc.ID, m.opts.ProjectRoot)
	if len(entries) > 0 {
		st.State = mapInstanceState(entries[0].stateOf())
		for _, e := range entries {
			st.Roots = append(st.Roots, e.root)
			if e.inst != nil {
				if pid := e.inst.PID(); pid != 0 {
					st.PIDs = append(st.PIDs, pid)
				}
				if pct := e.inst.Progress(); pct > st.IndexingPercent {
					st.IndexingPercent = pct
				}
				if msg := e.inst.LastError(); msg != "" {
					st.LastError = msg
				}
			}
			if e.docs != nil {
				st.OpenDocuments += e.docs.Count()
			}
			st.LogPath = LogPath(m.opts.AgentWorkspace, sc.ID, e.root)
		}
		errs, warns := m.pool.diags.Counts(sc.ID)
		st.Errors, st.Warnings = errs, warns
		sort.Strings(st.Roots)
		sort.Ints(st.PIDs)
		if st.IndexingPercent < 0 {
			st.IndexingPercent = 0 // -1: no token reported a percentage
		}
		return st
	}
	switch {
	case sc.Invalid != "":
		st.State = event.LSPStateBlocked
		st.Note = sc.Invalid
	case !sc.Enabled:
		if det != nil && det.Installed {
			st.State = event.LSPStateAvailable
		} else {
			st.State = event.LSPStateNotInstalled
		}
	default: // enabled, nothing running
		if det != nil && !det.Installed {
			st.State = event.LSPStateNotInstalled
		} else {
			st.State = event.LSPStateStopped
		}
	}
	return st
}

func mapInstanceState(s InstanceState) event.LSPServerState {
	switch s {
	case StateStarting, StateInitializing:
		return event.LSPStateStarting
	case StateIndexing:
		return event.LSPStateIndexing
	case StateReady:
		return event.LSPStateReady
	case StateFailed:
		return event.LSPStateFailed
	default: // stopped
		return event.LSPStateStopped
	}
}

// detectInfo answers the cached binary detection for one server, refreshing
// it in the background when older than detectTTL; the snapshot never blocks
// on probing. A refresh notifies the subscribers when it lands.
func (m *Manager) detectInfo(sc ServerConfig) *DetectResult {
	m.mu.Lock()
	entry, ok := m.detected[sc.ID]
	fresh := ok && time.Since(entry.at) <= detectTTL
	detecting := m.detecting[sc.ID]
	refresh := !fresh && !detecting && !m.closed
	if refresh {
		m.detecting[sc.ID] = true
		// Registered in the same critical section that observes closed:
		// Close sets closed under this lock before drainBackground waits on
		// bg, so no detection can start after the wait returned and write
		// into an agent workspace the composition root already tore down.
		m.bg.Add(1)
	}
	m.mu.Unlock()
	if refresh {
		go func() {
			defer m.bg.Done()
			env, _ := BuildEnv(EnvSpec{Passthrough: sc.EnvPassthrough, Env: sc.Env, FromProject: sc.EnvFromProject, Home: m.opts.Home})
			det := Detect(context.Background(), sc, env, runtime.GOOS, filepath.Join(StateDir(m.opts.AgentWorkspace), "detect.json"))
			m.mu.Lock()
			m.detected[sc.ID] = detectEntry{at: time.Now(), det: det}
			delete(m.detecting, sc.ID)
			m.mu.Unlock()
			m.notifySoon()
		}()
	}
	if !ok {
		return nil
	}
	result := entry.det
	return &result
}

// notifySoon delivers a snapshot to the subscribers after a debounce; the
// call itself runs in the background, never holding the manager lock.
func (m *Manager) notifySoon() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed || len(m.subs) == 0 || m.notifyPending {
		m.mu.Unlock()
		return
	}
	m.notifyPending = true
	m.mu.Unlock()
	time.AfterFunc(notifyDebounce, func() {
		m.mu.Lock()
		m.notifyPending = false
		m.mu.Unlock()
		m.notifyNow()
	})
}

// notifyNow delivers a snapshot to the subscribers without the debounce. A
// transition every surface must observe even when it is quickly followed by
// more changes — an install starting and failing within one debounce window
// is the case in point, watchers edge-detect Installing — goes through it;
// everything else keeps coalescing through notifySoon.
func (m *Manager) notifyNow() {
	if m == nil {
		return
	}
	m.mu.Lock()
	closed := m.closed
	subs := make([]func(event.LSPSnapshot), 0, len(m.subs))
	for _, fn := range m.subs {
		subs = append(subs, fn)
	}
	m.mu.Unlock()
	if closed || len(subs) == 0 {
		return
	}
	snap := m.Snapshot()
	for _, fn := range subs {
		fn(snap)
	}
}

func (m *Manager) Subscribe(fn func(event.LSPSnapshot)) (cancel func()) {
	if m == nil || fn == nil {
		return func() {}
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return func() {}
	}
	if m.subs == nil {
		m.subs = map[int]func(event.LSPSnapshot){}
	}
	id := m.nextSub
	m.nextSub++
	m.subs[id] = fn
	m.mu.Unlock()
	// The current snapshot is the subscription's first message: a change
	// that already happened (an install that finished between two of a
	// watcher's frames) must not wait for the next change to be seen. The
	// push bypasses notifySoon's debounce entirely, so it cannot coalesce
	// or double-fire with it.
	fn(m.Snapshot())
	return func() {
		m.mu.Lock()
		delete(m.subs, id)
		m.mu.Unlock()
	}
}

// SetEnabled flips one server's switch in enabled.json and adopts it: a
// disabled server's instances in this project stop (spec §5.4).
func (m *Manager) SetEnabled(serverID string, enabled bool) error {
	known := false
	for _, sc := range m.servers() {
		if sc.ID == serverID {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("unknown language server %q", serverID)
	}
	if err := SaveEnabled(m.opts.AgentWorkspace, serverID, enabled); err != nil {
		return err
	}
	m.invalidate()
	if !enabled {
		m.pool.stopServerInProject(serverID, m.opts.ProjectRoot)
	}
	m.notifySoon()
	return nil
}

// Restart restarts one server's instances in this project (spec §7.8: the
// /lsp way out of a crashed-for-good server).
func (m *Manager) Restart(serverID string) error {
	var srv *ServerConfig
	resolved := m.servers()
	for i := range resolved {
		if resolved[i].ID == serverID {
			srv = &resolved[i]
			break
		}
	}
	if srv == nil {
		return fmt.Errorf("unknown language server %q", serverID)
	}
	timeout := srv.StartupTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	for _, e := range m.pool.instancesOfProject(serverID, m.opts.ProjectRoot) {
		if e.inst == nil {
			continue // still starting: nothing to restart
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err := e.inst.Restart(ctx)
		cancel()
		if err != nil {
			return err
		}
	}
	m.notifySoon()
	return nil
}

// installState is one server's install as any surface can see it: whether it
// is running, its last output lines and, once it failed, why.
type installState struct {
	running bool
	log     []string // the last installTailLines output lines
	err     string
}

func (m *Manager) Install(ctx context.Context, serverID string, progress func(line string)) error {
	st, srv, err := m.beginInstall(serverID)
	if err != nil {
		return err
	}
	return m.runInstall(ctx, srv, st, progress)
}

// beginInstall resolves the server and marks its install running, notifying
// without the debounce. Callers that hand the install to a background
// goroutine still leave Installing=true in the snapshot the moment the call
// returns: watchers edge-detect Installing, and a subscriber's first
// snapshot must show the new attempt, never the previous one's outcome.
func (m *Manager) beginInstall(serverID string) (st *installState, srv *ServerConfig, err error) {
	resolved := m.servers()
	for i := range resolved {
		if resolved[i].ID == serverID {
			srv = &resolved[i]
			break
		}
	}
	if srv == nil {
		return nil, nil, fmt.Errorf("unknown language server %q", serverID)
	}
	m.mu.Lock()
	if m.installs == nil {
		m.installs = map[string]*installState{}
	}
	if cur := m.installs[serverID]; cur != nil && cur.running {
		m.mu.Unlock()
		return nil, nil, fmt.Errorf("%s is already being installed", serverID)
	}
	// The install state lives on the manager so every surface sees it, no
	// matter which of them started the install.
	st = &installState{running: true}
	m.installs[serverID] = st
	m.mu.Unlock()
	// The start of an install must not coalesce with what follows: an
	// install can fail inside one debounce window, and watchers
	// edge-detect Installing, so a merged first snapshot (already
	// finished) would leave them waiting for an end that never comes.
	m.notifyNow()
	return st, srv, nil
}

// runInstall executes one already-marked-running install to its terminal
// state: the outcome lands in the install state every surface reads.
func (m *Manager) runInstall(ctx context.Context, srv *ServerConfig, st *installState, progress func(line string)) error {
	err := Install(ctx, *srv, runtime.GOOS, filepath.Join(StateDir(m.opts.AgentWorkspace), "detect.json"), func(line string) {
		m.mu.Lock()
		st.log = append(st.log, line)
		if len(st.log) > installTailLines {
			st.log = st.log[len(st.log)-installTailLines:]
		}
		m.mu.Unlock()
		if progress != nil {
			progress(line)
		}
		m.notifySoon()
	})
	m.mu.Lock()
	st.running = false
	if err != nil {
		st.err = err.Error()
	}
	m.mu.Unlock()
	if err == nil {
		m.mu.Lock()
		delete(m.detected, srv.ID) // the next Snapshot probes again
		m.mu.Unlock()
	}
	m.notifySoon()
	return err
}

func (m *Manager) SetRecommendationListener(fn func(ctx context.Context, rec event.LSPRecommendation)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.listener = fn
	m.mu.Unlock()
}

// DecideRecommendation applies the user's answer (spec §10.2). The
// recommendation leaves the pending set first, so a second answer to the
// same id is unknown no matter how the rest of the path fares.
func (m *Manager) DecideRecommendation(recommendationID string, choice event.LSPRecommendationChoice) error {
	if m == nil {
		return tool.ErrUnknownLSPRecommendation
	}
	if !choice.Valid() {
		return fmt.Errorf("invalid choice %q", choice)
	}
	m.mu.Lock()
	pend, ok := m.pendingRecs[recommendationID]
	if ok {
		delete(m.pendingRecs, recommendationID)
	}
	m.mu.Unlock()
	if !ok {
		return tool.ErrUnknownLSPRecommendation
	}
	switch choice {
	case event.LSPChoiceEnable:
		return m.applyRecommendation(pend)
	case event.LSPChoiceInstall:
		// The install outlives the answer: it runs in the background and,
		// when it succeeds, applies the same switch an "enable" did. Its
		// progress and outcome are in the snapshot every surface reads.
		// Marking it running is synchronous: this answer returns before the
		// install goroutine starts, and a watcher that subscribes right
		// after must see Installing=true, not the previous attempt's
		// terminal state.
		st, srv, err := m.beginInstall(pend.rec.ServerID)
		if err != nil {
			return err
		}
		go func() {
			if err := m.runInstall(context.Background(), srv, st, nil); err != nil {
				slog.Debug("lsp: recommendation install failed", "server", pend.rec.ServerID, "err", err)
				return
			}
			if err := m.applyRecommendation(pend); err != nil {
				slog.Debug("lsp: enabling after install failed", "server", pend.rec.ServerID, "err", err)
			}
		}()
		return nil
	case event.LSPChoiceNotNow:
		st := loadRecState(m.opts.AgentWorkspace)
		st.DismissedStreak++
		if st.DismissedStreak >= recDismissLimit {
			st.Disabled = true
			st.DisabledReason = fmt.Sprintf("dismissed %d times in a row", recDismissLimit)
		}
		if err := saveRecState(m.opts.AgentWorkspace, st); err != nil {
			return err
		}
	case event.LSPChoiceNever:
		st := loadRecState(m.opts.AgentWorkspace)
		st.Never = append(st.Never, pend.rec.ServerID)
		st.DismissedStreak = 0
		if err := saveRecState(m.opts.AgentWorkspace, st); err != nil {
			return err
		}
	case event.LSPChoiceDisableAll:
		st := loadRecState(m.opts.AgentWorkspace)
		st.Disabled = true
		st.DisabledReason = "turned off by the user"
		if err := saveRecState(m.opts.AgentWorkspace, st); err != nil {
			return err
		}
	}
	m.notifySoon()
	return nil
}

// applyRecommendation is the "enable" answer: the switch, the streak reset
// and the background prestart (spec §10.2). The transcript line is the
// surface's to write; this owns what the answer changes.
func (m *Manager) applyRecommendation(pend pendingRec) error {
	if err := m.SetEnabled(pend.rec.ServerID, true); err != nil {
		return err
	}
	st := loadRecState(m.opts.AgentWorkspace)
	st.DismissedStreak = 0
	if err := saveRecState(m.opts.AgentWorkspace, st); err != nil {
		return err
	}
	m.prestartRecommended(pend)
	m.notifySoon()
	return nil
}

// prestartRecommended starts the recommended server in the background, so
// diagnostics are there from the next edit instead of the first query.
func (m *Manager) prestartRecommended(pend pendingRec) {
	resolved := m.servers()
	var srv *ServerConfig
	for i := range resolved {
		if resolved[i].ID == pend.rec.ServerID {
			srv = &resolved[i]
			break
		}
	}
	if srv == nil {
		return
	}
	root, ok := ResolveRoot(pend.triggerFile, m.opts.ProjectRoot, *srv)
	if !ok {
		return
	}
	sc := *srv
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), sc.StartupTimeout)
		defer cancel()
		if _, err := m.pool.acquire(ctx, m, sc, root); err != nil {
			slog.Debug("lsp: recommendation prestart failed", "server", sc.ID, "err", err)
		}
	}()
}

// ResetRecommendations turns recommendations back on (spec §10.2).
func (m *Manager) ResetRecommendations() error {
	if err := ResetRecommendationState(m.opts.AgentWorkspace); err != nil {
		return err
	}
	m.notifySoon()
	return nil
}
