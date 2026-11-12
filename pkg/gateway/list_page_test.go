package gateway

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
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

// ---- the log page (plan 287) ---------------------------------------------

// logFixture is a gateway on a temporary home that holds records, one project
// and one named conversation.
type logFixture struct {
	server  *Server
	project state.Project
	session string
	day     string
	dayDir  string
}

func newLogFixture(t *testing.T) *logFixture {
	t.Helper()
	root := t.TempDir()
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(root, "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	projects := state.NewProjectStore(db, "main")
	projectRoot := filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(projectRoot, 0o755))
	project, err := projects.Create(ctx, state.CreateProjectInput{
		Name:       "forebrain-harness",
		Root:       projectRoot,
		ProjectKey: state.ProjectKeyForRoot(projectRoot),
	})
	require.NoError(t, err)

	sessions := state.NewSessionStore(db, "main")
	session := "cli-0001"
	require.NoError(t, sessions.Ensure(ctx, session, session))
	require.NoError(t, sessions.SetTitle(ctx, session, "Gemini 工具循环 400 排查"))

	day := time.Now().In(time.Local).Format("2006-01-02")
	fixture := &logFixture{
		server:  &Server{Home: root, Projects: projects, Sessions: sessions},
		project: project,
		session: session,
		day:     day,
		dayDir:  filepath.Join(root, "logs", day),
	}
	require.NoError(t, os.MkdirAll(fixture.dayDir, 0o755))
	return fixture
}

// writeRecords appends records the way a process does.
func (f *logFixture) writeRecords(t *testing.T, records ...telemetry.Record) {
	t.Helper()
	log := telemetry.OpenEventLog(f.server.Home, "gateway")
	t.Cleanup(func() { _ = log.Close() })
	for _, record := range records {
		log.Write(record)
	}
}

// writePayload writes one gzip payload under the day's x/.
func (f *logFixture) writePayload(t *testing.T, name string, body []byte) {
	t.Helper()
	dir := filepath.Join(f.dayDir, "x")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	file, err := os.Create(filepath.Join(dir, name))
	require.NoError(t, err)
	writer := gzip.NewWriter(file)
	_, err = writer.Write(body)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, file.Close())
}

// get calls one handler and returns its recorder.
func (f *logFixture) get(t *testing.T, target string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	return cronRequest(t, f.server, http.MethodGet, target, nil, handler)
}

func TestLogsListsRecordsWithNames(t *testing.T) {
	fixture := newLogFixture(t)
	at := time.Now().Add(-5 * time.Minute).Truncate(time.Millisecond)
	fixture.writeRecords(t,
		telemetry.Record{Time: at, Level: "INFO", Kind: "log", Msg: "a record with a project", Project: fixture.project.ProjectKey, Session: fixture.session},
		telemetry.Record{Time: at.Add(time.Second), Level: "WARN", Kind: "llm", Msg: "a record without one"},
	)

	recorder := fixture.get(t, "/api/logs", fixture.server.handleLogs)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var body struct {
		Records []struct {
			ID           string         `json:"id"`
			T            string         `json:"t"`
			Level        string         `json:"lvl"`
			Kind         string         `json:"kind"`
			Msg          string         `json:"msg"`
			Source       string         `json:"source"`
			Day          string         `json:"day"`
			Project      string         `json:"project"`
			ProjectID    string         `json:"project_id"`
			ProjectName  string         `json:"project_name"`
			Session      string         `json:"session"`
			SessionTitle string         `json:"session_title"`
			Attrs        map[string]any `json:"attrs"`
		} `json:"records"`
		Total       int            `json:"total"`
		Page        int            `json:"page"`
		PageSize    int            `json:"page_size"`
		LevelCounts map[string]int `json:"level_counts"`
		Buckets     []struct {
			Debug int `json:"debug"`
			Info  int `json:"info"`
			Warn  int `json:"warn"`
			Error int `json:"error"`
		} `json:"buckets"`
		Truncated bool `json:"truncated"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, 2, body.Total)
	require.Len(t, body.Records, 2)
	require.Equal(t, 1, body.Page)
	// The log table's own default page, not the list-wide ten.
	require.Equal(t, 50, body.PageSize)
	require.Equal(t, map[string]int{"DEBUG": 0, "INFO": 1, "WARN": 1, "ERROR": 0}, body.LevelCounts)
	require.Len(t, body.Buckets, 60)
	require.False(t, body.Truncated)

	second := body.Records[0]
	require.Equal(t, "a record without one", second.Msg)
	require.Equal(t, "gateway", second.Source)
	require.Equal(t, fixture.day, second.Day)
	require.NotEmpty(t, second.ID)
	require.NotEmpty(t, second.T)
	require.Equal(t, "WARN", second.Level)
	require.Equal(t, "llm", second.Kind)

	first := body.Records[1]
	require.Equal(t, fixture.project.ProjectKey, first.Project)
	require.Equal(t, fixture.project.ID, first.ProjectID)
	require.Equal(t, "forebrain-harness", first.ProjectName)
	require.Equal(t, fixture.session, first.Session)
	require.Equal(t, "Gemini 工具循环 400 排查", first.SessionTitle)

	// A session the store never saw keeps an empty title, not an error.
	fixture.writeRecords(t, telemetry.Record{Time: at.Add(2 * time.Second), Level: "INFO", Kind: "log", Msg: "orphan", Session: "cli-gone"})
	recorder = fixture.get(t, "/api/logs", fixture.server.handleLogs)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, 3, body.Total)
	require.Equal(t, "orphan", body.Records[0].Msg)
	require.Equal(t, "cli-gone", body.Records[0].Session)
	require.Empty(t, body.Records[0].SessionTitle)
}

func TestLogsProjectFilterTakesTheProjectID(t *testing.T) {
	fixture := newLogFixture(t)
	at := time.Now().Add(-5 * time.Minute)
	fixture.writeRecords(t,
		telemetry.Record{Time: at, Level: "INFO", Kind: "log", Msg: "ours", Project: fixture.project.ProjectKey},
		telemetry.Record{Time: at.Add(time.Second), Level: "INFO", Kind: "log", Msg: "somebody else's", Project: "prj_some_other_root"},
	)

	recorder := fixture.get(t, "/api/logs?project="+fixture.project.ID, fixture.server.handleLogs)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var body struct {
		Records []struct {
			Msg     string `json:"msg"`
			Project string `json:"project"`
		} `json:"records"`
		Total int `json:"total"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, 1, body.Total)
	require.Equal(t, "ours", body.Records[0].Msg)
	require.Equal(t, fixture.project.ProjectKey, body.Records[0].Project)

	// A project id this install does not have is an empty page, not a refusal.
	recorder = fixture.get(t, "/api/logs?project=prj_does_not_exist", fixture.server.handleLogs)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), `"total":0`)
	require.Contains(t, recorder.Body.String(), `"records":[]`)
}

