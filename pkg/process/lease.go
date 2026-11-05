// Background-singleton leases (plan 213 §3.4, design §2.7.5): the compare-and-
// swap row that keeps one replica of a shared database connected to a long-
// connection chat account. The channel package decides which kinds need one and
// names the account; this file owns the row and the lifecycle around it.
package process

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/channel"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

const (
	// LeaseTTL is how long a held lease stays valid without a renewal. It is
	// twice the renewal interval, so one missed renewal — a database hiccup,
	// not a dead process — does not hand the account to another replica.
	LeaseTTL = 20 * time.Second
	// LeaseRenewEvery is how often the holder extends its lease while the
	// channel is connected.
	LeaseRenewEvery = 10 * time.Second
	// LeaseRetryEvery is how often a replica that did not get the lease tries
	// again, which is what takes the account over when its holder goes away.
	LeaseRetryEvery = 15 * time.Second
)

var (
	// leaseAcquireSQL is the compare-and-swap: the row is written only when
	// there is no holder or the holder's lease has expired, and the caller
	// learns from RowsAffected whether it won. The comparison every arm makes
	// is against the instant of the attempt, never against the expiry being
	// written: MySQL's two IFs take that instant as a parameter of their own,
	// where VALUES(expires_at_ms) would compare against a value that is always
	// in the future and hand every standby a live holder's account. The
	// assignment order is part of the statement — MySQL runs the SET list left
	// to right, so holder is assigned before expires_at_ms and the second IF
	// still reads the old expiry.
	leaseAcquireSQL = dbx.SQL{
		SQLite: `
INSERT INTO fb_leases(name, holder, expires_at_ms) VALUES(?,?,?)
ON CONFLICT(name) DO UPDATE SET holder=excluded.holder, expires_at_ms=excluded.expires_at_ms
WHERE fb_leases.expires_at_ms < ?`,
		MySQL: `
INSERT INTO fb_leases(name, holder, expires_at_ms) VALUES(?,?,?)
ON DUPLICATE KEY UPDATE
  holder=IF(fb_leases.expires_at_ms < ?, VALUES(holder), fb_leases.holder),
  expires_at_ms=IF(fb_leases.expires_at_ms < ?, VALUES(expires_at_ms), fb_leases.expires_at_ms)`,
		Postgres: `
INSERT INTO fb_leases(name, holder, expires_at_ms) VALUES($1,$2,$3)
ON CONFLICT(name) DO UPDATE SET holder=excluded.holder, expires_at_ms=excluded.expires_at_ms
WHERE fb_leases.expires_at_ms < $4`,
	}
	leaseRenewSQL = dbx.SQL{
		SQLite:   `UPDATE fb_leases SET expires_at_ms=? WHERE name=? AND holder=?`,
		MySQL:    `UPDATE fb_leases SET expires_at_ms=? WHERE name=? AND holder=?`,
		Postgres: `UPDATE fb_leases SET expires_at_ms=$1 WHERE name=$2 AND holder=$3`,
	}
	leaseReleaseSQL = dbx.SQL{
		SQLite:   `DELETE FROM fb_leases WHERE name=? AND holder=?`,
		MySQL:    `DELETE FROM fb_leases WHERE name=? AND holder=?`,
		Postgres: `DELETE FROM fb_leases WHERE name=$1 AND holder=$2`,
	}
)

// Leases holds the fb_leases rows for one process. Holder is the same owner id
// the run lease uses, so a row names the replica that holds it and disappears
// with the process that wrote it.
type Leases struct {
	DB     *sql.DB
	Holder string
}

// Guard wraps a long-connection channel handler so the delegate runs only
// while this process holds the account's lease. A nil receiver, an empty name
// (a kind that keeps no connection, or a channel with no credential yet) or a
// nil handler returns the handler untouched.
func (l *Leases) Guard(name string, h channel.Handler) channel.Handler {
	if l == nil || l.DB == nil || h == nil || strings.TrimSpace(name) == "" {
		return h
	}
	return &leasedHandler{leases: l, name: strings.TrimSpace(name), handler: h}
}

