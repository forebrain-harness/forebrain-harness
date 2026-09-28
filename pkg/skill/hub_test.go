package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListDTOReturnsSourceAndTrustMetadata(t *testing.T) {
	home := t.TempDir()
	skillPath := filepath.Join(home, "workspace", "skills", "demo", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(`---
name: demo
description: demo skill
allowed_tools: view
---

body
`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	h := &Hub{Home: home}
	h.AddRoot(filepath.Join(home, "workspace", "skills"))
	list, err := h.ListDTO()
	if err != nil {
		t.Fatalf("ListDTO: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(list)=%d want 1", len(list))
	}
	if list[0].Source != "workspace" {
		t.Fatalf("source=%q want workspace", list[0].Source)
	}
	if list[0].Trust != "workspace" {
		t.Fatalf("trust=%q want workspace", list[0].Trust)
	}
}

func TestNewForHomeIncludesGlobalAndLegacySkillRoots(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	h := NewForHome(home, project)
	if len(h.Roots) < 3 {
		t.Fatalf("roots too short: %#v", h.Roots)
	}
	has := func(suffix string) bool {
		for _, root := range h.Roots {
			if strings.HasSuffix(filepath.Clean(root), filepath.Clean(suffix)) {
				return true
			}
		}
		return false
	}
	if !has(filepath.Join(home, "skills")) {
		t.Fatalf("missing global home skills root: %#v", h.Roots)
	}
	if !has(filepath.Join(home, "workspace", "skills")) {
		t.Fatalf("missing workspace skills root: %#v", h.Roots)
	}
	if !has(filepath.Join(".forebrain", "skills")) {
		t.Fatalf("missing project skills root: %#v", h.Roots)
	}
}

// Nested namespace dirs (no SKILL.md at top, sub-dirs each have SKILL.md)
// must surface every nested skill via depth-1 recursion in scanSkillDir.
func TestListManagedRecursesIntoNamespaceDirs(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "skills", ".system")

	write := func(rel, name string) {
		p := filepath.Join(root, rel, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		body := "---\nname: " + name + "\ndescription: " + name + " desc\n---\n\nbody\n"
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	// Top-level skill.
	write("plain", "plain")
	// Namespace dir with 2 nested skills (no SKILL.md at namespace root).
	write(filepath.Join("caveman", "caveman-review"), "caveman-review")
	write(filepath.Join("caveman", "caveman-help"), "caveman-help")

	h := &Hub{Home: home}
	h.AddRoot(root)
	list, err := h.ListDTO()
	if err != nil {
		t.Fatalf("ListDTO: %v", err)
	}

	got := map[string]bool{}
	for _, s := range list {
		got[s.Name] = true
	}
	for _, want := range []string{"plain", "caveman-review", "caveman-help"} {
		if !got[want] {
			t.Errorf("missing skill %q in: %v", want, got)
		}
	}
}

// Depth must cap at 1 — a skill nested 2 levels deep should NOT load.
// This prevents accidental discovery of skills inside scripts/, references/, etc.
func TestListManagedDoesNotRecursePastDepth1(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "skills", ".system")

	deep := filepath.Join(root, "ns", "deep", "buried")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	body := "---\nname: buried\ndescription: should not load\n---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(deep, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	h := &Hub{Home: home}
	h.AddRoot(root)
	list, _ := h.ListDTO()
	for _, s := range list {
		if s.Name == "buried" {
			t.Fatalf("depth-2 skill loaded: %v", list)
		}
	}
}

// The helpers below were production functions that only the tests in this
// package ever called: each is a home-scoped wrapper over the
// workspace-scoped function that actually runs, now that skill state is
// stored per primary agent under that agent's workspace root. They live
// here so the production files carry no unused code while the tests keep
// exercising the live functions underneath.

func NewForHome(home string, projectRoot string) *Hub {
	return NewForWorkspace(home, filepath.Join(strings.TrimSpace(home), "workspace"), projectRoot)
}
