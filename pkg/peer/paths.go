package peer

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Environment variables that move the rendezvous directories. They exist so a
// second machine-local deployment, and every test, can point the protocol
// somewhere harmless; a normal run sets none of them.
const (
	// registryDirEnv overrides the FB rendezvous directory.
	registryDirEnv = "FOREBRAIN_PEERS_DIR"
	// socketDirEnv overrides the FB socket directory itself.
	socketDirEnv = "FOREBRAIN_PEER_SOCKET_DIR"
	// ccRegistryDirEnv overrides the whole CC rendezvous directory path.
	ccRegistryDirEnv = "FOREBRAIN_PEERS_CC_DIR"
	// ccSocketDirEnv overrides the CC socket directory itself.
	ccSocketDirEnv = "FOREBRAIN_PEER_CC_SOCKET_DIR"

	// claudeConfigDirEnv is Claude Code's own configuration directory; its
	// rendezvous directory is "<it>/sessions".
	claudeConfigDirEnv = "CLAUDE_CONFIG_DIR"
	// claudeTmpDirEnv is Claude Code's own temp directory override, which
	// replaces /tmp for its socket namespace.
	claudeTmpDirEnv = "CLAUDE_CODE_TMPDIR"
	// runtimeDirEnv is the XDG runtime directory both namespaces prefer.
	runtimeDirEnv = "XDG_RUNTIME_DIR"
)

// maxSocketPath is the longest socket path a local socket accepts: sun_path is
// 104 bytes including the terminator. Claude Code falls back to a
// /tmp/<namespace>-<uid> directory past this length so the path stays short on
// macOS, whose default temp directory is deep; we do the same.
const maxSocketPath = 103

// directoryMode is the mode of every directory this package creates. Records
// are 0644 (like Claude Code's) and key files 0600.
const directoryMode os.FileMode = 0o700

// Enabled reports whether this process may run the peer service at all.
//
// Cross-process coordination is always on (owner ruling 2026-10-31), so this
// is not a switch: it answers a capability question (the protocol is
// implemented for darwin and linux only) and a test-safety question (a test
// binary that has not pointed both FB directories somewhere of its own would
// otherwise read and write the user's real entries).
func Enabled() bool {
	switch runtime.GOOS {
	case "darwin", "linux":
	default:
		return false
	}
	if testing.Testing() {
		if _, ok := envValue(registryDirEnv); !ok {
			return false
		}
		if _, ok := envValue(socketDirEnv); !ok {
			return false
		}
	}
	return true
}

