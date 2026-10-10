package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	llmopenai "github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	state "github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/stretchr/testify/require"
)

// The endpoint double must keep satisfying the control-plane port.
var _ tool.CodeIntelControl = (*lspControlDouble)(nil)

func TestHandleSlashCommands(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/slash/commands?surface=webchat&q=pl", nil)
	rr := httptest.NewRecorder()

	s.handleSlashCommands(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var out struct {
		Surface string `json:"surface"`
		Query   string `json:"query"`
		Records []struct {
			Name         string   `json:"name"`
			Category     string   `json:"category"`
			ActionKind   string   `json:"action_kind"`
			ArgumentHint string   `json:"argument_hint"`
			AllowedModes []string `json:"allowed_modes"`
		} `json:"records"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Equal(t, "webchat", out.Surface)
	require.Equal(t, "pl", out.Query)
	require.Len(t, out.Records, 1)
	require.Equal(t, "plan", out.Records[0].Name)
	require.Equal(t, "agent", out.Records[0].Category)
	require.Equal(t, "inject-prompt", out.Records[0].ActionKind)
	require.NotEmpty(t, out.Records[0].ArgumentHint)
	require.NotEmpty(t, out.Records[0].AllowedModes)
}

func TestHandleSlashCommandsSideConversationFilter(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/slash/commands?surface=webchat&side=1", nil)
	rr := httptest.NewRecorder()

	s.handleSlashCommands(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var out struct {
		Records []struct {
			Name string `json:"name"`
		} `json:"records"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.NotEmpty(t, out.Records)
	names := make(map[string]bool, len(out.Records))
	for _, rec := range out.Records {
		names[rec.Name] = true
	}
	require.True(t, names["status"])
	require.False(t, names["plan"])
}

func TestHandleSlashCommandsDuringRunFilter(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/slash/commands?surface=webchat&during_run=1", nil)
	rr := httptest.NewRecorder()

	s.handleSlashCommands(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var out struct {
		Records []struct {
			Name string `json:"name"`
		} `json:"records"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.NotEmpty(t, out.Records)
	names := make(map[string]bool, len(out.Records))
	for _, rec := range out.Records {
		names[rec.Name] = true
	}
	require.False(t, names["new"])
	require.False(t, names["resume"])
	require.False(t, names["fork"])
	require.True(t, names["status"])
}

func TestHandleSlashCommandsReturnsCanonicalAndOptionFlags(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/slash/commands?surface=webchat&q=pl&during_run=1&side=1", nil)
	rr := httptest.NewRecorder()

	s.handleSlashCommands(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var out struct {
		Options struct {
			DuringRun        bool `json:"during_run"`
			SideConversation bool `json:"side_conversation"`
		} `json:"options"`
		Records []struct {
			Name                    string `json:"name"`
			CanonicalName           string `json:"canonical_name"`
			AvailableDuringRun      bool   `json:"available_during_run"`
			AvailableInConversation bool   `json:"available_in_side_conversation"`
		} `json:"records"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.True(t, out.Options.DuringRun)
	require.True(t, out.Options.SideConversation)
	require.Empty(t, out.Records)
}

func TestHandleSlashCommandsSlashDiscoverySurfaceRejectsUnsupportedChannel(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/slash/commands?surface=cli", nil)
	rr := httptest.NewRecorder()

	s.handleSlashCommands(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), `slash discovery is not available for channel "cli"`)
}

func TestHandleSlashCommandsSlashDiscoverySurfaceRejectsUnsupportedChannelTrimmed(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/slash/commands?surface=+cli+", nil)
	rr := httptest.NewRecorder()

	s.handleSlashCommands(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), `slash discovery is not available for channel "cli"`)
}

func TestParseSlashCommandWithOptionsSlashExecutionSurfaceRejectsUnsupportedChannel(t *testing.T) {
	res := parseSlashCommandWithOptions(&Server{}, "s1", "cli", "/plan", turn.Context{}, nil)
	require.True(t, res.Handled)
	require.False(t, res.ShouldContinueRun)
	require.Equal(t, `unsupported slash channel "cli"`, res.Reply)
}

func TestParseSlashCommandWithOptionsSlashExecutionSurfaceRejectsUnsupportedChannelTrimmed(t *testing.T) {
	res := parseSlashCommandWithOptions(&Server{}, "s1", " cli ", "/plan", turn.Context{}, nil)
	require.True(t, res.Handled)
	require.False(t, res.ShouldContinueRun)
	require.Equal(t, `unsupported slash channel "cli"`, res.Reply)
}

// The webchat surface must carry the /goal objective through the gateway's
// slashCommandResult so the ws handler can enable goal-driven continuation.
func TestParseSlashCommandWebchatGoalCarriesObjective(t *testing.T) {
	res := parseSlashCommand(&Server{}, "s1", "webchat", "/goal ship the parser")
	require.True(t, res.ShouldContinueRun)
	require.Equal(t, "ship the parser", res.GoalObjective)
	require.Contains(t, res.ContinueInput, "ship the parser")
}

func TestParseSlashCommandWebchatGoalWithoutObjectiveIsUsageHint(t *testing.T) {
	res := parseSlashCommand(&Server{}, "s1", "webchat", "/goal")
	require.False(t, res.ShouldContinueRun)
	require.Empty(t, res.GoalObjective)
	require.True(t, res.Handled)
}

// cronTestServer builds the least server the standing-work endpoints need: a
// database for the jobs, and an active primary agent to own them.
func cronTestServer(t *testing.T) *Server {
	t.Helper()
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := &appcfg.Root{}
	return &Server{
		Home:  home,
		RunRT: &state.RunStore{DB: db},
		Env:   &process.Environment{Root: home, SQL: db, Deps: run.Deps{AppCfg: cfg}},
	}
}

// seedRun creates the conversation and run rows an event's foreign keys
// require, under the exact ids a fixture names.
func seedRun(t *testing.T, runs *state.RunStore, sessionID, runID string) {
	t.Helper()
	if err := state.NewSessionStore(runs.DB, "main").Ensure(context.Background(), sessionID, sessionID); err != nil {
		t.Fatalf("ensure session %s: %v", sessionID, err)
	}
	if _, err := runs.DB.ExecContext(context.Background(),
		`INSERT INTO fb_runs(id, session_id, input_text, status, created_at, updated_at) VALUES(?,?,'',?,0,0)`,
		runID, sessionID, string(state.RunStatusRunning)); err != nil {
		t.Fatalf("create run %s: %v", runID, err)
	}
}

func cronRequest(t *testing.T, s *Server, method, target string, body any, handler http.HandlerFunc, params ...string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, reader)
	if len(params) == 2 {
		req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: params[0], Value: params[1]}}))
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

// A job is created, listed, edited, fired-history-checked and removed through
// the API a surface actually calls.
func TestCronJobsRoundTripThroughTheAPI(t *testing.T) {
	s := cronTestServer(t)

	created := cronRequest(t, s, http.MethodPost, "/api/cron", map[string]any{
		"name": "morning brief", "schedule": "daily at 7am", "prompt": "summarise the inbox", "deliver": "telegram",
	}, s.handleCronJobs)
	if created.Code != http.StatusOK {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	var job state.CronJob
	if err := json.Unmarshal(created.Body.Bytes(), &job); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	if job.ID == "" || job.NextRunAt == nil {
		t.Fatalf("a created job must be scheduled: %#v", job)
	}

	listed := cronRequest(t, s, http.MethodGet, "/api/cron", nil, s.handleCronJobs)
	var list struct {
		AgentID string          `json:"agent_id"`
		Records []state.CronJob `json:"records"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Records) != 1 || list.Records[0].Name != "morning brief" {
		t.Fatalf("list = %#v", list)
	}

	paused := cronRequest(t, s, http.MethodPut, "/api/cron/"+job.ID, map[string]any{
		"schedule": "daily at 7am", "enabled": false,
	}, s.handleCronJob, "id", job.ID)
	if paused.Code != http.StatusOK {
		t.Fatalf("pause = %d %s", paused.Code, paused.Body.String())
	}
	var pausedJob state.CronJob
	_ = json.Unmarshal(paused.Body.Bytes(), &pausedJob)
	if pausedJob.NextRunAt != nil {
		t.Fatal("a paused job must have no next fire, or the scheduler would still pick it up")
	}

	resumed := cronRequest(t, s, http.MethodPut, "/api/cron/"+job.ID, map[string]any{"enabled": true}, s.handleCronJob, "id", job.ID)
	var resumedJob state.CronJob
	_ = json.Unmarshal(resumed.Body.Bytes(), &resumedJob)
	if resumedJob.NextRunAt == nil {
		t.Fatal("resuming a job must give it a next fire again")
	}

	removed := cronRequest(t, s, http.MethodDelete, "/api/cron/"+job.ID, nil, s.handleCronJob, "id", job.ID)
	if removed.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", removed.Code, removed.Body.String())
	}
	after := cronRequest(t, s, http.MethodGet, "/api/cron", nil, s.handleCronJobs)
	_ = json.Unmarshal(after.Body.Bytes(), &list)
	if len(list.Records) != 0 {
		t.Fatalf("job survived deletion: %#v", list.Records)
	}
}

// A schedule nobody can read is refused when the job is created, not silently
// stored as a job that never fires.
func TestCronJobCreationRefusesAnUnreadableSchedule(t *testing.T) {
	s := cronTestServer(t)
	rec := cronRequest(t, s, http.MethodPost, "/api/cron", map[string]any{
		"schedule": "whenever", "prompt": "do the thing",
	}, s.handleCronJobs)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create with a bad schedule = %d %s", rec.Code, rec.Body.String())
	}

	noPrompt := cronRequest(t, s, http.MethodPost, "/api/cron", map[string]any{"schedule": "every 1h"}, s.handleCronJobs)
	if noPrompt.Code != http.StatusBadRequest {
		t.Fatalf("create with no prompt = %d", noPrompt.Code)
	}
}

// A job belonging to another primary agent is not this tenant's to read.
func TestCronJobFromAnotherTenantIsNotFound(t *testing.T) {
	s := cronTestServer(t)
	store := &state.CronStore{DB: s.RunRT.DB}
	foreign := state.CronJob{ID: "job-foreign", Ag: "research", Sched: "every 1h", Prompt: "x", Enabled: true}
	if err := store.InsertJob(context.Background(), foreign); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	rec := cronRequest(t, s, http.MethodGet, "/api/cron/job-foreign", nil, s.handleCronJob, "id", "job-foreign")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("another tenant's job = %d %s", rec.Code, rec.Body.String())
	}
}

// A heartbeat is set on a session, read back, and cleared.
func TestHeartbeatRoundTripsThroughTheAPI(t *testing.T) {
	s := cronTestServer(t)
	mustGatewaySession(t, s.RunRT.DB, "s-1")

	type heartbeatView struct {
		SessionID   string `json:"session_id"`
		IntervalSec int    `json:"interval_seconds"`
		Prompt      string `json:"prompt"`
		Paused      bool   `json:"paused"`
		NextRunAt   *int64 `json:"next_run_at"`
		LastFiredAt *int64 `json:"last_fired_at"`
	}
	decode := func(rec *httptest.ResponseRecorder) *heartbeatView {
		t.Helper()
		var out heartbeatView
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode heartbeat: %v", err)
		}
		return &out
	}

	set := cronRequest(t, s, http.MethodPut, "/api/heartbeat", map[string]any{
		"session_id": "s-1", "interval_seconds": 600, "prompt": "anything new?",
	}, s.handleHeartbeat)
	if set.Code != http.StatusOK {
		t.Fatalf("set = %d %s", set.Code, set.Body.String())
	}
	if got := decode(set); got.Paused || got.NextRunAt == nil {
		t.Fatalf("a set heartbeat must be scheduled: %#v", got)
	}

	got := cronRequest(t, s, http.MethodGet, "/api/heartbeat?session_id=s-1", nil, s.handleHeartbeat)
	var read struct {
		Heartbeat *heartbeatView `json:"heartbeat"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &read); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if read.Heartbeat == nil || read.Heartbeat.Prompt != "anything new?" || read.Heartbeat.NextRunAt == nil || read.Heartbeat.Paused {
		t.Fatalf("heartbeat = %#v", read.Heartbeat)
	}

	paused := cronRequest(t, s, http.MethodPut, "/api/heartbeat", map[string]any{
		"session_id": "s-1", "interval_seconds": 600, "prompt": "anything new?", "paused": true,
	}, s.handleHeartbeat)
	if paused.Code != http.StatusOK {
		t.Fatalf("pause = %d %s", paused.Code, paused.Body.String())
	}
	if got := decode(paused); !got.Paused || got.NextRunAt != nil {
		t.Fatalf("a paused heartbeat must have no next fire: %#v", got)
	}

	tooFast := cronRequest(t, s, http.MethodPut, "/api/heartbeat", map[string]any{
		"session_id": "s-2", "interval_seconds": 5, "prompt": "spin",
	}, s.handleHeartbeat)
	if tooFast.Code != http.StatusBadRequest {
		t.Fatalf("a sub-minute heartbeat must be refused, got %d", tooFast.Code)
	}

	cleared := cronRequest(t, s, http.MethodDelete, "/api/heartbeat?session_id=s-1", nil, s.handleHeartbeat)
	if cleared.Code != http.StatusOK {
		t.Fatalf("clear = %d", cleared.Code)
	}
	after := cronRequest(t, s, http.MethodGet, "/api/heartbeat?session_id=s-1", nil, s.handleHeartbeat)
	_ = json.Unmarshal(after.Body.Bytes(), &read)
	if read.Heartbeat != nil {
		t.Fatalf("heartbeat survived clearing: %#v", read.Heartbeat)
	}
}

// The configuration editor must never hand a secret back to the browser, and
// must refuse text that would not load as a appcfg.
func TestConfigEndpointRedactsSecretsAndRefusesInvalidText(t *testing.T) {
	s := cronTestServer(t)
	path := filepath.Join(s.Home, "forebrain.yaml")
	if err := os.WriteFile(path, []byte("agents:\n  definitions:\n    main:\n      primary: true\n      channels:\n        telegram:\n          enabled: true\n          bot_token: super-secret\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	s.Env.ConfigPath = path

	rec := cronRequest(t, s, http.MethodGet, "/api/config", nil, s.handleConfigFile)
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Path string `json:"path"`
		YAML string `json:"yaml"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strings.Contains(got.YAML, "super-secret") {
		t.Fatal("the editor must not be handed the secret it is not allowed to read")
	}
	if !strings.Contains(got.YAML, appcfg.RedactedSecretPlaceholder) {
		t.Fatalf("a set secret must still be visible as set:\n%s", got.YAML)
	}
	if got.Path != path {
		t.Fatalf("path = %q", got.Path)
	}

	bad := cronRequest(t, s, http.MethodPut, "/api/config", map[string]any{"yaml": "agents: [unclosed"}, s.handleConfigFile)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unparsable config = %d %s", bad.Code, bad.Body.String())
	}
}

// Hooks run commands, so a hook table that would not load is refused at the
// moment it is submitted rather than at the moment a hook was due to fire.
func TestHooksEndpointRefusesAnUnknownEvent(t *testing.T) {
	s := cronTestServer(t)
	s.Env.ConfigPath = filepath.Join(s.Home, "forebrain.yaml")
	rec := cronRequest(t, s, http.MethodPut, "/api/hooks", map[string]any{
		"hooks": map[string]any{
			"WheneverIFeelLikeIt": []map[string]any{{"hooks": []map[string]any{{"type": "command", "command": "echo hi"}}}},
		},
	}, s.handleHooks)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown hook event = %d %s", rec.Code, rec.Body.String())
	}
}

// The hooks endpoint tells a surface what it may offer, from the same list the
// validator enforces.
func TestHooksEndpointPublishesTheKnownEventsAndTypes(t *testing.T) {
	s := cronTestServer(t)
	s.Env.ConfigPath = filepath.Join(s.Home, "forebrain.yaml")
	rec := cronRequest(t, s, http.MethodGet, "/api/hooks", nil, s.handleHooks)
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Events     []string `json:"events"`
		KnownTypes []string `json:"known_types"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Events) == 0 || len(got.KnownTypes) == 0 {
		t.Fatalf("hooks metadata = %#v", got)
	}
	for _, name := range got.Events {
		if !appcfg.ValidHookEventName(name) {
			t.Fatalf("offered event %q is not accepted by the validator", name)
		}
	}
}

func newProjectsTestServer(t *testing.T) (*Server, *state.ProjectStore, string) {
	t.Helper()
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", WorkspaceRoot: filepath.Join(home, "workspace")}}
	s := &Server{
		Home:     home,
		Runner:   runner,
		Projects: state.NewProjectStore(db, "main"),
		Sessions: state.NewSessionStore(db, "main"),
	}
	return s, s.Projects, home
}

func doJSON(t *testing.T, s *Server, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(b))
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTPForTest(rec, req)
	return rec
}

func TestProjectsCreateValidatesRoot(t *testing.T) {
	s, _, home := newProjectsTestServer(t)

	// Missing root.
	rec := doJSON(t, s, http.MethodPost, "/api/v1/projects", map[string]any{"name": "x"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing root: %d %s", rec.Code, rec.Body.String())
	}
	// Nonexistent directory.
	rec = doJSON(t, s, http.MethodPost, "/api/v1/projects", map[string]any{"name": "x", "root": "/nonexistent-project-dir"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("nonexistent root: %d", rec.Code)
	}
	// Filesystem root.
	rec = doJSON(t, s, http.MethodPost, "/api/v1/projects", map[string]any{"name": "x", "root": "/"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("fs root: %d", rec.Code)
	}
	// Inside the forebrain home.
	rec = doJSON(t, s, http.MethodPost, "/api/v1/projects", map[string]any{"name": "x", "root": filepath.Join(home, "nested")})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("inside home: %d", rec.Code)
	}
	// Not a directory.
	filePath := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s, http.MethodPost, "/api/v1/projects", map[string]any{"name": "x", "root": filePath})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("file root: %d", rec.Code)
	}
}

// A project is bound to one directory, under the one name forebrain gives it.
//
// The trust store, the launch context every project route resolves, and the
// project key written into the row all spell a directory with its symlinks
// resolved. A root kept as the caller typed it would leave the record holding
// two names for one place: a client could not match the project against the
// paths every other route reports, and the same checkout could be bound twice.
func TestProjectsCreateStoresTheCanonicalRoot(t *testing.T) {
	s, _, _ := newProjectsTestServer(t)
	target := projectTempRoot(t)
	link := filepath.Join(t.TempDir(), "by-another-name")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s, http.MethodPost, "/api/v1/projects", map[string]any{"name": "linked", "root": link})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var row map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if got, _ := row["root"].(string); got != target {
		t.Fatalf("root = %q, want the canonical %q", got, target)
	}
	if got, _ := row["project_key"].(string); got != memory.ProjectKey(target) {
		t.Fatalf("project_key = %q, want the key of the canonical root", got)
	}
}

func TestProjectsCreateTrustIsExplicitOperatorChoice(t *testing.T) {
	s, _, home := newProjectsTestServer(t)
	root := t.TempDir()

	// Without trust: project exists, no trust decision recorded.
	rec := doJSON(t, s, http.MethodPost, "/api/v1/projects", map[string]any{"name": "untrusted", "root": root})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var row map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if row["trust_recorded"] != false {
		t.Fatalf("trust must not be recorded by default: %v", row["trust_recorded"])
	}
	trusted, err := safety.IsTrusted(home, safety.Project{Root: root})
	if err != nil || trusted {
		t.Fatalf("no trust decision should exist yet: %v %v", trusted, err)
	}

	// With trust: the decision lands in the existing workspace trust store.
	rec = doJSON(t, s, http.MethodPost, "/api/v1/projects", map[string]any{"name": "trusted", "root": root, "trust": true})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create trusted: %d %s", rec.Code, rec.Body.String())
	}
	row = map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if row["trust_recorded"] != true {
		t.Fatalf("trust should be recorded when explicitly chosen: %v", row["trust_recorded"])
	}
	trusted, err = safety.IsTrusted(home, safety.Project{Root: root})
	if err != nil || !trusted {
		t.Fatalf("trust decision missing: %v %v", trusted, err)
	}
}

