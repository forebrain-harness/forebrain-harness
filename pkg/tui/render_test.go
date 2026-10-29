package tui

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
)

// TestRendererMeasuresTextInOneWidthModel pins the display-width model the
// whole terminal surface is laid out and addressed in. go-runewidth reads the
// process locale when it initialises and counts every East Asian *Ambiguous*
// rune as two cells under an East Asian LANG; lipgloss (x/ansi), which measures
// the lines the renderer builds, always counts them as one and cannot be told
// otherwise. Letting the locale decide put the two one column apart on every
// row built from ◆ │ ─ ● ○ ↑ · …, which is how the picker filter came to repaint
// the character before the caret and the overlay rules came out half width.
func TestRendererMeasuresTextInOneWidthModel(t *testing.T) {
	if runewidth.DefaultCondition.EastAsianWidth {
		t.Fatal("the renderer's width model must not follow the process locale: East Asian Ambiguous runes are one cell")
	}
	// The glyphs the pickers, rules, status rows and markdown tables are drawn
	// from, plus a few ambiguous runes that arrive in model and user text.
	for _, glyph := range []rune{'◆', '│', '❯', '●', '○', '─', '↑', '↓', '·', '…', '☆', '♥', '°', '→', '“', '±'} {
		if got, want := runewidth.RuneWidth(glyph), lipgloss.Width(string(glyph)); got != want {
			t.Errorf("%q: go-runewidth says %d cells, lipgloss says %d", glyph, got, want)
		}
	}
	// Genuinely wide runes stay two cells in both models; the declaration must
	// not have flattened CJK text.
	for _, glyph := range []rune{'我', '爱', '北', '京'} {
		if got, want := runewidth.RuneWidth(glyph), lipgloss.Width(string(glyph)); got != 2 || want != 2 {
			t.Errorf("%q: go-runewidth says %d cells, lipgloss says %d, want 2 from both", glyph, got, want)
		}
	}
}

// codexUsageLimitError reproduces what reaches the terminal when a ChatGPT
// subscription's allowance is spent: an SDK error whose text is the raw HTTP
// body, carrying the normalized quota detail alongside it.
func codexUsageLimitError() error {
	body := `{"eligible_promo":null,"message":"{\"error\":{\"type\":\"usage_limit_reached\",\"message\":\"The usage limit has been reached\",\"plan_type\":\"plus\",\"resets_at\":1788543035,\"eligible_promo\":null,\"resets_in_seconds\":8899}}","plan_type":"plus","resets_at":1788543035,"resets_in_seconds":8899,"type":"usage_limit_reached"}`
	return &llm.APIError{
		StatusCode: http.StatusTooManyRequests,
		Type:       "usage_limit_reached",
		Message:    body,
		Err:        errors.New(`POST "https://chatgpt.com/backend-api/codex/responses": 429 Too Many Requests ` + body),
		RateLimit:  llm.ParseRateLimit(http.StatusTooManyRequests, body, "", time.Unix(1788543035-8899, 0)),
	}
}

// TestPrintErrorExplainsProviderRefusal guards the terminal's last error
// surface: a failed turn must read as the provider's own sentence and the HTTP
// status, never as the provider's HTTP body.
func TestPrintErrorExplainsProviderRefusal(t *testing.T) {
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	renderer.PrintError(codexUsageLimitError())

	var content string
	for _, block := range renderer.vm.blocks {
		if block.frame.Kind == FrameError {
			content = block.frame.Content
		}
	}
	if content == "" {
		t.Fatalf("no error frame was rendered: %#v", renderer.vm.blocks)
	}
	want := "The usage limit has been reached (HTTP 429 Too Many Requests)"
	if !strings.Contains(content, want) {
		t.Errorf("error frame is missing %q:\n%s", want, content)
	}
	for _, unwanted := range []string{"usage_limit_reached", "resets_in_seconds", "chatgpt.com"} {
		if strings.Contains(content, unwanted) {
			t.Errorf("error frame still shows raw transport data %q:\n%s", unwanted, content)
		}
	}
}

// A failure that is not a provider refusal keeps its own wording.
func TestPrintErrorKeepsOrdinaryErrorText(t *testing.T) {
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	renderer.PrintError(errors.New("user prompt hook failed"))

	for _, block := range renderer.vm.blocks {
		if block.frame.Kind == FrameError && block.frame.Content == "user prompt hook failed" {
			return
		}
	}
	t.Fatalf("ordinary error text was rewritten: %#v", renderer.vm.blocks)
}

// The helpers below were production functions that only the tests ever
// called: each is a thin composition of live code. They live here so the
// production files carry no unused code while the tests keep exercising
// the live functions underneath.

func renderAssistantMarkdown(raw string) string {
	return renderAssistantMarkdownWithWidth(raw, 0, DiffThemeUnknown)
}

// formatToolContent transforms raw tool output into human-friendly display text
// based on the tool type. Returns empty string when there is nothing to show.
func formatToolContent(title, content, cwd string) string {
	return formatToolContentWithInput(title, content, cwd, nil)
}

// A refusal with words draws them as the card's body: a refused call never
// ran, so its Content is empty (the shell-refusal reality) and the words
// riding the meta are the only sentence the card has. A refusal without
// words keeps the card it always had — the placeholder, and nothing new
// (plan 154).
func TestDeniedToolCardBodyCarriesTheUsersWords(t *testing.T) {
	withWords := stripANSI(strings.Join(renderFrameLines(Frame{
		Kind: FrameTool, Title: "shell", Final: true,
		ToolMeta: tool.ToolMeta{Status: "denied", DenyFeedback: "改成移到 trash/"},
	}, 100, DiffThemeDark, 0), "\n"))
	if !strings.Contains(withWords, "改成移到 trash/") {
		t.Fatalf("denied card body lost the user's words:\n%s", withWords)
	}
	if strings.Contains(withWords, "(no output)") {
		t.Fatalf("denied card with words still shows the placeholder:\n%s", withWords)
	}

	withoutWords := stripANSI(strings.Join(renderFrameLines(Frame{
		Kind: FrameTool, Title: "shell", Final: true,
		ToolMeta: tool.ToolMeta{Status: "denied"},
	}, 100, DiffThemeDark, 0), "\n"))
	if !strings.Contains(withoutWords, "(no output)") {
		t.Fatalf("denied card without words lost its placeholder:\n%s", withoutWords)
	}
}

