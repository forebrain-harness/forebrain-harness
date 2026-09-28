package mcp

import (
	"errors"
	"strings"
	"testing"
)

type statusError struct {
	code int
	msg  string
}

func (e statusError) Error() string { return e.msg }
func (e statusError) Code() int     { return e.code }

func TestSessionExpiredRequiresHTTP404AndJSONRPCCode(t *testing.T) {
	err := statusError{code: 404, msg: `{"error":{"code":-32001,"message":"Session not found"}}`}
	if !IsSessionExpired(err) {
		t.Fatalf("must detect session expiry")
	}
	if IsSessionExpired(statusError{code: 404, msg: `not found`}) {
		t.Fatalf("generic 404 must not be treated as session expiry")
	}
	if IsSessionExpired(statusError{code: 500, msg: `{"error":{"code":-32001}}`}) {
		t.Fatalf("json-rpc code without HTTP 404 must not be treated as session expiry")
	}
	if IsSessionExpired(errors.New(`{"error":{"code":-32001}}`)) {
		t.Fatalf("missing HTTP status must not be treated as session expiry")
	}
}

func TestCapDescription(t *testing.T) {
	long := strings.Repeat("a", 3000)
	if got := CapDescription(long); len(got) != MaxDescriptionLength {
		t.Fatalf("description cap length=%d want %d", len(got), MaxDescriptionLength)
	}
	short := "short description"
	if got := CapDescription(short); got != short {
		t.Fatalf("short description changed: %q", got)
	}
}

func TestBuildMCPToolNameNormalizesServerAndTool(t *testing.T) {
	if got := BuildToolName("my server.io", "Read Thing"); got != "mcp__my_server_io__Read_Thing" {
		t.Fatalf("tool name=%q", got)
	}
	if got := BuildToolName("  weird...server  ", "  "); got != "mcp__weird_server__x" {
		t.Fatalf("fallback-normalized tool name=%q", got)
	}
}

func TestExpandEnvVarsInStringSupportsDefaultAndMissingVars(t *testing.T) {
	lookup := func(name string) (string, bool) {
		switch name {
		case "TOKEN":
			return "abc123", true
		default:
			return "", false
		}
	}
	got, missing := ExpandEnvVarsInString(
		"Bearer ${TOKEN} ${MISSING:-fallback} ${NOPE}",
		lookup,
	)
	if got != "Bearer abc123 fallback ${NOPE}" {
		t.Fatalf("expanded=%q", got)
	}
	if len(missing) != 1 || missing[0] != "NOPE" {
		t.Fatalf("missing=%v", missing)
	}
}

func TestRetryAfterSessionExpiredReconnectsAndRetriesOnce(t *testing.T) {
	calls := 0
	reconnects := 0
	got, err := RetryAfterSessionExpired(
		func() (string, error) {
			calls++
			if calls == 1 {
				return "", statusError{code: 404, msg: `{"error":{"code": -32001}}`}
			}
			return "ok", nil
		},
		func() error {
			reconnects++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("RetryAfterSessionExpired error: %v", err)
	}
	if got != "ok" || calls != 2 || reconnects != 1 {
		t.Fatalf("got=%q calls=%d reconnects=%d", got, calls, reconnects)
	}
}
