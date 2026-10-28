package skill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeGit installs a shell script named git at the front of PATH and returns
// the directory holding it (a test may rewrite the script's body between
// calls by writing it again).
func fakeGit(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "git")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// TestGitOutDeadlineHoldsWhenAGrandchildKeepsThePipe pins the root cause of the
// /skills freeze: git spawns helpers that inherit stdout/stderr, so killing git
// at the deadline left cmd.Wait blocked on the pipes until the helper exited —
// a 2 s deadline measured 1m15s. WaitDelay is what bounds it.
func TestGitOutDeadlineHoldsWhenAGrandchildKeepsThePipe(t *testing.T) {
	fakeGit(t, "sleep 5 &\nwait")
	previous := gitKillGrace
	gitKillGrace = 200 * time.Millisecond
	t.Cleanup(func() { gitKillGrace = previous })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _ = gitOut(ctx, "", "ls-remote", "https://example.invalid/x.git", "HEAD")
	elapsed := time.Since(start)
	// Unfixed this blocks for the script's full 5 s; the deadline plus the
	// grace is the bound the callers depend on.
	if elapsed >= 2*time.Second {
		t.Fatalf("gitOut returned after %v: a deadline does not bound the call while a grandchild holds the pipes", elapsed)
	}
}

// TestCachedRemoteHeadNeverProbes pins the read side: an unanswered repository
// reports cached=false and runs no git at all.
func TestCachedRemoteHeadNeverProbes(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "probed")
	fakeGit(t, fmt.Sprintf("printf ran > %q", marker))
	ForgetRemoteHeadCache()
	t.Cleanup(ForgetRemoteHeadCache)

	rev, ok, cached := CachedRemoteHead("https://example.invalid/x.git")
	if rev != "" || ok || cached {
		t.Fatalf("CachedRemoteHead = (%q, %v, %v), want an empty unanswered report", rev, ok, cached)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("CachedRemoteHead ran git; the panel path must only ever read the cache")
	}
}

// TestPrimeRemoteHeadFillsTheCache pins the write side: asking for a probe
// returns at once, and the answer the probe fetched is what the next read
// reports.
func TestPrimeRemoteHeadFillsTheCache(t *testing.T) {
	const want = "0123456789abcdef0123456789abcdef01234567"
	fakeGit(t, "echo "+want)
	ForgetRemoteHeadCache()
	t.Cleanup(ForgetRemoteHeadCache)

	start := time.Now()
	PrimeRemoteHead("https://example.invalid/x.git")
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("PrimeRemoteHead blocked %v; it must return without waiting for the probe", elapsed)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rev, ok, cached := CachedRemoteHead("https://example.invalid/x.git")
		if cached {
			if !ok || rev != want {
				t.Fatalf("cached probe = (%q, %v), want (%q, true)", rev, ok, want)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the background probe never answered")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestPrimeRemoteHeadProbesOncePerRepository pins the in-flight de-duplication:
// a listing recomputes every row, so the same repository asked repeatedly must
// start one probe, not one per row.
func TestPrimeRemoteHeadProbesOncePerRepository(t *testing.T) {
	countFile := filepath.Join(t.TempDir(), "count")
	fakeGit(t, fmt.Sprintf(
		"echo x >> %q\necho %s", countFile, "0123456789abcdef0123456789abcdef01234567"))
	ForgetRemoteHeadCache()
	t.Cleanup(ForgetRemoteHeadCache)

	repo := "https://example.invalid/x.git"
	for i := 0; i < 20; i++ {
		PrimeRemoteHead(repo)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, cached := CachedRemoteHead(repo); cached {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the background probe never answered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	raw, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatalf("read probe count: %v", err)
	}
	if got := strings.Count(string(raw), "\n"); got != 1 {
		t.Fatalf("20 requests started %d probes, want 1", got)
	}
}
