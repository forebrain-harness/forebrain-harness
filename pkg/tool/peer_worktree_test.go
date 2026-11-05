package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// peerWorkTreeFake answers the same-tree questions the way another process
// would: a fixed list of other agents' writes, and a record of what the tools
// announced to it.
type peerWorkTreeFake struct {
	peers   []WorkTreePeer
	touches []PeerTouch
	noted   []string
	roots   []string
}

func (f *peerWorkTreeFake) WorkTreePeers(_ context.Context, root string) []WorkTreePeer {
	f.roots = append(f.roots, root)
	return f.peers
}

func (f *peerWorkTreeFake) NoteWrite(_ context.Context, absPath string, _ time.Time) {
	f.noted = append(f.noted, absPath)
}

func (f *peerWorkTreeFake) TouchesBetween(_ context.Context, root string, _, _ time.Time) []PeerTouch {
	f.roots = append(f.roots, root)
	return f.touches
}

// The note the model reads when another agent wrote the same file: one line at
// the end of the result, with who and how long ago — and never on the card.
func TestFileToolsNoteAnotherAgentsWriteOnTheResult(t *testing.T) {
	root := t.TempDir()
	st := NewState(root)
	ctx := WithToolCompletionCapture(WithProjectRoot(context.Background(), root))

	threeMinutesAgo := time.Now().Add(-3 * time.Minute)
	peers := &peerWorkTreeFake{touches: []PeerTouch{{
		Path:      "b.go",
		AgentName: "b",
		Task:      "refactoring the parser",
		At:        threeMinutesAgo,
	}}}
	writeTool, err := NewFileWriteTool(st, &AgentToolRuntime{Peers: peers})
	if err != nil {
		t.Fatalf("NewFileWriteTool: %v", err)
	}
	editTool, err := NewFileEditToolWithOptions(st, FileEditOptions{Peers: peers})
	if err != nil {
		t.Fatalf("NewFileEditToolWithOptions: %v", err)
	}

	created := filepath.Join(root, "b.go")
	result := handleFileTool(t, ctx, writeTool, map[string]string{"file_path": created, "content": "package b\n"})
	want := "ok\n\nNote: b.go was also edited 3 min ago by b (task: refactoring the parser). Coordinate with peer_send before changing it further."
	if result != want {
		t.Fatalf("write_file result = %q, want %q", result, want)
	}
	if completion, ok := ToolCompletionFromContext(ctx); !ok || strings.Contains(fmt.Sprint(completion.Output), "was also edited") {
		t.Fatalf("completion = %#v, want the note kept off the card", completion.Output)
	}

	edited := filepath.Join(root, "b.go")
	rememberReadState(t, st, edited)
	result = handleFileTool(t, ctx, editTool, map[string]string{"file_path": edited, "old_string": "package b", "new_string": "package bee"})
	if !strings.HasSuffix(result, want[strings.Index(want, "Note: "):]) {
		t.Fatalf("edit_file result = %q, want the note at the end", result)
	}

	// Both tools announced their own write, so the next agent's guard sees it.
	if len(peers.noted) != 2 || peers.noted[0] != created || peers.noted[1] != edited {
		t.Fatalf("announced = %#v, want both writes", peers.noted)
	}
}

// Nobody else wrote the file: the result is exactly what it was before the
// guard existed.
func TestFileToolsResultUnchangedWithoutAnotherAgentsWrite(t *testing.T) {
	root := t.TempDir()
	st := NewState(root)
	ctx := WithToolCompletionCapture(WithProjectRoot(context.Background(), root))
	peers := &peerWorkTreeFake{}

	writeTool, err := NewFileWriteTool(st, &AgentToolRuntime{Peers: peers})
	if err != nil {
		t.Fatalf("NewFileWriteTool: %v", err)
	}
	result := handleFileTool(t, ctx, writeTool, map[string]string{"file_path": filepath.Join(root, "quiet.go"), "content": "package quiet\n"})
	if result != "ok" {
		t.Fatalf("write_file result = %q, want the bare ok", result)
	}
	if len(peers.noted) != 1 {
		t.Fatalf("announced = %#v, want this call's own write", peers.noted)
	}
}

// A patch that touches several files reports one note per path, after the
// summary line.
func TestApplyPatchNotesAnotherAgentsWrite(t *testing.T) {
	root := t.TempDir()
	st := NewState(root)
	peers := &peerWorkTreeFake{touches: []PeerTouch{{
		Path:      "c.go",
		AgentName: "c",
		At:        time.Now().Add(-2 * time.Minute),
	}}}
	shellTool, err := NewShellTool(st, &AgentToolRuntime{
		Cfg:   &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
		Peers: peers,
	})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	ctx := llm.WithAgentSessionID(WithToolCompletionCapture(WithProjectRoot(context.Background(), root)), "sess-peer-patch")
	patch := fmt.Sprintf("apply_patch <<'PATCH'\n*** Begin Patch\n*** Add File: %s\n+package c\n*** End Patch\nPATCH", filepath.Join(root, "c.go"))
	encoded, err := json.Marshal(map[string]string{"command": patch})
	if err != nil {
		t.Fatalf("encode args: %v", err)
	}
	raw, err := shellTool.Handle(ctx, string(encoded))
	if err != nil {
		t.Fatalf("apply_patch: %v", err)
	}
	// The shell tool answers with the payload the model reads, not with a bare
	// string: the note has to be inside its stdout.
	var payload map[string]any
	if err := json.Unmarshal([]byte(fmt.Sprint(raw)), &payload); err != nil {
		t.Fatalf("apply_patch payload %v: %v", raw, err)
	}
	result, _ := payload["stdout"].(string)
	want := "Note: c.go was also edited 2 min ago by c. Coordinate with peer_send before changing it further."
	if !strings.HasSuffix(result, want+"\n") {
		t.Fatalf("apply_patch stdout = %q, want it to end with %q", result, want)
	}
	if !strings.HasPrefix(result, "Applied patch to 1 file(s).") {
		t.Fatalf("apply_patch stdout = %q, want the summary first", result)
	}
	if len(peers.noted) != 1 || peers.noted[0] != filepath.Join(root, "c.go") {
		t.Fatalf("announced = %#v, want the patched path", peers.noted)
	}
}

