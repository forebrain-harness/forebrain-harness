package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// Deps is the set of path, config and store dependencies a Runner needs that
// are owned by the composition root (process.Environment), not by the Runner
// itself. A primary Runner shares one Deps with its Environment so there is a
// single owner for this state; a subagent Runner gets its own Deps with
// StateDir overridden (see Factory.NewIsolatedRunner), because its state lives
// under a per-subagent directory.
//
// R6: these fields used to sit directly on Runner as copies of what the
// Environment held, which is how Runner.AppCfg could drift from
// Environment.Config. Embedding the pointer means r.Home and r.AppCfg still
// resolve (promoted fields), but the storage is the shared Deps, not a second
// copy on every Runner.
type Deps struct {
	Home          string
	WorkspaceRoot string
	// ProjectKey is the session's fixed launch-project identity: the project
	// root with its separators turned into dashes (memory.ProjectKey). It is
	// propagated to tools, inherited by subagents, and names the per-project
	// directories of the plan and memory stores. Empty means the session has no
	// project identity — plans then live directly under <workspaceRoot>/plans
	// (gateway/channel conversations) and project memory does not apply.
	ProjectKey string
	// ProjectRoot is the user's project directory (the TUI launch cwd),
	// captured by ChatSession.SetWorkingDir. It is distinct from Home
	// (~/.forebrain) and WorkspaceRoot (~/.forebrain/workspace): it is where the
	// user's actual code lives. Subagents inherit it so the path-constraint
	// system prompt and file tools default to the real project, not the
	// forebrain state directory.
	ProjectRoot string
	AgentName   string
	StateDir    string
	Actions     *state.ActionService
	MCPServers  []appcfg.MCPServerConfig
	// MCPProject is the directory project-scope MCP entries run from (the
	// session's frozen launch-project root). It is captured alongside
	// MCPServers because together the two are the Runner's MCP fingerprint:
	// either changing is what rebuilds the MCP segment. Subagents inherit it
	// for the same reason they inherit MCPServers.
	MCPProject string
	// MCPDisabled lists the configured servers the agent's disable store kept
	// out of MCPServers when the session froze its list. Display only: they are
	// not started and never reach the tool table; /mcp shows them so the choice
	// can be seen and reversed.
	MCPDisabled []appcfg.MCPServerConfig
	// MCPDiagnostics reports the project-level MCP view for display: which
	// global entries the project replaced, which project entries are not
	// running and why, and whether the on-disk files drifted from the frozen
	// list. It is a display hook only — the effective list itself is frozen
	// for the session — installed by the composition root, which is the one
	// place that knows the launch project and the consent store. Nil on a
	// Runner assembled without one; readers then see an empty view.
	MCPDiagnostics func() mcp.ProjectMCPScopeSummary
	// ProjectMemoryOnly reports the launch project's memory scope: when true,
	// the session's memory recall and capture stay inside the project's own
	// scope and never reach the cross-project global scope. Frozen with the
	// same session context as the MCP list — the memory instruction sits in
	// the cached prefix.
	ProjectMemoryOnly bool
	// LaunchProject is the frozen launch-project trust decision this runtime
	// was built from. The TUI's is the directory it started in; a project
	// runner's is the project it serves. Zero means no launch project, and
	// project-gated features (project skills) read it rather than re-deriving
	// a project from the process working directory, which for a daemon is not
	// any session's project at all.
	LaunchProject safety.ProjectContext
	// ProjectInstructionsFor returns the project-level instructions for one
	// session, frozen at the session's first use (the composition root
	// snapshots them there). The returned string is injected verbatim as the
	// first developer-side message after the system prompt, so the tools and
	// system bytes ahead of it are unaffected by it. Nil means no project
	// instructions apply.
	ProjectInstructionsFor func(sessionID string) string
	MemoryStore            *memory.Store
	AppCfg                 *appcfg.Root
	SessionStore           *state.SessionStore
	RunRT                  *state.RunStore
}

type Runner struct {
	*Deps
	// SkillCommands lets the turn-owned slash command registry stay in sync with
	// installed skills without run importing turn directly (turn/run must not
	// import each other). process injects the real turn.RefreshSkills /
	// turn.IsBuiltinName implementations; left zero-valued in tests that do not
	// exercise skill-derived slash commands, which skill.Service tolerates.
	SkillCommands    SkillCommandHooks
	SubagentExecutor SubagentExecutor
	Control          *Controller
	Events           event.Sink
	main             *agent.Agent
	// primaryState is the one published effective primary-model snapshot
	// (primaryModelRuntime) together with the fail-closed health gate for run
	// entry points. It moves only inside the load transaction: the snapshot
	// after a complete build succeeded, the health flag back to false only
	// then. A value type with atomics, so a literal-constructed Runner is
	// usable without initialization.
	primaryState runnerPrimaryState
	// transcript builds what a request of the conversation carries; the agent
	// builds its turns with it and a compaction its summary request.
	transcript transcriptSession
	// summaryLLM is the model chain up to the wrappers that shape a request's
	// prefix, which a compaction's summary request is sent through.
	summaryLLM   llm.LLM
	mainCfg      AgentConfigYAML
	forkLLM      llm.LLM
	FileResolver FileReferenceResolver
	tools        *tool.State
	mu           runnerLocks
	// mcpReg owns this Runner's MCP sessions. Per-Runner rather than the
	// process-wide registry: see mcp.NewRegistry for the cross-tenant teardown
	// bug that ownership fixes (R7).
	mcpReg *mcp.Registry
	mcpSeg *mcpSegment
	// mcpLoad is the generation whose servers are still starting. It is the
	// ready barrier: r.main is not published until it settles, so nothing can
	// observe an agent whose tool table is missing its MCP half (see
	// startOrReplayMCPSegmentLocked).
	mcpLoad *mcpLoad
	// mcpHub fans this Runner's MCP status out to surfaces and survives a
	// generation change, so a surface subscribes once per session rather than
	// once per MCP restart.
	mcpHub mcpStatusHub
	// closed reports that the Runner was shut down: it takes no new MCP load,
	// and a load that is still in flight must not publish into it.
	closed     bool
	userTracer agent.Tracer
	// permRuntime holds the permission decision state (rule store + engine)
	// that used to be two direct fields here (permissionStore/
	// permissionEngine) plus a "perm" third of the split lock below (R3, see
	// docs/plan/TUI_FIRST_REFACTOR_TASKS.md and docs/plan/TUI_FIRST_REFACTOR_PROGRESS.md §1.22).
	// It now guards its own state with its own lock — Runner passes AppCfg/
	// Home/WorkspaceRoot/ProjectRoot in on every call rather than duplicating
	// them, so there remains exactly one owner of that config (I6).
	//
	// permRuntimeOnce guards permRuntime's own lazy creation with a
	// dedicated sync.Once rather than r.mu.load: loadLocked runs under
	// r.mu.load (held by its caller, Load) and itself calls permRuntimeGet,
	// so guarding the pointer's creation with r.mu.load would self-deadlock
	// (RWMutex.Lock is not reentrant) the moment permRuntime had not been
	// created yet when Load ran — confirmed by a real test-suite hang before
	// this was caught and fixed.
	permRuntime     *safety.Runtime
	permRuntimeOnce sync.Once
	subagentFlight  subagentSemaphore
	memPipeline     memory.PipelineHolder
	// foreground counts the user-facing turns this runtime has in flight, so
	// the background memory pipeline can keep its own prompt prefixes out of
	// the provider's cache while the user's conversation is using it.
	foreground foregroundFlight
}

// runnerLocks replaces what was one shared mutex guarding several unrelated
// concerns (R2). It is down to a single sub-lock, because each concern that
// left the Runner took its lock with it rather than continuing to borrow one
// here:
//
//   - "perm" went with the permission cluster into safety.Runtime (R3), which
//     guards it internally.
//   - "fork" went with the per-session cache into forkCacheStore (R7), which
//     bundles the map with the lock so the two cannot be separated.
//
// What is left guards the load cluster. The struct stays rather than
// collapsing back into a bare mutex field because it is where the scope of
// that lock, and the two deliberate exceptions below, are written down.
type runnerLocks struct {
	// load guards main, mainCfg, forkLLM, tools, userTracer, mcpReg and
	// memPipeline. The guardian used to be in this list; it now lives on
	// safety.Runtime, which does its own locking, and actionHook reads it
	// unguarded by design — that relies on a config reload never overlapping
	// an active run (see ConfigManager.applyIfIdle), which this lock does not
	// affect either way. permRuntime's lazy creation deliberately does
	// NOT use this lock — see permRuntimeOnce's comment on the Runner struct
	// for why nesting it inside loadLocked (which already holds load) would
	// self-deadlock.
	load sync.RWMutex
}

func inferMCPToolMeta(_ string, _ string, raw map[string]any) event.ToolMeta {
	annotations, _ := raw["annotations"].(map[string]any)
	readOnlyHint := mcpBoolAnnotation(annotations, "readOnlyHint", "read_only_hint")
	destructiveHint := mcpBoolAnnotation(annotations, "destructiveHint", "destructive_hint")
	openWorldHint := mcpBoolAnnotation(annotations, "openWorldHint", "open_world_hint")
	idempotentHint := mcpBoolAnnotation(annotations, "idempotentHint", "idempotent_hint")

	destructive := mcpToolRequiresApproval(readOnlyHint, destructiveHint, openWorldHint, appcfg.MCPToolApprovalAuto)

	// Only mark concurrency-safe if server explicitly signals both read-only and idempotent.
	// Read-only alone does not imply parallel-safety (e.g., rate-limited search, stateful pagination).
	concurrencySafe := false
	if idempotentHint != nil && *idempotentHint && !destructive {
		concurrencySafe = true
	}

	meta := event.ToolMeta{
		Category:        "mcp",
		ReadOnly:        !destructive,
		Destructive:     destructive,
		ConcurrencySafe: concurrencySafe,
		ReadOnlyHint:    readOnlyHint,
		DestructiveHint: destructiveHint,
		OpenWorldHint:   openWorldHint,
		IdempotentHint:  idempotentHint,
	}
	return meta
}

func mcpToolRequiresApproval(readOnlyHint, destructiveHint, openWorldHint *bool, mode appcfg.MCPToolApprovalMode) bool {
	switch mode.Normalized() {
	case appcfg.MCPToolApprovalApprove:
		return false
	case appcfg.MCPToolApprovalPrompt:
		return true
	case appcfg.MCPToolApprovalWrites:
		return readOnlyHint == nil || !*readOnlyHint
	default:
		if destructiveHint != nil && *destructiveHint {
			return true
		}
		if readOnlyHint != nil && *readOnlyHint {
			return false
		}
		destructive := destructiveHint == nil || *destructiveHint
		openWorld := openWorldHint == nil || *openWorldHint
		return destructive || openWorld
	}
}

func mcpBoolAnnotation(annotations map[string]any, keys ...string) *bool {
	for _, key := range keys {
		if value, ok := annotations[key].(bool); ok {
			copy := value
			return &copy
		}
	}
	return nil
}

