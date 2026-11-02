package state

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

// The dialect schema's contract (plan 209 §3.3, §3.4): MySQL and PostgreSQL
// declare the same tables, columns and index names as schema.sql, with each
// column's kind preserved. These tests need a server — FOREBRAIN_TEST_DATABASE_URL
// — and skip without one, so a plain `go test ./pkg/state` is still SQLite only.

// derivedColumns are the columns a dialect has to add because the engine cannot
// express SQLite's form directly: an explicit rowid for the store's ordering, a
// digest for a key InnoDB cannot hold over the whole value, and the tsvector
// PostgreSQL searches. Every one of them is derived from a column schema.sql
// has, so nothing here carries information the SQLite schema lacks.
var derivedColumns = map[dbx.Dialect]map[string]string{
	dbx.MySQL: {
		"fb_artifacts.row_id":          "SQLite's rowid, which the store orders same-timestamp artifacts by",
		"fb_runs.row_id":               "SQLite's rowid, which the store orders runs by when timestamps tie",
		"fb_memory_fts.row_id":         "SQLite's rowid, which fb_memory_index_files.first_rowid addresses",
		"fb_file_read_states.path_sha": "primary key digest of path, which InnoDB cannot index whole",
	},
	dbx.Postgres: {
		"fb_artifacts.row_id":     "SQLite's rowid, which the store orders same-timestamp artifacts by",
		"fb_runs.row_id":          "SQLite's rowid, which the store orders runs by when timestamps tie",
		"fb_memory_fts.row_id":    "SQLite's rowid, which fb_memory_index_files.first_rowid addresses",
		"fb_memory_fts.terms_tsv": "to_tsvector('simple', terms), the GIN-indexed half of the full-text search",
	},
}

// dialectOnlyTables hold the schema version on the engines that have no
// PRAGMA user_version: the table is the version mechanism, not part of the
// state schema schema.sql declares.
var dialectOnlyTables = map[string]bool{
	"fb_schema_version": true,
}

// ftsShadowPrefix is how SQLite names the five tables FTS5 keeps behind a
// virtual table. They are the engine's own storage, not columns the store
// reads, and no other dialect has an equivalent.
const ftsShadowPrefix = "fb_memory_fts_"

// testDialectSchemaMatch compares the dialect database with a fresh SQLite one.
func TestSchemaDialectMatchesSQLite(t *testing.T) {
	dialect, db := openDialectStateForTest(t)
	ctx := context.Background()

	sqliteDB, err := Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), nil)
	if err != nil {
		t.Fatalf("open sqlite state database: %v", err)
	}
	defer sqliteDB.Close()

	wantTables, wantColumns, wantIndexes := sqliteSchema(t, sqliteDB)
	gotTables, gotColumns, gotIndexes := dialectSchema(t, ctx, db, dialect)

	missing, extra := diffSets(wantTables, gotTables)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("%s is missing tables %v and has extra tables %v", dialect, missing, extra)
	}
	derived := derivedColumns[dialect]
	missing, extra = diffSets(wantColumns, gotColumns)
	var unexplained []string
	for _, col := range extra {
		if _, ok := derived[col]; !ok {
			unexplained = append(unexplained, col)
		}
	}
	for col, reason := range derived {
		if !gotColumns[col] {
			t.Fatalf("%s should carry the derived column %s (%s)", dialect, col, reason)
		}
	}
	if len(missing) > 0 || len(unexplained) > 0 {
		t.Fatalf("%s is missing columns %v and has unexplained extra columns %v", dialect, missing, unexplained)
	}
	missing, extra = diffSets(wantIndexes, gotIndexes)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("%s is missing indexes %v and has extra idx_ indexes %v", dialect, missing, extra)
	}

	// Every column keeps its kind: an INTEGER stays a 64-bit integer and a TEXT
	// stays a character type (design §2.4's type map).
	sqliteTypes := sqliteColumnTypes(t, sqliteDB)
	dialectTypes := dialectColumnTypes(t, ctx, db, dialect)
	for column, sqliteType := range sqliteTypes {
		if !gotColumns[column] {
			continue
		}
		if _, isDerived := derived[column]; isDerived {
			continue
		}
		got := dialectTypes[column]
		switch sqliteType {
		case "":
			// An FTS5 virtual table's columns carry no declared type, so there
			// is no kind for the dialect to preserve.
			continue
		case "INTEGER":
			if want := "bigint"; got != want {
				t.Fatalf("%s column %s is %q, want %s for SQLite's INTEGER", dialect, column, got, want)
			}
		case "TEXT":
			if !isCharacterType(got) {
				t.Fatalf("%s column %s is %q, want a character type for SQLite's TEXT", dialect, column, got)
			}
		default:
			t.Fatalf("schema.sql column %s has type %q, which the dialect comparison does not know", column, sqliteType)
		}
	}
}

