package run

import (
	"context"
	"encoding/json"
	"errors"
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
	wrapped := wrapSkillCatalogLLM(inner, []string{root}, t.TempDir(), newPromptStateStore(t))
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
	wrapped := wrapSkillCatalogLLM(inner, []string{root}, t.TempDir(), sessions)
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
	if _, err := wrapSkillCatalogLLM(before, []string{root}, stateRoot, sessions).Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("one"))}, nil); err != nil {
		t.Fatal(err)
	}

	// The process goes away and a skill is installed while it is down; the
	// same session then resumes against a brand-new runtime.
	writeCatalogSkill(t, root, "second-skill", "Installed while the process was down")
	after := &catalogCapturingLLM{}
	if _, err := wrapSkillCatalogLLM(after, []string{root}, stateRoot, sessions).Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("two"))}, nil); err != nil {
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
	wrapped := wrapSkillCatalogLLM(inner, []string{root}, t.TempDir(), nil)
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

// A new session's frozen catalog carries the installed skills and nothing
// else. The same session's second request is checked too: the freeze must not
// reintroduce a stale guidance section on a later turn.
func TestSkillCatalogFreezesWithoutProposalGuidance(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "first-skill", "Present at session start")
	sessions := newPromptStateStore(t)
	if err := sessions.Ensure(context.Background(), "s1", "first"); err != nil {
		t.Fatal(err)
	}
	inner := &catalogCapturingLLM{}
	wrapped := wrapSkillCatalogLLM(inner, []string{root}, t.TempDir(), sessions)
	ctx := llm.WithAgentSessionID(context.Background(), "s1")
	if _, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("one"))}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("two"))}, nil); err != nil {
		t.Fatal(err)
	}
	for i, got := range inner.developer {
		if !strings.Contains(got, "first-skill") {
			t.Fatalf("request %d lost the skill catalog:\n%s", i+1, got)
		}
		if strings.Contains(got, "Keeping "+"finished work") {
			t.Fatalf("request %d froze deleted guidance into the catalog:\n%s", i+1, got)
		}
	}
}

