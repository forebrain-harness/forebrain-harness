package main

import (
	"os"
	"testing"
)

// TestMain pins this package's tests to a throwaway home — unconditionally, and
// deliberately ignoring whatever FOREBRAIN_HOME the surrounding shell exports.
//
// These tests drive the real terminal entrypoint, and that entrypoint opens the
// state database. When it opens the user's own ~/.forebrain, two things happen
// that have both already happened twice on this machine: the database is
// migrated as a side effect of running the suite (leaving a multi-gigabyte
// pre-migration backup behind), and the run then blocks on the write lock of
// whatever forebrain the user has open, until the ten-minute test timeout.
//
// Trusting an inherited value is what made that possible: an agent session here
// exports FOREBRAIN_HOME pointing at the real install, so "pin it only if it is
// unset" pinned nothing. A test that wants a specific home still sets one —
// t.Setenv overrides this and restores it afterwards — so the only thing this
// default can take away is the ability to reach the user's own install by
// omission.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "forebrain-cmd-tests-home-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("FOREBRAIN_HOME", home); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
