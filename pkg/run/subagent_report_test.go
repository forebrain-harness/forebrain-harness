package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	toolpkg "github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// perTaskReportExecutor answers each fanout task with a report of its own,
// keyed by the task's prompt, so one fanout can deliver six distinct reports.
type perTaskReportExecutor struct {
	*fixedSubagentExecutor
	report func(task string) string
}

func (e *perTaskReportExecutor) RunSubagentExec(_ context.Context, req SubagentExecRequest) (string, error) {
	return e.report(req.Task), nil
}

// sizedReport builds a deterministic multi-line report whose first and last
// lines carry the task's own markers. Middle lines are 101 bytes each
// (100 ASCII characters plus the newline), so a report of n lines is about
// n*101 bytes and n*101 characters alike.
func sizedReport(task string, lines int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "BEGIN %s\n", task)
	for i := 0; i < lines-2; i++ {
		fmt.Fprintf(&b, "%s%04d\n", strings.Repeat("x", 96), i)
	}
	fmt.Fprintf(&b, "END %s\n", task)
	return b.String()
}

// fanoutReportFor drives one subagent_fanout call and returns the report text
// the dispatching agent receives.
func fanoutReportFor(t *testing.T, exec SubagentExecutor, args string) string {
	t.Helper()
	home := t.TempDir()
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterSubagentTool(a, Factory{
		Home:  home,
		Owner: &Runner{Deps: &Deps{AppCfg: subagentsEnabledConfig()}, SubagentExecutor: exec},
		Tools: toolpkg.NewState(home),
	}); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	fanTool, ok := aToolByName(a, "subagent_fanout")
	if !ok {
		t.Fatal("subagent_fanout not registered")
	}
	got, err := fanTool.Handle(context.Background(), args)
	if err != nil {
		t.Fatalf("subagent_fanout: %v", err)
	}
	report, ok := got.(string)
	if !ok {
		t.Fatalf("expected string payload, got %T", got)
	}
	return report
}