// handleFileTool runs one file tool call and returns the text the model reads.
func handleFileTool(t *testing.T, ctx context.Context, tool *llm.Tool, args map[string]string) string {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encode args: %v", err)
	}
	raw, err := tool.Handle(ctx, string(encoded))
	if err != nil {
		t.Fatalf("%s: %v", args["file_path"], err)
	}
	result, _ := raw.(string)
	return result
}

// rememberReadState satisfies the read a write/edit owes the tool, the way a
// read_file call would.
func rememberReadState(t *testing.T, st *State, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st.RememberRead(path, fi.ModTime(), fi.Size(), raw)
}

// shellApprovalRecorder stands in for the approval queue: it records every
// question the shell tool put and answers "pending".
type shellApprovalRecorder struct {
	asked  int
	inputs []map[string]any
}

func (r *shellApprovalRecorder) hook(_ context.Context, _ string, payload any) (string, bool, error) {
	r.asked++
	input, _ := payload.(map[string]any)
	r.inputs = append(r.inputs, input)
	return "action-peer", true, nil
}

// peerShellTool builds a shell tool whose port reports the given same-tree
// agents, in a directory of its own, with the recorder's hook installed.
func peerShellTool(t *testing.T, rec *shellApprovalRecorder, peers PeerWorkTree) (*llm.Tool, string) {
	t.Helper()
	root := t.TempDir()
	st := NewState(root)
	st.SetActionHook(rec.hook)
	shellTool, err := NewShellTool(st, &AgentToolRuntime{Cfg: &appcfg.Root{}, Peers: peers})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	return shellTool, root
}

func peerShellCtx(root string) context.Context {
	return WithToolCompletionCapture(WithProjectRoot(llm.WithAgentSessionID(context.Background(), "sess-peer-git"), root))
}

// A git command that would sweep this tree is nobody else's question any more:
// same-tree coordination is the agents' own business (owner 2026-11-05), so
// the shell tool asks nothing even while another agent has work in the tree.
func TestShellToolDoesNotAskBeforeAGitSweepWhileAnotherAgentWorks(t *testing.T) {
	rec := &shellApprovalRecorder{}
	shellTool, root := peerShellTool(t, rec, &peerWorkTreeFake{
		peers: []WorkTreePeer{{Name: "b", Kind: "terminal", Status: "busy", RecentFiles: []string{"b.go"}}},
	})
	if _, err := shellTool.Handle(peerShellCtx(root), `{"command":"git stash"}`); err != nil {
		t.Fatalf("git stash: %v", err)
	}
	if rec.asked != 0 {
		t.Fatalf("asked=%d, want no approval for a same-tree git sweep", rec.asked)
	}
}

// A command that failed while another agent was writing files under the same
// tree says which ones, so the model attributes the failure instead of
// repairing somebody else's half-finished edit. The card keeps the command's
// own output (plan 198 §3.6).
func TestFailedShellCommandNamesTheFilesAnotherAgentChanged(t *testing.T) {
	root := t.TempDir()
	st := NewState(root)
	peers := &peerWorkTreeFake{touches: []PeerTouch{
		{Path: "b.go", AgentName: "b", At: time.Now().Add(-time.Minute)},
		{Path: "pkg/c.go", AgentName: "c", At: time.Now().Add(-20 * time.Second)},
	}}
	shellTool, err := NewShellTool(st, &AgentToolRuntime{Cfg: &appcfg.Root{}, Peers: peers})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	ctx := peerShellCtx(root)
	raw, err := shellTool.Handle(ctx, `{"command":"false"}`)
	if err != nil {
		t.Fatalf("false: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(fmt.Sprint(raw)), &payload); err != nil {
		t.Fatalf("payload %v: %v", raw, err)
	}
	if payload["exit_code"] != float64(1) {
		t.Fatalf("exit_code = %#v, want the failing exit", payload["exit_code"])
	}
	changed, _ := payload["changed_by_other_agents"].([]any)
	if len(changed) != 2 {
		t.Fatalf("changed_by_other_agents = %#v, want both writes", payload["changed_by_other_agents"])
	}
	first, _ := changed[0].(map[string]any)
	if first["path"] != "b.go" || first["agent"] != "b" || first["minutes_ago"] != float64(1) {
		t.Fatalf("changed[0] = %#v, want the newest write named", first)
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected the shell completion")
	}
	if _, present := completion.Output["changed_by_other_agents"]; present {
		t.Fatalf("completion = %#v, want the attribution kept off the card", completion.Output)
	}
}

// Nothing else was written while the command ran: a failure stays the model's
// own to explain.
func TestFailedShellCommandWithoutOtherAgentsCarriesNoAttribution(t *testing.T) {
	root := t.TempDir()
	shellTool, err := NewShellTool(NewState(root), &AgentToolRuntime{Cfg: &appcfg.Root{}, Peers: &peerWorkTreeFake{}})
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	raw, err := shellTool.Handle(peerShellCtx(root), `{"command":"false"}`)
	if err != nil {
		t.Fatalf("false: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(fmt.Sprint(raw)), &payload); err != nil {
		t.Fatalf("payload %v: %v", raw, err)
	}
	if _, present := payload["changed_by_other_agents"]; present {
		t.Fatalf("payload = %#v, want no attribution", payload)
	}
}