func TestLogsRejectsBadParameters(t *testing.T) {
	fixture := newLogFixture(t)
	for _, target := range []string{
		"/api/logs?from=2026-11-11T12:00:00Z&to=2026-11-11T11:00:00Z",
		"/api/logs?level=LOUD",
		"/api/logs?from=abc",
		"/api/logs?to=yesterday",
		"/api/logs?source=carrier-pigeon",
	} {
		recorder := fixture.get(t, target, fixture.server.handleLogs)
		require.Equal(t, http.StatusBadRequest, recorder.Code, target)
		require.NotEmpty(t, strings.TrimSpace(recorder.Body.String()), target)
	}
}

func TestLogsRejectsNonGet(t *testing.T) {
	fixture := newLogFixture(t)
	handlers := map[string]http.HandlerFunc{
		"/api/logs":          fixture.server.handleLogs,
		"/api/logs/exchange": fixture.server.handleLogExchange,
		"/api/logs/blob":     fixture.server.handleLogBlob,
	}
	for target, handler := range handlers {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			recorder := cronRequest(t, fixture.server, method, target, nil, handler)
			require.Equal(t, http.StatusMethodNotAllowed, recorder.Code, "%s %s", method, target)
		}
	}
}

func TestLogExchangeAndBlob(t *testing.T) {
	fixture := newLogFixture(t)
	at := time.Now().Add(-5 * time.Minute).Truncate(time.Millisecond)
	id := "ab12cd-000004"
	requestBody := "POST https://api.example.com/v1/chat/completions HTTP/1.1\ncontent-type: application/json\n\n{\"model\":\"gpt\"}\n"
	responseBody := "HTTP/1.1 200 OK\ncontent-type: text/event-stream\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"shell\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"
	fixture.writePayload(t, id+".req.gz", []byte(requestBody))
	fixture.writePayload(t, id+".resp.gz", []byte(responseBody))
	fixture.writeRecords(t,
		telemetry.Record{Time: at, Level: "DEBUG", Kind: "llm", Msg: "llm request",
			Project: fixture.project.ProjectKey, Session: fixture.session, Agent: "main",
			Attrs: map[string]any{"x": id, "method": "POST", "url": "https://api.example.com/v1/chat/completions",
				"model": "gpt", "req_bytes": len(requestBody)},
			Blobs: map[string]telemetry.BlobRef{"req": {Name: id + ".req.gz", Bytes: int64(len(requestBody)), Whole: true}}},
		telemetry.Record{Time: at.Add(2 * time.Second), Level: "INFO", Kind: "llm", Msg: "llm response",
			Project: fixture.project.ProjectKey, Session: fixture.session, Agent: "main",
			Attrs: map[string]any{"x": id, "status": 200, "outcome": "ok", "dur_ms": 2000,
				"ttfb_ms": 100, "max_gap_ms": 50, "resp_bytes": len(responseBody)},
			Blobs: map[string]telemetry.BlobRef{"resp": {Name: id + ".resp.gz", Bytes: int64(len(responseBody)), Whole: true}}},
	)

	recorder := fixture.get(t, "/api/logs/exchange?day="+fixture.day+"&id="+id, fixture.server.handleLogExchange)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var answer struct {
		ID      string `json:"id"`
		Day     string `json:"day"`
		Request *struct {
			Model  string `json:"model"`
			Method string `json:"method"`
			Bytes  int64  `json:"bytes"`
			Whole  bool   `json:"whole"`
			Blob   string `json:"blob"`
		} `json:"request"`
		Response *struct {
			Status int    `json:"status"`
			DurMs  int64  `json:"dur_ms"`
			Blob   string `json:"blob"`
		} `json:"response"`
		Scope struct {
			Source       string `json:"source"`
			Project      string `json:"project"`
			ProjectName  string `json:"project_name"`
			Session      string `json:"session"`
			SessionTitle string `json:"session_title"`
			Agent        string `json:"agent"`
		} `json:"scope"`
		Summary *struct {
			Text         string `json:"text"`
			FinishReason string `json:"finish_reason"`
			ToolCalls    []struct {
				Name string `json:"name"`
			} `json:"tool_calls"`
		} `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &answer))
	require.Equal(t, id, answer.ID)
	require.Equal(t, fixture.day, answer.Day)
	require.NotNil(t, answer.Request)
	require.Equal(t, "gpt", answer.Request.Model)
	require.Equal(t, "POST", answer.Request.Method)
	require.True(t, answer.Request.Whole)
	require.Equal(t, id+".req.gz", answer.Request.Blob)
	require.NotNil(t, answer.Response)
	require.Equal(t, 200, answer.Response.Status)
	require.EqualValues(t, 2000, answer.Response.DurMs)
	require.Equal(t, fixture.project.ProjectKey, answer.Scope.Project)
	require.Equal(t, "forebrain-harness", answer.Scope.ProjectName)
	require.Equal(t, "Gemini 工具循环 400 排查", answer.Scope.SessionTitle)
	require.Equal(t, "gateway", answer.Scope.Source)
	require.Equal(t, "main", answer.Scope.Agent)
	require.NotNil(t, answer.Summary)
	require.Equal(t, "Hi", answer.Summary.Text)
	require.Equal(t, "tool_calls", answer.Summary.FinishReason)
	require.Len(t, answer.Summary.ToolCalls, 1)
	require.Equal(t, "shell", answer.Summary.ToolCalls[0].Name)

	// A window of the payload: exactly the bytes asked for, and the whole
	// payload's size in the header.
	recorder = fixture.get(t, "/api/logs/blob?day="+fixture.day+"&name="+id+".resp.gz&offset=10&limit=20", fixture.server.handleLogBlob)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, "text/plain; charset=utf-8", recorder.Header().Get("Content-Type"))
	require.Equal(t, strconv.Itoa(len(responseBody)), recorder.Header().Get("X-Log-Blob-Bytes"))
	require.Equal(t, responseBody[10:30], recorder.Body.String())
	require.Empty(t, recorder.Header().Get("Content-Disposition"))

	// download=1: the whole payload as an attachment, named after the file
	// without its .gz.
	recorder = fixture.get(t, "/api/logs/blob?day="+fixture.day+"&name="+id+".resp.gz&download=1", fixture.server.handleLogBlob)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, `attachment; filename="`+id+`.resp.txt"`, recorder.Header().Get("Content-Disposition"))
	require.Equal(t, responseBody, recorder.Body.String())

	// A call the day does not hold is a 404, and a day that is not a day is a 400.
	recorder = fixture.get(t, "/api/logs/exchange?day="+fixture.day+"&id=nope-000001", fixture.server.handleLogExchange)
	require.Equal(t, http.StatusNotFound, recorder.Code)
	recorder = fixture.get(t, "/api/logs/exchange?day=..&id="+id, fixture.server.handleLogExchange)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	recorder = fixture.get(t, "/api/logs/blob?day="+fixture.day+"&name=absent.req.gz", fixture.server.handleLogBlob)
	require.Equal(t, http.StatusNotFound, recorder.Code)
}

func TestLogBlobRejectsTraversal(t *testing.T) {
	fixture := newLogFixture(t)
	for _, target := range []string{
		"/api/logs/blob?day=" + fixture.day + "&name=../../forebrain.yaml.gz",
		"/api/logs/blob?day=" + fixture.day + "&name=..%2Fx.gz",
		"/api/logs/blob?day=" + fixture.day + "&name=sub/x.gz",
		"/api/logs/blob?day=" + fixture.day + "&name=x.txt",
		"/api/logs/blob?day=..&name=x.gz",
		"/api/logs/blob?name=x.gz",
	} {
		recorder := fixture.get(t, target, fixture.server.handleLogBlob)
		require.Equal(t, http.StatusBadRequest, recorder.Code, target)
	}
}
