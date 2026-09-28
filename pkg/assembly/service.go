package assembly

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/google/uuid"
)

const (
	ErrorMessageNotEnoughMessages = "not enough messages to compact"
	localUserHistoryTokenBudget   = 20_000
	// localCompactMaxAttempts bounds the halving retry loop. Each attempt halves
	// the budget, so this covers a ~1000x reduction.
	localCompactMaxAttempts = 10
	// summaryInputFraction leaves headroom for provider-side prompt scaffolding
	// the local estimator cannot see.
	summaryInputFraction = 0.85
)

const DefaultPrompt = `You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume the task.

Include:
- Current progress and key decisions made
- Important context, constraints, or user preferences
- What remains to be done (clear next steps)
- Any critical data, examples, or references needed to continue

Be concise, structured, and focused on helping the next LLM seamlessly continue the work.`

// SummaryPrefix re-exports state.CompactSummaryPrefix so the two layers can
// never drift: the compact writer and the session persistence/projection
// readers must agree on exactly what marks a handoff summary.
const SummaryPrefix = state.CompactSummaryPrefix

const WarningMessage = "Every compaction drops some detail, so a fresh conversation (/new) keeps long work accurate."

type Service struct {
	Sessions *state.SessionStore
	// PrimaryModel names the model the conversation being compacted runs on.
	// It takes the calling context because the answer is per conversation: a
	// shared runner may serve sessions on different models.
	PrimaryModel       func(ctx context.Context) (provider string, model string)
	CompactLLM         func(ctx context.Context) llm.LLM
	ModelProvider      func(model string) string
	CompactLLMForModel func(model string) llm.LLM
	Prompt             string
	// RemoteCompaction opts in to provider-native compaction (default: false).
	// Remote checkpoints store provider-specific opaque items that cannot be
	// replayed by a different provider; local summarization is the safe default.
	RemoteCompaction bool
	RemoteV1         bool
	ExplicitLimit    int
	LimitScope       string
	PreCompact       func(context.Context, string, string) error
	PostCompact      func(context.Context, string, string) error
	// Events receives every compaction's lifecycle: started, progress, and
	// how it ended. It is the surface's own run-event sink.
	Events event.Sink
	// ConversationSummary returns the summarizer for a stored conversation,
	// or nil when the conversation's own requests cannot be reproduced. See
	// ConversationSummarizer.
	ConversationSummary func(sessionID string) ConversationSummarizer
}

// ConversationSummarizer sends a compaction's summary request as the
// conversation's next request — the same system prompt, injected context,
// history and tools the conversation's own requests carry, sent through the
// same chain — with instruction as its final message. The provider then finds
// the conversation's cached prompt prefix and bills the summary request only
// for the instruction and the summary, instead of reading the whole history
// again at full price.
type ConversationSummarizer func(ctx context.Context, instruction llm.Message) (*llm.Result, error)

type Result struct {
	SessionID     string
	Trigger       string
	Strategy      string
	Reason        string
	SummarySource string
	Scope         string
	Summary       string
	BoundaryID    string
	ReplacedTurns int
	WindowNumber  int
	TokensBefore  int
	TokensAfter   int
	// Reactive marks a compaction forced by the provider rejecting a request
	// as too large, as opposed to one the threshold scheduled.
	Reactive bool
	Duration time.Duration
}

type autoCompactDecision struct {
	Should        bool
	Usage         int
	Reason        string
	PreviousModel string
}

func (s Service) ManualCompactSession(ctx context.Context, sessionID, trigger string) (Result, error) {
	if s.Sessions == nil {
		return Result{}, errors.New("nil session store")
	}
	sid := normalizedSessionID(sessionID)
	trigger = normalizedTrigger(trigger)
	messages, err := s.Sessions.ListTranscriptMessages(ctx, sid, 5000)
	if err != nil {
		return Result{}, err
	}
	ctx, run := s.beginCompaction(ctx, sid, trigger, llm.EstimateMessages(messages))
	res, err := s.compactSession(ctx, sid, messages, trigger, "", "")
	if err != nil {
		run.fail(err)
		return Result{}, err
	}
	return run.finish(res), nil
}

