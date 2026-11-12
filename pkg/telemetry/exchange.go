// One model call, one pair of files and one pair of records: what the request
// carried, what came back, and which conversation it belonged to (plans/286).
//
// This replaces the shared debug.log blocks. Every block in that file was
// written through one append lock by every session, subagent and process, said
// only "id=<pid>-<seq>", and a streamed answer reached the disk only when the
// stream ended — so a call whose process died mid-answer left nothing at all,
// and the call a person was debugging could not be told from its neighbours.
//
// Here each call writes its own two files under the day directory's x/, named
// by an id that the records carry, and a streamed response is written as it
// arrives.
package telemetry

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	// DefaultRecordBytes bounds a payload that is not a model's own output: a
	// tool result, and the OTel span's snapshot of a response body.
	DefaultRecordBytes = 128 * 1024

	// requestKeepBytes is what a SUCCESSFUL request keeps: 32 KiB of head and
	// 32 KiB of tail, with the count of what was left out between them.
	//
	// Only "ok" is trimmed. A refused, cut, cancelled or unfinished call keeps
	// its request whole, because that request IS the evidence: the 2026-11-08
	// Gemini "[400] Request contains an invalid argument." lost the middle 877
	// KB of a 1.1 MB request to exactly this cap, and the cause with it (owner
	// ruling 2026-11-11).
	requestKeepBytes = 64 * 1024

	// payloadInlineMaxBytes is a record attribute's own limit: a payload up to
	// this size rides in the record, and anything larger goes whole to its file
	// with a preview left behind.
	payloadInlineMaxBytes = attrMaxBytes
	// payloadPreviewBytes is how much of an oversized payload the record shows.
	payloadPreviewBytes = 512

	// blobFlushBytes is how much a streamed payload accumulates before it is
	// flushed to its file. With blobFlushEvery it is what makes the file hold
	// what has arrived so far, rather than only what a finished stream held.
	blobFlushBytes = 32 * 1024

	// attrBlobError reports a payload file that could not be written. The call
	// itself is unaffected: capture never fails a request.
	attrBlobError = "blob_error"
	// blobAttrPrefix marks an Emit attribute as a payload reference; the rest of
	// the key is the Record.Blobs key ("blob.req" -> Blobs["req"]).
	blobAttrPrefix = "blob."

	// kindLLM is the record kind of a model call, told apart from kindHTTP (the
	// access log) and kindLog (everything an ordinary logger said).
	kindLLM = "llm"

	// The outcomes of one call: what a reader filters on. Only ok is trimmed.
	outcomeOK             = "ok"
	outcomeHTTPError      = "http_error"
	outcomeReadError      = "read_error"
	outcomeTransportError = "transport_error"
	outcomeCancelled      = "cancelled"
)

// blobFlushEvery is the time half of the flush rule, a variable so a test can
// drive it without sleeping a second.
var blobFlushEvery = time.Second

var (
	// llmCallSeq and toolCallSeq number the calls of this process. The event
	// log's six hex digits (random per start-up) make the id unique across
	// processes, so no pid is needed and pid reuse cannot collide.
	llmCallSeq  atomic.Uint64
	toolCallSeq atomic.Uint64
)

// nextCallID is <proc>-<6 digits> for a model call and <proc>-t<6 digits> for a
// tool call, the id both of a call's records and of its payload files.
func nextCallID(log *EventLog, prefix string, seq *atomic.Uint64) string {
	return fmt.Sprintf("%s-%s%06d", log.Proc(), prefix, seq.Add(1))
}

// ---- the round tripper ----------------------------------------------------

// newExchangeRoundTripper keeps one record pair and one file pair per model
// call. It sits outside the OTel transport, so it sees the request exactly as
// the provider client built it and the response exactly as it arrived.
func newExchangeRoundTripper(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &exchangeRoundTripper{next: next}
}

type exchangeRoundTripper struct {
	next http.RoundTripper
}

func (t *exchangeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := readRequestBody(req)
	if err != nil {
		return nil, err
	}
	call := newExchangeCall(req, body)
	if call.log == nil {
		// No event log: nothing would name a payload file, so none is written.
		// The body is still read above — the OTel transport inside reads it too,
		// and a one-shot body has to leave here replayable either way.
		return t.next.RoundTrip(req)
	}
	call.recordRequest(req, body)

	resp, err := t.next.RoundTrip(req)
	if err != nil {
		call.recordTransportError(err)
		return nil, err
	}
	return call.observeResponse(resp)
}

