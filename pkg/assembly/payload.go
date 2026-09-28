package assembly

import (
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

func CompactResultPayload(res Result) event.ContextCompactedPayload {
	return event.ContextCompactedPayload{
		CreatedAtUTC: time.Now().UTC().Format(time.RFC3339), Trigger: res.Trigger, Strategy: res.Strategy,
		Reason: res.Reason, SummarySource: strings.TrimSpace(res.SummarySource), Scope: res.Scope, ReplacedItems: res.ReplacedTurns,
		WindowNumber: res.WindowNumber, Summary: strings.TrimSpace(res.Summary), BoundaryID: strings.TrimSpace(res.BoundaryID),
		TokensBefore: res.TokensBefore, TokensAfter: res.TokensAfter, Reactive: res.Reactive,
	}
}
