package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

// nopCodeIntel satisfies both code intelligence ports with empty methods so
// the interfaces' shape stays compile-checked.
type nopCodeIntel struct{}

var _ CodeIntelligence = nopCodeIntel{}
var _ CodeIntelControl = nopCodeIntel{}

func (nopCodeIntel) Handles(absPath string) bool { return false }

func (nopCodeIntel) Query(ctx context.Context, q CodeIntelQuery) (CodeIntelResult, error) {
	return CodeIntelResult{}, nil
}

func (nopCodeIntel) DidWrite(ctx context.Context, agentSessionID string, changes []FileChange) DiagnosticsDelta {
	return DiagnosticsDelta{}
}

func (nopCodeIntel) DidRead(ctx context.Context, absPath string, content []byte) {}

func (nopCodeIntel) DidRunShell(ctx context.Context) {}

func (nopCodeIntel) PeekLate(agentSessionID string) (text string, token uint64) {
	return "", 0
}

func (nopCodeIntel) AckLate(agentSessionID string, token uint64) {}

func (nopCodeIntel) Snapshot() event.LSPSnapshot { return event.LSPSnapshot{} }

func (nopCodeIntel) Subscribe(fn func(event.LSPSnapshot)) (cancel func()) { return func() {} }

func (nopCodeIntel) SetEnabled(serverID string, enabled bool) error { return nil }

func (nopCodeIntel) Restart(serverID string) error { return nil }

func (nopCodeIntel) Install(ctx context.Context, serverID string, progress func(line string)) error {
	return nil
}

func (nopCodeIntel) SetRecommendationListener(fn func(ctx context.Context, rec event.LSPRecommendation)) {
}

func (nopCodeIntel) DecideRecommendation(recommendationID string, choice event.LSPRecommendationChoice) error {
	return nil
}

func (nopCodeIntel) ResetRecommendations() error { return nil }

func TestLSPOperationsListsEveryOperationOnce(t *testing.T) {
	t.Parallel()
	if len(LSPOperations) != 13 {
		t.Fatalf("len(LSPOperations)=%d want 13 (%v)", len(LSPOperations), LSPOperations)
	}
	seen := make(map[string]bool, len(LSPOperations))
	for _, op := range LSPOperations {
		if seen[op] {
			t.Fatalf("LSPOperations lists %q twice (%v)", op, LSPOperations)
		}
		seen[op] = true
	}
	if first := LSPOperations[0]; first != "definition" {
		t.Fatalf("LSPOperations[0]=%q want %q", first, LSPOpDefinition)
	}
	if last := LSPOperations[len(LSPOperations)-1]; last != "diagnostics" {
		t.Fatalf("LSPOperations[last]=%q want %q", last, LSPOpDiagnostics)
	}
}

func TestDiagnosticsDeltaEmpty(t *testing.T) {
	t.Parallel()
	var zero DiagnosticsDelta
	if !zero.Empty() {
		t.Fatal("zero DiagnosticsDelta.Empty()=false want true")
	}
	spoken := DiagnosticsDelta{Text: "<diagnostics>…</diagnostics>"}
	if spoken.Empty() {
		t.Fatal("DiagnosticsDelta with Text.Empty()=true want false")
	}
}

// recordingCodeIntel records the queries the lsp tool passes to the runtime.
type recordingCodeIntel struct {
	nopCodeIntel
	queries []CodeIntelQuery
	result  CodeIntelResult
	err     error
}

func (r *recordingCodeIntel) Query(ctx context.Context, q CodeIntelQuery) (CodeIntelResult, error) {
	r.queries = append(r.queries, q)
	return r.result, r.err
}

// appendixB1Description is appendix B.1 of the LSP code intelligence spec,
// copied byte-for-byte. The lsp tool's description is part of the prompt
// prefix: one edited byte re-bills every cached token of every LSP-enabled
// session, so this golden fails before such a change can ship quietly.
const appendixB1Description = "Look up code through the project's language servers: definitions, declarations, type definitions, implementations, references, hover type information, document and workspace symbols, call and type hierarchies, and current diagnostics. Read-only. Lines are 1-based and numbered the way file reads show them. Pass symbol (a name on that line) instead of column when you are not sure of the exact column. Prefer this over text search when you need where a symbol is defined or used."

func TestLSPToolDescriptionIsAppendixB1(t *testing.T) {
	t.Parallel()
	if lspToolDescription != appendixB1Description {
		t.Fatalf("lspToolDescription drifted from appendix B.1:\ngot  %q\nwant %q", lspToolDescription, appendixB1Description)
	}
}

