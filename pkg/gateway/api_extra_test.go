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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/channel"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	llmopenai "github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
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
	// Scheduled work belongs to a project now, so the fixture has one.
	root := filepath.Join(home, "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if _, err := state.NewProjectStore(db, "main").Create(context.Background(), state.CreateProjectInput{Name: "p", Root: root}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return &Server{
		Home:  home,
		RunRT: &state.RunStore{DB: db},
		Env:   &process.Environment{Root: home, SQL: db, Deps: run.Deps{AppCfg: cfg}},
	}
}

// cronTestProjectID is the project the cron test server seeded, for routes
// that name one.
func cronTestProjectID(t *testing.T, s *Server) string {
	t.Helper()
	list, err := state.NewProjectStore(s.Env.SQL, "main").ListProjects(context.Background(), state.ListProjectsOptions{}, 10, 0)
	if err != nil || len(list) == 0 {
		t.Fatalf("project lookup: %v (%d)", err, len(list))
	}
	return list[0].ID
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
	if len(params) >= 2 && len(params)%2 == 0 {
		pairs := make(Params, 0, len(params)/2)
		for i := 0; i+1 < len(params); i += 2 {
			pairs = append(pairs, Param{Key: params[i], Value: params[i+1]})
		}
		req = req.WithContext(context.WithValue(req.Context(), ParamsKey, pairs))
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

// A job is created, listed, edited, fired-history-checked and removed through
// the API a surface actually calls. Jobs belong to the project the route names.
func TestCronJobsRoundTripThroughTheAPI(t *testing.T) {
	s := cronTestServer(t)
	pid := cronTestProjectID(t, s)

	created := cronRequest(t, s, http.MethodPost, "/api/projects/"+pid+"/cron/jobs", map[string]any{
		"name": "morning brief", "schedule": "daily at 7am", "prompt": "summarise the inbox", "deliver": "telegram",
	}, s.handleCronJobs, "id", pid)
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

	listed := cronRequest(t, s, http.MethodGet, "/api/projects/"+pid+"/cron/jobs", nil, s.handleCronJobs, "id", pid)
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

	paused := cronRequest(t, s, http.MethodPut, "/api/projects/"+pid+"/cron/jobs/"+job.ID, map[string]any{
		"schedule": "daily at 7am", "enabled": false,
	}, s.handleCronJob, "id", pid, "jobId", job.ID)
	if paused.Code != http.StatusOK {
		t.Fatalf("pause = %d %s", paused.Code, paused.Body.String())
	}
	var pausedJob state.CronJob
	_ = json.Unmarshal(paused.Body.Bytes(), &pausedJob)
	if pausedJob.NextRunAt != nil {
		t.Fatal("a paused job must have no next fire, or the scheduler would still pick it up")
	}

	resumed := cronRequest(t, s, http.MethodPut, "/api/projects/"+pid+"/cron/jobs/"+job.ID, map[string]any{"enabled": true}, s.handleCronJob, "id", pid, "jobId", job.ID)
	var resumedJob state.CronJob
	_ = json.Unmarshal(resumed.Body.Bytes(), &resumedJob)
	if resumedJob.NextRunAt == nil {
		t.Fatal("resuming a job must give it a next fire again")
	}

	removed := cronRequest(t, s, http.MethodDelete, "/api/projects/"+pid+"/cron/jobs/"+job.ID, nil, s.handleCronJob, "id", pid, "jobId", job.ID)
	if removed.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", removed.Code, removed.Body.String())
	}
	after := cronRequest(t, s, http.MethodGet, "/api/projects/"+pid+"/cron/jobs", nil, s.handleCronJobs, "id", pid)
	_ = json.Unmarshal(after.Body.Bytes(), &list)
	if len(list.Records) != 0 {
		t.Fatalf("job survived deletion: %#v", list.Records)
	}
}

// A schedule nobody can read is refused when the job is created, not silently
// stored as a job that never fires.
func TestCronJobCreationRefusesAnUnreadableSchedule(t *testing.T) {
	s := cronTestServer(t)
	pid := cronTestProjectID(t, s)
	rec := cronRequest(t, s, http.MethodPost, "/api/projects/"+pid+"/cron/jobs", map[string]any{
		"schedule": "whenever", "prompt": "do the thing",
	}, s.handleCronJobs, "id", pid)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create with a bad schedule = %d %s", rec.Code, rec.Body.String())
	}

	noPrompt := cronRequest(t, s, http.MethodPost, "/api/projects/"+pid+"/cron/jobs", map[string]any{"schedule": "every 1h"}, s.handleCronJobs, "id", pid)
	if noPrompt.Code != http.StatusBadRequest {
		t.Fatalf("create with no prompt = %d", noPrompt.Code)
	}
}

// A job belonging to another primary agent is not this tenant's to read.
func TestCronJobFromAnotherTenantIsNotFound(t *testing.T) {
	s := cronTestServer(t)
	pid := cronTestProjectID(t, s)
	store := &state.CronStore{DB: s.RunRT.DB}
	foreign := state.CronJob{ID: "job-foreign", Ag: "research", ProjectID: pid, Sched: "every 1h", Prompt: "x", Enabled: true}
	if err := store.InsertJob(context.Background(), foreign); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}
	rec := cronRequest(t, s, http.MethodGet, "/api/projects/"+pid+"/cron/jobs/job-foreign", nil, s.handleCronJob, "id", pid, "jobId", "job-foreign")
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

// A paged read of /api/hooks answers one page of the flattened table — one
// row per command, with the indices an edit or a delete needs to find it in
// the stored map — and the search reads the three columns the table shows.
// Without "page" the endpoint keeps the whole-map answer it always gave, which
// is what a write reads first.
func TestHooksEndpointPagesTheFlattenedTable(t *testing.T) {
	s := cronTestServer(t)
	s.Env.ConfigPath = filepath.Join(s.Home, "forebrain.yaml")
	yaml := "hooks:\n" +
		"  PostToolUse:\n" +
		"    - matcher: Bash\n" +
		"      hooks:\n" +
		"        - type: command\n" +
		"          command: echo first\n" +
		"    - hooks:\n" +
		"        - type: command\n" +
		"          command: echo second\n" +
		"  Stop:\n" +
		"    - hooks:\n" +
		"        - type: prompt\n" +
		"          prompt: summarise what happened\n"
	if err := os.WriteFile(s.Env.ConfigPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	type hookAnswer struct {
		Rows       []hookRow `json:"rows"`
		KnownTypes []string  `json:"known_types"`
		Events     []string  `json:"events"`
		Total      int       `json:"total"`
		Page       int       `json:"page"`
		PageSize   int       `json:"page_size"`
	}
	read := func(target string) hookAnswer {
		t.Helper()
		rec := cronRequest(t, s, http.MethodGet, target, nil, s.handleHooks)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var answer hookAnswer
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &answer))
		return answer
	}

	first := read("/api/hooks?page=1&page_size=2")
	require.Equal(t, 3, first.Total)
	require.Equal(t, 1, first.Page)
	require.Equal(t, 2, first.PageSize)
	require.Len(t, first.Rows, 2)
	// Events in name order, then each event's matchers and commands in the
	// order they are written; the two indices locate the row in the map.
	require.Equal(t, hookRow{Event: "PostToolUse", Matcher: "Bash", Command: "echo first", MatcherIdx: 0, CommandIdx: 0}, first.Rows[0])
	require.Equal(t, hookRow{Event: "PostToolUse", Matcher: "", Command: "echo second", MatcherIdx: 1, CommandIdx: 0}, first.Rows[1])
	// The offer lists answer beside the page, from the same lists the
	// validator enforces.
	require.NotEmpty(t, first.KnownTypes)
	require.NotEmpty(t, first.Events)

	second := read("/api/hooks?page=2&page_size=2")
	require.Equal(t, 3, second.Total)
	require.Equal(t, 2, second.Page)
	require.Len(t, second.Rows, 1)
	// A prompt hook's prompt is what its command column shows: it is what the
	// hook does.
	require.Equal(t, hookRow{Event: "Stop", Matcher: "", Command: "summarise what happened", MatcherIdx: 0, CommandIdx: 0}, second.Rows[0])

	// The search reads event, matcher and command alike.
	require.Equal(t, 2, read("/api/hooks?page=1&q=ECHO").Total)
	require.Equal(t, 1, read("/api/hooks?page=1&q=bash").Total)
	require.Equal(t, 1, read("/api/hooks?page=1&q=Stop").Total)

	// Without "page" the answer is the whole stored map, exactly as before.
	rec := cronRequest(t, s, http.MethodGet, "/api/hooks", nil, s.handleHooks)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var whole struct {
		Hooks      appcfg.HooksSettings `json:"hooks"`
		KnownTypes []string             `json:"known_types"`
		Events     []string             `json:"events"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &whole))
	require.Len(t, whole.Hooks, 2)
	require.Len(t, whole.Hooks["PostToolUse"], 2)
	require.NotEmpty(t, whole.Events)
	require.NotContains(t, rec.Body.String(), `"rows"`)
}

// GET /api/runtime names the state, the auth mode, the primary-agent count and
// the three directories the settings page's runtime tab lists; the version and
// the address are the browser's own and are not repeated here.
func TestRuntimeInfoReportsHomeAndDirs(t *testing.T) {
	s := cronTestServer(t)
	rec := cronRequest(t, s, http.MethodGet, "/api/runtime", nil, s.handleRuntimeInfo)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Status   string `json:"status"`
		Auth     string `json:"auth"`
		Agents   int    `json:"agents"`
		Home     string `json:"home"`
		LogsDir  string `json:"logs_dir"`
		StateDir string `json:"state_dir"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "ok", got.Status)
	// cronTestServer binds a configuration with the default auth mode.
	require.Equal(t, "token", got.Auth)
	require.Equal(t, 1, got.Agents)
	require.Equal(t, s.Home, got.Home)
	require.Equal(t, filepath.Join(s.Home, "logs"), got.LogsDir)
	require.Equal(t, filepath.Join(s.Home, "state"), got.StateDir)
	require.NotContains(t, rec.Body.String(), "version")
	require.NotContains(t, rec.Body.String(), "listen")

	// A gateway with no configuration bound has no auth mode to name, so the
	// key is left out rather than answered with a mode it is not running.
	bare := &Server{Home: t.TempDir()}
	rec = cronRequest(t, bare, http.MethodGet, "/api/runtime", nil, bare.handleRuntimeInfo)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), `"auth"`)

	rec = cronRequest(t, s, http.MethodPost, "/api/runtime", nil, s.handleRuntimeInfo)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
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
	// With trust: the project exists and the decision is recorded.
	trustedRoot := t.TempDir()
	rec = doJSON(t, s, http.MethodPost, "/api/v1/projects", map[string]any{"name": "trusted", "root": trustedRoot, "trust": true})
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
	trusted, err = safety.IsTrusted(home, safety.Project{Root: trustedRoot})
	if err != nil || !trusted {
		t.Fatalf("trust decision missing: %v %v", trusted, err)
	}
}

