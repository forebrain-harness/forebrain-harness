package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// AgentModel is the one answer to "which model does this agent run on", the
// way the runtime routes its calls: a dispatch-time override wins, then the
// type's own chain, then the conversation's model — with the reasoning effort
// that same configuration gives the model it names.
func TestAgentModelResolvesEachKindOfAgent(t *testing.T) {
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{
		Definitions: map[string]appcfg.AgentDefinition{
			"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{
				{
					Provider: "zhipuai", Model: "glm-5.3", APIKey: "key", BaseURL: "https://example.invalid",
					Params: appcfg.LLMRequestParams(`{"reasoning":{"effort":"xhigh"}}`),
				},
				{
					Provider: "zhipuai", Model: "glm-5.3-flash", APIKey: "key", BaseURL: "https://example.invalid",
					Params: appcfg.LLMRequestParams(`{"reasoning":{"effort":"high"}}`),
				},
			}},
			"explore": {LLMProviders: []appcfg.AgentLLMProviderConfig{
				{
					Provider: "deepseek", Model: "deepseek-v4-flash", APIKey: "key", BaseURL: "https://example.invalid",
					Params: appcfg.LLMRequestParams(`{"reasoning":{"effort":"medium"}}`),
				},
			}},
			"general-purpose": {},
		},
	}}
	r := &Runner{Deps: &Deps{AppCfg: cfg}}

	provider, model, effort := AgentModel(r, "session-1", nil)
	if provider != "zhipuai" || model != "glm-5.3" || effort != "xhigh" {
		t.Fatalf("primary = %s/%s · %s, want the conversation's model and its effort", provider, model, effort)
	}

	// A fork and a typed subagent without a chain of their own run on the
	// conversation's model, exactly as the runtime routes them.
	for _, entry := range []*agent.HistoryEntry{
		{AgentKind: "fork"},
		{AgentKind: "typed", AgentType: "general-purpose"},
	} {
		provider, model, effort = AgentModel(r, "session-1", entry)
		if provider != "zhipuai" || model != "glm-5.3" || effort != "xhigh" {
			t.Fatalf("%s = %s/%s · %s, want the conversation's model", entry.AgentKind, provider, model, effort)
		}
	}

	// A typed subagent whose definition has llm_providers runs on the first
	// of them, with the effort that entry configures.
	provider, model, effort = AgentModel(r, "session-1", &agent.HistoryEntry{AgentKind: "typed", AgentType: "explore"})
	if provider != "deepseek" || model != "deepseek-v4-flash" || effort != "medium" {
		t.Fatalf("typed with its own chain = %s/%s · %s", provider, model, effort)
	}

	// A dispatch-time override outranks both, and its effort comes from the
	// provider entry that model resolves to — the same entry
	// ConfiguredModelClient builds the client from.
	provider, model, effort = AgentModel(r, "session-1", &agent.HistoryEntry{
		AgentKind: "typed", AgentType: "plan-reviewer",
		ModelProvider: "zhipuai", Model: "glm-5.3-flash",
	})
	if provider != "zhipuai" || model != "glm-5.3-flash" || effort != "high" {
		t.Fatalf("override = %s/%s · %s, want the picked model and its own effort", provider, model, effort)
	}
}

// TestNewLLMForAgentTypeNilConfig verifies graceful degradation: a nil config
// yields no client and no error, so callers can treat the feature as disabled.
func TestNewLLMForAgentTypeNilConfig(t *testing.T) {
	c, err := NewLLMForAgentType(nil, "goal-evaluator")
	if err != nil {
		t.Fatalf("nil config should not error, got %v", err)
	}
	if c != nil {
		t.Fatalf("nil config should yield nil client, got %v", c)
	}
}

// TestResolvedAgentProvidersFallsBackToMain verifies that an absent or
// provider-less named definition falls back to the main agent's providers, so
// auxiliary callers work out of the box when no dedicated definition is configured.
func TestResolvedAgentProvidersFallsBackToMain(t *testing.T) {
	mainProv := appcfg.AgentLLMProviderConfig{Provider: "openai", Model: "gpt-4o"}
	cfg := &appcfg.Root{
		Agents: appcfg.AgentsSection{
			Definitions: map[string]appcfg.AgentDefinition{
				"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{mainProv}},
			},
		},
	}
	got := resolvedAgentProviders(cfg, "goal-evaluator")
	if len(got) != 1 || got[0].Provider != mainProv.Provider || got[0].Model != mainProv.Model {
		t.Fatalf("expected fallback to main providers, got %+v", got)
	}
}

