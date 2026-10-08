package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
)

// Every LLM request is streamed, and the answer must appear as it arrives
// rather than all at once when the run ends. Each chunk emits a live frame
// carrying the FULL accumulated text so the renderer redraws one block in
// place; RunEnded seals that same block with the Final frame.
func TestReducerStreamsAssistantAndFinalizesOnRunEnd(t *testing.T) {
	var r Reducer
	_ = r.Reduce(RunStartedMsg{RunID: "r1"})
	got := r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindAssistant,
		Content: "hello",
	}}).Frames
	if len(got) != 1 || got[0].Kind != FrameAssistant || got[0].Content != "hello" || got[0].Final {
		t.Fatalf("first chunk must stream as a live assistant frame, got %#v", got)
	}
	got = r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindAssistant,
		Content: "world",
	}}).Frames
	if len(got) != 1 || got[0].Content != "helloworld" || got[0].Final {
		t.Fatalf("later chunks must carry the full accumulated text, not the delta: %#v", got)
	}
	if got[0].RunID != "r1" {
		t.Fatalf("live assistant frame must carry its run id: %#v", got[0])
	}
	ev := r.Reduce(RunEndedMsg{RunID: "r1", WorkedDuration: 2 * time.Minute})
	if len(ev.Frames) != 1 {
		t.Fatalf("expected only assistant frame (worked status moved to WorkedStatus field), got %#v", ev.Frames)
	}
	if ev.Frames[0].Content != "helloworld" || !ev.Frames[0].Final {
		t.Fatalf("unexpected final assistant frame: %#v", ev.Frames[0])
	}
	if !strings.HasPrefix(ev.WorkedStatus, "Worked for 2m 00s · ") || !workedCompletionSuffix.MatchString(ev.WorkedStatus) {
		t.Fatalf("worked status = %q, want the run's own duration and completion time", ev.WorkedStatus)
	}
}

// The live frames and the sealing frame must land on ONE block: the view model
// coalesces consecutive live frames of the same kind and lets the Final frame
// replace the live one, so a streamed answer does not stack one block per token.
func TestViewModelCoalescesStreamedAssistantIntoOneBlock(t *testing.T) {
	var vm viewModel
	vm.append(Frame{Kind: FrameAssistant, Content: "hel"})
	vm.append(Frame{Kind: FrameAssistant, Content: "hello"})
	vm.append(Frame{Kind: FrameAssistant, Content: "hello world"})
	if len(vm.blocks) != 1 {
		t.Fatalf("streamed answer stacked %d blocks, want 1", len(vm.blocks))
	}
	vm.append(Frame{Kind: FrameAssistant, Content: "hello world", Final: true})
	if len(vm.blocks) != 1 || !vm.blocks[0].frame.Final {
		t.Fatalf("sealing frame must replace the live block, got %d blocks: %#v", len(vm.blocks), vm.blocks[0].frame)
	}
	// Text resumed after an interleaved tool call is a new block, not a
	// continuation of the answer that was already sealed before the tool.
	vm.append(Frame{Kind: FrameTool, Content: "ran something"})
	vm.append(Frame{Kind: FrameAssistant, Content: "and now the rest"})
	if len(vm.blocks) != 3 {
		t.Fatalf("post-tool answer must start its own block, got %d", len(vm.blocks))
	}
}

func TestReducerAccumulatesLiveShellOutputInRunningFrame(t *testing.T) {
	var r Reducer
	base := Message{
		Kind:            MsgKindTool,
		StepID:          "shell-live",
		ToolName:        "shell",
		Summary:         "running test command",
		ToolMeta:        tool.ToolMeta{ToolName: "shell", Status: "running"},
		ToolPhase:       event.RunEventToolOutputDelta,
		ToolOutputDelta: true,
	}
	base.Content = "first\r\n"
	first := r.Reduce(NewMessageMsg{Msg: base}).Frames
	if len(first) != 1 || first[0].Content != "first" || !first[0].StreamingOutput {
		t.Fatalf("unexpected first live frame: %#v", first)
	}

	base.Content = "second\n"
	second := r.Reduce(NewMessageMsg{Msg: base}).Frames
	if len(second) != 1 || second[0].Content != "first\nsecond" || !second[0].StreamingOutput {
		t.Fatalf("unexpected accumulated live frame: %#v", second)
	}

	completed := r.Reduce(NewMessageMsg{Msg: Message{
		Kind:      MsgKindTool,
		StepID:    "shell-live",
		ToolName:  "shell",
		Content:   "final result",
		ToolMeta:  tool.ToolMeta{ToolName: "shell", Status: "completed"},
		ToolPhase: event.RunEventToolCompleted,
	}}).Frames
	if len(completed) != 1 || completed[0].Content != "final result" || completed[0].StreamingOutput {
		t.Fatalf("completion did not replace live output: %#v", completed)
	}
}

// workedCompletionSuffix matches the " · HH:MM" the closing line ends with.
// The minute is read off the wall clock at format time, so assertions pin the
// shape, not the value.
var workedCompletionSuffix = regexp.MustCompile(`· \d{2}:\d{2}$`)

func TestReducerAppendsCountersToWorkedStatus(t *testing.T) {
	tracker := NewTracker()
	var r Reducer
	r.WithTracker(tracker)
	_ = r.Reduce(RunStartedMsg{RunID: "r1"})
	tracker.ObserveAgent("agent-1", "", time.Time{})
	tracker.ObserveToolStep("", "read-1", "read_file", "")
	tracker.ObserveUsageDelta("r1", 1500, 300)
	tracker.ObservePlanProgress(1, 2, "Writing tests")
	_ = r.Reduce(NewMessageMsg{Msg: Message{Kind: MsgKindTool, StepID: "read-1"}})
	_ = r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindAssistant,
		Content: "done",
	}})
	ev := r.Reduce(RunEndedMsg{
		RunID:          "r1",
		WorkedDuration: 62 * time.Second,
		InputTokens:    1500,
		OutputTokens:   300,
	})
	if len(ev.Frames) != 1 {
		t.Fatalf("expected assistant frame, got %#v", ev.Frames)
	}
	got := ev.WorkedStatus
	if !strings.HasPrefix(got, "Worked for 1m 02s · ☑1/2 · Writing tests · ") || !workedCompletionSuffix.MatchString(got) {
		t.Fatalf("unexpected worked status: %q", ev.WorkedStatus)
	}
}

// A turn the user interrupted still worked for as long as it ran, and its
// transcript still needs the boundary that says so. The old rule showed the
// line only for a normally completed turn, which left an interrupted one
// running into whatever came next.
func TestReducerShowsWorkedStatusForInterruptedToolRun(t *testing.T) {
	var r Reducer
	_ = r.Reduce(RunStartedMsg{RunID: "r1"})
	_ = r.Reduce(NewMessageMsg{Msg: Message{Kind: MsgKindTool, StepID: "read-1"}})
	_ = r.Reduce(NewMessageMsg{Msg: Message{Kind: MsgKindAssistant, Content: "partial"}})
	ev := r.Reduce(RunEndedMsg{RunID: "r1", WorkedDuration: 2 * time.Minute})
	if !strings.HasPrefix(ev.WorkedStatus, "Worked for 2m 00s · ") || !workedCompletionSuffix.MatchString(ev.WorkedStatus) {
		t.Fatalf("interrupted turn = %q, want it closed by its own worked status and completion time", ev.WorkedStatus)
	}
}

// The old rule hid the line for any turn of 60 seconds or less, which is most
// of them. A quick turn is now closed exactly like a long one, counters and all.
func TestReducerShowsWorkedStatusForShortRun(t *testing.T) {
	tracker := NewTracker()
	var r Reducer
	r.WithTracker(tracker)
	_ = r.Reduce(RunStartedMsg{RunID: "r1"})
	tracker.ObserveToolStep("", "read-1", "read_file", "")
	tracker.ObserveUsageDelta("r1", 1200, 80)
	ev := r.Reduce(RunEndedMsg{RunID: "r1", WorkedDuration: 2 * time.Second, InputTokens: 1200, OutputTokens: 80})
	got := ev.WorkedStatus
	if !strings.HasPrefix(got, "Worked for 2s · ") || !workedCompletionSuffix.MatchString(got) {
		t.Fatalf("short turn = %q", ev.WorkedStatus)
	}
}

// The completion minute is the local wall clock at format time, HH:MM.
func TestWorkedCompletionTimeFormatsLocalHourMinute(t *testing.T) {
	if got := workedCompletionTime(time.Date(2026, 9, 18, 11, 35, 12, 0, time.Local)); got != "11:35" {
		t.Fatalf("workedCompletionTime() = %q, want 11:35", got)
	}
}

// A failure ends a run too, and reports the same figures.
func TestReducerShowsWorkedStatusForFailedRun(t *testing.T) {
	var r Reducer
	_ = r.Reduce(RunStartedMsg{RunID: "r1"})
	ev := r.Reduce(RunEndedMsg{RunID: "r1", WorkedDuration: 3 * time.Second, Error: "provider rejected max_tokens"})
	if !strings.HasPrefix(ev.WorkedStatus, "Worked for 3s · ") || !workedCompletionSuffix.MatchString(ev.WorkedStatus) {
		t.Fatalf("failed turn = %q, want it closed by its own worked status and completion time", ev.WorkedStatus)
	}
}

func TestReducerFoldsRuntimeUsageDeltaBeforeRunEnd(t *testing.T) {
	tracker := NewTracker()
	var r Reducer
	r.WithTracker(tracker)

	_ = r.Reduce(RunStartedMsg{RunID: "r1"})
	out := r.Reduce(TokenUsageDeltaMsg{RunID: "r1", InputTokens: 1499, OutputTokens: 84})
	if len(out.Frames) != 0 {
		t.Fatalf("usage delta should not render frames, got %#v", out.Frames)
	}

	snap := tracker.SnapshotSession()
	if snap.InputTokens != 1499 || snap.OutputTokens != 84 {
		t.Fatalf("tracker snapshot = %+v, want input=1499 output=84", snap)
	}

	_ = r.Reduce(RunEndedMsg{RunID: "r1", InputTokens: 1499, OutputTokens: 84})
	snap = tracker.SnapshotSession()
	if snap.InputTokens != 1499 || snap.OutputTokens != 84 {
		t.Fatalf("run end should not double count usage, got %+v", snap)
	}
}

func TestReducerBuffersAssistantChunksVerbatim(t *testing.T) {
	var r Reducer
	_ = r.Reduce(RunStartedMsg{RunID: "r1"})
	_ = r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindAssistant,
		Content: "Hello",
	}})
	_ = r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindAssistant,
		Content: "!\r\nSecond line",
	}})
	got := r.Reduce(RunEndedMsg{RunID: "r1"}).Frames
	if len(got) == 0 || got[0].Kind != FrameAssistant {
		t.Fatalf("expected final assistant frame, got %#v", got)
	}
	if got[0].Content != "Hello!\nSecond line" {
		t.Fatalf("unexpected buffered assistant content: %q", got[0].Content)
	}
}

func TestReducerStreamsReasoningIncrementallyThenFinalizes(t *testing.T) {
	var r Reducer
	_ = r.Reduce(RunStartedMsg{RunID: "r1"})

	first := r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindReasoning,
		Content: "**Reviewing",
	}}).Frames
	if len(first) != 1 || first[0].Kind != FrameThinking {
		t.Fatalf("expected incremental thinking frame, got %#v", first)
	}
	if first[0].Final {
		t.Fatalf("incremental thinking frame must not be final: %#v", first[0])
	}
	if first[0].Content != "**Reviewing" {
		t.Fatalf("first thinking content = %q", first[0].Content)
	}

	second := r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindReasoning,
		Content: " tool functionality**",
	}}).Frames
	if len(second) != 1 || second[0].Kind != FrameThinking {
		t.Fatalf("expected second incremental thinking frame, got %#v", second)
	}
	// Streaming frames carry the full accumulated reasoning (not just the
	// delta) so the renderer re-wraps and redraws the whole block. Passing
	// only the delta makes token-level streaming print one fragment per line.
	if second[0].Content != "**Reviewing tool functionality**" {
		t.Fatalf("second thinking content = %q", second[0].Content)
	}

	final := r.Reduce(RunEndedMsg{RunID: "r1", WorkedDuration: time.Second}).Frames
	if len(final) == 0 || final[0].Kind != FrameThinking || !final[0].Final {
		t.Fatalf("run end must finalize streamed reasoning, got %#v", final)
	}
	if final[0].Content != "**Reviewing tool functionality**" {
		t.Fatalf("final thinking content = %q", final[0].Content)
	}
}

func TestReducerFinalizesReasoningOnReasoningDone(t *testing.T) {
	var r Reducer
	_ = r.Reduce(RunStartedMsg{RunID: "r1"})
	_ = r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindReasoning,
		Content: "Need another tool pass.",
	}})

	done := r.Reduce(ReasoningDoneMsg{}).Frames
	if len(done) != 1 || done[0].Kind != FrameThinking || !done[0].Final {
		t.Fatalf("reasoning done must finalize thinking, got %#v", done)
	}
	if done[0].Content != "Need another tool pass." {
		t.Fatalf("final thinking content = %q", done[0].Content)
	}

	runEnd := r.Reduce(RunEndedMsg{RunID: "r1"}).Frames
	for _, frame := range runEnd {
		if frame.Kind == FrameThinking {
			t.Fatalf("run end must not duplicate reasoning after done, got %#v", runEnd)
		}
	}
}

func TestReducerReasoningFramesDoNotCarrySyntheticTitle(t *testing.T) {
	var r Reducer
	_ = r.Reduce(RunStartedMsg{RunID: "r1"})

	first := r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindReasoning,
		Content: "Reviewing internal/tui/reducer.go.",
	}})
	if len(first.Frames) != 1 || first.Frames[0].Kind != FrameThinking {
		t.Fatalf("expected thinking frame, got %#v", first.Frames)
	}
	if first.Frames[0].Title != "" {
		t.Fatalf("thinking frame must not carry synthetic title: %#v", first.Frames[0])
	}
}

func TestReducerStreamResetClearsAssistantBuffer(t *testing.T) {
	var r Reducer
	_ = r.Reduce(RunStartedMsg{RunID: "r1"})
	_ = r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindAssistant,
		Content: "draft",
	}})
	_ = r.Reduce(StreamResetMsg{})
	got := r.Reduce(RunEndedMsg{RunID: "r1"}).Frames
	if len(got) != 0 {
		t.Fatalf("unexpected frames after reset: %#v", got)
	}
}

func TestReducerSuppressesInternalStatusEvents(t *testing.T) {
	var r Reducer
	if out := r.Reduce(TokenBudgetUpdatedMsg{Model: "gpt-5"}); len(out.Frames) != 0 {
		t.Fatalf("expected no token budget frames, got %#v", out.Frames)
	}
}

func TestReducerContextCompactedEmitsBanner(t *testing.T) {
	var r Reducer
	out := r.Reduce(ContextCompactedMsg{Payload: event.ContextCompactedPayload{
		CompactionID: "c1", Trigger: "auto", Strategy: "remote_v2", SummarySource: "remote_compaction",
		Reason: "context budget", BoundaryID: "window-3", WindowNumber: 3,
		ReplacedItems: 12, TokensBefore: 182_400, TokensAfter: 64_000,
		Summary:  "# Context Checkpoint\n\n- kept the plan",
		Duration: "14.2s",
	}})
	if len(out.Frames) != 1 {
		t.Fatalf("want one remote compact frame, got %d: %#v", len(out.Frames), out.Frames)
	}
	f := out.Frames[0]
	if f.Kind != FrameMemoryCompact || !f.Final || f.StepID != "compact-c1" || f.Title != compactTitleDone {
		t.Fatalf("frame = %#v", f)
	}
	if f.Summary != "182.4k → 64k tokens (−64%) · 14.2s" {
		t.Errorf("Summary = %q", f.Summary)
	}
	// The body is the checkpoint alone; debug fields stay out of the transcript.
	if f.Content != "# Context Checkpoint\n\n- kept the plan" {
		t.Errorf("Content = %q, want the checkpoint summary verbatim", f.Content)
	}
}

// TestReducerCompactionCardFollowsItsLifecycle pins the one card a
// compaction draws: started with its size, advancing with each report, and
// replaced in place by how it ended.
func TestReducerCompactionCardFollowsItsLifecycle(t *testing.T) {
	var r Reducer
	started := r.Reduce(ContextCompactingMsg{CompactionID: "c1", Trigger: "auto", TokensBefore: 182_400})
	if len(started.Frames) != 1 {
		t.Fatalf("started frames = %#v", started.Frames)
	}
	f := started.Frames[0]
	if f.Kind != FrameMemoryCompact || f.Final || f.StepID != "compact-c1" || f.Title != compactTitleRunning || f.Summary != "starting · 182.4k tokens" || f.Progress != 0 {
		t.Fatalf("running frame = %#v", f)
	}
	if started.ComposerTokenStats != nil {
		t.Fatal("a starting compaction must leave the footer alone, not read it as 0% left")
	}
	progress := r.Reduce(ContextCompactProgressMsg{CompactionID: "c1", Percent: 42, Phase: event.CompactPhaseSummarizing})
	if len(progress.Frames) != 1 || progress.Frames[0].Progress != 42 || progress.Frames[0].Summary != "summarizing · 182.4k tokens" || progress.Frames[0].StepID != "compact-c1" {
		t.Fatalf("progress frames = %#v", progress.Frames)
	}
	saving := r.Reduce(ContextCompactProgressMsg{CompactionID: "c1", Percent: 95, Phase: event.CompactPhaseSaving})
	if saving.Frames[0].Summary != "saving checkpoint · 182.4k tokens" {
		t.Fatalf("saving frame = %#v", saving.Frames[0])
	}
	done := r.Reduce(ContextCompactedMsg{Payload: event.ContextCompactedPayload{CompactionID: "c1", Strategy: "local", TokensBefore: 182_400, TokensAfter: 12_300}})
	if len(done.Frames) != 2 || done.Frames[0].StepID != "compact-c1" || !done.Frames[0].Final {
		t.Fatalf("done frames = %#v", done.Frames)
	}
	if done.Frames[1].Kind != FrameSystem || done.Frames[1].Content != assembly.WarningMessage {
		t.Fatalf("local compact warning=%#v", done.Frames[1])
	}
	if len(r.compactions) != 0 {
		t.Fatal("a finished compaction's card state must be released")
	}
}

func TestReducerCompactionFailureAndCancellation(t *testing.T) {
	var r Reducer
	r.Reduce(ContextCompactingMsg{CompactionID: "c1"})
	out := r.Reduce(ContextCompactFailedMsg{CompactionID: "c1", Error: "provider unavailable"})
	f := out.Frames[0]
	if f.Kind != FrameMemoryCompact || !f.Final || f.StepID != "compact-c1" || f.Title != compactTitleFailed || f.Content != "provider unavailable" {
		t.Fatalf("failed compact frame=%#v", f)
	}
	r.Reduce(ContextCompactingMsg{CompactionID: "c2"})
	out = r.Reduce(ContextCompactFailedMsg{CompactionID: "c2", Cancelled: true})
	if f := out.Frames[0]; f.Title != compactTitleCancelled || f.Content != "" || !f.Final {
		t.Fatalf("cancelled compact frame=%#v", f)
	}
}

// TestReducerSubagentCompactionStaysInItsView pins that a subagent's
// compaction card names the subagent, which routes it to that view.
func TestReducerSubagentCompactionStaysInItsView(t *testing.T) {
	var r Reducer
	for _, msg := range []any{
		ContextCompactingMsg{CompactionID: "c1", AgentID: "explorer-1"},
		ContextCompactProgressMsg{CompactionID: "c1", AgentID: "explorer-1", Percent: 10},
		ContextCompactedMsg{Payload: event.ContextCompactedPayload{CompactionID: "c1", AgentID: "explorer-1"}},
	} {
		for _, f := range r.Reduce(msg).Frames {
			if f.AgentID != "explorer-1" {
				t.Fatalf("%T drew %#v outside the subagent's view", msg, f)
			}
		}
	}
}

func TestReducerContextCompactedFlushesAssistantFirst(t *testing.T) {
	var r Reducer
	r.Reduce(RunStartedMsg{RunID: "r1"})
	r.Reduce(NewMessageMsg{Msg: Message{Kind: MsgKindAssistant, Content: "thinking..."}})
	out := r.Reduce(ContextCompactedMsg{Payload: event.ContextCompactedPayload{CompactionID: "c1", ReplacedItems: 3, TokensBefore: 100, TokensAfter: 40, Duration: "50ms"}})
	if len(out.Frames) != 2 {
		t.Fatalf("want assistant flush and compact frames, got %d: %#v", len(out.Frames), out.Frames)
	}
	if out.Frames[0].Kind != FrameAssistant {
		t.Fatalf("first frame must be FrameAssistant, got %v", out.Frames[0].Kind)
	}
	if out.Frames[1].Kind != FrameMemoryCompact {
		t.Fatalf("second frame must be FrameMemoryCompact, got %v", out.Frames[1].Kind)
	}
}

func TestReducerFlushesReasoningBeforeAssistant(t *testing.T) {
	var r Reducer
	_ = r.Reduce(RunStartedMsg{RunID: "r1"})
	_ = r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindReasoning,
		Content: "I need to fix all the return statements.",
	}})
	assistantStart := r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindAssistant,
		Content: "done",
	}}).Frames
	// Reasoning is sealed first, then the answer starts streaming in the same
	// batch: the thinking block closes before the answer block opens.
	if len(assistantStart) != 2 || assistantStart[0].Kind != FrameThinking || !assistantStart[0].Final {
		t.Fatalf("assistant start must finalize reasoning, got %#v", assistantStart)
	}
	if assistantStart[1].Kind != FrameAssistant || assistantStart[1].Content != "done" || assistantStart[1].Final {
		t.Fatalf("assistant start must also stream the first chunk, got %#v", assistantStart[1])
	}
	got := r.Reduce(RunEndedMsg{RunID: "r1", WorkedDuration: 4 * time.Second}).Frames
	if len(got) < 1 {
		t.Fatalf("expected assistant frame, got %#v", got)
	}
	if got[0].Kind != FrameAssistant {
		t.Fatalf("first frame kind=%v want FrameAssistant", got[0].Kind)
	}
	if assistantStart[0].Title != "" {
		t.Fatalf("assistant-start thinking frame must not carry synthetic title: %#v", assistantStart[0])
	}
}

func TestReducerSealsAgentTextWhenReasoningStarts(t *testing.T) {
	var r Reducer
	_ = r.Reduce(RunStartedMsg{RunID: "r1"})
	_ = r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindAssistant,
		Content: "step one.",
		AgentID: "task-r",
	}})
	thinkingStart := r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindReasoning,
		Content: "think",
		AgentID: "task-r",
	}}).Frames
	// A block of one kind ends when a block of the other kind starts, in
	// either direction: the reasoning that follows text seals that text as a
	// final frame, the way reasoning is sealed when text starts.
	if len(thinkingStart) != 2 || thinkingStart[0].Kind != FrameAssistant ||
		!thinkingStart[0].Final || thinkingStart[0].Content != "step one." {
		t.Fatalf("reasoning start must seal the streamed text, got %#v", thinkingStart)
	}
	if thinkingStart[1].Kind != FrameThinking || thinkingStart[1].Final {
		t.Fatalf("reasoning start must stream its own block live, got %#v", thinkingStart[1])
	}
	next := r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindAssistant,
		Content: "step two.",
		AgentID: "task-r",
	}}).Frames
	if len(next) != 2 || next[0].Kind != FrameThinking || !next[0].Final {
		t.Fatalf("text after thinking must seal the thinking block, got %#v", next)
	}
	if next[1].Kind != FrameAssistant || next[1].Content != "step two." || next[1].Final {
		t.Fatalf("the next text block must start from empty, not repeat what came before: %#v", next[1])
	}
}

func TestReducerMapsToolAndErrorMessages(t *testing.T) {
	var r Reducer
	tool := r.Reduce(NewMessageMsg{Msg: Message{
		Kind:     MsgKindTool,
		StepID:   "call-123",
		ToolName: "shell",
		Content:  "done",
	}}).Frames
	if len(tool) != 1 || tool[0].Kind != FrameTool || tool[0].Title != "shell" {
		t.Fatalf("unexpected tool frame: %#v", tool)
	}
	if tool[0].StepID != "call-123" {
		t.Fatalf("tool StepID = %q, want call-123", tool[0].StepID)
	}
	errFrame := r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindError,
		Content: "failed",
	}}).Frames
	if len(errFrame) != 1 || errFrame[0].Kind != FrameError {
		t.Fatalf("unexpected error frame: %#v", errFrame)
	}
}

// The executor's duration is the batch's wall clock on a multi-task card's
// header; a single dispatched task carries its own final elapsed on its row,
// so its header does not repeat one.
func TestReducerFanoutCompletionPreservesExecutorDuration(t *testing.T) {
	input := map[string]any{"tasks": []any{
		map[string]any{"prompt": "inspect durations", "subagent_type": "explore"},
		map[string]any{"prompt": "inspect the renderer", "subagent_type": "explore"},
	}}
	var r Reducer
	start := r.Reduce(NewMessageMsg{Msg: subagentStepMessage("fanout-duration", tool.StepEvent{
		Kind: tool.StepKindToolStarted, ToolName: "subagent_fanout", Input: input,
	})}).Frames
	if len(start) != 1 || start[0].Kind != FrameFanout || start[0].Duration != 0 {
		t.Fatalf("unexpected fanout start: %#v", start)
	}

	completed := r.Reduce(NewMessageMsg{Msg: subagentStepMessage("fanout-duration", tool.StepEvent{
		Kind: tool.StepKindToolCompleted, ToolName: "subagent_fanout", Input: input,
		Output:   map[string]any{"output": `{"results":[{"index":0,"ok":true},{"index":1,"ok":true}]}`},
		Duration: 1750 * time.Millisecond,
	})}).Frames
	if len(completed) != 1 || completed[0].Kind != FrameFanout {
		t.Fatalf("unexpected fanout completion: %#v", completed)
	}
	if completed[0].Duration != 1750*time.Millisecond {
		t.Fatalf("duration=%s want 1.75s", completed[0].Duration)
	}
}

