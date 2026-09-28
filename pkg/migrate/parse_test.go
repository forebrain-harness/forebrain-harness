package migrate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

func fixtureSession(t *testing.T) *parsedSession {
	t.Helper()
	parsed, err := parseSessionFile(context.Background(), filepath.Join("testdata", "session.jsonl"), "s-fix", nil)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// TestParseSessionMapsRecordTypes pins §4.2's row mapping over a fixture
// that carries every record kind the source writes: plain and multimodal
// user rows, thinking, text, a tool call and its sidecar-backed result,
// two compact boundaries each followed by its summary row, and the record
// types that must be dropped.
func TestParseSessionMapsRecordTypes(t *testing.T) {
	parsed := fixtureSession(t)

	var roles []string
	var contents []string
	for _, row := range parsed.Rows {
		roles = append(roles, row.Role)
		contents = append(contents, row.Content)
	}
	want := []string{
		"user",      // u-user1
		"reasoning", // thinking block
		"assistant", // text block
		"assistant", // tool_use row (empty content)
		"tool",      // tool_result row
		"user",      // multimodal row
		"system",    // boundary 1
		"assistant", // after compact
		"system",    // boundary 2
		"assistant", // final answer
	}
	if strings.Join(roles, ",") != strings.Join(want, ",") {
		t.Fatalf("roles = %v\nwant      %v", roles, want)
	}

	// The sidecar reference inlined the file next to the transcript.
	if !strings.Contains(contents[4], "sidecar body line two") {
		t.Fatalf("tool result content = %q; want the sidecar body inlined", contents[4])
	}
	// The compact-summary rows became no row of their own; the summary lives
	// inside the boundary part instead.
	for _, content := range contents {
		if strings.Contains(content, "SUMMARY:") {
			t.Fatalf("summary leaked into a standalone row: %q", content)
		}
	}
	// Aggregates: ai-title wins, usage sums across assistant records, the
	// cost-state carries the dollar figure, and cwd/branch come from records.
	if parsed.Title != "My Fixed Title" {
		t.Fatalf("title = %q", parsed.Title)
	}
	if parsed.PromptTok != 130 || parsed.CompletionTok != 13 {
		t.Fatalf("tokens = %d/%d", parsed.PromptTok, parsed.CompletionTok)
	}
	if parsed.CostUSD != 1.25 {
		t.Fatalf("cost = %v", parsed.CostUSD)
	}
	if parsed.Cwd != "/tmp/source-proj" || parsed.GitBranch != "main" {
		t.Fatalf("cwd/branch = %q/%q", parsed.Cwd, parsed.GitBranch)
	}
	// Timestamps are monotonic even where a record carried none (u-u4);
	// rows from one source record legitimately share its timestamp.
	var last int64
	for i, row := range parsed.Rows {
		if row.CreatedAt < last && i > 0 {
			t.Fatalf("row %d timestamp %d before %d", i, row.CreatedAt, last)
		}
		last = row.CreatedAt
	}
}

// TestParseSessionToolCallPairing checks the tool_use row's exact parts
// bytes — mapped name, canonical arguments, the call id replay pairs on —
// and the paired result row's meta columns.
func TestParseSessionToolCallPairing(t *testing.T) {
	parsed := fixtureSession(t)
	var call, result *parsedRow
	for i := range parsed.Rows {
		if parsed.Rows[i].Role == "assistant" && parsed.Rows[i].ToolStepID == "toolu_1" {
			call = &parsed.Rows[i]
		}
		if parsed.Rows[i].Role == "tool" {
			result = &parsed.Rows[i]
		}
	}
	if call == nil || result == nil {
		t.Fatalf("call/result rows missing: %+v %+v", call, result)
	}
	wantParts := `[{"tool_calls":[{"id":"toolu_1","type":"function","function":{"name":"shell","arguments":"{\"command\":\"ls -la\",\"description\":\"list files\"}"}}],"type":"tool_calls"}]`
	if call.PartsJSON != wantParts {
		t.Fatalf("tool_use parts =\n%s\nwant\n%s", call.PartsJSON, wantParts)
	}
	if result.ToolStepID != "toolu_1" {
		t.Fatalf("result tool_step_id = %q", result.ToolStepID)
	}
	var meta struct {
		ToolName   string         `json:"tool_name"`
		Status     string         `json:"status"`
		Invocation string         `json:"invocation"`
		Input      map[string]any `json:"input"`
	}
	if err := json.Unmarshal([]byte(result.ToolMetaJSON), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.ToolName != "shell" || meta.Status != "completed" || meta.Invocation != "ls -la" {
		t.Fatalf("tool meta = %+v", meta)
	}
	if meta.Input["command"] != "ls -la" {
		t.Fatalf("tool meta input = %+v", meta.Input)
	}
	// The result row carries tool_result_meta plus the display part.
	if !strings.Contains(result.PartsJSON, `"tool_call_id":"toolu_1"`) {
		t.Fatalf("result parts missing tool_result_meta: %s", result.PartsJSON)
	}
	if !strings.Contains(result.PartsJSON, `"type":"tool_display"`) {
		t.Fatalf("result parts missing tool_display: %s", result.PartsJSON)
	}
}

// TestParseSessionImagePart checks the base64 image lands as a parts image
// block the way native multimodal user rows store it.
func TestParseSessionImagePart(t *testing.T) {
	parsed := fixtureSession(t)
	var multimodal *parsedRow
	for i := range parsed.Rows {
		if strings.Contains(parsed.Rows[i].PartsJSON, `"type":"image"`) {
			multimodal = &parsed.Rows[i]
		}
	}
	if multimodal == nil {
		t.Fatal("no image part found")
	}
	if !strings.Contains(multimodal.PartsJSON, `"media_type":"image/png"`) || !strings.Contains(multimodal.PartsJSON, `"data":"aGk="`) {
		t.Fatalf("image part = %s", multimodal.PartsJSON)
	}
}

// TestParseSessionCompactBoundaries pins B1/B2's core: exactly one boundary
// row per source boundary, the summary living inside ReplacementHistory
// under the canonical prefix, the preserved segment spanning head..tail, and
// the synthesized window chain linking both boundaries to one initial id.
func TestParseSessionCompactBoundaries(t *testing.T) {
	parsed := fixtureSession(t)
	if len(parsed.boundaries) != 2 {
		t.Fatalf("boundaries = %d, want 2", len(parsed.boundaries))
	}
	first := parsed.Rows[parsed.boundaries[0]].boundary
	second := parsed.Rows[parsed.boundaries[1]].boundary

	if len(first.ReplacementHistory) == 0 {
		t.Fatal("first boundary has no replacement history")
	}
	summary := first.ReplacementHistory[0]
	if !strings.HasPrefix(summary.TextContent(), state.CompactSummaryPrefix+"\n\n") {
		t.Fatalf("summary prefix missing: %.80s", summary.TextContent())
	}
	if !strings.Contains(summary.TextContent(), "SUMMARY: the work so far continued") {
		t.Fatalf("summary body missing: %.120s", summary.TextContent())
	}
	// Preserved segment u-user1..u-u3 spans user, reasoning, assistant,
	// assistant(tool_use), tool.
	if len(first.ReplacementHistory) != 6 {
		t.Fatalf("first replacement history = %d messages, want summary + 5 preserved", len(first.ReplacementHistory))
	}
	if first.Trigger != "auto" || first.Strategy != "migrated" || first.SummarySource != "migrated" {
		t.Fatalf("boundary labels = %+v", first)
	}
	// Window chain: numbers from 1, one shared initial id, second links back.
	if first.WindowNumber != 1 || second.WindowNumber != 2 {
		t.Fatalf("window numbers = %d/%d", first.WindowNumber, second.WindowNumber)
	}
	if first.FirstWindowID == "" || first.FirstWindowID != parsed.initialWindowID || second.FirstWindowID != first.FirstWindowID {
		t.Fatalf("initial window id mismatch: %q vs %q / %q", first.FirstWindowID, parsed.initialWindowID, second.FirstWindowID)
	}
	if first.PreviousWindowID != "" || second.PreviousWindowID != first.WindowID {
		t.Fatalf("previous chain = %q / %q", first.PreviousWindowID, second.PreviousWindowID)
	}
	if first.WindowID == second.WindowID {
		t.Fatal("both boundaries share one window id")
	}
	// The boundary row's visible body records what the source measured.
	if !strings.Contains(parsed.Rows[parsed.boundaries[0]].Content, "trigger auto") {
		t.Fatalf("boundary body = %q", parsed.Rows[parsed.boundaries[0]].Content)
	}
}

// TestParseSessionWithoutTimestampsKeepsMonotonicClock feeds a transcript
// whose records carry no timestamps at all.
func TestParseSessionWithoutTimestampsKeepsMonotonicClock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	body := `{"type":"user","uuid":"a","message":{"role":"user","content":"one"}}
{"type":"assistant","uuid":"b","message":{"role":"assistant","content":[{"type":"text","text":"two"}]}}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed, err := parseSessionFile(context.Background(), path, "s", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Rows) != 2 {
		t.Fatalf("rows = %d", len(parsed.Rows))
	}
	if parsed.Rows[1].CreatedAt <= parsed.Rows[0].CreatedAt {
		t.Fatalf("clock not monotonic: %d then %d", parsed.Rows[0].CreatedAt, parsed.Rows[1].CreatedAt)
	}
	// Fallback title: first user text, capped at 40 runes.
	if parsed.Title != "one" {
		t.Fatalf("fallback title = %q", parsed.Title)
	}
}

// TestParseSessionBadLinesSkipped counts unparsable lines without failing
// the session.
func TestParseSessionBadLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	body := `not json at all
{"type":"user","uuid":"a","message":{"role":"user","content":"ok"}}
{"type":}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := 0
	parsed, err := parseSessionFile(context.Background(), path, "s", func() { bad++ })
	if err != nil {
		t.Fatal(err)
	}
	if bad != 2 || len(parsed.Rows) != 1 {
		t.Fatalf("bad lines = %d, rows = %d", bad, len(parsed.Rows))
	}
}

// --- Shared resume invariants (§1.2), tested over both sources' parsers ---

// claudeOrphanFixture is a Claude transcript whose tail leaves an assistant
// tool call unanswered — the exact shape an interrupted run leaves behind.
func claudeOrphanFixture(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "orphan.jsonl")
	body := `{"type":"user","uuid":"u1","timestamp":"2026-09-17T10:00:00Z","message":{"role":"user","content":"run it"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-09-17T10:00:01Z","message":{"role":"assistant","model":"m","content":[{"type":"tool_use","id":"toolu_orph","name":"Bash","input":{"command":"sleep 99"}}]}}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// codexOrphanFixture is the Codex twin: a function_call whose output record
// never arrived (turn_aborted between the two).
func codexOrphanFixture(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "orphan-rollout.jsonl")
	body := `{"timestamp":"2026-09-17T10:00:00.000Z","type":"session_meta","payload":{"session_id":"t-orph","cwd":"/tmp/x","context_window":{"window_id":"w1"}}}
{"timestamp":"2026-09-17T10:00:01.000Z","type":"response_item","payload":{"type":"message","id":"msg_u1","role":"user","content":[{"type":"input_text","text":"run it"}]}}
{"timestamp":"2026-09-17T10:00:02.000Z","type":"response_item","payload":{"type":"function_call","id":"fc_orph","call_id":"call_orph","name":"wait","arguments":"{\"seconds\":1}"}}
{"timestamp":"2026-09-17T10:00:03.000Z","type":"event_msg","payload":{"type":"turn_aborted","reason":"esc"}}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// assertResumeInvariants holds the four §1.2 rules against a parsed session:
// (1) every assistant tool call is answered by a matching tool row, (2) no
// dangling tool result, (3) the session opens on a user row, (4) no role the
// model-context rebuild drops mid-context starts the transcript.
func assertResumeInvariants(t *testing.T, parsed *parsedSession) {
	t.Helper()
	if len(parsed.Rows) == 0 {
		t.Fatal("no rows parsed")
	}
	if parsed.Rows[0].Role != llm.RoleUser {
		t.Fatalf("first row role = %q, want user", parsed.Rows[0].Role)
	}
	calls := map[string]bool{}
	for _, row := range parsed.Rows {
		if row.Role == llm.RoleAssistant && strings.TrimSpace(row.ToolStepID) != "" {
			calls[strings.TrimSpace(row.ToolStepID)] = false
		}
		if row.Role == llm.RoleTool {
			id := strings.TrimSpace(row.ToolStepID)
			_, ok := calls[id]
			if !ok {
				t.Fatalf("dangling tool result %q answers no call", id)
			}
			calls[id] = true
		}
	}
	for id, answered := range calls {
		if !answered {
			t.Fatalf("orphan tool call %q was not answered", id)
		}
	}
	// Every assistant call row survived: answering is the repair, deletion
	// is not (§1.2 rule 1's second half).
	var assistantCalls int
	for _, row := range parsed.Rows {
		if row.Role == llm.RoleAssistant && strings.TrimSpace(row.ToolStepID) != "" {
			assistantCalls++
		}
	}
	if assistantCalls != len(calls) {
		t.Fatalf("call rows = %d, distinct calls = %d", assistantCalls, len(calls))
	}
}

// TestClaudeOrphanCallGetsCancelledResult proves the Claude parser answers
// an interrupted call with a cancelled result and keeps the call row.
func TestClaudeOrphanCallGetsCancelledResult(t *testing.T) {
	dir := t.TempDir()
	path := claudeOrphanFixture(t, dir)
	parsed, err := parseSessionFile(context.Background(), path, "orph", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertResumeInvariants(t, parsed)
	if parsed.cancelledResults != 1 {
		t.Fatalf("cancelled results = %d, want 1", parsed.cancelledResults)
	}
	var sawCancelled bool
	for _, row := range parsed.Rows {
		if row.Role == llm.RoleTool && strings.Contains(row.Content, "canceled") {
			sawCancelled = true
		}
	}
	if !sawCancelled {
		t.Fatal("cancelled answer row missing")
	}
}

// TestCodexOrphanCallGetsCancelledResult is the Codex twin of the above.
func TestCodexOrphanCallGetsCancelledResult(t *testing.T) {
	dir := t.TempDir()
	path := codexOrphanFixture(t, dir)
	parsed, err := parseCodexRollout(context.Background(), path, "t-orph", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertResumeInvariants(t, parsed)
	if parsed.cancelledResults != 1 {
		t.Fatalf("cancelled results = %d, want 1", parsed.cancelledResults)
	}
}

// TestCodexDropsEncryptedReasoningAndDeveloper checks the two §4.2 drops are
// counted, not written: an encrypted-only reasoning item and a developer
// message never become rows.
func TestCodexDropsEncryptedReasoningAndDeveloper(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drops.jsonl")
	body := `{"timestamp":"2026-09-17T10:00:00.000Z","type":"session_meta","payload":{"session_id":"t-drops","cwd":"/tmp/x"}}
{"timestamp":"2026-09-17T10:00:01.000Z","type":"response_item","payload":{"type":"message","id":"msg_d","role":"developer","content":[{"type":"input_text","text":"dev"}]}}
{"timestamp":"2026-09-17T10:00:02.000Z","type":"response_item","payload":{"type":"message","id":"msg_u","role":"user","content":[{"type":"input_text","text":"hi"}]}}
{"timestamp":"2026-09-17T10:00:03.000Z","type":"response_item","payload":{"type":"reasoning","id":"rs_enc","summary":[],"encrypted_content":"X"}}
{"timestamp":"2026-09-17T10:00:04.000Z","type":"response_item","payload":{"type":"message","id":"msg_a","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed, err := parseCodexRollout(context.Background(), path, "t-drops", nil)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.stats.EncryptedReasoning != 1 || parsed.stats.DeveloperMessages != 1 {
		t.Fatalf("stats = %+v, want one encrypted reasoning and one developer drop", parsed.stats)
	}
	for _, row := range parsed.Rows {
		if row.Role == "developer" {
			t.Fatal("developer row must not be written")
		}
		if row.Role == "reasoning" {
			t.Fatal("encrypted-only reasoning must not be written")
		}
	}
	assertResumeInvariants(t, parsed)
}

// TestClaudeCompactedSessionProjectionSelfConsistent checks the projected
// context of a compacted Claude session equals the boundary's replacement
// history followed by the rows after the boundary (§1.2 rule 4) — the
// B1/B2 acceptance: the summary is visible to a continued conversation.
func TestClaudeCompactedSessionProjectionSelfConsistent(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	opts := fixture.options(t, false)
	report, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _, _, _ := report.SessionCounts(); got != 2 {
		t.Fatalf("migrated = %d", got)
	}
	store := state.NewSessionStore(opts.DB, "main")
	msgs, err := store.ListTranscriptMessages(ctx, "cli-s-fix", 5000)
	if err != nil {
		t.Fatal(err)
	}
	assertProviderSafeMessages(t, msgs)
}

// assertProviderSafeMessages runs the pairing rules over already-stored
// messages: the model-context rebuild parses exactly these rows, so the same
// invariants apply after a round trip through the database. A compacted
// session's projection legitimately opens on an opaque compaction item (its
// Role is empty by design — Compaction is the payload), so the first-row
// check accepts user-or-compaction.
func assertProviderSafeMessages(t *testing.T, msgs []llm.Message) {
	t.Helper()
	if len(msgs) == 0 {
		t.Fatal("no messages stored")
	}
	if msgs[0].Role != llm.RoleUser && msgs[0].Compaction == nil {
		t.Fatalf("first stored message role = %q", msgs[0].Role)
	}
	calls := map[string]bool{}
	for _, msg := range msgs {
		if msg.Compaction != nil {
			continue
		}
		switch msg.Role {
		case llm.RoleAssistant:
			for _, call := range msg.ToolCalls {
				calls[call.ID] = false
			}
		case llm.RoleTool:
			if _, ok := calls[msg.ToolCallID]; !ok {
				t.Fatalf("stored dangling tool result %q", msg.ToolCallID)
			}
			calls[msg.ToolCallID] = true
		case "reasoning", "developer":
			t.Fatalf("role %q must not reach the model context", msg.Role)
		}
	}
	for id, answered := range calls {
		if !answered {
			t.Fatalf("stored orphan call %q", id)
		}
	}
}

// TestCodexStoredSessionContinuable proves a migrated Codex session rebuilds
// into a provider-safe message list, including the encrypted compaction
// item replayed verbatim (C1).
func TestCodexStoredSessionContinuable(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureCodexHome(t)
	opts := fixture.options(t, false)
	if _, err := RunCodex(ctx, opts, nil); err != nil {
		t.Fatal(err)
	}
	store := state.NewSessionStore(opts.DB, "main")
	msgs, err := store.ListTranscriptMessages(ctx, "cli-t-codex-main", 5000)
	if err != nil {
		t.Fatal(err)
	}
	assertProviderSafeMessages(t, msgs)
	var sawCompaction bool
	for _, msg := range msgs {
		if msg.Compaction != nil && msg.Compaction.EncryptedContent == "COMPACT-ENCRYPTED-1" {
			sawCompaction = true
		}
	}
	if !sawCompaction {
		t.Fatal("encrypted compaction item missing from the rebuilt context")
	}
}
