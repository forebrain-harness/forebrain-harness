// Run input, cancellation, and post-turn persistence.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

func toPendingInputPreview(in run.QueuePreview) turn.PendingInputPreview {
	return turn.PendingInputPreview{
		PendingSteers:  peerQueuedLines(in.Steers),
		RejectedSteers: peerQueuedLines(in.Rejected),
		QueuedMessages: peerQueuedLines(in.FollowUp),
	}
}

// peerQueuedLines is queued input as a preview row shows it: a peer message
// waiting its turn is the sender's words, never the envelope the model will
// read (plan 246 §3.5). Only the display copy is unwrapped — the queue still
// hands the model the text it received.
func peerQueuedLines(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		if _, body, ok := process.PeerEnvelopeDisplay(line); ok {
			out[i] = body
			continue
		}
		out[i] = line
	}
	return out
}

type runInputRequest struct {
	Message     string   `json:"message"`
	Action      string   `json:"action"`
	Attachments []string `json:"attachments"`
	// MentionImages are workspace-relative image paths, as start_run takes
	// them; they are re-resolved inside the workspace when the message is sent.
	MentionImages []string `json:"mention_images"`
}

func (req runInputRequest) input() run.Input {
	return run.Input{Text: req.Message, Attachments: req.Attachments, MentionImages: req.MentionImages}
}

// queuedInputReply is a queued message handed back to its client whole —
// its text and everything it attached — to edit.
func queuedInputReply(item run.Input, accepted bool, preview turn.PendingInputPreview) map[string]any {
	return map[string]any{
		"accepted":       accepted,
		"message":        item.Text,
		"attachments":    item.Attachments,
		"mention_images": item.MentionImages,
		"preview":        preview,
	}
}

// batchReadOnly refuses a request that would change a batch conversation
// (plans/216 §3.4).
//
// A batch conversation is a task execution: it replayed, inspected and
// downloaded, and the only things a person may do to it are the task controls
// — stop, continue, run again — which go through the run and task routes, never
// through these. Renaming it, switching its model or its mode, compacting it,
// rewinding it, reverting its files, or typing into it would all change what
// the execution is, and the execution is the business system's record.
//
// The refusal is here, in the gateway, rather than only in the page: the web is
// one client of this API, and a read-only conversation that is read-only
// because the page hid a button is not read-only at all.
func (s *Server) batchReadOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sid := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
		if store := s.projectStore(); store != nil && sid != "" {
			if ptype, err := store.SessionProjectType(r.Context(), sid); err == nil && ptype == state.ProjectTypeBatch {
				writeJSONStatus(w, http.StatusConflict, map[string]any{"error": "batch_session_read_only"})
				return
			}
		}
		h(w, r)
	}
}

func (s *Server) handleRunInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	runID := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if runID == "" {
		runID = strings.TrimSpace(r.PathValue("id"))
	}
	q, sessionID, ok := s.runController().Queue(runID)
	if !ok {
		http.Error(w, "active run input not available", http.StatusConflict)
		return
	}
	owned, ownershipErr := s.sessionOwned(r.Context(), sessionID)
	if ownershipErr != nil {
		http.Error(w, ownershipErr.Error(), http.StatusInternalServerError)
		return
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	// A batch task takes no input: nobody joins an unattended execution, not
	// one step into its turn and not behind it. The queue is closed too, so
	// this is the sentence a caller gets instead of a silent drop.
	if ptype, err := s.projectStore().SessionProjectType(r.Context(), sessionID); err == nil && ptype == state.ProjectTypeBatch {
		writeJSONStatus(w, http.StatusConflict, map[string]any{"error": "batch_takes_no_input"})
		return
	}
	var req runInputRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	in, err := s.withWebInputParts(r.Context(), s.runnerFor(r.Context(), sessionID), sessionID, req.input())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	accepted := q.Steer(in)
	preview := toPendingInputPreview(q.Preview())
	if accepted {
		s.appendPendingInputUpdated(r.Context(), runID, sessionID, preview)
	}
	writeRunInputJSON(w, map[string]any{"accepted": accepted, "preview": preview})
}

func (s *Server) handleRunQueuedInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	runID := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if runID == "" {
		runID = strings.TrimSpace(r.PathValue("id"))
	}
	q, sessionID, ok := s.runController().Queue(runID)
	if !ok {
		http.Error(w, "active run input not available", http.StatusConflict)
		return
	}
	owned, ownershipErr := s.sessionOwned(r.Context(), sessionID)
	if ownershipErr != nil {
		http.Error(w, ownershipErr.Error(), http.StatusInternalServerError)
		return
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	// A batch task takes no input: nobody joins an unattended execution, not
	// one step into its turn and not behind it. The queue is closed too, so
	// this is the sentence a caller gets instead of a silent drop.
	if ptype, err := s.projectStore().SessionProjectType(r.Context(), sessionID); err == nil && ptype == state.ProjectTypeBatch {
		writeJSONStatus(w, http.StatusConflict, map[string]any{"error": "batch_takes_no_input"})
		return
	}
	var req runInputRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if strings.EqualFold(strings.TrimSpace(req.Action), "edit_last") {
		item, accepted := q.Recall()
		out := toPendingInputPreview(q.Preview())
		if accepted {
			s.appendPendingInputUpdated(r.Context(), runID, sessionID, out)
		}
		writeRunInputJSON(w, queuedInputReply(item, accepted, out))
		return
	}
	accepted := q.FollowUp(req.input())
	preview := toPendingInputPreview(q.Preview())
	if accepted {
		s.appendPendingInputUpdated(r.Context(), runID, sessionID, preview)
	}
	writeRunInputJSON(w, map[string]any{"accepted": accepted, "preview": preview})
}

// finishRun ends a run, and every way a gateway run ends goes through it.
// The run's status is terminal before anything reports its end, so the next
// turn the person sends is never refused by the run that just told them it
// ended. What the user steered or queued that the run never took is handed
// back as a durable event on the conversation, published before whatever the
// caller then reports about the run's end: the queue's boundary decision —
// what runs next versus what returns to the composer — travels in the event,
// so the client sends or takes back accordingly and nothing is dropped with
// the run.
func (s *Server) finishRun(ctx context.Context, sessionID, runID string, status state.RunStatus) {
	if s.RunRT != nil {
		// A run the executor already settled is refused by the status rule
		// (a run ends once); that refusal is the mechanism working, not an
		// error to report.
		_ = s.RunRT.SetStatus(ctx, runID, status)
	}
	send, restore := s.runController().Release(runID, runBoundaryForStatus(status))
	if len(send) > 0 || len(restore) > 0 {
		payload := event.QueuedInputReleasedPayload{}
		for _, in := range restore {
			payload.Inputs = append(payload.Inputs, releasedInput(in))
		}
		for _, in := range send {
			payload.Next = append(payload.Next, releasedInput(in))
		}
		if err := s.publishGatewayRunEvent(ctx, sessionID, runID, event.RunEventQueuedInputReleased, payload); err != nil {
			slog.Error("release queued input", "run_id", runID, "session_id", sessionID, "err", err)
		}
	}
	s.stampRunEnd(ctx, runID)
	s.runController().Finish(runID)
	s.runStartedAt.Delete(runID)
}

// runBoundaryForStatus maps how a gateway run ended onto the queue boundary
// the engine decides with. A run that ran to its end completed; anything else
// — a cancellation, a failure — is an interruption, so what the run never took
// goes back to the composer. The gateway has no path that interrupts a run in
// order to send its steers; that boundary belongs to surfaces that know why
// the interrupt was issued.
func runBoundaryForStatus(status state.RunStatus) run.Boundary {
	if status == state.RunStatusDone {
		return run.BoundaryCompleted
	}
	return run.BoundaryInterrupted
}

func releasedInput(in run.Input) event.ReleasedInput {
	return event.ReleasedInput{Text: in.Text, Attachments: in.Attachments, MentionImages: in.MentionImages}
}

// stampRunEnd gives a run that ends here its clock, so replay closes it with
// the "Worked for" line the page closed it with. An ending that persisted its
// own output already stamped the window it measured, and that stamp stands (a
// run ends once); this covers the endings that write nothing — an approval
// that expired or was cancelled, a continuation that could not proceed.
func (s *Server) stampRunEnd(ctx context.Context, runID string) {
	if s == nil || s.Sessions == nil || strings.TrimSpace(runID) == "" {
		return
	}
	var startedAt time.Time
	if stored, ok := s.runStartedAt.Load(runID); ok {
		startedAt, _ = stored.(time.Time)
	}
	if startedAt.IsZero() && s.RunRT != nil {
		if rn, err := s.RunRT.GetRun(ctx, runID); err == nil && rn != nil {
			startedAt = time.Unix(rn.CreatedAt, 0)
		}
	}
	if startedAt.IsZero() {
		return
	}
	finishedAt := time.Now()
	worked := finishedAt.Sub(startedAt)
	if worked < 0 {
		worked = 0
	}
	if err := s.Sessions.StampRunTiming(ctx, runID, state.RunTiming{StartedAt: startedAt, FinishedAt: finishedAt, Worked: worked}); err != nil {
		slog.Error("stamp run end", "run_id", runID, "err", err)
	}
}

