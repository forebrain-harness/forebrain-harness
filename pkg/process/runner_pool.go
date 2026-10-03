// The project runner pool: one Runner per (primary agent × project), so the
// things that hang off a Runner — the effective MCP list, project skills, the
// permission snapshot, the sandbox's view of it — can differ per project the
// way they already differ per agent.
//
// The TUI needs none of this: its launch project is the process's, and its
// one Runner is the pool's base entry. The gateway serves sessions in many
// projects with one process, so it resolves the Runner through the pool
// instead of holding a single one.
//
// Freezing rules, all derived from the session-freeze contract:
//
//   - A session is bound to a pool entry the first time it resolves a Runner.
//     A project edit afterwards creates a new entry for new sessions; the
//     bound session keeps the old one, so its instructions, memory scope and
//     MCP list do not move mid-session.
//   - Each entry snapshots the project row it was built from and serves
//     per-session instruction snapshots from that copy.
//   - Eviction closes the entry's Runner — an evicted Runner's servers must not
//     keep running as orphans — and only ever evicts an entry none of whose
//     sessions is still live, because re-resolving a live session would hand it
//     a fresh tool table mid-conversation. Live means the session row has been
//     written within runnerPoolSessionEviction: a turn touches its session's
//     updated_at, so "recently talked to" is exactly what that column records,
//     and a session whose conversation has gone quiet for the threshold has no
//     provider cache prefix left to preserve — the thing the freeze protects
//     no longer exists.
//   - Between the last turn and eviction there is a gentler step: an entry that
//     goes unresolved for a while has its MCP connections released (they grow
//     back on the next request) while the entry, its runner and its frozen tool
//     table all stay, so an active conversation never notices and the subprocess
//     count does not grow without end.
package process

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// DefaultProjectRunnerPoolSize bounds how many project runners stay live.
// Each holds MCP sessions, so the bound exists to keep subprocess count
// finite; the base (no-project) runner is not counted.
const DefaultProjectRunnerPoolSize = 8

// RunnerPool serves the Runner a session should run on.
type RunnerPool struct {
	env *Environment
	// projects is the store the pool resolves session→project through.
	projects *state.ProjectStore
	max      int

	mu sync.Mutex
	// entries is keyed by agentID + "\x00" + projectID + "\x00" + updatedAt,
	// so an edited project yields a fresh entry while sessions bound to the
	// old one keep it.
	entries map[string]*poolEntry
	// sessions binds sessionID → entry key, recorded at first resolution.
	sessions map[string]string
	// lru orders entries oldest-used first for eviction.
	lru []*poolEntry
	// sweepMu guards sweepStop, the channel that ends the idle sweeper.
	sweepMu   sync.Mutex
	sweepStop chan struct{}
	// releaseIdleMCP is the call the sweeper makes on an idle entry's runner.
	// Production leaves it nil, which means the real call; tests substitute a
	// recorder so the sweeper can be observed without standing a real MCP
	// child up. Nil-safe by construction, never read without checking.
	releaseIdleMCP func(*run.Runner) bool
}

// The idle release's two clocks.
//
// An entry is a candidate when nobody resolved through it for
// runnerPoolIdleRelease; the runner underneath must additionally have run no
// foreground turn for runnerPoolForegroundSettle, so a long turn whose session
// stopped resolving mid-flight (a tool call that takes minutes, a subagent)
// cannot have its connections pulled out from under it. Both are vars rather
// than consts so a test can shrink them instead of sleeping in real time.
var (
	// runnerPoolSweepInterval is how often the sweeper looks. It is a floor on
	// nothing — release can only ever happen at or after the idle threshold,
	// and the interval only decides how promptly that is noticed.
	runnerPoolSweepInterval = time.Minute
	// runnerPoolIdleRelease is how long an entry goes unresolved before its
	// runner's MCP connections are worth giving back. Long enough that a
	// human-paced conversation (each turn re-resolving) never touches it;
	// short enough that a project visited this morning is not still holding a
	// child process tonight.
	runnerPoolIdleRelease = 30 * time.Minute
	// runnerPoolForegroundSettle is the quiet period the runner itself must
	// observe, measured from the end of its last foreground turn.
	runnerPoolForegroundSettle = time.Minute
	// runnerPoolSessionEviction is how long a session must have been quiet
	// before the entry it is bound to may be evicted. Hardcoded rather than
	// configurable: it is not a preference, it is the point at which the cache
	// prefix the freeze protects has already expired on the provider's side,
	// and exposing it would invite tuning it into a mid-conversation runner
	// swap. A var only so a test can shrink it.
	runnerPoolSessionEviction = 24 * time.Hour
)

