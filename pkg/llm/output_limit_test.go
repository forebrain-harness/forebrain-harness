package llm

import (
	"strings"
	"testing"
)

func TestNormalizeStopReason(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"max_tokens", StopReasonOutputLimit},
		{"length", StopReasonOutputLimit},
		{"MAX_TOKENS", StopReasonOutputLimit},
		{"max_output_tokens", StopReasonOutputLimit},
		// The Responses status "incomplete" is deliberately not an output
		// limit: the content filter cuts a response the same way.
		{"incomplete", "incomplete"},
		{"content_filter", "content_filter"},
		{"end_turn", "end_turn"},
		{"", ""},
		{"  Max_Tokens  ", StopReasonOutputLimit},
	}
	for _, tc := range cases {
		if got := NormalizeStopReason(tc.in); got != tc.want {
			t.Errorf("NormalizeStopReason(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if !OutputLimitReached("max_tokens") || !OutputLimitReached("length") || !OutputLimitReached("MAX_TOKENS") {
		t.Error("OutputLimitReached must hold for every output-limit vocabulary")
	}
	if OutputLimitReached("incomplete") || OutputLimitReached("content_filter") || OutputLimitReached("") {
		t.Error("OutputLimitReached must not hold for non-limit reasons")
	}
}

func TestOutputLimitRecoveryShouldContinue(t *testing.T) {
	var r OutputLimitRecovery
	if r.ShouldContinue("output_limit", 10, 1) {
		t.Error("a response with tool calls is never continued")
	}
	if r.ShouldContinue("end_turn", 10, 0) {
		t.Error("a response not cut by the output limit is never continued")
	}
	if !r.ShouldContinue("output_limit", 10, 0) {
		t.Error("the first cut-off response must continue")
	}
	for i := 0; i < OutputLimitContinuations; i++ {
		_ = r.Resume("fragment")
	}
	if r.ShouldContinue("output_limit", 10, 0) {
		t.Errorf("the recovery must stop after %d continuations", OutputLimitContinuations)
	}
	if !r.Exhausted() {
		t.Error("Exhausted must be true once the continuation budget is spent")
	}
}

func TestOutputLimitRecoveryResume(t *testing.T) {
	var r OutputLimitRecovery
	msgs := r.Resume("")
	if len(msgs) != 1 || msgs[0].Role != RoleUser {
		t.Fatalf("Resume(\"\") = %d messages, want exactly 1 user message", len(msgs))
	}
	// The thinking-only prompt is the one sent when there is no fragment to
	// continue from; an empty assistant turn is rejected by every protocol.
	if !strings.Contains(msgs[0].TextContent(), "reason much more briefly") {
		t.Errorf("Resume(\"\") must send the thinking-only prompt, got %.60q", msgs[0].TextContent())
	}

	msgs = r.Resume("abc")
	if len(msgs) != 2 {
		t.Fatalf("Resume(\"abc\") = %d messages, want 2", len(msgs))
	}
	if msgs[0].Role != RoleAssistant || msgs[0].TextContent() != "abc" {
		t.Errorf("first message must be the assistant fragment, got role=%s text=%q", msgs[0].Role, msgs[0].TextContent())
	}
	if msgs[1].Role != RoleUser {
		t.Errorf("second message must be the user prompt, got role=%s", msgs[1].Role)
	}
	if !strings.Contains(msgs[1].TextContent(), "Resume directly") {
		t.Errorf("Resume with a fragment must send the resume prompt, got %.60q", msgs[1].TextContent())
	}
}

func TestOutputTokenCap(t *testing.T) {
	// The shipped catalog's opencode-go/qwen3.8-max row carries a 131072
	// output limit; the cap compresses it to the default.
	if got := OutputTokenCap("opencode-go", "qwen3.8-max", 0); got != DefaultOutputTokenCap {
		t.Errorf("OutputTokenCap(catalog 131072, no row) = %d, want %d", got, DefaultOutputTokenCap)
	}
	if got := OutputTokenCap("opencode-go", "qwen3.8-max", 50000); got != 50000 {
		t.Errorf("OutputTokenCap(row 50000) = %d, want 50000", got)
	}
	if got := OutputTokenCap("nobody", "never-heard-of-it", 0); got != DefaultOutputTokenCap {
		t.Errorf("OutputTokenCap(unknown model) = %d, want %d", got, DefaultOutputTokenCap)
	}
	// A catalog row below the cap keeps its own, smaller figure.
	if got := OutputTokenCap("openai", "gpt-4o", 0); got <= 0 || got > DefaultOutputTokenCap {
		t.Errorf("OutputTokenCap(openai/gpt-4o) = %d, want the catalog figure capped at %d", got, DefaultOutputTokenCap)
	}
}
