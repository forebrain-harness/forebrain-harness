package opencode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/anthropic"
)

// The table stores each model's protocol redundantly with ProtocolFor. This is
// the test that keeps the two in agreement: an edit to one that misses the other
// would otherwise hard-code a wrong base URL for that model.
func TestTableProtocolsMatchProtocolFor(t *testing.T) {
	for _, tc := range []struct {
		provider string
		models   []ModelMeta
	}{
		{provider: "opencode", models: ConsoleModels},
		{provider: "opencode-go", models: GoModels},
	} {
		if len(tc.models) == 0 {
			t.Fatalf("%s table is empty", tc.provider)
		}
		for _, m := range tc.models {
			got, err := ProtocolFor(tc.provider, m.ID)
			if err != nil {
				t.Errorf("ProtocolFor(%q, %q) error: %v", tc.provider, m.ID, err)
				continue
			}
			if got != m.Protocol {
				t.Errorf("%s: %s table protocol = %q, ProtocolFor says %q", tc.provider, m.ID, m.Protocol, got)
			}
			if m.ContextWindow <= 0 {
				t.Errorf("%s: %s has no context window", tc.provider, m.ID)
			}
		}
	}
}

func TestTableIDsAreUnique(t *testing.T) {
	for _, tc := range []struct {
		provider string
		models   []ModelMeta
	}{
		{provider: "opencode", models: ConsoleModels},
		{provider: "opencode-go", models: GoModels},
	} {
		seen := map[string]bool{}
		for _, m := range tc.models {
			if seen[m.ID] {
				t.Errorf("%s table lists %q twice", tc.provider, m.ID)
			}
			seen[m.ID] = true
		}
	}
}

