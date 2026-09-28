package run

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

type factoryTestSubagentExecutor struct{}

func (factoryTestSubagentExecutor) RunSubagentExec(context.Context, string, string, string, string, string, string) (string, error) {
	return "", nil
}

func TestNewIsolatedRunnerInheritsSubagentRuntimeFromOwner(t *testing.T) {
	control := NewController()
	owner := &Runner{Deps: &Deps{RunRT: &state.RunStore{}}, SubagentExecutor: factoryTestSubagentExecutor{}, Control: control}

	r := Factory{Home: t.TempDir(), Owner: owner}.NewIsolatedRunner("factory-test")
	if r == nil {
		t.Fatal("nil runner")
	}
	if r.RunRT != owner.RunRT {
		t.Fatal("isolated runner did not inherit RunRT")
	}
	if r.SubagentExecutor == nil {
		t.Fatal("isolated runner did not inherit SubagentExecutor")
	}
	if r.Control != control {
		t.Fatal("isolated runner did not inherit run controller")
	}
}

func TestNewIsolatedRunnerFallsBackToOwnerWorkspaceRoot(t *testing.T) {
	home := t.TempDir()
	ownerWorkspace := filepath.Join(home, "workspaces", "review")
	owner := &Runner{Deps: &Deps{Home: home, WorkspaceRoot: ownerWorkspace}}

	r := Factory{Home: home, Owner: owner}.NewIsolatedRunner("hook")
	if r == nil {
		t.Fatal("nil runner")
	}
	if got := r.WorkspaceRoot; got != ownerWorkspace {
		t.Fatalf("workspace root = %q, want owner workspace %q", got, ownerWorkspace)
	}
	wantStatePrefix := filepath.Join(ownerWorkspace, "state", "subagents")
	if got := r.StateDir; !filepath.IsAbs(got) || filepath.Dir(got) != wantStatePrefix {
		t.Fatalf("state dir = %q, want child under %q", got, wantStatePrefix)
	}
}

func TestNewIsolatedRunnerInheritsOwnerProjectKey(t *testing.T) {
	owner := &Runner{Deps: &Deps{Home: t.TempDir(), ProjectKey: "-Users-ada-work-proj"}}

	r := Factory{Home: owner.Home, Owner: owner}.NewIsolatedRunner("hook")
	if r == nil {
		t.Fatal("nil runner")
	}
	if got := r.ProjectKey; got != owner.ProjectKey {
		t.Fatalf("project key = %q, want %q", got, owner.ProjectKey)
	}
}
