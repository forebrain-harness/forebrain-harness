package process

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	"github.com/forebrain-harness/forebrain-harness/pkg/channel"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
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
	// NoLaunchProject starts a process whose working directory carries no
	// business meaning (R4): the gateway serves sessions in many projects, so
	// the directory it happens to be started in is not a project, its trust,
	// or any session's default cwd. When set, LaunchDir is ignored, the
	// effective sandbox and approval policy are derived from an empty launch
	// project (fail closed: read-only, ask on request), and the base runner
	// carries no ProjectRoot.
	NoLaunchProject bool
	// ExactLaunchRoot starts a process whose launch directory IS its project
	// root (R2, the interactive surface): the directory the TUI was started
	// in is the project, never walked up to a .git ancestor. When set,
	// LaunchDir is resolved as a registered root (safety.ResolveRegisteredContext)
	// instead of a launch directory, so a TUI started in a checkout
	// subdirectory runs — and records trust and consent — as that
	// subdirectory's project.
	ExactLaunchRoot bool

	EnableUploads     bool
	EnableChannels    bool
	EnableTelemetry   bool
	EnableApprovalTTL bool
	// ReplicaKind is what this process records about itself in fb_run_owners
	// (plan 213 §3.1): state.RunOwnerKindGateway or
	// state.RunOwnerKindTerminal. Empty derives it from SessionSource.
	ReplicaKind string
}

