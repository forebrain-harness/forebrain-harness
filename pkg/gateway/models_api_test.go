// The models page's API as the page drives it (plan 265): through the route
// table, against a real state database, so a path parameter and the duplicate
// index are exercised rather than mocked.
package gateway

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
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/stretchr/testify/require"
)

// modelsPageServer is a gateway serving the models page with a real state
// database and configuration, plus a request helper that goes through the route
// table.
func modelsPageServer(t *testing.T) (*Server, gatewayDo) {
	t.Helper()
	s, _, _ := rulesServer(t)
	srv := NewRestServer("")
	s.AttachExtraRoutes(srv)
	return s, func(method, target string, body ...string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		var reader io.Reader
		if len(body) > 0 {
			reader = strings.NewReader(body[0])
		}
		req := httptest.NewRequest(method, target, reader)
		if reader != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		srv.Server.Handler.ServeHTTP(rec, req)
		return rec
	}
}

// llmModelRows reads the table's answer as the page does: the rows of the first
// page plus the default the install runs on.
func llmModelRows(t *testing.T, do gatewayDo) ([]map[string]any, string) {
	t.Helper()
	rec := do(http.MethodGet, "/api/llm-models?page=1&page_size=100")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Models         []map[string]any `json:"models"`
		Total          int              `json:"total"`
		DefaultModelID string           `json:"default_model_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, len(body.Models), body.Total)
	return body.Models, body.DefaultModelID
}

// llmModelByID finds one row of a listing. The listing's order is the table's
// (created_at, then id), and two rows written in the same millisecond tie on the
// first key, so a test names the row it means.
func llmModelByID(t *testing.T, rows []map[string]any, id string) map[string]any {
	t.Helper()
	for _, row := range rows {
		if row["id"] == id {
			return row
		}
	}
	t.Fatalf("no row %q in %d rows", id, len(rows))
	return nil
}

// createModel adds one row and returns its id.
func createModel(t *testing.T, do gatewayDo, body string) string {
	t.Helper()
	rec := do(http.MethodPost, "/api/llm-models", body)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var row struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &row))
	require.NotEmpty(t, row.ID)
	return row.ID
}

// Two rows for one model are two endpoints, and each says where it sends to.
func TestLLMModelsAPIListShowsBothEndpoints(t *testing.T) {
	_, do := modelsPageServer(t)

	first := createModel(t, do, `{"provider":"deepseek","model":"deepseek-v4","base_url":"https://api.deepseek.com","api_key":"sk-first"}`)
	second := createModel(t, do, `{"provider":"deepseek","model":"deepseek-v4","base_url":"https://proxy.example.com/v1","api_key":"sk-second"}`)

	rows, _ := llmModelRows(t, do)
	require.Len(t, rows, 2)
	require.Equal(t, "https://api.deepseek.com/chat/completions", llmModelByID(t, rows, first)["endpoint"])
	require.Equal(t, "https://proxy.example.com/v1/chat/completions", llmModelByID(t, rows, second)["endpoint"])
	require.Equal(t, llmProtocolChatCompletions, llmModelByID(t, rows, first)["protocol"])
}

// The same provider, model, base URL and path is one row: the second write is
// refused and names the row that holds the identity, and a spelling that
// normalizes to it is the same row.
func TestLLMModelsAPIRefusesTheSameIdentity(t *testing.T) {
	_, do := modelsPageServer(t)
	first := createModel(t, do, `{"provider":"deepseek","model":"deepseek-v4","base_url":"https://api.deepseek.com","api_key":"sk-first"}`)

	for _, body := range []string{
		`{"provider":"deepseek","model":"deepseek-v4","base_url":"https://api.deepseek.com","api_path":"/chat/completions","api_key":"sk-x"}`,
		`{"provider":"DeepSeek","model":"deepseek-v4","base_url":"https://api.deepseek.com/","api_key":"sk-x"}`,
	} {
		rec := do(http.MethodPost, "/api/llm-models", body)
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		var conflict struct {
			Error      string `json:"error"`
			ExistingID string `json:"existing_id"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &conflict))
		require.Equal(t, first, conflict.ExistingID)
		require.Contains(t, conflict.Error, "已经配置过")
	}
}

