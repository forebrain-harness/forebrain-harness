package run

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

const (
	// subagentReportInlineChars is one report's inline budget. Characters, not
	// bytes, so a Chinese report and an English one get the same room; the
	// number is Claude Code's Agent-result cap.
	subagentReportInlineChars = 50_000
	// subagentFanoutInlineChars is everything one fanout may put in the
	// model's context, all reports together.
	subagentFanoutInlineChars = 200_000
	// subagentReportPreviewChars is what an over-budget report keeps when the
	// fanout as a whole is over budget: its head, enough to recognise the
	// report, plus the path to the rest.
	subagentReportPreviewChars = 2_000
)

// reportCutNote is the one sentence that says a report was cut, by how much,
// and where the whole of it lives. Provider-neutral and byte-stable for a
// given cut, per the FOREBRAIN.md house rules for model-visible text.
func reportCutNote(from, to int, path string) string {
	if strings.TrimSpace(path) == "" {
		return fmt.Sprintf("[report cut from %d to %d characters]", from, to)
	}
	return fmt.Sprintf("[report cut from %d to %d characters; the whole report is at %s — read it with read_file, its end carries conclusions]", from, to, strings.TrimSpace(path))
}

// cutAtLineBoundary returns s's first budget characters, pulled back to the
// last line boundary inside them, so a cut leaves whole lines rather than
// splitting one in half. A single line longer than the budget is cut
// mid-line: there is no boundary to honour.
func cutAtLineBoundary(s string, budget int) string {
	if budget <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= budget {
		return s
	}
	head := string([]rune(s)[:budget])
	if idx := strings.LastIndex(head, "\n"); idx > 0 {
		head = head[:idx]
	}
	return head
}

// cutReportBody is the single place a report body is cut for the model: a
// head of budget characters on a line boundary, plus — when something was cut
// — the fixed sentence naming the path to the whole report. The fanout
// section renderer and the subagent_run tool both funnel through here, so no
// caller grows a truncation of its own.
func cutReportBody(report string, budget int, path string) string {
	if utf8.RuneCountInString(report) <= budget {
		return report
	}
	cut := cutAtLineBoundary(report, budget)
	return cut + "\n" + reportCutNote(utf8.RuneCountInString(report), utf8.RuneCountInString(cut), path)
}

// spillSubagentReport writes a report to the tool-output spill directory
// exactly as the subagent produced it — no trimming, no newline rewriting —
// so the file behind a report's path is byte-identical to the report and
// read_file can page through it. Store, not GovernDetailed: governing here
// would write a second, truncated copy over the real full text.
func spillSubagentReport(fac Factory, taskID, report string) (string, error) {
	governor := newToolOutputGovernor(factoryToolsState(fac))
	return governor.Store("subagent_report", strings.TrimSpace(taskID), report)
}

// budgetSubagentReport is a report's one cut. Within budget the text is
// returned untouched and pathless; over budget the whole report is spilled
// first, and the head — cut on a line boundary, the path in the fixed
// sentence — is what goes to the model.
func budgetSubagentReport(fac Factory, taskID, report string, budget int) (inline, path string) {
	if utf8.RuneCountInString(report) <= budget {
		return report, ""
	}
	path, err := spillSubagentReport(fac, taskID, report)
	if err != nil {
		path = ""
	}
	return cutReportBody(report, budget, path), path
}

// subagentReportSection is one report as rendered into a fanout's text.
type subagentReportSection struct {
	Header string
	Body   string
	Path   string
	Chars  int
}

// fanoutSectionHeader is a section's first line. The name is the one every
// surface already calls the task by (tool.SubagentTaskTitle); status is ok,
// cancelled or failed; task_id and ran_for ride along when the result carries
// them, so a section can be acted on — subagent_status, a continuation —
// without re-reading anything.
func fanoutSectionHeader(index, total int, r FanoutResult) string {
	status := "failed"
	switch {
	case r.OK:
		status = "ok"
	case r.Status == string(agent.StatusCancelled):
		status = "cancelled"
	}
	head := fmt.Sprintf("## [%d/%d] %s · %s", index+1, total,
		tool.SubagentTaskTitle(r.Title, r.Task), status)
	if id := strings.TrimSpace(r.TaskID); id != "" {
		head += " · task_id=" + id
	}
	if ran := strings.TrimSpace(r.RanFor); ran != "" {
		head += " · ran_for=" + ran
	}
	return head
}

