package opencode

import (
	"errors"
	"testing"
)

func TestProviderBaseURL(t *testing.T) {
	cases := []struct {
		provider string
		want     string
	}{
		{provider: "opencode", want: InferenceBase},
		{provider: "opencode-go", want: GoInferenceBase},
		{provider: "OpenCode-Go", want: GoInferenceBase},
		{provider: "opencode ", want: InferenceBase},
		{provider: "", want: ""},
		{provider: "anthropic", want: ""},
	}
	for _, tc := range cases {
		if got := ProviderBaseURL(tc.provider); got != tc.want {
			t.Errorf("ProviderBaseURL(%q) = %q, want %q", tc.provider, got, tc.want)
		}
	}
	// The subscription root is the console root plus one segment, which is what
	// lets ClientBaseURL treat a base as "already the subscription root".
	if GoInferenceBase != InferenceBase+"/go" {
		t.Fatalf("GoInferenceBase = %q, want %q", GoInferenceBase, InferenceBase+"/go")
	}
}

func TestProtocolForDocumentedFamilies(t *testing.T) {
	cases := []struct {
		provider string
		model    string
		want     Protocol
	}{
		// gemini-* is Google on both surfaces.
		{provider: "opencode", model: "gemini-3.5-pro", want: ProtocolGoogle},
		{provider: "opencode-go", model: "gemini-4-argon-contributor", want: ProtocolGoogle},
		// responses families.
		{provider: "opencode", model: "gpt-5.6-codex", want: ProtocolResponses},
		{provider: "opencode", model: "grok-4.7", want: ProtocolResponses},
		{provider: "opencode", model: "grok-build-0.1", want: ProtocolResponses},
		{provider: "opencode", model: "muse-spark-1.3", want: ProtocolResponses},
		{provider: "opencode-go", model: "gpt-6-luna", want: ProtocolResponses},
		{provider: "opencode-go", model: "muse-spark-1.2-contributor", want: ProtocolResponses},
		// claude-* is Anthropic on both surfaces.
		{provider: "opencode", model: "claude-opus-4-7", want: ProtocolAnthropic},
		{provider: "opencode-go", model: "claude-haiku-5-5", want: ProtocolAnthropic},
		// The remaining Anthropic models differ per surface.
		{provider: "opencode", model: "qwen3.8-flash", want: ProtocolAnthropic},
		{provider: "opencode", model: "qwen3.7-max", want: ProtocolAnthropic},
		{provider: "opencode", model: "qwen3.7-plus", want: ProtocolAnthropic},
		{provider: "opencode", model: "qwen3.6-plus", want: ProtocolAnthropic},
		{provider: "opencode", model: "qwen3.5-plus", want: ProtocolAnthropic},
		{provider: "opencode-go", model: "qwen3.8-flash", want: ProtocolAnthropic},
		{provider: "opencode-go", model: "qwen3.7-plus", want: ProtocolAnthropic},
		// chat/completions families.
		{provider: "opencode", model: "qwen3.8-max", want: ProtocolChatCompletions},
		{provider: "opencode", model: "deepseek-v4-pro", want: ProtocolChatCompletions},
		{provider: "opencode", model: "glm-5.3", want: ProtocolChatCompletions},
		{provider: "opencode", model: "kimi-k3", want: ProtocolChatCompletions},
		{provider: "opencode", model: "mistral-large-4", want: ProtocolChatCompletions},
		{provider: "opencode", model: "mimo-v2.5", want: ProtocolChatCompletions},
		{provider: "opencode", model: "big-pickle", want: ProtocolChatCompletions},
		{provider: "opencode-go", model: "glm-5.3-flash", want: ProtocolChatCompletions},
		{provider: "opencode-go", model: "deepseek-v4.1-flash", want: ProtocolChatCompletions},
		{provider: "opencode-go", model: "hy4-preview", want: ProtocolChatCompletions},
	}
	for _, tc := range cases {
		got, err := ProtocolFor(tc.provider, tc.model)
		if err != nil {
			t.Errorf("ProtocolFor(%q, %q) error: %v", tc.provider, tc.model, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ProtocolFor(%q, %q) = %q, want %q", tc.provider, tc.model, got, tc.want)
		}
	}
}

// Two models speak a different protocol on each surface, which is the whole
// reason ProtocolFor takes the provider. Treating these as one rule would send
// an Anthropic body to a chat/completions endpoint (or the reverse) and every
// request for them would fail.
func TestProtocolForSplitsModelsAcrossSurfaces(t *testing.T) {
	for _, model := range []string{"qwen3.8-max", "minimax-m3"} {
		console, err := ProtocolFor("opencode", model)
		if err != nil {
			t.Fatalf("ProtocolFor(opencode, %q) error: %v", model, err)
		}
		if console != ProtocolChatCompletions {
			t.Errorf("Console %s protocol = %q, want %q", model, console, ProtocolChatCompletions)
		}
		goSub, err := ProtocolFor("opencode-go", model)
		if err != nil {
			t.Fatalf("ProtocolFor(opencode-go, %q) error: %v", model, err)
		}
		if goSub != ProtocolAnthropic {
			t.Errorf("Go %s protocol = %q, want %q", model, goSub, ProtocolAnthropic)
		}
	}
}

func TestProtocolForDefaultsToChatCompletions(t *testing.T) {
	for _, model := range []string{"some-model-added-later", "qwen9-max", "hy5-preview", ""} {
		for _, provider := range []string{"opencode", "opencode-go"} {
			got, err := ProtocolFor(provider, model)
			if err != nil {
				t.Fatalf("ProtocolFor(%q, %q) error: %v", provider, model, err)
			}
			if got != ProtocolChatCompletions {
				t.Errorf("ProtocolFor(%q, %q) = %q, want %q", provider, model, got, ProtocolChatCompletions)
			}
		}
	}
}

func TestProtocolForRejectsSystemoneModels(t *testing.T) {
	for _, model := range []string{"jev-1", "jev-decision-max"} {
		if _, err := ProtocolFor("opencode", model); !errors.Is(err, ErrUnsupportedProtocol) {
			t.Fatalf("ProtocolFor(opencode, %q) error = %v, want ErrUnsupportedProtocol", model, err)
		}
	}
}

func TestClientBaseURL(t *testing.T) {
	const mirror = "https://mirror.example.com/inference"
	cases := []struct {
		name     string
		protocol Protocol
		goSub    bool
		root     string
		want     string
	}{
		{
			name:     "chat completions console default root",
			protocol: ProtocolChatCompletions,
			want:     InferenceBase + "/openai/v1",
		},
		{
			name:     "chat completions go default root",
			protocol: ProtocolChatCompletions,
			goSub:    true,
			want:     GoInferenceBase + "/openai/v1",
		},
		{
			name:     "responses console default root",
			protocol: ProtocolResponses,
			want:     InferenceBase + "/openai/v1",
		},
		{
			name:     "responses go default root",
			protocol: ProtocolResponses,
			goSub:    true,
			want:     GoInferenceBase + "/openai/v1",
		},
		{
			// The Anthropic SDK appends /v1/messages itself, so the base must
			// not carry /v1.
			name:     "anthropic console default root",
			protocol: ProtocolAnthropic,
			want:     InferenceBase + "/anthropic",
		},
		{
			name:     "anthropic go default root",
			protocol: ProtocolAnthropic,
			goSub:    true,
			want:     GoInferenceBase + "/anthropic",
		},
		{
			name:     "google console default root",
			protocol: ProtocolGoogle,
			want:     InferenceBase + "/google",
		},
		{
			name:     "google go default root",
			protocol: ProtocolGoogle,
			goSub:    true,
			want:     GoInferenceBase + "/google",
		},
		{
			name:     "explicit canonical root",
			protocol: ProtocolChatCompletions,
			root:     InferenceBase,
			want:     InferenceBase + "/openai/v1",
		},
		{
			name:     "mirror root console",
			protocol: ProtocolChatCompletions,
			root:     mirror,
			want:     mirror + "/openai/v1",
		},
		{
			name:     "mirror root with trailing slash",
			protocol: ProtocolAnthropic,
			root:     mirror + "/",
			want:     mirror + "/anthropic",
		},
		{
			name:     "mirror root google go",
			protocol: ProtocolGoogle,
			goSub:    true,
			root:     mirror,
			want:     mirror + "/go/google",
		},
		{
			// A caller that hands in the subscription's own canonical base must
			// not get /go/go.
			name:     "canonical go root is not doubled",
			protocol: ProtocolChatCompletions,
			goSub:    true,
			root:     GoInferenceBase,
			want:     GoInferenceBase + "/openai/v1",
		},
		{
			name:     "canonical go root without subscription flag",
			protocol: ProtocolChatCompletions,
			root:     GoInferenceBase,
			want:     InferenceBase + "/openai/v1",
		},
	}
	for _, tc := range cases {
		if got := ClientBaseURL(tc.protocol, tc.goSub, tc.root); got != tc.want {
			t.Errorf("%s: ClientBaseURL(%q, %v, %q) = %q, want %q", tc.name, tc.protocol, tc.goSub, tc.root, got, tc.want)
		}
	}
}
