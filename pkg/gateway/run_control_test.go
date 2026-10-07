package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/channel"
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
	s.finishRun(context.Background(), "sid", rr.ID, state.RunStatusCancelled)
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

	// The run cannot take a steer (no turn runtime is attached), so the
	// message is kept as a refused steer — still recallable for editing.
	q.Detach()

	req := httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/input", strings.NewReader(`{"message":"retry at end"}`))
	req.SetPathValue("id", runID)
	rr := httptest.NewRecorder()
	s.handleRunInput(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

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
	// A message with parts steers whatever it attached (D7): what it attached
	// travels in the queue's record of it, so it comes back whole on recall.
	require.Equal(t, true, steer["accepted"], "a steer carries what the message attached")

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

// edit_last recalls the newest queued message by the shared enqueue clock —
// the rule that replaced steer-first precedence, which recalled an older
// steer whenever the newest message happened to be an ordinary follow-up.
func TestHandleRunQueuedInputEditLastRetractsPendingSteer(t *testing.T) {
	s := &Server{}
	runID := "run-edit-steer"
	q := queue(t, s, runID)

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

	// The follow-up was queued after the steer, so it is the one that comes
	// back; the older steer keeps waiting for the run's next tool boundary.
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
	require.Equal(t, "next turn", resp.Message)
	require.Equal(t, []string{"please adjust"}, resp.Preview.PendingSteers)
	require.Empty(t, resp.Preview.QueuedMessages)

	// With the follow-up gone, the steer is the newest message left: recall
	// hands it back too, retracting the run's copy so it cannot also be
	// delivered to the model.
	req = httptest.NewRequest(http.MethodPost, "/api/runs/"+runID+"/queued-input", strings.NewReader(`{"action":"edit_last"}`))
	req.SetPathValue("id", runID)
	rr = httptest.NewRecorder()
	s.handleRunQueuedInput(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.True(t, resp.Accepted)
	require.Equal(t, "please adjust", resp.Message)
	require.Empty(t, resp.Preview.PendingSteers)
	for _, entry := range q.Runtime().Snapshot() {
		if entry.Mode == run.TurnInputModeSteer {
			t.Fatalf("recalled steer must be retracted from the run's runtime: %#v", entry)
		}
	}
}

// Each completed run releases one batch of the conversation's queue as the
// next turn's input, oldest follow-up first; the turn after that takes the
// next one. (The engine's own boundary table is pinned in pkg/run; this is
// the gateway-side shape: release per run, in run order.)
func TestRunInputDrainNextFollowUpStartsWithOldestQueuedMessage(t *testing.T) {
	s := &Server{}
	ctl := s.runController()
	require.True(t, ctl.Track("run-drain-1", "sid", func() {}))
	q, _, ok := ctl.Queue("run-drain-1")
	require.True(t, ok)

	require.True(t, q.FollowUp(run.Input{Text: "first"}))
	require.True(t, q.FollowUp(run.Input{Text: "second"}))

	send, restore := ctl.Release("run-drain-1", run.BoundaryCompleted)
	require.Empty(t, restore)
	require.Len(t, send, 1)
	require.Equal(t, "first", send[0].Text)

	require.True(t, ctl.Track("run-drain-2", "sid", func() {}))
	send, restore = ctl.Release("run-drain-2", run.BoundaryCompleted)
	require.Empty(t, restore)
	require.Len(t, send, 1)
	require.Equal(t, "second", send[0].Text)

	require.True(t, ctl.Track("run-drain-3", "sid", func() {}))
	send, _ = ctl.Release("run-drain-3", run.BoundaryCompleted)
	require.Empty(t, send)
}

// Steers the ended turn never delivered run as the next turn's input, ahead
// of the ordinary follow-up queued behind them.
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

	send, restore := s.runController().Release(runID, run.BoundaryCompleted)
	require.Empty(t, restore)
	require.Len(t, send, 2)
	require.Equal(t, "steer one", send[0].Text)
	require.Equal(t, "steer two", send[1].Text)

	// The follow-up the first release left behind is the next turn's input.
	require.True(t, s.runController().Track("run-drain-rejected-2", "sid", func() {}))
	send, restore = s.runController().Release("run-drain-rejected-2", run.BoundaryCompleted)
	require.Empty(t, restore)
	require.Len(t, send, 1)
	require.Equal(t, "queued turn", send[0].Text)
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

	item, ok := q.Recall()

	if !ok {
		t.Fatal("expected the attachment-only message to be recallable")
	}
	if len(item.Attachments) != 1 || item.Attachments[0] != "/tmp/shot.png" {
		t.Fatalf("recall lost the attachment: %#v", item)
	}
	if preview := q.Preview(); len(preview.FollowUp) != 0 {
		t.Fatalf("recalled message must leave the queue: %#v", preview.FollowUp)
	}
}

// Recall hands back the newest message by the shared enqueue clock — a later
// ordinary message before an earlier refused steer — and reports nothing once
// every message has come back.
func TestPopLatestEditablePrefersNewestOrdinaryMessage(t *testing.T) {
	q := run.NewInputQueue()
	q.FollowUp(run.Input{Text: "rejected", Rejected: true})
	q.FollowUp(run.Input{Text: "first"})
	q.FollowUp(run.Input{Text: "second"})

	item, popped := q.Recall()
	if !popped || item.Text != "second" {
		t.Fatalf("expected the newest ordinary message, got %#v", item)
	}
	if item, popped = q.Recall(); !popped || item.Text != "first" {
		t.Fatalf("expected the remaining ordinary message, got %#v", item)
	}
	if item, popped = q.Recall(); !popped || item.Text != "rejected" {
		t.Fatalf("expected the rejected steer last, got %#v", item)
	}
	if _, popped = q.Recall(); popped {
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
	// Enqueues publish through the queue's change hook as well as over their
	// own response; each publication carries the settled preview.
	steps := pendingInputSteps(t, s, runID)
	require.Len(t, steps, 2)
	require.Equal(t, []string{"first steer"}, steps[0].PendingSteers)
	require.Equal(t, []string{"next turn"}, steps[1].QueuedMessages)

	require.Len(t, rt.DrainSteers(), 1)

	published := pendingInputSteps(t, s, runID)
	require.Len(t, published, 3)
	require.Empty(t, published[2].PendingSteers, "the delivered steer must leave the preview")
	require.Equal(t, []string{"next turn"}, published[2].QueuedMessages, "untouched queued input must stay")
}

// The queue's change hook is the one publication path for preview changes:
// it fires for enqueues and deliveries alike, each time with the queue's
// settled state — never a half-applied transition in the middle of a step.
func TestRejectingPendingSteersDoesNotPublishIntermediatePreview(t *testing.T) {
	s := newRunInputTestServer(t)
	runID := "run-steer-reject"
	q := trackedQueue(t, s, runID)
	accepted := q.Steer(run.Input{Text: "undelivered steer"})
	require.True(t, accepted)

	// Every hook publication carries a state the client can act on: after
	// the enqueue the steer is pending, and the turn's boundary consuming it
	// is one settled step.
	require.Len(t, pendingInputSteps(t, s, runID), 1)
	require.Equal(t, []string{"undelivered steer"}, pendingInputSteps(t, s, runID)[0].PendingSteers)
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

	started := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	s.persistCancelledGatewayTurn("s1", "r1", turn.RunEnd{StartedAt: started, FinishedAt: started.Add(4 * time.Second), Worked: 4 * time.Second}, nil, partial)
	turns, err := s.Sessions.ListAllMessages(ctx, "s1", 0)
	require.NoError(t, err)
	var kept []string
	for _, row := range turns {
		kept = append(kept, row.Role+":"+row.Content)
		// What the stopped run wrote is the run's, and carries its clock.
		require.Equal(t, "r1", row.RunID, row.Role)
		require.Equal(t, int64(4_000), row.RunWorkedMs, row.Role)
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
	waiting  *turn.WaitingError
	// delay holds the turn open long enough for its worked clock to reach a
	// whole millisecond, which is the precision runs keep.
	delay time.Duration
}

func (x *recordingRunExecutor) Run(_ context.Context, req turn.TurnRequest, started func(string)) (*agent.Result, error) {
	if started != nil {
		started(req.ExistingRunID)
	}
	x.mu.Lock()
	x.requests = append(x.requests, req)
	err, answer, delay := x.err, x.answer, x.delay
	if x.waiting != nil {
		err = x.waiting
	}
	x.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
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
	server    *Server
	sessions  *state.SessionStore
	executor  *recordingRunExecutor
	cronStore *state.CronStore
	url       string
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
	// The gateway's run store owns its runs: a process identity plus the
	// lease that vouches for it, the way the served gateway is composed.
	runStore := &state.RunStore{DB: db, Owner: "gateway-fixture"}
	stopLease, err := runStore.HoldOwnerLease(ctx)
	require.NoError(t, err)
	t.Cleanup(stopLease)
	executor := &recordingRunExecutor{answer: "picked up where it left off"}
	// The web turn path resolves attachments through the session's runner.
	cfg := &appcfg.Root{}
	// An Env turns the control plane's auth on; the fixture's pages are local
	// test clients, so it runs the mode a local no-auth deployment runs.
	cfg.Gateway.Auth.Mode = "none"
	cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{"main": {
		Primary:      true,
		LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "gpt-main", APIKey: "test-key", BaseURL: "http://127.0.0.1:9/v1"}},
	}}
	runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", AppCfg: cfg, SessionStore: sessions}}
	require.NoError(t, runner.Load())
	env := &process.Environment{Root: home, SQL: db, Runner: runner,
		Deps: run.Deps{Home: home, AgentName: "main", AppCfg: cfg, SessionStore: sessions, RunRT: runStore}}
	s := &Server{
		Home: home, Sessions: sessions, RunRT: runStore, Runner: runner,
		Core: turn.New(turn.WithRunEventStore(runStore), turn.WithSessionStore(sessions), turn.WithRunExecutor(executor)),
		Env:  env,
	}
	runner.Events = s.RunEvents()
	s.Core.SetAutoContinue(s.autoContinueConfig())
	t.Cleanup(s.Core.StopAutoContinue)
	env.Cron().Bind(ctx, "main", process.ScheduledTurns{StartHeartbeat: s.startHeartbeatTurn, StartFire: s.startCronFire})
	t.Cleanup(env.Cron().Stop)
	server := httptest.NewServer(http.HandlerFunc(s.HandleChatWS))
	t.Cleanup(server.Close)
	return &autoContinueGateway{server: s, sessions: sessions, executor: executor, cronStore: &state.CronStore{DB: db}, url: "ws" + strings.TrimPrefix(server.URL, "http")}
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
	require.False(t, req.Unattended, "a continued web turn must be able to continue again")

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

// recordingSubagentExecutor is the composition root's subagent executor for the
// gateway tests: it records what each execution was asked to do.
type recordingSubagentExecutor struct {
	mu    sync.Mutex
	tasks []string
}

func (e *recordingSubagentExecutor) RunSubagentExec(_ context.Context, req run.SubagentExecRequest) (string, error) {
	e.mu.Lock()
	e.tasks = append(e.tasks, req.Task)
	e.mu.Unlock()
	return "answered", nil
}

func (e *recordingSubagentExecutor) PersistSubagentTurn(context.Context, run.SubagentTurn) {}

func (e *recordingSubagentExecutor) SubagentExecutionStarting(context.Context, string) {}

func (e *recordingSubagentExecutor) SubagentExecutionEnded(context.Context, run.SubagentExecutionEnd) {
}

func (e *recordingSubagentExecutor) last() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.tasks) == 0 {
		return ""
	}
	return e.tasks[len(e.tasks)-1]
}

