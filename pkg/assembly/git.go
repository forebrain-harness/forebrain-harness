// Package gitcontext collects the repository state that agent turns describe
// to the model, and exposes it as a pre-hook so every surface injects it the
// same way.
//
// The hook sets a system addendum rather than editing the user's message. That
// distinction is the whole point: an addendum is rendered once, at the end of
// the transcript, and is only re-emitted when its content changes, so a turn's
// user text stays byte-identical to what was typed and to what is replayed
// next turn. Splicing context into the prompt instead perturbs the prefix that
// caching depends on, and — because user turns are persisted — re-sends the
// same block on every later turn.
package assembly

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
)

// collectTimeout bounds the git calls. A turn must not stall on a slow or
// pathological repository, and missing context is always better than a hang.
const collectTimeout = 8 * time.Second

// State is a repository's uncommitted state.
type State struct {
	Status   string // `git status --porcelain` output
	DiffStat string // `git diff --stat HEAD` output
}

// Empty reports that the tree is clean, so there is nothing worth describing.
func (s State) Empty() bool {
	return strings.TrimSpace(s.Status) == "" && strings.TrimSpace(s.DiffStat) == ""
}

// cacheTTL collapses the repeated reads a single turn makes: the prompt hook
// and the context snapshot both want this state, and shelling out to git twice
// per turn for the same answer is pure latency. It is short enough that the
// next turn always re-reads, so the model never sees a stale tree.
const cacheTTL = 2 * time.Second

type cacheEntry struct {
	state  State
	readAt time.Time
}

var cache struct {
	mu    sync.Mutex
	roots map[string]cacheEntry
}

// Collect reads the working state of the repository at root. A non-repository,
// a git failure, or a timeout yields an empty State rather than an error:
// callers add this context opportunistically and must proceed without it.
func Collect(ctx context.Context, root string) State {
	root = strings.TrimSpace(root)
	if root == "" {
		return State{}
	}
	if state, ok := cachedState(root); ok {
		return state
	}
	state := readState(ctx, root)
	storeState(root, state)
	return state
}

func cachedState(root string) (State, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.roots[root]
	if !ok || time.Since(entry.readAt) >= cacheTTL {
		return State{}, false
	}
	return entry.state, true
}

func storeState(root string, state State) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.roots == nil {
		cache.roots = map[string]cacheEntry{}
	}
	cache.roots[root] = cacheEntry{state: state, readAt: time.Now()}
}

func readState(ctx context.Context, root string) State {
	ctx, cancel := context.WithTimeout(ctx, collectTimeout)
	defer cancel()

	statusOut, err := exec.CommandContext(ctx, "git", "-C", root, "status", "--porcelain").CombinedOutput()
	if err != nil {
		return State{}
	}
	diffOut, err := exec.CommandContext(ctx, "git", "-C", root, "diff", "--stat", "HEAD").CombinedOutput()
	if err != nil {
		diffOut = nil
	}
	return State{
		Status:   strings.TrimSpace(string(statusOut)),
		DiffStat: strings.TrimSpace(string(diffOut)),
	}
}

// maxRenderedLines caps each section so a repository with thousands of dirty
// files cannot crowd out the conversation.
const maxRenderedLines = 40

// Render formats state as the tagged block injected into a turn. An empty
// state renders as "" so the hook can skip injection entirely.
func Render(state State) string {
	if state.Empty() {
		return ""
	}
	var b strings.Builder
	b.WriteString("<git_context>")
	if status := clampLines(state.Status, maxRenderedLines); status != "" {
		b.WriteString("\nstatus:\n")
		b.WriteString(status)
	}
	if diff := clampLines(state.DiffStat, maxRenderedLines); diff != "" {
		b.WriteString("\ndiff_stat:\n")
		b.WriteString(diff)
	}
	b.WriteString("\n</git_context>")
	return b.String()
}

func clampLines(text string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) <= limit {
		return text
	}
	kept := append([]string(nil), lines[:limit]...)
	kept = append(kept, fmt.Sprintf("… and %d more", len(lines)-limit))
	return strings.Join(kept, "\n")
}

// RootFunc resolves the repository a turn is about. It is a function because
// the active workspace can change between turns.
type RootFunc func(hc hook.HookContext) string

// PreHook returns the shared pre-hook that describes repository state to the
// model. It contributes a system addendum and never rewrites the user's text.
func GitPreHook(root RootFunc) hook.AgentHook {
	return func(ctx context.Context, phase string, hc hook.HookContext, text string) (hook.PreHookResult, error) {
		out := hook.PreHookResult{Text: text}
		if phase != "pre" || root == nil {
			return out, nil
		}
		if !hook.InteractiveTrigger(hc.Trigger) {
			return out, nil
		}
		// Slash commands are dispatched locally and never benefit from it.
		if trimmed := strings.TrimSpace(text); trimmed == "" || strings.HasPrefix(trimmed, "/") {
			return out, nil
		}
		rendered := Render(Collect(ctx, root(hc)))
		if rendered == "" {
			return out, nil
		}
		out.SystemAddendum = rendered
		return out, nil
	}
}
