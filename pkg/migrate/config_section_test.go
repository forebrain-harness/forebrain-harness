package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// seedMigrateSections plants a configuration document's runtime sections as
// fb_config rows, the state a running process leaves behind (plan 188): the
// importer reads and writes the stored sections.
func seedMigrateSections(t *testing.T, db *sql.DB, root appcfg.Root) {
	t.Helper()
	for _, name := range []string{"agents", "hooks", "lsp", "features", "memories", "cron", "permissions", "tools"} {
		raw, err := migrateSectionJSON(root, name)
		if err != nil {
			t.Fatal(err)
		}
		if raw == nil {
			continue
		}
		if _, err := db.Exec(`INSERT INTO fb_config (section, value_json, updated_at_ms) VALUES(?,?,0)
ON CONFLICT(section) DO UPDATE SET value_json = excluded.value_json`, name, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
}

// migrateSectionJSON marshals one Root section the way the write paths store
// it, resolved by the section's yaml name.
func migrateSectionJSON(root appcfg.Root, name string) (json.RawMessage, error) {
	typ := reflect.TypeOf(appcfg.Root{})
	for i := 0; i < typ.NumField(); i++ {
		tag := strings.Split(typ.Field(i).Tag.Get("yaml"), ",")[0]
		if tag != name {
			continue
		}
		field := reflect.ValueOf(root).Field(i)
		if field.IsZero() {
			return nil, nil
		}
		return json.Marshal(field.Interface())
	}
	return nil, nil
}

// migrateSectionRow reads one stored section's raw JSON for an assertion.
func migrateSectionRow(t *testing.T, db *sql.DB, section string) string {
	t.Helper()
	var raw string
	if err := db.QueryRow(`SELECT value_json FROM fb_config WHERE section = ?`, section).Scan(&raw); err != nil {
		t.Fatalf("read stored %s: %v", section, err)
	}
	return raw
}

// openMigrateStateDB opens the scratch state database an import test uses.
func openMigrateStateDB(t *testing.T, home string) *sql.DB {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
