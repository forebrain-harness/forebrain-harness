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

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/subscription"
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

	// A row whose key the user pastes says so: refreshing it means pasting a
	// new one, which is not "the sign-in is the credential" (plan 281).
	chatGPT.refreshErr = subscription.ErrKeyPasted
	rec = do(http.MethodPost, "/api/llm-models/"+id+"/refresh-key")
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "重新粘一次")
	require.NotContains(t, rec.Body.String(), "登录就是凭据")

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

// A capability answer is only the levels the client that sends this row's
// requests can carry, and efforts_known says whether anything positively listed
// such a level at all (plan 318 F1/F4). It is the same answer the pickers, the
// gate and the subagent drawer read, so the four shapes a row can have are
// pinned here: catalog levels on a Messages endpoint, the same on chat
// completions, a Google model, and a model whose only answer is a toggle.
func TestLLMModelCapsReportOnlyWhatTheClientCanSend(t *testing.T) {
	useGatewayCatalog(t, `{
		"custom/gateway-trimmed-model": {"id":"custom/gateway-trimmed-model","reasoning":true,
			"reasoning_options":[{"type":"effort","values":["minimal","low","medium","high"]}]},
		"opencode-go/gemini-gateway-model": {"id":"opencode-go/gemini-gateway-model","reasoning":true,
			"reasoning_options":[{"type":"effort","values":["none","low","high"]}]},
		"openai/gateway-toggle-model": {"id":"openai/gateway-toggle-model","reasoning":true,
			"reasoning_options":[{"type":"toggle"}]}
	}`)

	t.Run("a Messages row answers without the levels its client drops", func(t *testing.T) {
		_, do := modelsPageServer(t)
		id := createModel(t, do, `{"provider":"custom","model":"gateway-trimmed-model","base_url":"https://llm.example.com","api_path":"/v1/messages","api_key":"sk-msg"}`)
		rows, _ := llmModelRows(t, do)
		row := llmModelByID(t, rows, id)
		// minimal is the Anthropic client's to drop, so it is not an answer.
		require.Equal(t, []string{"low", "medium", "high"}, llmModelCapsOf(t, row["catalog_caps"]).Efforts)
		require.Equal(t, true, row["efforts_known"])

		// The form asks the same question while the row is being written, and
		// gets the same answer.
		rec := do(http.MethodGet, "/api/llm-models/catalog-caps?provider=custom&model=gateway-trimmed-model&api_path=/v1/messages")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body llmModelCapsBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, []string{"low", "medium", "high"}, body.Efforts)
		require.True(t, body.EffortsKnown)
	})

	t.Run("the same model on chat completions keeps every level", func(t *testing.T) {
		_, do := modelsPageServer(t)
		id := createModel(t, do, `{"provider":"custom","model":"gateway-trimmed-model","base_url":"https://llm.example.com","api_path":"/chat/completions","api_key":"sk-chat"}`)
		rows, _ := llmModelRows(t, do)
		row := llmModelByID(t, rows, id)
		require.Equal(t, []string{"minimal", "low", "medium", "high"}, llmModelCapsOf(t, row["catalog_caps"]).Efforts)
		require.Equal(t, true, row["efforts_known"])
	})

	t.Run("a Google model has no level to offer and says so", func(t *testing.T) {
		_, do := modelsPageServer(t)
		// The generateContent client carries no effort parameter, so every level
		// the catalog names for this model is unsendable: efforts_known=false is
		// the answer that lets the form offer the boxes instead of a dropdown
		// built from an empty list (plan 318 F4).
		rec := do(http.MethodGet, "/api/llm-models/catalog-caps?provider=opencode-go&model=gemini-gateway-model&api_path=/chat/completions")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body llmModelCapsBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Empty(t, body.Efforts)
		require.False(t, body.EffortsKnown)
		require.Empty(t, body.EffortsSource)
	})

	t.Run("a toggle-only model is unknown, and the person may pick the levels", func(t *testing.T) {
		_, do := modelsPageServer(t)
		id := createModel(t, do, `{"provider":"openai","model":"gateway-toggle-model","base_url":"https://api.openai.com/v1","api_key":"sk-toggle"}`)
		rows, _ := llmModelRows(t, do)
		row := llmModelByID(t, rows, id)
		require.Empty(t, llmModelCapsOf(t, row["catalog_caps"]).Efforts)
		require.Equal(t, false, row["efforts_known"], "no source listed a level this build can send")

		// The engine's gap-filler: what the person ticks is stored, and the row
		// then reports it (still not as a source's answer).
		rec := do(http.MethodPut, "/api/llm-models/"+id, `{"caps":{"efforts":["high","low"]}}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		rows, _ = llmModelRows(t, do)
		row = llmModelByID(t, rows, id)
		require.Equal(t, []string{"low", "high"}, llmModelCapsOf(t, row["catalog_caps"]).Efforts,
			"the declared levels are reported in the one picker order")
		require.Equal(t, false, row["efforts_known"])
	})

	t.Run("a vendor that lists levels no longer decides them", func(t *testing.T) {
		// Owner 2026-11-19: a row's levels are the user's assertion, so the
		// catalog naming levels for a model no longer refuses a hand edit.
		_, do := modelsPageServer(t)
		id := createModel(t, do, `{"provider":"custom","model":"gateway-trimmed-model","base_url":"https://llm.example.com","api_path":"/chat/completions","api_key":"sk-vendor"}`)
		rec := do(http.MethodPut, "/api/llm-models/"+id, `{"caps":{"efforts":["xhigh"]}}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		rows, _ := llmModelRows(t, do)
		row := llmModelByID(t, rows, id)
		require.Equal(t, []string{"xhigh"}, llmModelCapsOf(t, row["caps"]).Efforts,
			"the user's assertion is stored even though the catalog lists its own levels")
	})
}

