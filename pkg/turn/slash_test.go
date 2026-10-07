package turn

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/stretchr/testify/require"
)

func TestListSlashCommandsFiltersByPrefix(t *testing.T) {
	records := ListSlashCommands(SurfaceWebChat, "pl", DiscoveryOptions{})
	require.Len(t, records, 1)
	require.Equal(t, "plan", records[0].Name)
	require.Equal(t, "plan", records[0].CanonicalName)
	require.Equal(t, "agent", records[0].Category)
	require.Equal(t, "inject-prompt", records[0].ActionKind)
	require.NotEmpty(t, records[0].ArgumentHint)
	require.NotEmpty(t, records[0].AllowedModes)
}

func TestListSlashCommandsSideConversationFiltersToSafeCommands(t *testing.T) {
	records := ListSlashCommands(SurfaceWebChat, "", DiscoveryOptions{SideConversation: true})
	require.NotEmpty(t, records)
	names := make(map[string]bool, len(records))
	for _, r := range records {
		names[r.Name] = true
	}
	require.True(t, names["diff"])
	require.True(t, names["status"])
	require.False(t, names["plan"])
	require.False(t, names["new"])
}

func TestExecuteSlashCommandDelegatesToUnifiedExecutor(t *testing.T) {
	res := ExecuteSlashCommand(Context{
		Home:      t.TempDir(),
		SessionID: "s1",
		Surface:   SurfaceWebChat,
	}, "/approvals")

	require.True(t, res.Handled)
	require.Contains(t, res.Reply, "There is no /approvals")
}

func TestServiceSlashFacadeDelegatesToUnifiedRegistryAndExecutor(t *testing.T) {
	svc := New(nil)

	records := svc.ListSlashCommands(SurfaceWebChat, "pl", DiscoveryOptions{})
	require.Len(t, records, 1)
	require.Equal(t, "plan", records[0].Name)

	res := svc.ExecuteSlashCommand(Context{
		Home:      t.TempDir(),
		SessionID: "s1",
		Surface:   SurfaceWebChat,
	}, "/approvals")
	require.True(t, res.Handled)
	require.Contains(t, res.Reply, "There is no /approvals")
}

func TestCommandServiceUsesCanonicalExecutor(t *testing.T) {
	service := CommandService{}
	res := service.Execute(Context{Surface: SurfaceWebChat}, "/unknown")
	require.True(t, res.Handled)
	require.Contains(t, res.Reply, "There is no /unknown")
}

// recordingSlashHandlers implements every shared slash handler interface and
// records what it was asked to do. It stands in for a surface, so the same
// instance can answer both a terminal and a web-chat Context.
type recordingSlashHandlers struct {
	calls     []string
	selection ModelSelectionState
	selectErr error
	// settings, when set, answers ModelSettings in place of the built-in
	// fixture config (tests that drive the file-writing half install it).
	settings func(sessionID string) (ModelSettings, error)
}

func (r *recordingSlashHandlers) record(name, sessionID string, args []string) (string, bool) {
	r.calls = append(r.calls, name+"("+sessionID+","+strings.Join(args, " ")+")")
	return name + ": ok", true
}

