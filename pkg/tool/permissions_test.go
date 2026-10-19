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
)

func TestRequestPermissionsToolFeature(t *testing.T) {
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
	if _, ok := st.ToolMetaByName("request_permissions"); ok {
		t.Fatal("request_permissions registered while feature is disabled")
	}

	st = NewState(t.TempDir())
	cfg := &appcfg.Root{Features: appcfg.FeaturesSection{RequestPermissionsTool: appcfg.BoolPtr(true)}}
	if err := RegisterDefaultTools(newAgent(t), st, &AgentToolRuntime{Cfg: cfg}); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.ToolMetaByName("request_permissions"); !ok {
		t.Fatal("request_permissions missing while feature is enabled")
	}
}

func TestShellAdditionalPermissionsFeature(t *testing.T) {
	disabled, err := NewShellTool(NewState(t.TempDir()), &AgentToolRuntime{Cfg: &appcfg.Root{}})
	if err != nil {
		t.Fatal(err)
	}
	properties, _ := disabled.InputSchema()["properties"].(map[string]any)
	if _, ok := properties["additional_permissions"]; ok {
		t.Fatal("additional_permissions present while feature is disabled")
	}
	if _, err := disabled.Handle(context.Background(), `{"command":"true","sandbox_permissions":"with_additional_permissions","additional_permissions":{"network":{"enabled":true}}}`); err == nil {
		t.Fatal("with_additional_permissions accepted while feature is disabled")
	}

	enabledCfg := &appcfg.Root{Features: appcfg.FeaturesSection{ExecPermissionApprovals: appcfg.BoolPtr(true)}}
	enabled, err := NewShellTool(NewState(t.TempDir()), &AgentToolRuntime{Cfg: enabledCfg})
	if err != nil {
		t.Fatal(err)
	}
	properties, _ = enabled.InputSchema()["properties"].(map[string]any)
	if _, ok := properties["additional_permissions"]; !ok {
		t.Fatal("additional_permissions missing while feature is enabled")
	}
}

func TestPermissionProfilePathsAreAvailableToInProcessFileTools(t *testing.T) {
	project := t.TempDir()
	external := t.TempDir()
	readPath := filepath.Join(external, "read.txt")
	writePath := filepath.Join(external, "write.txt")
	if err := os.WriteFile(readPath, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &appcfg.Root{
		DefaultPermissions: "custom",
		Permissions: appcfg.PermissionProfiles{"custom": {
			FileSystem: &appcfg.FileSystemPermissions{Entries: map[string]appcfg.FileSystemPermissionValue{
				readPath:  {Access: appcfg.FileSystemAccessRead},
				writePath: {Access: appcfg.FileSystemAccessWrite},
			}},
		}},
		SandboxMode: appcfg.SandboxModeWorkspaceWrite,
	}
	st := NewState(project)
	ApplyFilesystemPolicy(st, project, project, cfg, safety.Snapshot{})
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
