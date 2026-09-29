package gateway

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
)

const wsProtocolVersion = "2026-04-18"

var wsInboundOps = map[string]struct{}{
	"connect":           {},
	"bind_session":      {},
	"resume_connection": {},
	"start_run":         {},
	"cancel_run":        {},
	// cancel_command stops what the connection is doing with a message before
	// any run exists — a slash command such as /compact, or the compaction a
	// turn waits on before it starts, which it withdraws — so there is no run
	// to cancel by id.
	"cancel_command":  {},
	"switch_mode":     {},
	"query_mode":      {},
	"approve_action":  {},
	"deny_action":     {},
	"submit_answer":   {},
	"sync_context":    {},
	"control_request": {},
	"ping":            {},
	"mcp_status":      {},
	"mcp_resources":   {},
	"mcp_tool_search": {},
	"task_status":     {},
	// cancel_auto_continue stops the continuation a session is waiting to
	// run once a usage limit resets.
	wsOpCancelAutoContinue: {},
}

// wsOutboundOps are ops the server sends that are NOT canonical run events:
// they describe live state (a startup in progress, a connection that failed)
// and must never be persisted to a session's event log.
//
// The list is what makes that structural rather than a promise: the persistence
// path is canonicalRunEventsFromWS, which is a whitelist of inbound ops, so an op
// that appears here and not there writes nothing.
var wsOutboundOps = map[string]struct{}{
	// mcp_status_event is the MCP status notification. It is deliberately NOT
	// named mcp_status: that is the inbound request a client sends to ask for
	// the same information, and the two would then be indistinguishable to a
	// client that has to match a reply to its request.
	"mcp_status_event": {},
	// The answer to cancel_auto_continue. What the cancel changed reaches the
	// session log as its own auto_continue_cancelled event.
	wsOpAutoContinueCancelAck: {},
}

var wsOpsRequireRequestID = map[string]struct{}{
	"bind_session":    {},
	"start_run":       {},
	"cancel_run":      {},
	"switch_mode":     {},
	"approve_action":  {},
	"deny_action":     {},
	"submit_answer":   {},
	"sync_context":    {},
	"control_request": {},
}

type wsRequestCacheEntry struct {
	Response wsServerMsg
	ExpireAt time.Time
}

func normalizeWSClientOp(op string, hasMessage bool) string {
	out := strings.ToLower(strings.TrimSpace(op))
	if out == "" && hasMessage {
		return "start_run"
	}
	return out
}

func normalizeWSClientMessage(m wsClientMsg) wsClientMsg {
	op := normalizeWSClientOp(m.Op, strings.TrimSpace(m.Message.Content) != "" || strings.TrimSpace(m.Content) != "")
	if op == "" && strings.TrimSpace(m.Type) == event.GatewayControlTypeRequest {
		op = "control_request"
	}
	m.Op = op
	return m
}

func validateWSClientMessage(m wsClientMsg) error {
	// An omitted version is the legacy protocol and remains accepted during
	// rollout. Once a client declares the canonical protocol it must match: an
	// unknown approval/event schema must never be interpreted as a successful
	// old operation.
	if version := strings.TrimSpace(m.ProtocolVersion); version != "" && version != wsProtocolVersion {
		return fmt.Errorf("unsupported protocol_version: %s (server: %s)", version, wsProtocolVersion)
	}
	op := normalizeWSClientOp(m.Op, strings.TrimSpace(m.Message.Content) != "" || strings.TrimSpace(m.Content) != "")
	if op == "" {
		return fmt.Errorf("missing op")
	}
	if _, ok := wsInboundOps[op]; !ok {
		return fmt.Errorf("unsupported op: %s", op)
	}
	if _, ok := wsOpsRequireRequestID[op]; ok && strings.TrimSpace(m.RequestID) == "" {
		return fmt.Errorf("request_id required for op: %s", op)
	}
	switch op {
	case "control_request":
		if strings.TrimSpace(m.Type) != event.GatewayControlTypeRequest {
			return fmt.Errorf("control_request type required")
		}
		switch strings.TrimSpace(m.Request.Subtype) {
		case "interrupt":
		default:
			return fmt.Errorf("unsupported control request subtype: %s", strings.TrimSpace(m.Request.Subtype))
		}
	case "approve_action", "deny_action", "submit_answer":
		if strings.TrimSpace(m.ActionID) == "" {
			return fmt.Errorf("action_id required for op: %s", op)
		}
	case wsOpCancelAutoContinue:
		if strings.TrimSpace(m.SessionID) == "" {
			return fmt.Errorf("session_id required for op: %s", op)
		}
	case "cancel_run":
		if strings.TrimSpace(m.RunID) == "" {
			return fmt.Errorf("run_id required for op: %s", op)
		}
	case "switch_mode":
		mode := strings.ToLower(strings.TrimSpace(m.Mode))
		switch mode {
		case "agent", "plan":
		default:
			return fmt.Errorf("unsupported mode: %s", m.Mode)
		}
	case "start_run":
		body := strings.TrimSpace(m.Message.Content)
		if body == "" {
			body = strings.TrimSpace(m.Content)
		}
		// A message may say nothing but what it attached.
		if body == "" && len(m.Message.Attachments) == 0 && len(m.Message.MentionImages) == 0 {
			return fmt.Errorf("message content or an attachment required for start_run")
		}
	}
	return nil
}

