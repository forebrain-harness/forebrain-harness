// Slash command handlers on the chat session, plus skill commands.
package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/migrate"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// HandleCompactSlash runs the manual /compact. Inline arguments are refused
// before any handler runs, so there are none to read here.
func (s *ChatSession) HandleCompactSlash(ctx context.Context, sessionID, channel string, args []string) (string, bool) {
	if s == nil || s.sessStore() == nil {
		return turn.ExecuteCompact(ctx, sessionID, nil).Reply, true
	}
	result := turn.ExecuteCompact(ctx, sessionID, run.CompactionService(s.runner(), s.sessStore()))
	s.tuiMu.Lock()
	hasTUI := s.uiNotify != nil
	s.tuiMu.Unlock()
	if hasTUI {
		// The compaction's own events draw its card — running, then how it
		// ended — so the screen needs no second account of it.
		return "", true
	}
	return result.Reply, true
}

func (s *ChatSession) HandleClearSlash(ctx context.Context, sessionID, channel string, args []string) (string, bool) {
	_, _ = channel, args
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		sid = "default"
	}
	if err := s.ClearSurfaceSession(ctx, sid); err != nil {
		return "Could not clear the context: " + err.Error(), true
	}
	return turn.ContextClearedReply, true
}

func (s *ChatSession) HandleContextSlash(ctx context.Context, sessionID, channel string, args []string) (string, bool) {
	_ = channel
	if s == nil || s.runner() == nil {
		return "context: unavailable", true
	}
	sid := strings.TrimSpace(sessionID)
	src := turn.ContextSources{
		Snapshots: s.Env.Tools(),
		Filtering: tool.CompressorFor(s.runner().StateRoot()).Store(),
	}
	if runs := s.runSvc(); runs != nil {
		src.Compactions = runs
	}
	provider, model := run.PrimaryModel(s.runner())
	used, _ := s.contextOccupancy(ctx, sid)
	src.Gauge = turn.ContextGaugeOf(provider, model, used, s.compactExplicitLimit())
	return turn.ContextReport(ctx, src, sid), true
}

func (s *ChatSession) HandleStatusSlash(sessionID, channel string, side bool) (string, bool) {
	_ = channel
	cfg := mcpConfigFromChatSession(s)
	if cfg == nil || s.runner() == nil {
		return "status: unavailable", true
	}
	rep := turn.BuildStatusReport(context.Background(), s.statusSource(cfg, sessionID, side))
	return turn.RenderStatusMarkdown(rep), true
}

// statusSource gathers this session's facts for the shared /status builder.
// It backs both the markdown reply (non-TTY) and the interactive panel, so
// the two cannot disagree. The caller has checked the runner.
func (s *ChatSession) statusSource(cfg *appcfg.Root, sessionID string, side bool) turn.StatusSource {
	r := s.runner()
	provider, model := run.PrimaryModel(r)
	sid := strings.TrimSpace(sessionID)
	sessionName := ""
	if store := s.sessStore(); store != nil {
		sessionName, _ = store.SessionTitle(context.Background(), sid)
	}
	perm := turn.PermissionsOf(r.PermissionSnapshotForSession(sid), cfg)
	var instructions []assembly.RuleSource
	if hook := s.Env.RulesHook(); hook != nil {
		instructions = hook.InstructionSources()
	}
	src := turn.StatusSource{
		Version:      home.Version,
		SessionName:  sessionName,
		SessionID:    sid,
		Side:         side,
		Directory:    strings.TrimSpace(r.ProjectRoot),
		AgentID:      strings.TrimSpace(r.AgentName),
		StateRoot:    s.stateRoot(),
		ProjectKey:   runnerProjectKey(r),
		RunStore:     s.runSvc(),
		Provider:     provider,
		Model:        model,
		Endpoint:     run.PrimaryEndpoint(r),
		FastOn:       s.IsFastMode(),
		Permissions:  perm,
		Sandbox:      safety.SandboxStatusLine(cfg),
		MCP:          s.mcpInventorySource(),
		Instructions: instructions,
		ConfigFiles:  turn.StatusConfigFiles(cfg, r.MCPProjectStatus().ProjectRoot),
		SkillOffer:   cfg.EffectiveFeatures().SkillOffer,
		ContextUsage: func() (int, int) {
			used, _ := s.contextOccupancy(context.Background(), sid)
			return used, s.compactExplicitLimit()
		},
	}
	if ctl := r.CodeIntelControl; ctl != nil {
		snap := ctl.Snapshot()
		src.LSP = &snap
	}
	return src
}

