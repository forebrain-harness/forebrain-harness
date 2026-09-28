package gateway

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

func RunServeBlocking(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	h, err := process.Open(ctx, process.OpenOptions{
		EnableUploads:   true,
		EnableChannels:  true,
		EnableTelemetry: true,
		// The gateway starts the sweeper after its durable event bus is wired,
		// so expiration can be observed live instead of only changing SQLite.
		EnableApprovalTTL: false,
		SessionSource:     "webchat",
	})
	if err != nil {
		return err
	}
	defer h.Close()
	root := h.Root
	runner := h.Runner
	hooks := h.Hooks
	actionSvc := h.Deps.Actions
	runSvc := h.Deps.RunRT
	fileSvc := h.Files
	memStore := h.Deps.MemoryStore
	sessStore := h.Deps.SessionStore
	chReg := h.Channels
	skillHub := skill.NewForWorkspace(root, runner.WorkspaceRoot, runner.LaunchProject.Project.Root)
	staticDist := strings.TrimSpace(os.Getenv("FOREBRAIN_STATIC_DIST"))
	notifier := event.NewNotifier()
	process.StartConfigHotReload(ctx, h)
	core := turn.New(
		turn.WithWorkspaceRoot(runner.StateRoot()),
		turn.WithPermissionFacade(runner),
		turn.WithRunEventStore(runSvc),
		turn.WithChildRunStore(runSvc),
		turn.WithSessionStore(sessStore),
		turn.WithForegroundLocker(h.Foreground),
		turn.WithSessionSource("web"),
		// The web /models listing discovers ChatGPT models on this node's own
		// credentials, exactly like /connect does in the terminal.
		turn.WithChatGPTModels(process.NewChatGPTModelsSource(root)),
		// The run executor is installed below, once gw exists: its options
		// close over the Server for the goal evaluator.
		// P5-4: refuse to start a turn against a session that is still parked
		// on an approval. Submit has had this gate all along but nothing
		// supplied one, so a channel message arriving while a gate was open
		// started a second run against that session. submitChannelTurn already
		// knows how to report the wait to the user.
		turn.WithApprovalGate(&turn.PendingApprovalGate{
			Runs:      runSvc,
			Actions:   actionSvc,
			Evaluator: runner,
		}),
	)
	// The pool is built before the first request so project sessions are
	// served by per-project runners from the start.
	h.EnsureRunnerPool()
	gw := &Server{
		Home:   root,
		Core:   core,
		Runner: runner,
		Env:    h,
		// The gateway's own launch project: the global skill routes act on it,
		// as they did when they implicitly resolved the process directory.
		LaunchProject: h.LaunchProject,
		Projects:      state.NewProjectStore(h.SQL, runner.AgentName),
		Actions:       actionSvc,
		RunRT:         runSvc,
		Files:         fileSvc,
		MemoryStore:   memStore,
		Sessions:      sessStore,
		Hooks:         hooks,
		SkillHub:      skillHub,
		Channels:      chReg,
		Notifier:      notifier,
		StaticDist:    staticDist,
		runControl:    h.Control,
	}
	if staticDist == "" {
		if fsys, ok := FS(); ok {
			gw.StaticFS = fsys
		}
	}
	// The same wire the terminal makes: the runtime publishes the subagent
	// stream — lifecycle, assistant text, reasoning, usage, provider web search —
	// into a surface sink. The gateway's sink is a bus because it serves more
	// than one connection.
	runner.Events = gw.RunEvents()
	stopApprovalExpiry := state.StartExpireSweeper(ctx, actionSvc, state.ExpireSweeperConfig{
		TTL:    process.ApprovalTTLFromEnv(),
		Reason: "approval_ttl_expired",
		OnExpired: func(expireCtx context.Context, actionID string) {
			gw.expireGatewayApproval(expireCtx, actionID)
		},
	})
	defer stopApprovalExpiry()
	// P8-3/P5-12: both the channel inbound turn and the websocket turn submit
	// through Core.Submit, so the executor is wired here. "webchat" is only the
	// fallback surface label — a channel turn carries its own channel id on the
	// request Origin. The goal evaluator is read per turn because it is built
	// from the live config.
	core.SetRunExecutor(h.NewRunExecutor("webchat", process.RunExecutorOptions{
		PreviewMax: 4096,
		// The gateway is unattended: a browser is not watching a status line and
		// nobody can press Escape, so a required MCP server that did not come up
		// fails the run — with every required failure named at once — instead of
		// quietly leaving its tools out of the tool table. An interactive
		// surface leaves this false and reports the same failure in its own
		// status view; refusing every turn for the rest of a session nobody can
		// intervene in would be worse than saying what is wrong.
		FailOnRequiredMCP: true,
	}))
	svc := skill.NewServiceForWorkspace(root, runner.WorkspaceRoot)
	svc.ProjectRoot = runner.LaunchProject.Project.Root
	svc.OnRefresh = func() error { return turn.RefreshSkills(svc.Home, svc.Workspace(), runner.LaunchProject) }
	svc.IsBuiltin = turn.IsBuiltinName
	_ = svc.Refresh()
	addr := strings.TrimSpace(h.Deps.AppCfg.Gateway.HTTPAddr)
	if addr == "" {
		addr = "127.0.0.1:6060"
	}
	httpSrv := NewRestServer(addr)
	gw.AttachREST(httpSrv)
	gddHandler := httpSrv.Handler
	httpSrv.Handler = ServeHTTPChain(h, gw, gddHandler)
	slog.Info("gateway auth configured", "mode", strings.TrimSpace(h.Deps.AppCfg.Gateway.Auth.Mode))
	httpSrv.Run()
	return nil
}