// TestResolvedAgentProvidersPrefersDedicated verifies that a dedicated
// definition takes precedence over main, so users can route the extractor to a
// cheaper model.
func TestResolvedAgentProvidersPrefersDedicated(t *testing.T) {
	mainProv := appcfg.AgentLLMProviderConfig{Provider: "openai", Model: "gpt-4o"}
	dedProv := appcfg.AgentLLMProviderConfig{Provider: "openai", Model: "gpt-4o-mini"}
	cfg := &appcfg.Root{
		Agents: appcfg.AgentsSection{
			Definitions: map[string]appcfg.AgentDefinition{
				"main":           {LLMProviders: []appcfg.AgentLLMProviderConfig{mainProv}},
				"goal-evaluator": {LLMProviders: []appcfg.AgentLLMProviderConfig{dedProv}},
			},
		},
	}
	got := resolvedAgentProviders(cfg, "goal-evaluator")
	if len(got) != 1 || got[0].Provider != dedProv.Provider || got[0].Model != dedProv.Model {
		t.Fatalf("expected dedicated providers, got %+v", got)
	}
}

func TestOpenAIPromptCachingOnlyTargetsOpenAI(t *testing.T) {
	tests := map[string]bool{
		"anthropic":  false,
		"deepseek":   false,
		"zhipuai":    false,
		"moonshotai": false,
		"openai":     true,
		"alibaba":    false,
	}
	for provider, want := range tests {
		if got := openAIPromptCaching(provider); got != want {
			t.Errorf("openAIPromptCaching(%q) = %t, want %t", provider, got, want)
		}
	}
}

