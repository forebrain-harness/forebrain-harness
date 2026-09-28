package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

type catalogCapturingLLM struct{ developer []string }

func (c *catalogCapturingLLM) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	var got string
	for _, m := range messages {
		if m.Role == llm.RoleDeveloper {
			got += m.TextContent()
		}
	}
	c.developer = append(c.developer, got)
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("ok")})
	return &llm.Result{Message: &msg}, nil
}

// newPromptStateStore is a real session store: the catalog freeze lives in a
// session's own prompt state, so a test that means to exercise the freeze has
// to give the wrapper a session to freeze against.
func newPromptStateStore(t *testing.T) *state.SessionStore {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	return store
}

func writeCatalogSkill(t *testing.T, root, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Every enabled skill is named and described ahead of the conversation, with
// the path the model reads to use it.
func TestSkillCatalogIsInjectedAheadOfTheConversation(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "review-flow", "Review a change carefully")

	inner := &catalogCapturingLLM{}
	wrapped := wrapSkillCatalogLLM(inner, []string{root}, t.TempDir(), newPromptStateStore(t), nil)
	ctx := llm.WithAgentSessionID(context.Background(), "s1")
	if _, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := inner.developer[0]
	for _, want := range []string{"## Skills", "### Available skills", "review-flow", "Review a change carefully", "### How to use skills"} {
		if !strings.Contains(got, want) {
			t.Fatalf("catalog missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, filepath.Join(root, "review-flow", "SKILL.md")) {
		t.Fatalf("catalog does not tell the model which file to read:\n%s", got)
	}
}

// I0: the catalog sits in the cached prefix, so it must be byte-identical for
// the life of a session even when the skill set changes on disk underneath it.
// Re-rendering per turn would re-bill every cached token in the session.
func TestSkillCatalogIsFrozenForTheSession(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "first-skill", "The only skill at session start")

	inner := &catalogCapturingLLM{}
	sessions := newPromptStateStore(t)
	if err := sessions.Ensure(context.Background(), "s1", "first"); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Ensure(context.Background(), "s2", "second"); err != nil {
		t.Fatal(err)
	}
	wrapped := wrapSkillCatalogLLM(inner, []string{root}, t.TempDir(), sessions, nil)
	ctx := llm.WithAgentSessionID(context.Background(), "s1")
	if _, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("one"))}, nil); err != nil {
		t.Fatal(err)
	}

	// A skill is installed mid-session.
	writeCatalogSkill(t, root, "second-skill", "Installed after the session began")
	if _, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("two"))}, nil); err != nil {
		t.Fatal(err)
	}

	if inner.developer[0] != inner.developer[1] {
		t.Fatalf("the catalog changed mid-session, which re-bills the whole cached prefix:\nfirst:\n%s\nsecond:\n%s",
			inner.developer[0], inner.developer[1])
	}
	if strings.Contains(inner.developer[1], "second-skill") {
		t.Fatal("a mid-session skill install leaked into the frozen catalog")
	}

	// A new session picks the change up.
	next := llm.WithAgentSessionID(context.Background(), "s2")
	if _, err := wrapped.Execute(next, []llm.Message{llm.UserMessage(llm.Text("three"))}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inner.developer[2], "second-skill") {
		t.Fatalf("a new session did not pick up the installed skill:\n%s", inner.developer[2])
	}
}

// TestSkillCatalogFreezeOutlivesTheProcess pins where the freeze lives: in the
// session's own prompt state, not in this process. It is what replaced a map of
// rendered catalogs that a long-running gateway kept for every session it had
// ever served, and it buys the property that map never had — a session resumed
// after a restart is served the bytes it was served before, instead of a fresh
// render that re-bills its whole cached prefix.
func TestSkillCatalogFreezeOutlivesTheProcess(t *testing.T) {
	root, stateRoot := t.TempDir(), t.TempDir()
	writeCatalogSkill(t, root, "first-skill", "Present when the session started")
	sessions := newPromptStateStore(t)
	if err := sessions.Ensure(context.Background(), "s1", "first"); err != nil {
		t.Fatal(err)
	}
	ctx := llm.WithAgentSessionID(context.Background(), "s1")

	before := &catalogCapturingLLM{}
	if _, err := wrapSkillCatalogLLM(before, []string{root}, stateRoot, sessions, nil).Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("one"))}, nil); err != nil {
		t.Fatal(err)
	}

	// The process goes away and a skill is installed while it is down; the
	// same session then resumes against a brand-new runtime.
	writeCatalogSkill(t, root, "second-skill", "Installed while the process was down")
	after := &catalogCapturingLLM{}
	if _, err := wrapSkillCatalogLLM(after, []string{root}, stateRoot, sessions, nil).Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("two"))}, nil); err != nil {
		t.Fatal(err)
	}
	if after.developer[0] != before.developer[0] {
		t.Fatalf("a resumed session was served a re-rendered catalog:\nbefore:\n%s\nafter:\n%s", before.developer[0], after.developer[0])
	}
}

// A run with no session — a hook, a one-off — has no session to freeze
// against, and must still not change the block it injects from call to call.
func TestSkillCatalogIsFrozenForASessionlessRuntime(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "first-skill", "Present at the first call")

	inner := &catalogCapturingLLM{}
	wrapped := wrapSkillCatalogLLM(inner, []string{root}, t.TempDir(), nil, nil)
	if _, err := wrapped.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("one"))}, nil); err != nil {
		t.Fatal(err)
	}
	writeCatalogSkill(t, root, "second-skill", "Installed between calls")
	if _, err := wrapped.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("two"))}, nil); err != nil {
		t.Fatal(err)
	}
	if inner.developer[1] != inner.developer[0] {
		t.Fatalf("the injected catalog changed between calls:\nfirst:\n%s\nsecond:\n%s", inner.developer[0], inner.developer[1])
	}
}

