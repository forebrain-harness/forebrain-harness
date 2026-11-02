package memory

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

// The BM25 half of memory search.
//
// Exact substring matching answers "does this line contain what I typed", which
// is precise and unforgiving: one character off, and a memory that is plainly
// about the subject scores nothing. That is tolerable for an identifier and
// useless for Chinese, where the same idea is written with different word
// boundaries every time.
//
// So the files are also indexed into FTS5, segmented into terms, and every line
// FTS5 returns for a query carries a bm25() score. A line is a candidate when
// it shares a whole term with the query, and bm25 says how relevant it is.
//
// Both sides are cut by searchTerms, and a term is compared whole: the line
// under a term and the query asking for it are the same string or they do not
// match at all. FTS5's own tokenizer cuts the stored terms again, at every
// connector, so it may offer a candidate whose terms only look alike — the
// exact comparison below is what decides, and drops it.

// The search-index statements. Every engine keeps the same three tables —
// the index rows, the per-file bookkeeping, and the file table — but only
// SQLite's fb_memory_fts is a virtual table with a rowid: the other two carry
// an explicit row_id, and each engine matches the terms its own way (design
// §2.5). The Go side cuts both the query and the stored line into whole terms
// and is the final arbiter, so the SQL-side match is a recall filter whose
// scores only have to rank the candidates it returns.
var (
	memIndexFilesSelectSQL = dbx.SQL{
		SQLite:   `SELECT path, size, mtime_unix, terms_revision, first_rowid, line_count FROM fb_memory_index_files WHERE root = ?`,
		MySQL:    `SELECT path, size, mtime_unix, terms_revision, first_rowid, line_count FROM fb_memory_index_files WHERE root = ?`,
		Postgres: `SELECT path, size, mtime_unix, terms_revision, first_rowid, line_count FROM fb_memory_index_files WHERE root = $1`,
	}
	memFTSRangeDeleteSQL = dbx.SQL{
		SQLite:   `DELETE FROM fb_memory_fts WHERE rowid >= ? AND rowid < ?`,
		MySQL:    `DELETE FROM fb_memory_fts WHERE row_id >= ? AND row_id < ?`,
		Postgres: `DELETE FROM fb_memory_fts WHERE row_id >= $1 AND row_id < $2`,
	}
	memIndexFileDeleteSQL = dbx.SQL{
		SQLite:   `DELETE FROM fb_memory_index_files WHERE root = ? AND path = ?`,
		MySQL:    `DELETE FROM fb_memory_index_files WHERE root = ? AND path = ?`,
		Postgres: `DELETE FROM fb_memory_index_files WHERE root = $1 AND path = $2`,
	}
	memFTSHighWaterSQL = dbx.SQL{
		SQLite:   `SELECT IFNULL(MAX(rowid),0)+1 FROM fb_memory_fts`,
		MySQL:    `SELECT IFNULL(MAX(row_id),0)+1 FROM fb_memory_fts`,
		Postgres: `SELECT COALESCE(MAX(row_id),0)+1 FROM fb_memory_fts`,
	}
	memFTSInsertSQL = dbx.SQL{
		SQLite:   `INSERT INTO fb_memory_fts(rowid, terms, root, path, line_no) VALUES(?,?,?,?,?)`,
		MySQL:    `INSERT INTO fb_memory_fts(row_id, terms, root, path, line_no) VALUES(?,?,?,?,?)`,
		Postgres: `INSERT INTO fb_memory_fts(row_id, terms, root, path, line_no) VALUES($1,$2,$3,$4,$5)`,
	}
	// The file row is keyed by (root, path): a primary key on SQLite and
	// PostgreSQL, and on MySQL the unique index over the SHA-256 of the pair —
	// the only unique key the table has, so its upsert conflicts exactly there.
	memIndexFileUpsertSQL = dbx.SQL{
		SQLite: `INSERT INTO fb_memory_index_files(root, path, size, mtime_unix, terms_revision, first_rowid, line_count) VALUES(?,?,?,?,?,?,?)
		 ON CONFLICT(root, path) DO UPDATE SET size = excluded.size, mtime_unix = excluded.mtime_unix, terms_revision = excluded.terms_revision, first_rowid = excluded.first_rowid, line_count = excluded.line_count`,
		MySQL: `INSERT INTO fb_memory_index_files(root, path, size, mtime_unix, terms_revision, first_rowid, line_count) VALUES(?,?,?,?,?,?,?)
		 ON DUPLICATE KEY UPDATE size = VALUES(size), mtime_unix = VALUES(mtime_unix), terms_revision = VALUES(terms_revision), first_rowid = VALUES(first_rowid), line_count = VALUES(line_count)`,
		Postgres: `INSERT INTO fb_memory_index_files(root, path, size, mtime_unix, terms_revision, first_rowid, line_count) VALUES($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT(root, path) DO UPDATE SET size = excluded.size, mtime_unix = excluded.mtime_unix, terms_revision = excluded.terms_revision, first_rowid = excluded.first_rowid, line_count = excluded.line_count`,
	}
	// The candidate query, one shape per engine: SQLite asks FTS5 for a bm25
	// score, MySQL for the FULLTEXT relevance of the same quoted phrases, and
	// PostgreSQL for a ts_rank over the generated tsvector. All three are given
	// the query's terms once (MySQL twice, because its score and its filter are
	// two MATCH clauses) and the memory root to stay inside.
	memFTSSearchSQL = dbx.SQL{
		SQLite: `SELECT path, line_no, terms, bm25(fb_memory_fts) FROM fb_memory_fts
		 WHERE fb_memory_fts MATCH ? AND root = ?`,
		MySQL: `SELECT path, line_no, terms, -(MATCH(terms) AGAINST(? IN BOOLEAN MODE)) FROM fb_memory_fts
		 WHERE MATCH(terms) AGAINST(? IN BOOLEAN MODE) AND root = ?`,
		// PostgreSQL's ts_rank counts term frequency and knows nothing of
		// rarity, which is the only thing the relevance floor is about, so the
		// score is the sum of the inverse document frequencies of the query's
		// terms this line carries — bm25's dominant term, on bm25's scale, and
		// negated like it. The query itself is still one tsquery.
		Postgres: `WITH corpus AS (
		   SELECT COUNT(*)::float8 AS n FROM fb_memory_fts WHERE root = $2
		 ), wanted AS (
		   SELECT w.word AS word,
		     (SELECT COUNT(*)::float8 FROM fb_memory_fts x
		      WHERE x.root = $2 AND x.terms_tsv @@ plainto_tsquery('simple', w.word)) AS df
		   FROM unnest(string_to_array($1, ' ')) AS w(word)
		 ), query AS (
		   SELECT to_tsquery('simple',
		     (SELECT string_agg(quote_literal(wanted.word), ' | ') FROM wanted)) AS value
		 )
		 SELECT f.path, f.line_no, f.terms,
		   -(SELECT COALESCE(SUM(LN((c.n + 0.5) / (wanted.df + 0.5))), 0)
		     FROM wanted, corpus c
		     WHERE f.terms_tsv @@ plainto_tsquery('simple', wanted.word)) AS score
		 FROM fb_memory_fts f, query q
		 WHERE f.terms_tsv @@ q.value AND f.root = $2`,
	}
)

