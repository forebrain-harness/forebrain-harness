// The loaded-skill catalog and the store backing tool state.
package tool

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	_ "github.com/mattn/go-sqlite3"
)

// LoadedSkill is the internal catalog record for one discovered skill, keyed by
// skill name. RootDir stays out of anything model-visible so host filesystem
// paths are not advertised in every request.
type LoadedSkill struct {
	Name    string
	RootDir string
}

// ReplaceLoadedSkillCatalog swaps in the skills discovered for the current
// agent. The catalog exists so skill directories are readable: it is the sole
// source of truth for skill resource access.
func (s *State) ReplaceLoadedSkillCatalog(skills []LoadedSkill) {
	if s == nil {
		return
	}
	next := normalizedLoadedSkills(skills)
	s.mu.Lock()
	s.loadedSkills = next
	s.mu.Unlock()
}

func normalizedLoadedSkills(skills []LoadedSkill) map[string]LoadedSkill {
	next := make(map[string]LoadedSkill, len(skills))
	for _, item := range skills {
		name := strings.TrimSpace(item.Name)
		root := normalizeRootPath(item.RootDir)
		if name == "" || root == "" {
			continue
		}
		item.Name = name
		item.RootDir = root
		next[name] = item
	}
	return next
}

func (s *State) LoadedSkills() []LoadedSkill {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	out := make([]LoadedSkill, 0, len(s.loadedSkills))
	for _, item := range s.loadedSkills {
		out = append(out, item)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *State) LoadedSkillRoots() []string {
	items := s.LoadedSkills()
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		root := strings.TrimSpace(item.RootDir)
		if root == "" {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	sort.Strings(out)
	return out
}

// LoadedSkillForPath returns the most-specific loaded skill containing path.
func (s *State) LoadedSkillForPath(path string) (LoadedSkill, bool) {
	if s == nil {
		return LoadedSkill{}, false
	}
	lexical := strings.TrimSpace(path)
	if abs, err := filepath.Abs(lexical); err == nil {
		lexical = filepath.Clean(abs)
	}
	normalized := normalizeRootPath(path)
	if normalized == "" && lexical == "" {
		return LoadedSkill{}, false
	}
	items := s.LoadedSkills()
	var matched LoadedSkill
	for _, item := range items {
		if !pathWithinRoot(normalized, item.RootDir) && !pathWithinRoot(lexical, item.RootDir) {
			continue
		}
		if matched.RootDir == "" || len(item.RootDir) > len(matched.RootDir) {
			matched = item
		}
	}
	return matched, matched.RootDir != ""
}

func (s *State) PathUnderLoadedSkillRoot(path string) bool {
	_, ok := s.LoadedSkillForPath(path)
	return ok
}

// LoadedSkillMainFile identifies a catalog-backed root SKILL.md. It accepts the
// canonical path and the shortened catalog spelling that read_file repairs, so
// a started event can use the Skill card before the handler finishes resolving
// the file.
func (s *State) LoadedSkillMainFile(path string) (LoadedSkill, string, bool) {
	if s == nil {
		return LoadedSkill{}, "", false
	}
	if item, ok := s.LoadedSkillForPath(path); ok {
		mainFile := normalizeRootPath(filepath.Join(item.RootDir, "SKILL.md"))
		if normalizeRootPath(path) == mainFile {
			return item, mainFile, true
		}
	}
	if resolved, item, ok := s.resolveShortenedSkillPath(path); ok {
		mainFile := normalizeRootPath(filepath.Join(item.RootDir, "SKILL.md"))
		if normalizeRootPath(resolved) == mainFile {
			return item, mainFile, true
		}
	}
	return LoadedSkill{}, "", false
}

// ResolveLoadedSkillRead resolves path only against roots backed by
// the discovered skills.
func (s *State) ResolveLoadedSkillRead(path string) (string, bool) {
	if s == nil {
		return "", false
	}
	abs, err := ResolveWithinRoots(path, s.LoadedSkillRoots())
	if err != nil {
		return "", false
	}
	return abs, true
}

// skillPathMatch is one way a requested path could be naming a file inside a
// loaded skill: the skill whose name appears as a directory in the path, and
// the file the trailing segments name inside that skill's real root.
type skillPathMatch struct {
	Skill LoadedSkill
	File  string
	// Shortened reports that the directory the path puts the skill in really
	// does contain that skill's root — the path is the catalog path with
	// intermediate directories dropped, not an unrelated path that happens to
	// share a skill's name.
	Shortened bool
}

// matchSkillPaths returns every loaded skill that could explain a path that is
// not there. It is the one place that question is answered, for all the answers
// built on it: repairing a read, telling the model where the skill it was
// reaching for actually lives, correcting a write aimed at a path that only
// names a skill, and recognizing a shell command that addresses one. Answering
// it in one place is the point — a path the read side resolves into a skill and
// the write side does not is how a "modify this skill" write became a stray
// directory beside it.
//
// A path that exists is never matched — a real file is never reinterpreted.
func (s *State) matchSkillPaths(path string) []skillPathMatch {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil
	}
	requested, err := filepath.Abs(trimmed)
	if err != nil {
		return nil
	}
	// Cleaning here is what keeps the trailing segments a plain suffix: no ".."
	// survives to be re-joined onto a skill root.
	requested = filepath.Clean(requested)
	if _, err := os.Stat(requested); err == nil {
		return nil
	}
	var out []skillPathMatch
	for _, item := range s.LoadedSkills() {
		if match, ok := matchSkillPath(requested, item); ok {
			out = append(out, match)
		}
	}
	return out
}

// matchSkillPath returns how requested could be naming something in item: the
// file inside item's root that its trailing segments name once the dropped
// directories are restored, and whether the directory it put the skill in is
// one that really holds that skill.
//
// A path that names neither — no file there, and a directory that does not hold
// the skill — is an unrelated path that merely shares a name, and is no match.
func matchSkillPath(requested string, item LoadedSkill) (skillPathMatch, bool) {
	dir, rest := requested, ""
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return skillPathMatch{}, false
		}
		base := filepath.Base(dir)
		if base == item.Name {
			candidate := item.RootDir
			if rest != "" {
				candidate = filepath.Join(item.RootDir, rest)
			}
			file := ""
			// The same pathguard an ordinary catalog read goes through, so a
			// symlink inside a skill cannot name a file outside it.
			if abs, err := ResolveWithinRoots(candidate, []string{item.RootDir}); err == nil {
				if fi, statErr := os.Stat(abs); statErr == nil && fi.Mode().IsRegular() {
					file = abs
				}
			}
			shortened := pathWithinRoot(item.RootDir, parent)
			if file != "" || shortened {
				return skillPathMatch{Skill: item, File: file, Shortened: shortened}, true
			}
		}
		if rest == "" {
			rest = base
		} else {
			rest = filepath.Join(base, rest)
		}
		dir = parent
	}
}

