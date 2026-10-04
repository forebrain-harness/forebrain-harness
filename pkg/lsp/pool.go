package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

// janitorInterval is how often the pool checks for idle instances
// (spec §7.8). A package-level variable only so tests can shorten it;
// production code must not write it.
var janitorInterval = time.Minute

// instanceShutdownTimeout bounds one background instance shutdown.
const instanceShutdownTimeout = 10 * time.Second

// errPoolClosed answers acquires on a pool that already closed.
var errPoolClosed = errors.New("the language server pool is closed")

// sweptPIDFiles records the pid files this process already swept: one sweep
// per process per path (spec §7.8).
var sweptPIDFiles sync.Map

// poolInstance is one running instance together with everything the pool
// layers on top of it (spec §4.3, §7.5): the keys it serves (a multi-root
// instance answers several), the managers sharing it, and its usage
// accounting.
type poolInstance struct {
	keys        []string // every key this instance serves (multi-root aliases)
	root        string   // the workspace root it was started with
	serverID    string
	fingerprint string
	projectRoot string
	srv         ServerConfig
	inst        *Instance           // set under Pool.mu when the start attempt finished
	docs        *DocSync            // set together with inst
	paths       map[string]struct{} // documents opened through the pool (a superset of docs: eviction only removes)
	refs        map[*Manager]struct{}
	lastUsed    time.Time
	inflight    int
	ready       chan struct{} // closed when the start attempt finished
	startErr    error
}

// started reports whether the start attempt behind e already settled.
func started(e *poolInstance) bool {
	select {
	case <-e.ready:
		return true
	default:
		return false
	}
}

// instanceView is what callers outside Pool.mu need from one entry.
type instanceView struct {
	root     string
	inst     *Instance // nil while the first start is still running
	docs     *DocSync
	startErr error
}

// stateOf is what the snapshot shows: the instance's state, or what the
// pool can say while the first start is still in flight.
func (v instanceView) stateOf() InstanceState {
	if v.inst == nil {
		if v.startErr != nil {
			return StateFailed
		}
		return StateStarting
	}
	return v.inst.State()
}

// Pool is the process-wide owner of language-server instances. One per
// process.Environment; every runner gets a Manager from it.
type Pool struct {
	cfg         atomic.Pointer[appcfg.Root]
	mu          sync.Mutex
	managers    map[*Manager]struct{}
	closed      bool
	instances   map[string]*poolInstance // key: serverID \x00 fingerprint \x00 root (spec §7.5)
	diags       *DiagStore
	janitorStop chan struct{}
	now         func() time.Time // injectable clock
}

// NewPool builds the pool over the configuration in force and starts the
// idle janitor (spec §7.8); Close stops it again.
func NewPool(cfg *appcfg.Root) *Pool {
	p := &Pool{
		managers:    map[*Manager]struct{}{},
		instances:   map[string]*poolInstance{},
		diags:       NewDiagStore(),
		janitorStop: make(chan struct{}),
		now:         time.Now,
	}
	p.cfg.Store(cfg)
	go p.janitor()
	return p
}

// config returns the configuration in force.
func (p *Pool) config() *appcfg.Root {
	if p == nil {
		return nil
	}
	return p.cfg.Load()
}

// instanceKey is the instance identity (spec §7.5): server id, configuration
// fingerprint and workspace root, NUL-joined (no path can contain one).
func instanceKey(serverID, fingerprint, root string) string {
	return serverID + "\x00" + fingerprint + "\x00" + root
}