func TestProjectsCRUDPinDelete(t *testing.T) {
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

	// Pin keeps the project live and listed.
	rec = doJSON(t, s, http.MethodPost, "/api/v1/projects/"+id+"/pin", map[string]any{"pinned": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("pin: %d", rec.Code)
	}
	rec = doJSON(t, s, http.MethodGet, "/api/v1/projects", nil)
	list = struct {
		Projects []map[string]any `json:"projects"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Projects) != 1 {
		t.Fatalf("a pinned project must stay listed: %+v", list)
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

// TestProjectsListPagesWithTotalAndSessionCount pins the project table's
// answer: a page-bearing request is filtered, ordered and sliced on the
// server, counts every matching row, carries each project's conversation
// count, and a page past the end serves the last page.
func TestProjectsListPagesWithTotalAndSessionCount(t *testing.T) {
	s, store, _ := newProjectsTestServer(t)
	ctx := context.Background()
	ids := make([]string, 0, 3)
	for _, name := range []string{"alpha", "beta", "gamma"} {
		p, err := store.Create(ctx, state.CreateProjectInput{Name: name, Root: filepath.Join(t.TempDir(), name)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	// alpha holds two conversations; beta holds a session that never said
	// anything, which is not a conversation; gamma holds none.
	for _, sid := range []string{"conv-1", "conv-2"} {
		if _, err := s.Sessions.Append(ctx, sid, "user", "hello"); err != nil {
			t.Fatal(err)
		}
		if err := store.BindSession(ctx, sid, ids[0]); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Sessions.Ensure(ctx, "conv-empty", "empty"); err != nil {
		t.Fatal(err)
	}
	if err := store.BindSession(ctx, "conv-empty", ids[1]); err != nil {
		t.Fatal(err)
	}

	decode := func(raw []byte) (struct {
		Projects []map[string]any `json:"projects"`
		Total    int              `json:"total"`
		Page     int              `json:"page"`
		PageSize int              `json:"page_size"`
	}, map[string]map[string]any) {
		t.Helper()
		var answer struct {
			Projects []map[string]any `json:"projects"`
			Total    int              `json:"total"`
			Page     int              `json:"page"`
			PageSize int              `json:"page_size"`
		}
		if err := json.Unmarshal(raw, &answer); err != nil {
			t.Fatalf("decode: %v (%s)", err, raw)
		}
		byID := map[string]map[string]any{}
		for _, row := range answer.Projects {
			if id, ok := row["id"].(string); ok {
				byID[id] = row
			}
		}
		return answer, byID
	}

	rec := doJSON(t, s, http.MethodGet, "/api/v1/projects?page=1&page_size=2", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("page 1: %d %s", rec.Code, rec.Body.String())
	}
	first, rows := decode(rec.Body.Bytes())
	if first.Total != 3 || first.Page != 1 || first.PageSize != 2 || len(first.Projects) != 2 {
		t.Fatalf("page 1 = %d rows, total %d, page %d, size %d; want 2 of 3 on page 1",
			len(first.Projects), first.Total, first.Page, first.PageSize)
	}
	for id, row := range rows {
		if _, ok := row["session_count"]; !ok {
			t.Fatalf("every row carries its conversation count: %s = %v", id, row)
		}
	}

	rec = doJSON(t, s, http.MethodGet, "/api/v1/projects?page=2&page_size=2", nil)
	second, _ := decode(rec.Body.Bytes())
	if second.Total != 3 || second.Page != 2 || len(second.Projects) != 1 {
		t.Fatalf("page 2 = %d rows, total %d, page %d; want the last 1 of 3",
			len(second.Projects), second.Total, second.Page)
	}

	// A page past the end serves the last page, not an empty table.
	rec = doJSON(t, s, http.MethodGet, "/api/v1/projects?page=9&page_size=2", nil)
	past, _ := decode(rec.Body.Bytes())
	if past.Total != 3 || past.Page != 2 || len(past.Projects) != 1 {
		t.Fatalf("page past the end = %d rows, total %d, page %d; want page 2 of 3",
			len(past.Projects), past.Total, past.Page)
	}

	// The search box decides the page and the total together, and the row it
	// finds carries its own conversation count.
	rec = doJSON(t, s, http.MethodGet, "/api/v1/projects?page=1&page_size=10&q=alp", nil)
	searched, found := decode(rec.Body.Bytes())
	if searched.Total != 1 || len(searched.Projects) != 1 || searched.Projects[0]["id"] != ids[0] {
		t.Fatalf("search = %+v total %d, want only alpha", searched.Projects, searched.Total)
	}
	if got := found[ids[0]]["session_count"]; got != float64(2) {
		t.Fatalf("alpha's session_count = %v, want 2", got)
	}
	rec = doJSON(t, s, http.MethodGet, "/api/v1/projects?page=1&page_size=10&q=bet", nil)
	_, quiet := decode(rec.Body.Bytes())
	if got, seen := quiet[ids[1]]["session_count"]; !seen || got != float64(0) {
		t.Fatalf("beta holds no conversation, session_count = %v (present %v)", got, seen)
	}
}

// TestProjectsListWithoutPageKeepsTheOldAnswer pins that the callers which
// read this list without a page — the chat project picker and the folder
// dialog — still get the whole list under "projects", with no paging fields
// beside it. Every row gained the conversation count, and their own
// search/limit/offset arguments still work.
func TestProjectsListWithoutPageKeepsTheOldAnswer(t *testing.T) {
	s, store, _ := newProjectsTestServer(t)
	ctx := context.Background()
	for _, name := range []string{"alpha", "beta"} {
		if _, err := store.Create(ctx, state.CreateProjectInput{Name: name, Root: filepath.Join(t.TempDir(), name)}); err != nil {
			t.Fatal(err)
		}
	}

	rec := doJSON(t, s, http.MethodGet, "/api/v1/projects", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var answer map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	projects, _ := answer["projects"].([]any)
	if len(projects) != 2 {
		t.Fatalf("unpaged list = %v, want both projects", answer)
	}
	for _, field := range []string{"total", "page", "page_size"} {
		if _, seen := answer[field]; seen {
			t.Fatalf("%q must only appear on a paged answer: %v", field, answer)
		}
	}
	for _, raw := range projects {
		row, _ := raw.(map[string]any)
		if _, ok := row["session_count"]; !ok {
			t.Fatalf("every row carries the conversation count: %v", row)
		}
	}

	rec = doJSON(t, s, http.MethodGet, "/api/v1/projects?search=alp&limit=1", nil)
	answer = map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	projects, _ = answer["projects"].([]any)
	if len(projects) != 1 || projects[0].(map[string]any)["name"] != "alpha" {
		t.Fatalf("the picker's own search/limit = %v", answer)
	}
}

// TestProjectArchiveRouteIsGone pins that archiving a project is gone: the
// route no longer exists.
func TestProjectArchiveRouteIsGone(t *testing.T) {
	s, _, _ := newProjectsTestServer(t)
	rec := doJSON(t, s, http.MethodPost, "/api/v1/projects/any/archive", map[string]any{"archived": true})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("archive route = %d, want 404", rec.Code)
	}
}

// TestProjectCreateRootTakenReturnsConflict pins that a second project on a
// root the agent already registered is a 409 carrying the existing project.
func TestProjectCreateRootTakenReturnsConflict(t *testing.T) {
	s, _, _ := newProjectsTestServer(t)
	root := t.TempDir()
	rec := doJSON(t, s, http.MethodPost, "/api/v1/projects", map[string]any{"name": "one", "root": root})
	if rec.Code != http.StatusCreated {
		t.Fatalf("first create: %d %s", rec.Code, rec.Body.String())
	}
	var first map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s, http.MethodPost, "/api/v1/projects", map[string]any{"name": "two", "root": root})
	if rec.Code != http.StatusConflict {
		t.Fatalf("second create: %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "project_root_taken" {
		t.Fatalf("conflict body error = %v", body["error"])
	}
	project, _ := body["project"].(map[string]any)
	if project == nil || project["id"] != first["id"] {
		t.Fatalf("conflict must return the existing project: %v vs %v", project, first)
	}
}

// TestProjectDeletePreviewReturnsCounts pins what the confirmation dialog reads.
func TestProjectDeletePreviewReturnsCounts(t *testing.T) {
	s, store, _ := newProjectsTestServer(t)
	ctx := context.Background()
	root := t.TempDir()
	rec := doJSON(t, s, http.MethodPost, "/api/v1/projects", map[string]any{"name": "prev", "root": root})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id, _ := created["id"].(string)
	for _, sid := range []string{"p1", "p2"} {
		if err := s.Sessions.Ensure(ctx, sid, "t"); err != nil {
			t.Fatal(err)
		}
		// The count follows the sessions tab's predicate: only a session
		// that holds a message is a conversation.
		if _, err := s.Sessions.Append(ctx, sid, "user", "hello"); err != nil {
			t.Fatal(err)
		}
		if err := store.BindSession(ctx, sid, id); err != nil {
			t.Fatal(err)
		}
	}
	rec = doJSON(t, s, http.MethodGet, "/api/v1/projects/"+id+"/delete-preview", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["sessions"].(float64) != 2 || body["cron_jobs"].(float64) != 0 || body["running"] != false {
		t.Fatalf("preview body = %v", body)
	}
}

// TestProjectDeleteBusyReturnsConflict pins that deleting a project whose
// conversation has a run in flight is a 409 the dialog can name.
func TestProjectDeleteBusyReturnsConflict(t *testing.T) {
	s, store, home := newProjectsTestServer(t)
	ctx := context.Background()
	root := t.TempDir()
	rec := doJSON(t, s, http.MethodPost, "/api/v1/projects", map[string]any{"name": "busy", "root": root})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id, _ := created["id"].(string)
	if err := s.Sessions.Ensure(ctx, "sess-busy", "t"); err != nil {
		t.Fatal(err)
	}
	if err := store.BindSession(ctx, "sess-busy", id); err != nil {
		t.Fatal(err)
	}
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := (&state.RunStore{DB: db}).CreateRun(ctx, "sess-busy", "hello"); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s, http.MethodDelete, "/api/v1/projects/"+id, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("busy delete: %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "project_busy" {
		t.Fatalf("busy body = %v", body)
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
	// The session list for the project contains it — once it holds a
	// message, because a row with no message is not a conversation.
	_, err = s.Sessions.Append(context.Background(), session.ID, "user", "hello")
	if err != nil {
		t.Fatal(err)
	}
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
	// ...and so does the agent's conversation drawer: every conversation
	// belongs to a project now, and the drawer lists it carrying the
	// project's name.
	rec = httptest.NewRecorder()
	s.handleChatSessions(rec, httptest.NewRequest(http.MethodGet, "/api/chat/sessions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("chat sessions: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), session.ID) || !strings.Contains(rec.Body.String(), `"project_name":"withsessions"`) {
		t.Fatalf("drawer list lost the project session %s or its project name: %s", session.ID, rec.Body.String())
	}
	// The chat page still names the opened session — its title and its
	// project — by reading the session itself rather than the drawer's
	// summary row.
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
	s, projects, _ := newProjectsTestServer(t)
	ctx := context.Background()
	ensure := func(id, title string) {
		t.Helper()
		if err := s.Sessions.Ensure(ctx, id, title); err != nil {
			t.Fatal(err)
		}
	}
	// The drawer lists conversations across projects, so the ordinary one
	// belongs to a project — the create endpoint's rule since plan 029 —
	// while the workshop task stays project-less: the one shape that
	// endpoint still opens at the primary agent's own level.
	p, err := projects.Create(ctx, state.CreateProjectInput{Name: "purpose", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ensure("talk", "a conversation")
	if err := projects.BindSession(ctx, "talk", p.ID); err != nil {
		t.Fatal(err)
	}
	ensure("task", "a workshop task")
	if err := s.Sessions.SetSessionSource(ctx, "task", state.SessionSourceWorkshop); err != nil {
		t.Fatal(err)
	}
	// Both hold a message: what this test pins is the query's purpose filter,
	// and a session with no message is not a conversation, so neither list
	// would return it.
	for _, id := range []string{"talk", "task"} {
		if _, err := s.Sessions.Append(ctx, id, "user", "hello"); err != nil {
			t.Fatal(err)
		}
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
	// The drawer's default list holds the project's conversation and the
	// primary agent's own workshop task alike: every conversation the agent
	// may reopen.
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"talk"`) || !strings.Contains(rec.Body.String(), `"id":"task"`) {
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

// TestChatSessionsPagesTheDrawerList pins the drawer list's paged answer:
// the list contract's shape — records, total, page and page_size — with the
// project's name on each row that belongs to one, a search that filters on
// the server before the page is cut, and a page past the end serving the
// last page. The workshop list's answer stays the shape it always had.
func TestChatSessionsPagesTheDrawerList(t *testing.T) {
	s, projects, _ := newProjectsTestServer(t)
	ctx := context.Background()
	p, err := projects.Create(ctx, state.CreateProjectInput{Name: "paged", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// Three conversations in the project, each a step apart in time so the
	// order is deterministic, plus a pre-project ordinary row that must not
	// take a row of any page.
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("c-%d", i)
		if err := s.Sessions.Ensure(ctx, id, "conversation "+id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Sessions.Append(ctx, id, "user", "hello"); err != nil {
			t.Fatal(err)
		}
		if err := projects.BindSession(ctx, id, p.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Sessions.DB().Exec("UPDATE fb_sessions SET updated_at = ? WHERE id = ?", int64(30-i), id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Sessions.Append(ctx, "legacy", "user", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sessions.DB().Exec("UPDATE fb_sessions SET updated_at = ? WHERE id = ?", int64(99), "legacy"); err != nil {
		t.Fatal(err)
	}

	get := func(query string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		s.handleChatSessions(rec, httptest.NewRequest(http.MethodGet, "/api/chat/sessions"+query, nil))
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return rec, body
	}

	rec, page1 := get("?source=&page=1&page_size=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("page 1: %d %s", rec.Code, rec.Body.String())
	}
	if got := page1["total"].(float64); got != 3 {
		t.Fatalf("total = %v, want 3", page1["total"])
	}
	ids := func(body map[string]any) []string {
		out := []string{}
		for _, row := range body["records"].([]any) {
			out = append(out, row.(map[string]any)["id"].(string))
		}
		return out
	}
	if want := []string{"c-0", "c-1"}; !slices.Equal(ids(page1), want) {
		t.Fatalf("page 1 rows = %v, want %v", ids(page1), want)
	}
	for _, row := range page1["records"].([]any) {
		if row.(map[string]any)["project_name"] != "paged" {
			t.Fatalf("row without its project's name: %v", row)
		}
	}

	rec, page2 := get("?source=&page=2&page_size=2")
	if rec.Code != http.StatusOK || !slices.Equal(ids(page2), []string{"c-2"}) {
		t.Fatalf("page 2: %d %s", rec.Code, rec.Body.String())
	}
	if got := page2["total"].(float64); got != 3 {
		t.Fatalf("page 2 total = %v, want 3", page2["total"])
	}

	// A page past the end serves the last page.
	rec, last := get("?source=&page=99&page_size=2")
	if rec.Code != http.StatusOK || !slices.Equal(ids(last), []string{"c-2"}) {
		t.Fatalf("past-the-end page: %d %s", rec.Code, rec.Body.String())
	}
	if got := last["page"].(float64); got != 2 {
		t.Fatalf("served page = %v, want 2", last["page"])
	}
	if got := last["page_size"].(float64); got != 2 {
		t.Fatalf("page_size = %v, want 2", last["page_size"])
	}

	// The search filters on the server before the page is cut.
	rec, found := get("?source=&q=conversation+c-2&page=1&page_size=2")
	if rec.Code != http.StatusOK || !slices.Equal(ids(found), []string{"c-2"}) || found["total"].(float64) != 1 {
		t.Fatalf("searched page: %d %s", rec.Code, rec.Body.String())
	}

	// The workshop list keeps its legacy shape: records alone, no pager.
	rec, workshop := get("?source=workshop")
	if rec.Code != http.StatusOK {
		t.Fatalf("workshop list: %d %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{"total", "page", "page_size"} {
		if _, paged := workshop[field]; paged {
			t.Fatalf("workshop answer grew a pager: %s", rec.Body.String())
		}
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

// TestProjectMCPPreviewRowsCarryNoRuntimeWithoutAProjectRunner pins the
// project page's live half: it reads the project's own runner, and a project
// with none — nothing has talked in it yet — reports rows with no runtime
// state at all, which the page renders as "not started". The base runner is
// never the source: it hosts no servers.
func TestProjectMCPPreviewRowsCarryNoRuntimeWithoutAProjectRunner(t *testing.T) {
	f := newMCPPreviewFixture(t)

	body := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet, "/api/v1/projects/"+f.projectID+"/mcp?page=1&page_size=10", nil))
	for _, name := range mcpReadRowNames(t, body) {
		row := mcpServerRow(t, body, name)
		if _, ok := row["conn_status"]; ok {
			t.Fatalf("%s carries a runtime state with no project runner: %#v", name, row)
		}
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
	projects.Get("/:id/delete-preview", s.handleProjectDeletePreview)
	projects.Post("/:id/pin", s.handleProjectPin)
	projects.Get("/:id/sessions", s.handleProjectSessionsList)
	projects.Post("/:id/sessions", s.handleProjectSessionsCreate)
	projects.Get("/:id/mcp", s.handleProjectMCPPreview)
	projects.Post("/:id/mcp/consent", s.handleProjectMCPConsent)
	projects.Get("/:id/lsp", s.handleProjectLSPPreview)
	projects.Post("/:id/lsp/consent", s.handleProjectLSPConsent)
	projects.Get("/:id/servers/pending", s.handleProjectServersPending)
	projects.Post("/:id/servers/consent", s.handleProjectServersConsent)
	projects.Put("/:id/servers/auto-allow", s.handleProjectServersAutoAllow)
	// Project-scoped skill lifecycle, mirroring the production routes.
	projects.Get("/:id/skills", s.handleProjectSkillsList)
	projects.Post("/:id/skills", s.handleProjectSkillsCreate)
	projects.Post("/:id/skills/install", s.handleProjectSkillsInstall)
	projects.Get("/:id/skills/:name", s.handleProjectSkillGet)
	projects.Put("/:id/skills/:name", s.handleProjectSkillsUpdate)
	projects.Post("/:id/skills/toggle", s.handleProjectSkillsToggle)
	host := rest.Group("/api/host")
	host.Get("/dirs", s.handleHostDirs)
	host.Post("/dirs", s.handleHostDirs)
	host.Get("/trust", s.handleHostTrust)
	host.Post("/trust", s.handleHostTrust)
	// The MCP write surface, mirroring the production routes: three layers,
	// each with create, update, delete and a connection test.
	mcpRoutes := rest.Group("/api/v1/mcp")
	mcpRoutes.Get("/servers", s.handleMCPServersV1)
	mcpRoutes.Post("/servers", s.handleMCPGlobalServersWrite)
	mcpRoutes.Post("/servers/test", s.handleMCPServersTest)
	mcpRoutes.Put("/servers/:name", s.handleMCPGlobalServersWrite)
	mcpRoutes.Delete("/servers/:name", s.handleMCPGlobalServersWrite)
	agentRoutes := rest.Group("/api/v1/agents")
	agentRoutes.Post("/primary/:id/mcp/servers", s.handleMCPAgentServersWrite)
	agentRoutes.Post("/primary/:id/mcp/servers/test", s.handleMCPAgentServersTest)
	agentRoutes.Put("/primary/:id/mcp/servers/:name", s.handleMCPAgentServersWrite)
	agentRoutes.Delete("/primary/:id/mcp/servers/:name", s.handleMCPAgentServersWrite)
	projects.Post("/:id/mcp/servers", s.handleProjectMCPServersWrite)
	projects.Post("/:id/mcp/servers/test", s.handleProjectMCPServersTest)
	projects.Put("/:id/mcp/servers/:name", s.handleProjectMCPServersWrite)
	projects.Delete("/:id/mcp/servers/:name", s.handleProjectMCPServersWrite)
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
		{Slug: "gpt-hidden", Visibility: "hide", Priority: 0},
		{Slug: "gpt-future", Visibility: "list", Priority: 1,
			DefaultReasoningEffort: "low",
			SupportedReasoningEfforts: []llmopenai.ReasoningEffortPreset{
				{Effort: "low"}, {Effort: "max"},
			}},
		{Slug: "gpt-second", Visibility: "list", Priority: 2},
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

	// The listing also carries the endpoint each provider defaults to: the
	// model-service form fills a base URL in from here. A list, so a client
	// that camel-cases keys cannot rewrite a provider id.
	var defaults struct {
		ProviderBaseURLs []struct {
			Provider string `json:"provider"`
			BaseURL  string `json:"base_url"`
		} `json:"provider_base_urls"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &defaults))
	require.NotEmpty(t, defaults.ProviderBaseURLs)
	for _, row := range defaults.ProviderBaseURLs {
		if row.Provider == "deepseek" {
			require.Equal(t, "https://api.deepseek.com", row.BaseURL)
			return
		}
	}
	t.Fatalf("deepseek missing from %+v", defaults.ProviderBaseURLs)
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

// TestChatMessagesToolImageRowCarriesNoBase64 pins 057's API face: a tool
// result whose image part knows its file is served as a file_reference, so the
// /messages row (and the whole response) stays base64-free and bounded in
// size, and the row does not grow an attachments field — that field is what
// the user pinned, while a tool row's reference is the pixels the model was
// sent. A user row's real attachment keeps surfacing exactly as before.
func TestChatMessagesToolImageRowCarriesNoBase64(t *testing.T) {
	ctx := context.Background()
	s := cronTestServer(t)
	s.Sessions = state.NewSessionStore(s.Env.SQL, "main")
	require.NoError(t, s.Sessions.Ensure(ctx, "s1", "s1"))

	// A user row with a real attachment: the attachments field keeps it.
	userParts := state.MessagePartsJSON(llm.UserMessage(llm.Text("look")), "look")
	userParts = state.AppendRawPartJSON(userParts, state.FileReferencePartJSON("/tmp/clip.png", "clip.png", "image/png"))
	_, err := s.Sessions.AppendStructuredMessage(ctx, "s1", "user", "look", "", userParts, "", "", "", "", state.MessageExecTiming{})
	require.NoError(t, err)

	// A tool row whose image part was built from a file on disk, exactly as
	// every image-producing tool does.
	imgPath := filepath.Join(t.TempDir(), "shot.png")
	pngBytes := tinyPNG(t)
	require.NoError(t, os.WriteFile(imgPath, pngBytes, 0o600))
	part, _, err := llm.ImagePartWithin(imgPath, "image/png", pngBytes, 0)
	require.NoError(t, err)
	require.Equal(t, imgPath, part.SourcePath)
	call := llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: "{}"}}
	toolMsg := llm.ToolResultMessage("call-1", llm.Text("saved"), part)
	require.NoError(t, s.Sessions.AppendMessageSequence(ctx, "s1", []llm.Message{llm.AssistantMessage(nil, call), toolMsg}, "", ""))

	rec := cronRequest(t, s, http.MethodGet, "/api/chat/sessions/s1/messages", nil, s.handleChatMessages, "id", "s1")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "base64", "no row may carry image base64")

	type chatAttachmentRow struct {
		Name      string `json:"name"`
		MediaType string `json:"media_type,omitempty"`
	}
	var rows []struct {
		Role        string              `json:"role"`
		PartsJSON   string              `json:"parts_json"`
		Attachments []chatAttachmentRow `json:"attachments"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))

	var tool, user *struct {
		Role        string              `json:"role"`
		PartsJSON   string              `json:"parts_json"`
		Attachments []chatAttachmentRow `json:"attachments"`
	}
	for i := range rows {
		switch rows[i].Role {
		case "tool":
			tool = &rows[i]
		case "user":
			user = &rows[i]
		}
	}
	require.NotNil(t, tool, "tool row missing: %s", rec.Body.String())
	require.NotNil(t, user, "user row missing: %s", rec.Body.String())

	require.Contains(t, tool.PartsJSON, `"file_reference"`)
	require.Contains(t, tool.PartsJSON, imgPath)
	require.Less(t, len(tool.PartsJSON), 2048, "tool row parts_json must stay small")
	require.Empty(t, tool.Attachments, "a tool row must not surface attachments")

	require.Len(t, user.Attachments, 1)
	require.Equal(t, "clip.png", user.Attachments[0].Name)
	require.Equal(t, "image/png", user.Attachments[0].MediaType)
}

// TestChatMessagesOmitToolContent pins the history DTO's tool rows: the row's
// model-facing content — for a shell, the JSON map with its stdout — is not
// sent, because the page draws the row from parts_json and shell_card and the
// model payload would be a second and third copy of the same bytes. The rows
// around it keep their content word for word.
func TestChatMessagesOmitToolContent(t *testing.T) {
	ctx := context.Background()
	s := cronTestServer(t)
	s.Sessions = state.NewSessionStore(s.Env.SQL, "main")
	require.NoError(t, s.Sessions.Ensure(ctx, "s1", "s1"))

	// A shell row persisted the way the engine persists it: the model-facing
	// payload as content, the formatted card body as the display part.
	evt := tool.StepEvent{
		Kind:     tool.StepKindToolCompleted,
		ToolName: "shell",
		Input:    map[string]any{"command": "printf hello"},
		Output:   map[string]any{"stdout": "hello\n", "stderr": "", "exit_code": 0},
	}
	body, _ := tool.FormatToolStepResult(evt, tool.DefaultMaxFormattedBody)
	metaJSON, err := json.Marshal(tool.BuildToolMeta(evt))
	require.NoError(t, err)
	call := llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: `{"command":"printf hello"}`}}
	toolMsg := llm.ToolResultMessage("call-1", llm.Text(`{"stdout":"hello\n","stderr":"","exit_code":0}`))
	toolMsg.ToolDisplay = &llm.ToolDisplayState{Body: body, Summary: tool.SummarizeToolStep(evt), ToolMetaJSON: string(metaJSON)}
	require.NoError(t, s.Sessions.AppendMessageSequence(ctx, "s1", []llm.Message{
		llm.UserMessage(llm.Text("typed by hand")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("running it")}, call),
		toolMsg,
	}, "", ""))

	rec := cronRequest(t, s, http.MethodGet, "/api/chat/sessions/s1/messages", nil, s.handleChatMessages, "id", "s1")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var rows []struct {
		Role      string `json:"role"`
		Content   string `json:"content"`
		PartsJSON string `json:"parts_json"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))

	var toolRow, userRow, assistantRow *struct {
		Role      string `json:"role"`
		Content   string `json:"content"`
		PartsJSON string `json:"parts_json"`
	}
	for i := range rows {
		switch rows[i].Role {
		case "tool":
			toolRow = &rows[i]
		case "user":
			userRow = &rows[i]
		case "assistant":
			assistantRow = &rows[i]
		}
	}
	require.NotNil(t, toolRow, "tool row missing: %s", rec.Body.String())
	require.NotNil(t, userRow, "user row missing: %s", rec.Body.String())
	require.NotNil(t, assistantRow, "assistant row missing: %s", rec.Body.String())

	require.Empty(t, toolRow.Content, "a tool row's model-facing content must not be sent")
	require.NotEmpty(t, toolRow.PartsJSON, "the tool row's display copy must stay")
	require.Equal(t, "typed by hand", userRow.Content)
	require.Equal(t, "running it", assistantRow.Content)
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

// TestChatMessagesPagesBackwards pins plan 173's paging contract: a limit
// returns the newest page, a before cursor the page directly above it, and
// together the pages tile the conversation with no repeats and no gaps —
// including the two rows that live on a page boundary. A compaction anchored
// at the boundary row shows on one page only (the older page's tail). A run
// that straddles the boundary closes with its Worked row on the newer page,
// the only page that holds its last row.
func TestChatMessagesPagesBackwards(t *testing.T) {
	ctx := context.Background()
	s := cronTestServer(t)
	s.Sessions = state.NewSessionStore(s.Env.SQL, "main")
	seedRun(t, s.RunRT, "s1", "r1")
	seedRun(t, s.RunRT, "s1", "r2")
	base := time.Unix(1_700_000_000, 0)
	appendRow := func(role, content, runID string, at time.Duration) int64 {
		t.Helper()
		id, err := s.Sessions.AppendMessageForRun(ctx, "s1", runID, role, content, "", "", state.MessageExecTiming{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Env.SQL.ExecContext(ctx, `UPDATE fb_messages SET created_at = ? WHERE id = ?`, base.Add(at).Unix(), id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// Six visible rows, pages of three. r1 straddles the page boundary: its
	// first row is the older page's newest row, its last row the newer
	// page's oldest. The compaction fires between the two — anchored at the
	// boundary row, drawn right after it.
	rowOne := appendRow("user", "one", "", 0)
	rowTwo := appendRow("user", "two", "", time.Second)
	rowThree := appendRow("assistant", "three", "r1", 2*time.Second)
	rowFour := appendRow("assistant", "four", "r1", 3*time.Second)
	rowFive := appendRow("assistant", "five", "r2", 4*time.Second)
	rowSix := appendRow("assistant", "six", "r2", 5*time.Second)
	// A finished clock per run, so each closes with a Worked row.
	for _, r := range []struct {
		id         string
		start, end time.Duration
	}{{"r1", 2 * time.Second, 3*time.Second + 500*time.Millisecond}, {"r2", 4 * time.Second, 6 * time.Second}} {
		if _, err := s.Env.SQL.ExecContext(ctx,
			`UPDATE fb_runs SET started_at_ms = ?, finished_at_ms = ?, worked_ms = ? WHERE id = ?`,
			base.Add(r.start).UnixMilli(), base.Add(r.end).UnixMilli(), (r.end - r.start).Milliseconds(), r.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.RunRT.AppendSessionEvent(ctx, sessionEventRecord(event.NewRunEvent("", "", "s1", event.RunEventContextCompacted, event.ContextCompactedPayload{CompactionID: "c1", Trigger: "manual", TokensBefore: 900, TokensAfter: 100}, base.Add(2500*time.Millisecond)))); err != nil {
		t.Fatal(err)
	}

	type row struct {
		Role       string `json:"role"`
		Content    string `json:"content"`
		RowID      int64  `json:"row_id"`
		RunID      string `json:"run_id"`
		Compaction *struct {
			CompactionID string `json:"compaction_id"`
		} `json:"compaction"`
	}
	page := func(target string) []row {
		t.Helper()
		rec := cronRequest(t, s, http.MethodGet, target, nil, s.handleChatMessages, "id", "s1")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", target, rec.Code, rec.Body.String())
		}
		var rows []row
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	render := func(rows []row) string {
		parts := make([]string, 0, len(rows))
		for _, r := range rows {
			name := r.Role
			if r.RunID != "" {
				name += "-" + r.RunID
			}
			if r.Compaction != nil {
				name += "-" + r.Compaction.CompactionID
			}
			parts = append(parts, name)
		}
		return strings.Join(parts, ",")
	}

	// a) The newest page is the three newest rows; before = the page's
	// oldest row id returns the three rows directly above it, and the two
	// pages' message rows together are the whole conversation exactly once.
	newer := page("/api/chat/sessions/s1/messages?limit=3")
	if got := render(newer); got != "assistant-r1,worked-r1,assistant-r2,assistant-r2,worked-r2" {
		t.Fatalf("newest page = %s", got)
	}
	older := page(fmt.Sprintf("/api/chat/sessions/s1/messages?limit=3&before=%d", newer[0].RowID))
	if got := render(older); got != "user,user,assistant-r1,compaction-c1" {
		t.Fatalf("older page = %s", got)
	}
	if newer[0].RowID != rowFour {
		t.Fatalf("the cursor row = %d, want four's row %d", newer[0].RowID, rowFour)
	}
	var gotIDs []int64
	for _, r := range append(append([]row{}, older...), newer...) {
		if r.Role == "user" || r.Role == "assistant" {
			gotIDs = append(gotIDs, r.RowID)
		}
	}
	wantIDs := []int64{rowOne, rowTwo, rowThree, rowFour, rowFive, rowSix}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("pages tiled %v, want %v (no repeats, no gaps)", gotIDs, wantIDs)
	}

	// b) The compaction anchored at the boundary row (three) appears once,
	// on the older page's tail; the newer page does not repeat it at its
	// head.
	compactions := 0
	for _, r := range append(append([]row{}, older...), newer...) {
		if r.Compaction != nil {
			compactions++
		}
	}
	if compactions != 1 || older[len(older)-1].Compaction == nil {
		t.Fatalf("the boundary compaction must close the older page once: %s | %s", render(older), render(newer))
	}

	// c) r1 straddles the boundary, so its Worked row is on the newer page,
	// right after its actual last row; the older page, which cannot know
	// where r1 ended, does not close it a second time.
	for _, r := range older {
		if r.Role == "worked" && r.RunID == "r1" {
			t.Fatalf("the older page closed the straddling run: %s", render(older))
		}
	}
	if newer[1].Role != "worked" || newer[1].RunID != "r1" {
		t.Fatalf("the newer page must close r1 after its last row: %s", render(newer))
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
	// A tool row's model-facing content is not part of the history DTO (plan
	// 171): the surface draws the row from its parts and card facts, so the
	// same bytes are not sent a second and third time.
	require.Empty(t, transcript[2].Content)
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
	// Since plan 029 an upload is saved into the conversation's project, so the
	// owner's session is bound to one. The other agent's session is nobody's
	// here and reads as missing before a project is ever consulted.
	root := t.TempDir()
	projects := state.NewProjectStore(db, "main")
	project, err := projects.Create(ctx, state.CreateProjectInput{Name: "mine", Root: root})
	require.NoError(t, err)
	require.NoError(t, projects.BindSession(ctx, "s-mine", project.ID))
	s := &Server{
		Files:    &state.FileStore{DB: db, Home: t.TempDir(), Cfg: state.LoadConfigFromEnv()},
		Sessions: state.NewSessionStore(db, "main"),
		Projects: projects,
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
	// The original was saved into the project (the root, since no dir was
	// given) rather than the workspace, and its row says so.
	require.Equal(t, "note.txt", f.ProjectPath)
	require.Empty(t, f.StorageKey)
	require.FileExists(t, filepath.Join(root, "note.txt"))
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
	// A conversation born with no directory of its own — the terminal's own
	// path through the store — still takes the process's launch directory:
	// opening a project session must not have moved that default.
	const ordinaryID = "ordinary-launch"
	if err := s.Sessions.Ensure(context.Background(), ordinaryID, "Ordinary"); err != nil {
		t.Fatal(err)
	}
	if got := cwdOf(ordinaryID); got != "/launch" {
		t.Fatalf("ordinary session born at %q, want the launch directory the fixture set", got)
	}
}

// TestNewWebChatIsNamedByItsFirstMessage pins that a chat the web opens from
// the new-chat page is born unnamed — its stored title is its own id, which
// the client draws as a placeholder — and that its first user message then
// names it, the way a terminal session is named, instead of a stored
// placeholder outranking that message forever. (Since plan 029 a web chat is
// opened into a project, so it is born under the folder the picker chose.)
func TestNewWebChatIsNamedByItsFirstMessage(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.Open(ctx, filepath.Join(home, "state.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	sessions := state.NewSessionStore(db, "main")
	s := &Server{Home: home, Sessions: sessions, Projects: state.NewProjectStore(db, "main")}

	folder := t.TempDir()
	payload, err := json.Marshal(map[string]any{"title": "", "folder": folder})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	s.handleChatSessionCreate(rec, httptest.NewRequest(http.MethodPost, "/api/chat/sessions", strings.NewReader(string(payload))))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.NotEmpty(t, created.ID)

	// Born unnamed: the stored title is the session's own id, which the store
	// reports as an empty display title, so the client draws its placeholder.
	storedTitle, err := sessions.SessionTitle(ctx, created.ID)
	require.NoError(t, err)
	require.Empty(t, storedTitle)

	_, err = sessions.Append(ctx, created.ID, "user", "Plan the release checklist")
	require.NoError(t, err)
	storedTitle, err = sessions.SessionTitle(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, "Plan the release checklist", storedTitle)
}

// TestChatSessionCreateRequiresProjectOrFolder pins the plan-029 contract for
// the new-chat endpoint: an ordinary conversation can only be opened into a
// project (named by id, or created from a folder), a folder already bound to a
// project reuses it, and the one exception — a primary-agent-level workshop
// task — still opens with no project.
func TestChatSessionCreateRequiresProjectOrFolder(t *testing.T) {
	ctx := context.Background()
	s, store, _ := newProjectsTestServer(t)
	s.Sessions.ConfigureMemoryDefaults("disabled", "webchat", "/launch", "main")

	create := func(body map[string]any) *httptest.ResponseRecorder {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		rr := httptest.NewRecorder()
		s.handleChatSessionCreate(rr, httptest.NewRequest(http.MethodPost, "/api/chat/sessions", strings.NewReader(string(raw))))
		return rr
	}
	cwdOf := func(id string) string {
		var cwd string
		require.NoError(t, s.Sessions.DB().QueryRow("SELECT cwd FROM fb_sessions WHERE id=?", id).Scan(&cwd))
		return cwd
	}

	// No project, an ordinary source: refused.
	rr := create(map[string]any{"title": "orphan"})
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "project_required")

	// A folder that is not yet a project: the project is created from it and
	// the session is born in that directory.
	folder := projectTempRoot(t)
	rr = create(map[string]any{"title": "with folder", "folder": folder})
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	var first struct {
		ID      string `json:"id"`
		Project struct {
			ID   string `json:"id"`
			Root string `json:"root"`
		} `json:"project"`
		Created bool `json:"created"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &first))
	require.True(t, first.Created, "the folder had no project, so one should have been created")
	require.NotEmpty(t, first.Project.ID)
	require.Equal(t, folder, first.Project.Root)
	require.Equal(t, folder, cwdOf(first.ID))

	// The same folder again reuses the project.
	rr = create(map[string]any{"title": "again", "folder": folder})
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	var second struct {
		ID      string `json:"id"`
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
		Created bool `json:"created"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &second))
	require.False(t, second.Created, "the folder already had a project")
	require.Equal(t, first.Project.ID, second.Project.ID)

	// A known project id opens its session in the project root.
	rr = create(map[string]any{"title": "by id", "project_id": first.Project.ID})
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	var byID struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &byID))
	require.Equal(t, folder, cwdOf(byID.ID))

	// A project whose root no longer exists is refused with the directory code.
	gone := projectTempRoot(t)
	p, err := store.Create(ctx, state.CreateProjectInput{Name: "gone", Root: gone, ProjectKey: state.ProjectKeyForRoot(gone)})
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(gone))
	rr = create(map[string]any{"title": "missing dir", "project_id": p.ID})
	require.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "session_directory_missing")

	// A primary-agent-level workshop task is the one no-project exception.
	rr = create(map[string]any{"title": "agent workshop", "source": "workshop"})
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
}

// TestCronJobBindsOnlyToThisAgentsProject pins that a job's project is tenant
// data: a route naming another agent's project, or one that does not exist, is
// refused as not found instead of binding the job across tenants.
func TestCronJobBindsOnlyToThisAgentsProject(t *testing.T) {
	s := cronTestServer(t)
	ctx := context.Background()
	theirs, err := state.NewProjectStore(s.RunRT.DB, "someone-else").Create(ctx, state.CreateProjectInput{Name: "theirs", Root: t.TempDir()})
	require.NoError(t, err)

	for _, projectID := range []string{theirs.ID, "prj_missing"} {
		rec := cronRequest(t, s, http.MethodPost, "/api/projects/"+projectID+"/cron/jobs", map[string]any{
			"schedule": "every 1h", "prompt": "report",
		}, s.handleCronJobs, "id", projectID)
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	}
	mine := cronTestProjectID(t, s)
	rec := cronRequest(t, s, http.MethodPost, "/api/projects/"+mine+"/cron/jobs", map[string]any{
		"schedule": "every 1h", "prompt": "report",
	}, s.handleCronJobs, "id", mine)
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

// A preset picked in the web dropdown is durable: a server (and runner)
// rebuilt over the same state database — a restart, or another process —
// reads the same preset back from the row the POST wrote.
func TestSessionPresetSurvivesServerRestart(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(ctx, "s1", "s1"))
	cfg := &appcfg.Root{}
	newServer := func() *Server {
		runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", AppCfg: cfg, SessionStore: sessions}}
		return &Server{Home: home, Runner: runner, Env: &process.Environment{Deps: run.Deps{AppCfg: cfg}, Root: home, Sandbox: safety.NewManager(), Runner: runner}}
	}
	s := newServer()

	rr := httptest.NewRecorder()
	s.handleSessionPreset(rr, httptest.NewRequest(http.MethodPost, "/api/permissions/session-preset", strings.NewReader(`{"session_id":"s1","preset":"full-access"}`)))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// The "other process": a fresh Server and Runner, nothing in memory.
	rr = httptest.NewRecorder()
	newServer().handleSessionPresetGet(rr, httptest.NewRequest(http.MethodGet, "/api/permissions/session-preset?session_id=s1", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"current":"full-access"`)
	require.Contains(t, rr.Body.String(), `"source":"picker"`)
}

// A session that ran under TUI --yolo before presets were persisted is read
// back as full-access once its own tool results carry the bypass marker.
func TestSessionPresetGetInfersLegacyYoloSession(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(ctx, "yoloed", "yoloed"))
	_, err = db.Exec(`INSERT INTO fb_messages(session_id, role, visibility, content, created_at) VALUES('yoloed','tool','visible','{"output":"ok","approval_bypassed_by_yolo":true}',10)`)
	require.NoError(t, err)

	cfg := &appcfg.Root{}
	runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", AppCfg: cfg, SessionStore: sessions}}
	s := &Server{Home: home, Runner: runner, Env: &process.Environment{Deps: run.Deps{AppCfg: cfg}, Root: home, Sandbox: safety.NewManager(), Runner: runner}}

	rr := httptest.NewRecorder()
	s.handleSessionPresetGet(rr, httptest.NewRequest(http.MethodGet, "/api/permissions/session-preset?session_id=yoloed", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"current":"full-access"`)
	require.Contains(t, rr.Body.String(), `"source":"observed"`)
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

func TestProviderKeyBackfillFollowsTheEntryNotTheProvider(t *testing.T) {
	s, home := providersDTOServer(t)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".env"),
		[]byte("ZHIPUAI_A_KEY=sk-aaaa-1111\nZHIPUAI_B_KEY=sk-bbbb-2222\n"), 0o600))
	created := providersPut(t, s, `{"providers":[
		{"provider":"zhipuai","models":["glm-4"],"base_url":"https://a.example","api_key":"${ZHIPUAI_A_KEY}"},
		{"provider":"zhipuai","models":["glm-4"],"base_url":"https://b.example","api_key":"${ZHIPUAI_B_KEY}"}
	]}`)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())

	// 同两行再存一次，api_key 都不在线上 —— "用户没动 key"。
	untouched := providersPut(t, s, `{"providers":[
		{"provider":"zhipuai","models":["glm-4"],"base_url":"https://a.example"},
		{"provider":"zhipuai","models":["glm-4"],"base_url":"https://b.example"}
	]}`)
	require.Equal(t, http.StatusOK, untouched.Code, untouched.Body.String())

	rows := providersGet(t, s)
	require.Len(t, rows, 2)
	require.Equal(t, "https://a.example", rows[0]["base_url"])
	require.Equal(t, "https://b.example", rows[1]["base_url"])
	require.Equal(t, "1111", rows[0]["api_key_hint"], "a.example 必须还是 A 的 key")
	require.Equal(t, "2222", rows[1]["api_key_hint"], "b.example 不许继承 A 的 key")
}

func TestProviderKeyBackfillNeverCrossesEndpoints(t *testing.T) {
	s, home := providersDTOServer(t)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".env"),
		[]byte("ZHIPUAI_A_KEY=sk-aaaa-1111\n"), 0o600))
	created := providersPut(t, s, `{"providers":[
		{"provider":"zhipuai","models":["glm-4"],"base_url":"https://a.example","api_key":"${ZHIPUAI_A_KEY}"}
	]}`)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())

	// 同一批请求里再加一个**新 endpoint**，仍然不带 key：它没有任何已存身份可继承。
	added := providersPut(t, s, `{"providers":[
		{"provider":"zhipuai","models":["glm-4"],"base_url":"https://a.example"},
		{"provider":"zhipuai","models":["glm-4"],"base_url":"https://c.example"}
	]}`)
	require.Equal(t, http.StatusBadRequest, added.Code,
		"a new endpoint must not inherit another endpoint's key; the write is refused instead: %s", added.Body.String())
	require.Contains(t, added.Body.String(), "needs api_key")
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

// A key-mode fallback cannot lose its key either. The row would save and then
// answer 401 the moment the chain fell back to it, so an empty api_key is
// refused wherever the row sits, and nothing is written.
func TestProvidersDTOKeyClearOnAFallbackIsRefused(t *testing.T) {
	s, home := providersDTOServer(t)
	cfgPath := filepath.Join(home, "forebrain.yaml")
	seed := `agents:
  definitions:
    main:
      primary: true
      llm_providers:
      - provider: deepseek
        model: deepseek-chat
        api_key: ${DEEPSEEK_API_KEY}
        base_url: https://api.deepseek.com
      - provider: arcee-ai
        model: e2e-model
        api_key: ${ARCEE_AI_API_KEY}
        base_url: https://api.arcee.ai/v1
`
	require.NoError(t, os.WriteFile(cfgPath, []byte(seed), 0o600))
	before, err := os.ReadFile(cfgPath)
	require.NoError(t, err)

	cleared := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","base_url":"https://api.deepseek.com","models":["deepseek-chat"]},
		{"provider":"arcee-ai","base_url":"https://api.arcee.ai/v1","models":["e2e-model"],"api_key":""}
	]}`)
	require.Equal(t, http.StatusBadRequest, cleared.Code, cleared.Body.String())
	require.Contains(t, cleared.Body.String(), "arcee-ai needs api_key")

	after, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "a refused clear writes nothing")

	// The untouched table still reads back with both keys.
	rows := providersGet(t, s)
	require.Len(t, rows, 2)
	require.Equal(t, true, rows[0]["api_key_set"])
	require.Equal(t, true, rows[1]["api_key_set"])
}

// The login-backed services are the one shape an empty key is correct for:
// their credential is the file the login writes, so a keyless row of theirs is
// saved rather than refused.
func TestProvidersDTOLoginBackedFallbackMayStayKeyless(t *testing.T) {
	s, home := providersDTOServer(t)
	seed := `agents:
  definitions:
    main:
      primary: true
      llm_providers:
      - provider: deepseek
        model: deepseek-chat
        api_key: ${DEEPSEEK_API_KEY}
        base_url: https://api.deepseek.com
      - provider: chatgpt
        model: gpt-5
        base_url: https://chatgpt.com/backend-api/codex
`
	require.NoError(t, os.WriteFile(filepath.Join(home, "forebrain.yaml"), []byte(seed), 0o600))

	rr := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","base_url":"https://api.deepseek.com","models":["deepseek-chat"]},
		{"provider":"chatgpt","base_url":"https://chatgpt.com/backend-api/codex","models":["gpt-5"],"api_key":""}
	]}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	saved, err := appcfg.LoadPersisted(filepath.Join(home, "forebrain.yaml"))
	require.NoError(t, err)
	entries := saved.Agents.Definitions["main"].LLMProviders
	require.Len(t, entries, 2)
	require.Empty(t, entries[1].APIKey, "a login-backed row carries no key")
	rows := providersGet(t, s)
	require.Len(t, rows, 2)
	require.Equal(t, false, rows[1]["api_key_set"])
}

// A key-mode provider cannot be left keyless when it is the primary: the
// gateway would save and then refuse to start. The existing completeness rule
// answers it — the page must not grow a copy of it — and the file stays as it
// was.
func TestProvidersDTOKeyClearOnThePrimaryIsRefused(t *testing.T) {
	s, home := providersDTOServer(t)
	cfgPath := filepath.Join(home, "forebrain.yaml")
	seed := `agents:
  definitions:
    main:
      primary: true
      llm_providers:
      - provider: deepseek
        model: deepseek-chat
        api_key: ${DEEPSEEK_API_KEY}
        base_url: https://api.deepseek.com
`
	require.NoError(t, os.WriteFile(cfgPath, []byte(seed), 0o600))
	before, err := os.ReadFile(cfgPath)
	require.NoError(t, err)

	rr := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","base_url":"https://api.deepseek.com","models":["deepseek-chat"],"api_key":""}
	]}`)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "needs api_key")

	after, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "a refused clear writes nothing")
}

// A typed secret is the most explicit statement a client can make, so it wins
// over an empty api_key travelling in the same row: the key is stored in .env
// and the yaml keeps the reference. Nothing is cleared.
func TestProvidersDTOTypedSecretWinsOverAnEmptyReference(t *testing.T) {
	s, home := providersDTOServer(t)
	rr := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","base_url":"https://api.deepseek.com","models":["m"],"api_key":"","api_key_plain":"sk-typed-1234"}
	]}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	cfgRaw, err := os.ReadFile(filepath.Join(home, "forebrain.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(cfgRaw), "${DEEPSEEK_API_KEY}")
	require.NotContains(t, string(cfgRaw), "sk-typed-1234")
	envRaw, err := os.ReadFile(filepath.Join(home, ".env"))
	require.NoError(t, err)
	require.Contains(t, string(envRaw), "sk-typed-1234")

	rows := providersGet(t, s)
	require.Len(t, rows, 1)
	require.Equal(t, true, rows[0]["api_key_set"])
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

// The settings page never sees a stored key — it shows a hint and sends
// api_key_plain only for a key the user just typed — so its rows carry no key
// and the handler keeps the stored one per provider. For a login-backed row
// that stored value is a credential lent by another provider (the /connect bug),
// and keeping it alive would keep the login disabled: the save must drop it
// while the provider that owns the reference keeps its own.
func TestProvidersDTOHealsALoginRowCarryingAnotherProvidersKey(t *testing.T) {
	s, home := providersDTOServer(t)
	seed := `agents:
  definitions:
    main:
      primary: true
      llm_providers:
      - provider: deepseek
        model: deepseek-chat
        api_key: ${DEEPSEEK_API_KEY}
        base_url: https://api.deepseek.com
      - provider: opencode-go
        model: minimax-m3.1-flash-free
        api_key: ${DEEPSEEK_API_KEY}
        base_url: https://opencode.ai/inference/go
`
	require.NoError(t, os.WriteFile(filepath.Join(home, "forebrain.yaml"), []byte(seed), 0o600))

	// What the table sends for rows whose key the user did not touch.
	rr := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","base_url":"https://api.deepseek.com","models":["deepseek-chat"]},
		{"provider":"opencode-go","base_url":"https://opencode.ai/inference/go","models":["minimax-m3.1-flash-free"]}
	]}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	cfgRaw, err := os.ReadFile(filepath.Join(home, "forebrain.yaml"))
	require.NoError(t, err)
	saved, err := appcfg.LoadPersisted(filepath.Join(home, "forebrain.yaml"))
	require.NoError(t, err)
	entries := saved.Agents.Definitions["main"].LLMProviders
	require.Len(t, entries, 2)
	require.Equal(t, "deepseek", entries[0].Provider)
	require.Equal(t, "${DEEPSEEK_API_KEY}", entries[0].APIKey, "the owner keeps its own key")
	require.Equal(t, "opencode-go", entries[1].Provider)
	require.Empty(t, entries[1].APIKey, "the login row must not keep the lent key")
	require.Contains(t, string(cfgRaw), "${DEEPSEEK_API_KEY}")
	// The row still reports a key on disk? It must not: the hint follows the
	// stored value, so a healed row reads as keyless to the next page load.
	rows := providersGet(t, s)
	require.Len(t, rows, 2)
	require.Equal(t, false, rows[1]["api_key_set"])
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

// workspaceTreeServer builds a Server whose file view is one conversation's,
// filed under a project rooted at one temp dir. The conversation id the tree
// and snippet requests name is returned alongside the workspace.
func workspaceTreeServer(t *testing.T) (*Server, string, string) {
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
	s := &Server{Home: home, Env: &process.Environment{Deps: run.Deps{AppCfg: cfg}}}
	return s, ws, scopeServerToRoot(t, s, home, ws)
}

// scopeServerToRoot files s's file view under a project rooted at root: one
// conversation bound to it, returned as the id the tree, snippet and @ requests
// carry. The file view resolves its root from the session's project, so a
// request names the conversation the browser has open rather than the agent's
// workspace.
func scopeServerToRoot(t *testing.T, s *Server, home, root string) string {
	t.Helper()
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	projects := state.NewProjectStore(db, "main")
	project, err := projects.Create(ctx, state.CreateProjectInput{Name: "ws", Root: root})
	require.NoError(t, err)
	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(ctx, "ws-session", "ws-session"))
	require.NoError(t, projects.BindSession(ctx, "ws-session", project.ID))
	s.Sessions = sessions
	s.Projects = projects
	return "ws-session"
}

func getWorkspaceTree(t *testing.T, s *Server, sid, path string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/api/workspace/tree?session_id=" + sid
	if path != "" {
		target += "&path=" + path
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	s.handleWorkspaceTree(rr, req)
	return rr
}

func TestWorkspaceTreeListsOneLevelDirectoriesFirst(t *testing.T) {
	s, ws, sid := workspaceTreeServer(t)
	for _, name := range []string{"b.txt", "A.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(ws, name), []byte("x"), 0o644))
	}
	for _, name := range []string{"zdir", "adir", ".git"} {
		require.NoError(t, os.MkdirAll(filepath.Join(ws, name), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(ws, "adir", "nested.txt"), []byte("x"), 0o644))

	rr := getWorkspaceTree(t, s, sid, "")
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
	s, _, sid := workspaceTreeServer(t)
	rr := getWorkspaceTree(t, s, sid, "../")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "path is outside the workspace")
}

func TestWorkspaceTreeHidesSymlinksLeavingWorkspace(t *testing.T) {

	s, ws, sid := workspaceTreeServer(t)
	require.NoError(t, os.WriteFile(filepath.Join(ws, "real.txt"), []byte("x"), 0o644))
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "sub"), 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(ws, "out")))
	// A link whose target is inside the workspace stays listed, following the
	// target's type.
	require.NoError(t, os.Symlink(filepath.Join(ws, "real.txt"), filepath.Join(ws, "in")))

	rr := getWorkspaceTree(t, s, sid, "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.NotContains(t, rr.Body.String(), `"name":"out"`)
	require.Contains(t, rr.Body.String(), `"name":"in"`)

	rr = getWorkspaceTree(t, s, sid, "out")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	s2, ws2, sid2 := workspaceTreeServer(t)
	require.NoError(t, os.MkdirAll(filepath.Join(ws2, "target"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(ws2, "target"), filepath.Join(ws2, "in")))
	rr = getWorkspaceTree(t, s2, sid2, "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"name":"in"`)
	require.Contains(t, rr.Body.String(), `"is_dir":true`)
}