// A dispatch that failed leaves its tasks waiting — no subagent ever spawned to
// report an end — and the card must report the failure the run recorded. Live
// reduces a started message and then the completion; a replay reduces the
// completed row alone. Both arrival orders have to settle the waiting task the
// same way, or the same conversation shows a failure after a resume that the
// live surface never painted (or the reverse).
func TestReducerFanoutFailedDispatchSettlesWaitingTasksBothOrders(t *testing.T) {
	input := map[string]any{"task": "verify the diff quickly"}
	const why = "unknown tool \"subagent_run\""
	newStarted := func() Message {
		return subagentStepMessage("fanout-fail", tool.StepEvent{
			Kind: tool.StepKindToolStarted, ToolName: "subagent_run", Input: input,
		})
	}
	completed := subagentStepMessage("fanout-fail", tool.StepEvent{
		Kind: tool.StepKindToolCompleted, ToolName: "subagent_run", Input: input, Error: why,
	})

	// Live order: started, then the failed completion.
	var live Reducer
	live.Reduce(NewMessageMsg{Msg: newStarted()})
	got := live.Reduce(NewMessageMsg{Msg: completed}).Frames
	if len(got) != 1 || got[0].Kind != FrameFanout {
		t.Fatalf("live completion frames=%#v", got)
	}
	if s := got[0].Summary; s != "Failed to run 1 fork task" {
		t.Fatalf("live summary = %q, want the dispatch failure reported", s)
	}
	if got[0].FanoutCallError != why {
		t.Fatalf("live call error = %q, want %q", got[0].FanoutCallError, why)
	}
	if strings.Contains(got[0].Content, why) {
		t.Fatalf("the call's failure must not be copied onto the task rows: %q", got[0].Content)
	}

	// Replay order: the completed row alone.
	var replay Reducer
	got = replay.Reduce(NewMessageMsg{Msg: completed}).Frames
	if len(got) != 1 || got[0].Kind != FrameFanout {
		t.Fatalf("replay frames=%#v", got)
	}
	if got[0].Summary != "Failed to run 1 fork task" {
		t.Fatalf("replay summary = %q, want the same card live drew", got[0].Summary)
	}
	if liveSummary := liveSummaryOf(t, &live, "fanout-fail"); liveSummary != got[0].Summary {
		t.Fatalf("live summary %q != replay summary %q", liveSummary, got[0].Summary)
	}
}

func liveSummaryOf(t *testing.T, r *Reducer, stepID string) string {
	t.Helper()
	fs := r.findFanoutByStepID(stepID)
	if fs == nil {
		t.Fatal("fanout state missing")
	}
	return fanoutCardHeader(fs)
}

func TestReducerKeepsRunningToolFramesVisible(t *testing.T) {
	var r Reducer
	got := r.Reduce(NewMessageMsg{Msg: Message{
		Kind:     MsgKindTool,
		StepID:   "call-fanout",
		ToolName: "subagent_fanout",
		Summary:  "running subagent_fanout",
	}}).Frames
	if len(got) != 1 {
		t.Fatalf("expected one running tool frame, got %#v", got)
	}
	if got[0].Kind != FrameTool || got[0].Title != "subagent_fanout" {
		t.Fatalf("unexpected running tool frame: %#v", got[0])
	}
	if got[0].Summary != "running subagent_fanout" {
		t.Fatalf("running tool summary = %q, want running subagent_fanout", got[0].Summary)
	}
}

func TestReducerSubagentSpawnedAddsRosterRow(t *testing.T) {
	var r Reducer
	_ = r.Reduce(SubagentSpawnedMsg{
		AgentID:   "task-1",
		AgentType: "verification",
		TaskID:    "task-1",
	})
	snap := r.AgentRosterSnapshot()
	if len(snap.Rows) != 1 {
		t.Fatalf("expected one roster row, got %#v", snap.Rows)
	}
	if snap.Rows[0].ID != "task-1" || snap.Rows[0].Status != "running" {
		t.Fatalf("unexpected roster row: %#v", snap.Rows[0])
	}
}

func TestReducerSubagentContinueKeepsRepeatedPromptPerExecution(t *testing.T) {
	r := &Reducer{}
	first := r.Reduce(SubagentSpawnedMsg{AgentID: "task-1", TaskID: "task-1", Task: "check again", ExecutionID: "exec-1"})
	duplicate := r.Reduce(SubagentSpawnedMsg{AgentID: "task-1", TaskID: "task-1", Task: "check again", ExecutionID: "exec-1"})
	_ = r.Reduce(SubagentEndedMsg{AgentID: "task-1", TaskID: "task-1", Status: "done", ExecutionID: "exec-1"})
	continued := r.Reduce(SubagentSpawnedMsg{AgentID: "task-1", TaskID: "task-1", Task: "check again", ExecutionID: "exec-2"})
	if len(first.Frames) != 2 || len(continued.Frames) != 2 {
		t.Fatalf("frame counts first=%d continued=%d", len(first.Frames), len(continued.Frames))
	}
	// The duplicate spawn repeats no prompt; it re-emits the card it already
	// opened, which replaces that block in place rather than adding one.
	if len(duplicate.Frames) != 1 || duplicate.Frames[0].Kind != FrameFanout {
		t.Fatalf("duplicate spawn frames = %#v, want just the card it already opened", duplicate.Frames)
	}
	if duplicate.Frames[0].StepID != first.Frames[len(first.Frames)-1].StepID {
		t.Fatalf("duplicate spawn opened a second card: %#v", duplicate.Frames)
	}
	if continued.Frames[0].Kind != FrameUser || continued.Frames[0].Content != "check again" {
		t.Fatalf("continued prompt frame = %+v", continued.Frames[0])
	}
}

func TestReducerCountsToolStepOnceAcrossLifecycleMessages(t *testing.T) {
	tracker := NewTracker()
	var r Reducer
	r.WithTracker(tracker)
	for _, content := range []string{"running git status", "awaiting approval", "ran git status"} {
		_ = r.Reduce(NewMessageMsg{Msg: Message{
			Kind:     MsgKindTool,
			StepID:   "call-git-status",
			ToolName: "shell",
			Content:  content,
		}})
	}
	if got := tracker.SnapshotSession().Tools; got != 1 {
		t.Fatalf("expected one logical tool call, got %d", got)
	}
}

func TestReducerFlushesAssistantBufferBeforeToolFrame(t *testing.T) {
	var r Reducer
	_ = r.Reduce(RunStartedMsg{RunID: "r1"})
	_ = r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindAssistant,
		Content: "Now let me check whether there are other new files to include:",
	}})
	got := r.Reduce(NewMessageMsg{Msg: Message{
		Kind:     MsgKindTool,
		ToolName: "shell",
		Content:  "clean",
	}}).Frames
	if len(got) != 2 {
		t.Fatalf("expected buffered assistant frame followed by tool frame, got %#v", got)
	}
	if got[0].Kind != FrameAssistant || got[0].Content != "Now let me check whether there are other new files to include:" || !got[0].Final {
		t.Fatalf("unexpected leading assistant frame: %#v", got[0])
	}
	if got[1].Kind != FrameTool || got[1].Title != "shell" {
		t.Fatalf("unexpected trailing tool frame: %#v", got[1])
	}

	// Continued assistant text after a tool call should buffer fresh and
	// flush at RunEnded without re-emitting the prior text.
	_ = r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindAssistant,
		Content: "Continue output",
	}})
	end := r.Reduce(RunEndedMsg{RunID: "r1"}).Frames
	if len(end) == 0 || end[0].Kind != FrameAssistant || end[0].Content != "Continue output" {
		t.Fatalf("expected only new assistant text on run end, got %#v", end)
	}
}

// A subagent's todo list belongs to that subagent's view, not the
// conversation, and it must not overwrite the composer's plan progress for the
// primary agent's own plan.
func TestReducerRoutesSubagentPlanIntoItsOwnView(t *testing.T) {
	tracker := NewTracker()
	var r Reducer
	r.WithTracker(tracker)
	r.Reduce(PlanUpdatedMsg{Payload: event.PlanUpdatedPayload{
		Title: "Updated Plan", Explanation: "Writing tests", Completed: 1, Total: 2,
		Items: []event.PlanUpdateItem{{Content: "Write tests", Status: "in_progress"}},
	}})

	out := r.Reduce(PlanUpdatedMsg{Payload: event.PlanUpdatedPayload{
		Title: "Updated Plan", Explanation: "Reading source", Completed: 0, Total: 5,
		Items:   []event.PlanUpdateItem{{Content: "Read source", Status: "in_progress"}},
		AgentID: "task-1",
	}})
	if len(out.Frames) != 1 || out.Frames[0].Kind != FramePlan {
		t.Fatalf("expected a plan frame, got %#v", out.Frames)
	}
	if out.Frames[0].AgentID != "task-1" {
		t.Fatalf("plan frame agent = %q, want the subagent that emitted it", out.Frames[0].AgentID)
	}
	if !strings.Contains(out.Frames[0].Content, "▣ Read source") {
		t.Fatalf("unexpected plan content: %q", out.Frames[0].Content)
	}
	snap := tracker.SnapshotSession()
	if snap.PlanDone != 1 || snap.PlanTotal != 2 || snap.PlanActive != "Write tests" {
		t.Fatalf("a subagent's plan overwrote the conversation's progress: %+v", snap)
	}
}

func TestReducerMapsPlanUpdatedMessage(t *testing.T) {
	tracker := NewTracker()
	var r Reducer
	r.WithTracker(tracker)
	out := r.Reduce(PlanUpdatedMsg{Payload: event.PlanUpdatedPayload{
		Title:       "Updated Plan",
		Explanation: "Writing tests",
		Completed:   1,
		Total:       2,
		Items: []event.PlanUpdateItem{
			{Content: "Write tests", Status: "in_progress"},
			{Content: "Ship", Status: "pending"},
		},
	}})
	if len(out.Frames) != 1 || out.Frames[0].Kind != FramePlan {
		t.Fatalf("expected plan frame, got %#v", out.Frames)
	}
	if out.Frames[0].Title != "Updated Plan" {
		t.Fatalf("expected Updated Plan title, got %q", out.Frames[0].Title)
	}
	if !strings.Contains(out.Frames[0].Content, "Writing tests") || !strings.Contains(out.Frames[0].Content, "▣ Write tests") || !strings.Contains(out.Frames[0].Content, "□ Ship") {
		t.Fatalf("unexpected plan content: %q", out.Frames[0].Content)
	}
	snap := tracker.SnapshotSession()
	if snap.PlanDone != 1 || snap.PlanTotal != 2 || snap.PlanActive != "Write tests" {
		t.Fatalf("unexpected tracker plan progress: %+v", snap)
	}
}

func TestReducerSystemMessageRendersFullReport(t *testing.T) {
	var r Reducer
	body := make([]string, 0, 10)
	for i := 1; i <= 10; i++ {
		body = append(body, fmt.Sprintf("report line %d", i))
	}
	out := r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindSystem,
		Content: strings.Join(body, "\n"),
	}})
	if len(out.Frames) != 1 {
		t.Fatalf("expected one frame, got %#v", out.Frames)
	}
	frame := out.Frames[0]
	if frame.Kind != FrameSystem {
		t.Fatalf("expected system frame, got %#v", frame.Kind)
	}
	for _, line := range body {
		if !strings.Contains(frame.Content, line) {
			t.Fatalf("expected full report content, missing %q in %q", line, frame.Content)
		}
	}

	// The viewport renders every line (no folding) and no row of the report is
	// clickable: a user-requested report is never collapsed behind a header.
	vm := &viewModel{}
	vm.append(frame)
	vr := renderViewport(vm, 80, 200, 0, DiffThemeDark)
	plain := stripANSI(strings.Join(vr.lines, "\n"))
	for _, line := range body {
		if !strings.Contains(plain, line) {
			t.Fatalf("expected rendered report to contain %q, got:\n%s", line, plain)
		}
	}
	for row := 0; row < len(vr.lines); row++ {
		if vr.isClickableRow(row, vm) {
			t.Fatalf("system report row %d should not be clickable", row)
		}
	}
}

func TestReducerTokenBudgetUpdatedReturnsComposerStats(t *testing.T) {
	var r Reducer
	out := r.Reduce(TokenBudgetUpdatedMsg{
		TokenUsage:    9000,
		ContextWindow: 128000,
		PercentLeft:   93,
	})
	if out.ComposerTokenStats == nil {
		t.Fatalf("expected composer token stats")
	}
	if !out.ComposerTokenStats.Active {
		t.Fatalf("expected active composer token stats: %#v", out.ComposerTokenStats)
	}
	if out.ComposerTokenStats.PercentLeft != 93 {
		t.Fatalf("unexpected percent left: %#v", out.ComposerTokenStats)
	}
}

func TestReducerSessionSwitch(t *testing.T) {
	var r Reducer
	switched := r.Reduce(SessionSwitchedMsg{SessionID: "s2", Title: "two"})
	if switched.SwitchSessionID != "s2" {
		t.Fatalf("unexpected switch id: %#v", switched)
	}
}

// A run's duration is reported however short it is: the label closes the
// transcript, so suppressing it for a quick turn left the reader with no
// boundary and no account of what the turn cost.
func TestWorkedForLabel(t *testing.T) {
	if got := workedForLabel(2 * time.Second); got != "Worked for 2s" {
		t.Fatalf("unexpected label for a short run: %q", got)
	}
	if got := workedForLabel(61 * time.Second); got != "Worked for 1m 01s" {
		t.Fatalf("unexpected minutes label: %q", got)
	}
	if got := workedForLabel(9*time.Minute + 9*time.Second); got != "Worked for 9m 09s" {
		t.Fatalf("unexpected minutes label: %q", got)
	}
	if got := workedForLabel(time.Hour + 2*time.Minute + 3*time.Second); got != "Worked for 1h 02m 03s" {
		t.Fatalf("unexpected hours label: %q", got)
	}
}

func TestFormatThoughtDurationLabel(t *testing.T) {
	if got := formatThoughtDurationLabel(0); got != "" {
		t.Fatalf("expected empty thought label for zero duration, got %q", got)
	}
	if got := formatThoughtDurationLabel(42 * time.Second); got != "Thought for 42s" {
		t.Fatalf("unexpected seconds-only label: %q", got)
	}
	if got := formatThoughtDurationLabel(time.Minute + 10*time.Second); got != "Thought for 1m 10s" {
		t.Fatalf("unexpected minutes label: %q", got)
	}
	if got := formatThoughtDurationLabel(time.Hour + 2*time.Minute + 3*time.Second); got != "Thought for 1h 02m 03s" {
		t.Fatalf("unexpected hours label: %q", got)
	}
}

// TestReducerReasoningDurationSetOnFinal verifies that the reducer records the
// thinking-block duration on the finalized FrameThinking and resets it for the
// next block so each block shows its own elapsed time, not a cumulative total.
func TestReducerReasoningDurationSetOnFinal(t *testing.T) {
	var r Reducer
	_ = r.Reduce(RunStartedMsg{RunID: "r1"})

	// First reasoning delta stamps the start.
	r1 := r.Reduce(NewMessageMsg{Msg: Message{Kind: MsgKindReasoning, Content: "first thought"}})
	if len(r1.Frames) != 1 || r1.Frames[0].Kind != FrameThinking || r1.Frames[0].Final {
		t.Fatalf("expected one non-final thinking frame, got %#v", r1.Frames)
	}

	// Flush via an interleaved assistant message: produces the final thinking
	// frame carrying Duration > 0, then the assistant frame.
	a := r.Reduce(NewMessageMsg{Msg: Message{Kind: MsgKindAssistant, Content: "answer"}})
	var finalThinking *Frame
	for i := range a.Frames {
		if a.Frames[i].Kind == FrameThinking && a.Frames[i].Final {
			finalThinking = &a.Frames[i]
			break
		}
	}
	if finalThinking == nil {
		t.Fatalf("expected a final thinking frame, got %#v", a.Frames)
	}
	if finalThinking.Duration <= 0 {
		t.Fatalf("expected final thinking Duration > 0, got %v", finalThinking.Duration)
	}

	// Second reasoning block: a NEW start time, so its duration is independent.
	time.Sleep(5 * time.Millisecond)
	r2 := r.Reduce(NewMessageMsg{Msg: Message{Kind: MsgKindReasoning, Content: "second thought"}})
	_ = r2
	a2 := r.Reduce(NewMessageMsg{Msg: Message{Kind: MsgKindAssistant, Content: "answer2"}})
	var secondThinking *Frame
	for i := range a2.Frames {
		if a2.Frames[i].Kind == FrameThinking && a2.Frames[i].Final {
			secondThinking = &a2.Frames[i]
			break
		}
	}
	if secondThinking == nil {
		t.Fatalf("expected a second final thinking frame, got %#v", a2.Frames)
	}
	if secondThinking.Duration <= 0 {
		t.Fatalf("expected second thinking Duration > 0, got %v", secondThinking.Duration)
	}
}

func TestReducerComputesWorkedDurationFromLocalRunTimingFallback(t *testing.T) {
	var r Reducer
	_ = r.Reduce(RunStartedMsg{RunID: "r1"})
	r.activeRunStartedAt = time.Now().Add(-62*time.Second - 200*time.Millisecond)
	_ = r.Reduce(NewMessageMsg{Msg: Message{Kind: MsgKindTool, StepID: "read-1"}})
	_ = r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindAssistant,
		Content: "ok",
	}})
	ev := r.Reduce(RunEndedMsg{RunID: "r1"})
	if len(ev.Frames) != 1 {
		t.Fatalf("expected only assistant frame, got %#v", ev.Frames)
	}
	if !strings.HasPrefix(ev.WorkedStatus, "Worked for 1m 02s") {
		t.Fatalf("expected fallback worked duration around 62s, got %q", ev.WorkedStatus)
	}
}

// A picker a command asked for opens at once, with no line announcing it.
func TestReducerPickerRequestsOpenWithoutAFrame(t *testing.T) {
	var r Reducer
	picker := &turn.Picker{Command: "model"}
	if out := r.Reduce(SlashPickerRequestedMsg{Picker: picker}); out.RequestPicker != picker || len(out.Frames) != 0 {
		t.Fatalf("picker request = %#v", out)
	}
	if perm := r.Reduce(PermissionManagementRequestedMsg{}); !perm.RequestPermissionMgmt || len(perm.Frames) != 0 {
		t.Fatalf("permissions request = %#v", perm)
	}
	if skill := r.Reduce(SkillSelectionRequestedMsg{}); !skill.RequestSkillSelect || len(skill.Frames) != 0 {
		t.Fatalf("skills request = %#v", skill)
	}
	if sessions := r.Reduce(SessionSelectionRequestedMsg{}); !sessions.RequestSessionSelect || len(sessions.Frames) != 0 {
		t.Fatalf("sessions request = %#v", sessions)
	}
}

func TestReducerSubagentSpawnedEmitsStatusFrameWithAgentID(t *testing.T) {
	var r Reducer
	_ = r.Reduce(RunStartedMsg{RunID: "parent-1"})
	out := r.Reduce(SubagentSpawnedMsg{
		AgentID:   "task-7",
		AgentType: "general-purpose",
		TaskID:    "task-7",
	})
	if len(out.Frames) != 1 {
		t.Fatalf("expected one frame, got %#v", out.Frames)
	}
	f := out.Frames[0]
	// The dispatch with no call opens a card of its own: the conversation's
	// account of this agent, and its task row is the way into its view.
	if f.Kind != FrameFanout {
		t.Fatalf("expected FrameFanout, got %s", f.Kind)
	}
	if got := f.FanoutLineAgents[0]; got != "task-7" {
		t.Fatalf("expected the task row to open task-7's view, got %q", got)
	}
	if f.AgentID != "" {
		t.Fatalf("the card belongs to the conversation, got AgentID=%q", f.AgentID)
	}
	if f.RunID != "parent-1" {
		t.Fatalf("expected RunID=parent-1, got %q", f.RunID)
	}
	if !strings.Contains(f.Summary, "general-purpose") {
		t.Fatalf("unexpected title %q", f.Title)
	}
	if !strings.Contains(f.Content, "general-purpose") {
		t.Fatalf("the card must name the agent by its type when nothing else did, got %q", f.Content)
	}
}

func TestReducerMaintainsSubagentRosterRows(t *testing.T) {
	var r Reducer
	_ = r.Reduce(SubagentSpawnedMsg{
		AgentID:   "agent-1",
		AgentType: "review",
		TaskID:    "task-1",
	})
	snap := r.AgentRosterSnapshot()
	if len(snap.Rows) != 1 {
		t.Fatalf("expected one roster row, got %#v", snap.Rows)
	}
	if snap.Rows[0].ID != "agent-1" {
		t.Fatalf("expected ID agent-1, got %#v", snap.Rows[0])
	}
	if snap.Rows[0].Kind != "subagent" {
		t.Fatalf("expected subagent row, got %#v", snap.Rows[0])
	}
	if snap.Rows[0].Label != "review" {
		t.Fatalf("expected label review, got %#v", snap.Rows[0])
	}
	if snap.Rows[0].Status != "running" {
		t.Fatalf("expected running status, got %#v", snap.Rows[0])
	}

	// Finished subagents are removed from the roster.
	_ = r.Reduce(SubagentEndedMsg{
		AgentID:   "agent-1",
		AgentType: "review",
		TaskID:    "task-1",
		Status:    "cancelled",
	})
	snap = r.AgentRosterSnapshot()
	if len(snap.Rows) != 0 {
		t.Fatalf("expected zero roster rows after end, got %#v", snap.Rows)
	}
}

func TestReducerSubagentEndedRemovesRosterRow(t *testing.T) {
	var r Reducer
	_ = r.Reduce(SubagentSpawnedMsg{
		AgentID:   "agent-1",
		AgentType: "explore",
	})
	_ = r.Reduce(SubagentSpawnedMsg{
		AgentID:   "agent-2",
		AgentType: "explore",
	})
	snap := r.AgentRosterSnapshot()
	if len(snap.Rows) != 2 {
		t.Fatalf("expected two roster rows, got %d", len(snap.Rows))
	}
	// End one; only the other should survive.
	_ = r.Reduce(SubagentEndedMsg{
		AgentID: "agent-1",
		Status:  "done",
	})
	snap = r.AgentRosterSnapshot()
	if len(snap.Rows) != 1 {
		t.Fatalf("expected one roster row after partial end, got %d", len(snap.Rows))
	}
	if snap.Rows[0].ID != "agent-2" {
		t.Fatalf("expected agent-2, got %q", snap.Rows[0].ID)
	}
	// End the last one; roster should be empty.
	_ = r.Reduce(SubagentEndedMsg{
		AgentID: "agent-2",
		Status:  "done",
	})
	snap = r.AgentRosterSnapshot()
	if len(snap.Rows) != 0 {
		t.Fatalf("expected zero roster rows after both ended, got %d", len(snap.Rows))
	}
}

func TestReducerSubagentEndedEmitsStatusFrameWithError(t *testing.T) {
	var r Reducer
	out := r.Reduce(SubagentEndedMsg{
		AgentID:   "task-9",
		AgentType: "verification",
		TaskID:    "task-9",
		Status:    "failed",
		Error:     "tool timed out",
	})
	// Two frames: the error the subagent's own view ends on, and the card the
	// primary transcript keeps as the way back into that view.
	if len(out.Frames) != 2 {
		t.Fatalf("expected two frames, got %#v", out.Frames)
	}
	closing := out.Frames[0]
	if closing.Kind != FrameError {
		t.Fatalf("expected FrameError first, got %s", closing.Kind)
	}
	if closing.AgentID != "task-9" {
		t.Fatalf("expected AgentID=task-9 on the closing frame, got %q", closing.AgentID)
	}
	if !strings.Contains(closing.Content, "tool timed out") {
		t.Fatalf("expected error text in the closing frame, got %q", closing.Content)
	}
	f := out.Frames[1]
	if f.Kind != FrameFanout {
		t.Fatalf("expected the card, got %s", f.Kind)
	}
	if f.AgentID != "" {
		t.Fatalf("the card belongs to the conversation, got AgentID=%q", f.AgentID)
	}
	if got := f.FanoutLineAgents[0]; got != "task-9" {
		t.Fatalf("expected the task row to open task-9's view, got %q", got)
	}
	if f.Summary != "Ran 1 verification task · 0 done, 1 failed" {
		t.Fatalf("expected the failure counted on the card, got %q", f.Summary)
	}
	if !strings.Contains(f.Content, "tool timed out") {
		t.Fatalf("expected error text in content, got %q", f.Content)
	}
}

// A subagent that finishes cleanly needs no closing error frame: its own final
// answer is the ending its view shows.
func TestReducerSubagentEndedOKEmitsNoClosingErrorFrame(t *testing.T) {
	var r Reducer
	out := r.Reduce(SubagentEndedMsg{
		AgentID:   "task-ok",
		AgentType: "verification",
		TaskID:    "task-ok",
		Status:    "ok",
	})
	for _, f := range out.Frames {
		if f.Kind == FrameError {
			t.Fatalf("a clean subagent must not emit an error frame, got %#v", f)
		}
	}
}

