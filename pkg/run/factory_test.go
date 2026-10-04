package run

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
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

// factoryCodeIntelStub is both ports at once, the way a real lsp.Manager is.
type factoryCodeIntelStub struct{}

func (factoryCodeIntelStub) Handles(string) bool { return false }

func (factoryCodeIntelStub) Query(context.Context, tool.CodeIntelQuery) (tool.CodeIntelResult, error) {
	return tool.CodeIntelResult{}, nil
}

func (factoryCodeIntelStub) DidWrite(context.Context, string, []tool.FileChange) tool.DiagnosticsDelta {
	return tool.DiagnosticsDelta{}
}

func (factoryCodeIntelStub) DidRead(context.Context, string, []byte)  {}
func (factoryCodeIntelStub) DidRunShell(context.Context)              {}
func (factoryCodeIntelStub) PeekLate(string) (string, uint64)         { return "", 0 }
func (factoryCodeIntelStub) AckLate(string, uint64)                   {}
func (factoryCodeIntelStub) Snapshot() event.LSPSnapshot              { return event.LSPSnapshot{} }
func (factoryCodeIntelStub) Subscribe(func(event.LSPSnapshot)) func() { return func() {} }
func (factoryCodeIntelStub) SetEnabled(string, bool) error            { return nil }
func (factoryCodeIntelStub) Restart(string) error                     { return nil }
func (factoryCodeIntelStub) Install(context.Context, string, func(string)) error {
	return nil
}
func (factoryCodeIntelStub) SetRecommendationListener(func(context.Context, event.LSPRecommendation)) {
}
func (factoryCodeIntelStub) DecideRecommendation(string, event.LSPRecommendationChoice) error {
	return nil
}
func (factoryCodeIntelStub) ResetRecommendations() error { return nil }

func TestIsolatedRunnerInheritsCodeIntel(t *testing.T) {
	stub := factoryCodeIntelStub{}
	owner := &Runner{Deps: &Deps{Home: t.TempDir(), CodeIntel: stub, CodeIntelControl: stub, CodeIntelTool: true}}

	r := Factory{Home: owner.Home, Owner: owner}.NewIsolatedRunner("code-intel")
	if r == nil {
		t.Fatal("nil runner")
	}
	if r.CodeIntel != tool.CodeIntelligence(stub) {
		t.Fatal("isolated runner did not inherit CodeIntel")
	}
	if r.CodeIntelControl != tool.CodeIntelControl(stub) {
		t.Fatal("isolated runner did not inherit CodeIntelControl")
	}
	if !r.CodeIntelTool {
		t.Fatal("isolated runner did not inherit CodeIntelTool")
	}

	orphan := Factory{Home: t.TempDir()}.NewIsolatedRunner("code-intel")
	if orphan == nil {
		t.Fatal("nil runner")
	}
	if orphan.CodeIntel != nil || orphan.CodeIntelControl != nil || orphan.CodeIntelTool {
		t.Fatal("a factory without an owner must produce zero-valued code-intel fields")
	}
}
