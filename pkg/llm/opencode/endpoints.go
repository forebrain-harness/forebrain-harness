package opencode

import (
	"errors"
	"fmt"
	"strings"
)

const (
	// ConsoleServer is the authorization server: the device flow and the
	// account endpoints live here.
	ConsoleServer = "https://opencode.ai/console"

	// InferenceBase is the Console inference root, serving both the free
	// family and pay-as-you-go models.
	InferenceBase = "https://opencode.ai/inference"

	// GoInferenceBase is the Go subscription's inference root.
	GoInferenceBase = InferenceBase + "/go"

	// ClientID is OpenCode's public client. Third-party clients share it and
	// identify themselves through the User-Agent, which is why UserAgent()
	// carries this binary's name and version on every request.
	ClientID = "opencode-cli"

	// CredentialsFile is the login file, resolved against FOREBRAIN_HOME so it
	// sits next to forebrain.yaml.
	CredentialsFile = "opencode.json"

	// PublicBearer is the literal token the free model family accepts. It is
	// what the official client sends when the user has no OpenCode account.
	PublicBearer = "public"

	// APIKeyEnv is the Console API key variable. It is spelled out rather than
	// derived from the provider name: the generic derivation would turn
	// "opencode-go" into OPENCODE-GO_API_KEY, which no shell can export.
	APIKeyEnv = "OPENCODE_API_KEY"
)

// Provider names as they appear in configuration. Both are served by one
// package because they differ only in the inference root and in the protocol a
// handful of models speak.
const (
	providerConsole = "opencode"
	providerGo      = "opencode-go"
)

// Protocol names one of the four wire protocols the gateway serves.
type Protocol string

const (
	// ProtocolChatCompletions is OpenAI-compatible chat/completions.
	ProtocolChatCompletions Protocol = "openai/v1/chat/completions"
	// ProtocolResponses is the OpenAI responses API.
	ProtocolResponses Protocol = "openai/v1/responses"
	// ProtocolAnthropic is the Anthropic messages API.
	ProtocolAnthropic Protocol = "anthropic/v1/messages"
	// ProtocolGoogle is the Gemini generateContent API.
	ProtocolGoogle Protocol = "google/v1beta"
)

// ErrUnsupportedProtocol reports a model that is not a chat model at all.
// OpenCode's systemone endpoint (jev-*) takes structured decision requests and
// has no conversational shape, so forebrain must never route it.
var ErrUnsupportedProtocol = errors.New("opencode: model is not a chat model")

// isGoSubscription reports whether provider names the Go subscription.
func isGoSubscription(provider string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), providerGo)
}

// ProviderBaseURL returns the canonical base_url for a provider. Configuration
// validation requires a non-empty base_url even though the client builds its
// own URLs, so this is the value a login flow writes; a caller also compares a
// configured base_url against it to tell the canonical value from a mirror.
// An unknown provider has no canonical base and reports "".
func ProviderBaseURL(provider string) string {
	switch {
	case isGoSubscription(provider):
		return GoInferenceBase
	case strings.EqualFold(strings.TrimSpace(provider), providerConsole):
		return InferenceBase
	}
	return ""
}

// ProtocolFor returns the wire protocol a model speaks on a provider.
//
// It takes the provider as well as the model because the mapping is not a
// function of the model id alone: qwen3.8-max and minimax-m3 speak
// chat/completions on Console and Anthropic messages on the Go subscription
// (2026-10-21 official table).
//
// Otherwise the documented families decide: gemini-* speaks Google, gpt-*,
// grok-4*, grok-build* and muse-spark* speak responses, claude-* speaks
// Anthropic, and the Anthropic-only qwen models are listed per provider below.
// Anything else — including an id the table has never seen — speaks
// chat/completions, which is what the overwhelming majority of models
// documented so far do.
func ProtocolFor(provider, model string) (Protocol, error) {
	model = strings.TrimSpace(model)
	if strings.HasPrefix(model, "jev-") {
		// The systemone endpoint is a structured decision API, not a chat
		// model; routing it as chat would send a body it cannot read.
		return "", fmt.Errorf("%w: %s", ErrUnsupportedProtocol, model)
	}
	switch {
	case strings.HasPrefix(model, "gemini-"):
		return ProtocolGoogle, nil
	case strings.HasPrefix(model, "gpt-"),
		strings.HasPrefix(model, "grok-4"),
		strings.HasPrefix(model, "grok-build"),
		strings.HasPrefix(model, "muse-spark"):
		return ProtocolResponses, nil
	case strings.HasPrefix(model, "claude-"):
		return ProtocolAnthropic, nil
	}
	anthropicFamily := consoleAnthropicModels
	if isGoSubscription(provider) {
		anthropicFamily = goAnthropicModels
	}
	for _, id := range anthropicFamily {
		if model == id {
			return ProtocolAnthropic, nil
		}
	}
	return ProtocolChatCompletions, nil
}

// consoleAnthropicModels lists the models served by Anthropic messages on
// Console that the bare claude-* prefix does not already cover.
var consoleAnthropicModels = []string{
	"qwen3.8-flash",
	"qwen3.7-max",
	"qwen3.7-plus",
	"qwen3.6-plus",
	"qwen3.5-plus",
}

// goAnthropicModels lists the models served by Anthropic messages on the Go
// subscription that the bare claude-* prefix does not already cover. Two of
// them (qwen3.8-max, minimax-m3) speak chat/completions on Console, which is
// why the two lists are separate and why ProtocolFor needs the provider.
var goAnthropicModels = []string{
	"qwen3.8-max",
	"qwen3.8-flash",
	"qwen3.7-plus",
	"minimax-m3",
}

// ClientBaseURL returns the base URL to hand a protocol client, which appends
// its own path suffix: "/chat/completions" and "/responses" under
// /openai/v1, "/v1/messages" under /anthropic, and
// "/v1beta/models/{model}:generateContent" under /google.
//
// inferenceRoot is the inference root the caller wants requests to hit. An
// empty value selects the canonical Console root, which is the normal case; a
// non-empty one is a mirror or proxy the user configured. The Go subscription
// segment is appended exactly once here, so a caller that passes the
// subscription's own canonical base (which already ends in /go) is not
// doubled.
func ClientBaseURL(protocol Protocol, goSubscription bool, inferenceRoot string) string {
	root := strings.TrimRight(strings.TrimSpace(inferenceRoot), "/")
	if root == "" {
		root = InferenceBase
	}
	root = strings.TrimSuffix(root, "/go")
	if goSubscription {
		root += "/go"
	}
	switch protocol {
	case ProtocolChatCompletions, ProtocolResponses:
		return root + "/openai/v1"
	case ProtocolAnthropic:
		return root + "/anthropic"
	case ProtocolGoogle:
		return root + "/google"
	}
	// Not reachable through the constants above; a root without a suffix is
	// the only honest answer for a protocol this function does not know.
	return root
}
