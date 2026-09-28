package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// newTurnErrorDetail classifies a failed turn for the surfaces that write their
// own wording. It returns nil when the provider layer cannot classify the
// failure, which is exactly when the runtime's English sentence is all there is
// to show.
func newTurnErrorDetail(err error) *event.TurnErrorDetail {
	explanation, ok := llm.Explain(err)
	if !ok {
		return nil
	}
	detail := &event.TurnErrorDetail{
		Code:            string(explanation.Code),
		Status:          explanation.Status,
		Plan:            explanation.Plan,
		ProviderMessage: explanation.ProviderMessage,
	}
	if !explanation.ResetAt.IsZero() {
		detail.ResetAt = explanation.ResetAt.UTC().Format(time.RFC3339)
	}
	if explanation.RetryAfter > 0 {
		detail.RetryAfterSeconds = int(explanation.RetryAfter.Round(time.Second) / time.Second)
	}
	return detail
}

// turnErrorDetail recovers the detail a run_error message carries in its data
// field, so the canonical event stream says the same thing the legacy message
// does. Only newTurnErrorDetail puts anything there.
func turnErrorDetail(data any) *event.TurnErrorDetail {
	detail, _ := data.(*event.TurnErrorDetail)
	return detail
}

func canonicalRunEventsFromWS(m wsServerMsg) []event.RunEvent {
	op := strings.TrimSpace(strings.ToLower(m.Op))
	if op == "" || op == "run_event" {
		return nil
	}
	// A live-state op is answered by the socket, never written to the session's
	// log: it describes a startup that is happening right now, and a resumed
	// session must not replay a "connecting" line that finished hours ago. This
	// check is an explicit statement of a guarantee the whitelist below already
	// provides, so a future op added to both places is a deliberate decision
	// rather than an accident.
	if _, live := wsOutboundOps[op]; live {
		return nil
	}

	typeName := ""
	var payload any
	switch op {
	case "run_started":
		typeName = event.RunEventTurnStarted
		payload = event.TurnStartedPayload{}
	case "run_completed":
		typeName = event.RunEventTurnCompleted
		payload = event.TurnCompletedPayload{
			Text:      m.Text,
			ElapsedMS: int64Field(m.Data, "elapsed_ms"),
		}
	case "run_cancelled":
		typeName = event.RunEventTurnCancelled
		payload = event.TurnCancelledPayload{Message: strings.TrimSpace(m.Message)}
	case "run_error":
		typeName = event.RunEventTurnError
		payload = event.TurnErrorPayload{
			Error:   strings.TrimSpace(m.Error),
			Message: strings.TrimSpace(m.Message),
			Detail:  turnErrorDetail(m.Data),
		}
	case "requires_action":
		typeName = event.RunEventApprovalReq
		approvalData := anyMap(m.Data)
		payload = event.ApprovalRequestedPayload{
			ActionID:             firstNonEmptyString(stringOr(approvalData["action_id"]), stringOr(approvalData["id"])),
			ActionKind:           firstNonEmptyString(stringOr(approvalData["action_kind"]), stringOr(approvalData["kind"])),
			AgentID:              stringOr(approvalData["agent_id"]),
			SubagentType:         stringOr(approvalData["subagent_type"]),
			RequiresAction:       approvalRequiresActionPayload(m.Data),
			Message:              strings.TrimSpace(m.Message),
			PermissionSuggestion: approvalPermissionSuggestionPayload(m.Data),
		}
	case "mode_changed":
		typeName = event.RunEventModeChanged
		if modePayload, ok := modeChangedPayload(m.Data); ok {
			payload = modePayload
		} else {
			payload = m.Data
		}
	case "pending_input_updated":
		typeName = event.RunEventPendingInputUpdated
		if pendingPayload, ok := pendingInputUpdatedPayload(m.Data); ok {
			payload = pendingPayload
		} else {
			payload = m.Data
		}
	case "token_budget_updated":
		typeName = event.CompactEventBudgetUpdated
		if budgetPayload, ok := tokenBudgetUpdatedPayload(m.Data); ok {
			payload = budgetPayload
		} else {
			payload = m.Data
		}
	case "step":
		if eventType, stepPayload, ok := stepRunEventPayload(m.Data); ok {
			typeName = eventType
			payload = stepPayload
		}
	}
	if typeName == "" {
		return nil
	}

	runID := strings.TrimSpace(m.RunID)
	sessionID := strings.TrimSpace(m.SessionID)
	now := time.Now().UTC()
	baseID := "evt-" + strconvNowNano()
	events := []event.RunEvent{
		event.NewRunEvent(baseID, runID, sessionID, typeName, payload, now),
	}
	if diffPayload, ok := turn.ExtractTurnDiffPayload(payload); ok {
		events = append(events, event.NewRunEvent(baseID+"-diff", runID, sessionID, event.DiffEventTurnUpdated, diffPayload, now))
	}
	return events
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func stringOr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	default:
		return ""
	}
}

