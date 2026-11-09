package process

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/modelinfo"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

// migrateEnvForTest is a scratch home with an open state database, the section
// validator installed the way Open installs it before the migration runs, and
// the two sections a pre-table install carries written straight in — a row the
// store's write path would have re-serialized is exactly what the migration has
// to read.
func migrateEnvForTest(t *testing.T, agents, memories string) (*Environment, context.Context) {
	t.Helper()
	env, ctx := composeEnvForTest(t, "approval_policy: \"on-request\"\n")
	if agents != "" {
		storeSectionForTest(t, env, ctx, "agents", agents)
	}
	if memories != "" {
		storeSectionForTest(t, env, ctx, "memories", memories)
	}
	InstallConfigSectionValidator(env.ConfigPath, env.SQL)
	// The validator is process-wide; leaving this test's database installed
	// would point every later test at a closed file.
	t.Cleanup(func() { state.SetSectionValidator(nil) })
	return env, ctx
}

func storedSectionForTest(t *testing.T, env *Environment, ctx context.Context, section string) string {
	t.Helper()
	var raw string
	if err := env.SQL.QueryRowContext(ctx,
		dbx.Rebind(dbx.DialectOf(env.SQL), `SELECT value_json FROM fb_config WHERE section = ?`),
		section).Scan(&raw); err != nil {
		t.Fatalf("read section %s: %v", section, err)
	}
	return raw
}

func storedSectionStampForTest(t *testing.T, env *Environment, ctx context.Context, section string) int64 {
	t.Helper()
	var stamp int64
	if err := env.SQL.QueryRowContext(ctx,
		dbx.Rebind(dbx.DialectOf(env.SQL), `SELECT updated_at_ms FROM fb_config WHERE section = ?`),
		section).Scan(&stamp); err != nil {
		t.Fatalf("read section stamp %s: %v", section, err)
	}
	return stamp
}

func modelTableForTest(t *testing.T, env *Environment, ctx context.Context) []state.LLMModel {
	t.Helper()
	rows, err := (&state.LLMModelStore{DB: env.SQL}).List(ctx)
	if err != nil {
		t.Fatalf("list the model table: %v", err)
	}
	return rows
}

// storedDefinitionForTest reads one agent definition out of the stored agents
// section, as the migration left it.
func storedDefinitionForTest(t *testing.T, env *Environment, ctx context.Context, agent string) appcfg.AgentDefinition {
	t.Helper()
	var agents appcfg.AgentsSection
	if err := json.Unmarshal([]byte(storedSectionForTest(t, env, ctx, "agents")), &agents); err != nil {
		t.Fatalf("read agents section: %v", err)
	}
	return agents.Definitions[agent]
}

func storedMemoriesForTest(t *testing.T, env *Environment, ctx context.Context) appcfg.MemorySection {
	t.Helper()
	var memories appcfg.MemorySection
	if err := json.Unmarshal([]byte(storedSectionForTest(t, env, ctx, "memories")), &memories); err != nil {
		t.Fatalf("read memories section: %v", err)
	}
	return memories
}

func modelRowForTest(t *testing.T, rows []state.LLMModel, provider, model, baseURL string) state.LLMModel {
	t.Helper()
	for _, row := range rows {
		if row.Provider == provider && row.Model == model && row.BaseURL == baseURL {
			return row
		}
	}
	t.Fatalf("no row for %s/%s at %q in %+v", provider, model, baseURL, rows)
	return state.LLMModel{}
}