func (s Service) AutoCompactSession(ctx context.Context, sessionID, _ string) (Result, bool, error) {
	decision, err := s.autoCompactDecision(ctx, sessionID)
	if err != nil || !decision.Should {
		return Result{}, false, err
	}
	sid := normalizedSessionID(sessionID)
	messages, err := s.Sessions.ListTranscriptMessages(ctx, sid, 5000)
	if err != nil {
		return Result{}, false, err
	}
	ctx, run := s.beginCompaction(ctx, sid, "auto", llm.EstimateMessages(messages))
	res, err := s.compactSession(ctx, sid, messages, "auto", decision.Reason, decision.PreviousModel)
	if err != nil {
		run.fail(err)
		return Result{}, false, err
	}
	return run.finish(res), true, nil
}

// compactSession replaces a stored session's history with one checkpoint. The
// previous model, when set, summarizes first: the history was written for it.
func (s Service) compactSession(ctx context.Context, sid string, messages []llm.Message, trigger, reason, previousModel string) (Result, error) {
	if len(messages) == 0 {
		return Result{}, errors.New(ErrorMessageNotEnoughMessages)
	}
	if s.PreCompact != nil {
		if err := s.PreCompact(ctx, sid, trigger); err != nil {
			return Result{}, err
		}
	}
	var previousClient llm.LLM
	if previousModel != "" && s.CompactLLMForModel != nil {
		previousClient = s.CompactLLMForModel(previousModel)
	}
	// The conversation's own requests are sent to the current model; a
	// compaction the previous model summarizes is a different request.
	var conversation ConversationSummarizer
	if s.ConversationSummary != nil {
		conversation = s.ConversationSummary(sid)
	}
	summarizeAsConversation := conversation
	if previousClient != nil {
		summarizeAsConversation = nil
	}
	replacement, res, err := s.compactMessages(ctx, messages, nil, sid, trigger, reason, previousClient, summarizeAsConversation)
	if err != nil && previousClient != nil {
		if _, remote := previousClient.(llm.ContextCompactor); remote && shouldRetryWithCurrentModel(err) {
			replacement, res, err = s.compactMessages(ctx, messages, nil, sid, trigger, reason, nil, conversation)
		}
	}
	if err != nil {
		return Result{}, err
	}
	if res, err = s.persistCheckpoint(ctx, sid, replacement, res); err != nil {
		return Result{}, err
	}
	s.runPostCompact(ctx, sid, trigger)
	return res, nil
}

// runPostCompact notifies the PostCompact hook of a checkpoint that is already
// committed. The hook cannot undo it: reporting its failure as a failed
// compaction would tell the caller the history is unchanged when it was
// replaced — and a mid-turn caller that believed that would keep growing the
// uncompacted slice and persist the compacted prefix after the new boundary.
func (s Service) runPostCompact(ctx context.Context, sid, trigger string) {
	if s.PostCompact == nil {
		return
	}
	if err := s.PostCompact(ctx, sid, trigger); err != nil {
		slog.Warn("PostCompact hook failed after the checkpoint was committed", "session_id", sid, "trigger", trigger, "error", err)
	}
}

// ShouldAutoCompactSession deliberately ignores pending input.
// The explicit threshold is clamped to 90% of the window.
func (s Service) ShouldAutoCompactSession(ctx context.Context, sessionID, _ string) (bool, int, error) {
	decision, err := s.autoCompactDecision(ctx, sessionID)
	return decision.Should, decision.Usage, err
}