// With no skills there is no section at all, rather than an empty heading.
// The skill-offer criteria join the same frozen block. Both tests below pin
// the two properties the frozen prefix depends on: the criteria render even
// with no skills installed, and a session never sees the block re-rendered.
func TestSkillOfferGuidanceRendersWithZeroSkills(t *testing.T) {
	inner := &catalogCapturingLLM{}
	wrapped := wrapSkillCatalogLLM(inner, []string{t.TempDir()}, t.TempDir(), newPromptStateStore(t), func() string {
		return skill.RenderSkillOfferGuidance(skill.SkillOfferGuidanceOptions{Enabled: true})
	})
	ctx := llm.WithAgentSessionID(context.Background(), "no-skills")
	if _, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
		t.Fatal(err)
	}
	got := inner.developer[0]
	if strings.Contains(got, "### Available skills") {
		t.Fatalf("no skills are installed, yet a catalog listing appeared:\n%s", got)
	}
	if !strings.Contains(got, "/skill-generator") {
		t.Fatalf("zero-skill session lost the offer criteria:\n%s", got)
	}
}

func TestSkillOfferGuidanceJoinsTheFrozenBlock(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "first-skill", "Present at session start")
	sessions := newPromptStateStore(t)
	if err := sessions.Ensure(context.Background(), "s1", "first"); err != nil {
		t.Fatal(err)
	}
	inner := &catalogCapturingLLM{}
	wrapped := wrapSkillCatalogLLM(inner, []string{root}, t.TempDir(), sessions, func() string {
		return skill.RenderSkillOfferGuidance(skill.SkillOfferGuidanceOptions{Enabled: true})
	})
	ctx := llm.WithAgentSessionID(context.Background(), "s1")
	if _, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("one"))}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("two"))}, nil); err != nil {
		t.Fatal(err)
	}
	if inner.developer[0] != inner.developer[1] {
		t.Fatalf("the frozen block re-rendered within one session:\nfirst:\n%s\nsecond:\n%s",
			inner.developer[0], inner.developer[1])
	}
	if !strings.Contains(inner.developer[0], "/skill-generator") {
		t.Fatalf("frozen block lost the offer criteria:\n%s", inner.developer[0])
	}
}

func TestSkillOfferGuidanceOffKeepsCatalogByteIdentical(t *testing.T) {
	inner := &catalogCapturingLLM{}
	wrapped := wrapSkillCatalogLLM(inner, []string{t.TempDir()}, t.TempDir(), newPromptStateStore(t), func() string {
		return skill.RenderSkillOfferGuidance(skill.SkillOfferGuidanceOptions{Enabled: false})
	})
	ctx := llm.WithAgentSessionID(context.Background(), "offer-off")
	if _, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(inner.developer[0], "skill-generator") {
		t.Fatalf("disabled offer leaked guidance into the frozen block:\n%s", inner.developer[0])
	}
}

func TestSkillCatalogIsSilentWithoutSkills(t *testing.T) {
	inner := &catalogCapturingLLM{}
	wrapped := wrapSkillCatalogLLM(inner, []string{t.TempDir()}, t.TempDir(), newPromptStateStore(t), nil)
	ctx := llm.WithAgentSessionID(context.Background(), "s1")
	if _, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(inner.developer[0], "## Skills") {
		t.Fatalf("an empty skills section was injected:\n%s", inner.developer[0])
	}
}

// TestRunnerKeepsPrefixWhenSkillFilesChangeMidSession pins the rule that a
// skill change only reaches the model in a new session. The skills catalog sits
// in the prompt prefix, so rebuilding the agent because a SKILL.md appeared
// would invalidate the cached system prompt, tool definitions, and every prior
// turn — for a change no one asked to apply to this session.
func TestRunnerKeepsPrefixWhenSkillFilesChangeMidSession(t *testing.T) {
	home := t.TempDir()
	// An untrusted working directory keeps the project skill root out of the
	// picture, so the assertions below speak only about the workspace root.
	t.Chdir(t.TempDir())
	writeRunnerSkillFile(t, filepath.Join(home, "workspace", "skills", "first-skill", "SKILL.md"),
		"name: first-skill\ndescription: Present before the session started", "First body")

	r := &Runner{Deps: &Deps{Home: home, AppCfg: &appcfg.Root{
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
	}}}
	if err := r.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !hasLoadedSkill(r, "first-skill") {
		t.Fatalf("skill present at load time was not registered: %v", loadedSkillNames(r))
	}
	loaded := r.Agent()

	writeRunnerSkillFile(t, filepath.Join(home, "workspace", "skills", "second-skill", "SKILL.md"),
		"name: second-skill\ndescription: Written after the session started", "Second body")

	// The context is cancelled up front so the turn stops at the LLM call. The
	// reload decision under test happens before that, so the invariant is still
	// exercised without the test depending on a provider.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = r.RunContent(ctx, []llm.ContentPart{llm.Text("hi")})

	if r.Agent() != loaded {
		t.Fatal("agent was rebuilt mid-session after a skill file changed")
	}
	if hasLoadedSkill(r, "second-skill") {
		t.Fatalf("skill written mid-session entered this session's tool table: %v", loadedSkillNames(r))
	}
}

func loadedSkillNames(r *Runner) []string {
	items := r.Tools().LoadedSkills()
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	return names
}

func hasLoadedSkill(r *Runner, name string) bool {
	for _, candidate := range loadedSkillNames(r) {
		if candidate == name {
			return true
		}
	}
	return false
}

