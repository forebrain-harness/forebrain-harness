//go:build !windows

package home

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func OpenNoFollowForWrite(target string, mode os.FileMode) (*os.File, error) {
	target = filepath.Clean(strings.TrimSpace(target))
	if target == "" {
		return nil, errors.New("empty path")
	}
	fd, err := unix.Open(target, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), target)
	if f == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open file")
	}
	return f, nil
}
