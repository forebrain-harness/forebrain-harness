// The subscriptions page's API as the page drives it (plan 265): the registry's
// list, a finished sign-in's offers, and the write that turns chosen offers
// into model rows.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/process"
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

// The account a stored sign-in names rides the registry's list, and the logout
// route answers every way its contract reads: 204 with the stub asked, 409
// with the external source named, 404 for a subscription that cannot remove a
// login and for an id no subscription carries. The rows the sign-in wrote
// survive the sign-out; the next listing marks their source missing.
func TestSubscriptionsAPIAccountAndLogout(t *testing.T) {
	s, do := modelsPageServer(t)
	chatGPT, openCode := loginStubs(t)
	chatGPT.state = turn.SignInCheck{State: turn.SignInUsable}
	chatGPT.hasAccount = true
	chatGPT.account = subscription.Account{Name: "dev@example.test", Detail: "plus", SharedPath: "/Users/codex/.codex/auth.json"}
	openCode.state = turn.SignInCheck{State: turn.SignInUsable}
	openCode.hasAccount = true
	openCode.account = subscription.Account{Name: "OPENCODE_API_KEY", EnvKey: true}
	previous := gatewaySubscriptions
	gatewaySubscriptions = func() []subscription.Subscription { return []subscription.Subscription{chatGPT, openCode} }
	t.Cleanup(func() { gatewaySubscriptions = previous })

	rec := do(http.MethodGet, "/api/subscriptions")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list []struct {
		ID      string `json:"id"`
		State   string `json:"state"`
		Account *struct {
			Name       string `json:"name"`
			Detail     string `json:"detail"`
			EnvKey     bool   `json:"env_key"`
			SharedPath string `json:"shared_path"`
		} `json:"account"`
		CanLogout bool `json:"can_logout"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list, 2)
	require.Equal(t, "chatgpt", list[0].ID)
	require.NotNil(t, list[0].Account, "a named account must ride the row")
	require.Equal(t, "dev@example.test", list[0].Account.Name)
	require.Equal(t, "plus", list[0].Account.Detail)
	require.Equal(t, "/Users/codex/.codex/auth.json", list[0].Account.SharedPath)
	require.True(t, list[0].CanLogout)
	// The environment's key is a sign-in this surface cannot remove: it is
	// named, and no sign-out is offered for it.
	require.NotNil(t, list[1].Account)
	require.Equal(t, "OPENCODE_API_KEY", list[1].Account.Name)
	require.True(t, list[1].Account.EnvKey)
	require.False(t, list[1].CanLogout)

	// A row this sign-in wrote survives the sign-out, and the next listing
	// marks its source missing — the login is the credential, not the row.
	empty := ""
	rowID, err := process.UpsertLLMModel(context.Background(), s.Env.SQL, process.LLMModelInput{
		Provider: "chatgpt", Model: "gpt-5-codex", BaseURL: "https://chatgpt.com/backend-api/codex",
		APIKey: &empty, Source: "chatgpt",
	})
	require.NoError(t, err)

	rec = do(http.MethodPost, "/api/subscriptions/chatgpt/logout")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Empty(t, rec.Body.String(), "204 answers nothing; the page refreshes itself")
	require.Equal(t, 1, chatGPT.logoutCount())

	chatGPT.state = turn.SignInCheck{State: turn.SignInMissing}
	rows, _ := llmModelRows(t, do)
	require.Len(t, rows, 1, "the model rows must survive the sign-out")
	require.Equal(t, rowID, rows[0]["id"])
	require.Equal(t, "missing", rows[0]["source_state"])

	// An external source answers 409 and says so, so the page never promises a
	// signed-out state it did not reach.
	chatGPT.logoutErr = fmt.Errorf("unset QODER_PERSONAL_ACCESS_TOKEN: %w", subscription.ErrExternalLogin)
	rec = do(http.MethodPost, "/api/subscriptions/chatgpt/logout")
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"external_login":true`)
	require.Contains(t, rec.Body.String(), "重新登录")
	chatGPT.logoutErr = nil

	// A subscription without the capability answers 404 in both languages'
	// words: there is no login it can remove.
	baseOnly := struct{ subscription.Subscription }{chatGPT}
	prevSubs := gatewaySubscriptions
	gatewaySubscriptions = func() []subscription.Subscription { return []subscription.Subscription{baseOnly} }
	prevByID := gatewaySubscriptionByID
	gatewaySubscriptionByID = func(id string) (subscription.Subscription, bool) {
		if strings.EqualFold(strings.TrimSpace(id), "chatgpt") {
			return baseOnly, true
		}
		return nil, false
	}
	t.Cleanup(func() {
		gatewaySubscriptions = prevSubs
		gatewaySubscriptionByID = prevByID
	})
	rec = do(http.MethodPost, "/api/subscriptions/chatgpt/logout")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "没有可退出的登录")

	// An id no subscription carries is the same 404 every route answers.
	rec = do(http.MethodPost, "/api/subscriptions/nosuch/logout")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "没有这个订阅")

	// Only POST is served; anything else is the routes' uniform 405.
	require.Equal(t, http.StatusMethodNotAllowed, do(http.MethodGet, "/api/subscriptions/chatgpt/logout").Code)
}
