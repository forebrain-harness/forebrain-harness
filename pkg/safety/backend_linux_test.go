//go:build linux

package safety

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

func TestLinuxBubblewrapIntegrationReleaseGate(t *testing.T) {
	if os.Getenv("FOREBRAIN_SANDBOXRT_INTEGRATION") != "1" {
		t.Skip("set FOREBRAIN_SANDBOXRT_INTEGRATION=1 to run live bubblewrap checks")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		if _, err2 := exec.LookPath("bubblewrap"); err2 != nil {
			t.Skipf("bubblewrap unavailable: %v / %v", err, err2)
		}
	}
	cfg := &appcfg.Root{}
	cfg.SandboxMode = appcfg.SandboxModeWorkspaceWrite
	m := NewManager()
	workDir := t.TempDir()
	blockedDir := filepath.Join(workDir, "blocked")
	if err := os.MkdirAll(blockedDir, 0o755); err != nil {
		t.Fatalf("mkdir blocked: %v", err)
	}

	cwdRes, cwdDecision, cwdErr := m.RunCommand(context.Background(), cfg, CommandRequest{
		ToolKind: ToolKindShell,
		Command:  "printf ok > allowed.txt",
		WorkDir:  workDir,
		Timeout:  15 * time.Second,
	})
	if cwdErr != nil || !cwdDecision.UseSandbox || cwdRes.ExitCode != 0 {
		t.Fatalf("cwd write failed: decision=%+v res=%+v err=%v", cwdDecision, cwdRes, cwdErr)
	}
	if data, err := os.ReadFile(filepath.Join(workDir, "allowed.txt")); err != nil || strings.TrimSpace(string(data)) != "ok" {
		t.Fatalf("cwd write content mismatch: data=%q err=%v", data, err)
	}

	denyRes, denyDecision, denyErr := m.RunCommand(context.Background(), cfg, CommandRequest{
		ToolKind:            ToolKindShell,
		Command:             "printf blocked > blocked/nope.txt",
		WorkDir:             workDir,
		Timeout:             15 * time.Second,
		DeniedWritablePaths: []string{blockedDir},
	})
	var deniedErr *DeniedError
	if !errors.As(denyErr, &deniedErr) || !denyDecision.UseSandbox || denyRes.ExitCode == 0 {
		t.Fatalf("deny write did not fail as expected: decision=%+v res=%+v err=%v", denyDecision, denyRes, denyErr)
	}
	if _, err := os.Stat(filepath.Join(blockedDir, "nope.txt")); err == nil {
		t.Fatalf("deny write created blocked file")
	}

	netRes, netDecision, netErr := m.RunCommand(context.Background(), cfg, CommandRequest{
		ToolKind: ToolKindShell,
		Command:  "curl -I https://example.com -m 5",
		WorkDir:  workDir,
		Timeout:  15 * time.Second,
	})
	if netErr != nil || !netDecision.UseSandbox || netRes.ExitCode == 0 {
		t.Fatalf("default network was not blocked: decision=%+v res=%+v err=%v", netDecision, netRes, netErr)
	}
}

func TestBuildBubblewrapArgsUnsharesNetworkByDefault(t *testing.T) {
	args := buildBubblewrapArgs(&appcfg.Root{}, CommandRequest{Command: "true"}, "/workspace")

	assertContains(t, args, "--unshare-net")
}

func TestBuildBubblewrapArgsMasksDeniedReadPaths(t *testing.T) {
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "secret")
	secretDir := filepath.Join(dir, "secretdir")
	if err := os.WriteFile(secretFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := os.Mkdir(secretDir, 0o755); err != nil {
		t.Fatalf("mkdir secretdir: %v", err)
	}

	args := buildBubblewrapArgs(&appcfg.Root{}, CommandRequest{
		Command:             "true",
		DeniedReadablePaths: []string{secretFile, secretDir},
	}, "/workspace")

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--ro-bind /dev/null "+secretFile) {
		t.Fatalf("expected denied read file masked with /dev/null, got: %s", joined)
	}
	if !strings.Contains(joined, "--tmpfs "+secretDir) {
		t.Fatalf("expected denied read dir masked with tmpfs, got: %s", joined)
	}
}

func TestBuildBubblewrapArgsRebindsAllowedReadAfterDeny(t *testing.T) {
	dir := t.TempDir()
	deniedDir := filepath.Join(dir, "denied")
	allowedSub := filepath.Join(deniedDir, "allowed")
	if err := os.MkdirAll(allowedSub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	args := buildBubblewrapArgs(&appcfg.Root{}, CommandRequest{
		Command:                 "true",
		DeniedReadablePaths:     []string{deniedDir},
		AdditionalReadablePaths: []string{allowedSub},
	}, "/workspace")

	denyIdx := -1
	allowIdx := -1
	for i := 0; i+2 < len(args); i++ {
		if args[i] == "--tmpfs" && args[i+1] == deniedDir {
			denyIdx = i
		}
		if args[i] == "--ro-bind" && args[i+1] == allowedSub && args[i+2] == allowedSub {
			allowIdx = i
		}
	}
	if denyIdx < 0 || allowIdx < 0 {
		t.Fatalf("expected both deny tmpfs and allow ro-bind, got: %v", args)
	}
	if allowIdx < denyIdx {
		t.Fatalf("expected allow_read re-bind to come after deny mask (last wins), deny=%d allow=%d", denyIdx, allowIdx)
	}
}

func TestReadOnlyProfileBindsWorkspaceReadOnlyAndOmitsWriteGrants(t *testing.T) {
	workspace := t.TempDir()
	grant := t.TempDir()
	args := buildBubblewrapArgs(&appcfg.Root{}, CommandRequest{
		Command:                 "touch denied",
		Profile:                 ProfileReadOnly,
		AdditionalWritablePaths: []string{grant},
	}, workspace)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--ro-bind "+workspace+" "+workspace) {
		t.Fatalf("read-only workspace must be ro-bound: %s", joined)
	}
	if strings.Contains(joined, "--bind "+workspace+" "+workspace) {
		t.Fatalf("read-only workspace must not be writable: %s", joined)
	}
	if strings.Contains(joined, "--bind "+grant+" "+grant) {
		t.Fatalf("read-only profile must omit write grants: %s", joined)
	}
}

func TestManagedProfileHonorsStructuredWriteGrant(t *testing.T) {
	workspace := t.TempDir()
	grant := t.TempDir()
	args := buildBubblewrapArgs(&appcfg.Root{}, CommandRequest{
		Command:                 "touch allowed",
		Profile:                 ProfileManaged,
		AdditionalReadablePaths: []string{workspace},
		AdditionalWritablePaths: []string{grant},
	}, workspace)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--bind "+grant+" "+grant) {
		t.Fatalf("managed profile must honor structured write grant: %s", joined)
	}
}

func TestDenyWriteOverridesWriteGrantForSamePath(t *testing.T) {
	workspace := t.TempDir()
	args := buildBubblewrapArgs(&appcfg.Root{}, CommandRequest{
		Command:                 "touch blocked",
		Profile:                 ProfileManaged,
		AdditionalReadablePaths: []string{workspace},
		AdditionalWritablePaths: []string{workspace},
		DeniedWritablePaths:     []string{workspace},
	}, workspace)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--bind "+workspace+" "+workspace) {
		t.Fatalf("deny-write path retained a writable mount: %s", joined)
	}
	if !strings.Contains(joined, "--ro-bind "+workspace+" "+workspace) {
		t.Fatalf("deny-write path must remain readable: %s", joined)
	}
}