type poolEntry struct {
	key     string
	runner  *run.Runner
	project state.Project
	// instructions freezes per-session project instructions.
	insMu        sync.Mutex
	instructions map[string]string
	lastUsed     time.Time
}

// RunnerForSessionOnEnv resolves the Runner one session should run on,
// creating the environment's pool on first use. The TUI never triggers pool
// creation: its sessions have no project row, so every call returns the base
// Runner and the pool is never built.
func (env *Environment) RunnerForSession(ctx context.Context, sessionID string) *run.Runner {
	if env == nil {
		return nil
	}
	if env.SQL == nil || env.Deps.SessionStore == nil {
		return env.Runner
	}
	env.poolOnce.Do(func() {
		env.pool = NewRunnerPool(env, 0)
	})
	if env.pool == nil {
		return env.Runner
	}
	return env.pool.RunnerForSession(ctx, sessionID)
}

// RunnerPool exposes the environment's pool for surfaces that need more than
// the per-session runner (binding a new session, tearing down on an agent
// switch). Nil when it has not been created yet.
func (env *Environment) RunnerPool() *RunnerPool {
	if env == nil {
		return nil
	}
	return env.pool
}

// EnsureRunnerPool builds the pool eagerly. The gateway does this at startup
// so the first project session does not pay the build cost, and so a broken
// project configuration surfaces at startup rather than mid-conversation.
func (env *Environment) EnsureRunnerPool() {
	if env == nil || env.SQL == nil {
		return
	}
	env.poolOnce.Do(func() {
		env.pool = NewRunnerPool(env, 0)
	})
}

// CloseRunnerPool tears down the pool's runners. Called on primary-agent
// switches and process shutdown.
func (env *Environment) CloseRunnerPool() {
	if env == nil {
		return
	}
	if p := env.pool; p != nil {
		p.Close()
	}
}

// NewRunnerPool builds the pool over one environment. max <= 0 means the
// default bound.
func NewRunnerPool(env *Environment, max int) *RunnerPool {
	if env == nil {
		return nil
	}
	if max <= 0 {
		max = DefaultProjectRunnerPoolSize
	}
	p := &RunnerPool{
		env:      env,
		projects: state.NewProjectStore(env.SQL, env.Deps.AgentName),
		max:      max,
		entries:  make(map[string]*poolEntry),
		sessions: make(map[string]string),
	}
	p.startSweep()
	return p
}

// startSweep runs the idle sweeper: every interval it releases the MCP
// connections of entries nobody has resolved through lately. Idempotent, so
// re-arming after a RebindAgent's teardown cannot start a second goroutine.
//
// The sweeper stops on Close — the pool's entries are gone by then — and is
// restarted by RebindAgent, which is a teardown-and-continue rather than an
// end: the pool object itself is reused for the next agent's entries.
func (p *RunnerPool) startSweep() {
	if p == nil {
		return
	}
	p.sweepMu.Lock()
	defer p.sweepMu.Unlock()
	if p.sweepStop != nil {
		return
	}
	stop := make(chan struct{})
	p.sweepStop = stop
	go func() {
		ticker := time.NewTicker(runnerPoolSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				p.sweepIdleEntries(time.Now())
				// Eviction and state cleanup are teardowns, not the tick's
				// siblings: give them their own context so a slow store read
				// cannot borrow the sweep's lifetime decisions, and its own
				// bound so a stuck query cannot stack up behind the ticker.
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				p.evictStaleEntries(ctx, time.Now())
				p.sweepToolState(ctx, time.Now())
				cancel()
			}
		}
	}()
}

