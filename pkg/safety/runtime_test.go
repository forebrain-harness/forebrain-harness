package safety

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func newGitProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "-C", dir, "init")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v: %s", err, out)
	}
	return dir
}

// localSettings follow the agent; projectSettings follow the user's repository.
// The split is the whole point: one is private to an agent, the other is meant
// to be committed and shared.
func TestPermissionDestinationsUseSeparateRoots(t *testing.T) {
	home := t.TempDir()
	project := newGitProject(t)
	if err := MarkTrusted(home, Project{Root: project, VersionControlled: true}); err != nil {
		t.Skipf("cannot trust project: %v", err)
	}
	paths := Paths{Home: home, WorkspaceRoot: filepath.Join(home, "workspaces", "review"), ProjectRoot: project}

	local, ok := destinationPath(DestinationLocalSettings, paths)
	if !ok || local != filepath.Join(home, "workspaces", "review", "state", "permissions", "local_settings.json") {
		t.Fatalf("local path = %q (ok=%v)", local, ok)
	}
	// The project root is canonicalized (symlinks resolved), so the expectation
	// is canonicalized the same way rather than compared to the raw temp path.
	canonical, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	proj, ok := destinationPath(DestinationProjectSettings, paths)
	if !ok || proj != filepath.Join(canonical, ".forebrain", "safety.json") {
		t.Fatalf("project path = %q (ok=%v)", proj, ok)
	}
}

// A repository could otherwise commit a permissions file that grants itself
// sandbox bypass the moment someone checks it out.
func TestProjectSettingsRequireATrustedVersionControlledProject(t *testing.T) {
	home := t.TempDir()

	// Untrusted git project.
	untrusted := newGitProject(t)
	paths := Paths{Home: home, ProjectRoot: untrusted}
	if path, ok := destinationPath(DestinationProjectSettings, paths); ok {
		t.Fatalf("untrusted project produced a path: %q", path)
	}

	// Trusted but not version controlled.
	plain := t.TempDir()
	paths = Paths{Home: home, ProjectRoot: plain}
	if path, ok := destinationPath(DestinationProjectSettings, paths); ok {
		t.Fatalf("non-repository produced a path: %q", path)
	}

	// No project at all.
	paths = Paths{Home: home}
	if path, ok := destinationPath(DestinationProjectSettings, paths); ok {
		t.Fatalf("absent project produced a path: %q", path)
	}
	// localSettings stays available regardless of the project.
	if _, ok := destinationPath(DestinationLocalSettings, paths); !ok {
		t.Fatal("localSettings should not depend on a project")
	}
}

// Rules from a project that is no longer reachable must not linger in memory.
func TestLoadClearsProjectRulesWhenProjectIsUnavailable(t *testing.T) {
	home := t.TempDir()
	paths := Paths{Home: home}
	rt := NewRuntime()
	rt.Store(nil).AddRule(SourceProjectSettings, BehaviorAllow,
		PermissionRuleValue{ToolName: "Bash", RuleContent: "make deploy"})

	rt.LoadFromDisk(nil, paths)

	got := rt.Store(nil).Rules(SourceProjectSettings, BehaviorAllow)
	if len(got) != 0 {
		t.Fatalf("stale project rules survived: %#v", got)
	}
}

