// The subscriptions page's API as the page drives it (plan 265): the registry's
// list, a finished sign-in's offers, and the write that turns chosen offers
// into model rows.
package gateway

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/subscription"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/stretchr/testify/require"
)

// The subscriptions page lists what the registry offers, with each sign-in's
// state; a finished sign-in's offers can be written, and only those.
func TestSubscriptionsAPIProvision(t *testing.T) {
	_, do := modelsPageServer(t)
	chatGPT, _ := loginStubs(t)
	chatGPT.state = turn.SignInCheck{State: turn.SignInUsable}
	reasoningOff := true
	chatGPT.offers = []subscription.Offer{{
		Provider: "chatgpt", Model: "gpt-5-codex", Name: "GPT-5 Codex", BaseURL: "https://chatgpt.com/backend-api/codex", Default: true,
		ContextWindow: 400000, SupportsImages: true, KeyRequired: true, FromStaticList: true,
		Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium", ReasoningOff: &reasoningOff,
	}}
	previous := gatewaySubscriptions
	gatewaySubscriptions = func() []subscription.Subscription { return []subscription.Subscription{chatGPT} }
	t.Cleanup(func() { gatewaySubscriptions = previous })

	rec := do(http.MethodGet, "/api/subscriptions")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list []struct {
		ID    string   `json:"id"`
		Name  string   `json:"name"`
		Flows []string `json:"flows"`
		State string   `json:"state"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list, 1)
	require.Equal(t, "chatgpt", list[0].ID)
	require.Equal(t, "usable", list[0].State)
	require.Equal(t, []string{subscription.FlowBrowserCallback, subscription.FlowPasteCallback}, list[0].Flows)

	rec = do(http.MethodGet, "/api/subscriptions/chatgpt/offers")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var offers []struct {
		Provider       string `json:"provider"`
		Model          string `json:"model"`
		Name           string `json:"name"`
		Endpoint       string `json:"endpoint"`
		Default        bool   `json:"default"`
		Held           bool   `json:"held"`
		NeedsKey       bool   `json:"needs_key"`
		FromStaticList bool   `json:"from_static_list"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &offers))
	require.Len(t, offers, 1)
	// The page draws the vendor's name beside the wire id the row carries.
	require.Equal(t, "gpt-5-codex", offers[0].Model)
	require.Equal(t, "GPT-5 Codex", offers[0].Name)
	require.Equal(t, "https://chatgpt.com/backend-api/codex", offers[0].Endpoint)
	require.True(t, offers[0].Default)
	// Nothing is held before the write: a first sign-in opens on the vendor's
	// own default.
	require.False(t, offers[0].Held)
	// The two facts a plan that asks for a pasted key adds (plan 281).
	require.True(t, offers[0].NeedsKey)
	require.True(t, offers[0].FromStaticList)

	// A row the subscription never listed cannot be written through it.
	rec = do(http.MethodPost, "/api/subscriptions/chatgpt/provision",
		`{"offers":[{"provider":"chatgpt","model":"gpt-3.5","base_url":"https://chatgpt.com/backend-api/codex"}]}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "没有你选择的那一行")

	// The listed one is written, and the first model an agent gets is its
	// default. Each offer carries its own subagent authorization, which lands on
	// the row it wrote (283).
	rec = do(http.MethodPost, "/api/subscriptions/chatgpt/provision",
		`{"offers":[{"provider":"chatgpt","model":"gpt-5-codex","base_url":"https://chatgpt.com/backend-api/codex","subagent_allowed":true}]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var written struct {
		IDs []string `json:"ids"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &written))
	require.Len(t, written.IDs, 1)
	rows, _ := llmModelRows(t, do)
	writtenRow := llmModelByID(t, rows, written.IDs[0])
	require.Equal(t, true, writtenRow["subagent_allowed"])
	// The listing's capabilities from the offer landed on the row as its own:
	// the window it declared and the image switch, not the catalog's guess.
	caps, _ := writtenRow["caps"].(map[string]any)
	require.EqualValues(t, 400_000, caps["context_window"])
	require.Equal(t, true, caps["images"])
	// The listing's thinking answers land beside them: its levels, the level it
	// defaults to, and its off switch — three facts the model page reads back.
	require.Equal(t, []any{"low", "medium", "high"}, caps["efforts"])
	require.Equal(t, "medium", caps["default_effort"])
	require.Equal(t, true, caps["reasoning_off"])
	_, defaultID := llmModelRows(t, do)
	require.Equal(t, written.IDs[0], defaultID)

	// After the write the same listing says the row is this install's own,
	// which is what the page preselects.
	rec = do(http.MethodGet, "/api/subscriptions/chatgpt/offers")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var after []struct {
		Held bool `json:"held"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &after))
	require.Len(t, after, 1)
	require.True(t, after[0].Held, "a model this install holds must be marked for the page")

	// A plan that needs a key answers with its choices or its consent.
	chatGPT.provisionErr = subscription.ErrKeyChoice{Keys: []subscription.KeyCandidate{{ID: "k1", Name: "main", Tail: "1234"}}}
	rec = do(http.MethodPost, "/api/subscriptions/chatgpt/provision",
		`{"offers":[{"provider":"chatgpt","model":"gpt-5-codex","base_url":"https://chatgpt.com/backend-api/codex"}]}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"key_choice"`)
	require.Contains(t, rec.Body.String(), `"tail":"1234"`)

	chatGPT.provisionErr = subscription.ErrNoKey
	rec = do(http.MethodPost, "/api/subscriptions/chatgpt/provision",
		`{"offers":[{"provider":"chatgpt","model":"gpt-5-codex","base_url":"https://chatgpt.com/backend-api/codex"}]}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"needs_key_consent":true`)

	// A plan whose key the user pastes asks for one, naming the plan and where
	// the key comes from, and the key the page sends is what the subscription
	// was told (plan 281).
	chatGPT.provisionErr = subscription.ErrPlanKeyRequired{
		Plan: "coding-plan", PlanLabel: "Coding Plan", ConsoleURL: "https://console.example.test/apiKey",
	}
	rec = do(http.MethodPost, "/api/subscriptions/chatgpt/provision",
		`{"offers":[{"provider":"chatgpt","model":"gpt-5-codex","base_url":"https://chatgpt.com/backend-api/codex"}],"key":"ark-pasted-placeholder"}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var needKey struct {
		Error    string `json:"error"`
		NeedsKey bool   `json:"needs_key"`
		Plan     string `json:"plan"`
		Console  string `json:"console_url"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &needKey))
	require.True(t, needKey.NeedsKey)
	require.Equal(t, "coding-plan", needKey.Plan)
	require.Equal(t, "https://console.example.test/apiKey", needKey.Console)
	require.NotContains(t, rec.Body.String(), "ark-pasted-placeholder")

	// A row only a person can re-key says so instead of "the sign-in is the
	// credential".
	chatGPT.provisionErr = subscription.ErrKeyPasted
	rec = do(http.MethodPost, "/api/subscriptions/chatgpt/provision",
		`{"offers":[{"provider":"chatgpt","model":"gpt-5-codex","base_url":"https://chatgpt.com/backend-api/codex"}]}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "重新粘一次")

	// The key the page sends reaches the subscription's own write.
	chatGPT.provisionErr = nil
	rec = do(http.MethodPost, "/api/subscriptions/chatgpt/provision",
		`{"offers":[{"provider":"chatgpt","model":"gpt-5-codex","base_url":"https://chatgpt.com/backend-api/codex"}],"key":"ark-pasted-placeholder"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	chatGPT.mu.Lock()
	sent := chatGPT.provisions[len(chatGPT.provisions)-1]
	chatGPT.mu.Unlock()
	require.Equal(t, "ark-pasted-placeholder", sent.Key)

	require.Equal(t, http.StatusNotFound, do(http.MethodGet, "/api/subscriptions/zhipuai/offers").Code)
}
