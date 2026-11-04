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
	// learns from RowsAffected whether it won.
	leaseAcquireSQL = dbx.SQL{
		SQLite: `
INSERT INTO fb_leases(name, holder, expires_at_ms) VALUES(?,?,?)
ON CONFLICT(name) DO UPDATE SET holder=excluded.holder, expires_at_ms=excluded.expires_at_ms
WHERE fb_leases.expires_at_ms < ?`,
		MySQL: `
INSERT INTO fb_leases(name, holder, expires_at_ms) VALUES(?,?,?)
ON DUPLICATE KEY UPDATE
  holder=IF(fb_leases.expires_at_ms < VALUES(expires_at_ms), VALUES(holder), holder),
  expires_at_ms=IF(fb_leases.expires_at_ms < VALUES(expires_at_ms), VALUES(expires_at_ms), fb_leases.expires_at_ms)`,
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
	expires := time.Now().UnixMilli() + LeaseTTL.Milliseconds()
	res, err := dbx.Exec(ctx, l.DB, leaseAcquireSQL.For(dbx.DialectOf(l.DB)),
		name, l.Holder, expires, expires)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
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
// does not.
type leasedHandler struct {
	leases  *Leases
	name    string
	handler channel.Handler

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *leasedHandler) ID() string { return s.handler.ID() }

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
	if won {
		// The delegate's Start stays synchronous so a misconfigured channel is
		// reported to the bind exactly as it was before the lease existed.
		if err := s.handler.Start(runCtx, add, bus); err != nil {
			cancel()
			_ = s.leases.release(context.WithoutCancel(runCtx), s.name)
			close(done)
			return err
		}
	}
	go s.hold(runCtx, add, bus, done, cancel)
	return nil
}

// hold keeps the delegate connected for as long as this process holds the
// lease. delegateCancel is nil until the delegate has been started, so a
// replica that never won the lease never stops a handler it never started.
func (s *leasedHandler) hold(ctx context.Context, add channel.RouteAdder, bus channel.Bus, done chan struct{}, delegateCancel context.CancelFunc) {
	defer close(done)
	for {
		if delegateCancel != nil && !s.renewHeld(ctx) {
			// The lease is gone: disconnect at once, then wait to win the
			// account back rather than holding a connection that is no longer
			// this replica's to hold.
			_ = s.handler.Stop(context.WithoutCancel(ctx))
			delegateCancel()
			delegateCancel = nil
		}
		if ctx.Err() != nil {
			return
		}
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
	cancel, done := s.cancel, s.done
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
