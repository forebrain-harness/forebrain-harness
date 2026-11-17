package process

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// A stored reference written before the model table existed is a
// "provider/model" label. The migration rewrites the ones a row carries into
// that row's id — the reference half of plan 262, which survives plan 315 —
// and leaves the list-forming half, which no longer exists, alone.
func TestLLMModelsMigrateRewritesALabelToTheRowsID(t *testing.T) {
	env, ctx := migrateEnvForTest(t, `{"definitions":{"main":{"primary":true,"default_model":"openai/gpt-x"}}}`, "")
	if _, err := (&state.LLMModelStore{DB: env.SQL}).Create(context.Background(), state.LLMModel{
		ID: "llm-kept", Provider: "openai", Model: "gpt-x", APIKey: "sk-kept",
		BaseURL: "https://a.example.com/v1", APIPath: "/chat/completions",
	}); err != nil {
		t.Fatalf("seed the row: %v", err)
	}

	if err := migrateLegacyLLMModels(context.Background(), env.SQL); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var agents appcfg.AgentsSection
	if err := json.Unmarshal([]byte(storedSectionForTest(t, env, ctx, "agents")), &agents); err != nil {
		t.Fatalf("read the agents section: %v", err)
	}
	if got := agents.Definitions["main"].DefaultModel; got != "llm-kept" {
		t.Fatalf("default_model = %q, want the row id llm-kept", got)
	}
	rows := modelTableForTest(t, env, ctx)
	if len(rows) != 1 || rows[0].ID != "llm-kept" {
		t.Fatalf("model table = %+v, want the one row the fixture seeded", rows)
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

// provider, model, base URL and API path is one row.

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

// The catalog provider a custom row may name is a tri-state input like the
// other optional columns (owner 2026-11-16): nil keeps what the row stores, ""
// clears it, and a create writes what the input names — normalized the one way
// every ingress normalizes it.
func TestUpsertLLMModelKeepsOrClearsTheCatalogProvider(t *testing.T) {
	store, ctx := upsertEnvForTest(t)
	key := "sk-1"
	named := "alibaba"
	if _, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "custom", Model: "qwen4-max-preview", BaseURL: "https://api.example.com/v1", APIKey: &key,
		CatalogProvider: &named,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	rows, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].CatalogProvider != "alibaba" {
		t.Fatalf("rows = %+v, want the row naming alibaba", rows)
	}
	if _, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "custom", Model: "qwen4-max-preview", BaseURL: "https://api.example.com/v1",
	}); err != nil {
		t.Fatalf("upsert naming no provider: %v", err)
	}
	rows, err = store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].CatalogProvider != "alibaba" {
		t.Fatalf("rows = %+v; nil must keep the stored catalog provider", rows)
	}
	empty := ""
	if _, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "custom", Model: "qwen4-max-preview", BaseURL: "https://api.example.com/v1",
		CatalogProvider: &empty,
	}); err != nil {
		t.Fatalf("upsert clearing the provider: %v", err)
	}
	rows, err = store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].CatalogProvider != "" {
		t.Fatalf("rows = %+v; an empty input must clear the catalog provider", rows)
	}
	slashed := "alibaba/"
	if _, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "custom", Model: "qwen4-max-preview", BaseURL: "https://api.example.com/v1",
		CatalogProvider: &slashed,
	}); err != nil {
		t.Fatalf("upsert a slashed provider: %v", err)
	}
	rows, err = store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].CatalogProvider != "alibaba" {
		t.Fatalf("rows = %+v; the stored provider carries no surrounding slash", rows)
	}
}

