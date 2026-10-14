package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

const defaultMaxMCPOutputTokens = 25000

// accounting carries what the outfilter store needs to attribute a compression
// to a state root and session. A zero value disables accounting, leaving the
// compression behaviour itself unchanged.
type accounting struct {
	stateRoot string
	sessionID string
}

// ToolResultContent turns one raw MCP tool result into the content the model
// receives: the text envelope processToolResultJSON produces (image blocks
// replaced by a saved-path line, large outputs offloaded or truncated) plus one
// image part per image block in the result.
//
// resultsDir is the directory results are written into — the primary agent
// workspace's state/mcp-results, injected by Session.SetResultsDir; it is NOT
// home (see Session.resultsRootValue). imageRefusal, when non-empty, is the
// reason this session's model may not be handed pixels: the image parts are
// dropped and only the text envelope is returned, so a text-only model gets an
// actionable line instead of a 400.
//
// The pixels are handed over as ContentParts because the whole point of the
// change is that a screenshot an MCP server returned reaches the model as an
// image, not as a base64 blob in a text envelope. Binary blocks are still
// persisted first, so the audit trail and retrieve_output keep working. The
// surface-facing metadata (path, MIME, dimensions, byte counts — never base64)
// is captured onto ctx as the tool step's Output, so both surfaces can draw a
// thumbnail by path.
func ToolResultContent(ctx context.Context, resultsDir, serverName, toolName, raw string, acct accounting, imageRefusal string) ([]llm.ContentPart, error) {
	stripped, imageParts, surface, err := extractResultImageParts(resultsDir, serverName, raw)
	if err != nil {
		return nil, err
	}
	if len(surface) > 0 {
		tool.CaptureToolOutput(ctx, surface)
	}
	if strings.TrimSpace(imageRefusal) != "" {
		imageParts = nil
	}
	text, err := processToolResultJSON(resultsDir, serverName, toolName, stripped, acct)
	if err != nil {
		return nil, err
	}
	parts := make([]llm.ContentPart, 0, 1+len(imageParts))
	parts = append(parts, llm.Text(text))
	parts = append(parts, imageParts...)
	return parts, nil
}

// extractResultImageParts persists every image block in an MCP tool result and
// returns the result JSON with each block replaced by a saved-path text line,
// one image part per block, and the surface-facing map of the first image (nil
// when the result has none).
//
// The replacement keeps the text envelope free of base64 — a block's data is
// hundreds of kilobytes that must never reach the model as text nor a surface
// event — while the part carries the pixels. A block whose MIME is not one of
// the four a provider accepts is still persisted and named, but yields no part.
func extractResultImageParts(resultsDir, serverName, raw string) (string, []llm.ContentPart, map[string]any, error) {
	if strings.TrimSpace(resultsDir) == "" {
		return raw, nil, nil, nil
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return raw, nil, nil, nil
	}
	content, ok := root["content"].([]any)
	if !ok {
		return raw, nil, nil, nil
	}
	var parts []llm.ContentPart
	var surface map[string]any
	changed := false
	for i, item := range content {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if typ, _ := m["type"].(string); !strings.EqualFold(strings.TrimSpace(typ), "image") {
			continue
		}
		data, _ := m["data"].(string)
		if strings.TrimSpace(data) == "" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			continue
		}
		mimeType, _ := m["mimeType"].(string)
		mimeType = strings.TrimSpace(mimeType)
		if mimeType == "" {
			mimeType = llm.ImageMIMEType("", decoded)
		}
		path, err := persistMCPResult(resultsDir, serverName, "image", extensionForMIME(mimeType), decoded)
		if err != nil {
			content[i] = map[string]any{
				"type": "text",
				"text": fmt.Sprintf("Binary content (%s, %d bytes) could not be saved: %v", fallbackUnknown(mimeType), len(decoded), err),
			}
			changed = true
			continue
		}
		content[i] = map[string]any{
			"type": "text",
			"text": fmt.Sprintf("Binary content saved to %s (%s, %d bytes).", path, fallbackUnknown(mimeType), len(decoded)),
		}
		changed = true
		switch mimeType {
		case "image/jpeg", "image/png", "image/gif", "image/webp":
			part, res, err := llm.ImagePartWithin(path, mimeType, decoded, llm.DefaultMaxImageBytes)
			if err != nil {
				return "", nil, nil, err
			}
			parts = append(parts, part)
			if surface == nil {
				surface = tool.ImageReadOutput(path, path, res)
			}
		}
	}
	if !changed {
		return raw, nil, nil, nil
	}
	b, err := json.Marshal(root)
	if err != nil {
		return "", nil, nil, err
	}
	return string(b), parts, surface, nil
}

