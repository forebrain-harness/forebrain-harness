// The loaded-skill catalog and the store backing tool state.
package tool

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

// LoadedSkill is the internal catalog record for one discovered skill, keyed by
// skill name. RootDir stays out of anything model-visible so host filesystem
// paths are not advertised in every request.
//
// Version and VersionsRoot are the session's pin: the version the skill was
// at when this runtime loaded it, and where that version's history lives.
// Version 0 means the skill carries no version history — a built-in, a
// cross-tool directory — and its content is read from RootDir as always.
type LoadedSkill struct {
	Name    string
	RootDir string
	// Version is the published version this session reads, 0 when the skill
	// is not versioned.
	Version int
	// VersionsRoot is <skill root>/.versions/<name> for a versioned skill,
	// empty otherwise.
	VersionsRoot string
}

// PinnedDir is the directory this session reads the skill's content from: the
// pinned version's own copy for a versioned skill, the formal directory
// otherwise.
func (item LoadedSkill) PinnedDir() string {
	if item.Version > 0 && strings.TrimSpace(item.VersionsRoot) != "" {
		return filepath.Join(item.VersionsRoot, "v"+strconv.Itoa(item.Version))
	}
	return item.RootDir
}

// PinnedSkillFile is the SKILL.md this session reads for the skill.
func (item LoadedSkill) PinnedSkillFile() string {
	return filepath.Join(item.PinnedDir(), "SKILL.md")
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
		if item.Version <= 0 {
			item.Version = 0
			item.VersionsRoot = ""
		}
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

// LoadedSkillRoots lists every directory a loaded skill's content may be read
// from: the skill's formal root, plus the pinned version directory a
// versioned session reads instead. The pinned copy is exactly as much "backed
// by a discovered skill" as the formal directory is.
func (s *State) LoadedSkillRoots() []string {
	items := s.LoadedSkills()
	out := make([]string, 0, 2*len(items))
	seen := make(map[string]struct{}, 2*len(items))
	add := func(root string) {
		root = strings.TrimSpace(root)
		if root == "" {
			return
		}
		if _, ok := seen[root]; ok {
			return
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	for _, item := range items {
		add(item.RootDir)
		add(item.PinnedDir())
	}
	sort.Strings(out)
	return out
}

// LoadedSkillForPath returns the most-specific loaded skill containing path.
// A path inside the session's pinned version copy addresses the skill just as
// a path inside its formal directory does.
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
		within := pathWithinRoot(normalized, item.RootDir) || pathWithinRoot(lexical, item.RootDir)
		if !within && item.Version > 0 {
			within = pathWithinRoot(normalized, item.PinnedDir()) || pathWithinRoot(lexical, item.PinnedDir())
		}
		if !within {
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

// SkillDraftTarget is where a write aimed at a skill lands once the skill's
// version store has redirected it: the draft path, the skill it belongs to,
// and — when the write addressed the formal directory — the formal path it
// was aimed at.
type SkillDraftTarget struct {
	SkillName string
	Target    string
	Source    string
	// Addressed is the path the write addressed, in its original spelling.
	// The read tools key their read states by that spelling, and a formal
	// read is what satisfies a redirected write's read requirement, so the
	// redirection keeps it beside the canonical source.
	Addressed string
}

// SkillDraftResolver settles one write path against the versioned skill
// roots. handled=false leaves the path to the ordinary write rules; an error
// is a refusal — the version records are read-only, and a whole skill
// directory is the skills list's to delete, not a file write's.
//
// tool cannot reach the skill package (Layer 2 keeps tool below skill), so
// the runner injects the implementation; this port is the whole contract.
type SkillDraftResolver func(path string) (SkillDraftTarget, bool, error)

// SetSkillDraftResolver installs the draft-redirection port. Nil restores the
// un-redirected behavior, which is what a runtime without versioned skill
// roots — and every existing test harness — runs with.
func (s *State) SetSkillDraftResolver(resolve SkillDraftResolver) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.skillDraftResolve = resolve
	s.mu.Unlock()
}

func (s *State) draftResolver() SkillDraftResolver {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	resolve := s.skillDraftResolve
	s.mu.Unlock()
	return resolve
}

// resolveSkillDraftWrite settles one write path against the draft resolver:
// the redirected target when the version store owns the path, and whether any
// loaded-skill repair happened first, so the caller can attribute the write.
func (s *State) resolveSkillDraftWrite(requested string) (writePathResolution, bool, error) {
	resolve := s.draftResolver()
	if resolve == nil {
		return writePathResolution{}, false, nil
	}
	// The loaded-skill catalog's repair runs first: a shortened catalog path
	// is repaired into the formal directory it named, and it is that formal
	// path the draft redirection then answers for.
	base := strings.TrimSpace(requested)
	if target, item, ok := s.LoadedSkillWriteTarget(requested); ok {
		base = target
		_ = item
	}
	draft, handled, err := resolve(base)
	if err != nil || !handled {
		return writePathResolution{}, handled, err
	}
	draft.Addressed = strings.TrimSpace(requested)
	out := writePathResolution{Abs: draft.Target}
	if normalizeRootPath(draft.Source) != normalizeRootPath(draft.Target) && draft.Source != "" {
		out.Draft = &draft
	}
	if cleaned := normalizeRootPath(requested); cleaned != "" && cleaned != normalizeRootPath(draft.Target) && draft.Source == "" {
		// A write that addressed the draft through a different spelling:
		// still the same draft, still worth naming where it landed.
		out.Draft = &draft
	}
	return out, true, nil
}

// SkillDraftWriteTarget reports the draft path a write path redirects to, for
// callers that need to read or validate what a write will actually touch
// before the write runs. It answers exactly what the write tools' resolution
// answers, refusals included.
func (s *State) SkillDraftWriteTarget(path string) (target string, handled bool, err error) {
	res, handled, err := s.resolveSkillDraftWrite(path)
	if err != nil || !handled {
		return "", handled, err
	}
	return res.Abs, true, nil
}

// draftRedirectReadState answers the read a redirected write still owes. The
// model read the formal file; the version store seeded the draft from the
// published version; a read state recorded for the formal path whose content
// still matches the draft's bytes is a read of exactly those bytes. Anything
// else — no read at all, or a draft that has already moved on — is not.
func (s *State) draftRedirectReadState(res writePathResolution, abs string) (ReadState, bool) {
	if s == nil || res.Draft == nil {
		return ReadState{}, false
	}
	// The formal read is keyed by the spelling the read resolved — the
	// addressed path, or the canonical source when the write addressed a
	// repaired or differently-spelled path.
	var rs ReadState
	var ok bool
	for _, key := range []string{res.Draft.Addressed, res.Draft.Source} {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if candidate, found := s.GetReadState(key); found {
			rs, ok = candidate, true
			break
		}
	}
	if !ok {
		return ReadState{}, false
	}
	raw, err := os.ReadFile(abs)
	if err != nil || !readStateMatchesContent(rs, raw) {
		return ReadState{}, false
	}
	return rs, true
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
// the full redacted original of every compressed output in a database so the
// agent can always get back what a filter hid; this is what makes aggressive
// compression safe. We mirror that contract.
//
// The table lives in the process's state database in every mode (owner ruling
// 2026-11-01: one main database). It used to be a SQLite file of its own under
// state/outfilter — plan 212 §3 for the servers, and now for SQLite too,
// because a second file on shared storage is exactly what a replica cannot
// read safely.

// Store persists compressed-command history for later retrieval. It borrows
// the state database handle: the store never opens or closes a database of its
// own, and Close leaves the handle to whoever opened it.
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