// A slash-selected skill is delivered as prompt text, not as a tool result.
// The message must therefore say what it is — the user picked this skill and
// its body is inline — without any vocabulary implying the model called
// something or could call it again.
func TestExplicitSkillActivationMessageCarriesNoToolVocabulary(t *testing.T) {
	body := "<skill>\n<name>review</name>\n<path>/skills/review/SKILL.md</path>\nReview carefully.\n</skill>"
	msg := explicitSkillActivationMessage(explicitSkillActivation{SkillName: "review", Content: body})

	text := msg.TextContent()
	if !strings.Contains(text, "`review` skill") {
		t.Fatalf("message does not name the selected skill:\n%s", text)
	}
	if !strings.Contains(text, body) {
		t.Fatalf("message does not carry the skill body:\n%s", text)
	}
	// The message delivers instructions; it must not read as a description of
	// some callable mechanism, which would send the model looking for one.
	for _, banned := range []string{"tool", "call", "invoke"} {
		if strings.Contains(strings.ToLower(text), banned) {
			t.Fatalf("message reads as an invocation protocol (%q) rather than instructions:\n%s", banned, text)
		}
	}
	if !msg.IsMeta || !msg.Ephemeral {
		t.Fatalf("activation message must stay meta and ephemeral, got IsMeta=%t Ephemeral=%t", msg.IsMeta, msg.Ephemeral)
	}
	if msg.Role != llm.RoleUser {
		t.Fatalf("role = %q, want user: the skill body is content for this turn", msg.Role)
	}
}

// toolCollector stands in for the agent when a test only needs the tools a
// registration produced.
type toolCollector struct{ tools []*llm.Tool }

func (c *toolCollector) AddTool(t *llm.Tool) error {
	c.tools = append(c.tools, t)
	return nil
}

func (c *toolCollector) byName(name string) *llm.Tool {
	for _, t := range c.tools {
		if t.Name() == name {
			return t
		}
	}
	return nil
}

func registerSkillToolForTest(t *testing.T, skillRoots ...string) (*llm.Tool, *tool.State) {
	t.Helper()
	st := tool.NewState(skillRoots...)
	loaded := make([]tool.LoadedSkill, 0, len(skillRoots))
	for _, root := range skillRoots {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				loaded = append(loaded, tool.LoadedSkill{Name: entry.Name(), RootDir: filepath.Join(root, entry.Name())})
			}
		}
	}
	st.ReplaceLoadedSkillCatalog(loaded)
	collector := &toolCollector{}
	if err := RegisterSkillTool(collector, st); err != nil {
		t.Fatal(err)
	}
	skillTool := collector.byName("skill")
	if skillTool == nil {
		t.Fatal("skill tool was not registered")
	}
	return skillTool, st
}

// The skill tool is the model's one way to act on the catalog: it takes a name
// from the list and returns that skill's whole instructions.
func TestSkillToolLoadsADiscoveredSkillByName(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "golang-testing", "Write Go tests")
	skillTool, _ := registerSkillToolForTest(t, root)

	exec, err := skillTool.Execute(context.Background(), `{"name":"golang-testing"}`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out, _ := exec.Result.(string)
	for _, want := range []string{"<skill>", "<name>golang-testing</name>", filepath.Join(root, "golang-testing", "SKILL.md"), "body"} {
		if !strings.Contains(out, want) {
			t.Fatalf("loaded skill is missing %q:\n%s", want, out)
		}
	}
}

// A model echoes a name back in whatever shape it remembers it. Resolution goes
// through the same normalization every other skill lookup uses, so a fold or an
// underscore still reaches the skill the catalog named.
func TestSkillToolResolvesTheNameTheModelEchoesBack(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "golang-code-style", "Go style")
	skillTool, _ := registerSkillToolForTest(t, root)

	for _, spelling := range []string{"golang-code-style", "Golang-Code-Style", "golang_code_style", "  golang-code-style  "} {
		exec, err := skillTool.Execute(context.Background(), `{"name":`+quoteJSON(spelling)+`}`)
		if err != nil {
			t.Fatalf("%q: %v", spelling, err)
		}
		if out, _ := exec.Result.(string); !strings.Contains(out, "<name>golang-code-style</name>") {
			t.Fatalf("%q did not resolve to the installed skill:\n%s", spelling, out)
		}
	}
}

// An unknown name is an ordinary tool error naming what was asked for, so the
// model can fall back in one line instead of retrying a load that cannot work.
func TestSkillToolRejectsANameItDoesNotHave(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "present", "here")
	skillTool, _ := registerSkillToolForTest(t, root)

	if _, err := skillTool.Execute(context.Background(), `{"name":"absent"}`); err == nil {
		t.Fatal("loading a skill this session does not have must fail")
	} else if !strings.Contains(err.Error(), "absent") {
		t.Fatalf("error must name the skill that was asked for: %v", err)
	}
	if _, err := skillTool.Execute(context.Background(), `{"name":"  "}`); err == nil {
		t.Fatal("an empty name must fail")
	}
}

// The schema is what makes this tool safe for the cached prefix: the tool array
// renders ahead of the system prompt, so the schema must not carry the skill
// names. Installing a skill may change the catalog section and nothing else.
func TestSkillToolSchemaDoesNotCarryTheSkillSet(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "alpha-skill", "first")
	before, _ := registerSkillToolForTest(t, root)
	firstSchema, err := json.Marshal(before.InputSchema())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(firstSchema), "alpha-skill") {
		t.Fatalf("schema names an installed skill:\n%s", firstSchema)
	}

	writeCatalogSkill(t, root, "beta-skill", "second")
	after, _ := registerSkillToolForTest(t, root)
	secondSchema, err := json.Marshal(after.InputSchema())
	if err != nil {
		t.Fatal(err)
	}
	if string(firstSchema) != string(secondSchema) {
		t.Fatalf("installing a skill changed the tool schema:\n%s\n%s", firstSchema, secondSchema)
	}
	if before.Description() != after.Description() {
		t.Fatal("installing a skill changed the tool description")
	}
}

