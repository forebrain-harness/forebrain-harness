// Database open, options, and the drivers behind both SQLite and the
// network-hosted dialects (plan 209).
package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	sqlite3 "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schemaSQL string

func Open(ctx context.Context, absSQLitePath string, opts *OpenOptions) (*sql.DB, error) {
	if err := requireSQLiteBuild(ctx); err != nil {
		return nil, err
	}
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
	dbx.Register(db, dbx.SQLite)
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
	if err := migrateStateSchema(ctx, db, abs); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate state schema: %w", err)
	}
	return db, nil
}

// OpenFromConfig opens the state database the configuration names (plan 209,
// design §2.1). An empty databaseURL is the SQLite file under home/state, the
// behaviour every install had before MySQL and PostgreSQL were supported; a
// mysql:// or postgres:// / postgresql:// URL opens that server instead.
//
// The URL is the only place the host, port, database and credentials come
// from, and it is read from forebrain.yaml — never from the state database,
// which cannot name where it lives.
func OpenFromConfig(ctx context.Context, home string, databaseURL string, opts *OpenOptions) (*sql.DB, error) {
	target := strings.TrimSpace(databaseURL)
	if target == "" {
		return Open(ctx, StateDBPath(home), opts)
	}
	u, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("parse database.url: %w", err)
	}
	switch u.Scheme {
	case "mysql":
		return openMySQL(ctx, u, opts)
	case "postgres", "postgresql":
		return openPostgres(ctx, u, opts)
	default:
		return nil, fmt.Errorf("unsupported database type %q in database.url: use mysql://, postgres:// or postgresql://, or leave it empty for SQLite", u.Scheme)
	}
}

// openMySQL opens a MySQL handle through the driver's own connector: the DSN
// string form cannot carry every password, and a connector takes the parsed
// configuration directly, so a password holding ':' or '@' still reaches the
// server as written.
func openMySQL(ctx context.Context, u *url.URL, opts *OpenOptions) (*sql.DB, error) {
	cfg, err := mysqlConfigFromURL(u)
	if err != nil {
		return nil, err
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db := sql.OpenDB(connector)
	return finishOpen(ctx, db, dbx.MySQL, opts)
}

// openPostgres opens a PostgreSQL handle. pgx reads the URL as written; its
// default transaction isolation is READ COMMITTED, which is what the store
// expects (writes are serialised by dbx's global lock, not by the isolation
// level).
func openPostgres(ctx context.Context, u *url.URL, opts *OpenOptions) (*sql.DB, error) {
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	return finishOpen(ctx, db, dbx.Postgres, opts)
}

// finishOpen registers the dialect, proves the server answers, and then either
// verifies the schema version (a read-only open) or migrates it.
func finishOpen(ctx context.Context, db *sql.DB, dialect dbx.Dialect, opts *OpenOptions) (*sql.DB, error) {
	dbx.Register(db, dialect)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to %s: %w", dialect, err)
	}
	if opts != nil && opts.ReadOnly {
		if err := requireCurrentSchemaVersion(ctx, db); err != nil {
			_ = db.Close()
			return nil, err
		}
		return db, nil
	}
	if err := migrateStateSchema(ctx, db, ""); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate state schema: %w", err)
	}
	return db, nil
}

// MySQL session and connection settings the store depends on, forced over
// whatever the URL asks for. utf8mb4_bin keeps text comparison byte-exact the
// way SQLite's BINARY collation is, parseTime stays off because every stored
// time is an integer of milliseconds, and multiStatements stays off because
// the dialect schema files are executed one statement at a time.
const (
	mysqlCharset   = "utf8mb4"
	mysqlCollation = "utf8mb4_bin"
)

// mysqlConfigFromURL turns a mysql:// URL into the driver's configuration,
// including the database it names.
func mysqlConfigFromURL(u *url.URL) (*mysql.Config, error) {
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" {
		return nil, fmt.Errorf("database.url names no MySQL database")
	}
	cfg, err := mysqlServerConfigFromURL(u)
	if err != nil {
		return nil, err
	}
	cfg.DBName = name
	return cfg, nil
}

// mysqlServerConfigFromURL is the part of the configuration that names the
// server and the session, without a database: the same settings a scratch
// database is created through.
func mysqlServerConfigFromURL(u *url.URL) (*mysql.Config, error) {
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("database.url names no MySQL host")
	}
	port := u.Port()
	if port == "" {
		port = "3306"
	}
	cfg := mysql.NewConfig()
	cfg.Params = map[string]string{}
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(host, port)
	if u.User != nil {
		cfg.User = u.User.Username()
		cfg.Passwd, _ = u.User.Password()
	}
	cfg.ParseTime = false
	cfg.MultiStatements = false
	if err := cfg.Apply(mysql.Charset(mysqlCharset, mysqlCollation)); err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	// A session setting the driver applies on every new connection: the global
	// write lock serialises writers, and READ COMMITTED is what a reader then
	// expects to see — the rows another writer already committed, never its
	// half-finished transaction.
	cfg.Params["transaction_isolation"] = "'READ-COMMITTED'"
	for key, values := range u.Query() {
		if len(values) == 0 || forcedMySQLParams[key] {
			continue
		}
		cfg.Params[key] = values[len(values)-1]
	}
	return cfg, nil
}

// forcedMySQLParams are the settings mysqlConfigFromURL decides itself: a URL
// that carries them would change how the store's text compares, how times are
// scanned, or whether one query can carry several statements.
var forcedMySQLParams = map[string]bool{
	"charset":               true,
	"collation":             true,
	"parseTime":             true,
	"multiStatements":       true,
	"transaction_isolation": true,
}

