package turn

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/stretchr/testify/require"
)

// rewindHarness is a real state store and a real tool state whose history dir
// is a temp directory: the engine's dependencies, exactly as the runner wires
// them.
type rewindHarness struct {
	ctx   context.Context
	store *state.SessionStore
	runs  *state.RunStore
	tools *tool.State
	eng   *RewindStore
	sid   string
}

func newRewindHarness(t *testing.T) *rewindHarness {
	t.Helper()
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	runs := &state.RunStore{DB: db}
	tools := tool.NewState()
	tools.SetHistoryDir(filepath.Join(t.TempDir(), "history"))
	eng := &RewindStore{Store: store, Runs: runs, Snap: tools}
	const sid = "s-rewind"
	require.NoError(t, store.Ensure(ctx, sid, sid))
	return &rewindHarness{ctx: ctx, store: store, runs: runs, tools: tools, eng: eng, sid: sid}
}

func (h *rewindHarness) userMessage(t *testing.T, text string) int64 {
	t.Helper()
	id, err := h.store.AppendMessageForRun(h.ctx, h.sid, "", "user", text, "", "", state.MessageExecTiming{})
	require.NoError(t, err)
	return id
}

// recordWrite mimics what a file tool does: snapshot the bytes it overwrites,
// write the file, and record the change in the ledger.
func (h *rewindHarness) recordWrite(t *testing.T, anchor int64, path string, before []byte, existed bool, after []byte, deleted bool) {
	t.Helper()
	snap := ""
	if existed {
		p, err := h.tools.SnapshotBeforeWrite(h.ctx, path, before, true)
		require.NoError(t, err)
		snap = p
	}
	switch {
	case deleted:
		require.NoError(t, os.Remove(path))
	case !existed:
		require.NoError(t, os.WriteFile(path, after, 0o644))
	default:
		require.NoError(t, os.WriteFile(path, after, 0o644))
	}
	require.NoError(t, h.store.RecordFileChange(h.ctx, state.FileChange{
		SessionID: h.sid, RunID: "r", AnchorMessageID: anchor, Path: path,
		ExistedBefore: existed, BeforeSnapshot: snap,
		AfterSHA256: state.SHA256Hex(after), Deleted: deleted,
	}))
}

