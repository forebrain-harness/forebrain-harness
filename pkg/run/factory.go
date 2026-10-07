// Building runners: the factory that assembles a Runner and the isolated
// runners subagents get.
package run

import (
	"path/filepath"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/google/uuid"
)

type Factory struct {
	Home          string
	AgentName     string
	WorkspaceRoot string
	ProjectKey    string
	ProjectRoot   string
	StateRoot     string
	MemoryStore   *memory.Store
	AppCfg        *appcfg.Root
	Tools         *tool.State
	Owner         *Runner
}

func (f Factory) NewIsolatedRunner(label string) *Runner {
	root := strings.TrimSpace(f.StateRoot)
	if root == "" {
		// Subagents inherit the parent primary agent's workspace, so their
		// state lives under <wsRoot>/state/subagents, not <home>/state.
		root = filepath.Join(f.workspaceRoot(), "state", "subagents")
	}
	id := uuid.NewString()
	dir := id
	if label = strings.TrimSpace(label); label != "" {
		dir = label + "-" + id
	}
	var actions = f.ownerActionService()
	var sessionStore = f.ownerSessionStore()
	var runRT = f.ownerRunRT()
	var subagentExecutor = f.ownerSubagentExecutor()
	var control = f.ownerControl()
	codeIntel, codeIntelControl, codeIntelTool := f.ownerCodeIntel()
	return &Runner{Deps: &Deps{Home: f.Home, AgentName: f.AgentName, WorkspaceRoot: f.workspaceRoot(), StateDir: filepath.Join(root, dir), Actions: actions, MCPServers: f.ownerMCPServers(), MCPProject: f.ownerMCPProject(), MemoryStore: f.MemoryStore, AppCfg: f.AppCfg, SessionStore: sessionStore, RunRT: runRT, ProjectKey: f.projectKey(), ProjectRoot: f.projectRoot(), CodeIntel: codeIntel, CodeIntelControl: codeIntelControl, CodeIntelTool: codeIntelTool}, SubagentExecutor: subagentExecutor, Control: control, FileResolver: f.ownerFileResolver()}
}

func (f Factory) workspaceRoot() string {
	if ws := strings.TrimSpace(f.WorkspaceRoot); ws != "" {
		return ws
	}
	if f.Owner != nil {
		return f.Owner.workspaceRoot()
	}
	return filepath.Join(strings.TrimSpace(f.Home), "workspace")
}

// projectKey returns the launch-project identity a subagent must inherit: the
// factory's own value if set, otherwise the owning runner's.

func (f Factory) projectKey() string {
	if pk := strings.TrimSpace(f.ProjectKey); pk != "" {
		return pk
	}
	if f.Owner != nil {
		return strings.TrimSpace(f.Owner.ProjectKey)
	}
	return ""
}

func (f Factory) ActiveWorkspaceRoot() string {
	return f.workspaceRoot()
}

// subagentScopeRoot returns the root that identifies which agent owns a
// subagent — both the on-disk ledger and the in-memory live registry are keyed
// by it. It is the agent workspace, not Home, for the same reason the plan-mode
// entry above is: every reader resolves the per-agent root, so a Home-rooted
// write lands where nobody reads. The ledger also holds delegated task text and
// subagent output verbatim, which is one tenant's data.
//
// Returns "" when nothing configured a real root — workspaceRoot() degrades to
// the relative "workspace" for a bare Factory literal, and a ledger scattered
// into the process cwd is worse than no ledger.
func (f Factory) subagentScopeRoot() string {
	root := strings.TrimSpace(f.workspaceRoot())
	if !filepath.IsAbs(root) {
		return ""
	}
	return root
}

// projectRoot returns the user's project directory — the directory where the
// user's actual code lives. This is distinct from the forebrain home directory
// (Home, ~/.forebrain) and the agent's workspace state root (workspaceRoot,
// ~/.forebrain/workspace): neither of those is the user's project.
//
// Resolution order:
//  1. f.ProjectRoot — explicitly set (e.g. by ChatSession.SetWorkingDir via
//     the parent Runner, or by NewIsolatedRunner inheriting it).
//  2. f.Owner.ProjectRoot — the parent Runner's explicitly captured launch
//     cwd, used when subagentFactory() doesn't set ProjectRoot directly but
//     sets Owner.
//  3. f.Tools.AllowedRoots()[0] — fallback for paths that never call
//     SetWorkingDir (gateway). This is os.Getwd() captured at
//     the Runner's first Load() (DefaultAllowedRootsForWorkspace puts the
//     cwd first).
//
// The path-constraint system prompt for subagents advertises this value as
// the "project root" so the LLM knows it can access the user's project
// files. Using the explicit ProjectRoot (rather than Home) fixes the bug
// where subagents were told only ~/.forebrain was accessible.
func (f Factory) projectRoot() string {
	if pr := strings.TrimSpace(f.ProjectRoot); pr != "" {
		return pr
	}
	if f.Owner != nil {
		if pr := strings.TrimSpace(f.Owner.ProjectRoot); pr != "" {
			return pr
		}
	}
	if f.Tools != nil {
		if roots := f.Tools.AllowedRoots(); len(roots) > 0 {
			return strings.TrimSpace(roots[0])
		}
	}
	return ""
}

// appConfig resolves the configuration a subagent spawned from this factory
// answers to. The owning runner is authoritative when present — it holds the
// configuration that was actually loaded for the active agent — and a detached
// factory falls back to the copy it carries.
func (f Factory) appConfig() *appcfg.Root {
	if f.Owner != nil {
		return f.Owner.AppCfg
	}
	return f.AppCfg
}

func (f Factory) ownerActionService() *state.ActionService {
	if f.Owner != nil {
		return f.Owner.Actions
	}
	return nil
}

func (f Factory) ownerSessionStore() *state.SessionStore {
	if f.Owner != nil {
		return f.Owner.SessionStore
	}
	return nil
}

func (f Factory) ownerRunRT() *state.RunStore {
	if f.Owner != nil {
		return f.Owner.RunRT
	}
	return nil
}

func (f Factory) ownerMCPServers() []appcfg.MCPServerConfig {
	if f.Owner != nil && len(f.Owner.MCPServers) > 0 {
		return append([]appcfg.MCPServerConfig(nil), f.Owner.MCPServers...)
	}
	return nil
}

// ownerMCPProject hands a subagent the directory project-scope MCP entries
// run from, mirroring ownerMCPServers: a subagent that restarts a
// project-scope server must run it from the same place its parent does.
func (f Factory) ownerMCPProject() string {
	if f.Owner != nil {
		return strings.TrimSpace(f.Owner.MCPProject)
	}
	return ""
}

// ownerCodeIntel hands a subagent its parent's language-server runtime:
// the child works in the same project, and a child whose tool table
// differed from its parent's would break a fork's shared prompt prefix.
func (f Factory) ownerCodeIntel() (tool.CodeIntelligence, tool.CodeIntelControl, bool) {
	if f.Owner != nil && f.Owner.Deps != nil {
		return f.Owner.CodeIntel, f.Owner.CodeIntelControl, f.Owner.CodeIntelTool
	}
	return nil, nil, false
}

func (f Factory) ownerSubagentExecutor() SubagentExecutor {
	if f.Owner != nil {
		return f.Owner.SubagentExecutor
	}
	return nil
}

func (f Factory) ownerControl() *Controller {
	if f.Owner != nil {
		return f.Owner.Control
	}
	return nil
}

func (f Factory) ownerFileResolver() FileReferenceResolver {
	if f.Owner != nil {
		return f.Owner.FileResolver
	}
	return nil
}
