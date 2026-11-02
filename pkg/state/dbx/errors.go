package dbx

import (
	"errors"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	sqlite3 "github.com/mattn/go-sqlite3"
)

// ErrBusy reports that the state database's write lock is held by another
// writer, which is what SQLite's "database is locked" and MySQL's and
// PostgreSQL's lock timeouts all mean here. A caller that retries a busy write
// matches this with errors.Is, whichever engine produced it.
var ErrBusy = errors.New("state database write lock is held by another writer")

// IsUniqueViolation reports whether err is a uniqueness conflict — a duplicate
// on a unique index or on a primary key — the answer SQLite (2067 / 1555),
// MySQL (1062) and PostgreSQL (23505) each give in their own words.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) {
		// MySQL and PostgreSQL report a primary-key clash as the same code a
		// unique index raises (1062 / 23505), and SQLite splits the two, so
		// both of its codes are one conflict here.
		switch sqliteErr.ExtendedCode {
		case sqlite3.ErrConstraintUnique, sqlite3.ErrConstraintPrimaryKey:
			return true
		}
		return false
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1062
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

// IsBusy reports whether err is the write lock being unavailable: SQLite's
// busy/locked results, MySQL's lock-wait timeout (1205) and deadlock (1213),
// PostgreSQL's lock_not_available (55P03) and deadlock_detected (40P01), and
// the busy error this package raises when a lock it waits for is not granted.
func IsBusy(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrBusy) {
		return true
	}
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) {
		switch sqliteErr.Code & 0xff {
		case sqlite3.ErrBusy, sqlite3.ErrLocked:
			return true
		}
		return false
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case 1205, 1213:
			return true
		}
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "55P03", "40P01":
			return true
		}
		return false
	}
	return false
}
