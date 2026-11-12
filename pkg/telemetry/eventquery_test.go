package telemetry

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/stretchr/testify/require"
)

// testEventLog is a log under a temporary home, closed with the test.
func testEventLog(t *testing.T, root, source string) *EventLog {
	t.Helper()
	log := OpenEventLog(root, source)
	t.Cleanup(func() { _ = log.Close() })
	return log
}

// writeRecords appends records through the real writer, so the files the query
// reads are the ones 284 writes.
func writeRecords(t *testing.T, log *EventLog, records ...Record) {
	t.Helper()
	for _, record := range records {
		log.Write(record)
	}
}

// logDayDir is one day's directory under a test home, created if needed.
func logDayDir(t *testing.T, root string, day time.Time) string {
	t.Helper()
	dir := filepath.Join(root, home.LogsDir, day.In(time.Local).Format(dayLayout))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return dir
}

// recordLine renders a record the way the writer does — "t" first, then the
// encoded object — without the redaction and bounds Write applies.
func recordLine(t *testing.T, record Record) string {
	t.Helper()
	if record.Time.IsZero() {
		record.Time = time.Now()
	}
	body, err := json.Marshal(record)
	require.NoError(t, err)
	return `{"t":"` + record.Time.Format(eventTimeLayout) + `",` + string(body[1:]) + "\n"
}

// writeLines writes one record file of explicit lines.
func writeLines(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(joined(lines)), 0o600))
	return path
}

func joined(lines []string) string {
	var out string
	for _, line := range lines {
		out += line
	}
	return out
}

// writeGzipFile writes a gzip payload under the day's x/ and returns its name.
func writeGzipFile(t *testing.T, root, day, name string, body []byte) string {
	t.Helper()
	dir := filepath.Join(root, home.LogsDir, day, blobDirName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, name)
	file, err := os.Create(path)
	require.NoError(t, err)
	writer := gzip.NewWriter(file)
	_, err = writer.Write(body)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, file.Close())
	return path
}

// noon returns the local noon of a day offset from today, which is inside the
// retention window every query walks.
func noon(daysAgo int) time.Time {
	now := time.Now().In(time.Local)
	return time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.Local).AddDate(0, 0, -daysAgo)
}

// ---- QueryEvents ---------------------------------------------------------

// Every dimension narrows the same set: two sources, two projects, three
// sessions, four levels and three kinds, each asked for on its own.
func TestQueryEventsFiltersByEveryDimension(t *testing.T) {
	root := t.TempDir()
	tui := testEventLog(t, root, "tui")
	gateway := testEventLog(t, root, "gateway")
	at := time.Now().Add(-10 * time.Minute).Truncate(time.Millisecond)

	writeRecords(t, tui,
		Record{Time: at, Level: "INFO", Kind: "log", Msg: "tui a", Project: "prj_root_a", Session: "s-a", Run: "run-1"},
		Record{Time: at.Add(time.Second), Level: "WARN", Kind: "llm", Msg: "tui b", Project: "prj_root_a", Session: "s-b"},
		Record{Time: at.Add(2 * time.Second), Level: "ERROR", Kind: "tool", Msg: "tui c", Project: "prj_root_b", Session: "s-a"},
		Record{Time: at.Add(3 * time.Second), Level: "DEBUG", Kind: "log", Msg: "tui d", Project: "prj_root_b", Session: "s-c"},
	)
	writeRecords(t, gateway,
		Record{Time: at.Add(4 * time.Second), Level: "INFO", Kind: "http", Msg: "gw a", Project: "prj_root_a", Session: "s-a"},
		Record{Time: at.Add(5 * time.Second), Level: "INFO", Kind: "log", Msg: "gw b", Project: "prj_root_b", Session: "s-b"},
	)

	query := func(q EventQuery) EventPage {
		t.Helper()
		q.From, q.To = at.Add(-time.Minute), at.Add(time.Minute)
		page, err := QueryEvents(context.Background(), root, q)
		require.NoError(t, err)
		return page
	}

	// No filter: everything, newest first.
	all := query(EventQuery{})
	require.Equal(t, 6, all.Total)
	require.Len(t, all.Rows, 6)
	require.Equal(t, "gw b", all.Rows[0].Msg)
	require.Equal(t, "tui a", all.Rows[5].Msg)
	require.Equal(t, "tui", all.Rows[5].Source)
	require.Equal(t, "gateway", all.Rows[0].Source)
	require.Equal(t, os.Getpid(), all.Rows[0].PID)
	require.NotEmpty(t, all.Rows[0].Proc)
	require.Equal(t, startOfLocalDay(time.Now()).Format(dayLayout), all.Rows[0].Day)
	require.Contains(t, all.Rows[0].ID, all.Rows[0].Day+"/gateway-")

	require.Equal(t, 4, query(EventQuery{Sources: []string{"tui"}}).Total)
	require.Equal(t, 2, query(EventQuery{Sources: []string{"gateway"}}).Total)
	require.Equal(t, 6, query(EventQuery{Sources: []string{"tui", "gateway", "cli"}}).Total)
	require.Equal(t, 0, query(EventQuery{Sources: []string{"cli"}}).Total)

	require.Equal(t, 3, query(EventQuery{Project: "prj_root_a"}).Total)
	require.Equal(t, 3, query(EventQuery{Project: "prj_root_b"}).Total)
	require.Equal(t, 3, query(EventQuery{Session: "s-a"}).Total)
	require.Equal(t, 2, query(EventQuery{Session: "s-b"}).Total)
	require.Equal(t, 1, query(EventQuery{Session: "s-c"}).Total)
	require.Equal(t, 1, query(EventQuery{Run: "run-1"}).Total)
	require.Equal(t, 3, query(EventQuery{Kind: "log"}).Total)
	require.Equal(t, 1, query(EventQuery{Kind: "llm"}).Total)
	require.Equal(t, 1, query(EventQuery{Kind: "http"}).Total)

	require.Equal(t, 3, query(EventQuery{Levels: []string{"INFO"}}).Total)
	require.Equal(t, 2, query(EventQuery{Levels: []string{"WARN", "ERROR"}}).Total)
	require.Equal(t, 6, query(EventQuery{Levels: []string{"DEBUG", "INFO", "WARN", "ERROR"}}).Total)

	// Dimensions combine.
	combined := query(EventQuery{Project: "prj_root_a", Levels: []string{"INFO"}, Sources: []string{"gateway"}})
	require.Equal(t, 1, combined.Total)
	require.Equal(t, "gw a", combined.Rows[0].Msg)
	require.Equal(t, "s-a", combined.Rows[0].Session)
	require.Equal(t, "http", combined.Rows[0].Kind)
}

