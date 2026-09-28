package memory

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// TestMain puts the segmentation dictionary beside the test binary, which is
// where a search looks for it.
func TestMain(m *testing.M) {
	executable, err := os.Executable()
	if err == nil {
		err = InstallDictionary(filepath.Dir(executable))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "install segmentation dictionary:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// filler is text sharing no term with any test query. An index is only able to
// tell a relevant line from an irrelevant one when the query's terms are rare
// in it — that is what BM25's IDF measures — so a store of two lines that both
// answer the query is a store where every score is zero and nothing can be
// ranked or filtered. Real memory folders are hundreds of lines; these fill to
// the same shape.
const filler = "部署 流水线 构建 缓存 产物 校验 回滚 发布 灰度 观测\n" +
	"日志 追踪 采样 告警 阈值 容量 扩缩 降级 熔断 限流\n" +
	"账号 权限 角色 策略 审计 密钥 轮换 证书 网关 路由\n"

// testSearchIndexDB is a state database for a test that needs to search. Every
// search goes through the index now, so a backend built without one refuses to
// answer — which is what the tests below would hit if they skipped this.
// padded surrounds body with lines sharing none of its words. A term is only
// discriminating when it is rare, which is what BM25 scores and what the
// relevance floor filters on, so a fixture of a few lines that all answer the
// query is a fixture where nothing can be ranked and everything is noise.
func padded(body string) string {
	return body + strings.Repeat(filler, 25)
}

func testSearchIndexDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func indexedBackend(t *testing.T, files map[string]string) Backend {
	t.Helper()
	root := filepath.Join(t.TempDir(), "memories")
	if _, seeded := files["filler.md"]; !seeded {
		files["filler.md"] = strings.Repeat(filler, 30)
	}
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return New(root).WithSearchIndex(testSearchIndexDB(t))
}

func searchAll(t *testing.T, b Backend, queries ...string) SearchResponse {
	t.Helper()
	response, err := b.Search(SearchRequest{Queries: queries, TopK: MaxSearchResults})
	if err != nil {
		t.Fatalf("search %v: %v", queries, err)
	}
	return response
}

// A Chinese query recalls a memory it shares a word with, even though no
// substring of the query occurs in it.
//
// This is the case exact matching cannot serve: Chinese is written without
// spaces, so the same idea comes out with different word boundaries every time
// and one differing character reduces a substring search to nothing. The index
// segments both sides into words, so the shared word is found — and the match
// reports that word, not the query, because the word is what the reader has to
// be shown in the text.
func TestSearchRecallsAChineseMemoryThroughASharedWord(t *testing.T) {
	b := indexedBackend(t, map[string]string{
		"MEMORY.md": "审批浮层必须完整展示待批准内容\n无关的一行\n",
	})
	response := searchAll(t, b, "审批覆盖层")
	if len(response.Matches) != 1 {
		t.Fatalf("matches=%d, want the line sharing 审批: %+v", len(response.Matches), response.Matches)
	}
	match := response.Matches[0]
	if match.MatchLineNumber != 1 {
		t.Fatalf("match line=%d want 1", match.MatchLineNumber)
	}
	if len(match.MatchedTerms) == 0 || !strings.Contains(match.Content, match.MatchedTerms[0]) {
		t.Fatalf("matched terms %v must occur in the matched text %q", match.MatchedTerms, match.Content)
	}
	// Recall is entirely the index's, so a backend without one refuses rather
	// than reporting an empty memory it never actually searched.
	if _, err := New(b.Root).Search(SearchRequest{Queries: []string{"审批覆盖层"},
		TopK: MaxSearchResults}); err == nil {
		t.Fatal("a backend with no index must refuse to search, not answer empty")
	}
}

// An exact substring hit is never pushed below a memory that only shares a
// word. Recall grows; precision keeps its place at the top, which is what a
// small max_results depends on.
func TestSearchRanksExactHitsAboveSharedWordRecall(t *testing.T) {
	b := indexedBackend(t, map[string]string{
		"a-shared.md": "审批流程的说明\n",
		"b-exact.md":  "审批流程记录\n",
	})
	response := searchAll(t, b, "审批流程记录")
	if len(response.Matches) < 2 {
		t.Fatalf("want both files recalled, got %+v", response.Matches)
	}
	if response.Matches[0].Path != "b-exact.md" {
		t.Fatalf("exact hit must rank first, got %q", response.Matches[0].Path)
	}
}

// Relevance, not path order, decides among results the index recalled: the line
// that answers more of the query comes first even though its path sorts later.
func TestSearchOrdersSharedWordRecallByRelevance(t *testing.T) {
	b := indexedBackend(t, map[string]string{
		"a-one.md":  "浮层说明\n",
		"b-both.md": "审批浮层说明\n",
	})
	response := searchAll(t, b, "审批", "浮层")
	if len(response.Matches) < 2 {
		t.Fatalf("want both files recalled, got %+v", response.Matches)
	}
	if response.Matches[0].Path != "b-both.md" {
		t.Fatalf("the line answering both queries must rank first, got %q (%+v)", response.Matches[0].Path, response.Matches)
	}
}

// The index is refreshed from the same bytes the search reads, so an edit — or
// a deletion — can never leave a stale row answering for a file.
func TestSearchIndexNeverAnswersForContentNoLongerOnDisk(t *testing.T) {
	b := indexedBackend(t, map[string]string{"MEMORY.md": "审批浮层说明\n"})
	if got := len(searchAll(t, b, "审批覆盖层").Matches); got != 1 {
		t.Fatalf("matches=%d want 1 before the edit", got)
	}
	path := filepath.Join(b.Root, "MEMORY.md")
	if err := os.WriteFile(path, []byte("完全不同的内容\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Size differs, so the change is visible whatever the filesystem's mtime
	// granularity happens to be.
	if got := len(searchAll(t, b, "审批覆盖层").Matches); got != 0 {
		t.Fatalf("matches=%d want 0 after the line was rewritten", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := len(searchAll(t, b, "完全不同").Matches); got != 0 {
		t.Fatalf("matches=%d want 0 after the file was deleted", got)
	}
}

// A search narrowed to one directory must not prune the index for the files it
// never walked: it has no evidence about them, and throwing their rows away
// would make the next full search re-read everything.
func TestScopedSearchKeepsTheIndexOutsideItsSubtree(t *testing.T) {
	b := indexedBackend(t, map[string]string{
		"MEMORY.md":              "审批浮层说明\n",
		"rollout_summaries/a.md": "部署流程说明\n",
	})
	searchAll(t, b, "审批")
	scope := "rollout_summaries"
	if _, err := b.Search(SearchRequest{Queries: []string{"部署"}, Path: &scope, TopK: MaxSearchResults}); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := b.index.db.QueryRow(`SELECT count(*) FROM fb_memory_index_files WHERE root = ? AND path = 'MEMORY.md'`, b.Root).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("a scoped search dropped the index for MEMORY.md, which it never walked")
	}
}

// The relevance floor drops what the index recalled but cannot justify.
//
// Recall is deliberately wide: a line sharing one term with the query becomes a
// candidate, and for a query carrying any common word that is most of the
// store. Ranking alone does not fix it — with nothing better to offer, the
// least bad noise still comes back looking like recalled memory.
func TestSearchDropsIndexRecallBelowTheRelevanceFloor(t *testing.T) {
	// One line answers the query on a rare term; the rest only share a common
	// one, which is what BM25 scores as uninformative.
	body := "审批浮层必须完整展示待批准内容\n"
	for index := 0; index < 60; index++ {
		body += "部署 流水线 构建 产物 校验 内容\n"
	}
	b := indexedBackend(t, map[string]string{"MEMORY.md": body})

	response := searchAll(t, b, "审批的内容")
	if len(response.Matches) == 0 {
		t.Fatal("the line sharing the rare term must survive the floor")
	}
	for _, match := range response.Matches {
		if match.score > MinIndexRelevance {
			t.Fatalf("a match below the floor was returned: score=%f %q", match.score, match.Content)
		}
	}
	if !strings.Contains(response.Matches[0].Content, "审批") {
		t.Fatalf("the rare-term line must rank first, got %q", response.Matches[0].Content)
	}
	// The common term alone recalls dozens of lines; the floor is what keeps
	// them from filling the page.
	if len(response.Matches) > 5 {
		t.Fatalf("the floor let %d lines through; the common term alone should not qualify", len(response.Matches))
	}
}

// An identifier is one term, not the words it is spelled out of.
//
// The segmenter reads an underscore as punctuation, so "chatibs_claw_client"
// used to reach both the index and the query as "chatibs", "claw" and "client".
// A store is full of lines that merely say "client", and every one of them came
// back as an answer to a request for that identifier, with the bare word marked
// as the reason it did.
func TestSearchKeepsAnIdentifierWhole(t *testing.T) {
	b := indexedBackend(t, map[string]string{
		"paths.md": padded("cwd: /srv/guangfa/chatibsagent/chatibs_claw_client\n"),
		"other.md": padded("the AgentEngineCompatClient is the client for this service\n"),
	})
	response := searchAll(t, b, "chatibs_claw_client")
	if len(response.Matches) != 1 {
		t.Fatalf("matches = %d, want 1: %+v", len(response.Matches), response.Matches)
	}
	match := response.Matches[0]
	if match.Path != "paths.md" {
		t.Fatalf("path = %q, want paths.md", match.Path)
	}
	if len(match.MatchedTerms) != 1 || match.MatchedTerms[0] != "chatibs_claw_client" {
		t.Fatalf("matched terms = %q, want the identifier whole", match.MatchedTerms)
	}
}

// A hyphenated word is one term too, and a bare part of it answers nothing.
//
// "read-only" is one adjective, not the two words "read" and "only". Cut at the
// hyphen, the query asked for "read" — a word so common that a memory about a
// filename called "read_file" came back as the answer to a question about
// mount permissions, with that "read" marked as the reason it did.
func TestSearchKeepsAHyphenatedWordWhole(t *testing.T) {
	b := indexedBackend(t, map[string]string{
		"mounts.md": padded("the module cache is mounted read-only\n"),
		"naming.md": padded("do not invent a filename: read_file does not exist\n"),
	})
	response := searchAll(t, b, "isolated module cache read-only permission denied")
	if len(response.Matches) != 1 {
		t.Fatalf("matches = %d, want only the read-only line: %+v", len(response.Matches), response.Matches)
	}
	match := response.Matches[0]
	if match.Path != "mounts.md" {
		t.Fatalf("path = %q, want mounts.md", match.Path)
	}
	for _, term := range match.MatchedTerms {
		if term == "read" || term == "only" {
			t.Fatalf("matched terms = %q, want the hyphenated word whole", match.MatchedTerms)
		}
	}
	if len(MatchRanges("read_file does not exist", match.MatchedTerms)) != 0 {
		t.Fatalf("matched terms %q mark a word inside read_file", match.MatchedTerms)
	}
}

// A compound word answers to itself and not to its parts. A store is full of
// lines that merely say "task" or "outcome", and a reader asking for one of
// those is not asking about every line that names "task_outcome".
func TestSearchDoesNotAnswerAWordWithACompoundThatContainsIt(t *testing.T) {
	b := indexedBackend(t, map[string]string{
		"run.md": padded("task_outcome: success\n"),
	})
	if response := searchAll(t, b, "task outcome"); len(response.Matches) != 0 {
		t.Fatalf("matches = %+v, want none: a part is not the compound", response.Matches)
	}
	response := searchAll(t, b, "task_outcome")
	if len(response.Matches) != 1 {
		t.Fatalf("matches = %d, want the line naming it: %+v", len(response.Matches), response.Matches)
	}
	if got := response.Matches[0].MatchedTerms; len(got) != 1 || got[0] != "task_outcome" {
		t.Fatalf("matched terms = %q, want the compound whole", got)
	}
}

// A dotted word is one term: "8.4.5" is a version, not the numbers 8, 4 and 5.
//
// The dot used to split the query and every indexed line, so a search for one
// release answered with every line that merely held an 8, a 4 or a 5.
func TestSearchKeepsADottedWordWhole(t *testing.T) {
	b := indexedBackend(t, map[string]string{
		"release.md": padded("the fix shipped in 8.4.5.\n"),
		"counts.md":  padded("8 retries, 4 workers and 5 shards\n"),
		"later.md":   padded("upgrade to 18.4.5 or 8.4.50\n"),
	})
	response := searchAll(t, b, "8.4.5")
	if len(response.Matches) != 1 {
		t.Fatalf("matches = %d, want only the line naming 8.4.5: %+v", len(response.Matches), response.Matches)
	}
	match := response.Matches[0]
	if match.Path != "release.md" {
		t.Fatalf("path = %q, want release.md", match.Path)
	}
	if len(match.MatchedTerms) != 1 || match.MatchedTerms[0] != "8.4.5" {
		t.Fatalf("matched terms = %q, want the version whole", match.MatchedTerms)
	}
}

// The mark is drawn by the same rule: a part of a dotted word is not marked,
// and the dot that ends a sentence is not part of the word before it.
func TestMatchRangesKeepsADottedWordWhole(t *testing.T) {
	text := "8.4.5 replaced 8."
	if got := MatchRanges(text, []string{"8"}); len(got) != 1 || text[got[0][0]:got[0][1]] != "8" || got[0][0] != 15 {
		t.Fatalf("ranges = %v, want only the lone 8", got)
	}
	if got := MatchRanges(text, []string{"8.4.5"}); len(got) != 1 || got[0] != [2]int{0, 5} {
		t.Fatalf("ranges = %v, want the version", got)
	}
	if got := MatchRanges(text, []string{"4"}); len(got) != 0 {
		t.Fatalf("ranges = %v, want none inside the version", got)
	}
}

// Japanese is cut into its words by the Japanese dictionary, so a word finds
// the line it is written in, whatever it is written against: "デプロイ" is a
// word of "確認してからデプロイします", not of some longer blob.
func TestSearchFindsAJapaneseWordInsideASentence(t *testing.T) {
	b := indexedBackend(t, map[string]string{
		"deploy.md": padded("東京都の天気予報を確認してからデプロイします。\n"),
		"review.md": padded("承認プロセスは自動リファクタリングの前に必要です\n"),
	})
	response := searchAll(t, b, "デプロイ")
	if len(response.Matches) != 1 || response.Matches[0].Path != "deploy.md" {
		t.Fatalf("matches = %+v, want the line that deploys", response.Matches)
	}
	if got := response.Matches[0].MatchedTerms; len(got) != 1 || got[0] != "デプロイ" {
		t.Fatalf("matched terms = %q, want デプロイ", got)
	}
	response = searchAll(t, b, "リファクタリングの承認")
	if len(response.Matches) != 1 || response.Matches[0].Path != "review.md" {
		t.Fatalf("matches = %+v, want the line about approval, and no line for a bare particle", response.Matches)
	}
}

// Korean finds a word inside the longer word its particle is attached to:
// "검색" is what a reader asks for, and "검색에서" is how a line says it.
func TestSearchFindsAKoreanWordUnderItsParticle(t *testing.T) {
	b := indexedBackend(t, map[string]string{
		"search.md": padded("메모리 검색에서 한국어 문장을 찾을 수 없는 문제를 수정했습니다\n"),
		"other.md":  padded("배포 전에 승인 절차가 필요합니다\n"),
	})
	response := searchAll(t, b, "검색")
	if len(response.Matches) != 1 || response.Matches[0].Path != "search.md" {
		t.Fatalf("matches = %+v, want the line that searches", response.Matches)
	}
}

// Rows written under older term-splitting rules are rebuilt, not trusted.
//
// The index is derived from the files and from the rules that cut a line into
// terms. A file nobody has touched since the rules changed still holds rows
// spelling terms the query no longer asks for, and leaving them there answers
// "your memory holds nothing about this" for a memory that plainly does.
func TestSearchRebuildsRowsFromAnOlderTermsRevision(t *testing.T) {
	db := testSearchIndexDB(t)
	root := filepath.Join(t.TempDir(), "memories")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "paths.md"), []byte(padded("cwd: /srv/chatibs_claw_client\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	b := New(root).WithSearchIndex(db)
	searchAll(t, b, "chatibs_claw_client")

	// Put the store back the way an older revision left it: the identifier
	// shattered into words, recorded as current.
	if _, err := db.Exec(`UPDATE fb_memory_fts SET terms = 'cwd srv chatibs claw client' WHERE path = 'paths.md' AND line_no = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE fb_memory_index_files SET terms_revision = ? WHERE path = 'paths.md'`, TermsRevision-1); err != nil {
		t.Fatal(err)
	}
	response := searchAll(t, b, "chatibs_claw_client")
	if len(response.Matches) != 1 || response.Matches[0].MatchLineNumber != 1 {
		t.Fatalf("stale rows were not rebuilt: %+v", response.Matches)
	}
}

// Re-indexing one file drops only that file's rows. The file row records the
// rowid range its lines occupy, so a refresh deletes that range instead of
// scanning the whole FTS table for its (root, path) — which for a real folder
// meant one table scan per file on every search. The lines of every other file
// must be exactly where they were.
func TestReindexingOneFileDropsOnlyItsOwnRows(t *testing.T) {
	ctx := context.Background()
	db := testSearchIndexDB(t)
	idx := newSearchIndex(db)
	const root = "/root"

	a := indexedFile{path: "a.md", content: padded("审批浮层说明"), size: 100, modTime: time.Unix(1, 0)}
	b := indexedFile{path: "b.md", content: padded("部署流水线说明"), size: 100, modTime: time.Unix(1, 0)}
	if err := idx.sync(ctx, root, []indexedFile{a, b}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	rangeOf := func(path string) (first, count int64) {
		t.Helper()
		if err := db.QueryRow(`SELECT first_rowid, line_count FROM fb_memory_index_files WHERE root = ? AND path = ?`, root, path).Scan(&first, &count); err != nil {
			t.Fatalf("range of %s: %v", path, err)
		}
		return first, count
	}
	_, bCount := rangeOf("b.md")
	if bCount == 0 {
		t.Fatal("fixture indexed no lines of b.md")
	}

	// Rewrite one file and re-index the whole root.
	a.content, a.size, a.modTime = padded("完全不同的内容"), 200, time.Unix(2, 0)
	if err := idx.sync(ctx, root, []indexedFile{a, b}); err != nil {
		t.Fatalf("re-sync: %v", err)
	}

	bFirst, bCountAfter := rangeOf("b.md")
	var inRange int
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_memory_fts WHERE rowid >= ? AND rowid < ?`, bFirst, bFirst+bCountAfter).Scan(&inRange); err != nil {
		t.Fatal(err)
	}
	if int64(inRange) != bCountAfter {
		t.Fatalf("b.md's range holds %d of its %d rows after a.md was re-indexed", inRange, bCountAfter)
	}
	// The row count is exactly the sum of the files' ranges, so no row of the
	// rewritten file was left behind and none of another file's moved.
	var total, sum int
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_memory_fts`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT line_count FROM fb_memory_index_files WHERE root = ?`, root)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		sum += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if total != sum {
		t.Fatalf("FTS holds %d rows but files claim %d; a refresh leaked rows", total, sum)
	}
	// The rewritten file answers for its new content, and b.md still for its own.
	if _, err := idx.signals(ctx, root, []string{"完全不同的内容"}); err != nil {
		t.Fatal(err)
	}
	if sigs, err := idx.signals(ctx, root, []string{"部署流水线说明"}); err != nil {
		t.Fatal(err)
	} else if _, ok := sigs["b.md"]; !ok {
		t.Fatal("b.md stopped being recalled after another file was re-indexed")
	}
}

// TestConcurrentReindexesOfOneRootLeaveNoOrphanRows pins that two searches of
// the same root — the terminal and the gateway share the index — re-index a
// changed file once between them. Each plans from what the index holds inside
// its own write transaction; planning from a read taken before it let the
// second re-index over the first's fresh rows without deleting them, rows no
// file record points at that every later search would return.
func TestConcurrentReindexesOfOneRootLeaveNoOrphanRows(t *testing.T) {
	ctx := context.Background()
	db := testSearchIndexDB(t)
	const root = "/root"
	a := indexedFile{path: "a.md", content: padded("审批浮层说明"), size: 100, modTime: time.Unix(1, 0)}
	if err := newSearchIndex(db).sync(ctx, root, []indexedFile{a}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	// Enough lines that one re-index is still writing while the others plan.
	a.content, a.size, a.modTime = strings.Repeat("完全不同的内容 部署流水线说明\n", 2000), 200, time.Unix(2, 0)
	const writers = 8
	start := make(chan struct{})
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		go func() {
			<-start
			errs <- newSearchIndex(db).sync(ctx, root, []indexedFile{a})
		}()
	}
	close(start)
	for i := 0; i < writers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent sync: %v", err)
		}
	}
	var total, claimed int
	if err := db.QueryRow(`SELECT COUNT(*) FROM fb_memory_fts`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT line_count FROM fb_memory_index_files WHERE root = ? AND path = ?`, root, a.path).Scan(&claimed); err != nil {
		t.Fatal(err)
	}
	if total != claimed {
		t.Fatalf("FTS holds %d rows but a.md claims %d; a concurrent re-index leaked rows", total, claimed)
	}
}