// llmModelCapsOf decodes one capability answer the way the drawer reads it.
func llmModelCapsOf(t *testing.T, raw any) llmModelCaps {
	t.Helper()
	encoded, err := json.Marshal(raw)
	require.NoError(t, err)
	var caps llmModelCaps
	require.NoError(t, json.Unmarshal(encoded, &caps))
	return caps
}

func useGatewayCatalog(t *testing.T, raw string) {
	t.Helper()
	cat, err := llm.Parse([]byte(raw))
	require.NoError(t, err)
	llm.SetGlobalCatalogForTest(cat)
	t.Cleanup(func() {
		if data, err := llm.RepoModelsJSONForTest(); err == nil {
			if embedded, err := llm.Parse(data); err == nil {
				llm.SetGlobalCatalogForTest(embedded)
			}
		}
	})
}

// A custom row may name the provider whose official data describes its model
// (owner 2026-11-16): the value travels back with the row, and the catalog
// answer the drawer shows comes from that entry, named by catalog_id.
func TestModelsAPIRoundTripsTheCatalogProvider(t *testing.T) {
	useGatewayCatalog(t, `{
		"alibaba/qwen4-max-preview": {"id":"alibaba/qwen4-max-preview","attachment":true,"reasoning":true,
			"reasoning_options":[{"type":"effort","values":["low","high"]}],
			"limit":{"context":1000000,"output":262144}}
	}`)
	_, do := modelsPageServer(t)
	id := createModel(t, do, `{"provider":"custom","model":"qwen4-max-preview","base_url":"https://api.example.com/v1","api_path":"/chat/completions","api_key":"sk-custom","catalog_provider":"alibaba"}`)

	rows, _ := llmModelRows(t, do)
	row := llmModelByID(t, rows, id)
	require.Equal(t, "alibaba", row["catalog_provider"])
	require.Equal(t, "alibaba/qwen4-max-preview", row["catalog_id"])
	caps, ok := row["catalog_caps"].(map[string]any)
	require.True(t, ok)
	require.EqualValues(t, 1000000, caps["context_window"])

	// The same row named through the capability endpoint the drawer asks.
	rec := do(http.MethodPost, "/api/llm-models/catalog-caps",
		`{"id":"`+id+`","provider":"custom","model":"qwen4-max-preview","base_url":"https://api.example.com/v1","catalog_provider":"alibaba"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body llmModelCapsBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "alibaba/qwen4-max-preview", body.CatalogID)
	require.EqualValues(t, 1000000, *body.ContextWindow)
}

// Only a custom row names the provider its data lives under: a built-in
// provider's row is described by its own pair, so the write is refused rather
// than stored and ignored.
func TestModelsAPIRefusesACatalogProviderOnABuiltinRow(t *testing.T) {
	_, do := modelsPageServer(t)
	rec := do(http.MethodPost, "/api/llm-models",
		`{"provider":"openai","model":"gpt-5.5","base_url":"https://api.openai.com/v1","api_key":"sk-1","catalog_provider":"alibaba"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "只有自定义模型可以指定数据服务商")
}

// A row that stops being custom drops the provider it used to name: the value
// describes a custom model's data, and a built-in row has none to describe.
func TestModelsAPIClearsTheCatalogProviderWhenTheProviderChanges(t *testing.T) {
	useGatewayCatalog(t, `{
		"alibaba/qwen4-max-preview": {"id":"alibaba/qwen4-max-preview","limit":{"context":1000000}}
	}`)
	_, do := modelsPageServer(t)
	id := createModel(t, do, `{"provider":"custom","model":"qwen4-max-preview","base_url":"https://api.example.com/v1","api_path":"/chat/completions","api_key":"sk-custom","catalog_provider":"alibaba"}`)

	rec := do(http.MethodPut, "/api/llm-models/"+id,
		`{"provider":"openai","model":"gpt-5.5","base_url":"https://api.openai.com/v1"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "catalog_provider")

	rows, _ := llmModelRows(t, do)
	row := llmModelByID(t, rows, id)
	require.Equal(t, "openai", row["provider"])
	require.NotContains(t, row, "catalog_provider")
}

// The capability endpoint answers from the entry the request names, and from
// the model name alone when it names none: the two answers a drawer shows while
// the person types.
func TestModelsAPICatalogCapsEndpointHonoursTheCatalogProvider(t *testing.T) {
	useGatewayCatalog(t, `{
		"alibaba/qwen4-max-preview": {"id":"alibaba/qwen4-max-preview","limit":{"context":1000000,"output":262144}},
		"zeta/qwen4-max-preview": {"id":"zeta/qwen4-max-preview","limit":{"context":2000000,"output":32768}}
	}`)
	_, do := modelsPageServer(t)

	named := do(http.MethodGet, "/api/llm-models/catalog-caps?provider=custom&model=qwen4-max-preview&base_url=https://api.example.com/v1&catalog_provider=zeta")
	require.Equal(t, http.StatusOK, named.Code, named.Body.String())
	var withKey llmModelCapsBody
	require.NoError(t, json.Unmarshal(named.Body.Bytes(), &withKey))
	require.Equal(t, "zeta/qwen4-max-preview", withKey.CatalogID)
	require.EqualValues(t, 2000000, *withKey.ContextWindow)

	unnamed := do(http.MethodGet, "/api/llm-models/catalog-caps?provider=custom&model=qwen4-max-preview&base_url=https://api.example.com/v1")
	require.Equal(t, http.StatusOK, unnamed.Code, unnamed.Body.String())
	var withoutKey llmModelCapsBody
	require.NoError(t, json.Unmarshal(unnamed.Body.Bytes(), &withoutKey))
	require.Equal(t, "alibaba/qwen4-max-preview", withoutKey.CatalogID)
	require.EqualValues(t, 1000000, *withoutKey.ContextWindow)
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

// The picker list carries each row's own configured effort (plan 299): a
// picker that overrides one type's level has to show what the row itself gets
// when it names none, and that value is the one plan 300 computed once rather
// than a guess from the catalog.
func TestLLMModelsAPIChoicesCarryTheRowsConfiguredEffort(t *testing.T) {
	_, do := modelsPageServer(t)
	createModel(t, do, `{"provider":"deepseek","model":"deepseek-v4","base_url":"https://api.deepseek.com","api_key":"sk-first","caps":{"efforts":["low","high"],"default_effort":"high"}}`)
	createModel(t, do, `{"provider":"deepseek","model":"deepseek-v4-flash","base_url":"https://api.deepseek.com","api_key":"sk-first"}`)

	rec := do(http.MethodGet, "/api/llm-models/choices")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var choices []struct {
		Model         string `json:"model"`
		DefaultEffort string `json:"default_effort"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &choices))
	efforts := map[string]string{}
	for _, choice := range choices {
		efforts[choice.Model] = choice.DefaultEffort
	}
	require.Equal(t, "high", efforts["deepseek-v4"], "the row's configured level reaches the picker")
	require.Equal(t, "", efforts["deepseek-v4-flash"], "a row with no configured level answers empty, not a guess")
}

