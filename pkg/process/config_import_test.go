package process

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	homepkg "github.com/forebrain-harness/forebrain-harness/pkg/home"
	openaiauth "github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

// importHomeForTest prepares a scratch home the legacy import runs against.
// The home is pinned because the import resolves ${ENV} references against
// the home's .env and Open's compose merges it.
func importHomeForTest(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	return home
}

func importOpenForTest(t *testing.T, home string) *Environment {
	t.Helper()
	env, err := Open(context.Background(), OpenOptions{
		Home:       home,
		ConfigPath: filepath.Join(home, "forebrain.yaml"),
		LaunchDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	resetProcessWideInstalls(t)
	resetProcessWideInstalls(t)
	return env
}

// importedModelsForTest reads the model table of a home an Open has migrated
// (plan 262).
func importedModelsForTest(t *testing.T, env *Environment) []state.LLMModel {
	t.Helper()
	rows, err := (&state.LLMModelStore{DB: env.SQL}).List(context.Background())
	if err != nil {
		t.Fatalf("list the model table: %v", err)
	}
	return rows
}

// resetProcessWideInstalls clears the process-wide installs Open makes — the
// section validator and the database-backed credential stores — so a later
// test in this binary works against its own home rather than the one this
// test opened (whose temp dir is gone by then).
func resetProcessWideInstalls(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		state.SetSectionValidator(nil)
		openaiauth.SetCredentialsStore(nil)
		opencode.SetCredentialsStore(nil)
		mcp.SetOAuthOverlayStore(nil)
	})
}

