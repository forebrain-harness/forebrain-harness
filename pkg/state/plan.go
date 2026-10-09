// Package planstore persists plan-mode plan files.
//
// Plans live in a per-conversation directory:
// <workspaceRoot>/plans/<projectKey>/<session>/*.md when a project key is
// available, or <workspaceRoot>/plans/<session>/*.md for gateway/channel
// conversations that are not tied to a launch project. Each plan is its own
// file with an LLM-chosen, human-readable name (e.g. "add-s3-skill-sync.md").
// Historical plans are preserved: producing a new plan creates a new file
// rather than overwriting the previous one.
//
// The "current" plan for a conversation is the most-recently-modified .md file
// in that conversation's directory. This needs no pointer/sidecar file and no
// write-time hook: the plan the agent last wrote or edited is the one surfaced
// to the user for approval, /plan show, and status. If the directory is empty,
// PlanPath falls back to a default <dir>/plan.md (which may not yet exist).
//
// Plan files left flat in <workspaceRoot>/plans/<projectKey>/*.md by installs
// predating per-conversation directories are history only: no conversation
// resolves them as its current plan (owner decision 2026-10-09: no
// migration, no fallback).
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

// PlanDirForSession returns the directory holding one conversation's plan
// files: <PlanDirForProject>/<session>. Plans belong to a conversation, not to
// a project: two conversations planning in the same project — in one process
// or in several terminals — must never resolve, show or edit each other's
// plan. A conversation's subagents share its directory; callers derive the
// session with tool.PlanSessionIDFromContext.
func PlanDirForSession(workspaceRoot, projectKey, sessionID string) string {
	return filepath.Join(PlanDirForProject(workspaceRoot, projectKey), planSessionSegment(sessionID))
}

// planSessionSegment turns a session id into a single directory name. Anything
// that could reach outside the project's plan directory is neutralised, and an
// empty id maps to "default" like every other per-session store (see modePath).
func planSessionSegment(sessionID string) string {
	s := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '-'
		default:
			return r
		}
	}, strings.TrimSpace(sessionID))
	s = strings.Trim(s, ". ")
	if s == "" {
		return "default"
	}
	return s
}

// PlanPathForSession returns a conversation's current plan file: the newest
// .md file directly in PlanDirForSession, or a default <dir>/plan.md when the
// conversation has not written one yet. The returned path may not exist.
func PlanPathForSession(workspaceRoot, projectKey, sessionID string) string {
	dir := PlanDirForSession(workspaceRoot, projectKey, sessionID)
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

// isPlanMarkdown reports whether a directory entry counts as a plan file:
// a non-hidden, non-directory entry whose name ends in .md (case-insensitive).
// It is the single definition shared by newestMarkdown and CopySessionPlans.
func isPlanMarkdown(e os.DirEntry) bool {
	if e.IsDir() {
		return false
	}
	name := e.Name()
	if strings.HasPrefix(name, ".") {
		return false
	}
	return strings.HasSuffix(strings.ToLower(name), ".md")
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
		if !isPlanMarkdown(e) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		mt := info.ModTime()
		if newest == "" || mt.After(newestMT) {
			newest = filepath.Join(dir, e.Name())
			newestMT = mt
		}
	}
	return newest
}

// GetPlanForSession reads a conversation's current plan. A missing file yields
// ("", nil).
func GetPlanForSession(workspaceRoot, projectKey, sessionID string) (string, error) {
	p := PlanPathForSession(workspaceRoot, projectKey, sessionID)
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// SetPlanForSession writes content to a conversation's current plan file,
// creating its directory; with no plan yet it writes <dir>/plan.md.
func SetPlanForSession(workspaceRoot, projectKey, sessionID, content string) error {
	p := PlanPathForSession(workspaceRoot, projectKey, sessionID)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	return os.WriteFile(p, []byte(strings.TrimSpace(content)+"\n"), 0o600)
}

// CopySessionPlans copies every plan file of one conversation into another's
// directory, keeping modification times so the plan that was current in the
// source stays current in the copy. /fork uses it: a fork starts with the
// plans its source had. A source without plans is not an error.
func CopySessionPlans(workspaceRoot, projectKey, fromSessionID, toSessionID string) error {
	src := PlanDirForSession(workspaceRoot, projectKey, fromSessionID)
	dst := PlanDirForSession(workspaceRoot, projectKey, toSessionID)
	if src == dst {
		return nil
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !isPlanMarkdown(e) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		target := filepath.Join(dst, e.Name())
		if err := os.WriteFile(target, b, 0o600); err != nil {
			return err
		}
		mt := info.ModTime()
		if err := os.Chtimes(target, mt, mt); err != nil {
			return err
		}
	}
	return nil
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
