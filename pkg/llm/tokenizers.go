package llm

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm/hftok"
)

// The tokenizer files local token estimation counts with.
//
// Local token counting picks the tokenizer of the model in use rather than one
// global tiktoken encoder. The tokenizer files for the mainstream model
// families ship beside the binary — committed into the repository, not
// embedded — the same scheme models.json uses (see assets.go). On first use an
// id's files are copied out of the cache if they are there, otherwise extracted
// from beside the binary, and only when neither has them are they downloaded,
// from HuggingFace first and ModelScope second. Enterprise/offline installs can
// copy the cache directory across machines; the cache holds the decompressed
// original files in a fixed, human-readable layout:
//
//	<FOREBRAIN_HOME>/cache/tokenizers/<id>/<file>
//
// builtinTokenizers is the one list of ids, files, checksums and matching rules;
// assets/tokenizers/README.md mirrors it and TestBundledTokenizerFilesMatchTable
// keeps the two in step.

const (
	defaultTokenizerID   = "gpt-o200k"
	tokenizersBundleDir  = "tokenizers"
	hubHuggingFace       = "huggingface"
	hubModelScope        = "modelscope"
	defaultHFEndpoint    = "https://huggingface.co"
	defaultModelScopeURL = "https://modelscope.cn"
	hfEndpointEnv        = "HF_ENDPOINT"
)

// TokenizerFile is one file of a bundled tokenizer: its name under the id's
// directory, the sha256 of the bytes after decompression, and those bytes'
// length. The checksum is compiled in, so a tampered download cannot install a
// file this binary never declared.
type TokenizerFile struct {
	Name   string
	SHA256 string
	Size   int64
}

// BuiltinTokenizer is one bundled tokenizer and the models it serves.
type BuiltinTokenizer struct {
	ID    string
	Files []TokenizerFile // Files[0] is the model; a tiktoken model's Files[1] is its tokenizer_config.json
	// Tiktoken loads the pair with hftok.FromTiktoken(Files[0], Files[1], Pattern);
	// otherwise Files[0] is loaded with hftok.FromFile.
	Tiktoken      bool
	Pattern       string
	HuggingFace   string // repo id on HuggingFace, "" when absent
	ModelScope    string // repo id on ModelScope, "" when absent
	ModelPrefixes []string
	Providers     []string
}