// mcpOutputSectionText extracts the fenced block following an "output:" heading
// and unwraps a recognized MCP envelope. Unrecognized bodies remain verbatim so
// callers can show tool output that does not use MCP content blocks.
func mcpOutputSectionText(content string) string {
	raw := mcpOutputSectionBody(content)
	if raw == "" {
		return ""
	}
	return extractMCPToolOutputText(raw)
}

// renderSkillInstallLines renders one install card the way the viewport does.
func renderSkillInstallLines(f Frame) string {
	return stripANSI(strings.Join(renderFrameLines(f, 100, DiffThemeDark, 0), "\n"))
}

// A running install says what it is doing and how long it has been doing it;
// the elapsed time is what tells the user the fetch is still alive, since the
// phase itself can stand still for the whole clone.
func TestSkillInstallCardReportsPhaseAndElapsedWhileRunning(t *testing.T) {
	text := renderSkillInstallLines(Frame{
		Kind:     FrameSkillInstall,
		StepID:   "skill-install:1",
		Title:    "samber/cc-skills-golang",
		Summary:  "installing",
		Content:  "downloading the package",
		Duration: 17 * time.Second,
	})
	for _, want := range []string{"installing", "samber/cc-skills-golang", "downloading the package", "17s"} {
		if !strings.Contains(text, want) {
			t.Fatalf("running install card %q missing %q", text, want)
		}
	}
}

// The finished card replaces the running one in place, so it has to carry the
// whole result: what landed and how to reach it.
func TestSkillInstallCardReportsResultWhenFinished(t *testing.T) {
	text := renderSkillInstallLines(Frame{
		Kind:    FrameSkillInstall,
		StepID:  "skill-install:1",
		Title:   "samber/cc-skills-golang",
		Summary: "installed 2 skills",
		Content: "names: golang-cli, golang-testing\nnote: applies to a new session",
		Final:   true,
	})
	for _, want := range []string{"installed 2 skills", "names: golang-cli, golang-testing", "applies to a new session"} {
		if !strings.Contains(text, want) {
			t.Fatalf("finished install card %q missing %q", text, want)
		}
	}
}

// A failed install still closes its card: the running block is the same block,
// and an unfinished one would animate forever.
func TestSkillInstallCardReportsFailure(t *testing.T) {
	text := renderSkillInstallLines(Frame{
		Kind:    FrameSkillInstall,
		StepID:  "skill-install:1",
		Title:   "samber/cc-skills-golang",
		Summary: skillInstallFailedSummary,
		Content: skill.ErrGitMissing.Error(),
		Final:   true,
	})
	if !strings.Contains(text, skillInstallFailedSummary) || !strings.Contains(text, skill.ErrGitMissing.Error()) {
		t.Fatalf("failed install card %q does not report the failure", text)
	}
}

// Every checkpoint of one install owns the same block: the transcript must end
// up with a single card per install, not one line per checkpoint.
func TestSkillInstallCheckpointsReplaceOneBlock(t *testing.T) {
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	for _, phase := range []string{"preparing", "downloading the package", "loading into the catalog"} {
		renderer.RenderFrame(Frame{
			Kind:    FrameSkillInstall,
			StepID:  "skill-install:1",
			Title:   "openai/skills",
			Summary: "installing",
			Content: phase,
		})
	}
	renderer.RenderFrame(Frame{
		Kind:    FrameSkillInstall,
		StepID:  "skill-install:1",
		Title:   "openai/skills",
		Summary: "installed pptx",
		Final:   true,
	})
	blocks := 0
	var last Frame
	for _, block := range renderer.vm.blocks {
		if block.frame.Kind == FrameSkillInstall {
			blocks++
			last = block.frame
		}
	}
	if blocks != 1 {
		t.Fatalf("expected one install card, got %d", blocks)
	}
	if !last.Final || last.Summary != "installed pptx" {
		t.Fatalf("card did not settle on the install result: %+v", last)
	}
}

// An install fails for reasons only the underlying tool can name, so the card
// shows git's message exactly as it arrived — no headline, no advice, nothing
// dropped.
func TestSkillInstallCardShowsTheOriginalError(t *testing.T) {
	raw := "git [clone --depth 1 https://example.invalid/x.git repo]: fatal: unable to access: LibreSSL SSL_connect: SSL_ERROR_SYSCALL"
	text := renderSkillInstallLines(Frame{
		Kind:    FrameSkillInstall,
		StepID:  "skill-install:1",
		Title:   "example/x",
		Summary: skillInstallFailedSummary,
		Content: raw,
		Final:   true,
	})
	for _, want := range []string{"SSL_ERROR_SYSCALL", "clone --depth 1", "unable to access"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the card dropped part of the original error %q: %q", want, text)
		}
	}
}

// TestComposerStatusLineIsOneControlFreeRow pins the property the composer's
// live status row lost once a model-written plan label reached it: a tab or a
// newline in the text must not survive into the painted row, and the row must
// stay one column short of the terminal width so it can never spill onto the
// spacer row below it.
func TestComposerStatusLineIsOneControlFreeRow(t *testing.T) {
	r := &Renderer{}
	const width = 60
	out := r.renderComposerStatusLine("Tasks 0/5 · implement\tregistry\nfreeze · 177 tools", width)
	if hasCursorMovingByte(out) {
		t.Fatalf("status row carries a cursor-moving byte: %q", out)
	}
	if w := paintedRowDisplayWidth(out); w > width-1 {
		t.Fatalf("status row paints %d columns, budget is %d: %q", w, width-1, out)
	}
	if !strings.Contains(out, "implement registry freeze") {
		t.Fatalf("the fold must keep the label's words: %q", out)
	}
}

// TestWorkedStatusLineTruncatesAnOverWideLabel guards the other producer that
// used to return over-wide text verbatim: a label longer than the row is cut
// at the row budget instead of spilling onto the row below.
func TestWorkedStatusLineTruncatesAnOverWideLabel(t *testing.T) {
	const width = 20
	got := workedStatusLine("a very long working label that overflows", width)
	if hasCursorMovingByte(got) {
		t.Fatalf("worked row carries a cursor-moving byte: %q", got)
	}
	if w := paintedRowDisplayWidth(got); w > width {
		t.Fatalf("worked row paints %d columns on a %d-column row: %q", w, width, got)
	}
	if !strings.HasPrefix(got, "─ ") {
		t.Fatalf("a truncated worked row must keep its rule prefix: %q", got)
	}
	// The normal branch still draws the rule out to the full row width.
	full := workedStatusLine("short", 30)
	if w := paintedRowDisplayWidth(full); w != 30 {
		t.Fatalf("a short label's worked row must fill the row, got %d columns: %q", w, full)
	}
}

