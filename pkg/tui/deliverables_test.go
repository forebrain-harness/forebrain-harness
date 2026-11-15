package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// rows is the delivered set these tests share: two files of different classes.
func deliverableRows() []tool.Deliverable {
	return []tool.Deliverable{
		{Title: "2026 Q3 review", Name: "q3.md", Kind: "markdown", RelPath: "out/q3.md", Size: 2048},
		{Title: "ddl.sql", Name: "ddl.sql", Kind: "text", RelPath: "db/ddl.sql", Size: 288994},
	}
}

// manyDeliverables builds n rows, every other one with a name long enough to
// wrap at the test width.
func manyDeliverables(n int) []tool.Deliverable {
	rows := make([]tool.Deliverable, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("file-%02d.txt", i)
		if i%2 == 0 {
			name = fmt.Sprintf("file-%02d-%s.txt", i, strings.Repeat("很长的文件名", 6))
		}
		rows = append(rows, tool.Deliverable{
			Title: name, Name: name, Kind: "text",
			RelPath: fmt.Sprintf("out/f%02d.txt", i), Size: int64(1000 + i),
		})
	}
	return rows
}

// deliverableToolMessage is one completed present_files step as the live
// surface hands it to the reducer.
func deliverableToolMessage(rows []tool.Deliverable, status, content string) NewMessageMsg {
	return NewMessageMsg{Msg: Message{
		Kind: MsgKindTool, StepID: "call_1", ToolName: "present_files",
		Content:   content,
		ToolMeta:  tool.ToolMeta{ToolName: "present_files", Status: status, Deliverables: rows},
		Timestamp: time.Unix(1700000000, 0),
	}}
}

// TestDeliverableCardHoldsNoBodyWhenCompleted pins the one card rule: a call
// that delivered files says so on its header and leaves the list to the turn's
// group, while a failed, refused or canceled call keeps its body — that text is
// the only explanation the user gets.
func TestDeliverableCardHoldsNoBodyWhenCompleted(t *testing.T) {
	for _, tc := range []struct {
		status  string
		content string
		want    string
	}{
		{status: "completed", content: "- **q3.md** · markdown · 2.0 KB · `out/q3.md`", want: ""},
		{status: "failed", content: "**error** (present_files)\n\n```\npermission denied\n```", want: "**error** (present_files)\n\n```\npermission denied\n```"},
		{status: "cancelled", content: "Presented files.", want: "Presented files."},
	} {
		r := &Reducer{}
		frames := r.Reduce(deliverableToolMessage(deliverableRows(), tc.status, tc.content)).Frames
		if len(frames) == 0 || frames[0].Kind != FrameTool {
			t.Fatalf("%s: frames = %#v, want a tool card", tc.status, frames)
		}
		if got := frames[0].Content; got != tc.want {
			t.Fatalf("%s: card body = %q, want %q", tc.status, got, tc.want)
		}
	}
	// A step recorded before the tool reported rows keeps its prose and gives
	// the turn no deliverables at all.
	r := &Reducer{}
	frames := r.Reduce(deliverableToolMessage(nil, "completed", "Presented files.")).Frames
	if len(frames) == 0 || frames[0].Content != "Presented files." {
		t.Fatalf("legacy card body = %#v, want its prose", frames)
	}
	if got := r.Reduce(RunEndedMsg{RunID: "run-1", ClosesTurn: true}).Frames; len(got) != 0 {
		t.Fatalf("a turn that presented no rows drew %#v", got)
	}
}

// TestDeliverablesGroupClosesTheTurnOnce covers the turn boundary the group
// hangs on: a turn resumed by an approval runs twice and still presents one
// group, drawn at the run-end that closes the turn, after the turn's text.
func TestDeliverablesGroupClosesTheTurnOnce(t *testing.T) {
	r := &Reducer{}
	r.Reduce(deliverableToolMessage(deliverableRows()[:1], "completed", ""))
	// The first run ends, the turn continues: no group yet.
	if got := r.Reduce(RunEndedMsg{RunID: "run-1"}).Frames; len(got) != 0 {
		t.Fatalf("a continuing run closed the turn with %#v", got)
	}
	// The resumed run presents the second file and closes the turn.
	r.Reduce(deliverableToolMessage(deliverableRows()[1:], "completed", ""))
	ev := r.Reduce(RunEndedMsg{RunID: "run-2", ClosesTurn: true})
	if len(ev.Frames) != 1 || ev.Frames[0].Kind != FrameDeliverables {
		t.Fatalf("closing frames = %#v, want one deliverables group", ev.Frames)
	}
	group := ev.Frames[0]
	if len(group.Deliverables) != 2 {
		t.Fatalf("group rows = %#v, want both runs' files", group.Deliverables)
	}
	if group.EventID != "" {
		t.Fatalf("group event id = %q, want none: one turn has one producer", group.EventID)
	}
	if ev.WorkedStatus == "" {
		t.Fatal("the closing run drew no Worked for line")
	}
	// The rows are taken, not copied: a second close cannot draw them again.
	if got := r.Reduce(RunEndedMsg{RunID: "run-3", ClosesTurn: true}).Frames; len(got) != 0 {
		t.Fatalf("the same turn's rows were drawn twice: %#v", got)
	}
}

