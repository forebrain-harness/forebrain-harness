// Prompt suggestion: predict the user's next message over the conversation's
// cached request prefix, then sanitize and filter it so only a plausible,
// user-voiced next input is offered as ghost text. The instruction, the
// normalization, the word count and the rejection chain are a strict port of
// Claude Code 2.1.322's prompt-suggestion module (its M, q, C/w and D), so the
// two products make the same accept/reject decision on the same prediction.
package assembly

import (
	"context"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// SuggestionPrompt is the single instruction appended to the conversation's own
// next-request prefix. It is provider-neutral and byte-stable: the fork bills
// only this instruction plus the prediction against the cached prefix. The text
// is Claude Code's suggestion-mode prompt verbatim, down to its examples and its
// silence clauses, so the prediction it draws is the one CC's filter expects.
const SuggestionPrompt = `[SUGGESTION MODE: Suggest what the user might naturally type next into Claude Code.]

FIRST: Look at the user's recent messages and original request.

Your job is to predict what THEY would type - not what you think they should do.

THE TEST: Would they think "I was just about to type that"?

EXAMPLES:
User asked "fix the bug and run tests", bug is fixed → "run the tests"
After code written → "try it out"
Claude offers options → suggest the one the user would likely pick, based on conversation
Claude asks to continue → "yes" or "go ahead"
Task complete, obvious follow-up → "commit this" or "push it"
After error or misunderstanding → silence (let them assess/correct)

Be specific: "run the tests" beats "continue".

NEVER SUGGEST:
- Evaluative ("looks good", "thanks")
- Questions ("what about...?")
- Claude-voice ("Let me...", "I'll...", "Here's...")
- New ideas they didn't ask about
- Multiple sentences

Stay silent if the next step isn't obvious from what the user said.

Stay silent if a suggestion could be unsafe or inappropriate — including any sensitive topic (security incidents, credentials, harm, private data). Even when the user is doing legitimate security or cybersecurity work, do not predict potentially unsafe actions.

Format: 2-12 words, match the user's style. Or nothing.

Reply with ONLY the suggestion, no quotes or explanation.`

// Script tables for the word count, mirroring CC's A/R/L/x: Han half-weight,
// the phonetic scripts (kana plus the mainland Southeast Asian scripts) quarter-
// weight, Hangul and other letters/numbers counted per word.
var (
	suggHan      = unicode.Scripts["Han"]
	suggPhonetic = []*unicode.RangeTable{
		unicode.Scripts["Hiragana"], unicode.Scripts["Katakana"],
		unicode.Scripts["Thai"], unicode.Scripts["Lao"],
		unicode.Scripts["Khmer"], unicode.Scripts["Myanmar"],
	}
	suggHangul = unicode.Scripts["Hangul"]
)

func suggIsHan(r rune) bool { return unicode.Is(suggHan, r) }
func suggIsPhonetic(r rune) bool {
	for _, t := range suggPhonetic {
		if unicode.Is(t, r) {
			return true
		}
	}
	return false
}
func suggIsHangul(r rune) bool { return unicode.Is(suggHangul, r) }
func suggIsLetterNumber(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r)
}

// suggestionTagPair unwraps a whole <tag>…</tag> wrapper, but only when the
// inside carries no second closing tag of the same name — a nested wrapper is
// meta commentary, left intact for the filter to reject (CC's q guard). RE2 has
// no backreference, so the open and close names are captured and compared in the
// replacer rather than matched by \1.
var suggestionTagPair = regexp.MustCompile(`(?is)^<(suggestion|response|output|answer|result)>([\s\S]*)</(suggestion|response|output|answer|result)>$`)

// suggestionLabel strips a leading "Suggestion:" / "提案：" style label, in any
// of the languages CC names, before the prediction itself.
var suggestionLabel = regexp.MustCompile(`(?i)^\s*(suggested\s+(response|reply|input|prompt)|suggestion|response|reply|answer|output|result|提案|回答|返信|応答|出力|結果|建議|回复|答案|输出|结果|제안|답변|응답|출력|결과)\s*[:：]\s*`)

