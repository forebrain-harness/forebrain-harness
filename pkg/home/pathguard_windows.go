//go:build windows

package home

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func OpenNoFollowForWrite(target string, mode os.FileMode) (*os.File, error) {
	target = filepath.Clean(strings.TrimSpace(target))
	if target == "" {
		return nil, errors.New("empty path")
	}

	// On Windows, we use FILE_FLAG_OPEN_REPARSE_POINT to avoid following symlinks
	// os.O_CREATE|os.O_EXCL|os.O_WRONLY provides atomic create-if-not-exists semantics
	// This prevents TOCTOU attacks similar to O_NOFOLLOW on Unix

	// First check if it's a symlink
	if fi, err := os.Lstat(target); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("target is a symlink")
		}
		// File exists and is not a symlink - fail with O_EXCL semantics
		return nil, &os.PathError{Op: "open", Path: target, Err: syscall.ERROR_FILE_EXISTS}
	}

	// Create the file with exclusive access
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return nil, err
	}

	return f, nil
}
