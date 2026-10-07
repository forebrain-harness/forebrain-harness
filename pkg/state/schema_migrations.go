package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
)

// stateSchemaVersion is the shape schema.sql declares. A database at a lower
// version is carried forward by the migrations below; one at a higher version
// was written by a newer binary.
const stateSchemaVersion = 8

var (
	// ErrStateSchemaNewer is returned when the file was written by a newer
	// binary and must not be touched by this one.
	ErrStateSchemaNewer = errors.New("state database was written by a newer forebrain; upgrade forebrain to open it")
	// ErrStateSchemaOutdated is returned by read-only opens of a database the
	// writable path would have migrated first.
	ErrStateSchemaOutdated = errors.New("state database needs an upgrade; start forebrain once to upgrade it")
)

// schemaMigration carries a database from version-1 to version in one
// transaction on one connection. It runs with foreign keys off and holds the
// write lock, so it may rebuild tables wholesale but must leave every
// reference intact: the caller checks PRAGMA foreign_key_check before commit.
type schemaMigration struct {
	version int
	apply   func(ctx context.Context, conn *sql.Conn) error
}

var stateSchemaMigrations = []schemaMigration{
	{version: 1, apply: migrateStateV0ToV1},
	{version: 2, apply: migrateStateV1ToV2},
	{version: 3, apply: migrateStateV2ToV3},
	{version: 4, apply: migrateStateV3ToV4},
	{version: 5, apply: migrateStateV4ToV5},
	{version: 6, apply: migrateStateV5ToV6},
	{version: 7, apply: migrateStateV6ToV7},
	{version: 8, apply: migrateStateV7ToV8},
}

// migrateStateV3ToV4 adds the session-purpose column. SQLite allows ADD
// COLUMN with a non-NULL default, so it is a single statement in the
// migration's own transaction; every existing row reads as an ordinary
// conversation. A database whose table already carries the column (a fixture
// built from the current schema.sql with a lowered user_version) skips the
// statement instead of failing on a duplicate.
func migrateStateV3ToV4(ctx context.Context, conn *sql.Conn) error {
	var present int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('fb_sessions') WHERE name='source'`).Scan(&present); err != nil {
		return err
	}
	if present > 0 {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `ALTER TABLE fb_sessions ADD COLUMN source TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add fb_sessions.source: %w", err)
	}
	return nil
}

// migrateStateV4ToV5 adds run ownership: the owner column on fb_runs, the
// fb_run_owners lease table, and the index behind the session-exclusivity
// check. Every statement tolerates the object already existing — a fixture
// built from the current schema.sql with a lowered user_version skips it
// instead of failing on a duplicate. Existing running rows migrate with
// owner = ” (two ASCII apostrophes): the process that drove them is long
// gone, so the first reaper to run ends them as abandoned.
func migrateStateV4ToV5(ctx context.Context, conn *sql.Conn) error {
	var ownerCol int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('fb_runs') WHERE name='owner'`).Scan(&ownerCol); err != nil {
		return err
	}
	if ownerCol == 0 {
		if _, err := conn.ExecContext(ctx, `ALTER TABLE fb_runs ADD COLUMN owner TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add fb_runs.owner: %w", err)
		}
	}
	var ownersTable int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='fb_run_owners'`).Scan(&ownersTable); err != nil {
		return err
	}
	if ownersTable == 0 {
		// The same text schema.sql declares, so an upgraded file and a fresh
		// one hold the same object byte for byte.
		if _, err := conn.ExecContext(ctx, `
CREATE TABLE fb_run_owners (
  owner TEXT PRIMARY KEY CHECK (TRIM(owner) <> ''),
  heartbeat_at_ms INTEGER NOT NULL
) STRICT`); err != nil {
			return fmt.Errorf("create fb_run_owners: %w", err)
		}
	}
	var liveIdx int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_fb_runs_session_live'`).Scan(&liveIdx); err != nil {
		return err
	}
	if liveIdx == 0 {
		if _, err := conn.ExecContext(ctx, `
CREATE INDEX idx_fb_runs_session_live ON fb_runs(session_id)
  WHERE parent_run_id IS NULL AND status IN ('running', 'waiting_action')`); err != nil {
			return fmt.Errorf("create idx_fb_runs_session_live: %w", err)
		}
	}
	return nil
}

// migrateStateV5ToV6 indexes fire records by the session they ran in — a
// run's end finds its fire by that session — and marks the sessions of
// fires recorded before this version as what they are, so they leave the
// conversation lists the way every later fire's session is born out of them.
// The two new columns are added the way v4 added its column: a fixture built
// from the current schema.sql with a lowered user_version already carries
// them and skips the ALTER instead of failing on a duplicate.
func migrateStateV5ToV6(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_fb_cron_runs_session ON fb_cron_runs(session_id)`); err != nil {
		return fmt.Errorf("create idx_fb_cron_runs_session: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE fb_sessions SET source = 'cron' WHERE source = '' AND id IN (SELECT session_id FROM fb_cron_runs)`); err != nil {
		return fmt.Errorf("mark the sessions of recorded fires: %w", err)
	}
	var runCodeCol int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('fb_cron_runs') WHERE name='error_code'`).Scan(&runCodeCol); err != nil {
		return err
	}
	if runCodeCol == 0 {
		if _, err := conn.ExecContext(ctx, `ALTER TABLE fb_cron_runs ADD COLUMN error_code TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add fb_cron_runs.error_code: %w", err)
		}
	}
	var jobCodeCol int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('fb_cron_jobs') WHERE name='last_error_code'`).Scan(&jobCodeCol); err != nil {
		return err
	}
	if jobCodeCol == 0 {
		if _, err := conn.ExecContext(ctx, `ALTER TABLE fb_cron_jobs ADD COLUMN last_error_code TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add fb_cron_jobs.last_error_code: %w", err)
		}
	}
	return nil
}

// migrateStateV6ToV7 gives the tool rows a denied approval wrote before this
// version their display half. Those rows carry only the message written for
// the model — the instruction text, the user's feedback, and for a denied
// exit_plan_mode the full text of every plan review the refusal wrapped
// around it — so a replay printed that text as the card body. The display
// part states what the live card stated: the rule pkg/tool's
// DeniedToolDisplayBody names, frozen here the way a migration freezes every
// rule of its time. Rows that already carry a tool_display part keep theirs.
func migrateStateV6ToV7(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `
SELECT id, content, parts FROM fb_messages
WHERE role='tool' AND content LIKE 'Tool approval denied by user: the user rejected this %'`)
	if err != nil {
		return fmt.Errorf("select denied tool rows: %w", err)
	}
	type deniedRow struct {
		id      int64
		content string
		parts   string
	}
	var found []deniedRow
	for rows.Next() {
		var r deniedRow
		if err := rows.Scan(&r.id, &r.content, &r.parts); err != nil {
			rows.Close()
			return fmt.Errorf("scan denied tool row: %w", err)
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read denied tool rows: %w", err)
	}
	rows.Close()
	for _, r := range found {
		var parts []map[string]any
		if raw := strings.TrimSpace(r.parts); raw != "" && raw != "[]" {
			if err := json.Unmarshal([]byte(raw), &parts); err != nil {
				return fmt.Errorf("parse parts of denied tool row %d: %w", r.id, err)
			}
		}
		if part, ok := deniedToolDisplayPartV7(r.content); ok && !partsHaveToolDisplayV7(parts) {
			encoded, err := json.Marshal(append(parts, part))
			if err != nil {
				return fmt.Errorf("encode parts of denied tool row %d: %w", r.id, err)
			}
			if _, err := conn.ExecContext(ctx, `UPDATE fb_messages SET parts=? WHERE id=?`, string(encoded), r.id); err != nil {
				return fmt.Errorf("append denied display part to row %d: %w", r.id, err)
			}
		}
	}
	return nil
}

func partsHaveToolDisplayV7(parts []map[string]any) bool {
	for _, part := range parts {
		if v, _ := part["type"].(string); v == PartTypeToolDisplay {
			return true
		}
	}
	return false
}

// deniedToolDisplayPartV7 derives the display part one pre-v7 denial row
// carries, from the text the row holds. The tool name is the one the stored
// sentence names; the user's words are the text after the "User feedback:"
// marker, but only when what follows carries no review — a denial after a
// plan review wrapped the reviews around the user's own words, and the
// display half never shows those (decision D15, option B).
func deniedToolDisplayPartV7(content string) (map[string]any, bool) {
	const prefix = "Tool approval denied by user: the user rejected this "
	rest := strings.TrimPrefix(content, prefix)
	name := rest
	if idx := strings.Index(name, " tool call."); idx >= 0 {
		name = name[:idx]
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false
	}
	body := ""
	switch name {
	case "exit_plan_mode":
		body = deniedRowUserFeedbackV7(content)
		if strings.TrimSpace(body) == "" {
			body = "(no output)"
		}
	case "request_permissions":
		body = "The user did not approve the requested permissions"
	}
	return map[string]any{
		"type":           PartTypeToolDisplay,
		"body":           body,
		"tool_meta_json": fmt.Sprintf(`{"tool_name":%s,"status":"denied"}`, strconv.Quote(name)),
	}, true
}

func deniedRowUserFeedbackV7(content string) string {
	const marker = "\n\nUser feedback: "
	idx := strings.Index(content, marker)
	if idx < 0 {
		return ""
	}
	feedback := strings.TrimSpace(content[idx+len(marker):])
	if strings.Contains(feedback, "<review model=") {
		return ""
	}
	return feedback
}

// sendCallOfSubagentEventV8 is the one correlation that names the
// subagent_send call a legacy lifecycle event belongs to: the call's result
// run_id is the execution id of the subagent it started, so an event whose
// execution_id names that run answers that call. Used as a scalar it yields
// the call's step_id; its EXISTS form says whether there is one at all.
// Executions no send call answers — a goal check, a plan reviewer, a
// subagent the user started directly — match nothing and keep their empty
// parent_tool_call_id.
const sendCallOfSubagentEventV8 = `SELECT json_extract(s.payload_json, '$.step_id') FROM fb_session_events s
WHERE s.session_id = fb_session_events.session_id
  AND s.event_type = 'tool_call_completed'
  AND json_valid(s.payload_json)
  AND json_extract(s.payload_json, '$.tool_name') = 'subagent_send'
  AND json_valid(json_extract(s.payload_json, '$.output.output'))
  AND json_extract(json_extract(s.payload_json, '$.output.output'), '$.run_id')
      = json_extract(fb_session_events.payload_json, '$.execution_id')
LIMIT 1`

