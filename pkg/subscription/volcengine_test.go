package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/stretchr/testify/require"
)

// The subscription is a built-in: it is what every surface lists third, it signs
// in with the console's device code, and it writes volcengine rows.
func TestVolcengineDescriptor(t *testing.T) {
	var sub Subscription = volcengine{}
	require.Equal(t, "volcengine", sub.ID())
	en, zh := sub.Label()
	require.Equal(t, "Volcengine Ark", en)
	require.Equal(t, "火山方舟", zh)
	require.Equal(t, []string{FlowDeviceCode}, sub.Flows())

	found, ok := ByID(" Volcengine ")
	require.True(t, ok)
	require.Equal(t, volcengineID, found.ID())
}

// The two data-plane roots this subscription offers are the provider registry's
// own Coding Plan and Agent Plan endpoints: a row the login writes and a row a
// hand-entered key writes must land on the same host, or one plan becomes two.
func TestVolcenginePlanEndpointsMatchTheProviderRegistry(t *testing.T) {
	provider, ok := appcfg.BuiltinProviderByID(volcengineID)
	require.True(t, ok)
	want := map[string]bool{}
	for _, endpoint := range provider.Endpoints {
		want[endpoint.BaseURL+" "+endpoint.APIPath] = true
	}
	for _, plan := range []string{volcengineCodingPlan, volcengineAgentPlan, volcengineAgentPlanTeam} {
		baseURL, apiPath := volcenginePlanEndpoint(plan)
		require.True(t, want[baseURL+" "+apiPath],
			"%s's endpoint %s %s is not one the provider registry offers", plan, baseURL, apiPath)
	}
	require.Equal(t, appcfg.APIPathAnthropicMessages, "/v1/messages")
}

