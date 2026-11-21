// The turn-end deliverable top-up: the engine's guarantee that every tracked
// change a coding turn made in its project is registered as that turn's
// deliverable, whatever the model did or forgot to present.
//
// The prompt-driven mandate (the office/batch pattern, extended to coding by
// CodingDeliveryPrompt) is the model's half; this file is the engine's. At
// each terminal branch of Submit the turn's tracked write set — the
// file-change ledger plus the changed paths every shell step reported,
// including failed and cancelled commands — is diffed against what the
// conversation already holds, and every path whose bytes are not yet
// delivered is registered as a fallback artifact. No synthetic tool step is
// emitted and the transcript is untouched: the surfaces read the rows the
// way they read any others.
//
// Only a coding conversation is topped up (owner, 2026-11-21): the work is
// the deliverable there, and the model's presentation is the title and the
// description rather than the set. Office keeps the model's set alone — a
// helper script is not a deliverable — and batch keeps its own outputs scan.
package turn

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// turnDeliverableTimeout bounds one top-up: it runs under the session lock
// at the turn's very end, so it may add at most this much to the wait, and a
// path that cannot be finished in time is left to the next turn's comparison
// rather than to an unbounded hold.
const turnDeliverableTimeout = 10 * time.Second

// turnDeliverablePathLimit caps how many paths one top-up registers, the same
// ceiling the batch outputs scan carries: past it the claim the list makes
// ("this turn's work") would stop being readable. The candidates are sorted
// first, so the truncated set is the same on every run of it.
const turnDeliverablePathLimit = 200

// deliverableScopeSource is the optional slice of the session store the
// top-up resolves its scope through. *state.SessionStore satisfies it; a
// service whose session store does not (a test double) resolves no root and
// the top-up is a no-op.
type deliverableScopeSource interface {
	SessionProjectScope(ctx context.Context, sessionID string) (projectType, projectRoot string, err error)
}

// deliverableChangeSource is the optional slice the tracked write set is read
// through: the file-change ledger scoped to the run tree, and the run ids of
// that tree.
type deliverableChangeSource interface {
	FileChangesForRunTree(ctx context.Context, sessionID, runID string) ([]state.FileChangeRow, error)
	RunTreeIDs(ctx context.Context, sessionID, runID string) ([]string, error)
}

// deliverableEventSource is the optional slice the raw shell-step payloads
// are read through — the uncapped per-type reader the replay path uses, not
// the capped general list.
type deliverableEventSource interface {
	ListRunEventsOfTypes(ctx context.Context, runID string, types ...string) ([]state.SessionEvent, error)
}

// deliverableArtifactStore is the artifact store the top-up writes through —
// BatchArtifactStore plus the session-level version check that keeps
// unchanged bytes from being delivered twice.
type deliverableArtifactStore interface {
	AddArtifact(ctx context.Context, a state.Artifact) (state.Artifact, error)
	LatestArtifactByPath(ctx context.Context, sessionID, path string) (state.Artifact, bool, error)
}

// deliverableScope resolves the conversation's delivery scope: the project
// type it froze at birth and the root of the project it belongs to. A
// conversation without a project, or a service whose session store cannot
// say, resolves no root — the top-up has nothing to anchor containment to
// and stays silent, leaving delivery to the prompt's half.
func (s *Service) deliverableScope(ctx context.Context, sessionID string) (projectType, projectRoot string) {
	scope, ok := s.sessionStore.(deliverableScopeSource)
	if !ok || scope == nil {
		return "", ""
	}
	projectType, projectRoot, err := scope.SessionProjectScope(ctx, sessionID)
	if err != nil {
		slog.WarnContext(ctx, "turn deliverable scope not resolved", "session", sessionID, "err", err)
		return "", ""
	}
	return projectType, projectRoot
}

// canonicalDeliverablePath resolves one spelling of a path the way the
// comparison needs it: the physical location, because the ledger's tool
// paths and the shell diff's git paths can spell one file differently (a
// symlinked directory component). A path that cannot be resolved is cleaned
// and compared as-is — the containment test still rejects what falls outside
// the project.
func canonicalDeliverablePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// deliverablePathExcluded reports whether a canonical path inside the project
// root is not the conversation's to deliver: the engine's own plumbing stays
// out of the deliverables, so nothing under git metadata is one.
func deliverablePathExcluded(rel string) bool {
	if rel == "" {
		return true
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == ".git" {
			return true
		}
	}
	return false
}

