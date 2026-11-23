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
	if r.ShouldContinue("output_limit", "fragment", 1) {
		t.Error("a response with tool calls is never continued")
	}
	if r.ShouldContinue("end_turn", "完整的一句话。", 0) {
		t.Error("a response that is neither cut by the output limit nor cut structurally is never continued")
	}
	if !r.ShouldContinue("output_limit", "fragment", 0) {
		t.Error("the first cut-off response must continue")
	}
	for i := 0; i < OutputLimitContinuations; i++ {
		_ = r.Resume("fragment")
	}
	if r.ShouldContinue("output_limit", "fragment", 0) {
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
	// The first sentence must not claim a cause: the same prompt is now also
	// sent when the provider called a cut stream a normal finish.
	if !strings.HasPrefix(msgs[1].TextContent(), "Your previous answer was cut off before it finished.") {
		t.Errorf("resume prompt must open with the cause-neutral sentence, got %.60q", msgs[1].TextContent())
	}
	if strings.Contains(msgs[1].TextContent(), "Output token limit hit") {
		t.Errorf("resume prompt must not assert the output limit as the cause, got %.80q", msgs[1].TextContent())
	}
}

// TestAnswerCutOff pins the predicate against the answers it was designed on.
// The positives are the seven real tails the backtest found (a population of
// every tool-call-free assistant answer in the state database, 7 hits and 0
// false positives), each quoted verbatim from the rows it came from, plus two
// probes that distinguish this implementation from a cheaper one.
func TestAnswerCutOff(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{
			name: "567541: open parenthesis at the end (R1)",
			text: "未评审范围：`sql/20261123-审核队列凭证应急修复.sql` 的 SQL 语义、`audit_copilot` 仓库、以及 001 已落地实现的代码质量（那属于执",
			want: true,
		},
		{
			name: "425091: open Chinese quote at the end (R1)",
			text: "实现两处引擎修复：未知工具用通用中文动词＋工具名；答复里有“给人",
			want: true,
		},
		{
			name: "297264: open corner bracket at the end (R1)",
			text: "2. 脚本只做存量。之后新进 `system_user` 的用户不会自动有主智能体行——那正是你这轮让放弃的「completions /",
			want: true,
		},
		{
			name: "127630: open parenthesis before an English tail (R1)",
			text: "内容是 **OpenAI 官方 API 的 OpenAPI 3.1 规范**（title: \"OpenAI API\", version",
			want: true,
		},
		{
			name: "449330: unpaired backtick at the end of the line (R2)",
			text: "- 同时附上使用了 `MEMORY.md` 的 `",
			want: true,
		},
		{
			name: "405201: backtick opens a span that runs on (R2)",
			text: "**`Login`（:136-142）**：`AuthorizeURL()`、`UserCode()`（注释 `// device_code only`）、`Submit(ctx",
			want: true,
		},
		{
			name: "405137: backtick opens a span that runs on (R2)",
			text: "- 入口有两个：抽屉里服务商选 `自定义`；以及**对话页模型下拉底部** `添加",
			want: true,
		},
		{
			// The cut landed inside a code span, so the backtick has no
			// partner and must not be stripped: the parenthesis behind it is
			// real prose structure.
			name: "cut inside an unterminated span still sees the parenthesis (R1)",
			text: "说明：见 `（那属于执",
			want: true,
		},
		{
			// The discriminator between "drop the span" and "drop everything
			// before the span": the parenthesis sits before the paired span,
			// so only an implementation that keeps the text around the span
			// finds it.
			name: "parenthesis before a paired span still counts (R1)",
			text: "（见 `x` 说明",
			want: true,
		},
		{
			name: "333175: complete answer ending on a period",
			text: "on`). No commit, no stash/checkout; `plans/` untouched. Pre-existing unformatted `pkg/tui/chat_turn_test.go` left alone.",
			want: false,
		},
		{
			name: "290062: complete answer ending on a full stop",
			text: "随两页既有 yaml 块惯例（英 :34/:257、中 :32/:250 均有先例）；计划 Done criteria 的「git status 只显示两个文件」在嵌套仓现场不适用（owner 有未提交改动），已用编辑前后状态对照替代证明。",
			want: false,
		},
		{
			name: "Chinese list ending on a bare noun",
			text: "- 改了 output_limit.go\n- 改了 orchestration_llm.go",
			want: false,
		},
		{
			name: "balanced brackets that close before the end",
			text: "结构（已核对）完成",
			want: false,
		},
		{
			name: "answer ending on a code fence line",
			text: "说明如下：\n```",
			want: false,
		},
		{
			name: "backtick quoting a character",
			text: "用 ` 包裹代码",
			want: false,
		},
		{
			name: "backtick naming the character",
			text: "反引号是 ` 这个符号",
			want: false,
		},
		{
			// The parenthesis lives inside a paired span: literal text, not an
			// unclosed structure.
			name: "parenthesis quoted inside a paired span",
			text: "用 `（` 转义",
			want: false,
		},
		{
			// 115983: a subagent report whose body quotes an unclosed
			// parenthesis inside a span and whose answer ends on a fence. A
			// full-text scan called this a cut; the last-line scan does not.
			name: "body span holding an unclosed parenthesis, answer ends on a fence",
			text: "### 勘误\n\n- `grep count 勘误（2026-10-10` 得到的差异与上文一致\n\nDone. Followed the per instructions.\n```",
			want: false,
		},
		{name: "empty", text: "", want: false},
		{name: "whitespace only", text: "  \n\t\n ", want: false},
	}
	for _, tc := range cases {
		if got := AnswerCutOff(tc.text); got != tc.want {
			t.Errorf("AnswerCutOff(%s) = %v, want %v (text %.60q)", tc.name, got, tc.want, tc.text)
		}
	}
}

// TestShouldContinueOnClaimedCompleteStop pins which provider words can be
// contradicted by the text. The answer is cut in every case; only a reason that
// claims the answer is whole turns it into a continuation.
func TestShouldContinueOnClaimedCompleteStop(t *testing.T) {
	cut := "未评审范围：`sql/x.sql` 的 SQL 语义（那属于执"
	whole := "完整的一句话。"
	cases := []struct {
		reason   string
		fragment string
		want     bool
	}{
		// The four protocols' own words for "the answer is complete", plus
		// Gemini's upper-case spelling (NormalizeStopReason returns anything it
		// does not recognize unchanged, so the comparison must fold case).
		{"stop", cut, true},
		{"end_turn", cut, true},
		{"completed", cut, true},
		{"STOP", cut, true},
		// A cut that the provider admits stays on the output-limit path.
		{"length", cut, true},
		// Reasons that say something else cut the answer: re-asking is wrong.
		{"content_filter", cut, false},
		{"stop_sequence", cut, false},
		{"incomplete", cut, false},
		{"pause_turn", cut, false},
		{"refusal", cut, false},
		{"SAFETY", cut, false},
		// A claimed-complete answer that really is complete is left alone.
		{"stop", whole, false},
		{"end_turn", "", false},
	}
	for _, tc := range cases {
		var r OutputLimitRecovery
		if got := r.ShouldContinue(tc.reason, tc.fragment, 0); got != tc.want {
			t.Errorf("ShouldContinue(%q, %.40q) = %v, want %v", tc.reason, tc.fragment, got, tc.want)
		}
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
