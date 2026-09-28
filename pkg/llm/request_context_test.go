package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkoukk/tiktoken-go"
)

func TestParseCacheUsage(t *testing.T) {
	tests := []struct {
		name string
		body string
		want Usage
	}{
		{"openai", `{"usage":{"prompt_tokens":100,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":40}}}`, Usage{InputTokens: 60, OutputTokens: 7, CacheReadInputTokens: 40}},
		{"deepseek", `{"usage":{"prompt_tokens":100,"prompt_cache_hit_tokens":45,"cache_creation_input_tokens":20}}`, Usage{InputTokens: 35, CacheReadInputTokens: 45, CacheCreationInputTokens: 20}},
		{"anthropic", `{"usage":{"input_tokens":100,"output_tokens":7,"cache_read_input_tokens":45,"cache_creation_input_tokens":20}}`, Usage{InputTokens: 35, OutputTokens: 7, CacheReadInputTokens: 45, CacheCreationInputTokens: 20}},
		{"kimi", `{"usage":{"prompt_tokens":100,"cached_tokens":45}}`, Usage{InputTokens: 55, CacheReadInputTokens: 45}},
		{"glm", `{"usage":{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":45}}}`, Usage{InputTokens: 55, CacheReadInputTokens: 45}},
		{"qwen", `{"usage":{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":45},"cache_creation_input_tokens":20}}`, Usage{InputTokens: 35, CacheReadInputTokens: 45, CacheCreationInputTokens: 20}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseCacheUsage([]byte(tt.body))
			if !ok || got != tt.want {
				t.Fatalf("ParseCacheUsage() = %#v, %v; want %#v, true", got, ok, tt.want)
			}
		})
	}
}

func TestCacheUsageTransportRestoresBodyAndReadsSSE(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			body := `{"usage":{"prompt_tokens":10,"cached_tokens":4}}`
			contentType := "application/json"
			if stream {
				body = "data: " + body + "\n\ndata: [DONE]\n"
				contentType = "text/event-stream"
			}
			sink := NewCacheUsageSink()
			req, err := http.NewRequestWithContext(WithCacheUsageSink(context.Background(), sink), http.MethodPost, "https://example.test", nil)
			if err != nil {
				t.Fatal(err)
			}
			rt := &CacheUsageTransport{Next: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadAll(resp.Body); err != nil {
				t.Fatal(err)
			}
			got, ok := sink.Usage()
			if !ok || got.InputTokens != 6 || got.CacheReadInputTokens != 4 {
				t.Fatalf("usage = %#v, %v", got, ok)
			}
		})
	}
}