// migrateStateV7ToV8 backfills the dispatching call of the subagent
// lifecycle events subagent_send wrote before the dispatcher started
// carrying the call id over to the async execution (plan 012's A8, decision
// D10, option A). It changes data only — no table, no column — and leaves
// every other field of every touched row byte for byte as it was: only the
// empty parent_tool_call_id gains the step_id of the send call whose result
// named this execution. Events that already name a call keep theirs.
func migrateStateV7ToV8(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `
UPDATE fb_session_events
SET payload_json = json_set(payload_json, '$.parent_tool_call_id', (`+sendCallOfSubagentEventV8+`))
WHERE event_type IN ('subagent_spawned', 'subagent_ended')
  AND json_valid(payload_json)
  AND IFNULL(json_extract(payload_json, '$.parent_tool_call_id'), '') = ''
  AND EXISTS (`+sendCallOfSubagentEventV8+`)`); err != nil {
		return fmt.Errorf("backfill the send call of legacy subagent events: %w", err)
	}
	return nil
}

// migrateStateSchema brings the database behind db to stateSchemaVersion. The
// whole decision runs inside one IMMEDIATE transaction: taking the write lock
// before reading user_version means a second process that starts while an
// 800MB migration is in flight waits (its connection raised busy_timeout for
// exactly that) and then sees the version the first process committed, instead
// of racing it or failing after the pool's five seconds.
func migrateStateSchema(ctx context.Context, db *sql.DB) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := conn.Close(); err == nil {
			err = closeErr
		}
	}()
	if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout = 600000`); err != nil {
		return err
	}
	// The pool hands this connection to ordinary callers next; they wait as
	// long as the DSN promises, not as long as a migration may.
	defer func() {
		if _, pragmaErr := conn.ExecContext(context.Background(), fmt.Sprintf(`PRAGMA busy_timeout = %d`, stateBusyTimeoutMs)); err == nil && pragmaErr != nil {
			err = pragmaErr
		}
	}()
	// The setting is a no-op inside a transaction, so it belongs here, and the
	// migration rewrites tables wholesale instead of editing them row by row.
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer func() {
		if _, pragmaErr := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`); err == nil && pragmaErr != nil {
			err = pragmaErr
		}
	}()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	var version int
	if err := conn.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version == stateSchemaVersion {
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return err
		}
		committed = true
		return nil
	}
	if version > stateSchemaVersion {
		return ErrStateSchemaNewer
	}

	var fbTables int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE 'fb\_%' ESCAPE '\'`).
		Scan(&fbTables); err != nil {
		return err
	}
	if fbTables == 0 {
		// An empty file is a fresh database, not a migration.
		if _, err := conn.ExecContext(ctx, schemaSQL); err != nil {
			return fmt.Errorf("create state schema: %w", err)
		}
		if err := setSchemaVersion(ctx, conn, stateSchemaVersion); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return err
		}
		committed = true
		return nil
	}

	from := version
	for _, m := range stateSchemaMigrations {
		if m.version <= version {
			continue
		}
		if err := m.apply(ctx, conn); err != nil {
			return fmt.Errorf("migrate state database to v%d: %w", m.version, err)
		}
	}
	if err := requireNoForeignKeyViolations(ctx, conn); err != nil {
		return err
	}
	if err := setSchemaVersion(ctx, conn, stateSchemaVersion); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true

	// The rename-aside/copy/drop rebuild leaves the file full of free pages;
	// hand them back once nothing holds a transaction on this database. A step
	// that only added something freed nothing, and rewriting an 800MB file
	// for it would hold every other process off the database for no gain.
	var freePages int64
	if err := conn.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freePages); err != nil {
		return err
	}
	if freePages > 0 {
		if _, err := conn.ExecContext(ctx, `VACUUM`); err != nil {
			return fmt.Errorf("vacuum migrated state database: %w", err)
		}
	}
	slog.Info("migrated state database", "from_version", from, "to_version", stateSchemaVersion, "freed_pages", freePages)
	return nil
}

// requireCurrentSchemaVersion is the read-only gate: without the write lock a
// migration needs, an outdated database is an error rather than a best-effort
// partial read.
func requireCurrentSchemaVersion(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version != stateSchemaVersion {
		return ErrStateSchemaOutdated
	}
	return nil
}

func setSchemaVersion(ctx context.Context, conn *sql.Conn, version int) error {
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		return err
	}
	return nil
}

// requireNoForeignKeyViolations fails the migration when rows it produced
// reference parents that do not exist, naming each offending table.
func requireNoForeignKeyViolations(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var table string
		var rowid, parent sql.NullString
		var fkid int
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return err
		}
		counts[table]++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(counts) == 0 {
		return nil
	}
	var parts []string
	for table, n := range counts {
		parts = append(parts, fmt.Sprintf("%s=%d", table, n))
	}
	sort.Strings(parts)
	return fmt.Errorf("migrated database violates foreign keys: %s", strings.Join(parts, ", "))
}

// stateV1Copies carries each kept table forward, parents before the children
// that reference them. Each entry decides what its table keeps, rewrites and
// discards; a table the final schema declares but no entry names starts empty
// (fb_memory_index_files, see migrateStateV0ToV1).
var stateV1Copies = []struct {
	table string
	copy  func(ctx context.Context, conn *sql.Conn) (int64, error)
}{
	{"fb_projects", migrateV1Projects},
	{"fb_sessions", migrateV1Sessions},
	{"fb_runs", migrateV1Runs},
	{"fb_messages", migrateV1Messages},
	{"fb_session_ui_state", migrateV1SessionUIState},
	{"fb_session_prompt_state", migrateV1SessionPromptState},
	{"fb_session_events", migrateV1SessionEvents},
	{"fb_actions", migrateV1Actions},
	{"fb_run_waits", migrateV1RunWaits},
	{"fb_files", migrateV1Files},
	{"fb_cron_jobs", migrateV1CronJobs},
	{"fb_cron_runs", migrateV1CronRuns},
	{"fb_heartbeats", migrateV1Heartbeats},
	{"fb_memory_stage1_outputs", migrateV1MemoryStage1Outputs},
	{"fb_memory_jobs", migrateV1MemoryJobs},
	{"fb_session_model_state", migrateV1SessionModelState},
}

// migrateStateV0ToV1 carries the pre-versioning shape — schema.sql as released
// binaries declared it, plus whatever older tables a longer history left
// behind — to the versioned v1 shape. A v0 database is rebuilt rather than
// altered: every table is renamed aside, the final schema is created exactly
// as on an empty database, rows are copied table by table, and the renamed
// originals and any residual table are dropped. What a table's copy keeps,
// rewrites, or discards is decided by one function per table, so the
// database's contents and its declared shape move together.
func migrateStateV0ToV1(ctx context.Context, conn *sql.Conn) error {
	if err := dropAllTriggers(ctx, conn); err != nil {
		return err
	}
	if err := dropAllDeclaredIndexes(ctx, conn); err != nil {
		return err
	}
	if err := renameTablesToLegacy(ctx, conn); err != nil {
		return err
	}
	// The FTS index is derived from files on disk and is rebuilt by the next
	// search; its bookkeeping in fb_memory_index_files travels with it, which
	// is why that table is not on the copy list.
	if _, err := conn.ExecContext(ctx, `DROP TABLE IF EXISTS "fb_memory_fts"`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("create v1 schema: %w", err)
	}
	counts := map[string]string{}
	for _, c := range stateV1Copies {
		n, err := c.copy(ctx, conn)
		if err != nil {
			return fmt.Errorf("copy %s: %w", c.table, err)
		}
		counts[c.table] = fmt.Sprintf("copied=%d", n)
	}
	// Runs are timed once, after the rows that carry them exist.
	if err := migrateV1RunTiming(ctx, conn); err != nil {
		return err
	}
	if err := fixDanglingReferences(ctx, conn); err != nil {
		return err
	}
	dropped, err := dropTablesNotInFinalSchema(ctx, conn)
	if err != nil {
		return err
	}
	// The renamed originals of copied tables are cleanup, not data loss; only
	// tables no copy claimed are reported with the rows they take with them.
	copied := map[string]bool{}
	for _, c := range stateV1Copies {
		copied[c.table] = true
	}
	for _, residual := range dropped {
		if copied[residual.name] {
			continue
		}
		counts[residual.name] = fmt.Sprintf("dropped_rows=%d", residual.rows)
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	described := make([]string, len(keys))
	for i, k := range keys {
		described[i] = k + " " + counts[k]
	}
	slog.Info("state database migration tables", "counts", strings.Join(described, "; "))
	return nil
}

// migrateStateV1ToV2 repairs databases opened by an intermediate development
// build of the v1 migration. Those builds could stamp any intermediate schema
// as v1, causing later binaries to skip the rebuild even though some tables
// still had old columns. A database that matches the finished v1 shape — the
// declared schema without what later versions add — only needs its version
// advanced; every other v1 shape goes through the rebuild, whose copies read
// each table in whichever shape it is in.
func migrateStateV1ToV2(ctx context.Context, conn *sql.Conn) error {
	matches, err := stateSchemaMatchesDeclared(ctx, conn, stateObjectsAddedAfterV2)
	if err != nil {
		return err
	}
	if matches {
		return nil
	}
	return migrateStateV0ToV1(ctx, conn)
}

// migrateStateV2ToV3 adds the tables v3 declares. Each is created from the
// text schema.sql declares for it, so an upgraded file and a fresh one hold
// the same object byte for byte; a file the v0 or intermediate-v1 rebuild
// just produced already has them, having been built from schema.sql itself.
func migrateStateV2ToV3(ctx context.Context, conn *sql.Conn) error {
	declared, err := declaredSchemaObjects(ctx)
	if err != nil {
		return err
	}
	present, err := readSchemaObjects(ctx, conn)
	if err != nil {
		return err
	}
	have := make(map[string]bool, len(present))
	for _, object := range present {
		have[object.name] = true
	}
	// Tables first, then the indexes on them.
	for _, typ := range []string{"table", "index"} {
		for _, object := range declared {
			if object.typ != typ || !stateObjectsAddedAfterV2[object.table] || have[object.name] {
				continue
			}
			if _, err := conn.ExecContext(ctx, object.ddl); err != nil {
				return fmt.Errorf("create %s: %w", object.name, err)
			}
		}
	}
	return nil
}

func dropAllTriggers(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='trigger'`)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, name := range names {
		if _, err := conn.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+quoteIdent(name)); err != nil {
			return err
		}
	}
	return nil
}

func dropAllDeclaredIndexes(ctx context.Context, conn *sql.Conn) error {
	// Automatic indexes (sql IS NULL) belong to their table's constraints and
	// disappear with it; declared ones must go before the final schema
	// re-creates same-named indexes over the new tables.
	rows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='index' AND sql IS NOT NULL`)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, name := range names {
		if _, err := conn.ExecContext(ctx, `DROP INDEX IF EXISTS `+quoteIdent(name)); err != nil {
			return err
		}
	}
	return nil
}

