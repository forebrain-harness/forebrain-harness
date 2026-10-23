package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubConsole points the device flow at one fake console. Both the API root and
// the web root move, because the verification URL the code endpoint returns is a
// relative path that the flow resolves against the web root.
func stubConsole(t *testing.T, serverURL string) {
	t.Helper()
	oldAPI, oldWeb := consoleAPIBase, consoleWebBase
	consoleAPIBase, consoleWebBase = serverURL, serverURL
	t.Cleanup(func() {
		consoleAPIBase, consoleWebBase = oldAPI, oldWeb
	})
}

// stubPollFloor collapses the polling delay so a test can watch several polls
// without waiting the protocol's seconds.
func stubPollFloor(t *testing.T) {
	t.Helper()
	oldFloor, oldIncrease := devicePollFloor, deviceSlowDownIncrement
	devicePollFloor = time.Millisecond
	deviceSlowDownIncrement = time.Millisecond
	t.Cleanup(func() {
		devicePollFloor, deviceSlowDownIncrement = oldFloor, oldIncrease
	})
}

func TestSaveLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	want := Credentials{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().Add(time.Hour).UTC().Truncate(time.Second),
		AccountID:    "usr_1",
		Email:        "dev@example.com",
		OrgID:        "org_1",
		OrgName:      "Acme",
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credentials: %v", err)
	}
	// A refresh token is a credential: the file must not be group or world
	// readable.
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("credentials mode = %o, want 600", mode)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken {
		t.Fatalf("tokens = %q/%q, want %q/%q", got.AccessToken, got.RefreshToken, want.AccessToken, want.RefreshToken)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("expiry = %v, want %v", got.ExpiresAt, want.ExpiresAt)
	}
	if got.AccountID != want.AccountID || got.Email != want.Email || got.OrgID != want.OrgID || got.OrgName != want.OrgName {
		t.Fatalf("profile = %+v, want %+v", got, want)
	}
}

func TestLoadRejectsIncompleteCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	if err := os.WriteFile(path, []byte(`{"access_token":"access-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted a login with no refresh token")
	} else if !strings.Contains(err.Error(), "/connect") {
		t.Fatalf("error = %v, want it to point at /connect", err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("Load accepted a missing file")
	}
}

// The full device flow against a fake console: code, two pending polls, the
// grant, then the profile calls. It pins the wire details the live console
// requires — JSON bodies (not form encoding), the official client id, the poll
// grant type — because getting any of them wrong makes the flow fail only in
// production.
func TestLoginDeviceFlowCompletesAgainstFakeConsole(t *testing.T) {
	stubPollFloor(t)
	var (
		mu          sync.Mutex
		polls       int
		codeRequest map[string]any
		bearerSeen  []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if ct := r.Header.Get("Content-Type"); r.Method == http.MethodPost && ct != "application/json" {
			t.Errorf("%s Content-Type = %q, want application/json", r.URL.Path, ct)
		}
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "forebrain-harness/") {
			t.Errorf("%s User-Agent = %q, want a forebrain-harness/<version> identity", r.URL.Path, ua)
		}
		switch r.URL.Path {
		case "/auth/device/code":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode device code body: %v", err)
			}
			codeRequest = body
			writeJSON(t, w, http.StatusOK, map[string]any{
				"device_code":               "device-1",
				"user_code":                 "ABCD-EFGH",
				"verification_uri_complete": "/console/device?user_code=ABCD-EFGH&client_id=opencode-cli",
				"expires_in":                600,
				"interval":                  0,
			})
		case "/auth/device/token":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode token body: %v", err)
			}
			polls++
			switch polls {
			case 1, 2:
				if got := body["grant_type"]; got != "urn:ietf:params:oauth:grant-type:device_code" {
					t.Errorf("grant_type = %v, want the device grant", got)
				}
				if got := body["device_code"]; got != "device-1" {
					t.Errorf("device_code = %v", got)
				}
				// The real endpoint reports this with an error status, so the
				// flow must read the body rather than trust the status.
				writeJSON(t, w, http.StatusBadRequest, map[string]any{"error": "authorization_pending"})
			case 3:
				// No org id: the login must fall back to the first org by name.
				writeJSON(t, w, http.StatusOK, map[string]any{
					"access_token":  "access-1",
					"refresh_token": "refresh-1",
					"expires_in":    3600,
				})
			default:
				t.Errorf("unexpected poll %d", polls)
				writeJSON(t, w, http.StatusBadRequest, map[string]any{"error": "authorization_pending"})
			}
		case "/api/user":
			bearerSeen = append(bearerSeen, r.Header.Get("Authorization"))
			writeJSON(t, w, http.StatusOK, map[string]any{"id": "usr_1", "email": "dev@example.com"})
		case "/api/orgs":
			bearerSeen = append(bearerSeen, r.Header.Get("Authorization"))
			writeJSON(t, w, http.StatusOK, []map[string]any{
				{"id": "org_z", "name": "Zeta"},
				{"id": "org_a", "name": "Alpha"},
			})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	stubConsole(t, server.URL)

	path := filepath.Join(t.TempDir(), "opencode.json")
	var started []LoginInfo
	var opened []string
	var progress strings.Builder
	creds, err := LoginDeviceFlow(context.Background(), path, &progress,
		func(url string) error {
			opened = append(opened, url)
			return nil
		},
		func(info LoginInfo) { started = append(started, info) },
		nil)
	if err != nil {
		t.Fatalf("LoginDeviceFlow: %v", err)
	}

	wantURL := server.URL + "/console/device?user_code=ABCD-EFGH&client_id=opencode-cli"
	if len(started) != 1 {
		t.Fatalf("onStart fired %d times, want exactly once", len(started))
	}
	if started[0].VerificationURL != wantURL || started[0].UserCode != "ABCD-EFGH" {
		t.Fatalf("onStart info = %+v, want %s / ABCD-EFGH", started[0], wantURL)
	}
	if len(opened) != 1 || opened[0] != wantURL {
		t.Fatalf("openBrowser calls = %v, want the verification URL", opened)
	}
	// The URL is printed as well: on a machine with no browser the reader still
	// has to be able to finish the flow by hand.
	if !strings.Contains(progress.String(), wantURL) || !strings.Contains(progress.String(), "ABCD-EFGH") {
		t.Fatalf("progress text = %q, want it to carry the URL and the code", progress.String())
	}
	if codeRequest["client_id"] != ClientID {
		t.Fatalf("device code client_id = %v, want %q", codeRequest["client_id"], ClientID)
	}
	if codeRequest["supports_org_scope"] != true {
		t.Fatalf("device code supports_org_scope = %v, want true", codeRequest["supports_org_scope"])
	}

	if creds.AccessToken != "access-1" || creds.RefreshToken != "refresh-1" {
		t.Fatalf("credentials = %+v", creds)
	}
	if creds.Email != "dev@example.com" || creds.AccountID != "usr_1" {
		t.Fatalf("profile = %+v, want the /api/user fields", creds)
	}
	// The grant carried no org, so the first org by name wins.
	if creds.OrgID != "org_a" || creds.OrgName != "Alpha" {
		t.Fatalf("org = %q/%q, want org_a/Alpha", creds.OrgID, creds.OrgName)
	}
	if len(bearerSeen) != 2 {
		t.Fatalf("profile calls = %d, want 2", len(bearerSeen))
	}
	for _, header := range bearerSeen {
		if header != "Bearer access-1" {
			t.Fatalf("profile Authorization = %q, want the new access token", header)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("credentials were not written: %v", err)
	}
	saved, err := Load(path)
	if err != nil {
		t.Fatalf("Load after login: %v", err)
	}
	if saved.AccessToken != "access-1" {
		t.Fatalf("saved access token = %q", saved.AccessToken)
	}
}

func TestLoginDeviceFlowReportsDenial(t *testing.T) {
	stubPollFloor(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/device/code":
			writeJSON(t, w, http.StatusOK, map[string]any{
				"device_code":               "device-1",
				"user_code":                 "ABCD-EFGH",
				"verification_uri_complete": "/console/device?user_code=ABCD-EFGH",
				"expires_in":                600,
			})
		case "/auth/device/token":
			writeJSON(t, w, http.StatusBadRequest, map[string]any{"error": "access_denied"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	stubConsole(t, server.URL)

	_, err := LoginDeviceFlow(context.Background(), filepath.Join(t.TempDir(), "opencode.json"), nil,
		func(string) error { return nil }, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("error = %v, want a denial", err)
	}
}

// writeRecorder notes whether the flow has written anything yet, so a test can
// pin the first stage report to before the first output.
type writeRecorder struct {
	buf   bytes.Buffer
	wrote bool
}

func (w *writeRecorder) Write(p []byte) (int, error) {
	w.wrote = true
	return w.buf.Write(p)
}

// The stages name what the flow is doing as it does it, and the first one —
// the code request that can block for seconds — must fire before the flow has
// printed anything: that silent gap is the whole reason the callback exists.
func TestLoginDeviceFlowReportsEachStage(t *testing.T) {
	stubPollFloor(t)
	var polls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/device/code":
			writeJSON(t, w, http.StatusOK, map[string]any{
				"device_code":               "device-1",
				"user_code":                 "ABCD-EFGH",
				"verification_uri_complete": "/console/device?user_code=ABCD-EFGH",
				"expires_in":                600,
			})
		case "/auth/device/token":
			polls++
			if polls == 1 {
				writeJSON(t, w, http.StatusBadRequest, map[string]any{"error": "authorization_pending"})
				return
			}
			writeJSON(t, w, http.StatusOK, map[string]any{
				"access_token":  "access-1",
				"refresh_token": "refresh-1",
				"expires_in":    3600,
			})
		case "/api/user":
			writeJSON(t, w, http.StatusOK, map[string]any{"id": "usr_1", "email": "dev@example.com"})
		case "/api/orgs":
			writeJSON(t, w, http.StatusOK, []map[string]any{{"id": "org_a", "name": "Alpha"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	stubConsole(t, server.URL)

	var stages []LoginStage
	var output writeRecorder
	_, err := LoginDeviceFlow(context.Background(), filepath.Join(t.TempDir(), "opencode.json"), &output,
		func(string) error { return nil }, nil,
		func(stage LoginStage) {
			// The first report is the guarantee a surface relies on: the
			// code request runs before the flow says anything, so a
			// progress line is the only thing that can be on screen then.
			if len(stages) == 0 && output.wrote {
				t.Error("the first stage fired after output was written")
			}
			stages = append(stages, stage)
		})
	if err != nil {
		t.Fatalf("LoginDeviceFlow: %v", err)
	}
	want := []LoginStage{LoginStageRequestingCode, LoginStageWaitingAuthorization, LoginStageFinishing}
	if len(stages) != len(want) {
		t.Fatalf("stages = %v, want %v", stages, want)
	}
	for i := range want {
		if stages[i] != want[i] {
			t.Fatalf("stages = %v, want %v in order", stages, want)
		}
	}
	if !strings.Contains(output.buf.String(), "To sign in to OpenCode") {
		t.Fatalf("output = %q, want the sign-in instructions", output.buf.String())
	}
}

// A caller that wants no stage reports passes nil, and the flow must work as
// before: the callback is an optional observer, never a participant.
func TestLoginDeviceFlowWithoutStageCallbackStillWorks(t *testing.T) {
	stubPollFloor(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/device/code":
			writeJSON(t, w, http.StatusOK, map[string]any{
				"device_code":               "device-1",
				"user_code":                 "ABCD-EFGH",
				"verification_uri_complete": "/console/device?user_code=ABCD-EFGH",
				"expires_in":                600,
			})
		case "/auth/device/token":
			writeJSON(t, w, http.StatusOK, map[string]any{
				"access_token":  "access-1",
				"refresh_token": "refresh-1",
				"expires_in":    3600,
			})
		case "/api/user":
			writeJSON(t, w, http.StatusOK, map[string]any{"id": "usr_1", "email": "dev@example.com"})
		case "/api/orgs":
			writeJSON(t, w, http.StatusOK, []map[string]any{{"id": "org_a", "name": "Alpha"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	stubConsole(t, server.URL)

	path := filepath.Join(t.TempDir(), "opencode.json")
	creds, err := LoginDeviceFlow(context.Background(), path, nil,
		func(string) error { return nil }, nil, nil)
	if err != nil {
		t.Fatalf("LoginDeviceFlow: %v", err)
	}
	if creds.AccessToken != "access-1" || creds.RefreshToken != "refresh-1" {
		t.Fatalf("credentials = %+v", creds)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("credentials were not written: %v", err)
	}
}

// A verification URL that is not http(s) must never reach a browser, whichever
// surface asked for the login.
func TestAbsoluteVerificationURL(t *testing.T) {
	oldWeb := consoleWebBase
	consoleWebBase = "https://opencode.ai"
	t.Cleanup(func() { consoleWebBase = oldWeb })

	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{
			name: "relative path resolves against the console host",
			raw:  "/console/device?user_code=ABCD-EFGH",
			want: "https://opencode.ai/console/device?user_code=ABCD-EFGH",
		},
		{name: "absolute https kept", raw: "https://opencode.ai/console/device", want: "https://opencode.ai/console/device"},
		{name: "absolute http kept", raw: "http://localhost:8080/device", want: "http://localhost:8080/device"},
		{name: "javascript rejected", raw: "javascript:alert(1)", wantErr: true},
		{name: "empty rejected", raw: "   ", wantErr: true},
	}
	for _, tc := range cases {
		got, err := absoluteVerificationURL(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: got %q, want an error", tc.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSelectOrg(t *testing.T) {
	orgs := []orgInfo{{ID: "org_z", Name: "Zeta"}, {ID: "org_a", Name: "Alpha"}}
	if id, name := selectOrg("org_z", orgs); id != "org_z" || name != "Zeta" {
		t.Errorf("granted org = %q/%q, want the granted id and its name", id, name)
	}
	if id, name := selectOrg("", orgs); id != "org_a" || name != "Alpha" {
		t.Errorf("fallback org = %q/%q, want the first by name", id, name)
	}
	if id, name := selectOrg("", nil); id != "" || name != "" {
		t.Errorf("no orgs = %q/%q, want empty", id, name)
	}
	// The caller's slice must not be reordered by the lookup.
	if orgs[0].ID != "org_z" {
		t.Error("selectOrg reordered the caller's org slice")
	}
}

// The page reports "canceled" the moment the user asks, so the file must not
// appear afterwards: the profile calls that follow the token grant swallow
// every error, a cancel included, and the flow would otherwise save a login
// nobody is waiting for any more.
func TestLoginDeviceFlowDoesNotSaveAfterTheLoginIsCanceled(t *testing.T) {
	stubPollFloor(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/device/code":
			writeJSON(t, w, http.StatusOK, map[string]any{
				"device_code":               "device-1",
				"user_code":                 "ABCD-EFGH",
				"verification_uri_complete": "/console/device?user_code=ABCD-EFGH",
				"expires_in":                600,
				"interval":                  0,
			})
		case "/auth/device/token":
			writeJSON(t, w, http.StatusOK, map[string]any{
				"access_token":  "access-1",
				"refresh_token": "refresh-1",
				"expires_in":    3600,
			})
		case "/api/user":
			// The user gave up while this request was in flight; the profile
			// calls answer normally anyway, exactly as the real console would.
			cancel()
			writeJSON(t, w, http.StatusOK, map[string]any{"id": "usr_1", "email": "dev@example.com"})
		case "/api/orgs":
			writeJSON(t, w, http.StatusOK, []map[string]any{{"id": "org_a", "name": "Alpha"}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	stubConsole(t, server.URL)

	path := filepath.Join(t.TempDir(), "opencode.json")
	_, err := LoginDeviceFlow(ctx, path, io.Discard, func(string) error { return nil }, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the canceled login", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("a canceled login left a credentials file behind (stat err = %v)", statErr)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Errorf("write response: %v", err)
	}
}