// acquire returns the instance for (srv, root), starting one when none runs
// (spec §7.5): an existing key is shared; a multi-root-capable instance of
// the same server, fingerprint and project gets the folder added; otherwise
// the max_servers cap applies before a launch. The caller's ctx bounds only
// the wait — the start itself always runs to completion.
func (p *Pool) acquire(ctx context.Context, m *Manager, srv ServerConfig, root string) (*poolInstance, error) {
	key := instanceKey(srv.ID, srv.Fingerprint, root)

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errPoolClosed
	}
	// An entry for this key exists: share it and wait for its start. A
	// settled entry answers without consulting the ctx — no waiting is
	// involved, and an already-expired window must not race it away.
	if entry, ok := p.instances[key]; ok {
		entry.refs[m] = struct{}{}
		ready := entry.ready
		p.mu.Unlock()
		select {
		case <-ready:
		default:
			select {
			case <-ready:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if entry.startErr != nil {
			// A failed start leaves the table so the next call retries.
			p.mu.Lock()
			if p.instances[key] == entry {
				p.removeLocked(entry)
			}
			p.mu.Unlock()
			return nil, entry.startErr
		}
		return entry, nil
	}

	// Multi-root reuse (spec §7.5): the same server, fingerprint and
	// project in a ready instance that watches workspace folders.
	var reuse *poolInstance
	for _, e := range p.instances {
		if e.serverID != srv.ID || e.fingerprint != srv.Fingerprint || !SamePath(e.projectRoot, m.opts.ProjectRoot) {
			continue
		}
		if !started(e) || e.startErr != nil || e.inst == nil {
			continue
		}
		if !e.inst.Capabilities().WorkspaceFolderChanges() {
			continue
		}
		reuse = e
		break
	}
	if reuse != nil {
		inst := reuse.inst
		p.mu.Unlock()
		if err := inst.AddFolder(ctx, root); err == nil {
			p.mu.Lock()
			if existing, ok := p.instances[key]; ok {
				existing.refs[m] = struct{}{}
				p.mu.Unlock()
				return existing, nil
			}
			if p.listedLocked(reuse) && !p.closed {
				reuse.keys = append(reuse.keys, key)
				p.instances[key] = reuse
				reuse.refs[m] = struct{}{}
				p.mu.Unlock()
				return reuse, nil
			}
			p.mu.Unlock()
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, errPoolClosed
		}
	}

	// The cap (spec §7.5, appendix C too-many-servers): over max_servers,
	// stop the idle instance used least recently; with nothing idle to
	// stop, refuse. Multi-root reuse maps several table keys to one
	// process, so the cap counts distinct instances, not keys — like the
	// janitor, releaseFor and Close dedups below.
	distinct := map[*poolInstance]bool{}
	for _, e := range p.instances {
		distinct[e] = true
	}
	if len(distinct) >= p.config().EffectiveLSP().MaxServers {
		var victim *poolInstance
		for e := range distinct {
			if e.inflight != 0 || !started(e) {
				continue // an in-flight request or start is not idle
			}
			if victim == nil || e.lastUsed.Before(victim.lastUsed) {
				victim = e
			}
		}
		if victim == nil {
			n := len(distinct)
			p.mu.Unlock()
			return nil, fmt.Errorf("too many language servers are running (%d); raise lsp.max_servers or stop one with /lsp", n)
		}
		p.removeLocked(victim)
		p.mu.Unlock()
		p.shutdownEntry(victim)
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, errPoolClosed
		}
	}

	entry := &poolInstance{
		keys:        []string{key},
		root:        root,
		serverID:    srv.ID,
		fingerprint: srv.Fingerprint,
		projectRoot: m.opts.ProjectRoot,
		srv:         srv,
		paths:       map[string]struct{}{},
		refs:        map[*Manager]struct{}{m: {}},
		lastUsed:    p.now(),
		ready:       make(chan struct{}),
	}
	p.instances[key] = entry
	p.mu.Unlock()

	go p.start(entry, m)

	select {
	case <-entry.ready:
	default:
		select {
		case <-entry.ready:
		case <-ctx.Done():
			return nil, ctx.Err() // the start keeps running in the background
		}
	}
	if entry.startErr != nil {
		p.mu.Lock()
		if p.instances[key] == entry {
			p.removeLocked(entry)
		}
		p.mu.Unlock()
		return nil, entry.startErr
	}
	return entry, nil
}

// listedLocked reports whether any of entry's keys still maps to it; the
// caller holds p.mu.
func (p *Pool) listedLocked(entry *poolInstance) bool {
	for _, k := range entry.keys {
		if p.instances[k] == entry {
			return true
		}
	}
	return false
}

// removeLocked drops every key entry holds in the table; the caller holds
// p.mu. A multi-root entry must vanish whole.
func (p *Pool) removeLocked(entry *poolInstance) {
	for _, k := range entry.keys {
		if p.instances[k] == entry {
			delete(p.instances, k)
		}
	}
}

