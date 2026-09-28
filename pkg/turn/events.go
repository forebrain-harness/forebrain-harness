// Run event listing and cancellation finalisation.
package turn

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// ListRunEvents returns a run's own events in the wire shape, oldest first.
// The durable log is the one record of what the run did: the run events API and
// the goal reader page the same rows the conversation does, scoped to the run
// that produced them.
func (s *Service) ListRunEvents(ctx context.Context, runID string, limit int) ([]event.RunEvent, error) {
	if s == nil || s.runEventStore == nil {
		return []event.RunEvent{}, nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return []event.RunEvent{}, nil
	}
	records, err := s.runEventStore.ListRunEvents(ctx, runID, limit)
	if err != nil {
		return nil, fmt.Errorf("list run events: %w", err)
	}
	events := make([]event.RunEvent, 0, len(records))
	for _, record := range records {
		events = append(events, RunEventFromRecord(record))
	}
	return events, nil
}

// RunEventFromRecord lifts one stored event to the wire type every surface
// reads. The stored row carries no schema version because a migrated row is at
// the current shape by construction, so the wire stamps it here.
func RunEventFromRecord(record state.SessionEvent) event.RunEvent {
	return event.RunEvent{
		ID:            record.ID,
		Sequence:      record.Sequence,
		SchemaVersion: event.RunEventSchemaVersion,
		RunID:         record.RunID,
		SessionID:     record.SessionID,
		Type:          record.Type,
		Payload:       append(json.RawMessage(nil), record.Payload...),
		CreatedAt:     record.CreatedAt,
	}
}

func ExtractTurnDiffPayload(payload any) (event.TurnDiffUpdatedPayload, bool) {
	payloadMap, ok := normalizePayloadMap(payload)
	if !ok {
		return event.TurnDiffUpdatedPayload{}, false
	}
	output := mapField(payloadMap, "output")
	if output == nil {
		if nested := eventString(payloadMap, "data"); strings.TrimSpace(nested) != "" {
			var nestedPayload any
			if err := json.Unmarshal([]byte(nested), &nestedPayload); err == nil {
				output = mapField(nestedPayload, "output")
			}
		}
	}
	if output == nil {
		return event.TurnDiffUpdatedPayload{}, false
	}
	diffMap := mapField(output, "turn_diff")
	if diffMap == nil {
		return event.TurnDiffUpdatedPayload{}, false
	}
	path := strings.TrimSpace(eventString(diffMap, "path"))
	added := intField(diffMap, "added")
	deleted := intField(diffMap, "deleted")
	diffText := eventString(diffMap, "unified_diff")
	status := strings.TrimSpace(eventString(diffMap, "status"))
	if status == "" {
		status = inferTurnDiffStatus(added, deleted)
	}
	summary := strings.TrimSpace(path)
	if summary != "" || added > 0 || deleted > 0 {
		summary = fmt.Sprintf("%s (+%d/-%d)", path, added, deleted)
	}
	file := event.TurnDiffFile{
		Path:    path,
		Status:  status,
		Added:   added,
		Deleted: deleted,
	}
	if strings.TrimSpace(diffText) != "" {
		parsed := event.Parse(diffText)
		if len(parsed.Files) > 0 {
			file.OldPath = parsed.Files[0].OldPath
			file.Binary = parsed.Files[0].Binary
			file.Hunks = toProtoHunks(parsed.Files[0].Hunks)
		} else {
			// diffText had no recognisable diff --git header; build a synthetic
			// hunk from the raw text so the frontend still has something to render.
			file.Hunks = rawTextHunks(diffText)
		}
	}
	return event.TurnDiffUpdatedPayload{
		Files:   []event.TurnDiffFile{file},
		Summary: summary,
	}, true
}

// toProtoHunks converts diffview hunks to the protocol wire type.
func toProtoHunks(hunks []event.Hunk) []event.DiffHunk {
	if len(hunks) == 0 {
		return nil
	}
	out := make([]event.DiffHunk, len(hunks))
	for i, h := range hunks {
		out[i] = event.DiffHunk{
			OldStart: h.OldStart,
			NewStart: h.NewStart,
			Lines:    toProtoLines(h.Lines),
		}
	}
	return out
}

func toProtoLines(lines []event.DiffLine) []event.DiffLine {
	out := make([]event.DiffLine, len(lines))
	for i, l := range lines {
		var kind event.DiffLineKind
		switch l.Kind {
		case event.LineAdd:
			kind = event.DiffLineAdd
		case event.LineDel:
			kind = event.DiffLineDelete
		default:
			kind = event.DiffLineContext
		}
		out[i] = event.DiffLine{Kind: kind, OldNo: l.OldNo, NewNo: l.NewNo, Text: l.Text}
	}
	return out
}

// rawTextHunks wraps a raw diff string into a single synthetic hunk so the
// frontend can still render it even without proper diff --git headers.
func rawTextHunks(text string) []event.DiffHunk {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	hunk := event.DiffHunk{}
	for _, line := range lines {
		switch {
		case len(line) > 0 && line[0] == '+' && !strings.HasPrefix(line, "+++"):
			hunk.Lines = append(hunk.Lines, event.DiffLine{Kind: event.DiffLineAdd, Text: line[1:]})
		case len(line) > 0 && line[0] == '-' && !strings.HasPrefix(line, "---"):
			hunk.Lines = append(hunk.Lines, event.DiffLine{Kind: event.DiffLineDelete, Text: line[1:]})
		default:
			hunk.Lines = append(hunk.Lines, event.DiffLine{Kind: event.DiffLineContext, Text: strings.TrimPrefix(line, " ")})
		}
	}
	return []event.DiffHunk{hunk}
}

func normalizePayloadMap(payload any) (map[string]any, bool) {
	switch x := payload.(type) {
	case map[string]any:
		return x, true
	default:
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, false
		}
		var out map[string]any
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, false
		}
		return out, true
	}
}

func mapField(v any, key string) map[string]any {
	base, ok := normalizePayloadMap(v)
	if !ok {
		return nil
	}
	if raw, ok := base[key].(map[string]any); ok {
		return raw
	}
	if raw := base[key]; raw != nil {
		if nested, ok := normalizePayloadMap(raw); ok {
			return nested
		}
	}
	return nil
}

func eventString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func intField(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch raw := m[key].(type) {
	case int:
		return raw
	case int32:
		return int(raw)
	case int64:
		return int(raw)
	case float32:
		return int(raw)
	case float64:
		return int(raw)
	default:
		return 0
	}
}

func inferTurnDiffStatus(added, deleted int) string {
	switch {
	case added > 0 && deleted == 0:
		return "added"
	case added == 0 && deleted > 0:
		return "deleted"
	case added > 0 || deleted > 0:
		return "modified"
	default:
		return ""
	}
}

// RunCanceler persists a cancelled run and its active descendants.
type RunCanceler interface {
	SetStatus(context.Context, string, state.RunStatus) error
	CancelRunningDescendants(context.Context, string) error
}

// FinalizeCancel records cancellation after its execution has been stopped.
func FinalizeCancel(ctx context.Context, store RunCanceler, runID string) {
	if store == nil {
		return
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	_ = store.SetStatus(ctx, runID, state.RunStatusCancelled)
	_ = store.CancelRunningDescendants(ctx, runID)
}
