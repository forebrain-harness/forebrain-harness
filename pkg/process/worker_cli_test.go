package process

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// mcpServerEntry is a real child process that starts and never answers a
// JSON-RPC handshake.
//
// That is the shape of the problem the required-server check exists for: the
// server is present and running, so it is nothing like a missing binary, and the
// startup ends on its own deadline. A sleeping process is used rather than a fake
// because the contract under test is about a real child that never replies — and
// because the deadline path is also the path that has to reclaim it.
func mcpServerEntry(name string, required bool) string {
	entry := "    mcp_servers:\n" +
		"      - name: " + name + "\n" +
		"        transport: stdio\n" +
		"        command: /bin/sleep\n" +
		"        args: [\"30\"]\n"
	if required {
		entry += "        required: true\n"
	}
	return entry + "        startup_timeout: 0.4\n"
}

// countingLLM counts provider requests, so a test can assert that a
// required-server failure stops the run before the model is asked anything.
type countingLLM struct {
	mu    sync.Mutex
	calls int
}

func (c *countingLLM) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *countingLLM) add() {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
}

// envWithMCPServers opens a real environment around a fake provider, with the
// given MCP section in its configuration.
func envWithMCPServers(t *testing.T, mcpSection string) (*Environment, *countingLLM) {
	t.Helper()
	t.Setenv("FOREBRAIN_REQUIRED_MCP_KEY", "test-key")
	counter := &countingLLM{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counter.add()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "gpt-test",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "the model answered"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
		})
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	cfg := "features:\n" +
		"  memories: false\n" +
		"agents:\n" +
		"  defaults:\n" +
		mcpSection +
		"  definitions:\n" +
		"    main:\n" +
		"      llm_providers:\n" +
		"        - provider: openai\n" +
		"          model: gpt-test\n" +
		"          api_key: ${FOREBRAIN_REQUIRED_MCP_KEY}\n" +
		"          base_url: " + srv.URL + "/v1\n" +
		"          params:\n" +
		"            stream: false\n"
	if err := os.WriteFile(filepath.Join(home, "forebrain.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := Open(context.Background(), OpenOptions{
		Home:          home,
		ConfigPath:    filepath.Join(home, "forebrain.yaml"),
		LaunchDir:     t.TempDir(),
		SessionSource: "webchat",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(env.Close)
	return env, counter
}

// TestRunExecutorFailsBeforeTheModelWhenARequiredServerDidNotStart pins the
// unattended-run contract at the entry point the gateway uses.
//
// The run must stop at the barrier: an unattended session has nobody watching a
// status line, so proceeding without the tools the configuration requires would
// produce confident work that is missing its inputs. The error names the server
// and keeps the underlying text, and the provider is never asked.
func TestRunExecutorFailsBeforeTheModelWhenARequiredServerDidNotStart(t *testing.T) {
	env, counter := envWithMCPServers(t, mcpServerEntry("required-docs", true))

	executor := env.NewRunExecutor("webchat", RunExecutorOptions{FailOnRequiredMCP: true})
	_, err := executor.Run(context.Background(), turn.TurnRequest{SessionID: "s-required", UserText: "hello"}, func(string) {})
	if err == nil {
		t.Fatal("a run whose required MCP server never started must fail")
	}
	var aggregate *run.MCPRequiredStartupError
	if !errors.As(err, &aggregate) {
		t.Fatalf("error = %T %v, want the aggregate required-startup error", err, err)
	}
	if len(aggregate.Failures) != 1 || aggregate.Failures[0].Server != "required-docs" {
		t.Fatalf("failures = %+v, want the required server named", aggregate.Failures)
	}
	if !strings.Contains(aggregate.Failures[0].Error, "context deadline exceeded") {
		t.Fatalf("failure text = %q, want the deadline the runtime hit", aggregate.Failures[0].Error)
	}
	if counter.count() != 0 {
		t.Fatalf("the provider was called %d times; a required-server failure must stop before the model", counter.count())
	}
}

// TestRunExecutorRunsWhenOnlyAnOptionalServerFailed pins the other half of the
// contract: only what the configuration requires is fatal, so a server the run
// can do without still leaves a working session.
func TestRunExecutorRunsWhenOnlyAnOptionalServerFailed(t *testing.T) {
	env, counter := envWithMCPServers(t, mcpServerEntry("optional-docs", false))

	executor := env.NewRunExecutor("webchat", RunExecutorOptions{FailOnRequiredMCP: true})
	if err := env.Deps.SessionStore.Ensure(context.Background(), "s-optional", "s-optional"); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Run(context.Background(), turn.TurnRequest{SessionID: "s-optional", UserText: "hello"}, func(string) {}); err != nil {
		t.Fatalf("an optional server that failed must not stop the run: %v", err)
	}
	if counter.count() == 0 {
		t.Fatal("the model was never asked, so the run did not actually proceed")
	}
}

// TestRunAgentOnceSupervisedFailsWhenARequiredServerDidNotStart pins the same
// contract for the unattended entries a schedule or a one-shot prompt uses.
func TestRunAgentOnceSupervisedFailsWhenARequiredServerDidNotStart(t *testing.T) {
	env, counter := envWithMCPServers(t, mcpServerEntry("required-docs", true))

	_, _, err := RunAgentOnceSupervised(context.Background(), env, AgentOnceInput{
		SessionID: "cron-session",
		Input:     "nightly summary",
	})
	if err == nil {
		t.Fatal("an unattended one-shot run must fail when a required server did not start")
	}
	if !strings.Contains(err.Error(), "required-docs") {
		t.Fatalf("error %q does not name the required server", err)
	}
	// RunAgentOnceExec is the path a schedule reaches, and it reports the same
	// failure as a message rather than an error.
	_, errMsg := env.RunAgentOnceExec(context.Background(), "cron-session", "cron", "", "nightly summary")
	if !strings.Contains(errMsg, "required-docs") {
		t.Fatalf("RunAgentOnceExec error %q does not name the required server", errMsg)
	}
	if counter.count() != 0 {
		t.Fatalf("the provider was called %d times for unattended runs that had to stop", counter.count())
	}
}

// TestSubagentFailsWhenThePrimaryRunnersRequiredServerDidNotStart pins that a
// child cannot quietly outlive a required-server failure: a subagent reuses the
// primary Runner's generation, so it meets the same barrier.
func TestSubagentFailsWhenThePrimaryRunnersRequiredServerDidNotStart(t *testing.T) {
	env, counter := envWithMCPServers(t, mcpServerEntry("required-docs", true))

	_, _, err := RunSubagentSupervised(context.Background(), env, "do the thing", "", "", "s-1", "worker-1", "")
	if err == nil {
		t.Fatal("a subagent must not run on a generation whose required server failed")
	}
	if !strings.Contains(err.Error(), "required-docs") {
		t.Fatalf("error %q does not name the required server", err)
	}
	if counter.count() != 0 {
		t.Fatalf("the provider was called %d times for a child that had to stop", counter.count())
	}
}

// TestScheduledPromptRunsInTheFreshSessionItNames pins that an unattended run
// in a session nobody has opened yet — every cron fire gets a fresh one —
// creates that session for the bound primary agent before the run row that
// belongs to it, instead of failing the run row's reference to it.
func TestScheduledPromptRunsInTheFreshSessionItNames(t *testing.T) {
	env, counter := envWithMCPServers(t, "")
	out, errMsg := env.RunAgentOnceExec(context.Background(), "cron-job-1-1790000000", "cron", "", "nightly summary")
	if errMsg != "" {
		t.Fatalf("scheduled prompt failed: %s", errMsg)
	}
	if out != "the model answered" || counter.count() == 0 {
		t.Fatalf("scheduled prompt output = %q after %d model calls", out, counter.count())
	}
	var owner string
	if err := env.SQL.QueryRowContext(context.Background(), `SELECT agent_id FROM fb_sessions WHERE id=?`, "cron-job-1-1790000000").Scan(&owner); err != nil {
		t.Fatalf("the fire's session was not recorded: %v", err)
	}
	if owner != env.Deps.SessionStore.AgentID() {
		t.Fatalf("fire session owner = %q, want the bound primary agent %q", owner, env.Deps.SessionStore.AgentID())
	}
}
