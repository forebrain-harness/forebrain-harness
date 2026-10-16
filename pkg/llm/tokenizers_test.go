package llm

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm/hftok"
)

// deepseekV3TokenizerJSON is a real tokenizer.json used to stand in for a built-in.
const deepseekV3TokenizerJSON = "testdata/deepseek_v3_tokenizer.json"

// allowTokenizerFiles turns tokenizer file access on for one test and restores
// the package state afterwards. Non-downloading tests pass DisableDownload: true.
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
		DisableDownload: true,
		Overrides:       []TokenizerOverride{{Model: "acme-", Tokenizer: "qwen-3.8"}},
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
	if err := Configure(TokenEstimateOptions{CacheDir: cache, DisableDownload: true}); err != nil {
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
	if err := Configure(TokenEstimateOptions{CacheDir: cache, DisableDownload: true}); err != nil {
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
		CacheDir:        t.TempDir(),
		DisableDownload: true,
		Overrides:       []TokenizerOverride{{Model: "my-", Path: deepseekV3TokenizerJSON}},
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
	if err := Configure(TokenEstimateOptions{CacheDir: t.TempDir(), DisableDownload: true}); err != nil {
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

// convertedBuiltin describes a converted built-in (Source set) over
// testdata/bytelevel_bpe_tokenizer.json, returning it with its source bytes and
// converted files.
func convertedBuiltin(t *testing.T) (BuiltinTokenizer, []byte, map[string][]byte) {
	t.Helper()
	source := readByteLevelFixture(t)
	model, config, pattern := convertFixture(t, source)
	bt := BuiltinTokenizer{
		ID: "test-conv",
		Files: []TokenizerFile{
			{Name: "tiktoken.model", SHA256: sha256Hex(model), Size: int64(len(model))},
			{Name: "tokenizer_config.json", SHA256: sha256Hex(config), Size: int64(len(config))},
		},
		Tiktoken:      true,
		Pattern:       pattern,
		Source:        &TokenizerFile{Name: "tokenizer.json", SHA256: sha256Hex(source), Size: int64(len(source))},
		HuggingFace:   "acme/testconv",
		ModelScope:    "acme/testconv",
		ModelPrefixes: []string{"testconv"},
	}
	return bt, source, map[string][]byte{"tiktoken.model": model, "tokenizer_config.json": config}
}

// sourceServer serves body as acme/testconv's tokenizer.json on the HuggingFace
// path and 404s everything else; it counts every request.
func sourceServer(t *testing.T, body []byte) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Path == "/acme/testconv/resolve/main/tokenizer.json" {
			_, _ = w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

// Test 12.
func TestConvertedDownloadFallback(t *testing.T) {
	allowTokenizerFiles(t)
	bt, source, converted := convertedBuiltin(t)
	defer swapBuiltinTokenizers([]BuiltinTokenizer{bt})()
	t.Setenv(AssetsDirEnv, t.TempDir()) // no bundle
	server, _ := sourceServer(t, source)

	cache := t.TempDir()
	if err := Configure(TokenEstimateOptions{
		CacheDir:            cache,
		Hubs:                []string{"huggingface"},
		HuggingFaceEndpoint: server.URL,
	}); err != nil {
		t.Fatal(err)
	}
	_ = EstimatorFor("", "testconv") // kicks off the async download
	waitForEstimator(t, "", "testconv", "test-conv")
	if n := EstimatorFor("", "testconv").Count("hello the world"); n <= 0 {
		t.Fatalf("converted tokenizer counted %d tokens", n)
	}
	for name, want := range converted {
		got, err := os.ReadFile(filepath.Join(cache, "test-conv", name))
		if err != nil {
			t.Fatalf("converted file %s missing from the cache: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("cached %s differs from the conversion", name)
		}
	}
	if _, err := os.Stat(filepath.Join(cache, "test-conv", "tokenizer.json")); !os.IsNotExist(err) {
		t.Fatalf("the source tokenizer.json was cached (stat err=%v)", err)
	}
}

// Test 13.
func TestConvertedDownloadRejectsUnverifiedFiles(t *testing.T) {
	cases := map[string]func(bt *BuiltinTokenizer, source []byte) []byte{
		// The hub serves a source that is not the one the table names.
		"tampered source": func(_ *BuiltinTokenizer, source []byte) []byte {
			return bytes.Replace(source, []byte("<|pad|>"), []byte("<|pda|>"), 1)
		},
		// The source verifies, but its conversion is not the table's file.
		"conversion mismatch": func(bt *BuiltinTokenizer, source []byte) []byte {
			bt.Files[1].SHA256 = strings.Repeat("0", 64)
			return source
		},
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			allowTokenizerFiles(t)
			bt, source, _ := convertedBuiltin(t)
			body := tamper(&bt, source)
			defer swapBuiltinTokenizers([]BuiltinTokenizer{bt})()
			t.Setenv(AssetsDirEnv, t.TempDir())
			server, hits := sourceServer(t, body)

			cache := t.TempDir()
			if err := Configure(TokenEstimateOptions{
				CacheDir:            cache,
				Hubs:                []string{"huggingface"},
				HuggingFaceEndpoint: server.URL,
			}); err != nil {
				t.Fatal(err)
			}
			_ = EstimatorFor("", "testconv")
			deadline := time.Now().Add(5 * time.Second)
			for atomic.LoadInt32(hits) == 0 && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			time.Sleep(300 * time.Millisecond) // let the rejecting goroutine finish

			if got := EstimatorFor("", "testconv").Name(); got != "rune_div4" {
				t.Fatalf("estimator after a rejected download = %q, want rune_div4", got)
			}
			entries, err := os.ReadDir(filepath.Join(cache, "test-conv"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("files were cached despite the failed verification: %v", entries)
			}
		})
	}
}

// writeConvertedBundle writes bt's files (their bytes in data, keyed by name)
// gzipped into a fresh assets directory and points AssetsDirEnv at it.
func writeConvertedBundle(t *testing.T, bt BuiltinTokenizer, data map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	dstDir := filepath.Join(dir, "tokenizers", bt.ID)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range bt.Files {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		if _, err := w.Write(data[file.Name]); err != nil {
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

// Test 14.
func TestStaleTokenizerJSONRemovedFromCache(t *testing.T) {
	allowTokenizerFiles(t)
	conv, _, converted := convertedBuiltin(t)
	plain := testBuiltin(t) // Source == nil: its tokenizer.json is the model itself
	defer swapBuiltinTokenizers([]BuiltinTokenizer{conv, plain})()
	writeConvertedBundle(t, conv, converted)

	cache := t.TempDir()
	stale := filepath.Join(cache, conv.ID, "tokenizer.json")
	writeStale := func() {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stale, []byte(`{"stale": true}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeStale()
	plainData, err := os.ReadFile(deepseekV3TokenizerJSON)
	if err != nil {
		t.Fatal(err)
	}
	plainPath := filepath.Join(cache, plain.ID, "tokenizer.json")
	if err := os.MkdirAll(filepath.Dir(plainPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plainPath, plainData, 0o644); err != nil {
		t.Fatal(err)
	}

	// The startup seed — what Configure starts in the background when
	// bulkSeedAllowed is on, run synchronously here so it cannot outlive the
	// swapped table — removes the stale source file and extracts the converted
	// files; the unconverted id's tokenizer.json stays.
	seedCacheFromBundle(estimatorSettings{cacheDir: cache})
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("seed left the stale tokenizer.json (stat err=%v)", err)
	}
	for _, file := range conv.Files {
		if _, err := os.Stat(filepath.Join(cache, conv.ID, file.Name)); err != nil {
			t.Fatalf("seed did not extract %s: %v", file.Name, err)
		}
	}
	if _, err := os.Stat(plainPath); err != nil {
		t.Fatalf("seed removed the tokenizer.json of an unconverted id: %v", err)
	}

	// Loading the converted id removes a stale source file too.
	writeStale()
	if err := Configure(TokenEstimateOptions{CacheDir: cache, DisableDownload: true}); err != nil {
		t.Fatal(err)
	}
	if got := EstimatorFor("", "testconv").Name(); got != conv.ID {
		t.Fatalf("estimator id = %q, want %s", got, conv.ID)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("loading left the stale tokenizer.json (stat err=%v)", err)
	}
}

// fixtureEstimator is a tokenizerEstimator over the small byte-level BPE
// fixture, with an empty count cache.
func fixtureEstimator(t *testing.T) (*tokenizerEstimator, *hftok.Tokenizer) {
	t.Helper()
	tk, err := hftok.FromFile(byteLevelBPETokenizerJSON)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tk.Close() })
	return &tokenizerEstimator{id: "fixture", tk: tk}, tk
}

func (e *tokenizerEstimator) cachedCountsForTest() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.counts)
}

// textOfLen returns a distinct text of exactly n bytes for i.
func textOfLen(i, n int) string {
	prefix := fmt.Sprintf("%08d the ", i)
	return prefix + strings.Repeat("x", n-len(prefix))
}

func TestCountCacheReturnsSameCount(t *testing.T) {
	est, tk := fixtureEstimator(t)

	long := strings.Repeat("func (s *Server) handle() error { // 处理请求 then return\n", 40)
	want := tk.Count(long)
	if first, second := est.Count(long), est.Count(long); first != want || second != want {
		t.Fatalf("Count = %d then %d, want %d both times", first, second, want)
	}
	if got := est.cachedCountsForTest(); got != 1 {
		t.Fatalf("cached counts = %d after one long text, want 1", got)
	}

	short := textOfLen(1, countCacheMinBytes-1)
	if len(short) != 255 {
		t.Fatalf("short text is %d bytes, want 255", len(short))
	}
	if got, want := est.Count(short), tk.Count(short); got != want {
		t.Fatalf("Count(255 bytes) = %d, want %d", got, want)
	}
	if got := est.cachedCountsForTest(); got != 1 {
		t.Fatalf("a 255-byte text was remembered (cached counts = %d)", got)
	}

	edge := textOfLen(2, countCacheMinBytes)
	if len(edge) != 256 {
		t.Fatalf("edge text is %d bytes, want 256", len(edge))
	}
	want = tk.Count(edge)
	if first, second := est.Count(edge), est.Count(edge); first != want || second != want {
		t.Fatalf("Count(256 bytes) = %d then %d, want %d", first, second, want)
	}
	if got := est.cachedCountsForTest(); got != 2 {
		t.Fatalf("a 256-byte text was not remembered (cached counts = %d)", got)
	}
}

func TestCountCacheBounded(t *testing.T) {
	est, _ := fixtureEstimator(t)
	for i := 0; i < countCacheCap+100; i++ {
		if est.Count(textOfLen(i, countCacheMinBytes)) <= 0 {
			t.Fatalf("text %d counted zero tokens", i)
		}
		if got := est.cachedCountsForTest(); got > countCacheCap {
			t.Fatalf("after %d texts the cache holds %d counts, above the cap %d", i+1, got, countCacheCap)
		}
	}
	if got := est.cachedCountsForTest(); got == 0 || got > countCacheCap {
		t.Fatalf("cache holds %d counts, want 1..%d", got, countCacheCap)
	}
}

func TestCountCacheConcurrent(t *testing.T) {
	est, tk := fixtureEstimator(t)
	var texts []string
	for i := 0; i < 200; i++ {
		switch i % 3 {
		case 0:
			texts = append(texts, textOfLen(i, 100)) // below the cache threshold
		case 1:
			texts = append(texts, textOfLen(i, 600))
		default:
			texts = append(texts, textOfLen(i%30, 1200)) // repeats across goroutines
		}
	}
	want := make([]int, len(texts))
	for i, text := range texts {
		want[i] = tk.Count(text)
	}

	const workers = 8
	got := make([][]int, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		got[w] = make([]int, len(texts))
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for k := range texts {
				i := (k + w*25) % len(texts) // each worker starts elsewhere
				got[w][i] = est.Count(texts[i])
			}
		}(w)
	}
	wg.Wait()
	for w := range got {
		for i := range texts {
			if got[w][i] != want[i] {
				t.Fatalf("worker %d text %d: Count = %d, want %d", w, i, got[w][i], want[i])
			}
		}
	}
}

// BenchmarkEstimateMessagesRepeated counts a ~2 MB history of 2000 distinct
// messages with the real glm-5 tokenizer twice, the way consecutive model calls
// re-count the same history, and reports each pass in milliseconds.
func BenchmarkEstimateMessagesRepeated(b *testing.B) {
	ResetTokenizersForTest()
	previous := tokenizerFilesAllowed
	tokenizerFilesAllowed = true
	b.Cleanup(func() {
		tokenizerFilesAllowed = previous
		ResetTokenizersForTest()
	})
	assetsDir, err := RepoAssetsDirForTest()
	if err != nil {
		b.Fatal(err)
	}
	b.Setenv(AssetsDirEnv, assetsDir)
	if err := Configure(TokenEstimateOptions{CacheDir: b.TempDir(), DisableDownload: true}); err != nil {
		b.Fatal(err)
	}
	est := EstimatorFor("zhipuai", "glm-5.3")
	if est.Name() != "glm-5" {
		b.Fatalf("estimator = %q, want glm-5", est.Name())
	}

	const snippet = "func handle%[1]d(ctx context.Context, req *Request) error { // 处理第 %[1]d 个请求并返回结果\n" +
		"\tif req == nil {\n\t\treturn errors.New(\"请求为空: %[1]d\")\n\t}\n\treturn s.store.Save(ctx, req.ID)\n}\n"
	var first, second time.Duration
	var bytesPerPass int
	b.ResetTimer()
	for iter := 0; iter < b.N; iter++ {
		b.StopTimer()
		// Distinct per message and per iteration, so the first pass of every
		// iteration is cold.
		msgs := make([]Message, 2000)
		bytesPerPass = 0
		for i := range msgs {
			var sb strings.Builder
			fmt.Fprintf(&sb, "// iteration %d, message %d\n", iter, i)
			for r := 0; r < 6; r++ {
				fmt.Fprintf(&sb, snippet, i*10+r)
			}
			msgs[i] = UserMessage(Text(sb.String()))
			bytesPerPass += sb.Len()
		}
		b.StartTimer()

		start := time.Now()
		n1 := EstimateMessagesWith(est, msgs)
		mid := time.Now()
		n2 := EstimateMessagesWith(est, msgs)
		end := time.Now()
		first += mid.Sub(start)
		second += end.Sub(mid)
		if n1 != n2 {
			b.Fatalf("second pass counted %d, first %d", n2, n1)
		}
	}
	b.ReportMetric(float64(first.Microseconds())/1000/float64(b.N), "first-ms")
	b.ReportMetric(float64(second.Microseconds())/1000/float64(b.N), "second-ms")
	b.ReportMetric(float64(bytesPerPass)/(1<<20), "history-MB")
}

func TestEmptyHubsMeansDefaultHubs(t *testing.T) {
	ResetTokenizersForTest()
	t.Cleanup(ResetTokenizersForTest)
	for name, hubs := range map[string][]string{"nil": nil, "empty": {}} {
		if err := Configure(TokenEstimateOptions{Hubs: hubs}); err != nil {
			t.Fatal(err)
		}
		settings, _ := currentSettings()
		if got := strings.Join(settings.hubs, ","); got != "huggingface,modelscope" {
			t.Errorf("%s hubs → settings hubs %q, want huggingface,modelscope", name, got)
		}
		if settings.disableDownload {
			t.Errorf("%s hubs disabled downloading", name)
		}
	}
	if err := Configure(TokenEstimateOptions{DisableDownload: true}); err != nil {
		t.Fatal(err)
	}
	if settings, _ := currentSettings(); !settings.disableDownload {
		t.Error("DisableDownload was not recorded")
	}
}

func TestDisableDownloadNeverRequests(t *testing.T) {
	allowTokenizerFiles(t)
	bt := testBuiltin(t)
	defer swapBuiltinTokenizers([]BuiltinTokenizer{bt})()
	t.Setenv(AssetsDirEnv, t.TempDir()) // no bundle

	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "must not be reached", http.StatusInternalServerError)
	}))
	defer server.Close()

	cache := t.TempDir()
	if err := Configure(TokenEstimateOptions{
		CacheDir:            cache,
		Hubs:                []string{"huggingface", "modelscope"},
		HuggingFaceEndpoint: server.URL,
		ModelScopeEndpoint:  server.URL,
		DisableDownload:     true,
	}); err != nil {
		t.Fatal(err)
	}
	if got := EstimatorFor("", "testtok").Name(); got != "rune_div4" {
		t.Fatalf("estimator with nothing local and downloads disabled = %q, want rune_div4", got)
	}
	time.Sleep(time.Second)
	if got := EstimatorFor("", "testtok").Name(); got != "rune_div4" {
		t.Fatalf("estimator after a second = %q, want rune_div4", got)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("downloads disabled, yet the hub got %d requests", got)
	}
}

func TestReconfigureRetriesFailedResolution(t *testing.T) {
	allowTokenizerFiles(t)
	bt, _, converted := convertedBuiltin(t)
	defer swapBuiltinTokenizers([]BuiltinTokenizer{bt})()
	t.Setenv(AssetsDirEnv, t.TempDir()) // no bundle yet

	cache := t.TempDir()
	missing := filepath.Join(t.TempDir(), "tokenizer.json")
	options := TokenEstimateOptions{
		CacheDir:        cache,
		DisableDownload: true,
		Overrides:       []TokenizerOverride{{Model: "local-", Path: missing}},
	}
	if err := Configure(options); err != nil {
		t.Fatal(err)
	}
	if got := EstimatorFor("", "testconv").Name(); got != "rune_div4" {
		t.Fatalf("estimator before the files exist = %q, want rune_div4", got)
	}
	if got := EstimatorFor("", "local-model").Name(); got != "rune_div4" {
		t.Fatalf("override estimator before its file exists = %q, want rune_div4", got)
	}

	// The files appear (an operator copies them in); until the configuration
	// is applied again the recorded failure stands.
	writeConvertedBundle(t, bt, converted)
	if err := os.WriteFile(missing, readByteLevelFixture(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := EstimatorFor("", "testconv").Name(); got != "rune_div4" {
		t.Fatalf("estimator before the reload = %q, want the recorded rune_div4", got)
	}

	if err := Configure(options); err != nil {
		t.Fatal(err)
	}
	if got := EstimatorFor("", "testconv").Name(); got != bt.ID {
		t.Fatalf("estimator after the reload = %q, want %s", got, bt.ID)
	}
	if got := EstimatorFor("", "local-model").Name(); got != "custom" {
		t.Fatalf("override estimator after the reload = %q, want custom", got)
	}

	// A loaded tokenizer survives a further reload.
	loaded := EstimatorFor("", "testconv")
	if err := Configure(options); err != nil {
		t.Fatal(err)
	}
	if EstimatorFor("", "testconv") != loaded {
		t.Fatal("a reload dropped a loaded tokenizer")
	}
}