func normalizeMCPDelegatingInputSchema(raw any) map[string]any {
	schemaMap, ok := raw.(map[string]any)
	if !ok {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	clone, _ := normalizeMCPSchemaValue(schemaMap, 0).(map[string]any)
	if clone == nil {
		clone = make(map[string]any, 2)
	}
	if _, ok := clone["type"]; !ok {
		clone["type"] = "object"
	}
	if _, ok := clone["properties"]; !ok {
		clone["properties"] = map[string]any{}
	}
	return clone
}

const maxMCPSchemaDepth = 64

// normalizeMCPSchemaValue deep-copies a server-authored schema, rewriting every
// `additionalProperties: true` to false.
//
// The tool pipeline requires object schemas to be closed — ensureToolJSONSchema
// rejects an open one outright — but a third-party server's schema is not ours
// to police, and `additionalProperties: true` is ordinary JSON Schema. Rejecting
// it dropped the tool from the agent entirely, with no diagnostic anywhere.
// Closing it keeps the tool callable and the provider request valid, and costs
// the server nothing: arguments are forwarded verbatim, so a server that accepts
// extra keys still receives whatever the model sends.
//
// The copy is deep because the source map belongs to the MCP client's cached
// tool listing, which must not be mutated.
func normalizeMCPSchemaValue(value any, depth int) any {
	if depth > maxMCPSchemaDepth {
		return value
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if key == "additionalProperties" {
				if open, isBool := item.(bool); isBool && open {
					out[key] = false
					continue
				}
			}
			out[key] = normalizeMCPSchemaValue(item, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = normalizeMCPSchemaValue(item, depth+1)
		}
		return out
	default:
		return value
	}
}

func newMCPDelegatingTool(toolName, description string, inputSchema any, call func(context.Context, json.RawMessage) (string, error)) (*llm.Tool, error) {
	schema := normalizeMCPDelegatingInputSchema(inputSchema)
	tool, err := llm.NewRawTool(toolName, description, schema, func(ctx context.Context, arguments string) (any, error) {
		arguments = strings.TrimSpace(arguments)
		if arguments == "" {
			arguments = "{}"
		}
		return call(ctx, json.RawMessage(arguments))
	})
	if err != nil {
		return nil, err
	}
	return tool.SetContainsExternalContext(true), nil
}

func (r *Runner) Load() error {
	r.mu.load.Lock()
	err := r.loadWithIntentLocked(primaryModelOrdinary, "", "", nil)
	r.mu.load.Unlock()
	if err != nil {
		return err
	}
	// Resolve filesystem policy onto the in-process tools after releasing the
	// load lock: RefreshFilesystemPolicy takes r.mu.load.RLock itself (to
	// read r.tools) and Go's RWMutex is not reentrant.
	r.RefreshFilesystemPolicy()
	return nil
}

func (r *Runner) actionHook(ctx context.Context, kind string, payload any) (string, bool, error) {
	pay := payloadToMap(payload)
	strictAutoReview := r != nil && r.permRuntimeGet().StrictAutoReviewEnabledForSession(
		tool.ConversationSessionIDFromContext(ctx), r.policyRunIDForTool(ctx), r.AppCfg,
	)
	exitPlanMode := strings.EqualFold(strings.TrimSpace(kind), "exit_plan_mode")
	isRequestPermissions := strings.EqualFold(strings.TrimSpace(kind), "request_permissions")
	isApplyPatch := strings.EqualFold(strings.TrimSpace(kind), "apply_patch")
	if exitPlanMode {
		approved, err := r.approvedExitPlanAction(ctx, tool.ApprovedActionIDFromContext(ctx), pay)
		if err != nil {
			return "", false, err
		}
		if approved {
			return tool.ApprovedActionIDFromContext(ctx), false, nil
		}
	} else if tool.ApprovedActionIDFromContext(ctx) != "" {
		return "", false, nil
	}
	// A child run has no branch of its own here. It suspends through the same
	// action queue as the primary runner: the waiting action is attached to the
	// child run ID below and carries the worker's identity, and the
	// orchestration layer preserves the continuation for replay. That includes
	// request_permissions, which children used to be refused outright — a
	// refusal that left a subagent with no way to ask for access it needed, so
	// it abandoned the path it was sent to read instead of putting the question
	// to the operator. A child's grant is bounded by what the operator approves
	// and by the scope they pick: a turn grant binds to the child's own run ID,
	// which is why nothing here has to narrow it after the fact.
	forceToolApproval := exitPlanMode || payloadForcesToolApproval(pay)
	snapshot := r.PermissionSnapshotForSession(tool.ConversationSessionIDFromContext(ctx))
	approvalPolicy := snapshot.ApprovalPolicy
	sessionCfg := safety.ConfigForSnapshot(r.AppCfg, snapshot)
	if isApplyPatch {
		if approved, err := r.applyPatchSessionApproval(ctx, pay); err != nil {
			return "", false, err
		} else if approved {
			return "", false, nil
		}
		constrained := r.sandboxAllowsFileMutation(ctx, "edit_file", pay, safety.Decision{Behavior: safety.BehaviorAsk, Reason: "default_ask"})
		sandboxAvailable := sessionCfg != nil && safety.NewManager().Decide(sessionCfg, safety.ToolKindShell).UseSandbox
		if sessionCfg != nil && sessionCfg.DangerFullAccessEnabled() {
			return "", false, nil
		}
		if constrained && sandboxAvailable && approvalPolicy.Mode != safety.ApprovalUnlessTrusted {
			return "", false, nil
		}
		rejectsSandboxApproval := approvalPolicy.Mode == safety.ApprovalNever ||
			(approvalPolicy.Mode == safety.ApprovalGranular && !approvalPolicy.Granular.SandboxApproval)
		if rejectsSandboxApproval {
			if sessionCfg != nil && sessionCfg.SandboxMode == appcfg.SandboxModeReadOnly {
				return "", false, fmt.Errorf("patch rejected: writing is blocked by read-only sandbox; rejected by user approval settings")
			}
			return "", false, fmt.Errorf("patch rejected: writing outside of the project; rejected by user approval settings")
		}
		if !constrained || !sandboxAvailable {
			pay["sandbox_permissions"] = string(safety.SandboxPermissionsRequireEscalated)
		}
	}
	decision := safety.Decision{Behavior: safety.BehaviorAsk, Reason: "default_ask"}
	if !isApplyPatch {
		decision = r.evaluateToolPermission(tool.ConversationSessionIDFromContext(ctx), kind, pay)
		decision = r.applyMCPToolApprovalPolicy(ctx, kind, decision)
		switch decision.Behavior {
		case safety.BehaviorDeny:
			return "", false, formatPermissionDenyError(decision, kind, pay)
		case safety.BehaviorAllow:
			if !exitPlanMode && !strictAutoReview && (!forceToolApproval || decision.Matched != nil) {
				if isRequestPermissions {
					tool.CapturePolicyApprovalReason(ctx, requestPermissionsApprovalExemptionReason(decision))
				}
				return "", false, nil
			}
		}
	}
	// Leaving plan mode is never a permission question, so no permission bypass
	// answers it. The approval is where the user reads the plan, can send it to
	// a second model, and picks whether the implementation starts from a fresh
	// context — a decision the runtime needs an answer to, not a gate standing
	// in the way of one. That is why the allow-rule branch above and the
	// approval-policy-never branch below both exclude it, and why the tool
	// fails closed when this hook reports that nobody was asked. YOLO belongs
	// with them: bypassing here returned "approved, nothing pending", which the
	// tool correctly refused to act on, and plan mode became impossible to
	// leave.
	if !exitPlanMode && decision.Behavior != safety.BehaviorDeny && r.yoloEnabled() {
		if isRequestPermissions {
			tool.CapturePolicyApprovalReason(ctx, "approvals and sandbox are globally bypassed")
		}
		return "", false, nil
	}
	if !exitPlanMode && forceToolApproval && approvalPolicy.Mode == safety.ApprovalNever {
		return "", false, fmt.Errorf("permission denied: approval policy is never")
	}
	if !forceToolApproval {
		switch approvalPolicy.Mode {
		case safety.ApprovalOnRequest:
			if r.sandboxAllowsFileMutation(ctx, kind, pay, decision) {
				if isRequestPermissions {
					tool.CapturePolicyApprovalReason(ctx, "the requested filesystem access is already available")
				}
				return "", false, nil
			}
		}
	}
	if approvalPolicy.Mode == safety.ApprovalGranular &&
		!granularApprovalPromptEnabled(approvalPolicy.Granular, kind, pay, decision) {
		return "", false, fmt.Errorf("approval prompt disabled by granular approval policy for %q", kind)
	}
	if !exitPlanMode && !isRequestPermissions && r.permissionRequestHookApplies(kind) {
		hookDecision, hookMessage, hookErr := r.runPermissionRequestHook(ctx, kind, pay)
		if hookErr != nil {
			return "", false, hookErr
		}
		switch hookDecision {
		case "allow":
			return "", false, nil
		case "deny":
			return "", false, fmt.Errorf("permission denied by PermissionRequest hook: %s", hookMessage)
		}
	}
	if strictAutoReview && !exitPlanMode {
		if r.permRuntimeGet().Guardian() == nil {
			return "", false, fmt.Errorf("automatic approval review is unavailable")
		}
		transcript, transcriptErr := r.guardianTranscript(ctx)
		if transcriptErr != nil {
			return "", false, fmt.Errorf("automatic approval review failed closed: %w", transcriptErr)
		}
		review, reviewErr := r.permRuntimeGet().Guardian().Review(ctx, tool.RunIDFromContext(ctx), transcript, kind, pay)
		if reviewErr != nil {
			return "", false, reviewErr
		}
		if review.Outcome != "allow" {
			return "", false, fmt.Errorf("permission denied by automatic approval review (%s): %s", review.RiskLevel, review.Rationale)
		}
		return "", false, nil
	}
	if !exitPlanMode && r.autoReviewApprovalsEnabled(approvalPolicy) {
		transcript, transcriptErr := r.guardianTranscript(ctx)
		if transcriptErr != nil {
			return "", false, fmt.Errorf("guardian approval review failed closed: %w", transcriptErr)
		}
		review, reviewErr := r.permRuntimeGet().Guardian().Review(ctx, tool.RunIDFromContext(ctx), transcript, kind, pay)
		if reviewErr != nil {
			return "", false, reviewErr
		}
		if review.Outcome != "allow" {
			if payloadBool(pay, "request_permissions") {
				return "", false, tool.ErrRequestPermissionsDenied
			}
			return "", false, fmt.Errorf("permission denied by guardian (%s): %s", review.RiskLevel, review.Rationale)
		}
		if isRequestPermissions {
			tool.CapturePolicyApprovalReason(ctx, "automatically approved by the auto-review approvals reviewer")
		}
		return "", false, nil
	}
	if r.Actions == nil {
		return "", false, fmt.Errorf("approval required for %q but actions service is unavailable", kind)
	}
	// The action belongs to the conversation the tool call ran in — the
	// session of the run that raised it, which the action row now carries as
	// its own column. When a subagent raised it, the payload still names which
	// worker is asking: someone answering one of three concurrent fanout
	// children has no way to tell which of them is blocked on them otherwise.
	sessionID := strings.TrimSpace(tool.ConversationSessionIDFromContext(ctx))
	if sessionID == "" && r.RunRT != nil {
		if rid := r.policyRunIDForTool(ctx); rid != "" {
			if run, runErr := r.RunRT.GetRun(context.Background(), rid); runErr == nil && run != nil {
				sessionID = strings.TrimSpace(run.SessionID)
			}
		}
	}
	if sessionID == "" {
		return "", false, fmt.Errorf("approval for %q has no conversation to belong to", kind)
	}
	enriched := false
	if agentID := strings.TrimSpace(tool.HookAgentIDFromContext(ctx)); agentID != "" {
		pay["agent_id"] = agentID
		enriched = true
	}
	if subtype := strings.TrimSpace(tool.SubagentTypeFromContext(ctx)); subtype != "" {
		pay["subagent_type"] = subtype
		enriched = true
	}
	if enriched {
		payload = pay
	}
	a, err := r.Actions.CreatePending(context.Background(), sessionID, kind, payload)
	if err != nil {
		return "", false, err
	}
	if r.RunRT != nil {
		rid := r.policyRunIDForTool(ctx)
		if rid != "" {
			toolIn := "{}"
			if b, jerr := json.Marshal(payload); jerr == nil {
				toolIn = string(b)
			}
			if serr := r.RunRT.SetWaitingAction(context.Background(), rid, state.Wait{
				RunID:            rid,
				ActionID:         a.ID,
				ToolName:         strings.TrimSpace(kind),
				ToolInputJSON:    toolIn,
				AgentID:          tool.HookAgentIDFromContext(ctx),
				SubagentType:     tool.SubagentTypeFromContext(ctx),
				SandboxProfile:   payloadString(pay, "sandbox_profile"),
				RequestedProfile: payloadString(pay, "requested_profile"),
				ProfileElevation: payloadBool(pay, "profile_elevation"),
			}); serr != nil {
				return "", false, serr
			}
		}
	}
	return a.ID, true, nil
}

// hookRuntime returns the hook runtime this Runner's tools were loaded with.
//
// The tool state owns it. Load stores it there for the tool middleware and the
// orchestration wrapper, which already reads it back the same way
// (orchestration_llm.go), so a field on the Runner was a second reference to
// one object -- and one that a caller could read before Load had replaced it.
// Nil before the first Load, which is what the field was too.
func (r *Runner) hookRuntime() *hook.Runtime {
	if r == nil || r.tools == nil {
		return nil
	}
	rt, _ := r.tools.RuntimeValue("hooks_runtime").(*hook.Runtime)
	return rt
}

func (r *Runner) runPermissionRequestHook(ctx context.Context, kind string, payload map[string]any) (string, string, error) {
	hookRT := r.hookRuntime()
	if hookRT == nil {
		return "", "", nil
	}
	sessionID := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
	if sessionID == "" {
		sessionID = "default"
	}
	transcriptPath := strings.TrimSpace(tool.HookTranscriptPathFromContext(ctx))
	if transcriptPath == "" {
		var err error
		transcriptPath, err = hook.WriteSessionTranscriptArtifact(hookRT.StateRoot(), hookRT.Sess, sessionID)
		if err != nil {
			return "", "", err
		}
	}
	cwd := strings.TrimSpace(tool.ProjectRootFromContext(ctx))
	if cwd == "" {
		cwd = r.workspaceRoot()
	}
	permissionMode := string(r.PermissionSnapshotForSession(tool.ConversationSessionIDFromContext(ctx)).ApprovalPolicy.Mode)
	toolName, toolInput, aliases := permissionRequestHookPayload(kind, payload)
	outcome, err := hookRT.ExecutePermissionRequest(ctx, hook.PermissionRequestInput{
		BaseInput: hook.BaseInput{
			HookEventName: hook.EventPermissionRequest, SessionID: sessionID,
			TranscriptPath: transcriptPath, Cwd: cwd,
			PermissionMode: permissionMode,
			AgentID:        tool.HookAgentIDFromContext(ctx), AgentType: tool.SubagentTypeFromContext(ctx),
		},
		TurnID: tool.RunIDFromContext(ctx), Model: r.approvalHookModel(ctx),
		ToolName: toolName, ToolInput: toolInput, MatcherAliases: aliases,
	})
	if err != nil {
		return "", "", err
	}
	return outcome.Decision, outcome.Message, nil
}

func (r *Runner) permissionRequestHookApplies(kind string) bool {
	kind = strings.TrimSpace(kind)
	if strings.EqualFold(kind, "shell") || strings.EqualFold(kind, "apply_patch") {
		return true
	}
	if r != nil && r.tools != nil {
		if meta, ok := r.tools.ToolMetaByName(kind); ok && strings.EqualFold(strings.TrimSpace(meta.Category), "mcp") {
			return true
		}
	}
	return strings.HasPrefix(strings.ToLower(kind), "mcp__")
}

func permissionRequestHookPayload(kind string, payload map[string]any) (string, map[string]any, []string) {
	kind = strings.TrimSpace(kind)
	switch {
	case strings.EqualFold(kind, "shell"):
		input := map[string]any{"command": payloadString(payload, "command")}
		description := strings.TrimSpace(payloadString(payload, "justification"))
		if context, port, ok := permissionRequestNetworkTarget(payload); ok {
			target := string(context.Protocol) + "://" + context.Host
			if port > 0 {
				target += ":" + strconv.Itoa(port)
			}
			description = "network-access " + target
		}
		if description != "" {
			input["description"] = description
		}
		return "Bash", input, nil
	case strings.EqualFold(kind, "apply_patch"):
		return "apply_patch", map[string]any{"command": payloadString(payload, "patch")}, []string{"Write", "Edit"}
	default:
		return kind, payload, nil
	}
}

func permissionRequestNetworkTarget(payload map[string]any) (safety.NetworkApprovalContext, int, bool) {
	var context safety.NetworkApprovalContext
	switch value := payload["network_approval_context"].(type) {
	case safety.NetworkApprovalContext:
		context = value
	case *safety.NetworkApprovalContext:
		if value != nil {
			context = *value
		}
	case map[string]any:
		context.Host, _ = value["host"].(string)
		if protocol, ok := value["protocol"].(string); ok {
			context.Protocol = safety.NetworkApprovalProtocol(protocol)
		}
	}
	context.Host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(context.Host), "."))
	if context.Host == "" || !context.Protocol.Valid() {
		return safety.NetworkApprovalContext{}, 0, false
	}
	port := 0
	switch value := payload["network_port"].(type) {
	case int:
		port = value
	case float64:
		if value >= 0 && value <= 65535 && value == math.Trunc(value) {
			port = int(value)
		}
	}
	if port < 0 || port > 65535 {
		return safety.NetworkApprovalContext{}, 0, false
	}
	return context, port, true
}

func (r *Runner) approvalHookModel(ctx context.Context) string {
	if r == nil || r.AppCfg == nil {
		return ""
	}
	if snap := r.primaryState.snapshot.Load(); snap != nil {
		_, model := r.effectiveModelFor(ctx)
		return strings.TrimSpace(model)
	}
	if entry, ok := r.unloadedPrimaryModelFallback(); ok {
		return strings.TrimSpace(entry.Model)
	}
	return ""
}

