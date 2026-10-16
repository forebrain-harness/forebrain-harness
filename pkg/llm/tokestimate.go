package llm

import (
	"log/slog"
	"os"
	"strings"
)

// TokenEstimateOptions configures local token estimation. It is filled from
// agents.defaults.token_estimate at Runner load and applied by Configure. When
// CacheDir is empty (or Configure is never called) every estimator is the
// rune/4 heuristic, which is the unit tests' expectation.
type TokenEstimateOptions struct {
	CacheDir string // $FOREBRAIN_HOME/cache/tokenizers; "" disables model tokenizers
	// Hubs is the download order. Empty (nil or not) means HuggingFace then
	// ModelScope; it only orders the hubs — DisableDownload turns downloading
	// off.
	Hubs                []string
	HuggingFaceEndpoint string // "" → $HF_ENDPOINT → https://huggingface.co
	ModelScopeEndpoint  string // "" → https://modelscope.cn
	DefaultTokenizer    string // "" → gpt-o200k
	Overrides           []TokenizerOverride
	DisableDownload     bool // never download a missing tokenizer (offline/intranet)
}

// TokenizerOverride maps a model-name prefix to a specific tokenizer.
type TokenizerOverride struct {
	Model     string // model-name prefix, case-insensitive
	Tokenizer string // a built-in tokenizer id
	Path      string // an absolute path to a local tokenizer.json
}

// Configure records the token estimation settings. An unknown hub or an unknown
// built-in id is warned about and ignored rather than rejected. It never
// blocks: the startup cache warm-up runs in the background, and a tokenizer is
// loaded (or downloaded) lazily the first time it is needed. A tokenizer that
// could not be resolved under the previous settings is looked for again the
// next time it is needed (see forgetFailedResolutions). It returns an error to
// keep the load path's error handling uniform; there is currently no way for it
// to fail.
func Configure(c TokenEstimateOptions) error {
	settings := estimatorSettings{
		cacheDir:        strings.TrimSpace(c.CacheDir),
		hfEndpoint:      firstNonEmpty(strings.TrimSpace(c.HuggingFaceEndpoint), strings.TrimSpace(os.Getenv(hfEndpointEnv)), defaultHFEndpoint),
		msEndpoint:      firstNonEmpty(strings.TrimSpace(c.ModelScopeEndpoint), defaultModelScopeURL),
		defaultID:       strings.TrimSpace(c.DefaultTokenizer),
		disableDownload: c.DisableDownload,
	}

	hubs := c.Hubs
	if len(hubs) == 0 {
		hubs = []string{hubHuggingFace, hubModelScope}
	}
	for _, hub := range hubs {
		switch strings.ToLower(strings.TrimSpace(hub)) {
		case hubHuggingFace, hubModelScope:
			settings.hubs = append(settings.hubs, strings.ToLower(strings.TrimSpace(hub)))
		default:
			slog.Warn("token_estimate.hubs: unknown hub ignored", "hub", hub)
		}
	}

	if settings.defaultID != "" && !isBuiltinTokenizerID(settings.defaultID) {
		slog.Warn("token_estimate.default_tokenizer: unknown tokenizer id ignored", "id", settings.defaultID)
		settings.defaultID = ""
	}
	for _, override := range c.Overrides {
		if strings.TrimSpace(override.Model) == "" {
			slog.Warn("token_estimate.tokenizers: entry without a model ignored")
			continue
		}
		if id := strings.TrimSpace(override.Tokenizer); id != "" && !isBuiltinTokenizerID(id) {
			slog.Warn("token_estimate.tokenizers: unknown tokenizer id ignored", "model", override.Model, "tokenizer", id)
			continue
		}
		settings.overrides = append(settings.overrides, override)
	}

	estimators.mu.Lock()
	estimators.settings = settings
	estimators.configured = true
	estimators.mu.Unlock()
	forgetFailedResolutions()

	if bulkSeedAllowed && settings.cacheDir != "" {
		go seedCacheFromBundle(settings)
	}
	return nil
}

// ImageTokens is the per-image token allowance used when the real dimensions
// are not available. Attached screenshots commonly land in this range across
// providers; decoding every image to size it exactly is not worth the cost for
// a budget estimate.
const ImageTokens = 2000

// EstimateText estimates the tokens of a string with the default estimator.
func EstimateText(s string) int {
	return EstimateTextWith(DefaultEstimator(), s)
}

func estimateHeuristicRuneDiv4(s string) int {
	n := len([]rune(s))
	if n == 0 {
		return 0
	}
	t := n / 4
	if t <= 0 {
		return 1
	}
	return t
}

// EstimateMessages returns the estimated prompt cost of a message slice as the
// model receives it.
//
// This is the one occupancy function. Auto-compaction decides at two
// checkpoints — before a turn is assembled, and before every mid-turn LLM call
// — and both must measure the same conversation the same way, or one of them
// trips first every time and the other becomes unreachable. Anything that is
// not sent to the model (display blocks kept alongside a persisted row, for
// instance) must be projected out before it reaches this function.
func EstimateMessages(msgs []Message) int {
	return EstimateMessagesWith(DefaultEstimator(), msgs)
}

// EstimateMessage estimates one message with the default estimator: its text,
// the name and arguments of each tool call it carries, replayed
// compaction/agent content, and a flat allowance per image part plus
// per-message framing.
func EstimateMessage(msg Message) int {
	return EstimateMessageWith(DefaultEstimator(), msg)
}

// estimateImageTokens accounts for image parts, which carry no text and so
// would otherwise be invisible to occupancy accounting despite costing on the
// order of a thousand tokens each.
func estimateImageTokens(parts []ContentPart) int {
	total := 0
	for _, part := range parts {
		switch part.Type {
		case ContentTypeImageBase64, ContentTypeImageURL:
			total += ImageTokens
		}
	}
	return total
}
