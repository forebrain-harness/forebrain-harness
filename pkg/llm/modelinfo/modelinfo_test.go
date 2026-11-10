package modelinfo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
)

// shippedCatalogFixture is the shipped models.json shape: one flat table keyed
// "provider/model", each entry carrying capability fields, reasoning_options
// (absent, empty, toggle-only or effort levels) and an optional status.
const shippedCatalogFixture = `{
	"opencode-go/step-5-preview": {"id":"opencode-go/step-5-preview","reasoning":true,
		"reasoning_options":[{"type":"effort","values":["low","medium","high"]}],
		"limit":{"context":1000,"output":100},"attachment":true},
	"opencode-go/longcat-2.0": {"id":"opencode-go/longcat-2.0","reasoning":true,
		"reasoning_options":[{"type":"toggle"}],"limit":{"context":200,"output":20}},
	"openai/plain-model": {"id":"openai/plain-model","limit":{"context":4096}},
	"openai/gpt-4o": {"id":"openai/gpt-4o","reasoning":true,"attachment":true,
		"reasoning_options":[{"type":"effort","values":["low","medium","high"]}],
		"limit":{"context":128000,"output":16384}},
	"deepseek/deepseek-v4-flash": {"id":"deepseek/deepseek-v4-flash","reasoning":true,
		"reasoning_options":[{"type":"effort","values":["low","high"]}],
		"limit":{"context":1000000,"output":384000}},
	"deepseek/deepseek-v4-flash-0731": {"id":"deepseek/deepseek-v4-flash-0731","reasoning":true,
		"reasoning_options":[{"type":"effort","values":["low"]}],"limit":{"context":1,"output":1},
		"status":"deprecated"},
	"zhipuai/glm-5.3": {"id":"zhipuai/glm-5.3","reasoning":true,
		"reasoning_options":[{"type":"effort","values":["low","high","max"]}],
		"limit":{"context":1000000,"output":128000}},
	"alpha/dup": {"id":"alpha/dup","reasoning":true,
		"reasoning_options":[{"type":"effort","values":["low"]}],"limit":{"context":11,"output":1}},
	"beta/dup": {"id":"beta/dup","reasoning":true,
		"reasoning_options":[{"type":"effort","values":["high"]}],"limit":{"context":22,"output":2}}
}`

