package lsp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// ManagerOptions describes the runner a Manager serves. Everything here is
// frozen for the runner's lifetime.
type ManagerOptions struct {
	Home              string // FOREBRAIN_HOME
	AgentWorkspace    string // the primary agent's workspace root; state lives in <AgentWorkspace>/state/lsp
	ProjectRoot       string // the launch project root; "" when the runner has no project
	ProjectKey        string
	Trusted           bool // safety.TrustedRoot(launch) != ""
	VersionControlled bool
	// ToolRegistered is the frozen answer of ToolEnabled for this runner; the
	// caller computes it and passes it in so the snapshot can report it.
	ToolRegistered bool
}

// quietWindow is the push-server quiet period after the target files
// answered (spec §8.3.2); a package-level variable only so tests can
// shorten it. Production code must not write it.
var quietWindow = 300 * time.Millisecond

// lateEditedTTL is how long a session's edited files stay eligible for late
// diagnostics (spec §8.4: 30 minutes).
const lateEditedTTL = 30 * time.Minute

// Manager is one runner's view of the pool.
type Manager struct {
	pool     *Pool
	opts     ManagerOptions
	mu       sync.Mutex
	closed   bool
	listener func(ctx context.Context, rec event.LSPRecommendation)
	subs     map[int]func(event.LSPSnapshot)
	nextSub  int

	// The merged server set and the project-file state, cached until the
	// configuration pointer, enabled.json's mtime, the project file's mtime
	// or the project consent store's mtime changes (spec §5.3).
	cache        []ServerConfig
	cacheProject ProjectServerState
	cacheCfg     *appcfg.Root
	cacheEnabled time.Time
	cacheProjMod time.Time
	cacheConsMod time.Time

	// The late-diagnostics queue (spec §8.4), keyed by agent session id.
	late        map[string]*lateSession
	cancelDiags func()

	lastSweep   time.Time
	gitSnapshot map[string]time.Time

	// Binary detection for Snapshot, refreshed in the background.
	detected  map[string]detectEntry
	detecting map[string]bool

	// Installs any surface started (lsp.Install), keyed by server id, so
	// every snapshot can show their progress.
	installs map[string]*installState

	// Recommendations (spec §10): the conversation sessions that already
	// got one, and the ones still waiting for the user's answer.
	recommended map[string]bool
	pendingRecs map[string]pendingRec

	// bg tracks the manager's background writers (the orphan sweep and the
	// binary detections) so Pool.Close can wait them out before the agent
	// workspace disappears underneath them.
	bg sync.WaitGroup

	notifyPending bool // a debounced subscriber notify is scheduled
}

var (
	_ tool.CodeIntelligence = (*Manager)(nil)
	_ tool.CodeIntelControl = (*Manager)(nil)
)

// lateSession is one agent session's late-diagnostics state (spec §8.4).
type lateSession struct {
	edited   map[string]time.Time  // file -> last edit time (kept 30 minutes)
	known    map[string][]Problem  // what the model was last told, per file
	pending  map[string]LateChange // not yet delivered
	inWindow map[string]bool       // inside a DidWrite window right now: no late items
	token    uint64                // bumps on every pending change
}

// detectEntry is one server's cached binary detection.
type detectEntry struct {
	at  time.Time
	det DetectResult
}

// pendingRec is one recommendation awaiting the user's answer.
type pendingRec struct {
	rec         event.LSPRecommendation
	sessionID   string
	triggerFile string // absolute path of the edited file that triggered it
}

// recDetectMaxAge is how fresh a binary detection must be before a
// recommendation trusts it; older or missing, one background probe runs and
// the next edit decides (spec §10.1).
const recDetectMaxAge = 10 * time.Minute

// start runs the manager's background setup; NewManager must return
// immediately, so everything here runs in a goroutine.
func (m *Manager) start() {
	if m.opts.AgentWorkspace != "" {
		// One orphan sweep per process per pid file (spec §7.8).
		if pidFile := PIDFile(m.opts.AgentWorkspace); pidFile != "" {
			if _, loaded := sweptPIDFiles.LoadOrStore(pidFile, struct{}{}); !loaded {
				m.bg.Add(1)
				go func() {
					defer m.bg.Done()
					sweepOrphans(pidFile)
				}()
			}
		}
	}
	if m.pool.config().EffectiveLSP().LateDelivery {
		m.mu.Lock()
		m.cancelDiags = m.pool.diags.Subscribe(m.onDiagPublish)
		m.mu.Unlock()
	}
	if !m.gated() {
		return
	}
	// Prewarm (spec §5.1): servers marked prewarm whose root markers sit at
	// the project root start in the background.
	for _, sc := range m.servers() {
		if !sc.Enabled || !sc.Prewarm || sc.Invalid != "" {
			continue
		}
		marker := firstMarker(sc.RootMarkers)
		if marker == "" || !dirContainsAnyMarker(m.opts.ProjectRoot, sc.RootMarkers) {
			continue
		}
		probe := filepath.Join(m.opts.ProjectRoot, marker)
		root, ok := ResolveRoot(probe, m.opts.ProjectRoot, sc)
		if !ok {
			continue
		}
		srv := sc
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), srv.StartupTimeout)
			defer cancel()
			if _, err := m.pool.acquire(ctx, m, srv, root); err != nil {
				slog.Debug("lsp: prewarm failed", "server", srv.ID, "err", err)
			}
		}()
	}
}

func firstMarker(markers []string) string {
	for _, marker := range markers {
		if marker != "" {
			return marker
		}
	}
	return ""
}

