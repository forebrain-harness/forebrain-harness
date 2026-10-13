package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaxMessageBytes bounds one incoming message (spec §7.1).
const MaxMessageBytes = 64 << 20

// maxOutboxBytes bounds what may wait for the peer to read it. A peer that
// leaves this much of its input unread is as hung as one that stalls.
const maxOutboxBytes = 64 << 20

// writeChunkBytes is the most one write hands the peer at once, so the stall
// timer measures progress rather than the size of the whole frame.
const writeChunkBytes = 16 << 10

// writeStallTimeout is how long one chunk of an outgoing frame may wait for
// the peer to read it before the connection ends with ErrWriteStalled. A
// package-level variable only so tests can shorten it; production code must
// not write it.
var writeStallTimeout = 30 * time.Second

// ErrWriteStalled ends a connection whose peer stopped reading its input: a
// language server that is alive but hung (its binary's volume vanished, it
// deadlocked) never exits, so only its unread input gives it away.
var ErrWriteStalled = errors.New("jsonrpc: the peer stopped reading its input")

// Standard JSON-RPC and LSP error codes used by this package.
const (
	CodeParseError       int64 = -32700
	CodeInvalidRequest   int64 = -32600
	CodeMethodNotFound   int64 = -32601
	CodeInvalidParams    int64 = -32602
	CodeInternalError    int64 = -32603
	CodeRequestCancelled int64 = -32800
)

// RPCError is a JSON-RPC error object; it is also the error type Call returns
// when the server answers with an error.
type RPCError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// ErrMethodNotFound, returned by a RequestHandler, answers -32601.
var ErrMethodNotFound = errors.New("method not found")

// ErrMessageTooLarge is returned by ReadMessage for a body over MaxMessageBytes.
var ErrMessageTooLarge = errors.New("jsonrpc: message too large")

// ErrConnClosed is what a Call returns when the connection ends first.
var ErrConnClosed = errors.New("jsonrpc: connection closed")

// ReadMessage reads one framed message body.
func ReadMessage(r *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			break // the blank line ends the headers
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("jsonrpc: malformed header %q", line)
		}
		if !strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			continue // Content-Type and unknown headers are ignored
		}
		n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("jsonrpc: invalid Content-Length %q", value)
		}
		if n > MaxMessageBytes {
			return nil, ErrMessageTooLarge
		}
		contentLength = int(n)
	}
	if contentLength < 0 {
		return nil, errors.New("jsonrpc: missing Content-Length header")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// WriteMessage writes one framed message.
func WriteMessage(w io.Writer, body []byte) error {
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

// RequestHandler answers a server-to-client request. Returning an *RPCError
// sends it as-is; ErrMethodNotFound sends -32601; any other error sends -32603
// with err.Error() as the message.
type RequestHandler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// NotificationHandler receives server-to-client notifications, in arrival order.
type NotificationHandler func(method string, params json.RawMessage)

// ConnOptions configures a Conn.
type ConnOptions struct {
	OnRequest RequestHandler      // nil: every request answers -32601
	OnNotify  NotificationHandler // nil: notifications are dropped
}

// Conn is one JSON-RPC connection. It starts its read and write loops in
// NewConn. Sending never waits on the peer: frames queue on the outbox and
// the write loop is the only goroutine that ever blocks on w.
type Conn struct {
	r       *bufio.Reader
	w       io.Writer
	closer  io.Closer // r's source, when closing it can unblock the read loop
	wcloser io.Closer // w, when closing it can unblock a write the peer never reads
	opts    ConnOptions
	ctx     context.Context // cancelled once the connection ends
	cancel  context.CancelFunc
	stall   time.Duration // writeStallTimeout when the connection opened
	wake    chan struct{} // buffered 1: the outbox has frames

	mu          sync.Mutex
	nextID      int64
	pending     map[int64]chan *wireMessage
	outbox      [][]byte // framed messages not yet written, in send order
	outboxBytes int
	closed      bool // the connection ended; guards err, pending and outbox
	err         error
	userClosed  bool // Close was called; the closers are closed exactly once
	done        chan struct{}
}

// NewConn starts one JSON-RPC connection over the peer's streams (a language
// server's stdout as r and its stdin as w).
func NewConn(r io.Reader, w io.Writer, opts ConnOptions) *Conn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{
		r:       bufio.NewReader(r),
		w:       w,
		opts:    opts,
		ctx:     ctx,
		cancel:  cancel,
		stall:   writeStallTimeout,
		wake:    make(chan struct{}, 1),
		pending: map[int64]chan *wireMessage{},
		done:    make(chan struct{}),
	}
	if closer, ok := r.(io.Closer); ok {
		c.closer = closer
	}
	if wcloser, ok := w.(io.Closer); ok {
		c.wcloser = wcloser
	}
	go c.readLoop()
	go c.writeLoop()
	return c
}

// wireMessage is the superset every incoming message decodes into.
type wireMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *RPCError       `json:"error"`
}

