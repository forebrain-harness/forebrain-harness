package process

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/fsnotify/fsnotify"
)

func StartConfigHotReload(ctx context.Context, env *Environment) {
	if env == nil || strings.TrimSpace(env.ConfigPath) == "" {
		return
	}
	env.watchMu.Lock()
	if env.watchStop != nil {
		env.watchMu.Unlock()
		return
	}
	stopCh := make(chan struct{})
	stop := sync.OnceFunc(func() { close(stopCh) })
	env.watchID++
	watchID := env.watchID
	env.watchStop = stop
	env.watchMu.Unlock()
	cfgPath := env.ConfigPath
	go func() {
		defer env.clearConfigHotReload(watchID)
		w, err := fsnotify.NewWatcher()
		if err != nil {
			slog.Error("config hot reload: watcher", "err", err)
			return
		}
		defer w.Close()
		dir := filepath.Dir(cfgPath)
		base := filepath.Clean(filepath.Base(cfgPath))
		if err := w.Add(dir); err != nil {
			slog.Error("config hot reload: add watch dir", "dir", dir, "err", err)
			return
		}
		var debounceMu sync.Mutex
		var debounce *time.Timer
		schedule := func() {
			debounceMu.Lock()
			defer debounceMu.Unlock()
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(400*time.Millisecond, func() {
				select {
				case <-ctx.Done():
					return
				case <-stopCh:
					return
				default:
				}
				if err := env.RequestConfigReload(); err != nil {
					slog.Error("config hot reload", "err", err)
					return
				}
				slog.Info("config hot reload: applied", "path", cfgPath)
			})
		}
		for {
			select {
			case <-ctx.Done():
				debounceMu.Lock()
				if debounce != nil {
					debounce.Stop()
				}
				debounceMu.Unlock()
				return
			case <-stopCh:
				debounceMu.Lock()
				if debounce != nil {
					debounce.Stop()
				}
				debounceMu.Unlock()
				return
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				if err != nil {
					slog.Error("config hot reload: watch", "err", err)
				}
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				evBase := filepath.Clean(filepath.Base(ev.Name))
				if evBase != base {
					continue
				}
				if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
					schedule()
				}
			}
		}
	}()
}

func (env *Environment) clearConfigHotReload(id uint64) {
	if env == nil {
		return
	}
	env.watchMu.Lock()
	if env.watchID == id {
		env.watchStop = nil
	}
	env.watchMu.Unlock()
}

func (env *Environment) stopConfigHotReload() {
	if env == nil {
		return
	}
	env.watchMu.Lock()
	stop := env.watchStop
	env.watchStop = nil
	env.watchMu.Unlock()
	if stop != nil {
		stop()
	}
}

// SetActiveRunProbe configures foreground activity for deferred reloads.
func (env *Environment) SetActiveRunProbe(probe ActiveRunProbe) {
	if env == nil {
		return
	}
	m := env.configManager()
	if m != nil {
		m.SetProbe(probe)
	}
}

// RequestConfigReload marks a reload pending and applies it when idle.
func (env *Environment) RequestConfigReload() error {
	if env == nil {
		return fmt.Errorf("nil environment")
	}
	return env.configManager().Request()
}

// configManager returns the reload coordinator, creating it on first use.
//
// It is created lazily rather than only in Open because a nil manager used to
// mean "apply the reload immediately" — while ChatSession.ReloadConfig, having
// seen a run in flight, had already returned "config reload deferred until the
// current task finishes" to the user. An Environment built anywhere other than
// Open therefore applied a config change mid-run and reported the opposite.
// Making the manager unconditional turns "a reload never lands while a run is
// active" into a property of the type instead of a property of one constructor.
func (env *Environment) configManager() *ConfigManager {
	env.managerMu.RLock()
	m := env.reload
	env.managerMu.RUnlock()
	if m != nil {
		return m
	}
	env.managerMu.Lock()
	defer env.managerMu.Unlock()
	if env.reload == nil {
		env.reload = NewConfigManager(env.Control, env.reloadConfig)
	}
	return env.reload
}

// ConfigIdle applies a deferred reload after foreground work completes.
func (env *Environment) ConfigIdle() error {
	if env == nil {
		return fmt.Errorf("nil environment")
	}
	return env.configManager().Idle()
}

func (env *Environment) ReloadConfig() error {
	return env.reloadConfig()
}

