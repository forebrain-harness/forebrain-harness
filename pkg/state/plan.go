// Package planstore persists plan-mode plan files.
//
// Plans live in a per-project directory: <workspaceRoot>/plans/<projectKey>/*.md
// when a project key is available, or directly under <workspaceRoot>/plans/*.md
// for gateway/channel conversations that are not tied to a launch project.
// Each plan is its own file with an LLM-chosen, human-readable name (e.g.
// "add-s3-skill-sync.md"). Historical plans are preserved: producing a new
// plan creates a new file rather than overwriting the previous one.
//
// The "current" plan for a scope is the most-recently-modified .md file in
// that directory. This needs no pointer/sidecar file and no write-time hook:
// the plan the agent last wrote or edited is the one surfaced to the user for
// approval, /plan show, and status. If the directory is empty, PlanPath falls
// back to a default <dir>/plan.md (which may not yet exist).
package state

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PlanDirForProject returns the directory holding plan files for a launch
// project. projectKey is the project identity produced by memories.ProjectKey
// (the project root with its separators turned into dashes); when empty, plans
// live directly under <workspaceRoot>/plans.
func PlanDirForProject(workspaceRoot, projectKey string) string {
	root := strings.TrimSpace(workspaceRoot)
	key := sanitizeSegment(projectKey)
	if key == "" {
		return filepath.Join(root, "plans")
	}
	return filepath.Join(root, "plans", key)
}

// PlanPathForProject returns the current plan file for a project scope: the
// newest .md file directly in PlanDirForProject, or a default
// <PlanDirForProject>/plan.md when none exists yet. The returned path may not
// exist on disk.
func PlanPathForProject(workspaceRoot, projectKey string) string {
	dir := PlanDirForProject(workspaceRoot, projectKey)
	if f := newestMarkdown(dir); f != "" {
		return f
	}
	return filepath.Join(dir, "plan.md")
}

func sanitizeSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = filepath.Base(s)
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '-'
		default:
			return r
		}
	}, s)
	return strings.Trim(s, ". ")
}

// newestMarkdown returns the absolute path of the most-recently-modified .md
// file directly in dir, or "" if the directory has none.
func newestMarkdown(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var newest string
	var newestMT time.Time
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		mt := info.ModTime()
		if newest == "" || mt.After(newestMT) {
			newest = filepath.Join(dir, name)
			newestMT = mt
		}
	}
	return newest
}

// GetForProject reads the current plan file for a project scope. A missing file
// yields ("", nil).
func GetPlanForProject(workspaceRoot, projectKey string) (string, error) {
	p := PlanPathForProject(workspaceRoot, projectKey)
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// SetForProject writes content to the current project-scoped plan file
// (creating its directory). When no plan exists yet it writes the default
// <PlanDirForProject>/plan.md.
func SetPlanForProject(workspaceRoot, projectKey, content string) error {
	p := PlanPathForProject(workspaceRoot, projectKey)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	return os.WriteFile(p, []byte(strings.TrimSpace(content)+"\n"), 0o600)
}

// ImplementationHeading is the canonical heading an agent appends to a plan
// file once it has carried the plan out. It closes the plan loop: the plan
// file records not only what was intended but what actually landed.
//
// The heading is matched, not just written, so it must stay a single literal
// the reminder text and the detector share.
const ImplementationHeading = "## Implementation Status"

// HasImplementationRecord reports whether plan content already carries the
// implementation record appended by ImplementationHeading. Matching is done on
// a heading line so that a passing mention of the phrase in prose does not
// count as a record.
func HasImplementationRecord(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.EqualFold(strings.TrimSpace(line), ImplementationHeading) {
			return true
		}
	}
	return false
}