// mcpInventorySource gathers this session's MCP facts: the frozen list, the
// entries the disable store kept out of it, and the live state. It never
// waits for an MCP startup still in flight — the panel shows it connecting.
func (s *ChatSession) mcpInventorySource() turn.MCPInventorySource {
	r := s.runner()
	scope := r.MCPProjectStatus()
	src := turn.MCPInventorySource{
		Servers:       r.MCPServers,
		Disabled:      r.MCPDisabled,
		Runtime:       runnerMCPRuntime(r),
		Scope:         scope,
		Tools:         r.MCPStartup().PublishedTools(),
		ProjectSource: turn.ProjectMCPSource(scope.ProjectRoot),
		GlobalSource:  turn.GlobalMCPSource(mcpConfigFromChatSession(s)),
		ErrorLogPath:  filepath.Join(s.home(), home.LogsDir, "error.log"),
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

func (s *ChatSession) HandlePermissionsSlash(sessionID, channel string, args []string) (string, bool) {
	_ = channel
	if s == nil || s.runner() == nil {
		return "permissions: unavailable", true
	}
	if len(args) == 0 {
		snap := s.runner().PermissionSnapshotForSession(sessionID)
		return turn.PermissionsReport(turn.PermissionsOf(snap, s.cfg()), snap), true
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "explain":
		return s.handlePermissionExplainSlash(sessionID, args[1:]), true
	default:
		return permissionsUsage(), true
	}
}

func (s *ChatSession) ModelSummaryString() string {
	if s == nil {
		return ""
	}
	provider, model := run.PrimaryModel(s.runner())
	if strings.TrimSpace(provider) == "" && strings.TrimSpace(model) == "" {
		return "model: unavailable"
	}
	if strings.TrimSpace(provider) == "" {
		return "model=" + strings.TrimSpace(model)
	}
	if strings.TrimSpace(model) == "" {
		return "provider=" + strings.TrimSpace(provider)
	}
	return strings.TrimSpace("provider=" + strings.TrimSpace(provider) + "\nmodel=" + strings.TrimSpace(model))
}

func (s *ChatSession) SkillListString() string {
	if s == nil || strings.TrimSpace(s.home()) == "" {
		return ""
	}
	overview, err := s.skillLifecycleService().Overview()
	if err != nil {
		return "skills: unavailable"
	}
	if len(overview.Installed) == 0 {
		return "skills: no skills available"
	}
	var b strings.Builder
	b.WriteString("skills:\n")
	for _, it := range overview.Installed {
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(it.Name))
		if desc := strings.TrimSpace(it.Description); desc != "" {
			b.WriteString(": ")
			b.WriteString(desc)
		}
		if src := strings.TrimSpace(skillSourceLabel(strings.TrimSpace(s.home()), strings.TrimSpace(it.RootPath))); src != "" {
			b.WriteString(" [")
			b.WriteString(src)
			b.WriteString("]")
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func (s *ChatSession) CurrentModelOption() string {
	return llm.FormatProviderModel(run.PrimaryModel(s.runner()))
}

func (s *ChatSession) CurrentModelReasoningEffort() string {
	return run.PrimaryReasoningEffort(s.runner())
}

// SubagentModelSummary names the model and reasoning effort one subagent type
// runs on, and reports false when the type has no chain of its own and
// therefore runs on whatever the primary agent runs on. The caller shows the
// primary agent's own footer values in that case, which is what the runtime
// actually does.
func (s *ChatSession) SubagentModelSummary(agentType string) (model string, effort string, ok bool) {
	if s == nil {
		return "", "", false
	}
	provider, id, effort, own := run.SubagentOwnModel(s.runner(), agentType)
	if !own {
		return "", "", false
	}
	return llm.FormatProviderModel(provider, id), effort, true
}

func (s *ChatSession) AvailableSkillOptions() []string {
	if s == nil || strings.TrimSpace(s.home()) == "" {
		return nil
	}
	list, err := s.skillLifecycleService().List()
	if err != nil || len(list) == 0 {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, it := range list {
		label := strings.TrimSpace(it.Name)
		if desc := strings.TrimSpace(it.Description); desc != "" {
			label += ": " + desc
		}
		out = append(out, label)
	}
	return out
}

func (s *ChatSession) AvailableSkillToggleOptions() []skill.Entry {
	if s == nil || strings.TrimSpace(s.home()) == "" {
		return nil
	}
	infos, err := skill.DiscoverForWorkspace(strings.TrimSpace(s.home()), strings.TrimSpace(s.skillLifecycleService().WorkspaceRoot), s.LaunchProject.Project.Root)
	if err != nil || len(infos) == 0 {
		return nil
	}
	out := make([]skill.Entry, 0, len(infos))
	for _, it := range infos {
		name := strings.TrimSpace(it.Name)
		if name == "" {
			continue
		}
		out = append(out, it)
	}
	return out
}

func (s *ChatSession) ApplySkillSelection(label string) (string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return "", fmt.Errorf("skills: empty selection")
	}
	name := label
	if base, _, ok := strings.Cut(label, ":"); ok {
		name = strings.TrimSpace(base)
	}
	if strings.TrimSpace(s.home()) == "" {
		return "", fmt.Errorf("skills: unavailable")
	}
	res, err := s.skillLifecycleService().Inspect(name)
	if err != nil {
		return "", err
	}
	allowedTools := strings.TrimSpace(res.AllowedTools)
	if allowedTools == "" {
		allowedTools = readAllowedToolsFromSkillMarkdown(strings.TrimSpace(res.Path))
	}
	if allowedTools == "" {
		allowedTools = "(all by default)"
	}
	desc := strings.TrimSpace(res.Description)
	if desc == "" {
		desc = "(none)"
	}
	return fmt.Sprintf("skill: %s\ndescription: %s\nsource: %s\ntrust: %s\npath: %s\nallowed_tools: %s", strings.TrimSpace(res.Name), desc, strings.TrimSpace(res.Source), strings.TrimSpace(res.Trust), strings.TrimSpace(res.Path), allowedTools), nil
}

// The skills catalog is part of the prompt prefix and is frozen when a session
// starts, so a skill change is picked up by the next session rather than the
// running one. These notes say so rather than letting a reply imply the change
// is already live. The skill stays reachable by name meanwhile: /<skill-name>
// reads SKILL.md from disk for that turn without touching the prefix.
const skillToggleNextSessionNote = "\nnote: applies to a new session; this session's skills catalog is unchanged"

func skillLoadNowNote(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return skillToggleNextSessionNote
	}
	return "\nnote: listed in the skills catalog of a new session; use /" + name + " to load it in this one"
}

func (s *ChatSession) ApplySkillEnabledSelection(enabledPaths []string) (string, error) {
	if s == nil || strings.TrimSpace(s.home()) == "" {
		return "", fmt.Errorf("skills: unavailable")
	}
	svc := s.skillLifecycleService()
	if err := svc.SetEnabledPaths(enabledPaths); err != nil {
		return "", err
	}
	entries, err := skill.DiscoverForWorkspace(strings.TrimSpace(s.home()), strings.TrimSpace(svc.WorkspaceRoot), svc.ProjectRoot)
	if err != nil {
		return "skills: updated" + skillToggleNextSessionNote, nil
	}
	enabledCount := 0
	for _, item := range entries {
		if item.Enabled {
			enabledCount++
		}
	}
	return fmt.Sprintf("skills: updated enabled set (%d/%d enabled)", enabledCount, len(entries)) + skillToggleNextSessionNote, nil
}

// InstallSkillPackageAsync fetches and installs a skill package on its own
// goroutine, reporting through the notification queue. Fetching a package is
// network work that routinely runs for tens of seconds; running it on the
// surface's event loop left the composer painted but dead for that whole
// span, so the install is a background job and the conversation stays fully
// usable while it runs. The install dies with ctx, so leaving the session
// abandons an in-flight clone instead of letting it finish unobserved.
func (s *ChatSession) InstallSkillPackageAsync(ctx context.Context, sourceRef string, destScope string) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sourceRef = strings.TrimSpace(sourceRef)
	id := "skill-install:" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if strings.TrimSpace(s.home()) == "" {
		s.notifyUI(SkillInstallDoneMsg{ID: id, SourceRef: sourceRef, Err: errors.New("this session has no skills home, so nothing can be installed")})
		return
	}
	req := skill.InstallRequest{SourceRef: sourceRef, DestScope: skillInstallScope(destScope)}
	svc := s.skillLifecycleService()

	var phaseMu sync.Mutex
	phase := skill.InstallProgress{Phase: "starting"}
	svc.OnInstallProgress = func(progress skill.InstallProgress) {
		phaseMu.Lock()
		phase = progress
		phaseMu.Unlock()
	}
	currentPhase := func() string {
		phaseMu.Lock()
		defer phaseMu.Unlock()
		return phase.Describe()
	}

	started := time.Now()
	s.notifyUI(SkillInstallProgressMsg{ID: id, SourceRef: sourceRef, Phase: currentPhase()})
	go func() {
		type outcome struct {
			res *skill.InstallResult
			err error
		}
		resultCh := make(chan outcome, 1)
		go func() {
			res, err := svc.Install(ctx, req)
			resultCh <- outcome{res: res, err: err}
		}()
		// The install reports a phase only when it changes, and the slowest
		// phase is the one that reports nothing while a clone runs. The card
		// is therefore re-emitted on a clock so its elapsed time keeps moving
		// and the user can see the install is still alive.
		ticker := time.NewTicker(skillInstallTick)
		defer ticker.Stop()
		for {
			select {
			case done := <-resultCh:
				if done.err != nil {
					s.notifyUI(SkillInstallDoneMsg{ID: id, SourceRef: sourceRef, Elapsed: time.Since(started), Err: done.err})
					return
				}
				s.notifyUI(SkillInstallDoneMsg{
					ID:        id,
					SourceRef: sourceRef,
					Elapsed:   time.Since(started),
					Summary:   skillInstallSummary(done.res),
					Report:    skillInstallReport(done.res),
				})
				return
			case <-ticker.C:
				s.notifyUI(SkillInstallProgressMsg{ID: id, SourceRef: sourceRef, Phase: currentPhase(), Elapsed: time.Since(started)})
			}
		}
	}()
}

// skillInstallTick is how often a running install re-reports itself. It
// matches the cadence the surface can usefully repaint a live card at.
const skillInstallTick = 200 * time.Millisecond

func skillInstallScope(destScope string) skill.Scope {
	switch strings.ToLower(strings.TrimSpace(destScope)) {
	case "project":
		return skill.ScopeProject
	case "workspace":
		return skill.ScopeWorkspace
	default:
		return skill.ScopeGlobal
	}
}

// skillInstallSummary is the one-line verdict the finished card's header
// shows.
func skillInstallSummary(res *skill.InstallResult) string {
	if res == nil {
		return "installed"
	}
	if len(res.Installed) > 1 {
		return fmt.Sprintf("installed %d skills", len(res.Installed))
	}
	name := strings.TrimSpace(res.Name)
	if name == "" {
		return "installed"
	}
	return "installed " + name
}

// skillInstallReport is the finished card's body: what landed, where it came
// from, and how to reach it. A skill joins the catalog a new session builds,
// so the note names the one way to use it in this one.
func skillInstallReport(res *skill.InstallResult) string {
	if res == nil {
		return strings.TrimPrefix(skillToggleNextSessionNote, "\n")
	}
	lines := []string{}
	if len(res.Installed) > 1 {
		names := make([]string, 0, len(res.Installed))
		for _, item := range res.Installed {
			names = append(names, strings.TrimSpace(item.Name))
		}
		lines = append(lines, "names: "+strings.Join(names, ", "))
		lines = append(lines, "source: "+strings.TrimSpace(res.Source))
		lines = append(lines, strings.TrimPrefix(skillToggleNextSessionNote, "\n"))
		return strings.Join(lines, "\n")
	}
	if len(res.Installed) == 1 {
		lines = append(lines, "path: "+strings.TrimSpace(res.Installed[0].SkillPath))
	}
	if res.Metadata != nil {
		lines = append(lines, "source: "+strings.TrimSpace(res.Metadata.Source))
		lines = append(lines, "trust: "+strings.TrimSpace(res.Metadata.Trust))
		if len(res.Installed) != 1 {
			lines = append(lines, "path: "+strings.TrimSpace(res.SkillPath))
		}
	}
	lines = append(lines, strings.TrimPrefix(skillLoadNowNote(res.Name), "\n"))
	return strings.Join(lines, "\n")
}

// SkillSelectionByName resolves a /skills picker entry to the same trusted
// selection an explicit /<skill-name> invocation submits: the skill's name and
// the path of its SKILL.md. It never reads the file — loading belongs to the
// runner's preload phase, where the load is visible and failures get the Skill
// failure card.
func (s *ChatSession) SkillSelectionByName(name string) (string, string, error) {
	if s == nil || strings.TrimSpace(s.home()) == "" {
		return "", "", fmt.Errorf("skills: unavailable")
	}
	res, err := s.skillLifecycleService().Inspect(strings.TrimSpace(name))
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(res.Name), filepath.Join(strings.TrimSpace(res.Path), "SKILL.md"), nil
}

func (s *ChatSession) CreateSkill(name string, content string) (string, error) {
	if s == nil || strings.TrimSpace(s.home()) == "" {
		return "", fmt.Errorf("skills: unavailable")
	}
	svc := s.skillLifecycleService()
	item, err := svc.Create(skill.CreateRequest{Name: name, Content: content})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("skills: created %s\npath: %s", strings.TrimSpace(item.Name), strings.TrimSpace(item.Path)) + skillLoadNowNote(item.Name), nil
}

func (s *ChatSession) UpdateSkill(name string, content string) (string, error) {
	if s == nil || strings.TrimSpace(s.home()) == "" {
		return "", fmt.Errorf("skills: unavailable")
	}
	svc := s.skillLifecycleService()
	item, err := svc.Update(skill.UpdateRequest{Name: name, Content: content})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("skills: updated %s\npath: %s", strings.TrimSpace(item.Name), strings.TrimSpace(item.Path)) + skillLoadNowNote(item.Name), nil
}

func skillSourceLabel(home string, rootPath string) string {
	home = strings.TrimSpace(home)
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return "unknown"
	}
	absRoot := rootPath
	if resolved, err := filepath.Abs(rootPath); err == nil {
		absRoot = filepath.Clean(resolved)
	}
	if home != "" {
		if resolvedHome, err := filepath.Abs(home); err == nil {
			home = filepath.Clean(resolvedHome)
			wsSkills := filepath.Join(home, "workspace", "skills")
			homeSkills := filepath.Join(home, "skills")
			if hasPathPrefix(absRoot, wsSkills) {
				return "workspace"
			}
			if hasPathPrefix(absRoot, homeSkills) {
				return "home"
			}
		}
	}
	return "external"
}

func hasPathPrefix(path string, prefix string) bool {
	path = filepath.Clean(strings.TrimSpace(path))
	prefix = filepath.Clean(strings.TrimSpace(prefix))
	if path == prefix {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(path, prefix+sep)
}

func readAllowedToolsFromSkillMarkdown(rootPath string) string {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(rootPath, "SKILL.md"))
	if err != nil || len(raw) == 0 {
		return ""
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "---" {
			break
		}
		if !strings.HasPrefix(strings.ToLower(line), "allowed_tools:") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, "allowed_tools:"))
	}
	return ""
}

