package mcp

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// issueListResult builds an MCP envelope whose text block is the kind of
// uniform array-of-objects that MCP servers return, which is exactly the shape
// TOON compresses best.
func issueListResult(n int) string {
	rows := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, map[string]any{
			"number": i + 1,
			"title":  "some issue title that is reasonably long",
			"state":  "open",
			"author": "octocat",
		})
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		panic(err)
	}
	envelope, err := json.Marshal(map[string]any{
		"content": []any{map[string]any{"type": "text", "text": string(payload)}},
	})
	if err != nil {
		panic(err)
	}
	return string(envelope)
}

func TestApplyTOONCompressesUniformJSONArray(t *testing.T) {
	raw := issueListResult(40)
	got, saved := applyTOONToResultJSON(raw)
	if saved <= 0 {
		t.Fatalf("expected TOON to save bytes, got %d (out=%q)", saved, got)
	}
	if len(got) >= len(raw) {
		t.Fatalf("output not smaller: %d -> %d", len(raw), len(got))
	}
	// The envelope must still be valid JSON with the text block in place.
	var envelope struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(got), &envelope); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(envelope.Content) != 1 || envelope.Content[0].Type != "text" {
		t.Fatalf("envelope shape changed: %+v", envelope.Content)
	}
	// Field names must survive; TOON keeps them in the tabular header.
	for _, key := range []string{"number", "title", "state", "author"} {
		if !strings.Contains(envelope.Content[0].Text, key) {
			t.Errorf("TOON output lost field %q", key)
		}
	}
	// Data must survive too.
	if !strings.Contains(envelope.Content[0].Text, "octocat") {
		t.Errorf("TOON output lost row data")
	}
	t.Logf("MCP result: %d -> %d bytes (%d%% saved, ~%d tokens kept out)",
		len(raw), len(got), 100*saved/len(raw), saved/4)
}

func TestApplyTOONLeavesNonJSONTextAlone(t *testing.T) {
	prose := strings.Repeat("this is a plain prose answer with no structure. ", 40)
	raw, err := json.Marshal(map[string]any{
		"content": []any{map[string]any{"type": "text", "text": prose}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, saved := applyTOONToResultJSON(string(raw))
	if saved != 0 || got != string(raw) {
		t.Fatalf("prose must pass through unchanged, saved=%d", saved)
	}
}

func TestApplyTOONSkipsImagePayloads(t *testing.T) {
	// Image blocks carry base64 that must survive byte-for-byte.
	raw, err := json.Marshal(map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": strings.Repeat(`{"a":1},`, 200)},
			map[string]any{"type": "image", "data": strings.Repeat("A", 1024)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, saved := applyTOONToResultJSON(string(raw))
	if saved != 0 || got != string(raw) {
		t.Fatalf("image-bearing result must pass through unchanged, saved=%d", saved)
	}
}

func TestApplyTOONFailsOpenOnInvalidJSON(t *testing.T) {
	raw := "{not json at all" + strings.Repeat(" padding", 100)
	got, saved := applyTOONToResultJSON(raw)
	if saved != 0 || got != raw {
		t.Fatalf("invalid JSON must pass through unchanged, saved=%d", saved)
	}
}

func TestApplyTOONSkipsSmallResults(t *testing.T) {
	raw := `{"content":[{"type":"text","text":"[{\"a\":1}]"}]}`
	got, saved := applyTOONToResultJSON(raw)
	if saved != 0 || got != raw {
		t.Fatalf("small result must pass through unchanged, saved=%d", saved)
	}
}

func TestToonDisabledByEnv(t *testing.T) {
	t.Setenv("FOREBRAIN_MCP_TOON", "off")
	raw := issueListResult(40)
	got, saved := applyTOONToResultJSON(raw)
	if saved != 0 || got != raw {
		t.Fatalf("opt-out must disable TOON, saved=%d", saved)
	}
}

func TestProcessToolResultJSONAppliesTOONBeforeTruncation(t *testing.T) {
	// Pick a budget that straddles the payload: the raw result is over the limit
	// and would be offloaded to a file, while the TOON-encoded result fits. That
	// is the whole point of compressing before the size check.
	raw := issueListResult(40)
	toonOut, saved := applyTOONToResultJSON(raw)
	if saved <= 0 {
		t.Fatalf("precondition: TOON did not compress the fixture")
	}
	budgetChars := (len(raw) + len(toonOut)) / 2
	t.Setenv("MAX_MCP_OUTPUT_TOKENS", strconv.Itoa(budgetChars/4))
	out, err := ProcessToolResultJSON(t.TempDir(), "github", "list_issues", raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "OUTPUT TRUNCATED") || strings.Contains(out, "Large MCP result") {
		t.Fatalf("TOON should have avoided truncation/offload, got: %q", out[:min(200, len(out))])
	}
	if !strings.Contains(out, "octocat") {
		t.Fatalf("row data lost: %q", out[:min(200, len(out))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