// A subagent stopped by a usage limit is continued by a message the engine
// sends that subagent, not by a turn in the conversation: the web turn path is
// never reached, and the subagent's own execution receives the prompt.
func TestGatewayContinuesASubagentAfterItsUsageLimit(t *testing.T) {
	g := newAutoContinueGateway(t)
	rec := &recordingSubagentExecutor{}
	g.server.Runner.SubagentExecutor = rec
	// A worker session id unique to this test: the engine's channels are
	// process-global and keyed by it, so sharing one with another test would
	// leave a stale execution in front of this continuation.
	now := time.Now().Unix()
	require.NoError(t, agent.AppendHistory(filepath.Join(g.server.Home, "workspace"), agent.HistoryEntry{
		AgentID: "task-limit", TaskID: "task-limit", SessionID: "session-ws", RunID: "run-limit",
		WorkerSessionID: "worker-limit", AgentKind: "typed", AgentType: "general-purpose",
		Title: "usage limit probe", Task: "answer briefly", Status: agent.StatusOK,
		StartedAt: now, UpdatedAt: now, FinishedAt: now,
	}))

	err := g.server.continueAfterUsageLimit(context.Background(), turn.AutoContinuePlan{
		SessionID: "session-ws", AgentKey: "task-limit", Origin: turn.Origin{Surface: turn.SurfaceWebChat}, Attempt: 1,
	}, turn.AutoContinuePrompt)
	require.NoError(t, err)

	require.Eventually(t, func() bool { return strings.Contains(rec.last(), "usage limit") }, 5*time.Second, 5*time.Millisecond)
	require.Equal(t, turn.TurnRequest{}, g.executor.last(), "a subagent's continuation must not run as a conversation turn")
}

