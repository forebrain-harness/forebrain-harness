package process

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	homepkg "github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
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
	// Open installs the process-wide section validator over this test's home;
	// a later test in this binary writes sections against its own home, so
	// the install must not outlive the environment that made it.
	t.Cleanup(func() { state.SetSectionValidator(nil) })
	return env
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
// hooks, approval_policy) plus the gateway section the file keeps.
const legacyImportYAML = `gateway:
  http_addr: 127.0.0.1:6060
approval_policy: on-request
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
	for _, section := range []string{"approval_policy", "hooks", "agents"} {
		if _, ok := rows[section]; !ok {
			t.Fatalf("section %s missing from fb_config (rows: %v)", section, rows)
		}
	}
	if got := rows["approval_policy"]; !strings.Contains(got, "on-request") {
		t.Fatalf("approval_policy row = %q", got)
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
	for _, dead := range []string{"agents:", "hooks:", "approval_policy:"} {
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

	// The composed runtime still resolves the agents the import moved: the
	// provider chain Open loaded carries the imported key.
	if got := env.RawConfig.Agents.Definitions["main"].LLMProviders[0].APIKey; got != "sk-import" {
		t.Fatalf("composed api_key = %q, want the imported plaintext value", got)
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

// TestImportLegacyConfigResolvesEnvReferencesToPlaintext pins the owner's
// plaintext-credentials ruling on the import path: a ${NAME} reference the
// home's .env resolves becomes the plaintext value in fb_config; one it
// cannot resolve stays a reference for compose-time expansion.
func TestImportLegacyConfigResolvesEnvReferencesToPlaintext(t *testing.T) {
	home := importHomeForTest(t)
	if err := os.WriteFile(filepath.Join(homepkg.DefaultEnvPath(home)), []byte(
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

	agents := configRowsForTest(t, env.SQL)["agents"]
	if !strings.Contains(agents, "sk-legacy-plain") {
		t.Fatalf("provider key was not resolved to plaintext in fb_config:\n%s", agents)
	}
	if !strings.Contains(agents, "hdr-plain") {
		t.Fatalf("mcp header secret was not resolved to plaintext in fb_config:\n%s", agents)
	}
	if strings.Contains(agents, "${FOREBRAIN_IMPORT_TEST_KEY}") {
		t.Fatalf("resolvable reference survived in fb_config:\n%s", agents)
	}
	if !strings.Contains(agents, "${FOREBRAIN_IMPORT_NOWHERE}") {
		t.Fatalf("unresolvable reference must stay a reference:\n%s", agents)
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
		err := env.SQL.QueryRow(`SELECT value_json FROM fb_mcp_oauth WHERE project_key = ? AND server_name = ?`, key, name).Scan(&value)
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
	seed("approval_policy", `"never"`)
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
	// The database's sections won the compose: the file's on-request policy
	// and its provider did not become the runtime's.
	if env.RawConfig.ApprovalPolicy.Mode != appcfg.ApprovalPolicyNever {
		t.Fatalf("composed approval_policy = %q, want the database's never", env.RawConfig.ApprovalPolicy.Mode)
	}
	if got := env.RawConfig.Agents.Definitions["main"].LLMProviders[0].Model; got != "gpt-seeded" {
		t.Fatalf("composed provider model = %q, want the database's gpt-seeded", got)
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
	persisted, err := appcfg.LoadPersisted(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rewriteMinimalConfigFile(cfgPath, persisted); err != nil {
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
	for _, leaked := range []string{"sandbox_workspace_write:", "agents:", "approval_policy:", "hooks:"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("minimal file leaked %s:\n%s", leaked, text)
		}
	}
	// The rewritten file still loads as a configuration.
	if _, err := appcfg.LoadPersisted(cfgPath); err != nil {
		t.Fatalf("reload minimal file: %v", err)
	}
}