func TestNewLLMFromYAMLRequiresExplicitMainFields(t *testing.T) {
	for _, k := range []string{
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		"OPENROUTER_API_KEY",
		"ANTHROPIC_API_KEY",
		"DEEPSEEK_API_KEY",
		"DEEPSEEK_BASE_URL",
		"GOOGLE_API_KEY",
	} {
		t.Setenv(k, "")
	}
	for _, tc := range []struct {
		name string
		cfg  *LLMProviderYAML
		want string
	}{
		{
			name: "missing provider",
			cfg: &LLMProviderYAML{
				Model:  "gpt-5",
				APIKey: "k", BaseURL: "https://api.example/v1",
			},
			want: "llm provider is required",
		},
		{
			name: "missing model",
			cfg: &LLMProviderYAML{
				Provider: "openai",
				APIKey:   "k", BaseURL: "https://api.example/v1",
			},
			want: "llm model is required",
		},
		{
			name: "missing api key",
			cfg: &LLMProviderYAML{
				Provider: "openai",
				Model:    "gpt-5",
				BaseURL:  "https://api.example/v1",
			},
			want: "llm api_key is required",
		},
		{
			name: "missing base url",
			cfg: &LLMProviderYAML{
				Provider: "openai",
				Model:    "gpt-5",
				APIKey:   "k",
			},
			want: "llm base_url is required",
		},
		{
			name: "zhipuai accepted as openai-compatible provider",
			cfg: &LLMProviderYAML{
				Provider: "zhipuai",
				Model:    "glm-5.1",
				APIKey:   "k",
				BaseURL:  "https://api.z.ai/api/paas/v4",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewLLMFromYAML(tc.cfg)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("NewLLMFromYAML error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewLLMFromYAML error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestRunnerLoadDoesNotRegisterEnableThinkingTool(t *testing.T) {
	cfg := &appcfg.Root{
		Agents: appcfg.AgentsSection{
			Definitions: map[string]appcfg.AgentDefinition{
				"main": {
					LLMProviders: []appcfg.AgentLLMProviderConfig{{
						Provider: "openai",
						Model:    "qwen-test",
						APIKey:   "test-key",
						BaseURL:  "http://127.0.0.1:9/v1",
						Params:   appcfg.LLMRequestParams(`{"enable_thinking":true}`),
					}},
				},
			},
		},
	}
	r := &Runner{Deps: &Deps{Home: t.TempDir(), AppCfg: cfg}}

	if err := r.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := string(r.MainYAMLConfig().LLMChain[0].Params); got != `{"enable_thinking":true}` {
		t.Fatalf("provider params = %q", got)
	}
	if strings.Contains(r.MainAgentDescription(), "enable_thinking") {
		t.Fatalf("agent prompt still mentions enable_thinking: %q", r.MainAgentDescription())
	}
	for _, tool := range r.LoadedTools() {
		if tool != nil && tool.Name() == "enable_thinking" {
			t.Fatalf("enable_thinking tool should not be registered")
		}
	}
}

func TestRunnerLoadDiscoversSkillsWithoutRegisteringThemAsTools(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatalf("create project marker: %v", err)
	}
	trustedProject, err := safety.Resolve(project)
	if err != nil {
		t.Fatalf("resolve project: %v", err)
	}
	if err := safety.MarkTrusted(home, trustedProject); err != nil {
		t.Fatalf("mark trusted project: %v", err)
	}
	// The launch project is declared, not inherited from the process: the
	// runner reads its project skills from here and never from the working
	// directory. No chdir, deliberately — a runtime that needs one is the bug
	// this froze away.
	launch, err := safety.ResolveProjectContext(home, project)
	if err != nil {
		t.Fatalf("resolve launch project: %v", err)
	}
	if err := skill.Install(home); err != nil {
		t.Fatalf("install system skills: %v", err)
	}
	writeRunnerSkillFile(t, filepath.Join(project, ".forebrain", "skills", "project-review", "SKILL.md"), `name: project-review
description: Project review mode`, "Project body")
	writeRunnerSkillFile(t, filepath.Join(home, "workspace", "skills", "caveman-commit", "SKILL.md"), `name: caveman-commit
description: Commit message mode`, "Hot body")
	writeRunnerSkillFile(t, filepath.Join(home, "skills", "shared-deck", "SKILL.md"), `name: shared-deck
description: Deck helper`, "Cold body")
	cfg := &appcfg.Root{
		Agents: appcfg.AgentsSection{
			Definitions: map[string]appcfg.AgentDefinition{
				"main": {
					LLMProviders: []appcfg.AgentLLMProviderConfig{{
						Provider: "openai",
						Model:    "qwen-test",
						APIKey:   "test-key",
						BaseURL:  "http://127.0.0.1:9/v1",
					}},
				},
			},
		},
	}
	r := &Runner{Deps: &Deps{Home: home, AppCfg: cfg, LaunchProject: launch, ProjectRoot: project}}

	if err := r.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	// No individual skill is a tool. They are described in the prompt catalog
	// and loaded through the one fixed skill tool, which is what keeps the tool
	// array — rendered ahead of the whole cached prefix — the same shape no
	// matter which skills are installed or which of them a turn uses.
	loaded := map[string]bool{}
	for _, tl := range r.LoadedTools() {
		if tl != nil {
			loaded[tl.Name()] = true
		}
	}
	for _, name := range []string{"project-review", "caveman-commit", "shared-deck"} {
		if loaded[name] {
			t.Fatalf("skill %q appeared in the tool table; skills belong to the prompt catalog", name)
		}
	}
	loadedSkills := map[string]tool.LoadedSkill{}
	for _, item := range r.Tools().LoadedSkills() {
		loadedSkills[item.Name] = item
	}
	systemSkillRoot, err := filepath.EvalSymlinks(filepath.Join(home, "skills", ".system"))
	if err != nil {
		t.Fatalf("resolve system skill root: %v", err)
	}
	// The fourth skill source is the installed system catalog. Every skill it
	// contributed is checked rather than one of them: the embedded assets
	// change, so naming one here would test nothing the day it is dropped, and
	// picking one from a map would pick a different one on every run.
	var systemSkills []string
	for name, item := range loadedSkills {
		if strings.HasPrefix(strings.TrimSpace(item.RootDir), systemSkillRoot+string(filepath.Separator)) {
			systemSkills = append(systemSkills, name)
		}
	}
	if len(systemSkills) == 0 {
		t.Fatalf("no system skill loaded from %s: %+v", systemSkillRoot, loadedSkills)
	}
	sort.Strings(systemSkills)
	for _, name := range systemSkills {
		if loaded[name] {
			t.Fatalf("system skill %q appeared in the tool table; skills belong to the prompt catalog", name)
		}
	}
	// The only skill-category tool metadata is the loader itself.
	skillMetas := []string{}
	for _, meta := range r.Tools().ToolMetas() {
		if strings.EqualFold(strings.TrimSpace(meta.Category), "skill") {
			skillMetas = append(skillMetas, strings.TrimSpace(meta.Name))
		}
	}
	// And the tool table is byte-identical to one built with no skills at all.
	//
	// That is the property the cached prefix depends on — installing, renaming
	// or toggling a skill may move the catalog section and nothing that renders
	// ahead of the system prompt — and it is asserted directly rather than by
	// searching descriptions for skill names. A name search cannot state this
	// property: it passes a tool that leaks a skill under another spelling, and
	// fails a tool whose wording happens to contain a skill's name, which is
	// what a system skill called "plan" did to enter_plan_mode.
	got, want := renderedToolTable(t, r), toolTableWithoutSkills(t)
	// A comparison of two empty tables would pass while asserting nothing.
	if len(got) == 0 {
		t.Fatal("no tool table was rendered, so the comparison below would be vacuous")
	}
	if got != want {
		t.Fatalf("installed skills changed the tool table, which the whole cached prefix sits behind:\n with skills: %s\n without:     %s", got, want)
	}
	if len(skillMetas) != 1 || skillMetas[0] != "skill" {
		t.Fatalf("skill-category tool metadata = %v, want exactly the skill loader", skillMetas)
	}
	// Every discovered skill is in the runtime catalog, which is what grants
	// read access to its directory. There is no always/deferred split any more:
	// that existed only to decide which skills entered the tool array.
	for _, name := range append([]string{"project-review", "caveman-commit", "shared-deck"}, systemSkills...) {
		if strings.TrimSpace(loadedSkills[name].RootDir) == "" {
			t.Fatalf("discovered skill %s missing from runtime catalog: %+v", name, loadedSkills)
		}
	}
	projectSkillRoot, err := filepath.EvalSymlinks(filepath.Join(project, ".forebrain", "skills", "project-review"))
	if err != nil {
		t.Fatalf("resolve project skill root: %v", err)
	}
	if got, want := loadedSkills["project-review"].RootDir, projectSkillRoot; got != want {
		t.Fatalf("project skill root=%q want %q", got, want)
	}
	workspaceSkillRoot, err := filepath.EvalSymlinks(filepath.Join(home, "workspace", "skills", "caveman-commit"))
	if err != nil {
		t.Fatalf("resolve workspace skill root: %v", err)
	}
	if got, want := loadedSkills["caveman-commit"].RootDir, workspaceSkillRoot; got != want {
		t.Fatalf("workspace skill root=%q want %q", got, want)
	}
	sharedSkillRoot, err := filepath.EvalSymlinks(filepath.Join(home, "skills", "shared-deck"))
	if err != nil {
		t.Fatalf("resolve shared skill root: %v", err)
	}
	if got, want := loadedSkills["shared-deck"].RootDir, sharedSkillRoot; got != want {
		t.Fatalf("shared skill root=%q want %q", got, want)
	}
}

func TestNewLLMForAgentConfigUsesPrimaryProviderOnly(t *testing.T) {
	firstCalls := 0
	secondCalls := 0

	firstSrv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		firstCalls++
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusUnauthorized)
		_, _ = rw.Write([]byte(`{"error":{"message":"primary provider unauthorized"}}`))
	}))
	defer firstSrv.Close()

	secondSrv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		secondCalls++
		rw.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": 1,
			"model":   "gpt-5.5",
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "unexpected fallback success",
				},
				"finish_reason": "stop",
			}},
		}
		if err := json.NewEncoder(rw).Encode(resp); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer secondSrv.Close()

	cfg := &AgentConfigYAML{
		LLMChain: []*LLMProviderYAML{
			{
				Provider: "openai",
				Model:    "primary",
				APIKey:   "test-key",
				BaseURL:  firstSrv.URL + "/v1",
			},
			{
				Provider: "openai",
				Model:    "secondary",
				APIKey:   "test-key",
				BaseURL:  secondSrv.URL + "/v1",
			},
		},
	}

	client, err := NewLLMForAgentConfig(cfg)
	if err != nil {
		t.Fatalf("NewLLMForAgentConfig error = %v", err)
	}
	if _, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err == nil || !strings.Contains(err.Error(), "primary provider unauthorized") {
		t.Fatalf("Execute error = %v, want primary provider error", err)
	}
	if firstCalls != 1 {
		t.Fatalf("primary calls = %d, want 1", firstCalls)
	}
	if secondCalls != 0 {
		t.Fatalf("secondary calls = %d, want 0", secondCalls)
	}
}

