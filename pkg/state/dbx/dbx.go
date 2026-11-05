package dbx

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
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

// Placeholder renders the placeholder of the n-th parameter (1-based) on d:
// PostgreSQL numbers its parameters, SQLite and MySQL write every one as '?'.
// A statement translated from SQLite numbers its parameters in the order
// SQLite's '?' appear, so n is the ordinal of the '?' the parameter fills.
func Placeholder(d Dialect, n int) string {
	if d == Postgres {
		return "$" + strconv.Itoa(n)
	}
	return "?"
}

// Placeholders renders n placeholders for a list assembled at run time — an
// IN (…) whose length only the caller knows — starting at the n-th parameter.
func Placeholders(d Dialect, start, n int) string {
	if n <= 0 {
		return ""
	}
	marks := make([]string, 0, n)
	for i := 0; i < n; i++ {
		marks = append(marks, Placeholder(d, start+i))
	}
	return strings.Join(marks, ",")
}

// Rebind rewrites a SQLite '?'-placeholder statement for d: PostgreSQL numbers
// its parameters, SQLite and MySQL write every one as '?'. The arguments stay
// in the order the '?' appear. A '?' inside a single-quoted string literal is
// left alone, so a statement that carries a literal '?' still rebinds.
//
// It is for a statement that is assembled or seeded outside the store's own
// dbx.SQL arms — a test's fixture, a one-off probe — where writing the same text
// three times would be more code than the statement is worth.
func Rebind(d Dialect, query string) string {
	if d != Postgres {
		return query
	}
	var b strings.Builder
	n := 0
	inString := false
	for i := 0; i < len(query); i++ {
		c := query[i]
		if inString {
			b.WriteByte(c)
			if c == '\'' {
				if i+1 < len(query) && query[i+1] == '\'' {
					b.WriteByte(query[i+1])
					i++
				} else {
					inString = false
				}
			}
			continue
		}
		switch c {
		case '\'':
			inString = true
			b.WriteByte(c)
		case '?':
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// dbDialects maps a *sql.DB registered by Open to the dialect it speaks.
var dbDialects sync.Map

// Register records the dialect db speaks. Open calls it once per database; a
// handle nobody registered reads as SQLite, which is the only engine a caller
// can open without going through Open.
func Register(db *sql.DB, d Dialect) {
	if db == nil {
		return
	}
	dbDialects.Store(db, d)
}

// Conn is db.Conn: it returns the connection as-is and records nothing. Its
// callers — the migration executor pinning a lock, and beginMySQL taking the
// write lock before it begins the transaction — already hold the dialect.
func Conn(ctx context.Context, db *sql.DB) (*sql.Conn, error) {
	return db.Conn(ctx)
}

// DialectOf answers only for a *sql.DB registered by Open and a *Tx from
// BeginTx, so what it remembers is bounded by the databases the process opens,
// not by the connections it hands out. A hand-driven *sql.Conn deliberately
// reads as the default: no caller asks one for its dialect, and every caller
// that needs one holds the *sql.DB or the *Tx.
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
	}
	return SQLite
}