// The plan reviewer's view closes the way every subagent's does: a "Worked
// for" line in its own view, stating how long this execution ran. It is the
// same code path any other agent takes — the reviewer differs only in who
// dispatched it — so this pins the equality the owner asked for.
func TestPlanReviewerViewClosesWithWorkedFor(t *testing.T) {
	tracker := NewTracker()
	var red Reducer
	red.WithTracker(tracker)
	started := time.Now().Add(-2 * time.Minute)
	red.Reduce(SubagentSpawnedMsg{
		AgentID: "subagent-e358", AgentType: "plan-reviewer", TaskID: "subagent-e358",
		Title: "Plan review", Task: "review the plan", ExecutionID: "exec-1", Timestamp: started,
	})
	var worked *Frame
	for _, f := range red.Reduce(SubagentEndedMsg{
		AgentID: "subagent-e358", AgentType: "plan-reviewer", TaskID: "subagent-e358",
		Status: "ok", ExecutionID: "exec-1", Timestamp: started.Add(90 * time.Second),
	}).Frames {
		if f.Kind == FrameStatus && strings.HasPrefix(f.Title, "Worked for") {
			worked = &f
		}
	}
	if worked == nil {
		t.Fatal("the plan reviewer's view must close with a Worked for line")
	}
	if worked.AgentID != "subagent-e358" {
		t.Fatalf("the Worked for line belongs to the reviewer's own view, got agent %q", worked.AgentID)
	}
	if !strings.Contains(worked.Title, "1m 30s") {
		t.Fatalf("worked line = %q, want the duration of this execution", worked.Title)
	}
}

// A subagent's clock belongs to one execution, not to its identity: a second
// execution (a continue, a user-sent message in its view) must run with a
// Working line of its own and close with a Worked for line whose duration
// counts only that execution, leaving the idle gap between the two out.
func TestContinuedSubagentGetsAClockPerExecution(t *testing.T) {
	const agentID = "subagent-77"
	tracker := NewTracker()
	var red Reducer
	red.WithTracker(tracker)
	base := time.Now().Add(-11*time.Minute - 5*time.Second)

	red.Reduce(SubagentSpawnedMsg{AgentID: agentID, AgentType: "explore", TaskID: agentID, ExecutionID: "exec-1", Timestamp: base})
	red.Reduce(SubagentEndedMsg{AgentID: agentID, AgentType: "explore", TaskID: agentID, Status: "ok", ExecutionID: "exec-1", Timestamp: base.Add(time.Minute)})

	// The checklist spans executions: it belongs to the agent, not the run.
	red.Reduce(PlanUpdatedMsg{Payload: event.PlanUpdatedPayload{
		AgentID: agentID, Completed: 2, Total: 5,
		Items: []event.PlanUpdateItem{{Content: "list files", Status: "in_progress"}},
	}})

	secondStart := base.Add(11 * time.Minute)
	red.Reduce(SubagentSpawnedMsg{AgentID: agentID, AgentType: "explore", TaskID: agentID, ExecutionID: "exec-2", Timestamp: secondStart})

	snap, ok := tracker.SnapshotAgentRun(agentID)
	if !ok {
		t.Fatal("the second execution must have a working snapshot")
	}
	if snap.Ended {
		t.Fatal("the second execution must report the agent as running, not still ended from the first")
	}
	if elapsed := snap.Elapsed; elapsed > time.Minute {
		t.Fatalf("second execution clock = %v, want it counted from the second start", elapsed)
	}
	if counters := formatWorkingCounters(snap.Counters); !strings.Contains(counters, "☑2/5") {
		t.Fatalf("the working line's counters = %q, want the agent's own checklist progress", counters)
	}

	var worked *Frame
	for _, f := range red.Reduce(SubagentEndedMsg{
		AgentID: agentID, AgentType: "explore", TaskID: agentID, Status: "ok", ExecutionID: "exec-2",
		Timestamp: secondStart.Add(30 * time.Second),
	}).Frames {
		if f.Kind == FrameStatus && strings.HasPrefix(f.Title, "Worked for") {
			worked = &f
		}
	}
	if worked == nil {
		t.Fatal("the second execution must close with its own Worked for line")
	}
	if !strings.Contains(worked.Title, "Worked for 30s") {
		t.Fatalf("worked line = %q, want only the second execution's 30s", worked.Title)
	}
	if !strings.Contains(worked.Title, "☑2/5") {
		t.Fatalf("worked line = %q, want the checklist progress it ended with", worked.Title)
	}
}

func TestReducerSubagentSpawnedIncrementsTrackerAgents(t *testing.T) {
	tr := NewTracker()
	r := (&Reducer{}).WithTracker(tr)
	_ = r.Reduce(SubagentSpawnedMsg{AgentID: "a-1", AgentType: "general"})
	_ = r.Reduce(SubagentSpawnedMsg{AgentID: "a-2", AgentType: "general"})
	_ = r.Reduce(SubagentSpawnedMsg{AgentID: "a-1", AgentType: "general"}) // dedup
	snap := tr.SnapshotSession()
	if snap.Agents != 2 {
		t.Fatalf("expected 2 unique agents, got %d", snap.Agents)
	}
}

func TestReducerFanoutTaskToolFolding(t *testing.T) {
	var r Reducer
	// Step 1: start a subagent_run (fanout-of-1) with a task.
	got := r.Reduce(NewMessageMsg{Msg: subagentStepMessage("fanout-step-1", tool.StepEvent{
		Kind: tool.StepKindToolStarted, ToolName: "subagent_run",
		Input: map[string]any{
			"task":  "investigate auth bug",
			"title": "Auth Bug Investigation",
		},
	})}).Frames
	if len(got) != 1 || got[0].Kind != FrameFanout {
		t.Fatalf("expected FrameFanout, got %#v", got)
	}
	// Check the tasks were parsed.
	if got[0].Summary != "Running 1 fork task…" {
		t.Fatalf("summary = %q, want Running 1 fork task…", got[0].Summary)
	}

	// Step 2: spawn the subagent the dispatching call names.
	_ = r.Reduce(SubagentSpawnedMsg{
		AgentID:          "task-agent-1",
		AgentType:        "explore",
		TaskID:           "task-agent-1",
		ParentToolCallID: "fanout-step-1",
		TaskIndex:        0,
	})

	// Step 3: send a tool_started event for the subagent that belongs to the fanout.
	got2 := r.Reduce(NewMessageMsg{Msg: Message{
		Kind:      MsgKindTool,
		StepID:    "tool-step-1",
		ToolName:  "example_tool",
		ToolMeta:  tool.ToolMeta{Invocation: "example auth"},
		AgentID:   "task-agent-1",
		ToolPhase: "tool_call_started",
	}}).Frames
	// Should emit: a FrameTool (for per-agent VM) + FrameFanout (for main surface).
	if len(got2) != 2 {
		t.Fatalf("expected 2 frames (FrameTool + FrameFanout), got %d", len(got2))
	}
	fanoutIdx := 0
	if got2[0].Kind != FrameFanout {
		fanoutIdx = 1
	}
	if got2[fanoutIdx].Kind != FrameFanout {
		t.Fatalf("expected FrameFanout in result, got %#v", got2)
	}
	if !strings.Contains(got2[fanoutIdx].Content, "example auth") {
		t.Fatalf("FrameFanout.Content missing tool label: %q", got2[fanoutIdx].Content)
	}

	// Step 4: send tool_completed for same step → label should be in content, not double-counted.
	got3 := r.Reduce(NewMessageMsg{Msg: Message{
		Kind:      MsgKindTool,
		StepID:    "tool-step-1",
		ToolName:  "example_tool",
		ToolMeta:  tool.ToolMeta{Invocation: "example auth"},
		AgentID:   "task-agent-1",
		ToolPhase: "tool_call_completed",
	}}).Frames
	if len(got3) != 2 {
		t.Fatalf("expected 2 frames on completed, got %d", len(got3))
	}
	fanoutIdx3 := 0
	if got3[0].Kind != FrameFanout {
		fanoutIdx3 = 1
	}
	if strings.Count(got3[fanoutIdx3].Content, "example auth") != 1 {
		t.Fatalf("expected single tool entry after started+completed dedup: %q", got3[fanoutIdx3].Content)
	}
}

func TestReducerFanoutTaskToolRingBufferCap(t *testing.T) {
	var r Reducer
	// Start a fanout.
	_ = r.Reduce(NewMessageMsg{Msg: subagentStepMessage("fanout-step-buf", tool.StepEvent{
		Kind: tool.StepKindToolStarted, ToolName: "subagent_run",
		Input: map[string]any{
			"task":  "investigate",
			"title": "Investigation",
		},
	})})
	_ = r.Reduce(SubagentSpawnedMsg{
		AgentID: "agent-buf-1", AgentType: "explore", TaskID: "agent-buf-1",
		ParentToolCallID: "fanout-step-buf", TaskIndex: 0,
	})

	// Send 6 started events. Ring buffer caps at 4; ToolTotal should be 6.
	for i := 0; i < 6; i++ {
		_ = r.Reduce(NewMessageMsg{Msg: Message{
			Kind:      MsgKindTool,
			StepID:    fmt.Sprintf("tool-step-%d", i),
			ToolName:  "example_tool",
			ToolMeta:  tool.ToolMeta{Invocation: fmt.Sprintf("example %d", i)},
			AgentID:   "agent-buf-1",
			ToolPhase: "tool_call_started",
		}})
	}
	// Now send completed for the first 2 tools (displaced from ring buffer).
	_ = r.Reduce(NewMessageMsg{Msg: Message{
		Kind:      MsgKindTool,
		StepID:    "tool-step-0",
		ToolName:  "example_tool",
		ToolMeta:  tool.ToolMeta{Invocation: "example 0"},
		AgentID:   "agent-buf-1",
		ToolPhase: "tool_call_completed",
	}})
	_ = r.Reduce(NewMessageMsg{Msg: Message{
		Kind:      MsgKindTool,
		StepID:    "tool-step-1",
		ToolName:  "example_tool",
		ToolMeta:  tool.ToolMeta{Invocation: "example 1"},
		AgentID:   "agent-buf-1",
		ToolPhase: "tool_call_completed",
	}})

	// Find the fanout state to verify ToolTotal.
	fs := r.findFanoutByStepID("fanout-step-buf")
	if fs == nil {
		t.Fatal("fanout state not found")
	}
	if len(fs.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(fs.Tasks))
	}
	task := fs.Tasks[0]
	if task.ToolTotal != 6 {
		t.Fatalf("ToolTotal = %d, want 6 (no double-count from displaced completed)", task.ToolTotal)
	}
	if len(task.RecentTools) != 1 {
		t.Fatalf("RecentTools len = %d, want 1 (ring buffer cap)", len(task.RecentTools))
	}
}

func TestReducerFanoutTaskTokenUsageFolding(t *testing.T) {
	tracker := NewTracker()
	var r Reducer
	r.WithTracker(tracker)
	_ = r.Reduce(RunStartedMsg{RunID: "run-tok"})
	_ = r.Reduce(NewMessageMsg{Msg: subagentStepMessage("fanout-tok", tool.StepEvent{
		Kind: tool.StepKindToolStarted, ToolName: "subagent_run",
		Input: map[string]any{"task": "investigate", "title": "Investigation"},
	})})
	_ = r.Reduce(SubagentSpawnedMsg{
		AgentID: "agent-tok-1", AgentType: "explore", TaskID: "agent-tok-1",
		ParentToolCallID: "fanout-tok", TaskIndex: 0,
	})

	// Subagent-tagged usage deltas fold into the parent run's token totals
	// through the tracker's subagent accumulator. They repaint nothing: the
	// card shows no token count, so a delta has nothing to add to it.
	got := r.Reduce(TokenUsageDeltaMsg{AgentID: "agent-tok-1", InputTokens: 1200, OutputTokens: 300}).Frames
	if len(got) != 0 {
		t.Fatalf("a usage delta must not repaint the card, got %#v", got)
	}
	_ = r.Reduce(TokenUsageDeltaMsg{AgentID: "agent-tok-1", InputTokens: 500, OutputTokens: 0})

	fs := r.findFanoutByStepID("fanout-tok")
	if fs == nil || len(fs.Tasks) != 1 {
		t.Fatalf("fanout state missing: %#v", fs)
	}

	// Subagent tokens must land on the parent run's Working line and the
	// footer session total.
	if live := tracker.SnapshotActiveRun(); live.InputTokens != 1700 || live.OutputTokens != 300 {
		t.Fatalf("active-run = %+v, want in=1700 out=300 (subagent folded into parent run)", live)
	}
	if sess := tracker.SnapshotSession(); sess.InputTokens != 1700 || sess.OutputTokens != 300 {
		t.Fatalf("session = %+v, want in=1700 out=300 (subagent folded into footer total)", sess)
	}

	// After the task completes, its own view is closed by a "Worked for" line
	// (the token spend is tracked but not painted on the card). This task saw
	// no observed tool call, so the card body has no third-layer rows at all.
	end := r.Reduce(SubagentEndedMsg{
		AgentID: "agent-tok-1", AgentType: "explore", TaskID: "agent-tok-1", Status: "done",
		ParentToolCallID: "fanout-tok", TaskIndex: 0,
	}).Frames
	if len(end) != 2 || end[0].Kind != FrameStatus || end[0].AgentID != "agent-tok-1" || end[1].Kind != FrameFanout {
		t.Fatalf("expected the subagent's closing status then the card on end, got %#v", end)
	}
	if !strings.HasPrefix(end[0].Title, "Worked for ") || !workedCompletionSuffix.MatchString(end[0].Title) {
		t.Fatalf("closing status = %q, want duration and completion time", end[0].Title)
	}
	if strings.Contains(end[1].Content, "tokens") || strings.Contains(end[1].Content, "tool uses") {
		t.Fatalf("fanout body = %q, want no stats line (no tool uses, no token count)", end[1].Content)
	}

	// An untagged (primary) delta folds into the run, not a task.
	_ = r.Reduce(TokenUsageDeltaMsg{RunID: "run-tok", InputTokens: 10, OutputTokens: 10})
	if live := tracker.SnapshotActiveRun(); live.InputTokens != 1710 {
		t.Fatalf("primary delta missing from the run: %+v", live)
	}
}

func TestReducerMapsSubagentLifecycleSystemMessage(t *testing.T) {
	var r Reducer
	out := r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindSystem,
		Content: "subagent_completed: verification [task-1] status=ok",
	}})
	if len(out.Frames) != 1 {
		t.Fatalf("expected one frame, got %#v", out.Frames)
	}
	if out.Frames[0].Kind != FrameSystem {
		t.Fatalf("expected system frame, got %#v", out.Frames[0])
	}
	if out.Frames[0].Content != "subagent_completed: verification [task-1] status=ok" {
		t.Fatalf("unexpected subagent lifecycle content: %#v", out.Frames[0])
	}
}

// ---- plan 013: every subagent_* call draws the same card ----

// subagentStepMessage builds the MsgKindTool message the live step hook emits
// for one subagent_* call, so a test's ToolMeta carries the facts the
// production path derives (012's SubagentCallFromStep inside BuildToolMeta).
func subagentStepMessage(stepID string, evt tool.StepEvent) Message {
	evt.StepID = strings.TrimSpace(stepID)
	meta := tool.BuildToolMeta(evt)
	body, _ := tool.FormatToolStepResult(evt, tool.DefaultMaxFormattedBody)
	return Message{
		Kind:      MsgKindTool,
		StepID:    stepID,
		ToolName:  evt.ToolName,
		ToolMeta:  meta,
		Content:   body,
		ToolPhase: evt.Kind,
		Duration:  evt.Duration,
	}
}

// lastFanoutFrame returns the last FrameFanout of a reduction, the card the
// later asserts read.
func lastFanoutFrame(t *testing.T, ev EventResult) Frame {
	t.Helper()
	var out Frame
	for _, f := range ev.Frames {
		if f.Kind == FrameFanout {
			out = f
		}
	}
	if out.Kind != FrameFanout {
		t.Fatalf("no FrameFanout in %#v", ev.Frames)
	}
	return out
}

// fanoutContentLines splits a card's content into ANSI-stripped lines.
func fanoutContentLines(content string) []string {
	raw := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		out = append(out, stripANSI(line))
	}
	return out
}

// assertNoStatsLine guards T21: no content line is a "· N tool uses" stats row.
func assertNoStatsLine(t *testing.T, content string) {
	t.Helper()
	for _, line := range fanoutContentLines(content) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "· ") && strings.HasSuffix(trimmed, "tool uses") {
			t.Fatalf("stats row must not appear: %q in\n%s", line, content)
		}
	}
}

// renderFanoutForTest paints one card and returns its ANSI-stripped lines.
func renderFanoutForTest(f Frame) []string {
	var buf bytes.Buffer
	r := NewRenderer(&buf, &buf)
	r.renderFanout(f)
	return strings.Split(strings.TrimRight(stripANSI(buf.String()), "\n"), "\n")
}

func TestSendCardFollowsItsAgentToTheEnd(t *testing.T) {
	t0 := time.Unix(1790000000, 0).UTC()
	input := map[string]any{
		"title": "计划001 Go车道实施", "task": "执行 docs/plan/SRT/001 的 Go 部分",
		"subagent_type": "general-purpose",
	}
	var r Reducer
	var frames []Frame
	collect := func(ev EventResult) {
		frames = append(frames, ev.Frames...)
	}
	collect(r.Reduce(NewMessageMsg{Msg: subagentStepMessage("send-1", tool.StepEvent{
		Kind: tool.StepKindToolStarted, ToolName: "subagent_send", Input: input,
	})}))
	collect(r.Reduce(SubagentSpawnedMsg{
		AgentID: "subagent-6e5c", AgentType: "general-purpose", TaskID: "subagent-6e5c",
		Title: "计划001 Go车道实施", Task: "执行 docs/plan/SRT/001 的 Go 部分",
		ParentToolCallID: "send-1", TaskIndex: 0, ExecutionID: "exec-1", Timestamp: t0,
	}))
	for i, label := range []string{"read README.md", "shell gofmt -l pkg cmd"} {
		collect(r.Reduce(NewMessageMsg{Msg: Message{
			Kind: MsgKindTool, StepID: fmt.Sprintf("agent-step-%d", i), ToolName: "shell",
			ToolMeta: tool.ToolMeta{Invocation: label}, AgentID: "subagent-6e5c",
			ToolPhase: tool.StepKindToolStarted,
		}}))
	}
	collect(r.Reduce(NewMessageMsg{Msg: subagentStepMessage("send-1", tool.StepEvent{
		Kind: tool.StepKindToolCompleted, ToolName: "subagent_send", Input: input, Duration: 20 * time.Millisecond,
		Output: map[string]any{"output": `{"task_id":"subagent-6e5c","run_id":"exec-1","status":"running","started_at":1790000000,"agent_type":"general-purpose"}`},
	})}))
	collect(r.Reduce(SubagentEndedMsg{
		AgentID: "subagent-6e5c", AgentType: "general-purpose", TaskID: "subagent-6e5c",
		Status: "ok", ParentToolCallID: "send-1", TaskIndex: 0, ExecutionID: "exec-1",
		FinishedAt: t0.Add(843 * time.Second), Timestamp: t0.Add(843 * time.Second),
	}))

	for _, f := range frames {
		if f.Kind == FrameStatus {
			t.Fatalf("the card replaces the lifecycle text lines, got %#v", f)
		}
	}
	card := Frame{}
	for _, f := range frames {
		if f.Kind == FrameFanout {
			card = f
		}
	}
	if card.Kind != FrameFanout {
		t.Fatalf("no send card was drawn: %#v", frames)
	}
	if card.Summary != "Ran 1 general-purpose task in background" {
		t.Fatalf("summary = %q, want Ran 1 general-purpose task in background", card.Summary)
	}
	if !strings.Contains(stripANSI(card.Content), "计划001 Go车道实施") {
		t.Fatalf("card body lacks the task name:\n%s", card.Content)
	}
	if !strings.Contains(card.Content, "… +1 tool uses") {
		t.Fatalf("card body lacks the tool overflow row:\n%s", card.Content)
	}
	if strings.Contains(card.Content, "{") {
		t.Fatalf("card body must not contain JSON:\n%s", card.Content)
	}
	assertNoStatsLine(t, card.Content)
	// The single task's clock came from the lifecycle events, not the 20ms call.
	clock := fanoutClockOfLine(card, fanoutContentLines(card.Content), "计划001 Go车道实施")
	if !clock.Start.Equal(t0) || !clock.End.Equal(t0.Add(843*time.Second)) {
		t.Fatalf("task clock = %+v, want the execution's start and stop", clock)
	}
}

func fanoutClockOfLine(f Frame, plainLines []string, needle string) fanoutLineClock {
	var zero fanoutLineClock
	for i, line := range plainLines {
		if strings.Contains(line, needle) && i < len(f.FanoutLineClocks) {
			return f.FanoutLineClocks[i]
		}
	}
	return zero
}