// readRequestBody reads the request body whole and puts a fresh reader back.
// The transport this replaces did the same, and its layers depend on it: the
// OTel transport reads the body again, and an adapter's own retry re-sends it.
func readRequestBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

// ---- one call -------------------------------------------------------------

// exchangeCall is the state of one call in flight: its identity, its timings,
// and the one file each side writes to.
type exchangeCall struct {
	log *EventLog
	ctx context.Context
	id  string

	method string
	url    string // already redacted
	model  string
	stream bool // the request asked for a stream
	// reqBody is the request body's own size; reqText is the whole request file
	// (request line, headers, body) kept for the atomic rewrite a success does.
	reqBody int64
	reqText []byte
	reqName string
	reqPath string

	status    int
	streamed  bool // the response really was an event stream
	respName  string
	respHead  int64
	respBytes int64

	start    time.Time
	ttfb     time.Duration
	lastByte time.Time
	maxGap   time.Duration

	once     sync.Once
	blobErrs []string
}

func newExchangeCall(req *http.Request, body []byte) *exchangeCall {
	now := time.Now()
	return &exchangeCall{
		log:      DefaultEventLog(),
		ctx:      req.Context(),
		method:   strings.TrimSpace(req.Method),
		url:      redactURL(req.URL),
		model:    requestModel(req, body),
		stream:   streamRequested(req, body),
		reqBody:  int64(len(body)),
		start:    now,
		lastByte: now,
	}
}

// recordRequest writes the request payload and its DEBUG record. Both exist
// before the downstream call starts, so a process that dies mid-request leaves
// the request behind.
func (c *exchangeCall) recordRequest(req *http.Request, body []byte) {
	c.id = nextCallID(c.log, "", &llmCallSeq)
	if payloadCaptureEnabled() {
		c.reqText = c.requestText(req, body)
		c.writeRequestPayload()
	}
	attrs := []slog.Attr{
		slog.String("x", c.id),
		slog.String("method", c.method),
		slog.String("url", c.url),
		slog.String("model", c.model),
		slog.Int64("req_bytes", c.reqBody),
		slog.Bool("stream", c.stream),
	}
	// As written: the file holds the whole request at this point, and a success
	// trims it later — the response record says what it ended up as.
	if c.reqName != "" {
		attrs = append(attrs, slog.Any(blobAttrPrefix+"req",
			BlobRef{Name: c.reqName, Bytes: int64(len(c.reqText)), Whole: true}))
	}
	if len(c.blobErrs) > 0 {
		attrs = append(attrs, slog.String(attrBlobError, strings.Join(c.blobErrs, "; ")))
	}
	c.emit(slog.LevelDebug, "llm request", attrs)
}

// requestText is the request file's content: the request line, the redacted
// headers, a blank line, then the body. Everything but what the redactors
// rewrite is the bytes that went on the wire, so the file can be replayed.
func (c *exchangeCall) requestText(req *http.Request, body []byte) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s %s %s\n", c.method, c.url, req.Proto)
	b.WriteString(formatRedactedHeaderLines(req.Header))
	b.WriteByte('\n')
	b.WriteString(RedactLogLine(string(body)))
	return b.Bytes()
}

func (c *exchangeCall) writeRequestPayload() {
	name := c.id + ".req.gz"
	path, err := c.log.BlobPath(c.start, name)
	if err != nil {
		c.payloadFailed(err)
		return
	}
	if err := writeGzip(path, c.reqText); err != nil {
		_ = os.Remove(path)
		c.payloadFailed(err)
		return
	}
	c.reqName, c.reqPath = name, path
}

// observeResponse decides how the response body is recorded and hands the
// caller its response. A unary answer is read here, as the transport this
// replaces did: the caller gets a replayable body and the record lands even
// when the adapter only looks at the status — a 4xx's error body is exactly the
// evidence a failed call needs. A streamed answer is never read here.
func (c *exchangeCall) observeResponse(resp *http.Response) (*http.Response, error) {
	c.status = resp.StatusCode
	c.ttfb = time.Since(c.start)
	c.lastByte = time.Now()
	c.streamed = isEventStreamResponse(resp)
	// Snapshot what the record and the payload describe before returning: an
	// outer transport may normalise the response while the body is consumed.
	snapshot := snapshotHTTPResponseMetadata(resp)
	sink := c.startResponsePayload(snapshot)

	if resp.Body == nil {
		c.finish(nil, false, sink)
		return resp, nil
	}
	if c.streamed {
		resp.Body = &teeBody{body: resp.Body, call: c, sink: sink}
		return resp, nil
	}
	tee := &teeBody{body: resp.Body, call: c, sink: sink}
	body, err := io.ReadAll(tee)
	_ = tee.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		// The record already carries the read error; a response whose body
		// could not be read is not one the caller can use.
		return nil, err
	}
	return resp, nil
}

