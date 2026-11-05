package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// peerWorkTreeFake answers the three same-tree questions the way another
// process would: a fixed list of other agents' writes, and a record of what the
// tools announced to it.
type peerWorkTreeFake struct {
	peers   []WorkTreePeer
	touches []PeerTouch
	noted   []string
	roots   []string

	// The git sweep guard asks a narrower question than the same-tree guards
	// do, so the two answers are set apart: sweep is what SweepPeers says, and
	// sweepRoots records the directory it was asked about.
	sweep      []WorkTreePeer
	sweepRoots []string
}

func (f *peerWorkTreeFake) WorkTreePeers(_ context.Context, root string) []WorkTreePeer {
	f.roots = append(f.roots, root)
	return f.peers
}

func (f *peerWorkTreeFake) SweepPeers(_ context.Context, root string) []WorkTreePeer {
	f.sweepRoots = append(f.sweepRoots, root)
	return f.sweep
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

// A git command that would sweep this tree is a question whenever another agent
// is working in it, and the payload names who and what they touched (plan 198
// §3.5): the command string alone cannot say whether it is safe to run.
func TestShellToolAsksBeforeAGitSweepWhileAnotherAgentWorks(t *testing.T) {
	rec := &shellApprovalRecorder{}
	shellTool, root := peerShellTool(t, rec, &peerWorkTreeFake{
		peers: []WorkTreePeer{{
			Name: "b", Kind: "terminal", Status: "busy", Task: "the parser",
			RecentFiles: []string{"b.go", "pkg/c.go"},
		}},
		sweep: []WorkTreePeer{{
			Name: "b", Kind: "terminal", Status: "busy", Task: "the parser",
			RecentFiles: []string{"b.go", "pkg/c.go"},
		}},
	})

	_, err := shellTool.Handle(peerShellCtx(root), `{"command":"git stash"}`)
	var pending *RequiresActionError
	if !errors.As(err, &pending) || pending.ActionKind != "shell" {
		t.Fatalf("err=%v, want a pending shell approval", err)
	}
	if rec.asked != 1 {
		t.Fatalf("asked=%d, want the one question", rec.asked)
	}
	asked := rec.inputs[0]
	for _, key := range []string{"force_tool_approval", "ask_even_in_yolo"} {
		if asked[key] != true {
			t.Fatalf("%s = %#v, want true", key, asked[key])
		}
	}
	if asked["approval_reason"] != "peer_work_tree" {
		t.Fatalf("approval_reason = %#v, want peer_work_tree", asked["approval_reason"])
	}
	if asked["dangerous_rule"] != "git_stash" {
		t.Fatalf("dangerous_rule = %#v, want git_stash", asked["dangerous_rule"])
	}
	if names, _ := asked["peer_names"].([]string); len(names) != 1 || names[0] != "b" {
		t.Fatalf("peer_names = %#v, want the one peer", asked["peer_names"])
	}
	peers, _ := asked["peer_files"].([]map[string]any)
	if len(peers) != 1 || peers[0]["name"] != "b" {
		t.Fatalf("peer_files = %#v, want the one peer with its files", asked["peer_files"])
	}
	if files, _ := peers[0]["files"].([]string); len(files) != 2 || files[0] != "b.go" {
		t.Fatalf("peer_files[0].files = %#v, want the files it edited", peers[0]["files"])
	}
}

// A sweep is judged in the directory the command runs in, not in the session's
// project root. A linked work tree nested in the main tree has its own working
// tree and index: `git stash` inside it cannot touch the agent working in the
// main tree, so listing that agent would put a false claim on the card — and
// this approval is asked even under full access.
func TestShellToolJudgesAGitSweepInTheDirectoryItChanges(t *testing.T) {
	rec := &shellApprovalRecorder{}
	fake := &peerWorkTreeFake{peers: []WorkTreePeer{{
		Name: "forebrain-harness-65", Kind: "terminal", Status: "busy",
	}}}
	shellTool, root := peerShellTool(t, rec, fake)
	worktree := filepath.Join(root, ".worktrees", "232")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(map[string]string{"command": "cd " + worktree + " && git stash"})
	if err != nil {
		t.Fatalf("encode args: %v", err)
	}
	if _, err := shellTool.Handle(peerShellCtx(root), string(encoded)); err != nil {
		var pending *RequiresActionError
		if !errors.As(err, &pending) {
			t.Fatalf("cd ... && git stash: %v", err)
		}
	}
	if len(fake.sweepRoots) != 1 {
		t.Fatalf("sweep roots = %#v, want the one directory the command changes in (%s)", fake.sweepRoots, worktree)
	}
	if fake.sweepRoots[0] != worktree {
		t.Fatalf("sweep root = %q, want the command's own directory %q", fake.sweepRoots[0], worktree)
	}
	for _, asked := range rec.inputs {
		if asked["approval_reason"] == "peer_work_tree" {
			t.Fatalf("asked %#v, want no peer question for a sweep in a work tree nobody else works in", asked)
		}
	}
}

// The same command with nobody else in the tree is nobody else's business: the
// guard asks nothing and the command runs its ordinary path.
func TestShellToolLeavesAGitSweepAloneWithoutOtherAgents(t *testing.T) {
	empty := &shellApprovalRecorder{}
	shellTool, root := peerShellTool(t, empty, &peerWorkTreeFake{})
	if _, err := shellTool.Handle(peerShellCtx(root), `{"command":"git stash list"}`); err != nil {
		t.Fatalf("git stash list: %v", err)
	}
	if empty.asked != 0 {
		t.Fatalf("asked=%d, want no question", empty.asked)
	}

	readOnly := &shellApprovalRecorder{}
	shellTool, root = peerShellTool(t, readOnly, &peerWorkTreeFake{peers: []WorkTreePeer{{Name: "b"}}})
	if _, err := shellTool.Handle(peerShellCtx(root), `{"command":"git stash list"}`); err != nil {
		t.Fatalf("git stash list with a peer present: %v", err)
	}
	if readOnly.asked != 0 {
		t.Fatalf("asked=%d, want no question for a read-only stash form", readOnly.asked)
	}
}

// A batch task has nobody to ask: the task sandbox is the enforcement, so the
// guard stays out of its way exactly as every other approval does (plan 198
// §3.5).
func TestBatchShellGitSweepIsNotPutToAnyone(t *testing.T) {
	rec := &shellApprovalRecorder{}
	shellTool, root := peerShellTool(t, rec, &peerWorkTreeFake{peers: []WorkTreePeer{{Name: "b"}}})
	if _, err := shellTool.Handle(WithBatchBoundary(peerShellCtx(root), BatchBoundary{Workspace: root}), `{"command":"git stash"}`); err != nil {
		t.Fatalf("batch git stash: %v", err)
	}
	if rec.asked != 0 {
		t.Fatalf("asked=%d, want nobody asked in a batch task", rec.asked)
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
