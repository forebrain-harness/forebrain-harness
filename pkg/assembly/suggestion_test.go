package assembly

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// TestSuggestionRejectionMatrix pins the sanitize+filter chain to the same
// accept/reject decisions the web prototype's filter scene demonstrates, row for
// row. A rejected prediction is silence, so the reason matters only for tests
// and telemetry; the composer shows nothing either way.
func TestSuggestionRejectionMatrix(t *testing.T) {
	cases := []struct {
		raw  string
		want string // "" = accept
	}{
		{"run the tests", ""},
		{"跑一下测试", ""},
		{"yes", ""},
		{"/compact", ""},
		{"looks good", "evaluative"},
		{"谢谢", "evaluative"},
		{"what should I do next?", ""},
		{"Let me check the logs", "claude_voice"},
		{"我来看看", "claude_voice"},
		{"让我们继续", ""},
		{"I fixed the bug. Now I will run the tests.", "multiple_sentences"},
		{"nothing found", "meta_text"},
		{"(run the tests)", "meta_wrapped"},
		{"Suggestion: run the tests", ""},
		{"api error: 429", "error_message"},
		{"a", "too_few_words"},
	}
	for _, c := range cases {
		norm := NormalizeSuggestion(c.raw)
		got := SuggestionRejection(norm)
		if got != c.want {
			t.Errorf("raw=%q norm=%q rejection=%q want %q", c.raw, norm, got, c.want)
		}
	}
}

func TestSuggestionNormalizeStripsWrappers(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"Suggestion: run the tests", "run the tests"},
		{"suggestion： run the tests", "run the tests"},
		{"<suggestion>run the tests</suggestion>", "run the tests"},
		{"建議：跑一下测试", "跑一下测试"},
		{"  run the tests  ", "run the tests"},
		{"(run the tests)", "(run the tests)"}, // wraps are rejected, not rescued
	}
	for _, c := range cases {
		if got := NormalizeSuggestion(c.raw); got != c.want {
			t.Errorf("NormalizeSuggestion(%q)=%q want %q", c.raw, got, c.want)
		}
	}
}

func TestSuggestionWordCountCJK(t *testing.T) {
	// CC's C weights Han at half a word each: five Han characters are ceil(5/2)=3.
	if got := suggestionWordCount("跑一下测试"); got != 3 {
		t.Errorf("CJK word count=%d want 3", got)
	}
	if got := suggestionWordCount("run the tests"); got != 3 {
		t.Errorf("latin word count=%d want 3", got)
	}
	// A single Han character is ceil(1/2)=1, and the raw CJK count gates it.
	if got := suggestionWordCount("跑"); got != 1 {
		t.Errorf("single Han word count=%d want 1", got)
	}
	if got := suggestionCJKCount("跑一下测试"); got != 5 {
		t.Errorf("raw CJK count=%d want 5", got)
	}
}

func TestSuggestionShapeFilters(t *testing.T) {
	long := strings.Repeat("aaaaaaaaaa ", 10) // 110 chars, 10 words: past too_long, under too_many_words
	if got := SuggestionRejection(long); got != "too_long" {
		t.Errorf("100 chars rejection=%q want too_long", got)
	}
	if got := SuggestionRejection("one two three four five six seven eight nine ten eleven twelve thirteen"); got != "too_many_words" {
		t.Errorf("13 words rejection=%q want too_many_words", got)
	}
	if got := SuggestionRejection("run the tests\n- step"); got != "has_formatting" {
		t.Errorf("newline rejection=%q want has_formatting", got)
	}
	if got := SuggestionRejection("done"); got != "done" {
		t.Errorf("done rejection=%q want done", got)
	}
	if got := SuggestionRejection(""); got != "empty" {
		t.Errorf("empty rejection=%q want empty", got)
	}
}

// TestSuggestionSuggestNextStep checks the fork wrapper: an acceptable prediction
// passes through, a rejected one becomes silence, and a missing summarizer or a
// provider error is handled without offering anything.
func TestSuggestionSuggestNextStep(t *testing.T) {
	msg := llm.UserMessage(llm.Text("run the tests"))
	svc := Service{ConversationSummary: func(string) ConversationSummarizer {
		return func(context.Context, llm.Message) (*llm.Result, error) {
			return &llm.Result{Message: &msg}, nil
		}
	}}
	got, err := svc.SuggestNextStep(context.Background(), "s1")
	if err != nil || got != "run the tests" {
		t.Fatalf("acceptable prediction got=%q err=%v", got, err)
	}

	bad := llm.UserMessage(llm.Text("looks good"))
	svc.ConversationSummary = func(string) ConversationSummarizer {
		return func(context.Context, llm.Message) (*llm.Result, error) {
			return &llm.Result{Message: &bad}, nil
		}
	}
	got, err = svc.SuggestNextStep(context.Background(), "s1")
	if err != nil || got != "" {
		t.Fatalf("rejected prediction should be silence got=%q err=%v", got, err)
	}

	svc.ConversationSummary = func(string) ConversationSummarizer { return nil }
	got, err = svc.SuggestNextStep(context.Background(), "s1")
	if err != nil || got != "" {
		t.Fatalf("nil summarizer got=%q err=%v", got, err)
	}

	svc.ConversationSummary = nil
	got, err = svc.SuggestNextStep(context.Background(), "s1")
	if err != nil || got != "" {
		t.Fatalf("nil ConversationSummary got=%q err=%v", got, err)
	}

	boom := errors.New("provider down")
	svc.ConversationSummary = func(string) ConversationSummarizer {
		return func(context.Context, llm.Message) (*llm.Result, error) { return nil, boom }
	}
	_, err = svc.SuggestNextStep(context.Background(), "s1")
	if !errors.Is(err, boom) {
		t.Fatalf("provider error should propagate got=%v", err)
	}
}