// HandleFastSlash routes /fast invocations from the slash executor.
// Recognizes: /fast (toggle), /fast on, /fast off, /fast status.
// Rejects when the active model is not Anthropic Opus.
func (s *ChatSession) HandleFastSlash(sessionID, channel string, args []string) (string, bool) {
	_, _ = sessionID, channel
	if !llm.IsOpusLabel(s.CurrentModelOption()) {
		return "fast: unavailable (current model is not Anthropic Opus)", true
	}
	current := s.IsFastMode()
	action := ""
	if len(args) > 0 {
		action = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch action {
	case "", "toggle":
		next := !current
		if err := s.SetFastMode(next); err != nil {
			return "fast: " + err.Error(), true
		}
		if next {
			return "fast: on (Anthropic priority service tier)", true
		}
		return "fast: off", true
	case "on", "true", "1", "enable":
		if current {
			return "fast: already on", true
		}
		if err := s.SetFastMode(true); err != nil {
			return "fast: " + err.Error(), true
		}
		return "fast: on (Anthropic priority service tier)", true
	case "off", "false", "0", "disable":
		if !current {
			return "fast: already off", true
		}
		if err := s.SetFastMode(false); err != nil {
			return "fast: " + err.Error(), true
		}
		return "fast: off", true
	case "status":
		if current {
			return "fast: on", true
		}
		return "fast: off", true
	default:
		return "fast: usage /fast [on|off|status]", true
	}
}

// PrimaryAgents lists the primary agents /agent offers.
func (s *ChatSession) PrimaryAgents() ([]turn.PrimaryAgent, error) {
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

// SwitchPrimaryAgent switches to the primary agent picked from /agent.
func (s *ChatSession) SwitchPrimaryAgent(_ context.Context, id string) (turn.PrimaryAgent, error) {
	summary, err := process.SwitchAgent(s.agentSwitchDeps(), id)
	if err != nil {
		return turn.PrimaryAgent{}, err
	}
	return turn.PrimaryAgent{ID: summary.ID, WorkspaceRoot: summary.WorkspaceRoot, Active: true}, nil
}

// ModelSettings is what /model works from in the terminal: the live config,
// the agent whose models it offers, and the selection the runner is actually
// running — never config order alone.
func (s *ChatSession) ModelSettings(string) (turn.ModelSettings, error) {
	path, err := home.ResolveConfigPath(strings.TrimSpace(s.home()))
	if err != nil {
		return turn.ModelSettings{}, err
	}
	sel := run.PrimaryModelSelection(s.runner())
	return turn.ModelSettings{
		Config:     modelConfigFromChatSession(s),
		AgentName:  chatSessionActiveAgentName(s),
		ConfigPath: path,
		Selection: turn.ModelSelectionState{
			Provider: sel.Provider,
			Model:    sel.Model,
			Effort:   sel.Effort,
			Set:      sel.Set,
		},
	}, nil
}

// ReloadModelCatalog takes a default-file write /model made into the live
// session without selecting config index zero as the live model: the reload
// refreshes the catalog and provider details while the ordinary pinned
// reload keeps the session's effective selection.
func (s *ChatSession) ReloadModelCatalog(context.Context, string) error {
	return s.reloadConfigFromDisk()
}

// SelectModel applies choice as the live model for sessionID and persists it
// as that session's own selection. The live apply and the persistence are one
// outcome: a persistence failure rolls the live runner back to the previous
// selection rather than leaving a runtime nothing will restore.
func (s *ChatSession) SelectModel(ctx context.Context, sessionID string, choice turn.ModelChoice, effort *string) error {
	ctx, cancel := context.WithTimeout(ctx, selectModelTimeout)
	defer cancel()
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return fmt.Errorf("select model: no active session")
	}
	store := s.sessStore()
	if store == nil {
		return fmt.Errorf("select model: session store unavailable")
	}
	r := s.runner()
	if r == nil {
		return fmt.Errorf("select model: runner unavailable")
	}
	// A first-command model change must be durable: the row is created here,
	// not by whatever else might have touched the session first.
	if err := store.Ensure(ctx, sid, sid); err != nil {
		return err
	}
	previous := run.PrimaryModelSelection(r)
	choice.Provider = strings.TrimSpace(choice.Provider)
	choice.Model = strings.TrimSpace(choice.Model)
	if err := r.SetPrimaryModel(choice.Provider, choice.Model, effort); err != nil {
		if run.IsPrimaryModelRuntimeUncertain(err) {
			return turn.NewModelSelectionError(turn.ModelRuntimeUncertain, err)
		}
		return err
	}
	published := run.PrimaryModelSelection(r)
	stored, err := store.SaveSessionModelSelection(ctx, sid, state.SessionModelSelection{
		Provider: published.Provider,
		Model:    published.Model,
		Effort:   published.Effort,
	})
	if err == nil && stored {
		return nil
	}
	if err == nil {
		err = fmt.Errorf("session %s has no row to store the model choice", sid)
	}
	// The live runner is on the requested model but nothing durable says so:
	// restore the outgoing selection, and report precisely if that fails.
	restoreErr := s.restoreRunnerSelection(ctx, r, previous)
	switch {
	case run.IsPrimaryModelRuntimeUncertain(restoreErr):
		return turn.NewModelSelectionError(turn.ModelRuntimeUncertain, restoreErr)
	case restoreErr != nil && run.PrimaryModelSelection(r) == published:
		// The runner kept the requested runtime while the row was not
		// written: applied, not durable.
		return turn.NewModelSelectionError(turn.ModelAppliedNotDurable, err)
	case restoreErr != nil:
		return turn.NewModelSelectionError(turn.ModelRuntimeUncertain, fmt.Errorf("persistence failed (%v) and the rollback also failed (%w)", err, restoreErr))
	default:
		return fmt.Errorf("select model: %w", err)
	}
}

// restoreRunnerSelection puts the runner back on the selection a failed
// switch was rolling back to, resetting when there was none.
func (s *ChatSession) restoreRunnerSelection(_ context.Context, r *run.Runner, previous run.PrimaryModelState) error {
	if !previous.Set {
		return r.ResetPrimaryModel()
	}
	effort := previous.Effort
	return r.SetPrimaryModel(previous.Provider, previous.Model, &effort)
}

// selectModelTimeout bounds the persistence half of a live selection.
const selectModelTimeout = 30 * time.Second

func (s *ChatSession) AgentRosterSnapshot(sessionID string) AgentRosterSnapshot {
	if s == nil {
		return AgentRosterSnapshot{}
	}
	sessionID = strings.TrimSpace(sessionID)
	resolver, err := s.primaryAgentResolver()
	if err != nil {
		return AgentRosterSnapshot{}
	}
	active, err := resolver.Active()
	if err != nil {
		return AgentRosterSnapshot{}
	}
	// The primary agent's row says whether it is working, not that it is the
	// agent in force — every row here belongs to that one.
	rows := []AgentRosterRow{{
		ID:        active.ID,
		Kind:      "primary",
		Label:     active.ID,
		Status:    "idle",
		SessionID: sessionID,
	}}
	if s.tuiRunActive() {
		rows[0].Status = "running"
		s.tuiRunMu.Lock()
		rows[0].RunID = s.tuiRunIDLocked(sessionID)
		s.tuiRunMu.Unlock()
	}

	seen := map[string]int{streamAgentRosterKey(rows[0]): 0}
	if s.sessStore() != nil && sessionID != "" {
		children, err := s.sessStore().ListChildSessionsRecent(context.Background(), sessionID, 50)
		if err == nil {
			for _, child := range children {
				row := AgentRosterRow{
					ID:        child.ID,
					Kind:      "subagent",
					ParentID:  sessionID,
					Label:     child.Title,
					Status:    "available",
					SessionID: child.ID,
				}
				if strings.TrimSpace(row.Label) == "" {
					row.Label = child.ID
				}
				rows = appendOrReplaceStreamRosterRow(rows, seen, row, false)
			}
		}
	}

	for _, entry := range agent.RegistryFor(s.stateRoot()).List(agent.Query{SessionID: sessionID, Limit: 200}) {
		row := streamSubagentRosterRow(entry)
		if strings.TrimSpace(row.ID) == "" {
			continue
		}
		rows = appendOrReplaceStreamRosterRow(rows, seen, row, true)
	}
	return AgentRosterSnapshot{Rows: rows}
}

func (s *ChatSession) CancelSubagent(query SubagentControlQuery) bool {
	taskID := strings.TrimSpace(query.TaskID)
	runID := strings.TrimSpace(query.RunID)
	agentID := strings.TrimSpace(query.AgentID)
	reg := agent.RegistryFor(s.stateRoot())
	if taskID != "" && reg.Cancel(agent.Query{TaskID: taskID}) {
		return true
	}
	if runID != "" && reg.Cancel(agent.Query{RunID: runID}) {
		return true
	}
	if agentID == "" {
		return false
	}
	for _, entry := range reg.List(agent.Query{Limit: 500}) {
		if entry.AgentID != agentID && entry.TaskID != agentID && entry.RunID != agentID {
			continue
		}
		if strings.TrimSpace(entry.TaskID) != "" && reg.Cancel(agent.Query{TaskID: entry.TaskID}) {
			return true
		}
		if strings.TrimSpace(entry.RunID) != "" && reg.Cancel(agent.Query{RunID: entry.RunID}) {
			return true
		}
	}
	return false
}

func (s *ChatSession) CancelAllAgents() AgentCancelSummary {
	out := AgentCancelSummary{}
	if s.CancelActiveRun() {
		out.Main = 1
	}
	for _, entry := range agent.RegistryFor(s.stateRoot()).List(agent.Query{Limit: 500}) {
		query := SubagentControlQuery{
			AgentID: strings.TrimSpace(entry.AgentID),
			TaskID:  strings.TrimSpace(entry.TaskID),
			RunID:   strings.TrimSpace(entry.RunID),
		}
		if query.AgentID == "" {
			query.AgentID = query.TaskID
		}
		if query.AgentID == "" {
			query.AgentID = query.RunID
		}
		if s.CancelSubagent(query) {
			out.Subagents++
		}
	}
	return out
}

func (s *ChatSession) primaryAgentResolver() (*appcfg.Resolver, error) {
	if s == nil {
		return nil, fmt.Errorf("primary agent session unavailable")
	}
	return appcfg.NewResolver(strings.TrimSpace(s.home()), s.cfg())
}

// agentSwitchDeps binds this session's runtime to the shared switch routine, so
// the terminal rebuilds the isolation boundary exactly as the gateway does.
func (s *ChatSession) agentSwitchDeps() process.AgentDeps {
	if s == nil {
		return process.AgentDeps{}
	}
	return process.AgentDeps{
		Home:    strings.TrimSpace(s.home()),
		Cfg:     s.cfg(),
		Runner:  s.runner(),
		Sandbox: s.sandbox(),
	}
}

// stateRoot returns the active primary agent's per-agent state root (its
// workspace root), onto which the mode/plan/todo/faststate stores join "state".
// Every TUI seam touching per-agent state must use this so the main agent
// and a non-main agent never share a state directory.
func (s *ChatSession) stateRoot() string {
	if s == nil {
		return ""
	}
	return appcfg.ActiveStateRoot(strings.TrimSpace(s.home()), s.cfg())
}

// BuildMigrateOptions is the exported form the migrate picker flow uses.
func (s *ChatSession) BuildMigrateOptions(onlyProject bool, sessionID string, sourceRoot string) *migrate.Options {
	return s.buildMigrateOptions(onlyProject, sessionID, sourceRoot)
}

// RunMigrationAsync is the exported form the migrate picker flow uses.
func (s *ChatSession) RunMigrationAsync(ctx context.Context, opts *migrate.Options) {
	s.runMigrationAsync(ctx, opts)
}

// buildMigrateOptions wires a migrate.Options from this session's
// environment: the state database, the active agent's workspace and tenant,
// the config file, and the consolidation callback the composition root owns
// (B8 — the layer ceiling forbids migrate from reaching the runner itself).
// sourceRoot carries the validated source directory (empty = default).
func (s *ChatSession) buildMigrateOptions(onlyProject bool, sessionID string, sourceRoot string) *migrate.Options {
	if s == nil || s.Env == nil || s.Env.SQL == nil {
		return nil
	}
	workspace := ""
	if runner := s.runner(); runner != nil {
		workspace = strings.TrimSpace(runner.WorkspaceRoot)
	}
	if workspace == "" {
		workspace = s.stateRoot()
	}
	configPath := ""
	if path, err := home.ResolveConfigPath(strings.TrimSpace(s.home())); err == nil {
		configPath = path
	}
	agentID := ""
	if store := s.sessStore(); store != nil {
		agentID = store.AgentID()
	}
	return &migrate.Options{
		OnlyProject:        onlyProject,
		CurrentProjectRoot: strings.TrimSpace(s.WorkingDir),
		SessionID:          strings.TrimSpace(sessionID),
		Home:               strings.TrimSpace(s.home()),
		AgentID:            agentID,
		AgentWorkspace:     workspace,
		ConfigPath:         configPath,
		DB:                 s.Env.SQL,
		SourceRoot:         strings.TrimSpace(sourceRoot),
		InputHistoryPath:   filepath.Join(workspace, "state", "cli-input-history.txt"),
		Consolidate: func(ctx context.Context, sid string) error {
			if runner := s.runner(); runner != nil {
				return runner.ConsolidateMemoriesNow(ctx, sid)
			}
			return errors.New("runner unavailable")
		},
	}
}

// runMigrationAsync runs the import on a goroutine and reports through the
// notification queue, so the event loop keeps serving input and the viewport
// while transcripts are parsed. Progress is throttled to the rate the status
// line can usefully show.
func (s *ChatSession) runMigrationAsync(ctx context.Context, opts *migrate.Options) {
	if s == nil || opts == nil {
		return
	}
	source := opts.SourceName()
	go func() {
		report, err := migrate.RunSource(ctx, source, opts, s.migrateProgressSink())
		if err != nil {
			s.notifyUI(MigrationDoneMsg{Title: "Migration failed", Err: err})
			return
		}
		title := "Migration preview"
		if !opts.DryRun {
			title = "Migration complete"
		}
		s.persistMigrationReport(opts.SessionID, source, title, report)
		s.notifyUI(MigrationDoneMsg{Title: title, Report: report.Text()})
	}()
}

// PreviewMigrationAsync runs the dry run whose plan the confirm dialog shows.
func (s *ChatSession) PreviewMigrationAsync(ctx context.Context, opts *migrate.Options, run func()) {
	if s == nil || opts == nil {
		return
	}
	source := opts.SourceName()
	go func() {
		plan, report, err := migrate.PlanSource(ctx, source, opts, s.migrateProgressSink())
		if err != nil {
			s.notifyUI(MigrationDoneMsg{Title: "Migration preview failed", Err: err})
			return
		}
		s.notifyUI(MigrationPreviewMsg{Plan: plan.Text(), Report: report.Text(), Run: run})
	}()
}

// migrateProgressSink adapts migrate's progress callback onto the
// notification queue with a 200ms floor between emissions: the queue is
// serialized with rendering, and transcript parsing can emit thousands of
// checkpoints over a large history.
func (s *ChatSession) migrateProgressSink() func(migrate.Progress) {
	var mu sync.Mutex
	var last time.Time
	return func(progress migrate.Progress) {
		now := time.Now()
		mu.Lock()
		if now.Sub(last) < 200*time.Millisecond && progress.Stage == "sessions" {
			mu.Unlock()
			return
		}
		last = now
		mu.Unlock()
		s.notifyUI(MigrationProgressMsg{
			Stage: progress.Stage, Done: progress.Done, Total: progress.Total, Detail: progress.Detail,
		})
	}
}

// persistMigrationReport writes the report the user is about to see as a
// session event BEFORE displaying it, so quitting the moment it appears
// still leaves it replayable (shown implies recorded). The session row is
// ensured first: a conversation that has only ever run slash commands has
// no transcript row yet, and an event against a session id that owns no row
// is invisible to /resume — the report would be durable but unreachable.
func (s *ChatSession) persistMigrationReport(sessionID, source, title string, report *migrate.Report) {
	if s == nil || s.runSvc() == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	if store := s.sessStore(); store != nil {
		_ = store.Ensure(context.Background(), strings.TrimSpace(sessionID), strings.TrimSpace(sessionID))
	}
	raw, err := json.Marshal(event.MigrationCompletedPayload{
		Source: source, Title: title, Report: report.Text(),
	})
	if err != nil {
		return
	}
	_, _ = s.runSvc().AppendSessionEvent(context.Background(), state.SessionEvent{
		ID:        "migration-completed:" + strconv.FormatInt(time.Now().UnixNano(), 36),
		SessionID: strings.TrimSpace(sessionID),
		Type:      event.RunEventMigrationCompleted,
		Payload:   raw,
		CreatedAt: time.Now(),
	})
}

// StateRoot is the exported accessor for the active agent's per-agent state
// root, used by the TUI entrypoint (cmd/forebrain) to seed tui with the
// same workspace root the session resolves for plan-mode state.
func (s *ChatSession) StateRoot() string {
	return s.stateRoot()
}

func (s *ChatSession) skillLifecycleService() *skill.Service {
	if s == nil {
		svc := skill.NewService("")
		svc.OnRefresh = func() error { return turn.RefreshSkills(svc.Home, svc.Workspace(), safety.ProjectContext{}) }
		svc.IsBuiltin = turn.IsBuiltinName
		return svc
	}
	// The launch project is frozen with the session: project-scope reads,
	// writes and toggles all resolve against it, never against the process.
	launch := s.LaunchProject
	projectKey := ""
	if s.runner() != nil {
		projectKey = strings.TrimSpace(s.runner().ProjectKey)
	}
	if resolver, err := s.primaryAgentResolver(); err == nil {
		if active, err := resolver.Active(); err == nil && strings.TrimSpace(active.WorkspaceRoot) != "" {
			svc := skill.NewServiceForWorkspace(strings.TrimSpace(s.home()), strings.TrimSpace(active.WorkspaceRoot))
			svc.ProjectKey = projectKey
			svc.ProjectRoot = launch.Project.Root
			svc.OnRefresh = func() error { return turn.RefreshSkills(svc.Home, svc.Workspace(), launch) }
			svc.IsBuiltin = turn.IsBuiltinName
			return svc
		}
	}
	svc := skill.NewService(strings.TrimSpace(s.home()))
	svc.ProjectKey = projectKey
	svc.ProjectRoot = launch.Project.Root
	svc.OnRefresh = func() error { return turn.RefreshSkills(svc.Home, svc.Workspace(), launch) }
	svc.IsBuiltin = turn.IsBuiltinName
	return svc
}

func streamSubagentRosterRow(entry agent.HistoryEntry) AgentRosterRow {
	id := strings.TrimSpace(entry.AgentID)
	if id == "" {
		id = strings.TrimSpace(entry.TaskID)
	}
	if id == "" {
		id = strings.TrimSpace(entry.RunID)
	}
	label := strings.TrimSpace(entry.AgentType)
	if label == "" {
		label = strings.TrimSpace(entry.AgentKind)
	}
	if label == "" {
		label = id
	}
	status := string(entry.Status)
	if strings.TrimSpace(status) == "" {
		status = "running"
	}
	return AgentRosterRow{
		ID:             id,
		Kind:           "subagent",
		Label:          label,
		Status:         status,
		Title:          strings.TrimSpace(entry.Title),
		Task:           strings.TrimSpace(entry.Task),
		SessionID:      strings.TrimSpace(entry.WorkerSessionID),
		RunID:          strings.TrimSpace(entry.RunID),
		ElapsedSeconds: streamElapsedSeconds(entry.StartedAt, entry.UpdatedAt),
	}
}

func appendOrReplaceStreamRosterRow(rows []AgentRosterRow, seen map[string]int, row AgentRosterRow, replace bool) []AgentRosterRow {
	key := streamAgentRosterKey(row)
	if idx, ok := seen[key]; ok {
		if replace {
			rows[idx] = row
		}
		return rows
	}
	seen[key] = len(rows)
	return append(rows, row)
}

func streamAgentRosterKey(row AgentRosterRow) string {
	if strings.TrimSpace(row.Kind) == "subagent" {
		if runID := strings.TrimSpace(row.RunID); runID != "" {
			return "subagent:run:" + runID
		}
	}
	if id := strings.TrimSpace(row.ID); id != "" {
		return strings.TrimSpace(row.Kind) + ":" + id
	}
	return strings.TrimSpace(row.Kind) + ":" + strings.TrimSpace(row.SessionID)
}

func streamElapsedSeconds(started, updated int64) int {
	if started <= 0 {
		return 0
	}
	end := updated
	if end <= 0 {
		end = time.Now().Unix()
	}
	if end < started {
		return 0
	}
	return int(end - started)
}

func (s *ChatSession) HandleMCPSlash(sessionID, channel string) (string, bool) {
	_, _ = sessionID, channel
	if s.runner() == nil {
		return "mcp: unavailable", true
	}
	return turn.RenderMCPInventoryMarkdown(turn.BuildMCPInventory(s.mcpInventorySource())), true
}

func (s *ChatSession) HandleLSPSlash(sessionID, channel string) (string, bool) {
	_, _ = sessionID, channel
	ctl := s.lspControl()
	if ctl == nil {
		return "lsp: unavailable", true
	}
	return turn.RenderLSPInventoryMarkdown(ctl.Snapshot()), true
}

// startMCPLocalOAuth begins the local OAuth flow for one server: it listens
// for the browser callback and returns the authorization URL to open. done
// receives the outcome once the flow ends — the underlying error verbatim when
// it fails, otherwise what the new credential changes.
//
// The session is not reloaded. Its tool table is frozen before the first
// request, so a reload could at best rebuild clients for nothing and at worst
// move the prompt prefix mid-session. A server this session registered reads
// the stored token the next time it reconnects; a server that registered
// nothing without the credential gets its tools in a new session.
func (s *ChatSession) startMCPLocalOAuth(serverName string, done func(result string)) (string, error) {
	r := s.runner()
	if r == nil {
		return "", errors.New("mcp auth: unavailable")
	}
	var srv appcfg.MCPServerConfig
	for _, list := range [][]appcfg.MCPServerConfig{r.MCPServers, r.MCPDisabled} {
		for _, candidate := range list {
			if mcp.SameServerName(candidate.Name, serverName) {
				srv = mcp.MergeMCPServerOAuth(s.home(), candidate)
			}
		}
	}
	name := strings.TrimSpace(srv.Name)
	if name == "" {
		return "", fmt.Errorf("mcp auth: no server named %q in this session", strings.TrimSpace(serverName))
	}
	o := srv.OAuth
	if strings.TrimSpace(o.AuthorizationURL) == "" ||
		strings.TrimSpace(o.TokenURL) == "" ||
		strings.TrimSpace(o.ClientID) == "" {
		return "", errors.New("mcp auth: server oauth missing authorization_url token_url client_id")
	}
	verifier, err := mcp.PKCEVerifier()
	if err != nil {
		return "", fmt.Errorf("mcp auth: %w", err)
	}
	state, err := mcp.PKCEVerifier()
	if err != nil {
		return "", fmt.Errorf("mcp auth: %w", err)
	}
	challenge := mcp.PKCEChallengeS256(verifier)
	registered := len(turn.AttributeMCPTools(r.MCPServers, r.MCPStartup().PublishedTools())[name]) > 0
	type readyResult struct {
		authURL string
		err     error
	}
	ready := make(chan readyResult, 1)
	flowCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	go func() {
		defer cancel()
		var exchangeOAuth appcfg.MCPOAuthConfig
		readyPublished := false
		code, listenErr := mcp.ListenForOAuthCode(
			flowCtx,
			"/mcp/oauth/callback",
			state,
			func(callbackURL string) {
				exchangeOAuth = o
				exchangeOAuth.RedirectURL = callbackURL
				authURL, authErr := mcp.OAuthAuthorizeURL(exchangeOAuth, state, challenge)
				readyPublished = true
				ready <- readyResult{authURL: authURL, err: authErr}
			},
		)
		if listenErr != nil {
			if !readyPublished {
				ready <- readyResult{err: listenErr}
				return
			}
			done(name + ": authentication failed: " + listenErr.Error())
			return
		}
		tok, exchangeErr := mcp.ExchangeOAuthAuthCode(flowCtx, exchangeOAuth, code, verifier)
		if exchangeErr != nil {
			done(name + ": authentication failed: " + exchangeErr.Error())
			return
		}
		delta := appcfg.MCPOAuthConfig{
			AccessToken:  tok.AccessToken,
			RefreshToken: tok.RefreshToken,
			TokenType:    tok.TokenType,
		}
		if strings.TrimSpace(tok.RefreshToken) != "" {
			delta.Mode = "refresh"
		} else {
			delta.Mode = "static"
		}
		if err := mcp.SaveOAuthOverlayMerge(s.home(), mcp.OverlayProjectKey(srv), srv.Name, delta); err != nil {
			done(name + ": authentication failed: " + err.Error())
			return
		}
		if registered {
			done(name + ": authenticated · used on next reconnect")
			return
		}
		done(name + ": authenticated · tools available in a new session")
	}()
	res := <-ready
	if res.err != nil {
		cancel()
		return "", fmt.Errorf("mcp auth: %w", res.err)
	}
	return res.authURL, nil
}

func (s *ChatSession) HandleDiffSlash(sessionID, channel string, args []string) (string, bool) {
	_, _ = sessionID, channel
	if s == nil {
		return "diff: unavailable", true
	}
	// Unfenced: tui/commands.go parses the raw text with event.Parse.
	return turn.ExecuteDiffSlash(s.runner().ProjectRoot, args, false), true
}

// HandleSandboxSlash reports what the sandbox is doing. It does not change it:
// the sandbox is one half of a decision whose other half is when Forebrain Harness stops
// to ask, and /permissions is where that decision is made as a whole. Moving
// this half alone produced the state the presets exist to prevent — most
// visibly a full-access sandbox still asking about everything, which matches no
// preset and so could not even be named back to the user.
//
// There is one report and no arguments (the executor rejects them for this
// command). The diagnostics that used to need `doctor` are part of it: "what is
// the sandbox doing" and "why isn't it working" are the same question asked one
// step apart, and splitting them behind a subcommand only hid the answer.
func (s *ChatSession) HandleSandboxSlash(sessionID, channel string, args []string) (string, bool) {
	_, _, _ = sessionID, channel, args
	if s == nil {
		return "sandbox: unavailable", true
	}
	return safety.FormatSandboxReport(s.cfg(), s.sandbox()), true
}

func modelConfigFromChatSession(s *ChatSession) *appcfg.Root {
	if s == nil {
		return nil
	}
	if s.runner() != nil && s.runner().AppCfg != nil {
		return s.runner().AppCfg
	}
	return s.cfg()
}

func chatSessionActiveAgentName(s *ChatSession) string {
	if s != nil && s.runner() != nil && strings.TrimSpace(s.runner().AgentName) != "" {
		return strings.TrimSpace(s.runner().AgentName)
	}
	return "main"
}

// refreshSandboxRuntime re-derives the sandbox manager and the in-process file
// tools' policy from the live config. The environment owns both, so this is a
// one-line delegate; it must not be called while configApplyMu is held, since
// the environment takes reloadMu and reloadConfig takes those in the other
// order.
func (s *ChatSession) refreshSandboxRuntime() {
	if s == nil {
		return
	}
	s.Env.RefreshSandboxRuntime()
}

func (s *ChatSession) handlePermissionExplainSlash(sessionID string, args []string) string {
	if s == nil {
		return turn.ExecutePermissionExplain(nil, sessionID, args, turn.PermissionExplainUsage)
	}
	return turn.ExecutePermissionExplain(s.runner(), sessionID, args, turn.PermissionExplainUsage)
}

func permissionsUsage() string {
	// The terminal has the preset picker.
	return turn.PermissionsUsage(true)
}

// runnerMCPServers returns the session's frozen effective MCP list: global
// entries plus gated, consented project entries, resolved once at startup.
// Reading the config file's list instead would report servers this session
// never started.
// runnerMCPRuntime is the session's own view of the servers it started, in
// configuration order, or nil when there is no runner to ask.
//
// It is passed to the MCP formatting so /mcp and /status report this
// conversation's startup state rather than the process-wide mirror, which only
// knows what the last Runner to publish happened to see.
func runnerMCPRuntime(r *run.Runner) *turn.MCPRuntimeView {
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

func mcpConfigFromChatSession(s *ChatSession) *appcfg.Root {
	if s == nil {
		return nil
	}
	if s.runner() != nil && s.runner().AppCfg != nil {
		return s.runner().AppCfg
	}
	return s.cfg()
}

func (s *ChatSession) HandleMemoriesSlash(ctx context.Context, sessionID, channel string, args []string) (string, bool) {
	_ = ctx
	_ = sessionID
	_ = channel
	if len(args) > 0 {
		return "memories: usage /memories", true
	}
	return "Open memory settings to configure memory use and generation.", true
}

func (s *ChatSession) MemorySettings() (bool, bool, bool) {
	settings := memory.SettingsFromConfig(s.cfg())
	return settings.Enabled, settings.UseMemories, settings.GenerateMemories
}

func (s *ChatSession) UpdateMemorySettings(sessionID string, featureEnabled, useMemories, generateMemories *bool) error {
	if s == nil {
		return fmt.Errorf("memory settings unavailable")
	}
	path, err := home.ResolveConfigPath(strings.TrimSpace(s.home()))
	if err != nil {
		return err
	}
	if err := appcfg.PatchMemory(path, appcfg.MemoryPatch{FeatureEnabled: featureEnabled, UseMemories: useMemories, GenerateMemories: generateMemories}); err != nil {
		return err
	}
	if err := s.reloadConfigFromDisk(); err != nil {
		return err
	}
	if generateMemories != nil && s.memoryStore() != nil {
		mode := memory.ThreadMemoryDisabled
		if *generateMemories {
			mode = memory.ThreadMemoryEnabled
		}
		return s.memoryStore().SetThreadMemoryMode(context.Background(), sessionID, mode)
	}
	return nil
}

// ResetMemories clears memory: by default just the current session's project
// scope (the common ask — "forget what you learned about this repo"), or
// every scope of the active primary agent when all is true.
func (s *ChatSession) ResetMemories(ctx context.Context, all bool) error {
	if s == nil || s.memoryStore() == nil || s.memoryStore().DB == nil || s.runner() == nil {
		return fmt.Errorf("memory reset unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	roots, err := memory.ResolveRootsForAgent(strings.TrimSpace(s.runner().StateRoot()))
	if err != nil {
		return err
	}
	if all {
		// Scope the reset to the active primary agent: /memories reset must
		// clear this tenant's store, not every tenant's.
		if err := s.memoryStore().Reset(ctx); err != nil {
			return err
		}
		if err := memory.Clear(roots.Scope(memory.GlobalScope())); err != nil {
			return err
		}
		scopes, err := roots.ListProjectScopes()
		if err != nil {
			return err
		}
		for _, scope := range scopes {
			if err := memory.Clear(roots.Scope(scope)); err != nil {
				return err
			}
		}
		return nil
	}
	key := strings.TrimSpace(s.runner().ProjectKey)
	if key == "" {
		return fmt.Errorf("this session has no project to reset")
	}
	scope := memory.Scope{Kind: memory.ScopeProject, Key: key}
	if err := s.memoryStore().ResetScope(ctx, scope); err != nil {
		return err
	}
	return memory.Clear(roots.Scope(scope))
}

// MemorySkillOptions lists the skills the memory consolidation agent wrote into
// the memory folder. They are proposals: that folder is outside every root the
// skill loader scans, and the consolidation agent's file tools are confined to
// it, so nothing there can reach the model until a person promotes it here.
func (s *ChatSession) MemorySkillOptions() []MemorySkillOption {
	if s == nil || strings.TrimSpace(s.home()) == "" {
		return nil
	}
	items, err := s.skillLifecycleService().ListMemorySkills()
	if err != nil || len(items) == 0 {
		return nil
	}
	loadedRoots := s.loadedSkillRoots()
	out := make([]MemorySkillOption, 0, len(items))
	for _, item := range items {
		option := MemorySkillOption{
			Name:         item.Name,
			Description:  item.Description,
			Status:       string(item.Status),
			SlashCommand: item.SlashCommand,
			Shadows:      item.Shadows,
			Blocked:      item.Blocked,
			Promoted:     strings.TrimSpace(item.InstalledPath) != "",
		}
		if option.Promoted {
			_, option.LoadedInSession = loadedRoots[normalizeSkillRoot(item.InstalledPath)]
		}
		out = append(out, option)
	}
	return out
}

// PromoteMemorySkills copies the named proposals into the workspace skill root.
// Like every skill change it only writes to disk: the promoted skill becomes a
// registered tool when the next session builds its agent, and until then it is
// reachable by name with its slash command.
func (s *ChatSession) PromoteMemorySkills(names []string) (string, error) {
	if s == nil || strings.TrimSpace(s.home()) == "" {
		return "", fmt.Errorf("skills: unavailable")
	}
	promoted, err := s.skillLifecycleService().PromoteMemorySkills(names)
	if err != nil {
		return "", err
	}
	var msg strings.Builder
	fmt.Fprintf(&msg, "skills: promoted %d from memory", len(promoted))
	for _, item := range promoted {
		fmt.Fprintf(&msg, "\n- %s → %s", item.Name, item.Path)
	}
	commands := make([]string, 0, len(promoted))
	for _, item := range promoted {
		if strings.TrimSpace(item.SlashCommand) != "" {
			commands = append(commands, item.SlashCommand)
		}
	}
	msg.WriteString("\nnote: listed in the skills catalog of a new session")
	if len(commands) > 0 {
		msg.WriteString("; use " + strings.Join(commands, ", ") + " to load them in this one")
	}
	return msg.String(), nil
}

// loadedSkillRoots is the set of skill directories in this session's tool
// table. It is the ground truth for "active in this session", and it only
// changes when the agent is built, which is exactly the distinction the picker
// needs to draw.
func (s *ChatSession) loadedSkillRoots() map[string]struct{} {
	if s == nil || s.runner() == nil {
		return nil
	}
	tools := s.Env.Tools()
	if tools == nil {
		return nil
	}
	roots := tools.LoadedSkillRoots()
	out := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if key := normalizeSkillRoot(root); key != "" {
			out[key] = struct{}{}
		}
	}
	return out
}

func normalizeSkillRoot(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return strings.ToLower(filepath.Clean(path))
}

// skillWorkshopSkillName is the bundled meta-skill that owns everything
// agentic about a skill's life cycle: discovery, installation from the wider
// ecosystem, authoring, evaluation, and description tuning. It deliberately
// has no slash command of its own (SKILL.md sets `slash-command: false`) so
// /skills is the single entry point the user has to remember.
const skillWorkshopSkillName = "skill-workshop"

type skillsMenuAction int

const (
	skillsActionAdd skillsMenuAction = iota
	skillsActionCreate
	skillsActionImprove
	skillsActionToggle
	skillsActionImportMemory
	skillsActionOpenSkill
)

type skillsMenuRow struct {
	action skillsMenuAction
	entry  skill.Entry
}

// skillsOutcome is what a sub-flow reports back to the top-level menu loop:
// either it finished the command (leave, optionally submitting a turn) or it
// was cancelled and the user should land back on the menu they came from.
type skillsOutcome struct {
	submission ComposerSubmission
	submitted  bool
	done       bool
}

func skillsBack() skillsOutcome { return skillsOutcome{} }
func skillsDone() skillsOutcome { return skillsOutcome{done: true} }
func skillsSubmit(sub ComposerSubmission) skillsOutcome {
	return skillsOutcome{submission: sub, submitted: true, done: true}
}

// handleSkills renders the single /skills turn. It returns a submission
// when the chosen action is one the model carries out (running a skill, or
// handing work to the skill workshop); pure local state changes render a frame
// and return no submission.
func (c *commandController) handleSkills(ctx context.Context) (ComposerSubmission, bool) {
	if c == nil || c.session == nil || c.selector == nil {
		return ComposerSubmission{}, false
	}
	for {
		entries := c.session.AvailableSkillToggleOptions()
		memorySkills := c.session.MemorySkillOptions()
		items, rows := buildSkillsMenu(entries, len(memorySkills) > 0)
		idx, ok, err := c.selector.SelectRich("Skills\nRun an installed skill, or add, create, and tune skills", items, 0)
		if err != nil {
			c.renderer.PrintError(err)
			return ComposerSubmission{}, false
		}
		if !ok || idx < 0 || idx >= len(rows) {
			return ComposerSubmission{}, false
		}
		var outcome skillsOutcome
		switch row := rows[idx]; row.action {
		case skillsActionAdd:
			outcome = c.handleSkillAdd(ctx)
		case skillsActionCreate:
			outcome = c.handleSkillCreate()
		case skillsActionImprove:
			outcome = c.handleSkillImprove(entries)
		case skillsActionToggle:
			outcome = c.handleSkillToggle(entries)
		case skillsActionImportMemory:
			if c.handleMemorySkillImport(memorySkills) {
				outcome = skillsDone()
			}
		case skillsActionOpenSkill:
			outcome = c.handleSkillEntry(row.entry)
		}
		if outcome.done {
			return outcome.submission, outcome.submitted
		}
	}
}

// buildSkillsMenu lays out the whole command as one list: the things you can
// do to your skill set first, then every installed skill grouped by where it
// came from. Grouping and status live in the row itself so the user never has
// to open a submenu to learn what is installed or whether it is on.
func buildSkillsMenu(entries []skill.Entry, hasMemorySkills bool) ([]SelectItem, []skillsMenuRow) {
	items := []SelectItem{
		{Label: "Add a skill", Description: "Install from the catalog, a GitHub repo, or a local folder", Category: "Manage"},
		{Label: "Create a skill", Description: "Design and test a new skill with the skill workshop", Category: "Manage"},
		{Label: "Improve a skill", Description: "Edit, benchmark, and tune an installed skill", Category: "Manage"},
		{Label: "Enable / disable skills", Description: "Choose which skills load in new sessions", Category: "Manage"},
	}
	rows := []skillsMenuRow{
		{action: skillsActionAdd},
		{action: skillsActionCreate},
		{action: skillsActionImprove},
		{action: skillsActionToggle},
	}
	if hasMemorySkills {
		items = append(items, SelectItem{Label: importFromMemoryAction, Description: "Promote skills the memory agent drafted for this project", Category: "Manage"})
		rows = append(rows, skillsMenuRow{action: skillsActionImportMemory})
	}
	for _, entry := range sortSkillEntries(entries) {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}
		items = append(items, SelectItem{
			Label:       name,
			Description: skillEntryDescription(entry),
			Category:    skillEntryCategory(entry),
		})
		rows = append(rows, skillsMenuRow{action: skillsActionOpenSkill, entry: entry})
	}
	return items, rows
}

// sortSkillEntries orders skills by source and then by name so the same skill
// keeps the same place in the list between invocations. Discovery order is the
// filesystem walk order, which is stable enough to look deliberate and random
// enough to make a long list hard to scan.
func sortSkillEntries(entries []skill.Entry) []skill.Entry {
	out := make([]skill.Entry, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.Name) == "" {
			continue
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		li, lj := skillSourceRank(out[i].Source), skillSourceRank(out[j].Source)
		if li != lj {
			return li < lj
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func skillSourceRank(source string) int {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "project":
		return 0
	case "workspace":
		return 1
	case "global", "user":
		return 2
	case "system":
		return 4
	default:
		return 3
	}
}

// skillEntryCategory is the heading a skill is listed under, capitalized like
// the picker's other headings.
func skillEntryCategory(entry skill.Entry) string {
	source := strings.TrimSpace(entry.Source)
	if source == "" {
		source = "installed"
	}
	return strings.ToUpper(source[:1]) + source[1:] + " skills"
}

func skillEntryDescription(entry skill.Entry) string {
	parts := make([]string, 0, 4)
	if !entry.Enabled {
		parts = append(parts, "(off)")
	}
	if desc := strings.TrimSpace(entry.Description); desc != "" {
		parts = append(parts, desc)
	}
	switch {
	case len(entry.ShadowedBy) > 0:
		parts = append(parts, "· shadowed by a higher-priority copy")
	case len(entry.Shadows) > 0:
		parts = append(parts, "· shadows "+strconv.Itoa(len(entry.Shadows))+" other copy/copies")
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// handleSkillEntry is the per-skill sheet: run it, read what it is, flip it
// on or off, or send it to the workshop for editing — the four things a user
// wants after picking a skill out of the list.
func (c *commandController) handleSkillEntry(entry skill.Entry) skillsOutcome {
	name := strings.TrimSpace(entry.Name)
	toggleLabel := "Disable this skill"
	toggleDesc := "Stop loading it in new sessions"
	if !entry.Enabled {
		toggleLabel = "Enable this skill"
		toggleDesc = "Load it in new sessions"
	}
	items := []SelectItem{
		{Label: "Run it now", Description: "Load " + name + " into this turn and give it a task"},
		{Label: "Show details", Description: "Source, trust, path, and allowed tools"},
		{Label: toggleLabel, Description: toggleDesc},
		{Label: "Improve it", Description: "Hand it to the skill workshop to edit and evaluate"},
	}
	idx, ok, err := c.selector.SelectRich(name+"\n"+skillEntryDescription(entry), items, 0)
	if err != nil {
		c.renderer.PrintError(err)
		return skillsDone()
	}
	if !ok {
		return skillsBack()
	}
	switch idx {
	case 0:
		return c.runSkillNow(entry)
	case 1:
		msg, applyErr := c.session.ApplySkillSelection(name)
		if applyErr != nil {
			c.renderer.PrintError(applyErr)
			return skillsDone()
		}
		c.renderFrameForSkills(msg)
		return skillsDone()
	case 2:
		return c.toggleSingleSkill(entry)
	case 3:
		return c.improveSkill(entry)
	}
	return skillsBack()
}

func (c *commandController) runSkillNow(entry skill.Entry) skillsOutcome {
	name := strings.TrimSpace(entry.Name)
	request, ok, err := c.selector.Input("What should "+name+" do? (leave empty to just follow its instructions)", "")
	if err != nil {
		c.renderer.PrintError(err)
		return skillsDone()
	}
	if !ok {
		return skillsBack()
	}
	skillName, skillPath, selErr := c.session.SkillSelectionByName(name)
	if selErr != nil {
		c.renderer.PrintError(selErr)
		return skillsDone()
	}
	text := turn.SkillCommandInput(name, request)
	sub, built := buildComposerSubmission(text, "/skills "+name, nil)
	if !built {
		return skillsDone()
	}
	sub.RawInput = "/skills " + name
	sub.SkillName = skillName
	sub.SkillPath = skillPath
	return skillsSubmit(sub)
}

// toggleSingleSkill flips one skill without making the user walk the whole
// multi-select list, which is what they came for when they opened a specific
// skill. The enabled set is written as a whole because that is the shape the
// store keeps.
func (c *commandController) toggleSingleSkill(entry skill.Entry) skillsOutcome {
	target := strings.TrimSpace(entry.Path)
	enabledPaths := make([]string, 0, 8)
	for _, item := range c.session.AvailableSkillToggleOptions() {
		path := strings.TrimSpace(item.Path)
		if path == "" {
			continue
		}
		keep := item.Enabled
		if path == target {
			keep = !entry.Enabled
		}
		if keep {
			enabledPaths = append(enabledPaths, path)
		}
	}
	msg, err := c.session.ApplySkillEnabledSelection(enabledPaths)
	if err != nil {
		c.renderer.PrintError(err)
		return skillsDone()
	}
	c.renderFrameForSkills(msg)
	return skillsDone()
}

func (c *commandController) handleSkillToggle(entries []skill.Entry) skillsOutcome {
	if len(entries) == 0 {
		c.renderFrameForSkills("skills: no skills available")
		return skillsDone()
	}
	options := make([]string, 0, len(entries))
	defaults := make([]string, 0, len(entries))
	pathByLabel := make(map[string]string, len(entries))
	for _, item := range sortSkillEntries(entries) {
		label := strings.TrimSpace(item.Name)
		if desc := strings.TrimSpace(item.Description); desc != "" {
			label += " - " + desc
		}
		if src := strings.TrimSpace(item.Source); src != "" {
			label += " [" + src + "]"
		}
		if len(item.ShadowedBy) > 0 {
			label += " (shadowed)"
		} else if len(item.Shadows) > 0 {
			label += " (active, shadows " + strconv.Itoa(len(item.Shadows)) + ")"
		}
		options = append(options, label)
		pathByLabel[label] = strings.TrimSpace(item.Path)
		if item.Enabled {
			defaults = append(defaults, label)
		}
	}
	selected, ok, err := c.selector.MultiSelect("Enable / disable skills\nSpace toggles, enter saves. Applies to new sessions.", options, defaults)
	if err != nil {
		c.renderer.PrintError(err)
		return skillsDone()
	}
	if !ok {
		return skillsBack()
	}
	enabledPaths := make([]string, 0, len(selected))
	for _, label := range selected {
		if path := strings.TrimSpace(pathByLabel[strings.TrimSpace(label)]); path != "" {
			enabledPaths = append(enabledPaths, path)
		}
	}
	msg, applyErr := c.session.ApplySkillEnabledSelection(enabledPaths)
	if applyErr != nil {
		c.renderer.PrintError(applyErr)
		return skillsDone()
	}
	c.renderFrameForSkills(msg)
	return skillsDone()
}

// handleSkillAdd replaces the old "enter source reference" prompt. Two of the
// four routes need no typing at all, and the two that do say what a valid
// answer looks like — a user who has never installed a skill can still get one.
func (c *commandController) handleSkillAdd(ctx context.Context) skillsOutcome {
	items := []SelectItem{
		{Label: "Browse the catalog", Description: "Curated skills published at github.com/openai/skills"},
		{Label: "Search by what you need", Description: "Describe a task; the workshop finds and vets a skill for it"},
		{Label: "From a GitHub repo or URL", Description: "e.g. openai/skills or https://github.com/owner/repo/tree/main/skills/foo"},
		{Label: "From a local folder", Description: "A directory on this machine that contains SKILL.md"},
	}
	idx, ok, err := c.selector.SelectRich("Add a skill\nWhere should it come from?", items, 0)
	if err != nil {
		c.renderer.PrintError(err)
		return skillsDone()
	}
	if !ok {
		return skillsBack()
	}
	switch idx {
	case 0:
		return c.workshopHandoff(
			"List the skills available to install from the curated catalog, tell me what each one is for, and install the ones I pick.",
			"/skills browse catalog")
	case 1:
		need, inputOK, inputErr := c.selector.Input("What should the skill help with? (e.g. reviewing React pull requests)", "")
		if inputErr != nil {
			c.renderer.PrintError(inputErr)
			return skillsDone()
		}
		if !inputOK {
			return skillsBack()
		}
		if strings.TrimSpace(need) == "" {
			return skillsBack()
		}
		return c.workshopHandoff(
			"Find a skill for this: "+strings.TrimSpace(need)+"\n\nVerify quality before recommending anything, show me the options, and install the one I choose.",
			"/skills find "+strings.TrimSpace(need))
	case 2:
		return c.installFromSource(ctx, "GitHub repo or URL", "owner/repo or https://github.com/owner/repo/tree/main/skills/<name>")
	case 3:
		return c.installFromSource(ctx, "Local folder", "/path/to/skill-directory")
	}
	return skillsBack()
}

func (c *commandController) installFromSource(ctx context.Context, kind string, example string) skillsOutcome {
	sourceRef, ok, err := c.selector.Input(kind+" — e.g. "+example, "")
	if err != nil {
		c.renderer.PrintError(err)
		return skillsDone()
	}
	if !ok {
		return skillsBack()
	}
	if strings.TrimSpace(sourceRef) == "" {
		return skillsBack()
	}
	scopeItems := []SelectItem{
		{Label: "global", Description: "~/.forebrain/skills — available in every project"},
		{Label: "workspace", Description: "This workspace only"},
		{Label: "project", Description: ".forebrain/skills — checked in with this repo"},
	}
	scopeIdx, scopeOK, scopeErr := c.selector.SelectRich("Where should it be installed?", scopeItems, 0)
	if scopeErr != nil {
		c.renderer.PrintError(scopeErr)
		return skillsDone()
	}
	if !scopeOK {
		return skillsBack()
	}
	scope := scopeItems[scopeIdx].Label
	// The answers are in; nothing else needs the user. Hand the fetch to the
	// background and leave, so the composer comes straight back instead of
	// standing frozen behind a clone that owns the event loop.
	c.session.InstallSkillPackageAsync(ctx, strings.TrimSpace(sourceRef), scope)
	return skillsDone()
}

func (c *commandController) handleSkillCreate() skillsOutcome {
	intent, ok, err := c.selector.Input("What should the new skill do? (a sentence is enough)", "")
	if err != nil {
		c.renderer.PrintError(err)
		return skillsDone()
	}
	if !ok {
		return skillsBack()
	}
	intent = strings.TrimSpace(intent)
	request := "Create a new skill with me."
	if intent != "" {
		request += " Here is what it should do: " + intent
	}
	request += "\n\nInterview me where the intent is unclear, write the draft, and offer to test it."
	display := "/skills create"
	if intent != "" {
		display += " " + intent
	}
	return c.workshopHandoff(request, display)
}

func (c *commandController) handleSkillImprove(entries []skill.Entry) skillsOutcome {
	sorted := sortSkillEntries(entries)
	if len(sorted) == 0 {
		c.renderFrameForSkills("skills: no skills available")
		return skillsDone()
	}
	items := make([]SelectItem, 0, len(sorted))
	for _, entry := range sorted {
		items = append(items, SelectItem{
			Label:       strings.TrimSpace(entry.Name),
			Description: skillEntryDescription(entry),
			Category:    skillEntryCategory(entry),
		})
	}
	idx, ok, err := c.selector.SelectRich("Improve a skill\nWhich skill should the workshop work on?", items, 0)
	if err != nil {
		c.renderer.PrintError(err)
		return skillsDone()
	}
	if !ok || idx < 0 || idx >= len(sorted) {
		return skillsBack()
	}
	return c.improveSkill(sorted[idx])
}

func (c *commandController) improveSkill(entry skill.Entry) skillsOutcome {
	name := strings.TrimSpace(entry.Name)
	goal, ok, err := c.selector.Input("What should change about "+name+"? (leave empty to have it evaluated first)", "")
	if err != nil {
		c.renderer.PrintError(err)
		return skillsDone()
	}
	if !ok {
		return skillsBack()
	}
	goal = strings.TrimSpace(goal)
	request := "Improve the installed skill \"" + name + "\" at " + strings.TrimSpace(entry.Path) + "."
	if goal != "" {
		request += "\n\nWhat I want changed: " + goal
	} else {
		request += "\n\nStart by reading it and telling me what you would change, then test the change."
	}
	display := "/skills improve " + name
	return c.workshopHandoff(request, display)
}

// workshopHandoff loads the skill workshop into this turn and gives it the
// request. Loading happens through the activation payload rather than the tool
// table, which is frozen for the life of the session.
func (c *commandController) workshopHandoff(request string, display string) skillsOutcome {
	skillName, skillPath, err := c.session.SkillSelectionByName(skillWorkshopSkillName)
	if err != nil {
		c.renderer.PrintError(err)
		return skillsDone()
	}
	sub, built := buildComposerSubmission(request, display, nil)
	if !built {
		return skillsDone()
	}
	sub.RawInput = display
	sub.SkillName = skillName
	sub.SkillPath = skillPath
	return skillsSubmit(sub)
}

func (c *commandController) renderFrameForSkills(msg string) {
	if text := strings.TrimSpace(msg); text != "" {
		c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "skills", Content: text, Final: true})
	}
}

const importFromMemoryAction = "Import from Memory"

// handleMemorySkillImport is the human gate on model-authored skills. The
// memory consolidation agent writes them into the memory folder, which no skill
// root covers, so they stay invisible to the model until someone approves them
// here. Everything the decision needs is on the label — what the skill is, how
// it relates to a copy already promoted, which slash command it would add, what
// it would shadow — and blocked entries are shown with their reason rather than
// hidden, so a refusal is explained instead of looking like an omission.
//
// It reports whether the caller should leave the skills menu.
func (c *commandController) handleMemorySkillImport(items []MemorySkillOption) bool {
	if c == nil || c.session == nil || c.selector == nil {
		return true
	}
	if len(items) == 0 {
		c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "skills", Content: "skills: memory has no skills to import", Final: true})
		return true
	}
	options := make([]string, 0, len(items))
	nameByLabel := make(map[string]string, len(items))
	blocked := make([]string, 0)
	for _, item := range items {
		label := memorySkillLabel(item)
		if strings.TrimSpace(item.Blocked) != "" {
			blocked = append(blocked, label)
			continue
		}
		options = append(options, label)
		nameByLabel[label] = item.Name
	}
	if len(options) == 0 {
		content := "skills: no memory skill can be imported"
		if len(blocked) > 0 {
			content += "\n" + strings.Join(blocked, "\n")
		}
		c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "skills", Content: content, Final: true})
		return true
	}

	prompt := "Import from Memory\nSkills written by memory consolidation. Selected ones are copied into your workspace and become tools in a new session."
	if len(blocked) > 0 {
		prompt += "\n\nNot importable:\n" + strings.Join(blocked, "\n")
	}
	// Nothing is preselected: this is the approval step, so every import has to
	// be an explicit choice rather than a default the user forgot to clear.
	selected, ok, err := c.selector.MultiSelect(prompt, options, nil)
	if err != nil {
		c.renderer.PrintError(err)
		return true
	}
	if !ok || len(selected) == 0 {
		return true
	}
	names := make([]string, 0, len(selected))
	for _, label := range selected {
		if name := strings.TrimSpace(nameByLabel[strings.TrimSpace(label)]); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return true
	}
	sort.Strings(names)
	if overwriting := divergedAmong(items, names); len(overwriting) > 0 {
		confirmed, confirmOK, confirmErr := c.selector.Confirm(
			"Overwrite local edits to "+strings.Join(overwriting, ", ")+"?", false)
		if confirmErr != nil {
			c.renderer.PrintError(confirmErr)
			return true
		}
		if !confirmOK || !confirmed {
			return true
		}
	}
	msg, err := c.session.PromoteMemorySkills(names)
	if err != nil {
		c.renderer.PrintError(err)
		return true
	}
	if text := strings.TrimSpace(msg); text != "" {
		c.renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "skills", Content: text, Final: true})
	}
	return true
}