// installShippedCatalog makes the fixture the catalog this build answers from,
// the way startup does, and restores the global state afterwards.
func installShippedCatalog(t *testing.T) {
	t.Helper()
	cat, err := llm.Parse([]byte(shippedCatalogFixture))
	if err != nil {
		t.Fatalf("parse shipped catalog fixture: %v", err)
	}
	llm.SetGlobalCatalogForTest(cat)
	t.Cleanup(func() {
		llm.SetGlobalCatalogForTest(nil)
		llm.SetModelOverrides(nil, nil)
	})
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

// TestModelsDevSourceReadsTheShippedCatalog is the point of the change: the
// general source answers from the catalog beside the binary, offline, with no
// request and no cache of its own to go stale.
func TestModelsDevSourceReadsTheShippedCatalog(t *testing.T) {
	installShippedCatalog(t)
	source := ModelsDevSource{}
	if !source.Applies(Row{Provider: "opencode-go", Model: "step-5-preview"}, "opencode-go") {
		t.Fatal("the general source must apply to every row")
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
}

// A model the catalog has retired is not a source of truth for levels any more
// — the same answer the upstream document's status gives.
func TestModelsDevSourceSkipsDeprecated(t *testing.T) {
	installShippedCatalog(t)
	source := ModelsDevSource{}
	if _, err := source.Fetch(context.Background(), Row{Provider: "deepseek", Model: "deepseek-v4-flash-0731"}, ""); !errors.Is(err, ErrNotListed) {
		t.Fatalf("deprecated entry err = %v, want ErrNotListed", err)
	}
}

// A row that reasons with nothing discrete — a toggle, or an entry with no
// reasoning_options at all — is a successful answer with no levels: the surface
// then offers the checkboxes. It is not a failure.
func TestModelsDevSourceListedWithoutLevelsStillAnswers(t *testing.T) {
	installShippedCatalog(t)
	source := ModelsDevSource{}
	toggle, err := source.Fetch(context.Background(), Row{Provider: "opencode-go", Model: "longcat-2.0"}, "")
	if err != nil {
		t.Fatalf("toggle fetch: %v", err)
	}
	if len(toggle.Efforts) != 0 || toggle.ContextWindow != 200 {
		t.Fatalf("toggle info = %+v", toggle)
	}
	plain, err := source.Fetch(context.Background(), Row{Provider: "openai", Model: "plain-model"}, "")
	if err != nil {
		t.Fatalf("plain fetch: %v", err)
	}
	if len(plain.Efforts) != 0 || plain.ContextWindow != 4096 {
		t.Fatalf("plain info = %+v", plain)
	}
}

// A model the catalog does not carry is an answer too: ErrNotListed, which the
// resolver treats as "the user picks the levels", never as a failure.
func TestModelsDevSourceNotListedIsAnAnswer(t *testing.T) {
	installShippedCatalog(t)
	source := ModelsDevSource{}
	if _, err := source.Fetch(context.Background(), Row{Provider: "deepseek", Model: "no-such-model"}, ""); !errors.Is(err, ErrNotListed) {
		t.Fatalf("unknown model err = %v, want ErrNotListed", err)
	}
	// A provider the catalog has no rows for (meituan is one) asks the user
	// rather than inventing levels.
	if _, err := source.Fetch(context.Background(), Row{Provider: "meituan", Model: "LongCat-2.5"}, ""); !errors.Is(err, ErrNotListed) {
		t.Fatalf("meituan err = %v, want ErrNotListed", err)
	}
}

// TestModelsDevSourceIgnoresRowOverrides pins the layer split: the default
// answer reads LookupCatalog, so the row being edited never becomes part of
// what its own form calls "official" (plan 265).
func TestModelsDevSourceIgnoresRowOverrides(t *testing.T) {
	installShippedCatalog(t)
	no := false
	window := int64(64000)
	llm.SetModelOverrides(map[string]llm.ModelOverride{
		"openai/gpt-4o": {SupportsImages: &no, ContextWindow: &window, Efforts: []string{"high"}},
	}, map[string]string{"openai/gpt-4o": "openai/gpt-4o"})
	source := ModelsDevSource{}
	info, err := source.Fetch(context.Background(), Row{Provider: "openai", Model: "gpt-4o"}, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !reflect.DeepEqual(info.Efforts, []string{"low", "medium", "high"}) {
		t.Fatalf("efforts = %v, want the catalog's levels", info.Efforts)
	}
	if info.ContextWindow != 128000 || info.Images == nil || !*info.Images {
		t.Fatalf("info = %+v, want the catalog's defaults", info)
	}
}

// Custom rows are named the way a user types them. A vendor-prefixed name is
// its key; a bare name falls through to the suffix match, and several matches
// resolve to the first in sorted order so the answer is stable run to run.
func TestModelsDevSourceCustomLookups(t *testing.T) {
	installShippedCatalog(t)
	source := ModelsDevSource{}
	prefixed, err := source.Fetch(context.Background(), Row{Provider: "custom", Model: "openai/gpt-4o"}, "")
	if err != nil {
		t.Fatalf("prefixed fetch: %v", err)
	}
	if !reflect.DeepEqual(prefixed.Efforts, []string{"low", "medium", "high"}) || prefixed.ContextWindow != 128000 {
		t.Fatalf("prefixed info = %+v", prefixed)
	}
	bare, err := source.Fetch(context.Background(), Row{Provider: "custom", Model: "glm-5.3"}, "")
	if err != nil {
		t.Fatalf("bare fetch: %v", err)
	}
	if !reflect.DeepEqual(bare.Efforts, []string{"low", "high", "max"}) {
		t.Fatalf("bare info = %+v", bare)
	}
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

// brokenSource is a Source that applies to every row and always fails, the
// way an unreachable subscription API behaves.
type brokenSource struct{ err error }

func (s brokenSource) Applies(Row, string) bool { return true }
func (s brokenSource) Fetch(context.Context, Row, string) (Info, error) {
	return Info{}, s.err
}

// TestResolverFallsBackToTheCatalog pins the precedence chain: the source that
// owns the row is asked first, and its failure resolves through the shipped
// catalog rather than ending the answer.
func TestResolverFallsBackToTheCatalog(t *testing.T) {
	installShippedCatalog(t)
	resolver := &Resolver{Sources: []Source{brokenSource{err: fmt.Errorf("backend down")}, ModelsDevSource{}}}
	if _, err := resolver.Resolve(context.Background(), Row{Provider: "openai", Model: "no-such-model"}, "", ""); err == nil {
		t.Fatal("a row neither source lists must not resolve")
	}
	model, err := resolver.Resolve(context.Background(), Row{Provider: "openai", Model: "gpt-4o"}, "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if model.Origin != "models.dev" || !reflect.DeepEqual(model.Efforts, []string{"low", "medium", "high"}) {
		t.Fatalf("info = %+v", model)
	}
}

// TestResolverPrefersTheOwningSubscription pins the other half: a row a
// subscription owns is answered by it, and the catalog is not consulted.
func TestResolverPrefersTheOwningSubscription(t *testing.T) {
	installShippedCatalog(t)
	owning := ChatGPTSource{List: func(ctx context.Context) ([]openai.ModelInfo, error) {
		return []openai.ModelInfo{{Slug: "gpt-4o",
			SupportedReasoningEfforts: []openai.ReasoningEffortPreset{{Effort: "high"}}}}, nil
	}}
	resolver := &Resolver{Sources: []Source{owning, ModelsDevSource{}}}
	info, err := resolver.Resolve(context.Background(), Row{Provider: "chatgpt", Model: "gpt-4o"}, "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if info.Origin != "chatgpt-api" || !reflect.DeepEqual(info.Efforts, []string{"high"}) {
		t.Fatalf("info = %+v", info)
	}
}

// TestResolverTimesOutOneSource pins the per-source bound: a source that never
// answers cannot hold the chain, and the offline fall-back still resolves.
func TestResolverTimesOutOneSource(t *testing.T) {
	installShippedCatalog(t)
	stuck := ChatGPTSource{List: func(ctx context.Context) ([]openai.ModelInfo, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	resolver := &Resolver{
		Sources: []Source{stuck, ModelsDevSource{}},
		Timeout: 20 * time.Millisecond,
	}
	info, err := resolver.Resolve(context.Background(), Row{Provider: "openai", Model: "gpt-4o"}, "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if info.Origin != "models.dev" {
		t.Fatalf("info = %+v", info)
	}
}
