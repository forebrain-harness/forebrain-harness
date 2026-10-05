package process

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	"github.com/forebrain-harness/forebrain-harness/pkg/channel"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	openaiauth "github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/lsp"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/session"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/google/uuid"
)

// OpenOptions controls process-wide runtime composition.
type OpenOptions struct {
	Home          string
	ConfigPath    string
	LaunchDir     string
	Config        *appcfg.Root
	SessionSource string

	EnableUploads     bool
	EnableChannels    bool
	EnableTelemetry   bool
	EnableApprovalTTL bool
}

// Environment owns process-wide runtime dependencies.
type Environment struct {
	Root          string
	LaunchDir     string
	LaunchProject safety.ProjectContext
	SQL           *sql.DB
	// Deps is the single owner of the path/config/store dependencies this
	// process shares with its primary Runner. Runner.Deps points at this very
	// field, so there is exactly one place that state lives (R6). It is a value
	// rather than a pointer so an Environment built without it is still safe to
	// call methods on: a zero Deps reads as empty rather than nil-dereferencing.
	// Subagents get their own Deps with StateDir overridden.
	Deps          run.Deps
	Sandbox       *safety.Manager
	Runner        *run.Runner
	Hooks         *hook.AgentPipeline
	Files         *state.FileStore
	Channels      *channel.Registry
	Foreground    *session.Locker
	Control       *run.Controller
	sessionSource string

	ConfigPath string
	reloadMu   sync.Mutex
	managerMu  sync.RWMutex
	reload     *ConfigManager
	poolOnce   sync.Once
	pool       *RunnerPool
	// LSP is the process-wide language-server pool; lspManager is the
	// primary runner's view of it. Project runners get their own managers
	// from the same pool (see RunnerPool).
	LSP               *lsp.Pool
	lspManager        *lsp.Manager
	watchMu           sync.Mutex
	watchStop         func()
	watchID           uint64
	cronOnce          sync.Once
	cron              *CronService
	rulesHook         *assembly.PreHook
	ctxHook           *assembly.Hook
	OnConfigReload    func(*appcfg.Root)
	telShutdown       func(context.Context) error
	approvalSweepStop func()
	// ownerLeaseStop deregisters this process's run-owner lease. It runs
	// after the runners are closed and before the SQL handle is: while any
	// runner lives it may still settle runs under this owner, and the DELETE
	// needs the database.
	ownerLeaseStop func()
}

func (env *Environment) Close() {
	if env == nil {
		return
	}
	env.stopConfigHotReload()
	if env.approvalSweepStop != nil {
		env.approvalSweepStop()
	}
	// The primary Runner is closed exactly where the pooled ones are, and
	// before telemetry and SQL: its MCP sessions are children of this process,
	// and the state root they write accounting into is backed by the SQL handle
	// that is about to close. Leaving it out was how the primary agent's servers
	// outlived the install that started them.
	if env.Runner != nil {
		_ = env.Runner.Close()
	}
	env.CloseRunnerPool()
	// This process's runs are nobody's now: deregistering the lease makes
	// any run it failed to settle immediately readable as abandoned. It must
	// precede the SQL close below, which the DELETE and the renewal loop
	// both need.
	if env.ownerLeaseStop != nil {
		env.ownerLeaseStop()
	}
	// Language servers are children of this process too; they go after
	// every runner that could still ask them for something.
	if env.LSP != nil {
		_ = env.LSP.Close()
	}
	if env.telShutdown != nil && env.SQL != nil {
		_ = env.telShutdown(context.Background())
	}
	if env.SQL != nil {
		_ = env.SQL.Close()
	}
}

// ContextHook returns the shared context assembly hook.
func (env *Environment) ContextHook() *assembly.Hook {
	if env == nil {
		return nil
	}
	return env.ctxHook
}

// RulesHook returns the shared project-rules hook.
func (env *Environment) RulesHook() *assembly.PreHook {
	if env == nil {
		return nil
	}
	return env.rulesHook
}

// sessionPrimaryModel resolves one conversation's own model choice against
// the runner's live config, falling back to the runner's published selection.
// The context engine's per-session model limits need it: a shared runner may
// serve sessions on different models.
func sessionPrimaryModel(r *run.Runner, store *state.SessionStore) func(sessionID string) (string, string) {
	return func(sessionID string) (string, string) {
		return run.PrimaryModelForSession(r, store, sessionID)
	}
}