// resolveShortenedSkillPath repairs the one mistake the skill catalog invites:
// a catalog path quoted with the directories above the skill dropped. The
// catalog hands the model `<skills>/.system/caveman/caveman-commit/SKILL.md`,
// and a model that reads the skill name as the name of the directory holding it
// asks for `<skills>/.system/caveman-commit/SKILL.md`.
//
// The repair is deliberately narrow, because silently reading the wrong file is
// worse than failing a read: it fires only for a path whose own directories say
// it was reaching into this skill, it never leaves the skill root, and two
// skills that both explain the path are an ambiguity rather than a guess
// between them. No location becomes readable that ResolveLoadedSkillRead did
// not already reach, and an existing path is never rewritten.
func (s *State) resolveShortenedSkillPath(path string) (string, LoadedSkill, bool) {
	var (
		resolved string
		matched  LoadedSkill
	)
	for _, match := range s.matchSkillPaths(path) {
		if !match.Shortened || match.File == "" {
			continue
		}
		if resolved != "" && resolved != match.File {
			return "", LoadedSkill{}, false
		}
		resolved, matched = match.File, match.Skill
	}
	if resolved == "" {
		return "", LoadedSkill{}, false
	}
	return resolved, matched, true
}

// loadedSkillForMissingPath names the skill a path that is not there was
// reaching for, when exactly one loaded skill explains it. It answers the reads
// too far off to repair — a skill quoted under the wrong skills root, say —
// where the alternative is handing the model a bare "no such file" about a file
// the catalog told it to read.
func (s *State) loadedSkillForMissingPath(path string) (LoadedSkill, bool) {
	var matched LoadedSkill
	for _, match := range s.matchSkillPaths(path) {
		if matched.RootDir != "" && matched.RootDir != match.Skill.RootDir {
			return LoadedSkill{}, false
		}
		matched = match.Skill
	}
	return matched, matched.RootDir != ""
}

