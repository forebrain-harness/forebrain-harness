package mcp

import (
	"context"
	"errors"
	"strings"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

// TestConnectionTool is one tool a connection test listed.
type TestConnectionTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// TestConnectionResult is what one connection attempt found. A server that
// refuses to connect is a result, not an error: the server's own text is the
// only clue the user has for why, and it travels back verbatim.
type TestConnectionResult struct {
	OK        bool                 `json:"ok"`
	ToolCount int                  `json:"tool_count"`
	Tools     []TestConnectionTool `json:"tools,omitempty"`
	Error     string               `json:"error,omitempty"`
	ElapsedMS int64                `json:"elapsed_ms"`
}

// TestConnection starts one configured entry, lists its tools, and tears it
// down again — the check behind the "test connection" button, and the same
// path a session takes to start the entry: same transport parsing, same ${ENV}
// expansion, same OAuth handler, same command validation and command-hash
// check, same startup_timeout. "The test passed" and "a new session can
// connect" are therefore the same statement, which is the only thing that makes
// a test button worth pressing.
//
// It writes no configuration and leaves no child process behind: the connection
// is aborted on every path out, including a failed listing, exactly as a
// session's own failed startup does. dir is the working directory the entry
// would run from, which callers take from the session they are testing for.
//
// An error return means the entry could not be turned into a connection attempt
// at all (an empty command, an unknown transport) — the caller's problem to
// report as bad input. Everything a server said about itself comes back inside
// the result.
func TestConnection(ctx context.Context, home, dir string, srv appcfg.MCPServerConfig) (TestConnectionResult, error) {
	started := time.Now()
	res := TestConnectionResult{Tools: nil}
	finish := func() TestConnectionResult {
		res.ElapsedMS = time.Since(started).Milliseconds()
		return res
	}
	if name := strings.TrimSpace(srv.Name); name == "" {
		return res, errTestConnectionInput("server name is empty")
	}
	switch normalizeTransport(srv) {
	case "stdio":
		if strings.TrimSpace(srv.Command) == "" {
			return res, errTestConnectionInput("stdio transport requires a command")
		}
	case "streamable_http", "sse", "websocket":
		if strings.TrimSpace(srv.URL) == "" {
			return res, errTestConnectionInput(strings.ToLower(strings.TrimSpace(srv.Transport)) + " transport requires a url")
		}
	default:
		return res, errTestConnectionInput("unknown transport " + strings.TrimSpace(srv.Transport))
	}
	startCtx, cancel := context.WithTimeout(ctx, StartupTimeoutFor(srv))
	defer cancel()
	sess, err := Start(startCtx, home, dir, srv)
	if err != nil {
		res.Error = err.Error()
		return finish(), nil
	}
	metas, err := sess.ListToolMetas(startCtx)
	if err != nil {
		// The process is signalled before the close, so the teardown does not
		// spend the SDK's terminate duration waiting on a server that is
		// already not answering.
		_ = sess.Abort()
		res.Error = err.Error()
		return finish(), nil
	}
	_ = sess.Abort()
	for _, meta := range metas {
		tool := TestConnectionTool{}
		if name, ok := meta["name"].(string); ok {
			tool.Name = strings.TrimSpace(name)
		}
		if tool.Name == "" {
			continue
		}
		if desc, ok := meta["description"].(string); ok {
			tool.Description = CapDescription(strings.TrimSpace(desc))
		}
		res.Tools = append(res.Tools, tool)
	}
	res.OK = true
	res.ToolCount = len(res.Tools)
	return finish(), nil
}

// testConnectionInputError marks a request that never became a connection
// attempt, so callers can answer "your entry is unusable" (400) instead of
// "the server did not answer" (a result).
type testConnectionInputError struct{ msg string }

func (e testConnectionInputError) Error() string { return e.msg }

func errTestConnectionInput(msg string) error { return testConnectionInputError{msg: msg} }

// IsTestConnectionInputError reports whether err says the entry could not be
// tried at all, rather than that trying it failed.
func IsTestConnectionInputError(err error) bool {
	var target testConnectionInputError
	return errors.As(err, &target)
}