func writeRunnerSkillFile(t *testing.T, path, frontmatter, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	content := "---\n" + strings.TrimSpace(frontmatter) + "\n---\n" + strings.TrimSpace(body) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}

// renderedToolTable is the tool array as the model receives it: every tool's
// name, description and schema, in order. It is what the prompt cache's prefix
// begins with, so two runtimes that render it identically are two runtimes a
// cached prefix survives between.
func renderedToolTable(t *testing.T, r *Runner) string {
	t.Helper()
	var sb strings.Builder
	for _, meta := range r.Tools().ToolMetas() {
		fmt.Fprintf(&sb, "\n%s\n%s\n%s\n", meta.Name, meta.Description, meta.InputSchema)
	}
	return sb.String()
}

// toolTableWithoutSkills builds the same runtime in a home where no skill is
// installed, so the caller can compare against a runtime where four are.
func toolTableWithoutSkills(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatalf("create bare project: %v", err)
	}
	trusted, err := safety.Resolve(project)
	if err != nil {
		t.Fatalf("resolve bare project: %v", err)
	}
	if err := safety.MarkTrusted(home, trusted); err != nil {
		t.Fatalf("trust bare project: %v", err)
	}
	bare := &Runner{Deps: &Deps{Home: home, AppCfg: &appcfg.Root{
		Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
			"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{{
				Provider: "openai", Model: "qwen-test", APIKey: "test-key", BaseURL: "http://127.0.0.1:9/v1",
			}}},
		}},
	}}}
	if err := bare.Load(); err != nil {
		t.Fatalf("load skill-free runtime: %v", err)
	}
	return renderedToolTable(t, bare)
}