// loadedSkillAddressedByPath returns the loaded skill a path is addressing: the
// one that contains it, or — for a path that is not there — the one it is a
// shortened catalog path into, where the directory it puts the skill in really
// holds that skill.
//
// The second case is what keeps the catalog's boundary from being a matter of
// spelling. A model that quotes the catalog path with the bundle directory
// dropped is addressing that skill, and a write there is a write to the skill:
// answered any other way it becomes a stray directory beside the real one,
// holding an edit that changed nothing and a name that discovery then hides
// behind the skill it was meant to change. A new skill created under another
// root keeps its own name, because no directory there holds the existing one.
func (s *State) loadedSkillAddressedByPath(path string) (LoadedSkill, bool) {
	if item, ok := s.LoadedSkillForPath(path); ok {
		return item, true
	}
	for _, match := range s.matchSkillPaths(path) {
		if match.Shortened {
			return match.Skill, true
		}
	}
	return LoadedSkill{}, false
}

// LoadedSkillWriteTarget reports the loaded skill a write path addresses and the
// path the write lands on, repaired where the requested one was a shortened
// catalog path.
//
// It is resolveShortenedSkillPath's repair, applied to writes: the read side
// already decides what such a path means, and a path the read side resolves
// into a skill while the write side resolves somewhere else is how a "change
// this skill" write became a stray file beside it. The repair carries the same
// narrowness — it fires only for a path whose own directories say it was
// reaching into this skill, never leaves the skill root, treats two skills that
// both explain the path as an ambiguity, and never rewrites a path that exists —
// and the caller reports it the same way, so the model is told which file it
// actually wrote.
func (s *State) LoadedSkillWriteTarget(path string) (string, LoadedSkill, bool) {
	if item, ok := s.LoadedSkillForPath(path); ok {
		normalized := normalizeRootPath(path)
		if normalized == "" {
			normalized = filepath.Clean(strings.TrimSpace(path))
		}
		return normalized, item, true
	}
	return s.resolveShortenedSkillPath(path)
}

// LoadedSkillShellAccessReason reports why a shell command needs the user”'s
// explicit approval before it runs: it addresses a loaded skill and cannot be
// proven read-only, so it may rewrite instructions this session loads. Commands
// that do not address a loaded skill, and provably read-only ones, keep the
// normal shell policy and return "".
func (s *State) LoadedSkillShellAccessReason(command, cwd string, readOnly bool) string {
	item, referenced := s.loadedSkillReferencedByShell(command, cwd)
	if !referenced || readOnly {
		return ""
	}
	return fmt.Sprintf("This command is not provably read-only and reaches the %q skill, whose files this session loads its instructions from.", item.Name)
}

func (s *State) loadedSkillReferencedByShell(command, cwd string) (LoadedSkill, bool) {
	if item, ok := s.loadedSkillAddressedByPath(cwd); ok {
		return item, true
	}
	items := s.LoadedSkills()
	for _, item := range items {
		if strings.Contains(command, item.RootDir) {
			return item, true
		}
	}
	for _, token := range safety.SplitShellWords(command) {
		candidates := []string{strings.TrimSpace(strings.Trim(token, ";|&<>"))}
		if eq := strings.IndexByte(token, '='); eq >= 0 && eq+1 < len(token) {
			candidates = append(candidates, strings.TrimSpace(strings.Trim(token[eq+1:], ";|&<>")))
		}
		for _, candidate := range candidates {
			if candidate == "" || candidate == "-" || strings.HasPrefix(candidate, "$") || !safety.LooksLikeFilePath(candidate) {
				continue
			}
			path := candidate
			if !filepath.IsAbs(path) {
				path = filepath.Join(cwd, path)
			}
			if item, ok := s.loadedSkillAddressedByPath(path); ok {
				return item, true
			}
		}
	}
	return LoadedSkill{}, false
}