// ApprovalTTLFromEnv returns the configured approval auto-expire TTL parsed from
// FOREBRAIN_APPROVAL_TTL_SEC, or 0 when unset/invalid (disabled).
func ApprovalTTLFromEnv() time.Duration {
	v := strings.TrimSpace(os.Getenv("FOREBRAIN_APPROVAL_TTL_SEC"))
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

func Open(ctx context.Context, options ...OpenOptions) (*Environment, error) {
	// Provider clients are Layer 0 and cannot reach pkg/telemetry, so the
	// composition root installs the observability implementations behind llm's
	// ports. Same shape, and same reason, as the skill ports below: skill
	// cannot import memory or tool directly (C7, and the
	// skill -> tool -> memory -> skill cycle that
	// memory.ListAuthoredSkillsForWorkspace would otherwise close). All four
	// are safe to repeat across multiple Open calls, since each always
	// registers the same implementation.
	llm.SetHTTPTransportWrapper(telemetry.WrapLLMHTTPTransport)
	llm.SetDebugLogger(telemetry.LogProviderDebug)
	skill.SetAuthoredSkillSource(memory.ListAuthoredSkillsForWorkspace)

	opts := OpenOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	root := strings.TrimSpace(opts.Home)
	if root == "" {
		var err error
		root, err = home.Root()
		if err != nil {
			return nil, err
		}
	}
	if err := home.Ensure(root); err != nil {
		return nil, err
	}
	if err := Seed(root); err != nil {
		return nil, err
	}
	cfgPath := strings.TrimSpace(opts.ConfigPath)
	if cfgPath == "" {
		var err error
		cfgPath, err = home.ResolveConfigPath(root)
		if err != nil {
			return nil, err
		}
	}
	var loaded appcfg.Root
	if opts.Config != nil {
		loaded = *opts.Config
	} else {
		var err error
		loaded, err = appcfg.Load(cfgPath)
		if err != nil {
			return nil, fmt.Errorf("load config %s: %w", cfgPath, err)
		}
	}
	// credentials.chatgpt from the loaded config, installed once so every
	// ChatGPT client built in this process reads the same file. Empty leaves
	// the default of <FOREBRAIN_HOME>/auth.json in place.
	openaiauth.SetCredentialsPath(loaded.Credentials.ChatGPT)

	launchDir := strings.TrimSpace(opts.LaunchDir)
	if launchDir == "" {
		launchDir, _ = os.Getwd()
	}
	effective, launchProject, trustErr := safety.ResolveEffectiveConfig(loaded, root, launchDir)
	if trustErr != nil {
		slog.Warn("project trust lookup failed; using read-only sandbox", "err", trustErr)
	}
	cfgRoot := new(appcfg.Root)
	*cfgRoot = effective
	safety.ApplyYOLO(cfgRoot)
	if err := safety.NewManager().StartupCheck(cfgRoot); err != nil {
		return nil, err
	}
	// Every per-agent seam below (channels, sessions, memories, the runner's
	// workspace) hangs off the active primary agent, so resolve it once here.
	activeAgent := activePrimaryAgent(root, cfgRoot)
	mergeAgentClawbotSession(cfgRoot, activeAgent)

	var shutdownTel func(context.Context) error
	if opts.EnableTelemetry {
		shutdownTel, _ = telemetry.InitFromEnv("forebrain")
	}
	sqlDB, err := state.OpenStateFromHome(ctx, root, cfgRoot)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	source := strings.TrimSpace(opts.SessionSource)
	if source == "" {
		source = memory.SessionSourceWebchat
	}
	env := &Environment{Root: root, LaunchDir: launchDir, LaunchProject: launchProject, SQL: sqlDB, Sandbox: safety.NewManager(), Foreground: session.NewLocker(), Control: run.NewController(), sessionSource: source, telShutdown: shutdownTel}
	env.reload = NewConfigManager(env.Control, env.reloadConfig)
	// The effective MCP list is resolved once per session and frozen: global
	// entries stamped with their scope, project entries gated on the frozen
	// launch project (trust + VCS) and their recorded per-entry consents.
	// Nothing later in this process may recompute it — a mid-session change
	// would rebuild the tool table and the prompt prefix derived from it.
	mcpRes := ResolveSessionMCP(root, activeAgent.WorkspaceRoot, cfgRoot.Agents.Defaults.MCPServers, launchProject)
	frozenMCP := mcpRes.Servers
	// The whole frozen list, disabled entries included, is what the drift
	// check compares against the disk: a disable toggle is not drift.
	frozenAll := append(append([]appcfg.MCPServerConfig(nil), frozenMCP...), mcpRes.Disabled...)
	frozenAgentWorkspace := activeAgent.WorkspaceRoot
	// The replaced globals are not in the frozen list by definition, so the
	// display hook cannot re-derive them from it; the resolution's own record
	// of what this session overrode is carried alongside.
	frozenOverridden := mcpRes.Summary.OverriddenGlobal

	actionSvc := &state.ActionService{DB: sqlDB}
	// The owner names this process to the shared database: the runs it creates
	// and resumes are stamped with it, and the lease it holds below is what
	// tells every other process on this machine that those runs are alive.
	runSvc := &state.RunStore{DB: sqlDB, Owner: source + "-" + uuid.NewString()}
	fileSvc := &state.FileStore{DB: sqlDB, Home: root, Cfg: state.LoadConfigFromEnv()}
	if v := strings.TrimSpace(os.Getenv("FOREBRAIN_WORK_ITEM_LEASE_SEC")); v != "" {
		if _, err := strconv.Atoi(v); err == nil {
			slog.Warn("ignoring FOREBRAIN_WORK_ITEM_LEASE_SEC: batch/taskrt runtime removed")
		}
	}
	// Both stores are tenant-scoped: they must know which primary agent owns the
	// sessions they record before the first one is written.
	sessStore := state.NewSessionStore(sqlDB, activeAgent.ID)
	memStore := memory.NewStore(sqlDB, activeAgent.ID)
	env.LSP = lsp.NewPool(cfgRoot)
	lspOpts := lsp.ManagerOptions{
		Home:              root,
		AgentWorkspace:    activeAgent.WorkspaceRoot,
		ProjectRoot:       launchProject.Project.Root,
		Trusted:           safety.TrustedRoot(launchProject) != "",
		VersionControlled: launchProject.Project.VersionControlled,
	}
	if lspOpts.ProjectRoot != "" {
		lspOpts.ProjectKey = memory.ProjectKey(lspOpts.ProjectRoot)
	}
	lspOpts.ToolRegistered = lsp.ToolEnabled(cfgRoot, lspOpts)
	env.lspManager = env.LSP.NewManager(lspOpts)
	env.Deps = run.Deps{
		Home:        root,
		Actions:     actionSvc,
		MCPServers:  frozenMCP,
		MCPProject:  mcpRes.ProjectRoot,
		MCPDisabled: mcpRes.Disabled,
		// The launch project and its trust decision are frozen with the
		// runtime, the same way the runner pool freezes them for project
		// runners: project skill roots are session state, and a runtime
		// without the frozen decision cannot resolve them at all.
		LaunchProject: launchProject,
		MCPDiagnostics: func() mcp.ProjectMCPScopeSummary {
			summary := InspectProjectMCP(frozenAgentWorkspace, frozenAll, launchProject)
			summary.OverriddenGlobal = frozenOverridden
			return summary
		},
		// The language-server runtime is the same frozen shape: the pool is
		// process-wide, this manager is the primary runner's view of it, and
		// the tool decision is computed once and never recomputed on reload.
		CodeIntel:        env.lspManager,
		CodeIntelControl: env.lspManager,
		CodeIntelTool:    lspOpts.ToolRegistered,
		MemoryStore:      memStore,
		AppCfg:           cfgRoot,
		SessionStore:     sessStore,
		RunRT:            runSvc,
	}
	runner := &run.Runner{Deps: &env.Deps, Control: env.Control, SkillCommands: run.SkillCommandHooks{
		Refresh:   turn.RefreshSkills,
		IsBuiltin: turn.IsBuiltinName,
	}}
	// AgentName names the tenant this runner serves. It selects the agent's
	// llm_providers and its channels, so leaving it unset would run a non-main
	// primary agent on main's model.
	runner.AgentName = activeAgent.ID
	runner.WorkspaceRoot = activeAgent.WorkspaceRoot
	publishLSPRecommendations(env.lspManager, runner)
	if launchProject.Project.Root != "" {
		runner.ProjectRoot = launchProject.Project.Root
		runner.ProjectKey = memory.ProjectKey(launchProject.Project.Root)
	}
	// The session is filed under the directory it was launched in, which is the
	// same identity the read path uses: memory.ProjectKey resolves launchDir and
	// launchProject.Project.Root to one key, so what a session writes and what
	// it recalls land in the same scope. The agent workspace root is state, not
	// a project, and naming it here would send every extracted memory to a scope
	// nothing reads.
	sessStore.ConfigureMemoryDefaults(memory.SessionModeForConfig(cfgRoot), source, launchDir, memory.GitBranch(launchDir))
	// Store uploaded originals under the active agent's workspace so they fall
	// inside the agent's allowed roots and can be opened by its file tools.
	fileSvc.WorkspaceRoot = runner.WorkspaceRoot
	// A Runner that cannot be built is a startup failure, not a log line: the
	// alternative was an environment that came up with no agent behind it, and
	// every later symptom (no tools, no skills, an empty model) pointed at the
	// surface instead of at the load that actually failed.
	if err := runner.Load(); err != nil {
		_ = env.LSP.Close()
		return nil, fmt.Errorf("process agent load: %w", err)
	}
	safety.UpdateManagerWithLocalConfig(env.Sandbox, root, cfgRoot, runner.PermissionSnapshot(), nil)
	// Deny the other primaries' workspaces before the first turn: the file
	// tools' boundary was set by runner.Load above, but a shell command only
	// meets the boundary inside the sandbox.
	ApplySandboxIsolation(AgentDeps{
		Home: root, Cfg: cfgRoot, Runner: runner, Sandbox: env.Sandbox,
	}, activeAgent)
	svc := skill.NewServiceForWorkspace(root, runner.WorkspaceRoot)
	svc.ProjectRoot = runner.ProjectRoot
	svc.OnRefresh = func() error { return turn.RefreshSkills(svc.Home, svc.Workspace(), env.LaunchProject) }
	svc.IsBuiltin = turn.IsBuiltinName
	_ = svc.Refresh()

	if opts.EnableChannels {
		env.Channels = channel.NewRegistry()
	}

	skillHub := skill.NewForWorkspace(root, runner.WorkspaceRoot, launchProject.Project.Root)
	ctxEngine := assembly.NewRuntimeHook(assembly.HookParams{
		Cfg:           cfgRoot,
		Home:          root,
		WorkspaceRoot: runner.WorkspaceRoot,
		Store:         sessStore,
		RunRT:         runSvc,
		SkillHub:      skillHub,
		PrimaryModel:  sessionPrimaryModel(runner, sessStore),
		PinsProvider: func(sessionID string) []string {
			tools := runner.Tools()
			if tools == nil {
				return nil
			}
			return tools.WorkingSetPins(sessionID)
		},
		ReadStatesProvider: func() []tool.ReadState {
			tools := runner.Tools()
			if tools == nil {
				return nil
			}
			return tools.ReadStates()
		},
		SnapshotWriter: func(sessionID string, snapshot assembly.AssemblyResult) {
			tools := runner.Tools()
			if tools == nil {
				return
			}
			_ = tools.SetContextSnapshot(sessionID, snapshot)
		},
	})
	env.ctxHook = ctxEngine
	hooks := hook.NewAgentPipeline()
	rh := assembly.NewPreHook(cfgRoot, root, runner.WorkspaceRoot)
	env.rulesHook = rh
	rulesHook := rh.Hook()
	planHook := hook.NewPlanModeForWorkspace(root, runner.WorkspaceRoot)
	gitContextRoot := strings.TrimSpace(runner.WorkspaceRoot)
	if gitContextRoot == "" {
		gitContextRoot = root
	}
	hooks.AddCorePreHooks(hook.CorePreHooks{
		ForebrainRules: rulesHook,
		PlanMode:       planHook.Hook(),
		ContextEngine:  ctxEngine.Hook(),
		GitContext: assembly.GitPreHook(func(hook.HookContext) string {
			return gitContextRoot
		}),
	})
	env.ConfigPath = cfgPath
	env.Runner = runner
	env.Hooks = hooks
	if opts.EnableApprovalTTL {
		if ttl := ApprovalTTLFromEnv(); ttl > 0 {
			env.approvalSweepStop = state.StartExpireSweeper(ctx, actionSvc, state.ExpireSweeperConfig{
				TTL:    ttl,
				Reason: "approval_ttl_expired",
				OnExpired: func(c context.Context, actionID string) {
					if runSvc == nil {
						return
					}
					runID, _, ferr := runSvc.FindRunByAction(c, actionID)
					if ferr != nil || strings.TrimSpace(runID) == "" {
						return
					}
					_ = runSvc.ClearWait(c, runID)
					_ = runSvc.SetStatus(c, runID, state.RunStatusFailed)
				},
			})
		}
	}
	if opts.EnableUploads {
		env.Files = fileSvc
	}
	runner.FileResolver = fileReferenceResolver(fileSvc, runner.AgentName)
	runner.SubagentExecutor = env
	// The lease is registered only once every failure path is behind us: the
	// renewal goroutine must not outlive an Open that is about to return an
	// error. From here the process is live — its runs are vouched for until
	// Close deregisters it.
	ownerStop, err := runSvc.HoldOwnerLease(ctx)
	if err != nil {
		_ = env.LSP.Close()
		_ = env.Runner.Close()
		return nil, fmt.Errorf("register run owner lease: %w", err)
	}
	env.ownerLeaseStop = ownerStop
	return env, nil
}

// publishLSPRecommendations hands recommendations from mgr to whichever
// surface is attached to runner when one is made. The sink is read at
// publish time: surfaces attach it after the runner is built, and a
// subagent or fork sharing the parent's manager still reaches the parent
// runner's surface — the reason this is wired here rather than in pkg/run,
// whose sub-runner Load would overwrite the parent's listener.
func publishLSPRecommendations(mgr *lsp.Manager, runner *run.Runner) {
	if mgr == nil || runner == nil {
		return
	}
	mgr.SetRecommendationListener(func(ctx context.Context, rec event.LSPRecommendation) {
		sink := runner.Events
		if sink == nil {
			return
		}
		_ = sink.Publish(context.WithoutCancel(ctx), event.NewRunEvent(
			rec.ID, tool.RunIDFromContext(ctx), tool.ConversationSessionIDFromContext(ctx),
			event.RunEventLSPRecommendation, rec, time.Now()))
	})
}

// fileReferenceResolver resolves the file_id of a persisted file_reference
// back to a path on disk.
//
// Both surfaces a process serves write file references, in two different
// file_id namespaces: the web surface uploads the file and records its upload
// ID, while a terminal attachment is recorded by absolute path because
// turn.BuildSurfaceUserPartsJSON deliberately never copies the bytes into the
// transcript. A resolver that understands only upload IDs therefore fails
// every replayed terminal attachment, and rehydration silently leaves the text
// placeholder — the model never sees the image the user attached. Both
// namespaces have to be covered, upload IDs first so a real upload is never
// mistaken for a path.
func fileReferenceResolver(fileSvc *state.FileStore, agentID string) run.FileReferenceResolver {
	return run.ChainResolver{
		run.FilertResolver{
			Resolve: func(ctx context.Context, fileID string) (string, bool) {
				if fileSvc == nil {
					return "", false
				}
				absPath, _, err := fileSvc.EnsureLocalFile(ctx, agentID, fileID)
				if err != nil || absPath == "" {
					return "", false
				}
				return absPath, true
			},
		},
		run.PathResolver{},
	}
}

// activePrimaryAgent resolves the primary agent this process serves. It falls
// back to the main agent's defaults exactly where config.ActiveStateRoot
// does, so the tenant recorded in the database always names the workspace the
// runtime is actually reading and writing.
func activePrimaryAgent(home string, cfg *appcfg.Root) appcfg.Summary {
	trimmed := strings.TrimSpace(home)
	if resolver, err := appcfg.NewResolver(trimmed, cfg); err == nil {
		if active, err := resolver.Active(); err == nil {
			return active
		}
	}
	return appcfg.Summary{
		ID:            appcfg.ActiveID(trimmed, cfg),
		WorkspaceRoot: appcfg.ActiveStateRoot(trimmed, cfg),
	}
}

// mergeAgentClawbotSession folds persisted Weixin credentials into the active
// agent's channel. Both the credential file and the channel
// belong to that agent, so the merge never crosses tenants.
// ActiveAgentWorkspace resolves the workspace root of the active primary
// agent using the cached runtime config. Startup prompts that record
// per-agent decisions before a session opens need it; loading it here rather
// than resolving the agent again keeps one definition of "active".
func ActiveAgentWorkspace() (string, error) {
	ctx, err := Resolve()
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(ctx.Home)
	if root == "" {
		var err error
		root, err = home.Root()
		if err != nil {
			return "", err
		}
	}
	return strings.TrimSpace(activePrimaryAgent(root, &ctx.Config).WorkspaceRoot), nil
}

func mergeAgentClawbotSession(cfg *appcfg.Root, active appcfg.Summary) {
	if cfg == nil {
		return
	}
	ch := appcfg.ChannelsForAgent(cfg, active.ID)
	channel.MergePersistedClawbot(&ch, active.WorkspaceRoot)
	appcfg.SetChannelsForAgent(cfg, active.ID, ch)
}
