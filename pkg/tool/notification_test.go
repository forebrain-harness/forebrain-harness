package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

func TestBuildNotificationHookInputUsesStableContract(t *testing.T) {
	in, ok := BuildNotificationHookInput(NotificationRequest{
		Key:       "mcp-connectivity",
		Message:   "server disconnected",
		SessionID: "sess",
		RunID:     "run",
		Channel:   "gateway",
	})
	if !ok {
		t.Fatalf("expected notification")
	}
	if in.HookEventName != "Notification" {
		t.Fatalf("hook event=%q want Notification", in.HookEventName)
	}
	if in.NotificationType != "mcp_connectivity" {
		t.Fatalf("type=%q want mcp_connectivity", in.NotificationType)
	}
	if in.Title == "" || in.Message != "server disconnected" {
		t.Fatalf("unexpected input: %+v", in)
	}
}

func TestBuildNotificationHookInputDropsUnknownOrEmptyNotifications(t *testing.T) {
	if _, ok := BuildNotificationHookInput(NotificationRequest{Key: "unknown", Message: "x"}); ok {
		t.Fatalf("unknown key should be dropped")
	}
	if _, ok := BuildNotificationHookInput(NotificationRequest{Key: "startup"}); ok {
		t.Fatalf("empty message should be dropped")
	}
}

func TestNotifyToolStepHookBuildsNotificationPayload(t *testing.T) {
	var got NotificationHookInput
	hook := FuncNotificationHook(func(ctx context.Context, in NotificationHookInput) error {
		got = in
		return nil
	})
	in, ok := BuildToolStepHookInput("s1", "r1", "webchat", StepEvent{
		Kind:     event.RunEventToolCompleted,
		StepID:   "tool-1",
		ToolName: "read_file",
		Output:   map[string]any{"lines": 2},
	})
	if !ok {
		t.Fatal("expected tool step input")
	}
	if err := hook.Notify(context.Background(), in); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got.HookEventName != "Notification" || got.NotificationType != "tool_result" {
		t.Fatalf("unexpected hook identity: %+v", got)
	}
	if got.SessionID != "s1" || got.RunID != "r1" || got.Channel != "webchat" {
		t.Fatalf("missing runtime identity: %+v", got)
	}
	if got.ToolName != "read_file" || got.ToolUseID != "tool-1" || got.Message == "" {
		t.Fatalf("missing tool payload: %+v", got)
	}
}

func TestNotifyToolStepHookClassifiesErrors(t *testing.T) {
	var got NotificationHookInput
	hook := FuncNotificationHook(func(ctx context.Context, in NotificationHookInput) error {
		got = in
		return nil
	})
	in, ok := BuildToolStepHookInput("", "", "", StepEvent{ToolName: "shell", Error: "permission denied"})
	if !ok {
		t.Fatal("expected tool error input")
	}
	if err := hook.Notify(context.Background(), in); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got.NotificationType != "tool_error" {
		t.Fatalf("notification_type=%q want tool_error", got.NotificationType)
	}
}

func TestBuildToolStepHookInputRejectsMissingToolName(t *testing.T) {
	if _, ok := BuildToolStepHookInput("s1", "r1", "tui", StepEvent{}); ok {
		t.Fatal("expected empty tool name to be rejected")
	}
}

func TestBuildToolStepHookInputSurfacesRequestPermissions(t *testing.T) {
	evt := StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "request_permissions",
		Input:    map[string]any{"reason": "inspect external files"},
		Output: map[string]any{
			"scope":           "session",
			"permissions":     map[string]any{"file_system": map[string]any{"entries": []map[string]any{{"path": map[string]any{"type": "path", "path": "/tmp/external"}, "access": "read"}}}},
			"approval_status": "auto_approved",
			"approval_reason": "matched allow rule `request_permissions` (user settings)",
		},
	}
	if !ToolStepUserVisible(evt) {
		t.Fatal("request_permissions lifecycle must be user-visible")
	}
	in, ok := BuildToolStepHookInput("s1", "r1", "tui", evt)
	if !ok {
		t.Fatal("request_permissions must reach notification hooks")
	}
	if !strings.Contains(in.Message, "auto-approved") {
		t.Fatalf("hook message must explain the auto-approval: %q", in.Message)
	}
	if !strings.Contains(in.Message, "matched allow rule") {
		t.Fatalf("hook message must name the exemption condition: %q", in.Message)
	}
	if !strings.Contains(in.Message, "/tmp/external") {
		t.Fatalf("hook message must list the requested paths: %q", in.Message)
	}
}

