package process

import (
	"context"
	"fmt"
	"strings"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// applyComposedConfigLocked adopts loaded as the configuration and re-derives
// everything the environment froze from it. The caller holds reloadMu: both
// entering paths — SyncConfigFromDB, after composing a changed snapshot, and
// AdoptLaunchTrust, after replacing the launch project the derivation reads —
// reach this through the same lock, so a trust adoption can never interleave
// with a snapshot application.
//
// There is no file-only caller: a process adopts a changed configuration at its
// turn boundary, from the composed source (plan 187), and a hand-edited
// forebrain.yaml takes effect on restart (plan 189).
func (env *Environment) applyComposedConfigLocked(loaded appcfg.Root) error {
	// The raw configuration is retained before any derivation: pool entries
	// re-derive their own effective config from it (each project from its own
	// trust), so the base runner's derived values must not be what they receive.
	env.RawConfig = loaded
	next := effectiveFor(loaded, env.LaunchProject)
	if err := safety.NewManager().StartupCheck(next); err != nil {
		return err
	}
	if env.Sandbox == nil {
		env.Sandbox = safety.NewManager()
	}
	if env.Runner != nil {
		// LoadConfig owns the pointer swap, the pinned ordinary load and the
		// rollback: a failed apply restores the old pointer and rebuilds the
		// old effective runtime, joining a rollback failure into the returned
		// error instead of discarding it.
		if err := env.Runner.LoadConfig(next); err != nil {
			return fmt.Errorf("load runner: %w", err)
		}
		// The MCP list is deliberately NOT taken from the applied snapshot: it
		// was resolved once for this session (project gating + consents) and
		// is frozen. An application that changed agents.defaults.mcp_servers
		// takes effect in the next session, exactly like project files do —
		// the effective tool list and the system prefix derived from it must
		// not move mid-session. Runner.Load keeps the live MCP sessions
		// because the list (and its fingerprint) is unchanged.
		if env.Deps.MemoryStore != nil && &env.Deps != env.Runner.Deps {
			env.Runner.Deps.MemoryStore = env.Deps.MemoryStore
		}
	}
	if env.Runner == nil || &env.Deps != env.Runner.Deps {
		env.Deps.AppCfg = next
	}
	// The language-server pool adopts the applied configuration the same way
	// the rest of the environment does. The frozen per-runner decisions (the
	// lsp tool's registration) deliberately do not move: they sit in each
	// session's prompt prefix.
	if env.LSP != nil {
		env.LSP.Reconcile(next)
	}
	if p := env.pool; p != nil {
		if err := p.PropagateConfig(context.Background(), env.RawConfig); err != nil {
			// The base runner and the environment already adopted next; the
			// entries named here keep their previous runtime (LoadConfig's own
			// rollback). Truthful wording matters: this is not a failed apply.
			return fmt.Errorf("config adopted, but some project runners kept their previous configuration: %w", err)
		}
	}
	if env.Deps.SessionStore != nil {
		// Only the mode follows the configuration. The session's source and
		// project identity were decided once in Open and applying a
		// configuration has no standing to move them.
		env.Deps.SessionStore.SetMemoryMode(memory.SessionModeForConfig(next))
	}
	env.refreshSandboxRuntimeLocked()
	if env.rulesHook != nil {
		env.rulesHook.Cfg = next
	}
	if env.ctxHook != nil {
		env.ctxHook.Cfg = next
	}
	wsRoot := ""
	if env.Runner != nil {
		wsRoot = env.Runner.WorkspaceRoot
	}
	svc := skill.NewServiceForWorkspace(env.Root, wsRoot)
	svc.ProjectRoot = env.LaunchProject.Project.Root
	svc.OnRefresh = func() error { return turn.RefreshSkills(svc.Home, svc.Workspace(), env.LaunchProject) }
	svc.IsBuiltin = turn.IsBuiltinName
	_ = svc.Refresh()
	if env.OnConfigReload != nil {
		env.OnConfigReload(next)
	}
	// The event keeps its name for the surfaces that listen to it, but its
	// meaning is now "a configuration snapshot was applied at a turn boundary",
	// not "a watched file changed" (plan 189 §6).
	if env.Runner != nil && env.Runner.Events != nil {
		_ = env.Runner.Events.Publish(context.Background(), event.NewRunEvent("", "", "", event.RunEventConfigReloaded, event.ConfigReloadedPayload{Path: env.ConfigPath}, time.Now()))
	}
	return nil
}

// AdoptLaunchTrust re-reads the launch project's trust decision and
// re-derives everything the environment froze from it — the base runner's
// effective sandbox and approval policy, the sandbox manager, the skills
// service — from the configuration it already holds. The interactive surface
// calls it after the operator answers the trust question inside the TUI. A
// process with no launch project has nothing to re-read.
func (env *Environment) AdoptLaunchTrust() error {
	if env == nil {
		return fmt.Errorf("nil environment")
	}
	if env.noLaunchProject || strings.TrimSpace(env.LaunchProject.Project.Root) == "" {
		return nil
	}
	env.reloadMu.Lock()
	defer env.reloadMu.Unlock()
	launch, err := safety.ResolveRegisteredContext(env.Root, env.LaunchProject.Project.Root)
	if err != nil {
		return err
	}
	env.LaunchProject = launch
	env.Deps.LaunchProject = launch
	if env.Runner != nil && env.Runner.Deps != &env.Deps {
		env.Runner.Deps.LaunchProject = launch
	}
	return env.applyComposedConfigLocked(env.RawConfig)
}

// AdoptConfig records next as the live configuration.
//
// This is the surface-side half of an application: a caller that has already
// derived next (EffectiveConfig and YOLO applied) hands it here, and the
// environment re-points what it froze — the Deps the runner shares, the LSP
// pool, the hooks, the sandbox manager. Environment.Deps.AppCfg is what
// stateRoot() resolves the active primary agent's workspace from, and the run
// executor hands that to AgentContextForProject: a stale value there sends the
// agent's plan-path and session-mode lookups at the wrong workspace.
//
// This does not call OnConfigReload: the caller is the surface that produced
// next and already holds it, and re-entering the callback would take the
// surface's own apply lock a second time.
//
// Callers must not hold a lock that OnConfigReload acquires.
func (env *Environment) AdoptConfig(next *appcfg.Root) {
	if env == nil || next == nil {
		return
	}
	env.reloadMu.Lock()
	defer env.reloadMu.Unlock()
	env.Deps.AppCfg = next
	// next is already derived (the surface applied EffectiveConfig and YOLO
	// before handing it here), so it cannot serve as the raw configuration a
	// project runner derives from. Re-read the file when there is one: the
	// invariant RawConfig carries — the on-disk form, underived — must hold for
	// every entry the pool builds from it. A read failure keeps the previous
	// raw config rather than replacing it with an already-derived one.
	if path := strings.TrimSpace(env.ConfigPath); path != "" {
		if raw, err := appcfg.Load(path); err == nil {
			env.RawConfig = raw
		}
	}
	if env.LSP != nil {
		env.LSP.Reconcile(next)
	}
	if env.rulesHook != nil {
		env.rulesHook.Cfg = next
	}
	if env.ctxHook != nil {
		env.ctxHook.Cfg = next
	}
	env.refreshSandboxRuntimeLocked()
}

func (env *Environment) RefreshSandboxRuntime() {
	if env == nil {
		return
	}
	env.reloadMu.Lock()
	defer env.reloadMu.Unlock()
	env.refreshSandboxRuntimeLocked()
}

func (env *Environment) refreshSandboxRuntimeLocked() {
	if env == nil || env.Deps.AppCfg == nil {
		return
	}
	if env.Sandbox == nil {
		env.Sandbox = safety.NewManager()
	}
	var snap safety.Snapshot
	projectRoot := ""
	if env.Runner != nil {
		snap = env.Runner.PermissionSnapshot()
		projectRoot = env.Runner.ProjectRoot
	}
	safety.UpdateManagerWithLocalConfig(env.Sandbox, env.Root, env.Deps.AppCfg, snap, nil, projectRoot)
	// Re-apply read isolation to the in-process file tools immediately. The
	// manager only governs subprocesses; the file tools carry their own copy of
	// the filesystem policy, so without this a sandbox change lands for shell
	// commands but not for Read/Write until the next runner load.
	if env.Runner != nil {
		env.Runner.RefreshSandboxAvailability()
		env.Runner.RefreshFilesystemPolicy()
	}
}
