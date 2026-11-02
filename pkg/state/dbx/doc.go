// Package dbx is the database-dialect layer of the state store: which dialect
// a handle speaks (Dialect, Register, DialectOf), one statement per dialect
// (SQL and its For), the global write lock that gives MySQL and PostgreSQL
// SQLite's serialised writes (BeginTx, Exec, AcquireMigrationLock), and the
// error classification the store asks about (IsUniqueViolation, IsBusy).
//
// It exists because of the owner's ruling on plan 209: no ORM, SQLite's
// statements are never edited, and MySQL and PostgreSQL each get their own
// text chosen at run time. The files are one concern each — dbx.go the
// registry and the statement triple, tx.go the transactions and locks,
// errors.go the classification.
//
// It imports the three drivers and nothing from this repository, so pkg/state
// and pkg/memory can depend on it without a cycle.
package dbx