// unloadedPrimaryModelFallback is the config-order read every primary-model
// consumer keeps for a Runner that never completed a load (test fixtures
// assembled without Load). A loaded runner's truth is the published snapshot;
// this fallback is not a live-selection channel.
func (r *Runner) unloadedPrimaryModelFallback() (appcfg.AgentLLMProviderConfig, bool) {
	providers := resolvedAgentProviders(r.AppCfg, r.activeAgentNameForModel())
	if len(providers) == 0 {
		return appcfg.AgentLLMProviderConfig{}, false
	}
	return providers[0], true
}

// effectivePrimaryClient builds a fresh client from the published effective
// snapshot, not from config order, so an auxiliary model user (explicit
// compaction) cannot run on a provider the conversation is not running on.
func (r *Runner) effectivePrimaryClient() llm.LLM {
	if r == nil {
		return nil
	}
	if snap := r.primaryState.snapshot.Load(); snap != nil {
		chain := llmYAMLsFromResolved([]appcfg.AgentLLMProviderConfig{snap.provider})
		if len(chain) > 0 && chain[0] != nil {
			client, err := NewLLMFromYAML(chain[0])
			if err != nil {
				slog.Warn("build effective primary client", "err", err)
				return nil
			}
			return client
		}
		return nil
	}
	client, _ := NewLLMForAgentType(r.AppCfg, r.activeAgentNameForModel())
	return client
}

func (r *Runner) applyPatchSessionApproval(ctx context.Context, payload map[string]any) (bool, error) {
	if r == nil {
		return false, nil
	}
	paths := approvalPayloadPaths(payload)
	if len(paths) == 0 {
		return false, nil
	}
	for _, path := range paths {
		decision := r.permRuntimeGet().EvaluateRaw(tool.ConversationSessionIDFromContext(ctx), "apply_patch", strings.TrimSpace(path), r.AppCfg)
		if decision.Matched != nil && decision.Behavior == safety.BehaviorDeny {
			return false, formatPermissionDenyError(decision, "apply_patch", map[string]any{"path": path})
		}
		if decision.Matched == nil || decision.Matched.Source != safety.SourceSession ||
			decision.Behavior != safety.BehaviorAllow || !decision.BypassSandbox {
			return false, nil
		}
	}
	return true, nil
}

func (r *Runner) autoReviewApprovalsEnabled(policy safety.ApprovalPolicy) bool {
	if r == nil || r.AppCfg == nil || !strings.EqualFold(strings.TrimSpace(r.AppCfg.ApprovalsReviewer), "auto_review") {
		return false
	}
	return policy.Mode == safety.ApprovalOnRequest || policy.Mode == safety.ApprovalGranular
}

func (r *Runner) guardianTranscript(ctx context.Context) ([]llm.Message, error) {
	if r == nil || r.SessionStore == nil {
		return nil, nil
	}
	sessionID := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
	if sessionID == "" {
		return nil, nil
	}
	return r.SessionStore.ListTranscriptMessages(ctx, sessionID, 40)
}

func granularApprovalPromptEnabled(cfg safety.GranularApprovalConfig, kind string, payload map[string]any, decision safety.Decision) bool {
	if _, ok := payload["network_approval_context"]; ok {
		return true
	}
	if decision.Matched != nil && decision.Matched.Behavior == safety.BehaviorAsk {
		return cfg.Rules
	}
	if payloadBool(payload, "request_permissions") {
		return cfg.RequestPermissions
	}
	if payloadBool(payload, "mcp_elicitation") {
		return cfg.MCPElicitations
	}
	if strings.EqualFold(strings.TrimSpace(payloadString(payload, "category")), "mcp") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(payloadString(payload, "category")), "skill") {
		return cfg.SkillApproval
	}
	return cfg.SandboxApproval
}

func payloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func (r *Runner) approvedExitPlanAction(ctx context.Context, actionID string, payload map[string]any) (bool, error) {
	if r == nil || r.Actions == nil || strings.TrimSpace(actionID) == "" {
		return false, nil
	}
	sessionID, _ := payload["session_id"].(string)
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, nil
	}
	action, err := r.Actions.Get(ctx, actionID)
	if errors.Is(err, state.ErrActionNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if action.Status != state.ActionApproved || !strings.EqualFold(strings.TrimSpace(action.Kind), "exit_plan_mode") {
		return false, nil
	}
	var actionPayload map[string]any
	if err := json.Unmarshal([]byte(action.PayloadJSON), &actionPayload); err != nil {
		return false, nil
	}
	actionSessionID, _ := actionPayload["session_id"].(string)
	return strings.TrimSpace(actionSessionID) == sessionID, nil
}

// errRunnerClosed is what any load on a shut-down Runner returns, so callers
// racing a teardown (the runner pool propagating a config reload past an
// eviction) can classify the answer as "gone" instead of "failed".
var errRunnerClosed = errors.New("runner is closed")

func (r *Runner) loadLocked(candidate *appcfg.AgentLLMProviderConfig) error {
	if r.closed {
		return errRunnerClosed
	}
	name := r.AgentName
	if name == "" {
		name = "main"
	}
	// Merge ~/.forebrain/.env into os.Environ before resolving any ${ENV_VAR}
	// LLM api_key/base_url placeholders below. Callers that reach loadLocked
	// via config.LoadPersisted (e.g. the /model slash command's
	// ApplyModelSelection, config hot-reload) intentionally do NOT merge .env,
	// so without this the placeholder resolves to empty and the LLM call fails
	// with an auth error. Idempotent: re-reads the on-disk .env each call.
	if _, envErr := appcfg.MergeDotEnvToProcess(); envErr != nil {
		slog.Warn("merge dotenv to process for agent load", "err", envErr)
	}
	cfgMap, err := LoadAgentConfigsFromRoot(r.AppCfg)
	if err != nil {
		return err
	}
	cfg, ok := cfgMap[name]
	if !ok {
		return fmt.Errorf("agent %q not found in yaml", name)
	}
	// The effective primary model is the published snapshot's candidate, not
	// config order: rewriting only the chain head leaves every other entry
	// (and AppCfg itself) untouched. llmYAMLsFromResolved allocates fresh
	// values with copied Params, so this cannot mutate AppCfg.
	if candidate != nil {
		head := llmYAMLsFromResolved([]appcfg.AgentLLMProviderConfig{*candidate})
		if len(head) == 0 || head[0] == nil {
			return fmt.Errorf("effective provider %s/%s did not resolve to a chain entry",
				strings.TrimSpace(candidate.Provider), strings.TrimSpace(candidate.Model))
		}
		cfg.LLMChain = append(head, cfg.LLMChain...)
	}
	// The MCP segment is torn down and rebuilt only when its fingerprint
	// changed (see loadMCPSegmentLocked); a reload that leaves the list alone
	// keeps the live sessions, which is both cheaper and what keeps the tool
	// table — and the prompt prefix derived from it — byte-stable across a
	// config reload. Closing here would reach only this Runner's sessions;
	// the process-wide registry is never a teardown target.
	llmClient := llmOverrideForTest
	if llmClient == nil {
		var err error
		llmClient, err = NewLLMForAgentConfig(&cfg)
		if err != nil {
			return err
		}
	}
	r.permRuntimeGet().SetGuardian(nil, "")
	autoReviewer := strings.EqualFold(strings.TrimSpace(r.AppCfg.ApprovalsReviewer), "auto_review")
	guardianLLM, guardianErr := NewLLMForAgentType(r.AppCfg, "guardian")
	if guardianErr != nil {
		if autoReviewer {
			return fmt.Errorf("initialize guardian approval reviewer: %w", guardianErr)
		}
		slog.Warn("initialize automatic approval reviewer", "err", guardianErr)
	} else if guardianLLM != nil {
		r.permRuntimeGet().SetGuardian(guardianLLM, r.AppCfg.AutoReview.Policy)
	} else if autoReviewer {
		return fmt.Errorf("initialize guardian approval reviewer: no LLM configured")
	}
	// Build per-type LLM clients for built-in typed subagents (explore, plan,
	// verification, general-purpose) so each can use a dedicated provider/model
	// configured under agents.definitions[<type>].llm_providers. Types without a
	// dedicated definition fall through to the main agent's LLM transparently
	// via the typedSubagentProviderLLM wrapper.
	typeLLMs := make(map[string]llm.LLM)
	for _, typeName := range agent.PublicTypeNames() {
		def, ok := r.AppCfg.Agents.Definitions[typeName]
		if !ok || len(appcfg.ResolvedLLMConfigs(def)) == 0 {
			continue
		}
		typedLLM, terr := NewLLMForAgentType(r.AppCfg, typeName)
		if terr != nil {
			slog.Warn("failed to build typed subagent LLM, falling back to main", "type", typeName, "err", terr)
			continue
		}
		if typedLLM != nil {
			typeLLMs[typeName] = typedLLM
			slog.Debug("typed subagent LLM configured", "type", typeName)
		}
	}
	// A run's context may carry its session's own model choice; route the
	// base client on it. It sits below the typed-subagent wrapper so types
	// with their own chain keep it, and below every prompt-shaping wrapper so
	// the cached prefix is model-agnostic.
	llmClient = wrapSessionModelLLM(llmClient, r.sessionModelClient)
	llmClient = wrapTypedSubagentProviderLLM(llmClient, typeLLMs)
	// A run may pin its own model (plan review lets the user pick the reviewer),
	// which outranks both the per-type provider above and the main agent's model.
	llmClient = wrapSubagentModelOverrideLLM(llmClient, func(override SubagentModelOverride) (llm.LLM, error) {
		return ConfiguredModelClient(r.AppCfg, r.activeAgentNameForModel(), override.Provider, override.Model)
	})
	llmClient = wrapTypedSubagentPromptLLM(llmClient)
	llmClient = wrapCodegraphPromptLLM(llmClient)
	// The skill roots are resolved once per load, from this runtime's frozen
	// launch project: the same roots feed the catalog in the prompt prefix and
	// the discovery below, so the two can never disagree inside one runtime.
	skillRoots := skill.AgentSkillRoots(r.Home, r.workspaceRoot(), r.LaunchProject)
	// The skills catalog sits beside the memory instruction, ahead of the
	// conversation and frozen per session for the same cache reason. The
	// standing skill-offer criteria join the same frozen block.
	llmClient = wrapSkillCatalogLLM(llmClient, skillRoots, r.workspaceRoot(), r.SessionStore, r.skillOfferGuidance)
	llmClient = wrapMemoryInstructionLLM(llmClient, r.workspaceRoot(), r.ProjectKey, r.AppCfg, r.SessionStore, r.ProjectMemoryOnly())
	llmClient = wrapMemoryCitationLLM(llmClient, r.AppCfg, r.MemoryStore)
	llmClient = wrapTypedSubagentToolFilterLLM(llmClient)
	// Everything wrapped so far shapes the start of a request — system prompt,
	// injected context, the tools a typed subagent sees. A compaction's summary
	// request is sent through exactly this, so it starts byte for byte like the
	// conversation's own requests and reuses their cached prefix; the wrappers
	// below only add reminders at the end of a turn's request and recover a
	// turn's call, and a summary wants neither.
	summaryChain := llmClient
	r.summaryLLM = summaryChain
	// The reminder planModeLLM injects is not written here. It reaches the
	// transcript through the run's own message sequence, which the orchestration
	// loop adopts it into (see reminderAdoptionSink): a mid-call write would
	// land between an assistant tool_calls row stored at an approval gate and the
	// result that answers it, and the dangling-result repair then strips both.
	llmClient = wrapPlanModeLLM(llmClient, r.workspaceRoot(), r.ProjectKey)
	// The skill-offer gate sits outside the plan-mode wrapper: it suppresses
	// itself in plan mode, and when it does inject, both wrappers' reminders
	// reach the live session through the same adoption sink in record order.
	// The tool state is read through a getter because it is built later in
	// this same Load than the chain being wrapped here.
	llmClient = wrapSkillOfferLLM(llmClient, r.AppCfg, func() *tool.State { return r.tools }, r.workspaceRoot(), r.LaunchProject)
	// Build compactDeps for the checkpoint path. SessionID is
	// extracted from context at LLM call time.
	compactSvc := assembly.Service{
		Sessions: r.SessionStore,
		PrimaryModel: func(ctx context.Context) (string, string) {
			return r.effectiveModelFor(ctx)
		},
		CompactLLM: r.sessionClientFor,
		ModelProvider: func(model string) string {
			return ProviderForAgentModel(r.AppCfg, r.activeAgentNameForModel(), model)
		},
		CompactLLMForModel: func(model string) llm.LLM {
			_, c, _ := NewLLMForAgentModel(r.AppCfg, r.activeAgentNameForModel(), model)
			return c
		},
		Prompt:           r.AppCfg.Compact.Prompt,
		RemoteCompaction: r.AppCfg.Compact.UseRemoteCompaction(),
		RemoteV1:         !r.AppCfg.Compact.UseRemoteV2(),
		ExplicitLimit:    r.AppCfg.Compact.ModelAutoCompactTokenLimit,
		LimitScope:       r.AppCfg.Compact.ModelAutoCompactTokenLimitScope,
		PreCompact: func(ctx context.Context, sid, trigger string) error {
			return r.runCompactHook(ctx, hook.EventPreCompact, sid, trigger)
		},
		PostCompact: func(ctx context.Context, sid, trigger string) error {
			return r.runCompactHook(ctx, hook.EventPostCompact, sid, trigger)
		},
		Events: event.SinkFunc(r.publishSurfaceEvent),
	}
	compactDeps := &CompactChainDeps{
		ExplicitLimit: r.AppCfg.Compact.ModelAutoCompactTokenLimit,
		LimitScope:    strings.TrimSpace(r.AppCfg.Compact.ModelAutoCompactTokenLimitScope),
		// Observed window behaviour is per-model, so the checkpoint path needs the
		// same identity the compact service resolves.
		ActiveModel: func(ctx context.Context) (string, string) {
			return r.effectiveModelFor(ctx)
		},
		TryCompact: func(ctx context.Context, msgs []llm.Message, tools []*llm.Tool, reactive bool) ([]llm.Message, bool, error) {
			// Compaction owns model history, so it remains scoped to the worker
			// session. Only permission decisions use ConversationSessionID.
			sessionID := llm.AgentSessionIDFromContext(ctx)
			if sessionID == "" {
				return nil, false, nil
			}
			// The summary request is the request the agent was about to
			// make, with the summary instruction after it.
			conversation := func(ctx context.Context, instruction llm.Message) (*llm.Result, error) {
				return summaryChain.Execute(ctx, append(append([]llm.Message(nil), msgs...), instruction), tools)
			}
			return compactSvc.TryCompactOnMessages(ctx, msgs, sessionID, reactive, conversation)
		},
	}
	// Re-wrap the LLM client with compactDeps wired in.
	llmClient = WrapRecoverableLLM(
		llmClient,
		func(ctx context.Context) (json.RawMessage, bool) {
			if r.tools == nil {
				return nil, false
			}
			sessionID := llm.AgentSessionIDFromContext(ctx)
			if sessionID == "" {
				return nil, false
			}
			return r.tools.GetContextSnapshot(sessionID)
		},
		compactDeps,
	)
	// The dedup lives inside the wrapper this hands it to, which is rebuilt on
	// every Load along with it, so the Runner has no reason to keep a
	// reference of its own.
	llmClient = wrapGuardrailsLLM(llmClient, r.AppCfg, newGuardrailRunDedup())
	stateDir := strings.TrimSpace(r.StateDir)
	if stateDir == "" {
		stateDir = filepath.Join(r.workspaceRoot(), "state")
	}
	allowedRoots := tool.DefaultAllowedRootsForWorkspace(r.workspaceRoot())
	if r.tools == nil {
		r.tools = tool.NewState(allowedRoots...)
	}
	activeWorkspace := r.workspaceRoot()
	var siblingWorkspaces []string
	if resolver, resolveErr := appcfg.NewResolver(strings.TrimSpace(r.Home), r.AppCfg); resolveErr == nil {
		if paths, pathsErr := resolver.Paths(); pathsErr == nil {
			if strings.TrimSpace(paths.Active.WorkspaceRoot) == strings.TrimSpace(activeWorkspace) {
				siblingWorkspaces = paths.SiblingWorkspaces
			}
		}
	}
	r.tools.SetPrimaryWorkspaceBoundary(r.Home, activeWorkspace, siblingWorkspaces)
	r.tools.SetHistoryDir(filepath.Join(stateDir, "file-history"))
	r.tools.SetToolResultDir(filepath.Join(stateDir, tool.SpillDir))
	// Ensure the user's project directory is an allowed root for file and shell tools.
	if pr := strings.TrimSpace(r.ProjectRoot); pr != "" {
		r.tools.PrependAllowedRoot(pr)
	}
	r.tools.ResetToolMiddlewares()
	hookRT := &hook.Runtime{
		Home:          r.Home,
		WorkspaceRoot: r.workspaceRoot(),
		Cfg:           r.AppCfg,
		Sess:          r.SessionStore,
		Actions:       r.Actions,
		NewPromptRunner: func(label string) (hook.PromptRun, error) {
			fac := Factory{
				Home:          r.Home,
				AgentName:     r.AgentName,
				WorkspaceRoot: r.workspaceRoot(),
				ProjectKey:    r.ProjectKey,
				ProjectRoot:   r.ProjectRoot,
				MemoryStore:   r.MemoryStore,
				AppCfg:        r.AppCfg,
			}
			rr := fac.NewIsolatedRunner(label)
			if rr == nil {
				return nil, fmt.Errorf("nil isolated runner")
			}
			if err := rr.Load(); err != nil {
				return nil, err
			}
			return func(ctx context.Context, input string) (string, error) {
				return RunText(rr, ctx, input)
			}, nil
		},
		RunAgentHook: r.RunAgentHook,
		SessionID:    llm.AgentSessionIDFromContext,
		ToolUseID:    tool.ToolUseIDFromContext,
		PermissionMode: func() string {
			if r == nil {
				return "on-request"
			}
			return string(r.permRuntimeGet().Mode(r.AppCfg))
		},
	}
	r.tools.SetRuntimeValue("hooks_runtime", hookRT)
	r.tools.AddToolMiddleware(hook.NewToolMiddleware(hook.ToolMiddlewareOptions{
		Runtime: hookRT,
	}))
	// Content validity comes next, before the approval gate: a SKILL.md that
	// could not be read by any entry point is refused outright rather than
	// approved and then refused, and the check needs no user decision to make.
	r.tools.AddToolMiddleware(skillWriteGuard(skillRoots, func() *tool.State { return r.tools }, r.SkillCommands.IsBuiltin))
	// Hooks run first and may block/tool. Permission evaluation then runs on
	// the effective input before the handler chooses sandboxed execution.
	r.tools.AddToolMiddleware(r.newToolPermissionMiddleware())
	r.forkLLM = r.wrapExecutionLLM(llmClient, false)
	mainLLM := r.wrapExecutionLLM(llmClient, true)
	desc := strings.TrimSpace(cfg.Description)
	if desc == "" {
		desc = "Forebrain Harness agent"
	}
	desc += agent.ScopeDisciplinePrompt
	desc += agentIntermediateToolPromptSuffix
	//desc += agentCollaborationPromptSuffix
	a, err := agent.New(mainLLM, name, desc)
	if err != nil {
		return err
	}
	instructionsFor := r.Deps.ProjectInstructionsFor
	if r.Deps == nil {
		instructionsFor = nil
	}
	r.transcript = transcriptSession{store: r.SessionStore, systemPrompt: desc, resolver: r.FileResolver, projectInstructions: instructionsFor}
	a.SetSessionBuilder(r.transcript.build)
	// The permission runtime is rebuilt on every load regardless of the action
	// service: the rules on disk belong to this agent's workspace, so a load
	// that skipped them would evaluate the previously active agent's approvals.
	// Only the approval prompt itself needs Actions. safety.Runtime.LoadFromDisk
	// guards this reassignment with its own lock (R3), so unlike the R2-era
	// code here, loadLocked does not need to borrow any of Runner's own locks
	// to stay mutually exclusive with a concurrent permission evaluation.
	r.permRuntimeGet().LoadFromDisk(r.AppCfg, r.permPaths())
	if r.Actions != nil {
		r.tools.SetActionHook(r.actionHook)
	}
	rt := &tool.AgentToolRuntime{
		Home: r.Home, WorkspaceRoot: r.workspaceRoot(), ProjectKey: r.ProjectKey, Cfg: r.AppCfg, YOLO: r.yoloEnabled(), Sess: r.SessionStore, Actions: r.Actions, RunRT: r.RunRT,
		PolicyRunID:                  r.policyRunIDForTool,
		PermissionSnapshot:           r.PermissionSnapshot,
		PermissionSnapshotForSession: r.PermissionSnapshotForSession,
		ApplyPermissionUpdate:        r.ApplyPermissionUpdate,
	}
	te := llm.TokenEstimateOptions{}
	if r.AppCfg != nil {
		cfg := r.AppCfg.Agents.Defaults.TokenEstimate
		te = llm.TokenEstimateOptions{Encoding: cfg.Encoding, Model: cfg.Model, TokenizerPath: cfg.TokenizerPath}
	}
	if strings.TrimSpace(te.Encoding) == "" && strings.TrimSpace(te.Model) == "" {
		if len(cfg.LLMChain) > 0 && cfg.LLMChain[0] != nil {
			te.Model = strings.TrimSpace(cfg.LLMChain[0].Model)
		}
	}
	if err := llm.Configure(te); err != nil {
		return err
	}
	_ = tool.RegisterDefaultTools(a, r.tools, rt)
	_ = RegisterSkillTool(a, r.tools)
	_ = RegisterSubagentTool(a, r.subagentFactory())
	// This agent's workspace skills and its trusted project skill root are
	// listed in the catalog; user, ~/.agents, and system roots are listed too but
	// deferred. Either default can be overridden per skill from /skills.
	// Skills are described in the prompt catalog (skillCatalogLLM) and read
	// from disk on demand. The roots are the same expression the catalog
	// wrapper resolved above, so the catalog and the discovered set agree.
	discoveredSkills, skillErr := (skill.Loader{
		Roots:     skillRoots,
		StateRoot: r.workspaceRoot(),
	}).Discover()
	if skillErr != nil {
		slog.Warn("skill discovery incomplete", "err", skillErr)
	}
	nSkill := len(discoveredSkills)
	loadedSkills := make([]tool.LoadedSkill, 0, len(discoveredSkills))
	for _, item := range discoveredSkills {
		loadedSkills = append(loadedSkills, tool.LoadedSkill{
			Name:    item.Name,
			RootDir: item.RootDir,
		})
	}
	// The loaded-skill catalog is the sole source of truth for skill resource
	// access. Replace it atomically so reloads and primary-agent switches cannot
	// retain roots from the previous runtime snapshot.
	r.tools.ReplaceLoadedSkillCatalog(loadedSkills)
	// MCP is the one part of a Load that talks to unbounded external I/O the
	// operator configured: a package manager downloading, a service that is not
	// answering. It therefore runs in the background, and r.main is published
	// only once its half of the tool table is in place — see
	// startOrReplayMCPSegmentLocked. Everything that reads a published agent
	// through RunContent/LoadedTools waits on that barrier, so no fork or hook
	// agent is ever handed a tool table that is missing its MCP tools.
	pendingMCP := r.startOrReplayMCPSegmentLocked(a)
	svc := skill.NewServiceForWorkspace(r.Home, r.workspaceRoot())
	svc.ProjectRoot = r.launchProjectRoot()
	if r.SkillCommands.Refresh != nil {
		svc.OnRefresh = func() error { return r.SkillCommands.Refresh(svc.Home, svc.Workspace(), r.LaunchProject) }
	}
	svc.IsBuiltin = r.SkillCommands.IsBuiltin
	if err := svc.Refresh(); err != nil {
		slog.Warn("skill refresh", "err", err)
	}
	a.SetTracer(pickUserTracer(r.userTracer))
	r.mainCfg = cfg
	if pendingMCP == nil {
		// The segment was reused (or is empty), so this Load is complete.
		r.main = a
	}
	slog.Info("agent load", "skills", nSkill, "mcp_generation", mcpGenerationID(mcpSegmentFingerprint(r.MCPServers, r.mcpProjectDir())), "mcp_pending", pendingMCP != nil)
	// Drop the cached pipeline so the next launch rebuilds it against the
	// config this Load just applied.
	r.memPipeline.Reset()
	return nil
}