func (s Service) autoCompactDecision(ctx context.Context, sessionID string) (autoCompactDecision, error) {
	if s.Sessions == nil {
		return autoCompactDecision{}, nil
	}
	sid := normalizedSessionID(sessionID)
	turns, err := s.Sessions.ListRecentMessages(ctx, sid, 5000)
	if err != nil {
		return autoCompactDecision{}, err
	}
	usage := state.TokenCountWithEstimation(turns)
	provider, model := "", ""
	if s.PrimaryModel != nil {
		provider, model = s.PrimaryModel(ctx)
	}
	limits, _ := llm.Lookup(provider, model)
	budget := state.CalculateTokenBudgetWithOptions(usage, model, limits, state.TokenBudgetOptions{ExplicitLimit: s.ExplicitLimit})
	previousModel := lastAssistantModel(turns)
	if previousModel != "" {
		previousProvider := ""
		if s.ModelProvider != nil {
			previousProvider = s.ModelProvider(previousModel)
		}
		modelChanged := !strings.EqualFold(previousModel, model)
		providerChanged := previousProvider != "" && provider != "" && !strings.EqualFold(previousProvider, provider)
		if modelChanged || providerChanged {
			previousLimits, previousKnown := llm.Lookup(previousProvider, previousModel)
			currentLimits, currentKnown := llm.Lookup(provider, model)
			if previousKnown && currentKnown {
				if reason := modelChangeCompactReason(usage, s.LimitScope, budget.AutoCompactThreshold, previousLimits, currentLimits); reason != "" {
					return autoCompactDecision{Should: true, Usage: usage, Reason: reason, PreviousModel: previousModel}, nil
				}
			}
			// Provider/model switch without compaction. The new provider's
			// cache has never seen this session's prefix, so the next request
			// is a guaranteed full miss of `usage` tokens. This is the
			// conservative posture: compaction is NOT forced (a checkpoint
			// shrinks Σprompt, which is cost-positive but hit-rate-neutral at
			// best); the switch is declared to the prefix tracker and called
			// out loudly enough that the cost is visible instead of silent.
			llm.RecordPrefixGeneration(sid, "provider_switch", usage, previousProvider+"/"+previousModel+"->"+provider+"/"+model)
			slog.Warn("model switch starts a new prompt-cache generation: the new provider holds none of this session's cached prefix",
				"session_id", sid, "previous_model", previousModel, "model", model, "prompt_tokens", usage)
		}
	}
	threshold := budget.AutoCompactThreshold
	if usage >= budget.EffectiveContextWindow {
		return autoCompactDecision{Should: true, Usage: usage, Reason: "model_context_window_pressure"}, nil
	}
	thresholdUsage := usage
	if normalizedScope(s.LimitScope) == "body_after_prefix" {
		estimatedPrefill := usage
		if _, checkpoint, checkpointErr := s.Sessions.LatestCompactBoundary(ctx, sid); checkpointErr != nil {
			return autoCompactDecision{}, checkpointErr
		} else if len(checkpoint.ReplacementHistory) > 0 {
			estimatedPrefill = llm.EstimateMessages(checkpoint.ReplacementHistory)
		}
		thresholdUsage = state.BodyAfterPrefixTokenCount(turns, estimatedPrefill)
	}
	return autoCompactDecision{Should: thresholdUsage >= threshold, Usage: usage, Reason: "model_context_window_pressure"}, nil
}

func modelChangeCompactReason(usage int, scope string, currentAutoLimit int, previous, current llm.Hit) string {
	if previous.CompHash != "" && current.CompHash != "" && previous.CompHash != current.CompHash {
		return "comp_hash_changed"
	}
	previousLimit := previous.Limits().EffectiveInputLimit()
	currentLimit := current.Limits().EffectiveInputLimit()
	if previousLimit <= currentLimit {
		return ""
	}
	if usage >= currentLimit {
		return "model_downshift"
	}
	if normalizedScope(scope) == "total" && usage > currentAutoLimit {
		return "model_downshift"
	}
	return ""
}

// TryCompactOnMessages checkpoints a live agent's history mid-turn and returns
// the history the next request carries. Reactive marks a compaction forced by
// the provider rejecting the request as too large. Conversation, when set,
// sends the summary request as the request the agent was about to make.
func (s Service) TryCompactOnMessages(ctx context.Context, messages []llm.Message, sessionID string, reactive bool, conversation ConversationSummarizer) ([]llm.Message, bool, error) {
	if s.Sessions == nil || s.CompactLLM == nil || strings.TrimSpace(sessionID) == "" || len(messages) == 0 {
		return nil, false, nil
	}
	ctx, run := s.beginCompaction(ctx, sessionID, "auto", llm.EstimateMessages(messages))
	replacement, result, err := s.compactLiveMessages(ctx, messages, sessionID, reactive, conversation)
	if err != nil {
		run.fail(err)
		return nil, false, err
	}
	run.finish(result)
	// The checkpoint excludes initial instructions. Reinject current initial
	// context for the request that immediately follows a mid-turn compaction.
	return injectInitialContext(messages, replacement), true, nil
}

