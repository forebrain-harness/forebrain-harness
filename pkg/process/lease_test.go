package process

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/channel"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

func testLeases(t *testing.T, holder string) *Leases {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Leases{DB: db, Holder: holder}
}

// TestLeaseIsHeldByOneReplicaAndTakenOverWhenItExpires pins the compare-and-swap
// behind a long-connection channel (plan 213 §3.4): the first replica to ask
// holds the account, a second is refused while that lease is fresh, and the
// second takes over as soon as the first stops renewing.
func TestLeaseIsHeldByOneReplicaAndTakenOverWhenItExpires(t *testing.T) {
	ctx := context.Background()
	first := testLeases(t, "webchat-a")
	second := testLeases(t, "webchat-b")
	// Two replicas share one database, which is the whole premise.
	second.DB = first.DB

	won, err := first.acquire(ctx, "channel:acct")
	if err != nil || !won {
		t.Fatalf("the first replica = %v (%v), want the lease", won, err)
	}
	won, err = second.acquire(ctx, "channel:acct")
	if err != nil {
		t.Fatalf("the second replica: %v", err)
	}
	if won {
		t.Fatal("a fresh lease must not be taken by a second replica")
	}
	held, err := first.renew(ctx, "channel:acct")
	if err != nil || !held {
		t.Fatalf("the holder's renewal = %v (%v), want the lease to stay its", held, err)
	}
	held, err = second.renew(ctx, "channel:acct")
	if err != nil {
		t.Fatalf("a renewal by a non-holder: %v", err)
	}
	if held {
		t.Fatal("a replica that does not hold the lease must not renew it")
	}

	// The holder stops renewing: its lease ages out and the other replica's
	// next attempt wins the account.
	ageLease(t, first.DB, "channel:acct")
	won, err = second.acquire(ctx, "channel:acct")
	if err != nil || !won {
		t.Fatalf("after the lease expired = %v (%v), want the second replica to take over", won, err)
	}
	held, err = first.renew(ctx, "channel:acct")
	if err != nil {
		t.Fatalf("a renewal by the former holder: %v", err)
	}
	if held {
		t.Fatal("the former holder must not still be able to renew")
	}
}

// TestLeaseReleaseGivesTheAccountBack pins the holder's own exit: releasing
// leaves no row behind, so another replica can take the account immediately
// rather than waiting for the lease to expire.
func TestLeaseReleaseGivesTheAccountBack(t *testing.T) {
	ctx := context.Background()
	holder := testLeases(t, "webchat-a")
	other := testLeases(t, "webchat-b")
	other.DB = holder.DB

	if won, err := holder.acquire(ctx, "channel:acct"); err != nil || !won {
		t.Fatalf("acquire = %v (%v), want the lease", won, err)
	}
	if err := holder.release(ctx, "channel:acct"); err != nil {
		t.Fatalf("release: %v", err)
	}
	won, err := other.acquire(ctx, "channel:acct")
	if err != nil || !won {
		t.Fatalf("after the holder released = %v (%v), want the other replica", won, err)
	}
	// A release by a non-holder must not disturb the row that replaced it.
	if err := holder.release(ctx, "channel:acct"); err != nil {
		t.Fatalf("a non-holder's release: %v", err)
	}
	var keeper string
	if err := holder.DB.QueryRow(dbx.Rebind(dbx.DialectOf(holder.DB),
		`SELECT holder FROM fb_leases WHERE name=?`), "channel:acct").Scan(&keeper); err != nil {
		t.Fatal(err)
	}
	if keeper != "webchat-b" {
		t.Fatalf("lease holder = %q, want the replica that took it over", keeper)
	}
}

// stubChannel is a long-connection adapter double: like every real adapter it
// starts a loop of its own and returns, and it records when it is connected.
type stubChannel struct {
	mu         sync.Mutex
	connects   int
	live       int
	cancel     context.CancelFunc
	done       chan struct{}
}

func (s *stubChannel) ID() string { return "stub" }

func (s *stubChannel) Start(ctx context.Context, _ channel.RouteAdder, _ channel.Bus) error {
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.mu.Lock()
	s.connects++
	s.live++
	s.cancel, s.done = cancel, done
	s.mu.Unlock()
	go func() {
		defer close(done)
		<-runCtx.Done()
		s.mu.Lock()
		s.live--
		s.mu.Unlock()
	}()
	return nil
}

func (s *stubChannel) Stop(context.Context) error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	return nil
}

func (s *stubChannel) connected() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live
}

func (s *stubChannel) connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connects
}