// A range that starts on one day and ends on the next reads both days'
// directories and only the records inside the range.
func TestQueryEventsTimeRangeAcrossDays(t *testing.T) {
	root := t.TempDir()
	log := testEventLog(t, root, "tui")
	twoAgo, oneAgo, today := noon(2), noon(1), noon(0)
	yesterdayStart := startOfLocalDay(oneAgo)
	writeRecords(t, log,
		Record{Time: twoAgo, Level: "INFO", Kind: "log", Msg: "two days ago"},
		Record{Time: yesterdayStart.Add(6 * time.Hour), Level: "INFO", Kind: "log", Msg: "yesterday morning"},
		Record{Time: yesterdayStart.Add(18 * time.Hour), Level: "INFO", Kind: "log", Msg: "yesterday evening"},
		Record{Time: startOfLocalDay(today).Add(7 * time.Hour), Level: "INFO", Kind: "log", Msg: "today morning"},
	)

	// Yesterday's noon to today's nine: the evening of one day and the morning
	// of the next, which live in two different directories.
	page, err := QueryEvents(context.Background(), root, EventQuery{
		From: yesterdayStart.Add(12 * time.Hour),
		To:   startOfLocalDay(today).Add(9 * time.Hour),
	})

	require.NoError(t, err)
	require.Equal(t, 2, page.Total)
	require.Equal(t, "today morning", page.Rows[0].Msg)
	require.Equal(t, "yesterday evening", page.Rows[1].Msg)
	require.NotEqual(t, page.Rows[0].Day, page.Rows[1].Day)
}

// 200 records, 50 of them in one millisecond: four pages of 50 concatenate to
// 200 rows with no repeat and no increase in time.
func TestQueryEventsNewestFirstAndStablePaging(t *testing.T) {
	root := t.TempDir()
	log := testEventLog(t, root, "tui")
	base := time.Now().Add(-2 * time.Hour).Truncate(time.Millisecond)
	var records []Record
	for i := 0; i < 200; i++ {
		at := base.Add(time.Duration(i) * time.Millisecond)
		if i >= 120 && i < 170 {
			// Fifty records written in the same millisecond.
			at = base.Add(120 * time.Millisecond)
		}
		records = append(records, Record{Time: at, Level: "INFO", Kind: "log", Msg: fmt.Sprintf("record %03d", i)})
	}
	writeRecords(t, log, records...)

	query := EventQuery{From: base.Add(-time.Minute), To: base.Add(time.Hour), PageSize: 50}
	seen := map[string]bool{}
	var times []time.Time
	for page := 1; page <= 4; page++ {
		query.Page = page
		result, err := QueryEvents(context.Background(), root, query)
		require.NoError(t, err)
		require.Equal(t, 200, result.Total)
		require.Len(t, result.Rows, 50)
		require.Equal(t, page, result.Page)
		for _, row := range result.Rows {
			require.False(t, seen[row.ID], "row %s served twice", row.ID)
			seen[row.ID] = true
			times = append(times, row.Time)
		}
	}
	require.Len(t, seen, 200)
	for i := 1; i < len(times); i++ {
		require.False(t, times[i].After(times[i-1]), "row %d is newer than the one before it", i)
	}
	// A page past the end serves the last one.
	query.Page = 99
	last, err := QueryEvents(context.Background(), root, query)
	require.NoError(t, err)
	require.Equal(t, 4, last.Page)
	require.Len(t, last.Rows, 50)
}