func TestQueryCardsShowTaskRowsNotJSON(t *testing.T) {
	record := func(status string, started, finished int64) string {
		return `{"task_id":"subagent-6e5c","agent_type":"general-purpose","agent_kind":"typed",` +
			`"title":"计划001 Go车道实施","task":"你在仓库执行 docs/plan 的 Go 部分。先完整阅读该计划文件。",` +
			`"status":"` + status + `","execution_id":"exec-1","started_at":` + strconv.FormatInt(started, 10) +
			`,"finished_at":` + strconv.FormatInt(finished, 10) + `}`
	}
	cases := []struct {
		name    string
		tool    string
		input   map[string]any
		output  string
		running string
		done    string
		rows    []string
		noJSON  string
	}{
		{
			name: "status", tool: "subagent_status", input: map[string]any{"task_id": "subagent-6e5c"},
			output:  record("ok", 1790000000, 1790000843),
			running: "Checking 1 task…", done: "Checked 1 general-purpose task",
			rows: []string{"✓ 计划001 Go车道实施"},
		},
		{
			name: "wait timed out", tool: "subagent_wait", input: map[string]any{"task_id": "subagent-6e5c", "timeout_ms": 1000},
			output:  `{"record":` + record("running", 1790000000, 0) + `,"timed_out":true}`,
			running: "Waiting for 1 task…", done: "Waited for 1 general-purpose task",
			rows: []string{"计划001 Go车道实施", "· still running when the wait ended"},
		},
		{
			name: "close", tool: "subagent_close", input: map[string]any{"task_id": "subagent-6e5c"},
			output:  `{"status":"cancel_requested","record":` + record("running", 1790000000, 0) + `}`,
			running: "Stopping 1 task…", done: "Stopped 1 general-purpose task",
			rows: []string{"计划001 Go车道实施", "· stop requested"},
		},
		{
			name: "list", tool: "subagent_list", input: map[string]any{"limit": 20},
			output: `{"records":[` + record("ok", 1790000000, 1790000843) + `,` +
				record("running", 1790000100, 0) + `,` +
				record("failed", 1790000000, 1790000128) + `]}`,
			running: "Listing tasks…", done: "Listed 3 tasks",
			rows: []string{
				"✓ 计划001 Go车道实施 · 14m3s", "· general-purpose",
				"计划001 Go车道实施", "· general-purpose",
				"✗ 计划001 Go车道实施 · 2m8s", "· general-purpose",
			},
		},
		{
			name: "empty list", tool: "subagent_list", input: map[string]any{"limit": 20},
			output: `{"records":[]}`, running: "Listing tasks…", done: "Listed 0 tasks",
			rows: []string{"(no output)"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r Reducer
			start := lastFanoutFrame(t, r.Reduce(NewMessageMsg{Msg: subagentStepMessage("q-1", tool.StepEvent{
				Kind: tool.StepKindToolStarted, ToolName: tc.tool, Input: tc.input,
			})}))
			if start.Summary != tc.running {
				t.Fatalf("running header = %q, want %q", start.Summary, tc.running)
			}
			card := lastFanoutFrame(t, r.Reduce(NewMessageMsg{Msg: subagentStepMessage("q-1", tool.StepEvent{
				Kind: tool.StepKindToolCompleted, ToolName: tc.tool, Input: tc.input,
				Output: map[string]any{"output": tc.output}, Duration: 4100 * time.Millisecond,
			})}))
			if card.Summary != tc.done {
				t.Fatalf("settled header = %q, want %q", card.Summary, tc.done)
			}
			if !card.Final {
				t.Fatalf("a settled query card must be Final")
			}
			painted := renderFanoutForTest(card)
			for _, want := range tc.rows {
				found := false
				for _, line := range painted {
					if strings.TrimSpace(line) == want || strings.Contains(strings.TrimSpace(line), want) {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("card lacks row %q:\n%s", want, strings.Join(painted, "\n"))
				}
			}
			for _, line := range append(fanoutContentLines(card.Content), painted...) {
				if strings.Contains(line, "{") || strings.Contains(line, `"agent_id"`) {
					t.Fatalf("card row is JSON: %q", line)
				}
				if strings.Contains(line, "先完整阅读该计划文件") {
					t.Fatalf("card row leaks the record's prompt: %q", line)
				}
			}
		})
	}
}

func TestRunCardNamesOneTaskInTheSingular(t *testing.T) {
	input := map[string]any{"task": "追踪模型选择器", "subagent_type": "general-purpose"}
	var r Reducer
	start := lastFanoutFrame(t, r.Reduce(NewMessageMsg{Msg: subagentStepMessage("run-1", tool.StepEvent{
		Kind: tool.StepKindToolStarted, ToolName: "subagent_run", Input: input,
	})}))
	if start.Summary != "Running 1 general-purpose task…" {
		t.Fatalf("running header = %q, want Running 1 general-purpose task…", start.Summary)
	}
	done := lastFanoutFrame(t, r.Reduce(NewMessageMsg{Msg: subagentStepMessage("run-1", tool.StepEvent{
		Kind: tool.StepKindToolCompleted, ToolName: "subagent_run", Input: input,
		Output: map[string]any{"output": `{"task_id":"subagent-1a2b","status":"ok","finished_at":1790000182,"agent_type":"general-purpose"}`},
	})}))
	if done.Summary != "Ran 1 general-purpose task" {
		t.Fatalf("settled header = %q, want Ran 1 general-purpose task", done.Summary)
	}
}

func TestFanoutCardTakesSkippedOutcomesFromTheCallFacts(t *testing.T) {
	t0 := time.Unix(1790000000, 0).UTC()
	input := map[string]any{"tasks": []any{
		map[string]any{"prompt": "迁移导入", "subagent_type": "general-purpose"},
		map[string]any{"prompt": "", "subagent_type": "general-purpose"},
	}}
	var r Reducer
	_ = r.Reduce(NewMessageMsg{Msg: subagentStepMessage("fan-1", tool.StepEvent{
		Kind: tool.StepKindToolStarted, ToolName: "subagent_fanout", Input: input,
	})})
	_ = r.Reduce(SubagentSpawnedMsg{
		AgentID: "subagent-8c5c", AgentType: "general-purpose", TaskID: "subagent-8c5c",
		ParentToolCallID: "fan-1", TaskIndex: 0, ExecutionID: "exec-a", Timestamp: t0,
	})
	_ = r.Reduce(SubagentEndedMsg{
		AgentID: "subagent-8c5c", TaskID: "subagent-8c5c", Status: "ok",
		ParentToolCallID: "fan-1", TaskIndex: 0, ExecutionID: "exec-a", Timestamp: t0.Add(time.Minute),
	})
	card := lastFanoutFrame(t, r.Reduce(NewMessageMsg{Msg: subagentStepMessage("fan-1", tool.StepEvent{
		Kind: tool.StepKindToolCompleted, ToolName: "subagent_fanout", Input: input,
		Output: map[string]any{"output": `{"results":[{"index":0,"ok":true},{"index":1,"error":"skipped: empty prompt"}]}`},
	})}))
	if card.Summary != "Ran 2 general-purpose tasks · 1 done, 1 skipped" {
		t.Fatalf("summary = %q, want Ran 2 general-purpose tasks · 1 done, 1 skipped", card.Summary)
	}
	lines := fanoutContentLines(card.Content)
	sawEmpty, sawReason := false, false
	for _, line := range lines {
		if strings.TrimSpace(line) == "! (empty task)" {
			sawEmpty = true
		}
		if strings.TrimSpace(line) == "skipped: empty prompt" {
			sawReason = true
		}
	}
	if !sawEmpty || !sawReason {
		t.Fatalf("card lacks the skipped row or its reason:\n%s", card.Content)
	}
}

func TestFanoutCardShowsWhyTheCallFailed(t *testing.T) {
	input := map[string]any{"tasks": []any{
		map[string]any{"prompt": "迁移导入", "subagent_type": "general-purpose"},
		map[string]any{"prompt": "扩展目录", "subagent_type": "general-purpose"},
	}}
	const why = "subagent capacity is 0; main agent must execute this fanout directly"
	var r Reducer
	_ = r.Reduce(NewMessageMsg{Msg: subagentStepMessage("fan-fail", tool.StepEvent{
		Kind: tool.StepKindToolStarted, ToolName: "subagent_fanout", Input: input,
	})})
	card := lastFanoutFrame(t, r.Reduce(NewMessageMsg{Msg: subagentStepMessage("fan-fail", tool.StepEvent{
		Kind: tool.StepKindToolCompleted, ToolName: "subagent_fanout", Input: input, Error: why,
	})}))
	if card.Summary != "Failed to run 2 general-purpose tasks" {
		t.Fatalf("summary = %q, want Failed to run 2 general-purpose tasks", card.Summary)
	}
	if card.FanoutCallError != why {
		t.Fatalf("call error = %q, want %q", card.FanoutCallError, why)
	}
	painted := renderFanoutForTest(card)
	if got := strings.TrimSpace(painted[1]); got != why {
		t.Fatalf("first body line = %q, want the call error:\n%s", got, strings.Join(painted, "\n"))
	}
}

func TestCancelledTasksAreCountedOnTheCard(t *testing.T) {
	t0 := time.Unix(1790000000, 0).UTC()
	input := map[string]any{"tasks": []any{
		map[string]any{"prompt": "任务16", "subagent_type": "general-purpose"},
		map[string]any{"prompt": "任务17", "subagent_type": "general-purpose"},
		map[string]any{"prompt": "任务18", "subagent_type": "general-purpose"},
	}}
	var r Reducer
	_ = r.Reduce(NewMessageMsg{Msg: subagentStepMessage("fan-3", tool.StepEvent{
		Kind: tool.StepKindToolStarted, ToolName: "subagent_fanout", Input: input,
	})})
	for i, status := range []string{"ok", "cancelled", "cancelled"} {
		agent := fmt.Sprintf("subagent-%d", i)
		_ = r.Reduce(SubagentSpawnedMsg{
			AgentID: agent, AgentType: "general-purpose", TaskID: agent,
			ParentToolCallID: "fan-3", TaskIndex: i, ExecutionID: fmt.Sprintf("exec-%d", i), Timestamp: t0,
		})
		_ = r.Reduce(SubagentEndedMsg{
			AgentID: agent, TaskID: agent, Status: status,
			ParentToolCallID: "fan-3", TaskIndex: i, ExecutionID: fmt.Sprintf("exec-%d", i), Timestamp: t0.Add(time.Minute),
		})
	}
	card := lastFanoutFrame(t, r.Reduce(NewMessageMsg{Msg: subagentStepMessage("fan-3", tool.StepEvent{
		Kind: tool.StepKindToolCompleted, ToolName: "subagent_fanout", Input: input,
		Output: map[string]any{"output": `{"results":[{"index":0,"ok":true},{"index":1,"ok":true},{"index":2,"ok":true}]}`},
	})}))
	if card.Summary != "Ran 3 general-purpose tasks · 1 done, 2 cancelled" {
		t.Fatalf("summary = %q, want Ran 3 general-purpose tasks · 1 done, 2 cancelled", card.Summary)
	}
	for _, line := range fanoutContentLines(card.Content) {
		if strings.Contains(line, "任务17") || strings.Contains(line, "任务18") {
			if !strings.HasPrefix(strings.TrimSpace(line), "×") {
				t.Fatalf("cancelled row must carry ×: %q", line)
			}
		}
	}
}

func TestSpawnWithoutItsCallNeverJoinsAnotherCard(t *testing.T) {
	input := map[string]any{"tasks": []any{
		map[string]any{"prompt": "任务16 migrate 导入", "subagent_type": "general-purpose"},
		map[string]any{"prompt": "任务17 扩展目录", "subagent_type": "general-purpose"},
	}}
	var r Reducer
	before := lastFanoutFrame(t, r.Reduce(NewMessageMsg{Msg: subagentStepMessage("fan-2", tool.StepEvent{
		Kind: tool.StepKindToolStarted, ToolName: "subagent_fanout", Input: input,
	})}))
	ev := r.Reduce(SubagentSpawnedMsg{
		AgentID: "subagent-77c0", AgentType: "explore", TaskID: "subagent-77c0",
		Title: "查找所有调用点", ParentToolCallID: "other-call", TaskIndex: 0, ExecutionID: "exec-x",
		Timestamp: time.Unix(1790000000, 0).UTC(),
	})
	var foreign *Frame
	for i := range ev.Frames {
		if ev.Frames[i].Kind == FrameFanout {
			foreign = &ev.Frames[i]
		}
	}
	if foreign == nil {
		t.Fatalf("a dispatch without its call must open its own card: %#v", ev.Frames)
	}
	if !strings.HasPrefix(foreign.StepID, "subagent-exec:") {
		t.Fatalf("standalone card StepID = %q, want the subagent-exec: prefix", foreign.StepID)
	}
	if foreign.Summary != "Running 1 explore task…" {
		t.Fatalf("standalone card header = %q", foreign.Summary)
	}
	after := r.findFanoutByStepID("fan-2")
	if after == nil {
		t.Fatal("the fanout card vanished")
	}
	for i, task := range after.Tasks {
		if task.AgentID == "subagent-77c0" {
			t.Fatalf("the foreign agent joined task %d: %#v", i, task)
		}
	}
	for i, line := range fanoutContentLines(before.Content) {
		want := []string{"任务16 migrate 导入", "任务17 扩展目录"}
		if i < len(want) && strings.TrimSpace(line) != want[i] {
			t.Fatalf("fanout row %d = %q, want %q", i, line, want[i])
		}
	}
	for _, agent := range before.FanoutLineAgents {
		if agent == "subagent-77c0" {
			t.Fatalf("the foreign agent owns a fanout row: %#v", before.FanoutLineAgents)
		}
	}
}

func TestContinueLeavesTheOriginalCardAlone(t *testing.T) {
	t0 := time.Unix(1790000000, 0).UTC()
	input := map[string]any{"tasks": []any{
		map[string]any{"prompt": "任务16 migrate 导入", "subagent_type": "general-purpose"},
		map[string]any{"prompt": "任务17 扩展目录", "subagent_type": "general-purpose"},
	}}
	var r Reducer
	_ = r.Reduce(NewMessageMsg{Msg: subagentStepMessage("fan-4", tool.StepEvent{
		Kind: tool.StepKindToolStarted, ToolName: "subagent_fanout", Input: input,
	})})
	for i := range [2]int{} {
		agent := fmt.Sprintf("subagent-%d", i)
		_ = r.Reduce(SubagentSpawnedMsg{
			AgentID: agent, AgentType: "general-purpose", TaskID: agent,
			ParentToolCallID: "fan-4", TaskIndex: i, ExecutionID: fmt.Sprintf("exec-%d", i), Timestamp: t0,
		})
		_ = r.Reduce(SubagentEndedMsg{
			AgentID: agent, TaskID: agent, Status: "ok",
			ParentToolCallID: "fan-4", TaskIndex: i, ExecutionID: fmt.Sprintf("exec-%d", i), Timestamp: t0.Add(time.Minute),
		})
	}
	settled := lastFanoutFrame(t, r.Reduce(NewMessageMsg{Msg: subagentStepMessage("fan-4", tool.StepEvent{
		Kind: tool.StepKindToolCompleted, ToolName: "subagent_fanout", Input: input,
		Output: map[string]any{"output": `{"results":[{"index":0,"ok":true},{"index":1,"ok":true}]}`},
	})}))

	// Continue the second task through its own call.
	contInput := map[string]any{"task_id": "subagent-1", "message": "继续做扩展目录"}
	_ = r.Reduce(NewMessageMsg{Msg: subagentStepMessage("cont-1", tool.StepEvent{
		Kind: tool.StepKindToolStarted, ToolName: "subagent_continue", Input: contInput,
	})})
	_ = r.Reduce(SubagentSpawnedMsg{
		AgentID: "subagent-1", AgentType: "general-purpose", TaskID: "subagent-1", Title: "任务17 扩展目录",
		ParentToolCallID: "cont-1", TaskIndex: 0, ExecutionID: "exec-cont", Timestamp: t0.Add(2 * time.Minute),
	})
	_ = r.Reduce(SubagentEndedMsg{
		AgentID: "subagent-1", TaskID: "subagent-1", Status: "ok",
		ParentToolCallID: "cont-1", TaskIndex: 0, ExecutionID: "exec-cont", Timestamp: t0.Add(3 * time.Minute),
	})
	contCard := lastFanoutFrame(t, r.Reduce(NewMessageMsg{Msg: subagentStepMessage("cont-1", tool.StepEvent{
		Kind: tool.StepKindToolCompleted, ToolName: "subagent_continue", Input: contInput,
		Output:   map[string]any{"output": `{"record":{"task_id":"subagent-1","agent_type":"general-purpose","title":"任务17 扩展目录","status":"ok","execution_id":"exec-cont","started_at":1790000120,"finished_at":1790000180}}`},
		Duration: time.Minute,
	})}))
	if contCard.Summary != "Continued 1 general-purpose task" {
		t.Fatalf("continue header = %q, want Continued 1 general-purpose task", contCard.Summary)
	}
	if after := r.findFanoutByStepID("fan-4"); after == nil || emitFanoutContent(after) != settled.Content {
		t.Fatalf("the original card changed:\n%s\nvs\n%s", emitFanoutContent(after), settled.Content)
	}
}

// emitFanoutContent re-renders a fanout state's content for comparison.
func emitFanoutContent(fs *fanoutState) string {
	if fs == nil {
		return ""
	}
	content, _, _ := renderFanoutContent(fs)
	return content
}

// fanoutStateIDsOf lists the open card ids, for assertions about which cards
// exist.
func fanoutStateIDsOf(r *Reducer) []string {
	ids := make([]string, 0, len(r.fanoutStates))
	for id := range r.fanoutStates {
		ids = append(ids, id)
	}
	return ids
}

func TestNestedDispatchCardBelongsToItsAgentView(t *testing.T) {
	var r Reducer
	card := lastFanoutFrame(t, r.Reduce(NewMessageMsg{Msg: Message{
		Kind: MsgKindTool, StepID: "nested-1", ToolName: "subagent_run", AgentID: "task-a",
		ToolMeta: tool.BuildToolMeta(tool.StepEvent{
			Kind: tool.StepKindToolStarted, ToolName: "subagent_run",
			Input: map[string]any{"task": "查找所有调用点", "subagent_type": "explore"},
		}),
		ToolPhase: tool.StepKindToolStarted,
	}}))
	if card.AgentID != "task-a" {
		t.Fatalf("nested card AgentID = %q, want task-a", card.AgentID)
	}
}

// A subagent's own subagent_* call reaches the surface through the run-event
// mirror, whose meta conversion once dropped the card facts: the nested
// dispatch then fell back to a generic tool card in the subagent's view
// instead of the subagent card the preview shows.
func TestNestedDispatchMirroredThroughRunEventsKeepsItsCard(t *testing.T) {
	evt := event.NewRunEvent("r-1", "run-n", "s1", event.RunEventToolCompleted,
		event.ToolCallCompletedPayload{
			StepID: "nested-2", ToolName: "subagent_run", DurationSeconds: 0.03,
			Data:        "tool subagent_run: persist subagent run: FOREIGN KEY constraint failed",
			DisplayBody: "tool subagent_run: persist subagent run: FOREIGN KEY constraint failed",
			ToolMeta: event.ToolCallMeta{
				ToolName: "subagent_run", Status: "failed", AgentID: "task-a",
				SubagentCall: &event.SubagentCall{
					Verb: "run",
					Tasks: []event.SubagentCallTask{{
						Index: 0, Title: "总结 README 内容", AgentType: "explore", Status: "failed",
					}},
				},
			},
		}, time.Unix(100, 0).UTC())
	var p event.ToolCallCompletedPayload
	if err := json.Unmarshal(evt.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	msg := subagentToolStepMsg(evt, p.StepID, p.ToolName, p.Summary, p.ToolMeta, evt.Type)
	msg.Msg.Content = p.DisplayBody
	msg.Msg.Duration = time.Duration(p.DurationSeconds * float64(time.Second))

	var r Reducer
	card := lastFanoutFrame(t, r.Reduce(msg))
	if card.AgentID != "task-a" {
		t.Fatalf("nested card AgentID = %q, want task-a", card.AgentID)
	}
	if card.Summary != "Failed to run 1 explore task" {
		t.Fatalf("nested card summary = %q, want Failed to run 1 explore task", card.Summary)
	}
	if !strings.Contains(card.FanoutCallError, "FOREIGN KEY constraint failed") {
		t.Fatalf("nested card lost the call's error: %q", card.FanoutCallError)
	}
}

func TestRuntimeDispatchedAgentGetsACardNotStatusLines(t *testing.T) {
	t0 := time.Unix(1790000000, 0).UTC()
	var r Reducer
	spawn := r.Reduce(SubagentSpawnedMsg{
		AgentID: "plan-review-4f1a", AgentType: "plan-reviewer", TaskID: "plan-review-4f1a",
		Title: "Plan review", ExecutionID: "exec-r", Timestamp: t0,
	})
	end := r.Reduce(SubagentEndedMsg{
		AgentID: "plan-review-4f1a", TaskID: "plan-review-4f1a", Status: "ok",
		ExecutionID: "exec-r", Timestamp: t0.Add(2 * time.Minute),
	})
	for _, ev := range []EventResult{spawn, end} {
		for _, f := range ev.Frames {
			if f.Kind == FrameStatus {
				t.Fatalf("the lifecycle text lines are gone, got %#v", f)
			}
		}
	}
	card := lastFanoutFrame(t, end)
	if card.Summary != "Ran 1 plan-reviewer task" {
		t.Fatalf("summary = %q, want Ran 1 plan-reviewer task", card.Summary)
	}
	if !strings.HasPrefix(strings.TrimSpace(fanoutContentLines(card.Content)[0]), "✓ Plan review") {
		t.Fatalf("card row = %q, want ✓ Plan review", fanoutContentLines(card.Content)[0])
	}
}

func TestSettledTaskHasNoToolUseStatsLine(t *testing.T) {
	t0 := time.Unix(1790000000, 0).UTC()
	input := map[string]any{"tasks": []any{
		map[string]any{"prompt": "任务16 migrate 导入", "subagent_type": "general-purpose"},
		map[string]any{"prompt": "任务17 扩展目录", "subagent_type": "general-purpose"},
	}}
	var r Reducer
	_ = r.Reduce(NewMessageMsg{Msg: subagentStepMessage("fan-5", tool.StepEvent{
		Kind: tool.StepKindToolStarted, ToolName: "subagent_fanout", Input: input,
	})})
	statuses := []string{"ok", "failed"}
	for i, status := range statuses {
		agent := fmt.Sprintf("subagent-f%d", i)
		_ = r.Reduce(SubagentSpawnedMsg{
			AgentID: agent, AgentType: "general-purpose", TaskID: agent,
			ParentToolCallID: "fan-5", TaskIndex: i, ExecutionID: fmt.Sprintf("exec-f%d", i), Timestamp: t0,
		})
		for j := 0; j < 3; j++ {
			_ = r.Reduce(NewMessageMsg{Msg: Message{
				Kind: MsgKindTool, StepID: fmt.Sprintf("step-%d-%d", i, j), ToolName: "shell",
				ToolMeta: tool.ToolMeta{Invocation: fmt.Sprintf("tool %d of %d", j, i)}, AgentID: agent,
				ToolPhase: tool.StepKindToolStarted,
			}})
		}
		_ = r.Reduce(SubagentEndedMsg{
			AgentID: agent, TaskID: agent, Status: status, Error: map[bool]string{true: "上游连接中断（EOF）", false: ""}[status == "failed"],
			ParentToolCallID: "fan-5", TaskIndex: i, ExecutionID: fmt.Sprintf("exec-f%d", i), Timestamp: t0.Add(time.Minute),
		})
	}
	card := lastFanoutFrame(t, r.Reduce(NewMessageMsg{Msg: subagentStepMessage("fan-5", tool.StepEvent{
		Kind: tool.StepKindToolCompleted, ToolName: "subagent_fanout", Input: input,
		Output: map[string]any{"output": `{"results":[{"index":0,"ok":true},{"index":1,"error":"upstream EOF"}]}`},
	})}))
	assertNoStatsLine(t, card.Content)
	if !strings.Contains(card.Content, "上游连接中断（EOF）") {
		t.Fatalf("card lost the failed task's reason:\n%s", card.Content)
	}
	if got := strings.Count(card.Content, "… +2 tool uses"); got != 2 {
		t.Fatalf("overflow rows = %d, want one per task:\n%s", got, card.Content)
	}
}

func TestTaskRowsShowLiveAndFinalElapsed(t *testing.T) {
	t0 := time.Unix(1790000000, 0).UTC()
	input := map[string]any{"tasks": []any{
		map[string]any{"prompt": "任务16 migrate 导入", "subagent_type": "general-purpose"},
		map[string]any{"prompt": "任务17 扩展目录", "subagent_type": "general-purpose"},
	}}
	var r Reducer
	_ = r.Reduce(NewMessageMsg{Msg: subagentStepMessage("fan-6", tool.StepEvent{
		Kind: tool.StepKindToolStarted, ToolName: "subagent_fanout", Input: input,
	})})
	_ = r.Reduce(SubagentSpawnedMsg{
		AgentID: "subagent-t1", AgentType: "general-purpose", TaskID: "subagent-t1",
		ParentToolCallID: "fan-6", TaskIndex: 0, ExecutionID: "exec-t1", Timestamp: t0,
	})
	_ = r.Reduce(SubagentSpawnedMsg{
		AgentID: "subagent-t2", AgentType: "general-purpose", TaskID: "subagent-t2",
		ParentToolCallID: "fan-6", TaskIndex: 1, ExecutionID: "exec-t2", Timestamp: t0.Add(time.Second),
	})
	card := lastFanoutFrame(t, r.Reduce(SubagentEndedMsg{
		AgentID: "subagent-t2", TaskID: "subagent-t2", Status: "ok",
		ParentToolCallID: "fan-6", TaskIndex: 1, ExecutionID: "exec-t2",
		FinishedAt: t0.Add(75 * time.Second), Timestamp: t0.Add(75 * time.Second),
	}))
	lines := fanoutContentLines(card.Content)
	ended := fanoutClockOfLine(card, lines, "任务17 扩展目录")
	if !ended.Start.Equal(t0.Add(time.Second)) || !ended.End.Equal(t0.Add(75*time.Second)) {
		t.Fatalf("ended clock = %+v, want t0+1s..t0+75s", ended)
	}
	if live := fanoutClockOfLine(card, lines, "任务16 migrate 导入"); !live.End.IsZero() {
		t.Fatalf("still-running clock already ended: %+v", live)
	}

	restore := fanoutClockNow
	fanoutClockNow = func() time.Time { return t0.Add(200 * time.Second) }
	defer func() { fanoutClockNow = restore }()
	painted := renderFanoutForTest(card)
	sawFinal, sawLive := false, false
	for _, line := range painted {
		switch {
		case strings.Contains(line, "任务17 扩展目录"):
			if !strings.HasSuffix(strings.TrimSpace(line), "· 1m14s") {
				t.Fatalf("ended row = %q, want the 1m14s final elapsed", line)
			}
			sawFinal = true
		case strings.Contains(line, "任务16 migrate 导入"):
			if !strings.HasSuffix(strings.TrimSpace(line), "· 3m 20s") {
				t.Fatalf("running row = %q, want the 3m 20s live clock", line)
			}
			sawLive = true
		}
	}
	if !sawFinal || !sawLive {
		t.Fatalf("card lost a task row:\n%s", strings.Join(painted, "\n"))
	}

	// A single-task dispatch card names its duration on the task row alone.
	single := map[string]any{"task": "追踪", "subagent_type": "explore"}
	var r2 Reducer
	_ = r2.Reduce(NewMessageMsg{Msg: subagentStepMessage("run-2", tool.StepEvent{
		Kind: tool.StepKindToolStarted, ToolName: "subagent_run", Input: single,
	})})
	singleCard := lastFanoutFrame(t, r2.Reduce(NewMessageMsg{Msg: subagentStepMessage("run-2", tool.StepEvent{
		Kind: tool.StepKindToolCompleted, ToolName: "subagent_run", Input: single,
		Output:   map[string]any{"output": `{"task_id":"subagent-z","status":"ok","finished_at":1790000182,"agent_type":"explore"}`},
		Duration: 182 * time.Second,
	})}))
	if singleCard.Summary != "Ran 1 explore task" {
		t.Fatalf("single-task header = %q, want Ran 1 explore task without a duration", singleCard.Summary)
	}
	for _, line := range renderFanoutForTest(singleCard) {
		if strings.HasPrefix(strings.TrimSpace(line), "●") && strings.Contains(line, " · ") {
			t.Fatalf("single-task header repeats the duration: %q", line)
		}
	}
}

func TestQueryRowFollowsItsExecutionToTheEnd(t *testing.T) {
	t0 := time.Unix(1790000000, 0).UTC()
	var r Reducer
	sendInput := map[string]any{"title": "计划001 Go车道实施", "task": "读 README", "subagent_type": "general-purpose"}
	_ = r.Reduce(NewMessageMsg{Msg: subagentStepMessage("send-9", tool.StepEvent{
		Kind: tool.StepKindToolStarted, ToolName: "subagent_send", Input: sendInput,
	})})
	_ = r.Reduce(SubagentSpawnedMsg{
		AgentID: "subagent-9a", AgentType: "general-purpose", TaskID: "subagent-9a", Title: "计划001 Go车道实施",
		ParentToolCallID: "send-9", TaskIndex: 0, ExecutionID: "exec-9", Timestamp: t0,
	})
	closeCard := lastFanoutFrame(t, r.Reduce(NewMessageMsg{Msg: subagentStepMessage("close-9", tool.StepEvent{
		Kind: tool.StepKindToolCompleted, ToolName: "subagent_close",
		Input:    map[string]any{"task_id": "subagent-9a"},
		Output:   map[string]any{"output": `{"status":"cancel_requested","record":{"task_id":"subagent-9a","agent_type":"general-purpose","title":"计划001 Go车道实施","status":"running","execution_id":"exec-9","started_at":1790000000}}`},
		Duration: 30 * time.Millisecond,
	})}))
	lines := fanoutContentLines(closeCard.Content)
	if got := strings.TrimSpace(lines[0]); !strings.HasPrefix(got, "计划001") {
		t.Fatalf("query row = %q, want the running task without an icon", got)
	}
	if !strings.Contains(closeCard.Content, "stop requested") {
		t.Fatalf("query row lost its phrase:\n%s", closeCard.Content)
	}

	// The execution the row follows ends later: the row settles with it, and
	// the settled card is re-emitted — state no frame carries never repaints
	// the screen, whose row would keep walking its running clock past the end.
	endedFrames := r.Reduce(SubagentEndedMsg{
		AgentID: "subagent-9a", TaskID: "subagent-9a", Status: "cancelled",
		ParentToolCallID: "send-9", TaskIndex: 0, ExecutionID: "exec-9",
		FinishedAt: t0.Add(302 * time.Second), Timestamp: t0.Add(302 * time.Second),
	})
	var settledCard Frame
	for _, f := range endedFrames.Frames {
		if f.Kind == FrameFanout && f.StepID == "close-9" {
			settledCard = f
		}
	}
	if settledCard.StepID == "" {
		t.Fatalf("the ended execution did not re-emit the close card it settled: %d frames", len(endedFrames.Frames))
	}
	lines = fanoutContentLines(settledCard.Content)
	if got := strings.TrimSpace(lines[0]); !strings.HasPrefix(got, "×") {
		t.Fatalf("settled query row = %q, want the cancelled icon", got)
	}
	if !strings.Contains(settledCard.Content, "stop requested") {
		t.Fatalf("settled query row lost its phrase:\n%s", settledCard.Content)
	}
	if clock := fanoutClockOfLine(settledCard, lines, "计划001"); !clock.End.Equal(t0.Add(302 * time.Second)) {
		t.Fatalf("settled query clock = %+v, want the execution's stop", clock)
	}
}

func TestAbandonedExecutionEndsAtItsRealStopTime(t *testing.T) {
	t0 := time.Unix(1790000000, 0).UTC()
	const reason = "The process running this turn stopped before it finished."
	var r Reducer
	_ = r.Reduce(SubagentSpawnedMsg{
		AgentID: "subagent-ab", AgentType: "general-purpose", TaskID: "subagent-ab", Title: "核对迁移",
		Timestamp: t0,
	})
	card := lastFanoutFrame(t, r.Reduce(SubagentEndedMsg{
		AgentID: "subagent-ab", TaskID: "subagent-ab", Status: "failed", Error: reason,
		FinishedAt: t0.Add(30 * time.Second), Timestamp: t0.Add(2 * time.Hour),
	}))
	lines := fanoutContentLines(card.Content)
	if got := strings.TrimSpace(lines[0]); !strings.HasPrefix(got, "✗ 核对迁移") {
		t.Fatalf("row = %q, want the failed icon and the task name", got)
	}
	if !strings.Contains(card.Content, reason) {
		t.Fatalf("row lost the reaper's reason:\n%s", card.Content)
	}
	if clock := fanoutClockOfLine(card, lines, "核对迁移"); !clock.End.Equal(t0.Add(30 * time.Second)) {
		t.Fatalf("clock = %+v, want the execution's real stop at t0+30s", clock)
	}
	restore := fanoutClockNow
	fanoutClockNow = func() time.Time { return t0.Add(2 * time.Hour) }
	defer func() { fanoutClockNow = restore }()
	for _, line := range renderFanoutForTest(card) {
		if strings.Contains(line, "核对迁移") && !strings.HasSuffix(strings.TrimSpace(line), "· 30s") {
			t.Fatalf("row = %q, want the 30s final elapsed, not two hours", line)
		}
	}
}

func TestPlanReviewCardUsesTheDispatchVerbs(t *testing.T) {
	t0 := time.Unix(1790000000, 0).UTC()
	var r Reducer
	started := lastFanoutFrame(t, r.Reduce(PlanReviewStartedMsg{ReviewID: "rv-1", Provider: "zhipuai", Model: "glm-5.3-flash"}))
	if started.Summary != "Starting 1 plan-reviewer task…" {
		t.Fatalf("header = %q, want Starting 1 plan-reviewer task…", started.Summary)
	}
	_ = r.Reduce(SubagentSpawnedMsg{
		AgentID: "plan-review-4f1a", AgentType: "plan-reviewer", TaskID: "plan-review-4f1a",
		Title: "Plan review", ParentToolCallID: "rv-1", TaskIndex: 0, ExecutionID: "exec-rv1", Timestamp: t0,
	})
	running := lastFanoutFrame(t, r.Reduce(SubagentSpawnedMsg{
		AgentID: "plan-review-4f1a", AgentType: "plan-reviewer", TaskID: "plan-review-4f1a",
		Title: "Plan review", ParentToolCallID: "rv-1", TaskIndex: 0, ExecutionID: "exec-rv1", Timestamp: t0,
	}))
	if running.Summary != "Running 1 plan-reviewer task…" {
		t.Fatalf("header = %q, want Running 1 plan-reviewer task…", running.Summary)
	}
	for _, id := range fanoutStateIDsOf(&r) {
		if strings.HasPrefix(id, "subagent-exec:") {
			t.Fatalf("the reviewer opened a standalone card beside its review card: %v", fanoutStateIDsOf(&r))
		}
	}
	done := lastFanoutFrame(t, r.Reduce(SubagentEndedMsg{
		AgentID: "plan-review-4f1a", TaskID: "plan-review-4f1a", Status: "ok",
		ParentToolCallID: "rv-1", TaskIndex: 0, ExecutionID: "exec-rv1", Timestamp: t0.Add(2 * time.Minute),
	}))
	if done.Summary != "Ran 1 plan-reviewer task" {
		t.Fatalf("header = %q, want Ran 1 plan-reviewer task", done.Summary)
	}

	// A review that never started reports the failure it was stopped by.
	var r2 Reducer
	_ = r2.Reduce(PlanReviewStartedMsg{ReviewID: "rv-2", Provider: "zhipuai", Model: "glm-5.3-flash"})
	const why = "model zhipuai/glm-5.3-flash is not configured for this session"
	failed := lastFanoutFrame(t, r2.Reduce(PlanReviewReviewedMsg{ReviewID: "rv-2", Provider: "zhipuai", Model: "glm-5.3-flash", Outcome: "failed", Error: why}))
	if failed.Summary != "Failed to start 1 plan-reviewer task" {
		t.Fatalf("header = %q, want Failed to start 1 plan-reviewer task", failed.Summary)
	}
	if failed.FanoutCallError != why {
		t.Fatalf("call error = %q, want %q", failed.FanoutCallError, why)
	}
	painted := renderFanoutForTest(failed)
	if got := strings.TrimSpace(painted[1]); got != why {
		t.Fatalf("first body line = %q, want the error:\n%s", got, strings.Join(painted, "\n"))
	}
}

// The plan review happens while the exit-plan approval still holds it: the user
// asks for the review, the reviewer runs, and only then does the user approve
// the exit. The three records are one exchange, so the card reads between the
// two confirmation lines, above the parked exit-plan call they both refer to —
// not appended after it, where it would read as a footnote to the approval.
func TestPlanReviewCardLandsBetweenTheApprovalConfirmations(t *testing.T) {
	r := newComposerRenderer(t, 100, 40)
	const asked = "✔ You asked zhipuai/glm-5.3-flash to review the plan"
	const approved = "✔ You approved forebrain to exit plan mode"

	reducer := &Reducer{}
	r.RenderFrame(Frame{Kind: FrameAssistant, Content: "the plan is written", Final: true})
	r.RenderFrame(Frame{Kind: FrameTool, StepID: "call-exit", Title: "exit_plan_mode", Final: true})
	r.RenderFrame(Frame{Kind: FrameStatus, Content: asked, InsertBeforeLastTool: true, Final: true})
	for _, f := range reducer.Reduce(PlanReviewStartedMsg{
		ReviewID: "rv-1", Provider: "zhipuai", Model: "glm-5.3-flash",
	}).Frames {
		r.RenderFrame(f)
	}
	r.RenderFrame(Frame{Kind: FrameStatus, Content: approved, InsertBeforeLastTool: true, Final: true})

	got := make([]string, 0, len(r.vm.blocks))
	for _, block := range r.vm.blocks {
		switch block.frame.Kind {
		case FrameAssistant:
			got = append(got, "assistant")
		case FrameTool:
			got = append(got, "exit plan mode")
		case FrameFanout:
			got = append(got, "card:"+block.frame.StepID)
		case FrameStatus:
			got = append(got, "status:"+strings.TrimSpace(stripANSI(block.frame.Content)))
		default:
			got = append(got, string(block.frame.Kind))
		}
	}
	want := []string{"assistant", "status:" + asked, "card:rv-1", "status:" + approved, "exit plan mode"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("transcript order:\n%v\nwant:\n%v", got, want)
	}

	// A card already carrying this review's StepID updates in place: the
	// reviewer finishing must not append a second block.
	for _, f := range reducer.Reduce(PlanReviewReviewedMsg{
		ReviewID: "rv-1", Provider: "zhipuai", Model: "glm-5.3-flash", Outcome: "done",
	}).Frames {
		r.RenderFrame(f)
	}
	if n := len(r.vm.blocks); n != len(want) {
		t.Fatalf("the review's update added a block: %d blocks, want %d", n, len(want))
	}
}

// The card's headers and the transcript summaries 012 derives are the same
// words in different cases: one table feeds both, so they cannot disagree.
func TestSubagentCardHeadersMatchTheStepSummaries(t *testing.T) {
	input := map[string]any{"task": "读 README", "subagent_type": "general-purpose"}
	cases := []struct {
		tool  string
		phase string
	}{
		{"subagent_run", tool.StepKindToolStarted},
		{"subagent_run", tool.StepKindToolCompleted},
		{"subagent_continue", tool.StepKindToolStarted},
		{"subagent_continue", tool.StepKindToolCompleted},
		{"subagent_status", tool.StepKindToolStarted},
		{"subagent_status", tool.StepKindToolCompleted},
		{"subagent_wait", tool.StepKindToolStarted},
		{"subagent_wait", tool.StepKindToolCompleted},
		{"subagent_close", tool.StepKindToolStarted},
		{"subagent_close", tool.StepKindToolCompleted},
		{"subagent_list", tool.StepKindToolStarted},
		{"subagent_list", tool.StepKindToolCompleted},
	}
	for _, tc := range cases {
		evt := tool.StepEvent{Kind: tc.phase, ToolName: tc.tool, Input: input,
			Output: map[string]any{"output": `{"task_id":"subagent-1","agent_type":"general-purpose","title":"读 README","status":"ok","started_at":1790000000,"finished_at":1790000042}`}}
		summary := strings.TrimSpace(tool.SummarizeToolStep(evt))
		var r Reducer
		start := r.Reduce(NewMessageMsg{Msg: subagentStepMessage("s", evt)})
		if tc.phase == tool.StepKindToolStarted {
			evt2 := evt
			evt2.Kind = tool.StepKindToolCompleted
			_ = r.Reduce(NewMessageMsg{Msg: subagentStepMessage("s", evt2)})
		}
		card := Frame{}
		for _, f := range start.Frames {
			if f.Kind == FrameFanout {
				card = f
			}
		}
		if card.Kind != FrameFanout {
			t.Fatalf("%s %s: no card", tc.tool, tc.phase)
		}
		want := strings.ToUpper(summary[:1]) + summary[1:]
		if tc.phase == tool.StepKindToolStarted {
			want += "…"
		}
		if card.Summary != want {
			t.Fatalf("%s %s: header = %q, want the summary's words %q", tc.tool, tc.phase, card.Summary, want)
		}
	}
}

// TestReducerFanoutAgentReasoningNotDropped guards that a fanout subagent's
// reasoning deltas are emitted as AgentID-tagged FrameThinking (routed only to
// the per-agent view), not silently suppressed. Previously reduceMessage
// dropped assistant/reasoning for any fanout agent, starving the subagent view.
func TestReducerFanoutAgentReasoningNotDropped(t *testing.T) {
	var r Reducer
	_ = r.Reduce(NewMessageMsg{Msg: Message{
		Kind:     MsgKindTool,
		StepID:   "fanout-reason",
		ToolName: "subagent_run",
		ToolMeta: tool.ToolMeta{
			Input: map[string]any{"task": "investigate", "title": "Investigation"},
		},
	}})
	_ = r.Reduce(SubagentSpawnedMsg{AgentID: "agent-reason-1", AgentType: "explore", TaskID: "agent-reason-1"})

	got := r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindReasoning,
		Content: "considering the auth flow",
		AgentID: "agent-reason-1",
	}}).Frames
	if len(got) != 1 {
		t.Fatalf("expected one thinking frame for fanout subagent, got %#v", got)
	}
	if got[0].Kind != FrameThinking {
		t.Fatalf("expected FrameThinking, got %#v", got[0])
	}
	if got[0].AgentID != "agent-reason-1" {
		t.Fatalf("thinking frame must be AgentID-tagged for per-agent routing, got %q", got[0].AgentID)
	}
	if !strings.Contains(got[0].Content, "considering the auth flow") {
		t.Fatalf("thinking frame missing reasoning text: %q", got[0].Content)
	}
}

