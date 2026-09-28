package safety

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

func TestDecideShell(t *testing.T) {
	m := NewManager()
	cfg := &appcfg.Root{}
	cfg.SandboxMode = appcfg.SandboxModeWorkspaceWrite
	d := m.Decide(cfg, ToolKindShell)
	if d.Mode != ModeSandboxed {
		t.Fatalf("expected sandboxed shell, got %+v", d)
	}
	if d.UnavailableReason != "" {
		if d.UseSandbox {
			t.Fatalf("expected unavailable sandbox to stay disabled, got %+v", d)
		}
	} else if !d.UseSandbox {
		t.Fatalf("expected sandboxed shell, got %+v", d)
	}
	cfg.SandboxMode = appcfg.SandboxModeDangerFullAccess
	d = m.Decide(cfg, ToolKindShell)
	if d.UseSandbox || d.Mode != ModeDangerFullAccess {
		t.Fatalf("expected host shell mode, got %+v", d)
	}
}

func TestSelectedPermissionConfigurationTakesPrecedenceOverSandboxMode(t *testing.T) {
	cfg := &appcfg.Root{
		DefaultPermissions: "restricted",
		Permissions: appcfg.PermissionProfiles{"restricted": {
			FileSystem: &appcfg.FileSystemPermissions{Entries: map[string]appcfg.FileSystemPermissionValue{
				":root": {Access: appcfg.FileSystemAccessRead},
			}},
		}},
		SandboxMode: appcfg.SandboxModeDangerFullAccess,
	}
	decision := NewManager().Decide(cfg, ToolKindShell)
	if decision.Mode != ModeSandboxed {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestMissingSandboxConfigFailsClosed(t *testing.T) {
	decision := NewManager().DecideShellCommand(nil, "pwd", SandboxPermissionsUseDefault, ProfileWorkspaceWrite, false)
	if decision.UseSandbox || decision.Mode != ModeSandboxed || decision.Reason != "sandbox_unavailable" || decision.UnavailableReason == "" {
		t.Fatalf("missing config must not fall back to host execution: %+v", decision)
	}
}

func TestProfileForConfigRawOmissionFailsClosed(t *testing.T) {
	if got := ProfileForConfig(&appcfg.Root{}); got != ProfileReadOnly {
		t.Fatalf("raw omitted config profile = %q", got)
	}
	trusted := EffectiveConfig(appcfg.Root{}, ProjectContext{Project: Project{VersionControlled: true}, TrustLevel: LevelTrusted})
	if got := ProfileForConfig(&trusted); got != ProfileWorkspaceWrite {
		t.Fatalf("resolved trusted config profile = %q", got)
	}
	if got := ProfileForConfig(&appcfg.Root{SandboxMode: appcfg.SandboxModeReadOnly}); got != ProfileReadOnly {
		t.Fatalf("explicit read-only profile = %q", got)
	}
}

func TestDecideShellCommandRequireEscalated(t *testing.T) {
	m := NewManager()
	cfg := &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite}
	d := m.DecideShellCommand(cfg, "go test ./...", SandboxPermissionsRequireEscalated, ProfileWorkspaceWrite, false)
	if d.UseSandbox || d.Mode != ModeDangerFullAccess || d.Reason != "require_escalated" {
		t.Fatalf("expected escalated execution to use the host, got %+v", d)
	}
	if !d.RequiresPermission {
		t.Fatalf("expected escalated execution to require approval, got %+v", d)
	}
}

func TestUserShellAlwaysUsesFullAccess(t *testing.T) {
	m := NewManager()
	cfg := &appcfg.Root{SandboxMode: appcfg.SandboxModeReadOnly}
	d := m.Decide(cfg, ToolKindUserShell)
	if d.UseSandbox || d.Mode != ModeDangerFullAccess || d.Reason != "user_shell" {
		t.Fatalf("user shell must use full access, got %+v", d)
	}
}

func TestDecideReportsUnavailableReasonWhenSandboxBackendMissing(t *testing.T) {
	t.Setenv("PATH", "")
	m := NewManager()
	cfg := &appcfg.Root{}
	cfg.SandboxMode = appcfg.SandboxModeWorkspaceWrite
	d := m.Decide(cfg, ToolKindShell)
	if !strings.Contains(d.UnavailableReason, "_not_found") {
		t.Fatalf("expected *_not_found, got %+v", d)
	}
}

func TestStartupCheckFailsWhenSandboxEnabledAndBackendMissing(t *testing.T) {
	t.Setenv("PATH", "")
	m := NewManager()
	cfg := &appcfg.Root{}
	cfg.SandboxMode = appcfg.SandboxModeWorkspaceWrite
	if err := m.StartupCheck(cfg); err == nil {
		t.Fatalf("expected startup check failure")
	}
}

func TestStartupCheckPassesWhenSandboxDisabled(t *testing.T) {
	t.Setenv("PATH", "")
	m := NewManager()
	cfg := &appcfg.Root{}
	cfg.SandboxMode = appcfg.SandboxModeDangerFullAccess
	if err := m.StartupCheck(cfg); err != nil {
		t.Fatalf("expected no startup failure when sandbox disabled, got %v", err)
	}
}

func TestRunCommandReturnsExecutorTimingOnSuccessAndFailure(t *testing.T) {
	m := NewManager()
	cfg := &appcfg.Root{}
	cfg.SandboxMode = appcfg.SandboxModeDangerFullAccess

	res, _, err := m.RunCommand(context.Background(), cfg, CommandRequest{
		ToolKind: ToolKindShell,
		Command:  "printf ok",
		WorkDir:  t.TempDir(),
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatalf("success RunCommand: %v", err)
	}
	if !res.ExecutionTiming.Valid() {
		t.Fatalf("success timing invalid: %#v", res.ExecutionTiming)
	}

	res, _, err = m.RunCommand(context.Background(), cfg, CommandRequest{
		ToolKind: ToolKindShell,
		Command:  "sleep 5",
		WorkDir:  t.TempDir(),
		Timeout:  50 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected command error")
	}
	if !res.ExecutionTiming.Valid() {
		t.Fatalf("failure timing invalid: %#v", res.ExecutionTiming)
	}
}

func TestRunCommandStreamsOutputBeforeCompletion(t *testing.T) {
	m := NewManager()
	cfg := &appcfg.Root{}
	cfg.SandboxMode = appcfg.SandboxModeDangerFullAccess

	firstChunk := make(chan string, 1)
	done := make(chan CommandResult, 1)
	errs := make(chan error, 1)
	go func() {
		res, _, err := m.RunCommand(context.Background(), cfg, CommandRequest{
			ToolKind: ToolKindShell,
			Command:  "printf first; sleep 0.5; printf second >&2",
			WorkDir:  t.TempDir(),
			Timeout:  3 * time.Second,
			OnOutput: func(stream OutputStream, chunk []byte) {
				select {
				case firstChunk <- string(stream) + ":" + string(chunk):
				default:
				}
			},
		})
		done <- res
		errs <- err
	}()

	select {
	case got := <-firstChunk:
		if got != "stdout:first" {
			t.Fatalf("first streamed chunk = %q, want stdout:first", got)
		}
	case <-done:
		t.Fatal("command completed before its first output was streamed")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for live command output")
	}

	res := <-done
	if err := <-errs; err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if res.Stdout != "first" || res.Stderr != "second" {
		t.Fatalf("complete output changed: stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
}

func TestRunCommandFailsClosedWhenBackendUnavailable(t *testing.T) {
	// The LLM-driven shell tool must never silently drop to unsandboxed host
	// execution when the backend is missing. It always fails closed whenever
	// the sandbox is enabled.
	t.Setenv("PATH", "")
	m := NewManager()
	cfg := &appcfg.Root{}
	cfg.SandboxMode = appcfg.SandboxModeWorkspaceWrite
	_, decision, err := m.RunCommand(context.Background(), cfg, CommandRequest{
		ToolKind: ToolKindShell,
		Command:  "printf should-not-run",
		WorkDir:  t.TempDir(),
		Timeout:  10 * time.Second,
	})
	if decision.UnavailableReason == "" {
		t.Skipf("backend available in this environment, cannot exercise fail-closed: %+v", decision)
	}
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("expected UnavailableError, got %v", err)
	}
}

func TestRunCommandUsesConfiguredProfileWhenRequestOmitsIt(t *testing.T) {
	t.Setenv("PATH", "")
	cfg := &appcfg.Root{SandboxMode: appcfg.SandboxModeReadOnly}
	_, decision, _ := NewManager().RunCommand(context.Background(), cfg, CommandRequest{
		ToolKind: ToolKindShell,
		Command:  "pwd",
		WorkDir:  t.TempDir(),
	})
	if decision.Profile != ProfileReadOnly {
		t.Fatalf("profile=%q want=%q", decision.Profile, ProfileReadOnly)
	}
}

func TestApplyRuntimeConfigToRequestCarriesCompleteFilesystemPolicy(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "secret")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	depth := 2
	req, err := applyRuntimeConfigToRequest(CommandRequest{}, RuntimeConfig{
		Filesystem: FilesystemConfig{
			AllowRead:               []string{root},
			AllowWrite:              []string{filepath.Join(root, "out")},
			DenyRead:                []string{secret},
			DenyWrite:               []string{filepath.Join(root, "locked")},
			DenyReadPatterns:        []string{filepath.Join(root, "sec*")},
			GlobScanMaxDepth:        &depth,
			IncludePlatformDefaults: true,
		},
		NetworkEnabled: true,
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, req.AdditionalReadablePaths, root)
	assertContains(t, req.AdditionalWritablePaths, filepath.Join(root, "out"))
	assertContains(t, req.DeniedReadablePaths, secret)
	assertContains(t, req.DeniedWritablePaths, filepath.Join(root, "locked"))
	assertContains(t, req.DeniedReadablePatterns, filepath.Join(root, "sec*"))
	if !req.IncludePlatformDefaults || !req.AllowNetwork {
		t.Fatalf("request=%+v", req)
	}
}

// A request that resolved its own write set must keep exactly that set. The
// caller that does this promotes a read-only request to the managed profile to
// name the exceptions to a strict boundary, and the configured workspace the
// boundary exists to exclude must not come back in through the merge.
func TestApplyRuntimeConfigToRequestKeepsAnExclusiveWriteSet(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	cache := filepath.Join(root, "cache", "go")
	locked := filepath.Join(root, "locked")
	runtimeCfg := RuntimeConfig{Filesystem: FilesystemConfig{
		AllowWrite: []string{workspace, filepath.Clean(os.TempDir())},
		AllowRead:  []string{root},
		DenyWrite:  []string{locked},
		DenyRead:   []string{locked},
	}}

	exclusive, err := applyRuntimeConfigToRequest(CommandRequest{
		AdditionalWritablePaths: []string{cache},
		ExclusiveWritablePaths:  true,
	}, runtimeCfg, root)
	if err != nil {
		t.Fatalf("applyRuntimeConfigToRequest: %v", err)
	}
	if len(exclusive.AdditionalWritablePaths) != 1 || exclusive.AdditionalWritablePaths[0] != cache {
		t.Fatalf("exclusive write set = %v, want just %q", exclusive.AdditionalWritablePaths, cache)
	}
	// Reads and denials are the boundary being kept, so they still apply.
	assertContains(t, exclusive.AdditionalReadablePaths, root)
	assertContains(t, exclusive.DeniedWritablePaths, locked)

	// Without the flag the configured roots are what the request runs with.
	inherited, err := applyRuntimeConfigToRequest(CommandRequest{
		AdditionalWritablePaths: []string{cache},
	}, runtimeCfg, root)
	if err != nil {
		t.Fatalf("applyRuntimeConfigToRequest: %v", err)
	}
	assertContains(t, inherited.AdditionalWritablePaths, cache)
	assertContains(t, inherited.AdditionalWritablePaths, workspace)
	assertContains(t, inherited.AdditionalWritablePaths, filepath.Clean(os.TempDir()))
}

func TestStatusReflectsSandboxMode(t *testing.T) {
	m := NewManager()
	cfg := &appcfg.Root{}
	cfg.SandboxMode = appcfg.SandboxModeWorkspaceWrite
	st := m.Status(cfg)
	if !st.EffectiveEnabled && st.UnavailableReason == "" {
		t.Fatalf("unexpected status flags: %+v", st)
	}
}

func TestStatusReportsSandboxSettingsLockedByPolicy(t *testing.T) {
	m := NewManager()
	cfg := &appcfg.Root{}
	cfg.SandboxMode = appcfg.SandboxModeWorkspaceWrite
	UpdateManagerWithLocalConfig(m, t.TempDir(), cfg, Snapshot{}, nil)
	if st := m.Status(cfg); st.SettingsLocked {
		t.Fatalf("local settings alone must not lock sandbox UI: %+v", st)
	}

	m.UpdateConfig(cfg, []SourceSettings{{
		Source:      SourcePolicySettings,
		RootPath:    t.TempDir(),
		SandboxMode: appcfg.SandboxModeWorkspaceWrite,
	}}, Snapshot{}, t.TempDir(), t.TempDir(), t.TempDir(), nil)
	if st := m.Status(cfg); !st.SettingsLocked {
		t.Fatalf("policy sandbox settings must be reported as locked: %+v", st)
	}
}

func TestDoctorReportsPlatformAndWarnings(t *testing.T) {
	m := NewManager()
	rep := m.Doctor(&appcfg.Root{})
	if rep.Platform == "" {
		t.Fatalf("expected platform in doctor report: %+v", rep)
	}
	if rep.Backend == "" {
		t.Fatalf("expected backend in doctor report: %+v", rep)
	}
}

func TestUpdateConfigAndRefreshConfigCacheRuntimeConfig(t *testing.T) {
	m := NewManager()
	home := t.TempDir()
	cfg := &appcfg.Root{}
	cfg.SandboxWorkspaceWrite.WritableRoots = []string{"./cache"}
	snap := Snapshot{}
	rc := UpdateManagerWithLocalConfig(m, home, cfg, snap, nil)
	if len(rc.Filesystem.AllowWrite) == 0 {
		t.Fatalf("expected allow_write entries, got %+v", rc)
	}
	cfg.SandboxWorkspaceWrite.WritableRoots = []string{"./cache2"}
	rc = m.UpdateConfig(cfg, LocalConfigSources(home, cfg), snap, home, home, t.TempDir(), nil)
	refreshed := m.RefreshConfig()
	if len(refreshed.Filesystem.AllowWrite) == 0 || len(rc.Filesystem.AllowWrite) == 0 {
		t.Fatalf("expected refreshed runtime config, got rc=%+v refreshed=%+v", rc, refreshed)
	}
}

func TestStatusUsesCachedConfigWhenNilProvided(t *testing.T) {
	m := NewManager()
	cfg := &appcfg.Root{}
	cfg.SandboxMode = appcfg.SandboxModeWorkspaceWrite
	UpdateManagerWithLocalConfig(m, t.TempDir(), cfg, Snapshot{}, nil)
	st := m.Status(nil)
	if st.Mode != ModeSandboxed {
		t.Fatalf("expected cached status to report sandboxed mode, got %+v", st)
	}
}

// TestRunHostCommandCancellation verifies that cancelling the context during
// a host shell command terminates the process group (including child processes)
// and returns the context error promptly, not after the child process finishes.
func TestRunHostCommandCancellation(t *testing.T) {
	if detectPlatform() != PlatformDarwin && detectPlatform() != PlatformLinux && detectPlatform() != PlatformWSL {
		t.Skip("process group cancel test requires Unix process groups")
	}
	m := NewManager()
	cfg := &appcfg.Root{}
	cfg.SandboxMode = appcfg.SandboxModeDangerFullAccess

	ctx, cancel := context.WithCancel(context.Background())
	// Run a command that spawns a grandchild that sleeps for a long time.
	// If process-group kill works, the cancel should return quickly (< 2s).
	// If only the direct shell is killed, Wait would block for the sleep.
	done := make(chan struct{})
	go func() {
		_, _, err := m.RunCommand(ctx, cfg, CommandRequest{
			ToolKind: ToolKindShell,
			Command:  "sh -c 'sleep 30 & sleep 30 & wait'",
			WorkDir:  t.TempDir(),
			Timeout:  60 * time.Second,
		})
		if err == nil {
			t.Error("expected cancellation error")
		} else if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
		close(done)
	}()

	// Give the command a moment to start its children.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Good — returned promptly.
	case <-time.After(5 * time.Second):
		t.Fatal("RunCommand did not return within 5s after cancellation — " +
			"child processes may not have been killed")
	}
}

// TestRunHostCommandCancellationBeforeStart verifies that when the context is
// already cancelled before the command starts, RunCommand returns immediately
// without executing anything.
func TestRunHostCommandCancellationBeforeStart(t *testing.T) {
	m := NewManager()
	cfg := &appcfg.Root{}
	cfg.SandboxMode = appcfg.SandboxModeDangerFullAccess

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel before RunCommand.

	_, _, err := m.RunCommand(ctx, cfg, CommandRequest{
		ToolKind: ToolKindShell,
		Command:  "printf should-not-run",
		WorkDir:  t.TempDir(),
		Timeout:  10 * time.Second,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestRunHostCommandTimeout verifies that a timeout (DeadlineExceeded) is
// returned when the command exceeds its timeout and the process group is
// properly terminated.
func TestRunHostCommandTimeout(t *testing.T) {
	m := NewManager()
	cfg := &appcfg.Root{}
	cfg.SandboxMode = appcfg.SandboxModeDangerFullAccess

	_, _, err := m.RunCommand(context.Background(), cfg, CommandRequest{
		ToolKind: ToolKindShell,
		Command:  "sleep 60",
		WorkDir:  t.TempDir(),
		Timeout:  100 * time.Millisecond,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

// TestSandboxStatusLineReadsTheEffectiveStatus pins the /status sandbox row to
// the status /sandbox reports: a full-access configuration is never shown as
// enforced by the platform backend, and a named profile outranks sandbox_mode.
func TestSandboxStatusLineReadsTheEffectiveStatus(t *testing.T) {
	full := &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}
	if got := SandboxStatusLine(full); got != "danger-full-access · not enforced" {
		t.Fatalf("full access line = %q", got)
	}
	profile := &appcfg.Root{SandboxMode: appcfg.SandboxModeWorkspaceWrite, DefaultPermissions: appcfg.PermissionProfileDangerFullAccess}
	if got := SandboxModeLabel(profile); got != appcfg.PermissionProfileDangerFullAccess {
		t.Fatalf("profile label = %q, want the profile to outrank sandbox_mode", got)
	}
	if got := SandboxStatusLine(profile); got != appcfg.PermissionProfileDangerFullAccess+" · not enforced" {
		t.Fatalf("profile line = %q", got)
	}
}
