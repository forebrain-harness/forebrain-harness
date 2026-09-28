// Tool registration: the default set, the agent registry, and runtime wiring.
package tool

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
)

func DefaultAllowedRootsForWorkspace(workspaceRoot string) []string {
	var roots []string
	if wd, err := os.Getwd(); err == nil && wd != "" {
		roots = append(roots, wd)
	}
	if ws := strings.TrimSpace(workspaceRoot); ws != "" {
		roots = append(roots, ws)
	}
	return roots
}

func MergeAllowedRootPaths(roots []string, more []string) []string {
	seen := make(map[string]struct{}, len(roots)+len(more))
	out := make([]string, 0, len(roots)+len(more))
	for _, r := range roots {
		r = filepath.Clean(strings.TrimSpace(r))
		if r == "" {
			continue
		}
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	for _, r := range more {
		r = filepath.Clean(strings.TrimSpace(r))
		if r == "" {
			continue
		}
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}

// registerToolMeta records the routing metadata the tool-call
// scheduler read, taking the input schema from the live tool so what callers are
// shown can never drift from what the tool accepts.
//
// Call it next to the tool's own registration. Registering metadata somewhere
// else means the two can be reached under different conditions, leaving either a
// phantom entry naming a tool that was never registered or a registered tool the
// scheduler cannot classify (and therefore never runs concurrently).
func registerToolMeta(st *State, tool *llm.Tool, desc, category string, flags event.ToolMeta) {
	if st == nil || tool == nil {
		return
	}
	flags.Name = tool.Name()
	flags.Description = desc
	flags.Category = category
	if schema, err := json.Marshal(tool.InputSchema()); err == nil {
		flags.InputSchema = schema
	}
	st.RegisterToolMeta(flags)
}

func RegisterDefaultTools(a ToolAdder, st *State, rt *AgentToolRuntime) error {
	if a == nil || st == nil {
		return nil
	}
	if rt != nil && rt.PermissionSnapshotForSession != nil {
		st.SetRuntimeValue("permission_snapshot_for_session", rt.PermissionSnapshotForSession)
	} else if rt != nil && rt.PermissionSnapshot != nil {
		st.SetRuntimeValue("permission_snapshot", rt.PermissionSnapshot)
	}
	if rt != nil && memory.ReadPathEnabled(rt.Cfg) {
		// StateRoot, not Home: widening the read allowlist to a home-derived
		// memory root would let this agent's read_file reach every other
		// primary agent's memory. Scoped to this session's own project plus
		// the agent's global preferences — never the whole memories/ tree — so
		// a plain read_file/grep cannot reach another project's memory either.
		if roots, err := memory.ResolveRootsForAgent(rt.StateRoot()); err == nil {
			readRoots := []string{roots.Scope(memory.GlobalScope()).MemoryRoot}
			if key := strings.TrimSpace(rt.ProjectKey); key != "" {
				readRoots = append(readRoots, roots.Scope(memory.Scope{Kind: memory.ScopeProject, Key: key}).MemoryRoot)
			}
			rt.AdditionalReadRoots = MergeAllowedRootPaths(rt.AdditionalReadRoots, readRoots)
			st.SetAdditionalReadRoots(rt.AdditionalReadRoots)
		}
	}
	registerMeta := func(tool *llm.Tool, desc, category string, flags event.ToolMeta) {
		registerToolMeta(st, tool, desc, category, flags)
	}

	t1, err := NewFileReadTool(st)
	if err != nil {
		return err
	}
	registerMeta(t1, "Use when you need a file's contents, or before any write/edit (required first)", "filesystem", event.ToolMeta{ReadOnly: true, ConcurrencySafe: true})
	t2, err := NewFileWriteTool(st, rt)
	if err != nil {
		return err
	}
	registerMeta(t2, "Use when creating a new file or fully replacing one's contents (requires prior read to overwrite)", "filesystem", event.ToolMeta{Destructive: true})
	t3, err := NewFileEditTool(st)
	if err != nil {
		return err
	}
	registerMeta(t3, "Use when making a targeted change to an existing file via exact string replace (requires prior read)", "filesystem", event.ToolMeta{Destructive: true})
	t7, err := NewShellTool(st, rt)
	if err != nil {
		return err
	}
	registerMeta(t7, shellToolDescription, "shell", event.ToolMeta{Destructive: true, InterruptBehavior: "interrupt"})
	tRetrieve, err := NewRetrieveOutputTool(rt)
	if err != nil {
		return err
	}
	registerMeta(tRetrieve, retrieveOutputToolDescription, "shell", event.ToolMeta{ReadOnly: true, ConcurrencySafe: true})
	t16, err := NewWebFetchTool(st, rt)
	if err != nil {
		return err
	}
	t16.SetContainsExternalContext(true)
	registerMeta(t16, "Use when you need the contents of a specific public URL (HTTP(S), SSRF-protected)", "web", event.ToolMeta{ReadOnly: true, ConcurrencySafe: true})
	t17, err := NewWebSearchTool(st, rt)
	if err != nil {
		return err
	}
	t17.SetContainsExternalContext(true)
	registerMeta(t17, "Use when you need current information from the public web (Tavily, Brave, or Baidu Qianfan)", "web", event.ToolMeta{ReadOnly: true, ConcurrencySafe: true})
	var t21 *llm.Tool
	if requestPermissionsToolEnabled(rt) {
		t21, err = NewRequestPermissionsTool(st, rt)
		if err != nil {
			return err
		}
		registerMeta(t21, "Use when additional filesystem or network permissions are required for later file or shell operations", "permissions", event.ToolMeta{ReadOnly: true})
	}
	for _, t := range []*llm.Tool{t1, t2, t3} {
		_ = st.Register(a, t)
	}
	_ = st.Register(a, t7)
	_ = st.Register(a, tRetrieve)
	_ = st.Register(a, t16)
	_ = st.Register(a, t17)
	if t21 != nil {
		_ = st.Register(a, t21)
	}
	return registerSessionAndMemoryTools(a, st, rt)
}

func requestPermissionsToolEnabled(rt *AgentToolRuntime) bool {
	return rt != nil && rt.Cfg != nil && rt.Cfg.EffectiveFeatures().RequestPermissionsTool
}

type ToolAdder interface {
	AddTool(*llm.Tool) error
}

type MiddlewareProvider interface {
	ToolMiddlewares() []llm.ToolMiddleware
}

// AddToolMiddleware attaches a middleware to the tools this state registers.
//
// A middleware closes over the runtime that created it — the permission one
// carries a specific agent's rules and approval path — so it belongs to that
// runtime's state, not to the process. While these lived in a package-level
// slice, a second runtime in the same process inherited the first one's
// middlewares: two primary agents shared one agent's permissions, and a test
// ran its tools through a runner that had already finished.
func (s *State) AddToolMiddleware(mw llm.ToolMiddleware) {
	if s == nil || mw == nil {
		return
	}
	s.mu.Lock()
	s.toolMiddlewares = append(s.toolMiddlewares, mw)
	s.mu.Unlock()
}

// ResetToolMiddlewares drops what a previous load attached, which is what makes
// reloading an agent replace its middlewares rather than stack another copy.
func (s *State) ResetToolMiddlewares() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.toolMiddlewares = nil
	s.mu.Unlock()
}

// ToolMiddlewares is the chain a tool registered through this state runs in:
// this runtime's own middlewares, wrapped in the telemetry that observes every
// tool call in the process. A nil state contributes no runtime middlewares,
// which is the honest answer for a registration that belongs to no runtime —
// there are no permissions to apply on behalf of nobody.
func (s *State) ToolMiddlewares() []llm.ToolMiddleware {
	var extra []llm.ToolMiddleware
	if s != nil {
		s.mu.Lock()
		extra = append(extra, s.toolMiddlewares...)
		s.mu.Unlock()
	}
	mws := make([]llm.ToolMiddleware, 0, len(extra)+3)
	mws = append(mws, telemetry.NewToolDebugFileLogMiddleware())
	mws = append(mws, extra...)
	mws = append(mws, telemetry.NewToolTraceMiddleware())
	return mws
}

// providerToolNamePattern is the tool-name grammar accepted by the OpenAI
// Responses and Chat Completions APIs. A name outside it is rejected up front
// with invalid_request_error, which fails the whole turn rather than just the
// offending tool, so registration is refused here instead.
var providerToolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func add(a ToolAdder, t *llm.Tool, providers ...MiddlewareProvider) error {
	if a == nil {
		return fmt.Errorf("nil agent")
	}
	if t == nil {
		return fmt.Errorf("nil tool")
	}
	if name := t.Name(); !providerToolNamePattern.MatchString(name) {
		return fmt.Errorf("invalid tool name %q: must match %s", name, providerToolNamePattern)
	}
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		t.Use(provider.ToolMiddlewares()...)
	}
	return a.AddTool(t)
}

