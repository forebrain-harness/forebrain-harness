package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/stretchr/testify/require"
)

func TestHandleSessionCompact(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest error: %v", err)
	}
	defer db.Close()

	sess := state.NewSessionStore(db, "main")
	_, _ = sess.Append(ctx, "s1", "user", "Please fix internal/gateway/ws_run_events.go")
	_, _ = sess.Append(ctx, "s1", "assistant", "Next step: run go test ./internal/gateway")

	s := &Server{Home: home, Sessions: sess}
	req := httptest.NewRequest(http.MethodPost, "/api/chat/sessions/s1/compact", nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{
		{Key: "id", Value: "s1"},
	}))
	rr := httptest.NewRecorder()

	s.handleSessionCompact(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "current model unavailable") {
		t.Fatalf("unexpected body: %s", rr.Body.String())
	}
}

func TestHandleSessionCompactSuccess(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest error: %v", err)
	}
	defer db.Close()

	sess := state.NewSessionStore(db, "main")
	_, _ = sess.Append(ctx, "s1", "user", "Please fix internal/gateway/ws_run_events.go")
	_, _ = sess.Append(ctx, "s1", "assistant", "Next step: run go test ./internal/gateway")
	_, _ = sess.Append(ctx, "s1", "user", "Also check /context payloads")
	_, _ = sess.Append(ctx, "s1", "assistant", "Will inspect compact timeline fields")

	fakeLLM := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/event-stream")
		_, _ = rw.Write([]byte("data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Compacted summary for API test.\"},\"finish_reason\":null}]}\n\n"))
		_, _ = rw.Write([]byte("data: {\"id\":\"2\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":512,\"completion_tokens\":32,\"total_tokens\":544}}\n\n"))
		_, _ = rw.Write([]byte("data: [DONE]\n\n"))
	}))
	defer fakeLLM.Close()
	cfg := &appcfg.Root{
		Agents: appcfg.AgentsSection{
			Definitions: map[string]appcfg.AgentDefinition{
				"main": {
					LLMProviders: []appcfg.AgentLLMProviderConfig{{
						Provider: "openai",
						Model:    "test-model",
						APIKey:   "test-key",
						BaseURL:  fakeLLM.URL,
						APIPath:  "/v1/chat/completions",
					}},
				},
			},
		},
	}

	s := &Server{
		Home:     home,
		Sessions: sess,
		Runner:   &run.Runner{Deps: &run.Deps{Home: home, AppCfg: cfg, AgentName: "main", SessionStore: sess}},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/chat/sessions/s1/compact", nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{
		{Key: "id", Value: "s1"},
	}))
	rr := httptest.NewRecorder()

	s.handleSessionCompact(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json decode: %v body=%s", err, rr.Body.String())
	}
	if got := strings.TrimSpace(stringAny(resp["strategy"])); got != "local" {
		t.Fatalf("strategy=%q body=%v", got, resp)
	}
	if strings.TrimSpace(stringAny(resp["boundary_id"])) == "" {
		t.Fatalf("missing boundary_id: %v", resp)
	}
	if got := intAny(resp["tokens_before"]); got <= 0 {
		t.Fatalf("tokens_before=%d body=%v", got, resp)
	}
	if got := intAny(resp["tokens_after"]); got <= 0 {
		t.Fatalf("tokens_after=%d body=%v", got, resp)
	}
	if got := intAny(resp["replaced_items"]); got <= 0 {
		t.Fatalf("replaced_items=%d body=%v", got, resp)
	}
}

func stringAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func intAny(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float64:
		return int(x)
	default:
		return 0
	}
}

