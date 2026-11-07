package llm

import (
	"context"
	"errors"
	"testing"
)

// The incident of plans/273: a response that settled with no text and no tool
// call although the provider billed 688 output tokens for it.
func TestBlankResultErrorFlagsBilledEmpty(t *testing.T) {
	msg := AssistantMessage(nil)
	res := &Result{Message: &msg, Usage: &Usage{OutputTokens: 688}, StopReason: "stop"}
	err := BlankResultError(res)
	if err == nil {
		t.Fatal("a settled response that billed output yet delivered nothing must be flagged")
	}
	if !errors.Is(err, ErrBlankModelOutput) {
		t.Fatalf("error = %v, want ErrBlankModelOutput", err)
	}
	if !APIErrorIsBlankModelOutput(err) {
		t.Fatalf("APIErrorIsBlankModelOutput(%v) = false, want true", err)
	}
	if usage := UsageFromError(err); usage == nil || usage.OutputTokens != 688 {
		t.Fatalf("usage = %+v, want the billed 688 output tokens", usage)
	}
}

// A multi-round call bills its continuation rounds on BilledUsage; that view,
// not the settled round's Usage, decides whether the empty answer was paid for.
func TestBlankResultErrorUsesBilledUsage(t *testing.T) {
	msg := AssistantMessage(nil)
	res := &Result{
		Message:     &msg,
		Usage:       &Usage{OutputTokens: 0},
		BilledUsage: &Usage{OutputTokens: 50},
		StopReason:  "stop",
	}
	if err := BlankResultError(res); err == nil {
		t.Fatal("BilledUsage carries the billed output; an empty settled round under it must still be flagged")
	}
}

func TestBlankResultErrorLeavesOthersAlone(t *testing.T) {
	textMsg := AssistantMessage([]ContentPart{Text("hello")})
	callMsg := AssistantMessage(nil, ToolCall{
		ID:   "call-1",
		Type: ToolTypeFunction,
		Function: FunctionCall{
			Name:      "probe",
			Arguments: `{}`,
		},
	})
	emptyMsg := AssistantMessage(nil)
	cases := []struct {
		name string
		res  *Result
	}{
		{"text present", &Result{Message: &textMsg, Usage: &Usage{OutputTokens: 5}, StopReason: "stop"}},
		{"tool call present", &Result{Message: &callMsg, Usage: &Usage{OutputTokens: 5}, StopReason: "stop"}},
		{"nothing billed", &Result{Message: &emptyMsg, Usage: &Usage{InputTokens: 10}}},
		{"output limit is plans/272's", &Result{Message: &emptyMsg, Usage: &Usage{OutputTokens: 688}, StopReason: "output_limit"}},
		{"nil result", nil},
		{"nil message", &Result{Usage: &Usage{OutputTokens: 688}, StopReason: "stop"}},
	}
	for _, tc := range cases {
		if err := BlankResultError(tc.res); err != nil {
			t.Errorf("%s: BlankResultError = %v, want nil", tc.name, err)
		}
	}
	// A refusal the provider named stays a refusal: re-sending would only be
	// refused again, and calling it a blank output would hide it.
	for _, reason := range []string{"refusal", "content_filter", "sensitive"} {
		res := &Result{Message: &emptyMsg, Usage: &Usage{OutputTokens: 688}, StopReason: reason}
		if err := BlankResultError(res); err != nil {
			t.Errorf("stop reason %q: BlankResultError = %v, want nil", reason, err)
		}
	}
}

// blankStaticLLM returns one fixed outcome on every call.
type blankStaticLLM struct {
	res *Result
	err error
}

func (s *blankStaticLLM) Execute(context.Context, []Message, []*Tool) (*Result, error) {
	return s.res, s.err
}

func TestBlankResultAsErrorPassesErrorsAndAnswersThrough(t *testing.T) {
	boom := errors.New("boom")
	wrapped := NewBlankResultAsError()(&blankStaticLLM{err: boom})
	if _, err := wrapped.Execute(context.Background(), nil, nil); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the inner error returned as is", err)
	}
	msg := AssistantMessage([]ContentPart{Text("the answer")})
	answer := &Result{Message: &msg}
	wrapped = NewBlankResultAsError()(&blankStaticLLM{res: answer})
	res, err := wrapped.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if res != answer {
		t.Fatalf("result = %p, want the answered result pointer returned as is (%p)", res, answer)
	}
}