// TestDeliverablesGroupDedupesRows pins that a row list reaching the reducer
// twice does not double a line in the group.
func TestDeliverablesGroupDedupesRows(t *testing.T) {
	r := &Reducer{}
	r.Reduce(deliverableToolMessage(deliverableRows(), "completed", ""))
	r.Reduce(deliverableToolMessage(deliverableRows(), "completed", ""))
	ev := r.Reduce(RunEndedMsg{RunID: "run-1", ClosesTurn: true})
	if len(ev.Frames) != 1 || len(ev.Frames[0].Deliverables) != 2 {
		t.Fatalf("group = %#v, want the two files once each", ev.Frames)
	}
}

// TestWithdrawnTurnDropsItsDeliverables pins that a withdrawn turn is a turn
// that never happened: its files do not leak into the next turn's group.
func TestWithdrawnTurnDropsItsDeliverables(t *testing.T) {
	live := &foregroundTurn{}
	withdrawn := &foregroundTurn{}
	withdrawn.withdraw()
	r := &Reducer{}
	r.Reduce(RunStartedMsg{RunID: "run-1", turn: live})
	r.Reduce(deliverableToolMessage(deliverableRows(), "completed", ""))
	if ev := r.Reduce(RunEndedMsg{RunID: "run-1", turn: withdrawn, ClosesTurn: true}); len(ev.Frames) != 0 {
		t.Fatalf("a withdrawn turn drew %#v", ev.Frames)
	}
	// The next turn presents nothing of its own: it inherits nothing either.
	ev := r.Reduce(RunEndedMsg{RunID: "run-2", turn: &foregroundTurn{}, ClosesTurn: true})
	if len(ev.Frames) != 0 {
		t.Fatalf("a later turn inherited %#v", ev.Frames)
	}
}

// deliverableGroupsIn counts the files a rendered (or folded) body lists,
// skipping any trailing hint row the caller cut off.
func deliverableGroupsIn(lines []string) int {
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = sgrPattern.ReplaceAllString(line, "")
	}
	return len(deliverableLineGroups(plain))
}

