package llm

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/pkoukk/tiktoken-go"
)

var st struct {
	mu sync.Mutex
	tk *tiktoken.Tiktoken
	// useHF tracks whether an HF tokenizer has been configured. Guarded by mu
	// like the rest of this struct: Configure runs once per Runner.Load, and
	// concurrent agents/sessions loading in the same process (a real,
	// unremarkable scenario for a multi-session TUI or Gateway) call it
	// concurrently — a plain package-level bool here raced under -race.
	useHF    bool
	label    string
	encoding string
	heur     bool
	pending  bool
	gen      uint64
}

var (
	tiktokenGetEncoding      = tiktoken.GetEncoding
	tiktokenModelToEncoding  = tiktoken.MODEL_TO_ENCODING
	tiktokenPrefixToEncoding = tiktoken.MODEL_PREFIX_TO_ENCODING
)

// usingHFTokenizer reports whether the most recent Configure call selected an
// HF tokenizer.
func usingHFTokenizer() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.useHF
}

type TokenEstimateOptions struct {
	Encoding      string
	Model         string
	TokenizerPath string
}

func Configure(c TokenEstimateOptions) error {
	// If a HuggingFace tokenizer path is configured, use it instead of tiktoken
	if path := strings.TrimSpace(c.TokenizerPath); path != "" {
		// Reset tiktoken state so EstimateText falls through to HF
		st.mu.Lock()
		st.useHF = true
		st.tk = nil
		st.heur = true
		st.mu.Unlock()
		return ConfigureHFTokenizer(path)
	}

	st.mu.Lock()
	st.useHF = false
	target, warnModel, err := configuredEncoding(c)
	if err != nil {
		st.tk = nil
		st.encoding = ""
		st.pending = false
		st.label = ""
		st.heur = true
		st.mu.Unlock()
		slog.Error("tokestimate: using rune/4 fallback", "err", err)
		return nil
	}
	if st.tk != nil && !st.pending && st.encoding == target {
		st.label = target
		st.heur = false
		st.mu.Unlock()
		return nil
	}
	st.gen++
	gen := st.gen
	st.tk = nil
	st.encoding = target
	st.label = target
	st.heur = false
	st.pending = true
	st.mu.Unlock()
	if warnModel {
		slog.Info("tokestimate: model has no tiktoken map; using cl100k", "model", strings.TrimSpace(c.Model), "encoding", tiktoken.MODEL_CL100K_BASE)
	}
	go warmEncoding(gen, target)
	return nil
}

func activeTiktoken() (tk *tiktoken.Tiktoken, heur bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.tk != nil && !st.heur {
		return st.tk, false
	}
	return nil, true
}

// ImageTokens is the per-image token allowance used when the real dimensions
// are not available. Attached screenshots commonly land in this range across
// providers; decoding every image to size it exactly is not worth the cost for
// a budget estimate.
const ImageTokens = 2000

func EstimateText(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}

	// If an HF tokenizer is configured, use it
	if usingHFTokenizer() {
		if hft := activeHFTokenizer(); hft != nil {
			return hft.Encode(s)
		}
		// HF tokenizer still loading, use heuristic
		return estimateHeuristicRuneDiv4(s)
	}

	tk, heur := activeTiktoken()
	if tk != nil && !heur {
		return len(tk.EncodeOrdinary(s))
	}
	return estimateHeuristicRuneDiv4(s)
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

func configuredEncoding(c TokenEstimateOptions) (string, bool, error) {
	encName := strings.TrimSpace(c.Encoding)
	if encName != "" {
		if !knownEncoding(encName) {
			return "", false, fmt.Errorf("unknown encoding %q", encName)
		}
		return encName, false, nil
	}
	mod := strings.TrimSpace(c.Model)
	if mod == "" {
		return tiktoken.MODEL_CL100K_BASE, false, nil
	}
	if enc, ok := tiktokenModelToEncoding[mod]; ok {
		return enc, false, nil
	}
	for prefix, enc := range tiktokenPrefixToEncoding {
		if strings.HasPrefix(mod, prefix) {
			return enc, false, nil
		}
	}
	return tiktoken.MODEL_CL100K_BASE, true, nil
}

func knownEncoding(name string) bool {
	switch strings.TrimSpace(name) {
	case "gpt2", tiktoken.MODEL_O200K_BASE, tiktoken.MODEL_CL100K_BASE, tiktoken.MODEL_P50K_BASE, tiktoken.MODEL_P50K_EDIT, tiktoken.MODEL_R50K_BASE:
		return true
	default:
		return false
	}
}

func warmEncoding(gen uint64, encoding string) {
	tk, err := tiktokenGetEncoding(encoding)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.gen != gen || st.encoding != encoding {
		return
	}
	st.pending = false
	if err != nil {
		st.tk = nil
		st.heur = true
		slog.Error("tokestimate: background init failed; using rune/4", "encoding", encoding, "err", err)
		return
	}
	st.tk = tk
	st.heur = false
	st.label = encoding
	slog.Info("tokestimate: ready", "label", st.label)
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
	total := 0
	for _, msg := range msgs {
		total += EstimateMessage(msg)
	}
	return total
}

// EstimateMessage estimates one message: its text, the name and arguments of
// each tool call it carries, replayed compaction/agent content, and a flat
// allowance per image part plus per-message framing.
func EstimateMessage(msg Message) int {
	total := EstimateText(msg.TextContent())
	for _, tc := range msg.ToolCalls {
		total += EstimateText(tc.Function.Name + tc.Function.Arguments)
	}
	if msg.Compaction != nil {
		total += EstimateText(msg.Compaction.EncryptedContent)
	}
	if msg.AgentMessage != nil {
		for _, item := range msg.AgentMessage.Content {
			total += EstimateText(item.Text + item.EncryptedContent)
		}
	}
	total += estimateImageTokens(msg.Parts)
	return total + 4
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
