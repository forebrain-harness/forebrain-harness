//go:build !windows

package state

import "golang.org/x/sys/unix"

// diskFreeSpace reports the bytes available to an unprivileged write in the
// filesystem that holds path. It backs the pre-migration backup's headroom
// check: VACUUM INTO writes a second copy of the database beside the first,
// so the migration refuses to start when the copy would not fit.
func diskFreeSpace(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