func configRowsForTest(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT section, value_json FROM fb_config`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var section, value string
		if err := rows.Scan(&section, &value); err != nil {
			t.Fatal(err)
		}
		out[section] = value
	}
	return out
}

// legacyImportYAML is a file-era configuration: runtime sections (agents,
// hooks, approval_preset) plus the gateway section the file keeps.
const legacyImportYAML = `gateway:
  http_addr: 127.0.0.1:6060
approval_preset: full_access
hooks:
  PreToolUse:
    - matcher: Bash
      hooks:
        - type: command
          command: /bin/true
agents:
  definitions:
    main:
      llm_providers:
        - provider: openai
          model: gpt-import
          api_key: ${FOREBRAIN_IMPORT_TEST_KEY}
          base_url: http://localhost:0/v1
`

// TestImportLegacyConfigRewritesYAMLAndSeedsDB pins the one-time import's
// whole first part: a file-era configuration lands in fb_config, the file is
// rewritten down to its startup sections with a backup of the original, and a
// second startup neither imports again nor rewrites the file again.
func TestImportLegacyConfigRewritesYAMLAndSeedsDB(t *testing.T) {
	t.Setenv("FOREBRAIN_IMPORT_TEST_KEY", "sk-import")
	home := importHomeForTest(t)
	cfgPath := filepath.Join(home, "forebrain.yaml")
	original := []byte(legacyImportYAML)
	if err := os.WriteFile(cfgPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	env := importOpenForTest(t, home)

	rows := configRowsForTest(t, env.SQL)
	for _, section := range []string{"approval_preset", "hooks", "agents"} {
		if _, ok := rows[section]; !ok {
			t.Fatalf("section %s missing from fb_config (rows: %v)", section, rows)
		}
	}
	if got := rows["approval_preset"]; !strings.Contains(got, "full_access") {
		t.Fatalf("approval_preset row = %q", got)
	}

	rewritten, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(rewritten)
	if !strings.Contains(text, "# runtime configuration lives in the state database (fb_config)") {
		t.Fatalf("rewritten file lost its header comment:\n%s", text)
	}
	if !strings.Contains(text, "gateway:") {
		t.Fatalf("rewritten file lost the gateway section:\n%s", text)
	}
	for _, dead := range []string{"agents:", "hooks:", "approval_preset:"} {
		if strings.Contains(text, dead) {
			t.Fatalf("rewritten file still carries runtime section %s:\n%s", dead, text)
		}
	}

	if findBackupForTest(t, home) == "" {
		t.Fatal("no backup file was written")
	}
	backupBytes, err := os.ReadFile(findBackupForTest(t, home))
	if err != nil {
		t.Fatal(err)
	}
	if string(backupBytes) != legacyImportYAML {
		t.Fatalf("backup does not hold the original file verbatim:\n%s", backupBytes)
	}

	// The composed runtime still resolves the agent the import moved: plan 262
	// stored the imported key in the model table and gave the definition that
	// row as its default.
	models := importedModelsForTest(t, env)
	if len(models) != 1 || models[0].APIKey != "sk-import" {
		t.Fatalf("model rows = %+v, want the imported plaintext value", models)
	}
	if got := env.RawConfig.Agents.Definitions["main"].DefaultModel; got != models[0].ID {
		t.Fatalf("composed default_model = %q, want the imported row %q", got, models[0].ID)
	}

	// Second startup: fb_config is no longer empty, so nothing imports again
	// — no second backup, no further rewrite.
	env.Close()
	env2 := importOpenForTest(t, home)
	defer env2.Close()
	if got := len(listBackupsForTest(t, home)); got != 1 {
		t.Fatalf("second startup wrote %d backups, want the original 1", got)
	}
	again, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != text {
		t.Fatalf("second startup rewrote the file again:\n%s", again)
	}
}

func findBackupForTest(t *testing.T, home string) string {
	t.Helper()
	backups := listBackupsForTest(t, home)
	if len(backups) != 1 {
		t.Fatalf("found %d backup files, want 1", len(backups))
	}
	return backups[0]
}

func listBackupsForTest(t *testing.T, home string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(home, "forebrain.yaml.pre-db-*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// TestImportLegacyConfigKeepsEnvReferences pins what the import stores now
// that the home's .env is retired (plan 268): a ${NAME} reference reaches
// fb_config as the reference it is, and the model table is the one place the
// .env still supplies a value to (plan 262's reader runs before the .env is
// renamed aside).
func TestImportLegacyConfigKeepsEnvReferences(t *testing.T) {
	home := importHomeForTest(t)
	if err := os.WriteFile(filepath.Join(homepkg.LegacyEnvPath(home)), []byte(
		"FOREBRAIN_IMPORT_TEST_KEY=sk-legacy-plain\nFOREBRAIN_IMPORT_MCP_HDR=hdr-plain\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(home, "forebrain.yaml")
	body := `gateway:
  http_addr: 127.0.0.1:6060
agents:
  definitions:
    main:
      llm_providers:
        - provider: openai
          model: gpt-import
          api_key: ${FOREBRAIN_IMPORT_TEST_KEY}
          base_url: http://localhost:0/v1
        - provider: openai
          model: gpt-fallback
          api_key: ${FOREBRAIN_IMPORT_NOWHERE}
          base_url: http://localhost:0/v1
      mcp_servers:
        - name: import-srv
          command: /usr/bin/true
          headers:
            x-api-key: ${FOREBRAIN_IMPORT_MCP_HDR}
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	env := importOpenForTest(t, home)
	defer env.Close()

	// A header's reference is stored as the reference: compose-time expansion
	// resolves it from the operator's variables and the process environment,
	// and nothing bakes one machine's value into the database on the way in.
	agents := configRowsForTest(t, env.SQL)["agents"]
	if !strings.Contains(agents, "${FOREBRAIN_IMPORT_MCP_HDR}") {
		t.Fatalf("mcp header reference did not survive in fb_config:\n%s", agents)
	}
	if strings.Contains(agents, "hdr-plain") {
		t.Fatalf("the import baked one machine's value into fb_config:\n%s", agents)
	}
	// The LLM half moved with the models themselves (plan 262): the key the
	// .env resolves is plaintext in the row, and it is nowhere in fb_config.
	models := importedModelsForTest(t, env)
	byModel := map[string]state.LLMModel{}
	for _, row := range models {
		byModel[row.Model] = row
	}
	if got := byModel["gpt-import"].APIKey; got != "sk-legacy-plain" {
		t.Fatalf("row key = %q, want the .env's plaintext value", got)
	}
	if strings.Contains(agents, "sk-legacy-plain") {
		t.Fatalf("the key is still duplicated into fb_config:\n%s", agents)
	}
	// A reference nothing resolves keeps its literal text: the start must not
	// fail over it (that test reaching here is the proof), and the models page
	// shows exactly what is missing (plan 268 revision).
	if got := byModel["gpt-fallback"].APIKey; got != "${FOREBRAIN_IMPORT_NOWHERE}" {
		t.Fatalf("unresolvable reference produced key %q, want the reference as written", got)
	}
	// The .env itself is out of reach after this start (plan 268).
	if _, err := os.Stat(filepath.Join(home, ".env")); !os.IsNotExist(err) {
		t.Fatalf("the home still has a .env: err=%v", err)
	}
}

