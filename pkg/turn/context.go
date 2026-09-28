package turn

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

type SnapshotStore interface {
	GetContextSnapshot(sessionID string) (json.RawMessage, bool)
	GetContextSnapshotForRun(sessionID, runID string, compactions []event.ContextCompactedPayload) (json.RawMessage, bool)
	ClearContextSnapshot(sessionID string)
}

// ContextResetStore moves a conversation's context-reset cursor.
type ContextResetStore interface {
	SetSessionContextResetToLatest(ctx context.Context, sessionID string) (int64, error)
}

// ContextClearedReply is what /clear says on every surface.
const ContextClearedReply = "Context cleared: what came before stays visible but is no longer sent to the model."

// ClearContext is /clear on every surface: the conversation's model context
// starts over. Every row stays for reading and replay, but nothing before this
// point is sent to the model again, and the cached context snapshot, which
// described what was, is dropped.
func ClearContext(ctx context.Context, sessions ContextResetStore, snapshots SnapshotStore, sessionID string) error {
	if _, err := sessions.SetSessionContextResetToLatest(ctx, sessionID); err != nil {
		return err
	}
	if snapshots != nil {
		snapshots.ClearContextSnapshot(sessionID)
	}
	return nil
}

// CompactionLog is where a conversation's compactions are recorded: the
// session's event log, which every surface and every process writes through.
type CompactionLog interface {
	ListSessionEventsOfType(ctx context.Context, sessionID, eventType string, limit int) ([]state.SessionEvent, error)
}

// conversationCompactionLimit bounds how many compactions a context report
// reads back; the report shows the latest and a count.
const conversationCompactionLimit = 64

// ContextSources is what /context reports from. Every surface fills it from
// the same stores, so the report reads the same everywhere.
type ContextSources struct {
	Snapshots   SnapshotStore
	Compactions CompactionLog
	// Filtering is the agent's record of the tool output it filtered before
	// the model saw it; nil when none is kept.
	Filtering *tool.Store
	// Gauge is how full the context window is: the same gauge /status and
	// the composer footer show (ContextGaugeOf).
	Gauge StatusContext
}

// ContextReport is /context: how full the context window is, what the last
// turn brought into it besides the conversation, how the conversation has
// been compacted, and what was kept out of the window. A section that cannot
// be read says so with the error that stopped it.
func ContextReport(ctx context.Context, src ContextSources, sessionID string) string {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		sid = "default"
	}
	var sections []string
	sections = append(sections, contextWindowSection(src.Gauge))
	var compactions []event.ContextCompactedPayload
	var compactErr error
	if src.Compactions != nil {
		compactions, compactErr = ConversationCompactions(ctx, src.Compactions, sid)
	}
	var snap contextSnapshot
	hasSnapshot := false
	if src.Snapshots != nil {
		if raw, ok := src.Snapshots.GetContextSnapshotForRun(sid, tool.RunIDFromContext(ctx), compactions); ok && len(raw) > 0 {
			hasSnapshot = json.Unmarshal(raw, &snap) == nil
		}
	}
	sections = append(sections, broughtInSection(snap, hasSnapshot))
	sections = append(sections, compactionsSection(compactions, compactErr))
	if len(snap.Spills) > 0 {
		sections = append(sections, "Kept out of the window\n  "+countNoun(len(snap.Spills), "tool result")+" too large for the window "+pluralVerb(len(snap.Spills), "was", "were")+" saved to files the model reads as needed.")
	}
	if section := filteringSection(src.Filtering); section != "" {
		sections = append(sections, section)
	}
	return strings.Join(sections, "\n\n")
}

// ContextGaugeOf is how full the context window is, from the conversation's
// size and the model's limits: the one computation behind the /status gauge,
// the /context report and the composer footer, so they cannot disagree.
func ContextGaugeOf(provider, model string, usage, explicitCompactLimit int) StatusContext {
	// The footer ignores Lookup's ok flag and feeds whatever the model table
	// yields (its defaults included); every reader must get the same answer.
	limits, _ := llm.Lookup(strings.TrimSpace(provider), strings.TrimSpace(model))
	budget := state.CalculateTokenBudgetWithOptions(usage, strings.TrimSpace(model), limits, state.TokenBudgetOptions{ExplicitLimit: explicitCompactLimit})
	return StatusContext{
		PercentLeft:   budget.PercentLeft,
		UsedTokens:    budget.TokenUsage,
		WindowTokens:  budget.ContextWindow,
		AutoCompactAt: budget.AutoCompactThreshold,
	}
}

func contextWindowSection(g StatusContext) string {
	var b strings.Builder
	b.WriteString("Context window\n")
	if g.UsedTokens > 0 {
		fmt.Fprintf(&b, "  %s %d%% left · %s of %s", ContextGaugeBar(g.PercentLeft), g.PercentLeft, FormatTokens(g.UsedTokens), FormatTokens(g.WindowTokens))
	} else {
		fmt.Fprintf(&b, "  Empty so far · %s", FormatTokens(g.WindowTokens))
	}
	if g.AutoCompactAt > 0 {
		fmt.Fprintf(&b, "\n  Compacts automatically at %s", FormatTokens(g.AutoCompactAt))
	}
	return b.String()
}