// store.go — local recovery store (CCR: Compress-Cite-Retrieve). Boost keeps
// the full redacted original of every compressed output in a SQLite database
// so the agent can always get back what a filter hid; this is what makes
// aggressive compression safe. We mirror that contract.

// Store persists compressed-command history for later retrieval.
type Store struct {
	db *sql.DB
	mu sync.Mutex
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

// OpenStore opens (creating if needed) the history database at path.
func OpenStore(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	// The same durability contract as the state database: WAL, an immediate
	// write lock so a second process (the terminal and the gateway share this
	// file) waits instead of failing to upgrade, and a five-second busy timeout.
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	if err := migrateHistoryStore(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// historySchemaVersion is the shape historySchemaSQL declares. A database at a
// lower version is carried forward by migrateHistoryStore; one at a higher
// version was written by a newer binary.
const historySchemaVersion = 1

// historySchemaSQL is the v1 shape: one table of compressed outputs, keyed by a
// single retrieval id. It exists as a plain script rather than the additive
// "CREATE TABLE IF NOT EXISTS" the store used to run, because a shape newer
// than the released one has to be built by a versioned migration, not patched.
const historySchemaSQL = `
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
) STRICT;`

// migrateHistoryStore brings the history database behind db to
// historySchemaVersion in one IMMEDIATE transaction. A fresh file is created
// at the current version; a v0 file — the released `commands` table, which may
// predate the kind discriminator and the spool columns — has its rows carried
// onto the v1 table and is then dropped. Timestamps move from the driver's TEXT
// spelling to milliseconds since the epoch; a non-empty timestamp that does not
// parse would silently become NULL under a NOT NULL column, so the count of
// such rows is checked and the migration fails instead.
func migrateHistoryStore(db *sql.DB) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("outfilter store migrate: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	var version int
	if err := conn.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("outfilter store migrate: %w", err)
	}
	if version == historySchemaVersion {
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return err
		}
		committed = true
		return nil
	}
	if version > historySchemaVersion {
		return fmt.Errorf("outfilter store was written by a newer forebrain (user_version=%d)", version)
	}

	hasOutput, err := historyTableExists(ctx, conn, "output_records")
	if err != nil {
		return err
	}
	if !hasOutput {
		if _, err := conn.ExecContext(ctx, historySchemaSQL); err != nil {
			return fmt.Errorf("outfilter store migrate: %w", err)
		}
	}
	hasCommands, err := historyTableExists(ctx, conn, "commands")
	if err != nil {
		return err
	}
	if hasCommands {
		if err := copyHistoryCommands(ctx, conn); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `DROP TABLE commands`); err != nil {
			return fmt.Errorf("outfilter store migrate: %w", err)
		}
	}
	// The v0 indexes name the table that no longer exists; dropping them keeps
	// a migrated file's sqlite_master identical to a fresh one's.
	for _, index := range []string{"idx_outfilter_commands_ts", "idx_outfilter_commands_kind_ts"} {
		if _, err := conn.ExecContext(ctx, `DROP INDEX IF EXISTS `+index); err != nil {
			return fmt.Errorf("outfilter store migrate: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, historySchemaVersion)); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

func historyTableExists(ctx context.Context, conn *sql.Conn, name string) (bool, error) {
	var n int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
		return false, fmt.Errorf("outfilter store migrate: %w", err)
	}
	return n > 0, nil
}

// copyHistoryCommands carries the v0 `commands` rows onto output_records,
// tolerating a still older shape: a column the old table lacks takes the v1
// default, and cmd/command are the same string under two names.
func copyHistoryCommands(ctx context.Context, conn *sql.Conn) error {
	present, err := historyColumnSet(ctx, conn, "commands")
	if err != nil {
		return err
	}
	expr := func(column, fallback string) string {
		if present[column] {
			return quoteHistoryIdent(column)
		}
		return fallback
	}
	kind := expr("kind", "'shell'")
	// A NULL or empty timestamp is "no time"; anything else must convert. The
	// value is rounded, not truncated — the julianday arithmetic lands a
	// fraction under the exact millisecond and CAST would drop it to the wrong
	// one.
	createdAt := `CASE WHEN IFNULL(` + expr("timestamp", "''") + `,'')='' THEN NULL
	  ELSE CAST(ROUND((julianday(NULLIF(` + expr("timestamp", "''") + `,'')) - 2440587.5) * 86400000) AS INTEGER) END`
	var badCount int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM commands WHERE `+createdAt+` IS NULL AND IFNULL(`+expr("timestamp", "''")+`,'')<>''`).Scan(&badCount); err != nil {
		return fmt.Errorf("outfilter store migrate: %w", err)
	}
	if badCount > 0 {
		return fmt.Errorf("outfilter store migrate: %d command timestamps could not be converted to milliseconds", badCount)
	}
	copySQL := fmt.Sprintf(`INSERT INTO output_records(
  id, kind, command, session_id, original_output, filtered_output,
  original_output_bytes, filtered_output_bytes, saved_tokens,
  capability_id, capability_version, retrieve_count, retrieve_reason,
  spool_path, spool_omitted_bytes, created_at_ms)
SELECT
  id, IFNULL(%s,'shell'), %s, %s, %s, %s,
  %s, %s, %s, %s, %s, %s, %s, %s, %s, %s
FROM commands`,
		kind,
		expr("cmd", "''"), expr("session_id", "''"),
		expr("original_output", "''"), expr("filtered_output", "''"),
		expr("original_output_bytes", "0"), expr("filtered_output_bytes", "0"), expr("saved_tokens", "0"),
		expr("capability_id", "''"), expr("capability_version", "''"),
		expr("retrieve_count", "0"), expr("retrieve_reason", "''"),
		expr("spool_path", "''"), expr("spool_omitted_bytes", "0"), createdAt)
	if _, err := conn.ExecContext(ctx, copySQL); err != nil {
		return fmt.Errorf("outfilter store migrate: %w", err)
	}
	return nil
}

func historyColumnSet(ctx context.Context, conn *sql.Conn, table string) (map[string]bool, error) {
	rows, err := conn.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("outfilter store migrate: %w", err)
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		set[strings.ToLower(name)] = true
	}
	return set, rows.Err()
}

func quoteHistoryIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
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
	res, err := s.db.Exec(
		`INSERT INTO output_records (kind, command, original_output, filtered_output,
			original_output_bytes, filtered_output_bytes, saved_tokens, session_id,
			capability_id, capability_version, spool_path, spool_omitted_bytes, created_at_ms)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.Kind, e.Command, e.OriginalOutput, e.FilteredOutput,
		e.OriginalBytes, e.FilteredBytes, e.SavedTokens, e.SessionID,
		e.CapabilityID, e.CapabilityVer, e.SpoolPath, e.SpoolOmittedBytes, e.Timestamp.UTC().UnixMilli(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
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
	err := s.db.QueryRow(
		`SELECT id, command, original_output, filtered_output,
			original_output_bytes, filtered_output_bytes, saved_tokens,
			session_id, capability_id, capability_version, retrieve_count, retrieve_reason,
			kind, spool_path, spool_omitted_bytes, created_at_ms
		 FROM output_records WHERE id = ?`, id,
	).Scan(&e.ID, &e.Command, &e.OriginalOutput, &e.FilteredOutput,
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
	_, err := s.db.Exec(
		`UPDATE output_records SET retrieve_count = retrieve_count + 1, retrieve_reason = ? WHERE id = ?`,
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
	query := `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN saved_tokens > 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(original_output_bytes), 0),
		COALESCE(SUM(filtered_output_bytes), 0),
		COALESCE(SUM(saved_tokens), 0),
		COALESCE(SUM(retrieve_count), 0)
		FROM output_records`
	var where []string
	args := []any{}
	if !since.IsZero() {
		where = append(where, `created_at_ms >= ?`)
		args = append(args, since.UTC().UnixMilli())
	}
	if kind != "" {
		where = append(where, `kind = ?`)
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

// Close closes the underlying database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
