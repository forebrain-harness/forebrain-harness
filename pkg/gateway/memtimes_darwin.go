//go:build darwin

package gateway

import (
	"syscall"
	"time"
)

// memoryFileBirthTime returns a file's creation time where the platform
// records one. darwin's stat carries the birth timestamp.
func memoryFileBirthTime(info osFileInfo) (time.Time, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	sec, nsec := st.Birthtimespec.Sec, st.Birthtimespec.Nsec
	if sec == 0 && nsec == 0 {
		return time.Time{}, false
	}
	return time.Unix(sec, nsec), true
}
