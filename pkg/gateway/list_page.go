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
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
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