func TestProjectsCRUDPinArchiveDelete(t *testing.T) {
	s, store, _ := newProjectsTestServer(t)
	root := t.TempDir()

	rec := doJSON(t, s, http.MethodPost, "/api/v1/projects", map[string]any{"name": "orig", "root": root, "instructions": "one", "memory_scope": "project_only"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("no id: %v", created)
	}
	if created["memory_scope"] != "project_only" {
		t.Fatalf("scope: %v", created["memory_scope"])
	}

	// List shows it.
	rec = doJSON(t, s, http.MethodGet, "/api/v1/projects", nil)
	var list struct {
		Projects []map[string]any `json:"projects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Projects) != 1 || list.Projects[0]["id"] != id {
		t.Fatalf("list: %+v", list)
	}

	// Patch edits editable fields only.
	rec = doJSON(t, s, http.MethodPatch, "/api/v1/projects/"+id, map[string]any{"name": "renamed", "instructions": "two", "memory_scope": "shared"})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	p, err := store.Get(context.Background(), id)
	if err != nil || p.Name != "renamed" || p.Instructions != "two" || p.MemoryScope != "shared" {
		t.Fatalf("patch not applied: %+v %v", p, err)
	}

	// Pin then archive then list filtering.
	rec = doJSON(t, s, http.MethodPost, "/api/v1/projects/"+id+"/pin", map[string]any{"pinned": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("pin: %d", rec.Code)
	}
	rec = doJSON(t, s, http.MethodPost, "/api/v1/projects/"+id+"/archive", map[string]any{"archived": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("archive: %d", rec.Code)
	}
	rec = doJSON(t, s, http.MethodGet, "/api/v1/projects", nil)
	list = struct {
		Projects []map[string]any `json:"projects"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Projects) != 0 {
		t.Fatalf("archived project must be hidden: %+v", list)
	}
	rec = doJSON(t, s, http.MethodGet, "/api/v1/projects?archived=true", nil)
	list = struct {
		Projects []map[string]any `json:"projects"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Projects) != 1 {
		t.Fatalf("archived project must be listed with filter: %+v", list)
	}

	// Delete removes it.
	rec = doJSON(t, s, http.MethodDelete, "/api/v1/projects/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	if _, err := store.Get(context.Background(), id); err == nil {
		t.Fatalf("deleted project still present")
	}
}

func TestProjectSessionCreateBindsProjectAndCwd(t *testing.T) {
	s, store, _ := newProjectsTestServer(t)
	root := t.TempDir()
	rec := doJSON(t, s, http.MethodPost, "/api/v1/projects", map[string]any{"name": "withsessions", "root": root})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := created["id"].(string)

	rec = doJSON(t, s, http.MethodPost, "/api/v1/projects/"+id+"/sessions", map[string]any{"title": "in project"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("session create: %d %s", rec.Code, rec.Body.String())
	}
	var session struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session.ID == "" {
		t.Fatalf("no session id")
	}
	project, ok, err := store.ProjectForSession(context.Background(), session.ID)
	if err != nil || !ok || project.ID != id {
		t.Fatalf("session not bound: %v %v %+v", err, ok, project)
	}
	// The session list for the project contains it.
	rec = doJSON(t, s, http.MethodGet, "/api/v1/projects/"+id+"/sessions", nil)
	var list struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0]["id"] != session.ID {
		t.Fatalf("project sessions: %+v", list)
	}
	// ...and the agent's own conversation list does not: a project's
	// sessions live in its project space only.
	rec = httptest.NewRecorder()
	s.handleChatSessions(rec, httptest.NewRequest(http.MethodGet, "/api/chat/sessions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("chat sessions: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), session.ID) {
		t.Fatalf("project session %s leaked into the conversation list: %s", session.ID, rec.Body.String())
	}
	// The chat page still names the opened session — its title and its
	// project — by reading the session itself rather than by looking it up
	// in the drawer's list, which never holds a project's sessions.
	rec = httptest.NewRecorder()
	s.handleChatSession(rec, withID(httptest.NewRequest(http.MethodGet, "/api/chat/sessions/"+session.ID, nil), session.ID))
	if rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"title":"in project"`) ||
		!strings.Contains(rec.Body.String(), `"name":"withsessions"`) ||
		!strings.Contains(rec.Body.String(), `"id":"`+id+`"`) {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}
}

// TestChatSessionsAreFilteredByPurpose pins that the drawer and the workshop
// each get their own list from the query itself: the source parameter picks
// the purpose, anything else is refused, and no amount of scheduled-task
// fires can crowd the agent's own conversations out of the drawer.
func TestChatSessionsAreFilteredByPurpose(t *testing.T) {
	s, _, _ := newProjectsTestServer(t)
	ctx := context.Background()
	ensure := func(id, title string) {
		t.Helper()
		if err := s.Sessions.Ensure(ctx, id, title); err != nil {
			t.Fatal(err)
		}
	}
	ensure("talk", "a conversation")
	ensure("task", "a workshop task")
	if err := s.Sessions.SetSessionSource(ctx, "task", state.SessionSourceWorkshop); err != nil {
		t.Fatal(err)
	}
	// 250 scheduled-task fires, all newer than the conversation.
	for i := 0; i < 250; i++ {
		id := fmt.Sprintf("fire-%03d", i)
		ensure(id, id)
		if err := s.Sessions.SetSessionSource(ctx, id, state.SessionSourceCron); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Sessions.DB().Exec("UPDATE fb_sessions SET updated_at = ? WHERE id = ?", int64(10_000+i), id); err != nil {
			t.Fatal(err)
		}
	}

	list := func(query string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		s.handleChatSessions(rec, httptest.NewRequest(http.MethodGet, "/api/chat/sessions"+query, nil))
		return rec
	}
	rec := list("")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"talk"`) || strings.Contains(rec.Body.String(), `"id":"task"`) {
		t.Fatalf("default list: %d %s", rec.Code, rec.Body.String())
	}
	rec = list("?source=workshop")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"task"`) || strings.Contains(rec.Body.String(), `"id":"talk"`) {
		t.Fatalf("workshop list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := list("?source=cron"); rec.Code != http.StatusBadRequest {
		t.Fatalf("source=cron: %d %s, want 400", rec.Code, rec.Body.String())
	}
}

// TestChatSessionNamesOneConversation pins the page's own lookup: an opened
// session's title and project come from the session itself — an unnamed one
// answers "" so the client draws its placeholder — and another tenant's
// session is not ours to name.
func TestChatSessionNamesOneConversation(t *testing.T) {
	s, store, _ := newProjectsTestServer(t)
	ctx := context.Background()

	// A named conversation outside any project, and an unnamed one — stored
	// titled with its own id, which reads as none.
	if err := s.Sessions.Ensure(ctx, "plain", "Just talking"); err != nil {
		t.Fatal(err)
	}
	if err := s.Sessions.Ensure(ctx, "fresh", "fresh"); err != nil {
		t.Fatal(err)
	}
	// One bound to a project.
	p, err := store.Create(ctx, state.CreateProjectInput{Name: "named", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Sessions.Ensure(ctx, "bound", "In the project"); err != nil {
		t.Fatal(err)
	}
	if err := store.BindSession(ctx, "bound", p.ID); err != nil {
		t.Fatal(err)
	}
	// Another tenant's session, in the same database.
	if err := state.NewSessionStore(s.Sessions.DB(), "other").Ensure(ctx, "theirs", "Not ours"); err != nil {
		t.Fatal(err)
	}

	one := func(sid string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		s.handleChatSession(rec, withID(httptest.NewRequest(http.MethodGet, "/api/chat/sessions/"+sid, nil), sid))
		return rec
	}
	rec := one("plain")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"title":"Just talking"`) || !strings.Contains(rec.Body.String(), `"project":null`) {
		t.Fatalf("plain session: %d %s", rec.Code, rec.Body.String())
	}
	if rec := one("fresh"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"title":""`) {
		t.Fatalf("unnamed session: %d %s", rec.Code, rec.Body.String())
	}
	rec = one("bound")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"title":"In the project"`) || !strings.Contains(rec.Body.String(), `"name":"named"`) || !strings.Contains(rec.Body.String(), p.ID) {
		t.Fatalf("project session: %d %s", rec.Code, rec.Body.String())
	}
	if rec := one("theirs"); rec.Code != http.StatusNotFound {
		t.Fatalf("another tenant's session: %d %s, want 404", rec.Code, rec.Body.String())
	}
}

func TestProjectMCPPreviewAndConsentEndpoint(t *testing.T) {
	s, _, home := newProjectsTestServer(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := safety.MarkTrusted(home, safety.Project{Root: root}); err != nil {
		t.Fatal(err)
	}
	srvPath := filepath.Join(root, ".forebrain", "mcp_servers.yaml")
	if err := os.MkdirAll(filepath.Dir(srvPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srvPath, []byte("mcp_servers:\n  - name: webtool\n    transport: stdio\n    command: /bin/true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, http.MethodPost, "/api/v1/projects", map[string]any{"name": "mcpproj", "root": root, "trust": true})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := created["id"].(string)

	// Preview: the entry shows as pending consent, not loaded.
	rec = doJSON(t, s, http.MethodGet, "/api/v1/projects/"+id+"/mcp", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	var preview struct {
		Servers        []map[string]any    `json:"servers"`
		PendingConsent []map[string]string `json:"pending_consent"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Servers) != 0 {
		t.Fatalf("unconfirmed entry must not be in the server list: %+v", preview.Servers)
	}
	if len(preview.PendingConsent) != 1 || preview.PendingConsent[0]["name"] != "webtool" {
		t.Fatalf("pending: %+v", preview.PendingConsent)
	}

	// Confirm through the endpoint.
	rec = doJSON(t, s, http.MethodPost, "/api/v1/projects/"+id+"/mcp/consent", map[string]any{"allow": []string{"webtool"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("consent: %d %s", rec.Code, rec.Body.String())
	}

	// Now the preview loads it with project scope.
	rec = doJSON(t, s, http.MethodGet, "/api/v1/projects/"+id+"/mcp", nil)
	preview = struct {
		Servers        []map[string]any    `json:"servers"`
		PendingConsent []map[string]string `json:"pending_consent"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Servers) != 1 || preview.Servers[0]["scope"] != "project" {
		t.Fatalf("servers after consent: %+v", preview.Servers)
	}
	if len(preview.PendingConsent) != 0 {
		t.Fatalf("nothing should be pending: %+v", preview.PendingConsent)
	}
}

// ServeHTTPForTest dispatches one request through the projects routes without
// standing up the whole REST server.
func (s *Server) ServeHTTPForTest(w http.ResponseWriter, r *http.Request) {
	if s == nil {
		http.Error(w, "nil server", http.StatusInternalServerError)
		return
	}
	rest := NewRestServer("127.0.0.1:0")
	projects := rest.Group("/api/v1/projects")
	projects.Get("/", s.handleProjectsList)
	projects.Post("/", s.handleProjectsCreate)
	projects.Get("/:id", s.handleProjectOne)
	projects.Patch("/:id", s.handleProjectOne)
	projects.Delete("/:id", s.handleProjectDelete)
	projects.Post("/:id/pin", s.handleProjectPin)
	projects.Post("/:id/archive", s.handleProjectArchive)
	projects.Get("/:id/sessions", s.handleProjectSessionsList)
	projects.Post("/:id/sessions", s.handleProjectSessionsCreate)
	projects.Get("/:id/mcp", s.handleProjectMCPPreview)
	projects.Post("/:id/mcp/consent", s.handleProjectMCPConsent)
	projects.Get("/:id/lsp", s.handleProjectLSPPreview)
	projects.Post("/:id/lsp/consent", s.handleProjectLSPConsent)
	// Project-scoped skill lifecycle, mirroring the production routes.
	projects.Get("/:id/skills", s.handleProjectSkillsList)
	projects.Post("/:id/skills", s.handleProjectSkillsCreate)
	projects.Post("/:id/skills/install", s.handleProjectSkillsInstall)
	projects.Get("/:id/skills/:name", s.handleProjectSkillGet)
	projects.Put("/:id/skills/:name", s.handleProjectSkillsUpdate)
	projects.Post("/:id/skills/toggle", s.handleProjectSkillsToggle)
	rest.Handler.ServeHTTP(w, r)
}

// The project-scoped skill lifecycle exists because a gateway serves several
// projects at once. Two projects each create a skill of the same name, and
// each file lands in its own project — neither in the other project, and
// neither beside the gateway process. Before this, both writes went to the
// directory the process was started in, so one project's skill was visible to
// the other and to nobody's project.
// projectTempRoot is a temporary project directory spelled the way forebrain
// spells it. A project root is stored, resolved and reported canonically, and
// on macOS a temp directory is reached through a symlinked /var — a fixture
// that kept the raw path would be comparing two spellings of one directory and
// calling the difference a leak.
func projectTempRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestProjectScopedSkillsWriteIntoTheirOwnProject(t *testing.T) {
	s, _, home := newProjectsTestServer(t)
	rootA := projectTempRoot(t)
	rootB := projectTempRoot(t)
	if err := os.MkdirAll(filepath.Join(rootA, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rootB, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	create := func(name, root string) string {
		t.Helper()
		rec := doJSON(t, s, http.MethodPost, "/api/v1/projects", map[string]any{"name": name, "root": root, "trust": true})
		if rec.Code != http.StatusCreated {
			t.Fatalf("create project %s: %d %s", name, rec.Code, rec.Body.String())
		}
		var created map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		id, _ := created["id"].(string)
		if id == "" {
			t.Fatalf("project %s has no id: %s", name, rec.Body.String())
		}
		return id
	}
	idA := create("alpha", rootA)
	idB := create("beta", rootB)

	content := `---
name: release-flow
description: The steps that cut a release in this repository
---

1. run the checks
`
	for id, name := range map[string]string{idA: "alpha", idB: "beta"} {
		rec := doJSON(t, s, http.MethodPost, "/api/v1/projects/"+id+"/skills", map[string]any{
			"name":    "release-flow",
			"content": content,
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("create skill in %s: %d %s", name, rec.Code, rec.Body.String())
		}
	}

	inA := filepath.Join(rootA, ".forebrain", "skills", "release-flow", "SKILL.md")
	inB := filepath.Join(rootB, ".forebrain", "skills", "release-flow", "SKILL.md")
	for _, path := range []string{inA, inB} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s: %v", path, err)
		}
	}

	// Each project's listing shows its own skill, scoped to that project.
	listFor := func(id string) []map[string]any {
		t.Helper()
		rec := doJSON(t, s, http.MethodGet, "/api/v1/projects/"+id+"/skills", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list skills: %d %s", rec.Code, rec.Body.String())
		}
		var overview struct {
			Installed []map[string]any `json:"installed"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &overview); err != nil {
			t.Fatal(err)
		}
		return overview.Installed
	}
	for id, root := range map[string]string{idA: rootA, idB: rootB} {
		rows := listFor(id)
		found := false
		for _, row := range rows {
			path, _ := row["root_path"].(string)
			if row["name"] != "release-flow" {
				continue
			}
			if filepath.Clean(path) != filepath.Clean(filepath.Join(root, ".forebrain", "skills", "release-flow")) {
				t.Fatalf("skill listed under the wrong project: %s", path)
			}
			if row["origin"] != "project" {
				t.Fatalf("skill origin=%v want project", row["origin"])
			}
			found = true
		}
		if !found {
			t.Fatalf("project %s does not list its own skill: %+v", id, rows)
		}
	}

	// The workspace and the gateway home stay clean: a project skill is the
	// project's, not the agent's and not the process's.
	for _, stray := range []string{
		filepath.Join(home, "workspace", "skills", "release-flow"),
		filepath.Join(home, "skills", "release-flow"),
		filepath.Join(".forebrain", "skills", "release-flow"),
	} {
		if _, err := os.Stat(stray); err == nil {
			t.Fatalf("project skill leaked outside its project: %s", stray)
		}
	}

	// A project route still refuses an unknown project rather than falling
	// back to anything.
	rec := doJSON(t, s, http.MethodGet, "/api/v1/projects/no-such-project/skills", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project: %d %s", rec.Code, rec.Body.String())
	}
}

type stubModelsSource struct {
	models []llmopenai.ModelInfo
	err    error
	calls  int
}

func (s *stubModelsSource) ChatGPTModels(ctx context.Context) ([]llmopenai.ModelInfo, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.models, nil
}

// A chatgpt-scoped listing answers from live account discovery: backend
// priority order, exact slugs, and per-provider status the frontend can show.
func TestHandleModelsListChatGPTDiscoversAccountModels(t *testing.T) {
	src := &stubModelsSource{models: []llmopenai.ModelInfo{
		{Slug: "gpt-hidden", DisplayName: "Hidden", Visibility: "hide", Priority: 0},
		{Slug: "gpt-future", DisplayName: "GPT Future", Visibility: "list", Priority: 1,
			DefaultReasoningEffort: "low",
			SupportedReasoningEfforts: []llmopenai.ReasoningEffortPreset{
				{Effort: "low"}, {Effort: "max"},
			}},
		{Slug: "gpt-second", DisplayName: "GPT Second", Visibility: "list", Priority: 2},
	}}
	s := &Server{Core: turn.New(turn.WithChatGPTModels(src))}
	req := httptest.NewRequest(http.MethodGet, "/api/models?provider=chatgpt", nil)
	rr := httptest.NewRecorder()

	s.handleModelsList(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var out struct {
		Records []turn.ModelRecord        `json:"records"`
		Status  []turn.ModelCatalogStatus `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Len(t, out.Records, 2)
	require.Equal(t, "gpt-future", out.Records[0].APIModel)
	require.True(t, out.Records[0].IsDefault)
	require.Equal(t, []string{"low", "max"}, out.Records[0].ReasoningEfforts)
	require.Equal(t, "gpt-second", out.Records[1].APIModel)
	require.Len(t, out.Status, 1)
	require.Equal(t, "chatgpt", out.Status[0].Provider)
	require.Equal(t, turn.ModelSourceChatGPTAccount, out.Status[0].Source)
	require.NotNil(t, out.Status[0].FetchedAt)
	require.False(t, out.Status[0].FetchedAt.IsZero())
}

// A discovery failure must not read as an empty catalog: the response says the
// provider is logged-out/unavailable and keeps its shape.
func TestHandleModelsListReportsDiscoveryFailure(t *testing.T) {
	src := &stubModelsSource{err: errors.New("fetch ChatGPT models: http 401: token expired")}
	s := &Server{Core: turn.New(turn.WithChatGPTModels(src))}
	req := httptest.NewRequest(http.MethodGet, "/api/models?provider=chatgpt", nil)
	rr := httptest.NewRecorder()

	s.handleModelsList(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var out struct {
		Records []turn.ModelRecord        `json:"records"`
		Status  []turn.ModelCatalogStatus `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Empty(t, out.Records)
	require.Len(t, out.Status, 1)
	require.NotEmpty(t, out.Status[0].Error)
	require.Empty(t, out.Status[0].Source)
	require.Nil(t, out.Status[0].FetchedAt, "an error status must not claim a fetch time")
}

// A cancelled request stops the discovery instead of holding the listing.
func TestHandleModelsListHonorsRequestCancellation(t *testing.T) {
	src := &stubModelsSource{err: context.Canceled}
	s := &Server{Core: turn.New(turn.WithChatGPTModels(src))}
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	cancel()
	rr := httptest.NewRecorder()

	s.handleModelsList(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var out struct {
		Status []turn.ModelCatalogStatus `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Len(t, out.Status, 1)
	require.NotEmpty(t, out.Status[0].Error)
}

// The gateway wires discovery from its own home's credentials: a server
// without a source still answers from the static catalog and reports ChatGPT
// as unconfigured rather than guessing.
func TestHandleModelsListWithoutSourceReportsUnconfigured(t *testing.T) {
	s := &Server{Core: turn.New()}
	req := httptest.NewRequest(http.MethodGet, "/api/models?provider=chatgpt", nil)
	rr := httptest.NewRecorder()

	s.handleModelsList(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var out struct {
		Records []turn.ModelRecord        `json:"records"`
		Status  []turn.ModelCatalogStatus `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	require.Empty(t, out.Records)
	require.Len(t, out.Status, 1)
	require.NotEmpty(t, out.Status[0].Error)
}

// TestChatMessagesReportsWhoWroteEachUserRow pins the history contract for
// origin markers: a row written on the person's behalf says so, and the
// person's own row carries no origin key at all.
func TestChatMessagesReportsWhoWroteEachUserRow(t *testing.T) {
	ctx := context.Background()
	s := cronTestServer(t)
	s.Sessions = state.NewSessionStore(s.Env.SQL, "main")
	turn.PersistUserTurn(ctx, s.Sessions, turn.UserTurn{SessionID: "s1", ModelInput: "typed by hand", EnsureSession: true})
	turn.PersistUserTurn(ctx, s.Sessions, turn.UserTurn{SessionID: "s1", ModelInput: "anything new?", Origin: state.MessageOriginHeartbeat})

	rec := cronRequest(t, s, http.MethodGet, "/api/chat/sessions/s1/messages", nil, s.handleChatMessages, "id", "s1")
	if rec.Code != http.StatusOK {
		t.Fatalf("messages = %d %s", rec.Code, rec.Body.String())
	}
	var raw []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 2 {
		t.Fatalf("rows = %s", rec.Body.String())
	}
	if _, ok := raw[0]["origin"]; ok {
		t.Fatalf("the person's own row must carry no origin key: %s", rec.Body.String())
	}
	if raw[1]["role"] != "user" || raw[1]["content"] != "anything new?" || raw[1]["origin"] != "heartbeat" {
		t.Fatalf("heartbeat row = %#v", raw[1])
	}
}

// TestChatMessagesPlacesEachCompactionAsItsOwnRow pins the web history's
// account of compactions: every finished one is a row of its own, where the
// live conversation drew it — a manual one right after the checkpoint it
// wrote, a pre-turn one after the message it made room for — and a failure
// keeps its error.
func TestChatMessagesPlacesEachCompactionAsItsOwnRow(t *testing.T) {
	ctx := context.Background()
	s := cronTestServer(t)
	s.Sessions = state.NewSessionStore(s.Env.SQL, "main")
	_, _ = s.Sessions.Append(ctx, "s1", "user", "first question")
	_, _ = s.Sessions.Append(ctx, "s1", "assistant", "first answer")
	checkpoint := func(windowID string) {
		t.Helper()
		part := state.CompactBoundaryPart{Trigger: "manual", Strategy: "local", WindowID: windowID, WindowNumber: 1,
			ReplacementHistory: []llm.Message{llm.UserMessage(llm.Text(state.CompactSummaryPrefix + "\nsummary"))}}
		if err := s.Sessions.AppendCompactCheckpoint(ctx, "s1", "summary", part); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint("w-manual")
	checkpoint("w-preturn")
	_, _ = s.Sessions.Append(ctx, "s1", "user", "second question")
	_, _ = s.Sessions.Append(ctx, "s1", "assistant", "second answer")
	for _, evt := range []event.RunEvent{
		event.NewRunEvent("", "", "s1", event.RunEventContextCompacted, event.ContextCompactedPayload{CompactionID: "c1", Trigger: "manual", BoundaryID: "w-manual", TokensBefore: 900, TokensAfter: 100}, time.Now()),
		event.NewRunEvent("", "", "s1", event.RunEventContextCompacted, event.ContextCompactedPayload{CompactionID: "c2", Trigger: "auto", BoundaryID: "w-preturn"}, time.Now()),
		event.NewRunEvent("", "", "s1", event.RunEventContextCompactError, event.ContextCompactFailedPayload{CompactionID: "c3", Trigger: "manual", Error: "provider down"}, time.Now().Add(time.Hour)),
	} {
		if _, err := s.RunRT.AppendSessionEvent(ctx, sessionEventRecord(evt)); err != nil {
			t.Fatal(err)
		}
	}
	rec := cronRequest(t, s, http.MethodGet, "/api/chat/sessions/s1/messages", nil, s.handleChatMessages, "id", "s1")
	if rec.Code != http.StatusOK {
		t.Fatalf("messages = %d %s", rec.Code, rec.Body.String())
	}
	var rows []struct {
		Role       string `json:"role"`
		Content    string `json:"content"`
		Compaction *struct {
			Status       string `json:"status"`
			CompactionID string `json:"compaction_id"`
			Error        string `json:"error"`
			TokensBefore int    `json:"tokens_before"`
		} `json:"compaction"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, row := range rows {
		if row.Compaction != nil {
			got = append(got, row.Role+":"+row.Compaction.Status+":"+row.Compaction.CompactionID+row.Compaction.Error)
			continue
		}
		got = append(got, row.Role+":"+row.Content)
	}
	want := []string{
		"user:first question", "assistant:first answer",
		"compaction:done:c1",
		"user:second question",
		"compaction:done:c2",
		"assistant:second answer",
		"compaction:failed:c3provider down",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rows =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestChatMessagesPlacesAGoalsLinesWhereTheTerminalDrawsThem pins the web
// history's account of a /goal to the terminal's: the goal opens right after
// the message that asked for it, each round's line comes before that round's
// answer, the runtime's continuation prompt is not shown, and the goal ends
// after its last round and before the user's next message.
func TestChatMessagesPlacesAGoalsLinesWhereTheTerminalDrawsThem(t *testing.T) {
	ctx := context.Background()
	s := cronTestServer(t)
	s.Sessions = state.NewSessionStore(s.Env.SQL, "main")
	seedRun(t, s.RunRT, "s1", "r1")
	base := time.Unix(1_700_000_000, 0)
	appendRow := func(role string, msg llm.Message, at time.Duration) int64 {
		t.Helper()
		id, err := s.Sessions.AppendStructuredMessage(ctx, "s1", role, msg.TextContent(), "", state.MessagePartsJSON(msg, msg.TextContent()), "", "", "", "", state.MessageExecTiming{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Env.SQL.ExecContext(ctx, `UPDATE fb_messages SET created_at = ? WHERE id = ?`, base.Add(at).Unix(), id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	continuation := llm.UserMessage(llm.Text(run.ContinuationPrompt("ship", "two tests fail")))
	continuation.IsMeta = true
	asked := appendRow("user", llm.UserMessage(llm.Text("/goal ship")), 0)
	roundOne := appendRow("assistant", llm.AssistantMessage([]llm.ContentPart{llm.Text("round one")}), 2*time.Second)
	appendRow("user", continuation, 4*time.Second)
	appendRow("assistant", llm.AssistantMessage([]llm.ContentPart{llm.Text("round two")}), 6*time.Second)
	appendRow("user", llm.UserMessage(llm.Text("thanks")), 60*time.Second)
	for _, evt := range []event.RunEvent{
		event.NewRunEvent("goal-started:r1", "r1", "s1", event.RunEventGoalStarted, event.GoalStartedPayload{Objective: "ship", MaxRounds: run.MaxGoalRounds, AfterRowID: asked}, base),
		event.NewRunEvent("goal-round:r1:2", "r1", "s1", event.RunEventGoalRoundStarted, event.GoalRoundStartedPayload{Round: 2, Why: "two tests fail", CheckAgentID: "check-1", AfterRowID: roundOne}, base.Add(3*time.Second)),
		event.NewRunEvent("goal-completed:r1", "r1", "s1", event.RunEventGoalCompleted, event.GoalCompletedPayload{Objective: "ship", Status: event.GoalStatusDone, Rounds: 2, Why: "all pass", DurationMs: 8_000, CheckAgentID: "check-2"}, base.Add(8*time.Second)),
	} {
		if _, err := s.RunRT.AppendSessionEvent(ctx, sessionEventRecord(evt)); err != nil {
			t.Fatal(err)
		}
	}
	rec := cronRequest(t, s, http.MethodGet, "/api/chat/sessions/s1/messages", nil, s.handleChatMessages, "id", "s1")
	if rec.Code != http.StatusOK {
		t.Fatalf("messages = %d %s", rec.Code, rec.Body.String())
	}
	var rows []struct {
		Role    string       `json:"role"`
		Content string       `json:"content"`
		Goal    *chatGoalRow `json:"goal"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, row := range rows {
		if row.Goal != nil {
			got = append(got, fmt.Sprintf("goal:%s:%d:%s:%s", row.Goal.Phase, max(row.Goal.Round, row.Goal.Rounds), row.Goal.Why+row.Goal.Objective, row.Goal.CheckAgentID))
			continue
		}
		got = append(got, row.Role+":"+row.Content)
	}
	want := []string{
		"user:/goal ship",
		"goal:started:0:ship:",
		"assistant:round one",
		"goal:round:2:two tests fail:check-1",
		"assistant:round two",
		"goal:completed:2:all passship:check-2",
		"user:thanks",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rows =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// newTimingSnapshotServer builds the conversation T4's timing rules govern: a
// completed run with assistant rows, a tool row carrying its own execution
// timing, and a `!cmd` user row carrying the shell command's timing outside
// any run.
func newTimingSnapshotServer(t *testing.T) (*Server, *state.SessionStore) {
	t.Helper()
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sessions := state.NewSessionStore(db, "main")
	if err := sessions.Ensure(ctx, "s1", "s1"); err != nil {
		t.Fatal(err)
	}
	runs := &state.RunStore{DB: db}
	run, err := runs.CreateRun(ctx, "s1", "review the diff")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	finish := start.Add(90 * time.Second)
	if _, err := sessions.Append(ctx, "s1", "user", "review the diff"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.AppendStructuredMessageForRun(ctx, "s1", run.ID, "assistant", "checked", "m-a", "[]", "model-x", "{}", "", "", state.MessageExecTiming{}); err != nil {
		t.Fatal(err)
	}
	// The run's own clock is stamped when the run ends.
	if err := sessions.AppendMessageSequenceForRun(ctx, "s1", run.ID, []llm.Message{llm.AssistantMessage([]llm.ContentPart{llm.Text("checked")})}, "model-x", ""); err != nil {
		t.Fatal(err)
	}
	if err := sessions.StampRunTiming(ctx, run.ID, state.RunTiming{StartedAt: start, FinishedAt: finish, Worked: 90 * time.Second}); err != nil {
		t.Fatal(err)
	}
	// A tool row: its timing is the tool call's own execution window.
	toolStart := start.Add(10 * time.Second)
	toolFinish := toolStart.Add(5 * time.Second)
	if _, err := sessions.AppendToolTurnWithMeta(ctx, "s1", "tool", "shell output", "call-1", `{"tool_name":"shell"}`,
		state.MessageExecTiming{StartedAtMs: toolStart.UnixMilli(), FinishedAtMs: toolFinish.UnixMilli(), DurationMs: 5_000}); err != nil {
		t.Fatal(err)
	}
	// A `!cmd` user row: timing of a command the user ran, outside any run.
	if _, err := sessions.Append(ctx, "s1", "user", "next question"); err != nil {
		t.Fatal(err)
	}
	cmdStart := finish.Add(30 * time.Second)
	if _, err := sessions.AppendToolTurnWithMeta(ctx, "s1", "user", "$ git status\nok", "", `{"kind":"user_shell"}`,
		state.MessageExecTiming{StartedAtMs: cmdStart.UnixMilli(), FinishedAtMs: cmdStart.Add(2 * time.Second).UnixMilli(), DurationMs: 2_000}); err != nil {
		t.Fatal(err)
	}
	return &Server{Sessions: sessions, RunRT: runs}, sessions
}

// A run that failed before it said anything still ended, and the page closed
// it with its "Worked for" line; the history closes it the same way, after
// the user's message that is the run's only row.
func TestChatMessagesCloseASilentFailedRunAfterItsMessage(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(ctx, "s1", "s1"))
	runs := &state.RunStore{DB: db}
	failed, err := runs.CreateRun(ctx, "s1", "hello")
	require.NoError(t, err)
	_, err = turn.PersistUserTurn(ctx, sessions, turn.UserTurn{SessionID: "s1", RunID: failed.ID, ModelInput: "hello"})
	require.NoError(t, err)
	start := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	turn.PersistCancelledTurn(ctx, sessions, turn.CancelledTurn{SessionID: "s1", RunID: failed.ID, End: turn.RunEnd{StartedAt: start, FinishedAt: start.Add(2 * time.Second), Worked: 2 * time.Second}})
	_, err = turn.PersistUserTurn(ctx, sessions, turn.UserTurn{SessionID: "s1", ModelInput: "again"})
	require.NoError(t, err)

	s := &Server{Sessions: sessions, RunRT: runs}
	rec := cronRequest(t, s, http.MethodGet, "/api/chat/sessions/s1/messages", nil, s.handleChatMessages, "id", "s1")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var rows []struct {
		Role     string `json:"role"`
		Content  string `json:"content"`
		RunID    string `json:"run_id"`
		WorkedMs int64  `json:"worked_duration_ms"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	var got []string
	for _, row := range rows {
		got = append(got, row.Role+":"+row.Content)
	}
	require.Equal(t, []string{"user:hello", "worked:", "user:again"}, got)
	require.Equal(t, failed.ID, rows[1].RunID)
	require.Equal(t, int64(2_000), rows[1].WorkedMs)
}

// TestChatMessagesTimingFieldsStable pins the /messages response's timing
// fields: the run window and worked duration on the worked row that closes a
// run after the last row it wrote, the execution window on tool rows, and the
// command window on `!cmd` user rows — all as RFC3339 strings and millisecond
// counts.
func TestChatMessagesTimingFieldsStable(t *testing.T) {
	s, _ := newTimingSnapshotServer(t)
	rec := cronRequest(t, s, http.MethodGet, "/api/chat/sessions/s1/messages", nil, s.handleChatMessages, "id", "s1")
	if rec.Code != http.StatusOK {
		t.Fatalf("messages = %d %s", rec.Code, rec.Body.String())
	}
	var rows []struct {
		Role       string `json:"role"`
		RunID      string `json:"run_id,omitempty"`
		RunStarted string `json:"run_started_at,omitempty"`
		RunFinish  string `json:"run_finished_at,omitempty"`
		WorkedMs   int64  `json:"worked_duration_ms,omitempty"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	var worked, tool, cmd *struct {
		Role       string `json:"role"`
		RunID      string `json:"run_id,omitempty"`
		RunStarted string `json:"run_started_at,omitempty"`
		RunFinish  string `json:"run_finished_at,omitempty"`
		WorkedMs   int64  `json:"worked_duration_ms,omitempty"`
	}
	workedAt := -1
	for i := range rows {
		switch {
		case rows[i].Role == "assistant" && rows[i].WorkedMs != 0:
			t.Fatalf("assistant row %d carries the run's clock; the worked row does", i)
		case rows[i].Role == "worked" && worked == nil:
			worked, workedAt = &rows[i], i
		case rows[i].Role == "tool" && tool == nil:
			tool = &rows[i]
		case rows[i].Role == "user" && rows[i].WorkedMs != 0:
			cmd = &rows[i]
		}
	}
	if worked == nil || tool == nil || cmd == nil {
		t.Fatalf("rows = %+v, want a worked, a tool and a timed user row", rows)
	}
	if worked.RunStarted == "" || worked.RunFinish == "" || worked.WorkedMs != 90_000 || worked.RunID == "" {
		t.Fatalf("worked timing = %q %q %d %q", worked.RunStarted, worked.RunFinish, worked.WorkedMs, worked.RunID)
	}
	// The run's last row is its assistant answer; the line follows it.
	if workedAt == 0 || rows[workedAt-1].Role != "assistant" {
		t.Fatalf("worked row at %d follows %+v, want the run's last row", workedAt, rows[workedAt-1])
	}
	if tool.RunStarted == "" || tool.RunFinish == "" || tool.WorkedMs != 5_000 {
		t.Fatalf("tool timing = %q %q %d", tool.RunStarted, tool.RunFinish, tool.WorkedMs)
	}
	if cmd.RunStarted == "" || cmd.RunFinish == "" || cmd.WorkedMs != 2_000 {
		t.Fatalf("!cmd timing = %q %q %d", cmd.RunStarted, cmd.RunFinish, cmd.WorkedMs)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/s1/messages", nil)
	_ = req
}

// TestChatSurfacesRenderTheSessionFactSpread is T5's protection net: it builds
// one conversation carrying every kind of fact the event merge touches — a
// main-agent tool call, a main-agent plan update, a subagent's lifecycle and
// its own tool call and plan, a goal, and an approval — then asserts what the
// two web surfaces report for it. The storage behind those facts changes when
// the run-step ledger and the tool-audit table merge into the session event
// log; the facts a surface sees must not.
func TestChatSurfacesRenderTheSessionFactSpread(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	defer db.Close()
	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(ctx, "s1", "s1"))
	runs := &state.RunStore{DB: db}
	parent, err := runs.CreateRun(ctx, "s1", "review the diff")
	require.NoError(t, err)
	child, err := runs.CreateSubagentRun(ctx, parent.ID, "s1", "verification")
	require.NoError(t, err)

	// Transcript: the main agent's tool call and its result, plus the closing
	// assistant text.
	_, err = sessions.Append(ctx, "s1", "user", "review the diff")
	require.NoError(t, err)
	callParts := `[{"type":"tool_call","id":"call-1","name":"read_file","arguments":"{\"file_path\":\"/repo/a.go\"}"}]`
	_, err = sessions.AppendStructuredMessageForRun(ctx, "s1", parent.ID, "assistant", "", "m-1", callParts, "model", "", "call-1", `{"tool_name":"read_file"}`, state.MessageExecTiming{})
	require.NoError(t, err)
	_, err = sessions.AppendToolTurnWithMeta(ctx, "s1", "tool", "file body", "call-1", `{"tool_name":"read_file"}`, state.MessageExecTiming{StartedAtMs: 1780000000000, FinishedAtMs: 1780000000500, DurationMs: 500})
	require.NoError(t, err)
	_, err = sessions.AppendStructuredMessageForRun(ctx, "s1", parent.ID, "assistant", "done", "m-3", `[]`, "model", "", "", "", state.MessageExecTiming{})
	require.NoError(t, err)

	// Main-agent tool events, its plan update, and the subagent's lifecycle:
	// all of them live in the one canonical event log now.
	appendEvent := func(id, runID, typ string, payload any) {
		raw, err := json.Marshal(payload)
		require.NoError(t, err)
		_, err = runs.AppendSessionEvent(ctx, state.SessionEvent{ID: id, SessionID: "s1", RunID: runID, Type: typ, Payload: raw, CreatedAt: time.Now()})
		require.NoError(t, err)
	}
	appendEvent("evt-tool-start", parent.ID, event.RunEventToolStarted, event.ToolCallStartedPayload{Kind: event.RunEventToolStarted, StepID: "call-1", ToolName: "read_file"})
	appendEvent("evt-tool-done", parent.ID, event.RunEventToolCompleted, event.ToolCallCompletedPayload{Kind: event.RunEventToolCompleted, StepID: "call-1", ToolName: "read_file", Output: map[string]any{"content": "file body"}, DurationSeconds: 0.5})
	appendEvent("evt-plan", parent.ID, event.RunEventPlanUpdated, event.PlanUpdatedPayload{Title: "Updated Plan", Items: []event.PlanUpdateItem{{ID: "T1", Content: "step one", Status: "completed"}}})
	appendEvent("evt-sub-spawn", child.ID, event.RunEventSubagentSpawned, event.SubagentSpawnedPayload{AgentID: "task-7", AgentType: "verification", TaskID: "task-7", Task: "check it"})
	appendEvent("evt-sub-end", child.ID, event.RunEventSubagentEnded, event.SubagentEndedPayload{AgentID: "task-7", AgentType: "verification", TaskID: "task-7", Status: "completed", Output: "looks good"})

	// Canonical events already in the log: the turn's bounds and its goal.
	_, err = runs.AppendSessionEvent(ctx, state.SessionEvent{ID: "evt-turn-start", SessionID: "s1", RunID: parent.ID, Type: event.RunEventTurnStarted, CreatedAt: time.Now()})
	require.NoError(t, err)
	goalPayload, err := json.Marshal(event.GoalStartedPayload{Objective: "ship it"})
	require.NoError(t, err)
	_, err = runs.AppendSessionEvent(ctx, state.SessionEvent{ID: "evt-goal-start", SessionID: "s1", RunID: parent.ID, Type: event.RunEventGoalStarted, Payload: goalPayload, CreatedAt: time.Now()})
	require.NoError(t, err)

	// An approval raised by the running turn.
	approvalPayload, err := json.Marshal(event.ApprovalRequestedPayload{ActionID: "act-1", ActionKind: "shell", ToolStepID: "call-1"})
	require.NoError(t, err)
	_, err = runs.AppendSessionEvent(ctx, state.SessionEvent{ID: "evt-approval", SessionID: "s1", RunID: parent.ID, Type: event.RunEventApprovalReq, Payload: approvalPayload, CreatedAt: time.Now()})
	require.NoError(t, err)

	s := &Server{Sessions: sessions, RunRT: runs, Actions: &state.ActionService{DB: db}}
	_ = child

	// The transcript the web chat shows: the four rows, in order, with the run
	// each belongs to.
	rec := cronRequest(t, s, http.MethodGet, "/api/chat/sessions/s1/messages", nil, s.handleChatMessages, "id", "s1")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var rows []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
		RunID   string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	// The transcript rows, in order; the goal is a row of its own at the
	// position the live conversation drew it.
	var transcript []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
		RunID   string `json:"run_id"`
	}
	goalRows := 0
	for _, row := range rows {
		if row.Role == "goal" {
			goalRows++
			continue
		}
		transcript = append(transcript, row)
	}
	require.Len(t, transcript, 4)
	require.Equal(t, []string{"user", "assistant", "tool", "assistant"},
		[]string{transcript[0].Role, transcript[1].Role, transcript[2].Role, transcript[3].Role})
	require.Equal(t, "review the diff", transcript[0].Content)
	require.Equal(t, "file body", transcript[2].Content)
	require.Equal(t, "done", transcript[3].Content)
	require.Equal(t, parent.ID, transcript[1].RunID)
	require.Equal(t, 1, goalRows, "the goal event must render as its own row")

	// The event page the web stream replays from: every fact above is in it.
	evRec := cronRequest(t, s, http.MethodGet, "/api/chat/sessions/s1/events", nil, s.handleChatSessionEvents, "id", "s1")
	require.Equal(t, http.StatusOK, evRec.Code, evRec.Body.String())
	var page struct {
		Events []struct {
			Type    string `json:"type"`
			EventID string `json:"id"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(evRec.Body.Bytes(), &page))
	types := map[string]bool{}
	for _, e := range page.Events {
		types[e.Type] = true
	}
	for _, want := range []string{
		event.RunEventTurnStarted, event.RunEventGoalStarted, event.RunEventApprovalReq,
		event.RunEventToolStarted, event.RunEventToolCompleted, event.RunEventPlanUpdated,
		event.RunEventSubagentSpawned, event.RunEventSubagentEnded,
	} {
		require.True(t, types[want], "event page missing %s; got %v", want, types)
	}
}

// A file belongs to the conversation that uploaded it. The endpoints resolve a
// file against the gateway's own tenant, so another agent's file reads as
// missing rather than as data one tenant can fetch for another.
func TestFileEndpointsHideAnotherAgentsFile(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	mustGatewaySession(t, db, "s-mine")
	require.NoError(t, state.NewSessionStore(db, "other").Ensure(ctx, "s-other", "s-other"))

	files := &state.FileStore{DB: db, Home: t.TempDir(), Cfg: state.LoadConfigFromEnv()}
	mine, err := files.CreateFromReader(ctx, "s-mine", "mine.txt", strings.NewReader("hi"), 2, "text/plain")
	require.NoError(t, err)
	theirs, err := files.CreateFromReader(ctx, "s-other", "theirs.txt", strings.NewReader("hi"), 2, "text/plain")
	require.NoError(t, err)
	// Extract the owner's text so the text endpoint has something to serve.
	_, _, err = files.EnsureParsedText(ctx, "main", mine.ID)
	require.NoError(t, err)

	s := &Server{Files: files, Sessions: state.NewSessionStore(db, "main")}
	withID := func(req *http.Request, id string) *http.Request {
		return req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: id}}))
	}

	handlers := map[string]http.HandlerFunc{
		"file-one":      s.handleFileOne,
		"file-download": s.handleFileDownload,
		"file-text":     s.handleFileText,
	}
	for name, h := range handlers {
		rec := httptest.NewRecorder()
		h(rec, withID(httptest.NewRequest(http.MethodGet, "/api/files/x", nil), mine.ID))
		require.Equal(t, http.StatusOK, rec.Code, "%s: the owner's file must be served", name)

		rec = httptest.NewRecorder()
		h(rec, withID(httptest.NewRequest(http.MethodGet, "/api/files/x", nil), theirs.ID))
		require.Equal(t, http.StatusNotFound, rec.Code, "%s: another agent's file must read as missing", name)
	}
}

// TestHeartbeatEndpointsAnswerOnlyForOwnedSessions pins that a heartbeat is
// part of its conversation: another primary agent's session reads as missing
// to every method, so its recurring instruction can be neither read, replaced
// nor cleared from here.
func TestHeartbeatEndpointsAnswerOnlyForOwnedSessions(t *testing.T) {
	s := cronTestServer(t)
	s.Sessions = state.NewSessionStore(s.RunRT.DB, "main")
	mustGatewaySession(t, s.RunRT.DB, "s-mine")
	require.NoError(t, state.NewSessionStore(s.RunRT.DB, "other").Ensure(context.Background(), "s-theirs", "s-theirs"))
	require.NoError(t, (&state.CronStore{DB: s.RunRT.DB}).SaveHeartbeat(context.Background(), state.Heartbeat{SessionID: "s-theirs", IntervalSec: 600, Prompt: "theirs"}))

	put := cronRequest(t, s, http.MethodPut, "/api/heartbeat", map[string]any{
		"session_id": "s-theirs", "interval_seconds": 600, "prompt": "hijack",
	}, s.handleHeartbeat)
	require.Equal(t, http.StatusNotFound, put.Code, put.Body.String())
	get := cronRequest(t, s, http.MethodGet, "/api/heartbeat?session_id=s-theirs", nil, s.handleHeartbeat)
	require.Equal(t, http.StatusNotFound, get.Code, get.Body.String())
	del := cronRequest(t, s, http.MethodDelete, "/api/heartbeat?session_id=s-theirs", nil, s.handleHeartbeat)
	require.Equal(t, http.StatusNotFound, del.Code, del.Body.String())
	hb, err := (&state.CronStore{DB: s.RunRT.DB}).GetHeartbeat(context.Background(), "s-theirs")
	require.NoError(t, err)
	require.NotNil(t, hb)
	require.Equal(t, "theirs", hb.Prompt)

	mine := cronRequest(t, s, http.MethodPut, "/api/heartbeat", map[string]any{
		"session_id": "s-mine", "interval_seconds": 600, "prompt": "mine",
	}, s.handleHeartbeat)
	require.Equal(t, http.StatusOK, mine.Code, mine.Body.String())
}

// TestHeartbeatsListNamesEachConversation pins the list the chat page reads to
// show which conversations a heartbeat asks again: this agent's beats only,
// each with its conversation's title, and an unnamed conversation reads as
// untitled rather than as its id.
func TestHeartbeatsListNamesEachConversation(t *testing.T) {
	s := cronTestServer(t)
	s.Sessions = state.NewSessionStore(s.RunRT.DB, "main")
	ctx := context.Background()
	require.NoError(t, s.Sessions.Ensure(ctx, "s-named", "Release notes"))
	mustGatewaySession(t, s.RunRT.DB, "s-unnamed")
	require.NoError(t, state.NewSessionStore(s.RunRT.DB, "other").Ensure(ctx, "s-theirs", "Theirs"))
	store := &state.CronStore{DB: s.RunRT.DB}
	require.NoError(t, store.SaveHeartbeat(ctx, state.Heartbeat{SessionID: "s-theirs", IntervalSec: 600, Prompt: "theirs"}))
	for _, sid := range []string{"s-named", "s-unnamed"} {
		rec := cronRequest(t, s, http.MethodPut, "/api/heartbeat", map[string]any{
			"session_id": sid, "interval_seconds": 600, "prompt": "anything new?", "paused": sid == "s-unnamed",
		}, s.handleHeartbeat)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	rec := cronRequest(t, s, http.MethodGet, "/api/heartbeats", nil, s.handleHeartbeats)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		Records []struct {
			SessionID    string `json:"session_id"`
			SessionTitle string `json:"session_title"`
			Paused       bool   `json:"paused"`
		} `json:"records"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	got := map[string]string{}
	paused := map[string]bool{}
	for _, row := range out.Records {
		got[row.SessionID] = row.SessionTitle
		paused[row.SessionID] = row.Paused
	}
	require.Equal(t, map[string]string{"s-named": "Release notes", "s-unnamed": ""}, got)
	require.Equal(t, map[string]bool{"s-named": false, "s-unnamed": true}, paused)
}

// TestFileUploadBelongsToAnOwnedSession pins that an upload lands in one of
// this agent's conversations: without a session it is refused in one
// sentence, and another agent's session reads as missing, instead of the file
// being written under an invented "default" session the row's foreign key
// rejects.
func TestFileUploadBelongsToAnOwnedSession(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	mustGatewaySession(t, db, "s-mine")
	require.NoError(t, state.NewSessionStore(db, "other").Ensure(ctx, "s-other", "s-other"))
	s := &Server{
		Files:    &state.FileStore{DB: db, Home: t.TempDir(), Cfg: state.LoadConfigFromEnv()},
		Sessions: state.NewSessionStore(db, "main"),
	}
	upload := func(sessionID string) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		if sessionID != "" {
			require.NoError(t, form.WriteField("session_id", sessionID))
		}
		part, err := form.CreateFormFile("file", "note.txt")
		require.NoError(t, err)
		_, err = part.Write([]byte("hello"))
		require.NoError(t, err)
		require.NoError(t, form.Close())
		req := httptest.NewRequest(http.MethodPost, "/api/files", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		rec := httptest.NewRecorder()
		s.handleFiles(rec, req)
		return rec
	}
	require.Equal(t, http.StatusBadRequest, upload("").Code)
	require.Equal(t, http.StatusNotFound, upload("s-other").Code)
	created := upload("s-mine")
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var out struct {
		FileID string `json:"file_id"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &out))
	f, err := s.Files.Get(ctx, "main", out.FileID)
	require.NoError(t, err)
	require.Equal(t, "s-mine", f.SessionID)
}

// TestAskActionBelongsToAnOwnedSession pins that an ask posted over HTTP is
// raised in one of this agent's conversations only.
func TestAskActionBelongsToAnOwnedSession(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	mustGatewaySession(t, db, "s-mine")
	require.NoError(t, state.NewSessionStore(db, "other").Ensure(ctx, "s-other", "s-other"))
	s := &Server{Actions: &state.ActionService{DB: db}, Sessions: state.NewSessionStore(db, "main")}
	ask := func(sessionID string) *httptest.ResponseRecorder {
		t.Helper()
		form := map[string]any{"questions": []map[string]any{{
			"id": "q1", "prompt": "which?", "options": []map[string]any{{"id": "a", "label": "A"}, {"id": "b", "label": "B"}},
		}}}
		if sessionID != "" {
			form["session_id"] = sessionID
		}
		raw, err := json.Marshal(form)
		require.NoError(t, err)
		rec := httptest.NewRecorder()
		s.handleActionsAsk(rec, httptest.NewRequest(http.MethodPost, "/api/actions/ask", bytes.NewReader(raw)))
		return rec
	}
	require.Equal(t, http.StatusBadRequest, ask("").Code)
	require.Equal(t, http.StatusNotFound, ask("s-other").Code)
	require.Equal(t, http.StatusCreated, ask("s-mine").Code)
}

// TestActionsRequesterFilterIsAppliedBeforeTheLimit pins D2's rule for the
// actions list: narrowing to the subagent that raised an action happens in
// the query, so a page limit applies to that subagent's actions rather than to
// a page of everyone's truncated first.
func TestActionsRequesterFilterIsAppliedBeforeTheLimit(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	mustGatewaySession(t, db, "s-1")
	_, err = db.ExecContext(ctx, `
INSERT INTO fb_actions(id,session_id,kind,status,payload_json,answer_json,error,created_at,updated_at)
VALUES('worker-action','s-1','shell','pending','{"agent_id":"task-7"}','','',1,1)`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 50)
INSERT INTO fb_actions(id,session_id,kind,status,payload_json,answer_json,error,created_at,updated_at)
SELECT printf('main-%02d',i),'s-1','shell','pending','{}','','',i+10,i+10 FROM n`)
	require.NoError(t, err)
	s := &Server{Home: t.TempDir(), Actions: &state.ActionService{DB: db}, Sessions: state.NewSessionStore(db, "main")}
	rr := httptest.NewRecorder()
	s.handleActions(rr, httptest.NewRequest(http.MethodGet, "/api/actions?agent_id=task-7&limit=10", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var rows []actionListRow
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	require.Equal(t, "worker-action", rows[0].ID)
	require.Equal(t, "task-7", rows[0].AgentID)
}

// TestProjectSessionIsBornInTheProjectAndLeavesOthersAlone pins the fix for
// the store-wide default rewrite: a session opened inside a project is born
// with the project root as its cwd, and the next ordinary chat is still born
// in the process's launch directory instead of inheriting the project's.
func TestProjectSessionIsBornInTheProjectAndLeavesOthersAlone(t *testing.T) {
	s, _, _ := newProjectsTestServer(t)
	s.Sessions.ConfigureMemoryDefaults("disabled", "webchat", "/launch", "main")

	root := projectTempRoot(t)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s, http.MethodPost, "/api/v1/projects", map[string]any{"name": "alpha", "root": root, "trust": true})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rec.Code, rec.Body.String())
	}
	var project struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}

	rec = doJSON(t, s, http.MethodPost, "/api/v1/projects/"+project.ID+"/sessions", map[string]any{"title": "In project"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project session: %d %s", rec.Code, rec.Body.String())
	}
	var born struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &born); err != nil {
		t.Fatal(err)
	}

	srec := httptest.NewRecorder()
	s.handleChatSessionCreate(srec, httptest.NewRequest(http.MethodPost, "/api/chat/sessions", strings.NewReader(`{"title":"Ordinary"}`)))
	if srec.Code != http.StatusCreated {
		t.Fatalf("create ordinary session: %d %s", srec.Code, srec.Body.String())
	}
	var ordinary struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(srec.Body.Bytes(), &ordinary); err != nil {
		t.Fatal(err)
	}

	cwdOf := func(id string) string {
		t.Helper()
		var cwd string
		if err := s.Sessions.DB().QueryRow("SELECT cwd FROM fb_sessions WHERE id=?", id).Scan(&cwd); err != nil {
			t.Fatal(err)
		}
		return cwd
	}
	if got := cwdOf(born.ID); got != root {
		t.Fatalf("project session born at %q, want the project root %q", got, root)
	}
	if got := cwdOf(ordinary.ID); got != "/launch" {
		t.Fatalf("ordinary session born at %q, want the launch directory the fixture set", got)
	}
}

// TestNewWebChatIsNamedByItsFirstMessage pins that a chat the web opens with
// "New chat" is born unnamed — the list shows no title, so the client draws
// its own placeholder — and that its first user message then names it, the
// way a terminal session is named, instead of a stored placeholder outranking
// that message forever.
func TestNewWebChatIsNamedByItsFirstMessage(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	sessions := state.NewSessionStore(db, "main")
	s := &Server{Sessions: sessions}

	rec := httptest.NewRecorder()
	s.handleChatSessionCreate(rec, httptest.NewRequest(http.MethodPost, "/api/chat/sessions", strings.NewReader(`{"title":""}`)))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.NotEmpty(t, created.ID)

	listTitle := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		s.handleChatSessions(rec, httptest.NewRequest(http.MethodGet, "/api/chat/sessions", nil))
		var list struct {
			Records []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"records"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
		for _, row := range list.Records {
			if row.ID == created.ID {
				return row.Title
			}
		}
		t.Fatalf("session %s missing from the list", created.ID)
		return ""
	}
	require.Empty(t, listTitle())

	_, err = sessions.Append(ctx, created.ID, "user", "Plan the release checklist")
	require.NoError(t, err)
	require.Equal(t, "Plan the release checklist", listTitle())
}

// TestCronJobBindsOnlyToThisAgentsProject pins that a job's project binding
// is tenant data: another agent's project, or one that does not exist, is
// refused as not found instead of binding the job across tenants or failing on
// the raw foreign-key error.
func TestCronJobBindsOnlyToThisAgentsProject(t *testing.T) {
	s := cronTestServer(t)
	ctx := context.Background()
	active, err := s.activePrimarySummary()
	require.NoError(t, err)
	theirs, err := state.NewProjectStore(s.RunRT.DB, "someone-else").Create(ctx, state.CreateProjectInput{Name: "theirs", Root: t.TempDir()})
	require.NoError(t, err)
	mine, err := state.NewProjectStore(s.RunRT.DB, active.ID).Create(ctx, state.CreateProjectInput{Name: "mine", Root: t.TempDir()})
	require.NoError(t, err)

	for _, projectID := range []string{theirs.ID, "prj_missing"} {
		rec := cronRequest(t, s, http.MethodPost, "/api/cron", map[string]any{
			"schedule": "every 1h", "prompt": "report", "project_id": projectID,
		}, s.handleCronJobs)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "project not found")
	}
	rec := cronRequest(t, s, http.MethodPost, "/api/cron", map[string]any{
		"schedule": "every 1h", "prompt": "report", "project_id": mine.ID,
	}, s.handleCronJobs)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func rulesServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	cfgPath := filepath.Join(home, "forebrain.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("agents:\n  definitions:\n    main:\n      primary: true\n"), 0o600))
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{"main": {Primary: true}}
	env := &process.Environment{Deps: run.Deps{Home: home, AppCfg: cfg}}
	env.ConfigPath = cfgPath
	return &Server{Home: home, Env: env}, home, workspace
}

func TestAgentRuleFilesListAndWhitelist(t *testing.T) {
	s, _, workspace := rulesServer(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("hello"), 0o644))

	rr := httptest.NewRecorder()
	s.handleAgentRuleFiles(rr, httptest.NewRequest(http.MethodGet, "/api/rules/agent", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.Contains(t, body, `"name":"AGENTS.md","exists":true`)
	require.Contains(t, body, `"name":"USER.md","exists":false`)

	rr = httptest.NewRecorder()
	req := withParamName(httptest.NewRequest(http.MethodGet, "/api/rules/agent/x", nil), "../../etc/passwd")
	s.handleAgentRuleFile(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "unsupported rule file")
}

func TestAgentRuleFilePutGetRoundTrip(t *testing.T) {
	s, _, workspace := rulesServer(t)

	rr := httptest.NewRecorder()
	req := withParamName(httptest.NewRequest(http.MethodPut, "/api/rules/agent/USER.md", strings.NewReader("be kind")), "USER.md")
	s.handleAgentRuleFile(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	rr = httptest.NewRecorder()
	req = withParamName(httptest.NewRequest(http.MethodGet, "/api/rules/agent/USER.md", nil), "USER.md")
	s.handleAgentRuleFile(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var got struct {
		Name    string `json:"name"`
		Exists  bool   `json:"exists"`
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	require.Equal(t, "USER.md", got.Name)
	require.True(t, got.Exists)
	require.Equal(t, "be kind", got.Content)

	raw, err := os.ReadFile(filepath.Join(workspace, "USER.md"))
	require.NoError(t, err)
	require.Equal(t, "be kind", string(raw))
}

// A file that does not exist yet answers in the same shape as one that does,
// so no reply can be mistaken for file content — even a file whose text
// happens to look like a status reply.
func TestAgentRuleFileReadAnswersOneShape(t *testing.T) {
	s, _, workspace := rulesServer(t)
	read := func(name string) map[string]any {
		rr := httptest.NewRecorder()
		s.handleAgentRuleFile(rr, withParamName(httptest.NewRequest(http.MethodGet, "/api/rules/agent/"+name, nil), name))
		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
		var out map[string]any
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
		return out
	}
	missing := read("SOUL.md")
	require.Equal(t, false, missing["exists"])
	require.Equal(t, "", missing["content"])

	lookalike := `{"exists":false,"content":""}`
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "SOUL.md"), []byte(lookalike), 0o644))
	present := read("SOUL.md")
	require.Equal(t, true, present["exists"])
	require.Equal(t, lookalike, present["content"])
}

func TestAgentRuleFileBudgetWarning(t *testing.T) {
	s, _, _ := rulesServer(t)
	big := bytes.Repeat([]byte("x"), assemblyBudgetBytes+1)
	rr := httptest.NewRecorder()
	req := withParamName(httptest.NewRequest(http.MethodPut, "/api/rules/agent/SOUL.md", bytes.NewReader(big)), "SOUL.md")
	s.handleAgentRuleFile(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "exceeds_assembly_budget")

	rr = httptest.NewRecorder()
	req = withParamName(httptest.NewRequest(http.MethodPut, "/api/rules/agent/SOUL.md", strings.NewReader("short")), "SOUL.md")
	s.handleAgentRuleFile(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NotContains(t, rr.Body.String(), "warning")
}

func TestProjectRuleDirValidationOnly(t *testing.T) {
	// Direct path validation without a project store: the dir gate is the
	// security boundary under test.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755))
	for _, dir := range []string{"../", "docs/nested", "missing"} {
		_, ok := projectRulePath(root, dir)
		require.False(t, ok, "dir=%s should be rejected", dir)
	}
	full, ok := projectRulePath(root, "docs")
	require.True(t, ok)
	require.Equal(t, filepath.Join(root, "docs", "FOREBRAIN.md"), full)
	full, ok = projectRulePath(root, "")
	require.True(t, ok)
	require.Equal(t, filepath.Join(root, "FOREBRAIN.md"), full)

	// .git is the repository's machinery and a symlinked directory may lead
	// anywhere: neither is a layer of the project's chain.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "linked")))
	for _, dir := range []string{".git", "linked", "."} {
		_, ok := projectRulePath(root, dir)
		require.False(t, ok, "dir=%s should be rejected", dir)
	}
}

// The project listing is the chain as it stands — the root plus every layer
// holding a FOREBRAIN.md — and the create list is every subdirectory layer
// without one.
func TestProjectRuleFilesListsTheChainAndCreatableLayers(t *testing.T) {
	s, _, _ := rulesServer(t)
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	s.Projects = state.NewProjectStore(db, "main")
	root := t.TempDir()
	for _, dir := range []string{"api", "docs", "web"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "docs", "FOREBRAIN.md"), []byte("docs rules"), 0o644))
	p, err := s.Projects.Create(context.Background(), state.CreateProjectInput{Name: "p", Root: root})
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	s.handleProjectRuleFiles(rr, withNamedParam(httptest.NewRequest(http.MethodGet, "/api/rules/project/"+p.ID, nil), "projectId", p.ID))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var listing struct {
		Files []struct {
			Dir    string `json:"dir"`
			Exists bool   `json:"exists"`
		} `json:"files"`
		Create []string `json:"create"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &listing))
	require.Len(t, listing.Files, 2)
	require.Equal(t, "", listing.Files[0].Dir)
	require.False(t, listing.Files[0].Exists)
	require.Equal(t, "docs", listing.Files[1].Dir)
	require.True(t, listing.Files[1].Exists)
	require.Equal(t, []string{"api", "web"}, listing.Create)

	rr = httptest.NewRecorder()
	req := withNamedParam(httptest.NewRequest(http.MethodGet, "/api/rules/project/"+p.ID+"/file?dir=docs", nil), "projectId", p.ID)
	s.handleProjectRuleFile(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.JSONEq(t, `{"dir":"docs","exists":true,"content":"docs rules"}`, rr.Body.String())
}

func TestApprovalDefaultGetPut(t *testing.T) {
	s, _, _ := rulesServer(t)
	// The Default preset is on-request + workspace-write; a config carrying
	// exactly that pair matches it, anything else (including a blank config)
	// reads as custom.
	s.Env.Deps.AppCfg.ApprovalPolicy = appcfg.NewApprovalPolicy(appcfg.ApprovalPolicyOnRequest)
	s.Env.Deps.AppCfg.SandboxMode = appcfg.SandboxModeWorkspaceWrite

	rr := httptest.NewRecorder()
	s.handleApprovalDefaultGet(rr, httptest.NewRequest(http.MethodGet, "/api/permissions/approval-default", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"current":"auto"`)

	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/permissions/approval-default", strings.NewReader(`{"preset":"full-access"}`))
	s.handleApprovalDefaultPut(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"current":"full-access"`)

	persisted, err := s.persistedConfig()
	require.NoError(t, err)
	require.Equal(t, appcfg.ApprovalPolicyNever, persisted.ApprovalPolicy.Mode)
	require.Equal(t, appcfg.SandboxModeDangerFullAccess, persisted.SandboxMode)

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/permissions/approval-default", strings.NewReader(`{"preset":"yolo"}`))
	s.handleApprovalDefaultPut(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestSessionPresetValidates(t *testing.T) {
	s, _, _ := rulesServer(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/permissions/session-preset", strings.NewReader(`{"preset":"read-only"}`))
	s.handleSessionPreset(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "session_id required")

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/permissions/session-preset", strings.NewReader(`{"session_id":"s1","preset":"nope"}`))
	s.handleSessionPreset(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// The session read needs a session too.
	rr = httptest.NewRecorder()
	s.handleSessionPresetGet(rr, httptest.NewRequest(http.MethodGet, "/api/permissions/session-preset", nil))
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// Without a mounted runner the endpoint reports unavailability rather
	// than pretending to have applied anything.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/permissions/session-preset", strings.NewReader(`{"session_id":"s1","preset":"read-only"}`))
	s.handleSessionPreset(rr, req)
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

// A preset picked for one conversation is that conversation's alone: the
// live config every other conversation, channel and scheduled job of the
// gateway runs under keeps its sandbox and approval policy.
func TestSessionPresetBelongsToItsConversation(t *testing.T) {
	home := t.TempDir()
	cfg := &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite, ApprovalPolicy: appcfg.NewApprovalPolicy(appcfg.ApprovalPolicyOnRequest)}
	runner := &run.Runner{Deps: &run.Deps{Home: home, AppCfg: cfg}}
	s := &Server{Home: home, Runner: runner, Env: &process.Environment{Deps: run.Deps{AppCfg: cfg}, Root: home, Sandbox: safety.NewManager(), Runner: runner}}

	rr := httptest.NewRecorder()
	s.handleSessionPreset(rr, httptest.NewRequest(http.MethodPost, "/api/permissions/session-preset", strings.NewReader(`{"session_id":"s1","preset":"full-access"}`)))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	require.Equal(t, appcfg.SandboxModeWorkspaceWrite, cfg.SandboxMode)
	require.Equal(t, appcfg.ApprovalPolicyOnRequest, cfg.ApprovalPolicy.Mode)

	current := func(sessionID string) string {
		rr := httptest.NewRecorder()
		s.handleSessionPresetGet(rr, httptest.NewRequest(http.MethodGet, "/api/permissions/session-preset?session_id="+sessionID, nil))
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		return rr.Body.String()
	}
	require.Contains(t, current("s1"), `"current":"full-access"`)
	require.Contains(t, current("s2"), `"current":"auto"`)

	owning := runner.EvaluatePermissionForSession("s1", "shell", "rm -rf /tmp/build-output")
	require.Equal(t, "danger_full_access", owning.Reason)
	other := runner.EvaluatePermissionForSession("s2", "shell", "rm -rf /tmp/build-output")
	require.NotEqual(t, "danger_full_access", other.Reason)
	require.False(t, other.BypassSandbox)
}

// Permission writes scoped to one conversation check its ownership first: a
// preset or a rule aimed at another primary agent's session is answered as
// missing and never reaches the permission store, while this agent's own
// sessions keep the behavior the endpoints exist for.
func TestSessionScopedPermissionWritesRefuseAnotherAgentsSession(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, state.NewSessionStore(db, "other-agent").Ensure(ctx, "their-session", "their-session"))
	cfg := &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite, ApprovalPolicy: appcfg.NewApprovalPolicy(appcfg.ApprovalPolicyOnRequest)}
	runner := &run.Runner{Deps: &run.Deps{Home: home, AppCfg: cfg}}
	s := &Server{
		Home:     home,
		Runner:   runner,
		Sessions: state.NewSessionStore(db, "main"),
		Env:      &process.Environment{Deps: run.Deps{AppCfg: cfg}, Root: home, Sandbox: safety.NewManager(), Runner: runner},
	}
	require.NoError(t, s.Sessions.Ensure(ctx, "my-session", "my-session"))
	require.NoError(t, s.Sessions.Ensure(ctx, "rule-session", "rule-session"))

	rr := httptest.NewRecorder()
	s.handleSessionPreset(rr, httptest.NewRequest(http.MethodPost, "/api/permissions/session-preset", strings.NewReader(`{"session_id":"their-session","preset":"full-access"}`)))
	require.Equal(t, http.StatusNotFound, rr.Code)
	foreign := runner.EvaluatePermissionForSession("their-session", "shell", "rm -rf /tmp/build-output")
	require.NotEqual(t, "danger_full_access", foreign.Reason, "another agent's session gained a preset")
	require.False(t, foreign.BypassSandbox)

	update := safety.PermissionUpdate{
		Type:        safety.UpdateAddRules,
		Destination: safety.DestinationSession,
		SessionID:   "their-session",
		Behavior:    safety.BehaviorAllow,
		Rules:       []safety.PermissionRuleValue{{ToolName: "Bash", RuleContent: "git status"}},
	}
	raw, err := json.Marshal(update)
	require.NoError(t, err)
	rr = httptest.NewRecorder()
	s.handlePermissionUpdate(rr, httptest.NewRequest(http.MethodPost, "/api/permissions/updates", bytes.NewReader(raw)))
	require.Equal(t, http.StatusNotFound, rr.Code)
	if d := runner.EvaluatePermissionForSession("their-session", "Bash", "git status"); d.Matched != nil {
		t.Fatalf("a rule reached another agent's session: %+v", d)
	}

	rr = httptest.NewRecorder()
	s.handleSessionPreset(rr, httptest.NewRequest(http.MethodPost, "/api/permissions/session-preset", strings.NewReader(`{"session_id":"my-session","preset":"full-access"}`)))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, "danger_full_access", runner.EvaluatePermissionForSession("my-session", "shell", "rm -rf /tmp/build-output").Reason)

	update.SessionID = "rule-session"
	raw, err = json.Marshal(update)
	require.NoError(t, err)
	rr = httptest.NewRecorder()
	s.handlePermissionUpdate(rr, httptest.NewRequest(http.MethodPost, "/api/permissions/updates", bytes.NewReader(raw)))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	if d := runner.EvaluatePermissionForSession("rule-session", "Bash", "git status"); d.Behavior != safety.BehaviorAllow {
		t.Fatalf("own session did not take the rule: %+v", d)
	}
}

func withParamName(r *http.Request, value string) *http.Request {
	return withNamedParam(r, "name", value)
}

// providersDTOServer builds a server whose active agent owns an empty
// provider table, with a config file to persist into.
func providersDTOServer(t *testing.T) (*Server, string) {
	t.Helper()
	s, home, _ := rulesServer(t)
	require.NoError(t, os.WriteFile(filepath.Join(home, "workspace", "skills"), nil, 0o644|os.ModeDir))
	return s, home
}

func providersGet(t *testing.T, s *Server) []map[string]any {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handleProviders(rr, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var out struct {
		Providers []map[string]any `json:"providers"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	return out.Providers
}

func providersPut(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/providers", strings.NewReader(body))
	s.handleProviders(rr, req)
	return rr
}

func TestProvidersDTORoundTripStoresKeyAsEnvReference(t *testing.T) {
	s, home := providersDTOServer(t)

	created := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","models":["deepseek-v4","deepseek-v4-flash"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-live-plain-9876"}
	]}`)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())

	// The yaml keeps only the reference; the plaintext lives in .env with
	// owner-only permissions.
	cfgRaw, err := os.ReadFile(filepath.Join(home, "forebrain.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(cfgRaw), "${DEEPSEEK_API_KEY}")
	require.NotContains(t, string(cfgRaw), "sk-live-plain-9876")
	envRaw, err := os.ReadFile(filepath.Join(home, ".env"))
	require.NoError(t, err)
	require.Contains(t, string(envRaw), "DEEPSEEK_API_KEY=")
	require.Contains(t, string(envRaw), "sk-live-plain-9876")
	info, err := os.Stat(filepath.Join(home, ".env"))
	require.NoError(t, err)
	require.Zero(t, info.Mode().Perm()&0o077, ".env must be 0600: %v", info.Mode())

	// The GET answers set+hint and no key material of any kind.
	rows := providersGet(t, s)
	require.Len(t, rows, 1)
	require.Equal(t, "deepseek", rows[0]["provider"])
	require.Equal(t, true, rows[0]["api_key_set"])
	require.Equal(t, "9876", rows[0]["api_key_hint"])
	models := rows[0]["models"].([]any)
	require.Equal(t, []any{"deepseek-v4", "deepseek-v4-flash"}, models)
	raw, _ := json.Marshal(rows)
	require.NotContains(t, string(raw), "sk-live-plain-9876")
	require.NotContains(t, string(raw), "[REDACTED]")
}

// params keys are the provider's own request fields: the GET hands them back
// as JSON text so no client-side key normalization can rename them, and the
// PUT refuses anything but an object.
func TestProvidersDTOParamsRoundTripKeepsProviderFieldNames(t *testing.T) {
	s, _ := providersDTOServer(t)
	rr := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","base_url":"https://api.deepseek.com","models":["deepseek-v4"],"api_key_plain":"sk-params-1111","params":{"max_tokens":512,"reasoning_effort":"high"}}
	]}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	rows := providersGet(t, s)
	require.Len(t, rows, 1)
	text, ok := rows[0]["params"].(string)
	require.True(t, ok, "params must travel as JSON text, got %T", rows[0]["params"])
	require.JSONEq(t, `{"max_tokens":512,"reasoning_effort":"high"}`, text)

	rr = providersPut(t, s, `{"providers":[{"provider":"deepseek","models":["deepseek-v4"],"params":["not","an","object"]}]}`)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

// The first row is the agent's primary model, which startup requires to be
// complete; a table that would leave it incomplete is refused instead of
// saved into a config the gateway could not start with again.
func TestProvidersDTORefusesAnIncompletePrimary(t *testing.T) {
	s, home := providersDTOServer(t)
	rr := providersPut(t, s, `{"providers":[{"provider":"moonshotai","models":[],"api_key_plain":"sk-e2e-7788"}]}`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "model")
	require.Contains(t, rr.Body.String(), "base_url")
	cfgRaw, err := os.ReadFile(filepath.Join(home, "forebrain.yaml"))
	require.NoError(t, err)
	require.NotContains(t, string(cfgRaw), "moonshotai")

	// An incomplete row is fine as a fallback behind a complete primary.
	rr = providersPut(t, s, `{"providers":[
		{"provider":"deepseek","base_url":"https://api.deepseek.com","models":["deepseek-v4"],"api_key_plain":"sk-primary-1234"},
		{"provider":"moonshotai","models":[],"api_key_plain":"sk-e2e-7788"}
	]}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
}

// A provider the engine's model catalog does not know has no client: the
// row is refused at save time, the way setup refuses it, rather than written
// into a config whose next runner load (a switch, a restart) would fail.
func TestProvidersDTORefusesAProviderTheEngineCannotBuild(t *testing.T) {
	s, home := providersDTOServer(t)
	rr := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","base_url":"https://api.deepseek.com","models":["deepseek-v4"],"api_key_plain":"sk-primary-1234"},
		{"provider":"made-up-svc","base_url":"https://llm.example.test/v1","models":["m"],"api_key_plain":"sk-x-9999"}
	]}`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "unsupported provider: made-up-svc")
	cfgRaw, err := os.ReadFile(filepath.Join(home, "forebrain.yaml"))
	require.NoError(t, err)
	require.NotContains(t, string(cfgRaw), "made-up-svc")
}

// The config file holds one entry per model (the engine's expanded form);
// adjacent same-signature entries fold back into one editable row.
func TestProvidersDTOFoldsAdjacentSameSignatureEntries(t *testing.T) {
	s, home := providersDTOServer(t)
	cfgPath := filepath.Join(home, "forebrain.yaml")
	seed := `agents:
  definitions:
    main:
      primary: true
      llm_providers:
      - provider: deepseek
        model: deepseek-v4
        api_key: ${DEEPSEEK_API_KEY}
      - provider: deepseek
        model: deepseek-v4-flash
        api_key: ${DEEPSEEK_API_KEY}
      - provider: openai
        model: gpt-test
`
	require.NoError(t, os.WriteFile(cfgPath, []byte(seed), 0o600))

	rows := providersGet(t, s)
	require.Len(t, rows, 2)
	require.Equal(t, "deepseek", rows[0]["provider"])
	require.Equal(t, []any{"deepseek-v4", "deepseek-v4-flash"}, rows[0]["models"].([]any))
	require.Equal(t, true, rows[0]["api_key_set"])
	require.Equal(t, "openai", rows[1]["provider"])

	// An interleaved order is a fallback order, not one service: it stays
	// three rows.
	interleaved := `agents:
  definitions:
    main:
      primary: true
      llm_providers:
      - provider: deepseek
        model: deepseek-v4
      - provider: openai
        model: gpt-test
      - provider: deepseek
        model: deepseek-v4-flash
`
	require.NoError(t, os.WriteFile(cfgPath, []byte(interleaved), 0o600))
	require.Len(t, providersGet(t, s), 3)
}

func TestProvidersDTOPutWithoutKeyKeepsStoredKey(t *testing.T) {
	s, home := providersDTOServer(t)
	created := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","base_url":"https://api.deepseek.com","models":["deepseek-v4"],"api_key_plain":"sk-live-keep-4321"}
	]}`)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())

	envBefore, err := os.ReadFile(filepath.Join(home, ".env"))
	require.NoError(t, err)

	// A PUT that touches only the model list carries no key fields: the
	// stored key and the .env entry are exactly what they were.
	updated := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","base_url":"https://api.deepseek.com","models":["deepseek-v4","deepseek-r2"]}
	]}`)
	require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
	rows := providersGet(t, s)
	require.Len(t, rows, 1)
	require.Equal(t, true, rows[0]["api_key_set"])
	require.Equal(t, "4321", rows[0]["api_key_hint"])
	require.Equal(t, []any{"deepseek-v4", "deepseek-r2"}, rows[0]["models"].([]any))

	envAfter, err := os.ReadFile(filepath.Join(home, ".env"))
	require.NoError(t, err)
	require.Equal(t, string(envBefore), string(envAfter))
}

func TestProvidersDTOKeyRotationUpdatesEnv(t *testing.T) {
	s, home := providersDTOServer(t)
	require.Equal(t, http.StatusOK, providersPut(t, s,
		`{"providers":[{"provider":"deepseek","base_url":"https://api.deepseek.com","models":["m"],"api_key_plain":"sk-old-aaaa"}]}`).Code)

	rotated := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","base_url":"https://api.deepseek.com","models":["m"],"api_key_plain":"sk-new-bbbb"}
	]}`)
	require.Equal(t, http.StatusOK, rotated.Code, rotated.Body.String())
	envRaw, err := os.ReadFile(filepath.Join(home, ".env"))
	require.NoError(t, err)
	require.Contains(t, string(envRaw), "sk-new-bbbb")
	require.NotContains(t, string(envRaw), "sk-old-aaaa")
	rows := providersGet(t, s)
	require.Equal(t, "bbbb", rows[0]["api_key_hint"])
}

// A short key hides entirely: the hint must not be the whole key.
func TestProvidersDTOShortKeyHintIsFullyMasked(t *testing.T) {
	s, _ := providersDTOServer(t)
	require.Equal(t, http.StatusOK, providersPut(t, s,
		`{"providers":[{"provider":"deepseek","base_url":"https://api.deepseek.com","models":["m"],"api_key_plain":"ab"}]}`).Code)
	rows := providersGet(t, s)
	require.Equal(t, true, rows[0]["api_key_set"])
	require.Equal(t, "••••", rows[0]["api_key_hint"])
}

// An explicit api_key value (a pasted ${ENV} reference) passes through as-is.
func TestProvidersDTOExplicitReferencePassesThrough(t *testing.T) {
	s, home := providersDTOServer(t)
	require.Equal(t, http.StatusOK, providersPut(t, s,
		`{"providers":[{"provider":"openai","base_url":"https://llm.example.test/v1","models":["m"],"api_key":"${MY_CUSTOM_KEY}"}]}`).Code)
	cfgRaw, err := os.ReadFile(filepath.Join(home, "forebrain.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(cfgRaw), "${MY_CUSTOM_KEY}")
	require.NoFileExists(t, filepath.Join(home, ".env"))
}

// The expanded per-model entries the DTO writes are what the engine itself
// expands — no yaml model list, so nothing is lost on the next save.
func TestProvidersDTOPersistedShapeIsEngineExpanded(t *testing.T) {
	s, home := providersDTOServer(t)
	require.Equal(t, http.StatusOK, providersPut(t, s, `{"providers":[
		{"provider":"deepseek","base_url":"https://api.deepseek.com","models":["a","b"],"api_key_plain":"sk-x-1234"}
	]}`).Code)
	cfgRaw, err := os.ReadFile(filepath.Join(home, "forebrain.yaml"))
	require.NoError(t, err)
	body := string(cfgRaw)
	require.Contains(t, body, "model: a")
	require.Contains(t, body, "model: b")
	require.NotContains(t, body, "- a")
}

// workspaceTreeServer builds a Server whose active workspace is one temp dir.
func workspaceTreeServer(t *testing.T) (*Server, string) {
	t.Helper()
	home := t.TempDir()
	ws := filepath.Join(home, "workspace")
	require.NoError(t, os.MkdirAll(ws, 0o755))
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main": {Primary: true},
	}
	require.NoError(t, os.MkdirAll(filepath.Join(home, "state"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, "state", "primary-agent.json"), []byte(`{"active":"main"}`+"\n"), 0o600))
	return &Server{Home: home, Env: &process.Environment{Deps: run.Deps{AppCfg: cfg}}}, ws
}

func getWorkspaceTree(t *testing.T, s *Server, query string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/workspace/tree"+query, nil)
	s.handleWorkspaceTree(rr, req)
	return rr
}

func TestWorkspaceTreeListsOneLevelDirectoriesFirst(t *testing.T) {
	s, ws := workspaceTreeServer(t)
	for _, name := range []string{"b.txt", "A.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(ws, name), []byte("x"), 0o644))
	}
	for _, name := range []string{"zdir", "adir", ".git"} {
		require.NoError(t, os.MkdirAll(filepath.Join(ws, name), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(ws, "adir", "nested.txt"), []byte("x"), 0o644))

	rr := getWorkspaceTree(t, s, "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	body := rr.Body.String()
	// Directories first, case-insensitive name order, .git hidden, one level.
	require.Contains(t, body, `"name":"adir"`)
	require.Contains(t, body, `"name":"zdir"`)
	require.Contains(t, body, `"name":"A.txt"`)
	require.Contains(t, body, `"name":"b.txt"`)
	require.NotContains(t, body, ".git")
	require.NotContains(t, body, "nested.txt")
	adirAt := indexOf(t, body, `"name":"adir"`)
	zdirAt := indexOf(t, body, `"name":"zdir"`)
	aTxtAt := indexOf(t, body, `"name":"A.txt"`)
	bTxtAt := indexOf(t, body, `"name":"b.txt"`)
	require.Less(t, adirAt, zdirAt)
	require.Less(t, zdirAt, aTxtAt)
	require.Less(t, aTxtAt, bTxtAt)
}

func TestWorkspaceTreeRejectsPathsOutsideWorkspace(t *testing.T) {
	s, _ := workspaceTreeServer(t)
	rr := getWorkspaceTree(t, s, "?path=../")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "path is outside the workspace")
}

func TestWorkspaceTreeHidesSymlinksLeavingWorkspace(t *testing.T) {

	s, ws := workspaceTreeServer(t)
	require.NoError(t, os.WriteFile(filepath.Join(ws, "real.txt"), []byte("x"), 0o644))
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "sub"), 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(ws, "out")))
	// A link whose target is inside the workspace stays listed, following the
	// target's type.
	require.NoError(t, os.Symlink(filepath.Join(ws, "real.txt"), filepath.Join(ws, "in")))

	rr := getWorkspaceTree(t, s, "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.NotContains(t, rr.Body.String(), `"name":"out"`)
	require.Contains(t, rr.Body.String(), `"name":"in"`)

	rr = getWorkspaceTree(t, s, "?path=out")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	s2, ws2 := workspaceTreeServer(t)
	require.NoError(t, os.MkdirAll(filepath.Join(ws2, "target"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(ws2, "target"), filepath.Join(ws2, "in")))
	rr = getWorkspaceTree(t, s2, "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"name":"in"`)
	require.Contains(t, rr.Body.String(), `"is_dir":true`)
}

func TestWorkspaceTreeMissingAndFilePaths(t *testing.T) {
	s, ws := workspaceTreeServer(t)
	require.NoError(t, os.WriteFile(filepath.Join(ws, "A.txt"), []byte("x"), 0o644))

	rr := getWorkspaceTree(t, s, "?path=missing")
	require.Equal(t, http.StatusNotFound, rr.Code)

	rr = getWorkspaceTree(t, s, "?path=A.txt")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "not a directory")
}

func TestWorkspaceTreeEmptyDirectory(t *testing.T) {
	s, ws := workspaceTreeServer(t)
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "empty"), 0o755))
	rr := getWorkspaceTree(t, s, "?path=empty")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"records":[]`)
}

func TestWorkspaceSnippetRejectsSymlinkEscape(t *testing.T) {

	s, ws := workspaceTreeServer(t)
	secret := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(secret, "secret.txt"), []byte("secret"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(secret, "secret.txt"), filepath.Join(ws, "leak.txt")))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "..notes.md"), []byte("dots are a legal name"), 0o644))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/workspace/snippet?path=leak.txt", nil)
	s.handleWorkspaceSnippet(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "path is outside the workspace")
	require.NotContains(t, rr.Body.String(), "secret")

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/workspace/snippet?path=..notes.md", nil)
	s.handleWorkspaceSnippet(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "dots are a legal name")
}

func indexOf(t *testing.T, haystack, needle string) int {
	t.Helper()
	idx := strings.Index(haystack, needle)
	require.GreaterOrEqual(t, idx, 0, "expected %q in %q", needle, haystack)
	return idx
}

func cronPreviewRequest(t *testing.T, s *Server, schedule string) map[string]any {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/cron/preview", strings.NewReader(`{"schedule":`+quoteJSON(schedule)+`}`))
	s.handleCronPreview(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	return out
}

func quoteJSON(v string) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func TestCronPreviewEveryInterval(t *testing.T) {
	s := cronTestServer(t)
	out := cronPreviewRequest(t, s, "every 30m")
	require.Equal(t, true, out["valid"])
	require.Equal(t, "every", out["kind"])
	require.Equal(t, "every 30m", out["raw"])
	next, ok := out["next"].([]any)
	require.True(t, ok)
	require.Len(t, next, 3)
	t1, err1 := time.Parse(time.RFC3339, next[0].(string))
	t2, err2 := time.Parse(time.RFC3339, next[1].(string))
	t3, err3 := time.Parse(time.RFC3339, next[2].(string))
	require.NoError(t, err1)
	require.NoError(t, err2)
	require.NoError(t, err3)
	require.True(t, t2.Sub(t1) == 30*time.Minute && t3.Sub(t2) == 30*time.Minute, "interval times must step by the schedule: %v", next)
	require.True(t, t1.After(time.Now().Add(29*time.Minute)), "first fire must be ahead: %v", next)
}

func TestCronPreviewOnceRelativeHasOneFire(t *testing.T) {
	s := cronTestServer(t)
	out := cronPreviewRequest(t, s, "in 30m")
	require.Equal(t, true, out["valid"])
	require.Equal(t, "once", out["kind"])
	next := out["next"].([]any)
	require.Len(t, next, 1)
	at, err := time.Parse(time.RFC3339, next[0].(string))
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(30*time.Minute), at, 2*time.Minute)
}

func TestCronPreviewCalendarKeepsRawAndKindCron(t *testing.T) {
	s := cronTestServer(t)
	out := cronPreviewRequest(t, s, "daily at 7am")
	require.Equal(t, true, out["valid"])
	// The plain-language forms compile to a cron evaluation; the stored text
	// stays exactly what the user wrote.
	require.Equal(t, "cron", out["kind"])
	require.Equal(t, "daily at 7am", out["raw"])
	next := out["next"].([]any)
	require.Len(t, next, 3)
	at, err := time.Parse(time.RFC3339, next[0].(string))
	require.NoError(t, err)
	require.Equal(t, 7, at.Hour())
	require.Equal(t, 0, at.Minute())
}

func TestCronPreviewCronExpressionAndMultiDayCron(t *testing.T) {
	s := cronTestServer(t)
	out := cronPreviewRequest(t, s, "0 9 * * 1,3,5")
	require.Equal(t, true, out["valid"])
	require.Equal(t, "cron", out["kind"])
	next := out["next"].([]any)
	require.Len(t, next, 3)
	// Monday, Wednesday, Friday only.
	for _, raw := range next {
		at, err := time.Parse(time.RFC3339, raw.(string))
		require.NoError(t, err)
		weekday := at.Weekday()
		require.Contains(t, []time.Weekday{time.Monday, time.Wednesday, time.Friday}, weekday, "fire day %v", at)
	}
}

func TestCronPreviewInvalidScheduleAnswersEngineMessage(t *testing.T) {
	s := cronTestServer(t)
	out := cronPreviewRequest(t, s, "whenever")
	require.Equal(t, false, out["valid"])
	require.NotEmpty(t, out["error"], "the engine's own message is what the builder shows")

	// Below the engine's one-minute floor the preview says so too.
	out = cronPreviewRequest(t, s, "every 30s")
	require.Equal(t, false, out["valid"])
	require.Contains(t, out["error"], "minimum")

	// An empty schedule is an invalid preview, not a transport error.
	out = cronPreviewRequest(t, s, "  ")
	require.Equal(t, false, out["valid"])
}

// The agent page lists agent-wide jobs only; a project tab lists its own. The
// two listings must never mix.
func TestCronJobsListSplitsByProject(t *testing.T) {
	s := cronTestServer(t)
	db := s.RunRT.DB
	s.Projects = state.NewProjectStore(db, "main")
	project, err := s.Projects.Create(context.Background(), state.CreateProjectInput{Name: "cron-e2e", Root: t.TempDir()})
	require.NoError(t, err)

	created := cronRequest(t, s, http.MethodPost, "/api/cron", map[string]any{
		"name": "agent-wide", "schedule": "every 2h", "prompt": "agent work",
	}, s.handleCronJobs)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())

	projectJob := cronRequest(t, s, http.MethodPost, "/api/cron", map[string]any{
		"name": "bound", "schedule": "every 3h", "prompt": "project work", "project_id": project.ID,
	}, s.handleCronJobs)
	require.Equal(t, http.StatusOK, projectJob.Code, projectJob.Body.String())

	decode := func(rr *httptest.ResponseRecorder) []state.CronJob {
		var list struct {
			Records []state.CronJob `json:"records"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
		return list.Records
	}

	agentRows := decode(cronRequest(t, s, http.MethodGet, "/api/cron", nil, s.handleCronJobs))
	require.Len(t, agentRows, 1)
	require.Equal(t, "agent-wide", agentRows[0].Name)
	require.Empty(t, agentRows[0].ProjectID)

	projectRows := decode(cronRequest(t, s, http.MethodGet, "/api/cron?project_id="+project.ID, nil, s.handleCronJobs))
	require.Len(t, projectRows, 1)
	require.Equal(t, "bound", projectRows[0].Name)
	require.Equal(t, project.ID, projectRows[0].ProjectID)

	// Someone else's project is not addressable from this agent.
	other, err := state.NewProjectStore(db, "someone-else").Create(context.Background(), state.CreateProjectInput{Name: "theirs", Root: t.TempDir()})
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, cronRequest(t, s, http.MethodGet, "/api/cron?project_id="+other.ID, nil, s.handleCronJobs).Code)
	require.Equal(t, http.StatusNotFound, cronRequest(t, s, http.MethodGet, "/api/cron?project_id=missing", nil, s.handleCronJobs).Code)
}

