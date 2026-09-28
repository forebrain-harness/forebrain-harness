package mcp

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// R7's whole reason for existing: two Runners configured with the same server
// name must not share a registry entry, because closing one's connections
// would then tear down the other's.
//
// Before ownership moved per-Runner, this was a real multi-agent hazard rather
// than a theoretical one — the process-wide registry keyed on server name
// alone, so a subagent reloading its config closed the primary agent's live
// MCP sessions.
func TestPerRunnerRegistriesDoNotShareEntries(t *testing.T) {
	primary := NewRegistry()
	subagent := NewRegistry()

	primary.Apply(StatusUpdate{Name: "filesystem", Transport: "stdio", Status: ConnStatusConnected,
		AuthStatus: AuthStatusAuthenticated, ToolCount: 3})
	subagent.Apply(StatusUpdate{Name: "filesystem", Transport: "stdio", Status: ConnStatusConnected,
		AuthStatus: AuthStatusAuthenticated, ToolCount: 5})

	if got := len(primary.ListStatus()); got != 1 {
		t.Fatalf("primary holds %d servers, want 1", got)
	}
	if primary.ListStatus()[0].ToolCount != 3 {
		t.Fatalf("primary's entry = %+v, want its own tool count: the subagent overwrote it",
			primary.ListStatus()[0])
	}

	// Closing the subagent's registry must leave the primary's untouched.
	subagent.Close()
	if got := len(subagent.ListStatus()); got != 0 {
		t.Fatalf("subagent still holds %d servers after Close", got)
	}
	if got := len(primary.ListStatus()); got != 1 {
		t.Fatalf("primary holds %d servers after the subagent closed, want its own 1 — "+
			"closing one Runner's MCP connections tore down another's", got)
	}
}

// Close is idempotent, since Load calls it on every reload including the first.
func TestRegistryCloseIsIdempotent(t *testing.T) {
	r := NewRegistry()
	r.Apply(StatusUpdate{Name: "srv", Transport: "stdio", Status: ConnStatusConnected,
		AuthStatus: AuthStatusAuthenticated, ToolCount: 1})
	r.Close()
	r.Close()
	if got := len(r.ListStatus()); got != 0 {
		t.Fatalf("registry holds %d servers after two Closes", got)
	}
	(*Registry)(nil).Close()
}

