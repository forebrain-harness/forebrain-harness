package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxModelsBytes bounds the models response. The live Console catalog measured
// 85 entries — tens of kilobytes — so the bound exists only so a broken or
// hostile response cannot exhaust memory.
const maxModelsBytes = 4 << 20

// ModelInfo is one entry of the gateway's model list. The endpoint carries only
// id/object/created/owned_by: no protocol, no context window, no capabilities.
// Everything else comes from the table in models_table.go.
type ModelInfo struct {
	ID string `json:"id"`
}

// ModelsError reports a non-OK models response. StatusCode carries the HTTP
// status so a caller can tell "log in again" (401) from "no subscription"
// (403), rate limiting (429) and gateway faults (5xx).
type ModelsError struct {
	StatusCode int
	Message    string
}

func (e *ModelsError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("fetch OpenCode models: http %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("fetch OpenCode models: http %d", e.StatusCode)
}

// FetchModels lists the models a surface offers.
//
// goSubscription selects the Go subscription's catalog (its endpoint needs a
// real subscription token); otherwise the Console catalog is read. base injects
// routing and
// identity, typically a *Transport; the catalog itself is authenticated by
// token, matching the inference path. The call honors ctx, never follows
// redirects (a bearer token must not be re-sent to another host) and bounds the
// response size.
func FetchModels(ctx context.Context, base http.RoundTripper, goSubscription bool, token string) ([]ModelInfo, error) {
	root := InferenceBase
	if goSubscription {
		root = GoInferenceBase
	}
	endpoint := root + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch OpenCode models: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent())
	if token = strings.TrimSpace(token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	transport := base
	if transport == nil {
		transport = http.DefaultTransport
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch OpenCode models: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsBytes+1))
	if err != nil {
		return nil, fmt.Errorf("fetch OpenCode models: read response: %w", err)
	}
	if len(body) > maxModelsBytes {
		return nil, fmt.Errorf("fetch OpenCode models: response exceeds %d bytes", maxModelsBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &ModelsError{StatusCode: resp.StatusCode, Message: authErrorMessage(body)}
	}
	var parsed struct {
		Data []ModelInfo `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("fetch OpenCode models: decode response: %w", err)
	}
	// A missing member and an empty array mean different things: the first is a
	// protocol break, the second a surface that currently lists nothing.
	if parsed.Data == nil {
		return nil, fmt.Errorf("fetch OpenCode models: response has no data field")
	}
	for i, m := range parsed.Data {
		if strings.TrimSpace(m.ID) == "" {
			return nil, fmt.Errorf("fetch OpenCode models: model at index %d has an empty id", i)
		}
	}
	return parsed.Data, nil
}
