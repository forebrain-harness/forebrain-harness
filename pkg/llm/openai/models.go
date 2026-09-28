// ChatGPT subscription model discovery. The endpoint, required parameters and
// response schema were verified against the live backend on 2026-09-23; see
// docs/plan/CHATGPT_MODEL_DISCOVERY_PLAN.md §3.4.
package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxModelsBytes bounds the models response. Real responses measured 0.36MB
// (7 models) to 0.52MB (9 models); the bound exists only so a broken or
// hostile response cannot exhaust memory.
const maxModelsBytes = 8 << 20

// ReasoningEffortPreset is one reasoning level a backend model supports.
type ReasoningEffortPreset struct {
	Effort      string `json:"effort"`
	Description string `json:"description,omitempty"`
}

// ModelInfo is one entry of the subscription backend's model list. Fields
// forebrain does not consume are intentionally absent: unknown members of a
// response are ignored by the decoder.
type ModelInfo struct {
	Slug                      string                  `json:"slug"`
	DisplayName               string                  `json:"display_name"`
	Description               string                  `json:"description,omitempty"`
	DefaultReasoningEffort    string                  `json:"default_reasoning_level,omitempty"`
	SupportedReasoningEfforts []ReasoningEffortPreset `json:"supported_reasoning_levels"`
	Visibility                string                  `json:"visibility"`
	SupportedInAPI            bool                    `json:"supported_in_api"`
	Priority                  int32                   `json:"priority"`
	InputModalities           []string                `json:"input_modalities,omitempty"`
	ContextWindow             *int64                  `json:"context_window,omitempty"`
	MaxContextWindow          *int64                  `json:"max_context_window,omitempty"`
	CompHash                  string                  `json:"comp_hash,omitempty"`
}

// PickerVisible reports whether the backend lists this model in pickers.
// "list" is the only value that means visible; anything else — including
// values introduced later — stays hidden.
func (m ModelInfo) PickerVisible() bool { return m.Visibility == "list" }

// SupportsAttachments reports whether the model accepts image input.
func (m ModelInfo) SupportsAttachments() bool {
	for _, modality := range m.InputModalities {
		if strings.EqualFold(strings.TrimSpace(modality), "image") {
			return true
		}
	}
	return false
}

type modelsResponse struct {
	Models []ModelInfo `json:"models"`
}

// ModelsError reports a non-OK models response. StatusCode carries the HTTP
// status so callers can distinguish "log in again" (401) from "no access"
// (403), rate limiting (429) and backend faults (5xx).
type ModelsError struct {
	StatusCode int
	Message    string
}

func (e *ModelsError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("fetch ChatGPT models: http %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("fetch ChatGPT models: http %d", e.StatusCode)
}

// FetchModels fetches the account's available models from the ChatGPT
// subscription backend. base injects authentication and routing headers —
// *Transport in production; baseURL is the Codex backend root (CodexBaseURL in
// production); clientVersion is the codex protocol version declared to the
// backend, resolved live by the caller with ResolveClientVersion. The call
// honors ctx, never follows redirects (credentials must not be re-sent to
// another host) and bounds the response size.
func FetchModels(ctx context.Context, base http.RoundTripper, baseURL, clientVersion string) ([]ModelInfo, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/models?client_version=" + url.QueryEscape(clientVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch ChatGPT models: %w", err)
	}
	req.Header.Set("Accept", "application/json")
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
		return nil, fmt.Errorf("fetch ChatGPT models: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsBytes+1))
	if err != nil {
		return nil, fmt.Errorf("fetch ChatGPT models: read response: %w", err)
	}
	if len(body) > maxModelsBytes {
		return nil, fmt.Errorf("fetch ChatGPT models: response exceeds %d bytes", maxModelsBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &ModelsError{StatusCode: resp.StatusCode, Message: modelsErrorMessage(body)}
	}
	var parsed modelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("fetch ChatGPT models: decode response: %w", err)
	}
	// A missing member and an empty array mean different things: the first is
	// a protocol break, the second is an account with nothing to show.
	if parsed.Models == nil {
		return nil, fmt.Errorf("fetch ChatGPT models: response has no models field")
	}
	for i, m := range parsed.Models {
		if strings.TrimSpace(m.Slug) == "" {
			return nil, fmt.Errorf("fetch ChatGPT models: model at index %d has an empty slug", i)
		}
	}
	return parsed.Models, nil
}

// modelsErrorMessage extracts a short, safe message from an error response.
// Server error bodies carry no credentials, but they can be large, so only the
// decoded error message — or a trimmed body head — survives.
func modelsErrorMessage(body []byte) string {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && strings.TrimSpace(payload.Error.Message) != "" {
		return truncateMessage(payload.Error.Message)
	}
	return truncateMessage(strings.TrimSpace(string(body)))
}

func truncateMessage(s string) string {
	const limit = 300
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}