// renameTablesToLegacy moves every ordinary fb_ table aside. The FTS virtual
// table cannot be renamed and its shadow tables belong to its module, so both
// are left for the explicit drop that follows.
func renameTablesToLegacy(ctx context.Context, conn *sql.Conn) error {
	tables, err := connOrdinaryTables(ctx, conn)
	if err != nil {
		return err
	}
	for _, name := range tables {
		if !strings.HasPrefix(name, "fb_") {
			continue
		}
		if _, err := conn.ExecContext(ctx,
			`ALTER TABLE `+quoteIdent(name)+` RENAME TO `+quoteIdent("legacy_"+name)); err != nil {
			return err
		}
	}
	return nil
}

// residualTable is one table the rebuild dropped and the rows it held.
type residualTable struct {
	name string
	rows int64
}

// dropTablesNotInFinalSchema removes the renamed originals and any residual
// table no declared schema claims anymore (fb_jobs on real machines), leaving
// the database holding exactly the objects a fresh one has. Each drop reports
// the rows it discarded.
func dropTablesNotInFinalSchema(ctx context.Context, conn *sql.Conn) ([]residualTable, error) {
	final, err := declaredSchemaObjectNames(ctx)
	if err != nil {
		return nil, err
	}
	tables, err := connOrdinaryTables(ctx, conn)
	if err != nil {
		return nil, err
	}
	var dropped []residualTable
	for _, name := range tables {
		if final[name] {
			continue
		}
		var rows int64
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdent(name)).Scan(&rows); err != nil {
			return nil, fmt.Errorf("count residual table %s: %w", name, err)
		}
		if _, err := conn.ExecContext(ctx, `DROP TABLE `+quoteIdent(name)); err != nil {
			return nil, fmt.Errorf("drop residual table %s: %w", name, err)
		}
		dropped = append(dropped, residualTable{name: strings.TrimPrefix(name, "legacy_"), rows: rows})
	}
	return dropped, nil
}

// legacyTableExists reports whether the renamed original of a table is present
// at all; a database older than the table copies nothing for it.
func legacyTableExists(ctx context.Context, conn *sql.Conn, table string) (bool, error) {
	var n int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func execCopy(ctx context.Context, conn *sql.Conn, query string) (int64, error) {
	if _, err := conn.ExecContext(ctx, query); err != nil {
		return 0, err
	}
	var copied int64
	if err := conn.QueryRowContext(ctx, `SELECT changes()`).Scan(&copied); err != nil {
		return 0, err
	}
	return copied, nil
}

// v1Expr resolves a legacy column expression against one legacy table.
type v1Expr struct {
	conn *sql.Conn
	err  error
}

func (e *v1Expr) get(table, column, fallback string) string {
	if e.err != nil {
		return ""
	}
	expr, err := legacyColumnExpr(context.Background(), e.conn, table, column, fallback)
	if err != nil {
		e.err = err
	}
	return expr
}

// pick reads a column of the final shape: the column itself when the legacy
// table already has it — an intermediate v1 build converted that table before
// stamping the file — and otherwise v0, the expression deriving it from the
// v0 shape. The rebuild meets both, table by table, and must carry each.
func (e *v1Expr) pick(table, column, v0 string) string {
	if e.err != nil {
		return ""
	}
	columns, err := connTableColumnNames(context.Background(), e.conn, table)
	if err != nil {
		e.err = err
		return ""
	}
	if columns[strings.ToLower(column)] {
		return quoteIdent(column)
	}
	return "(" + v0 + ")"
}

// migrateV1Projects maps archived_at onto NULL-for-live; a column a still
// older database lacks takes the default the fresh schema declares.
func migrateV1Projects(ctx context.Context, conn *sql.Conn) (int64, error) {
	if ok, err := legacyTableExists(ctx, conn, "legacy_fb_projects"); err != nil || !ok {
		return 0, err
	}
	e := &v1Expr{conn: conn}
	archived := e.get("legacy_fb_projects", "archived_at", "0")
	if e.err != nil {
		return 0, e.err
	}
	return execCopy(ctx, conn, fmt.Sprintf(`INSERT INTO fb_projects(
  id, agent_id, name, icon, description, instructions, root, project_key,
  memory_scope, resource_access, pinned, archived_at, created_at, updated_at)
SELECT
  id, agent_id, name,
  IFNULL(%s,''), IFNULL(%s,''), IFNULL(%s,''), root, IFNULL(%s,''),
  IFNULL(%s,'shared'), IFNULL(%s,1), IFNULL(%s,0), NULLIF(%s,0), created_at, updated_at
FROM legacy_fb_projects`,
		e.get("legacy_fb_projects", "icon", "NULL"),
		e.get("legacy_fb_projects", "description", "NULL"),
		e.get("legacy_fb_projects", "instructions", "NULL"),
		e.get("legacy_fb_projects", "project_key", "NULL"),
		e.get("legacy_fb_projects", "memory_scope", "NULL"),
		e.get("legacy_fb_projects", "resource_access", "NULL"),
		e.get("legacy_fb_projects", "pinned", "NULL"),
		archived))
}

// migrateV1Runs drops the runs of conversations that did not survive and maps
// the parent link onto NULL-for-none. Usage columns a still older database
// lacks take their zero defaults.
//
// A child run whose parent did not survive goes with it, and so does its own
// subtree: that is what the parent link's ON DELETE CASCADE means once the
// database enforces it, and the rows that hung off such a run — its messages,
// events and waits — are resolved against the surviving runs by the copies
// that follow.
func migrateV1Runs(ctx context.Context, conn *sql.Conn) (int64, error) {
	if ok, err := legacyTableExists(ctx, conn, "legacy_fb_runs"); err != nil || !ok {
		return 0, err
	}
	e := &v1Expr{conn: conn}
	expr := func(column, fallback string) string { return e.get("legacy_fb_runs", column, fallback) }
	parent := expr("parent_run_id", "''")
	cacheRead, cacheWrite, llmCalls := expr("usage_cache_read_tokens", "0"), expr("usage_cache_creation_tokens", "0"), expr("usage_llm_calls", "0")
	prompt, completion := expr("usage_prompt_tokens", "0"), expr("usage_completion_tokens", "0")
	// A run an intermediate build already timed keeps its clock; a v0 run has
	// none yet and is timed from its rows afterwards (migrateV1RunTiming).
	started, finished, worked := expr("started_at_ms", "NULL"), expr("finished_at_ms", "NULL"), expr("worked_ms", "NULL")
	if e.err != nil {
		return 0, e.err
	}
	copied, err := execCopy(ctx, conn, fmt.Sprintf(`INSERT INTO fb_runs(
  id, session_id, parent_run_id, input_text, status,
  usage_prompt_tokens, usage_completion_tokens, usage_cache_read_tokens,
  usage_cache_creation_tokens, usage_llm_calls, started_at_ms, finished_at_ms, worked_ms,
  created_at, updated_at)
SELECT id, session_id, NULLIF(%s,''), input_text, status,
  IFNULL(%s,0), IFNULL(%s,0), IFNULL(%s,0),
  IFNULL(%s,0), IFNULL(%s,0), %s, %s, %s, created_at, updated_at
FROM legacy_fb_runs
WHERE EXISTS(SELECT 1 FROM fb_sessions s WHERE s.id=legacy_fb_runs.session_id)`,
		parent, prompt, completion, cacheRead, cacheWrite, llmCalls, started, finished, worked))
	if err != nil {
		return 0, err
	}
	orphaned, err := execCopy(ctx, conn, `
DELETE FROM fb_runs WHERE id IN (
  WITH RECURSIVE orphaned(id) AS (
    SELECT c.id FROM fb_runs c
    WHERE c.parent_run_id IS NOT NULL
      AND NOT EXISTS(SELECT 1 FROM fb_runs p WHERE p.id=c.parent_run_id)
    UNION
    SELECT c.id FROM fb_runs c JOIN orphaned o ON c.parent_run_id=o.id
  )
  SELECT id FROM orphaned)`)
	if err != nil {
		return 0, err
	}
	if orphaned > 0 {
		slog.Info("state migration dropped runs whose parent run did not survive", "runs", orphaned)
	}
	return copied - orphaned, nil
}

// v1PlanCardTool reports the plan-card tool the event projection suppresses,
// copied here rather than imported: pkg/state must not depend on pkg/event,
// and the migration has to make the same call the projection does.
func v1PlanCardTool(toolName string) bool {
	return strings.TrimSpace(toolName) == "session_todo"
}

// v1JSONTrue reads a JSON flag lifted out of a payload by ->>: a JSON boolean
// arrives as "1", a value some writer stored as the string "true" keeps that
// spelling, and both mean the flag was set.
func v1JSONTrue(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "true", "1":
		return true
	default:
		return false
	}
}

// v1StepRendersAsPlan mirrors event.ToolStepRendersAsPlan: a session_todo
// call is represented by its plan card, so its tool steps carry no card.
func v1StepRendersAsPlan(toolName, kind, errText string) bool {
	if !v1PlanCardTool(toolName) {
		return false
	}
	switch kind {
	case "tool_call_started":
		return true
	case "tool_call_output_delta", "tool_call_completed":
		return strings.TrimSpace(errText) == ""
	}
	return false
}