const agentIntermediateToolPromptSuffix = "\n\nUse intermediate_tool as a high-frequency working-notes tool while you inspect source code, perform code review, gather evidence, compare alternatives, or develop a fix. Record new findings, important details, hypotheses, review issues, design ideas, and your own reasoning as soon as you discover them. Before replying to the user or wrapping up the task, read the saved intermediate notes back and use them to verify that your final answer and implementation reflect the important findings."

const agentCollaborationPromptSuffix = "\n\nPrefer subagent-based execution when the task benefits from parallel research, bounded implementation ownership, or independent verification. Use direct main-thread execution for small tightly coupled work. Prefer subagent_fanout for independent read-only investigation, subagent_run for bounded one-shot execution, and subagent_continue when an existing worker already owns the relevant context."

func pickUserTracer(t agent.Tracer) agent.Tracer {
	if t == nil {
		return agent.Noop
	}
	return t
}

func (r *Runner) wrapExecutionLLM(inner llm.LLM, bootstrap bool) llm.LLM {
	if inner == nil {
		return nil
	}
	wrapped := wrapForkCaptureLLM(inner)
	var orchestrated llm.LLM
	if bootstrap {
		orchestrated = wrapToolOrchestrationLLMWithHome(wrapped, r.tools, r.Home)
	} else {
		orchestrated = wrapToolOrchestrationLLM(wrapped, r.tools)
	}
	return orchestrated
}

func (r *Runner) SetUserTracer(t agent.Tracer) {
	if r == nil {
		return
	}
	r.mu.load.Lock()
	defer r.mu.load.Unlock()
	r.userTracer = t
	if r.main != nil {
		r.main.SetTracer(pickUserTracer(t))
	}
}

// policyRunIDForTool names the run that is executing this tool call: the run
// whose approval queue a gate belongs to, and whose durable wait row an
// approval writes.
//
// One Runner executes many runs at once — subagent_fanout dispatches its
// children onto the parent's Runner and agent, and each child's tools run there
// while the parent's own turn is still in flight — so this identity is
// per-goroutine and can only come from the executing call's context. Every
// entry point that starts a run installs it there (supervisor.Run for both the
// new-run and existing-run paths, the gateway for the run context it builds
// itself, and runForkSubagent for the child agent it drives directly), which is
// what makes the context the single answer rather than one of several.
func (r *Runner) policyRunIDForTool(ctx context.Context) string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(tool.RunIDFromContext(ctx))
}

func (r *Runner) Run(ctx context.Context, input string) (*agent.Result, error) {
	if err := r.runtimeHealthErr(); err != nil {
		return nil, err
	}
	return r.RunContent(ctx, []llm.ContentPart{llm.Text(input)})
}

