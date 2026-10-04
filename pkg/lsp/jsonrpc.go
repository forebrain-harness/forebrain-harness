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
)

// MaxMessageBytes bounds one incoming message (spec §7.1).
const MaxMessageBytes = 64 << 20

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

// Conn is one JSON-RPC connection. It starts its read loop in NewConn.
type Conn struct {
	r       *bufio.Reader
	w       io.Writer
	closer  io.Closer // r's source, when closing it can unblock the read loop
	opts    ConnOptions
	ctx     context.Context // cancelled once the connection ends
	cancel  context.CancelFunc
	writeMu sync.Mutex // one frame at a time: header and body stay adjacent

	mu         sync.Mutex
	nextID     int64
	pending    map[int64]chan *wireMessage
	closed     bool // the connection ended; guards err and pending below
	err        error
	userClosed bool // Close was called; the closer is closed exactly once
	done       chan struct{}
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
		pending: map[int64]chan *wireMessage{},
		done:    make(chan struct{}),
	}
	if closer, ok := r.(io.Closer); ok {
		c.closer = closer
	}
	go c.readLoop()
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
	// Best effort: a failing write means the peer is gone, and the read loop
	// notices on its own.
	_ = c.writeFrame(body)
}

func (c *Conn) writeFrame(body []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return WriteMessage(c.w, body)
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
	if err := c.writeFrame(body); err != nil {
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
			_ = c.writeFrame(body)
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

// Notify sends a notification.
func (c *Conn) Notify(method string, params any) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return ErrConnClosed
	}
	body, err := json.Marshal(wireNotification{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	return c.writeFrame(body)
}

// Close stops the connection; pending calls return ErrConnClosed. Idempotent.
func (c *Conn) Close() error {
	c.mu.Lock()
	var closer io.Closer
	if !c.userClosed {
		c.userClosed = true
		closer = c.closer
	}
	c.mu.Unlock()
	c.shutdown(nil)
	if closer == nil {
		return nil
	}
	// Closing the read side unblocks the read loop; its own shutdown is a
	// no-op by now.
	return closer.Close()
}

// shutdown ends the connection exactly once: it records why (nil when Close
// asked for it), wakes every pending call through done, and cancels the
// context request handlers saw.
func (c *Conn) shutdown(cause error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.err = cause
	c.pending = map[int64]chan *wireMessage{}
	c.mu.Unlock()
	close(c.done)
	c.cancel()
}

// Done is closed when the read loop ends (peer closed, protocol error, Close).
func (c *Conn) Done() <-chan struct{} {
	return c.done
}

// Err reports why the read loop ended (nil after Close).
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