// launchPlan is what starting srv at root needs, computed the same way for
// the pool and for forebrain lsp doctor.
type launchPlan struct {
	Spec    InstanceSpec
	Detect  DetectResult
	Missing []string // ${VAR} references that did not resolve
}

// planLaunch assembles the InstanceSpec for one server at one root: the
// environment, the binary detection, the cache directory and the placeholder
// expansion (spec §9.3, §6.4, §6.2). Callbacks are left nil; the caller fills
// the ones it needs before StartInstance.
func planLaunch(ctx context.Context, srv ServerConfig, root, home, agentWorkspace string) (launchPlan, error) {
	id := srv.ID
	env, missing := BuildEnv(EnvSpec{Passthrough: srv.EnvPassthrough, Env: srv.Env, FromProject: srv.EnvFromProject, Home: home})
	if len(missing) > 0 {
		// Written before the server opens its log, so the note leads it.
		logFirstLine(LogPath(agentWorkspace, id, root), "missing environment variables: "+strings.Join(missing, ", "))
	}

	det := Detect(ctx, srv, env, runtime.GOOS, filepath.Join(StateDir(agentWorkspace), "detect.json"))
	if !det.Installed {
		suffix := ""
		if det.InstallCommand != "" {
			suffix = "; install it with: " + det.InstallCommand
		}
		return launchPlan{}, fmt.Errorf("language server %s is enabled but its command %q was not found%s", id, srv.Command, suffix) // appendix C, not-installed
	}

	cache := CacheDir(agentWorkspace, id, root)
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return launchPlan{}, fmt.Errorf("language server %s failed to start: %v", id, err) // appendix C, start-failed
	}
	args := make([]string, len(srv.Args))
	for i, a := range srv.Args {
		args[i] = ExpandCacheDir(a, cache)
	}
	return launchPlan{
		Spec: InstanceSpec{
			ServerID:              id,
			Command:               det.Path,
			Args:                  args,
			Env:                   env,
			Root:                  root,
			InitializationOptions: ExpandCacheDirJSON(srv.InitializationOptions, cache),
			Settings:              ExpandCacheDirJSON(srv.Settings, cache),
			AutoAnswers:           srv.AutoAnswers,
			Readiness:             srv.Readiness,
			StartupTimeout:        srv.StartupTimeout,
			ShutdownTimeout:       srv.ShutdownTimeout,
			RestartOnCrash:        srv.RestartOnCrash,
			MaxRestarts:           srv.MaxRestarts,
			LogPath:               LogPath(agentWorkspace, id, root),
			PIDFile:               PIDFile(agentWorkspace),
		},
		Detect:  det,
		Missing: missing,
	}, nil
}

// start launches one instance in the background (spec §7.8) and settles
// entry.ready whatever happens. It runs detached from any caller's context:
// callers stop waiting, the start continues.
func (p *Pool) start(entry *poolInstance, m *Manager) {
	srv := entry.srv
	id, root := srv.ID, entry.root
	ws := m.opts.AgentWorkspace

	plan, err := planLaunch(context.Background(), srv, root, m.opts.Home, ws)
	if err != nil {
		p.settle(entry, nil, err)
		return
	}
	spec := plan.Spec
	spec.OnNotification = p.onNotification(id)
	spec.OnStateChange = p.notifyRefs(entry)
	spec.OnRestart = func(ctx context.Context) {
		p.mu.Lock()
		docs := entry.docs
		p.mu.Unlock()
		if docs != nil {
			docs.Reopen(ctx) // the restarted server re-opens what it had
		}
	}
	inst, err := StartInstance(context.Background(), spec)
	if err != nil {
		p.settle(entry, nil, fmt.Errorf("language server %s failed to start: %v", id, err)) // appendix C, start-failed
		return
	}
	p.settle(entry, inst, nil)
}

// settle records a start outcome and wakes every waiter. The assignments
// happen under Pool.mu so janitor, reconcile and snapshot readers always
// synchronize with them.
func (p *Pool) settle(entry *poolInstance, inst *Instance, err error) {
	var docs *DocSync
	if inst != nil {
		docs = NewDocSync(inst, languageFor(entry.srv), 64)
	}
	p.mu.Lock()
	entry.inst = inst
	entry.docs = docs
	entry.startErr = err
	p.mu.Unlock()
	close(entry.ready)
	p.notifyRefs(entry)()
}