// shellChangedPaths reads the changed paths a run's shell steps reported,
// from the raw completion payloads the diff rides in. ChangedFiles — not the
// diff's file records — is the source: binary and large files appear only
// there. The turn_diff_updated companion carries even less and is never
// consulted.
func shellChangedPaths(ctx context.Context, events deliverableEventSource, runID string) []string {
	rows, err := events.ListRunEventsOfTypes(ctx, runID, event.RunEventToolCompleted)
	if err != nil {
		slog.WarnContext(ctx, "turn deliverable shell steps not read", "run_id", runID, "err", err)
		return nil
	}
	var out []string
	for _, row := range rows {
		var payload map[string]any
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			continue
		}
		output, ok := payload["output"].(map[string]any)
		if !ok {
			continue
		}
		diff, ok := tool.DecodeShellEditDiff(output["shell_edit_diff"])
		if !ok {
			continue
		}
		out = append(out, diff.ChangedFiles...)
	}
	return out
}

// CollectRunDeliverables settles one run's coding deliverables now: the turn-end
// top-up, callable by a surface that writes a run's terminal frame itself.
// Idempotent — the same bytes are never registered twice. It takes no session
// lock: the caller may be a surface goroutine settling a parked run.
func (s *Service) CollectRunDeliverables(ctx context.Context, sessionID, runID string) {
	if s == nil {
		return
	}
	// Only a conversation's own run settles here. A subagent's writes ride its
	// parent's turn-end settle (the run-tree read covers them), and a child
	// settling first would make the parent skip them: the version check is
	// session-level by path, so the same bytes already delivered under the child
	// read as delivered for the parent's frame. The recovery paths cannot tell
	// the two apart from memory — FindRunByAction returns the wait's own run,
	// which a subagent's gate wrote — so the guard lives here, not at the call.
	// Fail closed: without the run store the parent/child question cannot be
	// answered, and settling unguarded is the very failure this guard exists for.
	if s.runEventStore == nil {
		return
	}
	if run, err := s.runEventStore.GetRun(ctx, runID); err != nil || run == nil ||
		strings.TrimSpace(run.ParentRunID) != "" {
		return
	}
	pt, root := s.deliverableScope(ctx, sessionID)
	if root == "" {
		return
	}
	s.collectTurnDeliverables(ctx, sessionID, runID, pt, root)
}

