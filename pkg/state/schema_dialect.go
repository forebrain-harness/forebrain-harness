// The MySQL and PostgreSQL half of the state schema: the two dialect scripts
// and the executor that creates or carries them forward (plan 209, design
// §2.3–§2.5). SQLite keeps its own path — schema.sql plus the historical
// migrations — in schema_migrations.go, which dispatches here for the other
// two engines.
package state

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

//go:embed schema_mysql.sql
var schemaMySQL string

//go:embed schema_postgres.sql
var schemaPostgres string

// dialectSchemaFor returns the script that creates the state schema on d.
func dialectSchemaFor(d dbx.Dialect) (string, error) {
	switch d {
	case dbx.MySQL:
		return schemaMySQL, nil
	case dbx.Postgres:
		return schemaPostgres, nil
	default:
		return "", fmt.Errorf("state: %s has no dialect schema", d)
	}
}

// The statements only MySQL and PostgreSQL have: they ask the catalog for the
// tables and read the version row from fb_schema_version, where SQLite asks
// sqlite_master and PRAGMA user_version itself. Their SQLite arm is empty on
// purpose — these statements are never run there, and the schema snapshot
// (pkg/architecture) holds the SQLite text of the questions they answer.
var (
	fbTableCountSQL = dbx.SQL{
		MySQL:    `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name LIKE 'fb\_%'`,
		Postgres: `SELECT COUNT(*) FROM pg_catalog.pg_tables WHERE schemaname = current_schema() AND tablename LIKE 'fb\_%'`,
	}
	schemaVersionReadSQL = dbx.SQL{
		MySQL:    `SELECT version FROM fb_schema_version LIMIT 1`,
		Postgres: `SELECT version FROM fb_schema_version LIMIT 1`,
	}
	schemaVersionDeleteSQL = dbx.SQL{
		MySQL:    `DELETE FROM fb_schema_version`,
		Postgres: `DELETE FROM fb_schema_version`,
	}
	schemaVersionInsertSQL = dbx.SQL{
		MySQL:    `INSERT INTO fb_schema_version (version) VALUES (?)`,
		Postgres: `INSERT INTO fb_schema_version (version) VALUES ($1)`,
	}
)

// migrateDialectSchema brings a MySQL or PostgreSQL database to
// stateSchemaVersion. A database with no state table is created from the
// dialect script whole and stamped with the version — there is no history to
// replay, because a new database starts at the current shape (design §2.4). A
// database that has one is carried forward by the migrations it is missing,
// each of which has to implement all three dialects.
//
// One migration lock covers the whole run so two processes starting at once
// cannot create or migrate the schema twice; it is a different lock from the
// write lock, so an ordinary writer never waits on a migration.
func migrateDialectSchema(ctx context.Context, db *sql.DB, dialect dbx.Dialect) error {
	release, err := dbx.AcquireMigrationLock(ctx, db)
	if err != nil {
		return err
	}
	defer release()

	var tables int
	if err := db.QueryRowContext(ctx, fbTableCountSQL.For(dialect)).Scan(&tables); err != nil {
		return fmt.Errorf("count state tables: %w", err)
	}
	if tables == 0 {
		if err := createDialectSchema(ctx, db, dialect); err != nil {
			return err
		}
		return writeDialectSchemaVersion(ctx, db, dialect, stateSchemaVersion)
	}

	version, err := readDialectSchemaVersion(ctx, db, dialect)
	if err != nil {
		return err
	}
	if version == stateSchemaVersion {
		return nil
	}
	if version > stateSchemaVersion {
		return ErrStateSchemaNewer
	}
	if err := requireDialectMigrations(stateSchemaMigrations, version); err != nil {
		return err
	}
	conn, err := dbx.Conn(ctx, db)
	if err != nil {
		return err
	}
	defer conn.Close()
	for _, m := range stateSchemaMigrations {
		if m.version <= version {
			continue
		}
		apply := m.applyMySQL
		if dialect == dbx.Postgres {
			apply = m.applyPostgres
		}
		if err := apply(ctx, conn); err != nil {
			return fmt.Errorf("migrate state database to v%d: %w", m.version, err)
		}
	}
	return writeDialectSchemaVersion(ctx, db, dialect, stateSchemaVersion)
}

