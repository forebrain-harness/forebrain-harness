package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTodoStatusConstants(t *testing.T) {
	if StatusPending != "pending" {
		t.Errorf("expected StatusPending=%q, got %q", "pending", StatusPending)
	}
	if StatusInProgress != "in_progress" {
		t.Errorf("expected StatusInProgress=%q, got %q", "in_progress", StatusInProgress)
	}
	if StatusCompleted != "completed" {
		t.Errorf("expected StatusCompleted=%q, got %q", "completed", StatusCompleted)
	}
	if StatusCancelled != "cancelled" {
		t.Errorf("expected StatusCancelled=%q, got %q", "cancelled", StatusCancelled)
	}
}

func TestTodoPathDefaultSession(t *testing.T) {
	tmpDir := t.TempDir()
	want := filepath.Join(tmpDir, "state", "todos", "default.json")
	got := todoPath(tmpDir, "")
	if got != want {
		t.Errorf("todoPath(_, empty) = %s; want %s", got, want)
	}
}

func TestTodoPathTrimmedSession(t *testing.T) {
	tmpDir := t.TempDir()
	want := filepath.Join(tmpDir, "state", "todos", "sess123.json")
	got := todoPath(tmpDir, "  sess123  ")
	if got != want {
		t.Errorf("todoPath(_, trimmed) = %s; want %s", got, want)
	}
}

func TestTodoLoadNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	l, err := Load(tmpDir, "nonexistent")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(l.Items) != 0 {
		t.Errorf("expected empty list, got %d items", len(l.Items))
	}
}

func TestTodoLoadReadError(t *testing.T) {
	tmpDir := t.TempDir()
	p := todoPath(tmpDir, "dir")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir todo path: %v", err)
	}
	if _, err := Load(tmpDir, "dir"); err == nil {
		t.Fatal("expected read error for directory todo path")
	}
}

func TestTodoLoadSaveRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	original := List{
		Items: []Item{
			{ID: "1", Content: "First task", Status: StatusPending, UpdatedAt: 1000},
			{ID: "2", Content: "Second task", Status: StatusCompleted, UpdatedAt: 2000},
		},
	}
	if err := Save(tmpDir, "roundtrip", original); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	loaded, err := Load(tmpDir, "roundtrip")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(loaded.Items) != len(original.Items) {
		t.Fatalf("item count mismatch: got %d, want %d", len(loaded.Items), len(original.Items))
	}
	for i, want := range original.Items {
		got := loaded.Items[i]
		if got.ID != want.ID || got.Content != want.Content || got.Status != want.Status {
			t.Errorf("item[%d]: got %+v, want %+v", i, got, want)
		}
	}
}

