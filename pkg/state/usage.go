// Token usage accounting and work-activity records.
package state

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

const (
	maxOutputTokensForSummary = 20000
	// defaultContextWindowTokens is the window used when neither the model
	// catalog nor the configured context window names one.
	defaultContextWindowTokens = 200000
	// DefaultAutoCompactPercent is the share of the effective input window at
	// which automatic compaction starts when nothing is configured.
	//
	// Do not lower this default "to save money" on large-window models (e.g.
	// GLM's 1,000,000-token window): every compaction rewrites history, which
	// starts a new prompt-cache generation — the dominant term of the hit-rate
	// equation  hit ≈ 1 − G/N. A lower threshold fires compaction more often,
	// raises G, and lowers the account-level cache hit rate even while it
	// lowers absolute cost. Any change here must be validated against the
	// prefix tracker's generations_per_request before it ships.
	DefaultAutoCompactPercent = appcfg.DefaultAutoCompactPercent
)

// EffectiveInputWindow is the denominator both the auto-compaction threshold
// and the context-pressure warning are measured against: the model's input
// limit, else its context window, else the configured context window.
// fallbackContextTokens is that configured window (agents.defaults
// context_inject.model_context_tokens); 0 selects the built-in default.
func EffectiveInputWindow(limits llm.Limits, fallbackContextTokens int) int {
	if limits.Input > 0 {
		return limits.Input
	}
	if limits.Context > 0 {
		return limits.Context
	}
	if fallbackContextTokens > 0 {
		return fallbackContextTokens
	}
	return defaultContextWindowTokens
}

type TokenUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

type TokenBudget struct {
	Model                  string
	ContextWindow          int
	MaxOutputTokens        int
	EffectiveContextWindow int
	AutoCompactThreshold   int
	TokenUsage             int
	PercentLeft            int
	ShouldAutoCompact      bool
}

type TokenBudgetOptions struct {
	// AutoCompactPercent is the share of the effective input window at which
	// automatic compaction starts. 0 selects the default; a value outside the
	// configured range is clamped to it.
	AutoCompactPercent int
	// FallbackContextTokens is the configured context window, used when the
	// model catalog does not know this model's window.
	FallbackContextTokens int
}

// autoCompactPercent is the share in force: the configured one, clamped to the
// range the setting accepts (the config layer stores a value already in range,
// so this only guards callers that bypass it).
func (o TokenBudgetOptions) autoCompactPercent() int {
	if o.AutoCompactPercent <= 0 {
		return DefaultAutoCompactPercent
	}
	return min(max(o.AutoCompactPercent, appcfg.MinAutoCompactPercent), appcfg.MaxAutoCompactPercent)
}

func TokenUsageFromLLMUsage(usage *llm.Usage) (TokenUsage, bool) {
	if usage == nil {
		return TokenUsage{}, false
	}
	out := TokenUsage{
		InputTokens:              nonNegative(usage.InputTokens),
		OutputTokens:             nonNegative(usage.OutputTokens),
		CacheCreationInputTokens: nonNegative(usage.CacheCreationInputTokens),
		CacheReadInputTokens:     nonNegative(usage.CacheReadInputTokens),
	}
	if out.TokenCount() <= 0 {
		return TokenUsage{}, false
	}
	return out, true
}

func MarshalTokenUsage(usage *llm.Usage) string {
	out, ok := TokenUsageFromLLMUsage(usage)
	if !ok {
		return ""
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

func contentBlocksJSON(parts []llm.ContentPart, fallbackText string) []map[string]any {
	blocks := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case llm.ContentTypeText:
			if strings.TrimSpace(part.Text) != "" {
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": part.Text,
				})
			}
		case llm.ContentTypeImageURL:
			if strings.TrimSpace(part.ImageURL) != "" {
				blocks = append(blocks, map[string]any{
					"type": "image",
					"source": map[string]any{
						"type": "url",
						"url":  part.ImageURL,
					},
				})
			}
		case llm.ContentTypeImageBase64:
			if src := strings.TrimSpace(part.SourcePath); src != "" {
				// A part that knows its file is stored by reference: the bytes stay
				// on disk (the tool already wrote them there), the row and the API
				// stay base64-free, and a reload re-derives the same part.
				blocks = append(blocks, map[string]any{
					"type":      PartTypeFileReference,
					"file_id":   src,
					"label":     filepath.Base(src),
					"mime_type": strings.TrimSpace(part.MIMEType),
				})
				break
			}
			if strings.TrimSpace(part.ImageBase64) != "" || strings.TrimSpace(part.MIMEType) != "" {
				blocks = append(blocks, map[string]any{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": strings.TrimSpace(part.MIMEType),
						"data":       strings.TrimSpace(part.ImageBase64),
					},
				})
			}
		}
	}
	if len(blocks) == 0 {
		if text := strings.TrimSpace(fallbackText); text != "" {
			blocks = append(blocks, map[string]any{
				"type": "text",
				"text": fallbackText,
			})
		}
	}
	return blocks
}