func wsRequestCacheGet(cache map[string]wsRequestCacheEntry, requestID string, now time.Time) (wsServerMsg, bool) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return wsServerMsg{}, false
	}
	for k, v := range cache {
		if now.After(v.ExpireAt) {
			delete(cache, k)
		}
	}
	hit, ok := cache[requestID]
	if !ok || now.After(hit.ExpireAt) {
		delete(cache, requestID)
		return wsServerMsg{}, false
	}
	return hit.Response, true
}

func wsRequestCacheSet(cache map[string]wsRequestCacheEntry, requestID string, resp wsServerMsg, ttl time.Duration, now time.Time) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	cache[requestID] = wsRequestCacheEntry{
		Response: resp,
		ExpireAt: now.Add(ttl),
	}
}

// wsInboxLimit is how many messages a client may have sent ahead of a busy
// connection. The web client has at most a couple in flight — a stop, an
// approval — so a client this far ahead is not following the protocol.
const wsInboxLimit = 256

// wsInbox holds what a client sent that the connection's loop has not taken
// yet, in the order it arrived. The reader only ever appends: it is the one
// that sees the client leave and the one that answers a stop while the loop is
// busy with a command or a pre-turn compaction, so it must never wait for the
// loop to take a message.
type wsInbox struct {
	mu      sync.Mutex
	pending []wsClientMsg
	// ready holds a token whenever a message may be waiting.
	ready chan struct{}
}

func newWSInbox() *wsInbox {
	return &wsInbox{ready: make(chan struct{}, 1)}
}

// put queues a message, and reports false for a client more than
// wsInboxLimit messages ahead of the loop.
func (b *wsInbox) put(m wsClientMsg) bool {
	b.mu.Lock()
	if len(b.pending) >= wsInboxLimit {
		b.mu.Unlock()
		return false
	}
	b.pending = append(b.pending, m)
	b.mu.Unlock()
	select {
	case b.ready <- struct{}{}:
	default:
	}
	return true
}

// take returns the oldest message the loop has not taken yet.
func (b *wsInbox) take() (wsClientMsg, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) == 0 {
		return wsClientMsg{}, false
	}
	m := b.pending[0]
	b.pending[0] = wsClientMsg{}
	b.pending = b.pending[1:]
	return m, true
}

// mcpStatusEventData is the payload of an outbound mcp_status_event: the
// generation's servers in configuration order with their startup state.
//
// It carries the state names the REST projection uses, so a client renders the
// same four states from either transport instead of learning two vocabularies.
func mcpStatusEventData(snapshot run.MCPSnapshot) map[string]any {
	servers := make([]map[string]any, 0, len(snapshot.Servers))
	for _, rec := range snapshot.Servers {
		row := map[string]any{
			"name":        rec.Name,
			"conn_status": string(rec.ConnStatus),
			"tool_count":  rec.ToolCount,
			"required":    rec.Required,
			"generation":  snapshot.Generation,
		}
		if strings.TrimSpace(rec.Transport) != "" {
			row["transport"] = rec.Transport
		}
		if strings.TrimSpace(rec.Error) != "" {
			row["error"] = rec.Error
		}
		servers = append(servers, row)
	}
	return map[string]any{
		"generation": snapshot.Generation,
		"pending":    snapshot.Pending,
		"servers":    servers,
	}
}