// A project space's permission tab manages that project's own rules: the
// write lands in the route project's .forebrain/safety.json — never in the
// project the gateway itself was launched in — and is refused for a project
// whose rules the engine would not honor.
func TestProjectPermissionRulesBelongToTheRouteProject(t *testing.T) {
	s, home, _ := rulesServer(t)
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	s.Projects = state.NewProjectStore(db, "main")

	launchRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(launchRoot, ".git"), 0o755))
	require.NoError(t, safety.MarkTrusted(home, safety.Project{Root: launchRoot}))
	s.Env.LaunchProject = safety.ProjectContext{Project: safety.Project{Root: launchRoot, VersionControlled: true}, TrustLevel: safety.LevelTrusted}

	projectRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".git"), 0o755))
	p, err := s.Projects.Create(context.Background(), state.CreateProjectInput{Name: "p", Root: projectRoot})
	require.NoError(t, err)

	update := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		body := `{"type":"addRules","behavior":"deny","rules":[{"tool_name":"Bash","rule_content":"rm -rf:*"}]}`
		req := withID(httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/permissions/updates", strings.NewReader(body)), p.ID)
		s.handleProjectPermissionUpdate(rr, req)
		return rr
	}
	list := func() map[string]any {
		rr := httptest.NewRecorder()
		s.handleProjectPermissionRules(rr, withID(httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+p.ID+"/permissions/rules", nil), p.ID))
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
		return out
	}

	// Untrusted: the engine would drop the rule, so the write is refused.
	require.Equal(t, false, list()["applies"])
	rr := update()
	require.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
	require.NoFileExists(t, filepath.Join(projectRoot, ".forebrain", "safety.json"))

	require.NoError(t, safety.MarkTrusted(home, safety.Project{Root: projectRoot}))
	rr = update()
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	raw, err := os.ReadFile(filepath.Join(projectRoot, ".forebrain", "safety.json"))
	require.NoError(t, err)
	require.Contains(t, string(raw), "rm -rf")
	require.NoFileExists(t, filepath.Join(launchRoot, ".forebrain", "safety.json"))

	listing := list()
	require.Equal(t, true, listing["applies"])
	require.Contains(t, fmt.Sprint(listing["rules"]), "rm -rf")
}

