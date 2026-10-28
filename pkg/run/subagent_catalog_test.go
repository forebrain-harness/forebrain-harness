package run

import (
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

func subagentCatalogTestBool(v bool) *bool { return &v }

// catalogTestConfig is one primary agent with a disabled built-in, a rewrite of
// another built-in, and two custom subagents (one pick, one one-shot).
func catalogTestConfig() *appcfg.Root {
	return &appcfg.Root{Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {
			Primary: true,
			Subagents: []appcfg.SubagentConfig{
				{
					Name:    "verification",
					Enabled: subagentCatalogTestBool(false),
				},
				{
					Name:            "explore",
					Model:           "deepseek/deepseek-v4-flash",
					ReasoningEffort: "low",
					SystemPrompt:    "Search only what the task names.",
				},
				{
					Name:         "repo-scout",
					Description:  "Finds the files a task touches",
					SystemPrompt: "You are repo-scout.",
					Tools:        appcfg.SubagentToolsPick,
					AllowedTools: []string{"read_file", "code_search"},
				},
				{
					Name:         "note-taker",
					Description:  "Writes one note, then stops",
					SystemPrompt: "You are note-taker.",
					Continuable:  subagentCatalogTestBool(false),
				},
			},
		},
	}}}
}

func TestSubagentCatalogListsEnabledBuiltinsThenCustom(t *testing.T) {
	catalog := SubagentCatalogFor(catalogTestConfig(), "main")
	var names []string
	for _, def := range catalog.Public() {
		names = append(names, def.Name)
	}
	want := []string{"general-purpose", "explore", "plan", "repo-scout", "note-taker"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("Public() = %v, want %v", names, want)
	}
	// An agent with no configuration of its own dispatches every built-in, in
	// the definition order.
	catalog = SubagentCatalogFor(catalogTestConfig(), "someone-else")
	for i, def := range catalog.Public() {
		if def.Name != agent.PublicSubagentNames[i] {
			t.Fatalf("unconfigured agent Public()[%d] = %q, want %q", i, def.Name, agent.PublicSubagentNames[i])
		}
	}
}

func TestSubagentCatalogDisabledBuiltinIsRefusedWithReason(t *testing.T) {
	catalog := SubagentCatalogFor(catalogTestConfig(), "main")
	if _, err := catalog.ResolvePublic("verification"); err == nil {
		t.Fatal("a disabled built-in must not resolve")
	} else if !strings.Contains(err.Error(), `"verification" is disabled for this agent`) {
		t.Fatalf("error %q does not say the type is disabled for this agent", err)
	}
	if def, err := catalog.ResolvePublic(" EXPLORE "); err != nil || def.Name != "explore" {
		t.Fatalf("an enabled built-in must resolve: def=%+v err=%v", def, err)
	}
	if _, err := catalog.ResolvePublic("no-such-agent-type"); err == nil {
		t.Fatal("an unknown type must not resolve")
	} else if !strings.Contains(err.Error(), "repo-scout") || strings.Contains(err.Error(), "verification") {
		t.Fatalf("error %q must list the available types, which excludes the disabled one", err)
	}
	// A disabled custom subagent is refused the same way: the model was told
	// about it by an older enum and must learn why it cannot have it.
	cfg := catalogTestConfig()
	cfg.Agents.Definitions["main"].Subagents[2].Enabled = subagentCatalogTestBool(false)
	catalog = SubagentCatalogFor(cfg, "main")
	if _, err := catalog.ResolvePublic("repo-scout"); err == nil {
		t.Fatal("a disabled custom subagent must not resolve")
	} else if !strings.Contains(err.Error(), `"repo-scout" is disabled for this agent`) {
		t.Fatalf("error %q does not say the custom type is disabled for this agent", err)
	}
}