// fanoutFailureLines carries a failed or cancelled task's own account —
// error, resets_at, next — verbatim from the result's fields, in the
// key: value shape every other subagent failure text uses.
func fanoutFailureLines(r FanoutResult) string {
	if r.OK {
		return ""
	}
	var b strings.Builder
	for _, kv := range [][2]string{
		{"error", r.Error},
		{"resets_at", r.ResetsAt},
		{"next", r.Next},
	} {
		if v := strings.TrimSpace(kv[1]); v != "" {
			b.WriteString(kv[0] + ": " + v + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderSubagentReportSection renders one result as a section: the header
// line, the failure account when the task did not succeed, and the report
// body cut to budget by the shared cut in cutReportBody.
func renderSubagentReportSection(index, total int, r FanoutResult, path string, budget int) subagentReportSection {
	sec := subagentReportSection{Header: fanoutSectionHeader(index, total, r), Path: path}
	body := cutReportBody(r.Output, budget, path)
	if details := fanoutFailureLines(r); details != "" {
		if body == "" {
			body = details
		} else {
			body = details + "\n" + body
		}
	}
	sec.Body = body
	sec.Chars = utf8.RuneCountInString(body)
	return sec
}

// fanoutCardFacts is the thin sidecar a fanout's card rows settle from: per
// task only index, ok, status and error — the four keys the card derivation
// (tool.SubagentCallFromStep) reads. No prompt, no report text: those belong
// to the model-visible report and the subagent's own view, never to a card.
func fanoutCardFacts(results []FanoutResult) []map[string]any {
	facts := make([]map[string]any, 0, len(results))
	for _, r := range results {
		facts = append(facts, map[string]any{
			"index":  r.Index,
			"ok":     r.OK,
			"status": r.Status,
			"error":  r.Error,
		})
	}
	return facts
}

// fanoutSummaryLine is the report's first line: how many tasks, how they
// ended, how long the longest one ran — a fanout's tasks run together, so the
// longest task is the fanout's own duration.
func fanoutSummaryLine(results []FanoutResult) string {
	ok, failed, cancelled := 0, 0, 0
	var longest time.Duration
	for _, r := range results {
		switch {
		case r.OK:
			ok++
		case r.Status == string(agent.StatusCancelled):
			cancelled++
		default:
			failed++
		}
		if d, err := time.ParseDuration(strings.TrimSpace(r.RanFor)); err == nil && d > longest {
			longest = d
		}
	}
	line := fmt.Sprintf("%d tasks · %d ok · %d failed", len(results), ok, failed)
	if cancelled > 0 {
		line += fmt.Sprintf(" · %d cancelled", cancelled)
	}
	if ran := tool.FormatDuration(longest); ran != "" {
		line += " · " + ran
	}
	return line
}

// renderFanoutReport turns fanout results into the plain text the dispatching
// agent reads: a summary line, then one section per task in dispatch order.
// No task's prompt is echoed — a section names a task by its title, never by
// the prompt that started it. Each report is cut at most once: over
// subagentReportInlineChars on its own, or down to subagentReportPreviewChars
// when the reports together exceed subagentFanoutInlineChars, largest first.
// A cut report's whole text is spilled exactly once, as-is, and every section
// that alludes to it points at that one path.
func renderFanoutReport(fac Factory, results []FanoutResult) string {
	total := len(results)
	bodies := make([]string, total)
	paths := make([]string, total)
	for i := range results {
		bodies[i], paths[i] = budgetSubagentReport(fac, results[i].TaskID, results[i].Output, subagentReportInlineChars)
	}
	sum := 0
	for _, body := range bodies {
		sum += utf8.RuneCountInString(body)
	}
	// Largest first: the fewest downgrades bring the total back inside the
	// budget, and a downgraded section keeps its header and its path.
	order := make([]int, total)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return utf8.RuneCountInString(bodies[order[a]]) > utf8.RuneCountInString(bodies[order[b]])
	})
	downgraded := make([]bool, total)
	for _, i := range order {
		if sum <= subagentFanoutInlineChars {
			break
		}
		size := utf8.RuneCountInString(bodies[i])
		if size <= subagentReportPreviewChars {
			continue
		}
		if paths[i] == "" {
			if p, err := spillSubagentReport(fac, results[i].TaskID, results[i].Output); err == nil {
				paths[i] = p
			}
		}
		bodies[i] = cutReportBody(results[i].Output, subagentReportPreviewChars, paths[i])
		sum += utf8.RuneCountInString(bodies[i]) - size
		downgraded[i] = true
	}
	var b strings.Builder
	b.WriteString(fanoutSummaryLine(results))
	for i := range results {
		budget := subagentReportInlineChars
		if downgraded[i] {
			budget = subagentReportPreviewChars
		}
		sec := renderSubagentReportSection(i, total, results[i], paths[i], budget)
		b.WriteString("\n\n")
		b.WriteString(sec.Header)
		if sec.Body != "" {
			b.WriteString("\n")
			b.WriteString(sec.Body)
		}
	}
	return b.String()
}