func TestCacheUsageTransportFlushesUnterminatedSSELineOnClose(t *testing.T) {
	sink := NewCacheUsageSink()
	req, err := http.NewRequestWithContext(WithCacheUsageSink(context.Background(), sink), http.MethodPost, "https://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	body := `data: {"usage":{"prompt_tokens":12,"cached_tokens":5}}`
	rt := &CacheUsageTransport{Next: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	got, ok := sink.Usage()
	if !ok || got.InputTokens != 7 || got.CacheReadInputTokens != 5 {
		t.Fatalf("usage = %#v, %v", got, ok)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestFastIsFalseOnBareContext(t *testing.T) {
	ctx := context.Background()
	if Fast(ctx) {
		t.Fatalf("expected false on bare context, got true")
	}
}

func TestFastIsFalseOnNilContext(t *testing.T) {
	if Fast(nil) {
		t.Fatalf("expected false on nil context, got true")
	}
}

func TestWithFastRoundTrips(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
	}{
		{"enabled", true},
		{"disabled", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithFast(context.Background(), tc.enabled)
			if got := Fast(ctx); got != tc.enabled {
				t.Fatalf("Fast(ctx) = %v, want %v", got, tc.enabled)
			}
		})
	}
}

func TestWithFastLastValueWins(t *testing.T) {
	ctx := WithFast(context.Background(), true)
	ctx = WithFast(ctx, false)
	if Fast(ctx) {
		t.Fatalf("expected false after override, got true")
	}
}

func TestConfigureDoesNotBlockOnEncodingLoad(t *testing.T) {
	resetTokestimateStateForTest()
	orig := tiktokenGetEncoding
	defer func() {
		tiktokenGetEncoding = orig
		resetTokestimateStateForTest()
	}()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	tiktokenGetEncoding = func(name string) (*tiktoken.Tiktoken, error) {
		started <- struct{}{}
		<-release
		return nil, errors.New("boom")
	}
	if err := Configure(TokenEstimateOptions{Encoding: tiktoken.MODEL_CL100K_BASE}); err != nil {
		t.Fatalf("Configure error: %v", err)
	}
	select {
	case <-started:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("background load did not start")
	}
	if got := Label(); got != "rune_div4" {
		t.Fatalf("label during pending=%q", got)
	}
	close(release)
	waitForTokestimateSettled(t)
	if got := Label(); got != "rune_div4" {
		t.Fatalf("label after fallback=%q", got)
	}
}

func TestConfigurePromotesToTiktokenAfterBackgroundReady(t *testing.T) {
	resetTokestimateStateForTest()
	defer resetTokestimateStateForTest()
	orig := tiktokenGetEncoding
	defer func() { tiktokenGetEncoding = orig }()
	tk := &tiktoken.Tiktoken{}
	tiktokenGetEncoding = func(name string) (*tiktoken.Tiktoken, error) {
		return tk, nil
	}
	if err := Configure(TokenEstimateOptions{Encoding: tiktoken.MODEL_CL100K_BASE}); err != nil {
		t.Fatalf("Configure error: %v", err)
	}
	waitForTokestimateSettled(t)
	if got := Label(); got != "tiktoken:"+tiktoken.MODEL_CL100K_BASE {
		t.Fatalf("label after ready=%q", got)
	}
	st.mu.Lock()
	loaded := st.tk == tk && !st.heur
	st.mu.Unlock()
	if !loaded {
		t.Fatal("expected background-loaded tokenizer to become active")
	}
}

func waitForTokestimateSettled(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		pending := st.pending
		st.mu.Unlock()
		if !pending {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("tokestimate did not settle")
}

func resetTokestimateStateForTest() {
	st.mu.Lock()
	st.tk = nil
	st.label = ""
	st.encoding = ""
	st.heur = false
	st.pending = false
	st.gen = 0
	st.useHF = false
	st.mu.Unlock()
	resetHFStateForTest()
}

func TestInstall_EmptyHome(t *testing.T) {
	if err := Install(""); err == nil {
		t.Fatal("expected error for empty forebrainHome")
	}
	if err := Install("   "); err == nil {
		t.Fatal("expected error for whitespace-only forebrainHome")
	}
}

func TestInstall_SeedsWhenMissing(t *testing.T) {
	tmp := t.TempDir()
	if err := Install(tmp); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	target := filepath.Join(tmp, "models.json")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("models.json not written: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("models.json is empty")
	}

	// Content must match the embedded catalog.
	embedded := EmbeddedModelsJSON()
	if len(data) != len(embedded) {
		t.Fatalf("seeded size = %d, want %d", len(data), len(embedded))
	}
}

func TestInstall_DoesNotOverwriteExisting(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "models.json")

	// Pre-write a user-customized models.json.
	custom := []byte(`{"custom/model": {"id":"custom/model","limit":{"context":999}}}`)
	if err := os.WriteFile(target, custom, 0o644); err != nil {
		t.Fatalf("pre-write: %v", err)
	}

	if err := Install(tmp); err != nil {
		t.Fatalf("Install: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(custom) {
		t.Fatal("Install overwrote existing user-customized models.json")
	}
}

func TestInstall_ThenResolve(t *testing.T) {
	tmp := t.TempDir()

	// Before Install, Resolve should fail (no models.json).
	if _, err := Resolve(tmp); err == nil {
		t.Fatal("expected Resolve to fail before Install")
	}

	if err := Install(tmp); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// After Install, Resolve should succeed and return a usable catalog.
	cat, err := Resolve(tmp)
	if err != nil {
		t.Fatalf("Resolve after Install: %v", err)
	}
	if cat == nil || cat.Len() == 0 {
		t.Fatal("catalog is nil or empty after Install + Resolve")
	}
}

// stubLLM is a minimal LLM for testing.
type stubLLM struct {
	fn func(ctx context.Context, messages []Message, tools []*Tool) (*Result, error)
}

func (s *stubLLM) Execute(ctx context.Context, messages []Message, tools []*Tool) (*Result, error) {
	return s.fn(ctx, messages, tools)
}

func okLLM(content string) *stubLLM {
	return &stubLLM{fn: func(_ context.Context, _ []Message, _ []*Tool) (*Result, error) {
		return &Result{Message: &Message{Parts: []ContentPart{Text(content)}}}, nil
	}}
}

func errLLM(err error) *stubLLM {
	return &stubLLM{fn: func(_ context.Context, _ []Message, _ []*Tool) (*Result, error) {
		return nil, err
	}}
}

func loadTestCatalog(t *testing.T) *Catalog {
	t.Helper()
	cat, err := Parse(EmbeddedModelsJSON())
	if err != nil {
		t.Fatalf("parse embedded models.json: %v", err)
	}
	return cat
}

func TestLookupMiss(t *testing.T) {
	t.Parallel()
	if _, ok := Lookup("openai", " "); ok {
		t.Fatal("expected empty model miss")
	}
	if _, ok := Lookup("openai", "missing"); ok {
		t.Fatal("expected miss")
	}
}

func TestLookupHitAndCatalogID(t *testing.T) {
	SetGlobalCatalogForTest(loadTestCatalog(t))
	defer SetGlobalCatalogForTest(nil)

	hit, ok := Lookup("openai", "gpt-4o")
	if !ok {
		t.Fatal("expected lookup hit for openai/gpt-4o")
	}
	if hit.ContextWindow == 0 {
		t.Fatalf("expected non-zero context window, got %+v", hit)
	}
	if hit.CatalogID == "" {
		t.Fatalf("expected non-empty catalog id, got %+v", hit)
	}
}

func TestKnownProviderDerivedFromCatalog(t *testing.T) {
	SetGlobalCatalogForTest(loadTestCatalog(t))
	defer SetGlobalCatalogForTest(nil)

	// Providers present as prefixes in models.json are known.
	for _, p := range []string{"openai", "anthropic", "deepseek", "zhipuai", "alibaba", "moonshotai", "google", "minimax", "xai", "mistral"} {
		if !KnownProvider(p) {
			t.Fatalf("KnownProvider(%q) = false, want true", p)
		}
	}
	// Strings that are not catalog prefixes are unknown.
	for _, p := range []string{"qwen", "moonshot", "zai", "openrouter", "groq", "ollama", "gpt", ""} {
		if KnownProvider(p) {
			t.Fatalf("KnownProvider(%q) = true, want false", p)
		}
	}
}

// The label format has one definition on each side; a missing half must not
// produce a dangling separator, and every form must survive the round trip.
func TestFormatProviderModelRoundTrips(t *testing.T) {
	cases := []struct {
		provider, model, want string
	}{
		{"openai", "gpt-5.1", "openai / gpt-5.1"},
		{"", "gpt-5.1", "gpt-5.1"},
		{"openai", "", "openai"},
		{"", "", ""},
		{"  anthropic  ", "  claude-opus-4-1 ", "anthropic / claude-opus-4-1"},
	}
	for _, tc := range cases {
		got := FormatProviderModel(tc.provider, tc.model)
		if got != tc.want {
			t.Fatalf("FormatProviderModel(%q,%q) = %q, want %q", tc.provider, tc.model, got, tc.want)
		}
		if got == "" {
			continue
		}
		provider, model := ParseProviderModelLabel(got)
		if model != strings.TrimSpace(tc.model) && strings.TrimSpace(tc.model) != "" {
			t.Fatalf("round trip of %q lost the model: %q", got, model)
		}
		if strings.TrimSpace(tc.provider) != "" && strings.TrimSpace(tc.model) != "" &&
			provider != strings.TrimSpace(tc.provider) {
			t.Fatalf("round trip of %q lost the provider: %q", got, provider)
		}
	}
}

type stubRT struct{ marked bool }

func (s *stubRT) RoundTrip(*http.Request) (*http.Response, error) { return nil, nil }

// The whole point of inverting these ports is that the tracing which used to
// be unconditional still reaches provider traffic. If WrapHTTPTransport
// stopped applying the installed wrapper, every provider request would quietly
// lose its trace and nothing would fail.
func TestWrapHTTPTransportAppliesTheInstalledWrapper(t *testing.T) {
	t.Cleanup(func() { SetHTTPTransportWrapper(nil) })

	inner := &stubRT{}
	if got := WrapHTTPTransport(inner); got != http.RoundTripper(inner) {
		t.Fatalf("with no wrapper installed, WrapHTTPTransport returned %T, want the transport unchanged", got)
	}

	var sawInner http.RoundTripper
	marker := &stubRT{marked: true}
	SetHTTPTransportWrapper(func(next http.RoundTripper) http.RoundTripper {
		sawInner = next
		return marker
	})
	got := WrapHTTPTransport(inner)
	if got != http.RoundTripper(marker) {
		t.Fatalf("WrapHTTPTransport returned %T, want the wrapper's result", got)
	}
	if sawInner != http.RoundTripper(inner) {
		t.Fatal("the wrapper was not handed the original transport, so the chain is broken")
	}

	// NewHTTPClient must go through the same wrapper, otherwise a provider
	// built with it is untraced while one built from an explicit transport is.
	if c := NewHTTPClient(); c.Transport != http.RoundTripper(marker) {
		t.Fatalf("NewHTTPClient transport = %T, want the wrapped one", c.Transport)
	}
	// Streaming responses are long-lived; a client-level timeout would cut
	// them off mid-stream.
	if c := NewHTTPClient(); c.Timeout != 0 {
		t.Fatalf("NewHTTPClient timeout = %v, want none", c.Timeout)
	}

	SetHTTPTransportWrapper(nil)
	if got := WrapHTTPTransport(inner); got != http.RoundTripper(inner) {
		t.Fatalf("after clearing, WrapHTTPTransport returned %T, want the transport unchanged", got)
	}
}

// A nil transport means http.DefaultTransport, matching net/http's own rule.
func TestWrapHTTPTransportSubstitutesTheDefaultForNil(t *testing.T) {
	t.Cleanup(func() { SetHTTPTransportWrapper(nil) })
	var seen http.RoundTripper
	SetHTTPTransportWrapper(func(next http.RoundTripper) http.RoundTripper {
		seen = next
		return next
	})
	WrapHTTPTransport(nil)
	if seen != http.DefaultTransport {
		t.Fatalf("wrapper saw %T for a nil transport, want http.DefaultTransport", seen)
	}
}

func TestLogDebugIsSilentUntilInstalled(t *testing.T) {
	t.Cleanup(func() { SetDebugLogger(nil) })
	LogDebug("topic", "must not panic with no logger installed")

	var got [2]string
	SetDebugLogger(func(topic, message string) { got = [2]string{topic, message} })
	LogDebug("openai_responses", "content.Type=text")
	if got != [2]string{"openai_responses", "content.Type=text"} {
		t.Fatalf("logger received %v, want the topic and message verbatim", got)
	}

	SetDebugLogger(nil)
	got = [2]string{}
	LogDebug("topic", "dropped")
	if got != [2]string{} {
		t.Fatalf("logger still received %v after being cleared", got)
	}
}

func TestIsOpus(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
		want     bool
	}{
		{"anthropic opus 4", "anthropic", "claude-opus-4-7", true},
		{"anthropic opus mixed case", "Anthropic", "Claude-Opus-4-7", true},
		{"anthropic sonnet not opus", "anthropic", "claude-sonnet-4-6", false},
		{"anthropic haiku not opus", "anthropic", "claude-haiku-4-5", false},
		{"openai gpt not opus", "openai", "gpt-4o", false},
		{"non-anthropic with opus in name", "openai", "opus-clone", false},
		{"empty provider", "", "claude-opus-4-7", false},
		{"empty model", "anthropic", "", false},
		{"whitespace trimmed", "  anthropic  ", "  claude-opus-4-7  ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsOpus(tc.provider, tc.model); got != tc.want {
				t.Fatalf("IsOpus(%q,%q) = %v, want %v", tc.provider, tc.model, got, tc.want)
			}
		})
	}
}

