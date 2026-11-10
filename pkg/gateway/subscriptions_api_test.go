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
	chatGPT.offers = []subscription.Offer{{
		Provider: "chatgpt", Model: "gpt-5-codex", BaseURL: "https://chatgpt.com/backend-api/codex", Default: true,
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
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Endpoint string `json:"endpoint"`
		Default  bool   `json:"default"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &offers))
	require.Len(t, offers, 1)
	require.Equal(t, "https://chatgpt.com/backend-api/codex", offers[0].Endpoint)
	require.True(t, offers[0].Default)

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
	_, defaultID := llmModelRows(t, do)
	require.Equal(t, written.IDs[0], defaultID)

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

	require.Equal(t, http.StatusNotFound, do(http.MethodGet, "/api/subscriptions/zhipuai/offers").Code)
}
