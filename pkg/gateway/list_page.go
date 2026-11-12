package gateway

// list_page.go is the one request/answer shape that every list endpoint the web
// pages page through follows (owner 2026-10-18: every table is paged on the
// server, after its search, filters and order are applied — the browser never
// cuts pages itself).
//
// The contract, held by each list endpoint:
//
//   - it reads q, sort, order, page and page_size, plus its own filters;
//   - it filters, then orders, then slices, all on the server;
//   - it answers under the endpoint's own list key (files, projects, sessions,
//     servers, …) and adds total, page and page_size beside it;
//   - a page past the end serves the last page, so deleting the last row of the
//     last page lands on the page before it instead of an empty table;
//   - only a request carrying "page" is paged (Paged). Without it the endpoint
//     keeps the answer it always gave: the chat project picker, the folder
//     dialog and other non-table callers read these endpoints too and must not
//     suddenly get ten rows.
//
// A list in memory slices with pageOf; a list in the database uses LIMIT/OFFSET
// with a COUNT(*) over the same WHERE clause.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
)

// listQuery is one list request's search, order and page, read the same way by
// every list endpoint the web pages page through.
type listQuery struct {
	Q        string // trimmed "q"
	Sort     string // "sort": an endpoint-defined key; "" = the endpoint's default
	Order    string // "asc" | "desc"; "" = the default direction of Sort
	Page     int    // 1-based
	PageSize int
	// Paged is true only when the request carried "page". Without it an
	// endpoint keeps its old answer (the whole list, or its old limit/offset
	// window): the chat project picker, the folder dialog and other non-table
	// callers read these endpoints too and must not suddenly get 10 rows.
	Paged bool
}

const (
	listDefaultPageSize = 10
	listMaxPageSize     = 100
)

// readListQuery reads q, sort, order, page and page_size. page < 1 → 1;
// page_size < 1 → listDefaultPageSize; page_size > listMaxPageSize →
// listMaxPageSize; order other than "asc"/"desc" → "".
func readListQuery(r *http.Request) listQuery {
	query := r.URL.Query()
	q := listQuery{
		Q:        strings.TrimSpace(query.Get("q")),
		Sort:     strings.TrimSpace(query.Get("sort")),
		Page:     1,
		PageSize: listDefaultPageSize,
	}
	if order := strings.TrimSpace(query.Get("order")); order == "asc" || order == "desc" {
		q.Order = order
	}
	if raw := strings.TrimSpace(query.Get("page")); raw != "" {
		q.Paged = true
		if page, err := strconv.Atoi(raw); err == nil && page > 1 {
			q.Page = page
		}
	}
	if size, err := strconv.Atoi(strings.TrimSpace(query.Get("page_size"))); err == nil && size > 0 {
		q.PageSize = min(size, listMaxPageSize)
	}
	return q
}

// desc reports whether the order is descending, given the sort key's own
// default (e.g. times default to newest first, names to A→Z).
func (q listQuery) desc(defaultDesc bool) bool {
	switch q.Order {
	case "asc":
		return false
	case "desc":
		return true
	}
	return defaultDesc
}

// pageOf returns the requested page of items and the page actually served:
// a page past the end serves the last page (so a delete that empties the last
// page lands on the one before it instead of an empty table). The served slice
// keeps the emptiness of items: an empty non-nil list stays non-nil.
func pageOf[T any](items []T, q listQuery) (page []T, servedPage int) {
	size := q.PageSize
	if size < 1 {
		size = listDefaultPageSize
	}
	total := len(items)
	pages := (total + size - 1) / size
	if pages < 1 {
		pages = 1
	}
	served := q.Page
	if served < 1 {
		served = 1
	}
	if served > pages {
		served = pages
	}
	start := (served - 1) * size
	if start > total {
		start = total
	}
	end := min(start+size, total)
	return items[start:end], served
}

