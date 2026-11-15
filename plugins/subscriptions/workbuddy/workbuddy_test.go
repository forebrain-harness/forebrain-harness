// workbuddy_test.go drives every network path of the plugin against httptest
// servers with temp-dir homes: no real WorkBuddy endpoint is contacted, and no
// real token is read or written. The static table's expectations are hard-coded
// from the shipped product.json so this package tests the same on a machine
// without WorkBuddy installed.
package workbuddy

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/subscription"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// savePluginKnobs snapshots every package-level knob a test can move — the
// endpoint, the poll interval and the sign-in deadline — and restores all three
// afterwards. Each helper below calls it, so a test that moves one knob still
// cannot leave another test reading a local server, a 10 ms poll or a 100 ms
// deadline.
func savePluginKnobs(t *testing.T) {
	t.Helper()
	base, interval, timeout := workBuddyBaseURL, loginPollInterval, loginTimeout
	t.Cleanup(func() {
		workBuddyBaseURL, loginPollInterval, loginTimeout = base, interval, timeout
	})
}

// withEndpoint points the plugin at the test server for one test.
func withEndpoint(t *testing.T, server *httptest.Server) {
	t.Helper()
	savePluginKnobs(t)
	workBuddyBaseURL = server.URL
}

// withLoginTiming shortens the sign-in's poll interval and deadline.
func withLoginTiming(t *testing.T, interval, timeout time.Duration) {
	t.Helper()
	savePluginKnobs(t)
	loginPollInterval, loginTimeout = interval, timeout
}

// restoreShippedTable publishes exactly the capability rows init() installs —
// for a test that reads the table (so it does not depend on what an earlier test
// left there) and for one that replaces it (so its listing does not outlive it).
// The table is package state: a test that wiped or narrowed it would answer
// "unknown" to the next test, and to the next -count round.
func restoreShippedTable(t *testing.T) {
	t.Helper()
	publish := func() { installBuiltinModels(chatModels(staticWorkBuddyModels())) }
	publish()
	t.Cleanup(publish)
}

// testState opens a throwaway state database the credentials can live in.
func testState(t *testing.T) *sql.DB {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// storeCredentials writes one login the way the plugin does.
func storeCredentials(t *testing.T, db *sql.DB, home string, creds Credentials) {
	t.Helper()
	if err := saveCredentials(context.Background(), db, home, creds); err != nil {
		t.Fatalf("save credentials: %v", err)
	}
}

// freshCredentials is a usable login whose token is good for an hour.
func freshCredentials() Credentials {
	return Credentials{
		UID:          "uid-1",
		Name:         "Test Account",
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().Add(time.Hour),
		SessionState: "session-1",
		Domain:       "www.workbuddy.cn",
		LoginMethod:  authMethod,
	}
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

// --- the shipped table -------------------------------------------------------

func TestStaticTableMatchesOfficialProduct(t *testing.T) {
	rows := staticWorkBuddyModels()
	if len(rows) != 16 {
		t.Fatalf("static table holds %d rows, want 16 (the conversation agent's models, without auto)", len(rows))
	}
	if rows[0].ID != "hy4free" || rows[0].Name != "Hy4" {
		t.Fatalf("first row = %q/%q, want the picker's first entry hy4free/Hy4", rows[0].ID, rows[0].Name)
	}
	byID := map[string]workBuddyModel{}
	for _, m := range rows {
		byID[m.ID] = m
	}
	flash := byID["glm-5.3-flash"]
	if strings.Join(flash.Efforts, ",") != "low,high,max" {
		t.Fatalf("glm-5.3-flash efforts = %v, want [low high max]", flash.Efforts)
	}
	if flash.CanDisableThinking == nil || *flash.CanDisableThinking {
		t.Fatalf("glm-5.3-flash canDisableThinking = %v, want false", flash.CanDisableThinking)
	}
	if !byID["glm-5.2"].SupportsImages {
		t.Fatal("glm-5.2 declares image input in the product config")
	}
	if got := byID["deepseek-v4-pro"].MaxInputTokens; got != 1000000 {
		t.Fatalf("deepseek-v4-pro window = %d, want 1000000", got)
	}
	// The levels the product states are the levels the host offers, with "none"
	// first when the model can turn thinking off.
	flashLevels := thinkingLevels(flash)
	if strings.Join(flashLevels, ",") != "low,high,max" {
		t.Fatalf("glm-5.3-flash host levels = %v", flashLevels)
	}
	if levels := thinkingLevels(byID["glm-5.2"]); strings.Join(levels, ",") != "none,high,xhigh" {
		t.Fatalf("glm-5.2 host levels = %v, want the product's set behind its off switch", levels)
	}
}

func TestChatModelsKeepTheConversationSubset(t *testing.T) {
	rows := staticWorkBuddyModels()
	kept := chatModels(rows)
	if len(kept) != len(rows) {
		ids := make([]string, 0, len(kept))
		for _, m := range kept {
			ids = append(ids, m.ID)
		}
		t.Fatalf("conversation subset holds %d of %d rows: %v", len(kept), len(rows), ids)
	}
	seen := map[string]bool{}
	for _, m := range kept {
		seen[m.ID] = true
	}
	for _, id := range []string{"auto", "default", "wb-lite", "fast-model", "kling-v3-t2v", "hunyuan-image-alpha"} {
		if seen[id] {
			t.Fatalf("%q is not a conversation choice and must not be in the table", id)
		}
	}
	for _, id := range []string{"hy4free", "hy4std", "glm-5.3", "glm-5.3-flash", "deepseek-v4-pro"} {
		if !seen[id] {
			t.Fatalf("%q is a conversation choice and must be kept", id)
		}
	}
}

// --- the model catalog ------------------------------------------------------

func TestFetchModelsFallsBackToShippedTable(t *testing.T) {
	home := t.TempDir()
	records, fromStatic := FetchModels(context.Background(), nil, home)
	if !fromStatic {
		t.Fatal("no login must answer from the shipped table")
	}
	if len(records) != 16 {
		t.Fatalf("fallback holds %d rows, want the 16 conversation choices", len(records))
	}
	for _, record := range records {
		if record.imagesUndeclared {
			t.Fatalf("%q: the shipped table states its modalities, so nothing is undeclared", record.ID)
		}
	}
}

func TestFetchModelsPrefersLiveAndFillsCapabilities(t *testing.T) {
	home := t.TempDir()
	storeCredentials(t, nil, home, freshCredentials())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/config" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-1" {
			t.Errorf("listing Authorization = %q, want the login's bearer token", got)
		}
		writeJSON(w, `{"code":0,"msg":"OK","data":{
			"models":[
				{"id":"glm-5.3-flash","name":"GLM-5.3-Flash"},
				{"id":"brand-new-model","name":"Brand New"},
				{"id":"hunyuan-image-alpha"},
				{"id":"fast-model"},
				{"id":"tool-less","supportsToolCall":false}
			],
			"agents":[
				{"name":"cli","tags":["cli","default","model:craft"],
				 "models":["glm-5.3-flash","brand-new-model","tool-less"]}
			]
		}}`)
	}))
	t.Cleanup(server.Close)
	withEndpoint(t, server)

	records, fromStatic := FetchModels(context.Background(), nil, home)
	if fromStatic {
		t.Fatal("a live listing answered, so the offers are not the shipped table")
	}
	if len(records) != 2 {
		t.Fatalf("records = %v, want the agent's two chat models (an explicit supportsToolCall:false is dropped, and a catalog entry the agent does not declare is not offered)", records)
	}
	byID := map[string]workBuddyModel{}
	for _, record := range records {
		byID[record.ID] = record
	}
	for _, id := range []string{"hunyuan-image-alpha", "fast-model"} {
		if _, offered := byID[id]; offered {
			t.Fatalf("%q is in the catalog but not in the agent's models, so the picker never offers it", id)
		}
	}
	known := byID["glm-5.3-flash"]
	if known.MaxInputTokens != 1000000 || known.MaxOutputTokens != 131072 {
		t.Fatalf("glm-5.3-flash windows = %d/%d, want them filled from the shipped table", known.MaxInputTokens, known.MaxOutputTokens)
	}
	if strings.Join(known.Efforts, ",") != "low,high,max" || known.CanDisableThinking == nil || *known.CanDisableThinking {
		t.Fatalf("glm-5.3-flash thinking = %v/%v, want the shipped levels and off switch", known.Efforts, known.CanDisableThinking)
	}
	if !known.SupportsImages || known.imagesUndeclared {
		t.Fatalf("glm-5.3-flash images = %v/undeclared %v, want the shipped answer", known.SupportsImages, known.imagesUndeclared)
	}
	fresh := byID["brand-new-model"]
	if fresh.MaxInputTokens != 0 || len(fresh.Efforts) != 0 || fresh.CanDisableThinking != nil {
		t.Fatalf("live-only id stated unknown capabilities as %+v, want them unknown", fresh)
	}
	if !fresh.imagesUndeclared || fresh.SupportsImages {
		t.Fatalf("live-only id images = %v/undeclared %v, want the offer to answer nothing", fresh.SupportsImages, fresh.imagesUndeclared)
	}
}

