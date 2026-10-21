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
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
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

// A conversation's model is one answer, and it is the conversation's own: the
// row a surface wrote, resolved against the live config the way turn entry
// resolves it. The runner's published snapshot answers only for a conversation
// that has no row — or a row the config no longer offers, the same ordinary
// reload that rewrites it — because a surface reading the snapshot instead
// names a model the conversation's requests never go to.
func TestPrimaryModelStateForSessionPrefersTheConversationsRow(t *testing.T) {
	runner, store := newSessionRoutingRunner(t, nil)
	ctx := context.Background()
	for _, sid := range []string{"conv-pinned", "conv-bare", "conv-gone"} {
		if err := store.Ensure(ctx, sid, "conversation"); err != nil {
			t.Fatalf("Ensure %q: %v", sid, err)
		}
	}
	snapshot := PrimaryModelSelection(runner)
	if !snapshot.Set || snapshot.Provider != "openai" || snapshot.Model != "gpt-a" {
		t.Fatalf("the runner's published selection = %+v, want the first configured entry", snapshot)
	}
	save := func(sid, provider, model, effort string) {
		t.Helper()
		if _, err := store.SaveSessionModelSelection(ctx, sid, state.SessionModelSelection{
			Provider: provider, Model: model, Effort: effort,
		}); err != nil {
			t.Fatalf("SaveSessionModelSelection %q: %v", sid, err)
		}
	}

	// The row wins, effort included: what the requests carry is what the
	// surface draws.
	save("conv-pinned", "openai", "gpt-b", "high")
	if got := PrimaryModelStateForSession(runner, store, "conv-pinned"); got.Provider != "openai" || got.Model != "gpt-b" || got.Effort != "high" || !got.Set {
		t.Fatalf("state for a pinned conversation = %+v, want openai/gpt-b effort high", got)
	}
	if provider, model := PrimaryModelForSession(runner, store, "conv-pinned"); provider != "openai" || model != "gpt-b" {
		t.Fatalf("name for a pinned conversation = %q/%q, want openai/gpt-b", provider, model)
	}

	// An empty effort in the row means the requests carry none — not "the
	// model's configured effort", which the wire would have to add.
	save("conv-bare", "openai", "gpt-b", "")
	if got := PrimaryModelStateForSession(runner, store, "conv-bare"); got.Model != "gpt-b" || got.Effort != "" {
		t.Fatalf("state for a conversation pinned without effort = %+v, want openai/gpt-b and no effort", got)
	}

	// A row the config no longer offers falls back to the published selection,
	// as the wire does; so does a conversation that never pinned one, and an
	// empty id.
	save("conv-gone", "openai", "gpt-gone", "")
	for _, sid := range []string{"conv-gone", "conv-never-pinned", ""} {
		if got := PrimaryModelStateForSession(runner, store, sid); got != snapshot {
			t.Fatalf("state for %q = %+v, want the published selection %+v", sid, got, snapshot)
		}
	}
}

// openCodeCall is one request a client built by NewLLMFromYAML made to the
// fake OpenCode gateway.
type openCodeCall struct {
	Path   string
	Auth   string
	Agent  string
	Header http.Header
}

// openCodeGateway stands in for https://opencode.ai/inference. It records what
// the client sent and answers with an empty event stream, which is enough for
// every client to return: these tests assert on the request, not the answer.
func openCodeGateway(t *testing.T) (*httptest.Server, func() []openCodeCall) {
	t.Helper()
	var (
		mu    sync.Mutex
		calls []openCodeCall
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, openCodeCall{
			Path:   r.URL.Path,
			Auth:   r.Header.Get("Authorization"),
			Agent:  r.Header.Get("User-Agent"),
			Header: r.Header.Clone(),
		})
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []openCodeCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]openCodeCall(nil), calls...)
	}
}

// runOpenCodeClient builds the client and makes one call, which the gateway
// records. Execute is expected to fail against the empty stream; the request it
// produced is the point.
func runOpenCodeClient(t *testing.T, cfg *LLMProviderYAML) {
	t.Helper()
	client, err := NewLLMFromYAML(cfg)
	if err != nil {
		t.Fatalf("NewLLMFromYAML(%s/%s) error = %v", cfg.Provider, cfg.Model, err)
	}
	if client == nil {
		t.Fatalf("NewLLMFromYAML(%s/%s) returned no client", cfg.Provider, cfg.Model)
	}
	if _, err := client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
		t.Logf("Execute(%s/%s) error (expected on an empty stream): %v", cfg.Provider, cfg.Model, err)
	}
}