// writeListPage answers one page: {"<key>": rows, "total": n, "page": p,
// "page_size": s} plus any extra top-level fields the endpoint keeps (pass nil
// for none). page is the page pageOf actually served.
func writeListPage(w http.ResponseWriter, key string, rows any, total int, page int, q listQuery, extra map[string]any) {
	payload := make(map[string]any, 4+len(extra))
	for field, value := range extra {
		payload[field] = value
	}
	payload[key] = rows
	payload["total"] = total
	payload["page"] = page
	payload["page_size"] = q.PageSize
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

// --- the log page (plan 287) ----------------------------------------------
//
// Three read-only endpoints over the event log plans 284–286 write under
// <home>/logs: the records themselves, one model call's request and response,
// and one payload. They live here because this is where every list endpoint
// lives, and pkg/gateway is at the architecture test's production-file cap.
//
// Nothing here reads the disk itself: pkg/telemetry owns the format, and this
// layer only reads query parameters, adds the names (project, session title)
// that come from the state database, and writes the JSON.

const (
	// logListDefaultPageSize is the log table's page: ten records are not a
	// turn, and the page's rows are cheap to carry (only the served page is
	// decoded).
	logListDefaultPageSize = 50
	// logProjectWindow and logProjectScanMax bound the project list this
	// endpoint reads to name the rows it serves. The store pages its list and
	// its window is capped at 500 rows; an install has few projects, so this is
	// one read in practice and a bound rather than a promise in the worst case.
	logProjectWindow  = 500
	logProjectScanMax = 5000
	// logBlobDefaultLimit and logBlobMaxLimit bound the bytes one payload read
	// returns: a quarter of a megabyte by default, a megabyte at most.
	logBlobDefaultLimit = 256 * 1024
	logBlobMaxLimit     = 1024 * 1024
)

// logLevelNames is the API's level vocabulary, the same four the writer stores.
var logLevelNames = []string{"DEBUG", "INFO", "WARN", "ERROR"}

// logRow is one record as the page reads it: the record itself (its file name's
// facts restored) plus the names the state database knows.
type logRow struct {
	telemetry.EventRow
	T            string `json:"t"`
	ProjectID    string `json:"project_id,omitempty"`
	ProjectName  string `json:"project_name,omitempty"`
	SessionTitle string `json:"session_title,omitempty"`
}

// logExchangeAnswer is one call as the page opens it: the reader's object plus
// the project and session names.
type logExchangeAnswer struct {
	ID         string                     `json:"id"`
	Day        string                     `json:"day"`
	Request    *telemetry.ExchangePart    `json:"request"`
	Response   *telemetry.ExchangeReply   `json:"response"`
	Scope      logExchangeScope           `json:"scope"`
	Summary    *telemetry.ResponseSummary `json:"summary"`
	Concurrent []logConcurrentCall        `json:"concurrent"`
}

// logExchangeScope is the call's scope with the project's name beside its key.
type logExchangeScope struct {
	telemetry.ExchangeScope
	ProjectName  string `json:"project_name,omitempty"`
	SessionTitle string `json:"session_title,omitempty"`
}

// logConcurrentCall is another call in flight at the same time, with its
// session's title.
type logConcurrentCall struct {
	telemetry.ExchangeCall
	SessionTitle string `json:"session_title,omitempty"`
}

// handleLogs serves GET /api/logs: the records of the requested time range and
// dimensions, newest first, one page at a time.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	query := r.URL.Query()
	q := readListQuery(r)
	if strings.TrimSpace(query.Get("page_size")) == "" {
		// The log table asks for 50 rows unless it says otherwise; readListQuery
		// carries the list-wide default of 10.
		q.PageSize = logListDefaultPageSize
	}
	// Truncated to the second: the page shows bucket starts, and a default
	// window should not carry the clock's nanoseconds into every one of them.
	now := time.Now().Truncate(time.Second)
	to, err := logTime(query.Get("to"), now)
	if err != nil {
		http.Error(w, "to: "+err.Error(), http.StatusBadRequest)
		return
	}
	from, err := logTime(query.Get("from"), to.Add(-time.Hour))
	if err != nil {
		http.Error(w, "from: "+err.Error(), http.StatusBadRequest)
		return
	}
	if from.After(to) {
		http.Error(w, "from is after to", http.StatusBadRequest)
		return
	}
	levels, err := logLevels(query.Get("level"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sources, err := logSources(query.Get("source"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// The records carry the project's key, the request names its id: the store
	// translates, and an id that names no project here matches nothing.
	project, known, err := s.logProjectKey(r.Context(), strings.TrimSpace(query.Get("project")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !known {
		writeListPage(w, "records", []logRow{}, 0, 1, q, map[string]any{
			"level_counts": emptyLogLevelCounts(),
			"buckets":      []telemetry.EventBucket{},
			"truncated":    false,
		})
		return
	}
	page, err := telemetry.QueryEvents(s.Home, telemetry.EventQuery{
		From: from, To: to,
		Levels: levels, Sources: sources,
		Project: project,
		Session: strings.TrimSpace(query.Get("session")),
		Run:     strings.TrimSpace(query.Get("run")),
		Kind:    strings.TrimSpace(query.Get("kind")),
		Text:    q.Q,
		Page:    q.Page, PageSize: q.PageSize,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rows, err := s.logRows(r.Context(), page.Rows)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeListPage(w, "records", rows, page.Total, page.Page, q, map[string]any{
		"level_counts": page.LevelCounts,
		"buckets":      page.Buckets,
		"truncated":    page.Truncated,
	})
}

// handleLogExchange serves GET /api/logs/exchange: each of one call's two
// records, its payload names, a summary of what came back and the calls that
// were in flight with it.
func (s *Server) handleLogExchange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	query := r.URL.Query()
	exchange, err := telemetry.ReadExchange(s.Home,
		strings.TrimSpace(query.Get("day")), strings.TrimSpace(query.Get("id")))
	switch {
	case errors.Is(err, telemetry.ErrBadLogRequest):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, fs.ErrNotExist):
		http.Error(w, "no such call", http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	projects, err := s.logProjectsByKey(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	answer := logExchangeAnswer{
		ID: exchange.ID, Day: exchange.Day,
		Request: exchange.Request, Response: exchange.Response,
		Summary: exchange.Summary,
		Scope:   logExchangeScope{ExchangeScope: exchange.Scope},
		// An empty list, not null: the page draws these as a row.
		Concurrent: make([]logConcurrentCall, 0, len(exchange.Concurrent)),
	}
	if project, ok := projects[exchange.Scope.Project]; ok {
		answer.Scope.ProjectName = project.Name
	}
	// One call and the calls around it: a handful of distinct sessions, looked up
	// once each. This call's own project name comes from the same index.
	ids := make([]string, 0, len(exchange.Concurrent)+1)
	ids = append(ids, exchange.Scope.Session)
	for _, call := range exchange.Concurrent {
		ids = append(ids, call.Session)
	}
	titles := s.logSessionTitles(r.Context(), ids)
	answer.Scope.SessionTitle = titles[exchange.Scope.Session]
	for _, call := range exchange.Concurrent {
		answer.Concurrent = append(answer.Concurrent, logConcurrentCall{
			ExchangeCall: call, SessionTitle: titles[call.Session],
		})
	}
	writeJSON(w, answer)
}

// handleLogBlob serves GET /api/logs/blob: one payload file, decompressed, as a
// byte range by default or whole with download=1.
func (s *Server) handleLogBlob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	query := r.URL.Query()
	name := strings.TrimSpace(query.Get("name"))
	reader, total, err := telemetry.OpenBlob(s.Home, strings.TrimSpace(query.Get("day")), name)
	switch {
	case errors.Is(err, telemetry.ErrBadLogRequest):
		// The name or the day is not a payload's: refused before anything was
		// opened, which is the only answer a malformed name gets.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, fs.ErrNotExist):
		http.Error(w, "no such payload", http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = reader.Close() }()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if total >= 0 {
		// The size of the whole payload, whatever window this answer carries: the
		// page shows "40 of 1176204 bytes".
		w.Header().Set("X-Log-Blob-Bytes", strconv.FormatInt(total, 10))
	}
	if query.Get("download") == "1" {
		w.Header().Set("Content-Disposition",
			`attachment; filename="`+strings.TrimSuffix(name, ".gz")+`.txt"`)
		// An unfinished stream ends in an error the client sees as a truncated
		// body; what arrived is the answer.
		_, _ = io.Copy(w, reader)
		return
	}
	offset := logBlobWindow(query.Get("offset"), 0, 0, total)
	limit := logBlobWindow(query.Get("limit"), logBlobDefaultLimit, 1, logBlobMaxLimit)
	if offset > 0 {
		// A gzip stream cannot seek: the bytes before the window are read and
		// dropped.
		_, _ = io.CopyN(io.Discard, reader, offset)
	}
	_, _ = io.CopyN(w, reader, limit)
}

// logRows renders one page of records: the names of the projects and sessions on
// that page, and nothing else. Only the served page is looked up — a page of
// records does not name every session a week of logs mentions.
func (s *Server) logRows(ctx context.Context, rows []telemetry.EventRow) ([]logRow, error) {
	projects, err := s.logProjectsByKey(ctx)
	if err != nil {
		return nil, err
	}
	sessions := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Session != "" {
			sessions = append(sessions, row.Session)
		}
	}
	titles := s.logSessionTitles(ctx, sessions)
	out := make([]logRow, 0, len(rows))
	for _, row := range rows {
		item := logRow{
			EventRow:     row,
			T:            telemetry.FormatEventTime(row.Time),
			SessionTitle: titles[row.Session],
		}
		if project, ok := projects[row.Project]; ok {
			item.ProjectID, item.ProjectName = project.ID, project.Name
		}
		out = append(out, item)
	}
	return out, nil
}

// logSessionTitles names the conversations it was given, one lookup per distinct
// id. A server with no session store, and a conversation the store does not
// know, both leave the title empty: a log record outlives the session it
// belonged to, and that is not an error.
func (s *Server) logSessionTitles(ctx context.Context, ids []string) map[string]string {
	titles := make(map[string]string, len(ids))
	if s.Sessions == nil {
		return titles
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, seen := titles[id]; seen {
			continue
		}
		// The same store call the conversation page makes when it names one
		// session (handleChatSession).
		title, err := s.Sessions.SessionTitle(ctx, id)
		if err != nil {
			continue
		}
		titles[id] = title
	}
	return titles
}

// logProjectsByKey reads the projects of the active agent as two lookups at
// once: the rows carry a project KEY, and the page shows the project's id and
// name, so the list is indexed by that key. The store offers paged reads only,
// so this walks its pages (one in any install) rather than adding a query.
func (s *Server) logProjectsByKey(ctx context.Context) (map[string]state.Project, error) {
	store := s.projectStore()
	if store == nil {
		return nil, nil
	}
	projects := map[string]state.Project{}
	for offset := 0; offset < logProjectScanMax; {
		window := min(logProjectWindow, logProjectScanMax-offset)
		page, total, err := store.ListProjectsPage(ctx, state.ListProjectsOptions{}, window, offset)
		if err != nil {
			return nil, err
		}
		for _, project := range page {
			projects[project.ProjectKey] = project
		}
		offset += len(page)
		if len(page) == 0 || offset >= total {
			break
		}
	}
	return projects, nil
}

// logProjectKey is the "project" parameter as the records spell it. An empty
// parameter means every project; known=false means the id names no project this
// gateway serves, which the page shows as no rows rather than as a bad request.
func (s *Server) logProjectKey(ctx context.Context, id string) (key string, known bool, err error) {
	if id == "" {
		return "", true, nil
	}
	store := s.projectStore()
	if store == nil {
		return "", false, nil
	}
	project, err := store.Get(ctx, id)
	if errors.Is(err, state.ErrProjectNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if project.ProjectKey == "" {
		return "", false, nil
	}
	return project.ProjectKey, true, nil
}

// logTime parses one bound of the range, falling back to the default when the
// parameter is absent.
func logTime(raw string, fallback time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, errors.New("not an RFC3339 time")
	}
	return at, nil
}

// logLevels reads the comma-separated levels the page wants. Absent means the
// three a person reads by default: DEBUG is opt-in.
func logLevels(raw string) ([]string, error) {
	levels := make([]string, 0, len(logLevelNames))
	for _, field := range strings.Split(raw, ",") {
		level := strings.ToUpper(strings.TrimSpace(field))
		if level == "" {
			continue
		}
		if !slices.Contains(logLevelNames, level) {
			return nil, errors.New("unknown level " + strconv.Quote(level))
		}
		levels = append(levels, level)
	}
	if len(levels) == 0 {
		return []string{"INFO", "WARN", "ERROR"}, nil
	}
	return levels, nil
}

// logSources reads the comma-separated sources; absent means every source.
func logSources(raw string) ([]string, error) {
	sources := make([]string, 0, 3)
	for _, field := range strings.Split(raw, ",") {
		source := strings.TrimSpace(field)
		if source == "" {
			continue
		}
		if source != "tui" && source != "gateway" && source != "cli" {
			return nil, errors.New("unknown source " + strconv.Quote(source))
		}
		sources = append(sources, source)
	}
	return sources, nil
}

// logBlobWindow reads one byte window parameter, clamped to what this endpoint
// serves: an offset past the payload is the payload's end, and a limit is never
// larger than one megabyte.
func logBlobWindow(raw string, fallback, low, high int64) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < low {
		return fallback
	}
	return min(value, high)
}

// emptyLogLevelCounts is the tally of a query that matched nothing: the four
// levels at zero, so the page's level buttons keep their shape.
func emptyLogLevelCounts() map[string]int {
	counts := make(map[string]int, len(logLevelNames))
	for _, level := range logLevelNames {
		counts[level] = 0
	}
	return counts
}
