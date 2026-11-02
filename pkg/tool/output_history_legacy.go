// The one-time import of a pre-migration tool-output history file.
//
// Until the history became a table of the state database (owner ruling
// 2026-11-01: one main database), it was a SQLite file of its own at
// <state root>/state/outfilter/history.db. An install that still has one keeps
// its records — and the retrieval ids its transcripts cite — because the first
// open carries them into the main table and renames the file aside as a backup.
// Nothing here writes to the legacy file, and nothing deletes it.
package tool

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
	_ "github.com/mattn/go-sqlite3"
)

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

// importLegacyHistory carries an existing history file into the main database.
//
// The file is claimed by renaming it before a single row is read: two processes
// can open the same home at once (the terminal and the gateway share it), both
// find the file, and only the one whose rename succeeds imports. The renamed
// path is both the marker — the file is no longer where the next open looks —
// and the backup. Rows go in one transaction, so an import that fails part way
// leaves the main table as it was instead of half a history.
func (s *Store) importLegacyHistory(ctx context.Context, stateRoot string) error {
	path := legacyHistoryPath(stateRoot)
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil // no history file: the ordinary case for every new install
	}
	aside := path + ".imported-" + strconv.FormatInt(time.Now().Unix(), 10)
	if err := os.Rename(path, aside); err != nil {
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			return nil // another process claimed it first, and imports it
		}
		return err
	}
	entries, err := readLegacyOutputRecords(ctx, aside)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	return s.insertImportedRecords(ctx, entries)
}

// legacyEntry is one record read out of the legacy file, with the id it was
// stored under so a transcript's citation still resolves.
type legacyEntry struct {
	Entry
	createdAtMs int64
}

// readLegacyOutputRecords reads the file's records, tolerating both shapes it
// has been released in: the current output_records table, and the earlier
// commands table, whose columns vary by vintage and so are mapped by name.
func readLegacyOutputRecords(ctx context.Context, path string) ([]legacyEntry, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	has, err := legacyTableExists(ctx, db, "output_records")
	if err != nil {
		return nil, err
	}
	if has {
		return readLegacyOutputTable(ctx, db)
	}
	has, err = legacyTableExists(ctx, db, "commands")
	if err != nil {
		return nil, err
	}
	if !has {
		// A file with neither table held nothing this store wrote: it is kept
		// as it was and the import is over.
		return nil, nil
	}
	return readLegacyCommandsTable(ctx, db)
}

// legacyTableExists asks the legacy file itself, which is the only place its
// shape is knowable.
func legacyTableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
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
// unknown, and the file it came from is kept either way.
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