// Two entries name the same provider and model at different endpoints, and the
// default is written as the label they share: the label can only match the
// first, which is what the runtime resolved before the table existed.
func TestLLMModelsMigrateMovesLegacyEntriesAndKeepsTheFirstDefault(t *testing.T) {
	env, ctx := migrateEnvForTest(t, `{"definitions":{"main":{"primary":true,
		"llm_providers":[
			{"provider":"openai","model":"gpt-x","api_key":"sk-first","base_url":"https://a.example.com/v1"},
			{"provider":"openai","model":"gpt-x","api_key":"sk-second","base_url":"https://b.example.com/v1"}],
		"default_model":"openai/gpt-x"}}}`, "")

	if err := migrateLegacyLLMModels(ctx, env.SQL, env.Root); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	rows := modelTableForTest(t, env, ctx)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(rows), rows)
	}
	for _, row := range rows {
		if !strings.HasPrefix(row.ID, appcfg.LegacyLLMModelIDPrefix) {
			t.Fatalf("row id %q does not carry the legacy prefix", row.ID)
		}
		if row.APIPath != "/chat/completions" {
			t.Fatalf("row %s api_path = %q, want the protocol's default", row.ID, row.APIPath)
		}
	}
	first := modelRowForTest(t, rows, "openai", "gpt-x", "https://a.example.com/v1")
	second := modelRowForTest(t, rows, "openai", "gpt-x", "https://b.example.com/v1")
	if first.ID == second.ID {
		t.Fatal("the two endpoints collapsed into one row")
	}
	if first.APIKey != "sk-first" || second.APIKey != "sk-second" {
		t.Fatalf("keys = %q/%q, want each endpoint's own", first.APIKey, second.APIKey)
	}

	section := storedSectionForTest(t, env, ctx, "agents")
	if strings.Contains(section, "llm_providers") {
		t.Fatalf("the agents section still carries llm_providers: %s", section)
	}
	if got := storedDefinitionForTest(t, env, ctx, "main").DefaultModel; got != first.ID {
		t.Fatalf("default_model = %q, want the first entry's row %q", got, first.ID)
	}
}

// The same model configured on two primary agents is one row, and the key it
// keeps is the one the first definition carried.
func TestLLMModelsMigrateDeduplicatesAcrossAgents(t *testing.T) {
	env, ctx := migrateEnvForTest(t, `{"definitions":{
		"main":{"primary":true,
			"llm_providers":[{"provider":"openai","model":"gpt-x","api_key":"sk-main","base_url":"https://a.example.com/v1"}],
			"default_model":"openai/gpt-x"},
		"review":{"primary":true,
			"llm_providers":[{"provider":"openai","model":"gpt-x","api_key":"sk-review","base_url":"https://a.example.com/v1"}],
			"default_model":"openai/gpt-x"}}}`, "")

	if err := migrateLegacyLLMModels(ctx, env.SQL, env.Root); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	rows := modelTableForTest(t, env, ctx)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(rows), rows)
	}
	if rows[0].APIKey != "sk-main" {
		t.Fatalf("key = %q, want main's entry (the first definition)", rows[0].APIKey)
	}
	if got := storedDefinitionForTest(t, env, ctx, "main").DefaultModel; got != rows[0].ID {
		t.Fatalf("main default_model = %q, want %q", got, rows[0].ID)
	}
	if got := storedDefinitionForTest(t, env, ctx, "review").DefaultModel; got != rows[0].ID {
		t.Fatalf("review default_model = %q, want %q", got, rows[0].ID)
	}
}