func TestBuildRenderedToolStepNotificationRequestPermissionsSuppressUIStillVisible(t *testing.T) {
	evt := StepEvent{
		Kind:       StepKindToolCompleted,
		ToolName:   "request_permissions",
		SuppressUI: true,
		Output:     map[string]any{"approval_status": "approved", "approval_reason": "the user approved this request in an approval prompt"},
	}
	if !ToolStepUserVisible(evt) {
		t.Fatal("request_permissions must stay user-visible even when SuppressUI is set")
	}
}

func TestBuildRenderedToolStepNotificationCarriesDisplayAndRole(t *testing.T) {
	rendered, ok := BuildRenderedToolStepNotification("s1", "r1", "webchat", StepEvent{
		Kind:     event.RunEventToolCompleted,
		StepID:   "tool-1",
		ToolName: "read_file",
		Input:    map[string]any{"file_path": "a.go"},
		Output:   map[string]any{"preview_text": "1|package main", "preview_kind": "file"},
	})
	if !ok {
		t.Fatal("expected rendered notification")
	}
	if rendered.TranscriptRole != "tool" {
		t.Fatalf("transcript role=%q want tool", rendered.TranscriptRole)
	}
	if strings.TrimSpace(rendered.DisplayBody) == "" {
		t.Fatal("expected display body")
	}
	if rendered.Input.Message != rendered.DisplayBody {
		t.Fatalf("hook message should match display body: %#v", rendered)
	}
	if strings.Contains(rendered.DisplayBody, "tool use id:") {
		t.Fatalf("display body must not expose tool use id: %q", rendered.DisplayBody)
	}
	if strings.Contains(rendered.Input.Message, "tool use id: `tool-1`") {
		t.Fatalf("display/hook body must not expose tool use id: %q", rendered.Input.Message)
	}
}

func TestBuildRenderedToolStepNotificationSuppressesStartedAndAwaitingApprovalBodies(t *testing.T) {
	if _, ok := BuildRenderedToolStepNotification("s1", "r1", "tui", StepEvent{
		Kind:     StepKindToolStarted,
		ToolName: "shell",
		Input:    map[string]any{"command": "git status --porcelain=v1 -b"},
	}); ok {
		t.Fatal("expected started notification body to be suppressed")
	}
	if _, ok := BuildRenderedToolStepNotification("s1", "r1", "tui", StepEvent{
		Kind:       StepKindToolCompleted,
		ToolName:   "shell",
		ActionID:   "act-1",
		ActionKind: "shell",
		Input:      map[string]any{"command": "git status --porcelain=v1 -b"},
		Output:     map[string]any{"requires_action": true},
	}); ok {
		t.Fatal("expected awaiting-approval notification body to be suppressed")
	}
	if _, ok := BuildRenderedToolStepNotification("s1", "r1", "tui", StepEvent{
		Kind:     StepKindToolCompleted,
		ToolName: "review",
		Input:    map[string]any{},
		Output: map[string]any{
			"output": "# Review\nbody",
		},
	}); !ok {
		t.Fatal("expected review skill completion notification body to be rendered without suppression override")
	}
}

// A paging read's extent is an engine fact, and the canonical event is the
// only carrier a replaying surface has. Losing it there left a subagent's
// read_file card with no lines range — the call carried no offset/limit to
// derive one from, so the header could not describe the page it returned.
func TestCompletedRunEventCarriesReadFilePagingFacts(t *testing.T) {
	ctx := WithHookAgentID(context.Background(), "subagent-1")
	evt, ok := RunEventFromStep(ctx, "s1", "run-child", "tui", StepEvent{
		Kind: StepKindToolCompleted, StepID: "call-1", ToolName: "read_file",
		Input: map[string]any{"file_path": "pkg/tui/render.go"},
		Output: map[string]any{
			"result_lines": 200,
			"offset":       0,
			"preview_kind": "file",
		},
	})
	if !ok {
		t.Fatal("a completed boundary must project into a run event")
	}
	var payload event.ToolCallCompletedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		t.Fatalf("unmarshal completed payload: %v", err)
	}
	if payload.ToolMeta.ResultLines != 200 || payload.ToolMeta.ResultOffset != 0 {
		t.Fatalf("paging facts = (%d, %d), want (200, 0)", payload.ToolMeta.ResultLines, payload.ToolMeta.ResultOffset)
	}
}