// acquire takes the lease when it is free or expired and reports whether this
// caller won it.
func (l *Leases) acquire(ctx context.Context, name string) (bool, error) {
	now := time.Now().UnixMilli()
	expires := now + LeaseTTL.Milliseconds()
	// The MySQL arm binds the cutoff itself twice, so it takes two arguments
	// more than the SQLite and PostgreSQL arms, which compare against the one
	// trailing placeholder.
	dialect := dbx.DialectOf(l.DB)
	args := []any{name, l.Holder, expires, now}
	if dialect == dbx.MySQL {
		args = append(args, now)
	}
	res, err := dbx.Exec(ctx, l.DB, leaseAcquireSQL.For(dialect), args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	// "Changed a row" is the same event as "won the lease": the SET clause only
	// fires when the bound instant beats the row's expiry. MySQL reports 1 for
	// the insert and 2 for the update it performed, so a `== 1` test would
	// never let a replica win a lease that already had a row; SQLite and
	// PostgreSQL report 1 for both.
	return n > 0, nil
}

// renew extends a lease this process holds, and reports whether it still does:
// a row that another replica took over, or one that expired, matches nothing.
func (l *Leases) renew(ctx context.Context, name string) (bool, error) {
	res, err := dbx.Exec(ctx, l.DB, leaseRenewSQL.For(dbx.DialectOf(l.DB)),
		time.Now().UnixMilli()+LeaseTTL.Milliseconds(), name, l.Holder)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// release gives the lease back. It is only ever called by the holder, so the
// row's holder column is part of the condition: a lease another replica has
// already taken over is left alone.
func (l *Leases) release(ctx context.Context, name string) error {
	_, err := dbx.Exec(ctx, l.DB, leaseReleaseSQL.For(dbx.DialectOf(l.DB)), name, l.Holder)
	return err
}

// leasedHandler is the channel.Handler a long-connection adapter is replaced
// by. It starts the delegate when — and only when — this process holds the
// lease, renews while it does, and disconnects the moment a renewal says it
// does not. Mounting the delegate's routes is the one thing it does not gate:
// every replica serves the route (see MountRoutes).
type leasedHandler struct {
	leases  *Leases
	name    string
	handler channel.Handler

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	// won records that this process has held the account at some point, which is
	// what Stop needs to know before it gives the row back.
	won bool
}

func (s *leasedHandler) ID() string { return s.handler.ID() }

// MountRoutes forwards the capability to the delegate. The registry sees this
// wrapper rather than the adapter, so without the delegation a lease-gated kind
// that mounts routes would mount none on any replica, holder included.
func (s *leasedHandler) MountRoutes(add channel.RouteAdder, bus channel.Bus) {
	if m, ok := s.handler.(channel.RouteMounter); ok {
		m.MountRoutes(add, bus)
	}
}

func (s *leasedHandler) Start(ctx context.Context, add channel.RouteAdder, bus channel.Bus) error {
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.mu.Lock()
	s.cancel, s.done = cancel, done
	s.mu.Unlock()

	won, err := s.leases.acquire(runCtx, s.name)
	if err != nil {
		cancel()
		return fmt.Errorf("channel %s: acquire the account lease: %w", s.handler.ID(), err)
	}
	// The delegate runs on a context of its own. Losing the lease cancels the
	// channel's loop and nothing else: the retry loop below has to survive it,
	// or a displaced holder never takes the account back.
	var delegateCancel context.CancelFunc
	if won {
		s.noteWon()
		delegateCtx, cancelDelegate := context.WithCancel(runCtx)
		// The delegate's Start stays synchronous so a misconfigured channel is
		// reported to the bind exactly as it was before the lease existed.
		if err := s.handler.Start(delegateCtx, add, bus); err != nil {
			cancelDelegate()
			cancel()
			_ = s.leases.release(context.WithoutCancel(runCtx), s.name)
			close(done)
			return err
		}
		delegateCancel = cancelDelegate
	}
	// A replica that lost this first acquire hands hold a nil delegate: it must
	// go straight to the retry loop rather than judge a lease it never took.
	go s.hold(runCtx, add, bus, done, delegateCancel)
	return nil
}

// noteWon records that this process holds the account, so Stop knows it has a
// row to give back.
func (s *leasedHandler) noteWon() {
	s.mu.Lock()
	s.won = true
	s.mu.Unlock()
}

// hold keeps the delegate connected for as long as this process holds the
// lease. delegateCancel is nil until the delegate has been started, so a
// replica that never won the lease never stops a handler it never started, and
// a replica whose delegate has been disconnected waits for the account again
// instead of leaving.
func (s *leasedHandler) hold(ctx context.Context, add channel.RouteAdder, bus channel.Bus, done chan struct{}, delegateCancel context.CancelFunc) {
	defer close(done)
	for {
		if delegateCancel != nil {
			// This replica holds the account while its delegate runs, so
			// renewHeld blocks here: it returns only once a renewal says the
			// lease is gone, or when the handler is stopping. Either way the
			// connection goes at once — a connection that is no longer this
			// replica's to hold — and the replica turns standby rather than
			// exiting.
			_ = s.renewHeld(ctx)
			_ = s.handler.Stop(context.WithoutCancel(ctx))
			delegateCancel()
			delegateCancel = nil
			if ctx.Err() != nil {
				return
			}
			continue
		}
		// A standby retries at the lease's own cadence. That retry is what takes
		// the account over when its holder goes away.
		if !sleep(ctx, LeaseRetryEvery) {
			return
		}
		won, err := s.leases.acquire(ctx, s.name)
		if err != nil {
			slog.Error("channel: acquire the account lease", "channel", s.handler.ID(), "err", err)
			continue
		}
		if !won {
			continue
		}
		s.noteWon()
		runCtx, cancel := context.WithCancel(ctx)
		if err := s.handler.Start(runCtx, add, bus); err != nil {
			cancel()
			_ = s.leases.release(context.WithoutCancel(ctx), s.name)
			// A channel that cannot start must not keep its account's lease away
			// from a replica that could run it.
			slog.Error("channel: start", "channel", s.handler.ID(), "err", err)
			return
		}
		delegateCancel = cancel
	}
}

// renewHeld renews once per interval and reports whether the lease is still
// this process's. A renewal that says otherwise — or fails — ends the hold.
func (s *leasedHandler) renewHeld(ctx context.Context) bool {
	ticker := time.NewTicker(LeaseRenewEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			held, err := s.leases.renew(ctx, s.name)
			if err == nil && held {
				continue
			}
			if err != nil {
				slog.Error("channel: renew the account lease", "channel", s.handler.ID(), "err", err)
			}
			return false
		}
	}
}

func (s *leasedHandler) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel, done, won := s.cancel, s.done, s.won
	s.won = false
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
		}
	}
	if won {
		// A holder that stops gives the account back instead of leaving its row
		// behind: every standby would otherwise wait out the whole TTL for an
		// account this process has already given up.
		if err := s.leases.release(context.WithoutCancel(ctx), s.name); err != nil {
			slog.Error("channel: release the account lease", "channel", s.handler.ID(), "err", err)
		}
	}
	return s.handler.Stop(ctx)
}

// sleep waits for the duration or until the context is done, and reports
// whether the wait completed.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