// The session's cost is what its requests spent, in the figures /status
// reports — not a placeholder saying token cost is unavailable.
func TestSessionCostSummaryReportsWhatTheSessionSpent(t *testing.T) {
	ctx := context.Background()
	s := cronTestServer(t)
	s.Sessions = state.NewSessionStore(s.Env.SQL, "main")
	seedRun(t, s.RunRT, "s1", "r1")
	require.NoError(t, s.RunRT.SetRunUsage(ctx, "r1", state.LastRunUsage{PromptTokens: 200, CompletionTokens: 50, CacheReadTokens: 600, CacheWriteTokens: 200, LLMCalls: 2}))

	rec := cronRequest(t, s, http.MethodGet, "/api/chat/sessions/s1/cost-summary", nil, s.handleSessionCostSummary, "id", "s1")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, float64(1000), got["input_tokens"])
	require.Equal(t, float64(50), got["output_tokens"])
	require.Equal(t, float64(2), got["requests"])
	require.Equal(t, float64(60), got["cache_hit_percent"])
	require.NotContains(t, rec.Body.String(), "note")
}

// lspControlDouble records the actions the /v1/lsp endpoints run and answers
// a fixed snapshot.
type lspControlDouble struct {
	mu        sync.Mutex
	snapshot  event.LSPSnapshot
	enabled   []string
	restarts  []string
	installs  []string
	decisions []string
	decideErr error
	recResets int
	err       error
}