// An update keeps the key it does not mention and clears the one it empties;
// moving a row onto another row's identity is refused the same way.
func TestLLMModelsAPIUpdateKeepsOrClearsTheKey(t *testing.T) {
	_, do := modelsPageServer(t)
	first := createModel(t, do, `{"provider":"deepseek","model":"deepseek-v4","base_url":"https://api.deepseek.com","api_key":"sk-first"}`)
	second := createModel(t, do, `{"provider":"deepseek","model":"deepseek-v4","base_url":"https://proxy.example.com/v1","api_key":"sk-second"}`)

	rec := do(http.MethodPut, "/api/llm-models/"+second,
		`{"provider":"deepseek","model":"deepseek-v4","base_url":"https://proxy.example.com/v1","api_path":"/chat/completions"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var row struct {
		APIKey      string `json:"api_key"`
		APIKeyLast4 string `json:"api_key_last4"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &row))
	require.Equal(t, "sk-second", row.APIKey)
	require.Equal(t, "cond", row.APIKeyLast4)

	// The identity is another row's: refused, naming it.
	rec = do(http.MethodPut, "/api/llm-models/"+second,
		`{"provider":"deepseek","model":"deepseek-v4","base_url":"https://api.deepseek.com"}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), first)

	// An empty key is "there is none", not "leave it alone".
	rec = do(http.MethodPut, "/api/llm-models/"+second, `{"api_key":""}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	row = struct {
		APIKey      string `json:"api_key"`
		APIKeyLast4 string `json:"api_key_last4"`
	}{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &row))
	require.Empty(t, row.APIKey)
	require.Contains(t, rec.Body.String(), `"api_key_set":false`)

	rows, _ := llmModelRows(t, do)
	updated := llmModelByID(t, rows, second)
	require.Equal(t, false, updated["api_key_set"])
	require.NotContains(t, updated, "api_key")
}