// Close releases the manager. Idempotent.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.subs = nil
	m.listener = nil
	cancelDiags := m.cancelDiags
	m.cancelDiags = nil
	m.mu.Unlock()
	if cancelDiags != nil {
		cancelDiags()
	}
	if m.pool != nil {
		m.pool.forget(m)
		m.pool.releaseFor(m)
	}
}

// servers returns the merged server set, cached until the configuration
// pointer, enabled.json's mtime, the project file's mtime or the project
// consent store's mtime changes (spec §5.3).
func (m *Manager) servers() []ServerConfig {
	servers, _ := m.resolveCached()
	return servers
}

// projectState returns the project-file view resolved alongside servers(),
// under the same cache: pending, denied and the file's notes (spec §5.2).
func (m *Manager) projectState() ProjectServerState {
	_, state := m.resolveCached()
	return state
}

// resolveCached fills and returns the merged server set and the project-file
// state together: both derive from the same files, so one cache key covers
// them.
func (m *Manager) resolveCached() ([]ServerConfig, ProjectServerState) {
	cfg := m.pool.config()
	mod := enabledModTime(m.opts.AgentWorkspace)
	projMod := projectFileModTime(m.opts.ProjectRoot)
	consMod := projectConsentModTime(m.opts.AgentWorkspace)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cache != nil && m.cacheCfg == cfg && mod.Equal(m.cacheEnabled) &&
		projMod.Equal(m.cacheProjMod) && consMod.Equal(m.cacheConsMod) {
		return m.cache, m.cacheProject
	}
	enabled, err := LoadEnabled(m.opts.AgentWorkspace) // unreadable: resolve as if empty
	if err != nil {
		enabled = nil
	}
	project := ResolveProjectServers(m.opts.AgentWorkspace, m.opts.ProjectRoot, m.opts.ProjectKey, m.opts.Trusted)
	m.cache = ResolveServers(ResolveInput{Config: cfg, ProjectServers: project.Allowed, Enabled: enabled, GOOS: runtime.GOOS})
	m.cacheProject = project
	m.cacheCfg = cfg
	m.cacheEnabled = mod
	m.cacheProjMod = projMod
	m.cacheConsMod = consMod
	return m.cache, m.cacheProject
}

// enabledModTime is enabled.json's mtime (zero when absent) — the cheap
// invalidation signal for the server cache.
func enabledModTime(agentWorkspace string) time.Time {
	if agentWorkspace == "" {
		return time.Time{}
	}
	if st, err := os.Stat(filepath.Join(StateDir(agentWorkspace), "enabled.json")); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}

// projectFileModTime is .forebrain/lsp_servers.yaml's mtime (zero when
// absent) — the invalidation signal for project entries and their notes.
func projectFileModTime(projectRoot string) time.Time {
	if strings.TrimSpace(projectRoot) == "" {
		return time.Time{}
	}
	if st, err := os.Stat(ProjectLSPPath(projectRoot)); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}

// projectConsentModTime is project_consent.json's mtime (zero when absent) —
// the invalidation signal for the consent-decided project entries.
func projectConsentModTime(agentWorkspace string) time.Time {
	if agentWorkspace == "" {
		return time.Time{}
	}
	if st, err := os.Stat(ProjectConsentPath(agentWorkspace)); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}

// invalidate clears the server cache; Reconcile and SetEnabled call it.
func (m *Manager) invalidate() {
	m.mu.Lock()
	m.cache = nil
	m.cacheCfg = nil
	m.cacheProject = ProjectServerState{}
	m.mu.Unlock()
}

// gated is the startup gate (spec §9.2): the feature, trust and a project
// root must all hold before anything starts.
func (m *Manager) gated() bool {
	if m == nil || m.pool == nil {
		return false
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return false
	}
	return m.pool.config().EffectiveFeatures().LSP && m.opts.Trusted && m.opts.ProjectRoot != ""
}

// Handles reports, without starting anything, whether an enabled server
// covers absPath in this project.
func (m *Manager) Handles(absPath string) bool {
	if m == nil || !m.gated() {
		return false
	}
	servers := m.servers()
	primary, diagnostics := ServersForFile(servers, absPath, m.opts.ProjectRoot)
	if primary != nil {
		if _, ok := ResolveRoot(absPath, m.opts.ProjectRoot, *primary); ok {
			return true
		}
	}
	for i := range diagnostics {
		if _, ok := ResolveRoot(absPath, m.opts.ProjectRoot, diagnostics[i]); ok {
			return true
		}
	}
	return false
}

// Query runs one lsp tool operation (spec §8.1): it validates the request,
// starts the server if needed, syncs the document, sends the request and
// renders the model-facing text. The implementation lives in query.go.
func (m *Manager) Query(ctx context.Context, q tool.CodeIntelQuery) (tool.CodeIntelResult, error) {
	return m.query(ctx, q)
}