// TestImportLegacyCredentialsAndMCPOverlay pins the import's other two parts:
// the login files land in the auth.* rows — including a credentials.chatgpt
// override path — and the MCP overlay tree lands in fb_mcp_oauth with the
// real (project_key, server_name) pairs. The source files are left exactly
// as they were.
func TestImportLegacyCredentialsAndMCPOverlay(t *testing.T) {
	t.Setenv("FOREBRAIN_IMPORT_TEST_KEY", "sk-import")
	home := importHomeForTest(t)
	cfgPath := filepath.Join(home, "forebrain.yaml")
	body := `gateway:
  http_addr: 127.0.0.1:6060
credentials:
  chatgpt: custom-auth.json
agents:
  defaults:
    mcp_servers:
      - name: global-srv
        command: /usr/bin/true
  definitions:
    main:
      llm_providers:
        - provider: openai
          model: gpt-import
          api_key: ${FOREBRAIN_IMPORT_TEST_KEY}
          base_url: http://localhost:0/v1
    other:
      llm_providers:
        - provider: openai
          model: gpt-other
          api_key: ${FOREBRAIN_IMPORT_TEST_KEY}
          base_url: http://localhost:0/v1
      mcp_servers:
        - name: agent-srv
          command: /usr/bin/true
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// The login files, one at the overridden path and one at the default.
	chatgptAuth := []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"at","refresh_token":"rt"}}`)
	chatgptPath := filepath.Join(home, "custom-auth.json")
	if err := os.WriteFile(chatgptPath, chatgptAuth, 0o600); err != nil {
		t.Fatal(err)
	}
	opencodeAuth := []byte(`{"access_token":"oat","refresh_token":"ort"}`)
	opencodePath := filepath.Join(home, "opencode.json")
	if err := os.WriteFile(opencodePath, opencodeAuth, 0o600); err != nil {
		t.Fatal(err)
	}

	// A project row, so a project-scoped overlay's namespace can be
	// recovered from the database, plus the overlay files themselves.
	ctx := context.Background()
	db, err := state.Open(ctx, state.StateDBPath(home), nil)
	if err != nil {
		t.Fatal(err)
	}
	projRoot := t.TempDir()
	projKey := state.ProjectKeyForRoot(projRoot)
	projects := state.NewProjectStore(db, "main")
	if _, err := projects.Create(ctx, state.CreateProjectInput{Name: "import-proj", Root: projRoot, ProjectKey: projKey}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	overlayForTest := func(t *testing.T, rel string, doc string) []byte {
		t.Helper()
		p := filepath.Join(home, "state", "mcp-oauth", rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		raw := []byte(doc)
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	globalOverlay := overlayForTest(t, "global-srv.json", `{"access_token":"g"}`)
	agentOverlay := overlayForTest(t, filepath.Join("projects", mcp.OverlaySlug("agent:other"), "agent-srv.json"), `{"access_token":"a"}`)
	projOverlay := overlayForTest(t, filepath.Join("projects", mcp.OverlaySlug(projKey), "proj-srv.json"), `{"access_token":"p"}`)

	env := importOpenForTest(t, home)
	defer env.Close()

	// The credential rows hold the files' bytes verbatim.
	if got := configRowsForTest(t, env.SQL)["auth.chatgpt"]; got != string(chatgptAuth) {
		t.Fatalf("auth.chatgpt row = %q, want the overridden file's bytes", got)
	}
	if got := configRowsForTest(t, env.SQL)["auth.opencode"]; got != string(opencodeAuth) {
		t.Fatalf("auth.opencode row = %q, want the file's bytes", got)
	}

	// The overlay rows carry the recovered pairs.
	rowForTest := func(key, name string) string {
		var value string
		err := env.SQL.QueryRow(dbx.Rebind(dbx.DialectOf(env.SQL), `SELECT value_json FROM fb_mcp_oauth WHERE project_key = ? AND server_name = ?`), key, name).Scan(&value)
		if err != nil {
			t.Fatalf("fb_mcp_oauth (%q, %q): %v", key, name, err)
		}
		return value
	}
	if got := rowForTest("", "global-srv"); got != string(globalOverlay) {
		t.Fatalf("global overlay row = %q", got)
	}
	if got := rowForTest("agent:other", "agent-srv"); got != string(agentOverlay) {
		t.Fatalf("agent overlay row = %q", got)
	}
	if got := rowForTest(projKey, "proj-srv"); got != string(projOverlay) {
		t.Fatalf("project overlay row = %q", got)
	}

	// Every source file is untouched.
	for path, want := range map[string][]byte{
		chatgptPath:  chatgptAuth,
		opencodePath: opencodeAuth,
		filepath.Join(home, "state", "mcp-oauth", "global-srv.json"):                                            globalOverlay,
		filepath.Join(home, "state", "mcp-oauth", "projects", mcp.OverlaySlug("agent:other"), "agent-srv.json"): agentOverlay,
		filepath.Join(home, "state", "mcp-oauth", "projects", mcp.OverlaySlug(projKey), "proj-srv.json"):        projOverlay,
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s was modified by the import", path)
		}
	}
}

// TestImportLegacyConfigSkipsPeersSection pins the database-only rule of the
// peers section (plan 195 §3.1): the one-time import takes the file's other
// runtime sections but never a peers section, whatever the file says — the
// composed switch stays off until a settings surface writes the row.
func TestImportLegacyConfigSkipsPeersSection(t *testing.T) {
	home := importHomeForTest(t)
	cfgPath := filepath.Join(home, "forebrain.yaml")
	raw := "compact:\n  prompt: from-file\npeers:\n  claude_code: true\n"
	if err := os.WriteFile(cfgPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, err := state.Open(ctx, state.StateDBPath(home), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := importLegacyConfigIfNeeded(ctx, db, home, cfgPath); err != nil {
		t.Fatalf("importLegacyConfigIfNeeded: %v", err)
	}
	rows := configRowsForTest(t, db)
	if _, ok := rows["compact"]; !ok {
		t.Fatalf("compact was not imported: rows = %v", rows)
	}
	if _, ok := rows["peers"]; ok {
		t.Fatalf("peers was imported from the file: rows = %v", rows)
	}

	composed, err := composeConfigFrom(ctx, cfgPath, db)
	if err != nil {
		t.Fatalf("composeConfigFrom: %v", err)
	}
	if composed.Peers.ClaudeCode != nil {
		t.Fatalf("composed peers.claude_code = %v, want nil (the file never supplies it)", composed.Peers.ClaudeCode)
	}
}

// TestPeersRowSurvivesAFileCopy pins the other direction of the same rule: a
// row the database holds is the switch, and a later hand edit of the file —
// or a file left over from before the section moved — cannot turn it off.
func TestPeersRowSurvivesAFileCopy(t *testing.T) {
	home := importHomeForTest(t)
	cfgPath := filepath.Join(home, "forebrain.yaml")
	if err := os.WriteFile(cfgPath, []byte(legacyImportYAML+"peers:\n  claude_code: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, err := state.Open(ctx, state.StateDBPath(home), nil)
	if err != nil {
		t.Fatal(err)
	}
	seed := func(section, doc string) {
		t.Helper()
		if err := (&state.ConfigStore{DB: db}).UpdateSection(ctx, section, func(cur json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(doc), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed("peers", `{"claude_code":true}`)
	seed("agents", `{"definitions":{"main":{"llm_providers":[{"api_key":"sk-seeded","base_url":"http://localhost:0/v1","model":"gpt-seeded","provider":"openai"}]}}}`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	env := importOpenForTest(t, home)
	defer env.Close()
	if env.RawConfig.Peers.ClaudeCode == nil || !*env.RawConfig.Peers.ClaudeCode {
		t.Fatalf("composed peers.claude_code = %v, want the database's true (the file's false is dead)", env.RawConfig.Peers.ClaudeCode)
	}
}

// TestImportLegacyConfigSkipsWhenDBHoldsSections pins the idempotence gate
// from the other side: with fb_config already holding rows, a startup leaves
// a file-era configuration completely alone — no import, no rewrite, no
// backup.
func TestImportLegacyConfigSkipsWhenDBHoldsSections(t *testing.T) {
	home := importHomeForTest(t)
	cfgPath := filepath.Join(home, "forebrain.yaml")
	if err := os.WriteFile(cfgPath, []byte(legacyImportYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	// Seed one section row by hand: the database is no longer empty.
	ctx := context.Background()
	db, err := state.Open(ctx, state.StateDBPath(home), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Seed rows by hand — the database is no longer empty, so the import
	// must stand down even though the file still carries runtime sections.
	// Both sections the runtime needs are seeded: with the database the only
	// source, a missing agents row would leave the process with no model.
	seed := func(section, doc string) {
		t.Helper()
		if err := (&state.ConfigStore{DB: db}).UpdateSection(ctx, section, func(cur json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(doc), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed("approval_preset", `"full_access"`)
	seed("agents", `{"definitions":{"main":{"llm_providers":[{"api_key":"sk-seeded","base_url":"http://localhost:0/v1","model":"gpt-seeded","provider":"openai"}]}}}`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	env := importOpenForTest(t, home)
	defer env.Close()

	if got := len(listBackupsForTest(t, home)); got != 0 {
		t.Fatalf("import ran anyway: %d backups exist", got)
	}
	yamlBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(yamlBytes) != legacyImportYAML {
		t.Fatal("the configuration file was rewritten although fb_config was not empty")
	}
	// The database's sections won the compose: the file's preset and its
	// provider did not become the runtime's.
	if env.RawConfig.ApprovalPreset != appcfg.ApprovalPresetFullAccess {
		t.Fatalf("composed approval_preset = %q, want the database's full_access", env.RawConfig.ApprovalPreset)
	}
	if models := importedModelsForTest(t, env); len(models) != 1 || models[0].Model != "gpt-seeded" {
		t.Fatalf("composed model rows = %+v, want the database's gpt-seeded", models)
	}
}

// TestCredentialRowAndFileSettleOnTheNewerCopy pins the switch-point rule of
// plan 188 W12: while both backends were live a token refresh still went to
// the file, so a file modified after its row was written wins on the next
// start — and a file the row has not outrun is left alone, so the rule cannot
// resurrect a stale login.
func TestCredentialRowAndFileSettleOnTheNewerCopy(t *testing.T) {
	home := importHomeForTest(t)
	cfgPath := filepath.Join(home, "forebrain.yaml")
	if err := os.WriteFile(cfgPath, []byte(legacyImportYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(home, "auth.json")
	first := []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"first","refresh_token":"r1"}}`)
	if err := os.WriteFile(authPath, first, 0o600); err != nil {
		t.Fatal(err)
	}

	env := importOpenForTest(t, home)
	if got := configRowsForTest(t, env.SQL)["auth.chatgpt"]; got != string(first) {
		t.Fatalf("auth.chatgpt row = %q, want the file's first login", got)
	}
	env.Close()

	// A refresh written to the file after the row landed: the file is newer,
	// so the next start adopts it.
	second := []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"second","refresh_token":"r2"}}`)
	if err := os.WriteFile(authPath, second, 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Minute)
	if err := os.Chtimes(authPath, future, future); err != nil {
		t.Fatal(err)
	}
	env2 := importOpenForTest(t, home)
	if got := configRowsForTest(t, env2.SQL)["auth.chatgpt"]; got != string(second) {
		t.Fatalf("auth.chatgpt row = %q, want the newer file's login", got)
	}
	env2.Close()

	// The reverse: a stale file left behind after the row moved on must not
	// overwrite it.
	stale := []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"stale","refresh_token":"r0"}}`)
	if err := os.WriteFile(authPath, stale, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(authPath, old, old); err != nil {
		t.Fatal(err)
	}
	env3 := importOpenForTest(t, home)
	defer env3.Close()
	if got := configRowsForTest(t, env3.SQL)["auth.chatgpt"]; got != string(second) {
		t.Fatalf("auth.chatgpt row = %q, want the stored newer login to survive a stale file", got)
	}
}

// TestRewriteMinimalConfigFileLeavesOnlyStartupSections pins the file shape
// the import leaves behind: gateway and credentials, and nothing else — the
// sandbox section's yaml tags carry no omitempty, so marshaling a whole Root
// would drag a zero sandbox_workspace_write block along.
func TestRewriteMinimalConfigFileLeavesOnlyStartupSections(t *testing.T) {
	home := importHomeForTest(t)
	cfgPath := filepath.Join(home, "forebrain.yaml")
	if err := os.WriteFile(cfgPath, []byte(legacyImportYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	persisted, err := loadPersistedDocumentForTest(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rewriteMinimalConfigFile(cfgPath, appcfg.FileRoot{
		Gateway:     persisted.Gateway,
		Credentials: persisted.Credentials,
	}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "gateway:") {
		t.Fatalf("minimal file lost the gateway section:\n%s", text)
	}
	for _, leaked := range []string{"sandbox_workspace_write:", "agents:", "approval_preset:", "hooks:"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("minimal file leaked %s:\n%s", leaked, text)
		}
	}
	// The rewritten file still loads as a configuration.
	if _, err := loadPersistedDocumentForTest(cfgPath); err != nil {
		t.Fatalf("reload minimal file: %v", err)
	}
}

// TestImportMarkerGatesTheFileImport pins plan 207 §3.5's three cases, which
// together replace the old "the whole table is empty" trigger: a file-era home
// with nothing stored imports the file's sections once and takes the marker; a
// home a 187-era binary already imported has rows but no marker, so it takes
// only the marker and leaves its file alone; and a home with the marker
// imports nothing ever again, whatever the file says.
func TestImportMarkerGatesTheFileImport(t *testing.T) {
	const fileEra = `gateway:
  http_addr: 127.0.0.1:6060
compact:
  prompt: file prompt
  auto_compact_percent: 70
agents:
  definitions:
    main:
      llm_providers:
        - provider: openai
          model: gpt-file
          api_key: ${FOREBRAIN_IMPORT_TEST_KEY}
          base_url: http://localhost:0/v1
`
	t.Setenv("FOREBRAIN_IMPORT_TEST_KEY", "sk-import")

	t.Run("file-era home imports once", func(t *testing.T) {
		home := importHomeForTest(t)
		cfgPath := filepath.Join(home, "forebrain.yaml")
		if err := os.WriteFile(cfgPath, []byte(fileEra), 0o600); err != nil {
			t.Fatal(err)
		}
		env := importOpenForTest(t, home)
		defer env.Close()

		rows := configRowsForTest(t, env.SQL)
		if got := rows["compact"]; !strings.Contains(got, "file prompt") || !strings.Contains(got, "70") {
			t.Fatalf("compact row = %s, want the file's values", got)
		}
		// The rest of the section is filled by the defaults, not by the import.
		var compact appcfg.CompactSection
		if err := json.Unmarshal([]byte(rows["compact"]), &compact); err != nil {
			t.Fatal(err)
		}
		if compact.AutoCompactPercent != 70 || compact.Prompt != "file prompt" {
			t.Fatalf("compact = %+v, want the file's values verbatim", compact)
		}
		if _, ok := rows[legacyImportMarkerSection]; !ok {
			t.Fatalf("no import marker was written (rows: %v)", configRowNames(rows))
		}
		// The file is rewritten down to its supported keys, with a backup.
		rewritten, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(rewritten), "compact:") || strings.Contains(string(rewritten), "agents:") {
			t.Fatalf("rewritten file still carries a runtime section:\n%s", rewritten)
		}
		if findBackupForTest(t, home) == "" {
			t.Fatal("no backup of the original file was written")
		}
	})

	t.Run("187-era home only takes the marker", func(t *testing.T) {
		home := importHomeForTest(t)
		cfgPath := filepath.Join(home, "forebrain.yaml")
		if err := os.WriteFile(cfgPath, []byte(fileEra), 0o600); err != nil {
			t.Fatal(err)
		}
		// The rows a 187-era binary left behind, and no marker: the file has
		// already been rewritten in that era, so the file is not a source.
		ctx := context.Background()
		db, err := state.Open(ctx, state.StateDBPath(home), nil)
		if err != nil {
			t.Fatal(err)
		}
		store := &state.ConfigStore{DB: db}
		if err := store.UpdateSection(ctx, "compact", func(json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"prompt":"database prompt","auto_compact_percent":55}`), nil
		}); err != nil {
			t.Fatal(err)
		}
		// A working install holds its model too, or Open has nothing to run.
		if err := store.UpdateSection(ctx, "agents", func(json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"definitions":{"main":{"llm_providers":[{"provider":"openai","model":"gpt-stored","api_key":"sk-stored","base_url":"http://localhost:0/v1"}]}}}`), nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}

		env := importOpenForTest(t, home)
		defer env.Close()

		rows := configRowsForTest(t, env.SQL)
		if got := rows["compact"]; !strings.Contains(got, "database prompt") {
			t.Fatalf("compact row = %s, want the database's value to win", got)
		}
		if _, ok := rows["approval_preset"]; !ok {
			t.Fatalf("no defaults were stored for the 187-era home (rows: %v)", configRowNames(rows))
		}
		if _, ok := rows[legacyImportMarkerSection]; !ok {
			t.Fatal("the marker was not written for an already-imported home")
		}
		if got := len(listBackupsForTest(t, home)); got != 0 {
			t.Fatalf("the import ran again: %d backups exist", got)
		}
		body, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != fileEra {
			t.Fatalf("the file was rewritten although the database already held the configuration:\n%s", body)
		}
	})

	t.Run("marked home ignores the file", func(t *testing.T) {
		home := importHomeForTest(t)
		cfgPath := filepath.Join(home, "forebrain.yaml")
		if err := os.WriteFile(cfgPath, []byte(legacyImportYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		// First startup: imports legacyImportYAML and writes the marker.
		env := importOpenForTest(t, home)
		env.Close()

		// The file then carries a stored section again; the marker says the
		// import is over, so nothing reads it.
		if err := os.WriteFile(cfgPath, []byte(fileEra), 0o600); err != nil {
			t.Fatal(err)
		}
		env2 := importOpenForTest(t, home)
		defer env2.Close()

		rows := configRowsForTest(t, env2.SQL)
		if got := rows["compact"]; strings.Contains(got, "file prompt") {
			t.Fatalf("compact row = %s, want the marked home's stored value to survive", got)
		}
		if got := env2.RawConfig.Compact.Prompt; got == "file prompt" {
			t.Fatalf("the file's compact section reached the runtime after the import: %q", got)
		}
		if got := len(listBackupsForTest(t, home)); got != 1 {
			t.Fatalf("%d backups exist, want only the one the first import wrote", got)
		}
	})
}

func configRowNames(rows map[string]string) []string {
	names := make([]string, 0, len(rows))
	for name := range rows {
		names = append(names, name)
	}
	return names
}

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