// cancelRun stops a run. One in flight is ended by whatever drives it, which
// finishes it and reports the end. One parked on an approval has nothing
// driving it, so it is finished here and reported parked: the caller then
// reports its end, since nothing else will.
func (s *Server) cancelRun(ctx context.Context, sessionID, runID string) (invoked, parked bool) {
	ctl := s.runController()
	if active, ok := ctl.Get(runID); ok {
		parked = active.Phase() == run.Waiting
		if sessionID == "" {
			sessionID = active.SessionID
		}
	}
	invoked = ctl.Cancel(runID, context.Canceled)
	if !invoked {
		return false, false
	}
	if s.RunRT != nil {
		turn.FinalizeCancel(ctx, s.RunRT, runID)
	}
	if parked {
		s.finishRun(ctx, sessionID, runID, state.RunStatusCancelled)
	}
	return invoked, parked
}

func (s *Server) appendPendingInputUpdated(ctx context.Context, runID, sessionID string, preview turn.PendingInputPreview) {
	if s == nil || strings.TrimSpace(runID) == "" {
		return
	}
	_ = s.publishGatewayRunEvent(ctx, sessionID, strings.TrimSpace(runID), event.RunEventPendingInputUpdated, event.PendingInputUpdatedPayload{
		PendingSteers:  preview.PendingSteers,
		RejectedSteers: preview.RejectedSteers,
		QueuedMessages: preview.QueuedMessages,
	})
}

// watchRunQueue republishes a run's queue after every change to it, and draws
// each steer the run hands its model into the conversation as the user's own
// message, ahead of the preview that no longer lists it. The delivered list is
// drained here — the way the terminal takes it to render — so each steer is
// published exactly once and the conversation's queue does not keep growing.
func (s *Server) watchRunQueue(q *run.InputQueue, runID, sessionID string) {
	if s == nil || q == nil {
		return
	}
	// The hook runs on whichever goroutine changed the queue — the run's
	// stream when a steer is committed, a request handler when the user queues
	// or recalls — so changes are published one at a time. A delivered steer
	// is then always published by the invocation that moved it, before the
	// commit returns to the stream whose next event is the answer, and the
	// last preview stored is one read after the last change.
	var publishing sync.Mutex
	q.SetChangeHook(func() {
		publishing.Lock()
		defer publishing.Unlock()
		ctx := context.Background()
		for _, in := range q.TakeDelivered() {
			payload := event.InputDeliveredPayload{
				Text:          in.Text,
				Attachments:   in.Attachments,
				MentionImages: in.MentionImages,
			}
			// A peer message delivered into a running turn is that session's
			// letter, and the page draws it as one — which needs the sender,
			// the identity block and the words inside the envelope, not just
			// the envelope itself (plan 200 §3, plan 246 §3.4).
			if state.IsPeerOrigin(in.Origin) {
				payload.Origin = in.Origin
				if fromName, body, ok := process.PeerEnvelopeDisplay(in.Text); ok {
					payload.FromName = fromName
					payload.PeerMark, payload.PeerTone = process.PeerMark(fromName, "")
					payload.PeerBody = body
				}
			}
			_ = s.publishGatewayRunEvent(ctx, sessionID, runID, event.RunEventInputDelivered, payload)
		}
		s.appendPendingInputUpdated(ctx, runID, sessionID, toPendingInputPreview(q.Preview()))
	})
}

// handleSessionInterruptSend is Esc's "interrupt and send immediately" for the
// conversation itself: the run's model call in flight is abandoned and the turn
// goes on carrying the steers queued behind it — no run end, no "Worked for",
// one clock. The answer is the engine's: false means nothing was waiting, which
// leaves the surface its other meaning for Esc, stopping the run.
func (s *Server) handleSessionInterruptSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sid := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if sid == "" {
		http.NotFound(w, r)
		return
	}
	owned, err := s.sessionOwned(r.Context(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	runID, _, ok := s.runController().Find(sid)
	interrupted := ok && s.runController().InterruptToSend(runID)
	writeJSON(w, map[string]any{"interrupted": interrupted})
}

func writeRunInputJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// --- Replica-to-replica forwarding of run-bound operations (plan 213 §3.2) ---
//
// The operations below act on live state that exists in exactly one place: the
// run.Controller, the input queue and the approval wait of the process actually
// running the conversation. A request that arrives at any other replica is
// forwarded, by name, to the one that owns the run, and its answer — status and
// body — is returned unchanged.

// internalRunOpRequest is how a replica asks the replica that owns a
// conversation to perform one operation on it. Op names the operation, and the
// fields carry exactly what that operation's public route and body carry.
type internalRunOpRequest struct {
	Op         string          `json:"op"`
	RunID      string          `json:"run_id,omitempty"`
	ActionID   string          `json:"action_id,omitempty"`
	AgentID    string          `json:"agent_id,omitempty"`
	SubagentID string          `json:"subagent_id,omitempty"`
	Body       json.RawMessage `json:"body,omitempty"`
}

// internalRunOps is the table of operations a replica will perform for a peer.
// Every endpoint that reads or writes this process's run.Controller, its
// InputQueue or an approval wait belongs here (plan 213 §7): a new one that does
// must be added to this table and wrapped on its route, or the browser calling
// it would reach a replica that answers "nothing is running".
var internalRunOps = map[string]struct {
	// param is the path parameter the public route takes; field names the
	// request field that supplies its value.
	param   string
	field   string
	handler func(*Server) http.HandlerFunc
}{
	"run_cancel":         {param: "id", field: "run", handler: func(s *Server) http.HandlerFunc { return s.handleRunCancel }},
	"run_input":          {param: "id", field: "run", handler: func(s *Server) http.HandlerFunc { return s.handleRunInput }},
	"run_queued_input":   {param: "id", field: "run", handler: func(s *Server) http.HandlerFunc { return s.handleRunQueuedInput }},
	"action_approve":     {param: "id", field: "action", handler: func(s *Server) http.HandlerFunc { return s.handleActionsApprove }},
	"action_deny":        {param: "id", field: "action", handler: func(s *Server) http.HandlerFunc { return s.handleActionsDeny }},
	"action_answer":      {param: "id", field: "action", handler: func(s *Server) http.HandlerFunc { return s.handleActionsAnswer }},
	"action_plan_review": {param: "id", field: "action", handler: func(s *Server) http.HandlerFunc { return s.handleActionPlanReview }},
	"agent_cancel":       {param: "agentId", field: "agent", handler: func(s *Server) http.HandlerFunc { return s.handlePrimaryAgentCancel }},
	"agent_cancel_all":   {handler: func(s *Server) http.HandlerFunc { return s.handleAgentCancelAll }},
	"subagent_cancel":    {param: "id", field: "subagent", handler: func(s *Server) http.HandlerFunc { return s.handleSubagentCancel }},
}

// paramValue is the route parameter this operation is performed with.
func (req internalRunOpRequest) paramValue(field string) string {
	switch field {
	case "run":
		return strings.TrimSpace(req.RunID)
	case "action":
		return strings.TrimSpace(req.ActionID)
	case "agent":
		return strings.TrimSpace(req.AgentID)
	case "subagent":
		return strings.TrimSpace(req.SubagentID)
	}
	return ""
}

// handleInternalRunOp performs one forwarded operation as if the caller had
// reached the public route itself: the same handler runs, with the same
// parameters and the same request body, and its answer is what the caller gets.
func (s *Server) handleInternalRunOp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var req internalRunOpRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "internal run-op body: "+err.Error(), http.StatusBadRequest)
		return
	}
	op, ok := internalRunOps[strings.TrimSpace(req.Op)]
	if !ok {
		http.Error(w, "unsupported internal operation: "+strings.TrimSpace(req.Op), http.StatusBadRequest)
		return
	}
	inner := r.Clone(r.Context())
	inner.Method = http.MethodPost
	inner.URL = &url.URL{Path: internalRunOpPath}
	inner.RequestURI = internalRunOpPath
	if len(req.Body) > 0 {
		inner.Body = io.NopCloser(bytes.NewReader(req.Body))
		inner.ContentLength = int64(len(req.Body))
	} else {
		inner.Body = http.NoBody
		inner.ContentLength = 0
	}
	var params Params
	if op.param != "" {
		params = Params{{Key: op.param, Value: req.paramValue(op.field)}}
	}
	inner = inner.WithContext(context.WithValue(inner.Context(), ParamsKey, params))
	rec := &captureResponseWriter{header: http.Header{}}
	op.handler(s)(rec, inner)
	rec.writeTo(w)
}