// DidWrite syncs the files an edit tool just wrote and waits — at most the
// diagnostics wait window — for the problems the edit introduced (spec
// §8.3). It never fails the edit and never panics: an internal error means
// no diagnostics, and what arrives late moves to the late queue.
func (m *Manager) DidWrite(ctx context.Context, agentSessionID string, changes []tool.FileChange) tool.DiagnosticsDelta {
	var delta tool.DiagnosticsDelta
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("lsp: DidWrite failed internally; reporting no diagnostics", "panic", r)
			delta = tool.DiagnosticsDelta{}
		}
	}()
	if m == nil || m.pool == nil || len(changes) == 0 {
		return delta
	}
	eff := m.pool.config().EffectiveLSP()
	if !m.gated() {
		return delta
	}
	// A recommendation looks at the same edits the diagnostics do, but it
	// is independent of the after-edit switch: an edit counts even when
	// diagnostics are off (spec §10.1). It never waits on anything here.
	m.considerRecommendation(ctx, changes)
	if !eff.AfterEdit {
		return delta
	}
	// One window for the whole call (spec §8.3.2): start, baseline, sync and
	// collection all share it.
	start := m.pool.now()
	deadline := start.Add(eff.Wait)
	windowCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	// Grouping: every (server, root) pair that owns at least one written
	// file; files no enabled server covers never wait.
	servers := m.servers()
	groups := map[string]*writeGroup{}
	var order []string
	for _, ch := range changes {
		primary, diagnostics := ServersForFile(servers, ch.AbsPath, m.opts.ProjectRoot)
		var candidates []ServerConfig
		if primary != nil && primary.Diagnostics {
			candidates = append(candidates, *primary)
		}
		for i := range diagnostics {
			if diagnostics[i].Diagnostics {
				candidates = append(candidates, diagnostics[i])
			}
		}
		for _, srv := range candidates {
			root, ok := ResolveRoot(ch.AbsPath, m.opts.ProjectRoot, srv)
			if !ok {
				continue
			}
			key := instanceKey(srv.ID, srv.Fingerprint, root)
			g := groups[key]
			if g == nil {
				g = &writeGroup{srv: srv, root: root, idx: map[string]int{}}
				groups[key] = g
				order = append(order, key)
			}
			g.add(ch)
		}
	}
	if len(groups) == 0 {
		return delta // every file is unowned: zero cost (spec §8.3.2)
	}

	paths := make([]string, 0, len(changes))
	seenPaths := map[string]bool{}
	for _, ch := range changes {
		path := filepath.Clean(ch.AbsPath)
		if seenPaths[path] {
			continue
		}
		seenPaths[path] = true
		paths = append(paths, path)
	}

	// The late queue (spec §8.4): register the edit and shield the files
	// while the window runs.
	st := &didWriteState{
		pending:  map[string]bool{},
		acquired: map[string]bool{},
		known:    map[string][]Problem{},
	}
	m.beginWindow(agentSessionID, eff, paths)
	defer m.finishWindow(agentSessionID, st.known)

	var wg sync.WaitGroup
	for _, key := range order {
		g := groups[key]
		wg.Add(1)
		go func(g *writeGroup) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					slog.Debug("lsp: one diagnostics group failed", "server", g.srv.ID, "panic", r)
				}
			}()
			m.didWriteGroup(windowCtx, g, st, eff, start, deadline)
		}(g)
	}
	wg.Wait()

	var pending []string
	for _, path := range paths {
		if st.isPending(path) {
			pending = append(pending, path)
		}
	}
	serverIDs := make([]string, 0, len(st.acquired))
	for id := range st.acquired {
		serverIDs = append(serverIDs, id)
	}
	sort.Strings(serverIDs)
	text, summary := FormatEditDiagnostics(st.problems, pending, serverIDs, st.baselineUnavailable, m.reportOptions(eff, paths))
	delta.Text, delta.Summary = text, summary
	return delta
}

// considerRecommendation offers to enable (or install and enable) a language
// server the first time a conversation session edits a file no enabled
// server covers (spec §10.1). It returns at once: everything that reads a
// file or probes a binary runs in the background, and the listener it calls
// is never waited on.
func (m *Manager) considerRecommendation(ctx context.Context, changes []tool.FileChange) {
	if m == nil || m.pool == nil || len(changes) == 0 {
		return
	}
	if !m.pool.config().EffectiveLSP().Recommendations {
		return
	}
	if !m.opts.Trusted {
		return
	}
	// Any edit in the conversation counts — the main agent's, a typed
	// subagent's, a fork child's alike: they share one surface, the
	// recommendation is the user's to answer, and it deduplicates per
	// conversation session below.
	sid := tool.ConversationSessionIDFromContext(ctx)
	if sid == "" {
		return
	}
	m.mu.Lock()
	listener := m.listener
	already := m.recommended[sid]
	m.mu.Unlock()
	if listener == nil || already {
		return
	}
	// recommendations.json, the binary detection and the publish itself are
	// all off the edit path; the recommendation must outlive the turn that
	// earned it, so the context sheds its cancellation too.
	go m.recommendAsync(context.WithoutCancel(ctx), sid, listener, changes)
}

