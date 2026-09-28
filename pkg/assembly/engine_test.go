package assembly

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

type staticSource struct {
	id    string
	items []ContextItem
}

func (s staticSource) ID() string { return s.id }
func (s staticSource) Collect(req AssemblyRequest) ([]ContextItem, error) {
	return append([]ContextItem(nil), s.items...), nil
}

func TestEngineBuildTracksContextPressureAndEvictions(t *testing.T) {
	engine := New()
	engine.Register(staticSource{id: "pinned_source", items: []ContextItem{{
		Layer:           LayerWorking,
		Title:           "Pinned",
		Content:         strings.Repeat("A", 120),
		Priority:        100,
		EstimatedTokens: 90,
		Pinned:          true,
	}}})
	engine.Register(staticSource{id: "large_source", items: []ContextItem{{
		Layer:           LayerEvidence,
		Title:           "Large",
		Content:         strings.Repeat("B", 120),
		Priority:        80,
		EstimatedTokens: 40,
	}}})

	res := engine.Build(AssemblyRequest{
		SessionID:   "s1",
		Mode:        "implement",
		Query:       strings.Repeat("query ", 12),
		LimitTokens: 100,
		MaxItems:    4,
	})

	if len(res.Items) != 1 || res.Items[0].SourceID != "pinned_source" {
		t.Fatalf("expected pinned item to survive budget pruning: %+v", res.Items)
	}
	if res.ProjectedContextTokens <= 0 {
		t.Fatalf("expected projected context tokens, got %+v", res)
	}
	if res.ContextPressure != "block" {
		t.Fatalf("expected context pressure to be block, got %q with budget %+v", res.ContextPressure, res.Budget)
	}
	if len(res.EvictionDetails) == 0 {
		t.Fatalf("expected eviction details, got none: %+v", res.Provenance)
	}
}

func TestRecentFilesSourceCollectSummarizesRecentFileTouches(t *testing.T) {
	st := tool.NewState(t.TempDir())
	st.RememberRead("/repo/internal/compact/service.go", time.Now(), 12, []byte("abc"))
	st.RememberWrite("/repo/internal/gateway/auto_assembly.go", time.Now(), 34, []byte("def"))

	items, err := (RecentFilesSource{
		ReadStatesProvider: func() []tool.ReadState {
			return st.ReadStates()
		},
	}).Collect(AssemblyRequest{})
	if err != nil {
		t.Fatalf("Collect error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected single recent-files item, got %d", len(items))
	}
	got := items[0]
	if got.SourceID != "recent_files_source" || got.Layer != LayerWorking {
		t.Fatalf("unexpected item: %+v", got)
	}
	for _, want := range []string{"Recent File Activity", "read: /repo/internal/compact/service.go", "write: /repo/internal/gateway/auto_assembly.go"} {
		if !strings.Contains(got.Content, want) && !strings.Contains(got.Title, want) {
			t.Fatalf("recent files summary missing %q: %+v", want, got)
		}
	}
}

func TestGitContextSourceCollectSummarizesChanges(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "Forebrain Harness Test")
	runGit(t, dir, "config", "user.email", "forebrain@example.com")

	writeFile(t, filepath.Join(dir, "internal/actionrt/service.go"), "package actionrt\n\nfunc Service() {}\n")
	writeFile(t, filepath.Join(dir, "internal/agentnotify/format.go"), "package agentnotify\n\nfunc Format() {}\n")
	writeFile(t, filepath.Join(dir, "README.md"), "hello\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")

	writeFile(t, filepath.Join(dir, "internal/actionrt/service.go"), "package actionrt\n\nfunc Service() {}\n\nfunc Changed() {}\n")
	writeFile(t, filepath.Join(dir, "internal/actionrt/types.go"), "package actionrt\n\ntype T struct{}\n")
	writeFile(t, filepath.Join(dir, "internal/agentnotify/format.go"), "package agentnotify\n\nfunc Format() {}\n\nfunc Extra() {}\n")
	runGit(t, dir, "add", ".")

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	defer func() {
		if chdirErr := os.Chdir(wd); chdirErr != nil {
			t.Fatalf("restore cwd error: %v", chdirErr)
		}
	}()

	items, err := (GitContextSource{}).Collect(AssemblyRequest{Mode: "agent"})
	if err != nil {
		t.Fatalf("Collect error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected single git item, got %d", len(items))
	}
	blob := items[0].Content
	for _, want := range []string{
		"dirty_files:",
		"hot_paths:",
		"internal/actionrt",
		"internal/agentnotify",
		"largest_diffs:",
		"diff_totals:",
	} {
		if !strings.Contains(blob, want) {
			t.Fatalf("git summary missing %q: %q", want, blob)
		}
	}
	if strings.Contains(blob, "status:\n") || strings.Contains(blob, "diff_stat:\n") {
		t.Fatalf("git summary must not emit raw status or diff_stat blocks: %q", blob)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}

func writeFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s error: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile %s error: %v", path, err)
	}
}
