//go:build !darwin

package gateway

import (
	"time"
)

// memoryFileBirthTime returns a file's creation time where the platform
// records one. Most Linux filesystems expose no birth time through stat, so
// the listing's created_at falls back to updated_at there — the sort still
// works, it just degrades to the modification order.
func memoryFileBirthTime(info osFileInfo) (time.Time, bool) {
	return time.Time{}, false
}