func (r *Runner) RunContent(ctx context.Context, parts []llm.ContentPart) (*agent.Result, error) {
	if err := r.runtimeHealthErr(); err != nil {
		return nil, err
	}
	if r != nil {
		// One store read per turn: the conversation's own stored model choice
		// becomes the run's immutable selection, inherited by every child
		// context the run spawns.
		ctx = r.injectSessionModelSelection(ctx)
	}
	if r != nil {
		r.foreground.enter()
		defer r.foreground.leave()
	}
	acc := &internalLLMUsageAccumulator{}
	ctx = WithInternalLLMUsageAccumulator(ctx, acc)
	// The parameters a fork inherits are captured during this turn and used by
	// a fork this turn spawns, so they belong to the turn.
	ctx = WithForkCacheCapture(ctx)
	if r != nil {
		sessionID := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
		ctx = llm.WithExternalContextObserver(ctx, func() {
			r.markExternalContextUsed(context.Background(), sessionID)
		})
	}
	r.mu.load.Lock()
	// The agent is built once per session and never rebuilt from underneath a
	// turn. Its tool definitions and skills catalog sit in the prompt prefix, so
	// reloading it mid-session invalidates the cached system prompt, tool
	// definitions, and every prior turn. A skill written to disk therefore
	// reaches the catalog in the next session, not this one; until then the
	// user can still reach it
	// explicitly with /<skill-name>, which reads SKILL.md from disk and appends
	// it at the tail of the message list where it costs its own tokens once.
	//
	// An unpublished agent is not the same as an unloaded one: a Load whose MCP
	// half is still connecting has not published r.main yet, and loading again
	// here would rebuild the whole agent — skills, permissions, tool table — for
	// a generation that is already on its way to publishing one.
	if r.main == nil && r.mcpLoad == nil {
		if err := r.loadWithIntentLocked(primaryModelOrdinary, "", "", nil); err != nil {
			r.mu.load.Unlock()
			return nil, err
		}
	}
	main := r.main
	r.mu.load.Unlock()
	if main == nil {
		// MCP startup is still running: the tool table is not settled yet, and
		// this is the first request of the session. Wait for the barrier so the
		// prompt prefix is built once, complete, instead of being rebuilt after
		// the servers report in.
		switch err := r.MCPStartup().Wait(ctx); {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil, err
		case err != nil:
			// A required server did not come up. The turn still runs, because
			// the failure is already on its way to the session as a durable MCP
			// failure record — and refusing every turn for the rest of the
			// session because one configured server is down would make the
			// session unusable rather than informative. Non-interactive entries
			// treat the same state as fatal; see RequiredMCPFailures.
		}
		r.mu.load.RLock()
		main = r.main
		r.mu.load.RUnlock()
	}
	if main == nil {
		return nil, errors.New("agent not loaded")
	}
	if len(parts) == 0 {
		parts = []llm.ContentPart{llm.Text("")}
	}
	preloaded, err := r.preloadExplicitSkill(ctx)
	if err != nil {
		// The Skill failure card has already been shown; ending here keeps the
		// turn from spending an LLM request on instructions that never loaded.
		return nil, err
	}
	res, err := main.Run(preloaded, parts...)
	if res != nil {
		// Replace the inner agent's Summary.Usage with the runner-level
		// accumulator snapshot. The inner agent's accumulator (created by
		// agent.Run) only covers LLM calls made after agent.Run starts.
		// Pre-hook LLM calls (context-engine, guardrails, etc.) happen
		// after the runner accumulator is created but before the agent
		// accumulator, so they flow into the runner accumulator via the
		// parent link but are missing from the agent accumulator.
		// Using the runner accumulator ensures the Summary.Usage delivered
		// to the caller (and ultimately to the "Worked for" message)
		// matches the streaming usage deltas accumulated by the tracker.
		// All four figures come from the same snapshot: the cache split is
		// what the persisted row's hit rate is computed from, and taking it
		// from the agent accumulator instead would describe fewer calls
		// than the input and output beside it.
		if res.Summary != nil {
			total := acc.Snapshot()
			if total.InputTokens > 0 || total.OutputTokens > 0 ||
				total.CacheReadInputTokens > 0 || total.CacheCreationInputTokens > 0 {
				res.Summary.Usage.InputTokens = total.InputTokens
				res.Summary.Usage.OutputTokens = total.OutputTokens
				res.Summary.Usage.CacheReadInputTokens = total.CacheReadInputTokens
				res.Summary.Usage.CacheCreationInputTokens = total.CacheCreationInputTokens
			}
		}
		if last, ok := acc.LastResponse(); ok {
			res.LastResponseUsage = &last
		}
	}
	mergeErrorUsageTotal := func(res *agent.Result, err error) (*agent.Result, error) {
		total := acc.Snapshot()
		if total.InputTokens <= 0 && total.OutputTokens <= 0 &&
			total.CacheReadInputTokens <= 0 && total.CacheCreationInputTokens <= 0 {
			return res, err
		}
		if err != nil {
			err = attachUsageToError(err, &total)
		}
		return res, err
	}
	return mergeErrorUsageTotal(res, err)
}

func (r *Runner) subagentFactory() Factory {
	return Factory{
		Home:          r.Home,
		AgentName:     r.AgentName,
		WorkspaceRoot: r.workspaceRoot(),
		ProjectKey:    r.ProjectKey,
		MemoryStore:   r.MemoryStore,
		AppCfg:        r.AppCfg,
		Tools:         r.tools,
		Owner:         r,
	}
}

func (r *Runner) markExternalContextUsed(ctx context.Context, sessionID string) {
	if r == nil {
		return
	}
	reconsolidate, err := memory.MarkPollutedByExternalContext(ctx, r.AppCfg, r.MemoryStore, sessionID)
	if err != nil {
		slog.Debug("memory pollution update failed", "session", sessionID, "err", err)
		return
	}
	if reconsolidate {
		r.LaunchMemoryStartup(sessionID)
	}
}

// Agent returns the published agent, or nil while a Load is still settling. It
// takes the load lock because publication happens on the settling generation's
// own goroutine, not on the caller's.
func (r *Runner) Agent() *agent.Agent {
	if r == nil {
		return nil
	}
	r.mu.load.RLock()
	defer r.mu.load.RUnlock()
	return r.main
}

func (r *Runner) MainYAMLConfig() AgentConfigYAML {
	return r.mainCfg
}

func (r *Runner) ForkLLM() llm.LLM {
	r.mu.load.RLock()
	defer r.mu.load.RUnlock()
	return r.forkLLM
}

// publishSurfaceEvent hands an event to the surface this runner serves. The
// sink is attached by the surface after the runner is built, so it is read at
// publish time; a runner no surface is attached to has nobody to tell.
func (r *Runner) publishSurfaceEvent(ctx context.Context, evt event.RunEvent) error {
	if r.Events == nil {
		return nil
	}
	return r.Events.Publish(ctx, evt)
}

// ContextCompactLLM returns a fresh current-agent-model client without the
// execution wrapper, so a compact request cannot recursively auto-assembly.
// It builds from the conversation's own selection when the context names one,
// falling back to the effective deep-cloned provider snapshot — never
// NewLLMForAgentType, which follows file order.
func (r *Runner) ContextCompactLLM(ctx context.Context) llm.LLM {
	if r == nil {
		return nil
	}
	return r.sessionClientFor(ctx)
}

func (r *Runner) activeAgentNameForModel() string {
	if r == nil || strings.TrimSpace(r.AgentName) == "" {
		return "main"
	}
	return strings.TrimSpace(r.AgentName)
}

// PrimaryModel resolves the active primary agent's provider and model id the
// same way the runtime does, so a status line cannot disagree with what is
// actually running.
//
// A loaded runner answers from the published effective snapshot. A Runner that
// never completed a load (test fixtures) keeps the config-order fallback,
// which expands a provider entry that names several models (Models: [a, b])
// into one entry per model — the old per-surface copies read
// appcfg.PrimaryLLM, which returns the raw first entry untouched, so a config
// that used the Models list form showed an empty model id on /status and
// /model while the runtime was running the first model. Every caller that
// draws "which model am I on" must come through here, not re-read the config.
func PrimaryModel(r *Runner) (string, string) {
	if r == nil || r.AppCfg == nil {
		return "", ""
	}
	if snap := r.primaryState.snapshot.Load(); snap != nil {
		return strings.TrimSpace(snap.provider.Provider), strings.TrimSpace(snap.provider.Model)
	}
	if entry, ok := r.unloadedPrimaryModelFallback(); ok {
		return strings.TrimSpace(entry.Provider), strings.TrimSpace(entry.Model)
	}
	return "", ""
}

// PrimaryEndpoint is the base URL the primary model's requests go to, read
// from the same source PrimaryModel does — the effective snapshot first, the
// config-order fallback only for a Runner that never loaded — so /status
// cannot show an endpoint the runtime is not actually using.
func PrimaryEndpoint(r *Runner) string {
	if r == nil || r.AppCfg == nil {
		return ""
	}
	if snap := r.primaryState.snapshot.Load(); snap != nil {
		return strings.TrimSpace(snap.provider.BaseURL)
	}
	if entry, ok := r.unloadedPrimaryModelFallback(); ok {
		return strings.TrimSpace(entry.BaseURL)
	}
	return ""
}

// SubagentOwnModel names the provider, model and reasoning effort a subagent
// type runs on when that type has an LLM chain of its own, and reports false
// when it has none.
//
// It answers exactly the question the runtime answers when it routes a child's
// call: the typed client map above is built from agent.PublicTypeNames() and
// only for a type whose definition resolves to at least one provider, and
// typedSubagentProviderLLM falls through to the primary agent's client for
// every other type. A surface that draws "which model is this subagent on"
// must come through here rather than re-reading the config, so the display
// cannot claim a model the runtime is not using; false means "whatever the
// primary agent is on", which the surface already knows.
func SubagentOwnModel(r *Runner, agentType string) (provider, model, effort string, ok bool) {
	agentType = strings.TrimSpace(agentType)
	if r == nil || r.AppCfg == nil || agentType == "" {
		return "", "", "", false
	}
	if !slices.Contains(agent.PublicTypeNames(), agentType) {
		return "", "", "", false
	}
	def, defined := r.AppCfg.Agents.Definitions[agentType]
	if !defined {
		return "", "", "", false
	}
	providers := appcfg.ResolvedLLMConfigs(def)
	if len(providers) == 0 {
		return "", "", "", false
	}
	return strings.TrimSpace(providers[0].Provider),
		strings.TrimSpace(providers[0].Model),
		reasoningEffortFromParams(providers[0].Params),
		true
}

// reasoningEffortFromParams reads params.reasoning.effort out of a provider
// entry's provider-native request params, which is where both the primary
// agent's and a typed subagent's effort is configured.
func reasoningEffortFromParams(params appcfg.LLMRequestParams) string {
	if len(params) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(params, &payload); err != nil {
		return ""
	}
	reasoning, ok := payload["reasoning"].(map[string]any)
	if !ok || reasoning == nil {
		return ""
	}
	effort := strings.TrimSpace(fmt.Sprint(reasoning["effort"]))
	if effort == "" || effort == "<nil>" {
		return ""
	}
	return effort
}

func (r *Runner) ContextCompactModelProvider(model string) string {
	if r == nil {
		return ""
	}
	return ProviderForAgentModel(r.AppCfg, r.activeAgentNameForModel(), model)
}

func (r *Runner) ContextCompactLLMForModel(model string) llm.LLM {
	if r == nil {
		return nil
	}
	_, client, _ := NewLLMForAgentModel(r.AppCfg, r.activeAgentNameForModel(), model)
	return client
}

func (r *Runner) runCompactHook(ctx context.Context, event, sessionID, trigger string) error {
	hookRT := r.hookRuntime()
	if hookRT == nil {
		return nil
	}
	transcript, _ := hook.WriteSessionTranscriptArtifact(hookRT.StateRoot(), r.SessionStore, sessionID)
	model := ""
	if snap := r.primaryState.snapshot.Load(); snap != nil {
		_, model = r.effectiveModelFor(ctx)
	} else if entry, ok := r.unloadedPrimaryModelFallback(); ok {
		model = strings.TrimSpace(entry.Model)
	}
	out, err := hookRT.ExecuteCompact(ctx, event, hook.CompactInput{
		BaseInput: hook.BaseInput{HookEventName: event, SessionID: sessionID, TranscriptPath: transcript, Cwd: r.workspaceRoot()},
		Model:     model, Trigger: trigger,
	})
	if err != nil {
		return err
	}
	if out.Blocked {
		return fmt.Errorf("%s hook stopped compaction: %s", event, strings.TrimSpace(out.StopReason))
	}
	return nil
}

func (r *Runner) RunCompactHook(ctx context.Context, event, sessionID, trigger string) error {
	return r.runCompactHook(ctx, event, sessionID, trigger)
}

func (r *Runner) MainAgentDescription() string {
	r.mu.load.RLock()
	defer r.mu.load.RUnlock()
	if r.main != nil {
		return r.main.Description()
	}
	return strings.TrimSpace(r.mainCfg.Description)
}

func (r *Runner) Tools() *tool.State {
	r.mu.load.RLock()
	defer r.mu.load.RUnlock()
	return r.tools
}

// MCPRegistry returns this Runner's own MCP registry, which holds live
// sessions. Callers that need to list a server's tools or resources (e.g. the
// /mcp slash command) must go through this registry rather than
// mcp.GlobalRegistry() -- the global registry is a read-only, session-less
// mirror by design (see Registry.Mirror), so it can never answer a
// ListTools/ListResources call.
func (r *Runner) MCPRegistry() *mcp.Registry {
	if r == nil {
		return nil
	}
	r.mu.load.RLock()
	defer r.mu.load.RUnlock()
	return r.mcpReg
}

// mcpSegment is the reusable MCP half of a Load: the live sessions (owned by
// r.mcpReg) plus the tool definitions built from them. A Load whose effective
// server list and project directory fingerprint is unchanged re-registers the
// cached definitions instead of restarting servers, so a config reload — which
// cannot change the frozen list — neither churns MCP subprocesses nor moves
// one byte of the tool definitions the prompt prefix is derived from.
type mcpSegment struct {
	fingerprint string
	generation  string
	// servers covers every configured server in configuration order, including
	// the ones that failed or were skipped: a failure stays visible in the
	// status surfaces after the barrier settled, it just carries no tools.
	servers []mcpSegmentServer
}

type mcpSegmentServer struct {
	name      string
	transport string
	url       string
	required  bool
	// connected reports whether this server's tools are in the segment.
	connected bool
	tools     []*llm.Tool
	metas     []event.ToolMeta
	// toolCount is what the server advertised in tools/list. It is cached
	// because the reload replay has to report the same number the first load
	// did: reporting the number of tools that survived construction moved the
	// number whenever a server's schema was rejected.
	toolCount int
	// discovery caches the derived-tool definitions for this server. They are
	// what a reload rebuilds, rather than re-deriving them from a live session:
	// a session that could not be reached silently dropped the whole group.
	discovery []mcpDiscoveryTool
}

func (seg *mcpSegment) generationServers() []mcpGenerationServer {
	if seg == nil {
		return nil
	}
	out := make([]mcpGenerationServer, 0, len(seg.servers))
	for _, srv := range seg.servers {
		out = append(out, mcpGenerationServer{name: srv.name, transport: srv.transport, url: srv.url, required: srv.required})
	}
	return out
}

// mcpSegmentFingerprint hashes the effective server list together with the
// directory project-scope entries run from. Scope and credential stamps are
// part of each entry's own fingerprint through ServerFingerprint.
//
// The startup policy (timeout, required) is part of THIS hash and not of
// ServerFingerprint. The two answer different questions: ServerFingerprint is
// the identity a project MCP consent was granted for, so adding a local
// patience dial to it would revoke consent and ask the operator to authorise
// again; this one decides whether the segment is rebuilt, and raising a timeout
// is exactly the case where rebuilding — and retrying the server that timed out
// — is what the operator asked for.
func mcpSegmentFingerprint(servers []appcfg.MCPServerConfig, projectDir string) string {
	h := sha256.New()
	for _, srv := range servers {
		_, _ = io.WriteString(h, strings.TrimSpace(srv.Name))
		_, _ = io.WriteString(h, "\x00")
		_, _ = io.WriteString(h, mcp.ServerFingerprint(srv))
		_, _ = io.WriteString(h, "\x00")
		_, _ = io.WriteString(h, strconv.FormatFloat(srv.StartupTimeout, 'g', -1, 64))
		_, _ = io.WriteString(h, "\x00")
		_, _ = io.WriteString(h, strconv.FormatBool(srv.Required))
		_, _ = io.WriteString(h, "\x00")
	}
	_, _ = io.WriteString(h, "dir="+strings.TrimSpace(projectDir))
	return hex.EncodeToString(h.Sum(nil))
}

