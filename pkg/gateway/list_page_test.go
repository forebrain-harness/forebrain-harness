package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadListQueryClampsPageAndSize(t *testing.T) {
	read := func(rawURL string) listQuery {
		return readListQuery(httptest.NewRequest(http.MethodGet, rawURL, nil))
	}

	// A request that carries no "page" is not paged: the non-table callers
	// reading the same endpoints keep the answer they always got.
	q := read("/api/projects?page_size=0&order=up&q=%20%20needle%20")
	require.False(t, q.Paged)
	require.Equal(t, 1, q.Page)
	require.Equal(t, 10, q.PageSize)
	require.Equal(t, "", q.Order)
	require.Equal(t, "needle", q.Q)

	// "page" present: paged, and anything below 1 is the first page.
	for _, rawURL := range []string{"/api/projects?page=0", "/api/projects?page=1"} {
		q = read(rawURL)
		require.True(t, q.Paged, rawURL)
		require.Equal(t, 1, q.Page, rawURL)
	}

	q = read("/api/projects?page_size=1000")
	require.Equal(t, 100, q.PageSize)

	q = read("/api/projects?page=9&page_size=25&sort=created&order=desc")
	require.Equal(t, 9, q.Page)
	require.Equal(t, 25, q.PageSize)
	require.Equal(t, "created", q.Sort)
	require.Equal(t, "desc", q.Order)
}

func TestPageOfServesLastPageWhenPastTheEnd(t *testing.T) {
	items := make([]int, 25)
	for index := range items {
		items[index] = index
	}

	page, servedPage := pageOf(items, listQuery{Page: 9, PageSize: 10})
	require.Equal(t, 3, servedPage)
	require.Equal(t, items[20:25], page)

	// An empty list serves page 1 and stays a non-nil empty slice, so the
	// answer is [] rather than null.
	empty, emptyPage := pageOf([]string{}, listQuery{Page: 4, PageSize: 10})
	require.Equal(t, 1, emptyPage)
	require.NotNil(t, empty)
	require.Empty(t, empty)
}

func TestWriteListPageKeepsTheListKey(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeListPage(recorder, "rows", []string{"a", "b"}, 2, 1, listQuery{Page: 1, PageSize: 10}, map[string]any{"scope": "global"})

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	rows, ok := body["rows"].([]any)
	require.True(t, ok)
	require.Len(t, rows, 2)
	require.EqualValues(t, 2, body["total"])
	require.EqualValues(t, 1, body["page"])
	require.EqualValues(t, 10, body["page_size"])
	require.Equal(t, "global", body["scope"])
}
