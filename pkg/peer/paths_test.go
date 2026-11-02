package peer

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// testDirs is the pair of overridden directories every test uses: the package
// refuses to run in a test binary that has not pointed both of them somewhere
// harmless.
type testDirs struct {
	registry string
	sockets  string
	ccDir    string
	ccSocket string
}

// isolate points every directory this package knows at a fresh temporary tree
// and returns the paths. Nothing a test does can then reach the real FB
// directory, ~/.claude/sessions or /tmp/*-socks.
func isolate(t *testing.T) testDirs {
	t.Helper()
	root := t.TempDir()
	// t.TempDir is too deep on macOS for a socket path (sun_path is 104
	// bytes), so the socket directory is created short under /tmp.
	sockets, err := os.MkdirTemp("/tmp", "fbp")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockets) })
	dirs := testDirs{
		registry: filepath.Join(root, "registry"),
		sockets:  sockets,
		ccDir:    filepath.Join(root, "cc-sessions"),
		ccSocket: filepath.Join(root, "cc-sockets"),
	}
	for _, dir := range []string{dirs.registry, dirs.ccSocket} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv(registryDirEnv, dirs.registry)
	t.Setenv(socketDirEnv, dirs.sockets)
	// The CC overrides stay unset by default: FB mode, and no stat of a real
	// Claude Code directory.
	t.Setenv(ccRegistryDirEnv, "")
	t.Setenv(ccSocketDirEnv, "")
	return dirs
}

func TestEnabledNeedsBothFBDirectoriesInATest(t *testing.T) {
	isolate(t)
	if !Enabled() {
		t.Fatal("Enabled is false with both FB overrides set")
	}

	t.Setenv(socketDirEnv, "")
	if Enabled() {
		t.Fatal("Enabled is true without a socket directory override in a test")
	}

	t.Setenv(registryDirEnv, "")
	t.Setenv(socketDirEnv, "/tmp/somewhere")
	if Enabled() {
		t.Fatal("Enabled is true without a registry directory override in a test")
	}
}

func TestLayoutUsesTheOverriddenDirectories(t *testing.T) {
	dirs := isolate(t)
	layout := NewLayout(false)
	if layout.ClaudeCode {
		t.Fatal("NewLayout(false) reported CC mode")
	}
	if layout.Dir != dirs.registry {
		t.Fatalf("registry dir = %q, want %q", layout.Dir, dirs.registry)
	}
	if layout.SocketDir != dirs.sockets {
		t.Fatalf("socket dir = %q, want %q", layout.SocketDir, dirs.sockets)
	}
	if path := layout.SocketPath(4242); path != filepath.Join(dirs.sockets, "4242.sock") {
		t.Fatalf("socket path = %q", path)
	}
}

func TestClaudeCodeDetected(t *testing.T) {
	dirs := isolate(t)

	if ClaudeCodeDetected() {
		t.Fatal("ClaudeCodeDetected is true with no CC directory override")
	}

	missing := filepath.Join(dirs.ccDir, "absent")
	t.Setenv(ccRegistryDirEnv, missing)
	if ClaudeCodeDetected() {
		t.Fatal("ClaudeCodeDetected is true for a path that does not exist")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("detection created %s", missing)
	}

	if err := os.MkdirAll(dirs.ccDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ccRegistryDirEnv, dirs.ccDir)
	if !ClaudeCodeDetected() {
		t.Fatal("ClaudeCodeDetected is false for an existing directory")
	}

	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ccRegistryDirEnv, file)
	if ClaudeCodeDetected() {
		t.Fatal("ClaudeCodeDetected is true for a regular file")
	}
}