// TestReducerSubagentEndedFlushesBufferedAssistant guards that a subagent's
// buffered assistant text is flushed as an AgentID-tagged final FrameAssistant
// when the subagent ends. RunEndedMsg only flushes the primary buffer, so
// without this the subagent's final answer never reaches its per-agent view.
func TestReducerSubagentEndedFlushesBufferedAssistant(t *testing.T) {
	var r Reducer
	_ = r.Reduce(NewMessageMsg{Msg: Message{
		Kind:     MsgKindTool,
		StepID:   "fanout-flush",
		ToolName: "subagent_run",
		ToolMeta: tool.ToolMeta{
			Input: map[string]any{"task": "investigate", "title": "Investigation"},
		},
	}})
	_ = r.Reduce(SubagentSpawnedMsg{AgentID: "agent-flush-1", AgentType: "explore", TaskID: "agent-flush-1"})

	// A subagent's answer streams too, tagged with its AgentID so the live
	// frame is routed to that subagent's own view rather than the conversation.
	if got := r.Reduce(NewMessageMsg{Msg: Message{
		Kind:    MsgKindAssistant,
		Content: "Here is what I found.",
		AgentID: "agent-flush-1",
	}}).Frames; len(got) != 1 || got[0].AgentID != "agent-flush-1" || got[0].Final {
		t.Fatalf("subagent assistant text must stream into its own view, got %#v", got)
	}

	end := r.Reduce(SubagentEndedMsg{
		AgentID:   "agent-flush-1",
		AgentType: "explore",
		TaskID:    "agent-flush-1",
		Status:    "done",
	}).Frames

	var flushed *Frame
	for i := range end {
		if end[i].Kind == FrameAssistant {
			flushed = &end[i]
			break
		}
	}
	if flushed == nil {
		t.Fatalf("expected a flushed FrameAssistant on subagent end, got %#v", end)
	}
	if flushed.AgentID != "agent-flush-1" {
		t.Fatalf("flushed assistant must be AgentID-tagged for per-agent routing, got %q", flushed.AgentID)
	}
	if flushed.Content != "Here is what I found." {
		t.Fatalf("flushed assistant content = %q, want %q", flushed.Content, "Here is what I found.")
	}
	if !flushed.Final {
		t.Fatalf("flushed assistant frame must be Final")
	}
}

// truncateForDisplay is fed user- and model-authored text (search queries,
// commands, task titles) and spends maxLen as a terminal-column budget.
func TestTruncateForDisplayCutsToAColumnBudget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{name: "short input is returned whole", input: "go test ./...", maxLen: 96, want: "go test ./..."},
		{name: "ascii spends one column per char", input: "abcdefghij", maxLen: 5, want: "abcd…"},
		{name: "wide chars cost two columns each", input: "重构 approval 文案", maxLen: 8, want: "重构 ap…"},
		{name: "budget below one wide char", input: "重构", maxLen: 1, want: "…"},
		{name: "budget of zero", input: "重构", maxLen: 0, want: "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateForDisplay(tc.input, tc.maxLen)
			if got != tc.want {
				t.Fatalf("truncateForDisplay(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.want)
			}
		})
	}
}

// Display text arrives from tools and models that may have byte-sliced it
// already, so the damage is often done before truncation gets a say.
func TestTruncateForDisplayRepairsInvalidUTF8(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
	}{
		{name: "truncated lead byte at the end", input: "ok \xe4\xb8"},
		{name: "stray bytes at the start", input: "\xff\xfe" + strings.Repeat("a", 40)},
		{name: "invalid byte mid-string", input: "before \x80 after"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, maxLen := range []int{1, 5, 20, 1000} {
				got := truncateForDisplay(tc.input, maxLen)
				if !utf8.ValidString(got) {
					t.Fatalf("maxLen=%d passed invalid UTF-8 through: %q", maxLen, got)
				}
				if w := runewidth.StringWidth(got); w > maxLen {
					t.Fatalf("maxLen=%d produced %d columns: %q", maxLen, w, got)
				}
			}
		})
	}
}

// Sweeping every budget catches the boundaries a table misses: the result must
// always fit the columns it was given and never end mid-character.
func TestTruncateForDisplayNeverOverflowsOrSplitsRunes(t *testing.T) {
	input := "git commit -m \"重构 approval 文案 with a 长 message\""
	for maxLen := 1; maxLen <= runewidth.StringWidth(input)+2; maxLen++ {
		got := truncateForDisplay(input, maxLen)
		if !utf8.ValidString(got) {
			t.Fatalf("maxLen=%d produced invalid UTF-8: %q", maxLen, got)
		}
		if w := runewidth.StringWidth(got); w > maxLen {
			t.Fatalf("maxLen=%d produced %d columns: %q", maxLen, w, got)
		}
	}
}

// A fanout child's ending must reach both places: the task row on the fanout
// card, and the child's own view. Before this, the fanout branch returned as
// soon as it had rebuilt the card, so the subagent's view never learned that
// its run had failed.
func TestReducerFanoutChildEndedEmitsClosingFrameForItsOwnView(t *testing.T) {
	var r Reducer
	_ = r.Reduce(NewMessageMsg{Msg: Message{
		Kind:      MsgKindTool,
		ToolName:  "subagent_fanout",
		StepID:    "step-1",
		ToolPhase: event.RunEventToolStarted,
		ToolMeta: tool.ToolMeta{
			ToolName: "subagent_fanout",
			Status:   "running",
			Input: map[string]any{
				"tasks": []any{map[string]any{"prompt": "inspect the client", "subagent_type": "plan"}},
			},
		},
	}})
	_ = r.Reduce(SubagentSpawnedMsg{AgentID: "task-1", AgentType: "plan", TaskID: "task-1", Task: "inspect the client"})
	out := r.Reduce(SubagentEndedMsg{
		AgentID:   "task-1",
		AgentType: "plan",
		TaskID:    "task-1",
		Status:    "failed",
		Error:     "the sandbox blocked git grep",
	})

	var closing, fanout *Frame
	for i := range out.Frames {
		switch out.Frames[i].Kind {
		case FrameError:
			closing = &out.Frames[i]
		case FrameFanout:
			fanout = &out.Frames[i]
		}
	}
	if fanout == nil {
		t.Fatalf("the fanout card must still be rebuilt, got %#v", out.Frames)
	}
	if closing == nil {
		t.Fatalf("the child's view must be told why it stopped, got %#v", out.Frames)
	}
	if closing.AgentID != "task-1" {
		t.Fatalf("closing frame AgentID = %q, want the child's roster key", closing.AgentID)
	}
	if !strings.Contains(closing.Content, "the sandbox blocked git grep") {
		t.Fatalf("closing frame content = %q, want the failure reason", closing.Content)
	}
}

// The helpers below were production functions that only the tests ever
// called: each is a thin composition of live code. They live here so the
// production files carry no unused code while the tests keep exercising
// the live functions underneath.

// renderFrameLines renders one frame to its display lines by REUSING the legacy
// renderers. Width-aware primitives are called directly where they exist
// (thinking); the remaining card/status/markdown paths are captured from a
// scratch buffer-backed Renderer driven exactly as the live RenderFrame drives
// them, with fullBodyMode on so nothing is truncated before the viewport folds
// it. The package-level terminal-size override pins width for the
// width-deriving helpers (termWidthOrDefault / maxCardContentWidth) during the
// capture; it is saved and restored so live composer rendering is unaffected.
func renderFrameLines(f Frame, width int, theme DiffTheme, spinnerPhase int, cwdOpt ...string) []string {
	lines, _ := renderFrameLinesWithAgents(f, width, theme, spinnerPhase, cwdOpt...)
	return lines
}

// ObserveTool increments the session tool counter.
func (t *Tracker) ObserveTool(toolName string) {
	t.ObserveToolStep("", "", toolName, "")
}

// drainCaretRequest returns the caret position a click posted for the raw input
// reader, or -1 when none was posted.
func drainCaretRequest() int {
	select {
	case caret := <-interactiveInputCaretCh:
		return caret
	default:
		return -1
	}
}

// clickAt performs the press/release pair a single mouse click produces.
func clickAt(r *Renderer, col, row int) {
	r.ViewportSelectStart(col, row)
	r.ViewportSelectEnd(col, row)
}

// composerTextRowScreen returns the screen row of the composer's nth text row.
func composerTextRowScreen(r *Renderer, textRow int) int {
	return r.vpBodyHeight + r.vpLastComposer.text.firstLine - r.vpLastComposer.scrollOffset + textRow
}

func newComposerRenderer(t *testing.T, width, height int) *Renderer {
	t.Helper()
	forcedTermWidth, forcedTermHeight = width, height
	t.Cleanup(func() { forcedTermWidth, forcedTermHeight = 0, 0 })
	var out bytes.Buffer
	r := NewRenderer(&out, &out)
	r.EnableViewportMode()
	t.Cleanup(r.DisableViewportMode)
	t.Cleanup(func() { drainCaretRequest() })
	return r
}

// TestComposerClickPlacesCaretAtClickedRune is the reported bug: clicking in the
// composer did nothing, so the caret could only be moved with the arrow keys.
func TestComposerClickPlacesCaretAtClickedRune(t *testing.T) {
	r := newComposerRenderer(t, 80, 24)
	cursor := len("hello world")
	r.RenderComposerState(ComposerRenderState{Text: "hello world", Cursor: &cursor})

	row := composerTextRowScreen(r, 0)
	prefix := r.vpLastComposer.text.rows[0].prefixWidth
	drainCaretRequest()
	clickAt(r, prefix+6, row) // the "w" of "world"

	if got := drainCaretRequest(); got != 6 {
		t.Fatalf("click on the 7th column of the composer text should request caret 6, got %d", got)
	}
}

// TestComposerClickOnWideRunesUsesDisplayColumns guards the CJK case: a wide
// rune covers two cells, and a click on either of them lands before it.
func TestComposerClickOnWideRunesUsesDisplayColumns(t *testing.T) {
	r := newComposerRenderer(t, 80, 24)
	cursor := 0
	r.RenderComposerState(ComposerRenderState{Text: "你好世界", Cursor: &cursor})

	row := composerTextRowScreen(r, 0)
	prefix := r.vpLastComposer.text.rows[0].prefixWidth
	for _, tc := range []struct{ col, want int }{{0, 0}, {1, 0}, {2, 1}, {3, 1}, {4, 2}, {99, 4}} {
		drainCaretRequest()
		clickAt(r, prefix+tc.col, row)
		if got := drainCaretRequest(); got != tc.want {
			t.Fatalf("click at text column %d should request caret %d, got %d", tc.col, tc.want, got)
		}
	}
}

// TestComposerClickOnWrappedRowResolvesThroughTheWrap checks a click on the
// second visual row of a soft-wrapped composer, where the caret offset is the
// row's own start plus the clicked column rather than the column alone.
func TestComposerClickOnWrappedRowResolvesThroughTheWrap(t *testing.T) {
	r := newComposerRenderer(t, 40, 24)
	text := strings.Repeat("word ", 20)
	cursor := 0
	r.RenderComposerState(ComposerRenderState{Text: text, Cursor: &cursor})

	rows := r.vpLastComposer.text.rows
	if len(rows) < 2 {
		t.Fatalf("expected the composer text to wrap onto several rows, got %d", len(rows))
	}
	second := rows[1]
	drainCaretRequest()
	clickAt(r, second.prefixWidth+3, composerTextRowScreen(r, 1))

	want := second.startRune + 3
	if got := drainCaretRequest(); got != want {
		t.Fatalf("click on the second wrapped row should request caret %d, got %d", want, got)
	}
	if []rune(text)[want] != []rune(text)[second.startRune+3] {
		t.Fatalf("caret must index the buffer, not the row")
	}
}

// TestComposerClickPastEndOfLineLandsAtItsEnd covers clicking in the empty
// space to the right of a hard line: the caret goes to the end of that line's
// text, never into the next one.
func TestComposerClickPastEndOfLineLandsAtItsEnd(t *testing.T) {
	r := newComposerRenderer(t, 80, 24)
	cursor := 0
	r.RenderComposerState(ComposerRenderState{Text: "ab\ncdef", Cursor: &cursor})

	rows := r.vpLastComposer.text.rows
	if len(rows) != 2 {
		t.Fatalf("expected two composer text rows, got %d", len(rows))
	}
	drainCaretRequest()
	clickAt(r, rows[0].prefixWidth+40, composerTextRowScreen(r, 0))
	if got := drainCaretRequest(); got != 2 {
		t.Fatalf("click past the end of the first line should request caret 2, got %d", got)
	}
}