// stopSweep ends the idle sweeper. Safe to call more than once and on a pool
// that never started one.
func (p *RunnerPool) stopSweep() {
	if p == nil {
		return
	}
	p.sweepMu.Lock()
	defer p.sweepMu.Unlock()
	if p.sweepStop != nil {
		close(p.sweepStop)
		p.sweepStop = nil
	}
}

// sweepIdleEntries releases the MCP connections of every entry that has gone
// unresolved past the idle threshold.
//
// The entries are only selected under the lock; the release happens outside it,
// because tearing a stdio connection down waits out the spec's terminate
// sequence and one slow server must not hold up every session trying to resolve
// a runner. Selection is by lastUsed alone — the runner's own quiet period is
// checked inside the release, where a load still in flight or a turn still
// running answers "no" for itself.
//
// Releasing is not eviction: the entry, its runner and its frozen tool table
// all stay, so a session bound to it keeps resolving to exactly what it always
// did. Only the resource nobody was using goes back.
func (p *RunnerPool) sweepIdleEntries(now time.Time) {
	if p == nil {
		return
	}
	p.mu.Lock()
	var idle []*poolEntry
	for _, entry := range p.entries {
		if entry == nil || entry.runner == nil {
			continue
		}
		if now.Sub(entry.lastUsed) < runnerPoolIdleRelease {
			continue
		}
		idle = append(idle, entry)
	}
	p.mu.Unlock()
	// The release hook is read under its own lock rather than the pool's: a
	// test substituting a recorder does so on a live pool, whose background
	// sweeper may be mid-sweep.
	p.sweepMu.Lock()
	release := p.releaseIdleMCP
	p.sweepMu.Unlock()
	for _, entry := range idle {
		if release != nil {
			_ = release(entry.runner)
			continue
		}
		entry.runner.MCPStartup().ReleaseIdleConnections(runnerPoolForegroundSettle)
	}
}