// lspToolSchemaGolden is the exact JSON schema of the lsp tool's input. The
// schema is part of the prompt prefix (spec §3.2): any field, enum, bound or
// description change invalidates every cached prefix of an LSP-enabled
// session, so it is pinned byte-for-byte here.
const lspToolSchemaGolden = `{"$id":"https://github.com/forebrain-harness/forebrain-harness/pkg/tool/lsp-input","$schema":"https://json-schema.org/draft/2020-12/schema","additionalProperties":false,"properties":{"column":{"description":"1-based column in characters. Omit when symbol is given.","minimum":1,"type":"integer"},"file_path":{"description":"Absolute or workspace-relative file path. Required for every operation except workspace_symbols.","type":"string"},"include_declaration":{"description":"For references: also return the declaration itself.","type":"boolean"},"line":{"description":"1-based line number, as shown by file reads.","minimum":1,"type":"integer"},"max_results":{"description":"Maximum locations to return (default 50, at most 200).","maximum":200,"minimum":1,"type":"integer"},"operation":{"description":"What to look up.","enum":["definition","declaration","type_definition","implementation","references","hover","document_symbols","workspace_symbols","incoming_calls","outgoing_calls","supertypes","subtypes","diagnostics"],"type":"string"},"query":{"description":"Symbol name or prefix to search for. Required for workspace_symbols.","type":"string"},"symbol":{"description":"A name on the given line to position on, used instead of column.","type":"string"}},"required":["operation"],"type":"object"}`

func TestLSPToolSchemaGolden(t *testing.T) {
	t.Parallel()
	tool, err := NewLSPTool(NewState(t.TempDir()), &AgentToolRuntime{CodeIntel: nopCodeIntel{}})
	if err != nil {
		t.Fatalf("NewLSPTool: %v", err)
	}
	got, err := json.Marshal(tool.InputSchema())
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	if string(got) != lspToolSchemaGolden {
		t.Fatalf("lsp schema drifted (prompt prefix stability):\ngot  %s\nwant %s", got, lspToolSchemaGolden)
	}
}

func TestLSPToolPassesQuery(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "main.go")
	if err := os.WriteFile(p, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	stub := &recordingCodeIntel{result: CodeIntelResult{
		Text:    "definition of `X`: 1 result in 1 file",
		Display: map[string]any{"result_count": 1},
	}}
	st := NewState(root)
	tool, err := NewLSPTool(st, &AgentToolRuntime{CodeIntel: stub})
	if err != nil {
		t.Fatalf("NewLSPTool: %v", err)
	}
	ctx := WithToolCompletionCapture(context.Background())
	out, err := tool.Handle(ctx, `{"operation":"  DEFINITION ","file_path":"`+escapeJSONPath(p)+`","line":3,"symbol":"X","include_declaration":true,"max_results":500}`)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if out != stub.result.Text {
		t.Fatalf("tool output = %q, want the runtime text %q", out, stub.result.Text)
	}
	if len(stub.queries) != 1 {
		t.Fatalf("runtime saw %d queries, want 1", len(stub.queries))
	}
	q := stub.queries[0]
	if q.Operation != "definition" {
		t.Fatalf("Operation = %q, want normalized %q", q.Operation, "definition")
	}
	if q.AbsPath != p {
		t.Fatalf("AbsPath = %q, want resolved %q", q.AbsPath, p)
	}
	if q.DisplayPath != p {
		t.Fatalf("DisplayPath = %q, want the model's original argument %q", q.DisplayPath, p)
	}
	if q.MaxResults != 200 {
		t.Fatalf("MaxResults = %d, want capped 200", q.MaxResults)
	}
	if q.Line != 3 || q.Symbol != "X" || !q.IncludeDeclaration {
		t.Fatalf("query position/flags drifted: %+v", q)
	}
	if q.PreviewAllowed == nil {
		t.Fatal("PreviewAllowed was not passed to the runtime")
	}
	if !q.PreviewAllowed(p) {
		t.Fatalf("PreviewAllowed(%q) = false, want true for an in-root path", p)
	}
	completion, ok := ToolCompletionFromContext(ctx)
	if !ok {
		t.Fatal("expected tool completion capture")
	}
	for _, key := range []string{"operation", "file_path", "line", "symbol", "output", "result_count"} {
		if _, ok := completion.Output[key]; !ok {
			t.Fatalf("captured output missing %q: %+v", key, completion.Output)
		}
	}
	if completion.Output["operation"] != "definition" {
		t.Fatalf("captured operation = %v, want the normalized one", completion.Output["operation"])
	}
	if completion.Output["output"] != stub.result.Text {
		t.Fatalf("captured output text = %v, want the runtime text", completion.Output["output"])
	}
}

