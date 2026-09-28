package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ConnStatus string

const (
	ConnStatusUnknown      ConnStatus = "unknown"
	ConnStatusConnecting   ConnStatus = "connecting"
	ConnStatusConnected    ConnStatus = "connected"
	ConnStatusDisconnected ConnStatus = "disconnected"
	ConnStatusError        ConnStatus = "error"
	// ConnStatusCancelled is the terminal state of a server whose startup was
	// cancelled before it finished: the operator skipped an optional server
	// while waiting on the ready barrier, or the owning Runner closed. It is a
	// state of its own rather than a flavour of ConnStatusError because nothing
	// failed — there is no error text to show and the session must not report a
	// failure for it.
	ConnStatusCancelled ConnStatus = "cancelled"
)

type AuthStatus string

const (
	AuthStatusNone          AuthStatus = "none"
	AuthStatusConfigured    AuthStatus = "configured"
	AuthStatusAuthenticated AuthStatus = "authenticated"
	AuthStatusNeedsAuth     AuthStatus = "needs-auth"
	AuthStatusFailed        AuthStatus = "failed"
)

type ServerRecord struct {
	Name        string     `json:"name"`
	Transport   string     `json:"transport"`
	URL         string     `json:"url,omitempty"`
	ConnStatus  ConnStatus `json:"conn_status"`
	AuthStatus  AuthStatus `json:"auth_status"`
	Error       string     `json:"error,omitempty"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
	ToolCount   int        `json:"tool_count"`
	// Required marks a server whose tools the runtime treated as mandatory for
	// the generation this record belongs to. It travels with the record so a
	// surface can label a failure without re-reading the configuration, which
	// may have moved on since.
	Required bool `json:"required,omitempty"`
	// Generation names the MCP load generation this record was written by. A
	// record outlives the generation that wrote it only in the process-wide
	// mirror, where the name is what stops a stale generation from overwriting
	// a newer one.
	Generation string `json:"generation,omitempty"`

	session *Session
	// ownerSeq orders writers: a registry created later owns a higher sequence,
	// so a write from an older generation never replaces a newer generation's
	// record in a shared (mirror) registry.
	ownerSeq uint64
	// mirror records that this record was written through Mirror. Mirrored
	// records never hold a session, so a Close on the mirror cannot tear down a
	// connection a Runner still owns.
	mirror bool
}

// Settled reports whether this server reached a terminal startup state. A
// record still connecting is the only unsettled one; "connecting" is written
// when the generation registers its servers, so a server whose task has not
// been scheduled yet already counts towards the total.
func (rec ServerRecord) Settled() bool {
	return rec.ConnStatus != ConnStatusConnecting
}

// StatusUpdate is one state transition for one server of one load generation.
// It is the single write shape every producer funnels through Apply, so the
// ordering rule, the subscription notification and the ownership rule exist in
// exactly one place.
type StatusUpdate struct {
	Name       string
	Transport  string
	URL        string
	Status     ConnStatus
	AuthStatus AuthStatus
	Error      string
	ToolCount  int
	Required   bool
	Generation string
	// Session transfers ownership of a live connection to the registry. Only a
	// connected status carries one, and only to a registry that owns
	// connections: the process-wide mirror never receives one, so a Close on it
	// can never reclaim a connection a Runner is still using.
	Session *Session
}

// registrySeq orders registry instances process-wide, so a write can tell a
// newer generation from an older one.
var registrySeq atomic.Uint64

type subscriber struct {
	id uint64
	fn func([]ServerRecord)
}

type Registry struct {
	mu      sync.RWMutex
	servers map[string]*ServerRecord
	subs    map[uint64]subscriber
	nextSub uint64
	// seq is this registry's place in the process-wide order. Records written
	// by a higher seq win.
	seq uint64
	// deliver serializes "take the snapshot" with "hand it to subscribers", so
	// two concurrent writes cannot deliver their snapshots out of order (an
	// older snapshot arriving after a newer one would move a subscriber
	// backwards). It is always taken before mu, never after.
	deliver sync.Mutex
	// closed makes Close idempotent.
	closed bool
}

// NewRegistry creates a registry owned by one Runner.
//
// R7 makes ownership per-Runner rather than process-wide. The old shared
// registry was keyed by server name alone, so two Runners configured with the
// same server name — the primary agent and a subagent, or two primary agents
// across a switch — overwrote each other's session. Closing one Runner's MCP
// connections then tore down connections another was still using.
func NewRegistry() *Registry {
	return newRegistry()
}

// newRegistry builds one standalone registry. Nothing is published anywhere
// until the owner asks for it (see Mirror): the process-wide view is written by
// the Runner that owns the generation, not as a side effect of constructing a
// registry, so a registry built for a read-only purpose (a consent preview, a
// test) cannot report itself as a running server.
func newRegistry() *Registry {
	return &Registry{
		servers: make(map[string]*ServerRecord),
		subs:    make(map[uint64]subscriber),
		seq:     registrySeq.Add(1),
	}
}

// Close shuts every session this registry owns and empties it. It is safe to
// call more than once.
//
// Subscribers get one last, empty snapshot before they are dropped: the servers
// they were watching are gone with the generation, and leaving them on their
// last "connecting" snapshot would show a startup that can never finish. The
// notification is delivered outside the registry lock, and sessions are closed
// outside it too, so a slow subscriber or a hung server cannot block another
// caller of this registry.
func (r *Registry) Close() {
	if r == nil {
		return
	}
	r.deliver.Lock()
	defer r.deliver.Unlock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	servers := r.servers
	r.servers = make(map[string]*ServerRecord)
	subs := r.subscribersLocked()
	r.subs = make(map[uint64]subscriber)
	r.mu.Unlock()
	for _, rec := range servers {
		if rec != nil && rec.session != nil {
			_ = rec.session.Close()
		}
	}
	for _, sub := range subs {
		sub.fn(nil)
	}
}

var globalRegistry = newRegistry()

// GlobalRegistry is the process-wide view used by status commands (/mcp and
// the gateway's equivalent), which report on the active primary agent. It is
// deliberately NOT where connections are owned: a Runner registers into its
// own registry and mirrors into this one, so a subagent tearing down its
// servers cannot close a connection the primary agent is still using.
func GlobalRegistry() *Registry {
	return globalRegistry
}

// Apply writes one status transition into this registry. A nil registry owns
// nothing, so it cannot take ownership of a session either: closing it here
// rather than leaking the connection keeps "registered" and "owned" the same
// thing.
func (r *Registry) Apply(update StatusUpdate) {
	if r == nil {
		if update.Session != nil {
			_ = update.Session.Close()
		}
		return
	}
	r.write(update, 0, false)
}

// write applies one update and notifies subscribers when something changed.
// owner is the sequence of the registry that produced the update: a write from
// a lower sequence never replaces a record a higher sequence wrote, so closing
// and recreating a generation cannot be undone by a stale writer that was still
// running.
func (r *Registry) write(update StatusUpdate, owner uint64, mirror bool) {
	if r == nil {
		if update.Session != nil {
			_ = update.Session.Close()
		}
		return
	}
	name := strings.TrimSpace(update.Name)
	if name == "" {
		if update.Session != nil {
			_ = update.Session.Close()
		}
		return
	}
	if owner == 0 {
		owner = r.seq
	}
	update.Name = name

	r.deliver.Lock()
	defer r.deliver.Unlock()
	r.mu.Lock()
	if r.closed {
		// A closed registry owns nothing and has no readers left. Taking the
		// session here would mean storing a connection whose only owner is a
		// registry nobody will close again, so it is closed instead: in this
		// registry "recorded" and "owned" are the same thing, in both directions.
		r.mu.Unlock()
		if update.Session != nil {
			_ = update.Session.Close()
		}
		return
	}
	changed, toClose := r.writeLocked(update, owner, mirror)
	var snapshot []ServerRecord
	var subs []subscriber
	if changed {
		snapshot = r.snapshotLocked()
		subs = r.subscribersLocked()
	}
	r.mu.Unlock()
	// Sessions this write displaced or refused are closed outside both locks: a
	// Close on a stdio session waits on the child process, and holding a
	// registry lock through that would block every other server's status.
	for _, sess := range toClose {
		_ = sess.Close()
	}
	if !changed {
		return
	}
	for _, sub := range subs {
		sub.fn(snapshot)
	}
}

// writeLocked applies one update under r.mu. Returns whether the stored record
// changed, plus the sessions the caller must close: a session displaced by a
// new one, a session belonging to a record this write refused to touch, and a
// connection left behind by a record that is no longer connected.
func (r *Registry) writeLocked(update StatusUpdate, owner uint64, mirror bool) (bool, []*Session) {
	if r.servers == nil {
		r.servers = make(map[string]*ServerRecord)
	}
	existing := r.servers[update.Name]
	if existing != nil && existing.ownerSeq > owner {
		// A newer generation owns this name. Closing the rejected session is
		// what keeps a stale writer's connection from leaking.
		if update.Session != nil {
			return false, []*Session{update.Session}
		}
		return false, nil
	}
	next := ServerRecord{
		Name:       update.Name,
		Transport:  update.Transport,
		URL:        update.URL,
		ConnStatus: update.Status,
		AuthStatus: update.AuthStatus,
		Error:      strings.TrimSpace(update.Error),
		ToolCount:  update.ToolCount,
		Required:   update.Required,
		Generation: strings.TrimSpace(update.Generation),
		ownerSeq:   owner,
		mirror:     mirror,
	}
	var toClose []*Session
	if existing != nil {
		// A transition that does not restate a field keeps what the record
		// already carried, so a status-only move (connecting -> error) cannot
		// blank the transport or drop the requirement flag.
		if next.Transport == "" {
			next.Transport = existing.Transport
		}
		if next.URL == "" {
			next.URL = existing.URL
		}
		if next.AuthStatus == "" || next.AuthStatus == AuthStatusNone {
			next.AuthStatus = existing.AuthStatus
		}
		if next.Generation == "" {
			next.Generation = existing.Generation
		}
		if !next.Required {
			next.Required = existing.Required
		}
		// Only a write that brings a live connection stores one; any other
		// status drops the handle the record held. The one exception is a
		// release, which keeps the handle on a disconnected record on purpose
		// (see ReleaseSessions) — and even that ends here, because the write
		// replacing it belongs to a newer generation that brings its own.
		if next.ConnStatus == ConnStatusConnected && update.Session != nil {
			next.session = update.Session
			if existing.session != nil && existing.session != update.Session {
				toClose = append(toClose, existing.session)
			}
		} else if existing.session != nil {
			toClose = append(toClose, existing.session)
		}
		if existing.ConnStatus == next.ConnStatus && existing.Error == next.Error &&
			existing.ToolCount == next.ToolCount && existing.session == next.session &&
			existing.Transport == next.Transport && existing.URL == next.URL &&
			existing.AuthStatus == next.AuthStatus && existing.Required == next.Required &&
			existing.Generation == next.Generation && existing.mirror == next.mirror {
			return false, toClose
		}
	} else if next.ConnStatus == ConnStatusConnected {
		next.session = update.Session
	} else if update.Session != nil {
		// A first write that is not a connect carries no ownership, and the
		// connection it brought has no record to live in.
		toClose = append(toClose, update.Session)
	}
	if next.ConnStatus == ConnStatusConnected {
		now := time.Now()
		next.ConnectedAt = &now
	}
	r.servers[update.Name] = &next
	return true, toClose
}

// MarkConnecting records that one configured server is being started. It is
// written before the connecting goroutine is spawned, so a snapshot taken at
// any moment counts the server towards the total instead of hiding it.
func (r *Registry) MarkConnecting(update StatusUpdate) {
	update.Status = ConnStatusConnecting
	update.Error = ""
	update.Session = nil
	update.ToolCount = 0
	r.Apply(update)
}

// MarkError records that one server failed to start or to list its tools. The
// message is kept verbatim: the underlying text is what the operator needs, and
// paraphrasing it here would lose the one copy that came from the server.
func (r *Registry) MarkError(update StatusUpdate, err error) {
	update.Status = ConnStatusError
	if err != nil {
		update.Error = err.Error()
	}
	update.Session = nil
	update.ToolCount = 0
	r.Apply(update)
}

// MarkCancelled records that one server's startup was cancelled before it
// finished. No error text is attached: nothing failed.
func (r *Registry) MarkCancelled(update StatusUpdate) {
	update.Status = ConnStatusCancelled
	update.Error = ""
	update.Session = nil
	update.ToolCount = 0
	r.Apply(update)
}

// Mirror publishes this registry's current record for one server into a target
// registry — in practice the process-wide view the status commands read.
//
// It mirrors the record rather than the update, so the target always shows what
// the owning registry holds and no caller can publish a state it did not store.
// The session pointer stays behind because storing it in a shared view would
// mean a Close on that view tore down connections a Runner is still using —
// the exact cross-Runner teardown the per-Runner keying exists to prevent. The
// source registry's sequence travels with the write, so a stale generation
// cannot overwrite a newer owner's record.
func (r *Registry) Mirror(target *Registry, serverName string) {
	if r == nil || target == nil {
		return
	}
	rec, ok := r.StatusOf(serverName)
	if !ok {
		return
	}
	target.write(StatusUpdate{
		Name:       rec.Name,
		Transport:  rec.Transport,
		URL:        rec.URL,
		Status:     rec.ConnStatus,
		AuthStatus: rec.AuthStatus,
		Error:      rec.Error,
		ToolCount:  rec.ToolCount,
		Required:   rec.Required,
		Generation: rec.Generation,
	}, r.seq, true)
}

// Republish mirrors every record this registry holds into the process-wide
// view. The view is shared by every Runner, so another one may have overwritten
// the same server names since these records were written; a reload replays
// cached tool definitions without contacting any server, and this is what keeps
// the shared view describing the generation that is actually serving.
func (r *Registry) Republish() {
	if r == nil {
		return
	}
	for _, rec := range r.ListStatus() {
		r.Mirror(globalRegistry, rec.Name)
	}
}

// ReleaseSessions tears every live connection this registry owns down and
// reports how many it released.
//
// It is the idle half of ownership: a Runner whose sessions nobody has used for
// a while gives back the child processes and long-lived transports they hold,
// without giving up the sessions themselves — the records keep their handles,
// because the tool table the session was built on still names these servers and
// the next call has to reach them. A released record reads as
// ConnStatusDisconnected, which is not one of the startup states: nothing
// failed, nothing is starting, and the connection comes back on the next
// request (see Session.Release and Session.ensureSession).
//
// The teardown happens outside both locks — a stdio close waits out the spec's
// terminate sequence — and subscribers are told after it, so the snapshot they
// receive describes connections that are actually gone rather than going.
func (r *Registry) ReleaseSessions() int {
	if r == nil {
		return 0
	}
	r.deliver.Lock()
	defer r.deliver.Unlock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return 0
	}
	var toRelease []*Session
	for _, rec := range r.servers {
		if rec == nil || rec.session == nil {
			continue
		}
		next := *rec
		next.ConnStatus = ConnStatusDisconnected
		next.ConnectedAt = nil
		r.servers[rec.Name] = &next
		toRelease = append(toRelease, rec.session)
	}
	var snapshot []ServerRecord
	var subs []subscriber
	if len(toRelease) > 0 {
		snapshot = r.snapshotLocked()
		subs = r.subscribersLocked()
	}
	r.mu.Unlock()
	for _, sess := range toRelease {
		_ = sess.Release()
	}
	for _, sub := range subs {
		sub.fn(snapshot)
	}
	return len(toRelease)
}

// Subscribe registers fn for every subsequent status snapshot and immediately
// hands it the current one.
//
// The replay and the registration are one atomic step with respect to writers,
// which is the linearization point that makes the subscription lossless: a
// transition either happened before the call (so it is already in the replayed
// snapshot) or after it (so it is delivered). Snapshots are delivered in the
// order they were taken, and never from a writer that has not finished, because
// delivery is serialized with snapshotting.
//
// fn runs on the calling goroutine and on writer goroutines, so it must not
// block and must not call back into this registry (a write from inside a
// callback would deadlock on the delivery lock). Surfaces enqueue.
func (r *Registry) Subscribe(fn func([]ServerRecord)) func() {
	if r == nil || fn == nil {
		return func() {}
	}
	r.deliver.Lock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		r.deliver.Unlock()
		fn(nil)
		return func() {}
	}
	id := r.nextSub
	r.nextSub++
	if r.subs == nil {
		r.subs = make(map[uint64]subscriber)
	}
	r.subs[id] = subscriber{id: id, fn: fn}
	snapshot := r.snapshotLocked()
	r.mu.Unlock()
	fn(snapshot)
	r.deliver.Unlock()
	return func() {
		// Taking the delivery lock is what makes "cancelled" mean it: a snapshot
		// already being handed out is finished first, so after this returns fn is
		// never called again. A subscriber that unsubscribed from inside its own
		// callback would deadlock here, which is why the contract above says a
		// callback only enqueues.
		r.deliver.Lock()
		r.mu.Lock()
		delete(r.subs, id)
		r.mu.Unlock()
		r.deliver.Unlock()
	}
}

func (r *Registry) subscribersLocked() []subscriber {
	if len(r.subs) == 0 {
		return nil
	}
	out := make([]subscriber, 0, len(r.subs))
	for _, sub := range r.subs {
		out = append(out, sub)
	}
	// Registration order, so a replay is reproducible when more than one
	// subscriber is attached.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].id > out[j].id; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// snapshotLocked copies every record without exposing the session pointer.
// Caller holds r.mu.
func (r *Registry) snapshotLocked() []ServerRecord {
	out := make([]ServerRecord, 0, len(r.servers))
	for _, rec := range r.servers {
		if rec == nil {
			continue
		}
		cp := *rec
		cp.session = nil
		out = append(out, cp)
	}
	return out
}

// ListStatus returns the current snapshot. The order is the map's and is not a
// contract; callers that need configuration order sort by their own list (see
// the Runner's ordered snapshot helper).
func (r *Registry) ListStatus() []ServerRecord {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshotLocked()
}

// StatusOf returns one server's snapshot record.
func (r *Registry) StatusOf(name string) (ServerRecord, bool) {
	if r == nil {
		return ServerRecord{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.servers[strings.TrimSpace(name)]
	if !ok || rec == nil {
		return ServerRecord{}, false
	}
	cp := *rec
	cp.session = nil
	return cp, true
}

// GetSession returns the session one server's record holds. A connected record
// hands back a live connection; a disconnected one hands back the released
// handle, which grows its connection back on the next request — so a caller
// that only needs to reach the server does not have to care which state the
// record is in.
func (r *Registry) GetSession(name string) (*Session, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.servers[strings.TrimSpace(name)]
	if !ok || rec == nil || rec.session == nil {
		return nil, false
	}
	return rec.session, true
}

// ResourceInfos lists the resources one server advertises.
func (r *Registry) ResourceInfos(ctx context.Context, serverName string) ([]ResourceInfo, error) {
	sess, ok := r.GetSession(serverName)
	if !ok {
		return nil, fmt.Errorf("server %q not found or not connected", serverName)
	}
	return sess.ResourceInfos(ctx)
}

// LiveResourceServers names the servers holding a live connection whose
// initialize result declared resources — the ones a resource list can be asked
// of right now without starting anything.
func (r *Registry) LiveResourceServers() map[string]bool {
	out := map[string]bool{}
	if r == nil {
		return out
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for name, rec := range r.servers {
		if rec == nil || rec.session == nil || rec.ConnStatus != ConnStatusConnected {
			continue
		}
		if rec.session.Capabilities().Resources {
			out[name] = true
		}
	}
	return out
}

func (r *Registry) ListResources(ctx context.Context, serverName string) (string, error) {
	sess, ok := r.GetSession(serverName)
	if !ok {
		return "", fmt.Errorf("server %q not found or not connected", serverName)
	}
	return sess.ListResourcesJSON(ctx)
}

func (r *Registry) ReadResource(ctx context.Context, serverName, uri string) (string, error) {
	sess, ok := r.GetSession(serverName)
	if !ok {
		return "", fmt.Errorf("server %q not found or not connected", serverName)
	}
	return sess.ReadResourceJSON(ctx, uri)
}

func (r *Registry) ListTools(ctx context.Context, serverName string) ([]map[string]any, error) {
	sess, ok := r.GetSession(serverName)
	if !ok {
		return nil, fmt.Errorf("server %q not found or not connected", serverName)
	}
	return sess.ListToolMetas(ctx)
}