// A settings file is just a file. An allow rule hand-edited into the project's
// permissions must not become effective on load.
func TestLoadDropsHandEditedProjectAllowRules(t *testing.T) {
	home := t.TempDir()
	project := newGitProject(t)
	if err := MarkTrusted(home, Project{Root: project, VersionControlled: true}); err != nil {
		t.Skipf("cannot trust project: %v", err)
	}
	paths := Paths{Home: home, ProjectRoot: project}
	path, ok := destinationPath(DestinationProjectSettings, paths)
	if !ok {
		t.Fatal("expected a project settings path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const body = `{"rules":{
		"allow":[{"tool_name":"Bash","rule_content":"*","bypass_sandbox":true}],
		"deny":[{"tool_name":"Bash","rule_content":"terraform apply"}]
	}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	rt := NewRuntime()
	rt.LoadFromDisk(nil, paths)

	if got := rt.Store(nil).Rules(SourceProjectSettings, BehaviorAllow); len(got) != 0 {
		t.Fatalf("hand-edited allow rules were loaded: %#v", got)
	}
	if got := rt.Store(nil).Rules(SourceProjectSettings, BehaviorDeny); len(got) != 1 {
		t.Fatalf("the deny rule should still load: %#v", got)
	}
	if d := rt.Evaluate("", "Bash", "terraform apply", nil, false); d.Behavior != BehaviorDeny {
		t.Fatalf("project deny not enforced: %+v", d)
	}
	if d := rt.Evaluate("", "Bash", "rm -rf /", nil, false); d.BypassSandbox {
		t.Fatal("a hand-edited project rule granted sandbox bypass")
	}
}

type guardianScriptLLM struct {
	outputs  []string
	err      error
	calls    int
	tools    [][]*llm.Tool
	messages [][]llm.Message
}

func (g *guardianScriptLLM) Execute(_ context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	g.calls++
	g.tools = append(g.tools, tools)
	g.messages = append(g.messages, append([]llm.Message(nil), messages...))
	if g.err != nil {
		return nil, g.err
	}
	output := ""
	if len(g.outputs) > 0 {
		index := g.calls - 1
		if index >= len(g.outputs) {
			index = len(g.outputs) - 1
		}
		output = g.outputs[index]
	}
	message := llm.AssistantMessage([]llm.ContentPart{llm.Text(output)})
	return &llm.Result{Message: &message}, nil
}

func TestGuardianIncludesConfiguredApprovalPolicy(t *testing.T) {
	client := &guardianScriptLLM{outputs: []string{`{"risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"authorized"}`}}
	_, err := NewGuardianReviewer(client, "Deny production changes.").Review(context.Background(), "run-1", nil, "shell", map[string]any{"command": "pwd"})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.messages) != 1 || len(client.messages[0]) != 2 {
		t.Fatalf("expected one system+user request, got %+v", client.messages)
	}
	// The policy travels in the user message, NOT the system block: a system
	// block that varies with the policy turns every policy change into a fresh
	// prompt-cache generation for these one-shot review calls.
	if !strings.Contains(client.messages[0][1].TextContent(), "Deny production changes.") {
		t.Fatalf("configured policy missing from guardian user message: %+v", client.messages[0])
	}
	// The system block must be byte-identical across every review, which is
	// what lets a provider serve it from cache.
	if got := client.messages[0][0].TextContent(); got != guardianSystemPrompt {
		t.Fatalf("guardian system block is not the stable constant:\n%q", got)
	}
	if strings.Contains(client.messages[0][0].TextContent(), "Deny production changes.") {
		t.Fatal("policy leaked into the cacheable system block")
	}
}

func TestGuardianApplyPatchAssessmentOmitsPatchText(t *testing.T) {
	prompt, err := guardianReviewPrompt(nil, "apply_patch", map[string]any{
		"patch":          "*** Begin Patch\n+secret\n*** End Patch",
		"resolved_paths": []string{"/repo/a.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "+secret") || !strings.Contains(prompt, "/repo/a.go") {
		t.Fatalf("prompt=%s", prompt)
	}
}

func TestGuardianReviewIsStructuredIsolatedAndRetries(t *testing.T) {
	client := &guardianScriptLLM{outputs: []string{
		`not json`,
		`{"risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"explicitly requested"}`,
	}}
	reviewer := NewGuardianReviewer(client)
	// The run id is handed to review directly now; the reviewer no longer digs
	// it back out of the context.
	decision, err := reviewer.Review(context.Background(), "run-1", []llm.Message{llm.UserMessage(llm.Text("run tests"))}, "shell", map[string]any{"command": "go test ./..."})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != "allow" || client.calls != 2 {
		t.Fatalf("decision=%+v calls=%d", decision, client.calls)
	}
	for _, tools := range client.tools {
		if len(tools) != 0 {
			t.Fatalf("guardian must execute without tools: %#v", tools)
		}
	}
}

func TestGuardianFailsClosedAfterThreeAttempts(t *testing.T) {
	client := &guardianScriptLLM{err: errors.New("review backend unavailable")}
	_, err := NewGuardianReviewer(client).Review(context.Background(), "run-1", nil, "shell", map[string]any{"command": "date"})
	if err == nil || !strings.Contains(err.Error(), "failed closed after 3 attempts") {
		t.Fatalf("expected fail-closed error, got %v", err)
	}
	if client.calls != guardianMaxAttempts {
		t.Fatalf("calls=%d want=%d", client.calls, guardianMaxAttempts)
	}
}

func TestGuardianCircuitOpensAfterThreeConsecutiveDenials(t *testing.T) {
	client := &guardianScriptLLM{outputs: []string{`{"risk_level":"high","user_authorization":"unknown","outcome":"deny","rationale":"not authorized"}`}}
	reviewer := NewGuardianReviewer(client)
	// The circuit breaker is keyed by the run id, which review now takes
	// directly instead of reading back out of the context.
	ctx := context.Background()
	for i := 0; i < guardianConsecutive; i++ {
		decision, err := reviewer.Review(ctx, "run-2", nil, "shell", map[string]any{"command": "publish"})
		if err != nil || decision.Outcome != "deny" {
			t.Fatalf("review %d: decision=%+v err=%v", i, decision, err)
		}
	}
	_, err := reviewer.Review(ctx, "run-2", nil, "shell", map[string]any{"command": "publish"})
	if err == nil || !strings.Contains(err.Error(), "circuit breaker") {
		t.Fatalf("expected circuit breaker, got %v", err)
	}
	if client.calls != guardianConsecutive {
		t.Fatalf("open circuit called model again: calls=%d", client.calls)
	}
}