// onNotification returns the notification sink for one server: only
// textDocument/publishDiagnostics reaches the store (spec §7.3); the
// instance handled everything else itself.
func (p *Pool) onNotification(serverID string) func(method string, params json.RawMessage) {
	return func(method string, params json.RawMessage) {
		if method != "textDocument/publishDiagnostics" {
			return
		}
		var pub PublishDiagnosticsParams
		if err := json.Unmarshal(params, &pub); err != nil {
			return
		}
		path, err := URIToPath(pub.URI)
		if err != nil {
			return // not a local file: nothing to key the store by
		}
		p.diags.Publish(serverID, path, pub.Version, pub.Diagnostics)
	}
}

// notifyRefs returns the OnStateChange callback of one instance: every
// manager sharing it re-publishes its /lsp snapshot (debounced).
func (p *Pool) notifyRefs(entry *poolInstance) func() {
	return func() {
		p.mu.Lock()
		ms := make([]*Manager, 0, len(entry.refs))
		for m := range entry.refs {
			ms = append(ms, m)
		}
		p.mu.Unlock()
		for _, m := range ms {
			m.notifySoon()
		}
	}
}

// begin and end bracket one use of an instance: the accounting behind the
// idle timeout and the eviction cap.
func (p *Pool) begin(entry *poolInstance) {
	p.mu.Lock()
	entry.inflight++
	entry.lastUsed = p.now()
	p.mu.Unlock()
}

func (p *Pool) end(entry *poolInstance) {
	p.mu.Lock()
	entry.inflight--
	entry.lastUsed = p.now()
	p.mu.Unlock()
}

// notePath records a document opened through this entry; the sweep re-syncs
// these, filtered by IsOpen so the DocSync eviction is respected.
func (p *Pool) notePath(entry *poolInstance, path string) {
	p.mu.Lock()
	entry.paths[path] = struct{}{}
	p.mu.Unlock()
}

// openPaths lists the entry's documents that are still open on the server.
func (p *Pool) openPaths(entry *poolInstance) []string {
	p.mu.Lock()
	paths := make([]string, 0, len(entry.paths))
	for path := range entry.paths {
		paths = append(paths, path)
	}
	p.mu.Unlock()
	sort.Strings(paths)
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if entry.docs.IsOpen(path) {
			out = append(out, path)
		}
	}
	return out
}

// janitor stops idle instances every janitorInterval (spec §7.8).
func (p *Pool) janitor() {
	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.janitorStop:
			return
		case <-ticker.C:
			p.janitorOnce(p.now())
		}
	}
}

// janitorOnce stops every started instance with no in-flight request whose
// last use is older than the configured idle timeout (spec §7.8). Split out
// of the loop so a test can run one step with an injected clock.
func (p *Pool) janitorOnce(now time.Time) {
	idle := p.config().EffectiveLSP().IdleTimeout
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	var stop []*poolInstance
	seen := map[*poolInstance]bool{}
	for _, e := range p.instances {
		if seen[e] {
			continue
		}
		seen[e] = true
		if e.inflight != 0 || !started(e) {
			continue
		}
		if now.Sub(e.lastUsed) > idle {
			p.removeLocked(e)
			stop = append(stop, e)
		}
	}
	p.mu.Unlock()
	for _, e := range stop {
		p.shutdownEntry(e)
	}
}

// shutdownEntry stops one entry's instance in the background, bounded by
// instanceShutdownTimeout. A start still in flight finishes first; its
// outcome decides whether there is anything to stop.
func (p *Pool) shutdownEntry(entry *poolInstance) {
	go func() {
		<-entry.ready
		p.mu.Lock()
		inst := entry.inst
		p.mu.Unlock()
		if inst == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), instanceShutdownTimeout)
		defer cancel()
		_ = inst.Shutdown(ctx)
	}()
}