// lspLateStub is the CodeIntelligence port with only the late-diagnostics
// half live: PeekLate answers from fields, AckLate records what the wrapper
// acknowledged. The rest of the port stays inert.
type lspLateStub struct {
	text  string
	token uint64
	peeks int
	acks  []struct {
		sid   string
		token uint64
	}
}

func (s *lspLateStub) Handles(absPath string) bool { return false }

func (s *lspLateStub) Query(ctx context.Context, q tool.CodeIntelQuery) (tool.CodeIntelResult, error) {
	return tool.CodeIntelResult{}, nil
}

func (s *lspLateStub) DidWrite(ctx context.Context, agentSessionID string, changes []tool.FileChange) tool.DiagnosticsDelta {
	return tool.DiagnosticsDelta{}
}

func (s *lspLateStub) DidRead(ctx context.Context, absPath string, content []byte) {}

func (s *lspLateStub) DidRunShell(ctx context.Context) {}

func (s *lspLateStub) PeekLate(agentSessionID string) (string, uint64) {
	s.peeks++
	return s.text, s.token
}

func (s *lspLateStub) AckLate(agentSessionID string, token uint64) {
	s.acks = append(s.acks, struct {
		sid   string
		token uint64
	}{agentSessionID, token})
}

// lspRecordingLLM captures what the wrapper handed the inner client, and can
// be scripted to fail so the caller's acknowledgement path is observable.
type lspRecordingLLM struct {
	err     error
	gotMsgs []llm.Message
	calls   int
}

func (m *lspRecordingLLM) Execute(_ context.Context, msgs []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	m.calls++
	m.gotMsgs = append([]llm.Message(nil), msgs...)
	if m.err != nil {
		return nil, m.err
	}
	return &llm.Result{Message: &llm.Message{Role: llm.RoleAssistant}}, nil
}