// TestComposerClickLeadingNewlinesAddsBackTheTrimmedPrefix guards the offset
// translation: the card trims leading newlines before drawing, so a caret
// resolved against the drawing has to be shifted back into the buffer.
func TestComposerClickLeadingNewlinesAddsBackTheTrimmedPrefix(t *testing.T) {
	r := newComposerRenderer(t, 80, 24)
	cursor := 0
	r.RenderComposerState(ComposerRenderState{Text: "\n\nhello", Cursor: &cursor})

	row := composerTextRowScreen(r, 0)
	prefix := r.vpLastComposer.text.rows[0].prefixWidth
	drainCaretRequest()
	clickAt(r, prefix+3, row)

	if got := drainCaretRequest(); got != 5 { // 2 trimmed newlines + 3
		t.Fatalf("caret must refer to the buffer the reader owns, got %d", got)
	}
}

// TestComposerClickOffTheTextRowsRequestsNothing keeps the gesture confined to
// the text: the card's borders, footer and the transcript above stay
// selection/toggle surfaces.
func TestComposerClickOffTheTextRowsRequestsNothing(t *testing.T) {
	r := newComposerRenderer(t, 80, 24)
	cursor := 0
	r.RenderComposerState(ComposerRenderState{Text: "hello", Cursor: &cursor})

	textRow := composerTextRowScreen(r, 0)
	for _, row := range []int{textRow - 1, textRow + 1, 0} {
		drainCaretRequest()
		clickAt(r, 4, row)
		if got := drainCaretRequest(); got != -1 {
			t.Fatalf("click on row %d must not move the composer caret, got request %d", row, got)
		}
	}
}

// TestComposerDragSelectionDoesNotMoveTheCaret keeps copy-by-drag intact: only
// a plain click places the caret.
func TestComposerDragSelectionDoesNotMoveTheCaret(t *testing.T) {
	r := newComposerRenderer(t, 80, 24)
	cursor := 0
	r.RenderComposerState(ComposerRenderState{Text: "hello world", Cursor: &cursor})

	row := composerTextRowScreen(r, 0)
	prefix := r.vpLastComposer.text.rows[0].prefixWidth
	drainCaretRequest()
	r.ViewportSelectStart(prefix, row)
	r.ViewportSelectDrag(prefix+5, row)
	r.ViewportSelectEnd(prefix+5, row)
	if got := drainCaretRequest(); got != -1 {
		t.Fatalf("a drag selection must not move the caret, got request %d", got)
	}
}

// TestComposerClickWhileScrolledUsesFullBlockRows checks a composer tall enough
// to scroll: the visible rows are a window onto the full block, and a click
// still resolves through the row it actually shows.
func TestComposerClickWhileScrolledUsesFullBlockRows(t *testing.T) {
	r := newComposerRenderer(t, 40, 12)
	text := strings.Repeat("word ", 60)
	cursor := len([]rune(text))
	r.RenderComposerState(ComposerRenderState{Text: text, Cursor: &cursor})

	if r.vpLastComposer.scrollOffset == 0 {
		t.Fatalf("expected the composer to be scrolled with %d runes of text", len([]rune(text)))
	}
	// The first visible text row, wherever the scroll put it.
	firstVisible := r.vpLastComposer.scrollOffset
	if firstVisible < r.vpLastComposer.text.firstLine {
		firstVisible = r.vpLastComposer.text.firstLine
	}
	row := firstVisible - r.vpLastComposer.text.firstLine
	drainCaretRequest()
	clickAt(r, r.vpLastComposer.text.rows[row].prefixWidth+2, r.vpBodyHeight+firstVisible-r.vpLastComposer.scrollOffset)

	want := r.vpLastComposer.text.rows[row].startRune + 2
	if got := drainCaretRequest(); got != want {
		t.Fatalf("click on a scrolled composer row should request caret %d, got %d", want, got)
	}
}

// TestSlashComposerClickPlacesCaret covers the composer's other shape: while
// the draft starts with "/" the card collapses to a single prompt line.
func TestSlashComposerClickPlacesCaret(t *testing.T) {
	r := newComposerRenderer(t, 80, 24)
	cursor := 0
	r.RenderComposerState(ComposerRenderState{Text: "/model gpt", Cursor: &cursor})

	rows := r.vpLastComposer.text.rows
	if len(rows) != 1 {
		t.Fatalf("expected a single slash-composer text row, got %d", len(rows))
	}
	drainCaretRequest()
	clickAt(r, rows[0].prefixWidth+6, composerTextRowScreen(r, 0))
	if got := drainCaretRequest(); got != 6 {
		t.Fatalf("click on the slash composer should request caret 6, got %d", got)
	}
}

// TestComposerClickIgnoredWhileAModalOwnsTheScreen keeps the two surfaces
// apart: while an overlay is up its own field owns the caret.
func TestComposerClickIgnoredWhileAModalOwnsTheScreen(t *testing.T) {
	r := newComposerRenderer(t, 80, 24)
	cursor := 0
	r.RenderComposerState(ComposerRenderState{Text: "hello", Cursor: &cursor})
	textRow := composerTextRowScreen(r, 0)

	r.BeginComposerOverlay()
	r.SetOverlayComposer([]string{"pick one"}, -1, 0)
	drainCaretRequest()
	clickAt(r, 4, textRow)
	if got := drainCaretRequest(); got != -1 {
		t.Fatalf("a click during a modal must not move the composer caret, got %d", got)
	}
}

// TestOverlayClickPositionResolvesRowsAndColumns covers the position a modal is
// handed for a click: which of the lines it wrote, and where along it.
func TestOverlayClickPositionResolvesRowsAndColumns(t *testing.T) {
	r := newComposerRenderer(t, 80, 24)
	r.BeginComposerOverlay()
	r.SetOverlayComposer([]string{"first line", "second line", "third line"}, -1, 0)

	// Row 0 of the painted block is the pinned separator.
	if _, _, ok := r.OverlayClickPosition(3, r.vpBodyHeight); ok {
		t.Fatal("the separator row is not part of the overlay's own lines")
	}
	line, col, ok := r.OverlayClickPosition(4, r.vpBodyHeight+2)
	if !ok || line != 1 || col != 4 {
		t.Fatalf("expected line 1 column 4, got line %d column %d ok=%v", line, col, ok)
	}
}

// TestOverlayClickPositionThroughSoftWrap checks the reflowed case: a
// user_interaction overlay keeps logical rows and the paint wraps them, so a
// click on a continuation row must come back as a column inside the logical
// line, past the words the earlier rows showed.
func TestOverlayClickPositionThroughSoftWrap(t *testing.T) {
	r := newComposerRenderer(t, 40, 24)
	long := strings.Repeat("word ", 12)
	r.BeginComposerOverlay()
	r.SetSoftWrappingOverlayComposer([]string{"header", long}, -1, 0)

	// Find the second painted row of the wrapped logical line.
	var rows []int
	for i, ref := range r.vpOverlayRefs {
		if ref.line == 1 {
			rows = append(rows, i)
		}
	}
	if len(rows) < 2 {
		t.Fatalf("expected the long line to wrap, got %d rows", len(rows))
	}
	ref := r.vpOverlayRefs[rows[1]]
	line, col, ok := r.OverlayClickPosition(2, r.vpBodyHeight+1+rows[1])
	if !ok || line != 1 {
		t.Fatalf("expected the wrapped row to resolve to logical line 1, got %d ok=%v", line, ok)
	}
	if want := ref.logicalCol + 2; col != want {
		t.Fatalf("expected column %d inside the logical line, got %d", want, col)
	}
	// The column must name the same rune the row actually showed, which is what
	// makes the click land on the character under the pointer rather than on
	// whatever summing row widths would have suggested.
	wrapped := softWrapOverlayLine(long, 40-viewportRightPadding)
	if got, want := []rune(long)[col], []rune(wrapped[1])[2]; got != want {
		t.Fatalf("column %d points at %q, the clicked cell shows %q", col, string(got), string(want))
	}
}

// TestComposerClickLandsTheCaretOnTheClickedCell closes the loop the user
// actually sees: the click is resolved to an offset, the reader adopts it and
// re-renders, and the paint puts the hardware caret back on exactly the cell
// that was clicked.
func TestComposerClickLandsTheCaretOnTheClickedCell(t *testing.T) {
	r := newComposerRenderer(t, 80, 24)
	cursor := len("hello world")
	r.RenderComposerState(ComposerRenderState{Text: "hello world", Cursor: &cursor})

	row := composerTextRowScreen(r, 0)
	col := r.vpLastComposer.text.rows[0].prefixWidth + 6
	drainCaretRequest()
	clickAt(r, col, row)
	caret := drainCaretRequest()
	if caret < 0 {
		t.Fatal("click on the composer text should request a caret")
	}

	// What the raw input reader answers with, through run.go's draft handler.
	out := r.out.(*bytes.Buffer)
	out.Reset()
	r.RenderComposerState(ComposerRenderState{Text: "hello world", Cursor: &caret})

	want := fmt.Sprintf("\x1b[%d;%dH", row+1, col+1)
	if !strings.Contains(out.String(), want) {
		t.Fatalf("expected the caret to be repainted at the clicked cell %q, output:\n%q", want, out.String())
	}
}

// A withdrawn turn is the one turn that does not close with a "Worked for …"
// line, and the exception stops there: the request went out and was billed, so
// its tokens still have to reach the session totals the composer footer and the
// prompt-cache hit-rate numbers are computed from.
func TestReducerWithdrawnRunIsSilentButStillAccounted(t *testing.T) {
	tracker := NewTracker()
	var r Reducer
	r.WithTracker(tracker)
	turn := &foregroundTurn{}
	if ev := r.Reduce(RunStartedMsg{RunID: "r1", turn: turn}); len(ev.Frames) != 0 {
		t.Fatalf("run start produced frames: %#v", ev.Frames)
	}
	if !turn.withdraw() {
		t.Fatal("withdrawal refused before any response")
	}
	ev := r.Reduce(RunEndedMsg{RunID: "r1", turn: turn, WorkedDuration: 3 * time.Second, InputTokens: 1200, OutputTokens: 80})
	if len(ev.Frames) != 0 || ev.WorkedStatus != "" {
		t.Fatalf("withdrawn turn left residue: frames=%#v status=%q", ev.Frames, ev.WorkedStatus)
	}
	if got := tracker.SnapshotSession(); got.InputTokens != 1200 || got.OutputTokens != 80 {
		t.Fatalf("withdrawn run was not billed to the session: %+v", got)
	}
	// The next turn must be unaffected by the suppression that preceded it.
	_ = r.Reduce(RunStartedMsg{RunID: "r2"})
	if ev := r.Reduce(RunEndedMsg{RunID: "r2", WorkedDuration: 2 * time.Second}); ev.WorkedStatus == "" {
		t.Fatal("suppression leaked into the following turn")
	}
}

// Esc can land before the run-started notification is even dequeued, and the
// withdrawn turn's end can arrive after a whole new turn is under way. Neither
// ordering may disturb the turn that is actually running.
func TestReducerWithdrawnRunOutOfOrderLifecycle(t *testing.T) {
	tracker := NewTracker()
	var r Reducer
	r.WithTracker(tracker)

	withdrawn := &foregroundTurn{}
	withdrawn.withdraw()
	// Start arrives already withdrawn: nothing is rendered, and ending a run the
	// tracker never started must not throw or corrupt the session totals.
	if ev := r.Reduce(RunStartedMsg{RunID: "r1", turn: withdrawn}); len(ev.Frames) != 0 {
		t.Fatalf("withdrawn run start rendered: %#v", ev.Frames)
	}

	// A new turn begins and is genuinely running.
	live := &foregroundTurn{}
	_ = r.Reduce(RunStartedMsg{RunID: "r2", turn: live})

	// Now the withdrawn turn's end finally arrives, out of order.
	if ev := r.Reduce(RunEndedMsg{RunID: "r1", turn: withdrawn, WorkedDuration: 9 * time.Second, InputTokens: 700, OutputTokens: 20}); len(ev.Frames) != 0 || ev.WorkedStatus != "" {
		t.Fatalf("late withdrawn end produced residue: %#v / %q", ev.Frames, ev.WorkedStatus)
	}
	// Its tokens are still real and still counted.
	if got := tracker.SnapshotSession(); got.InputTokens != 700 || got.OutputTokens != 20 {
		t.Fatalf("withdrawn run lost its billing: %+v", got)
	}
	// The running turn must be untouched by that late message.
	if r.activeRunID != "r2" || r.activeTurn != live {
		t.Fatalf("late withdrawn end stole the live turn: activeRunID=%q activeTurn==live? %v", r.activeRunID, r.activeTurn == live)
	}
	ev := r.Reduce(RunEndedMsg{RunID: "r2", turn: live, WorkedDuration: 2 * time.Second})
	if ev.WorkedStatus == "" {
		t.Fatal("the live turn lost its Worked for line to the withdrawn one")
	}
}

// Everything the withdrawn turn produces late is addressed to that turn, so a
// notification still in the queue when it is withdrawn is dropped rather than
// rendered into whatever turn is current by the time it is processed.
func TestReducerDropsLateFramesFromAWithdrawnTurn(t *testing.T) {
	var r Reducer
	turn := &foregroundTurn{}
	turn.withdraw()
	for _, msg := range []any{
		NewMessageMsg{turn: turn, Msg: Message{Kind: MsgKindAssistant, Content: "late answer"}},
		NewMessageMsg{turn: turn, Msg: Message{Kind: MsgKindReasoning, Content: "late thinking"}},
		ReasoningDoneMsg{turn: turn},
		StreamResetMsg{turn: turn},
	} {
		if ev := r.Reduce(msg); len(ev.Frames) != 0 || ev.WorkedStatus != "" {
			t.Fatalf("%T from a withdrawn turn rendered: %#v", msg, ev)
		}
	}
}

// The retained user block is found by the turn identity it was rendered with,
// never by its text: two identical submissions must not be confusable.
func TestRendererWithdrawSubmissionRemovesOnlyItsOwnBlock(t *testing.T) {
	r := &Renderer{}
	first := &foregroundTurn{}
	second := &foregroundTurn{}
	r.vm.append(Frame{Kind: FrameUser, Title: "you", Content: "same text", turn: first})
	r.vm.append(Frame{Kind: FrameAssistant, Content: "an answer"})
	r.vm.append(Frame{Kind: FrameUser, Title: "you", Content: "same text", turn: second})
	r.withdrawSubmission(second)
	var kept []string
	for _, block := range r.vm.blocks {
		kept = append(kept, block.frame.Content)
	}
	if strings.Join(kept, "|") != "same text|an answer" {
		t.Fatalf("blocks after withdrawal = %v", kept)
	}
	if r.vm.blocks[0].frame.turn != first {
		t.Fatal("removed the wrong submission")
	}
	// A turn with no retained block (non-TTY, or already removed) is a no-op.
	r.withdrawSubmission(second)
	if len(r.vm.blocks) != 2 {
		t.Fatalf("repeat withdrawal changed the transcript: %d blocks", len(r.vm.blocks))
	}
}

// The painter used to rewrite the whole screen on every frame: each row was
// addressed, erased with EL and written again, whether or not anything about
// it had changed. Repaints are frequent — one per streamed delta, one per
// keystroke, plus the 200ms status, 500ms caret-blink and 120ms compact
// tickers — and a terminal presents whenever it likes inside that byte stream,
// so it could sample a row after the erase and before the rewrite. Static
// content (the composer's rules, transcript text already on screen) therefore
// blanked and reappeared many times a second. The tests below pin the
// properties that make that impossible.

// paintFrame repaints the viewport and returns the bytes it wrote.
func paintFrame(t *testing.T, r *Renderer, out *bytes.Buffer) string {
	t.Helper()
	out.Reset()
	r.mu.Lock()
	r.paintViewportLocked()
	r.mu.Unlock()
	return out.String()
}

// erasedRows lists the 1-based screen rows a paint erased and rewrote.
func erasedRows(paint string) []int {
	var rows []int
	for _, m := range regexp.MustCompile(`\x1b\[(\d+);1H\x1b\[2K`).FindAllStringSubmatch(paint, -1) {
		n, _ := strconv.Atoi(m[1])
		rows = append(rows, n)
	}
	return rows
}

func TestViewportRepaintOfAnUnchangedScreenTouchesNoRow(t *testing.T) {
	r := newComposerRenderer(t, 80, 24)
	out := r.out.(*bytes.Buffer)
	cursor := len("hello")
	r.RenderComposerState(ComposerRenderState{Text: "hello", Cursor: &cursor})
	r.RenderFrame(Frame{Kind: FrameAssistant, Content: "already on screen", Final: true})

	// Two repaints of identical state: the second must find nothing to do.
	paintFrame(t, r, out)
	if rows := erasedRows(paintFrame(t, r, out)); len(rows) != 0 {
		t.Fatalf("a repaint with nothing changed must not erase any row, erased %v", rows)
	}
}

func TestViewportTypingLeavesTheComposerRulesAndTranscriptAlone(t *testing.T) {
	r := newComposerRenderer(t, 80, 24)
	out := r.out.(*bytes.Buffer)
	r.RenderFrame(Frame{Kind: FrameAssistant, Content: "already on screen", Final: true})
	cursor := len("hell")
	r.RenderComposerState(ComposerRenderState{Text: "hell", Cursor: &cursor})
	paintFrame(t, r, out)

	// One more keystroke. Only the row carrying the composer text may move.
	cursor++
	out.Reset()
	r.RenderComposerState(ComposerRenderState{Text: "hello", Cursor: &cursor})
	rows := erasedRows(out.String())
	textRow := composerTextRowScreen(r, 0) + 1 // erasedRows reports 1-based rows
	if len(rows) != 1 || rows[0] != textRow {
		t.Fatalf("typing one character must repaint only the composer text row %d, repainted %v\npaint: %q",
			textRow, rows, out.String())
	}
}

func TestViewportStreamingScrollsTheTranscriptInsteadOfRewritingIt(t *testing.T) {
	r := newComposerRenderer(t, 80, 24)
	out := r.out.(*bytes.Buffer)
	cursor := 0
	r.RenderComposerState(ComposerRenderState{Text: "", Cursor: &cursor})
	// Fill the transcript past the bottom so follow mode is scrolling.
	lines := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("- line %02d", i))
	}
	r.RenderFrame(Frame{Kind: FrameAssistant, Content: strings.Join(lines, "\n"), Final: false})
	paintFrame(t, r, out)

	// One more streamed line shifts the whole visible transcript up by one.
	out.Reset()
	r.RenderFrame(Frame{Kind: FrameAssistant, Content: strings.Join(append(lines, "- line 40"), "\n"), Final: false})
	paint := out.String()

	if !strings.Contains(paint, "\x1b[1S") {
		t.Fatalf("a streamed line that shifts the transcript up must scroll the region (\\x1b[1S), got %q", paint)
	}
	if !strings.Contains(paint, "\x1b[r") {
		t.Fatalf("the scrolling region must be restored after the scroll, got %q", paint)
	}
	if rows := erasedRows(paint); len(rows) > 3 {
		t.Fatalf("a one-row shift must repaint only the rows it exposed, repainted %d rows %v\npaint: %q",
			len(rows), rows, paint)
	}
	// The screen must still be exactly the frame it was asked to paint.
	r.mu.Lock()
	defer r.mu.Unlock()
	vr := renderViewport(r.activeVMLocked(), 80-viewportRightPadding, r.vpBodyHeight, 1<<30, r.diffTheme, r.cwd)
	for i, want := range vr.lines {
		if r.vpPainted[i] != want {
			t.Fatalf("after the scroll, screen row %d is %q, want %q", i+1, r.vpPainted[i], want)
		}
	}
}

func TestViewportFrameIsWrittenInsideSynchronizedOutput(t *testing.T) {
	r := newComposerRenderer(t, 80, 24)
	out := r.out.(*bytes.Buffer)
	cursor := 0
	r.RenderComposerState(ComposerRenderState{Text: "", Cursor: &cursor})

	out.Reset()
	r.RenderFrame(Frame{Kind: FrameAssistant, Content: "a new reply", Final: true})
	paint := out.String()
	// Synchronized output (DEC private mode 2026) makes the terminal present
	// the whole frame at once instead of sampling the screen part-way through
	// it. Terminals that do not implement it ignore the unknown private mode.
	if !strings.HasPrefix(paint, "\x1b[?2026h") || !strings.HasSuffix(paint, "\x1b[?2026l") {
		t.Fatalf("a frame must be written as one synchronized update, got %q", paint)
	}
	if n := strings.Count(paint, "\x1b[?2026h"); n != 1 {
		t.Fatalf("a frame must be one synchronized update, got %d, paint %q", n, paint)
	}
}

// TestViewportPaintedRowsAreSelfContained pins the invariant the damage
// comparison rests on: a row's bytes must not depend on the rows painted
// before it, or skipping an unchanged row could change how a later one looks.
// Colour is forced on because below a TTY lipgloss renders colourless
// strings, which would let every SGR assertion here pass vacuously.
func TestViewportPaintedRowsAreSelfContained(t *testing.T) {
	forceColorProfile(t)
	r := newComposerRenderer(t, 100, 40)
	out := r.out.(*bytes.Buffer)
	cursor := len("hello")
	r.RenderComposerState(ComposerRenderState{Text: "hello", Cursor: &cursor})
	for _, f := range []Frame{
		{Kind: FrameUser, Content: "please refactor the painter"},
		{Kind: FrameAssistant, Content: "**bold**, `code` and a block:\n\n```go\nfunc main() {}\n```\n", Final: true},
		{Kind: FrameThinking, Content: "thinking\nabout it", Final: true},
		{Kind: FrameTool, Title: "read_file", Content: "one\ntwo\nthree\nfour\nfive", Final: true},
		{Kind: FrameError, Content: "something failed"},
		{Kind: FrameSystem, Content: "a system note"},
		// A shell header whose command wraps the coloured span across several
		// rows: the exact shape that used to repaint continuations white.
		{Kind: FrameTool, Title: "shell", Content: "ok", Final: true,
			ToolMeta: tool.ToolMeta{Input: map[string]any{"command": longShellCommand}}},
		// Inline styles the paragraph wrapper breaks mid-span.
		{Kind: FrameAssistant, Final: true,
			Content: "Please check `readFileContentsForTheLongRunningMigrationToolAndReportProgressEverywhere` and " +
				"**a very bold statement that rambles on long enough to cross the wrap boundary of this row for sure**."},
	} {
		r.RenderFrame(f)
	}
	paintFrame(t, r, out)

	func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		assertRowsDrawIndependently(t, r.vpPainted)
		for i, row := range r.vpPainted {
			if state := sgrStateAfter(row); state != "" {
				t.Fatalf("screen row %d leaves SGR %q in effect, so the row after it is not self-contained: %q",
					i+1, state, row)
			}
		}
	}()

	// A modal overlay reflows and re-slices its line table on every paint and
	// every scroll, so its rows answer the same invariant — including the
	// first row a scroll exposes, which no earlier row precedes on screen.
	drainCaretRequest()
	r.BeginComposerOverlay()
	options := make([]string, 0, 16)
	for i := 0; i < 16; i++ {
		options = append(options, lipgloss.NewStyle().Foreground(lipgloss.Color("214")).
			Render(fmt.Sprintf("option %d — a long enough description that it wraps onto more than one row of the overlay area at this width", i)))
	}
	r.SetSoftWrappingOverlayComposer(options, -1, 0)
	drainCaretRequest()
	r.mu.Lock()
	assertRowsDrawIndependently(t, r.vpPainted)
	r.mu.Unlock()

	if !r.ViewportScrollOverlay(5) {
		t.Fatal("the overlay should have had room to scroll")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	assertRowsDrawIndependently(t, r.vpPainted)
}

// longShellCommand is long enough that its coloured header span must wrap
// onto at least three rows at the tests' viewport width.
const longShellCommand = "CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/turn ./pkg/agent ./pkg/assembly " +
	"./pkg/run ./pkg/tool ./pkg/llm ./pkg/mcp ./pkg/state ./pkg/skill ./pkg/memory " +
	"-run TestSomethingVeryLongIndeed -count=1 -v ./internal/... ./pkg/... ./cmd/..."