// recommendAsync is considerRecommendation's background half: it picks the
// candidate and the mode, then hands the recommendation to the listener.
func (m *Manager) recommendAsync(ctx context.Context, sid string, listener func(context.Context, event.LSPRecommendation), changes []tool.FileChange) {
	st := loadRecState(m.opts.AgentWorkspace)
	if st.Disabled {
		return
	}
	never := make(map[string]bool, len(st.Never))
	for _, id := range st.Never {
		never[id] = true
	}
	servers := m.servers()
	root := m.opts.ProjectRoot
	var (
		srv         ServerConfig
		triggerFile string
		triggerExt  string
	)
	for _, ch := range changes {
		if ch.After == nil {
			continue
		}
		primary, diagnostics := ServersForFile(servers, ch.AbsPath, root)
		if primary != nil || len(diagnostics) > 0 {
			continue // an enabled server already covers this file
		}
		cands := MatchFile(servers, ch.AbsPath)
		var pick *ServerConfig
		for i := range cands {
			c := &cands[i]
			// Custom entries are the user's own doing; only the catalog's
			// primary servers are ours to suggest (spec §10.1).
			if !c.InCatalog || c.Enabled || c.Invalid != "" || c.Role != appcfg.LSPRolePrimary || never[c.ID] {
				continue
			}
			if pick == nil || c.Priority > pick.Priority || (c.Priority == pick.Priority && c.ID < pick.ID) {
				pick = c
			}
		}
		if pick != nil {
			srv = *pick
			triggerFile = ch.AbsPath
			triggerExt = strings.ToLower(filepath.Ext(ch.AbsPath))
			if triggerExt == "" {
				triggerExt = filepath.Base(ch.AbsPath)
			}
			break
		}
	}
	if triggerFile == "" {
		return
	}

	// Detection must be fresh; a probe one edit old answers the next time
	// (spec §10.1). detectInfo is the same refresh the snapshot uses.
	m.mu.Lock()
	entry, have := m.detected[srv.ID]
	fresh := have && time.Since(entry.at) <= recDetectMaxAge
	m.mu.Unlock()
	if !fresh {
		_ = m.detectInfo(srv)
		return
	}
	det := entry.det

	rec := event.LSPRecommendation{
		ID:               newRecommendationID(),
		ServerID:         srv.ID,
		DisplayName:      srv.DisplayName,
		Languages:        srv.Languages,
		TriggerExtension: triggerExt,
	}
	if rec.ID == "" {
		return // no id, no decision URL: the next edit tries again
	}
	switch {
	case det.Installed:
		rec.Mode = "enable"
		rec.BinaryPath = det.Path
		rec.Version = det.Version
	default:
		recipe := UsableInstallRecipe(srv, os.Environ(), runtime.GOOS)
		if recipe == nil {
			return
		}
		rec.Mode = "install"
		rec.InstallCommand = strings.Join(recipe.Argv, " ")
	}

	m.mu.Lock()
	// Re-check under the lock: two concurrent edits of one session must
	// produce exactly one recommendation, and a manager that closed while
	// the candidate was being picked has no listener anymore.
	if m.closed || m.recommended[sid] || m.listener == nil {
		m.mu.Unlock()
		return
	}
	if m.recommended == nil {
		m.recommended = map[string]bool{}
	}
	if m.pendingRecs == nil {
		m.pendingRecs = map[string]pendingRec{}
	}
	m.recommended[sid] = true
	m.pendingRecs[rec.ID] = pendingRec{rec: rec, sessionID: sid, triggerFile: triggerFile}
	m.mu.Unlock()
	listener(ctx, rec)
}

// newRecommendationID mints one recommendation's id: lsprec- plus 8 hex
// characters. The id names a decision URL, so the bytes are unpredictable.
func newRecommendationID() string {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "" // crypto/rand does not fail; an empty id publishes nothing
	}
	return fmt.Sprintf("lsprec-%x", raw[:])
}

// writeGroup is one (server, root) pair of a DidWrite call, with the files
// in input order (a repeated file keeps its first Before and last After).
type writeGroup struct {
	srv   ServerConfig
	root  string
	files []tool.FileChange
	idx   map[string]int
}

func (g *writeGroup) add(ch tool.FileChange) {
	path := filepath.Clean(ch.AbsPath)
	if i, ok := g.idx[path]; ok {
		g.files[i].After = ch.After
		return
	}
	g.idx[path] = len(g.files)
	g.files = append(g.files, ch)
}

func (g *writeGroup) paths() []string {
	out := make([]string, len(g.files))
	for i, ch := range g.files {
		out[i] = filepath.Clean(ch.AbsPath)
	}
	return out
}

func (g *writeGroup) wroteSet() map[string]bool {
	out := make(map[string]bool, len(g.files))
	for _, ch := range g.files {
		out[filepath.Clean(ch.AbsPath)] = true
	}
	return out
}

// didWriteState is one DidWrite call's shared result, merged from the
// per-group goroutines.
type didWriteState struct {
	mu                  sync.Mutex
	problems            []Problem
	pending             map[string]bool
	acquired            map[string]bool
	known               map[string][]Problem
	baselineUnavailable bool
}

func (s *didWriteState) addPending(paths []string) {
	s.mu.Lock()
	for _, p := range paths {
		s.pending[p] = true
	}
	s.mu.Unlock()
}

func (s *didWriteState) isPending(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending[path]
}

// mergeKnown folds one server's current problems for a path into the shared
// known list (spec §8.3 step 6: the late baseline).
func (s *didWriteState) mergeKnown(path, serverID string, cur []Problem) {
	merged := mergeServerProblems(s.known[path], serverID, cur)
	if len(merged) == 0 {
		delete(s.known, path)
		return
	}
	s.known[path] = merged
}