// TestDeliverablesFoldByFile covers the fold both ways: four files fold to
// three rows plus a hint, and a click unfolds them and folds them back.
func TestDeliverablesFoldByFile(t *testing.T) {
	four := []tool.Deliverable{
		{Title: "q3.md", Name: "q3.md", Kind: "markdown", RelPath: "out/q3.md", Size: 2048},
		{Title: "ddl.sql", Name: "ddl.sql", Kind: "text", RelPath: "db/ddl.sql", Size: 288},
		{Title: "deck.pptx", Name: "deck.pptx", Kind: "presentation", RelPath: "out/deck.pptx", Size: 512},
		{Title: "shot.png", Name: "shot.png", Kind: "image", RelPath: "out/shot.png", Size: 1024},
	}
	vm := viewModel{}
	block := vm.append(Frame{Kind: FrameDeliverables, Deliverables: four, Final: true})
	if !block.collapsible {
		t.Fatal("four files must fold")
	}
	full, _ := renderFrameLinesWithAgents(block.displayFrame(), 80, DiffThemeDark, 0)
	if len(full) != len(four) {
		t.Fatalf("four short rows rendered %d rows: %q", len(full), full)
	}
	// A row is the name and nothing else (owner ruling): the glyph carries the
	// class and the suffix repeats it, so neither the class, the size nor the
	// path is drawn, and no separator appears between facts.
	if got, want := stripANSI(full[0]), "  ¶ q3.md"; got != want {
		t.Fatalf("row = %q, want %q", got, want)
	}
	for _, row := range full {
		plain := stripANSI(row)
		if strings.Contains(plain, " · ") {
			t.Fatalf("row %q draws a separator", plain)
		}
		for _, redundant := range []string{"markdown", "B ·", "KB", "out/", "db/"} {
			if strings.Contains(plain, redundant) {
				t.Fatalf("row %q repeats %q", plain, redundant)
			}
		}
	}
	folded, headerLocal := foldBlockLines(block, full, 80, 0, "")
	if headerLocal != 0 {
		t.Fatalf("collapsed header row = %d, want 0 so the hint row is clickable", headerLocal)
	}
	if len(folded) != toolOutputMaxLines+1 {
		t.Fatalf("collapsed rows = %d, want %d files + hint:\n%s", len(folded), toolOutputMaxLines, strings.Join(folded, "\n"))
	}
	if plain := stripANSI(folded[len(folded)-1]); !strings.Contains(plain, "··· 1 more deliverable · click to show") {
		t.Fatalf("hint = %q", plain)
	}
	if got := deliverableGroupsIn(folded[:len(folded)-1]); got != toolOutputMaxLines {
		t.Fatalf("collapsed body lists %d files, want %d", got, toolOutputMaxLines)
	}

	// Clicking the hint unfolds; the rows come back whole and the hint says how
	// to fold again.
	vr := renderViewport(&vm, 80, 20, 0, DiffThemeDark)
	hintRow := -1
	for i, line := range vr.lines {
		if strings.Contains(stripANSI(line), "more deliverable") {
			hintRow = i
			break
		}
	}
	if hintRow < 0 {
		t.Fatalf("no fold hint on screen:\n%s", stripANSI(strings.Join(vr.lines, "\n")))
	}
	if !vm.toggleAtScreenRow(vr, hintRow) {
		t.Fatal("clicking the hint did not toggle the group")
	}
	expanded := renderViewport(&vm, 80, 20, 0, DiffThemeDark)
	expandedPlain := stripANSI(strings.Join(expanded.lines, "\n"))
	if !strings.Contains(expandedPlain, "··· click to hide") {
		t.Fatalf("expanded screen lacks the hide hint:\n%s", expandedPlain)
	}
	if strings.Contains(expandedPlain, "more deliverable") {
		t.Fatalf("expanded screen still shows the fold hint:\n%s", expandedPlain)
	}
	if got := deliverableGroupsIn(expanded.lines[:len(expanded.lines)-1]); got != len(four) {
		t.Fatalf("expanded body lists %d files, want %d:\n%s", got, len(four), expandedPlain)
	}
	// Clicking the "click to hide" row folds it back to three files.
	hideRow := -1
	for i, line := range expanded.lines {
		if strings.Contains(stripANSI(line), "click to hide") {
			hideRow = i
		}
	}
	if hideRow < 0 || !vm.toggleAtScreenRow(expanded, hideRow) {
		t.Fatalf("clicking %q did not fold the group back", "click to hide")
	}
	again := renderViewport(&vm, 80, 20, 0, DiffThemeDark)
	if plain := stripANSI(strings.Join(again.lines, "\n")); !strings.Contains(plain, "··· 1 more deliverable · click to show") {
		t.Fatalf("folded-back screen:\n%s", plain)
	}
}

// TestDeliverablesFoldCountsFilesNotRows is the resource and wrapping case: 50
// files fold to 3 + a hint counting 47, and unfolding lists all 50 even though
// half of the titles wrap. Counting rendered rows instead of files would fold
// away a file's own wrapped tail and report the wrong remainder.
func TestDeliverablesFoldCountsFilesNotRows(t *testing.T) {
	rows := manyDeliverables(50)
	block := (&viewModel{}).append(Frame{Kind: FrameDeliverables, Deliverables: rows, Final: true})
	full, _ := renderFrameLinesWithAgents(block.displayFrame(), 80, DiffThemeDark, 0)
	if len(full) <= len(rows) {
		t.Fatalf("expected wrapped rows, got %d rows for %d files", len(full), len(rows))
	}
	if got := deliverableGroupsIn(full); got != len(rows) {
		t.Fatalf("groups = %d, want %d files", got, len(rows))
	}
	folded, _ := foldBlockLines(block, full, 80, 0, "")
	if plain := stripANSI(folded[len(folded)-1]); !strings.Contains(plain, "··· 47 more deliverables · click to show") {
		t.Fatalf("hint = %q", plain)
	}
	if got := deliverableGroupsIn(folded[:len(folded)-1]); got != toolOutputMaxLines {
		t.Fatalf("collapsed body lists %d files, want %d:\n%s", got, toolOutputMaxLines, strings.Join(folded, "\n"))
	}
	block.expanded = true
	expanded, headerLocal := foldBlockLines(block, full, 80, 0, "")
	if headerLocal != 0 {
		t.Fatalf("expanded header row = %d, want 0", headerLocal)
	}
	if len(expanded) != len(full)+1 {
		t.Fatalf("expanded rows = %d, want %d + the hide hint", len(expanded), len(full))
	}
	if got := deliverableGroupsIn(expanded[:len(expanded)-1]); got != len(rows) {
		t.Fatalf("expanded body lists %d files, want %d", got, len(rows))
	}
}

