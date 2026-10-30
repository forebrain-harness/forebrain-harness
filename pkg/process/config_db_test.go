package process

import (
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
		`INSERT INTO fb_config (section, value_json, updated_at_ms) VALUES(?,?,0)
ON CONFLICT(section) DO UPDATE SET value_json = excluded.value_json`,
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
	storeSectionForTest(t, env, ctx, "approvals_reviewer", `"auto_review"`)

	composed, err := composeConfig(ctx, env)
	if err != nil {
		t.Fatalf("composeConfig: %v", err)
	}
	if composed.Compact.Prompt != "from-db" {
		t.Fatalf("compact.prompt = %q, want the database's value", composed.Compact.Prompt)
	}
	if composed.ApprovalsReviewer != "auto_review" {
		t.Fatalf("approvals_reviewer = %q, want the database's value", composed.ApprovalsReviewer)
	}
	// A runtime section the database does not hold reads as its zero value;
	// the file's copy must not leak back in.
	if composed.SandboxMode != "" {
		t.Fatalf("sandbox_mode = %q, want empty (the file's runtime sections are dead)", composed.SandboxMode)
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
	storeSectionForTest(t, env, ctx, "approval_policy", `{"mode":"bogus"}`)

	if _, err := composeConfig(ctx, env); err == nil {
		t.Fatal("composeConfig accepted an invalid approval_policy, want validateLoadedRoot's rejection")
	} else if !strings.Contains(err.Error(), "invalid approval_policy") {
		t.Fatalf("composeConfig error = %v, want the approval_policy validation message", err)
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
	if err := store.UpdateSection(ctx, "approval_policy", func(cur json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"never"`), nil
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
	if env.RawConfig.ApprovalPolicy.Mode != "never" {
		t.Fatalf("raw config approval_policy = %q, want the database's never", env.RawConfig.ApprovalPolicy.Mode)
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
	// The import stored the ${ENV} reference (this home has no .env) and
	// compose-time expansion resolved it from the process environment.
	if got := composed.Agents.Definitions["main"].LLMProviders[0].APIKey; got != "sk-gate" {
		t.Fatalf("composed api_key = %q, want the process environment's value", got)
	}
	if composed.Gateway.HTTPAddr != "127.0.0.1:6060" {
		t.Fatalf("composed gateway.http_addr = %q, want the file's section", composed.Gateway.HTTPAddr)
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
	if err := store.UpdateSection(ctx, "approval_policy", func(cur json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"never"`), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.SyncConfigFromDB(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if env.RawConfig.ApprovalPolicy.Mode != "never" {
		t.Fatalf("applied approval_policy = %q, want the database's never", env.RawConfig.ApprovalPolicy.Mode)
	}
	// The runtime sections the import moved are still there after the sync:
	// the file holds none of them.
	if got := env.RawConfig.Agents.Definitions["main"].LLMProviders[0].Model; got != "gpt-import" {
		t.Fatalf("reloaded provider model = %q, want the imported gpt-import", got)
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
	// approval_policy is a scalar section: the row holds the mode's own JSON
	// string, the value its Root field marshals to.
	storeSectionForTest(t, env, ctx, "approval_policy", `"on-request"`)

	var invalid *state.InvalidSectionError
	err := env.UpdateRootSection(ctx, "approval_policy", func(cfg *appcfg.Root) error {
		cfg.ApprovalPolicy = appcfg.NewApprovalPolicy("bogus")
		return nil
	})
	if !errors.As(err, &invalid) {
		t.Fatalf("invalid candidate error = %v, want *state.InvalidSectionError", err)
	}
	if !strings.Contains(err.Error(), "approval_policy") {
		t.Fatalf("error = %v, want the loader's own sentence", err)
	}
	var stored string
	if err := env.SQL.QueryRowContext(ctx, `SELECT value_json FROM fb_config WHERE section = 'approval_policy'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != `"on-request"` {
		t.Fatalf("approval_policy after refused write = %s, want the pre-write value", stored)
	}

	// A valid candidate lands, and the next compose reads it back.
	if err := env.UpdateRootSection(ctx, "approval_policy", func(cfg *appcfg.Root) error {
		cfg.ApprovalPolicy = appcfg.NewApprovalPolicy(appcfg.ApprovalPolicyNever)
		return nil
	}); err != nil {
		t.Fatalf("valid candidate: %v", err)
	}
	composed, err := composeConfig(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if composed.ApprovalPolicy.Mode != appcfg.ApprovalPolicyNever {
		t.Fatalf("composed approval_policy = %q, want never", composed.ApprovalPolicy.Mode)
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