func TestHandleChatSessionContextReadsCompactCheckpoint(t *testing.T) {
	home := t.TempDir()
	runner := &run.Runner{Deps: &run.Deps{Home: home}}
	tools := tool.NewState(home)
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	runs := &state.RunStore{DB: db}
	seedRun(t, runs, "s1", "run-1")
	_, err = runs.AppendSessionEvent(context.Background(), state.SessionEvent{
		SessionID: "s1", RunID: "run-1", Type: event.RunEventContextCompacted,
		Payload: event.EncodePayload(event.ContextCompactedPayload{
			CreatedAtUTC: "2026-09-24T10:00:00Z",
			Trigger:      "auto", Strategy: "remote_v2", Reason: "context budget",
			SummarySource: "remote_compaction", Scope: "total", ReplacedItems: 3,
			BoundaryID: "window-7", WindowNumber: 7, Summary: "compact checkpoint",
			TokensBefore: 7000, TokensAfter: 2000,
		}),
	})
	require.NoError(t, err)
	require.NoError(t, tools.SetContextSnapshot("s1", map[string]any{
		"session_id":       "s1",
		"mode":             "agent",
		"working_set":      []string{"tail summary"},
		"context_pressure": "warn",
		"budget": map[string]any{
			"used_tokens":  4096,
			"limit_tokens": 8192,
		},
		"model_context_tokens":     128000,
		"conversation_tokens":      4096,
		"projected_context_tokens": 8192,
		"remaining_context_tokens": 119808,
		"items": []map[string]any{
			{"source_id": "working_set_source", "title": "Task Working Set", "content": "internal/gateway/server.go"},
		},
	}))
	setRunnerToolsForTest(runner, tools)

	// Env is what production always has, and the handler now asks it
	// for the tool state rather than reaching through the Runner (R4).
	s := &Server{Home: home, Runner: runner, RunRT: runs, Env: &process.Environment{Root: home, Runner: runner}}
	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/s1/context?run_id=run-1", nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: "s1"}}))
	rr := httptest.NewRecorder()
	s.handleChatSessionContext(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "warn", resp["context_pressure"])
	require.NotNil(t, resp["context_timeline"])
	timeline, ok := resp["context_timeline"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, timeline)
	entry, ok := timeline[0].(map[string]any)
	require.True(t, ok)
	require.NotEmpty(t, entry["created_at_utc"])
	require.Equal(t, "window-7", entry["boundary_id"])
	require.Equal(t, "remote_v2", entry["strategy"])
	require.Equal(t, "remote_compaction", entry["summary_source"])
	_, hasActiveBoundary := resp["active_boundary_id"]
	require.False(t, hasActiveBoundary)
	attribution, ok := resp["token_attribution"].(map[string]any)
	require.True(t, ok)
	require.NotNil(t, attribution)
	audit, ok := resp["compact_audit"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "s1", audit["session_id"])
	diff, ok := resp["compact_diff"].([]any)
	require.True(t, ok)
	require.Len(t, diff, 1)
	export, ok := resp["compact_export"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "session_context", export["generated_from"])
	require.Nil(t, export["active_boundary_id"])
	require.Nil(t, export["active_window_id"])
	_, hasOldMemoryExplain := resp["memory_explain"]
	require.False(t, hasOldMemoryExplain)
}

