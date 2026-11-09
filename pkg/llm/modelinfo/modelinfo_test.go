package modelinfo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
)

// devCatalogFixture is the shape measurements on 2026-11-07 produced: two
// tables, providers grouped by id and a flat top-level one keyed by
// "vendor/model", each entry carrying reasoning_options / limit / modalities /
// status.
const devCatalogFixture = `{
  "providers": {
    "opencode-go": {"models": {
      "step-5-preview": {"reasoning":true,"reasoning_options":[{"type":"effort","values":["low","medium","high"]}],
        "limit":{"context":1000,"output":100},"modalities":{"input":["text","image"]}},
      "longcat-2.0": {"reasoning":true,"reasoning_options":[{"type":"toggle"}],"limit":{"context":200,"output":20}}
    }},
    "opencode": {"models": {
      "claude-opus-4-8": {"reasoning":true,"reasoning_options":[{"type":"effort","values":["low","medium","high","xhigh","max"]}],
        "limit":{"context":200000,"output":64000}}
    }},
    "deepseek": {"models": {
      "deepseek-v4-flash": {"reasoning":true,"reasoning_options":[{"type":"effort","values":["low","high"]}],"limit":{"context":1000000,"output":384000}},
      "deepseek-v4-flash-0731": {"reasoning":true,"reasoning_options":[{"type":"effort","values":["low"]}],"limit":{"context":1,"output":1},"status":"deprecated"}
    }}
  },
  "models": {
    "openai/gpt-4o": {"reasoning":true,"reasoning_options":[{"type":"effort","values":["low","medium","high"]}],
      "limit":{"context":128000,"output":16384},"modalities":{"input":["text","image"]}},
    "deepseek/deepseek-v4-flash": {"reasoning":true,"reasoning_options":[{"type":"effort","values":["low","high"]}],
      "limit":{"context":1000000,"output":384000}},
    "zhipuai/glm-5.3": {"reasoning":true,"reasoning_options":[{"type":"effort","values":["low","high","max"]}],
      "limit":{"context":1000000,"output":128000}},
    "alpha/dup": {"reasoning":true,"reasoning_options":[{"type":"effort","values":["low"]}],"limit":{"context":11,"output":1}},
    "beta/dup": {"reasoning":true,"reasoning_options":[{"type":"effort","values":["high"]}],"limit":{"context":22,"output":2}}
  }
}`