func (r *recordingSlashHandlers) HandleCompactSlash(_ context.Context, sessionID, _ string, args []string) (string, bool) {
	return r.record("compact", sessionID, args)
}
func (r *recordingSlashHandlers) HandleClearSlash(_ context.Context, sessionID, _ string, args []string) (string, bool) {
	return r.record("clear", sessionID, args)
}
func (r *recordingSlashHandlers) HandleContextSlash(_ context.Context, sessionID, _ string, args []string) (string, bool) {
	return r.record("context", sessionID, args)
}
func (r *recordingSlashHandlers) HandleMemoriesSlash(_ context.Context, sessionID, _ string, args []string) (string, bool) {
	return r.record("memories", sessionID, args)
}
func (r *recordingSlashHandlers) HandleStatusSlash(sessionID, _ string, side bool) (string, bool) {
	if side {
		return r.record("status", sessionID, []string{"side"})
	}
	return r.record("status", sessionID, nil)
}
func (r *recordingSlashHandlers) HandlePermissionsSlash(sessionID, _ string, args []string) (string, bool) {
	return r.record("permissions", sessionID, args)
}
func (r *recordingSlashHandlers) HandleMCPSlash(sessionID, _ string) (string, bool) {
	return r.record("mcp", sessionID, nil)
}
func (r *recordingSlashHandlers) HandleSandboxSlash(sessionID, _ string, args []string) (string, bool) {
	return r.record("sandbox", sessionID, args)
}
func (r *recordingSlashHandlers) HandleDiffSlash(sessionID, _ string, args []string) (string, bool) {
	return r.record("diff", sessionID, args)
}
func (r *recordingSlashHandlers) ModelSettings(sessionID string) (ModelSettings, error) {
	r.record("model", sessionID, nil)
	if r.settings != nil {
		return r.settings(sessionID)
	}
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "gpt-5"}, {Provider: "deepseek", Model: "deepseek-chat"}}},
	}}}
	return ModelSettings{Config: cfg, AgentName: "main", Selection: r.selection}, nil
}
func (r *recordingSlashHandlers) SelectModel(_ context.Context, sessionID string, choice ModelChoice, effort *string) error {
	r.record("select", sessionID, []string{choice.Provider + "/" + choice.Model})
	if effort != nil {
		r.record("select-effort", sessionID, []string{*effort})
	}
	return r.selectErr
}
func (r *recordingSlashHandlers) ReloadModelCatalog(_ context.Context, sessionID string) error {
	r.record("reload-catalog", sessionID, nil)
	return nil
}
func (r *recordingSlashHandlers) PrimaryAgents() ([]PrimaryAgent, error) {
	r.record("agents", "", nil)
	return []PrimaryAgent{{ID: "main", WorkspaceRoot: "/w/main", Active: true}, {ID: "ops", WorkspaceRoot: "/w/ops"}}, nil
}
func (r *recordingSlashHandlers) SwitchPrimaryAgent(_ context.Context, id string) (PrimaryAgent, error) {
	r.record("switch", id, nil)
	return PrimaryAgent{ID: id, WorkspaceRoot: "/w/" + id, Active: true}, nil
}
func (r *recordingSlashHandlers) HandleFastSlash(sessionID, _ string, args []string) (string, bool) {
	return r.record("fast", sessionID, args)
}

func contextForSurface(h *recordingSlashHandlers, surface Surface, channel, home string) Context {
	return Context{
		CommandContext: context.Background(),
		Home:           home,
		StateRoot:      home,
		SessionID:      "sid-1",
		Channel:        channel,
		Surface:        surface,
		FastAvailable:  true,
		Compact:        h,
		Clear:          h,
		ContextDebug:   h,
		Status:         h,
		Permissions:    h,
		MCP:            h,
		Sandbox:        h,
		Diff:           h,
		Model:          h,
		Agent:          h,
		Fast:           h,
		Memories:       h,
	}
}

// TestSharedSlashCommandsDispatchIdenticallyOnBothSurfaces is P4-11's contract:
// the eleven commands both surfaces offer must reach the same handler with the
// same arguments and produce the same canonical Result, whichever surface asked.
//
// It compares the canonical fields, not the reply text -- a surface is allowed
// to render differently (the web chat fences a diff, the terminal does not),
// but it is not allowed to route, parse, or decide differently. Any surface
// special-case added inside the dispatcher fails this.
func TestSharedSlashCommandsDispatchIdenticallyOnBothSurfaces(t *testing.T) {
	home := t.TempDir()
	inputs := []string{
		"/compact too long",
		"/clear",
		"/context",
		"/status",
		"/permissions explain Bash",
		"/mcp",
		"/sandbox",
		"/diff HEAD~1",
		"/model",
		"/agent",
		"/fast",
		"/memories",
	}
	for _, input := range inputs {
		t.Run(strings.TrimPrefix(strings.Fields(input)[0], "/"), func(t *testing.T) {
			term := &recordingSlashHandlers{}
			web := &recordingSlashHandlers{}
			termRes := Execute(contextForSurface(term, SurfaceTUI, "tui", home), input)
			webRes := Execute(contextForSurface(web, SurfaceWebChat, "webchat", home), input)

			if !termRes.Handled || !webRes.Handled {
				t.Fatalf("%q handled: terminal=%v web=%v; both surfaces offer this command",
					input, termRes.Handled, webRes.Handled)
			}
			if !slices.Equal(term.calls, web.calls) {
				t.Fatalf("%q dispatched differently: terminal=%v web=%v", input, term.calls, web.calls)
			}
			if termRes.ShouldContinueRun != webRes.ShouldContinueRun ||
				termRes.ContinueInput != webRes.ContinueInput ||
				termRes.SessionChanged != webRes.SessionChanged ||
				termRes.ModeChanged != webRes.ModeChanged ||
				termRes.Mode != webRes.Mode ||
				termRes.ForcePlan != webRes.ForcePlan ||
				!reflect.DeepEqual(termRes.Picker, webRes.Picker) {
				t.Fatalf("%q canonical outcome differs:\n terminal=%+v\n web=%+v", input, termRes, webRes)
			}
		})
	}
}