// Reconcile adopts a reloaded configuration: every manager re-resolves its
// servers, and instances whose server vanished, was disabled, became
// invalid or changed its configuration fingerprint stop (spec §5.3).
func (p *Pool) Reconcile(cfg *appcfg.Root) {
	if p == nil || cfg == nil {
		return
	}
	p.cfg.Store(cfg)
	p.mu.Lock()
	managers := make([]*Manager, 0, len(p.managers))
	for m := range p.managers {
		managers = append(managers, m)
	}
	p.mu.Unlock()
	for _, m := range managers {
		m.invalidate()
	}

	p.mu.Lock()
	var stop []*poolInstance
	seen := map[*poolInstance]bool{}
	for _, e := range p.instances {
		if seen[e] {
			continue
		}
		seen[e] = true
		var m *Manager
		for ref := range e.refs {
			m = ref // any manager resolves the server the same way
			break
		}
		if m == nil {
			continue // nobody references it: releaseFor owns its end
		}
		var current *ServerConfig
		resolved := m.servers()
		for i := range resolved {
			if resolved[i].ID == e.serverID {
				current = &resolved[i]
				break
			}
		}
		if current == nil || !current.Enabled || current.Invalid != "" || current.Fingerprint != e.fingerprint {
			p.removeLocked(e)
			stop = append(stop, e)
		}
	}
	p.mu.Unlock()
	for _, e := range stop {
		p.shutdownEntry(e)
	}
	for _, m := range managers {
		m.notifySoon()
	}
}

// releaseFor drops m from every instance's refs; instances left without a
// reference stop (spec §4.3).
func (p *Pool) releaseFor(m *Manager) {
	if p == nil {
		return
	}
	p.mu.Lock()
	var stop []*poolInstance
	seen := map[*poolInstance]bool{}
	for _, e := range p.instances {
		if seen[e] {
			continue
		}
		seen[e] = true
		delete(e.refs, m)
		if len(e.refs) == 0 {
			p.removeLocked(e)
			stop = append(stop, e)
		}
	}
	p.mu.Unlock()
	for _, e := range stop {
		p.shutdownEntry(e)
	}
}

// releaseIdleFor stops m's instances that have been idle for a minute — the
// runner pool calls it when a session goes quiet, more eagerly than the
// janitor's lsp.idle_timeout.
func (p *Pool) releaseIdleFor(m *Manager) {
	if p == nil {
		return
	}
	cutoff := p.now().Add(-time.Minute)
	p.mu.Lock()
	var stop []*poolInstance
	seen := map[*poolInstance]bool{}
	for _, e := range p.instances {
		if seen[e] {
			continue
		}
		seen[e] = true
		if _, ref := e.refs[m]; !ref {
			continue
		}
		if e.inflight != 0 || !started(e) {
			continue
		}
		if e.lastUsed.After(cutoff) {
			continue
		}
		p.removeLocked(e)
		stop = append(stop, e)
	}
	p.mu.Unlock()
	for _, e := range stop {
		p.shutdownEntry(e)
	}
}

// stopServerInProject stops every instance of one server in one project.
func (p *Pool) stopServerInProject(serverID, projectRoot string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	var stop []*poolInstance
	seen := map[*poolInstance]bool{}
	for _, e := range p.instances {
		if seen[e] {
			continue
		}
		seen[e] = true
		if e.serverID != serverID || !SamePath(e.projectRoot, projectRoot) {
			continue
		}
		p.removeLocked(e)
		stop = append(stop, e)
	}
	p.mu.Unlock()
	for _, e := range stop {
		p.shutdownEntry(e)
	}
}

// instancesOfProject copies one server's instances for a project (any root),
// taken under Pool.mu so the fields are stable for the reader.
func (p *Pool) instancesOfProject(serverID, projectRoot string) []instanceView {
	if p == nil || projectRoot == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := map[*poolInstance]bool{}
	var out []instanceView
	for _, e := range p.instances {
		if seen[e] {
			continue
		}
		seen[e] = true
		if e.serverID != serverID || !SamePath(e.projectRoot, projectRoot) {
			continue
		}
		out = append(out, instanceView{root: e.root, inst: e.inst, docs: e.docs, startErr: e.startErr})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].root < out[b].root })
	return out
}