// Environment owns process-wide runtime dependencies.
type Environment struct {
	Root          string
	LaunchDir     string
	LaunchProject safety.ProjectContext
	// RawConfig is the configuration as loaded from disk, before any
	// project-scoped derivation (EffectiveConfig) or YOLO override. The base
	// runner stores the derived form in Deps.AppCfg; this is the one every
	// project runner is derived from — each project's sandbox and approval
	// policy come from its own trust decision, not the base runner's.
	RawConfig appcfg.Root
	// noLaunchProject records that this process was started with no project of
	// its own (OpenOptions.NoLaunchProject — the gateway). Such a process
	// cannot serve a conversation that belongs to no project: it has no runner
	// to run it on (R4). Every other process (the interactive surface) does
	// have one, its launch directory, and serves project-less conversations on
	// its base runner.
	noLaunchProject bool
	SQL             *sql.DB
	// Leases holds the fb_leases rows this process takes: one per long-
	// connection chat account, so that only one replica of a shared database
	// is connected to it (plan 213 §3.4). A single-replica install wins every
	// lease it asks for, so the machinery is inert there.
	Leases *Leases
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
	// ChannelMigrationNotes are the channels that were enabled when the retired
	// primary-agent-level configuration was removed at startup, so the startup
	// banner can say so once.
	ChannelMigrationNotes []string

	ConfigPath string
	// runtimeConfigFromDB records that this process composes its runtime
	// configuration from the state database — the production path, where Open
	// imported the file's sections into fb_config before the first compose.
	// An environment built around OpenOptions.Config (the test bypass that
	// supplies the whole configuration) keeps the file as its source, exactly
	// as before plan 187, and so does a hand-built Environment.
	runtimeConfigFromDB bool
	reloadMu            sync.Mutex
	// configFingerprint is the digest of the configuration snapshot currently
	// in force, guarded by reloadMu. SyncConfigFromDB compares a freshly
	// composed snapshot against it: equal means nothing changed and the sync
	// is a no-op, different means the full re-application runs. Open seeds it
	// from the first composed root.
	configFingerprint []byte
	poolOnce          sync.Once
	pool              *RunnerPool
	// LSP is the process-wide language-server pool; lspManager is the
	// primary runner's view of it. Project runners get their own managers
	// from the same pool (see RunnerPool).
	LSP            *lsp.Pool
	lspManager     *lsp.Manager
	cronOnce       sync.Once
	cron           *CronService
	rulesHook      *assembly.PreHook
	ctxHook        *assembly.Hook
	OnConfigReload func(*appcfg.Root)
	// OnSubagentExecution reports every subagent execution's start and end to
	// the surface that opened the session, so it can arm the subagent's own
	// auto-continue. Nil — a one-shot run, a channel with nobody to cancel a
	// wait — schedules nothing, which is how "a channel never auto-continues"
	// is structural rather than a surface check.
	OnSubagentExecution *SubagentExecutionHooks
	telShutdown         func(context.Context) error
	approvalSweepStop   func()
	// peers is this process's peer registration (plan 195); Close stops it
	// before anything it reads from goes away.
	peers *peerService
	// peerOut is this process's port onto the other processes' agents (plan
	// 197): the peer tools reach it through Deps.Peers. It is built once and
	// outlives the registration, because the tool table sits in the prompt
	// prefix while a registration may start, switch modes and stop.
	peerOut *peerOutbound
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
	if env.approvalSweepStop != nil {
		env.approvalSweepStop()
	}
	// The peer registration reads the controller and the stores below; stop
	// it before they go. Its own stop is once-guarded, so the surface's
	// separate stop function stays safe to call.
	if svc := env.peers; svc != nil {
		svc.stop()
		env.peers = nil
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

// sessionModelHit answers what one conversation's model may do, read from the
// configured row it runs on: the catalog's entry with that row's capabilities
// laid over it (owner 2026-11-10). The assembly's model limits read this, so a
// model page's context window applies to the row it was set on.
func sessionModelHit(r *run.Runner, store *state.SessionStore) func(sessionID string) (llm.Hit, bool) {
	return func(sessionID string) (llm.Hit, bool) {
		sel := run.PrimaryModelStateForSession(r, store, sessionID)
		return llm.LookupRow(sel.ModelID, sel.Provider, sel.Model)
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

// effectiveFor derives the runtime configuration for one launch project from
// the raw on-disk configuration. ApplyYOLO runs first, because YOLO now forces
// the effective approval preset (plan 203) rather than patching a policy and a
// sandbox mode of its own: the derivation right after it turns that preset into
// the pair full access means, so a --yolo process runs exactly as a picked
// full-access preset does. EffectiveConfig then fills the approval policy and
// sandbox mode the preset implies for this project's trust decision. The order
// matches the base runner's own derivation in Open, so a project runner and the
// base runner see the same configured values, derived for different projects.
//
// The raw configuration is passed by value and never mutated: safety.EffectiveConfig
// returns a copy, and the pointer returned is the caller's to own.
func effectiveFor(raw appcfg.Root, launch safety.ProjectContext) *appcfg.Root {
	safety.ApplyYOLO(&raw)
	effective := safety.EffectiveConfig(raw, launch)
	out := new(appcfg.Root)
	*out = effective
	return out
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
	// Every record names who it belongs to. telemetry cannot import tool or
	// agent (both sit above it), so the scope a context already carries is
	// read here and handed over once per process (plan 285). worker is empty
	// when the subagent's transcript session is the conversation's own: the
	// two fields would then say the same thing twice.
	telemetry.RegisterScope(func(ctx context.Context) telemetry.Scope {
		conv := tool.ConversationSessionIDFromContext(ctx)
		worker := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
		if worker == conv {
			worker = ""
		}
		return telemetry.Scope{
			Project: tool.ProjectKeyFromContext(ctx),
			Session: conv,
			Worker:  worker,
			Run:     tool.RunIDFromContext(ctx),
			Agent:   agent.NameFromContext(ctx),
		}
	})

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
	var channelMigrationNotes []string
	var loaded appcfg.Root
	if opts.Config != nil {
		loaded = *opts.Config
	} else {
		channelMigrationNotes = MigrateLegacyChannels(cfgPath)
		var err error
		loaded, err = appcfg.Load(cfgPath)
		if err != nil {
			return nil, fmt.Errorf("load config %s: %w", cfgPath, err)
		}
	}
	// The launch project is resolved only when this process has one. The
	// gateway sets NoLaunchProject: the directory it is started in is not a
	// project, so neither its trust decision nor its default cwd apply to any
	// session it serves (R4). An empty launch project derives a read-only
	// sandbox and an ask-on-request approval policy.
	var launchDir string
	var launchProject safety.ProjectContext
	if !opts.NoLaunchProject {
		launchDir = strings.TrimSpace(opts.LaunchDir)
		if launchDir == "" {
			launchDir, _ = os.Getwd()
		}
		var trustErr error
		if opts.ExactLaunchRoot {
			// The launch directory is the project boundary (R2): resolve it
			// as a registered root so it is never relocated onto an enclosing
			// checkout's .git.
			launchProject, trustErr = safety.ResolveRegisteredContext(root, launchDir)
		} else {
			launchProject, trustErr = safety.ResolveProjectContext(root, launchDir)
		}
		if trustErr != nil {
			slog.Warn("project trust lookup failed; using read-only sandbox", "err", trustErr)
		}
	}
	// The state database opens before anything consumes the runtime
	// configuration: the composed configuration reads every runtime section
	// from it (plan 187). Where the database is comes from the file's own
	// database section, which is the one thing the database cannot hold
	// (plan 209): nothing is read out of it to find it.
	databaseURL, err := appcfg.FileDatabaseURL(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("read database.url from %s: %w", cfgPath, err)
	}
	sqlDB, err := state.OpenStateFromHome(ctx, root, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// Every failure from here until the Environment owns the database handle
	// must close it itself: nothing has been handed it yet.
	if opts.Config == nil {
		if err := importLegacyConfigIfNeeded(ctx, sqlDB, root, cfgPath); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("import legacy configuration: %w", err)
		}
	}
	// The database-backed credential stores go in behind the Layer 0 ports
	// (plan 187 §1.4); those packages' own reads and writes keep using the
	// files until 188 switches them.
	installCredentialStores(sqlDB)
	// The write-side gate every section update runs (plan 188 §1): a
	// candidate must compose with this install's configuration and pass the
	// loader's value-domain checks before it lands.
	InstallConfigSectionValidator(cfgPath, sqlDB)
	// Every stored section gets the settings the database does not hold yet
	// (plan 207 §3.2): after the migration has cleaned the rows and after the
	// legacy import has put the file's values in, so the file wins over the
	// defaults and only the missing keys are filled. A new install and a
	// migrated one take this same path, which is what makes "an old install is
	// completed the way a new one is born" true rather than a second,
	// hand-written migration per setting.
	if opts.Config == nil {
		store, err := (&Environment{Root: root, ConfigPath: cfgPath, SQL: sqlDB}).configStore()
		if err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("open config store: %w", err)
		}
		if err := ensureStoredConfigDefaults(ctx, store); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("seed stored configuration defaults: %w", err)
		}
	}
	// The one-time final sync of plan 188 W12: the 187→188 transition kept
	// writing the login files and the MCP overlay tree, so whichever side is
	// newer wins into the database here — and nothing writes those files
	// again.
	if opts.Config == nil {
		if err := syncLegacyCredentialFilesIfNeeded(ctx, sqlDB, root); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("sync legacy credential files: %w", err)
		}
	}
	// The pre-table model configuration moves into fb_llm_models before
	// anything reads the configuration (plan 262): the agents section stops
	// carrying llm_providers, and every reference names a row instead of a
	// "provider/model" label. Gated on content, so a legacy yaml imported into
	// a fresh home later is migrated on the next start.
	if opts.Config == nil {
		if err := migrateLegacyLLMModels(ctx, sqlDB, root); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("move model configurations into the state database: %w", err)
		}
	}
	// The home's .env is retired (owner 2026-11-07, plan 268): what it held
	// moves into the state database and the configuration file, and the file is
	// renamed aside. It runs after the model migration, which is the one reader
	// that still wants the file's values, and before anything composes the
	// configuration, so the composed ${ENV} references resolve from what this
	// step stored.
	if opts.Config == nil {
		if err := migrateLegacyDotEnv(ctx, sqlDB, root, cfgPath); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("move .env values into the state database: %w", err)
		}
	}
	var configFingerprint []byte
	if opts.Config == nil {
		// OpenOptions.Config keeps its bypass semantics: it supplies the
		// whole configuration and neither the file nor the database is
		// consulted — tests run on it.
		composed, cerr := composeConfig(ctx, &Environment{Root: root, ConfigPath: cfgPath, SQL: sqlDB})
		if cerr != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("compose config: %w", cerr)
		}
		loaded = composed
	}
	// The model layers the catalog lookup reads come from the configuration
	// this process starts on — composed from the database, or handed in whole
	// through OpenOptions.Config (plan 263).
	installModelOverrides(loaded)
	// The fingerprint of the configuration that starts the process: every
	// later SyncConfigFromDB compares against it, so the first turn does not
	// re-apply what Open just applied.
	if fp, ferr := configSnapshotFingerprint(loaded); ferr == nil {
		configFingerprint = fp
	}
	cfgRoot := effectiveFor(loaded, launchProject)
	// A sandbox is used when there is one and skipped when there is not (owner
	// 2026-10-31, plan 215 §0.1): a host without a usable bubblewrap runs in
	// full access rather than refusing to start, so this is an operations note
	// — the one line an operator reads in the log — and never a surface
	// notice. The probe behind it is a real run of the backend, which is why
	// the reason is worth recording even though nothing is refused.
	if !safety.SandboxAvailable() {
		slog.Info("no sandbox available; every conversation runs with full access", "platform", runtime.GOOS)
	}
	// Every per-agent seam below (sessions, memories, the runner's
	// workspace, the project channels) hangs off the active primary agent, so
	// resolve it once here. It reads the composed configuration.
	activeAgent := activePrimaryAgent(root, cfgRoot)

	var shutdownTel func(context.Context) error
	if opts.EnableTelemetry {
		shutdownTel, _ = telemetry.InitFromEnv("forebrain")
	}

	source := strings.TrimSpace(opts.SessionSource)
	if source == "" {
		source = memory.SessionSourceWebchat
	}
	env := &Environment{Root: root, LaunchDir: launchDir, LaunchProject: launchProject, RawConfig: loaded, noLaunchProject: opts.NoLaunchProject, SQL: sqlDB, Sandbox: safety.NewManager(), Foreground: session.NewLocker(), Control: run.NewController(), sessionSource: source, telShutdown: shutdownTel, ChannelMigrationNotes: channelMigrationBannerNotes(channelMigrationNotes), configFingerprint: configFingerprint, runtimeConfigFromDB: opts.Config == nil}
	// The environment's own runner is the primary agent's: it serves no
	// conversation — every conversation runs on its project's runner (see
	// RunnerPool) — so it never resolves or hosts an MCP list. Servers
	// started here would be a second copy of the ones the conversation
	// actually uses; a conversation that begins re-resolves on its project's
	// runner, never here.
	var mcpRes MCPResolution
	frozenMCP := mcpRes.Servers
	frozenAgentWorkspace := activeAgent.WorkspaceRoot

	actionSvc := &state.ActionService{DB: sqlDB}
	// The owner names this process to the shared database: the runs it creates
	// and resumes are stamped with it, and the lease it holds below is what
	// tells every other process on this machine that those runs are alive.
	runSvc := &state.RunStore{DB: sqlDB, Owner: source + "-" + uuid.NewString()}
	env.Leases = &Leases{DB: sqlDB, Holder: runSvc.Owner}
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
		Home:           root,
		AgentWorkspace: activeAgent.WorkspaceRoot,
		ProjectRoot:    launchProject.Project.Root,
		Trusted:        safety.TrustedRoot(launchProject) != "",
	}
	if lspOpts.ProjectRoot != "" {
		lspOpts.ProjectKey = state.ProjectKeyForRoot(lspOpts.ProjectRoot)
	}
	// pkg/lsp may not import the store's package, so the composition root
	// hands it the switch as an answer. The closure captures the workspace
	// and key by value: both are frozen for this manager's lifetime.
	ws, key := activeAgent.WorkspaceRoot, lspOpts.ProjectKey
	lspOpts.AutoAllowProject = func() bool {
		return mcp.ProjectAutoAllowed(ws, key)
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
		// The display hook reads the list the Runner runs now, not the one it
		// froze at startup: a session open re-resolves the list, and a hook
		// that closed over the startup list would keep describing a list the
		// session no longer runs.
		MCPDiagnostics: func(frozen []appcfg.MCPServerConfig) mcp.ProjectMCPScopeSummary {
			return InspectProjectMCP(frozenAgentWorkspace, frozen, launchProject)
		},
		// The re-resolution port: a conversation that is beginning asks for
		// the three scope layers again, which is the only place that knows
		// the consent store and the launch project's trust decision.
		MCPResolve: func(agentName, agentWorkspace string, launch safety.ProjectContext) (run.MCPServersResolution, error) {
			agents := env.Deps.AppCfg.Agents
			res := ResolveSessionMCP(root, agentWorkspace, agents, agentName, launch)
			return run.MCPServersResolution{Servers: res.Servers, Disabled: res.Disabled}, nil
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
	// No re-resolution either: the base runner never hosts MCP, so a
	// conversation that begins re-resolves on its project's runner, never
	// here.
	env.Deps.MCPResolve = nil
	env.Deps.MCPDiagnostics = nil
	// The same-tree guards (plan 198) read their port from the tool runtime the
	// load below builds, so it has to be there before that load: the port is
	// built once and outlives the registration, exactly as it does for the peer
	// tools, which read it again on every call.
	env.Deps.Peers = env.peerDirectory()
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
		runner.ProjectKey = state.ProjectKeyForRoot(launchProject.Project.Root)
	}
	// The session is filed under the directory it was launched in, which is the
	// same identity the read path uses: memory.ProjectKey resolves launchDir and
	// launchProject.Project.Root to one key, so what a session writes and what
	// it recalls land in the same scope. The agent workspace root is state, not
	// a project, and naming it here would send every extracted memory to a scope
	// nothing reads.
	sessStore.ConfigureMemoryDefaults(memory.SessionModeFor(cfgRoot), source, launchDir, memory.GitBranch(launchDir))
	// The global approval preset is the posture a session is born with (plan
	// 203 §2b): it is copied into each new session's own permission row, so a
	// later change here reaches only the sessions created afterwards.
	sessStore.SetApprovalPresetDefault(string(cfgRoot.ApprovalPreset.Effective()))
	// Store uploaded originals under the active agent's workspace so they fall
	// inside the agent's allowed roots and can be opened by its file tools.
	fileSvc.WorkspaceRoot = runner.WorkspaceRoot
	// A Runner that cannot be built is a startup failure, not a log line: the
	// alternative was an environment that came up with no agent behind it, and
	// every later symptom (no tools, no skills, an empty model) pointed at the
	// surface instead of at the load that actually failed. The one exception is
	// a home with no usable model yet: the gateway serves, and the first model
	// is added on the web's Settings › Models page — refusing to start would
	// leave that page unreachable (owner 2026-11-07, plan 268 revision). A turn
	// before that lands reports the missing model with that same pointer.
	if err := runner.Load(); err != nil {
		if !run.IsModelSetupIncomplete(err) {
			_ = env.LSP.Close()
			return nil, fmt.Errorf("process agent load: %w", err)
		}
		slog.Warn("no model is configured yet; sign in to the web UI and add one under Settings › Models", "err", err)
	}
	safety.UpdateManagerWithLocalConfig(env.Sandbox, root, cfgRoot, runner.PermissionSnapshot(), nil, runner.ProjectRoot)
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
		ModelHit:      sessionModelHit(runner, sessStore),
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
	env.wireRunner(runner)
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
	// The replica registration is what tells the other replicas of a shared
	// database whether a running conversation belongs here and, when it does
	// not, where to forward to (plan 213 §3.1). It rides on the lease row
	// HoldOwnerLease just created, so it cannot outlive the process either.
	kind, internalAddr := replicaRegistration(opts.ReplicaKind, source, loaded)
	if err := registerReplica(ctx, runSvc, kind, internalAddr, replicaHost()); err != nil {
		ownerStop()
		_ = env.LSP.Close()
		_ = env.Runner.Close()
		return nil, fmt.Errorf("register replica: %w", err)
	}
	return env, nil
}

// wireRunner attaches the two process-owned services every Runner needs and
// that a construction site must not have to remember individually: the
// file-reference resolver (upload IDs first, then absolute paths — see
// fileReferenceResolver) and this environment as the in-process subagent
// executor a subagent_run requires. Open wired the base Runner with both by
// hand; a per-project Runner the pool builds must receive the identical pair,
// or a gateway session running on its project's Runner loses subagent_run
// ("subagent requires an in-process executor") and cannot resolve file
// references. env.Files is nil when uploads are disabled, which
// fileReferenceResolver tolerates.
func (env *Environment) wireRunner(r *run.Runner) {
	r.FileResolver = fileReferenceResolver(env.Files, r.AgentName)
	r.SubagentExecutor = env
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