func TestLSPReminderAppendedAndAcked(t *testing.T) {
	late := "Language server diagnostics changed for files you edited earlier in this session:"
	stub := &lspLateStub{text: late, token: 7}
	inner := &lspRecordingLLM{}
	w := wrapLSPDiagnosticsReminderLLM(inner, stub)
	sink := &reminderAdoptionSink{}
	ctx := withReminderAdoptionSink(llm.WithAgentSessionID(context.Background(), "sess-late"), sink)
	base := []llm.Message{llm.UserMessage(llm.Text("continue"))}
	if _, err := w.Execute(ctx, base, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls=%d want 1", inner.calls)
	}
	if len(inner.gotMsgs) != 2 {
		t.Fatalf("messages=%d want 2 (request plus reminder)", len(inner.gotMsgs))
	}
	last := inner.gotMsgs[len(inner.gotMsgs)-1]
	if last.Role != llm.RoleUser || !last.IsMeta {
		t.Fatalf("reminder must be an IsMeta user message, got %+v", last)
	}
	if got := llm.TextContent(last.Parts...); got != "<system-reminder>\n"+late+"\n</system-reminder>" {
		t.Fatalf("reminder body=%q", got)
	}
	// The incoming request is appended to, never mutated in place.
	if len(base) != 1 {
		t.Fatalf("caller's slice mutated: %d", len(base))
	}
	// The reminder is published to the orchestration loop at the request's end.
	adopted := sink.take()
	if len(adopted) != 1 || adopted[0].insertAt != 1 {
		t.Fatalf("reminder adoptions=%+v want one at the end of the request", adopted)
	}
	// Delivered once: the token is acknowledged only after the call succeeded.
	if len(stub.acks) != 1 || stub.acks[0].sid != "sess-late" || stub.acks[0].token != 7 {
		t.Fatalf("acks=%+v want AckLate(sess-late, 7)", stub.acks)
	}
}

func TestLSPReminderNotAckedOnError(t *testing.T) {
	stub := &lspLateStub{text: "late text", token: 3}
	inner := &lspRecordingLLM{err: errors.New("provider down")}
	w := wrapLSPDiagnosticsReminderLLM(inner, stub)
	ctx := llm.WithAgentSessionID(context.Background(), "sess-late-err")
	if _, err := w.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("continue"))}, nil); err == nil {
		t.Fatal("inner error must propagate")
	}
	if stub.peeks != 1 {
		t.Fatalf("PeekLate calls=%d want 1", stub.peeks)
	}
	if len(stub.acks) != 0 {
		t.Fatalf("a failed call must not acknowledge, acks=%+v", stub.acks)
	}
}