// evictStaleEntries closes the entries whose every session has gone quiet past
// the eviction threshold, and drops their bindings.
//
// The rule is about liveness, not recency of the entry: an entry survives as
// long as ANY session bound to it talked within runnerPoolSessionEviction, so
// an active session pins its runner however full the pool is — the opposite of
// an LRU, which would evict the busy entry of an unpopular project to make room
// for a popular one. Only when the whole conversation has been quiet past the
// threshold is the entry evictable, because the thing eviction could break —
// the session's frozen tool table matching a live provider cache prefix — has
// already expired on its own by then. A session coming back afterwards resolves
// a fresh entry and pays one MCP startup, which is what resuming a day-old
// conversation costs anyway.
//
// The session facts come from the store, not from the pool's own bookkeeping:
// every turn touches its session row's updated_at, so the database is the one
// place that knows when a conversation last happened. Sessions with no row do
// not veto — a binding whose conversation was never created (or was deleted)
// cannot hold a runner open forever — but the entry's own lastUsed must also be
// past the threshold, which is what keeps a session that resolved through the
// entry but has not spoken yet from losing its runner on its first day.
//
// The query and the close both happen outside the pool lock: one is a database
// round trip and the other waits out MCP teardown, and neither may hold up a
// session resolving its runner. lastUsed is re-checked under the lock before
// the entry is removed, so a resolution that arrived mid-query un-candidates
// the entry instead of being evicted out from under.
func (p *RunnerPool) evictStaleEntries(ctx context.Context, now time.Time) {
	if p == nil || p.env.Deps.SessionStore == nil {
		return
	}
	p.mu.Lock()
	type candidate struct {
		entry    *poolEntry
		sessions []string
	}
	var candidates []candidate
	for key, entry := range p.entries {
		if entry == nil || entry.runner == nil {
			continue
		}
		if now.Sub(entry.lastUsed) < runnerPoolSessionEviction {
			continue
		}
		var sessions []string
		for sessionID, bound := range p.sessions {
			if bound == key {
				sessions = append(sessions, sessionID)
			}
		}
		candidates = append(candidates, candidate{entry: entry, sessions: sessions})
	}
	p.mu.Unlock()
	if len(candidates) == 0 {
		return
	}
	ids := make([]string, 0, 16)
	for _, cand := range candidates {
		ids = append(ids, cand.sessions...)
	}
	lastActive, err := p.env.Deps.SessionStore.LastActiveByIDs(ctx, ids)
	if err != nil {
		slog.Warn("runner pool eviction: read session activity", "err", err)
		return
	}
	p.mu.Lock()
	var evicted []*poolEntry
	for _, cand := range candidates {
		key := cand.entry.key
		current, ok := p.entries[key]
		if !ok || current != cand.entry {
			continue
		}
		if !entryEvictableLocked(current, cand.sessions, lastActive, now) {
			continue
		}
		delete(p.entries, key)
		for i, e := range p.lru {
			if e == current {
				p.lru = append(p.lru[:i], p.lru[i+1:]...)
				break
			}
		}
		for sessionID, bound := range p.sessions {
			if bound == key {
				delete(p.sessions, sessionID)
			}
		}
		evicted = append(evicted, current)
	}
	p.mu.Unlock()
	if len(evicted) == 0 {
		return
	}
	slog.Info("runner pool evicted stale entries", "count", len(evicted))
	closePoolEntries(evicted)
}

// entryEvictableLocked is the eviction rule as one decision. Caller holds
// p.mu and passes the activity map read outside it.
//
// Three things can still save an entry at this point: a resolution arrived
// while the activity was being read (lastUsed moved past the threshold — the
// binding that resolved it stays), a session bound to it talked within the
// threshold (the live pin, however stale the entry), and a turn still running
// on its runner (the connections belong to it). Eviction is what is left.
func entryEvictableLocked(entry *poolEntry, sessions []string, lastActive map[string]int64, now time.Time) bool {
	if entry == nil || entry.runner == nil {
		return false
	}
	if now.Sub(entry.lastUsed) < runnerPoolSessionEviction {
		// Resolved through since the candidate pass: no longer a candidate.
		return false
	}
	cutoff := now.Add(-runnerPoolSessionEviction)
	for _, sessionID := range sessions {
		if updated, ok := lastActive[sessionID]; ok && time.Unix(updated, 0).After(cutoff) {
			return false
		}
	}
	// The runner's own say: a turn in flight on it outranks every clock here.
	return entry.runner.MCPStartup().IdleFor(runnerPoolForegroundSettle)
}