func TestFetchModelsMergesEnterpriseList(t *testing.T) {
	accountListing := `{"code":0,"msg":"OK","data":{
		"models":[
			{"id":"glm-5.3","name":"GLM-5.3"},
			{"id":"acct-only","name":"Account Only"}
		],
		"agents":[
			{"name":"cli","tags":["cli","model:craft"],"models":["glm-5.3","acct-only"]}
		]
	}}`
	enterpriseListing := `{"code":0,"msg":"OK","data":[
		{"id":"glm-5.3","name":"Enterprise GLM"},
		{"id":"ent-only","name":"Enterprise Only"}
	]}`

	t.Run("the enterprise listing overrides and appends", func(t *testing.T) {
		home := t.TempDir()
		creds := freshCredentials()
		creds.EnterpriseID = "ent-1"
		storeCredentials(t, nil, home, creds)

		var enterpriseHits int32
		var seenPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v3/config":
				writeJSON(w, accountListing)
			case "/console/enterprises/ent-1/config/models":
				atomic.AddInt32(&enterpriseHits, 1)
				seenPath = r.URL.Path
				writeJSON(w, enterpriseListing)
			default:
				t.Errorf("unexpected request %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(server.Close)
		withEndpoint(t, server)

		records, fromStatic := FetchModels(context.Background(), nil, home)
		if fromStatic {
			t.Fatal("both listings answered")
		}
		if atomic.LoadInt32(&enterpriseHits) != 1 || seenPath != "/console/enterprises/ent-1/config/models" {
			t.Fatalf("enterprise endpoint hits = %d at %q", enterpriseHits, seenPath)
		}
		if len(records) != 3 {
			t.Fatalf("records = %+v, want the account's two ids with the enterprise entry replacing glm-5.3", records)
		}
		if records[0].ID != "glm-5.3" || records[0].Name != "Enterprise GLM" {
			t.Fatalf("glm-5.3 = %+v, want the enterprise entry in the account's place", records[0])
		}
		if records[1].ID != "acct-only" {
			t.Fatalf("second record = %q, want the account's own order kept", records[1].ID)
		}
		appended := records[2]
		if appended.ID != "ent-only" {
			t.Fatalf("third record = %q, want the enterprise-only id appended", appended.ID)
		}
		if appended.MaxInputTokens != 0 || !appended.imagesUndeclared {
			t.Fatalf("enterprise-only id = %+v, want unknown capabilities and no image answer", appended)
		}
	})

	t.Run("a refused enterprise listing does not cost the account its models", func(t *testing.T) {
		home := t.TempDir()
		creds := freshCredentials()
		creds.EnterpriseID = "ent-1"
		storeCredentials(t, nil, home, creds)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v3/config":
				writeJSON(w, accountListing)
			case "/console/enterprises/ent-1/config/models":
				w.WriteHeader(http.StatusForbidden)
			default:
				t.Errorf("unexpected request %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(server.Close)
		withEndpoint(t, server)

		records, fromStatic := FetchModels(context.Background(), nil, home)
		if fromStatic {
			t.Fatal("the account's own listing answered")
		}
		if len(records) != 2 {
			t.Fatalf("records = %+v, want the account's two models", records)
		}
	})

	t.Run("a personal account never asks for the enterprise listing", func(t *testing.T) {
		home := t.TempDir()
		storeCredentials(t, nil, home, freshCredentials())

		var enterpriseHits int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v3/config":
				writeJSON(w, accountListing)
			default:
				if strings.HasPrefix(r.URL.Path, "/console/enterprises/") {
					atomic.AddInt32(&enterpriseHits, 1)
				}
				t.Errorf("unexpected request %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(server.Close)
		withEndpoint(t, server)

		if _, fromStatic := FetchModels(context.Background(), nil, home); fromStatic {
			t.Fatal("the account's own listing answered")
		}
		if atomic.LoadInt32(&enterpriseHits) != 0 {
			t.Fatal("a login naming no enterprise must not be sent to the enterprise endpoint")
		}
	})
}

// --- registration -----------------------------------------------------------

// TestFetchModelsOffersTheSurfaceAgentListInOrder pins the picker's source: the
// conversation agent's declared models, in its order, without the routing
// alias, without catalog entries it does not declare (a disabled entry among
// them), and without ids the catalog does not carry.
func TestFetchModelsOffersTheSurfaceAgentListInOrder(t *testing.T) {
	home := t.TempDir()
	storeCredentials(t, nil, home, freshCredentials())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/config" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, `{"code":0,"msg":"OK","data":{
			"models":[
				{"id":"auto","name":"Auto","isDefault":true},
				{"id":"hy4free","name":"Hy4"},
				{"id":"glm-5.3","name":"GLM-5.3"},
				{"id":"kimi-k3.1-pro","name":"Kimi-K3.1-Pro"},
				{"id":"fast-model","name":"Fast"},
				{"id":"wb-lite","name":"Lite","disabled":true}
			],
			"agents":[
				{"name":"cli","tags":["cli","default","model:craft"],
				 "models":["auto","hy4free","glm-5.3","kimi-k3.1-pro","wb-lite","ghost-model"]}
			]
		}}`)
	}))
	t.Cleanup(server.Close)
	withEndpoint(t, server)

	records, fromStatic := FetchModels(context.Background(), nil, home)
	if fromStatic {
		t.Fatal("the account's own listing answered")
	}
	got := make([]string, 0, len(records))
	for _, record := range records {
		got = append(got, record.ID)
	}
	if want := "hy4free,glm-5.3,kimi-k3.1-pro"; strings.Join(got, ",") != want {
		t.Fatalf("offers = %v, want the agent's list in its own order: %s", got, want)
	}
	if records[0].Name != "Hy4" {
		t.Fatalf("hy4free name = %q, want the catalog's name", records[0].Name)
	}
}

// TestFetchModelsKeepsTheCatalogWhenTheListingNamesNoAgent is the guard for a
// product config that states no conversation agent: the catalog stands as it
// came, because dropping entries on a guess would hide models the account can
// call.
func TestFetchModelsKeepsTheCatalogWhenTheListingNamesNoAgent(t *testing.T) {
	home := t.TempDir()
	storeCredentials(t, nil, home, freshCredentials())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/config" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, `{"code":0,"msg":"OK","data":{"models":[
			{"id":"glm-5.3","name":"GLM-5.3"},
			{"id":"fast-model","name":"Fast"}
		]}}`)
	}))
	t.Cleanup(server.Close)
	withEndpoint(t, server)

	records, fromStatic := FetchModels(context.Background(), nil, home)
	if fromStatic {
		t.Fatal("a live listing answered, so the offers are not the shipped table")
	}
	got := make([]string, 0, len(records))
	for _, record := range records {
		got = append(got, record.ID)
	}
	if want := "glm-5.3,fast-model"; strings.Join(got, ",") != want {
		t.Fatalf("offers = %v, want the catalog as it came: %s", got, want)
	}
}

func TestPluginRegistersProviderFactoryAndSubscription(t *testing.T) {
	// The registrations are init()'s and additive; the capability table is the
	// one piece of package state another test can replace, so install the
	// shipped one here rather than trust the file order.
	restoreShippedTable(t)

	var provider *config.BuiltinProvider
	for _, candidate := range config.BuiltinProviders() {
		if candidate.ID == "workbuddy" {
			row := candidate
			provider = &row
		}
	}
	if provider == nil {
		t.Fatal("the plugin must register the workbuddy provider")
	}
	if !provider.LoginBacked || len(provider.Endpoints) != 1 {
		t.Fatalf("provider = %+v, want one login-backed endpoint", provider)
	}
	// The product endpoint itself, not the knob some test may have pointed at a
	// local server: the registration happened at init().
	if got := provider.Endpoints[0].BaseURL; got != "https://www.workbuddy.cn/v2" {
		t.Fatalf("endpoint base = %q, want the product endpoint", got)
	}
	if !llm.HasProviderFactory("workbuddy") {
		t.Fatal("the plugin must register its LLM factory")
	}
	if _, ok := subscription.ByID("workbuddy"); !ok {
		t.Fatal("the plugin must register its subscription")
	}
	// The init() also publishes the shipped capabilities, so a surface knows the
	// windows and levels before the first live listing.
	levels, known := llm.ReasoningEfforts("workbuddy", "glm-5.3-flash")
	if !known || strings.Join(levels, ",") != "low,high,max" {
		t.Fatalf("builtin levels for glm-5.3-flash = %v known=%v", levels, known)
	}
}

// --- the sign-in ------------------------------------------------------------

// loginGateway is the fake gateway's sign-in half: a state request, a token poll
// that stays pending for the first three calls, and the account answer.
func loginGateway(t *testing.T, polls *int32, seenState *http.Header) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/plugin/auth/state":
			if got := r.URL.Query().Get("platform"); got != productPlatform {
				t.Errorf("state platform = %q, want %q", got, productPlatform)
			}
			for _, header := range []string{"X-No-Authorization", "X-No-User-Id", "X-No-Enterprise-Id", "X-No-Department-Info"} {
				if r.Header.Get(header) != "true" {
					t.Errorf("state request %s = %q, want \"true\"", header, r.Header.Get(header))
				}
			}
			if r.Header.Get("Authorization") != "" {
				t.Error("the state request must carry no credential")
			}
			if seenState != nil {
				*seenState = r.Header.Clone()
			}
			writeJSON(w, `{"code":0,"msg":"OK","data":{"state":"state-1","authUrl":"https://www.workbuddy.cn/login?platform=workbuddy&state=state-1"}}`)
		case "/v2/plugin/auth/token":
			if got := r.URL.Query().Get("state"); got != "state-1" {
				t.Errorf("token poll state = %q", got)
			}
			if atomic.AddInt32(polls, 1) <= 3 {
				writeJSON(w, `{"code":11217,"msg":"login ing..."}`)
				return
			}
			writeJSON(w, `{"code":0,"msg":"OK","data":{"accessToken":"access-9","refreshToken":"refresh-9","expiresIn":7200,"refreshExpiresIn":86400,"tokenType":"Bearer","sessionState":"state-9","domain":"www.workbuddy.cn"}}`)
		case "/v2/plugin/account":
			if got := r.Header.Get("Authorization"); got != "Bearer access-9" {
				t.Errorf("account Authorization = %q, want the fresh token", got)
			}
			writeJSON(w, `{"code":0,"msg":"OK","data":{"uid":"uid-9","uin":"uin-9","type":"personal","nickname":"Nine","enterpriseId":"","idSource":""}}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestLoginExternalLinkPollsUntilToken(t *testing.T) {
	withLoginTiming(t, time.Millisecond, 2*time.Second)
	var polls int32
	server := loginGateway(t, &polls, nil)
	t.Cleanup(server.Close)
	withEndpoint(t, server)

	home := t.TempDir()
	env := subscription.Env{Home: home}

	login, err := workBuddySubscription{}.Start(context.Background(), env, subscription.FlowBrowserCallback)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	handle, ok := login.(*workBuddyLogin)
	if !ok {
		t.Fatalf("login handle is %T", login)
	}
	// The authorization URL is readable as soon as the flow has it: the official
	// client publishes it before it starts polling.
	var published atomic.Value
	go func() {
		for {
			if u := handle.AuthorizeURL(); u != "" {
				published.Store(u)
				return
			}
			time.Sleep(time.Millisecond)
			if handle.UserCode() != "" {
				return
			}
		}
	}()

	if err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if got := atomic.LoadInt32(&polls); got != 4 {
		t.Fatalf("token polls = %d, want 3 pending answers then the token", got)
	}
	if got, _ := published.Load().(string); got != "https://www.workbuddy.cn/login?platform=workbuddy&state=state-1" {
		t.Fatalf("published authorization URL = %q", got)
	}
	if got := handle.UserCode(); got != "" {
		t.Fatalf("user code = %q, want none: this sign-in confirms in the browser", got)
	}
	if err := handle.Submit(context.Background(), "http://localhost/callback?code=x"); err == nil {
		t.Fatal("Submit must refuse: there is no callback to paste")
	}

	creds, ok, err := loadCredentials(context.Background(), nil, home)
	if err != nil || !ok {
		t.Fatalf("stored login: ok=%v err=%v", ok, err)
	}
	if creds.AccessToken != "access-9" || creds.RefreshToken != "refresh-9" {
		t.Fatalf("stored tokens = %q/%q", creds.AccessToken, creds.RefreshToken)
	}
	if creds.UID != "uid-9" || creds.Name != "Nine" || creds.SessionState != "state-9" {
		t.Fatalf("stored account = %+v", creds)
	}
	if creds.LoginMethod != authMethod {
		t.Fatalf("stored login method = %q", creds.LoginMethod)
	}
	if got := time.Until(creds.ExpiresAt); got < 7100*time.Second || got > 7200*time.Second {
		t.Fatalf("stored expiry = %s away, want the answer's expiresIn", got)
	}
}

func TestLoginExternalLinkStopsAtTimeout(t *testing.T) {
	withLoginTiming(t, 10*time.Millisecond, 100*time.Millisecond)
	var polls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/plugin/auth/state":
			writeJSON(w, `{"code":0,"msg":"OK","data":{"state":"state-1","authUrl":"https://www.workbuddy.cn/login?platform=workbuddy&state=state-1"}}`)
		case "/v2/plugin/auth/token":
			atomic.AddInt32(&polls, 1)
			writeJSON(w, `{"code":11217,"msg":"login ing..."}`)
		default:
			t.Errorf("a sign-in that never completes must not reach %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	withEndpoint(t, server)

	home := t.TempDir()
	start := time.Now()
	_, err := LoginExternalLink(context.Background(), nil, home, nil)
	if err == nil || !strings.Contains(err.Error(), "did not finish in time") {
		t.Fatalf("error = %v, want the deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the poll ran %s, want it bounded by loginTimeout", elapsed)
	}
	if atomic.LoadInt32(&polls) < 2 {
		t.Fatalf("token polls = %d, want it to keep asking until the deadline", polls)
	}
	if _, ok, _ := loadCredentials(context.Background(), nil, home); ok {
		t.Fatal("a sign-in that never completed must store nothing")
	}
}

func TestLoginExternalLinkCancelStopsPolling(t *testing.T) {
	withLoginTiming(t, 10*time.Millisecond, 30*time.Second)
	var polls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/plugin/auth/state":
			writeJSON(w, `{"code":0,"msg":"OK","data":{"state":"state-1","authUrl":"https://www.workbuddy.cn/login?platform=workbuddy&state=state-1"}}`)
		case "/v2/plugin/auth/token":
			atomic.AddInt32(&polls, 1)
			writeJSON(w, `{"code":11217,"msg":"login ing..."}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	withEndpoint(t, server)

	ctx, cancel := context.WithCancel(context.Background())
	login, err := workBuddySubscription{}.Start(ctx, subscription.Env{Home: t.TempDir()}, subscription.FlowBrowserCallback)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&polls) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	wait := make(chan error, 1)
	go func() { wait <- login.Wait(ctx) }()
	cancel()
	select {
	case err := <-wait:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wait = %v, want the canceled context", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a canceled sign-in must return instead of polling on")
	}
	// The hand-written leak check: the poll loop is over, so the counter stops.
	settled := atomic.LoadInt32(&polls)
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&polls); got != settled {
		t.Fatalf("polls went from %d to %d after the cancel: the loop outlived its context", settled, got)
	}
	// Wait returns on the canceled context, before the flow's own goroutine is
	// done with it: let that goroutine finish here, so the knobs this test
	// restores are nobody's to read any more.
	select {
	case <-login.(*workBuddyLogin).done:
	case <-time.After(2 * time.Second):
		t.Fatal("the canceled sign-in's flow did not return")
	}
}

// --- refresh ----------------------------------------------------------------

func TestRefreshUsesOfficialHeaders(t *testing.T) {
	home := t.TempDir()
	old := freshCredentials()
	old.ExpiresAt = time.Now().Add(time.Hour)
	storeCredentials(t, nil, home, old)

	var seen http.Header
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/plugin/auth/token/refresh" {
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		seen = r.Header.Clone()
		raw := make([]byte, 64)
		n, _ := r.Body.Read(raw)
		body = strings.TrimSpace(string(raw[:n]))
		writeJSON(w, `{"code":0,"msg":"OK","data":{"accessToken":"access-2","refreshToken":"refresh-2","expiresIn":7200,"domain":"example.corp","sessionState":"session-2"}}`)
	}))
	t.Cleanup(server.Close)
	withEndpoint(t, server)

	fresh, err := ForceRefreshCredentials(context.Background(), nil, home)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	// No token ever reaches a log line or an error message.
	if seen.Get("X-Refresh-Token") != "refresh-1" {
		t.Fatalf("X-Refresh-Token = %q", seen.Get("X-Refresh-Token"))
	}
	if seen.Get("X-Auth-Refresh-Source") != "plugin" {
		t.Fatalf("X-Auth-Refresh-Source = %q", seen.Get("X-Auth-Refresh-Source"))
	}
	if seen.Get("X-Domain") != "www.workbuddy.cn" {
		t.Fatalf("X-Domain = %q, want the credential's own domain", seen.Get("X-Domain"))
	}
	if body != "{}" {
		t.Fatalf("refresh body = %q, want an empty object", body)
	}
	if fresh.AccessToken != "access-2" || fresh.RefreshToken != "refresh-2" {
		t.Fatalf("refreshed tokens = %q/%q", fresh.AccessToken, fresh.RefreshToken)
	}
	if !fresh.ExpiresAt.After(time.Now().Add(7100 * time.Second)) {
		t.Fatalf("refreshed expiry = %s away, want it moved forward by expiresIn", time.Until(fresh.ExpiresAt))
	}
	if fresh.Domain != "example.corp" || fresh.SessionState != "session-2" {
		t.Fatalf("refreshed login = %+v", fresh)
	}
	if fresh.UID != "uid-1" {
		t.Fatalf("a refresh must keep the account it was issued for, got %q", fresh.UID)
	}
	stored, ok, err := loadCredentials(context.Background(), nil, home)
	if err != nil || !ok || stored.AccessToken != "access-2" {
		t.Fatalf("stored after refresh = %+v ok=%v err=%v", stored, ok, err)
	}
}

func TestRefresh401ReportsSignInDead(t *testing.T) {
	home := t.TempDir()
	expired := freshCredentials()
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	storeCredentials(t, nil, home, expired)

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"code":401,"msg":"token invalid"}`))
		}))
		withEndpoint(t, server)

		_, err := EnsureFreshCredentials(context.Background(), nil, home)
		if !isUnauthorized(err) {
			t.Fatalf("refresh answered %d, error = %v, want the sign-in-required rejection", status, err)
		}
		var api *llm.APIError
		if !errors.As(err, &api) || api.StatusCode != status {
			t.Fatalf("error = %v, want the gateway's %d", err, status)
		}
		server.Close()
	}
}

