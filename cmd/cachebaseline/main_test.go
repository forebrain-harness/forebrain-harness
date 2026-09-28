package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/architecture"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// scriptedLLM returns one fixed usage per call, in order, so
// recordLongToolTurn can be tested without a live provider. It also records
// each call's system prompt and whether llm.Fast(ctx) was set, so tests
// can confirm recordAgentSwitch actually varies the prefix and
// recordFastMode actually threads the fast-mode signal through.
type scriptedLLM struct {
	calls        int
	usage        []llm.Usage
	sawFast      []bool
	sawSystemMsg []string
}

func (m *scriptedLLM) Execute(ctx context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	u := m.usage[m.calls]
	m.calls++
	m.sawFast = append(m.sawFast, llm.Fast(ctx))
	system := ""
	for _, msg := range messages {
		if msg.Role == llm.RoleSystem {
			system = msg.TextContent()
			break
		}
	}
	m.sawSystemMsg = append(m.sawSystemMsg, system)
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("ok")})
	return &llm.Result{Message: &msg, Usage: &u}, nil
}

func TestRecordLongToolTurnReturnsLastCallUsage(t *testing.T) {
	client := &scriptedLLM{usage: []llm.Usage{
		{InputTokens: 800, CacheReadInputTokens: 0, CacheCreationInputTokens: 0},
		{InputTokens: 200, CacheReadInputTokens: 600, CacheCreationInputTokens: 0},
		{InputTokens: 150, CacheReadInputTokens: 700, CacheCreationInputTokens: 0},
	}}
	use, err := recordLongToolTurn(client)
	if err != nil {
		t.Fatalf("recordLongToolTurn: %v", err)
	}
	// Must reflect the LAST call (steady-state, after the prefix has had a
	// chance to be cached), not the first (necessarily cold) call.
	if use.Read != 700 || use.Input != 150 || use.Creation != 0 {
		t.Fatalf("use = %+v, want the third call's usage", use)
	}
	if client.calls != 3 {
		t.Fatalf("calls = %d, want 3 (one cold turn + two follow-ups)", client.calls)
	}
}

func TestRecordFastModeThreadsFastSignalToEveryCall(t *testing.T) {
	client := &scriptedLLM{usage: []llm.Usage{
		{InputTokens: 800},
		{InputTokens: 200, CacheReadInputTokens: 600},
		{InputTokens: 150, CacheReadInputTokens: 700},
	}}
	if _, err := recordFastMode(client); err != nil {
		t.Fatalf("recordFastMode: %v", err)
	}
	for i, fast := range client.sawFast {
		if !fast {
			t.Fatalf("sawFast[%d] = false, want true (recordFastMode must set llm.WithFast on every call)", i)
		}
	}
}

func TestRecordAgentSwitchUsesADifferentSystemPromptOnTheSecondCall(t *testing.T) {
	client := &scriptedLLM{usage: []llm.Usage{
		{InputTokens: 900, CacheReadInputTokens: 0, CacheCreationInputTokens: 900},
		{InputTokens: 50, CacheReadInputTokens: 0, CacheCreationInputTokens: 950},
	}}
	use, err := recordAgentSwitch(client)
	if err != nil {
		t.Fatalf("recordAgentSwitch: %v", err)
	}
	if client.calls != 2 {
		t.Fatalf("calls = %d, want 2 (one turn per agent)", client.calls)
	}
	if client.sawSystemMsg[0] == client.sawSystemMsg[1] {
		t.Fatal("both calls saw the same system prompt, want the second call to switch to a different one")
	}
	// Must reflect the second (switched-prefix) call, which is what
	// "agent_switch" is actually measuring.
	if use.Creation != 950 {
		t.Fatalf("use = %+v, want the second call's usage (creation=950)", use)
	}
}

func TestRecordApprovalResumeAppendsASyntheticToolResultBeforeResuming(t *testing.T) {
	toolCall := llm.AssistantMessage(nil, llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "shell", Arguments: `{"command":"go vet ./pkg/state/...","description":"vet"}`}})
	client := &recordingScriptedLLM{
		scriptedLLM: scriptedLLM{usage: []llm.Usage{
			{InputTokens: 800},
			{InputTokens: 100, CacheReadInputTokens: 700},
		}},
		replies: []*llm.Message{&toolCall, nil},
	}
	use, err := recordApprovalResume(client)
	if err != nil {
		t.Fatalf("recordApprovalResume: %v", err)
	}
	if client.calls != 2 {
		t.Fatalf("calls = %d, want 2 (tool call + resume)", client.calls)
	}
	if len(client.sawMessages) != 2 {
		t.Fatalf("sawMessages = %d, want 2 recorded calls", len(client.sawMessages))
	}
	secondCallMessages := client.sawMessages[1]
	var sawToolResult bool
	for _, m := range secondCallMessages {
		if m.Role == llm.RoleTool && m.ToolCallID == "call-1" {
			sawToolResult = true
		}
	}
	if !sawToolResult {
		t.Fatalf("second call's messages = %+v, want a tool_result for call-1 before resuming", secondCallMessages)
	}
	if use.Read != 700 {
		t.Fatalf("use = %+v, want the resume call's usage", use)
	}
}

