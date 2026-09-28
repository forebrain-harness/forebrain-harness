package mcp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const defaultMaxMCPOutputTokens = 25000

// accounting carries what the outfilter store needs to attribute a compression
// to a state root and session. A zero value disables accounting, leaving the
// compression behaviour itself unchanged.
type accounting struct {
	stateRoot string
	sessionID string
}

func processToolResultJSON(home, serverName, toolName, raw string, acct accounting) (string, error) {
	out := strings.TrimSpace(raw)
	if out == "" {
		return out, nil
	}
	processed, err := persistBinaryResourceContent(home, serverName, out)
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
	if !largeOutputFilesEnabled() || strings.TrimSpace(home) == "" || resultJSONContainsImage(out) {
		return truncateMCPOutput(out, maxChars), nil
	}
	path, err := persistMCPResult(home, serverName, toolName, ".json", []byte(out))
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

func persistBinaryResourceContent(home, serverName, raw string) (string, error) {
	if strings.TrimSpace(home) == "" {
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
		path, err := persistMCPResult(home, serverName, "blob", ext, bytes)
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

func persistMCPResult(home, serverName, toolName, ext string, content []byte) (string, error) {
	dir := filepath.Join(strings.TrimSpace(home), "state", "mcp-results")
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
