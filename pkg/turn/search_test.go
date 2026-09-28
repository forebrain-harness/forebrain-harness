package turn

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMentionCandidatesFromFilesSynthesizesParentDirs(t *testing.T) {
	got := candidatesFromFiles([]string{"internal/webui/x.go", "internal/a.go", "internal/webui/x.go"})
	want := []Candidate{
		{Path: "internal/", IsDir: true},
		{Path: "internal/a.go"},
		{Path: "internal/webui/", IsDir: true},
		{Path: "internal/webui/x.go"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestMentionTopLevelListsDirsFirstSkippingHidden(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"zdir", "adir", ".hidden", "node_modules"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"bfile.txt", "afile.txt"} {
		if err := os.WriteFile(filepath.Join(root, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := topLevelCandidates(root)
	want := []Candidate{
		{Path: "adir/", IsDir: true},
		{Path: "zdir/", IsDir: true},
		{Path: "afile.txt"},
		{Path: "bfile.txt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestMentionCandidatesForQueryTiers(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub", "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "target.go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "other.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	// Empty query → top level.
	got := Search(root, "")
	if len(got) == 0 || got[0].Path != "sub/" {
		t.Fatalf("empty query: got %#v", got)
	}

	// Path-like query → literal scan with the typed prefix preserved.
	got = Search(root, "./sub/")
	wantPaths := []string{"./sub/inner/", "./sub/target.go"}
	if len(got) != 2 || got[0].Path != wantPaths[0] || got[1].Path != wantPaths[1] {
		t.Fatalf("path-like query: got %#v, want %v", got, wantPaths)
	}

	// Fuzzy query → index-backed match.
	got = Search(root, "tgo")
	found := false
	for _, c := range got {
		if c.Path == "sub/target.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("fuzzy query: %#v lacks sub/target.go", got)
	}
}

func TestMentionGitFilesListsTrackedAndUntracked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "tracked.go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "tracked.go")
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := gitFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	for _, f := range files {
		set[f] = true
	}
	if !set["tracked.go"] || !set["untracked.txt"] || !set[".gitignore"] {
		t.Fatalf("missing expected files in %v", files)
	}
	if set["ignored.txt"] {
		t.Fatalf("gitignored file leaked into %v", files)
	}
}

func TestMentionGitFilesDropsWorktreeDeletions(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	for _, dir := range []string{"skills/kept", "skills/gone"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "SKILL.md"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-qm", "init")

	// Deleted in the working tree but not yet staged: the index still names it.
	if err := os.RemoveAll(filepath.Join(root, "skills/gone")); err != nil {
		t.Fatal(err)
	}

	files, err := gitFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f == "skills/gone/SKILL.md" {
			t.Fatalf("deleted file still listed in %v", files)
		}
	}
	for _, c := range candidatesFromFiles(files) {
		if c.Path == "skills/gone/" {
			t.Fatalf("deleted directory still offered as a candidate")
		}
	}
}
