package tui

import (
	"context"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

// turnCompletedEvent is a finished run as the conversation log records it: the
// clock the worked line reports and the run's checklist facts. Both surfaces
// write the same record for their own runs, so a resumed conversation and the
// gateway read one shape.
//
// The answer text itself is the transcript's, written just before this; the
// event does not carry a second copy of it. Text is left empty on purpose.
//
// The id is derived from the run rather than generated per call, so closing
// the same run twice — a reconnect, a replayed close — records one end, not
// two, the way every other derived-identity event the surface writes does.
func (s *ChatSession) turnCompletedEvent(sessionID, runID string, elapsed time.Duration) event.RunEvent {
	return event.NewRunEvent("turn-completed:"+runID, runID, sessionID, event.RunEventTurnCompleted, event.TurnCompletedPayload{
		ElapsedMS:    elapsed.Milliseconds(),
		RunPlanFacts: s.runPlanFacts(context.Background(), runID),
		RunArtifacts: s.runArtifacts(context.Background(), runID),
	}, time.Now())
}