// collectTurnDeliverables is the turn-end top-up itself: every tracked change
// the run tree made that the conversation does not already hold as this
// version is registered as a fallback deliverable of the run. It never
// returns an error and never changes the turn's outcome — a file that cannot
// be read, hashed or compared is logged and skipped, and the next turn's
// comparison still sees it.
func (s *Service) collectTurnDeliverables(ctx context.Context, sessionID, runID, projectType, projectRoot string) {
	sid := strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	root := strings.TrimSpace(projectRoot)
	if s == nil || sid == "" || runID == "" || root == "" {
		return
	}
	// Only a coding conversation is guaranteed its turn's files (owner
	// 2026-11-21): the work itself is the deliverable there, and the model's
	// presentation is the title and description, not the set. Office keeps the
	// model's set alone (a helper script is not a deliverable) and batch keeps
	// its own outputs/ scan.
	if projectType != state.ProjectTypeCoding {
		return
	}
	changes, ok := s.sessionStore.(deliverableChangeSource)
	if !ok || changes == nil {
		return
	}
	artifacts, ok := s.batchArtifacts.(deliverableArtifactStore)
	if !ok || artifacts == nil {
		return
	}
	events, ok := s.runEventStore.(deliverableEventSource)
	if !ok || events == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(ctx, "turn deliverable top-up panicked", "session", sid, "run_id", runID, "panic", r)
		}
	}()
	// The top-up outlives the turn's own cancellation: a cancelled turn's
	// writes already landed and are still the user's to receive, and the
	// bound keeps the session lock this runs under from holding forever.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), turnDeliverableTimeout)
	defer cancel()

	root = canonicalDeliverablePath(root)
	home := ""
	if s.workspaceRoot != "" {
		home = canonicalDeliverablePath(s.workspaceRoot)
	}

	// The tracked write set, keyed by canonical path: the ledger first, then
	// the shell steps of every run in the tree (a failed or cancelled
	// command's writes ride the same payload a successful one's do). The raw
	// spellings are kept beside the key — the ledger records the path the
	// tool resolved (symlinks unevaluated) while the shell diff records the
	// physical one, and the version comparison below must recognize a
	// delivered row under either.
	paths := make(map[string][]string)
	addPath := func(path string) {
		if strings.TrimSpace(path) == "" {
			return
		}
		key := canonicalDeliverablePath(path)
		for _, have := range paths[key] {
			if have == path {
				return
			}
		}
		paths[key] = append(paths[key], path)
	}
	if rows, err := changes.FileChangesForRunTree(ctx, sid, runID); err != nil {
		slog.WarnContext(ctx, "turn deliverable ledger not read", "session", sid, "run_id", runID, "err", err)
	} else {
		for _, row := range rows {
			if row.Deleted {
				continue
			}
			addPath(row.Path)
		}
	}
	runIDs, err := changes.RunTreeIDs(ctx, sid, runID)
	if err != nil {
		slog.WarnContext(ctx, "turn deliverable run tree not read", "session", sid, "run_id", runID, "err", err)
	}
	for _, id := range runIDs {
		for _, path := range shellChangedPaths(ctx, events, id) {
			addPath(path)
		}
	}

	// Sorted, so the cap below keeps the same paths on every run of it: a map
	// walk would deliver whichever 200 the runtime happened to visit first.
	candidates := make([]string, 0, len(paths))
	for path := range paths {
		candidates = append(candidates, path)
	}
	sort.Strings(candidates)

	registered := 0
	for _, path := range candidates {
		if registered >= turnDeliverablePathLimit {
			slog.WarnContext(ctx, "turn deliverable top-up truncated", "limit", turnDeliverablePathLimit, "candidates", len(candidates))
			break
		}
		if ctx.Err() != nil {
			slog.WarnContext(ctx, "turn deliverable top-up timed out", "session", sid, "run_id", runID, "registered", registered)
			slog.WarnContext(ctx, "turn deliverable top-up incomplete", "registered", registered, "candidates", len(candidates))
			return
		}
		spellings := paths[path]
		if !filepath.IsAbs(path) {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		if deliverablePathExcluded(rel) {
			continue
		}
		if home != "" {
			if homeRel, err := filepath.Rel(home, path); err == nil && (homeRel == "." || (homeRel != ".." && !strings.HasPrefix(homeRel, ".."+string(filepath.Separator)) && !filepath.IsAbs(homeRel))) {
				continue
			}
		}
		if _, err := os.Stat(path); err != nil {
			// Gone again — deleted since it was written, or a path the
			// command only touched. Nothing to deliver.
			continue
		}
		sum, kind, mimeType, preview, size, mtimeNS, err := hashArtifactFile(path)
		if err != nil {
			slog.WarnContext(ctx, "turn deliverable not hashed", "session", sid, "path", path, "err", err)
			continue
		}
		// The version comparison recognizes a delivered row under either
		// spelling the file was recorded as: the model presented one, the
		// ledger wrote another.
		delivered := false
		for _, candidate := range append([]string{path}, spellings...) {
			latest, found, err := artifacts.LatestArtifactByPath(ctx, sid, candidate)
			if err != nil {
				slog.WarnContext(ctx, "turn deliverable version not read", "session", sid, "path", candidate, "err", err)
				continue
			}
			if found && latest.SHA256 == sum {
				// This version is already delivered — presented by the model,
				// or topped up by an earlier turn of the conversation.
				delivered = true
				break
			}
		}
		if delivered {
			continue
		}
		name := filepath.Base(path)
		if _, err := artifacts.AddArtifact(ctx, state.Artifact{
			SessionID: sid,
			RunID:     runID,
			Path:      path,
			RelPath:   filepath.ToSlash(rel),
			Name:      name,
			Title:     name,
			MIME:      mimeType,
			Kind:      kind,
			Preview:   preview,
			Size:      size,
			MTimeNS:   mtimeNS,
			SHA256:    sum,
			Origin:    state.ArtifactOriginFallback,
		}); err != nil {
			slog.WarnContext(ctx, "turn deliverable not recorded", "session", sid, "path", path, "err", err)
			continue
		}
		registered++
	}
	if registered > 0 {
		slog.InfoContext(ctx, "turn deliverables topped up", "session", sid, "run_id", runID, "count", registered)
	}
}