func (c *lspControlDouble) Snapshot() event.LSPSnapshot { return c.snapshot }
func (c *lspControlDouble) Subscribe(func(event.LSPSnapshot)) func() {
	return func() {}
}
func (c *lspControlDouble) SetEnabled(serverID string, enabled bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enabled = append(c.enabled, fmt.Sprintf("%v:%s", enabled, serverID))
	return c.err
}
func (c *lspControlDouble) Restart(serverID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.restarts = append(c.restarts, serverID)
	return c.err
}
func (c *lspControlDouble) Install(ctx context.Context, serverID string, progress func(string)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.installs = append(c.installs, serverID)
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.err
}
func (c *lspControlDouble) SetRecommendationListener(func(context.Context, event.LSPRecommendation)) {
}
func (c *lspControlDouble) DecideRecommendation(recommendationID string, choice event.LSPRecommendationChoice) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.decisions = append(c.decisions, fmt.Sprintf("%s:%s", recommendationID, choice))
	if c.decideErr != nil {
		return c.decideErr
	}
	return nil
}
func (c *lspControlDouble) ResetRecommendations() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recResets++
	return c.err
}

// GET /api/v1/lsp answers the same JSON the panel renders, and a gateway
// without a control plane answers 503 rather than an empty snapshot.
func TestLSPSnapshotEndpoint(t *testing.T) {
	ctl := &lspControlDouble{snapshot: event.LSPSnapshot{
		ProjectRoot: "/proj", Trusted: true, FeatureEnabled: true,
		Servers: []event.LSPServerStatus{{ID: "gopls", Enabled: true, State: event.LSPStateReady}},
	}}
	s := &Server{Runner: &run.Runner{Deps: &run.Deps{CodeIntelControl: ctl}}}
	rec := httptest.NewRecorder()
	s.handleLSPSnapshot(rec, httptest.NewRequest(http.MethodGet, "/api/v1/lsp", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var snap event.LSPSnapshot
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &snap))
	require.Equal(t, "/proj", snap.ProjectRoot)
	require.Len(t, snap.Servers, 1)
	require.Equal(t, "gopls", snap.Servers[0].ID)

	bare := &Server{Runner: &run.Runner{Deps: &run.Deps{}}}
	rec = httptest.NewRecorder()
	bare.handleLSPSnapshot(rec, httptest.NewRequest(http.MethodGet, "/api/v1/lsp", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "language servers are not available")
}

// The /v1/lsp server actions share the terminal panel's control plane:
// enable/disable/restart answer 200, install answers 202 and really starts,
// a missing id answers 400, a control-plane refusal answers 409, and no
// control plane answers 503.
func TestLSPServerActions(t *testing.T) {
	ctl := &lspControlDouble{}
	s := &Server{Runner: &run.Runner{Deps: &run.Deps{CodeIntelControl: ctl}}}
	post := func(handler func(http.ResponseWriter, *http.Request), body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodPost, "/api/v1/lsp/servers/x", strings.NewReader(body)))
		return rec
	}

	rec := post(s.handleLSPServerEnable, `{"id":"gopls"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = post(s.handleLSPServerDisable, `{"id":"gopls"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = post(s.handleLSPServerRestart, `{"id":"gopls"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"true:gopls", "false:gopls"}, ctl.enabled)
	require.Equal(t, []string{"gopls"}, ctl.restarts)

	rec = post(s.handleLSPServerInstall, `{"id":"pyright"}`)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"started":true`)
	require.Eventually(t, func() bool {
		ctl.mu.Lock()
		defer ctl.mu.Unlock()
		return len(ctl.installs) == 1 && ctl.installs[0] == "pyright"
	}, time.Second, 5*time.Millisecond)

	rec = post(s.handleLSPServerEnable, `{"id":""}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "id required")

	rec = post(s.handleLSPServerEnable, `not json`)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	refusing := &lspControlDouble{err: errors.New("unknown language server \"nope\"")}
	refuser := &Server{Runner: &run.Runner{Deps: &run.Deps{CodeIntelControl: refusing}}}
	rec = post(refuser.handleLSPServerEnable, `{"id":"nope"}`)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "unknown language server")

	bare := &Server{Runner: &run.Runner{Deps: &run.Deps{}}}
	rec = post(bare.handleLSPServerRestart, `{"id":"gopls"}`)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// POST /v1/lsp/recommendations/reset runs the control plane's reset and
// reports its refusal with its own text.
func TestLSPRecommendationsResetEndpoint(t *testing.T) {
	ctl := &lspControlDouble{}
	s := &Server{Runner: &run.Runner{Deps: &run.Deps{CodeIntelControl: ctl}}}
	rec := httptest.NewRecorder()
	s.handleLSPRecommendationsReset(rec, httptest.NewRequest(http.MethodPost, "/api/v1/lsp/recommendations/reset", strings.NewReader(`{"session_id":"s1"}`)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, ctl.recResets)

	notYet := &lspControlDouble{err: errors.New("language servers are not available in this build yet")}
	unbuilt := &Server{Runner: &run.Runner{Deps: &run.Deps{CodeIntelControl: notYet}}}
	rec = httptest.NewRecorder()
	unbuilt.handleLSPRecommendationsReset(rec, httptest.NewRequest(http.MethodPost, "/api/v1/lsp/recommendations/reset", strings.NewReader(`{}`)))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "not available in this build yet")
}

// POST /v1/lsp/recommendations/:id/decision applies the web card's answer:
// 200 with the choice on record, 400 for a choice the runtime does not
// know, 404 for a recommendation that was never made or already answered,
// and 503 without a control plane.
func TestLSPRecommendationDecisionEndpoint(t *testing.T) {
	ctl := &lspControlDouble{}
	s := &Server{Runner: &run.Runner{Deps: &run.Deps{CodeIntelControl: ctl}}}
	post := func(id, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/lsp/recommendations/"+id+"/decision", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: id}}))
		s.handleLSPRecommendationDecision(rec, req)
		return rec
	}

	rec := post("lsprec-1", `{"choice":"enable","session_id":"s1"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{"lsprec-1:enable"}, ctl.decisions)

	rec = post("lsprec-1", `{"choice":"bogus"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid choice")

	rec = post("lsprec-1", `not json`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid choice")

	unknown := &lspControlDouble{decideErr: tool.ErrUnknownLSPRecommendation}
	refuser := &Server{Runner: &run.Runner{Deps: &run.Deps{CodeIntelControl: unknown}}}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/lsp/recommendations/lsprec-2/decision", strings.NewReader(`{"choice":"not_now"}`))
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: "lsprec-2"}}))
	refuser.handleLSPRecommendationDecision(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)

	refusal := &lspControlDouble{decideErr: errors.New("unknown language server \"nope\"")}
	conflicted := &Server{Runner: &run.Runner{Deps: &run.Deps{CodeIntelControl: refusal}}}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/lsp/recommendations/lsprec-3/decision", strings.NewReader(`{"choice":"enable"}`))
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: "lsprec-3"}}))
	conflicted.handleLSPRecommendationDecision(rec, req)
	require.Equal(t, http.StatusConflict, rec.Code)

	bare := &Server{Runner: &run.Runner{Deps: &run.Deps{}}}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/lsp/recommendations/lsprec-4/decision", strings.NewReader(`{"choice":"enable"}`))
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: "lsprec-4"}}))
	bare.handleLSPRecommendationDecision(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestProjectLSPPreviewAndConsentEndpoint(t *testing.T) {
	s, _, home := newProjectsTestServer(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := safety.MarkTrusted(home, safety.Project{Root: root}); err != nil {
		t.Fatal(err)
	}
	srvPath := filepath.Join(root, ".forebrain", "lsp_servers.yaml")
	if err := os.MkdirAll(filepath.Dir(srvPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "servers:\n  projectls: {command: /bin/projectls, extension_to_language: {\".pl\": projectls}}\n"
	if err := os.WriteFile(srvPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, http.MethodPost, "/api/v1/projects", map[string]any{"name": "lspproj", "root": root, "trust": true})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := created["id"].(string)

	// An unknown project answers 404.
	rec = doJSON(t, s, http.MethodGet, "/api/v1/projects/not-a-project/lsp", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project: %d %s", rec.Code, rec.Body.String())
	}

	// Preview: the entry awaits confirmation.
	rec = doJSON(t, s, http.MethodGet, "/api/v1/projects/"+id+"/lsp", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	var preview struct {
		Trusted bool                `json:"trusted"`
		Pending []map[string]string `json:"pending"`
		Allowed []string            `json:"allowed"`
		Denied  []string            `json:"denied"`
		Notes   []string            `json:"notes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if !preview.Trusted {
		t.Fatal("a trusted, version-controlled project must preview as trusted")
	}
	if len(preview.Pending) != 1 || preview.Pending[0]["id"] != "projectls" {
		t.Fatalf("pending: %+v", preview.Pending)
	}
	if want := "projectls: runs `/bin/projectls`"; preview.Pending[0]["summary"] != want {
		t.Fatalf("summary = %q, want %q", preview.Pending[0]["summary"], want)
	}
	if len(preview.Allowed) != 0 || len(preview.Denied) != 0 {
		t.Fatalf("allowed=%v denied=%v", preview.Allowed, preview.Denied)
	}

	// Confirm through the endpoint.
	rec = doJSON(t, s, http.MethodPost, "/api/v1/projects/"+id+"/lsp/consent", map[string]any{"allow": []string{"projectls"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("consent: %d %s", rec.Code, rec.Body.String())
	}
	var decided struct {
		OK      bool     `json:"ok"`
		Allowed []string `json:"allowed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &decided); err != nil {
		t.Fatal(err)
	}
	if !decided.OK || len(decided.Allowed) != 1 || decided.Allowed[0] != "projectls" {
		t.Fatalf("consent reply: %+v", decided)
	}

	// Now the preview lists it as allowed and nothing pending.
	rec = doJSON(t, s, http.MethodGet, "/api/v1/projects/"+id+"/lsp", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview after consent: %d %s", rec.Code, rec.Body.String())
	}
	preview = struct {
		Trusted bool                `json:"trusted"`
		Pending []map[string]string `json:"pending"`
		Allowed []string            `json:"allowed"`
		Denied  []string            `json:"denied"`
		Notes   []string            `json:"notes"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Allowed) != 1 || preview.Allowed[0] != "projectls" {
		t.Fatalf("allowed after consent: %v", preview.Allowed)
	}
	if len(preview.Pending) != 0 {
		t.Fatalf("nothing should be pending: %+v", preview.Pending)
	}
}

// The cron settings endpoint is the structured surface of
// cron.retention_days: it reads the effective value with its bounds, writes
// through the same validation every other write path uses, and a null write
// returns the key to unset.
func TestCronSettingsRoundTrip(t *testing.T) {
	s := cronTestServer(t)
	path := filepath.Join(s.Home, "forebrain.yaml")
	s.Env.ConfigPath = path

	type cronSettings struct {
		RetentionDays int  `json:"retention_days"`
		Configured    bool `json:"configured"`
		DefaultDays   int  `json:"default_days"`
		MinDays       int  `json:"min_days"`
		MaxDays       int  `json:"max_days"`
	}
	var got cronSettings

	rec := cronRequest(t, s, http.MethodGet, "/api/cron-settings", nil, s.handleCronSettings)
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.RetentionDays != 30 || got.Configured || got.DefaultDays != 30 || got.MinDays != 1 || got.MaxDays != 3650 {
		t.Fatalf("unset settings = %+v, want the 30-day default reported as not configured", got)
	}

	rec = cronRequest(t, s, http.MethodPut, "/api/cron-settings", map[string]any{"retention_days": 7}, s.handleCronSettings)
	if rec.Code != http.StatusOK {
		t.Fatalf("put 7 = %d %s", rec.Code, rec.Body.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "cron:") || !strings.Contains(string(raw), "retention_days: 7") {
		t.Fatalf("saved config lacks the cron section:\n%s", raw)
	}
	if eff := s.liveCfg().CronRetentionDays(); eff != 7 {
		t.Fatalf("live retention after the write = %d, want 7", eff)
	}
	rec = cronRequest(t, s, http.MethodGet, "/api/cron-settings", nil, s.handleCronSettings)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.RetentionDays != 7 || !got.Configured {
		t.Fatalf("settings after the write = %+v, want 7 configured", got)
	}

	rec = cronRequest(t, s, http.MethodPut, "/api/cron-settings", map[string]any{"retention_days": 0}, s.handleCronSettings)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("put 0 = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if raw, _ = os.ReadFile(path); !strings.Contains(string(raw), "retention_days: 7") {
		t.Fatalf("a rejected write changed the file:\n%s", raw)
	}

	rec = cronRequest(t, s, http.MethodPut, "/api/cron-settings", map[string]any{"retention_days": nil}, s.handleCronSettings)
	if rec.Code != http.StatusOK {
		t.Fatalf("put null = %d %s", rec.Code, rec.Body.String())
	}
	if raw, _ = os.ReadFile(path); strings.Contains(string(raw), "retention_days") {
		t.Fatalf("null did not unset the key:\n%s", raw)
	}
	rec = cronRequest(t, s, http.MethodGet, "/api/cron-settings", nil, s.handleCronSettings)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.RetentionDays != 30 || got.Configured {
		t.Fatalf("settings after the reset = %+v, want the 30-day default, not configured", got)
	}
}

// The structured write carries one integer, so its body is bounded like every
// other small write: a payload past the limit is refused before it is parsed
// and never reaches the config file.
func TestCronSettingsPutBoundsRequestBody(t *testing.T) {
	s := cronTestServer(t)
	path := filepath.Join(s.Home, "forebrain.yaml")
	s.Env.ConfigPath = path

	rec := cronRequest(t, s, http.MethodPut, "/api/cron-settings", strings.Repeat("x", 8192), s.handleCronSettings)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized body = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "too large") {
		t.Fatalf("refusal should name the body limit: %s", rec.Body.String())
	}
	if raw, _ := os.ReadFile(path); strings.Contains(string(raw), "retention_days") {
		t.Fatalf("a refused write changed the file:\n%s", raw)
	}

	rec = cronRequest(t, s, http.MethodPut, "/api/cron-settings", map[string]any{"retention_days": 7}, s.handleCronSettings)
	if rec.Code != http.StatusOK {
		t.Fatalf("small body = %d %s", rec.Code, rec.Body.String())
	}
	if raw, err := os.ReadFile(path); err != nil || !strings.Contains(string(raw), "retention_days: 7") {
		t.Fatalf("the small write did not land:\n%s (%v)", raw, err)
	}
}

// The YAML editor and the structured endpoint share one validation: what the
// editor submits is parsed exactly the way the loader reads a file, so a
// retention the loader would refuse never reaches disk from there either.
func TestCronSettingsSharedValidationWithTheYAMLEditor(t *testing.T) {
	s := cronTestServer(t)
	path := filepath.Join(s.Home, "forebrain.yaml")
	s.Env.ConfigPath = path

	rec := cronRequest(t, s, http.MethodPut, "/api/config", map[string]any{
		"yaml": "cron:\n  retention_days: 0\n",
	}, s.handleConfigFile)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("yaml editor with retention 0 = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		raw, _ := os.ReadFile(path)
		t.Fatalf("a rejected editor write reached the file:\n%s", raw)
	}
}

// TestChatMessagesCarriesSubagentCallFacts pins the one transport change the
// web's subagent cards need: a tool row that answered a subagent_* call
// carries the call's card facts, derived by the same engine rule the live
// path used, so a reloaded card says what the live one said.
func TestChatMessagesCarriesSubagentCallFacts(t *testing.T) {
	ctx := context.Background()
	s := cronTestServer(t)
	s.Sessions = state.NewSessionStore(s.RunRT.DB, "main")
	if err := s.Sessions.Ensure(ctx, "s1", "s1"); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	statusArgs, err := json.Marshal(map[string]any{"agent_id": "subagent-6e5c"})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	statusResult, err := json.Marshal(map[string]any{
		"agent_id": "subagent-6e5c", "agent_kind": "typed", "agent_type": "general-purpose",
		"task_id": "task-16", "status": "running", "started_at": 1_700_000_000,
		"execution_id": "exec-1", "title": "计划001 Go车道实施", "run_id": "exec-1",
	})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	callRow := llm.AssistantMessage(nil, llm.ToolCall{
		ID:   "call-status-1",
		Type: llm.ToolTypeFunction,
		Function: llm.FunctionCall{
			Name:      "subagent_status",
			Arguments: string(statusArgs),
		},
	})
	if _, err := s.Sessions.AppendStructuredMessage(ctx, "s1", "assistant", "", "", state.MessagePartsJSON(callRow, ""), "", "", "", "", state.MessageExecTiming{}); err != nil {
		t.Fatalf("append call row: %v", err)
	}
	answer := llm.ToolResultMessage("call-status-1", llm.Text(string(statusResult)))
	answer.ToolDisplay = &llm.ToolDisplayState{Body: "card body", Summary: "check 计划001 Go车道实施"}
	if _, err := s.Sessions.AppendStructuredMessage(ctx, "s1", "tool", string(statusResult), "", state.MessagePartsJSON(answer, string(statusResult)), "", "", "call-status-1", "", state.MessageExecTiming{}); err != nil {
		t.Fatalf("append answer row: %v", err)
	}

	rec := cronRequest(t, s, http.MethodGet, "/api/chat/sessions/s1/messages", nil, s.handleChatMessages, "id", "s1")
	if rec.Code != http.StatusOK {
		t.Fatalf("messages = %d %s", rec.Code, rec.Body.String())
	}
	var rows []struct {
		Role         string              `json:"role"`
		ToolStepID   string              `json:"tool_step_id"`
		SubagentCall *event.SubagentCall `json:"subagent_call"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	var toolRow *struct {
		Role         string              `json:"role"`
		ToolStepID   string              `json:"tool_step_id"`
		SubagentCall *event.SubagentCall `json:"subagent_call"`
	}
	for i := range rows {
		if rows[i].Role == "tool" {
			toolRow = &rows[i]
		}
	}
	if toolRow == nil {
		t.Fatalf("no tool row in %s", rec.Body.String())
	}
	if toolRow.ToolStepID != "call-status-1" {
		t.Fatalf("tool row step id = %q", toolRow.ToolStepID)
	}

	// The facts on the row are the engine's own derivation over the stored
	// transcript — exactly what turn.SubagentCallsInTranscript says.
	stored, err := s.Sessions.ListAllMessages(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	want := turn.SubagentCallsInTranscript(stored)["call-status-1"]
	if want.Verb != "status" || len(want.Tasks) != 1 {
		t.Fatalf("engine facts = %+v", want)
	}
	if toolRow.SubagentCall == nil {
		t.Fatalf("tool row carries no subagent_call: %s", rec.Body.String())
	}
	if !reflect.DeepEqual(*toolRow.SubagentCall, want) {
		t.Fatalf("row facts = %+v, want the engine's %+v", *toolRow.SubagentCall, want)
	}
	// A call the transcript never answered keeps its row free of facts.
	for _, row := range rows {
		if row.Role == "assistant" && row.SubagentCall != nil {
			t.Fatalf("assistant row carries facts: %+v", row.SubagentCall)
		}
	}
}

// exitPlanServer is one gateway with a session parked on an exit-plan
// approval: the run waits, the action is pending, and the plan file the
// approval is asking about exists under the server's own state root.
func exitPlanServer(t *testing.T, cfg *appcfg.Root) (*Server, *state.RunStore, *state.ActionService, string, string) {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.Open(ctx, filepath.Join(home, "state.sqlite"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	mustGatewaySession(t, db, "s1")
	runs := &state.RunStore{DB: db}
	actions := &state.ActionService{DB: db}
	parkedRun, err := runs.CreateRun(ctx, "s1", "plan the work")
	require.NoError(t, err)
	action, err := actions.CreatePending(ctx, "s1", "exit_plan_mode", map[string]any{"session_id": "s1"})
	require.NoError(t, err)
	require.NoError(t, runs.SetWaitingAction(ctx, parkedRun.ID, state.Wait{
		RunID: parkedRun.ID, ActionID: action.ID, ToolName: "exit_plan_mode", ToolInputJSON: "{}",
	}))
	s := &Server{
		Home: home, RunRT: runs, Actions: actions,
		Sessions: state.NewSessionStore(db, "main"),
		Runner:   &run.Runner{Deps: &run.Deps{ProjectKey: "proj", AppCfg: cfg}},
	}
	require.NoError(t, state.SetPlanForSession(s.stateRoot(), s.projectKey(), "s1", "# Plan\n\n1. Ship it."))
	return s, runs, actions, action.ID, "s1"
}

// The approval-request endpoint is the web's copy of the TUI's approval
// overlay: for a parked exit-plan gate it carries the plan itself, the models
// a review may be handed to, and nothing when no gate is open.
func TestSessionApprovalRequestCarriesTheExitPlanCard(t *testing.T) {
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "gpt-5.1", APIKey: "sk-test"}}},
	}}}
	s, runs, _, actionID, _ := exitPlanServer(t, cfg)
	// One completed review on the conversation: the card shows it above the
	// choices, in the wire's own units and field names.
	reviewPayload, err := json.Marshal(event.PlanReviewedPayload{
		ActionID: actionID, ReviewID: "plan-review:e2e",
		Provider: "openai", Model: "gpt-5.1",
		Text: "Verdict: rework.", DurationMs: 125000, Outcome: "done",
	})
	require.NoError(t, err)
	_, err = runs.AppendSessionEvent(context.Background(), state.SessionEvent{
		ID: "plan-review:e2e:reviewed", SessionID: "s1",
		Type: event.RunEventPlanReviewed, Payload: reviewPayload,
	})
	require.NoError(t, err)

	rec := cronRequest(t, s, http.MethodGet, "/api/chat/sessions/s1/approval-request", nil, s.handleSessionApprovalRequest, "id", "s1")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		ActionID         string `json:"action_id"`
		Kind             string `json:"kind"`
		PlanText         string `json:"plan_text"`
		PlanReviewModels []struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
			Current  bool   `json:"current"`
		} `json:"plan_review_models"`
		PlanReviews []struct {
			Provider   string `json:"provider"`
			Model      string `json:"model"`
			Text       string `json:"text"`
			DurationMs int64  `json:"duration_ms"`
		} `json:"plan_reviews"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, "exit_plan_mode", out.Kind)
	require.NotEmpty(t, out.ActionID)
	require.Contains(t, out.PlanText, "1. Ship it.")
	require.Len(t, out.PlanReviewModels, 1)
	require.Equal(t, "gpt-5.1", out.PlanReviewModels[0].Model)
	require.True(t, out.PlanReviewModels[0].Current, "the model in force is the one marked current")
	require.Len(t, out.PlanReviews, 1)
	require.Equal(t, "gpt-5.1", out.PlanReviews[0].Model)
	require.Contains(t, out.PlanReviews[0].Text, "rework")
	require.Equal(t, int64(125000), out.PlanReviews[0].DurationMs, "durations cross the wire in milliseconds")

	// No gate, no card: the endpoint reports the absence rather than an error.
	require.NoError(t, state.NewSessionStore(s.RunRT.DB, "main").Ensure(context.Background(), "s-idle", "s-idle"))
	idleRec := cronRequest(t, s, http.MethodGet, "/api/chat/sessions/s-idle/approval-request", nil, s.handleSessionApprovalRequest, "id", "s-idle")
	require.Equal(t, http.StatusNoContent, idleRec.Code)
}

// The plan-review endpoint validates before it starts anything: the action
// must be a parked exit-plan approval, and the model must be one the session
// is configured with. A valid request is accepted for background work and
// leaves the review's own dispatch record on the conversation.
func TestActionPlanReviewValidatesBeforeStarting(t *testing.T) {
	ctx := context.Background()
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "gpt-5.1", APIKey: "sk-test"}}},
	}}}
	s, runs, actions, actionID, _ := exitPlanServer(t, cfg)

	shellAction, err := actions.CreatePending(ctx, "s1", "shell", map[string]any{"session_id": "s1"})
	require.NoError(t, err)
	rec := cronRequest(t, s, http.MethodPost, "/api/actions/"+shellAction.ID+"/plan-review",
		map[string]string{"provider": "openai", "model": "gpt-5.1"}, s.handleActionPlanReview, "id", shellAction.ID)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	rec = cronRequest(t, s, http.MethodPost, "/api/actions/"+actionID+"/plan-review",
		map[string]string{"provider": "openai", "model": "gpt-4o"}, s.handleActionPlanReview, "id", actionID)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	rec = cronRequest(t, s, http.MethodPost, "/api/actions/"+actionID+"/plan-review",
		map[string]string{"provider": "openai", "model": "gpt-5.1"}, s.handleActionPlanReview, "id", actionID)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	// The review ran to its own ending (this server has no subagent executor,
	// so it fails) and recorded that ending on the conversation, the way every
	// review outcome travels.
	var reviewed []state.SessionEvent
	require.Eventually(t, func() bool {
		reviewed, err = runs.ListSessionEventsOfType(ctx, "s1", event.RunEventPlanReviewed, 0)
		return err == nil && len(reviewed) == 1
	}, 5*time.Second, 50*time.Millisecond, "the review must close itself on the conversation")
	require.Contains(t, string(reviewed[0].Payload), actionID)
}

