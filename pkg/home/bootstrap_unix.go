//go:build !windows

package home

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func hasMultipleHardLinks(_ string, info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}

// symlinkResolvedEqual reports whether a path's EvalSymlinks result is
// equivalent to the original (i.e. no symlink redirection occurred). On Unix
// the comparison is exact.
func symlinkResolvedEqual(resolved, original string) bool {
	return resolved == original
}

// checkAncestorSymlinks enables per-ancestor symlink rejection. On Unix every
// parent directory is checked so a symlinked ancestor cannot redirect the
// workspace root.
const checkAncestorSymlinks = true

func openWorkspaceRootNoFollow(root string) (*os.File, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" {
		return nil, errors.New("workspace root is empty")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := fd
	for _, part := range strings.Split(strings.TrimPrefix(filepath.ToSlash(root), "/"), "/") {
		if part == "" || part == "." {
			continue
		}
		next, err := unix.Openat(current, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(current)
		if err != nil {
			return nil, err
		}
		current = next
	}
	f := os.NewFile(uintptr(current), root)
	if f == nil {
		_ = unix.Close(current)
		return nil, errors.New("open workspace root")
	}
	return f, nil
}

func writeFileIfMissingInWorkspace(rootDir *os.File, name, content string) error {
	if rootDir == nil {
		return errors.New("workspace root handle is nil")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("workspace bootstrap file name is empty")
	}
	fd, err := unix.Openat(int(rootDir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
	if err != nil {
		if err == unix.EEXIST {
			return nil
		}
		return err
	}
	f := os.NewFile(uintptr(fd), filepath.Join(rootDir.Name(), name))
	if f == nil {
		_ = unix.Close(fd)
		return errors.New("open workspace bootstrap file")
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}
