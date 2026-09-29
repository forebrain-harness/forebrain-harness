// Tool notifications: the hook, its contract, and the notifier.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

// RunEventFromStep converts a user-visible tool boundary to the canonical
// event consumed by both live surfaces and resume replay. Keeping this next to
// the display normalizer prevents TUI and gateway from serializing different
// facts for the same tool call.
func RunEventFromStep(ctx context.Context, sessionID, runID, channel string, evt StepEvent) (event.RunEvent, bool) {
	evt = NormalizeToolStepForDisplay(evt)
	if !ToolStepUserVisible(evt) || strings.TrimSpace(evt.ToolName) == "" {
		return event.RunEvent{}, false
	}
	// A plan-emitting tool speaks through its plan update, which is projected
	// just below. Building a tool event for the same call as well would put the
	// raw result next to the card that already renders it.
	if ToolStepRendersAsPlan(evt) {
		return event.RunEvent{}, false
	}
	meta := EnrichToolMeta(ctx, BuildToolMeta(evt))
	eventMeta := event.ToolCallMeta{
		ToolName: meta.ToolName, Status: meta.Status, Purpose: meta.Purpose,
		Invocation: meta.Invocation, Input: meta.Input, AgentID: meta.AgentID,
		AgentType: meta.AgentType, AgentKind: meta.AgentKind, Category: meta.Category,
		SkillName: meta.SkillName, SkillPath: meta.SkillPath,
		ResultLines: meta.ResultLines, ResultOffset: meta.ResultOffset,
		StartedAtMs: meta.StartedAtMs,
		// Origin is audit metadata; it rides on the canonical payload but never
		// on ToolMeta, so no renderer can branch on it.
		Origin: strings.TrimSpace(evt.Origin),
	}
	stepID := strings.TrimSpace(evt.StepID)
	id := ""
	if stepID != "" && evt.Kind != event.RunEventToolOutputDelta {
		id = fmt.Sprintf("tool:%s:%s:%s%s", strings.TrimSpace(runID), stepID, strings.TrimSpace(evt.Kind), toolStepBoundary(ctx, evt))
	}
	var payload any
	switch evt.Kind {
	case event.RunEventPlanUpdated:
		// The plan update is the user-facing form of a session_todo call, and
		// the only one. Projecting it here is what carries a subagent's todo
		// list to the surfaces, which reach a child run's steps through the
		// canonical event stream and not through the parent's step ledger.
		if evt.PlanUpdate == nil {
			return event.RunEvent{}, false
		}
		plan := *evt.PlanUpdate
		if strings.TrimSpace(plan.AgentID) == "" {
			plan.AgentID = meta.AgentID
		}
		payload = plan
	case event.RunEventToolStarted:
		payload = event.ToolCallStartedPayload{
			Kind: evt.Kind, StepID: stepID, Description: "tool " + evt.ToolName,
			ToolName: evt.ToolName, Input: evt.Input,
			Summary: strings.TrimSpace(SummarizeToolStep(evt)), ToolMeta: eventMeta,
		}
	case event.RunEventToolOutputDelta:
		chunk, _ := evt.Output["chunk"].(string)
		if chunk == "" {
			return event.RunEvent{}, false
		}
		payload = event.ToolCallOutputDeltaPayload{
			StepID: stepID, ToolName: evt.ToolName, Text: chunk, AgentID: meta.AgentID,
			Summary: strings.TrimSpace(SummarizeToolStep(evt)), ToolMeta: eventMeta,
		}
	case event.RunEventToolCompleted:
		rendered, _ := BuildRenderedToolStepNotification(sessionID, runID, channel, evt)
		rendered.ToolMeta = meta
		body := strings.TrimSpace(rendered.DisplayBody)
		if body == "" {
			body, _ = FormatToolStepResult(evt, DefaultMaxFormattedBody)
			body = strings.TrimSpace(body)
		}
		data := ""
		if raw, err := json.Marshal(evt.Output); err == nil {
			data = string(raw)
		}
		payload = event.ToolCallCompletedPayload{
			Kind: evt.Kind, StepID: stepID, Description: "tool " + evt.ToolName,
			DurationSeconds: evt.Duration.Seconds(), Data: data, ToolName: evt.ToolName,
			Output: evt.Output, Error: evt.Error, ErrorType: event.ClassifyToolError(evt.Error),
			DisplayBody: body, ActionID: evt.ActionID, ActionKind: evt.ActionKind,
			RetainAsHistory: evt.RetainAsHistory,
			Summary:         strings.TrimSpace(SummarizeToolStep(evt)), ToolMeta: eventMeta,
		}
	default:
		return event.RunEvent{}, false
	}
	return event.NewRunEvent(id, strings.TrimSpace(runID), strings.TrimSpace(sessionID), strings.TrimSpace(evt.Kind), payload, time.Now().UTC()), true
}

