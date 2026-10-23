package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// A tool card's header is worded by the engine (tool.DisplayHeader) — the
// terminal's words, "Read types.go lines 2-101", "Ran ls", "Notes" — so the
// web draws the same line the terminal does for the same call. The gateway
// only supplies what the wording needs that a page cannot: the conversation's
// own directory, which is what a path is shown relative to (a file inside the
// project reads relative, one outside it keeps its absolute path).

// sessionDisplayRoot is the directory a session's paths are shown relative to:
// its project root. A session with none — or whose directory is gone — falls
// back to the filesystem root, which relativises nothing; the empty string
// would instead mean "this process's working directory", which is the
// gateway's launch directory and means nothing here.
func (s *Server) sessionDisplayRoot(ctx context.Context, sessionID string) string {
	if root, err := s.filesRoot(ctx, sessionID, ""); err == nil && strings.TrimSpace(root) != "" {
		return strings.TrimSpace(root)
	}
	return string(filepath.Separator)
}

// toolHeaderFor is one card's header: the tool, what it is doing or did, and
// how long it took when that is known.
func toolHeaderFor(root, toolName, summary, body string, meta tool.ToolMeta, final bool, took time.Duration) string {
	if strings.TrimSpace(meta.ToolName) == "" {
		meta.ToolName = toolName
	}
	title := strings.TrimSpace(toolName)
	if title == "" {
		title = strings.TrimSpace(meta.ToolName)
	}
	return tool.DisplayHeader(tool.DisplayFrame{
		Title:    title,
		Summary:  summary,
		Meta:     meta,
		Final:    final,
		Content:  body,
		Duration: took,
	}, root)
}

// withStepHeader adds the card's header to a live step's data.
func withStepHeader(data map[string]any, header string) map[string]any {
	if data == nil {
		return nil
	}
	if h := strings.TrimSpace(header); h != "" {
		data["header"] = h
	}
	return data
}

// storedToolRowHeader is the header of one stored tool row, rebuilt from what
// the row kept — its display part and tool meta — so a reloaded card reads
// exactly as the live one did, however old the row is.
func storedToolRowHeader(root, partsJSON, toolMetaJSON string, took time.Duration) string {
	display, _ := state.ParseToolDisplayPart(partsJSON)
	var meta tool.ToolMeta
	for _, raw := range []string{display.ToolMetaJSON, toolMetaJSON} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if json.Unmarshal([]byte(raw), &meta) == nil {
			break
		}
	}
	if strings.TrimSpace(meta.ToolName) == "" {
		return ""
	}
	return toolHeaderFor(root, meta.ToolName, display.Summary, display.Body, meta, true, took)
}

// canceledCallHeaders is, for an assistant row that issued tool calls, the
// header each call would carry if it never ran ("Canceled go test ./...") —
// what the card of a call with no result of its own reads. The page uses one
// only for a call the transcript never answered.
func canceledCallHeaders(root, partsJSON string) map[string]string {
	raw := strings.TrimSpace(partsJSON)
	if raw == "" || !strings.Contains(raw, state.PartTypeToolCalls) {
		return nil
	}
	var parts []struct {
		Type      string `json:"type"`
		ToolCalls []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if json.Unmarshal([]byte(raw), &parts) != nil {
		return nil
	}
	out := map[string]string{}
	for _, part := range parts {
		if part.Type != state.PartTypeToolCalls {
			continue
		}
		for _, call := range part.ToolCalls {
			name := strings.TrimSpace(call.Function.Name)
			if call.ID == "" || name == "" {
				continue
			}
			var input map[string]any
			if args := strings.TrimSpace(call.Function.Arguments); args != "" {
				_ = json.Unmarshal([]byte(args), &input)
			}
			meta := tool.ToolMeta{ToolName: name, Status: "canceled", Input: input}
			out[call.ID] = toolHeaderFor(root, name, "", "", meta, true, 0)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// toolEventPayloadWithHeader adds the card header to a tool call's event
// payload, so a subagent's cards — read from the event log, live or replayed —
// say what the terminal says. Anything that is not a tool_call_started or
// tool_call_completed event, or whose payload does not parse, is returned as it
// came.
func toolEventPayloadWithHeader(root, eventType string, payload json.RawMessage) json.RawMessage {
	final := eventType == event.RunEventToolCompleted
	if (!final && eventType != event.RunEventToolStarted) || len(payload) == 0 {
		return payload
	}
	var fields map[string]any
	if json.Unmarshal(payload, &fields) != nil {
		return payload
	}
	var meta tool.ToolMeta
	if raw, err := json.Marshal(fields["tool_meta"]); err == nil {
		_ = json.Unmarshal(raw, &meta)
	}
	name, _ := fields["tool_name"].(string)
	summary, _ := fields["summary"].(string)
	body, _ := fields["display_body"].(string)
	var took time.Duration
	if seconds, ok := fields["duration_seconds"].(float64); ok && seconds > 0 {
		took = time.Duration(seconds * float64(time.Second))
	}
	header := toolHeaderFor(root, name, summary, body, meta, final, took)
	if strings.TrimSpace(header) == "" {
		return payload
	}
	fields["header"] = header
	out, err := json.Marshal(fields)
	if err != nil {
		return payload
	}
	return out
}
