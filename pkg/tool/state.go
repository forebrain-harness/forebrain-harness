// Tool runtime state: registry, context values, forks, and path helpers.
package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/invopop/jsonschema"
)

const (
	StepKindToolStarted           = event.RunEventToolStarted
	StepKindToolOutputDelta       = event.RunEventToolOutputDelta
	StepKindToolCompleted         = event.RunEventToolCompleted
	StepKindToolParallelStarted   = "tool_parallel_started"
	StepKindToolParallelCompleted = "tool_parallel_completed"
)

type ReadState struct {
	AbsPath   string
	ModTime   time.Time
	Size      int64
	SHA256Hex string
	ReadAt    time.Time
	TouchKind string
}

type ToolResultSpill struct {
	SessionID     string    `json:"session_id,omitempty"`
	RunID         string    `json:"run_id,omitempty"`
	ToolName      string    `json:"tool_name,omitempty"`
	CallID        string    `json:"call_id,omitempty"`
	Path          string    `json:"path,omitempty"`
	OriginalBytes int       `json:"original_bytes,omitempty"`
	OmittedBytes  int       `json:"omitted_bytes,omitempty"`
	TotalLines    int       `json:"total_lines,omitempty"`
	CreatedAt     time.Time `json:"created_at_utc,omitempty"`
}

