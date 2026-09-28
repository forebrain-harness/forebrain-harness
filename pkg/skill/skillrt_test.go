package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Discover finds every enabled skill under the roots. Skills are described in
// the prompt catalog and read from disk on demand; they are not registered as
// tools, so nothing here asserts anything about a tool table.
func TestDiscoverFindsSkillsAcrossRoots(t *testing.T) {
	home := t.TempDir()
	workspaceRoot := filepath.Join(home, "workspaces", "review")
	workspaceSkills := filepath.Join(workspaceRoot, "skills")
	sharedSkills := filepath.Join(home, "skills")
	writeSkillFile(t, filepath.Join(workspaceSkills, "workspace-review", "SKILL.md"), "workspace-review", "Private review workflow", "Workspace body")
	writeSkillFile(t, filepath.Join(sharedSkills, "shared-review", "SKILL.md"), "shared-review", "Shared review workflow", "Shared body")

	found, err := (Loader{Roots: []string{workspaceSkills, sharedSkills}, StateRoot: workspaceRoot}).Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("discovered %d skills, want 2: %+v", len(found), found)
	}
	byName := map[string]DiscoveredSkill{}
	for _, item := range found {
		byName[item.Name] = item
	}
	ws, ok := byName["workspace-review"]
	if !ok {
		t.Fatalf("workspace skill missing: %+v", found)
	}
	if ws.Description != "Private review workflow" {
		t.Fatalf("description = %q, want the SKILL.md description", ws.Description)
	}
	if !strings.HasSuffix(filepath.ToSlash(ws.SkillFile), "/workspace-review/SKILL.md") {
		t.Fatalf("skill file = %q, want the SKILL.md path the model will read", ws.SkillFile)
	}
}

// Nested namespace directories are discovered, and the earliest root wins for
// a duplicated skill name.
func TestDiscoverNamespacesAndRootPrecedence(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, filepath.Join(root, "superpowers", "using-superpowers", "SKILL.md"), "using-superpowers", "Bootstrap skill workflow", "Bootstrap body")
	writeSkillFile(t, filepath.Join(root, "superpowers", "test-driven-development", "SKILL.md"), "test-driven-development", "TDD workflow", "TDD body")
	found, err := (Loader{Roots: []string{root}}).Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("discovered %d namespace skills, want 2: %+v", len(found), found)
	}

	first, second := t.TempDir(), t.TempDir()
	writeSkillFile(t, filepath.Join(first, "shared", "SKILL.md"), "shared", "First desc", "First body")
	writeSkillFile(t, filepath.Join(second, "shared", "SKILL.md"), "shared", "Second desc", "Second body")
	found, err = (Loader{Roots: []string{first, second}}).Discover()
	if err != nil {
		t.Fatalf("Discover(precedence): %v", err)
	}
	if len(found) != 1 || found[0].Description != "First desc" {
		t.Fatalf("precedence = %+v, want only the first root's skill", found)
	}
}

func TestDiscoverNoRoots(t *testing.T) {
	found, err := (Loader{}).Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("discovered %+v, want nothing", found)
	}
}

func writeSkillFile(t *testing.T, path, name, desc, body string) {
	t.Helper()
	frontmatter := ""
	if name != "" {
		frontmatter += "name: " + name + "\n"
	}
	if desc != "" {
		frontmatter += "description: " + desc + "\n"
	}
	writeSkillFileWithFrontmatter(t, path, strings.TrimSuffix(frontmatter, "\n"), body)
}

func writeSkillFileWithFrontmatter(t *testing.T, path, frontmatter, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(path, []byte(skillMarkdownWithFrontmatter(frontmatter, body)), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}

func skillMarkdownWithFrontmatter(frontmatter, body string) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(strings.TrimSpace(frontmatter))
	if strings.TrimSpace(frontmatter) != "" {
		b.WriteByte('\n')
	}
	b.WriteString("---\n")
	b.WriteString(body)
	b.WriteByte('\n')
	return b.String()
}
