package mcp

import (
	"errors"
	"strings"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	DefaultToolTimeoutMillis = 100_000_000
	MaxDescriptionLength     = 2048

	// DefaultStartupTimeout is how long one MCP server may take to start and
	// list its tools when its entry does not say. Every server is external I/O
	// the operator configured — a package manager downloading, an HTTP service
	// that is not answering — so the bound is the difference between a first
	// turn that waits and a first turn that never arrives.
	DefaultStartupTimeout = 30 * time.Second
)

// ResolveStartupTimeout converts a configured startup_timeout in seconds into
// the duration the runtime applies. Zero means the default; an unusable value
// is an error rather than a silent substituted bound, so a typo cannot look
// like a working configuration.
func ResolveStartupTimeout(seconds float64) (time.Duration, error) {
	if err := appcfg.ValidateMCPStartupTimeout(seconds); err != nil {
		return 0, err
	}
	if seconds == 0 {
		return DefaultStartupTimeout, nil
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

// StartupTimeoutFor is ResolveStartupTimeout for the places that must not fail:
// an entry that reached a live session has already been validated at load, so a
// rejected value here can only mean a caller built the config struct by hand.
func StartupTimeoutFor(srv appcfg.MCPServerConfig) time.Duration {
	timeout, err := ResolveStartupTimeout(srv.StartupTimeout)
	if err != nil || timeout <= 0 {
		return DefaultStartupTimeout
	}
	return timeout
}

type errorCoder interface {
	Code() int
}

// IsSessionExpired reports whether a call failed because the server dropped the
// session it was made on, which is the one failure a reconnect can recover
// from.
//
// The SDK reports it as ErrSessionMissing — the spec's "HTTP 404 Not Found for
// requests carrying a terminated session id" — and that is the check that
// matters today. The transport-level form below is kept for a transport that
// reports the same thing as a coded error carrying the JSON-RPC -32001 body:
// without it, a recognised expiry would be the only thing standing between a
// dropped session and a tool call that fails for the rest of the session.
func IsSessionExpired(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, mcpsdk.ErrSessionMissing) {
		return true
	}
	coder, ok := err.(errorCoder)
	if !ok || coder.Code() != 404 {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, `"code":-32001`) || strings.Contains(msg, `"code": -32001`)
}

func CapDescription(s string) string {
	if len(s) <= MaxDescriptionLength {
		return s
	}
	return s[:MaxDescriptionLength]
}

func RetryAfterSessionExpired(call func() (string, error), reconnect func() error) (string, error) {
	if call == nil {
		return "", nil
	}
	out, err := call()
	if !IsSessionExpired(err) {
		return out, err
	}
	if reconnect == nil {
		return out, err
	}
	if rerr := reconnect(); rerr != nil {
		return out, rerr
	}
	return call()
}

func ExpandEnvVarsInString(value string, lookup func(string) (string, bool)) (string, []string) {
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	var missing []string
	var b strings.Builder
	for i := 0; i < len(value); {
		if i+2 > len(value) || value[i] != '$' || value[i+1] != '{' {
			b.WriteByte(value[i])
			i++
			continue
		}
		end := strings.IndexByte(value[i+2:], '}')
		if end < 0 {
			b.WriteByte(value[i])
			i++
			continue
		}
		expr := value[i+2 : i+2+end]
		next := i + 2 + end + 1
		name := expr
		def := ""
		hasDefault := false
		if j := strings.Index(expr, ":-"); j >= 0 {
			name = expr[:j]
			def = expr[j+2:]
			hasDefault = true
		}
		if v, ok := lookup(name); ok {
			b.WriteString(v)
		} else if hasDefault {
			b.WriteString(def)
		} else {
			missing = append(missing, name)
			b.WriteString(value[i:next])
		}
		i = next
	}
	return b.String(), missing
}

func ExpandEnvMap(in map[string]string, lookup func(string) (string, bool)) (map[string]string, []string) {
	out := make(map[string]string, len(in))
	var missing []string
	for k, v := range in {
		expanded, miss := ExpandEnvVarsInString(v, lookup)
		out[k] = expanded
		missing = append(missing, miss...)
	}
	return out, missing
}

func NormalizeName(name string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.TrimSpace(name) {
		valid := r >= 'a' && r <= 'z' ||
			r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' ||
			r == '-' ||
			r == '_'
		if valid {
			b.WriteRune(r)
			lastUnderscore = r == '_'
			continue
		}
		if lastUnderscore {
			continue
		}
		b.WriteByte('_')
		lastUnderscore = true
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "x"
	}
	return out
}

func BuildToolName(serverName string, toolName string) string {
	return "mcp__" + NormalizeName(serverName) + "__" + NormalizeName(toolName)
}