// didWriteGroup runs one (server, root) group through the wait window
// (spec §8.3): acquire, baseline, sync, collect, quiet period, then the
// deltas.
func (m *Manager) didWriteGroup(ctx context.Context, g *writeGroup, st *didWriteState, eff appcfg.EffectiveLSP, start, deadline time.Time) {
	entry, err := m.pool.acquire(ctx, m, g.srv, g.root)
	if err != nil {
		// Not up in time (or refused): the files go pending; the start, when
		// it finishes, serves whatever arrives through the late path.
		slog.Debug("lsp: no instance for the diagnostics window", "server", g.srv.ID, "err", err)
		st.addPending(g.paths())
		return
	}
	m.pool.begin(entry)
	defer m.pool.end(entry)
	docs, inst := entry.docs, entry.inst
	st.mu.Lock()
	st.acquired[g.srv.ID] = true
	st.mu.Unlock()

	baseSnap := m.pool.diags.Snapshot(g.srv.ID) // the baseline for files we did not write
	seq0 := m.pool.diags.Seq()

	bases := make([][]Problem, len(g.files))
	unavailable := false
	for i, ch := range g.files {
		path := filepath.Clean(ch.AbsPath)
		if ch.After == nil {
			// Deleted (spec §8.3.3): the server forgets the document, the
			// file is not reported.
			if err := docs.Close(ctx, path); err != nil {
				slog.Debug("lsp: didClose failed", "path", path, "err", err)
			}
			m.pool.diags.Forget(g.srv.ID, path)
			continue
		}
		var base []Problem
		switch {
		case docs.IsOpen(path):
			base = baseSnap[path] // opened earlier: the store is the baseline
		case ch.Before != nil:
			// The pre-edit content opens for a baseline, within half the
			// window (spec §8.3.3).
			if v, err := docs.OpenWith(ctx, path, ch.Before); err != nil {
				unavailable = true
				slog.Debug("lsp: baseline open failed", "path", path, "err", err)
			} else {
				half := start.Add(eff.Wait / 2)
				if deadline.Before(half) {
					half = deadline
				}
				bCtx, bcancel := context.WithDeadline(ctx, half)
				if m.pool.diags.WaitFor(bCtx, g.srv.ID, path, v, seq0) {
					if problems, _, _, ok := m.pool.diags.Get(g.srv.ID, path); ok {
						base = problems
					}
				} else {
					unavailable = true
				}
				bcancel()
			}
		}
		// A new file keeps an empty baseline (spec §8.3.3).
		bases[i] = base

		seq1 := m.pool.diags.Seq()
		v, err := docs.Change(ctx, path, ch.After)
		if err != nil {
			slog.Debug("lsp: document sync failed", "path", path, "err", err)
			st.addPending([]string{path})
			continue
		}
		if err := docs.Save(ctx, path); err != nil {
			slog.Debug("lsp: didSave failed", "path", path, "err", err)
		}
		m.pool.notePath(entry, path)

		// Collect (spec §8.3.2): pull when the server can, otherwise wait
		// for a push of at least the version we sent.
		done := false
		if supported, err := PullDiagnostics(ctx, inst, m.pool.diags, g.srv.ID, path); supported && err == nil {
			done = true
		} else if supported {
			slog.Debug("lsp: pull diagnostics failed", "server", g.srv.ID, "err", err)
		}
		if !done && !m.pool.diags.WaitFor(ctx, g.srv.ID, path, v, seq1) {
			st.addPending([]string{path})
		}
	}

	// The quiet period (spec §8.3.2): after the target files answered, wait
	// out quietWindow for other files, within the window's remainder.
	if m.pool.now().Before(deadline) {
		m.pool.diags.WaitQuiet(ctx, g.srv.ID, quietWindow, deadline)
	}

	current := m.pool.diags.Snapshot(g.srv.ID)
	wrote := g.wroteSet()
	for i, ch := range g.files {
		path := filepath.Clean(ch.AbsPath)
		if ch.After == nil || st.isPending(path) {
			continue
		}
		st.mu.Lock()
		st.problems = append(st.problems, NewProblems(bases[i], current[path])...)
		st.mergeKnown(path, g.srv.ID, current[path])
		st.mu.Unlock()
	}
	// Other files this window published, inside the project (spec §8.3.4):
	// the delta against the window's opening snapshot.
	for _, path := range m.pool.diags.PublishedSince(g.srv.ID, seq0) {
		if wrote[path] {
			continue
		}
		if !pathWithin(path, m.opts.ProjectRoot) {
			continue
		}
		st.mu.Lock()
		st.problems = append(st.problems, NewProblems(baseSnap[path], current[path])...)
		st.mergeKnown(path, g.srv.ID, current[path])
		st.mu.Unlock()
	}
	if unavailable {
		st.mu.Lock()
		st.baselineUnavailable = true
		st.mu.Unlock()
	}
}

// mergeServerProblems replaces serverID's slice of the known list with cur,
// keeping every other server's problems (the store keys by server; the late
// queue keys by file).
func mergeServerProblems(known []Problem, serverID string, cur []Problem) []Problem {
	var out []Problem
	for _, p := range known {
		if p.ServerID != serverID {
			out = append(out, p)
		}
	}
	out = append(out, cur...)
	return out
}

