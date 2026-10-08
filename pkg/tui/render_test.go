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
// surface: a failed turn must read as a sentence about the account's limit,
// never as the provider's HTTP body.
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
	for _, want := range []string{"Usage limit reached on your plus plan", "available again in 2h 28m"} {
		if !strings.Contains(content, want) {
			t.Errorf("error frame is missing %q:\n%s", want, content)
		}
	}
	for _, unwanted := range []string{"usage_limit_reached", "resets_in_seconds", "chatgpt.com", "429"} {
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
	idx, ok, err := sel.Review("Trust this directory?", nil, []string{"Quit", "Trust and continue"}, 0)
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

// A user_interaction card names every question it asks, in full — the same
// words the web's card header uses — rather than nothing at all.
func TestUserInteractionCardHeaderNamesItsQuestions(t *testing.T) {
	meta := tool.ToolMeta{Status: "completed", Input: map[string]any{"questions": []any{
		map[string]any{"header": "Triggers"},
		map[string]any{"question": "What should the greeting look like when the user says hi first thing in the morning?"},
	}}}
	action, target, _ := toolDisplayParts(Frame{Kind: FrameTool, Title: "user_interaction", ToolMeta: meta, Final: true}, "", "")
	if action != "Asked user" {
		t.Fatalf("action = %q", action)
	}
	if target != "Triggers · What should the greeting look like when the user says hi first thing in the morning?" {
		t.Fatalf("target = %q, want every question named in full", target)
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
	block, ok := renderTurnDiffCard(withDiag, DiffThemeDark, 0)
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
	block, ok = renderTurnDiffCard(singular, DiffThemeDark, 0)
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
	block, ok = renderTurnDiffCard(pending, DiffThemeDark, 0)
	if !ok {
		t.Fatal("expected pending render to succeed")
	}
	if plain := stripANSI(block); !strings.Contains(plain, "  └ Diagnostics pending") {
		t.Fatalf("pending summary line missing:\n%s", plain)
	}

	// Without a section the card keeps its diff-only shape.
	block, ok = renderTurnDiffCard(sampleTurnDiff, DiffThemeDark, 0)
	if !ok {
		t.Fatal("expected diff-only render to succeed")
	}
	if plain := stripANSI(block); strings.Contains(plain, "diagnostic") {
		t.Fatalf("no diagnostics section, no diagnostics rows:\n%s", plain)
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

// BenchmarkSelfContainedRows measures the cost of making rows self-contained
// (each row opens its own style and resets at the end).
func BenchmarkSelfContainedRows(b *testing.B) {
	rows := make([]string, 50)
	for i := range rows {
		rows[i] = "\x1b[38;5;252mLine " + strings.Repeat("content ", 20) + "\x1b[0m"
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = selfContainedRows(rows)
	}
}