// forceColorProfile turns the global lipgloss profile up to ANSI256 for the
// duration of the test. Below a TTY lipgloss renders colourless strings, so
// styled rows would carry no SGR bytes and every colour assertion would pass
// vacuously. The profile is process global: a test using this helper must not
// call t.Parallel().
func forceColorProfile(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

// rowColumnStates walks row and reports, for every visible rune, the SGR state
// in effect at that cell when the row is drawn starting from state. SGR
// sequences update the state; every other rune is a visible cell. The end
// state is what a row drawn after this one inherits.
func rowColumnStates(row, state string) (cells []string, end string) {
	end = state
	for i := 0; i < len(row); {
		if row[i] == 0x1b {
			seq, n := paintEscapeSequence(row[i:])
			if n == 0 {
				i++
				continue
			}
			if seq[len(seq)-1] == 'm' {
				params := seq[2 : len(seq)-1]
				if params == "" || params == "0" {
					end = ""
				} else {
					if end != "" {
						end += ";"
					}
					end += params
				}
			}
			i += n
			continue
		}
		_, size := utf8.DecodeRuneInString(row[i:])
		i += size
		cells = append(cells, end)
	}
	return cells, end
}

// assertRowsDrawIndependently pins the invariant the damage comparison rests
// on: drawing each row alone from the default SGR state must put the same
// style on every visible cell as drawing all rows in sequence does. This
// catches both halves of the wrapped-white-header bug — a row that leaves a
// style open and a row that relies on the row above it to open one.
func assertRowsDrawIndependently(t *testing.T, rows []string) {
	t.Helper()
	carry := ""
	for i, row := range rows {
		inSequence, next := rowColumnStates(row, carry)
		alone, _ := rowColumnStates(row, "")
		if !slices.Equal(inSequence, alone) {
			t.Fatalf("screen row %d is not self-contained: drawn alone its cells carry %q, drawn in sequence they carry %q: %q",
				i+1, strings.Join(alone, "|"), strings.Join(inSequence, "|"), row)
		}
		carry = next
	}
}

// sgrStateAfter returns the SGR parameters still in effect at the end of s,
// empty when it ends in the default state.
func sgrStateAfter(s string) string {
	state := ""
	for _, m := range regexp.MustCompile(`\x1b\[([0-9;]*)m`).FindAllStringSubmatch(s, -1) {
		if m[1] == "" || m[1] == "0" {
			state = ""
			continue
		}
		if state != "" {
			state += ";"
		}
		state += m[1]
	}
	return state
}

func TestVerticalShiftPicksTheScrollThatSavesTheMostRows(t *testing.T) {
	rows := func(from, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("row %02d", from+i)
		}
		return out
	}
	old := rows(0, 10)

	// The transcript grew by two rows at the bottom: content moved up by two.
	up := append(rows(2, 8), "new A", "new B")
	if got := verticalShift(old, up, 10); got != 2 {
		t.Fatalf("a two-row advance should scroll up by 2, got %d", got)
	}
	// The reader scrolled back by two: content moved down by two.
	down := append([]string{"older A", "older B"}, rows(0, 8)...)
	if got := verticalShift(old, down, 10); got != -2 {
		t.Fatalf("a two-row scroll back should scroll down by 2, got %d", got)
	}
	// Unrelated content shares no rows, so there is nothing to save.
	if got := verticalShift(old, rows(100, 10), 10); got != 0 {
		t.Fatalf("an unrelated frame must not scroll, got %d", got)
	}
	// A shift that rescues fewer rows than it costs is not worth issuing.
	if got := verticalShift(old, append(rows(9, 1), rows(100, 9)...), 10); got != 0 {
		t.Fatalf("a shift saving one row must not scroll, got %d", got)
	}
}

// TestViewportResizeRepaintsEveryRow guards the one thing damage tracking must
// not get wrong: after the terminal reflows its own grid, nothing the painter
// remembers about the screen still holds.
func TestViewportResizeRepaintsEveryRow(t *testing.T) {
	r := newComposerRenderer(t, 80, 24)
	out := r.out.(*bytes.Buffer)
	cursor := 0
	r.RenderComposerState(ComposerRenderState{Text: "", Cursor: &cursor})
	r.RenderFrame(Frame{Kind: FrameAssistant, Content: "already on screen", Final: true})

	out.Reset()
	r.ViewportResize()
	got := erasedRows(out.String())
	if len(got) != 24 {
		t.Fatalf("a resize must repaint all 24 rows, repainted %d: %v", len(got), got)
	}
}

// TestViewportResizeErasesTheDisplayFirst covers the residue a narrowed
// terminal left behind: macOS Terminal keeps each row's cells past the new
// right edge out of sight, and erasing a row drops only its visible part, so
// the hidden tail of the wider layout slid into view on the rows the narrower
// frame left short. The repaint after a geometry change must erase the whole
// display before it writes any row; an ordinary repaint must not, or every
// keystroke would blank the screen.
func TestViewportResizeErasesTheDisplayFirst(t *testing.T) {
	r := newComposerRenderer(t, 120, 24)
	out := r.out.(*bytes.Buffer)
	cursor := 0
	r.RenderComposerState(ComposerRenderState{Text: "", Cursor: &cursor})
	r.RenderFrame(Frame{Kind: FrameAssistant, Content: "already on screen", Final: true})

	out.Reset()
	r.RenderComposerState(ComposerRenderState{Text: "h", Cursor: &cursor})
	if strings.Contains(out.String(), "\x1b[2J") {
		t.Fatalf("an ordinary repaint must not erase the display, got %q", out.String())
	}

	forcedTermWidth = 80
	out.Reset()
	r.ViewportResize()
	paint := out.String()
	erase := strings.Index(paint, "\x1b[2J")
	if erase < 0 {
		t.Fatalf("the repaint after narrowing must erase the display, got %q", paint)
	}
	if row := regexp.MustCompile(`\x1b\[\d+;1H`).FindStringIndex(paint); row == nil || row[0] < erase {
		t.Fatalf("the display must be erased before any row is written, got %q", paint)
	}
}

// TestSoftwareCaretMoveLeavesNoSecondCaret covers the other half of painting
// the caret as one cell: when it moves without the line changing, the cell it
// left must be put back, or the screen shows two carets.
func TestSoftwareCaretMoveLeavesNoSecondCaret(t *testing.T) {
	r := newComposerRenderer(t, 80, 24)
	out := r.out.(*bytes.Buffer)
	cursor := len("hello")
	r.RenderComposerState(ComposerRenderState{Text: "hello", Cursor: &cursor})
	r.EnableSoftwareCursor()

	// Arrow left: the composer text is untouched, only the caret moves.
	cursor--
	out.Reset()
	r.RenderComposerState(ComposerRenderState{Text: "hello", Cursor: &cursor})
	paint := out.String()

	if strings.Contains(paint, "\x1b[2K") {
		t.Fatalf("moving the caret must not erase the line being typed on, got %q", paint)
	}
	// Exactly two cells are addressed: the one the caret left, put back without
	// reverse video, and the one it moved to, painted with it.
	cells := regexp.MustCompile(`\x1b\[(\d+);(\d+)H`).FindAllStringSubmatchIndex(paint, -1)
	if len(cells) < 2 {
		t.Fatalf("a caret move must address the cell it left and the cell it took, got %q", paint)
	}
	left, took := paint[cells[0][0]:cells[1][0]], paint[cells[1][0]:]
	if strings.Contains(left, "\x1b[7m") {
		t.Fatalf("the cell the caret left must be restored without reverse video, got %q", left)
	}
	if !strings.Contains(left, " ") {
		t.Fatalf("the cell the caret left must be repainted with its own content, got %q", left)
	}
	if !strings.Contains(took, "\x1b[7mo\x1b[0m") {
		t.Fatalf("the caret must be painted over the rune at its new cell, got %q", took)
	}
	caretRow := composerTextRowScreen(r, 0) + 1
	// The prompt marker is two columns wide, so the caret sat one column past
	// "hello" and moves one column back; both are 1-based screen columns.
	wantLeft := fmt.Sprintf("\x1b[%d;%dH", caretRow, 2+len("hello")+1)
	wantTook := fmt.Sprintf("\x1b[%d;%dH", caretRow, 2+len("hell")+1)
	if !strings.HasPrefix(left, wantLeft) || !strings.HasPrefix(took, wantTook) {
		t.Fatalf("caret move should restore %q and paint %q, got %q then %q", wantLeft, wantTook, left, took)
	}
}

// hasCursorMovingByte reports whether s carries any byte the terminal acts on
// beyond printing a glyph inside the current row: a C0 control (tab, newline,
// carriage return, bell, …) or a CSI sequence that is neither SGR (…)m nor EL
// (…)K. One row of a damage-tracked canvas must never contain one.
func hasCursorMovingByte(s string) bool {
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			seq, n := paintEscapeSequence(s[i:])
			if n == 0 || !paintKeepsSequence(seq) {
				return true
			}
			i += n
			continue
		}
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
		i++
	}
	return false
}

// paintedRowDisplayWidth measures the columns a row's bytes occupy, skipping
// the SGR and EL sequences a row may carry. Same model as fitPaintRow.
func paintedRowDisplayWidth(s string) int {
	col := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			_, n := paintEscapeSequence(s[i:])
			i += max(n, 1)
			continue
		}
		if s[i] < 0x20 {
			i++
			continue
		}
		rn, size := utf8.DecodeRuneInString(s[i:])
		col += runewidth.RuneWidth(rn)
		i += size
	}
	return col
}

func TestFitPaintRowKeepsOnePhysicalRow(t *testing.T) {
	const reset = "\x1b[0m"
	cases := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{"plain ascii", "hello world", 80, "hello world"},
		{"cjk fits", "你好世界", 80, "你好世界"},
		{"cjk exact budget", "你好", 4, "你好"},
		{"cjk cut before a wide rune", "你好", 3, "你" + reset},
		{"east asian ambiguous is one cell", "a±b│", 80, "a±b│"},
		{"tab after one column", "a\tb", 80, "a       b"},
		{"tab after two columns", "ab\tc", 80, "ab      c"},
		{"tab at column zero", "\tx", 80, "        x"},
		{"newline dropped", "a\nb", 80, "ab"},
		{"carriage return dropped", "a\rb", 80, "ab"},
		{"vertical tab dropped", "a\x0bb", 80, "ab"},
		{"form feed dropped", "a\x0cb", 80, "ab"},
		{"backspace dropped", "a\bb", 80, "ab"},
		{"bell dropped", "a\x07b", 80, "ab"},
		{"sgr kept", "\x1b[2mhi\x1b[0m", 80, "\x1b[2mhi\x1b[0m"},
		{"el kept", "hi\x1b[K", 80, "hi\x1b[K"},
		{"el2 kept", "\x1b[2Khi", 80, "\x1b[2Khi"},
		{"erase display dropped", "a\x1b[2Jb", 80, "ab"},
		{"cursor address dropped", "a\x1b[1;2Hb", 80, "ab"},
		{"cursor hide dropped", "a\x1b[?25hb", 80, "ab"},
		{"scroll dropped", "a\x1b[1Sb", 80, "ab"},
		{"one under the budget", "abcde", 6, "abcde"},
		{"exact budget", "abcde", 5, "abcde"},
		{"one over the budget", "abcde", 4, "abcd" + reset},
		{"cut drops the tail including its el", "hello\x1b[K", 4, "hell" + reset},
		{"cut closes an open sgr", "\x1b[2mabcdef\x1b[0m", 4, "\x1b[2mabcd" + reset},
		{"empty row", "", 80, ""},
		{"width floor", "abc", 0, "a" + reset},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fitPaintRow(tc.in, tc.width)
			if got != tc.want {
				t.Fatalf("fitPaintRow(%q, %d) = %q, want %q", tc.in, tc.width, got, tc.want)
			}
			if again := fitPaintRow(got, tc.width); again != got {
				t.Fatalf("fitPaintRow is not a fixed point: %q became %q", got, again)
			}
			if hasCursorMovingByte(got) {
				t.Fatalf("result still carries a cursor-moving byte: %q", got)
			}
			if w := paintedRowDisplayWidth(got); w > max(tc.width, 1) {
				t.Fatalf("result paints %d columns on a %d-column row: %q", w, tc.width, got)
			}
		})
	}
}

// TestFitPaintRowKeepsAFlushFooterRowIntact pins the canvas budget decision:
// layoutComposerFooter lays the footer out to exactly the terminal width
// (sharedBlockFooterTruncateExtraRoom is zero on purpose, so the token stats
// sit flush against the right edge), and a row of exactly the terminal width
// occupies one physical row — the deferred autowrap is cleared by the next
// absolute CUP. Clipping the canvas at width-1 would slice one column off the
// token stats on every frame, so the canvas budget is the full width and the
// one-column courtesy margin stays with the producers.
func TestFitPaintRowKeepsAFlushFooterRowIntact(t *testing.T) {
	footer := layoutComposerFooter("glm · high", "177 tools · 33510k in / 162k out", 80)
	if w := lipgloss.Width(footer); w != 80 {
		t.Fatalf("the fixture must be a flush footer, got %d columns", w)
	}
	// The production footer row carries no trailing \x1b[K: the row spans the
	// full width, and an EL would erase the last painted cell on terminals
	// that keep the deferred wrap pending through it.
	row := "\x1b[2m" + footer + "\x1b[0m"
	if got := fitPaintRow(row, 80); got != row {
		t.Fatalf("a flush footer row must pass through the canvas unchanged, got %q", got)
	}
}

// canvasRowSegments splits one paint's bytes into the content written per
// \x1b[row;1H\x1b[2K row write: everything before the first write is frame
// setup, and a write runs until the next write, a caret address or the frame
// trailer.
func canvasRowSegments(paint string) []string {
	parts := regexp.MustCompile(`\x1b\[\d+;1H\x1b\[2K`).Split(paint, -1)
	parts = parts[1:]
	end := regexp.MustCompile(`\x1b\[\?\d+[hl]|\x1b\[\d+;\d+H`)
	for i, seg := range parts {
		if loc := end.FindStringIndex(seg); loc != nil {
			parts[i] = seg[:loc[0]]
		}
	}
	return parts
}

// assertPaintedCanvas is the invariant the damage comparison rests on, checked
// two ways: the shadow rows (what the painter believes is on screen) and the
// raw bytes of this paint's row writes must both stay inside one physical row
// each — no byte the terminal acts on beyond printing, no row wider than the
// terminal.
func assertPaintedCanvas(t *testing.T, r *Renderer, paint string, width int) {
	t.Helper()
	for i, seg := range canvasRowSegments(paint) {
		if hasCursorMovingByte(seg) {
			t.Fatalf("row write %d carries bytes the terminal acts on beyond one row: %q", i+1, seg)
		}
		if w := paintedRowDisplayWidth(seg); w > width {
			t.Fatalf("row write %d paints %d columns on a %d-column terminal: %q", i+1, w, width, seg)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, row := range r.vpPainted {
		if hasCursorMovingByte(row) {
			t.Fatalf("screen row %d carries a cursor-moving byte: %q", i+1, row)
		}
		if w := paintedRowDisplayWidth(row); w > width {
			t.Fatalf("screen row %d paints %d columns on a %d-column terminal: %q", i+1, w, width, row)
		}
	}
}

// TestCanvasRowsNeverBreakTheirPhysicalRow drives the real paint path with
// exactly the content that used to spill — tabs and newlines in a model-written
// status label, tabbed tool output — and holds the canvas to one physical row
// per synthesized row. The second frame shrinks the status row, the exact
// sequence that used to leave the spilled tail on a spacer row no write ever
// touched again.
func TestCanvasRowsNeverBreakTheirPhysicalRow(t *testing.T) {
	const width = 60
	r := newComposerRenderer(t, width, 24)
	out := r.out.(*bytes.Buffer)
	cursor := 0
	r.RenderComposerState(ComposerRenderState{Text: "", Cursor: &cursor})
	for _, f := range []Frame{
		{Kind: FrameUser, Content: "请把表格整理一下"},
		{Kind: FrameAssistant, Content: "**bold** and a table:\n\n| a | b |\n|---|---|\n| 你好 | world |\n", Final: true},
		{Kind: FrameThinking, Content: "thinking\nabout it", Final: true},
		{Kind: FrameTool, Title: "shell", Content: "col1\tcol2\ncrlf\r\nline\rbell\x07", Final: true},
		{Kind: FrameError, Content: "something failed"},
	} {
		r.RenderFrame(f)
	}
	r.RenderTransientStatus("test", "Tasks 0/2 · implement\tregistry\nand more · 177 tools · 33510k in / 162k out")
	assertPaintedCanvas(t, r, paintFrame(t, r, out), width)

	r.RenderTransientStatus("test", "Tasks 1/2 · done")
	assertPaintedCanvas(t, r, paintFrame(t, r, out), width)
}

// TestComposerVariantsStayWithinOnePhysicalRow walks the composer's block
// variants through the same canvas invariant: the plain card, a wrapped draft,
// the slash variant, option overlay rows (whose text is producer-built and may
// carry tabs) and the queued-input preview.
func TestComposerVariantsStayWithinOnePhysicalRow(t *testing.T) {
	const width = 60
	r := newComposerRenderer(t, width, 24)
	out := r.out.(*bytes.Buffer)
	cursor := 0
	variants := []struct {
		name string
		cs   ComposerRenderState
	}{
		{"empty", ComposerRenderState{Text: "", Cursor: &cursor}},
		{"long wrapped draft", ComposerRenderState{Text: strings.Repeat("type and wrap ", 20), Cursor: &cursor}},
		{"slash command", ComposerRenderState{Text: "/help me write a very long slash command", Cursor: &cursor}},
		{"overlay rows", ComposerRenderState{Text: "/x", Cursor: &cursor, OverlayRows: []OverlayRow{
			{Text: "plain option with\ttab"},
			{Text: "selected option", Selected: true},
		}}},
		{"slash menu", ComposerRenderState{Text: "/x", Cursor: &cursor, SlashMenu: &slashPanel{
			body: []panelLine{
				{lead: panelIndent, text: "Commands", style: &panelAccentStyle},
				{lead: panelCursor + "/x    ", text: "a description with\ttab long enough to wrap under its own column"},
			},
			focusStart: 0, focusEnd: 2,
			hint: "↑/↓ to navigate · Tab to complete · Enter to run · Esc to close",
		}}},
		{"queued preview", ComposerRenderState{Text: "", Cursor: &cursor, PendingInput: ComposerPendingInputPreview{QueuedMessages: []string{"queued\tfollow-up"}}}},
	}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			r.RenderComposerState(v.cs)
			assertPaintedCanvas(t, r, paintFrame(t, r, out), width)
		})
	}
}

// TestForceRepaintRealignsTheShadow is Ctrl+L's contract: whatever the shadow
// believed, the next paint after a forced repaint addresses every row.
func TestForceRepaintRealignsTheShadow(t *testing.T) {
	r := newComposerRenderer(t, 80, 24)
	out := r.out.(*bytes.Buffer)
	cursor := 0
	r.RenderComposerState(ComposerRenderState{Text: "", Cursor: &cursor})
	r.RenderFrame(Frame{Kind: FrameAssistant, Content: "already on screen", Final: true})
	paintFrame(t, r, out)

	out.Reset()
	r.ForceRepaint()
	rows := erasedRows(out.String())
	if len(rows) != 24 {
		t.Fatalf("a forced repaint must address all 24 rows, addressed %v", rows)
	}
	// And it must settle: a repaint of the unchanged screen afterwards finds
	// nothing to do, so the free flicker-free behavior is intact.
	if rows := erasedRows(paintFrame(t, r, out)); len(rows) != 0 {
		t.Fatalf("after a forced repaint an unchanged screen must not erase any row, erased %v", rows)
	}
}

// A /goal draws its own lines: the objective it opened with, each further
// round with the check's reason for it, and how it ended. A line the check
// decided opens that check's view. Each line closes whatever the round before
// said, so rounds never run together into one paragraph.
func TestReducerDrawsAGoalsLines(t *testing.T) {
	tracker := NewTracker()
	r := (&Reducer{}).WithTracker(tracker)
	_ = r.Reduce(RunStartedMsg{RunID: "r1"})

	started := r.Reduce(GoalStartedMsg{RunID: "r1", Objective: "make the tests pass"})
	if len(started.Frames) != 1 {
		t.Fatalf("started frames = %#v", started.Frames)
	}
	if f := started.Frames[0]; f.Kind != FrameGoal || !f.Final || f.Title != goalTitleStarted || f.Content != "make the tests pass" || !strings.Contains(f.Summary, "esc stops") || f.AgentID != "" {
		t.Fatalf("started frame = %#v", f)
	}
	if c := tracker.SnapshotActiveRun(); c.GoalRound != 1 || c.GoalChecking {
		t.Fatalf("counters after the goal started = %+v", c)
	}

	_ = r.Reduce(NewMessageMsg{Msg: Message{Kind: MsgKindAssistant, RunID: "r1", Content: "fixed the parser"}})
	spawned := r.Reduce(SubagentSpawnedMsg{AgentID: "check-1", AgentType: "goal-evaluator", Title: "Goal check", Task: "Objective: make the tests pass"})
	for _, f := range spawned.Frames {
		if f.SubagentLifecycleCard {
			t.Fatalf("the goal's check drew a card of its own: %#v", f)
		}
	}
	if c := tracker.SnapshotActiveRun(); !c.GoalChecking || c.GoalRound != 1 {
		t.Fatalf("counters while the check runs = %+v", c)
	}
	ended := r.Reduce(SubagentEndedMsg{AgentID: "check-1", AgentType: "goal-evaluator", Status: "ok", Output: `{"status":"continue"}`})
	for _, f := range ended.Frames {
		if f.SubagentLifecycleCard {
			t.Fatalf("the goal's check drew a closing card: %#v", f)
		}
	}
	if c := tracker.SnapshotActiveRun(); c.GoalChecking {
		t.Fatalf("counters after the check = %+v", c)
	}

	round := r.Reduce(GoalRoundStartedMsg{RunID: "r1", Round: 2, Why: "two tests still fail", CheckAgentID: "check-1"})
	if len(round.Frames) != 2 || round.Frames[0].Kind != FrameAssistant || !round.Frames[0].Final || round.Frames[0].Content != "fixed the parser" {
		t.Fatalf("round 2 must first close round 1's answer: %#v", round.Frames)
	}
	if f := round.Frames[1]; f.Kind != FrameGoal || f.Title != "Round 2" || f.Content != "two tests still fail" || f.AgentID != "check-1" || !f.SubagentLifecycleCard || !strings.Contains(f.Summary, "click to see the check") {
		t.Fatalf("round frame = %#v", f)
	}
	if c := tracker.SnapshotActiveRun(); c.GoalRound != 2 {
		t.Fatalf("counters in round 2 = %+v", c)
	}

	_ = r.Reduce(NewMessageMsg{Msg: Message{Kind: MsgKindAssistant, RunID: "r1", Content: "all green"}})
	done := r.Reduce(GoalCompletedMsg{RunID: "r1", Payload: event.GoalCompletedPayload{Status: event.GoalStatusDone, Rounds: 2, Why: "42 tests pass", DurationMs: 7_000, CheckAgentID: "check-2"}})
	if len(done.Frames) != 2 || done.Frames[0].Content != "all green" {
		t.Fatalf("the goal's end must first close the last round's answer: %#v", done.Frames)
	}
	if f := done.Frames[1]; f.Title != goalTitleDone || f.Summary != "2 rounds · 7s · click to see the check" || f.Content != "42 tests pass" || f.AgentID != "check-2" {
		t.Fatalf("done frame = %#v", f)
	}
	if c := tracker.SnapshotActiveRun(); c.GoalRound != 0 || c.GoalChecking {
		t.Fatalf("counters after the goal ended = %+v", c)
	}
}

func TestGoalCompletedFrameSaysHowTheGoalEnded(t *testing.T) {
	tests := []struct {
		payload event.GoalCompletedPayload
		title   string
		summary string
	}{
		{event.GoalCompletedPayload{Status: event.GoalStatusDone, Rounds: 1, DurationMs: 3_000}, goalTitleDone, "1 round · 3s"},
		{event.GoalCompletedPayload{Status: event.GoalStatusStuck, Rounds: 4, DurationMs: 72_000, Why: "the same edit twice"}, goalTitleStuck, "4 rounds · 1m 12s"},
		{event.GoalCompletedPayload{Status: event.GoalStatusCapped, Rounds: 100, DurationMs: 1_000}, goalTitleCapped, "100 rounds · 1s"},
		{event.GoalCompletedPayload{Status: event.GoalStatusInterrupted, Rounds: 3}, goalTitleInterrupted, "interrupted in round 3"},
		{event.GoalCompletedPayload{Status: event.GoalStatusFailed, Rounds: 2, Why: "529 overloaded"}, goalTitleFailed, "after round 2"},
		{event.GoalCompletedPayload{Status: event.GoalStatusFailed, Rounds: 2}, goalTitleFailed, "round 2 failed"},
	}
	for _, tt := range tests {
		f := goalCompletedFrame("r1", tt.payload)
		if f.Title != tt.title || f.Summary != tt.summary || f.Content != tt.payload.Why {
			t.Errorf("%s: frame = %q / %q / %q, want %q / %q", tt.payload.Status, f.Title, f.Summary, f.Content, tt.title, tt.summary)
		}
	}
}

// A goal line reads at the brightness of the conversation around it: its
// marker and title in the goal's colour, the rest never dimmed, and the whole
// of what it is about below it.
func TestRenderGoalLine(t *testing.T) {
	useANSI256ColorProfile(t)
	objective := strings.Repeat("make every package build and every test pass ", 6)
	lines := renderFrameLines(goalStartedFrame("r1", objective), 60, DiffThemeDark, 0)
	painted := stripAllANSI(strings.Join(lines, "\n"))
	squeeze := func(s string) string { return strings.Join(strings.Fields(strings.ReplaceAll(s, "└", "")), "") }
	if !strings.HasPrefix(squeeze(painted), squeeze("◎ Goal · works in rounds until a check of the workspace finds it met · esc stops")) {
		t.Fatalf("goal header:\n%s", painted)
	}
	if !strings.Contains(squeeze(painted), squeeze(objective)) {
		t.Fatalf("the objective must be shown whole:\n%s", painted)
	}
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "\x1b[2m") {
		t.Fatalf("a goal line must not be dimmed: %q", joined)
	}
	if !strings.Contains(joined, "38;5;214") || !strings.Contains(joined, "38;5;252") {
		t.Fatalf("goal line colours: %q", joined)
	}

	for _, tt := range []struct {
		frame  Frame
		marker string
	}{
		{goalRoundFrame("r1", 2, "why", ""), "↻ Round 2"},
		{goalCompletedFrame("r1", event.GoalCompletedPayload{Status: event.GoalStatusDone, Rounds: 1}), "✓ Goal met"},
		{goalCompletedFrame("r1", event.GoalCompletedPayload{Status: event.GoalStatusStuck, Rounds: 1}), "■ Goal stopped: no progress"},
		{goalCompletedFrame("r1", event.GoalCompletedPayload{Status: event.GoalStatusInterrupted, Rounds: 1}), "○ Goal stopped"},
		{goalCompletedFrame("r1", event.GoalCompletedPayload{Status: event.GoalStatusFailed, Rounds: 1}), "✗ Goal stopped: error"},
	} {
		got := stripAllANSI(strings.Join(renderFrameLines(tt.frame, 80, DiffThemeDark, 0), "\n"))
		if !strings.Contains(got, tt.marker) {
			t.Errorf("want %q in:\n%s", tt.marker, got)
		}
	}
}