// A stored ${NAME} reference becomes the plaintext it stood for — from the
// home's .env first, then the process environment — and a reference that
// resolves to nothing leaves an empty key with a warning that names no value.
func TestLLMModelsMigrateResolvesKeyReferences(t *testing.T) {
	const agents = `{"definitions":{"main":{"primary":true,
		"llm_providers":[{"provider":"openai","model":"gpt-x","api_key":"${FOO_KEY}","base_url":"https://a.example.com/v1"}],
		"default_model":"openai/gpt-x"}}}`

	t.Run("from the legacy dotenv", func(t *testing.T) {
		env, ctx := migrateEnvForTest(t, agents, "")
		if err := os.WriteFile(filepath.Join(env.Root, ".env"), []byte("FOO_KEY=sk-from-dotenv\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("FOO_KEY", "")
		if err := migrateLegacyLLMModels(ctx, env.SQL, env.Root); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		rows := modelTableForTest(t, env, ctx)
		if len(rows) != 1 || rows[0].APIKey != "sk-from-dotenv" {
			t.Fatalf("rows = %+v, want the .env's plaintext", rows)
		}
	})

	t.Run("from the process environment", func(t *testing.T) {
		env, ctx := migrateEnvForTest(t, agents, "")
		t.Setenv("FOO_KEY", "sk-from-env")
		if err := migrateLegacyLLMModels(ctx, env.SQL, env.Root); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		rows := modelTableForTest(t, env, ctx)
		if len(rows) != 1 || rows[0].APIKey != "sk-from-env" {
			t.Fatalf("rows = %+v, want the environment's plaintext", rows)
		}
	})

	// A reference nothing resolves keeps its literal text: an empty key fails
	// the whole start, while `${FOO_KEY}` is exactly what the models page has to
	// show the operator to fix (plan 268 revision).
	t.Run("unresolvable keeps the reference", func(t *testing.T) {
		env, ctx := migrateEnvForTest(t, agents, "")
		t.Setenv("FOO_KEY", "")
		var logged bytes.Buffer
		previous := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
		t.Cleanup(func() { slog.SetDefault(previous) })

		if err := migrateLegacyLLMModels(ctx, env.SQL, env.Root); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		rows := modelTableForTest(t, env, ctx)
		if len(rows) != 1 || rows[0].APIKey != "${FOO_KEY}" {
			t.Fatalf("rows = %+v, want the reference as written", rows)
		}
		text := logged.String()
		if !strings.Contains(text, "did not resolve") {
			t.Fatalf("no warning about the unresolved key: %s", text)
		}
		if strings.Contains(text, "sk-") || strings.Contains(text, "FOO_KEY=") {
			t.Fatalf("the log carries the key: %s", text)
		}
	})

	// A login-backed provider keeps no key at all: its credential is the login
	// file, and a literal ${...} would go out as the bearer token.
	t.Run("unresolvable on a login-backed provider keeps no key", func(t *testing.T) {
		const loginAgents = `{"definitions":{"main":{"primary":true,
			"llm_providers":[{"provider":"opencode","model":"deepseek-v4-pro","api_key":"${OPENCODE_KEY}","base_url":"https://opencode.ai/inference"}],
			"default_model":"opencode/deepseek-v4-pro"}}}`
		env, ctx := migrateEnvForTest(t, loginAgents, "")
		t.Setenv("OPENCODE_KEY", "")
		if err := migrateLegacyLLMModels(ctx, env.SQL, env.Root); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		rows := modelTableForTest(t, env, ctx)
		if len(rows) != 1 || rows[0].APIKey != "" {
			t.Fatalf("rows = %+v, want no key on a login-backed row", rows)
		}
	})
}

// The memory pipeline's models and a subagent's model become row ids; a label
// nothing resolves to is dropped from the subagent and falls back to "current"
// in the memories.
func TestLLMModelsMigrateRewritesSubagentAndMemoryReferences(t *testing.T) {
	env, ctx := migrateEnvForTest(t, `{"definitions":{"main":{"primary":true,
		"llm_providers":[{"provider":"openai","model":"gpt-x","api_key":"sk-1","base_url":"https://a.example.com/v1"}],
		"default_model":"openai/gpt-x",
		"subagents":[{"name":"explore","model":"openai/gpt-x"},{"name":"plan","model":"ghost/model"}]}}}`,
		`{"extract_model":"openai/gpt-x","consolidation_model":"current"}`)

	if err := migrateLegacyLLMModels(ctx, env.SQL, env.Root); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	rows := modelTableForTest(t, env, ctx)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(rows), rows)
	}
	def := storedDefinitionForTest(t, env, ctx, "main")
	if len(def.Subagents) != 2 {
		t.Fatalf("subagents = %+v, want the two entries kept", def.Subagents)
	}
	if def.Subagents[0].Model != rows[0].ID {
		t.Fatalf("explore model = %q, want the row id %q", def.Subagents[0].Model, rows[0].ID)
	}
	if def.Subagents[1].Model != "" {
		t.Fatalf("plan model = %q, want the dangling label dropped", def.Subagents[1].Model)
	}
	memories := storedMemoriesForTest(t, env, ctx)
	if memories.ExtractModel != rows[0].ID {
		t.Fatalf("extract_model = %q, want the row id %q", memories.ExtractModel, rows[0].ID)
	}
	if memories.ConsolidationModel != appcfg.MemoryModelCurrent {
		t.Fatalf("consolidation_model = %q, want %q unchanged", memories.ConsolidationModel, appcfg.MemoryModelCurrent)
	}
}