// The reasoning columns are a transcription of the official catalog
// (models.dev, 2026-10-21). Re-transcribing is the maintenance operation, so
// every row is pinned here: a model whose levels drift silently would hand the
// picker a level the model does not have.
func TestModelTableEffortsMatchTheOfficialCatalog(t *testing.T) {
	type want struct {
		id        string
		reasoning bool
		efforts   []string
	}
	for _, tc := range []struct {
		provider string
		models   []want
	}{
		{provider: "opencode", models: []want{
			{id: "big-pickle", reasoning: true},
			{id: "beam-free", reasoning: true, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
			{id: "glm-5.3-flash-free", reasoning: true, efforts: []string{"low", "high", "max"}},
			{id: "deepseek-v4.1-flash-free", reasoning: true, efforts: []string{"low", "high", "max"}},
			{id: "minimax-m3.1-flash-free", reasoning: true, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
			{id: "ll-alpha-free", reasoning: true, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
			{id: "longcat-2.5-preview-free", reasoning: true},
			{id: "exo-free", reasoning: true, efforts: []string{"high"}},
			{id: "mimo-v2.6-flash-free", reasoning: true},
			{id: "mimo-v2.5-free", reasoning: true},
			{id: "nemotron-3-ultra-free", reasoning: true},
			{id: "nemotron-3.5-lightning-free", reasoning: true},
			{id: "nemotron-3.5-super-vl-free", reasoning: true, efforts: []string{"none", "minimal", "low", "medium", "high", "xhigh"}},
			{id: "qwen3.8-max", reasoning: true},
			{id: "qwen3.8-flash", reasoning: true, efforts: []string{"low", "medium", "xhigh"}},
			// The catalog has no row for these two, so the table claims
			// nothing: no reason to promise thinking output.
			{id: "qwen3.7-max", reasoning: false},
			{id: "qwen3.7-plus", reasoning: false},
			{id: "qwen3.6-plus", reasoning: true},
			{id: "qwen3.5-plus", reasoning: true},
			{id: "mistral-large-4", reasoning: true, efforts: []string{"none", "high"}},
			{id: "grok-build-0.1", reasoning: true},
		}},
		{provider: "opencode-go", models: []want{
			{id: "grok-4.7", reasoning: true, efforts: []string{"low", "medium", "high", "xhigh"}},
			{id: "gpt-6-luna", reasoning: true, efforts: []string{"none", "low", "medium", "high", "xhigh", "max"}},
			{id: "gpt-5.6-luna", reasoning: true, efforts: []string{"none", "low", "medium", "high", "xhigh", "max"}},
			{id: "muse-spark-1.3-contributor", reasoning: true, efforts: []string{"minimal", "low", "medium", "high", "xhigh"}},
			{id: "muse-spark-1.2-contributor", reasoning: true, efforts: []string{"minimal", "low", "medium", "high", "xhigh"}},
			{id: "claude-haiku-5-5", reasoning: true, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
			{id: "qwen3.8-max", reasoning: true, efforts: []string{"low", "medium", "xhigh"}},
			{id: "qwen3.8-flash", reasoning: true, efforts: []string{"low", "medium", "xhigh"}},
			{id: "qwen3.7-plus", reasoning: true},
			{id: "minimax-m3", reasoning: true},
			// The table carries the levels the catalog names even though the
			// Google client cannot send one; the protocol trims them.
			{id: "gemini-4-argon-contributor", reasoning: true, efforts: []string{"low", "medium", "high"}},
			{id: "glm-5.3-flash", reasoning: true, efforts: []string{"low", "high", "max"}},
			{id: "glm-5.3", reasoning: true, efforts: []string{"low", "high", "max"}},
			{id: "glm-5.2", reasoning: true, efforts: []string{"high", "max"}},
			{id: "kimi-k3", reasoning: true, efforts: []string{"max"}},
			{id: "kimi-k2.7-code", reasoning: true},
			{id: "longcat-2.0", reasoning: true},
			{id: "deepseek-v4.1-flash", reasoning: true, efforts: []string{"low", "high", "max"}},
			{id: "deepseek-v4-pro", reasoning: true, efforts: []string{"high", "max"}},
			{id: "mimo-v2.6-flash", reasoning: true},
			{id: "mimo-v2.6-pro", reasoning: true},
			{id: "mimo-v2.5", reasoning: true},
			{id: "mimo-v2.5-pro", reasoning: true},
			{id: "hy4-preview", reasoning: true, efforts: []string{"none", "high"}},
			{id: "hy3", reasoning: true, efforts: []string{"none", "low", "high"}},
			{id: "step-5-preview", reasoning: true, efforts: []string{"low", "medium", "high"}},
			{id: "minimax-m3.1-flash-free", reasoning: true, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
			{id: "longcat-2.5-preview-free", reasoning: true},
			{id: "beam-free", reasoning: true, efforts: []string{"low", "medium", "high", "xhigh", "max"}},
		}},
	} {
		for _, w := range tc.models {
			meta, ok := LookupModelMeta(tc.provider, w.id)
			if !ok {
				t.Errorf("%s table is missing %q", tc.provider, w.id)
				continue
			}
			if meta.Reasoning != w.reasoning {
				t.Errorf("%s %q Reasoning = %v, want %v", tc.provider, w.id, meta.Reasoning, w.reasoning)
			}
			if !reflect.DeepEqual(meta.Efforts, w.efforts) {
				t.Errorf("%s %q Efforts = %v, want %v", tc.provider, w.id, meta.Efforts, w.efforts)
			}
		}
	}
}

// The columns have to stay coherent table-wide: a level on a model that does
// not reason, an upper-case level or a repeated one would each reach a picker
// as a broken choice.
func TestTableEffortsAreLowercaseAndBackedByReasoning(t *testing.T) {
	for _, tc := range []struct {
		provider string
		models   []ModelMeta
	}{
		{provider: "opencode", models: ConsoleModels},
		{provider: "opencode-go", models: GoModels},
	} {
		for _, m := range tc.models {
			if len(m.Efforts) == 0 {
				continue
			}
			if !m.Reasoning {
				t.Errorf("%s %q lists efforts but does not reason", tc.provider, m.ID)
			}
			seen := map[string]bool{}
			for _, e := range m.Efforts {
				if e != strings.ToLower(strings.TrimSpace(e)) || e == "" {
					t.Errorf("%s %q has a malformed level %q", tc.provider, m.ID, e)
				}
				if seen[e] {
					t.Errorf("%s %q lists %q twice", tc.provider, m.ID, e)
				}
				seen[e] = true
			}
		}
	}
}

func TestEffortsForProtocol(t *testing.T) {
	all := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	for _, tc := range []struct {
		name     string
		protocol Protocol
		efforts  []string
		want     []string
	}{
		{name: "chat completions passes everything through", protocol: ProtocolChatCompletions, efforts: all, want: all},
		{name: "responses passes everything through", protocol: ProtocolResponses, efforts: all, want: all},
		{name: "anthropic keeps its four levels", protocol: ProtocolAnthropic, efforts: all, want: []string{"low", "medium", "high", "max"}},
		{name: "google carries no effort parameter", protocol: ProtocolGoogle, efforts: all, want: nil},
		{name: "empty stays empty", protocol: ProtocolChatCompletions, efforts: nil, want: nil},
		{name: "a protocol with nothing sendable reports none", protocol: ProtocolAnthropic, efforts: []string{"none", "xhigh"}, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffortsForProtocol(tc.protocol, tc.efforts); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("EffortsForProtocol(%q, %v) = %v, want %v", tc.protocol, tc.efforts, got, tc.want)
			}
		})
	}

	// The result is a copy: a caller editing it must not reach the table.
	meta, ok := LookupModelMeta("opencode-go", "glm-5.3")
	if !ok {
		t.Fatal("Go table is missing glm-5.3")
	}
	got := EffortsForProtocol(meta.Protocol, meta.Efforts)
	got[0] = "mutated"
	if meta.Efforts[0] != "low" {
		t.Fatal("EffortsForProtocol handed back the table's backing array")
	}
}

// TestEffortsForProtocolMatchesTheAnthropicClient locks the two places that
// decide which levels reach the wire: the whitelist here and the switch in
// pkg/llm/anthropic. It drives the real client and reads the request body, so
// editing one without the other turns this red.
func TestEffortsForProtocolMatchesTheAnthropicClient(t *testing.T) {
	for _, level := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"} {
		if sent := anthropicClientSendsEffort(t, level); sent != (len(EffortsForProtocol(ProtocolAnthropic, []string{level})) > 0) {
			t.Errorf("level %q: anthropic client sends = %v, EffortsForProtocol keeps = %v", level, sent, len(EffortsForProtocol(ProtocolAnthropic, []string{level})) > 0)
		}
	}
}

// anthropicClientSendsEffort runs one request through the real Anthropic
// client and reports whether the level reached output_config.effort.
func anthropicClientSendsEffort(t *testing.T, level string) bool {
	t.Helper()
	var body []byte
	client := anthropic.NewAgentLLMWithTransport("test-key", "https://anthropic.test", "claude-haiku-5-5", 1024, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ = io.ReadAll(r.Body)
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"stub"}}`)),
			Request:    r,
		}, nil
	}), level)
	_, _ = client.Execute(context.Background(), []llm.Message{llm.UserMessage(llm.Text("hi"))}, nil)
	var payload struct {
		OutputConfig struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode request body: %v (body=%s)", err, body)
	}
	return payload.OutputConfig.Effort == level
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGoModelsCoverTheDocumentedSubscriptionSet(t *testing.T) {
	byProtocol := map[Protocol][]string{
		ProtocolResponses: {
			"grok-4.7", "gpt-6-luna", "gpt-5.6-luna",
			"muse-spark-1.3-contributor", "muse-spark-1.2-contributor",
		},
		ProtocolAnthropic: {
			"claude-haiku-5-5", "qwen3.8-max", "qwen3.8-flash", "qwen3.7-plus", "minimax-m3",
		},
		ProtocolGoogle: {"gemini-4-argon-contributor"},
		ProtocolChatCompletions: {
			"glm-5.3-flash", "glm-5.3", "glm-5.2", "kimi-k3", "kimi-k2.7-code",
			"longcat-2.0", "deepseek-v4.1-flash", "deepseek-v4-pro", "mimo-v2.6-flash",
			"mimo-v2.6-pro", "mimo-v2.5", "mimo-v2.5-pro", "hy4-preview", "hy3",
			"step-5-preview", "minimax-m3.1-flash-free", "longcat-2.5-preview-free", "beam-free",
		},
	}
	total := 0
	for protocol, ids := range byProtocol {
		for _, id := range ids {
			total++
			meta, ok := LookupModelMeta("opencode-go", id)
			if !ok {
				t.Errorf("Go table is missing %q", id)
				continue
			}
			if meta.Protocol != protocol {
				t.Errorf("Go table %q protocol = %q, want %q", id, meta.Protocol, protocol)
			}
		}
	}
	if len(GoModels) != total {
		t.Errorf("GoModels has %d entries, want %d (the documented subscription set)", len(GoModels), total)
	}
}

func TestLookupModelMeta(t *testing.T) {
	console, ok := LookupModelMeta("opencode", "qwen3.8-max")
	if !ok {
		t.Fatal("Console table is missing qwen3.8-max")
	}
	if console.Protocol != ProtocolChatCompletions || !console.Reasoning {
		t.Errorf("Console qwen3.8-max meta = %+v", console)
	}
	goSub, ok := LookupModelMeta("opencode-go", "qwen3.8-max")
	if !ok {
		t.Fatal("Go table is missing qwen3.8-max")
	}
	if goSub.Protocol != ProtocolAnthropic {
		t.Errorf("Go qwen3.8-max protocol = %q, want %q", goSub.Protocol, ProtocolAnthropic)
	}
	// A surface does not answer for the other surface's table.
	if meta, ok := LookupModelMeta("opencode-go", "big-pickle"); ok {
		t.Errorf("Go table answered for the Console free model: %+v", meta)
	}
	if _, ok := LookupModelMeta("opencode", "  "); ok {
		t.Error("LookupModelMeta accepted a blank id")
	}
	if _, ok := LookupModelMeta("opencode", "not-a-model"); ok {
		t.Error("LookupModelMeta invented an entry for an unknown id")
	}
	// Picker and config input arrive in mixed case.
	if _, ok := LookupModelMeta("opencode", "Big-Pickle"); !ok {
		t.Error("LookupModelMeta did not normalize case")
	}
}

func TestDefaultModelMeta(t *testing.T) {
	meta := DefaultModelMeta(" brand-new-model ")
	if meta.ID != "brand-new-model" {
		t.Errorf("DefaultModelMeta id = %q", meta.ID)
	}
	if meta.Protocol != ProtocolChatCompletions {
		t.Errorf("DefaultModelMeta protocol = %q, want %q", meta.Protocol, ProtocolChatCompletions)
	}
	if meta.ContextWindow != defaultContextWindow {
		t.Errorf("DefaultModelMeta window = %d, want %d", meta.ContextWindow, defaultContextWindow)
	}
	if meta.Free || meta.Reasoning || meta.Attachments {
		t.Errorf("DefaultModelMeta promises capabilities it cannot know: %+v", meta)
	}
}