// A subscription's listing states a model's capabilities, and the row keeps
// them: the window and the image switch are the row's own, so every reader sees
// what the vendor said. An input that says nothing about a capability keeps the
// stored one — signing in again with a listing that named no window must not
// erase the window the row already knew.
func TestUpsertLLMModelWritesAndKeepsTheListingsCapabilities(t *testing.T) {
	store, ctx := upsertEnvForTest(t)
	key := "sk-1"
	window := int64(1_000_000)
	images := true
	efforts := []string{"low", "medium", "xhigh"}
	off := true
	defaultEffort := "medium"
	id, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "openai", Model: "gpt-5.5", BaseURL: "https://api.openai.com/v1", APIKey: &key,
		CapContextWindow: &window, CapImages: &images,
		CapEfforts: &efforts, CapReasoningOff: &off, CapDefaultEffort: &defaultEffort,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].CapContextWindow == nil || *rows[0].CapContextWindow != 1_000_000 ||
		rows[0].CapImages == nil || !*rows[0].CapImages {
		t.Fatalf("rows = %+v, want the listing's caps on the row", rows)
	}
	if got := rows[0].CapEfforts; len(got) != 3 || got[2] != "xhigh" {
		t.Fatalf("row levels = %v, want the listing's", got)
	}
	if rows[0].CapReasoningOff == nil || !*rows[0].CapReasoningOff {
		t.Fatalf("row off switch = %v, want the listing's", rows[0].CapReasoningOff)
	}
	if rows[0].CapDefaultEffort != "medium" {
		t.Fatalf("row default effort = %q, want the listing's", rows[0].CapDefaultEffort)
	}

	if _, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "openai", Model: "gpt-5.5", BaseURL: "https://api.openai.com/v1",
	}); err != nil {
		t.Fatalf("upsert without caps: %v", err)
	}
	rows, err = store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != id || rows[0].CapContextWindow == nil || *rows[0].CapContextWindow != 1_000_000 {
		t.Fatalf("rows = %+v, want the stored window kept", rows)
	}
	if len(rows[0].CapEfforts) != 3 || rows[0].CapReasoningOff == nil || !*rows[0].CapReasoningOff ||
		rows[0].CapDefaultEffort != "medium" {
		t.Fatalf("row = %+v, want the stored thinking caps kept", rows[0])
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

// A login-backed row is one row whatever path it displays: writing it again
// updates it in place and keeps the path the models page already shows.
func TestUpsertLLMModelFindsALoginRowWhateverItsPath(t *testing.T) {
	store, ctx := upsertEnvForTest(t)
	empty := ""
	legacy, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "opencode-go", Model: "glm-5.3", BaseURL: "https://opencode.ai/inference/go",
		APIPath: "/chat/completions", APIKey: &empty,
	})
	if err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	again, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "opencode-go", Model: "glm-5.3", BaseURL: "https://opencode.ai/inference/go", APIKey: &empty,
	})
	if err != nil {
		t.Fatalf("write again: %v", err)
	}
	if again != legacy {
		t.Fatalf("id changed %q -> %q: the login decides the endpoint, so the path is not part of the row", legacy, again)
	}
	rows, err := store.List(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v err=%v, want one", rows, err)
	}
	if rows[0].APIPath != "/chat/completions" {
		t.Fatalf("api_path = %q, want the stored display path kept", rows[0].APIPath)
	}
}

