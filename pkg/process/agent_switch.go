// Package agentswitch owns the act of making a primary agent active.
//
// Switching primaries is an isolation boundary, not a label change: the file
// tools' allowed roots, the sibling-workspace denial list, runtime permission
// grants, and the sandbox manager's view of those permissions must all move
// together. Each surface used to re-implement that sequence, which is how they
// drifted — one aborted when the runner failed to reload while the other logged
// and carried on with a half-switched runner. Every surface now calls Apply so
// the boundary is rebuilt the same way no matter who asked.
package process

import (
	"fmt"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

// AgentDeps are the process-wide objects a switch rebinds. Runner is required;
// the rest are optional so a surface only supplies what it owns.
type AgentDeps struct {
	Home    string
	Cfg     *appcfg.Root
	Runner  *run.Runner
	Sandbox *safety.Manager

	// OnApplied runs after the boundary is rebuilt, for surface-owned state
	// that also follows the active agent (upload roots, caches, notifications).
	// It must not fail the switch, so it returns nothing.
	OnApplied func(active appcfg.Summary)
}

// Switch makes the agent matching query active and rebinds every per-agent
// seam to it. The active agent is persisted first so a failure to rebuild
// leaves the recorded state and the runtime agreeing on the same target.
func SwitchAgent(deps AgentDeps, query string) (appcfg.Summary, error) {
	resolver, err := appcfg.NewResolver(strings.TrimSpace(deps.Home), deps.Cfg)
	if err != nil {
		return appcfg.Summary{}, err
	}
	active, err := resolver.Switch(query)
	if err != nil {
		return appcfg.Summary{}, err
	}
	if err := ApplyAgent(deps, active); err != nil {
		return appcfg.Summary{}, err
	}
	return active, nil
}

// Apply rebinds the runtime to an already-resolved primary agent. It is
// separate from Switch because a surface can learn about the active agent from
// elsewhere (startup, another process writing the state file) and still needs
// the exact same rebuild.
//
// An error here means the isolation boundary is not established, so callers
// must surface it rather than continue serving turns.
func ApplyAgent(deps AgentDeps, active appcfg.Summary) error {
	workspace := strings.TrimSpace(active.WorkspaceRoot)
	if workspace == "" {
		return fmt.Errorf("agentswitch: primary agent %q has no workspace root", active.ID)
	}
	runner := deps.Runner
	if runner == nil {
		// No runner is bound yet, so there is no boundary to rebuild: the
		// active agent has been recorded and whoever constructs the runner next
		// reads it from that state. Surface-owned state still follows.
		if deps.OnApplied != nil {
			deps.OnApplied(active)
		}
		return nil
	}

	// Approvals granted to the previous agent are scoped to its workspace, so
	// they are dropped before the new one can inherit them.
	runner.ResetRuntimePermissionGrants()

	runner.AgentName = strings.TrimSpace(active.ID)
	runner.WorkspaceRoot = workspace
	// Sessions and memories are tenant data keyed by agent id in the state
	// database, which is shared by every primary agent. Rebinding the stores
	// here is what keeps rows written after the switch from landing under — and
	// being read back as — the previous agent's memories.
	runner.SessionStore.BindPrimaryAgent(active.ID)
	runner.Deps.MemoryStore.BindPrimaryAgent(active.ID)
	// Load rebuilds the tool state: allowed roots, the sibling-workspace
	// boundary, and the permission rules reloaded from this agent's settings.
	if err := runner.Load(); err != nil {
		return fmt.Errorf("agentswitch: reload runner for %q: %w", active.ID, err)
	}

	// The sandbox decides from a permission snapshot, which just changed, and
	// it must deny the other primaries' workspaces: the file tools filter their
	// own allowed roots, but a shell command only meets the boundary here.
	if deps.Sandbox != nil {
		safety.UpdateManagerWithLocalConfig(
			deps.Sandbox,
			strings.TrimSpace(deps.Home),
			deps.Cfg,
			runner.PermissionSnapshot(),
			nil,
		)
		deps.Sandbox.SetIsolatedPeerRoots(siblingWorkspaces(deps.Home, deps.Cfg, active))
	}

	if deps.OnApplied != nil {
		deps.OnApplied(active)
	}
	return nil
}

// ApplySandboxIsolation denies the other primaries' workspaces without
// rebuilding the runner. Startup paths use it: the runner is already loaded for
// the active agent, but the sandbox has not yet been told which workspaces are
// off limits, and that denial must exist before the first shell command runs.
func ApplySandboxIsolation(deps AgentDeps, active appcfg.Summary) {
	if deps.Sandbox == nil {
		return
	}
	deps.Sandbox.SetIsolatedPeerRoots(siblingWorkspaces(deps.Home, deps.Cfg, active))
}

// siblingWorkspaces lists the workspaces of every primary agent other than
// active. A resolution failure yields nothing rather than a partial list: a
// half-populated deny list would look like isolation while leaving a workspace
// reachable, so callers pair this with the file tools' own boundary.
func siblingWorkspaces(home string, cfg *appcfg.Root, active appcfg.Summary) []string {
	resolver, err := appcfg.NewResolver(strings.TrimSpace(home), cfg)
	if err != nil {
		return nil
	}
	out := make([]string, 0, 4)
	for _, item := range resolver.All() {
		if item.ID == active.ID {
			continue
		}
		if root := strings.TrimSpace(item.WorkspaceRoot); root != "" {
			out = append(out, root)
		}
	}
	return out
}