// TestSchemaDialectForeignKeysMatchSQLite compares the reference graph. It is
// separate from the table and column comparison because a foreign key can go
// missing without any name changing: MySQL accepts a column-level REFERENCES
// clause and creates nothing, so a dialect schema that wrote its keys beside
// the columns would pass every other check and enforce no cascade at all.
func TestSchemaDialectForeignKeysMatchSQLite(t *testing.T) {
	dialect, db := openDialectStateForTest(t)
	ctx := context.Background()
	sqliteDB, err := Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), nil)
	if err != nil {
		t.Fatalf("open sqlite state database: %v", err)
	}
	defer sqliteDB.Close()

	want := sqliteForeignKeys(t, sqliteDB)
	got := dialectForeignKeys(t, ctx, db, dialect)
	if len(want) == 0 {
		t.Fatal("schema.sql declares no foreign key at all")
	}
	missing, extra := diffSets(want, got)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("%s is missing foreign keys %v and has extra ones %v", dialect, missing, extra)
	}
}

// sqliteForeignKeys reads "child.column->parent.column" for every reference
// schema.sql declares.
func sqliteForeignKeys(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT m.name || '.' || f."from" || '->' || f."table" || '.' || f."to" FROM sqlite_master m JOIN pragma_foreign_key_list(m.name) f WHERE m.type = 'table'`)
	if err != nil {
		t.Fatalf("sqlite foreign keys: %v", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("sqlite foreign keys: %v", err)
		}
		out[key] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("sqlite foreign keys: %v", err)
	}
	return out
}

// dialectForeignKeys reads the same shape from the dialect's catalog. The
// PostgreSQL query pairs each child column with the referenced column by
// ordinal, so a composite key is read the way SQLite reports it.
func dialectForeignKeys(t *testing.T, ctx context.Context, db *sql.DB, dialect dbx.Dialect) map[string]bool {
	t.Helper()
	var query string
	switch dialect {
	case dbx.MySQL:
		query = `SELECT CONCAT(table_name, '.', column_name, '->', referenced_table_name, '.', referenced_column_name) FROM information_schema.key_column_usage WHERE table_schema = DATABASE() AND referenced_table_name IS NOT NULL`
	case dbx.Postgres:
		query = `SELECT kcu.table_name || '.' || kcu.column_name || '->' || ccu.table_name || '.' || ccu.column_name
			FROM information_schema.referential_constraints rc
			JOIN information_schema.key_column_usage kcu
			  ON kcu.constraint_name = rc.constraint_name AND kcu.constraint_schema = rc.constraint_schema
			JOIN information_schema.key_column_usage ccu
			  ON ccu.constraint_name = rc.unique_constraint_name AND ccu.constraint_schema = rc.unique_constraint_schema
			 AND ccu.ordinal_position = kcu.position_in_unique_constraint
			WHERE kcu.table_schema = current_schema()`
	default:
		t.Fatalf("%s has no foreign keys to read", dialect)
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("%v", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("%v", err)
		}
		out[key] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%v", err)
	}
	return out
}

// TestSchemaDialectQuotesReservedWords checks the columns schema.sql names that
// the target engine reserves (design §2.9's last row): each has to be quoted in
// the dialect script, or the engine reads it as syntax. MySQL answers with
// information_schema.keywords, PostgreSQL with pg_get_keywords.
func TestSchemaDialectQuotesReservedWords(t *testing.T) {
	dialect, db := openDialectStateForTest(t)
	ctx := context.Background()

	sqliteDB, err := Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), nil)
	if err != nil {
		t.Fatalf("open sqlite state database: %v", err)
	}
	defer sqliteDB.Close()
	columns := sqliteColumnNames(t, sqliteDB)

	reserved := dialectReservedWords(t, ctx, db, dialect)
	if len(reserved) == 0 {
		t.Fatalf("%s reported no reserved keywords at all", dialect)
	}
	script, err := dialectSchemaFor(dialect)
	if err != nil {
		t.Fatalf("dialect script: %v", err)
	}
	quote := "`"
	if dialect == dbx.Postgres {
		quote = `"`
	}
	var clashes []string
	for _, column := range columns {
		if !reserved[strings.ToLower(column)] {
			continue
		}
		clashes = append(clashes, column)
		if !strings.Contains(script, quote+column+quote) {
			t.Errorf("%s reserves %q and column %s is not quoted as %s%s%s in the dialect script",
				dialect, column, column, quote, column, quote)
		}
	}
	t.Logf("%s reserves %d of schema.sql's column names: %v", dialect, len(clashes), clashes)
	if dialect == dbx.MySQL {
		// The baseline's known clashes (plan 209 §3.4); a dialect script that
		// lost one of them would fail above, but naming them keeps this test
		// honest about what it looked at.
		for _, want := range []string{"call", "key", "trigger"} {
			if !reserved[want] {
				t.Fatalf("MySQL no longer reports %q as reserved, so the quoting check means something else", want)
			}
		}
	}
}