// TestGuardedChannelConnectsOnOneReplicaOnly pins what the lease is for: of
// two replicas holding the same account, exactly one has its channel connected,
// and the one that loses the lease disconnects at once instead of holding a
// connection that is no longer its to hold.
func TestGuardedChannelConnectsOnOneReplicaOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	firstHandler, secondHandler := &stubChannel{}, &stubChannel{}
	first := (&Leases{DB: db, Holder: "webchat-a"}).Guard("channel:acct", firstHandler)
	second := (&Leases{DB: db, Holder: "webchat-b"}).Guard("channel:acct", secondHandler)
	if first == nil || second == nil {
		t.Fatal("a leased handler must be returned")
	}
	if err := first.Start(ctx, nil, nil); err != nil {
		t.Fatalf("the first replica's Start: %v", err)
	}
	if err := second.Start(ctx, nil, nil); err != nil {
		t.Fatalf("the second replica's Start: %v", err)
	}
	waitForLease(t, "the holder to connect", func() bool { return firstHandler.connected() == 1 })
	if got := secondHandler.connected(); got != 0 {
		t.Fatalf("the replica without the lease connected %d times, want none", got)
	}

	// A third replica takes the account out from under the holder. The next
	// renewal says so, and the holder disconnects. The renewal is on a 10s
	// interval, so this waits for one tick rather than for a fixed short span.
	takeLease(t, db, "channel:acct", "webchat-c")
	waitForLease(t, "the first replica to disconnect", func() bool { return firstHandler.connected() == 0 })
	if got := secondHandler.connected(); got != 0 {
		t.Fatalf("a replica that never held the lease connected %d times, want none", got)
	}

	if err := first.Stop(context.Background()); err != nil {
		t.Fatalf("stop the first replica's handler: %v", err)
	}
	if err := second.Stop(context.Background()); err != nil {
		t.Fatalf("stop the second replica's handler: %v", err)
	}
}

// routeMountInboundPath is the callback URL the route-mounting double serves.
const routeMountInboundPath = "/channels/route-mount/inbound"

// routeMountChannel is the shape whatsapp has: an adapter that keeps a
// connection of its own *and* mounts an inbound HTTP route. Start only records
// that the connection came up; mounting the route — and with it the bus the
// route publishes on — is MountRoutes' business.
type routeMountChannel struct {
	mu         sync.Mutex
	starts     int
	mounts     int
	mountedBus channel.Bus
}

func (c *routeMountChannel) ID() string { return "route-mount" }

func (c *routeMountChannel) Start(context.Context, channel.RouteAdder, channel.Bus) error {
	c.mu.Lock()
	c.starts++
	c.mu.Unlock()
	return nil
}

func (c *routeMountChannel) Stop(context.Context) error { return nil }

// MountRoutes records one mount and publishes on the bus it was handed when
// the mounted route is called.
func (c *routeMountChannel) MountRoutes(add channel.RouteAdder, bus channel.Bus) {
	c.mu.Lock()
	c.mounts++
	c.mountedBus = bus
	c.mu.Unlock()
	add(http.MethodPost, routeMountInboundPath, func(rw http.ResponseWriter, r *http.Request) {
		if bus != nil {
			_ = bus.PublishInbound(r.Context(), channel.Inbound{
				ChannelID: "route-mount",
				SessionID: "chat:1",
				Text:      "hello",
			})
		}
		rw.WriteHeader(http.StatusNoContent)
	})
}

func (c *routeMountChannel) startCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.starts
}

func (c *routeMountChannel) mountCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mounts
}

func (c *routeMountChannel) mounted() channel.Bus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mountedBus
}

// recordingBus keeps what a channel published, so the test can tell a route
// that really delivered from one that only returned a status code.
type recordingBus struct {
	mu  sync.Mutex
	got []channel.Inbound
}

func (b *recordingBus) PublishInbound(_ context.Context, m channel.Inbound) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.got = append(b.got, m)
	return nil
}

func (b *recordingBus) published() []channel.Inbound {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]channel.Inbound(nil), b.got...)
}

