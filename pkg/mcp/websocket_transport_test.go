package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/testutil"
	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type singleWebSocketServerTransport struct {
	conn chan mcp.Connection
}

func (t *singleWebSocketServerTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case conn := <-t.conn:
		return conn, nil
	}
}

func TestStartSupportsWebSocketTransport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverTransport := &singleWebSocketServerTransport{conn: make(chan mcp.Connection, 1)}
	server := mcp.NewServer(&mcp.Implementation{Name: "ws-test", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echo over websocket"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "pong"}},
		}, struct{}{}, nil
	})
	runErr := make(chan error, 1)
	go func() {
		runErr <- server.Run(ctx, serverTransport)
	}()

	upgrader := websocket.Upgrader{}
	httpServer := testutil.NewLocalServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		serverTransport.conn <- newWebSocketConn(conn)
	}))
	defer httpServer.Close()

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	session, err := Start(ctx, t.TempDir(), t.TempDir(), appcfg.MCPServerConfig{
		Name:      "ws",
		Transport: "websocket",
		URL:       wsURL,
	})
	if err != nil {
		t.Fatalf("Start websocket: %v", err)
	}
	defer session.Close()

	tools, err := session.ListToolMetas(ctx)
	if err != nil {
		t.Fatalf("ListToolMetas: %v", err)
	}
	if len(tools) != 1 || tools[0]["name"] != "echo" {
		t.Fatalf("tools=%v", tools)
	}

	out, err := session.CallToolJSON(ctx, "echo", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CallToolJSON: %v", err)
	}
	if !strings.Contains(out, "pong") {
		t.Fatalf("out=%s", out)
	}

	cancel()
	select {
	case <-time.After(time.Second):
	case <-runErr:
	}
}