func TestSkillCatalogIsSilentWithoutSkills(t *testing.T) {
	inner := &catalogCapturingLLM{}
	wrapped := wrapSkillCatalogLLM(inner, []string{t.TempDir()}, t.TempDir(), newPromptStateStore(t))
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

	r := &Runner{Deps: (&Deps{Home: home}).WithConfig(&appcfg.Root{LLMModels: modelRowsForTest(appcfg.AgentLLMProviderConfig{
		Provider: "openai",
		Model:    "qwen-test",
		APIKey:   "test-key",
		BaseURL:  "http://127.0.0.1:9/v1",
	}),
		Agents: appcfg.AgentsSection{
			Definitions: map[string]appcfg.AgentDefinition{
				"main": {
					DefaultModel: "llm-1",
				},
			},
		},
	})}
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

// The activation message fixes what this turn is for. A skill whose own
// workflow produces plans otherwise loses its deliverable to a plan-mode turn
// the model asked for on its own.
func TestExplicitSkillActivationMessageNamesTheTurnContract(t *testing.T) {
	body := "<skill>\n<name>review</name>\n<path>/skills/review/SKILL.md</path>\nReview carefully.\n</skill>"
	msg := explicitSkillActivationMessage(explicitSkillActivation{SkillName: "review", Content: body})

	text := msg.TextContent()
	for _, want := range []string{"this turn's contract", "session's current mode"} {
		if !strings.Contains(text, want) {
			t.Fatalf("message does not name the turn contract (%q):\n%s", want, text)
		}
	}
	if !strings.Contains(text, body) {
		t.Fatalf("message no longer carries the skill body verbatim:\n%s", text)
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

	cfg := &appcfg.Root{LLMModels: modelRowsForTest(appcfg.AgentLLMProviderConfig{Provider: "openai", Model: "m", APIKey: "k", BaseURL: "http://127.0.0.1:9/v1"}), Agents: appcfg.AgentsSection{
		Definitions: map[string]appcfg.AgentDefinition{
			"main": {DefaultModel: "llm-1"},
		},
	}}
	r := &Runner{Deps: (&Deps{Home: home, WorkspaceRoot: workspace, LaunchProject: launch, ProjectRoot: project}).WithConfig(cfg)}
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
	wrapped := wrapSkillCatalogLLM(inner, want, workspace, newPromptStateStore(t))
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

// --- Skill versions: the session's pinned loads, and the prefix's integrity.

// TestSkillToolLoadsTheSessionPinnedVersion pins the session rule: a runtime
// that discovered a skill at v1 keeps reading v1's own copy after v2 is
// published, and the loaded context names the version directory it read.
func TestSkillToolLoadsTheSessionPinnedVersion(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "gateway-ops", "runbook")
	store := skill.VersionStore{Root: root}
	if _, err := store.Bootstrap("gateway-ops", skill.VersionRecord{Kind: skill.VersionKindCreated}); err != nil {
		t.Fatal(err)
	}
	draft, err := store.EnsureDraft("gateway-ops")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(draft, "SKILL.md"), []byte("---\nname: gateway-ops\ndescription: runbook\n---\n\nv2 body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish("gateway-ops", "v2"); err != nil {
		t.Fatal(err)
	}
	// The formal directory now holds v2; this session is pinned to v1.
	formal, err := os.ReadFile(filepath.Join(root, "gateway-ops", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(formal), "v2 body") {
		t.Fatalf("formal directory does not hold v2: %q", formal)
	}

	st := tool.NewState(root)
	st.ReplaceLoadedSkillCatalog([]tool.LoadedSkill{{
		Name:         "gateway-ops",
		RootDir:      filepath.Join(root, "gateway-ops"),
		Version:      1,
		VersionsRoot: filepath.Join(root, ".versions", "gateway-ops"),
	}})
	collector := &toolCollector{}
	if err := RegisterSkillTool(collector, st); err != nil {
		t.Fatal(err)
	}
	exec, err := collector.byName("skill").Execute(context.Background(), `{"name":"gateway-ops"}`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out, _ := exec.Result.(string)
	if !strings.Contains(out, "body") || strings.Contains(out, "v2 body") {
		t.Fatalf("session read the wrong version:\n%s", out)
	}
	pinnedPath := filepath.Join(root, ".versions", "gateway-ops", "v1", "SKILL.md")
	if !strings.Contains(out, pinnedPath) {
		t.Fatalf("loaded context does not name the version directory:\n%s", out)
	}
}

// TestSkillToolErrorsWhenPinnedVersionIsGone pins the no-fallback rule: a
// pinned version directory that has disappeared is an error naming the
// version, never a silent read of the formal directory.
func TestSkillToolErrorsWhenPinnedVersionIsGone(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "gateway-ops", "runbook")
	st := tool.NewState(root)
	st.ReplaceLoadedSkillCatalog([]tool.LoadedSkill{{
		Name:         "gateway-ops",
		RootDir:      filepath.Join(root, "gateway-ops"),
		Version:      7,
		VersionsRoot: filepath.Join(root, ".versions", "gateway-ops"),
	}})
	collector := &toolCollector{}
	if err := RegisterSkillTool(collector, st); err != nil {
		t.Fatal(err)
	}
	_, err := collector.byName("skill").Execute(context.Background(), `{"name":"gateway-ops"}`)
	if err == nil || !strings.Contains(err.Error(), "v7") {
		t.Fatalf("missing pinned version must error naming it: %v", err)
	}
}

// TestSkillCatalogBytesIgnoreVersions is the prompt-cache lock (STOP 2): a
// skill's version history and drafts never change the catalog's bytes — the
// catalog is part of the cached prefix, and the prefix may only change
// between sessions, never by a publish.
func TestSkillCatalogBytesIgnoreVersions(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "gateway-ops", "runbook")
	before, err := skill.Catalog([]string{root}, "")
	if err != nil {
		t.Fatal(err)
	}
	store := skill.VersionStore{Root: root}
	if _, err := store.Bootstrap("gateway-ops", skill.VersionRecord{Kind: skill.VersionKindCreated}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureDraft("gateway-ops"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish("gateway-ops", "a version the session never sees"); err != nil {
		t.Fatal(err)
	}
	after, err := skill.Catalog([]string{root}, "")
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("catalog bytes changed across a publish:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if strings.Contains(after, ".versions") || strings.Contains(after, "v1") || strings.Contains(after, ".drafts") {
		t.Fatalf("catalog leaks version storage:\n%s", after)
	}
}

// writeUserOnlySkill installs a skill whose SKILL.md declares
// `model-invocation: false`: the user reaches it through its slash command, the
// model is never offered it.
func writeUserOnlySkill(t *testing.T, root, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + desc + "\nmodel-invocation: false\n---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSkillToolRefusesAUserOnlySkillByName pins that a user-only skill is
// indistinguishable, through the skill tool, from a name that was never
// installed. The identical wording is the requirement: a distinct "that skill
// is user-invocable only" message would confirm to the model that the skill
// exists and is reachable by another route, which is exactly what the catalog
// filter that keeps it out of the prompt is hiding.
func TestSkillToolRefusesAUserOnlySkillByName(t *testing.T) {
	root := t.TempDir()
	writeCatalogSkill(t, root, "ordinary", "an ordinary skill")
	writeUserOnlySkill(t, root, "repo-writer", "writes files into the repository")

	st := tool.NewState(root)
	st.ReplaceLoadedSkillCatalog([]tool.LoadedSkill{
		{Name: "ordinary", RootDir: filepath.Join(root, "ordinary")},
		{Name: "repo-writer", RootDir: filepath.Join(root, "repo-writer")},
	})
	collector := &toolCollector{}
	if err := RegisterSkillTool(collector, st); err != nil {
		t.Fatal(err)
	}
	exec := collector.byName("skill")

	if _, err := exec.Execute(context.Background(), `{"name":"ordinary"}`); err != nil {
		t.Fatalf("an ordinary skill must still load: %v", err)
	}
	_, presentErr := exec.Execute(context.Background(), `{"name":"repo-writer"}`)
	if presentErr == nil {
		t.Fatal("a user-only skill loaded through the skill tool")
	}
	_, absentErr := exec.Execute(context.Background(), `{"name":"never-installed"}`)
	if absentErr == nil {
		t.Fatal("an unknown name must be refused")
	}
	// The message embeds the name that was asked for, so the two refusals are
	// compared with the name blanked out: one wording, one sentence, no hint
	// that the skill exists.
	blankName := func(err error, name string) string {
		return strings.Replace(err.Error(), `"`+name+`"`, `"<name>"`, 1)
	}
	got := blankName(presentErr, "repo-writer")
	if want := blankName(absentErr, "never-installed"); got != want {
		t.Fatalf("the two refusals differ, which tells the model the skill exists:\nuser-only: %q\nunknown:   %q", got, want)
	}
	if got != `no skill named "<name>" is available in this session` {
		t.Fatalf("the user-only refusal is not the unknown-name message: %q", got)
	}
}

// TestLoadActivationKeepsTheUserPathForAUserOnlySkill pins that
// `model-invocation: false` hides the skill from the model and from nothing
// else: the explicit slash invocation injects Content directly and never
// consults the flag, so the command must survive.
func TestLoadActivationKeepsTheUserPathForAUserOnlySkill(t *testing.T) {
	root := t.TempDir()
	writeUserOnlySkill(t, root, "repo-writer", "writes files into the repository")

	activation, err := skill.LoadActivation(filepath.Join(root, "repo-writer", "SKILL.md"))
	if err != nil {
		t.Fatalf("LoadActivation: %v", err)
	}
	if !activation.SlashCommand {
		t.Fatal("a user-only skill must keep its slash command")
	}
	if activation.ModelInvocation {
		t.Fatal("model-invocation: false must reach the activation")
	}
	if !strings.Contains(activation.Content, filepath.Join(root, "repo-writer", "SKILL.md")) {
		t.Fatalf("the explicit invocation lost its context:\n%s", activation.Content)
	}
}

// skillPublishFixture is a service over a fresh home holding one draft, plus the
// versioned root the draft sits in, so a test can both drive the tool and see
// whether a publish reached the disk.
func skillPublishFixture(t *testing.T, name string) (*skill.Service, string) {
	t.Helper()
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	root := filepath.Join(workspace, "skills")
	dir := filepath.Join(root, ".drafts", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: A skill the model drafted\n---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := skill.NewServiceForWorkspace(home, workspace)
	if err := svc.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return svc, root
}

// skillPublishState is the skill's version state as the disk holds it.
func skillPublishState(t *testing.T, root, name string) skill.SkillVersionState {
	t.Helper()
	state, err := skill.VersionStore{Root: root}.State(name)
	if err != nil {
		t.Fatalf("version state: %v", err)
	}
	return state
}

// registerSkillPublishToolForTest registers the tool against a collector and a
// state whose action hook is hook (nil leaves the state without one).
func registerSkillPublishToolForTest(t *testing.T, svc *skill.Service, hook func(ctx context.Context, kind string, payload any) (string, bool, error)) (*llm.Tool, *tool.State) {
	t.Helper()
	st := tool.NewState()
	if hook != nil {
		st.SetActionHook(hook)
	}
	collector := &toolCollector{}
	if err := RegisterSkillPublishTool(collector, st, svc); err != nil {
		t.Fatalf("RegisterSkillPublishTool: %v", err)
	}
	publishTool := collector.byName("skill_publish")
	if publishTool == nil {
		t.Fatal("skill_publish was not registered")
	}
	return publishTool, st
}

// approvalRaisingHook counts the questions it was asked and answers each with a
// pending action id.
type approvalRaisingHook struct {
	asked  int
	kind   string
	last   map[string]any
	action string
}

func (h *approvalRaisingHook) hook(_ context.Context, kind string, payload any) (string, bool, error) {
	h.asked++
	h.kind = kind
	h.last, _ = payload.(map[string]any)
	if h.action == "" {
		h.action = "action-skill-publish"
	}
	return h.action, true, nil
}

func (h *approvalRaisingHook) called() int { return h.asked }

// A name no draft stands behind is refused before anyone is asked: the question
// would be about a publish validateDraftForPublish then refuses, and the answer
// it teaches the user to give is "no".
func TestSkillPublishRefusesANameWithNoDraft(t *testing.T) {
	svc, root := skillPublishFixture(t, "publish-me")
	hook := &approvalRaisingHook{}
	publishTool, _ := registerSkillPublishToolForTest(t, svc, hook.hook)

	exec, err := publishTool.Execute(context.Background(), `{"name":"never-drafted","note":"first release"}`)
	if err == nil {
		t.Fatalf("publishing a skill with no draft must fail: %#v", exec.Result)
	}
	if got := err.Error(); got != `no skill named "never-drafted" is available in this session` {
		t.Fatalf("refusal = %q, want the missing-skill register", got)
	}
	if hook.called() != 0 {
		t.Fatalf("the hook was asked %d times; nothing may be asked before the draft exists", hook.called())
	}
	if state := skillPublishState(t, root, "never-drafted"); state.Current != 0 {
		t.Fatalf("version state = %+v, want nothing published", state)
	}
}

// The approval is the tool's substance: it names the skill, the note the user is
// accepting, the draft that would release, and the fact that the release is the
// next session's. Approving it publishes exactly that, and the answer says when
// the skill becomes usable.
func TestSkillPublishAsksThenPublishesOnApproval(t *testing.T) {
	svc, root := skillPublishFixture(t, "publish-me")
	hook := &approvalRaisingHook{}
	publishTool, _ := registerSkillPublishToolForTest(t, svc, hook.hook)

	_, err := publishTool.Execute(context.Background(), `{"name":"publish-me","note":"first release"}`)
	var rae *tool.RequiresActionError
	if !errors.As(err, &rae) || rae == nil {
		t.Fatalf("err = %v, want the approval gate", err)
	}
	if rae.ActionKind != "skill_publish" || rae.ToolName != "skill_publish" || rae.ActionID != hook.action {
		t.Fatalf("gate = %+v, want one skill_publish gate carrying %q", rae, hook.action)
	}
	if hook.kind != "skill_publish" {
		t.Fatalf("hook kind = %q, want skill_publish", hook.kind)
	}
	for _, field := range []string{"name", "note", "draft_path", "justification"} {
		if strings.TrimSpace(stringFromTestAny(hook.last[field])) == "" {
			t.Fatalf("approval payload has no %s: %+v", field, hook.last)
		}
	}
	if !strings.Contains(stringFromTestAny(hook.last["justification"]), "next session") {
		t.Fatalf("the question does not say when the skill takes effect: %v", hook.last["justification"])
	}
	if !strings.HasSuffix(stringFromTestAny(hook.last["draft_path"]), filepath.Join(".drafts", "publish-me", "SKILL.md")) {
		t.Fatalf("draft_path = %q, want the draft that would publish", hook.last["draft_path"])
	}
	if state := skillPublishState(t, root, "publish-me"); state.Current != 0 {
		t.Fatalf("asking published something: %+v", state)
	}

	// The approved replay is the same call, answered.
	exec, err := publishTool.Execute(tool.WithApprovedActionID(context.Background(), rae.ActionID), `{"name":"publish-me","note":"first release"}`)
	if err != nil {
		t.Fatalf("approved replay: %v", err)
	}
	out, _ := exec.Result.(string)
	if !strings.Contains(out, "v1") {
		t.Fatalf("result does not name the version it published:\n%s", out)
	}
	if !strings.Contains(out, "next session") {
		t.Fatalf("result does not say when the skill takes effect:\n%s", out)
	}
	state := skillPublishState(t, root, "publish-me")
	if state.Current != 1 || len(state.Versions) != 1 || state.Versions[0].Note != "first release" {
		t.Fatalf("published state = %+v, want v1 with the note the user accepted", state)
	}
	if state.HasDraft {
		t.Fatal("the draft must be consumed by the publish")
	}
}

// A refusal without words has one answer, and it is not "publish anyway": the
// gate reaches the in-place approval dispatcher, the refusal stops the turn, and
// nothing was written.
func TestSkillPublishRefusalWithoutWordsStopsTheTurnAndWritesNothing(t *testing.T) {
	const sid = "skill-publish-refused"
	svc, root := skillPublishFixture(t, "publish-me")
	st := tool.NewState()
	streamHookRunner(t, sid, st)
	collector := &toolCollector{}
	if err := RegisterSkillPublishTool(collector, st, svc); err != nil {
		t.Fatalf("RegisterSkillPublishTool: %v", err)
	}
	publishTool := collector.byName("skill_publish")
	refuse := func(context.Context, *tool.RequiresActionError) (context.Context, error) {
		return nil, tool.ErrApprovalRejectedStop
	}
	inner := &inlineHookLLM{attempts: []func(ctx context.Context) (*llm.Result, error){
		func(context.Context) (*llm.Result, error) {
			call := llm.ToolCall{ID: "call-publish", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{
				Name: "skill_publish", Arguments: `{"name":"publish-me","note":"first release"}`,
			}}
			msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("publishing")}, call)
			return &llm.Result{Message: &msg, Usage: &llm.Usage{}}, nil
		},
	}}
	w := wrapToolOrchestrationLLM(inner, st)
	capture := NewPartialSessionCapture()
	_, err := w.Execute(WithPartialSessionCapture(inlineApprovalCtx(sid, "run-skill-publish", refuse), capture),
		[]llm.Message{llm.UserMessage(llm.Text("go"))}, []*llm.Tool{publishTool})
	if !errors.Is(err, ErrTurnStoppedByRejection) {
		t.Fatalf("execute err = %v, want ErrTurnStoppedByRejection", err)
	}
	if inner.callCount() != 1 {
		t.Fatalf("model calls = %d, want 1: a wordless refusal ends the turn", inner.callCount())
	}
	snap := capture.Snapshot()
	if len(snap) == 0 || snap[len(snap)-1].TextContent() != tool.RejectedStopText {
		t.Fatalf("the refused call's result is not the stop sentence: %#v", snap)
	}
	if state := skillPublishState(t, root, "publish-me"); state.Current != 0 {
		t.Fatalf("a refused publish wrote v%d", state.Current)
	}
}

// With no action hook there is nobody to ask, and the tool refuses rather than
// publishing unattended: an embedder with no approval path gets a refusal, the
// same answer the protected-read gate gives.
func TestSkillPublishRefusesWhenNothingCanBeAsked(t *testing.T) {
	svc, root := skillPublishFixture(t, "publish-me")
	publishTool, _ := registerSkillPublishToolForTest(t, svc, nil)

	_, err := publishTool.Execute(context.Background(), `{"name":"publish-me","note":"first release"}`)
	if err == nil {
		t.Fatal("an approval nobody can be asked must refuse the call")
	}
	if !strings.Contains(err.Error(), "no one to ask") {
		t.Fatalf("refusal = %q, want it to say there is nobody to ask", err)
	}
	if state := skillPublishState(t, root, "publish-me"); state.Current != 0 {
		t.Fatalf("an unasked publish wrote v%d", state.Current)
	}
}

// The note is the line the user reads in the approval, so it is required: a
// version history of empty notes tells nobody why a skill changed.
func TestSkillPublishRefusesAnEmptyNoteBeforeAsking(t *testing.T) {
	svc, root := skillPublishFixture(t, "publish-me")
	hook := &approvalRaisingHook{}
	publishTool, _ := registerSkillPublishToolForTest(t, svc, hook.hook)

	for _, args := range []string{`{"name":"publish-me"}`, `{"name":"publish-me","note":"   "}`} {
		if _, err := publishTool.Execute(context.Background(), args); err == nil {
			t.Fatalf("%s: an empty note must refuse the call", args)
		} else if !strings.Contains(err.Error(), "note is required") {
			t.Fatalf("%s: refusal = %q, want it to name the note", args, err)
		}
	}
	if hook.called() != 0 {
		t.Fatalf("the hook was asked %d times; an empty note is refused before asking", hook.called())
	}
	if state := skillPublishState(t, root, "publish-me"); state.Current != 0 {
		t.Fatalf("an empty note published v%d", state.Current)
	}
}

// A runtime with no skill service and a name with no draft refuse in one voice:
// the model can do nothing different about either, and a distinct message would
// describe the machine's wiring to a model that cannot act on it.
func TestSkillPublishWithoutAServiceRefusesLikeAMissingSkill(t *testing.T) {
	hook := &approvalRaisingHook{}
	publishTool, _ := registerSkillPublishToolForTest(t, nil, hook.hook)

	_, err := publishTool.Execute(context.Background(), `{"name":"publish-me","note":"first release"}`)
	if err == nil {
		t.Fatal("a missing skill service must refuse the call")
	}
	if got, want := err.Error(), `no skill named "publish-me" is available in this session`; got != want {
		t.Fatalf("refusal = %q, want %q", got, want)
	}
	if hook.called() != 0 {
		t.Fatalf("the hook was asked %d times with no service to publish through", hook.called())
	}
}

// A batch conversation runs unattended, so it is never offered the tool that
// exists to ask a person a question; a coding conversation is.
func TestSkillPublishIsNotRegisteredForABatchConversation(t *testing.T) {
	cfg := &appcfg.Root{
		LLMModels: modelRowsForTest(appcfg.AgentLLMProviderConfig{
			Provider: "openai", Model: "qwen-test", APIKey: "test-key", BaseURL: "http://127.0.0.1:9/v1",
		}),
		Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
			"main": {DefaultModel: "llm-1"},
		}},
	}
	loaded := func(projectType string) map[string]bool {
		r := &Runner{Deps: (&Deps{Home: t.TempDir(), AgentName: "main", ProjectType: projectType}).WithConfig(cfg)}
		if err := r.Load(); err != nil {
			t.Fatalf("Load(%s): %v", projectType, err)
		}
		names := map[string]bool{}
		for _, entry := range r.LoadedTools() {
			if entry != nil {
				names[entry.Name()] = true
			}
		}
		return names
	}
	if coding := loaded(state.ProjectTypeCoding); !coding["skill_publish"] {
		t.Fatalf("a coding conversation must offer skill_publish; it has %v", coding)
	}
	if batch := loaded(state.ProjectTypeBatch); batch["skill_publish"] {
		t.Fatalf("a batch conversation must not offer skill_publish; it has %v", batch)
	}
}

// A subagent never sees the tool and is refused if it calls it anyway: the
// approval a publish needs belongs to the user, and a subagent's approval
// surface is the parent agent.
func TestSkillPublishIsWithheldFromSubagents(t *testing.T) {
	svc, _ := skillPublishFixture(t, "publish-me")
	hook := &approvalRaisingHook{}
	publishTool, st := registerSkillPublishToolForTest(t, svc, hook.hook)

	plain, err := llm.NewTool("read_only_probe", "reads", func(context.Context, *struct{}) (string, error) { return "ok", nil })
	if err != nil {
		t.Fatal(err)
	}
	subCtx := tool.WithSubagentRun(context.Background(), true)
	kept := dropSubagentManagementTools(subCtx, []*llm.Tool{publishTool, plain})
	if len(kept) != 1 || kept[0].Name() != "read_only_probe" {
		t.Fatalf("a subagent's tool set kept %v, want only the ordinary tool", toolNamesForPublishTest(kept))
	}
	if err := st.GuardTool(subCtx, "skill_publish"); err == nil {
		t.Fatal("GuardTool must refuse a primary-only tool inside a subagent")
	}
}

// The replay already carries the user's answer, so it must not put the question
// a second time.
func TestSkillPublishDoesNotAskTwiceOnAnApprovedReplay(t *testing.T) {
	svc, root := skillPublishFixture(t, "publish-me")
	hook := &approvalRaisingHook{}
	publishTool, _ := registerSkillPublishToolForTest(t, svc, hook.hook)

	ctx := tool.WithApprovedActionID(context.Background(), "action-skill-publish")
	exec, err := publishTool.Execute(ctx, `{"name":"publish-me","note":"first release"}`)
	if err != nil {
		t.Fatalf("approved replay: %v", err)
	}
	if hook.called() != 0 {
		t.Fatalf("the approved replay asked %d times, want 0", hook.called())
	}
	if out, _ := exec.Result.(string); !strings.Contains(out, "v1") {
		t.Fatalf("replay result = %q, want the published version", out)
	}
	if state := skillPublishState(t, root, "publish-me"); state.Current != 1 {
		t.Fatalf("version state = %+v, want v1", state)
	}
}

func toolNamesForPublishTest(tools []*llm.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, entry := range tools {
		if entry != nil {
			names = append(names, entry.Name())
		}
	}
	return names
}

func stringFromTestAny(value any) string {
	text, _ := value.(string)
	return text
}