// fakeGatewaySubagentExecutor stands in for the reviewer's execution: plan
// review runs as a subagent, so a stub on the runner is the seam that makes a
// review complete with text instead of failing to open.
type fakeGatewaySubagentExecutor struct{ reply string }

func (f *fakeGatewaySubagentExecutor) RunSubagentExec(context.Context, run.SubagentExecRequest) (string, error) {
	return f.reply, nil
}
func (f *fakeGatewaySubagentExecutor) PersistSubagentTurn(context.Context, run.SubagentTurn) {}

func (f *fakeGatewaySubagentExecutor) SubagentExecutionStarting(context.Context, string) {}

func (f *fakeGatewaySubagentExecutor) SubagentExecutionEnded(context.Context, run.SubagentExecutionEnd) {
}

// A finished review delivers itself on the web's path too: the approval
// closes as the marker denial, the resolution event lands on the
// conversation, and the resume composition the gateway submits — the same
// call resumeGatewayRun makes — carries the review to the planning model
// with no user words claimed. The provider points at a closed port so the
// resumed turn cannot leave the machine.
func TestActionPlanReviewDeliversOnDone(t *testing.T) {
	ctx := context.Background()
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{{
			Provider: "openai", Model: "gpt-5.1", APIKey: "sk-test", BaseURL: "http://127.0.0.1:1",
		}}},
	}}}
	s, runs, actions, actionID, _ := exitPlanServer(t, cfg)
	s.Runner.SubagentExecutor = &fakeGatewaySubagentExecutor{reply: "Verdict: rework the cache story."}
	// Retire the parked run's wait row so the resume the delivery dispatches
	// exits on its first lookup instead of writing (mode switch, run status)
	// into a temp dir the test is about to remove. Everything asserted here —
	// the denial, the event, the resume composition — commits before that
	// goroutine is spawned.
	fenceRunID, _, findErr := runs.FindRunByAction(ctx, actionID)
	require.NoError(t, findErr)
	require.NotEmpty(t, fenceRunID)
	require.NoError(t, runs.ClearWait(ctx, fenceRunID))

	rec := cronRequest(t, s, http.MethodPost, "/api/actions/"+actionID+"/plan-review",
		map[string]string{"provider": "openai", "model": "gpt-5.1"}, s.handleActionPlanReview, "id", actionID)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	require.Eventually(t, func() bool {
		act, err := actions.Get(ctx, actionID)
		return err == nil && act != nil &&
			act.Status == state.ActionDenied && act.Error == turn.PlanReviewDeliveredReason
	}, 5*time.Second, 50*time.Millisecond, "the review must deliver itself to the planner")

	var resolved []state.SessionEvent
	var err error
	require.Eventually(t, func() bool {
		resolved, err = runs.ListSessionEventsOfType(ctx, "s1", event.RunEventApprovalResolved, 0)
		return err == nil && len(resolved) == 1
	}, 5*time.Second, 50*time.Millisecond, "the delivery must record its resolution")
	var payload event.ApprovalResolvedPayload
	require.NoError(t, json.Unmarshal(resolved[0].Payload, &payload))
	require.Equal(t, actionID, payload.ActionID)
	require.Equal(t, string(state.ActionDenied), payload.Decision)
	require.Equal(t, turn.PlanReviewDeliveredReason, payload.Reason)

	act, err := actions.Get(ctx, actionID)
	require.NoError(t, err)
	resume := turn.BuildApprovalResumeWithPlanReviews(ctx, runs, act, nil, "exit_plan_mode", false)
	require.True(t, resume.Denied)
	require.Contains(t, resume.Reason, "The plan review you asked for has returned.")
	require.Contains(t, resume.Reason, "Verdict: rework the cache story.")
	require.NotContains(t, resume.Reason, turn.PlanReviewDeliveredReason)
	require.Empty(t, resume.Feedback, "delivery claims no user words")
}

