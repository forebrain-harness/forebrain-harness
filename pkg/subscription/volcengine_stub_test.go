package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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