// mcpGenerationID names one load generation for display and for the stable
// identity of its failure records. It is derived from the fingerprint, so the
// same configuration keeps the same name across runs of one session and a
// changed configuration never does.
func mcpGenerationID(fingerprint string) string {
	if len(fingerprint) > 12 {
		return fingerprint[:12]
	}
	return fingerprint
}

// mcpServerWorkingDir picks the working directory one server entry runs from:
// the session's project root for project-scope entries (their configuration
// travels with that repository), the process working directory otherwise,
// which is what global entries have always used.
func (r *Runner) mcpServerWorkingDir(srv appcfg.MCPServerConfig, fallback string) string {
	if mcp.IsProjectScope(srv) {
		if dir := strings.TrimSpace(r.MCPProject); dir != "" {
			return dir
		}
	}
	return fallback
}

// mcpGenerationServer is one configured server of a generation. It is held by
// the status hub so a snapshot can list servers in configuration order without
// reading the Runner: a subscriber callback runs inside the registry's delivery
// lock, and taking the load lock there would close a lock cycle against the
// settle path, which holds the load lock while it writes to the registry.
type mcpGenerationServer struct {
	name      string
	transport string
	url       string
	required  bool
}

// MCPSnapshot is one MCP generation as a surface shows it: every configured
// server in configuration order, with its startup state.
type MCPSnapshot struct {
	// Generation identifies the load these servers belong to.
	Generation string `json:"generation,omitempty"`
	// Servers is the effective list in configuration order.
	Servers []mcp.ServerRecord `json:"servers"`
	// Pending reports whether this generation's startup is still running.
	Pending bool `json:"pending"`
	// Progress is the same generation summarised.
	//
	// It travels with the snapshot because a subscriber is handed one from
	// inside a registry's delivery lock, and asking the Runner for the summary
	// there would take the load lock — the one the settling generation holds
	// while it writes to that very registry. A subscriber must be able to render
	// everything it is told without touching the Runner.
	Progress MCPStartupProgress `json:"progress"`
}

// summariseMCPSnapshot derives the progress and the servers of one generation
// from its records alone, so every caller — the Runner reading its registry and a
// subscriber handed a snapshot — computes the same thing the same way, without
// needing the load lock.
func summariseMCPSnapshot(generation string, startedAt time.Time, servers []mcpGenerationServer, records []mcp.ServerRecord) MCPSnapshot {
	byName := make(map[string]mcp.ServerRecord, len(records))
	for _, rec := range records {
		byName[strings.ToLower(strings.TrimSpace(rec.Name))] = rec
	}
	snapshot := MCPSnapshot{Generation: generation}
	progress := MCPStartupProgress{Generation: generation, StartedAt: startedAt}
	for _, srv := range servers {
		rec, ok := byName[strings.ToLower(strings.TrimSpace(srv.name))]
		if !ok {
			rec = mcp.ServerRecord{Name: srv.name, ConnStatus: mcp.ConnStatusUnknown}
		}
		if strings.TrimSpace(rec.Transport) == "" {
			rec.Transport = srv.transport
		}
		if strings.TrimSpace(rec.URL) == "" {
			rec.URL = srv.url
		}
		if !rec.Required {
			rec.Required = srv.required
		}
		snapshot.Servers = append(snapshot.Servers, rec)
		progress.Total++
		switch rec.ConnStatus {
		case mcp.ConnStatusConnected:
			progress.Connected++
		case mcp.ConnStatusError:
			progress.Errored++
		case mcp.ConnStatusCancelled:
			progress.Skipped++
		case mcp.ConnStatusConnecting:
			progress.InFlight++
		case mcp.ConnStatusDisconnected:
			// An idle release, not a startup state: the generation finished long
			// ago and its Runner gave the connection back. Counting it as in
			// flight would put a startup line on screen for something that is
			// not starting, and keep the snapshot Pending forever.
			progress.Released++
		default:
			// A configured server the registry has no record for has not started,
			// which is what "in flight" means here.
			progress.InFlight++
		}
		if rec.ConnStatus == mcp.ConnStatusConnecting {
			if rec.Required {
				progress.RequiredInFlight++
			} else {
				progress.OptionalInFlight++
			}
		}
	}
	snapshot.Pending = progress.InFlight > 0
	snapshot.Progress = progress
	return snapshot
}

// MCPStartupProgress is the startup state of one MCP generation: how many of
// its servers have reached a terminal state, and how many are still starting.
type MCPStartupProgress struct {
	Generation string
	// Total is every configured server of the generation.
	Total int
	// InFlight is the number still starting.
	InFlight  int
	Connected int
	Errored   int
	Skipped   int
	// Released counts servers whose connections an idle Runner gave back. They
	// are not in flight — nothing is starting — and they are not failures: the
	// connection returns on the next request, so a released server must not
	// hold a startup line on screen or fail an unattended run.
	Released int
	// RequiredInFlight counts required servers still starting; OptionalInFlight
	// counts the optional ones, which are the only ones an interactive skip may
	// cancel.
	RequiredInFlight int
	OptionalInFlight int
	// StartedAt is when this generation began starting, zero when it never did.
	StartedAt time.Time
}

// CanSkipOptional reports whether an interactive skip has anything to cancel.
// It is what makes Esc mean "skip the rest I can spare" without pretending a
// required server was skipped.
func (p MCPStartupProgress) CanSkipOptional() bool {
	return p.OptionalInFlight > 0
}

// Settled is how many of the generation's servers reached a terminal state.
func (p MCPStartupProgress) Settled() int {
	if p.InFlight > p.Total {
		return 0
	}
	return p.Total - p.InFlight
}

// MCPRequiredFailure is one required server that did not reach a connected
// state. State is the terminal status it reached and Error the underlying text,
// which is kept verbatim: it is the only copy that came from the server.
type MCPRequiredFailure struct {
	Server string         `json:"server"`
	State  mcp.ConnStatus `json:"state"`
	Error  string         `json:"error,omitempty"`
}

// MCPRequiredStartupError aggregates every required server that failed, so a
// non-interactive run reports all of them at once instead of the first.
type MCPRequiredStartupError struct {
	Failures []MCPRequiredFailure `json:"failures"`
}

func (e *MCPRequiredStartupError) Error() string {
	if e == nil || len(e.Failures) == 0 {
		return "required MCP server startup failed"
	}
	parts := make([]string, 0, len(e.Failures))
	for _, f := range e.Failures {
		if strings.TrimSpace(f.Error) == "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", f.Server, f.State))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%s): %s", f.Server, f.State, f.Error))
	}
	return "required MCP server startup failed: " + strings.Join(parts, "; ")
}

// mcpLoadTask is one server's startup, owned by exactly one mcpLoad.
type mcpLoadTask struct {
	index     int
	srv       appcfg.MCPServerConfig
	name      string
	transport string
	url       string
	dir       string
	required  bool
	timeout   time.Duration
	// ctx is this server's own lifetime: it is cancelled when the generation is
	// abandoned, and — for an optional server only — when the operator skips
	// it.
	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	state     mcp.ConnStatus
	errMsg    string
	session   *mcp.Session
	rawTools  []map[string]any
	toolCount int
	caps      mcp.ServerCapabilities
}

// mcpLoad is one MCP startup generation: the servers being started, their
// contexts, and the ready barrier.
//
// A load owns its own registry, so a superseded generation's late writes land
// in a registry nobody reads instead of mutating the live one.
type mcpLoad struct {
	r           *Runner
	fingerprint string
	generation  string
	reg         *mcp.Registry
	tasks       []*mcpLoadTask
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	started     time.Time

	mu    sync.Mutex
	agent *agent.Agent
	// claimed records that settle has taken the agent above and started building
	// the tool table into it, which is what closes the window a later Load could
	// otherwise use to swap in a different agent mid-settle.
	claimed bool
	skipped map[int]bool
}

func (ld *mcpLoad) generationServers() []mcpGenerationServer {
	if ld == nil {
		return nil
	}
	out := make([]mcpGenerationServer, 0, len(ld.tasks))
	for _, t := range ld.tasks {
		out = append(out, mcpGenerationServer{name: t.name, transport: t.transport, url: t.url, required: t.required})
	}
	return out
}

// published reports whether this load has finished and handed its segment over.
func (ld *mcpLoad) published() bool {
	if ld == nil {
		return true
	}
	select {
	case <-ld.done:
		return true
	default:
		return false
	}
}

// attachAgent points this generation at the agent a later Load built, and
// reports whether it took.
//
// It is refused once the generation has claimed an agent to register its tools
// into: from that moment the MCP half of the tool table is being built into that
// one agent, and pointing the generation at a different one would publish an
// agent whose MCP tools were registered somewhere else. The caller then leaves
// r.main to the generation, which publishes the agent it claimed.
func (ld *mcpLoad) attachAgent(a *agent.Agent) bool {
	if ld == nil {
		return false
	}
	ld.mu.Lock()
	defer ld.mu.Unlock()
	if ld.claimed {
		return false
	}
	ld.agent = a
	return true
}

// claimAgent takes the agent this generation will register its tools into and
// publish. After it, attachAgent is refused.
func (ld *mcpLoad) claimAgent() *agent.Agent {
	if ld == nil {
		return nil
	}
	ld.mu.Lock()
	defer ld.mu.Unlock()
	ld.claimed = true
	return ld.agent
}

// abandon cancels this load and everything it started. The load's own goroutines
// still finish on their own and close the sessions they opened: cancelling is
// what makes them stop, not what makes them disappear.
func (ld *mcpLoad) abandon() {
	if ld == nil {
		return
	}
	ld.cancel()
}

// counts summarises the tasks' terminal states.
func (ld *mcpLoad) counts() (connected, errored, skipped int) {
	if ld == nil {
		return 0, 0, 0
	}
	for _, t := range ld.tasks {
		t.mu.Lock()
		state := t.state
		t.mu.Unlock()
		switch state {
		case mcp.ConnStatusConnected:
			connected++
		case mcp.ConnStatusError:
			errored++
		case mcp.ConnStatusCancelled:
			skipped++
		}
	}
	return connected, errored, skipped
}

// cancelOptional skips every optional server whose startup is still in flight,
// and reports whether it skipped anything new. Required servers are never
// touched here: the operator skipping them would look like a working session
// with silently missing tools, so a required server can only finish, fail, time
// out, or go with the Runner.
func (ld *mcpLoad) cancelOptional() bool {
	if ld == nil {
		return false
	}
	ld.mu.Lock()
	if ld.skipped == nil {
		ld.skipped = make(map[int]bool)
	}
	var cancels []context.CancelFunc
	for _, t := range ld.tasks {
		if t.required || ld.skipped[t.index] {
			continue
		}
		t.mu.Lock()
		pending := t.state == mcp.ConnStatusConnecting
		t.mu.Unlock()
		if !pending {
			continue
		}
		ld.skipped[t.index] = true
		cancels = append(cancels, t.cancel)
	}
	ld.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	return len(cancels) > 0
}

// startOrReplayMCPSegmentLocked prepares the MCP half of a Load. Caller holds
// r.mu.load.
//
// It returns the load whose startup continues in the background, or nil when
// the segment was reused synchronously — a replay of a cached segment, or a
// generation with no servers to start. In the nil case the caller publishes the
// agent immediately; otherwise r.main stays unpublished until the load settles,
// so no observer — including LoadedTools, which fork and hook agents build
// their tool tables from — can see an agent whose MCP tools are still coming.
func (r *Runner) startOrReplayMCPSegmentLocked(a *agent.Agent) *mcpLoad {
	fp := mcpSegmentFingerprint(r.MCPServers, r.mcpProjectDir())
	if ld := r.mcpLoad; ld != nil && ld.fingerprint == fp && !ld.published() {
		// The same generation is already starting: point it at the agent this
		// Load just built and let it publish that one. If it has already claimed
		// an agent to register into, this Load's agent is dropped and the
		// generation publishes the one it claimed — either way exactly one agent
		// is published, and it is the one the MCP tools were built into.
		ld.attachAgent(a)
		r.mcpHub.attach(ld.reg, ld.generationServers(), ld.generation, true, ld.started)
		return ld
	}
	if seg := r.mcpSeg; seg != nil && seg.fingerprint == fp {
		r.replayMCPSegmentLocked(a, seg)
		r.mcpHub.attach(r.mcpReg, seg.generationServers(), seg.generation, false, time.Time{})
		return nil
	}
	if r.mcpLoad != nil {
		r.mcpLoad.abandon()
		r.mcpLoad = nil
	}
	if r.mcpSeg != nil || r.mcpReg != nil {
		r.dropMCPSegmentLocked()
	}
	ld := r.newMCPLoadLocked(a)
	if len(ld.tasks) == 0 {
		// Nothing to start. The empty segment is already the final answer, so
		// publication stays inline and Load keeps its old shape for a session
		// with no MCP servers.
		r.mcpSeg = &mcpSegment{fingerprint: ld.fingerprint, generation: ld.generation}
		r.mcpLoad = nil
		r.mcpHub.attach(ld.reg, nil, ld.generation, false, time.Time{})
		close(ld.done)
		return nil
	}
	go ld.run()
	return ld
}

// newMCPLoadLocked builds one generation: the registry it owns, one task per
// configured server, and the "connecting" record for every one of them.
//
// The records are written before any goroutine is spawned, so a snapshot taken
// at any moment counts the whole list rather than only the servers whose
// goroutine happens to have been scheduled. Caller holds r.mu.load.
func (r *Runner) newMCPLoadLocked(a *agent.Agent) *mcpLoad {
	fp := mcpSegmentFingerprint(r.MCPServers, r.mcpProjectDir())
	ctx, cancel := context.WithCancel(context.Background())
	reg := mcp.NewRegistry()
	ld := &mcpLoad{
		r:           r,
		fingerprint: fp,
		generation:  mcpGenerationID(fp),
		reg:         reg,
		ctx:         ctx,
		cancel:      cancel,
		done:        make(chan struct{}),
		started:     time.Now(),
		agent:       a,
	}
	workspaceDir, err := os.Getwd()
	if err != nil {
		slog.Error("mcp segment: resolve current workspace", "err", err)
		workspaceDir = ""
	}
	for _, entry := range r.MCPServers {
		srv := entry
		// Defense in depth for the assembly-time clamp: a project entry that
		// reached this list unclamped still cannot run with widened approvals,
		// nor decide how long this session waits for it.
		if mcp.IsProjectScope(srv) {
			mcp.ClampProjectRuntimePolicy(&srv)
		}
		name := strings.TrimSpace(srv.Name)
		if name == "" {
			continue
		}
		taskCtx, taskCancel := context.WithCancel(ctx)
		ld.tasks = append(ld.tasks, &mcpLoadTask{
			index:     len(ld.tasks),
			srv:       srv,
			name:      name,
			transport: srv.Transport,
			url:       srv.URL,
			dir:       r.mcpServerWorkingDir(srv, workspaceDir),
			required:  srv.Required,
			timeout:   mcp.StartupTimeoutFor(srv),
			ctx:       taskCtx,
			cancel:    taskCancel,
			state:     mcp.ConnStatusConnecting,
		})
		reg.MarkConnecting(mcp.StatusUpdate{
			Name:       name,
			Transport:  srv.Transport,
			URL:        srv.URL,
			Required:   srv.Required,
			Generation: ld.generation,
		})
		ld.mirror(name)
	}
	r.mcpReg = reg
	r.mcpSeg = nil
	r.mcpLoad = ld
	r.mcpHub.attach(reg, ld.generationServers(), ld.generation, true, ld.started)
	return ld
}

