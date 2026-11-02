package safety

import (
	"os"
	"path/filepath"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

// A registered project inside a larger checkout is its own project: the
// registered path is the boundary, and resolving it must not walk up to the
// enclosing repository's .git.
func TestResolveRegisteredProjectKeepsTheRegisteredBoundary(t *testing.T) {
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(checkout, "services", "api")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}

	project, err := ResolveRegisteredProject(inner)
	if err != nil {
		t.Fatal(err)
	}
	canonicalInner, err := CanonicalPath(inner)
	if err != nil {
		t.Fatal(err)
	}
	if project.Root != canonicalInner {
		t.Fatalf("registered root %q was relocated to %q", canonicalInner, project.Root)
	}
	if project.VersionControlled {
		t.Fatalf("a directory without its own .git reported VersionControlled")
	}

	// The launch resolver is the contrasting behavior: from inside the
	// checkout it names the checkout. Both are correct for their callers.
	launch, err := Resolve(inner)
	if err != nil {
		t.Fatal(err)
	}
	canonicalCheckout, err := CanonicalPath(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Root != canonicalCheckout {
		t.Fatalf("launch resolve expected the checkout at %q, got %q", canonicalCheckout, launch.Root)
	}

	own := t.TempDir()
	if err := os.WriteFile(filepath.Join(own, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	vc, err := ResolveRegisteredProject(own)
	if err != nil {
		t.Fatal(err)
	}
	if !vc.VersionControlled {
		t.Fatalf("a worktree-style .git file did not report VersionControlled")
	}
}

// The trust decision for a registered project follows its own root, not the
// checkout it may live in.
func TestResolveRegisteredContextTrustsItsOwnRoot(t *testing.T) {
	home := t.TempDir()
	inner := filepath.Join(t.TempDir(), "sub", "project")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, err := ResolveRegisteredContext(home, inner)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.TrustLevel != LevelUnknown {
		t.Fatalf("expected no trust decision yet, got %q", ctx.TrustLevel)
	}
	if err := MarkTrusted(home, Project{Root: inner}); err != nil {
		t.Fatal(err)
	}
	ctx, err = ResolveRegisteredContext(home, inner)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.TrustLevel != LevelTrusted {
		t.Fatalf("expected the inner root's trust decision, got %q", ctx.TrustLevel)
	}
	if !ctx.IsTrusted() {
		t.Fatalf("expected IsTrusted after MarkTrusted on the registered root")
	}
}

// A project's permission rules live at the root its runtime was given —
// exactly that root — so a project registered inside a trusted checkout
// neither reads nor writes the checkout's rules file.
func TestProjectSettingsPathTakesTheProjectRootAsGiven(t *testing.T) {
	home := t.TempDir()
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := MarkTrusted(home, Project{Root: checkout}); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(checkout, "services", "api")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if path, ok := ProjectSettingsPath(Paths{Home: home, ProjectRoot: inner}); ok {
		t.Fatalf("a subdirectory without its own repository has no project rules, got %q", path)
	}
	path, ok := ProjectSettingsPath(Paths{Home: home, ProjectRoot: checkout})
	if !ok {
		t.Fatal("the trusted checkout itself keeps its rules")
	}
	canonical, err := CanonicalPath(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(canonical, ".forebrain", "safety.json") {
		t.Fatalf("rules path = %q", path)
	}
}

// EffectiveConfig is the one place the approval preset becomes the pair the
// runtime acts on (plan 203). The table is pinned here in full — two presets
// across the three trust decisions — because every cell is a different answer
// to "does this ask, and is it contained".
func TestEffectiveConfigDerivesThePairFromThePreset(t *testing.T) {
	trusted := ProjectContext{Project: Project{Root: t.TempDir(), VersionControlled: true}, TrustLevel: LevelTrusted}
	rejected := ProjectContext{Project: Project{Root: t.TempDir(), VersionControlled: true}, TrustLevel: LevelUntrusted}
	undecided := ProjectContext{Project: Project{Root: t.TempDir(), VersionControlled: true}}

	cases := []struct {
		name       string
		preset     appcfg.ApprovalPresetID
		launch     ProjectContext
		wantPolicy appcfg.ApprovalPolicyMode
		wantMode   appcfg.SandboxMode
	}{
		{"workspace_write/trusted", appcfg.ApprovalPresetWorkspaceWrite, trusted, appcfg.ApprovalPolicyOnRequest, appcfg.SandboxModeWorkspaceWrite},
		{"workspace_write/rejected", appcfg.ApprovalPresetWorkspaceWrite, rejected, appcfg.ApprovalPolicyUntrusted, appcfg.SandboxModeWorkspaceWrite},
		{"workspace_write/undecided", appcfg.ApprovalPresetWorkspaceWrite, undecided, appcfg.ApprovalPolicyOnRequest, appcfg.SandboxModeReadOnly},
		{"full_access/trusted", appcfg.ApprovalPresetFullAccess, trusted, appcfg.ApprovalPolicyNever, appcfg.SandboxModeDangerFullAccess},
		{"full_access/rejected", appcfg.ApprovalPresetFullAccess, rejected, appcfg.ApprovalPolicyNever, appcfg.SandboxModeDangerFullAccess},
		{"full_access/undecided", appcfg.ApprovalPresetFullAccess, undecided, appcfg.ApprovalPolicyNever, appcfg.SandboxModeDangerFullAccess},
		// An unset preset is the default: empty reaches here only from a
		// database whose value predates plan 207.
		{"empty/trusted", "", trusted, appcfg.ApprovalPolicyOnRequest, appcfg.SandboxModeWorkspaceWrite},
		{"empty/rejected", "", rejected, appcfg.ApprovalPolicyUntrusted, appcfg.SandboxModeWorkspaceWrite},
		{"empty/undecided", "", undecided, appcfg.ApprovalPolicyOnRequest, appcfg.SandboxModeReadOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EffectiveConfig(appcfg.Root{ApprovalPreset: tc.preset}, tc.launch)
			if got.ApprovalPolicy.Mode != tc.wantPolicy || got.SandboxMode != tc.wantMode {
				t.Fatalf("policy=%q sandbox=%q, want %q/%q", got.ApprovalPolicy.Mode, got.SandboxMode, tc.wantPolicy, tc.wantMode)
			}
			// The pair is derived in place, so every reader downstream sees the
			// resolved preset rather than an empty string.
			if got.ApprovalPreset != appcfg.ApprovalPresetWorkspaceWrite && got.ApprovalPreset != appcfg.ApprovalPresetFullAccess {
				t.Fatalf("preset = %q, want the effective preset", got.ApprovalPreset)
			}
		})
	}
}