func TestSubagentCatalogPromptPrefersRewriteThenBuiltin(t *testing.T) {
	catalog := SubagentCatalogFor(catalogTestConfig(), "main")
	if got, ok := catalog.Prompt("explore"); !ok || got != "Search only what the task names." {
		t.Fatalf("Prompt(explore) = %q, %v; want the agent's rewrite", got, ok)
	}
	builtin, _ := agent.SystemPromptForSubtype("plan")
	if got, ok := catalog.Prompt("plan"); !ok || got != builtin {
		t.Fatalf("Prompt(plan) = %q, %v; want the built-in prompt %q", got, ok, builtin)
	}
	if got, ok := catalog.Prompt("repo-scout"); !ok || got != "You are repo-scout." {
		t.Fatalf("Prompt(repo-scout) = %q, %v; want the custom prompt", got, ok)
	}
	// An override whose prompt is blank is not a rewrite: the built-in text
	// still applies, byte for byte.
	cfg := catalogTestConfig()
	cfg.Agents.Definitions["main"].Subagents[1].SystemPrompt = "   "
	catalog = SubagentCatalogFor(cfg, "main")
	if got, ok := catalog.Prompt("explore"); !ok || got != mustSystemPrompt(t, "explore") {
		t.Fatalf("Prompt(explore) = %q, %v; want the built-in prompt", got, ok)
	}
}

func TestSubagentCatalogCustomDefinitionFields(t *testing.T) {
	catalog := SubagentCatalogFor(catalogTestConfig(), "main")
	def, err := catalog.ResolvePublic("repo-scout")
	if err != nil {
		t.Fatalf("resolve repo-scout: %v", err)
	}
	if def.Description != "Finds the files a task touches" || def.SystemPrompt != "You are repo-scout." {
		t.Fatalf("custom definition text wrong: %+v", def)
	}
	if def.Source != SubagentSourceCustom {
		t.Fatalf("Source = %q, want %q", def.Source, SubagentSourceCustom)
	}
	if !def.Continuable || def.OneShot {
		t.Fatalf("a custom subagent without continuable must be continuable: %+v", def)
	}
	def, err = catalog.ResolvePublic("note-taker")
	if err != nil {
		t.Fatalf("resolve note-taker: %v", err)
	}
	if def.Continuable || !def.OneShot {
		t.Fatalf("continuable: false must make the subagent one-shot: %+v", def)
	}
	policy, ok := catalog.ToolPolicy("repo-scout")
	if !ok || policy.Mode != appcfg.SubagentToolsPick {
		t.Fatalf("ToolPolicy(repo-scout) = %+v, %v; want pick", policy, ok)
	}
	if policy.Blocks("read_file") || !policy.Blocks("write_file") || !policy.Blocks("subagent_run") {
		t.Fatalf("pick policy must allow only the chosen tools: %+v", policy)
	}
	// A custom subagent with no tools mode is read-only, the narrowest policy.
	policy, ok = catalog.ToolPolicy("note-taker")
	if !ok || policy.Mode != appcfg.SubagentToolsReadOnly || !policy.Blocks("write_file") {
		t.Fatalf("ToolPolicy(note-taker) = %+v, %v; want read_only", policy, ok)
	}
	if _, ok := catalog.ToolPolicy("explore"); ok {
		t.Fatal("a built-in keeps its static tool policy; the catalog must not answer one")
	}
}