func int64Field(v any, key string) int64 {
	asMap, ok := v.(map[string]any)
	if !ok {
		return 0
	}
	switch raw := asMap[key].(type) {
	case int:
		return int64(raw)
	case int32:
		return int64(raw)
	case int64:
		return raw
	case float32:
		return int64(raw)
	case float64:
		return int64(raw)
	default:
		return 0
	}
}

func approvalRequiresActionPayload(v any) any {
	asMap, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := make(map[string]any, len(asMap))
	for k, val := range asMap {
		if strings.EqualFold(strings.TrimSpace(k), "permission_suggestion") {
			continue
		}
		out[k] = val
	}
	return out
}

func approvalPermissionSuggestionPayload(v any) *event.PermissionSuggestionPayload {
	asMap, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := asMap["permission_suggestion"]
	if !ok {
		return nil
	}
	suggMap, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	out := &event.PermissionSuggestionPayload{
		PermissionToolName:   strings.TrimSpace(stringOr(suggMap["permission_tool_name"])),
		PermissionInput:      strings.TrimSpace(stringOr(suggMap["permission_input"])),
		ExactRuleContent:     strings.TrimSpace(stringOr(suggMap["exact_rule_content"])),
		PrefixRuleContent:    strings.TrimSpace(stringOr(suggMap["prefix_rule_content"])),
		SuggestedDestination: safety.PermissionDestination(strings.TrimSpace(stringOr(suggMap["suggested_destination"]))),
		PermissionMode:       safety.PermissionMode(strings.TrimSpace(stringOr(suggMap["permission_mode"]))),
		PermissionReason:     strings.TrimSpace(stringOr(suggMap["permission_reason"])),
		BypassSandbox:        boolField(suggMap, "bypass_sandbox"),
		OneShotOnly:          boolField(suggMap, "one_shot_only"),
	}
	if decisions, ok := suggMap["available_decisions"].([]any); ok {
		out.AvailableDecisions = append([]any(nil), decisions...)
	}
	if rawAmendment, ok := suggMap["proposed_execpolicy_amendment"]; ok {
		if encoded, err := json.Marshal(rawAmendment); err == nil {
			_ = json.Unmarshal(encoded, &out.ProposedExecPolicyAmendment)
		}
	}
	if rawAmendments, ok := suggMap["proposed_network_policy_amendments"]; ok {
		if encoded, err := json.Marshal(rawAmendments); err == nil {
			_ = json.Unmarshal(encoded, &out.ProposedNetworkPolicyAmendments)
		}
	}
	if rawContext, ok := suggMap["network_approval_context"]; ok {
		if encoded, err := json.Marshal(rawContext); err == nil {
			var context safety.NetworkApprovalContext
			if json.Unmarshal(encoded, &context) == nil && context.Protocol.Valid() && strings.TrimSpace(context.Host) != "" {
				out.NetworkApproval = &context
				out.NetworkPort = int(int64Field(suggMap, "network_port"))
			}
		}
	}
	switch rawOpts := suggMap["destination_options"].(type) {
	case []any:
		out.DestinationOptions = make([]safety.PermissionDestination, 0, len(rawOpts))
		for _, it := range rawOpts {
			if s := strings.TrimSpace(stringOr(it)); s != "" {
				out.DestinationOptions = append(out.DestinationOptions, safety.PermissionDestination(s))
			}
		}
	case []safety.PermissionDestination:
		out.DestinationOptions = append([]safety.PermissionDestination(nil), rawOpts...)
	}
	if out.PermissionToolName == "" &&
		out.PermissionInput == "" &&
		out.ExactRuleContent == "" &&
		out.PrefixRuleContent == "" &&
		len(out.DestinationOptions) == 0 &&
		out.SuggestedDestination == "" &&
		out.PermissionMode == "" &&
		out.PermissionReason == "" {
		return nil
	}
	return out
}