// builtinTokenizers is the built-in table, a variable so tests can replace it.
// The qwen-3.8 entry (which owns the "deepseek-r1-distill-qwen" prefix) is
// listed before deepseek-v4 so that prefix wins over the bare "deepseek"
// prefix: matching is first-match in this order.
var builtinTokenizers = []BuiltinTokenizer{
	{
		ID:            "qwen-3.8",
		Files:         []TokenizerFile{{Name: "tokenizer.json", SHA256: "0997f410c57a1f4e53b09e4be8f4a172d90edd9564368fb0847030937229b9f3", Size: 12809320}},
		HuggingFace:   "Qwen/Qwen3.8-27B",
		ModelScope:    "Qwen/Qwen3.8-27B",
		ModelPrefixes: []string{"qwen", "qwq", "deepseek-r1-distill-qwen"},
		Providers:     []string{"alibaba", "dashscope"},
	},
	{
		ID:            "deepseek-v4",
		Files:         []TokenizerFile{{Name: "tokenizer.json", SHA256: "c90dfa01249db1be4245780a052ede752e1361c612ac6d08e2bdada7d599476b", Size: 6367257}},
		HuggingFace:   "deepseek-ai/DeepSeek-V4.1-Flash",
		ModelScope:    "deepseek-ai/DeepSeek-V4.1-Flash",
		ModelPrefixes: []string{"deepseek"},
		Providers:     []string{"deepseek"},
	},
	{
		ID:            "glm-5",
		Files:         []TokenizerFile{{Name: "tokenizer.json", SHA256: "19e773648cb4e65de8660ea6365e10acca112d42a854923df93db4a6f333a82d", Size: 20217442}},
		HuggingFace:   "zai-org/GLM-5.3",
		ModelScope:    "ZhipuAI/GLM-5.3",
		ModelPrefixes: []string{"glm"},
		Providers:     []string{"zhipuai", "zai"},
	},
	{
		ID:            "gpt-o200k",
		Files:         []TokenizerFile{{Name: "tokenizer.json", SHA256: "0614fe83cadab421296e664e1f48f4261fa8fef6e03e63bb75c20f38e37d07d3", Size: 27868174}},
		HuggingFace:   "openai/gpt-oss-120b",
		ModelScope:    "openai-mirror/gpt-oss-120b",
		ModelPrefixes: []string{"gpt", "chatgpt", "o1", "o3", "o4", "codex"},
		Providers:     []string{"openai"},
	},
	{
		ID: "kimi-k3",
		Files: []TokenizerFile{
			{Name: "tiktoken.model", SHA256: "b6c497a7469b33ced9c38afb1ad6e47f03f5e5dc05f15930799210ec050c5103", Size: 2795286},
			{Name: "tokenizer_config.json", SHA256: "5d0803c94db9cd78763499e0956c95fd5a225c14a727e5a6cf5db3f96f010a6e", Size: 3478},
		},
		Tiktoken:      true,
		Pattern:       kimiPattern,
		HuggingFace:   "moonshotai/Kimi-K3",
		ModelScope:    "moonshotai/Kimi-K3",
		ModelPrefixes: []string{"kimi", "moonshot"},
		Providers:     []string{"moonshotai"},
	},
	{
		ID:            "minimax-m2",
		Files:         []TokenizerFile{{Name: "tokenizer.json", SHA256: "757622126525aeeb131756849d93298070ff3f0319c455ec8c5bb0f6b1cebbe8", Size: 9730160}},
		HuggingFace:   "MiniMaxAI/MiniMax-M2",
		ModelScope:    "MiniMax/MiniMax-M2",
		ModelPrefixes: []string{"minimax"},
		Providers:     []string{"minimax"},
	},
	{
		ID:            "mimo-v2.5",
		Files:         []TokenizerFile{{Name: "tokenizer.json", SHA256: "633518aad78f9f61bae2ae420d621215754a4424c918b052cd8c22a3b59e99d2", Size: 7033572}},
		HuggingFace:   "XiaomiMiMo/MiMo-V2.5",
		ModelScope:    "XiaomiMiMo/MiMo-V2.5",
		ModelPrefixes: []string{"mimo"},
		Providers:     []string{"xiaomi"},
	},
	{
		ID:            "gemma-4",
		Files:         []TokenizerFile{{Name: "tokenizer.json", SHA256: "cc8d3a0ce36466ccc1278bf987df5f71db1719b9ca6b4118264f45cb627bfe0f", Size: 32169626}},
		HuggingFace:   "google/gemma-4-31b-it",
		ModelScope:    "google/gemma-4-31b-it",
		ModelPrefixes: []string{"gemini", "gemma"},
		Providers:     []string{"google"},
	},
	{
		ID:            "claude-legacy",
		Files:         []TokenizerFile{{Name: "tokenizer.json", SHA256: "c241737df24b4e7f7c9af4fdcee29a0ca903dcb288a8b753bc346a3092911767", Size: 1774213}},
		HuggingFace:   "Xenova/claude-tokenizer",
		ModelScope:    "Xenova/claude-tokenizer",
		ModelPrefixes: []string{"claude"},
		Providers:     []string{"anthropic"},
	},
	{
		ID:            "grok-2",
		Files:         []TokenizerFile{{Name: "tokenizer.json", SHA256: "63e538a53de71acf0585603a7f1f62cc66c5e6fa5763d753d10ad071f5c5d4fe", Size: 17963143}},
		HuggingFace:   "alvarobartt/grok-2-tokenizer",
		ModelPrefixes: []string{"grok"},
		Providers:     []string{"xai"},
	},
}