// The level tally ignores the level filter: asking for ERROR alone still says
// how many of each level the same query would return.
func TestQueryEventsLevelCountsIgnoreLevelFilter(t *testing.T) {
	root := t.TempDir()
	log := testEventLog(t, root, "tui")
	at := time.Now().Add(-time.Minute)
	writeRecords(t, log,
		Record{Time: at, Level: "DEBUG", Kind: "log", Msg: "d1"},
		Record{Time: at.Add(time.Millisecond), Level: "DEBUG", Kind: "log", Msg: "d2"},
		Record{Time: at.Add(2 * time.Millisecond), Level: "INFO", Kind: "log", Msg: "i1"},
		Record{Time: at.Add(3 * time.Millisecond), Level: "INFO", Kind: "log", Msg: "i2"},
		Record{Time: at.Add(4 * time.Millisecond), Level: "INFO", Kind: "log", Msg: "i3"},
		Record{Time: at.Add(5 * time.Millisecond), Level: "WARN", Kind: "log", Msg: "w1"},
		Record{Time: at.Add(6 * time.Millisecond), Level: "ERROR", Kind: "log", Msg: "e1"},
	)

	page, err := QueryEvents(context.Background(), root, EventQuery{
		From: at.Add(-time.Minute), To: at.Add(time.Minute),
		Levels: []string{"ERROR"},
	})

	require.NoError(t, err)
	require.Equal(t, 1, page.Total)
	require.Len(t, page.Rows, 1)
	require.Equal(t, "ERROR", page.Rows[0].Level)
	require.Equal(t, map[string]int{"DEBUG": 2, "INFO": 3, "WARN": 1, "ERROR": 1}, page.LevelCounts)
}

// The histogram covers the range: 60 buckets starting at From, and every served
// row counted exactly once.
func TestQueryEventsBucketsCoverTheRange(t *testing.T) {
	root := t.TempDir()
	log := testEventLog(t, root, "tui")
	from := time.Now().Add(-time.Hour).Truncate(time.Second)
	writeRecords(t, log,
		Record{Time: from.Add(30 * time.Second), Level: "INFO", Kind: "log", Msg: "early"},
		Record{Time: from.Add(30*time.Minute + 5*time.Second), Level: "WARN", Kind: "log", Msg: "middle"},
		Record{Time: from.Add(59 * time.Minute), Level: "ERROR", Kind: "log", Msg: "late"},
	)

	page, err := QueryEvents(context.Background(), root, EventQuery{From: from, To: from.Add(time.Hour)})
	require.NoError(t, err)
	require.Len(t, page.Buckets, eventBucketCount)
	require.True(t, page.Buckets[0].T.Equal(from))
	require.Equal(t, 3, page.Total)
	sum := 0
	for _, bucket := range page.Buckets {
		sum += bucket.Debug + bucket.Info + bucket.Warn + bucket.Error
	}
	require.Equal(t, page.Total, sum)
	require.Equal(t, 1, page.Buckets[0].Info)
	require.Equal(t, 1, page.Buckets[30].Warn)
	require.Equal(t, 1, page.Buckets[59].Error)
}

// A host name with dashes (a Kubernetes pod) and a rolled-over part number both
// split from the right.
func TestQueryEventsParsesHostWithDashes(t *testing.T) {
	root := t.TempDir()
	dir := logDayDir(t, root, time.Now())
	at := time.Now().Add(-time.Minute)
	line := recordLine(t, Record{Time: at, Level: "INFO", Kind: "log", Msg: "pod record"})
	writeLines(t, dir, "gateway-gw-pod-7f9c-4117-51be02.jsonl", line)
	writeLines(t, dir, "gateway-gw-pod-7f9c-4117-51be02.1.jsonl",
		recordLine(t, Record{Time: at.Add(time.Second), Level: "INFO", Kind: "log", Msg: "pod record, part 1"}))

	page, err := QueryEvents(context.Background(), root, EventQuery{From: at.Add(-time.Minute), To: at.Add(time.Minute)})
	require.NoError(t, err)
	require.Equal(t, 2, page.Total)
	for _, row := range page.Rows {
		require.Equal(t, "gateway", row.Source)
		require.Equal(t, "gw-pod-7f9c", row.Host)
		require.Equal(t, 4117, row.PID)
		require.Equal(t, "51be02", row.Proc)
	}
	require.Equal(t, "pod record, part 1", page.Rows[0].Msg)
}