// No sign-in is a missing one; a stored credential whose action answers is a
// finished sign-in; a refused credential is dead, and a vendor that could not be
// reached leaves the login standing.
func TestVolcengineProbeStates(t *testing.T) {
	t.Run("no sign-in", func(t *testing.T) {
		newVolcengineStub(t)
		check := (volcengine{}).Probe(context.Background(), testEnv(t))
		require.Equal(t, turn.SignInMissing, check.State)
	})
	t.Run("usable", func(t *testing.T) {
		stub, _ := newVolcengineStub(t)
		stub.results[volcengineGetPersonalPlan] = `{"PlanType":"Lite","Status":"Running"}`
		env := testEnv(t)
		seedVolcengineCredential(t, env, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
		check := (volcengine{}).Probe(context.Background(), env)
		require.Equal(t, turn.SignInUsable, check.State)
		require.Equal(t, []string{volcengineGetPersonalPlan}, stub.seenAnswers())
	})
	t.Run("no plan held is still usable", func(t *testing.T) {
		stub, _ := newVolcengineStub(t)
		stub.failures[volcengineGetPersonalPlan] = stubFailure{status: http.StatusNotFound, code: "ResourceNotFound.Plan"}
		env := testEnv(t)
		seedVolcengineCredential(t, env, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
		require.Equal(t, turn.SignInUsable, (volcengine{}).Probe(context.Background(), env).State)
	})
	t.Run("dead", func(t *testing.T) {
		stub, _ := newVolcengineStub(t)
		stub.failures[volcengineGetPersonalPlan] = stubFailure{status: http.StatusForbidden, code: "AccessDenied"}
		env := testEnv(t)
		seedVolcengineCredential(t, env, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
		require.Equal(t, turn.SignInDead, (volcengine{}).Probe(context.Background(), env).State)
	})
	t.Run("unreachable", func(t *testing.T) {
		stub, _ := newVolcengineStub(t)
		stub.failures[volcengineGetPersonalPlan] = stubFailure{status: http.StatusBadGateway, code: ""}
		env := testEnv(t)
		seedVolcengineCredential(t, env, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
		require.Equal(t, turn.SignInUnreachable, (volcengine{}).Probe(context.Background(), env).State)
	})
	t.Run("expired credential refreshes", func(t *testing.T) {
		stub, _ := newVolcengineStub(t)
		stub.results[volcengineGetPersonalPlan] = `{"PlanType":"Lite","Status":"Running"}`
		stub.tokens = []stubAnswer{{status: http.StatusOK, body: stubTokenBody(
			stubAccessToken("AKLTrenewedplaceholder", "renewed-secret-placeholder", "renewed-session-placeholder"),
			"rotated-refresh-placeholder", 900)}}
		env := testEnv(t)
		seedVolcengineCredential(t, env, time.Date(2025, 12, 31, 23, 0, 0, 0, time.UTC))
		require.Equal(t, turn.SignInUsable, (volcengine{}).Probe(context.Background(), env).State)
		credential, found, err := loadVolcengineCredential(context.Background(), env)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "AKLTrenewedplaceholder", credential.AccessKeyID)
	})
}

// The personal plans the account holds are what the picker lists, each model on
// its plan's own root, and every personal offer says it needs a pasted key.
func TestVolcengineOffersListThePersonalPlans(t *testing.T) {
	stub, _ := newVolcengineStub(t)
	stub.answer = func(action string, body map[string]any) (int, string, bool) {
		switch action {
		case volcengineGetPersonalPlan:
			plan, _ := body["Plan"].(string)
			return http.StatusOK, `{"ResponseMetadata":{"RequestId":"r"},"Result":{"PlanType":"Pro","Status":"Running","Plan":"` + plan + `"}}`, true
		case volcengineListArkCodingPlanModel:
			return http.StatusOK, `{"Result":{"Datas":[{"ModelID":"doubao-seed-2.1-pro"},{"ModelID":"glm-5.3"}]}}`, true
		case volcengineListArkAgentPlanModel:
			return http.StatusOK, `{"Result":{"Datas":[{"ModelID":"kimi-k3"}]}}`, true
		case volcengineListSeatInfos:
			return http.StatusOK, `{"Result":{"BizSummaries":[],"Data":[]}}`, true
		}
		return 0, "", false
	}
	env := testEnv(t)
	seedVolcengineCredential(t, env, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))

	offers, err := (volcengine{}).Offers(context.Background(), env)
	require.NoError(t, err)
	require.Len(t, offers, 3)

	coding := offers[0]
	require.Equal(t, volcengineCodingPlan, coding.Plan)
	require.Equal(t, "Coding Plan", coding.PlanLabel)
	require.Equal(t, "doubao-seed-2.1-pro", coding.Model)
	require.Equal(t, volcengineCodingBaseURL, coding.BaseURL)
	require.Equal(t, "/v1/messages", coding.APIPath)
	require.Equal(t, int64(256000), coding.ContextWindow)
	require.True(t, coding.SupportsImages)
	require.True(t, coding.KeyRequired)
	require.True(t, coding.Default, "a first sign-in ticks one model")
	require.False(t, coding.FromStaticList)
	require.Equal(t, volcengineID, coding.Provider)
	require.Equal(t, volcengineID, coding.Subscription)

	agent := offers[2]
	require.Equal(t, volcengineAgentPlan, agent.Plan)
	require.Equal(t, volcengineAgentBaseURL, agent.BaseURL)
	require.True(t, agent.KeyRequired)
	require.False(t, agent.Default)

	// The Agent Plan's personal catalog asked for the personal edition.
	require.Equal(t, map[string]any{"Edition": "personal"}, stub.lastBody(t, volcengineListArkAgentPlanModel))
}

// A listing that fails falls back to the shipped table for that plan, marked as
// static so the page says the console is the authority; a plan the account does
// not hold is not offered at all.
func TestVolcengineOffersFallBackToTheShippedTable(t *testing.T) {
	stub, _ := newVolcengineStub(t)
	stub.answer = func(action string, body map[string]any) (int, string, bool) {
		switch action {
		case volcengineGetPersonalPlan:
			plan, _ := body["Plan"].(string)
			if plan == "AgentPlan" {
				return http.StatusNotFound, `{"ResponseMetadata":{"Error":{"Code":"ResourceNotFound.Plan"}}}`, true
			}
			return http.StatusOK, `{"Result":{"PlanType":"Lite","Status":"Running"}}`, true
		case volcengineListArkCodingPlanModel:
			return http.StatusInternalServerError, `{"ResponseMetadata":{"Error":{"Code":"InternalError"}}}`, true
		case volcengineListSeatInfos:
			return http.StatusOK, `{"Result":{"Data":[]}}`, true
		}
		return 0, "", false
	}
	env := testEnv(t)
	seedVolcengineCredential(t, env, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))

	offers, err := (volcengine{}).Offers(context.Background(), env)
	require.NoError(t, err)
	require.NotEmpty(t, offers)
	for _, offer := range offers {
		require.Equal(t, volcengineCodingPlan, offer.Plan, "the Agent Plan is not held")
		require.True(t, offer.FromStaticList)
		require.True(t, offer.KeyRequired)
		require.Equal(t, volcengineCodingBaseURL, offer.BaseURL)
	}
	provider, ok := appcfg.BuiltinProviderByID(volcengineID)
	require.True(t, ok)
	require.Len(t, offers, len(provider.Models), "the shipped table is what a failed listing falls back to")
}

// The plan listings carry model ids only, so the shipped table is the only
// place image support can come from. A model the table does not name is offered
// with its image switch undeclared rather than off — nobody said that model
// rejects pictures, and a row answering "no images" for it turned away every
// image in the turn (owner report 2026-11-13: kimi-k3.1-pro).
func TestVolcengineOffersDeclareOnlyWhatTheTableNames(t *testing.T) {
	stub, _ := newVolcengineStub(t)
	stub.answer = func(action string, body map[string]any) (int, string, bool) {
		switch action {
		case volcengineGetPersonalPlan:
			plan, _ := body["Plan"].(string)
			return http.StatusOK, `{"ResponseMetadata":{"RequestId":"r"},"Result":{"PlanType":"Pro","Status":"Running","Plan":"` + plan + `"}}`, true
		case volcengineListArkCodingPlanModel:
			return http.StatusOK, `{"Result":{"Datas":[{"ModelID":"glm-5.3"}]}}`, true
		case volcengineListArkAgentPlanModel:
			return http.StatusOK, `{"Result":{"Datas":[{"ModelID":"kimi-k3.1-pro"},{"ModelID":"kimi-k3"}]}}`, true
		case volcengineListSeatInfos:
			return http.StatusOK, `{"Result":{"BizSummaries":[],"Data":[]}}`, true
		}
		return 0, "", false
	}
	env := testEnv(t)
	seedVolcengineCredential(t, env, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))

	offers, err := (volcengine{}).Offers(context.Background(), env)
	require.NoError(t, err)
	byModel := make(map[string]Offer, len(offers))
	for _, offer := range offers {
		byModel[offer.Model] = offer
	}

	unknown := byModel["kimi-k3.1-pro"]
	require.True(t, unknown.ImagesUndeclared, "a model the table does not name declares nothing about images")
	require.False(t, unknown.SupportsImages)
	require.Zero(t, unknown.ContextWindow, "nor a window")

	named := byModel["kimi-k3"]
	require.False(t, named.ImagesUndeclared, "the table names kimi-k3")
	require.True(t, named.SupportsImages, "and says it takes images")

	// The other answer the table can give: a model it names without image
	// support stays declared, so its row still refuses pictures.
	text := byModel["glm-5.3"]
	require.False(t, text.ImagesUndeclared, "the table names glm-5.3")
	require.False(t, text.SupportsImages)
}

