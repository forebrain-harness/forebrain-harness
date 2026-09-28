// Run input, cancellation, and post-turn persistence.
package gateway

import (
	"context"
	"encoding/json"
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
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
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
		item, preview, accepted := q.PopLatest()
		out := toPendingInputPreview(preview)
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
// What the user steered or queued that the run never took is handed back as a
// durable event on the conversation, published before whatever the caller
// then reports about the run's end, so the client that sent those messages
// sends them next or takes them back: none is dropped with the run.
func (s *Server) finishRun(ctx context.Context, sessionID, runID string) {
	if released := s.runController().Release(runID); len(released) > 0 {
		inputs := make([]event.ReleasedInput, 0, len(released))
		for _, in := range released {
			inputs = append(inputs, event.ReleasedInput{Text: in.Text, Attachments: in.Attachments, MentionImages: in.MentionImages})
		}
		if err := s.publishGatewayRunEvent(ctx, sessionID, runID, event.RunEventQueuedInputReleased, event.QueuedInputReleasedPayload{Inputs: inputs}); err != nil {
			slog.Error("release queued input", "run_id", runID, "session_id", sessionID, "err", err)
		}
	}
	s.runController().Finish(runID)
	s.runStartedAt.Delete(runID)
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
		s.finishRun(ctx, sessionID, runID)
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
	SessionID string
	ChannelID string
	RunID     string
	UserText  string
	// RawInput is what the user actually typed when it differs from UserText
	// (a slash command that expanded into a longer prompt). It becomes the
	// transcript row's display content, so a resume replay shows the command
	// rather than its expansion -- the same rule the terminal applies.
	RawInput        string
	AssistantText   string
	RunStartedAt    time.Time
	RunFinishedAt   time.Time
	WorkedMs        int64
	AssistantResult *agent.Result

	AppendUser      bool
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
	if opt.AppendUser || opt.AppendAssistant {
		s.appendTranscriptTurns(ctx, sid, opt)
	}
}

func (s *Server) appendTranscriptTurns(ctx context.Context, sessionID string, opt gatewayPostTurnOptions) {
	if s == nil || s.Sessions == nil {
		return
	}
	sid := normalizedGatewaySessionID(sessionID)
	_ = s.Sessions.Ensure(ctx, sid, sid)
	if opt.AppendUser {
		turn.PersistUserTurn(ctx, s.Sessions, turn.UserTurn{
			SessionID:  sid,
			ModelInput: opt.UserText,
			RawInput:   opt.RawInput,
		})
	}
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
			Text:       opt.AssistantText,
			Model:      model,
			StartedAt:  opt.RunStartedAt,
			FinishedAt: opt.RunFinishedAt,
			WorkedMs:   opt.WorkedMs,
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
func (s *Server) persistCancelledGatewayTurn(sessionID string, capture *run.PartialSessionCapture, partial *turn.StreamPartial) {
	if s == nil || s.Sessions == nil {
		return
	}
	var captured []llm.Message
	if capture != nil {
		captured = capture.Snapshot()
	}
	turn.PersistCancelledTurn(context.Background(), s.Sessions, turn.CancelledTurn{
		SessionID:        sessionID,
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