func (s Service) compactLiveMessages(ctx context.Context, messages []llm.Message, sessionID string, reactive bool, conversation ConversationSummarizer) ([]llm.Message, Result, error) {
	if s.PreCompact != nil {
		if err := s.PreCompact(ctx, sessionID, "auto"); err != nil {
			return nil, Result{}, err
		}
	}
	reason := ""
	if reactive {
		reason = "provider reported that the context window was exceeded"
		// The request the agent was about to make is the one the provider
		// just refused as too large; it cannot carry the summary either.
		conversation = nil
	}
	replacement, result, err := s.compactMessages(ctx, messages, nil, sessionID, "auto", reason, nil, conversation)
	if err != nil {
		return nil, Result{}, err
	}
	result.Reactive = reactive
	if result, err = s.persistCheckpoint(ctx, sessionID, replacement, result); err != nil {
		return nil, Result{}, err
	}
	s.runPostCompact(ctx, sessionID, "auto")
	return replacement, result, nil
}

func (s Service) compactMessages(ctx context.Context, messages []llm.Message, tools []*llm.Tool, sessionID, trigger, reason string, client llm.LLM, conversation ConversationSummarizer) ([]llm.Message, Result, error) {
	if client == nil && s.CompactLLM != nil {
		client = s.CompactLLM(ctx)
	}
	if client == nil {
		return nil, Result{}, errors.New("compact: current model unavailable")
	}
	before := llm.EstimateMessages(messages)
	var replacement []llm.Message
	var summary, strategy, source string
	if remote, ok := client.(llm.ContextCompactor); ok && s.RemoteCompaction {
		mode := llm.CompactModeRemoteV1
		if !s.RemoteV1 {
			mode = llm.CompactModeRemoteV2
		}
		// Provider-native compaction streams nothing: the whole call is
		// the model reading the history, and it ends with the reply.
		stopReading := startReadingProgress(ctx, before)
		out, err := remote.Compact(ctx, messages, tools, mode)
		stopReading()
		if err == nil && (out == nil || len(out.Messages) == 0) {
			err = errors.New("compact: provider returned empty replacement history")
		}
		var remoteReplacement []llm.Message
		if err == nil {
			// The emptiness check above runs on the raw reply, but what gets
			// checkpointed is the reply minus initial context. A reply made up
			// entirely of system messages survives that check and then strips to
			// nothing, which would persist a checkpoint the model-context readers
			// reject — leaving the session to re-summarize its whole history on
			// every later round. Treat it as a remote failure so the local
			// summarizer still produces a usable checkpoint.
			remoteReplacement = stripInitialContext(out.Messages)
			if len(remoteReplacement) == 0 {
				err = errors.New("compact: provider returned no replacement history beyond initial context")
			}
		}
		if err != nil {
			if fallback, fallbackSummary, fallbackErr := s.localFallbackAfterRemoteFailure(ctx, client, messages, conversation, err); fallbackErr == nil {
				replacement, summary = fallback, fallbackSummary
				strategy, source = "local", "model_summary"
			} else {
				return nil, Result{}, fallbackErr
			}
		} else {
			// Provider-native compaction streams nothing to measure; its
			// summarizing phase completes when the reply lands.
			replacement = remoteReplacement
			strategy, source = string(mode), "remote_compaction"
			reportCompactProgress(ctx, summaryEndPercent, event.CompactPhaseSummarizing)
		}
	} else {
		var err error
		replacement, summary, err = s.localCompact(ctx, client, messages, conversation)
		if err != nil {
			return nil, Result{}, err
		}
		strategy, source = "local", "model_summary"
	}
	if strings.TrimSpace(reason) == "" {
		reason = "model_context_window_pressure"
	}
	if trigger == "manual" {
		reason = "user_requested"
	}
	result := Result{
		SessionID:     normalizedSessionID(sessionID),
		Trigger:       trigger,
		Strategy:      strategy,
		Reason:        reason,
		SummarySource: source,
		Scope:         normalizedScope(s.LimitScope),
		Summary:       summary,
		ReplacedTurns: max(0, len(stripInitialContext(messages))-len(replacement)),
		TokensBefore:  before,
		TokensAfter:   llm.EstimateMessages(replacement),
	}
	return replacement, result, nil
}

