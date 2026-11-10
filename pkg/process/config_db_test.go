package process

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	openaiauth "github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

// composeEnvForTest builds the minimum Environment composeConfig needs: a
// scratch home with a config file and an open state database. The home is
// pinned because the composed load merges the home's .env.
func composeEnvForTest(t *testing.T, yaml string) (*Environment, context.Context) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(home, "forebrain.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(home, "state", "forebrain.state.sqlite"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Environment{Root: home, ConfigPath: cfgPath, SQL: db}, ctx
}

// storeSectionForTest writes one section row directly: the store's write
// path validates candidates, and these tests need to plant exactly the bytes
// they name — valid or not — the way a row left by an earlier state of the
// install reads back.
func storeSectionForTest(t *testing.T, env *Environment, ctx context.Context, section string, doc string) {
	t.Helper()
	if _, err := env.SQL.ExecContext(ctx,
		dbx.Rebind(dbx.DialectOf(env.SQL), `INSERT INTO fb_config (section, value_json, updated_at_ms) VALUES(?,?,0)
ON CONFLICT(section) DO UPDATE SET value_json = excluded.value_json`),
		section, doc); err != nil {
		t.Fatalf("store section %s: %v", section, err)
	}
}

// TestComposeConfigDBSectionOverridesYAML pins the composition's core rule:
// for the runtime sections the database wins. What the file still says about
// them is dead — after the legacy import it says nothing — and a row the
// database holds replaces the file's value wholesale.
func TestComposeConfigDBSectionOverridesYAML(t *testing.T) {
	env, ctx := composeEnvForTest(t, `gateway:
  http_addr: 127.0.0.1:6060
sandbox_mode: read-only
compact:
  prompt: from-yaml
`)
	storeSectionForTest(t, env, ctx, "compact", `{"prompt":"from-db"}`)
	storeSectionForTest(t, env, ctx, "approval_preset", `"full_access"`)

	composed, err := composeConfig(ctx, env)
	if err != nil {
		t.Fatalf("composeConfig: %v", err)
	}
	if composed.Compact.Prompt != "from-db" {
		t.Fatalf("compact.prompt = %q, want the database's value", composed.Compact.Prompt)
	}
	if composed.ApprovalPreset != appcfg.ApprovalPresetFullAccess {
		t.Fatalf("approval_preset = %q, want the database's value", composed.ApprovalPreset)
	}
	// sandbox_mode is no longer a setting at all: the file's copy is dead, and
	// the derived value stays empty until safety.EffectiveConfig runs.
	if composed.SandboxMode != "" {
		t.Fatalf("sandbox_mode = %q, want empty (the file's runtime sections are dead)", composed.SandboxMode)
	}
}

// TestComposeConfigPeersSectionIsDatabaseOnly pins the peers section's split
// (plan 195 §3.1): the row composes into Root, an absent row composes as the
// interop switch off, and the file's own copy of the section is as dead as
// every other runtime section's — the database is the only source.
func TestComposeConfigPeersSectionIsDatabaseOnly(t *testing.T) {
	env, ctx := composeEnvForTest(t, "gateway:\n  http_addr: 127.0.0.1:6060\npeers:\n  claude_code: false\n")
	storeSectionForTest(t, env, ctx, "peers", `{"claude_code":true}`)

	composed, err := composeConfig(ctx, env)
	if err != nil {
		t.Fatalf("composeConfig: %v", err)
	}
	if composed.Peers.ClaudeCode == nil || !*composed.Peers.ClaudeCode {
		t.Fatalf("peers.claude_code = %v, want the database's true (the file's false is dead)", composed.Peers.ClaudeCode)
	}

	bare, bctx := composeEnvForTest(t, "gateway:\n  http_addr: 127.0.0.1:6060\n")
	bareComposed, err := composeConfig(bctx, bare)
	if err != nil {
		t.Fatalf("composeConfig without a peers row: %v", err)
	}
	if bareComposed.Peers.ClaudeCode != nil {
		t.Fatalf("peers.claude_code = %v with no row, want nil (the switch off)", bareComposed.Peers.ClaudeCode)
	}
}

// TestComposeConfigGatewayAlwaysFromYAML pins the other half of the split:
// gateway is a startup section and only the file supplies it — even a stray
// gateway row in the database cannot override the file.
func TestComposeConfigGatewayAlwaysFromYAML(t *testing.T) {
	env, ctx := composeEnvForTest(t, `gateway:
  http_addr: 127.0.0.1:7070
  auth:
    mode: token
    token: ${FOREBRAIN_GATEWAY_TOKEN}
`)
	t.Setenv("FOREBRAIN_GATEWAY_TOKEN", "tok-from-env")
	storeSectionForTest(t, env, ctx, "gateway", `{"http_addr":"127.0.0.1:9999"}`)

	composed, err := composeConfig(ctx, env)
	if err != nil {
		t.Fatalf("composeConfig: %v", err)
	}
	if composed.Gateway.HTTPAddr != "127.0.0.1:7070" {
		t.Fatalf("gateway.http_addr = %q, want the file's value", composed.Gateway.HTTPAddr)
	}
	if composed.Gateway.Auth.Token != "tok-from-env" {
		t.Fatalf("gateway.auth.token = %q, want the ${ENV} reference expanded from the environment", composed.Gateway.Auth.Token)
	}
}

