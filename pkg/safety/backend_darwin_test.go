//go:build darwin

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

func TestNormalizeSeatbeltPathsIncludesPrivateVarVariant(t *testing.T) {
	paths := normalizeSeatbeltPaths([]string{"/var/folders/example/T"})

	assertContains(t, paths, filepath.Clean("/var/folders/example/T"))
	assertContains(t, paths, filepath.Clean("/private/var/folders/example/T"))
}

func TestNormalizeSeatbeltPathsIncludesEvalSymlinkVariant(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	paths := normalizeSeatbeltPaths([]string{link})
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("eval target: %v", err)
	}

	assertContains(t, paths, link)
	assertContains(t, paths, resolvedTarget)
}

func TestSeatbeltProfileAllowsDevNullWrites(t *testing.T) {
	profile, err := seatbeltProfile(&appcfg.Root{}, CommandRequest{Profile: ProfileReadOnly}, t.TempDir())
	if err != nil {
		t.Fatalf("seatbeltProfile: %v", err)
	}
	if !strings.Contains(profile, `(allow file-write-data`) || !strings.Contains(profile, `(path "/dev/null")`) {
		t.Fatalf("expected /dev/null write allow in profile:\n%s", profile)
	}
}

func TestSeatbeltProfileEnforcesDenyRead(t *testing.T) {
	profile, err := seatbeltProfile(&appcfg.Root{}, CommandRequest{
		DeniedReadablePaths:     []string{"/etc/secret"},
		AdditionalReadablePaths: []string{"/etc", "/etc/secret/public"},
	}, t.TempDir())
	if err != nil {
		t.Fatalf("seatbeltProfile: %v", err)
	}
	if !strings.Contains(profile, `(allow file-read* file-test-existence (require-all (subpath "/etc") (require-not (literal "/etc/secret")) (require-not (subpath "/etc/secret"))))`) {
		t.Fatalf("expected denied path excluded from its readable parent:\n%s", profile)
	}
	if !strings.Contains(profile, `(allow file-read* file-test-existence (require-all (subpath "/etc/secret/public")))`) {
		t.Fatalf("expected allow_read carve-out re-allowed:\n%s", profile)
	}
}

func TestSeatbeltProfileUsesClosedDefaultAndExplicitReadRoots(t *testing.T) {
	profile, err := seatbeltProfile(&appcfg.Root{}, CommandRequest{
		Profile:                 ProfileReadOnly,
		AdditionalReadablePaths: []string{"/etc/secret/public"},
	}, t.TempDir())
	if err != nil {
		t.Fatalf("seatbeltProfile: %v", err)
	}
	if !strings.Contains(profile, "(deny default)") || !strings.Contains(profile, `(allow file-read* file-test-existence (require-all (subpath "/etc/secret/public")))`) {
		t.Fatalf("expected closed default with an explicit read root:\n%s", profile)
	}
}

func TestSeatbeltProfileAddsMinimalRuntimeAndDenyGlob(t *testing.T) {
	profile, err := seatbeltProfile(&appcfg.Root{}, CommandRequest{
		Profile:                 ProfileManaged,
		IncludePlatformDefaults: true,
		DeniedReadablePaths:     []string{"/etc/secret"},
		DeniedReadablePatterns:  []string{"/workspace/**/.env"},
	}, "/workspace")
	if err != nil {
		t.Fatalf("seatbeltProfile: %v", err)
	}
	if !strings.Contains(profile, `(subpath "/usr/lib")`) {
		t.Fatalf("expected minimal runtime reads:\n%s", profile)
	}
	if !strings.Contains(profile, `(deny file-read* (regex #"^/workspace/(.*/)?\.env$"))`) {
		t.Fatalf("expected deny glob regex:\n%s", profile)
	}
	if !strings.Contains(profile, `(allow file-write* (require-all (subpath "/tmp")`) {
		t.Fatalf("expected minimal runtime scratch write access:\n%s", profile)
	}
	if !strings.Contains(profile, `(allow file-read* file-test-existence (require-all (subpath "/etc") (require-not (literal "/etc/secret")) (require-not (subpath "/etc/secret"))))`) {
		t.Fatalf("expected explicit deny to narrow platform default roots:\n%s", profile)
	}
}