func TestLSPReminderNoTextNoMessage(t *testing.T) {
	stub := &lspLateStub{token: 5}
	inner := &lspRecordingLLM{}
	w := wrapLSPDiagnosticsReminderLLM(inner, stub)
	ctx := llm.WithAgentSessionID(context.Background(), "sess-late-empty")
	base := []llm.Message{llm.UserMessage(llm.Text("continue"))}
	if _, err := w.Execute(ctx, base, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(inner.gotMsgs) != 1 {
		t.Fatalf("messages=%d want the request unchanged", len(inner.gotMsgs))
	}
	if len(stub.acks) != 0 {
		t.Fatalf("nothing delivered, nothing acknowledged, acks=%+v", stub.acks)
	}
}

func TestLSPReminderNeedsSession(t *testing.T) {
	stub := &lspLateStub{text: "late text", token: 9}
	inner := &lspRecordingLLM{}
	w := wrapLSPDiagnosticsReminderLLM(inner, stub)
	base := []llm.Message{llm.UserMessage(llm.Text("continue"))}
	if _, err := w.Execute(context.Background(), base, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if stub.peeks != 0 {
		t.Fatalf("PeekLate calls=%d want 0 without an agent session", stub.peeks)
	}
	if len(inner.gotMsgs) != 1 {
		t.Fatalf("messages=%d want the request unchanged", len(inner.gotMsgs))
	}
}

// TestAgentModelAndRoutingAgree pins that the context-based resolution a
// compaction reads and the record-based resolution surfaces draw answer the
// same question: for every kind of agent, building the context the way that
// agent really executes and asking agentModelFor must name the same provider
// and model AgentModel resolves from its history entry. The two drifting is
// how a compaction ends up sized by a model the run never talks to.
func TestAgentModelAndRoutingAgree(t *testing.T) {
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{
		Definitions: map[string]appcfg.AgentDefinition{
			"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{
				{Provider: "zhipuai", Model: "glm-5.3", APIKey: "key", BaseURL: "https://example.invalid"},
			}},
			"explore": {LLMProviders: []appcfg.AgentLLMProviderConfig{
				{Provider: "deepseek", Model: "deepseek-v4-flash", APIKey: "key", BaseURL: "https://example.invalid"},
			}},
			"general-purpose": {},
		},
	}}
	r := &Runner{Deps: &Deps{AppCfg: cfg}}

	cases := []struct {
		name string
		ctx  context.Context
		item *agent.HistoryEntry
	}{
		{
			name: "primary agent on the main thread",
			ctx:  WithQuerySource(context.Background(), "repl_main_thread"),
		},
		{
			name: "fork",
			ctx:  WithQuerySource(context.Background(), "agent:builtin:fork"),
			item: &agent.HistoryEntry{AgentKind: "fork"},
		},
		{
			name: "typed without a chain of its own",
			ctx:  tool.WithSubagentType(WithQuerySource(context.Background(), "agent:custom"), "general-purpose"),
			item: &agent.HistoryEntry{AgentKind: "typed", AgentType: "general-purpose"},
		},
		{
			name: "typed with its own chain",
			ctx:  tool.WithSubagentType(WithQuerySource(context.Background(), "agent:builtin:explore"), "explore"),
			item: &agent.HistoryEntry{AgentKind: "typed", AgentType: "explore"},
		},
		{
			name: "dispatch-time override",
			ctx: tool.WithSubagentType(
				WithSubagentModelOverride(
					WithQuerySource(context.Background(), "agent:custom"),
					SubagentModelOverride{Provider: "zhipuai", Model: "glm-5.3-flash"},
				), "plan-reviewer"),
			item: &agent.HistoryEntry{AgentKind: "typed", AgentType: "plan-reviewer", ModelProvider: "zhipuai", Model: "glm-5.3-flash"},
		},
	}
	for _, tc := range cases {
		ctxProvider, ctxModel := r.agentModelFor(tc.ctx)
		recProvider, recModel, _ := AgentModel(r, "session-1", tc.item)
		if ctxProvider != recProvider || ctxModel != recModel {
			t.Fatalf("%s: agentModelFor=%s/%s disagrees with AgentModel=%s/%s",
				tc.name, ctxProvider, ctxModel, recProvider, recModel)
		}
	}
}