// v1MigratedStepPayload is the SQL that reshapes one persisted run step into
// the canonical tool-event payload. `started` events carry no output, error or
// duration: nothing has run yet.
func v1MigratedStepPayload(eventType string) string {
	if eventType == "tool_call_started" {
		return `json_object(
  'kind', p->>'$.kind',
  'step_id', p->>'$.step_id',
  'tool_name', p->>'$.tool_name',
  'description', IFNULL(p->>'$.tool_description', ''),
  'action_id', IFNULL(p->>'$.action_id', ''),
  'action_kind', IFNULL(p->>'$.action_kind', ''),
  'tool_meta', json_object('tool_name', p->>'$.tool_name', 'input', json(IFNULL(p->'$.input', '{}')))
)`
	}
	return `json_object(
  'kind', p->>'$.kind',
  'step_id', p->>'$.step_id',
  'tool_name', p->>'$.tool_name',
  'description', IFNULL(p->>'$.tool_description', ''),
  'error', IFNULL(p->>'$.error', ''),
  'action_id', IFNULL(p->>'$.action_id', ''),
  'action_kind', IFNULL(p->>'$.action_kind', ''),
  'duration_seconds', IFNULL(p->>'$.duration', 0) / 1e9,
  'output', json(IFNULL(p->'$.output', '{}')),
  'tool_meta', json_object('tool_name', p->>'$.tool_name', 'input', json(IFNULL(p->'$.input', '{}')))
)`
}