// A custom model names its protocol; without one there is nothing to send.
func TestLLMModelsAPICustomNeedsAProtocol(t *testing.T) {
	_, do := modelsPageServer(t)
	rec := do(http.MethodPost, "/api/llm-models",
		`{"provider":"custom","model":"vendor/model-x","base_url":"https://llm.example.com/v1","api_key":"sk-custom"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "请选择接口协议")

	rec = do(http.MethodPost, "/api/llm-models",
		`{"provider":"custom","model":"vendor/model-x","base_url":"https://llm.example.com/v1","api_path":"/responses","api_key":"sk-custom"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var row struct {
		ProviderName string `json:"provider_name"`
		Protocol     string `json:"protocol"`
		Custom       bool   `json:"custom"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &row))
	require.Equal(t, "自定义", row.ProviderName)
	require.Equal(t, llmProtocolResponses, row.Protocol)
	require.True(t, row.Custom)
}

// A sign-in-backed provider is added by signing in, never typed in.
func TestLLMModelsAPIRefusesASignInProvider(t *testing.T) {
	_, do := modelsPageServer(t)
	rec := do(http.MethodPost, "/api/llm-models",
		`{"provider":"chatgpt","model":"gpt-5-codex","base_url":"https://chatgpt.com/backend-api/codex","api_key":"sk-x"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "请通过订阅登录添加")
}

// A row an agent runs on cannot be deleted behind its back; any other row can.
func TestLLMModelsAPIDeleteRefusesTheDefaultModel(t *testing.T) {
	_, do := modelsPageServer(t)
	kept := createModel(t, do, `{"provider":"deepseek","model":"deepseek-v4","base_url":"https://api.deepseek.com","api_key":"sk-first"}`)
	other := createModel(t, do, `{"provider":"deepseek","model":"deepseek-v4-flash","base_url":"https://api.deepseek.com","api_key":"sk-first"}`)

	rec := do(http.MethodPut, "/api/llm-models/"+kept+"/default")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = do(http.MethodDelete, "/api/llm-models/"+kept)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var conflict struct {
		Error      string   `json:"error"`
		DefaultFor []string `json:"default_for"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &conflict))
	require.Equal(t, []string{"main"}, conflict.DefaultFor)

	require.Equal(t, http.StatusNoContent, do(http.MethodDelete, "/api/llm-models/"+other).Code)
	require.Equal(t, http.StatusNotFound, do(http.MethodDelete, "/api/llm-models/"+other).Code)
}

// The stored key is answered to the page that edits it — and nowhere else: the
// picker list, an error body and the log never carry it (owner 2026-11-07).
func TestLLMModelsAPIRevealsTheKeyOnlyToTheEditingPage(t *testing.T) {
	do := func() gatewayDo {
		_, helper := modelsPageServer(t)
		return helper
	}()
	const key = "sk-reveal-marker-9f8e7d"
	id := createModel(t, do, `{"provider":"deepseek","model":"deepseek-v4","base_url":"https://api.deepseek.com","api_key":"`+key+`"}`)

	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	// The editing page sees it, byte for byte.
	rec := do(http.MethodGet, "/api/llm-models?page=1&page_size=10")
	require.Contains(t, rec.Body.String(), key)
	rec = do(http.MethodPut, "/api/llm-models/"+id, `{"caps":{"images":true}}`)
	require.Contains(t, rec.Body.String(), key)

	// The picker list carries no key at all.
	rec = do(http.MethodGet, "/api/llm-models/choices")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), key)
	require.NotContains(t, rec.Body.String(), "api_key")

	// Neither does a refusal.
	rec = do(http.MethodPost, "/api/llm-models",
		`{"provider":"deepseek","model":"deepseek-v4","base_url":"https://api.deepseek.com","api_key":"`+key+`"}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), key)

	require.NotContains(t, logged.String(), key)
}

// The connection test is an answer, not an HTTP error: the page shows the
// sentence beside the button. A body with no key tests with the row's own.
func TestLLMModelsAPITestConnection(t *testing.T) {
	_, do := modelsPageServer(t)

	var gotAuth string
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"pong"},"finish_reason":null}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"id":"1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(endpoint.Close)
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(refusing.Close)

	rec := do(http.MethodPost, "/api/llm-models/test",
		`{"provider":"custom","model":"vendor/model-x","base_url":"`+endpoint.URL+`","api_path":"/chat/completions","api_key":"sk-probe"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"ok":true`)
	require.Equal(t, "Bearer sk-probe", gotAuth)

	rec = do(http.MethodPost, "/api/llm-models/test",
		`{"provider":"custom","model":"vendor/model-x","base_url":"`+refusing.URL+`","api_path":"/chat/completions","api_key":"sk-probe"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var answer struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &answer))
	require.False(t, answer.OK)
	require.Contains(t, answer.Error, "refused")

	// Only an id: the key comes from the row, and never travels back.
	id := createModel(t, do, `{"provider":"custom","model":"vendor/model-x","base_url":"`+endpoint.URL+`","api_path":"/chat/completions","api_key":"sk-stored"}`)
	gotAuth = ""
	rec = do(http.MethodPost, "/api/llm-models/test", `{"id":"`+id+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"ok":true`)
	require.Equal(t, "Bearer sk-stored", gotAuth)
	require.NotContains(t, rec.Body.String(), "sk-stored")
}

// A row a subscription wrote keeps the model, the endpoint and the key its
// sign-in chose: only its advanced settings are editable here.
func TestLLMModelsAPISubscriptionRowIsAdvancedOnly(t *testing.T) {
	s, do := modelsPageServer(t)
	chatGPT, _ := loginStubs(t)
	db := s.Env.SQL
	empty := ""
	id, err := process.UpsertLLMModel(context.Background(), db, process.LLMModelInput{
		Provider: "chatgpt", Model: "gpt-5-codex", BaseURL: "https://chatgpt.com/backend-api/codex",
		APIKey: &empty, Source: "chatgpt",
	})
	require.NoError(t, err)

	rec := do(http.MethodPut, "/api/llm-models/"+id, `{"base_url":"https://elsewhere.example.com"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "订阅来的模型只能改高级配置")

	// The authorization switch is a permission, not the row's identity: it is
	// editable on a sign-in row like the capability overrides are (283).
	rec = do(http.MethodPut, "/api/llm-models/"+id, `{"subagent_allowed":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"subagent_allowed":true`)

	rec = do(http.MethodPut, "/api/llm-models/"+id, `{"caps":{"images":true,"context_window":256000}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var row struct {
		Source      string `json:"source"`
		SourceName  string `json:"source_name"`
		SourceState string `json:"source_state"`
		Caps        struct {
			Images        *bool  `json:"images"`
			ContextWindow *int64 `json:"context_window"`
		} `json:"caps"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &row))
	require.Equal(t, "chatgpt", row.Source)
	require.Equal(t, "ChatGPT", row.SourceName)
	require.True(t, *row.Caps.Images)
	require.EqualValues(t, 256000, *row.Caps.ContextWindow)
	require.Contains(t, []string{"usable", "missing", "unreachable", "dead"}, row.SourceState)

	// Refreshing the key is the subscription's own call, for the row that names
	// it; a sign-in that stores no key has nothing to fetch.
	rec = do(http.MethodPost, "/api/llm-models/"+id+"/refresh-key")
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "登录就是凭据")
	require.Equal(t, []string{id}, chatGPT.refreshed)

	// A plan that does hold a key answers with the fresh row.
	chatGPT.refreshErr = nil
	rec = do(http.MethodPost, "/api/llm-models/"+id+"/refresh-key")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"ok":true`)
}

// The catalog lookup answers what a model can do by default — the values a row's
// catalog_caps carries — and says whether a source documented its thinking
// levels.
func TestLLMModelsAPICatalogCaps(t *testing.T) {
	// A provider the catalog has no rows for answers from the built-in model
	// table, the layer volcengine and BytePlus live in.
	llm.SetBuiltinModels("gateway-test-provider", []llm.Model{{
		ID: "m1", Provider: "gateway-test-provider", APIModel: "m1",
		ContextWindow: 128000, DefaultMaxTokens: 4096,
		SupportsAttachments: true, CanReason: true,
		ReasoningOptions: []llm.ReasoningOption{{Type: "effort", Values: []string{"low", "high"}}},
		ReasoningKnown:   true,
	}})
	t.Cleanup(func() { llm.SetBuiltinModels("gateway-test-provider", nil) })
	_, do := modelsPageServer(t)

	rec := do(http.MethodGet, "/api/llm-models/catalog-caps?provider=gateway-test-provider&model=m1&base_url=https://llm.example.com/v1")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body llmModelCapsBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.EqualValues(t, 128000, *body.ContextWindow)
	require.EqualValues(t, 4096, *body.MaxOutput)
	require.True(t, *body.Images)
	require.True(t, *body.Reasoning)
	require.Equal(t, []string{"low", "high"}, body.Efforts)
	// The built-in table is part of the default layer, and the shipped catalog
	// source answers from it (owner 2026-11-09): the levels are known, and the
	// label names where they came from.
	require.True(t, body.EffortsKnown)
	require.Equal(t, "models.dev", body.EffortsSource)

	// A model nothing lists has no catalog values at all.
	rec = do(http.MethodGet, "/api/llm-models/catalog-caps?provider=deepseek&model=nothing-at-all")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"efforts_known":false}`, rec.Body.String())

	require.Equal(t, http.StatusBadRequest, do(http.MethodGet, "/api/llm-models/catalog-caps").Code)
}

// The provider picker is the registry: the built-in providers in display order,
// the sign-in ones marked, and the three protocols a custom model may name.
func TestLLMModelsAPIProviders(t *testing.T) {
	_, do := modelsPageServer(t)
	rec := do(http.MethodGet, "/api/llm-providers")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Providers []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			LoginBacked bool   `json:"login_backed"`
			Endpoints   []struct {
				BaseURL string `json:"base_url"`
				APIPath string `json:"api_path"`
			} `json:"endpoints"`
		} `json:"providers"`
		Protocols []struct {
			APIPath string `json:"api_path"`
			Name    string `json:"name"`
		} `json:"protocols"`
		CustomID string `json:"custom_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotEmpty(t, body.Providers)
	require.Equal(t, "chatgpt", body.Providers[0].ID)
	require.True(t, body.Providers[0].LoginBacked)
	require.Equal(t, "openai", body.Providers[2].ID, "the registry's order is display order")
	require.Equal(t, "OpenAI", body.Providers[2].Name)
	require.Equal(t, "https://api.openai.com/v1", body.Providers[2].Endpoints[0].BaseURL)
	require.Len(t, body.Protocols, 3)
	require.Equal(t, "custom", body.CustomID)
}

// The picker's wording follows the browser's language, the one place this
// surface asks: Chinese when the request names nothing, English when it asks for
// it first.
func TestLLMModelsAPIProvidersFollowTheRequestLanguage(t *testing.T) {
	s, _ := modelsPageServer(t)
	read := func(language string) string {
		req := httptest.NewRequest(http.MethodGet, "/api/llm-providers", nil)
		if language != "" {
			req.Header.Set("Accept-Language", language)
		}
		rec := httptest.NewRecorder()
		s.handleLLMProviders(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return rec.Body.String()
	}
	require.Contains(t, read(""), "通义千问")
	require.Contains(t, read("en-US,en;q=0.9"), "Qwen Cloud")
	require.NotContains(t, read("en-US,en;q=0.9"), "通义千问")
}

// A static segment wins over a row id, so the picker list and the row routes
// live on the same prefix (plan 265's STOP condition 2).
func TestLLMModelsAPIRoutesKeepStaticSegments(t *testing.T) {
	_, do := modelsPageServer(t)
	createModel(t, do, `{"provider":"deepseek","model":"deepseek-v4","base_url":"https://api.deepseek.com","api_key":"sk-first"}`)

	rec := do(http.MethodGet, "/api/llm-models/choices")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "deepseek/deepseek-v4")
	// "choices" is not a row, and a row route still resolves a real id.
	require.Equal(t, http.StatusNotFound, do(http.MethodDelete, "/api/llm-models/choices").Code)
}

// The table's own store is what these endpoints write: a row created here must
// be readable by the runtime's view (plan 260).
func TestLLMModelsAPIWritesTheRuntimeView(t *testing.T) {
	s, do := modelsPageServer(t)
	id := createModel(t, do, `{"provider":"deepseek","model":"deepseek-v4","base_url":"https://api.deepseek.com","api_key":"sk-first"}`)

	stored, err := (&state.LLMModelStore{DB: s.Env.SQL}).Get(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "deepseek", stored.Provider)
	require.Equal(t, "/chat/completions", stored.APIPath)
	require.Equal(t, "sk-first", stored.APIKey)
}

// A model the page stores is a model the process runs on: the write re-composes
// the configuration, so the runtime snapshot every picker reads
// (/api/chat/model, Environment.Deps.AppCfg) lists the row at once instead of
// waiting for an unrelated reload. A delete takes it out the same way.
//
// The environment is a real one (process.Open composes from the database), which
// is what makes the snapshot a snapshot: the hand-built test servers in this
// file set the configuration directly and would pass whatever the write did.
func TestLLMModelsAPIReachesTheRuntimeSnapshot(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("FOREBRAIN_MODELS_SNAPSHOT_KEY", "sk-snapshot-start")
	cfgPath := filepath.Join(home, "forebrain.yaml")
	raw := "agents:\n  definitions:\n    main:\n      primary: true\n      llm_providers:\n" +
		"        - provider: deepseek\n          model: deepseek-v4\n" +
		"          api_key: ${FOREBRAIN_MODELS_SNAPSHOT_KEY}\n          base_url: http://127.0.0.1:9/v1\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(raw), 0o600))
	env, err := process.Open(ctx, process.OpenOptions{Home: home, ConfigPath: cfgPath, NoLaunchProject: true, SessionSource: "webchat"})
	require.NoError(t, err)
	defer env.Close()
	// Open installs the process-wide section validator over this home; a later
	// test in this binary writes sections against its own home. Adopting a
	// configuration also publishes its model rows into the process-wide catalog
	// lookup (pkg/llm), which outlives the environment: without clearing it a
	// later test would resolve a model from a home that is already gone.
	t.Cleanup(func() {
		state.SetSectionValidator(nil)
		llm.SetModelOverrides(nil)
	})

	s := &Server{Home: home, Env: env, Runner: env.Runner, Sessions: env.Deps.SessionStore, RunRT: env.Deps.RunRT}
	srv := NewRestServer("")
	s.AttachExtraRoutes(srv)
	do := func(method, target string, body ...string) *httptest.ResponseRecorder {
		var reader io.Reader
		if len(body) > 0 {
			reader = strings.NewReader(body[0])
		}
		req := httptest.NewRequest(method, target, reader)
		if reader != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		srv.Server.Handler.ServeHTTP(rec, req)
		return rec
	}
	// The models the conversation's own read offers: the runtime's view, not the
	// store's.
	runtimeModels := func(t *testing.T) map[string]string {
		t.Helper()
		rec := do(http.MethodGet, "/api/chat/model")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body struct {
			Options []struct {
				Model   string `json:"model"`
				BaseURL string `json:"base_url"`
			} `json:"options"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		out := make(map[string]string, len(body.Options))
		for _, option := range body.Options {
			out[option.Model] = option.BaseURL
		}
		return out
	}

	before := runtimeModels(t)
	require.Contains(t, before, "deepseek-v4", "the configuration the process started on")
	require.NotContains(t, before, "deepseek-snapshot")

	rec := do(http.MethodPost, "/api/llm-models",
		`{"provider":"deepseek","model":"deepseek-snapshot","base_url":"https://snapshot.test/v1","api_key":"sk-snapshot"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.NotEmpty(t, created.ID)

	// The row the page just stored is offered to the conversation at once, with
	// the endpoint it was configured against.
	after := runtimeModels(t)
	require.Equal(t, "https://snapshot.test/v1", after["deepseek-snapshot"],
		"a stored model must reach the runtime snapshot: %v", after)

	// Deleting it takes it back out: the snapshot follows the table both ways.
	require.Equal(t, http.StatusNoContent, do(http.MethodDelete, "/api/llm-models/"+created.ID).Code)
	require.NotContains(t, runtimeModels(t), "deepseek-snapshot")
}

// TestLLMModelsAPICapsValidationFollowsTheShippedCatalog pins the "official
// levels" rule against the offline catalog (owner 2026-11-09): a model the
// catalog lists with levels has them decided by its vendor; a model the catalog
// does not list, or lists without discrete levels, keeps the user's pick.
func TestLLMModelsAPICapsValidationFollowsTheShippedCatalog(t *testing.T) {
	// The built-in table is a row of the same default layer the shipped catalog
	// fills, so this pins the rule without disturbing the package's catalog.
	llm.SetBuiltinModels("caps-test", []llm.Model{
		{ID: "with-levels", Provider: "caps-test", APIModel: "with-levels",
			ReasoningOptions: []llm.ReasoningOption{{Type: "effort", Values: []string{"low", "high"}}},
			ReasoningKnown:   true},
		{ID: "without-levels", Provider: "caps-test", APIModel: "without-levels"},
	})
	t.Cleanup(func() { llm.SetBuiltinModels("caps-test", nil) })

	s := &Server{}
	r := httptest.NewRequest(http.MethodPut, "/api/llm-models/x", nil)

	// Listed with levels: hand-entering them is refused.
	err := s.llmModelCapsValid(r, "caps-test", "with-levels", llmModelCaps{Efforts: []string{"low"}}, state.LLMModel{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "官方文档")

	// A stored row that already carries exactly these levels may send them
	// back unchanged: the rule is about setting them now.
	require.NoError(t, s.llmModelCapsValid(r, "caps-test", "with-levels",
		llmModelCaps{Efforts: []string{"low"}},
		state.LLMModel{CapEfforts: []string{"low"}}))

	// Listed without discrete levels: the user's pick is allowed.
	require.NoError(t, s.llmModelCapsValid(r, "caps-test", "without-levels",
		llmModelCaps{Efforts: []string{"low"}}, state.LLMModel{}))

	// Not in the catalog at all: allowed, the same as before the catalog existed.
	require.NoError(t, s.llmModelCapsValid(r, "caps-test", "no-such-model",
		llmModelCaps{Efforts: []string{"low"}}, state.LLMModel{}))

	// The level names themselves are checked first, whatever the catalog says.
	require.Error(t, s.llmModelCapsValid(r, "openai", "no-such-model",
		llmModelCaps{Efforts: []string{"ultra"}}, state.LLMModel{}))
}