// captureResponseWriter records an operation's answer so it can be handed back
// to the caller unchanged: status, headers and body.
type captureResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (c *captureResponseWriter) Header() http.Header { return c.header }

func (c *captureResponseWriter) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

func (c *captureResponseWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.body.Write(p)
}

func (c *captureResponseWriter) writeTo(w http.ResponseWriter) {
	for k, vs := range c.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	status := c.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(c.body.Bytes())
}

// routeParam reads one route parameter, however this router delivered it.
func routeParam(r *http.Request, name string) string {
	if r == nil || name == "" {
		return ""
	}
	if v := strings.TrimSpace(ParamsFromContext(r.Context()).ByName(name)); v != "" {
		return v
	}
	return strings.TrimSpace(r.PathValue(name))
}

// runOwnerOfRun is the replica that owns the run's live state, or nil when the
// run is not live anywhere else.
func (s *Server) runOwnerOfRun(ctx context.Context, runID string) *state.ReplicaPeer {
	runID = strings.TrimSpace(runID)
	if runID == "" || s == nil || s.RunRT == nil {
		return nil
	}
	rn, err := s.RunRT.GetRun(ctx, runID)
	if err != nil || rn == nil {
		return nil
	}
	return s.relayPeerForSession(ctx, rn.SessionID)
}

// runOwnerOfAction is the replica parked on the action's approval, or nil when
// the conversation it belongs to is not being driven elsewhere.
func (s *Server) runOwnerOfAction(ctx context.Context, actionID string) *state.ReplicaPeer {
	actionID = strings.TrimSpace(actionID)
	if actionID == "" || s == nil || s.Actions == nil {
		return nil
	}
	pending, err := s.Actions.Get(ctx, actionID)
	if err != nil {
		return nil
	}
	return s.relayPeerForSession(ctx, pending.SessionID)
}

// runOwnerOfPrimaryAgent is the replica running the active primary agent's
// conversation, when that is another one.
func (s *Server) runOwnerOfPrimaryAgent(ctx context.Context) *state.ReplicaPeer {
	if s == nil {
		return nil
	}
	run, err := s.findCancellablePrimaryRun(ctx)
	if err != nil || strings.TrimSpace(run.ID) == "" {
		return nil
	}
	return s.relayPeerForSession(ctx, run.SessionID)
}

// forwardRunOpToRunOwner wraps an operation keyed by the run in its route: when
// another replica owns the run, the operation is forwarded there and its answer
// returned unchanged. A run that is not live elsewhere is served here, so the
// request behaves exactly as it did before replicas existed.
func (s *Server) forwardRunOpToRunOwner(op string, inner http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := routeParam(r, "id")
		if peer := s.runOwnerOfRun(r.Context(), runID); peer != nil {
			if s.forwardRunOp(w, r, *peer, internalRunOpRequest{Op: op, RunID: runID}) {
				return
			}
		}
		inner(w, r)
	}
}

// forwardRunOpToActionOwner is forwardRunOpToRunOwner for the approval family,
// whose route names an action rather than a run.
func (s *Server) forwardRunOpToActionOwner(op string, inner http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actionID := routeParam(r, "id")
		if peer := s.runOwnerOfAction(r.Context(), actionID); peer != nil {
			if s.forwardRunOp(w, r, *peer, internalRunOpRequest{Op: op, ActionID: actionID}) {
				return
			}
		}
		inner(w, r)
	}
}

// forwardRunOpToPrimaryAgentOwner is forwardRunOpToRunOwner for the agent-wide
// cancel, whose route names an agent.
func (s *Server) forwardRunOpToPrimaryAgentOwner(op string, inner http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := routeParam(r, "agentId")
		if peer := s.runOwnerOfPrimaryAgent(r.Context()); peer != nil {
			if s.forwardRunOp(w, r, *peer, internalRunOpRequest{Op: op, AgentID: agentID}) {
				return
			}
		}
		inner(w, r)
	}
}

// otherReplicas is every reachable replica except this process: one registered a
// heartbeat ago, of the gateway kind, with an address to send to.
func (s *Server) otherReplicas(ctx context.Context) []state.ReplicaPeer {
	if s == nil || s.RunRT == nil {
		return nil
	}
	peers, err := s.RunRT.LiveReplicaPeers(ctx)
	if err != nil {
		slog.Warn("gateway forward: read live replicas", "err", err)
		return nil
	}
	out := make([]state.ReplicaPeer, 0, len(peers))
	for _, peer := range peers {
		if peer.Owner == s.RunRT.Owner || peer.Kind != state.RunOwnerKindGateway || peer.InternalAddr == "" {
			continue
		}
		out = append(out, peer)
	}
	return out
}

// forwardRunOpToEveryReplica wraps an operation that belongs to no single
// conversation: cancelling every agent, or cancelling a subagent by the id its
// own replica knows it by. This replica acts first and then asks the others, and
// the answers are folded into one — a boolean says whether anything anywhere was
// cancelled, a count how many.
func (s *Server) forwardRunOpToEveryReplica(op string, inner http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec := &captureResponseWriter{header: http.Header{}}
		inner(rec, r)
		if rec.status >= http.StatusBadRequest {
			rec.writeTo(w)
			return
		}
		merged := map[string]any{"ok": true}
		mergeReplicaAnswer(merged, rec.body.Bytes())
		for _, peer := range s.otherReplicas(r.Context()) {
			answer, status, err := s.postRunOp(r, peer, internalRunOpRequest{Op: op})
			if err != nil || status != http.StatusOK {
				// A replica that cannot be reached must not turn a cancel into a
				// failure for the others: what it holds is reported and the rest
				// still happened.
				slog.Warn("gateway forward: broadcast", "op", op, "owner", peer.Owner, "addr", peer.InternalAddr, "status", status, "err", err)
				continue
			}
			mergeReplicaAnswer(merged, answer.body.Bytes())
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(merged)
	}
}

// mergeReplicaAnswer folds one answer into the merged one.
func mergeReplicaAnswer(dst map[string]any, answer []byte) {
	var m map[string]any
	if json.Unmarshal(answer, &m) != nil {
		return
	}
	for k, v := range m {
		if k == "ok" {
			continue
		}
		switch got := v.(type) {
		case bool:
			prev, _ := dst[k].(bool)
			dst[k] = prev || got
		case float64:
			prev, _ := dst[k].(float64)
			dst[k] = prev + got
		}
	}
}