// beginWindow registers the edited files on the session's late queue and
// shields them from late delivery while the window runs (spec §8.4).
func (m *Manager) beginWindow(sid string, eff appcfg.EffectiveLSP, paths []string) {
	if sid == "" || !eff.LateDelivery {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.late[sid]
	if s == nil {
		s = &lateSession{
			edited:   map[string]time.Time{},
			known:    map[string][]Problem{},
			pending:  map[string]LateChange{},
			inWindow: map[string]bool{},
		}
		m.late[sid] = s
	}
	now := time.Now()
	for _, p := range paths {
		s.edited[p] = now
		s.inWindow[p] = true
	}
}

// finishWindow lifts the in-window shield and adopts the current problem
// lists as the session's known state (spec §8.4).
func (m *Manager) finishWindow(sid string, known map[string][]Problem) {
	if sid == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.late[sid]
	if s == nil {
		return
	}
	for path, problems := range known {
		if len(problems) == 0 {
			delete(s.known, path)
			continue
		}
		s.known[path] = problems
	}
	s.inWindow = map[string]bool{}
	m.pruneLateLocked(time.Now())
}

// onDiagPublish is the diagnostics-store subscription behind the late queue
// (spec §8.4): a publish after the wait window for a file this session
// edited becomes that session's pending change.
func (m *Manager) onDiagPublish(serverID, absPath string) {
	path := filepath.Clean(absPath)
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	var cur []Problem
	if problems, _, _, ok := m.pool.diags.Get(serverID, path); ok {
		cur = problems
	}
	for _, s := range m.late {
		editedAt, edited := s.edited[path]
		if !edited || editedAt.Before(now.Add(-lateEditedTTL)) || s.inWindow[path] {
			continue // not this session's file, too old, or inside its window
		}
		merged := mergeServerProblems(s.known[path], serverID, cur)
		var change LateChange
		if news := NewProblems(s.known[path], merged); len(news) > 0 {
			change = LateChange{Path: path, New: news}
		} else if len(s.known[path]) > 0 && len(merged) == 0 {
			change = LateChange{Path: path, Cleared: true}
		} else {
			continue
		}
		s.pending[path] = change
		s.token++
	}
	m.pruneLateLocked(now)
}

// pruneLateLocked drops expired edits and empty sessions; the caller holds
// m.mu.
func (m *Manager) pruneLateLocked(now time.Time) {
	cutoff := now.Add(-lateEditedTTL)
	for sid, s := range m.late {
		for path, at := range s.edited {
			if at.Before(cutoff) {
				delete(s.edited, path)
			}
		}
		if len(s.edited) == 0 && len(s.pending) == 0 {
			delete(m.late, sid)
		}
	}
}

// PeekLate renders the pending late diagnostics for one agent session
// (spec §8.4), with a token AckLate expects.
func (m *Manager) PeekLate(agentSessionID string) (string, uint64) {
	if m == nil || agentSessionID == "" {
		return "", 0
	}
	m.mu.Lock()
	s := m.late[agentSessionID]
	var (
		token   uint64
		changes []LateChange
	)
	if s != nil && len(s.pending) > 0 {
		token = s.token
		paths := make([]string, 0, len(s.pending))
		for path := range s.pending {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			changes = append(changes, s.pending[path])
		}
	}
	m.pruneLateLocked(time.Now())
	m.mu.Unlock()
	if len(changes) == 0 {
		return "", 0
	}
	eff := m.pool.config().EffectiveLSP()
	text := FormatLateDiagnostics(changes, m.reportOptions(eff, nil))
	if text == "" {
		// Everything fell below the severity threshold: the model will never
		// see these, so the queue empties here.
		m.mu.Lock()
		if s := m.late[agentSessionID]; s != nil {
			s.pending = map[string]LateChange{}
		}
		m.mu.Unlock()
		return "", 0
	}
	return text, token
}

// AckLate marks everything up to token as delivered (spec §8.4).
func (m *Manager) AckLate(agentSessionID string, token uint64) {
	if m == nil || agentSessionID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.late[agentSessionID]
	if s == nil || token != s.token {
		return // an older token: newer changes arrived, the next Peek carries them all
	}
	for path, change := range s.pending {
		if change.Cleared {
			s.known[path] = nil
			continue
		}
		s.known[path] = append(s.known[path], change.New...)
	}
	s.pending = map[string]LateChange{}
}

// DidRead pre-opens a read file on the server that already runs for it — a
// diagnostics baseline for later edits. It never starts a server (spec §7.6)
// and returns immediately.
func (m *Manager) DidRead(ctx context.Context, absPath string, content []byte) {
	if m == nil || !m.gated() {
		return
	}
	primary, _ := ServersForFile(m.servers(), absPath, m.opts.ProjectRoot)
	if primary == nil {
		return
	}
	root, ok := ResolveRoot(absPath, m.opts.ProjectRoot, *primary)
	if !ok {
		return
	}
	entry := m.pool.findStarted(primary.ID, primary.Fingerprint, root)
	if entry == nil {
		return
	}
	path := filepath.Clean(absPath)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Debug("lsp: DidRead failed", "path", path, "panic", r)
			}
		}()
		ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		m.pool.begin(entry)
		defer m.pool.end(entry)
		if _, _, err := entry.docs.EnsureSynced(ctx2, path); err != nil {
			slog.Debug("lsp: DidRead sync failed", "path", path, "err", err)
			return
		}
		m.pool.notePath(entry, path)
	}()
}

// DidRunShell rescans what a shell command may have changed: open documents
// re-sync from the disk, and git-reported changes reach servers that watch
// files. It returns immediately; the scan is background work, at most once
// per second.
func (m *Manager) DidRunShell(ctx context.Context) {
	if m == nil || !m.gated() {
		return
	}
	m.mu.Lock()
	now := m.pool.now()
	if now.Sub(m.lastSweep) < time.Second {
		m.mu.Unlock()
		return
	}
	m.lastSweep = now
	m.mu.Unlock()
	go m.sweep()
}

// sweep is the background half of DidRunShell.
func (m *Manager) sweep() {
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("lsp: post-shell sweep failed", "panic", r)
		}
	}()
	entries := m.pool.entriesFor(m)
	// 1. Open documents follow the disk again: an mtime change becomes a
	// didChange.
	for _, e := range entries {
		m.pool.begin(e)
		for _, path := range m.pool.openPaths(e) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, _, _ = e.docs.EnsureSynced(ctx, path)
			cancel()
		}
		m.pool.end(e)
	}
	changes := m.gitChanges()
	if len(changes) == 0 || len(entries) == 0 {
		return
	}
	// 2. Watched files: changes the shell made, for servers that registered
	// workspace/didChangeWatchedFiles, on documents the server does not
	// have open.
	for _, e := range entries {
		regs := e.inst.Registrations("workspace/didChangeWatchedFiles")
		if len(regs) == 0 {
			continue
		}
		watchers := parseWatchers(regs)
		if len(watchers) == 0 {
			continue
		}
		var events []FileEvent
		for _, ch := range changes {
			if e.docs.IsOpen(ch.path) {
				continue // open documents were synced above
			}
			if !watcherMatches(watchers, e.inst.Folders(), ch.path) {
				continue
			}
			events = append(events, FileEvent{URI: PathToURI(ch.path), Type: ch.typ})
		}
		if len(events) == 0 {
			continue
		}
		if err := e.inst.Notify("workspace/didChangeWatchedFiles", map[string]any{"changes": events}); err != nil {
			slog.Debug("lsp: watched-file notification failed", "server", e.serverID, "err", err)
		}
	}
}