// The vendor listing's display name reaches the page's human-facing fields —
// the table's model_name and the picker's label — while model stays the wire
// id the edit form and a pick's identity use.
func TestLLMModelsAPIShowsVendorListingDisplayName(t *testing.T) {
	s, do := modelsPageServer(t)
	llm.SetBuiltinModels("qodercn", []llm.Model{{ID: "gfmodel", Name: "GLM-5.3-Flash"}})
	t.Cleanup(func() { llm.SetBuiltinModels("qodercn", nil) })
	// The host test binary carries no qodercn plugin, so the row is not
	// login-backed here and the upsert asks for a key; the display projection
	// under test never reads it.
	key := "sk-fixture"
	id, err := process.UpsertLLMModel(context.Background(), s.Env.SQL, process.LLMModelInput{
		Provider: "qodercn", Model: "gfmodel", BaseURL: "https://gateway.qoder.com.cn",
		APIPath: "/algo/api/v2/service/pro/sse/agent_chat_generation",
		APIKey:  &key, Source: "qodercn",
	})
	require.NoError(t, err)

	rows, _ := llmModelRows(t, do)
	row := llmModelByID(t, rows, id)
	require.Equal(t, "gfmodel", row["model"], "identity stays the wire id")
	require.Equal(t, "GLM-5.3-Flash", row["model_name"])

	rec := do(http.MethodGet, "/api/llm-models/choices")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var choices []struct {
		Model     string `json:"model"`
		ModelName string `json:"model_name"`
		Label     string `json:"label"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &choices))
	require.Len(t, choices, 1)
	require.Equal(t, "gfmodel", choices[0].Model)
	require.Equal(t, "GLM-5.3-Flash", choices[0].ModelName)
	require.Equal(t, "qodercn/GLM-5.3-Flash", choices[0].Label)
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
// (/api/chat/model, Environment.Deps.Config()) lists the row at once instead of
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
	raw := "agents:\n  definitions:\n    main:\n      primary: true\n      default_model: llm-start\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(raw), 0o600))
	// The model the process starts on lives in the table, which exists before
	// the process opens its configuration (plan 315).
	require.NoError(t, os.MkdirAll(filepath.Join(home, "state"), 0o755))
	seedDB, err := state.OpenStateFromHome(ctx, home, "")
	require.NoError(t, err)
	_, err = (&state.LLMModelStore{DB: seedDB}).Create(ctx, state.LLMModel{
		ID: "llm-start", Provider: "deepseek", Model: "deepseek-v4",
		APIKey: "sk-snapshot-start", BaseURL: "http://127.0.0.1:9/v1",
	})
	require.NoError(t, err)
	require.NoError(t, seedDB.Close())
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
		llm.SetModelOverrides(nil, nil)
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

// TestLLMModelsAPICapsValidationVocabularyOnly pins the write path's remaining
// capability refusals (owner 2026-11-19): the level vocabulary is checked
// whatever the catalog says, and the catalog itself is no longer a refusal
// authority — the 2026-11-09 "official levels" rule is overridden, so a model
// listed with levels, listed without them, or absent from the catalog all
// store the user's pick.
func TestLLMModelsAPICapsValidationVocabularyOnly(t *testing.T) {
	// The built-in table is a row of the same default layer the shipped catalog
	// fills, so this pins the vocabulary rule without disturbing the package's
	// catalog.
	llm.SetBuiltinModels("caps-test", []llm.Model{
		{ID: "with-levels", Provider: "caps-test", APIModel: "with-levels",
			ReasoningOptions: []llm.ReasoningOption{{Type: "effort", Values: []string{"low", "high"}}},
			ReasoningKnown:   true},
		{ID: "without-levels", Provider: "caps-test", APIModel: "without-levels"},
	})
	t.Cleanup(func() { llm.SetBuiltinModels("caps-test", nil) })

	s := &Server{}
	r := httptest.NewRequest(http.MethodPut, "/api/llm-models/x", nil)

	// Listed with levels: hand-entering them is stored (owner 2026-11-19).
	require.NoError(t, s.llmModelCapsValid(r, "caps-test", "with-levels", "",
		llmModelCaps{Efforts: []string{"low"}}, state.LLMModel{}))

	// A stored row that already carries exactly these levels may send them
	// back unchanged, the same as any other write.
	require.NoError(t, s.llmModelCapsValid(r, "caps-test", "with-levels", "",
		llmModelCaps{Efforts: []string{"low"}},
		state.LLMModel{CapEfforts: []string{"low"}}))

	// Listed without discrete levels: the user's pick is allowed.
	require.NoError(t, s.llmModelCapsValid(r, "caps-test", "without-levels", "",
		llmModelCaps{Efforts: []string{"low"}}, state.LLMModel{}))

	// Not in the catalog at all: allowed, the same as any listed model now.
	require.NoError(t, s.llmModelCapsValid(r, "caps-test", "no-such-model", "",
		llmModelCaps{Efforts: []string{"low"}}, state.LLMModel{}))

	// The level names themselves are checked first, whatever the catalog says:
	// the vocabulary whitelist is a completeness check, not a knowledge gate.
	require.Error(t, s.llmModelCapsValid(r, "openai", "no-such-model", "",
		llmModelCaps{Efforts: []string{"ultra"}}, state.LLMModel{}))
}

// The models form's capability answers are the user's assertion (owner
// 2026-11-19): `default_effort:"none"` and any declared level are stored as
// asked, whatever the catalog or the protocol can send — a level the client
// drops is silently not sent, and the remote endpoint decides. Only the level
// vocabulary is refused. The catalog's own answer is still *reported* (read
// projection, plan 311), unchanged by this.
func TestLLMModelCapsOffDefault(t *testing.T) {
	useGatewayCatalog(t, `{
		"openai/gateway-off-model": {"id":"openai/gateway-off-model","reasoning":true,
			"reasoning_options":[{"type":"effort","values":["none","low","high"]}]},
		"openai/gateway-plain-model": {"id":"openai/gateway-plain-model","reasoning":true,
			"reasoning_options":[{"type":"effort","values":["low","high"]}]},
		"custom/gateway-messages-model": {"id":"custom/gateway-messages-model","reasoning":true,
			"reasoning_options":[{"type":"effort","values":["none","minimal","low","medium"]}]}
	}`)

	// Each case gets its own install: a row's identity is the provider, model,
	// base URL and path, and two cases on one table would collide on it.
	capsOf := func(t *testing.T, do gatewayDo, id string) llmModelCaps {
		t.Helper()
		rows, _ := llmModelRows(t, do)
		row := llmModelByID(t, rows, id)
		raw, err := json.Marshal(row["caps"])
		require.NoError(t, err)
		var caps llmModelCaps
		require.NoError(t, json.Unmarshal(raw, &caps))
		return caps
	}

	t.Run("an off-capable row stores none", func(t *testing.T) {
		_, do := modelsPageServer(t)
		id := createModel(t, do, `{"provider":"openai","model":"gateway-off-model","base_url":"https://api.openai.com/v1","api_key":"sk-off"}`)
		rec := do(http.MethodPut, "/api/llm-models/"+id, `{"caps":{"default_effort":"none"}}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, "none", capsOf(t, do, id).DefaultEffort)
	})

	t.Run("none is stored even where the catalog lists no off switch", func(t *testing.T) {
		// Owner 2026-11-19: the user's assertion is stored; the endpoint
		// decides what a request carries.
		_, do := modelsPageServer(t)
		id := createModel(t, do, `{"provider":"openai","model":"gateway-plain-model","base_url":"https://api.openai.com/v1","api_key":"sk-plain"}`)
		rec := do(http.MethodPut, "/api/llm-models/"+id, `{"caps":{"default_effort":"none"}}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, "none", capsOf(t, do, id).DefaultEffort)
	})

	t.Run("the write's own no is stored as asked, row's yes or not", func(t *testing.T) {
		_, do := modelsPageServer(t)
		id := createModel(t, do, `{"provider":"openai","model":"gateway-off-model","base_url":"https://api.openai.com/v1","api_key":"sk-masked"}`)
		rec := do(http.MethodPut, "/api/llm-models/"+id, `{"caps":{"reasoning_off":true}}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		// The row says it can turn thinking off; this write says it cannot and
		// names none anyway. Self-contradictory input is stored as asked —
		// refusing it would be knowledge acting as an authority again.
		rec = do(http.MethodPut, "/api/llm-models/"+id, `{"caps":{"reasoning_off":false,"default_effort":"none"}}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, "none", capsOf(t, do, id).DefaultEffort)
	})

	t.Run("none is stored even on a protocol that cannot send it", func(t *testing.T) {
		_, do := modelsPageServer(t)
		id := createModel(t, do, `{"provider":"custom","model":"gateway-messages-model","base_url":"https://llm.example.com","api_path":"/v1/messages","api_key":"sk-msg"}`)
		rec := do(http.MethodPut, "/api/llm-models/"+id, `{"caps":{"reasoning_off":true,"default_effort":"none"}}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})

	t.Run("a level the protocol drops is stored as the default it names", func(t *testing.T) {
		_, do := modelsPageServer(t)
		// A model nothing documents: its levels are the form's own declaration.
		// minimal is declared even though the Messages client sends no such
		// level — the write stores it (owner 2026-11-19).
		id := createModel(t, do, `{"provider":"custom","model":"undocumented-model","base_url":"https://llm.example.com","api_path":"/v1/messages","api_key":"sk-msg-level"}`)
		rec := do(http.MethodPut, "/api/llm-models/"+id, `{"caps":{"efforts":["minimal","low"],"default_effort":"minimal"}}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, "minimal", capsOf(t, do, id).DefaultEffort)
		// The level the protocol does send is stored too.
		rec = do(http.MethodPut, "/api/llm-models/"+id, `{"caps":{"efforts":["minimal","low"],"default_effort":"low"}}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, "low", capsOf(t, do, id).DefaultEffort)
	})

	t.Run("efforts never carry none", func(t *testing.T) {
		_, do := modelsPageServer(t)
		id := createModel(t, do, `{"provider":"openai","model":"gateway-off-model","base_url":"https://api.openai.com/v1","api_key":"sk-levels"}`)
		rec := do(http.MethodPut, "/api/llm-models/"+id, `{"caps":{"efforts":["none","low"]}}`)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	})

	t.Run("the catalog answer reports the off switch", func(t *testing.T) {
		_, do := modelsPageServer(t)
		rec := do(http.MethodGet, "/api/llm-models/catalog-caps?provider=openai&model=gateway-off-model")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body llmModelCapsBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.NotNil(t, body.ReasoningOff)
		require.True(t, *body.ReasoningOff)
		// The levels are reported as the catalog names them, none included.
		require.Equal(t, []string{"none", "low", "high"}, body.Efforts)

		rec = do(http.MethodGet, "/api/llm-models/catalog-caps?provider=openai&model=gateway-plain-model")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		body = llmModelCapsBody{}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Nil(t, body.ReasoningOff)
	})

	t.Run("the dropdown's levels follow the protocol the form is on", func(t *testing.T) {
		_, do := modelsPageServer(t)
		rec := do(http.MethodGet, "/api/llm-models/catalog-caps?provider=custom&model=gateway-messages-model&api_path=/v1/messages")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body llmModelCapsBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		// The Messages client sends neither none nor minimal, so the model's own
		// off capability cannot be offered there either.
		require.Equal(t, []string{"low", "medium"}, body.Efforts)
		require.Nil(t, body.ReasoningOff)

		// The same model on a protocol that sends everything keeps them.
		rec = do(http.MethodGet, "/api/llm-models/catalog-caps?provider=custom&model=gateway-messages-model&api_path=/chat/completions")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		body = llmModelCapsBody{}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, []string{"none", "minimal", "low", "medium"}, body.Efforts)
		require.NotNil(t, body.ReasoningOff)
		require.True(t, *body.ReasoningOff)
	})

	t.Run("a row no source lists answers from its own declaration", func(t *testing.T) {
		_, do := modelsPageServer(t)
		id := createModel(t, do, `{"provider":"custom","model":"undocumented-model","base_url":"https://llm.example.com","api_path":"/v1/messages","api_key":"sk-decl","caps":{"efforts":["minimal","low"]}}`)
		rec := do(http.MethodGet, "/api/llm-models/catalog-caps?id="+id+"&provider=custom&model=undocumented-model&base_url=https://llm.example.com")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body llmModelCapsBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.False(t, body.EffortsKnown)
		// minimal is declared and the protocol drops it, so the dropdown offers
		// only what the client can send; the checkbox data is untouched.
		require.Equal(t, []string{"low"}, body.Efforts)

		rows, _ := llmModelRows(t, do)
		row := llmModelByID(t, rows, id)
		require.Equal(t, []string{"minimal", "low"}, capsOf(t, do, id).Efforts,
			"the row's own declaration is what the checkboxes replay")
		require.Equal(t, false, row["efforts_known"])
	})

	t.Run("clearing the default reads back empty", func(t *testing.T) {
		_, do := modelsPageServer(t)
		id := createModel(t, do, `{"provider":"openai","model":"gateway-off-model","base_url":"https://api.openai.com/v1","api_key":"sk-clear"}`)
		rec := do(http.MethodPut, "/api/llm-models/"+id, `{"caps":{"default_effort":"high"}}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		// The form omits the field to clear it: the whole caps block is replaced.
		rec = do(http.MethodPut, "/api/llm-models/"+id, `{"caps":{}}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Empty(t, capsOf(t, do, id).DefaultEffort)
	})

	t.Run("no api path means no protocol filter", func(t *testing.T) {
		_, do := modelsPageServer(t)
		rec := do(http.MethodGet, "/api/llm-models/catalog-caps?provider=custom&model=gateway-messages-model")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body llmModelCapsBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, []string{"none", "minimal", "low", "medium"}, body.Efforts)
	})
}

