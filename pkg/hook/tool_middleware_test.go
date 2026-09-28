package hook

import (
	"path/filepath"
	"strings"
	"testing"
)

// artifactPaths returns every per-agent path this package builds for one
// runtime, produced by the real constructors rather than re-joined by the test.
func artifactPaths(t *testing.T, rt *Runtime) map[string]string {
	t.Helper()
	// A nil session store still materialises the file, which is all we need to
	// learn where the writer decided to put it.
	transcript, err := WriteSessionTranscriptArtifact(rt.StateRoot(), nil, "s1")
	if err != nil {
		t.Fatalf("WriteSessionTranscriptArtifact: %v", err)
	}
	return map[string]string{
		"state root":           rt.StateRoot(),
		"hook transcript":      transcript,
		"sidechain transcript": SidechainTranscriptPath(rt.StateRoot(), "s1", "agent-7"),
	}
}

// Hook artifacts replay a session verbatim — prompts, tool arguments, tool
// results. They are tenant data, so every path this package builds must land in
// the owning agent's workspace and never in the shared FOREBRAIN_HOME/state, which
// every primary agent can read.
func TestHookArtifactPathsStayInsideAgentWorkspace(t *testing.T) {
	home := t.TempDir()
	rt := &Runtime{Home: home, WorkspaceRoot: filepath.Join(home, "workspaces", "acme")}

	sharedState := filepath.Join(home, "state")
	for name, path := range artifactPaths(t, rt) {
		if strings.HasPrefix(filepath.Clean(path), sharedState) {
			t.Fatalf("%s = %s leaks into the shared home state tree", name, path)
		}
		if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(rt.WorkspaceRoot)) {
			t.Fatalf("%s = %s is outside the agent workspace %s", name, path, rt.WorkspaceRoot)
		}
	}
}

// Two primary agents sharing a home must not collide on any hook artifact.
func TestHookArtifactPathsAreDisjointAcrossAgents(t *testing.T) {
	home := t.TempDir()
	acme := artifactPaths(t, &Runtime{Home: home, WorkspaceRoot: filepath.Join(home, "workspaces", "acme")})
	globex := artifactPaths(t, &Runtime{Home: home, WorkspaceRoot: filepath.Join(home, "workspaces", "globex")})

	for name, path := range acme {
		if path == globex[name] {
			t.Fatalf("%s is shared between two primary agents: %s", name, path)
		}
	}
}

// A Runtime wired with only Home — the shape every construction site had before
// WorkspaceRoot existed — must still resolve to a workspace rather than the
// shared state tree, so a caller someone forgets to convert degrades to the
// main agent instead of reintroducing the leak.
func TestStateRootFallsBackToMainWorkspaceNotHome(t *testing.T) {
	home := filepath.Join("/tmp", "forebrain-home")
	got := (&Runtime{Home: home}).StateRoot()
	if want := filepath.Join(home, "workspace"); got != want {
		t.Fatalf("StateRoot() = %q, want %q", got, want)
	}
}