func TestRefreshIsSingleFlight(t *testing.T) {
	home := t.TempDir()
	expired := freshCredentials()
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	storeCredentials(t, nil, home, expired)

	var refreshes int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/plugin/auth/token/refresh" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		n := atomic.AddInt32(&refreshes, 1)
		// A refresh token rotates on use: answering a second caller from the
		// same old value would be refused by the real gateway.
		if r.Header.Get("X-Refresh-Token") != "refresh-1" {
			t.Errorf("refresh %d spent %q twice", n, r.Header.Get("X-Refresh-Token"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		time.Sleep(20 * time.Millisecond)
		writeJSON(w, `{"code":0,"msg":"OK","data":{"accessToken":"access-2","refreshToken":"refresh-2","expiresIn":7200}}`)
	}))
	t.Cleanup(server.Close)
	withEndpoint(t, server)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := EnsureFreshCredentials(context.Background(), nil, home); err != nil {
				t.Errorf("ensure: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&refreshes); got != 1 {
		t.Fatalf("refresh endpoint called %d times, want exactly 1", got)
	}
	stored, ok, err := loadCredentials(context.Background(), nil, home)
	if err != nil || !ok {
		t.Fatalf("stored login: ok=%v err=%v", ok, err)
	}
	if stored.AccessToken != "access-2" || stored.RefreshToken != "refresh-2" {
		t.Fatalf("stored tokens = %q/%q, want the one refresh's answer", stored.AccessToken, stored.RefreshToken)
	}
}

func TestProbeStates(t *testing.T) {
	newServer := func(t *testing.T, status int) *httptest.Server {
		t.Helper()
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"code":500,"msg":"no"}`))
		}))
	}

	t.Run("missing", func(t *testing.T) {
		check := workBuddySubscription{}.Probe(context.Background(), subscription.Env{Home: t.TempDir()})
		if check.State != turn.SignInMissing {
			t.Fatalf("state = %v, want missing", check.State)
		}
		if check.Err != nil || check.Records != nil {
			t.Fatalf("missing login = %+v, want no error and no records", check)
		}
	})

	t.Run("usable", func(t *testing.T) {
		home := t.TempDir()
		storeCredentials(t, nil, home, freshCredentials())
		check := workBuddySubscription{}.Probe(context.Background(), subscription.Env{Home: home})
		if check.State != turn.SignInUsable || check.Err != nil {
			t.Fatalf("usable login = %+v", check)
		}
	})

	t.Run("dead", func(t *testing.T) {
		home := t.TempDir()
		expired := freshCredentials()
		expired.ExpiresAt = time.Now().Add(-time.Minute)
		storeCredentials(t, nil, home, expired)
		server := newServer(t, http.StatusUnauthorized)
		t.Cleanup(server.Close)
		withEndpoint(t, server)

		check := workBuddySubscription{}.Probe(context.Background(), subscription.Env{Home: home})
		if check.State != turn.SignInDead || !isUnauthorized(check.Err) {
			t.Fatalf("dead login = %+v, want the sign-in-required rejection", check)
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		home := t.TempDir()
		expired := freshCredentials()
		expired.ExpiresAt = time.Now().Add(-time.Minute)
		storeCredentials(t, nil, home, expired)
		server := newServer(t, http.StatusServiceUnavailable)
		t.Cleanup(server.Close)
		withEndpoint(t, server)

		check := workBuddySubscription{}.Probe(context.Background(), subscription.Env{Home: home})
		if check.State != turn.SignInUnreachable || check.Err == nil {
			t.Fatalf("unreachable gateway = %+v, want the stored login kept and the failure reported", check)
		}
	})
}

// --- the transport ----------------------------------------------------------

// recordingGateway answers every model request with a minimal Chat Completions
// document and records what the transport sent.
type recordingGateway struct {
	requests []*http.Request
	bodies   []string
	mu       sync.Mutex
}

func (g *recordingGateway) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 1<<20)
		n, _ := r.Body.Read(raw)
		g.mu.Lock()
		g.requests = append(g.requests, r.Clone(context.Background()))
		g.bodies = append(g.bodies, string(raw[:n]))
		g.mu.Unlock()
		writeJSON(w, `{"id":"1","object":"chat.completion","model":"glm-5.0","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}
}

// TestDisguiseHeadersWearTheAgentCLIIdentity pins the identity the official
// client presents: the User-Agent carrying the agent CLI's build (the gateway
// picks the catalog generation by it), the header set its model requests send,
// and nothing else — a header the client does not send would be a tell, so the
// whole map is whitelisted.
func TestDisguiseHeadersWearTheAgentCLIIdentity(t *testing.T) {
	creds := freshCredentials()
	creds.UID = "user 1"
	creds.EnterpriseID = "ent-9"
	creds.IDSource = "sso"
	header := disguiseHeaders(creds, "glm-5.3")

	fixed := map[string]string{
		"Accept":              "application/json",
		"Content-Type":        "application/json",
		"Authorization":       "Bearer access-1",
		"User-Agent":          "WorkBuddy/5.9.3 CLI/2.165.0",
		"X-Product":           "SaaS",
		"X-Product-Version":   "5.9.3",
		"X-IDE-Type":          "WorkBuddy",
		"X-IDE-Name":          "WorkBuddy",
		"X-IDE-Version":       "5.9.3",
		"X-Agent-Intent":      "craft",
		"X-Agent-Purpose":     "conversation",
		"X-Agent-Runtime":     "local-agent",
		"X-Requested-With":    "XMLHttpRequest",
		"X-Codebuddy-Request": "1",
		"X-Private-Data":      "true",
		"X-Auth-Method":       "browser",
		"X-Domain":            creds.Domain,
		"X-User-Id":           url.QueryEscape("user 1"),
		"X-Enterprise-Id":     "ent-9",
		"X-Tenant-Id":         "ent-9",
		"X-Model-ID":          "glm-5.3",
	}
	for name, want := range fixed {
		if got := header.Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	requestID, conversationID := header.Get("X-Request-ID"), header.Get("X-Conversation-ID")
	if requestID == "" || conversationID == "" || requestID == conversationID {
		t.Fatalf("X-Request-ID/X-Conversation-ID = %q/%q, want two distinct per-request ids", requestID, conversationID)
	}
	if got := header.Get("X-Conversation-Message-ID"); got != requestID {
		t.Fatalf("X-Conversation-Message-ID = %q, want the request id %q", got, requestID)
	}
	if got := header.Get("X-Conversation-Request-ID"); got != conversationID {
		t.Fatalf("X-Conversation-Request-ID = %q, want the conversation id %q", got, conversationID)
	}
	if header.Get("X-Userinfo") == "" {
		t.Fatal("an enterprise login states its X-Userinfo identity quadruple")
	}
	allowed := map[string]bool{}
	for _, name := range []string{
		"X-Request-ID", "X-Conversation-ID", "X-Conversation-Request-ID",
		"X-Conversation-Message-ID", "X-Userinfo",
	} {
		allowed[http.CanonicalHeaderKey(name)] = true
	}
	for name := range fixed {
		allowed[http.CanonicalHeaderKey(name)] = true
	}
	for name := range header {
		if !allowed[name] {
			t.Fatalf("unexpected header %q: every header this transport sends must be one the official client sends", name)
		}
	}
}

func TestTransportSendsOfficialHeadersAndPath(t *testing.T) {
	cases := []struct {
		name           string
		creds          Credentials
		wantUser       string
		wantEnterprise string
		wantUserinfo   bool
	}{
		{
			name: "personal account with a sign-in method",
			creds: func() Credentials {
				creds := freshCredentials()
				creds.UID = "user 1"
				creds.Domain = "example.internal"
				return creds
			}(),
			wantUser:     url.QueryEscape("user 1"),
			wantUserinfo: true,
		},
		{
			name: "enterprise account",
			creds: func() Credentials {
				creds := freshCredentials()
				creds.UID = "uid-2"
				creds.EnterpriseID = "ent-9"
				creds.IDSource = "sso"
				return creds
			}(),
			wantUser:       "uid-2",
			wantEnterprise: "ent-9",
			wantUserinfo:   true,
		},
		{
			name: "no account fields at all",
			creds: Credentials{
				AccessToken: "access-1",
				ExpiresAt:   time.Now().Add(time.Hour),
			},
			wantUserinfo: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			storeCredentials(t, nil, home, tc.creds)
			gateway := &recordingGateway{}
			server := httptest.NewServer(gateway.handler(t))
			t.Cleanup(server.Close)
			withEndpoint(t, server)

			transport := &Transport{Home: home}
			body := `{"model":"glm-5.0","messages":[{"role":"user","content":"hi"}]}`
			req := httptest.NewRequest(http.MethodPost, "http://placeholder/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := transport.RoundTrip(req)
			if err != nil {
				t.Fatalf("round trip: %v", err)
			}
			resp.Body.Close()

			if len(gateway.requests) != 1 {
				t.Fatalf("requests = %d", len(gateway.requests))
			}
			seen := gateway.requests[0]
			if seen.URL.Path != "/v2/chat/completions" {
				t.Fatalf("path = %q, want the product's model path", seen.URL.Path)
			}
			if wantHost := strings.TrimPrefix(server.URL, "http://"); seen.Host != wantHost {
				t.Fatalf("host = %q, want the product endpoint %q whatever the credential's domain says", seen.Host, wantHost)
			}
			for header, want := range map[string]string{
				"Authorization":       "Bearer access-1",
				"X-Product":           "SaaS",
				"X-Product-Version":   "5.9.3",
				"X-IDE-Type":          "WorkBuddy",
				"X-IDE-Name":          "WorkBuddy",
				"X-IDE-Version":       "5.9.3",
				"X-Agent-Intent":      "craft",
				"X-Agent-Purpose":     "conversation",
				"X-Agent-Runtime":     "local-agent",
				"X-Requested-With":    "XMLHttpRequest",
				"X-Codebuddy-Request": "1",
				"X-Auth-Method":       "browser",
				"X-Private-Data":      "true",
				"X-Model-ID":          "glm-5.0",
				"User-Agent":          "WorkBuddy/5.9.3 CLI/2.165.0",
			} {
				if got := seen.Header.Get(header); got != want {
					t.Fatalf("%s = %q, want %q", header, got, want)
				}
			}
			if seen.Header.Get("X-Request-ID") == "" || seen.Header.Get("X-Conversation-ID") == "" {
				t.Fatal("every request carries its own X-Request-ID and X-Conversation-ID")
			}
			if seen.Header.Get("X-Request-ID") == seen.Header.Get("X-Conversation-ID") {
				t.Fatal("a request id and a conversation id are different values")
			}
			if got, want := seen.Header.Get("X-Conversation-Message-ID"), seen.Header.Get("X-Request-ID"); got != want {
				t.Fatalf("X-Conversation-Message-ID = %q, want the request id %q", got, want)
			}
			if got, want := seen.Header.Get("X-Conversation-Request-ID"), seen.Header.Get("X-Conversation-ID"); got != want {
				t.Fatalf("X-Conversation-Request-ID = %q, want the conversation id %q", got, want)
			}
			if wantDomain := firstNonEmptyString(tc.creds.Domain, strings.TrimPrefix(server.URL, "http://")); seen.Header.Get("X-Domain") != wantDomain {
				t.Fatalf("X-Domain = %q, want %q (the credential's domain, not the route)", seen.Header.Get("X-Domain"), wantDomain)
			}
			switch {
			case tc.wantUser == "" && seen.Header.Get("X-User-Id") != "":
				t.Fatalf("X-User-Id = %q, want it absent when the login names no account", seen.Header.Get("X-User-Id"))
			case tc.wantUser != "" && seen.Header.Get("X-User-Id") != tc.wantUser:
				t.Fatalf("X-User-Id = %q, want %q (URL-encoded)", seen.Header.Get("X-User-Id"), tc.wantUser)
			}
			if got := seen.Header.Get("X-Enterprise-Id"); got != tc.wantEnterprise {
				t.Fatalf("X-Enterprise-Id = %q, want %q", got, tc.wantEnterprise)
			}
			if got := seen.Header.Get("X-Tenant-Id"); got != tc.wantEnterprise {
				t.Fatalf("X-Tenant-Id = %q, want %q (the tenant the client states beside the enterprise)", got, tc.wantEnterprise)
			}
			userinfo := seen.Header.Get("X-Userinfo")
			if !tc.wantUserinfo {
				if userinfo != "" {
					t.Fatalf("X-Userinfo = %q, want it absent for an account with no identity fields", userinfo)
				}
				return
			}
			raw, err := base64.StdEncoding.DecodeString(userinfo)
			if err != nil {
				t.Fatalf("X-Userinfo = %q, want base64 of the identity quadruple", userinfo)
			}
			var info map[string]string
			if err := json.Unmarshal(raw, &info); err != nil {
				t.Fatalf("X-Userinfo payload = %q", string(raw))
			}
			if info["uin"] != strings.TrimSpace(tc.creds.UID) || info["owner_uin"] != strings.TrimSpace(tc.creds.EnterpriseID) {
				t.Fatalf("X-Userinfo = %v", info)
			}
			if info["token_source"] != tc.creds.LoginMethod {
				t.Fatalf("X-Userinfo token_source = %q, want the login method", info["token_source"])
			}
			// The body is the client's, unchanged.
			if gateway.bodies[0] != body {
				t.Fatalf("body = %q, want the client's own Chat Completions document", gateway.bodies[0])
			}
		})
	}
}

func TestTransportRefreshesOnceOn401(t *testing.T) {
	home := t.TempDir()
	storeCredentials(t, nil, home, freshCredentials())

	var refreshes, calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/chat/completions":
			if atomic.AddInt32(&calls, 1) == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"token expired"}}`))
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer access-2" {
				t.Errorf("retried Authorization = %q, want the refreshed token", got)
			}
			writeJSON(w, `{"id":"1","object":"chat.completion","model":"glm-5.0","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
		case "/v2/plugin/auth/token/refresh":
			atomic.AddInt32(&refreshes, 1)
			writeJSON(w, `{"code":0,"msg":"OK","data":{"accessToken":"access-2","refreshToken":"refresh-2","expiresIn":7200}}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	withEndpoint(t, server)

	transport := &Transport{Home: home}
	req := httptest.NewRequest(http.MethodPost, "http://placeholder/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.0","messages":[]}`))
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the retried request to succeed", resp.StatusCode)
	}
	if atomic.LoadInt32(&refreshes) != 1 || atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("refreshes = %d, calls = %d, want one refresh and one retry", refreshes, calls)
	}
	stored, ok, err := loadCredentials(context.Background(), nil, home)
	if err != nil || !ok || stored.AccessToken != "access-2" {
		t.Fatalf("stored after the 401 refresh = %+v ok=%v err=%v", stored, ok, err)
	}
}