// wireCall is a request this side sends; ids are ours, so int64.
type wireCall struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// wireNotification is a notification this side sends.
type wireNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// wireResponse answers a server-to-client request; the id is the peer's raw
// JSON, so a string id round-trips verbatim.
type wireResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

func (c *Conn) readLoop() {
	for {
		body, err := ReadMessage(c.r)
		if err != nil {
			c.shutdown(err)
			return
		}
		var msg wireMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			c.shutdown(fmt.Errorf("jsonrpc: malformed message: %w", err))
			return
		}
		switch {
		case msg.Method != "" && hasID(msg.ID):
			go c.serveRequest(&msg)
		case msg.Method != "":
			// Synchronous, so notifications stay in arrival order; handlers
			// must not block (the caller's responsibility).
			if c.opts.OnNotify != nil {
				c.opts.OnNotify(msg.Method, msg.Params)
			}
		case hasID(msg.ID):
			c.deliver(&msg)
		}
		// Anything else (no method and no id) is ignored.
	}
}

func hasID(raw json.RawMessage) bool {
	id := strings.TrimSpace(string(raw))
	return id != "" && id != "null"
}

// deliver hands a response to its pending Call. A response whose id matches
// no pending call (it was cancelled) is dropped.
func (c *Conn) deliver(msg *wireMessage) {
	var id int64
	if err := json.Unmarshal(msg.ID, &id); err != nil {
		return // not one of our int64 ids
	}
	c.mu.Lock()
	ch, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if ok {
		ch <- msg // buffered 1 and single-use: never blocks
	}
}

// serveRequest answers one server-to-client request on its own goroutine.
func (c *Conn) serveRequest(msg *wireMessage) {
	result, err := c.invoke(msg)
	if err != nil {
		c.reply(msg.ID, nil, toRPCError(err))
		return
	}
	c.reply(msg.ID, result, nil)
}

func (c *Conn) invoke(msg *wireMessage) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			result, err = nil, fmt.Errorf("handler panic: %v", r)
		}
	}()
	if c.opts.OnRequest == nil {
		return nil, ErrMethodNotFound
	}
	return c.opts.OnRequest(c.ctx, msg.Method, msg.Params)
}

func toRPCError(err error) *RPCError {
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		return rpcErr
	}
	if errors.Is(err, ErrMethodNotFound) {
		return &RPCError{Code: CodeMethodNotFound, Message: ErrMethodNotFound.Error()}
	}
	return &RPCError{Code: CodeInternalError, Message: err.Error()}
}

func (c *Conn) reply(id json.RawMessage, result any, rpcErr *RPCError) {
	resp := wireResponse{JSONRPC: "2.0", ID: id}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else if raw, err := json.Marshal(result); err != nil { // nil marshals as null
		resp.Error = &RPCError{Code: CodeInternalError, Message: err.Error()}
	} else {
		resp.Result = raw
	}
	body, err := json.Marshal(resp)
	if err != nil {
		return
	}
	// Best effort: a connection that ended has no one left to answer.
	_ = c.send(body)
}

// send queues one message for the write loop and returns at once; messages
// reach the peer in the order send was called. It fails only when the
// connection already ended — or ends it, when the peer has left
// maxOutboxBytes of its input unread.
func (c *Conn) send(body []byte) error {
	frame := make([]byte, 0, len(body)+32)
	frame = fmt.Appendf(frame, "Content-Length: %d\r\n\r\n", len(body))
	frame = append(frame, body...)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return c.closedError()
	}
	if c.outboxBytes+len(frame) > maxOutboxBytes {
		c.mu.Unlock()
		c.endStalled(fmt.Errorf("%w: %d MiB of it is waiting", ErrWriteStalled, maxOutboxBytes>>20))
		return c.closedError()
	}
	c.outbox = append(c.outbox, frame)
	c.outboxBytes += len(frame)
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default: // the write loop is already due to look
	}
	return nil
}