// baseArgTestContext is a Context with no handlers wired: every command that
// needs a surface service answers "unavailable", never with a usage error.
// That is exactly the seam the consistency contract needs — whether a parser
// refuses an argument is independent of whether a handler is present.
func baseArgTestContext(t *testing.T) Context {
	return Context{
		CommandContext: context.Background(),
		Surface:        SurfaceTUI,
		Channel:        "tui",
		SessionID:      "s1",
		// Mode and plan writes must land in a temp root, never in the source
		// tree: the bare-form probe below exercises /plan, which switches
		// mode on entry.
		StateRoot: t.TempDir(),
	}
}

// usageReply reports whether a reply is a usage refusal: the one outcome the
// hint-and-parser contract forbids for a hinted example.
func usageReply(name, reply string) bool {
	return strings.Contains(reply, "usage") ||
		strings.Contains(reply, "takes no arguments") ||
		strings.Contains(reply, "does not accept inline arguments") ||
		strings.HasPrefix(reply, "/"+name)
}

// TestArgumentHintsMatchParsers is the single source of truth guarding
// R-args: the hint a user sees and the arguments a parser accepts can never
// drift apart, because both sides are derived from this table.
func TestArgumentHintsMatchParsers(t *testing.T) {
	for _, cmd := range commands {
		switch {
		case cmd.SupportsInlineArgs && strings.TrimSpace(cmd.ArgumentHint) == "":
			t.Errorf("/%s accepts inline args but shows no hint", cmd.Name)
		case !cmd.SupportsInlineArgs && strings.TrimSpace(cmd.ArgumentHint) != "":
			t.Errorf("/%s shows a hint %q but refuses inline args", cmd.Name, cmd.ArgumentHint)
		}
	}
}

// TestNoArgCommandsRefuseAnyArgument drives every no-argument command with an
// argument: the reply must be the inline-args refusal, never a partial
// execution or a different usage line.
func TestNoArgCommandsRefuseAnyArgument(t *testing.T) {
	for _, cmd := range commands {
		if cmd.SupportsInlineArgs {
			continue
		}
		res := Execute(baseArgTestContext(t), "/"+cmd.Name+" an-arg")
		if !res.Handled && !res.ShouldContinueRun {
			t.Errorf("/%s an-arg: not handled", cmd.Name)
			continue
		}
		if !strings.Contains(res.Reply, "does not accept inline arguments") {
			t.Errorf("/%s an-arg = %q, want the inline-args refusal", cmd.Name, res.Reply)
		}
		// The bare form must not be a usage error on its own.
		bare := Execute(baseArgTestContext(t), "/"+cmd.Name)
		if !bare.Handled && !bare.ShouldContinueRun {
			// Inject-prompt commands (init, plan) legitimately answer with
			// ShouldContinueRun instead of Handled.
			t.Errorf("/%s: not handled", cmd.Name)
		}
		if strings.HasPrefix(strings.ToLower(bare.Reply), "usage") || strings.Contains(bare.Reply, ": usage") {
			t.Errorf("/%s bare = %q, unexpectedly a usage error", cmd.Name, bare.Reply)
		}
	}
}