// A running team seat is offered from the enterprise catalog, and its rows take
// the seat's own key, so no pasted key is asked for.
func TestVolcengineOffersListTheTeamSeat(t *testing.T) {
	stub, _ := newVolcengineStub(t)
	stub.answer = func(action string, body map[string]any) (int, string, bool) {
		switch action {
		case volcengineGetPersonalPlan:
			return http.StatusNotFound, `{"ResponseMetadata":{"Error":{"Code":"ResourceNotFound.Plan"}}}`, true
		case volcengineListSeatInfos:
			return http.StatusOK, `{"Result":{"BizSummaries":[{"BizInfo":"Enterprise","ActiveCount":1,"TotalCount":1}],` +
				`"Data":[{"SeatID":"seat-2","BizInfo":"Enterprise","SeatStatus":2,"BillingStatus":2},` +
				`{"SeatID":"seat-1","BizInfo":"Enterprise","SeatStatus":2,"BillingStatus":2}]}}`, true
		case volcengineListArkAgentPlanModel:
			return http.StatusOK, `{"Result":{"Datas":[{"ModelID":"doubao-seed-2.1-pro"}]}}`, true
		}
		return 0, "", false
	}
	env := testEnv(t)
	seedVolcengineCredential(t, env, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))

	offers, err := (volcengine{}).Offers(context.Background(), env)
	require.NoError(t, err)
	require.Len(t, offers, 1)
	require.Equal(t, volcengineAgentPlanTeam, offers[0].Plan)
	require.Equal(t, "Agent Plan · Enterprise", offers[0].PlanLabel)
	require.Equal(t, volcengineAgentBaseURL, offers[0].BaseURL)
	require.False(t, offers[0].KeyRequired)

	// The seat listing asked for running seats, and the catalog for the
	// enterprise edition — inside the handler's goroutine a test only records,
	// so the bodies are read back here.
	seatFilter, ok := stub.lastBody(t, volcengineListSeatInfos)["Filter"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(2), seatFilter["SeatStatus"])
	require.Equal(t, map[string]any{"Edition": "enterprise"}, stub.lastBody(t, volcengineListArkAgentPlanModel))
}

