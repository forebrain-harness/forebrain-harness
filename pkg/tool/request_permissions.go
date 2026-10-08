// The request_permissions tool and the action it raises.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

const requestPermissionsActionKind = "request_permissions"

type RequestPermissionsInput struct {
	EnvironmentID string                          `json:"environment_id,omitempty" jsonschema:"description=Optional primary environment id. Omit for the current environment."`
	Reason        string                          `json:"reason,omitempty" jsonschema:"description=Short reason the additional permissions are needed."`
	Permissions   safety.RequestPermissionProfile `json:"permissions"`
}

func (in *RequestPermissionsInput) UnmarshalJSON(data []byte) error {
	type wire RequestPermissionsInput
	var raw struct {
		wire
		EnvironmentIDAlias string `json:"environmentId"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*in = RequestPermissionsInput(raw.wire)
	if strings.TrimSpace(in.EnvironmentID) == "" {
		in.EnvironmentID = raw.EnvironmentIDAlias
	}
	return nil
}

func NewRequestPermissionsTool(st *State, rt *AgentToolRuntime) (*llm.Tool, error) {
	return llm.NewTool(
		requestPermissionsActionKind,
		"Use when additional filesystem or network permissions are required for later file or shell operations.",
		func(ctx context.Context, in *RequestPermissionsInput) (string, error) {
			if st == nil {
				return "", fmt.Errorf("nil state")
			}
			if in == nil {
				in = &RequestPermissionsInput{}
			}
			if err := st.GuardTool(ctx, requestPermissionsActionKind); err != nil {
				return "", err
			}
			if strings.TrimSpace(in.EnvironmentID) != "" && !strings.EqualFold(strings.TrimSpace(in.EnvironmentID), "primary") {
				return "", fmt.Errorf("unsupported environment_id %q: only the primary environment is available", in.EnvironmentID)
			}
			normalized, err := normalizeRequestPermissionsInput(ctx, st, in)
			if err != nil {
				return "", err
			}
			if _, denied := RequestPermissionsDeniedFromContext(ctx); denied {
				response := safety.RequestPermissionsResponse{Scope: safety.GrantScopeTurn}
				b, _ := json.Marshal(response)
				status, reason := requestPermissionsApprovalMeta(ctx)
				out := requestPermissionsCapturedOutput(response, status, reason)
				// A denial grants nothing, so the response carries no paths.
				// Surface the requested profile instead so the card can show
				// what was denied rather than an empty request.
				withRequestedPermissions(out, normalized)
				CaptureToolOutput(ctx, out)
				return string(b), nil
			}
			if ApprovedActionIDFromContext(ctx) == "" && !PolicyApprovedFromContext(ctx) {
				hook := st.ActionHook()
				if hook == nil {
					return "", fmt.Errorf("request_permissions requires an approval service")
				}
				payload := requestPermissionsPayload(ctx, normalized, "")
				id, ok, hookErr := hook(ctx, requestPermissionsActionKind, payload)
				if hookErr != nil {
					if errors.Is(hookErr, ErrRequestPermissionsDenied) {
						response := safety.RequestPermissionsResponse{Scope: safety.GrantScopeTurn}
						b, _ := json.Marshal(response)
						out := requestPermissionsCapturedOutput(response, "denied", "the automatic approval review denied the request")
						withRequestedPermissions(out, normalized)
						CaptureToolOutput(ctx, out)
						return string(b), nil
					}
					return "", hookErr
				}
				if ok && id != "" {
					CaptureToolRequiresAction(ctx, id, requestPermissionsActionKind, map[string]any{"requires_action": true})
					return "", &RequiresActionError{ActionID: id, ActionKind: requestPermissionsActionKind, ToolName: requestPermissionsActionKind, ToolInput: normalized}
				}
			}
			response, responseErr := responseForApprovedRequest(ctx, rt, normalized)
			if responseErr != nil {
				return "", responseErr
			}
			applyRequestPermissionsResponse(rt, response, ConversationSessionIDFromContext(ctx), RunIDFromContext(ctx))
			b, _ := json.Marshal(response)
			status, reason := requestPermissionsApprovalMeta(ctx)
			CaptureToolOutput(ctx, requestPermissionsCapturedOutput(response, status, reason))
			return string(b), nil
		},
	)
}

func normalizeRequestPermissionsInput(ctx context.Context, st *State, in *RequestPermissionsInput) (*RequestPermissionsInput, error) {
	if in == nil {
		return nil, fmt.Errorf("permissions required")
	}
	out := *in
	out.EnvironmentID = strings.TrimSpace(out.EnvironmentID)
	out.Reason = strings.TrimSpace(out.Reason)
	if out.Permissions.Network.IsEmpty() {
		out.Permissions.Network = nil
	}
	if out.Permissions.Network == nil && out.Permissions.FileSystem == nil {
		return nil, fmt.Errorf("empty permission request")
	}
	if out.Permissions.FileSystem != nil {
		cwd := requestPermissionsCWD(ctx)
		// A subagent type whose tool policy forbids both file-writing tools
		// cannot be handed write access by an approval either. The grant widens
		// the sandbox for that worker's shell, so honouring it would turn a
		// read-only type into a writing one behind a prompt that only says a
		// path was requested. Read and deny entries are untouched: asking to
		// read a directory outside the project is what this tool is for.
		readOnlyType := TypedSubagentBlocksFileWrites(SubagentTypeFromContext(ctx))
		fs := *out.Permissions.FileSystem
		entries := make([]safety.FileSystemPermissionEntry, 0, len(fs.Read)+len(fs.Write)+len(fs.Entries))
		for _, p := range fs.Read {
			entries = append(entries, safety.FileSystemPermissionEntry{Path: safety.NewFileSystemPermissionPath(p), Access: safety.FileSystemAccessRead})
		}
		for _, p := range fs.Write {
			entries = append(entries, safety.FileSystemPermissionEntry{Path: safety.NewFileSystemPermissionPath(p), Access: safety.FileSystemAccessWrite})
		}
		entries = append(entries, fs.Entries...)
		if fs.GlobScanMaxDepth != nil && *fs.GlobScanMaxDepth <= 0 {
			return nil, fmt.Errorf("glob_scan_max_depth must be greater than zero")
		}
		normalized := make([]safety.FileSystemPermissionEntry, 0, len(entries))
		seen := map[string]struct{}{}
		for _, entry := range entries {
			switch entry.Access {
			case safety.FileSystemAccessRead, safety.FileSystemAccessWrite, safety.FileSystemAccessDeny:
			default:
				return nil, fmt.Errorf("invalid filesystem permission access %q", entry.Access)
			}
			if entry.Access == safety.FileSystemAccessWrite && readOnlyType {
				return nil, fmt.Errorf(
					"subagent type %q cannot write files, so it cannot request write access; request read access instead",
					SubagentTypeFromContext(ctx),
				)
			}
			if entry.MissingPathBehavior != "" && entry.MissingPathBehavior != safety.FileSystemPermissionMissingPathSkip {
				return nil, fmt.Errorf("invalid missing_path_behavior %q", entry.MissingPathBehavior)
			}
			entry.Path.Type = permissionPathType(entry.Path)
			var isolationPath string
			switch entry.Path.Type {
			case safety.FileSystemPermissionPathTypePath:
				p, ok := entry.Path.Resolve(cwd)
				if !ok {
					return nil, fmt.Errorf("invalid filesystem permission path %q", entry.Path.Path)
				}
				entry.Path = safety.NewFileSystemPermissionPath(normalizeRootPath(p))
				isolationPath = entry.Path.Path
			case safety.FileSystemPermissionPathTypeGlobPattern:
				if entry.Access != safety.FileSystemAccessDeny {
					return nil, fmt.Errorf("glob file system permissions only support deny-read entries")
				}
				pattern := strings.TrimSpace(entry.Path.Pattern)
				if pattern == "" {
					return nil, fmt.Errorf("filesystem glob pattern required")
				}
				entry.Path = safety.FileSystemPermissionPath{Type: safety.FileSystemPermissionPathTypeGlobPattern, Pattern: pattern}
				resolvedPattern := pattern
				if !filepath.IsAbs(resolvedPattern) {
					resolvedPattern = filepath.Join(cwd, resolvedPattern)
				}
				isolationPath = globStaticRoot(resolvedPattern)
			case safety.FileSystemPermissionPathTypeSpecial:
				resolved, ok := entry.Path.Resolve(cwd)
				if ok {
					isolationPath = normalizeRootPath(resolved)
				}
			default:
				return nil, fmt.Errorf("invalid filesystem permission path type %q", entry.Path.Type)
			}
			if isolationPath != "" && st.PathOverlapsSiblingPrimaryWorkspace(isolationPath) {
				return nil, fmt.Errorf("path not allowed: primary agent workspace isolation blocks %s", isolationPath)
			}
			if isolationPath != "" && st.PathInsidePrimaryHome(isolationPath) && !st.PathUnderPrimaryWorkspace(isolationPath) {
				return nil, fmt.Errorf("path not allowed: only the active primary agent workspace is accessible under FOREBRAIN_HOME: %s", isolationPath)
			}
			key := string(entry.Access) + "\x00" + entry.Path.Key() + "\x00" + string(entry.MissingPathBehavior)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			normalized = append(normalized, entry)
		}
		fs.Read = nil
		fs.Write = nil
		fs.Entries = normalized
		if len(normalized) == 0 {
			out.Permissions.FileSystem = nil
			if out.Permissions.Network == nil {
				return nil, fmt.Errorf("empty permission request")
			}
			return &out, nil
		}
		out.Permissions.FileSystem = &fs
	}
	return &out, nil
}

func permissionPathType(path safety.FileSystemPermissionPath) safety.FileSystemPermissionPathType {
	if path.Type == "" && strings.TrimSpace(path.Path) != "" {
		return safety.FileSystemPermissionPathTypePath
	}
	return path.Type
}

func globStaticRoot(pattern string) string {
	pattern = filepath.Clean(strings.TrimSpace(pattern))
	index := strings.IndexAny(pattern, "*?[]")
	if index < 0 {
		return pattern
	}
	if index == 0 {
		return ""
	}
	prefix := pattern[:index]
	if strings.HasSuffix(prefix, string(filepath.Separator)) {
		return filepath.Clean(prefix)
	}
	return filepath.Dir(prefix)
}

func requestPermissionsCWD(ctx context.Context) string {
	if p := strings.TrimSpace(ProjectRootFromContext(ctx)); p != "" {
		if abs, err := filepath.Abs(p); err == nil {
			return filepath.Clean(abs)
		}
	}
	wd, err := os.Getwd()
	if err != nil || strings.TrimSpace(wd) == "" {
		return string(filepath.Separator)
	}
	abs, err := filepath.Abs(wd)
	if err != nil {
		return filepath.Clean(wd)
	}
	return filepath.Clean(abs)
}

func requestPermissionsPayload(ctx context.Context, in *RequestPermissionsInput, requestedBy string) map[string]any {
	payload := map[string]any{
		"request_permissions": true,
		"environment_id":      strings.TrimSpace(in.EnvironmentID),
		"reason":              strings.TrimSpace(in.Reason),
		"permissions":         in.Permissions,
		"cwd":                 requestPermissionsCWD(ctx),
	}
	if id := strings.TrimSpace(ToolUseIDFromContext(ctx)); id != "" {
		payload["originating_tool_call_id"] = id
	}
	if requestedBy = strings.TrimSpace(requestedBy); requestedBy != "" {
		payload["requested_by"] = requestedBy
	}
	return payload
}

func responseForApprovedRequest(ctx context.Context, rt *AgentToolRuntime, in *RequestPermissionsInput) (safety.RequestPermissionsResponse, error) {
	if rt != nil && rt.Actions != nil {
		if actionID := strings.TrimSpace(ApprovedActionIDFromContext(ctx)); actionID != "" {
			if action, err := rt.Actions.Get(ctx, actionID); err == nil && action != nil {
				if response, ok := RequestPermissionsResponseFromApprovedAction(action.Kind, action.PayloadJSON, action.AnswerJSON); ok {
					return response, nil
				}
				return safety.RequestPermissionsResponse{}, fmt.Errorf("approved request_permissions action contains an invalid grant response")
			}
		}
	}
	requested := safety.RequestPermissionsResponse{Permissions: in.Permissions, Scope: safety.GrantScopeTurn}
	return safety.IntersectRequestPermissionsResponseAtCWD(requested, requested, requestPermissionsCWD(ctx))
}

// requestPermissionsApprovalMeta resolves how the current invocation was
// authorized so user-facing surfaces can render the decision. Approval-exempt
// (auto-approved) calls carry the concrete policy condition that exempted
// them from an interactive approval prompt; denied calls carry the denial
// reason. status is one of "auto_approved", "approved", or "denied".
func requestPermissionsApprovalMeta(ctx context.Context) (status, reason string) {
	if denyReason, denied := RequestPermissionsDeniedFromContext(ctx); denied {
		if strings.TrimSpace(denyReason) == "" {
			return "denied", "denied by the permission policy"
		}
		return "denied", strings.TrimSpace(denyReason)
	}
	if ApprovedActionIDFromContext(ctx) != "" {
		if reason := PolicyApprovalReasonFromContext(ctx); reason != "" {
			return "approved", reason
		}
		return "approved", "the user approved this request in an approval prompt"
	}
	if reason := PolicyApprovalReasonFromContext(ctx); reason != "" {
		return "auto_approved", reason
	}
	if PolicyApprovedFromContext(ctx) {
		return "auto_approved", "approved by the permission policy"
	}
	return "approved", ""
}

// requestPermissionsCapturedOutput builds the tool-completion payload for step
// events, carrying the granted permissions plus the approval decision so the
// TUI can explain auto-approved (approval-exempt) and denied requests.
func requestPermissionsCapturedOutput(response safety.RequestPermissionsResponse, status, reason string) map[string]any {
	out := map[string]any{"permissions": response.Permissions, "scope": response.Scope}
	if status = strings.TrimSpace(status); status != "" {
		out["approval_status"] = status
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		out["approval_reason"] = reason
	}
	return out
}

// withRequestedPermissions copies the normalised input permissions into the
// output map so that denial cards can display which paths were requested even
// though the response itself grants nothing. It is a no-op when in is nil or
// carries no FileSystem profile.
func withRequestedPermissions(out map[string]any, in *RequestPermissionsInput) {
	if out == nil || in == nil || (in.Permissions.FileSystem == nil && in.Permissions.Network == nil) {
		return
	}
	out["permissions"] = in.Permissions
}

func applyRequestPermissionsResponse(rt *AgentToolRuntime, response safety.RequestPermissionsResponse, sessionID, runID string) {
	if rt == nil || rt.ApplyPermissionUpdate == nil {
		return
	}
	update := safety.PermissionUpdate{
		Type: safety.UpdateAddPermissionGrants, Destination: safety.DestinationSession,
		SessionID: strings.TrimSpace(sessionID),
	}
	if response.Permissions.FileSystem != nil {
		for _, entry := range response.Permissions.FileSystem.Entries {
			grant := safety.FileSystemPermissionGrant{
				Entry: entry, Scope: response.Scope,
				GlobScanMaxDepth: cloneIntPointer(response.Permissions.FileSystem.GlobScanMaxDepth),
			}
			if grant.Scope == safety.GrantScopeTurn {
				grant.RunID = strings.TrimSpace(runID)
			}
			update.FileSystemGrants = append(update.FileSystemGrants, grant)
		}
	}
	if response.Permissions.Network.AllowsNetwork() {
		grant := safety.NetworkPermissionGrant{Enabled: true, Scope: response.Scope}
		if grant.Scope == safety.GrantScopeTurn {
			grant.RunID = strings.TrimSpace(runID)
		}
		update.NetworkGrants = append(update.NetworkGrants, grant)
	}
	if response.StrictAutoReview && response.Scope == safety.GrantScopeTurn {
		update.StrictAutoReviewRunID = strings.TrimSpace(runID)
	}
	if len(update.FileSystemGrants) > 0 || len(update.NetworkGrants) > 0 {
		rt.ApplyPermissionUpdate(update)
	}
}

func ValidateRequestPermissionsResponseForAction(kind, payloadJSON string, response safety.RequestPermissionsResponse) (safety.RequestPermissionsResponse, error) {
	if !strings.EqualFold(strings.TrimSpace(kind), requestPermissionsActionKind) {
		return safety.RequestPermissionsResponse{}, fmt.Errorf("action kind %q is not request_permissions", kind)
	}
	var requested struct {
		Permissions safety.RequestPermissionProfile `json:"permissions"`
		CWD         string                          `json:"cwd"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(payloadJSON)), &requested) != nil ||
		requestPermissionProfileEmpty(requested.Permissions) || !filepath.IsAbs(strings.TrimSpace(requested.CWD)) {
		return safety.RequestPermissionsResponse{}, fmt.Errorf("invalid request_permissions action payload")
	}
	return safety.IntersectRequestPermissionsResponseAtCWD(
		safety.RequestPermissionsResponse{Permissions: requested.Permissions, Scope: safety.GrantScopeTurn},
		response,
		requested.CWD,
	)
}

