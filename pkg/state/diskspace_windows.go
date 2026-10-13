//go:build windows

package state

import "golang.org/x/sys/windows"

// diskFreeSpace reports the bytes available to the caller in the filesystem
// that holds path. It backs the pre-migration backup's headroom check: VACUUM
// INTO writes a second copy of the database beside the first, so the
// migration refuses to start when the copy would not fit.
func diskFreeSpace(path string) (int64, error) {
	dir, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(dir, &free, nil, nil); err != nil {
		return 0, err
	}
	return int64(free), nil
}