// searchIndex is the FTS5 index over one state database. It is addressed by
// memory root, so each project's and the global scope's memories stay separate
// rows of one table and a search never sees another scope's lines.
type searchIndex struct{ db *sql.DB }

func newSearchIndex(db *sql.DB) *searchIndex {
	if db == nil {
		return nil
	}
	return &searchIndex{db: db}
}

// indexedFile is one file as the search walk read it: the bytes that were
// matched against, plus the stat the index uses to notice the next change.
type indexedFile struct {
	path    string
	content string
	size    int64
	modTime time.Time
}

// lineSignal is what the index knows about one line of one file: which of the
// call's queries share a term with it, which terms those were, and how relevant
// bm25 judged the line. terms is keyed by the query's position in the request.
type lineSignal struct {
	terms map[int][]string
	score float64
}

// fileSignals maps a 1-indexed line number to what the index knows about it.
type fileSignals map[int]*lineSignal

// sync brings the index for root in line with the files just read.
//
// A file is re-indexed when its size or mtime moved, when the rules that cut a
// line into terms changed under it, and dropped when it is
// gone. Because the caller passes the very content it is about to search, the
// index can never describe a version of a file the search is not looking at —
// including a file, or a whole scope, that was erased: the walk stops finding
// it and this drops it, so nothing has to remember to invalidate the index.
//
// files is the whole root, every time, so a path no longer in it is a path that
// is gone.
//
// What the index already holds is read inside the write transaction. Two
// searches of the same root (the terminal and the gateway share the index)
// would otherwise both plan from the same stale rowid ranges, and the second
// would re-index a file over the first's rows without deleting them — rows no
// file record points at any more, returned by every later search.
func (s *searchIndex) sync(ctx context.Context, root string, files []indexedFile) error {
	tx, err := dbx.BeginTx(ctx, s.db, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stored, err := storedFiles(ctx, tx, root)
	if err != nil {
		return err
	}
	present := make(map[string]struct{}, len(files))
	for _, file := range files {
		present[file.path] = struct{}{}
		prev, known := stored[file.path]
		if known && prev.size == file.size && prev.mtime == file.modTime.Unix() && prev.revision == termsRevisionInForce() {
			continue
		}
		if err := replaceFileRows(ctx, tx, root, file, prev); err != nil {
			return err
		}
	}
	for path, prev := range stored {
		if _, kept := present[path]; kept {
			continue
		}
		if err := deleteFileRows(ctx, tx, root, path, prev); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func withinScope(path, scope string) bool {
	if scope == "" {
		return true
	}
	return path == scope || strings.HasPrefix(path, scope+"/")
}

type storedFileState struct {
	size  int64
	mtime int64
	// revision is the term-splitting revision the rows were written under. A
	// file whose bytes never moved still has to be re-indexed when that
	// changes: its rows spell terms by rules the query no longer uses.
	revision int64
	// firstRowid and lineCount locate the file's rows in fb_memory_fts, so a
	// re-index drops its own rows by range instead of scanning the whole index.
	firstRowid int64
	lineCount  int64
}

func storedFiles(ctx context.Context, tx *dbx.Tx, root string) (map[string]storedFileState, error) {
	rows, err := tx.QueryContext(ctx, memIndexFilesSelectSQL.For(tx.Dialect()), root)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stored := map[string]storedFileState{}
	for rows.Next() {
		var path string
		var state storedFileState
		if err := rows.Scan(&path, &state.size, &state.mtime, &state.revision, &state.firstRowid, &state.lineCount); err != nil {
			return nil, err
		}
		stored[path] = state
	}
	return stored, rows.Err()
}

func deleteFileRows(ctx context.Context, tx *dbx.Tx, root, path string, prev storedFileState) error {
	if prev.lineCount > 0 {
		if _, err := tx.ExecContext(ctx, memFTSRangeDeleteSQL.For(tx.Dialect()), prev.firstRowid, prev.firstRowid+prev.lineCount); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, memIndexFileDeleteSQL.For(tx.Dialect()), root, path)
	return err
}

func replaceFileRows(ctx context.Context, tx *dbx.Tx, root string, file indexedFile, prev storedFileState) error {
	if err := deleteFileRows(ctx, tx, root, file.path, prev); err != nil {
		return err
	}
	// Rows are appended above the index high-water mark and their range recorded
	// on the file row, so the file's own rows can be dropped by rowid later
	// without a scan. The transaction holds the write lock (_txlock=immediate),
	// so no other writer can take the same range between the read and the insert.
	var firstRowid int64
	if err := tx.QueryRowContext(ctx, memFTSHighWaterSQL.For(tx.Dialect())).Scan(&firstRowid); err != nil {
		return err
	}
	insert, err := tx.PrepareContext(ctx, memFTSInsertSQL.For(tx.Dialect()))
	if err != nil {
		return err
	}
	defer insert.Close()
	var lineCount int64
	for index, line := range textLines(file.content) {
		terms := searchTerms(line)
		if len(terms) == 0 {
			continue
		}
		if _, err := insert.ExecContext(ctx, firstRowid+lineCount, strings.Join(terms, " "), root, file.path, index+1); err != nil {
			return err
		}
		lineCount++
	}
	_, err = tx.ExecContext(ctx, memIndexFileUpsertSQL.For(tx.Dialect()),
		root, file.path, file.size, file.modTime.Unix(), termsRevisionInForce(), firstRowid, lineCount)
	return err
}

// signals asks the index which lines under root share a term with any of the
// queries, and how relevant each is.
//
// The MATCH expression is an OR of every term of every query, which is what
// makes the index a recall layer rather than a second filter: a line that
// shares one term is a candidate, and bm25 decides whether it deserves to be
// read before the others. Which query a term came from is recovered here, from
// the query's own terms, so the caller can report what recalled each line.
func (s *searchIndex) signals(ctx context.Context, root string, queries []string) (map[string]fileSignals, error) {
	queryTerms := make([][]string, len(queries))
	var all []string
	for index, query := range queries {
		terms := searchTerms(query)
		queryTerms[index] = terms
		all = append(all, queryTerms[index]...)
	}
	expression := matchExpression(all)
	if expression == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, memFTSSearchSQL.For(dbx.DialectOf(s.db)), searchArgs(dbx.DialectOf(s.db), all, expression, root)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	signals := map[string]fileSignals{}
	for rows.Next() {
		var path, terms string
		var line int
		var score float64
		if err := rows.Scan(&path, &line, &terms, &score); err != nil {
			return nil, err
		}
		present := map[string]struct{}{}
		for _, term := range strings.Fields(terms) {
			present[term] = struct{}{}
		}
		signal := &lineSignal{terms: map[int][]string{}, score: score}
		for index, wanted := range queryTerms {
			for _, term := range wanted {
				if _, hit := present[term]; hit {
					signal.terms[index] = append(signal.terms[index], term)
				}
			}
		}
		if len(signal.terms) == 0 {
			continue
		}
		if signals[path] == nil {
			signals[path] = fileSignals{}
		}
		signals[path][line] = signal
	}
	return signals, rows.Err()
}

// searchArgs feeds the candidate query its dialect's arguments: SQLite takes
// the FTS5 expression as it stands, MySQL takes the same terms as quoted
// boolean-mode phrases (and twice, because its score and its filter are two
// MATCH clauses), and PostgreSQL takes the terms space-joined — the very shape
// its generated tsvector was built from — turned into a tsquery inside the
// statement, so no term's punctuation can be read as syntax.
func searchArgs(d dbx.Dialect, terms []string, expression, root string) []any {
	switch d {
	case dbx.MySQL:
		phrases := make([]string, 0, len(terms))
		seen := make(map[string]struct{}, len(terms))
		for _, term := range terms {
			term = strings.TrimSpace(term)
			if term == "" {
				continue
			}
			if _, repeat := seen[term]; repeat {
				continue
			}
			seen[term] = struct{}{}
			// A quote inside a boolean-mode phrase is escaped with a
			// backslash; the term is bound, so nothing else is escaped.
			phrases = append(phrases, `"`+strings.ReplaceAll(term, `"`, `\"`)+`"`)
		}
		joined := strings.Join(phrases, " ")
		return []any{joined, joined, root}
	case dbx.Postgres:
		return []any{strings.Join(terms, " "), root}
	default:
		return []any{expression, root}
	}
}

// matchExpression builds the FTS5 query: every term as a quoted string, ORed.
// Quoting is what keeps a term that happens to be an FTS5 operator ("OR", "*",
// "NEAR") from being read as syntax, and an embedded quote is doubled, which is
// how FTS5 escapes one.
func matchExpression(terms []string) string {
	seen := make(map[string]struct{}, len(terms))
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		if _, repeat := seen[term]; repeat {
			continue
		}
		seen[term] = struct{}{}
		quoted = append(quoted, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
	}
	if len(quoted) == 0 {
		return ""
	}
	return strings.Join(quoted, " OR ")
}