func RequestPermissionsResponseFromApprovedAction(kind, payloadJSON, answerJSON string) (safety.RequestPermissionsResponse, bool) {
	if !strings.EqualFold(strings.TrimSpace(kind), requestPermissionsActionKind) {
		return safety.RequestPermissionsResponse{}, false
	}
	var requested struct {
		Permissions safety.RequestPermissionProfile `json:"permissions"`
		CWD         string                          `json:"cwd"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(payloadJSON)), &requested) != nil ||
		requestPermissionProfileEmpty(requested.Permissions) || !filepath.IsAbs(strings.TrimSpace(requested.CWD)) {
		return safety.RequestPermissionsResponse{}, false
	}
	var response safety.RequestPermissionsResponse
	if raw := strings.TrimSpace(answerJSON); raw != "" {
		if json.Unmarshal([]byte(raw), &response) != nil {
			return safety.RequestPermissionsResponse{}, false
		}
	} else {
		response = safety.RequestPermissionsResponse{
			Permissions: requested.Permissions,
			Scope:       safety.GrantScopeTurn,
		}
	}
	normalized, err := safety.IntersectRequestPermissionsResponseAtCWD(
		safety.RequestPermissionsResponse{Permissions: requested.Permissions, Scope: safety.GrantScopeTurn},
		response,
		requested.CWD,
	)
	return normalized, err == nil
}

// PermissionUpdateFromApprovedAction materializes an approved
// request_permissions action into runtime grants. Clients that
// support subset grants may place a RequestPermissionsResponse in answerJSON;
// otherwise approval grants the normalized request payload in full.
func PermissionUpdateFromApprovedAction(kind, payloadJSON, answerJSON, runID string) (safety.PermissionUpdate, bool) {
	if !strings.EqualFold(strings.TrimSpace(kind), requestPermissionsActionKind) {
		return safety.PermissionUpdate{}, false
	}
	response, ok := RequestPermissionsResponseFromApprovedAction(kind, payloadJSON, answerJSON)
	if !ok {
		return safety.PermissionUpdate{}, false
	}
	update := safety.PermissionUpdate{
		Type:        safety.UpdateAddPermissionGrants,
		Destination: safety.DestinationSession,
	}
	if response.Permissions.FileSystem != nil {
		for _, entry := range response.Permissions.FileSystem.Entries {
			if entry.Path.Key() == "" {
				continue
			}
			grant := safety.FileSystemPermissionGrant{
				Entry: entry, Scope: response.Scope,
				GlobScanMaxDepth: cloneIntPointer(response.Permissions.FileSystem.GlobScanMaxDepth),
			}
			if response.Scope == safety.GrantScopeTurn {
				grant.RunID = strings.TrimSpace(runID)
			}
			update.FileSystemGrants = append(update.FileSystemGrants, grant)
		}
	}
	if response.Permissions.Network.AllowsNetwork() {
		grant := safety.NetworkPermissionGrant{Enabled: true, Scope: response.Scope}
		if grant.Scope == safety.GrantScopeTurn {
			grant.RunID = strings.TrimSpace(runID)
		}
		update.NetworkGrants = append(update.NetworkGrants, grant)
	}
	if response.StrictAutoReview && response.Scope == safety.GrantScopeTurn {
		update.StrictAutoReviewRunID = strings.TrimSpace(runID)
	}
	if len(update.FileSystemGrants) == 0 && len(update.NetworkGrants) == 0 {
		return safety.PermissionUpdate{}, false
	}
	return update, true
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func requestPermissionProfileEmpty(profile safety.RequestPermissionProfile) bool {
	return profile.Network == nil && profile.FileSystem == nil
}

var ErrRequestPermissionsDenied = errors.New("request permissions denied")

type RequiresActionError struct {
	RunID                 string
	AgentID               string
	SubagentType          string
	ActionID              string
	ActionKind            string
	ToolName              string
	ToolInput             any
	OriginatingToolCallID string
	// SessionSnapshot is the LLM session at the moment the RAE fired. It
	// includes the assistant tool_use that triggered the approval gate and may
	// include tool_result messages for earlier calls from the same assistant
	// message that completed before the gate. On resume, the orchestration loop
	// preserves those completed results and only executes missing tool_calls.
	// nil for callers that do not participate in resume.
	SessionSnapshot []llm.Message
}

func (e *RequiresActionError) Error() string {
	if e == nil {
		return "requires action"
	}
	if e.ActionKind != "" {
		return fmt.Sprintf("requires action: %s (%s)", e.ActionID, e.ActionKind)
	}
	return fmt.Sprintf("requires action: %s", e.ActionID)
}

// ToolApprovalResumeState carries the saved orchestration state needed to
// resume a previously-paused LLM session after the user approved or denied
// the pending action. The orchestration loop consumes it once on entry and
// proceeds as if the assistant tool_use that triggered the pause had just
// been emitted.
type ToolApprovalResumeState struct {
	Session []llm.Message
	// Denied indicates the user rejected the tool call. When set, the
	// orchestration loop should inject denial tool results instead of
	// re-executing the pending tool calls.
	Denied bool
	// DenyReason carries the model-facing guidance when denying: the user's
	// feedback, and for a plan approval the reviews it collected (composed at
	// the resume, where the reviews still are). It is included in the denial
	// tool result message so the LLM sees the user's guidance.
	DenyReason string
	// DenyFeedback is the user's own words alone. The denial's display half —
	// what the live refusal card shows and the persisted tool result carries
	// as its tool_display body — states it rather than the model-facing
	// guidance, the way every other card separates what the user reads from
	// what the model is told.
	DenyFeedback string
	// DeliveredReview marks a denial that review delivery closed, not the
	// user: there are no user words to show, and the display half says the
	// handoff line instead of "(no output)".
	DeliveredReview bool
	// BeginContinuation atomically crosses the durable execution fence. It is
	// intentionally runtime-only: the wait row persists the phase, while the
	// callback binds this process's owner token to the exact continuation.
	BeginContinuation func(context.Context) error
	// EndContinuation closes that fence. The question the fence asks — "may
	// this tool call be replayed, or might it already have run?" — is answered
	// the moment the replay produces its results, so the fence is released
	// there rather than at the end of the turn: everything the model does
	// afterwards is ordinary work that the run lifecycle already governs, and
	// leaving the fence open across it made a process that exited mid-turn look
	// like a tool whose outcome nobody knows.
	//
	// It reports no error, unlike its opening half: the close is a durable
	// cleanup with nothing left to decide, and failing a turn that has already
	// executed its tool over it would be worse than letting the run's own
	// completion path clear the row.
	EndContinuation func(context.Context)
	beginOnce       sync.Once
	beginErr        error
	endOnce         sync.Once
}

type toolApprovalResumeKey struct{}

func WithToolApprovalResume(ctx context.Context, state *ToolApprovalResumeState) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if state == nil {
		return ctx
	}
	return context.WithValue(ctx, toolApprovalResumeKey{}, state)
}

func ToolApprovalResumeFromContext(ctx context.Context) *ToolApprovalResumeState {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(toolApprovalResumeKey{}).(*ToolApprovalResumeState)
	return v
}

// BeginApprovalContinuation invokes a resume fence at most once for this
// context. A nil callback is valid for legacy/in-memory-only callers.
func BeginApprovalContinuation(ctx context.Context) error {
	state := ToolApprovalResumeFromContext(ctx)
	if state == nil || state.BeginContinuation == nil {
		return nil
	}
	state.beginOnce.Do(func() {
		if state.BeginContinuation != nil {
			state.beginErr = state.BeginContinuation(ctx)
		}
	})
	return state.beginErr
}

// EndApprovalContinuation releases the resume fence at most once for this
// context. Like its opening half it is a no-op for callers that hold no
// durable continuation.
func EndApprovalContinuation(ctx context.Context) {
	state := ToolApprovalResumeFromContext(ctx)
	if state == nil || state.EndContinuation == nil {
		return
	}
	state.endOnce.Do(func() {
		if state.EndContinuation != nil {
			state.EndContinuation(ctx)
		}
	})
}

// ReplayResultEntry captures a single tool call result from a resume replay.
type ReplayResultEntry struct {
	ToolCallID  string
	ToolName    string
	Content     string
	Timing      llm.ExecutionTiming
	ToolDisplay *llm.ToolDisplayState
}

// ReplayResultCapture collects tool results produced during a resume replay
// so the caller (runResumeApprovedTurn) can persist them to the session store.
// Without this, the replayed tool result lives only inside the tool
// orchestration LLM's internal session and is discarded — leaving a dangling
// tool_calls row in the store that RepairDanglingToolResults later strips,
// causing the LLM to lose knowledge that the tool was called and approved.
type ReplayResultCapture struct {
	mu      sync.Mutex
	entries []ReplayResultEntry
}

func NewReplayResultCapture() *ReplayResultCapture {
	return &ReplayResultCapture{}
}

func (c *ReplayResultCapture) Add(entry ReplayResultEntry) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry.ToolDisplay != nil {
		display := *entry.ToolDisplay
		entry.ToolDisplay = &display
	}
	c.entries = append(c.entries, entry)
}

func (c *ReplayResultCapture) Entries() []ReplayResultEntry {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ReplayResultEntry, len(c.entries))
	copy(out, c.entries)
	for i := range out {
		if out[i].ToolDisplay != nil {
			display := *out[i].ToolDisplay
			out[i].ToolDisplay = &display
		}
	}
	return out
}

type replayResultCaptureKey struct{}

func WithReplayResultCapture(ctx context.Context, capture *ReplayResultCapture) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if capture == nil {
		return ctx
	}
	return context.WithValue(ctx, replayResultCaptureKey{}, capture)
}

func ReplayResultCaptureFromContext(ctx context.Context) *ReplayResultCapture {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(replayResultCaptureKey{}).(*ReplayResultCapture)
	return v
}