// TestNewLLMFromYAMLOpenCodeRoutesByProtocol pins the base-URL contract: the
// model decides the protocol, the protocol decides the client, and each client
// appends its own path suffix to the base URL it is given. The two models that
// flip between surfaces (qwen3.8-max, minimax-m3) are the reason the provider
// is part of the decision.
func TestNewLLMFromYAMLOpenCodeRoutesByProtocol(t *testing.T) {
	t.Setenv("OPENCODE_API_KEY", "")
	for _, tc := range []struct {
		name     string
		provider string
		model    string
		apiKey   string
		wantPath string
	}{
		{name: "free console model speaks chat completions", provider: "opencode", model: "big-pickle", wantPath: "/openai/v1/chat/completions"},
		{name: "gpt family speaks responses", provider: "opencode", model: "gpt-5.6-luna", apiKey: "sk-test", wantPath: "/openai/v1/responses"},
		{name: "grok family speaks responses", provider: "opencode", model: "grok-4.7", apiKey: "sk-test", wantPath: "/openai/v1/responses"},
		{name: "claude family speaks anthropic", provider: "opencode", model: "claude-sonnet-5", apiKey: "sk-test", wantPath: "/anthropic/v1/messages"},
		{name: "gemini family speaks google", provider: "opencode", model: "gemini-4-flash", apiKey: "sk-test", wantPath: "/google/v1beta/models/gemini-4-flash:generateContent"},
		{name: "qwen3.8-max is chat completions on console", provider: "opencode", model: "qwen3.8-max", apiKey: "sk-test", wantPath: "/openai/v1/chat/completions"},
		{name: "qwen3.8-max is anthropic on the go subscription", provider: "opencode-go", model: "qwen3.8-max", apiKey: "sk-test", wantPath: "/go/anthropic/v1/messages"},
		{name: "minimax-m3 is chat completions on console", provider: "opencode", model: "minimax-m3", apiKey: "sk-test", wantPath: "/openai/v1/chat/completions"},
		{name: "minimax-m3 is anthropic on the go subscription", provider: "opencode-go", model: "minimax-m3", apiKey: "sk-test", wantPath: "/go/anthropic/v1/messages"},
		{name: "go chat completions carries the subscription segment", provider: "opencode-go", model: "glm-5.3", apiKey: "sk-test", wantPath: "/go/openai/v1/chat/completions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FOREBRAIN_HOME", t.TempDir())
			srv, calls := openCodeGateway(t)
			runOpenCodeClient(t, &LLMProviderYAML{
				Provider: tc.provider,
				Model:    tc.model,
				APIKey:   tc.apiKey,
				BaseURL:  srv.URL,
			})
			got := calls()
			if len(got) != 1 {
				t.Fatalf("requests = %d, want 1: %+v", len(got), got)
			}
			if got[0].Path != tc.wantPath {
				t.Fatalf("request path = %q, want %q", got[0].Path, tc.wantPath)
			}
			// The transport is the only thing that stamps this identity, and
			// OpenCode's Go subscription admits third-party clients by it — on
			// every protocol, including responses.
			//
			// The responses client blanks the User-Agent by default (that is how
			// the ChatGPT/Codex path keeps the SDK's identity off the wire), so
			// this assertion is also the end-to-end proof that the OpenCode
			// transport's claim survives the responses chain intact.
			if got[0].Agent == "" || !strings.HasPrefix(got[0].Agent, "forebrain-harness/") {
				t.Fatalf("User-Agent = %q, want forebrain-harness/<version>", got[0].Agent)
			}
			wantAuth := "Bearer " + tc.apiKey
			if tc.apiKey == "" {
				wantAuth = "Bearer public"
			}
			if got[0].Auth != wantAuth {
				t.Fatalf("Authorization = %q, want %q", got[0].Auth, wantAuth)
			}
		})
	}
}