func memorySkillLabel(item MemorySkillOption) string {
	label := strings.TrimSpace(item.Name)
	if desc := strings.TrimSpace(item.Description); desc != "" {
		label += " - " + desc
	}
	if state := memorySkillStateLabel(item); state != "" {
		label += " [" + state + "]"
	}
	if reason := strings.TrimSpace(item.Blocked); reason != "" {
		return label + " (blocked: " + reason + ")"
	}
	var notes []string
	if cmd := strings.TrimSpace(item.SlashCommand); cmd != "" {
		notes = append(notes, "adds "+cmd)
	}
	if shadowed := strings.TrimSpace(item.Shadows); shadowed != "" {
		notes = append(notes, "shadows "+shadowed)
	}
	if len(notes) == 0 {
		return label
	}
	return label + " (" + strings.Join(notes, ", ") + ")"
}

// memorySkillStateLabel folds the disk status together with whether this
// session has the skill loaded. They answer different questions — is the copy
// current, and is it active right now — and a promoted skill is deliberately
// not active until the next session, so that has to read as a state of its own
// rather than looking like the promotion failed.
func memorySkillStateLabel(item MemorySkillOption) string {
	status := strings.TrimSpace(item.Status)
	if !item.Promoted {
		return status
	}
	if item.LoadedInSession {
		return status
	}
	if status == "" || status == "up-to-date" {
		return "imported, active next session"
	}
	return status + ", active next session"
}

func divergedAmong(items []MemorySkillOption, names []string) []string {
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		wanted[name] = struct{}{}
	}
	out := make([]string, 0)
	for _, item := range items {
		if _, ok := wanted[item.Name]; !ok {
			continue
		}
		if strings.TrimSpace(item.Status) == "diverged" {
			out = append(out, item.Name)
		}
	}
	return out
}
