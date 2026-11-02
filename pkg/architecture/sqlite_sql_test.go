package architecture_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/architecture"
)

// TestSQLiteStatementsUnchanged is the guard behind owner ruling 209 §0.2:
// SQLite's statements are not edited while MySQL and PostgreSQL translations
// are added. The snapshot was taken from the tree before any translation work
// started, and every statement still reachable as a literal -- either handed
// to a query method or written as a dbx.SQL's SQLite field -- must be one the
// snapshot already held.
//
// A statement may move (into dbx.SQL{SQLite: …}) without failing this test,
// because the set is compared, not the position. Editing one character of one
// statement fails it, which is the whole point.
func TestSQLiteStatementsUnchanged(t *testing.T) {
	root := findRepoRoot(t)
	got, err := architecture.SQLiteStatements(root)
	if err != nil {
		t.Fatalf("extract SQLite statements: %v", err)
	}
	// Statements assembled at run time are not in the set; the plans that
	// translate them check these sites by hand, so they are named here.
	dynamic, err := architecture.DynamicSQLSites(root)
	if err != nil {
		t.Fatalf("find dynamically built statements: %v", err)
	}
	t.Logf("%d literal statements snapshotted, %d call sites build theirs at run time", len(got), len(dynamic))
	for _, site := range dynamic {
		t.Logf("dynamic statement at %s", site)
	}
	path := filepath.Join(root, "pkg", "architecture", "testdata", "sqlite_sql_snapshot.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot %s: %v", path, err)
	}
	var want []string
	if err := json.Unmarshal(body, &want); err != nil {
		t.Fatalf("decode snapshot %s: %v", path, err)
	}

	present := make(map[string]bool, len(got))
	for _, s := range got {
		present[s] = true
	}
	expected := make(map[string]bool, len(want))
	for _, s := range want {
		expected[s] = true
	}
	changed := false
	for _, s := range want {
		if !present[s] {
			changed = true
			t.Errorf("statement from the snapshot is gone or edited: %q", s)
		}
	}
	for _, s := range got {
		if !expected[s] {
			changed = true
			t.Errorf("statement is not in the snapshot: %q", s)
		}
	}
	if changed {
		t.Fatalf("SQLite statements must stay byte-identical (plan 209 §3.5): %d snapshotted, %d found; "+
			"regenerate with go run ./scripts/sqlite-sql-snapshot only for a deliberate change",
			len(want), len(got))
	}
}