// kimiPattern is the split regex moonshotai/Kimi-K3 ships in
// tokenization_kimi.py, its eight alternatives joined with "|" (the fifth
// starts with a space). It is the pattern hftok.FromTiktoken needs.
const kimiPattern = `[\p{Han}]+|` +
	`[^\r\n\p{L}\p{N}]?[\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}&&[^\p{Han}]]*[\p{Ll}\p{Lm}\p{Lo}\p{M}&&[^\p{Han}]]+(?i:'s|'t|'re|'ve|'m|'ll|'d)?|` +
	`[^\r\n\p{L}\p{N}]?[\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}&&[^\p{Han}]]+[\p{Ll}\p{Lm}\p{Lo}\p{M}&&[^\p{Han}]]*(?i:'s|'t|'re|'ve|'m|'ll|'d)?|` +
	`\p{N}{1,3}|` +
	` ?[^\s\p{L}\p{N}]+[\r\n]*|` +
	`\s*[\r\n]+|` +
	`\s+(?!\S)|` +
	`\s+`

// Estimator counts the tokens of a piece of text.
type Estimator interface {
	// Count returns the number of tokens in text; text is used as given.
	Count(text string) int
	// Name is the tokenizer id, or "rune_div4" for the heuristic.
	Name() string
}

// heuristicEstimator is the rune/4 fallback used when no tokenizer is
// configured or one cannot be resolved.
type heuristicEstimator struct{}

func (heuristicEstimator) Count(text string) int { return estimateHeuristicRuneDiv4(text) }
func (heuristicEstimator) Name() string          { return "rune_div4" }

// tokenizerEstimator wraps a loaded HuggingFace/tiktoken tokenizer.
type tokenizerEstimator struct {
	id string
	tk *hftok.Tokenizer
}

func (e *tokenizerEstimator) Count(text string) int { return e.tk.Count(text) }
func (e *tokenizerEstimator) Name() string          { return e.id }

// TokenizerIDFor returns the built-in tokenizer id that serves a model, in
// order: a configured override whose model prefix matches; a built-in model
// prefix; a configured provider; and finally the default id
// (token_estimate.default_tokenizer, or gpt-o200k). Only the last path segment
// of the model name is considered, lowercased, so
// "deepseek/deepseek-v4-flash" and "deepseek-v4-flash" agree.
func TokenizerIDFor(provider, model string) string {
	settings, _ := currentSettings()
	norm := normalizeModelName(model)
	if override, ok := matchOverride(settings.overrides, norm); ok {
		if id := strings.TrimSpace(override.Tokenizer); id != "" && isBuiltinTokenizerID(id) {
			return id
		}
	}
	if id := matchBuiltinPrefix(norm); id != "" {
		return id
	}
	if id := matchProvider(normalizeProvider(provider)); id != "" {
		return id
	}
	if id := strings.TrimSpace(settings.defaultID); id != "" && isBuiltinTokenizerID(id) {
		return id
	}
	return defaultTokenizerID
}

// DefaultEstimator is the estimator EstimateText and friends use. It resolves
// the default tokenizer; per-model resolution at the compaction decision is
// plan 042. Without a Configure (or with an empty cache dir) it is the
// heuristic.
func DefaultEstimator() Estimator {
	return EstimatorFor("", "")
}

// EstimatorFor returns the estimator for a session's provider/model. The
// resolution order is: a test override (SetEstimatorForTest); then, only when
// tokenizer files are allowed and a cache dir is configured, a configured
// override's local path, the resolved built-in id, and finally the default id.
// When none can be produced it returns the heuristic. This never reads a file
// or the network in a test binary (tokenizerFilesAllowed is false there).
func EstimatorFor(provider, model string) Estimator {
	if override, ok := testOverride(provider, model); ok {
		return override
	}
	if !tokenizerFilesAllowed {
		return heuristicEstimator{}
	}
	settings, configured := currentSettings()
	if !configured || strings.TrimSpace(settings.cacheDir) == "" {
		return heuristicEstimator{}
	}
	if override, ok := matchOverride(settings.overrides, normalizeModelName(model)); ok {
		if path := strings.TrimSpace(override.Path); path != "" {
			if est, ok := resolveLocalPath(path); ok {
				return est
			}
		}
	}
	id := TokenizerIDFor(provider, model)
	if est, ok := resolveTokenizer(settings, id); ok {
		return est
	}
	if def := settings.defaultIDOrDefault(); def != id {
		if est, ok := resolveTokenizer(settings, def); ok {
			return est
		}
	}
	return heuristicEstimator{}
}