// summaryReplyInstruction closes every summary instruction. The conversation's
// own request carries its tools, and a summary is an answer, not a step.
const summaryReplyInstruction = "Reply with the summary itself as plain text. Do not call any tools."

type executeOnlyLLM struct{ inner llm.LLM }

func (w executeOnlyLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if w.inner == nil {
		return nil, errors.New("compact: current model unavailable")
	}
	return w.inner.Execute(ctx, messages, tools)
}

func (s Service) localFallbackAfterRemoteFailure(ctx context.Context, client llm.LLM, messages []llm.Message, conversation ConversationSummarizer, remoteErr error) ([]llm.Message, string, error) {
	if remoteErr == nil {
		return nil, "", errors.New("compact: remote compaction failed")
	}
	if errors.Is(remoteErr, context.Canceled) || errors.Is(remoteErr, context.DeadlineExceeded) {
		return nil, "", remoteErr
	}
	if !shouldRetryWithCurrentModel(remoteErr) {
		return nil, "", remoteErr
	}
	fallback, summary, localErr := s.localCompact(ctx, executeOnlyLLM{inner: client}, messages, conversation)
	if localErr == nil {
		return fallback, summary, nil
	}
	return nil, "", fmt.Errorf("compact: remote compaction failed: %v; local summary fallback failed: %w", remoteErr, localErr)
}