// TestConnStatusWireValuesAreFixed pins the four startup states' on-the-wire
// names. They leave this package straight into REST responses and the frontend,
// so a rename is a protocol change, not an internal one.
func TestConnStatusWireValuesAreFixed(t *testing.T) {
	want := map[ConnStatus]string{
		ConnStatusConnecting: "connecting",
		ConnStatusConnected:  "connected",
		ConnStatusError:      "error",
		ConnStatusCancelled:  "cancelled",
	}
	for status, wire := range want {
		if string(status) != wire {
			t.Fatalf("conn status %q serializes as %q, want %q", status, string(status), wire)
		}
	}
	rec := ServerRecord{Name: "srv", ConnStatus: ConnStatusCancelled, AuthStatus: AuthStatusNone}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"conn_status":"cancelled"`) {
		t.Fatalf("cancelled record serializes as %s", raw)
	}
	// A skipped server is settled, but it is not an error: nothing failed, and
	// no error text exists for it.
	if !rec.Settled() {
		t.Fatal("a cancelled server is in a terminal state")
	}
	if rec.Error != "" {
		t.Fatalf("cancelled record carries error text: %q", rec.Error)
	}
}

// TestRegistrySubscribeReplaysThenStreams pins the linearization contract a
// surface depends on: the snapshot handed over at subscription time plus every
// later change is exactly the history, with no state lost in the gap.
func TestRegistrySubscribeReplaysThenStreams(t *testing.T) {
	reg := NewRegistry()
	reg.MarkConnecting(StatusUpdate{Name: "early", Transport: "stdio"})

	var mu sync.Mutex
	var seen [][]ServerRecord
	cancel := reg.Subscribe(func(snapshot []ServerRecord) {
		mu.Lock()
		seen = append(seen, snapshot)
		mu.Unlock()
	})

	reg.MarkError(StatusUpdate{Name: "early", Transport: "stdio"}, errors.New("boom"))
	reg.Apply(StatusUpdate{Name: "late", Transport: "stdio", Status: ConnStatusConnected,
		AuthStatus: AuthStatusAuthenticated, ToolCount: 2})

	mu.Lock()
	got := append([][]ServerRecord(nil), seen...)
	mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("subscription deliveries = %d, want replay + two changes", len(got))
	}
	if rec, ok := recordByName(got[0], "early"); !ok || rec.ConnStatus != ConnStatusConnecting {
		t.Fatalf("replayed snapshot missing the connecting state: %+v", got[0])
	}
	if rec, ok := recordByName(got[1], "early"); !ok || rec.ConnStatus != ConnStatusError || rec.Error != "boom" {
		t.Fatalf("first change = %+v, want the error with its text", got[1])
	}
	if _, ok := recordByName(got[2], "late"); !ok {
		t.Fatalf("second change missing the new server: %+v", got[2])
	}

	// Unsubscribing stops delivery, and the snapshot never carries a session.
	cancel()
	reg.Apply(StatusUpdate{Name: "after-cancel", Transport: "stdio", Status: ConnStatusConnected,
		AuthStatus: AuthStatusAuthenticated, ToolCount: 1})
	mu.Lock()
	afterCancel := len(seen)
	mu.Unlock()
	if afterCancel != 3 {
		t.Fatalf("a cancelled subscription received %d deliveries", afterCancel-3)
	}
	for _, snapshot := range got {
		for _, rec := range snapshot {
			if rec.session != nil {
				t.Fatal("a delivered snapshot exposed a live session pointer")
			}
		}
	}
}

// TestRegistryCloseDeliversAFinalEmptySnapshot pins the end of a generation for
// subscribers: the servers are gone with the registry, and leaving a subscriber
// on its last "connecting" snapshot would show a startup that can never finish.
func TestRegistryCloseDeliversAFinalEmptySnapshot(t *testing.T) {
	reg := NewRegistry()
	reg.MarkConnecting(StatusUpdate{Name: "srv", Transport: "stdio"})
	var mu sync.Mutex
	var last []ServerRecord
	delivered := 0
	reg.Subscribe(func(snapshot []ServerRecord) {
		mu.Lock()
		last = snapshot
		delivered++
		mu.Unlock()
	})
	reg.Close()
	mu.Lock()
	gotLast, gotDelivered := last, delivered
	mu.Unlock()
	if gotDelivered != 2 || gotLast != nil {
		t.Fatalf("Close delivered %d snapshots, last=%+v want a single empty one", gotDelivered, gotLast)
	}
}

// TestMirrorNeverLetsAnOlderGenerationOverwriteANewerOne pins the ownership rule
// for the process-wide view: it is shared by every Runner, so a stale generation
// that is still finishing its teardown must not overwrite the record of the
// generation that replaced it.
func TestMirrorNeverLetsAnOlderGenerationOverwriteANewerOne(t *testing.T) {
	mirror := NewRegistry()
	old := NewRegistry()
	fresh := NewRegistry()

	old.Apply(StatusUpdate{Name: "shared", Transport: "stdio", Status: ConnStatusConnected,
		AuthStatus: AuthStatusAuthenticated, ToolCount: 1, Session: &Session{}})
	old.Mirror(mirror, "shared")
	rec, ok := mirror.StatusOf("shared")
	if !ok || rec.ConnStatus != ConnStatusConnected {
		t.Fatalf("mirror after the first generation: %+v ok=%v", rec, ok)
	}
	if _, ok := mirror.GetSession("shared"); ok {
		t.Fatal("a mirrored record must never hold a session")
	}

	fresh.MarkError(StatusUpdate{Name: "shared", Transport: "stdio"}, errors.New("fresh failure"))
	fresh.Mirror(mirror, "shared")
	rec, _ = mirror.StatusOf("shared")
	if rec.ConnStatus != ConnStatusError || rec.Error != "fresh failure" {
		t.Fatalf("the newer generation must win the mirror: %+v", rec)
	}

	// The superseded generation finishes its teardown after the new one has
	// taken over; its writes must be ignored.
	old.MarkCancelled(StatusUpdate{Name: "shared", Transport: "stdio"})
	old.Mirror(mirror, "shared")
	rec, _ = mirror.StatusOf("shared")
	if rec.ConnStatus != ConnStatusError || rec.Error != "fresh failure" {
		t.Fatalf("a stale generation overwrote the newer record: %+v", rec)
	}
}

// TestRegistryConcurrentMarkSubscribeCancel exercises the registry's locks the
// way a generation does: several servers reporting while surfaces subscribe and
// unsubscribe. Run under -race.
func TestRegistryConcurrentMarkSubscribeCancel(t *testing.T) {
	reg := NewRegistry()
	// The two halves get their own WaitGroup on purpose. A subscriber goroutine
	// only stops when its stop channel closes, and that happens once the writers
	// are done — so waiting on one group for both would wait for a stop signal
	// that only arrives after the wait returns.
	var writers, subscribers sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		server := "srv-" + strconv.Itoa(i)
		writers.Add(1)
		go func() {
			defer writers.Done()
			reg.MarkConnecting(StatusUpdate{Name: server, Transport: "stdio"})
			for j := 0; j < 25; j++ {
				reg.MarkError(StatusUpdate{Name: server, Transport: "stdio"}, errors.New("retry"))
			}
			reg.Apply(StatusUpdate{Name: server, Transport: "stdio", Status: ConnStatusConnected,
				AuthStatus: AuthStatusAuthenticated, ToolCount: 1})
		}()
	}
	for i := 0; i < 4; i++ {
		subscribers.Add(1)
		go func() {
			defer subscribers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				cancel := reg.Subscribe(func([]ServerRecord) {})
				cancel()
			}
		}()
	}
	writers.Wait()
	close(stop)
	subscribers.Wait()
	if got := len(reg.ListStatus()); got != 4 {
		t.Fatalf("registry holds %d servers, want 4", got)
	}
}

func recordByName(records []ServerRecord, name string) (ServerRecord, bool) {
	for _, rec := range records {
		if rec.Name == name {
			return rec, true
		}
	}
	return ServerRecord{}, false
}

// TestMirroringTakesNoSessionOwnership pins the invariant the per-Runner
// keying exists for: the process-wide registry is a status view, so closing it
// must never tear down a connection a Runner still owns.
//
// This used to rest on a comment ("it owns no lifetime") while the mirror
// still held the session pointer, which meant any Close on the global registry
// would have closed another Runner's connections.
func TestMirroringTakesNoSessionOwnership(t *testing.T) {
	owner := NewRegistry()
	mirror := NewRegistry()

	sess := &Session{}
	owner.Apply(StatusUpdate{Name: "docs", Transport: "stdio", Status: ConnStatusConnected,
		AuthStatus: AuthStatusAuthenticated, ToolCount: 3, Session: sess})
	owner.Mirror(mirror, "docs")

	// The mirror reports the server ...
	if got := len(mirror.ListStatus()); got != 1 {
		t.Fatalf("mirror ListStatus = %d entries, want 1", got)
	}
	// ... but holds no session for it, so it cannot close one.
	if _, ok := mirror.GetSession("docs"); ok {
		t.Fatal("mirror must not expose a session it does not own")
	}
	// The owner still does.
	if _, ok := owner.GetSession("docs"); !ok {
		t.Fatal("owner lost its session")
	}

	// Closing the mirror empties it without touching the owner's session.
	mirror.Close()
	if got := len(mirror.ListStatus()); got != 0 {
		t.Fatalf("mirror after Close = %d entries, want 0", got)
	}
	if _, ok := owner.GetSession("docs"); !ok {
		t.Fatal("closing the mirror removed the owner's session")
	}
}

// TestUnsubscribeIsFinalEvenAgainstAWriterInFlight pins what cancelling a
// subscription promises.
//
// A snapshot is taken and delivered under one lock, so a writer can be inside a
// delivery when another goroutine cancels. If the cancel did not wait for that
// delivery, a subscriber could be called after it had torn down what the
// callback writes into — and the callback's session id would already belong to
// a conversation that had moved on.
func TestUnsubscribeIsFinalEvenAgainstAWriterInFlight(t *testing.T) {
	reg := NewRegistry()
	var (
		mu         sync.Mutex
		deliveries int
		cancelled  bool
		after      int
	)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	cancel := reg.Subscribe(func([]ServerRecord) {
		mu.Lock()
		deliveries++
		n := deliveries
		if cancelled {
			after++
		}
		mu.Unlock()
		if n == 1 {
			// The replay, delivered on the subscribing goroutine. Blocking here
			// would block Subscribe itself.
			return
		}
		if n == 2 {
			entered <- struct{}{}
			<-release
		}
	})

	// A writer is inside the callback. The cancel must not return while that
	// delivery is still running: a subscriber that has been told it is
	// unsubscribed, and is already tearing down what its callback writes into,
	// would otherwise still be inside one.
	go reg.MarkConnecting(StatusUpdate{Name: "docs", Generation: "gen-1"})
	<-entered
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		cancel()
		mu.Lock()
		cancelled = true
		mu.Unlock()
	}()
	select {
	case <-returned:
		t.Fatal("cancel returned while a delivery to the subscriber was still in flight")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-returned

	// Anything the registry does now must reach nobody.
	for i := 0; i < 20; i++ {
		reg.MarkError(StatusUpdate{Name: "docs", Generation: "gen-1"}, errors.New("boom"))
		reg.MarkConnecting(StatusUpdate{Name: "docs", Generation: "gen-1"})
	}
	mu.Lock()
	defer mu.Unlock()
	if after != 0 {
		t.Fatalf("a cancelled subscriber was called %d times after cancel returned", after)
	}
}

// TestAClosedRegistryTakesNoFurtherOwnership pins that "recorded" and "owned"
// stay the same thing in both directions: a write that arrives after the
// registry closed — a generation settling into one that was just torn down —
// must not park a live connection in a registry nobody will close again.
func TestAClosedRegistryTakesNoFurtherOwnership(t *testing.T) {
	reg := NewRegistry()
	reg.Close()
	reg.Apply(StatusUpdate{Name: "docs", Status: ConnStatusConnected, Generation: "gen-1"})
	if records := reg.ListStatus(); len(records) != 0 {
		t.Fatalf("a closed registry took a write: %+v", records)
	}
	if _, ok := reg.StatusOf("docs"); ok {
		t.Fatal("a closed registry answered for a server it was told about after closing")
	}
}