// Two rows name the same provider and model at different endpoints, and a
// subagent is pinned to the second of them by id. That is the configuration a
// label cannot express, so the migration must leave it alone: rewriting it to
// the first twin would silently move the subagent to another endpoint.
func TestLLMModelsMigrateKeepsASubagentPinnedToItsRowID(t *testing.T) {
	env, ctx := migrateEnvForTest(t, "", "")
	key := "sk-1"
	first, err := UpsertLLMModel(ctx, env.SQL, LLMModelInput{
		Provider: "openai", Model: "gpt-x", BaseURL: "https://a.example.com/v1", APIKey: &key,
	})
	if err != nil {
		t.Fatalf("first twin: %v", err)
	}
	second, err := UpsertLLMModel(ctx, env.SQL, LLMModelInput{
		Provider: "openai", Model: "gpt-x", BaseURL: "https://b.example.com/v1", APIKey: &key,
	})
	if err != nil {
		t.Fatalf("second twin: %v", err)
	}
	storeSectionForTest(t, env, ctx, "agents", `{"definitions":{"main":{"primary":true,`+
		`"default_model":"`+first+`","subagents":[{"name":"explore","model":"`+second+`"}]}}}`)
	stamp := storedSectionStampForTest(t, env, ctx, "agents")

	if err := migrateLegacyLLMModels(ctx, env.SQL, env.Root); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	def := storedDefinitionForTest(t, env, ctx, "main")
	if len(def.Subagents) != 1 || def.Subagents[0].Model != second {
		t.Fatalf("subagent model = %+v, want the twin it was pinned to (%q), not the first (%q)", def.Subagents, second, first)
	}
	if got := storedSectionStampForTest(t, env, ctx, "agents"); got != stamp {
		t.Fatalf("a configuration that already names rows was rewritten (stamp %d -> %d)", stamp, got)
	}
}

// A session's stored selection names the row it was running on, so a resumed
// conversation keeps its model after the legacy lists are gone.
func TestLLMModelsMigrateBackfillsSessionModelIDs(t *testing.T) {
	env, ctx := migrateEnvForTest(t, `{"definitions":{"main":{"primary":true,
		"llm_providers":[{"provider":"openai","model":"gpt-x","api_key":"sk-1","base_url":"https://a.example.com/v1"}],
		"default_model":"openai/gpt-x"}}}`, "")
	for _, stmt := range []string{
		`INSERT INTO fb_sessions (id, agent_id, title, created_at, updated_at) VALUES ('s1','main','s1',1,1)`,
		`INSERT INTO fb_session_model_state (session_id, provider, model, effort, model_id, updated_at)
		 VALUES ('s1','openai','gpt-x','','',1)`,
	} {
		if _, err := env.SQL.ExecContext(ctx, dbx.Rebind(dbx.DialectOf(env.SQL), stmt)); err != nil {
			t.Fatalf("seed session state: %v", err)
		}
	}

	if err := migrateLegacyLLMModels(ctx, env.SQL, env.Root); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	rows := modelTableForTest(t, env, ctx)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(rows), rows)
	}
	var modelID string
	if err := env.SQL.QueryRowContext(ctx,
		dbx.Rebind(dbx.DialectOf(env.SQL), `SELECT model_id FROM fb_session_model_state WHERE session_id = ?`),
		"s1").Scan(&modelID); err != nil {
		t.Fatalf("read session model state: %v", err)
	}
	if modelID != rows[0].ID {
		t.Fatalf("session model_id = %q, want %q", modelID, rows[0].ID)
	}
}

// Running the migration twice changes nothing the second time: the rows are
// reused and the sections are not rewritten.
func TestLLMModelsMigrateIsIdempotent(t *testing.T) {
	env, ctx := migrateEnvForTest(t, `{"definitions":{"main":{"primary":true,
		"llm_providers":[{"provider":"openai","model":"gpt-x","api_key":"sk-1","base_url":"https://a.example.com/v1"}],
		"default_model":"openai/gpt-x"}}}`,
		`{"extract_model":"openai/gpt-x"}`)

	if err := migrateLegacyLLMModels(ctx, env.SQL, env.Root); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	rows := modelTableForTest(t, env, ctx)
	agents := storedSectionForTest(t, env, ctx, "agents")
	memories := storedSectionForTest(t, env, ctx, "memories")
	agentsStamp := storedSectionStampForTest(t, env, ctx, "agents")
	memoriesStamp := storedSectionStampForTest(t, env, ctx, "memories")

	if err := migrateLegacyLLMModels(ctx, env.SQL, env.Root); err != nil {
		t.Fatalf("second migration: %v", err)
	}
	if again := modelTableForTest(t, env, ctx); len(again) != len(rows) {
		t.Fatalf("rows after the second run = %d, want %d", len(again), len(rows))
	}
	if got := storedSectionForTest(t, env, ctx, "agents"); got != agents {
		t.Fatalf("the agents section was rewritten by a second run:\n%s\n%s", agents, got)
	}
	if got := storedSectionForTest(t, env, ctx, "memories"); got != memories {
		t.Fatalf("the memories section was rewritten by a second run:\n%s\n%s", memories, got)
	}
	if got := storedSectionStampForTest(t, env, ctx, "agents"); got != agentsStamp {
		t.Fatalf("the agents section stamp moved from %d to %d", agentsStamp, got)
	}
	if got := storedSectionStampForTest(t, env, ctx, "memories"); got != memoriesStamp {
		t.Fatalf("the memories section stamp moved from %d to %d", memoriesStamp, got)
	}
}