// selfContainedRows must hand back a table whose every row opens the style it
// is drawn in and ends in the default state, without touching the caller's
// slice, the row count, or the visible text.
func TestSelfContainedRowsOpensCarriedStylesOnEachRow(t *testing.T) {
	rows := []string{
		"\x1b[1;38;5;214mlong coloured header that wraps onto",
		"a second row\x1b[0m and plain text again",
	}
	got := selfContainedRows(rows)
	if len(got) != len(rows) {
		t.Fatalf("row count changed: %d -> %d", len(rows), len(got))
	}
	if want := "\x1b[1;38;5;214mlong coloured header that wraps onto\x1b[0m"; got[0] != want {
		t.Fatalf("row 0 = %q, want %q", got[0], want)
	}
	// The carry sits at the very start of the row: continuation prefixes were
	// drawn inside the open style when the transcript painted sequentially.
	if want := "\x1b[1;38;5;214ma second row\x1b[0m and plain text again"; got[1] != want {
		t.Fatalf("row 1 = %q, want %q", got[1], want)
	}
	// The caller's slice is untouched.
	if rows[0] != "\x1b[1;38;5;214mlong coloured header that wraps onto" {
		t.Fatalf("input row 0 was modified: %q", rows[0])
	}
	// Stacked openers replay in order; the visible text is byte-identical.
	for i := range rows {
		if stripANSI(got[i]) != stripANSI(rows[i]) {
			t.Fatalf("row %d visible text changed: %q -> %q", i, stripANSI(rows[i]), stripANSI(got[i]))
		}
		if sgrStateAfter(got[i]) != "" {
			t.Fatalf("row %d leaves SGR %q in effect", i, sgrStateAfter(got[i]))
		}
	}
}

func TestSelfContainedRowsLeavesCleanTablesAlone(t *testing.T) {
	rows := []string{
		"\x1b[38;5;214mred\x1b[0m plain tail",
		"second row, no ANSI",
		"",
		"\x1b[2m dimmed \x1b[0m",
	}
	if got := selfContainedRows(rows); &got[0] != &rows[0] {
		t.Fatalf("an already self-contained table must be returned unchanged, got %q", got)
	}
}

func TestSelfContainedRowsStopsCarryingAfterAnInRowReset(t *testing.T) {
	rows := []string{
		"\x1b[38;5;214mred\x1b[0m plain",
		"next row opens fresh",
	}
	got := selfContainedRows(rows)
	if got[1] != rows[1] {
		t.Fatalf("a row after an in-row reset must not reopen the old style: %q", got[1])
	}
}

func TestSelfContainedRowsDecoratesNoBlankRows(t *testing.T) {
	rows := []string{
		"\x1b[38;5;214ma wrapped colour",
		"",
		"   ",
		"tail\x1b[0m",
	}
	got := selfContainedRows(rows)
	if got[1] != "" || got[2] != "   " {
		t.Fatalf("blank rows must not carry sequences: %q / %q", got[1], got[2])
	}
	if want := "\x1b[38;5;214mtail\x1b[0m"; got[3] != want {
		t.Fatalf("row 3 = %q, want %q", got[3], want)
	}
}

func TestSelfContainedRowsIsIdempotentAndDedupsRepeats(t *testing.T) {
	rows := []string{
		"\x1b[38;5;214mA\x1b[38;5;214m B",
		"C\x1b[0m",
	}
	got := selfContainedRows(rows)
	// The repeated opener is applied once on replay, like the terminal would.
	if want := "\x1b[38;5;214mC\x1b[0m"; got[1] != want {
		t.Fatalf("row 1 = %q, want %q", got[1], want)
	}
	again := selfContainedRows(got)
	if !slices.Equal(again, got) {
		t.Fatalf("normalising self-contained rows must be idempotent: %q -> %q", got, again)
	}
}

// A page answered before the TUI starts hands the terminal over the moment it
// has an answer, so it must not read past it: a user who answers and starts
// typing in one burst would otherwise lose what they typed to the page.
func TestStandaloneReviewLeavesTypeAheadForTheTUI(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := w.Write([]byte("\x1b[B\rhello")); err != nil {
		t.Fatal(err)
	}
	w.Close()

	sel := newStandaloneRawSelector(r, io.Discard)
	idx, ok, err := sel.Review("Apply this choice?", nil, []string{"Quit", "Apply"}, 0)
	if err != nil || !ok || idx != 1 {
		t.Fatalf("Review = (%d, %v, %v), want the second action", idx, ok, err)
	}
	rest, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read the rest: %v", err)
	}
	if string(rest) != "hello" {
		t.Fatalf("the page consumed the type-ahead: %q left, want %q", rest, "hello")
	}
}

func TestToolOutputBlockKeepsOneForegroundDespiteCommandColours(t *testing.T) {
	out := formatToolOutputBlock("plain \x1b[0mafter reset\n\x1b[97mbright white\x1b[39m tail")
	for _, seq := range []string{"\x1b[0mafter", "\x1b[97m", "\x1b[39m"} {
		if strings.Contains(out, seq) {
			t.Fatalf("command colour %q survived into the output block: %q", seq, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, thinkingColor) {
			t.Fatalf("row not painted in the output colour: %q", line)
		}
	}
}

// TestToolOutputBlockKeepsTheNoOutputPlaceholderFaint pins that stripping the
// command's colours does not also strip the renderer's own styling: the
// "(no output)" placeholder its callers render faint stays faint.
func TestToolOutputBlockKeepsTheNoOutputPlaceholderFaint(t *testing.T) {
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(previous) })

	placeholder := lipgloss.NewStyle().Faint(true).Render(toolNoOutputText)
	if !strings.Contains(placeholder, "\x1b[2m") {
		t.Fatalf("test setup: faint placeholder carries no faint code: %q", placeholder)
	}
	out := formatToolOutputBlock(placeholder)
	if !strings.Contains(out, placeholder) {
		t.Fatalf("placeholder lost its faint style: %q", out)
	}
	if !strings.HasPrefix(out, thinkingColor) || !strings.Contains(stripANSI(out), "└ "+toolNoOutputText) {
		t.Fatalf("placeholder not drawn as an output row: %q", out)
	}
	// Command output that merely contains the words keeps the one-colour rule.
	cmd := formatToolOutputBlock("\x1b[31mfailed\x1b[0m (no output)")
	if strings.Contains(cmd, "\x1b[31m") || strings.Contains(cmd, "\x1b[2m") {
		t.Fatalf("command output kept or gained styling: %q", cmd)
	}
}