// occupySessionWithAnotherOwner leaves, on the fixture's own database, the
// state another live forebrain process leaves behind: a run store with its
// own owner and a lease it keeps renewing, holding the session's primary run
// in the given state. The gateway's CreateRun must refuse beside it — that is
// the cross-process rule, not an in-memory precheck.
func (g *autoContinueGateway) occupySessionWithAnotherOwner(t *testing.T, status state.RunStatus) {
	t.Helper()
	other := &state.RunStore{DB: g.server.RunRT.DB, Owner: "other-process"}
	stop, err := other.HoldOwnerLease(context.Background())
	require.NoError(t, err)
	t.Cleanup(stop)
	rr, err := other.CreateRun(context.Background(), "session-ws", "in flight")
	require.NoError(t, err)
	if status != state.RunStatusRunning {
		require.NoError(t, other.SetStatus(context.Background(), rr.ID, status))
	}
}

func TestAutoContinuationStandsDownWhenTheSessionIsBusy(t *testing.T) {
	g := newAutoContinueGateway(t)
	g.occupySessionWithAnotherOwner(t, state.RunStatusRunning)
	err := g.server.continueAfterUsageLimit(context.Background(), turn.AutoContinuePlan{SessionID: "session-ws"}, turn.AutoContinuePrompt)
	require.ErrorIs(t, err, turn.ErrAutoContinueUnavailable)
	require.Empty(t, g.executor.requests, "a second run must not start beside the one in flight")
}

// An executor that fails before run.Run ever started (a required MCP server
// that never came up, say) leaves the ending to the gateway's own funnel. The
// funnel sets the run's status before it reports the end, so the moment a
// page reads the failure the database already agrees the run is over — and
// the person's very next message is not refused by the run that just told
// them it ended.
func TestADetachedTurnThatFailsBeforeTheExecutorRanIsFailedBeforeItsEndIsReported(t *testing.T) {
	g := newAutoContinueGateway(t)
	g.executor.err = errors.New("a required MCP server did not come up")
	conn, _ := g.bind(t)

	require.NoError(t, g.server.startHeartbeatTurn(context.Background(), "session-ws", "anything new?"))
	failed := readRunEventOfType(t, conn, event.RunEventTurnError)
	rr, err := g.server.RunRT.GetRun(context.Background(), failed.RunID)
	require.NoError(t, err)
	require.Equal(t, state.RunStatusFailed, rr.Status, "the run is terminal in the store before its end reaches the page")

	// The next unattended turn in the same session starts: the failed run no
	// longer holds the session.
	g.executor.err = nil
	g.executor.answer = "after the failure"
	require.NoError(t, g.server.startHeartbeatTurn(context.Background(), "session-ws", "anything new?"))
	completed := readRunEventOfType(t, conn, event.RunEventTurnCompleted)
	require.NotEqual(t, failed.RunID, completed.RunID)
	require.Eventually(t, func() bool { return g.server.runController().Active() == 0 }, 5*time.Second, 10*time.Millisecond)
}

// startGatewayReaper wires the fixture exactly the way RunServeBlocking
// does, so these tests observe the production composition.
func (g *autoContinueGateway) startGatewayReaper() func() {
	return turn.AbandonedRunReaper{
		Runs:    g.server.RunRT,
		Publish: g.server.publishGatewayRunEvent,
		Recover: func(context.Context) { g.server.recoverResolvedApprovalWaitsOnce() },
	}.Start(context.Background())
}

// ageIntoAbandonment moves a run and its owner's last heartbeat into the
// past, the state a process leaves behind when it dies while driving a run:
// it started the run, kept renewing for a while, then stopped, and its lease
// has since lapsed.
func (g *autoContinueGateway) ageIntoAbandonment(t *testing.T, runID, owner string) {
	t.Helper()
	now := time.Now().Unix()
	_, err := g.server.RunRT.DB.ExecContext(context.Background(),
		`UPDATE fb_runs SET created_at=?, updated_at=? WHERE id=?`, now-120, now-70, runID)
	require.NoError(t, err)
	_, err = g.server.RunRT.DB.ExecContext(context.Background(),
		`UPDATE fb_run_owners SET heartbeat_at_ms=? WHERE owner=?`, (now-70)*1000, owner)
	require.NoError(t, err)
}