// startResponsePayload creates the response file and writes its first part —
// the status line and the redacted headers. The body follows through teeBody.
func (c *exchangeCall) startResponsePayload(resp *http.Response) *payloadSink {
	if !payloadCaptureEnabled() {
		return nil
	}
	name := c.id + ".resp.gz"
	path, err := c.log.BlobPath(time.Now(), name)
	if err != nil {
		c.payloadFailed(err)
		return nil
	}
	sink, err := openPayloadSink(path)
	if err != nil {
		c.payloadFailed(err)
		return nil
	}
	c.respName = name
	head := []byte(resp.Proto + " " + resp.Status + "\n" + formatRedactedHeaderLines(resp.Header) + "\n")
	if _, err := sink.Write(head); err != nil {
		c.payloadFailed(err)
		return nil
	}
	c.respHead = int64(len(head))
	return sink
}

// recordTransportError reports a call the downstream transport refused or could
// not answer. The request payload stays whole: nothing came back.
func (c *exchangeCall) recordTransportError(err error) {
	level := slog.LevelWarn
	outcome := outcomeTransportError
	if errors.Is(err, context.Canceled) {
		// Cancellation is a person or a policy acting, not a failure.
		level, outcome = slog.LevelInfo, outcomeCancelled
	}
	attrs := c.callAttrs(outcome)
	attrs = append(attrs, slog.Any(attrErr, err))
	c.emit(level, "llm response", attrs)
}

// finish closes the call out: the response file gets its final bytes, a
// successful request file is trimmed to head and tail, and the response record
// is written. Everything runs once, whichever of EOF, a read error or a Close
// gets here first.
func (c *exchangeCall) finish(readErr error, earlyClose bool, sink *payloadSink) {
	c.once.Do(func() {
		if gap := time.Since(c.lastByte); gap > c.maxGap {
			c.maxGap = gap
		}
		outcome, level := outcomeOf(c.status, readErr, earlyClose)
		if sink != nil {
			if err := sink.Close(); err != nil {
				c.payloadFailed(err)
			}
		}
		attrs := c.callAttrs(outcome)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			attrs = append(attrs, slog.Any("read_err", readErr))
		}
		c.emit(level, "llm response", attrs)
	})
}

// callAttrs is what every response record says: which call, how it went, how
// long it took, and where its two payload files are. A successful call is the
// only one whose request file is trimmed to head and tail.
func (c *exchangeCall) callAttrs(outcome string) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("x", c.id),
		slog.String("method", c.method),
		slog.String("url", c.url),
		slog.String("model", c.model),
		slog.Int("status", c.status),
		slog.String("outcome", outcome),
		slog.Int64("dur_ms", time.Since(c.start).Milliseconds()),
		slog.Int64("ttfb_ms", c.ttfb.Milliseconds()),
		slog.Int64("max_gap_ms", c.maxGap.Milliseconds()),
		slog.Int64("req_bytes", c.reqBody),
		slog.Int64("resp_bytes", c.respBytes),
		slog.Bool("stream", c.streamed),
	}
	if c.reqName != "" {
		ref := BlobRef{Name: c.reqName, Bytes: int64(len(c.reqText)), Whole: true}
		if outcome == outcomeOK {
			if omitted, err := boundRequestPayload(c.reqPath, c.reqText); err != nil {
				c.payloadFailed(err)
			} else if omitted > 0 {
				ref.Whole, ref.Omitted = false, omitted
			}
		}
		attrs = append(attrs, slog.Any(blobAttrPrefix+"req", ref))
	}
	if c.respName != "" {
		attrs = append(attrs, slog.Any(blobAttrPrefix+"resp",
			BlobRef{Name: c.respName, Bytes: c.respBytes + c.respHead, Whole: true}))
	}
	if len(c.blobErrs) > 0 {
		attrs = append(attrs, slog.String(attrBlobError, strings.Join(c.blobErrs, "; ")))
	}
	return attrs
}

