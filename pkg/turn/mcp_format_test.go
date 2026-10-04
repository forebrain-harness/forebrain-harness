package turn

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	mcpkg "github.com/forebrain-harness/forebrain-harness/pkg/mcp"
)

// TestCountMCPLineRendersOnlyNonZeroStates covers the /status MCP row.
func TestCountMCPLineRendersOnlyNonZeroStates(t *testing.T) {
	line := MCPCountLine{Total: 5, Connected: 2, Failed: 1, NeedsAuth: 1, Disabled: 1}
	got := line.Render()
	for _, want := range []string{"2 connected", "1 failed", "1 needs authentication", "1 disabled"} {
		if !strings.Contains(got, want) {
			t.Fatalf("line %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "0 ") {
		t.Fatalf("line %q must not contain zero counts", got)
	}
	if (MCPCountLine{}).Render() != "" {
		t.Fatal("an empty line must render empty; the row is omitted, never \"0 servers\"")
	}
}

func rawTool(t *testing.T, name string, schema map[string]any) *llm.Tool {
	t.Helper()
	tool, err := llm.NewRawTool(name, "desc of "+name, schema, func(context.Context, string) (any, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

// TestBuildMCPInventoryStatesAndNextSession covers the list semantics: live
// states, a failed server's own error text verbatim, a connected server that
// still needs authentication counted once (as needing it), a running server
// marked for the next session, and a server the store kept out of this
// session shown as disabled — never started, with no tools. /status's counts
// come from the same inventory.
func TestBuildMCPInventoryStatesAndNextSession(t *testing.T) {
	servers := []appcfg.MCPServerConfig{
		{Name: "global-one", Command: "global-one", Args: []string{"serve", "--mcp"}, Transport: "stdio"},
		{Name: "broken", Transport: "stdio", Command: "broken"},
		{Name: "notion", URL: "https://user:pw@mcp.notion.test/mcp?token=x", Headers: map[string]string{"B": "1", "A": "2"}},
	}
	disabled := []appcfg.MCPServerConfig{{Name: "paused", Transport: "stdio", Command: "paused"}}
	runtime := &MCPRuntimeView{Servers: []mcpkg.ServerRecord{
		{Name: "global-one", ConnStatus: mcpkg.ConnStatusConnected, AuthStatus: mcpkg.AuthStatusAuthenticated},
		{Name: "broken", ConnStatus: mcpkg.ConnStatusError, Error: "connection refused"},
		{Name: "notion", ConnStatus: mcpkg.ConnStatusConnected, AuthStatus: mcpkg.AuthStatusNeedsAuth},
	}}
	inv := BuildMCPInventory(MCPInventorySource{
		Servers:  servers,
		Disabled: disabled,
		DisabledNext: func(srv appcfg.MCPServerConfig) bool {
			return srv.Name == "global-one" || srv.Name == "paused"
		},
		Runtime:      runtime,
		Tools:        []*llm.Tool{rawTool(t, "mcp__global-one__search", nil)},
		GlobalSource: "/home/u/.forebrain/forebrain.yaml",
	})
	if inv.Total != 4 || len(inv.Groups) != 1 || len(inv.Groups[0].Entries) != 4 {
		t.Fatalf("inventory = %+v", inv)
	}
	one, broken, notion, paused := inv.Entry("global-one"), inv.Entry("broken"), inv.Entry("NOTION"), inv.Entry("paused")
	// A server with no OAuth reads "none" even though the runtime records
	// every accepted connection as authenticated.
	if one.Status != MCPStatusConnected || one.Command != "global-one serve --mcp" || one.ToolCount != 1 || !one.DisabledNext || one.Auth != "none" {
		t.Fatalf("global-one = %+v", one)
	}
	if got := MCPServerStatusLabel(*one); got != "✓ connected · 1 tool · disabled from next session" {
		t.Fatalf("running-but-disabled label = %q", got)
	}
	if broken.Status != MCPStatusFailed || broken.Error != "connection refused" {
		t.Fatalf("broken = %+v", broken)
	}
	if notion.Status != MCPStatusNeedsAuth || notion.URL != "https://mcp.notion.test/mcp" || strings.Join(notion.HeaderKeys, ",") != "A,B" {
		t.Fatalf("notion = %+v", notion)
	}
	if paused.Running || paused.Status != MCPStatusDisabled || paused.ToolCount != 0 {
		t.Fatalf("paused = %+v", paused)
	}
	if got := MCPServerStatusLabel(*paused); got != "○ disabled" {
		t.Fatalf("disabled label = %q", got)
	}
	counts := inv.Counts()
	if counts != (MCPCountLine{Total: 4, Connected: 1, Failed: 1, NeedsAuth: 1, Disabled: 1}) {
		t.Fatalf("counts = %+v, want every server counted once by its state", counts)
	}
	rendered := RenderMCPInventoryMarkdown(inv)
	for _, want := range []string{"Global MCPs (/home/u/.forebrain/forebrain.yaml)", "Error: connection refused", "△ needs authentication"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("markdown %q missing %q", rendered, want)
		}
	}
	// Re-enabled mid-session, a server kept out of this one says what the next
	// session will do.
	paused.DisabledNext = false
	if got := MCPServerStatusLabel(*paused); got != "○ disabled · enabled from next session" {
		t.Fatalf("re-enabled label = %q", got)
	}
}

// TestAttributeMCPToolsUsesTheLongestServerPrefix pins attribution by the
// mcp__<NormalizeName(server)>__ contract: a server whose normalized name
// contains "__" keeps its own tools, and parameters come in a stable order —
// required first, then by name — never the schema map's iteration order.
func TestAttributeMCPToolsUsesTheLongestServerPrefix(t *testing.T) {
	servers := []appcfg.MCPServerConfig{{Name: "a"}, {Name: "a__b"}}
	schema := map[string]any{
		"type":     "object",
		"required": []any{"zeta"},
		"properties": map[string]any{
			"beta":  map[string]any{"type": "string"},
			"alpha": map[string]any{"type": []any{"string", "null"}},
			"zeta":  map[string]any{"type": "object", "properties": map[string]any{"inner": map[string]any{"type": "integer"}}},
		},
	}
	got := AttributeMCPTools(servers, []*llm.Tool{
		rawTool(t, "mcp__a__b__deep", nil),
		rawTool(t, "mcp__a__shallow", schema),
		rawTool(t, "read_file", nil),
	})
	if len(got["a__b"]) != 1 || got["a__b"][0].Name != "deep" {
		t.Fatalf("a__b tools = %+v", got["a__b"])
	}
	if len(got["a"]) != 1 || got["a"][0].Name != "shallow" {
		t.Fatalf("a tools = %+v", got["a"])
	}
	params := got["a"][0].Params
	var names []string
	for _, p := range params {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "zeta,alpha,beta" {
		t.Fatalf("param order = %v, want required first then by name", names)
	}
	if params[1].Type != "string | null" || len(params[0].Children) != 1 || params[0].Children[0].Name != "inner" {
		t.Fatalf("params = %+v", params)
	}
}

// TestBuildMCPInventoryEmptySaysWhereToConfigure pins the empty case: one
// sentence and the two files servers live in, never JSON.
func TestBuildMCPInventoryEmptySaysWhereToConfigure(t *testing.T) {
	got := RenderMCPInventoryMarkdown(BuildMCPInventory(MCPInventorySource{
		GlobalSource:  "/home/u/.forebrain/forebrain.yaml",
		ProjectSource: "/proj/.forebrain/mcp_servers.yaml",
	}))
	want := "No MCP servers configured\n\nGlobal servers live in /home/u/.forebrain/forebrain.yaml\nProject servers live in /proj/.forebrain/mcp_servers.yaml"
	if got != want {
		t.Fatalf("empty inventory = %q", got)
	}
}

// TestBuildMCPInventoryProjectNotes covers the scope sections: project entries
// not in effect, overridden globals, pending on-disk changes.
func TestBuildMCPInventoryProjectNotes(t *testing.T) {
	inv := BuildMCPInventory(MCPInventorySource{
		Servers: []appcfg.MCPServerConfig{{Name: "live", Command: "live"}},
		Scope: mcpkg.ProjectMCPScopeSummary{
			OverriddenGlobal: []string{"replaced"},
			NotApplied:       []mcpkg.MCPNotApplied{{Name: "ghost", Reason: "command not found"}},
			PendingReload:    true,
		},
	})
	rendered := RenderMCPInventoryMarkdown(inv)
	for _, want := range []string{
		"Not in effect: ghost — command not found",
		"take effect in a new session",
		"replaced",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("markdown %q missing %q", rendered, want)
		}
	}
}

// TestSetMCPServerDisabledFindsTheSessionsServer pins the one action both
// surfaces run: it finds the named server among the running and the kept-out
// entries, records the choice by scope, refuses a required server with its own
// sentence, and names an unknown one.
func TestSetMCPServerDisabledFindsTheSessionsServer(t *testing.T) {
	root := t.TempDir()
	running := []appcfg.MCPServerConfig{{Name: "codegraph"}, {Name: "must", Required: true}}
	kept := []appcfg.MCPServerConfig{{Name: "notion"}}
	reply, err := SetMCPServerDisabled(root, running, kept, "CodeGraph", true)
	if err != nil || reply != "codegraph: disabled from next session" {
		t.Fatalf("disable = %q, %v", reply, err)
	}
	next, err := mcpkg.DisabledForNextSession(root)
	if err != nil || !next(running[0]) {
		t.Fatalf("codegraph not marked: %v", err)
	}
	if reply, err = SetMCPServerDisabled(root, running, kept, "notion", false); err != nil || reply != "notion: enabled from next session" {
		t.Fatalf("enable = %q, %v", reply, err)
	}
	if _, err := SetMCPServerDisabled(root, running, kept, "must", true); err == nil || !strings.Contains(err.Error(), "required by config") {
		t.Fatalf("required server: %v", err)
	}
	if _, err := SetMCPServerDisabled(root, running, kept, "ghost", true); err == nil {
		t.Fatal("an unknown server must be refused")
	}
}

// TestStatusConfigFilesListsTheProjectMCPFile pins the Config files row: the
// files the configuration was loaded from, then the project MCP file when the
// project has one.
func TestStatusConfigFilesListsTheProjectMCPFile(t *testing.T) {
	proj := t.TempDir()
	cfg := &appcfg.Root{SourceFiles: []string{"/home/u/.forebrain/forebrain.yaml"}}
	if got := StatusConfigFiles(cfg, proj); len(got) != 1 {
		t.Fatalf("no project file: %v", got)
	}
	if err := os.MkdirAll(filepath.Join(proj, ".forebrain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".forebrain", "mcp_servers.yaml"), []byte("mcp_servers: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := StatusConfigFiles(cfg, proj)
	if len(got) != 2 || got[1] != filepath.Join(proj, ".forebrain", "mcp_servers.yaml") {
		t.Fatalf("config files = %v", got)
	}
}

// TestLSPServerStateLabel covers every state's wording, the install override,
// the indexing percentage's omission at zero, and the problem counts'
// singular/plural with each zero side omitted.
func TestLSPServerStateLabel(t *testing.T) {
	cases := []struct {
		name string
		s    event.LSPServerStatus
		want string
	}{
		{"ready", event.LSPServerStatus{State: event.LSPStateReady}, "✓ ready"},
		{"indexing without percent", event.LSPServerStatus{State: event.LSPStateIndexing}, "indexing…"},
		{"indexing with percent", event.LSPServerStatus{State: event.LSPStateIndexing, IndexingPercent: 42}, "indexing… 42%"},
		{"starting", event.LSPServerStatus{State: event.LSPStateStarting}, "starting…"},
		{"failed", event.LSPServerStatus{State: event.LSPStateFailed}, "✗ failed"},
		{"stopped", event.LSPServerStatus{State: event.LSPStateStopped}, "○ enabled, not running"},
		{"available", event.LSPServerStatus{State: event.LSPStateAvailable}, "○ available"},
		{"not installed", event.LSPServerStatus{State: event.LSPStateNotInstalled}, "– not installed"},
		{"blocked", event.LSPServerStatus{State: event.LSPStateBlocked}, "△ blocked"},
		{"installing hides the state", event.LSPServerStatus{State: event.LSPStateReady, Installing: true}, "installing…"},
		{
			"enabled with both problems",
			event.LSPServerStatus{State: event.LSPStateReady, Enabled: true, Errors: 2, Warnings: 1},
			"✓ ready · 2 errors, 1 warning",
		},
		{
			"enabled with errors only",
			event.LSPServerStatus{State: event.LSPStateReady, Enabled: true, Errors: 1},
			"✓ ready · 1 error",
		},
		{
			"enabled with warnings only",
			event.LSPServerStatus{State: event.LSPStateReady, Enabled: true, Warnings: 3},
			"✓ ready · 3 warnings",
		},
		{
			"disabled servers carry no problem counts",
			event.LSPServerStatus{State: event.LSPStateAvailable, Errors: 4, Warnings: 5},
			"○ available",
		},
	}
	for _, tc := range cases {
		if got := LSPServerStateLabel(tc.s); got != tc.want {
			t.Errorf("%s: label = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestLSPStatusLine covers the /status row: only enabled servers count, the
// fixed category order, and the empty answer when none is enabled.
func TestLSPStatusLine(t *testing.T) {
	snap := event.LSPSnapshot{Servers: []event.LSPServerStatus{
		{ID: "a", Enabled: true, State: event.LSPStateReady},
		{ID: "b", Enabled: true, State: event.LSPStateReady},
		{ID: "c", Enabled: true, State: event.LSPStateIndexing},
		{ID: "d", Enabled: true, State: event.LSPStateFailed},
		{ID: "e", Enabled: false, State: event.LSPStateReady}, // disabled: never counted
	}}
	if got := LSPStatusLine(snap); got != "2 running, 1 indexing, 1 failed" {
		t.Fatalf("line = %q", got)
	}
	if got := LSPStatusLine(event.LSPSnapshot{}); got != "" {
		t.Fatalf("no enabled server must render empty, got %q", got)
	}
}

// TestRenderLSPInventoryMarkdown pins the /lsp text reply's five shapes.
func TestRenderLSPInventoryMarkdown(t *testing.T) {
	base := event.LSPSnapshot{
		ProjectRoot:    "/Users/tester/proj",
		Trusted:        true,
		FeatureEnabled: true,
		Servers: []event.LSPServerStatus{
			{ID: "gopls", Enabled: true, State: event.LSPStateReady, Languages: []string{"Go"}, Errors: 1},
			{ID: "pyright", Enabled: false, State: event.LSPStateAvailable, Languages: []string{"Python"}},
		},
	}

	got := RenderLSPInventoryMarkdown(base)
	want := strings.Join([]string{
		"Language servers · 2 configured, 1 enabled",
		"Project: /Users/tester/proj",
		"",
		"Enabled",
		"- gopls · ✓ ready · 1 error · Go",
		"",
		"Available",
		"- pyright · ○ available · Python",
	}, "\n")
	if got != want {
		t.Fatalf("trusted project:\ngot:\n%s\nwant:\n%s", got, want)
	}

	untrusted := base
	untrusted.Trusted = false
	if got := RenderLSPInventoryMarkdown(untrusted); !strings.Contains(got, "Project: /Users/tester/proj (not trusted: language servers do not start here)") {
		t.Fatalf("untrusted project:\n%s", got)
	}

	noProject := base
	noProject.ProjectRoot = ""
	if got := RenderLSPInventoryMarkdown(noProject); !strings.Contains(got, "No project: language servers start only in trusted projects") {
		t.Fatalf("no project:\n%s", got)
	}

	off := base
	off.FeatureEnabled = false
	if got := RenderLSPInventoryMarkdown(off); !strings.Contains(got, "Turned off by features.lsp") {
		t.Fatalf("feature off:\n%s", got)
	}

	recs := base
	recs.RecommendationsDisabled = true
	recs.RecommendationsDisabledReason = "dismissed 5 times"
	recs.Servers[0].LastError = "gopls exited"
	got = RenderLSPInventoryMarkdown(recs)
	for _, want := range []string{"  Error: gopls exited", "Recommendations are off (dismissed 5 times); /lsp can turn them back on"} {
		if !strings.Contains(got, want) {
			t.Fatalf("recommendations off:\n%s\nmissing %q", got, want)
		}
	}

	if got := RenderLSPInventoryMarkdown(event.LSPSnapshot{Servers: []event.LSPServerStatus{}}); got != "No language servers configured" {
		t.Fatalf("empty = %q", got)
	}
}
