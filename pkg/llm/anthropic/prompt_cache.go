package anthropic

import anthropicapi "github.com/anthropics/anthropic-sdk-go"

const (
	// BreakpointBudget is Anthropic's maximum number of cache markers.
	BreakpointBudget = 4
	// BlockStride keeps markers inside Anthropic's cache lookback window.
	BlockStride = 15
)

// ApplyPromptCache marks the stable request prefix for Anthropic caching.
// Tools precede system blocks, followed by the conversation.
func ApplyPromptCache(system []anthropicapi.TextBlockParam, messages []anthropicapi.MessageParam) {
	budget := BreakpointBudget
	if len(system) > 0 {
		system[len(system)-1].CacheControl = cacheControl()
		budget--
	}
	markMessages(messages, budget)
}

func markMessages(messages []anthropicapi.MessageParam, budget int) {
	if budget <= 0 {
		return
	}
	marked, since := 0, 0
	for i := len(messages) - 1; i >= 0 && marked < budget; i-- {
		content := messages[i].Content
		for j := len(content) - 1; j >= 0 && marked < budget; j-- {
			if marked > 0 {
				since++
				if since < BlockStride {
					continue
				}
			}
			control := content[j].GetCacheControl()
			if control == nil {
				continue
			}
			*control = cacheControl()
			marked++
			since = 0
		}
	}
}

func cacheControl() anthropicapi.CacheControlEphemeralParam {
	control := anthropicapi.NewCacheControlEphemeralParam()
	control.TTL = anthropicapi.CacheControlEphemeralTTLTTL1h
	return control
}
