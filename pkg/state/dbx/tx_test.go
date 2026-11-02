package dbx_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

// holdFor is how long the first transaction of a serialisation test keeps the
// write lock: long enough to be measured, short enough that a build does not
// wait on it.
const holdFor = 300 * time.Millisecond

func TestWriteLockSerialisesTransactions(t *testing.T) {
	for _, d := range []dbx.Dialect{dbx.SQLite, dbx.MySQL, dbx.Postgres} {
		t.Run(d.String(), func(t *testing.T) {
			db := openDialect(t, d)
			ctx := context.Background()

			var (
				wg        sync.WaitGroup
				firstHeld = make(chan struct{})
				firstDone = make(chan time.Time, 1)
				firstErr  error
			)
			wg.Add(1)
			go func() {
				defer wg.Done()
				tx, err := dbx.BeginTx(ctx, db, nil)
				if err != nil {
					firstErr = err
					close(firstHeld)
					return
				}
				close(firstHeld)
				time.Sleep(holdFor)
				if _, err := tx.ExecContext(ctx, `SELECT 1`); err != nil {
					firstErr = err
					_ = tx.Rollback()
					return
				}
				firstErr = tx.Commit()
				firstDone <- time.Now()
			}()

			<-firstHeld
			if firstErr != nil {
				t.Fatalf("first transaction: %v", firstErr)
			}
			start := time.Now()
			second, err := dbx.BeginTx(ctx, db, nil)
			waited := time.Since(start)
			if err != nil {
				wg.Wait()
				t.Fatalf("second transaction: %v", err)
			}
			if err := second.Rollback(); err != nil {
				t.Fatalf("rollback second: %v", err)
			}
			// The first transaction's commit instant is not compared: see below.
			<-firstDone
			wg.Wait()
			if firstErr != nil {
				t.Fatalf("first transaction: %v", firstErr)
			}
			// The second could only begin once the first had released the
			// write lock, so it waited for the hold. Nothing is asserted about
			// which of the two the clock observed first: PostgreSQL releases
			// its transaction lock while the first client is still reading the
			// commit's answer, which is serialised — just not ordered against
			// the caller's own timestamp.
			if waited < holdFor/2 {
				t.Fatalf("the second transaction began after %s while the first held the write lock for %s: writes are not serialised",
					waited, holdFor)
			}
		})
	}
}

func TestExecWaitsForOpenTransaction(t *testing.T) {
	for _, d := range []dbx.Dialect{dbx.MySQL, dbx.Postgres} {
		t.Run(d.String(), func(t *testing.T) {
			db := openDialect(t, d)
			ctx := context.Background()

			inTx := make(chan struct{})
			txDone := make(chan error, 1)
			go func() {
				tx, err := dbx.BeginTx(ctx, db, nil)
				if err != nil {
					close(inTx)
					txDone <- err
					return
				}
				close(inTx)
				time.Sleep(holdFor)
				txDone <- tx.Commit()
			}()
			<-inTx

			start := time.Now()
			if _, err := dbx.Exec(ctx, db, `SELECT 1`); err != nil {
				<-txDone
				t.Fatalf("exec: %v", err)
			}
			elapsed := time.Since(start)
			if err := <-txDone; err != nil {
				t.Fatalf("commit: %v", err)
			}
			if elapsed < holdFor/2 {
				t.Fatalf("Exec finished in %s while a transaction held the write lock for %s", elapsed, holdFor)
			}
		})
	}
}

// openDialect opens a scratch database for one dialect: SQLite in a temporary
// file, MySQL and PostgreSQL on the server FOREBRAIN_TEST_DATABASE_URL names.
func openDialect(t *testing.T, d dbx.Dialect) *sql.DB {
	t.Helper()
	if d == dbx.SQLite {
		return openSQLite(t)
	}
	return openTestDatabase(t, d)
}