// entriesFor answers the started instances a manager references; callers
// may read their inst/docs directly (set before ready closed).
func (p *Pool) entriesFor(m *Manager) []*poolInstance {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := map[*poolInstance]bool{}
	var out []*poolInstance
	for _, e := range p.instances {
		if seen[e] {
			continue
		}
		seen[e] = true
		if _, ref := e.refs[m]; !ref || !started(e) || e.startErr != nil {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].root < out[b].root })
	return out
}

// instanceForServer answers a started instance of one server for rendering
// problem positions: the one holding path open — its synced content and
// encoding are what produced the problem — or, with none holding it, any
// running one; nil when none runs.
func (p *Pool) instanceForServer(serverID, path string) *instanceView {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var fallback *instanceView
	for _, e := range p.instances {
		if e.serverID != serverID || !started(e) || e.startErr != nil {
			continue
		}
		view := instanceView{root: e.root, inst: e.inst, docs: e.docs}
		if e.docs != nil && e.docs.IsOpen(path) {
			return &view
		}
		if fallback == nil {
			fallback = &view
		}
	}
	return fallback
}

// findStarted looks up a ready instance without starting anything; DidRead
// pre-opens documents only on instances that already run (spec §7.6).
func (p *Pool) findStarted(serverID, fingerprint, root string) *poolInstance {
	if p == nil {
		return nil
	}
	key := instanceKey(serverID, fingerprint, root)
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.instances[key]
	if e == nil || !started(e) || e.startErr != nil {
		return nil
	}
	return e
}

// NewManager returns the view one runner uses. After Close it returns a
// manager that is already closed.
func (p *Pool) NewManager(opts ManagerOptions) *Manager {
	m := &Manager{
		pool:      p,
		opts:      opts,
		late:      map[string]*lateSession{},
		detected:  map[string]detectEntry{},
		detecting: map[string]bool{},
	}
	if p == nil {
		m.closed = true
		return m
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		m.closed = true
		return m
	}
	p.managers[m] = struct{}{}
	p.mu.Unlock()
	m.start()
	return m
}

// Close stops every instance and closes every manager. Idempotent.
func (p *Pool) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	close(p.janitorStop)
	managers := make([]*Manager, 0, len(p.managers))
	for m := range p.managers {
		managers = append(managers, m)
	}
	entries := make([]*poolInstance, 0, len(p.instances))
	seen := map[*poolInstance]bool{}
	for _, e := range p.instances {
		if seen[e] {
			continue
		}
		seen[e] = true
		entries = append(entries, e)
	}
	p.managers = map[*Manager]struct{}{}
	p.instances = map[string]*poolInstance{}
	p.mu.Unlock()
	for _, m := range managers {
		m.Close()
	}
	// The managers' background writers (orphan sweep, binary detection)
	// must finish before this returns: the composition root tears the agent
	// workspace down right after Close.
	for _, m := range managers {
		m.drainBackground()
	}
	// Parallel shutdown with a total budget (spec §7.8); whatever misses
	// it is logged and keeps being torn down in the background.
	var wg sync.WaitGroup
	for _, e := range entries {
		wg.Add(1)
		go func(e *poolInstance) {
			defer wg.Done()
			<-e.ready
			p.mu.Lock()
			inst := e.inst
			p.mu.Unlock()
			if inst == nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), instanceShutdownTimeout)
			defer cancel()
			_ = inst.Shutdown(ctx)
		}(e)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(instanceShutdownTimeout):
		slog.Warn("lsp: shutting down language servers timed out; continuing in the background")
	}
	return nil
}

func (p *Pool) forget(m *Manager) {
	if p == nil {
		return
	}
	p.mu.Lock()
	delete(p.managers, m)
	p.mu.Unlock()
}

// logFirstLine appends one line to the instance log before the server opens
// it, so the note leads the file.
func logFirstLine(path, line string) {
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

// languageFor maps a path to its languageId through a server's tables:
// exact filenames first, then the lowercase extension.
func languageFor(srv ServerConfig) func(absPath string) string {
	return func(absPath string) string {
		if lang, ok := srv.Filenames[filepath.Base(absPath)]; ok {
			return lang
		}
		return srv.ExtensionToLanguage[strings.ToLower(filepath.Ext(absPath))]
	}
}