func TestTodoReplaceNewItems(t *testing.T) {
	tmpDir := t.TempDir()
	items := []Item{
		{ID: "a", Content: "Task A"},
		{ID: "b", Content: "Task B"},
	}
	result, err := Replace(tmpDir, "replace-new", items)
	if err != nil {
		t.Fatalf("Replace error: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result.Items))
	}
	ids := map[string]bool{result.Items[0].ID: true, result.Items[1].ID: true}
	if !ids["a"] || !ids["b"] {
		t.Errorf("expected items a and b, got IDs %v", ids)
	}
}

// A rewritten plan uses fresh ids. The rows of the previous generation are not
// resent, so they must be gone — a merge kept them on screen forever, pinned at
// whatever status they last had.
func TestTodoReplaceDropsRowsNotResent(t *testing.T) {
	tmpDir := t.TempDir()
	_, err := Replace(tmpDir, "replace-test", []Item{{ID: "x", Content: "Original"}})
	if err != nil {
		t.Fatalf("first Replace error: %v", err)
	}
	result, err := Replace(tmpDir, "replace-test", []Item{{ID: "y", Content: "New"}})
	if err != nil {
		t.Fatalf("second Replace error: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != "y" {
		t.Fatalf("expected only the resent row, got %#v", result.Items)
	}
	loaded, err := Load(tmpDir, "replace-test")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(loaded.Items) != 1 || loaded.Items[0].ID != "y" {
		t.Fatalf("stale row persisted on disk: %#v", loaded.Items)
	}
}

func TestTodoReplaceUpdateExisting(t *testing.T) {
	tmpDir := t.TempDir()
	_, err := Replace(tmpDir, "update-test", []Item{{ID: "z", Content: "Old", Status: StatusPending}})
	if err != nil {
		t.Fatalf("first Replace error: %v", err)
	}
	result, err := Replace(tmpDir, "update-test", []Item{{ID: "z", Content: "Updated", Status: StatusCompleted}})
	if err != nil {
		t.Fatalf("second Replace error: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}
	if result.Items[0].Content != "Updated" {
		t.Errorf("expected Content Updated, got %q", result.Items[0].Content)
	}
	if result.Items[0].Status != StatusCompleted {
		t.Errorf("expected TodoStatus Completed, got %q", result.Items[0].Status)
	}
}

func TestTodoReplaceEmptyIDSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	result, err := Replace(tmpDir, "empty-id", []Item{{ID: "", Content: "skip me"}, {ID: "valid", Content: "keep me"}})
	if err != nil {
		t.Fatalf("Replace error: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item (empty ID skipped), got %d", len(result.Items))
	}
	if result.Items[0].ID != "valid" {
		t.Errorf("expected ID valid, got %q", result.Items[0].ID)
	}
}

func TestTodoReplaceDefaultStatus(t *testing.T) {
	tmpDir := t.TempDir()
	result, err := Replace(tmpDir, "default-status", []Item{{ID: "s1", Content: "no status set"}})
	if err != nil {
		t.Fatalf("Replace error: %v", err)
	}
	if result.Items[0].Status != StatusPending {
		t.Errorf("expected default TodoStatus Pending, got %q", result.Items[0].Status)
	}
}

func TestTodoReplaceReturnsSaveErrorWithRows(t *testing.T) {
	tmpDir := t.TempDir()
	p := todoPath(tmpDir, "dir")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir todo path: %v", err)
	}
	result, err := Replace(tmpDir, "dir", []Item{{ID: "x", Content: "task"}})
	if err == nil {
		t.Fatal("expected save error for directory todo path")
	}
	if len(result.Items) != 1 || result.Items[0].ID != "x" {
		t.Fatalf("expected the normalized rows alongside the save error, got %#v", result.Items)
	}
}

func TestTodoReplaceWhitespaceIDTrimmed(t *testing.T) {
	tmpDir := t.TempDir()
	result, err := Replace(tmpDir, "ws-id", []Item{{ID: "  s1  ", Content: "trimmed"}})
	if err != nil {
		t.Fatalf("Replace error: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}
	if result.Items[0].ID != "s1" {
		t.Errorf("expected trimmed ID s1, got %q", result.Items[0].ID)
	}
}

func TestTodoLoadInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "state", "todos", "bad.json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("{invalid json"), 0o600)
	_, err := Load(tmpDir, "bad")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestTodoSaveCreatesDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	nested := filepath.Join(tmpDir, "deep", "nested", "dir")
	_ = Save(nested, "session", List{Items: []Item{{ID: "1"}}})
	l, err := Load(nested, "session")
	if err != nil {
		t.Fatalf("Load after save in nested dir: %v", err)
	}
	if len(l.Items) != 1 {
		t.Errorf("expected 1 item, got %d", len(l.Items))
	}
}

func TestTodoSaveWriteError(t *testing.T) {
	tmpDir := t.TempDir()
	p := todoPath(tmpDir, "blocked")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir blocking dir: %v", err)
	}
	if err := Save(tmpDir, "blocked", List{Items: []Item{{ID: "1"}}}); err == nil {
		t.Fatal("expected write error")
	}
}

func BenchmarkTodoReplace(b *testing.B) {
	tmpDir := b.TempDir()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Replace(tmpDir, "bench", []Item{{ID: "id", Content: "content"}})
	}
}

func TestTodoPath_DefaultSessionID(t *testing.T) {
	p := todoPath("", "")
	if filepath.Base(p) != "default.json" {
		t.Errorf("expected base 'default.json', got %q", filepath.Base(p))
	}
}

func TestTodoPath_SessionWithSpaces(t *testing.T) {
	p := todoPath("", "  test  ")
	if filepath.Base(p) != "test.json" {
		t.Errorf("expected base 'test.json', got %q", filepath.Base(p))
	}
}

func TestTodoLoad_NonExistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	l, err := Load(tmpDir, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(l.Items) != 0 {
		t.Errorf("expected empty list, got %d items", len(l.Items))
	}
}

func TestTodoSaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	l := List{Items: []Item{
		{ID: "1", Content: "First task", Status: StatusPending},
		{ID: "2", Content: "Second task", Status: StatusCompleted},
	}}
	err := Save(tmpDir, "test", l)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	loaded, err := Load(tmpDir, "test")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(loaded.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(loaded.Items))
	}
	if loaded.Items[0].Content != "First task" {
		t.Errorf("expected 'First task', got %q", loaded.Items[0].Content)
	}
	if loaded.Items[1].Status != StatusCompleted {
		t.Errorf("expected StatusCompleted, got %q", loaded.Items[1].Status)
	}
}