// TestDeliverableRowsAreClickable pins the hover highlight: every row of the
// group is a click target, the first one included, because the group has no
// header.
func TestDeliverableRowsAreClickable(t *testing.T) {
	vm := viewModel{}
	vm.append(Frame{Kind: FrameDeliverables, Deliverables: manyDeliverables(5), Final: true})
	vr := renderViewport(&vm, 80, 20, 0, DiffThemeDark)
	if len(vr.lines) < toolOutputMaxLines+1 {
		t.Fatalf("rows on screen = %d", len(vr.lines))
	}
	for row := 0; row < toolOutputMaxLines+1; row++ {
		if !vr.isClickableRow(row, &vm) {
			t.Fatalf("row %d of the group is not clickable", row)
		}
	}
}

// replayedDeliverableTurns builds the stored transcript of one turn: the
// person's message and a completed present_files row carrying the rows on its
// tool meta (which is what BuildToolMeta stored live).
func replayedDeliverableTurns(worked bool, rows []tool.Deliverable) []state.Message {
	raw, _ := json.Marshal(tool.ToolMeta{ToolName: "present_files", Status: "completed", Deliverables: rows})
	parts, _ := json.Marshal([]map[string]any{{
		"type":           "tool_display",
		"body":           "- **q3.md** · markdown · 2.0 KB · `out/q3.md`",
		"summary":        "ran present_files",
		"tool_meta_json": string(raw),
	}})
	call := state.Message{
		RowID: 2, RunID: "run-1", Role: "tool", ToolStepID: "call_1",
		PartsJSON: string(parts), CreatedAt: 1700000100,
	}
	if worked {
		call.RunStartedAtMs = 1700000000000
		call.RunFinishedAtMs = 1700000120000
		call.RunWorkedMs = 12000
	}
	return []state.Message{
		{RowID: 1, RunID: "run-1", Role: "user", Content: "present the files", CreatedAt: 1700000000},
		call,
	}
}

// replayDeliverableBlocks replays the turns and returns the block frames in
// order, so the group's position can be asserted against the line it closes.
func replayDeliverableBlocks(t *testing.T, turns []state.Message) []Frame {
	t.Helper()
	renderer := NewRenderer(nil, nil)
	renderer.EnableViewportMode()
	t.Cleanup(renderer.DisableViewportMode)
	replayTimelineWithWorkers(renderer, turns, nil, &Reducer{}, nil, nil)
	var frames []Frame
	for _, block := range renderer.vm.blocks {
		frames = append(frames, block.frame)
	}
	return frames
}

// TestReplayDrawsTheGroupAboveItsWorkedLine pins the replayed position: the
// turn's files are drawn after its text and immediately before its "Worked for"
// line, never after it.
func TestReplayDrawsTheGroupAboveItsWorkedLine(t *testing.T) {
	frames := replayDeliverableBlocks(t, replayedDeliverableTurns(true, deliverableRows()))
	group, worked := -1, -1
	for i, f := range frames {
		switch {
		case f.Kind == FrameDeliverables:
			if group >= 0 {
				t.Fatalf("replay drew %d groups", 2)
			}
			group = i
		case f.Kind == FrameStatus && strings.HasPrefix(strings.TrimSpace(f.Title), "Worked for"):
			worked = i
		}
	}
	if group < 0 || worked < 0 {
		t.Fatalf("group at %d, worked line at %d (frames %#v)", group, worked, frames)
	}
	if group+1 != worked {
		t.Fatalf("group at %d, worked line at %d: the group must sit right above it", group, worked)
	}
	if len(frames[group].Deliverables) != 2 {
		t.Fatalf("replayed group rows = %#v", frames[group].Deliverables)
	}
	// The card above it carries no body: its rows are the group's.
	for _, f := range frames[:group] {
		if f.Kind == FrameTool && strings.TrimSpace(f.Content) != "" {
			t.Fatalf("replayed card still carries a body: %q", f.Content)
		}
	}
}

