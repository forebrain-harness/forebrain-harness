package gateway

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// ServeOptions is what the command line hands the gateway when it starts.
type ServeOptions struct {
	// Out receives the startup banner and the route table.
	Out io.Writer
	// Version is this build's version, printed under the banner.
	Version string
	// SignInLink prints a sign-in URL that carries the gateway token. The
	// command sets it only when stdout is a terminal, so the token never lands
	// in a log file that captures a service's output.
	SignInLink bool
}

func RunServeBlocking(ctx context.Context, opts ServeOptions) error {
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
	// Every process reaps the runs whose owners stopped renewing: a crashed
	// gateway replica, a terminal that was killed mid-turn. The reap is a
	// compare-and-swap, so whichever process gets there first settles the run,
	// and its ending is reported exactly once, through the session's own event
	// funnel — the same way every other ending is.
	stopReaper := turn.AbandonedRunReaper{
		Runs:    runSvc,
		Publish: gw.publishGatewayRunEvent,
		Recover: func(context.Context) { gw.recoverResolvedApprovalWaitsOnce() },
	}.Start(ctx)
	defer stopReaper()
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
	// A web turn stopped by a spent usage allowance continues by itself once
	// the allowance returns; the engine keeps the wait, and every page watching
	// the session shows it and can cancel it.
	core.SetAutoContinue(gw.autoContinueConfig())
	defer core.StopAutoContinue()
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

	authMode := gatewayAuthMode(h.Deps.AppCfg)
	signInToken := ""
	if opts.SignInLink && authMode == "token" {
		signInToken = strings.TrimSpace(h.Deps.AppCfg.Gateway.Auth.Token)
	}
	banner := startupBanner{
		Version:       opts.Version,
		Routes:        httpSrv.Routes(),
		ChannelAgent:  chReg.AgentID(),
		ChannelRoutes: chReg.RouteKeys(),
		AuthMode:      authMode,
		SignInToken:   signInToken,
		WebUI:         gw.resolveStaticFS() != nil,
	}
	return httpSrv.Run(func(a net.Addr) {
		banner.Addr = a
		if opts.Out != nil {
			writeStartupBanner(opts.Out, banner)
		}
	})
}

// bannerArt is the wordmark printed when the gateway starts.
const bannerArt = " _____              _               _\n" +
	"|  ___|__  _ __ ___| |__  _ __ __ _(_)_ __\n" +
	"| |_ / _ \\| '__/ _ \\ '_ \\| '__/ _` | | '_ \\\n" +
	"|  _| (_) | | |  __/ |_) | | | (_| | | | | |\n" +
	"|_|  \\___/|_|  \\___|_.__/|_|  \\__,_|_|_| |_|\n"

type startupBanner struct {
	Version       string      // this build's version, "" prints no version line suffix
	Routes        []RouteInfo // the router's table, web UI routes included
	ChannelAgent  string      // the primary agent whose channels are mounted
	ChannelRoutes []string    // "METHOD /path", from channel.Registry.RouteKeys
	Addr          net.Addr    // the address actually bound
	AuthMode      string
	SignInToken   string // set only when a sign-in link is to be printed
	WebUI         bool   // true when the web UI is being served
}

// displayHost maps an unspecified bind address to loopback for the clickable
// URLs: a browser cannot connect to "0.0.0.0" as a destination.
func displayHost(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// signInFragmentValue encodes the token for the link's #token= fragment the
// way the sign-in page decodes it (decodeURIComponent): a configured token is
// free text, and an unescaped "&", "#" or "%" in it would cut or corrupt the
// value the page reads back.
func signInFragmentValue(token string) string {
	return strings.ReplaceAll(url.QueryEscape(token), "+", "%20")
}

func writeStartupBanner(w io.Writer, b startupBanner) {
	var out strings.Builder
	out.WriteString(bannerArt)
	if strings.TrimSpace(b.Version) != "" {
		fmt.Fprintf(&out, "Forebrain Harness Gateway %s\n", b.Version)
	} else {
		out.WriteString("Forebrain Harness Gateway\n")
	}

	routes := make([]RouteInfo, 0, len(b.Routes)+1)
	routes = append(routes, b.Routes...)
	// /ws/chat is served by the middleware chain before the router ever sees
	// it, so it is not in the router's table; the listing must still show it.
	routes = append(routes, RouteInfo{Method: "WS", Path: "/ws/chat"})
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})
	fmt.Fprintf(&out, "\nRoutes (%d)\n", len(routes))
	for _, r := range routes {
		fmt.Fprintf(&out, "  %-8s%s\n", r.Method, r.Path)
	}

	if len(b.ChannelRoutes) > 0 {
		fmt.Fprintf(&out, "Channel routes · %s (%d)\n", strings.TrimSpace(b.ChannelAgent), len(b.ChannelRoutes))
		for _, key := range b.ChannelRoutes {
			method, path, _ := strings.Cut(key, " ")
			fmt.Fprintf(&out, "  %-8s%s\n", method, path)
		}
	}

	base := "http://" + displayHost(b.Addr)
	if b.WebUI {
		fmt.Fprintf(&out, "Web UI       %s/\n", base)
	}
	if b.SignInToken != "" {
		fmt.Fprintf(&out, "Sign in      %s/login#token=%s\n", base, signInFragmentValue(b.SignInToken))
	}
	fmt.Fprintf(&out, "Auth         %s\n", strings.TrimSpace(b.AuthMode))
	fmt.Fprintf(&out, "Listening on http://%s\n", b.Addr.String())
	_, _ = io.WriteString(w, out.String())
}
