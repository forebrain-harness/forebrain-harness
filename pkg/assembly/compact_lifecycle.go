package assembly

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/google/uuid"
)

// compaction is one compaction as every surface sees it. Each of the service's
// entry points — the manual command, the pre-turn check, and the mid-turn
// checkpoint of any agent — runs its work inside exactly one, so the lifecycle
// is published by the only code that knows when a compaction starts, how far it
// has come and how it ended, and never re-derived by a surface from a result it
// was handed afterwards.
//
// The events go to the conversation the user is looking at: the conversation
// session and the agent come from the context, so a subagent's compaction —
// which rewrites that subagent's worker transcript — is shown in that
// subagent's view and not in the conversation.
type compaction struct {
	events       event.Sink
	publishCtx   context.Context
	id           string
	runID        string
	sessionID    string
	agentID      string
	trigger      string
	tokensBefore int
	started      time.Time

	mu   sync.Mutex
	last compactProgress
}

// beginCompaction publishes context_compacting and returns the context the
// compaction must run under, which carries its progress reporter.
func (s Service) beginCompaction(ctx context.Context, sessionID, trigger string, tokensBefore int) (context.Context, *compaction) {
	sid := tool.ConversationSessionIDFromContext(ctx)
	if sid == "" {
		sid = normalizedSessionID(sessionID)
	}
	c := &compaction{
		events: s.Events,
		// The terminal event must be recorded even when the compaction ended
		// because its context was cancelled: that is exactly the event that
		// tells the surface the running card is over.
		publishCtx:   context.WithoutCancel(ctx),
		id:           uuid.Must(uuid.NewV7()).String(),
		runID:        strings.TrimSpace(tool.RunIDFromContext(ctx)),
		sessionID:    sid,
		agentID:      tool.HookAgentIDFromContext(ctx),
		trigger:      normalizedTrigger(trigger),
		tokensBefore: tokensBefore,
		started:      time.Now(),
		last:         compactProgress{Percent: -1},
	}
	c.publish(event.RunEventContextCompacting, event.ContextCompactingPayload{
		CompactionID: c.id, Trigger: c.trigger, TokensBefore: tokensBefore, AgentID: c.agentID,
	})
	return context.WithValue(ctx, compactProgressKey{}, c), c
}

// finish publishes context_compacted for a checkpoint that is persisted, and
// returns the result with the compaction's own duration.
func (c *compaction) finish(res Result) Result {
	res.Duration = time.Since(c.started)
	payload := CompactResultPayload(res)
	payload.CompactionID = c.id
	payload.AgentID = c.agentID
	payload.CreatedAtUTC = time.Now().UTC().Format(time.RFC3339)
	payload.Duration = res.Duration.Round(100 * time.Millisecond).String()
	c.publish(event.RunEventContextCompacted, payload.Canonicalized())
	return res
}

// fail publishes context_compact_failed for a compaction that replaced
// nothing. A cancellation is the user's own decision, not an error.
func (c *compaction) fail(err error) {
	payload := event.ContextCompactFailedPayload{CompactionID: c.id, Trigger: c.trigger, AgentID: c.agentID}
	if errors.Is(err, context.Canceled) {
		payload.Cancelled = true
	} else if err != nil {
		payload.Error = strings.TrimSpace(err.Error())
	}
	c.publish(event.RunEventContextCompactError, payload)
}

func (c *compaction) publish(eventType string, payload any) {
	if c.events == nil {
		return
	}
	_ = c.events.Publish(c.publishCtx, event.NewRunEvent("", c.runID, c.sessionID, eventType, payload, time.Now()))
}

// compactProgress is how far a running compaction has come. Percent rises
// monotonically from 0 and stays below 100: completion is the compacted event
// itself, never a progress report.
type compactProgress struct {
	Percent int
	Phase   string
}

type compactProgressKey struct{}

// compactPhaseRank orders the phases, so a report can never take a
// compaction back to a phase it has left.
func compactPhaseRank(phase string) int {
	switch phase {
	case event.CompactPhaseReading:
		return 1
	case event.CompactPhaseSummarizing:
		return 2
	case event.CompactPhaseSaving:
		return 3
	default:
		return 0
	}
}

// reportCompactProgress forwards progress for the compaction running under
// ctx, never backwards: a later, lower estimate — a retry restarting its
// count, a reading tick that lost the race with the first summary token — is
// dropped, and so is a report that changes nothing.
func reportCompactProgress(ctx context.Context, percent int, phase string) {
	c, _ := ctx.Value(compactProgressKey{}).(*compaction)
	if c == nil {
		return
	}
	percent = min(max(percent, 0), 99)
	c.mu.Lock()
	stale := percent < c.last.Percent || compactPhaseRank(phase) < compactPhaseRank(c.last.Phase)
	if stale || (percent == c.last.Percent && phase == c.last.Phase) {
		c.mu.Unlock()
		return
	}
	c.last = compactProgress{Percent: percent, Phase: phase}
	c.mu.Unlock()
	c.publish(event.RunEventContextCompactProgress, event.ContextCompactProgressPayload{
		CompactionID: c.id, Percent: percent, Phase: phase, AgentID: c.agentID,
	})
}