// Register adds a tool to an agent, wrapped in the middlewares s carries.
//
// It is a method rather than a package function because the chain a tool runs
// in is the registering runtime's, and naming that runtime at the call site is
// what keeps one runtime's permissions off another runtime's tools.
func (s *State) Register(a ToolAdder, t *llm.Tool) error {
	return add(a, t, s)
}

type AgentToolRuntime struct {
	Home                         string
	WorkspaceRoot                string
	AdditionalReadRoots          []string
	ProjectKey                   string
	Cfg                          *appcfg.Root
	YOLO                         bool
	Sess                         *state.SessionStore
	Actions                      *state.ActionService
	RunRT                        *state.RunStore
	PolicyRunID                  func(ctx context.Context) string
	PermissionSnapshot           func() safety.Snapshot
	PermissionSnapshotForSession func(string) safety.Snapshot
	ApplyPermissionUpdate        func(safety.PermissionUpdate)
}

// StateRoot returns the per-agent state root onto which the mode/plan/todo/
// intermediate stores join "state". It is the agent's workspace root, falling
// back to <home>/workspace for the main agent. Every store-backed tool must use
// this (not rt.Home) so a non-main primary agent gets its own isolated state.
func (rt *AgentToolRuntime) StateRoot() string {
	if rt == nil {
		return ""
	}
	if ws := strings.TrimSpace(rt.WorkspaceRoot); ws != "" {
		return ws
	}
	if home := strings.TrimSpace(rt.Home); home != "" {
		return filepath.Join(home, "workspace")
	}
	return ""
}