// The text filter is a case-insensitive substring of the whole line.
func TestQueryEventsTextIsCaseInsensitive(t *testing.T) {
	root := t.TempDir()
	log := testEventLog(t, root, "tui")
	at := time.Now().Add(-time.Minute)
	writeRecords(t, log,
		Record{Time: at, Level: "INFO", Kind: "log", Msg: "Gemini tool loop 400"},
		Record{Time: at.Add(time.Second), Level: "INFO", Kind: "log", Msg: "something else"},
	)
	page, err := QueryEvents(context.Background(), root, EventQuery{
		From: at.Add(-time.Minute), To: at.Add(time.Minute), Text: "gemini TOOL",
	})

	require.NoError(t, err)
	require.Equal(t, 1, page.Total)
	require.Equal(t, "Gemini tool loop 400", page.Rows[0].Msg)

	// The filter reaches the attributes, which a plain message search would miss.
	page, err = QueryEvents(context.Background(), root, EventQuery{
		From: at.Add(-time.Minute), To: at.Add(time.Minute), Text: "else",
	})

	require.NoError(t, err)
	require.Equal(t, 1, page.Total)
}

// A file with a line that is not JSON and a line past the reader's ceiling still
// answers with the records around them.
func TestQueryEventsSkipsOverlongAndBrokenLines(t *testing.T) {
	root := t.TempDir()
	dir := logDayDir(t, root, time.Now())
	at := time.Now().Add(-time.Minute)
	long := `{"t":"` + at.Format(eventTimeLayout) + `","lvl":"INFO","kind":"log","msg":"` +
		string(bytes.Repeat([]byte("x"), 200*1024)) + `"}` + "\n"
	writeLines(t, dir, "tui-mbp-89572-a3f09c.jsonl",
		recordLine(t, Record{Time: at, Level: "INFO", Kind: "log", Msg: "before the noise"}),
		"this is not a record at all\n",
		long,
		`{"broken":`,
		"\n",
		recordLine(t, Record{Time: at.Add(time.Second), Level: "INFO", Kind: "log", Msg: "after the noise"}),
	)

	page, err := QueryEvents(context.Background(), root, EventQuery{From: at.Add(-time.Minute), To: at.Add(time.Minute)})
	require.NoError(t, err)
	require.Equal(t, 2, page.Total)
	require.Equal(t, "after the noise", page.Rows[0].Msg)
	require.Equal(t, "before the noise", page.Rows[1].Msg)
}

// A process's raw stderr is one record of its own, at the file's mtime, carrying
// the end of what the process said.
func TestQueryEventsSurfacesProcessStderr(t *testing.T) {
	root := t.TempDir()
	dir := logDayDir(t, root, time.Now())
	name := "stderr-gateway-gw-1-4117-51be02.log"
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("panic: boom\n\ngoroutine 1 [running]:\n"), 0o600))
	at := time.Now().Add(-time.Minute)
	log := testEventLog(t, root, "tui")
	writeRecords(t, log, Record{Time: at, Level: "INFO", Kind: "log", Msg: "a normal record"})

	page, err := QueryEvents(context.Background(), root, EventQuery{From: at.Add(-time.Hour), To: time.Now().Add(time.Minute)})
	require.NoError(t, err)
	require.Equal(t, 2, page.Total)
	stderrRow := page.Rows[0]
	require.Equal(t, stderrKind, stderrRow.Kind)
	require.Equal(t, "ERROR", stderrRow.Level)
	require.Equal(t, stderrRecordMsg, stderrRow.Msg)
	require.Equal(t, "gateway", stderrRow.Source)
	require.Equal(t, "gw-1", stderrRow.Host)
	require.Equal(t, 4117, stderrRow.PID)
	require.Contains(t, stderrRow.Attrs["tail"], "goroutine 1 [running]")
	require.EqualValues(t, len("panic: boom\n\ngoroutine 1 [running]:\n"), stderrRow.Attrs["bytes"])
	require.Equal(t, 1, page.LevelCounts["ERROR"])
	// The record still counts the range it was written in, and the stderr file
	// carries no session, so a session filter excludes it.
	require.Equal(t, 0, queryFor(t, root, at, EventQuery{Session: "s-a"}))
}

// queryFor is one QueryEvents over the minute around at.
func queryFor(t *testing.T, root string, at time.Time, q EventQuery) int {
	t.Helper()
	q.From, q.To = at.Add(-time.Hour), time.Now().Add(time.Minute)
	page, err := QueryEvents(context.Background(), root, q)
	require.NoError(t, err)
	return page.Total
}

