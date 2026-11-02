// The tool-output history store: the Compress-Cite-Retrieve table the agent
// reads compressed output back from, and the one-time import of the file it
// used to live in.
//
// output_records is a table of the process's main state database in every mode
// (owner ruling 2026-11-01: one main database), so the store lives in the
// package that owns that database. It borrows the handle it is handed: nothing
// here opens, closes or migrates a database of its own.
package state

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
	_ "github.com/mattn/go-sqlite3"
)

// Store persists compressed-command history for later retrieval (CCR:
// Compress-Cite-Retrieve). Boost keeps the full redacted original of every
// compressed output in a database so the agent can always get back what a
// filter hid; this is what makes aggressive compression safe. We mirror that
// contract.
//
// The store borrows the state database handle: it never opens or closes a
// database of its own, and Close leaves the handle to whoever opened it.
type Store struct {
	db      *sql.DB
	dialect dbx.Dialect
	mu      sync.Mutex
}

// Record kinds. Boost keeps shell output and MCP tool responses in two separate
// tables (`commands` and `mcp_tool_calls`) even though the columns it needs are
// the same. We keep one table with a discriminator instead: the retrieval id
// stays a single namespace, so retrieve_output recovers an MCP response with the
// same code path it already uses for shell output, and savings for both
// mechanisms aggregate in one query.
const (
	KindShell = "shell"
	KindMCP   = "mcp"
)

// Entry is one compressed output record (shell command or MCP tool call).
type Entry struct {
	ID   int64
	Kind string // KindShell (default) or KindMCP
	// Command is the shell command line for KindShell, or "server/tool" for
	// KindMCP — in both cases the thing whose output was compressed.
	Command           string
	Timestamp         time.Time
	OriginalOutput    string
	FilteredOutput    string
	OriginalBytes     int
	FilteredBytes     int
	SavedTokens       int
	CapabilityID      string // e.g. "toml:builtin:make", "go:grep", "mcp:toon"
	CapabilityVer     string
	SessionID         string
	RetrieveCount     int
	RetrieveReason    string
	SpoolPath         string
	SpoolOmittedBytes int64
}

// The table's statements. The SQLite text is the text the store has always
// run; MySQL and PostgreSQL are translations with the same rows, the same
// order and the same nulls. PostgreSQL has no LastInsertId, so its insert
// hands the new id back the one way the engine can.
var (
	outputRecordInsertSQL = dbx.SQL{
		SQLite: `INSERT INTO output_records (kind, command, original_output, filtered_output,
			original_output_bytes, filtered_output_bytes, saved_tokens, session_id,
			capability_id, capability_version, spool_path, spool_omitted_bytes, created_at_ms)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		MySQL: `INSERT INTO output_records (kind, command, original_output, filtered_output,
			original_output_bytes, filtered_output_bytes, saved_tokens, session_id,
			capability_id, capability_version, spool_path, spool_omitted_bytes, created_at_ms)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		Postgres: `INSERT INTO output_records (kind, command, original_output, filtered_output,
			original_output_bytes, filtered_output_bytes, saved_tokens, session_id,
			capability_id, capability_version, spool_path, spool_omitted_bytes, created_at_ms)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id`,
	}
	outputRecordSelectSQL = dbx.SQL{
		SQLite: `SELECT id, command, original_output, filtered_output,
			original_output_bytes, filtered_output_bytes, saved_tokens,
			session_id, capability_id, capability_version, retrieve_count, retrieve_reason,
			kind, spool_path, spool_omitted_bytes, created_at_ms
		 FROM output_records WHERE id = ?`,
		MySQL: `SELECT id, command, original_output, filtered_output,
			original_output_bytes, filtered_output_bytes, saved_tokens,
			session_id, capability_id, capability_version, retrieve_count, retrieve_reason,
			kind, spool_path, spool_omitted_bytes, created_at_ms
		 FROM output_records WHERE id = ?`,
		Postgres: `SELECT id, command, original_output, filtered_output,
			original_output_bytes, filtered_output_bytes, saved_tokens,
			session_id, capability_id, capability_version, retrieve_count, retrieve_reason,
			kind, spool_path, spool_omitted_bytes, created_at_ms
		 FROM output_records WHERE id = $1`,
	}
	outputRecordRetrieveSQL = dbx.SQL{
		SQLite:   `UPDATE output_records SET retrieve_count = retrieve_count + 1, retrieve_reason = ? WHERE id = ?`,
		MySQL:    `UPDATE output_records SET retrieve_count = retrieve_count + 1, retrieve_reason = ? WHERE id = ?`,
		Postgres: `UPDATE output_records SET retrieve_count = retrieve_count + 1, retrieve_reason = $1 WHERE id = $2`,
	}
	// outputRecordTotalsSQL is the aggregate every savings report reads. Its
	// text is the same on all three engines — only the placeholders the WHERE
	// clause adds are numbered — so statsSince assembles it from this one
	// string.
	outputRecordTotalsSQL = `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN saved_tokens > 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(original_output_bytes), 0),
		COALESCE(SUM(filtered_output_bytes), 0),
		COALESCE(SUM(saved_tokens), 0),
		COALESCE(SUM(retrieve_count), 0)
		FROM output_records`
)