// forwardRunOp sends one operation to the replica that owns it and copies the
// answer back unchanged, reporting whether it did. It reads the request body
// itself and puts it back, so a request it does not forward still arrives whole
// at the handler.
func (s *Server) forwardRunOp(w http.ResponseWriter, r *http.Request, peer state.ReplicaPeer, req internalRunOpRequest) bool {
	answer, status, err := s.postRunOp(r, peer, req)
	if err != nil {
		writeGatewayRelayError(w, "正在运行该对话的副本不可达（"+peer.InternalAddr+"），本次操作没有执行。")
		return true
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		// A peer's refusal is about the replicas' own handshake, not about the
		// browser that asked: replaying the status would sign the user out of a
		// page whose credentials were never in question, and replaying the body
		// would hand out the peer's answer. The caller is told the operation did
		// not happen, and what refused it stays in this replica's log.
		slog.Warn("gateway forward: the owning replica refused the operation", "owner", peer.Owner, "addr", peer.InternalAddr, "op", req.Op, "status", status)
		writeGatewayRelayError(w, gatewayPeerRefusedText)
		return true
	}
	for k, vs := range answer.header {
		if strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Transfer-Encoding") {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(status)
	_, _ = w.Write(answer.body.Bytes())
	return true
}

// postRunOp is the request itself: it carries the body the caller sent, if any,
// and the shared gateway token the peer authenticates with.
func (s *Server) postRunOp(r *http.Request, peer state.ReplicaPeer, req internalRunOpRequest) (captureResponseWriter, int, error) {
	out := captureResponseWriter{header: http.Header{}}
	if s == nil || peer.InternalAddr == "" || r == nil {
		return out, 0, errors.New("no peer to forward to")
	}
	cfg := s.liveCfg()
	if cfg == nil {
		return out, 0, errors.New("gateway has no live configuration")
	}
	token := strings.TrimSpace(cfg.Gateway.Auth.Token)
	if token == "" {
		return out, 0, errors.New("gateway has no token to authenticate a peer with")
	}
	if req.Body == nil && r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			return out, 0, err
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if len(body) > 0 {
			req.Body = json.RawMessage(body)
		}
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return out, 0, err
	}
	target := strings.TrimSuffix(strings.TrimSpace(peer.InternalAddr), "/") + internalRunOpPath
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return out, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(httpReq)
	if err != nil {
		slog.Warn("gateway forward: the owning replica is unreachable", "owner", peer.Owner, "addr", peer.InternalAddr, "op", req.Op, "err", err)
		return out, 0, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if readErr != nil {
		return out, 0, readErr
	}
	for k, vs := range resp.Header {
		out.header[k] = vs
	}
	out.status = resp.StatusCode
	_, _ = out.body.Write(body)
	return out, resp.StatusCode, nil
}

// gatewayPeerRefusedText is what a caller gets when the replica that owns the
// conversation refused the forwarded operation. It is the whole of what the
// browser learns: no peer address, no token, no status detail — which replica
// refused and why is the operator's log, not the page's.
const gatewayPeerRefusedText = "正在运行该对话的副本拒绝了本次操作，本次操作没有执行。"

// writeGatewayRelayError is what a caller gets when the replica running the
// conversation cannot be reached: a 502 it can show, in words it can read,
// rather than a silent success that changed nothing.
func writeGatewayRelayError(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": "replica_unreachable", "message": text})
}

func (s *Server) handleRunCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if id == "" {
		id = strings.TrimSpace(r.PathValue("id"))
	}
	if id == "" {
		http.NotFound(w, r)
		return
	}
	sessionID := ""
	if s.RunRT != nil {
		rn, err := s.RunRT.GetRun(r.Context(), id)
		if err != nil || rn == nil {
			http.NotFound(w, r)
			return
		}
		owned, ownershipErr := s.sessionOwned(r.Context(), rn.SessionID)
		if ownershipErr != nil {
			http.Error(w, ownershipErr.Error(), http.StatusInternalServerError)
			return
		}
		if !owned {
			http.NotFound(w, r)
			return
		}
		sessionID = rn.SessionID
	}
	cancelled, parked := s.cancelRun(r.Context(), sessionID, id)
	if !cancelled && s.RunRT != nil {
		// No run of this process was stopped: the record still says how it
		// ended, as a supervisor kill.
		turn.FinalizeCancel(r.Context(), s.RunRT, id)
	}
	if parked {
		// Nothing drives a run parked on an approval to report its end.
		_ = s.publishGatewayRunEvent(r.Context(), sessionID, id, event.RunEventTurnCancelled, event.TurnCancelledPayload{Message: "cancelled"})
	}
	// A run still in flight hands its queue back once it has ended; until then
	// the preview is what it holds.
	preview := turn.PendingInputPreview{}
	if q, _, ok := s.runController().Queue(id); ok {
		preview = toPendingInputPreview(q.Preview())
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "cancelled": cancelled, "preview": preview})
}

type gatewayPostTurnOptions struct {
	SessionID       string
	ChannelID       string
	RunID           string
	AssistantText   string
	RunStartedAt    time.Time
	RunFinishedAt   time.Time
	WorkedMs        int64
	AssistantResult *agent.Result

	AppendAssistant bool
	BareMode        bool
}

func normalizedGatewaySessionID(sessionID string) string {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return "default"
	}
	return sid
}

func (s *Server) finishSuccessfulTurn(ctx context.Context, opt gatewayPostTurnOptions) {
	if s == nil {
		return
	}
	sid := normalizedGatewaySessionID(opt.SessionID)
	if opt.AppendAssistant {
		s.appendTranscriptTurns(ctx, sid, opt)
	}
}

func (s *Server) appendTranscriptTurns(ctx context.Context, sessionID string, opt gatewayPostTurnOptions) {
	if s == nil || s.Sessions == nil {
		return
	}
	sid := normalizedGatewaySessionID(sessionID)
	_ = s.Sessions.Ensure(ctx, sid, sid)
	if opt.AppendAssistant {
		model := ""
		// The transcript records the model this conversation actually ran
		// on: the session's own selection when it has one, not the process
		// default (and not the base runner, which a project session never
		// even runs on).
		if r := s.runnerFor(ctx, sessionID); r != nil {
			_, currentModel := run.PrimaryModelForSession(r, s.Sessions, sessionID)
			model = currentModel
		}
		turn.PersistAssistantTurn(ctx, s.Sessions, turn.AssistantTurn{
			SessionID: sid,
			RunID:     opt.RunID,
			Result:    opt.AssistantResult,
			// The caller's text has already been sanitised for outbound
			// delivery, so it is the copy of record for the transcript.
			Text:  opt.AssistantText,
			Model: model,
			End:   turn.RunEnd{StartedAt: opt.RunStartedAt, FinishedAt: opt.RunFinishedAt, Worked: time.Duration(opt.WorkedMs) * time.Millisecond},
		})
	}
}

// withAnswerStream streams the primary agent's answer to the page as it is
// written: published on the conversation's event log, as every subagent's
// stream is, and collected so a cancelled turn keeps what was already shown.
// The answer is always streamed — nothing judges it whole first.
func (s *Server) withAnswerStream(ctx context.Context, runID, sessionID string, streamed *bool, partial *turn.StreamPartial) context.Context {
	stream := run.EventStreamSink(context.WithoutCancel(ctx), s.RunEvents(), runID, sessionID, "", streamed)
	// Register the in-flight block so a page that arrives mid-turn is shown the
	// answer and reasoning the push-only deltas carried and no log keeps. The
	// entry is dropped when the run's ending event reaches the bus.
	s.streamParts().registerPrimary(runID, sessionID, partial)
	publishDelta, publishReasoning := stream.OnDelta, stream.OnReasoningDelta
	publishReasoningDone := stream.OnReasoningDone
	publishReasoningCarry := stream.OnReasoningCarry
	stream.OnDelta = func(text string) {
		partial.AppendContent(text)
		publishDelta(text)
	}
	stream.OnReasoningDelta = func(text string) {
		partial.AppendReasoning(text)
		publishReasoning(text)
	}
	stream.OnReasoningDone = func() {
		// The block ended and was written to the transcript as its own row;
		// drop it from the cancel buffer so a later cancel keeps only what is
		// still in flight.
		partial.ResponseReasoningDone()
		if publishReasoningDone != nil {
			publishReasoningDone()
		}
	}
	stream.OnReasoningCarry = func(carry, model string) {
		// The reply's reasoning as far as it is complete: a cancel stores it
		// with the half answer, so the reply the page watched being cut is
		// replayed to its model with the thinking it was written from.
		partial.SetReasoningCarry(carry, model)
		if publishReasoningCarry != nil {
			publishReasoningCarry(carry, model)
		}
	}
	stream.OnResponseCompleted = partial.ResponseCompleted
	// A transport retry kills the whole attempt: the partial the page was
	// shown so far belongs to a dead stream and the retry re-streams from the
	// first token. Drop it on every notice, Cleared included — at exhaustion
	// the final dead attempt only ever gets Cleared, and on success that
	// notice lands before the retried stream's first token, when the partial
	// is already empty — a no-op. Then let the sink's own publication reach
	// every watching surface.
	publishTransportRetry := stream.OnTransportRetry
	stream.OnTransportRetry = func(tr llm.TransportRetry) {
		partial.Reset()
		if publishTransportRetry != nil {
			publishTransportRetry(tr)
		}
	}
	return llm.WithStreamSink(ctx, stream)
}

// persistCancelledGatewayTurn writes the partial content a cancelled gateway
// (webchat) run already completed, so it is not lost from the transcript DB:
// the orchestration's partial session (completed assistant tool_calls and the
// tool results captured before the cancellation), then the answer and
// reasoning the page was already streamed, the same way the terminal keeps
// them. Dangling tool_calls left by a tool execution interrupted at cancel
// time are repaired.
func (s *Server) persistCancelledGatewayTurn(sessionID, runID string, end turn.RunEnd, capture *run.PartialSessionCapture, partial *turn.StreamPartial) {
	if s == nil || s.Sessions == nil {
		return
	}
	var captured []llm.Message
	if capture != nil {
		captured = capture.Snapshot()
	}
	partialCarry, partialCarryModel := partial.ReasoningCarry()
	turn.PersistCancelledTurn(context.Background(), s.Sessions, turn.CancelledTurn{
		SessionID:         sessionID,
		RunID:             runID,
		End:               end,
		Captured:          captured,
		PartialText:       partial.Content(),
		PartialReasoning:  partial.Reasoning(),
		PartialCarry:      partialCarry,
		PartialCarryModel: partialCarryModel,
		OnRepairError: func(err error) {
			slog.Error("cancelled turn: dangling tool_calls repair failed", "session", sessionID, "err", err)
		},
	})
}