// EstimateTextWith counts the tokens of s with e, trimming surrounding
// whitespace first (an empty string is zero tokens) — the same rule EstimateText
// applies.
func EstimateTextWith(e Estimator, s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if e == nil {
		return estimateHeuristicRuneDiv4(s)
	}
	return e.Count(s)
}

// EstimateMessageWith estimates one message with e, using the same composition
// rule as EstimateMessage.
func EstimateMessageWith(e Estimator, msg Message) int {
	total := EstimateTextWith(e, msg.TextContent())
	for _, tc := range msg.ToolCalls {
		total += EstimateTextWith(e, tc.Function.Name+tc.Function.Arguments)
	}
	if msg.Compaction != nil {
		total += EstimateTextWith(e, msg.Compaction.EncryptedContent)
	}
	if msg.AgentMessage != nil {
		for _, item := range msg.AgentMessage.Content {
			total += EstimateTextWith(e, item.Text+item.EncryptedContent)
		}
	}
	total += estimateImageTokens(msg.Parts)
	return total + 4
}

// EstimateMessagesWith estimates a message slice with e.
func EstimateMessagesWith(e Estimator, msgs []Message) int {
	total := 0
	for _, msg := range msgs {
		total += EstimateMessageWith(e, msg)
	}
	return total
}

// SetEstimatorForTest pins the estimator for a provider/model pair and returns
// a function that restores the previous one. It matches by the same
// normalized provider/model TokenizerIDFor uses and takes precedence over
// everything else, including tokenizerFilesAllowed=false — plan 042's tests
// inject a fake estimator this way.
func SetEstimatorForTest(provider, model string, e Estimator) (restore func()) {
	estimators.mu.Lock()
	if estimators.overrides == nil {
		estimators.overrides = map[string]Estimator{}
	}
	key := overrideKey(provider, model)
	previous, existed := estimators.overrides[key]
	estimators.overrides[key] = e
	estimators.mu.Unlock()
	return func() {
		estimators.mu.Lock()
		defer estimators.mu.Unlock()
		if existed {
			estimators.overrides[key] = previous
		} else {
			delete(estimators.overrides, key)
		}
	}
}

// ResetTokenizersForTest clears the configured settings, the test overrides and
// every cached slot, returning the package to its unconfigured state.
func ResetTokenizersForTest() {
	estimators.mu.Lock()
	estimators.configured = false
	estimators.settings = estimatorSettings{}
	estimators.overrides = nil
	estimators.mu.Unlock()
	slotRegistry.mu.Lock()
	slotRegistry.slots = map[string]*tokenizerSlot{}
	slotRegistry.mu.Unlock()
}

// tokenizerFilesAllowed gates every tokenizer file read and download. It is
// false in a test binary: a test binary keeps the rune/4 heuristic and never
// touches tokenizer files or the network unless the test opts in by flipping
// this (llm's own tests do). bulkSeedAllowed is the same for the startup
// extraction, which Configure otherwise kicks off.
var (
	tokenizerFilesAllowed = !testing.Testing()
	bulkSeedAllowed       = !testing.Testing()
)

// estimatorSettings is the resolved configuration, recorded by Configure.
type estimatorSettings struct {
	cacheDir   string
	hubs       []string
	hfEndpoint string
	msEndpoint string
	defaultID  string
	overrides  []TokenizerOverride
}

func (s estimatorSettings) defaultIDOrDefault() string {
	if id := strings.TrimSpace(s.defaultID); id != "" {
		return id
	}
	return defaultTokenizerID
}

