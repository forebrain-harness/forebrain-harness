// The "every statement has a dialect arm" contract (plans 210–212) and the
// "every write takes the write lock" contract (plan 232).
package architecture

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// rawStatement is one SQL statement a product file hands a query method as a
// plain literal, with where it was found.
type rawStatement struct {
	File string
	Line int
	SQL  string
}

// DialectAllowance is one file that may keep raw SQLite statements, and why.
type DialectAllowance struct {
	File   string
	Reason string
	// Exception narrows the allowance to the statements its reason explains,
	// and it is what keeps an allowance from covering a file's next raw
	// statement by accident. Nil means the whole file is exempt — right for a
	// file that holds nothing but the foreign-file read itself; a file that
	// also holds product statements must say which ones it means.
	Exception func(sql string) bool
}

// permits reports whether this allowance covers one raw statement.
func (a DialectAllowance) permits(sql string) bool {
	return a.Exception == nil || a.Exception(sql)
}

// dialectAllowlist names the files whose raw literals are not the product's
// state database in SQLite's spelling, each with the reason it is exempt. A new
// raw statement anywhere else fails TestStatementsHaveDialectArms.
var dialectAllowlist = []DialectAllowance{
	{
		File: "pkg/state/db.go",
		Reason: "asks the linked SQLite engine whether it was built with FTS5, " +
			"on a :memory: handle of its own — the question has no meaning on a server",
	},
	{
		File: "pkg/state/schema_migrations.go",
		Reason: "the historical SQLite migrations: PRAGMA, sqlite_master and per-version " +
			"bodies that only the SQLite executor runs, because a MySQL or PostgreSQL " +
			"database is built from its dialect schema at the current version",
	},
	{
		File: "pkg/state/output_history.go",
		Reason: "beside the tool-output history store — whose own statements are all dbx.SQL " +
			"arms — sits the one-time read of the history file an install had before that table " +
			"moved into the state database: a foreign SQLite file, read once, that the import " +
			"never writes. Only that read is exempt, never the file",
		Exception: func(sql string) bool { return legacyHistoryStatement.MatchString(sql) },
	},
	{
		File: "pkg/migrate/codex.go",
		Reason: "reads the Codex CLI's own state file — a foreign SQLite database the importer " +
			"never writes, opened read-only from a snapshot",
	},
}

// legacyHistoryStatement recognises the statements the one-time read of a
// pre-migration history file runs: it asks a foreign SQLite file about its own
// tables (sqlite_master, PRAGMA) and reads the released `commands` table. It
// names those constructs rather than the function that reads the file, so a raw
// statement added to the same file tomorrow is still reported.
var legacyHistoryStatement = regexp.MustCompile(`(?i)\bsqlite_master\b|\bpragma\b|\bcommands\b`)

// sqliteOnlyMarkers are the constructs that spell a statement for SQLite alone:
// they are what a translated statement had to stop using.
var sqliteOnlyMarkers = []struct {
	Pattern *regexp.Regexp
	Reason  string
}{
	{regexp.MustCompile(`(?i)\bPRAGMA\b`), "PRAGMA"},
	{regexp.MustCompile(`(?i)\bsqlite_master\b`), "sqlite_master"},
	{regexp.MustCompile(`(?i)\bsqlite_(sequence|compileoption_used|version)\b`), "sqlite_ function"},
	{regexp.MustCompile(`(?i)\bpragma_table_info\b`), "pragma_table_info"},
	{regexp.MustCompile(`(?i)\browid\b`), "rowid"},
	{regexp.MustCompile(`(?i)\bIFNULL\s*\(`), "IFNULL("},
	{regexp.MustCompile(`(?i)\bON CONFLICT\b`), "ON CONFLICT"},
	{regexp.MustCompile(`(?i)\bINSERT OR (IGNORE|REPLACE)\b`), "INSERT OR"},
	{regexp.MustCompile(`(?i)\bjson_extract\s*\(`), "json_extract("},
	{regexp.MustCompile(`(?i)\bjson_set\s*\(`), "json_set("},
	{regexp.MustCompile(`(?i)\bbm25\s*\(`), "bm25("},
	{regexp.MustCompile(`(?i)\bVACUUM\b`), "VACUUM"},
	{regexp.MustCompile(`(?i)\bAUTOINCREMENT\b`), "AUTOINCREMENT"},
	{regexp.MustCompile(`(?i)\bjulianday\s*\(`), "julianday("},
	{regexp.MustCompile(`(?i)\bBEGIN IMMEDIATE\b`), "BEGIN IMMEDIATE"},
	{regexp.MustCompile(`(?i)\)\s*STRICT\b`), "STRICT"},
}