// The scan stops at its ceiling and says so, so the page can call its total a
// lower bound.
func TestQueryEventsStopsAtScanLimit(t *testing.T) {
	root := t.TempDir()
	log := testEventLog(t, root, "tui")
	at := time.Now().Add(-time.Minute)
	for i := 0; i < 20; i++ {
		writeRecords(t, log, Record{Time: at.Add(time.Duration(i) * time.Millisecond), Level: "INFO", Kind: "log", Msg: "row"})
	}
	previous := eventScanMaxHits
	eventScanMaxHits = 5
	t.Cleanup(func() { eventScanMaxHits = previous })

	page, err := QueryEvents(context.Background(), root, EventQuery{From: at.Add(-time.Minute), To: at.Add(time.Minute)})
	require.NoError(t, err)
	require.True(t, page.Truncated)
	require.Greater(t, page.Total, 0)

	// Under the ceiling the same query is complete.
	eventScanMaxHits = previous
	page, err = QueryEvents(context.Background(), root, EventQuery{From: at.Add(-time.Minute), To: at.Add(time.Minute)})
	require.NoError(t, err)
	require.False(t, page.Truncated)
	require.Equal(t, 20, page.Total)
}

// ---- ReadExchange --------------------------------------------------------

// exchangeRecord is one half of the record pair 286 writes.
func exchangeRecord(at time.Time, msg string, attrs map[string]any, blobs map[string]BlobRef, session string) Record {
	return Record{
		Time: at, Level: "INFO", Kind: kindLLM, Msg: msg,
		Project: "prj_root_a", Session: session, Run: "run-1", Agent: "main",
		Attrs: attrs, Blobs: blobs,
	}
}

func TestReadExchangePairsRequestAndResponse(t *testing.T) {
	root := t.TempDir()
	day := time.Now().In(time.Local).Format(dayLayout)
	at := time.Now().Add(-2 * time.Minute).Truncate(time.Millisecond)
	id := "a3f09c-000391"
	requestBody := "POST https://api.example.com/v1/messages HTTP/1.1\ncontent-type: application/json\n\n{\"model\":\"claude\"}"
	responseBody := "HTTP/1.1 200 OK\ncontent-type: text/event-stream\n\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":11,\"output_tokens\":3}}\n\n"
	writeGzipFile(t, root, day, id+".req.gz", []byte(requestBody))
	writeGzipFile(t, root, day, id+".resp.gz", []byte(responseBody))

	dir := logDayDir(t, root, time.Now())
	writeLines(t, dir, "tui-mbp-4117-a3f09c.jsonl",
		recordLine(t, exchangeRecord(at, "llm request", map[string]any{
			"x": id, "method": "POST", "url": "https://api.example.com/v1/messages",
			"model": "claude", "req_bytes": len(requestBody), "stream": true,
		}, map[string]BlobRef{"req": {Name: id + ".req.gz", Bytes: int64(len(requestBody)), Whole: true}}, "s-a")),
		recordLine(t, exchangeRecord(at.Add(5*time.Second), "llm response", map[string]any{
			"x": id, "method": "POST", "url": "https://api.example.com/v1/messages",
			"model": "claude", "status": 200, "outcome": "ok", "dur_ms": 5195,
			"ttfb_ms": 5190, "max_gap_ms": 5190, "req_bytes": len(requestBody), "resp_bytes": len(responseBody),
		}, map[string]BlobRef{"resp": {Name: id + ".resp.gz", Bytes: int64(len(responseBody)), Whole: true}}, "s-a")),
	)

	exchange, err := ReadExchange(context.Background(), root, day, id)
	require.NoError(t, err)
	require.Equal(t, id, exchange.ID)
	require.Equal(t, day, exchange.Day)
	require.NotNil(t, exchange.Request)
	require.Equal(t, "POST", exchange.Request.Method)
	require.Equal(t, "claude", exchange.Request.Model)
	require.EqualValues(t, len(requestBody), exchange.Request.Bytes)
	require.True(t, exchange.Request.Whole)
	require.Equal(t, id+".req.gz", exchange.Request.Blob)
	require.NotNil(t, exchange.Response)
	require.Equal(t, 200, exchange.Response.Status)
	require.Equal(t, "ok", exchange.Response.Outcome)
	require.EqualValues(t, 5195, exchange.Response.DurMs)
	require.EqualValues(t, 5190, exchange.Response.TTFBMs)
	require.Equal(t, id+".resp.gz", exchange.Response.Blob)
	require.Equal(t, "tui", exchange.Scope.Source)
	require.Equal(t, 4117, exchange.Scope.PID)
	require.Equal(t, "prj_root_a", exchange.Scope.Project)
	require.Equal(t, "s-a", exchange.Scope.Session)
	require.Equal(t, "run-1", exchange.Scope.Run)
	require.Equal(t, "main", exchange.Scope.Agent)
	require.NotNil(t, exchange.Summary)
	require.Equal(t, "hello", exchange.Summary.Text)
	require.Equal(t, "end_turn", exchange.Summary.FinishReason)
	require.Equal(t, &SummaryUsage{Input: 11, Output: 3}, exchange.Summary.Usage)
	require.Empty(t, exchange.Concurrent)

	// A call the day does not hold is not found, and a day that is not a day is
	// refused before the directory is read.
	_, err = ReadExchange(context.Background(), root, day, "nope-000001")
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = ReadExchange(context.Background(), root, "..", id)
	require.ErrorIs(t, err, ErrBadLogRequest)
}