func TestTransportDoesNotLeakHostAuthorization(t *testing.T) {
	home := t.TempDir()
	storeCredentials(t, nil, home, freshCredentials())
	gateway := &recordingGateway{}
	server := httptest.NewServer(gateway.handler(t))
	t.Cleanup(server.Close)
	withEndpoint(t, server)

	transport := &Transport{Home: home}
	req := httptest.NewRequest(http.MethodPost, "http://placeholder/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.0","messages":[]}`))
	req.Header.Set("Authorization", "Bearer some-other-provider-key")
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	resp.Body.Close()

	if got := gateway.requests[0].Header.Get("Authorization"); got != "Bearer access-1" {
		t.Fatalf("Authorization = %q, want the login's token replacing whatever the row carried", got)
	}
}

func TestTransportPassesSSEThrough(t *testing.T) {
	home := t.TempDir()
	storeCredentials(t, nil, home, freshCredentials())
	t.Setenv("FOREBRAIN_HOME", home)

	var seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			`data: {"id":"1","object":"chat.completion.chunk","created":1,"model":"glm-5.0","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"},"finish_reason":null}]}` + "\n\n" +
				`data: {"id":"2","object":"chat.completion.chunk","created":1,"model":"glm-5.0","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}` + "\n\n" +
				`data: {"id":"3","object":"chat.completion.chunk","created":1,"model":"glm-5.0","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
				"data: [DONE]\n\n"))
	}))
	t.Cleanup(server.Close)
	withEndpoint(t, server)

	client, err := newWorkBuddyLLM(llm.ProviderLLMRequest{
		Model:           "glm-5.0",
		MaxOutputTokens: 64,
		ParamsJSON:      []byte(`{"stream":true}`),
	})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	result, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if seenPath != "/v2/chat/completions" {
		t.Fatalf("path = %q, want the product's model path", seenPath)
	}
	if result.Message == nil || result.Message.TextContent() != "hello" {
		t.Fatalf("reply = %+v, want the SSE stream folded into \"hello\"", result.Message)
	}
	if result.Usage == nil || result.Usage.InputTokens != 7 || result.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v, want the stream's own counts", result.Usage)
	}
}

// --- storage, provision -----------------------------------------------------

func TestCredentialsDoubleWriteAndPermissions(t *testing.T) {
	db := testState(t)
	// A home the plugin has to create for itself, so its own permissions are
	// what the assertions read.
	home := filepath.Join(t.TempDir(), "home")
	first := freshCredentials()
	storeCredentials(t, db, home, first)

	path := credentialPath(home)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("credential file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("credential file mode = %o, want 0600 (it holds tokens)", mode)
	}
	if dirInfo, err := os.Stat(home); err != nil {
		t.Fatalf("credential dir: %v", err)
	} else if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("credential dir mode = %o, want 0700", dirInfo.Mode().Perm())
	}
	storedInDB, err := (&state.ConfigStore{DB: db}).LoadCredential(context.Background(), credentialSection)
	if err != nil || len(storedInDB) == 0 {
		t.Fatalf("state store credential: %v", err)
	}
	var dbCreds Credentials
	if err := json.Unmarshal(storedInDB, &dbCreds); err != nil || dbCreds.AccessToken != first.AccessToken {
		t.Fatalf("state store credential = %+v err=%v", dbCreds, err)
	}

	// The fresher of the two wins, whichever place it is in.
	fileOnly := first
	fileOnly.AccessToken = "access-newer"
	fileOnly.ExpiresAt = first.ExpiresAt.Add(time.Hour)
	if err := saveCredentials(context.Background(), nil, home, fileOnly); err != nil {
		t.Fatalf("save file: %v", err)
	}
	loaded, ok, err := loadCredentials(context.Background(), db, home)
	if err != nil || !ok || loaded.AccessToken != "access-newer" {
		t.Fatalf("loaded = %+v ok=%v err=%v, want the fresher file's login", loaded, ok, err)
	}
	dbNewer := first
	dbNewer.AccessToken = "access-db-newer"
	dbNewer.ExpiresAt = fileOnly.ExpiresAt.Add(time.Hour)
	if err := saveCredentials(context.Background(), db, t.TempDir(), dbNewer); err != nil {
		t.Fatalf("save store: %v", err)
	}
	loaded, ok, err = loadCredentials(context.Background(), db, home)
	if err != nil || !ok || loaded.AccessToken != "access-db-newer" {
		t.Fatalf("loaded = %+v ok=%v err=%v, want the fresher store's login", loaded, ok, err)
	}
}

func TestProvisionWritesLoginBackedRows(t *testing.T) {
	db := testState(t)
	home := t.TempDir()
	storeCredentials(t, db, home, freshCredentials())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"code":0,"msg":"OK","data":{"models":[{"id":"glm-5.3-flash"},{"id":"glm-5.2"}]}}`)
	}))
	t.Cleanup(server.Close)
	withEndpoint(t, server)
	// Offers() republishes the table from the live listing; this test puts the
	// shipped one back, so nothing reads a two-row catalog afterwards.
	restoreShippedTable(t)

	sub := workBuddySubscription{}
	if id := sub.ID(); id != "workbuddy" {
		t.Fatalf("ID = %q", id)
	}
	if en, zh := sub.Label(); en != "WorkBuddy" || zh != "WorkBuddy" {
		t.Fatalf("label = %q/%q", en, zh)
	}
	flows := sub.Flows()
	if len(flows) != 2 || flows[0] != subscription.FlowBrowserCallback || flows[1] != subscription.FlowDeviceCode {
		t.Fatalf("flows = %v, want browser_callback first so the terminal can open the page itself", flows)
	}

	env := subscription.Env{Home: home, DB: db}
	offers, err := sub.Offers(context.Background(), env)
	if err != nil {
		t.Fatalf("offers: %v", err)
	}
	if len(offers) != 2 {
		t.Fatalf("offers = %+v, want the live listing's two models", offers)
	}
	if offers[0].BaseURL != workBuddyBaseURL+"/v2" || offers[0].APIPath != "" {
		t.Fatalf("offer endpoint = %q%q, want the transport's own base and the protocol's default path", offers[0].BaseURL, offers[0].APIPath)
	}
	if offers[0].ContextWindow != 1000000 {
		t.Fatalf("offer window = %d, want the shipped table's maxInputTokens", offers[0].ContextWindow)
	}
	if offers[0].FromStaticList {
		t.Fatal("a live listing must not be marked as the shipped table")
	}
	if got := strings.Join(offers[0].Efforts, ","); got != "low,high,max" || offers[0].DefaultEffort != "high" {
		t.Fatalf("offer thinking = %q/%q", got, offers[0].DefaultEffort)
	}
	if offers[0].ReasoningOff == nil || *offers[0].ReasoningOff {
		t.Fatalf("offer ReasoningOff = %v, want the shipped canDisableThinking", offers[0].ReasoningOff)
	}

	ids, err := sub.Provision(context.Background(), env, offers[:1], subscription.ProvisionOptions{})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("ids = %v", ids)
	}
	var provider, model, apiKey, source string
	if err := db.QueryRowContext(context.Background(),
		`select provider, model, api_key, source from fb_llm_models where id = ?`, ids[0]).
		Scan(&provider, &model, &apiKey, &source); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if provider != "workbuddy" || model != "glm-5.3-flash" {
		t.Fatalf("row = %q/%q", provider, model)
	}
	if apiKey != "" {
		t.Fatalf("row api_key = %q, want none: the login is the credential", apiKey)
	}
	if source != "workbuddy" {
		t.Fatalf("row source = %q", source)
	}
	if err := sub.RefreshKey(context.Background(), env, model); err != subscription.ErrNoKeyToRefresh {
		t.Fatalf("RefreshKey = %v, want ErrNoKeyToRefresh", err)
	}
}

// firstNonEmptyString is the first non-empty value.
func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
