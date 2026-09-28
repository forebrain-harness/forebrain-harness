package state

import (
	"encoding/json"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

const PartTypeContextCompactBoundary = "context_compact_boundary"

// CompactSummaryPrefix marks a compaction handoff summary. It is the canonical
// definition; assembly.SummaryPrefix re-exports it so the session layer can
// recognise a summary message without importing the compact package (which
// already imports session). A summary is only ever legitimate inside a
// boundary's ReplacementHistory — never as a standalone transcript row — so the
// persistence layer uses this to refuse writing one back into the append-only
// log.
const CompactSummaryPrefix = `Another language model started to solve this problem and produced a summary of its thinking process. You also have access to the state of the tools that were used by that language model. Use this to build on the work that has already been done and avoid duplicating work. Here is the summary produced by the other language model, use the information in this summary to assist with your own analysis:`

// IsCompactSummaryMessage reports whether a message is a compaction handoff
// summary (the SummaryPrefix-tagged user message stored inside a checkpoint's
// ReplacementHistory).
func IsCompactSummaryMessage(msg llm.Message) bool {
	if strings.TrimSpace(msg.Role) != llm.RoleUser {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(msg.TextContent()), CompactSummaryPrefix+"\n")
}

// CompactBoundaryPart is the durable CompactedItem checkpoint.
// ReplacementHistory is authoritative; older transcript rows are audit-only.
type CompactBoundaryPart struct {
	Type               string        `json:"type"`
	Trigger            string        `json:"trigger,omitempty"`
	Strategy           string        `json:"strategy,omitempty"`
	SummarySource      string        `json:"summary_source,omitempty"`
	ReplacementHistory []llm.Message `json:"replacement_history"`
	WindowNumber       uint64        `json:"window_number"`
	FirstWindowID      string        `json:"first_window_id"`
	PreviousWindowID   string        `json:"previous_window_id,omitempty"`
	WindowID           string        `json:"window_id"`
}

func EncodeCompactBoundaryPart(part CompactBoundaryPart) string {
	part.Type = PartTypeContextCompactBoundary
	b, err := json.Marshal(part)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func PartsJSONWithCompactPart(fallbackText, rawCompactPart string) string {
	rawCompactPart = strings.TrimSpace(rawCompactPart)
	if rawCompactPart == "" || !json.Valid([]byte(rawCompactPart)) {
		return ContentPartsJSON(nil, fallbackText)
	}
	var parts []json.RawMessage
	_ = json.Unmarshal([]byte(ContentPartsJSON(nil, fallbackText)), &parts)
	parts = append(parts, json.RawMessage(rawCompactPart))
	b, err := json.Marshal(parts)
	if err != nil {
		return ContentPartsJSON(nil, fallbackText)
	}
	return string(b)
}

func ParseCompactBoundaryPart(raw string) (CompactBoundaryPart, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return CompactBoundaryPart{}, false
	}
	var parts []json.RawMessage
	if json.Unmarshal([]byte(raw), &parts) == nil {
		for _, item := range parts {
			var part CompactBoundaryPart
			if json.Unmarshal(item, &part) == nil && part.Type == PartTypeContextCompactBoundary {
				return part, true
			}
		}
	}
	var part CompactBoundaryPart
	if json.Unmarshal([]byte(raw), &part) != nil || part.Type != PartTypeContextCompactBoundary {
		return CompactBoundaryPart{}, false
	}
	return part, true
}