// A row the user added first wins: the migration creates nothing and points
// the references at the row that is already there.
func TestLLMModelsMigrateReusesAnExistingRow(t *testing.T) {
	env, ctx := migrateEnvForTest(t, `{"definitions":{"main":{"primary":true,
		"llm_providers":[{"provider":"openai","model":"gpt-x","api_key":"sk-legacy","base_url":"https://a.example.com/v1"}],
		"default_model":"openai/gpt-x"}}}`, "")
	existing, err := (&state.LLMModelStore{DB: env.SQL}).Create(ctx, state.LLMModel{
		ID: "llm-user-row", Provider: "openai", Model: "gpt-x", BaseURL: "https://a.example.com/v1", APIKey: "sk-user",
	})
	if err != nil {
		t.Fatalf("store the user's row: %v", err)
	}

	if err := migrateLegacyLLMModels(ctx, env.SQL, env.Root); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	rows := modelTableForTest(t, env, ctx)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want only the user's: %+v", len(rows), rows)
	}
	if rows[0].ID != existing.ID || rows[0].APIKey != "sk-user" {
		t.Fatalf("row = %+v, want the user's untouched", rows[0])
	}
	if got := storedDefinitionForTest(t, env, ctx, "main").DefaultModel; got != existing.ID {
		t.Fatalf("default_model = %q, want the existing row %q", got, existing.ID)
	}
}

// The placeholder normalize() keeps on main carries no model: it is stripped
// and nothing is stored.
func TestLLMModelsMigrateStripsThePlaceholder(t *testing.T) {
	env, ctx := migrateEnvForTest(t, `{"definitions":{"main":{"primary":true,"llm_providers":[{}]}}}`, "")

	if err := migrateLegacyLLMModels(ctx, env.SQL, env.Root); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if rows := modelTableForTest(t, env, ctx); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none for a placeholder", rows)
	}
	if section := storedSectionForTest(t, env, ctx, "agents"); strings.Contains(section, "llm_providers") {
		t.Fatalf("the placeholder survived: %s", section)
	}
}

// A login-backed row carries the endpoint the client builds from the login, so
// the models page has a real path to show instead of "the login decides".
func TestLLMModelsMigrateCompletesLoginEndpoints(t *testing.T) {
	env, ctx := migrateEnvForTest(t, `{"definitions":{"main":{"primary":true,
		"llm_providers":[
			{"provider":"chatgpt","model":"gpt-5.5","base_url":"https://chatgpt.com/backend-api/codex"},
			{"provider":"opencode-go","model":"claude-haiku-5-5","base_url":"https://opencode.ai/inference/go"},
			{"provider":"opencode-go","model":"gpt-6-luna","base_url":"https://opencode.ai/inference/go"},
			{"provider":"opencode-go","model":"glm-5.3","base_url":"https://opencode.ai/inference/go"}]}}}`, "")

	if err := migrateLegacyLLMModels(ctx, env.SQL, env.Root); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	rows := modelTableForTest(t, env, ctx)
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4: %+v", len(rows), rows)
	}
	for _, tc := range []struct {
		provider, model, apiPath, source string
	}{
		{"chatgpt", "gpt-5.5", "/responses", "chatgpt"},
		{"opencode-go", "claude-haiku-5-5", "/v1/messages", "opencode-go"},
		{"opencode-go", "gpt-6-luna", "/responses", "opencode-go"},
		{"opencode-go", "glm-5.3", "/chat/completions", "opencode-go"},
	} {
		row := modelForTest(t, rows, tc.provider, tc.model)
		if row.APIPath != tc.apiPath {
			t.Fatalf("%s/%s api_path = %q, want %q", tc.provider, tc.model, row.APIPath, tc.apiPath)
		}
		if row.Source != tc.source {
			t.Fatalf("%s/%s source = %q, want %q", tc.provider, tc.model, row.Source, tc.source)
		}
		if row.BaseURL == "" {
			t.Fatalf("%s/%s has no base_url", tc.provider, tc.model)
		}
	}
}

