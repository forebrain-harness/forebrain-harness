// Run input, cancellation, and post-turn persistence.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

func toPendingInputPreview(in run.QueuePreview) turn.PendingInputPreview {
	return turn.PendingInputPreview{
		PendingSteers:  in.Steers,
		RejectedSteers: in.Rejected,
		QueuedMessages: in.FollowUp,
	}
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
	var req runInputRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	accepted := q.Steer(req.input())
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

func writeRunInputJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
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
// An answer the output guardrail must judge whole is not streamed; it is sent
// once, judged, when the run ends.
func (s *Server) withAnswerStream(ctx context.Context, runID, sessionID string, streamed *bool, partial *turn.StreamPartial) context.Context {
	if safety.OutputRailEnabled(s.liveCfg()) {
		return ctx
	}
	stream := run.EventStreamSink(context.WithoutCancel(ctx), s.RunEvents(), runID, sessionID, "", streamed)
	publishDelta, publishReasoning := stream.OnDelta, stream.OnReasoningDelta
	stream.OnDelta = func(text string) {
		partial.AppendContent(text)
		publishDelta(text)
	}
	stream.OnReasoningDelta = func(text string) {
		partial.AppendReasoning(text)
		publishReasoning(text)
	}
	stream.OnResponseCompleted = partial.ResponseCompleted
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
	turn.PersistCancelledTurn(context.Background(), s.Sessions, turn.CancelledTurn{
		SessionID:        sessionID,
		RunID:            runID,
		End:              end,
		Captured:         captured,
		PartialText:      partial.Content(),
		PartialReasoning: partial.Reasoning(),
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

// prepareWebTurnInput makes everything a message attaches available to the
// agent before its turn exists. An upload is saved into the workspace and
// named in the text by the path it was saved at; an image upload and every
// image the @ picker attached are also shown to the model. Anything that
// cannot be made available fails the whole message with the error that
// stopped it: a turn never starts without a file the user attached.
func (s *Server) prepareWebTurnInput(ctx context.Context, content string, uploads, mentionImages []string) (webTurnInput, error) {
	text := strings.TrimSpace(content)
	var blocks, markers, parts []string
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
		blocks = append(blocks, "[Attachment "+named+"]\nSaved at: "+absPath+"\nUse your file tools to open and process it as needed.")
		markers = append(markers, "[Attachment "+label+"]")
		if strings.HasPrefix(mimeType, "image/") {
			parts = append(parts, state.FileReferencePartJSON(fid, label, mimeType))
		} else {
			parts = append(parts, state.AttachmentPartJSON(fid, label, mimeType))
		}
	}
	resolver := turn.WorkspaceResolver{Root: s.mentionRoot()}
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
		// Read now, so an image the model cannot be shown stops the message
		// here rather than reaching the model as a bare name.
		image, err := llm.ImageFile(abs)
		if err != nil {
			return webTurnInput{}, err
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
	return s.Env.AssembleContextSnapshot(hook.HookContext{
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

func (s *Server) compactExplicitLimit() int {
	if s == nil || s.Runner == nil || s.Runner.AppCfg == nil {
		return 0
	}
	return s.Runner.AppCfg.Compact.ModelAutoCompactTokenLimit
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
		q.SetChangeHook(func() {
			s.appendPendingInputUpdated(context.Background(), runID, sid, toPendingInputPreview(q.Preview()))
		})
		inputRT = q.Runtime()
	}
	go s.runDetachedTurn(runCtx, t, runID, startedAt, inputRT)
	return runID, nil
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
	agCtx = process.AgentContextForProject(agCtx, s.stateRoot(), sid, s.projectKey())
	agCtx = s.withDetachedGatewayApprovalHooks(agCtx, sid, runID)

	outcome, err := s.Core.Submit(agCtx, turn.TurnRequest{
		SessionID:                sid,
		Origin:                   t.Origin,
		Trigger:                  t.Trigger,
		UserText:                 t.Prompt,
		RawInput:                 t.Prompt,
		ExistingRunID:            runID,
		Unattended:               t.Unattended,
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
	if lc := s.liveCfg(); lc != nil {
		answer = safety.SanitizeOutbound(lc, answer)
	}
	if strings.TrimSpace(answer) != "" && !streamed {
		// Not streamed — sent whole by the provider, or held for the output
		// guardrail — so it reaches the page once, as its stream would have.
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
	_ = s.publishGatewayRunEvent(ctx, sid, runID, event.RunEventTurnCompleted, event.TurnCompletedPayload{
		Text:      answer,
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
	birth := state.SessionBirth{Source: state.SessionSourceCron}
	var project state.Project
	if pid := strings.TrimSpace(fire.Job.ProjectID); pid != "" {
		p, err := s.projectStore().Get(ctx, pid)
		if err != nil {
			return err
		}
		project = p
		birth.Cwd, birth.GitBranch = p.Root, memory.GitBranch(p.Root)
	}
	if err := s.Sessions.EnsureAt(ctx, fire.SessionID, fire.Title, birth); err != nil {
		return err
	}
	if strings.TrimSpace(fire.Job.ProjectID) != "" {
		if err := s.bindSessionToProject(ctx, fire.SessionID, project); err != nil {
			return err
		}
	}
	_, err := s.startDetachedTurn(detachedTurn{
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
// and ends, and the continuations pending right now, when there are any. The
// conversation's own is also sent as the single auto_continue every client
// already reads; the whole list, subagents included, travels as auto_continues.
func sessionBoundData(cursor, highWater int64, plans []event.AutoContinueScheduledPayload) map[string]any {
	data := map[string]any{
		"cursor":         cursor,
		"high_water":     highWater,
		"schema_version": event.RunEventSchemaVersion,
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
