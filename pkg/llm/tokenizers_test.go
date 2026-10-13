package llm

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// deepseekV3TokenizerJSON is a real tokenizer.json used to stand in for a built-in.
const deepseekV3TokenizerJSON = "testdata/deepseek_v3_tokenizer.json"

// allowTokenizerFiles turns tokenizer file access on for one test and restores
// the package state afterwards. Non-downloading tests pass Hubs: []string{}.
func allowTokenizerFiles(t *testing.T) {
	t.Helper()
	ResetTokenizersForTest()
	previous := tokenizerFilesAllowed
	tokenizerFilesAllowed = true
	t.Cleanup(func() {
		tokenizerFilesAllowed = previous
		ResetTokenizersForTest()
	})
}

// swapBuiltinTokenizers replaces the built-in table for one test.
func swapBuiltinTokenizers(toks []BuiltinTokenizer) func() {
	previous := builtinTokenizers
	builtinTokenizers = toks
	return func() { builtinTokenizers = previous }
}

// testBuiltin describes a one-file built-in tokenizer over testdata/deepseek_v3_tokenizer.json.
func testBuiltin(t *testing.T) BuiltinTokenizer {
	t.Helper()
	data, err := os.ReadFile(deepseekV3TokenizerJSON)
	if err != nil {
		t.Fatal(err)
	}
	return BuiltinTokenizer{
		ID:            "test-tok",
		Files:         []TokenizerFile{{Name: "tokenizer.json", SHA256: sha256Hex(data), Size: int64(len(data))}},
		HuggingFace:   "acme/testtok",
		ModelScope:    "acme/testtok",
		ModelPrefixes: []string{"testtok"},
		Providers:     []string{"testing"},
	}
}

// writeBundle writes bt's bundled .gz files into a fresh directory and points
// AssetsDirEnv at it.
func writeBundle(t *testing.T, bt BuiltinTokenizer) {
	t.Helper()
	data, err := os.ReadFile(deepseekV3TokenizerJSON)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, file := range bt.Files {
		dstDir := filepath.Join(dir, "tokenizers", bt.ID)
		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		w, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dstDir, file.Name+".gz"), buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(AssetsDirEnv, dir)
}

func waitForEstimator(t *testing.T, provider, model, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if EstimatorFor(provider, model).Name() == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("EstimatorFor(%q,%q).Name() never became %q (got %q)", provider, model, want, EstimatorFor(provider, model).Name())
}

// Test 1.
func TestBundledTokenizerFilesMatchTable(t *testing.T) {
	assetsDir, err := RepoAssetsDirForTest()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, bt := range builtinTokenizers {
		if seen[bt.ID] {
			t.Errorf("duplicate tokenizer id %q", bt.ID)
		}
		seen[bt.ID] = true
		for _, file := range bt.Files {
			gzPath := filepath.Join(assetsDir, "tokenizers", bt.ID, file.Name+".gz")
			f, err := os.Open(gzPath)
			if err != nil {
				t.Errorf("%s: %v", gzPath, err)
				continue
			}
			gz, err := gzip.NewReader(f)
			if err != nil {
				_ = f.Close()
				t.Errorf("%s: %v", gzPath, err)
				continue
			}
			data, err := io.ReadAll(gz)
			_ = gz.Close()
			_ = f.Close()
			if err != nil {
				t.Errorf("%s: %v", gzPath, err)
				continue
			}
			if int64(len(data)) != file.Size {
				t.Errorf("%s: size = %d, want %d", gzPath, len(data), file.Size)
			}
			if got := sha256Hex(data); got != file.SHA256 {
				t.Errorf("%s: sha256 = %s, want %s", gzPath, got, file.SHA256)
			}
		}
	}
}