// TestHintedCommandsAcceptTheirOwnHint exercises each hinted command with a
// representative argument drawn from its own hint: the reply must never be a
// usage refusal. Unavailable handlers answer "unavailable", which is honest —
// the parser accepted the shape.
func TestHintedCommandsAcceptTheirOwnHint(t *testing.T) {
	examples := map[string]string{
		"permissions": "/permissions explain read_file",
		"rename":      "/rename a fresh title",
		"plan":        "/plan ship the parser rewrite",
		"diff":        "/diff main.go",
		"goal":        "/goal finish the parser rewrite",
	}
	for _, cmd := range commands {
		if !cmd.SupportsInlineArgs {
			continue
		}
		example, ok := examples[cmd.Name]
		if !ok {
			t.Errorf("/%s accepts inline args but has no hint example in the consistency test — add one", cmd.Name)
			continue
		}
		res := Execute(baseArgTestContext(t), example)
		if !res.Handled && !res.ShouldContinueRun {
			t.Errorf("%s: not handled", example)
			continue
		}
		if usageReply(cmd.Name, res.Reply) {
			t.Errorf("%s = %q, the parser refused its own hint's example", example, res.Reply)
		}
	}
}

// /diff answers with the project's diff, or one sentence when there is none:
// never a diff-shaped message, never a fenced sentence.
func TestExecuteDiffSlashSaysWhyThereIsNoDiff(t *testing.T) {
	notRepo := t.TempDir()
	if got := ExecuteDiffSlash(notRepo, nil, true); got != notRepo+" is not a git repository, so there is no diff to show." {
		t.Fatalf("outside a repository = %q", got)
	}
	repo := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if got := ExecuteDiffSlash(repo, nil, true); got != "No uncommitted changes." {
		t.Fatalf("clean repository = %q", got)
	}
	if got := ExecuteDiffSlash(repo, []string{"docs"}, false); got != "No uncommitted changes in docs." {
		t.Fatalf("clean path = %q", got)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ExecuteDiffSlash(repo, nil, true); !strings.HasPrefix(got, "```diff\ndiff --git") || !strings.Contains(got, "+hello") {
		t.Fatalf("fenced diff = %q", got)
	}
}

// /help lists the built-in commands, then the skills, on every surface; a
// command a surface does not have is answered with the closest ones it does.
func TestHelpCatalogAndUnknownCommandSuggestions(t *testing.T) {
	ResetDynamic()
	t.Cleanup(ResetDynamic)
	skill := DynamicCommand{
		Command: Command{Name: "context-save", Description: "save the session context", Category: SkillCategory, AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, Visibility: VisibilityPublic},
		Handler: func(Context, string, []string) Result { return Result{Handled: true} },
	}
	require.NoError(t, ReplaceDynamicSource("test", []DynamicCommand{skill}))

	for _, surface := range []Surface{SurfaceWebChat, SurfaceTUI} {
		res := Execute(Context{Home: t.TempDir(), SessionID: "s1", Surface: surface}, "/help")
		require.True(t, res.Handled)
		require.True(t, strings.HasPrefix(res.Reply, "Commands\n/"), res.Reply)
		require.Contains(t, res.Reply, "\n\nSkills\n/context-save — save the session context")
		require.Less(t, strings.Index(res.Reply, "/model —"), strings.Index(res.Reply, "Skills"))
	}
	require.Equal(t, "There is no /modle; did you mean /model?", UnknownCommandReply(SurfaceWebChat, "modle", DiscoveryOptions{}))
	require.Equal(t, "There is no /zzzq; type / to see every command.", UnknownCommandReply(SurfaceWebChat, "zzzq", DiscoveryOptions{}))
}

// /model offers the agent's configured models in their configured order —
// a model array expanded into one choice each — the first one in force.
func TestModelChoicesAreTheAgentsConfiguredModels(t *testing.T) {
	codex := "gpt-5.3-co" + "dex"
	cfg := appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{
			{Provider: "zhipuai", Model: "glm-5.1"},
			{Provider: "openai", Model: codex},
			{Provider: "openai", Model: "gpt-5.4", Models: appcfg.StringList{"gpt-5.4", "gpt-5.5"}},
		}},
		"writer": {LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "anthropic", Model: "claude-sonnet-4-5"}}},
	}}}
	var got []string
	for _, choice := range ModelChoices(&cfg, "main") {
		got = append(got, choice.Provider+"/"+choice.Model)
	}
	require.Equal(t, []string{"zhipuai/glm-5.1", "openai/" + codex, "openai/gpt-5.4", "openai/gpt-5.5"}, got)
	writer := ModelChoices(&cfg, "writer")
	require.Len(t, writer, 1)
	require.Equal(t, "claude-sonnet-4-5", writer[0].Model)
	require.Equal(t, ModelChoices(&cfg, "main"), ModelChoices(&cfg, "no-such-agent"), "an unknown agent runs on main's models")
}