// TestSchemaDialectVersionRow proves the dialect database was stamped with the
// version this binary writes: a fresh one is stamped on creation, and a second
// open finds it current instead of recreating anything.
func TestSchemaDialectVersionRow(t *testing.T) {
	dialect, db := openDialectStateForTest(t)
	version, err := readDialectSchemaVersion(context.Background(), db, dialect)
	if err != nil {
		t.Fatalf("read dialect version: %v", err)
	}
	if version != stateSchemaVersion {
		t.Fatalf("%s state database is at v%d, want v%d", dialect, version, stateSchemaVersion)
	}
}

// openDialectStateForTest opens a fresh state database on the test server and
// reports which dialect it is.
func openDialectStateForTest(t *testing.T) (dbx.Dialect, *sql.DB) {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(testDatabaseURLVar))
	if raw == "" {
		t.Skipf("%s is not set", testDatabaseURLVar)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", testDatabaseURLVar, err)
	}
	var dialect dbx.Dialect
	switch u.Scheme {
	case "mysql":
		dialect = dbx.MySQL
	case "postgres", "postgresql":
		dialect = dbx.Postgres
	default:
		t.Fatalf("%s names an unsupported database type %q", testDatabaseURLVar, u.Scheme)
	}
	db, err := OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open %s state database: %v", dialect, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return dialect, db
}