// webTurnInput is a web message ready to become a turn: the text the model
// is sent, the stored row's parts, and what the row shows. The parts are the
// one description of the message: what the model is sent now is read back
// from them (run.UserInputParts), exactly as every later request replays the
// stored row, so the two are the same message.
type webTurnInput struct {
	text      string
	partsJSON string
	// display is what the row shows: the text as the user sent it, or, when
	// nothing was typed, a marker for each thing the message attached, the
	// way the terminal shows an image it sent.
	display string
}

// webMessageAttachesImage reports whether the message shows the model an
// image of its own: an image the @ picker attached, or an upload stored as an
// image. It only reads what the uploads were recorded as; a file that cannot
// be made available is prepareWebTurnInput's to report.
func (s *Server) webMessageAttachesImage(ctx context.Context, uploads, mentionImages []string) bool {
	for _, picked := range mentionImages {
		if strings.TrimSpace(picked) != "" {
			return true
		}
	}
	if s.Files == nil || s.Sessions == nil {
		return false
	}
	for _, fid := range uploads {
		fid = strings.TrimSpace(fid)
		if fid == "" {
			continue
		}
		if rec, err := s.Files.Get(ctx, s.Sessions.AgentID(), fid); err == nil && rec != nil &&
			strings.HasPrefix(strings.TrimSpace(rec.MediaType), "image/") {
			return true
		}
	}
	return false
}

