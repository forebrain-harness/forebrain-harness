// Session lifecycle, its store, and input history.
package turn

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

type InputHistoryReader interface {
	ListUserInputHistory(ctx context.Context, sessionID string, limit int) ([]string, error)
}

func (s *Service) ListUserInputHistory(ctx context.Context, sessionID string, limit int) ([]string, error) {
	if s == nil || s.inputHistoryReader == nil {
		return []string{}, nil
	}
	return s.inputHistoryReader.ListUserInputHistory(ctx, sessionID, limit)
}

type SessionSummary = state.SessionSummary

type SessionCreateResult struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// CreateSession opens a new conversation. A title is a name the caller chose;
// without one the session is born unnamed — titled with its own id, as a
// terminal session is — so its first user message names it. A placeholder
// such as "New chat" is the surface's to draw for an unnamed session, never a
// name to store: a stored one would outrank the first message forever.
func (s *Service) CreateSession(ctx context.Context, title string) (SessionCreateResult, error) {
	title = strings.TrimSpace(title)
	if s == nil || s.sessionStore == nil {
		return SessionCreateResult{Title: title}, nil
	}
	source := strings.TrimSpace(s.sessionSource)
	if source == "" {
		source = "web"
	}
	id := strings.TrimSpace(state.NewID(source))
	if id == "" {
		return SessionCreateResult{}, fmt.Errorf("turn: empty generated session id")
	}
	stored := title
	if stored == "" {
		stored = id
	}
	if err := s.sessionStore.Ensure(ctx, id, stored); err != nil {
		return SessionCreateResult{}, err
	}
	return SessionCreateResult{ID: id, Title: title}, nil
}

func (s *Service) RenameSession(ctx context.Context, sessionID, title string) error {
	sessionID = strings.TrimSpace(sessionID)
	title = strings.TrimSpace(title)
	if sessionID == "" {
		return nil
	}
	if title == "" {
		title = sessionID
	}
	if s == nil || s.sessionStore == nil {
		return nil
	}
	return s.sessionStore.SetTitle(ctx, sessionID, title)
}

func (s *Service) ListSessionsRecent(ctx context.Context, limit int) ([]SessionSummary, error) {
	if s == nil || s.sessionStore == nil {
		return []SessionSummary{}, nil
	}
	return s.sessionStore.ListSessionsRecent(ctx, limit)
}

func (s *Service) ListSessionMessages(ctx context.Context, sessionID string, limit int) ([]state.Message, error) {
	if s == nil || s.sessionStore == nil {
		return []state.Message{}, nil
	}
	return s.sessionStore.ListRecentMessages(ctx, sessionID, limit)
}

// CancelledTurnStore is the slice of the session store a cancelled turn writes
// through.
type CancelledTurnStore interface {
	AppendMessageSequence(ctx context.Context, sessionID string, msgs []llm.Message, model, usageJSON string) error
	Append(ctx context.Context, sessionID, role, content string) (int64, error)
	RepairDanglingToolResults(ctx context.Context, sessionID string) (int, error)
}

// CancelledTurn is what a surface hands over when its turn was cancelled.
type CancelledTurn struct {
	SessionID string
	// Captured is the orchestration's partial session: the assistant tool_calls
	// and tool results that completed before the cancellation.
	Captured []llm.Message
	// PartialText is assistant text a streaming surface received but never saw
	// finish. Empty on a surface that does not stream into a buffer of its own.
	PartialText string
	// PartialReasoning is stored as its own reasoning row, as on a normal turn.
	PartialReasoning string
	Model            string
	StartedAt        time.Time
	FinishedAt       time.Time
	// OnRepairError reports a failed RepairDanglingToolResults call. The repair
	// itself stays best-effort -- a failure here must not abort the cancel
	// path, which is why the call site below discards the error rather than
	// returning it -- but a surface still needs the chance to log it: a repair
	// failure here means the next turn's send to the provider is likely to
	// fail on an unanswered tool_call, with nothing else pointing at why. Nil
	// ignores the error.
	OnRepairError func(error)
}