// createDialectSchema runs the dialect script one statement at a time. It does
// not run in a transaction because MySQL commits DDL as it goes; the migration
// lock is what keeps a second process out, and a failure part way through
// leaves a database missing its version row, which the next open reports
// instead of migrating a half-built schema.
func createDialectSchema(ctx context.Context, db *sql.DB, dialect dbx.Dialect) error {
	script, err := dialectSchemaFor(dialect)
	if err != nil {
		return err
	}
	for _, stmt := range splitSQLStatements(script) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create state schema: %w", err)
		}
	}
	return nil
}

// splitSQLStatements cuts a dialect script into one statement per ';', leaving
// a ';' inside a quoted string or behind a -- comment alone.
func splitSQLStatements(script string) []string {
	var (
		out      []string
		current  strings.Builder
		inString bool
		inLine   bool
	)
	for i := 0; i < len(script); i++ {
		c := script[i]
		switch {
		case inLine:
			current.WriteByte(c)
			if c == '\n' {
				inLine = false
			}
		case inString:
			current.WriteByte(c)
			if c != '\'' {
				continue
			}
			// A doubled quote is an escaped one, not the end of the string.
			if i+1 < len(script) && script[i+1] == '\'' {
				current.WriteByte(script[i+1])
				i++
				continue
			}
			inString = false
		case c == '\'':
			inString = true
			current.WriteByte(c)
		case c == '-' && i+1 < len(script) && script[i+1] == '-':
			inLine = true
			current.WriteByte(c)
		case c == ';':
			if stmt := strings.TrimSpace(current.String()); stmt != "" {
				out = append(out, stmt)
			}
			current.Reset()
		default:
			current.WriteByte(c)
		}
	}
	if stmt := strings.TrimSpace(current.String()); stmt != "" {
		out = append(out, stmt)
	}
	return out
}

// errDialectSchemaIncomplete is a database that holds state tables but no
// version row: an interrupted creation, or one made by hand.
var errDialectSchemaIncomplete = errors.New("state database has tables but no row in fb_schema_version; drop the database and start forebrain again to create it")

// readDialectSchemaVersion reads the version row. A database holding state
// tables but no version row is an interrupted creation or a hand-made one, and
// neither can be carried forward safely: what version it is at is unknowable.
func readDialectSchemaVersion(ctx context.Context, db *sql.DB, dialect dbx.Dialect) (int, error) {
	var version int64
	err := db.QueryRowContext(ctx, schemaVersionReadSQL.For(dialect)).Scan(&version)
	if err == nil {
		return int(version), nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errDialectSchemaIncomplete
	}
	return 0, fmt.Errorf("read state schema version: %w", err)
}

// writeDialectSchemaVersion puts the version row at version, inside the global
// write lock so it cannot interleave with another writer's transaction.
func writeDialectSchemaVersion(ctx context.Context, db *sql.DB, dialect dbx.Dialect, version int) error {
	tx, err := dbx.BeginTx(ctx, db, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, schemaVersionDeleteSQL.For(dialect)); err != nil {
		return fmt.Errorf("clear state schema version: %w", err)
	}
	if _, err := tx.ExecContext(ctx, schemaVersionInsertSQL.For(dialect), version); err != nil {
		return fmt.Errorf("record state schema version: %w", err)
	}
	return tx.Commit()
}

// requireDialectMigrations fails when a migration the database still needs has
// no implementation for its engine. Design §0.4: from here on every schema
// change is written three times, and a migration that skipped MySQL or
// PostgreSQL must stop the open instead of being silently skipped over.
func requireDialectMigrations(migrations []schemaMigration, version int) error {
	for _, m := range migrations {
		if m.version <= version {
			continue
		}
		if m.applyMySQL == nil {
			return fmt.Errorf("state schema migration to v%d has no MySQL implementation", m.version)
		}
		if m.applyPostgres == nil {
			return fmt.Errorf("state schema migration to v%d has no PostgreSQL implementation", m.version)
		}
	}
	return nil
}

// requireDialectSchemaVersion is the read-only gate for MySQL and PostgreSQL,
// the counterpart of the PRAGMA user_version check SQLite makes: without the
// lock a migration needs, a database at another version is an error rather
// than a best-effort partial read.
func requireDialectSchemaVersion(ctx context.Context, db *sql.DB, dialect dbx.Dialect) error {
	version, err := readDialectSchemaVersion(ctx, db, dialect)
	if err != nil {
		if errors.Is(err, errDialectSchemaIncomplete) {
			return ErrStateSchemaOutdated
		}
		return err
	}
	if version != stateSchemaVersion {
		return ErrStateSchemaOutdated
	}
	return nil
}