// gitChange is one path a shell command may have touched; typ is the LSP
// FileEvent kind: 1 created, 2 changed, 3 deleted.
type gitChange struct {
	path string
	typ  int
}

// maxSweepPaths caps one sweep.
const maxSweepPaths = 2000

// gitChanges diffs `git status` against the previous sweep's snapshot; the
// first scan only builds the snapshot. git prints paths relative to the
// repository root from any working directory, so the toplevel is resolved
// once to map them back.
func (m *Manager) gitChanges() []gitChange {
	root := m.opts.ProjectRoot
	if root == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	repo := strings.TrimSpace(gitLine(ctx, root, "rev-parse", "--show-toplevel"))
	if repo == "" {
		repo = root
	}
	out, err := exec.CommandContext(ctx, "git", "-C", root, "status", "--porcelain=v1", "-z", "--untracked-files=all").Output()
	if err != nil {
		slog.Debug("lsp: git status failed", "dir", root, "err", err)
		return nil
	}
	nowPaths := make(map[string]time.Time, 64)
	for _, token := range strings.Split(string(out), "\x00") {
		if !isStatusToken(token) {
			continue // e.g. the previous path of a rename record
		}
		rel := token[3:]
		if rel == "" {
			continue
		}
		path := filepath.Join(repo, filepath.FromSlash(rel))
		var mod time.Time
		if st, err := os.Stat(path); err == nil {
			mod = st.ModTime()
		}
		nowPaths[path] = mod // zero mtime: the path is gone (deleted)
	}
	m.mu.Lock()
	first := m.gitSnapshot == nil
	prev := m.gitSnapshot
	m.gitSnapshot = nowPaths
	m.mu.Unlock()
	if first {
		return nil
	}
	changes := make([]gitChange, 0, 16)
	for path, mod := range nowPaths {
		old, existed := prev[path]
		switch {
		case !existed:
			if !mod.IsZero() {
				changes = append(changes, gitChange{path, 1})
			}
		case mod.IsZero():
			if !old.IsZero() {
				changes = append(changes, gitChange{path, 3})
			}
		case !mod.Equal(old):
			changes = append(changes, gitChange{path, 2})
		}
	}
	for path := range prev {
		if _, ok := nowPaths[path]; ok {
			continue
		}
		// Leaving `git status` is not deletion: git add and git commit
		// drop a file that still exists from the listing. A path gone
		// from disk is deleted (LSP FileEvent 3); a survivor is a
		// change (2) so watchers re-read it.
		typ := 3
		if _, err := os.Stat(path); err == nil {
			typ = 2
		}
		changes = append(changes, gitChange{path, typ})
	}
	if len(changes) > maxSweepPaths {
		changes = changes[:maxSweepPaths]
	}
	sort.Slice(changes, func(a, b int) bool { return changes[a].path < changes[b].path })
	return changes
}

// isStatusToken reports whether one -z record starts with git's two status
// columns and a space; anything else (a rename's previous path) is skipped.
func isStatusToken(token string) bool {
	if len(token) < 3 || token[2] != ' ' {
		return false
	}
	return statusChar(token[0]) && statusChar(token[1])
}

func statusChar(c byte) bool {
	return c == ' ' || strings.IndexByte("MADRCUT?!", c) >= 0
}

// gitLine runs one git command in dir and returns its first output line.
func gitLine(ctx context.Context, dir string, args ...string) string {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.SplitN(string(out), "\n", 2)[0]
}

// watcher is one watcher of a didChangeWatchedFiles registration: a glob and
// the directory it is relative to ("" = the workspace folders).
type watcher struct {
	base string
	glob string
}

// parseWatchers reads the registerOptions of every live registration; a
// globPattern is either a plain glob or a RelativePattern.
func parseWatchers(regs []json.RawMessage) []watcher {
	var out []watcher
	for _, reg := range regs {
		var opts struct {
			Watchers []struct {
				GlobPattern json.RawMessage `json:"globPattern"`
			} `json:"watchers"`
		}
		if err := json.Unmarshal(reg, &opts); err != nil {
			continue
		}
		for _, w := range opts.Watchers {
			var plain string
			if err := json.Unmarshal(w.GlobPattern, &plain); err == nil && plain != "" {
				out = append(out, watcher{glob: plain})
				continue
			}
			var relative struct {
				BaseURI string `json:"baseUri"`
				Pattern string `json:"pattern"`
			}
			if err := json.Unmarshal(w.GlobPattern, &relative); err == nil && relative.Pattern != "" {
				base := ""
				if dir, err := URIToPath(relative.BaseURI); err == nil {
					base = dir
				}
				out = append(out, watcher{base: base, glob: relative.Pattern})
			}
		}
	}
	return out
}