var (
	suggestionDoneRe      = regexp.MustCompile(`^\P{L}*(完了(しました)?|完成了?|완료됨?)\P{L}*$`)
	suggestionMetaRe      = regexp.MustCompile(`^\P{L}*(沉默|沈默|静默|침묵|提案(なし|はありません)|特に(なし|ありません)|[无没沒]有?建[议議]|(제안|해당)\s*없음)\P{L}*$`)
	suggestionSilenceRe   = regexp.MustCompile(`\bsilence is\b|\bstay(s|ing)? silent\b`)
	suggestionSilenceOnly = regexp.MustCompile(`^\W*silence\W*$`)
	suggestionWrappedRe   = regexp.MustCompile(`(?s)^(\(.*\)|\[.*\]|（.*）|［.*］|【.*】|〔.*〕)$`)
	suggestionWordLabel   = regexp.MustCompile(`^\w+:\s`)
	suggestionMulti       = regexp.MustCompile(`[.!?]\s+[A-Z]|[。！？]\s*[\p{L}\p{N}]`)
	suggestionFormatRe    = regexp.MustCompile(`[\n*]`)
	suggestionEvalCJK     = regexp.MustCompile(`^\P{L}*(ありがとう(ございます|ございました)?|助かりました|[谢謝][谢謝][你您]?|感谢[你您]?|感謝します|감사합니다|고마워요?)(\P{L}|的|$)|(良さそう|よさそう|いいですね|看起来不错|太好了|完美|完璧|좋네요)(です|ですね|だね)?\P{L}*$`)
	suggestionVoiceRe     = regexp.MustCompile(`(?i)^(let me|i'll|i've|i'm|i can|i would|i think|i notice|here's|here is|here are|that's|this is|this will|you can|you should|you could|sure,|of course|certainly|我来|我会|我将)`)
)

var (
	suggestionWordWhitelist = []string{"yes", "yeah", "yep", "yea", "yup", "sure", "ok", "okay", "push", "commit", "deploy", "stop", "continue", "check", "exit", "quit", "no"}
	suggestionErrorPrefixes = []string{"api error:", "prompt is too long", "request timed out", "invalid api key", "image was too large"}
	suggestionEvaluative    = []string{"thanks", "thank you", "looks good", "sounds good", "that works", "that worked", "that's all", "nice", "great", "perfect", "makes sense", "awesome", "excellent"}
)

// NormalizeSuggestion strips the wrapper a model sometimes adds around its
// prediction — an XML-ish tag pair or a "Suggestion:" label — leaving the bare
// text (CC's q). It does not unwrap parentheses/quotes: a wholly wrapped
// prediction is meta commentary, rejected by SuggestionRejection rather than
// rescued here.
func NormalizeSuggestion(raw string) string {
	t := strings.TrimSpace(raw)
	t = suggestionTagPair.ReplaceAllStringFunc(t, func(m string) string {
		sub := suggestionTagPair.FindStringSubmatch(m)
		if sub == nil || !strings.EqualFold(sub[1], sub[3]) {
			return m
		}
		tag, inner := sub[1], sub[2]
		if strings.Contains(inner, "</"+strings.ToLower(tag)+">") || strings.Contains(inner, "</"+strings.ToUpper(tag)+">") {
			return m
		}
		return inner
	})
	t = suggestionLabel.ReplaceAllString(t, "")
	return strings.TrimSpace(t)
}

// suggestionCJKCount is CC's w: the raw count of Han, phonetic and Hangul
// characters, used only to decide whether a sub-two-word prediction is CJK
// enough to stand on its own.
func suggestionCJKCount(t string) int {
	n := 0
	for _, r := range t {
		if suggIsHan(r) || suggIsPhonetic(r) || suggIsHangul(r) {
			n++
		}
	}
	return n
}

// suggestionWordCount is CC's C: a weighted word count. Each whitespace-run
// word contributes 1 when it has no Han or phonetic character; otherwise its
// Hangul/other letters count 1, each Han character 1/2 and each phonetic
// character 1/4, the sum rounded up. A Chinese prediction is thus measured in
// half-words the way a reader perceives its length, not per character.
func suggestionWordCount(t string) int {
	t = strings.TrimSpace(t)
	if t == "" {
		return 0
	}
	total := 0.0
	for _, word := range strings.Fields(t) {
		var han, phonetic, hangul, other int
		for _, r := range word {
			switch {
			case suggIsHan(r):
				han++
			case suggIsPhonetic(r):
				phonetic++
			case suggIsHangul(r):
				hangul++
			case suggIsLetterNumber(r):
				other++
			}
		}
		if han == 0 && phonetic == 0 {
			total += 1
			continue
		}
		w := 0.0
		if hangul+other > 0 {
			w += 1
		}
		w += float64(han)/2 + float64(phonetic)/4
		total += w
	}
	return int(math.Ceil(total))
}