func modelForTest(t *testing.T, rows []state.LLMModel, provider, model string) state.LLMModel {
	t.Helper()
	for _, row := range rows {
		if row.Provider == provider && row.Model == model {
			return row
		}
	}
	t.Fatalf("no row for %s/%s in %+v", provider, model, rows)
	return state.LLMModel{}
}

// upsertEnvForTest is a state database of its own: UpsertLLMModel needs
// nothing but the handle.
func upsertEnvForTest(t *testing.T) (*state.LLMModelStore, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &state.LLMModelStore{DB: db}, ctx
}

// Signing in again replaces the key it had, and the row keeps its id: the same
// provider, model, base URL and API path is one row.
func TestUpsertLLMModelUpdatesTheExistingRowInPlace(t *testing.T) {
	store, ctx := upsertEnvForTest(t)
	signedIn := "sk-first"
	first, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "OpenAI", Model: "gpt-5.5", BaseURL: "https://api.openai.com/v1/", APIKey: &signedIn,
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	rotated := "sk-rotated"
	second, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "openai", Model: "gpt-5.5", BaseURL: "https://api.openai.com/v1", APIKey: &rotated,
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second != first {
		t.Fatalf("id changed from %q to %q", first, second)
	}
	rows, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(rows), rows)
	}
	if rows[0].APIKey != "sk-rotated" {
		t.Fatalf("key = %q, want the rotated one", rows[0].APIKey)
	}
	if rows[0].APIPath != "/chat/completions" {
		t.Fatalf("api_path = %q, want the protocol's default completed before the lookup", rows[0].APIPath)
	}
}

// An input that says nothing about the key keeps the stored one; an empty
// string clears it.
func TestUpsertLLMModelKeepsAndClearsTheStoredKey(t *testing.T) {
	store, ctx := upsertEnvForTest(t)
	key := "sk-first"
	if _, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "openai", Model: "gpt-5.5", BaseURL: "https://api.openai.com/v1", APIKey: &key,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "openai", Model: "gpt-5.5", BaseURL: "https://api.openai.com/v1",
	}); err != nil {
		t.Fatalf("upsert without a key: %v", err)
	}
	rows, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].APIKey != "sk-first" {
		t.Fatalf("rows = %+v, want the stored key kept", rows)
	}
	empty := ""
	if _, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "openai", Model: "gpt-5.5", BaseURL: "https://api.openai.com/v1", APIKey: &empty,
	}); err == nil {
		t.Fatal("clearing a key on a key-authenticated provider must be refused")
	}
}

// A different endpoint is a different row: the same model reached through a
// mirror is a second configuration, not an edit of the first.
func TestUpsertLLMModelAddsARowForAnotherEndpoint(t *testing.T) {
	store, ctx := upsertEnvForTest(t)
	key := "sk-first"
	direct, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "openai", Model: "gpt-5.5", BaseURL: "https://api.openai.com/v1", APIKey: &key,
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	mirror, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "openai", Model: "gpt-5.5", BaseURL: "https://mirror.example.com/v1", APIKey: &key,
	})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if direct == mirror {
		t.Fatal("two endpoints collapsed into one row")
	}
	rows, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(rows), rows)
	}
}

// A login-backed model needs no key, and the row it writes is the one the
// session selection names.
func TestUpsertLLMModelStoresALoginRowWithoutAKey(t *testing.T) {
	store, ctx := upsertEnvForTest(t)
	empty := ""
	id, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "opencode-go", Model: "glm-5.3", BaseURL: "https://opencode.ai/inference/go", APIKey: &empty,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	row, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.APIKey != "" || row.Provider != "opencode-go" {
		t.Fatalf("row = %+v, want a keyless login row", row)
	}
	if _, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "openai", Model: "gpt-5.5", BaseURL: "https://api.openai.com/v1",
	}); err == nil {
		t.Fatal("a key-authenticated provider without a key must be refused")
	}
}