// The tool exists even for a session with no skills at all. Registering it
// conditionally would put the skill set back into the tool array: installing
// the first skill would change the array's shape and re-bill the cached
// tools+system prefix, which is exactly what keeping skills out of it prevents.
func TestSkillToolIsRegisteredWithNoSkillsInstalled(t *testing.T) {
	collector := &toolCollector{}
	if err := RegisterSkillTool(collector, tool.NewState()); err != nil {
		t.Fatal(err)
	}
	if collector.byName("skill") == nil {
		t.Fatal("the skill tool must be registered even when no skill is installed")
	}
}

// A load the model performs and a load the user selects must arrive as the same
// context, or a skill would behave differently depending on who reached for it.
func TestSkillToolMatchesExplicitActivationByteForByte(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "shared-load", "same both ways")
	skillTool, _ := registerSkillToolForTest(t, root)

	exec, err := skillTool.Execute(context.Background(), `{"name":"shared-load"}`)
	if err != nil {
		t.Fatal(err)
	}
	viaTool, _ := exec.Result.(string)
	explicit, err := skill.LoadActivation(filepath.Join(root, "shared-load", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if viaTool != explicit.Content {
		t.Fatalf("tool load and explicit activation differ:\n%s\n---\n%s", viaTool, explicit.Content)
	}
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// offerTestHarness builds a skillOfferLLM with every dependency under the
// test's control, plus the inner capturing LLM.
type offerTestHarness struct {
	inner *capturingLLM
	wrap  *skillOfferLLM
	ctx   context.Context
	state *tool.State
	home  string
	cfg   *appcfg.Root
	sid   string
}

func newOfferHarness(t *testing.T) *offerTestHarness {
	t.Helper()
	home := t.TempDir()
	cfg := &appcfg.Root{}
	st := tool.NewState()
	st.ReplaceLoadedSkillCatalog([]tool.LoadedSkill{{Name: skillOfferCommandName, RootDir: filepath.Join(home, ".forebrain", "skills", skillOfferCommandName)}})
	h := &offerTestHarness{
		inner: &capturingLLM{},
		state: st,
		home:  home,
		cfg:   cfg,
		sid:   "offer-session",
	}
	h.ctx = llm.WithAgentSessionID(context.Background(), h.sid)
	h.rewrap()
	return h
}

func (h *offerTestHarness) rewrap() {
	project := safety.ProjectContext{
		Project:    safety.Project{Root: h.home, VersionControlled: true},
		TrustLevel: safety.LevelTrusted,
	}
	h.wrap = wrapSkillOfferLLM(h.inner, h.cfg, func() *tool.State { return h.state }, h.home, project).(*skillOfferLLM)
}

func (h *offerTestHarness) setFeature(enabled bool) {
	off := enabled
	h.cfg.Features.SkillOffer = &off
}

func offerToolCall(id, name string) llm.Message {
	return llm.Message{
		Role:  llm.RoleAssistant,
		Parts: []llm.ContentPart{llm.Text("working")},
		ToolCalls: []llm.ToolCall{{
			ID:       id,
			Type:     llm.ToolTypeFunction,
			Function: llm.FunctionCall{Name: name, Arguments: "{}"},
		}},
	}
}

func offerToolResult(id, text string) llm.Message {
	return llm.ToolResultMessage(id, llm.Text(text))
}

// runSegment builds the messages of one tool-driven run: a genuine user turn,
// then alternating assistant tool-call batches and tool results.
func runSegment(calls []struct {
	id   string
	name string
	fail bool
}) []llm.Message {
	msgs := []llm.Message{llm.UserMessage(llm.Text("do the thing"))}
	for i, c := range calls {
		msgs = append(msgs, offerToolCall(c.id, c.name))
		text := fmt.Sprintf("result %d", i)
		if c.fail {
			text = fmt.Sprintf("Error executing tool '%s': boom", c.name)
		}
		msgs = append(msgs, offerToolResult(c.id, text))
	}
	return msgs
}

// TestSkillOfferGateOpensOnBreadth pins the breadth branch and its exact
// boundary: eight results across three tools open the gate, seven results do
// not, eight results across two tools do not.
func TestSkillOfferGateOpensOnBreadth(t *testing.T) {
	build := func(n int, tools []string) []llm.Message {
		calls := make([]struct {
			id   string
			name string
			fail bool
		}, 0, n)
		for i := 0; i < n; i++ {
			calls = append(calls, struct {
				id   string
				name string
				fail bool
			}{id: fmt.Sprintf("c%d", i), name: tools[i%len(tools)]})
		}
		return runSegment(calls)
	}
	threeTools := []string{"shell", "read_file", "write_file"}
	twoTools := []string{"shell", "read_file"}

	cases := []struct {
		name   string
		msgs   []llm.Message
		inject bool
	}{
		{"eight results three tools", build(8, threeTools), true},
		{"seven results three tools", build(7, threeTools), false},
		{"eight results two tools", build(8, twoTools), false},
		{"nine results three tools", build(9, threeTools), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOfferHarness(t)
			if _, err := h.wrap.Execute(h.ctx, tc.msgs, nil); err != nil {
				t.Fatal(err)
			}
			sent := h.inner.gotMsgs
			injected := len(sent) > len(tc.msgs)
			if injected != tc.inject {
				t.Fatalf("injected=%d want %v\nlast message: %q", len(sent)-len(tc.msgs), tc.inject, llm.TextContent(sent[len(sent)-1].Parts...))
			}
			if tc.inject {
				last := sent[len(sent)-1]
				if !last.IsMeta || last.Role != llm.RoleUser {
					t.Fatalf("reminder must be an IsMeta user message: %#v", last)
				}
				if !strings.Contains(llm.TextContent(last.Parts...), skillOfferMarker) {
					t.Fatalf("reminder missing marker: %q", llm.TextContent(last.Parts...))
				}
			}
		})
	}
}

// TestSkillOfferGateOpensOnRecovery pins the depth branch: a failure the same
// tool later recovered from opens the gate at five results, and stays shut at
// four.
func TestSkillOfferGateOpensOnRecovery(t *testing.T) {
	calls := func(n int) []struct {
		id   string
		name string
		fail bool
	} {
		out := make([]struct {
			id   string
			name string
			fail bool
		}, 0, n)
		for i := 0; i < n; i++ {
			c := struct {
				id   string
				name string
				fail bool
			}{id: fmt.Sprintf("c%d", i), name: "shell"}
			if i == 0 {
				c.fail = true
			}
			out = append(out, c)
		}
		return out
	}
	for _, tc := range []struct {
		n      int
		inject bool
	}{{5, true}, {4, false}} {
		h := newOfferHarness(t)
		msgs := runSegment(calls(tc.n))
		if _, err := h.wrap.Execute(h.ctx, msgs, nil); err != nil {
			t.Fatal(err)
		}
		got := h.inner.gotMsgs
		if got := len(got) - len(msgs); (got > 0) != tc.inject {
			t.Fatalf("n=%d injected=%d want %v", tc.n, got, tc.inject)
		}
	}
}

// TestSkillOfferSuppressedWithoutVersionControlledProject covers suppression
// 1: without a version-controlled project root the offer would name a command
// whose output has nowhere to land.
func TestSkillOfferSuppressedWithoutVersionControlledProject(t *testing.T) {
	h := newOfferHarness(t)
	h.wrap.project = safety.ProjectContext{}
	msgs := runSegment(breadthCalls(8, []string{"shell", "read_file", "write_file"}))
	if _, err := h.wrap.Execute(h.ctx, msgs, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(h.inner.gotMsgs); got != len(msgs) {
		t.Fatalf("injected %d reminders without a project root", got-len(msgs))
	}
}

// TestSkillOfferSuppressedWhenFeatureOff covers suppression 2: the switch
// turns off the proposing, not the command.
func TestSkillOfferSuppressedWhenFeatureOff(t *testing.T) {
	h := newOfferHarness(t)
	h.setFeature(false)
	msgs := runSegment(breadthCalls(8, []string{"shell", "read_file", "write_file"}))
	if _, err := h.wrap.Execute(h.ctx, msgs, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(h.inner.gotMsgs); got != len(msgs) {
		t.Fatalf("injected %d reminders with the feature off", got-len(msgs))
	}
}

// TestSkillOfferSuppressedWhenGeneratorUnavailable covers suppression 3: a
// disabled or missing generator skill means the proposed command does not
// exist.
func TestSkillOfferSuppressedWhenGeneratorUnavailable(t *testing.T) {
	h := newOfferHarness(t)
	h.state.ReplaceLoadedSkillCatalog(nil)
	msgs := runSegment(breadthCalls(8, []string{"shell", "read_file", "write_file"}))
	if _, err := h.wrap.Execute(h.ctx, msgs, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(h.inner.gotMsgs); got != len(msgs) {
		t.Fatalf("injected %d reminders with the generator skill unavailable", got-len(msgs))
	}
}

// TestSkillOfferSuppressedForSubagent covers suppression 4: proposals belong
// to the main thread only.
func TestSkillOfferSuppressedForSubagent(t *testing.T) {
	h := newOfferHarness(t)
	ctx := tool.WithForkChild(h.ctx, true)
	msgs := runSegment(breadthCalls(8, []string{"shell", "read_file", "write_file"}))
	if _, err := h.wrap.Execute(ctx, msgs, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(h.inner.gotMsgs); got != len(msgs) {
		t.Fatalf("injected %d reminders in a fork child", got-len(msgs))
	}

	h2 := newOfferHarness(t)
	ctx2 := tool.WithSubagentType(h.ctx, "explore")
	if _, err := h2.wrap.Execute(ctx2, msgs, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(h2.inner.gotMsgs); got != len(msgs) {
		t.Fatalf("injected %d reminders in a typed subagent", got-len(msgs))
	}
}

// TestSkillOfferSuppressedForGeneratorRuns covers suppression 5: a run the
// generator itself started must not propose generating.
func TestSkillOfferSuppressedForGeneratorRuns(t *testing.T) {
	h := newOfferHarness(t)
	ctx := WithExplicitSkillSelection(h.ctx, skillOfferCommandName, "/tmp/whatever/SKILL.md")
	msgs := runSegment(breadthCalls(8, []string{"shell", "read_file", "write_file"}))
	if _, err := h.wrap.Execute(ctx, msgs, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(h.inner.gotMsgs); got != len(msgs) {
		t.Fatalf("injected %d reminders during a generator run", got-len(msgs))
	}
}

// TestSkillOfferSuppressedInPlanMode covers suppression 6: planned work has
// not run yet.
func TestSkillOfferSuppressedInPlanMode(t *testing.T) {
	h := newOfferHarness(t)
	setMode(t, h.home, h.sid, state.ModePlan, 0)
	msgs := runSegment(breadthCalls(8, []string{"shell", "read_file", "write_file"}))
	if _, err := h.wrap.Execute(h.ctx, msgs, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(h.inner.gotMsgs); got != len(msgs) {
		t.Fatalf("injected %d reminders in plan mode", got-len(msgs))
	}
}

// TestSkillOfferSuppressedOnceAlreadyOffered covers suppression 7 and the
// compaction/resume story at once: the once-per-session rule reads the marker
// out of the transcript-visible history, so any later request — next
// iteration, post-compaction replacement, resumed session — that carries the
// persisted reminder is left alone.
func TestSkillOfferSuppressedOnceAlreadyOffered(t *testing.T) {
	h := newOfferHarness(t)
	msgs := runSegment(breadthCalls(8, []string{"shell", "read_file", "write_file"}))
	if _, err := h.wrap.Execute(h.ctx, msgs, nil); err != nil {
		t.Fatal(err)
	}
	sent := h.inner.gotMsgs
	if len(sent) != len(msgs)+1 {
		t.Fatalf("first request must inject exactly one reminder, got %d", len(sent)-len(msgs))
	}
	// The next request of the same run: history plus the adopted reminder.
	if _, err := h.wrap.Execute(h.ctx, sent, nil); err != nil {
		t.Fatal(err)
	}
	second := h.inner.gotMsgs
	if len(second) != len(sent) {
		t.Fatalf("second request must not inject again: %d vs %d", len(second), len(sent))
	}
	// The strict-prefix invariant within one run: the second request is the
	// first request's history with messages only appended.
	if !offerStrictPrefix(sent, second) {
		t.Fatal("second request is not a strict superset of the first in order")
	}
}

// TestSkillOfferReminderAnchoredAcrossIterations pins the anchoring: every
// request of a run sends the reminder at the same boundary, with the history
// before it byte-identical.
func TestSkillOfferReminderAnchoredAcrossIterations(t *testing.T) {
	h := newOfferHarness(t)
	msgs := runSegment(breadthCalls(8, []string{"shell", "read_file", "write_file"}))
	if _, err := h.wrap.Execute(h.ctx, msgs, nil); err != nil {
		t.Fatal(err)
	}
	first := h.inner.gotMsgs
	continued := append(append([]llm.Message{}, first...),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("done")}))
	if _, err := h.wrap.Execute(h.ctx, continued, nil); err != nil {
		t.Fatal(err)
	}
	second := h.inner.gotMsgs
	if len(second) != len(continued) {
		t.Fatalf("anchored reminder must not duplicate on iteration 2: %d vs %d", len(second), len(continued))
	}
	for i := range first {
		if llm.TextContent(first[i].Parts...) != llm.TextContent(second[i].Parts...) ||
			first[i].Role != second[i].Role || first[i].IsMeta != second[i].IsMeta {
			t.Fatalf("iteration 2 moved or rewrote the history at %d", i)
		}
	}
}

// TestSkillOfferSuppressedForRunsBelowTheGate pins the closed gate: a short,
// single-tool run earns no reminder.
func TestSkillOfferSuppressedForRunsBelowTheGate(t *testing.T) {
	h := newOfferHarness(t)
	msgs := runSegment(breadthCalls(3, []string{"read_file"}))
	if _, err := h.wrap.Execute(h.ctx, msgs, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(h.inner.gotMsgs); got != len(msgs) {
		t.Fatalf("injected %d reminders for a short run", got-len(msgs))
	}
}

func breadthCalls(n int, tools []string) []struct {
	id   string
	name string
	fail bool
} {
	calls := make([]struct {
		id   string
		name string
		fail bool
	}, 0, n)
	for i := 0; i < n; i++ {
		calls = append(calls, struct {
			id   string
			name string
			fail bool
		}{id: fmt.Sprintf("c%d", i), name: tools[i%len(tools)]})
	}
	return calls
}

func offerStrictPrefix(prefix, whole []llm.Message) bool {
	if len(prefix) > len(whole) {
		return false
	}
	for i := range prefix {
		if prefix[i].Role != whole[i].Role || prefix[i].IsMeta != whole[i].IsMeta ||
			prefix[i].ToolCallID != whole[i].ToolCallID ||
			llm.TextContent(prefix[i].Parts...) != llm.TextContent(whole[i].Parts...) {
			return false
		}
	}
	return true
}

// guardHarness builds the middleware over one skill root and runs tool calls
// through it, exactly as the tool table does.
type guardHarness struct {
	root   string
	state  *tool.State
	guard  llm.ToolMiddleware
	called int
}

func newGuardHarness(t *testing.T, builtins ...string) *guardHarness {
	t.Helper()
	root := t.TempDir()
	builtin := func(name string) bool {
		for _, b := range builtins {
			if b == name {
				return true
			}
		}
		return false
	}
	h := &guardHarness{root: root, state: tool.NewState()}
	h.guard = skillWriteGuard([]string{root}, func() *tool.State { return h.state }, builtin)
	return h
}

// call runs one tool call through the guard and the wrapped handler, and
// reports the handler's error (or nil) plus whether the handler ran at all.
func (h *guardHarness) call(t *testing.T, toolName string, args any) (handlerErr error, ran bool) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	toolDef, err := llm.NewTool(toolName, "test", func(context.Context, *struct{}) (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	next := h.guard(toolDef, func(context.Context, string) (any, error) {
		h.called++
		return "ok", nil
	})
	_, err = next(context.Background(), string(raw))
	h.called = 0
	return err, err == nil
}

func skillBody(name string, withDescription bool) string {
	body := "---\nname: " + name + "\n"
	if withDescription {
		body += "description: Use this when the release steps matter.\n"
	}
	return body + "---\n\n1. do the thing\n"
}

// A SKILL.md without a description is refused with the parser's own error: it
// would be skipped by discovery everywhere, so writing it is writing nothing.
func TestSkillWriteGuardRefusesAMissingDescription(t *testing.T) {
	h := newGuardHarness(t)
	target := filepath.Join(h.root, "release-flow", "SKILL.md")
	err, _ := h.call(t, "write_file", map[string]any{
		"file_path": target,
		"content":   skillBody("release-flow", false),
	})
	if err == nil {
		t.Fatal("a SKILL.md without a description was allowed to land")
	}
	if !strings.Contains(err.Error(), "description") {
		t.Fatalf("error should be the parser's, and name the missing description: %q", err.Error())
	}
}

func TestSkillWriteGuardRefusesAMismatchedDirectoryName(t *testing.T) {
	h := newGuardHarness(t)
	err, _ := h.call(t, "write_file", map[string]any{
		"file_path": filepath.Join(h.root, "release-flow", "SKILL.md"),
		"content":   skillBody("something-else", true),
	})
	if err == nil {
		t.Fatal("a name/directory mismatch was allowed")
	}
	if !strings.Contains(err.Error(), "something-else") || !strings.Contains(err.Error(), "release-flow") {
		t.Fatalf("error should name both spellings: %q", err.Error())
	}
}

// A skill whose name is a builtin command would take that command over, so the
// write that would create it is refused. An existing SKILL.md under that name
// is left alone: the collision already exists, and refusing its edits would
// only make it uncorrectable.
func TestSkillWriteGuardRefusesABuiltinNameCollision(t *testing.T) {
	h := newGuardHarness(t, "status")
	err, _ := h.call(t, "write_file", map[string]any{
		"file_path": filepath.Join(h.root, "status", "SKILL.md"),
		"content":   skillBody("status", true),
	})
	if err == nil {
		t.Fatal("a skill named after a builtin command was allowed to land")
	}
	if !strings.Contains(err.Error(), "built-in") {
		t.Fatalf("error should explain the collision: %q", err.Error())
	}

	// The same name in an existing directory is editable.
	dir := filepath.Join(h.root, "status")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillBody("status", true)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err, _ := h.call(t, "write_file", map[string]any{
		"file_path": filepath.Join(dir, "SKILL.md"),
		"content":   skillBody("status", true),
	}); err != nil {
		t.Fatalf("correcting an existing skill that already collides must be allowed: %v", err)
	}
}

// A well-formed skill is written through, and so is any ordinary file: the
// guard is about what a SKILL.md must be, not about writing in general.
func TestSkillWriteGuardAllowsValidWrites(t *testing.T) {
	h := newGuardHarness(t)
	if err, ran := h.call(t, "write_file", map[string]any{
		"file_path": filepath.Join(h.root, "release-flow", "SKILL.md"),
		"content":   skillBody("release-flow", true),
	}); err != nil || !ran {
		t.Fatalf("a valid skill was refused: %v", err)
	}
	for _, target := range []string{
		filepath.Join(h.root, "release-flow", "references", "notes.md"),
		filepath.Join(h.root, "notes.md"),
		filepath.Join(t.TempDir(), "plain.md"),
	} {
		if err, ran := h.call(t, "write_file", map[string]any{
			"file_path": target,
			"content":   "anything at all",
		}); err != nil || !ran {
			t.Fatalf("ordinary file %s was refused: %v", target, err)
		}
	}
}

// edit_file is judged on the content it would produce, not on the arguments:
// the same file is refused or allowed depending on what the edit leaves behind.
func TestSkillWriteGuardChecksTheEditedResult(t *testing.T) {
	h := newGuardHarness(t)
	dir := filepath.Join(h.root, "release-flow")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mdPath := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(mdPath, []byte(skillBody("release-flow", true)), 0o600); err != nil {
		t.Fatal(err)
	}

	// Dropping the description line is refused.
	err, _ := h.call(t, "edit_file", map[string]any{
		"file_path":  mdPath,
		"old_string": "description: Use this when the release steps matter.\n",
		"new_string": "",
	})
	if err == nil {
		t.Fatal("an edit that removed the description was allowed")
	}

	// A harmless edit goes through.
	if err, ran := h.call(t, "edit_file", map[string]any{
		"file_path":  mdPath,
		"old_string": "1. do the thing",
		"new_string": "1. do the thing\n2. verify it",
	}); err != nil || !ran {
		t.Fatalf("a valid edit was refused: %v", err)
	}

	// An edit that cannot apply is left to the tool: the guard does not invent
	// an error for a call the tool itself will reject.
	if err, ran := h.call(t, "edit_file", map[string]any{
		"file_path":  mdPath,
		"old_string": "text that is not in the file",
		"new_string": "x",
	}); err != nil || !ran {
		t.Fatalf("an unapplicable edit should pass through: %v", err)
	}
}

// apply_patch issued through shell is the third way a SKILL.md can be written,
// and it is judged the same way: on the file the patch would produce.
func TestSkillWriteGuardChecksApplyPatchThroughShell(t *testing.T) {
	h := newGuardHarness(t)
	badPatch := "apply_patch <<'PATCH'\n*** Begin Patch\n*** Add File: " +
		filepath.Join(h.root, "release-flow", "SKILL.md") +
		"\n+---\n+name: release-flow\n+---\n+\n+body\n*** End Patch\nPATCH"
	err, _ := h.call(t, "shell", map[string]any{"command": badPatch})
	if err == nil {
		t.Fatal("an apply_patch adding a description-less SKILL.md was allowed")
	}
	if !strings.Contains(err.Error(), "description") {
		t.Fatalf("error should be the parser's: %q", err.Error())
	}

	goodPatch := "apply_patch <<'PATCH'\n*** Begin Patch\n*** Add File: " +
		filepath.Join(h.root, "release-flow", "SKILL.md") +
		"\n+---\n+name: release-flow\n+description: Use when cutting a release.\n+---\n+\n+body\n*** End Patch\nPATCH"
	if err, ran := h.call(t, "shell", map[string]any{"command": goodPatch}); err != nil || !ran {
		t.Fatalf("a valid apply_patch was refused: %v", err)
	}

	// A shell command that is not a patch is not the guard's business.
	if err, ran := h.call(t, "shell", map[string]any{"command": "ls -la"}); err != nil || !ran {
		t.Fatalf("an ordinary shell command was refused: %v", err)
	}
}

// A path that only reaches the skill file through a symlink is judged like the
// file it is. The guard canonicalizes both sides for exactly this: a link is
// not an escape hatch from the invariant.
func TestSkillWriteGuardSeesThroughSymlinks(t *testing.T) {
	h := newGuardHarness(t)
	linked := filepath.Join(t.TempDir(), "linked-skill")
	if err := os.MkdirAll(filepath.Join(h.root, "release-flow"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(h.root, "release-flow"), linked); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	err, _ := h.call(t, "write_file", map[string]any{
		"file_path": filepath.Join(linked, "SKILL.md"),
		"content":   skillBody("release-flow", false),
	})
	if err == nil {
		t.Fatal("a symlinked path bypassed the guard")
	}
}

// The guard is what the generator skill meets when it writes: the file it
// produces is validated exactly like a hand-written one, and a refusal arrives
// as the parser's own words.
func TestSkillWriteGuardErrorIsTheParsersOwn(t *testing.T) {
	h := newGuardHarness(t)
	_, _ = h.call(t, "write_file", map[string]any{
		"file_path": filepath.Join(h.root, "release-flow", "SKILL.md"),
		"content":   "no frontmatter at all",
	})
	direct := skill.ValidateSkillContent([]byte("no frontmatter at all"))
	err, _ := h.call(t, "write_file", map[string]any{
		"file_path": filepath.Join(h.root, "release-flow", "SKILL.md"),
		"content":   "no frontmatter at all",
	})
	if err == nil || direct == nil {
		t.Fatal("expected both the guard and the parser to refuse this content")
	}
	if err.Error() != direct.Error() {
		t.Fatalf("guard error %q must be the parser's %q", err.Error(), direct.Error())
	}
}

// The runner used to resolve its skill roots twice: once for the catalog that
// enters the cached prompt prefix and once for discovery, with the catalog side
// re-deriving a project from the process working directory. In a gateway the
// two could name different projects, so the catalog listed one project's skills
// while the loaded catalog granted access to another's. One resolution is the
// fix, and this test pins it from the outside: the roots the runtime kept are
// the launch-derived ones, every discovered skill sits under them, and the
// frozen catalog block names the project skill.
func TestRunnerSkillRootsAgreeBetweenCatalogAndDiscovery(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	project := filepath.Join(home, "project-b")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	trustedProject, err := safety.Resolve(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := safety.MarkTrusted(home, trustedProject); err != nil {
		t.Fatal(err)
	}
	launch, err := safety.ResolveProjectContext(home, project)
	if err != nil {
		t.Fatal(err)
	}
	writeRunnerSkillFile(t, filepath.Join(project, ".forebrain", "skills", "proj-only", "SKILL.md"),
		`name: proj-only`+"\ndescription: Project-scope skill", "Project body")
	writeRunnerSkillFile(t, filepath.Join(workspace, "skills", "ws-only", "SKILL.md"),
		`name: ws-only`+"\ndescription: Workspace-scope skill", "Workspace body")

	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{
		Definitions: map[string]appcfg.AgentDefinition{
			"main": {LLMProviders: []appcfg.AgentLLMProviderConfig{{Provider: "openai", Model: "m", APIKey: "k", BaseURL: "http://127.0.0.1:9/v1"}}},
		},
	}}
	r := &Runner{Deps: &Deps{Home: home, WorkspaceRoot: workspace, AppCfg: cfg, LaunchProject: launch, ProjectRoot: project}}
	if err := r.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// The runtime resolves its roots with skill.AgentSkillRoots, once per
	// load; the project root comes first, and it is the launch project — not
	// wherever the process happens to run.
	want := skill.AgentSkillRoots(home, workspace, launch)
	if got := filepath.Clean(want[0]); got != filepath.Clean(filepath.Join(trustedProject.Root, ".forebrain", "skills")) {
		t.Fatalf("first root=%q want the launch project's .forebrain/skills", got)
	}

	// Every skill the runtime discovered lives under a root it kept, which is
	// the property that made "in the catalog but not loadable" possible.
	for _, item := range r.Tools().LoadedSkills() {
		if !underAnyRoot(item.RootDir, want) {
			t.Fatalf("discovered skill %q at %s is not under any runtime skill root %v", item.Name, item.RootDir, want)
		}
	}
	names := map[string]bool{}
	for _, item := range r.Tools().LoadedSkills() {
		names[item.Name] = true
	}
	if !names["proj-only"] || !names["ws-only"] {
		t.Fatalf("expected both the project and workspace skill to be loadable, got %v", names)
	}

	// The catalog that enters the cached prefix describes the same set: a
	// skill the model is not told about could never be chosen.
	inner := &capturingLLM{}
	wrapped := wrapSkillCatalogLLM(inner, want, workspace, newPromptStateStore(t), r.skillOfferGuidance)
	ctx := llm.WithAgentSessionID(context.Background(), "roots-session")
	if _, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil); err != nil {
		t.Fatal(err)
	}
	catalog := ""
	for _, m := range inner.gotMsgs {
		if m.Role == llm.RoleDeveloper {
			catalog = llm.TextContent(m.Parts...)
			break
		}
	}
	if !strings.Contains(catalog, "proj-only") || !strings.Contains(catalog, "ws-only") {
		t.Fatalf("frozen catalog does not describe the discovered skills:\n%s", catalog)
	}
	if !strings.Contains(catalog, filepath.Join(project, ".forebrain", "skills", "proj-only", "SKILL.md")) {
		t.Fatalf("frozen catalog does not point at the project skill's own file:\n%s", catalog)
	}
}

func underAnyRoot(path string, roots []string) bool {
	canonical := skill.CanonicalSkillPath(path)
	for _, root := range roots {
		if canonical == skill.CanonicalSkillPath(root) {
			return true
		}
		rel, err := filepath.Rel(skill.CanonicalSkillPath(root), canonical)
		if err != nil {
			continue
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