// An account that holds nothing says so instead of offering an empty picker.
func TestVolcengineOffersAnswerAnEmptyAccount(t *testing.T) {
	stub, _ := newVolcengineStub(t)
	stub.answer = func(action string, body map[string]any) (int, string, bool) {
		switch action {
		case volcengineGetPersonalPlan:
			return http.StatusNotFound, `{"ResponseMetadata":{"Error":{"Code":"ResourceNotFound.Plan"}}}`, true
		case volcengineListSeatInfos:
			return http.StatusOK, `{"Result":{"Data":[]}}`, true
		}
		return 0, "", false
	}
	env := testEnv(t)
	seedVolcengineCredential(t, env, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	_, err := (volcengine{}).Offers(context.Background(), env)
	require.EqualError(t, err, "this Volcengine account holds no Ark plan")
}

// A personal plan writes nothing without the key the user pastes, and says which
// plan is asking.
func TestVolcengineProvisionNeedsThePastedKey(t *testing.T) {
	newVolcengineStub(t)
	env := testEnv(t)
	offers := []Offer{{
		Subscription: volcengineID, Plan: volcengineCodingPlan, PlanLabel: "Coding Plan", Provider: volcengineID,
		Model: "doubao-seed-2.1-pro", BaseURL: volcengineCodingBaseURL, APIPath: "/v1/messages", KeyRequired: true,
	}}
	_, err := (volcengine{}).Provision(context.Background(), env, offers, ProvisionOptions{})
	var need ErrPlanKeyRequired
	require.ErrorAs(t, err, &need)
	require.Equal(t, volcengineCodingPlan, need.Plan)
	require.Equal(t, "Coding Plan", need.PlanLabel)
	require.Equal(t, volcengineKeyConsoleURL, need.ConsoleURL)
	require.EqualError(t, err, "the Coding Plan plan needs an API key from the vendor's console")
	require.Zero(t, countVolcengineRows(t, env))
}

// The key the user pastes is what the row carries — and it never reaches an
// error text.
func TestVolcengineProvisionWritesThePastedKey(t *testing.T) {
	newVolcengineStub(t)
	env := testEnv(t)
	const pasted = "ark-pasted-placeholder-key"
	offers := []Offer{{
		Subscription: volcengineID, Plan: volcengineCodingPlan, PlanLabel: "Coding Plan", Provider: volcengineID,
		Model: "doubao-seed-2.1-pro", BaseURL: volcengineCodingBaseURL, APIPath: "/v1/messages", KeyRequired: true,
	}}
	ids, err := (volcengine{}).Provision(context.Background(), env, offers, ProvisionOptions{Key: pasted})
	require.NoError(t, err)
	require.Len(t, ids, 1)

	var key, source, plan, baseURL string
	require.NoError(t, env.DB.QueryRowContext(context.Background(),
		`SELECT api_key, source, source_plan, base_url FROM fb_llm_models WHERE id = ?`, ids[0]).
		Scan(&key, &source, &plan, &baseURL))
	require.Equal(t, pasted, key)
	require.Equal(t, volcengineID, source)
	require.Equal(t, volcengineCodingPlan, plan)
	require.Equal(t, volcengineCodingBaseURL, baseURL)

	// A write that cannot land must not repeat the key in its sentence.
	_, err = (volcengine{}).Provision(context.Background(), Env{Home: env.Home, DB: nil}, offers, ProvisionOptions{Key: pasted})
	require.Error(t, err)
	require.NotContains(t, err.Error(), pasted)
}

// A row that already holds its plan's key is updated without asking for it
// again; only a row that would end up keyless requires the paste.
func TestVolcengineProvisionKeepsTheKeyARowAlreadyHas(t *testing.T) {
	newVolcengineStub(t)
	env := testEnv(t)
	offer := Offer{
		Subscription: volcengineID, Plan: volcengineCodingPlan, PlanLabel: "Coding Plan", Provider: volcengineID,
		Model: "doubao-seed-2.1-pro", BaseURL: volcengineCodingBaseURL, APIPath: "/v1/messages", KeyRequired: true,
	}
	ids, err := (volcengine{}).Provision(context.Background(), env, []Offer{offer}, ProvisionOptions{Key: "first-key-placeholder"})
	require.NoError(t, err)

	// The same offer again, with no key pasted: the row keeps the key it has.
	again, err := (volcengine{}).Provision(context.Background(), env, []Offer{offer}, ProvisionOptions{})
	require.NoError(t, err)
	require.Equal(t, ids, again)
	var key string
	require.NoError(t, env.DB.QueryRowContext(context.Background(),
		`SELECT api_key FROM fb_llm_models WHERE id = ?`, ids[0]).Scan(&key))
	require.Equal(t, "first-key-placeholder", key)
}

// A row this install does not hold yet still needs its plan's key, and the
// refusal names the plan.
func TestVolcengineProvisionStillAsksForAFreshRow(t *testing.T) {
	newVolcengineStub(t)
	env := testEnv(t)
	_, err := (volcengine{}).Provision(context.Background(), env, []Offer{{
		Subscription: volcengineID, Plan: volcengineAgentPlan, PlanLabel: "Agent Plan", Provider: volcengineID,
		Model: "glm-5.3", BaseURL: volcengineAgentBaseURL, APIPath: "/v1/messages", KeyRequired: true,
	}}, ProvisionOptions{})
	var need ErrPlanKeyRequired
	require.ErrorAs(t, err, &need)
	require.Equal(t, volcengineAgentPlan, need.Plan)
}

// A team row takes the seat's key from the control plane; the pasted key is not
// what it carries.
func TestVolcengineProvisionFetchesTheSeatKey(t *testing.T) {
	stub, _ := newVolcengineStub(t)
	stub.results[volcengineListSeatInfos] = `{"Data":[{"SeatID":"seat-1","BizInfo":"Enterprise","SeatStatus":2,"BillingStatus":2}]}`
	stub.results[volcengineGetTeamSeatAPIKey] = `{"APIKey":"seat-key-placeholder"}`
	env := testEnv(t)
	seedVolcengineCredential(t, env, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	offers := []Offer{{
		Subscription: volcengineID, Plan: volcengineAgentPlanTeam, PlanLabel: "Agent Plan · Enterprise",
		Provider: volcengineID, Model: "doubao-seed-2.1-pro", BaseURL: volcengineAgentBaseURL, APIPath: "/v1/messages",
	}}
	ids, err := (volcengine{}).Provision(context.Background(), env, offers, ProvisionOptions{Key: "a-pasted-key"})
	require.NoError(t, err)
	require.Len(t, ids, 1)

	var key, plan string
	require.NoError(t, env.DB.QueryRowContext(context.Background(),
		`SELECT api_key, source_plan FROM fb_llm_models WHERE id = ?`, ids[0]).Scan(&key, &plan))
	require.Equal(t, "seat-key-placeholder", key)
	require.Equal(t, volcengineAgentPlanTeam, plan)
	require.Equal(t, map[string]any{"SeatId": "seat-1"}, stub.lastBody(t, volcengineGetTeamSeatAPIKey))
}

// Refreshing a personal row answers that its key is pasted, not fetched; a team
// row is refetched from its seat and updated in place.
func TestVolcengineRefreshKeyAnswersEachKindOfRow(t *testing.T) {
	stub, _ := newVolcengineStub(t)
	env := testEnv(t)
	seedVolcengineCredential(t, env, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))

	personal, err := (volcengine{}).Provision(context.Background(), env, []Offer{{
		Subscription: volcengineID, Plan: volcengineCodingPlan, PlanLabel: "Coding Plan", Provider: volcengineID,
		Model: "doubao-seed-2.1-pro", BaseURL: volcengineCodingBaseURL, APIPath: "/v1/messages", KeyRequired: true,
	}}, ProvisionOptions{Key: "personal-placeholder-key"})
	require.NoError(t, err)
	require.EqualError(t, (volcengine{}).RefreshKey(context.Background(), env, personal[0]),
		"this plan's key is pasted by hand; paste a new one from the console")
	require.ErrorIs(t, (volcengine{}).RefreshKey(context.Background(), env, personal[0]), ErrKeyPasted)

	stub.results[volcengineListSeatInfos] = `{"Data":[{"SeatID":"seat-1","BizInfo":"Enterprise","SeatStatus":2,"BillingStatus":2}]}`
	stub.results[volcengineGetTeamSeatAPIKey] = `{"APIKey":"seat-key-first"}`
	team, err := (volcengine{}).Provision(context.Background(), env, []Offer{{
		Subscription: volcengineID, Plan: volcengineAgentPlanTeam, PlanLabel: "Agent Plan · Enterprise",
		Provider: volcengineID, Model: "glm-5.3", BaseURL: volcengineAgentBaseURL, APIPath: "/v1/messages",
	}}, ProvisionOptions{})
	require.NoError(t, err)

	stub.mu.Lock()
	stub.results[volcengineGetTeamSeatAPIKey] = `{"APIKey":"seat-key-rotated"}`
	stub.mu.Unlock()
	require.NoError(t, (volcengine{}).RefreshKey(context.Background(), env, team[0]))
	var key string
	require.NoError(t, env.DB.QueryRowContext(context.Background(),
		`SELECT api_key FROM fb_llm_models WHERE id = ?`, team[0]).Scan(&key))
	require.Equal(t, "seat-key-rotated", key)
}

// The exchange answers a refusal as the OAuth code it names, so the poll loop
// branches on a value rather than on a message.
func TestVolcengineOAuthRefusalCarriesTheCode(t *testing.T) {
	err := &volcengineOAuthRefusal{Status: http.StatusBadRequest, Code: "authorization_pending"}
	code, refused := volcengineRefusalCode(err)
	require.True(t, refused)
	require.Equal(t, "authorization_pending", code)

	_, refused = volcengineRefusalCode(errors.New("connection reset"))
	require.False(t, refused)
	var api *volcengineError
	require.False(t, errors.As(errors.New("connection reset"), &api))
	require.Equal(t, "signin.volcengine.com answered authorization_pending", err.Error())
}

// countVolcengineRows is how many rows this subscription has written so far.
func countVolcengineRows(t *testing.T, env Env) int {
	t.Helper()
	var count int
	require.NoError(t, env.DB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM fb_llm_models WHERE source = ?`, volcengineID).Scan(&count))
	return count
}

// volcengineStub stands in for both hosts the subscription talks to: the console
// sign-in (by path) and the Ark control plane (by the Action query). A test
// scripts the answers it wants and reads back exactly what was sent, so no test
// reaches Volcengine and no test needs a credential.
type volcengineStub struct {
	server *httptest.Server

	mu sync.Mutex
	// device is the console's device authorization answer; deviceStatus, when
	// set, refuses the request with that status instead.
	device       volcengineDeviceAuth
	deviceStatus int
	// tokens is the scripted token-endpoint answers, taken in order.
	tokens []stubAnswer
	// results is each action's Result JSON; failures refuse an action instead.
	results  map[string]string
	failures map[string]stubFailure
	// answer, when set, decides an action's whole reply — status and body — from
	// the action and the body it arrived with, for the actions a test varies by
	// their own fields (GetPersonalPlan names one plan per call).
	answer func(action string, body map[string]any) (status int, payload string, custom bool)

	actions []string
	queries []string
	auth    []string
	bodies  map[string]json.RawMessage
	forms   []stubForm
}

// stubForm is one form as it arrived, with the path it was posted to.
type stubForm struct {
	path string
	form url.Values
}

// stubAnswer is one scripted HTTP answer: a status (0 = 200) and a body.
type stubAnswer struct {
	status int
	body   string
}

// stubFailure refuses one action: an HTTP status and the error code the control
// plane's envelope carries.
type stubFailure struct {
	status int
	code   string
}

// newVolcengineStub starts the stand-in and points both of the subscription's
// factories at it. clock fixes the time both hosts see; sleep records the poll
// loop's waits instead of serving them.
func newVolcengineStub(t *testing.T) (*volcengineStub, *[]time.Duration) {
	t.Helper()
	stub := &volcengineStub{results: map[string]string{}, failures: map[string]stubFailure{}}
	stub.server = httptest.NewServer(stub)
	t.Cleanup(stub.server.Close)

	waits := &[]time.Duration{}
	clock := func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	sleep := func(ctx context.Context, d time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		*waits = append(*waits, d)
		return nil
	}
	previousSignIn, previousClient := newVolcengineSignIn, newVolcengineClient
	newVolcengineSignIn = func() *volcengineSignIn {
		return &volcengineSignIn{Transport: stub.server.Client().Transport, Endpoint: stub.server.URL, Now: clock, Sleep: sleep}
	}
	newVolcengineClient = func() *volcengineClient {
		return &volcengineClient{Transport: stub.server.Client().Transport, Endpoint: stub.server.URL, Now: clock}
	}
	t.Cleanup(func() { newVolcengineSignIn, newVolcengineClient = previousSignIn, previousClient })
	return stub, waits
}

func (s *volcengineStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.URL.Path {
	case volcengineDeviceAuthPath:
		form, _ := url.ParseQuery(string(raw))
		s.forms = append(s.forms, stubForm{path: volcengineDeviceAuthPath, form: form})
		if s.deviceStatus != 0 {
			w.WriteHeader(s.deviceStatus)
			_, _ = io.WriteString(w, `{"error":"invalid_client","error_description":"unknown client"}`)
			return
		}
		s.write(w, http.StatusOK, s.device)
		return
	case volcengineTokenPath:
		form, _ := url.ParseQuery(string(raw))
		s.forms = append(s.forms, stubForm{path: volcengineTokenPath, form: form})
		if len(s.tokens) == 0 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "no scripted token answer")
			return
		}
		next := s.tokens[0]
		s.tokens = s.tokens[1:]
		status := next.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, next.body)
		return
	}
	action := r.URL.Query().Get("Action")
	s.actions = append(s.actions, action)
	s.queries = append(s.queries, r.URL.RawQuery)
	s.auth = append(s.auth, r.Header.Get("Authorization"))
	if s.bodies == nil {
		s.bodies = map[string]json.RawMessage{}
	}
	s.bodies[action] = raw
	if s.answer != nil {
		var decoded map[string]any
		_ = json.Unmarshal(raw, &decoded)
		if status, payload, custom := s.answer(action, decoded); custom {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, payload)
			return
		}
	}
	if failure, refused := s.failures[action]; refused {
		status := failure.status
		if status == 0 {
			status = http.StatusBadRequest
		}
		code := failure.code
		if code == "" {
			code = "InternalError"
		}
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"ResponseMetadata":{"RequestId":"req","Error":{"Code":%q,"Message":"refused"}}}`, code)
		return
	}
	result, known := s.results[action]
	if !known {
		result = "{}"
	}
	fmt.Fprintf(w, `{"ResponseMetadata":{"RequestId":"req"},"Result":%s}`, result)
}