func TestIsOpusLabel(t *testing.T) {
	cases := []struct {
		name  string
		label string
		want  bool
	}{
		{"plain opus label", "anthropic/claude-opus-4-7", true},
		{"label with extra", "anthropic/claude-opus-4-7 - fast", true},
		{"sonnet label", "anthropic/claude-sonnet-4-6", false},
		{"openai label", "openai/gpt-4o", false},
		{"empty label", "", false},
		{"no slash", "claude-opus-4-7", false},
		{"whitespace around segments", "  anthropic / claude-opus-4-7  ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsOpusLabel(tc.label); got != tc.want {
				t.Fatalf("IsOpusLabel(%q) = %v, want %v", tc.label, got, tc.want)
			}
		})
	}
}

func TestNewExecutionTiming(t *testing.T) {
	started := time.Now()
	completed := started.Add(75 * time.Millisecond)
	timing := NewExecutionTiming(started, completed)
	if !timing.Valid() {
		t.Fatalf("timing should be valid: %#v", timing)
	}
	if timing.StartedAt != started || timing.CompletedAt != completed {
		t.Fatalf("timestamps changed: %#v", timing)
	}
	if timing.Duration != 75*time.Millisecond {
		t.Fatalf("duration=%s want 75ms", timing.Duration)
	}
}