// suggestionUTF16Len is JavaScript's String.length — the count of UTF-16 code
// units — which CC's too_long gate measures, so an astral character counts two.
func suggestionUTF16Len(t string) int {
	n := 0
	for _, r := range t {
		n += utf16.RuneLen(r)
	}
	return n
}

func suggestionContainsAny(low string, list []string) bool {
	for _, v := range list {
		if strings.Contains(low, v) {
			return true
		}
	}
	return false
}

func suggestionHasPrefixAny(low string, list []string) bool {
	for _, v := range list {
		if strings.HasPrefix(low, v) {
			return true
		}
	}
	return false
}

// SuggestionRejection returns the reason a normalized prediction must not be
// offered, or "" when it is acceptable. The order is CC's D chain verbatim:
// done, meta_text, meta_wrapped, error_message, prefixed_label, too_few_words,
// too_many_words, too_long, multiple_sentences, has_formatting, evaluative,
// claude_voice. A rejected prediction is silence — the composer shows nothing
// rather than a bad guess.
func SuggestionRejection(text string) string {
	if text == "" {
		return "empty"
	}
	low := strings.ToLower(text)
	words := suggestionWordCount(text)

	if low == "done" || suggestionDoneRe.MatchString(text) {
		return "done"
	}
	if low == "nothing found" || low == "nothing found." ||
		strings.HasPrefix(low, "nothing to suggest") || strings.HasPrefix(low, "no suggestion") ||
		suggestionSilenceRe.MatchString(low) || suggestionSilenceOnly.MatchString(low) ||
		suggestionMetaRe.MatchString(text) {
		return "meta_text"
	}
	if suggestionWrappedRe.MatchString(text) {
		return "meta_wrapped"
	}
	if suggestionHasPrefixAny(low, suggestionErrorPrefixes) {
		return "error_message"
	}
	if suggestionWordLabel.MatchString(text) {
		return "prefixed_label"
	}
	if words < 2 {
		if strings.HasPrefix(text, "/") {
			// a slash command stands alone
		} else if cjk := suggestionCJKCount(text); cjk > 0 {
			if cjk < 2 {
				return "too_few_words"
			}
		} else if !suggestionWhitelisted(low) {
			return "too_few_words"
		}
	}
	if words > 12 {
		return "too_many_words"
	}
	if suggestionUTF16Len(text) >= 100 {
		return "too_long"
	}
	if suggestionMulti.MatchString(text) {
		return "multiple_sentences"
	}
	if suggestionFormatRe.MatchString(text) {
		return "has_formatting"
	}
	if suggestionContainsAny(low, suggestionEvaluative) || suggestionEvalCJK.MatchString(text) {
		return "evaluative"
	}
	if suggestionVoiceRe.MatchString(text) {
		return "claude_voice"
	}
	// Go's RE2 has no lookahead: CC's 让我(?!们) is "让我" but not "让我们" ("let
	// us"), which is user-voiced and must pass.
	if strings.HasPrefix(text, "让我") && !strings.HasPrefix(text, "让我们") {
		return "claude_voice"
	}
	return ""
}

func suggestionWhitelisted(low string) bool {
	for _, w := range suggestionWordWhitelist {
		if low == w {
			return true
		}
	}
	return false
}

// SuggestNextStep predicts the conversation's next user message by sending the
// suggestion instruction as the conversation's next request — the same cached
// prefix its own turns carry — then sanitizing and filtering the answer. It
// returns "" (no error) when there is nothing acceptable to offer, so callers
// treat silence as a normal outcome.
func (s Service) SuggestNextStep(ctx context.Context, sessionID string) (string, error) {
	if s.ConversationSummary == nil {
		return "", nil
	}
	summarize := s.ConversationSummary(sessionID)
	if summarize == nil {
		return "", nil
	}
	res, err := summarize(llm.WithMutedForegroundStream(ctx), llm.UserMessage(llm.Text(SuggestionPrompt)))
	if err != nil {
		return "", err
	}
	if res == nil || res.Message == nil {
		return "", nil
	}
	norm := NormalizeSuggestion(res.Message.TextContent())
	if SuggestionRejection(norm) != "" {
		return "", nil
	}
	return norm, nil
}