// migrateV1SessionEvents carries the conversation event log forward and folds
// the run-step ledger into it: the two tables become one ordered record where
// every fact is written once.
//
// Every event v0 recorded is kept, its run link resolved onto NULL-for-none.
// Of the steps, only the ones no event already stands for are migrated: a main
// agent's tool call (never a suppressed, plan-card or permission-request one)
// and a main agent's plan update. Everything else a step held — subagent
// lifecycle, goal, turn bounds — already has its canonical event, or had no
// reader. The two streams are merged by time so the migrated steps land where
// the conversation actually drew them, and the events' own order is preserved
// because sequence is the cursor surfaces page by and may not be re-sorted.
func migrateV1SessionEvents(ctx context.Context, conn *sql.Conn) (int64, error) {
	if ok, err := legacyTableExists(ctx, conn, "legacy_fb_session_events"); err != nil || !ok {
		return 0, err
	}

	// Cursor A: the events v0 already recorded, in their authoritative order.
	type queued struct {
		sessionID string
		runID     any
		eventID   string
		eventType string
		payload   string
		occurred  int64
	}
	var a []queued
	// seen keys each tool event by its conversation, type and step id: a step
	// id is the provider's call id, which is only unique within the
	// conversation that made the call.
	seen := map[string]bool{}
	seenKey := func(sessionID, eventType, stepID string) string {
		return sessionID + "\x00" + eventType + "\x00" + stepID
	}
	rows, err := conn.QueryContext(ctx, `
SELECT e.session_id,
  CASE WHEN EXISTS(SELECT 1 FROM fb_runs r WHERE r.id=NULLIF(e.run_id,'')) THEN NULLIF(e.run_id,'') END,
  e.event_id, e.event_type, e.payload_json, e.occurred_at_ms,
  IFNULL(e.payload_json->>'$.step_id','')
FROM legacy_fb_session_events e
WHERE EXISTS(SELECT 1 FROM fb_sessions s WHERE s.id=e.session_id)
ORDER BY e.sequence`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var q queued
		var stepID string
		if err := rows.Scan(&q.sessionID, &q.runID, &q.eventID, &q.eventType, &q.payload, &q.occurred, &stepID); err != nil {
			rows.Close()
			return 0, err
		}
		a = append(a, q)
		if id := strings.TrimSpace(stepID); id != "" {
			seen[seenKey(q.sessionID, q.eventType, id)] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Cursor B: the steps that still need an event, in the order they happened.
	// id is the table's rowid alias: the payload projection later in this
	// function looks each row up by it, and the migration has already dropped
	// every declared index, so (run_id, seq) is a full scan while id is a
	// rowid seek.
	type step struct {
		id        int64
		runID     string
		sessionID string
		seq       int64
		eventType string
		toolName  string
		errText   string
		hasPlan   bool
		planAgent string
		occurred  int64
	}
	var b []step
	// An intermediate build that already folded the ledger in has no step
	// table left; only its events are carried.
	hasSteps, err := legacyTableExists(ctx, conn, "legacy_fb_run_steps")
	if err != nil {
		return 0, err
	}
	if !hasSteps {
		for _, q := range a {
			if _, err := conn.ExecContext(ctx, `
INSERT INTO fb_session_events(session_id, run_id, event_id, event_type, payload_json, occurred_at_ms)
VALUES(?,?,?,?,?,?)`, q.sessionID, q.runID, q.eventID, q.eventType, q.payload, q.occurred); err != nil {
				return 0, err
			}
		}
		slog.Info("state migration session events", "events_kept", len(a), "steps_migrated", 0)
		return int64(len(a)), nil
	}
	stepRows, err := conn.QueryContext(ctx, `
SELECT s.id, s.run_id, r.session_id, s.seq, s.event_type, s.created_at,
  IFNULL(s.payload_json->>'$.tool_name',''),
  IFNULL(s.payload_json->>'$.error',''),
  IFNULL(s.payload_json->>'$.agent_id',''),
  IFNULL(s.payload_json->>'$.suppress_ui',''),
  IFNULL(s.payload_json->>'$.plan_update.agent_id',''),
  CASE WHEN json_type(s.payload_json, '$.plan_update') IS NULL THEN 0 ELSE 1 END,
  IFNULL(s.payload_json->>'$.step_id','')
FROM legacy_fb_run_steps s
JOIN fb_runs r ON r.id = s.run_id
WHERE s.event_type IN ('tool_call_started','tool_call_completed','plan_updated')
  AND r.parent_run_id IS NULL
ORDER BY s.created_at ASC, s.run_id ASC, s.seq ASC`)
	if err != nil {
		return 0, err
	}
	for stepRows.Next() {
		var st step
		var createdAt int64
		var agentID, suppressUI, stepID string
		var hasPlan int
		if err := stepRows.Scan(&st.id, &st.runID, &st.sessionID, &st.seq, &st.eventType, &createdAt,
			&st.toolName, &st.errText, &agentID, &suppressUI, &st.planAgent, &hasPlan, &stepID); err != nil {
			stepRows.Close()
			return 0, err
		}
		st.occurred = createdAt * 1000
		switch st.eventType {
		case "plan_updated":
			// A plan update only reaches the conversation when it belongs to
			// the primary agent; a subagent's is published as its own event.
			if hasPlan == 0 || strings.TrimSpace(st.planAgent) != "" {
				continue
			}
		default:
			// A tool step is migrated only when the primary agent raised it,
			// the UI showed it, and no event already stands for it.
			if strings.TrimSpace(agentID) != "" || v1JSONTrue(suppressUI) {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(st.toolName), "request_permissions") {
				continue
			}
			if v1StepRendersAsPlan(st.toolName, st.eventType, st.errText) {
				continue
			}
		}
		if id := strings.TrimSpace(stepID); id != "" && seen[seenKey(st.sessionID, st.eventType, id)] {
			continue
		}
		b = append(b, st)
	}
	stepRows.Close()
	if err := stepRows.Err(); err != nil {
		return 0, err
	}

	// Merge: A's order is authoritative; a step goes first only when it
	// happened strictly before A's next event.
	migrated, kept := 0, 0
	insertOld := func(q queued) error {
		_, err := conn.ExecContext(ctx, `
INSERT INTO fb_session_events(session_id, run_id, event_id, event_type, payload_json, occurred_at_ms)
VALUES(?,?,?,?,?,?)`, q.sessionID, q.runID, q.eventID, q.eventType, q.payload, q.occurred)
		return err
	}
	insertStep := func(st step) error {
		payloadExpr := v1MigratedStepPayload(st.eventType)
		if st.eventType == "plan_updated" {
			payloadExpr = `p->'$.plan_update'`
		}
		// `p` is the step's old payload_json, the name the payload templates
		// above are written against, so the row is projected to that name. The
		// lookup is by rowid: the index the v0 table had on (run_id, seq) is
		// gone by now, and a rowid seek stays O(log n) without it.
		if _, err := conn.ExecContext(ctx, `
INSERT INTO fb_session_events(session_id, run_id, event_id, event_type, payload_json, occurred_at_ms)
SELECT ?, ?, ?, ?, `+payloadExpr+`, ?
FROM (SELECT payload_json AS p FROM legacy_fb_run_steps WHERE id=?)`,
			st.sessionID, st.runID, fmt.Sprintf("evt-%s-%d", st.runID, st.seq), st.eventType, st.occurred, st.id); err != nil {
			return err
		}
		return nil
	}
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		if j < len(b) && (i >= len(a) || b[j].occurred < a[i].occurred) {
			if err := insertStep(b[j]); err != nil {
				return 0, err
			}
			migrated++
			j++
			continue
		}
		if err := insertOld(a[i]); err != nil {
			return 0, err
		}
		kept++
		i++
	}
	slog.Info("state migration session events", "events_kept", kept, "steps_migrated", migrated)
	return int64(kept + migrated), nil
}

// migrateV1Actions derives each action's owning conversation — the session_id
// its payload recorded, or the run a wait row ties it to — and drops the
// actions neither can place: they belong to no conversation any surface could
// list them under.
func migrateV1Actions(ctx context.Context, conn *sql.Conn) (int64, error) {
	if ok, err := legacyTableExists(ctx, conn, "legacy_fb_actions"); err != nil || !ok {
		return 0, err
	}
	e := &v1Expr{conn: conn}
	answer, errCol := e.get("legacy_fb_actions", "answer_json", "NULL"), e.get("legacy_fb_actions", "error", "NULL")
	// An intermediate build already recorded the owning session as a column
	// (and stopped copying it into the payload); a v0 action carries it in its
	// payload, or reaches it through the wait that blocks on it.
	column := e.get("legacy_fb_actions", "session_id", "NULL")
	if e.err != nil {
		return 0, e.err
	}
	session := fmt.Sprintf(`COALESCE(NULLIF(%s,''), NULLIF(payload_json->>'$.session_id',''),
    (SELECT NULLIF(r.session_id,'') FROM legacy_fb_run_waits w
      JOIN legacy_fb_runs r ON r.id=w.run_id
      WHERE w.action_id=legacy_fb_actions.id AND NULLIF(r.session_id,'') IS NOT NULL
        AND EXISTS(SELECT 1 FROM fb_sessions s WHERE s.id=r.session_id)))`, column)
	return execCopy(ctx, conn, fmt.Sprintf(`INSERT INTO fb_actions(
  id, session_id, kind, status, payload_json, answer_json, error, created_at, updated_at)
SELECT id, %s,
  kind, status, payload_json, IFNULL(%s,''), IFNULL(%s,''), created_at, updated_at
FROM legacy_fb_actions
WHERE EXISTS(SELECT 1 FROM fb_sessions s WHERE s.id = %s)`,
		session, answer, errCol, session))
}

// migrateV1RunWaits keeps one wait per run — the row GetWaitForRun would have
// picked (the long-run async switch first, otherwise the newest) — and splits
// the source JSON blob into its typed columns. Waits whose run or action did
// not survive are dropped with it.
func migrateV1RunWaits(ctx context.Context, conn *sql.Conn) (int64, error) {
	if ok, err := legacyTableExists(ctx, conn, "legacy_fb_run_waits"); err != nil || !ok {
		return 0, err
	}
	e := &v1Expr{conn: conn}
	snapshot := e.get("legacy_fb_run_waits", "session_snapshot_json", "NULL")
	// A v0 source blob could be '' (never set); the JSON operators error on
	// that, so it reads as the empty object it always meant.
	source := `COALESCE(NULLIF(` + e.get("legacy_fb_run_waits", "source_json", "NULL") + `,''),'{}')`
	// A wait an intermediate build already split keeps its typed columns — the
	// resume fence above all, which must never be forgotten on a continuation
	// that crossed it; a v0 wait reads them from its blob.
	field := func(column, v0 string) string { return e.pick("legacy_fb_run_waits", column, v0) }
	agent := field("agent_id", source+`->>'$.agent_id'`)
	subtype := field("subagent_type", source+`->>'$.subagent_type'`)
	subRun := field("subagent_run_id", source+`->>'$.subagent_run_id'`)
	sandbox := field("sandbox_profile", source+`->>'$.sandbox_profile'`)
	requested := field("requested_profile", source+`->>'$.requested_profile'`)
	elevation := field("profile_elevation", `CAST(`+source+`->>'$.profile_elevation' AS INTEGER)`)
	owner := field("resume_owner", source+`->>'$.resume_owner'`)
	claimed := field("resume_claimed_at_ms", `CAST(`+source+`->>'$.resume_claimed_at' AS INTEGER)`)
	phase := field("resume_phase", source+`->>'$.resume_phase'`)
	if e.err != nil {
		return 0, e.err
	}
	return execCopy(ctx, conn, fmt.Sprintf(`INSERT INTO fb_run_waits(
  run_id, action_id, tool_name, tool_input_json, session_snapshot_json,
  agent_id, subagent_type, subagent_run_id, sandbox_profile, requested_profile,
  profile_elevation, resume_owner, resume_claimed_at_ms, resume_phase,
  created_at, updated_at)
SELECT run_id, action_id, tool_name, tool_input_json, IFNULL(%s,''),
  IFNULL(%s,''),
  IFNULL(%s,''),
  CASE WHEN IFNULL(%s,'')='' THEN NULL
       WHEN EXISTS(SELECT 1 FROM fb_runs r WHERE r.id=%s)
       THEN %s ELSE NULL END,
  IFNULL(%s,''),
  IFNULL(%s,''),
  IFNULL(%s,0),
  IFNULL(%s,''),
  NULLIF(%s,0),
  IFNULL(%s,''),
  created_at, updated_at
FROM (
  SELECT *, ROW_NUMBER() OVER (
    PARTITION BY run_id
    ORDER BY CASE WHEN tool_name='long_run_async_switch' THEN 0 ELSE 1 END, updated_at DESC
  ) AS pick
  FROM legacy_fb_run_waits
  WHERE EXISTS(SELECT 1 FROM fb_runs r WHERE r.id=legacy_fb_run_waits.run_id)
    AND EXISTS(SELECT 1 FROM fb_actions a WHERE a.id=legacy_fb_run_waits.action_id)
)
WHERE pick=1`,
		snapshot, agent, subtype, subRun, subRun, subRun, sandbox, requested, elevation, owner, claimed, phase))
}

// migrateV1Files flattens the two mutually-exclusive location columns
// (storage_relpath for local, oss_key for s3) into one storage_key, renames the
// rest to their current names, and drops the rows no reader can reach: files
// whose session did not survive, and the workspace_file rows the schema
// reserved for a writer that never existed.
func migrateV1Files(ctx context.Context, conn *sql.Conn) (int64, error) {
	if ok, err := legacyTableExists(ctx, conn, "legacy_fb_files"); err != nil || !ok {
		return 0, err
	}
	e := &v1Expr{conn: conn}
	expr := func(column, fallback string) string { return e.get("legacy_fb_files", column, fallback) }
	source, relpath := expr("source", "'attachment'"), expr("storage_relpath", "''")
	ossBucket, ossKey := expr("oss_bucket", "''"), expr("oss_key", "''")
	parsedPath, parseError := expr("parsed_text_relpath", "''"), expr("error", "''")
	// A table an intermediate build already flattened keeps its own columns.
	bucket := e.pick("legacy_fb_files", "storage_bucket", ossBucket)
	key := e.pick("legacy_fb_files", "storage_key", fmt.Sprintf(`CASE storage_backend WHEN 's3' THEN %s ELSE %s END`, ossKey, relpath))
	textPath := e.pick("legacy_fb_files", "parsed_text_path", parsedPath)
	failure := e.pick("legacy_fb_files", "parse_error", parseError)
	if e.err != nil {
		return 0, e.err
	}
	return execCopy(ctx, conn, fmt.Sprintf(`INSERT INTO fb_files(
  id, session_id, original_name, media_type, size_bytes, sha256,
  storage_backend, storage_bucket, storage_key,
  parse_status, parsed_text_path, parse_error, created_at, updated_at)
SELECT id, session_id, original_name, media_type, size_bytes, sha256,
  storage_backend, IFNULL(%s,''), %s,
  parse_status, IFNULL(%s,''), IFNULL(%s,''), created_at, updated_at
FROM legacy_fb_files
WHERE IFNULL(%s,'attachment')='attachment'
  AND EXISTS(SELECT 1 FROM fb_sessions s WHERE s.id=legacy_fb_files.session_id)`,
		bucket, key, textPath, failure, source))
}

// migrateV1CronJobs maps the project link onto NULL-for-none (dropping one that
// points at a project that did not survive) and NULL onto "no last run".
func migrateV1CronJobs(ctx context.Context, conn *sql.Conn) (int64, error) {
	if ok, err := legacyTableExists(ctx, conn, "legacy_fb_cron_jobs"); err != nil || !ok {
		return 0, err
	}
	e := &v1Expr{conn: conn}
	expr := func(column, fallback string) string { return e.get("legacy_fb_cron_jobs", column, fallback) }
	project, lastRun := expr("project_id", "''"), expr("last_run_at", "0")
	name, prompt := expr("name", "NULL"), expr("prompt", "NULL")
	deliver, lastStatus := expr("deliver", "NULL"), expr("last_status", "NULL")
	lastError, lastOutput := expr("last_error", "NULL"), expr("last_output", "NULL")
	enabled, repeat, runCount := expr("enabled", "1"), expr("repeat_limit", "0"), expr("run_count", "0")
	streak := expr("failure_streak", "0")
	if e.err != nil {
		return 0, e.err
	}
	return execCopy(ctx, conn, fmt.Sprintf(`INSERT INTO fb_cron_jobs(
  id, agent_id, project_id, name, schedule, prompt, deliver, enabled, repeat_limit,
  run_count, next_run_at, last_run_at, last_status, last_error, last_output, failure_streak,
  created_at, updated_at)
SELECT id, agent_id,
  CASE WHEN NULLIF(%s,'') IS NULL THEN NULL
       WHEN EXISTS(SELECT 1 FROM fb_projects p WHERE p.id=legacy_fb_cron_jobs.project_id) THEN %s
       ELSE NULL END,
  IFNULL(%s,''), schedule, IFNULL(%s,''), IFNULL(%s,''), IFNULL(%s,1), IFNULL(%s,0),
  IFNULL(%s,0), next_run_at, NULLIF(%s,0), IFNULL(%s,''), IFNULL(%s,''), IFNULL(%s,''), IFNULL(%s,0),
  created_at, updated_at
FROM legacy_fb_cron_jobs`,
		project, project, name, prompt, deliver, enabled, repeat, runCount,
		lastRun, lastStatus, lastError, lastOutput, streak))
}

// migrateV1CronRuns drops the always-empty run_id, maps finished_at onto
// NULL-for-incomplete, and fills an agent id the fire never recorded from the
// job it belongs to. A fire whose tenant cannot be resolved is dropped: no
// surface could show it to anyone. A fire keeps its id — the history API names
// fires by it — and the table keeps its AUTOINCREMENT high-water mark.
func migrateV1CronRuns(ctx context.Context, conn *sql.Conn) (int64, error) {
	if ok, err := legacyTableExists(ctx, conn, "legacy_fb_cron_runs"); err != nil || !ok {
		return 0, err
	}
	e := &v1Expr{conn: conn}
	expr := func(column, fallback string) string { return e.get("legacy_fb_cron_runs", column, fallback) }
	agent, sessionID := expr("agent_id", "''"), expr("session_id", "''")
	trigger, status := expr("trigger", "'schedule'"), expr("status", "'running'")
	output, errCol := expr("output", "NULL"), expr("error", "NULL")
	delivered, finished := expr("delivered_to", "NULL"), expr("finished_at", "0")
	if e.err != nil {
		return 0, e.err
	}
	resolved := `COALESCE(NULLIF(TRIM(` + agent + `),''),
    (SELECT NULLIF(TRIM(j.agent_id),'') FROM legacy_fb_cron_jobs j WHERE j.id=legacy_fb_cron_runs.job_id))`
	copied, err := execCopy(ctx, conn, fmt.Sprintf(`INSERT INTO fb_cron_runs(
  id, job_id, agent_id, session_id, trigger, status, output, error, delivered_to, started_at, finished_at)
SELECT id, job_id, %s,
  IFNULL(%s,''), IFNULL(%s,'schedule'), IFNULL(%s,'running'),
  IFNULL(%s,''), IFNULL(%s,''), IFNULL(%s,''), started_at, NULLIF(%s,0)
FROM legacy_fb_cron_runs
WHERE NULLIF(%s,'') IS NOT NULL`,
		resolved, sessionID, trigger, status, output, errCol, delivered, finished, resolved))
	if err != nil {
		return 0, err
	}
	if err := carryAutoincrement(ctx, conn, "fb_cron_runs"); err != nil {
		return 0, err
	}
	return copied, nil
}

// migrateV1Heartbeats collapses the paused flag into a NULL next_run_at — the
// one representation the scheduler reads — maps a zero fired-at onto NULL for
// "never fired", and drops the redundant agent column (the session names the
// tenant). A heartbeat of a session that did not survive is dropped.
func migrateV1Heartbeats(ctx context.Context, conn *sql.Conn) (int64, error) {
	if ok, err := legacyTableExists(ctx, conn, "legacy_fb_heartbeats"); err != nil || !ok {
		return 0, err
	}
	e := &v1Expr{conn: conn}
	expr := func(column, fallback string) string { return e.get("legacy_fb_heartbeats", column, fallback) }
	paused, nextRun := expr("paused", "0"), expr("next_run_at", "0")
	lastFired := expr("last_fired_at", "0")
	if e.err != nil {
		return 0, e.err
	}
	return execCopy(ctx, conn, fmt.Sprintf(`INSERT INTO fb_heartbeats(
  session_id, interval_seconds, prompt, next_run_at, last_fired_at, created_at, updated_at)
SELECT session_id, interval_seconds, prompt,
  CASE WHEN IFNULL(%s,0)<>0 OR IFNULL(%s,0)<=0 THEN NULL ELSE %s END,
  NULLIF(%s,0), created_at, updated_at
FROM legacy_fb_heartbeats
WHERE EXISTS(SELECT 1 FROM fb_sessions s WHERE s.id=legacy_fb_heartbeats.session_id)`,
		paused, nextRun, nextRun, lastFired))
}

// migrateV1MemoryStage1Outputs drops the outputs of threads that did not
// survive — the composite key they hang off requires both the thread and its
// agent to match — fills the now-NOT NULL usage_count with its zero, and
// collapses the phase-2 flag to 0/1.
func migrateV1MemoryStage1Outputs(ctx context.Context, conn *sql.Conn) (int64, error) {
	if ok, err := legacyTableExists(ctx, conn, "legacy_fb_memory_stage1_outputs"); err != nil || !ok {
		return 0, err
	}
	e := &v1Expr{conn: conn}
	expr := func(column, fallback string) string {
		return e.get("legacy_fb_memory_stage1_outputs", column, fallback)
	}
	usage, selected := expr("usage_count", "0"), expr("selected_for_phase2", "0")
	if e.err != nil {
		return 0, e.err
	}
	return execCopy(ctx, conn, fmt.Sprintf(`INSERT INTO fb_memory_stage1_outputs(
  thread_id, agent_id, project_key, source_updated_at, raw_memory, rollout_summary, rollout_slug,
  generated_at, usage_count, last_usage, selected_for_phase2, selected_for_phase2_source_updated_at)
SELECT thread_id, agent_id, project_key, source_updated_at, raw_memory, rollout_summary, rollout_slug,
  generated_at, IFNULL(%s,0), last_usage,
  CASE WHEN IFNULL(%s,0)<>0 THEN 1 ELSE 0 END, selected_for_phase2_source_updated_at
FROM legacy_fb_memory_stage1_outputs
WHERE EXISTS(SELECT 1 FROM fb_sessions s
  WHERE s.id=legacy_fb_memory_stage1_outputs.thread_id
    AND s.agent_id=legacy_fb_memory_stage1_outputs.agent_id)`,
		usage, selected))
}

// migrateV1MemoryJobs copies the job ledger minus the worker id it no longer
// stores; the fencing it once claimed is the ownership_token's job.
func migrateV1MemoryJobs(ctx context.Context, conn *sql.Conn) (int64, error) {
	if ok, err := legacyTableExists(ctx, conn, "legacy_fb_memory_jobs"); err != nil || !ok {
		return 0, err
	}
	e := &v1Expr{conn: conn}
	expr := func(column, fallback string) string { return e.get("legacy_fb_memory_jobs", column, fallback) }
	ownership, status := expr("ownership_token", "NULL"), expr("status", "NULL")
	started, finished := expr("started_at", "NULL"), expr("finished_at", "NULL")
	lease, retryAt := expr("lease_until", "NULL"), expr("retry_at", "NULL")
	retryRemaining, lastError := expr("retry_remaining", "0"), expr("last_error", "NULL")
	inputWatermark, lastSuccess := expr("input_watermark", "NULL"), expr("last_success_watermark", "NULL")
	if e.err != nil {
		return 0, e.err
	}
	return execCopy(ctx, conn, fmt.Sprintf(`INSERT INTO fb_memory_jobs(
  kind, job_key, agent_id, status, ownership_token, started_at, finished_at, lease_until,
  retry_at, retry_remaining, last_error, input_watermark, last_success_watermark)
SELECT kind, job_key, agent_id, IFNULL(%s,''), %s, %s, %s, %s, %s, IFNULL(%s,0), %s, %s, %s
FROM legacy_fb_memory_jobs`,
		status, ownership, started, finished, lease, retryAt, retryRemaining, lastError, inputWatermark, lastSuccess))
}

// migrateV1Sessions rewrites the session row set: title becomes NOT NULL (an
// empty or NULL title names the session by its id), ”/0 sentinels become
// NULL, origin collapses to native/migrated, and the row-id pointers become
// integers. created_at has a CHECK to uphold, so a legacy zero is rederived
// from the session's oldest message and, failing that, from updated_at.
func migrateV1Sessions(ctx context.Context, conn *sql.Conn) (int64, error) {
	if ok, err := legacyTableExists(ctx, conn, "legacy_fb_sessions"); err != nil || !ok {
		return 0, err
	}
	e := &v1Expr{conn: conn}
	expr := func(column, fallback string) string { return e.get("legacy_fb_sessions", column, fallback) }
	title, parent, project := expr("title", "NULL"), expr("parent_session_id", "''"), expr("project_id", "''")
	origin, cwd, branch := expr("origin", "''"), expr("cwd", "''"), expr("git_branch", "''")
	mode, source, window := expr("memory_mode", "'disabled'"), expr("memory_source", "''"), expr("initial_window_id", "''")
	// The row pointers: an intermediate build already stored them as integer
	// ids with NULL for none; v0 kept a TEXT id and a 0.
	compact := e.pick("legacy_fb_sessions", "compact_boundary_message_id",
		`CAST(NULLIF(`+expr("compact_boundary_id", "''")+`,'') AS INTEGER)`)
	reset := e.pick("legacy_fb_sessions", "context_reset_message_id",
		`NULLIF(`+expr("context_reset_row_id", "0")+`,0)`)
	if e.err != nil {
		return 0, e.err
	}
	return execCopy(ctx, conn, fmt.Sprintf(`INSERT INTO fb_sessions(
  id, agent_id, title, parent_session_id, project_id, origin, cwd, git_branch,
  memory_mode, memory_source, initial_window_id, compact_boundary_message_id,
  context_reset_message_id, created_at, updated_at)
SELECT id, agent_id, title, parent, project, origin, cwd, branch, mode, source, window, compact, reset, created,
  MAX(updated_at, created)
FROM (
  SELECT l.id AS id, l.agent_id AS agent_id,
    CASE WHEN TRIM(IFNULL(%s,''))='' THEN l.id ELSE %s END AS title,
    NULLIF(%s,'') AS parent,
    NULLIF(%s,'') AS project,
    CASE WHEN %s='migrated' THEN 'migrated' ELSE 'native' END AS origin,
    %s AS cwd, %s AS branch, %s AS mode, %s AS source, %s AS window,
    %s AS compact,
    %s AS reset,
    CASE WHEN l.created_at>0 THEN l.created_at
      ELSE COALESCE((SELECT MIN(m.created_at) FROM legacy_fb_messages m WHERE m.session_id=l.id), l.updated_at) END AS created,
    l.updated_at AS updated_at
  FROM legacy_fb_sessions l
  WHERE TRIM(IFNULL(l.agent_id,''))<>''
)`,
		title, title, parent, project, origin, cwd, branch, mode, source, window, compact, reset))
}

// legacyMessageTiming is the per-row timing a v0 fb_messages row carried: the
// run it named and the window a surface stamped on it. Each expression is the
// legacy column when the table has it and "none" when a still older database
// predates it, so a file without timing simply has none to move.
type legacyMessageTiming struct {
	runID       string
	startedRaw  string
	finishedRaw string
	worked      string
	// startedMs and finishedMs are the TEXT timestamps as milliseconds since
	// the epoch, NULL for an empty value. SQLite parses a timestamp to whole
	// milliseconds; ROUND recovers exactly that value from the floating-point
	// Julian day, where CAST would truncate a hair under it to the one before.
	startedMs  string
	finishedMs string
	// timed says the table has the window columns at all; a file older than
	// them has no timing to move.
	timed bool
}

func resolveLegacyMessageTiming(ctx context.Context, conn *sql.Conn) (legacyMessageTiming, error) {
	columns, err := connTableColumnNames(ctx, conn, "legacy_fb_messages")
	if err != nil {
		return legacyMessageTiming{}, err
	}
	e := &v1Expr{conn: conn}
	t := legacyMessageTiming{
		runID:       e.get("legacy_fb_messages", "run_id", "''"),
		startedRaw:  e.get("legacy_fb_messages", "run_started_at", "''"),
		finishedRaw: e.get("legacy_fb_messages", "run_finished_at", "''"),
		worked:      e.get("legacy_fb_messages", "worked_duration_ms", "0"),
	}
	if e.err != nil {
		return legacyMessageTiming{}, e.err
	}
	ms := func(column string) string {
		return fmt.Sprintf(`CASE WHEN IFNULL(%s,'')='' THEN NULL ELSE CAST(ROUND((julianday(%s) - 2440587.5) * 86400000) AS INTEGER) END`, column, column)
	}
	t.startedMs, t.finishedMs = ms(t.startedRaw), ms(t.finishedRaw)
	t.timed = columns["run_started_at"] && columns["run_finished_at"]
	return t, nil
}

// windowed is the predicate of a row that carries a complete window.
func (t legacyMessageTiming) windowed() string {
	return fmt.Sprintf(`(IFNULL(%s,'')<>'' AND IFNULL(%s,'')<>'')`, t.startedRaw, t.finishedRaw)
}

// migrateV1Messages renames source to visibility with its value set mapped,
// drops rows no reader could ever reach (a session that does not exist, a role
// outside the enum, a legacy 'tui' source), and moves the legacy per-row
// timing to where it belongs.
//
// The same three legacy columns held two different facts. On a tool row, and
// on the user row of a shell command the user ran (the one user row a writer
// gives tool metadata), they were that row's own execution window; those
// become exec_* milliseconds. On every other row they were a copy of the run's
// window, stamped on each row of the turn; the run keeps it (see
// migrateV1RunTiming) and the row keeps nothing, so an ordinary user message
// never reads as a command with its own duration.
//
// Message ids are copied verbatim, and the table's AUTOINCREMENT high-water
// mark with them: they are the cursor every surface's scroll position and the
// compact/reset pointers are expressed in.
func migrateV1Messages(ctx context.Context, conn *sql.Conn) (int64, error) {
	if ok, err := legacyTableExists(ctx, conn, "legacy_fb_messages"); err != nil || !ok {
		return 0, err
	}
	e := &v1Expr{conn: conn}
	expr := func(column, fallback string) string { return e.get("legacy_fb_messages", column, fallback) }
	source, model := expr("source", "'transcript'"), expr("model", "NULL")
	parts, messageID := expr("parts", "NULL"), expr("message_id", "NULL")
	usage, toolStep := expr("usage_json", "NULL"), expr("tool_step_id", "NULL")
	toolMeta := expr("tool_meta_json", "NULL")
	if e.err != nil {
		return 0, e.err
	}
	timing, err := resolveLegacyMessageTiming(ctx, conn)
	if err != nil {
		return 0, err
	}
	// A non-empty timestamp that converts to NULL is a format this migration
	// does not understand; carrying on would read it as "never timed".
	var badCount int
	if err := conn.QueryRowContext(ctx, fmt.Sprintf(`
SELECT COUNT(*) FROM legacy_fb_messages m
WHERE EXISTS(SELECT 1 FROM fb_sessions s WHERE s.id=m.session_id)
  AND ((IFNULL(%s,'')<>'' AND %s IS NULL) OR (IFNULL(%s,'')<>'' AND %s IS NULL))`,
		timing.startedRaw, timing.startedMs, timing.finishedRaw, timing.finishedMs)).Scan(&badCount); err != nil {
		return 0, err
	}
	if badCount > 0 {
		return 0, fmt.Errorf("%d legacy message rows carry a run timestamp this migration cannot parse", badCount)
	}
	execTimed := fmt.Sprintf(`(m.role='tool' OR (m.role='user' AND IFNULL(%s,'')<>'')) AND %s`, toolMeta, timing.windowed())
	// A table an intermediate build already converted keeps its own
	// visibility and execution window; a v0 table derives them from its source
	// label and the per-row timing.
	visibility := e.pick("legacy_fb_messages", "visibility",
		fmt.Sprintf(`CASE %s WHEN 'transcript' THEN 'visible' WHEN 'transcript_repaired' THEN 'repaired' WHEN 'transcript_withdrawn' THEN 'withdrawn' END`, source))
	execStart := e.pick("legacy_fb_messages", "exec_started_at_ms", fmt.Sprintf(`CASE WHEN %s THEN %s END`, execTimed, timing.startedMs))
	execFinish := e.pick("legacy_fb_messages", "exec_finished_at_ms", fmt.Sprintf(`CASE WHEN %s THEN %s END`, execTimed, timing.finishedMs))
	execDuration := e.pick("legacy_fb_messages", "exec_duration_ms", fmt.Sprintf(`CASE WHEN %s THEN IFNULL(%s,0) END`, execTimed, timing.worked))
	if e.err != nil {
		return 0, e.err
	}
	rows, err := execCopy(ctx, conn, fmt.Sprintf(`INSERT INTO fb_messages(
  id, session_id, run_id, role, visibility, content, parts, message_id, model,
  usage_json, tool_step_id, tool_meta_json, exec_started_at_ms,
  exec_finished_at_ms, exec_duration_ms, created_at)
SELECT id, session_id, NULLIF(run_id,''), role, visibility, content, parts, message_id, model,
  usage, tool_step, tool_meta, exec_start, exec_finish, exec_duration, created_at
FROM (
  SELECT m.id AS id, m.session_id AS session_id, %s AS run_id, m.role AS role,
    CASE %s WHEN 'visible' THEN 'visible' WHEN 'repaired' THEN 'repaired' WHEN 'withdrawn' THEN 'withdrawn' END AS visibility,
    m.content AS content, IFNULL(%s,'[]') AS parts, IFNULL(%s,'') AS message_id, IFNULL(%s,'') AS model,
    IFNULL(%s,'') AS usage, IFNULL(%s,'') AS tool_step, IFNULL(%s,'') AS tool_meta,
    %s AS exec_start,
    %s AS exec_finish,
    %s AS exec_duration,
    m.created_at AS created_at
  FROM legacy_fb_messages m
  WHERE EXISTS(SELECT 1 FROM fb_sessions s WHERE s.id=m.session_id)
)
WHERE visibility IS NOT NULL AND role IN ('user','assistant','tool','reasoning','system')`,
		timing.runID, visibility, parts, messageID, model, usage, toolStep, toolMeta,
		execStart, execFinish, execDuration))
	if err != nil {
		return 0, err
	}
	if err := carryAutoincrement(ctx, conn, "fb_messages"); err != nil {
		return 0, err
	}
	return rows, nil
}

// migrateV1RunTiming moves the run-level timing off the message rows and onto
// the run they belong to, and binds old assistant turns to a run:
//
//   - A turn that already names a run has its window and worked time written
//     to that run, read from the newest timed assistant row of the run.
//   - A turn with no run id (rows written before run binding existed) is
//     matched by its own window to a still-untimed run of the same session
//     that no other rows belong to; when nothing matches, a run row is created
//     for the turn — the turn did happen, it was simply never recorded — so
//     replay can show its "Worked for" line like any other.
//
// Timing lives on the rows only for these two cases now, so it is read from
// legacy_fb_messages, not fb_messages.
func migrateV1RunTiming(ctx context.Context, conn *sql.Conn) error {
	if ok, err := legacyTableExists(ctx, conn, "legacy_fb_messages"); err != nil || !ok {
		return err
	}
	timing, err := resolveLegacyMessageTiming(ctx, conn)
	if err != nil || !timing.timed {
		return err
	}
	// a. Rows that already name a run: the newest timed assistant row wins.
	timed, err := execCopy(ctx, conn, fmt.Sprintf(`
UPDATE fb_runs SET started_at_ms=t.start_ms, finished_at_ms=t.finish_ms, worked_ms=t.worked
FROM (
  SELECT run_id, start_ms, finish_ms, worked FROM (
    SELECT %s AS run_id, %s AS start_ms, %s AS finish_ms, IFNULL(%s,0) AS worked,
      ROW_NUMBER() OVER (PARTITION BY %s ORDER BY id DESC) AS pick
    FROM legacy_fb_messages
    WHERE role='assistant' AND IFNULL(%s,'')<>'' AND %s)
  WHERE pick=1
) AS t
WHERE fb_runs.id=t.run_id`,
		timing.runID, timing.startedMs, timing.finishedMs, timing.worked, timing.runID, timing.runID, timing.windowed()))
	if err != nil {
		return err
	}

	// b. Turns with no run id: the exact timing triple is one turn's close-out
	// write, so its rows group by it; each group is matched to a run or given one.
	type legacyTurn struct {
		sessionID string
		startMs   int64
		finishMs  int64
		worked    int64
		minID     int64
		maxID     int64
	}
	groupRows, err := conn.QueryContext(ctx, fmt.Sprintf(`
SELECT session_id, MIN(%s), MIN(%s), IFNULL(%s,0), MIN(id), MAX(id)
FROM legacy_fb_messages
WHERE IFNULL(%s,'')='' AND role IN ('assistant','reasoning') AND %s
  AND EXISTS(SELECT 1 FROM fb_sessions s WHERE s.id=legacy_fb_messages.session_id)
GROUP BY session_id, %s, %s, IFNULL(%s,0)
ORDER BY MIN(id) ASC`,
		timing.startedMs, timing.finishedMs, timing.worked, timing.runID, timing.windowed(),
		timing.startedRaw, timing.finishedRaw, timing.worked))
	if err != nil {
		return err
	}
	var turns []legacyTurn
	for groupRows.Next() {
		var t legacyTurn
		if err := groupRows.Scan(&t.sessionID, &t.startMs, &t.finishMs, &t.worked, &t.minID, &t.maxID); err != nil {
			groupRows.Close()
			return err
		}
		turns = append(turns, t)
	}
	groupRows.Close()
	if err := groupRows.Err(); err != nil {
		return err
	}

	created := 0
	for _, t := range turns {
		startSec, finishSec := t.startMs/1000, t.finishMs/1000
		var matched string
		// A candidate is a top-level run of the same session, still untimed and
		// owning no rows of its own, whose creation falls in the turn's window;
		// the closest to the turn's start wins.
		err := conn.QueryRowContext(ctx, `
SELECT id FROM fb_runs
WHERE session_id=? AND parent_run_id IS NULL AND started_at_ms IS NULL
  AND created_at BETWEEN ? AND ?
  AND NOT EXISTS(SELECT 1 FROM fb_messages m WHERE m.run_id=fb_runs.id)
ORDER BY ABS(created_at - ?), created_at, id
LIMIT 1`, t.sessionID, startSec-2, finishSec+1, startSec).Scan(&matched)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if errors.Is(err, sql.ErrNoRows) {
			// No run recorded this turn; create one so its clock has a home.
			matched = fmt.Sprintf("legacy-run-%s-%d", t.sessionID, t.minID)
			if _, err := conn.ExecContext(ctx, `
INSERT INTO fb_runs(id, session_id, input_text, status, created_at, updated_at)
VALUES(?,?,?,?,?,?)`, matched, t.sessionID, "", string(RunStatusDone), startSec, finishSec); err != nil {
				return err
			}
			created++
		}
		if _, err := conn.ExecContext(ctx, `UPDATE fb_runs SET started_at_ms=?, finished_at_ms=?, worked_ms=? WHERE id=?`,
			t.startMs, t.finishMs, t.worked, matched); err != nil {
			return err
		}
		// Bind the whole turn — its tool rows too — to the run, so replay's
		// "Worked for" line lands after the turn it closed.
		if _, err := conn.ExecContext(ctx, `
UPDATE fb_messages SET run_id=? WHERE session_id=? AND id BETWEEN ? AND ? AND run_id IS NULL`,
			matched, t.sessionID, t.minID, t.maxID); err != nil {
			return err
		}
	}
	slog.Info("state migration run timing", "runs_timed", timed, "turns", len(turns), "turns_matched", len(turns)-created, "runs_created", created)
	return nil
}

// migrateV1SessionUIState copies each session's browsing state, dropping the
// rows whose session did not survive.
func migrateV1SessionUIState(ctx context.Context, conn *sql.Conn) (int64, error) {
	if ok, err := legacyTableExists(ctx, conn, "legacy_fb_session_ui_state"); err != nil || !ok {
		return 0, err
	}
	e := &v1Expr{conn: conn}
	stateJSON := e.get("legacy_fb_session_ui_state", "state_json", "NULL")
	if e.err != nil {
		return 0, e.err
	}
	return execCopy(ctx, conn, fmt.Sprintf(`INSERT INTO fb_session_ui_state(session_id, surface, state_json, updated_at)
SELECT session_id, surface, IFNULL(%s,'{}'), updated_at
FROM legacy_fb_session_ui_state
WHERE session_id IN (SELECT id FROM fb_sessions)`, stateJSON))
}

// migrateV1SessionModelState copies each session's pinned model choice, for a
// file a build that already had the table stamped at an intermediate version;
// no v0 file has one.
func migrateV1SessionModelState(ctx context.Context, conn *sql.Conn) (int64, error) {
	if ok, err := legacyTableExists(ctx, conn, "legacy_fb_session_model_state"); err != nil || !ok {
		return 0, err
	}
	return execCopy(ctx, conn, `INSERT INTO fb_session_model_state(session_id, provider, model, effort, updated_at)
SELECT session_id, provider, model, effort, updated_at
FROM legacy_fb_session_model_state
WHERE session_id IN (SELECT id FROM fb_sessions)`)
}

// migrateV1SessionPromptState copies the frozen prompt state byte for byte —
// it is the cached prompt prefix — dropping the rows whose session did not
// survive. Columns are named, not taken positionally, so a legacy table whose
// columns were reconciled in a different order still lands value on value.
func migrateV1SessionPromptState(ctx context.Context, conn *sql.Conn) (int64, error) {
	if ok, err := legacyTableExists(ctx, conn, "legacy_fb_session_prompt_state"); err != nil || !ok {
		return 0, err
	}
	return execCopy(ctx, conn, `INSERT INTO fb_session_prompt_state(session_id, key, value, updated_at)
SELECT session_id, key, value, updated_at
FROM legacy_fb_session_prompt_state
WHERE session_id IN (SELECT id FROM fb_sessions)`)
}

// carryAutoincrement keeps an AUTOINCREMENT table's high-water mark across the
// rebuild. The rename carried the legacy counter to legacy_<table>; copying the
// rows only advances the new counter to the largest id still present, so ids
// the legacy table handed out to rows since deleted would be issued again.
// AUTOINCREMENT exists to rule that out, and the migration keeps the promise.
func carryAutoincrement(ctx context.Context, conn *sql.Conn, table string) error {
	var legacySeq sql.NullInt64
	err := conn.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name=?`, "legacy_"+table).Scan(&legacySeq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE sqlite_sequence SET seq=MAX(seq, ?) WHERE name=?`, legacySeq.Int64, table); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `
INSERT INTO sqlite_sequence(name, seq)
SELECT ?, ? WHERE NOT EXISTS(SELECT 1 FROM sqlite_sequence WHERE name=?)`, table, legacySeq.Int64, table)
	return err
}

