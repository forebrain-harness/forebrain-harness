package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// Every path a file tool changes must land in the tool-write ledger, or a
// shell command running in parallel claims the change as its own (plan 180).
// The cases are the write paths of the three file tools: write_file
// create/modify, edit_file modify, and apply_patch create/modify/delete/move
// (a move announces both the deleted source and the destination).
func TestToolWritesLedgerCoversEveryFileToolPath(t *testing.T) {
	root := t.TempDir()
	st := NewState(root)
	t.Chdir(root)
	ctx := context.Background()

	writeTool, err := NewFileWriteTool(st, nil)
	if err != nil {
		t.Fatalf("NewFileWriteTool: %v", err)
	}
	editTool, err := NewFileEditTool(st)
	if err != nil {
		t.Fatalf("NewFileEditTool: %v", err)
	}
	shellTool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}

	handle := func(tool *llm.Tool, args map[string]string) {
		t.Helper()
		encoded, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("encode args: %v", err)
		}
		if _, err := tool.Handle(ctx, string(encoded)); err != nil {
			t.Fatalf("%s: %v", args["file_path"], err)
		}
	}
	applyPatch := func(command string) {
		t.Helper()
		encoded, err := json.Marshal(map[string]string{"command": command})
		if err != nil {
			t.Fatalf("encode args: %v", err)
		}
		patchCtx := llm.WithAgentSessionID(WithToolCompletionCapture(ctx), "sess-tool-writes")
		if _, err := shellTool.Handle(patchCtx, string(encoded)); err != nil {
			t.Fatalf("apply_patch: %v", err)
		}
	}
	rememberRead := func(path string) {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		st.RememberRead(path, fi.ModTime(), fi.Size(), raw)
	}
	mustExist := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	started := time.Now()
	ledgertestWriteFileCreate := filepath.Join(root, "wf-new.txt")
	handle(writeTool, map[string]string{"file_path": ledgertestWriteFileCreate, "content": "a\n"})

	ledgertestWriteFileModify := filepath.Join(root, "wf-mod.txt")
	mustExist(ledgertestWriteFileModify, "old\n")
	rememberRead(ledgertestWriteFileModify)
	handle(writeTool, map[string]string{"file_path": ledgertestWriteFileModify, "content": "new\n"})

	ledgertestEditFileModify := filepath.Join(root, "ef-mod.txt")
	mustExist(ledgertestEditFileModify, "a\n")
	rememberRead(ledgertestEditFileModify)
	handle(editTool, map[string]string{"file_path": ledgertestEditFileModify, "old_string": "a\n", "new_string": "b\n"})

	applyPatchCreate := filepath.Join(root, "ap-new.txt")
	applyPatch(fmt.Sprintf("apply_patch <<'PATCH'\n*** Begin Patch\n*** Add File: %s\n+line\n*** End Patch\nPATCH", applyPatchCreate))

	applyPatchModify := filepath.Join(root, "ap-mod.txt")
	mustExist(applyPatchModify, "old\n")
	applyPatch(fmt.Sprintf("apply_patch <<'PATCH'\n*** Begin Patch\n*** Update File: %s\n@@\n-old\n+new\n*** End Patch\nPATCH", applyPatchModify))

	applyPatchDelete := filepath.Join(root, "ap-del.txt")
	mustExist(applyPatchDelete, "delete me\n")
	applyPatch(fmt.Sprintf("apply_patch <<'PATCH'\n*** Begin Patch\n*** Delete File: %s\n*** End Patch\nPATCH", applyPatchDelete))

	applyPatchMoveSource := filepath.Join(root, "ap-move.txt")
	applyPatchMoveDest := filepath.Join(root, "ap-moved.txt")
	mustExist(applyPatchMoveSource, "old\n")
	applyPatch(fmt.Sprintf("apply_patch <<'PATCH'\n*** Begin Patch\n*** Update File: %s\n*** Move to: %s\n@@\n-old\n+new\n*** End Patch\nPATCH", applyPatchMoveSource, applyPatchMoveDest))

	writes := toolWritesBetween(started, time.Now())
	for _, path := range []string{
		ledgertestWriteFileCreate,
		ledgertestWriteFileModify,
		ledgertestEditFileModify,
		applyPatchCreate,
		applyPatchModify,
		applyPatchDelete,
		applyPatchMoveSource, // deleted by the move, never passed to attachTurnDiff
		applyPatchMoveDest,
	} {
		if _, ok := writes[resolveForLedger(path)]; !ok {
			t.Errorf("ledger miss for %s", path)
		}
	}
}