var estimators struct {
	mu         sync.Mutex
	configured bool
	settings   estimatorSettings
	overrides  map[string]Estimator // test-injected, keyed by overrideKey
}

// tokenizerSlot is the once-per-process resolution state for one tokenizer id
// (or one local override path). A loaded *hftok.Tokenizer is reused for the
// life of the process and never Closed.
type tokenizerSlot struct {
	mu           sync.Mutex
	resolved     bool
	est          Estimator
	downloadOnce sync.Once
}

var slotRegistry struct {
	mu    sync.Mutex
	slots map[string]*tokenizerSlot
}

func currentSettings() (estimatorSettings, bool) {
	estimators.mu.Lock()
	defer estimators.mu.Unlock()
	return estimators.settings, estimators.configured
}

func normalizeModelName(model string) string {
	model = strings.TrimSpace(model)
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	return strings.ToLower(model)
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func overrideKey(provider, model string) string {
	return normalizeProvider(provider) + "\x00" + normalizeModelName(model)
}

func testOverride(provider, model string) (Estimator, bool) {
	estimators.mu.Lock()
	defer estimators.mu.Unlock()
	if estimators.overrides == nil {
		return nil, false
	}
	e, ok := estimators.overrides[overrideKey(provider, model)]
	return e, ok
}

// matchOverride returns the configured override whose model prefix matches
// normModel, preferring the longest (most specific) prefix.
func matchOverride(overrides []TokenizerOverride, normModel string) (TokenizerOverride, bool) {
	best := -1
	var found TokenizerOverride
	for _, override := range overrides {
		prefix := strings.ToLower(strings.TrimSpace(override.Model))
		if prefix == "" || !strings.HasPrefix(normModel, prefix) {
			continue
		}
		if len(prefix) > best {
			best = len(prefix)
			found = override
		}
	}
	return found, best >= 0
}

// matchBuiltinPrefix returns the id whose model prefix matches, first match in
// builtinTokenizers order (so "deepseek-r1-distill-qwen" beats "deepseek").
func matchBuiltinPrefix(normModel string) string {
	for _, bt := range builtinTokenizers {
		for _, prefix := range bt.ModelPrefixes {
			if strings.HasPrefix(normModel, strings.ToLower(prefix)) {
				return bt.ID
			}
		}
	}
	return ""
}

// matchProvider returns the id whose provider list contains provider.
func matchProvider(provider string) string {
	if provider == "" {
		return ""
	}
	for _, bt := range builtinTokenizers {
		for _, p := range bt.Providers {
			if provider == strings.ToLower(p) {
				return bt.ID
			}
		}
	}
	return ""
}

func isBuiltinTokenizerID(id string) bool {
	for _, bt := range builtinTokenizers {
		if bt.ID == id {
			return true
		}
	}
	return false
}

func builtinTokenizerByID(id string) (BuiltinTokenizer, bool) {
	for _, bt := range builtinTokenizers {
		if bt.ID == id {
			return bt, true
		}
	}
	return BuiltinTokenizer{}, false
}

func slotFor(key string) *tokenizerSlot {
	slotRegistry.mu.Lock()
	defer slotRegistry.mu.Unlock()
	if slotRegistry.slots == nil {
		slotRegistry.slots = map[string]*tokenizerSlot{}
	}
	if slot, ok := slotRegistry.slots[key]; ok {
		return slot
	}
	slot := &tokenizerSlot{}
	slotRegistry.slots[key] = slot
	return slot
}

// resolveTokenizer loads id's estimator from the cache or the bundled assets,
// starting (once) a background download when neither has it. It never blocks on
// the network.
func resolveTokenizer(settings estimatorSettings, id string) (Estimator, bool) {
	bt, ok := builtinTokenizerByID(id)
	if !ok {
		return nil, false
	}
	slot := slotFor(id)
	slot.mu.Lock()
	if slot.resolved {
		est := slot.est
		slot.mu.Unlock()
		return est, est != nil
	}
	est, source, err := loadBundledOrCache(settings, bt)
	if err == nil {
		slot.resolved = true
		slot.est = est
		slot.mu.Unlock()
		slog.Info("tokenizer ready", "id", id, "source", source)
		return est, true
	}
	// Unavailable locally: record that (so the filesystem is not rescanned on
	// every call) and start one background download for this id and process.
	slot.resolved = true
	slot.est = nil
	slot.mu.Unlock()
	slot.downloadOnce.Do(func() {
		go downloadAndLoad(settings, bt, slot)
	})
	return nil, false
}

// resolveLocalPath loads a configured override's tokenizer.json. It is not
// copied or checksum-verified, and reused for the life of the process.
func resolveLocalPath(path string) (Estimator, bool) {
	slot := slotFor("local\x00" + path)
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.resolved {
		return slot.est, slot.est != nil
	}
	slot.resolved = true
	tk, err := hftok.FromFile(path)
	if err != nil {
		slog.Warn("configured tokenizer path could not be loaded", "path", path, "err", err)
		return nil, false
	}
	slot.est = &tokenizerEstimator{id: "custom", tk: tk}
	slog.Info("tokenizer ready", "id", "custom", "source", "path", "path", path)
	return slot.est, true
}

// loadBundledOrCache loads bt's estimator from files that are already in the
// cache, extracting any that are missing from beside the binary. It returns the
// source ("cache" or "bundle") on success.
func loadBundledOrCache(settings estimatorSettings, bt BuiltinTokenizer) (Estimator, string, error) {
	cacheDir := filepath.Join(settings.cacheDir, bt.ID)
	paths := make([]string, len(bt.Files))
	source := "cache"
	for i, file := range bt.Files {
		path := filepath.Join(cacheDir, file.Name)
		if fileMatches(path, file) {
			paths[i] = path
			continue
		}
		if err := extractBundleFile(settings.cacheDir, bt.ID, file, path); err != nil {
			return nil, "", err
		}
		source = "bundle"
		paths[i] = path
	}
	return loadFromPaths(bt, paths, source)
}

// fileMatches reports whether path holds file's exact bytes. An existing file
// that does not match is warned about once (this runs at most once per id).
func fileMatches(path string, file TokenizerFile) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	if int64(len(data)) == file.Size && sha256Hex(data) == file.SHA256 {
		return true
	}
	slog.Warn("tokenizer cache file failed verification; re-extracting", "path", path)
	return false
}