// Test 2.
func TestTokenizerIDFor(t *testing.T) {
	ResetTokenizersForTest()
	t.Cleanup(ResetTokenizersForTest)
	if err := Configure(TokenEstimateOptions{
		Hubs:      []string{},
		Overrides: []TokenizerOverride{{Model: "acme-", Tokenizer: "qwen-3.8"}},
	}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		provider, model, want string
	}{
		{"", "deepseek-v4-flash", "deepseek-v4"},
		{"", "deepseek/deepseek-v4-flash", "deepseek-v4"},
		{"", "deepseek-r1-distill-qwen-32b", "qwen-3.8"},
		{"", "glm-5.3", "glm-5"},
		{"", "GLM-4.7", "glm-5"},
		{"", "gpt-5.6-sol", "gpt-o200k"},
		{"", "o3-mini", "gpt-o200k"},
		{"", "kimi-k3", "kimi-k3"},
		{"", "qwen3.8-max", "qwen-3.8"},
		{"", "qwq-32b", "qwen-3.8"},
		{"", "MiniMax-M3", "minimax-m2"},
		{"", "mimo-v2.5-pro", "mimo-v2.5"},
		{"", "gemini-3.7-flash", "gemma-4"},
		{"", "gemma-4-31b-it", "gemma-4"},
		{"", "claude-opus-5-5", "claude-legacy"},
		{"", "grok-4.6", "grok-2"},
		{"anthropic", "opus", "claude-legacy"},
		{"unknown", "foo", "gpt-o200k"},
		{"", "acme-model", "qwen-3.8"}, // config override wins
	}
	for _, tc := range cases {
		if got := TokenizerIDFor(tc.provider, tc.model); got != tc.want {
			t.Errorf("TokenizerIDFor(%q, %q) = %q, want %q", tc.provider, tc.model, got, tc.want)
		}
	}
}

// Test 3.
func TestUnconfiguredEstimatorIsHeuristic(t *testing.T) {
	ResetTokenizersForTest()
	t.Cleanup(ResetTokenizersForTest)
	if got := EstimateText(strings.Repeat("abcd", 4)); got != 4 {
		t.Fatalf("EstimateText(16 runes) = %d, want 4", got)
	}
	if got := EstimatorFor("deepseek", "deepseek-v4-flash").Name(); got != "rune_div4" {
		t.Fatalf("unconfigured EstimatorFor name = %q, want rune_div4", got)
	}
}

// Test 4.
func TestEstimatorSeedsCacheFromBundle(t *testing.T) {
	allowTokenizerFiles(t)
	bt := testBuiltin(t)
	defer swapBuiltinTokenizers([]BuiltinTokenizer{bt})()
	writeBundle(t, bt)

	cache := t.TempDir()
	if err := Configure(TokenEstimateOptions{CacheDir: cache, Hubs: []string{}}); err != nil {
		t.Fatal(err)
	}
	est := EstimatorFor("", "testtok-model")
	if est.Name() != "test-tok" {
		t.Fatalf("estimator id = %q, want test-tok", est.Name())
	}
	if est.Count("hello world") <= 0 {
		t.Fatal("bundled tokenizer counted zero tokens")
	}
	cachePath := filepath.Join(cache, "test-tok", "tokenizer.json")
	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("cache file not extracted: %v", err)
	}
	if sha256Hex(data) != bt.Files[0].SHA256 {
		t.Fatal("cache file does not match the table")
	}
}

// Test 5.
func TestTamperedCacheFileIsReplaced(t *testing.T) {
	allowTokenizerFiles(t)
	bt := testBuiltin(t)
	defer swapBuiltinTokenizers([]BuiltinTokenizer{bt})()
	writeBundle(t, bt)

	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, "test-tok"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "test-tok", "tokenizer.json"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Configure(TokenEstimateOptions{CacheDir: cache, Hubs: []string{}}); err != nil {
		t.Fatal(err)
	}
	if got := EstimatorFor("", "testtok").Name(); got != "test-tok" {
		t.Fatalf("estimator id = %q, want test-tok", got)
	}
	data, err := os.ReadFile(filepath.Join(cache, "test-tok", "tokenizer.json"))
	if err != nil {
		t.Fatal(err)
	}
	if sha256Hex(data) != bt.Files[0].SHA256 {
		t.Fatal("the tampered cache file was not replaced by the bundle")
	}
}

// Test 6.
func TestMissingFileDownloadsFromHub(t *testing.T) {
	allowTokenizerFiles(t)
	bt := testBuiltin(t)
	defer swapBuiltinTokenizers([]BuiltinTokenizer{bt})()
	t.Setenv(AssetsDirEnv, t.TempDir()) // no bundle

	data, err := os.ReadFile(deepseekV3TokenizerJSON)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/acme/testtok/resolve/main/tokenizer.json" {
			_, _ = w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	cache := t.TempDir()
	if err := Configure(TokenEstimateOptions{
		CacheDir:            cache,
		Hubs:                []string{"huggingface"},
		HuggingFaceEndpoint: server.URL,
	}); err != nil {
		t.Fatal(err)
	}
	_ = EstimatorFor("", "testtok") // kicks off the async download
	waitForEstimator(t, "", "testtok", "test-tok")
	if _, err := os.Stat(filepath.Join(cache, "test-tok", "tokenizer.json")); err != nil {
		t.Fatalf("downloaded file missing: %v", err)
	}
}