// writeLoop is the connection's only writer. A failed or stalled write ends
// the connection.
func (c *Conn) writeLoop() {
	for {
		select {
		case <-c.wake:
		case <-c.done:
			return
		}
		for {
			c.mu.Lock()
			if c.closed || len(c.outbox) == 0 {
				c.mu.Unlock()
				break
			}
			frame := c.outbox[0]
			c.outbox[0] = nil
			c.outbox = c.outbox[1:]
			c.outboxBytes -= len(frame)
			c.mu.Unlock()
			if err := c.writeFrame(frame); err != nil {
				c.shutdown(fmt.Errorf("jsonrpc: write: %w", err))
				return
			}
		}
	}
}

// writeFrame writes one frame in chunks, each of which must reach the peer
// within the stall timeout. On a stall the connection ends and w is closed,
// the only thing that unblocks a write the peer will never read (a w that
// cannot be closed leaves this goroutine parked; nothing else waits on it).
func (c *Conn) writeFrame(frame []byte) error {
	for len(frame) > 0 {
		n := min(len(frame), writeChunkBytes)
		timer := time.AfterFunc(c.stall, func() {
			c.endStalled(fmt.Errorf("%w for %s", ErrWriteStalled, c.stall))
		})
		_, err := c.w.Write(frame[:n])
		timer.Stop()
		if err != nil {
			return err
		}
		frame = frame[n:]
	}
	return nil
}

// endStalled ends the connection because the peer stopped reading, then
// closes w so a write blocked on it returns.
func (c *Conn) endStalled(cause error) {
	c.shutdown(cause)
	if c.wcloser != nil {
		_ = c.wcloser.Close()
	}
}

// Call sends a request and decodes the result into result (may be nil).
// On ctx cancellation it sends $/cancelRequest {"id": <id>} and returns ctx.Err().
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ch := make(chan *wireMessage, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrConnClosed
	}
	id := c.nextID
	c.nextID++
	c.pending[id] = ch
	c.mu.Unlock()

	body, err := json.Marshal(wireCall{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		c.dropPending(id)
		return err
	}
	if err := c.send(body); err != nil {
		c.dropPending(id)
		return err
	}

	select {
	case msg := <-ch:
		if msg.Error != nil {
			return msg.Error
		}
		if result != nil && len(msg.Result) > 0 {
			if err := json.Unmarshal(msg.Result, result); err != nil {
				return fmt.Errorf("jsonrpc: decoding result of %q: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		// The id leaves the pending table first, so the late reply is dropped.
		c.dropPending(id)
		if body, err := json.Marshal(wireNotification{
			JSONRPC: "2.0",
			Method:  "$/cancelRequest",
			Params:  map[string]any{"id": id},
		}); err == nil {
			_ = c.send(body)
		}
		return ctx.Err()
	case <-c.done:
		return c.closedError()
	}
}

func (c *Conn) dropPending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// Notify queues a notification; it never waits for the peer to read it.
func (c *Conn) Notify(method string, params any) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return c.closedError()
	}
	body, err := json.Marshal(wireNotification{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	return c.send(body)
}

// Close stops the connection; pending calls return ErrConnClosed and
// unwritten messages are dropped. Idempotent.
func (c *Conn) Close() error {
	c.mu.Lock()
	var closer, wcloser io.Closer
	if !c.userClosed {
		c.userClosed = true
		closer, wcloser = c.closer, c.wcloser
	}
	c.mu.Unlock()
	c.shutdown(nil)
	if wcloser != nil {
		// Closing the write side returns a write the peer never read; the
		// peer sees the end of its input.
		_ = wcloser.Close()
	}
	if closer == nil {
		return nil
	}
	// Closing the read side unblocks the read loop; its own shutdown is a
	// no-op by now.
	return closer.Close()
}

// shutdown ends the connection exactly once: it records why (nil when Close
// asked for it), drops the unwritten messages, wakes every pending call and
// the write loop through done, and cancels the context request handlers saw.
func (c *Conn) shutdown(cause error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.err = cause
	c.pending = map[int64]chan *wireMessage{}
	c.outbox, c.outboxBytes = nil, 0
	c.mu.Unlock()
	close(c.done)
	c.cancel()
}

// Done is closed when the connection ends (peer closed, protocol error, a
// failed or stalled write, Close).
func (c *Conn) Done() <-chan struct{} {
	return c.done
}

// Err reports why the connection ended (nil after Close).
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Conn) closedError() error {
	if cause := c.Err(); cause != nil {
		return fmt.Errorf("%w: %w", ErrConnClosed, cause)
	}
	return ErrConnClosed
}
