package dbx_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/mattn/go-sqlite3"
)

// testDatabaseURL is the scratch server the MySQL and PostgreSQL halves of
// these tests run against (plan 209 §3.4). Unset means only SQLite runs.
const testDatabaseURL = "FOREBRAIN_TEST_DATABASE_URL"

func TestSQLFor(t *testing.T) {
	q := dbx.SQL{SQLite: "sqlite", MySQL: "mysql", Postgres: "postgres"}
	for _, tc := range []struct {
		dialect dbx.Dialect
		want    string
	}{
		{dbx.SQLite, "sqlite"},
		{dbx.MySQL, "mysql"},
		{dbx.Postgres, "postgres"},
	} {
		if got := q.For(tc.dialect); got != tc.want {
			t.Fatalf("For(%s) = %q, want %q", tc.dialect, got, tc.want)
		}
	}
	// A dialect without a translation must fail loudly: a query running
	// another dialect's text would be a silent wrong answer.
	for _, d := range []dbx.Dialect{dbx.MySQL, dbx.Postgres} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("For(%s) on a statement with no translation did not panic", d)
				}
			}()
			_ = dbx.SQL{SQLite: "select 1"}.For(d)
		}()
	}
}

func TestDialectOf(t *testing.T) {
	if got := dbx.DialectOf((*sql.DB)(nil)); got != dbx.SQLite {
		t.Fatalf("an unregistered handle reads as %s, want sqlite", got)
	}
	for _, d := range []dbx.Dialect{dbx.SQLite, dbx.MySQL, dbx.Postgres} {
		db := openSQLite(t)
		dbx.Register(db, d)
		if got := dbx.DialectOf(db); got != d {
			t.Fatalf("DialectOf(registered %s db) = %s", d, got)
		}
		conn, err := dbx.Conn(context.Background(), db)
		if err != nil {
			t.Fatalf("conn: %v", err)
		}
		if got := dbx.DialectOf(conn); got != d {
			t.Fatalf("DialectOf(conn of registered %s db) = %s", d, got)
		}
		_ = conn.Close()
	}
}

func TestDialectOfTx(t *testing.T) {
	db := openSQLite(t)
	tx, err := dbx.BeginTx(context.Background(), db, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if got := dbx.DialectOf(tx); got != dbx.SQLite {
		t.Fatalf("DialectOf(tx) = %s, want sqlite", got)
	}
	if got := tx.Dialect(); got != dbx.SQLite {
		t.Fatalf("tx.Dialect() = %s, want sqlite", got)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
}

// openSQLite opens a scratch SQLite database configured like the state
// database: every transaction takes the write lock at BEGIN.
func openSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+t.TempDir()+"/scratch.sqlite?_busy_timeout=5000&_journal_mode=WAL&_txlock=immediate&_foreign_keys=1")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping sqlite: %v", err)
	}
	return db
}

// openTestDatabase opens a registered handle on the scratch server named by
// FOREBRAIN_TEST_DATABASE_URL, in a database of its own, or skips when the
// variable is unset. Each dialect has its own URL scheme, so a test asks for
// the one it needs.
func openTestDatabase(t *testing.T, want dbx.Dialect) *sql.DB {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(testDatabaseURL))
	if raw == "" {
		t.Skipf("%s is not set", testDatabaseURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", testDatabaseURL, err)
	}
	switch {
	case want == dbx.MySQL && u.Scheme == "mysql":
		return openMySQLScratch(t, u)
	case want == dbx.Postgres && (u.Scheme == "postgres" || u.Scheme == "postgresql"):
		return openPostgresScratch(t, u)
	default:
		t.Skipf("%s names a %s database, not %s", testDatabaseURL, u.Scheme, want)
		return nil
	}
}

func scratchName(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "fallback"
	}
	return prefix + hex.EncodeToString(b[:])
}

func openMySQLScratch(t *testing.T, u *url.URL) *sql.DB {
	t.Helper()
	user := "root"
	pass := ""
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	addr := u.Host
	name := scratchName("fbd_")

	admin := mysql.NewConfig()
	admin.User, admin.Passwd, admin.Net, admin.Addr = user, pass, "tcp", addr
	adminDB, err := sql.Open("mysql", admin.FormatDSN())
	if err != nil {
		t.Fatalf("open mysql server: %v", err)
	}
	if _, err := adminDB.Exec(`CREATE DATABASE ` + name + ` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}

	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.Net, cfg.Addr, cfg.DBName = user, pass, "tcp", addr, name
	if err := cfg.Apply(mysql.Charset("utf8mb4", "utf8mb4_bin")); err != nil {
		t.Fatalf("charset: %v", err)
	}
	cfg.ParseTime = false
	cfg.MultiStatements = false
	cfg.Params = map[string]string{"transaction_isolation": "'READ-COMMITTED'"}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatalf("open scratch database: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping scratch database: %v", err)
	}
	dbx.Register(db, dbx.MySQL)
	t.Cleanup(func() {
		_ = db.Close()
		if _, err := adminDB.Exec(`DROP DATABASE IF EXISTS ` + name); err != nil {
			t.Logf("drop scratch database %s: %v", name, err)
		}
		_ = adminDB.Close()
	})
	return db
}

func openPostgresScratch(t *testing.T, u *url.URL) *sql.DB {
	t.Helper()
	name := scratchName("fbd_")
	adminURL := *u
	// The admin handle stays open until the cleanup drops the database: it is
	// the connection that does the dropping.
	adminDB, err := sql.Open("pgx", adminURL.String())
	if err != nil {
		t.Fatalf("open postgres server: %v", err)
	}
	if _, err := adminDB.Exec(`CREATE DATABASE ` + name); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}

	scratch := *u
	scratch.Path = "/" + name
	db, err := sql.Open("pgx", scratch.String())
	if err != nil {
		t.Fatalf("open scratch database: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping scratch database: %v", err)
	}
	dbx.Register(db, dbx.Postgres)
	t.Cleanup(func() {
		_ = db.Close()
		if _, err := adminDB.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`); err != nil {
			t.Logf("drop scratch database %s: %v", name, err)
		}
		_ = adminDB.Close()
	})
	return db
}
