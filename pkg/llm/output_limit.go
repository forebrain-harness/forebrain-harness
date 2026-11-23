package llm

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

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

// AnswerCutOff reports whether text the provider called complete stops inside a
// structure it never closed. It is the second half of "the answer was cut":
// providers do report the output budget as the reason, and some upstreams end
// the stream mid-sentence while reporting a normal finish, which is
// indistinguishable from a whole answer unless the text itself is inspected.
//
// Three conditions must all hold, and the shape is deliberate: the tail allowed
// list alone is far too loose (a Chinese list often ends on a bare noun), and an
// unbalanced bracket anywhere in the body is too loose as well (a good answer
// quoting a fragment inside a code span). Only the last non-empty line is
// scanned, and only its unpaired structures count.
func AnswerCutOff(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if last, _ := utf8.DecodeLastRuneInString(trimmed); strings.ContainsRune(answerTailAllowed, last) {
		return false
	}
	line := lastNonEmptyLine(trimmed)
	return cutInsideOpenMark(line) || cutInsideCodeSpan(line)
}

const (
	// answerTailAllowed are the characters an answer that simply ended may stop
	// on. Most are sentence-final punctuation, but the set deliberately also
	// carries mid-sentence marks and a few symbols: the cost of a false
	// negative here is a half answer passing as a whole one, the cost of a
	// false positive is one extra round of continuation.
	answerTailAllowed = "。.！!？?；;：:，,、）)」』》”’】…~✓|>*_-"
	// answerOpenMarks and answerCloseMarks are the paired structures whose
	// unclosed half means the sentence continues.
	answerOpenMarks  = "（【「『《“‘"
	answerCloseMarks = "）】」』》”’"
)

// lastNonEmptyLine is the answer's last line that carries visible text, with
// trailing blanks dropped so "the end of the line" is where a reader sees it.
func lastNonEmptyLine(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimRight(lines[i], " \t\r"); strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

// cutInsideOpenMark reports whether the line ends inside a Chinese bracket or
// quotation mark that was opened and never closed. Paired backtick spans are
// literal text, not prose structure, so they are removed first: otherwise a
// complete sentence that quotes a bracket ("用 `（` 转义") reads as a cut. An
// unpaired backtick is left in place, so a cut that lands inside a span is still
// seen. Scanning backwards means the first mark found decides.
func cutInsideOpenMark(line string) bool {
	stripped := stripCodeSpans(line)
	for i := len(stripped); i > 0; {
		r, size := utf8.DecodeLastRuneInString(stripped[:i])
		i -= size
		if strings.ContainsRune(answerOpenMarks, r) {
			return true
		}
		if strings.ContainsRune(answerCloseMarks, r) {
			return false
		}
	}
	return false
}

// stripCodeSpans drops every paired `…` span from line. A backtick with no
// closing partner keeps itself and everything after it: an unterminated span is
// exactly the cut this check exists for. Written as an IndexByte loop rather
// than a compiled regexp because it runs on every tool-call-free response.
func stripCodeSpans(line string) string {
	var out strings.Builder
	rest := line
	for {
		open := strings.IndexByte(rest, '`')
		if open < 0 {
			out.WriteString(rest)
			return out.String()
		}
		shut := strings.IndexByte(rest[open+1:], '`')
		if shut < 0 {
			out.WriteString(rest)
			return out.String()
		}
		out.WriteString(rest[:open])
		rest = rest[open+shut+2:]
	}
}

// cutInsideCodeSpan reports whether the line ends inside a backtick span it just
// opened. Odd backticks mean the last one has no partner; it still has to open a
// span rather than name the character, so the character after it must be text or
// the line's end ("用 ` 包裹代码" and "反引号是 ` 这个符号" both finish). A line
// made only of backticks and tildes is a code fence, which closes what the answer
// opened higher up and is not itself a cut.
func cutInsideCodeSpan(line string) bool {
	if isFenceLine(line) {
		return false
	}
	if strings.Count(line, "`")%2 == 0 {
		return false
	}
	last := strings.LastIndexByte(line, '`')
	if last == len(line)-1 {
		return true
	}
	r, _ := utf8.DecodeRuneInString(line[last+1:])
	return !unicode.IsSpace(r)
}

// isFenceLine reports whether the line is a code fence (``` or ~~~) and nothing
// else.
func isFenceLine(line string) bool {
	for _, r := range strings.TrimSpace(line) {
		if r != '`' && r != '~' {
			return false
		}
	}
	return true
}

// claimsCompleteButCut is AnswerCutOff behind the provider's own words: only a
// stop reason that asserts the answer is whole can be contradicted by the text.
// NormalizeStopReason returns anything it does not recognize unchanged, and
// Gemini reports STOP in upper case, so the comparison is case-insensitive. A
// response cut by the content filter or by a stop sequence is deliberately not
// here: re-asking one of those is wrong (see NormalizeStopReason).
func claimsCompleteButCut(stopReason, fragment string) bool {
	switch strings.ToLower(NormalizeStopReason(stopReason)) {
	case "stop", "end_turn", "completed":
		return AnswerCutOff(fragment)
	}
	return false
}

// OutputLimitContinuations is how many times one call re-asks after its answer
// was cut. The owner's 2026-11-23 ruling raised it from three to a hundred: an
// upstream that ends the stream mid-sentence — whether it admits it or not — has
// to be given room to finish a long answer. The cost is bounded but real: every
// round re-sends the whole conversation plus the answer so far, so the request
// behind the incident this was raised for (225k prompt tokens) can cost ~22.7M
// input tokens and ~17 minutes inside one Execute if all 100 rounds run. Most of
// that input hits the prompt cache, so the bill is far below face value.
// Cancelling is not checked between rounds; the next round's request observes
// the cancelled context, so a cancel lands at the end of the current round.
const OutputLimitContinuations = 100

const (
	// outputLimitResumePrompt is the message the model is asked to continue on.
	// The wording is deliberately blunt and provider-neutral: an apology or a
	// recap costs exactly the tokens this recovery is trying to buy back.
	outputLimitResumePrompt = "Your previous answer was cut off before it finished. Resume directly — no apology, no recap of what you were doing. Pick up mid-thought if that is where the cut happened. Break remaining work into smaller pieces."
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
// re-asking would duplicate them. Neither is a response that produced nothing
// and was not cut off. An answer is continued on the provider's own report (the
// output budget ran out), and also when the report says the answer is whole
// while the text plainly is not — an upstream that ends the stream mid-sentence
// and still sends "stop" is the same failure as an honest length cut, and
// trusting the report recorded half a sentence as a finished answer.
func (r *OutputLimitRecovery) ShouldContinue(stopReason, fragment string, toolCalls int) bool {
	if toolCalls > 0 {
		return false
	}
	if !OutputLimitReached(stopReason) && !claimsCompleteButCut(stopReason, fragment) {
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
// text is kept, but the user has to know it is a prefix. The wording names no
// cause because there are two — a budget that really ran out and an upstream
// that cut the stream and called it a completion — and the advice fits both.
const OutputLimitExhaustedNotice = "The answer was cut off before it finished and could not be continued further. Raise the model's output limit, lower its reasoning effort, or ask for a smaller piece of work."

// OutputLimitEmptyAnswerError is the terminal error when the budget ran out
// before any answer text existed at all (thinking consumed it). It replaces the
// old "empty assistant response after tool execution", which described a
// symptom and hid the cause.
const OutputLimitEmptyAnswerError = "the model's output budget ran out while it was still thinking, so no answer was produced; lower its reasoning effort or raise its output limit"