// sweepToolState cleans the per-session and per-path state that has outlived
// the conversation it belonged to, on every runner this process serves: the
// base runner — which carries every project-less session, so leaving it out
// would leak exactly the conversations the pool never sees — and each pooled
// entry's runner.
//
// The liveness fact is the session row again: a turn touches updated_at, so a
// row quiet past the eviction threshold belongs to a conversation whose state
// nothing will read (and whose provider cache is gone anyway), and a session
// with no row never had — or no longer has — a conversation to protect. The
// same threshold as eviction, deliberately: it is one rule about when a
// conversation's leftovers stop mattering, applied everywhere it does.
func (p *RunnerPool) sweepToolState(ctx context.Context, now time.Time) {
	if p == nil || p.env.Deps.SessionStore == nil {
		return
	}
	p.mu.Lock()
	runners := make([]*run.Runner, 0, len(p.entries)+1)
	if p.env.Runner != nil {
		runners = append(runners, p.env.Runner)
	}
	for _, entry := range p.entries {
		if entry != nil && entry.runner != nil {
			runners = append(runners, entry.runner)
		}
	}
	p.mu.Unlock()
	for _, runner := range runners {
		st := runner.Tools()
		if st == nil {
			continue
		}
		ids := st.SessionIDs()
		if len(ids) > 0 {
			lastActive, err := p.env.Deps.SessionStore.LastActiveByIDs(ctx, ids)
			if err != nil {
				slog.Warn("runner pool state sweep: read session activity", "err", err)
				continue
			}
			cutoff := now.Add(-runnerPoolSessionEviction)
			var quiet []string
			for _, sid := range ids {
				if updated, ok := lastActive[sid]; !ok || !time.Unix(updated, 0).After(cutoff) {
					quiet = append(quiet, sid)
				}
			}
			if dropped := st.DropSessions(quiet); dropped > 0 {
				slog.Debug("tool state dropped quiet sessions", "count", dropped)
			}
		}
		st.TrimRunKeyed(runnerPoolSessionEviction)
		st.TrimStaleReadStates(runnerPoolSessionEviction)
	}
}

// RunnerForSession resolves the Runner one session runs on. Sessions without
// a project — channel conversations, the base webchat — run on the
// environment's own Runner, exactly as before the pool existed.
func (p *RunnerPool) RunnerForSession(ctx context.Context, sessionID string) *run.Runner {
	if p == nil || p.env == nil {
		return nil
	}
	base := p.env.Runner
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || p.projects == nil {
		return base
	}
	project, ok, err := p.projects.ProjectForSession(ctx, sessionID)
	if err != nil || !ok {
		return base
	}
	entry, err := p.entryForSession(ctx, sessionID, project)
	if err != nil || entry == nil {
		return base
	}
	return entry.runner
}

func (p *RunnerPool) entryForSession(ctx context.Context, sessionID string, project state.Project) (*poolEntry, error) {
	p.mu.Lock()
	if bound, ok := p.sessions[sessionID]; ok {
		if entry, ok := p.entries[bound]; ok {
			entry.touchLocked()
			p.mu.Unlock()
			return entry, nil
		}
	}
	key := poolKey(p.env.Deps.AgentName, project)
	entry, ok := p.entries[key]
	if !ok {
		var err error
		entry, err = p.buildEntryLocked(ctx, project)
		if err != nil {
			p.mu.Unlock()
			return nil, err
		}
		p.entries[key] = entry
		p.lru = append(p.lru, entry)
	}
	p.sessions[sessionID] = key
	entry.touchLocked()
	evicted := p.evictLocked()
	p.mu.Unlock()
	// Closing a runner tears down its MCP sessions, and a stdio session's close
	// waits out the spec's terminate sequence: that must not happen under the
	// pool lock, or one unresponsive server would hold up every session trying
	// to resolve a runner.
	closePoolEntries(evicted)
	return entry, nil
}

// poolKey identifies one runner generation: the agent, the project, and a
// fingerprint of the fields a runner freezes (instructions, memory scope,
// resource access, identity). UpdatedAt alone is too coarse — two edits in
// the same second would silently share a generation — so the content itself
// is hashed. A session bound to an older generation keeps it; a new session
// after any edit resolves to the fresh one.
func poolKey(agentID string, project state.Project) string {
	body := strings.Join([]string{
		strings.TrimSpace(project.ID),
		strings.TrimSpace(project.Name),
		strings.TrimSpace(project.Root),
		strings.TrimSpace(project.ProjectKey),
		strings.TrimSpace(project.Instructions),
		state.NormalizeProjectMemoryScope(project.MemoryScope),
		fmt.Sprintf("%t", project.ResourceAccess),
		fmt.Sprintf("%d", project.UpdatedAt),
	}, "\x1f")
	sum := sha256.Sum256([]byte(body))
	return strings.TrimSpace(agentID) + "\x00" + strings.TrimSpace(project.ID) + "\x00" + hex.EncodeToString(sum[:])
}