// localCompact summarizes messages with the given model.
//
// The history reaching this function is by definition too large for the model,
// so it is trimmed to a budget derived from the learned window before the first
// call rather than after a failed one. Each retry halves the budget, so the
// number of provider round-trips is logarithmic in the history size instead of
// linear in the message count.
//
// When the conversation's own request can carry the summary, it is asked for
// that way first: the history then fits, and the request reuses the
// conversation's cached prefix. The trimmed, stand-alone request below is for
// a history that does not fit, and for a model that answered the
// conversation's request with a tool call instead of the summary.
func (s Service) localCompact(ctx context.Context, model llm.LLM, messages []llm.Message, conversation ConversationSummarizer) ([]llm.Message, string, error) {
	prompt := strings.TrimSpace(s.Prompt)
	if prompt == "" {
		prompt = DefaultPrompt
	}
	promptMsg := llm.UserMessage(llm.Text(prompt + "\n\n" + summaryReplyInstruction))
	// budget <= 0 means the window is unknown, in which case the full history is
	// tried first and only shrunk in response to an actual rejection.
	budget := s.summaryInputBudget(ctx, messages)
	var lastErr error
	attempted := false
	// One observed context for every attempt, so a retry's output continues
	// the progress instead of restarting it.
	summaryCtx, stopReading := withSummaryProgress(ctx, llm.EstimateMessages(messages))
	defer stopReading()
	if conversation != nil && (budget <= 0 || llm.EstimateMessages(messages) <= budget) {
		res, err := conversation(summaryCtx, promptMsg)
		switch {
		case err == nil && res != nil && res.Message != nil && strings.TrimSpace(res.Message.TextContent()) != "":
			summary := strings.TrimSpace(res.Message.TextContent())
			retained := retainedRealUserMessages(messages, localUserHistoryTokenBudget)
			return append(retained, llm.UserMessage(llm.Text(SummaryPrefix+"\n"+summary))), summary, nil
		case err == nil:
			// Answered with a tool call, or with nothing: ask on its own below.
		case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
			return nil, "", err
		case isContextWindowError(err) || isRecoverableStructuralError(err):
			lastErr = err
		default:
			return nil, "", err
		}
	}
	for attempt := 0; attempt < localCompactMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		input := messages
		if budget > 0 {
			input = trimHistoryToFit(messages, budget)
		}
		if len(input) == 0 {
			// Nothing fits at this budget; keep halving rather than giving up, so
			// a single oversized message cannot strand the loop.
			budget /= 2
			if budget <= 0 {
				break
			}
			continue
		}
		attempted = true
		res, err := model.Execute(summaryCtx, append(input, promptMsg), nil)
		if err == nil {
			if res == nil || res.Message == nil || strings.TrimSpace(res.Message.TextContent()) == "" {
				return nil, "", errors.New("compact: model returned empty summary")
			}
			summary := strings.TrimSpace(res.Message.TextContent())
			retained := retainedRealUserMessages(messages, localUserHistoryTokenBudget)
			retained = append(retained, llm.UserMessage(llm.Text(SummaryPrefix+"\n"+summary)))
			return retained, summary, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, "", err
		}
		lastErr = err
		if !isContextWindowError(err) && !isRecoverableStructuralError(err) {
			return nil, "", err
		}
		if budget <= 0 {
			// First attempt used the full history because the window was unknown.
			// The rejection reveals its size, so start halving from there.
			budget = llm.EstimateMessages(input)
		}
		budget /= 2
	}
	// Every summarization attempt was rejected for size or shape. Rather than
	// surface the provider error and end the turn, fall back to a summary built
	// without the model: the user's own recent turns. This keeps the session
	// alive and preserves verbatim what the user actually asked for.
	if attempted && lastErr != nil {
		if retained := retainedRealUserMessages(messages, localUserHistoryTokenBudget); len(retained) > 0 {
			summary := digestWithoutModel(messages)
			out := append(retained, llm.UserMessage(llm.Text(SummaryPrefix+"\n"+summary)))
			return out, summary, nil
		}
	}
	if lastErr != nil {
		return nil, "", fmt.Errorf("compact: unable to fit history in context window: %w", lastErr)
	}
	return nil, "", errors.New("compact: unable to fit history in context window")
}

// summaryInputBudget returns the token budget for a summarization request,
// reserving room for the summary output. It prefers the learned window for the
// active model over the catalog value, and falls back to a fraction of the
// history when no model is known.
func (s Service) summaryInputBudget(ctx context.Context, messages []llm.Message) int {
	provider, model := "", ""
	if s.PrimaryModel != nil {
		provider, model = s.PrimaryModel(ctx)
	}
	limits, _ := llm.Lookup(provider, model)
	window := llm.EffectiveWindow(provider, model, limits.Limits().EffectiveInputLimit())
	if window <= 0 {
		// Unknown window. Returning 0 tells the caller to send the full history
		// first: guessing a budget here would trim a history that may well have
		// fit, losing context for no reason.
		return 0
	}
	reserve := limits.Limits().EffectiveOutputReserve()
	if reserve <= 0 || reserve > window/2 {
		reserve = window / 4
	}
	budget := int(float64(window-reserve) * summaryInputFraction)
	if budget <= 0 {
		return localUserHistoryTokenBudget
	}
	return budget
}

// digestWithoutModel builds a minimal handoff note when no model call can be
// completed. It states the situation plainly instead of fabricating progress
// the summarizer never got to read.
func digestWithoutModel(messages []llm.Message) string {
	return "The previous context exceeded the model's window and could not be " +
		"summarized by the model. The recent user turns above are preserved " +
		"verbatim; earlier assistant reasoning and tool output were dropped. " +
		"Re-read any files or re-run any searches you need instead of relying " +
		"on earlier results."
}

// isRecoverableStructuralError reports whether a provider rejection is a
// history-shape complaint that a smaller, re-trimmed history can resolve.
// Without this, a 400 from an orphaned tool result or a non-user opening turn
// aborts compaction and the raw provider error reaches the user.
func isRecoverableStructuralError(err error) bool {
	if err == nil {
		return false
	}
	return llm.IsStructuralRequestError(err)
}