func TestWorkingLineNamesTheGoalsRound(t *testing.T) {
	for _, tt := range []struct {
		counters Counters
		want     string
	}{
		{Counters{}, " Working ("},
		{Counters{GoalRound: 3}, " Working toward the goal · round 3 ("},
		{Counters{GoalRound: 3, GoalChecking: true}, " Checking whether the goal is met · after round 3 ("},
	} {
		counters := tt.counters
		status := workingStatusFormatter(func() Counters { return counters }, nil)
		if got := stripAllANSI(status(time.Second, 0, "")); !strings.Contains(got, tt.want) {
			t.Errorf("working line = %q, want %q", got, tt.want)
		}
	}
}

// A wrapped overlay row hangs under its text: the continuation lines up after
// the indentation and the bullet.
func TestSoftWrapOverlayLineHangsUnderTheText(t *testing.T) {
	rows := softWrapOverlayLine("    · /a/very/long/project/path — will not load: not a version-controlled project", 40)
	if len(rows) < 2 {
		t.Fatalf("rows = %q", rows)
	}
	if !strings.HasPrefix(rows[0], "    · /a/very") {
		t.Fatalf("first row = %q", rows[0])
	}
	for _, row := range rows[1:] {
		if !strings.HasPrefix(row, "      ") || strings.HasPrefix(row, "       ") {
			t.Fatalf("continuation %q does not hang under the text", row)
		}
		if runewidth.StringWidth(row) > 40 {
			t.Fatalf("row %q is wider than 40", row)
		}
	}
	// Rows without a gutter keep their plain indentation, as before.
	plain := softWrapOverlayLine("    an option description that is long enough to wrap twice here", 30)
	for _, row := range plain[1:] {
		if !strings.HasPrefix(row, "    ") {
			t.Fatalf("indented continuation = %q", row)
		}
	}
}

// A goal's round outlives a run that parks on an approval — the resumed run
// works in the same round — and ends only when the goal's completion says so.
func TestGoalRoundSurvivesAParkedRun(t *testing.T) {
	tracker := NewTracker()
	r := Reducer{tracker: tracker}
	r.Reduce(RunStartedMsg{RunID: "r1"})
	r.Reduce(GoalStartedMsg{RunID: "r1", Objective: "ship it"})
	r.Reduce(GoalRoundStartedMsg{RunID: "r1", Round: 3})
	r.Reduce(RunEndedMsg{RunID: "r1"})   // parked on an approval
	r.Reduce(RunStartedMsg{RunID: "r1"}) // resumed after it
	if got := tracker.SnapshotActiveRun().GoalRound; got != 3 {
		t.Fatalf("the resumed run's working line reads round %d, want 3", got)
	}
	r.Reduce(GoalCompletedMsg{RunID: "r1"})
	if got := tracker.SnapshotActiveRun().GoalRound; got != 0 {
		t.Fatalf("round after the goal ended = %d, want 0", got)
	}
}

// Every slash overlay takes the same rows at the bottom of the screen — half
// the terminal, its rule included — however much it holds: the slash menu
// under the composer, a picker with one option, a report longer than the
// screen. A body too long for them scrolls inside, with the head and the key
// hint kept on screen.
func TestSlashOverlaysShareOneHeight(t *testing.T) {
	const width, height = 80, 40
	r := newComposerRenderer(t, width, height)
	want := slashPanelHeight(height)
	blockHeight := func() int {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.paintViewportLocked()
		return height - r.vpBodyHeight
	}

	cursor := 1
	menu := slashMenuPanel(&SlashOverlay{Visible: []turn.Command{{Name: "connect", Description: "configure or switch LLM provider"}}})
	r.RenderComposerState(ComposerRenderState{Text: "/c", Cursor: &cursor, SlashMenu: &menu})
	if got := blockHeight(); got != want {
		t.Fatalf("slash menu takes %d rows, want %d", got, want)
	}

	r.BeginComposerOverlay()
	one := &selectState{options: []string{"deepseek-chat"}, label: "Model\nThe model this agent runs on"}
	one.applyFilter()
	r.SetOverlayPanel(one.panel(), true, true)
	if got := blockHeight(); got != want {
		t.Fatalf("one-option picker takes %d rows, want %d", got, want)
	}

	report := strings.Repeat("a line of the report\n", 60)
	r.SetOverlayPanel(infoPanel("Migration preview", report, "import"), true, false)
	if got := blockHeight(); got != want {
		t.Fatalf("long report takes %d rows, want %d", got, want)
	}
	r.mu.Lock()
	painted := append([]string(nil), r.vpPainted[height-want:]...)
	r.mu.Unlock()
	if !strings.Contains(stripAnsi(painted[1]), "Migration preview") || !strings.Contains(stripAnsi(painted[len(painted)-1]), "Enter to import") {
		t.Fatalf("head or hint scrolled away: %q", painted)
	}
	if !r.ViewportScrollOverlay(5) {
		t.Fatal("a report longer than the panel must scroll")
	}
	r.EndOverlay()
}

// A caret painted in a tall overlay's field must not come back once the
// overlay closes. Closing it hands its rows to the transcript, which the
// painter moves with a region scroll; the caret's cell moves with them, so
// the painter must not restore it at the row it used to be on — that wrote a
// stray glyph from the old field (the "T" of "Type to filter") into whatever
// the next overlay drew there.
func TestClosedOverlayCaretIsNotRestoredAfterAScroll(t *testing.T) {
	const width, height = 80, 40
	r := newComposerRenderer(t, width, height)
	out := r.out.(*bytes.Buffer)
	for i := 0; i < 60; i++ {
		r.RenderFrame(Frame{Kind: FrameAssistant, Content: fmt.Sprintf("transcript line %02d", i), Final: true})
	}
	cursor := 0
	r.RenderComposerState(ComposerRenderState{Text: "", Cursor: &cursor})
	r.EnableSoftwareCursor()

	r.BeginComposerOverlay()
	picker := &selectState{options: []string{"low", "medium", "high"}, label: "Reasoning effort"}
	picker.applyFilter()
	r.SetOverlayPanel(picker.panel(), true, true)
	r.mu.Lock()
	r.cursorBlinkVisible = true
	r.paintViewportLocked()
	caretRow := r.vpCursor.row
	r.mu.Unlock()
	if caretRow < 0 {
		t.Fatal("the picker's filter shows no caret")
	}

	out.Reset()
	r.EndOverlay() // paints the frame that closes the overlay
	paint := out.String() + paintFrame(t, r, out)
	if stale := fmt.Sprintf("\x1b[%d;3H", caretRow+1); strings.Contains(paint, stale) {
		t.Fatalf("closing the overlay restored the old caret cell at row %d: %q", caretRow+1, paint)
	}
}

// The painted caret follows a region scroll the way the shadow does: moved
// with its cell while it stays in the region, gone once it scrolls off, and
// untouched below the region.
func TestPaintedCursorScrollsWithTheShadow(t *testing.T) {
	c := paintedCursor{row: 10, col: 3, paint: "P", clear: "C"}
	if got := c.scrolled(20, 4); got.row != 6 || got.at(got.clear) != "\x1b[7;3HC" {
		t.Fatalf("scrolled up 4: %+v, restores with %q", got, got.at(got.clear))
	}
	if got := c.scrolled(20, 11); got.row != -1 || got.at(got.clear) != "" {
		t.Fatalf("scrolled off: %+v", got)
	}
	if got := c.scrolled(20, -3); got.row != 13 {
		t.Fatalf("scrolled down 3: %+v", got)
	}
	if got := c.scrolled(8, 4); got != c {
		t.Fatalf("a caret below the region moved: %+v", got)
	}
}

func TestRunningToolHeaderShowsElapsed(t *testing.T) {
	f := Frame{Kind: FrameTool, Title: "shell", Summary: "node x.mjs", ToolMeta: tool.ToolMeta{Status: "running"}}
	f.ToolMeta.StartedAtMs = time.Now().Add(-75 * time.Second).UnixMilli()
	b := &viewBlock{frame: f}
	if got := ToolDisplayHeader(b.displayFrame(), ""); !strings.Contains(got, "1m 15s") {
		t.Fatalf("running header lacks elapsed time: %q", got)
	}
	b.frame.ToolMeta.StartedAtMs = time.Now().UnixMilli()
	if got := ToolDisplayHeader(b.displayFrame(), ""); strings.Contains(got, "0s") {
		t.Fatalf("sub-second run shows elapsed: %q", got)
	}
}

func TestSlashComposerSoftWrapsLongArgument(t *testing.T) {
	r := NewRenderer(nil, nil)
	const width = 60
	text := "/improve plan forebrain有一个问题需要解决：通过slash命令/model或者/connect命令修改当前生效的primary agent的模型后，FOREBRAIN_HOME/forebrain.yaml配置文件里的llm"
	cursor := len([]rune(text))
	block := r.buildComposerBlock(ComposerRenderState{Text: text, Cursor: &cursor}, width)
	rows := 0
	for _, line := range block.lines {
		if w := runewidth.StringWidth(stripANSI(line)); w >= width {
			t.Fatalf("row is %d cells wide on a %d-cell terminal, the terminal would autowrap it: %q", w, width, line)
		}
		if strings.Contains(line, "improve") || strings.Contains(line, "llm") {
			rows++
		}
	}
	if rows < 2 {
		t.Fatalf("long slash text was not wrapped across rows: %q", block.lines)
	}
	last := block.lines[block.cursorRow]
	if !strings.Contains(last, "llm") {
		t.Fatalf("caret row %d is not the last text row: %q", block.cursorRow, last)
	}
}

// A query call the run was interrupted on never completes: its card settles as
// canceled with the turn, instead of waiting forever.
func TestInterruptedWaitCardSettlesAsCanceled(t *testing.T) {
	var r Reducer
	_ = r.Reduce(RunStartedMsg{RunID: "run-w"})
	started := r.Reduce(NewMessageMsg{Msg: subagentStepMessage("wait-x", tool.StepEvent{
		Kind: tool.StepKindToolStarted, ToolName: "subagent_wait",
		Input: map[string]any{"task_id": "subagent-6e5c"},
	})})
	if got := lastFanoutFrame(t, started); got.Final {
		t.Fatalf("the wait card must be live while the call runs: %#v", got)
	}
	ended := r.Reduce(RunEndedMsg{RunID: "run-w"})
	card := lastFanoutFrame(t, ended)
	if !strings.HasPrefix(card.Summary, "Canceled ") {
		t.Fatalf("summary = %q, want the canceled label", card.Summary)
	}
	if !card.Final {
		t.Fatalf("the interrupted wait card must settle, got %#v", card)
	}
	if got := r.findFanoutByStepID("wait-x"); got == nil || got.CallStatus != "canceled" {
		t.Fatalf("card state = %#v, want canceled", got)
	}
}

// The benchmarks below measure the hot paths of renderViewport and
// paintViewportLocked: full repaints, no-change repaints, single-row
// streaming updates, scrolling over large histories, and the row-level
// helpers they call on every painted line.

// BenchmarkPaintViewportFullRepaint measures the cost of a full viewport
// repaint when the shadow is empty (cold start or after ForceRepaint).
func BenchmarkPaintViewportFullRepaint(b *testing.B) {
	var out bytes.Buffer
	r := NewRenderer(&out, &out)
	r.viewportMode = true

	// Build a realistic conversation state with multiple blocks
	r.RenderFrame(Frame{
		Kind:    FrameUser,
		Content: "What is the meaning of life?",
	})
	r.RenderFrame(Frame{
		Kind:    FrameAssistant,
		Content: strings.Repeat("The meaning of life is a profound question. ", 50),
	})
	r.RenderFrame(Frame{
		Kind:    FrameTool,
		StepID:  "tool-1",
		Title:   "read_file",
		Content: "file_path: README.md\noffset: 0\nlimit: 100",
	})
	r.RenderFrame(Frame{
		Kind:    FrameTool,
		StepID:  "tool-1",
		Title:   "read_file",
		Content: strings.Repeat("# Project Documentation\n\nThis is a sample file.\n", 20),
		Final:   true,
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out.Reset()
		r.mu.Lock()
		r.dropPaintShadowLocked()
		r.paintViewportLocked()
		r.mu.Unlock()
	}
}

// BenchmarkPaintViewportNoChanges measures the cost when the shadow matches
// the desired state (no rows need repainting).
func BenchmarkPaintViewportNoChanges(b *testing.B) {
	var out bytes.Buffer
	r := NewRenderer(&out, &out)
	r.viewportMode = true

	// Build conversation state
	r.RenderFrame(Frame{
		Kind:    FrameUser,
		Content: "Hello",
	})
	r.RenderFrame(Frame{
		Kind:    FrameAssistant,
		Content: strings.Repeat("Response text. ", 100),
	})

	// Initial paint to populate shadow
	r.mu.Lock()
	r.paintViewportLocked()
	r.mu.Unlock()
	out.Reset()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out.Reset()
		r.mu.Lock()
		r.paintViewportLocked()
		r.mu.Unlock()
	}
}

// BenchmarkPaintViewportSingleRowChange measures incremental repaint cost
// when only one row changes (typical streaming scenario).
func BenchmarkPaintViewportSingleRowChange(b *testing.B) {
	var out bytes.Buffer
	r := NewRenderer(&out, &out)
	r.viewportMode = true

	r.RenderFrame(Frame{
		Kind:    FrameAssistant,
		StepID:  "msg-1",
		Content: "Initial content",
	})

	// Initial paint
	r.mu.Lock()
	r.paintViewportLocked()
	r.mu.Unlock()
	out.Reset()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out.Reset()
		// Simulate streaming: update one block
		r.RenderFrame(Frame{
			Kind:    FrameAssistant,
			StepID:  "msg-1",
			Content: "Initial content " + strings.Repeat("x", i%10),
		})
		r.mu.Lock()
		r.paintViewportLocked()
		r.mu.Unlock()
	}
}

// BenchmarkRenderViewport measures the cost of building the viewport
// representation (before damage tracking and actual terminal I/O).
func BenchmarkRenderViewport(b *testing.B) {
	vm := &viewModel{
		blocks: make([]*viewBlock, 0, 50),
	}

	// Build a realistic block list
	for i := 0; i < 20; i++ {
		vm.blocks = append(vm.blocks, &viewBlock{
			id: vm.nextID,
			frame: Frame{
				Kind:    FrameAssistant,
				StepID:  "msg",
				Content: strings.Repeat("Sample content line. ", 50),
			},
			cache:     blockLineCache{},
			foldCache: foldLineCache{},
		})
		vm.nextID++
		vm.blocks = append(vm.blocks, &viewBlock{
			id: vm.nextID,
			frame: Frame{
				Kind:    FrameTool,
				StepID:  "tool",
				Title:   "shell",
				Content: "command: ls -la\ndescription: List files",
			},
			cache:     blockLineCache{},
			foldCache: foldLineCache{},
		})
		vm.nextID++
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = renderViewport(vm, 100, 40, 0, DiffThemeDark, "/home/user")
	}
}

// BenchmarkRenderViewportLargeHistory measures the cost when there are
// hundreds of historical blocks (realistic long session scenario).
func BenchmarkRenderViewportLargeHistory(b *testing.B) {
	vm := &viewModel{
		blocks: make([]*viewBlock, 0, 200),
	}

	// Build a large conversation history (100 blocks)
	for i := 0; i < 50; i++ {
		vm.blocks = append(vm.blocks, &viewBlock{
			id: vm.nextID,
			frame: Frame{
				Kind:    FrameUser,
				Content: "User question " + strings.Repeat("x", 50),
			},
			cache:     blockLineCache{},
			foldCache: foldLineCache{},
		})
		vm.nextID++
		vm.blocks = append(vm.blocks, &viewBlock{
			id: vm.nextID,
			frame: Frame{
				Kind:    FrameAssistant,
				Content: strings.Repeat("Assistant response text. ", 100),
			},
			cache:     blockLineCache{},
			foldCache: foldLineCache{},
		})
		vm.nextID++
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Scroll to top (offset 0) - this is where the lag happens
		_ = renderViewport(vm, 100, 40, 0, DiffThemeDark, "/home/user")
	}
}

// BenchmarkRenderViewportLargeHistoryScrolling measures the cost of
// scrolling through a large history.
func BenchmarkRenderViewportLargeHistoryScrolling(b *testing.B) {
	vm := &viewModel{
		blocks: make([]*viewBlock, 0, 200),
	}

	// Build a large conversation history (100 blocks)
	for i := 0; i < 50; i++ {
		vm.blocks = append(vm.blocks, &viewBlock{
			id: vm.nextID,
			frame: Frame{
				Kind:    FrameUser,
				Content: "User question " + strings.Repeat("x", 50),
			},
			cache:     blockLineCache{},
			foldCache: foldLineCache{},
		})
		vm.nextID++
		vm.blocks = append(vm.blocks, &viewBlock{
			id: vm.nextID,
			frame: Frame{
				Kind:    FrameAssistant,
				Content: strings.Repeat("Assistant response text. ", 100),
			},
			cache:     blockLineCache{},
			foldCache: foldLineCache{},
		})
		vm.nextID++
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Simulate scrolling at various positions
		offset := (i * 10) % 200
		_ = renderViewport(vm, 100, 40, offset, DiffThemeDark, "/home/user")
	}
}

// BenchmarkRenderViewportVeryLargeHistory measures performance with 500 blocks
// (realistic long development session with many tool calls and responses).
func BenchmarkRenderViewportVeryLargeHistory(b *testing.B) {
	vm := &viewModel{
		blocks: make([]*viewBlock, 0, 500),
	}

	// Build a very large conversation history (500 blocks)
	for i := 0; i < 250; i++ {
		vm.blocks = append(vm.blocks, &viewBlock{
			id: vm.nextID,
			frame: Frame{
				Kind:    FrameUser,
				Content: "User question " + strings.Repeat("x", 50),
			},
			cache:     blockLineCache{},
			foldCache: foldLineCache{},
		})
		vm.nextID++
		vm.blocks = append(vm.blocks, &viewBlock{
			id: vm.nextID,
			frame: Frame{
				Kind:    FrameAssistant,
				Content: strings.Repeat("Assistant response text. ", 100),
			},
			cache:     blockLineCache{},
			foldCache: foldLineCache{},
		})
		vm.nextID++
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Scroll to top - this is where the user-reported lag happens
		_ = renderViewport(vm, 100, 40, 0, DiffThemeDark, "/home/user")
	}
}

// BenchmarkSliceAppendGrowth measures the cost of slice growth patterns.
func BenchmarkSliceAppendGrowth(b *testing.B) {
	b.Run("SmallCapacity256", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			lines := make([]string, 0, 256)
			for j := 0; j < 5000; j++ {
				lines = append(lines, "sample line")
			}
			_ = lines
		}
	})

	b.Run("LargeCapacity8192", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			lines := make([]string, 0, 8192)
			for j := 0; j < 5000; j++ {
				lines = append(lines, "sample line")
			}
			_ = lines
		}
	})
}

// BenchmarkFitPaintRow measures the cost of fitting a single row to width
// (used on every painted row).
func BenchmarkFitPaintRow(b *testing.B) {
	// Test various row types
	rows := []string{
		"Simple ASCII text that fits in the terminal width exactly",
		"\x1b[38;5;252mColored text with SGR sequences\x1b[0m that needs width calculation",
		strings.Repeat("Very long line that exceeds terminal width ", 10),
		"Line with 中文 characters that take two cells each",
		"\x1b[1mBold\x1b[0m \x1b[3mitalic\x1b[0m \x1b[4munderline\x1b[0m mixed styles",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		row := rows[i%len(rows)]
		_ = fitPaintRow(row, 80)
	}
}

// BenchmarkApplySelectionHighlight measures selection highlighting cost.
func BenchmarkApplySelectionHighlight(b *testing.B) {
	line := "\x1b[38;5;252mThis is a line of text with some color codes\x1b[0m"
	selectBgSeq := "\x1b[48;5;24m"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = applySelectionHighlight(line, selectBgSeq, 10, 30)
	}
}

// BenchmarkActionCardScroll measures the cost of scrolling through a long
// action card (tool output with many lines).
func BenchmarkActionCardScroll(b *testing.B) {
	var out bytes.Buffer
	r := NewRenderer(&out, &out)
	r.viewportMode = true

	// Create a tool result with 1000 lines
	content := strings.Repeat("Line of tool output\n", 1000)
	r.RenderFrame(Frame{
		Kind:    FrameTool,
		StepID:  "tool-1",
		Title:   "shell",
		Content: content,
		Final:   true,
	})

	// Initial paint
	r.mu.Lock()
	r.paintViewportLocked()
	r.mu.Unlock()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out.Reset()
		// Simulate scrolling
		r.mu.Lock()
		r.vpScrollOffset = i % 500
		r.vpFollow = false
		r.paintViewportLocked()
		r.mu.Unlock()
	}
}

// BenchmarkSpinnerUpdate measures the cost of updating spinner phase
// (happens on every paint during tool execution).
func BenchmarkSpinnerUpdate(b *testing.B) {
	var out bytes.Buffer
	r := NewRenderer(&out, &out)
	r.viewportMode = true
	r.spinnerEpoch = time.Now()

	// Create a running tool
	r.RenderFrame(Frame{
		Kind:    FrameTool,
		StepID:  "tool-1",
		Title:   "shell",
		Content: "command: sleep 10",
		Summary: "running",
	})

	// Initial paint
	r.mu.Lock()
	r.paintViewportLocked()
	r.mu.Unlock()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out.Reset()
		r.mu.Lock()
		// Simulate time passing
		r.spinnerEpoch = time.Now().Add(-time.Duration(i) * spinnerAnimationTick)
		r.paintViewportLocked()
		r.mu.Unlock()
	}
}

// BenchmarkVerticalShift measures the cost of detecting vertical scroll
// (used to optimize repaints during streaming).
func BenchmarkVerticalShift(b *testing.B) {
	painted := make([]string, 40)
	rows := make([]string, 40)

	// Simulate a 1-row scroll
	for i := 0; i < 39; i++ {
		painted[i] = "Line " + strings.Repeat("x", 70)
		rows[i] = painted[i+1] // Shifted up by 1
	}
	rows[39] = "New bottom line"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = verticalShift(painted, rows, 40)
	}
}

// BenchmarkRenderViewportCacheEfficiency measures how effective the block
// cache is by rendering the same viewport multiple times.
func BenchmarkRenderViewportCacheEfficiency(b *testing.B) {
	vm := &viewModel{
		blocks: make([]*viewBlock, 0, 500),
	}

	// Build 500 blocks
	for i := 0; i < 250; i++ {
		vm.blocks = append(vm.blocks, &viewBlock{
			id: vm.nextID,
			frame: Frame{
				Kind:    FrameUser,
				Content: "User question " + strings.Repeat("x", 50),
			},
			cache:     blockLineCache{},
			foldCache: foldLineCache{},
		})
		vm.nextID++
		vm.blocks = append(vm.blocks, &viewBlock{
			id: vm.nextID,
			frame: Frame{
				Kind:    FrameAssistant,
				Content: strings.Repeat("Assistant response text. ", 100),
			},
			cache:     blockLineCache{},
			foldCache: foldLineCache{},
		})
		vm.nextID++
	}

	// First render to populate cache
	_ = renderViewport(vm, 100, 40, 0, DiffThemeDark, "/home/user")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Subsequent renders should hit cache
		_ = renderViewport(vm, 100, 40, 0, DiffThemeDark, "/home/user")
	}
}

// BenchmarkRealisticScrollingScenario simulates a realistic user scrolling
// through a large conversation with cache hits.
func BenchmarkRealisticScrollingScenario(b *testing.B) {
	vm := &viewModel{
		blocks: make([]*viewBlock, 0, 500),
	}

	// Build 500 blocks
	for i := 0; i < 250; i++ {
		vm.blocks = append(vm.blocks, &viewBlock{
			id: vm.nextID,
			frame: Frame{
				Kind:    FrameUser,
				Content: "User question " + strings.Repeat("x", 50),
			},
			cache:     blockLineCache{},
			foldCache: foldLineCache{},
		})
		vm.nextID++
		vm.blocks = append(vm.blocks, &viewBlock{
			id: vm.nextID,
			frame: Frame{
				Kind:    FrameAssistant,
				Content: strings.Repeat("Assistant response text. ", 100),
			},
			cache:     blockLineCache{},
			foldCache: foldLineCache{},
		})
		vm.nextID++
	}

	// Warm up cache
	for offset := 0; offset < 1000; offset += 10 {
		_ = renderViewport(vm, 100, 40, offset, DiffThemeDark, "/home/user")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Simulate smooth scrolling: small offset changes
		offset := (i * 3) % 1000 // Scroll 3 lines at a time
		_ = renderViewport(vm, 100, 40, offset, DiffThemeDark, "/home/user")
	}
}
