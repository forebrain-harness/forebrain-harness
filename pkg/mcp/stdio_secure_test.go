package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

func TestMCPStdioEnvStripsSecretLikeOverrides(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "outer-secret")
	t.Setenv("SAFE_VISIBLE", "from-parent")
	inherit := true
	env := mcpStdioEnv("", appcfg.MCPServerConfig{
		InheritParentEnv: &inherit,
		Env: map[string]string{
			"SAFE_VISIBLE":     "override-visible",
			"OPENAI_API_KEY":   "${OPENAI_API_KEY}",
			"FOREBRAIN_RUN_ID": "run-1",
			"_INTERNAL_TOKEN":  "forbidden",
		},
	})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "OPENAI_API_KEY=outer-secret") {
		t.Fatalf("secret OPENAI_API_KEY leaked into MCP env: %s", joined)
	}
	if strings.Contains(joined, "_INTERNAL_TOKEN=forbidden") {
		t.Fatalf("secret-like override leaked into MCP env: %s", joined)
	}
	if !strings.Contains(joined, "SAFE_VISIBLE=override-visible") {
		t.Fatalf("expected safe explicit env override: %s", joined)
	}
	if !strings.Contains(joined, "FOREBRAIN_RUN_ID=run-1") {
		t.Fatalf("expected safe runtime env override: %s", joined)
	}
}

func TestStartStdioConnectsRealMCPServerProcess(t *testing.T) {
	exe := buildMCPFixture(t)
	workspaceDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := Start(ctx, t.TempDir(), workspaceDir, appcfg.MCPServerConfig{
		Name:      "fixture",
		Transport: "stdio",
		Command:   exe,
	})
	if err != nil {
		t.Fatalf("Start stdio fixture: %v", err)
	}
	defer sess.Close()

	tools, err := sess.ListToolMetas(ctx)
	if err != nil {
		t.Fatalf("ListToolMetas: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("tools=%v", tools)
	}
	toolNames := map[string]bool{}
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		toolNames[name] = true
	}
	for _, want := range []string{"echo", "pwd", "roots"} {
		if !toolNames[want] {
			t.Fatalf("missing tool %q in %v", want, tools)
		}
	}

	out, err := sess.CallToolJSON(ctx, "echo", json.RawMessage(`{"text":"pong"}`))
	if err != nil {
		t.Fatalf("CallToolJSON: %v", err)
	}
	if !strings.Contains(out, "pong") {
		t.Fatalf("tool output=%s", out)
	}

	resources, err := sess.ListResourcesJSON(ctx)
	if err != nil {
		t.Fatalf("ListResourcesJSON: %v", err)
	}
	if !strings.Contains(resources, "file:///fixture/readme.md") {
		t.Fatalf("resources=%s", resources)
	}

	pwdOut, err := sess.CallToolJSON(ctx, "pwd", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CallToolJSON pwd: %v", err)
	}
	if !strings.Contains(pwdOut, workspaceDir) {
		t.Fatalf("pwd output=%s want cwd=%s", pwdOut, workspaceDir)
	}

	rootsOut, err := sess.CallToolJSON(ctx, "roots", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CallToolJSON roots: %v", err)
	}
	wantRootURI := uriFromPath(workspaceDir)
	if !strings.Contains(rootsOut, wantRootURI) {
		t.Fatalf("roots output=%s want root=%s", rootsOut, wantRootURI)
	}
}

func TestStartStdioConnectsInstalledCodeReviewGraph(t *testing.T) {
	exe, err := exec.LookPath("code-review-graph")
	if err != nil {
		t.Skip("code-review-graph not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess, err := Start(ctx, t.TempDir(), repoRootForTest(t), appcfg.MCPServerConfig{
		Name:      "code-review-graph",
		Transport: "stdio",
		Command:   exe,
		Args:      []string{"serve", "--repo", repoRootForTest(t)},
	})
	if err != nil {
		t.Fatalf("Start code-review-graph: %v", err)
	}
	defer sess.Close()
	tools, err := sess.ListToolMetas(ctx)
	if err != nil {
		t.Fatalf("ListToolMetas: %v", err)
	}
	if len(tools) == 0 {
		t.Fatalf("code-review-graph exposed no tools")
	}
	found := false
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		if name == "get_minimal_context_tool" || name == "list_graph_stats_tool" || strings.Contains(name, "graph") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("unexpected code-review-graph tools: %v", tools)
	}
}

func buildMCPFixture(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	mainPath := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(mainPath, []byte(mcpFixtureSource), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	exe := filepath.Join(tmp, "mcp-fixture")
	cmd := exec.Command("go", "build", "-o", exe, mainPath)
	cmd.Dir = repoRootForTest(t)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, out)
	}
	return exe
}

func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found")
		}
		dir = parent
	}
}

const mcpFixtureSource = `package main

import (
	"context"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoInput struct {
	Text string ` + "`json:\"text\"`" + `
}

type echoOutput struct {
	Text string ` + "`json:\"text\"`" + `
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echo text"}, func(ctx context.Context, req *mcp.CallToolRequest, in echoInput) (*mcp.CallToolResult, echoOutput, error) {
		return nil, echoOutput{Text: in.Text}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "pwd", Description: "report current working directory"}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, echoOutput, error) {
		wd, err := os.Getwd()
		if err != nil {
			return nil, echoOutput{}, err
		}
		return nil, echoOutput{Text: wd}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "roots", Description: "report client roots"}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
		res, err := req.Session.ListRoots(ctx, nil)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"roots": res.Roots}, nil
	})
	server.AddResource(&mcp.Resource{URI: "file:///fixture/readme.md", Name: "readme"}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: "hello"}}}, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		panic(err)
	}
}
`