func (s *volcengineStub) write(w http.ResponseWriter, status int, body any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// seenAnswers is every action the stub answered, in order.
func (s *volcengineStub) seenAnswers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.actions...)
}

// lastBody is the JSON body the named action was sent.
func (s *volcengineStub) lastBody(t *testing.T, action string) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.bodies[action]
	require.True(t, ok, "the stub never answered %s", action)
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))
	return body
}

// formsFor returns the forms posted to one sign-in path, in arrival order.
func (s *volcengineStub) formsFor(path string) []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	forms := make([]url.Values, 0, len(s.forms))
	for _, entry := range s.forms {
		if entry.path == path {
			forms = append(forms, entry.form)
		}
	}
	return forms
}

// seedVolcengineCredential stores a sign-in good for the stub's clock, so a test
// starts from "this machine is signed in" without a browser.
func seedVolcengineCredential(t *testing.T, env Env, expiresAt time.Time) volcengineCredential {
	t.Helper()
	credential := volcengineCredential{
		AccessKeyID:     "AKLTstubstubstubstubstubstubstub",
		SecretAccessKey: "stub-secret-access-key",
		SessionToken:    "stub-session-token",
		IssuedAt:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
		ExpiresAt:       expiresAt.Unix(),
		RefreshToken:    "stub-refresh-token",
		ClientID:        volcengineClientID,
		Scope:           volcengineScope,
	}
	require.NoError(t, saveVolcengineCredential(context.Background(), env, credential))
	return credential
}