// TestContextBudgetUsesEachAgentsOwnModel pins the per-agent half of the
// context gauge: the budget an agent's view shows must be sized by the window
// of the model that agent runs on — the same resolution AgentModel performs —
// never by the primary model's. A subagent on a smaller model would otherwise
// read "75%" of a window it does not have.
func TestContextBudgetUsesEachAgentsOwnModel(t *testing.T) {
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {Primary: true, LLMProviders: []appcfg.AgentLLMProviderConfig{
			{Provider: "zhipuai", Model: "glm-5.3", APIKey: "key", BaseURL: "https://example.invalid"},
		}},
		"explore": {LLMProviders: []appcfg.AgentLLMProviderConfig{
			{Provider: "deepseek", Model: "deepseek-v3.2", APIKey: "key", BaseURL: "https://example.invalid"},
		}},
		"general-purpose": {},
	}}}
	r := &Runner{Deps: &Deps{AppCfg: cfg}}

	cases := []struct {
		name       string
		subagent   *agent.HistoryEntry
		usage      int
		wantAgent  string
		wantWindow int
		wantLeft   int
	}{
		{
			name:       "primary agent",
			usage:      450000,
			wantWindow: 1000000,
			wantLeft:   50,
		},
		{
			name:       "fork on the conversation's model",
			subagent:   &agent.HistoryEntry{TaskID: "task-fork", AgentKind: "fork"},
			usage:      450000,
			wantAgent:  "task-fork",
			wantWindow: 1000000,
			wantLeft:   50,
		},
		{
			name:       "typed subagent without a chain of its own",
			subagent:   &agent.HistoryEntry{TaskID: "task-general", AgentKind: "typed", AgentType: "general-purpose"},
			usage:      450000,
			wantAgent:  "task-general",
			wantWindow: 1000000,
			wantLeft:   50,
		},
		{
			name:       "typed subagent on its own smaller window",
			subagent:   &agent.HistoryEntry{TaskID: "task-explore", AgentKind: "typed", AgentType: "explore"},
			usage:      28800,
			wantAgent:  "task-explore",
			wantWindow: 128000,
			wantLeft:   75,
		},
		{
			name: "plan reviewer on the model the user picked",
			subagent: &agent.HistoryEntry{
				TaskID: "task-review", AgentKind: "typed", AgentType: PlanReviewSubagentType,
				ModelProvider: "zhipuai", Model: "glm-4.5",
			},
			usage:      29491,
			wantAgent:  "task-review",
			wantWindow: 131072,
			wantLeft:   75,
		},
	}
	for _, tc := range cases {
		got, ok := ContextBudget(r, "conversation-1", tc.subagent, tc.usage)
		if !ok {
			t.Fatalf("%s: ContextBudget returned false", tc.name)
		}
		if got.AgentID != tc.wantAgent {
			t.Errorf("%s: AgentID = %q, want %q", tc.name, got.AgentID, tc.wantAgent)
		}
		if got.ContextWindow != tc.wantWindow {
			t.Errorf("%s: ContextWindow = %d, want %d", tc.name, got.ContextWindow, tc.wantWindow)
		}
		if got.EffectiveWindow != tc.wantWindow {
			t.Errorf("%s: EffectiveWindow = %d, want %d", tc.name, got.EffectiveWindow, tc.wantWindow)
		}
		if got.PercentLeft != tc.wantLeft {
			t.Errorf("%s: PercentLeft = %d, want %d", tc.name, got.PercentLeft, tc.wantLeft)
		}
		if got.TokenUsage != tc.usage {
			t.Errorf("%s: TokenUsage = %d, want %d", tc.name, got.TokenUsage, tc.usage)
		}
	}

	// Usage 0 is a fresh context: the whole window, still per agent.
	fresh, ok := ContextBudget(r, "conversation-1", nil, 0)
	if !ok || fresh.TokenUsage != 0 || fresh.PercentLeft != 100 || fresh.ContextWindow != 1000000 {
		t.Fatalf("fresh primary context = %+v, want the whole 1M window at 100%%", fresh)
	}
	smallFresh, ok := ContextBudget(r, "conversation-1", &agent.HistoryEntry{TaskID: "task-explore", AgentKind: "typed", AgentType: "explore"}, 0)
	if !ok || smallFresh.TokenUsage != 0 || smallFresh.PercentLeft != 100 || smallFresh.ContextWindow != 128000 {
		t.Fatalf("fresh explore context = %+v, want the whole 128k window at 100%%", smallFresh)
	}

	// The configured auto-compact limit is part of the budget, for the
	// subagent's window as much as the primary's.
	limited := &appcfg.Root{Agents: cfg.Agents, Compact: appcfg.CompactSection{ModelAutoCompactTokenLimit: 50000}}
	rl := &Runner{Deps: &Deps{AppCfg: limited}}
	primary, ok := ContextBudget(rl, "conversation-1", nil, 450000)
	if !ok || primary.AutoCompactThreshold != 50000 || primary.PercentLeft != 0 {
		t.Fatalf("primary budget past the configured limit = %+v, want threshold 50000 and 0%% left", primary)
	}
	explore, ok := ContextBudget(rl, "conversation-1", &agent.HistoryEntry{TaskID: "task-explore", AgentKind: "typed", AgentType: "explore"}, 28800)
	if !ok || explore.AutoCompactThreshold != 50000 || explore.PercentLeft != 42 {
		t.Fatalf("explore budget under the configured limit = %+v, want threshold 50000 and 42%% left", explore)
	}
}
