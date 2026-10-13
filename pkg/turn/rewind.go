// Rewind to before a message: the engine both surfaces share.
package turn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// Rewind modes.
const (
	RewindModeBoth = "both"
	RewindModeChat = "chat"
	RewindModeCode = "code"
)

// ErrRewindBusy reports that a rewind was asked for while the conversation has
// a run in flight; a running turn's file writes and messages are still moving,
// so there is no stable point to rewind to.
var ErrRewindBusy = errors.New("session_busy")

// ErrNoPendingRewind reports an undo with nothing to undo.
var ErrNoPendingRewind = errors.New("no rewind to undo")

// SnapshotWriter saves the bytes of a file before it is rewritten and returns
// the path they were saved to. *tool.State implements it, so a rewind's undo
// snapshots live beside the file tools' own.
type SnapshotWriter interface {
	SnapshotBeforeWrite(ctx context.Context, absPath string, content []byte, existed bool) (string, error)
}

// RewindStore is the state the rewind engine reads and writes: the session and
// run stores, and the snapshot writer for the files it rewrites.
type RewindStore struct {
	Store *state.SessionStore
	Runs  *state.RunStore
	Snap  SnapshotWriter
}

// RewindFile is one file a rewind would restore, as the confirm dialog lists it.
type RewindFile struct {
	Path string `json:"path"`
	// Added/Removed are the line counts between the file as it stands and the
	// content a restore would put back.
	Added        int  `json:"added"`
	Removed      int  `json:"removed"`
	ChangedAfter bool `json:"changed_after"`
	Created      bool `json:"created"`
}

// RewindPreview is what a surface shows before a rewind runs.
type RewindPreview struct {
	AnchorMessageID int64        `json:"anchor_message_id"`
	AnchorText      string       `json:"anchor_text"`
	Files           []RewindFile `json:"files"`
}

// RewindResult is what a completed rewind reports.
type RewindResult struct {
	AnchorText string   `json:"anchor_text"`
	Mode       string   `json:"mode"`
	Restored   []string `json:"restored"`
}

// ChangedFile is one file the conversation changed with a file tool.
type ChangedFile struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Created bool   `json:"created"`
}

// rewindFileUndo is one file the undo of a rewind must put back: the snapshot
// of its bytes as they were just before the rewind, and whether it existed
// then (an empty snapshot with existed false means the rewind created it).
type rewindFileUndo struct {
	Path         string `json:"path"`
	UndoSnapshot string `json:"undo_snapshot,omitempty"`
	Existed      bool   `json:"existed"`
}

// fileGroup is every change a conversation made to one path, first and last.
type fileGroup struct {
	path  string
	first state.FileChangeRow
	last  state.FileChangeRow
}

