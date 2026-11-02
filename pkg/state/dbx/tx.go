package dbx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// LockTimeout bounds how long a writer waits for the global write lock before
// it fails as busy. It matches SQLite's _busy_timeout=5000 in the state DSN:
// on all three engines a writer that cannot have the lock within five seconds
// is told so rather than queued for ever.
const LockTimeout = "5" // seconds, as the string both MySQL's GET_LOCK and PG's SET LOCAL take

// MigrationLockTimeout bounds the wait for the schema-migration lock. A
// migration can take minutes on a large database, so this matches the
// busy_timeout the SQLite migration executor raises for itself (600000ms).
const MigrationLockTimeout = "600"

const (
	// WriteLockName is the MySQL named lock every write transaction takes.
	WriteLockName = "forebrain_state_write"
	// MigrationLockName is the MySQL named lock a schema migration takes. It
	// is deliberately a different name: a migration waiting on the write lock
	// and a writer waiting on the migration lock must not be able to deadlock.
	MigrationLockName = "forebrain_state_migrate"

	// WriteLockKey and MigrationLockKey are the PostgreSQL advisory locks the
	// same two roles take (pg_advisory_xact_lock / pg_advisory_lock). They
	// spell "fb_write" / "fb_migra" in ASCII so a DBA staring at
	// pg_locks can tell them apart.
	WriteLockKey     int64 = 0x66625f7772697465
	MigrationLockKey int64 = 0x66625f6d69677261
)

// Tx is one transaction, carrying the dialect that opened it and the lock
// releases it must run when it ends.
type Tx struct {
	*sql.Tx

	dialect Dialect
	// release runs once, after Commit or Rollback, on the engines whose lock
	// outlives the transaction (MySQL's named lock is session scoped). It is
	// nil on SQLite and PostgreSQL, whose locks end with the transaction.
	release func() error
}

// BeginTx opens a write transaction. Every dialect ends up serialised the way
// SQLite's immediate transactions are: SQLite's DSN takes the write lock at
// BEGIN, PostgreSQL takes a transaction-scoped advisory lock, and MySQL takes
// a named lock on an exclusive connection before beginning.
func BeginTx(ctx context.Context, db *sql.DB, opts *sql.TxOptions) (*Tx, error) {
	switch DialectOf(db) {
	case MySQL:
		return beginMySQL(ctx, db, opts)
	case Postgres:
		return beginPostgres(ctx, db, opts)
	default:
		tx, err := db.BeginTx(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &Tx{Tx: tx, dialect: SQLite}, nil
	}
}

// Commit commits the transaction and then releases the lock that outlived it.
func (t *Tx) Commit() error {
	err := t.Tx.Commit()
	return errors.Join(err, t.releaseLock())
}

// Rollback rolls the transaction back and then releases the lock that outlived
// it. Rolling back an already-finished transaction reports sql.ErrTxDone, as
// *sql.Tx does.
func (t *Tx) Rollback() error {
	err := t.Tx.Rollback()
	return errors.Join(err, t.releaseLock())
}

func (t *Tx) releaseLock() error {
	if t == nil || t.release == nil {
		return nil
	}
	release := t.release
	t.release = nil
	return release()
}

// Dialect reports which dialect opened the transaction.
func (t *Tx) Dialect() Dialect {
	if t == nil {
		return SQLite
	}
	return t.dialect
}

func beginMySQL(ctx context.Context, db *sql.DB, opts *sql.TxOptions) (*Tx, error) {
	conn, err := Conn(ctx, db)
	if err != nil {
		return nil, err
	}
	// The lock release runs on its own context: a cancelled caller context
	// must not leave the named lock held on a connection nothing owns.
	if err := mysqlGetLock(ctx, conn, WriteLockName, LockTimeout); err != nil {
		_ = conn.Close()
		return nil, err
	}
	tx, err := conn.BeginTx(ctx, opts)
	if err != nil {
		_ = mysqlReleaseLock(context.Background(), conn, WriteLockName)
		_ = conn.Close()
		return nil, err
	}
	return &Tx{Tx: tx, dialect: MySQL, release: func() error {
		releaseErr := mysqlReleaseLock(context.Background(), conn, WriteLockName)
		return errors.Join(releaseErr, conn.Close())
	}}, nil
}

func beginPostgres(ctx context.Context, db *sql.DB, opts *sql.TxOptions) (*Tx, error) {
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	// Only the lock acquisition is bounded; the transaction itself may run as
	// long as the work takes.
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '`+LockTimeout+`s'`); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, WriteLockKey); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("acquire postgres write lock: %v: %w", err, ErrBusy)
	}
	return &Tx{Tx: tx, dialect: Postgres}, nil
}

// Exec runs one write outside a transaction. On MySQL and PostgreSQL it takes
// the global write lock in a short transaction of its own, because SQLite's
// immediate transactions already serialise a bare statement with every other
// writer and a single-dialect caller must not be able to slip between them.
func Exec(ctx context.Context, db *sql.DB, q string, args ...any) (sql.Result, error) {
	switch DialectOf(db) {
	case MySQL, Postgres:
		tx, err := BeginTx(ctx, db, nil)
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback() }()
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return res, nil
	default:
		return db.ExecContext(ctx, q, args...)
	}
}

// AcquireMigrationLock takes the schema-migration lock, returning the function
// that releases it. On SQLite it is a no-op: the migration executor's own
// IMMEDIATE transaction is the lock. On MySQL and PostgreSQL it is a
// connection-scoped lock, held for the whole migration on a connection of its
// own, because the executor runs its DDL outside one long transaction.
func AcquireMigrationLock(ctx context.Context, db *sql.DB) (release func(), err error) {
	switch DialectOf(db) {
	case MySQL:
		conn, err := Conn(ctx, db)
		if err != nil {
			return nil, err
		}
		if err := mysqlGetLock(ctx, conn, MigrationLockName, MigrationLockTimeout); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return func() {
			_ = mysqlReleaseLock(context.Background(), conn, MigrationLockName)
			_ = conn.Close()
		}, nil
	case Postgres:
		conn, err := Conn(ctx, db)
		if err != nil {
			return nil, err
		}
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, MigrationLockKey); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("acquire postgres migration lock: %v: %w", err, ErrBusy)
		}
		return func() {
			_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, MigrationLockKey)
			_ = conn.Close()
		}, nil
	default:
		return func() {}, nil
	}
}

func mysqlGetLock(ctx context.Context, conn *sql.Conn, name, timeout string) error {
	var granted sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(?, `+timeout+`)`, name).Scan(&granted); err != nil {
		return err
	}
	if !granted.Valid || granted.Int64 != 1 {
		return fmt.Errorf("%w: mysql lock %q not granted within %ss", ErrBusy, name, timeout)
	}
	return nil
}

func mysqlReleaseLock(ctx context.Context, conn *sql.Conn, name string) error {
	var released sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT RELEASE_LOCK(?)`, name).Scan(&released); err != nil {
		return err
	}
	return nil
}
