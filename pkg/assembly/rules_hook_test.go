package assembly

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// TestRulesHookReadsWorkingDirFromContext pins that the FOREBRAIN.md rules are
// resolved against the session project root on the context, not the process
// directory a gateway serves many projects from.
func TestRulesHookReadsWorkingDirFromContext(t *testing.T) {
	home := t.TempDir()
	wsRoot := t.TempDir()

	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "FOREBRAIN.md"), []byte("PROJECT RULES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := safety.MarkTrusted(home, safety.Project{Root: project, VersionControlled: true}); err != nil {
		t.Fatal(err)
	}

	// A different project the process happens to be sitting in.
	processDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(processDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(processDir, "FOREBRAIN.md"), []byte("PROCESS RULES"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := safety.MarkTrusted(home, safety.Project{Root: processDir, VersionControlled: true}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(processDir)

	p := NewPreHook(nil, home, wsRoot)
	ctx := tool.WithProjectRoot(context.Background(), project)
	out, err := p.run(ctx, "pre", hook.HookContext{Trigger: "user", SessionID: "s"}, "hello")
	if err != nil {
		t.Fatalf("rules hook: %v", err)
	}
	if !strings.Contains(out.SystemAddendum, "PROJECT RULES") {
		t.Fatalf("rules injection did not read the session working dir: %q", out.SystemAddendum)
	}
	if strings.Contains(out.SystemAddendum, "PROCESS RULES") {
		t.Fatalf("rules injection read the process directory: %q", out.SystemAddendum)
	}
}

// A rule file whose name starts with ".." is inside the working directory, so
// the rules list names it relatively — the absolute path never reaches the
// prompt for a file that lives in the project.
func TestDisplayRulePathKeepsDotDotNamesRelative(t *testing.T) {
	wd := t.TempDir()
	path := filepath.Join(wd, "..env")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := displayRulePath(path, wd); got != "..env" {
		t.Fatalf("displayRulePath(..env) = %q, want %q", got, "..env")
	}
}
