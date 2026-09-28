package process

import (
	"context"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// NewChatGPTModelsSource returns the ChatGPT subscription model source for a
// Forebrain Harness home. It resolves the credentials path exactly like the Responses
// client (credentials.chatgpt override, then <home>/auth.json) and
// authenticates through the same transport, so the models it lists are the
// models a turn would run on.
func NewChatGPTModelsSource(home string) turn.ChatGPTModelsSource {
	return newChatGPTModelsSource(openai.CredentialsPath(home), openai.CodexBaseURL)
}

func newChatGPTModelsSource(path, baseURL string) chatGPTModelsSource {
	return chatGPTModelsSource{path: path, baseURL: baseURL, resolveVersion: openai.ResolveClientVersion}
}

type chatGPTModelsSource struct {
	path           string
	baseURL        string
	resolveVersion func(context.Context) (string, error)
}

func (s chatGPTModelsSource) ChatGPTModels(ctx context.Context) ([]openai.ModelInfo, error) {
	// The declared client version is resolved live on every discovery: it
	// names the newest released codex client, whose availability gates the
	// backend's model list.
	version, err := s.resolveVersion(ctx)
	if err != nil {
		return nil, err
	}
	return openai.FetchModels(ctx, &openai.Transport{Path: s.path}, s.baseURL, version)
}

// DiscoverChatGPTModels lists the account's available ChatGPT subscription
// models for home, projected and ordered the way every surface shows them.
func DiscoverChatGPTModels(ctx context.Context, home string) ([]turn.ModelRecord, error) {
	return turn.DiscoverChatGPTModels(ctx, NewChatGPTModelsSource(home))
}