// Approving the plan a review is still running against stops the review: its
// result has no decision left to inform. The cancel is best-effort — a review
// that finishes first delivers against a closed gate and stays a card.
func TestGatewayApproveCancelsAnInFlightPlanReview(t *testing.T) {
	ctx := context.Background()
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{{
			Provider: "openai", Model: "gpt-5.1", APIKey: "sk-test", BaseURL: "http://127.0.0.1:1",
		}}},
	}}}
	s, runs, actions, actionID, _ := exitPlanServer(t, cfg)

	// One in-flight review, derived the way every surface derives it: a
	// plan_review_started no plan_reviewed has closed, and the subagent it
	// spawned pointing back at it.
	reviewID := "plan-review:rev-live"
	startedPayload, err := json.Marshal(event.PlanReviewStartedPayload{
		ActionID: actionID, ReviewID: reviewID, Provider: "openai", Model: "gpt-5.1",
	})
	require.NoError(t, err)
	_, err = runs.AppendSessionEvent(ctx, state.SessionEvent{
		ID: reviewID + ":started", SessionID: "s1",
		Type: event.RunEventPlanReviewStarted, Payload: startedPayload,
	})
	require.NoError(t, err)
	spawnedPayload, err := json.Marshal(event.SubagentSpawnedPayload{
		AgentID: "agent-review", ParentToolCallID: reviewID,
	})
	require.NoError(t, err)
	_, err = runs.AppendSessionEvent(ctx, state.SessionEvent{
		ID: "spawn-agent-review", SessionID: "s1",
		Type: event.RunEventSubagentSpawned, Payload: spawnedPayload,
	})
	require.NoError(t, err)
	cancelled := make(chan struct{})
	h := agent.RegistryFor(s.stateRoot()).Start(agent.HistoryEntry{
		AgentID: "agent-review", TaskID: "agent-review", RunID: "run-review", SessionID: "s1",
		Status: agent.StatusRunning, StartedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	}, func() { close(cancelled) })
	t.Cleanup(func() {
		h.Finish(agent.HistoryEntry{
			AgentID: "agent-review", TaskID: "agent-review", RunID: "run-review", SessionID: "s1",
			Status: agent.StatusCancelled, UpdatedAt: time.Now().Unix(),
		})
	})
	// Retire the parked run's wait row so the resume the approve dispatches
	// exits on its first lookup: a goroutine writing the mode switch or run
	// status would race this test's temp dir cleanup.
	fenceRunID, _, findErr := runs.FindRunByAction(ctx, actionID)
	require.NoError(t, findErr)
	require.NotEmpty(t, fenceRunID)
	require.NoError(t, runs.ClearWait(ctx, fenceRunID))

	rec := cronRequest(t, s, http.MethodPost, "/api/actions/"+actionID+"/approve", nil, s.handleActionsApprove, "id", actionID)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("approving the plan must stop the review still running against it")
	}
	act, err := actions.Get(ctx, actionID)
	require.NoError(t, err)
	require.Equal(t, state.ActionApproved, act.Status, "the approval the user made keeps the last word")
}

