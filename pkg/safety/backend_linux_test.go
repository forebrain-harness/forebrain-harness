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

// TestBuildBubblewrapArgsDenyListBoundary pins the mount order a batch task's
// deny-list boundary needs (plans/216): the whole root is a read-only bind, each
// boundary root is a tmpfs over it, and the workspace and the skill roots are
// bound back in *after* their parent — bubblewrap stacks later mounts on
// earlier ones, so that order is what makes an exception an exception.
func TestBuildBubblewrapArgsDenyListBoundary(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "project")
	workspace := filepath.Join(project, "tasks", "T", "1")
	home := filepath.Join(base, "home")
	skillRoots := filepath.Join(home, "skills")
	for _, dir := range []string{workspace, skillRoots} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	args := buildBubblewrapArgs(&appcfg.Root{}, CommandRequest{
		Command:                 "true",
		Profile:                 ProfileWorkspaceWrite,
		AdditionalReadablePaths: []string{string(filepath.Separator)},
		AdditionalWritablePaths: []string{workspace},
		ExclusiveWritablePaths:  true,
		DeniedReadableRoots:     []string{home, project},
		ReadableExceptions:      []string{workspace, skillRoots},
		IncludePlatformDefaults: true,
	}, workspace)

	idx := func(want ...string) int {
		for i := 0; i+len(want) <= len(args); i++ {
			match := true
			for j := range want {
				if args[i+j] != want[j] {
					match = false
					break
				}
			}
			if match {
				return i
			}
		}
		return -1
	}
	rootRead := idx("--ro-bind", "/", "/")
	homeDeny := idx("--tmpfs", home)
	projectDeny := idx("--tmpfs", project)
	workspaceBind := idx("--bind", workspace, workspace)
	skillBind := idx("--ro-bind", skillRoots, skillRoots)
	if rootRead < 0 || homeDeny < 0 || projectDeny < 0 || workspaceBind < 0 || skillBind < 0 {
		t.Fatalf("expected the root read bind, both boundary masks and both exceptions, got: %v", args)
	}
	for name, at := range map[string]int{"the home mask": homeDeny, "the project mask": projectDeny} {
		if at < rootRead {
			t.Fatalf("%s must come after the root bind (last mount wins), root=%d mask=%d", name, rootRead, at)
		}
	}
	for name, at := range map[string]int{"the workspace": workspaceBind, "the skill roots": skillBind} {
		if at < homeDeny || at < projectDeny {
			t.Fatalf("%s must be bound back after the masks, mask=%d/%d bind=%d", name, homeDeny, projectDeny, at)
		}
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

// mountArgIndex is the position of a "--flag value" mount argument, or -1.
func mountArgIndex(args []string, flag, value string) int {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return i
		}
	}
	return -1
}

// TestDeniedDirWithNestedBindDefersRemount pins the ordering bwrap needs
// (plans/150): a denied directory that holds a nested write or read grant is
// made a tmpfs first, the nested mounts are created inside it, and only then is
// the parent remounted read-only. Remounting first leaves nowhere to create the
// nested mount points.
func TestDeniedDirWithNestedBindDefersRemount(t *testing.T) {
	denied := filepath.Join(t.TempDir(), "project")
	workspace := filepath.Join(denied, "tasks", "T", "1")
	skillRoot := filepath.Join(denied, ".agents", "skills")
	for _, dir := range []string{workspace, skillRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	args := appendFilesystemPolicyArgs(nil, CommandRequest{
		AdditionalWritablePaths: []string{workspace},
		AdditionalReadablePaths: []string{skillRoot},
		DeniedReadablePaths:     []string{denied},
	})
	tmpfs := mountArgIndex(args, "--tmpfs", denied)
	writeBind := mountArgIndex(args, "--bind", workspace)
	readBind := mountArgIndex(args, "--ro-bind", skillRoot)
	remount := mountArgIndex(args, "--remount-ro", denied)
	if tmpfs < 0 {
		t.Fatalf("denied directory must become a tmpfs: %v", args)
	}
	if writeBind < 0 || readBind < 0 {
		t.Fatalf("nested grants must still be mounted: %v", args)
	}
	if remount < 0 {
		t.Fatalf("denied directory must still be remounted read-only: %v", args)
	}
	if tmpfs > writeBind || tmpfs > readBind {
		t.Fatalf("the tmpfs must come before the nested mounts: %v", args)
	}
	if remount < writeBind || remount < readBind {
		t.Fatalf("the read-only remount must come after the nested mounts: %v", args)
	}
}

// TestDeniedDirWithoutNestedRuleRemountsImmediately keeps the ordinary shape: a
// denied directory with nothing granted inside it is read-only at once.
func TestDeniedDirWithoutNestedRuleRemountsImmediately(t *testing.T) {
	denied := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(denied, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", denied, err)
	}
	args := appendFilesystemPolicyArgs(nil, CommandRequest{DeniedReadablePaths: []string{denied}})
	tmpfs := mountArgIndex(args, "--tmpfs", denied)
	remount := mountArgIndex(args, "--remount-ro", denied)
	if tmpfs < 0 || remount != tmpfs+2 {
		t.Fatalf("a denied directory with no nested rule must be read-only immediately: %v", args)
	}
}

// A directory marker must stay traversable by the user that created it. The
// deny list that produces one names several paths beneath the same missing
// directory — the git execution surfaces .git/hooks, .git/config, … — and the
// next entry's Lstat walks through the marker as that same user. A 0000 marker
// denied the walk (EACCES, which is not IsNotExist), so the prepare pass
// failed and every sandboxed shell in a workspace without a .git died at its
// first command — but only for a non-root gateway (a container's fixed uid,
// no CAP_DAC_OVERRIDE); root ignores the mode bits, and macOS has no markers.
func TestMissingDenyMarkerDirectoryStaysTraversableForTheNextEntry(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory mode bits; the defect is unprivileged-only")
	}
	root := t.TempDir()

	// The same shape protectedWritePaths emits for a workspace without a .git:
	// siblings beneath one missing directory, in the order they are appended.
	req := CommandRequest{
		Profile:                 ProfileWorkspaceWrite,
		WorkDir:                 root,
		AdditionalWritablePaths: []string{root},
		DeniedWritablePaths: []string{
			filepath.Join(root, ".git", "hooks"),
			filepath.Join(root, ".git", "config"),
			filepath.Join(root, ".git", "config.worktree"),
			filepath.Join(root, ".git", "modules"),
		},
	}
	markers, err := prepareLinuxMissingDenyMarkers(req)
	if err != nil {
		t.Fatalf("prepare over sibling deny paths under one missing directory: %v", err)
	}

	// The marker directory itself is what the next entry had to walk through.
	marker := filepath.Join(root, ".git")
	info, err := os.Lstat(marker)
	if err != nil {
		t.Fatalf("marker directory: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("marker %s is not a directory", marker)
	}
	// 0700: the creator may traverse it. The boundary is the mount table —
	// bwrap covers the marker with a tmpfs or read-only bind — never the
	// host-side mode of a directory that exists for one command.
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("marker mode = %o, want 700 (traversable by its creator)", perm)
	}
	// The walk that used to die: .git/config is itself a denied missing path,
	// so prepare created a file marker for it — reaching it at all is the
	// proof, because that Lstat is the one the 0000 directory used to turn
	// into EACCES.
	if _, err := os.Lstat(filepath.Join(marker, "config")); err != nil {
		t.Fatalf("lstat through the marker = %v, want the sibling file marker (the old failure was EACCES here)", err)
	}

	cleanupLinuxMissingDenyMarkers(markers)
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker survived cleanup: %v", err)
	}
}