type ContextTimelineEntry struct {
	Kind          string    `json:"kind,omitempty"`
	SessionID     string    `json:"session_id,omitempty"`
	RunID         string    `json:"run_id,omitempty"`
	CreatedAt     time.Time `json:"created_at_utc,omitempty"`
	Trigger       string    `json:"trigger,omitempty"`
	Strategy      string    `json:"strategy,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	BoundaryID    string    `json:"boundary_id,omitempty"`
	ReplacedItems int       `json:"replaced_items,omitempty"`
	WindowNumber  int       `json:"window_number,omitempty"`
	Summary       string    `json:"summary,omitempty"`
	SummarySource string    `json:"summary_source,omitempty"`
	Scope         string    `json:"scope,omitempty"`
	TokensBefore  int       `json:"tokens_before,omitempty"`
	TokensAfter   int       `json:"tokens_after,omitempty"`
	Reactive      bool      `json:"reactive,omitempty"`
	ToolName      string    `json:"tool_name,omitempty"`
	CallID        string    `json:"call_id,omitempty"`
	Path          string    `json:"path,omitempty"`
	OriginalBytes int       `json:"original_bytes,omitempty"`
	OmittedBytes  int       `json:"omitted_bytes,omitempty"`
	TotalLines    int       `json:"total_lines,omitempty"`
}

type State struct {
	mu                      sync.Mutex
	readFiles               map[string]ReadState
	toolMetas               map[string]event.ToolMeta
	loadedSkills            map[string]LoadedSkill
	contextShots            map[string]json.RawMessage
	toolSpills              map[string][]ToolResultSpill
	sessionSpills           map[string][]ToolResultSpill
	sessionRuntimeModes     map[string]runtimeSessionMode
	networkSessionDecisions map[string]map[networkSessionKey]bool
	workingSetPin           map[string][]string
	// toolMiddlewares wrap every tool this state registers. They belong to the
	// runtime that built them, which is why they are here and not in a package
	// variable every runtime in the process would share.
	toolMiddlewares              []llm.ToolMiddleware
	inProgressTools              map[string]struct{}
	roots                        []string
	confinedRoot                 string
	primaryHomeRoot              string
	approvedWrites               map[string]time.Time
	primaryWorkspaceRoot         string
	siblingPrimaryWorkspaceRoots []string
	denyRead                     []string
	allowRead                    []string
	additionalReadRoots          []string
	allowWrite                   []string
	denyReadPatterns             []safety.FileSystemPermissionPath
	readPolicyCWD                string
	historyDir                   string
	toolResultDir                string
	runtimeValues                map[string]any
	actionHook                   func(ctx context.Context, kind string, payload any) (actionID string, ok bool, err error)
	networkApprovalPromptHook    func(ctx context.Context, actionID string, payload map[string]any, request safety.NetworkApprovalRequest) (safety.NetworkApprovalDecision, error)
	subagentApprovalHook         SubagentApprovalHook
	stepHook                     func(ctx context.Context, evt StepEvent)
	readObserver                 func(ctx context.Context, absPath string, content []byte)
}

// runtimeSessionMode is a mode transition a tool performed during a run, held
// only until that run ends. See State.SetRuntimeSessionMode.
type runtimeSessionMode struct {
	mode     string
	planPath string
	runID    string
}

type ctxKey string

const (
	ctxKeyRunID                ctxKey = "forebrain_run_id"
	ctxKeyConversationSession  ctxKey = "forebrain_conversation_session_id"
	ctxKeyApprovedActionID     ctxKey = "forebrain_approved_action_id"
	ctxKeyMode                 ctxKey = "forebrain_mode"
	ctxKeyAllowedPlanPath      ctxKey = "forebrain_allowed_plan_path"
	ctxKeyForkChild            ctxKey = "forebrain_subagent_fork_child"
	ctxKeySubagentType         ctxKey = "forebrain_subagent_type"
	ctxKeySubagentDefSrc       ctxKey = "forebrain_subagent_definition_source"
	ctxKeyProjectRoot          ctxKey = "forebrain_project_root"
	ctxKeyWorkspaceRoot        ctxKey = "forebrain_workspace_root"
	ctxKeyProjectKey           ctxKey = "forebrain_project_key"
	ctxKeyToolUseID            ctxKey = "forebrain_tool_use_id"
	ctxKeyToolStepID           ctxKey = "forebrain_tool_step_id"
	ctxKeyPolicyApproved       ctxKey = "forebrain_policy_approved"
	ctxKeySandboxBypass        ctxKey = "forebrain_sandbox_bypass_approved"
	ctxKeyNetworkAccess        ctxKey = "forebrain_network_access_approved"
	ctxKeyHookAgentID          ctxKey = "forebrain_hook_agent_id"
	ctxKeyHookTranscript       ctxKey = "forebrain_hook_transcript_path"
	ctxKeySystemAddendum       ctxKey = "forebrain_system_addendum"
	ctxKeyToolCompletion       ctxKey = "forebrain_tool_completion_capture"
	ctxKeyRequestPermDeny      ctxKey = "forebrain_request_permissions_denied"
	ctxKeyPolicyApprovalReason ctxKey = "forebrain_policy_approval_reason_capture"
	ctxKeyStepHook             ctxKey = "forebrain_step_hook"
	ctxKeyNetworkApprovalHook  ctxKey = "forebrain_network_approval_hook"
	ctxKeySubagentApprovalHook ctxKey = "forebrain_subagent_approval_hook"
)

type StepEvent struct {
	Kind            string                    `json:"kind"`
	StepID          string                    `json:"step_id,omitempty"`
	ToolName        string                    `json:"tool_name,omitempty"`
	ToolDescription string                    `json:"tool_description,omitempty"`
	Input           map[string]any            `json:"input,omitempty"`
	Output          map[string]any            `json:"output,omitempty"`
	Error           string                    `json:"error,omitempty"`
	ActionID        string                    `json:"action_id,omitempty"`
	ActionKind      string                    `json:"action_kind,omitempty"`
	Duration        time.Duration             `json:"duration,omitempty"`
	PlanUpdate      *event.PlanUpdatedPayload `json:"plan_update,omitempty"`
	SuppressUI      bool                      `json:"suppress_ui,omitempty"`
	// RetainAsHistory marks a completed execution attempt that must remain in
	// the transcript when another lifecycle event reuses the same StepID. Shell
	// commands use this when a sandboxed attempt finishes before an approval-gated
	// retry starts.
	RetainAsHistory bool `json:"retain_as_history,omitempty"`
	// Attempt numbers an execution that finished before the logical call did:
	// 1 for the first such attempt, 0 for the call's own completion. It is what
	// separates a preserved attempt's event identity from the completion that
	// supersedes it, both of which report the same StepID.
	Attempt         int  `json:"attempt,omitempty"`
	ExternalContext bool `json:"external_context,omitempty"`
	// SkillName, SkillPath and Category identify a step that loads a skill:
	// the model's own load — the skill tool, or a read_file of a loaded skill's
	// root SKILL.md — or an explicit user invocation preloaded by the runner.
	// They are display metadata; the file contents still travel through the
	// ordinary tool result, and skill activation bodies never ride on the event.
	SkillName string `json:"skill_name,omitempty"`
	SkillPath string `json:"skill_path,omitempty"`
	Category  string `json:"category,omitempty"`
	// Origin records which chain produced the skill step ("explicit" for a
	// user-selected skill, "llm-load" for one the model loaded itself). It is
	// audit metadata: it is projected into the canonical event payload and the
	// run ledger, never into ToolMeta, and no rendering layer may read it.
	Origin string `json:"origin,omitempty"`
}

type ToolCompletionPayload struct {
	Output     map[string]any `json:"output,omitempty"`
	Error      string         `json:"error,omitempty"`
	ActionID   string         `json:"action_id,omitempty"`
	ActionKind string         `json:"action_kind,omitempty"`
	Duration   time.Duration  `json:"duration,omitempty"`
}

type toolCompletionCapture struct {
	payload  *ToolCompletionPayload
	attempts []ToolCompletionPayload
}

type networkSessionKey struct {
	Host     string
	Protocol safety.NetworkApprovalProtocol
	Port     int
}

func WithRunID(ctx context.Context, runID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeyRunID, runID)
}

func RunIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxKeyRunID).(string)
	return v
}

// WithConversationSessionID records the user-visible conversation separately
// from a subagent's worker transcript session. Permission grants and action
// ownership use this identity; model history continues to use
// llm.AgentSessionIDFromContext.
func WithConversationSessionID(ctx context.Context, sessionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeyConversationSession, strings.TrimSpace(sessionID))
}

func ConversationSessionIDFromContext(ctx context.Context) string {
	if ctx != nil {
		if value, _ := ctx.Value(ctxKeyConversationSession).(string); strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
}

func WithApprovedActionID(ctx context.Context, actionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeyApprovedActionID, actionID)
}

func ApprovedActionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxKeyApprovedActionID).(string)
	return v
}

func WithMode(ctx context.Context, mode string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeyMode, strings.TrimSpace(mode))
}

func ModeFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxKeyMode).(string)
	return strings.TrimSpace(v)
}

func WithSystemAddendum(ctx context.Context, s string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeySystemAddendum, s)
}

func SystemAddendumFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxKeySystemAddendum).(string)
	return v
}

// SetRuntimeSessionMode records a mode transition made by a tool mid-run
// (enter_plan_mode / exit_plan_mode) together with the plan directory that
// belongs to it, so the remaining tool calls of that run observe the new mode
// instead of the one the context was built with at run start.
//
// The record is scoped to the run that made the change. Once the run ends, the
// persisted mode store — read into the context by sessionctx at the start of
// every run — is authoritative again. Without that scoping the in-memory value
// outlives its run and shadows every later switch made from the UI, a slash
// command, or the gateway (which all write only the mode store): a session
// switched back to plan mode would still be treated as agent mode, silently
// dropping the plan-mode write policy, and vice versa.
func (s *State) SetRuntimeSessionMode(ctx context.Context, sessionID, mode, planPath string) {
	if s == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	mode = strings.TrimSpace(mode)
	if sessionID == "" || mode == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionRuntimeModes == nil {
		s.sessionRuntimeModes = make(map[string]runtimeSessionMode)
	}
	s.sessionRuntimeModes[sessionID] = runtimeSessionMode{
		mode:     mode,
		planPath: strings.TrimSpace(planPath),
		runID:    RunIDFromContext(ctx),
	}
}

func (s *State) SetNetworkSessionDecision(sessionID string, context safety.NetworkApprovalContext, port int, allow bool) {
	if s == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	context.Host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(context.Host), "."))
	if sessionID == "" || context.Host == "" || !context.Protocol.Valid() || port < 0 || port > 65535 {
		return
	}
	key := networkSessionKey{Host: context.Host, Protocol: context.Protocol, Port: port}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.networkSessionDecisions == nil {
		s.networkSessionDecisions = make(map[string]map[networkSessionKey]bool)
	}
	if s.networkSessionDecisions[sessionID] == nil {
		s.networkSessionDecisions[sessionID] = make(map[networkSessionKey]bool)
	}
	s.networkSessionDecisions[sessionID][key] = allow
}

func (s *State) NetworkSessionRules(sessionID string) []safety.SessionNetworkRule {
	if s == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	decisions := s.networkSessionDecisions[sessionID]
	out := make([]safety.SessionNetworkRule, 0, len(decisions))
	for key, allow := range decisions {
		out = append(out, safety.SessionNetworkRule{
			Context: safety.NetworkApprovalContext{Host: key.Host, Protocol: key.Protocol},
			Port:    key.Port, Allow: allow,
		})
	}
	return out
}

func (s *State) ContextWithRuntimeSessionMode(ctx context.Context) context.Context {
	if s == nil {
		return ctx
	}
	// Runtime mode is part of the executing agent's session state (like todos
	// and intermediate notes), not a permission grant shared by a conversation.
	sessionID := llm.AgentSessionIDFromContext(ctx)
	if sessionID == "" {
		return ctx
	}
	s.mu.Lock()
	entry, ok := s.sessionRuntimeModes[sessionID]
	s.mu.Unlock()
	// Only the run that made the transition may be corrected by it; see
	// SetRuntimeSessionMode.
	if !ok || entry.mode == "" || entry.runID != RunIDFromContext(ctx) {
		return ctx
	}
	ctx = WithMode(ctx, entry.mode)
	// The plan directory stays writable after the mode transition too: leaving
	// plan mode is where the agent starts implementing, and it must still be
	// able to write the implementation record back into the plan file.
	if entry.planPath != "" {
		ctx = WithAllowedPlanPath(ctx, entry.planPath)
	}
	return ctx
}

func WithAllowedPlanPath(ctx context.Context, absPlanPath string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeyAllowedPlanPath, strings.TrimSpace(absPlanPath))
}

func WithProjectKey(ctx context.Context, projectKey string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeyProjectKey, strings.TrimSpace(projectKey))
}

func ProjectKeyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxKeyProjectKey).(string)
	return strings.TrimSpace(v)
}

func AllowedPlanPathFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxKeyAllowedPlanPath).(string)
	return strings.TrimSpace(v)
}

func WithForkChild(ctx context.Context, on bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeyForkChild, on)
}

func IsForkChildFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(ctxKeyForkChild).(bool)
	return v
}

func WithSubagentType(ctx context.Context, subagentType string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeySubagentType, strings.ToLower(strings.TrimSpace(subagentType)))
}

func SubagentTypeFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxKeySubagentType).(string)
	return strings.TrimSpace(v)
}

func WithSubagentDefinitionSource(ctx context.Context, source string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeySubagentDefSrc, strings.ToLower(strings.TrimSpace(source)))
}

func SubagentDefinitionSourceFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxKeySubagentDefSrc).(string)
	return strings.TrimSpace(v)
}

func WithProjectRoot(ctx context.Context, root string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeyProjectRoot, strings.TrimSpace(root))
}

func ProjectRootFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxKeyProjectRoot).(string)
	return strings.TrimSpace(v)
}

func WithWorkspaceRoot(ctx context.Context, root string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeyWorkspaceRoot, strings.TrimSpace(root))
}

func WorkspaceRootFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxKeyWorkspaceRoot).(string)
	return strings.TrimSpace(v)
}

func WithToolUseID(ctx context.Context, toolUseID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeyToolUseID, strings.TrimSpace(toolUseID))
}

func ToolUseIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxKeyToolUseID).(string)
	return strings.TrimSpace(v)
}

func WithToolStepID(ctx context.Context, stepID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeyToolStepID, strings.TrimSpace(stepID))
}

func ToolStepIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxKeyToolStepID).(string)
	return strings.TrimSpace(v)
}

// WithToolCompletionCapture opens the slot one tool call reports its own
// result through. Every call gets its own slot, including a call that runs
// inside another call's context.
//
// Reusing an existing slot when one was already in the context looked
// harmless — the orchestration layer is the only caller, and it opens one per
// tool call — but a subagent's whole run executes inside the parent's
// subagent_run call, so every tool the child ran found the parent's slot and
// shared it. Whatever the child ran last and captured (a shell command, say)
// stayed in that slot, and the next child tool that captures nothing of its
// own — intermediate_tool, session_todo — adopted it as its result: reading
// back saved notes returned a shell's exit_code/stdout map with no notes in
// it, so the card said there were none. The parent's own subagent_run result
// was overwritten by its child's last tool the same way.
func WithToolCompletionCapture(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeyToolCompletion, &toolCompletionCapture{})
}

func CaptureToolCompletion(ctx context.Context, payload ToolCompletionPayload) {
	if ctx == nil {
		return
	}
	cap, _ := ctx.Value(ctxKeyToolCompletion).(*toolCompletionCapture)
	if cap == nil {
		return
	}
	cp := ToolCompletionPayload{
		Output:     cloneToolCompletionOutput(payload.Output),
		Error:      strings.TrimSpace(payload.Error),
		ActionID:   strings.TrimSpace(payload.ActionID),
		ActionKind: strings.TrimSpace(payload.ActionKind),
		Duration:   payload.Duration,
	}
	cap.payload = &cp
}

// CaptureToolAttempt records an execution that completed before the tool's
// logical call reached its final state. The orchestration layer emits these
// attempts immediately before the ordinary completion event, allowing the TUI
// to retain a sandbox-denied attempt while the same call waits for approval and
// is later replayed outside the sandbox.
func CaptureToolAttempt(ctx context.Context, payload ToolCompletionPayload) {
	if ctx == nil {
		return
	}
	cap, _ := ctx.Value(ctxKeyToolCompletion).(*toolCompletionCapture)
	if cap == nil {
		return
	}
	cap.attempts = append(cap.attempts, ToolCompletionPayload{
		Output:     cloneToolCompletionOutput(payload.Output),
		Error:      strings.TrimSpace(payload.Error),
		ActionID:   strings.TrimSpace(payload.ActionID),
		ActionKind: strings.TrimSpace(payload.ActionKind),
		Duration:   payload.Duration,
	})
}

func CaptureToolOutput(ctx context.Context, output map[string]any) {
	CaptureToolCompletion(ctx, ToolCompletionPayload{Output: output})
}

func CaptureToolError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	CaptureToolCompletion(ctx, ToolCompletionPayload{Error: err.Error()})
}

func CaptureToolRequiresAction(ctx context.Context, actionID, actionKind string, output map[string]any) {
	CaptureToolCompletion(ctx, ToolCompletionPayload{
		Output:     output,
		ActionID:   actionID,
		ActionKind: actionKind,
	})
}

func ToolCompletionFromContext(ctx context.Context) (ToolCompletionPayload, bool) {
	if ctx == nil {
		return ToolCompletionPayload{}, false
	}
	cap, _ := ctx.Value(ctxKeyToolCompletion).(*toolCompletionCapture)
	if cap == nil || cap.payload == nil {
		return ToolCompletionPayload{}, false
	}
	cp := *cap.payload
	cp.Output = cloneToolCompletionOutput(cp.Output)
	return cp, true
}

func ToolAttemptsFromContext(ctx context.Context) []ToolCompletionPayload {
	if ctx == nil {
		return nil
	}
	cap, _ := ctx.Value(ctxKeyToolCompletion).(*toolCompletionCapture)
	if cap == nil || len(cap.attempts) == 0 {
		return nil
	}
	out := make([]ToolCompletionPayload, 0, len(cap.attempts))
	for _, attempt := range cap.attempts {
		attempt.Output = cloneToolCompletionOutput(attempt.Output)
		out = append(out, attempt)
	}
	return out
}

func cloneToolCompletionOutput(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// WithPolicyApproved marks a tool call that has passed the unified permission
// middleware. PreToolUse hooks may rewrite or block a call, but cannot
// authorize it.
func WithPolicyApproved(ctx context.Context, on bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeyPolicyApproved, on)
}

func PolicyApprovedFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(ctxKeyPolicyApproved).(bool)
	return v
}

// WithRequestPermissionsDenied carries a policy-level empty-grant decision to
// request_permissions without turning it into a generic tool execution error.
func WithRequestPermissionsDenied(ctx context.Context, reason string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeyRequestPermDeny, strings.TrimSpace(reason))
}

func RequestPermissionsDeniedFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	reason, ok := ctx.Value(ctxKeyRequestPermDeny).(string)
	return strings.TrimSpace(reason), ok
}

// policyApprovalReasonCapture records why a tool call was exempted from
// interactive approval so user-facing surfaces can explain auto-approvals.
// The permission middleware and the approval action hook run inside the tool
// invocation and cannot mutate the caller's context, so they write into this
// shared capture installed by the orchestration layer.
//
// The writer (permission middleware / action hook) and the reader (the tool
// body) can run on different goroutines for parallel tool batches, so the
// field is mutex-guarded.
type policyApprovalReasonCapture struct {
	mu     sync.Mutex
	reason string
}

func (c *policyApprovalReasonCapture) set(reason string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.reason = reason
	c.mu.Unlock()
}

func (c *policyApprovalReasonCapture) get() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.TrimSpace(c.reason)
}

// WithPolicyApprovalReasonCapture installs a mutable capture that records the
// human-readable reason a tool call was auto-approved (exempt from an
// interactive approval prompt). Installing it twice returns the same capture.
func WithPolicyApprovalReasonCapture(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(ctxKeyPolicyApprovalReason).(*policyApprovalReasonCapture); ok {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyPolicyApprovalReason, &policyApprovalReasonCapture{})
}

// CapturePolicyApprovalReason records the human-readable reason a tool call
// was approved without an interactive approval prompt. It is a no-op when no
// capture is installed on the context or the reason is empty.
func CapturePolicyApprovalReason(ctx context.Context, reason string) {
	reason = strings.TrimSpace(reason)
	if ctx == nil || reason == "" {
		return
	}
	capture, _ := ctx.Value(ctxKeyPolicyApprovalReason).(*policyApprovalReasonCapture)
	capture.set(reason)
}

// PolicyApprovalReasonFromContext returns the captured auto-approval reason,
// or an empty string when nothing was captured.
func PolicyApprovalReasonFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	capture, _ := ctx.Value(ctxKeyPolicyApprovalReason).(*policyApprovalReasonCapture)
	return capture.get()
}

func WithSandboxBypassApproved(ctx context.Context, on bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeySandboxBypass, on)
}

func SandboxBypassApprovedFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(ctxKeySandboxBypass).(bool)
	return v
}

type ApprovedNetworkAccess struct {
	Context safety.NetworkApprovalContext
	Port    int
}

func WithApprovedNetworkAccess(ctx context.Context, access ApprovedNetworkAccess) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	access.Context.Host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(access.Context.Host), "."))
	if access.Port < 0 || access.Port > 65535 {
		access.Port = 0
	}
	return context.WithValue(ctx, ctxKeyNetworkAccess, access)
}

func ApprovedNetworkAccessFromContext(ctx context.Context) (ApprovedNetworkAccess, bool) {
	if ctx == nil {
		return ApprovedNetworkAccess{}, false
	}
	access, ok := ctx.Value(ctxKeyNetworkAccess).(ApprovedNetworkAccess)
	if !ok || access.Context.Host == "" || !access.Context.Protocol.Valid() {
		return ApprovedNetworkAccess{}, false
	}
	return access, true
}

func WithHookAgentID(ctx context.Context, agentID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeyHookAgentID, strings.TrimSpace(agentID))
}

func HookAgentIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxKeyHookAgentID).(string)
	return strings.TrimSpace(v)
}

func HookTranscriptPathFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxKeyHookTranscript).(string)
	return strings.TrimSpace(v)
}

// GuardWrite refuses the writes no approval can repair. Plan mode is the only
// one left: it is a phase the user entered deliberately and exit_plan_mode ends
// it, so the model has a way out that does not need the user to answer a prompt
// about every file.
//
// Everything else a write can run into — agent metadata directories, FOREBRAIN_HOME,
// git”'s execution surfaces, a loaded skill”'s own files — is a question for the
// user rather than a wall: see ProtectedWriteReason.
func (s *State) GuardWrite(ctx context.Context, absPath string) error {
	// Plan files live under the agent state root
	// (~/.forebrain/workspace/plans/<project>/*.md), so they sit inside FOREBRAIN_HOME
	// and beneath a ".forebrain" segment. The plan directory is an explicitly
	// sanctioned writable region, so a plan write must survive both.
	planTarget := isSanctionedPlanWrite(ctx, absPath)
	mode := strings.ToLower(strings.TrimSpace(ModeFromContext(ctx)))
	if mode == "plan" && !planTarget {
		return fmt.Errorf("plan mode: write blocked (only the plan directory is writable); call exit_plan_mode first")
	}
	return nil
}

// ProtectedWriteReason returns the one sentence the approval prompt shows for a
// write to a protected path, and "" when the write is an ordinary one.
//
// These paths were refusals once. A model that writes to any of them can
// rewrite the instructions it is about to be given, which is worth stopping
// for — but stopping for it means asking the user, not deciding for them: a
// refusal leaves the user unable to change a file they genuinely need to
// change, through the agent they are talking to. So each of these raises the
// approval overlay, and the sentence below is what it has to explain.
func (s *State) ProtectedWriteReason(ctx context.Context, absPath string) string {
	if isSanctionedPlanWrite(ctx, absPath) {
		return ""
	}
	clean := filepath.Clean(strings.TrimSpace(absPath))
	switch {
	case IsProtectedMetadataPath(absPath):
		return "Writing " + clean + " changes agent configuration and skills this project loads its instructions from."
	// FOREBRAIN_HOME by its real path, not by the name it happens to carry. The
	// default home is ~/.forebrain, which the check above already catches, but a
	// home relocated with --home or FOREBRAIN_HOME carries no such segment and
	// holds exactly the same skills, settings and agent state.
	case s.PathInsidePrimaryHome(absPath):
		return "Writing " + clean + " changes forebrain'''s own settings, state or skills under FOREBRAIN_HOME."
	case IsGitExecutionSurfacePath(absPath):
		return "Writing " + clean + " changes something git executes, so it runs on this machine the next time anyone runs git."
	}
	return ""
}

// isSanctionedPlanWrite reports whether absPath targets the plan region the
// context marks writable. The allowed plan region is a dedicated plan
// directory: permit any regular file directly inside it (the LLM may author new
// plan files with semantic names there) and reject the directory itself,
// sub-directories, and traversal outside it. Keep an exact-match fallback for
// any caller that still pins a single plan file path.
func isSanctionedPlanWrite(ctx context.Context, absPath string) bool {
	allowed := strings.TrimSpace(AllowedPlanPathFromContext(ctx))
	if allowed == "" || strings.TrimSpace(absPath) == "" {
		return false
	}
	cleanAbs := filepath.Clean(absPath)
	cleanAllowed := filepath.Clean(allowed)
	return isWithinDir(cleanAbs, cleanAllowed) || cleanAbs == cleanAllowed
}

// protectedMetadataDirNames are the directories no tool may write into. They
// are exactly the places agent configuration and project skills live: forebrain
// loads skills out of .forebrain, .agents, .claude, and .codex, so a model that
// could write there could rewrite the instructions it is about to be given.
//
// These four are a naming convention a checkout carries, so a name is the only
// thing that identifies them. FOREBRAIN_HOME is not one of them even though the
// default home is spelled ".forebrain": it is a single known location, guarded by
// its real path in GuardWrite, so relocating it with --home keeps the guard.
//
// ".git" is deliberately absent: a project's repository metadata is an ordinary
// part of the workspace and follows the same read/write rules as the rest of
// its files. The two places git executes from are held back separately, by
// IsGitExecutionSurfacePath.
var protectedMetadataDirNames = map[string]struct{}{
	".agents":    {},
	".forebrain": {},
	".claude":    {},
	".codex":     {},
}

// IsGitExecutionSurfacePath reports whether path is one of the places git turns
// into a command: the hook scripts it runs on ordinary operations, the config
// files whose keys name a program (core.pager, core.sshCommand, core.hooksPath,
// filter.*.clean, alias.*), and the submodule repositories under .git/modules
// that carry their own copy of both. A payload written there executes on the
// host the next time anyone runs git, outside the sandbox and without an
// approval, which is why these survive as the one part of .git that is not
// ordinary workspace state. Everything else under .git — index locks, refs,
// objects, logs — is writable like any other project file.
func IsGitExecutionSurfacePath(path string) bool {
	segments := pathSegments(path)
	for i, segment := range segments {
		if segment != ".git" {
			continue
		}
		rest := segments[i+1:]
		for j, part := range rest {
			switch part {
			case "hooks", "modules":
				return true
			case "config", "config.worktree":
				if j == len(rest)-1 {
					return true
				}
			}
		}
	}
	return false
}

// pathSegments splits a filesystem path into its components, dropping the
// volume name so a Windows path compares the same way as a POSIX one.
func pathSegments(path string) []string {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	clean := filepath.Clean(path)
	clean = strings.TrimPrefix(clean, filepath.VolumeName(clean))
	return strings.FieldsFunc(clean, func(r rune) bool {
		return r == '/' || r == '\\'
	})
}

func IsProtectedMetadataPath(path string) bool {
	for _, part := range pathSegments(path) {
		if _, protected := protectedMetadataDirNames[part]; protected {
			return true
		}
	}
	return false
}

// isWithinDir reports whether absPath is a regular file located directly
// inside dir (one level deep, no subdirectories, no ".." traversal). dir and
// absPath must already be filepath.Clean'd.
func isWithinDir(absPath, dir string) bool {
	if absPath == "" || dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, absPath)
	if err != nil {
		return false
	}
	if rel == "." || rel == "" {
		return false // the directory itself
	}
	if strings.HasPrefix(rel, "..") || strings.ContainsRune(rel, os.PathSeparator) {
		return false // escapes the dir or reaches into a subdirectory
	}
	return true
}

func NewState(allowedRoots ...string) *State {
	s := &State{
		readFiles:       make(map[string]ReadState),
		toolMetas:       make(map[string]event.ToolMeta),
		loadedSkills:    make(map[string]LoadedSkill),
		contextShots:    make(map[string]json.RawMessage),
		toolSpills:      make(map[string][]ToolResultSpill),
		sessionSpills:   make(map[string][]ToolResultSpill),
		workingSetPin:   make(map[string][]string),
		inProgressTools: make(map[string]struct{}),
		runtimeValues:   make(map[string]any),
	}
	for _, r := range allowedRoots {
		r = filepath.Clean(r)
		if r == "" {
			continue
		}
		s.roots = append(s.roots, r)
	}
	return s
}

func (s *State) SetRuntimeValue(key string, value any) {
	if s == nil {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runtimeValues == nil {
		s.runtimeValues = make(map[string]any)
	}
	s.runtimeValues[key] = value
}

func (s *State) RuntimeValue(key string) any {
	if s == nil {
		return nil
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runtimeValues == nil {
		return nil
	}
	return s.runtimeValues[key]
}

func (s *State) SetContextSnapshot(sessionID string, snapshot any) error {
	if s == nil {
		return fmt.Errorf("nil state")
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return fmt.Errorf("session id required")
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contextShots[sid] = json.RawMessage(b)
	return nil
}

func (s *State) GetContextSnapshot(sessionID string) (json.RawMessage, bool) {
	if s == nil {
		return nil, false
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.contextShots[sid]
	if !ok {
		return nil, false
	}
	out := make(json.RawMessage, len(v))
	copy(out, v)
	return out, true
}

// GetContextSnapshotForRun is the session's snapshot with the run's context
// timeline folded in. Compactions are the conversation's own record of them,
// oldest first, as the session's event log holds them: the state only knows
// what this process saw, and a compaction is a fact about the conversation.
func (s *State) GetContextSnapshotForRun(sessionID, runID string, compactions []event.ContextCompactedPayload) (json.RawMessage, bool) {
	raw, ok := s.GetContextSnapshot(sessionID)
	if !ok || len(raw) == 0 {
		return raw, ok
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw, ok
	}
	compacts := trimContextCompactions(compactions, 16)
	for i := 0; i < len(compacts)/2; i++ {
		j := len(compacts) - 1 - i
		compacts[i], compacts[j] = compacts[j], compacts[i]
	}
	spills := s.ToolResultSpills(sessionID, runID)
	if len(spills) > 0 {
		obj["tool_result_spills"] = spills
	}
	timeline := buildContextTimeline(compacts, spills)
	if len(timeline) > 0 {
		obj["context_timeline"] = timeline
	}
	attribution := BuildTokenAttribution(compacts, spills)
	if attribution != (TokenAttribution{}) {
		obj["token_attribution"] = attribution
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return raw, ok
	}
	return json.RawMessage(b), true
}

func (s *State) ClearContextSnapshot(sessionID string) {
	if s == nil {
		return
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.contextShots, sid)
}

// approvedWriteTTL is how long a write authorization stays valid. The value it
// gates is consumed within one tool call — approve an edit, then the read the
// edit still owes happens seconds later — so the horizon only has to clear any
// turn; anything past it is not a grant the user is still standing behind.
const approvedWriteTTL = time.Hour

// SessionIDs reports every session this state holds session-keyed data for.
//
// The maps are the only place that knows which conversations ever ran through
// this runtime, which is why the cleanup reads them rather than some registry:
// a session the state has never heard of has nothing to collect.
func (s *State) SessionIDs() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.sessionSpills)+len(s.contextShots))
	seen := make(map[string]struct{}, cap(out))
	// Every session-keyed map is collected here, in one place: a map added to
	// the struct has to be added to exactly this list to be cleaned, which is
	// the one convention that keeps the next map from quietly opting out.
	for sid := range s.contextShots {
		seen[sid] = struct{}{}
	}
	for sid := range s.sessionSpills {
		seen[sid] = struct{}{}
	}
	for sid := range s.sessionRuntimeModes {
		seen[sid] = struct{}{}
	}
	for sid := range s.networkSessionDecisions {
		seen[sid] = struct{}{}
	}
	for sid := range s.workingSetPin {
		seen[sid] = struct{}{}
	}
	for sid := range seen {
		if sid = strings.TrimSpace(sid); sid != "" {
			out = append(out, sid)
		}
	}
	sort.Strings(out)
	return out
}

// DropSessions removes every session-keyed entry for the given ids, and the
// run-keyed spill buckets that belong to them. It is the cleanup half of the
// state's session lifecycle: the caller decides when a conversation is over
// (the pool, from the session row's quietness) and this makes the state forget
// it. Idempotent, and an id the state never heard of is a no-op.
func (s *State) DropSessions(sessionIDs []string) int {
	if s == nil || len(sessionIDs) == 0 {
		return 0
	}
	drop := make(map[string]struct{}, len(sessionIDs))
	for _, sid := range sessionIDs {
		if sid = strings.TrimSpace(sid); sid != "" {
			drop[sid] = struct{}{}
		}
	}
	if len(drop) == 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dropped := 0
	for sid := range drop {
		before := false
		if _, ok := s.contextShots[sid]; ok {
			delete(s.contextShots, sid)
			before = true
		}
		if _, ok := s.sessionSpills[sid]; ok {
			delete(s.sessionSpills, sid)
			before = true
		}
		if _, ok := s.sessionRuntimeModes[sid]; ok {
			delete(s.sessionRuntimeModes, sid)
			before = true
		}
		if _, ok := s.networkSessionDecisions[sid]; ok {
			delete(s.networkSessionDecisions, sid)
			before = true
		}
		if _, ok := s.workingSetPin[sid]; ok {
			delete(s.workingSetPin, sid)
			before = true
		}
		if before {
			dropped++
		}
	}
	// Run-keyed spill buckets carry the session they ran in, so the runs of a
	// dropped conversation go with it; the run map of a live session keeps its
	// buckets.
	for rid, spills := range s.toolSpills {
		if len(spills) == 0 {
			continue
		}
		if _, ok := drop[spills[len(spills)-1].SessionID]; ok {
			delete(s.toolSpills, rid)
		}
	}
	return dropped
}

// TrimRunKeyed drops run-keyed spill buckets whose newest entry is older than
// maxAge, and write authorizations older than their TTL.
//
// Runs are supposed to end their spills; a run that
// failed mid-flight never does, and without a bound those buckets are the
// run-shaped version of the same leak. Age is read from the entries themselves
// — each carries a timestamp — so no bookkeeping of run order is needed.
func (s *State) TrimRunKeyed(maxAge time.Duration) int {
	if s == nil {
		return 0
	}
	cutoff := time.Now().Add(-maxAge)
	s.mu.Lock()
	defer s.mu.Unlock()
	dropped := 0
	for rid, spills := range s.toolSpills {
		if len(spills) == 0 {
			delete(s.toolSpills, rid)
			dropped++
			continue
		}
		// Newest last: a bucket whose latest spill is fresh stays, however old
		// its first one is.
		if spills[len(spills)-1].CreatedAt.Before(cutoff) {
			delete(s.toolSpills, rid)
			dropped++
		}
	}
	for path, granted := range s.approvedWrites {
		if time.Since(granted) > approvedWriteTTL {
			delete(s.approvedWrites, path)
			dropped++
		}
	}
	return dropped
}

// TrimStaleReadStates drops file-read baselines older than maxAge.
//
// A baseline exists to catch "the file changed since the agent last read it"
// on the next write. Losing one costs a re-read before that write — the safe
// direction — while keeping baselines for files the runtime will never touch
// again costs memory for the life of the process, which on a long-lived
// gateway is exactly the leak this trims.
func (s *State) TrimStaleReadStates(maxAge time.Duration) int {
	if s == nil {
		return 0
	}
	cutoff := time.Now().Add(-maxAge)
	s.mu.Lock()
	defer s.mu.Unlock()
	dropped := 0
	for path, st := range s.readFiles {
		if st.ReadAt.Before(cutoff) {
			delete(s.readFiles, path)
			dropped++
		}
	}
	return dropped
}

func (s *State) RecordToolResultSpill(spill ToolResultSpill) {
	if s == nil {
		return
	}
	runID := strings.TrimSpace(spill.RunID)
	sessionID := strings.TrimSpace(spill.SessionID)
	path := strings.TrimSpace(spill.Path)
	if runID == "" && sessionID == "" || path == "" {
		return
	}
	spill.RunID = runID
	spill.SessionID = sessionID
	spill.ToolName = strings.TrimSpace(spill.ToolName)
	spill.CallID = strings.TrimSpace(spill.CallID)
	spill.Path = filepath.Clean(path)
	if spill.CreatedAt.IsZero() {
		spill.CreatedAt = time.Now().UTC()
	} else {
		spill.CreatedAt = spill.CreatedAt.UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toolSpills == nil {
		s.toolSpills = make(map[string][]ToolResultSpill)
	}
	if s.sessionSpills == nil {
		s.sessionSpills = make(map[string][]ToolResultSpill)
	}
	if runID != "" {
		s.toolSpills[runID] = trimToolResultSpills(append(s.toolSpills[runID], spill), 64)
	}
	if sessionID != "" {
		s.sessionSpills[sessionID] = trimToolResultSpills(append(s.sessionSpills[sessionID], spill), 64)
	}
}

func (s *State) ToolResultSpills(sessionID, runID string) []ToolResultSpill {
	if s == nil {
		return nil
	}
	rid := strings.TrimSpace(runID)
	sid := strings.TrimSpace(sessionID)
	if rid == "" && sid == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	in := s.toolSpills[rid]
	if len(in) == 0 && sid != "" {
		in = s.sessionSpills[sid]
	}
	if len(in) == 0 {
		return nil
	}
	out := trimToolResultSpills(in, 16)
	for i := 0; i < len(out)/2; i++ {
		j := len(out) - 1 - i
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func trimToolResultSpills(in []ToolResultSpill, limit int) []ToolResultSpill {
	if limit <= 0 || len(in) <= limit {
		out := make([]ToolResultSpill, len(in))
		copy(out, in)
		return out
	}
	out := make([]ToolResultSpill, limit)
	copy(out, in[len(in)-limit:])
	return out
}

func trimContextCompactions(in []event.ContextCompactedPayload, limit int) []event.ContextCompactedPayload {
	if limit <= 0 || len(in) <= limit {
		out := make([]event.ContextCompactedPayload, len(in))
		copy(out, in)
		return out
	}
	out := make([]event.ContextCompactedPayload, limit)
	copy(out, in[len(in)-limit:])
	return out
}

func buildContextTimeline(compacts []event.ContextCompactedPayload, spills []ToolResultSpill) []ContextTimelineEntry {
	out := buildContextTimelineEntries(compacts, spills)
	if len(out) > 24 {
		out = out[:24]
	}
	return out
}

type TokenAttribution struct {
	CheckpointTokens    int `json:"checkpoint_tokens,omitempty"`
	ToolReferenceTokens int `json:"tool_reference_tokens,omitempty"`
	FreedTokens         int `json:"freed_tokens,omitempty"`
}

func BuildTokenAttribution(compacts []event.ContextCompactedPayload, spills []ToolResultSpill) TokenAttribution {
	return buildTokenAttribution(compacts, spills)
}

func buildContextTimelineEntries(compacts []event.ContextCompactedPayload, spills []ToolResultSpill) []ContextTimelineEntry {
	out := make([]ContextTimelineEntry, 0, len(compacts)+len(spills))
	for _, item := range compacts {
		out = append(out, ContextTimelineEntry{
			Kind:          "compact",
			CreatedAt:     parseRFC3339(item.CreatedAtUTC),
			Trigger:       strings.TrimSpace(item.Trigger),
			Reason:        strings.TrimSpace(item.Reason),
			BoundaryID:    strings.TrimSpace(item.BoundaryID),
			ReplacedItems: item.ReplacedItems,
			WindowNumber:  item.WindowNumber,
			Summary:       strings.TrimSpace(item.Summary),
			Strategy:      strings.TrimSpace(item.Strategy),
			SummarySource: strings.TrimSpace(item.SummarySource),
			Scope:         strings.TrimSpace(item.Scope),
			TokensBefore:  item.TokensBefore,
			TokensAfter:   item.TokensAfter,
			Reactive:      item.Reactive,
		})
	}
	for _, item := range spills {
		out = append(out, ContextTimelineEntry{
			Kind:          "tool_result_spill",
			SessionID:     strings.TrimSpace(item.SessionID),
			RunID:         strings.TrimSpace(item.RunID),
			CreatedAt:     item.CreatedAt,
			ToolName:      strings.TrimSpace(item.ToolName),
			CallID:        strings.TrimSpace(item.CallID),
			Path:          strings.TrimSpace(item.Path),
			OriginalBytes: item.OriginalBytes,
			OmittedBytes:  item.OmittedBytes,
			TotalLines:    item.TotalLines,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		left := out[i].CreatedAt
		right := out[j].CreatedAt
		if left.Equal(right) {
			if out[i].Kind == out[j].Kind {
				return i < j
			}
			return out[i].Kind < out[j].Kind
		}
		if left.IsZero() {
			return false
		}
		if right.IsZero() {
			return true
		}
		return left.After(right)
	})
	return out
}

func buildTokenAttribution(compacts []event.ContextCompactedPayload, spills []ToolResultSpill) TokenAttribution {
	attribution := TokenAttribution{}
	for _, item := range compacts {
		attribution.CheckpointTokens += estimateTokens(item.Summary)
	}
	for _, spill := range spills {
		attribution.ToolReferenceTokens += estimateTokens(spill.Path)
	}
	if freed := maxCompactTokenDelta(compacts); freed > 0 {
		attribution.FreedTokens = freed
	}
	return attribution
}

func maxCompactTokenDelta(compacts []event.ContextCompactedPayload) int {
	best := 0
	for _, item := range compacts {
		if item.TokensBefore <= 0 || item.TokensAfter < 0 {
			continue
		}
		if delta := item.TokensBefore - item.TokensAfter; delta > best {
			best = delta
		}
	}
	return best
}

func parseRFC3339(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return ts.UTC()
}

func estimateTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	return int(math.Max(1, math.Ceil(float64(len(text))/4.0)))
}

func (s *State) WorkingSetPins(sessionID string) []string {
	if s == nil {
		return nil
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	in := s.workingSetPin[sid]
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, x := range in {
		v := strings.TrimSpace(x)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func (s *State) UpsertWorkingSetPins(sessionID string, add []string, remove []string) []string {
	if s == nil {
		return nil
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	base := make(map[string]struct{})
	for _, x := range s.workingSetPin[sid] {
		v := strings.TrimSpace(x)
		if v == "" {
			continue
		}
		base[v] = struct{}{}
	}
	for _, x := range add {
		v := strings.TrimSpace(x)
		if v == "" {
			continue
		}
		base[v] = struct{}{}
	}
	for _, x := range remove {
		v := strings.TrimSpace(x)
		if v == "" {
			continue
		}
		delete(base, v)
	}
	out := make([]string, 0, len(base))
	for k := range base {
		out = append(out, k)
	}
	sort.Strings(out)
	s.workingSetPin[sid] = out
	next := make([]string, len(out))
	copy(next, out)
	return next
}

func (s *State) RegisterToolMeta(meta event.ToolMeta) {
	if s == nil || strings.TrimSpace(meta.Name) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolMetas[meta.Name] = meta
}

func (s *State) ToolMetas() []event.ToolMeta {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]event.ToolMeta, 0, len(s.toolMetas))
	for _, m := range s.toolMetas {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

var schemaCache sync.Map // map[string]json.RawMessage

func SchemaForInputType(t reflect.Type) json.RawMessage {
	if t == nil {
		return nil
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	key := t.PkgPath() + "." + t.Name()
	if v, ok := schemaCache.Load(key); ok {
		if b, ok := v.(json.RawMessage); ok {
			return b
		}
	}
	r := jsonschema.Reflector{
		AllowAdditionalProperties: false,
		DoNotReference:            true,
	}
	s := r.ReflectFromType(t)
	b, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	out := json.RawMessage(b)
	schemaCache.Store(key, out)
	return out
}

func (s *State) SetHistoryDir(dir string) {
	if s == nil {
		return
	}
	dir = strings.TrimSpace(dir)
	if dir != "" {
		dir = filepath.Clean(dir)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historyDir = dir
}

func (s *State) SetToolResultDir(dir string) {
	if s == nil {
		return
	}
	dir = strings.TrimSpace(dir)
	if dir != "" {
		dir = filepath.Clean(dir)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolResultDir = dir
	if dir == "" {
		return
	}
	for _, x := range s.roots {
		if filepath.Clean(x) == dir {
			return
		}
	}
	s.roots = append(s.roots, dir)
}

func (s *State) SetActionHook(h func(ctx context.Context, kind string, payload any) (actionID string, ok bool, err error)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actionHook = h
}

func (s *State) ActionHook() func(ctx context.Context, kind string, payload any) (actionID string, ok bool, err error) {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.actionHook
}

func (s *State) SetNetworkApprovalPromptHook(h func(ctx context.Context, actionID string, payload map[string]any, request safety.NetworkApprovalRequest) (safety.NetworkApprovalDecision, error)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.networkApprovalPromptHook = h
}

func (s *State) NetworkApprovalPromptHook() func(ctx context.Context, actionID string, payload map[string]any, request safety.NetworkApprovalRequest) (safety.NetworkApprovalDecision, error) {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.networkApprovalPromptHook
}

// SubagentApprovalHook resolves one approval a subagent raised while working on
// its task and returns the context that subagent's run resumes under, carrying
// the approved action id and the resume state.
//
// It is the in-place counterpart of the unwind-and-resume path the primary
// agent uses. A subagent dispatched from inside a running tool call cannot
// unwind: its dispatcher (subagent_fanout above all) is still holding the
// results of its siblings, and there is nothing above it that could replay the
// dispatch. So the surface is asked here, while the child's run stays alive,
// exactly as NetworkApprovalPromptHook is asked from inside the shell tool.
type SubagentApprovalHook func(ctx context.Context, rae *RequiresActionError) (context.Context, error)

type NetworkApprovalPromptHook func(ctx context.Context, actionID string, payload map[string]any, request safety.NetworkApprovalRequest) (safety.NetworkApprovalDecision, error)

type StepHook func(ctx context.Context, evt StepEvent)

// The gateway can run different conversations concurrently over one tool
// registry. Per-run hooks belong on the execution context; mutating State's
// process-wide hook would route one session's events or approval prompt to a
// different websocket. State hooks remain the fallback for the single-session
// TUI and existing embedders.
func WithSubagentApprovalHook(ctx context.Context, hook SubagentApprovalHook) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeySubagentApprovalHook, hook)
}

func SubagentApprovalHookFromContext(ctx context.Context, fallback *State) SubagentApprovalHook {
	if ctx != nil {
		if hook, _ := ctx.Value(ctxKeySubagentApprovalHook).(SubagentApprovalHook); hook != nil {
			return hook
		}
	}
	if fallback == nil {
		return nil
	}
	return fallback.SubagentApprovalHook()
}

func WithNetworkApprovalPromptHook(ctx context.Context, hook NetworkApprovalPromptHook) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeyNetworkApprovalHook, hook)
}

func NetworkApprovalPromptHookFromContext(ctx context.Context, fallback *State) NetworkApprovalPromptHook {
	if ctx != nil {
		if hook, _ := ctx.Value(ctxKeyNetworkApprovalHook).(NetworkApprovalPromptHook); hook != nil {
			return hook
		}
	}
	if fallback == nil {
		return nil
	}
	return fallback.NetworkApprovalPromptHook()
}

func WithStepHook(ctx context.Context, hook StepHook) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKeyStepHook, hook)
}

func StepHookFromContext(ctx context.Context, fallback *State) StepHook {
	if ctx != nil {
		if hook, _ := ctx.Value(ctxKeyStepHook).(StepHook); hook != nil {
			return hook
		}
	}
	if fallback == nil {
		return nil
	}
	return fallback.StepHook()
}

func (s *State) SetSubagentApprovalHook(h SubagentApprovalHook) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subagentApprovalHook = h
}

func (s *State) SubagentApprovalHook() SubagentApprovalHook {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subagentApprovalHook
}

func (s *State) SetStepHook(h func(ctx context.Context, evt StepEvent)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stepHook = h
}

func (s *State) StepHook() func(ctx context.Context, evt StepEvent) {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stepHook
}

func (s *State) SetReadObserver(h func(ctx context.Context, absPath string, content []byte)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readObserver = h
}

func (s *State) ReadObserver() func(ctx context.Context, absPath string, content []byte) {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readObserver
}

func (s *State) ToolResultDir() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.toolResultDir
}

func (s *State) HistoryDir() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.historyDir
}

// SetPrimaryWorkspaceBoundary records the forebrain home and the workspace roots
// that separate one primary agent from the next. home is FOREBRAIN_HOME itself,
// passed in rather than inferred: the home is what the write guards protect, so
// deriving it from the shape of a workspace path made the protection depend on
// what the directory happened to be called.
func (s *State) SetPrimaryWorkspaceBoundary(home string, active string, siblings []string) {
	if s == nil {
		return
	}
	active = normalizeRootPath(active)
	primaryHome := normalizeRootPath(home)
	normalizedSiblings := make([]string, 0, len(siblings))
	seenSiblings := map[string]struct{}{}
	for _, sibling := range siblings {
		sibling = normalizeRootPath(sibling)
		if sibling == "" || sibling == active {
			continue
		}
		if _, ok := seenSiblings[sibling]; ok {
			continue
		}
		seenSiblings[sibling] = struct{}{}
		normalizedSiblings = append(normalizedSiblings, sibling)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	oldActive := s.primaryWorkspaceRoot
	s.primaryHomeRoot = primaryHome
	s.primaryWorkspaceRoot = active
	s.siblingPrimaryWorkspaceRoots = normalizedSiblings
	if oldActive != "" && oldActive != active {
		// An approval belongs to the agent it was given to, the same way the
		// roots below and the safety store's runtime grants do.
		s.approvedWrites = nil
	}

	keepRoot := func(root string) bool {
		root = normalizeRootPath(root)
		if root == "" || root == active {
			return false
		}
		if oldActive != "" && oldActive != active && pathWithinRoot(root, oldActive) {
			return false
		}
		for _, sibling := range normalizedSiblings {
			if pathsOverlap(root, sibling) {
				return false
			}
		}
		return true
	}
	filteredRoots := make([]string, 0, len(s.roots)+1)
	seenRoots := map[string]struct{}{}
	for _, root := range s.roots {
		root = normalizeRootPath(root)
		if !keepRoot(root) {
			continue
		}
		if _, ok := seenRoots[root]; ok {
			continue
		}
		seenRoots[root] = struct{}{}
		filteredRoots = append(filteredRoots, root)
	}
	if active != "" {
		filteredRoots = append(filteredRoots, active)
	}
	s.roots = filteredRoots
}

// RememberApprovedWrite records that the user authorized a write to absPath, so
// the read that write needs does not ask them a second time.
//
// A write to a protected path is the decision; the read_file call that
// write_file and edit_file require first is a step on the way to it, not a
// separate thing to consent to. Reading a file the user has just agreed to have
// changed reveals nothing the approved change does not already imply.
func (s *State) RememberApprovedWrite(absPath string) {
	if s == nil {
		return
	}
	path := normalizeRootPath(absPath)
	if path == "" {
		return
	}
	s.mu.Lock()
	if s.approvedWrites == nil {
		s.approvedWrites = make(map[string]time.Time)
	}
	s.approvedWrites[path] = time.Now()
	s.mu.Unlock()
}

// ApprovedWrite reports whether the user has authorized a write to absPath.
func (s *State) ApprovedWrite(absPath string) bool {
	if s == nil {
		return false
	}
	path := normalizeRootPath(absPath)
	if path == "" {
		return false
	}
	s.mu.Lock()
	granted, ok := s.approvedWrites[path]
	if ok && time.Since(granted) > approvedWriteTTL {
		// The authorization answered a question of one tool call; a later one
		// asks again. Reading the check here keeps the horizon true even
		// between sweeps.
		delete(s.approvedWrites, path)
		ok = false
	}
	s.mu.Unlock()
	return ok
}

// ProtectedReadReason returns the one sentence the approval prompt shows for a
// read of forebrain's own home, and "" for an ordinary read.
//
// FOREBRAIN_HOME holds credentials — auth.json, the env file — so a read there is
// worth stopping for. It is the user's own home, though, and a user who asks
// the agent to look at their own settings has to be able to say yes: the stop
// is a prompt, not a refusal. Three things are already settled and are not
// asked about again: the active workspace and the skill catalog, each reachable
// by its own route, and a file whose write the user has already approved.
func (s *State) ProtectedReadReason(absPath string) string {
	if !s.PathInsidePrimaryHome(absPath) {
		return ""
	}
	if s.PathUnderPrimaryWorkspace(absPath) || s.PathUnderLoadedSkillRoot(absPath) {
		return ""
	}
	if s.ApprovedWrite(absPath) {
		return ""
	}
	return "Reading " + filepath.Clean(strings.TrimSpace(absPath)) + " reads forebrain's own settings and state under FOREBRAIN_HOME, which can hold credentials."
}

// PathInsidePrimaryHome reports whether path sits anywhere inside FOREBRAIN_HOME —
// the tree holding forebrain's own configuration, state and skills.
func (s *State) PathInsidePrimaryHome(path string) bool {
	if s == nil || strings.TrimSpace(path) == "" {
		return false
	}
	s.mu.Lock()
	home := s.primaryHomeRoot
	s.mu.Unlock()
	return home != "" && pathWithinRoot(path, home)
}

func (s *State) PathOverlapsSiblingPrimaryWorkspace(path string) bool {
	if s == nil || strings.TrimSpace(path) == "" {
		return false
	}
	path = normalizeRootPath(path)
	s.mu.Lock()
	siblings := append([]string(nil), s.siblingPrimaryWorkspaceRoots...)
	s.mu.Unlock()
	for _, sibling := range siblings {
		if pathsOverlap(path, sibling) {
			return true
		}
	}
	return false
}

func (s *State) PathUnderPrimaryWorkspace(file string) bool {
	if s == nil || strings.TrimSpace(file) == "" {
		return false
	}
	s.mu.Lock()
	root := s.primaryWorkspaceRoot
	s.mu.Unlock()
	if root == "" {
		return false
	}
	_, err := ResolveWithinRoots(file, []string{root})
	return err == nil
}

func normalizeRootPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if abs, err := filepath.Abs(raw); err == nil && abs != "" {
		raw = abs
	}
	raw = filepath.Clean(raw)
	if resolved, err := filepath.EvalSymlinks(raw); err == nil && strings.TrimSpace(resolved) != "" {
		raw = filepath.Clean(resolved)
	} else if resolved, ok := normalizeViaExistingParent(raw); ok {
		raw = resolved
	}
	return raw
}

func normalizeViaExistingParent(raw string) (string, bool) {
	parent := filepath.Clean(raw)
	for {
		if _, err := os.Stat(parent); err == nil {
			resolvedParent, resolveErr := filepath.EvalSymlinks(parent)
			if resolveErr != nil {
				return "", false
			}
			rel, relErr := filepath.Rel(parent, raw)
			if relErr != nil {
				return "", false
			}
			return filepath.Clean(filepath.Join(resolvedParent, rel)), true
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", false
		}
		parent = next
	}
}

func pathWithinRoot(path, root string) bool {
	path = normalizeRootPath(path)
	root = normalizeRootPath(root)
	if path == "" || root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func pathsOverlap(a, b string) bool {
	return pathWithinRoot(a, b) || pathWithinRoot(b, a)
}

func (s *State) AllowedRoots() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.roots))
	out = append(out, s.roots...)
	return out
}

// ConfineToRoot binds the file tools to a single directory, for an embedded
// agent that owns a folder rather than a workspace: memory consolidation is the
// first such caller. Confinement changes path resolution in two ways — a
// relative path is joined to dir instead of the process working directory, and
// a path outside dir is refused outright instead of being handed to the
// approval layer.
//
// Both halves matter for an agent with no operator attached. Such a run is
// policy-approved, so the "ask rather than refuse" fallback in
// resolveFileToolPath would otherwise write a bare `MEMORY.md` into whatever
// directory the CLI happened to be started in.
func (s *State) ConfineToRoot(dir string) {
	if s == nil {
		return
	}
	dir = normalizeRootPath(dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.confinedRoot = dir
}

// ConfinedRoot reports the directory the file tools are bound to, or "" when
// this State is not confined.
func (s *State) ConfinedRoot() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.confinedRoot
}

// SetReadPolicy records the sandbox deny_read / allow_read paths so in-process
// file tools (read_file, code_search) enforce the
// same read isolation the sandbox backends apply to shell commands. Without
// this, deny_read only constrains shelled-out commands and an agent could read
// a denied path directly through its own file tools.
func (s *State) SetReadPolicy(denyRead, allowRead, denyReadPatterns []string, cwd string) {
	if s == nil {
		return
	}
	norm := func(in []string) []string {
		out := make([]string, 0, len(in))
		seen := map[string]struct{}{}
		for _, raw := range in {
			p := strings.TrimSpace(raw)
			if p == "" {
				continue
			}
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
			p = filepath.Clean(p)
			if resolved, err := filepath.EvalSymlinks(p); err == nil {
				p = resolved
			}
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.denyRead = norm(denyRead)
	s.allowRead = norm(allowRead)
	s.denyReadPatterns = make([]safety.FileSystemPermissionPath, 0, len(denyReadPatterns))
	seenPatterns := map[string]struct{}{}
	for _, raw := range denyReadPatterns {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			continue
		}
		if _, ok := seenPatterns[pattern]; ok {
			continue
		}
		seenPatterns[pattern] = struct{}{}
		s.denyReadPatterns = append(s.denyReadPatterns, safety.FileSystemPermissionPath{
			Type:    safety.FileSystemPermissionPathTypeGlobPattern,
			Pattern: pattern,
		})
	}
	s.readPolicyCWD = filepath.Clean(strings.TrimSpace(cwd))
}

func (s *State) SetAdditionalReadRoots(roots []string) {
	if s == nil {
		return
	}
	normalized := make([]string, 0, len(roots))
	for _, root := range roots {
		root = normalizeRootPath(root)
		if root != "" {
			normalized = MergeAllowedRootPaths(normalized, []string{root})
		}
	}
	s.mu.Lock()
	s.additionalReadRoots = normalized
	s.mu.Unlock()
}

func (s *State) SetWritePolicy(allowWrite []string) {
	if s == nil {
		return
	}
	normalized := make([]string, 0, len(allowWrite))
	for _, path := range allowWrite {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if abs, err := filepath.Abs(path); err == nil {
			normalized = MergeAllowedRootPaths(normalized, []string{abs})
		}
	}
	s.mu.Lock()
	s.allowWrite = normalized
	s.mu.Unlock()
}

func (s *State) PermissionRoots(access safety.FileSystemPermissionAccess) []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var roots []string
	switch access {
	case safety.FileSystemAccessRead:
		roots = MergeAllowedRootPaths(s.allowRead, s.additionalReadRoots)
	case safety.FileSystemAccessWrite:
		roots = s.allowWrite
	}
	return append([]string(nil), roots...)
}

// ReadPathDenied reports whether reading the given path is forbidden by the
// configured deny_read policy. allow_read subpaths carve exceptions out of a
// denied subtree, matching the seatbelt/bubblewrap backend semantics. The
// check is deny-subtree based: a denied directory also denies everything under
// it unless an allow_read entry re-permits a more specific subpath.
func (s *State) ReadPathDenied(path string) bool {
	if s == nil {
		return false
	}
	p := strings.TrimSpace(path)
	if p == "" {
		return false
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	s.mu.Lock()
	deny := append([]string(nil), s.denyRead...)
	allow := MergeAllowedRootPaths(s.allowRead, s.additionalReadRoots)
	patterns := append([]safety.FileSystemPermissionPath(nil), s.denyReadPatterns...)
	cwd := s.readPolicyCWD
	s.mu.Unlock()
	for _, pattern := range patterns {
		if pattern.Matches(p, cwd) {
			return true
		}
	}
	if len(deny) == 0 {
		return false
	}
	if !pathWithinAny(p, deny) {
		return false
	}
	// Denied subtree. allow_read can re-permit a more specific subpath.
	if pathWithinAny(p, allow) {
		return false
	}
	return true
}

// ReadDirPrunable reports whether a directory can be skipped wholesale during a
// read walk: it is inside a denied subtree AND no allow_read carve-out lives at
// or beneath it. When an allow_read entry is nested under the directory, the
// walk must descend so the carve-out remains reachable.
func (s *State) ReadDirPrunable(dir string) bool {
	if s == nil {
		return false
	}
	p := strings.TrimSpace(dir)
	if p == "" {
		return false
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	s.mu.Lock()
	deny := append([]string(nil), s.denyRead...)
	allow := MergeAllowedRootPaths(s.allowRead, s.additionalReadRoots)
	patterns := append([]safety.FileSystemPermissionPath(nil), s.denyReadPatterns...)
	cwd := s.readPolicyCWD
	s.mu.Unlock()
	for _, pattern := range patterns {
		if pattern.Matches(p, cwd) {
			return true
		}
	}
	if len(deny) == 0 {
		return false
	}
	if !pathWithinAny(p, deny) {
		return false
	}
	// Denied dir. Do not prune if an allow_read carve-out sits at or under it.
	for _, a := range allow {
		a = filepath.Clean(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if a == p {
			return false
		}
		rel, err := filepath.Rel(p, a)
		if err != nil {
			continue
		}
		if rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..") {
			return false
		}
	}
	return true
}

// pathWithinAny reports whether target equals or is nested under any of roots.
func pathWithinAny(target string, roots []string) bool {
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "" {
			continue
		}
		if target == root {
			return true
		}
		rel, err := filepath.Rel(root, target)
		if err != nil {
			continue
		}
		if rel == "." {
			return true
		}
		if !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
			return true
		}
	}
	return false
}

func (s *State) SnapshotBeforeWrite(absPath string, content []byte) error {
	if s == nil {
		return nil
	}
	dir := s.HistoryDir()
	if dir == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(absPath))
	key := hex.EncodeToString(sum[:8])
	targetDir := filepath.Join(dir, key)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("%d.snapshot", time.Now().UnixNano())
	return os.WriteFile(filepath.Join(targetDir, name), content, 0o600)
}

func (s *State) LatestSnapshot(absPath string) (snapPath string, ok bool) {
	if s == nil {
		return "", false
	}
	dir := s.HistoryDir()
	if dir == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(absPath))
	key := hex.EncodeToString(sum[:8])
	targetDir := filepath.Join(dir, key)
	ents, err := os.ReadDir(targetDir)
	if err != nil {
		return "", false
	}
	var best string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".snapshot") {
			continue
		}
		if best == "" || n > best {
			best = n
		}
	}
	if best == "" {
		return "", false
	}
	return filepath.Join(targetDir, best), true
}

func (s *State) RememberRead(absPath string, mod time.Time, size int64, content []byte) {
	s.rememberTouch(absPath, mod, size, content, "read")
}

func (s *State) RememberWrite(absPath string, mod time.Time, size int64, content []byte) {
	s.rememberTouch(absPath, mod, size, content, "write")
}

func (s *State) rememberTouch(absPath string, mod time.Time, size int64, content []byte, kind string) {
	if s == nil {
		return
	}
	sum := sha256.Sum256(content)
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "read"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readFiles[absPath] = ReadState{
		AbsPath:   absPath,
		ModTime:   mod,
		Size:      size,
		SHA256Hex: hex.EncodeToString(sum[:]),
		ReadAt:    time.Now(),
		TouchKind: kind,
	}
}

func (s *State) GetReadState(absPath string) (ReadState, bool) {
	if s == nil {
		return ReadState{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.readFiles[absPath]
	return st, ok
}

func (s *State) ReadStates() []ReadState {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ReadState, 0, len(s.readFiles))
	for _, st := range s.readFiles {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ReadAt.Equal(out[j].ReadAt) {
			return out[i].AbsPath < out[j].AbsPath
		}
		return out[i].ReadAt.After(out[j].ReadAt)
	})
	return out
}

func readStateMatchesContent(st ReadState, content []byte) bool {
	if strings.TrimSpace(st.SHA256Hex) == "" {
		return true
	}
	sum := sha256.Sum256(content)
	return st.SHA256Hex == hex.EncodeToString(sum[:])
}

func statFile(absPath string) (os.FileInfo, error) {
	return os.Stat(absPath)
}

var ErrPathNotAllowed = home.ErrPathNotAllowed

// ErrPathReadDenied is returned when a path is blocked by the sandbox
// deny_read policy. It is distinct from ErrPathNotAllowed, which the file
// tools raise only for the boundaries no approval can lift: a
// request_permissions deny entry, another primary agent's workspace, the rest
// of FOREBRAIN_HOME, and a subagent reaching outside its roots. Both are hard
// policy refusals; neither may be turned into an approval prompt.
var ErrPathReadDenied = errors.New("path read denied by sandbox deny_read policy")

func ResolveWithinRoots(p string, roots []string) (string, error) {
	return home.ResolveWithinRoots(p, roots)
}

// getenv is a var so tests can override environment lookups.
var getenv = os.Getenv

// tokens.go / util — small shared helpers.

// EstimateTokens converts a byte delta into an approximate token count using
// the same ~4-bytes-per-token heuristic Boost's tokens.Estimate uses.
func EstimateTokens(bytes int) int {
	if bytes <= 0 {
		return 0
	}
	return bytes / 4
}

var (
	ansiCSI = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	ansiOSC = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	ansiEsc = regexp.MustCompile(`\x1b[@-_]`)
)

// StripANSI removes terminal escape sequences (colors, cursor movement, OSC)
// so filters match plain text and models don't spend tokens on control bytes.
func StripANSI(s string) string {
	s = ansiOSC.ReplaceAllString(s, "")
	s = ansiCSI.ReplaceAllString(s, "")
	s = ansiEsc.ReplaceAllString(s, "")
	return s
}

var (
	defaultEngineOnce sync.Once
	defaultEngine     *Engine
	defaultErrs       []error
)

// DefaultEngine returns the process-wide engine built from the embedded
// builtin filters (lazily compiled on first use).
func DefaultEngine() (*Engine, []error) {
	defaultEngineOnce.Do(func() {
		defaultEngine, defaultErrs = NewEngine(BuiltinFilterDocs(), "builtin")
	})
	return defaultEngine, defaultErrs
}

// redact.go — secret/identity concealment applied before output is stored or
// shown, mirroring Boost's concealer/redact layer. Compression must never leak
// credentials that a noisy tool printed. Patterns are intentionally
// conservative: only high-confidence secret shapes are masked so legitimate
// output is not destroyed.

// RedactedMarker replaces secret values.
const RedactedMarker = "***REDACTED***"

// RedactionDisabledEnv lets operators turn redaction off (Boost equivalent:
// BOOST_DISABLE_OUTPUT_REDACTION).
const RedactionDisabledEnv = "FOREBRAIN_OUTPUT_FILTER_DISABLE_REDACTION"

type secretRule struct {
	re   *regexp.Regexp
	repl string
}

var secretRules = []secretRule{
	// key=value / key: value pairs whose key names a secret. Captures the
	// secret-looking value while keeping the key for context.
	{
		re:   regexp.MustCompile(`(?i)\b([a-z0-9_.-]*(token|secret|password|passwd|api_?key|access_?key|private_?key|credential)[a-z0-9_.-]*)\s*[:=]\s*("?[^\s"',;]{8,}"?)`),
		repl: "${1}=" + RedactedMarker,
	},
	// Bearer / Basic auth headers.
	{
		re:   regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9+/_\-.=]{16,}`),
		repl: "${1} " + RedactedMarker,
	},
	// Common provider token prefixes.
	{re: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`), repl: RedactedMarker},                                   // GitHub
	{re: regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`), repl: RedactedMarker},                                 // GitHub fine-grained
	{re: regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`), repl: RedactedMarker},                                 // Slack
	{re: regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`), repl: RedactedMarker},                                        // OpenAI-style
	{re: regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), repl: RedactedMarker},                                             // AWS access key id
	{re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`), repl: RedactedMarker},                                        // Google API key
	{re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}\b`), repl: RedactedMarker}, // JWT
}

// Redact masks high-confidence secrets in s. It is a no-op when s is empty.
func Redact(s string) string {
	if s == "" {
		return s
	}
	for _, rule := range secretRules {
		s = rule.re.ReplaceAllString(s, rule.repl)
	}
	return s
}

// RedactEnabled reports whether output redaction is active for this process.
func RedactEnabled() bool {
	return !envTruthy(RedactionDisabledEnv)
}

func envTruthy(name string) bool {
	v := strings.ToLower(strings.TrimSpace(getenv(name)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