// copyIntoTaskInputs copies one attached file into a task execution's inputs/
// folder under its own name, adding " (2)" and so on when that name is taken.
// It returns the name the file landed under, which is how the message names it.
func copyIntoTaskInputs(src, name, inputsDir string) (string, error) {
	if err := os.MkdirAll(inputsDir, 0o755); err != nil {
		return "", err
	}
	base := filepath.Base(strings.TrimSpace(name))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = filepath.Base(src)
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	target := filepath.Join(inputsDir, base)
	for i := 2; ; i++ {
		_, statErr := os.Lstat(target)
		if errors.Is(statErr, fs.ErrNotExist) {
			break
		}
		if statErr != nil {
			return "", statErr
		}
		target = filepath.Join(inputsDir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
	}
	if err := copyFile(src, target); err != nil {
		return "", err
	}
	return filepath.Base(target), nil
}

// prepareWebTurnInput makes everything a message attaches available to the
// agent before its turn exists. An upload is saved into the workspace and
// named in the text by the path it was saved at; an image upload and every
// image the @ picker attached are also shown to the model. Anything that
// cannot be made available fails the whole message with the error that
// stopped it: a turn never starts without a file the user attached.
func (s *Server) prepareWebTurnInput(ctx context.Context, sessionID, content string, uploads, mentionImages []string) (webTurnInput, error) {
	text := strings.TrimSpace(content)
	var blocks, markers, parts []string
	// A batch execution works in its own task workspace, so what the business
	// system attached is copied into that execution's inputs/ folder and named
	// by that path: the file the model reads is the one the next execution is
	// handed, and both are inside the workspace the task owns.
	taskInputs := ""
	if store := s.projectStore(); store != nil && len(uploads) > 0 {
		if exec, _, ok, err := store.ExecutionForSession(ctx, sessionID); err == nil && ok {
			taskInputs = filepath.Join(exec.Workspace, "inputs")
		}
	}
	for _, fid := range uploads {
		fid = strings.TrimSpace(fid)
		if fid == "" {
			continue
		}
		if s.Files == nil {
			return webTurnInput{}, fmt.Errorf("attachment %s: this gateway keeps no uploaded files", fid)
		}
		absPath, rec, err := s.Files.EnsureLocalFile(ctx, s.Sessions.AgentID(), fid)
		if err != nil {
			return webTurnInput{}, err
		}
		label, mimeType := fid, ""
		if rec != nil {
			label = firstNonBlank(rec.OriginalName, fid)
			mimeType = strings.TrimSpace(rec.MediaType)
		}
		named := label
		if mimeType != "" {
			named += " (" + mimeType + ")"
		}
		// A file saved into the project is named by its path inside the
		// project: the agent's working directory is the project root, so the
		// same file its file tools read is the one the user attached.
		savedAt := absPath
		if rec != nil && strings.TrimSpace(rec.ProjectPath) != "" {
			savedAt = rec.ProjectPath
		}
		if taskInputs != "" {
			copied, copyErr := copyIntoTaskInputs(absPath, label, taskInputs)
			if copyErr != nil {
				return webTurnInput{}, copyErr
			}
			savedAt = "inputs/" + copied
		}
		blocks = append(blocks, "[Attachment "+named+"]\nSaved at: "+savedAt+"\nUse your file tools to open and process it as needed.")
		markers = append(markers, "[Attachment "+label+"]")
		if strings.HasPrefix(mimeType, "image/") {
			parts = append(parts, state.FileReferencePartJSON(fid, label, mimeType))
		} else {
			parts = append(parts, state.AttachmentPartJSON(fid, label, mimeType))
		}
	}
	// The @ picker's images are the conversation's files: a message that
	// attached one resolves it against the turn's session — the same root the
	// composer's file view used — never the agent's workspace. A message with
	// no attached image never asks for a root at all.
	var resolver turn.WorkspaceResolver
	if len(mentionImages) > 0 {
		root, rootErr := s.filesRoot(ctx, sessionID, "")
		if rootErr != nil {
			return webTurnInput{}, rootErr
		}
		resolver = turn.WorkspaceResolver{Root: root}
	}
	var imageMarkers []string
	seen := map[string]bool{}
	for _, picked := range mentionImages {
		picked = strings.TrimSpace(picked)
		abs, _, ok := resolver.Resolve(picked)
		if !ok {
			return webTurnInput{}, fmt.Errorf("attached image %q is not in the workspace", picked)
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		// Read now, so the reference records the MIME the file's own bytes name.
		// A file the ladder cannot send is no longer fatal and gets no
		// file_reference at all: the model is sent this row through
		// run.UserInputParts (server.go), so the one note it must see comes from
		// rehydrateMessageImages. Writing a second one here would say it twice —
		// and the note rides in the message text below, where the user reads it.
		image, err := llm.ImageFile(abs)
		if err != nil {
			blocks = append(blocks, llm.UnsendableImageNote(picked, "", err.Error()))
			continue
		}
		imageMarkers = append(imageMarkers, fmt.Sprintf("[Image #%d]", len(imageMarkers)+1))
		parts = append(parts, state.FileReferencePartJSON(abs, picked, image.MIMEType))
	}
	markers = append(markers, imageMarkers...)
	in := webTurnInput{display: text}
	if text == "" {
		// Nothing typed: the images stand for the text, as they do in the
		// terminal, and the row shows what was attached.
		text = strings.Join(imageMarkers, "\n")
		in.display = strings.Join(markers, "\n")
	}
	in.text = strings.TrimSpace(strings.Join(append([]string{text}, blocks...), "\n\n"))
	in.partsJSON = state.MessagePartsJSON(llm.UserMessage(llm.Text(in.text)), in.text)
	for _, part := range parts {
		in.partsJSON = state.AppendRawPartJSON(in.partsJSON, part)
	}
	return in, nil
}

// withWebInputParts gives a message that joins a running turn — a steer into
// the conversation's run, a message to one of its subagents — what a new turn
// sends for the same words and attachments: the text naming each attached file
// and where it was saved, each image, and the line naming the image's file
// (run.UserInputParts). The message keeps its own text and attachment lists,
// which are what its queue shows and hands back for editing.
func (s *Server) withWebInputParts(ctx context.Context, runner *run.Runner, sessionID string, in run.Input) (run.Input, error) {
	if len(in.Attachments) == 0 && len(in.MentionImages) == 0 {
		return in, nil
	}
	turnInput, err := s.prepareWebTurnInput(ctx, sessionID, in.Text, in.Attachments, in.MentionImages)
	if err != nil {
		return in, err
	}
	var resolver run.FileReferenceResolver = run.PathResolver{}
	if runner != nil && runner.FileResolver != nil {
		resolver = runner.FileResolver
	}
	in.Parts = run.UserInputParts(ctx, resolver, turnInput.partsJSON)
	return in, nil
}

func firstNonBlank(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (s *Server) assembleContextSnapshotForUserInput(ctx context.Context, sessionID string, channel string, input string, rawInput string) (assembly.AssemblyResult, bool, error) {
	if s == nil || s.Env == nil {
		return assembly.AssemblyResult{}, false, nil
	}
	return s.Env.AssembleContextSnapshot(ctx, hook.HookContext{
		SessionID: sessionID,
		Channel:   channel,
		Trigger:   "user",
		RawInput:  rawInput,
	}, input)
}

// autoCompactBeforeUserAppend compacts the conversation before a turn when it
// no longer leaves room for one. The compaction publishes its own lifecycle
// to the session's event bus, which is what every connection draws it from.
func (s *Server) autoCompactBeforeUserAppend(ctx context.Context, sessionID string, channelID string, input string, rawInput string) error {
	if s == nil || s.Sessions == nil {
		return nil
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		sid = "default"
	}
	ch := strings.TrimSpace(channelID)
	if ch == "" {
		ch = "webchat"
	}
	svc := run.CompactionService(s.runnerFor(ctx, sid), s.Sessions)
	_, err := assembly.RunPreflight(ctx, assembly.PreflightConfig{
		HookContext: hook.HookContext{
			SessionID: sid,
			Channel:   ch,
			Trigger:   "user",
			RawInput:  rawInput,
		},
		Input: input,
		Now:   time.Now,
		Assemble: func(ctx context.Context, hc hook.HookContext, input string) (assembly.AssemblyResult, bool, error) {
			return s.assembleContextSnapshotForUserInput(ctx, hc.SessionID, hc.Channel, input, hc.RawInput)
		},
		AutoCompact: svc.AutoCompactSession,
	})
	return err
}

func (s *Server) compactBudgetOptions() state.TokenBudgetOptions {
	if s == nil || s.Runner == nil {
		return state.TokenBudgetOptions{}
	}
	return run.CompactBudgetOptions(s.Runner.AppCfg)
}

// Auto-continue on the web: the engine's continuation of a conversation stopped
// by a usage limit runs here as a detached webchat turn, and a page cancels a
// waiting one over the socket.

// autoContinueTrigger names a continuation turn to hooks and telemetry, next
// to "user" and "resume".
const autoContinueTrigger = "auto_continue"

// wsOpCancelAutoContinue is the client op that stops a session's pending
// continuation, and wsOpAutoContinueCancelAck its reply.
const (
	wsOpCancelAutoContinue    = "cancel_auto_continue"
	wsOpAutoContinueCancelAck = "auto_continue_cancel_ack"
)

// autoContinueConfig installs auto-continue on the gateway's Core. Only web
// chat turns are continued: a channel user has no way to see the wait or
// cancel it, so a channel conversation is never resumed behind their back.
// The lifecycle is published on the run-event bus, which persists it to the
// session's log and delivers it to every page observing the session.
func (s *Server) autoContinueConfig() turn.AutoContinueConfig {
	return turn.AutoContinueConfig{
		Continue: s.continueAfterUsageLimit,
		Events:   s.RunEvents(),
		Surfaces: []turn.Surface{turn.SurfaceWebChat},
	}
}

// detachedTurn is a turn the runtime starts with no page behind it: a
// continuation after a usage limit, a heartbeat. Every page observing the
// session watches it through the event log; nothing owns it but the run.
type detachedTurn struct {
	SessionID string
	Prompt    string
	// Trigger names the turn to hooks and telemetry.
	Trigger string
	// Origin is where the turn comes from. Its surface decides whether a
	// usage limit hit by this turn is continued by itself later.
	Origin turn.Origin
	// Unattended marks a turn nobody is watching: a usage limit ends it for
	// good — the next trigger comes on schedule or by a person, never by a
	// continuation timer. A heartbeat sets it; a continued web turn does
	// not, because the continuation itself must be able to continue again.
	Unattended bool
	// PromptOrigin marks the prompt row as written on the person's behalf
	// (state.MessageOrigin*); empty when the prompt is the runtime's own text.
	PromptOrigin string
	// Announce publishes, under the new run and before turn_started, what
	// began it. Nil when the engine has announced it already.
	Announce func(ctx context.Context, runID string)
	// OnParked is told when the turn stops on an approval, after the run is
	// parked for any page to resume. Nil when nobody needs telling: a fire
	// uses it to warn its job's delivery target that the answer is waiting.
	OnParked func(ctx context.Context, gate *tool.RequiresActionError)
	// BatchInput is the original task text a batch execution's turn carries,
	// before any surface prepared it. Nil for every ordinary turn.
	BatchInput *turn.BatchTurnInput
}

// startDetachedTurn starts t's turn as a detached run. It is detached for the
// same reason an approval resume is: the page — if there ever was one — that
// began what this turn continues may have been closed hours ago, and whichever
// pages are open now observe the session through the event bus rather than own
// the run.
//
// The run is created before returning so a failure to start is reported to
// the caller rather than swallowed into a run nobody watches; the turn itself
// runs on after it. Whether the session may start one is CreateRun's one
// atomic answer — every process and every entry point is held to it — so a
// turn that cannot start leaves no run and no row behind.
func (s *Server) startDetachedTurn(t detachedTurn) (string, error) {
	sid := strings.TrimSpace(t.SessionID)
	ctx := context.Background()
	// A batch execution's turn is admitted before its run exists, so a refused
	// turn leaves no run and no row behind. The preview reads only: the
	// transition that starts the execution happens in Submit, under the
	// session lock.
	if batch, err := s.admitDetachedBatchTurn(ctx, sid, &t); err != nil {
		return "", err
	} else if batch {
		// Nobody is watching a scheduled or detached batch turn either.
		t.Unattended = true
	}
	startedAt := time.Now()
	runID := "ws-" + fmt.Sprint(startedAt.UnixNano())
	if s.RunRT != nil {
		rr, err := s.RunRT.CreateRun(ctx, sid, t.Prompt)
		if err != nil {
			return "", err
		}
		runID = rr.ID
	}
	runCtx, cancelCause := context.WithCancelCause(context.Background())
	s.runStartedAt.Store(runID, startedAt)
	// Tracked like any web run, so the page's stop control and the steer and
	// queue inputs reach it by its run id.
	s.runController().Track(runID, sid, func() { cancelCause(context.Canceled) })
	var inputRT *run.TurnInputRuntime
	if q, _, ok := s.runController().Queue(runID); ok {
		s.watchRunQueue(q, runID, sid)
		inputRT = q.Runtime()
	}
	go s.runDetachedTurn(runCtx, t, runID, startedAt, inputRT)
	return runID, nil
}

// admitDetachedBatchTurn runs a detached turn through the batch admission
// table when its conversation is a task execution, and fills in the turn's
// BatchInput so Submit can repeat the decision under the session lock. It
// reports whether this was a batch turn; a non-batch conversation is left
// exactly as it was, with no store read at all.
func (s *Server) admitDetachedBatchTurn(ctx context.Context, sid string, t *detachedTurn) (bool, error) {
	if t == nil || strings.TrimSpace(sid) == "" {
		return false, nil
	}
	store := s.projectStore()
	if store == nil {
		return false, nil
	}
	ptype, err := store.SessionProjectType(ctx, sid)
	if err != nil || ptype != state.ProjectTypeBatch {
		return false, nil
	}
	// The task's own prompt is what the turn is; the admission decides whether
	// this execution runs on it, on its fixed input, or not at all.
	t.BatchInput = &turn.BatchTurnInput{Text: t.Prompt}
	if _, err := turn.AdmitBatchTurn(ctx, store, sid, t.Prompt, false); err != nil {
		return true, err
	}
	return true, nil
}

// continueAfterUsageLimit starts the continuation. A subagent's continuation is
// a message the engine sends that subagent; the conversation's own runs as a
// detached run. Both are detached for the same reason an approval resume is:
// the page that sent the stopped turn may have been closed hours ago, and
// whichever pages are open now observe the session through the event bus rather
// than own the run.
func (s *Server) continueAfterUsageLimit(_ context.Context, plan turn.AutoContinuePlan, prompt string) error {
	if s == nil || s.Core == nil {
		return turn.ErrAutoContinueUnavailable
	}
	if strings.TrimSpace(plan.SessionID) == "" {
		return turn.ErrAutoContinueUnavailable
	}
	if strings.TrimSpace(plan.AgentKey) != "" {
		runner := s.runnerFor(context.Background(), plan.SessionID)
		if runner == nil {
			return turn.ErrAutoContinueUnavailable
		}
		return s.Env.ContinueSubagent(runner, s.subagentConversationSurface(plan.SessionID), plan, prompt)
	}
	_, err := s.startDetachedTurn(detachedTurn{
		SessionID: plan.SessionID,
		Prompt:    prompt,
		Trigger:   autoContinueTrigger,
		Origin:    turn.Origin{Surface: turn.SurfaceWebChat, ChannelID: "webchat"},
	})
	if errors.Is(err, state.ErrSessionBusy) {
		return turn.ErrAutoContinueUnavailable
	}
	return err
}

// runDetachedTurn runs the turn and reports every way it can end on the
// session's event log, the way the detached approval resume does.
func (s *Server) runDetachedTurn(runCtx context.Context, t detachedTurn, runID string, startedAt time.Time, inputRT *run.TurnInputRuntime) {
	sid := t.SessionID
	ctx := context.Background()
	if t.Announce != nil {
		t.Announce(ctx, runID)
	}
	_ = s.publishGatewayRunEvent(ctx, sid, runID, event.RunEventTurnStarted, event.TurnStartedPayload{})
	if s.Sessions != nil {
		turn.PersistUserTurn(ctx, s.Sessions, turn.UserTurn{SessionID: sid, RunID: runID, ModelInput: t.Prompt, RawInput: t.Prompt, Origin: t.PromptOrigin})
	}
	agCtx := llm.WithAgentSessionID(runCtx, sid)
	agCtx = tool.WithConversationSessionID(agCtx, sid)
	agCtx = tool.WithRunID(agCtx, runID)
	capture := run.NewPartialSessionCapture()
	agCtx = run.WithPartialSessionCapture(agCtx, capture)
	var streamed bool
	partial := &turn.StreamPartial{}
	agCtx = s.withAnswerStream(agCtx, runID, sid, &streamed, partial)
	if inputRT != nil {
		agCtx = run.WithTurnInputRuntime(agCtx, inputRT)
	}
	agCtx = process.AgentContextForProject(agCtx, s.stateRoot(), sid, s.sessionProjectKey(agCtx, sid))
	agCtx = s.withDetachedGatewayApprovalHooks(agCtx, sid, runID)

	outcome, err := s.Core.Submit(agCtx, turn.TurnRequest{
		SessionID:                sid,
		Origin:                   t.Origin,
		Trigger:                  t.Trigger,
		UserText:                 t.Prompt,
		RawInput:                 t.Prompt,
		ExistingRunID:            runID,
		Unattended:               t.Unattended,
		BatchInput:               t.BatchInput,
		AgentContextIsRunContext: true,
	}, nil)
	if outcome.Status == turn.TurnWaitingApproval && outcome.Resume != nil {
		err = &tool.RequiresActionError{
			RunID:           outcome.RunID,
			ActionID:        outcome.Resume.ActionID,
			ToolName:        outcome.Resume.ToolName,
			SessionSnapshot: outcome.Resume.SessionSnapshot,
		}
	}
	finishedAt := time.Now()
	elapsed := finishedAt.Sub(startedAt)
	if elapsed < 0 {
		elapsed = 0
	}

	var gate *tool.RequiresActionError
	switch {
	case errors.As(err, &gate):
		s.parkDetachedRunOnApproval(ctx, sid, runID, gate)
		if t.OnParked != nil {
			t.OnParked(ctx, gate)
		}
		return
	case errors.Is(err, context.Canceled):
		s.persistCancelledGatewayTurn(sid, runID, turn.RunEnd{StartedAt: startedAt, FinishedAt: finishedAt, Worked: elapsed}, capture, partial)
		if s.RunRT != nil {
			_ = s.RunRT.CancelRunningDescendants(ctx, runID)
		}
		s.finishRun(ctx, sid, runID, state.RunStatusCancelled)
		_ = s.publishGatewayRunEvent(ctx, sid, runID, event.RunEventTurnCancelled, event.TurnCancelledPayload{Message: "cancelled"})
		return
	case err != nil:
		// What the turn did before it failed stays in the transcript, so a
		// further continuation — the engine arms one if this was the limit
		// again — picks up after it rather than before it.
		s.persistCancelledGatewayTurn(sid, runID, turn.RunEnd{StartedAt: startedAt, FinishedAt: finishedAt, Worked: elapsed}, capture, partial)
		slog.Error("detached turn failed", "trigger", t.Trigger, "run_id", runID, "session_id", sid, "err", err)
		errText := llm.ExplainError(err)
		if s.RunRT != nil {
			_ = s.RunRT.FailRunningDescendants(ctx, runID)
		}
		s.finishRun(ctx, sid, runID, state.RunStatusFailed)
		_ = s.publishGatewayRunEvent(ctx, sid, runID, event.RunEventTurnError, event.TurnErrorPayload{Error: errText, Message: errText, Detail: newTurnErrorDetail(err)})
		return
	}

	answer := ""
	if outcome.Result != nil {
		answer = outcome.Result.TextContent()
	}
	if strings.TrimSpace(answer) != "" && !streamed {
		// Not streamed — sent whole by the provider — so it reaches the page
		// once, as its stream would have.
		_ = s.RunEvents().Publish(ctx, event.NewRunEvent("", runID, sid, event.RunEventAssistantDelta, event.AssistantDeltaPayload{Text: answer}, time.Now()))
	}
	s.finishSuccessfulTurn(ctx, gatewayPostTurnOptions{
		SessionID:       sid,
		ChannelID:       "webchat",
		RunID:           runID,
		AssistantText:   answer,
		RunStartedAt:    startedAt,
		RunFinishedAt:   finishedAt,
		WorkedMs:        elapsed.Milliseconds(),
		AssistantResult: outcome.Result,
		AppendAssistant: true,
	})
	s.finishRun(ctx, sid, runID, state.RunStatusDone)
	// No answer on the completion record: the run's words are in the transcript,
	// written as each block ends, and this durable event only says the run ended.
	_ = s.publishGatewayRunEvent(ctx, sid, runID, event.RunEventTurnCompleted, event.TurnCompletedPayload{
		ElapsedMS: elapsed.Milliseconds(),
	})
}

// heartbeatTrigger names a heartbeat's turn to hooks and telemetry.
const heartbeatTrigger = "heartbeat"

// startHeartbeatTurn starts a session's heartbeat as a turn of that
// conversation, exactly as if a page had sent the prompt: the prompt row is
// marked as the heartbeat's, the answer streams to every page watching, and
// the run ends with its worked line. It returns once the run has started.
func (s *Server) startHeartbeatTurn(_ context.Context, sessionID, prompt string) error {
	if s == nil || s.Core == nil {
		return fmt.Errorf("heartbeat: no turn runtime")
	}
	_, err := s.startDetachedTurn(detachedTurn{
		SessionID:    sessionID,
		Prompt:       prompt,
		Trigger:      heartbeatTrigger,
		Origin:       turn.Origin{Surface: turn.SurfaceWebChat, ChannelID: "heartbeat"},
		Unattended:   true,
		PromptOrigin: state.MessageOriginHeartbeat,
		Announce: func(ctx context.Context, runID string) {
			_ = s.publishGatewayRunEvent(ctx, sessionID, runID, event.RunEventHeartbeatFired, event.HeartbeatFiredPayload{Prompt: prompt})
		},
	})
	return err
}

// cronTrigger names a fire's turn to hooks and telemetry.
const cronTrigger = "cron"

// startCronFire opens one fire of a scheduled task as a conversation of its
// own: the session is born a cron session — in the job's project when it is
// bound to one — the prompt row is marked as the task's, and the turn runs
// exactly as a web turn does. A fire is a channel-like turn: nobody is
// watching it, so a usage limit is not continued behind the person's back;
// the next fire comes on schedule.
func (s *Server) startCronFire(ctx context.Context, fire turn.CronFire) error {
	if s == nil || s.Core == nil {
		return fmt.Errorf("cron: no turn runtime")
	}
	// A fire always runs in its job's project, and a project's directory must
	// still exist: a fire whose root is gone is recorded as failed with that
	// reason rather than running somewhere it does not belong.
	pid := strings.TrimSpace(fire.Job.ProjectID)
	if pid == "" {
		return fmt.Errorf("cron: job has no project")
	}
	p, err := s.projectStore().Get(ctx, pid)
	if err != nil {
		return err
	}
	if info, statErr := os.Stat(p.Root); statErr != nil || !info.IsDir() {
		return fmt.Errorf("project directory no longer exists: %s", p.Root)
	}
	birth := state.SessionBirth{
		Source:    state.SessionSourceCron,
		Cwd:       p.Root,
		GitBranch: memory.GitBranch(p.Root),
		ProjectID: p.ID,
	}
	if err := s.Sessions.EnsureAt(ctx, fire.SessionID, fire.Title, birth); err != nil {
		return err
	}
	if err := s.bindSessionToProject(ctx, fire.SessionID, p); err != nil {
		return err
	}
	if p.ProjectType == state.ProjectTypeBatch {
		// A fire of a batch project is a task of its own: one more thing to
		// run, not another execution of an old one. Its input is fixed by the
		// first turn Submit runs, atomically, so nothing is recorded here.
		task, taskErr := s.projectStore().CreateTask(ctx, state.Task{
			ProjectID: p.ID,
			Title:     fire.Title,
			Origin:    state.TaskOriginCron,
			OriginRef: fire.Job.ID,
		})
		if taskErr != nil {
			return taskErr
		}
		exec, execErr := s.projectStore().AddTaskExecution(ctx, task.ID, fire.SessionID, p.Root)
		if execErr != nil {
			return execErr
		}
		for _, dir := range []string{"inputs", "outputs", ".work"} {
			if mkErr := os.MkdirAll(filepath.Join(exec.Workspace, dir), 0o755); mkErr != nil {
				return mkErr
			}
		}
	}
	_, err = s.startDetachedTurn(detachedTurn{
		SessionID:    fire.SessionID,
		Prompt:       fire.Job.Prompt,
		Trigger:      cronTrigger,
		Origin:       turn.Origin{Surface: turn.SurfaceChannel, ChannelID: cronTrigger},
		PromptOrigin: state.MessageOriginCron,
		OnParked: func(ctx context.Context, gate *tool.RequiresActionError) {
			s.cron().FireParked(ctx, fire.SessionID, approvalNoticeText(gate.ToolName))
		},
	})
	return err
}

// settleScheduledFire asks the scheduler to look at a fire the moment its
// run reports an end, instead of on the next tick. It carries nothing but
// the session: the scheduler reads the ending back from the store, the
// same way its tick does.
func (s *Server) settleScheduledFire(evt event.RunEvent) {
	switch evt.Type {
	case event.RunEventTurnCompleted, event.RunEventTurnError, event.RunEventTurnCancelled:
	default:
		return
	}
	// Only a primary run's end settles anything: a subagent's end belongs to
	// its parent's turn, which has not ended yet.
	run, err := s.RunRT.GetRun(context.Background(), evt.RunID)
	if err != nil || run == nil || run.ParentRunID != "" {
		return
	}
	go s.cron().SettleFire(context.Background(), evt.SessionID)
}

// parkDetachedRunOnApproval leaves a detached run waiting on an approval
// exactly as the web turn loop leaves one it owns: the pre-gate history and
// the resumable wait are recorded, the gate is published for every page, and
// the run is marked waiting so a decision from any page resumes it.
func (s *Server) parkDetachedRunOnApproval(ctx context.Context, sid, runID string, gate *tool.RequiresActionError) {
	if s.RunRT != nil {
		if s.Sessions != nil && len(gate.SessionSnapshot) > 0 {
			_ = s.Sessions.AppendMessageSequenceForRun(ctx, sid, runID, gate.SessionSnapshot, "", "")
		}
		input, _ := json.Marshal(gate.ToolInput)
		_ = s.RunRT.SetWaitingAction(ctx, runID, state.Wait{
			RunID:           runID,
			ActionID:        gate.ActionID,
			ToolName:        gate.ToolName,
			ToolInputJSON:   string(input),
			SessionSnapshot: append([]llm.Message(nil), gate.SessionSnapshot...),
			AgentID:         gate.AgentID,
			SubagentType:    gate.SubagentType,
			SubagentRunID:   strings.TrimSpace(gate.RunID),
		})
	}
	if strings.TrimSpace(gate.RunID) == "" {
		gate.RunID = runID
	}
	s.publishDetachedGatewayApprovalRequest(ctx, sid, gate)
	s.runController().WaitApproval(runID)
}

// handleCancelAutoContinueMessage answers a page's cancel. It is answered by
// the socket's reader, like an approval, so it takes effect even while the
// connection's loop is busy.
func (s *Server) handleCancelAutoContinueMessage(ctx context.Context, m wsClientMsg) wsServerMsg {
	sid := strings.TrimSpace(m.SessionID)
	reply := wsServerMsg{Op: wsOpAutoContinueCancelAck, RequestID: m.RequestID, SessionID: sid}
	if sid == "" {
		reply.Error = "session_id required for op: " + wsOpCancelAutoContinue
		return reply
	}
	if owned, err := s.sessionOwned(ctx, sid); err != nil || !owned {
		reply.Error = "unknown session"
		return reply
	}
	agentID := strings.TrimSpace(m.AgentID)
	var cancelled bool
	switch {
	case agentID == "":
		cancelled = s.Core != nil && s.Core.CancelAutoContinue(ctx, sid, turn.AutoContinueCancelledByUser)
	case s.subagentNamed(sid, agentID):
		// The subagent must belong to this conversation and this primary
		// agent; another conversation's subagent is not found, exactly as its
		// input channel is.
		cancelled = s.Core != nil && s.Core.CancelAutoContinueForAgent(ctx, sid, agentID, turn.AutoContinueCancelledByUser)
	}
	reply.Data = map[string]any{"cancelled": cancelled}
	return reply
}

// autoContinuePlans is every continuation waiting in a conversation: its own
// first (when it has one), then each of its subagents'. A page binding to the
// conversation is told all of them, so a wait armed for a subagent before the
// page opened still shows in that subagent's view.
func (s *Server) autoContinuePlans(sid string) []event.AutoContinueScheduledPayload {
	if s == nil || s.Core == nil {
		return nil
	}
	plans := s.Core.PendingAutoContinuePlans(sid)
	out := make([]event.AutoContinueScheduledPayload, 0, len(plans))
	for _, plan := range plans {
		out = append(out, plan.Payload())
	}
	return out
}

// sessionBoundData is the session_bound reply's data: where the replay starts
// and ends, the continuations pending right now, when there are any, and the
// peer messages waiting for the person's decision. The conversation's own
// continuation is also sent as the single auto_continue every client already
// reads; the whole list, subagents included, travels as auto_continues.
//
// A held peer message is live state of the same kind as a continuation: it is
// not in the transcript yet, and a page that binds after it arrived has to be
// told, or the banner only appears when the next message comes in (plan 200 §3).
func (s *Server) sessionBoundData(sessionID string, cursor, highWater int64, plans []event.AutoContinueScheduledPayload) map[string]any {
	data := map[string]any{
		"cursor":         cursor,
		"high_water":     highWater,
		"schema_version": event.RunEventSchemaVersion,
	}
	if held := s.peerHeldRows(sessionID); len(held) > 0 {
		data["peer_held"] = held
	}
	if len(plans) == 0 {
		return data
	}
	data["auto_continues"] = plans
	for _, plan := range plans {
		if plan.AgentID == "" {
			data["auto_continue"] = plan
			break
		}
	}
	return data
}

// handleAutoContinue is the REST face of a session's pending continuation:
// GET says whether one is waiting and when it runs, DELETE cancels it. The
// page cancels over this rather than its observer socket, which may be
// reconnecting at the moment the reader clicks.
func (s *Server) handleAutoContinue(w http.ResponseWriter, r *http.Request) {
	sid := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sid == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}
	owned, err := s.sessionOwned(r.Context(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !owned {
		http.NotFound(w, r)
		return
	}
	plans := s.autoContinuePlans(sid)
	switch r.Method {
	case http.MethodGet:
		out := map[string]any{"session_id": sid, "pending": len(plans) > 0}
		if len(plans) > 0 {
			out["auto_continues"] = plans
			for _, plan := range plans {
				if plan.AgentID == "" {
					out["auto_continue"] = plan
					break
				}
			}
		}
		writeAgentsJSON(w, out)
	case http.MethodDelete:
		agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
		var cancelled bool
		switch {
		case agentID == "":
			cancelled = s.Core != nil && s.Core.CancelAutoContinue(r.Context(), sid, turn.AutoContinueCancelledByUser)
		case s.subagentNamed(sid, agentID):
			cancelled = s.Core != nil && s.Core.CancelAutoContinueForAgent(r.Context(), sid, agentID, turn.AutoContinueCancelledByUser)
		}
		writeAgentsJSON(w, map[string]any{"session_id": sid, "cancelled": cancelled})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}