func TestHandleChatSessionContextIncludesToolResultSpills(t *testing.T) {
	home := t.TempDir()
	runner := &run.Runner{Deps: &run.Deps{Home: home}}
	tools := tool.NewState(home)
	require.NoError(t, tools.SetContextSnapshot("s1", map[string]any{
		"session_id": "s1",
		"mode":       "agent",
	}))
	tools.RecordToolResultSpill(tool.ToolResultSpill{
		RunID:         "run-1",
		ToolName:      "shell",
		CallID:        "call-1",
		Path:          "/tmp/forebrain/state/tool-outputs/shell-call-1.txt",
		OriginalBytes: 240000,
		OmittedBytes:  210000,
		TotalLines:    8000,
		CreatedAt:     time.Date(2026, 6, 8, 1, 2, 3, 0, time.UTC),
	})
	setRunnerToolsForTest(runner, tools)

	// Env is what production always has, and the handler now asks it
	// for the tool state rather than reaching through the Runner (R4).
	s := &Server{Home: home, Runner: runner, Env: &process.Environment{Root: home, Runner: runner}}
	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/s1/context?run_id=run-1", nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: "s1"}}))
	rr := httptest.NewRecorder()
	s.handleChatSessionContext(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.EqualValues(t, 1, resp["tool_result_spill_count"])
	spills, ok := resp["tool_result_spills"].([]any)
	require.True(t, ok)
	require.Len(t, spills, 1)
	spill, ok := spills[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "shell", spill["tool_name"])
	require.Equal(t, "call-1", spill["call_id"])
	require.Equal(t, "/tmp/forebrain/state/tool-outputs/shell-call-1.txt", spill["path"])
	require.EqualValues(t, 240000, spill["original_bytes"])
	timeline, ok := resp["context_timeline"].([]any)
	require.True(t, ok)
	require.Len(t, timeline, 1)
	entry := timeline[0].(map[string]any)
	require.Equal(t, "tool_result_spill", entry["kind"])
}

func TestHandleChatSessionContextIncludesSessionToolResultSpillsWithoutRunID(t *testing.T) {
	home := t.TempDir()
	runner := &run.Runner{Deps: &run.Deps{Home: home}}
	tools := tool.NewState(home)
	require.NoError(t, tools.SetContextSnapshot("s1", map[string]any{
		"session_id": "s1",
		"mode":       "agent",
	}))
	tools.RecordToolResultSpill(tool.ToolResultSpill{
		SessionID:     "s1",
		RunID:         "run-1",
		ToolName:      "shell",
		CallID:        "call-1",
		Path:          "/tmp/forebrain/state/tool-outputs/shell-call-1.txt",
		OriginalBytes: 240000,
		CreatedAt:     time.Date(2026, 6, 8, 1, 2, 3, 0, time.UTC),
	})
	setRunnerToolsForTest(runner, tools)

	// Env is what production always has, and the handler now asks it
	// for the tool state rather than reaching through the Runner (R4).
	s := &Server{Home: home, Runner: runner, Env: &process.Environment{Root: home, Runner: runner}}
	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/s1/context", nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: "s1"}}))
	rr := httptest.NewRecorder()
	s.handleChatSessionContext(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.EqualValues(t, 1, resp["tool_result_spill_count"])
	spills, ok := resp["tool_result_spills"].([]any)
	require.True(t, ok)
	require.Len(t, spills, 1)
	spill := spills[0].(map[string]any)
	require.Equal(t, "s1", spill["session_id"])
	require.Equal(t, "run-1", spill["run_id"])
	timeline, ok := resp["context_timeline"].([]any)
	require.True(t, ok)
	require.Len(t, timeline, 1)
}

func TestHandleChatSessionContextReadsFullSnapshot(t *testing.T) {
	home := t.TempDir()
	runner := &run.Runner{Deps: &run.Deps{Home: home}}
	tools := tool.NewState(filepath.Join(home, "workspace"))
	require.NoError(t, tools.SetContextSnapshot("s1", map[string]any{
		"session_id":       "s1",
		"context_pressure": "ok",
		"budget": map[string]any{
			"used_tokens": 512,
		},
		"conversation_tokens":      512,
		"projected_context_tokens": 1024,
		"model_context_tokens":     128000,
	}))
	setRunnerToolsForTest(runner, tools)

	// Env is what production always has, and the handler now asks it
	// for the tool state rather than reaching through the Runner (R4).
	s := &Server{Home: home, Runner: runner, Env: &process.Environment{Root: home, Runner: runner}}
	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/s1/context?scope=full", nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: "s1"}}))
	rr := httptest.NewRecorder()
	s.handleChatSessionContext(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "ok", resp["context_pressure"])
	require.EqualValues(t, 128000, resp["model_context_tokens"])
}

func TestHandleChatSessionContextRejectsSlashTextResponse(t *testing.T) {
	s := &Server{Home: t.TempDir(), Runner: &run.Runner{Deps: &run.Deps{}}}
	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/s1/context", nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: "s1"}}))
	rr := httptest.NewRecorder()
	s.handleChatSessionContext(rr, req)

	require.Equal(t, http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	require.NotContains(t, rr.Body.String(), `"raw"`)
}

type stubGatewaySessionStore struct {
	summaries []state.SessionSummary
	turns     []state.Message
	created   []state.SessionSummary
}

func (s *stubGatewaySessionStore) Ensure(ctx context.Context, id string, title string) error {
	s.created = append(s.created, state.SessionSummary{ID: id, Title: title})
	return nil
}

func (s *stubGatewaySessionStore) SetTitle(ctx context.Context, id string, title string) error {
	s.created = append(s.created, state.SessionSummary{ID: id, Title: title})
	return nil
}

func (s stubGatewaySessionStore) ListSessionsRecent(ctx context.Context, limit int) ([]state.SessionSummary, error) {
	if limit <= 0 || limit >= len(s.summaries) {
		return append([]state.SessionSummary(nil), s.summaries...), nil
	}
	return append([]state.SessionSummary(nil), s.summaries[:limit]...), nil
}

func (s stubGatewaySessionStore) ListRecentMessages(ctx context.Context, sessionID string, limit int) ([]state.Message, error) {
	if limit <= 0 || limit >= len(s.turns) {
		return append([]state.Message(nil), s.turns...), nil
	}
	return append([]state.Message(nil), s.turns[:limit]...), nil
}

func TestHandleChatSessionsUsesAppCore(t *testing.T) {
	core := turn.New(turn.WithSessionStore(&stubGatewaySessionStore{
		summaries: []state.SessionSummary{
			{ID: "s2", Title: "Second", UpdatedAt: 1710000000},
			{ID: "s1", Title: "First", UpdatedAt: 1700000000},
		},
	}))
	s := &Server{Core: core}

	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions", nil)
	rr := httptest.NewRecorder()
	s.handleChatSessions(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp struct {
		Records []struct {
			ID         string `json:"id"`
			Title      string `json:"title"`
			CreateTime string `json:"create_time"`
			UpdateTime string `json:"update_time"`
		} `json:"records"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Records, 2)
	require.Equal(t, "s2", resp.Records[0].ID)
	require.Equal(t, "Second", resp.Records[0].Title)
	require.Equal(t, time.Unix(1710000000, 0).UTC().Format(time.RFC3339), resp.Records[0].UpdateTime)
}

func TestHandleChatMessagesUsesAppCore(t *testing.T) {
	core := turn.New(turn.WithSessionStore(&stubGatewaySessionStore{
		turns: []state.Message{
			{Role: "user", Content: "hello"},
			{Role: "system", Content: "hidden foreground system"},
			{RunID: "r1", Role: "assistant", Content: "world", RunStartedAtMs: 1778493600000, RunFinishedAtMs: 1778494149000, RunWorkedMs: 549000},
		},
	}))
	s := &Server{Core: core}

	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/s1/messages", nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: "s1"}}))
	rr := httptest.NewRecorder()
	s.handleChatMessages(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp []struct {
		Role             string `json:"role"`
		Content          string `json:"content"`
		RunStartedAt     string `json:"run_started_at"`
		RunFinishedAt    string `json:"run_finished_at"`
		WorkedDurationMs int64  `json:"worked_duration_ms"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp, 3)
	require.Equal(t, "assistant", resp[1].Role)
	require.Equal(t, "world", resp[1].Content)
	// The run closes with its own worked row, carrying the run's clock.
	require.Equal(t, "worked", resp[2].Role)
	require.Equal(t, "2026-05-11T10:00:00Z", resp[2].RunStartedAt)
	require.Equal(t, "2026-05-11T10:09:09Z", resp[2].RunFinishedAt)
	require.Equal(t, int64(549000), resp[2].WorkedDurationMs)
}

// The runtime writes user-role rows nobody typed - the environment context
// block, the plan-mode reminder. The terminal drops them from its replay; the
// web transcript has to drop them too, or they read as the user's own words and
// split the turn they sit inside into two.
func TestHandleChatMessagesDropsRuntimeAuthoredUserRows(t *testing.T) {
	reminder := state.MessagePartsJSON(llm.Message{
		Role: llm.RoleUser, IsMeta: true, Parts: []llm.ContentPart{llm.Text("<system-reminder>PLAN MODE ACTIVE</system-reminder>")},
	}, "")
	core := turn.New(turn.WithSessionStore(&stubGatewaySessionStore{
		turns: []state.Message{
			{Role: "user", Content: "plan the work"},
			{Role: "assistant", Content: "on it"},
			{Role: "user", Content: "<system-reminder>PLAN MODE ACTIVE</system-reminder>", PartsJSON: reminder},
			{Role: "assistant", Content: "done"},
		},
	}))
	s := &Server{Core: core}

	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/s1/messages", nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: "s1"}}))
	rr := httptest.NewRecorder()
	s.handleChatMessages(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp, 3)
	for _, row := range resp {
		require.NotContains(t, row.Content, "system-reminder")
	}
	require.Equal(t, []string{"user", "assistant", "assistant"}, []string{resp[0].Role, resp[1].Role, resp[2].Role})
}

func TestHandleChatMessagesReturnsDurableRunIdentity(t *testing.T) {
	core := turn.New(turn.WithSessionStore(&stubGatewaySessionStore{
		turns: []state.Message{{Role: "assistant", Content: "done", RunID: "run-exact"}},
	}))
	s := &Server{Core: core}
	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/s1/messages", nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: "s1"}}))
	rr := httptest.NewRecorder()
	s.handleChatMessages(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var resp []struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp, 1)
	require.Equal(t, "run-exact", resp[0].RunID)
}

func TestHandleChatMessagesHonorsLimitQuery(t *testing.T) {
	core := turn.New(turn.WithSessionStore(&stubGatewaySessionStore{
		turns: []state.Message{
			{Role: "user", Content: "one"},
			{Role: "assistant", Content: "two"},
			{Role: "user", Content: "three"},
		},
	}))
	s := &Server{Core: core}

	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/s1/messages?limit=2", nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: "s1"}}))
	rr := httptest.NewRecorder()
	s.handleChatMessages(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp, 2)
	require.Equal(t, "assistant", resp[0].Role)
	require.Equal(t, "two", resp[0].Content)
	require.Equal(t, "user", resp[1].Role)
	require.Equal(t, "three", resp[1].Content)
}

func TestHandleChatSessionCreateAndTitleUseAppCore(t *testing.T) {
	store := &stubGatewaySessionStore{}
	core := turn.New(turn.WithSessionStore(store))
	s := &Server{Core: core}

	req := httptest.NewRequest(http.MethodPost, "/api/chat/sessions", strings.NewReader(`{"title":"Draft"}`))
	rr := httptest.NewRecorder()
	s.handleChatSessionCreate(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

	var created struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	require.NotEmpty(t, created.ID)
	require.Contains(t, created.ID, "web-")
	require.Equal(t, "Draft", created.Title)

	titleReq := httptest.NewRequest(http.MethodPost, "/api/chat/sessions/"+created.ID+"/title", strings.NewReader(`{"title":"Renamed"}`))
	titleReq = titleReq.WithContext(context.WithValue(titleReq.Context(), ParamsKey, Params{{Key: "id", Value: created.ID}}))
	titleRR := httptest.NewRecorder()
	s.handleChatSessionTitle(titleRR, titleReq)
	require.Equal(t, http.StatusOK, titleRR.Code, titleRR.Body.String())
	require.Equal(t, created.ID, store.created[len(store.created)-1].ID)
	require.Equal(t, "Renamed", store.created[len(store.created)-1].Title)
}

func TestChannelSessionPrefixHelpers(t *testing.T) {
	require.Equal(t, "wx-user1", state.WrapChannelSession("weixin", "user1"))
	require.Equal(t, "user1", state.UnwrapChannelSession("weixin", "wx-user1"))
	require.Equal(t, "dd-open1", state.WrapChannelSession("dingtalk", "open1"))
	require.Equal(t, "qq-group1", state.WrapChannelSession("qq", "group1"))
	require.Equal(t, "fs-chat1", state.WrapChannelSession("feishu", "chat1"))
	require.Contains(t, state.NewID("cli"), "cli-")
	require.Contains(t, state.NewForSurface("webchat", ""), "web-")
	require.Contains(t, state.NewForSurface("cli", ""), "cli-")
}