// PreviewRewind reports the files a rewind to before anchorMessageID would
// restore, whether each has changed since the last tool write, and the line
// counts of the restore.
func PreviewRewind(ctx context.Context, store *RewindStore, sessionID string, anchorMessageID int64) (RewindPreview, error) {
	if err := requireStore(store); err != nil {
		return RewindPreview{}, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || anchorMessageID <= 0 {
		return RewindPreview{}, fmt.Errorf("rewind preview: invalid session or message")
	}
	text, _, err := store.Store.UserMessageContent(ctx, sessionID, anchorMessageID)
	if err != nil {
		return RewindPreview{}, err
	}
	rows, err := store.Store.FileChangesSince(ctx, sessionID, anchorMessageID)
	if err != nil {
		return RewindPreview{}, err
	}
	out := RewindPreview{AnchorMessageID: anchorMessageID, AnchorText: text, Files: []RewindFile{}}
	for _, g := range groupFileChanges(rows) {
		out.Files = append(out.Files, previewFile(g))
	}
	return out, nil
}

// Rewind restores the conversation and the files to the state before
// anchorMessageID. mode is "both", "chat" or "code"; keepPaths names files the
// code half must leave alone. The anchor's text is returned so a surface can
// put it back in the composer.
func Rewind(ctx context.Context, store *RewindStore, sessionID string, anchorMessageID int64, mode string, keepPaths []string) (RewindResult, error) {
	if err := requireStore(store); err != nil {
		return RewindResult{}, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || anchorMessageID <= 0 {
		return RewindResult{}, fmt.Errorf("rewind: invalid session or message")
	}
	mode = normalizeRewindMode(mode)
	busy, err := store.Runs.SessionHasLiveRun(ctx, sessionID)
	if err != nil {
		return RewindResult{}, err
	}
	if busy {
		return RewindResult{}, ErrRewindBusy
	}
	text, found, err := store.Store.UserMessageContent(ctx, sessionID, anchorMessageID)
	if err != nil {
		return RewindResult{}, err
	}
	if !found {
		return RewindResult{}, fmt.Errorf("rewind: anchor message not found")
	}
	// One rewind is undoable at a time: a second rewind fixes the first in
	// place before it runs.
	if err := store.Store.FixPendingRewinds(ctx, sessionID); err != nil {
		return RewindResult{}, err
	}

	var undos []rewindFileUndo
	var restored []string
	if mode == RewindModeBoth || mode == RewindModeCode {
		rows, err := store.Store.FileChangesSince(ctx, sessionID, anchorMessageID)
		if err != nil {
			return RewindResult{}, err
		}
		skip := map[string]bool{}
		for _, p := range keepPaths {
			skip[strings.TrimSpace(p)] = true
		}
		for _, g := range groupFileChanges(rows) {
			if skip[g.path] {
				continue
			}
			undo, err := restoreFile(ctx, store, g)
			if err != nil {
				return RewindResult{}, err
			}
			undos = append(undos, undo)
			restored = append(restored, g.path)
		}
	}

	hiddenFrom := int64(0)
	if mode == RewindModeBoth || mode == RewindModeChat {
		if hiddenFrom, err = store.Store.RewindEventStart(ctx, sessionID, anchorMessageID); err != nil {
			return RewindResult{}, err
		}
		if _, err := store.Store.RewindMessagesFrom(ctx, sessionID, anchorMessageID); err != nil {
			return RewindResult{}, err
		}
	}

	filesJSON, err := json.Marshal(undos)
	if err != nil {
		return RewindResult{}, err
	}
	rec := state.RewindRecord{
		ID:                 state.NewID("rw"),
		SessionID:          sessionID,
		AnchorMessageID:    anchorMessageID,
		Mode:               mode,
		HiddenEventSeqFrom: hiddenFrom,
		FilesJSON:          string(filesJSON),
		State:              "pending",
		CreatedAtMs:        time.Now().UnixMilli(),
	}
	if err := store.Store.InsertRewind(ctx, rec); err != nil {
		return RewindResult{}, err
	}
	if restored == nil {
		restored = []string{}
	}
	return RewindResult{AnchorText: text, Mode: mode, Restored: restored}, nil
}

// UndoRewind puts back the files and rows the conversation's pending rewind
// moved aside, and removes the rewind record.
func UndoRewind(ctx context.Context, store *RewindStore, sessionID string) error {
	if err := requireStore(store); err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("undo rewind: empty session")
	}
	rec, ok, err := store.Store.PendingRewind(ctx, sessionID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNoPendingRewind
	}
	var undos []rewindFileUndo
	if raw := strings.TrimSpace(rec.FilesJSON); raw != "" {
		if err := json.Unmarshal([]byte(raw), &undos); err != nil {
			return fmt.Errorf("undo rewind: parse files: %w", err)
		}
	}
	for _, u := range undos {
		if err := undoFile(u); err != nil {
			return err
		}
	}
	if rec.Mode == RewindModeBoth || rec.Mode == RewindModeChat {
		if _, err := store.Store.RestoreRewoundMessages(ctx, sessionID, rec.AnchorMessageID); err != nil {
			return err
		}
	}
	return store.Store.DeleteRewind(ctx, rec.ID)
}

// SessionFileChanges summarises every file the conversation changed with a
// file tool: the change from the file's earliest recorded state to its
// current one. It is the data behind the workbench's changed-files list.
func SessionFileChanges(ctx context.Context, store *RewindStore, sessionID string) ([]ChangedFile, error) {
	if err := requireStore(store); err != nil {
		return nil, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("file changes: empty session")
	}
	rows, err := store.Store.AllFileChanges(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := []ChangedFile{}
	for _, g := range groupFileChanges(rows) {
		before := restoredBytes(g.first)
		current, _ := readFileBytes(g.path)
		summary, _ := event.Build(g.path, before, current)
		out = append(out, ChangedFile{
			Path:    g.path,
			Added:   summary.Added,
			Removed: summary.Deleted,
			Created: !g.first.ExistedBefore,
		})
	}
	return out, nil
}

// RevertFile restores one path to the state before the conversation's first
// tool write to it and drops its ledger rows, so the changed-files list no
// longer shows it.
func RevertFile(ctx context.Context, store *RewindStore, sessionID, path string) error {
	if err := requireStore(store); err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	path = strings.TrimSpace(path)
	if sessionID == "" || path == "" {
		return fmt.Errorf("revert file: invalid session or path")
	}
	rows, err := store.Store.AllFileChanges(ctx, sessionID)
	if err != nil {
		return err
	}
	var target *fileGroup
	for _, g := range groupFileChanges(rows) {
		if g.path == path {
			gg := g
			target = &gg
			break
		}
	}
	if target == nil {
		return fmt.Errorf("revert file: no recorded change for %s", path)
	}
	if _, err := restoreFile(ctx, store, *target); err != nil {
		return err
	}
	return store.Store.DeleteFileChangePath(ctx, sessionID, path)
}

// FileChangeDiff returns the unified diff between a path's earliest recorded
// state and its current one, for the workbench's "compare" view.
func FileChangeDiff(ctx context.Context, store *RewindStore, sessionID, path string) (string, error) {
	if err := requireStore(store); err != nil {
		return "", err
	}
	sessionID = strings.TrimSpace(sessionID)
	path = strings.TrimSpace(path)
	if sessionID == "" || path == "" {
		return "", fmt.Errorf("file diff: invalid session or path")
	}
	rows, err := store.Store.AllFileChanges(ctx, sessionID)
	if err != nil {
		return "", err
	}
	for _, g := range groupFileChanges(rows) {
		if g.path != path {
			continue
		}
		before := restoredBytes(g.first)
		current, _ := readFileBytes(g.path)
		summary, _ := event.Build(g.path, before, current)
		return summary.UnifiedDiff, nil
	}
	return "", nil
}

func groupFileChanges(rows []state.FileChangeRow) []fileGroup {
	var groups []fileGroup
	idx := map[string]int{}
	for _, r := range rows {
		if i, ok := idx[r.Path]; ok {
			groups[i].last = r
			continue
		}
		idx[r.Path] = len(groups)
		groups = append(groups, fileGroup{path: r.Path, first: r, last: r})
	}
	return groups
}

func previewFile(g fileGroup) RewindFile {
	before := restoredBytes(g.first)
	current, _ := readFileBytes(g.path)
	summary, _ := event.Build(g.path, before, current)
	return RewindFile{
		Path:         g.path,
		Added:        summary.Added,
		Removed:      summary.Deleted,
		ChangedAfter: changedSinceLastWrite(g, current),
		Created:      !g.first.ExistedBefore,
	}
}

// changedSinceLastWrite reports whether the file no longer matches what the
// last tool write left. A last write that deleted the file matches when the
// file is still gone; a write is stale once someone edits the file by hand.
func changedSinceLastWrite(g fileGroup, current []byte) bool {
	if g.last.Deleted {
		_, exists := readFileBytes(g.path)
		return exists
	}
	if strings.TrimSpace(g.last.AfterSHA256) == "" {
		return false
	}
	return state.SHA256Hex(current) != g.last.AfterSHA256
}

// restoreFile puts a path back to the state before the group's first change,
// saving the bytes it replaces so the rewind is undoable.
func restoreFile(ctx context.Context, store *RewindStore, g fileGroup) (rewindFileUndo, error) {
	current, exists := readFileBytes(g.path)
	undo := rewindFileUndo{Path: g.path, Existed: exists}
	if exists {
		if store.Snap == nil {
			return undo, fmt.Errorf("rewind: no snapshot writer for %s", g.path)
		}
		path, err := store.Snap.SnapshotBeforeWrite(ctx, g.path, current, true)
		if err != nil {
			return undo, err
		}
		undo.UndoSnapshot = path
	}
	if g.first.ExistedBefore {
		if err := writeFileBytes(g.path, restoredBytes(g.first)); err != nil {
			return undo, err
		}
	} else if err := os.Remove(g.path); err != nil && !os.IsNotExist(err) {
		return undo, err
	}
	return undo, nil
}

func undoFile(u rewindFileUndo) error {
	if u.Existed {
		content, ok := readFileBytes(u.UndoSnapshot)
		if !ok {
			return fmt.Errorf("undo rewind: snapshot missing for %s", u.Path)
		}
		return writeFileBytes(u.Path, content)
	}
	if err := os.Remove(u.Path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// restoredBytes is the content a restore puts back for a group's first change:
// the saved snapshot when the file existed, nothing when it was created.
func restoredBytes(first state.FileChangeRow) []byte {
	if !first.ExistedBefore {
		return nil
	}
	content, _ := readFileBytes(first.BeforeSnapshot)
	return content
}

func readFileBytes(path string) ([]byte, bool) {
	if strings.TrimSpace(path) == "" {
		return nil, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return b, true
}

func writeFileBytes(path string, content []byte) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, content, 0o644)
}

func normalizeRewindMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case RewindModeChat:
		return RewindModeChat
	case RewindModeCode:
		return RewindModeCode
	default:
		return RewindModeBoth
	}
}

func requireStore(store *RewindStore) error {
	if store == nil || store.Store == nil {
		return fmt.Errorf("rewind: session store unavailable")
	}
	return nil
}

var _ SnapshotWriter = (*tool.State)(nil)
