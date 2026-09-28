package assembly

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

// FormatCompactBanner is the one line a finished compaction is read by: how
// much history there was, how much is left, and how long it took —
// "182.4k → 12.3k tokens (−93%) · 14.2s". The rest of the record (trigger,
// strategy, boundary) is bookkeeping and stays out of the line.
func FormatCompactBanner(payload event.ContextCompactedPayload, duration time.Duration) string {
	parts := make([]string, 0, 2)
	if before, after := payload.TokensBefore, payload.TokensAfter; before > 0 {
		tokens := fmt.Sprintf("%s → %s tokens", FormatTokenCount(before), FormatTokenCount(after))
		if after < before {
			tokens += fmt.Sprintf(" (−%d%%)", (before-after)*100/before)
		}
		parts = append(parts, tokens)
	}
	if duration > 0 {
		parts = append(parts, formatCompactDuration(duration))
	}
	return strings.Join(parts, " · ")
}

// FormatCompactOutput returns the body shown below the compact header: the
// checkpoint summary itself, verbatim, so the renderer can lay it out as
// Markdown. Returns "" when there is no checkpoint text, which renders as a
// header-only card.
func FormatCompactOutput(payload event.ContextCompactedPayload) string {
	return strings.TrimSpace(payload.Summary)
}

// FormatTokenCount writes a token count the way people read one: 355, 12.3k,
// 1.2M. The unit is the caller's to name.
//
// Halves round up, the way the web surface rounds them, so a count reads the
// same on both.
func FormatTokenCount(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", max(n, 0))
	case n < 1_000_000:
		return oneDecimal(float64(n)/1000) + "k"
	default:
		return oneDecimal(float64(n)/1_000_000) + "M"
	}
}

func oneDecimal(v float64) string {
	return trimTrailingZero(fmt.Sprintf("%.1f", math.Round(v*10)/10))
}

func trimTrailingZero(s string) string {
	return strings.TrimSuffix(s, ".0")
}

func formatCompactDuration(d time.Duration) string {
	if d < time.Minute {
		return trimTrailingZero(fmt.Sprintf("%.1f", d.Seconds())) + "s"
	}
	return d.Round(time.Second).String()
}