var stateTableReference = regexp.MustCompile(`\bfb_[a-z0-9_]+\b`)

// RawSQLStatements reports every product statement that reaches a query method
// as a plain literal, in file order. Statements inside a dbx.SQL arm are
// translated and are not reported; a file under pkg/state/dbx is the dialect
// layer itself.
func RawSQLStatements(root string) ([]rawStatement, error) {
	fset := token.NewFileSet()
	var out []rawStatement
	pkgRoot := filepath.Join(root, "pkg")
	err := filepath.WalkDir(pkgRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel, relErr := filepath.Rel(root, path); relErr == nil && rel == dialectLayer {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isQueryCall(call) {
				return true
			}
			for _, arg := range call.Args {
				lit, ok := arg.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				value, uerr := unquoteLiteral(lit.Value)
				if uerr != nil {
					return true
				}
				out = append(out, rawStatement{
					File: filepath.ToSlash(rel),
					Line: fset.Position(call.Pos()).Line,
					SQL:  value,
				})
				return true
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// UntranslatedStatements reports the raw statements a product file still hands
// a query method for the state database — one that names an fb_ table, or that
// is spelled with a construct only SQLite understands — outside the allowance.
// Each finding is "file:line: reason: statement".
func UntranslatedStatements(root string) ([]string, error) {
	stmts, err := RawSQLStatements(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, stmt := range stmts {
		reason, needs := UntranslatedStatementReason(stmt.File, stmt.SQL)
		if !needs {
			continue
		}
		out = append(out, fmt.Sprintf("%s:%d: %s: %s", stmt.File, stmt.Line, reason, firstSQLWords(stmt.SQL, 12)))
	}
	return out, nil
}

// UntranslatedStatementReason answers for one raw statement: why it needs a
// dialect arm, and whether it does. It is the whole decision UntranslatedStatements
// makes, over a file name and a statement rather than over the tree, so the
// allowance's predicates can be exercised on the statements they claim to
// explain — and on statements they must not cover.
func UntranslatedStatementReason(file, sql string) (reason string, needs bool) {
	for _, entry := range dialectAllowlist {
		if entry.File == file && entry.permits(sql) {
			return "", false
		}
	}
	if marker, ok := sqliteOnlyReason(sql); ok {
		return marker, true
	}
	if stateTableReference.MatchString(sql) {
		return "an fb_ table with no dialect arm", true
	}
	return "", false
}

// DialectAllowances exposes the allowlist to the test, which checks that every
// entry still names a file that exists and carries a reason.
func DialectAllowances() []DialectAllowance {
	out := make([]DialectAllowance, len(dialectAllowlist))
	copy(out, dialectAllowlist)
	return out
}

func sqliteOnlyReason(stmt string) (string, bool) {
	for _, marker := range sqliteOnlyMarkers {
		if marker.Pattern.MatchString(stmt) {
			return marker.Reason, true
		}
	}
	return "", false
}

// firstSQLWords is the statement's opening words, on one line, so a failure
// message names the statement without pasting all of it.
func firstSQLWords(stmt string, words int) string {
	fields := strings.Fields(stmt)
	if len(fields) > words {
		fields = fields[:words]
	}
	return strconv.Quote(strings.Join(fields, " "))
}

// schemaTableNames reads the tables a schema file declares, in either the
// plain or the virtual form.
func schemaTableNames(root, file string) (map[string]bool, error) {
	body, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)
		if !strings.HasPrefix(upper, "CREATE TABLE") && !strings.HasPrefix(upper, "CREATE VIRTUAL TABLE") {
			continue
		}
		fields := strings.Fields(trimmed)
		for index, field := range fields {
			if strings.EqualFold(field, "TABLE") && index+1 < len(fields) {
				name := strings.Trim(fields[index+1], `"`+"`")
				names[name] = true
				break
			}
		}
	}
	return names, nil
}

var writeTargetPattern = regexp.MustCompile(`(?is)\b(?:INSERT\s+(?:OR\s+\w+\s+)?INTO|REPLACE\s+INTO|UPDATE|DELETE\s+FROM)\s+"?([A-Za-z0-9_]+)"?`)

// StateTablesWritten reports every fb_ table a product file writes, with the
// file that writes it first.
func StateTablesWritten(root string) (map[string]string, error) {
	written := map[string]string{}
	record := func(file, stmt string) {
		for _, match := range writeTargetPattern.FindAllStringSubmatch(stmt, -1) {
			table := match[1]
			if !strings.HasPrefix(table, "fb_") {
				continue
			}
			if _, seen := written[table]; !seen {
				written[table] = file
			}
		}
	}
	raw, err := RawSQLStatements(root)
	if err != nil {
		return nil, err
	}
	for _, stmt := range raw {
		record(stmt.File, stmt.SQL)
	}
	arms, err := dbxSQLArms(root)
	if err != nil {
		return nil, err
	}
	for _, arm := range arms {
		record(arm.File, arm.SQL)
	}
	return written, nil
}

// dbxSQLArms reports every statement carried by a dbx.SQL literal, in any
// dialect, so a table only one arm writes is still found.
func dbxSQLArms(root string) ([]rawStatement, error) {
	fset := token.NewFileSet()
	var out []rawStatement
	pkgRoot := filepath.Join(root, "pkg")
	err := filepath.WalkDir(pkgRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isDBXSQLType(lit.Type) {
				return true
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				value, ok := kv.Value.(*ast.BasicLit)
				if !ok || value.Kind != token.STRING {
					continue
				}
				text, uerr := unquoteLiteral(value.Value)
				if uerr != nil {
					continue
				}
				out = append(out, rawStatement{File: filepath.ToSlash(rel), Line: fset.Position(kv.Pos()).Line, SQL: text})
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// dialectOnlyTables are the tables a dialect schema declares that schema.sql
// does not, and why. A table here is not part of the state schema at all: it is
// the engine's own mechanism, so neither dialect is missing it.
var dialectOnlyTables = map[string]string{
	"fb_schema_version": "holds the schema version on the engines that have no PRAGMA user_version; " +
		"SQLite keeps that number in the file header instead",
}

// TablesMissingFromDialectSchemas reports every fb_ table the product writes
// that schema_mysql.sql or schema_postgres.sql does not declare, as
// "table (written by file): missing from schema_x.sql".
func TablesMissingFromDialectSchemas(root string) ([]string, error) {
	sqliteTables, err := schemaTableNames(root, filepath.Join("pkg", "state", "schema.sql"))
	if err != nil {
		return nil, err
	}
	dialects := []struct {
		file   string
		tables map[string]bool
	}{
		{filepath.Join("pkg", "state", "schema_mysql.sql"), nil},
		{filepath.Join("pkg", "state", "schema_postgres.sql"), nil},
	}
	for index := range dialects {
		tables, err := schemaTableNames(root, dialects[index].file)
		if err != nil {
			return nil, err
		}
		dialects[index].tables = tables
	}
	written, err := StateTablesWritten(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for table, file := range written {
		if _, dialectOnly := dialectOnlyTables[table]; dialectOnly {
			continue
		}
		if !sqliteTables[table] {
			out = append(out, fmt.Sprintf("%s (written by %s): not in schema.sql", table, file))
			continue
		}
		for _, dialect := range dialects {
			if !dialect.tables[table] {
				out = append(out, fmt.Sprintf("%s (written by %s): not in %s",
					table, file, filepath.Base(dialect.file)))
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// ArmAllowance is one dbx.SQL variable a dialect may be left out of, and why.
// An empty arm is a deliberate exception — that engine runs a different shape of
// statement, not the same one — and dbx.SQL.For panics if it is ever asked for
// the arm it does not have.
type ArmAllowance struct {
	File    string
	Name    string
	Dialect string
	Reason  string
}

// armAllowlist is the whole exception list for TestEveryDBXSQLArmIsFilled. Each
// entry names one statement, so a new statement in the same file still fails.
var armAllowlist = []ArmAllowance{
	{
		File: "pkg/state/action_service.go", Name: "expirePendingActionsSQL", Dialect: "MySQL",
		Reason: "MySQL has no RETURNING: it reads the matching ids and updates them in the same " +
			"transaction (pendingActionsOlderThanSQL)",
	},
	{
		File: "pkg/state/action_service.go", Name: "pendingActionsOlderThanSQL", Dialect: "SQLite",
		Reason: "the MySQL half of expirePendingActionsSQL; SQLite and PostgreSQL take the ids from RETURNING",
	},
	{
		File: "pkg/state/action_service.go", Name: "pendingActionsOlderThanSQL", Dialect: "Postgres",
		Reason: "the MySQL half of expirePendingActionsSQL; SQLite and PostgreSQL take the ids from RETURNING",
	},
	{
		File: "pkg/state/action_service.go", Name: "expirePendingActionsUpdateSQL", Dialect: "SQLite",
		Reason: "the MySQL half of expirePendingActionsSQL; SQLite and PostgreSQL take the ids from RETURNING",
	},
	{
		File: "pkg/state/action_service.go", Name: "expirePendingActionsUpdateSQL", Dialect: "Postgres",
		Reason: "the MySQL half of expirePendingActionsSQL; SQLite and PostgreSQL take the ids from RETURNING",
	},
	{
		File: "pkg/state/runrt.go", Name: "reapAbandonedRunUpdateSQL", Dialect: "MySQL",
		Reason: "MySQL has no RETURNING: it moves the row and reads the clock back in the same " +
			"transaction (reapAbandonedRunClockMySQLSQL)",
	},
	{
		File: "pkg/state/runrt.go", Name: "reapAbandonedRunUpdateMySQLSQL", Dialect: "SQLite",
		Reason: "the MySQL half of reapAbandonedRunUpdateSQL; the other two engines return the clock",
	},
	{
		File: "pkg/state/runrt.go", Name: "reapAbandonedRunUpdateMySQLSQL", Dialect: "Postgres",
		Reason: "the MySQL half of reapAbandonedRunUpdateSQL; the other two engines return the clock",
	},
	{
		File: "pkg/state/runrt.go", Name: "reapAbandonedRunClockMySQLSQL", Dialect: "SQLite",
		Reason: "the MySQL half of reapAbandonedRunUpdateSQL; the other two engines return the clock",
	},
	{
		File: "pkg/state/runrt.go", Name: "reapAbandonedRunClockMySQLSQL", Dialect: "Postgres",
		Reason: "the MySQL half of reapAbandonedRunUpdateSQL; the other two engines return the clock",
	},
	{
		File: "pkg/state/schema_dialect.go", Name: "fbTableCountSQL", Dialect: "SQLite",
		Reason: "asks a server's own catalogue how many fb_ tables it holds; SQLite reads sqlite_master",
	},
	{
		File: "pkg/state/schema_dialect.go", Name: "schemaVersionReadSQL", Dialect: "SQLite",
		Reason: "SQLite tracks its schema version in PRAGMA user_version; the servers use fb_schema_version",
	},
	{
		File: "pkg/state/schema_dialect.go", Name: "schemaVersionDeleteSQL", Dialect: "SQLite",
		Reason: "SQLite tracks its schema version in PRAGMA user_version; the servers use fb_schema_version",
	},
	{
		File: "pkg/state/schema_dialect.go", Name: "schemaVersionInsertSQL", Dialect: "SQLite",
		Reason: "SQLite tracks its schema version in PRAGMA user_version; the servers use fb_schema_version",
	},
	{
		File: "pkg/memory/jobs.go", Name: "memJobClaimSQL", Dialect: "MySQL",
		Reason: "MySQL's upsert has no WHERE and cannot read the table it writes, so the stage-one " +
			"claim runs a guarded UPDATE and INSERT instead (claimStage1JobMySQL)",
	},
	{
		File: "pkg/state/project_channel_store.go", Name: "projectChannelUpsertSQL", Dialect: "MySQL",
		Reason: "ON DUPLICATE KEY UPDATE fires on the account index too, whose row belongs to another " +
			"project, so MySQL runs an ownership-checked update and insert instead",
	},
}

// ArmAllowances exposes armAllowlist to the test that checks each entry still
// names a file that exists and carries a reason.
func ArmAllowances() []ArmAllowance {
	out := make([]ArmAllowance, len(armAllowlist))
	copy(out, armAllowlist)
	return out
}

// UnexplainedEmptyArms is EmptyDialectArms minus the allowance above.
func UnexplainedEmptyArms(root string) ([]string, error) {
	found, err := EmptyDialectArms(root)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, entry := range armAllowlist {
		allowed[fmt.Sprintf("%s:%s: no %s statement", entry.File, entry.Name, entry.Dialect)] = true
	}
	var out []string
	for _, finding := range found {
		if !allowed[finding] {
			out = append(out, finding)
		}
	}
	return out, nil
}

// EmptyDialectArms reports every dbx.SQL literal that leaves a dialect's
// statement empty, as "file:<name or line>: no <dialect> statement", where name
// is the variable the literal is assigned to.
func EmptyDialectArms(root string) ([]string, error) {
	fset := token.NewFileSet()
	var out []string
	pkgRoot := filepath.Join(root, "pkg")
	err := filepath.WalkDir(pkgRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		check := func(lit *ast.CompositeLit, where string) {
			if !isDBXSQLType(lit.Type) {
				return
			}
			filled := map[string]bool{}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				// Any expression counts as filled — a statement assembled from
				// literals is still a statement; only an empty one is not.
				if value, ok := kv.Value.(*ast.BasicLit); ok && value.Kind == token.STRING {
					if value.Value == `""` || value.Value == "``" {
						continue
					}
				}
				filled[key.Name] = true
			}
			for _, dialect := range []string{"SQLite", "MySQL", "Postgres"} {
				if !filled[dialect] {
					out = append(out, fmt.Sprintf("%s:%s: no %s statement", filepath.ToSlash(rel), where, dialect))
				}
			}
		}
		// A literal assigned to a variable is reported by that variable's name;
		// one written inline is reported by its line.
		named := map[*ast.CompositeLit]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.ValueSpec:
				if len(x.Names) == 1 && len(x.Values) == 1 {
					if lit, ok := x.Values[0].(*ast.CompositeLit); ok {
						named[lit] = true
						check(lit, x.Names[0].Name)
					}
				}
			case *ast.CompositeLit:
				if !named[x] {
					check(x, strconv.Itoa(fset.Position(x.Pos()).Line))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	seen := map[string]bool{}
	unique := out[:0]
	for _, finding := range out {
		if seen[finding] {
			continue
		}
		seen[finding] = true
		unique = append(unique, finding)
	}
	return unique, nil
}

// BareHandleWriteShape is one write a source scan can recognise without type
// information. The statement text is never at the call — it comes from a dbx.SQL
// constant — so the constant a query method is handed, or the handle argument a
// shared writer is given, is what identifies a write.
type BareHandleWriteShape struct {
	// Name is the shape as a reader sees it in a failure message.
	Name string
	// Pattern is what the scan looks for in a source line.
	Pattern *regexp.Regexp
	// Reason says what the shape writes, for the guard's failure message.
	Reason string
}

// bareHandleWriteShapes are the standalone writes plan 232 found running on the
// bare *sql.DB handle. A bare statement takes no write lock on PostgreSQL, so a
// later write can commit ahead of an earlier one: event sequences are what a
// paging reader advances its cursor past, which is why the shapes are worth
// pinning one by one. The list is deliberately this narrow — "any write" needs
// type information a source scan does not have, and a bare handle is also what
// every read is handed.
var bareHandleWriteShapes = []BareHandleWriteShape{
	{
		Name:    `.DB.QueryRowContext(ctx, sessionEventInsertSQL`,
		Pattern: regexp.MustCompile(`\.DB\.QueryRowContext\(ctx, sessionEventInsertSQL\b`),
		Reason:  "the event insert is what allocates the sequence a paging reader follows",
	},
	{
		Name:    `.DB.QueryContext(ctx, expirePendingActionsSQL`,
		Pattern: regexp.MustCompile(`\.DB\.QueryContext\(ctx, expirePendingActionsSQL\b`),
		Reason:  "the expiry sweep moves approval rows",
	},
	{
		Name:    `ensureSession(ctx, s.db`,
		Pattern: regexp.MustCompile(`ensureSession\(ctx, s\.db\b`),
		Reason:  "the session upsert writes the row and the approval posture it is born with",
	},
	{
		Name:    `setMessageVisibility(ctx, s.db`,
		Pattern: regexp.MustCompile(`setMessageVisibility\(ctx, s\.db\b`),
		Reason:  "the visibility move writes the message row",
	},
}

// BareHandleAllowance is one file that may spell a bare-handle write shape
// without it being a write, and why.
type BareHandleAllowance struct {
	File   string
	Reason string
}

// bareHandleAllowlist names the files the scan above must not report. Every
// shape it knows is a write by construction — a read has no business naming
// those statements or those writers — so the one entry is the guard's own
// vocabulary, and a product file added here needs a reason as specific.
var bareHandleAllowlist = []BareHandleAllowance{
	{
		File: "pkg/architecture/dialect_coverage.go",
		Reason: "the guard's own vocabulary: the shapes are written out here as patterns and " +
			"report text, never handed to a database handle",
	},
}

// BareHandleAllowances exposes the allowlist to the test, which checks that
// every entry still names a file that exists and carries a reason.
func BareHandleAllowances() []BareHandleAllowance {
	out := make([]BareHandleAllowance, len(bareHandleAllowlist))
	copy(out, bareHandleAllowlist)
	return out
}

// BareHandleWrites reports every write a product file hands to a bare *sql.DB
// handle, as "file:line: shape (what it writes)". The rule is that a write goes
// through dbx.BeginTx, or dbx.Exec, which takes a transaction of its own: on
// MySQL and PostgreSQL that transaction is what holds the global write lock.
func BareHandleWrites(root string) ([]string, error) {
	var out []string
	pkgRoot := filepath.Join(root, "pkg")
	err := filepath.WalkDir(pkgRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		file := filepath.ToSlash(rel)
		if bareHandleAllowed(file) {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for index, line := range strings.Split(string(body), "\n") {
			for _, shape := range bareHandleWriteShapes {
				if !shape.Pattern.MatchString(line) {
					continue
				}
				out = append(out, fmt.Sprintf("%s:%d: %s (%s)", file, index+1, shape.Name, shape.Reason))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func bareHandleAllowed(file string) bool {
	for _, entry := range bareHandleAllowlist {
		if entry.File == file {
			return true
		}
	}
	return false
}