func (e *poolEntry) touchLocked() {
	e.lastUsed = time.Now()
}

// evictLocked drops the least-recently-used entries beyond the bound and
// returns them for the caller to close outside the lock.
//
// An entry a session is still bound to is never evicted. Evicting one makes
// that session re-resolve on its next turn, and re-resolving builds a fresh
// Runner whose MCP servers start from scratch — so the session's tool table,
// and the prompt prefix derived from it, would change mid-conversation. That is
// exactly the freeze the pool exists to protect, and it gets worse, not better,
// when a server is slow or failing. When every entry is bound the pool stays
// over its soft bound and says so.
func (p *RunnerPool) evictLocked() []*poolEntry {
	var evicted []*poolEntry
	scanned := 0
	for len(p.lru) > p.max && scanned < len(p.lru) {
		oldest := p.lru[0]
		if p.entryBoundLocked(oldest.key) {
			// Hold it: rotate to the back of the LRU order and consider the
			// next one, so a bound entry cannot pin the scan.
			p.lru = append(p.lru[1:], oldest)
			scanned++
			continue
		}
		p.lru = p.lru[1:]
		delete(p.entries, oldest.key)
		evicted = append(evicted, oldest)
		scanned = 0
	}
	if len(p.lru) > p.max {
		slog.Info("runner pool is over its soft bound: every entry is bound to a session",
			"entries", len(p.lru), "max", p.max)
	}
	return evicted
}

// entryBoundLocked reports whether any session still resolves through this
// entry. Caller holds p.mu.
func (p *RunnerPool) entryBoundLocked(key string) bool {
	if strings.TrimSpace(key) == "" {
		return false
	}
	for _, bound := range p.sessions {
		if bound == key {
			return true
		}
	}
	return false
}

// closePoolEntries closes evicted runners in parallel and outside the pool
// lock, with a total deadline: each close waits on that runner's MCP children,
// and one that ignores its cancellation must not hold up the pool.
func closePoolEntries(entries []*poolEntry) {
	var closing []*run.Runner
	for _, entry := range entries {
		if entry == nil || entry.runner == nil {
			continue
		}
		closing = append(closing, entry.runner)
	}
	if len(closing) == 0 {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for _, runner := range closing {
			wg.Add(1)
			go func(r *run.Runner) {
				defer wg.Done()
				_ = r.Close()
			}(runner)
		}
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(runnerPoolCloseDeadline):
		slog.Warn("runner pool close timed out", "runners", len(closing))
	}
}

// runnerPoolCloseDeadline bounds how long the pool waits for its runners to
// shut down. Every connection a closing runner holds goes with its own MCP
// cancellation, so exceeding this means a server or transport ignored it.
const runnerPoolCloseDeadline = 20 * time.Second

