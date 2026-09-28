package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessToolResultJSONPersistsLargeTextOutput(t *testing.T) {
	t.Setenv("MAX_MCP_OUTPUT_TOKENS", "1")
	home := t.TempDir()
	raw := `{"content":[{"type":"text","text":"` + strings.Repeat("x", 128) + `"}]}`

	got, err := ProcessToolResultJSON(home, "docs server", "search", raw)
	if err != nil {
		t.Fatalf("ProcessToolResultJSON error: %v", err)
	}
	if !strings.Contains(got, "saved to") || !strings.Contains(got, "state/mcp-results") {
		t.Fatalf("expected persisted output instructions, got: %s", got)
	}
	files, err := filepath.Glob(filepath.Join(home, "state", "mcp-results", "mcp-docs_server-search-*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one persisted result file, got %v", files)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read persisted result: %v", err)
	}
	if !strings.Contains(string(b), strings.Repeat("x", 64)) {
		t.Fatalf("persisted file missing result content: %q", string(b))
	}
}

func TestProcessToolResultJSONTruncatesLargeOutputWhenPersistenceDisabled(t *testing.T) {
	t.Setenv("MAX_MCP_OUTPUT_TOKENS", "1")
	t.Setenv("ENABLE_MCP_LARGE_OUTPUT_FILES", "0")
	home := t.TempDir()
	raw := `{"content":[{"type":"text","text":"` + strings.Repeat("y", 128) + `"}]}`

	got, err := ProcessToolResultJSON(home, "docs", "search", raw)
	if err != nil {
		t.Fatalf("ProcessToolResultJSON error: %v", err)
	}
	if strings.Contains(got, "saved to") {
		t.Fatalf("expected truncation, got persisted instructions: %s", got)
	}
	if !strings.Contains(got, "OUTPUT TRUNCATED") {
		t.Fatalf("expected truncation marker, got: %s", got)
	}
}

func TestProcessToolResultJSONPersistsBinaryResourceContent(t *testing.T) {
	home := t.TempDir()
	raw := `{"content":[{"type":"resource","resource":{"uri":"file://report.pdf","mimeType":"application/pdf","blob":"cGRmLWJ5dGVz"}}]}`

	got, err := ProcessToolResultJSON(home, "files", "read", raw)
	if err != nil {
		t.Fatalf("ProcessToolResultJSON error: %v", err)
	}
	if strings.Contains(got, "cGRmLWJ5dGVz") {
		t.Fatalf("base64 blob leaked into output: %s", got)
	}
	if !strings.Contains(got, "Binary content saved to") || !strings.Contains(got, ".pdf") {
		t.Fatalf("expected binary saved message, got: %s", got)
	}
	files, err := filepath.Glob(filepath.Join(home, "state", "mcp-results", "mcp-files-blob-*.pdf"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one binary file, got %v", files)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read binary file: %v", err)
	}
	if string(b) != "pdf-bytes" {
		t.Fatalf("binary content=%q", string(b))
	}
}

// The helpers below were production functions that only these tests called:
// each is a thin wrapper over the live function underneath. They live here
// so the production files carry no unused code.

func ProcessToolResultJSON(home, serverName, toolName, raw string) (string, error) {
	return processToolResultJSON(home, serverName, toolName, raw, accounting{})
}
