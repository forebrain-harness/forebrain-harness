// The tool-output history table: what a saved record reads back as, what the
// savings report aggregates, and the one-time import of the file the table
// replaced.
package state

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// openStateDBForTest opens a scratch state database: output_records is a table
// of that database in every mode, so a test that needs a history needs this
// handle rather than a file of its own.
func openStateDBForTest(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open the state database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// openOutputHistoryForTest binds a history store to a scratch state database.
func openOutputHistoryForTest(t *testing.T) *Store {
	t.Helper()
	store, err := OpenOutputHistory(context.Background(), openStateDBForTest(t), "")
	if err != nil {
		t.Fatalf("open the output history: %v", err)
	}
	return store
}

func TestStoreGetReadsSpooledOutput(t *testing.T) {
	dir := t.TempDir()
	store := openOutputHistoryForTest(t)
	spool := filepath.Join(dir, "command.output")
	if err := os.WriteFile(spool, []byte("full output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := store.Save(Entry{Command: "noisy", SpoolPath: spool, OriginalBytes: 12, SpoolOmittedBytes: 3})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(entry.OriginalOutput, "full output") || !strings.Contains(entry.OriginalOutput, "exceeded the spool quota") {
		t.Fatalf("output=%q", entry.OriginalOutput)
	}
}

func TestStoreGetReportsExpiredSpoolWithoutFailing(t *testing.T) {
	dir := t.TempDir()
	store := openOutputHistoryForTest(t)
	id, err := store.Save(Entry{
		Command: "noisy", FilteredOutput: "bounded preview", SpoolPath: filepath.Join(dir, "expired.output"),
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(entry.OriginalOutput, "bounded preview") || !strings.Contains(entry.OriginalOutput, "no longer available") {
		t.Fatalf("output=%q", entry.OriginalOutput)
	}
}

func TestStoreKindRoundTrip(t *testing.T) {
	store := openOutputHistoryForTest(t)

	shellID, err := store.Save(Entry{Kind: KindShell, Command: "make", OriginalOutput: "original-shell", FilteredOutput: "filtered-shell", OriginalBytes: 100, FilteredBytes: 10, SavedTokens: 22, CapabilityID: "toml:make"})
	if err != nil {
		t.Fatalf("Save shell: %v", err)
	}
	mcpID, err := store.Save(Entry{Kind: KindMCP, Command: "github/list_issues", OriginalOutput: "original-mcp", FilteredOutput: "filtered-mcp", OriginalBytes: 200, FilteredBytes: 20, SavedTokens: 44, CapabilityID: "mcp:toon"})
	if err != nil {
		t.Fatalf("Save mcp: %v", err)
	}

	shellEntry, err := store.Get(shellID)
	if err != nil {
		t.Fatalf("Get shell: %v", err)
	}
	if shellEntry.Kind != KindShell {
		t.Errorf("Kind=%q want %q", shellEntry.Kind, KindShell)
	}

	mcpEntry, err := store.Get(mcpID)
	if err != nil {
		t.Fatalf("Get mcp: %v", err)
	}
	if mcpEntry.Kind != KindMCP {
		t.Errorf("Kind=%q want %q", mcpEntry.Kind, KindMCP)
	}
}

func TestStatsSinceKindSeparatesShellAndMCP(t *testing.T) {
	store := openOutputHistoryForTest(t)

	_, _ = store.Save(Entry{Kind: KindShell, Command: "make", SavedTokens: 10, CapabilityID: "toml:make"})
	_, _ = store.Save(Entry{Kind: KindShell, Command: "make", SavedTokens: 5, CapabilityID: "toml:make"})
	_, _ = store.Save(Entry{Kind: KindMCP, Command: "srv/tool", SavedTokens: 20, CapabilityID: "mcp:toon"})

	all, err := store.StatsSince(time.Time{})
	if err != nil {
		t.Fatalf("StatsSince: %v", err)
	}
	if all.TotalCommands != 3 {
		t.Errorf("TotalCommands=%d want 3", all.TotalCommands)
	}
	if all.SavedTokens != 35 {
		t.Errorf("SavedTokens=%d want 35", all.SavedTokens)
	}

	shellOnly, err := store.StatsSinceKind(time.Time{}, KindShell)
	if err != nil {
		t.Fatalf("StatsSinceKind shell: %v", err)
	}
	if shellOnly.TotalCommands != 2 {
		t.Errorf("shell TotalCommands=%d want 2", shellOnly.TotalCommands)
	}
	if shellOnly.SavedTokens != 15 {
		t.Errorf("shell SavedTokens=%d want 15", shellOnly.SavedTokens)
	}

	mcpOnly, err := store.StatsSinceKind(time.Time{}, KindMCP)
	if err != nil {
		t.Fatalf("StatsSinceKind mcp: %v", err)
	}
	if mcpOnly.TotalCommands != 1 {
		t.Errorf("mcp TotalCommands=%d want 1", mcpOnly.TotalCommands)
	}
	if mcpOnly.SavedTokens != 20 {
		t.Errorf("mcp SavedTokens=%d want 20", mcpOnly.SavedTokens)
	}
}

// writeLegacyHistoryFile creates the history file an install carries from
// before the table moved into the state database. shape is its schema script,
// run against a fresh SQLite file at <stateRoot>/state/outfilter/history.db.
func writeLegacyHistoryFile(t *testing.T, stateRoot, shape string) string {
	t.Helper()
	path := legacyHistoryPath(stateRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open the legacy history file: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(shape); err != nil {
		t.Fatalf("create the legacy history file: %v", err)
	}
	return path
}

const legacyV1Shape = `
CREATE TABLE output_records (
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('shell', 'mcp')),
  command TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  original_output TEXT NOT NULL,
  filtered_output TEXT NOT NULL,
  original_output_bytes INTEGER NOT NULL,
  filtered_output_bytes INTEGER NOT NULL,
  saved_tokens INTEGER NOT NULL,
  capability_id TEXT NOT NULL DEFAULT '',
  capability_version TEXT NOT NULL DEFAULT '',
  retrieve_count INTEGER NOT NULL DEFAULT 0,
  retrieve_reason TEXT NOT NULL DEFAULT '',
  spool_path TEXT NOT NULL DEFAULT '',
  spool_omitted_bytes INTEGER NOT NULL DEFAULT 0,
  created_at_ms INTEGER NOT NULL
) STRICT;
INSERT INTO output_records (id, kind, command, session_id, original_output, filtered_output,
  original_output_bytes, filtered_output_bytes, saved_tokens, retrieve_count, created_at_ms)
VALUES
  (1, 'shell', 'make all', 's-1', 'orig-a', 'filt-a', 100, 40, 900, 2, 1767323045000),
  (2, 'mcp', 'server/tool', 's-2', 'orig-b', 'filt-b', 200, 80, 400, 0, 1767323100000),
  (3, 'shell', 'go test', 's-1', 'orig-c', 'filt-c', 50, 50, 0, 1, 1767323160000);`

// A history file from before the table joined the state database is carried
// into it once, ids and timestamps intact, and then deleted — file and, when
// the file was all it held, the state/outfilter directory with it. A second open
// has nothing left to import.
//
// The file cannot be imported twice from the same path, because the import that
// succeeds is the one that removes it: an install only ever sees a second copy
// if a user restores one from a backup, and that copy is refused by the ids its
// rows already hold rather than imported again (see
// TestLegacyHistoryRestoredCopyIsNotImportedTwice).
func TestLegacyHistoryFileIsImportedOnceAndDeleted(t *testing.T) {
	stateRoot := t.TempDir()
	path := writeLegacyHistoryFile(t, stateRoot, legacyV1Shape)
	ctx := context.Background()
	db := openStateDBForTest(t)

	store, err := OpenOutputHistory(ctx, db, stateRoot)
	if err != nil {
		t.Fatalf("import the legacy history: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the legacy file must be deleted, stat err = %v", err)
	}
	if left, err := filepath.Glob(filepath.Join(filepath.Dir(path), "history.db*")); err != nil || len(left) != 0 {
		t.Fatalf("the legacy file must be deleted, not kept aside: %v (%v)", left, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("the empty state/outfilter directory must be removed with the file, stat err = %v", err)
	}

	// The rows are in the main database, under the ids a transcript cites.
	entry, err := store.Get(1)
	if err != nil {
		t.Fatalf("Get(1) after import: %v", err)
	}
	if entry.Command != "make all" || entry.OriginalOutput != "orig-a" || entry.Kind != KindShell {
		t.Fatalf("record 1 = %+v, want the seeded command and output", entry)
	}
	if got := entry.Timestamp.UTC(); !got.Equal(time.UnixMilli(1767323045000).UTC()) {
		t.Fatalf("record 1 timestamp = %v, want the seeded millisecond", got)
	}
	if entry.RetrieveCount != 2 {
		t.Fatalf("record 1 retrieve count = %d, want 2", entry.RetrieveCount)
	}
	if mcp, err := store.Get(2); err != nil || mcp.Kind != KindMCP {
		t.Fatalf("record 2 = %+v (%v), want its mcp kind", mcp, err)
	}
	stats, err := store.StatsSince(time.Time{})
	if err != nil {
		t.Fatalf("StatsSince: %v", err)
	}
	if stats.TotalCommands != 3 || stats.FilteredCommands != 2 || stats.OriginalBytes != 350 ||
		stats.FilteredBytes != 170 || stats.SavedTokens != 1300 || stats.Retrievals != 3 {
		t.Fatalf("stats after import = %+v, want the seeded totals", stats)
	}

	// A second open imports nothing again: the file it would have read is gone,
	// so the table keeps its three rows rather than six, and nothing under the
	// state root comes back.
	again, err := OpenOutputHistory(ctx, db, stateRoot)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	stats, err = again.StatsSince(time.Time{})
	if err != nil {
		t.Fatalf("StatsSince after the second open: %v", err)
	}
	if stats.TotalCommands != 3 {
		t.Fatalf("the second open imported the history again: %+v", stats)
	}
	if matches, err := filepath.Glob(filepath.Join(stateRoot, "state", "outfilter", "*")); err != nil || len(matches) != 0 {
		t.Fatalf("the second open must leave nothing under state/outfilter, got %v (%v)", matches, err)
	}
}

// A copy of the history file restored from a backup — the same rows under the
// same ids the main table already holds — is not imported a second time. The
// delete-after-import is what makes that the only way to see the file again,
// and the explicit-id insert is what refuses it: the rows collide with
// themselves, the transaction rolls back, the file keeps the name it was given
// and the next open tries it again.
func TestLegacyHistoryRestoredCopyIsNotImportedTwice(t *testing.T) {
	stateRoot := t.TempDir()
	path := writeLegacyHistoryFile(t, stateRoot, legacyV1Shape)
	ctx := context.Background()
	db := openStateDBForTest(t)

	if _, err := OpenOutputHistory(ctx, db, stateRoot); err != nil {
		t.Fatalf("import the legacy history: %v", err)
	}
	// The user puts a copy back, exactly as a restore would.
	writeLegacyHistoryFile(t, stateRoot, legacyV1Shape)

	store, err := OpenOutputHistory(ctx, db, stateRoot)
	if err != nil {
		t.Fatalf("a restored copy must not fail the open: %v", err)
	}
	stats, err := store.StatsSince(time.Time{})
	if err != nil {
		t.Fatalf("StatsSince: %v", err)
	}
	if stats.TotalCommands != 3 || stats.SavedTokens != 1300 {
		t.Fatalf("the restored copy was imported on top of itself: %+v", stats)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a copy the import refused must be left where it was: %v", err)
	}
	if left, err := filepath.Glob(filepath.Join(filepath.Dir(path), "history.db*")); err != nil || len(left) != 1 || left[0] != path {
		t.Fatalf("the refused copy must keep its own name, got %v (%v)", left, err)
	}
}

const legacyV0Shape = `
CREATE TABLE commands (
  id INTEGER PRIMARY KEY,
  timestamp TEXT NOT NULL,
  cmd TEXT NOT NULL DEFAULT '',
  original_output TEXT NOT NULL DEFAULT '',
  filtered_output TEXT NOT NULL DEFAULT '',
  original_output_bytes INTEGER NOT NULL DEFAULT 0,
  filtered_output_bytes INTEGER NOT NULL DEFAULT 0,
  saved_tokens INTEGER NOT NULL DEFAULT 0,
  retrieve_count INTEGER NOT NULL DEFAULT 0,
  session_id TEXT NOT NULL DEFAULT '',
  capability_id TEXT NOT NULL DEFAULT '',
  capability_version TEXT NOT NULL DEFAULT '',
  retrieve_reason TEXT NOT NULL DEFAULT ''
);
INSERT INTO commands (timestamp, cmd, saved_tokens) VALUES ('2025-01-01T00:00:00Z', 'make', 7);
INSERT INTO commands (timestamp, cmd, saved_tokens) VALUES ('2025-01-02 03:04:05', 'go test', 3);`

// A file still on the released `commands` table imports too: its rows are
// mapped by name, a column the vintage predates reads as its default, and a
// timestamp that is not RFC 3339 is parsed the way the driver spelled it.
func TestLegacyHistoryCommandsTableIsImported(t *testing.T) {
	stateRoot := t.TempDir()
	writeLegacyHistoryFile(t, stateRoot, legacyV0Shape)
	ctx := context.Background()

	store, err := OpenOutputHistory(ctx, openStateDBForTest(t), stateRoot)
	if err != nil {
		t.Fatalf("import the legacy history: %v", err)
	}
	stats, err := store.StatsSince(time.Time{})
	if err != nil {
		t.Fatalf("StatsSince: %v", err)
	}
	if stats.TotalCommands != 2 || stats.SavedTokens != 10 {
		t.Fatalf("stats = %+v, want both commands and their savings", stats)
	}
	entry, err := store.Get(2)
	if err != nil {
		t.Fatalf("Get(2): %v", err)
	}
	if entry.Command != "go test" || entry.Kind != KindShell {
		t.Fatalf("record 2 = %+v, want the seeded command as a shell record", entry)
	}
	if got := entry.Timestamp.UTC(); !got.Equal(time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("record 2 timestamp = %v, want the parsed second", got)
	}
}

// A file that holds nothing this store wrote — neither table — is not a history
// to import and not one to delete: it goes back under the name it was found
// under, and the store opens empty.
func TestLegacyHistoryFileWithoutEitherTableIsUnreadable(t *testing.T) {
	stateRoot := t.TempDir()
	path := writeLegacyHistoryFile(t, stateRoot, `CREATE TABLE something_else (id INTEGER PRIMARY KEY)`)
	store, err := OpenOutputHistory(context.Background(), openStateDBForTest(t), stateRoot)
	if err != nil {
		t.Fatalf("a legacy file with no history table must not fail the open: %v", err)
	}
	stats, err := store.StatsSince(time.Time{})
	if err != nil || stats.TotalCommands != 0 {
		t.Fatalf("stats = %+v (%v), want an empty table", stats, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a file holding no history of ours must be left where it was: %v", err)
	}
	if left, err := filepath.Glob(filepath.Join(filepath.Dir(path), "history.db*")); err != nil || len(left) != 1 || left[0] != path {
		t.Fatalf("the unreadable file must keep its own name, got %v (%v)", left, err)
	}
}

// No mode opens a history file of its own any more: the store writes into the
// state database it was handed and creates nothing under the state root. This
// is the owner's ruling as a check — one main database, in every mode.
func TestOutputHistoryCreatesNoFileUnderTheStateRoot(t *testing.T) {
	stateRoot := t.TempDir()
	store, err := OpenOutputHistory(context.Background(), openStateDBForTest(t), stateRoot)
	if err != nil {
		t.Fatalf("open the output history: %v", err)
	}
	if _, err := store.Save(Entry{Command: "make all", OriginalOutput: "orig", FilteredOutput: "filt"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateRoot, "state", "outfilter")); !os.IsNotExist(err) {
		t.Fatalf("the store created %s instead of writing to the state database",
			filepath.Join(stateRoot, "state", "outfilter"))
	}
	stats, err := store.StatsSince(time.Time{})
	if err != nil || stats.TotalCommands != 1 {
		t.Fatalf("stats = %+v (%v), want the one row in the state database", stats, err)
	}
}