// newModelsDevServer serves the fixture and counts its hits, so the cache tests
// can prove a second resolve does not go back to the network.
func newModelsDevServer(t *testing.T) (*httptest.Server, *int64) {
	t.Helper()
	var hits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/catalog.json" || r.URL.Query().Get("type") != "all" {
			t.Errorf("unexpected catalog request %s", r.URL.String())
		}
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(devCatalogFixture))
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func TestModelsDevSourceResolvesOpenCodeModels(t *testing.T) {
	server, _ := newModelsDevServer(t)
	source := &ModelsDevSource{BaseURL: server.URL + "/catalog.json?type=all"}
	if !source.Applies(Row{Provider: "opencode-go", Model: "step-5-preview"}, "opencode-go") {
		t.Fatal("models.dev must apply to every row")
	}
	info, err := source.Fetch(context.Background(), Row{Provider: "opencode-go", Model: "step-5-preview"}, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !reflect.DeepEqual(info.Efforts, []string{"low", "medium", "high"}) {
		t.Fatalf("efforts = %v", info.Efforts)
	}
	if info.ContextWindow != 1000 || info.MaxOutput != 100 || info.Origin != "models.dev" {
		t.Fatalf("info = %+v", info)
	}
	if info.Images == nil || !*info.Images {
		t.Fatalf("images = %v, want true", info.Images)
	}
	// A toggle option names no levels: the row resolves with an empty list,
	// which is what makes a surface offer the checkboxes.
	toggle, err := source.Fetch(context.Background(), Row{Provider: "opencode-go", Model: "longcat-2.0"}, "")
	if err != nil {
		t.Fatalf("toggle fetch: %v", err)
	}
	if len(toggle.Efforts) != 0 {
		t.Fatalf("a toggle model must carry no levels, got %v", toggle.Efforts)
	}
}

func TestModelsDevSourceBuiltinVendorAndDeprecated(t *testing.T) {
	server, _ := newModelsDevServer(t)
	source := &ModelsDevSource{BaseURL: server.URL + "/catalog.json?type=all"}
	info, err := source.Fetch(context.Background(), Row{Provider: "deepseek", Model: "deepseek-v4-flash"}, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !reflect.DeepEqual(info.Efforts, []string{"low", "high"}) || info.ContextWindow != 1000000 {
		t.Fatalf("info = %+v", info)
	}
	// A retired entry is not a source of truth for levels any more.
	if _, err := source.Fetch(context.Background(), Row{Provider: "deepseek", Model: "deepseek-v4-flash-0731"}, ""); !errors.Is(err, ErrNotListed) {
		t.Fatalf("deprecated entry err = %v, want ErrNotListed", err)
	}
	if _, err := source.Fetch(context.Background(), Row{Provider: "deepseek", Model: "no-such-model"}, ""); !errors.Is(err, ErrNotListed) {
		t.Fatalf("unknown model err = %v, want ErrNotListed", err)
	}
	// A provider models.dev has no entry for (meituan, tencent, byteplus —
	// measured NOT FOUND 2026-11-07) asks the user for the levels.
	if _, err := source.Fetch(context.Background(), Row{Provider: "meituan", Model: "LongCat-2.5"}, ""); !errors.Is(err, ErrNotListed) {
		t.Fatalf("meituan err = %v, want ErrNotListed", err)
	}
}

func TestModelsDevSourceCustomLookups(t *testing.T) {
	server, _ := newModelsDevServer(t)
	source := &ModelsDevSource{BaseURL: server.URL + "/catalog.json?type=all"}
	// A vendor prefix the user typed is the flat table's key.
	prefixed, err := source.Fetch(context.Background(), Row{Provider: "custom", Model: "openai/gpt-4o"}, "")
	if err != nil {
		t.Fatalf("prefixed fetch: %v", err)
	}
	if !reflect.DeepEqual(prefixed.Efforts, []string{"low", "medium", "high"}) || prefixed.ContextWindow != 128000 {
		t.Fatalf("prefixed info = %+v", prefixed)
	}
	// No prefix: matched by the key's last segment.
	bare, err := source.Fetch(context.Background(), Row{Provider: "custom", Model: "glm-5.3"}, "")
	if err != nil {
		t.Fatalf("bare fetch: %v", err)
	}
	if !reflect.DeepEqual(bare.Efforts, []string{"low", "high", "max"}) {
		t.Fatalf("bare info = %+v", bare)
	}
	// Several matches: the first key in sorted order wins, so the answer is
	// stable from run to run.
	dup, err := source.Fetch(context.Background(), Row{Provider: "custom", Model: "dup"}, "")
	if err != nil {
		t.Fatalf("dup fetch: %v", err)
	}
	if !reflect.DeepEqual(dup.Efforts, []string{"low"}) || dup.ContextWindow != 11 {
		t.Fatalf("dup resolved to the wrong entry: %+v", dup)
	}
	if _, err := source.Fetch(context.Background(), Row{Provider: "custom", Model: "nothing-here"}, ""); !errors.Is(err, ErrNotListed) {
		t.Fatalf("unprefixed unknown err = %v, want ErrNotListed", err)
	}
}

func TestModelsDevSourceCacheTTL(t *testing.T) {
	server, hits := newModelsDevServer(t)
	clock := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	source := &ModelsDevSource{
		BaseURL: server.URL + "/catalog.json?type=all",
		TTL:     2 * time.Hour,
		Now:     func() time.Time { return clock },
	}
	row := Row{Provider: "opencode-go", Model: "step-5-preview"}
	for i := 0; i < 3; i++ {
		if _, err := source.Fetch(context.Background(), row, ""); err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt64(hits); got != 1 {
		t.Fatalf("catalog requests = %d, want 1 inside the TTL", got)
	}
	clock = clock.Add(2*time.Hour + time.Minute)
	if _, err := source.Fetch(context.Background(), row, ""); err != nil {
		t.Fatalf("fetch after TTL: %v", err)
	}
	if got := atomic.LoadInt64(hits); got != 2 {
		t.Fatalf("catalog requests = %d, want a refetch after the TTL", got)
	}
}

func TestModelsDevSourceRejectsAnUnrelatedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"unexpected":"shape"}`))
	}))
	defer server.Close()
	source := &ModelsDevSource{BaseURL: server.URL}
	if _, err := source.Fetch(context.Background(), Row{Provider: "opencode-go", Model: "step-5-preview"}, ""); err == nil {
		t.Fatal("a response with neither table must be an error, not an empty answer")
	}
}

func TestArkSourceResolvesPlanModels(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/models" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"doubao-seed-2-1-pro-260915","name":"doubao-seed-2.1-pro",
			 "effort":{"supported_levels":["low","medium","high","minimal"],"default_level":"high"},
			 "token_limits":{"context_window":262144,"max_output_token_length":262144},
			 "modalities":{"input_modalities":["text","image"]}},
			{"id":"image-model","name":"image-model","effort":null,
			 "token_limits":{"context_window":4096,"max_output_token_length":0},
			 "modalities":{"input_modalities":["text"]}}
		]}`))
	}))
	defer server.Close()
	source := ArkSource{BaseURL: server.URL}
	if !source.Applies(Row{Provider: "volcengine", Model: "doubao-seed-2.1-pro"}, "volcengine") {
		t.Fatal("ark must apply to a volcengine row")
	}
	if source.Applies(Row{Provider: "openai", Model: "gpt-4o"}, "") {
		t.Fatal("ark must not apply to another provider's row")
	}
	// Matched by the base name, the way a user types it.
	info, err := source.Fetch(context.Background(), Row{Provider: "volcengine", Model: "doubao-seed-2.1-pro"}, "ark-key")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if gotAuth != "Bearer ark-key" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if !reflect.DeepEqual(info.Efforts, []string{"low", "medium", "high", "minimal"}) || info.DefaultEffort != "high" {
		t.Fatalf("info = %+v", info)
	}
	if info.ContextWindow != 262144 || info.MaxOutput != 262144 || info.Origin != "ark-api" {
		t.Fatalf("info = %+v", info)
	}
	// Matched by the version id, the way the console names it.
	if _, err := source.Fetch(context.Background(), Row{Provider: "volcengine", Model: "doubao-seed-2-1-pro-260915"}, "ark-key"); err != nil {
		t.Fatalf("id match: %v", err)
	}
	// A model with no effort block answers without levels.
	plain, err := source.Fetch(context.Background(), Row{Provider: "volcengine", Model: "image-model"}, "ark-key")
	if err != nil {
		t.Fatalf("plain fetch: %v", err)
	}
	if len(plain.Efforts) != 0 {
		t.Fatalf("efforts = %v, want none", plain.Efforts)
	}
	if _, err := source.Fetch(context.Background(), Row{Provider: "volcengine", Model: "not-there"}, "ark-key"); !errors.Is(err, ErrNotListed) {
		t.Fatalf("unknown model err = %v, want ErrNotListed", err)
	}
	if _, err := source.Fetch(context.Background(), Row{Provider: "volcengine", Model: "doubao-seed-2.1-pro"}, "  "); err == nil {
		t.Fatal("a row with no key cannot be asked")
	}
}