// TestReplayDrawsTheGroupWithoutARunEnd is the crashed-turn case: a turn that
// recorded no run-end line still ends where the next person's message begins,
// and its files are not lost.
func TestReplayDrawsTheGroupWithoutARunEnd(t *testing.T) {
	turns := replayedDeliverableTurns(false, deliverableRows())
	frames := replayDeliverableBlocks(t, turns)
	group := -1
	for i, f := range frames {
		if f.Kind == FrameDeliverables {
			group = i
		}
	}
	if group < 0 {
		t.Fatalf("a turn with no run-end lost its group (frames %#v)", frames)
	}
	if len(frames[group].Deliverables) != 2 {
		t.Fatalf("replayed group rows = %#v", frames[group].Deliverables)
	}
}

// TestReplayKeepsOneGroupPerTurnAcrossRuns pins the merge: two runs of one turn
// (an approval resumed it) present one group, at the turn's last run-end.
func TestReplayKeepsOneGroupPerTurnAcrossRuns(t *testing.T) {
	turns := replayedDeliverableTurns(true, deliverableRows()[:1])
	second := state.Message{
		RowID: 3, RunID: "run-2", Role: "tool", ToolStepID: "call_2",
		PartsJSON: turns[1].PartsJSON, CreatedAt: 1700000200,
		RunStartedAtMs: 1700000100000, RunFinishedAtMs: 1700000130000, RunWorkedMs: 30000,
	}
	turns = append(turns, second)
	frames := replayDeliverableBlocks(t, turns)
	group, lastWorked := -1, -1
	groups := 0
	for i, f := range frames {
		if f.Kind == FrameDeliverables {
			groups++
			group = i
		}
		if f.Kind == FrameStatus && strings.HasPrefix(strings.TrimSpace(f.Title), "Worked for") {
			lastWorked = i
		}
	}
	if groups != 1 {
		t.Fatalf("replay drew %d groups for one turn, want 1", groups)
	}
	if group < 0 || group+1 != lastWorked {
		t.Fatalf("group at %d, last worked line at %d", group, lastWorked)
	}
	// The same file presented by both runs is one row, not two.
	if len(frames[group].Deliverables) != 1 {
		t.Fatalf("group rows = %#v", frames[group].Deliverables)
	}
}

// TestCompletedDeliverableCardDrawsNoPlaceholderBody pins what the card looks
// like once its rows moved to the group: its header alone, not the "(no
// output)" placeholder that would read as an output that went missing. A card
// without rows keeps the placeholder every other empty card uses.
func TestCompletedDeliverableCardDrawsNoPlaceholderBody(t *testing.T) {
	card := Frame{Kind: FrameTool, Title: "present_files", Summary: "ran present_files", Final: true,
		ToolMeta: tool.ToolMeta{ToolName: "present_files", Status: "completed", Deliverables: deliverableRows()}}
	lines, _ := renderFrameLinesWithAgents(card, 80, DiffThemeDark, 0)
	if plain := stripANSI(strings.Join(lines, "\n")); strings.Contains(plain, toolNoOutputText) {
		t.Fatalf("the delivered card drew the placeholder:\n%s", plain)
	}
	if len(lines) == 0 || strings.TrimSpace(stripANSI(lines[0])) == "" {
		t.Fatalf("the delivered card drew no header at all: %q", lines)
	}

	empty := Frame{Kind: FrameTool, Title: "present_files", Summary: "ran present_files", Final: true,
		ToolMeta: tool.ToolMeta{ToolName: "present_files", Status: "completed"}}
	lines, _ = renderFrameLinesWithAgents(empty, 80, DiffThemeDark, 0)
	if plain := stripANSI(strings.Join(lines, "\n")); !strings.Contains(plain, toolNoOutputText) {
		t.Fatalf("an empty card without rows lost its placeholder:\n%s", plain)
	}
}

// TestReplayToolStepMetaKeepsDeliverables pins the replay seam: the meta a
// stored row is rebuilt into must keep the delivered rows, or a card that had
// them live comes back empty after /resume.
func TestReplayToolStepMetaKeepsDeliverables(t *testing.T) {
	meta := tool.ToolMeta{ToolName: "present_files", Status: "completed", Deliverables: deliverableRows()}
	got := replayToolStepMeta("present_files", `{"files":[{"path":"ddl.sql"}]}`, meta, tool.StepKindToolCompleted)
	if len(got.Deliverables) != len(meta.Deliverables) {
		t.Fatalf("deliverables = %#v, want %#v", got.Deliverables, meta.Deliverables)
	}
	if got.Deliverables[1].RelPath != "db/ddl.sql" || got.Deliverables[1].Size != 288994 {
		t.Fatalf("replayed row = %+v", got.Deliverables[1])
	}
}