func StateDBPath(home string) string {
	return filepath.Join(home, "state", "forebrain.state.sqlite")
}

// OpenStateFromHome opens the home's state database at the URL the
// configuration file names — "" for the home's own SQLite file.
func OpenStateFromHome(ctx context.Context, home string, databaseURL string) (*sql.DB, error) {
	return OpenFromConfig(ctx, home, databaseURL, nil)
}

// testDatabaseURLVar names the MySQL or PostgreSQL server the dialect tests
// run against (plan 209 §3.4, design §2.8). Unset means the tests that need
// one skip, and every store test runs on SQLite exactly as before.
const testDatabaseURLVar = "FOREBRAIN_TEST_DATABASE_URL"

// OpenStateForTest opens a scratch state database for a test. With no
// FOREBRAIN_TEST_DATABASE_URL it is the SQLite file at absSQLitePath, as it
// always was. When the variable names a MySQL or PostgreSQL server, the test
// gets a database of its own on that server — named after a hash of
// absSQLitePath, dropped and recreated — and the same store code runs against
// it: that is what lets the suite run on all three engines without a test file
// changing.
func OpenStateForTest(ctx context.Context, absSQLitePath string) (*sql.DB, error) {
	url, ok, err := testDatabaseURLFor(absSQLitePath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return Open(ctx, absSQLitePath, nil)
	}
	return OpenFromConfig(ctx, "", url, nil)
}

// testDatabaseURLFor reports the URL of a fresh scratch database on the test
// server, or ok=false when no server is configured.
func testDatabaseURLFor(scratchPath string) (string, bool, error) {
	target := strings.TrimSpace(os.Getenv(testDatabaseURLVar))
	if target == "" {
		return "", false, nil
	}
	u, err := url.Parse(target)
	if err != nil {
		return "", false, fmt.Errorf("parse %s: %w", testDatabaseURLVar, err)
	}
	sum := sha256.Sum256([]byte(scratchPath))
	name := "fbt_" + hex.EncodeToString(sum[:8])
	switch u.Scheme {
	case "mysql":
		if err := recreateMySQLDatabase(u, name); err != nil {
			return "", false, err
		}
		scratch := *u
		scratch.Path = "/" + name
		return scratch.String(), true, nil
	case "postgres", "postgresql":
		if err := recreatePostgresDatabase(u, name); err != nil {
			return "", false, err
		}
		scratch := *u
		scratch.Path = "/" + name
		return scratch.String(), true, nil
	default:
		return "", false, fmt.Errorf("%s names an unsupported database type %q", testDatabaseURLVar, u.Scheme)
	}
}

// recreateMySQLDatabase drops and creates the scratch database, connecting as
// the configured user to the server itself rather than to the URL's database:
// that name is only a placeholder for the dialect tests, and the server's
// default schema is not guaranteed to exist on a hosted one.
func recreateMySQLDatabase(u *url.URL, name string) error {
	cfg, err := mysqlServerConfigFromURL(u)
	if err != nil {
		return err
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return fmt.Errorf("open mysql test server: %w", err)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DROP DATABASE IF EXISTS `+name); err != nil {
		return fmt.Errorf("drop scratch database %s: %w", name, err)
	}
	if _, err := db.ExecContext(ctx, `CREATE DATABASE `+name+` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`); err != nil {
		return fmt.Errorf("create scratch database %s: %w", name, err)
	}
	return nil
}

// recreatePostgresDatabase drops and creates the scratch database, connecting
// to the database the URL names (which must already exist).
func recreatePostgresDatabase(u *url.URL, name string) error {
	admin, err := sql.Open("pgx", u.String())
	if err != nil {
		return fmt.Errorf("open postgres test server: %w", err)
	}
	defer admin.Close()
	ctx := context.Background()
	if _, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+name); err != nil {
		return fmt.Errorf("drop scratch database %s: %w", name, err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+name); err != nil {
		return fmt.Errorf("create scratch database %s: %w", name, err)
	}
	return nil
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

// errSQLiteBuild is what a forebrain built without cgo or without SQLite's
// FTS5 module reports: memory search declares an FTS5 table, so such a build
// cannot open its state at all, and the way out is to rebuild it.
var errSQLiteBuild = errors.New("this forebrain was built without cgo or SQLite FTS5; reinstall it with: npm install -g @forebrain-harness/forebrain")

var (
	sqliteBuildOnce sync.Once
	sqliteBuildErr  error
)

// requireSQLiteBuild asks the linked SQLite engine itself whether it can
// serve the state schema. A cgo-less build links go-sqlite3's stub, whose
// every query fails; a build without the fts5 tag answers 0.
func requireSQLiteBuild(ctx context.Context) error {
	sqliteBuildOnce.Do(func() {
		db, err := sql.Open(sqliteDriverName(), ":memory:")
		if err != nil {
			sqliteBuildErr = errSQLiteBuild
			return
		}
		defer db.Close()
		var fts5 int
		if err := db.QueryRowContext(ctx, `SELECT sqlite_compileoption_used('ENABLE_FTS5')`).Scan(&fts5); err != nil || fts5 != 1 {
			sqliteBuildErr = errSQLiteBuild
		}
	})
	return sqliteBuildErr
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
	if err := requireSQLiteBuild(context.Background()); err != nil {
		return nil, err
	}
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
