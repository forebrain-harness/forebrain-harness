// Database open, options, and the SQLite driver configuration.
package state

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	sqlite3 "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schemaSQL string

func Open(ctx context.Context, absSQLitePath string, opts *OpenOptions) (*sql.DB, error) {
	abs, err := filepath.Abs(absSQLitePath)
	if err != nil {
		return nil, err
	}
	readOnly := opts != nil && opts.ReadOnly
	dsn := sqliteDataSourceForStateFile(abs)
	if readOnly {
		dsn = sqliteDataSourceForStateFileReadOnly(abs)
	}
	db, err := sql.Open(sqliteDriverName(), dsn)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if readOnly {
		if err := requireCurrentSchemaVersion(ctx, db); err != nil {
			_ = db.Close()
			return nil, err
		}
		return db, nil
	}
	if err := migrateStateSchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate state schema: %w", err)
	}
	return db, nil
}

func StateDBPath(home string) string {
	return filepath.Join(home, "state", "forebrain.state.sqlite")
}

func OpenStateFromHome(ctx context.Context, home string, cfg *appcfg.Root) (*sql.DB, error) {
	_ = cfg
	return Open(ctx, StateDBPath(home), nil)
}

func OpenStateForTest(ctx context.Context, absSQLitePath string) (*sql.DB, error) {
	return Open(ctx, absSQLitePath, nil)
}

type OpenOptions struct {
	// ReadOnly opens the database in read-only mode, skipping schema migrations
	// and DDL operations. Use for status/query commands that should not block on
	// or modify the schema.
	ReadOnly bool
}

// dbtx is the shared query surface of *sql.DB and *sql.Tx, so a write path
// can run standalone or inside one caller-owned transaction without two
// copies of its statements.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func init() {
	sql.Register("forebrain_sqlite", &sqlite3.SQLiteDriver{})
}

func sqliteDriverName() string {
	return "forebrain_sqlite"
}

func sqliteDataSourceForStateFile(abs string) string {
	p := filepath.ToSlash(abs)
	// A file URI path must be absolute (leading slash). Windows drive-letter
	// paths like "C:/Users/..." have no leading slash, so url.URL renders them
	// as "file:C:/Users/..." where the driver mis-parses "C:" as the URI
	// authority ("invalid uri authority: C:"). Prefixing a slash yields the
	// canonical "file:///C:/Users/..." form. POSIX paths already start with "/".
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{
		Scheme: "file",
		Path:   p,
		// cache=shared deliberately omitted: this is a real on-disk file, not
		// an unnamed :memory: database, so connections already share data via
		// the filesystem without it. Shared-cache mode instead adds its own
		// table-level locking between connections in the same process, which
		// _busy_timeout does not cover (that only retries SQLITE_BUSY, the
		// whole-file lock another process/connection holds; shared-cache's
		// table lock is a distinct SQLITE_LOCKED error) — confirmed to
		// produce a real, reproducible "database table is locked" error on
		// concurrent writers that was being silently discarded by a `_ =`
		// call site, permanently losing the final assistant message of a
		// completed turn. See docs/plan/TUI_FIRST_REFACTOR_PROGRESS.md for the full
		// characterization-test investigation that found this.
		// _txlock=immediate makes every write transaction take the write lock
		// up front, so a reader-turned-writer queues behind busy_timeout
		// instead of failing with SQLITE_BUSY_SNAPSHOT mid-transaction, which
		// busy_timeout does not retry (R1). _synchronous=NORMAL states the
		// driver's default explicitly: durable through process crashes, and
		// the fastest safe level under WAL.
		RawQuery: fmt.Sprintf("mode=rwc&_busy_timeout=%d&_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=1&_txlock=immediate", stateBusyTimeoutMs),
	}
	return u.String()
}

// stateBusyTimeoutMs is how long an ordinary statement waits for another
// writer's lock before it fails. Every pooled connection is opened with it,
// and a connection that raised it for a migration puts it back before the
// pool hands it to anyone else.
const stateBusyTimeoutMs = 5000

func sqliteDataSourceForStateFileReadOnly(abs string) string {
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{
		Scheme:   "file",
		Path:     p,
		RawQuery: fmt.Sprintf("mode=ro&_busy_timeout=%d&_foreign_keys=1", stateBusyTimeoutMs),
	}
	return u.String()
}

// OpenReadOnly opens an existing SQLite database file through the same
// registered driver the state store uses, without applying any schema. It
// exists for reading foreign databases (agent-migration sources) from a
// snapshot copy: mode=ro means the file is never created or written, so a
// missing or unreadable file surfaces as an error instead of an empty
// database silently standing in for one.
func OpenReadOnly(absSQLitePath string) (*sql.DB, error) {
	abs, err := filepath.Abs(absSQLitePath)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(sqliteDriverName(), sqliteDataSourceForStateFileReadOnly(abs))
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
