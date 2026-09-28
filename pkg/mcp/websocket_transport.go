package mcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type WebSocketTransport struct {
	Endpoint string
	Header   http.Header
	Dialer   *websocket.Dialer
}

func (t *WebSocketTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	dialer := t.Dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}
	conn, _, err := dialer.DialContext(ctx, t.Endpoint, t.Header)
	if err != nil {
		return nil, err
	}
	return newWebSocketConn(conn), nil
}

type webSocketConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
	once sync.Once
}

func newWebSocketConn(conn *websocket.Conn) mcp.Connection {
	return &webSocketConn{conn: conn}
}

func (c *webSocketConn) Read(context.Context) (jsonrpc.Message, error) {
	typ, r, err := c.conn.NextReader()
	if err != nil {
		return nil, err
	}
	if typ != websocket.TextMessage && typ != websocket.BinaryMessage {
		return nil, errors.New("unsupported websocket message type")
	}
	data, err := io.ReadAll(io.LimitReader(r, 16<<20))
	if err != nil {
		return nil, err
	}
	return jsonrpc.DecodeMessage(data)
}

func (c *webSocketConn) Write(_ context.Context, msg jsonrpc.Message) error {
	data, err := jsonrpc.EncodeMessage(msg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	w, err := c.conn.NextWriter(websocket.TextMessage)
	if err != nil {
		return err
	}
	_, werr := w.Write(data)
	cerr := w.Close()
	return errors.Join(werr, cerr)
}

func (c *webSocketConn) Close() error {
	var err error
	c.once.Do(func() {
		err = c.conn.Close()
	})
	return err
}

func (c *webSocketConn) SessionID() string {
	return ""
}