func TestReadExchangeWithoutResponse(t *testing.T) {
	root := t.TempDir()
	day := time.Now().In(time.Local).Format(dayLayout)
	at := time.Now().Add(-time.Minute)
	id := "b1c2d3-000007"
	dir := logDayDir(t, root, time.Now())
	writeLines(t, dir, "gateway-gw-4117-b1c2d3.jsonl",
		recordLine(t, exchangeRecord(at, "llm request", map[string]any{
			"x": id, "method": "POST", "url": "https://api.example.com/v1/chat", "model": "gpt",
			"req_bytes": 10, "stream": false,
		}, map[string]BlobRef{"req": {Name: id + ".req.gz", Bytes: 10, Whole: true}}, "s-b")),
	)

	exchange, err := ReadExchange(context.Background(), root, day, id)
	require.NoError(t, err)
	require.NotNil(t, exchange.Request)
	require.Nil(t, exchange.Response)
	require.Nil(t, exchange.Summary)
	require.Equal(t, "s-b", exchange.Scope.Session)
}

// Three calls overlap and one of them is the call the page opened: the other two
// are listed, oldest first, each with its own session.
func TestReadExchangeListsConcurrentCalls(t *testing.T) {
	root := t.TempDir()
	day := time.Now().In(time.Local).Format(dayLayout)
	base := time.Now().Add(-10 * time.Minute).Truncate(time.Millisecond)
	dir := logDayDir(t, root, time.Now())
	lines := []string{}
	add := func(id, session string, from, to time.Time, status int) {
		lines = append(lines,
			recordLine(t, exchangeRecord(from, "llm request", map[string]any{
				"x": id, "method": "POST", "url": "u", "model": "m", "req_bytes": 1,
			}, nil, session)),
			recordLine(t, exchangeRecord(to, "llm response", map[string]any{
				"x": id, "method": "POST", "url": "u", "model": "m", "status": status,
				"outcome": "ok", "dur_ms": 1, "req_bytes": 1, "resp_bytes": 1,
			}, nil, session)),
		)
	}
	add("one-000001", "s-1", base, base.Add(10*time.Second), 200)
	add("two-000002", "s-2", base.Add(5*time.Second), base.Add(20*time.Second), 500)
	add("three-000003", "s-3", base.Add(30*time.Second), base.Add(40*time.Second), 200)
	writeLines(t, dir, "tui-mbp-4117-abcdef.jsonl", lines...)

	exchange, err := ReadExchange(context.Background(), root, day, "one-000001")
	require.NoError(t, err)
	require.Len(t, exchange.Concurrent, 1)
	require.Equal(t, "two-000002", exchange.Concurrent[0].ID)
	require.Equal(t, "s-2", exchange.Concurrent[0].Session)
	require.Equal(t, 500, exchange.Concurrent[0].Status)

	// The call that ran afterwards does not overlap the first one.
	exchange, err = ReadExchange(context.Background(), root, day, "three-000003")
	require.NoError(t, err)
	require.Empty(t, exchange.Concurrent)
}

// ---- OpenBlob ------------------------------------------------------------

// Every name that could leave the day's x/ is refused by validation, before any
// file is opened.
func TestOpenBlobRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	day := time.Now().In(time.Local).Format(dayLayout)
	// A file the traversal would reach if the name were trusted.
	writeGzipFile(t, root, day, "real.req.gz", []byte("ok"))
	require.NoError(t, os.WriteFile(filepath.Join(root, "secret.gz"), []byte("secret"), 0o600))

	for _, name := range []string{"../x.gz", "a/b.gz", "x.txt", "..", ".", "", "sub/../x.gz"} {
		_, _, err := OpenBlob(root, day, name)
		require.ErrorIs(t, err, ErrBadLogRequest, "name %q", name)
		require.False(t, errors.Is(err, os.ErrNotExist), "name %q reached the filesystem", name)
	}
	for _, bad := range []string{"..", "", "2026-1-1", "2026-11-11/../..", "logs"} {
		_, _, err := OpenBlob(root, bad, "real.req.gz")
		require.ErrorIs(t, err, ErrBadLogRequest, "day %q", bad)
	}

	// A name that does not exist is not a bad request: it is a missing payload.
	_, _, err := OpenBlob(root, day, "absent.req.gz")
	require.ErrorIs(t, err, os.ErrNotExist)
}

