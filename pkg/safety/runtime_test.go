package safety

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
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

// Full access is the YOLO posture (plan 203): the same decisions come out of a
// full-access preset as out of FOREBRAIN_YOLO=1, in a trusted project and in
// one the user rejected — the trust decision never narrows full access. Only an
// explicit deny rule still refuses.
func TestFullAccessDecisionsMatchYOLOAcrossTrustDecisions(t *testing.T) {
	trusted := ProjectContext{Project: Project{Root: t.TempDir(), VersionControlled: true}, TrustLevel: LevelTrusted}
	rejected := ProjectContext{Project: Project{Root: t.TempDir(), VersionControlled: true}, TrustLevel: LevelUntrusted}
	calls := []struct{ tool, input string }{
		{"shell", "make release"},
		{"shell", "shutdown now"},
		{"edit_file", "/outside/project.go"},
	}

	newRuntime := func() *Runtime {
		rt := NewRuntime()
		store := rt.Store(nil)
		store.AddRule(SourceProjectSettings, BehaviorAsk, PermissionRuleValue{ToolName: "Bash", RuleContent: "make release"})
		store.AddRule(SourceProjectSettings, BehaviorDeny, PermissionRuleValue{ToolName: "Bash", RuleContent: "shutdown:*"})
		return rt
	}

	for _, launch := range []ProjectContext{trusted, rejected} {
		t.Setenv(EnvYOLO, "1")
		yoloRuntime := newRuntime()
		cfg := appcfg.Root{}
		flagged := make([]Decision, 0, len(calls))
		for _, call := range calls {
			flagged = append(flagged, yoloRuntime.Evaluate("", call.tool, call.input, &cfg, RuntimeYOLOEnabled()))
		}

		t.Setenv(EnvYOLO, "")
		presetRuntime := newRuntime()
		presetCfg := EffectiveConfig(appcfg.Root{ApprovalPreset: appcfg.ApprovalPresetFullAccess}, launch)
		if presetCfg.ApprovalPolicy.Mode != appcfg.ApprovalPolicyNever || presetCfg.SandboxMode != appcfg.SandboxModeDangerFullAccess {
			t.Fatalf("%s: derived config = %+v", launch.TrustLevel, presetCfg)
		}
		for i, call := range calls {
			got := presetRuntime.Evaluate("", call.tool, call.input, &presetCfg, presetCfg.DangerFullAccessEnabled())
			want := flagged[i]
			if got.Behavior != want.Behavior || got.Reason != want.Reason || got.BypassSandbox != want.BypassSandbox {
				t.Fatalf("%s: %s %q = %+v, yolo = %+v", launch.TrustLevel, call.tool, call.input, got, want)
			}
		}

		// The explicit deny rule is the one thing full access does not erase,
		// and the ask rule is allowed rather than refused.
		denied := presetRuntime.Evaluate("", "shell", "shutdown now", &presetCfg, presetCfg.DangerFullAccessEnabled())
		if denied.Behavior != BehaviorDeny {
			t.Fatalf("%s: an explicit deny rule was erased: %+v", launch.TrustLevel, denied)
		}
		asked := presetRuntime.Evaluate("", "shell", "make release", &presetCfg, presetCfg.DangerFullAccessEnabled())
		if asked.Behavior != BehaviorAllow || asked.Reason != ReasonYOLO {
			t.Fatalf("%s: an ask rule was not allowed: %+v", launch.TrustLevel, asked)
		}
	}
}