// TestModelsAPIAcceptsProviderTheBuildDoesNotKnow pins the web write path's
// half of the "configure it and it runs" invariant (owner 2026-11-19): a
// complete row naming a provider the build carries no data for is created,
// updated and given capability assertions like any other row; only
// completeness and the level vocabulary refuse.
func TestModelsAPIAcceptsProviderTheBuildDoesNotKnow(t *testing.T) {
	_, do := modelsPageServer(t)

	id := createModel(t, do, `{"provider":"novendor-xyz","model":"fake-1","base_url":"https://api.novendor.example/v1","api_path":"/chat/completions","api_key":"sk-x"}`)
	rows, _ := llmModelRows(t, do)
	row := llmModelByID(t, rows, id)
	require.Equal(t, "novendor-xyz", row["provider"])
	require.Equal(t, "fake-1", row["model"])

	// An update on the unknown provider behaves like any other row's.
	rec := do(http.MethodPut, "/api/llm-models/"+id,
		`{"provider":"novendor-xyz","model":"fake-2","base_url":"https://api.novendor.example/v1","api_path":"/chat/completions","api_key":"sk-x"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// A default effort the catalog knows nothing about is stored as the
	// user's assertion (the old knowledge gate would 400 this).
	rec = do(http.MethodPut, "/api/llm-models/"+id, `{"caps":{"default_effort":"high"}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rows, _ = llmModelRows(t, do)
	row = llmModelByID(t, rows, id)
	require.Equal(t, "high", llmModelCapsOf(t, row["caps"]).DefaultEffort)

	// Completeness still refuses: a row without a base URL is not a row.
	rec = do(http.MethodPost, "/api/llm-models",
		`{"provider":"novendor-xyz","model":"fake-1","api_key":"sk-x"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "请填写接口地址")

	// The level vocabulary still refuses, whatever the catalog knows.
	rec = do(http.MethodPut, "/api/llm-models/"+id, `{"caps":{"efforts":["ultra"]}}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// A row a setting the loader requires to resolve pins by id is refused exactly
// like a default (plan 345): the delete would leave the composed configuration
// refused, so every later turn would fail. The refusal names the setting, says
// nothing about a default, and the row stays listed.
func TestLLMModelsAPIDeleteRefusesAPinnedRow(t *testing.T) {
	s, do := modelsPageServer(t)
	pinned := createModel(t, do, `{"provider":"deepseek","model":"deepseek-v4","base_url":"https://api.deepseek.com","api_key":"sk-first"}`)
	require.NoError(t, s.Env.UpdateRootSection(context.Background(), "agents", func(cfg *appcfg.Root) error {
		cfg.Agents.SubagentModels = map[string]appcfg.SubagentConfig{"explore": {Model: pinned}}
		return nil
	}))

	rec := do(http.MethodDelete, "/api/llm-models/"+pinned)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var conflict struct {
		Error      string                        `json:"error"`
		DefaultFor []string                      `json:"default_for"`
		PinnedBy   []struct{ Kind, Name string } `json:"pinned_by"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &conflict))
	require.Len(t, conflict.PinnedBy, 1)
	require.Equal(t, "subagent", conflict.PinnedBy[0].Kind)
	require.Equal(t, "explore", conflict.PinnedBy[0].Name)
	require.Empty(t, conflict.DefaultFor)
	// The page is Chinese-first, so the sentence names the setting in Chinese
	// while the payload keeps the machine kind (plan 347).
	require.Contains(t, conflict.Error, "子代理 explore")

	rows, _ := llmModelRows(t, do)
	llmModelByID(t, rows, pinned)
}
