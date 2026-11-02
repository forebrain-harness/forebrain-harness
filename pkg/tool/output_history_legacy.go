// The one-time import of a pre-migration tool-output history file.
//
// Until the history became a table of the state database (owner ruling
// 2026-11-01: one main database), it was a SQLite file of its own at
// <state root>/state/outfilter/history.db. An install that still has one keeps
// its records — and the retrieval ids its transcripts cite — because the first
// open carries them into the main table and then deletes the file (owner
// ruling 2026-11-03: the old history.db must go, not be renamed aside). The main
// database is the history now, and a second copy of it under state/outfilter is
// the very file on shared storage a replica cannot read safely.
//
// Nothing here writes to the legacy file: it is read once and removed.
package tool

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
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
	has, err := legacyTableExists(ctx, db, "output_records")
	if err != nil {
		return nil, false, err
	}
	if has {
		entries, err = readLegacyOutputTable(ctx, db)
		return entries, true, err
	}
	has, err = legacyTableExists(ctx, db, "commands")
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
