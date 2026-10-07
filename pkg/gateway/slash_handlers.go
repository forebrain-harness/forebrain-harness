package gateway

import (
	"context"
	"fmt"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// HandleCompactSlash runs the manual /compact. Inline arguments are refused
// before any handler runs, so there are none to read here.
func (s *Server) HandleCompactSlash(ctx context.Context, sessionID, channel string, args []string) (string, bool) {
	if s == nil || s.Sessions == nil {
		return turn.ExecuteCompact(ctx, sessionID, nil).Reply, true
	}
	result := turn.ExecuteCompact(ctx, sessionID, run.CompactionService(s.runnerFor(ctx, sessionID), s.Sessions))
	if strings.TrimSpace(channel) == "webchat" {
		// The web draws the compaction from its own lifecycle events — running,
		// then how it ended — so the reply would only say it a second time.
		return "", true
	}
	return result.Reply, true
}

// HandleClearSlash is /clear on the web: the same context reset the terminal
// does.
func (s *Server) HandleClearSlash(ctx context.Context, sessionID, channel string, args []string) (string, bool) {
	_, _ = channel, args
	if s == nil || s.Sessions == nil {
		return "The context cannot be cleared here.", true
	}
	var snapshots turn.SnapshotStore
	if tools := s.Env.Tools(); tools != nil {
		snapshots = tools
	}
	if err := turn.ClearContext(ctx, s.Sessions, snapshots, strings.TrimSpace(sessionID)); err != nil {
		return "Could not clear the context: " + err.Error(), true
	}
	return turn.ContextClearedReply, true
}

func (s *Server) HandleContextSlash(ctx context.Context, sessionID, channel string, args []string) (string, bool) {
	_ = channel
	if s == nil || s.Runner == nil {
		return "context: unavailable", true
	}
	src := turn.ContextSources{
		Snapshots: s.Env.Tools(),
		Filtering: tool.CompressorFor(s.Runner.StateRoot()).Store(),
	}
	if s.RunRT != nil {
		src.Compactions = s.RunRT
	}
	r := s.runnerFor(ctx, sessionID)
	if r == nil {
		r = s.Runner
	}
	provider, model := run.PrimaryModelForSession(r, s.Sessions, sessionID)
	used, _ := run.ContextOccupancy(ctx, s.Sessions, sessionID)
	src.Gauge = turn.ContextGaugeOf(provider, model, used, s.compactExplicitLimit())
	return turn.ContextReport(ctx, src, sessionID), true
}

func (s *Server) HandleStatusSlash(sessionID, channel string, side bool) (string, bool) {
	_ = channel
	cfg := s.liveCfg()
	if cfg == nil {
		return "status: unavailable", true
	}
	ctx := context.Background()
	r := s.runnerFor(ctx, sessionID)
	if r == nil {
		r = s.Runner
	}
	if r == nil {
		return "status: unavailable", true
	}
	provider, model := run.PrimaryModelForSession(r, s.Sessions, sessionID)
	perm := turn.PermissionsOf(r.PermissionSnapshotForSession(sessionID), cfg)
	sessionName := ""
	if s.Sessions != nil {
		sessionName, _ = s.Sessions.SessionTitle(ctx, sessionID)
	}
	src := turn.StatusSource{
		Version:     process.AppVersion(),
		SessionName: sessionName,
		SessionID:   sessionID,
		Side:        side,
		Directory:   strings.TrimSpace(r.ProjectRoot),
		AgentID:     strings.TrimSpace(r.AgentName),
		StateRoot:   s.stateRoot(),
		ProjectKey:  r.ProjectKey,
		RunStore:    s.RunRT,
		Provider:    provider,
		Model:       model,
		Endpoint:    run.PrimaryEndpoint(r),
		Permissions: perm,
		Sandbox:     safety.SandboxStatusLine(cfg),
		MCP:         s.mcpInventorySource(r, cfg),
		ConfigFiles: turn.StatusConfigFiles(cfg, r.MCPProjectStatus().ProjectRoot),
		SkillOffer:  cfg.EffectiveFeatures().SkillOffer,
		ContextUsage: func() (int, int) {
			used, _ := run.ContextOccupancy(ctx, s.Sessions, sessionID)
			return used, s.compactExplicitLimit()
		},
	}
	if r.CodeIntelControl != nil {
		snap := r.CodeIntelControl.Snapshot()
		src.LSP = &snap
	}
	rep := turn.BuildStatusReport(ctx, src)
	return turn.RenderStatusMarkdown(rep), true
}

func (s *Server) HandlePermissionsSlash(sessionID, channel string, args []string) (string, bool) {
	_ = channel
	if s == nil {
		return "permissions: unavailable", true
	}
	r := s.runnerFor(context.Background(), sessionID)
	if r == nil {
		r = s.Runner
	}
	if r == nil {
		return "permissions: unavailable", true
	}
	if len(args) == 0 {
		snap := r.PermissionSnapshotForSession(sessionID)
		return turn.PermissionsReport(turn.PermissionsOf(snap, s.liveCfg()), snap), true
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "explain":
		return s.handlePermissionExplainSlash(sessionID, args[1:]), true
	default:
		return gatewayPermissionsUsage(), true
	}
}

func (s *Server) HandleMCPSlash(sessionID, channel string) (string, bool) {
	_ = channel
	cfg := s.liveCfg()
	r := s.runnerFor(context.Background(), sessionID)
	if r == nil {
		r = s.Runner
	}
	if cfg == nil || r == nil {
		return "mcp: unavailable", true
	}
	return turn.RenderMCPInventoryMarkdown(turn.BuildMCPInventory(s.mcpInventorySource(r, cfg))), true
}

func (s *Server) HandleLSPSlash(sessionID, channel string) (string, bool) {
	_ = channel
	r := s.runnerFor(context.Background(), sessionID)
	if r == nil {
		r = s.Runner
	}
	if r == nil || r.CodeIntelControl == nil {
		return "lsp: unavailable", true
	}
	return turn.RenderLSPInventoryMarkdown(r.CodeIntelControl.Snapshot()), true
}

// mcpInventorySource gathers a session runner's MCP facts exactly as the
// terminal does: the frozen list, the entries the disable store kept out of it,
// the live state and the published tool table — read without waiting on a
// startup still in flight.
func (s *Server) mcpInventorySource(r *run.Runner, cfg *appcfg.Root) turn.MCPInventorySource {
	scope := r.MCPProjectStatus()
	src := turn.MCPInventorySource{
		Servers:       r.MCPServers,
		Disabled:      r.MCPDisabled,
		Runtime:       mcpRuntimeView(r),
		Scope:         scope,
		Tools:         r.MCPStartup().PublishedTools(),
		ProjectSource: turn.ProjectMCPSource(scope.ProjectRoot),
		GlobalSource:  turn.GlobalMCPSource(cfg),
		ErrorLogPath:  process.ErrorLogPath(s.Home),
	}
	next, err := mcp.DisabledForNextSession(r.WorkspaceRoot)
	src.DisabledNext = next
	if err != nil {
		src.DisableStoreError = err.Error()
	}
	if reg := r.MCPRegistry(); reg != nil {
		src.ResourceServers = reg.LiveResourceServers()
	}
	return src
}

// HandleSandboxSlash reports what the sandbox is doing. Like the terminal's, it
// does not change it: the sandbox and the approval policy are two halves of one
// decision, and a command that moves only this half leaves a state no preset
// can name. This surface has no preset picker, so the sandbox is chosen in
// forebrain.yaml here — which is where a choice meant to outlive the process
// belongs anyway.
func (s *Server) HandleSandboxSlash(sessionID, channel string, args []string) (string, bool) {
	_, _, _ = sessionID, channel, args
	cfg := s.liveCfg()
	if cfg == nil {
		return "sandbox: unavailable", true
	}
	manager := (*safety.Manager)(nil)
	if s != nil && s.Env != nil && s.Env.Sandbox != nil {
		manager = s.Env.Sandbox
	}
	return safety.FormatSandboxReport(cfg, manager), true
}

func (s *Server) HandleDiffSlash(sessionID, channel string, args []string) (string, bool) {
	_ = channel
	r := s.runnerFor(context.Background(), sessionID)
	if r == nil {
		r = s.Runner
	}
	// The project /status names as the session's directory; fenced so the
	// webchat syntax-highlights it.
	return turn.ExecuteDiffSlash(r.ProjectRoot, args, true), true
}

// ModelSettings is what /model works from on the web: the live config, the
// session's agent, and the config file the providers page writes too. The
// marker is the session's own resolved choice — its stored row validated
// against the exact runner serving sessionID — falling back to that runner's
// published selection when the session never chose. The catalog comes from
// s.liveCfg(); a config reload propagates to every live pooled runner, so the
// catalog and every marker describe the same provider set.
func (s *Server) ModelSettings(sessionID string) (turn.ModelSettings, error) {
	cfg := s.liveCfg()
	path := s.configPath()
	if cfg == nil || path == "" {
		return turn.ModelSettings{}, fmt.Errorf("no configuration is bound")
	}
	agentName := "main"
	sel := turn.ModelSelectionState{}
	if r := s.runnerFor(context.Background(), sessionID); r != nil {
		if strings.TrimSpace(r.AgentName) != "" {
			agentName = strings.TrimSpace(r.AgentName)
		}
		state := s.sessionModelSelection(r, sessionID)
		sel = turn.ModelSelectionState{Provider: state.Provider, Model: state.Model, Effort: state.Effort, Set: state.Set}
	}
	return turn.ModelSettings{Config: cfg, AgentName: agentName, ConfigPath: path, Selection: sel}, nil
}

// sessionModelSelection resolves one web session's effective model: its
// stored row validated against the runner's live config, falling back to the
// runner's published selection. It is the marker every surface draws and the
// same resolution a turn's context carries.
func (s *Server) sessionModelSelection(r *run.Runner, sessionID string) run.PrimaryModelState {
	if s.Sessions == nil {
		return run.PrimaryModelSelection(r)
	}
	provider, model := run.PrimaryModelForSession(r, s.Sessions, sessionID)
	if strings.TrimSpace(provider) == "" && strings.TrimSpace(model) == "" {
		return run.PrimaryModelSelection(r)
	}
	effort := ""
	if row, ok, err := s.Sessions.SessionModelSelection(context.Background(), strings.TrimSpace(sessionID)); err == nil && ok {
		effort = row.Effort
	}
	return run.PrimaryModelState{Provider: provider, Model: model, Effort: effort, Set: true}
}

// SelectModel records choice as sessionID's own model choice. The runner's
// published selection is the process default and deliberately untouched: the
// session's next turn resolves this row into its run context and routes on
// it, so web sessions sharing a runner keep independent selections. A choice
// that no longer resolves on the session's runner is refused here, before
// anything is written.
func (s *Server) SelectModel(ctx context.Context, sessionID string, choice turn.ModelChoice, effort *string) error {
	r := s.runnerFor(ctx, sessionID)
	if r == nil {
		return fmt.Errorf("no runner is bound to this session")
	}
	if s.Sessions == nil {
		return fmt.Errorf("no session store is bound to this surface")
	}
	sid := strings.TrimSpace(sessionID)
	resolved, err := run.ResolveSessionModelChoice(r, choice.Provider, choice.Model, effort)
	if err != nil {
		return err
	}
	// A first-command model change must be durable: the row is created here,
	// not by whatever else might have touched the session first.
	if err := s.Sessions.Ensure(ctx, sid, sid); err != nil {
		return err
	}
	stored, err := s.Sessions.SaveSessionModelSelection(ctx, sid, state.SessionModelSelection{
		Provider: resolved.Provider,
		Model:    resolved.Model,
		Effort:   resolved.Effort,
	})
	if err != nil {
		return err
	}
	if !stored {
		return fmt.Errorf("the model choice could not be recorded for session %s", sid)
	}
	return nil
}

// ReloadModelCatalog takes a choice /model wrote into the shared
// catalog/default and the base runner, without selecting config index zero
// as any runner's live model. The reload propagates to every live pooled
// runner, so the whole process serves one catalog.
func (s *Server) ReloadModelCatalog(context.Context, string) error {
	if s.Env == nil {
		return nil
	}
	return s.Env.ReloadConfig()
}

// PrimaryAgents lists the primary agents /agent offers.
func (s *Server) PrimaryAgents() ([]turn.PrimaryAgent, error) {
	resolver, err := s.primaryAgentResolver()
	if err != nil {
		return nil, err
	}
	var out []turn.PrimaryAgent
	for _, summary := range resolver.All() {
		out = append(out, turn.PrimaryAgent{ID: summary.ID, WorkspaceRoot: summary.WorkspaceRoot, Active: summary.Active})
	}
	return out, nil
}

// SwitchPrimaryAgent switches to the primary agent picked from /agent, the
// way the agent switcher does.
func (s *Server) SwitchPrimaryAgent(_ context.Context, id string) (turn.PrimaryAgent, error) {
	resolver, err := s.primaryAgentResolver()
	if err != nil {
		return turn.PrimaryAgent{}, err
	}
	active, err := resolver.Switch(id)
	if err != nil {
		return turn.PrimaryAgent{}, err
	}
	if err := s.applyPrimaryAgent(active); err != nil {
		return turn.PrimaryAgent{}, err
	}
	return turn.PrimaryAgent{ID: active.ID, WorkspaceRoot: active.WorkspaceRoot, Active: true}, nil
}

// gatewayPermissionsUsage deliberately offers no way to change safety.
// The preset picker is a terminal surface; a web chat gets the snapshot and
// explain, and declares anything durable in the project's settings file.
func gatewayPermissionsUsage() string {
	return turn.PermissionsUsage(false)
}

func (s *Server) handlePermissionExplainSlash(sessionID string, args []string) string {
	// Same delegate as the TUI: ExecutePermissionExplain already answers an
	// empty tool with the full usage, so there is no separate short-form
	// branch here.
	if s == nil {
		return turn.ExecutePermissionExplain(nil, sessionID, args, turn.PermissionExplainUsage)
	}
	return turn.ExecutePermissionExplain(s.runnerFor(context.Background(), sessionID), sessionID, args, turn.PermissionExplainUsage)
}

// mcpRuntimeView is the runner's own view of the servers it started, in
// configuration order; nil before the first startup has registered any.
func mcpRuntimeView(r *run.Runner) *turn.MCPRuntimeView {
	if r == nil {
		return nil
	}
	snapshot := r.MCPStartup().Snapshot()
	if len(snapshot.Servers) == 0 {
		return nil
	}
	return &turn.MCPRuntimeView{
		Servers:    snapshot.Servers,
		Generation: snapshot.Generation,
		Pending:    snapshot.Pending,
	}
}