// contextSnapshot is the part of the assembly's snapshot the report reads.
type contextSnapshot struct {
	Budget struct {
		LimitTokens int `json:"limit_tokens"`
	} `json:"budget"`
	Provenance []struct {
		SourceID        string `json:"source_id"`
		Included        bool   `json:"included"`
		Reason          string `json:"reason"`
		EstimatedTokens int    `json:"estimated_tokens"`
	} `json:"provenance"`
	Spills []json.RawMessage `json:"tool_result_spills"`
}

// contextSourceNames names what each assembly source brings into a turn.
var contextSourceNames = map[string]string{
	"working_set_source":  "Files you pinned or referenced",
	"recent_files_source": "Files read recently",
	"git_context_source":  "Git status and changes",
}

func broughtInSection(snap contextSnapshot, ok bool) string {
	var b strings.Builder
	b.WriteString("Brought into the last turn")
	if !ok {
		b.WriteString("\n  Nothing yet: this is recorded when a turn starts.")
		return b.String()
	}
	lines := 0
	for _, p := range snap.Provenance {
		name := contextSourceNames[strings.TrimSpace(p.SourceID)]
		if name == "" {
			name = strings.TrimSpace(p.SourceID)
		}
		switch {
		case p.Included:
			fmt.Fprintf(&b, "\n  %s · %s tokens", name, FormatTokens(p.EstimatedTokens))
		case p.Reason == "budget":
			fmt.Fprintf(&b, "\n  %s · left out, over the %s-token allowance", name, FormatTokens(snap.Budget.LimitTokens))
		case p.Reason == "max_items":
			fmt.Fprintf(&b, "\n  %s · left out, too many items", name)
		default:
			continue
		}
		lines++
	}
	if lines == 0 {
		b.WriteString("\n  Nothing besides the conversation: no pinned files, recently read files or git changes.")
	}
	return b.String()
}

// ConversationCompactions returns the conversation's own compactions, oldest
// first. A subagent's compaction rewrote that subagent's history, not the
// conversation's, and is left out.
func ConversationCompactions(ctx context.Context, log CompactionLog, sessionID string) ([]event.ContextCompactedPayload, error) {
	events, err := log.ListSessionEventsOfType(ctx, sessionID, event.RunEventContextCompacted, conversationCompactionLimit)
	if err != nil {
		return nil, err
	}
	out := make([]event.ContextCompactedPayload, 0, len(events))
	for _, evt := range events {
		var payload event.ContextCompactedPayload
		if json.Unmarshal(evt.Payload, &payload) != nil || strings.TrimSpace(payload.AgentID) != "" {
			continue
		}
		out = append(out, payload.Canonicalized())
	}
	return out, nil
}

func compactionsSection(compactions []event.ContextCompactedPayload, err error) string {
	var b strings.Builder
	b.WriteString("Compactions")
	switch {
	case err != nil:
		b.WriteString("\n  " + err.Error())
	case len(compactions) == 0:
		b.WriteString("\n  None in this conversation yet.")
	default:
		latest := compactions[len(compactions)-1]
		fmt.Fprintf(&b, "\n  %s in this conversation; the latest", countNoun(len(compactions), "compaction"))
		if at, err := time.Parse(time.RFC3339, latest.CreatedAtUTC); err == nil {
			fmt.Fprintf(&b, ", at %s,", at.Local().Format("Jan 2 15:04"))
		}
		fmt.Fprintf(&b, " was %s", compactionTriggerText(latest))
		if before, after := latest.TokensBefore, latest.TokensAfter; before > 0 && after > 0 {
			fmt.Fprintf(&b, " and took it from %s to %s tokens", FormatTokens(before), FormatTokens(after))
		}
		b.WriteString(".")
	}
	return b.String()
}

func compactionTriggerText(p event.ContextCompactedPayload) string {
	switch {
	case p.Reactive:
		return "forced by the provider rejecting a full context"
	case strings.EqualFold(p.Trigger, "manual"):
		return "asked for with /compact"
	default:
		return "automatic"
	}
}

// filteringSection reports the tool output filtered before the model saw it.
// The record is the agent's, kept across its conversations.
func filteringSection(store *tool.Store) string {
	if store == nil {
		return ""
	}
	all, err := store.StatsSince(time.Time{})
	if err != nil {
		return "Filtered tool output\n  " + err.Error()
	}
	if all.TotalCommands == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Filtered tool output, across this agent's conversations\n  %d of %s trimmed before the model saw them · about %s tokens saved",
		all.FilteredCommands, countNoun(int(all.TotalCommands), "output"), FormatTokens(int(all.SavedTokens)))
	if all.Retrievals > 0 {
		fmt.Fprintf(&b, "\n  The model fetched a full output back %s", countNoun(int(all.Retrievals), "time"))
	}
	for _, kind := range []struct{ id, label string }{{tool.KindShell, "Shell commands"}, {tool.KindMCP, "MCP tools"}} {
		stats, err := store.StatsSinceKind(time.Time{}, kind.id)
		if err != nil || stats.TotalCommands == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n  %s · %d of %d trimmed · about %s tokens saved", kind.label, stats.FilteredCommands, stats.TotalCommands, FormatTokens(int(stats.SavedTokens)))
	}
	return b.String()
}

func pluralVerb(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