// TestComposeConfigRejectsInvalidSection pins that the composed root runs
// through the same validation a file-loaded one does: a database section
// holding an invalid value is a failed load, not a silently accepted one.
func TestComposeConfigRejectsInvalidSection(t *testing.T) {
	env, ctx := composeEnvForTest(t, "gateway:\n  http_addr: 127.0.0.1:6060\n")
	storeSectionForTest(t, env, ctx, "approval_preset", `"read_only"`)

	if _, err := composeConfig(ctx, env); err == nil {
		t.Fatal("composeConfig accepted an invalid approval_preset, want validateLoadedRoot's rejection")
	} else if !strings.Contains(err.Error(), "approval_preset must be workspace_write or full_access") {
		t.Fatalf("composeConfig error = %v, want the approval_preset validation message", err)
	}
}

// TestComposeConfigKeepsPlaintextSecrets pins the owner's 2026-10-30 ruling
// (plan 187 §1.3): credentials live in the database in plaintext. The
// plaintext-secret scan belongs to the file paths only; a database section
// holding a provider api_key or an MCP header secret is accepted as stored,
// expanded only when it literally references ${ENV}.
func TestComposeConfigKeepsPlaintextSecrets(t *testing.T) {
	env, ctx := composeEnvForTest(t, "gateway:\n  http_addr: 127.0.0.1:6060\n")
	storeSectionForTest(t, env, ctx, "agents", `{
  "defaults": {
    "mcp_servers": [
      {"name": "srv", "command": "run", "headers": {"x-api-key": "hdr-plain"}}
    ]
  },
  "definitions": {
    "main": {
      "llm_providers": [
        {"provider": "deepseek", "model": "m1", "api_key": "sk-plain"}
      ]
    }
  }
}`)

	composed, err := composeConfig(ctx, env)
	if err != nil {
		t.Fatalf("composeConfig rejected a plaintext credential: %v", err)
	}
	prov := composed.Agents.Definitions["main"].LLMProviders
	if len(prov) == 0 || prov[0].APIKey != "sk-plain" {
		t.Fatalf("api_key = %+v, want the plaintext value preserved", prov)
	}
	servers := composed.Agents.Defaults.MCPServers
	if len(servers) == 0 || servers[0].Headers["x-api-key"] != "hdr-plain" {
		t.Fatalf("mcp headers = %+v, want the plaintext value preserved", servers)
	}
}

// TestComposeConfigExpandsEnvReferencesInDBSections pins that ${ENV}
// references keep working for database sections: a user may hand-write one,
// and expansion happens at compose time (plan 187 §1.3).
func TestComposeConfigExpandsEnvReferencesInDBSections(t *testing.T) {
	t.Setenv("FOREBRAIN_COMPOSE_TEST_KEY", "sk-expanded")
	env, ctx := composeEnvForTest(t, "gateway:\n  http_addr: 127.0.0.1:6060\n")
	storeSectionForTest(t, env, ctx, "agents", `{
  "definitions": {
    "main": {
      "llm_providers": [
        {"provider": "deepseek", "model": "m1", "api_key": "${FOREBRAIN_COMPOSE_TEST_KEY}"}
      ]
    }
  }
}`)

	composed, err := composeConfig(ctx, env)
	if err != nil {
		t.Fatalf("composeConfig: %v", err)
	}
	prov := composed.Agents.Definitions["main"].LLMProviders
	if len(prov) == 0 || prov[0].APIKey != "sk-expanded" {
		t.Fatalf("api_key = %+v, want the expanded value", prov)
	}
}

// TestComposeConfigIgnoresCredentialRows pins that the auth.* credential rows
// share fb_config with the runtime sections without being Root fields: they
// must pass through composition untouched, for the credential stores to read.
func TestComposeConfigIgnoresCredentialRows(t *testing.T) {
	env, ctx := composeEnvForTest(t, "gateway:\n  http_addr: 127.0.0.1:6060\n")
	storeSectionForTest(t, env, ctx, "auth.chatgpt", `{"tokens":{"access_token":"a"}}`)

	if _, err := composeConfig(ctx, env); err != nil {
		t.Fatalf("composeConfig stumbled on a credential row: %v", err)
	}
}