// cacheFilePresentBySize reports whether path exists with file's expected
// length, without reading it — the cheap check the startup seed uses.
func cacheFilePresentBySize(path string, file TokenizerFile) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() == file.Size
}

// extractBundleFile decompresses the bundled <AssetsDir>/tokenizers/<id>/<file>.gz
// into cachePath through a temporary file in the same directory (an atomic
// rename), verifying the decompressed bytes first.
func extractBundleFile(cacheDir, id string, file TokenizerFile, cachePath string) error {
	dir, err := bundleDir()
	if err != nil {
		return err
	}
	bundlePath := filepath.Join(dir, id, file.Name+".gz")
	compressed, err := os.Open(bundlePath)
	if err != nil {
		return err
	}
	defer compressed.Close()
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(cachePath), file.Name+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return err
	}
	defer gz.Close()
	data, err := io.ReadAll(io.LimitReader(gz, file.Size+1))
	if err != nil {
		return err
	}
	if int64(len(data)) != file.Size || sha256Hex(data) != file.SHA256 {
		slog.Warn("bundled tokenizer file failed verification", "path", bundlePath)
		return fmt.Errorf("llm: bundled tokenizer %s/%s failed verification", id, file.Name)
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, cachePath)
}

// bundleDir is where the bundled tokenizer assets live, beside the binary.
func bundleDir() (string, error) {
	dir, err := AssetsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, tokenizersBundleDir), nil
}

