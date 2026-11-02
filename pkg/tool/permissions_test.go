package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// The permission-request tool is offered to every interactive conversation and
// to none of a batch task's (plan 203 fixed its switch on; the batch rule is
// plans/150's). A batch task runs unattended, so a tool whose whole purpose is
// to ask a person cannot be there.
func TestRequestPermissionsRegistration(t *testing.T) {
	newAgent := func(t *testing.T) *agent.Agent {
		t.Helper()
		a, err := agent.New(noopLLM{}, "test", "test")
		if err != nil {
			t.Fatal(err)
		}
		return a
	}

	st := NewState(t.TempDir())
	if err := RegisterDefaultTools(newAgent(t), st, &AgentToolRuntime{Cfg: &appcfg.Root{}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.ToolMetaByName("request_permissions"); !ok {
		t.Fatal("request_permissions missing for an interactive conversation")
	}

	batch := NewState(t.TempDir())
	if err := RegisterDefaultTools(newAgent(t), batch, &AgentToolRuntime{Cfg: &appcfg.Root{}, ProjectType: state.ProjectTypeBatch}); err != nil {
		t.Fatal(err)
	}
	if _, ok := batch.ToolMetaByName("request_permissions"); ok {
		t.Fatal("request_permissions registered for an unattended batch task")
	}
}

// The shell schema offers sandbox-external permissions unconditionally now:
// there is no switch to turn them off, so a model that needs a path outside the
// sandbox can always ask for it through the approval prompt.
func TestShellAdditionalPermissionsAreAlwaysOffered(t *testing.T) {
	shell, err := NewShellTool(NewState(t.TempDir()), &AgentToolRuntime{Cfg: &appcfg.Root{}})
	if err != nil {
		t.Fatal(err)
	}
	properties, _ := shell.InputSchema()["properties"].(map[string]any)
	if _, ok := properties["additional_permissions"]; !ok {
		t.Fatal("additional_permissions missing from the shell schema")
	}
	ctx := WithProjectRoot(context.Background(), t.TempDir())
	in := &ShellInput{
		Command:               "true",
		SandboxPermissions:    safety.SandboxPermissionsWithAdditionalPermissions,
		AdditionalPermissions: &safety.RequestPermissionProfile{Network: safety.NewNetworkPermissionProfile(true)},
	}
	if err := validateShellAdditionalPermissions(ctx, in, &AgentToolRuntime{Cfg: &appcfg.Root{}}); err != nil {
		t.Fatalf("with_additional_permissions refused: %v", err)
	}
}

// A granted path outside the workspace is readable and writable by the
// in-process file tools: the grant is what carries the permission now that
// permission profiles are gone (plan 203).
func TestGrantedPathsAreAvailableToInProcessFileTools(t *testing.T) {
	project := t.TempDir()
	external := t.TempDir()
	readPath := filepath.Join(external, "read.txt")
	writePath := filepath.Join(external, "write.txt")
	if err := os.WriteFile(readPath, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry := func(path string, access safety.FileSystemPermissionAccess) safety.FileSystemPermissionEntry {
		return safety.FileSystemPermissionEntry{Path: safety.NewFileSystemPermissionPath(path), Access: access}
	}
	snap := safety.Snapshot{FileSystemGrants: []safety.FileSystemPermissionGrant{
		{Entry: entry(readPath, safety.FileSystemAccessRead), Scope: safety.GrantScopeSession},
		{Entry: entry(writePath, safety.FileSystemAccessWrite), Scope: safety.GrantScopeSession},
	}}
	st := NewState(project)
	ApplyFilesystemPolicy(st, project, project, &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite}, snap)
	if got, err := resolveReadFilePath(context.Background(), st, readPath); err != nil || got.Abs != readPath {
		t.Fatalf("read path=%q err=%v", got.Abs, err)
	}
	if got, err := resolveWriteFilePath(context.Background(), st, writePath); err != nil || got != writePath {
		t.Fatalf("write path=%q err=%v", got, err)
	}
}

// TestBuiltinSubagentToolsTier pins the tool tier each built-in type reports to
// the surfaces that describe one without running it. The tier is the type's own
// policy — that is the whole point of deriving it rather than writing a second
// list — so this test names every built-in there is: a new one fails here
// instead of quietly being described as fully tooled.
func TestBuiltinSubagentToolsTier(t *testing.T) {
	want := map[string]string{
		"general-purpose":       appcfg.SubagentToolsEdit,
		"explore":               appcfg.SubagentToolsReadOnly,
		"plan":                  appcfg.SubagentToolsReadOnly,
		"verification":          appcfg.SubagentToolsReadOnly,
		"plan-reviewer":         appcfg.SubagentToolsReadOnly,
		"goal-evaluator":        appcfg.SubagentToolsReadOnly,
		"guardian":              SubagentToolsNone,
		"cavecrew-investigator": appcfg.SubagentToolsReadOnly,
		"cavecrew-builder":      appcfg.SubagentToolsEdit,
		"cavecrew-reviewer":     appcfg.SubagentToolsReadOnly,
	}
	for name, tier := range want {
		if got := BuiltinSubagentToolsTier(name); got != tier {
			t.Errorf("BuiltinSubagentToolsTier(%q) = %q, want %q", name, got, tier)
		}
	}
	for _, def := range agent.ActiveDefinitions() {
		if _, ok := want[def.Name]; !ok {
			t.Errorf("built-in %q has no expected tier in this test", def.Name)
		}
	}
	if got := BuiltinSubagentToolsTier("  EXPLORE "); got != appcfg.SubagentToolsReadOnly {
		t.Errorf("the tier must be read the way the policy is: got %q", got)
	}
	// The guardian is the one type with no tools at all, which is why its prompt
	// tells it not to use any.
	if guardian := BuiltinSubagentToolsTier("guardian"); guardian != SubagentToolsNone {
		t.Fatalf("guardian = %q, want %q", guardian, SubagentToolsNone)
	}
}

// A custom subagent in pick mode is held to the tools it was given, except for
// the pair it reports back with: talking to the agent that dispatched it is
// structural, not one more capability to tick.
func TestSubagentToolPolicyNeverBlocksPeerTools(t *testing.T) {
	policy := SubagentToolPolicy{Mode: appcfg.SubagentToolsPick, Allowed: map[string]struct{}{"read_file": {}}}
	if policy.Blocks("peer_send") {
		t.Fatal("pick mode blocked peer_send: reporting back to the dispatcher is structural")
	}
	if policy.Blocks("peer_list") {
		t.Fatal("pick mode blocked peer_list: naming the dispatcher is structural")
	}
	if !policy.Blocks("write_file") {
		t.Fatal("pick mode let through a tool it did not select")
	}
	if !policy.Blocks("subagent_run") {
		t.Fatal("every mode must keep the subagent family blocked")
	}
}

// A custom subagent that reaches GuardTool without its policy is refused
// everything — not waved through because the static table has no entry for its
// name.
func TestGuardToolRefusesACustomSubagentWithoutAPolicy(t *testing.T) {
	st := NewState(t.TempDir())
	ctx := WithSubagentDefinitionSource(WithSubagentType(context.Background(), "repo-scout"), SubagentDefinitionSourceCustom)
	for _, name := range []string{"read_file", "write_file", "edit_file", "shell", "subagent_run"} {
		err := st.GuardTool(ctx, name)
		if err == nil || !strings.Contains(err.Error(), "no tool policy") {
			t.Errorf("GuardTool(%q) = %v, want a refusal naming the missing policy", name, err)
		}
	}
	// With its policy the run is held to it, not refused wholesale.
	withPolicy := WithSubagentToolPolicy(ctx, SubagentToolPolicy{Mode: appcfg.SubagentToolsReadOnly})
	if err := st.GuardTool(withPolicy, "read_file"); err != nil {
		t.Fatalf("read_file refused under a read-only policy: %v", err)
	}
	if err := st.GuardTool(withPolicy, "write_file"); err == nil || !strings.Contains(err.Error(), "blocked by policy") {
		t.Fatalf("write_file under a read-only policy = %v", err)
	}
}