func (c *exchangeCall) emit(level slog.Level, msg string, attrs []slog.Attr) {
	Emit(c.ctx, level, kindLLM, msg, attrs...)
}

// payloadFailed records a payload that could not be written. The call keeps
// going: capture is a diagnostic, never a reason to fail a model request.
func (c *exchangeCall) payloadFailed(err error) {
	if err == nil {
		return
	}
	c.blobErrs = append(c.blobErrs, err.Error())
}

// outcomeOf maps how a call ended to its outcome and level. A level of WARN
// rather than ERROR is deliberate: retrying, re-signing and continuing all sit
// above this, so one failed call is not one failed turn — that is the turn's
// own ERROR record (plan 285).
func outcomeOf(status int, readErr error, earlyClose bool) (string, slog.Level) {
	switch {
	case earlyClose:
		// Closed before EOF: the caller stopped reading, which is a decision,
		// not a failure.
		return outcomeCancelled, slog.LevelInfo
	case errors.Is(readErr, context.Canceled):
		return outcomeCancelled, slog.LevelInfo
	case readErr != nil && !errors.Is(readErr, io.EOF):
		return outcomeReadError, slog.LevelWarn
	case status >= 200 && status < 300:
		return outcomeOK, slog.LevelInfo
	default:
		return outcomeHTTPError, slog.LevelWarn
	}
}

// ---- the streamed payload -------------------------------------------------

// teeBody passes the response through unchanged and writes it to the call's
// payload file in the same Read. It never buffers: the bytes go to the gzip
// writer and straight back to the caller, so memory stays at the writer's fixed
// buffer, the chunk boundaries are the transport's own, and errors are handed
// on as they arrive.
type teeBody struct {
	body io.ReadCloser
	call *exchangeCall
	sink *payloadSink
	once sync.Once
	eof  atomic.Bool
}

func (b *teeBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if n > 0 {
		b.call.gotBytes(n)
		if b.sink != nil {
			// Redacted chunk by chunk: holding the whole stream to redact it in
			// one piece is the buffering this exists to avoid.
			if _, werr := b.sink.Write([]byte(RedactLogLine(string(p[:n])))); werr != nil {
				b.call.payloadFailed(werr)
				_ = b.sink.Close()
				b.sink = nil
				b.call.respName = ""
			}
		}
	}
	if err != nil {
		b.eof.Store(errors.Is(err, io.EOF))
		b.finish(err, false)
	}
	return n, err
}

func (b *teeBody) Close() error {
	err := b.body.Close()
	b.finish(err, !b.eof.Load())
	return err
}

func (b *teeBody) finish(err error, earlyClose bool) {
	b.once.Do(func() { b.call.finish(err, earlyClose, b.sink) })
}

// gotBytes accounts for one read of the response body: how many bytes, and how
// long the stream had been silent before them. With the header-to-first-byte
// and last-byte-to-EOF gaps, "it stalled for 58 seconds and then dropped"
// reads straight off the record.
func (c *exchangeCall) gotBytes(n int) {
	now := time.Now()
	if gap := now.Sub(c.lastByte); gap > c.maxGap {
		c.maxGap = gap
	}
	c.lastByte = now
	c.respBytes += int64(n)
}

// ---- payload files --------------------------------------------------------

// payloadSink is one gzip payload file being written. Bytes go through a gzip
// writer into a fixed-size buffer; the buffer is flushed when blobFlushBytes
// have accumulated or blobFlushEvery has passed, so the file always holds
// roughly what has arrived.
type payloadSink struct {
	file    *os.File
	buf     *bufio.Writer
	gz      *gzip.Writer
	pending int
	flushed time.Time
	closed  bool
}

func openPayloadSink(path string) (*payloadSink, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	buf := bufio.NewWriterSize(file, 2*blobFlushBytes)
	gz, err := gzip.NewWriterLevel(buf, gzip.BestSpeed)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &payloadSink{file: file, buf: buf, gz: gz, flushed: time.Now()}, nil
}

func (s *payloadSink) Write(p []byte) (int, error) {
	n, err := s.gz.Write(p)
	if err != nil {
		return n, err
	}
	s.pending += n
	if s.pending >= blobFlushBytes || time.Since(s.flushed) >= blobFlushEvery {
		if err := s.Flush(); err != nil {
			return n, err
		}
	}
	return n, nil
}