// watcherMatches reports whether path hits any watcher. A plain glob is
// matched against the path relative to each workspace folder; a
// RelativePattern's glob is relative to its baseUri directory.
func watcherMatches(watchers []watcher, folderURIs []string, path string) bool {
	for _, w := range watchers {
		if w.base != "" {
			if rel, ok := relSlash(w.base, path); ok && matchWatcherGlob(w.glob, rel) {
				return true
			}
			continue
		}
		for _, uri := range folderURIs {
			folder, err := URIToPath(uri)
			if err != nil {
				continue
			}
			if rel, ok := relSlash(folder, path); ok && matchWatcherGlob(w.glob, rel) {
				return true
			}
		}
	}
	return false
}

// matchWatcherGlob matches relPath (slash-separated, relative) against one
// watcher glob: `**` spans directories, `*` and `?` stay inside a segment,
// and `{a,b}` alternates.
func matchWatcherGlob(pattern, relPath string) bool {
	pattern = strings.TrimPrefix(pattern, "./")
	for _, p := range expandBraces(pattern) {
		if globMatch(strings.Split(p, "/"), strings.Split(relPath, "/")) {
			return true
		}
	}
	return false
}

// globMatch matches slash-split segments; "**" consumes any number of them.
func globMatch(pat, name []string) bool {
	if len(pat) == 0 {
		return len(name) == 0
	}
	if pat[0] == "**" {
		for skip := 0; skip <= len(name); skip++ {
			if globMatch(pat[1:], name[skip:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 || !matchSegment(pat[0], name[0]) {
		return false
	}
	return globMatch(pat[1:], name[1:])
}

// matchSegment matches one segment against a pattern with `*` and `?`.
func matchSegment(pat, name string) bool {
	pi, ni := 0, 0
	star, starName := -1, 0
	for ni < len(name) {
		switch {
		case pi < len(pat) && (pat[pi] == '?' || pat[pi] == name[ni]):
			pi++
			ni++
		case pi < len(pat) && pat[pi] == '*':
			star = pi
			pi++
			starName = ni
		case star >= 0:
			pi = star + 1
			starName++
			ni = starName
		default:
			return false
		}
	}
	for pi < len(pat) && pat[pi] == '*' {
		pi++
	}
	return pi == len(pat)
}

// expandBraces expands `{a,b}` alternates, nesting included: the first `{`
// pairs with its own matching `}` — not with the last one in the pattern,
// which would mis-split parallel groups like `x{1,2}/y{a,b}`.
func expandBraces(pattern string) []string {
	open := strings.IndexByte(pattern, '{')
	if open < 0 {
		return []string{pattern}
	}
	closeIdx := -1
	for i, depth := open, 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				closeIdx = i
			}
		}
		if closeIdx >= 0 {
			break
		}
	}
	if closeIdx < 0 {
		return []string{pattern}
	}
	var out []string
	for _, alt := range splitAlternates(pattern[open+1 : closeIdx]) {
		out = append(out, expandBraces(pattern[:open]+alt+pattern[closeIdx+1:])...)
	}
	return out
}

// splitAlternates splits "a,b,c" on commas outside braces.
func splitAlternates(s string) []string {
	var (
		out   []string
		depth int
		last  int
	)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[last:i])
				last = i + 1
			}
		}
	}
	return append(out, s[last:])
}

// drainBackground waits out the manager's background writers (the orphan
// sweep and binary detections) so nothing writes into the agent workspace
// after Close returned.
func (m *Manager) drainBackground() {
	m.bg.Wait()
}

// ReleaseIdle stops this runner's instances idle for over a minute; the
// runner pool calls it when a session goes quiet — more eager than the
// janitor's lsp.idle_timeout.
func (m *Manager) ReleaseIdle() {
	if m == nil {
		return
	}
	m.pool.releaseIdleFor(m)
}

// reportOptions shapes the model-facing diagnostic text (spec §8.3 step 6).
// The position callback is fresh per report and reads each file at most
// once: every problem of one file reuses the first read's content.
func (m *Manager) reportOptions(eff appcfg.EffectiveLSP, firstPaths []string) ReportOptions {
	return ReportOptions{
		ProjectRoot: m.opts.ProjectRoot,
		MinSeverity: SeverityThreshold(eff.MinSeverity),
		MaxPerFile:  eff.MaxPerFile,
		MaxFiles:    eff.MaxFiles,
		FirstPaths:  firstPaths,
		Position:    (&positionResolver{m: m, files: map[string]resolvedContent{}}).position,
	}
}

// resolvedContent is one path's readable content, or not-ok when nothing
// readable exists (kept so the failure is not retried per problem).
type resolvedContent struct {
	content []byte
	ok      bool
}

// positionResolver maps problems to 1-based coordinates through the
// producing instance's document content — the disk when the server no
// longer holds the document — falling back to the raw LSP numbers when
// nothing is readable (spec §8.2). One resolver serves one report, which
// runs on one goroutine, so its cache needs no lock.
type positionResolver struct {
	m     *Manager
	files map[string]resolvedContent
}

func (r *positionResolver) position(p Problem) (line, column int) {
	if view := r.m.pool.instanceForServer(p.ServerID, p.Path); view != nil && view.inst != nil {
		f, cached := r.files[p.Path]
		if !cached {
			f.content, f.ok = documentContent(view, p.Path)
			r.files[p.Path] = f
		}
		if f.ok {
			return FromLSPPosition(f.content, Position{Line: uint32(p.Line), Character: uint32(p.Character)}, view.inst.Encoding())
		}
	}
	return p.Line + 1, p.Character + 1
}

func documentContent(view *instanceView, path string) (content []byte, ok bool) {
	if view.docs != nil {
		if c, _, open := view.docs.Content(path); open {
			return c, true
		}
	}
	disk, err := os.ReadFile(path)
	if err != nil || len(disk) > MaxDocumentBytes {
		return nil, false
	}
	return disk, true
}
