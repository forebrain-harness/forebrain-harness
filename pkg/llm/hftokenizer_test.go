package llm

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkoukk/tiktoken-go"
)

// testTokenizerJSONPath is DeepSeek V3's tokenizer.json, the real-world
// tokenizer the BPE implementation is checked against.
const testTokenizerJSONPath = "testdata/deepseek_v3_tokenizer.json"

func TestHFTokenizerLoads(t *testing.T) {
	tok, err := LoadHFTokenizer(testTokenizerJSONPath)
	if err != nil {
		t.Fatalf("LoadHFTokenizer error: %v", err)
	}
	if tok == nil {
		t.Fatal("expected non-nil tokenizer")
	}
	if tok.Label() != "hf-bpe" {
		t.Fatalf("label=%q, want hf-bpe", tok.Label())
	}
	if len(tok.vocab) == 0 {
		t.Fatal("vocab is empty")
	}
	if len(tok.merges) == 0 {
		t.Fatal("merges is empty")
	}
	t.Logf("vocab size=%d, merges=%d, added_tokens=%d", len(tok.vocab), len(tok.merges), len(tok.addedTokens))
}

func TestHFTokenizerEncode(t *testing.T) {
	tok, err := LoadHFTokenizer(testTokenizerJSONPath)
	if err != nil {
		t.Fatalf("LoadHFTokenizer error: %v", err)
	}

	// Ground truth from Python tokenizers.Tokenizer.from_file()
	// (raw tokenizer, not AutoTokenizer which has Chinese text bugs)
	tests := []struct {
		text string
		want int
	}{
		{"Hello!", 2},
		{"Hello, world!", 4},
		{"The quick brown fox jumps over the lazy dog.", 10},
		{"function main() { return 42; }", 9},
		{"1234567890", 4},
		{"", 0},
		{"A", 1},
		{"word", 1},
	}

	for _, tc := range tests {
		got := tok.Encode(tc.text)
		if got != tc.want {
			t.Errorf("Encode(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}
}

func TestHFTokenizerChineseText(t *testing.T) {
	// Use raw tokenizer ground truth (AutoTokenizer drops Chinese)
	tok, err := LoadHFTokenizer(testTokenizerJSONPath)
	if err != nil {
		t.Fatalf("LoadHFTokenizer error: %v", err)
	}

	// Chinese text should produce non-zero tokens (unlike AutoTokenizer which drops them)
	// Ground truth from raw tokenizers.Tokenizer.from_file():
	// "你好世界" -> [30594, 3427] = 2 tokens
	got := tok.Encode("你好世界")
	if got != 2 {
		t.Errorf("Encode(%q) = %d, want 2", "你好世界", got)
	}
}

func TestHFTokenizerConfigure(t *testing.T) {
	resetTokestimateStateForTest()
	defer resetTokestimateStateForTest()

	// Use Configure (not ConfigureHFTokenizer directly) so st.useHF is set
	te := TokenEstimateOptions{
		TokenizerPath: testTokenizerJSONPath,
	}
	if err := Configure(te); err != nil {
		t.Fatalf("Configure error: %v", err)
	}

	// Wait for background load
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !hfTokenizerPending() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if hfTokenizerPending() {
		t.Fatal("HF tokenizer still pending after timeout")
	}

	hft := activeHFTokenizer()
	if hft == nil {
		t.Fatal("expected HF tokenizer to be active")
	}

	// Label should include hf: prefix
	label := Label()
	if label == "" {
		t.Fatal("expected non-empty label")
	}
	t.Logf("Label after configure: %s", label)

	// EstimateText should use HF tokenizer
	got := EstimateText("Hello, world!")
	if got != 4 {
		t.Errorf("EstimateText(\"Hello, world!\") = %d, want 4", got)
	}
}

func TestHFTokenizerSmallSynthetic(t *testing.T) {
	// Test with a minimal synthetic tokenizer.json to verify BPE logic
	// without requiring the full DeepSeek tokenizer
	tmpDir := t.TempDir()
	tokPath := filepath.Join(tmpDir, "tokenizer.json")

	// Minimal tokenizer: BPE with a tiny vocab
	minimalJSON := `{
		"version": "1.0",
		"added_tokens": [],
		"pre_tokenizer": null,
		"model": {
			"type": "BPE",
			"vocab": {
				"a": 0, "b": 1, "c": 2, "ab": 3, "abc": 4
			},
			"merges": ["a b", "ab c"]
		}
	}`

	if err := os.WriteFile(tokPath, []byte(minimalJSON), 0644); err != nil {
		t.Fatalf("write temp tokenizer: %v", err)
	}

	tok, err := LoadHFTokenizer(tokPath)
	if err != nil {
		t.Fatalf("LoadHFTokenizer error: %v", err)
	}

	// "abc" should merge to a single token via "a b" then "ab c"
	got := tok.Encode("abc")
	if got != 1 {
		t.Errorf("Encode(\"abc\") = %d, want 1 (full merge)", got)
	}

	// "ab" should merge to a single token
	got = tok.Encode("ab")
	if got != 1 {
		t.Errorf("Encode(\"ab\") = %d, want 1", got)
	}

	// "ac" cannot merge (no "a c" merge rule) -> 2 tokens
	got = tok.Encode("ac")
	if got != 2 {
		t.Errorf("Encode(\"ac\") = %d, want 2", got)
	}
}

// The helpers below were production functions that only the tests in this
// package ever called. They live here so the production files carry no
// unused code while the tests keep exercising the live code underneath.

func hfTokenizerPending() bool {
	hfState.mu.Lock()
	defer hfState.mu.Unlock()
	return hfState.pending
}

func hfTokenizerLabel() string {
	hfState.mu.Lock()
	defer hfState.mu.Unlock()
	if hfState.tok != nil {
		return hfState.label
	}
	return ""
}

// resetHFStateForTest resets the HF tokenizer state for testing.
func resetHFStateForTest() {
	hfState.mu.Lock()
	defer hfState.mu.Unlock()
	hfState.tok = nil
	hfState.pending = false
	hfState.gen = 0
	hfState.label = ""
}

// The helpers below were production functions that only the tests in this
// package ever called. They live here so the production files carry no
// unused code while the tests keep exercising the live code underneath.

func Label() string {
	// If an HF tokenizer is configured, report its label
	if usingHFTokenizer() {
		if l := hfTokenizerLabel(); l != "" {
			return "hf:" + l
		}
		if hfTokenizerPending() {
			return "rune_div4"
		}
		return "rune_div4"
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if st.heur || st.pending {
		return "rune_div4"
	}
	if st.label != "" {
		return "tiktoken:" + st.label
	}
	return "tiktoken:" + tiktoken.MODEL_CL100K_BASE
}