//go:embed filters/*.toml
var builtinFiltersFS embed.FS

// BuiltinFilterDocs returns the embedded Boost-derived TOML filter documents,
// keyed by file name. These are the exact declarative filters carved out of
// the Boost v0.10.5 binary (see docs/plan/TOKEN_OPTIMIZATION.md for provenance).
func BuiltinFilterDocs() map[string]string {
	docs := map[string]string{}
	entries, err := fs.ReadDir(builtinFiltersFS, "filters")
	if err != nil {
		return docs
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		b, err := builtinFiltersFS.ReadFile(path.Join("filters", e.Name()))
		if err != nil || len(b) == 0 {
			continue
		}
		docs[e.Name()] = string(b)
	}
	return docs
}

func registerProductTools(a ToolAdder, st *State, rt *AgentToolRuntime) error {
	if a == nil || st == nil || rt == nil {
		return nil
	}
	t1, err := newUserInteractionTool(st, rt)
	if err != nil {
		return err
	}
	registerToolMeta(st, t1, "Use when blocked on a decision only the user can make: asks structured questions via the action queue", "session", event.ToolMeta{ReadOnly: true})
	_ = st.Register(a, t1)
	return nil
}

func newUserInteractionTool(st *State, rt *AgentToolRuntime) (*llm.Tool, error) {
	askTool, err := New(WithInteractor(func(ctx context.Context, in *Input) (map[string]Answer, error) {
		if st == nil || rt == nil || rt.Actions == nil {
			return nil, fmt.Errorf("user_interaction unavailable")
		}
		aid := strings.TrimSpace(ApprovedActionIDFromContext(ctx))
		if aid != "" {
			act, err := rt.Actions.Get(ctx, aid)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(act.Kind) != userInteractionToolName {
				return nil, fmt.Errorf("wrong action kind")
			}
			if act.Status != state.ActionAnswered {
				return nil, fmt.Errorf("answers not ready")
			}
			out, err := askAnswerJSONToHumanOutput(act.AnswerJSON)
			if err != nil {
				return nil, err
			}
			return out.Answers, nil
		}
		form, err := humanInputToAskForm(in)
		if err != nil {
			return nil, err
		}
		form.Kind = userInteractionToolName
		form.SessionID = strings.TrimSpace(ConversationSessionIDFromContext(ctx))
		form.AgentID = strings.TrimSpace(HookAgentIDFromContext(ctx))
		form.SubagentType = strings.TrimSpace(SubagentTypeFromContext(ctx))
		a, err := rt.Actions.CreateAsk(ctx, form)
		if err != nil {
			return nil, err
		}
		if rt.RunRT != nil {
			rid := ""
			if rt.PolicyRunID != nil {
				rid = strings.TrimSpace(rt.PolicyRunID(ctx))
			} else {
				rid = strings.TrimSpace(RunIDFromContext(ctx))
			}
			if rid != "" {
				if serr := rt.RunRT.SetWaitingAction(context.Background(), rid, state.Wait{
					RunID:         rid,
					ActionID:      a.ID,
					ToolName:      userInteractionToolName,
					ToolInputJSON: a.PayloadJSON,
					AgentID:       form.AgentID,
					SubagentType:  form.SubagentType,
				}); serr != nil {
					return nil, serr
				}
			}
		}
		return nil, &RequiresActionError{
			ActionID:     a.ID,
			ActionKind:   userInteractionToolName,
			ToolName:     userInteractionToolName,
			AgentID:      form.AgentID,
			SubagentType: form.SubagentType,
			ToolInput: map[string]any{
				"action_id": a.ID,
				"kind":      userInteractionToolName,
			},
		}
	}))
	if err != nil {
		return nil, err
	}
	return askTool.Tool(), nil
}

func VisibleToolsForSubagentSubtype(subtype string, toolNames []string) []string {
	normalized := strings.ToLower(strings.TrimSpace(subtype))
	if len(toolNames) == 0 {
		return nil
	}
	out := make([]string, 0, len(toolNames))
	for _, name := range toolNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if ToolVisibleForSubagentSubtype(normalized, name) {
			out = append(out, name)
		}
	}
	return out
}

func ToolVisibleForSubagentSubtype(subtype, toolName string) bool {
	name := strings.TrimSpace(toolName)
	normalized := strings.ToLower(strings.TrimSpace(subtype))
	return !toolBlockedForSubtype(normalized, name)
}

func toolBlockedForSubtype(subtype, toolName string) bool {
	blockedByType, ok := typedSubagentDisallowedTools[strings.ToLower(strings.TrimSpace(subtype))]
	if !ok {
		return false
	}
	_, blocked := blockedByType[strings.TrimSpace(toolName)]
	return blocked
}

type ToolNameMatchMode string

const (
	ToolNameMatchNone  ToolNameMatchMode = "none"
	ToolNameMatchExact ToolNameMatchMode = "exact_name"
	ToolNameMatchAlias ToolNameMatchMode = "canonical_alias"
)