// A run whose owner stopped renewing is ended by whichever process reaps it,
// and its ending reaches the bound page the way every ending does — through
// the session's own event funnel — with the run's clock stamped so history
// closes it with its worked line.
func TestTheGatewayReaperReportsAnAbandonedRunToItsBoundPage(t *testing.T) {
	g := newAutoContinueGateway(t)
	conn, _ := g.bind(t)
	ctx := context.Background()

	other := &state.RunStore{DB: g.server.RunRT.DB, Owner: "dead-process"}
	stop, err := other.HoldOwnerLease(ctx)
	require.NoError(t, err)
	rr, err := other.CreateRun(ctx, "session-ws", "write me a long report")
	require.NoError(t, err)
	_, err = turn.PersistUserTurn(ctx, g.sessions, turn.UserTurn{SessionID: "session-ws", RunID: rr.ID, ModelInput: "write me a long report"})
	require.NoError(t, err)
	g.ageIntoAbandonment(t, rr.ID, "dead-process")
	stop()

	stopReaper := g.startGatewayReaper()
	defer stopReaper()

	failed := readRunEventOfType(t, conn, event.RunEventTurnError)
	require.Equal(t, rr.ID, failed.RunID)
	var payload event.TurnErrorPayload
	require.NoError(t, json.Unmarshal(failed.Payload, &payload))
	require.Equal(t, turn.AbandonedRunReason, payload.Message)
	require.NotNil(t, payload.Detail)
	require.Equal(t, "run_abandoned", payload.Detail.Code)

	got, err := g.server.RunRT.GetRun(ctx, rr.ID)
	require.NoError(t, err)
	require.Equal(t, state.RunStatusFailed, got.Status)

	// The history API closes the reaped run with its worked line, the way it
	// closes every run that ended.
	rec := cronRequest(t, g.server, http.MethodGet, "/api/chat/sessions/session-ws/messages", nil, g.server.handleChatMessages, "id", "session-ws")
	var rows []struct {
		Role     string `json:"role"`
		RunID    string `json:"run_id"`
		WorkedMs int64  `json:"worked_duration_ms"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	var worked *struct {
		Role     string `json:"role"`
		RunID    string `json:"run_id"`
		WorkedMs int64  `json:"worked_duration_ms"`
	}
	for i := range rows {
		if rows[i].Role == "worked" && rows[i].RunID == rr.ID {
			worked = &rows[i]
		}
	}
	require.NotNil(t, worked, "the reaped run has no worked line in history")
	require.Greater(t, worked.WorkedMs, int64(0))
}

// A run that died in the middle of an approval continuation keeps its wait
// row, so the reaper leaves it alone: the continuation recovery owns it and
// knows whether its tool ran. Both run in the same cycle, and the ending is
// reported once, in the recovery's own words.
func TestTheReaperLeavesAnUncertainContinuationToItsRecovery(t *testing.T) {
	g := newAutoContinueGateway(t)
	actions := &state.ActionService{DB: g.server.RunRT.DB}
	g.server.Actions = actions
	ctx := context.Background()
	act, err := actions.CreatePending(ctx, "session-ws", "tool", nil)
	require.NoError(t, err)
	_, err = actions.Approve(ctx, act.ID, "test")
	require.NoError(t, err)

	other := &state.RunStore{DB: g.server.RunRT.DB, Owner: "dead-process"}
	stop, err := other.HoldOwnerLease(ctx)
	require.NoError(t, err)
	defer stop()
	rr, err := other.CreateRun(ctx, "session-ws", "resume me")
	require.NoError(t, err)
	require.NoError(t, other.SetWaitingAction(ctx, rr.ID, state.Wait{ActionID: act.ID, ToolName: "shell"}))
	require.NoError(t, other.SetStatus(ctx, rr.ID, state.RunStatusRunning))
	won, err := other.ClaimWaitResume(ctx, rr.ID, act.ID, "dead-process")
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, other.BeginWaitResumeExecution(ctx, rr.ID, act.ID, "dead-process"))
	// Both leases lapse: the continuation's and the owner's.
	_, err = g.server.RunRT.DB.ExecContext(ctx, `UPDATE fb_run_waits SET resume_claimed_at_ms=?`, (time.Now().Unix()-30)*1000)
	require.NoError(t, err)
	g.ageIntoAbandonment(t, rr.ID, "dead-process")

	stopReaper := g.startGatewayReaper()
	defer stopReaper()

	got, err := g.server.RunRT.GetRun(ctx, rr.ID)
	require.NoError(t, err)
	require.Equal(t, state.RunStatusFailed, got.Status)
	wait, err := g.server.RunRT.GetWaitForRun(ctx, rr.ID)
	require.NoError(t, err)
	require.Equal(t, state.WaitResumePhaseUncertain, wait.ResumePhase, "the recovery classified the continuation, not the reaper")

	// Exactly one ending was reported, and it is the recovery's sentence.
	events, err := g.server.RunRT.ListRunEventsOfTypes(ctx, rr.ID, event.RunEventTurnError)
	require.NoError(t, err)
	require.Len(t, events, 1)
	var payload event.TurnErrorPayload
	require.NoError(t, json.Unmarshal(events[0].Payload, &payload))
	require.Contains(t, payload.Message, "uncertain")
	require.NotEqual(t, turn.AbandonedRunReason, payload.Message)
}

// A continuation held by a fresh lease belongs to a live process: the
// periodic recovery pass must not take it over, and the run stays running.
func TestTheRecoveryDoesNotTakeOverAFreshlyLeasedContinuation(t *testing.T) {
	g := newAutoContinueGateway(t)
	actions := &state.ActionService{DB: g.server.RunRT.DB}
	g.server.Actions = actions
	ctx := context.Background()
	act, err := actions.CreatePending(ctx, "session-ws", "tool", nil)
	require.NoError(t, err)

	other := &state.RunStore{DB: g.server.RunRT.DB, Owner: "live-process"}
	stop, err := other.HoldOwnerLease(ctx)
	require.NoError(t, err)
	defer stop()
	rr, err := other.CreateRun(ctx, "session-ws", "resume me")
	require.NoError(t, err)
	require.NoError(t, other.SetWaitingAction(ctx, rr.ID, state.Wait{ActionID: act.ID, ToolName: "shell"}))
	require.NoError(t, other.SetStatus(ctx, rr.ID, state.RunStatusRunning))
	won, err := other.ClaimWaitResume(ctx, rr.ID, act.ID, "live-process")
	require.NoError(t, err)
	require.True(t, won)
	// The fence is crossed and the lease is fresh: the process is executing
	// the tool right now.
	require.NoError(t, other.BeginWaitResumeExecution(ctx, rr.ID, act.ID, "live-process"))

	stopReaper := g.startGatewayReaper()
	defer stopReaper()

	got, err := g.server.RunRT.GetRun(ctx, rr.ID)
	require.NoError(t, err)
	require.Equal(t, state.RunStatusRunning, got.Status, "a live process's continuation was taken over")
	wait, err := g.server.RunRT.GetWaitForRun(ctx, rr.ID)
	require.NoError(t, err)
	require.Equal(t, state.WaitResumePhaseExecutionStarted, wait.ResumePhase)
	events, err := g.server.RunRT.ListRunEventsOfTypes(ctx, rr.ID, event.RunEventTurnError)
	require.NoError(t, err)
	require.Empty(t, events, "a live continuation was reported as ended")
}

// A heartbeat is a real turn of its conversation: every page watching the
// session sees what began it and the answer it got, the transcript keeps both
// rows under the run that ran them, and the run closes with its clock.
func TestHeartbeatRunsAsATurnOfItsConversation(t *testing.T) {
	g := newAutoContinueGateway(t)
	g.executor.delay = 5 * time.Millisecond
	conn, _ := g.bind(t)

	require.NoError(t, g.server.startHeartbeatTurn(context.Background(), "session-ws", "anything new?"))

	fired := readRunEventOfType(t, conn, event.RunEventHeartbeatFired)
	var hb event.HeartbeatFiredPayload
	require.NoError(t, json.Unmarshal(fired.Payload, &hb))
	require.Equal(t, "anything new?", hb.Prompt)
	started := readRunEventOfType(t, conn, event.RunEventTurnStarted)
	completed := readRunEventOfType(t, conn, event.RunEventTurnCompleted)
	require.Equal(t, fired.RunID, started.RunID)
	require.Equal(t, started.RunID, completed.RunID)

	req := g.executor.last()
	require.Equal(t, heartbeatTrigger, req.Trigger)
	require.Equal(t, "heartbeat", req.Origin.ChannelID)
	require.Equal(t, turn.SurfaceWebChat, req.Origin.Surface)
	require.Equal(t, "anything new?", req.UserText)

	rows, err := g.sessions.ListRecentMessages(context.Background(), "session-ws", 10)
	require.NoError(t, err)
	var roles []string
	for _, m := range rows {
		roles = append(roles, m.Role)
	}
	require.Equal(t, []string{"user", "assistant"}, roles)
	require.Equal(t, "heartbeat", state.MessageOrigin(rows[0].PartsJSON))
	require.Equal(t, "anything new?", rows[0].Content)
	require.Equal(t, started.RunID, rows[0].RunID)
	require.Equal(t, started.RunID, rows[1].RunID)
	require.Greater(t, rows[1].RunWorkedMs, int64(0), "the run closes with its worked clock")

	require.Eventually(t, func() bool { return g.server.runController().Active() == 0 }, 5*time.Second, 10*time.Millisecond)
}

func TestHeartbeatStandsDownWhenTheSessionIsBusy(t *testing.T) {
	g := newAutoContinueGateway(t)
	g.occupySessionWithAnotherOwner(t, state.RunStatusRunning)
	err := g.server.startHeartbeatTurn(context.Background(), "session-ws", "anything new?")
	require.ErrorIs(t, err, state.ErrSessionBusy)
	require.Empty(t, g.executor.requests, "a heartbeat must not interrupt the run in flight")
	rows, rerr := g.sessions.ListRecentMessages(context.Background(), "session-ws", 10)
	require.NoError(t, rerr)
	require.Empty(t, rows, "a beat that did not fire must leave no row behind")
}

func TestHeartbeatStandsDownWhenTheSessionIsParked(t *testing.T) {
	g := newAutoContinueGateway(t)
	g.occupySessionWithAnotherOwner(t, state.RunStatusWaitingAction)
	err := g.server.startHeartbeatTurn(context.Background(), "session-ws", "anything new?")
	require.ErrorIs(t, err, state.ErrSessionBusy)
	require.ErrorIs(t, err, state.ErrSessionAwaitingApproval)
	require.Empty(t, g.executor.requests)
	rows, rerr := g.sessions.ListRecentMessages(context.Background(), "session-ws", 10)
	require.NoError(t, rerr)
	require.Empty(t, rows, "a beat beside a parked approval must leave no row behind")
}

// A heartbeat's turn is unattended: nobody is watching the conversation
// when it fires, so a usage limit it hits ends it for good rather than
// being continued by a timer nobody can see.
func TestHeartbeatTurnsAreUnattended(t *testing.T) {
	g := newAutoContinueGateway(t)
	require.NoError(t, g.server.startHeartbeatTurn(context.Background(), "session-ws", "anything new?"))
	require.Eventually(t, func() bool {
		req := g.executor.last()
		return req.Trigger == heartbeatTrigger && req.Unattended
	}, 5*time.Second, 10*time.Millisecond, "the heartbeat's turn never carried the unattended flag")
	require.Eventually(t, func() bool { return g.server.runController().Active() == 0 }, 5*time.Second, 10*time.Millisecond)
}

// A detached run parked on an approval writes its pre-gate snapshot under its
// own run id, so a replay attributes those rows to the run that wrote them.
func TestDetachedRunParkedOnApprovalBindsItsSnapshotRows(t *testing.T) {
	g := newAutoContinueGateway(t)
	conn, _ := g.bind(t)
	g.executor.waiting = &turn.WaitingError{
		Request: &turn.ToolApprovalRequest{ActionID: "a1", ToolName: "shell"},
		Resume:  &turn.ApprovalResumeState{ActionID: "a1", ToolName: "shell", SessionSnapshot: []llm.Message{llm.AssistantMessage([]llm.ContentPart{llm.Text("running it")})}},
	}

	require.NoError(t, g.server.startHeartbeatTurn(context.Background(), "session-ws", "anything new?"))
	readRunEventOfType(t, conn, event.RunEventApprovalReq)

	rows, err := g.sessions.ListAllMessages(context.Background(), "session-ws", 0)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(rows), 2, "the prompt row and the snapshot row are both there")
	runID := rows[0].RunID
	require.NotEmpty(t, runID)
	for _, row := range rows {
		require.Equal(t, runID, row.RunID, row.Role)
	}
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

// failingChannelExecutor creates a real run, takes a moment, and fails — a
// channel turn the provider refused.
type failingChannelExecutor struct {
	runs  *state.RunStore
	runID string
	err   error
}

func (x *failingChannelExecutor) Run(ctx context.Context, req turn.TurnRequest, started func(string)) (*agent.Result, error) {
	run, err := x.runs.CreateRun(ctx, req.SessionID, req.UserText)
	if err != nil {
		return nil, err
	}
	x.runID = run.ID
	if started != nil {
		started(run.ID)
	}
	time.Sleep(5 * time.Millisecond)
	return nil, x.err
}

// A channel turn that fails keeps what every surface keeps: the user's message,
// bound to the run it started, and the run's clock — so the conversation can
// be read back with its failed turn in it rather than without the message.
func TestFailedChannelTurnKeepsTheMessageBoundToItsTimedRun(t *testing.T) {
	ctx := context.Background()
	s := cronTestServer(t)
	s.Sessions = state.NewSessionStore(s.Env.SQL, "main")
	executor := &failingChannelExecutor{runs: s.RunRT, err: errors.New("connection reset by peer")}
	s.Core = turn.New(turn.WithSessionStore(s.Sessions), turn.WithRunExecutor(executor))

	_, ok := s.submitChannelTurn(ctx, "wecom", "wecom:u1", "the expanded prompt", "/plan ship it", "", "")
	require.False(t, ok)

	rows, err := s.Sessions.ListAllMessages(ctx, "wecom:u1", 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "user", rows[0].Role)
	require.Equal(t, "/plan ship it", rows[0].Content, "the row shows what the user sent")
	require.Equal(t, executor.runID, rows[0].RunID)
	require.Greater(t, rows[0].RunWorkedMs, int64(0))
	require.Contains(t, turn.RunWorkedLines(rows), 0)
}

// outboundRecorderChannel is a channel handler that records what the gateway
// delivered, so a test can read what a channel user was told.
type outboundRecorderChannel struct {
	mu    sync.Mutex
	texts []string
}

func (c *outboundRecorderChannel) ID() string { return "wecom" }

func (c *outboundRecorderChannel) Start(context.Context, channel.RouteAdder, channel.Bus) error {
	return nil
}

func (c *outboundRecorderChannel) Stop(context.Context) error { return nil }

func (c *outboundRecorderChannel) DeliverOutbound(_ context.Context, out channel.Outbound) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.texts = append(c.texts, out.Text)
	return nil
}

func (c *outboundRecorderChannel) delivered() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.texts...)
}

// A channel user whose session another live process holds a turn in is told
// the refusal's own sentence: ExplainError has no code for it and passes the
// store's wording through, and no run is created beside the live one.
func TestChannelTurnRefusedByABusySessionTellsTheUserTheSentence(t *testing.T) {
	ctx := context.Background()
	s := cronTestServer(t)
	s.Sessions = state.NewSessionStore(s.Env.SQL, "main")
	require.NoError(t, s.Sessions.Ensure(ctx, "wecom:u1", "wecom:u1"))
	rec := &outboundRecorderChannel{}
	reg := channel.NewRegistry()
	require.NoError(t, reg.Bind(ctx, "main", []channel.Handler{rec}, channelBus{s: s}))
	t.Cleanup(func() { _ = reg.Stop(ctx) })
	s.Channels = reg
	// Another live process holds the session's turn.
	other := &state.RunStore{DB: s.Env.SQL, Owner: "other-process"}
	stop, err := other.HoldOwnerLease(ctx)
	require.NoError(t, err)
	defer stop()
	_, err = other.CreateRun(ctx, "wecom:u1", "in flight")
	require.NoError(t, err)

	blocked := &failingChannelExecutor{runs: s.RunRT}
	s.Core = turn.New(turn.WithSessionStore(s.Sessions), turn.WithRunExecutor(blocked))

	_, ok := s.submitChannelTurn(ctx, "wecom", "wecom:u1", "hello", "hello", "", "")
	require.False(t, ok)
	require.Empty(t, blocked.runID, "no run was created beside the live one")
	require.Equal(t, []string{"This conversation is already running a turn; send again when it finishes."}, rec.delivered())
}

// fireGatewayChannel records what a fire delivers, the way a real channel
// handler would.
type fireGatewayChannel struct {
	mu   sync.Mutex
	sent []string
}

func (f *fireGatewayChannel) ID() string { return "firetest" }

func (f *fireGatewayChannel) Start(context.Context, channel.RouteAdder, channel.Bus) error {
	return nil
}

func (f *fireGatewayChannel) Stop(context.Context) error { return nil }

func (f *fireGatewayChannel) DeliverOutbound(_ context.Context, o channel.Outbound) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, o.Text)
	return nil
}

func (f *fireGatewayChannel) delivered() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

// fireAJobNow creates a job through the service and fires it now, then waits
// for its record: the session a fire runs in is named by the fire itself.
func fireAJobNow(t *testing.T, g *autoContinueGateway, in process.CronJobInput) (state.CronJob, state.CronRun) {
	t.Helper()
	ctx := context.Background()
	job, err := g.server.Env.Cron().CreateJob(ctx, "main", in)
	require.NoError(t, err)
	require.NoError(t, g.server.Env.Cron().RunJobNow(ctx, "main", job.ID))
	var fire state.CronRun
	require.Eventually(t, func() bool {
		runs, err := g.cronStore.ListRuns(ctx, job.ID, 5)
		if err != nil || len(runs) != 1 {
			return false
		}
		fire = runs[0]
		return true
	}, 5*time.Second, 10*time.Millisecond)
	return job, fire
}

// fireRunRecord reads the job's single fire record.
func fireRunRecord(t *testing.T, g *autoContinueGateway, jobID string) state.CronRun {
	t.Helper()
	runs, err := g.cronStore.ListRuns(context.Background(), jobID, 5)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	return runs[0]
}

func fireSessionEventOfType(t *testing.T, g *autoContinueGateway, sessionID, eventType string) state.SessionEvent {
	t.Helper()
	evts, err := g.server.RunRT.ListSessionEventsOfType(context.Background(), sessionID, eventType, 5)
	require.NoError(t, err)
	require.NotEmpty(t, evts, "no %s event on %s", eventType, sessionID)
	return evts[0]
}

// fireWaitsForStatus waits until the job's record reaches the status and
// returns it.
func fireWaitsForStatus(t *testing.T, g *autoContinueGateway, jobID, status string) state.CronRun {
	t.Helper()
	var rec state.CronRun
	require.Eventually(t, func() bool {
		runs, err := g.cronStore.ListRuns(context.Background(), jobID, 5)
		if err != nil || len(runs) != 1 {
			return false
		}
		rec = runs[0]
		return rec.Status == status
	}, 5*time.Second, 10*time.Millisecond)
	return rec
}

// TestCronFireRunsAsItsOwnConversation pins the shape the whole change is
// for: a fire is a full conversation — a session born a cron session, named
// for the job and the moment, whose prompt row is the task's, whose turn
// runs exactly as a web turn does, and whose record closes with the answer
// once the run really ended.
func TestCronFireRunsAsItsOwnConversation(t *testing.T) {
	g := newAutoContinueGateway(t)
	ctx := context.Background()
	enabled := true
	job, fire := fireAJobNow(t, g, process.CronJobInput{
		Name: "nightly brief", Schedule: "every 1h", Prompt: "summarise the inbox", Enabled: &enabled,
	})
	require.True(t, strings.HasPrefix(fire.SessionID, "cron-"+job.ID+"-"), "fire session %q", fire.SessionID)

	rec := fireWaitsForStatus(t, g, job.ID, state.CronStatusOK)
	require.Equal(t, "picked up where it left off", rec.Output)
	require.Equal(t, state.CronStatusOK, rec.Status)
	saved, err := g.server.Env.Cron().Job(ctx, "main", job.ID)
	require.NoError(t, err)
	require.Equal(t, state.CronStatusOK, saved.LastStatus)

	// The session is born a cron session, named for the job.
	var source, title string
	require.NoError(t, g.server.Env.SQL.QueryRowContext(ctx,
		`SELECT source, title FROM fb_sessions WHERE id=?`, fire.SessionID).Scan(&source, &title))
	require.Equal(t, state.SessionSourceCron, source)
	require.True(t, strings.HasPrefix(title, "nightly brief"), "fire session title %q", title)

	// The turn runs exactly as a web turn does, from a channel-like origin.
	req := g.executor.last()
	require.Equal(t, "summarise the inbox", req.UserText)
	require.Equal(t, cronTrigger, req.Trigger)
	require.Equal(t, turn.SurfaceChannel, req.Origin.Surface)
	require.Equal(t, cronTrigger, req.Origin.ChannelID)

	// The conversation holds the task's prompt and the answer.
	rows, err := g.sessions.ListRecentMessages(ctx, fire.SessionID, 10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(rows), 2, "the fire's conversation holds both rows")
	require.Equal(t, "user", rows[0].Role)
	require.Equal(t, "summarise the inbox", rows[0].Content)
	require.Equal(t, state.MessageOriginCron, state.MessageOrigin(rows[0].PartsJSON))
	var roles []string
	for _, r := range rows {
		roles = append(roles, r.Role)
	}
	require.Contains(t, roles, "assistant")

	// The turn began and ended as events, in that order.
	started := fireSessionEventOfType(t, g, fire.SessionID, event.RunEventTurnStarted)
	completed := fireSessionEventOfType(t, g, fire.SessionID, event.RunEventTurnCompleted)
	require.Less(t, started.Sequence, completed.Sequence)
}

// TestProjectCronFireRunsInItsProject pins that a job bound to a project
// fires a conversation of that project: born in the project's root and bound
// to it, so the pool serves the fire's turns with the project's runner.
func TestProjectCronFireRunsInItsProject(t *testing.T) {
	g := newAutoContinueGateway(t)
	ctx := context.Background()
	root := t.TempDir()
	p, err := state.NewProjectStore(g.server.Env.SQL, "main").Create(ctx, state.CreateProjectInput{Name: "api", Root: root})
	require.NoError(t, err)
	enabled := true
	job, fire := fireAJobNow(t, g, process.CronJobInput{
		Name: "project brief", Schedule: "every 1h", Prompt: "summarise the project", ProjectID: p.ID, Enabled: &enabled,
	})
	fireWaitsForStatus(t, g, job.ID, state.CronStatusOK)

	var projectID, cwd string
	require.NoError(t, g.server.Env.SQL.QueryRowContext(ctx,
		`SELECT project_id, cwd FROM fb_sessions WHERE id=?`, fire.SessionID).Scan(&projectID, &cwd))
	require.Equal(t, p.ID, projectID)
	require.Equal(t, root, cwd)
}

// TestCronFireParkedOnApprovalSettlesWhenItsRunEnds pins the D4 rule: a fire
// parked on an approval keeps its record open, refuses a second "run now",
// and warns the job's delivery target; when the approval is answered and the
// run ends, the fire settles and the answer is delivered — once.
func TestCronFireParkedOnApprovalSettlesWhenItsRunEnds(t *testing.T) {
	g := newAutoContinueGateway(t)
	ctx := context.Background()
	out := &fireGatewayChannel{}
	g.server.Env.Channels = channel.NewRegistry()
	require.NoError(t, g.server.Env.Channels.Bind(ctx, "main", []channel.Handler{out}, nil))
	g.executor.waiting = &turn.WaitingError{
		Request: &turn.ToolApprovalRequest{ActionID: "a-fire", ToolName: "write_file"},
		Resume: &turn.ApprovalResumeState{ActionID: "a-fire", ToolName: "write_file",
			SessionSnapshot: []llm.Message{llm.AssistantMessage([]llm.ContentPart{llm.Text("about to write")})}},
	}
	enabled := true
	job, fire := fireAJobNow(t, g, process.CronJobInput{
		Name: "writes a report", Schedule: "every 1h", Prompt: "write the report", Deliver: "firetest", Enabled: &enabled,
	})

	// The fire parks: its record stays open and "run it now" is refused.
	require.Eventually(t, func() bool { return len(g.executor.requests) == 1 }, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, state.CronStatusRunning, fireRunRecord(t, g, job.ID).Status)
	err := g.server.Env.Cron().RunJobNow(ctx, "main", job.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already running")

	// The job's delivery target was told the fire is waiting on a person.
	require.Eventually(t, func() bool { return len(out.delivered()) == 1 }, 5*time.Second, 10*time.Millisecond)
	require.Contains(t, out.delivered()[0], "Waiting for approval to run write_file")

	// The approval is answered; the run finishes, and the fire settles with
	// the answer and delivers it.
	runID, err := g.server.RunRT.FirstPrimaryRunID(ctx, fire.SessionID)
	require.NoError(t, err)
	require.NotEmpty(t, runID)
	require.NoError(t, g.server.RunRT.SetStatus(ctx, runID, state.RunStatusDone))
	_ = g.server.publishGatewayRunEvent(ctx, fire.SessionID, runID, event.RunEventTurnCompleted, event.TurnCompletedPayload{Text: "done"})

	rec := fireWaitsForStatus(t, g, job.ID, state.CronStatusOK)
	require.Equal(t, "done", rec.Output)
	require.Equal(t, "firetest", rec.DeliveredTo)
	require.Equal(t, []string{out.delivered()[0], "done"}, out.delivered())
}

// TestCronFireFailureIsRecordedExplained pins the unattended-run contract at
// the fire's own level: a run that stops before the model — here, a required
// MCP server that never came up — fails the fire, and the record carries the
// failure where the person will read it. This is the invariant the deleted
// one-shot-entry tests held before fires became conversations.
func TestCronFireFailureIsRecordedExplained(t *testing.T) {
	g := newAutoContinueGateway(t)
	g.executor.err = errors.New(`required MCP server "required-docs" did not come up`)
	enabled := true
	job, _ := fireAJobNow(t, g, process.CronJobInput{
		Name: "needs its tools", Schedule: "every 1h", Prompt: "read the docs", Enabled: &enabled,
	})

	rec := fireWaitsForStatus(t, g, job.ID, state.CronStatusFailed)
	require.Contains(t, rec.Error, "required-docs")
	require.Equal(t, "run_failed", rec.ErrorCode)
	saved, err := g.server.Env.Cron().Job(context.Background(), "main", job.ID)
	require.NoError(t, err)
	require.Equal(t, state.CronStatusFailed, saved.LastStatus)
}

// A crash mid-fire leaves an open record and a run nobody vouches for. The
// two tests below cover both ways it is settled: the gateway's own reaper
// reporting through the bus, and another process's reap that the bus never
// saw, settled from the persisted ending alone.
func TestCronFireAbandonedByACrashIsSettled(t *testing.T) {
	g := newAutoContinueGateway(t)
	ctx := context.Background()
	enabled := true
	job, err := g.server.Env.Cron().CreateJob(ctx, "main", process.CronJobInput{
		Name: "long report", Schedule: "every 1h", Prompt: "write a long report", Enabled: &enabled,
	})
	require.NoError(t, err)

	// A fire of this job started under a process that then died mid-run.
	sid := "cron-" + job.ID + "-1790000000"
	require.NoError(t, g.sessions.Ensure(ctx, sid, sid))
	dead := &state.RunStore{DB: g.server.Env.SQL, Owner: "dead-fire-process"}
	stop, err := dead.HoldOwnerLease(ctx)
	require.NoError(t, err)
	rr, err := dead.CreateRun(ctx, sid, "write a long report")
	require.NoError(t, err)
	_, err = g.cronStore.StartRun(ctx, state.CronRun{JobID: job.ID, AgentID: "main", SessionID: sid, Trigger: "schedule"})
	require.NoError(t, err)
	g.ageIntoAbandonment(t, rr.ID, "dead-fire-process")
	stop()

	stopReaper := g.startGatewayReaper()
	defer stopReaper()

	rec := fireWaitsForStatus(t, g, job.ID, state.CronStatusFailed)
	require.Equal(t, turn.AbandonedRunReason, rec.Error)
	require.Equal(t, "run_abandoned", rec.ErrorCode)
}

func TestCronFireReapedElsewhereIsSettledByTheTick(t *testing.T) {
	g := newAutoContinueGateway(t)
	ctx := context.Background()
	enabled := true
	job, err := g.server.Env.Cron().CreateJob(ctx, "main", process.CronJobInput{
		Name: "long report elsewhere", Schedule: "every 1h", Prompt: "write a long report", Enabled: &enabled,
	})
	require.NoError(t, err)

	sid := "cron-" + job.ID + "-1790000001"
	require.NoError(t, g.sessions.Ensure(ctx, sid, sid))
	dead := &state.RunStore{DB: g.server.Env.SQL, Owner: "dead-fire-process"}
	stop, err := dead.HoldOwnerLease(ctx)
	require.NoError(t, err)
	rr, err := dead.CreateRun(ctx, sid, "write a long report")
	require.NoError(t, err)
	_, err = g.cronStore.StartRun(ctx, state.CronRun{JobID: job.ID, AgentID: "main", SessionID: sid, Trigger: "schedule"})
	require.NoError(t, err)
	g.ageIntoAbandonment(t, rr.ID, "dead-fire-process")
	stop()

	// Another process — the TUI, say — reaps the run and records the ending
	// itself. The gateway's bus never sees it; the ending is on the store.
	tui := &state.RunStore{DB: g.server.Env.SQL, Owner: "tui-process"}
	require.NoError(t, tui.SetStatus(ctx, rr.ID, state.RunStatusFailed))
	_, err = tui.AppendSessionEvent(ctx, state.SessionEvent{
		ID: "evt-tui-reap", RunID: rr.ID, SessionID: sid,
		Type: event.RunEventTurnError,
		Payload: event.EncodePayload(event.TurnErrorPayload{
			Error: turn.AbandonedRunReason, Message: turn.AbandonedRunReason,
			Detail: &event.TurnErrorDetail{Code: "run_abandoned"},
		}),
	})
	require.NoError(t, err)

	// The next settle pass reads the persisted ending and closes the fire —
	// the same pass the scheduler's tick runs.
	g.server.Env.Cron().SettleFire(ctx, sid)
	rec := fireWaitsForStatus(t, g, job.ID, state.CronStatusFailed)
	require.Equal(t, turn.AbandonedRunReason, rec.Error)
	require.Equal(t, "run_abandoned", rec.ErrorCode)
}

// TestRunEndingOfAnOrdinarySessionTouchesNoFire pins the boundary: a run's
// end settles only the fire that ran in that session, and an ordinary
// conversation's turn ending leaves every fire alone.
func TestRunEndingOfAnOrdinarySessionTouchesNoFire(t *testing.T) {
	g := newAutoContinueGateway(t)
	ctx := context.Background()
	enabled := true
	job, err := g.server.Env.Cron().CreateJob(ctx, "main", process.CronJobInput{
		Name: "quiet job", Schedule: "every 1h", Prompt: "x", Enabled: &enabled,
	})
	require.NoError(t, err)
	sid := "cron-" + job.ID + "-1790000002"
	require.NoError(t, g.sessions.Ensure(ctx, sid, sid))
	_, err = g.cronStore.StartRun(ctx, state.CronRun{JobID: job.ID, AgentID: "main", SessionID: sid, Trigger: "schedule"})
	require.NoError(t, err)

	seedRun(t, g.server.RunRT, "session-ws", "r-ordinary")
	require.NoError(t, g.server.publishGatewayRunEvent(ctx, "session-ws", "r-ordinary",
		event.RunEventTurnCompleted, event.TurnCompletedPayload{Text: "an ordinary answer"}))
	fireSessionEventOfType(t, g, "session-ws", event.RunEventTurnCompleted)

	require.Never(t, func() bool {
		return fireRunRecord(t, g, job.ID).Status != state.CronStatusRunning
	}, 300*time.Millisecond, 20*time.Millisecond, "an ordinary session's ending must not settle a fire")
}

// TestSubagentEndingDoesNotSettleAFire pins that only a primary run's end
// settles anything: a subagent's run ending in the fire's session is part of
// the conversation's turn, not the turn's end.
func TestSubagentEndingDoesNotSettleAFire(t *testing.T) {
	g := newAutoContinueGateway(t)
	ctx := context.Background()
	enabled := true
	job, err := g.server.Env.Cron().CreateJob(ctx, "main", process.CronJobInput{
		Name: "dispatches work", Schedule: "every 1h", Prompt: "x", Enabled: &enabled,
	})
	require.NoError(t, err)
	sid := "cron-" + job.ID + "-1790000003"
	require.NoError(t, g.sessions.Ensure(ctx, sid, sid))
	_, err = g.cronStore.StartRun(ctx, state.CronRun{JobID: job.ID, AgentID: "main", SessionID: sid, Trigger: "schedule"})
	require.NoError(t, err)

	seedRun(t, g.server.RunRT, sid, "r-fire-parent")
	child, err := g.server.RunRT.CreateSubagentRun(ctx, "r-fire-parent", sid, "a child's task")
	require.NoError(t, err)
	require.NoError(t, g.server.publishGatewayRunEvent(ctx, sid, child.ID,
		event.RunEventTurnError, event.TurnErrorPayload{Error: "child failed", Message: "child failed"}))
	fireSessionEventOfType(t, g, sid, event.RunEventTurnError)

	require.Never(t, func() bool {
		return fireRunRecord(t, g, job.ID).Status != state.CronStatusRunning
	}, 300*time.Millisecond, 20*time.Millisecond, "a subagent's end must not settle a fire")
}