func TestChatGPTSourceResolvesTheAccountList(t *testing.T) {
	source := ChatGPTSource{List: func(ctx context.Context) ([]openai.ModelInfo, error) {
		window := int64(272000)
		return []openai.ModelInfo{
			{Slug: "gpt-5.6-codex", DefaultReasoningEffort: "medium",
				SupportedReasoningEfforts: []openai.ReasoningEffortPreset{{Effort: "low"}, {Effort: "medium"}, {Effort: "high"}},
				ContextWindow:             &window, InputModalities: []string{"text", "image"}},
			{Slug: "plain-model"},
		}, nil
	}}
	if !source.Applies(Row{Provider: "chatgpt", Model: "gpt-5.6-codex"}, "chatgpt") {
		t.Fatal("chatgpt must apply to its own rows")
	}
	if source.Applies(Row{Provider: "openai", Model: "gpt-5.6-codex"}, "") {
		t.Fatal("chatgpt must not apply to an API-key row")
	}
	info, err := source.Fetch(context.Background(), Row{Provider: "chatgpt", Model: "gpt-5.6-codex"}, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !reflect.DeepEqual(info.Efforts, []string{"low", "medium", "high"}) || info.DefaultEffort != "medium" {
		t.Fatalf("info = %+v", info)
	}
	if info.ContextWindow != 272000 || info.Origin != "chatgpt-api" {
		t.Fatalf("info = %+v", info)
	}
	if info.Images == nil || !*info.Images {
		t.Fatalf("images = %v, want true", info.Images)
	}
	plain, err := source.Fetch(context.Background(), Row{Provider: "chatgpt", Model: "plain-model"}, "")
	if err != nil {
		t.Fatalf("plain fetch: %v", err)
	}
	if len(plain.Efforts) != 0 || plain.ContextWindow != 0 {
		t.Fatalf("plain info = %+v", plain)
	}
	if _, err := source.Fetch(context.Background(), Row{Provider: "chatgpt", Model: "absent"}, ""); !errors.Is(err, ErrNotListed) {
		t.Fatalf("absent model err = %v, want ErrNotListed", err)
	}
}

// TestResolverFallsBackToModelsDev pins the precedence chain: the subscription
// API that owns the row is asked first, and its failure resolves through
// models.dev rather than ending the answer.
func TestResolverFallsBackToModelsDev(t *testing.T) {
	server, hits := newModelsDevServer(t)
	dev := &ModelsDevSource{BaseURL: server.URL + "/catalog.json?type=all"}
	failing := ChatGPTSource{List: func(ctx context.Context) ([]openai.ModelInfo, error) {
		return nil, fmt.Errorf("backend down")
	}}
	resolver := &Resolver{Sources: []Source{failing, dev}}
	info, err := resolver.Resolve(context.Background(), Row{Provider: "chatgpt", Model: "gpt-5.6-codex"}, "", "")
	if err == nil {
		// models.dev does not list this model in the fixture.
		t.Fatalf("a row neither source lists must not resolve: %+v", info)
	}
	if atomic.LoadInt64(hits) == 0 {
		t.Fatal("the fall-back source was never asked")
	}
	// A row models.dev does list resolves with its origin, even after the
	// subscription source failed.
	model, err := resolver.Resolve(context.Background(), Row{Provider: "opencode-go", Model: "step-5-preview"}, "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if model.Origin != "models.dev" || !reflect.DeepEqual(model.Efforts, []string{"low", "medium", "high"}) {
		t.Fatalf("info = %+v", model)
	}
}

// TestResolverPrefersTheOwningSubscription pins the other half: a row a
// subscription owns is answered by it, and the general source is not consulted.
func TestResolverPrefersTheOwningSubscription(t *testing.T) {
	server, hits := newModelsDevServer(t)
	owning := ChatGPTSource{List: func(ctx context.Context) ([]openai.ModelInfo, error) {
		return []openai.ModelInfo{{Slug: "gpt-5.6-codex",
			SupportedReasoningEfforts: []openai.ReasoningEffortPreset{{Effort: "high"}}}}, nil
	}}
	resolver := &Resolver{Sources: []Source{owning, &ModelsDevSource{BaseURL: server.URL + "/catalog.json?type=all"}}}
	info, err := resolver.Resolve(context.Background(), Row{Provider: "chatgpt", Model: "gpt-5.6-codex"}, "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if info.Origin != "chatgpt-api" || !reflect.DeepEqual(info.Efforts, []string{"high"}) {
		t.Fatalf("info = %+v", info)
	}
	if got := atomic.LoadInt64(hits); got != 0 {
		t.Fatalf("models.dev was asked %d times for a subscription row", got)
	}
}

// TestResolverTimesOutOneSource pins the per-source bound: a source that never
// answers cannot hold the chain, and the fall-back still resolves.
func TestResolverTimesOutOneSource(t *testing.T) {
	server, _ := newModelsDevServer(t)
	stuck := ChatGPTSource{List: func(ctx context.Context) ([]openai.ModelInfo, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	resolver := &Resolver{
		Sources: []Source{stuck, &ModelsDevSource{BaseURL: server.URL + "/catalog.json?type=all"}},
		Timeout: 20 * time.Millisecond,
	}
	info, err := resolver.Resolve(context.Background(), Row{Provider: "opencode-go", Model: "step-5-preview"}, "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if info.Origin != "models.dev" {
		t.Fatalf("info = %+v", info)
	}
}