// recordingScriptedLLM extends scriptedLLM to return a scripted reply
// message per call (so a tool call can be followed by a synthesized
// result) and to record the full message list each call received.
type recordingScriptedLLM struct {
	scriptedLLM
	replies     []*llm.Message
	sawMessages [][]llm.Message
}

func (m *recordingScriptedLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	idx := m.calls
	m.sawMessages = append(m.sawMessages, append([]llm.Message(nil), messages...))
	res, err := m.scriptedLLM.Execute(ctx, messages, tools)
	if err != nil || res == nil {
		return res, err
	}
	if idx < len(m.replies) && m.replies[idx] != nil {
		res.Message = m.replies[idx]
	}
	return res, nil
}

func TestRecordSubagentInsertsALargeSyntheticResultForTheDispatchCall(t *testing.T) {
	subagentCall := llm.AssistantMessage(nil, llm.ToolCall{ID: "call-1", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "subagent_run", Arguments: `{"task":"audit pkg/state error handling"}`}})
	client := &recordingScriptedLLM{
		scriptedLLM: scriptedLLM{usage: []llm.Usage{
			{InputTokens: 800},
			{InputTokens: 100, CacheReadInputTokens: 700},
		}},
		replies: []*llm.Message{&subagentCall, nil},
	}
	use, err := recordSubagent(client)
	if err != nil {
		t.Fatalf("recordSubagent: %v", err)
	}
	if client.calls != 2 {
		t.Fatalf("calls = %d, want 2 (dispatch + follow-up)", client.calls)
	}
	secondCallMessages := client.sawMessages[1]
	var toolResult *llm.Message
	for i := range secondCallMessages {
		if secondCallMessages[i].Role == llm.RoleTool && secondCallMessages[i].ToolCallID == "call-1" {
			toolResult = &secondCallMessages[i]
		}
	}
	if toolResult == nil {
		t.Fatalf("second call's messages = %+v, want a tool_result for call-1", secondCallMessages)
	}
	if len(toolResult.TextContent()) < len("[cachebaseline probe] tool not actually executed; synthetic ok result")*3 {
		t.Fatalf("subagent tool_result content is only %d bytes, want something realistically sized like a real subagent summary, not the short generic placeholder", len(toolResult.TextContent()))
	}
	if use.Read != 700 {
		t.Fatalf("use = %+v, want the follow-up call's usage", use)
	}
}

func TestLoadOrEmptyReportReturnsEmptyForMissingFile(t *testing.T) {
	report, err := loadOrEmptyReport(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("loadOrEmptyReport: %v", err)
	}
	if report.Providers == nil || len(report.Providers) != 0 {
		t.Fatalf("report = %+v, want an empty-but-non-nil Providers map", report)
	}
}

func TestWriteReportUpdatesOnlyTargetCaseAndPreservesOthers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	initial := architecture.CacheReport{Providers: map[string]architecture.CacheRun{
		"deepseek": {
			Model: "deepseek/deepseek-v4-pro",
			Cases: map[string]architecture.CacheUse{
				"long_tool_turn":  {Read: 1, Creation: 2, Input: 3},
				"approval_resume": {Read: 4, Creation: 5, Input: 6},
				"compaction":      {Read: 7, Creation: 8, Input: 9},
				"agent_switch":    {Read: 10, Creation: 11, Input: 12},
				"fast_mode":       {Read: 13, Creation: 14, Input: 15},
				"subagent":        {Read: 16, Creation: 17, Input: 18},
			},
		},
		"openai": {Model: "openai/gpt-5", Cases: map[string]architecture.CacheUse{
			"long_tool_turn": {Read: 100, Creation: 0, Input: 50},
		}},
	}}
	if err := writeReport(path, initial); err != nil {
		t.Fatalf("writeReport (seed): %v", err)
	}

	report, err := loadOrEmptyReport(path)
	if err != nil {
		t.Fatalf("loadOrEmptyReport: %v", err)
	}
	deepseek := report.Providers["deepseek"]
	deepseek.Cases["long_tool_turn"] = architecture.CacheUse{Read: 999, Creation: 0, Input: 111}
	report.Providers["deepseek"] = deepseek
	if err := writeReport(path, report); err != nil {
		t.Fatalf("writeReport (update): %v", err)
	}

	got, err := loadOrEmptyReport(path)
	if err != nil {
		t.Fatalf("loadOrEmptyReport (reread): %v", err)
	}
	if got.Providers["deepseek"].Cases["long_tool_turn"] != (architecture.CacheUse{Read: 999, Creation: 0, Input: 111}) {
		t.Fatalf("long_tool_turn = %+v, want the updated value", got.Providers["deepseek"].Cases["long_tool_turn"])
	}
	if got.Providers["deepseek"].Cases["approval_resume"] != (architecture.CacheUse{Read: 4, Creation: 5, Input: 6}) {
		t.Fatalf("approval_resume = %+v, want unchanged", got.Providers["deepseek"].Cases["approval_resume"])
	}
	if got.Providers["openai"].Cases["long_tool_turn"] != (architecture.CacheUse{Read: 100, Creation: 0, Input: 50}) {
		t.Fatalf("openai/long_tool_turn = %+v, want unchanged", got.Providers["openai"].Cases["long_tool_turn"])
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var roundTrip architecture.CacheReport
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatalf("file is not valid CacheReport JSON: %v", err)
	}
}