// TestRootConfigSectionsMatchRootFields guards the section table itself: a
// name with no Root field is a config the composer cannot apply, and the
// panic at init is better caught here with a readable failure.
func TestRootConfigSectionsMatchRootFields(t *testing.T) {
	if len(rootConfigSectionFields) != len(rootConfigSections) {
		t.Fatalf("section table resolved %d of %d names", len(rootConfigSectionFields), len(rootConfigSections))
	}
	root := reflectRootFields()
	for _, name := range rootConfigSections {
		if _, ok := root[name]; !ok {
			t.Fatalf("section %q has no config.Root field", name)
		}
	}
	for _, banned := range []string{"gateway", "credentials"} {
		if _, ok := rootConfigSectionFields[banned]; ok {
			t.Fatalf("section %q must stay in the file, not the database", banned)
		}
	}
}

func reflectRootFields() map[string]bool {
	out := make(map[string]bool)
	typ := reflect.TypeOf(appcfg.Root{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("yaml"), ",")[0]
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}

// syncEnvForTest opens a full environment on a scratch home whose
// configuration is already in the database: Open seeded the fingerprint of
// the configuration it applied, which is the steady state a turn's sync
// starts from.
func syncEnvForTest(t *testing.T) *Environment {
	t.Helper()
	t.Setenv("FOREBRAIN_IMPORT_TEST_KEY", "sk-sync")
	home := importHomeForTest(t)
	if err := os.WriteFile(filepath.Join(home, "forebrain.yaml"), []byte(legacyImportYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	return importOpenForTest(t, home)
}

// TestSyncConfigFromDBSkipsUnchangedSnapshot pins the steady state: Open
// seeded the fingerprint of the configuration it already applied, so a sync
// with nothing changed is one read-and-compare and applies nothing.
func TestSyncConfigFromDBSkipsUnchangedSnapshot(t *testing.T) {
	env := syncEnvForTest(t)
	defer env.Close()

	applied := 0
	env.OnConfigReload = func(*appcfg.Root) { applied++ }

	for i := 0; i < 3; i++ {
		if err := env.SyncConfigFromDB(context.Background()); err != nil {
			t.Fatalf("sync %d: %v", i, err)
		}
	}
	if applied != 0 {
		t.Fatalf("unchanged configuration was applied %d times, want 0", applied)
	}
}

// TestSyncConfigFromDBAppliesChangedSection pins the change half: a section a
// surface wrote to the database is adopted by the next sync — the full
// application runs once, the fingerprint moves, and the next sync is a no-op
// again.
func TestSyncConfigFromDBAppliesChangedSection(t *testing.T) {
	env := syncEnvForTest(t)
	defer env.Close()

	applied := 0
	env.OnConfigReload = func(*appcfg.Root) { applied++ }
	// The runner's event sink observes the config_reloaded event a changed
	// sync publishes — the same observable a run-event stream consumer has.
	var reloaded int
	env.Runner.Events = event.SinkFunc(func(_ context.Context, evt event.RunEvent) error {
		if evt.Type == event.RunEventConfigReloaded {
			reloaded++
		}
		return nil
	})

	store, err := env.configStore()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// The imported file said full_access; the surface moves it back to the
	// default, which is the change the sync has to see.
	if err := store.UpdateSection(ctx, "approval_preset", func(cur json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"workspace_write"`), nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := env.SyncConfigFromDB(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if applied != 1 {
		t.Fatalf("changed configuration applied %d times, want 1", applied)
	}
	if reloaded != 1 {
		t.Fatalf("config_reloaded events = %d, want 1", reloaded)
	}
	if env.RawConfig.ApprovalPreset != appcfg.ApprovalPresetWorkspaceWrite {
		t.Fatalf("raw config approval_preset = %q, want the database's workspace_write", env.RawConfig.ApprovalPreset)
	}

	// The fingerprint moved with the application: the next sync stays quiet.
	if err := env.SyncConfigFromDB(ctx); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if applied != 1 || reloaded != 1 {
		t.Fatalf("unchanged configuration was applied again (applied=%d, reloaded=%d)", applied, reloaded)
	}
}

// TestSyncConfigFromDBFailsClosed pins the failure semantics: a database the
// composer cannot read a valid configuration from is an error, not a silent
// run on defaults.
func TestSyncConfigFromDBFailsClosed(t *testing.T) {
	env := syncEnvForTest(t)
	defer env.Close()

	if _, err := env.SQL.Exec(`UPDATE fb_config SET value_json = 'not json' WHERE section = 'agents'`); err != nil {
		t.Fatal(err)
	}
	err := env.SyncConfigFromDB(context.Background())
	if err == nil {
		t.Fatal("sync accepted a corrupt section, want an error")
	}
	if !strings.Contains(err.Error(), "agents") {
		t.Fatalf("sync error = %v, want it to name the corrupt section", err)
	}
}

// TestSyncConfigFromDBNoopWithoutCompose pins that an environment which does
// not compose — the OpenOptions.Config bypass — has nothing to sync.
func TestSyncConfigFromDBNoopWithoutCompose(t *testing.T) {
	env := &Environment{ConfigPath: "/tmp/whatever.yaml"}
	if err := env.SyncConfigFromDB(context.Background()); err != nil {
		t.Fatalf("sync on a non-composing environment = %v, want a no-op", err)
	}
}

// TestOpenInstallsDatabaseCredentialStores pins the composition side of the
// credential ports (plan 187 §1.4): after Open, the Layer 0 packages'
// store-based accesses read what the legacy import put in the database — the
// login rows and the MCP OAuth rows — not the files.
func TestOpenInstallsDatabaseCredentialStores(t *testing.T) {
	env := syncEnvForTest(t)
	defer env.Close()

	chatgpt, err := openaiauth.LoadStoredCredentials(context.Background(), env.Root)
	if err != nil {
		t.Fatal(err)
	}
	// This home never had an auth.json, so a file-backed answer would be
	// nil; the database row is absent too, so the installed store answers
	// "no login" — the assertion is that the store answered without a file.
	if chatgpt != nil {
		t.Fatalf("auth.chatgpt = %q, want nil on a home with no login", chatgpt)
	}

	// Write through the port: the row lands in fb_config, not in a file.
	if err := openaiauth.SaveStoredCredentials(context.Background(), env.Root, json.RawMessage(`{"tokens":{"access_token":"db"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(env.Root, "auth.json")); !os.IsNotExist(err) {
		t.Fatal("the credential port wrote a file although Open installed the database store")
	}
	var row string
	if err := env.SQL.QueryRow(`SELECT value_json FROM fb_config WHERE section = 'auth.chatgpt'`).Scan(&row); err != nil {
		t.Fatalf("read the row the port wrote: %v", err)
	}
	if row != `{"tokens":{"access_token":"db"}}` {
		t.Fatalf("auth.chatgpt row = %q", row)
	}

	if err := mcp.SaveStoredOAuth(context.Background(), env.Root, "agent:main", "srv", json.RawMessage(`{"access_token":"o"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(env.Root, "state", "mcp-oauth")); !os.IsNotExist(err) {
		t.Fatal("the oauth port wrote the file tree although Open installed the database store")
	}
	var oauth string
	if err := env.SQL.QueryRow(`SELECT value_json FROM fb_mcp_oauth WHERE project_key = 'agent:main' AND server_name = 'srv'`).Scan(&oauth); err != nil {
		t.Fatalf("read the oauth row the port wrote: %v", err)
	}
	if oauth != `{"access_token":"o"}` {
		t.Fatalf("oauth row = %q", oauth)
	}

	// Restore the file defaults for the rest of the binary: this test's
	// environment closes with the run.
	t.Cleanup(func() {
		openaiauth.SetCredentialsStore(nil)
		opencode.SetCredentialsStore(nil)
		mcp.SetOAuthOverlayStore(nil)
	})
}

// TestComposedStartupConfigTransitionCases pins the accessor the pre-Open
// startup gates use (plan 187): a home whose database holds sections composes
// even though the file was emptied, and a home whose database holds none —
// fresh, or not yet migrated — keeps the file as the answer.
func TestComposedStartupConfigTransitionCases(t *testing.T) {
	t.Setenv("FOREBRAIN_IMPORT_TEST_KEY", "sk-gate")
	home := importHomeForTest(t)
	ctx := context.Background()

	// Fresh home: no database at all.
	if _, ok, err := ComposedStartupConfig(ctx, home); err != nil || ok {
		t.Fatalf("fresh home = (ok=%v, err=%v), want (false, nil)", ok, err)
	}

	// A database that holds no sections (pre-import): still the file's call.
	db, err := state.Open(ctx, state.StateDBPath(home), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ComposedStartupConfig(ctx, home); err != nil || ok {
		t.Fatalf("sectionless database = (ok=%v, err=%v), want (false, nil)", ok, err)
	}

	// A migrated home: the file carries only the startup sections, and the
	// composed answer carries the imported runtime ones.
	if err := os.WriteFile(filepath.Join(home, "forebrain.yaml"), []byte(legacyImportYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	env := importOpenForTest(t, home)
	env.Close()
	composed, ok, err := ComposedStartupConfig(ctx, home)
	if err != nil || !ok {
		t.Fatalf("migrated home = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	// The import stored the ${ENV} reference (this home has no .env); the model
	// migration moved the entry into fb_llm_models and resolved the reference
	// there, and the definition names the row instead of carrying a list.
	if len(composed.LLMModels) != 1 {
		t.Fatalf("model rows = %+v, want the imported model", composed.LLMModels)
	}
	if got := composed.LLMModels[0].APIKey; got != "sk-gate" {
		t.Fatalf("composed api_key = %q, want the process environment's value", got)
	}
	if got := composed.Agents.Definitions["main"].DefaultModel; got != composed.LLMModels[0].ID {
		t.Fatalf("default_model = %q, want the imported row %q", got, composed.LLMModels[0].ID)
	}
	if composed.Gateway.HTTPAddr != "127.0.0.1:6060" {
		t.Fatalf("composed gateway.http_addr = %q, want the file's section", composed.Gateway.HTTPAddr)
	}
}

// startupHomeForTest builds the home the startup gates judge when the database
// is somewhere else: a forebrain.yaml naming that database, which is close to
// all a MySQL or PostgreSQL install keeps in the file (plan 187 moved the rest
// into the database).
func startupHomeForTest(t *testing.T, databaseURL string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "gateway:\n  http_addr: 127.0.0.1:6060\n"
	if databaseURL != "" {
		cfg += "database:\n  url: " + databaseURL + "\n"
	}
	if err := os.WriteFile(filepath.Join(home, "forebrain.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestComposedStartupConfigReportsAnUnreachableServer pins plan 236 step 1: a
// configured server that cannot be reached is a failure an operator has to see,
// never an empty configuration. Before the change every open failure read as
// "no database yet", so the first-run gate sent a configured install back
// through setup and the real error was discarded.
func TestComposedStartupConfigReportsAnUnreachableServer(t *testing.T) {
	home := startupHomeForTest(t, "mysql://fb:s3cret-pw@127.0.0.1:1/forebrain")
	if _, ok, err := ComposedStartupConfig(context.Background(), home); err == nil {
		t.Fatalf("unreachable server = (ok=%v, err=nil), want an error", ok)
	}
}

// TestComposedStartupConfigErrorNamesTheFileNotTheCredentials pins plan 236
// step 3: the message names the configuration file and the engine that failed,
// and never the URL — it may carry the password.
func TestComposedStartupConfigErrorNamesTheFileNotTheCredentials(t *testing.T) {
	const password = "s3cret-pw-236"
	home := startupHomeForTest(t, "mysql://fb:"+password+"@127.0.0.1:1/forebrain")
	_, _, err := ComposedStartupConfig(context.Background(), home)
	if err == nil {
		t.Fatal("unreachable server: want an error")
	}
	msg := err.Error()
	if cfgPath := filepath.Join(home, "forebrain.yaml"); !strings.Contains(msg, cfgPath) {
		t.Fatalf("message %q does not name the config file %s", msg, cfgPath)
	}
	if !strings.Contains(msg, "mysql") {
		t.Fatalf("message %q does not name the engine", msg)
	}
	if strings.Contains(msg, password) {
		t.Fatalf("message %q echoes the password", msg)
	}
}

// TestComposedStartupConfigNeverQuotesTheURL pins plan 243 on the operator's
// path: the same malformed database.url, judged through the startup gate rather
// than the open. Plan 236 made this failure visible — it fails the first-run
// check and is written to the home's log — so the message has to be safe to
// show, and database.url is where the password lives.
func TestComposedStartupConfigNeverQuotesTheURL(t *testing.T) {
	env, ctx := composeEnvForTest(t, "database:\n  url: mysql://user:pw%zz@127.0.0.1:3306/fbt\n")
	_, ok, err := ComposedStartupConfig(ctx, env.Root)
	if err == nil {
		t.Fatalf("a malformed database.url = (ok=%v, err=nil), want an error", ok)
	}
	if strings.Contains(err.Error(), "pw%zz") {
		t.Fatalf("the error quoted the credential: %v", err)
	}
}

// TestComposedStartupConfigKeepsAFreshSQLiteHomeQuiet pins the one fallback that
// is correct (plan 236): a SQLite home with no state file yet is "not
// configured yet", not a failure. The state directory existing is the shape a
// fresh home reaches this gate in — home.Ensure creates it before the first
// setup check.
func TestComposedStartupConfigKeepsAFreshSQLiteHomeQuiet(t *testing.T) {
	ctx := context.Background()
	home := startupHomeForTest(t, "")
	if _, ok, err := ComposedStartupConfig(ctx, home); err != nil || ok {
		t.Fatalf("fresh SQLite home = (ok=%v, err=%v), want (false, nil)", ok, err)
	}

	// A home nothing has created yet: there is no state directory to hold a
	// database, so it is the same "not configured yet".
	bare := startupHomeForTest(t, "")
	if err := os.RemoveAll(filepath.Join(bare, "state")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ComposedStartupConfig(ctx, bare); err != nil || ok {
		t.Fatalf("home with no state directory = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

// TestComposedStartupConfigReportsAnUnreadableSQLiteFile pins the other side of
// that stat: a state file that is there but is not a database is a failure to
// report, not a fresh install. This is the case the missing-file fallback must
// not swallow, and why the fallback is a stat rather than the open's error —
// mattn/go-sqlite3 reports both as one opaque value.
func TestComposedStartupConfigReportsAnUnreadableSQLiteFile(t *testing.T) {
	home := startupHomeForTest(t, "")
	path := state.StateDBPath(home)
	if err := os.WriteFile(path, []byte("these bytes are not a state database\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := ComposedStartupConfig(context.Background(), home)
	if err == nil {
		t.Fatal("unreadable state file: want an error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("message %q does not name the state file %s", err, path)
	}
}

// TestComposedStartupConfigFallsBackOnAnOutdatedSchema pins the transition rule
// plan 236 keeps: a read-only open of a database this binary has not migrated is
// nothing to report — the file is still the answer until the binary is upgraded
// or migration runs.
func TestComposedStartupConfigFallsBackOnAnOutdatedSchema(t *testing.T) {
	home := startupHomeForTest(t, "")
	ctx := context.Background()
	db, err := state.Open(ctx, state.StateDBPath(home), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 0`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// The gate that makes it the transition case rather than a silent misread.
	if _, err := state.Open(ctx, state.StateDBPath(home), &state.OpenOptions{ReadOnly: true}); !errors.Is(err, state.ErrStateSchemaOutdated) {
		t.Fatalf("read-only open = %v, want ErrStateSchemaOutdated", err)
	}
	if _, ok, err := ComposedStartupConfig(ctx, home); err != nil || ok {
		t.Fatalf("outdated schema = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

// TestSyncConfigFromDBComposesFromDatabase pins plan 187 §3.2 from the
// applying side: a turn-boundary sync on a composing environment re-composes —
// the file's startup sections plus the database's runtime sections — so a
// runtime section the database holds survives the sync even though the file
// no longer carries it.
func TestSyncConfigFromDBComposesFromDatabase(t *testing.T) {
	env := syncEnvForTest(t)
	defer env.Close()

	store, err := env.configStore()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.UpdateSection(ctx, "approval_preset", func(cur json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"full_access"`), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.SyncConfigFromDB(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if env.RawConfig.ApprovalPreset != appcfg.ApprovalPresetFullAccess {
		t.Fatalf("applied approval_preset = %q, want the database's full_access", env.RawConfig.ApprovalPreset)
	}
	// The runtime sections the import moved are still there after the sync:
	// the file holds none of them. The model itself is a row (plan 262), and
	// the definition names it.
	if got := appcfg.EffectiveDefaultLLMFor(&env.RawConfig, "main"); got == nil || got.Model != "gpt-import" {
		t.Fatalf("reloaded provider model = %+v, want the imported gpt-import", got)
	}
}

// TestUpdateRootSectionValidatesValueDomains pins the write-side gate plan
// 188 §1 installs: a candidate that would not compose into a loadable
// configuration is refused as *state.InvalidSectionError and the section
// keeps its previous value; a removal that breaks the composed
// configuration is refused the same way.
func TestUpdateRootSectionValidatesValueDomains(t *testing.T) {
	env, ctx := composeEnvForTest(t, "gateway:\n  http_addr: 127.0.0.1:6060\n")
	InstallConfigSectionValidator(env.ConfigPath, env.SQL)
	// approval_preset is a scalar section: the row holds the id's own JSON
	// string, the value its Root field marshals to.
	storeSectionForTest(t, env, ctx, "approval_preset", `"workspace_write"`)

	var invalid *state.InvalidSectionError
	err := env.UpdateRootSection(ctx, "approval_preset", func(cfg *appcfg.Root) error {
		cfg.ApprovalPreset = appcfg.ApprovalPresetID("bogus")
		return nil
	})
	if !errors.As(err, &invalid) {
		t.Fatalf("invalid candidate error = %v, want *state.InvalidSectionError", err)
	}
	if !strings.Contains(err.Error(), "approval_preset") {
		t.Fatalf("error = %v, want the loader's own sentence", err)
	}
	var stored string
	if err := env.SQL.QueryRowContext(ctx, `SELECT value_json FROM fb_config WHERE section = 'approval_preset'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != `"workspace_write"` {
		t.Fatalf("approval_preset after refused write = %s, want the pre-write value", stored)
	}

	// A valid candidate lands, and the next compose reads it back.
	if err := env.UpdateRootSection(ctx, "approval_preset", func(cfg *appcfg.Root) error {
		cfg.ApprovalPreset = appcfg.ApprovalPresetFullAccess
		return nil
	}); err != nil {
		t.Fatalf("valid candidate: %v", err)
	}
	composed, err := composeConfig(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if composed.ApprovalPreset != appcfg.ApprovalPresetFullAccess {
		t.Fatalf("composed approval_preset = %q, want full_access", composed.ApprovalPreset)
	}
}

// TestUpdateRootSectionKeepsPlaintextCredentials pins the owner's ruling the
// write-side validation must not undo: plaintext credentials are legal in
// the database, so a section carrying a provider api_key or an MCP header
// secret in plaintext passes the value-domain validation and is stored as
// sent (plan 187 §1.3, plan 188 §1).
func TestUpdateRootSectionKeepsPlaintextCredentials(t *testing.T) {
	env, ctx := composeEnvForTest(t, "gateway:\n  http_addr: 127.0.0.1:6060\n")
	InstallConfigSectionValidator(env.ConfigPath, env.SQL)
	t.Cleanup(func() { state.SetSectionValidator(nil) })

	err := env.UpdateRootSection(ctx, "agents", func(cfg *appcfg.Root) error {
		if cfg.Agents.Definitions == nil {
			cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{}
		}
		cfg.Agents.Defaults.MCPServers = []appcfg.MCPServerConfig{
			{Name: "srv", Command: "run", Headers: map[string]string{"x-api-key": "hdr-plain"}},
		}
		cfg.Agents.Definitions["main"] = appcfg.AgentDefinition{Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{
			{Provider: "deepseek", Model: "m1", APIKey: "sk-plain"},
		}}
		return nil
	})
	if err != nil {
		t.Fatalf("plaintext credentials refused: %v", err)
	}
	var stored string
	if err := env.SQL.QueryRowContext(ctx, `SELECT value_json FROM fb_config WHERE section = 'agents'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, "sk-plain") || !strings.Contains(stored, "hdr-plain") {
		t.Fatalf("stored agents section lost a plaintext credential: %s", stored)
	}
}

// TestUpdateRootSectionZeroValueRemovesRow pins the storage convention: a
// section whose field is zero after the write reads the same absent as
// stored-empty, so the row is dropped and the table keeps only what the
// install means.
func TestUpdateRootSectionZeroValueRemovesRow(t *testing.T) {
	env, ctx := composeEnvForTest(t, "gateway:\n  http_addr: 127.0.0.1:6060\n")
	InstallConfigSectionValidator(env.ConfigPath, env.SQL)
	t.Cleanup(func() { state.SetSectionValidator(nil) })
	storeSectionForTest(t, env, ctx, "hooks", `{"PreToolUse":[{"matcher":".*","hooks":[{"command":"x"}]}]}`)

	if err := env.UpdateRootSection(ctx, "hooks", func(cfg *appcfg.Root) error {
		cfg.Hooks = nil
		return nil
	}); err != nil {
		t.Fatalf("zero the hooks section: %v", err)
	}
	var n int
	if err := env.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_config WHERE section = 'hooks'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("hooks rows = %d, want 0 (a zero section drops its row)", n)
	}
}

// test-only readers for a whole configuration document.

// loadDocumentForTest reads a whole configuration document — stored sections
// included — the way appcfg.Load read one before plan 207 narrowed forebrain.yaml
// to its supported keys: parse and normalize, then Load's tail (.env merge,
// ${ENV} expansion, validation). A test that assembles a runtime configuration
// in a file goes through here, because the file itself no longer carries those
// sections.
func loadDocumentForTest(path string) (appcfg.Root, error) {
	root, err := loadPersistedDocumentForTest(path)
	if err != nil {
		return appcfg.Root{}, err
	}
	if err := appcfg.FinishLoad(&root); err != nil {
		return appcfg.Root{}, err
	}
	return root, nil
}

// loadPersistedDocumentForTest is the same read without Load's tail: a ${ENV}
// reference stays a reference, as appcfg.LoadPersisted left it.
func loadPersistedDocumentForTest(path string) (appcfg.Root, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return appcfg.Root{}, err
	}
	return appcfg.ParseRootYAML(body)
}

// TestConfigDBComposesModelRows pins the install-wide model table's half of
// the composition (plan 260): fb_llm_models is not a section, so its rows are
// read straight into Root.LLMModels and the view every agent reads starts with
// them.
func TestConfigDBComposesModelRows(t *testing.T) {
	env, ctx := composeEnvForTest(t, "gateway:\n  http_addr: 127.0.0.1:6060\n")
	images, reasoning := true, false
	window := int64(200000)
	row, err := (&state.LLMModelStore{DB: env.SQL}).Create(ctx, state.LLMModel{
		Provider:         "OpenAI",
		Model:            "gpt-4o",
		BaseURL:          "https://table.test/",
		APIPath:          "/responses",
		APIKey:           "row-key",
		ParamsJSON:       `{"temperature":0.2}`,
		CapImages:        &images,
		CapReasoning:     &reasoning,
		CapEfforts:       []string{"low", "high"},
		CapContextWindow: &window,
	})
	if err != nil {
		t.Fatalf("create model row: %v", err)
	}
	composed, err := composeConfig(ctx, env)
	if err != nil {
		t.Fatalf("composeConfig: %v", err)
	}
	if len(composed.LLMModels) != 1 {
		t.Fatalf("LLMModels = %d rows (%+v), want the one stored row", len(composed.LLMModels), composed.LLMModels)
	}
	got := composed.LLMModels[0]
	if got.ID != row.ID || got.Provider != "openai" || got.Model != "gpt-4o" || got.BaseURL != "https://table.test" {
		t.Fatalf("composed row = %+v, want the store's normalized row %s", got, row.ID)
	}
	if got.APIPath != "/responses" || got.APIKey != "row-key" {
		t.Fatalf("composed row endpoint = %+v, want the stored api_path and key", got)
	}
	if string(got.Params) != `{"temperature":0.2}` {
		t.Fatalf("composed params = %q, want the row's params JSON", string(got.Params))
	}
	if got.Caps.Images == nil || !*got.Caps.Images || got.Caps.Reasoning == nil || *got.Caps.Reasoning {
		t.Fatalf("composed caps = %+v, want images true and reasoning false", got.Caps)
	}
	if len(got.Caps.Efforts) != 2 || got.Caps.Efforts[0] != "low" || got.Caps.ContextWindow == nil || *got.Caps.ContextWindow != 200000 {
		t.Fatalf("composed caps = %+v, want the stored levels and context window", got.Caps)
	}
	if view := appcfg.ConfiguredLLMs(&composed); len(view) == 0 || view[0].ID != row.ID {
		t.Fatalf("ConfiguredLLMs = %+v, want the table row first", view)
	}
}

// TestConfigDBFingerprintMovesWithAModelRow pins that a model-page edit
// reaches the next turn: Root.LLMModels is json:"-" and therefore outside the
// root's own encoding, so the fingerprint has to cover it explicitly. Nothing
// changed must still hash the same, or every turn would re-apply the config.
func TestConfigDBFingerprintMovesWithAModelRow(t *testing.T) {
	env, ctx := composeEnvForTest(t, "gateway:\n  http_addr: 127.0.0.1:6060\n")
	row, err := (&state.LLMModelStore{DB: env.SQL}).Create(ctx, state.LLMModel{
		Provider: "openai", Model: "gpt-4o", BaseURL: "https://table.test", APIKey: "first-key",
	})
	if err != nil {
		t.Fatalf("create model row: %v", err)
	}
	fingerprint := func() []byte {
		t.Helper()
		composed, err := composeConfig(ctx, env)
		if err != nil {
			t.Fatalf("composeConfig: %v", err)
		}
		fp, err := configSnapshotFingerprint(composed)
		if err != nil {
			t.Fatalf("configSnapshotFingerprint: %v", err)
		}
		return fp
	}
	before := fingerprint()
	if again := fingerprint(); !bytes.Equal(before, again) {
		t.Fatal("the fingerprint moved with nothing changed; every turn would re-apply the configuration")
	}
	row.APIKey = "second-key"
	if _, err := (&state.LLMModelStore{DB: env.SQL}).Update(ctx, row); err != nil {
		t.Fatalf("update model row: %v", err)
	}
	if after := fingerprint(); bytes.Equal(before, after) {
		t.Fatal("an api_key edit left the fingerprint unchanged; the next turn would run on the old row")
	}
}

// TestConfigDBFingerprintMovesWithSubagentAllowed pins that flipping only the
// authorization switch (owner 2026-11-10) still moves the configuration
// fingerprint: llmModelsFingerprintWire carries the column, so a switch a
// surface flipped reaches the next turn instead of being adopted never.
func TestConfigDBFingerprintMovesWithSubagentAllowed(t *testing.T) {
	env, ctx := composeEnvForTest(t, "gateway:\n  http_addr: 127.0.0.1:6060\n")
	created, err := (&state.LLMModelStore{DB: env.SQL}).Create(ctx, state.LLMModel{
		Provider: "openai", Model: "gpt-4o", BaseURL: "https://table.test", APIKey: "a-key",
	})
	if err != nil {
		t.Fatalf("create model row: %v", err)
	}
	fingerprint := func() []byte {
		t.Helper()
		composed, err := composeConfig(ctx, env)
		if err != nil {
			t.Fatalf("composeConfig: %v", err)
		}
		fp, err := configSnapshotFingerprint(composed)
		if err != nil {
			t.Fatalf("configSnapshotFingerprint: %v", err)
		}
		return fp
	}
	before := fingerprint()
	store := &state.LLMModelStore{DB: env.SQL}
	if err := store.SetSubagentAllowed(ctx, created.ID, true); err != nil {
		t.Fatalf("flip the switch: %v", err)
	}
	if after := fingerprint(); bytes.Equal(before, after) {
		t.Fatal("flipping subagent_allowed left the fingerprint unchanged; the next turn would run on the old row")
	}
}