func TestNewExecutionTimingRejectsIncompleteOrReversedInterval(t *testing.T) {
	now := time.Now()
	for _, timing := range []ExecutionTiming{
		NewExecutionTiming(time.Time{}, now),
		NewExecutionTiming(now, time.Time{}),
		NewExecutionTiming(now, now.Add(-time.Millisecond)),
	} {
		if timing.Valid() {
			t.Fatalf("timing unexpectedly valid: %#v", timing)
		}
	}
}

func TestAggregateExecutionTimingsUsesMemberSpan(t *testing.T) {
	base := time.Now()
	got := AggregateExecutionTimings(
		NewExecutionTiming(base.Add(20*time.Millisecond), base.Add(70*time.Millisecond)),
		ExecutionTiming{},
		NewExecutionTiming(base, base.Add(40*time.Millisecond)),
	)
	if !got.StartedAt.Equal(base) {
		t.Fatalf("started=%s want %s", got.StartedAt, base)
	}
	wantCompleted := base.Add(70 * time.Millisecond)
	if !got.CompletedAt.Equal(wantCompleted) {
		t.Fatalf("completed=%s want %s", got.CompletedAt, wantCompleted)
	}
	if got.Duration != 70*time.Millisecond {
		t.Fatalf("duration=%s want 70ms", got.Duration)
	}
}