// toolStepBoundary names which of a tool call's boundaries an event reports,
// so each of them gets an identity of its own.
//
// One logical call emits several events under a single step id whenever an
// approval stands between it and its result: the execution that ran before the
// gate is preserved as a historical attempt, the gate itself is a completion of
// its own, and the replay a granted approval starts produces a second
// started/completed pair. While all of them were "tool:<run>:<step>:<kind>",
// the session event log — which is idempotent by id so a reconnect may safely
// re-publish — kept the first and handed the first one's payload back for every
// later one. The call's real result was therefore never recorded, and each
// surface repainted the superseded attempt in its place, once per swallowed
// event.
//
// The name is built from what actually makes a boundary distinct: which
// execution it belongs to — unnamed for the call's first, named by the approval
// that authorized a replay — and, within that execution, the ordinal of an
// attempt that finished before the call itself did.
func toolStepBoundary(ctx context.Context, evt StepEvent) string {
	boundary := ""
	if approved := strings.TrimSpace(ApprovedActionIDFromContext(ctx)); approved != "" {
		boundary += ":approved-" + approved
	}
	if evt.Attempt > 0 {
		boundary += ":attempt-" + strconv.Itoa(evt.Attempt)
	}
	return boundary
}

func BuildToolStepHookInput(sessionID, runID, channel string, evt StepEvent) (NotificationHookInput, bool) {
	rendered, ok := BuildRenderedToolStepNotification(sessionID, runID, channel, evt)
	if !ok {
		return NotificationHookInput{}, false
	}
	return rendered.Input, true
}

func BuildRenderedToolStepNotification(sessionID, runID, channel string, evt StepEvent) (RenderedNotification, bool) {
	evt = NormalizeToolStepForDisplay(evt)
	if strings.TrimSpace(evt.ToolName) == "" {
		return RenderedNotification{}, false
	}
	if !ToolStepUserVisible(evt) {
		return RenderedNotification{}, false
	}
	if strings.TrimSpace(evt.Kind) == StepKindToolStarted {
		return RenderedNotification{}, false
	}
	if stepRequiresApproval(evt) {
		return RenderedNotification{}, false
	}
	displayBody, _ := FormatToolStepResult(evt, DefaultMaxFormattedBody)
	displayBody = strings.TrimSpace(displayBody)
	if displayBody == "" {
		if strings.EqualFold(strings.TrimSpace(evt.ToolName), "skill") && strings.TrimSpace(evt.Error) == "" {
			return RenderedNotification{}, false
		}
		displayBody = strings.TrimSpace(evt.Error)
	}
	typ := "tool_result"
	if strings.TrimSpace(evt.Error) != "" {
		typ = "tool_error"
	} else if strings.TrimSpace(evt.Kind) == StepKindToolStarted {
		typ = "tool_started"
	}
	input := NotificationHookInput{
		HookEventName:    "Notification",
		SessionID:        strings.TrimSpace(sessionID),
		RunID:            strings.TrimSpace(runID),
		Channel:          strings.TrimSpace(channel),
		Message:          displayBody,
		Title:            "Tool " + strings.TrimSpace(evt.ToolName),
		NotificationType: typ,
		ToolName:         strings.TrimSpace(evt.ToolName),
		ToolUseID:        strings.TrimSpace(evt.StepID),
	}
	// typ only ever carries tool_result/tool_error/tool_started, so this
	// notification is always transcript-role "tool".
	return RenderedNotification{
		Input:          input,
		DisplayBody:    displayBody,
		ToolMeta:       EnrichToolMeta(nil, BuildToolMeta(evt)),
		TranscriptRole: "tool",
	}, true
}

// ToolStepUserVisible is the shared user-delivery policy for ordinary tool
// lifecycle events. request_permissions is always user-visible: transcripts
// must show the permission request whether it needed an approval prompt or
// was auto-approved (the rendered body explains the exemption reason).
func ToolStepUserVisible(evt StepEvent) bool {
	if !evt.SuppressUI {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(evt.ToolName), "request_permissions") {
		return true
	}
	if strings.TrimSpace(evt.Error) != "" || strings.TrimSpace(evt.ActionID) != "" || strings.TrimSpace(evt.ActionKind) != "" {
		return true
	}
	requiresAction, _ := evt.Output["requires_action"].(bool)
	return requiresAction
}

type NotificationRequest struct {
	Key       string
	Message   string
	Title     string
	SessionID string
	RunID     string
	Channel   string
}

type notificationContract struct {
	Title string
	Type  string
}