// buildEntryLocked assembles one project runner. Caller holds p.mu.
func (p *RunnerPool) buildEntryLocked(ctx context.Context, project state.Project) (*poolEntry, error) {
	env := p.env
	baseDeps := env.Deps
	if baseDeps.AppCfg == nil {
		return nil, fmt.Errorf("runner pool: environment has no config")
	}
	// A registered project's boundary is the path the user registered: the
	// project space, its trust record and its skills all name that exact
	// root, so the sessions run inside it too. Resolving it as a launch
	// directory would walk up to an enclosing checkout and run a subdirectory
	// project under the checkout's skills, MCP servers and trust decision.
	launch, err := safety.ResolveRegisteredContext(env.Root, project.Root)
	if err != nil {
		return nil, fmt.Errorf("runner pool: resolve project %s: %w", project.Root, err)
	}
	agentWorkspace := baseDeps.WorkspaceRoot
	if strings.TrimSpace(agentWorkspace) == "" {
		agentWorkspace = workspaceRootFor(env)
	}
	mcpRes := ResolveSessionMCP(env.Root, agentWorkspace, baseDeps.AppCfg.Agents.Defaults.MCPServers, launch)
	frozenProject := project
	frozenServers := mcpRes.Servers
	// The whole frozen list, disabled entries included, is what the drift
	// check compares against the disk: a disable toggle is not drift.
	frozenAll := append(append([]appcfg.MCPServerConfig(nil), frozenServers...), mcpRes.Disabled...)
	frozenAgentWorkspace := agentWorkspace
	frozenOverridden := mcpRes.Summary.OverriddenGlobal
	entry := &poolEntry{
		project:      frozenProject,
		instructions: make(map[string]string),
	}
	deps := run.Deps{
		Home:          baseDeps.Home,
		WorkspaceRoot: baseDeps.WorkspaceRoot,
		ProjectKey:    project.ProjectKey,
		ProjectRoot:   project.Root,
		AgentName:     baseDeps.AgentName,
		StateDir:      baseDeps.StateDir,
		Actions:       baseDeps.Actions,
		MCPServers:    frozenServers,
		MCPProject:    mcpRes.ProjectRoot,
		MCPDisabled:   mcpRes.Disabled,
		MCPDiagnostics: func() mcp.ProjectMCPScopeSummary {
			summary := InspectProjectMCP(frozenAgentWorkspace, frozenAll, launch)
			summary.OverriddenGlobal = frozenOverridden
			return summary
		},
		LaunchProject: launch,
		MemoryStore:   baseDeps.MemoryStore,
		AppCfg:        baseDeps.AppCfg,
		SessionStore:  baseDeps.SessionStore,
		RunRT:         baseDeps.RunRT,
	}
	deps.ProjectMemoryOnly = state.NormalizeProjectMemoryScope(project.MemoryScope) == state.ProjectMemoryProjectOnly
	deps.ProjectInstructionsFor = func(sessionID string) string {
		return entry.frozenInstructions(sessionID)
	}
	// A project runner serves the same surface as the environment's own: its
	// subagent streams and compaction lifecycles go to the same sink, or a
	// session bound to a project would never see them.
	runner := &run.Runner{Deps: &deps, Control: env.Control, Events: env.Runner.Events, SkillCommands: run.SkillCommandHooks{
		Refresh:   turn.RefreshSkills,
		IsBuiltin: turn.IsBuiltinName,
	}}
	if err := runner.Load(); err != nil {
		return nil, fmt.Errorf("runner pool: load project runner: %w", err)
	}
	entry.runner = runner
	entry.key = poolKey(baseDeps.AgentName, project)
	return entry, nil
}

// frozenInstructions snapshots the project's instructions for one session on
// first use, from the project row this entry was built with.
func (e *poolEntry) frozenInstructions(sessionID string) string {
	if e == nil {
		return ""
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return e.project.Instructions
	}
	e.insMu.Lock()
	defer e.insMu.Unlock()
	if v, ok := e.instructions[sessionID]; ok {
		return v
	}
	v := e.project.Instructions
	e.instructions[sessionID] = v
	return v
}

// Close tears down every pooled runner. The base runner belongs to the
// environment and is not touched here. The idle sweeper stops here too: with no
// entries left it has nothing to do, and a pool being torn down must not leave
// a goroutine holding it alive.
func (p *RunnerPool) Close() {
	if p == nil {
		return
	}
	p.stopSweep()
	p.mu.Lock()
	entries := make([]*poolEntry, 0, len(p.entries))
	for _, entry := range p.entries {
		entries = append(entries, entry)
	}
	p.entries = make(map[string]*poolEntry)
	p.sessions = make(map[string]string)
	p.lru = nil
	p.mu.Unlock()
	closePoolEntries(entries)
}