func TestDarwinSeatbeltIntegrationReleaseGate(t *testing.T) {
	if os.Getenv("FOREBRAIN_SANDBOXRT_INTEGRATION") != "1" {
		t.Skip("set FOREBRAIN_SANDBOXRT_INTEGRATION=1 to run live sandbox-exec checks")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skipf("sandbox-exec unavailable: %v", err)
	}
	cfg := &appcfg.Root{}
	cfg.SandboxMode = appcfg.SandboxModeWorkspaceWrite
	m := NewManager()
	workDir := t.TempDir()
	blockedDir := filepath.Join(workDir, "blocked")
	if err := os.MkdirAll(blockedDir, 0o755); err != nil {
		t.Fatalf("mkdir blocked: %v", err)
	}
	tmpFile := filepath.Join(os.TempDir(), "forebrain-seatbelt-integration-"+time.Now().Format("20060102150405.000000000")+".txt")
	defer os.Remove(tmpFile)

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

	// The project's own repository metadata is an ordinary part of the
	// workspace: git has to take index locks and move refs for builds, tests and
	// commits, so a .git write must succeed inside the sandbox without an
	// escalation prompt. The places git executes from — its hooks and config —
	// and the agent configuration directories stay denied.
	if err := os.MkdirAll(filepath.Join(workDir, ".git", "hooks"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	gitRes, gitDecision, gitErr := m.RunCommand(context.Background(), cfg, CommandRequest{
		ToolKind: ToolKindShell,
		Command:  "printf lock > .git/index.lock",
		WorkDir:  workDir,
		Timeout:  15 * time.Second,
	})
	if gitErr != nil || !gitDecision.UseSandbox || gitRes.ExitCode != 0 {
		t.Fatalf(".git write failed: decision=%+v res=%+v err=%v", gitDecision, gitRes, gitErr)
	}
	if data, err := os.ReadFile(filepath.Join(workDir, ".git", "index.lock")); err != nil || strings.TrimSpace(string(data)) != "lock" {
		t.Fatalf(".git write content mismatch: data=%q err=%v", data, err)
	}

	for _, blocked := range []string{".git/hooks/pre-commit", ".git/config", ".forebrain/skill.md"} {
		blockedRes, blockedDecision, blockedErr := m.RunCommand(context.Background(), cfg, CommandRequest{
			ToolKind: ToolKindShell,
			Command:  "printf evil > " + blocked,
			WorkDir:  workDir,
			Timeout:  15 * time.Second,
		})
		if blockedDecision.UseSandbox && blockedRes.ExitCode == 0 {
			t.Fatalf("write to %s was not blocked: decision=%+v res=%+v err=%v", blocked, blockedDecision, blockedRes, blockedErr)
		}
	}

	tmpRes, tmpDecision, tmpErr := m.RunCommand(context.Background(), cfg, CommandRequest{
		ToolKind: ToolKindShell,
		Command:  "printf tmpok > " + shellQuote(tmpFile),
		WorkDir:  workDir,
		Timeout:  15 * time.Second,
	})
	if tmpErr != nil || !tmpDecision.UseSandbox || tmpRes.ExitCode != 0 {
		t.Fatalf("tmp write failed: decision=%+v res=%+v err=%v", tmpDecision, tmpRes, tmpErr)
	}
	if data, err := os.ReadFile(tmpFile); err != nil || strings.TrimSpace(string(data)) != "tmpok" {
		t.Fatalf("tmp write content mismatch: data=%q err=%v", data, err)
	}

	denyRes, denyDecision, denyErr := m.RunCommand(context.Background(), cfg, CommandRequest{
		ToolKind:            ToolKindShell,
		Command:             "printf blocked > blocked/nope.txt",
		WorkDir:             workDir,
		Timeout:             15 * time.Second,
		DeniedWritablePaths: []string{blockedDir},
	})
	var denied *DeniedError
	if !errors.As(denyErr, &denied) || !denyDecision.UseSandbox || denyRes.ExitCode == 0 {
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
	if !netDecision.UseSandbox || netRes.ExitCode == 0 {
		t.Fatalf("default network was not blocked: decision=%+v res=%+v err=%v", netDecision, netRes, netErr)
	}

	managedWorkDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	managedCfg := &appcfg.Root{
		DefaultPermissions: "managed",
		Permissions: appcfg.PermissionProfiles{"managed": {
			FileSystem: &appcfg.FileSystemPermissions{Entries: map[string]appcfg.FileSystemPermissionValue{
				":minimal":         {Access: appcfg.FileSystemAccessRead},
				":workspace_roots": {Access: appcfg.FileSystemAccessWrite},
			}},
		}},
		SandboxMode: appcfg.SandboxModeWorkspaceWrite,
	}
	managedRes, managedDecision, managedErr := NewManager().RunCommand(context.Background(), managedCfg, CommandRequest{
		ToolKind: ToolKindShell,
		Command:  "cat " + shellQuote(outsideFile),
		WorkDir:  managedWorkDir,
		Timeout:  15 * time.Second,
	})
	if !errors.As(managedErr, &denied) || managedDecision.Profile != ProfileManaged || managedRes.ExitCode == 0 {
		t.Fatalf("managed read boundary failed: decision=%+v res=%+v err=%v", managedDecision, managedRes, managedErr)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
