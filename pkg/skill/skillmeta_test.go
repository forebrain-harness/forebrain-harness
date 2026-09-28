package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

func TestNormalizeToken(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"Hello", "hello"},
		{"HELLO_WORLD", "hello-world"},
		{"  Foo Bar  ", "foo-bar"},
		{"", ""},
		{"a_b_c_d", "a-b-c-d"},
	}
	for _, tc := range tests {
		got := NormalizeToken(tc.input)
		if got != tc.want {
			t.Errorf("NormalizeToken(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestAgentSkillRoots_NoHome(t *testing.T) {
	roots := AgentSkillRoots("", "", safety.ProjectContext{})
	// Without a trusted Git project, only cross-tool user skills are available.
	if len(roots) < 1 {
		t.Fatalf("expected at least 1 root without home, got %d", len(roots))
	}
	foundAgents := false
	for _, root := range roots {
		if strings.HasSuffix(filepath.ToSlash(root), "/.agents/skills") {
			foundAgents = true
			break
		}
	}
	if !foundAgents {
		t.Errorf("expected user-home .agents skill root in %v", roots)
	}
}

func TestAgentSkillRoots_WithHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	roots := AgentSkillRoots("/home/user", "/home/user/workspace", safety.ProjectContext{})
	// Without an explicitly trusted Git project: workspace/skills > home/skills
	// > ~/.agents/skills > .system.
	if len(roots) < 4 {
		t.Fatalf("expected at least 4 roots with home, got %d", len(roots))
	}
	idx := func(suffix string) int {
		for i, r := range roots {
			if strings.HasSuffix(filepath.ToSlash(r), suffix) {
				return i
			}
		}
		return -1
	}
	workspace := idx("/home/user/workspace/skills")
	user := idx("/home/user/skills")
	agents := idx("/.agents/skills")
	system := idx("/home/user/skills/.system")
	if workspace == -1 || user == -1 || agents == -1 || system == -1 {
		t.Fatalf("missing expected root in %v", roots)
	}
	if !(workspace < user && user < agents && agents < system) {
		t.Fatalf("priority order broken: workspace=%d user=%d agents=%d system=%d",
			workspace, user, agents, system)
	}
}

func TestScanRoots_Alias(t *testing.T) {
	roots := ScanRoots("/tmp/test")
	expected := AgentSkillRoots("/tmp/test", "/tmp/test/workspace", safety.ProjectContext{})
	if len(roots) != len(expected) {
		t.Fatalf("ScanRoots returned %d roots, expected %d", len(roots), len(expected))
	}
}

func TestRegistry_LookupEmpty(t *testing.T) {
	reg := &Registry{}
	_, ok := reg.Lookup("nonexistent")
	if ok {
		t.Error("expected lookup to return false for empty registry")
	}
}

func TestRegistry_LookupAfterRefresh(t *testing.T) {
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "skills", "test-skill")
	os.MkdirAll(skillDir, 0o755)
	skillMD := `---
name: test-skill
description: A test skill
---
# Test Skill

This is a test.
`
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0o644)

	reg := &Registry{}
	err := reg.Refresh(tmpDir)
	if err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	entry, ok := reg.Lookup("test-skill")
	if !ok {
		t.Fatal("expected to find test-skill")
	}
	if entry.Name != "test-skill" {
		t.Errorf("expected name 'test-skill', got %q", entry.Name)
	}
	if entry.SkillPath == "" {
		t.Error("expected non-empty SkillPath")
	}
	if entry.SkillDir == "" {
		t.Error("expected non-empty SkillDir")
	}
}

func TestRegistry_LookupCaseInsensitive(t *testing.T) {
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "skills", "MySkill")
	os.MkdirAll(skillDir, 0o755)
	skillMD := `---
name: MySkill
description: Case test
---
# MySkill
`
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0o644)

	reg := &Registry{}
	err := reg.Refresh(tmpDir)
	if err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	// Should be findable with different case
	_, ok := reg.Lookup("myskill")
	if !ok {
		t.Error("expected case-insensitive lookup to succeed")
	}
	_, ok = reg.Lookup("MYSKILL")
	if !ok {
		t.Error("expected uppercase lookup to succeed")
	}
}

func TestRegistry_LookupByLeafName(t *testing.T) {
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "skills", "my-leaf-name")
	os.MkdirAll(skillDir, 0o755)
	skillMD := `---
name: DifferentName
description: Leaf test
---
# DifferentName
`
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0o644)

	reg := &Registry{}
	err := reg.Refresh(tmpDir)
	if err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	// Should be findable by leaf directory name
	_, ok := reg.Lookup("my-leaf-name")
	if !ok {
		t.Error("expected lookup by leaf name to succeed")
	}
}

func TestRegistry_SkipsNonSKILLFiles(t *testing.T) {
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "skills", "fake-skill")
	os.MkdirAll(skillDir, 0o755)
	// No SKILL.md file
	os.WriteFile(filepath.Join(skillDir, "README.md"), []byte("readme"), 0o644)

	reg := &Registry{}
	err := reg.Refresh(tmpDir)
	if err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	_, ok := reg.Lookup("fake-skill")
	if ok {
		t.Error("expected non-SKILL.md directory to be skipped")
	}
}

func TestRegistry_EmptySKILLContent(t *testing.T) {
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "skills", "empty-skill")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(""), 0o644)

	reg := &Registry{}
	err := reg.Refresh(tmpDir)
	if err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	_, ok := reg.Lookup("empty-skill")
	if ok {
		t.Error("expected empty SKILL.md to be skipped")
	}
}

func TestRegistry_NonExistentRoot(t *testing.T) {
	reg := &Registry{}
	err := reg.Refresh("/nonexistent/path/that/does/not/exist")
	if err != nil {
		t.Fatalf("Refresh should not error on nonexistent root: %v", err)
	}
}

// The helpers below were production functions that only the tests in this
// package ever called: each is a home-scoped wrapper over the
// workspace-scoped function that actually runs, now that skill state is
// stored per primary agent under that agent's workspace root. They live
// here so the production files carry no unused code while the tests keep
// exercising the live functions underneath.

// ScanRoots is the home-scoped wrapper the priority test uses; the explicit
// empty project context is what "no project" looks like now.
func ScanRoots(home string) []string {
	h := strings.TrimSpace(home)
	workspaceRoot := ""
	if h != "" {
		workspaceRoot = filepath.Join(h, "workspace")
	}
	return AgentSkillRoots(h, workspaceRoot, safety.ProjectContext{})
}

// Refresh rebuilds the registry for the main agent (workspace <home>/workspace).
// Per-agent callers use RefreshForWorkspace so a non-main agent reads its own
// isolated skill-toggle state.
func (r *Registry) Refresh(home string) error {
	return r.RefreshForWorkspace(home, "", "")
}

func (r *Registry) Lookup(token string) (regEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.byToken == nil {
		return regEntry{}, false
	}
	e, ok := r.byToken[NormalizeToken(token)]
	return e, ok
}
