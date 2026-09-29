package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestHandleRunSubagentsUsesAppCore(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, agent.AppendHistory(workspace, agent.HistoryEntry{
		TaskID:      "task-1",
		RunID:       "child-1",
		ParentRunID: "parent-1",
		SessionID:   "session-1",
		Task:        "first",
		Status:      agent.StatusRunning,
		StartedAt:   10,
		UpdatedAt:   20,
	}))

	core := turn.New(
		turn.WithWorkspaceRoot(workspace),
		turn.WithChildRunStore(stubGatewayChildRunStore{
			list: []state.Run{
				{ID: "child-1", ParentRunID: "parent-1", SessionID: "session-1", Status: state.RunStatusRunning, UpdatedAt: 20},
				{ID: "child-2", ParentRunID: "parent-1", SessionID: "session-1", Status: state.RunStatusDone, UpdatedAt: 15},
			},
		}),
	)
	s := &Server{Core: core}
	req := httptest.NewRequest(http.MethodGet, "/api/runs/parent-1/subagents?status=running", nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{
		{Key: "id", Value: "parent-1"},
	}))
	rr := httptest.NewRecorder()

	s.handleRunSubagents(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var body struct {
		RunID   string               `json:"run_id"`
		Runs    []state.Run          `json:"runs"`
		Records []agent.HistoryEntry `json:"records"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Equal(t, "parent-1", body.RunID)
	require.Len(t, body.Runs, 1)
	require.Equal(t, "child-1", body.Runs[0].ID)
	require.Len(t, body.Records, 1)
	require.Equal(t, "task-1", body.Records[0].TaskID)
}

type stubGatewayChildRunStore struct {
	list []state.Run
}

func (s stubGatewayChildRunStore) ListChildRuns(ctx context.Context, parentRunID string, limit int, statuses ...state.RunStatus) ([]state.Run, error) {
	allowed := map[state.RunStatus]struct{}{}
	for _, status := range statuses {
		allowed[status] = struct{}{}
	}
	out := make([]state.Run, 0, len(s.list))
	for _, run := range s.list {
		if parentRunID != "" && run.ParentRunID != parentRunID {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[run.Status]; !ok {
				continue
			}
		}
		out = append(out, run)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type stubGatewayRunEventStore struct {
	run    *state.Run
	events []state.SessionEvent
}

func (s stubGatewayRunEventStore) GetRun(ctx context.Context, id string) (*state.Run, error) {
	return s.run, nil
}

func (s stubGatewayRunEventStore) ListRunEvents(ctx context.Context, runID string, limit int) ([]state.SessionEvent, error) {
	return append([]state.SessionEvent(nil), s.events...), nil
}

func (s stubGatewayRunEventStore) ListRunsBySession(ctx context.Context, sessionID string, limit int) ([]state.Run, error) {
	if s.run == nil || s.run.SessionID != sessionID {
		return nil, nil
	}
	return []state.Run{*s.run}, nil
}

// The run events endpoint reads the durable log through the engine's core; the
// rows it returns are the stored events themselves, in the order they were
// recorded, stamped with the current wire schema version.
func TestHandleRunEventsUsesAppCore(t *testing.T) {
	core := turn.New(turn.WithRunEventStore(stubGatewayRunEventStore{
		run: &state.Run{ID: "r1", SessionID: "s1"},
		events: []state.SessionEvent{
			{ID: "evt-1", Sequence: 1, RunID: "r1", SessionID: "s1", Type: event.RunEventAssistantDelta, Payload: json.RawMessage(`{"text":"hi"}`)},
			{ID: "evt-2", Sequence: 2, RunID: "r1", SessionID: "s1", Type: event.RunEventToolCompleted, Payload: json.RawMessage(`{"tool_name":"read_file"}`)},
		},
	}))
	s := &Server{Core: core}
	req := httptest.NewRequest(http.MethodGet, "/api/runs/r1/events?limit=5", nil)
	req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{{Key: "id", Value: "r1"}}))
	rr := httptest.NewRecorder()

	s.handleRunEvents(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var body struct {
		RunID     string           `json:"run_id"`
		SessionID string           `json:"session_id"`
		Events    []event.RunEvent `json:"events"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Equal(t, "r1", body.RunID)
	require.Equal(t, "s1", body.SessionID)
	require.Len(t, body.Events, 2)
	require.Equal(t, event.RunEventAssistantDelta, body.Events[0].Type)
	require.Equal(t, event.RunEventToolCompleted, body.Events[1].Type)
	require.Equal(t, event.RunEventSchemaVersion, body.Events[0].SchemaVersion)
}

// TestHandleRunCancelLeavesARunningRunToItsDriver pins that cancelling a run
// in flight only stops it: whatever drives the run ends it, and it is that end
// which hands the queue back, so the hand-back is always reported before the
// run's end on the driver's own connection rather than racing it from here.
func TestHandleRunCancelLeavesARunningRunToItsDriver(t *testing.T) {
	s := newRunInputTestServer(t)
	rr, err := s.RunRT.CreateRun(context.Background(), "sid", "work")
	require.NoError(t, err)

	called := false
	require.True(t, s.runController().Track(rr.ID, "sid", func() { called = true }))
	q, _, ok := s.runController().Queue(rr.ID)
	require.True(t, ok)
	require.True(t, q.FollowUp(run.Input{Text: "next", MentionImages: []string{"shots/a.png"}}))

	w := cancelRunRequest(t, s, rr.ID)
	var out struct {
		OK      bool `json:"ok"`
		Preview struct {
			Queued []string `json:"queued_messages"`
		} `json:"preview"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.True(t, out.OK)
	require.True(t, called)
	require.Equal(t, []string{"next"}, out.Preview.Queued, "the queue is still the run's until it ends")
	require.Equal(t, 1, s.runController().Active())
	require.Empty(t, releasedInputSteps(t, s, rr.ID))

	// The driver ends the run.
	s.finishRun(context.Background(), "sid", rr.ID)
	require.Zero(t, s.runController().Active())
	require.Equal(t, []event.QueuedInputReleasedPayload{{Inputs: []event.ReleasedInput{{Text: "next", MentionImages: []string{"shots/a.png"}}}}}, releasedInputSteps(t, s, rr.ID))

	got, err := s.RunRT.GetRun(context.Background(), rr.ID)
	require.NoError(t, err)
	require.Equal(t, state.RunStatusCancelled, got.Status)
}

// TestHandleRunCancelFinishesARunParkedOnApproval pins the other case: a run
// waiting on an approval has nothing driving it, so the cancel ends it — its
// queue handed back first, then the run's end reported, since nothing else
// would report it.
func TestHandleRunCancelFinishesARunParkedOnApproval(t *testing.T) {
	s := newRunInputTestServer(t)
	rr, err := s.RunRT.CreateRun(context.Background(), "sid", "work")
	require.NoError(t, err)
	require.True(t, s.runController().Track(rr.ID, "sid", func() {}))
	q, _, ok := s.runController().Queue(rr.ID)
	require.True(t, ok)
	require.True(t, q.FollowUp(run.Input{Text: "next", Attachments: []string{"file-1"}}))
	require.True(t, s.runController().WaitApproval(rr.ID))

	cancelRunRequest(t, s, rr.ID)

	require.Zero(t, s.runController().Active())
	require.Equal(t, []event.QueuedInputReleasedPayload{{Inputs: []event.ReleasedInput{{Text: "next", Attachments: []string{"file-1"}}}}}, releasedInputSteps(t, s, rr.ID))
	events, err := s.RunRT.ListRunEvents(context.Background(), rr.ID, 100)
	require.NoError(t, err)
	var order []string
	for _, evt := range events {
		if evt.Type == event.RunEventQueuedInputReleased || evt.Type == event.RunEventTurnCancelled {
			order = append(order, evt.Type)
		}
	}
	require.Equal(t, []string{event.RunEventQueuedInputReleased, event.RunEventTurnCancelled}, order)
}

func cancelRunRequest(t *testing.T, s *Server, runID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/cancel", nil)
	req.SetPathValue("id", runID)
	w := httptest.NewRecorder()
	s.handleRunCancel(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	return w
}

func releasedInputSteps(t *testing.T, s *Server, runID string) []event.QueuedInputReleasedPayload {
	t.Helper()
	events, err := s.RunRT.ListRunEvents(context.Background(), runID, 100)
	require.NoError(t, err)
	var out []event.QueuedInputReleasedPayload
	for _, evt := range events {
		if evt.Type != event.RunEventQueuedInputReleased {
			continue
		}
		raw, err := json.Marshal(evt.Payload)
		require.NoError(t, err)
		var payload event.QueuedInputReleasedPayload
		require.NoError(t, json.Unmarshal(raw, &payload))
		out = append(out, payload)
	}
	return out
}

func TestRunControllerUsesProcessControl(t *testing.T) {
	ctl := run.NewController()
	s := &Server{Env: &process.Environment{Control: ctl}}
	require.Same(t, ctl, s.runController())
}

func queue(t *testing.T, s *Server, runID string) *run.InputQueue {
	t.Helper()
	ctl := s.runController()
	require.True(t, ctl.Track(runID, "sid", func() {}))
	t.Cleanup(func() { ctl.Untrack(runID) })
	q, _, ok := ctl.Queue(runID)
	require.True(t, ok)
	return q
}

func TestHandleRunInputSteerAndQueueExposePendingInputPreview(t *testing.T) {
	s := &Server{}
	runID := "run-active"
	queue(t, s, runID)

	req := httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/input", strings.NewReader(`{"message":"please adjust"}`))
	req.SetPathValue("id", runID)
	rr := httptest.NewRecorder()
	s.handleRunInput(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var steerResp struct {
		Accepted bool `json:"accepted"`
		Preview  struct {
			PendingSteers  []string `json:"pending_steers"`
			RejectedSteers []string `json:"rejected_steers"`
			QueuedMessages []string `json:"queued_messages"`
		} `json:"preview"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &steerResp))
	require.True(t, steerResp.Accepted)
	require.Equal(t, []string{"please adjust"}, steerResp.Preview.PendingSteers)

	req = httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/queued-input", strings.NewReader(`{"message":"next turn"}`))
	req.SetPathValue("id", runID)
	rr = httptest.NewRecorder()
	s.handleRunQueuedInput(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var queueResp struct {
		Preview struct {
			PendingSteers  []string `json:"pending_steers"`
			RejectedSteers []string `json:"rejected_steers"`
			QueuedMessages []string `json:"queued_messages"`
		} `json:"preview"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &queueResp))
	require.Equal(t, []string{"please adjust"}, queueResp.Preview.PendingSteers)
	require.Equal(t, []string{"next turn"}, queueResp.Preview.QueuedMessages)
}

func TestHandleRunQueuedInputEditLastQueuedMessage(t *testing.T) {
	s := &Server{}
	runID := "run-edit"
	queue(t, s, runID)

	for _, body := range []string{`{"message":"first"}`, `{"message":"second"}`} {
		req := httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/queued-input", strings.NewReader(body))
		req.SetPathValue("id", runID)
		rr := httptest.NewRecorder()
		s.handleRunQueuedInput(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/queued-input", strings.NewReader(`{"action":"edit_last"}`))
	req.SetPathValue("id", runID)
	rr := httptest.NewRecorder()
	s.handleRunQueuedInput(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp struct {
		Message string `json:"message"`
		Preview struct {
			QueuedMessages []string `json:"queued_messages"`
		} `json:"preview"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "second", resp.Message)
	require.Equal(t, []string{"first"}, resp.Preview.QueuedMessages)
}

func TestHandleRunQueuedInputEditLastQueuedMessageFallsBackToRejectedSteer(t *testing.T) {
	s := &Server{}
	runID := "run-edit-rejected"
	q := queue(t, s, runID)

	req := httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/input", strings.NewReader(`{"message":"retry at end"}`))
	req.SetPathValue("id", runID)
	rr := httptest.NewRecorder()
	s.handleRunInput(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	q.RejectSteers()

	req = httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/queued-input", strings.NewReader(`{"action":"edit_last"}`))
	req.SetPathValue("id", runID)
	rr = httptest.NewRecorder()
	s.handleRunQueuedInput(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp struct {
		Message string `json:"message"`
		Preview struct {
			RejectedSteers []string `json:"rejected_steers"`
		} `json:"preview"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, "retry at end", resp.Message)
	require.Empty(t, resp.Preview.RejectedSteers)
}

// TestHandleRunQueuedInputKeepsWhatTheMessageAttached pins that a message
// queued during a run comes back whole when the user recalls it to edit — its
// text, its uploaded files and the workspace images its @ picker attached —
// that recalling the only queued message leaves the run taking input, and
// that a steer is refused rather than sent without what it attached.
func TestHandleRunQueuedInputKeepsWhatTheMessageAttached(t *testing.T) {
	post := func(s *Server, runID, path, body string, handler func(http.ResponseWriter, *http.Request)) map[string]any {
		req := httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+path, strings.NewReader(body))
		req.SetPathValue("id", runID)
		rr := httptest.NewRecorder()
		handler(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
		return out
	}
	const attached = `{"message":"look at these","attachments":["file-1"],"mention_images":["shots/a.png","shots/b.png"]}`

	s := &Server{}
	queue(t, s, "run-steer-images")
	steer := post(s, "run-steer-images", "/input", attached, s.handleRunInput)
	require.Equal(t, false, steer["accepted"], "a steer cannot carry what the message attached")

	runID := "run-recall"
	queue(t, s, runID)
	post(s, runID, "/queued-input", attached, s.handleRunQueuedInput)
	got := post(s, runID, "/queued-input", `{"action":"edit_last"}`, s.handleRunQueuedInput)
	require.Equal(t, true, got["accepted"])
	require.Equal(t, "look at these", got["message"])
	require.Equal(t, []any{"file-1"}, got["attachments"])
	require.Equal(t, []any{"shots/a.png", "shots/b.png"}, got["mention_images"])

	// The queue emptied, but the run is still going and still takes input.
	again := post(s, runID, "/queued-input", attached, s.handleRunQueuedInput)
	require.Equal(t, true, again["accepted"], "recalling the last queued message closed the run to input")
}

func TestHandleRunQueuedInputEditLastRetractsPendingSteer(t *testing.T) {
	s := &Server{}
	runID := "run-edit-steer"
	queue(t, s, runID)

	// One accepted steer + one queued follow-up.
	req := httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/input", strings.NewReader(`{"message":"please adjust"}`))
	req.SetPathValue("id", runID)
	rr := httptest.NewRecorder()
	s.handleRunInput(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	req = httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/queued-input", strings.NewReader(`{"message":"next turn"}`))
	req.SetPathValue("id", runID)
	rr = httptest.NewRecorder()
	s.handleRunQueuedInput(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// edit_last pulls the pending steer back first (parity with the CLI).
	req = httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/queued-input", strings.NewReader(`{"action":"edit_last"}`))
	req.SetPathValue("id", runID)
	rr = httptest.NewRecorder()
	s.handleRunQueuedInput(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp struct {
		Accepted bool   `json:"accepted"`
		Message  string `json:"message"`
		Preview  struct {
			PendingSteers  []string `json:"pending_steers"`
			QueuedMessages []string `json:"queued_messages"`
		} `json:"preview"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.True(t, resp.Accepted)
	require.Equal(t, "please adjust", resp.Message)
	require.Empty(t, resp.Preview.PendingSteers)
	require.Equal(t, []string{"next turn"}, resp.Preview.QueuedMessages)
}

func TestRunInputDrainNextFollowUpStartsWithOldestQueuedMessage(t *testing.T) {
	s := &Server{}
	runID := "run-drain"
	q := queue(t, s, runID)

	require.True(t, q.FollowUp(run.Input{Text: "first"}))
	require.True(t, q.FollowUp(run.Input{Text: "second"}))

	got, ok := q.PopNext()
	require.True(t, ok)
	require.Equal(t, "first", got.Text)

	got, ok = q.PopNext()
	require.True(t, ok)
	require.Equal(t, "second", got.Text)

	_, ok = q.PopNext()
	require.False(t, ok)
}

func TestRunInputDrainNextMergesRejectedSteersBeforeQueuedMessage(t *testing.T) {
	s := &Server{}
	runID := "run-drain-rejected"
	q := queue(t, s, runID)

	for _, msg := range []string{"steer one", "steer two"} {
		req := httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/input", strings.NewReader(`{"message":"`+msg+`"}`))
		req.SetPathValue("id", runID)
		rr := httptest.NewRecorder()
		s.handleRunInput(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	}
	require.True(t, q.FollowUp(run.Input{Text: "queued turn"}))

	q.RejectSteers()

	got, ok := q.PopNext()
	require.True(t, ok)
	require.Equal(t, "steer one\n\nsteer two", got.Text)
	require.True(t, got.Rejected)

	got, ok = q.PopNext()
	require.True(t, ok)
	require.Equal(t, "queued turn", got.Text)
	require.False(t, got.Rejected)
}

// TestGatewayDoesNotEmitOrPersistIndependentRunPlans keeps the gateway from
// growing its own run-plan path again: plans belong to the shared runtime, and
// a second one here would emit plan steps the other surfaces never see.
//
// It scans every production file in the package rather than a hand-listed
// pair. The list version went stale the moment supervised_run.go was deleted
// (P8-3 moved channel inbound onto TurnService.Submit), failing on a missing
// file instead of on what it is meant to guard — and it would equally have
// missed the forbidden code appearing in any file nobody remembered to add.
func TestGatewayDoesNotEmitOrPersistIndependentRunPlans(t *testing.T) {
	forbidden := []string{
		`Op:        "plan"`,
		"SetRunPlanMeta",
		"runPlanPayload(",
		"mergePlannerSteps(",
	}
	var scanned int
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		scanned++
		src := string(b)
		for _, f := range forbidden {
			if strings.Contains(src, f) {
				t.Errorf("%s still contains an independent run-plan path %q", path, f)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned == 0 {
		t.Fatal("scanned no gateway production files; this test would pass vacuously")
	}
	if _, err := os.Stat("planner_run.go"); err == nil {
		t.Fatal("planner_run.go must be removed")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat planner_run.go: %v", err)
	}
}

// An attachment-only queued message must be recallable like any other. The
// response carries both Message and Attachments, so the client can restore the
// composer either way; skipping it made an image with no caption the one queued
// message the user could never take back.
func TestPopLatestEditableRecallsAttachmentOnlyMessage(t *testing.T) {
	q := run.NewInputQueue()
	if !q.FollowUp(run.Input{Attachments: []string{"/tmp/shot.png"}}) {
		t.Fatal("expected the attachment-only message to be queued")
	}

	item, preview, popped := q.PopLatest()

	if !popped {
		t.Fatal("expected the attachment-only message to be recallable")
	}
	if len(item.Attachments) != 1 || item.Attachments[0] != "/tmp/shot.png" {
		t.Fatalf("recall lost the attachment: %#v", item)
	}
	if len(preview.FollowUp) != 0 {
		t.Fatalf("recalled message must leave the queue: %#v", preview.FollowUp)
	}
}

// Ordinary messages still take priority over rejected steers, newest first.
func TestPopLatestEditablePrefersNewestOrdinaryMessage(t *testing.T) {
	q := run.NewInputQueue()
	q.FollowUp(run.Input{Text: "rejected", Rejected: true})
	q.FollowUp(run.Input{Text: "first"})
	q.FollowUp(run.Input{Text: "second"})

	item, _, popped := q.PopLatest()
	if !popped || item.Text != "second" {
		t.Fatalf("expected the newest ordinary message, got %#v", item)
	}
	if item, _, popped = q.PopLatest(); !popped || item.Text != "first" {
		t.Fatalf("expected the remaining ordinary message, got %#v", item)
	}
	if item, _, popped = q.PopLatest(); !popped || item.Text != "rejected" {
		t.Fatalf("expected the rejected steer last, got %#v", item)
	}
	if _, _, popped = q.PopLatest(); popped {
		t.Fatal("expected an empty queue to report nothing to recall")
	}
}

func newRunInputTestServer(t *testing.T) *Server {
	t.Helper()
	home := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(home, "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	if err := state.NewSessionStore(db, "main").Ensure(context.Background(), "sid", "sid"); err != nil {
		t.Fatal(err)
	}
	return &Server{Home: home, RunRT: &state.RunStore{DB: db}}
}

func pendingInputSteps(t *testing.T, s *Server, runID string) []event.PendingInputUpdatedPayload {
	t.Helper()
	events, err := s.RunRT.ListRunEvents(context.Background(), runID, 100)
	require.NoError(t, err)
	out := make([]event.PendingInputUpdatedPayload, 0, len(events))
	for _, evt := range events {
		if evt.Type != event.RunEventPendingInputUpdated {
			continue
		}
		var payload event.PendingInputUpdatedPayload
		require.NoError(t, json.Unmarshal(evt.Payload, &payload), "pending_input_updated payload should decode")
		out = append(out, payload)
	}
	return out
}

func trackedQueue(t *testing.T, s *Server, runID string) *run.InputQueue {
	t.Helper()
	seedRun(t, s.RunRT, "sid", runID)
	ctl := s.runController()
	require.True(t, ctl.Track(runID, "sid", func() {}))
	t.Cleanup(func() { ctl.Untrack(runID) })
	q, sid, ok := ctl.Queue(runID)
	require.True(t, ok)
	q.SetChangeHook(func() {
		s.appendPendingInputUpdated(context.Background(), runID, sid, toPendingInputPreview(q.Preview()))
	})
	return q
}

// An approval gate ends the run that hit it; the decision starts a new run over
// the same run ID. The messages the user queued before the approval belong to
// the turn, not to that first run, so the resumed run has to inherit the queue -
// otherwise they are never handed to the model and the client keeps promising a
// delivery that never happens.
func TestResumedRunAfterApprovalInheritsQueuedTurnInput(t *testing.T) {
	s := newRunInputTestServer(t)
	runID := "run-approval-resume"
	q := trackedQueue(t, s, runID)
	rt := q.Runtime()
	accepted := q.Steer(run.Input{Text: "also update the README"})
	require.True(t, accepted)

	require.True(t, s.runController().WaitApproval(runID))
	require.True(t, s.runController().Resume(runID))
	resumeCtx := run.WithTurnInputRuntime(context.Background(), rt)
	resumeRT := run.TurnInputRuntimeFromContext(resumeCtx)
	require.Same(t, rt, resumeRT, "the resumed run must drain the queue the suspended run collected")

	entries := resumeRT.DrainSteers()
	require.Len(t, entries, 1)
	require.Equal(t, "also update the README", llm.TextContent(entries[0].Parts...))
}

// Delivery is invisible to the client on its own: the agent loop takes the
// steer at a tool boundary and nothing else reports it. Without a published
// preview the message stays on screen as "submitted after next tool call" for
// the rest of the turn, even though it was already submitted.
func TestDeliveredSteerPublishesRefreshedPendingInputPreview(t *testing.T) {
	s := newRunInputTestServer(t)
	runID := "run-steer-delivery"
	q := trackedQueue(t, s, runID)
	rt := q.Runtime()
	accepted := q.Steer(run.Input{Text: "first steer"})
	require.True(t, accepted)
	accepted = q.FollowUp(run.Input{Text: "next turn"})
	require.True(t, accepted)
	require.Empty(t, pendingInputSteps(t, s, runID), "enqueue publishes over its own response, not a run step")

	require.Len(t, rt.DrainSteers(), 1)

	published := pendingInputSteps(t, s, runID)
	require.Len(t, published, 1)
	require.Empty(t, published[0].PendingSteers, "the delivered steer must leave the preview")
	require.Equal(t, []string{"next turn"}, published[0].QueuedMessages, "untouched queued input must stay")
}

// Re-queueing steers at the end of a turn is not a delivery: rejectPendingSteers
// moves them into the end-of-turn queue and returns the resulting preview, so
// the delivery hook must not publish the half-applied state in between.
func TestRejectingPendingSteersDoesNotPublishIntermediatePreview(t *testing.T) {
	s := newRunInputTestServer(t)
	runID := "run-steer-reject"
	q := trackedQueue(t, s, runID)
	accepted := q.Steer(run.Input{Text: "undelivered steer"})
	require.True(t, accepted)

	preview := q.RejectSteers()
	require.Equal(t, []string{"undelivered steer"}, preview.Rejected)
	require.Empty(t, preview.Steers)
	require.Empty(t, pendingInputSteps(t, s, runID), "the caller owns the preview for this transition")
}

// The helpers below were production functions that only the tests in this
// package ever called. They live here so the production files carry no
// unused code while the tests keep exercising the live code they wrap.

// The web conversation shows the primary agent's answer as it is written: each
// piece is published on the conversation's event log as it arrives, the same
// event a subagent's stream is, and kept so a cancelled turn persists what the
// page already showed. An answer the output guardrail judges whole is not
// streamed at all.
func TestWebAnswerStreamsAsItIsWritten(t *testing.T) {
	ctx := context.Background()
	s := cronTestServer(t)
	s.Sessions = state.NewSessionStore(s.Env.SQL, "main")
	seedRun(t, s.RunRT, "s1", "r1")
	partial := &turn.StreamPartial{}
	var streamed bool
	sink := llm.StreamSinkFrom(s.withAnswerStream(ctx, "r1", "s1", &streamed, partial))
	require.NotNil(t, sink)
	sink.OnReasoningDelta("thinking")
	sink.OnDelta("Hel")
	sink.OnDelta("lo")

	records, err := s.RunRT.ListSessionEventsOfType(ctx, "s1", event.RunEventAssistantDelta, 10)
	require.NoError(t, err)
	var pieces []string
	for _, rec := range records {
		var p event.AssistantDeltaPayload
		require.NoError(t, json.Unmarshal(rec.Payload, &p))
		require.Empty(t, p.AgentID, "the primary agent's answer is not tagged as a subagent's")
		require.Equal(t, "r1", rec.RunID)
		pieces = append(pieces, p.Text)
	}
	require.Equal(t, []string{"Hel", "lo"}, pieces)
	require.Equal(t, "Hello", partial.Content())
	require.Equal(t, "thinking", partial.Reasoning())

	s.persistCancelledGatewayTurn("s1", nil, partial)
	turns, err := s.Sessions.ListAllMessages(ctx, "s1", 0)
	require.NoError(t, err)
	var kept []string
	for _, row := range turns {
		kept = append(kept, row.Role+":"+row.Content)
	}
	require.Contains(t, strings.Join(kept, "|"), "assistant:Hello", "a cancelled turn keeps the answer the page was shown")

	enabled := true
	s.Env.Deps.AppCfg.Agents.Defaults.Guardrails.Output.Enabled = &enabled
	require.Nil(t, llm.StreamSinkFrom(s.withAnswerStream(ctx, "r2", "s1", &streamed, &turn.StreamPartial{})),
		"an answer the output guardrail must judge whole is not streamed")
}

// recordingRunExecutor answers every turn with a fixed outcome and remembers
// what it was asked to run.
type recordingRunExecutor struct {
	mu       sync.Mutex
	requests []turn.TurnRequest
	err      error
	answer   string
}

func (x *recordingRunExecutor) Run(_ context.Context, req turn.TurnRequest, started func(string)) (*agent.Result, error) {
	if started != nil {
		started(req.ExistingRunID)
	}
	x.mu.Lock()
	x.requests = append(x.requests, req)
	err, answer := x.err, x.answer
	x.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &agent.Result{Parts: []llm.ContentPart{llm.Text(answer)}}, nil
}

func (x *recordingRunExecutor) last() turn.TurnRequest {
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.requests) == 0 {
		return turn.TurnRequest{}
	}
	return x.requests[len(x.requests)-1]
}

func usageLimitResettingAt(resetAt time.Time) error {
	return &llm.APIError{
		StatusCode: http.StatusTooManyRequests,
		Type:       "usage_limit_reached",
		RateLimit:  &llm.RateLimit{Quota: true, Plan: "plus", Reason: "usage_limit_reached", ResetAt: resetAt},
	}
}

type autoContinueGateway struct {
	server   *Server
	sessions *state.SessionStore
	executor *recordingRunExecutor
	url      string
}

func newAutoContinueGateway(t *testing.T) *autoContinueGateway {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sessions := state.NewSessionStore(db, "main")
	require.NoError(t, sessions.Ensure(ctx, "session-ws", "session-ws"))
	runStore := &state.RunStore{DB: db}
	executor := &recordingRunExecutor{answer: "picked up where it left off"}
	// The web turn path resolves attachments through the session's runner.
	cfg := &appcfg.Root{}
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{"main": {
		Primary:      true,
		LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "gpt-main", APIKey: "test-key", BaseURL: "http://127.0.0.1:9/v1"}},
	}}
	runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", AppCfg: cfg, SessionStore: sessions}}
	require.NoError(t, runner.Load())
	s := &Server{
		Home: home, Sessions: sessions, RunRT: runStore, Runner: runner,
		Core: turn.New(turn.WithRunEventStore(runStore), turn.WithSessionStore(sessions), turn.WithRunExecutor(executor)),
	}
	runner.Events = s.RunEvents()
	s.Core.SetAutoContinue(s.autoContinueConfig())
	t.Cleanup(s.Core.StopAutoContinue)
	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	t.Cleanup(server.Close)
	return &autoContinueGateway{server: s, sessions: sessions, executor: executor, url: "ws" + strings.TrimPrefix(server.URL, "http")}
}

// bind opens a page on the session and returns it with its session_bound reply.
func (g *autoContinueGateway) bind(t *testing.T) (*websocket.Conn, wsServerMsg) {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(g.url, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	var connected wsServerMsg
	require.NoError(t, conn.ReadJSON(&connected))
	require.NoError(t, conn.WriteJSON(wsClientMsg{Op: "bind_session", RequestID: "bind", SessionID: "session-ws"}))
	for {
		var msg wsServerMsg
		require.NoError(t, conn.ReadJSON(&msg))
		if msg.Op == "session_bound" {
			return conn, msg
		}
	}
}

func readOp(t *testing.T, conn *websocket.Conn, op string) wsServerMsg {
	t.Helper()
	for {
		var msg wsServerMsg
		require.NoError(t, conn.ReadJSON(&msg))
		if msg.Op == op {
			return msg
		}
	}
}

func boundAutoContinue(t *testing.T, bound wsServerMsg) (event.AutoContinueScheduledPayload, bool) {
	t.Helper()
	data, _ := bound.Data.(map[string]any)
	raw, ok := data["auto_continue"]
	if !ok {
		return event.AutoContinueScheduledPayload{}, false
	}
	b, err := json.Marshal(raw)
	require.NoError(t, err)
	var payload event.AutoContinueScheduledPayload
	require.NoError(t, json.Unmarshal(b, &payload))
	return payload, true
}

// A web turn stopped by a usage limit schedules its continuation below the
// surface: every page on the session hears it, a page opened later is told on
// binding, and any page can cancel it.
func TestWebTurnStoppedByUsageLimitSchedulesAContinuationEveryPageCanCancel(t *testing.T) {
	g := newAutoContinueGateway(t)
	resetAt := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	g.executor.err = usageLimitResettingAt(resetAt)

	conn, bound := g.bind(t)
	_, pending := boundAutoContinue(t, bound)
	require.False(t, pending, "nothing is waiting before any turn failed")

	var send wsClientMsg
	send.RequestID = "req-turn"
	send.SessionID = "session-ws"
	send.Message.Content = "summarize the repo"
	require.NoError(t, conn.WriteJSON(send))

	scheduled := readRunEventOfType(t, conn, event.RunEventAutoContinueScheduled)
	var payload event.AutoContinueScheduledPayload
	require.NoError(t, json.Unmarshal(scheduled.Payload, &payload))
	require.Equal(t, resetAt.Add(5*time.Second).UTC().Format(time.RFC3339), payload.ContinueAt)
	require.Equal(t, resetAt.UTC().Format(time.RFC3339), payload.ResetAt)
	require.Equal(t, string(llm.ExplainRateLimitQuota), payload.Code)
	require.Equal(t, "plus", payload.Plan)
	require.Equal(t, 1, payload.Attempt)
	readOp(t, conn, "run_error")

	// A page opened mid-wait learns of it from the binding itself.
	other, otherBound := g.bind(t)
	snapshot, pending := boundAutoContinue(t, otherBound)
	require.True(t, pending, "a page opened during the wait must be told about it")
	require.Equal(t, payload.ContinueAt, snapshot.ContinueAt)

	require.NoError(t, other.WriteJSON(wsClientMsg{Op: wsOpCancelAutoContinue, RequestID: "cancel-1", SessionID: "session-ws"}))
	ack := readOp(t, other, wsOpAutoContinueCancelAck)
	require.Empty(t, ack.Error)
	require.Equal(t, map[string]any{"cancelled": true}, ack.Data)

	// The first page hears the cancellation it did not send.
	cancelled := readRunEventOfType(t, conn, event.RunEventAutoContinueCancelled)
	var reason event.AutoContinueCancelledPayload
	require.NoError(t, json.Unmarshal(cancelled.Payload, &reason))
	require.Equal(t, turn.AutoContinueCancelledByUser, reason.Reason)
	_, stillPending := g.server.Core.PendingAutoContinue("session-ws")
	require.False(t, stillPending)

	_, laterBound := g.bind(t)
	_, pending = boundAutoContinue(t, laterBound)
	require.False(t, pending, "a cancelled wait must not be offered to a page that binds afterwards")
}

func TestCancelAutoContinueNeedsASession(t *testing.T) {
	g := newAutoContinueGateway(t)
	conn, _ := g.bind(t)
	require.NoError(t, conn.WriteJSON(wsClientMsg{Op: wsOpCancelAutoContinue, RequestID: "cancel-1"}))
	ack := readOp(t, conn, wsOpAutoContinueCancelAck)
	require.Contains(t, ack.Error, "session_id required")
}

// The continuation itself runs as a detached web turn: pages that did not
// send the stopped turn — or were opened long after it — watch it through the
// session's event log, and the transcript records it like any other turn.
func TestAutoContinuationRunsAsADetachedWebTurn(t *testing.T) {
	g := newAutoContinueGateway(t)
	conn, _ := g.bind(t)

	plan := turn.AutoContinuePlan{SessionID: "session-ws", RunID: "stopped-run", Origin: turn.Origin{Surface: turn.SurfaceWebChat}, Attempt: 1}
	require.NoError(t, g.server.continueAfterUsageLimit(context.Background(), plan, turn.AutoContinuePrompt))

	started := readRunEventOfType(t, conn, event.RunEventTurnStarted)
	completed := readRunEventOfType(t, conn, event.RunEventTurnCompleted)
	require.Equal(t, started.RunID, completed.RunID)
	var done event.TurnCompletedPayload
	require.NoError(t, json.Unmarshal(completed.Payload, &done))
	require.Equal(t, "picked up where it left off", done.Text)

	req := g.executor.last()
	require.Equal(t, turn.AutoContinuePrompt, req.UserText)
	require.Equal(t, autoContinueTrigger, req.Trigger)
	require.Equal(t, turn.SurfaceWebChat, req.Origin.Surface)
	require.Equal(t, started.RunID, req.ExistingRunID)
	require.True(t, req.AgentContextIsRunContext)

	messages, err := g.sessions.ListRecentMessages(context.Background(), "session-ws", 10)
	require.NoError(t, err)
	var roles, contents []string
	for _, m := range messages {
		roles = append(roles, m.Role)
		contents = append(contents, m.Content)
	}
	require.Equal(t, []string{"user", "assistant"}, roles)
	require.Equal(t, turn.AutoContinuePrompt, contents[0])
	require.Eventually(t, func() bool { return g.server.runController().Active() == 0 }, 5*time.Second, 10*time.Millisecond)
}

func TestAutoContinuationStandsDownWhenTheSessionIsBusy(t *testing.T) {
	g := newAutoContinueGateway(t)
	require.True(t, g.server.runController().Track("run-in-flight", "session-ws", func() {}))
	err := g.server.continueAfterUsageLimit(context.Background(), turn.AutoContinuePlan{SessionID: "session-ws"}, turn.AutoContinuePrompt)
	require.ErrorIs(t, err, turn.ErrAutoContinueUnavailable)
	require.Empty(t, g.executor.requests, "a second run must not start beside the one in flight")
}

func TestAutoContinueRESTReportsAndCancelsTheWait(t *testing.T) {
	g := newAutoContinueGateway(t)
	g.executor.err = usageLimitResettingAt(time.Now().Add(time.Hour))
	_, _ = g.server.Core.Submit(context.Background(), turn.TurnRequest{SessionID: "session-ws", Origin: turn.Origin{Surface: turn.SurfaceWebChat}}, nil)

	call := func(method string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, "/api/auto-continue?session_id=session-ws", nil)
		rec := httptest.NewRecorder()
		g.server.handleAutoContinue(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		return body
	}
	got := call(http.MethodGet)
	require.Equal(t, true, got["pending"])
	require.NotEmpty(t, got["auto_continue"].(map[string]any)["continue_at"])

	require.Equal(t, true, call(http.MethodDelete)["cancelled"])
	require.Equal(t, false, call(http.MethodGet)["pending"])
	require.Equal(t, false, call(http.MethodDelete)["cancelled"])

	req := httptest.NewRequest(http.MethodGet, "/api/auto-continue?session_id=someone-else", nil)
	rec := httptest.NewRecorder()
	g.server.handleAutoContinue(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code, "another agent's session is not this page's to inspect")
}