// fixDanglingReferences NULLs the reference columns whose target did not
// survive the migration; with foreign keys off, these are the rows
// PRAGMA foreign_key_check would otherwise reject at the end.
func fixDanglingReferences(ctx context.Context, conn *sql.Conn) error {
	for _, stmt := range []string{
		`UPDATE fb_sessions SET parent_session_id=NULL
		  WHERE parent_session_id IS NOT NULL
		    AND NOT EXISTS(SELECT 1 FROM fb_sessions p WHERE p.id=fb_sessions.parent_session_id)`,
		`UPDATE fb_sessions SET project_id=NULL
		  WHERE project_id IS NOT NULL
		    AND NOT EXISTS(SELECT 1 FROM fb_projects t WHERE t.id=fb_sessions.project_id)`,
		`UPDATE fb_sessions SET compact_boundary_message_id=NULL
		  WHERE compact_boundary_message_id IS NOT NULL
		    AND NOT EXISTS(SELECT 1 FROM fb_messages m WHERE m.id=fb_sessions.compact_boundary_message_id)`,
		`UPDATE fb_sessions SET context_reset_message_id=NULL
		  WHERE context_reset_message_id IS NOT NULL
		    AND NOT EXISTS(SELECT 1 FROM fb_messages m WHERE m.id=fb_sessions.context_reset_message_id)`,
		`UPDATE fb_messages SET run_id=NULL
		  WHERE run_id IS NOT NULL
		    AND NOT EXISTS(SELECT 1 FROM fb_runs r WHERE r.id=fb_messages.run_id)`,
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// legacyColumnExpr is the building block of a dedicated copy: the column
// itself when the legacy table has it, the given SQL expression when a still
// older database predates the column.
func legacyColumnExpr(ctx context.Context, conn *sql.Conn, legacyTable, column, fallback string) (string, error) {
	columns, err := connTableColumnNames(ctx, conn, legacyTable)
	if err != nil {
		return "", err
	}
	if columns[strings.ToLower(column)] {
		return quoteIdent(column), nil
	}
	return "(" + fallback + ")", nil
}

// connOrdinaryTables lists user tables, excluding the sqlite_* bookkeeping,
// virtual tables, and a virtual table's shadow tables.
func connOrdinaryTables(ctx context.Context, conn *sql.Conn) ([]string, error) {
	rows, err := conn.QueryContext(ctx,
		`SELECT name, sql FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite\_%' ESCAPE '\'`)
	if err != nil {
		return nil, err
	}
	type object struct{ name, ddl string }
	var all, virtual []object
	for rows.Next() {
		var o object
		if err := rows.Scan(&o.name, &o.ddl); err != nil {
			rows.Close()
			return nil, err
		}
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(o.ddl)), "CREATE VIRTUAL TABLE") {
			virtual = append(virtual, o)
			continue
		}
		all = append(all, o)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	var tables []string
	for _, o := range all {
		shadow := false
		for _, v := range virtual {
			if strings.HasPrefix(o.name, v.name+"_") {
				shadow = true
				break
			}
		}
		if !shadow {
			tables = append(tables, o.name)
		}
	}
	return tables, nil
}

// declaredSchemaObjects reads the objects schema.sql produces, by running it
// on a throwaway database — the same path a fresh state file takes, so the set
// and each object's stored text are by definition the ones the script means.
func declaredSchemaObjects(ctx context.Context) ([]schemaObject, error) {
	target, err := sql.Open(sqliteDriverName(), ":memory:")
	if err != nil {
		return nil, err
	}
	defer func() { _ = target.Close() }()
	// An unnamed in-memory database belongs to the connection that created it,
	// so a pool free to open a second one would read the shape back from a
	// database where the script never ran.
	target.SetMaxOpenConns(1)
	if _, err := target.ExecContext(ctx, schemaSQL); err != nil {
		return nil, fmt.Errorf("build declared schema: %w", err)
	}
	return readSchemaObjects(ctx, target)
}

// declaredSchemaObjectNames is the set of object names schema.sql declares.
func declaredSchemaObjectNames(ctx context.Context) (map[string]bool, error) {
	objects, err := declaredSchemaObjects(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(objects))
	for _, object := range objects {
		names[object.name] = true
	}
	return names, nil
}

type schemaObject struct {
	typ, name, table, ddl string
}

type schemaQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// stateObjectsAddedAfterV2 are the tables later versions add to schema.sql, and
// with them every index on them. A database at v2 or below lacks them by
// definition, so the v1→v2 check that tells a finished v1 file from an
// intermediate one compares everything else, and v3 creates them from their
// declared text.
var stateObjectsAddedAfterV2 = map[string]bool{"fb_session_model_state": true}

// stateSchemaMatchesDeclared reports whether the database holds exactly the
// objects schema.sql declares, leaving out the tables in omit (and their
// indexes) on both sides. Comparing every object catches an intermediate
// build whose sessions table happened to be final while a later task had not
// updated another table or index yet.
func stateSchemaMatchesDeclared(ctx context.Context, conn *sql.Conn, omit map[string]bool) (bool, error) {
	want, err := declaredSchemaObjects(ctx)
	if err != nil {
		return false, err
	}
	got, err := readSchemaObjects(ctx, conn)
	if err != nil {
		return false, err
	}
	keep := func(objects []schemaObject) []schemaObject {
		out := objects[:0:0]
		for _, object := range objects {
			if !omit[object.table] {
				out = append(out, object)
			}
		}
		return out
	}
	want, got = keep(want), keep(got)
	if len(got) != len(want) {
		return false, nil
	}
	for i := range want {
		if got[i] != want[i] {
			return false, nil
		}
	}
	return true, nil
}

func readSchemaObjects(ctx context.Context, q schemaQueryer) ([]schemaObject, error) {
	rows, err := q.QueryContext(ctx, `
SELECT type, name, tbl_name, sql
FROM sqlite_master
WHERE name NOT LIKE 'sqlite\_%' ESCAPE '\' AND sql IS NOT NULL
ORDER BY type, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var objects []schemaObject
	for rows.Next() {
		var object schemaObject
		if err := rows.Scan(&object.typ, &object.name, &object.table, &object.ddl); err != nil {
			return nil, err
		}
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return objects, nil
}

// connTableColumnNames reads the lower-cased names of a table's columns.
func connTableColumnNames(ctx context.Context, conn *sql.Conn, table string) (map[string]bool, error) {
	rows, err := conn.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names[strings.ToLower(name)] = true
	}
	return names, rows.Err()
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