// --- TruncateRunes ---

func TestTruncateRunes_EmptyString(t *testing.T) {
	if got := TruncateRunes("", 10); got != "" {
		t.Errorf("TruncateRunes(%q, 10) = %q, want %q", "", got, "")
	}
}

func TestTruncateRunes_ZeroMax(t *testing.T) {
	if got := TruncateRunes("hello", 0); got != "" {
		t.Errorf("TruncateRunes(%q, 0) = %q, want %q", "hello", got, "")
	}
}

func TestTruncateRunes_NegativeMax(t *testing.T) {
	if got := TruncateRunes("hello", -1); got != "" {
		t.Errorf("TruncateRunes(%q, -1) = %q, want %q", "hello", got, "")
	}
}

func TestTruncateRunes_ExactlyMaxLength(t *testing.T) {
	s := "hello"
	if got := TruncateRunes(s, 5); got != s {
		t.Errorf("TruncateRunes(%q, 5) = %q, want %q", s, got, s)
	}
}

func TestTruncateRunes_ShorterThanMax(t *testing.T) {
	s := "hi"
	if got := TruncateRunes(s, 10); got != s {
		t.Errorf("TruncateRunes(%q, 10) = %q, want %q", s, got, s)
	}
}

func TestTruncateRunes_LongerThanMax(t *testing.T) {
	s := "hello world"
	got := TruncateRunes(s, 5)
	if got != "hello" {
		t.Errorf("TruncateRunes(%q, 5) = %q, want %q", s, got, "hello")
	}
}

