package run

import (
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// The protocol a model's requests are sent on decides which kinds a model's own
// capability can actually turn into a part: it is the routing NewLLMFromYAML
// makes, asked here by a reader that has no client.
func TestProtocolForMediaFollowsTheRouting(t *testing.T) {
	cases := []struct{ provider, model, want string }{
		{"anthropic", "claude-x", llm.ProtocolMessages},
		{"minimax", "abab-x", llm.ProtocolMessages},
		{"chatgpt", "gpt-5", llm.ProtocolResponses},
		{"google", "gemini-3-pro", llm.ProtocolGemini},
		{"openai", "gpt-4o", llm.ProtocolChatCompletions},
		{"volcengine", "doubao-seed", llm.ProtocolChatCompletions},
		{"opencode", "gemini-3.5-pro", llm.ProtocolGemini},
		{"opencode", "claude-opus-4-7", llm.ProtocolMessages},
		{"opencode", "gpt-5.6-codex", llm.ProtocolResponses},
		{"opencode", "some-chat-model", llm.ProtocolChatCompletions},
	}
	for _, tc := range cases {
		if got := protocolForMedia(tc.provider, tc.model); got != tc.want {
			t.Errorf("protocolForMedia(%q, %q) = %q, want %q", tc.provider, tc.model, got, tc.want)
		}
	}
	// A model the gateway refuses to route carries nothing: an unpublished
	// protocol is not a protocol.
	if got := protocolForMedia("opencode", "jev-decision"); got != "" {
		t.Errorf("protocolForMedia for an unroutable model = %q, want none", got)
	}
}