func (env *Environment) reloadConfig() error {
	if env == nil {
		return fmt.Errorf("nil environment")
	}
	env.reloadMu.Lock()
	defer env.reloadMu.Unlock()
	if strings.TrimSpace(env.ConfigPath) == "" {
		return fmt.Errorf("empty config path")
	}
	loaded, err := appcfg.Load(env.ConfigPath)
	if err != nil {
		return err
	}
	loaded = safety.EffectiveConfig(loaded, env.LaunchProject)
	next := new(appcfg.Root)
	*next = loaded
	safety.ApplyYOLO(next)
	if err := safety.NewManager().StartupCheck(next); err != nil {
		return err
	}
	mergeAgentClawbotSession(next, activePrimaryAgent(env.Root, next))
	if env.Sandbox == nil {
		env.Sandbox = safety.NewManager()
	}
	if env.Runner != nil {
		// LoadConfig owns the pointer swap, the pinned ordinary reload and
		// the rollback: a failed reload restores the old pointer and rebuilds
		// the old effective runtime, joining a rollback failure into the
		// returned error instead of discarding it.
		if err := env.Runner.LoadConfig(next); err != nil {
			return fmt.Errorf("load runner: %w", err)
		}
		// The MCP list is deliberately NOT taken from the reloaded file: it
		// was resolved once for this session (project gating + consents) and
		// is frozen. A reload that changed agents.defaults.mcp_servers takes
		// effect in the next session, exactly like project files do — the
		// effective tool list and the system prefix derived from it must not
		// move mid-session. Runner.Load keeps the live MCP sessions because
		// the list (and its fingerprint) is unchanged.
		if env.Deps.MemoryStore != nil && &env.Deps != env.Runner.Deps {
			env.Runner.Deps.MemoryStore = env.Deps.MemoryStore
		}
	}
	if env.Runner == nil || &env.Deps != env.Runner.Deps {
		env.Deps.AppCfg = next
	}
	// The language-server pool adopts the reloaded configuration the same
	// way the rest of the environment does. The frozen per-runner decisions
	// (the lsp tool's registration) deliberately do not move: they sit in
	// each session's prompt prefix.
	if env.LSP != nil {
		env.LSP.Reconcile(next)
	}
	if p := env.pool; p != nil {
		if err := p.PropagateConfig(context.Background(), next); err != nil {
			// The base runner and the environment already adopted next; the
			// entries named here keep their previous runtime (LoadConfig's own
			// rollback). Truthful wording matters: this is not a failed reload.
			return fmt.Errorf("config reloaded, but some project runners kept their previous configuration: %w", err)
		}
	}
	if env.Deps.SessionStore != nil {
		// Only the mode follows the configuration. The session's source and
		// project identity were decided once in Open and a reload has no
		// standing to move them.
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
	if env.Runner != nil && env.Runner.Events != nil {
		_ = env.Runner.Events.Publish(context.Background(), event.NewRunEvent("", "", "", event.RunEventConfigReloaded, event.ConfigReloadedPayload{Path: env.ConfigPath}, time.Now()))
	}
	return nil
}

// AdoptConfig records next as the live configuration.
//
// The TUI reaches config reload two ways. Environment.reloadConfig owns one of
// them and pushes the result outwards through OnConfigReload; the slash
// commands own the other (ChatSession.reloadConfigFromDisk) and validate and
// apply the file themselves. That second path used to update only the session
// and the Runner, leaving Environment.Deps.AppCfg pointing at the config the
// process started with -- and Environment.stateRoot() resolves the active
// primary agent's workspace from it, which the run executor then hands to
// AgentContextForProject. A stale value there sends the agent's plan-path and
// session-mode lookups at the wrong workspace.
//
// This does not call OnConfigReload: the caller is the surface that produced
// next and already holds it, and re-entering the callback would take the
// surface's own apply lock a second time.
//
// Callers must not hold a lock that OnConfigReload acquires. reloadConfig
// invokes that callback while holding reloadMu, so taking reloadMu from
// underneath it inverts the order and deadlocks.
func (env *Environment) AdoptConfig(next *appcfg.Root) {
	if env == nil || next == nil {
		return
	}
	env.reloadMu.Lock()
	defer env.reloadMu.Unlock()
	env.Deps.AppCfg = next
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
	if env.Runner != nil {
		snap = env.Runner.PermissionSnapshot()
	}
	safety.UpdateManagerWithLocalConfig(env.Sandbox, env.Root, env.Deps.AppCfg, snap, nil)
	// Re-apply read isolation to the in-process file tools immediately. The
	// manager only governs subprocesses; the file tools carry their own copy of
	// the filesystem policy, so without this a sandbox change lands for shell
	// commands but not for Read/Write until the next runner load.
	if env.Runner != nil {
		env.Runner.RefreshSandboxAvailability()
		env.Runner.RefreshFilesystemPolicy()
	}
}