// sqliteSchema reads the state schema a fresh SQLite database holds.
func sqliteSchema(t *testing.T, db *sql.DB) (tables, columns, indexes map[string]bool) {
	t.Helper()
	tables = stateTables(t, db, "sqlite",
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'fb\_%' ESCAPE '\'`)
	columns = map[string]bool{}
	for _, column := range sqliteColumnList(t, db) {
		columns[column] = true
	}
	indexes = stateTables(t, db, "sqlite",
		`SELECT name FROM sqlite_master WHERE type = 'index' AND name LIKE 'idx\_%' ESCAPE '\'`)
	return tables, columns, indexes
}

// dialectSchema reads the state schema a MySQL or PostgreSQL database holds.
func dialectSchema(t *testing.T, ctx context.Context, db *sql.DB, dialect dbx.Dialect) (tables, columns, indexes map[string]bool) {
	t.Helper()
	var tableQuery, columnQuery, indexQuery string
	switch dialect {
	case dbx.MySQL:
		tableQuery = `SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name LIKE 'fb\_%'`
		columnQuery = `SELECT table_name, column_name FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name LIKE 'fb\_%'`
		indexQuery = `SELECT DISTINCT index_name FROM information_schema.statistics WHERE table_schema = DATABASE() AND index_name LIKE 'idx\_%'`
	case dbx.Postgres:
		tableQuery = `SELECT tablename FROM pg_catalog.pg_tables WHERE schemaname = current_schema() AND tablename LIKE 'fb\_%'`
		columnQuery = `SELECT table_name, column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name LIKE 'fb\_%'`
		indexQuery = `SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND indexname LIKE 'idx\_%'`
	default:
		t.Fatalf("%s has no dialect schema to read", dialect)
	}
	tables = stateTables(t, db, dialect.String(), tableQuery)
	columns = map[string]bool{}
	for _, column := range scanPairs(t, ctx, db, columnQuery) {
		columns[column] = true
	}
	indexes = stateTables(t, db, dialect.String(), indexQuery)
	return tables, columns, indexes
}

// stateTables reads a one-column catalog query into the set of state tables or
// indexes it names, leaving out the tables that are a dialect's mechanism
// rather than part of the schema.
func stateTables(t *testing.T, db *sql.DB, label, query string) map[string]bool {
	t.Helper()
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if dialectOnlyTables[name] || strings.HasPrefix(name, ftsShadowPrefix) {
			continue
		}
		out[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	return out
}

// scanPairs reads a two-column catalog query into "table.column" names.
func scanPairs(t *testing.T, ctx context.Context, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("%v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatalf("%v", err)
		}
		if dialectOnlyTables[table] || strings.HasPrefix(table, ftsShadowPrefix) {
			continue
		}
		out = append(out, table+"."+column)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%v", err)
	}
	return out
}

// sqliteColumnList is every "table.column" schema.sql declares.
func sqliteColumnList(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT m.name, p.name FROM sqlite_master m JOIN pragma_table_info(m.name) p WHERE m.type = 'table' AND m.name LIKE 'fb\_%' ESCAPE '\'`)
	if err != nil {
		t.Fatalf("sqlite columns: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatalf("sqlite columns: %v", err)
		}
		if strings.HasPrefix(table, ftsShadowPrefix) {
			continue
		}
		out = append(out, table+"."+column)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("sqlite columns: %v", err)
	}
	return out
}

// sqliteColumnNames is every column name schema.sql declares, once each.
func sqliteColumnNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, entry := range sqliteColumnList(t, db) {
		name := entry[strings.Index(entry, ".")+1:]
		seen[name] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// sqliteColumnTypes maps "table.column" to the type schema.sql declares.
func sqliteColumnTypes(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT m.name, p.name, p.type FROM sqlite_master m JOIN pragma_table_info(m.name) p WHERE m.type = 'table' AND m.name LIKE 'fb\_%' ESCAPE '\'`)
	if err != nil {
		t.Fatalf("sqlite column types: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var table, column, typ string
		if err := rows.Scan(&table, &column, &typ); err != nil {
			t.Fatalf("sqlite column types: %v", err)
		}
		if strings.HasPrefix(table, ftsShadowPrefix) {
			continue
		}
		out[table+"."+column] = strings.ToUpper(typ)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("sqlite column types: %v", err)
	}
	return out
}

// dialectColumnTypes maps "table.column" to the type the dialect declares.
func dialectColumnTypes(t *testing.T, ctx context.Context, db *sql.DB, dialect dbx.Dialect) map[string]string {
	t.Helper()
	var query string
	switch dialect {
	case dbx.MySQL:
		query = `SELECT table_name, column_name, data_type FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name LIKE 'fb\_%'`
	case dbx.Postgres:
		query = `SELECT table_name, column_name, data_type FROM information_schema.columns WHERE table_schema = current_schema() AND table_name LIKE 'fb\_%'`
	default:
		t.Fatalf("%s has no dialect schema to read", dialect)
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("%v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var table, column, typ string
		if err := rows.Scan(&table, &column, &typ); err != nil {
			t.Fatalf("%v", err)
		}
		out[table+"."+column] = strings.ToLower(typ)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%v", err)
	}
	return out
}

// dialectReservedWords reads the engine's own reserved-word list.
func dialectReservedWords(t *testing.T, ctx context.Context, db *sql.DB, dialect dbx.Dialect) map[string]bool {
	t.Helper()
	var query string
	switch dialect {
	case dbx.MySQL:
		query = `SELECT word FROM information_schema.keywords WHERE reserved = 1`
	case dbx.Postgres:
		query = `SELECT word FROM pg_get_keywords() WHERE catcode = 'R'`
	default:
		t.Fatalf("%s has no reserved-word list to read", dialect)
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("%v", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var word string
		if err := rows.Scan(&word); err != nil {
			t.Fatalf("%v", err)
		}
		out[strings.ToLower(word)] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%v", err)
	}
	return out
}

// isCharacterType reports whether a dialect type holds text, in any of the
// widths MySQL offers.
func isCharacterType(typ string) bool {
	switch typ {
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext":
		return true
	}
	return false
}

// diffSets reports the entries missing from got and the ones got has alone.
func diffSets(want, got map[string]bool) (missing, extra []string) {
	for name := range want {
		if !got[name] {
			missing = append(missing, name)
		}
	}
	for name := range got {
		if !want[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}