// PersistCancelledTurn writes what a cancelled turn had already produced, so a
// transient cancel does not lose work the user watched happen, and then repairs
// tool_calls left dangling by a tool interrupted mid-execution.
//
// Both surfaces need this and their copies had drifted: only the terminal
// de-duplicated its streaming buffer against the captured session, so a gateway
// cancel could store the same assistant text twice.
func PersistCancelledTurn(ctx context.Context, store CancelledTurnStore, in CancelledTurn) {
	if store == nil {
		return
	}
	toPersist := append([]llm.Message(nil), in.Captured...)

	// The streaming buffer normally holds only the interrupted call's text: the
	// orchestration loop clears it each time a finished response enters the
	// session, where the capture takes over. An execution chain without that
	// loop never signals the boundary, so the buffer still spans the whole turn
	// and re-persisting it would duplicate every assistant message the capture
	// already carries. Catch exactly that case.
	//
	// The comparison is equality rather than a prefix or substring trim on
	// purpose. A partial match is not evidence of duplication -- a short
	// interrupted fragment can legitimately repeat text from earlier in the
	// same turn -- and trimming one would silently discard content the user saw.
	partialText := strings.TrimSpace(in.PartialText)
	if partialText != "" && partialText == CapturedAssistantTextForTurn(toPersist) {
		partialText = ""
	}
	// The calls the cancellation interrupted are answered inside the same
	// sequence, immediately behind the batch that opened them: a tool result has
	// to follow its own assistant message, and any partial text below would
	// otherwise come between them.
	toPersist = append(toPersist, CancelledToolAnswers(toPersist)...)
	if partialText != "" {
		toPersist = append(toPersist, llm.AssistantMessage([]llm.ContentPart{llm.Text(partialText)}))
	}
	if len(toPersist) > 0 {
		_ = store.AppendMessageSequence(
			ctx, in.SessionID, toPersist,
			in.Model,
			"", // usage is unknown at cancel time; the store tolerates it
		)
	}
	if reasoning := strings.TrimSpace(in.PartialReasoning); reasoning != "" {
		_, _ = store.Append(ctx, in.SessionID, "reasoning", reasoning)
	}
	// Best effort: a repair failure must not abort the cancel path.
	if _, err := store.RepairDanglingToolResults(ctx, in.SessionID); err != nil && in.OnRepairError != nil {
		in.OnRepairError(err)
	}
}

