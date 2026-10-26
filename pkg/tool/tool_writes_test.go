package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

func TestToolWritesLedgerPrunesOldEntries(t *testing.T) {
	dir := t.TempDir()
	expired := resolveForLedger(filepath.Join(dir, "expired.txt"))
	fresh := resolveForLedger(filepath.Join(dir, "fresh.txt"))
	now := time.Now()

	toolWrites.mu.Lock()
	toolWrites.byPath[expired] = now.Add(-toolWriteRetention - time.Minute)
	toolWrites.byPath[fresh] = now
	toolWrites.mu.Unlock()
	t.Cleanup(func() {
		toolWrites.mu.Lock()
		delete(toolWrites.byPath, expired)
		delete(toolWrites.byPath, fresh)
		toolWrites.mu.Unlock()
	})

	noteToolFileWrite(filepath.Join(dir, "noted.txt"), now) // runs pruneLocked

	got := toolWritesBetween(now.Add(-2*toolWriteRetention), now)
	if _, ok := got[expired]; ok {
		t.Fatal("expired entry survived the prune")
	}
	if _, ok := got[fresh]; !ok {
		t.Fatal("fresh entry was pruned")
	}
}

func TestToolWritesLedgerDropsOldestOverTheCap(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	oldest := resolveForLedger(filepath.Join(dir, "cap-oldest.txt"))
	toolWrites.mu.Lock()
	toolWrites.byPath[oldest] = now.Add(-20 * time.Minute)
	for i := 0; i < toolWriteMaxEntries-1; i++ {
		toolWrites.byPath[fmt.Sprintf("%s/cap-%d.txt", dir, i)] = now.Add(-20*time.Minute + time.Duration(i+1)*time.Millisecond)
	}
	toolWrites.mu.Unlock()
	t.Cleanup(func() {
		toolWrites.mu.Lock()
		for i := 0; i < toolWriteMaxEntries-1; i++ {
			delete(toolWrites.byPath, fmt.Sprintf("%s/cap-%d.txt", dir, i))
		}
		toolWrites.mu.Unlock()
	})

	noted := filepath.Join(dir, "noted.txt")
	noteToolFileWrite(noted, now) // one over the cap: the oldest entry goes

	toolWrites.mu.Lock()
	_, oldestAlive := toolWrites.byPath[oldest]
	_, notedAlive := toolWrites.byPath[resolveForLedger(noted)]
	size := len(toolWrites.byPath)
	toolWrites.mu.Unlock()
	if oldestAlive {
		t.Fatal("the oldest entry survived a cap-exceeding note")
	}
	if !notedAlive {
		t.Fatal("the new entry was dropped instead of the oldest")
	}
	if size != toolWriteMaxEntries {
		t.Fatalf("ledger size = %d, want the cap %d", size, toolWriteMaxEntries)
	}
}