func TestWorkspaceTreeMissingAndFilePaths(t *testing.T) {
	s, ws, sid := workspaceTreeServer(t)
	require.NoError(t, os.WriteFile(filepath.Join(ws, "A.txt"), []byte("x"), 0o644))

	rr := getWorkspaceTree(t, s, sid, "missing")
	require.Equal(t, http.StatusNotFound, rr.Code)

	rr = getWorkspaceTree(t, s, sid, "A.txt")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "not a directory")
}

func TestWorkspaceTreeEmptyDirectory(t *testing.T) {
	s, ws, sid := workspaceTreeServer(t)
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "empty"), 0o755))
	rr := getWorkspaceTree(t, s, sid, "empty")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"records":[]`)
}

func TestWorkspaceSnippetRejectsSymlinkEscape(t *testing.T) {

	s, ws, sid := workspaceTreeServer(t)
	secret := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(secret, "secret.txt"), []byte("secret"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(secret, "secret.txt"), filepath.Join(ws, "leak.txt")))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "..notes.md"), []byte("dots are a legal name"), 0o644))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/workspace/snippet?session_id="+sid+"&path=leak.txt", nil)
	s.handleWorkspaceSnippet(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "path is outside the workspace")
	require.NotContains(t, rr.Body.String(), "secret")

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/workspace/snippet?session_id="+sid+"&path=..notes.md", nil)
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

// Every job belongs to a project, and a project's listing shows only its own.
func TestCronJobsListIsPerProject(t *testing.T) {
	s := cronTestServer(t)
	db := s.RunRT.DB
	store := state.NewProjectStore(db, "main")
	first := cronTestProjectID(t, s)
	second, err := store.Create(context.Background(), state.CreateProjectInput{Name: "second", Root: t.TempDir()})
	require.NoError(t, err)

	created := cronRequest(t, s, http.MethodPost, "/api/projects/"+first+"/cron/jobs", map[string]any{
		"name": "first job", "schedule": "every 2h", "prompt": "work",
	}, s.handleCronJobs, "id", first)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())

	other := cronRequest(t, s, http.MethodPost, "/api/projects/"+second.ID+"/cron/jobs", map[string]any{
		"name": "second job", "schedule": "every 3h", "prompt": "work",
	}, s.handleCronJobs, "id", second.ID)
	require.Equal(t, http.StatusOK, other.Code, other.Body.String())

	decode := func(rr *httptest.ResponseRecorder) []state.CronJob {
		var list struct {
			Records []state.CronJob `json:"records"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
		return list.Records
	}

	firstRows := decode(cronRequest(t, s, http.MethodGet, "/api/projects/"+first+"/cron/jobs", nil, s.handleCronJobs, "id", first))
	require.Len(t, firstRows, 1)
	require.Equal(t, "first job", firstRows[0].Name)
	require.Equal(t, first, firstRows[0].ProjectID)

	secondRows := decode(cronRequest(t, s, http.MethodGet, "/api/projects/"+second.ID+"/cron/jobs", nil, s.handleCronJobs, "id", second.ID))
	require.Len(t, secondRows, 1)
	require.Equal(t, "second job", secondRows[0].Name)

	// Someone else's project, or one that does not exist, is not addressable
	// from this agent.
	theirs, err := state.NewProjectStore(db, "someone-else").Create(context.Background(), state.CreateProjectInput{Name: "theirs", Root: t.TempDir()})
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, cronRequest(t, s, http.MethodGet, "/api/projects/"+theirs.ID+"/cron/jobs", nil, s.handleCronJobs, "id", theirs.ID).Code)
	require.Equal(t, http.StatusNotFound, cronRequest(t, s, http.MethodGet, "/api/projects/missing/cron/jobs", nil, s.handleCronJobs, "id", "missing").Code)
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

// The cost summary carries the output rate /status computes: the same paired
// query (a run with tokens but no recorded time stays out of both sides) and
// the same one-decimal algorithm. A session with no recorded model-call time
// answers zero, which the card reads as "show no rate row".
func TestCostSummaryCarriesOutputRate(t *testing.T) {
	ctx := context.Background()
	s := cronTestServer(t)
	s.Sessions = state.NewSessionStore(s.Env.SQL, "main")
	seedRun(t, s.RunRT, "s1", "r1")
	seedRun(t, s.RunRT, "s1", "r2")
	require.NoError(t, s.RunRT.SetRunUsage(ctx, "r1", state.LastRunUsage{CompletionTokens: 500, LLMMs: 25_000}))
	require.NoError(t, s.RunRT.SetRunUsage(ctx, "r2", state.LastRunUsage{CompletionTokens: 9_999}))

	rec := cronRequest(t, s, http.MethodGet, "/api/chat/sessions/s1/cost-summary", nil, s.handleSessionCostSummary, "id", "s1")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, float64(20), got["output_tokens_per_second"])
	require.Equal(t, float64(25_000), got["generation_ms"])

	// No run recorded a time: the fields answer their zero values.
	seedRun(t, s.RunRT, "s2", "r3")
	require.NoError(t, s.RunRT.SetRunUsage(ctx, "r3", state.LastRunUsage{CompletionTokens: 700}))
	rec = cronRequest(t, s, http.MethodGet, "/api/chat/sessions/s2/cost-summary", nil, s.handleSessionCostSummary, "id", "s2")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got = nil
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, float64(0), got["output_tokens_per_second"])
	require.Equal(t, float64(0), got["generation_ms"])
}

// The cost summary carries the latest turn's attempt ledger (plan 165): what
// the run tried, in the order it happened — the 503 with its wait, then the
// attempt that answered. The turn's tree (plan 182) counts the subagents the
// answer dispatched, each row's run_id naming whose request it was.
func TestSessionCostSummaryIncludesTheLatestRunAttempts(t *testing.T) {
	ctx := context.Background()
	s := cronTestServer(t)
	s.Sessions = state.NewSessionStore(s.Env.SQL, "main")
	seedRun(t, s.RunRT, "s1", "r1")
	input, output := int64(120), int64(30)
	require.NoError(t, s.RunRT.InsertLLMAttempt(ctx, state.LLMAttempt{
		RunID: "r1", Call: 1, Attempt: 1, Status: 503, Error: "model overloaded",
		WaitMs: 1000, ElapsedMs: 240, Outcome: state.LLMAttemptFailed,
	}))
	require.NoError(t, s.RunRT.InsertLLMAttempt(ctx, state.LLMAttempt{
		RunID: "r1", Call: 1, Attempt: 2, ElapsedMs: 1800,
		InputTokens: &input, OutputTokens: &output, Outcome: state.LLMAttemptSuccess,
	}))

	rec := cronRequest(t, s, http.MethodGet, "/api/chat/sessions/s1/cost-summary", nil, s.handleSessionCostSummary, "id", "s1")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Attempts []state.LLMAttempt `json:"attempts"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	if len(got.Attempts) != 2 {
		t.Fatalf("attempts = %+v, want the failed try and the success", got.Attempts)
	}
	first, second := got.Attempts[0], got.Attempts[1]
	if first.Attempt != 1 || first.Status != 503 || first.Outcome != state.LLMAttemptFailed || first.WaitMs != 1000 {
		t.Fatalf("first attempt = %+v, want attempt 1 status 503 failed wait 1000", first)
	}
	if second.Attempt != 2 || second.Outcome != state.LLMAttemptSuccess || second.InputTokens == nil || *second.InputTokens != 120 {
		t.Fatalf("second attempt = %+v, want attempt 2 success with the response's usage", second)
	}

	// The ledger counts the agents the answer dispatched: a subagent run
	// under the same primary run contributes its own row, run_id naming
	// whose request it was. seedRun leaves updated_at at 0 and this session
	// holds a single primary run, so no same-second tie enters the pick.
	sub, err := s.RunRT.CreateSubagentRun(ctx, "r1", "s1", "verify the diff")
	if err != nil {
		t.Fatal(err)
	}
	require.NoError(t, s.RunRT.InsertLLMAttempt(ctx, state.LLMAttempt{
		RunID: sub.ID, Call: 1, Attempt: 1, Status: 500, Error: "provider 500",
		ElapsedMs: 90, Outcome: state.LLMAttemptFailed,
	}))
	rec = cronRequest(t, s, http.MethodGet, "/api/chat/sessions/s1/cost-summary", nil, s.handleSessionCostSummary, "id", "s1")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got = struct {
		Attempts []state.LLMAttempt `json:"attempts"`
	}{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	if len(got.Attempts) != 3 {
		t.Fatalf("attempts = %+v, want the run's two rows plus the subagent's", got.Attempts)
	}
	wantRunIDs := []string{"r1", "r1", sub.ID}
	for i, row := range got.Attempts {
		if row.RunID != wantRunIDs[i] {
			t.Fatalf("attempt %d run_id = %s, want %s: rows carry whose request they were", i, row.RunID, wantRunIDs[i])
		}
	}
	if third := got.Attempts[2]; third.Status != 500 || third.Outcome != state.LLMAttemptFailed {
		t.Fatalf("third attempt = %+v, want the subagent's failed try", third)
	}
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

// A paged read of /api/v1/lsp answers one page of the servers with the count,
// the search reads the id and the languages, and the snapshot's own fields
// stay on top; without "page" the endpoint keeps the whole-snapshot answer it
// always gave.
func TestLSPSnapshotPagesTheServers(t *testing.T) {
	ctl := &lspControlDouble{snapshot: event.LSPSnapshot{
		ProjectRoot:    "/proj",
		Trusted:        true,
		FeatureEnabled: true,
		ProjectNotes:   []string{"project file ignored"},
		Servers: []event.LSPServerStatus{
			{ID: "gopls", Languages: []string{"go"}, State: event.LSPStateReady},
			{ID: "pyright", Languages: []string{"python"}, State: event.LSPStateReady},
			{ID: "tsls", Languages: []string{"typescript", "javascript"}, State: event.LSPStateReady},
		},
	}}
	s := &Server{Runner: &run.Runner{Deps: &run.Deps{CodeIntelControl: ctl}}}

	type pageAnswer struct {
		ProjectRoot  string                  `json:"project_root"`
		Trusted      bool                    `json:"trusted"`
		FeatureState bool                    `json:"feature_enabled"`
		ProjectNotes []string                `json:"project_notes"`
		Servers      []event.LSPServerStatus `json:"servers"`
		Total        int                     `json:"total"`
		Page         int                     `json:"page"`
		PageSize     int                     `json:"page_size"`
	}
	read := func(target string) pageAnswer {
		t.Helper()
		rec := httptest.NewRecorder()
		s.handleLSPSnapshot(rec, httptest.NewRequest(http.MethodGet, target, nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var answer pageAnswer
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &answer))
		return answer
	}

	second := read("/api/v1/lsp?page=2&page_size=2")
	require.Equal(t, 3, second.Total)
	require.Equal(t, 2, second.Page)
	require.Equal(t, 2, second.PageSize)
	// The snapshot's own fields answer on top of the page, so a surface that
	// read them before still reads them.
	require.Equal(t, "/proj", second.ProjectRoot)
	require.True(t, second.Trusted)
	require.True(t, second.FeatureState)
	require.Equal(t, []string{"project file ignored"}, second.ProjectNotes)
	require.Len(t, second.Servers, 1)
	require.Equal(t, "tsls", second.Servers[0].ID)

	// The search reads the languages the table lists, not just the id.
	byLanguage := read("/api/v1/lsp?page=1&q=PYTHON")
	require.Equal(t, 1, byLanguage.Total)
	require.Len(t, byLanguage.Servers, 1)
	require.Equal(t, "pyright", byLanguage.Servers[0].ID)

	// Without "page" the answer is the whole snapshot, exactly as before: no
	// count, no page, every server, whatever the search says.
	rec := httptest.NewRecorder()
	s.handleLSPSnapshot(rec, httptest.NewRequest(http.MethodGet, "/api/v1/lsp?q=python", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var whole event.LSPSnapshot
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &whole))
	require.Len(t, whole.Servers, 3)
	require.NotContains(t, rec.Body.String(), "total")
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

// TestProjectLSPConsentDecideOnlyTouchesNamedEntries pins the project page's
// per-row decision for language servers: the named entry is decided, the other
// one keeps awaiting its own, and the same call with the other answer takes a
// denied entry back.
func TestProjectLSPConsentDecideOnlyTouchesNamedEntries(t *testing.T) {
	s, p, _ := projectPagesServer(t)
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, ".forebrain", "lsp_servers.yaml"), []byte(
		"servers:\n"+
			"  first: {command: /bin/first}\n"+
			"  second: {command: /bin/second}\n"), 0o644))

	statuses := func() map[string]string {
		t.Helper()
		rec := cronRequest(t, s, http.MethodGet, "/api/v1/projects/"+p.ID+"/lsp?page=1&page_size=10", nil,
			s.handleProjectLSPPreview, "id", p.ID)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		out := map[string]string{}
		for _, item := range decodeBody(t, rec.Body.Bytes())["rows"].([]any) {
			row := item.(map[string]any)
			out[fmt.Sprint(row["id"])] = fmt.Sprint(row["status"])
		}
		return out
	}
	// The fingerprint a row carries is the one a decide request must send
	// back, so the two read each other here: read from the row, returned for
	// the click.
	rowFingerprint := func(id string) string {
		t.Helper()
		rec := cronRequest(t, s, http.MethodGet, "/api/v1/projects/"+p.ID+"/lsp?page=1&page_size=10", nil,
			s.handleProjectLSPPreview, "id", p.ID)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		for _, item := range decodeBody(t, rec.Body.Bytes())["rows"].([]any) {
			row := item.(map[string]any)
			if fmt.Sprint(row["id"]) == id {
				fp := fmt.Sprint(row["fingerprint"])
				require.NotEmpty(t, fp, "row %q carries no fingerprint", id)
				return fp
			}
		}
		t.Fatalf("no row for %q", id)
		return ""
	}

	rec := cronRequest(t, s, http.MethodPost, "/api/v1/projects/"+p.ID+"/lsp/consent",
		map[string]any{"allow": []string{}, "decide": []string{"first"},
			"fingerprints": []any{map[string]any{"name": "first", "fingerprint": rowFingerprint("first")}}},
		s.handleProjectLSPConsent, "id", p.ID)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, map[string]string{"first": "denied", "second": "pending"}, statuses())

	rec = cronRequest(t, s, http.MethodPost, "/api/v1/projects/"+p.ID+"/lsp/consent",
		map[string]any{"allow": []string{"first"}, "decide": []string{"first"},
			"fingerprints": []any{map[string]any{"name": "first", "fingerprint": rowFingerprint("first")}}},
		s.handleProjectLSPConsent, "id", p.ID)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, map[string]string{"first": "allowed", "second": "pending"}, statuses())
}

// TestProjectLSPConsentDecideRefusesAStaleFingerprint is the language-server
// twin of the MCP one: the fingerprint travels with the row, a click against
// one the file no longer matches is refused whole — 409, nothing recorded,
// the entry still pending — and the refreshed fingerprint goes through.
func TestProjectLSPConsentDecideRefusesAStaleFingerprint(t *testing.T) {
	s, p, _ := projectPagesServer(t)
	serversPath := filepath.Join(p.Root, ".forebrain", "lsp_servers.yaml")
	writeServers := func(body string) {
		t.Helper()
		require.NoError(t, os.WriteFile(serversPath, []byte(body), 0o644))
	}
	writeServers("servers:\n  first: {command: /bin/first}\n  second: {command: /bin/second}\n")

	rowFingerprint := func(id string) string {
		t.Helper()
		rec := cronRequest(t, s, http.MethodGet, "/api/v1/projects/"+p.ID+"/lsp?page=1&page_size=10", nil,
			s.handleProjectLSPPreview, "id", p.ID)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		for _, item := range decodeBody(t, rec.Body.Bytes())["rows"].([]any) {
			row := item.(map[string]any)
			if fmt.Sprint(row["id"]) == id {
				return fmt.Sprint(row["fingerprint"])
			}
		}
		t.Fatalf("no row for %q", id)
		return ""
	}
	statuses := func() map[string]string {
		t.Helper()
		rec := cronRequest(t, s, http.MethodGet, "/api/v1/projects/"+p.ID+"/lsp?page=1&page_size=10", nil,
			s.handleProjectLSPPreview, "id", p.ID)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		out := map[string]string{}
		for _, item := range decodeBody(t, rec.Body.Bytes())["rows"].([]any) {
			row := item.(map[string]any)
			out[fmt.Sprint(row["id"])] = fmt.Sprint(row["status"])
		}
		return out
	}

	// The page read first; the file moves under it before the click.
	shown := rowFingerprint("first")
	require.NotEmpty(t, shown)
	writeServers("servers:\n  first: {command: /bin/rewritten}\n  second: {command: /bin/second}\n")

	rec := cronRequest(t, s, http.MethodPost, "/api/v1/projects/"+p.ID+"/lsp/consent",
		map[string]any{"allow": []string{"first"}, "decide": []string{"first"},
			"fingerprints": []any{map[string]any{"name": "first", "fingerprint": shown}}},
		s.handleProjectLSPConsent, "id", p.ID)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var refused struct {
		Error string   `json:"error"`
		Code  string   `json:"code"`
		Stale []string `json:"stale"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &refused))
	require.Equal(t, "consent_stale", refused.Code)
	require.Equal(t, []string{"first"}, refused.Stale)
	require.NotEmpty(t, refused.Error)
	// Nothing was recorded: first is still awaiting its decision.
	require.Equal(t, map[string]string{"first": "pending", "second": "pending"}, statuses())

	// The page's own recovery: reload the rows, send the fingerprint they now
	// carry, and the same click goes through.
	refreshed := rowFingerprint("first")
	require.NotEqual(t, shown, refreshed)
	rec = cronRequest(t, s, http.MethodPost, "/api/v1/projects/"+p.ID+"/lsp/consent",
		map[string]any{"allow": []string{"first"}, "decide": []string{"first"},
			"fingerprints": []any{map[string]any{"name": "first", "fingerprint": refreshed}}},
		s.handleProjectLSPConsent, "id", p.ID)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, map[string]string{"first": "allowed", "second": "pending"}, statuses())
}

// TestProjectLSPConsentDecideRequiresFingerprints: a request that decides
// named entries without saying what it was shown for them decides nothing.
func TestProjectLSPConsentDecideRequiresFingerprints(t *testing.T) {
	s, p, _ := projectPagesServer(t)
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, ".forebrain", "lsp_servers.yaml"), []byte(
		"servers:\n  first: {command: /bin/first}\n"), 0o644))

	rec := cronRequest(t, s, http.MethodPost, "/api/v1/projects/"+p.ID+"/lsp/consent",
		map[string]any{"allow": []string{"first"}, "decide": []string{"first"}}, s.handleProjectLSPConsent, "id", p.ID)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `fingerprint required for "first"`)

	// Nothing was recorded: first is still awaiting its decision.
	rec = cronRequest(t, s, http.MethodGet, "/api/v1/projects/"+p.ID+"/lsp?page=1&page_size=10", nil,
		s.handleProjectLSPPreview, "id", p.ID)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rows := decodeBody(t, rec.Body.Bytes())["rows"].([]any)
	require.Len(t, rows, 1)
	require.Equal(t, "pending", fmt.Sprint(rows[0].(map[string]any)["status"]))
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

// Plan 017: a reloaded subagent reads its words from its own worker session's
// transcript, so the messages endpoint must serve a worker session of this
// agent - and still refuse another agent's conversation.
func TestChatMessagesReadsOwnAgentSubagentWorkerSession(t *testing.T) {
	ctx := context.Background()
	s := cronTestServer(t)
	s.Sessions = state.NewSessionStore(s.RunRT.DB, "main")
	// The conversation that dispatched the subagent.
	if err := s.Sessions.Ensure(ctx, "s1", "s1"); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	// The subagent's own worker session: born from s1, owned by the same agent.
	if err := s.Sessions.EnsureAt(ctx, "worker-1", "worker", state.SessionBirth{
		Source: state.SessionSourceSubagent, ParentSessionID: "s1",
	}); err != nil {
		t.Fatalf("ensure worker session: %v", err)
	}
	if _, err := s.Sessions.AppendStructuredMessage(ctx, "worker-1", "assistant", "the worker's answer", "", "[]", "", "", "", "", state.MessageExecTiming{}); err != nil {
		t.Fatalf("append worker row: %v", err)
	}
	// Another agent's conversation: never reachable through this agent's server.
	other := state.NewSessionStore(s.RunRT.DB, "other")
	if err := other.Ensure(ctx, "other-session", "other"); err != nil {
		t.Fatalf("ensure other session: %v", err)
	}

	rec := cronRequest(t, s, http.MethodGet, "/api/chat/sessions/worker-1/messages", nil, s.handleChatMessages, "id", "worker-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("worker messages = %d %s", rec.Code, rec.Body.String())
	}
	require.Contains(t, rec.Body.String(), "the worker's answer")

	rec = cronRequest(t, s, http.MethodGet, "/api/chat/sessions/other-session/messages", nil, s.handleChatMessages, "id", "other-session")
	require.NotEqual(t, http.StatusOK, rec.Code, "another agent's session must not be served")
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
	// The session belongs to a project: a gateway process has no launch project
	// of its own, so the plan the approval asks about is resolved from the
	// session's project, not the base runner.
	projects := state.NewProjectStore(db, "main")
	project, err := projects.Create(ctx, state.CreateProjectInput{Name: "exitplan", Root: t.TempDir()})
	require.NoError(t, err)
	require.NoError(t, projects.BindSession(ctx, "s1", project.ID))
	s := &Server{
		Home: home, RunRT: runs, Actions: actions,
		Sessions: state.NewSessionStore(db, "main"),
		Projects: projects,
		Runner:   &run.Runner{Deps: &run.Deps{AppCfg: cfg}},
	}
	require.NoError(t, state.SetPlanForSession(s.stateRoot(), s.sessionProjectKey(ctx, "s1"), "s1", "# Plan\n\n1. Ship it."))
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
	// Retire the parked run's wait row: a run nothing is parked on has no
	// continuation to dispatch, so the approve answers without leaving a
	// resume goroutine behind to write the mode switch or run status into a
	// temp dir this test is already tearing down.
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

// The approval is already stored by the time the continuation is dispatched, so
// a client that went away between writing it and this lookup must not silently
// lose the resumed run: the lookup ignores the request's cancellation.
func TestResumeIfParkedStillDispatchesWhenTheRequestIsGone(t *testing.T) {
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{{
			Provider: "openai", Model: "gpt-5.1", APIKey: "sk-test", BaseURL: "http://127.0.0.1:1",
		}}},
	}}}
	s, _, _, actionID, _ := exitPlanServer(t, cfg)

	dispatched := make(chan string, 1)
	previous := dispatchGatewayResume
	dispatchGatewayResume = func(_ *Server, actionID string, _ bool) {
		dispatched <- actionID
	}
	t.Cleanup(func() { dispatchGatewayResume = previous })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.resumeGatewayRunIfParked(ctx, actionID, false)

	select {
	case got := <-dispatched:
		require.Equal(t, actionID, got)
	case <-time.After(2 * time.Second):
		t.Fatal("a request that went away must not drop the continuation of an answered action")
	}
}

