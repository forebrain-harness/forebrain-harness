package dbx

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// Dialect is the SQL dialect a handle speaks.
type Dialect int

const (
	// SQLite is the embedded engine, and the dialect of every handle no
	// Open registered — only SQLite can be opened outside Open.
	SQLite Dialect = iota
	// MySQL is MySQL 8.0 or later.
	MySQL
	// Postgres is PostgreSQL 14 or later.
	Postgres
)

func (d Dialect) String() string {
	switch d {
	case MySQL:
		return "mysql"
	case Postgres:
		return "postgres"
	default:
		return "sqlite"
	}
}

// SQL is one statement in each dialect. The SQLite text is the statement as it
// was before multi-database support and is never edited; MySQL and Postgres
// are translations of it with the same rows, the same order and the same
// nulls.
type SQL struct {
	SQLite   string
	MySQL    string
	Postgres string
}

// For returns the statement for d. An empty one panics: a missing translation
// is a programming error, and failing the first test that reaches it is how it
// gets noticed instead of a query quietly running the wrong dialect's text.
func (q SQL) For(d Dialect) string {
	var stmt string
	switch d {
	case MySQL:
		stmt = q.MySQL
	case Postgres:
		stmt = q.Postgres
	default:
		stmt = q.SQLite
	}
	if stmt == "" {
		panic(fmt.Sprintf("dbx: no %s statement for %q", d, q.SQLite))
	}
	return stmt
}

// handles maps a registered *sql.DB, and a *sql.Conn taken from one through
// Conn, to the dialect it speaks.
var (
	dbDialects   sync.Map
	connDialects sync.Map
)

// Register records the dialect db speaks. Open calls it once per database; a
// handle nobody registered reads as SQLite, which is the only engine a caller
// can open without going through Open.
func Register(db *sql.DB, d Dialect) {
	if db == nil {
		return
	}
	dbDialects.Store(db, d)
}

// Conn is db.Conn with the dialect recorded, so DialectOf answers for the
// connection a caller drives by hand — the migration executor does. A
// *sql.Conn taken straight from *sql.DB is indistinguishable from a SQLite
// one, which is why the store goes through here.
func Conn(ctx context.Context, db *sql.DB) (*sql.Conn, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	connDialects.Store(conn, DialectOf(db))
	return conn, nil
}

// DialectOf answers for a *sql.DB registered by Open, a *Tx from BeginTx, or
// a *sql.Conn obtained through Conn. Anything else is SQLite.
func DialectOf(q any) Dialect {
	switch v := q.(type) {
	case *Tx:
		if v != nil {
			return v.dialect
		}
	case *sql.DB:
		if d, ok := dbDialects.Load(v); ok {
			return d.(Dialect)
		}
	case *sql.Conn:
		if d, ok := connDialects.Load(v); ok {
			return d.(Dialect)
		}
	}
	return SQLite
}