// TestNewLLMFromYAMLAPIPathNamesTheProtocol pins the third format a model
// service can carry: a stored "/v1/messages" asks for the Anthropic Messages
// client, on a provider that is not itself an Anthropic one. The path is a
// protocol marker, not a URL — the Messages client appends the endpoint to the
// base URL — so the request must reach the base URL's own /v1/messages with
// Anthropic's headers, not a Chat Completions body rewritten onto that path.
func TestNewLLMFromYAMLAPIPathNamesTheProtocol(t *testing.T) {
	for _, tc := range []struct {
		name        string
		apiPath     string
		wantPath    string
		wantAnthrop bool
	}{
		{name: "messages path speaks anthropic", apiPath: "/v1/messages", wantPath: "/v1/messages", wantAnthrop: true},
		{name: "messages path tolerates a trailing slash", apiPath: "/v1/messages/", wantPath: "/v1/messages", wantAnthrop: true},
		{name: "chat path stays chat completions", apiPath: "/chat/completions", wantPath: "/chat/completions", wantAnthrop: false},
		{name: "responses path stays responses", apiPath: "/responses", wantPath: "/responses", wantAnthrop: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A server per case: a client retries a failed stream, and the
			// question here is what the first request looked like.
			srv, calls := openCodeGateway(t)
			runOpenCodeClient(t, &LLMProviderYAML{
				Provider: "deepseek",
				Model:    "deepseek-chat",
				APIKey:   "sk-test",
				BaseURL:  srv.URL,
				APIPath:  tc.apiPath,
			})

			got := calls()
			if len(got) == 0 {
				t.Fatal("no request reached the server")
			}
			if got[0].Path != tc.wantPath {
				t.Fatalf("request path = %q, want %q", got[0].Path, tc.wantPath)
			}
			// The two clients authenticate differently, which is what tells
			// them apart on a path both would otherwise produce.
			isAnthropic := got[0].Header.Get("anthropic-version") != ""
			if isAnthropic != tc.wantAnthrop {
				t.Fatalf("anthropic-version = %q, want anthropic client: %v", got[0].Header.Get("anthropic-version"), tc.wantAnthrop)
			}
		})
	}
}