// SetAgentDefaultModel creates main the way setup always did, and refuses a
// name no definition carries.
func TestSetAgentDefaultModelCreatesMainAndRefusesAnUnknownAgent(t *testing.T) {
	key := "sk-1"
	store, ctx := upsertEnvForTest(t)
	id, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "openai", Model: "gpt-5.5", BaseURL: "https://api.openai.com/v1", APIKey: &key,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	env := &Environment{Root: t.TempDir(), ConfigPath: filepath.Join(t.TempDir(), "forebrain.yaml"), SQL: store.DB}
	if err := os.WriteFile(env.ConfigPath, []byte("approval_policy: \"on-request\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	InstallConfigSectionValidator(env.ConfigPath, env.SQL)
	t.Cleanup(func() { state.SetSectionValidator(nil) })

	if err := SetAgentDefaultModel(ctx, store.DB, "", id); err != nil {
		t.Fatalf("set main: %v", err)
	}
	cfg, err := env.ComposedPersistedConfig(ctx)
	if err != nil {
		t.Fatalf("read the stored configuration: %v", err)
	}
	def, ok := cfg.Agents.Definitions["main"]
	if !ok || !def.Primary || def.DefaultModel != id {
		t.Fatalf("main = %+v, want it created with the row as its default", def)
	}
	if err := SetAgentDefaultModel(ctx, store.DB, "typo", id); err == nil {
		t.Fatal("an unknown agent name must be refused")
	}
}

// probeChatServer answers a Chat Completions stream the way a provider does, so
// a probe against it succeeds.
func probeChatServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeProbeChatStream(w)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// writeProbeChatStream is one complete Chat Completions stream: a content
// delta, the finish frame every client waits for, and the terminator.
func writeProbeChatStream(w http.ResponseWriter) {
	_, _ = w.Write([]byte("data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\"},\"finish_reason\":null}]}\n\n"))
	_, _ = w.Write([]byte("data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
}

func probeInput(baseURL string) LLMModelInput {
	key := "sk-probe-secret"
	return LLMModelInput{
		Provider: appcfg.CustomProvider,
		Model:    "openai/gpt-4o",
		BaseURL:  baseURL,
		APIPath:  appcfg.APIPathChatCompletions,
		APIKey:   &key,
	}
}

// TestProbeLLMModelAcceptsARealAnswer pins the success path: one message, no
// tools, a 16-token answer, and nothing to report.
func TestProbeLLMModelAcceptsARealAnswer(t *testing.T) {
	srv := probeChatServer(t, http.StatusOK, "")
	var gotBody string
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeProbeChatStream(w)
	})
	if err := ProbeLLMModel(context.Background(), probeInput(srv.URL)); err != nil {
		t.Fatalf("probe error = %v", err)
	}
	// The probe is the smallest real request: one user turn, no tools.
	if !strings.Contains(gotBody, `"ping"`) {
		t.Fatalf("probe body = %s, want the one-word message", gotBody)
	}
	if strings.Contains(gotBody, `"tools"`) {
		t.Fatalf("probe body carries tools: %s", gotBody)
	}
}

// TestProbeLLMModelClassifiesFailures pins the four sentences a person sees —
// and that none of them quotes the key or the request.
func TestProbeLLMModelClassifiesFailures(t *testing.T) {
	key := "sk-probe-secret"
	cases := []struct {
		name  string
		input func(t *testing.T) LLMModelInput
		want  string
	}{
		{
			name: "unauthorized",
			input: func(t *testing.T) LLMModelInput {
				return probeInput(probeChatServer(t, http.StatusUnauthorized, `{"error":{"message":"bad key"}}`).URL)
			},
			want: "the API key was refused",
		},
		{
			name: "forbidden",
			input: func(t *testing.T) LLMModelInput {
				return probeInput(probeChatServer(t, http.StatusForbidden, `{"error":{"message":"no access"}}`).URL)
			},
			want: "the API key was refused",
		},
		{
			name: "not found",
			input: func(t *testing.T) LLMModelInput {
				return probeInput(probeChatServer(t, http.StatusNotFound, `no such model`).URL)
			},
			want: "the endpoint or model was not found",
		},
		{
			name: "other status",
			input: func(t *testing.T) LLMModelInput {
				return probeInput(probeChatServer(t, http.StatusInternalServerError, `boom`).URL)
			},
			want: "the request failed: HTTP 500",
		},
		{
			name: "unreachable",
			input: func(t *testing.T) LLMModelInput {
				// A port nothing listens on: binding and closing gives one.
				srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
				url := srv.URL
				srv.Close()
				return probeInput(url)
			},
			want: "could not reach the base URL",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ProbeLLMModel(context.Background(), tc.input(t))
			if err == nil || err.Error() != tc.want {
				t.Fatalf("probe error = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), key) {
				t.Fatalf("probe error %q carries the key", err)
			}
		})
	}
}

// TestProbeLLMModelRefusesSignInModels pins the one class a key cannot test.
func TestProbeLLMModelRefusesSignInModels(t *testing.T) {
	for _, provider := range []string{"chatgpt", "opencode-go"} {
		err := ProbeLLMModel(context.Background(), LLMModelInput{Provider: provider, Model: "some-model"})
		if err == nil || err.Error() != "sign-in models are checked by signing in" {
			t.Fatalf("%s: probe error = %v, want the sign-in sentence", provider, err)
		}
	}
}

// TestInstallModelOverridesPublishesRegistryTablesAndRowCaps pins what the
// composition installs on every adoption: the registry's plan tables (so
// volcengine resolves without catalog rows) and the rows' own capabilities,
// with the oldest row winning when two name the same provider and model.
func TestInstallModelOverridesPublishesRegistryTablesAndRowCaps(t *testing.T) {
	t.Cleanup(func() {
		llm.SetModelOverrides(nil)
		llm.SetBuiltinModels("volcengine", nil)
		llm.SetBuiltinModels("byteplus", nil)
	})
	no := false
	context := int64(32000)
	output := int64(4096)
	model := "custom-model"
	root := appcfg.Root{LLMModels: []appcfg.LLMModelConfig{
		{ID: "row-old", Provider: appcfg.CustomProvider, Model: model, Caps: appcfg.LLMModelCaps{ContextWindow: &context, MaxOutput: &output, Images: &no}},
		{ID: "row-new", Provider: appcfg.CustomProvider, Model: model, Caps: appcfg.LLMModelCaps{ContextWindow: func() *int64 { v := int64(1); return &v }()}},
	}}
	installModelOverrides(root)

	if !llm.KnownProvider("volcengine") {
		t.Fatal("the registry's plan table was not installed: volcengine is still unknown")
	}
	hit, ok := llm.Lookup("volcengine", "doubao-seed-evolving")
	if !ok || hit.ContextWindow != 1024000 || hit.DefaultMaxTokens != 256000 {
		t.Fatalf("volcengine hit = %+v ok=%v", hit, ok)
	}
	hit, ok = llm.Lookup(appcfg.CustomProvider, model)
	if !ok {
		t.Fatal("the row's capabilities did not make its model resolve")
	}
	if hit.ContextWindow != 32000 || hit.DefaultMaxTokens != 4096 || hit.SupportsAttachments {
		t.Fatalf("hit = %+v, want the first row's capabilities", hit)
	}
}

// TestRefreshModelCatalogCapsPublishesEachAnswer pins the live half: answers
// arrive per row, off the request path, and a row the source does not list
// stays absent — which is what makes a surface show the checkboxes.
func TestRefreshModelCatalogCapsPublishesEachAnswer(t *testing.T) {
	t.Cleanup(resetModelCatalogCaps)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"providers":{"opencode-go":{"models":{"step-5-preview":{"reasoning_options":[{"type":"effort","values":["low","medium","high"]}],"limit":{"context":1000,"output":100}}}}}}`))
	}))
	t.Cleanup(srv.Close)
	resolver := &modelinfo.Resolver{Sources: []modelinfo.Source{&modelinfo.ModelsDevSource{BaseURL: srv.URL}}}
	refreshModelCatalogCaps([]appcfg.LLMModelConfig{
		{ID: "row-ok", Provider: "opencode-go", Model: "step-5-preview"},
		{ID: "row-missing", Provider: "opencode-go", Model: "not-listed"},
	}, resolver)

	deadline := time.Now().Add(5 * time.Second)
	for {
		info, ok := ModelCatalogCaps("row-ok")
		if ok {
			if !reflect.DeepEqual(info.Efforts, []string{"low", "medium", "high"}) || info.Origin != "models.dev" {
				t.Fatalf("row-ok caps = %+v", info)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("row-ok never received its live answer")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := ModelCatalogCaps("row-missing"); ok {
		t.Fatal("a row the source does not list must stay unknown, so the surface offers the checkboxes")
	}
}

// TestCustomProviderIDsAgree pins the one constant llm restates because it sits
// below pkg/config.
func TestCustomProviderIDsAgree(t *testing.T) {
	if appcfg.CustomProvider != "custom" {
		t.Fatalf("appcfg.CustomProvider = %q", appcfg.CustomProvider)
	}
	if !llm.KnownProvider(appcfg.CustomProvider) {
		t.Fatal("llm does not accept the custom-model provider id")
	}
	if err := VerifyProvider(appcfg.CustomProvider); err != nil {
		t.Fatalf("VerifyProvider(custom) = %v", err)
	}
}