// processToolResultJSON persists an MCP result's binary resource blocks, applies
// the TOON rewrite, and either truncates or offloads a result that is still too
// large. resultsDir is the directory results are written into (not a home root).
func processToolResultJSON(resultsDir, serverName, toolName, raw string, acct accounting) (string, error) {
	out := strings.TrimSpace(raw)
	if out == "" {
		return out, nil
	}
	processed, err := persistBinaryResourceContent(resultsDir, serverName, out)
	if err != nil {
		return "", err
	}
	out = processed
	// Re-encode JSON text blocks as TOON before the size check, so a large
	// result can come in under the limit instead of being truncated or offloaded
	// to a file the agent then has to spend a turn reading back.
	//
	// The pre-TOON form is the original for both accounting and recovery: it is
	// what would have entered context had this layer not run.
	beforeTOON := out
	if toonOut, saved := applyTOONToResultJSON(out); saved > 0 {
		out = recordMCPCompression(acct, serverName, toolName, beforeTOON, toonOut)
	}
	maxChars := maxMCPOutputChars()
	if len(out) <= maxChars {
		return out, nil
	}
	if !largeOutputFilesEnabled() || strings.TrimSpace(resultsDir) == "" || resultJSONContainsImage(out) {
		return truncateMCPOutput(out, maxChars), nil
	}
	path, err := persistMCPResult(resultsDir, serverName, toolName, ".json", []byte(out))
	if err != nil {
		return truncateMCPOutput(out, maxChars), nil
	}
	return fmt.Sprintf(
		"Large MCP result (%d characters) saved to %s. Read this file if you need the full output; otherwise use pagination or filters to retrieve a smaller result.",
		len(out),
		path,
	), nil
}

func maxMCPOutputChars() int {
	n := defaultMaxMCPOutputTokens
	if raw := strings.TrimSpace(os.Getenv("MAX_MCP_OUTPUT_TOKENS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			n = parsed
		}
	}
	return n * 4
}

func largeOutputFilesEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ENABLE_MCP_LARGE_OUTPUT_FILES"))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func truncateMCPOutput(s string, maxChars int) string {
	if maxChars < 0 {
		maxChars = 0
	}
	if len(s) > maxChars {
		s = s[:maxChars]
	}
	return s + "\n\n[OUTPUT TRUNCATED - exceeded MCP output limit]\n\nUse pagination or filtering on the MCP server to retrieve specific portions of the data."
}

func resultJSONContainsImage(raw string) bool {
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return false
	}
	return containsImageBlock(v)
}

func containsImageBlock(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if typ, _ := x["type"].(string); typ == "image" {
			return true
		}
		for _, child := range x {
			if containsImageBlock(child) {
				return true
			}
		}
	case []any:
		for _, child := range x {
			if containsImageBlock(child) {
				return true
			}
		}
	}
	return false
}

// persistBinaryResourceContent persists every embedded-resource blob in the
// result and replaces it with a saved-path text line, so base64 never rides in
// the text envelope the model reads.
func persistBinaryResourceContent(resultsDir, serverName, raw string) (string, error) {
	if strings.TrimSpace(resultsDir) == "" {
		return raw, nil
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return raw, nil
	}
	content, ok := root["content"].([]any)
	if !ok {
		return raw, nil
	}
	changed := false
	for i, item := range content {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		resource, ok := m["resource"].(map[string]any)
		if !ok {
			continue
		}
		blob, _ := resource["blob"].(string)
		if strings.TrimSpace(blob) == "" {
			continue
		}
		bytes, err := base64.StdEncoding.DecodeString(blob)
		if err != nil {
			continue
		}
		mimeType, _ := resource["mimeType"].(string)
		ext := extensionForMIME(mimeType)
		path, err := persistMCPResult(resultsDir, serverName, "blob", ext, bytes)
		if err != nil {
			content[i] = map[string]any{
				"type": "text",
				"text": fmt.Sprintf("Binary content (%s, %d bytes) could not be saved: %v", fallbackUnknown(mimeType), len(bytes), err),
			}
		} else {
			content[i] = map[string]any{
				"type": "text",
				"text": fmt.Sprintf("Binary content saved to %s (%s, %d bytes).", path, fallbackUnknown(mimeType), len(bytes)),
			}
		}
		changed = true
	}
	if !changed {
		return raw, nil
	}
	root["content"] = content
	b, err := json.Marshal(root)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// persistMCPResult writes one result file under resultsDir — the directory the
// Session injected, which is the primary agent workspace's state/mcp-results —
// and returns its path. resultsDir is used as the directory directly; both the
// injected value and the Session fallback already include state/mcp-results, so
// there is exactly one place the "state/mcp-results" segment is joined.
func persistMCPResult(resultsDir, serverName, toolName, ext string, content []byte) (string, error) {
	dir := strings.TrimSpace(resultsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if ext == "" {
		ext = ".txt"
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	name := fmt.Sprintf(
		"mcp-%s-%s-%d%s",
		NormalizeName(serverName),
		NormalizeName(toolName),
		time.Now().UnixNano(),
		ext,
	)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func extensionForMIME(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "application/json":
		return ".json"
	case "application/pdf":
		return ".pdf"
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "text/plain":
		return ".txt"
	default:
		parts := strings.Split(strings.TrimSpace(mimeType), "/")
		if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
			return "." + NormalizeName(parts[1])
		}
		return ".bin"
	}
}

func fallbackUnknown(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown type"
	}
	return s
}