// Test 7.
func TestDownloadFallsBackToModelScope(t *testing.T) {
	allowTokenizerFiles(t)
	bt := testBuiltin(t)
	defer swapBuiltinTokenizers([]BuiltinTokenizer{bt})()
	t.Setenv(AssetsDirEnv, t.TempDir())

	data, err := os.ReadFile(deepseekV3TokenizerJSON)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/acme/testtok/resolve/main/tokenizer.json":
			http.NotFound(w, r) // HuggingFace has it not
		case "/models/acme/testtok/resolve/master/tokenizer.json":
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cache := t.TempDir()
	if err := Configure(TokenEstimateOptions{
		CacheDir:            cache,
		Hubs:                []string{"huggingface", "modelscope"},
		HuggingFaceEndpoint: server.URL,
		ModelScopeEndpoint:  server.URL,
	}); err != nil {
		t.Fatal(err)
	}
	_ = EstimatorFor("", "testtok")
	waitForEstimator(t, "", "testtok", "test-tok")
}

// Test 8.
func TestDownloadRejectsWrongChecksum(t *testing.T) {
	allowTokenizerFiles(t)
	bt := testBuiltin(t)
	defer swapBuiltinTokenizers([]BuiltinTokenizer{bt})()
	t.Setenv(AssetsDirEnv, t.TempDir())

	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("this is not the tokenizer"))
	}))
	defer server.Close()

	cache := t.TempDir()
	if err := Configure(TokenEstimateOptions{
		CacheDir:            cache,
		Hubs:                []string{"huggingface"},
		HuggingFaceEndpoint: server.URL,
	}); err != nil {
		t.Fatal(err)
	}
	_ = EstimatorFor("", "testtok")
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&hits) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // let the rejecting goroutine finish

	if got := EstimatorFor("", "testtok").Name(); got != "rune_div4" {
		t.Fatalf("estimator after a bad download = %q, want rune_div4", got)
	}
	if _, err := os.Stat(filepath.Join(cache, "test-tok", "tokenizer.json")); !os.IsNotExist(err) {
		t.Fatalf("a file was written despite the checksum mismatch (stat err=%v)", err)
	}
}

// Test 9.
func TestTestBinaryNeverTouchesTokenizerFiles(t *testing.T) {
	ResetTokenizersForTest()
	t.Cleanup(ResetTokenizersForTest)
	// tokenizerFilesAllowed stays at its test-binary default (false).
	bt := testBuiltin(t)
	defer swapBuiltinTokenizers([]BuiltinTokenizer{bt})()
	writeBundle(t, bt)

	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "must not be reached", http.StatusInternalServerError)
	}))
	defer server.Close()

	cache := t.TempDir()
	if err := Configure(TokenEstimateOptions{
		CacheDir:            cache,
		Hubs:                []string{"huggingface"},
		HuggingFaceEndpoint: server.URL,
	}); err != nil {
		t.Fatal(err)
	}
	if got := EstimatorFor("", "testtok").Name(); got != "rune_div4" {
		t.Fatalf("test binary EstimatorFor name = %q, want rune_div4", got)
	}
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("test binary populated the cache: %v", entries)
	}
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("test binary made %d network requests, want 0", got)
	}
}

// Test 10.
func TestOverridePathLoadsLocalTokenizer(t *testing.T) {
	allowTokenizerFiles(t)
	if err := Configure(TokenEstimateOptions{
		CacheDir:  t.TempDir(),
		Hubs:      []string{},
		Overrides: []TokenizerOverride{{Model: "my-", Path: deepseekV3TokenizerJSON}},
	}); err != nil {
		t.Fatal(err)
	}
	est := EstimatorFor("x", "my-model")
	if est.Count("hello") <= 0 {
		t.Fatalf("override path estimator counted zero tokens (name=%q)", est.Name())
	}
}

// Test 11.
func TestKimiTiktokenFromBundle(t *testing.T) {
	ResetTokenizersForTest()
	previous := tokenizerFilesAllowed
	tokenizerFilesAllowed = true
	t.Cleanup(func() {
		tokenizerFilesAllowed = previous
		ResetTokenizersForTest()
	})
	assetsDir, err := RepoAssetsDirForTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(AssetsDirEnv, assetsDir)
	if err := Configure(TokenEstimateOptions{CacheDir: t.TempDir(), Hubs: []string{}}); err != nil {
		t.Fatal(err)
	}
	est := EstimatorFor("moonshotai", "kimi-k3")
	if est.Name() != "kimi-k3" {
		t.Fatalf("estimator id = %q, want kimi-k3", est.Name())
	}
	if n := est.Count("你好，世界"); n <= 0 {
		t.Fatalf("kimi tokenizer counted %d tokens for 你好，世界", n)
	}
}