func TestTodoReplace_NewItem(t *testing.T) {
	tmpDir := t.TempDir()
	items := []Item{{ID: "a", Content: "New item"}}
	result, err := Replace(tmpDir, "test", items)
	if err != nil {
		t.Fatalf("Replace failed: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}
	if result.Items[0].Content != "New item" {
		t.Errorf("expected 'New item', got %q", result.Items[0].Content)
	}
}

func TestTodoReplace_UpdateExistingItem(t *testing.T) {
	tmpDir := t.TempDir()
	// First write
	Replace(tmpDir, "test", []Item{{ID: "a", Content: "Original"}})
	// Then update
	result, err := Replace(tmpDir, "test", []Item{{ID: "a", Content: "Updated"}})
	if err != nil {
		t.Fatalf("Replace failed: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}
	if result.Items[0].Content != "Updated" {
		t.Errorf("expected 'Updated', got %q", result.Items[0].Content)
	}
}

// Regression for the two-plans-at-once progress panel: a session that wrote a
// four-row plan and later rewrote it with new ids showed all eight rows and two
// in_progress items, because the store unioned the generations.
func TestTodoReplaceSupersedesPreviousPlanGeneration(t *testing.T) {
	tmpDir := t.TempDir()
	first := []Item{
		{ID: "trace-models", Content: "Trace the model list", Status: StatusInProgress},
		{ID: "trace-oauth", Content: "Trace OAuth", Status: StatusPending},
	}
	if _, err := Replace(tmpDir, "test", first); err != nil {
		t.Fatalf("first Replace failed: %v", err)
	}
	second := []Item{
		{ID: "scan", Content: "Locate the code", Status: StatusCompleted},
		{ID: "integration-design", Content: "Design the integration", Status: StatusInProgress},
		{ID: "plan-review", Content: "Review the plan", Status: StatusPending},
	}
	result, err := Replace(tmpDir, "test", second)
	if err != nil {
		t.Fatalf("second Replace failed: %v", err)
	}
	if len(result.Items) != len(second) {
		t.Fatalf("expected only the current plan, got %#v", result.Items)
	}
	active := 0
	for i, item := range result.Items {
		if item.ID != second[i].ID {
			t.Fatalf("item[%d] = %q, want %q (order must follow the payload)", i, item.ID, second[i].ID)
		}
		if item.Status == StatusInProgress {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("expected a single in_progress row, got %d", active)
	}
}

func TestTodoReplaceLastRowWinsOnDuplicateID(t *testing.T) {
	tmpDir := t.TempDir()
	result, err := Replace(tmpDir, "dup", []Item{
		{ID: "a", Content: "First", Status: StatusPending},
		{ID: "a", Content: "Second", Status: StatusCompleted},
	})
	if err != nil {
		t.Fatalf("Replace failed: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected ids to stay unique, got %#v", result.Items)
	}
	if result.Items[0].Content != "Second" || result.Items[0].Status != StatusCompleted {
		t.Fatalf("expected the last row to win, got %#v", result.Items[0])
	}
}

func TestTodoReplace_EmptyIDSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	items := []Item{{ID: "", Content: "skip me"}, {ID: "x", Content: "keep me"}}
	result, err := Replace(tmpDir, "test", items)
	if err != nil {
		t.Fatalf("Replace failed: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != "x" {
		t.Error("expected only item with non-empty ID")
	}
}

func TestTodoReplace_DefaultStatusPending(t *testing.T) {
	tmpDir := t.TempDir()
	items := []Item{{ID: "a", Content: "no status set"}}
	result, err := Replace(tmpDir, "test", items)
	if err != nil {
		t.Fatalf("Replace failed: %v", err)
	}
	if result.Items[0].Status != StatusPending {
		t.Errorf("expected default StatusPending, got %q", result.Items[0].Status)
	}
}

func TestTodoReplace_TrimsID(t *testing.T) {
	tmpDir := t.TempDir()
	items := []Item{{ID: "  a  ", Content: "trimmed"}}
	result, err := Replace(tmpDir, "test", items)
	if err != nil {
		t.Fatalf("Replace failed: %v", err)
	}
	if result.Items[0].ID != "a" {
		t.Errorf("expected trimmed ID 'a', got %q", result.Items[0].ID)
	}
}

func TestTodoLoad_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "state", "todos", "bad.json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(`not json`), 0o644)
	_, err := Load(tmpDir, "bad")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestTodoSave_InvalidatesCorrectly(t *testing.T) {
	tmpDir := t.TempDir()
	l := List{Items: []Item{{ID: "1", Content: "test", Status: StatusPending}}}
	err := Save(tmpDir, "test", l)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	// Verify file exists and is valid JSON
	data, err := os.ReadFile(filepath.Join(tmpDir, "state", "todos", "test.json"))
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !strings.Contains(string(data), `"items"`) {
		t.Error("expected saved file to contain items key")
	}
}

func TestItemReadsLegacyActiveForm(t *testing.T) {
	var old, cur Item
	if err := json.Unmarshal([]byte(`{"id":"1","content":"c","status":"in_progress","active_form":"Writing"}`), &old); err != nil || old.Title != "Writing" {
		t.Fatalf("legacy name not read: %+v %v", old, err)
	}
	if err := json.Unmarshal([]byte(`{"id":"1","content":"c","status":"in_progress","title":"Writing","active_form":"stale"}`), &cur); err != nil || cur.Title != "Writing" {
		t.Fatalf("title must win over the legacy name: %+v %v", cur, err)
	}
	b, err := json.Marshal(cur)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if stored := string(b); !strings.Contains(stored, `"title":"Writing"`) || strings.Contains(stored, "active_form") {
		t.Fatalf("stored form is not title: %s", stored)
	}
}