// dropMCPSegmentLocked retires the cached segment and the registry that owned
// its sessions. Caller holds r.mu.load.
//
// The sessions are closed off the calling goroutine: a stdio close walks the
// spec's stdin/SIGTERM/SIGKILL sequence and can spend seconds per server, and
// the caller is a Load, which is on the path to the next frame. The registry is
// dropped from the Runner first, so nothing can adopt a connection it is
// closing.
func (r *Runner) dropMCPSegmentLocked() {
	r.mcpSeg = nil
	reg := r.mcpReg
	r.mcpReg = nil
	if reg == nil {
		return
	}
	go reg.Close()
}

// run starts one goroutine per server, then publishes. Completion order is not
// registration order: results are held per server index and applied in
// configuration order, so the tool table — and the prompt prefix derived from
// it — does not depend on which server answered first.
func (ld *mcpLoad) run() {
	var wg sync.WaitGroup
	for _, t := range ld.tasks {
		wg.Add(1)
		go func(t *mcpLoadTask) {
			defer wg.Done()
			ld.runTask(t)
		}(t)
	}
	wg.Wait()
	ld.settle()
}

func (ld *mcpLoad) runTask(t *mcpLoadTask) {
	ctx, cancel := context.WithTimeout(t.ctx, t.timeout)
	defer cancel()
	cli, err := mcp.Start(ctx, ld.r.Home, t.dir, t.srv)
	if err != nil {
		t.finishError(ld, err)
		return
	}
	// Account MCP response compression against the same state root the shell
	// tools use, so both kinds share one retrieval-id namespace and one savings
	// report.
	cli.SetStateRoot(ld.r.StateRoot())
	tools, err := cli.ListToolMetas(ctx)
	if err != nil {
		// Failure path: the process is signalled before the close, so the
		// close does not spend the SDK's terminate duration waiting on a server
		// that is not answering.
		_ = cli.Abort()
		t.finishError(ld, err)
		return
	}
	if t.ctx.Err() != nil {
		// Skipped (or the generation was abandoned) while the listing was in
		// flight: the connection is not wanted.
		_ = cli.Abort()
		t.finishSkipped(ld)
		return
	}
	caps := cli.Capabilities()
	t.mu.Lock()
	t.session = cli
	t.rawTools = tools
	t.toolCount = len(tools)
	t.caps = caps
	t.state = mcp.ConnStatusConnected
	t.mu.Unlock()
	// The server is connected now, and the status says so now. Holding the
	// transition until the whole generation settled left every surface reporting
	// "0 of 3 settled" for as long as the slowest server took, and naming servers
	// as "connecting" that had already answered — so the one number a reader
	// watches to decide whether to wait said nothing until there was nothing left
	// to wait for.
	//
	// The registry takes ownership of the connection here too, which is what
	// makes "recorded as connected" and "owned" the same moment. A generation
	// that is abandoned before it settles closes both the task's handle and the
	// registry's; a closed registry refuses the write and closes the connection
	// it was offered, so neither path can leave a child process with no owner.
	update := ld.statusFor(t)
	update.Status = mcp.ConnStatusConnected
	update.AuthStatus = mcp.AuthStatusAuthenticated
	update.ToolCount = len(tools)
	update.Session = cli
	ld.reg.Apply(update)
	ld.mirror(t.name)
}

// mirror publishes one server's current record into the process-wide status
// view the commands read. The owning registry stays the single fact source: this
// copies what it holds, and never a session.
func (ld *mcpLoad) mirror(name string) {
	ld.reg.Mirror(mcp.GlobalRegistry(), name)
}

func (ld *mcpLoad) statusFor(t *mcpLoadTask) mcp.StatusUpdate {
	return mcp.StatusUpdate{
		Name:       t.name,
		Transport:  t.transport,
		URL:        t.url,
		Required:   t.required,
		Generation: ld.generation,
	}
}

func (t *mcpLoadTask) finishError(ld *mcpLoad, err error) {
	if t.ctx.Err() != nil {
		t.finishSkipped(ld)
		return
	}
	msg := err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		// Keep the original text: it is the standard sentence every Go program
		// reports for an expired deadline, and the operator needs the deadline
		// named as well as what expired.
		msg = fmt.Sprintf("startup timed out after %s: %v", t.timeout, err)
	}
	t.mu.Lock()
	t.state = mcp.ConnStatusError
	t.errMsg = msg
	t.mu.Unlock()
	update := ld.statusFor(t)
	update.Error = msg
	ld.reg.MarkError(update, errors.New(msg))
	ld.mirror(t.name)
}

func (t *mcpLoadTask) finishSkipped(ld *mcpLoad) {
	t.mu.Lock()
	t.state = mcp.ConnStatusCancelled
	t.errMsg = ""
	t.mu.Unlock()
	ld.reg.MarkCancelled(ld.statusFor(t))
	ld.mirror(t.name)
}

// settle registers the connected servers' tools into the agent, in
// configuration order, and then publishes the agent and the segment as one
// step. Until that step nothing outside can see either, which is what makes the
// tool table — and therefore the cached prompt prefix — immutable for the
// session once its first request goes out.
func (ld *mcpLoad) settle() {
	r := ld.r
	reg := ld.reg
	agent := ld.claimAgent()
	seg := &mcpSegment{fingerprint: ld.fingerprint, generation: ld.generation}
	tools := 0
	for _, t := range ld.tasks {
		t.mu.Lock()
		state, sess := t.state, t.session
		t.mu.Unlock()
		// The registry already holds this server's state and its connection: the
		// task recorded both the moment it connected, so a surface did not have
		// to wait for the whole generation to learn that one server was up.
		connected := state == mcp.ConnStatusConnected && sess != nil
		cached, n := r.buildMCPSegmentServer(reg, agent, t, connected)
		tools += n
		seg.servers = append(seg.servers, cached)
	}
	elapsed := time.Since(ld.started)
	r.mu.load.Lock()
	if r.mcpLoad != ld || r.closed {
		// Superseded (or shut down) while starting: none of this belongs to the
		// generation that is serving, so its connections are closed rather than
		// adopted by it.
		r.mu.load.Unlock()
		ld.closeSessions()
		close(ld.done)
		return
	}
	r.mcpSeg = seg
	r.main = agent
	r.mcpLoad = nil
	r.mu.load.Unlock()
	r.mcpHub.markSettled()
	connected, errored, skipped := ld.counts()
	slog.Info("agent tools",
		"generation", ld.generation, "mcp", tools,
		"servers", len(ld.tasks), "connected", connected, "error", errored, "cancelled", skipped,
		"elapsed", elapsed.Round(time.Millisecond))
	close(ld.done)
}

// closeSessions closes every connection this load opened and empties its
// registry, which is the teardown for a generation that never served.
func (ld *mcpLoad) closeSessions() {
	if ld == nil {
		return
	}
	for _, t := range ld.tasks {
		t.mu.Lock()
		sess := t.session
		t.session = nil
		t.mu.Unlock()
		if sess != nil {
			_ = sess.Close()
		}
	}
	if ld.reg != nil {
		ld.reg.Close()
	}
}

// buildMCPSegmentServer builds one server's delegating tools, registers them
// into the agent, and returns the cache entry that lets a later Load restore
// them without contacting the server.
func (r *Runner) buildMCPSegmentServer(reg *mcp.Registry, a *agent.Agent, t *mcpLoadTask, connected bool) (mcpSegmentServer, int) {
	cached := mcpSegmentServer{
		name:      t.name,
		transport: t.transport,
		url:       t.url,
		required:  t.required,
		connected: connected,
		toolCount: t.toolCount,
	}
	if !connected || a == nil {
		return cached, 0
	}
	cached.discovery = mcpDiscoveryToolsFor(t.caps)
	call := mcpToolCaller(reg, t.name)
	registered := 0
	for _, tm := range t.rawTools {
		tn, _ := tm["name"].(string)
		if tn == "" {
			continue
		}
		toolLocal := tn
		desc, _ := tm["description"].(string)
		toolName := mcp.BuildToolName(t.name, tn)
		desc = mcp.CapDescription(desc)
		tt, err := newMCPDelegatingTool(toolName, desc+" (MCP "+t.name+")", tm["inputSchema"], func(ctx context.Context, arguments json.RawMessage) (string, error) {
			return call(ctx, toolLocal, arguments)
		})
		if err != nil {
			// Without this the tool just disappears from the agent: the
			// server connects, its tool is absent, and nothing says why.
			slog.Warn("mcp tool skipped: unusable input schema", "server", t.name, "tool", toolLocal, "err", err)
			continue
		}
		// Advertise the schema the tool actually carries, not the server's raw
		// one: normalization can change it, and callers must not be shown a
		// shape the tool would not accept.
		inputSchemaBytes, _ := json.Marshal(tt.InputSchema())
		if r.tools.Register(a, tt) != nil {
			continue
		}
		meta := inferMCPToolMeta(toolLocal, desc, tm)
		meta.MCPApprovalMode = string(t.srv.ToolApprovalMode(toolLocal))
		meta.Name = toolName
		meta.Description = strings.TrimSpace(desc) + " (MCP " + t.name + ")"
		meta.InputSchema = inputSchemaBytes
		stored := event.ToolMeta{
			Name:            meta.Name,
			Description:     meta.Description,
			Category:        meta.Category,
			ReadOnly:        meta.ReadOnly,
			ConcurrencySafe: meta.ConcurrencySafe,
			InputSchema:     meta.InputSchema,
			Destructive:     meta.Destructive,
			ReadOnlyHint:    meta.ReadOnlyHint,
			DestructiveHint: meta.DestructiveHint,
			OpenWorldHint:   meta.OpenWorldHint,
			IdempotentHint:  meta.IdempotentHint,
			MCPApprovalMode: meta.MCPApprovalMode,
		}
		r.tools.RegisterToolMeta(stored)
		cached.tools = append(cached.tools, tt)
		cached.metas = append(cached.metas, stored)
		registered++
	}
	registered += registerMCPDiscoveryTools(a, r.tools, t.name, cached.discovery, mcpDiscoveryCaller(reg, t.name))
	return cached, registered
}

// replayMCPSegmentLocked re-registers the cached MCP tool definitions into a
// newly built agent without contacting any server. The sessions stay owned by
// r.mcpReg exactly as they were.
//
// Everything it needs comes from the cache — the definitions, the derived tools
// and the advertised tool count — because anything re-derived at replay time
// (from a session that may no longer be reachable, say) would produce a
// different tool table from the one the session's cached prompt prefix was
// built on.
func (r *Runner) replayMCPSegmentLocked(a *agent.Agent, seg *mcpSegment) int {
	if seg == nil {
		return 0
	}
	reg := r.mcpReg
	n := 0
	for _, srv := range seg.servers {
		if !srv.connected {
			continue
		}
		for _, tt := range srv.tools {
			if r.tools.Register(a, tt) == nil {
				n++
			}
		}
		for _, meta := range srv.metas {
			r.tools.RegisterToolMeta(meta)
		}
		n += registerMCPDiscoveryTools(a, r.tools, srv.name, srv.discovery, mcpDiscoveryCaller(reg, srv.name))
	}
	// The process-wide view is refreshed from this generation's own records: it
	// is shared with every other Runner, and another one may have overwritten
	// the same server names since this segment started.
	if reg != nil {
		reg.Republish()
	}
	return n
}

// mcpStatusHub fans one generation's registry out to surface subscribers. It
// exists because the generation is not stable: a config reload replaces the
// registry and its sessions, and a surface that had subscribed directly to the
// old registry would simply stop hearing about MCP for the rest of the session.
type mcpStatusHub struct {
	mu         sync.Mutex
	next       uint64
	subs       map[uint64]func(MCPSnapshot)
	reg        *mcp.Registry
	detach     func()
	generation string
	servers    []mcpGenerationServer
	pending    bool
	// startedAt is when the attached generation began starting, which is what a
	// status line counts from. The hub holds it because a subscriber may not ask
	// the Runner (see MCPSnapshot.Progress).
	startedAt time.Time
}

func (h *mcpStatusHub) attach(reg *mcp.Registry, servers []mcpGenerationServer, generation string, pending bool, startedAt time.Time) {
	if h == nil {
		return
	}
	h.mu.Lock()
	old := h.detach
	h.detach = nil
	h.reg = reg
	h.generation = generation
	h.servers = servers
	h.pending = pending
	h.startedAt = startedAt
	h.mu.Unlock()
	if old != nil {
		old()
	}
	if reg == nil {
		return
	}
	cancel := reg.Subscribe(h.onRegistrySnapshot)
	h.mu.Lock()
	if h.reg == reg {
		h.detach = cancel
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	// A newer generation was attached while this one subscribed; the newer
	// attach owns the hub now.
	cancel()
}

// onRegistrySnapshot turns one registry delivery into an ordered snapshot and
// hands it to every subscriber. It runs inside the registry's delivery lock, so
// it only touches the hub's own state: reading the Runner here would take the
// load lock in the opposite order to the settle path and deadlock the two.
func (h *mcpStatusHub) onRegistrySnapshot(records []mcp.ServerRecord) {
	if h == nil {
		return
	}
	h.mu.Lock()
	snapshot := h.snapshotLocked(records)
	subs := h.subscribersLocked()
	h.mu.Unlock()
	for _, fn := range subs {
		fn(snapshot)
	}
}

func (h *mcpStatusHub) snapshotLocked(records []mcp.ServerRecord) MCPSnapshot {
	if len(records) == 0 {
		// The generation's registry is closed: its servers are gone, not
		// unknown. Reporting them as unknown would leave a startup line on
		// screen that can never finish.
		return MCPSnapshot{Generation: h.generation}
	}
	snapshot := summariseMCPSnapshot(h.generation, h.startedAt, h.servers, records)
	if snapshot.Pending && !h.pending {
		// Startup finished; the records are all terminal.
		snapshot.Pending = false
	}
	return snapshot
}

func (h *mcpStatusHub) subscribersLocked() []func(MCPSnapshot) {
	if len(h.subs) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(h.subs))
	for id := range h.subs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]func(MCPSnapshot), 0, len(ids))
	for _, id := range ids {
		out = append(out, h.subs[id])
	}
	return out
}