// OpenOutputHistory binds the tool-output history to the state database
// db. There is no second database to open in any mode: output_records is a
// table of the main one.
//
// stateRoot locates the pre-migration history file — <state root>/state/
// outfilter/history.db — and nothing else. An install that still has one hands
// its rows over here, once, best effort: a history nobody can read is worth
// less than a start-up that never happens, so a failed import is logged and the
// store starts empty rather than blocking the caller.
func OpenOutputHistory(ctx context.Context, db *sql.DB, stateRoot string) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("output history: no state database")
	}
	s := &Store{db: db, dialect: dbx.DialectOf(db)}
	if err := s.importLegacyHistory(ctx, stateRoot); err != nil {
		slog.Warn("kept the legacy tool-output history file without importing it",
			"path", legacyHistoryPath(stateRoot), "error", err)
	}
	return s, nil
}

// Save inserts a record and returns its retrieval id.
func (s *Store) Save(e Entry) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("store not open")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	if strings.TrimSpace(e.Kind) == "" {
		e.Kind = KindShell
	}
	args := []any{
		e.Kind, e.Command, e.OriginalOutput, e.FilteredOutput,
		e.OriginalBytes, e.FilteredBytes, e.SavedTokens, e.SessionID,
		e.CapabilityID, e.CapabilityVer, e.SpoolPath, e.SpoolOmittedBytes, e.Timestamp.UTC().UnixMilli(),
	}
	stmt := outputRecordInsertSQL.For(s.dialect)
	if s.dialect == dbx.Postgres {
		return insertOutputRecordReturningID(context.Background(), s.db, stmt, args)
	}
	res, err := dbx.Exec(context.Background(), s.db, stmt, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// insertOutputRecordReturningID runs the insert in a write transaction and
// hands back the id RETURNING gives it. PostgreSQL is the only engine whose
// driver cannot report the new id of an Exec, and the transaction is what puts
// the write behind the global lock every other out-of-transaction write takes.
func insertOutputRecordReturningID(ctx context.Context, db *sql.DB, stmt string, args []any) (int64, error) {
	tx, err := dbx.BeginTx(ctx, db, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	if err := tx.QueryRowContext(ctx, stmt, args...).Scan(&id); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// Get loads one record by retrieval id.
func (s *Store) Get(id int64) (Entry, error) {
	if s == nil || s.db == nil {
		return Entry{}, fmt.Errorf("store not open")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var e Entry
	var createdAtMs int64
	err := s.db.QueryRow(outputRecordSelectSQL.For(s.dialect), id).Scan(
		&e.ID, &e.Command, &e.OriginalOutput, &e.FilteredOutput,
		&e.OriginalBytes, &e.FilteredBytes, &e.SavedTokens,
		&e.SessionID, &e.CapabilityID, &e.CapabilityVer, &e.RetrieveCount, &e.RetrieveReason,
		&e.Kind, &e.SpoolPath, &e.SpoolOmittedBytes, &createdAtMs)
	if err != nil {
		return Entry{}, err
	}
	e.Timestamp = time.UnixMilli(createdAtMs)
	if strings.TrimSpace(e.SpoolPath) != "" {
		data, readErr := os.ReadFile(e.SpoolPath)
		if readErr != nil {
			// Spools are deliberately subject to a retention policy. Keep the
			// retrieval row useful after expiry instead of turning a normal cache
			// eviction into a misleading database/read failure.
			e.OriginalOutput = e.FilteredOutput + fmt.Sprintf("\n[full spooled output is no longer available: %v]\n", readErr)
			return e, nil
		}
		e.OriginalOutput = string(data)
		if e.SpoolOmittedBytes > 0 {
			e.OriginalOutput += fmt.Sprintf("\n... [%d bytes exceeded the spool quota] ...\n", e.SpoolOmittedBytes)
		}
	}
	return e, nil
}

// RecordRetrieve bumps the retrieve counter and records the reason, which the
// engine uses to auto-disable filters that repeatedly hide useful detail.
func (s *Store) RecordRetrieve(id int64, reason string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("store not open")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := dbx.Exec(context.Background(), s.db, outputRecordRetrieveSQL.For(s.dialect),
		strings.TrimSpace(reason), id,
	)
	return err
}

// Stats is the aggregate savings summary (boost report equivalent).
type Stats struct {
	TotalCommands    int64
	FilteredCommands int64
	OriginalBytes    int64
	FilteredBytes    int64
	SavedTokens      int64
	Retrievals       int64
}

// StatsSince aggregates savings for every record after since (zero = all),
// across both shell and MCP kinds.
func (s *Store) StatsSince(since time.Time) (Stats, error) {
	return s.statsSince(since, "")
}

// StatsSinceKind aggregates savings for one kind (KindShell / KindMCP). Split
// stats are what make the feedback loop actionable: a filter family whose
// retrieval count keeps climbing is hiding detail the agent needs, and that
// signal is meaningless if shell and MCP savings are pooled.
func (s *Store) StatsSinceKind(since time.Time, kind string) (Stats, error) {
	return s.statsSince(since, strings.TrimSpace(kind))
}

func (s *Store) statsSince(since time.Time, kind string) (Stats, error) {
	if s == nil || s.db == nil {
		return Stats{}, fmt.Errorf("store not open")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var st Stats
	query := outputRecordTotalsSQL
	var where []string
	args := []any{}
	if !since.IsZero() {
		where = append(where, `created_at_ms >= `+dbx.Placeholder(s.dialect, len(args)+1))
		args = append(args, since.UTC().UnixMilli())
	}
	if kind != "" {
		where = append(where, `kind = `+dbx.Placeholder(s.dialect, len(args)+1))
		args = append(args, kind)
	}
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	err := s.db.QueryRow(query, args...).Scan(
		&st.TotalCommands, &st.FilteredCommands, &st.OriginalBytes,
		&st.FilteredBytes, &st.SavedTokens, &st.Retrievals,
	)
	return st, err
}

// Close releases the store. The handle belongs to the composition root, which
// opened the one state database and hands it out; closing it here would take
// the database out from under every other store that borrowed it.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.db = nil
	return nil
}

// The one-time import of a pre-migration tool-output history file.
//
// Until the history became a table of the state database (owner ruling
// 2026-11-01: one main database), it was a SQLite file of its own at
// <state root>/state/outfilter/history.db. An install that still has one keeps
// its records — and the retrieval ids its transcripts cite — because the first
// open carries them into the main table and then deletes the file (owner ruling
// 2026-11-03: the old history.db must go, not be renamed aside). The main
// database is the history now, and a second copy of it under state/outfilter is
// the very file on shared storage a replica cannot read safely.
//
// Nothing here writes to the legacy file: it is read once and removed.

// The two statements the import itself runs against the main database: the
// explicit-id insert that keeps a transcript's citations resolving, and the
// PostgreSQL sequence re-seat that keeps the next saved id from colliding with
// an imported one.
var (
	outputRecordImportSQL = dbx.SQL{
		SQLite: `INSERT INTO output_records (id, kind, command, session_id, original_output, filtered_output,
			original_output_bytes, filtered_output_bytes, saved_tokens,
			capability_id, capability_version, retrieve_count, retrieve_reason,
			spool_path, spool_omitted_bytes, created_at_ms)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		MySQL: `INSERT INTO output_records (id, kind, command, session_id, original_output, filtered_output,
			original_output_bytes, filtered_output_bytes, saved_tokens,
			capability_id, capability_version, retrieve_count, retrieve_reason,
			spool_path, spool_omitted_bytes, created_at_ms)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		Postgres: `INSERT INTO output_records (id, kind, command, session_id, original_output, filtered_output,
			original_output_bytes, filtered_output_bytes, saved_tokens,
			capability_id, capability_version, retrieve_count, retrieve_reason,
			spool_path, spool_omitted_bytes, created_at_ms)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
	}
	// outputRecordReseatSequenceSQL moves the identity sequence to the highest
	// imported id. An explicit id never advances it, so without this the next
	// record saved would be handed an id an imported row already holds.
	outputRecordReseatSequenceSQL = `SELECT setval(pg_get_serial_sequence('output_records', 'id'), (SELECT MAX(id) FROM output_records))`
)

// legacyHistoryRelDir is where the history file lived under a state root.
var legacyHistoryRelDir = filepath.Join("state", "outfilter")

// legacyClaimSuffix names the file while it is being imported. The suffix is
// a claim, not a backup: the file is deleted once its rows are in the main
// table, and only a failed import ever puts the original name back.
const legacyClaimSuffix = ".importing-"

// legacyHistoryPath is the pre-migration history file for stateRoot, or "" for
// a caller that has no state root (an embedder's runtime, a unit test) and so
// cannot have one to import.
func legacyHistoryPath(stateRoot string) string {
	root := strings.TrimSpace(stateRoot)
	if root == "" {
		return ""
	}
	return filepath.Join(root, legacyHistoryRelDir, "history.db")
}

// importLegacyHistory carries an existing history file into the main database
// and deletes it.
//
// The file is claimed by renaming it before a single row is read: two processes
// can open the same home at once (the terminal and the gateway share it), both
// find the file, and only the one whose rename succeeds imports. The claimed
// path is the marker — the file is no longer where the next open looks — and it
// holds the rows only until they are in the main table. Rows go in one
// transaction, so an import that fails part way leaves the main table as it was
// instead of half a history; the claimed name is then put back, which is what
// makes the next open retry.
//
// The file is deleted after that transaction commits, and the directory with it
// when the file was all it held. A file holding no table this store ever wrote
// is not a history to carry over and not one to delete: it goes back under the
// name its owner gave it, and the next open finds the same nothing.
func (s *Store) importLegacyHistory(ctx context.Context, stateRoot string) error {
	path := legacyHistoryPath(stateRoot)
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil // no history file: the ordinary case for every new install
	}
	claimed := path + legacyClaimSuffix + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.Rename(path, claimed); err != nil {
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			return nil // another process claimed it first, and imports it
		}
		return err
	}
	entries, ours, err := readLegacyOutputRecords(ctx, claimed)
	if err != nil {
		return restoreLegacyHistoryName(claimed, path, err)
	}
	if !ours {
		return restoreLegacyHistoryName(claimed, path, nil)
	}
	if len(entries) > 0 {
		if err := s.insertImportedRecords(ctx, entries); err != nil {
			return restoreLegacyHistoryName(claimed, path, err)
		}
	}
	s.deleteLegacyHistoryFile(claimed)
	return nil
}

// restoreLegacyHistoryName puts a claimed file back under the name the next
// open looks for, so an import that failed is retried rather than lost. cause is
// what the caller reports; a restore that fails on its own is added to it.
func restoreLegacyHistoryName(claimed, path string, cause error) error {
	if err := os.Rename(claimed, path); err != nil {
		if cause == nil {
			return fmt.Errorf("put the legacy history file back: %w", err)
		}
		return fmt.Errorf("%w (and the legacy history file could not be put back: %v)", cause, err)
	}
	return cause
}

// deleteLegacyHistoryFile removes an imported history file, and the directory it
// lived in when the file was all it held. The rows are already in the main
// table, so a file that refuses to go is worth one log line and no more: a turn
// must not fail over a leftover copy of data the database already holds. A
// directory that is not empty is the ordinary answer from os.Remove, not an
// error worth reporting.
func (s *Store) deleteLegacyHistoryFile(claimed string) {
	if err := os.Remove(claimed); err != nil {
		slog.Warn("imported the legacy tool-output history but could not delete the file",
			"path", claimed, "error", err)
		return
	}
	_ = os.Remove(filepath.Dir(claimed))
}

// legacyEntry is one record read out of the legacy file, with the id it was
// stored under so a transcript's citation still resolves.
type legacyEntry struct {
	Entry
	createdAtMs int64
}

// readLegacyOutputRecords reads the file's records, tolerating both shapes it
// has been released in: the current output_records table, and the earlier
// commands table, whose columns vary by vintage and so are mapped by name. ours
// reports whether the file held either table; false means the file is not this
// store's history at all, and nothing in it is ours to import or to delete.
func readLegacyOutputRecords(ctx context.Context, path string) (entries []legacyEntry, ours bool, err error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = db.Close() }()
	has, err := legacyHistoryHasTable(ctx, db, "output_records")
	if err != nil {
		return nil, false, err
	}
	if has {
		entries, err = readLegacyOutputTable(ctx, db)
		return entries, true, err
	}
	has, err = legacyHistoryHasTable(ctx, db, "commands")
	if err != nil {
		return nil, false, err
	}
	if !has {
		// A file with neither table held nothing this store wrote.
		return nil, false, nil
	}
	entries, err = readLegacyCommandsTable(ctx, db)
	return entries, true, err
}

// legacyHistoryHasTable asks the legacy file itself, which is the only place
// its shape is knowable. (schema_migrations.go has a legacyTableExists of its
// own for the schema copy; this one reads a detached file through *sql.DB.)
func legacyHistoryHasTable(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
		return false, fmt.Errorf("read the legacy history file: %w", err)
	}
	return n > 0, nil
}

func readLegacyOutputTable(ctx context.Context, db *sql.DB) ([]legacyEntry, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, kind, command, session_id, original_output, filtered_output,
			original_output_bytes, filtered_output_bytes, saved_tokens,
			capability_id, capability_version, retrieve_count, retrieve_reason,
			spool_path, spool_omitted_bytes, created_at_ms
		 FROM output_records ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read the legacy history file: %w", err)
	}
	defer rows.Close()
	var out []legacyEntry
	for rows.Next() {
		var e legacyEntry
		if err := rows.Scan(&e.ID, &e.Kind, &e.Command, &e.SessionID, &e.OriginalOutput, &e.FilteredOutput,
			&e.OriginalBytes, &e.FilteredBytes, &e.SavedTokens,
			&e.CapabilityID, &e.CapabilityVer, &e.RetrieveCount, &e.RetrieveReason,
			&e.SpoolPath, &e.SpoolOmittedBytes, &e.createdAtMs); err != nil {
			return nil, fmt.Errorf("read the legacy history file: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// legacyCommandsColumns maps the v0 commands table onto the v1 record. It is
// keyed by column name because the table's own shape depends on the release
// that wrote it: cmd and command are the same string under two names, and a
// column the vintage predates reads as its v1 default.
type legacyCommandsColumns struct {
	id, command, sessionID, originalOutput, filteredOutput string
	capabilityID, capabilityVer, spoolPath                 string
	retrieveReason, timestamp                              string
	originalBytes, filteredBytes, savedTokens              string
	retrieveCount, spoolOmittedBytes                       string
}

func readLegacyCommandsTable(ctx context.Context, db *sql.DB) ([]legacyEntry, error) {
	rows, err := db.QueryContext(ctx, `SELECT * FROM commands ORDER BY rowid`)
	if err != nil {
		return nil, fmt.Errorf("read the legacy history file: %w", err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	position := map[string]int{}
	for i, column := range columns {
		position[strings.ToLower(column)] = i
	}
	pick := func(names ...string) string {
		for _, name := range names {
			if _, ok := position[name]; ok {
				return name
			}
		}
		return ""
	}
	c := legacyCommandsColumns{
		id:                pick("id"),
		command:           pick("cmd", "command"),
		sessionID:         pick("session_id"),
		originalOutput:    pick("original_output"),
		filteredOutput:    pick("filtered_output"),
		capabilityID:      pick("capability_id"),
		capabilityVer:     pick("capability_version"),
		spoolPath:         pick("spool_path"),
		retrieveReason:    pick("retrieve_reason"),
		timestamp:         pick("timestamp"),
		originalBytes:     pick("original_output_bytes"),
		filteredBytes:     pick("filtered_output_bytes"),
		savedTokens:       pick("saved_tokens"),
		retrieveCount:     pick("retrieve_count"),
		spoolOmittedBytes: pick("spool_omitted_bytes"),
	}
	var out []legacyEntry
	// lastID keeps the retrieval ids unique when the file has no id column to
	// give them, which no released shape lacks but a hand-made one might.
	var lastID int64
	for rows.Next() {
		values := make([]any, len(columns))
		holders := make([]any, len(columns))
		for i := range values {
			holders[i] = &values[i]
		}
		if err := rows.Scan(holders...); err != nil {
			return nil, fmt.Errorf("read the legacy history file: %w", err)
		}
		text := func(name string) string {
			i, ok := position[name]
			if !ok {
				return ""
			}
			return legacyText(values[i])
		}
		number := func(name string) int64 {
			i, ok := position[name]
			if !ok {
				return 0
			}
			return legacyInt(values[i])
		}
		e := legacyEntry{Entry: Entry{
			Kind:              KindShell,
			Command:           text(c.command),
			SessionID:         text(c.sessionID),
			OriginalOutput:    text(c.originalOutput),
			FilteredOutput:    text(c.filteredOutput),
			OriginalBytes:     int(number(c.originalBytes)),
			FilteredBytes:     int(number(c.filteredBytes)),
			SavedTokens:       int(number(c.savedTokens)),
			CapabilityID:      text(c.capabilityID),
			CapabilityVer:     text(c.capabilityVer),
			RetrieveCount:     int(number(c.retrieveCount)),
			RetrieveReason:    text(c.retrieveReason),
			SpoolPath:         text(c.spoolPath),
			SpoolOmittedBytes: number(c.spoolOmittedBytes),
		}}
		if kind := text("kind"); kind != "" {
			e.Kind = kind
		}
		e.ID = number(c.id)
		if e.ID == 0 {
			e.ID = lastID + 1
		}
		lastID = e.ID
		e.createdAtMs = legacyTimestampMs(text(c.timestamp))
		out = append(out, e)
	}
	return out, rows.Err()
}

// legacyTimestampMs converts the v0 TEXT timestamp into the milliseconds the
// v1 table stores. A stamp that does not parse keeps the epoch rather than
// failing the import: the record is still retrievable by id, only its time is
// unknown, and a record kept under an unknown time beats a turn that will not
// start.
func legacyTimestampMs(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	for _, layout := range []string{
		time.RFC3339Nano, time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC().UnixMilli()
		}
	}
	return 0
}

func legacyText(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case []byte:
		return string(value)
	default:
		return fmt.Sprint(value)
	}
}

func legacyInt(v any) int64 {
	switch value := v.(type) {
	case nil:
		return 0
	case int64:
		return value
	case float64:
		return int64(value)
	case bool:
		if value {
			return 1
		}
		return 0
	case []byte:
		n, _ := strconv.ParseInt(strings.TrimSpace(string(value)), 10, 64)
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return n
	default:
		return 0
	}
}

// insertImportedRecords writes the imported records in one transaction, ids
// and all. PostgreSQL's identity column does not move for an explicit id, so
// its sequence is re-seated on the highest one afterwards — otherwise the next
// record saved would collide with an imported row.
func (s *Store) insertImportedRecords(ctx context.Context, entries []legacyEntry) error {
	tx, err := dbx.BeginTx(ctx, s.db, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	insert := outputRecordImportSQL.For(s.dialect)
	for _, e := range entries {
		if _, err := tx.ExecContext(ctx, insert,
			e.ID, e.Kind, e.Command, e.SessionID, e.OriginalOutput, e.FilteredOutput,
			e.OriginalBytes, e.FilteredBytes, e.SavedTokens,
			e.CapabilityID, e.CapabilityVer, e.RetrieveCount, e.RetrieveReason,
			e.SpoolPath, e.SpoolOmittedBytes, e.createdAtMs,
		); err != nil {
			return err
		}
	}
	if s.dialect == dbx.Postgres {
		var reseated sql.NullInt64
		if err := tx.QueryRowContext(ctx, outputRecordReseatSequenceSQL).Scan(&reseated); err != nil {
			return err
		}
	}
	return tx.Commit()
}