// PropagateConfig offers next to every live pool entry through its own
// LoadConfig transaction: pointer swap, pinned ordinary reload, rollback. An
// entry whose load fails keeps its previous configuration and runtime and is
// named in the joined error; an entry evicted concurrently (its runner already
// closed) is skipped — teardown is not a propagation failure. The pool lock is
// held only to collect entries, because LoadConfig rebuilds a chain and one
// slow entry must not hold up session resolution for every other project.
func (p *RunnerPool) PropagateConfig(ctx context.Context, next *appcfg.Root) error {
	if p == nil || next == nil {
		return nil
	}
	p.mu.Lock()
	entries := make([]*poolEntry, 0, len(p.entries))
	for _, entry := range p.entries {
		if entry == nil || entry.runner == nil {
			continue
		}
		entries = append(entries, entry)
	}
	p.mu.Unlock()
	var failed []error
	for _, entry := range entries {
		err := entry.runner.LoadConfig(next)
		if err == nil || run.IsRunnerClosed(err) {
			continue
		}
		slog.Error("config reload: project runner kept its previous configuration",
			"project", strings.TrimSpace(entry.project.Name), "err", err)
		failed = append(failed, fmt.Errorf("project %s: %w", strings.TrimSpace(entry.project.Name), err))
	}
	return errors.Join(failed...)
}

// Runners returns every live runner in the pool, every generation still in
// it included, so a surface that changed the agent's own settings on disk can
// have every running session read them again.
func (p *RunnerPool) Runners() []*run.Runner {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []*run.Runner
	for _, entry := range p.entries {
		if entry != nil && entry.runner != nil {
			out = append(out, entry.runner)
		}
	}
	return out
}

// RunnersForProject returns the live runners built for one project, every
// generation still in the pool included, so a surface that changed that
// project's settings on disk can have its running sessions read them again.
func (p *RunnerPool) RunnersForProject(projectID string) []*run.Runner {
	if p == nil {
		return nil
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []*run.Runner
	for _, entry := range p.entries {
		if entry != nil && entry.runner != nil && strings.TrimSpace(entry.project.ID) == projectID {
			out = append(out, entry.runner)
		}
	}
	return out
}

// BindSession records which project a new session belongs to, so the next
// RunnerForSession resolves without a second lookup, and stamps the session's
// cwd default with the project root.
func (p *RunnerPool) BindSession(ctx context.Context, sessionID, projectID string) error {
	if p == nil || p.projects == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	projectID = strings.TrimSpace(projectID)
	if sessionID == "" {
		return nil
	}
	if projectID == "" {
		p.mu.Lock()
		delete(p.sessions, sessionID)
		p.mu.Unlock()
		return nil
	}
	project, err := p.projects.Get(ctx, projectID)
	if err != nil {
		return err
	}
	// Pre-resolve so the binding (and the runner build) happens once, here,
	// rather than racing two turns of the same new session.
	_, err = p.entryForSession(ctx, sessionID, project)
	return err
}

// RebindAgent re-points the pool at a different primary agent: every pooled
// runner belongs to the previous tenant, so they are all torn down.
func (p *RunnerPool) RebindAgent(agentID string) {
	if p == nil {
		return
	}
	p.Close()
	if p.projects != nil && p.env != nil {
		p.projects = state.NewProjectStore(p.env.SQL, strings.TrimSpace(agentID))
	}
	// Close stopped the sweeper with the rest of the teardown; the pool object
	// goes on serving the new agent's entries, so the sweeper comes back with
	// it.
	p.startSweep()
}

// SessionProject returns the project a session is bound to, for surfaces that
// need the entity (not just the runner) — the web project view, /status.
func (p *RunnerPool) SessionProject(ctx context.Context, sessionID string) (state.Project, bool) {
	if p == nil || p.projects == nil {
		return state.Project{}, false
	}
	project, ok, err := p.projects.ProjectForSession(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return state.Project{}, false
	}
	return project, ok
}

// workspaceRootFor resolves the agent workspace the way the environment does.
func workspaceRootFor(env *Environment) string {
	if env == nil {
		return ""
	}
	if r := env.Runner; r != nil {
		return r.WorkspaceRoot
	}
	return ""
}