func TestSubagentCatalogEnumDescriptionNamesCustomSubagents(t *testing.T) {
	catalog := SubagentCatalogFor(catalogTestConfig(), "main")
	got := catalog.EnumDescription()
	for _, want := range []string{
		"Optional public typed subagent.",
		"general-purpose: bounded independent implementation or general delegated work.",
		"explore: read-only investigation and locating facts.",
		"plan: read-only implementation design, sequencing, risks, and verification planning.",
		"repo-scout: Finds the files a task touches.",
		"note-taker: Writes one note, then stops.",
		"Omit subagent_type to fork the current agent.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("description is missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "verification:") {
		t.Fatalf("description names a disabled built-in: %s", got)
	}
	// The sentence the tool shipped before per-agent configuration is exactly
	// what an unconfigured agent's model reads, so nothing about the schema
	// prompt changed for anyone who does not configure subagents.
	var unconfigured strings.Builder
	unconfigured.WriteString("Optional public typed subagent. general-purpose: bounded independent implementation or general delegated work. explore: read-only investigation and locating facts. plan: read-only implementation design, sequencing, risks, and verification planning. verification: falsify or verify an implementation with concrete checks. Omit subagent_type to fork the current agent. Fork inherits the parent conversation context, runs in the background, and keeps intermediate tool output out of the main context.")
	if got := SubagentCatalogFor(nil, "main").EnumDescription(); got != unconfigured.String() {
		t.Fatalf("unconfigured description changed:\n got=%s\nwant=%s", got, unconfigured.String())
	}
}

func TestSubagentCatalogModelSplitsProviderAndModel(t *testing.T) {
	catalog := SubagentCatalogFor(catalogTestConfig(), "main")
	provider, model, effort, ok := catalog.Model("explore")
	if !ok || provider != "deepseek" || model != "deepseek-v4-flash" || effort != "low" {
		t.Fatalf("Model(explore) = %q %q %q %v", provider, model, effort, ok)
	}
	if _, _, _, ok := catalog.Model("plan"); ok {
		t.Fatal("a built-in with no model override must follow the conversation")
	}
	if _, _, _, ok := catalog.Model("repo-scout"); ok {
		t.Fatal("a custom subagent with no model must follow the conversation")
	}
}

func mustSystemPrompt(t *testing.T, name string) string {
	t.Helper()
	prompt, ok := agent.SystemPromptForSubtype(name)
	if !ok {
		t.Fatalf("the %q built-in has no system prompt", name)
	}
	return prompt
}

// TestOfficeCatalogOffersOnlyGeneralPurposeAndExplore pins what an office
// conversation may dispatch: the two built-ins whose work is not about code
// plus the agent's own custom subagents, with plan and verification refused
// as not available rather than merely disabled — while a coding catalog of
// the same configuration keeps its built-ins.
func TestOfficeCatalogOffersOnlyGeneralPurposeAndExplore(t *testing.T) {
	catalog := SubagentCatalogFor(catalogTestConfig(), "main").ForProjectType("office")
	var names []string
	for _, def := range catalog.Public() {
		names = append(names, def.Name)
	}
	want := []string{"general-purpose", "explore", "repo-scout", "note-taker"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("office Public() = %v, want %v", names, want)
	}
	if _, err := catalog.ResolvePublic("plan"); err == nil || !strings.Contains(err.Error(), "not available in office projects") {
		t.Fatalf("office ResolvePublic(plan) error = %v, want \"not available in office projects\"", err)
	}
	if _, err := catalog.ResolvePublic("verification"); err == nil || !strings.Contains(err.Error(), "not available in office projects") {
		t.Fatalf("office ResolvePublic(verification) error = %v, want \"not available in office projects\"", err)
	}
	// The same configuration read as coding keeps its coding built-ins.
	coding := SubagentCatalogFor(catalogTestConfig(), "main")
	var codingNames []string
	for _, def := range coding.Public() {
		codingNames = append(codingNames, def.Name)
	}
	codingWant := []string{"general-purpose", "explore", "plan", "repo-scout", "note-taker"}
	if strings.Join(codingNames, ",") != strings.Join(codingWant, ",") {
		t.Fatalf("coding Public() = %v, want %v", codingNames, codingWant)
	}
	if _, err := coding.ResolvePublic("plan"); err != nil {
		t.Fatalf("coding ResolvePublic(plan) = %v, want ok", err)
	}
}