// Flush pushes what has been compressed into the file. It is called from the
// read path, so it does no sync: the descriptor's bytes survive a process
// death without one, which is the case this covers.
func (s *payloadSink) Flush() error {
	if s == nil || s.closed {
		return nil
	}
	if err := s.gz.Flush(); err != nil {
		return err
	}
	if err := s.buf.Flush(); err != nil {
		return err
	}
	s.pending = 0
	s.flushed = time.Now()
	return nil
}

// Close writes the gzip trailer and closes the file: a truncated gzip stream
// cannot be read back, and a payload nobody can decompress is not evidence.
func (s *payloadSink) Close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	err := s.gz.Close()
	if bufErr := s.buf.Flush(); err == nil {
		err = bufErr
	}
	if fileErr := s.file.Close(); err == nil {
		err = fileErr
	}
	return err
}

// writeGzip writes one complete payload file.
func writeGzip(path string, parts ...[]byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	gz, err := gzip.NewWriterLevel(file, gzip.BestSpeed)
	if err != nil {
		_ = file.Close()
		return err
	}
	for _, part := range parts {
		if _, err := gz.Write(part); err != nil {
			_ = file.Close()
			return err
		}
	}
	if err := gz.Close(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// boundRequestPayload replaces a successful call's request file with its head
// and tail and reports how many bytes that left out (0 when the file is already
// small enough). The new file goes to a temporary name in the same directory
// and is renamed over the old one, so a reader sees either the whole request or
// the trimmed one, never a half-written file.
func boundRequestPayload(path string, text []byte) (int64, error) {
	if len(text) <= requestKeepBytes {
		return 0, nil
	}
	tmp := path + ".tmp"
	if err := writeGzip(tmp, []byte(BoundText(string(text), requestKeepBytes))); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return int64(len(text) - requestKeepBytes), nil
}

// writeToolPayload writes one whole tool payload (arguments, or a result that
// did not fit in the record) and returns the reference the record carries.
// Tool payloads go in verbatim: the arguments are the model's own output and
// are kept byte for byte, which is what makes them replayable (plan 274).
func writeToolPayload(log *EventLog, name string, payload []byte, whole bool, omitted int64) (BlobRef, error) {
	path, err := log.BlobPath(time.Now(), name)
	if err != nil {
		return BlobRef{}, err
	}
	if err := writeGzip(path, payload); err != nil {
		_ = os.Remove(path)
		return BlobRef{}, err
	}
	return BlobRef{Name: name, Bytes: int64(len(payload)), Whole: whole, Omitted: omitted}, nil
}

// previewText is the head of text, cut on a rune boundary so the record stays
// valid UTF-8.
func previewText(text string, max int) string {
	if len(text) <= max {
		return text
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// ---- display fields -------------------------------------------------------

// modelInPathRe and modelInBodyRe are the two places a model name appears: the
// Gemini path (/models/<name>:generateContent) and the OpenAI/Anthropic body.
var (
	modelInPathRe = regexp.MustCompile(`/models/([^:/?#]+)`)
	modelInBodyRe = regexp.MustCompile(`"model"\s*:\s*"([^"]+)"`)
	streamTrueRe  = regexp.MustCompile(`"stream"\s*:\s*true`)
)

// requestModel is the model a request is for, as a display field only: this
// layer does not parse a provider's protocol, it recognises the two places
// every provider in use puts the name. Empty when neither says.
func requestModel(req *http.Request, body []byte) string {
	if req != nil && req.URL != nil {
		if m := modelInPathRe.FindStringSubmatch(req.URL.Path); m != nil {
			return m[1]
		}
	}
	head := body
	if len(head) > 4*1024 {
		head = head[:4*1024]
	}
	if m := modelInBodyRe.FindSubmatch(head); m != nil {
		return string(m[1])
	}
	return ""
}

// streamRequested reports whether the request asked for a stream: OpenAI and
// Anthropic write "stream": true in the body, Gemini names the streaming
// method in the URL. What came back is the other half of the pair: the response
// record's stream says whether the answer really was an event stream.
func streamRequested(req *http.Request, body []byte) bool {
	if streamTrueRe.Match(body) {
		return true
	}
	return req != nil && req.URL != nil && strings.Contains(req.URL.Path, "stream")
}