// A window is the shell card's own: a write before or after it is not this
// command's, so the reader filters by the announced time, not by the shard a
// line happens to sit in.
func TestToolWritesLedgerIgnoresEntriesOutsideTheWindow(t *testing.T) {
	ledger := t.TempDir()
	t.Setenv(toolWriteDirEnv, ledger)
	dir := t.TempDir()
	outside := filepath.Join(dir, "earlier.txt")
	inside := filepath.Join(dir, "later.txt")
	now := time.Now()
	earlier := now.Add(-10 * time.Minute)

	noteToolFileWrite(outside, earlier)
	noteToolFileWrite(inside, now)

	near := toolWritesBetween(now.Add(-time.Minute), now)
	if _, ok := near[resolveForLedger(inside)]; !ok {
		t.Fatal("a write inside the window was not reported")
	}
	if _, ok := near[resolveForLedger(outside)]; ok {
		t.Fatal("a write before the window was reported")
	}
	old := toolWritesBetween(earlier.Add(-time.Minute), earlier.Add(time.Minute))
	if _, ok := old[resolveForLedger(outside)]; !ok {
		t.Fatal("a write inside its own window was not reported")
	}
}

// Shards are kept for the retention window and then deleted by whichever
// process writes next: that is the whole cleanup story, since a shard whose
// minute is over is never appended to again.
func TestToolWritesLedgerPrunesExpiredShards(t *testing.T) {
	ledger := t.TempDir()
	t.Setenv(toolWriteDirEnv, ledger)
	now := time.Now()

	expired := now.Add(-toolWriteRetention - 2*time.Minute)
	old := filepath.Join(t.TempDir(), "expired.txt")
	noteToolFileWrite(old, expired)
	staleShard := filepath.Join(ledger, toolWriteShardName(expired.Unix()/60))
	if _, err := os.Stat(staleShard); err != nil {
		t.Fatalf("expired shard was not written: %v", err)
	}

	resetToolWriteSweep()
	noteToolFileWrite(filepath.Join(t.TempDir(), "fresh.txt"), now)

	if _, err := os.Stat(staleShard); !os.IsNotExist(err) {
		t.Fatalf("expired shard survived the sweep: %v", err)
	}
	if got := toolWritesBetween(expired.Add(-time.Minute), expired.Add(time.Minute)); len(got) != 0 {
		t.Fatalf("expired announcements still read back: %v", got)
	}
	if got := toolWritesBetween(now.Add(-time.Minute), now); len(got) == 0 {
		t.Fatal("the new announcement was not readable")
	}
}

// The ledger's whole point since plan 186: a shell card in one forebrain must
// see a write announced by a different forebrain process. Only a second
// process can prove it, so the test binary re-executes itself.
func TestToolWritesLedgerIsVisiblySharedAcrossProcesses(t *testing.T) {
	t.Setenv(toolWriteDirEnv, t.TempDir())
	path := filepath.Join(t.TempDir(), "written-elsewhere.go")
	at := time.Now()

	runToolWriteHelperProcess(t, path, at)

	got := toolWritesBetween(at.Add(-time.Minute), at.Add(time.Minute))
	if _, ok := got[resolveForLedger(path)]; !ok {
		t.Fatalf("another process's write is invisible to this one: %v", got)
	}
}

// runToolWriteHelperProcess makes a second forebrain process announce a write.
// The child runs the same noteToolFileWrite a file tool runs, in its own
// address space, so only a ledger outside the process can see it.
func runToolWriteHelperProcess(t *testing.T, path string, at time.Time) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestToolWritesLedgerHelperProcess$")
	cmd.Env = append(os.Environ(),
		toolWriteHelperEnv+"=1",
		"FB_TOOLWRITES_PATH="+path,
		"FB_TOOLWRITES_AT="+strconv.FormatInt(at.UnixNano(), 10),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper process: %v\n%s", err, out)
	}
}

// TestToolWritesLedgerHelperProcess is the child side of
// runToolWriteHelperProcess, not a test of its own: without the marker in its
// environment it does nothing.
func TestToolWritesLedgerHelperProcess(t *testing.T) {
	if os.Getenv(toolWriteHelperEnv) == "" {
		t.Skip("helper process only")
	}
	at, err := strconv.ParseInt(os.Getenv("FB_TOOLWRITES_AT"), 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FB_TOOLWRITES_AT: %v\n", err)
		os.Exit(2)
	}
	noteToolFileWrite(os.Getenv("FB_TOOLWRITES_PATH"), time.Unix(0, at))
	os.Exit(0)
}

// resetToolWriteSweep clears the per-process rate limit so a test's own write
// performs the sweep it is about to assert on.
func resetToolWriteSweep() {
	toolWriteSweepMu.Lock()
	toolWriteSweptAt = time.Time{}
	toolWriteSweepMu.Unlock()
}