// The retry of an approval whose decision already committed converges on the
// idempotent path; it must still stop a review running against the plan, the
// same way the first response did.
func TestGatewayIdempotentApproveStillCancelsAnInFlightPlanReview(t *testing.T) {
	ctx := context.Background()
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{{
			Provider: "openai", Model: "gpt-5.1", APIKey: "sk-test", BaseURL: "http://127.0.0.1:1",
		}}},
	}}}
	s, runs, actions, actionID, _ := exitPlanServer(t, cfg)

	// One in-flight review, derived the way every surface derives it: a
	// plan_review_started no plan_reviewed has closed, and the subagent it
	// spawned pointing back at it.
	reviewID := "plan-review:rev-retry"
	startedPayload, err := json.Marshal(event.PlanReviewStartedPayload{
		ActionID: actionID, ReviewID: reviewID, Provider: "openai", Model: "gpt-5.1",
	})
	require.NoError(t, err)
	_, err = runs.AppendSessionEvent(ctx, state.SessionEvent{
		ID: reviewID + ":started", SessionID: "s1",
		Type: event.RunEventPlanReviewStarted, Payload: startedPayload,
	})
	require.NoError(t, err)
	spawnedPayload, err := json.Marshal(event.SubagentSpawnedPayload{
		AgentID: "agent-review", ParentToolCallID: reviewID,
	})
	require.NoError(t, err)
	_, err = runs.AppendSessionEvent(ctx, state.SessionEvent{
		ID: "spawn-agent-review", SessionID: "s1",
		Type: event.RunEventSubagentSpawned, Payload: spawnedPayload,
	})
	require.NoError(t, err)
	cancelled := make(chan struct{})
	h := agent.RegistryFor(s.stateRoot()).Start(agent.HistoryEntry{
		AgentID: "agent-review", TaskID: "agent-review", RunID: "run-review", SessionID: "s1",
		Status: agent.StatusRunning, StartedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	}, func() { close(cancelled) })
	t.Cleanup(func() {
		h.Finish(agent.HistoryEntry{
			AgentID: "agent-review", TaskID: "agent-review", RunID: "run-review", SessionID: "s1",
			Status: agent.StatusCancelled, UpdatedAt: time.Now().Unix(),
		})
	})
	// The decision commits first; the HTTP call below is its exact retry.
	_, err = actions.Approve(ctx, actionID, "approved")
	require.NoError(t, err)
	// Retire the parked run's wait row so the retry's resume goroutine exits
	// on its first lookup and cannot race this test's temp dir cleanup.
	fenceRunID, _, findErr := runs.FindRunByAction(ctx, actionID)
	require.NoError(t, findErr)
	require.NotEmpty(t, fenceRunID)
	require.NoError(t, runs.ClearWait(ctx, fenceRunID))

	rec := cronRequest(t, s, http.MethodPost, "/api/actions/"+actionID+"/approve", nil, s.handleActionsApprove, "id", actionID)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("an approved retry must still stop the review running against it")
	}
	act, err := actions.Get(ctx, actionID)
	require.NoError(t, err)
	require.Equal(t, state.ActionApproved, act.Status, "the committed approval keeps the last word")
}

// newSubagentViewTestServer builds a server whose conversation "sid" exists
// and whose runner resolves a subagent ledger under the same workspace root,
// so the subagent-view routes can be exercised end to end.
func newSubagentViewTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(context.Background(), "sid", "sid"))
	cfg := &appcfg.Root{}
	cfg.Compact.ModelAutoCompactTokenLimit = 200_000
	runner := &run.Runner{Deps: &run.Deps{Home: home, AppCfg: cfg, AgentName: "main"}}
	s := &Server{Home: home, RunRT: &state.RunStore{DB: db}, Sessions: sessions, Runner: runner}
	return s, filepath.Join(home, "workspace")
}

// seedSubagentRecord writes one subagent record into the runner's ledger, the
// shape a dispatch leaves behind.
func seedSubagentRecord(t *testing.T, workspaceRoot, sid, agentKey string) {
	t.Helper()
	now := time.Now().Unix()
	require.NoError(t, agent.AppendHistory(workspaceRoot, agent.HistoryEntry{
		AgentID:         agentKey,
		TaskID:          agentKey,
		SessionID:       sid,
		RunID:           "run-1",
		WorkerSessionID: "worker-1",
		AgentKind:       "typed",
		AgentType:       "general-purpose",
		Title:           "network probe",
		Task:            "answer briefly",
		Status:          agent.StatusOK,
		StartedAt:       now,
		UpdatedAt:       now,
		FinishedAt:      now,
	}))
}

// subagentViewRequest builds a request carrying the router's id/agent params,
// the way the router's context does.
func subagentViewRequest(method, sid, agentKey, body string) *http.Request {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/api/chat/sessions/"+sid+"/subagents/"+agentKey, reader)
	ctx := context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: sid}, {Key: "agent", Value: agentKey}})
	return req.WithContext(ctx)
}

// subagentViewRoutes pairs each route's handler with a method and body, so one
// table drives the foreign/unknown-agent assertions.
func subagentViewRequests(sid, agentKey string) map[string]*http.Request {
	return map[string]*http.Request{
		"input":        subagentViewRequest(http.MethodPost, sid, agentKey, `{"message":"continue"}`),
		"queued-input": subagentViewRequest(http.MethodPost, sid, agentKey, `{"action":"edit_last"}`),
		"interrupt":    subagentViewRequest(http.MethodPost, sid, agentKey, ""),
		"withdraw":     subagentViewRequest(http.MethodPost, sid, agentKey, ""),
		"compact":      subagentViewRequest(http.MethodPost, sid, agentKey, ""),
		"context":      subagentViewRequest(http.MethodGet, sid, agentKey, ""),
		"budget":       subagentViewRequest(http.MethodGet, sid, agentKey, ""),
	}
}

func (s *Server) dispatchSubagentViewRoute(name string, w http.ResponseWriter, r *http.Request) {
	switch name {
	case "input":
		s.handleSubagentInput(w, r)
	case "queued-input":
		s.handleSubagentQueuedInput(w, r)
	case "interrupt":
		s.handleSubagentInterruptSend(w, r)
	case "withdraw":
		s.handleSubagentWithdraw(w, r)
	case "compact":
		s.handleSubagentCompact(w, r)
	case "context":
		s.handleSubagentContext(w, r)
	case "budget":
		s.handleSubagentBudget(w, r)
	}
}

func TestSubagentViewEndpointsRefuseAForeignConversation(t *testing.T) {
	s, _ := newSubagentViewTestServer(t)
	for name, req := range subagentViewRequests("other", "agent-1") {
		w := httptest.NewRecorder()
		s.dispatchSubagentViewRoute(name, w, req)
		require.Equal(t, http.StatusNotFound, w.Code, "%s: %s", name, w.Body.String())
	}
}

func TestSubagentViewEndpointsRefuseAnUnknownAgent(t *testing.T) {
	s, _ := newSubagentViewTestServer(t)
	for name, req := range subagentViewRequests("sid", "ghost") {
		w := httptest.NewRecorder()
		s.dispatchSubagentViewRoute(name, w, req)
		require.Equal(t, http.StatusNotFound, w.Code, "%s: %s", name, w.Body.String())
	}
}

func TestSubagentInputRefusesConversationCommands(t *testing.T) {
	s, ws := newSubagentViewTestServer(t)
	seedSubagentRecord(t, ws, "sid", "agent-1")

	for _, tc := range []struct {
		message string
		code    string
	}{
		{"/new", subagentViewCommandCode},
		{"/rename x", subagentViewCommandCode},
		{"/compact", useDedicatedEndpointCode},
		{"/context", useDedicatedEndpointCode},
	} {
		req := subagentViewRequest(http.MethodPost, "sid", "agent-1", `{"message":`+strconv.Quote(tc.message)+`}`)
		w := httptest.NewRecorder()
		s.handleSubagentInput(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code, tc.message)
		var out struct {
			Code string `json:"code"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		require.Equal(t, tc.code, out.Code, tc.message)
	}
}

func TestSubagentInputStartsAnIdleSubagent(t *testing.T) {
	s, ws := newSubagentViewTestServer(t)
	seedSubagentRecord(t, ws, "sid", "agent-1")

	req := subagentViewRequest(http.MethodPost, "sid", "agent-1", `{"message":"continue"}`)
	w := httptest.NewRecorder()
	s.handleSubagentInput(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var out struct {
		Delivery string `json:"delivery"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Equal(t, string(run.SubagentDeliveryStarted), out.Delivery)
}

func TestSubagentQueuedInputRecallWithAnEmptyQueue(t *testing.T) {
	s, ws := newSubagentViewTestServer(t)
	seedSubagentRecord(t, ws, "sid", "agent-1")

	req := subagentViewRequest(http.MethodPost, "sid", "agent-1", `{"action":"edit_last"}`)
	w := httptest.NewRecorder()
	s.handleSubagentQueuedInput(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var out struct {
		Accepted bool `json:"accepted"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.False(t, out.Accepted)
}

func TestSubagentBudgetIsItsOwnWindow(t *testing.T) {
	s, ws := newSubagentViewTestServer(t)
	seedSubagentRecord(t, ws, "sid", "agent-1")

	req := subagentViewRequest(http.MethodGet, "sid", "agent-1", "")
	w := httptest.NewRecorder()
	s.handleSubagentBudget(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var out event.TokenBudgetUpdatedPayload
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Equal(t, agent.RosterKey("agent-1", "general-purpose"), out.AgentID)
}

// blockingSubagentExecutor holds one execution open until its context is
// cancelled, so a test can observe a subagent mid-run.
type blockingSubagentExecutor struct{ entered chan struct{} }

func (b *blockingSubagentExecutor) RunSubagentExec(ctx context.Context, _ run.SubagentExecRequest) (string, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return "", ctx.Err()
}

func (b *blockingSubagentExecutor) PersistSubagentTurn(context.Context, run.SubagentTurn) {}
func (b *blockingSubagentExecutor) SubagentExecutionStarting(context.Context, string)     {}
func (b *blockingSubagentExecutor) SubagentExecutionEnded(context.Context, run.SubagentExecutionEnd) {
}

func TestSubagentCompactRefusesWhileRunning(t *testing.T) {
	s, ws := newSubagentViewTestServer(t)
	seedSubagentRecord(t, ws, "sid", "agent-1")
	blocking := &blockingSubagentExecutor{entered: make(chan struct{}, 1)}
	s.Runner.SubagentExecutor = blocking

	req := subagentViewRequest(http.MethodPost, "sid", "agent-1", `{"message":"go"}`)
	w := httptest.NewRecorder()
	s.handleSubagentInput(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.Eventually(t, func() bool {
		return run.SubagentRunning(s.Runner, "sid", "agent-1")
	}, 5*time.Second, 20*time.Millisecond, "the user-driven execution must be running")

	compactReq := subagentViewRequest(http.MethodPost, "sid", "agent-1", "")
	cw := httptest.NewRecorder()
	s.handleSubagentCompact(cw, compactReq)
	require.Equal(t, http.StatusConflict, cw.Code, cw.Body.String())
	var out struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(cw.Body.Bytes(), &out))
	require.Equal(t, subagentRunningCode, out.Code)

	// Let the execution finish so the goroutine does not outlive the test.
	run.InterruptSubagentToSend(s.Runner, "sid", "agent-1")
	<-time.After(50 * time.Millisecond)
}

// The delivered-review handoff is only as durable as the field copy that
// carries it into the parked run: the flag decides whether the persisted
// denial's display half says the handoff line or "(no output)". Both chain
// ends are tested elsewhere (turn builds the flag, run renders the body); this
// pins the gateway hop so a refactor that drops the copy fails here instead of
// silently degrading the web timeline.
func TestGatewayResumeStateCarriesDeliveredReview(t *testing.T) {
	resume := turn.ApprovalResume{
		Session:         []llm.Message{llm.UserMessage(llm.Text("run the plan by me first"))},
		Denied:          true,
		Reason:          "The user asked reviewer to review this plan before approving it.",
		Feedback:        "",
		DeliveredReview: true,
	}
	got := gatewayResumeState(resume)
	require.NotNil(t, got)
	require.True(t, got.DeliveredReview, "the delivered-review marker must reach the parked run")
	require.True(t, got.Denied)
	require.Equal(t, resume.Reason, got.DenyReason)
	require.Equal(t, resume.Feedback, got.DenyFeedback)
	require.Equal(t, resume.Session, got.Session)
	// The projection is pure: the continuation fence is the caller's to bind
	// once the resume state is known to be used.
	require.Nil(t, got.BeginContinuation)
	require.Nil(t, got.EndContinuation)
}

// The engine tells the web's subagent surface about every change to that
// subagent's queue; the surface republishes the preview, so the open view
// stops showing a message the model already has.
func TestSubagentSurfacePublishesQueueChanges(t *testing.T) {
	s := newRunInputTestServer(t)
	surface := s.subagentConversationSurface("sid")
	require.NotNil(t, surface.OnQueueChanged)
	surface.OnQueueChanged("agent-1", run.QueuePreview{Steers: []string{"still queued"}})

	events, err := s.RunRT.ListSessionEventsOfType(context.Background(), "sid", event.RunEventPendingInputUpdated, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	var payload event.PendingInputUpdatedPayload
	require.NoError(t, json.Unmarshal(events[0].Payload, &payload))
	require.Equal(t, "agent-1", payload.AgentID)
	require.Equal(t, []string{"still queued"}, payload.PendingSteers)
}

// plan-md is the web's window on a conversation's plan; it must resolve that
// one session's plan even when another conversation in the same project wrote
// a plan more recently.
func TestSessionPlanMarkdownReturnsOnlyThatSessionsPlan(t *testing.T) {
	s := &Server{Home: t.TempDir(), Runner: &run.Runner{Deps: &run.Deps{ProjectKey: "proj"}}}
	require.NoError(t, state.SetPlanForSession(s.stateRoot(), s.projectKey(), "s1", "# Mine"))

	// A second session's plan, written later: invisible to s1's plan-md.
	other := state.PlanPathForSession(s.stateRoot(), s.projectKey(), "s2")
	require.NoError(t, os.MkdirAll(filepath.Dir(other), 0o755))
	require.NoError(t, os.WriteFile(other, []byte("# Theirs\n"), 0o600))
	future := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(other, future, future))

	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/s1/plan-md", nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{
		{Key: "id", Value: "s1"},
	}))
	rr := httptest.NewRecorder()
	s.handleSessionPlanMarkdown(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var body struct {
		Markdown string `json:"markdown"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Equal(t, "# Mine", body.Markdown)
}
