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
	state "github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

// dotEnvHomeForTest lays out a home and opens its state database: what the
// migration sees when a pre-plan-268 install starts.
func dotEnvHomeForTest(t *testing.T) (home, cfgPath string, db *sql.DB) {
	t.Helper()
	home = t.TempDir()
	cfgPath = filepath.Join(home, "forebrain.yaml")
	body := `# a hand-written comment the migration must not lose
gateway:
  http_addr: "127.0.0.1:6060"
  auth:
    token: "${FOREBRAIN_GATEWAY_TOKEN}"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	opened, err := state.OpenStateFromHome(context.Background(), home, "")
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	return home, cfgPath, opened
}

func writeLegacyEnvForTest(t *testing.T, home string, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte(body), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
}

func renamedEnvFilesForTest(t *testing.T, home string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(home, ".env.migrated-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return matches
}

func storedVariableForTest(t *testing.T, db *sql.DB, name string) (string, bool) {
	t.Helper()
	vars, err := (&state.EnvVarStore{DB: db}).Map(context.Background())
	if err != nil {
		t.Fatalf("read the variables: %v", err)
	}
	v, ok := vars[name]
	return v, ok
}

// The token a home kept in .env becomes the file's own plaintext value, with
// the file's comments and every other key left as they were.
func TestDotEnvMigrateMovesGatewayTokenIntoTheConfig(t *testing.T) {
	const body = `# a hand-written comment the migration must not lose
gateway:
  http_addr: "127.0.0.1:6060"
  auth:
    token: "${FOREBRAIN_GATEWAY_TOKEN}"
`
	// The name the process environment supplies wins: a container sets it, and
	// the reference is what such a deployment wants to keep.
	t.Run("process environment supplies the token", func(t *testing.T) {
		home, cfgPath, db := dotEnvHomeForTest(t)
		t.Setenv("FOREBRAIN_GATEWAY_TOKEN", "from-env")
		writeLegacyEnvForTest(t, home, "FOREBRAIN_GATEWAY_TOKEN=from-dotenv\n")
		if err := migrateLegacyDotEnv(context.Background(), db, home, cfgPath); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		raw, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		if !strings.Contains(string(raw), "${FOREBRAIN_GATEWAY_TOKEN}") {
			t.Fatalf("the reference a container resolves must stay:\n%s", raw)
		}
		if _, ok := storedVariableForTest(t, db, "FOREBRAIN_GATEWAY_TOKEN"); ok {
			t.Fatal("the gateway token must not also land in the variables table")
		}
	})

	// No environment value: the token moves into the file, in plaintext.
	t.Run("the file takes the value", func(t *testing.T) {
		home, cfgPath, db := dotEnvHomeForTest(t)
		writeLegacyEnvForTest(t, home, "FOREBRAIN_GATEWAY_TOKEN=plain-token-value\n")
		if err := migrateLegacyDotEnv(context.Background(), db, home, cfgPath); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		raw, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		if !strings.Contains(string(raw), "plain-token-value") {
			t.Fatalf("the token did not reach the file:\n%s", raw)
		}
		if strings.Contains(string(raw), "${FOREBRAIN_GATEWAY_TOKEN}") {
			t.Fatalf("the reference survived the migration:\n%s", raw)
		}
		if !strings.Contains(string(raw), "# a hand-written comment the migration must not lose") {
			t.Fatalf("the rewrite dropped the file's comments:\n%s", raw)
		}
		if !strings.Contains(string(raw), `http_addr: "127.0.0.1:6060"`) {
			t.Fatalf("the rewrite lost another key:\n%s", raw)
		}
		loaded, err := appcfg.LoadPersisted(cfgPath)
		if err != nil {
			t.Fatalf("load the migrated config: %v", err)
		}
		if loaded.Gateway.Auth.Token != "plain-token-value" {
			t.Fatalf("stored token = %q", loaded.Gateway.Auth.Token)
		}
		info, err := os.Stat(cfgPath)
		if err != nil {
			t.Fatalf("stat config: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("config mode = %v, want 0600", info.Mode().Perm())
		}
		if _, ok := storedVariableForTest(t, db, "FOREBRAIN_GATEWAY_TOKEN"); ok {
			t.Fatal("the gateway token must not also land in the variables table")
		}
	})

	// Neither side has the value: the file is left exactly as it was.
	t.Run("nothing resolves", func(t *testing.T) {
		home, cfgPath, db := dotEnvHomeForTest(t)
		writeLegacyEnvForTest(t, home, "SOMETHING_ELSE=x\n")
		if err := migrateLegacyDotEnv(context.Background(), db, home, cfgPath); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		raw, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		if !strings.Contains(string(raw), "${FOREBRAIN_GATEWAY_TOKEN}") {
			t.Fatalf("an unresolved token reference must stay a reference:\n%s", raw)
		}
	})
}

// The retired .env merge wrote FOREBRAIN_GATEWAY_TOKEN over gateway.auth.token
// whatever the file held, so the token in effect came from .env also when the
// file's own key named something else. Such a home must keep signing in with
// the token it had: dropping it left the gateway refusing every request.
func TestDotEnvMigrateKeepsTheTokenTheOldMergeForced(t *testing.T) {
	migrated := func(t *testing.T, body, dotenv string) (string, appcfg.Root) {
		t.Helper()
		home, cfgPath, db := dotEnvHomeForTest(t)
		if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		writeLegacyEnvForTest(t, home, dotenv)
		if err := migrateLegacyDotEnv(context.Background(), db, home, cfgPath); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		raw, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		loaded, err := appcfg.LoadPersisted(cfgPath)
		if err != nil {
			t.Fatalf("load the migrated config:\n%s\n%v", raw, err)
		}
		return string(raw), loaded
	}

	// The file references a name nothing ever supplied — a home created under
	// an earlier product name — and .env carries the token under the name the
	// merge read.
	t.Run("a reference to a name nothing supplies", func(t *testing.T) {
		raw, loaded := migrated(t, `# kept
gateway:
  http_addr: "127.0.0.1:6060"
  auth:
    token: ${OLDNAME_GATEWAY_TOKEN}
`, "FOREBRAIN_GATEWAY_TOKEN=token-in-effect\n")
		if loaded.Gateway.Auth.Token != "token-in-effect" {
			t.Fatalf("stored token = %q, want the one .env put in effect:\n%s", loaded.Gateway.Auth.Token, raw)
		}
		if !strings.Contains(raw, "# kept") || !strings.Contains(raw, `http_addr: "127.0.0.1:6060"`) {
			t.Fatalf("the rewrite lost the rest of the file:\n%s", raw)
		}
	})

	t.Run("the second legacy name", func(t *testing.T) {
		_, loaded := migrated(t, "gateway:\n  auth:\n    token: ${OLDNAME_GATEWAY_TOKEN}\n",
			"GATEWAY_TOKEN=second-name\n")
		if loaded.Gateway.Auth.Token != "second-name" {
			t.Fatalf("stored token = %q", loaded.Gateway.Auth.Token)
		}
	})

	t.Run("an empty key", func(t *testing.T) {
		_, loaded := migrated(t, "gateway:\n  http_addr: \"127.0.0.1:6060\"\n  auth:\n    token: \"\"\n",
			"FOREBRAIN_GATEWAY_TOKEN=token-in-effect\n")
		if loaded.Gateway.Auth.Token != "token-in-effect" {
			t.Fatalf("stored token = %q", loaded.Gateway.Auth.Token)
		}
	})

	t.Run("no key at all", func(t *testing.T) {
		raw, loaded := migrated(t, "gateway:\n  http_addr: \"127.0.0.1:6060\"\n",
			"FOREBRAIN_GATEWAY_TOKEN=token-in-effect\n")
		if loaded.Gateway.Auth.Token != "token-in-effect" {
			t.Fatalf("stored token = %q:\n%s", loaded.Gateway.Auth.Token, raw)
		}
		if loaded.Gateway.HTTPAddr != "127.0.0.1:6060" {
			t.Fatalf("http_addr = %q:\n%s", loaded.Gateway.HTTPAddr, raw)
		}
	})

	// A token written into the file in plaintext is the operator's own.
	t.Run("a hand-written token is never overwritten", func(t *testing.T) {
		raw, loaded := migrated(t, "gateway:\n  auth:\n    token: my-own-token\n",
			"FOREBRAIN_GATEWAY_TOKEN=from-dotenv\n")
		if loaded.Gateway.Auth.Token != "my-own-token" {
			t.Fatalf("stored token = %q, want the hand-written one:\n%s", loaded.Gateway.Auth.Token, raw)
		}
	})

	// A reference to another name the process environment supplies is the
	// container arrangement under a name of the operator's choosing.
	t.Run("a reference the environment supplies stays", func(t *testing.T) {
		t.Setenv("MY_GATEWAY_TOKEN", "from-env")
		raw, _ := migrated(t, "gateway:\n  auth:\n    token: ${MY_GATEWAY_TOKEN}\n",
			"FOREBRAIN_GATEWAY_TOKEN=from-dotenv\n")
		if !strings.Contains(raw, "${MY_GATEWAY_TOKEN}") {
			t.Fatalf("the reference the environment resolves must stay:\n%s", raw)
		}
	})
}

// A project channel's secret reference becomes the plaintext the reference
// stood for, and its account identity follows the plaintext.
func TestDotEnvMigrateTurnsChannelSecretsIntoPlaintext(t *testing.T) {
	home, cfgPath, db := dotEnvHomeForTest(t)
	ctx := context.Background()
	project, err := state.NewProjectStore(db, "main").Create(ctx, state.CreateProjectInput{
		Name: "channels", Root: t.TempDir(), ProjectType: state.ProjectTypeCoding,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	insertChannel := func(kind, configJSON, accountKey string) {
		t.Helper()
		if _, err := dbx.Exec(ctx, db, dbx.Rebind(dbx.DialectOf(db), `INSERT INTO fb_project_channels
(project_id, kind, enabled, config_json, account_key, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`),
			project.ID, kind, 1, configJSON, accountKey, 1, 1); err != nil {
			t.Fatalf("insert channel %s: %v", kind, err)
		}
	}
	insertChannel("telegram", `{"bot_token":"${FOREBRAIN_CH_PROJ_TELEGRAM_BOT_TOKEN}","enabled":true}`, "")
	insertChannel("slack", `{"bot_token":"${FOREBRAIN_NOWHERE}","enabled":true}`, "")
	writeLegacyEnvForTest(t, home, "FOREBRAIN_CH_PROJ_TELEGRAM_BOT_TOKEN=123:bot-secret\n")

	if err := migrateLegacyDotEnv(ctx, db, home, cfgPath); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store := state.NewProjectChannelStore(db, "main")
	rows, err := store.List(ctx, project.ID)
	if err != nil {
		t.Fatalf("list channels: %v", err)
	}
	byKind := map[string]state.ProjectChannel{}
	for _, row := range rows {
		byKind[row.Kind] = row
	}
	telegram := byKind["telegram"]
	if !strings.Contains(telegram.ConfigJSON, "123:bot-secret") {
		t.Fatalf("telegram secret was not stored in plaintext: %s", telegram.ConfigJSON)
	}
	if telegram.AccountKey == "" {
		t.Fatal("the account identity was not recomputed from the plaintext")
	}
	// A reference nothing resolves stays a reference: the operator re-enters it.
	if !strings.Contains(byKind["slack"].ConfigJSON, "${FOREBRAIN_NOWHERE}") {
		t.Fatalf("an unresolvable reference must stay: %s", byKind["slack"].ConfigJSON)
	}
}

// The web search and fetch keys move into the agents section, plaintext, and a
// key the operator already configured is left alone.
func TestDotEnvMigrateCarriesSearchKeys(t *testing.T) {
	home, cfgPath, db := dotEnvHomeForTest(t)
	ctx := context.Background()
	store := &state.ConfigStore{DB: db}
	seeded, err := json.Marshal(appcfg.AgentsSection{
		Defaults: appcfg.AgentDefaults{WebSearch: appcfg.WebSearchConfig{
			Brave: &appcfg.WebSearchBrave{APIKey: "already-configured"},
		}},
	})
	if err != nil {
		t.Fatalf("marshal agents: %v", err)
	}
	if err := store.ReplaceAll(ctx, map[string]json.RawMessage{"agents": seeded}); err != nil {
		t.Fatalf("store agents section: %v", err)
	}
	writeLegacyEnvForTest(t, home, strings.Join([]string{
		"TAVILY_API_KEY=tvly-plain",
		"DEEPSEEK_API_KEY=sk-deepseek-plain",
		"BRAVE_SEARCH_API_KEY=sk-brave-from-dotenv",
		"",
	}, "\n"))

	if err := migrateLegacyDotEnv(ctx, db, home, cfgPath); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sections, err := store.LoadSections(ctx)
	if err != nil {
		t.Fatalf("load sections: %v", err)
	}
	var agents appcfg.AgentsSection
	if err := json.Unmarshal(sections["agents"], &agents); err != nil {
		t.Fatalf("decode agents: %v", err)
	}
	if got := agents.Defaults.WebSearch.Tavily; got == nil || got.APIKey != "tvly-plain" {
		t.Fatalf("tavily key = %+v", got)
	}
	if got := agents.Defaults.WebSearch.DeepSeek; got == nil || got.APIKey != "sk-deepseek-plain" {
		t.Fatalf("deepseek search key = %+v", got)
	}
	if got := agents.Defaults.WebFetch.DeepSeek; got == nil || got.APIKey != "sk-deepseek-plain" {
		t.Fatalf("deepseek fetch key = %+v", got)
	}
	if got := agents.Defaults.WebSearch.Brave; got == nil || got.APIKey != "already-configured" {
		t.Fatalf("a configured key must not be overwritten: %+v", got)
	}
	// None of them also lands in the variables table: they were consumed.
	for _, name := range []string{"TAVILY_API_KEY", "DEEPSEEK_API_KEY", "BRAVE_SEARCH_API_KEY"} {
		if _, ok := storedVariableForTest(t, db, name); ok {
			t.Fatalf("%s was consumed by the search keys but also stored as a variable", name)
		}
	}
}

// Everything left over becomes an operator variable; a model provider's own
// variables and an existing variable stay out.
func TestDotEnvMigrateStoresTheRestAsVariables(t *testing.T) {
	home, cfgPath, db := dotEnvHomeForTest(t)
	ctx := context.Background()
	if err := (&state.EnvVarStore{DB: db}).Set(ctx, "GITHUB_TOKEN", "kept-existing"); err != nil {
		t.Fatalf("seed variable: %v", err)
	}
	if _, err := (&state.LLMModelStore{DB: db}).Create(ctx, state.LLMModel{
		Provider: "deepseek", Model: "deepseek-v4", BaseURL: "https://api.deepseek.com/v1",
		APIKey: "sk-row",
	}); err != nil {
		t.Fatalf("seed model row: %v", err)
	}
	writeLegacyEnvForTest(t, home, strings.Join([]string{
		"GITHUB_TOKEN=from-dotenv",
		"TAVILY_API_KEY=tvly",
		"A_WEIRD_NAME=x",
		"EMPTY_VALUE=",
		"DEEPSEEK_API_KEY=sk-deepseek-from-dotenv",
		"DEEPSEEK_BASE_URL=https://example.invalid",
		"OPENAI_API_KEY=sk-openai-no-model-row",
		"",
	}, "\n"))

	if err := migrateLegacyDotEnv(ctx, db, home, cfgPath); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if got, _ := storedVariableForTest(t, db, "GITHUB_TOKEN"); got != "kept-existing" {
		t.Fatalf("an existing variable was overwritten: %q", got)
	}
	if got, _ := storedVariableForTest(t, db, "A_WEIRD_NAME"); got != "x" {
		t.Fatalf("a plain variable did not move into the table: %q", got)
	}
	// A provider the model table holds owns its own variables.
	for _, name := range []string{"DEEPSEEK_API_KEY", "DEEPSEEK_BASE_URL"} {
		if _, ok := storedVariableForTest(t, db, name); ok {
			t.Fatalf("%s belongs to the model table and must not land in the variables", name)
		}
	}
	// A provider the build ships with owns its variable whether or not a row
	// carries it: the environment is never a model key's source.
	if _, ok := storedVariableForTest(t, db, "OPENAI_API_KEY"); ok {
		t.Fatal("a shipped provider's key must not land in the variables")
	}
}

// The file is renamed once its values have a home, and a second start is a
// no-op.
func TestDotEnvMigrateRenamesTheFileAndIsIdempotent(t *testing.T) {
	home, cfgPath, db := dotEnvHomeForTest(t)
	ctx := context.Background()
	writeLegacyEnvForTest(t, home, "GITHUB_TOKEN=ghp-plain\n")
	if err := migrateLegacyDotEnv(ctx, db, home, cfgPath); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".env")); !os.IsNotExist(err) {
		t.Fatalf("the home still has a .env: err=%v", err)
	}
	renamed := renamedEnvFilesForTest(t, home)
	if len(renamed) != 1 {
		t.Fatalf("renamed files = %v, want exactly one", renamed)
	}
	body, err := os.ReadFile(renamed[0])
	if err != nil {
		t.Fatalf("read the renamed file: %v", err)
	}
	if !strings.Contains(string(body), "ghp-plain") {
		t.Fatalf("the renamed file lost its content: %s", body)
	}
	if got, _ := storedVariableForTest(t, db, "GITHUB_TOKEN"); got != "ghp-plain" {
		t.Fatalf("variable = %q", got)
	}

	// Second start: no .env, so nothing happens.
	if err := migrateLegacyDotEnv(ctx, db, home, cfgPath); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if got := len(renamedEnvFilesForTest(t, home)); got != 1 {
		t.Fatalf("renamed files after a second start = %d, want 1", got)
	}
}
