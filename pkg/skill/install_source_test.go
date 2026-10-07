package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGlobalSkillWinsOverWorkspaceShadow(t *testing.T) {
	home := t.TempDir()
	globalDir := filepath.Join(home, "skills", "review-playbook")
	workspaceDir := filepath.Join(home, "workspace", "skills", "review-playbook")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "SKILL.md"), []byte("---\nname: review-playbook\ndescription: global\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "SKILL.md"), []byte("---\nname: review-playbook\ndescription: workspace\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := &Registry{}
	if err := reg.Refresh(home); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}
	entry, ok := reg.Lookup("review-playbook")
	if !ok {
		t.Fatal("expected to find global skill")
	}
	if entry.SkillDir == workspaceDir {
		t.Fatalf("workspace shadow unexpectedly won: %+v", entry)
	}
}
