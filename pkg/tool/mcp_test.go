package tool

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestActionAllowsApprovalUpdate(t *testing.T) {
	if !ActionAllowsApprovalUpdate("mcp__demo__read", `{"mcp_approval_mode":"auto"}`) {
		t.Fatal("auto mode must allow a remembered approval")
	}
	for _, mode := range []string{"prompt", "writes", "approve"} {
		if ActionAllowsApprovalUpdate("mcp__demo__write", `{"mcp_approval_mode":"`+mode+`"}`) {
			t.Fatalf("mode %q allowed a remembered approval", mode)
		}
	}
	if !ActionAllowsApprovalUpdate("shell", `{}`) {
		t.Fatal("non-MCP action was restricted")
	}
}

// newTestCompressor builds a compressor with only a store, which is all the MCP
// accounting path needs (compression already happened upstream).
func newTestCompressor(t *testing.T) *Compressor {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &Compressor{store: store}
}

// mcpEnvelope renders a realistic MCP tool result envelope.
func mcpEnvelope(text string) string {
	b, _ := json.Marshal(map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
	})
	return string(b)
}

// The marker must never be concatenated onto the envelope as trailing text: the
// result is parsed downstream (image detection, offload) and by the agent's MCP
// client, so a corrupted envelope would silently change behaviour.
func TestRecordMCPResultKeepsEnvelopeValidJSON(t *testing.T) {
	c := newTestCompressor(t)
	original := mcpEnvelope(strings.Repeat("original padding line\n", 400))
	compressed := mcpEnvelope("tiny")

	out, meta := c.RecordMCPResult("github", "list_issues", original, compressed, "sid-1")

	if !meta.Applied {
		t.Fatalf("meta.Applied = false, want true")
	}
	if meta.RetrieveID <= 0 {
		t.Fatalf("meta.RetrieveID = %d, want a stored id", meta.RetrieveID)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("result is not valid JSON after marker injection: %v\nout: %s", err, out)
	}
	content, ok := envelope["content"].([]any)
	if !ok {
		t.Fatalf("content array missing from %s", out)
	}
	if len(content) != 2 {
		t.Fatalf("content has %d blocks, want 2 (payload + marker)", len(content))
	}
	last, _ := content[1].(map[string]any)
	text, _ := last["text"].(string)
	if !strings.Contains(text, "retrieve_output") {
		t.Errorf("marker block text = %q, want a retrieve_output citation", text)
	}
	if typ, _ := last["type"].(string); typ != "text" {
		t.Errorf("marker block type = %q, want \"text\"", typ)
	}
}

// The stored original must be recoverable, which is what makes the compression
// safe rather than merely smaller.
func TestRecordMCPResultStoresRecoverableOriginal(t *testing.T) {
	c := newTestCompressor(t)
	original := mcpEnvelope(strings.Repeat("recoverable payload\n", 400))
	compressed := mcpEnvelope("tiny")

	_, meta := c.RecordMCPResult("github", "list_issues", original, compressed, "sid-1")
	if meta.RetrieveID <= 0 {
		t.Fatalf("no record stored")
	}
	entry, err := c.store.Get(meta.RetrieveID)
	if err != nil {
		t.Fatalf("Get(%d): %v", meta.RetrieveID, err)
	}
	if entry.OriginalOutput != original {
		t.Errorf("stored original does not round-trip")
	}
	if entry.Kind != KindMCP {
		t.Errorf("Kind = %q, want %q", entry.Kind, KindMCP)
	}
	if entry.Command != "github/list_issues" {
		t.Errorf("Command = %q, want \"github/list_issues\"", entry.Command)
	}
	if entry.CapabilityID != MCPCapabilityTOON {
		t.Errorf("CapabilityID = %q, want %q", entry.CapabilityID, MCPCapabilityTOON)
	}
	if entry.SessionID != "sid-1" {
		t.Errorf("SessionID = %q, want \"sid-1\"", entry.SessionID)
	}
}

// A saving too small to be worth a recovery record must not create one, so the
// store does not fill with noise. This mirrors the shell path's gate.
func TestRecordMCPResultSkipsInsignificantSavings(t *testing.T) {
	c := newTestCompressor(t)
	original := mcpEnvelope(strings.Repeat("x", 4000))
	compressed := mcpEnvelope(strings.Repeat("x", 3990)) // ~0% saved

	out, meta := c.RecordMCPResult("srv", "tool", original, compressed, "sid")
	if meta.RetrieveID != 0 {
		t.Errorf("RetrieveID = %d, want 0 for an insignificant saving", meta.RetrieveID)
	}
	if out != compressed {
		t.Errorf("output was modified for an insignificant saving")
	}
}

// Fail-open: a compression that did not shrink anything is not a compression.
func TestRecordMCPResultIgnoresNonShrinkingResult(t *testing.T) {
	c := newTestCompressor(t)
	same := mcpEnvelope("identical")
	out, meta := c.RecordMCPResult("srv", "tool", same, same, "sid")
	if meta.Applied {
		t.Errorf("Applied = true for a non-shrinking result")
	}
	if out != same {
		t.Errorf("output modified for a non-shrinking result")
	}
}

// An envelope shape this layer does not recognise must still be returned intact
// rather than being wrapped or mangled to fit the marker.
func TestRecordMCPResultLeavesUnknownShapeIntact(t *testing.T) {
	c := newTestCompressor(t)
	original := strings.Repeat("plain text, not an envelope\n", 400)
	compressed := "tiny"

	out, meta := c.RecordMCPResult("srv", "tool", original, compressed, "sid")
	if out != compressed {
		t.Errorf("output = %q, want the compressed form unchanged", out)
	}
	// The record is still worth keeping even without a citable marker.
	if meta.RetrieveID <= 0 {
		t.Errorf("RetrieveID = %d, want the record stored anyway", meta.RetrieveID)
	}
}

func TestMCPTarget(t *testing.T) {
	cases := []struct{ server, tool, want string }{
		{"github", "list_issues", "github/list_issues"},
		{"", "list_issues", "list_issues"},
		{"github", "", "github"},
		{"", "", "mcp"},
		{"  github  ", "  list_issues  ", "github/list_issues"},
	}
	for _, tc := range cases {
		if got := MCPTarget(tc.server, tc.tool); got != tc.want {
			t.Errorf("MCPTarget(%q, %q) = %q, want %q", tc.server, tc.tool, got, tc.want)
		}
	}
}

func TestRecordMCPResultNilSafe(t *testing.T) {
	var c *Compressor
	out, meta := c.RecordMCPResult("srv", "tool", "original", "compressed", "sid")
	if out != "compressed" || meta.Applied {
		t.Errorf("nil compressor was not fail-open: out=%q applied=%v", out, meta.Applied)
	}
}