// TestProjectTypeCatalogUsesDistinctSubagentPromptsUnlessConfigured checks
// the office and batch wordings reach a typed run — and that a prompt the
// user configured still outranks them, exactly as it outranks the coding
// built-in text.
func TestProjectTypeCatalogUsesDistinctSubagentPromptsUnlessConfigured(t *testing.T) {
	base := SubagentCatalogFor(catalogTestConfig(), "main")
	officePrompt, ok := base.ForProjectType("office").Prompt("general-purpose")
	if !ok {
		t.Fatal("office Prompt(general-purpose) not ok")
	}
	batchPrompt, ok := base.ForProjectType("batch").Prompt("general-purpose")
	if !ok {
		t.Fatal("batch Prompt(general-purpose) not ok")
	}
	if officePrompt == batchPrompt {
		t.Error("office and batch share the general-purpose subagent prompt")
	}
	if want, ok := agent.ProjectTypeSubagentPrompt("general-purpose", "office"); !ok || officePrompt != want {
		t.Errorf("office Prompt(general-purpose) = %q, want the office constant %q", officePrompt, want)
	}
	if want, ok := agent.ProjectTypeSubagentPrompt("general-purpose", "batch"); !ok || batchPrompt != want {
		t.Errorf("batch Prompt(general-purpose) = %q, want the batch constant %q", batchPrompt, want)
	}
	if !strings.Contains(officePrompt, "office task") || !strings.Contains(batchPrompt, "unattended batch task") {
		t.Error("subagent prompts do not carry their project-type wording")
	}
	codingPrompt, ok := base.Prompt("general-purpose")
	if !ok || strings.Contains(codingPrompt, "office task") {
		t.Errorf("coding Prompt(general-purpose) = %q, want the coding built-in", codingPrompt)
	}
	// The configured rewrite of explore wins in every project type.
	for _, projectType := range []string{"", "office", "batch"} {
		prompt, ok := base.ForProjectType(projectType).Prompt("explore")
		if !ok || prompt != "Search only what the task names." {
			t.Errorf("Prompt(explore, %q) = %q, want the configured rewrite", projectType, prompt)
		}
	}
}

// TestOfficeEnumDescriptionUsesOfficeSentences checks the subagent_type
// schema an office model reads: the office sentences, no plan or
// verification entries, custom subagents still named.
func TestOfficeEnumDescriptionUsesOfficeSentences(t *testing.T) {
	got := SubagentCatalogFor(catalogTestConfig(), "main").ForProjectType("office").EnumDescription()
	for _, name := range agent.OfficeSubagentNames {
		if !strings.Contains(got, name+": "+agent.OfficeSubagentEnumSentences[name]+".") {
			t.Errorf("office enum description lacks %s's office sentence: %q", name, got)
		}
	}
	for _, banned := range []string{"plan:", "verification:", "read-only investigation and locating facts"} {
		if strings.Contains(got, banned) {
			t.Errorf("office enum description contains %q: %q", banned, got)
		}
	}
	if !strings.Contains(got, "repo-scout:") {
		t.Errorf("office enum description omits the custom subagent: %q", got)
	}
}

// TestBatchEnumDescriptionUsesBatchSentences is the batch counterpart of
// TestOfficeEnumDescriptionUsesOfficeSentences.
func TestBatchEnumDescriptionUsesBatchSentences(t *testing.T) {
	got := SubagentCatalogFor(catalogTestConfig(), "main").ForProjectType("batch").EnumDescription()
	for _, name := range agent.OfficeSubagentNames {
		if !strings.Contains(got, name+": "+agent.BatchSubagentEnumSentences[name]+".") {
			t.Errorf("batch enum description lacks %s's batch sentence: %q", name, got)
		}
	}
	for _, banned := range []string{"plan:", "verification:", agent.OfficeSubagentEnumSentences["general-purpose"]} {
		if strings.Contains(got, banned) {
			t.Errorf("batch enum description contains %q: %q", banned, got)
		}
	}
}
