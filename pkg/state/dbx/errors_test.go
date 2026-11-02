package dbx_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsUniqueViolationSQLite(t *testing.T) {
	db := openSQLite(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE t (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO t (id) VALUES ('a')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO t (id) VALUES ('a')`)
	if err == nil {
		t.Fatal("duplicate insert succeeded")
	}
	if !dbx.IsUniqueViolation(err) {
		t.Fatalf("IsUniqueViolation(%v) = false", err)
	}
	if dbx.IsBusy(err) {
		t.Fatalf("IsBusy(%v) = true", err)
	}
}

func TestIsUniqueViolation(t *testing.T) {
	// Each dialect reports the conflict in its own words; the one the store
	// asks about must be recognised without the caller knowing the engine.
	for _, d := range []dbx.Dialect{dbx.MySQL, dbx.Postgres} {
		t.Run(d.String(), func(t *testing.T) {
			db := openTestDatabase(t, d)
			ctx := context.Background()
			if _, err := db.ExecContext(ctx, `CREATE TABLE t (id VARCHAR(64) PRIMARY KEY)`); err != nil {
				t.Fatalf("create table: %v", err)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO t (id) VALUES ('a')`); err != nil {
				t.Fatalf("insert: %v", err)
			}
			_, err := db.ExecContext(ctx, `INSERT INTO t (id) VALUES ('a')`)
			if err == nil {
				t.Fatal("duplicate insert succeeded")
			}
			if !dbx.IsUniqueViolation(err) {
				t.Fatalf("IsUniqueViolation(%v) = false", err)
			}
			if dbx.IsBusy(err) {
				t.Fatalf("IsBusy(%v) = true", err)
			}
		})
	}
}

func TestIsBusy(t *testing.T) {
	if !dbx.IsBusy(dbx.ErrBusy) {
		t.Fatal("the package's own busy error is not busy")
	}
	if dbx.IsBusy(nil) || dbx.IsBusy(sql.ErrNoRows) {
		t.Fatal("a non-busy error reads as busy")
	}
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"mysql lock wait timeout", &mysql.MySQLError{Number: 1205}},
		{"mysql deadlock", &mysql.MySQLError{Number: 1213}},
		{"postgres lock not available", &pgconn.PgError{Code: "55P03"}},
		{"postgres deadlock", &pgconn.PgError{Code: "40P01"}},
	} {
		if !dbx.IsBusy(tc.err) {
			t.Fatalf("%s is not busy", tc.name)
		}
	}
	if dbx.IsBusy(&mysql.MySQLError{Number: 1062}) {
		t.Fatal("a unique violation reads as busy")
	}
	if !errors.Is(dbx.ErrBusy, dbx.ErrBusy) {
		t.Fatal("ErrBusy does not match itself")
	}
}