func (s Service) persistCheckpoint(ctx context.Context, sessionID string, replacement []llm.Message, result Result) (Result, error) {
	if s.Sessions == nil {
		return Result{}, errors.New("nil session store")
	}
	// A boundary pointer must never reference an unusable checkpoint. The
	// model-context readers reject a checkpoint whose ReplacementHistory is
	// empty and fall back to the full transcript, which is indistinguishable
	// from "never compacted": the session re-reads its whole history and the
	// auto-compact check fires again on every later round. Validate here, at the
	// single place every compaction strategy funnels through, and before any row
	// is written — so a failure leaves no partial state behind.
	projected := cloneCompactMessages(stripInitialContext(replacement))
	if len(projected) == 0 {
		return Result{}, errors.New("compact: refusing to persist a checkpoint with no replacement history")
	}
	reportCompactProgress(ctx, savingPercent, event.CompactPhaseSaving)
	sid := normalizedSessionID(sessionID)
	_, previous, err := s.Sessions.LatestCompactBoundary(ctx, sid)
	if err != nil {
		return Result{}, err
	}
	windowID := uuid.Must(uuid.NewV7()).String()
	windowNumber, firstWindowID, previousWindowID := uint64(1), "", ""
	if previous.WindowNumber > 0 {
		windowNumber = previous.WindowNumber + 1
		firstWindowID = previous.FirstWindowID
		if firstWindowID == "" {
			firstWindowID = previous.WindowID
		}
		previousWindowID = previous.WindowID
	} else {
		initialWindowID, initialErr := s.Sessions.EnsureInitialWindowID(ctx, sid, uuid.Must(uuid.NewV7()).String())
		if initialErr != nil {
			return Result{}, initialErr
		}
		firstWindowID = initialWindowID
		previousWindowID = initialWindowID
	}
	text := strings.TrimSpace(result.Summary)
	if err := s.Sessions.AppendCompactCheckpoint(ctx, sid, text, state.CompactBoundaryPart{
		Trigger:            result.Trigger,
		Strategy:           result.Strategy,
		SummarySource:      result.SummarySource,
		ReplacementHistory: projected,
		WindowNumber:       windowNumber,
		FirstWindowID:      firstWindowID,
		PreviousWindowID:   previousWindowID,
		WindowID:           windowID,
	}); err != nil {
		return Result{}, err
	}
	result.BoundaryID = windowID
	result.WindowNumber = int(windowNumber)
	return result, nil
}

func retainedRealUserMessages(messages []llm.Message, budget int) []llm.Message {
	selected := make([]llm.Message, 0, 8)
	remaining := budget
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != llm.RoleUser || msg.IsMeta || strings.HasPrefix(strings.TrimSpace(msg.TextContent()), SummaryPrefix+"\n") {
			continue
		}
		if remaining <= 0 {
			break
		}
		text := msg.TextContent()
		cost := llm.EstimateText(text)
		if cost > remaining {
			selected = append(selected, llm.UserMessage(llm.Text(truncateTextToTokenBudget(text, remaining))))
			break
		}
		remaining -= cost
		selected = append(selected, llm.UserMessage(llm.Text(text)))
	}
	for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
		selected[i], selected[j] = selected[j], selected[i]
	}
	return selected
}

func injectInitialContext(original, replacement []llm.Message) []llm.Message {
	initial := make([]llm.Message, 0, 2)
	for _, msg := range original {
		if msg.Role != llm.RoleSystem {
			break
		}
		initial = append(initial, cloneMessage(msg))
	}
	if len(initial) == 0 {
		return cloneCompactMessages(replacement)
	}
	lastUser := -1
	for i := len(replacement) - 1; i >= 0; i-- {
		if replacement[i].AgentMessage != nil && !agentMessageIsCompletion(replacement[i].AgentMessage) {
			lastUser = i
			break
		}
		if replacement[i].Role == llm.RoleUser && !replacement[i].IsMeta {
			lastUser = i
			break
		}
	}
	if lastUser < 0 {
		return append(initial, cloneCompactMessages(replacement)...)
	}
	out := make([]llm.Message, 0, len(initial)+len(replacement))
	out = append(out, cloneCompactMessages(replacement[:lastUser])...)
	out = append(out, initial...)
	out = append(out, cloneCompactMessages(replacement[lastUser:])...)
	return out
}

