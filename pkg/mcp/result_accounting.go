package mcp

import (
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// result_accounting.go — bookkeeping for MCP response compression.
//
// The TOON re-encoding in ProcessToolResultJSON shrinks results before they
// enter context, but on its own it is unaccounted and unrecoverable: nothing
// records how much was saved, and the pre-compression response is gone. That
// breaks the same feedback loop shell compression relies on, where retrieval
// counts reveal a filter that hides detail the agent actually needs.
//
// Accounting is best-effort by design. Compression already happened by the time
// this runs, so a storage failure must never change the response.

// recordMCPCompression stores the original response for recovery and returns
// the response to hand back, with a retrieval marker appended when a record was
// written. Without an accounting target it returns compressed unchanged.
func recordMCPCompression(acct accounting, serverName, toolName, original, compressed string) string {
	if strings.TrimSpace(acct.stateRoot) == "" {
		return compressed
	}
	c := tool.CompressorFor(acct.stateRoot)
	if c == nil {
		return compressed
	}
	out, _ := c.RecordMCPResult(serverName, toolName, original, compressed, acct.sessionID)
	if strings.TrimSpace(out) == "" {
		return compressed
	}
	return out
}