// currentCompactPercent is how far the compaction under ctx has come.
func currentCompactPercent(ctx context.Context) int {
	c, _ := ctx.Value(compactProgressKey{}).(*compaction)
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return max(c.last.Percent, 0)
}

// The phases share the bar: reading the history spans
// [readingStartPercent, readingEndPercent), writing the summary runs from
// wherever reading stopped up to summaryEndPercent, and the rest is saving the
// checkpoint.
const (
	readingStartPercent = 3
	readingEndPercent   = 25
	summaryEndPercent   = 92
	savingPercent       = 95
	// readingTick is how often the reading phase advances while nothing
	// measurable arrives.
	readingTick = 250 * time.Millisecond
)

// readingProgressPercent maps the time the model has spent taking in the
// history onto the reading phase. Nothing measurable arrives before the first
// token, so the estimate follows the clock against the time a history of this
// size typically takes to read, and approaches — never reaches — the end of
// the phase.
func readingProgressPercent(elapsed time.Duration, historyTokens int) int {
	expected := max(3*time.Second, time.Duration(historyTokens)*time.Second/5000)
	fraction := 1 - math.Exp(-float64(elapsed)/float64(expected))
	return min(readingStartPercent+int(float64(readingEndPercent-readingStartPercent)*fraction), readingEndPercent-1)
}

// summaryProgressPercent maps the summary output streamed so far onto the
// summarizing phase, which starts wherever reading left the bar. A summary's
// final length is unknown until it ends, so the estimate follows the tokens
// actually arriving against the length summaries typically reach — a
// checkpoint runs to a couple of thousand tokens whatever the history's size —
// and approaches, never reaches, the end of the phase: the bar moves exactly
// as fast as the model writes, and only the persisted checkpoint completes it.
func summaryProgressPercent(base, outputTokens, historyTokens int) int {
	expected := min(max(historyTokens/40, 300), 1100)
	fraction := 1 - math.Exp(-float64(outputTokens)/float64(expected))
	return min(base+int(float64(summaryEndPercent-base)*fraction), summaryEndPercent-1)
}

// startReadingProgress advances the reading phase with the clock until stop
// is called — at the model's first output, or when the request ends.
func startReadingProgress(ctx context.Context, historyTokens int) (stop func()) {
	c, _ := ctx.Value(compactProgressKey{}).(*compaction)
	if c == nil {
		return func() {}
	}
	done := make(chan struct{})
	var once sync.Once
	started := time.Now()
	reportCompactProgress(ctx, readingStartPercent, event.CompactPhaseReading)
	go func() {
		ticker := time.NewTicker(readingTick)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				reportCompactProgress(ctx, readingProgressPercent(time.Since(started), historyTokens), event.CompactPhaseReading)
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

// withSummaryProgress runs a summarization request with its progress
// measured: the reading phase until the first output, then the output itself.
// The summary never reaches the foreground: assistant and reasoning deltas are
// counted here and not forwarded, and the request's own occupancy snapshot is
// dropped — it describes the summarization call, not the conversation, which
// the compacted event re-reports once it lands. The returned stop ends the
// reading phase's clock and must be called when the request is over.
func withSummaryProgress(ctx context.Context, historyTokens int) (context.Context, func()) {
	ctx = llm.WithMutedForegroundStream(ctx)
	sink := llm.StreamSink{}
	if muted := llm.StreamSinkFrom(ctx); muted != nil {
		sink = *muted
	}
	sink.OnUsageSnapshot = nil
	stopReading := func() {}
	if c, _ := ctx.Value(compactProgressKey{}).(*compaction); c != nil {
		stopReading = startReadingProgress(ctx, historyTokens)
		var mu sync.Mutex
		generated, base := 0, -1
		count := func(text string) {
			mu.Lock()
			if base < 0 {
				stopReading()
				base = max(currentCompactPercent(ctx), readingStartPercent)
			}
			generated += llm.EstimateText(text)
			percent := summaryProgressPercent(base, generated, historyTokens)
			mu.Unlock()
			reportCompactProgress(ctx, percent, event.CompactPhaseSummarizing)
		}
		sink.OnDelta = count
		sink.OnReasoningDelta = count
	}
	return llm.WithStreamSink(ctx, &sink), stopReading
}