func TestNewLayoutNeedsBothCCOverridesForCCMode(t *testing.T) {
	dirs := isolate(t)

	// Only one of the two CC overrides: the target mode cannot be honoured, so
	// the layout stays in FB mode.
	t.Setenv(ccRegistryDirEnv, dirs.ccDir)
	layout := NewLayout(true)
	if layout.ClaudeCode {
		t.Fatal("NewLayout(true) chose CC mode without a CC socket directory")
	}
	if layout.Dir != dirs.registry || layout.SocketDir != dirs.sockets {
		t.Fatalf("layout fell back to %q / %q", layout.Dir, layout.SocketDir)
	}

	t.Setenv(ccSocketDirEnv, dirs.ccSocket)
	layout = NewLayout(true)
	if !layout.ClaudeCode {
		t.Fatal("NewLayout(true) did not choose CC mode with both overrides set")
	}
	if layout.Dir != dirs.ccDir {
		t.Fatalf("CC registry dir = %q, want %q", layout.Dir, dirs.ccDir)
	}
	if layout.SocketDir != dirs.ccSocket {
		t.Fatalf("CC socket dir = %q, want %q", layout.SocketDir, dirs.ccSocket)
	}

	// The switch is off: FB mode even though CC mode is available.
	layout = NewLayout(false)
	if layout.ClaudeCode {
		t.Fatal("NewLayout(false) reported CC mode")
	}
	if strings.Contains(layout.Dir, "cc-sessions") || strings.Contains(layout.SocketDir, "cc-socks") {
		t.Fatalf("FB layout points at a CC path: %q / %q", layout.Dir, layout.SocketDir)
	}
}

func TestSocketPathFallsBackToTheShortNamespace(t *testing.T) {
	dirs := isolate(t)
	deep := dirs.sockets + strings.Repeat("/deeper", 20)

	t.Setenv(socketDirEnv, deep)
	fb := NewLayout(false)
	fbPath := fb.SocketPath(4242)
	if want := "/tmp/fb-socks-" + strconv.Itoa(selfUID()) + "/4242.sock"; fbPath != want {
		t.Fatalf("FB fallback = %q, want %q", fbPath, want)
	}
	if _, err := os.Stat(deep); !os.IsNotExist(err) {
		t.Fatalf("SocketPath created %s", deep)
	}

	t.Setenv(ccRegistryDirEnv, dirs.ccDir)
	t.Setenv(ccSocketDirEnv, deep)
	cc := NewLayout(true)
	ccPath := cc.SocketPath(4242)
	if want := "/tmp/cc-socks-" + strconv.Itoa(selfUID()) + "/4242.sock"; ccPath != want {
		t.Fatalf("CC fallback = %q, want %q", ccPath, want)
	}
}

func TestVetSocketDir(t *testing.T) {
	root := t.TempDir()

	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := vetSocketDir(private); err != nil {
		t.Fatalf("a 0700 directory was rejected: %v", err)
	}

	open := filepath.Join(root, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := vetSocketDir(open); err == nil {
		t.Fatal("a 0777 directory without the sticky bit was accepted")
	}

	sticky := filepath.Join(root, "sticky")
	if err := os.Mkdir(sticky, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sticky, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	if err := vetSocketDir(sticky); err != nil {
		t.Fatalf("a 1777 directory was rejected: %v", err)
	}

	link := filepath.Join(root, "link")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	if err := vetSocketDir(link); err == nil {
		t.Fatal("a symbolic link was accepted as a socket directory")
	}

	if err := vetSocketDir(filepath.Join(root, "missing")); err == nil {
		t.Fatal("a missing directory was accepted")
	}
}

func TestPrepareNeverCreatesTheClaudeCodeDirectory(t *testing.T) {
	dirs := isolate(t)

	fb := NewLayout(false)
	if err := fb.Prepare(); err != nil {
		t.Fatalf("FB Prepare: %v", err)
	}
	for _, dir := range []string{dirs.registry, dirs.sockets} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("FB Prepare did not create %s: %v", dir, err)
		}
	}

	// The CC rendezvous directory does not exist and the switch is on: mode
	// choice still lands in FB mode (no CC overrides), and Prepare leaves the
	// absent directory absent.
	t.Setenv(ccRegistryDirEnv, dirs.ccDir)
	t.Setenv(ccSocketDirEnv, dirs.ccSocket)
	if err := os.RemoveAll(dirs.ccSocket); err != nil {
		t.Fatal(err)
	}
	cc := NewLayout(true)
	if !cc.ClaudeCode {
		t.Fatal("CC mode was not chosen with both overrides set")
	}
	if err := cc.Prepare(); err != nil {
		t.Fatalf("CC Prepare: %v", err)
	}
	if info, err := os.Stat(dirs.ccSocket); err != nil || !info.IsDir() {
		t.Fatalf("CC Prepare did not create the socket directory: %v", err)
	}
	if _, err := os.Stat(dirs.ccDir); !os.IsNotExist(err) {
		t.Fatalf("CC Prepare created Claude Code's rendezvous directory %s", dirs.ccDir)
	}
}