func TestTruncateRunes_MultiByteCharacters(t *testing.T) {
	s := "ＡＢＣＤ" // 4 fullwidth characters
	got := TruncateRunes(s, 2)
	if got != "ＡＢ" {
		t.Errorf("TruncateRunes(%q, 2) = %q, want %q", s, got, "ＡＢ")
	}
}

func TestTruncateRunes_MixedASCIIAndUnicode(t *testing.T) {
	s := "HelloＡＢWorld"
	got := TruncateRunes(s, 8)
	want := "HelloＡＢW"
	if got != want {
		t.Errorf("TruncateRunes(%q, 8) = %q, want %q", s, got, want)
	}
}

func TestTruncateRunes_MaxOne(t *testing.T) {
	s := "abc"
	got := TruncateRunes(s, 1)
	if got != "a" {
		t.Errorf("TruncateRunes(%q, 1) = %q, want %q", s, got, "a")
	}
}

// --- LeadingTokenLooksLikeFilesystemPath ---

func TestLeadingTokenLooksLikeFilesystemPath_Empty(t *testing.T) {
	if LeadingTokenLooksLikeFilesystemPath("") {
		t.Error("expected empty token to return false")
	}
}

func TestLeadingTokenLooksLikeFilesystemPath_Whitespace(t *testing.T) {
	if LeadingTokenLooksLikeFilesystemPath("   ") {
		t.Error("expected whitespace token to return false")
	}
}

func TestLeadingTokenLooksLikeFilesystemPath_WindowsDrive(t *testing.T) {
	if !LeadingTokenLooksLikeFilesystemPath(`C:\Windows\System32`) {
		t.Error("expected Windows drive path to match")
	}
}

func TestLeadingTokenLooksLikeFilesystemPath_UnixTwoSeg(t *testing.T) {
	if !LeadingTokenLooksLikeFilesystemPath("/usr/local/bin") {
		t.Error("expected Unix two-segment path to match")
	}
}

func TestLeadingTokenLooksLikeFilesystemPath_Tilde(t *testing.T) {
	if !LeadingTokenLooksLikeFilesystemPath("~/") {
		t.Error("expected tilde to match")
	}
}

func TestLeadingTokenLooksLikeFilesystemPath_TildeSlash(t *testing.T) {
	if !LeadingTokenLooksLikeFilesystemPath("~/config") {
		t.Error("expected tilde/slash to match")
	}
}

func TestLeadingTokenLooksLikeFilesystemPath_DotDot(t *testing.T) {
	if !LeadingTokenLooksLikeFilesystemPath("../src") {
		t.Error("expected ../ to match")
	}
}

func TestLeadingTokenLooksLikeFilesystemPath_DotDotDot(t *testing.T) {
	if !LeadingTokenLooksLikeFilesystemPath("./build") {
		t.Error("expected ./ to match")
	}
}

func TestLeadingTokenLooksLikeFilesystemPath_SingleSlash(t *testing.T) {
	if LeadingTokenLooksLikeFilesystemPath("/") {
		t.Error("expected single slash to return false")
	}
}

func TestLeadingTokenLooksLikeFilesystemPath_SingleChar(t *testing.T) {
	if LeadingTokenLooksLikeFilesystemPath("a") {
		t.Error("expected single char to return false")
	}
}

