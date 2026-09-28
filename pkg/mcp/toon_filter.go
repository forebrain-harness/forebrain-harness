package mcp

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// toonMinBytes is the smallest text block worth re-encoding. Below this the
// TOON header overhead can exceed the savings, and the token difference is
// noise either way.
const toonMinBytes = 512

// toonEnabled reports whether MCP TOON re-encoding is on. It defaults to on and
// is disabled only by an explicit opt-out, matching how the other MCP output
// knobs in this package behave.
func toonEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("FOREBRAIN_MCP_TOON"))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// applyTOONToResultJSON rewrites JSON-bearing text blocks of an MCP tool result
// into TOON, which is materially denser for the uniform arrays-of-objects that
// MCP servers overwhelmingly return (issue lists, search hits, row sets).
//
// It walks the decoded envelope and rewrites every `{"type":"text","text":...}`
// block whose text parses as JSON, mirroring boost's per-content-item approach.
// Anything it cannot improve is left exactly as-is, and any structural surprise
// returns the original string, so this is fail-open: a result is never lost or
// mangled by a filter miss.
//
// It returns the possibly-rewritten JSON and the number of bytes saved.
func applyTOONToResultJSON(raw string) (string, int) {
	if !toonEnabled() || len(raw) < toonMinBytes {
		return raw, 0
	}
	var envelope any
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return raw, 0
	}
	// Images carry large base64 payloads that must survive untouched, and the
	// existing offload path already special-cases them.
	if containsImageBlock(envelope) {
		return raw, 0
	}
	if !rewriteTextBlocks(envelope) {
		return raw, 0
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		return raw, 0
	}
	// Only keep the rewrite when it actually shrank the payload; re-encoding can
	// otherwise change key order for no benefit.
	if len(out) >= len(raw) {
		return raw, 0
	}
	return string(out), len(raw) - len(out)
}

// rewriteTextBlocks converts qualifying text blocks in place and reports
// whether anything changed.
func rewriteTextBlocks(v any) bool {
	changed := false
	switch x := v.(type) {
	case map[string]any:
		if typ, _ := x["type"].(string); typ == "text" {
			if text, ok := x["text"].(string); ok && len(text) >= toonMinBytes {
				if encoded, ok := tool.JSONToTOON(text); ok && len(encoded) < len(text) {
					x["text"] = encoded
					changed = true
				}
			}
		}
		for _, child := range x {
			if rewriteTextBlocks(child) {
				changed = true
			}
		}
	case []any:
		for _, child := range x {
			if rewriteTextBlocks(child) {
				changed = true
			}
		}
	}
	return changed
}
