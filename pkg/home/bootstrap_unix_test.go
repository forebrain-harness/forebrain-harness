//go:build !windows

package home

import (
	"syscall"
	"testing"
)

func TestHasMultipleHardLinksWithNonStatSys(t *testing.T) {
	if hasMultipleHardLinks("", fakeFileInfo{}) {
		t.Fatal("fake file info should not report multiple hardlinks")
	}
	if !hasMultipleHardLinks("", fakeFileInfo{sys: &syscall.Stat_t{Nlink: 2}}) {
		t.Fatal("stat with Nlink=2 should report multiple hardlinks")
	}
}
