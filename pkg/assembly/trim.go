package assembly

import (
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// Dropping messages one at a time off the head of a history is not a safe way
// to shrink it. Provider APIs impose structural rules that a head-drop breaks
// almost immediately in a real agent run:
//
//   - a tool-result item must be preceded by the assistant tool_call that
//     opened its id, so dropping the assistant message orphans every result
//     that followed it;
//   - the first non-system item must be a user turn.
//
// Both violations return a 400 that is *not* a context-window error, so the
// enclosing recovery gives up and reports that 400 instead of compacting.
// trimHistoryToFit shrinks a history by whole, structurally valid units.

// messageGroup is one atomic unit of history: either a standalone message, or
// an assistant message together with the tool results answering its calls.
type messageGroup struct {
	messages []llm.Message
	tokens   int
}

// groupHistory splits messages into leading system prefix and atomic groups.
func groupHistory(messages []llm.Message) (prefix []llm.Message, groups []messageGroup) {
	i := 0
	for ; i < len(messages); i++ {
		if messages[i].Role != llm.RoleSystem {
			break
		}
		prefix = append(prefix, messages[i])
	}
	for i < len(messages) {
		msg := messages[i]
		group := messageGroup{messages: []llm.Message{msg}, tokens: llm.EstimateMessage(msg)}
		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
			open := make(map[string]bool, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				open[tc.ID] = true
			}
			// Absorb the contiguous run of tool results answering these calls.
			j := i + 1
			for ; j < len(messages); j++ {
				if messages[j].Role != llm.RoleTool || !open[messages[j].ToolCallID] {
					break
				}
				group.messages = append(group.messages, messages[j])
				group.tokens += llm.EstimateMessage(messages[j])
			}
			i = j
		} else {
			i++
		}
		groups = append(groups, group)
	}
	return prefix, groups
}

// trimHistoryToFit returns a structurally valid history that fits budget,
// composed of the system prefix, the oldest user turn, and the newest groups
// that still fit. Returns nil when not even that much fits.
//
// The oldest user turn is kept as an anchor for two reasons. It is the original
// request, which is the single most valuable message for a summarizer to see;
// and in a long agent run every later group is an assistant/tool pair, so
// without it the body would open on an assistant turn that providers reject.
// Ordering is preserved: groups are selected newest-first but emitted in their
// original order. Skipping the middle is fine — providers require valid pairing
// and a valid opening role, not contiguity.
func trimHistoryToFit(messages []llm.Message, budget int) []llm.Message {
	if len(messages) == 0 || budget <= 0 {
		return nil
	}
	prefix, groups := groupHistory(messages)
	remaining := budget
	for _, msg := range prefix {
		remaining -= llm.EstimateMessage(msg)
	}
	if remaining <= 0 {
		return nil
	}
	anchor := -1
	for i, group := range groups {
		if opensValidly(group) {
			anchor = i
			break
		}
	}
	if anchor >= 0 {
		if groups[anchor].tokens > remaining {
			return nil
		}
		remaining -= groups[anchor].tokens
	}
	keepFrom := len(groups)
	for i := len(groups) - 1; i > anchor; i-- {
		if groups[i].tokens > remaining {
			break
		}
		remaining -= groups[i].tokens
		keepFrom = i
	}
	kept := groups[keepFrom:]
	if anchor < 0 {
		// No user turn anywhere in this history (a subagent transcript, for
		// example). Fall back to dropping leading groups until one may legally
		// open the body.
		for len(kept) > 0 && !opensValidly(kept[0]) {
			kept = kept[1:]
		}
		if len(kept) == 0 {
			return nil
		}
	}
	out := make([]llm.Message, 0, len(messages))
	out = append(out, prefix...)
	if anchor >= 0 {
		out = append(out, groups[anchor].messages...)
	}
	for _, group := range kept {
		out = append(out, group.messages...)
	}
	return cloneCompactMessages(out)
}

// opensValidly reports whether a group may legally start a history body.
func opensValidly(group messageGroup) bool {
	if len(group.messages) == 0 {
		return false
	}
	first := group.messages[0]
	// Opaque provider items (remote compaction checkpoints, native agent
	// messages) carry no role and are valid openers.
	if first.Compaction != nil || first.AgentMessage != nil {
		return true
	}
	return first.Role == llm.RoleUser
}