// A user_interaction card's header says only that the user was asked: what it
// asked is the body's business, so the target stays empty however long the
// questions are.
func TestUserInteractionCardHeaderCarriesOnlyTheVerb(t *testing.T) {
	meta := tool.ToolMeta{Status: "completed", Input: map[string]any{"questions": []any{
		map[string]any{"header": "Triggers"},
		map[string]any{"question": "What should the greeting look like when the user says hi first thing in the morning?"},
	}}}
	action, target, _ := toolDisplayParts(Frame{Kind: FrameTool, Title: "user_interaction", ToolMeta: meta, Final: true}, "", "")
	if action != "Asked user" {
		t.Fatalf("action = %q", action)
	}
	if target != "" {
		t.Fatalf("target = %q, want the questions kept out of the header", target)
	}
}

// The lsp card's title names the operation and what it was pointed at: the
// symbol when one was given, the query for workspace searches, otherwise the
// file (shortened like a read_file path) with its line.
func TestToolDisplayPartsLSP(t *testing.T) {
	input := func(extra map[string]any) tool.ToolMeta {
		in := map[string]any{"operation": "definition"}
		for k, v := range extra {
			in[k] = v
		}
		return tool.ToolMeta{Input: in}
	}

	running, target, _ := toolDisplayParts(Frame{Kind: FrameTool, Title: "lsp", ToolMeta: tool.ToolMeta{Status: "running", Input: map[string]any{"operation": "definition", "symbol": "Query"}}}, "", "")
	if running != "Looking up" || target != "definition · Query" {
		t.Fatalf("running = (%q, %q), want Looking up / definition · Query", running, target)
	}

	completed, target, _ := toolDisplayParts(Frame{Kind: FrameTool, Title: "lsp", Final: true, ToolMeta: input(map[string]any{"query": "auth.Loop"})}, "", "")
	if completed != "Looked up" || target != "definition · auth.Loop" {
		t.Fatalf("query target = (%q, %q), want Looked up / definition · auth.Loop", completed, target)
	}

	failed, _, _ := toolDisplayParts(Frame{Kind: FrameTool, Title: "lsp", ToolMeta: tool.ToolMeta{Status: "failed", Input: map[string]any{"operation": "hover", "file_path": "/repo/main.go", "line": 12}}}, "", "")
	if failed != "Failed to look up" {
		t.Fatalf("failed action = %q, want Failed to look up", failed)
	}

	cwd := t.TempDir()
	p := filepath.Join(cwd, "main.go")
	_, target, _ = toolDisplayParts(Frame{Kind: FrameTool, Title: "lsp", Final: true, ToolMeta: tool.ToolMeta{Input: map[string]any{"operation": "hover", "file_path": p, "line": 12}}}, "", cwd)
	if target != "hover · main.go:12" {
		t.Fatalf("file target = %q, want hover · main.go:12 (shortened like read_file)", target)
	}

	if phrase := failedActionPhrase("lsp"); phrase != "look up" {
		t.Fatalf("failedActionPhrase(lsp) = %q, want look up", phrase)
	}
}

// An edit card carries its diagnostics section after the turn diff: the card
// then shows one summary line under the diff and the section's problem lines
// indented like diff rows. Long cards fold through the existing foldBlock, so
// nothing here is cut to a row budget on purpose.
func TestTurnDiffCardShowsDiagnostics(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	withDiag := sampleTurnDiff + "\n\n" +
		"lsp diagnostics: 2 new in 1 file\n\n" +
		"```text\n" +
		"internal/foo/bar.go\n" +
		"  error 12:6 missing return [gopls syntax]\n" +
		"  warning 30:1 unused variable\n" +
		"```"
	block, ok := renderTurnDiffCard(withDiag, DiffThemeDark)
	if !ok {
		t.Fatal("expected render to succeed")
	}
	plain := stripANSI(block)
	if !strings.Contains(plain, "  └ Found 2 new diagnostic issues in 1 file") {
		t.Fatalf("diagnostics summary line missing:\n%s", plain)
	}
	for _, want := range []string{
		"    internal/foo/bar.go",
		"      error 12:6 missing return [gopls syntax]",
		"      warning 30:1 unused variable",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("problem row %q missing:\n%s", want, plain)
		}
	}
	// The diff's own summary stays first.
	if !strings.HasPrefix(block, "  └ Added 2 lines, removed 1 line") {
		t.Fatalf("turn-diff summary must stay the card's first line:\n%s", plain)
	}

	// Singular counts keep their singular nouns.
	singular := sampleTurnDiff + "\n\n" +
		"lsp diagnostics: 1 new in 1 file\n\n" +
		"```text\n" +
		"internal/foo/bar.go\n" +
		"  error 12:6 missing return [gopls syntax]\n" +
		"```"
	block, ok = renderTurnDiffCard(singular, DiffThemeDark)
	if !ok {
		t.Fatal("expected singular render to succeed")
	}
	if plain := stripANSI(block); !strings.Contains(plain, "  └ Found 1 new diagnostic issue in 1 file") {
		t.Fatalf("singular summary line missing:\n%s", plain)
	}

	// A pending-only section names itself instead of a count.
	pending := sampleTurnDiff + "\n\n" +
		"lsp diagnostics: pending\n\n" +
		"```text\n" +
		"diagnostics for internal/foo/bar.go are still being computed and will follow\n" +
		"```"
	block, ok = renderTurnDiffCard(pending, DiffThemeDark)
	if !ok {
		t.Fatal("expected pending render to succeed")
	}
	if plain := stripANSI(block); !strings.Contains(plain, "  └ Diagnostics pending") {
		t.Fatalf("pending summary line missing:\n%s", plain)
	}

	// Without a section the card keeps its diff-only shape.
	block, ok = renderTurnDiffCard(sampleTurnDiff, DiffThemeDark)
	if !ok {
		t.Fatal("expected diff-only render to succeed")
	}
	if plain := stripANSI(block); strings.Contains(plain, "diagnostic") {
		t.Fatalf("no diagnostics section, no diagnostics rows:\n%s", plain)
	}
}

