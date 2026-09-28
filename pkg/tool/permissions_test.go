package tool

import (
	"context"
	"os"
	"path/filepath"
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