var notificationContracts = map[string]notificationContract{
	"auto-mode-unavailable": {Title: "Auto Mode", Type: "auto_mode_unavailable"},
	"subscription-switch":   {Title: "Subscription", Type: "subscription_switch"},
	"deprecation":           {Title: "Deprecation", Type: "deprecation"},
	"fast-mode":             {Title: "Fast Mode", Type: "fast_mode"},
	"ide-status":            {Title: "IDE Status", Type: "ide_status"},
	"install-message":       {Title: "Installation", Type: "install_message"},
	"mcp-connectivity":      {Title: "MCP Connectivity", Type: "mcp_connectivity"},
	"model-migration":       {Title: "Model Migration", Type: "model_migration"},
	"plugin-autoupdate":     {Title: "Plugin Update", Type: "plugin_autoupdate"},
	"plugin-installation":   {Title: "Plugin Installation", Type: "plugin_installation"},
	"rate-limit-warning":    {Title: "Rate Limit", Type: "rate_limit_warning"},
	"settings-errors":       {Title: "Settings", Type: "settings_errors"},
	"startup":               {Title: "Startup", Type: "startup"},
	"task-created":          {Title: "Task Created", Type: "task_created"},
	"task-output":           {Title: "Task Output", Type: "task_output"},
	"task-stopped":          {Title: "Task Stopped", Type: "task_stopped"},
	"task-updated":          {Title: "Task Updated", Type: "task_updated"},
	"teammate-shutdown":     {Title: "Teammate", Type: "teammate_shutdown"},
	"update":                {Title: "Update", Type: "update"},
}

func BuildNotificationHookInput(req NotificationRequest) (NotificationHookInput, bool) {
	key := strings.ToLower(strings.TrimSpace(req.Key))
	contract, ok := notificationContracts[key]
	if !ok {
		return NotificationHookInput{}, false
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		return NotificationHookInput{}, false
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = contract.Title
	}
	return NotificationHookInput{
		HookEventName:    "Notification",
		SessionID:        strings.TrimSpace(req.SessionID),
		RunID:            strings.TrimSpace(req.RunID),
		Channel:          strings.TrimSpace(req.Channel),
		Message:          msg,
		Title:            title,
		NotificationType: contract.Type,
	}, true
}

func NotifyHook(ctx context.Context, hook NotificationHook, req NotificationRequest) error {
	if hook == nil {
		return nil
	}
	in, ok := BuildNotificationHookInput(req)
	if !ok {
		return nil
	}
	return hook.Notify(ctx, in)
}

type ToolMeta struct {
	ToolName   string         `json:"tool_name,omitempty"`
	Status     string         `json:"status,omitempty"`
	Purpose    string         `json:"purpose,omitempty"`
	Invocation string         `json:"invocation,omitempty"`
	Input      map[string]any `json:"input,omitempty"`
	AgentID    string         `json:"agent_id,omitempty"`
	AgentType  string         `json:"agent_type,omitempty"`
	AgentKind  string         `json:"agent_kind,omitempty"`
	Category   string         `json:"category,omitempty"`
	SkillName  string         `json:"skill_name,omitempty"`
	SkillPath  string         `json:"skill_path,omitempty"`
	// ResultLines and ResultOffset are the engine-observed extent of a
	// completed paging read (read_file): how many logical content lines the
	// result actually holds and the 0-based file line it starts at. Input
	// records what the caller asked for, whose limit may exceed the page the
	// engine returned (the 200-line ceiling); every surface that shows a line
	// count or a line range derives it from these facts, never from
	// re-counting display rows, so the terminal's line wrapping can never
	// inflate them.
	ResultLines  int `json:"result_lines,omitempty"`
	ResultOffset int `json:"result_offset,omitempty"`
	// StartedAtMs is when a still-executing call began, in Unix milliseconds;
	// zero once the call has settled or when the engine did not observe the
	// start. Every surface derives "running for N seconds" from it.
	StartedAtMs int64 `json:"started_at_ms,omitempty"`
}

// RunningFor reports how long an executing call has been running as of now,
// and false when the meta carries no start time.
func (m ToolMeta) RunningFor(now time.Time) (time.Duration, bool) {
	if m.Status != "running" || m.StartedAtMs <= 0 {
		return 0, false
	}
	d := now.Sub(time.UnixMilli(m.StartedAtMs))
	if d < 0 {
		d = 0
	}
	return d, true
}

type NotificationHookInput struct {
	HookEventName    string `json:"hook_event_name"`
	SessionID        string `json:"session_id,omitempty"`
	RunID            string `json:"run_id,omitempty"`
	Channel          string `json:"channel,omitempty"`
	Message          string `json:"message"`
	Title            string `json:"title,omitempty"`
	NotificationType string `json:"notification_type"`
	ToolName         string `json:"tool_name,omitempty"`
	ToolUseID        string `json:"tool_use_id,omitempty"`
}

type RenderedNotification struct {
	Input          NotificationHookInput
	DisplayBody    string
	ToolMeta       ToolMeta
	TranscriptRole string
}

type NotificationHook interface {
	Notify(ctx context.Context, in NotificationHookInput) error
}

type FuncNotificationHook func(ctx context.Context, in NotificationHookInput) error

func (f FuncNotificationHook) Notify(ctx context.Context, in NotificationHookInput) error {
	if f == nil {
		return nil
	}
	return f(ctx, in)
}
