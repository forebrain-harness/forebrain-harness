package llm

import "strings"

// DefaultOutputTokenCap is the output budget when neither the model's row nor
// the catalog names one. Claude Code uses the same number for models its table
// does not describe; 8192 (the old default here) leaves too little room for a
// reasoning model, whose thinking shares the output budget with its answer.
const DefaultOutputTokenCap = 32000

// OutputTokenCap resolves the max_tokens one call may ask for. rowMax is the
// per-row override a user set (0 when none): an explicit setting always wins.
// Otherwise the catalog's limit.output is used, capped at the default — a
// 128k-output model still gets 32k, because the cap protects the context
// window and the provider's own concurrency limits, not the model's ceiling.
func OutputTokenCap(provider, model string, rowMax int64) int {
	if rowMax > 0 {
		return int(rowMax)
	}
	if hit, ok := Lookup(provider, model); ok && hit.DefaultMaxTokens > 0 {
		return min(int(hit.DefaultMaxTokens), DefaultOutputTokenCap)
	}
	return DefaultOutputTokenCap
}

// StopReasonOutputLimit is the normalized reason every adapter reports when the
// provider cut the response because the output budget ran out.
const StopReasonOutputLimit = "output_limit"

// NormalizeStopReason maps each protocol's vocabulary onto StopReasonOutputLimit.
// Anthropic says max_tokens, Chat Completions length, Gemini MAX_TOKENS, and an
// incomplete Responses response names max_output_tokens as its reason. The
// Responses status "incomplete" is deliberately not on the list: a response
// cut by the content filter is incomplete too, and re-asking it is wrong.
func NormalizeStopReason(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "max_tokens", "length", "max_output_tokens":
		return StopReasonOutputLimit
	}
	return strings.TrimSpace(reason)
}

// OutputLimitReached reports whether a response was cut by the output budget.
func OutputLimitReached(stopReason string) bool {
	return NormalizeStopReason(stopReason) == StopReasonOutputLimit
}

// OutputLimitContinuations is how many times one call re-asks after the output
// budget ran out. Three is what Claude Code uses: enough for a long answer that
// was cut mid-sentence, small enough that a model stuck in a thinking loop
// cannot spend an unbounded budget.
const OutputLimitContinuations = 3

const (
	// outputLimitResumePrompt is the message the model is asked to continue on.
	// The wording is deliberately blunt and provider-neutral: an apology or a
	// recap costs exactly the tokens this recovery is trying to buy back.
	outputLimitResumePrompt = "Output token limit hit. Resume directly — no apology, no recap of what you were doing. Pick up mid-thought if that is where the cut happened. Break remaining work into smaller pieces."
	// outputLimitThinkingOnlyPrompt is the resume message when the cut happened
	// before any answer text existed. There is nothing to continue from, so the
	// model is told that and asked to spend less on thinking.
	outputLimitThinkingOnlyPrompt = "Output token limit hit while you were still reasoning, before this response produced any text or tool call. That reasoning was not kept and is not part of this conversation, so there is nothing to resume. Continue from the conversation above: reason much more briefly and produce your answer or next tool call well within the limit."
)

// OutputLimitRecovery is the one state machine every adapter runs. It is not
// shared code by accident: the same failure used to be handled four different
// ways (a 16-round prefill loop in Chat Completions, nothing at all in the other
// three protocols), which is how a cut-off answer became a hard error on
// Anthropic and a silent fragment on OpenAI.
type OutputLimitRecovery struct{ rounds int }

// ShouldContinue decides whether the response just received is re-asked. Calls
// are never continued: they are work the model already decided to do, and
// re-asking would duplicate them. A response that produced nothing and was not
// cut off is not a continuation case either.
func (r *OutputLimitRecovery) ShouldContinue(stopReason string, producedChars, toolCalls int) bool {
	if toolCalls > 0 || !OutputLimitReached(stopReason) {
		return false
	}
	return r.rounds < OutputLimitContinuations
}

// Resume turns the cut-off response into the two messages the next request
// carries: the fragment as the assistant's own turn (so the model continues
// instead of restarting), and the resume instruction as a user turn. The
// fragment is omitted when it is empty — an assistant turn with no content is
// rejected by every protocol, and in the thinking-only case there is nothing to
// continue from anyway.
func (r *OutputLimitRecovery) Resume(fragment string) []Message {
	r.rounds++
	out := make([]Message, 0, 2)
	if strings.TrimSpace(fragment) != "" {
		out = append(out, AssistantMessage([]ContentPart{Text(fragment)}))
	}
	prompt := outputLimitResumePrompt
	if strings.TrimSpace(fragment) == "" {
		prompt = outputLimitThinkingOnlyPrompt
	}
	return append(out, UserMessage(Text(prompt)))
}

// Exhausted reports whether the recovery budget is spent: the caller then hands
// back the fragment it has, with StopReason set, instead of asking again.
func (r *OutputLimitRecovery) Exhausted() bool { return r.rounds >= OutputLimitContinuations }

// OutputLimitExhaustedNotice is shown when a turn ends on a cut-off answer: the
// text is kept, but the user has to know it is a prefix.
const OutputLimitExhaustedNotice = "The answer was cut off by this model's output limit and could not be continued further. Raise the model's output limit, lower its reasoning effort, or ask for a smaller piece of work."

// OutputLimitEmptyAnswerError is the terminal error when the budget ran out
// before any answer text existed at all (thinking consumed it). It replaces the
// old "empty assistant response after tool execution", which described a
// symptom and hid the cause.
const OutputLimitEmptyAnswerError = "the model's output budget ran out while it was still thinking, so no answer was produced; lower its reasoning effort or raise its output limit"