// A symlink where a payload should be is refused rather than followed.
func TestOpenBlobRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	day := time.Now().In(time.Local).Format(dayLayout)
	dir := filepath.Join(root, home.LogsDir, day, blobDirName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "secret.txt"), []byte("secret"), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(root, "secret.txt"), filepath.Join(dir, "link.req.gz")))

	_, _, err := OpenBlob(root, day, "link.req.gz")
	require.ErrorIs(t, err, ErrBadLogRequest)
}

// A payload whose writer has not finished ends in io.ErrUnexpectedEOF; the bytes
// that did arrive are what a reader asking where a stuck stream stopped needs.
func TestOpenBlobReadsUnfinishedStream(t *testing.T) {
	root := t.TempDir()
	day := time.Now().In(time.Local).Format(dayLayout)
	body := bytes.Repeat([]byte("the answer so far. "), 400)
	path := writeGzipFile(t, root, day, "cut-000001.resp.gz", body)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	// A stream cut before its trailer: the deflate blocks that arrived decompress,
	// the last few bytes of the gzip trailer never will.
	require.NoError(t, os.WriteFile(path, raw[:len(raw)-6], 0o600))

	reader, total, err := OpenBlob(root, day, "cut-000001.resp.gz")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	require.EqualValues(t, len(body), total)
	read, readErr := io.ReadAll(reader)
	require.True(t, readErr == nil || errors.Is(readErr, io.ErrUnexpectedEOF))
	require.Equal(t, string(body), string(read))
}

func TestOpenBlobReadsWholePayload(t *testing.T) {
	root := t.TempDir()
	day := time.Now().In(time.Local).Format(dayLayout)
	body := []byte("HTTP/1.1 200 OK\n\n{\"ok\":true}\n")
	writeGzipFile(t, root, day, "whole-000001.req.gz", body)

	reader, total, err := OpenBlob(root, day, "whole-000001.req.gz")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	require.EqualValues(t, len(body), total)
	read, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, string(body), string(read))
}

// ---- SummarizeResponse ---------------------------------------------------

// One event of each protocol's shape, plus the bodies that carry nothing this
// reader knows.
func TestSummarizeResponse(t *testing.T) {
	cases := []struct {
		name string
		body string
		want *ResponseSummary
	}{
		{
			name: "openai stream",
			body: "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"lo\",\"reasoning_content\":\"think\"}}]}\n" +
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"shell\",\"arguments\":\"{\\\"cmd\\\":\"}}]}}]}\n" +
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"ls\\\"}\"}}]}}]}\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":5}}\n" +
				"data: [DONE]\n",
			want: &ResponseSummary{
				Text: "Hello", Reasoning: "think",
				ToolCalls:    []SummaryToolCall{{Name: "shell", Arguments: `{"cmd":"ls"}`}},
				FinishReason: "tool_calls", Usage: &SummaryUsage{Input: 7, Output: 5}, Events: 5,
			},
		},
		{
			name: "openai reasoning spelled reasoning",
			body: "data: {\"choices\":[{\"delta\":{\"reasoning\":\"why\"}}]}\n",
			want: &ResponseSummary{Reasoning: "why", Events: 1},
		},
		{
			name: "anthropic stream",
			body: "event: content_block_start\n" +
				"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"name\":\"shell\",\"input\":{}}}\n\n" +
				"event: content_block_delta\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hmm\"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"a\\\":1}\"}}\n\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}\n\n",
			want: &ResponseSummary{
				Text: "Hi", Reasoning: "hmm",
				ToolCalls:    []SummaryToolCall{{Name: "shell", Arguments: `{"a":1}`}},
				FinishReason: "tool_use", Usage: &SummaryUsage{Input: 3, Output: 2}, Events: 5,
			},
		},
		{
			name: "responses stream",
			body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hey\"}\n\n" +
				"data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"because\"}\n\n" +
				"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"name\":\"shell\",\"arguments\":\"{\\\"x\\\":1}\"}}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":9,\"output_tokens\":4}}}\n\n",
			want: &ResponseSummary{
				Text: "Hey", Reasoning: "because",
				ToolCalls:    []SummaryToolCall{{Name: "shell", Arguments: `{"x":1}`}},
				FinishReason: "completed", Usage: &SummaryUsage{Input: 9, Output: 4}, Events: 4,
			},
		},
		{
			name: "gemini chunks",
			body: "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Yo\"},{\"text\":\"pondering\",\"thought\":true},{\"functionCall\":{\"name\":\"shell\",\"args\":{\"q\":2}}}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":6,\"candidatesTokenCount\":2}}\n",
			want: &ResponseSummary{
				Text: "Yo", Reasoning: "pondering",
				ToolCalls:    []SummaryToolCall{{Name: "shell", Arguments: `{"q":2}`}},
				FinishReason: "STOP", Usage: &SummaryUsage{Input: 6, Output: 2}, Events: 1,
			},
		},
		{
			name: "non-streamed openai reply",
			body: "{\"choices\":[{\"message\":{\"content\":\"unused\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}",
			// A non-streamed reply carries choices[0].message, not delta: this
			// reader only ever sees delta, so it reports nothing but the finish
			// reason and the usage it did read.
			want: &ResponseSummary{FinishReason: "stop", Usage: &SummaryUsage{Input: 1, Output: 1}, Events: 1},
		},
		{name: "an error body", body: "{\"error\":{\"message\":\"[400] Request contains an invalid argument.\"}}"},
		{name: "garbage", body: "\x00\x01not json at all"},
		{name: "empty", body: ""},
		{name: "unknown event shapes", body: "data: {\"kind\":\"ping\"}\ndata: {\"type\":\"keepalive\"}\n"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := SummarizeResponse([]byte(testCase.body))
			if testCase.want == nil {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, testCase.want.Text, got.Text)
			require.Equal(t, testCase.want.Reasoning, got.Reasoning)
			require.Equal(t, testCase.want.ToolCalls, got.ToolCalls)
			require.Equal(t, testCase.want.FinishReason, got.FinishReason)
			require.Equal(t, testCase.want.Usage, got.Usage)
			require.Equal(t, testCase.want.Events, got.Events)
		})
	}
}