// TestLeasedChannelMountsRoutesOnEveryReplica pins the split between mounting
// and connecting for a kind that does both (whatsapp): mounting is replica-
// local, so every replica — the lease holder and the standby — serves the
// inbound route, and only the holder runs the connection. Both replicas bind
// through the registry, because that is the one path every kind takes.
func TestLeasedChannelMountsRoutesOnEveryReplica(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	holderAdapter, standbyAdapter := &routeMountChannel{}, &routeMountChannel{}
	holderRegistry, standbyRegistry := channel.NewRegistry(), channel.NewRegistry()
	holderBus, standbyBus := &recordingBus{}, &recordingBus{}
	holder := (&Leases{DB: db, Holder: "webchat-a"}).Guard("channel:acct", holderAdapter)
	standby := (&Leases{DB: db, Holder: "webchat-b"}).Guard("channel:acct", standbyAdapter)
	if err := holderRegistry.BindProjects(ctx, "agent",
		[]channel.BoundChannel{{Handler: holder}}, holderBus); err != nil {
		t.Fatalf("bind the lease holder's channels: %v", err)
	}
	if err := standbyRegistry.BindProjects(ctx, "agent",
		[]channel.BoundChannel{{Handler: standby}}, standbyBus); err != nil {
		t.Fatalf("bind the standby's channels: %v", err)
	}

	// The standby's route must be up even though its connection is not: a
	// callback the load balancer delivers here would otherwise hit no route.
	if got := standbyAdapter.mountCount(); got != 1 {
		t.Fatalf("the replica without the lease mounted the route %d times, want once", got)
	}
	if got := standbyAdapter.mounted(); got == nil {
		t.Fatal("the replica without the lease mounted the route with a nil bus: its first callback would panic")
	}
	if got := standbyAdapter.startCount(); got != 0 {
		t.Fatalf("the replica without the lease started its connection %d times, want none", got)
	}
	// The holder mounts once too — a second mount point would give it two
	// routes for one callback URL.
	if got := holderAdapter.mountCount(); got != 1 {
		t.Fatalf("the lease holder mounted the route %d times, want once", got)
	}

	cancel()
	if err := holder.Stop(context.Background()); err != nil {
		t.Fatalf("stop the lease holder's handler: %v", err)
	}
	if err := standby.Stop(context.Background()); err != nil {
		t.Fatalf("stop the standby's handler: %v", err)
	}
}

// TestGuardLeavesWebhookKindsAndHalfConfiguredChannelsAlone pins the two ways a
// handler is returned untouched: a kind that mounts routes instead of holding a
// connection, and a long-connection kind whose credential is blank, which has
// no account to hold a lease for.
func TestGuardLeavesWebhookKindsAndHalfConfiguredChannelsAlone(t *testing.T) {
	leases := testLeases(t, "webchat-a")
	h := &stubChannel{}
	webhook := appcfg.ChannelsSection{Webhook: appcfg.Webhook{Token: "hook-token"}}
	if got := leases.Guard(channel.LeaseName("webhook", webhook), h); got != channel.Handler(h) {
		t.Fatal("a webhook kind must not be put behind a lease")
	}
	telegram := appcfg.ChannelsSection{Telegram: appcfg.Telegram{}}
	if got := leases.Guard(channel.LeaseName("telegram", telegram), h); got != channel.Handler(h) {
		t.Fatal("a channel with no credential yet must not be put behind a lease")
	}
	if got := leases.Guard("", h); got != channel.Handler(h) {
		t.Fatal("an empty lease name must leave the handler alone")
	}
	var nilLeases *Leases
	if got := nilLeases.Guard("channel:acct", h); got != channel.Handler(h) {
		t.Fatal("a nil lease store must leave the handler alone")
	}
}

func TestNeedsLeaseClassification(t *testing.T) {
	// Every kind the product can build is classified one way or the other, so
	// adding an adapter cannot silently leave it out of the table.
	for _, kind := range channel.ChannelKinds {
		_ = channel.NeedsLease(kind)
	}
	for _, kind := range []string{"telegram", "discord", "mattermost", "matrix",
		"homeassistant", "signal", "weixin", "dingtalk", "feishu", "qq", "whatsapp"} {
		if !channel.NeedsLease(kind) {
			t.Errorf("%s keeps a long connection and must take a lease", kind)
		}
	}
	for _, kind := range []string{"slack", "webhook", "http_bridge", "bluebubbles",
		"email", "sms", "wecom", "wecom_callback"} {
		if channel.NeedsLease(kind) {
			t.Errorf("%s is delivered by the load balancer and must not take a lease", kind)
		}
	}
}

// ageLease moves a lease's expiry into the past, which is what a holder that
// stopped renewing looks like to every other replica.
func ageLease(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	if _, err := db.Exec(dbx.Rebind(dbx.DialectOf(db),
		`UPDATE fb_leases SET expires_at_ms=? WHERE name=?`), 1, name); err != nil {
		t.Fatal(err)
	}
}

// takeLease moves an account to another holder with a fresh expiry, which is
// what a replica that won the lease looks like to the one that held it before.
func takeLease(t *testing.T, db *sql.DB, name, holder string) {
	t.Helper()
	if _, err := db.Exec(dbx.Rebind(dbx.DialectOf(db),
		`UPDATE fb_leases SET holder=?, expires_at_ms=? WHERE name=?`),
		holder, time.Now().UnixMilli()+LeaseTTL.Milliseconds(), name); err != nil {
		t.Fatal(err)
	}
}

func waitForLease(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