// An edit whose diff quotes a code fence within the three context lines around
// the change made the writer escalate the turn-diff fence past three backticks
// (fenceFor). The card must still be a card: the body is read back with the
// writer's own parser, so the fence's length needs no guesswork — matching a
// hardcoded ``` opened nothing and dropped the whole body into the generic
// renderer as "turn diff: …" followed by a bare diff.
func TestRenderTurnDiffCard_RendersAnEscalatedFence(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	evt := tool.StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "edit_file",
		Input:    map[string]any{"file_path": "/w/docs/plan.md"},
		Output: map[string]any{"turn_diff": event.Summary{
			Path: "/w/docs/plan.md", Added: 1, Deleted: 1,
			UnifiedDiff: "@@ -2,7 +2,7 @@\n # Plan\n \n ```bash\n-make build\n+make build test\n ```\n tail",
		}},
	}
	body, _ := tool.FormatToolStepResult(evt, tool.DefaultMaxFormattedBody)
	if !strings.Contains(body, "````diff") {
		t.Fatalf("the formatter did not escalate the fence:\n%s", body)
	}
	block, ok := renderTurnDiffCard(body, DiffThemeDark)
	if !ok {
		t.Fatalf("an escalated-fence turn diff must render as a diff card:\n%s", body)
	}
	plain := stripANSI(block)
	for _, want := range []string{"```bash", "+ make build test", "  tail"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("diff row %q missing from the card:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "turn diff:") {
		t.Fatalf("the card fell back to the raw body:\n%s", plain)
	}
}

// An edit whose diff quotes this card's own diagnostics format must not be read
// as diagnostics. A unified diff's context lines are " " + the file's text, and
// the old reader trimmed them before matching the header — so a file that
// documents the section (a note, a bug report, the card format itself) invented
// a count the tool never reported, and a ```text block further down supplied
// the "problem lines". The section is read from tool.FileDiff.Rest, the body
// after the turn diff, which is what the writer fills.
func TestRenderTurnDiffCardIgnoresDiagnosticsTextInsideTheDiff(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	// The hunk's context holds both shapes the reader looks for.
	turnDiff := event.Summary{
		Path:    "/w/notes.md",
		Added:   1,
		Deleted: 1,
		UnifiedDiff: "@@ -1,6 +1,6 @@\n" +
			" # Card format\n" +
			" lsp diagnostics: 2 new in 1 file\n" +
			" \n" +
			" ```text\n" +
			"-old note\n" +
			"+new note\n" +
			" ```\n",
	}
	body, _ := tool.FormatToolStepResult(tool.StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "edit_file",
		Input:    map[string]any{"file_path": "/w/notes.md"},
		Output:   map[string]any{"turn_diff": turnDiff},
	}, tool.DefaultMaxFormattedBody)
	block, ok := renderTurnDiffCard(body, DiffThemeDark)
	if !ok {
		t.Fatalf("an escalated-fence turn diff must render as a diff card:\n%s", body)
	}
	plain := stripANSI(block)
	if strings.Contains(plain, "Found 2 new diagnostic issues") {
		t.Fatalf("the diff's own context lines were read as a diagnostics section:\n%s", plain)
	}
	// The diff's summary is the card's only └ row without a section.
	if got := strings.Count(plain, "  └ "); got != 1 {
		t.Fatalf("card has %d └ rows, want just the turn-diff summary:\n%s", got, plain)
	}

	// A real diagnostics section still renders, and the quoted text cannot
	// outrank it: the reader stops at the turn diff's own end.
	body, _ = tool.FormatToolStepResult(tool.StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "edit_file",
		Input:    map[string]any{"file_path": "/w/notes.md"},
		Output: map[string]any{
			"turn_diff": turnDiff,
			"lsp_diagnostics": event.LSPDiagnosticsSummary{
				New:   1,
				Files: 1,
				Items: []event.LSPDiagnostic{{
					Path: "/w/notes.md", Line: 3, Column: 1,
					Severity: "error", Message: "missing return",
				}},
			},
		},
	}, tool.DefaultMaxFormattedBody)
	block, ok = renderTurnDiffCard(body, DiffThemeDark)
	if !ok {
		t.Fatalf("an escalated-fence turn diff must render as a diff card:\n%s", body)
	}
	plain = stripANSI(block)
	for _, want := range []string{
		"  └ Found 1 new diagnostic issue in 1 file",
		"      error 3:1 missing return",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("real diagnostics row %q missing:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "Found 2 new diagnostic issues") {
		t.Fatalf("the quoted header outranked the tool's own report:\n%s", plain)
	}
}

// The normalizer's "inside a fence" test follows the renderer's character and
// length rules, so a fence the body quotes and a ~~~ fence both keep their
// state. The prefix test it replaced flipped on any "```" line: the quoted
// fragment started a new "fence", and the code after the block kept an indent
// the prose no longer had.
//
// The third case is the anchor for the rule this tracker deliberately does not
// take from the renderer: indentation is not part of it, because a fence nested
// in a list item sits past four spaces and is still a fence.
func TestNormalizeAssistantTextIgnoresFencesInsideABlock(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "backticks quoted inside a longer fence",
			raw: "Here is the doc:\n\n" +
				"    ````markdown\n" +
				"    # Title\n" +
				"    ```bash\n" +
				"    make build\n" +
				"    ```\n" +
				"    done\n" +
				"    ````",
			want: "Here is the doc:\n\n" +
				"````markdown\n" +
				"    # Title\n" +
				"    ```bash\n" +
				"    make build\n" +
				"    ```\n" +
				"    done\n" +
				"````",
		},
		{
			name: "tilde fence",
			raw: "Here is the doc:\n\n" +
				"    ~~~\n" +
				"    code\n" +
				"    ~~~\n" +
				"    done",
			want: "Here is the doc:\n\n" +
				"~~~\n" +
				"    code\n" +
				"~~~\n" +
				"done",
		},
		{
			name: "fence indented past four spaces",
			raw:  "    before\n\n    ```go\n        fmt.Println(\"x\")\n    ```\n\n    after",
			want: "before\n\n```go\n        fmt.Println(\"x\")\n```\n\nafter",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeAssistantText(tc.raw); got != tc.want {
				t.Fatalf("unexpected normalized text:\nwant=%q\ngot =%q", tc.want, got)
			}
		})
	}
}

// Bodies that are not a turn diff stay out of the card: a shell card's stdout,
// a header whose diff did not fit, and a body that opens with a fence hold no
// turn diff for the card to read.
func TestRenderTurnDiffCard_RejectsNonTurnDiffBody(t *testing.T) {
	for _, content := range []string{
		"stdout:\n\n```text\nhello\n```",
		"turn diff: `/x` (+1/-0)\nno fence here",
		"````diff\n+a\n````",
	} {
		if block, ok := renderTurnDiffCard(content, DiffThemeDark); ok {
			t.Fatalf("%q rendered as a turn-diff card:\n%s", content, block)
		}
	}
}