func float64OrZero(v any) float64 {
	switch raw := v.(type) {
	case float32:
		return float64(raw)
	case float64:
		return raw
	case int:
		return float64(raw)
	case int32:
		return float64(raw)
	case int64:
		return float64(raw)
	default:
		return 0
	}
}

func modeChangedPayload(v any) (event.ModeChangedPayload, bool) {
	asMap, ok := v.(map[string]any)
	if !ok {
		return event.ModeChangedPayload{}, false
	}
	return event.ModeChangedPayload{
		Mode:  strings.TrimSpace(stringOr(asMap["mode"])),
		Phase: strings.TrimSpace(stringOr(asMap["phase"])),
	}, true
}

func pendingInputUpdatedPayload(v any) (event.PendingInputUpdatedPayload, bool) {
	asMap, ok := v.(map[string]any)
	if !ok {
		return event.PendingInputUpdatedPayload{}, false
	}
	return event.PendingInputUpdatedPayload{
		PendingSteers:  stringSliceField(asMap, "pending_steers"),
		RejectedSteers: stringSliceField(asMap, "rejected_steers"),
		QueuedMessages: stringSliceField(asMap, "queued_messages"),
	}, true
}

func stringSliceField(v map[string]any, key string) []string {
	raw, ok := v[key]
	if !ok {
		return nil
	}
	switch list := raw.(type) {
	case []string:
		return append([]string(nil), list...)
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s := strings.TrimSpace(stringOr(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func tokenBudgetUpdatedPayload(v any) (event.TokenBudgetUpdatedPayload, bool) {
	asMap, ok := v.(map[string]any)
	if !ok {
		return event.TokenBudgetUpdatedPayload{}, false
	}
	return event.TokenBudgetUpdatedPayload{
		Model:                strings.TrimSpace(stringOr(asMap["model"])),
		TokenUsage:           int(int64Field(asMap, "token_usage")),
		PercentLeft:          int(int64Field(asMap, "percent_left")),
		ContextWindow:        int(int64Field(asMap, "context_window")),
		EffectiveWindow:      int(int64Field(asMap, "effective_window")),
		AutoCompactThreshold: int(int64Field(asMap, "auto_compact_threshold")),
	}, true
}

func boolField(v map[string]any, key string) bool {
	raw, ok := v[key]
	if !ok {
		return false
	}
	switch b := raw.(type) {
	case bool:
		return b
	case string:
		return strings.EqualFold(strings.TrimSpace(b), "true")
	default:
		return false
	}
}

func (s *Server) tokenBudgetWSMessageFromSession(ctx context.Context, requestID, runID, sessionID string) (wsServerMsg, bool) {
	payload, ok := s.tokenBudgetPayloadFromSession(ctx, sessionID)
	if !ok {
		return wsServerMsg{}, false
	}
	return wsServerMsg{
		Op:        event.CompactEventBudgetUpdated,
		RequestID: strings.TrimSpace(requestID),
		RunID:     strings.TrimSpace(runID),
		SessionID: strings.TrimSpace(sessionID),
		Data:      payload,
	}, true
}

// contextOccupancy is how much of the context window the conversation fills
// right now: the last API response's whole prompt. The web's context gauge and
// /status both read it — the same input the terminal's footer uses — so the
// numbers agree everywhere.
func (s *Server) contextOccupancy(ctx context.Context, sessionID string) (int, bool) {
	if s == nil || s.Sessions == nil {
		return 0, false
	}
	turns, err := s.Sessions.ListRecentMessages(ctx, sessionID, 400)
	if err != nil {
		return 0, false
	}
	return state.TokenCountFromLastAPIResponse(turns), true
}

func (s *Server) tokenBudgetPayloadFromSession(ctx context.Context, sessionID string) (event.TokenBudgetUpdatedPayload, bool) {
	usage, ok := s.contextOccupancy(ctx, sessionID)
	if !ok || usage <= 0 {
		return event.TokenBudgetUpdatedPayload{}, false
	}
	provider, model := run.PrimaryModel(s.Runner)
	limits, _ := llm.Lookup(provider, model)
	budget := state.CalculateTokenBudgetWithOptions(usage, model, limits, state.TokenBudgetOptions{ExplicitLimit: s.compactExplicitLimit()})
	return event.TokenBudgetUpdatedPayload{
		Model:                budget.Model,
		TokenUsage:           budget.TokenUsage,
		PercentLeft:          budget.PercentLeft,
		ContextWindow:        budget.ContextWindow,
		EffectiveWindow:      budget.EffectiveContextWindow,
		AutoCompactThreshold: budget.AutoCompactThreshold,
	}, true
}

func stepRunEventPayload(v any) (string, any, bool) {
	asMap, ok := v.(map[string]any)
	if !ok {
		return "", nil, false
	}
	kind := strings.TrimSpace(strings.ToLower(stringOr(asMap["kind"])))
	switch kind {
	case event.RunEventToolStarted:
		nested := parseNestedStepData(stringOr(asMap["data"]))
		return event.RunEventToolStarted, event.ToolCallStartedPayload{
			Kind:         kind,
			StepID:       strings.TrimSpace(stringOr(asMap["step_id"])),
			Description:  strings.TrimSpace(stringOr(asMap["description"])),
			ToolName:     strings.TrimSpace(stringOr(asMap["tool_name"])),
			CurrentQuery: stringOr(asMap["current_query"]),
			Input:        mapFieldWS(asMap, "input"),
			Summary:      strings.TrimSpace(stringOr(asMap["summary"])),
			ToolMeta:     toolCallMetaFromWS(asMap, nested),
		}, true
	case event.RunEventToolOutputDelta:
		nested := parseNestedStepData(stringOr(asMap["data"]))
		text := stringOr(asMap["content"])
		if strings.TrimSpace(text) == "" {
			text = stringOr(asMap["data"])
		}
		meta := toolCallMetaFromWS(asMap, nested)
		return event.RunEventToolOutputDelta, event.ToolCallOutputDeltaPayload{
			StepID:   strings.TrimSpace(stringOr(asMap["step_id"])),
			ToolName: strings.TrimSpace(stringOr(asMap["tool_name"])),
			Text:     text,
			AgentID:  meta.AgentID,
			Summary:  strings.TrimSpace(stringOr(asMap["summary"])),
			ToolMeta: meta,
		}, true
	case event.RunEventToolCompleted:
		nested := parseNestedStepData(stringOr(asMap["data"]))
		output := mapFieldWS(asMap, "output")
		if output == nil {
			output = mapFieldWS(nested, "output")
		}
		errorText := strings.TrimSpace(stringOr(asMap["error"]))
		if errorText == "" {
			errorText = strings.TrimSpace(stringOr(nested["error"]))
		}
		errorType := strings.TrimSpace(stringOr(asMap["error_type"]))
		if errorType == "" {
			errorType = strings.TrimSpace(stringOr(nested["error_type"]))
		}
		displayBody := stringOr(asMap["display_body"])
		if strings.TrimSpace(displayBody) == "" {
			displayBody = stringOr(nested["display_body"])
		}
		actionID := strings.TrimSpace(stringOr(asMap["action_id"]))
		if actionID == "" {
			actionID = strings.TrimSpace(stringOr(nested["action_id"]))
		}
		actionKind := strings.TrimSpace(stringOr(asMap["action_kind"]))
		if actionKind == "" {
			actionKind = strings.TrimSpace(stringOr(nested["action_kind"]))
		}
		return event.RunEventToolCompleted, event.ToolCallCompletedPayload{
			Kind:            kind,
			StepID:          strings.TrimSpace(stringOr(asMap["step_id"])),
			Description:     strings.TrimSpace(stringOr(asMap["description"])),
			DurationSeconds: float64OrZero(asMap["duration_seconds"]),
			Data:            stringOr(asMap["data"]),
			ToolName:        strings.TrimSpace(stringOr(asMap["tool_name"])),
			Output:          output,
			Error:           errorText,
			ErrorType:       errorType,
			DisplayBody:     displayBody,
			ActionID:        actionID,
			ActionKind:      actionKind,
			Summary:         strings.TrimSpace(stringOr(asMap["summary"])),
			ToolMeta:        toolCallMetaFromWS(asMap, nested),
		}, true
	default:
		return "", nil, false
	}
}

func parseNestedStepData(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func toolCallMetaFromWS(top map[string]any, nested map[string]any) event.ToolCallMeta {
	metaMap := mapFieldWS(top, "tool_meta")
	if metaMap == nil {
		metaMap = mapFieldWS(nested, "tool_meta")
	}
	meta := event.ToolCallMeta{}
	if metaMap != nil {
		meta.ToolName = strings.TrimSpace(stringOr(metaMap["tool_name"]))
		meta.Status = strings.TrimSpace(stringOr(metaMap["status"]))
		meta.Purpose = strings.TrimSpace(stringOr(metaMap["purpose"]))
		meta.Invocation = strings.TrimSpace(stringOr(metaMap["invocation"]))
		meta.Input = mapFieldWS(metaMap, "input")
		meta.AgentID = strings.TrimSpace(stringOr(metaMap["agent_id"]))
		meta.AgentType = strings.TrimSpace(stringOr(metaMap["agent_type"]))
		meta.AgentKind = strings.TrimSpace(stringOr(metaMap["agent_kind"]))
		meta.ResultLines = int(float64OrZero(metaMap["result_lines"]))
		meta.ResultOffset = int(float64OrZero(metaMap["result_offset"]))
	}
	if meta.ToolName == "" {
		meta.ToolName = strings.TrimSpace(stringOr(top["tool_name"]))
	}
	if meta.ToolName == "" {
		meta.ToolName = strings.TrimSpace(stringOr(nested["tool_name"]))
	}
	return meta
}

func normalizeWSMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

func mapFieldWS(v any, key string) map[string]any {
	base := normalizeWSMap(v)
	if base == nil {
		return nil
	}
	raw, ok := base[key]
	if !ok {
		return nil
	}
	return normalizeWSMap(raw)
}

func strconvNowNano() string {
	return strconv.FormatInt(time.Now().UnixNano(), 10)
}

// runEventBus carries the shared engine's run-event stream to the websocket
// connections watching the session it belongs to.
//
// This is the gateway's half of the wiring the terminal does in one line
// (`runner.Events = <its own surface>`): the terminal serves exactly one
// session, so it can point the runner straight at its renderer, while the
// gateway serves many connections at once and has to route by session. What
// flows through here is everything a subagent produces that no other channel
// carries — the spawn/end lifecycle, and the assistant text, reasoning, token
// usage and provider-executed web search of the agent itself — which is what
// lets the web surface show a subagent's work under that subagent instead of
// dissolving it into the primary conversation.
type runEventBus struct {
	mu    sync.RWMutex
	next  int64
	subs  map[int64]*runEventSubscription
	store *state.RunStore
}

func newRunEventBus(stores ...*state.RunStore) *runEventBus {
	var store *state.RunStore
	if len(stores) > 0 {
		store = stores[0]
	}
	return &runEventBus{subs: map[int64]*runEventSubscription{}, store: store}
}

// Publish implements event.Sink. It never blocks on a subscriber: delivery is
// a bounded enqueue, and a slow client reconnects from the durable cursor
// rather than stalling the producer or growing memory without bound.
func (b *runEventBus) Publish(ctx context.Context, evt event.RunEvent) error {
	if b == nil {
		return nil
	}
	session := strings.TrimSpace(evt.SessionID)
	if session == "" {
		// Unattributable to a session, so it cannot be routed to a connection
		// without being shown to every other session's user.
		return nil
	}
	if b.store != nil {
		persisted, err := b.store.AppendSessionEvent(ctx, sessionEventRecord(evt))
		switch {
		case errors.Is(err, state.ErrSessionNotStarted):
			// The conversation has not started: the event is delivered and
			// belongs to no session's history.
		case err != nil:
			return err
		default:
			evt = turn.RunEventFromRecord(persisted)
		}
	}
	b.mu.RLock()
	subs := make([]*runEventSubscription, 0, len(b.subs))
	for _, sub := range b.subs {
		subs = append(subs, sub)
	}
	b.mu.RUnlock()
	for _, sub := range subs {
		sub.deliver(session, evt)
	}
	return nil
}

func sessionEventRecord(evt event.RunEvent) state.SessionEvent {
	return state.SessionEvent{
		ID: evt.ID, RunID: evt.RunID, SessionID: evt.SessionID, Type: evt.Type,
		Payload: append(json.RawMessage(nil), evt.Payload...), CreatedAt: evt.CreatedAt,
	}
}

// Subscribe registers a connection. The subscription starts bound to no
// session and therefore receives nothing until Bind names one.
func (b *runEventBus) Subscribe(deliver func(event.RunEvent)) *runEventSubscription {
	if b == nil || deliver == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	sub := &runEventSubscription{bus: b, id: b.next, send: deliver}
	b.subs[sub.id] = sub
	return sub
}

type runEventSubscription struct {
	bus  *runEventBus
	id   int64
	send func(event.RunEvent)

	mu        sync.RWMutex
	sessionID string
	paused    bool
	buffer    []event.RunEvent
	overflow  bool
}

const runEventBufferedTailLimit = 4096

// Bind points the subscription at a session. A connection calls it whenever it
// binds or switches sessions, so events for the session the user is no longer
// looking at stop arriving.
func (sub *runEventSubscription) Bind(sessionID string) {
	if sub == nil {
		return
	}
	sub.mu.Lock()
	sub.sessionID = strings.TrimSpace(sessionID)
	sub.mu.Unlock()
}

// Unbind discards an incomplete snapshot tail and stops observation. A failed
// durable snapshot must not fall through into live delivery: doing so would
// present the suffix as if the missing prefix had been restored successfully.
func (sub *runEventSubscription) Unbind() {
	if sub == nil {
		return
	}
	sub.mu.Lock()
	sub.sessionID = ""
	sub.paused = false
	sub.buffer = nil
	sub.overflow = false
	sub.mu.Unlock()
}

// BindBuffered starts a gap-free snapshot+tail handoff. Publishers retain
// matching events until the caller has written a durable snapshot.
func (sub *runEventSubscription) BindBuffered(sessionID string) {
	if sub == nil {
		return
	}
	sub.mu.Lock()
	sub.sessionID = strings.TrimSpace(sessionID)
	sub.paused = true
	sub.buffer = nil
	sub.overflow = false
	sub.mu.Unlock()
}

// ResumeAfter emits the buffered tail strictly after highWater, then resumes
// direct delivery. Holding the lock through enqueue prevents a concurrent
// publisher from overtaking the tail.
func (sub *runEventSubscription) ResumeAfter(highWater int64) bool {
	if sub == nil {
		return false
	}
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.overflow {
		sub.buffer = nil
		sub.paused = false
		sub.overflow = false
		return false
	}
	sort.SliceStable(sub.buffer, func(i, j int) bool {
		return sub.buffer[i].Sequence < sub.buffer[j].Sequence
	})
	for _, evt := range sub.buffer {
		if evt.Sequence > highWater {
			sub.send(evt)
		}
	}
	sub.buffer = nil
	sub.paused = false
	return true
}

func (sub *runEventSubscription) deliver(sessionID string, evt event.RunEvent) {
	if sub == nil {
		return
	}
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.sessionID == "" || sub.sessionID != strings.TrimSpace(sessionID) {
		return
	}
	if sub.paused {
		if len(sub.buffer) >= runEventBufferedTailLimit {
			sub.overflow = true
			return
		}
		sub.buffer = append(sub.buffer, evt)
		return
	}
	sub.send(evt)
}

func (sub *runEventSubscription) Close() {
	if sub == nil || sub.bus == nil {
		return
	}
	sub.bus.mu.Lock()
	delete(sub.bus.subs, sub.id)
	sub.bus.mu.Unlock()
}