func ContentPartsJSON(parts []llm.ContentPart, fallbackText string) string {
	blocks := contentBlocksJSON(parts, fallbackText)
	b, err := json.Marshal(blocks)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func AppendRawPartJSON(partsJSON string, rawPart string) string {
	rawPart = strings.TrimSpace(rawPart)
	if rawPart == "" || !json.Valid([]byte(rawPart)) {
		return strings.TrimSpace(partsJSON)
	}
	base := strings.TrimSpace(partsJSON)
	if base == "" || !json.Valid([]byte(base)) {
		base = "[]"
	}
	var parts []json.RawMessage
	if err := json.Unmarshal([]byte(base), &parts); err != nil {
		parts = nil
	}
	parts = append(parts, json.RawMessage(rawPart))
	b, err := json.Marshal(parts)
	if err != nil {
		return base
	}
	return string(b)
}

func (u TokenUsage) TokenCount() int {
	return nonNegative(u.InputTokens) +
		nonNegative(u.CacheCreationInputTokens) +
		nonNegative(u.CacheReadInputTokens) +
		nonNegative(u.OutputTokens)
}

func TokenCountFromLastAPIResponse(turns []Message) int {
	for i := len(turns) - 1; i >= 0; i-- {
		usage, ok := usageFromMessage(turns[i])
		if ok {
			return usage.TokenCount()
		}
	}
	return 0
}

func TokenCountWithEstimation(turns []Message) int {
	return TokenCountWithEstimationFor(turns, llm.DefaultEstimator())
}

// TokenCountWithEstimationFor is TokenCountWithEstimation against a specific
// model's estimator, so a session on a non-default tokenizer counts the
// installed rows the same way its own compaction decision does.
func TokenCountWithEstimationFor(turns []Message, e llm.Estimator) int {
	for i := len(turns) - 1; i >= 0; i-- {
		usage, ok := usageFromMessage(turns[i])
		if !ok {
			continue
		}
		anchor := i
		if id := strings.TrimSpace(turns[i].MessageID); id != "" {
			for j := i - 1; j >= 0; j-- {
				prior := turns[j]
				if strings.EqualFold(strings.TrimSpace(prior.Role), "assistant") {
					priorID := strings.TrimSpace(prior.MessageID)
					if priorID == id {
						anchor = j
						continue
					}
					if priorID != "" {
						break
					}
				}
			}
		}
		return usage.TokenCount() + estimateTurnsWith(e, turns[anchor+1:])
	}
	return estimateTurnsWith(e, turns)
}

func CalculateTokenBudget(tokenUsage int, model string, limits llm.Hit) TokenBudget {
	return CalculateTokenBudgetWithOptions(tokenUsage, model, limits, TokenBudgetOptions{})
}

func CalculateTokenBudgetWithOptions(tokenUsage int, model string, limits llm.Hit, opts TokenBudgetOptions) TokenBudget {
	effective := EffectiveInputWindow(limits.Limits(), opts.FallbackContextTokens)
	contextWindow := int(limits.ContextWindow)
	if contextWindow <= 0 {
		contextWindow = opts.FallbackContextTokens
	}
	if contextWindow <= 0 {
		contextWindow = defaultContextWindowTokens
	}
	maxOutput := int(limits.DefaultMaxTokens)
	if maxOutput <= 0 {
		maxOutput = maxOutputTokensForSummary
	}
	autoThreshold := effective * opts.autoCompactPercent() / 100
	if autoThreshold < 1 {
		autoThreshold = 1
	}
	threshold := autoThreshold
	percent := int(math.Round((float64(threshold-tokenUsage) / float64(threshold)) * 100))
	if percent < 0 {
		percent = 0
	}
	return TokenBudget{
		Model:                  strings.TrimSpace(model),
		ContextWindow:          contextWindow,
		MaxOutputTokens:        maxOutput,
		EffectiveContextWindow: effective,
		AutoCompactThreshold:   autoThreshold,
		TokenUsage:             nonNegative(tokenUsage),
		PercentLeft:            percent,
		ShouldAutoCompact:      tokenUsage >= autoThreshold,
	}
}

func usageFromMessage(msg Message) (TokenUsage, bool) {
	if !strings.EqualFold(strings.TrimSpace(msg.Role), "assistant") {
		return TokenUsage{}, false
	}
	// Usage anchors occupancy only when forebrain itself sent the request it
	// measures. A row with no run was imported from another harness (Claude
	// Code, Codex): its usage counts that harness's system prompt and tools, its
	// preserved thinking blocks and its tokenizer — none of which forebrain's
	// next request carries. Such rows are estimated like any other row instead.
	if strings.TrimSpace(msg.RunID) == "" {
		return TokenUsage{}, false
	}
	raw := strings.TrimSpace(msg.UsageJSON)
	if raw == "" {
		return TokenUsage{}, false
	}
	var usage TokenUsage
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		return TokenUsage{}, false
	}
	return usage, usage.TokenCount() > 0
}

func estimateTurnsWith(e llm.Estimator, turns []Message) int {
	total := 0
	for _, turn := range turns {
		total += estimateMessageWith(e, turn)
	}
	return total
}

// estimateMessageWith estimates one stored row at the size the model actually
// receives it, counting with e — the estimator of the model the request is
// headed for.
//
// A row carries parts that exist only for the surfaces — tool_display,
// memory_citation, the compact-boundary marker — and messagesFromStoredRows
// drops every one of them when it rebuilds the request. Estimating the raw
// blob instead charged them: the JSON of a tool_display block runs several
// times the tool output it describes, which read the stored conversation two
// to three times larger than the request built from it. Since the pre-turn and
// mid-turn compaction checkpoints share one threshold, that gap alone decided
// which checkpoint could ever fire. Project the row the way the request
// builder does, then estimate the projection.
func estimateMessageWith(e llm.Estimator, msg Message) int {
	if _, isBoundary := ParseCompactBoundaryPart(msg.PartsJSON); isBoundary {
		return 0
	}
	parsed, ok := ParseMessage(msg.Role, msg.Content, msg.PartsJSON)
	if !ok {
		// Not a row the request builder emits, so it occupies no context.
		return 0
	}
	return llm.EstimateMessageWith(e, parsed)
}

func nonNegative(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