// markSettled ends this generation's startup for subscribers. The records may
// not change again after the last server settles, so the pending flag needs a
// delivery of its own.
func (h *mcpStatusHub) markSettled() {
	if h == nil {
		return
	}
	h.mu.Lock()
	reg := h.reg
	if !h.pending {
		h.mu.Unlock()
		return
	}
	h.pending = false
	h.mu.Unlock()
	if reg != nil {
		h.onRegistrySnapshot(reg.ListStatus())
	}
}

// close detaches from the generation and tells subscribers the servers are
// gone, so a surface does not keep a startup line for a Runner that stopped.
func (h *mcpStatusHub) close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	detach := h.detach
	h.detach = nil
	h.reg = nil
	h.pending = false
	h.servers = nil
	h.startedAt = time.Time{}
	subs := h.subscribersLocked()
	h.subs = nil
	h.mu.Unlock()
	if detach != nil {
		detach()
	}
	empty := MCPSnapshot{}
	for _, fn := range subs {
		fn(empty)
	}
}

// subscribeMCPStatus registers fn for this Runner's MCP snapshots — the current
// one immediately, then every later change, across generation changes — and
// returns the call that unsubscribes.
func (h *mcpStatusHub) subscribeMCPStatus(fn func(MCPSnapshot)) func() {
	if h == nil || fn == nil {
		return func() {}
	}
	h.mu.Lock()
	id := h.next
	h.next++
	if h.subs == nil {
		h.subs = make(map[uint64]func(MCPSnapshot))
	}
	h.subs[id] = fn
	h.mu.Unlock()
	if reg := h.currentRegistry(); reg != nil {
		// Replay what the generation looks like right now; the next delivery is
		// the registry's own immediate one, which is why this can be a snapshot
		// taken a moment earlier.
		h.mu.Lock()
		snapshot := h.snapshotLocked(reg.ListStatus())
		h.mu.Unlock()
		fn(snapshot)
	}
	return func() {
		h.mu.Lock()
		delete(h.subs, id)
		h.mu.Unlock()
	}
}

func (h *mcpStatusHub) currentRegistry() *mcp.Registry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reg
}

// MCPStartup is one Runner's MCP startup as a surface sees it: what the current
// generation looks like, the barrier in front of its first request, and the skip
// an interactive surface may offer.
//
// It is a view rather than a set of Runner methods because the Runner's own
// surface is ratcheted (pkg/architecture: adding a method there is a decision
// about the type's shape, not a convenience), and none of this is the Runner's
// business — it is the MCP half of it.
type MCPStartup struct {
	runner *Runner
}

// MCPStartup returns this Runner's MCP startup view. It is never nil, so a
// caller that only observes state does not have to check.
func (r *Runner) MCPStartup() *MCPStartup {
	return &MCPStartup{runner: r}
}

// PublishedTools returns the tool table the startup has published, or nil
// while the first load is still assembling it. Unlike Runner.LoadedTools it
// never waits: it is what a display reads — the /mcp and /status panels on the
// terminal's main loop — and a display must not stall its surface behind an
// MCP startup that is still connecting.
func (v *MCPStartup) PublishedTools() []*llm.Tool {
	r := v.runner
	if r == nil {
		return nil
	}
	r.mu.load.RLock()
	defer r.mu.load.RUnlock()
	return cloneToolSlice(debuglogToolsView(r.main))
}

// Subscribe registers a surface for this Runner's MCP status. fn runs on the
// goroutine a status change came from — including inside a registry's delivery
// lock — so it must not block and must not call back into the Runner. Surfaces
// enqueue onto their own loop.
func (v *MCPStartup) Subscribe(fn func(MCPSnapshot)) func() {
	if v == nil || v.runner == nil {
		return func() {}
	}
	return v.runner.mcpHub.subscribeMCPStatus(fn)
}

// Snapshot returns the current generation's servers in configuration order,
// with their startup state.
func (v *MCPStartup) Snapshot() MCPSnapshot {
	if v == nil || v.runner == nil {
		return MCPSnapshot{}
	}
	r := v.runner
	r.mu.load.RLock()
	reg := r.mcpReg
	serverList := r.mcpGenerationServersLocked()
	generation := ""
	if r.mcpLoad != nil {
		generation = r.mcpLoad.generation
	} else if r.mcpSeg != nil {
		generation = r.mcpSeg.generation
	}
	r.mu.load.RUnlock()
	records := []mcp.ServerRecord(nil)
	if reg != nil {
		records = reg.ListStatus()
	}
	if len(records) == 0 {
		return MCPSnapshot{}
	}
	startedAt := r.mcpHub.startedAtValue()
	return summariseMCPSnapshot(generation, startedAt, serverList, records)
}

// startedAtValue reports when the attached generation began starting.
func (h *mcpStatusHub) startedAtValue() time.Time {
	if h == nil {
		return time.Time{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.startedAt
}

// mcpGenerationServersLocked names the servers of the generation the Runner is
// currently on: the one starting, or the one it last published. Caller holds
// r.mu.load.
func (r *Runner) mcpGenerationServersLocked() []mcpGenerationServer {
	if r.mcpLoad != nil {
		return r.mcpLoad.generationServers()
	}
	if r.mcpSeg != nil {
		return r.mcpSeg.generationServers()
	}
	return nil
}

// Progress reports the current generation's startup state.
func (v *MCPStartup) Progress() MCPStartupProgress {
	if v == nil || v.runner == nil {
		return MCPStartupProgress{}
	}
	return v.Snapshot().Progress
}

// Wait blocks until the current MCP generation's startup has settled.
//
// The error distinguishes the three outcomes a caller must tell apart: the
// caller's own cancellation, an aggregate of the required servers that did not
// come up, and a settled generation with nothing pending.
func (v *MCPStartup) Wait(ctx context.Context) error {
	if v == nil || v.runner == nil {
		return nil
	}
	r := v.runner
	for {
		r.mu.load.RLock()
		ld := r.mcpLoad
		r.mu.load.RUnlock()
		if ld == nil {
			return v.requiredError()
		}
		select {
		case <-ld.done:
			// Settled, or superseded by a newer generation: either way, read
			// again rather than report a state that is already gone.
			continue
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// CancelOptional skips the optional servers that are still starting and reports
// whether it skipped anything. Required servers are left alone.
func (v *MCPStartup) CancelOptional() bool {
	if v == nil || v.runner == nil {
		return false
	}
	r := v.runner
	r.mu.load.RLock()
	ld := r.mcpLoad
	r.mu.load.RUnlock()
	if ld == nil {
		return false
	}
	return ld.cancelOptional()
}

// ReleaseIdleConnections tears this Runner's settled MCP connections down and
// reports whether it released any. settle is how long the Runner must have been
// running no foreground turn — the pool passes its own idleness threshold and a
// short settle, so the two clocks agree that nobody is talking to this Runner
// before anything is taken away from it.
//
// Nothing the session was to the conversation is lost: the segment's cached
// tool definitions stay, the records keep their handles, and the first request
// through a released session grows the connection back (bounded by the server's
// own startup timeout). What goes away is the resource nobody was using — the
// child process and its transport of a server this Runner has not been asked
// about for a while.
//
// A generation still starting is never touched: its barrier is the one thing
// every surface is waiting on. A turn that begins after the idle check but
// before the release is also safe — its first tool call pays one bounded
// reconnect instead of failing, which is the documented cost of a released
// connection rather than an error.
func (v *MCPStartup) ReleaseIdleConnections(settle time.Duration) bool {
	if v == nil || v.runner == nil {
		return false
	}
	r := v.runner
	r.mu.load.RLock()
	ld, reg := r.mcpLoad, r.mcpReg
	r.mu.load.RUnlock()
	if ld != nil || reg == nil {
		return false
	}
	if !r.foreground.idleFor(settle) {
		return false
	}
	released := reg.ReleaseSessions()
	if released > 0 {
		// The process-wide view is shared with every other Runner; leaving the
		// stale connected records in it would have /status report servers as
		// live that this Runner put down.
		reg.Republish()
	}
	return released > 0
}

// RequiredFailures reports every required server of the current generation
// whose startup did not succeed, in configuration order.
func (v *MCPStartup) RequiredFailures() []MCPRequiredFailure {
	if v == nil || v.runner == nil {
		return nil
	}
	var out []MCPRequiredFailure
	for _, rec := range v.Snapshot().Servers {
		if !rec.Required || rec.ConnStatus == mcp.ConnStatusConnected {
			continue
		}
		if rec.ConnStatus == mcp.ConnStatusConnecting {
			// Still starting: not a failure yet.
			continue
		}
		if rec.ConnStatus == mcp.ConnStatusDisconnected {
			// Released while idle, not failed: the server came up when the
			// generation started, and its connection returns on the next
			// request. Failing an unattended run over it would turn a resource
			// decision into an outage.
			continue
		}
		out = append(out, MCPRequiredFailure{Server: rec.Name, State: rec.ConnStatus, Error: rec.Error})
	}
	return out
}

func (v *MCPStartup) requiredError() error {
	failures := v.RequiredFailures()
	if len(failures) == 0 {
		return nil
	}
	return &MCPRequiredStartupError{Failures: failures}
}

// IdleFor reports whether nothing of this Runner's is running: no MCP load in
// flight and no foreground turn that ended within settle. It is the probe a
// teardown decision uses before it commits — the same facts
// ReleaseIdleConnections acts on, as an answer instead of an action.
func (v *MCPStartup) IdleFor(settle time.Duration) bool {
	if v == nil || v.runner == nil {
		return false
	}
	r := v.runner
	r.mu.load.RLock()
	ld := r.mcpLoad
	r.mu.load.RUnlock()
	return ld == nil && r.foreground.idleFor(settle)
}

// Close shuts this Runner down: it takes no new MCP load, cancels the startup
// that is in flight and waits it out, then closes the connections it owns.
//
// The order matters. Startup is cancelled and drained before the registry is
// closed, because a load whose task is still connecting would otherwise adopt a
// connection after the teardown and leave a child process running with no
// owner. The wait is bounded: a server that ignores cancellation must not make
// shutting down an install hang, and the log says so when it happens.
func (r *Runner) Close() error {
	if r == nil {
		return nil
	}
	r.mu.load.Lock()
	if r.closed {
		r.mu.load.Unlock()
		return nil
	}
	r.closed = true
	ld := r.mcpLoad
	reg := r.mcpReg
	r.mcpLoad = nil
	r.mcpSeg = nil
	r.mcpReg = nil
	r.main = nil
	r.mu.load.Unlock()
	r.mcpHub.close()
	if ld != nil {
		ld.abandon()
		select {
		case <-ld.done:
		case <-time.After(mcpCloseGracePeriod):
			slog.Warn("mcp startup did not stop in time", "generation", ld.generation)
		}
	}
	if reg != nil {
		reg.Close()
	}
	return nil
}

// mcpCloseGracePeriod bounds how long a Runner's Close waits for a cancelled
// startup to unwind. Every connection a task holds is closed as it returns, so
// exceeding it means a server or a transport ignored cancellation.
const mcpCloseGracePeriod = 15 * time.Second

// mcpProjectDir is the directory project-scope MCP entries run from.
func (r *Runner) mcpProjectDir() string {
	return strings.TrimSpace(r.MCPProject)
}

// ProjectMemoryOnly reports whether this runtime's launch project restricts
// memory to its own scope.
func (r *Runner) ProjectMemoryOnly() bool {
	if r == nil || r.Deps == nil {
		return false
	}
	return r.Deps.ProjectMemoryOnly
}

// ProjectInstructionsFor returns the frozen project instructions for one
// session, or "" when none apply.
func (r *Runner) ProjectInstructionsFor(sessionID string) string {
	if r == nil || r.Deps == nil || r.Deps.ProjectInstructionsFor == nil {
		return ""
	}
	return strings.TrimSpace(r.Deps.ProjectInstructionsFor(sessionID))
}

// MCPProjectStatus returns the project-level MCP view for display. See
// Deps.MCPDiagnostics; a Runner without a hook reports nothing, which
// renders as "no project view" rather than guessing.
func (r *Runner) MCPProjectStatus() mcp.ProjectMCPScopeSummary {
	if r == nil || r.Deps == nil || r.Deps.MCPDiagnostics == nil {
		return mcp.ProjectMCPScopeSummary{}
	}
	return r.Deps.MCPDiagnostics()
}

// LoadedTools returns the tools registered on the loaded agent.
//
// Derived on demand rather than cached in a field: the cached copy was taken
// once at the end of Load, so anything registering a tool afterwards — a skill
// activating mid-session — left it stale, and nothing detected that. Reading
// through the agent means the answer cannot drift from what the agent actually
// holds.
//
// It waits for a startup that is still in flight, because this is the tool
// table a fork or hook agent is built from: handing one of those a table
// without the MCP tools would give a child a different tool set — and a
// different prompt prefix — from the parent that spawned it. The wait is
// bounded by the servers' own startup timeouts.
func (r *Runner) LoadedTools() []*llm.Tool {
	if r == nil {
		return nil
	}
	r.mu.load.RLock()
	pending := r.main == nil && r.mcpLoad != nil
	r.mu.load.RUnlock()
	if pending {
		_ = r.MCPStartup().Wait(context.Background())
	}
	r.mu.load.RLock()
	defer r.mu.load.RUnlock()
	return cloneToolSlice(debuglogToolsView(r.main))
}

func (r *Runner) workspaceRoot() string {
	if ws := strings.TrimSpace(r.WorkspaceRoot); ws != "" {
		return ws
	}
	return filepath.Join(strings.TrimSpace(r.Home), "workspace")
}

// launchProjectRoot is the root of this runtime's frozen launch project, or
// empty when the session has none. LaunchProject carries the trust decision
// and ProjectRoot the plain directory; both are frozen with the session by the
// composition root, and either order of preference resolves the same root
// wherever both are set.
func (r *Runner) launchProjectRoot() string {
	if r == nil {
		return ""
	}
	if root := strings.TrimSpace(r.LaunchProject.Project.Root); root != "" {
		return root
	}
	return strings.TrimSpace(r.ProjectRoot)
}

// StateRoot returns the per-agent state root (the runner's workspace root) onto
// which the mode/plan/todo stores join "state". Callers that hold a *Runner but
// not the cfg (e.g. the subagent bridge) use this so they resolve the SAME root
// the runner itself uses for its tools and plan-mode wrapper.
func (r *Runner) StateRoot() string {
	return r.workspaceRoot()
}
