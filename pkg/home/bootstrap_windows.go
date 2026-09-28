//go:build windows

package home

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func hasMultipleHardLinks(path string, _ os.FileInfo) bool {
	// Windows exposes the hard-link count via GetFileInformationByHandle's
	// NumberOfLinks field (NTFS). Open the file and query it so the workspace
	// bootstrap rejects hard-linked files the same way it does on Unix.
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	h, err := syscall.CreateFile(
		p,
		0, // query metadata only; no read/write access needed
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)

	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		return false
	}
	return info.NumberOfLinks > 1
}

// symlinkResolvedEqual reports whether a path's EvalSymlinks result points at
// the same directory as the original. On Windows, EvalSymlinks also expands
// 8.3 short names (RUNNER~1) and traverses system junctions found in temp and
// profile paths, so a plain string compare yields false positives. We compare
// by file identity (os.SameFile) and fall back to a case-insensitive,
// separator-normalized path compare.
func symlinkResolvedEqual(resolved, original string) bool {
	if strings.EqualFold(filepath.Clean(resolved), filepath.Clean(original)) {
		return true
	}
	ri, err1 := os.Stat(resolved)
	oi, err2 := os.Stat(original)
	if err1 == nil && err2 == nil {
		return os.SameFile(ri, oi)
	}
	return false
}

// checkAncestorSymlinks disables per-ancestor symlink rejection on Windows.
// Standard Windows temp and profile paths legitimately traverse system
// junctions (a reparse-point class), which would otherwise be misflagged as a
// security violation. The workspace root itself is still checked for being a
// symlink, and the root's EvalSymlinks result is verified via
// symlinkResolvedEqual.
const checkAncestorSymlinks = false

func openWorkspaceRootNoFollow(root string) (*os.File, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" {
		return nil, errors.New("workspace root is empty")
	}

	// Windows has no O_NOFOLLOW. To get the same guard — reject a workspace root
	// reached through a user-placed symlink — resolve the final component with
	// Lstat and reject only genuine symlink reparse points. We deliberately do
	// NOT walk every ancestor: Windows temp/profile paths legitimately contain
	// system junctions and 8.3 short names that are not security-relevant here,
	// and EvalSymlinks-style ancestor checks misflag them.
	if fi, err := os.Lstat(root); err != nil {
		return nil, err
	} else if fi.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("workspace root is a symlink")
	}

	f, err := os.Open(root)
	if err != nil {
		return nil, err
	}

	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.IsDir() {
		f.Close()
		return nil, errors.New("not a directory")
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

	// Construct full path
	targetPath := filepath.Join(rootDir.Name(), name)

	// Check if it's a symlink before creating
	if fi, err := os.Lstat(targetPath); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return errors.New("target is a symlink")
		}
		// File exists - return nil (equivalent to EEXIST behavior on Unix)
		return nil
	}

	// Create file with exclusive access
	f, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	_, err = f.WriteString(content)
	return err
}
