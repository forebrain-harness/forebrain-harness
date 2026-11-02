package tool

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// openStateDBForTest opens a scratch state database. The tool-output history is
// a table of that database in every mode, so a test that needs a history needs
// this handle rather than a file of its own.
func openStateDBForTest(t *testing.T) *sql.DB {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
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
// into it once, ids and timestamps intact, and then set aside under a name that
// says so. Nothing is deleted: an install keeps its old file as a backup, and
// the second open has nothing left to import.
func TestLegacyHistoryFileIsImportedOnceAndKept(t *testing.T) {
	stateRoot := t.TempDir()
	path := writeLegacyHistoryFile(t, stateRoot, legacyV1Shape)
	ctx := context.Background()
	db := openStateDBForTest(t)

	store, err := OpenOutputHistory(ctx, db, stateRoot)
	if err != nil {
		t.Fatalf("import the legacy history: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the legacy file must be renamed aside, stat err = %v", err)
	}
	aside, err := filepath.Glob(path + ".imported-*")
	if err != nil || len(aside) != 1 {
		t.Fatalf("the legacy file must be kept as history.db.imported-<unix>, got %v (%v)", aside, err)
	}
	if info, err := os.Stat(aside[0]); err != nil || info.Size() == 0 {
		t.Fatalf("the kept file must still hold its rows: %v", err)
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

	// A second open imports nothing again: the file it would have read is no
	// longer where it looks, so the table keeps its three rows rather than six.
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
	if matches, err := filepath.Glob(path + ".imported-*"); err != nil || len(matches) != 1 {
		t.Fatalf("the second open must not rename anything, got %v (%v)", matches, err)
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

// A file that holds nothing this store wrote — neither table — is left exactly
// where it is, renamed but never rewritten, and the store opens empty.
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
	if matches, err := filepath.Glob(path + ".imported-*"); err != nil || len(matches) != 1 {
		t.Fatalf("the unreadable file must still be kept, got %v (%v)", matches, err)
	}
}