func agentMessageIsCompletion(message *llm.AgentMessageState) bool {
	return message != nil && len(message.Content) > 0 && message.Content[0].Type == "input_text" && strings.HasPrefix(message.Content[0].Text, "Message Type: FINAL_ANSWER\n")
}

func stripInitialContext(messages []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == llm.RoleSystem {
			continue
		}
		out = append(out, cloneMessage(msg))
	}
	return out
}

func cloneCompactMessages(messages []llm.Message) []llm.Message {
	out := make([]llm.Message, len(messages))
	for i := range messages {
		out[i] = cloneMessage(messages[i])
	}
	return out
}

func cloneMessage(msg llm.Message) llm.Message {
	msg.Parts = append([]llm.ContentPart(nil), msg.Parts...)
	msg.ToolCalls = append([]llm.ToolCall(nil), msg.ToolCalls...)
	if msg.Compaction != nil {
		state := *msg.Compaction
		msg.Compaction = &state
	}
	if msg.AgentMessage != nil {
		state := *msg.AgentMessage
		state.Content = append([]llm.AgentMessageContent(nil), msg.AgentMessage.Content...)
		msg.AgentMessage = &state
	}
	return msg
}

func truncateTextToTokenBudget(text string, budget int) string {
	maxBytes := max(0, budget*4)
	if maxBytes > 0 && len(text) <= maxBytes {
		return text
	}
	leftEnd := utf8PrefixBytes(text, maxBytes/2)
	rightStart := utf8SuffixStart(text, maxBytes-maxBytes/2)
	if rightStart < leftEnd {
		rightStart = leftEnd
	}
	removedTokens := max(0, (len(text)-maxBytes+3)/4)
	return text[:leftEnd] + fmt.Sprintf("…%d tokens truncated…", removedTokens) + text[rightStart:]
}

func utf8PrefixBytes(text string, budget int) int {
	if budget <= 0 {
		return 0
	}
	if len(text) <= budget {
		return len(text)
	}
	end := 0
	for i := range text {
		if i > budget {
			break
		}
		end = i
	}
	return end
}

func utf8SuffixStart(text string, budget int) int {
	if budget <= 0 {
		return len(text)
	}
	target := len(text) - budget
	if target <= 0 {
		return 0
	}
	for i := range text {
		if i >= target {
			return i
		}
	}
	return len(text)
}

func lastAssistantModel(turns []state.Message) string {
	for i := len(turns) - 1; i >= 0; i-- {
		if strings.EqualFold(strings.TrimSpace(turns[i].Role), string(llm.RoleAssistant)) {
			if model := strings.TrimSpace(turns[i].Model); model != "" {
				return model
			}
		}
	}
	return ""
}

func shouldRetryWithCurrentModel(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"invalid request", "unexpected status", "response ended with status", "context_length_exceeded", "context window",
		"expected exactly one compaction", "missing encrypted_content", "empty replacement history",
		"usage limit", "rate limit", "overloaded", "internal server", "retry limit",
		"status 400", "status 429", "status 500", "status 502", "status 503", "status 529",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func isContextWindowError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "context_length_exceeded") || strings.Contains(s, "context window") || strings.Contains(s, "prompt is too long") || strings.Contains(s, "maximum context")
}

func normalizedSessionID(s string) string {
	if strings.TrimSpace(s) == "" {
		return "default"
	}
	return strings.TrimSpace(s)
}
func normalizedTrigger(s string) string {
	if strings.TrimSpace(s) == "" {
		return "manual"
	}
	return strings.TrimSpace(s)
}
func normalizedScope(s string) string {
	if strings.TrimSpace(s) == "body_after_prefix" {
		return "body_after_prefix"
	}
	return "total"
}