func TestShellNotificationUsesShellIdentity(t *testing.T) {
	evt := StepEvent{
		Kind: StepKindToolCompleted, StepID: "step-1", ToolName: "shell",
		Input:  map[string]any{"command": "git status --short"},
		Output: map[string]any{"stdout": "ok\n", "exit_code": 0},
	}
	rendered, ok := BuildRenderedToolStepNotification("s", "r", "tui", evt)
	if !ok {
		t.Fatal("expected rendered notification")
	}
	if rendered.Input.ToolName != "shell" || rendered.ToolMeta.ToolName != "shell" {
		t.Fatalf("shell identity changed in notification: %+v", rendered)
	}
}

// A streaming chunk is a boundary of a call that has not finished, and the
// canonical event is the only thing a surface reading this stream has to go on.
// Serializing the chunk alone left the surface to invent the rest, so the
// subagent view painted a finished, nameless card over live output.
func TestOutputDeltaEventReportsTheCallStillRunning(t *testing.T) {
	ctx := WithHookAgentID(context.Background(), "subagent-1")
	evt, ok := RunEventFromStep(ctx, "s1", "run-child", "tui", StepEvent{
		Kind: event.RunEventToolOutputDelta, StepID: "call-1", ToolName: "shell",
		Input:  map[string]any{"command": "node scripts/build-all.mjs"},
		Output: map[string]any{"chunk": "[build-all] tsdown\n"},
	})
	if !ok {
		t.Fatal("a streaming boundary must project into a run event")
	}
	var payload event.ToolCallOutputDeltaPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		t.Fatalf("unmarshal delta payload: %v", err)
	}
	if payload.ToolMeta.Status != "running" {
		t.Fatalf("delta status = %q, want running", payload.ToolMeta.Status)
	}
	if got := stringFromAny(payload.ToolMeta.Input["command"]); got != "node scripts/build-all.mjs" {
		t.Fatalf("delta lost the command it is running: %q", got)
	}
	if payload.ToolMeta.AgentID != "subagent-1" || payload.AgentID != "subagent-1" {
		t.Fatalf("delta lost its agent: %+v", payload)
	}
	if payload.Summary != "running node scripts/build-all.mjs" {
		t.Fatalf("delta summary = %q", payload.Summary)
	}
}

func TestBuildToolMetaCarriesStartOnlyWhileRunning(t *testing.T) {
	start := time.Now().Add(-90 * time.Second)
	run := BuildToolMeta(StepEvent{Kind: StepKindToolOutputDelta, StepID: "s2", ToolName: "shell", StartedAt: start})
	d, ok := run.RunningFor(time.Now())
	if !ok || d < 89*time.Second {
		t.Fatalf("running meta lacks elapsed: %v %v", d, ok)
	}
	done := BuildToolMeta(StepEvent{Kind: StepKindToolCompleted, StepID: "s2", ToolName: "shell", StartedAt: start})
	if done.StartedAtMs != 0 {
		t.Fatalf("settled meta still carries a start: %d", done.StartedAtMs)
	}
}

// TestRunEventFromStepCarriesTheRunningStart pins that the canonical event —
// what the web, a subagent's view and a reload read — carries the start as
// well, not only the TUI's direct step hook.
func TestRunEventFromStepCarriesTheRunningStart(t *testing.T) {
	start := time.Now().Add(-30 * time.Second)
	evt, ok := RunEventFromStep(context.Background(), "sess", "run", "tui", StepEvent{
		Kind: StepKindToolStarted, StepID: "s3", ToolName: "shell",
		Input: map[string]any{"command": "sleep 60"}, StartedAt: start,
	})
	if !ok {
		t.Fatal("started step produced no run event")
	}
	var payload event.ToolCallStartedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		t.Fatalf("decode started payload: %v", err)
	}
	if payload.ToolMeta.StartedAtMs != start.UnixMilli() {
		t.Fatalf("canonical started_at_ms = %d, want %d", payload.ToolMeta.StartedAtMs, start.UnixMilli())
	}
}