// ---- the scan's cost -----------------------------------------------------

// BenchmarkQueryEvents reads a hundred megabytes of records with a session
// filter: the plan's ceiling is 1.5s for this shape on a developer machine.
func BenchmarkQueryEvents(b *testing.B) {
	root := b.TempDir()
	day := startOfLocalDay(time.Now()).Format(dayLayout)
	dir := filepath.Join(root, home.LogsDir, day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	now := time.Now()
	from := now.Add(-time.Hour)
	file, err := os.Create(filepath.Join(dir, "gateway-bench-4117-51be02.jsonl"))
	if err != nil {
		b.Fatal(err)
	}
	writer := bufio.NewWriterSize(file, 1<<20)
	var written int64
	for index := 0; written < 100<<20; index++ {
		at := from.Add(time.Duration(index) * 250 * time.Microsecond)
		session := "cli-other"
		if index%10 == 0 {
			session = "cli-wanted"
		}
		line := `{"t":"` + at.Format(eventTimeLayout) + `","lvl":"INFO","kind":"tool","msg":"a tool call finished",` +
			`"project":"prj_root_a","session":"` + session + `","run":"run-1","agent":"main",` +
			`"at":"pkg/run/runner.go:1356","attrs":{"tool":"shell","dur_ms":12,"out":"a line of output"}}` + "\n"
		if _, err := writer.WriteString(line); err != nil {
			b.Fatal(err)
		}
		written += int64(len(line))
	}
	if err := writer.Flush(); err != nil {
		b.Fatal(err)
	}
	if err := file.Close(); err != nil {
		b.Fatal(err)
	}

	// The plan's measurement is the session filter; the text filter is the one
	// that touches every line, so it is measured beside it.
	for _, testCase := range []struct {
		name  string
		query EventQuery
	}{
		{"session", EventQuery{From: from, To: now, Session: "cli-wanted", PageSize: 50}},
		{"text", EventQuery{From: from, To: now, Text: "a line of output", PageSize: 50}},
	} {
		b.Run(testCase.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				page, err := QueryEvents(context.Background(), root, testCase.query)
				if err != nil {
					b.Fatal(err)
				}
				if page.Total == 0 {
					b.Fatal("no rows: the fixture does not match the filter")
				}
			}
		})
	}
}

// A caller that has gone away stops the scan instead of leaving it to read the
// rest of the tree: the request's context is checked at every file boundary.
func TestQueryEventsStopsWhenTheCallerIsGone(t *testing.T) {
	root := t.TempDir()
	writeRecords(t, testEventLog(t, root, "tui"),
		Record{Time: time.Now(), Level: "INFO", Kind: "log", Msg: "one"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	now := time.Now()
	_, err := QueryEvents(ctx, root, EventQuery{From: now.Add(-time.Hour), To: now.Add(time.Hour)})
	require.ErrorIs(t, err, context.Canceled)
}

// The exchange scan reads two day directories; a cancelled request must not
// keep walking them either.
func TestReadExchangeStopsWhenTheCallerIsGone(t *testing.T) {
	root := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ReadExchange(ctx, root, time.Now().Format(dayLayout), "any")
	require.ErrorIs(t, err, context.Canceled)
}