// fanoutTasksJSON builds fanout args for count tasks whose prompts are
// "task-<i>" and titles "T<ii>"; the prompts carry PROMPTMARK so a test can
// prove the report never echoes them.
func fanoutTasksJSON(count int) string {
	var b strings.Builder
	b.WriteString(`{"tasks":[`)
	for i := 0; i < count; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"title":"T%d","prompt":"task-%d brief PROMPTMARK-%d","subagent_type":"general-purpose"}`, i, i, i)
	}
	b.WriteString(`],"max_parallel":6}`)
	return b.String()
}

// extractReportPath pulls the one spill path out of a cut report's fixed
// sentence: "the whole report is at <path> — read it with read_file".
func extractReportPath(t *testing.T, report string) string {
	t.Helper()
	const lead = "the whole report is at "
	start := strings.Index(report, lead)
	if start < 0 {
		t.Fatalf("no report path in:\n%s", truncatePreviewText(report, 400))
	}
	rest := report[start+len(lead):]
	end := strings.Index(rest, " — read it with read_file")
	if end < 0 {
		t.Fatalf("report path sentence malformed: %q", truncatePreviewText(rest, 200))
	}
	return rest[:end]
}

// inlineReportBody returns the report text a section shows before its cut
// sentence — the part the inline budget bounds. The summary line and the
// section header are framing, not report, so neither counts against it.
func inlineReportBody(report string) string {
	if idx := strings.Index(report, "\n[report cut from "); idx >= 0 {
		report = report[:idx]
	}
	if idx := strings.Index(report, "\n## ["); idx >= 0 {
		rest := report[idx+1:]
		if nl := strings.Index(rest, "\n"); nl >= 0 {
			return rest[nl+1:]
		}
		return ""
	}
	if idx := strings.Index(report, "\n"); idx >= 0 {
		return report[idx+1:]
	}
	return report
}

// Six 10KB reports: every section header, every report's first and last
// line reach the dispatching agent, no prompt is echoed, and the whole
// report stays inside the fanout budget.
func TestFanoutReportDeliversEveryReport(t *testing.T) {
	exec := &perTaskReportExecutor{
		fixedSubagentExecutor: &fixedSubagentExecutor{},
		report:                func(task string) string { name, _, _ := strings.Cut(task, " "); return sizedReport(name, 100) },
	}
	report := fanoutReportFor(t, exec, fanoutTasksJSON(6))
	for i := 0; i < 6; i++ {
		task := fmt.Sprintf("task-%d", i)
		for _, want := range []string{
			fmt.Sprintf("## [%d/6] T%d · ok", i+1, i),
			"BEGIN " + task,
			"END " + task,
		} {
			if !strings.Contains(report, want) {
				t.Fatalf("report missing %q:\n%s", want, truncatePreviewText(report, 400))
			}
		}
	}
	if strings.Contains(report, "PROMPTMARK") {
		t.Fatal("the report echoed a task prompt")
	}
	if n := utf8.RuneCountInString(report); n > subagentFanoutInlineChars {
		t.Fatalf("report is %d characters, over the fanout budget", n)
	}
	if strings.Contains(report, "[report cut from ") {
		t.Fatal("6×10KB needs no cut at all")
	}
}

// One 60KB report is cut exactly once: an inline head within the single
// report budget, one cut sentence, one path — and the file behind that path
// is the report byte for byte, in many lines.
func TestFanoutReportTruncatesOnce(t *testing.T) {
	const task = "big-report"
	original := sizedReport(task, 600) // ~60,600 characters
	exec := &perTaskReportExecutor{
		fixedSubagentExecutor: &fixedSubagentExecutor{},
		report:                func(prompt string) string { return original },
	}
	report := fanoutReportFor(t, exec, `{"tasks":[{"title":"Big","prompt":"`+task+`","subagent_type":"general-purpose"}],"max_parallel":1}`)
	if !strings.Contains(report, "## [1/1] Big · ok") {
		t.Fatalf("missing section header:\n%s", truncatePreviewText(report, 400))
	}
	if got := strings.Count(report, "[report cut from "); got != 1 {
		t.Fatalf("report cut sentences = %d, want exactly 1", got)
	}
	if got := strings.Count(report, "the whole report is at "); got != 1 {
		t.Fatalf("report paths = %d, want exactly 1", got)
	}
	if n := utf8.RuneCountInString(inlineReportBody(report)); n > subagentReportInlineChars {
		t.Fatalf("inline body is %d characters, over the single report budget", n)
	}
	if strings.Contains(report, "END "+task) {
		t.Fatal("the cut still shows the report's last line inline")
	}
	path := extractReportPath(t, report)
	spilled, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the spilled report: %v", err)
	}
	if string(spilled) != original {
		t.Fatalf("spilled file differs from the original report (%d vs %d bytes)", len(spilled), len(original))
	}
	if !strings.Contains(string(spilled), "\n") {
		t.Fatal("the spilled report is a single line; read_file cannot page it")
	}
}

// Six 40KB reports together exceed the fanout budget with none over the
// single-report budget: the two largest shrink to previews with paths, the
// other four ride along whole, and every section header stays.
func TestFanoutReportShrinksLargestFirst(t *testing.T) {
	exec := &perTaskReportExecutor{
		fixedSubagentExecutor: &fixedSubagentExecutor{},
		report:                func(task string) string { name, _, _ := strings.Cut(task, " "); return sizedReport(name, 400) }, // ~40,400 chars each
	}
	report := fanoutReportFor(t, exec, fanoutTasksJSON(6))
	if got := strings.Count(report, "[report cut from "); got != 2 {
		t.Fatalf("report cut sentences = %d, want exactly 2 (the downgraded pair)", got)
	}
	if got := strings.Count(report, "the whole report is at "); got != 2 {
		t.Fatalf("report paths = %d, want exactly 2", got)
	}
	for i := 0; i < 6; i++ {
		if !strings.Contains(report, fmt.Sprintf("## [%d/6] T%d · ok", i+1, i)) {
			t.Fatalf("missing header of task %d:\n%s", i, truncatePreviewText(report, 400))
		}
	}
	// Equal sizes downgrade in index order: tasks 0 and 1 are the previews.
	for _, i := range []int{0, 1} {
		if !strings.Contains(report, fmt.Sprintf("BEGIN task-%d", i)) {
			t.Fatalf("preview of task %d lost its head", i)
		}
		if strings.Contains(report, fmt.Sprintf("END task-%d", i)) {
			t.Fatalf("task %d should be a preview, not the whole report", i)
		}
	}
	for _, i := range []int{2, 3, 4, 5} {
		if !strings.Contains(report, fmt.Sprintf("END task-%d", i)) {
			t.Fatalf("task %d should ride along whole", i)
		}
	}
	if n := utf8.RuneCountInString(report); n > subagentFanoutInlineChars+10_000 {
		t.Fatalf("report is %d characters; the downgrade did not bring it back inside the budget", n)
	}
}

// The path a cut report hands the model is a file read_file can page through:
// many lines, and the report's end reachable by offset.
func TestFanoutReportPathIsReadableInPages(t *testing.T) {
	const task = "paged-report"
	original := sizedReport(task, 600)
	home := t.TempDir()
	toolsState := toolpkg.NewState(home)
	exec := &perTaskReportExecutor{
		fixedSubagentExecutor: &fixedSubagentExecutor{},
		report:                func(string) string { return original },
	}
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterSubagentTool(a, Factory{
		Home:  home,
		Owner: &Runner{Deps: &Deps{AppCfg: subagentsEnabledConfig()}, SubagentExecutor: exec},
		Tools: toolsState,
	}); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	fanTool, _ := aToolByName(a, "subagent_fanout")
	got, err := fanTool.Handle(context.Background(), `{"tasks":[{"title":"Paged","prompt":"`+task+`","subagent_type":"general-purpose"}],"max_parallel":1}`)
	if err != nil {
		t.Fatalf("subagent_fanout: %v", err)
	}
	path := extractReportPath(t, got.(string))
	spilled, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the spilled report: %v", err)
	}
	if lines := strings.Count(string(spilled), "\n"); lines <= 1 {
		t.Fatalf("spilled report has %d lines; paging needs many", lines)
	}
	readTool, err := toolpkg.NewFileReadTool(toolsState)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}
	page, err := readTool.Handle(context.Background(), fmt.Sprintf(`{"file_path":%q,"offset":590,"limit":20}`, path))
	if err != nil {
		t.Fatalf("read_file on the spilled report: %v", err)
	}
	text := ""
	switch v := page.(type) {
	case string:
		text = v
	default:
		b, _ := json.Marshal(v)
		text = string(b)
	}
	if !strings.Contains(text, "END "+task) {
		t.Fatalf("the report's end is not reachable by paging; page tail: %q", truncatePreviewText(text, 200))
	}
}

// A continuation's own answer survives the merge even when the earlier report
// filled the record: the merge reserves room for the new text. The ceiling
// counts characters — the preview cut inside counts bytes, and its ellipsis
// rides on top, exactly as it always has.
func TestContinuationKeepsNewResult(t *testing.T) {
	got := mergeContinuationOutput(strings.Repeat("旧", 12000), "新结论")
	if !strings.Contains(got, "新结论") {
		t.Fatalf("the continuation's own answer was dropped: %q…", truncatePreviewText(got, 200))
	}
	if n := utf8.RuneCountInString(got); n > 24000 {
		t.Fatalf("merged record is %d characters, over the ceiling", n)
	}
}

// subagentRunReportFor drives one subagent_run call and returns the response
// map the model receives.
func subagentRunReportFor(t *testing.T, output string) map[string]any {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runSvc := &state.RunStore{DB: db}
	ensureRunSessions(t, db, "session-1")
	parent, err := runSvc.CreateRun(ctx, "session-1", "parent")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterSubagentTool(a, Factory{
		Home:  home,
		Owner: &Runner{Deps: &Deps{RunRT: runSvc, AppCfg: subagentsEnabledConfig()}, SubagentExecutor: &fixedSubagentExecutor{output: output}},
		Tools: toolpkg.NewState(home),
	}); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	runTool, ok := aToolByName(a, "subagent_run")
	if !ok {
		t.Fatal("subagent_run not registered")
	}
	got, err := runTool.Handle(toolpkg.WithRunID(llm.WithAgentSessionID(ctx, "session-1"), parent.ID), `{"task":"inspect","subagent_type":"general-purpose"}`)
	if err != nil {
		t.Fatalf("subagent_run: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(got.(string)), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return payload
}

// A 60KB subagent_run report is cut once, here: the output field carries a
// head within the budget plus the one sentence and the one path, the file
// behind the path is the report byte for byte, and no governor touched it.
func TestSubagentRunReportHasNoDoubleSpill(t *testing.T) {
	original := sizedReport("run-report", 600)
	payload := subagentRunReportFor(t, original)
	output, _ := payload["output"].(string)
	if got := strings.Count(output, "[report cut from "); got != 1 {
		t.Fatalf("report cut sentences = %d, want exactly 1", got)
	}
	if got := strings.Count(output, "the whole report is at "); got != 1 {
		t.Fatalf("report paths = %d, want exactly 1", got)
	}
	body := output
	if idx := strings.Index(output, "\n[report cut from "); idx >= 0 {
		body = output[:idx]
	}
	if n := utf8.RuneCountInString(body); n > subagentReportInlineChars {
		t.Fatalf("output body is %d characters, over the single report budget", n)
	}
	if strings.Contains(output, "[tool output truncated for context:") || strings.Contains(output, "read_file:") {
		t.Fatalf("the report was governed a second time: %q", truncatePreviewText(output, 400))
	}
	path := extractReportPath(t, output)
	spilled, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the spilled report: %v", err)
	}
	if string(spilled) != original {
		t.Fatalf("spilled file differs from the original report (%d vs %d bytes)", len(spilled), len(original))
	}
}

// A short subagent_run report is delivered untouched: same bytes, no cut
// sentence, no path.
func TestSubagentRunShortReportIsUntouched(t *testing.T) {
	original := sizedReport("short-report", 20)
	payload := subagentRunReportFor(t, original)
	output, _ := payload["output"].(string)
	if output != original {
		t.Fatalf("short report was touched:\ngot  %q\nwant %q", truncatePreviewText(output, 200), truncatePreviewText(original, 200))
	}
	if strings.Contains(output, "[report cut from ") {
		t.Fatal("a short report must not carry a cut sentence")
	}
}

// The card facts ride the step event's sidecar; the model-visible report
// never carries a copy of them. The engine assembles the step event's Output
// from what the tool captured — this test mirrors that assembly.
func TestFanoutCardFactsRideTheSidecarNotTheReport(t *testing.T) {
	exec := &perTaskReportExecutor{
		fixedSubagentExecutor: &fixedSubagentExecutor{},
		report:                func(task string) string { name, _, _ := strings.Cut(task, " "); return sizedReport(name, 10) },
	}
	home := t.TempDir()
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterSubagentTool(a, Factory{
		Home:  home,
		Owner: &Runner{Deps: &Deps{AppCfg: subagentsEnabledConfig()}, SubagentExecutor: exec},
		Tools: toolpkg.NewState(home),
	}); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	fanTool, _ := aToolByName(a, "subagent_fanout")
	ctx := toolpkg.WithToolCompletionCapture(context.Background())
	got, err := fanTool.Handle(ctx, fanoutTasksJSON(3))
	if err != nil {
		t.Fatalf("subagent_fanout: %v", err)
	}
	report := got.(string)
	captured, ok := toolpkg.ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("subagent_fanout captured no tool completion")
	}
	facts, ok := captured.Output["results"]
	if !ok {
		t.Fatal("the captured output carries no results sidecar")
	}
	if list, ok := facts.([]map[string]any); !ok || len(list) != 3 {
		t.Fatalf("sidecar results = %#v, want three per-task facts", facts)
	}
	if text, _ := captured.Output["output"].(string); text != report {
		t.Fatal("the captured output text differs from the model-visible report")
	}
	if strings.Contains(report, `"results"`) || strings.Contains(report, `"index"`) {
		t.Fatal("the model-visible report carries a copy of the card facts")
	}
}