// /lsp is a tools-category panel command on both surfaces, next to /mcp.
func TestLSPCommandTables(t *testing.T) {
	cmd, ok := Find("lsp")
	require.True(t, ok, "/lsp must be a builtin command")
	require.Equal(t, []Surface{SurfaceWebChat, SurfaceTUI}, cmd.AllowedSurfaces)
	require.Equal(t, "tools", cmd.Category)
	require.Equal(t, "open-panel", cmd.ActionKind)
	for _, surface := range []Surface{SurfaceWebChat, SurfaceTUI} {
		names := make([]string, 0)
		for _, c := range VisibleWithOptions(surface, DiscoveryOptions{}) {
			names = append(names, c.Name)
		}
		require.Contains(t, names, "lsp", "surface %s: %v", surface, names)
	}
}

// Every built-in command declares how it behaves in a subagent's own view
// (decision D4). A new command that forgets is caught here, before it silently
// runs against the wrong conversation in a subagent's view.
func TestEveryBuiltinCommandHasASubagentViewScope(t *testing.T) {
	want := map[string]SubagentViewScope{
		// Acts on the subagent whose view it is typed in.
		"compact": SubagentViewActs,
		"context": SubagentViewActs,
		// Runs exactly as it does in the conversation's view.
		"help":        SubagentViewGlobal,
		"status":      SubagentViewGlobal,
		"mcp":         SubagentViewGlobal,
		"lsp":         SubagentViewGlobal,
		"permissions": SubagentViewGlobal,
		"sandbox":     SubagentViewGlobal,
		"exit":        SubagentViewGlobal,
		"diff":        SubagentViewGlobal,
		"subagents":   SubagentViewGlobal,
		"skills":      SubagentViewGlobal,
		"connect":     SubagentViewGlobal,
		"memories":    SubagentViewGlobal,
		"migrate":     SubagentViewGlobal,
		// Hidden, and answered with one sentence pointing back to the conversation.
		"new":    SubagentViewHidden,
		"resume": SubagentViewHidden,
		"fork":   SubagentViewHidden,
		"rename": SubagentViewHidden,
		"init":   SubagentViewHidden,
		"clear":  SubagentViewHidden,
		"plan":   SubagentViewHidden,
		"agent":  SubagentViewHidden,
		"model":  SubagentViewHidden,
		"fast":   SubagentViewHidden,
		"goal":   SubagentViewHidden,
	}
	for _, cmd := range commands {
		got, ok := want[cmd.Name]
		if !ok {
			t.Fatalf("builtin /%s has no expected subagent-view scope in this test", cmd.Name)
		}
		if cmd.SubagentView == SubagentViewUnset {
			t.Fatalf("builtin /%s must declare a subagent-view scope", cmd.Name)
		}
		if cmd.SubagentView != got {
			t.Fatalf("/%s subagent-view scope = %q, want %q", cmd.Name, cmd.SubagentView, got)
		}
		delete(want, cmd.Name)
	}
	if len(want) > 0 {
		t.Fatalf("commands in the test table are missing from the registry: %v", want)
	}
}

// A subagent's own view lists every command that runs there, and hides the ones
// that would change the conversation (D4).
func TestSubagentViewHidesConversationCommands(t *testing.T) {
	listed := map[string]bool{}
	for _, cmd := range VisibleWithOptions(SurfaceTUI, DiscoveryOptions{SubagentView: true}) {
		listed[cmd.Name] = true
	}
	for _, hidden := range []string{"new", "resume", "fork", "rename", "init", "clear", "plan", "agent", "model", "fast", "goal"} {
		if listed[hidden] {
			t.Fatalf("the subagent view must hide /%s", hidden)
		}
	}
	for _, shown := range []string{"compact", "context", "help", "status", "mcp", "lsp", "permissions", "sandbox", "exit", "diff", "subagents", "skills", "connect", "memories", "migrate"} {
		if !listed[shown] {
			t.Fatalf("the subagent view must keep /%s", shown)
		}
	}
}
