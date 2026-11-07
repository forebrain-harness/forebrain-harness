package llm

import (
	"context"
	"errors"
	"strings"
)

// ErrBlankModelOutput marks a response that settled with no text and no tool
// call although the provider billed output tokens for it. Its text contains
// "empty output" on purpose: APIErrorIsBlankModelOutput recognised that phrase
// before this sentinel existed, and errors.Is now recognises the sentinel.
var ErrBlankModelOutput = errors.New("model returned empty output")

// stopReasonIsRefusal reports a finish reason with which the provider said it
// declined or filtered the answer. Re-sending the same request would be
// refused again, and calling the refusal a blank output would hide it.
func stopReasonIsRefusal(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "refusal", "content_filter", "sensitive":
		return true
	}
	return false
}

// BlankResultError returns ErrBlankModelOutput, carrying the billed usage, when
// res is a settled response that delivered nothing the turn can use: no tool
// call, no text, yet output tokens billed. A response cut by the output budget
// is not blank — its recovery is the output-limit continuation — and neither is
// a refusal. Otherwise it returns nil.
func BlankResultError(res *Result) error {
	if res == nil || res.Message == nil || len(res.Message.ToolCalls) > 0 {
		return nil
	}
	if strings.TrimSpace(res.Message.TextContent()) != "" {
		return nil
	}
	billed := res.Billed()
	if billed == nil || billed.OutputTokens <= 0 {
		return nil
	}
	if OutputLimitReached(res.StopReason) || stopReasonIsRefusal(res.StopReason) {
		return nil
	}
	return WithUsageError(ErrBlankModelOutput, billed)
}

// NewBlankResultAsError turns a blank settled response into ErrBlankModelOutput
// so the blank-output retry above it re-sends the request. Without it such a
// response is a success that nothing retries, and the engine later throws the
// whole turn away as an empty answer.
func NewBlankResultAsError() LLMMiddleware {
	return func(next LLM) LLM { return &blankResultAsErrorLLM{inner: next} }
}

type blankResultAsErrorLLM struct{ inner LLM }

func (b *blankResultAsErrorLLM) Execute(ctx context.Context, messages []Message, tools []*Tool) (*Result, error) {
	res, err := b.inner.Execute(ctx, messages, tools)
	if err != nil {
		return res, err
	}
	if blank := BlankResultError(res); blank != nil {
		return nil, blank
	}
	return res, nil
}
