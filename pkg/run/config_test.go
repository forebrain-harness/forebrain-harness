package run

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

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