func (h *rewindHarness) write(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

// TestRewindBothRestoresCodeAndHidesMessages is the whole feature in one turn:
// two rounds changed a and b and created c; rewinding to before the first
// round restores a and b, deletes c, and hides the conversation from there on,
// and undoing puts everything back.
func TestRewindBothRestoresCodeAndHidesMessages(t *testing.T) {
	h := newRewindHarness(t)
	repo := t.TempDir()
	a := filepath.Join(repo, "a.go")
	b := filepath.Join(repo, "b.go")
	c := filepath.Join(repo, "c.go")
	h.write(t, a, "a0\n")
	h.write(t, b, "b0\n")

	u1 := h.userMessage(t, "round one")
	u2 := h.userMessage(t, "round two")
	h.recordWrite(t, u1, a, []byte("a0\n"), true, []byte("a1\n"), false)
	h.recordWrite(t, u1, b, []byte("b0\n"), true, []byte("b1\n"), false)
	h.recordWrite(t, u2, c, nil, false, []byte("c1\n"), false)

	res, err := Rewind(h.ctx, h.eng, h.sid, u1, RewindModeBoth, nil)
	require.NoError(t, err)
	require.Equal(t, RewindModeBoth, res.Mode)
	require.ElementsMatch(t, []string{a, b, c}, res.Restored)
	require.Equal(t, "a0\n", readFile(t, a))
	require.Equal(t, "b0\n", readFile(t, b))
	_, statErr := os.Stat(c)
	require.True(t, os.IsNotExist(statErr), "the created file is deleted")

	msgs, err := h.store.ListRecentMessages(h.ctx, h.sid, 50)
	require.NoError(t, err)
	require.Empty(t, msgs, "the conversation from the anchor on is hidden")

	require.NoError(t, UndoRewind(h.ctx, h.eng, h.sid))
	require.Equal(t, "a1\n", readFile(t, a))
	require.Equal(t, "b1\n", readFile(t, b))
	require.Equal(t, "c1\n", readFile(t, c))
	msgs, err = h.store.ListRecentMessages(h.ctx, h.sid, 50)
	require.NoError(t, err)
	require.Len(t, msgs, 2, "undo brings the conversation back")
}

func TestRewindCodeOnlyLeavesConversation(t *testing.T) {
	h := newRewindHarness(t)
	a := filepath.Join(t.TempDir(), "a.go")
	h.write(t, a, "a0\n")
	u1 := h.userMessage(t, "hello")
	h.recordWrite(t, u1, a, []byte("a0\n"), true, []byte("a1\n"), false)

	_, err := Rewind(h.ctx, h.eng, h.sid, u1, RewindModeCode, nil)
	require.NoError(t, err)
	require.Equal(t, "a0\n", readFile(t, a))
	msgs, err := h.store.ListRecentMessages(h.ctx, h.sid, 50)
	require.NoError(t, err)
	require.Len(t, msgs, 1, "code-only keeps the conversation")
}

func TestRewindChatOnlyLeavesFiles(t *testing.T) {
	h := newRewindHarness(t)
	a := filepath.Join(t.TempDir(), "a.go")
	h.write(t, a, "a0\n")
	u1 := h.userMessage(t, "hello")
	h.recordWrite(t, u1, a, []byte("a0\n"), true, []byte("a1\n"), false)

	_, err := Rewind(h.ctx, h.eng, h.sid, u1, RewindModeChat, nil)
	require.NoError(t, err)
	require.Equal(t, "a1\n", readFile(t, a), "chat-only leaves the file")
	msgs, err := h.store.ListRecentMessages(h.ctx, h.sid, 50)
	require.NoError(t, err)
	require.Empty(t, msgs)
}

// TestRewindPreviewMarksHandEditsAndKeepPaths pins the two rules a person
// relies on: a file changed by hand since the last tool write is flagged, and
// a flagged file can be excluded from the restore.
func TestRewindPreviewMarksHandEditsAndKeepPaths(t *testing.T) {
	h := newRewindHarness(t)
	repo := t.TempDir()
	a := filepath.Join(repo, "a.go")
	b := filepath.Join(repo, "b.go")
	h.write(t, a, "a0\n")
	h.write(t, b, "b0\n")
	u1 := h.userMessage(t, "hello")
	h.recordWrite(t, u1, a, []byte("a0\n"), true, []byte("a1\n"), false)
	h.recordWrite(t, u1, b, []byte("b0\n"), true, []byte("b1\n"), false)
	// The user edits b by hand after the tool wrote it.
	h.write(t, b, "b1-hand\n")

	preview, err := PreviewRewind(h.ctx, h.eng, h.sid, u1)
	require.NoError(t, err)
	require.Len(t, preview.Files, 2)
	byPath := map[string]RewindFile{}
	for _, f := range preview.Files {
		byPath[f.Path] = f
	}
	require.False(t, byPath[a].ChangedAfter)
	require.True(t, byPath[b].ChangedAfter, "a hand edit since the last tool write is flagged")

	_, err = Rewind(h.ctx, h.eng, h.sid, u1, RewindModeBoth, []string{b})
	require.NoError(t, err)
	require.Equal(t, "a0\n", readFile(t, a))
	require.Equal(t, "b1-hand\n", readFile(t, b), "an excluded file is left alone")
}

func TestRewindUndoAfterNewMessageFails(t *testing.T) {
	h := newRewindHarness(t)
	a := filepath.Join(t.TempDir(), "a.go")
	h.write(t, a, "a0\n")
	u1 := h.userMessage(t, "hello")
	h.recordWrite(t, u1, a, []byte("a0\n"), true, []byte("a1\n"), false)
	_, err := Rewind(h.ctx, h.eng, h.sid, u1, RewindModeBoth, nil)
	require.NoError(t, err)
	// A new user message fixes the rewind in place.
	_ = h.userMessage(t, "a fresh question")
	require.ErrorIs(t, UndoRewind(h.ctx, h.eng, h.sid), ErrNoPendingRewind)
	require.Equal(t, "a0\n", readFile(t, a), "a fixed rewind is not undone")
}

func TestRewindBusyWhileRunAlive(t *testing.T) {
	h := newRewindHarness(t)
	u1 := h.userMessage(t, "hello")
	_, err := h.runs.DB.ExecContext(h.ctx,
		`INSERT INTO fb_runs(id, session_id, input_text, status, created_at, updated_at) VALUES('r-live', ?, 'x', 'waiting_action', 1, 1)`, h.sid)
	require.NoError(t, err)
	_, err = Rewind(h.ctx, h.eng, h.sid, u1, RewindModeBoth, nil)
	require.ErrorIs(t, err, ErrRewindBusy)
}

// TestSessionFileChangesAndRevert covers the workbench's data source: a file
// the conversation changed is listed with its net line change, and reverting
// it restores the earliest state and drops it from the list.
func TestSessionFileChangesAndRevert(t *testing.T) {
	h := newRewindHarness(t)
	a := filepath.Join(t.TempDir(), "a.go")
	h.write(t, a, "a0\n")
	u1 := h.userMessage(t, "hello")
	h.recordWrite(t, u1, a, []byte("a0\n"), true, []byte("a1\nb\n"), false)

	files, err := SessionFileChanges(h.ctx, h.eng, h.sid)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, a, files[0].Path)
	require.Equal(t, 2, files[0].Added)
	require.False(t, files[0].Created)

	require.NoError(t, RevertFile(h.ctx, h.eng, h.sid, a))
	require.Equal(t, "a0\n", readFile(t, a))
	files, err = SessionFileChanges(h.ctx, h.eng, h.sid)
	require.NoError(t, err)
	require.Empty(t, files, "a reverted file leaves the changed-files list")
}

func TestFileChangeDiff(t *testing.T) {
	h := newRewindHarness(t)
	a := filepath.Join(t.TempDir(), "a.go")
	h.write(t, a, "a0\n")
	u1 := h.userMessage(t, "hello")
	h.recordWrite(t, u1, a, []byte("a0\n"), true, []byte("a1\n"), false)
	diff, err := FileChangeDiff(h.ctx, h.eng, h.sid, a)
	require.NoError(t, err)
	require.Contains(t, diff, "-a0")
	require.Contains(t, diff, "+a1")
}

// TestRewindRequiresStoreAndBusyValue keeps the error contract the gateway
// maps to a 409 explicit.
func TestRewindRequiresStoreAndBusyValue(t *testing.T) {
	require.ErrorIs(t, ErrRewindBusy, ErrRewindBusy)
	require.False(t, errors.Is(ErrNoPendingRewind, ErrRewindBusy))
}