func TestLSPToolMaxResultsDefaultsToFifty(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := filepath.Join(root, "a.go")
	if err := os.WriteFile(p, []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	stub := &recordingCodeIntel{}
	tool, err := NewLSPTool(NewState(root), &AgentToolRuntime{CodeIntel: stub})
	if err != nil {
		t.Fatalf("NewLSPTool: %v", err)
	}
	if _, err := tool.Handle(context.Background(), `{"operation":"document_symbols","file_path":"`+escapeJSONPath(p)+`"}`); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(stub.queries) != 1 || stub.queries[0].MaxResults != 50 {
		t.Fatalf("MaxResults not defaulted to 50: %+v", stub.queries)
	}
}

func TestLSPToolAuthorizesLikeReadFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	outsidePath := filepath.Join(outside, "note.txt")
	if err := os.WriteFile(outsidePath, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	deniedDir := t.TempDir()
	deniedPath := filepath.Join(deniedDir, "secret.txt")
	if err := os.WriteFile(deniedPath, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write denied file: %v", err)
	}

	// A confined state refuses outside its root instead of asking.
	confined := NewState(root)
	confined.ConfineToRoot(root)
	stub := &recordingCodeIntel{}
	tool, err := NewLSPTool(confined, &AgentToolRuntime{CodeIntel: stub})
	if err != nil {
		t.Fatalf("NewLSPTool: %v", err)
	}
	readTool, err := NewFileReadTool(confined)
	if err != nil {
		t.Fatalf("NewFileReadTool: %v", err)
	}
	args := `{"operation":"definition","file_path":"` + escapeJSONPath(outsidePath) + `","line":1,"column":1}`
	_, lspErr := tool.Handle(context.Background(), args)
	_, readErr := readTool.Handle(context.Background(), `{"file_path":"`+escapeJSONPath(outsidePath)+`"}`)
	if !errors.Is(lspErr, ErrPathNotAllowed) {
		t.Fatalf("confined lsp error = %v, want ErrPathNotAllowed", lspErr)
	}
	if !errors.Is(readErr, ErrPathNotAllowed) {
		t.Fatalf("confined read_file error = %v, want ErrPathNotAllowed (same class as lsp)", readErr)
	}
	if len(stub.queries) != 0 {
		t.Fatalf("runtime was contacted %d times on a refused path, want 0", len(stub.queries))
	}

	// deny_read is a hard refusal for both tools alike.
	stub2 := &recordingCodeIntel{}
	st := NewState(root)
	st.SetReadPolicy([]string{deniedDir}, nil, nil, root)
	tool2, err := NewLSPTool(st, &AgentToolRuntime{CodeIntel: stub2})
	if err != nil {
		t.Fatalf("NewLSPTool: %v", err)
	}
	_, lspErr = tool2.Handle(context.Background(), `{"operation":"hover","file_path":"`+escapeJSONPath(deniedPath)+`","line":1,"column":1}`)
	if !errors.Is(lspErr, ErrPathReadDenied) {
		t.Fatalf("denied lsp error = %v, want ErrPathReadDenied", lspErr)
	}
	if len(stub2.queries) != 0 {
		t.Fatalf("runtime was contacted %d times on a denied path, want 0", len(stub2.queries))
	}
}

func TestLSPToolPreviewAllowed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	denied := filepath.Join(root, "denied")
	if err := os.MkdirAll(denied, 0o755); err != nil {
		t.Fatalf("mkdir denied: %v", err)
	}
	if err := os.WriteFile(filepath.Join(denied, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write denied file: %v", err)
	}
	st := NewState(root)
	st.SetReadPolicy([]string{denied}, nil, nil, root)
	stub := &recordingCodeIntel{}
	tool, err := NewLSPTool(st, &AgentToolRuntime{CodeIntel: stub})
	if err != nil {
		t.Fatalf("NewLSPTool: %v", err)
	}
	if _, err := tool.Handle(context.Background(), `{"operation":"workspace_symbols","query":"Foo"}`); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(stub.queries) != 1 || stub.queries[0].PreviewAllowed == nil {
		t.Fatalf("runtime did not receive a PreviewAllowed: %+v", stub.queries)
	}
	allowed := stub.queries[0].PreviewAllowed
	if p := filepath.Join(root, "main.go"); !allowed(p) {
		t.Fatalf("PreviewAllowed in-root = false, want true")
	}
	if p := filepath.Join(t.TempDir(), "main.go"); allowed(p) {
		t.Fatalf("PreviewAllowed out-of-root = true, want false")
	}
	if p := filepath.Join(denied, "main.go"); allowed(p) {
		t.Fatalf("PreviewAllowed denied = true, want false")
	}
}

func TestLSPToolRequiresRuntime(t *testing.T) {
	t.Parallel()
	tool, err := NewLSPTool(NewState(t.TempDir()), &AgentToolRuntime{})
	if err != nil {
		t.Fatalf("NewLSPTool: %v", err)
	}
	if tool == nil {
		t.Fatal("NewLSPTool returned nil tool")
	}
	_, err = tool.Handle(context.Background(), `{"operation":"hover","file_path":"/tmp/x.go","line":1,"column":1}`)
	if err == nil || !strings.Contains(err.Error(), "language servers are not available") {
		t.Fatalf("handle without runtime error = %v, want unavailable error", err)
	}
}