// TestNewLLMFromYAMLOpenCodeCredentialChain pins the order a credential is
// taken in, because three different login surfaces write different things: an
// api_key (possibly an ${ENV} reference), OPENCODE_API_KEY, the opencode.json
// the device-code login writes, and the literal "public" bearer that only the
// free family accepts.
func TestNewLLMFromYAMLOpenCodeCredentialChain(t *testing.T) {
	const paidModel = "deepseek-v4-pro"
	t.Run("paid model without any credential fails loudly", func(t *testing.T) {
		t.Setenv("FOREBRAIN_HOME", t.TempDir())
		t.Setenv("OPENCODE_API_KEY", "")
		_, err := NewLLMFromYAML(&LLMProviderYAML{Provider: "opencode", Model: paidModel, BaseURL: "https://opencode.ai/inference"})
		if err == nil {
			t.Fatal("NewLLMFromYAML accepted a paid OpenCode model with no credential")
		}
		if !strings.Contains(err.Error(), "OPENCODE_API_KEY") || !strings.Contains(err.Error(), "/connect") {
			t.Fatalf("error = %v, want it to name the credential and /connect", err)
		}
	})
	t.Run("environment key", func(t *testing.T) {
		t.Setenv("FOREBRAIN_HOME", t.TempDir())
		t.Setenv("OPENCODE_API_KEY", "sk-env")
		srv, calls := openCodeGateway(t)
		runOpenCodeClient(t, &LLMProviderYAML{Provider: "opencode-go", Model: paidModel, BaseURL: srv.URL})
		got := calls()
		if len(got) != 1 || got[0].Auth != "Bearer sk-env" {
			t.Fatalf("requests = %+v, want one with Bearer sk-env", got)
		}
	})
	t.Run("config key wins over the environment", func(t *testing.T) {
		t.Setenv("FOREBRAIN_HOME", t.TempDir())
		t.Setenv("OPENCODE_API_KEY", "sk-env")
		t.Setenv("OPENCODE_TEST_KEY", "sk-config")
		srv, calls := openCodeGateway(t)
		runOpenCodeClient(t, &LLMProviderYAML{Provider: "opencode", Model: paidModel, APIKey: "${OPENCODE_TEST_KEY}", BaseURL: srv.URL})
		got := calls()
		if len(got) != 1 || got[0].Auth != "Bearer sk-config" {
			t.Fatalf("requests = %+v, want one with Bearer sk-config", got)
		}
	})
	t.Run("login file", func(t *testing.T) {
		homeRoot := t.TempDir()
		t.Setenv("FOREBRAIN_HOME", homeRoot)
		t.Setenv("OPENCODE_API_KEY", "")
		path := opencode.DefaultPath(homeRoot)
		if err := opencode.Save(path, opencode.Credentials{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			ExpiresAt:    time.Now().Add(2 * time.Hour),
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		srv, calls := openCodeGateway(t)
		runOpenCodeClient(t, &LLMProviderYAML{Provider: "opencode-go", Model: paidModel, BaseURL: srv.URL})
		got := calls()
		if len(got) != 1 || got[0].Auth != "Bearer access-token" {
			t.Fatalf("requests = %+v, want one with the logged-in access token", got)
		}
	})
	t.Run("public bearer is offered to the free family only", func(t *testing.T) {
		t.Setenv("FOREBRAIN_HOME", t.TempDir())
		t.Setenv("OPENCODE_API_KEY", "")
		srv, calls := openCodeGateway(t)
		runOpenCodeClient(t, &LLMProviderYAML{Provider: "opencode", Model: "glm-5.3-flash-free", BaseURL: srv.URL})
		got := calls()
		if len(got) != 1 || got[0].Auth != "Bearer public" {
			t.Fatalf("requests = %+v, want one with Bearer public", got)
		}
	})
}

// TestNewLLMFromYAMLOpenCodeHonorsConfiguredRoot keeps the proxy-mirror case
// working: a user who points base_url at their own gateway keeps that root,
// with the protocol path appended under it.
func TestNewLLMFromYAMLOpenCodeHonorsConfiguredRoot(t *testing.T) {
	t.Setenv("FOREBRAIN_HOME", t.TempDir())
	t.Setenv("OPENCODE_API_KEY", "sk-test")
	srv, calls := openCodeGateway(t)
	runOpenCodeClient(t, &LLMProviderYAML{
		Provider: "opencode",
		Model:    "deepseek-v4-pro",
		BaseURL:  srv.URL + "/mirror",
	})
	got := calls()
	if len(got) != 1 || got[0].Path != "/mirror/openai/v1/chat/completions" {
		t.Fatalf("requests = %+v, want a call under the configured /mirror root", got)
	}
}

// TestProviderInferenceIgnoresLoginBackedProviders pins the one thing the
// reference rows for the login-backed surfaces must not break: a bare model id
// whose name is shared (glm-5.3 is served by zhipuai and listed by both
// OpenCode surfaces) still infers the static provider. Counting the account
// catalog's rows would make every such id ambiguous and answer nothing.
func TestProviderInferenceIgnoresLoginBackedProviders(t *testing.T) {
	data, err := llm.RepoModelsJSONForTest()
	if err != nil {
		t.Fatal(err)
	}
	cat, err := llm.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	llm.SetGlobalCatalogForTest(cat)
	t.Cleanup(func() { llm.SetGlobalCatalogForTest(cat) })

	// The ambiguity has to be real, or the test proves nothing.
	owners := map[string]bool{}
	for _, m := range llm.AllModels() {
		if strings.EqualFold(strings.TrimSpace(m.APIModel), "glm-5.3") {
			owners[m.Provider] = true
		}
	}
	for _, provider := range []string{"zhipuai", "opencode", "opencode-go"} {
		if !owners[provider] {
			t.Fatalf("catalog no longer lists %s/glm-5.3 (owners=%v)", provider, owners)
		}
	}

	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{
		Definitions: map[string]appcfg.AgentDefinition{
			"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{
				{Provider: "zhipuai", Model: "glm-5.3"},
			}},
		},
	}}
	if got := ProviderForAgentModel(cfg, "main", "glm-5.3"); got != "zhipuai" {
		t.Fatalf("ProviderForAgentModel(glm-5.3) = %q, want zhipuai", got)
	}
	// A model only a login-backed surface carries cannot be inferred at all.
	if got := ProviderForAgentModel(cfg, "main", "kimi-k3"); got != "" {
		t.Fatalf("ProviderForAgentModel(kimi-k3) = %q, want empty (opencode-only id)", got)
	}
}