// A shell card's stdout that quotes a code fence escalates the fence around it,
// so the section reader must close on a fence of the same length. Matching any
// "```" prefix ended the stream at the quoted fence and dropped everything
// after it; the stderr section that follows must still be found.
func TestExtractFencedSectionKeepsStdoutWhenFenceEscalates(t *testing.T) {
	stdout := "```bash\nmake build\n```\ntail after the fence"
	stderr := "warning: one"
	evt := tool.StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "shell",
		Output:   map[string]any{"stdout": stdout, "stderr": stderr, "exit_code": 1},
	}
	body, _ := tool.FormatToolStepResult(evt, tool.DefaultMaxFormattedBody)
	if !strings.Contains(body, "````text") {
		t.Fatalf("the formatter did not escalate the stdout fence:\n%s", body)
	}
	sections := toolOutputSections(body)
	if len(sections) != 2 {
		t.Fatalf("sections = %d, want stdout and stderr:\n%s", len(sections), body)
	}
	if sections[0] != stdout {
		t.Fatalf("stdout section = %q, want the whole stream:\n%s", sections[0], body)
	}
	if sections[1] != stderr {
		t.Fatalf("stderr section = %q, want %q:\n%s", sections[1], stderr, body)
	}
}

// A problem message wider than the terminal wraps inside the card instead of
// running past the edge, where the painter cut it: every row fits the painted
// width, the continuation rows hang under the problem, and the whole message
// survives.
func TestTurnDiffCardWrapsLongDiagnostics(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const width = 80
	message := "Type '{ 'nav.chat': string; 'nav.tasks': string; 'nav.agents': string; " +
		"'nav.memories': string; 'nav.settings': string; 'nav.aria': string; 'nav.toggle': string; } " +
		"is missing the following properties from type 'Messages': 'chat.compaction.running', 'chat.compaction.done'"
	content := sampleTurnDiff + "\n\n" +
		"lsp diagnostics: 1 new in 1 file\n\n" +
		"```text\n" +
		"frontend/src/locales/index.ts\n" +
		"  error 980:7 " + message + " [ts 2739]\n" +
		"```"
	lines, _ := renderFrameLinesWithAgents(Frame{Kind: FrameTool, Title: "edit_file", Final: true, Content: content}, width, DiffThemeDark, 0)

	problem := -1
	for i, line := range lines {
		if fitted := fitPaintRow(line, width); fitted != line {
			t.Fatalf("row %d is wider than %d columns and gets cut:\n%q\n%q", i, width, line, fitted)
		}
		if strings.HasPrefix(stripANSI(line), "      error 980:7 Type") {
			problem = i
		}
	}
	if problem < 0 {
		t.Fatalf("problem row missing:\n%s", stripANSI(strings.Join(lines, "\n")))
	}
	var joined []string
	for _, line := range lines[problem:] {
		plain := stripANSI(line)
		if line != lines[problem] && !strings.HasPrefix(plain, "      ") {
			break
		}
		joined = append(joined, strings.TrimSpace(plain))
	}
	if len(joined) < 2 {
		t.Fatalf("long message should wrap onto continuation rows:\n%s", stripANSI(strings.Join(lines, "\n")))
	}
	if got, want := strings.Join(joined, " "), "error 980:7 "+message+" [ts 2739]"; got != want {
		t.Fatalf("wrapped message lost text:\ngot  %q\nwant %q", got, want)
	}
}

// A subagent card's body is content text, and content text is never dimmed:
// the card used to paint every row Faint, which the surface's own rules forbid.
func TestSubagentCardBodyIsFullBrightness(t *testing.T) {
	// Styles only render under a color profile; the test writer is not a TTY,
	// so pin one for the duration of the assertion.
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	var buf bytes.Buffer
	r := NewRenderer(&buf, &buf)
	r.renderFanout(Frame{
		Kind:    FrameFanout,
		StepID:  "fanout-bright",
		Final:   true,
		Summary: "Ran 2 general-purpose tasks · 1 done, 1 failed",
		Content: "\x1b[32m✓\x1b[0m 任务16 migrate 导入\n  └ shell gofmt -l pkg cmd\n    … +30 tool uses\n" +
			"\x1b[31m✗\x1b[0m 任务17 扩展目录\n  上游连接中断（EOF）\n",
		FanoutLineAgents: []string{"subagent-8c5c", "subagent-8c5c", "subagent-8c5c", "subagent-37aa", "subagent-37aa"},
	})
	out := buf.String()
	if strings.Contains(out, "\x1b[2m") {
		t.Fatalf("card body is dimmed:\n%q", out)
	}
	plain := stripANSI(out)
	for _, want := range []string{"任务16 migrate 导入", "✗ 任务17 扩展目录", "… +30 tool uses"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("card lost the row %q:\n%s", want, plain)
		}
	}
}

// The reported bug, end to end: the engine formatter dropped the first line's
// leading spaces, so the shell card's first output row sat two columns left of
// every later row. This walks the real engine formatter and the real card
// renderer and requires every body row to align on the same column.
func TestToolOutputCardAlignsFirstRowWithRest(t *testing.T) {
	stdout := "  GET     /api/chat/sessions\n" +
		"  POST    /api/chat/sessions\n" +
		"  GET     /api/chat/sessions/:id\n"
	body, _ := tool.FormatToolStepResult(tool.StepEvent{
		ToolName: "shell",
		Kind:     "tool_completed",
		Input:    map[string]any{"command": `grep -E '/api/chat' "$TMPDIR/forebrain-run/gw.log"`},
		Output:   map[string]any{"stdout": stdout, "exit_code": 0},
	}, 0)
	f := Frame{
		Kind:    FrameTool,
		Title:   "shell",
		Final:   true,
		Content: body,
		Summary: "shell",
		ToolMeta: tool.ToolMeta{
			Status: "completed",
			Input:  map[string]any{"command": `grep -E '/api/chat' "$TMPDIR/forebrain-run/gw.log"`},
		},
	}
	lines := renderFrameLines(f, 120, DiffThemeDark, 0)
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = stripANSI(l)
	}

	// The card header is row 0; the first output row carries the four-column
	// gutter plus the payload's own two-space indent.
	if len(plain) < 2 || plain[1] != "  └   GET     /api/chat/sessions" {
		t.Fatalf("first output row not aligned:\n%s", strings.Join(plain, "\n"))
	}

	const marker = "/api/chat/sessions"
	cols := []int{}
	for _, l := range plain {
		// Count columns in runes: the "└" gutter glyph is three bytes.
		if i := strings.Index(l, marker); i >= 0 {
			cols = append(cols, len([]rune(l[:i])))
		}
	}
	if len(cols) != 3 {
		t.Fatalf("expected 3 route rows, got %d:\n%s", len(cols), strings.Join(plain, "\n"))
	}
	for _, c := range cols[1:] {
		if c != cols[0] {
			t.Fatalf("route rows not column-aligned: %v\n%s", cols, strings.Join(plain, "\n"))
		}
	}
	t.Logf("route columns: %v", cols)
}