// The path is identity for a provider whose endpoints are told apart by it.
func TestLLMModelHeldKeepsThePathForAKeyProvider(t *testing.T) {
	store, ctx := upsertEnvForTest(t)
	key := "ark-placeholder-key"
	row, err := UpsertLLMModel(ctx, store.DB, LLMModelInput{
		Provider: "volcengine", Model: "glm-5.3", BaseURL: "https://ark.cn-beijing.volces.com/api/coding",
		APIPath: "/v1/messages", APIKey: &key,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, found, err := LLMModelHeld(ctx, store.DB, "volcengine", "glm-5.3", "https://ark.cn-beijing.volces.com/api/coding", "")
	if err != nil {
		t.Fatalf("held: %v", err)
	}
	if found {
		t.Fatal("a plan's OpenAI-compatible path is a different endpoint, not the same row")
	}
	got, found, err := LLMModelHeld(ctx, store.DB, "volcengine", "glm-5.3", "https://ark.cn-beijing.volces.com/api/coding", "/v1/messages")
	if err != nil || !found || got.ID != row {
		t.Fatalf("held = %+v found=%t err=%v, want the seeded row", got, found, err)
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

// TestDatabaseCapsWinOverCatalog pins the two-layer answer a configured row
// gets: the shipped catalog is the default, and the row's own capability
// columns win over it. LookupCatalog stays the defaults-only answer the models
// page compares against, so the row being edited never pollutes its own form
// (plan 265). The thinking levels are the one exception: when the catalog
// describes them, they are the vendor's data and the row's stale column does
// not shadow them (owner 2026-11-16, the same rule the web write path applies).
func TestDatabaseCapsWinOverCatalog(t *testing.T) {
	cat, err := llm.Parse([]byte(`{
		"openai/gpt-4o": {"id":"openai/gpt-4o","limit":{"context":128000,"output":16384},
			"reasoning_options":[{"type":"effort","values":["low","high"]}]}
	}`))
	if err != nil {
		t.Fatalf("parse catalog: %v", err)
	}
	llm.SetGlobalCatalogForTest(cat)
	t.Cleanup(func() {
		llm.SetGlobalCatalogForTest(nil)
		llm.SetModelOverrides(nil, nil)
	})

	window := int64(64000)
	root := appcfg.Root{LLMModels: []appcfg.LLMModelConfig{{
		ID: "row-1", Provider: "openai", Model: "gpt-4o",
		Caps: appcfg.LLMModelCaps{ContextWindow: &window, Efforts: []string{"high"}},
	}}}
	installModelOverrides(root)

	hit, ok := llm.Lookup("openai", "gpt-4o")
	if !ok || hit.ContextWindow != 64000 {
		t.Fatalf("Lookup = %+v ok=%v, want the row's window", hit, ok)
	}
	if levels, known := hit.ReasoningEfforts(); !known || !reflect.DeepEqual(levels, []string{"low", "high"}) {
		t.Fatalf("levels = %v known=%v, want the catalog's: a vendor that describes the levels decides them", levels, known)
	}

	defaults, ok := llm.LookupCatalog("openai", "gpt-4o")
	if !ok || defaults.ContextWindow != 128000 {
		t.Fatalf("LookupCatalog = %+v ok=%v, want the catalog's window", defaults, ok)
	}
	if levels, known := defaults.ReasoningEfforts(); !known || !reflect.DeepEqual(levels, []string{"low", "high"}) {
		t.Fatalf("default levels = %v known=%v, want the catalog's levels", levels, known)
	}
}

// TestInstallModelOverridesPublishesRegistryTablesAndRowCaps pins what the
// composition installs on every adoption: the registry's plan tables (so
// volcengine resolves without catalog rows) and the rows' own capabilities,
// keyed by row id — two rows naming the same provider and model keep their own
// settings, and a caller that knows only the pair gets the catalog's values
// rather than either row's (owner 2026-11-10, plan 283 Part E).
func TestInstallModelOverridesPublishesRegistryTablesAndRowCaps(t *testing.T) {
	t.Cleanup(func() {
		llm.SetModelOverrides(nil, nil)
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
	// Two rows name this pair, so the pair alone has no single answer: the
	// catalog's values stand, and the custom model stays unknown there.
	if _, ok := llm.Lookup(appcfg.CustomProvider, model); ok {
		t.Fatal("a pair two rows name must not resolve from one row's capabilities")
	}
	old, ok := llm.LookupRow("row-old", appcfg.CustomProvider, model)
	if !ok || old.ContextWindow != 32000 || old.DefaultMaxTokens != 4096 || old.SupportsAttachments {
		t.Fatalf("row-old hit = %+v ok=%v, want its own capabilities", old, ok)
	}
	fresh, ok := llm.LookupRow("row-new", appcfg.CustomProvider, model)
	if !ok || fresh.ContextWindow != 1 {
		t.Fatalf("row-new hit = %+v ok=%v, want its own window", fresh, ok)
	}
}

// A row that carries no capability column but names the provider its model
// data lives under still installs itself, so the row's lookup reads that
// entry's window (owner 2026-11-16). A row that names nothing stays out, and a
// sibling that names no entry is untouched by the first one's key.
func TestModelOverridesInstallRowsThatNameOnlyACatalogKey(t *testing.T) {
	cat, err := llm.Parse([]byte(`{
		"alibaba/qwen4-max-preview": {"id":"alibaba/qwen4-max-preview","limit":{"context":1000000,"output":262144}},
		"acme/solo-model": {"id":"acme/solo-model","limit":{"context":8192}}
	}`))
	if err != nil {
		t.Fatalf("parse catalog: %v", err)
	}
	llm.SetGlobalCatalogForTest(cat)
	t.Cleanup(func() {
		llm.SetGlobalCatalogForTest(nil)
		llm.SetModelOverrides(nil, nil)
	})

	root := appcfg.Root{LLMModels: []appcfg.LLMModelConfig{
		{ID: "row-keyed", Provider: appcfg.CustomProvider, Model: "qwen4-max-preview", CatalogProvider: "alibaba"},
		{ID: "row-plain", Provider: appcfg.CustomProvider, Model: "nothing-here"},
	}}
	installModelOverrides(root)

	hit, ok := llm.LookupRow("row-keyed", appcfg.CustomProvider, "qwen4-max-preview")
	if !ok || hit.ContextWindow != 1000000 || hit.DefaultMaxTokens != 262144 {
		t.Fatalf("row-keyed hit = %+v ok=%v; want the entry its catalog provider names", hit, ok)
	}
	if _, ok := llm.LookupRow("row-plain", appcfg.CustomProvider, "nothing-here"); ok {
		t.Fatal("a row that names no caps and no catalog entry must not install an override")
	}
}

// TestRefreshModelCatalogCapsPublishesEachAnswer pins the live half: answers
// arrive per row, off the request path, from the shipped catalog, and a row the
// catalog does not list stays absent — which is what makes a surface show the
// checkboxes.
func TestRefreshModelCatalogCapsPublishesEachAnswer(t *testing.T) {
	t.Cleanup(resetModelCatalogCaps)
	cat, err := llm.Parse([]byte(`{
		"opencode-go/step-5-preview": {"id":"opencode-go/step-5-preview","reasoning":true,
			"reasoning_options":[{"type":"effort","values":["low","medium","high"]}],"limit":{"context":1000,"output":100}}
	}`))
	if err != nil {
		t.Fatalf("parse catalog: %v", err)
	}
	llm.SetGlobalCatalogForTest(cat)
	t.Cleanup(func() { llm.SetGlobalCatalogForTest(nil) })

	resolver := &modelinfo.Resolver{Sources: []modelinfo.Source{modelinfo.ModelsDevSource{}}}
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
		t.Fatal("a row the catalog does not list must stay unknown, so the surface offers the checkboxes")
	}
}

// failingSource stands in for a subscription API in the sweep tests: it applies
// to every row and answers with the error its test put on that row.
type failingSource struct {
	apply  func(Row string) bool
	answer func(Row string) (modelinfo.Info, error)
}

func (s failingSource) Applies(row modelinfo.Row, _ string) bool {
	return s.apply == nil || s.apply(row.Model)
}

func (s failingSource) Fetch(_ context.Context, row modelinfo.Row, _ string) (modelinfo.Info, error) {
	return s.answer(row.Model)
}

// TestRefreshModelCatalogCapsLogsEachFailureOnce pins the log budget owner
// 2026-11-09 asked for: a model the catalog does not list is not a failure and
// logs nothing; a real failure logs one line per distinct text per sweep, not
// one per configured row — the model name is a field, never the dedupe key.
func TestRefreshModelCatalogCapsLogsEachFailureOnce(t *testing.T) {
	t.Cleanup(resetModelCatalogCaps)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	backendDown := fmt.Errorf("subscription backend unreachable")
	keyRejected := fmt.Errorf("plan key rejected")
	source := failingSource{answer: func(model string) (modelinfo.Info, error) {
		switch model {
		case "a", "b":
			return modelinfo.Info{}, backendDown
		case "c":
			return modelinfo.Info{}, keyRejected
		case "d":
			return modelinfo.Info{}, modelinfo.ErrNotListed
		default:
			return modelinfo.Info{Efforts: []string{"high"}, Origin: "models.dev"}, nil
		}
	}}
	resolver := &modelinfo.Resolver{Sources: []modelinfo.Source{source}}
	refreshModelCatalogCaps([]appcfg.LLMModelConfig{
		{ID: "row-a", Provider: "test", Model: "a"},
		{ID: "row-b", Provider: "test", Model: "b"},
		{ID: "row-c", Provider: "test", Model: "c"},
		{ID: "row-d", Provider: "test", Model: "d"},
		{ID: "row-ok", Provider: "test", Model: "ok"},
	}, resolver)

	// The sweep is sequential, so the successful last row is the completion
	// signal: once it landed, every log line of this sweep is written.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := ModelCatalogCaps("row-ok"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sweep never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	out := logs.String()
	if got := strings.Count(out, "live model capabilities unavailable"); got != 2 {
		t.Fatalf("log lines = %d, want one per distinct failure (2):\n%s", got, out)
	}
	if got := strings.Count(out, "subscription backend unreachable"); got != 1 {
		t.Fatalf("the shared failure logged %d times, want once:\n%s", got, out)
	}
	if got := strings.Count(out, "plan key rejected"); got != 1 {
		t.Fatalf("the second failure logged %d times, want once:\n%s", got, out)
	}
	if strings.Contains(out, "model not listed") {
		t.Fatalf("ErrNotListed must be silent, not logged:\n%s", out)
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

// modelRowsFromEntriesForTest is how an in-memory fixture seeds the install's model table
// (plan 315): one row per model an entry names — `model: [a, b]` expanding the
// way a provider list used to — with the row ids llm-1, llm-2, … in order. A
// definition names one of them with default_model; a built-in subagent type
// names one in agents.subagent_models.<type>.
func modelRowsFromEntriesForTest(entries ...appcfg.AgentLLMProviderConfig) []appcfg.LLMModelConfig {
	var rows []appcfg.LLMModelConfig
	for _, entry := range entries {
		models := append(appcfg.StringList(nil), entry.Models...)
		if len(models) == 0 && entry.Model != "" {
			models = appcfg.StringList{entry.Model}
		}
		if len(models) == 0 {
			models = appcfg.StringList{""}
		}
		for _, model := range models {
			rows = append(rows, appcfg.LLMModelConfig{
				ID:          fmt.Sprintf("llm-%d", len(rows)+1),
				Provider:    entry.Provider,
				Model:       model,
				BaseURL:     entry.BaseURL,
				APIPath:     entry.APIPath,
				APIKey:      entry.APIKey,
				Params:      append(appcfg.LLMRequestParams(nil), entry.Params...),
				URLVerbatim: entry.URLVerbatim,
			})
		}
	}
	return rows
}

// seedFileModelForTest gives a file-seeded fixture the one model its runner
// needs: a row of the install's table pointing at a dead endpoint, which every
// fixture in this package replaces with a fake LLM anyway (plan 315 — a
// configuration file cannot carry a model).
func seedFileModelForTest(t *testing.T, home, baseURL string) {
	t.Helper()
	seedModelTableForTest(t, Context{Home: home, ConfigPath: filepath.Join(home, "forebrain.yaml")}, state.LLMModel{
		ID: "llm-fixture", Provider: "openai", Model: "gpt-test",
		APIKey: "test-key", BaseURL: baseURL, APIPath: "/chat/completions",
		ParamsJSON: `{"stream":false}`,
	})
}

// fixtureModelRows is the one-row table a fixture that loads a configuration
// document and hands it straight to a runner needs (plan 315).
func fixtureModelRows(baseURL string) []appcfg.LLMModelConfig {
	return []appcfg.LLMModelConfig{{
		ID: "llm-fixture", Provider: "openai", Model: "gpt-test",
		APIKey: "test-key", BaseURL: baseURL, APIPath: "/chat/completions",
		Params: appcfg.LLMRequestParams(`{"stream":false}`),
	}}
}

// storeAgentsSectionForTest writes a raw stored section into the home's state
// database before the process opens it — the fixture's way of stating what a
// surface wrote (plan 315: the definitions live in the database).
func storeAgentsSectionForTest(t *testing.T, home, cfgPath, section, doc string) {
	t.Helper()
	db, err := openStateForWrite(Context{Home: home, ConfigPath: cfgPath})
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := (&state.ConfigStore{DB: db}).UpdateSection(context.Background(), section, func(json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(doc), nil
	}); err != nil {
		t.Fatalf("store the %s section: %v", section, err)
	}
}
