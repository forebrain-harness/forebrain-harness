package process

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
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
	// No login on this machine means nothing to discover. Resolving the client
	// version is a request to the npm registry, and the fetch would fail on the
	// missing credentials right after it — so ask for the login first. The
	// wording is the Transport's own, which callers already match on.
	if _, err := openai.Load(s.path); err != nil {
		return nil, fmt.Errorf("ChatGPT login required: %w", err)
	}
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

// NewOpenCodeModelsSource returns the OpenCode model source for a home: the
// Console and the Go subscription catalogs, both read with the login file or
// OPENCODE_API_KEY through the same transport a turn would use.
func NewOpenCodeModelsSource(home string) turn.OpenCodeModelsSource {
	return newOpenCodeModelsSource(opencode.DefaultPath(home))
}

func newOpenCodeModelsSource(path string) openCodeModelsSource {
	return openCodeModelsSource{path: path, fetch: opencode.FetchModels}
}

type openCodeModelsSource struct {
	// path is the login file, <FOREBRAIN_HOME>/opencode.json — the same one
	// /connect and the web login write.
	path string
	// fetch is the catalog call. It is a field so a test can observe which
	// credential a surface resolved without talking to opencode.ai.
	fetch func(ctx context.Context, base http.RoundTripper, goSubscription bool, token string) ([]opencode.ModelInfo, error)
}

// OpenCodeModels lists one surface's catalog.
func (s openCodeModelsSource) OpenCodeModels(ctx context.Context, goSubscription bool) ([]opencode.ModelInfo, error) {
	token, transport, err := s.credential()
	if err != nil {
		return nil, err
	}
	return s.fetch(ctx, transport, goSubscription, token)
}

// OpenCodeSignedIn reports whether this machine has an OpenCode credential at
// all: a login file, or an API key in the environment. Callers use it to spend
// no request on a catalog that would answer 401.
func (s openCodeModelsSource) OpenCodeSignedIn() bool {
	_, _, err := s.credential()
	return err == nil
}

// credential resolves the bearer and the transport the catalogs are read with.
// Neither surface answers without one.
func (s openCodeModelsSource) credential() (string, http.RoundTripper, error) {
	if c, err := opencode.Load(s.path); err == nil && strings.TrimSpace(c.AccessToken) != "" {
		// The login-file transport refreshes a nearly expired token and writes
		// the rotated pair back, so discovery leaves the login usable.
		return c.AccessToken, &opencode.Transport{Path: s.path}, nil
	}
	if key := strings.TrimSpace(os.Getenv(opencode.APIKeyEnv)); key != "" {
		return key, opencode.NewKeyTransport(key), nil
	}
	// The turn layer asks OpenCodeSignedIn before an unscoped listing, so this
	// is reached when a caller asked for one surface by name on a machine with
	// no credential at all.
	return "", nil, errors.New("OpenCode models require a login or " +
		opencode.APIKeyEnv + "; run /connect to sign in")
}