// summaryToolBody is the live card-body pipeline (renderCompactFrame calls it
// when not in full-body test mode), and it used to strings.TrimSpace the whole
// body — dropping the indent of a first line that is itself payload, as the
// intermediate_tool notes and retrieve_output bodies are. renderFrameLines
// forces full-body mode and so never exercises this; assert it directly.
func TestSummaryToolBodyKeepsFirstLineIndent(t *testing.T) {
	f := Frame{Kind: FrameTool, Title: "intermediate_tool", Content: "  alpha\n  beta", Final: true}
	got := summaryToolBody(f, f.Title)
	if !strings.Contains(got, "  alpha") {
		t.Fatalf("summaryToolBody dropped the first line's indent: %q", got)
	}
}

// ---- migrated from migration_card_test.go (tests for this file's production code) ----

// migration_card_test.go pins the /migrate progress card: the compact card's
// exact bar bytes, the one-block lifecycle (preview opens it, later
// checkpoints refresh it, the report replaces it in place), the animation
// ticker that keeps the bar alive, and the replay side's StepID-less frame.

func useMigrationCardTerminal(t *testing.T) (*Renderer, *strings.Builder) {
	t.Helper()
	forcedTermWidth = 80
	forcedTermHeight = 24
	t.Cleanup(func() {
		forcedTermWidth = 0
		forcedTermHeight = 0
	})
	out := &strings.Builder{}
	r := NewRenderer(out, out)
	r.EnableViewportMode()
	t.Cleanup(r.DisableViewportMode)
	return r, out
}

// barLine picks the line that carries the progress bar out of a frame's
// rendered lines, ANSI bytes included.
func barLine(t *testing.T, lines []string) string {
	t.Helper()
	for _, line := range lines {
		if strings.Contains(stripAllANSI(line), "━") {
			return line
		}
	}
	t.Fatalf("no bar line in %q", lines)
	return ""
}

// TestMigrationCardDrawsTheCompactBarExactly pins the owner requirement that
// the migration bar is the compact bar: at the same percent, phase and width
// the two cards' bar lines are byte-identical, colors included.
func TestMigrationCardDrawsTheCompactBarExactly(t *testing.T) {
	useMigrationCardTerminal(t)
	migrationLines, _ := renderFrameLinesWithAgents(Frame{
		Kind: FrameMigration, StepID: migrationStepID,
		Title: migrationTitleMigrating, Summary: "sessions 3/12", Progress: 42,
	}, 80, DiffThemeDark, 4)
	compactLines, _ := renderFrameLinesWithAgents(Frame{
		Kind: FrameMemoryCompact, StepID: "c", Title: compactTitleRunning, Progress: 42,
	}, 80, DiffThemeDark, 4)
	if got, want := barLine(t, migrationLines), barLine(t, compactLines); got != want {
		t.Fatalf("migration bar ≠ compact bar:\nmigration: %q\ncompact:   %q", got, want)
	}
	if painted := stripAllANSI(strings.Join(migrationLines, "\n")); !strings.Contains(painted, " 42%") {
		t.Fatalf("bar lost its percentage:\n%s", painted)
	}
}

// TestMigrationCardIsOneBlockFromPreviewToReport pins the card's lifecycle:
// the running frame opens one block, further checkpoints refresh it without
// stacking, the animation ticker runs while it is live, and the final report
// replaces the same block and stops the ticker.
func TestMigrationCardIsOneBlockFromPreviewToReport(t *testing.T) {
	r, out := useMigrationCardTerminal(t)
	r.RenderFrame(Frame{
		Kind: FrameMigration, StepID: migrationStepID,
		Title: migrationTitlePreviewing, Summary: "discovering", Progress: 0,
	})
	r.RenderFrame(Frame{
		Kind: FrameMigration, StepID: migrationStepID,
		Title: migrationTitleMigrating, Summary: "sessions 5/12 · s-fix.jsonl", Progress: 41,
	})
	r.mu.Lock()
	blocks := len(r.vm.blocks)
	ticker := r.compactAnimationTicker
	r.mu.Unlock()
	painted := stripAllANSI(out.String())
	if blocks != 1 {
		t.Fatalf("migration card stacked %d blocks, want one", blocks)
	}
	if ticker == nil {
		t.Fatal("migration animation ticker was not started")
	}
	if !strings.Contains(painted, migrationTitleMigrating) || !strings.Contains(painted, " 41%") {
		t.Fatalf("running migration card not painted:\n%s", painted)
	}

	r.RenderFrame(migrationLiveReportFrame(migrationTitleDone, "Migrated from Claude Code\n  Conversations: 1 imported"))
	r.mu.Lock()
	blocks = len(r.vm.blocks)
	ticker = r.compactAnimationTicker
	r.mu.Unlock()
	painted = stripAllANSI(out.String())
	if blocks != 1 {
		t.Fatalf("final report did not replace the progress card: %d blocks", blocks)
	}
	if ticker != nil {
		t.Fatal("migration animation ticker was not stopped by the final frame")
	}
	if !strings.Contains(painted, migrationTitleDone) || !strings.Contains(painted, "1 imported") {
		t.Fatalf("final report not painted:\n%s", painted)
	}
}

// TestMigrationFailureReplacesTheProgressBarInPlace pins that a failed run's
// error text lands on the progress card's own block — the bar never dangles
// at its last percent while an error card stacks under it.
func TestMigrationFailureReplacesTheProgressBarInPlace(t *testing.T) {
	r, out := useMigrationCardTerminal(t)
	r.RenderFrame(Frame{
		Kind: FrameMigration, StepID: migrationStepID,
		Title: migrationTitleMigrating, Summary: "sessions 5/12", Progress: 41,
	})
	r.RenderFrame(Frame{
		Kind: FrameMigration, StepID: migrationStepID, Final: true,
		Title: migrationTitleFailed, Content: "state database unavailable",
	})
	r.mu.Lock()
	blocks := len(r.vm.blocks)
	r.mu.Unlock()
	painted := stripAllANSI(out.String())
	if blocks != 1 {
		t.Fatalf("failure stacked a second block: %d", blocks)
	}
	if !strings.Contains(painted, migrationTitleFailed) || !strings.Contains(painted, "state database unavailable") {
		t.Fatalf("failure card not painted:\n%s", painted)
	}
}