func TestLeadingTokenLooksLikeFilesystemPath_URL(t *testing.T) {
	if LeadingTokenLooksLikeFilesystemPath("https://example.com") {
		t.Error("expected URL to return false")
	}
}

func TestLeadingTokenLooksLikeFilesystemPath_Command(t *testing.T) {
	if LeadingTokenLooksLikeFilesystemPath("ls -la") {
		t.Error("expected command to return false")
	}
}

// =============================================================================
// LeadingTokenLooksLikeFilesystemPath — table-driven tests
// =============================================================================

func TestLeadingTokenLooksLikeFilesystemPath(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  bool
	}{
		// ---- Windows drive-letter paths ----
		{
			name:  "Windows C:\\ path",
			token: `C:\Windows\System32`,
			want:  true,
		},
		{
			name:  "Windows D:/ path forward slash",
			token: `D:/Program Files/app`,
			want:  true,
		},
		{
			name:  "Windows lowercase drive e:",
			token: `e:\temp\out.log`,
			want:  true,
		},
		{
			name:  "Windows Z:\\ deep nesting",
			token: `Z:\a\b\c\d`,
			want:  true,
		},

		// ---- Unix absolute paths (must have >= 2 slashes total) ----
		{
			name:  "/usr/bin two segments",
			token: "/usr/bin",
			want:  true,
		},
		{
			name:  "/var/log/syslog three segments",
			token: "/var/log/syslog",
			want:  true,
		},
		{
			name:  "/ single slash only",
			token: "/",
			want:  false,
		},
		{
			name:  "/tmp single segment",
			token: "/tmp",
			want:  false,
		},
		{
			name:  "/etc/hosts two segments",
			token: "/etc/hosts",
			want:  true,
		},

		// ---- Tilde paths ----
		{
			name:  "~/home tilde-slash",
			token: "~/home",
			want:  true,
		},
		{
			name:  "~ bare tilde",
			token: "~",
			want:  true,
		},
		{
			name:  "~/.bashrc hidden dotfile",
			token: "~/.bashrc",
			want:  true,
		},

		// ---- Relative paths ----
		{
			name:  "./file dot-dot-slash",
			token: "./file",
			want:  true,
		},
		{
			name:  "../dir parent-dir-slash",
			token: "../dir",
			want:  true,
		},
		{
			name:  "../../nested double parent",
			token: "../../nested/deep",
			want:  true,
		},
		{
			name:  "./ build directory",
			token: "./build",
			want:  true,
		},

		// ---- Edge cases that should NOT match ----
		{
			name:  "empty string",
			token: "",
			want:  false,
		},
		{
			name:  "whitespace only",
			token: "   ",
			want:  false,
		},
		{
			name:  "single letter",
			token: "a",
			want:  false,
		},
		{
			name:  "two letters",
			token: "ab",
			want:  false,
		},
		{
			name:  "three letters no separator",
			token: "abc",
			want:  false,
		},
		{
			name:  "plain words separated by space",
			token: "ls -la",
			want:  false,
		},
		{
			name:  "URL scheme not filesystem",
			token: "https://example.com",
			want:  false,
		},
		{
			name:  "ftp URL not filesystem",
			token: "ftp://files.example.com",
			want:  false,
		},
		{
			name:  "command with flag",
			token: "cat --help",
			want:  false,
		},
		{
			name:  "colon but no backslash/slash after",
			token: "http:",
			want:  false,
		},
		{
			name:  "drive-like but missing separator",
			token: "C:no_sep",
			want:  false,
		},
		{
			name:  "trailing colon only",
			token: "C:",
			want:  false,
		},
		{
			name:  "number token",
			token: "42",
			want:  false,
		},
		{
			name:  "symbol only",
			token: "@#$%",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LeadingTokenLooksLikeFilesystemPath(tt.token)
			if got != tt.want {
				t.Errorf("LeadingTokenLooksLikeFilesystemPath(%q) = %v, want %v",
					tt.token, got, tt.want)
			}
		})
	}
}
