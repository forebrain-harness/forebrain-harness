package process

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

// TestProcessReadsOnEveryDialect is the dialect half of the reads this package
// makes of the state database (plans 210–212): the same statements run against
// SQLite, and against MySQL or PostgreSQL when FOREBRAIN_TEST_DATABASE_URL
// names a server. The rest of the package's tests open SQLite by path, so
// without this they would only ever prove the SQLite arm.
func TestProcessReadsOnEveryDialect(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state", "dialect-reads.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &state.ConfigStore{DB: db}

	// A project row, so the MCP overlay candidates can recover its key.
	root := t.TempDir()
	projectKey := state.ProjectKeyForRoot(root)
	if _, err := state.NewProjectStore(db, "main").Create(ctx, state.CreateProjectInput{
		Name: "dialect-p", Root: root, ProjectKey: projectKey,
	}); err != nil {
		t.Fatal(err)
	}

	// sessionSource reads fb_sessions.source under its tenant.
	sessions := state.NewSessionStore(db, "main")
	if err := sessions.Ensure(ctx, "s-workshop", "s-workshop"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, dbx.Rebind(dbx.DialectOf(db), `UPDATE fb_sessions SET source = 'workshop' WHERE id = 's-workshop'`)); err != nil {
		t.Fatal(err)
	}
	pool := NewRunnerPool(&Environment{SQL: db, Deps: run.Deps{AgentName: "main"}}, 1)
	t.Cleanup(pool.Close)
	source, err := pool.sessionSource(ctx, "s-workshop")
	if err != nil || source != "workshop" {
		t.Fatalf("sessionSource = %q, %v; want workshop", source, err)
	}
	if source, err := pool.sessionSource(ctx, "s-absent"); err != nil || source != "" {
		t.Fatalf("sessionSource of a missing session = %q, %v; want empty", source, err)
	}

	// mcpOverlayCandidates lists the projects whose keys name overlay dirs.
	_, projectKeys, err := mcpOverlayCandidates(ctx, db, appcfg.Root{})
	if err != nil {
		t.Fatalf("mcpOverlayCandidates: %v", err)
	}
	if got := projectKeys[mcp.OverlaySlug(projectKey)]; len(got) != 1 || got[0] != projectKey {
		t.Fatalf("project keys = %v, want %q under its slug", projectKeys, projectKey)
	}

	// The overlay tree's files become fb_mcp_oauth rows: the empty table is
	// what lets the import run, and the row is what proves it stored them.
	overlay := filepath.Join(home, "state", "mcp-oauth", "projects", mcp.OverlaySlug(projectKey), "proj-srv.json")
	if err := os.MkdirAll(filepath.Dir(overlay), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, []byte(`{"access_token":"p"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := importLegacyMCPOAuth(ctx, db, store, home, appcfg.Root{}); err != nil {
		t.Fatalf("importLegacyMCPOAuth: %v", err)
	}
	var stored string
	if err := db.QueryRowContext(ctx, dbx.Rebind(dbx.DialectOf(db), `SELECT value_json FROM fb_mcp_oauth WHERE project_key = ? AND server_name = ?`),
		projectKey, "proj-srv").Scan(&stored); err != nil {
		t.Fatalf("read the imported overlay row: %v", err)
	}
	if stored != `{"access_token":"p"}` {
		t.Fatalf("overlay row = %q", stored)
	}

	// syncCredentialFileRow compares a login file's mtime with its row's
	// stamp, and stores the file when it is newer.
	loginPath := filepath.Join(home, "chatgpt-auth.json")
	if err := os.WriteFile(loginPath, []byte(`{"access_token":"at"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syncCredentialFileRow(ctx, db, store, "auth.chatgpt", loginPath); err != nil {
		t.Fatalf("syncCredentialFileRow: %v", err)
	}
	raw, err := store.LoadCredential(ctx, "auth.chatgpt")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"access_token":"at"}` {
		t.Fatalf("auth.chatgpt row = %q", raw)
	}
	// A row already newer than the file is left alone.
	if err := store.SaveCredential(ctx, "auth.chatgpt", json.RawMessage(`{"access_token":"row"}`)); err != nil {
		t.Fatal(err)
	}
	if err := syncCredentialFileRow(ctx, db, store, "auth.chatgpt", loginPath); err != nil {
		t.Fatalf("syncCredentialFileRow (row newer): %v", err)
	}
	raw, err = store.LoadCredential(ctx, "auth.chatgpt")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"access_token":"row"}` {
		t.Fatalf("a newer row must keep its value, got %q", raw)
	}
}