// TestMigrationReplayFrameCarriesNoStepID pins the replay side: the report a
// resume rebuilds is a plain system card with no StepID, so two migrations in
// one session each keep their own block — and it never collides with the
// live card's id.
func TestMigrationReplayFrameCarriesNoStepID(t *testing.T) {
	frame := migrationReportFrame("Migration complete", "the report")
	if frame.Kind != FrameSystem || frame.StepID != "" || !frame.Final {
		t.Fatalf("replay frame = %+v, want a StepID-less final system card", frame)
	}
	if frame.Title != "Migration complete" || frame.Content != "the report" {
		t.Fatalf("replay frame lost its report: %+v", frame)
	}
	if migrationReportFrame("  ", "x").Title != "Migration complete" {
		t.Fatal("replay frame must default its title")
	}
}

func TestShellDiffCardShowsOutputThenFiles(t *testing.T) {
	evt := tool.StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "shell",
		Output: map[string]any{
			"stdout":    "done\n",
			"exit_code": 0,
			"shell_edit_diff": tool.ShellEditDiff{
				Files: []tool.ShellEditDiffFile{
					{
						Path: "/repo/a.txt", DisplayPath: "a.txt", Status: "updated",
						Added: 1, Deleted: 1, UnifiedDiff: "--- a:before\n+++ a:after\n@@ -1,3 +1,3 @@\n ctx\n-old\n+new\n",
					},
					{
						Path: "/repo/new.py", DisplayPath: "new.py", Status: "created",
						Added: 1, Deleted: 0, UnifiedDiff: "--- n:before\n+++ n:after\n@@ -1 +1,2 @@\n+print()\n",
					},
				},
				MoreFiles: 1,
				Shared:    true,
			},
		},
	}
	body, trunc := tool.FormatToolStepResult(evt, tool.DefaultMaxFormattedBody)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	block, ok := renderShellDiffCard(body, DiffThemeDark)
	if !ok {
		t.Fatalf("renderShellDiffCard rejected a body with diff blocks:\n%s", body)
	}
	lines := strings.Split(stripANSI(block), "\n")
	if !strings.HasPrefix(lines[0], "  └ ") || !strings.Contains(lines[0], "done") {
		t.Fatalf("first line = %q, want the stdout under the tree prefix", lines[0])
	}
	wantInOrder := []string{
		"Updated a.txt (+1 -1)",
		"- old",
		"+ new",
		"Created new.py (+1 -0)",
		"+ print()",
		"… 1 more file changed",
		"(another command ran in this repository at the same time; a change made by either may show under either result)",
	}
	cursor := 1
	for _, want := range wantInOrder {
		found := -1
		for i := cursor; i < len(lines); i++ {
			if strings.Contains(lines[i], want) {
				found = i
				break
			}
		}
		if found < 0 {
			t.Fatalf("line %q missing after line %d:\n%s", want, cursor, block)
		}
		cursor = found + 1
	}
	for _, line := range lines[1:] {
		if !strings.HasPrefix(line, "    ") {
			t.Fatalf("row outside the tree prefix: %q", line)
		}
	}
}

func TestShellDiffCardWithoutStdoutStartsWithTheFirstFile(t *testing.T) {
	updated, err := event.Build("/repo/a.txt", []byte("a\n"), []byte("b\n"))
	if err != nil {
		t.Fatal(err)
	}
	evt := tool.StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "shell",
		Output: map[string]any{
			"apply_patch": true,
			"files": []map[string]any{
				{"abs_path": "/repo/a.txt", "display_path": "a.txt", "operation": "update", "turn_diff": updated},
			},
		},
	}
	body, _ := tool.FormatToolStepResult(evt, tool.DefaultMaxFormattedBody)
	block, ok := renderShellDiffCard(body, DiffThemeDark)
	if !ok {
		t.Fatalf("renderShellDiffCard rejected an apply_patch body:\n%s", body)
	}
	first := strings.Split(stripANSI(block), "\n")[0]
	if !strings.HasPrefix(first, "  └ Updated a.txt") {
		t.Fatalf("first line = %q, want the first file header under the tree prefix", first)
	}
}

func TestShellCardWithoutDiffBlocksIsUnchanged(t *testing.T) {
	evt := tool.StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "shell",
		Output:   map[string]any{"stdout": "plain output\n", "exit_code": 0},
	}
	body, _ := tool.FormatToolStepResult(evt, tool.DefaultMaxFormattedBody)
	if _, ok := renderShellDiffCard(body, DiffThemeDark); ok {
		t.Fatalf("renderShellDiffCard accepted an ordinary shell body:\n%s", body)
	}
}

func TestShellDiffFrameFoldsLikeAnEditCard(t *testing.T) {
	evt := tool.StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "shell",
		Output: map[string]any{
			"stdout":    "done\n",
			"exit_code": 0,
			"shell_edit_diff": tool.ShellEditDiff{
				Files: []tool.ShellEditDiffFile{{
					Path: "/repo/a.txt", DisplayPath: "a.txt", Status: "created",
					Added: 1, Deleted: 0, UnifiedDiff: "@@ -1 +1,2 @@\n+line\n",
				}},
			},
		},
	}
	withBlocks, _ := tool.FormatToolStepResult(evt, tool.DefaultMaxFormattedBody)
	plainEvt := tool.StepEvent{
		Kind:     event.RunEventToolCompleted,
		ToolName: "shell",
		Output:   map[string]any{"stdout": "plain\n", "exit_code": 0},
	}
	withoutBlocks, _ := tool.FormatToolStepResult(plainEvt, tool.DefaultMaxFormattedBody)

	diffFrame := Frame{Kind: FrameTool, Title: "shell", Content: withBlocks}
	if got := toolFrameMaxLines(diffFrame); got != 30 {
		t.Fatalf("toolFrameMaxLines(shell with diff blocks) = %d, want 30", got)
	}
	if !toolPreviewUsesRenderedBody(diffFrame) {
		t.Fatal("toolPreviewUsesRenderedBody(shell with diff blocks) = false, want true")
	}
	plainFrame := Frame{Kind: FrameTool, Title: "shell", Content: withoutBlocks}
	if got := toolFrameMaxLines(plainFrame); got != toolOutputMaxLines {
		t.Fatalf("toolFrameMaxLines(shell without diff blocks) = %d, want %d", got, toolOutputMaxLines)
	}
	if toolPreviewUsesRenderedBody(plainFrame) {
		t.Fatal("toolPreviewUsesRenderedBody(shell without diff blocks) = true, want false")
	}
}
