package process

import (
	"context"
	"database/sql"
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
	// A real gap between the two attempts: the second replica must be refused
	// for what the row says, not because both calls landed in one millisecond.
	time.Sleep(5 * time.Millisecond)
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

// TestLeaseStandbyTakesOverWhenTheHolderStopsRenewing pins the half of the
// lease that makes a standby worth having: a replica whose first acquire lost
// keeps trying, so it takes the account over once the holder's row ages out —
// and a replica that loses the account must rejoin the retry loop and take it
// back rather than exit for good.
func TestLeaseStandbyTakesOverWhenTheHolderStopsRenewing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstLeases := testLeases(t, "webchat-a")
	secondLeases := testLeases(t, "webchat-b")
	secondLeases.DB = firstLeases.DB
	db := firstLeases.DB

	firstChannel, secondChannel := &stubChannel{}, &stubChannel{}
	first := firstLeases.Guard("channel:acct", firstChannel)
	second := secondLeases.Guard("channel:acct", secondChannel)
	if first == nil || second == nil {
		t.Fatal("a leased handler must be returned")
	}
	if err := first.Start(ctx, nil, nil); err != nil {
		t.Fatalf("the first replica's Start: %v", err)
	}
	if err := second.Start(ctx, nil, nil); err != nil {
		t.Fatalf("the second replica's Start: %v", err)
	}
	waitForLease(t, "the holder to connect", func() bool { return firstChannel.connected() == 1 })
	if got := secondChannel.connected(); got != 0 {
		t.Fatalf("the replica without the lease connected %d times, want none", got)
	}

	// The holder dies without renewing. A renewal only matches the row while it
	// still names this process, so a row left behind by a replica that is gone
	// has to be both moved to that replica and aged: ageing alone leaves it with
	// the live holder, whose next renewal pushes the expiry forward again and
	// keeps the standby out for as long as the holder runs.
	dieLease(t, db, "channel:acct")
	waitForLease(t, "the standby to take the account over", func() bool { return secondChannel.connected() == 1 })

	// Symmetric half: the replica that has just lost the account must rejoin the
	// retry loop and take it back, instead of exiting for good.
	dieLease(t, db, "channel:acct")
	waitForLease(t, "the first replica to take the account back", func() bool { return firstChannel.connected() == 1 })

	if err := first.Stop(context.Background()); err != nil {
		t.Fatalf("stop the first replica's handler: %v", err)
	}
	if err := second.Stop(context.Background()); err != nil {
		t.Fatalf("stop the second replica's handler: %v", err)
	}
	// A stopped replica must not reconnect: the loops end with the handler.
	firstConnections, secondConnections := firstChannel.connections(), secondChannel.connections()
	time.Sleep(50 * time.Millisecond)
	if got := firstChannel.connections(); got != firstConnections {
		t.Fatalf("the stopped first replica reconnected %d times, want none", got-firstConnections)
	}
	if got := secondChannel.connections(); got != secondConnections {
		t.Fatalf("the stopped second replica reconnected %d times, want none", got-secondConnections)
	}
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

// dieLease leaves the account with a replica that is no longer running and
// whose lease has run out, which is what a holder that stopped renewing looks
// like to the replicas that are still up.
func dieLease(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	takeLease(t, db, name, "webchat-gone")
	ageLease(t, db, name)
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
