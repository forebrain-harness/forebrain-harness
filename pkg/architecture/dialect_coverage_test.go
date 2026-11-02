package architecture_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/architecture"
)

// TestStatementsHaveDialectArms is the guard behind the owner's ruling that
// MySQL 8 and PostgreSQL 14 are fully supported: every statement the product
// runs against its state database is written once per dialect, in a dbx.SQL
// arm. A statement handed to a query method as a plain literal is SQLite's
// spelling only, and on a server it is a syntax error the tests that happen to
// reach it may never hit.
//
// The allowlist below is the whole exception list, and every entry says why.
func TestStatementsHaveDialectArms(t *testing.T) {
	root := findRepoRoot(t)
	found, err := architecture.UntranslatedStatements(root)
	if err != nil {
		t.Fatalf("scan statements: %v", err)
	}
	for _, site := range found {
		t.Errorf("statement has no MySQL/PostgreSQL arm: %s", site)
	}
	if len(found) > 0 {
		t.Fatalf("%d statements are still SQLite-only; translate them into a dbx.SQL arm", len(found))
	}
}

// Two dialects' arms are worth nothing if one is empty: dbx.SQL.For panics on a
// missing arm at run time, which is a test failure waiting to happen instead.
func TestEveryDBXSQLArmIsFilled(t *testing.T) {
	root := findRepoRoot(t)
	found, err := architecture.UnexplainedEmptyArms(root)
	if err != nil {
		t.Fatalf("scan arms: %v", err)
	}
	for _, site := range found {
		t.Errorf("dbx.SQL without a %s", site)
	}
}

// Every fb_ table the product writes must exist in both dialect schemas: a
// statement translated for MySQL or PostgreSQL is useless against a table that
// schema never declares, and the difference only shows up the first time that
// statement runs on a server.
func TestStateTablesExistInEveryDialectSchema(t *testing.T) {
	root := findRepoRoot(t)
	missing, err := architecture.TablesMissingFromDialectSchemas(root)
	if err != nil {
		t.Fatalf("compare schemas: %v", err)
	}
	for _, entry := range missing {
		t.Errorf("table missing from a dialect schema: %s", entry)
	}
}

// An arm allowance is per file and dialect for the same reason the statement
// allowance is per file: an exemption nobody has any use for any more must not
// quietly cover a new one.
func TestArmAllowanceStillApplies(t *testing.T) {
	root := findRepoRoot(t)
	for _, entry := range architecture.ArmAllowances() {
		if entry.Reason == "" {
			t.Errorf("%s: %s is allowed without a reason", entry.File, entry.Dialect)
		}
		if _, err := os.Stat(filepath.Join(root, entry.File)); err != nil {
			t.Errorf("%s is allowed but does not exist: %v", entry.File, err)
		}
	}
}

// An allowance that names a file covers every raw statement in it, which is how
// a file allowed for one foreign-file read silently covers the next product
// statement someone adds beside it. pkg/state/output_history.go is exactly that
// shape: its store statements are dbx.SQL arms, and only the one-time read of
// the pre-migration history file may keep SQLite's spelling. Its allowance
// therefore carries a predicate, and this is the check that the predicate earns
// the exemption it is given: the legacy statements pass, a planted product
// statement does not.
func TestNarrowedAllowanceCoversOnlyTheStatementsItExplains(t *testing.T) {
	const file = "pkg/state/output_history.go"
	permitted := []string{
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`,
		`PRAGMA table_info(commands)`,
		`SELECT * FROM commands ORDER BY rowid`,
	}
	for _, stmt := range permitted {
		if reason, needs := architecture.UntranslatedStatementReason(file, stmt); needs {
			t.Errorf("%s: the legacy history read is exempt but was reported as %q", stmt, reason)
		}
	}
	planted := `SELECT id, command FROM fb_sessions WHERE id = ?`
	reason, needs := architecture.UntranslatedStatementReason(file, planted)
	if !needs {
		t.Fatalf("a raw fb_ statement planted in an allowed file was not reported; "+
			"the allowance covers the whole file (%s) rather than the read it explains", file)
	}
	if reason == "" {
		t.Error("the planted statement was reported without a reason")
	}
	// The same statement outside the allowance is reported on its own merits,
	// so the check above is about the predicate and not about the statement.
	if _, needs := architecture.UntranslatedStatementReason("pkg/tool/shell_output.go", planted); !needs {
		t.Error("a raw fb_ statement outside the allowance must be reported")
	}
}

// The predicate is a narrowing, not a second allowance: a statement the reason
// does not explain stays reported even though its file is on the list.
func TestNarrowedAllowanceDoesNotCoverADialectSpellingInTheSameFile(t *testing.T) {
	const file = "pkg/state/output_history.go"
	for _, stmt := range []string{
		`SELECT id FROM output_records WHERE IFNULL(kind, '') = ?`,
		`INSERT OR REPLACE INTO output_records (id) VALUES (?)`,
	} {
		if _, needs := architecture.UntranslatedStatementReason(file, stmt); !needs {
			t.Errorf("%s: a SQLite-only spelling in an allowed file was not reported", stmt)
		}
	}
}

// The allowance is a list of files, and an allowance for a file nobody has any
// more is an exemption that quietly covers a new one. Each entry must name a
// file that exists and say, in one line, why it may keep SQLite's spelling —
// and an entry whose predicate narrows it to some of the file's statements has
// to say which ones, because the predicate is the only thing bounding it.
func TestDialectAllowanceStillApplies(t *testing.T) {
	root := findRepoRoot(t)
	allowances := architecture.DialectAllowances()
	if len(allowances) == 0 {
		t.Fatal("the allowance is empty; the raw-statement scan would have to allow nothing")
	}
	seen := map[string]bool{}
	for _, entry := range allowances {
		if entry.Reason == "" {
			t.Errorf("%s is allowed without a reason", entry.File)
		}
		if seen[entry.File] {
			t.Errorf("%s is allowed twice", entry.File)
		}
		seen[entry.File] = true
		if _, err := os.Stat(filepath.Join(root, entry.File)); err != nil {
			t.Errorf("%s is allowed but does not exist: %v", entry.File, err)
		}
	}
}