// loadFromPaths builds an estimator from already-resolved file paths.
func loadFromPaths(bt BuiltinTokenizer, paths []string, source string) (Estimator, string, error) {
	var (
		tk  *hftok.Tokenizer
		err error
	)
	if bt.Tiktoken {
		tk, err = hftok.FromTiktoken(paths[0], paths[1], bt.Pattern)
	} else {
		tk, err = hftok.FromFile(paths[0])
	}
	if err != nil {
		return nil, "", fmt.Errorf("llm: load tokenizer %s: %w", bt.ID, err)
	}
	return &tokenizerEstimator{id: bt.ID, tk: tk}, source, nil
}

// downloadAndLoad downloads bt's files (once per id, per process) and, on
// success, installs the loaded estimator into slot so the next EstimatorFor
// call uses it. It never runs in a test binary.
func downloadAndLoad(settings estimatorSettings, bt BuiltinTokenizer, slot *tokenizerSlot) {
	if err := downloadTokenizerFiles(settings, bt); err != nil {
		slog.Warn("tokenizer download failed; using the rune/4 estimate",
			"id", bt.ID, "err", err, "cache_dir", filepath.Join(settings.cacheDir, bt.ID))
		return
	}
	est, _, err := loadBundledOrCache(settings, bt)
	if err != nil {
		slog.Warn("downloaded tokenizer could not be loaded", "id", bt.ID, "err", err)
		return
	}
	slot.mu.Lock()
	slot.est = est
	slot.mu.Unlock()
	slog.Info("tokenizer ready", "id", bt.ID, "source", "download")
}

// downloadTokenizerFiles tries each configured hub in order until one serves
// every file of bt and all of them verify.
func downloadTokenizerFiles(settings estimatorSettings, bt BuiltinTokenizer) error {
	var errs []string
	for _, hub := range settings.hubs {
		var base, repo, ref string
		switch hub {
		case hubHuggingFace:
			base, repo, ref = settings.hfEndpoint, bt.HuggingFace, "main"
		case hubModelScope:
			base, repo, ref = strings.TrimRight(settings.msEndpoint, "/")+"/models", bt.ModelScope, "master"
		default:
			continue
		}
		if strings.TrimSpace(repo) == "" {
			continue
		}
		if err := downloadFromHub(settings, bt, base, repo, ref); err != nil {
			errs = append(errs, hub+": "+err.Error())
			continue
		}
		return nil
	}
	return fmt.Errorf("no hub served %s: %s", bt.ID, strings.Join(errs, "; "))
}

func downloadFromHub(settings estimatorSettings, bt BuiltinTokenizer, base, repo, ref string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	cacheDir := filepath.Join(settings.cacheDir, bt.ID)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	for _, file := range bt.Files {
		url := strings.TrimRight(base, "/") + "/" + repo + "/resolve/" + ref + "/" + file.Name
		if err := downloadOne(client, url, filepath.Join(cacheDir, file.Name), file); err != nil {
			return err
		}
	}
	return nil
}

func downloadOne(client *http.Client, url, dest string, file TokenizerFile) error {
	response, err := client.Get(url)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, file.Size+1))
	if err != nil {
		return err
	}
	if int64(len(data)) != file.Size || sha256Hex(data) != file.SHA256 {
		return fmt.Errorf("GET %s: content does not match the expected size and checksum", url)
	}
	return writeFileAtomic(dest, data)
}

// writeFileAtomic writes data to dest through a temporary file in the same
// directory, so a reader sees either the old file or the complete new one.
func writeFileAtomic(dest string, data []byte) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(dest)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, dest)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// seedCacheFromBundle extracts every built-in tokenizer that is not already in
// the cache from beside the binary. It is a best-effort startup warm-up: it
// never downloads and never loads a tokenizer, and a missing bundle is left for
// a later need to download.
func seedCacheFromBundle(settings estimatorSettings) {
	for _, bt := range builtinTokenizers {
		for _, file := range bt.Files {
			cachePath := filepath.Join(settings.cacheDir, bt.ID, file.Name)
			if cacheFilePresentBySize(cachePath, file) {
				continue
			}
			_ = extractBundleFile(settings.cacheDir, bt.ID, file, cachePath)
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
