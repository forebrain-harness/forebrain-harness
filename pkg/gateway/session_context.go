package gateway

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

func (s *Server) handleChatSessionContext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	sessionID := strings.TrimSpace(ParamsFromContext(r.Context()).ByName("id"))
	if sessionID == "" {
		http.NotFound(w, r)
		return
	}
	if s == nil || s.Runner == nil {
		http.Error(w, "context unavailable", http.StatusServiceUnavailable)
		return
	}
	if tools := s.Env.Tools(); tools == nil {
		http.Error(w, "context unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()
	if runID := strings.TrimSpace(r.URL.Query().Get("run_id")); runID != "" {
		ctx = tool.WithRunID(ctx, runID)
	}
	w.Header().Set("Content-Type", "application/json")

	body := map[string]any{}
	var compactions []event.ContextCompactedPayload
	if s.RunRT != nil {
		read, err := turn.ConversationCompactions(ctx, s.RunRT, sessionID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		compactions = read
	}
	if st := s.Env.Tools(); st != nil {
		snapshot, ok := st.GetContextSnapshotForRun(sessionID, tool.RunIDFromContext(ctx), compactions)
		if ok && len(snapshot) > 0 {
			_ = json.Unmarshal(snapshot, &body)
			if body == nil {
				body = map[string]any{}
			}
		}
	}
	if s.Sessions != nil {
		if boundaryID, part, err := s.Sessions.LatestCompactBoundary(ctx, sessionID); err == nil && boundaryID > 0 {
			body["window_number"] = part.WindowNumber
			body["active_boundary_id"] = strings.TrimSpace(formatOptionalBoundaryRowID(boundaryID))
			body["active_window_id"] = strings.TrimSpace(part.WindowID)
		}
	}
	if timeline, ok := body["context_timeline"].([]any); ok && len(timeline) > 0 {
		body["compact_audit"] = map[string]any{
			"session_id": sessionID,
			"run_id":     tool.RunIDFromContext(ctx),
			"timeline":   timeline,
		}
		body["compact_export"] = map[string]any{
			"session_id":         sessionID,
			"run_id":             tool.RunIDFromContext(ctx),
			"generated_from":     "session_context",
			"context_timeline":   timeline,
			"token_attribution":  body["token_attribution"],
			"window_number":      body["window_number"],
			"active_boundary_id": body["active_boundary_id"],
			"active_window_id":   body["active_window_id"],
		}
		body["compact_diff"] = buildCompactDiff(timeline)
	}
	if spills, ok := body["tool_result_spills"].([]any); ok {
		body["tool_result_spill_count"] = len(spills)
	}
	enc, _ := json.Marshal(body)
	_, _ = w.Write(enc)
}

func buildCompactDiff(timeline []any) []map[string]any {
	if len(timeline) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(timeline))
	var prevTokensAfter int
	var havePrev bool
	for _, raw := range timeline {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if kind := strings.TrimSpace(stringValue(entry["kind"])); kind != "compact" {
			continue
		}
		diff := map[string]any{
			"trigger": strings.TrimSpace(stringValue(entry["trigger"])), "strategy": strings.TrimSpace(stringValue(entry["strategy"])),
			"boundary_id": strings.TrimSpace(stringValue(entry["boundary_id"])), "window_number": intValue(entry["window_number"]),
			"tokens_before": intValue(entry["tokens_before"]), "tokens_after": intValue(entry["tokens_after"]),
		}
		if havePrev {
			diff["delta_from_previous_after"] = intValue(entry["tokens_after"]) - prevTokensAfter
		}
		prevTokensAfter = intValue(entry["tokens_after"])
		havePrev = true
		out = append(out, diff)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func intValue(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float32:
		return int(x)
	case float64:
		return int(x)
	default:
		return 0
	}
}

func formatOptionalBoundaryRowID(v int64) string {
	if v <= 0 {
		return ""
	}
	return strconv.FormatInt(v, 10)
}