// CapturedAssistantTextForTurn concatenates the assistant text captured since
// the last real user message, which is the unit a streaming buffer would
// duplicate.
func CapturedAssistantTextForTurn(snapshot []llm.Message) string {
	lastUserIdx := -1
	for i := len(snapshot) - 1; i >= 0; i-- {
		if snapshot[i].Role == llm.RoleUser && !snapshot[i].IsMeta {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx < 0 {
		return ""
	}
	var b strings.Builder
	for i := lastUserIdx + 1; i < len(snapshot); i++ {
		if snapshot[i].Role == llm.RoleAssistant {
			b.WriteString(llm.TextContent(snapshot[i].Parts...))
		}
	}
	return strings.TrimSpace(b.String())
}

// UserTurnStore is the slice of the session store a user message is written
// through.
type UserTurnStore interface {
	Ensure(ctx context.Context, id, title string) error
	AppendStructuredMessage(ctx context.Context, sessionID, role, content, messageID, partsJSON, model, usageJSON, toolStepID, toolMetaJSON string, exec state.MessageExecTiming) (int64, error)
}

// UserTurn is the user message a turn persists.
type UserTurn struct {
	SessionID string
	// ModelInput is the prompt actually fed to the model. For a slash command
	// this is the expanded prompt, not what the user typed.
	ModelInput string
	// RawInput is what the user actually typed, when it differs from
	// ModelInput. Empty means the two are the same.
	RawInput string
	// Parts is the expanded multimodal input, used to render PartsJSON when the
	// surface has not rendered one itself.
	Parts []llm.ContentPart
	// PartsJSON is the surface's own rendering, which wins when set: it is the
	// one that carries attachment references.
	PartsJSON string
	// EnsureSession creates the session row first. A surface that has already
	// ensured it can leave this false.
	EnsureSession bool
}

// PersistUserTurn writes the user message for a turn.
//
// The row's content field is display-only: the model context is rebuilt from
// PartsJSON, which is preferred over content when parsing. So content carries
// what the user actually typed and PartsJSON carries the expanded prompt --
// that way a resume replay shows the command the user entered rather than the
// prompt it expanded into. The terminal did this and the gateway stored the
// expanded text in both, which is what this unifies.
func PersistUserTurn(ctx context.Context, store UserTurnStore, in UserTurn) (int64, error) {
	if store == nil {
		return 0, nil
	}
	modelInput := in.ModelInput
	if strings.TrimSpace(modelInput) == "" {
		return 0, nil
	}
	sid := strings.TrimSpace(in.SessionID)
	if sid == "" {
		sid = "default"
	}
	if in.EnsureSession {
		if err := store.Ensure(ctx, sid, sid); err != nil {
			return 0, err
		}
	}
	displayContent := modelInput
	if raw := strings.TrimSpace(in.RawInput); raw != "" {
		displayContent = raw
	}
	partsJSON := strings.TrimSpace(in.PartsJSON)
	if partsJSON == "" {
		msg := llm.UserMessage(llm.Text(modelInput))
		if len(in.Parts) > 0 {
			msg = llm.UserMessage(in.Parts...)
		}
		partsJSON = state.MessagePartsJSON(msg, modelInput)
	}
	return store.AppendStructuredMessage(
		ctx, sid, "user", displayContent, "", partsJSON, "", "", "", "", state.MessageExecTiming{},
	)
}

// AssistantTurnStore is the slice of the session store a finished turn's
// assistant output is written through.
type AssistantTurnStore interface {
	Append(ctx context.Context, sessionID, role, content string) (int64, error)
	AppendMessageSequence(ctx context.Context, sessionID string, msgs []llm.Message, model, usageJSON string) error
	AppendStructuredMessage(ctx context.Context, sessionID, role, content, messageID, partsJSON, model, usageJSON, toolStepID, toolMetaJSON string, exec state.MessageExecTiming) (int64, error)
}

// AssistantTurn is the assistant output a finished turn persists.
type AssistantTurn struct {
	SessionID string
	// RunID durably binds newly written display rows to this execution. It is
	// optional for compatibility helpers, but live TUI/gateway turns always set
	// it so event replay can attach subagent cards without timestamp guesses.
	RunID  string
	Result *agent.Result
	// Text overrides the text resolved from Result. A surface that has already
	// sanitised its outbound copy passes that, so the transcript records what
	// the user was actually shown.
	Text  string
	Model string
	// StartedAt and FinishedAt are the surface's clock for the run — the
	// same window its "Worked for" line reported live.
	StartedAt  time.Time
	FinishedAt time.Time
	WorkedMs   int64
	// OnSequenceError reports a failed session append. It matters because the
	// run itself has already been marked done by the time this runs, so a
	// dropped append leaves the transcript missing content the user was told
	// they got, with nothing else observing it. Logging does not recover the
	// message, but silence is worse. Nil ignores the error.
	OnSequenceError func(error)
}

// PersistAssistantTurn writes a finished turn's assistant output: the reasoning
// row, then either the accumulated session or a single structured message.
//
// The two surfaces had this twice and it drifted -- only the terminal dropped a
// reasoning-only echo. Sharing it is also what lets a cross-surface contract
// test compare complete transcripts rather than only the rows the executor
// happens to write.
func PersistAssistantTurn(ctx context.Context, store AssistantTurnStore, in AssistantTurn) {
	if store == nil {
		return
	}
	resolved, reasoning := AssistantOutcomeText(in.Result)
	text := strings.TrimSpace(in.Text)
	if text == "" {
		text = strings.TrimSpace(resolved)
	} else if in.Result != nil && strings.TrimSpace(resolved) == "" {
		// The resolver judged this a reasoning-only echo, so the surface's copy
		// of it is not an answer either.
		text = ""
	}
	if reasoning != "" {
		_, _ = store.Append(ctx, in.SessionID, "reasoning", reasoning)
	}
	if in.Result != nil && len(in.Result.Session) > 0 {
		var err error
		if runStore, ok := store.(interface {
			AppendMessageSequenceForRun(context.Context, string, string, []llm.Message, string, string, state.RunTiming) error
		}); ok && strings.TrimSpace(in.RunID) != "" {
			err = runStore.AppendMessageSequenceForRun(
				ctx, in.SessionID, in.RunID, in.Result.Session,
				in.Model, state.MarshalTokenUsage(in.Result.LastResponseUsageCopy()),
				state.RunTiming{StartedAt: in.StartedAt, FinishedAt: in.FinishedAt, Worked: time.Duration(in.WorkedMs) * time.Millisecond},
			)
		} else {
			err = store.AppendMessageSequence(
				ctx, in.SessionID, in.Result.Session,
				in.Model, state.MarshalTokenUsage(in.Result.LastResponseUsageCopy()),
			)
		}
		if err != nil && in.OnSequenceError != nil {
			in.OnSequenceError(err)
		}
		return
	}
	if text == "" {
		return
	}
	assistant := llm.AssistantMessage(in.Result.PartsCopy())
	if in.Result != nil {
		assistant.MemoryCitation = in.Result.MemoryCitation
	}
	if runStore, ok := store.(interface {
		AppendStructuredMessageForRun(context.Context, string, string, string, string, string, string, string, string, string, state.MessageExecTiming) (int64, error)
	}); ok && strings.TrimSpace(in.RunID) != "" {
		_, _ = runStore.AppendStructuredMessageForRun(
			ctx, in.SessionID, in.RunID, "assistant", text, "",
			state.MessagePartsJSON(assistant, text),
			in.Model,
			state.MarshalTokenUsage(in.Result.LastResponseUsageCopy()),
			"", state.MessageExecTiming{},
		)
		return
	}
	_, _ = store.AppendStructuredMessage(
		ctx, in.SessionID, "assistant", text, "", state.MessagePartsJSON(assistant, text),
		in.Model, state.MarshalTokenUsage(in.Result.LastResponseUsageCopy()),
		"", "", state.MessageExecTiming{},
	)
}

// CancelledToolCallNote is what a call that never ran reports back to the model.
// It is the tool's own result, so it is written in the voice every other tool
// result uses: what happened to this call, in one sentence.
const CancelledToolCallNote = "The user canceled this call before it ran."

// cancelledToolCallStatus is the status a card carries once its call is known
// never to have run. It is the same one-l spelling Renderer.FinalizePendingTools
// stamps on a live pending card, so a replayed card and a live one agree.
const cancelledToolCallStatus = "canceled"

// AbandonedToolCallStore is the transcript half of closing out a stopped run:
// read what the run left behind, write the answers it never produced.
type AbandonedToolCallStore interface {
	ListTranscriptMessages(ctx context.Context, sessionID string, limit int) ([]llm.Message, error)
	AppendNewMessages(ctx context.Context, sessionID, runID string, messages []llm.Message, model string, usageJSON string) error
}

// AnswerAbandonedToolCalls records that the calls a stopped run left open were
// cancelled, by writing the tool result each one is missing. It reports how many
// it wrote.
//
// A run that ends at a cancelled approval, or under an interrupt, leaves an
// assistant row carrying tool_calls that nothing answers. Providers reject that
// transcript, and the repair that made it legal did so by deleting the calls
// from the stored row - which is the same row a resume replays, so the card the
// user watched turn "canceled" vanished from their history as soon as they sent
// one more message. Answering the call instead keeps both halves honest: the
// transcript is legal, the card survives with the status it ended on, and the
// model is told the call was cancelled rather than being left to believe it
// never made one.
//
// Only the transcript's trailing batch is answered, because that is the only one
// an append can answer: a tool result has to follow the assistant message that
// opened it and row order is insertion order. That is also the only batch a
// stopped run can leave open, since this runs at the moment the run stops.
func AnswerAbandonedToolCalls(ctx context.Context, store AbandonedToolCallStore, sessionID, model string) int {
	if store == nil || strings.TrimSpace(sessionID) == "" {
		return 0
	}
	msgs, err := store.ListTranscriptMessages(ctx, strings.TrimSpace(sessionID), 5000)
	if err != nil {
		return 0
	}
	answers := CancelledToolAnswers(msgs)
	if len(answers) == 0 {
		return 0
	}
	if err := store.AppendNewMessages(ctx, strings.TrimSpace(sessionID), "", answers, model, ""); err != nil {
		return 0
	}
	return len(answers)
}

// CancelledToolAnswers returns the tool results the trailing assistant batch in
// msgs is missing, in the order the calls were issued. It is empty unless msgs
// ends with an assistant tool_calls row and the tool rows that answer part of
// it, which is the exact shape a stopped run leaves behind.
func CancelledToolAnswers(msgs []llm.Message) []llm.Message {
	answered := make(map[string]struct{}, 4)
	batch := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		role := strings.TrimSpace(msgs[i].Role)
		if strings.EqualFold(role, llm.RoleTool) {
			if id := strings.TrimSpace(msgs[i].ToolCallID); id != "" {
				answered[id] = struct{}{}
			}
			continue
		}
		if strings.EqualFold(role, llm.RoleAssistant) && len(msgs[i].ToolCalls) > 0 {
			batch = i
		}
		break
	}
	if batch < 0 {
		return nil
	}
	out := make([]llm.Message, 0, len(msgs[batch].ToolCalls))
	for _, call := range msgs[batch].ToolCalls {
		id := strings.TrimSpace(call.ID)
		if id == "" {
			continue
		}
		if _, ok := answered[id]; ok {
			continue
		}
		answered[id] = struct{}{}
		out = append(out, cancelledToolAnswer(call))
	}
	return out
}

// cancelledToolAnswer builds the result for one call that never ran.
//
// The model-facing content says what happened; the display half deliberately
// carries no body, because the card the user was looking at had none either -
// it was built when the call was dispatched and only its status changed. That
// is also why the summary comes from the started phase: a card reading "ran ls"
// under a canceled badge would claim the call went through.
func cancelledToolAnswer(call llm.ToolCall) llm.Message {
	name := strings.TrimSpace(call.Function.Name)
	var input map[string]any
	if raw := strings.TrimSpace(call.Function.Arguments); raw != "" {
		_ = json.Unmarshal([]byte(raw), &input)
	}
	step := tool.StepEvent{
		Kind:     tool.StepKindToolStarted,
		StepID:   strings.TrimSpace(call.ID),
		ToolName: name,
		Input:    input,
	}
	meta := tool.BuildToolMeta(step)
	meta.Status = cancelledToolCallStatus
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		metaJSON = nil
	}
	msg := llm.ToolResultMessage(strings.TrimSpace(call.ID), llm.Text(CancelledToolCallNote))
	msg.ToolDisplay = &llm.ToolDisplayState{
		Summary:      strings.TrimSpace(tool.SummarizeToolStep(step)),
		ToolMetaJSON: string(metaJSON),
	}
	return msg
}

// StreamPartial collects what a surface was streamed of a turn's answer and
// reasoning, so a cancelled turn can keep the partial answer the user already
// saw (CancelledTurn.PartialText). Every surface that streams a turn keeps one.
type StreamPartial struct {
	mu        sync.Mutex
	content   strings.Builder
	reasoning strings.Builder
}

func (p *StreamPartial) AppendContent(text string) {
	if p == nil || text == "" {
		return
	}
	p.mu.Lock()
	p.content.WriteString(text)
	p.mu.Unlock()
}

func (p *StreamPartial) AppendReasoning(text string) {
	if p == nil || text == "" {
		return
	}
	p.mu.Lock()
	p.reasoning.WriteString(text)
	p.mu.Unlock()
}

// ResponseCompleted drops the answer text collected so far: a finished
// response has entered the session, where the run's partial session capture
// owns it. Reasoning is kept: a completed response's reasoning reaches the
// transcript only through the reasoning row the cancel path writes.
func (p *StreamPartial) ResponseCompleted() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.content.Reset()
	p.mu.Unlock()
}

func (p *StreamPartial) Content() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.content.String()
}

func (p *StreamPartial) Reasoning() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reasoning.String()
}