// ClaudeCodeDetected reports whether Claude Code is installed on this machine,
// which the surfaces use to decide whether to show the interop switch at all.
//
// The test is the existence of Claude Code's rendezvous directory: it is the
// directory interop would use, so its absence means interop is impossible,
// while "claude is on PATH" would answer differently for a TUI and a gateway
// with different environments. Only the stat happens here — nothing is read,
// written or created.
func ClaudeCodeDetected() bool {
	if testing.Testing() {
		if _, ok := envValue(ccRegistryDirEnv); !ok {
			return false
		}
	}
	dir, ok := ccRegistryDir()
	if !ok {
		return false
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// Layout is the one directory pair a process registers, advertises and listens
// in. ClaudeCode says which of the two modes it is: true is CC mode (Claude
// Code's directory and socket namespace), false is FB mode.
type Layout struct {
	ClaudeCode bool
	Dir        string
	SocketDir  string
}

// NewLayout builds the layout for the mode the caller decided on. claudeCode
// is the target mode computed by the caller — the interop switch is on and
// ClaudeCodeDetected reported true — not a raw switch value.
//
// It only reads the environment and never touches the file system. A test
// binary that has not overridden both CC paths is forced into FB mode, so no
// test can reach the real ~/.claude/sessions or /tmp/cc-socks.
func NewLayout(claudeCode bool) Layout {
	if claudeCode && testing.Testing() {
		if _, ok := envValue(ccRegistryDirEnv); !ok {
			claudeCode = false
		} else if _, ok := envValue(ccSocketDirEnv); !ok {
			claudeCode = false
		}
	}
	if claudeCode {
		dir, _ := ccRegistryDir()
		socketDir, _ := ccSocketDir()
		return Layout{ClaudeCode: true, Dir: dir, SocketDir: socketDir}
	}
	dir, _ := fbRegistryDir()
	socketDir, _ := fbSocketDir()
	return Layout{ClaudeCode: false, Dir: dir, SocketDir: socketDir}
}

// Prepare creates the directories this layout writes in, 0700, and returns the
// first error. FB mode creates its rendezvous directory; CC mode never does —
// Claude Code's directory missing is an error the caller reports, not a
// directory this package invents.
func (l Layout) Prepare() error {
	if !l.ClaudeCode {
		if err := os.MkdirAll(l.Dir, directoryMode); err != nil {
			return err
		}
	}
	return os.MkdirAll(l.SocketDir, directoryMode)
}

// SocketPath is where this layout listens for pid. It only builds the string —
// nothing is created — and falls back to the short /tmp namespace when the
// derived path is too long for a socket address.
func (l Layout) SocketPath(pid int) string {
	name := strconv.Itoa(pid) + ".sock"
	path := filepath.Join(l.SocketDir, name)
	if len(path) <= maxSocketPath {
		return path
	}
	namespace := "fb-socks"
	if l.ClaudeCode {
		namespace = "cc-socks"
	}
	return filepath.Join("/tmp", namespace+"-"+strconv.Itoa(selfUID()), name)
}

// vetSocketDir checks the directory a socket is about to be created in: it
// must be a real directory (not a symlink), owned by this user or root, and
// writable by group or others only when it carries the sticky bit. A directory
// that fails is a directory another user could swap sockets out of, so the
// service refuses to start rather than listen there.
func vetSocketDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("peer: socket directory " + dir + " is not a directory")
	}
	uid, ok := fileOwnerUID(info)
	if !ok {
		return errors.New("peer: cannot read the owner of socket directory " + dir)
	}
	if self := selfUID(); uid != self && uid != 0 {
		return errors.New("peer: socket directory " + dir + " belongs to another user")
	}
	if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
		return errors.New("peer: socket directory " + dir + " is world-writable without the sticky bit")
	}
	return nil
}

// fbRegistryDir is forebrain's own rendezvous directory, machine-wide for this
// user so that processes with different FOREBRAIN_HOMEs still meet.
func fbRegistryDir() (string, bool) {
	if dir, ok := envValue(registryDirEnv); ok {
		return filepath.Clean(dir), true
	}
	cache, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(cache) == "" {
		return "", false
	}
	return filepath.Join(cache, "forebrain", "peers"), true
}

// fbSocketDir is forebrain's own socket namespace.
func fbSocketDir() (string, bool) {
	if dir, ok := envValue(socketDirEnv); ok {
		return filepath.Clean(dir), true
	}
	if base, ok := envValue(runtimeDirEnv); ok {
		return filepath.Join(base, "fb-socks"), true
	}
	return filepath.Join("/tmp", "fb-socks-"+strconv.Itoa(selfUID())), true
}

// ccRegistryDir is Claude Code's rendezvous directory. This package writes
// there only when it already exists.
func ccRegistryDir() (string, bool) {
	if dir, ok := envValue(ccRegistryDirEnv); ok {
		return filepath.Clean(dir), true
	}
	if dir, ok := envValue(claudeConfigDirEnv); ok {
		return filepath.Join(dir, "sessions"), true
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", false
	}
	return filepath.Join(home, ".claude", "sessions"), true
}

// ccSocketDir is Claude Code's socket namespace, which our CC-mode sockets must
// live in: a reply or idle notice is only accepted from an address inside it.
func ccSocketDir() (string, bool) {
	if dir, ok := envValue(ccSocketDirEnv); ok {
		return filepath.Clean(dir), true
	}
	base := "/tmp"
	if dir, ok := envValue(runtimeDirEnv); ok {
		base = dir
	} else if dir, ok := envValue(claudeTmpDirEnv); ok {
		base = dir
	}
	return filepath.Join(base, "cc-socks"), true
}

// envValue reads an override, treating an empty value as unset.
func envValue(name string) (string, bool) {
	value := strings.TrimSpace(os.Getenv(name))
	return value, value != ""
}