// Nothing is parked on the action, so there is no continuation to dispatch: the
// lookup finds no wait row and no goroutine is left behind writing into stores
// the surface may be closing.
func TestResumeIfParkedDispatchesNothingWithoutAParkedRun(t *testing.T) {
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{{
			Provider: "openai", Model: "gpt-5.1", APIKey: "sk-test", BaseURL: "http://127.0.0.1:1",
		}}},
	}}}
	s, runs, _, actionID, _ := exitPlanServer(t, cfg)

	runID, _, err := runs.FindRunByAction(context.Background(), actionID)
	require.NoError(t, err)
	require.NotEmpty(t, runID)
	require.NoError(t, runs.ClearWait(context.Background(), runID))

	dispatched := make(chan string, 1)
	previous := dispatchGatewayResume
	dispatchGatewayResume = func(_ *Server, actionID string, _ bool) {
		dispatched <- actionID
	}
	t.Cleanup(func() { dispatchGatewayResume = previous })

	s.resumeGatewayRunIfParked(context.Background(), actionID, false)

	select {
	case got := <-dispatched:
		t.Fatalf("nothing is parked on %s, so nothing may be dispatched (got %s)", actionID, got)
	case <-time.After(200 * time.Millisecond):
	}
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
	// Retire the parked run's wait row so the retry has no continuation to
	// dispatch and cannot leave a resume goroutine racing this test's temp
	// dir cleanup.
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
	// A production runner always carries the controller: a subagent's channel
	// keeps its queue there, and a steer is only deliverable through it.
	runner := &run.Runner{Deps: &run.Deps{Home: home, AppCfg: cfg, AgentName: "main"}, Control: run.NewController()}
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
		// /compact is a built-in hidden from a subagent's view (plan 142): it
		// answers like any other command that belongs to the conversation.
		{"/compact", subagentViewCommandCode},
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

// A skill command typed in a subagent's web view is sent to that subagent as a
// steer with its skill selected. The registry lists skills beside the built-ins,
// so finding the name is not what tells the two apart: a registered skill was
// once refused as a conversation command.
func TestSubagentInputSendsARegisteredSkillCommand(t *testing.T) {
	s, ws := newSubagentViewTestServer(t)
	seedSubagentRecord(t, ws, "sid", "agent-1")
	release := holdSubagentRunning(t, s)

	turn.ResetDynamic()
	t.Cleanup(turn.ResetDynamic)
	require.NoError(t, turn.ReplaceDynamicSource("gw-test", []turn.DynamicCommand{{
		// The shape a real skill registers with: it takes the request inline.
		Command: turn.Command{
			Name: "probe-skill", Description: "probe", Category: turn.SkillCategory,
			AllowedSurfaces:    []turn.Surface{turn.SurfaceTUI, turn.SurfaceWebChat},
			SupportsInlineArgs: true, ArgumentHint: "[request]", Visibility: turn.VisibilityPublic,
		},
		SkillPath: "/skills/probe-skill/SKILL.md",
		Handler: func(turn.Context, string, []string) turn.Result {
			return turn.Result{Handled: true, ShouldContinueRun: true, ContinueInput: "use probe-skill: go", SkillName: "probe-skill", SkillPath: "/skills/probe-skill/SKILL.md"}
		},
	}}))

	req := subagentViewRequest(http.MethodPost, "sid", "agent-1", `{"message":"/probe-skill go"}`)
	w := httptest.NewRecorder()
	s.handleSubagentInput(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var out struct {
		Delivery string                   `json:"delivery"`
		Preview  turn.PendingInputPreview `json:"preview"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Equal(t, string(run.SubagentDeliverySteered), out.Delivery)
	require.Equal(t, []string{"use probe-skill: go"}, out.Preview.PendingSteers,
		"the skill's expansion is what the subagent is sent")
	close(release)
	require.Eventually(t, func() bool {
		return !run.SubagentRunning(s.Runner, "sid", "agent-1")
	}, 5*time.Second, 20*time.Millisecond, "the held execution never ended")
}

// holdSubagentRunning starts one execution of the seeded subagent and holds it
// open, so the endpoint tests talk to a subagent that is actually working — the
// only state in which its view takes input. The returned channel closes the
// execution.
func holdSubagentRunning(t *testing.T, s *Server) chan struct{} {
	t.Helper()
	hold := &holdingSubagentExecutor{release: make(chan struct{})}
	s.Runner.SubagentExecutor = hold
	require.NoError(t, run.StartSubagentAutoContinue(context.Background(), s.Runner, run.SubagentSurface{}, "sid", "agent-1", run.Input{Text: "start"}))
	require.Eventually(t, func() bool {
		return run.SubagentRunning(s.Runner, "sid", "agent-1")
	}, 5*time.Second, 20*time.Millisecond, "the subagent never started running")
	return hold.release
}

// holdingSubagentExecutor blocks one execution until its release channel
// closes, so a test can observe a subagent between its start and its first tool
// boundary.
type holdingSubagentExecutor struct{ release chan struct{} }

func (h *holdingSubagentExecutor) RunSubagentExec(ctx context.Context, _ run.SubagentExecRequest) (string, error) {
	select {
	case <-h.release:
		return "done", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (h *holdingSubagentExecutor) PersistSubagentTurn(context.Context, run.SubagentTurn) {}
func (h *holdingSubagentExecutor) SubagentExecutionStarting(context.Context, string)     {}
func (h *holdingSubagentExecutor) SubagentExecutionEnded(context.Context, run.SubagentExecutionEnd) {
}

// An idle subagent takes no messages: its view has no composer then, and a page
// that still sends one is refused with 409 so it can put the text where it
// belongs — in the conversation's composer — instead of losing it.
func TestSubagentInputRefusesAnIdleSubagent(t *testing.T) {
	s, ws := newSubagentViewTestServer(t)
	seedSubagentRecord(t, ws, "sid", "agent-1")

	req := subagentViewRequest(http.MethodPost, "sid", "agent-1", `{"message":"continue"}`)
	w := httptest.NewRecorder()
	s.handleSubagentInput(w, req)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	var out struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Equal(t, subagentNotRunningCode, out.Code)
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
// one session's plan under the session's own project key, even when another
// conversation in the same project wrote a plan more recently. The process has
// no launch project of its own, so the key comes from the session, not the base
// runner.
func TestSessionPlanMarkdownReturnsOnlyThatSessionsPlan(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	projects := state.NewProjectStore(db, "main")
	project, err := projects.Create(ctx, state.CreateProjectInput{Name: "planmd", Root: t.TempDir()})
	require.NoError(t, err)
	require.NoError(t, state.NewSessionStore(db, "main").Ensure(ctx, "s1", "s1"))
	require.NoError(t, projects.BindSession(ctx, "s1", project.ID))

	s := &Server{Home: home, Projects: projects}
	key := s.sessionProjectKey(ctx, "s1")
	require.Equal(t, state.ProjectKeyForRoot(project.Root), key)
	require.NoError(t, state.SetPlanForSession(s.stateRoot(), key, "s1", "# Mine"))

	// A second session's plan, written later: invisible to s1's plan-md.
	other := state.PlanPathForSession(s.stateRoot(), key, "s2")
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

// A conversation that replans writes a new plan file each time, so its plan
// history is its directory. plans hands that history to the workbench newest
// first, with each document's text, and with paths relative to the state root
// so the user's home directory never reaches the browser.
func TestSessionPlansListsAConversationsHistory(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	projects := state.NewProjectStore(db, "main")
	project, err := projects.Create(ctx, state.CreateProjectInput{Name: "plans", Root: t.TempDir()})
	require.NoError(t, err)
	require.NoError(t, state.NewSessionStore(db, "main").Ensure(ctx, "s1", "s1"))
	require.NoError(t, projects.BindSession(ctx, "s1", project.ID))

	s := &Server{Home: home, Projects: projects, Sessions: state.NewSessionStore(db, "main")}
	key := s.sessionProjectKey(ctx, "s1")
	require.Equal(t, state.ProjectKeyForRoot(project.Root), key)

	dir := state.PlanDirForSession(s.stateRoot(), key, "s1")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	for name, content := range map[string]string{"older.md": "# Older", "newer.md": "# Newer"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content+"\n"), 0o600))
		mt := t0
		if name == "newer.md" {
			mt = t0.Add(time.Minute)
		}
		require.NoError(t, os.Chtimes(filepath.Join(dir, name), mt, mt))
	}

	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/s1/plans", nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{
		{Key: "id", Value: "s1"},
	}))
	rr := httptest.NewRecorder()
	s.handleSessionPlans(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var body struct {
		Plans []struct {
			Name      string `json:"name"`
			Path      string `json:"path"`
			UpdatedAt int64  `json:"updated_at"`
			Markdown  string `json:"markdown"`
		} `json:"plans"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Len(t, body.Plans, 2)

	newest, older := body.Plans[0], body.Plans[1]
	require.Equal(t, "newer.md", newest.Name)
	require.Equal(t, "# Newer", newest.Markdown)
	require.Equal(t, t0.Add(time.Minute).Unix(), newest.UpdatedAt)
	require.False(t, filepath.IsAbs(newest.Path), "path must not be absolute: %q", newest.Path)
	require.Equal(t, "plans/"+key+"/s1/newer.md", newest.Path)

	require.Equal(t, "older.md", older.Name)
	require.Equal(t, "# Older", older.Markdown)
	require.Equal(t, "plans/"+key+"/s1/older.md", older.Path)
}

// A conversation's plans are its own: an id this agent has no session for is
// 404 on both plan reads. plan-md used to answer 200 with an empty document,
// which is indistinguishable from "this conversation never planned".
func TestSessionPlanEndpointsRejectUnknownSession(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	s := &Server{
		Home:     home,
		Projects: state.NewProjectStore(db, "main"),
		Sessions: state.NewSessionStore(db, "main"),
	}

	for _, tc := range []struct {
		path    string
		handler http.HandlerFunc
	}{
		{"/api/chat/sessions/does-not-exist/plans", s.handleSessionPlans},
		{"/api/chat/sessions/does-not-exist/plan-md", s.handleSessionPlanMarkdown},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{
				{Key: "id", Value: "does-not-exist"},
			}))
			rr := httptest.NewRecorder()
			tc.handler(rr, req)
			require.Equal(t, http.StatusNotFound, rr.Code)
		})
	}
}

// The session-model endpoints serve one conversation's own choice and the
// options it may be switched to: the options are the agent's configured chain
// with their catalog-reported reasoning levels, a pair or an effort the
// options do not offer is refused with 400, and a valid write lands on the
// session's row only — the runner's published default stays untouched, so the
// change rides the next turn exactly as /model's live switch does.
func TestChatSessionModelEndpoints(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "forebrain.yaml")
	t.Setenv("FOREBRAIN_WEB_MODEL_TEST_KEY", "test-key")
	cfg := appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{
			{Provider: "zhipuai", Model: "glm-5.3-flash", APIKey: "${FOREBRAIN_WEB_MODEL_TEST_KEY}", BaseURL: "http://a.test/v1"},
			{Provider: "deepseek", Model: "deepseek-chat", APIKey: "${FOREBRAIN_WEB_MODEL_TEST_KEY}", BaseURL: "http://b.test/v1"},
			{Provider: "deepseek", Model: "deepseek-v4-pro", APIKey: "${FOREBRAIN_WEB_MODEL_TEST_KEY}", BaseURL: "http://c.test/v1"},
		}},
	}}}
	require.NoError(t, appcfg.Save(cfgPath, cfg))
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sess := state.NewSessionStore(db, "main")
	require.NoError(t, sess.Ensure(context.Background(), "s1", "s1"))
	env := &process.Environment{Root: home, ConfigPath: cfgPath, Deps: run.Deps{Home: home, AgentName: "main", AppCfg: &cfg, SessionStore: sess}}
	runner := &run.Runner{Deps: &env.Deps}
	env.Runner = runner
	s := &Server{Home: home, Runner: runner, Sessions: sess, Env: env}
	require.NoError(t, s.Env.ReloadConfig())

	modelCall := func(method string, body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		var reader io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			reader = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, "/api/chat/sessions/s1/model", reader)
		req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{
			{Key: "id", Value: "s1"},
		}))
		rec := httptest.NewRecorder()
		if method == http.MethodGet {
			s.handleChatSessionModelGet(rec, req)
		} else {
			s.handleChatSessionModelPut(rec, req)
		}
		return rec
	}

	// GET: a session that never chose reads the chain's first entry; a model
	// the catalog knows can reason carries /model's levels, one it knows
	// cannot carries none.
	rec := modelCall(http.MethodGet, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Effort   string `json:"effort"`
		Options  []struct {
			Provider       string   `json:"provider"`
			Model          string   `json:"model"`
			Efforts        []string `json:"efforts"`
			SupportsImages bool     `json:"supports_images"`
		} `json:"options"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "zhipuai", got.Provider)
	require.Equal(t, "glm-5.3-flash", got.Model)
	require.Len(t, got.Options, 3)
	require.Equal(t, []string{"low", "high", "max"}, got.Options[0].Efforts)
	require.Empty(t, got.Options[1].Efforts, "deepseek-chat does not reason, so it has no levels")
	// The chip marks the models the catalog knows take images: only
	// glm-5.3-flash does; deepseek-chat and deepseek-v4-pro are text-only.
	require.Equal(t, []bool{true, false, false},
		[]bool{got.Options[0].SupportsImages, got.Options[1].SupportsImages, got.Options[2].SupportsImages})
	require.Equal(t, []string{"low", "high", "max"}, got.Options[2].Efforts)
	publishedBefore := run.PrimaryModelSelection(runner)

	// A conversation that does not exist yet reads the same model and options
	// with no session to name — the new-chat page's chips draw from it.
	defaultRec := httptest.NewRecorder()
	s.handleChatModelDefault(defaultRec, httptest.NewRequest(http.MethodGet, "/api/chat/model", nil))
	require.Equal(t, http.StatusOK, defaultRec.Code, defaultRec.Body.String())
	var fresh struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Options  []struct {
			Model   string   `json:"model"`
			Efforts []string `json:"efforts"`
		} `json:"options"`
	}
	require.NoError(t, json.Unmarshal(defaultRec.Body.Bytes(), &fresh))
	require.Equal(t, got.Provider, fresh.Provider)
	require.Equal(t, got.Model, fresh.Model)
	require.Len(t, fresh.Options, 3)
	require.Equal(t, []string{"low", "high", "max"}, fresh.Options[0].Efforts)
	wrong := httptest.NewRecorder()
	s.handleChatModelDefault(wrong, httptest.NewRequest(http.MethodPost, "/api/chat/model", nil))
	require.Equal(t, http.StatusMethodNotAllowed, wrong.Code)

	// A pair the agent is not configured with is refused before any write.
	rec = modelCall(http.MethodPut, map[string]any{"provider": "openai", "model": "gpt-9"})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	// An effort the model does not offer is refused the same way — including a
	// level that exists in the catalog's vocabulary but not in this model's row.
	rec = modelCall(http.MethodPut, map[string]any{"provider": "zhipuai", "model": "glm-5.3-flash", "effort": "ultra"})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	rec = modelCall(http.MethodPut, map[string]any{"provider": "zhipuai", "model": "glm-5.3-flash", "effort": "xhigh"})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	_, hasRow, err := sess.SessionModelSelection(context.Background(), "s1")
	require.NoError(t, err)
	require.False(t, hasRow, "a refused write must not store anything")

	// A valid write is the session's own row: read back verbatim, while the
	// runner's published default stays with the chain's first entry.
	rec = modelCall(http.MethodPut, map[string]any{"provider": "zhipuai", "model": "glm-5.3-flash", "effort": "high"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"effort":"high"`)
	row, hasRow, err := sess.SessionModelSelection(context.Background(), "s1")
	require.NoError(t, err)
	require.True(t, hasRow)
	require.Equal(t, state.SessionModelSelection{Provider: "zhipuai", Model: "glm-5.3-flash", Effort: "high"}, row)
	require.Equal(t, publishedBefore, run.PrimaryModelSelection(runner), "the runner's published default must stay untouched")

	// An empty effort is the explicit "model default" pin, not a level.
	rec = modelCall(http.MethodPut, map[string]any{"provider": "zhipuai", "model": "glm-5.3-flash", "effort": ""})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	row, _, err = sess.SessionModelSelection(context.Background(), "s1")
	require.NoError(t, err)
	require.Equal(t, "", row.Effort)
}

// The web chip's levels come from the same capability question /model asks: an
// OpenCode model carries the levels models.json records for its own surface,
// trimmed to what the protocol's client can send.
// The new-chat page's model read follows the agent's most recently used
// model (owner 2026-10-23): a session row fresher than the chain's first
// entry is what the chips show, and the runner's published selection stays
// untouched — the row is a reading, not a switch.
func TestChatModelDefaultFollowsMostRecentlyUsed(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "forebrain.yaml")
	t.Setenv("FOREBRAIN_WEB_MODEL_TEST_KEY", "test-key")
	cfg := appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{
			{Provider: "zhipuai", Model: "glm-5.3-flash", APIKey: "${FOREBRAIN_WEB_MODEL_TEST_KEY}", BaseURL: "http://a.test/v1"},
			{Provider: "deepseek", Model: "deepseek-chat", APIKey: "${FOREBRAIN_WEB_MODEL_TEST_KEY}", BaseURL: "http://b.test/v1"},
		}},
	}}}
	require.NoError(t, appcfg.Save(cfgPath, cfg))
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sess := state.NewSessionStore(db, "main")
	require.NoError(t, sess.Ensure(context.Background(), "used", "used"))
	// Another conversation chose the chain's second entry — that row is the
	// most recently used one.
	_, err = sess.SaveSessionModelSelection(context.Background(), "used", state.SessionModelSelection{
		Provider: "deepseek", Model: "deepseek-chat", Effort: "",
	})
	require.NoError(t, err)

	env := &process.Environment{Root: home, ConfigPath: cfgPath, Deps: run.Deps{Home: home, AgentName: "main", AppCfg: &cfg, SessionStore: sess}}
	runner := &run.Runner{Deps: &env.Deps}
	env.Runner = runner
	s := &Server{Home: home, Runner: runner, Sessions: sess, Env: env}
	require.NoError(t, s.Env.ReloadConfig())

	rec := httptest.NewRecorder()
	s.handleChatModelDefault(rec, httptest.NewRequest(http.MethodGet, "/api/chat/model", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"provider":"deepseek"`)
	require.Contains(t, rec.Body.String(), `"model":"deepseek-chat"`)
	// The read moved nothing on the runner.
	require.Equal(t, "glm-5.3-flash", run.PrimaryModelSelection(runner).Model)
}

func TestSessionModelOptionsCarryOpenCodeEfforts(t *testing.T) {
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{{
			Provider: "opencode-go",
			Model:    "glm-5.3",
			Models:   appcfg.StringList{"glm-5.3", "minimax-m3", "qwen3.8-max"},
		}}},
	}}}
	options := (&Server{}).sessionModelOptions(cfg, "main")
	require.Len(t, options, 3)
	require.Equal(t, []string{"low", "high", "max"}, options[0].Efforts)
	require.Empty(t, options[1].Efforts, "a toggle-only model has no level to pick")
	require.Equal(t, []string{"low", "medium", "xhigh"}, options[2].Efforts, "the Anthropic client can send xhigh")
}

// TestRewindRoutes pins the plan-033 HTTP surface: the six new routes answer,
// and the removed rewind-last route is gone (a 404 on the real router).
func TestRewindRoutes(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	runs := &state.RunStore{DB: db}
	require.NoError(t, store.Ensure(ctx, "s1", "s1"))
	a := filepath.Join(t.TempDir(), "a.go")
	require.NoError(t, os.WriteFile(a, []byte("a1\n"), 0o644))
	rowID, err := store.Append(ctx, "s1", "user", "hello")
	require.NoError(t, err)
	require.NoError(t, store.RecordFileChange(ctx, state.FileChange{
		SessionID: "s1", AnchorMessageID: rowID, Path: a, ExistedBefore: true, AfterSHA256: "x",
	}))
	// A file the conversation created and that is gone from disk: reverting it
	// needs no snapshot writer, so the route is exercised without a tool state.
	gone := filepath.Join(t.TempDir(), "gone.go")
	require.NoError(t, store.RecordFileChange(ctx, state.FileChange{
		SessionID: "s1", AnchorMessageID: rowID, Path: gone, ExistedBefore: false,
	}))

	s := &Server{Sessions: store, RunRT: runs}
	srv := NewRestServer("")
	s.AttachExtraRoutes(srv)

	do := func(method, target string, body any) *httptest.ResponseRecorder {
		var reader io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			reader = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, target, reader)
		rec := httptest.NewRecorder()
		srv.Server.Handler.ServeHTTP(rec, req)
		return rec
	}

	require.Equal(t, http.StatusNotFound,
		do(http.MethodPost, "/api/chat/sessions/s1/rewind-last", nil).Code,
		"the removed rewind-last route must not answer")

	esc := url.QueryEscape(a)
	msgID := strconv.FormatInt(rowID, 10)
	rec := do(http.MethodGet, "/api/chat/sessions/s1/rewind-preview?message_id="+msgID, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), a)

	rec = do(http.MethodGet, "/api/chat/sessions/s1/file-changes", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), a)

	rec = do(http.MethodGet, "/api/chat/sessions/s1/file-changes/diff?path="+esc, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	// Chat-only needs no snapshot writer, so it runs without a tool state.
	rec = do(http.MethodPost, "/api/chat/sessions/s1/rewind", map[string]any{"message_id": rowID, "mode": "chat"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = do(http.MethodPost, "/api/chat/sessions/s1/rewind/undo", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = do(http.MethodPost, "/api/chat/sessions/s1/file-changes/revert", map[string]any{"path": gone})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// mcpWriteFixture is one gateway server over a real forebrain.yaml: the write
// endpoints edit a file, so the file has to exist and be reloadable.
type mcpWriteFixture struct {
	server    *Server
	cfgPath   string
	home      string
	agentName string
	projects  *state.ProjectStore
}

// mcpWriteProvider is the minimum a gateway configuration needs: every write
// reloads the configuration onto the serving runner, and a runner without a
// provider is not a runtime.
func mcpWriteProvider() []appcfg.AgentLLMProviderConfig {
	return []appcfg.AgentLLMProviderConfig{{
		Provider: "openai",
		Model:    "gpt-main",
		APIKey:   "${FOREBRAIN_MCP_WRITE_TEST_KEY}",
		BaseURL:  "http://127.0.0.1:9/v1",
	}}
}

// newMCPWriteFixture builds a gateway server over a real forebrain.yaml. The file
// is written through config.Save from the caller's own configuration, so the
// fixture stays a configuration rather than a string to keep valid by hand.
func newMCPWriteFixture(t *testing.T, mutate func(*appcfg.Root), agentName string, effective []appcfg.MCPServerConfig) *mcpWriteFixture {
	t.Helper()
	t.Setenv("FOREBRAIN_MCP_WRITE_TEST_KEY", "test-key")
	home := t.TempDir()
	cfgPath := filepath.Join(home, "forebrain.yaml")
	root := appcfg.Root{ApprovalsReviewer: "user"}
	root.Agents.Definitions = map[string]appcfg.AgentDefinition{
		"main": {Primary: true, LLMProviders: mcpWriteProvider()},
	}
	if mutate != nil {
		mutate(&root)
	}
	if err := appcfg.Save(cfgPath, root); err != nil {
		t.Fatalf("save fixture config: %v", err)
	}
	cfg, err := appcfg.LoadPersisted(cfgPath)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ws := filepath.Join(home, "workspace")
	runner := &run.Runner{Deps: &run.Deps{
		Home:          home,
		AgentName:     agentName,
		WorkspaceRoot: ws,
		ProjectRoot:   home,
		MCPServers:    effective,
		AppCfg:        &cfg,
	}}
	env := &process.Environment{
		Root:       home,
		ConfigPath: cfgPath,
		RawConfig:  cfg,
		Deps:       run.Deps{Home: home, AgentName: agentName, AppCfg: &cfg, WorkspaceRoot: ws},
		Runner:     runner,
	}
	s := &Server{Home: home, Runner: runner, Env: env, Projects: state.NewProjectStore(db, agentName)}
	if err := env.ReloadConfig(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	return &mcpWriteFixture{server: s, cfgPath: cfgPath, home: home, agentName: agentName, projects: s.Projects}
}

// sharedMCPFixture is the configuration most of these tests start from: one
// shared entry with a literal credential in a header, which is the value the read
// side has to mask.
func sharedMCPFixture(root *appcfg.Root) {
	root.Agents.Defaults.MCPServers = []appcfg.MCPServerConfig{{
		Name:           "shared-doc",
		Transport:      "streamable_http",
		URL:            "https://docs.test/mcp",
		Headers:        map[string]string{"Authorization": "Bearer eyJhbGciOiJSUzI1NiJ9"},
		RequestTimeout: 30,
	}}
}

func (f *mcpWriteFixture) config(t *testing.T) appcfg.Root {
	t.Helper()
	cfg, err := appcfg.LoadPersisted(f.cfgPath)
	if err != nil {
		t.Fatalf("read back %s: %v", f.cfgPath, err)
	}
	return cfg
}

func (f *mcpWriteFixture) shared(t *testing.T) []appcfg.MCPServerConfig {
	t.Helper()
	return f.config(t).Agents.Defaults.MCPServers
}

func findServer(t *testing.T, servers []appcfg.MCPServerConfig, name string) appcfg.MCPServerConfig {
	t.Helper()
	for _, srv := range servers {
		if mcp.SameServerName(srv.Name, name) {
			return srv
		}
	}
	t.Fatalf("no entry named %q in %+v", name, servers)
	return appcfg.MCPServerConfig{}
}

// TestMCPSharedListWriteCreatesUpdatesAndDeletes pins the lifecycle of one entry
// in the shared list: it lands in forebrain.yaml, an update leaves every field it
// did not mention alone, and a delete removes it.
func TestMCPSharedListWriteCreatesUpdatesAndDeletes(t *testing.T) {
	f := newMCPWriteFixture(t, sharedMCPFixture, "main", nil)

	rec := doJSON(t, f.server, http.MethodPost, "/api/v1/mcp/servers", map[string]any{
		"name":            "smart-doc",
		"transport":       "streamable_http",
		"target":          "http://192.168.73.51:8112/mcp",
		"enabled":         true,
		"startup_timeout": 10,
		"request_timeout": 30,
		"headers":         map[string]string{"Authorization": "Bearer ${SMART_DOC_TOKEN}"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	saved := findServer(t, f.shared(t), "smart-doc")
	if saved.URL != "http://192.168.73.51:8112/mcp" || saved.StartupTimeout != 10 || saved.RequestTimeout != 30 {
		t.Fatalf("saved = %+v", saved)
	}
	if got := saved.Headers["Authorization"]; got != "Bearer ${SMART_DOC_TOKEN}" {
		t.Fatalf("headers = %+v", saved.Headers)
	}
	if !appcfg.ServerEnabled(saved) {
		t.Fatalf("enabled: true must be stored: %+v", saved.Enabled)
	}

	// A field the client did not send is left alone: switching one entry off
	// must not disturb the two entries around it.
	rec = doJSON(t, f.server, http.MethodPut, "/api/v1/mcp/servers/smart-doc", map[string]any{"enabled": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	saved = findServer(t, f.shared(t), "smart-doc")
	if appcfg.ServerEnabled(saved) {
		t.Fatalf("enabled: false must be stored: %+v", saved.Enabled)
	}
	if saved.URL != "http://192.168.73.51:8112/mcp" || saved.RequestTimeout != 30 || saved.StartupTimeout != 10 {
		t.Fatalf("an unmentioned field must survive: %+v", saved)
	}
	untouched := findServer(t, f.shared(t), "shared-doc")
	if untouched.URL != "https://docs.test/mcp" || untouched.RequestTimeout != 30 {
		t.Fatalf("the other entry must be untouched: %+v", untouched)
	}

	rec = doJSON(t, f.server, http.MethodDelete, "/api/v1/mcp/servers/smart-doc", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if len(f.shared(t)) != 1 {
		t.Fatalf("shared list = %+v", f.shared(t))
	}
	if rec := doJSON(t, f.server, http.MethodDelete, "/api/v1/mcp/servers/smart-doc", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("deleting a missing entry: %d %s, want 404", rec.Code, rec.Body.String())
	}
}

// TestMCPServerWriteValidationAndConflicts pins the status each refusal reports:
// a bad field is 400, a name that already exists is 409, an entry that is not
// there is 404 — and a refused write changes nothing.
func TestMCPServerWriteValidationAndConflicts(t *testing.T) {
	f := newMCPWriteFixture(t, sharedMCPFixture, "main", nil)
	before := f.shared(t)

	cases := []struct {
		name   string
		method string
		target string
		body   map[string]any
		want   int
		text   string
	}{
		{
			name: "duplicate name", method: http.MethodPost, target: "/api/v1/mcp/servers",
			body: map[string]any{"name": "shared-doc", "transport": "stdio", "target": "/bin/x"},
			want: http.StatusConflict, text: "already configured",
		},
		{
			name: "unknown transport", method: http.MethodPost, target: "/api/v1/mcp/servers",
			body: map[string]any{"name": "new", "transport": "carrier-pigeon", "target": "/bin/x"},
			want: http.StatusBadRequest, text: "stdio or streamable_http",
		},
		{
			name: "no transport", method: http.MethodPost, target: "/api/v1/mcp/servers",
			body: map[string]any{"name": "new", "target": "/bin/x"},
			want: http.StatusBadRequest, text: "transport is required",
		},
		{
			name: "name with whitespace", method: http.MethodPost, target: "/api/v1/mcp/servers",
			body: map[string]any{"name": "two words", "transport": "stdio", "target": "/bin/x"},
			want: http.StatusBadRequest, text: "whitespace",
		},
		{
			name: "unclosed quote in the command", method: http.MethodPost, target: "/api/v1/mcp/servers",
			body: map[string]any{"name": "new", "transport": "stdio", "target": `/bin/x --root "/tmp/a b`},
			want: http.StatusBadRequest, text: "unclosed",
		},
		{
			name: "http target that is not a URL", method: http.MethodPost, target: "/api/v1/mcp/servers",
			body: map[string]any{"name": "new", "transport": "streamable_http", "target": "/tmp/not-a-url"},
			want: http.StatusBadRequest, text: "http(s) URL",
		},
		{
			name: "switched off and required", method: http.MethodPost, target: "/api/v1/mcp/servers",
			body: map[string]any{"name": "new", "transport": "stdio", "target": "/bin/x", "enabled": false, "required": true},
			want: http.StatusBadRequest, text: "required server must be enabled",
		},
		{
			name: "negative request timeout", method: http.MethodPost, target: "/api/v1/mcp/servers",
			body: map[string]any{"name": "new", "transport": "stdio", "target": "/bin/x", "request_timeout": -1},
			want: http.StatusBadRequest, text: "request_timeout",
		},
		{
			name: "unknown entry", method: http.MethodPut, target: "/api/v1/mcp/servers/ghost",
			body: map[string]any{"enabled": false},
			want: http.StatusNotFound, text: "no MCP server named",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doJSON(t, f.server, tc.method, tc.target, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status = %d %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
			if !strings.Contains(rec.Body.String(), tc.text) {
				t.Fatalf("body = %q, want %q in it", rec.Body.String(), tc.text)
			}
		})
	}
	after := f.shared(t)
	if len(after) != len(before) {
		t.Fatalf("a refused write must change nothing: %+v -> %+v", before, after)
	}
}

// TestMCPServerWriteKeepsUntouchedFieldsAndTargets pins the round trip a modal
// depends on: the target line comes back exactly as it was rendered, sending it
// back changes nothing, and the fields with no editor of their own (the per-tool
// approval table) survive.
func TestMCPServerWriteKeepsUntouchedFieldsAndTargets(t *testing.T) {
	entries := []appcfg.MCPServerConfig{
		{
			Name:      "quoted",
			Transport: "stdio",
			Command:   "mcp-server",
			Args:      []string{"--root", "/tmp/a b"},
			Tools:     map[string]appcfg.MCPServerToolConfig{"search": {ApprovalMode: appcfg.MCPToolApprovalApprove}},
		},
		{
			Name:      "queried",
			Transport: "streamable_http",
			URL:       "https://user:pw@docs.test/mcp?token=x",
		},
	}
	// The session froze exactly these entries, which is what the list reports.
	f := newMCPWriteFixture(t, func(root *appcfg.Root) {
		root.Agents.Defaults.MCPServers = entries
	}, "main", entries)

	// The list renders the stdio target as one line and the URL without its
	// userinfo and query.
	listed := decodeMCPServersBody(t, func() *httptest.ResponseRecorder {
		rec := doJSON(t, f.server, http.MethodGet, "/api/v1/mcp/servers", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		return rec
	}())
	rendered := listed["servers"].([]any)[0].(map[string]any)
	target, _ := rendered["target"].(string)
	if target != `mcp-server --root "/tmp/a b"` {
		t.Fatalf("rendered target = %q", target)
	}
	queried := mcpServerRow(t, listed, "queried")
	if got, _ := queried["target"].(string); got != "https://docs.test/mcp" {
		t.Fatalf("a listed URL must be sanitized: %q", got)
	}

	// Sending the rendered values straight back — what a form that always
	// submits every field does — changes neither the command nor the URL.
	rec := doJSON(t, f.server, http.MethodPut, "/api/v1/mcp/servers/quoted", map[string]any{
		"target":  target,
		"enabled": false,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, f.server, http.MethodPut, "/api/v1/mcp/servers/queried", map[string]any{
		"target": "https://docs.test/mcp",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	saved := findServer(t, f.shared(t), "quoted")
	if saved.Command != "mcp-server" || len(saved.Args) != 2 || saved.Args[1] != "/tmp/a b" {
		t.Fatalf("the command must be byte-for-byte unchanged: %+v", saved)
	}
	if appcfg.ServerEnabled(saved) {
		t.Fatalf("the field that did change must have changed: %+v", saved.Enabled)
	}
	if got := saved.Tools["search"].ApprovalMode; got != appcfg.MCPToolApprovalApprove {
		t.Fatalf("a field with no editor must be preserved: %+v", saved.Tools)
	}
	queriedSaved := findServer(t, f.shared(t), "queried")
	if queriedSaved.URL != "https://user:pw@docs.test/mcp?token=x" {
		t.Fatalf("the masked URL must not be written over the real one: %q", queriedSaved.URL)
	}

	// A target that really changed replaces the command line.
	rec = doJSON(t, f.server, http.MethodPut, "/api/v1/mcp/servers/quoted", map[string]any{
		"target": `other-server --flag "value with spaces"`,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	saved = findServer(t, f.shared(t), "quoted")
	if saved.Command != "other-server" || len(saved.Args) != 2 || saved.Args[1] != "value with spaces" {
		t.Fatalf("a new target must be parsed: %+v", saved)
	}
}

// TestMCPServerWriteNeverReturnsAStoredSecret pins the direction that matters: a
// credential the surface is not allowed to read comes back as the placeholder,
// sending the placeholder back keeps the stored value, and a plaintext secret
// written by hand is refused rather than saved into a file the loader would then
// refuse to read.
func TestMCPServerWriteNeverReturnsAStoredSecret(t *testing.T) {
	entry := appcfg.MCPServerConfig{
		Name:      "secretive",
		Transport: "streamable_http",
		URL:       "https://docs.test/mcp",
		Headers: map[string]string{
			"Authorization": "Bearer eyJhbGciOiJSUzI1NiJ9",
			"x-api-key":     "${DOCS_API_KEY}",
		},
		OAuth: appcfg.MCPOAuthConfig{ClientSecret: "${DOCS_OAUTH_SECRET}"},
	}
	f := newMCPWriteFixture(t, func(root *appcfg.Root) {
		root.Agents.Defaults.MCPServers = []appcfg.MCPServerConfig{entry}
	}, "main", []appcfg.MCPServerConfig{entry})
	// A live token in the credential store must not travel with the
	// configuration either: the list describes what is configured.
	if err := mcp.SaveOAuthOverlayMerge(f.home, "", "secretive", appcfg.MCPOAuthConfig{AccessToken: "real-access-token"}); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, f.server, http.MethodGet, "/api/v1/mcp/servers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "eyJhbGciOiJSUzI1NiJ9") {
		t.Fatalf("the header literal must not come back: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "real-access-token") {
		t.Fatalf("a stored token must not come back: %s", rec.Body.String())
	}
	row := mcpServerRow(t, decodeMCPServersBody(t, rec), "secretive")
	headers := row["headers"].(map[string]any)
	if headers["Authorization"] != appcfg.RedactedSecretPlaceholder {
		t.Fatalf("headers = %+v", headers)
	}
	if headers["x-api-key"] != "" {
		t.Fatalf("a reference the load resolved (its env is unset) reads empty, not a secret: %+v", headers)
	}
	oauth := row["oauth"].(map[string]any)
	if oauth["client_secret"] != "" {
		t.Fatalf("a secret reference the load resolved (its env unset) reads empty, not a secret: %+v", oauth)
	}
	if oauth["access_token"] != "" {
		t.Fatalf("the config has no access token to show: %+v", oauth)
	}
	if row["oauth_overlay"] != true {
		t.Fatalf("the list must say a stored credential exists: %+v", row)
	}

	// Sending the placeholder back leaves the stored literal alone.
	rec = doJSON(t, f.server, http.MethodPut, "/api/v1/mcp/servers/secretive", map[string]any{
		"headers": map[string]string{"Authorization": appcfg.RedactedSecretPlaceholder},
		"oauth":   map[string]any{"client_secret": appcfg.RedactedSecretPlaceholder},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	saved := findServer(t, f.shared(t), "secretive")
	if saved.Headers["Authorization"] != "Bearer eyJhbGciOiJSUzI1NiJ9" {
		t.Fatalf("headers = %+v", saved.Headers)
	}
	if saved.OAuth.ClientSecret != "${DOCS_OAUTH_SECRET}" {
		t.Fatalf("oauth = %+v", saved.OAuth)
	}
	if saved.Headers["x-api-key"] != "${DOCS_API_KEY}" {
		t.Fatalf("a header the update did not mention must survive: %+v", saved.Headers)
	}

	// An empty value is how the form removes one key, and it removes only that
	// one.
	rec = doJSON(t, f.server, http.MethodPut, "/api/v1/mcp/servers/secretive", map[string]any{
		"headers": map[string]string{"x-api-key": ""},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	saved = findServer(t, f.shared(t), "secretive")
	if _, ok := saved.Headers["x-api-key"]; ok {
		t.Fatalf("an empty value must remove the key: %+v", saved.Headers)
	}
	if saved.Headers["Authorization"] != "Bearer eyJhbGciOiJSUzI1NiJ9" {
		t.Fatalf("removing one key must not touch the others: %+v", saved.Headers)
	}

	// A secret-like key written as a literal is refused: the loader would not
	// read the file back.
	before := mustRead(t, f.cfgPath)
	rec = doJSON(t, f.server, http.MethodPut, "/api/v1/mcp/servers/secretive", map[string]any{
		"headers": map[string]string{"x-api-key": "sk-live-plaintext"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "plaintext secret") {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if mustRead(t, f.cfgPath) != before {
		t.Fatal("a refused write must change nothing")
	}
}

// TestMCPWriteAddressesTheLayerTheRouteNames pins that the same payload lands in
// the three different places, and that a layer the route cannot address is
// refused before anything is written.
func TestMCPWriteAddressesTheLayerTheRouteNames(t *testing.T) {
	f := newMCPWriteFixture(t, func(root *appcfg.Root) {
		root.Agents.Definitions["alice"] = appcfg.AgentDefinition{Primary: true}
	}, "main", nil)

	rec := doJSON(t, f.server, http.MethodPost, "/api/v1/agents/primary/alice/mcp/servers", map[string]any{
		"name": "agent-doc", "transport": "stdio", "target": "/bin/alice-doc",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("agent create: %d %s", rec.Code, rec.Body.String())
	}
	cfg := f.config(t)
	own := cfg.Agents.MCPServersFor("alice")
	if len(own) != 1 || own[0].Command != "/bin/alice-doc" {
		t.Fatalf("alice's own list = %+v", own)
	}
	if len(cfg.Agents.Defaults.MCPServers) != 0 {
		t.Fatalf("the shared list must not be touched: %+v", cfg.Agents.Defaults.MCPServers)
	}
	// The other agents, and the fields of alice's definition, are untouched.
	aliceDef := cfg.Agents.Definitions["alice"]
	if !aliceDef.Primary {
		t.Fatalf("only the MCP list may change: %+v", aliceDef)
	}

	rec = doJSON(t, f.server, http.MethodPost, "/api/v1/agents/primary/nobody/mcp/servers", map[string]any{
		"name": "x", "transport": "stdio", "target": "/bin/x",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown agent: %d %s, want 404", rec.Code, rec.Body.String())
	}

	// The project layer writes the project's own file, and a project that is not
	// trusted is refused — with no file created.
	root := t.TempDir()
	p, err := f.projects.Create(context.Background(), state.CreateProjectInput{Name: "p", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	target := "/api/v1/projects/" + p.ID + "/mcp/servers"
	rec = doJSON(t, f.server, http.MethodPost, target, map[string]any{
		"name": "projtool", "transport": "stdio", "target": "/bin/pt",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("untrusted project: %d %s, want 409", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".forebrain", "mcp_servers.yaml")); !os.IsNotExist(err) {
		t.Fatalf("an untrusted project must not get a file: %v", err)
	}
	if err := safety.MarkTrusted(f.home, safety.Project{Root: root}); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, f.server, http.MethodPost, target, map[string]any{
		"name": "projtool", "transport": "stdio", "target": "/bin/pt",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("project create: %d %s", rec.Code, rec.Body.String())
	}
	projectFile := filepath.Join(root, ".forebrain", "mcp_servers.yaml")
	raw, err := os.ReadFile(projectFile)
	if err != nil {
		t.Fatalf("the project file must exist: %v", err)
	}
	if !strings.Contains(string(raw), "projtool") {
		t.Fatalf("project file:\n%s", raw)
	}
	// And it is the project file, not forebrain.yaml.
	if strings.Contains(string(mustRead(t, f.cfgPath)), "projtool") {
		t.Fatalf("forebrain.yaml must not carry the project's entry:\n%s", mustRead(t, f.cfgPath))
	}
}

// TestMCPLayerListingReportsOneLayerAndWhatItIsDoing pins the read side the
// settings screens work from: ?scope lists the configured entries of that layer,
// including one a higher layer shadows — which is reported as not running rather
// than hidden, because it is still the entry the user came to edit.
func TestMCPLayerListingReportsOneLayerAndWhatItIsDoing(t *testing.T) {
	// The agent layer redefines codegraph, so the shared entry is not the one
	// a session loads. The override lives in the configuration — the base
	// runner hosts no servers — and the listing resolves the layers to say
	// what runs and what only sits there configured.
	f := newMCPWriteFixture(t, func(root *appcfg.Root) {
		root.Agents.Defaults.MCPServers = []appcfg.MCPServerConfig{{
			Name:      "codegraph",
			Transport: "stdio",
			Command:   "/bin/shared-cg",
		}}
		main := root.Agents.Definitions["main"]
		main.MCPServers = []appcfg.MCPServerConfig{{
			Name:      "codegraph",
			Transport: "stdio",
			Command:   "/bin/agent-cg",
			Scope:     appcfg.MCPServerScopeAgent,
			AgentKey:  "main",
		}}
		root.Agents.Definitions["main"] = main
	}, "main", nil)

	rec := doJSON(t, f.server, http.MethodGet, "/api/v1/mcp/servers?scope=global", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("global layer: %d %s", rec.Code, rec.Body.String())
	}
	globalBody := decodeMCPServersBody(t, rec)
	if globalBody["scope"] != "global" {
		t.Fatalf("body = %+v", globalBody)
	}
	rows, _ := globalBody["servers"].([]any)
	if len(rows) != 1 {
		t.Fatalf("the layer holds exactly its own entry: %+v", rows)
	}
	row := rows[0].(map[string]any)
	if row["name"] != "codegraph" || row["running"] != false {
		t.Fatalf("a shadowed shared entry is configured and not running: %+v", row)
	}
	if row["command"] != nil {
		t.Fatalf("the row carries the entry's fields, not a summary: %+v", row)
	}
	if row["target"] != "/bin/shared-cg" {
		t.Fatalf("the configured entry is the one listed: %+v", row)
	}
	// The runtime the session reports belongs to the agent entry, so it must not
	// be attached to the shared one.
	if _, ok := row["conn_status"]; ok {
		t.Fatalf("a shadowed entry has no runtime of its own: %+v", row)
	}

	rec = doJSON(t, f.server, http.MethodGet, "/api/v1/mcp/servers?scope=agent", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent layer: %d %s", rec.Code, rec.Body.String())
	}
	agentBody := decodeMCPServersBody(t, rec)
	agentRows, _ := agentBody["servers"].([]any)
	if len(agentRows) != 1 {
		t.Fatalf("the agent layer holds exactly its own override: %+v", agentRows)
	}
	agentRow := agentRows[0].(map[string]any)
	if agentRow["target"] != "/bin/agent-cg" || agentRow["running"] != true {
		t.Fatalf("the agent's own entry is the one a session loads: %+v", agentRow)
	}

	// The unscoped listing is still the effective list.
	rec = doJSON(t, f.server, http.MethodGet, "/api/v1/mcp/servers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if _, scope := decodeMCPServersBody(t, rec)["scope"]; scope {
		t.Fatalf("the effective listing is not a layer listing: %s", rec.Body.String())
	}
}

// TestMCPTestEndpointTriesTheEntryWithoutWritingIt pins the button's contract:
// the entry is really started and really lists tools, the file is untouched
// afterwards, and a server that will not start is reported as a result rather
// than as a request failure.
func TestMCPTestEndpointTriesTheEntryWithoutWritingIt(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "acceptance", "fake_mcp_stdio.py"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	f := newMCPWriteFixture(t, sharedMCPFixture, "main", nil)
	before := mustRead(t, f.cfgPath)

	rec := doJSON(t, f.server, http.MethodPost, "/api/v1/mcp/servers/test", map[string]any{
		"name":      "fake",
		"transport": "stdio",
		"target":    mcp.FormatStdioTarget(python, []string{script, "echo"}),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("test: %d %s", rec.Code, rec.Body.String())
	}
	var res struct {
		OK        bool `json:"ok"`
		ToolCount int  `json:"tool_count"`
		Tools     []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if !res.OK || res.ToolCount < 1 {
		t.Fatalf("result = %+v (%s)", res, rec.Body.String())
	}
	if res.Tools[0].Name != "echo" {
		t.Fatalf("tools = %+v", res.Tools)
	}
	if mustRead(t, f.cfgPath) != before {
		t.Fatal("testing a connection must not write the configuration")
	}

	// A server that cannot start is an answer to show the user, not an error.
	rec = doJSON(t, f.server, http.MethodPost, "/api/v1/mcp/servers/test", map[string]any{
		"name":      "broken",
		"transport": "stdio",
		"target":    filepath.Join(t.TempDir(), "not-a-command"),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("test: %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.OK || strings.TrimSpace(res.Error) == "" {
		t.Fatalf("result = %+v", res)
	}

	// An entry that is not a connection at all is the caller's to fix.
	rec = doJSON(t, f.server, http.MethodPost, "/api/v1/mcp/servers/test", map[string]any{
		"name": "emptiness", "transport": "stdio",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if mustRead(t, f.cfgPath) != before {
		t.Fatal("no refused test may write the configuration")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestAgentRuleFilesPagesWithSearchAndTotal pins the rule-file table's answer:
// a page-bearing request is searched and sliced on the server and counts every
// matching row.
func TestAgentRuleFilesPagesWithSearchAndTotal(t *testing.T) {
	s, _, workspace := rulesServer(t)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("hello"), 0o644))

	rr := httptest.NewRecorder()
	s.handleAgentRuleFiles(rr, httptest.NewRequest(http.MethodGet, "/api/rules/agent?page=1&page_size=1", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var paged struct {
		Files    []ruleFileRow `json:"files"`
		Total    int           `json:"total"`
		Page     int           `json:"page"`
		PageSize int           `json:"page_size"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &paged))
	require.Equal(t, 3, paged.Total)
	require.Equal(t, 1, paged.Page)
	require.Equal(t, 1, paged.PageSize)
	require.Len(t, paged.Files, 1)
	require.Equal(t, "AGENTS.md", paged.Files[0].Name)

	rr = httptest.NewRecorder()
	s.handleAgentRuleFiles(rr, httptest.NewRequest(http.MethodGet, "/api/rules/agent?page=1&page_size=10&q=soul", nil))
	paged = struct {
		Files    []ruleFileRow `json:"files"`
		Total    int           `json:"total"`
		Page     int           `json:"page"`
		PageSize int           `json:"page_size"`
	}{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &paged))
	require.Equal(t, 1, paged.Total)
	require.Len(t, paged.Files, 1)
	require.Equal(t, "SOUL.md", paged.Files[0].Name)
}

// TestAgentRuleFilesWithoutPageKeepsTheOldAnswer pins that the drawer reading
// this list without a page still gets every file and no paging fields.
func TestAgentRuleFilesWithoutPageKeepsTheOldAnswer(t *testing.T) {
	s, _, _ := rulesServer(t)
	rr := httptest.NewRecorder()
	s.handleAgentRuleFiles(rr, httptest.NewRequest(http.MethodGet, "/api/rules/agent", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var answer map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &answer))
	files, _ := answer["files"].([]any)
	require.Len(t, files, 3)
	for _, field := range []string{"total", "page", "page_size"} {
		_, seen := answer[field]
		require.False(t, seen, "%q must only appear on a paged answer", field)
	}
}

// TestSkillsListPagesWithSearchAndOrigin pins the skill table's answer: the
// search reads name and description, the origin filter takes several layers at
// once, every matching row is counted, and the actions the page offers stay on
// the answer.
func TestSkillsListPagesWithSearchAndOrigin(t *testing.T) {
	home := t.TempDir()
	s, skillsRoot := newSkillVersionsServer(t, home)
	writeSkill := func(root, name, description string) {
		t.Helper()
		dir := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
			[]byte("---\nname: "+name+"\ndescription: "+description+"\n---\n\nbody\n"), 0o644))
	}
	writeSkill(skillsRoot, "agent-ops", "agent runbook")
	writeSkill(filepath.Join(home, "skills"), "shared-ops", "shared runbook")
	writeSkill(filepath.Join(home, "skills", ".system"), "builtin-ops", "builtin runbook")

	type answer struct {
		Installed []skillListEntry `json:"installed"`
		Actions   []string         `json:"actions"`
		Total     int              `json:"total"`
		Page      int              `json:"page"`
		PageSize  int              `json:"page_size"`
	}
	list := func(target string) answer {
		t.Helper()
		rr := httptest.NewRecorder()
		s.handleSkillsList(rr, httptest.NewRequest(http.MethodGet, target, nil))
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var out answer
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
		return out
	}

	// One page, three rows: the total is every match, not the page's length.
	page := list("/api/skills?page=1&page_size=2")
	require.Equal(t, 3, page.Total)
	require.Equal(t, 1, page.Page)
	require.Equal(t, 2, page.PageSize)
	require.Len(t, page.Installed, 2)

	// The shared library's own page asks for two layers at once.
	shared := list("/api/skills?page=1&page_size=10&origin=shared,builtin")
	require.Equal(t, 2, shared.Total)
	origins := []string{}
	for _, row := range shared.Installed {
		origins = append(origins, string(row.Origin))
	}
	require.ElementsMatch(t, []string{"shared", "builtin"}, origins)

	searched := list("/api/skills?page=1&page_size=10&q=builtin+runbook")
	require.Equal(t, 1, searched.Total)
	require.Len(t, searched.Installed, 1)
	require.Equal(t, "builtin-ops", searched.Installed[0].Name)
}

// TestSkillsListWithoutPageKeepsTheOldAnswer pins that a caller without a page
// still gets the whole listing under "installed".
func TestSkillsListWithoutPageKeepsTheOldAnswer(t *testing.T) {
	home := t.TempDir()
	s, skillsRoot := newSkillVersionsServer(t, home)
	dir := filepath.Join(skillsRoot, "agent-ops")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: agent-ops\ndescription: agent runbook\n---\n\nbody\n"), 0o644))

	rr := httptest.NewRecorder()
	s.handleSkillsList(rr, httptest.NewRequest(http.MethodGet, "/api/skills", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var answer map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &answer))
	installed, _ := answer["installed"].([]any)
	require.Len(t, installed, 1)
	for _, field := range []string{"total", "page", "page_size"} {
		_, seen := answer[field]
		require.False(t, seen, "%q must only appear on a paged answer", field)
	}
}

// toolsPageServer builds a server whose runtime exposes four tools across
// three categories, two of them read-only.
func toolsPageServer(t *testing.T) *Server {
	t.Helper()
	home := t.TempDir()
	runner := &run.Runner{Deps: &run.Deps{Home: home}}
	tools := tool.NewState(home)
	for _, meta := range []event.ToolMeta{
		{Name: "read_file", Description: "read a file from disk", Category: "filesystem", ReadOnly: true},
		{Name: "write_file", Description: "write a file to disk", Category: "filesystem"},
		{Name: "shell", Description: "run a command", Category: "shell", Destructive: true},
		{Name: "web_fetch", Description: "fetch a web page", Category: "web", ReadOnly: true},
	} {
		tools.RegisterToolMeta(meta)
	}
	setRunnerToolsForTest(runner, tools)
	return &Server{Home: home, Runner: runner, Env: &process.Environment{Deps: run.Deps{Home: home}, Runner: runner}}
}

// TestToolsListPagesWithFiltersCategoriesAndTotal pins the tool table's
// answer: name and description are searched, the category and type filters
// apply on the server, the rows come back A→Z, the category list is every
// category the runtime exposes whatever the filters leave, and the count is
// every matching row.
func TestToolsListPagesWithFiltersCategoriesAndTotal(t *testing.T) {
	s := toolsPageServer(t)
	type answer struct {
		Tools      []event.ToolMeta `json:"tools"`
		Categories []string         `json:"categories"`
		Total      int              `json:"total"`
		Page       int              `json:"page"`
		PageSize   int              `json:"page_size"`
	}
	list := func(target string) answer {
		t.Helper()
		rr := httptest.NewRecorder()
		s.handleToolsList(rr, httptest.NewRequest(http.MethodGet, target, nil))
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var out answer
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
		return out
	}

	page := list("/api/tools?page=1&page_size=2")
	require.Equal(t, 4, page.Total)
	require.Equal(t, 1, page.Page)
	require.Equal(t, 2, page.PageSize)
	require.Len(t, page.Tools, 2)
	// A→Z: read_file before shell before web_fetch before write_file.
	require.Equal(t, "read_file", page.Tools[0].Name)
	require.Equal(t, "shell", page.Tools[1].Name)
	// The dropdown keeps every category, including the ones this page does
	// not show.
	require.Equal(t, []string{"filesystem", "shell", "web"}, page.Categories)

	readOnly := list("/api/tools?page=1&page_size=10&type=readonly")
	require.Equal(t, 2, readOnly.Total)
	require.Equal(t, []string{"filesystem", "shell", "web"}, readOnly.Categories)
	for _, row := range readOnly.Tools {
		require.True(t, row.ReadOnly, "%s is not read-only", row.Name)
	}

	category := list("/api/tools?page=1&page_size=10&category=filesystem")
	require.Equal(t, 2, category.Total)

	searched := list("/api/tools?page=1&page_size=10&q=command")
	require.Equal(t, 1, searched.Total)
	require.Equal(t, "shell", searched.Tools[0].Name)

	// A page past the end serves the last page.
	past := list("/api/tools?page=9&page_size=3")
	require.Equal(t, 4, past.Total)
	require.Equal(t, 2, past.Page)
	require.Len(t, past.Tools, 1)
	require.Equal(t, "write_file", past.Tools[0].Name)
}

// TestToolsListWithoutPageStaysABareArray pins the old answer of the endpoint:
// the tool drawer reads it whole, as a bare array with no paging fields.
func TestToolsListWithoutPageStaysABareArray(t *testing.T) {
	s := toolsPageServer(t)
	rr := httptest.NewRecorder()
	s.handleToolsList(rr, httptest.NewRequest(http.MethodGet, "/api/tools", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var rows []event.ToolMeta
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &rows))
	require.Len(t, rows, 4)
}

// permissionRulesPageServer builds a server whose agent holds three local
// permission rules.
func permissionRulesPageServer(t *testing.T) *Server {
	t.Helper()
	s, home, _ := rulesServer(t)
	dir := filepath.Join(home, "workspace", "state", "permissions")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "local_settings.json"), []byte(`{"rules":{
		"allow":[{"tool_name":"read_file"},{"tool_name":"shell","rule_content":"git status:*"}],
		"deny":[{"tool_name":"shell","rule_content":"rm -rf /"}]}}`), 0o600))
	return s
}

// TestPermissionRulesPagesWithSearchAndMode pins the permission table's
// answer: the tool and the rule text are searched on the server, every match
// is counted, and the approval mode stays on the answer beside the page.
func TestPermissionRulesPagesWithSearchAndMode(t *testing.T) {
	s := permissionRulesPageServer(t)
	type answer struct {
		Mode     string              `json:"mode"`
		Rules    []permissionRuleRow `json:"rules"`
		Total    int                 `json:"total"`
		Page     int                 `json:"page"`
		PageSize int                 `json:"page_size"`
	}
	list := func(target string) answer {
		t.Helper()
		rr := httptest.NewRecorder()
		s.handlePermissionRules(rr, httptest.NewRequest(http.MethodGet, target, nil))
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var out answer
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
		return out
	}

	page := list("/api/permissions/rules?page=1&page_size=2")
	require.Equal(t, 3, page.Total)
	require.Equal(t, 1, page.Page)
	require.Equal(t, 2, page.PageSize)
	require.Len(t, page.Rules, 2)
	require.NotEmpty(t, page.Mode)

	searched := list("/api/permissions/rules?page=1&page_size=10&q=git+status")
	require.Equal(t, 1, searched.Total)
	require.Len(t, searched.Rules, 1)
	require.Equal(t, "shell", searched.Rules[0].ToolName)
	require.Equal(t, safety.BehaviorAllow, searched.Rules[0].Behavior)

	denied := list("/api/permissions/rules?page=1&page_size=10&q=rm+-rf")
	require.Equal(t, 1, denied.Total)
	require.Equal(t, safety.BehaviorDeny, denied.Rules[0].Behavior)
}

// TestPermissionRulesWithoutPageKeepTheOldAnswer pins that the page-less
// answer still carries the whole rule list and the mode, with no paging
// fields beside them.
func TestPermissionRulesWithoutPageKeepTheOldAnswer(t *testing.T) {
	s := permissionRulesPageServer(t)
	rr := httptest.NewRecorder()
	s.handlePermissionRules(rr, httptest.NewRequest(http.MethodGet, "/api/permissions/rules", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var answer map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &answer))
	rules, _ := answer["rules"].([]any)
	require.Len(t, rules, 3)
	require.NotEmpty(t, answer["mode"])
	for _, field := range []string{"total", "page", "page_size"} {
		_, seen := answer[field]
		require.False(t, seen, "%q must only appear on a paged answer", field)
	}
}

// mcpReadRowNames lists one answer's row names in the order the server sent
// them: the order is part of the answer (a layer in the order its file writes
// it, a project before the layers it overrides), so it is asserted directly.
func mcpReadRowNames(t *testing.T, body map[string]any) []string {
	t.Helper()
	rows, ok := body["servers"].([]any)
	require.True(t, ok, "servers is a list: %#v", body["servers"])
	names := make([]string, 0, len(rows))
	for _, item := range rows {
		row, ok := item.(map[string]any)
		require.True(t, ok, "row is an object: %#v", item)
		name, _ := row["name"].(string)
		names = append(names, name)
	}
	return names
}

// mcpReadRowScopes lists the layers an answer's rows report, in the same order.
func mcpReadRowScopes(t *testing.T, body map[string]any) []string {
	t.Helper()
	rows, _ := body["servers"].([]any)
	scopes := make([]string, 0, len(rows))
	for _, item := range rows {
		row, _ := item.(map[string]any)
		scope, _ := row["scope"].(string)
		scopes = append(scopes, scope)
	}
	return scopes
}

// TestMCPAgentViewHidesShadowedGlobalAndListsAgentFirst pins the menu bar's MCP
// page: the primary agent's own entries, then the shared ones it inherits, and a
// shared entry whose name the agent redefines is not listed at all. One name,
// one row — the row that is the entry actually configured for this agent, not
// the shared one it replaced.
func TestMCPAgentViewHidesShadowedGlobalAndListsAgentFirst(t *testing.T) {
	f := newMCPWriteFixture(t, func(root *appcfg.Root) {
		main := root.Agents.Definitions["main"]
		main.MCPServers = []appcfg.MCPServerConfig{
			{Name: "b", Transport: "stdio", Command: "/bin/agent-b"},
			{Name: "c", Transport: "stdio", Command: "/bin/agent-c"},
		}
		root.Agents.Definitions["main"] = main
		root.Agents.Defaults.MCPServers = []appcfg.MCPServerConfig{
			{Name: "a", Transport: "stdio", Command: "/bin/shared-a"},
			{Name: "b", Transport: "stdio", Command: "/bin/shared-b"},
		}
	}, "main", nil)

	body := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet, "/api/v1/mcp/servers?view=agent", nil))
	require.Equal(t, []string{"b", "c", "a"}, mcpReadRowNames(t, body))
	require.Equal(t, []string{appcfg.MCPServerScopeAgent, appcfg.MCPServerScopeAgent, appcfg.MCPServerScopeGlobal}, mcpReadRowScopes(t, body))
	require.Equal(t, "agent", body["view"])
	// The b that is listed is the agent's own entry, not the shared one it
	// replaced: showing the shared command would describe a server that never
	// runs.
	require.Equal(t, "/bin/agent-b", mcpServerRow(t, body, "b")["target"])
	// Without "page" the view answers the whole list, and none of the paging
	// fields are invented.
	require.NotContains(t, body, "total")
	require.NotContains(t, body, "page")

	// With "page" the same order is sliced on the server and counted.
	paged := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet, "/api/v1/mcp/servers?view=agent&page=1&page_size=2", nil))
	require.Equal(t, []string{"b", "c"}, mcpReadRowNames(t, paged))
	require.EqualValues(t, 3, paged["total"])
	require.EqualValues(t, 1, paged["page"])
	require.EqualValues(t, 2, paged["page_size"])
}

// TestMCPLayerListMarksSharedEntriesShadowedByAgent pins the settings page's
// shared listing: an entry a same-named entry of the primary agent's own list
// replaces is listed there — that is the page it is edited on — and the row
// names the layer that covers it, so "not running" has a reason beside it.
func TestMCPLayerListMarksSharedEntriesShadowedByAgent(t *testing.T) {
	f := newMCPWriteFixture(t, func(root *appcfg.Root) {
		main := root.Agents.Definitions["main"]
		main.MCPServers = []appcfg.MCPServerConfig{
			{Name: "b", Transport: "stdio", Command: "/bin/agent-b"},
			{Name: "c", Transport: "stdio", Command: "/bin/agent-c"},
		}
		root.Agents.Definitions["main"] = main
		root.Agents.Defaults.MCPServers = []appcfg.MCPServerConfig{
			{Name: "a", Transport: "stdio", Command: "/bin/shared-a"},
			{Name: "b", Transport: "stdio", Command: "/bin/shared-b"},
		}
	}, "main", nil)

	shared := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet, "/api/v1/mcp/servers?scope=global", nil))
	require.Equal(t, []string{"a", "b"}, mcpReadRowNames(t, shared))
	shadowed := mcpServerRow(t, shared, "b")
	require.Equal(t, appcfg.MCPServerScopeAgent, shadowed["shadowed_by"])
	require.Equal(t, "main", shadowed["shadowed_by_name"])
	// The entry nothing replaces carries the fields not at all, so a client
	// can tell "covered" from "reported empty".
	own := mcpServerRow(t, shared, "a")
	require.NotContains(t, own, "shadowed_by")
	require.NotContains(t, own, "shadowed_by_name")

	ownLayer := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet, "/api/v1/mcp/servers?scope=agent", nil))
	require.Equal(t, []string{"b", "c"}, mcpReadRowNames(t, ownLayer))
	for _, name := range []string{"b", "c"} {
		row := mcpServerRow(t, ownLayer, name)
		require.NotContains(t, row, "shadowed_by")
		require.NotContains(t, row, "shadowed_by_name")
	}
}

// TestMCPLayerListPagesWithTotal pins the settings page's listing: one layer in
// the order its file writes it, searched over the name and the command or
// address, filtered by layer, sliced on the server with the total beside it —
// and unchanged for a request that carries no page.
func TestMCPLayerListPagesWithTotal(t *testing.T) {
	servers := make([]appcfg.MCPServerConfig, 0, 12)
	for i := range 12 {
		name := fmt.Sprintf("tool-%c", 'a'+i)
		servers = append(servers, appcfg.MCPServerConfig{
			Name:      name,
			Transport: "stdio",
			Command:   "/bin/run-" + name,
		})
	}
	f := newMCPWriteFixture(t, func(root *appcfg.Root) {
		root.Agents.Defaults.MCPServers = servers
	}, "main", nil)

	// No page: the whole layer under "servers", with the fields it always had.
	all := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet, "/api/v1/mcp/servers?scope=global", nil))
	require.Len(t, mcpReadRowNames(t, all), 12)
	require.Equal(t, "global", all["scope"])
	require.NotContains(t, all, "total")
	require.NotContains(t, all, "page")

	// Second page of ten: two rows, and the total is the whole layer's.
	second := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet, "/api/v1/mcp/servers?scope=global&page=2&page_size=10", nil))
	require.Equal(t, []string{"tool-k", "tool-l"}, mcpReadRowNames(t, second))
	require.EqualValues(t, 12, second["total"])
	require.EqualValues(t, 2, second["page"])
	require.EqualValues(t, 10, second["page_size"])
	require.Equal(t, "global", second["scope"])

	// A page past the end serves the last page rather than an empty table.
	past := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet, "/api/v1/mcp/servers?scope=global&page=9&page_size=10", nil))
	require.Len(t, mcpReadRowNames(t, past), 2)
	require.EqualValues(t, 2, past["page"])

	// The search reads the name and the command, and nothing else: the
	// transport a row reports is not a thing anyone searches a server by.
	byName := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet, "/api/v1/mcp/servers?scope=global&q=tool-c&page=1&page_size=10", nil))
	require.Equal(t, []string{"tool-c"}, mcpReadRowNames(t, byName))
	require.EqualValues(t, 1, byName["total"])
	byCommand := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet, "/api/v1/mcp/servers?scope=global&q=run-tool-e&page=1&page_size=10", nil))
	require.Equal(t, []string{"tool-e"}, mcpReadRowNames(t, byCommand))
	byOtherField := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet, "/api/v1/mcp/servers?scope=global&q=stdio&page=1&page_size=10", nil))
	require.Empty(t, mcpReadRowNames(t, byOtherField))
	require.EqualValues(t, 0, byOtherField["total"])

	// The layer filter keeps the rows of the layers it names. This view holds
	// one layer, so its own comes back and any other leaves nothing.
	require.Len(t, mcpReadRowNames(t, decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet,
		"/api/v1/mcp/servers?scope=global&page=1&page_size=20&source=global,agent", nil))), 12)
	require.Empty(t, mcpReadRowNames(t, decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet,
		"/api/v1/mcp/servers?scope=global&page=1&page_size=10&source=agent", nil))))
}

// mcpPreviewFixture is a gateway over a trusted project whose MCP file holds one
// confirmed entry and one still awaiting a decision, below an agent layer of one
// entry and a shared layer of one: the three layers a project view shows.
type mcpPreviewFixture struct {
	*mcpWriteFixture
	root string
	// projectID is the registered project's id.
	projectID string
}

func newMCPPreviewFixture(t *testing.T) *mcpPreviewFixture {
	t.Helper()
	f := newMCPWriteFixture(t, func(root *appcfg.Root) {
		main := root.Agents.Definitions["main"]
		main.MCPServers = []appcfg.MCPServerConfig{
			{Name: "b", Transport: "stdio", Command: "/bin/agent-b"},
		}
		root.Agents.Definitions["main"] = main
		root.Agents.Defaults.MCPServers = []appcfg.MCPServerConfig{
			{Name: "a", Transport: "stdio", Command: "/bin/shared-a"},
		}
	}, "main", nil)

	root := projectTempRoot(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	require.NoError(t, safety.MarkTrusted(f.home, safety.Project{Root: root}))
	file := filepath.Join(root, ".forebrain", "mcp_servers.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, []byte(
		"mcp_servers:\n"+
			"  - name: d\n    transport: stdio\n    command: /bin/project-d\n"+
			"  - name: e\n    transport: stdio\n    command: /bin/project-e\n"), 0o644))

	rec := doJSON(t, f.server, http.MethodPost, "/api/v1/projects", map[string]any{"name": "mcpview", "root": root, "trust": true})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	id, _ := created["id"].(string)
	require.NotEmpty(t, id, rec.Body.String())

	// d is confirmed and e is not. The confirmation goes to the store the
	// project resolution reads rather than through the endpoint, because that
	// endpoint also records every other pending entry as declined.
	allowProjectMCPEntry(t, f.server.Runner.WorkspaceRoot, root, "d")
	return &mcpPreviewFixture{mcpWriteFixture: f, root: root, projectID: id}
}

// allowProjectMCPEntry records the operator's yes for one project entry, the way
// the startup prompt does.
func allowProjectMCPEntry(t *testing.T, workspace, root, name string) {
	t.Helper()
	entries, _ := mcp.LoadProjectMCPServers(root)
	consents, err := mcp.LoadProjectConsents(workspace)
	require.NoError(t, err)
	found := false
	for _, srv := range entries {
		if srv.Name != name {
			continue
		}
		consents.Decide(memory.ProjectKey(root), srv.Name, mcp.ServerFingerprint(srv), mcp.ProjectConsentAllow, time.Now())
		found = true
	}
	require.True(t, found, "no entry %q in %s", name, root)
	require.NoError(t, mcp.SaveProjectConsents(workspace, consents))
}

// TestProjectMCPPreviewOrdersProjectAgentGlobalAndIncludesPending pins the
// project space's MCP table: the entries the page does not own are shown too,
// ordered project → agent → global, and an entry still awaiting a decision is a
// row of its own at the top — that is where 允许 / 拒绝 is offered and where the
// total has to count it.
func TestProjectMCPPreviewOrdersProjectAgentGlobalAndIncludesPending(t *testing.T) {
	f := newMCPPreviewFixture(t)

	body := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet, "/api/v1/projects/"+f.projectID+"/mcp?page=1&page_size=10", nil))
	require.Equal(t, []string{"e", "d", "b", "a"}, mcpReadRowNames(t, body))
	require.Equal(t, []string{
		appcfg.MCPServerScopeProject,
		appcfg.MCPServerScopeProject,
		appcfg.MCPServerScopeAgent,
		appcfg.MCPServerScopeGlobal,
	}, mcpReadRowScopes(t, body))
	// The total counts the pending row, so the pager and the "共 N 条" line
	// agree with what the table shows.
	require.EqualValues(t, 4, body["total"])

	pending := mcpServerRow(t, body, "e")
	require.Equal(t, true, pending["pending"])
	require.Equal(t, false, pending["running"])
	// A pending row carries the entry's own fields, so the drawer can show the
	// command being asked for instead of a bare name.
	require.Equal(t, "/bin/project-e", pending["target"])
	require.NotEmpty(t, pending["summary"])

	allowed := mcpServerRow(t, body, "d")
	require.Equal(t, true, allowed["allowed"])
	require.NotContains(t, allowed, "pending")

	// The three project notes ride the paged answer too: they are the same
	// question whether or not a table is asking.
	require.Contains(t, body, "overridden_global")
	require.Contains(t, body, "not_applied")
	require.Contains(t, body, "pending_consent")

	// The layer filter keeps only the layers it names.
	onlyShared := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet,
		"/api/v1/projects/"+f.projectID+"/mcp?page=1&page_size=10&source=global", nil))
	require.Equal(t, []string{"a"}, mcpReadRowNames(t, onlyShared))
	require.EqualValues(t, 1, onlyShared["total"])
	onlyProject := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet,
		"/api/v1/projects/"+f.projectID+"/mcp?page=1&page_size=10&source=project", nil))
	require.Equal(t, []string{"e", "d"}, mcpReadRowNames(t, onlyProject))

	// The search reads the name and the command.
	searched := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet,
		"/api/v1/projects/"+f.projectID+"/mcp?page=1&page_size=10&q=project-e", nil))
	require.Equal(t, []string{"e"}, mcpReadRowNames(t, searched))
}

// TestProjectMCPConsentDecideOnlyTouchesNamedEntries pins the project page's
// per-row decision: the entries the request names are decided, everything else
// keeps the state it had, a declined entry stays a row that can be allowed
// again, and the note under the table repeats neither the pending nor the
// declined rows.
func TestProjectMCPConsentDecideOnlyTouchesNamedEntries(t *testing.T) {
	f := newMCPPreviewFixture(t)
	// A second unconfirmed entry, so a decision for one of them can be shown
	// to leave the other alone.
	file := filepath.Join(f.root, ".forebrain", "mcp_servers.yaml")
	raw, err := os.ReadFile(file)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, append(raw,
		[]byte("  - name: f\n    transport: stdio\n    command: /bin/project-f\n")...), 0o644))

	previewNames := func() []string {
		t.Helper()
		body := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet,
			"/api/v1/projects/"+f.projectID+"/mcp?page=1&page_size=20", nil))
		return mcpReadRowNames(t, body)
	}
	// The fingerprint a row carries is the one a decide request must send
	// back, so the two read each other here: read from the row, returned for
	// the click.
	rowFingerprint := func(name string) string {
		t.Helper()
		body := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet,
			"/api/v1/projects/"+f.projectID+"/mcp?page=1&page_size=20", nil))
		fp, ok := mcpServerRow(t, body, name)["fingerprint"].(string)
		require.True(t, ok && fp != "", "row %q carries no fingerprint", name)
		return fp
	}
	notAppliedNames := func(body map[string]any) []string {
		t.Helper()
		items, _ := body["not_applied"].([]any)
		names := make([]string, 0, len(items))
		for _, item := range items {
			row, _ := item.(map[string]any)
			names = append(names, fmt.Sprint(row["name"]))
		}
		return names
	}

	// Decline e through the endpoint, deciding nothing else.
	rec := doJSON(t, f.server, http.MethodPost, "/api/v1/projects/"+f.projectID+"/mcp/consent",
		map[string]any{"allow": []string{}, "decide": []string{"e"},
			"fingerprints": []any{map[string]any{"name": "e", "fingerprint": rowFingerprint("e")}}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// The table's project block in the order a decision moves an entry
	// through: still to decide, allowed, declined.
	require.Equal(t, []string{"f", "d", "e", "b", "a"}, previewNames())
	body := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet,
		"/api/v1/projects/"+f.projectID+"/mcp?page=1&page_size=20", nil))
	declined := mcpServerRow(t, body, "e")
	require.Equal(t, true, declined["declined"])
	require.NotContains(t, declined, "pending")
	require.Equal(t, false, declined["running"])
	require.Equal(t, "/bin/project-e", declined["target"])
	// f was not named, so it is still awaiting its own decision.
	pending := body["pending_consent"].([]any)
	require.Len(t, pending, 1)
	require.Equal(t, "f", pending[0].(map[string]any)["name"])
	// The rows already say what happened to e and f, so the note under the
	// table does not repeat them.
	require.Empty(t, notAppliedNames(body))

	// Allowing a declined entry takes it back: the same call, the other answer.
	rec = doJSON(t, f.server, http.MethodPost, "/api/v1/projects/"+f.projectID+"/mcp/consent",
		map[string]any{"allow": []string{"e"}, "decide": []string{"e"},
			"fingerprints": []any{map[string]any{"name": "e", "fingerprint": rowFingerprint("e")}}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet,
		"/api/v1/projects/"+f.projectID+"/mcp?page=1&page_size=20", nil))
	allowed := mcpServerRow(t, body, "e")
	require.Equal(t, true, allowed["allowed"])
	require.NotContains(t, allowed, "declined")
	require.Len(t, body["pending_consent"].([]any), 1)

	// A request that decides no entry changes no entry.
	rec = doJSON(t, f.server, http.MethodPost, "/api/v1/projects/"+f.projectID+"/mcp/consent",
		map[string]any{"allow": []string{}, "decide": []string{}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet,
		"/api/v1/projects/"+f.projectID+"/mcp?page=1&page_size=20", nil))
	require.Len(t, body["pending_consent"].([]any), 1)
	require.Equal(t, "f", body["pending_consent"].([]any)[0].(map[string]any)["name"])
}

// TestProjectMCPConsentWithoutDecideKeepsDecidingAll pins the semantics the
// startup prompt and every pre-existing caller use: a request that names no
// entries answers for the whole batch, so an entry left out of allow is
// recorded as declined rather than left pending.
func TestProjectMCPConsentWithoutDecideKeepsDecidingAll(t *testing.T) {
	f := newMCPPreviewFixture(t)

	rec := doJSON(t, f.server, http.MethodPost, "/api/v1/projects/"+f.projectID+"/mcp/consent",
		map[string]any{"allow": []string{}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet,
		"/api/v1/projects/"+f.projectID+"/mcp?page=1&page_size=20", nil))
	// e is now declined and stays in the table; d keeps its allow.
	require.Equal(t, []string{"d", "e", "b", "a"}, mcpReadRowNames(t, body))
	declined := mcpServerRow(t, body, "e")
	require.Equal(t, true, declined["declined"])
	require.NotContains(t, declined, "pending")
	require.Equal(t, true, mcpServerRow(t, body, "d")["allowed"])
	require.Empty(t, body["pending_consent"].([]any))
}

// TestProjectMCPConsentDecideRefusesAStaleFingerprint pins the binding the
// per-row decision now carries: the fingerprint travels with the row, and a
// click against a fingerprint the file no longer matches is refused whole —
// 409, nothing recorded, the entry still pending — until the page reloads and
// the operator confirms the entry the file now holds.
func TestProjectMCPConsentDecideRefusesAStaleFingerprint(t *testing.T) {
	f := newMCPPreviewFixture(t)

	rowFingerprint := func(name string) string {
		t.Helper()
		body := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet,
			"/api/v1/projects/"+f.projectID+"/mcp?page=1&page_size=20", nil))
		fp, ok := mcpServerRow(t, body, name)["fingerprint"].(string)
		require.True(t, ok && fp != "", "row %q carries no fingerprint", name)
		return fp
	}
	// The page read e; the file moves under it before the click.
	shown := rowFingerprint("e")
	file := filepath.Join(f.root, ".forebrain", "mcp_servers.yaml")
	require.NoError(t, os.WriteFile(file, []byte(
		"mcp_servers:\n"+
			"  - name: d\n    transport: stdio\n    command: /bin/project-d\n"+
			"  - name: e\n    transport: stdio\n    command: /bin/project-e-rewritten\n"), 0o644))

	rec := doJSON(t, f.server, http.MethodPost, "/api/v1/projects/"+f.projectID+"/mcp/consent",
		map[string]any{"allow": []string{"e"}, "decide": []string{"e"},
			"fingerprints": []any{map[string]any{"name": "e", "fingerprint": shown}}})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var refused struct {
		Error string   `json:"error"`
		Code  string   `json:"code"`
		Stale []string `json:"stale"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &refused))
	require.Equal(t, "consent_stale", refused.Code)
	require.Equal(t, []string{"e"}, refused.Stale)
	require.NotEmpty(t, refused.Error)

	// Nothing was recorded: e is exactly as pending as before, under the
	// fingerprint the file now has.
	body := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet,
		"/api/v1/projects/"+f.projectID+"/mcp?page=1&page_size=20", nil))
	row := mcpServerRow(t, body, "e")
	require.NotContains(t, row, "allowed")
	require.NotContains(t, row, "declined")
	require.Equal(t, true, row["pending"])
	require.Equal(t, "/bin/project-e-rewritten", row["target"])

	// The page's own recovery: reload the rows, send the fingerprint they now
	// carry, and the same click goes through.
	refreshed := rowFingerprint("e")
	require.NotEqual(t, shown, refreshed)
	rec = doJSON(t, f.server, http.MethodPost, "/api/v1/projects/"+f.projectID+"/mcp/consent",
		map[string]any{"allow": []string{"e"}, "decide": []string{"e"},
			"fingerprints": []any{map[string]any{"name": "e", "fingerprint": refreshed}}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet,
		"/api/v1/projects/"+f.projectID+"/mcp?page=1&page_size=20", nil))
	require.Equal(t, true, mcpServerRow(t, body, "e")["allowed"])
}

// TestProjectMCPConsentDecideRequiresFingerprints: a request that decides
// named entries without saying what it was shown for them decides nothing.
func TestProjectMCPConsentDecideRequiresFingerprints(t *testing.T) {
	f := newMCPPreviewFixture(t)

	rec := doJSON(t, f.server, http.MethodPost, "/api/v1/projects/"+f.projectID+"/mcp/consent",
		map[string]any{"allow": []string{"e"}, "decide": []string{"e"}})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `fingerprint required for "e"`)

	// Nothing was recorded: e is still awaiting its decision.
	body := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet,
		"/api/v1/projects/"+f.projectID+"/mcp?page=1&page_size=20", nil))
	require.Equal(t, true, mcpServerRow(t, body, "e")["pending"])
}

// TestProjectMCPPreviewWithoutPageKeepsOldFields pins the backward half of the
// paging contract: a caller that sends no page still gets the answer this
// endpoint always gave — the effective list in the merge's own order with the
// unconfirmed entries in pending_consent alone, and the three project notes —
// because other callers read it that way and must not suddenly receive a
// table.
func TestProjectMCPPreviewWithoutPageKeepsOldFields(t *testing.T) {
	f := newMCPPreviewFixture(t)

	body := decodeMCPServersBody(t, doJSON(t, f.server, http.MethodGet, "/api/v1/projects/"+f.projectID+"/mcp", nil))
	// Merge order, not the table's: the shared layer first, then the agent's,
	// then the project's own — and no pending row.
	require.Equal(t, []string{"a", "b", "d"}, mcpReadRowNames(t, body))
	require.NotContains(t, body, "total")
	require.NotContains(t, body, "page")
	require.NotContains(t, body, "page_size")
	require.NotContains(t, body, "pageSize")

	overridden, ok := body["overridden_global"]
	require.True(t, ok, "the project notes stay: %#v", body)
	require.Nil(t, overridden)
	require.NotNil(t, body["not_applied"])
	pending, ok := body["pending_consent"].([]any)
	require.True(t, ok, "pending_consent stays a list: %#v", body["pending_consent"])
	require.Len(t, pending, 1)
	item, _ := pending[0].(map[string]any)
	require.Equal(t, "e", item["name"])
	require.NotEmpty(t, item["summary"])
}

// project_pages_test.go covers the paged reads of a project space (plan 075):
// the conversation, rule-file, permission, language-server, scheduled-task and
// channel tables. Each endpoint keeps the answer it always gave when the request
// carries no "page" — non-table callers read them too — and answers one server
// page, with the total, when it does.

// projectPagesServer is one project space's server: a trusted, version-controlled
// project, its session store, and the cron service the session list reads its
// heartbeats from.
func projectPagesServer(t *testing.T) (*Server, state.Project, string) {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	cfgPath := filepath.Join(home, "forebrain.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("agents:\n  definitions:\n    main:\n      primary: true\n"), 0o600))
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{"main": {Primary: true}}
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	workspace := filepath.Join(home, "workspace")
	runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", WorkspaceRoot: workspace}}
	env := &process.Environment{Root: home, SQL: db, Runner: runner, Deps: run.Deps{Home: home, AppCfg: cfg}}
	env.ConfigPath = cfgPath
	root := filepath.Join(home, "proj")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".forebrain"), 0o755))
	require.NoError(t, safety.MarkTrusted(home, safety.Project{Root: root}))
	projects := state.NewProjectStore(db, "main")
	p, err := projects.Create(ctx, state.CreateProjectInput{Name: "proj", Root: root})
	require.NoError(t, err)
	return &Server{
		Home:     home,
		Runner:   runner,
		Env:      env,
		Projects: projects,
		Sessions: state.NewSessionStore(db, "main"),
	}, p, workspace
}

// seedProjectConversation creates one conversation of the project holding the
// given messages, at the given creation and update times.
func seedProjectConversation(t *testing.T, s *Server, p state.Project, id, title string, createdAt, updatedAt int64, messages ...[2]string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.Sessions.Ensure(ctx, id, title))
	require.NoError(t, s.Projects.BindSession(ctx, id, p.ID))
	for _, message := range messages {
		_, err := s.Env.SQL.Exec(
			`INSERT INTO fb_messages(session_id, role, visibility, content, created_at) VALUES(?,?,?,?,?)`,
			id, message[0], message[1], "text", 1)
		require.NoError(t, err)
	}
	_, err := s.Env.SQL.Exec(`UPDATE fb_sessions SET created_at = ?, updated_at = ? WHERE id = ?`, createdAt, updatedAt, id)
	require.NoError(t, err)
}

// decodeBody reads one list answer into a generic map.
func decodeBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}

// TestProjectSessionsListPagedCarriesModelCountAndHeartbeat pins the conversation
// table's rows: the model the session pinned, the messages a person exchanged,
// when it was created, and its heartbeat.
func TestProjectSessionsListPagedCarriesModelCountAndHeartbeat(t *testing.T) {
	ctx := context.Background()
	s, p, _ := projectPagesServer(t)
	seedProjectConversation(t, s, p, "sess-a", "First", 100, 400,
		[2]string{"user", "visible"}, [2]string{"assistant", "visible"}, [2]string{"tool", "visible"})
	seedProjectConversation(t, s, p, "sess-b", "Second", 200, 300, [2]string{"user", "visible"})
	_, err := s.Sessions.SaveSessionModelSelection(ctx, "sess-a", state.SessionModelSelection{Provider: "anthropic", Model: "claude-sonnet-4"})
	require.NoError(t, err)
	_, err = s.cron().SetHeartbeat(ctx, state.Heartbeat{SessionID: "sess-b", IntervalSec: 600, Prompt: "check the build"}, false)
	require.NoError(t, err)

	rec := cronRequest(t, s, http.MethodGet, "/api/projects/"+p.ID+"/sessions?page=1&page_size=10", nil, s.handleProjectSessionsList, "id", p.ID)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	answer := decodeBody(t, rec.Body.Bytes())
	require.Equal(t, float64(2), answer["total"])
	require.Equal(t, float64(1), answer["page"])
	require.Equal(t, float64(10), answer["page_size"])
	rows, ok := answer["sessions"].([]any)
	require.True(t, ok)
	require.Len(t, rows, 2)

	first := rows[0].(map[string]any)
	require.Equal(t, "sess-a", first["id"])
	require.Equal(t, "First", first["title"])
	require.Equal(t, float64(100), first["created_at"])
	require.Equal(t, float64(400), first["updated_at"])
	require.Equal(t, "claude-sonnet-4", first["model"])
	// Two assistant/user rows: the tool row is work, not a message.
	require.Equal(t, float64(2), first["message_count"])
	require.NotContains(t, first, "heartbeat")

	second := rows[1].(map[string]any)
	// A session that never pinned a model answers an empty name rather than the
	// agent's default.
	require.Equal(t, "", second["model"])
	require.Equal(t, float64(1), second["message_count"])
	beat, ok := second["heartbeat"].(map[string]any)
	require.True(t, ok, "a conversation with a heartbeat must carry it: %v", second)
	require.Equal(t, float64(600), beat["interval_seconds"])
	require.Equal(t, false, beat["paused"])

	// The table's own sort: by creation time, newest first when asked.
	sorted := cronRequest(t, s, http.MethodGet, "/api/projects/"+p.ID+"/sessions?page=1&page_size=10&sort=created&order=asc", nil, s.handleProjectSessionsList, "id", p.ID)
	require.Equal(t, http.StatusOK, sorted.Code, sorted.Body.String())
	sortedRows := decodeBody(t, sorted.Body.Bytes())["sessions"].([]any)
	require.Equal(t, "sess-a", sortedRows[0].(map[string]any)["id"])
}

// TestProjectSessionsListWithoutPageKeepsTheOldAnswer pins the 071 contract: a
// request with no "page" gets exactly the answer it always got — the old fields,
// no derived ones and no pager.
func TestProjectSessionsListWithoutPageKeepsTheOldAnswer(t *testing.T) {
	s, p, _ := projectPagesServer(t)
	seedProjectConversation(t, s, p, "sess-old", "Old answer", 100, 400, [2]string{"user", "visible"})

	rec := cronRequest(t, s, http.MethodGet, "/api/projects/"+p.ID+"/sessions", nil, s.handleProjectSessionsList, "id", p.ID)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, `{"sessions":[{"id":"sess-old","title":"Old answer","updated_at":400}]}`+"\n", rec.Body.String())
}

// channelSeed writes one channel row as the form would have saved it.
func channelSeed(t *testing.T, s *Server, projectID, kind, configJSON string, enabled bool) {
	t.Helper()
	require.NoError(t, state.NewProjectChannelStore(s.Env.SQL, "main").Upsert(context.Background(), state.ProjectChannel{
		ProjectID:  projectID,
		Kind:       kind,
		Enabled:    enabled,
		ConfigJSON: configJSON,
	}))
}

// channelRows reads a paged channel answer into a map by kind.
func channelRows(t *testing.T, body []byte) map[string]map[string]any {
	t.Helper()
	answer := decodeBody(t, body)
	rows, ok := answer["rows"].([]any)
	require.True(t, ok, "a paged channel answer carries rows: %s", body)
	out := map[string]map[string]any{}
	for _, row := range rows {
		item := row.(map[string]any)
		out[item["kind"].(string)] = item
	}
	return out
}

// TestChannelsReportConfiguredOnlyForSavedKinds pins what "configured" means: a
// kind holds a configuration once the form saved one. A row the engine defaults
// the fields from, and a row saved empty, are both unconfigured.
func TestChannelsReportConfiguredOnlyForSavedKinds(t *testing.T) {
	s, p, _ := projectPagesServer(t)
	channelSeed(t, s, p.ID, "feishu", `{"app_id":"cli_123","app_secret":"plain-secret-9876"}`, true)
	channelSeed(t, s, p.ID, "telegram", `{}`, false)

	rec := cronRequest(t, s, http.MethodGet, "/api/projects/"+p.ID+"/channels?page=1&page_size=40", nil, s.handleChannels, "id", p.ID)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rows := channelRows(t, rec.Body.Bytes())
	// Every kind the engine can build is offered, so a kind with nothing stored
	// can still be configured.
	require.Len(t, rows, 18)
	require.Equal(t, true, rows["feishu"]["configured"])
	require.Equal(t, true, rows["feishu"]["enabled"])
	require.Equal(t, false, rows["telegram"]["configured"], "a row saved empty holds no configuration")
	require.Equal(t, false, rows["slack"]["configured"], "a kind with no row is unconfigured")
	require.Equal(t, "飞书", rows["feishu"]["label_zh"])
	require.Equal(t, "Feishu", rows["feishu"]["label_en"])
	require.Equal(t, false, rows["slack"]["enabled"])
	// An unconfigured kind still offers every field its configuration
	// declares, or the edit form would have nothing to type into.
	fields, _ := rows["slack"]["fields"].(map[string]any)
	require.Equal(t, "", fields["bot_token"], "an unset field is empty, not missing")
	require.Contains(t, fields, "bot_token")
	require.Contains(t, fields, "secret")
}

// TestChannelsPagedRowsCarrySecretHints pins the masked hint a page shows for a
// stored secret: the mask and the last four characters, read from the stored
// value, never the value itself.
func TestChannelsPagedRowsCarrySecretHints(t *testing.T) {
	s, p, _ := projectPagesServer(t)
	require.NoError(t, os.WriteFile(filepath.Join(s.Home, ".env"), []byte("FEISHU_APP_SECRET=plain-secret-9876\n"), 0o600))
	// A secret the page typed is stored as a ${VAR} reference, which is what the
	// hint has to resolve through the home's .env.
	channelSeed(t, s, p.ID, "feishu", `{"app_id":"cli_123","app_secret":"${FEISHU_APP_SECRET}"}`, true)

	rec := cronRequest(t, s, http.MethodGet, "/api/projects/"+p.ID+"/channels?page=1&page_size=40", nil, s.handleChannels, "id", p.ID)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "plain-secret-9876", "a stored secret must never travel")

	feishu := channelRows(t, rec.Body.Bytes())["feishu"]
	keys, ok := feishu["secret_keys"].([]any)
	require.True(t, ok)
	require.Contains(t, keys, "app_secret")
	hints, ok := feishu["secret_hints"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "\u2022\u2022\u2022\u20229876", hints["app_secret"])
	fields, ok := feishu["fields"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "cli_123", fields["app_id"])
	require.Equal(t, "[REDACTED]", fields["app_secret"])

	// The search box finds a kind by either of its names.
	searched := cronRequest(t, s, http.MethodGet, "/api/projects/"+p.ID+"/channels?page=1&page_size=40&q=飞书", nil, s.handleChannels, "id", p.ID)
	require.Equal(t, http.StatusOK, searched.Code, searched.Body.String())
	require.Len(t, channelRows(t, searched.Body.Bytes()), 1)

	// Without "page" the answer is the one the page always read: the whole map of
	// kind → fields, and no rows.
	whole := cronRequest(t, s, http.MethodGet, "/api/projects/"+p.ID+"/channels", nil, s.handleChannels, "id", p.ID)
	require.Equal(t, http.StatusOK, whole.Code, whole.Body.String())
	answer := decodeBody(t, whole.Body.Bytes())
	require.NotContains(t, answer, "rows")
	require.NotContains(t, answer, "total")
	kinds, ok := answer["kinds"].([]any)
	require.True(t, ok)
	require.Len(t, kinds, 18)
	channels, ok := answer["channels"].(map[string]any)
	require.True(t, ok)
	feishuFields := channels["feishu"].(map[string]any)
	require.Equal(t, "cli_123", feishuFields["app_id"])
	require.Equal(t, "[REDACTED]", feishuFields["app_secret"])
	require.Equal(t, true, feishuFields["enabled"])
}

// TestProjectRuleFilesPagedAndWhole pins the rule-file table: searched by file
// path, paged on the server, with the layers that hold no file still offered to
// the create dialog.
func TestProjectRuleFilesPagedAndWhole(t *testing.T) {
	s, p, _ := projectPagesServer(t)
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, projectRuleFile), []byte("root rules"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(p.Root, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, "docs", projectRuleFile), []byte("docs rules"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(p.Root, "internal"), 0o755))

	whole := cronRequest(t, s, http.MethodGet, "/api/rules/project/"+p.ID, nil, s.handleProjectRuleFiles, "projectId", p.ID)
	require.Equal(t, http.StatusOK, whole.Code, whole.Body.String())
	wholeAnswer := decodeBody(t, whole.Body.Bytes())
	require.NotContains(t, wholeAnswer, "total")
	require.Len(t, wholeAnswer["files"].([]any), 2)
	require.Equal(t, []any{".forebrain", "internal"}, wholeAnswer["create"])

	paged := cronRequest(t, s, http.MethodGet, "/api/rules/project/"+p.ID+"?page=1&page_size=1", nil, s.handleProjectRuleFiles, "projectId", p.ID)
	require.Equal(t, http.StatusOK, paged.Code, paged.Body.String())
	pagedAnswer := decodeBody(t, paged.Body.Bytes())
	require.Equal(t, float64(2), pagedAnswer["total"])
	require.Len(t, pagedAnswer["files"].([]any), 1)
	require.Equal(t, "", pagedAnswer["files"].([]any)[0].(map[string]any)["dir"], "the root layer lists first")
	// The create list is not a page: the dialog offers every layer that has no
	// file, whatever the search or the page leaves.
	require.Equal(t, []any{".forebrain", "internal"}, pagedAnswer["create"])

	searched := cronRequest(t, s, http.MethodGet, "/api/rules/project/"+p.ID+"?page=1&page_size=10&q=docs", nil, s.handleProjectRuleFiles, "projectId", p.ID)
	require.Equal(t, http.StatusOK, searched.Code, searched.Body.String())
	searchedAnswer := decodeBody(t, searched.Body.Bytes())
	require.Equal(t, float64(1), searchedAnswer["total"])
	require.Equal(t, "docs", searchedAnswer["files"].([]any)[0].(map[string]any)["dir"])
}

// TestProjectPermissionRulesPagedAndWhole pins the project permission table: the
// project's own rules, searched over the tool and the rule's text, with "applies"
// beside the page.
func TestProjectPermissionRulesPagedAndWhole(t *testing.T) {
	s, p, _ := projectPagesServer(t)
	update := cronRequest(t, s, http.MethodPost, "/api/v1/projects/"+p.ID+"/permissions/updates",
		map[string]any{
			"type":     "addRules",
			"behavior": "deny",
			"rules": []map[string]any{
				{"tool_name": "Bash", "rule_content": "git push:*"},
				{"tool_name": "Bash", "rule_content": "rm -rf:*"},
			},
		}, s.handleProjectPermissionUpdate, "id", p.ID)
	require.Equal(t, http.StatusOK, update.Code, update.Body.String())

	whole := cronRequest(t, s, http.MethodGet, "/api/v1/projects/"+p.ID+"/permissions/rules", nil, s.handleProjectPermissionRules, "id", p.ID)
	require.Equal(t, http.StatusOK, whole.Code, whole.Body.String())
	wholeAnswer := decodeBody(t, whole.Body.Bytes())
	require.Equal(t, true, wholeAnswer["applies"])
	require.NotContains(t, wholeAnswer, "total")
	require.Len(t, wholeAnswer["rules"].([]any), 2)

	paged := cronRequest(t, s, http.MethodGet, "/api/v1/projects/"+p.ID+"/permissions/rules?page=1&page_size=1", nil, s.handleProjectPermissionRules, "id", p.ID)
	require.Equal(t, http.StatusOK, paged.Code, paged.Body.String())
	pagedAnswer := decodeBody(t, paged.Body.Bytes())
	require.Equal(t, true, pagedAnswer["applies"])
	require.Equal(t, float64(2), pagedAnswer["total"])
	require.Len(t, pagedAnswer["rules"].([]any), 1)

	searched := cronRequest(t, s, http.MethodGet, "/api/v1/projects/"+p.ID+"/permissions/rules?page=1&page_size=10&q=git+push", nil, s.handleProjectPermissionRules, "id", p.ID)
	require.Equal(t, http.StatusOK, searched.Code, searched.Body.String())
	searchedAnswer := decodeBody(t, searched.Body.Bytes())
	require.Equal(t, float64(1), searchedAnswer["total"])
	require.Contains(t, searchedAnswer["rules"].([]any)[0].(map[string]any)["rule_content"], "git push")
}

// TestProjectLSPPreviewPagedAndWhole pins the language-server table: pending,
// allowed and denied entries as one list in that order, searched by id and
// summary, while the old answer keeps its three separate lists.
func TestProjectLSPPreviewPagedAndWhole(t *testing.T) {
	s, p, _ := projectPagesServer(t)
	serversPath := filepath.Join(p.Root, ".forebrain", "lsp_servers.yaml")
	writeServers := func(body string) {
		t.Helper()
		require.NoError(t, os.WriteFile(serversPath, []byte(body), 0o644))
	}
	writeServers("servers:\n" +
		"  als: {command: /bin/als, extension_to_language: {\".al\": als}}\n" +
		"  bls: {command: /bin/bls, extension_to_language: {\".bl\": bls}}\n" +
		"  cls: {command: /bin/cls, extension_to_language: {\".cl\": cls}}\n")

	consent := cronRequest(t, s, http.MethodPost, "/api/v1/projects/"+p.ID+"/lsp/consent",
		map[string]any{"allow": []string{"als"}}, s.handleProjectLSPConsent, "id", p.ID)
	require.Equal(t, http.StatusOK, consent.Code, consent.Body.String())
	// One entry still awaits confirmation, so all three decisions are on the table.
	writeServers("servers:\n" +
		"  als: {command: /bin/als, extension_to_language: {\".al\": als}}\n" +
		"  bls: {command: /bin/bls, extension_to_language: {\".bl\": bls}}\n" +
		"  cls: {command: /bin/cls, extension_to_language: {\".cl\": cls}}\n" +
		"  dls: {command: /bin/dls, extension_to_language: {\".dl\": dls}}\n")

	whole := cronRequest(t, s, http.MethodGet, "/api/v1/projects/"+p.ID+"/lsp", nil, s.handleProjectLSPPreview, "id", p.ID)
	require.Equal(t, http.StatusOK, whole.Code, whole.Body.String())
	wholeAnswer := decodeBody(t, whole.Body.Bytes())
	require.Equal(t, true, wholeAnswer["trusted"])
	require.NotContains(t, wholeAnswer, "total")
	require.Len(t, wholeAnswer["pending"].([]any), 1)
	require.Equal(t, []any{"als"}, wholeAnswer["allowed"])
	require.Equal(t, []any{"bls", "cls"}, wholeAnswer["denied"])

	paged := cronRequest(t, s, http.MethodGet, "/api/v1/projects/"+p.ID+"/lsp?page=1&page_size=2", nil, s.handleProjectLSPPreview, "id", p.ID)
	require.Equal(t, http.StatusOK, paged.Code, paged.Body.String())
	pagedAnswer := decodeBody(t, paged.Body.Bytes())
	require.Equal(t, true, pagedAnswer["trusted"])
	require.Equal(t, float64(4), pagedAnswer["total"])
	firstPage := pagedAnswer["rows"].([]any)
	require.Len(t, firstPage, 2)
	// Waiting for confirmation leads; what was decided follows.
	require.Equal(t, "dls", firstPage[0].(map[string]any)["id"])
	require.Equal(t, "pending", firstPage[0].(map[string]any)["status"])
	require.Contains(t, firstPage[0].(map[string]any)["summary"], "/bin/dls")
	require.Equal(t, "als", firstPage[1].(map[string]any)["id"])
	require.Equal(t, "allowed", firstPage[1].(map[string]any)["status"])
	// The consent store keeps a decided entry by id, so it has no summary.
	require.Equal(t, "", firstPage[1].(map[string]any)["summary"])

	searched := cronRequest(t, s, http.MethodGet, "/api/v1/projects/"+p.ID+"/lsp?page=1&page_size=10&q=cls", nil, s.handleProjectLSPPreview, "id", p.ID)
	require.Equal(t, http.StatusOK, searched.Code, searched.Body.String())
	searchedAnswer := decodeBody(t, searched.Body.Bytes())
	require.Equal(t, float64(1), searchedAnswer["total"])
	require.Equal(t, "cls", searchedAnswer["rows"].([]any)[0].(map[string]any)["id"])
	require.Equal(t, "denied", searchedAnswer["rows"].([]any)[0].(map[string]any)["status"])
}

// TestCronJobsPagedAndWhole pins the scheduled-task table: searched over the name
// and the prompt, paged on the server, while the old answer keeps its whole list.
func TestCronJobsPagedAndWhole(t *testing.T) {
	ctx := context.Background()
	s, p, _ := projectPagesServer(t)
	for _, job := range []struct{ name, prompt string }{
		{"morning brief", "summarise the inbox"},
		{"nightly sweep", "look for stale issues"},
	} {
		_, err := s.cron().CreateJob(ctx, "main", process.CronJobInput{
			ProjectID: p.ID,
			Name:      job.name,
			Schedule:  "every 1h",
			Prompt:    job.prompt,
		})
		require.NoError(t, err)
	}

	whole := cronRequest(t, s, http.MethodGet, "/api/v1/projects/"+p.ID+"/cron/jobs", nil, s.handleCronJobs, "id", p.ID)
	require.Equal(t, http.StatusOK, whole.Code, whole.Body.String())
	wholeAnswer := decodeBody(t, whole.Body.Bytes())
	require.Equal(t, p.ID, wholeAnswer["project_id"])
	require.NotContains(t, wholeAnswer, "total")
	require.Len(t, wholeAnswer["records"].([]any), 2)

	paged := cronRequest(t, s, http.MethodGet, "/api/v1/projects/"+p.ID+"/cron/jobs?page=1&page_size=1", nil, s.handleCronJobs, "id", p.ID)
	require.Equal(t, http.StatusOK, paged.Code, paged.Body.String())
	pagedAnswer := decodeBody(t, paged.Body.Bytes())
	require.Equal(t, float64(2), pagedAnswer["total"])
	require.Len(t, pagedAnswer["records"].([]any), 1)

	searched := cronRequest(t, s, http.MethodGet, "/api/v1/projects/"+p.ID+"/cron/jobs?page=1&page_size=10&q=stale", nil, s.handleCronJobs, "id", p.ID)
	require.Equal(t, http.StatusOK, searched.Code, searched.Body.String())
	searchedAnswer := decodeBody(t, searched.Body.Bytes())
	require.Equal(t, float64(1), searchedAnswer["total"])
	require.Equal(t, "nightly sweep", searchedAnswer["records"].([]any)[0].(map[string]any)["name"])
}

// TestCronJobRunsPagedAndWhole pins the run-history read: a page with its total
// when the request pages, the old limited list when it does not.
func TestCronJobRunsPagedAndWhole(t *testing.T) {
	ctx := context.Background()
	s, p, _ := projectPagesServer(t)
	job, err := s.cron().CreateJob(ctx, "main", process.CronJobInput{
		ProjectID: p.ID,
		Name:      "morning brief",
		Schedule:  "every 1h",
		Prompt:    "summarise the inbox",
	})
	require.NoError(t, err)
	store := &state.CronStore{DB: s.Env.SQL}
	for _, startedAt := range []int64{100, 200, 300} {
		_, err := store.StartRun(ctx, state.CronRun{JobID: job.ID, AgentID: "main", Trigger: "schedule", StartedAt: startedAt})
		require.NoError(t, err)
	}

	whole := cronRequest(t, s, http.MethodGet, "/api/v1/projects/"+p.ID+"/cron/jobs/"+job.ID+"/runs?limit=1", nil, s.handleCronJobRuns, "id", p.ID, "jobId", job.ID)
	require.Equal(t, http.StatusOK, whole.Code, whole.Body.String())
	wholeAnswer := decodeBody(t, whole.Body.Bytes())
	require.Equal(t, job.ID, wholeAnswer["job_id"])
	require.NotContains(t, wholeAnswer, "total")
	require.Len(t, wholeAnswer["records"].([]any), 1)

	paged := cronRequest(t, s, http.MethodGet, "/api/v1/projects/"+p.ID+"/cron/jobs/"+job.ID+"/runs?page=1&page_size=2", nil, s.handleCronJobRuns, "id", p.ID, "jobId", job.ID)
	require.Equal(t, http.StatusOK, paged.Code, paged.Body.String())
	pagedAnswer := decodeBody(t, paged.Body.Bytes())
	require.Equal(t, float64(3), pagedAnswer["total"])
	require.Equal(t, job.ID, pagedAnswer["job_id"])
	runs := pagedAnswer["runs"].([]any)
	require.Len(t, runs, 2)
	// History reads newest first.
	require.Equal(t, float64(300), runs[0].(map[string]any)["started_at"])
	require.Equal(t, float64(200), runs[1].(map[string]any)["started_at"])

	last := cronRequest(t, s, http.MethodGet, "/api/v1/projects/"+p.ID+"/cron/jobs/"+job.ID+"/runs?page=9&page_size=2", nil, s.handleCronJobRuns, "id", p.ID, "jobId", job.ID)
	require.Equal(t, http.StatusOK, last.Code, last.Body.String())
	lastAnswer := decodeBody(t, last.Body.Bytes())
	require.Equal(t, float64(2), lastAnswer["page"], "a page past the end serves the last page")
	require.Len(t, lastAnswer["runs"].([]any), 1)
}

// TestChannelLabelsMatchFrontendJSON holds the two copies of the channel display
// names together: the frontend's JSON is the one source, this package mirrors it
// so the table can be searched by what the page shows.
func TestChannelLabelsMatchFrontendJSON(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "frontend", "src", "lib", "channelLabels.json"))
	require.NoError(t, err)
	var frontend map[string][]string
	require.NoError(t, json.Unmarshal(raw, &frontend))

	require.Len(t, frontend, len(channelLabels), "the two tables name a different number of kinds")
	for kind, names := range frontend {
		require.Len(t, names, 2, "%s: the JSON entry must be [中文, English]", kind)
		zh, en := channelLabel(kind)
		require.Equal(t, names[0], zh, "%s: 中文名字", kind)
		require.Equal(t, names[1], en, "%s: English name", kind)
	}
	for _, kind := range channel.ChannelKinds {
		require.Contains(t, frontend, kind, "every channel kind the engine builds needs a display name")
	}
	// A kind the table does not name reads as itself.
	zh, en := channelLabel("not-a-kind")
	require.Equal(t, "not-a-kind", zh)
	require.Equal(t, "not-a-kind", en)
}

// TestCronJobEditWithoutDeliverKeepsTheJobsTarget pins the pause switch: it sends
// only {"enabled": …}, and pausing a job must not make it stop posting where it
// posted before.
func TestCronJobEditWithoutDeliverKeepsTheJobsTarget(t *testing.T) {
	ctx := context.Background()
	s, p, _ := projectPagesServer(t)
	deliver := "feishu"
	job, err := s.cron().CreateJob(ctx, "main", process.CronJobInput{
		ProjectID:  p.ID,
		Name:       "morning brief",
		Schedule:   "every 1h",
		Prompt:     "summarise the inbox",
		Deliver:    deliver,
		DeliverSet: true,
	})
	require.NoError(t, err)
	require.Equal(t, deliver, job.Deliver)

	paused := cronRequest(t, s, http.MethodPut, "/api/v1/projects/"+p.ID+"/cron/jobs/"+job.ID,
		map[string]any{"enabled": false}, s.handleCronJob, "id", p.ID, "jobId", job.ID)
	require.Equal(t, http.StatusOK, paused.Code, paused.Body.String())
	pausedJob, err := s.cron().Job(ctx, "main", job.ID)
	require.NoError(t, err)
	require.False(t, pausedJob.Enabled)
	require.Equal(t, deliver, pausedJob.Deliver, "pausing kept the delivery target")

	// An edit that does name one still changes it, and one that names none
	// clears it: the pointer is what tells the two apart.
	switched := cronRequest(t, s, http.MethodPut, "/api/v1/projects/"+p.ID+"/cron/jobs/"+job.ID,
		map[string]any{"deliver": "telegram"}, s.handleCronJob, "id", p.ID, "jobId", job.ID)
	require.Equal(t, http.StatusOK, switched.Code, switched.Body.String())
	switchedJob, err := s.cron().Job(ctx, "main", job.ID)
	require.NoError(t, err)
	require.Equal(t, "telegram", switchedJob.Deliver)

	cleared := cronRequest(t, s, http.MethodPut, "/api/v1/projects/"+p.ID+"/cron/jobs/"+job.ID,
		map[string]any{"deliver": ""}, s.handleCronJob, "id", p.ID, "jobId", job.ID)
	require.Equal(t, http.StatusOK, cleared.Code, cleared.Body.String())
	clearedJob, err := s.cron().Job(ctx, "main", job.ID)
	require.NoError(t, err)
	require.Equal(t, "", clearedJob.Deliver)
}

// TestWithConfigWriteSerialisesWriters pins the guarantee the config write
// paths lean on: only one read-modify-write of forebrain.yaml runs at a time,
// so a save that started from a stale file cannot overwrite another's change.
func TestWithConfigWriteSerialisesWriters(t *testing.T) {
	s := &Server{}

	var mu sync.Mutex
	inside, peak, ran := 0, 0, 0
	errs := make([]error, 8)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.withConfigWrite(func() error {
				mu.Lock()
				inside++
				ran++
				if inside > peak {
					peak = inside
				}
				mu.Unlock()
				time.Sleep(5 * time.Millisecond)
				mu.Lock()
				inside--
				mu.Unlock()
				return nil
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "writer %d", i)
	}
	require.Equal(t, 1, peak, "writers must never overlap")
	require.Equal(t, 8, ran, "every writer must have run")
	require.Equal(t, 0, inside)
}

// providersGetPaged exercises the paged GET branch, which must carry the same
// agent-level default_model the plain GET does.
func providersGetPaged(t *testing.T, s *Server) (string, []map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/providers?page=1", nil)
	s.handleProviders(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var out struct {
		DefaultModel string           `json:"default_model"`
		Items        []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	return out.DefaultModel, out.Items
}

func providersGetDefaultModel(t *testing.T, s *Server) string {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handleProviders(rr, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var out struct {
		DefaultModel string `json:"default_model"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	return out.DefaultModel
}

// seedProvidersConfig writes main's llm_providers (one entry per line,
// provider/model pairs) straight to the yaml, in the exact order given.
func seedProvidersConfig(t *testing.T, home string, extraTop string, pairs ...[2]string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("agents:\n  definitions:\n    main:\n      primary: true\n")
	b.WriteString(extraTop)
	b.WriteString("      llm_providers:\n")
	for _, p := range pairs {
		b.WriteString("      - provider: " + p[0] + "\n        model: " + p[1] + "\n        api_key: ${DEEPSEEK_API_KEY}\n        base_url: https://api.deepseek.com\n")
	}
	cfgPath := filepath.Join(home, "forebrain.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(b.String()), 0o600))
	return cfgPath
}

// The PUT must not write the client's row order into the config: the disk
// order is the base, known rows update in place, dropped rows are deleted,
// and genuinely new rows are appended at the end.
func TestProvidersDTOPutPreservesDiskOrder(t *testing.T) {
	s, home := providersDTOServer(t)
	cfgPath := seedProvidersConfig(t, home, "",
		[2]string{"deepseek", "deepseek-v4"},
		[2]string{"openai", "gpt-test"},
		[2]string{"moonshotai", "kimi"},
	)

	// The page reorders two rows (moonshot first), drops openai, and adds a
	// brand-new row — as a stale snapshot written back whole would.
	rr := providersPut(t, s, `{"providers":[
		{"provider":"moonshotai","models":["kimi"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-keep-1111"},
		{"provider":"deepseek","models":["deepseek-v4"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-keep-2222"},
		{"provider":"zhipuai","models":["glm-5.3"],"base_url":"https://open.bigmodel.cn/api/paas/v4","api_key_plain":"sk-new-3333"}
	]}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	cfgRaw, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	require.Equal(t, []string{"deepseek", "moonshotai", "zhipuai"}, providerOrderInYAML(t, string(cfgRaw)), "disk order must be preserved: existing rows in place, new row appended at the end")
}

func providerOrderInYAML(t *testing.T, raw string) []string {
	t.Helper()
	cfg, err := appcfg.ParseRootYAML([]byte(raw))
	require.NoError(t, err)
	out := []string{}
	for _, item := range cfg.Agents.Definitions["main"].LLMProviders {
		out = append(out, item.Provider)
	}
	return out
}

// default_model is the agent-level default: the PUT writes it, refuses a
// dangling value, refuses clearing it, and both GET branches report the
// effective value (default_model when set, otherwise the first row).
func TestProvidersDTODefaultModel(t *testing.T) {
	s, home := providersDTOServer(t)
	cfgPath := seedProvidersConfig(t, home, "",
		[2]string{"deepseek", "deepseek-v4"},
		[2]string{"openai", "gpt-test"},
	)

	require.Equal(t, "deepseek/deepseek-v4", providersGetDefaultModel(t, s), "absent default_model reports the first row")
	pagedDefault, _ := providersGetPaged(t, s)
	require.Equal(t, "deepseek/deepseek-v4", pagedDefault, "paged GET reports the same default_model")

	rr := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","models":["deepseek-v4"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-a-1111"},
		{"provider":"openai","models":["gpt-test"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-b-2222"}
	],"default_model":"openai/gpt-test"}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	cfgRaw, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	require.Contains(t, string(cfgRaw), "default_model: openai/gpt-test")
	require.Equal(t, "openai/gpt-test", providersGetDefaultModel(t, s))

	// A dangling default_model is refused, not saved.
	rr = providersPut(t, s, `{"providers":[
		{"provider":"deepseek","models":["deepseek-v4"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-a-1111"},
		{"provider":"openai","models":["gpt-test"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-b-2222"}
	],"default_model":"openai/gone"}`)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "default_model")

	// Clearing is refused: switch the default, do not empty it.
	rr = providersPut(t, s, `{"providers":[
		{"provider":"deepseek","models":["deepseek-v4"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-a-1111"},
		{"provider":"openai","models":["gpt-test"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-b-2222"}
	],"default_model":""}`)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

// The row the default resolves to may not be deleted — explicitly pointed at
// by default_model, or implicitly as the first row of an unset default.
func TestProvidersDTORefusesDeletingTheDefaultRow(t *testing.T) {
	s, home := providersDTOServer(t)
	seedProvidersConfig(t, home, "      default_model: openai/gpt-test\n",
		[2]string{"deepseek", "deepseek-v4"},
		[2]string{"openai", "gpt-test"},
	)

	// Dropping openai while default_model points at it: refused.
	rr := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","models":["deepseek-v4"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-a-1111"}
	]}`)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "默认模型")

	// Retargeting the default in the same write that drops openai: allowed.
	rr = providersPut(t, s, `{"providers":[
		{"provider":"deepseek","models":["deepseek-v4"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-a-1111"}
	],"default_model":"deepseek/deepseek-v4"}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// Implicit default: with default_model unset, the first row cannot be
	// deleted either.
	s2, home2 := providersDTOServer(t)
	seedProvidersConfig(t, home2, "",
		[2]string{"deepseek", "deepseek-v4"},
		[2]string{"openai", "gpt-test"},
	)
	rr = providersPut(t, s2, `{"providers":[
		{"provider":"openai","models":["gpt-test"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-b-2222"}
	]}`)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "默认模型")
}

// providerModelOrderInYAML is the expanded provider/model order the file was
// written in, one item per stored entry.
func providerModelOrderInYAML(t *testing.T, raw string) []string {
	t.Helper()
	cfg, err := appcfg.ParseRootYAML([]byte(raw))
	require.NoError(t, err)
	out := []string{}
	for _, item := range cfg.Agents.Definitions["main"].LLMProviders {
		out = append(out, item.Provider+"/"+item.Model)
	}
	return out
}

func providerBaseURLInYAML(t *testing.T, raw, provider string) string {
	t.Helper()
	cfg, err := appcfg.ParseRootYAML([]byte(raw))
	require.NoError(t, err)
	for _, item := range cfg.Agents.Definitions["main"].LLMProviders {
		if strings.EqualFold(strings.TrimSpace(item.Provider), provider) {
			return strings.TrimSpace(item.BaseURL)
		}
	}
	return ""
}

// An edit is an edit, not a delete plus an append: a row the page changed the
// endpoint of keeps the position its stored entries occupy.
func TestProvidersDTOPutEditsARowInPlace(t *testing.T) {
	s, home := providersDTOServer(t)
	cfgPath := seedProvidersConfig(t, home, "",
		[2]string{"deepseek", "deepseek-v4"},
		[2]string{"openai", "gpt-test"},
		[2]string{"moonshotai", "kimi"},
	)

	rr := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","models":["deepseek-v4"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-a-1111"},
		{"provider":"openai","models":["gpt-test"],"base_url":"https://mirror.example.com","api_key_plain":"sk-b-2222",
		 "replaces":{"provider":"openai","base_url":"https://api.deepseek.com","models":["gpt-test"]}},
		{"provider":"moonshotai","models":["kimi"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-c-3333"}
	]}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	cfgRaw, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	require.Equal(t, []string{"deepseek", "openai", "moonshotai"}, providerOrderInYAML(t, string(cfgRaw)),
		"the edited row must keep its stored slot instead of being appended at the end")
	require.Equal(t, "https://mirror.example.com", providerBaseURLInYAML(t, string(cfgRaw), "openai"))
}

// Adding a model to an existing service must leave its entries adjacent: the
// read path folds only adjacent same-signature entries into one row.
func TestProvidersDTOPutAddsAModelNextToItsRow(t *testing.T) {
	s, home := providersDTOServer(t)
	cfgPath := seedProvidersConfig(t, home, "",
		[2]string{"deepseek", "deepseek-v4"},
		[2]string{"openai", "gpt-test"},
	)

	rr := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","models":["deepseek-v4","deepseek-v4-flash"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-a-1111",
		 "replaces":{"provider":"deepseek","base_url":"https://api.deepseek.com","models":["deepseek-v4"]}},
		{"provider":"openai","models":["gpt-test"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-b-2222"}
	]}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	cfgRaw, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	require.Equal(t, []string{"deepseek/deepseek-v4", "deepseek/deepseek-v4-flash", "openai/gpt-test"},
		providerModelOrderInYAML(t, string(cfgRaw)),
		"the new model must sit next to the row it belongs to")

	rows := providersGet(t, s)
	require.Len(t, rows, 2, "the two deepseek models are one row, not two")
	require.Equal(t, "deepseek", rows[0]["provider"])
	require.Equal(t, []any{"deepseek-v4", "deepseek-v4-flash"}, rows[0]["models"].([]any))
}

func TestProvidersDTOPutRemovesAModelFromARow(t *testing.T) {
	s, home := providersDTOServer(t)
	cfgPath := seedProvidersConfig(t, home, "",
		[2]string{"deepseek", "deepseek-v4"},
		[2]string{"deepseek", "deepseek-v4-flash"},
		[2]string{"openai", "gpt-test"},
	)

	rr := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","models":["deepseek-v4"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-a-1111",
		 "replaces":{"provider":"deepseek","base_url":"https://api.deepseek.com","models":["deepseek-v4","deepseek-v4-flash"]}},
		{"provider":"openai","models":["gpt-test"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-b-2222"}
	]}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	cfgRaw, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	require.Equal(t, []string{"deepseek/deepseek-v4", "openai/gpt-test"}, providerModelOrderInYAML(t, string(cfgRaw)))
	require.NotContains(t, string(cfgRaw), "deepseek-v4-flash")
}

// Editing the row the default resolves to must not hand the default to another
// model: with default_model unset the first stored row is the default, so the
// edited row has to keep its slot.
func TestProvidersDTOEditingTheImplicitDefaultKeepsItDefault(t *testing.T) {
	s, home := providersDTOServer(t)
	seedProvidersConfig(t, home, "",
		[2]string{"deepseek", "deepseek-v4"},
		[2]string{"openai", "gpt-test"},
	)
	require.Equal(t, "deepseek/deepseek-v4", providersGetDefaultModel(t, s))

	rr := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","models":["deepseek-v4"],"base_url":"https://mirror.example.com","api_key_plain":"sk-a-1111",
		 "replaces":{"provider":"deepseek","base_url":"https://api.deepseek.com","models":["deepseek-v4"]}},
		{"provider":"openai","models":["gpt-test"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-b-2222"}
	]}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, "deepseek/deepseek-v4", providersGetDefaultModel(t, s),
		"the user did not touch the default, so it must not move to another model")

	rows := providersGet(t, s)
	require.Len(t, rows, 2)
	require.Equal(t, "deepseek", rows[0]["provider"])
	require.Equal(t, "https://mirror.example.com", rows[0]["base_url"])
	require.Equal(t, "openai", rows[1]["provider"])
}

// Editing the model an explicit default_model names is an edit of the default,
// not a deletion of it: the default follows the edit.
func TestProvidersDTOEditingTheExplicitDefaultFollowsTheEdit(t *testing.T) {
	s, home := providersDTOServer(t)
	cfgPath := seedProvidersConfig(t, home, "      default_model: deepseek/deepseek-v4\n",
		[2]string{"deepseek", "deepseek-v4"},
		[2]string{"openai", "gpt-test"},
	)

	rr := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","models":["deepseek-v4-pro"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-a-1111",
		 "replaces":{"provider":"deepseek","base_url":"https://api.deepseek.com","models":["deepseek-v4"]}},
		{"provider":"openai","models":["gpt-test"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-b-2222"}
	]}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	cfgRaw, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	require.Contains(t, string(cfgRaw), "default_model: deepseek/deepseek-v4-pro")
	require.Equal(t, "deepseek/deepseek-v4-pro", providersGetDefaultModel(t, s))
}

// A stale page (its row no longer on disk) claims no slot: the row falls back
// to identity matching, which makes it a new row appended at the end.
func TestProvidersDTOStaleReplacesFallsBackToIdentity(t *testing.T) {
	s, home := providersDTOServer(t)
	cfgPath := seedProvidersConfig(t, home, "",
		[2]string{"deepseek", "deepseek-v4"},
		[2]string{"openai", "gpt-test"},
	)

	rr := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","models":["deepseek-v4"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-a-1111"},
		{"provider":"openai","models":["gpt-test"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-b-2222"},
		{"provider":"zhipuai","models":["glm-5.3"],"base_url":"https://open.bigmodel.cn/api/paas/v4","api_key_plain":"sk-c-3333",
		 "replaces":{"provider":"ghost","base_url":"https://nowhere.example.com","models":["ghost-model"]}}
	]}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	cfgRaw, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	require.Equal(t, []string{"deepseek", "openai", "zhipuai"}, providerOrderInYAML(t, string(cfgRaw)))
}

// Editing a row so that the model the default points at disappears is still a
// deletion of the default model.
func TestProvidersDTOEditThatRemovesTheDefaultModelIsRefused(t *testing.T) {
	s, home := providersDTOServer(t)
	seedProvidersConfig(t, home, "      default_model: deepseek/deepseek-v4-flash\n",
		[2]string{"deepseek", "deepseek-v4"},
		[2]string{"deepseek", "deepseek-v4-flash"},
		[2]string{"openai", "gpt-test"},
	)

	rr := providersPut(t, s, `{"providers":[
		{"provider":"deepseek","models":["deepseek-v4"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-a-1111",
		 "replaces":{"provider":"deepseek","base_url":"https://api.deepseek.com","models":["deepseek-v4","deepseek-v4-flash"]}},
		{"provider":"openai","models":["gpt-test"],"base_url":"https://api.deepseek.com","api_key_plain":"sk-b-2222"}
	]}`)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), "默认模型")
}

// mergeProviderEntries is the PUT's merge on its own: the slot-claiming rules
// the HTTP tests above only reach one path at a time.
func TestMergeProviderEntries(t *testing.T) {
	entry := func(provider, baseURL, model string) appcfg.AgentLLMProviderConfig {
		return appcfg.AgentLLMProviderConfig{Provider: provider, BaseURL: baseURL, Model: model}
	}
	labels := func(entries []appcfg.AgentLLMProviderConfig) []string {
		out := []string{}
		for _, item := range entries {
			out = append(out, item.Provider+"/"+item.Model)
		}
		return out
	}
	slot := func(provider, model string) *string {
		label := provider + "/" + model
		return &label
	}
	const ds = "https://api.deepseek.com"

	cases := []struct {
		name string
		// stored is the list on disk (already expanded, one entry per model).
		// rows are the request's rows after the same expansion.
		stored []appcfg.AgentLLMProviderConfig
		rows   []providerMergeRow
		// order is the merged provider/model order.
		order []string
		// replaced maps a stored slot to what the edit put there; a nil value
		// means the edit removed that slot's model. A slot with no key was
		// never claimed by an edited row.
		replaced map[int]*string
	}{
		{
			name:   "no replaces keeps the identity merge and the stored order",
			stored: []appcfg.AgentLLMProviderConfig{entry("deepseek", ds, "deepseek-v4"), entry("openai", ds, "gpt-test")},
			rows: []providerMergeRow{
				{Entries: []appcfg.AgentLLMProviderConfig{
					entry("openai", "https://mirror.example.com", "gpt-test"),
					entry("deepseek", ds, "deepseek-v4"),
				}},
			},
			order:    []string{"deepseek/deepseek-v4", "openai/gpt-test"},
			replaced: map[int]*string{},
		},
		{
			name:   "an edited row replaces its own slot in place",
			stored: []appcfg.AgentLLMProviderConfig{entry("deepseek", ds, "deepseek-v4"), entry("openai", ds, "gpt-test"), entry("moonshotai", ds, "kimi")},
			rows: []providerMergeRow{
				{Entries: []appcfg.AgentLLMProviderConfig{entry("deepseek", ds, "deepseek-v4")}},
				{
					Entries:  []appcfg.AgentLLMProviderConfig{entry("openai", "https://mirror.example.com", "gpt-test")},
					Replaces: &providerReplaces{Provider: "openai", BaseURL: ds, Models: []string{"gpt-test"}},
				},
				{Entries: []appcfg.AgentLLMProviderConfig{entry("moonshotai", ds, "kimi")}},
			},
			order:    []string{"deepseek/deepseek-v4", "openai/gpt-test", "moonshotai/kimi"},
			replaced: map[int]*string{1: slot("openai", "gpt-test")},
		},
		{
			name:   "an added model lands right after the row's last slot",
			stored: []appcfg.AgentLLMProviderConfig{entry("deepseek", ds, "deepseek-v4"), entry("openai", ds, "gpt-test")},
			rows: []providerMergeRow{
				{
					Entries: []appcfg.AgentLLMProviderConfig{
						entry("deepseek", ds, "deepseek-v4"),
						entry("deepseek", ds, "deepseek-v4-flash"),
					},
					Replaces: &providerReplaces{Provider: "deepseek", BaseURL: ds, Models: []string{"deepseek-v4"}},
				},
				{Entries: []appcfg.AgentLLMProviderConfig{entry("openai", ds, "gpt-test")}},
			},
			order:    []string{"deepseek/deepseek-v4", "deepseek/deepseek-v4-flash", "openai/gpt-test"},
			replaced: map[int]*string{0: slot("deepseek", "deepseek-v4")},
		},
		{
			name:   "a model the edit dropped deletes its slot",
			stored: []appcfg.AgentLLMProviderConfig{entry("deepseek", ds, "deepseek-v4"), entry("deepseek", ds, "deepseek-v4-flash"), entry("openai", ds, "gpt-test")},
			rows: []providerMergeRow{
				{
					Entries:  []appcfg.AgentLLMProviderConfig{entry("deepseek", ds, "deepseek-v4")},
					Replaces: &providerReplaces{Provider: "deepseek", BaseURL: ds, Models: []string{"deepseek-v4", "deepseek-v4-flash"}},
				},
				{Entries: []appcfg.AgentLLMProviderConfig{entry("openai", ds, "gpt-test")}},
			},
			order:    []string{"deepseek/deepseek-v4", "openai/gpt-test"},
			replaced: map[int]*string{0: slot("deepseek", "deepseek-v4"), 1: nil},
		},
		{
			name:   "a second claimant of a taken slot falls back to identity matching",
			stored: []appcfg.AgentLLMProviderConfig{entry("deepseek", ds, "deepseek-v4"), entry("openai", ds, "gpt-test")},
			rows: []providerMergeRow{
				{
					Entries:  []appcfg.AgentLLMProviderConfig{entry("deepseek", ds, "deepseek-v4-pro")},
					Replaces: &providerReplaces{Provider: "deepseek", BaseURL: ds, Models: []string{"deepseek-v4"}},
				},
				{
					Entries:  []appcfg.AgentLLMProviderConfig{entry("deepseek", ds, "deepseek-v4-flash")},
					Replaces: &providerReplaces{Provider: "deepseek", BaseURL: ds, Models: []string{"deepseek-v4"}},
				},
				{Entries: []appcfg.AgentLLMProviderConfig{entry("openai", ds, "gpt-test")}},
			},
			order:    []string{"deepseek/deepseek-v4-pro", "openai/gpt-test", "deepseek/deepseek-v4-flash"},
			replaced: map[int]*string{0: slot("deepseek", "deepseek-v4-pro")},
		},
		{
			name:   "an empty Replaces.Models claims the bare entry",
			stored: []appcfg.AgentLLMProviderConfig{entry("deepseek", ds, ""), entry("openai", ds, "gpt-test")},
			rows: []providerMergeRow{
				{
					Entries:  []appcfg.AgentLLMProviderConfig{entry("deepseek", ds, "deepseek-v4")},
					Replaces: &providerReplaces{Provider: "deepseek", BaseURL: ds},
				},
				{Entries: []appcfg.AgentLLMProviderConfig{entry("openai", ds, "gpt-test")}},
			},
			order:    []string{"deepseek/deepseek-v4", "openai/gpt-test"},
			replaced: map[int]*string{0: slot("deepseek", "deepseek-v4")},
		},
		{
			name:   "a Replaces that matches nothing claims nothing",
			stored: []appcfg.AgentLLMProviderConfig{entry("deepseek", ds, "deepseek-v4")},
			rows: []providerMergeRow{
				{Entries: []appcfg.AgentLLMProviderConfig{entry("deepseek", ds, "deepseek-v4")}},
				{
					Entries:  []appcfg.AgentLLMProviderConfig{entry("zhipuai", "https://open.bigmodel.cn/api/paas/v4", "glm-5.3")},
					Replaces: &providerReplaces{Provider: "ghost", BaseURL: "https://nowhere.example.com", Models: []string{"ghost-model"}},
				},
			},
			order:    []string{"deepseek/deepseek-v4", "zhipuai/glm-5.3"},
			replaced: map[int]*string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := mergeProviderEntries(tc.stored, tc.rows)
			require.Equal(t, tc.order, labels(result.Merged))

			got := map[int]*string{}
			for position, replaced := range result.Replaced {
				if replaced == nil {
					got[position] = nil
					continue
				}
				got[position] = slot(replaced.Provider, replaced.Model)
			}
			require.Equal(t, tc.replaced, got, "Replaced must report what every claimed slot became")
		})
	}
}

// TestChatSessionsFiltersTheDrawerListByProject pins the drawer's project
// filter: project_id narrows the list — and the total the pages are counted
// from — to one project's conversations, on the server; an empty filter is
// every project, and a project nothing belongs to lists nothing.
func TestChatSessionsFiltersTheDrawerListByProject(t *testing.T) {
	s, projects, _ := newProjectsTestServer(t)
	ctx := context.Background()
	alpha, err := projects.Create(ctx, state.CreateProjectInput{Name: "alpha", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	beta, err := projects.Create(ctx, state.CreateProjectInput{Name: "beta", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for id, project := range map[string]string{"a-1": alpha.ID, "a-2": alpha.ID, "b-1": beta.ID} {
		if err := s.Sessions.Ensure(ctx, id, "conversation "+id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Sessions.Append(ctx, id, "user", "hello"); err != nil {
			t.Fatal(err)
		}
		if err := projects.BindSession(ctx, id, project); err != nil {
			t.Fatal(err)
		}
	}

	get := func(query string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		s.handleChatSessions(rec, httptest.NewRequest(http.MethodGet, "/api/chat/sessions"+query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	names := func(body map[string]any) []string {
		out := []string{}
		for _, row := range body["records"].([]any) {
			out = append(out, row.(map[string]any)["project_name"].(string))
		}
		slices.Sort(out)
		return out
	}

	all := get("?page=1&page_size=50")
	if all["total"].(float64) != 3 {
		t.Fatalf("unfiltered total = %v, want 3", all["total"])
	}
	only := get("?page=1&page_size=50&project_id=" + alpha.ID)
	if only["total"].(float64) != 2 || !slices.Equal(names(only), []string{"alpha", "alpha"}) {
		t.Fatalf("alpha list = %v (total %v), want alpha's two", names(only), only["total"])
	}
	none := get("?page=1&page_size=50&project_id=prj_nothing")
	if none["total"].(float64) != 0 || len(none["records"].([]any)) != 0 {
		t.Fatalf("unknown project list = %v, want nothing", none)
	}
}

// The one-screen project servers confirmation: the pending answer lists both
// kinds in one place, the consent records the whole answer at once, and the
// auto-allow switch is the same value everywhere it is shown.

// newServersConsentFixture is one project space over two undecided project
// files: two MCP entries and one language-server entry, so the confirmation
// has something to ask about in both groups.
func newServersConsentFixture(t *testing.T) (*Server, state.Project) {
	t.Helper()
	s, p, _ := projectPagesServer(t)
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, ".forebrain", "mcp_servers.yaml"), []byte(
		"mcp_servers:\n"+
			"  - name: caveman\n    transport: stdio\n    command: npx -y caveman-shrink\n"+
			"  - name: webtool\n    transport: stdio\n    command: /bin/webtool\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, ".forebrain", "lsp_servers.yaml"), []byte(
		"servers:\n"+
			"  gopls: {command: gopls, args: [serve], extension_to_language: {\".go\": go}}\n"), 0o644))
	return s, p
}

// pendingServerRow is one row of the pending answer: the entry as the dialog
// shows it, with the fingerprint its answer has to send back.
type pendingServerRow struct {
	Name        string `json:"name"`
	Detail      string `json:"detail"`
	Fingerprint string `json:"fingerprint"`
}

type serversPendingAnswer struct {
	MCP       []pendingServerRow `json:"mcp"`
	LSP       []pendingServerRow `json:"lsp"`
	AutoAllow bool               `json:"auto_allow"`
}

func projectServersPending(t *testing.T, s *Server, id string) serversPendingAnswer {
	t.Helper()
	rec := doJSON(t, s, http.MethodGet, "/api/v1/projects/"+id+"/servers/pending", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var answer serversPendingAnswer
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &answer))
	return answer
}

func pendingNames(rows []pendingServerRow) []string {
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Name)
	}
	return names
}

// TestProjectServersPendingListsBothKindsWithAutoAllow pins the dialog's one
// read: both groups with the names and fingerprints the answer is bound to —
// a language server named by its id — and the switch's state, which the two
// project pages repeat so the toolbar toggle never guesses.
func TestProjectServersPendingListsBothKindsWithAutoAllow(t *testing.T) {
	s, p := newServersConsentFixture(t)

	answer := projectServersPending(t, s, p.ID)
	require.Equal(t, []string{"caveman", "webtool"}, pendingNames(answer.MCP))
	for _, row := range answer.MCP {
		require.NotEmpty(t, row.Detail, "%s carries no detail", row.Name)
		require.NotEmpty(t, row.Fingerprint, "%s carries no fingerprint", row.Name)
	}
	require.Equal(t, []string{"gopls"}, pendingNames(answer.LSP))
	require.NotEmpty(t, answer.LSP[0].Detail)
	require.NotEmpty(t, answer.LSP[0].Fingerprint)
	require.False(t, answer.AutoAllow)

	// The two project pages carry the switch beside their tables, paged and
	// unpaged alike.
	for _, target := range []string{
		"/api/v1/projects/" + p.ID + "/mcp",
		"/api/v1/projects/" + p.ID + "/mcp?page=1&page_size=10",
		"/api/v1/projects/" + p.ID + "/lsp",
		"/api/v1/projects/" + p.ID + "/lsp?page=1&page_size=10",
	} {
		rec := doJSON(t, s, http.MethodGet, target, nil)
		require.Equal(t, http.StatusOK, rec.Code, target)
		body := decodeBody(t, rec.Body.Bytes())
		require.Equal(t, false, body["auto_allow"], target)
	}
}

// TestProjectServersConsentRecordsBothKindsAndTurnsSwitchOn pins the dialog's
// whole answer: every listed entry is decided — checked means allowed,
// unchecked means denied — and the switch turns on with it, so the next
// session is asked nothing.
func TestProjectServersConsentRecordsBothKindsAndTurnsSwitchOn(t *testing.T) {
	s, p := newServersConsentFixture(t)
	pending := projectServersPending(t, s, p.ID)

	rec := doJSON(t, s, http.MethodPost, "/api/v1/projects/"+p.ID+"/servers/consent", map[string]any{
		"mcp": []any{
			map[string]any{"name": "caveman", "fingerprint": pending.MCP[0].Fingerprint, "allow": true},
			map[string]any{"name": "webtool", "fingerprint": pending.MCP[1].Fingerprint, "allow": false},
		},
		"lsp": []any{
			map[string]any{"name": "gopls", "fingerprint": pending.LSP[0].Fingerprint, "allow": true},
		},
		"always_allow": true,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var answered struct {
		OK bool `json:"ok"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &answered))
	require.True(t, answered.OK)

	// Both groups are decided, so nothing is pending any more, and the
	// switch is on.
	after := projectServersPending(t, s, p.ID)
	require.Empty(t, after.MCP)
	require.Empty(t, after.LSP)
	require.True(t, after.AutoAllow)

	// The project pages show the same answer: one MCP entry allowed and one
	// denied — a denial is a row that can be taken back — and the language
	// server allowed.
	mcpBody := decodeMCPServersBody(t, doJSON(t, s, http.MethodGet,
		"/api/v1/projects/"+p.ID+"/mcp?page=1&page_size=20", nil))
	require.Equal(t, true, mcpServerRow(t, mcpBody, "caveman")["allowed"])
	require.Equal(t, true, mcpServerRow(t, mcpBody, "webtool")["declined"])
	require.Equal(t, true, mcpBody["auto_allow"])
	lspBody := decodeBody(t, doJSON(t, s, http.MethodGet,
		"/api/v1/projects/"+p.ID+"/lsp?page=1&page_size=20", nil).Body.Bytes())
	require.Equal(t, "allowed", lspBody["rows"].([]any)[0].(map[string]any)["status"])
	require.Equal(t, true, lspBody["auto_allow"])
}

// TestProjectServersConsentRefusesAStaleFingerprint pins the binding the whole
// answer carries: each choice names the fingerprint the dialog showed, and a
// project file that moved under the operator is refused whole — 409, nothing
// of either kind recorded, the switch untouched — until the surface reloads.
func TestProjectServersConsentRefusesAStaleFingerprint(t *testing.T) {
	s, p := newServersConsentFixture(t)
	pending := projectServersPending(t, s, p.ID)

	// The dialog read caveman; the file moves under it before the answer.
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, ".forebrain", "mcp_servers.yaml"), []byte(
		"mcp_servers:\n"+
			"  - name: caveman\n    transport: stdio\n    command: /bin/caveman-rewritten\n"+
			"  - name: webtool\n    transport: stdio\n    command: /bin/webtool\n"), 0o644))

	rec := doJSON(t, s, http.MethodPost, "/api/v1/projects/"+p.ID+"/servers/consent", map[string]any{
		"mcp": []any{
			map[string]any{"name": "caveman", "fingerprint": pending.MCP[0].Fingerprint, "allow": true},
			map[string]any{"name": "webtool", "fingerprint": pending.MCP[1].Fingerprint, "allow": true},
		},
		"lsp": []any{
			map[string]any{"name": "gopls", "fingerprint": pending.LSP[0].Fingerprint, "allow": true},
		},
		"always_allow": true,
	})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var refused struct {
		Error string   `json:"error"`
		Code  string   `json:"code"`
		Stale []string `json:"stale"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &refused))
	require.Equal(t, "consent_stale", refused.Code)
	require.Equal(t, []string{"caveman"}, refused.Stale)
	require.NotEmpty(t, refused.Error)

	// Nothing was recorded: both kinds still ask, and the switch is off.
	after := projectServersPending(t, s, p.ID)
	require.Equal(t, []string{"caveman", "webtool"}, pendingNames(after.MCP))
	require.Equal(t, []string{"gopls"}, pendingNames(after.LSP))
	require.False(t, after.AutoAllow)
}

// TestProjectServersAutoAllowRoundTrip pins the project page's switch: on, the
// undecided entries load without asking so both groups read empty; off, they
// ask again. An unknown project is a 404 on all three endpoints.
func TestProjectServersAutoAllowRoundTrip(t *testing.T) {
	s, p := newServersConsentFixture(t)

	rec := doJSON(t, s, http.MethodPut, "/api/v1/projects/"+p.ID+"/servers/auto-allow", map[string]any{"on": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var on struct {
		OK bool `json:"ok"`
		On bool `json:"on"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &on))
	require.True(t, on.OK)
	require.True(t, on.On)

	after := projectServersPending(t, s, p.ID)
	require.Empty(t, after.MCP)
	require.Empty(t, after.LSP)
	require.True(t, after.AutoAllow)

	rec = doJSON(t, s, http.MethodPut, "/api/v1/projects/"+p.ID+"/servers/auto-allow", map[string]any{"on": false})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var off struct {
		OK bool `json:"ok"`
		On bool `json:"on"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &off))
	require.True(t, off.OK)
	require.False(t, off.On)

	again := projectServersPending(t, s, p.ID)
	require.Equal(t, []string{"caveman", "webtool"}, pendingNames(again.MCP))
	require.Equal(t, []string{"gopls"}, pendingNames(again.LSP))
	require.False(t, again.AutoAllow)

	// An unknown project answers 404 on every one of the three endpoints.
	for method, path := range map[string]string{
		http.MethodGet:  "/api/v1/projects/nope/servers/pending",
		http.MethodPost: "/api/v1/projects/nope/servers/consent",
		http.MethodPut:  "/api/v1/projects/nope/servers/auto-allow",
	} {
		rec := doJSON(t, s, method, path, map[string]any{})
		require.Equal(t, http.StatusNotFound, rec.Code, method+" "+path)
	}
}

// TestChatToolOutputServesItsOwnSessionOnly pins the full-output read: a
// truncated shell card's spool is served whole to the conversation that ran
// it, and to nobody else — a foreign session, an unknown id and a malformed
// id read as 404/404/400, never as another conversation's output.
func TestChatToolOutputServesItsOwnSessionOnly(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	spool := filepath.Join(root, "spool-1.txt")
	full := strings.Repeat("throughput line\n", 4000)
	require.NoError(t, os.WriteFile(spool, []byte(full), 0o644))
	store, err := tool.OpenStore(filepath.Join(root, "state", "outfilter", "history.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	id, err := store.Save(tool.Entry{
		Kind: tool.KindShell, Command: "go test ./...",
		FilteredOutput: "preview", OriginalBytes: len(full), FilteredBytes: len("preview"),
		SessionID: "s1", SpoolPath: spool,
	})
	require.NoError(t, err)

	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(ctx, "s1", "s1"))
	require.NoError(t, sessions.Ensure(ctx, "s2", "s2"))

	s := &Server{Sessions: sessions, Runner: &run.Runner{Deps: &run.Deps{WorkspaceRoot: root}}}
	get := func(sid, retrieve string) *httptest.ResponseRecorder {
		return cronRequest(t, s, http.MethodGet, "/api/chat/sessions/"+sid+"/tool-output/"+retrieve, nil,
			s.handleChatToolOutput, "id", sid, "retrieve", retrieve)
	}
	rec := get("s1", strconv.FormatInt(id, 10))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Command           string `json:"command"`
		Text              string `json:"text"`
		SpoolOmittedBytes int64  `json:"spool_omitted_bytes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "go test ./...", body.Command)
	require.Equal(t, full, body.Text)
	require.Equal(t, int64(0), body.SpoolOmittedBytes)

	require.Equal(t, http.StatusNotFound, get("s2", strconv.FormatInt(id, 10)).Code, "another session's spool is not found")
	require.Equal(t, http.StatusNotFound, get("s1", "999999").Code)
	require.Equal(t, http.StatusBadRequest, get("s1", "abc").Code)
}

// TestSubagentToolOutputServesItsWorkersSessionOnly: from a subagent's own
// view the spool must belong to that subagent's worker session — the session
// its shell calls ran under — not to the conversation or any other worker.
func TestSubagentToolOutputServesItsWorkersSessionOnly(t *testing.T) {
	s, ws := newSubagentViewTestServer(t)
	seedSubagentRecord(t, ws, "sid", "agent-1")

	full := "worker session's own output\n"
	mine := filepath.Join(ws, "spool-mine.txt")
	require.NoError(t, os.WriteFile(mine, []byte(full), 0o644))
	conversation := filepath.Join(ws, "spool-conversation.txt")
	require.NoError(t, os.WriteFile(conversation, []byte("primary session output"), 0o644))
	store, err := tool.OpenStore(filepath.Join(ws, "state", "outfilter", "history.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	mineID, err := store.Save(tool.Entry{
		Kind: tool.KindShell, Command: "grep -r x .",
		FilteredOutput: "preview", SessionID: "worker-1", SpoolPath: mine,
	})
	require.NoError(t, err)
	conversationID, err := store.Save(tool.Entry{
		Kind: tool.KindShell, Command: "ls",
		FilteredOutput: "preview", SessionID: "sid", SpoolPath: conversation,
	})
	require.NoError(t, err)

	get := func(sid, agentKey, retrieve string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/"+sid+"/subagents/"+agentKey+"/tool-output/"+retrieve, nil)
		ctx := context.WithValue(req.Context(), ParamsKey, Params{
			{Key: "id", Value: sid}, {Key: "agent", Value: agentKey}, {Key: "retrieve", Value: retrieve},
		})
		w := httptest.NewRecorder()
		s.handleSubagentToolOutput(w, req.WithContext(ctx))
		return w
	}
	rec := get("sid", "agent-1", strconv.FormatInt(mineID, 10))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Command string `json:"command"`
		Text    string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "grep -r x .", body.Command)
	require.Equal(t, full, body.Text)

	require.Equal(t, http.StatusNotFound, get("sid", "agent-1", strconv.FormatInt(conversationID, 10)).Code,
		"a spool of the conversation's own session is not the worker's")
	require.Equal(t, http.StatusNotFound, get("sid", "ghost", strconv.FormatInt(mineID, 10)).Code)
	require.Equal(t, http.StatusBadRequest, get("sid", "agent-1", "abc").Code)
}
