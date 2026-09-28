package hook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

func TestWriteSessionTranscriptArtifact(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	defer db.Close()
	sess := state.NewSessionStore(db, "main")
	_, _ = sess.Append(ctx, "s1", "user", "hello")
	_, _ = sess.Append(ctx, "s1", "assistant", "world")

	workspace := filepath.Join(home, "workspaces", "acme")
	path, err := WriteSessionTranscriptArtifact(workspace, sess, "s1")
	if err != nil {
		t.Fatalf("WriteSessionTranscriptArtifact: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	txt := string(raw)
	if txt == "" || !containsAll(txt, `"role":"user"`, `"content":"hello"`, `"role":"assistant"`, `"content":"world"`) {
		t.Fatalf("unexpected transcript artifact: %s", txt)
	}
}

func TestExecuteUserPromptSubmitCommandHookBlocks(t *testing.T) {
	home := t.TempDir()
	rt := &Runtime{
		Home: home,
		Cfg: &config.Root{
			Hooks: config.HooksSettings{
				EventUserPromptSubmit: {{
					Hooks: []config.HookCommand{{
						Type:    config.HookTypeCommand,
						Command: "echo '{\"continue\":false,\"stopReason\":\"blocked by test\"}'",
					}},
				}},
			},
		},
	}
	out, err := rt.ExecuteUserPromptSubmit(context.Background(), UserPromptSubmitInput{
		BaseInput: BaseInput{
			HookEventName:  EventUserPromptSubmit,
			SessionID:      "s1",
			TranscriptPath: filepath.Join(home, "hook.jsonl"),
			Cwd:            home,
		},
		Prompt: "hello",
	})
	if err != nil {
		t.Fatalf("ExecuteUserPromptSubmit: %v", err)
	}
	if !out.Blocked || out.StopReason != "blocked by test" {
		t.Fatalf("unexpected submit outcome: %+v", out)
	}
}

func TestExecutePreToolUseCommandHookReturnsDecisionAndUpdatedInput(t *testing.T) {
	home := t.TempDir()
	rt := &Runtime{
		Home: home,
		Cfg: &config.Root{
			Hooks: config.HooksSettings{
				EventPreToolUse: {{
					Matcher: "shell",
					Hooks: []config.HookCommand{{
						Type:    config.HookTypeCommand,
						Command: "echo '{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"allow\",\"permissionDecisionReason\":\"ok\",\"updatedInput\":{\"command\":\"echo changed\"},\"additionalContext\":\"ctx\"}}'",
					}},
				}},
			},
		},
	}
	out, err := rt.ExecutePreToolUse(context.Background(), PreToolUseInput{
		BaseInput: BaseInput{
			HookEventName:  EventPreToolUse,
			SessionID:      "s1",
			TranscriptPath: filepath.Join(home, "hook.jsonl"),
			Cwd:            home,
		},
		ToolName:  "shell",
		ToolUseID: "tool-1",
		ToolInput: map[string]any{"command": "echo hi"},
	})
	if err != nil {
		t.Fatalf("ExecutePreToolUse: %v", err)
	}
	if out.PermissionDecision != "" || out.PermissionDecisionReason != "" {
		t.Fatalf("hook allow must not authorize the tool: %+v", out)
	}
	if out.UpdatedInput["command"] != "echo changed" {
		t.Fatalf("unexpected updated input: %+v", out.UpdatedInput)
	}
	if out.AdditionalContext != "ctx" {
		t.Fatalf("unexpected additional context: %+v", out)
	}
}

func TestExecutePermissionRequestUsesDenyPrecedence(t *testing.T) {
	home := t.TempDir()
	rt := &Runtime{
		Home: home,
		Cfg: &config.Root{Hooks: config.HooksSettings{
			EventPermissionRequest: {
				{
					Matcher: "shell",
					Hooks: []config.HookCommand{
						{Type: config.HookTypeCommand, Command: `echo '{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}'`},
						{Type: config.HookTypeCommand, Command: `echo '{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"blocked target"}}}'`},
					},
				},
			},
		}},
	}
	out, err := rt.ExecutePermissionRequest(context.Background(), PermissionRequestInput{
		BaseInput: BaseInput{HookEventName: EventPermissionRequest, SessionID: "s1", Cwd: home},
		TurnID:    "turn-1", Model: "model", ToolName: "shell", ToolInput: map[string]any{"command": "curl example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != "deny" || out.Message != "blocked target" {
		t.Fatalf("outcome=%+v", out)
	}
}

func TestExecutePermissionRequestIgnoresUnsupportedOutput(t *testing.T) {
	home := t.TempDir()
	rt := &Runtime{Home: home, Cfg: &config.Root{Hooks: config.HooksSettings{
		EventPermissionRequest: {{Matcher: "shell", Hooks: []config.HookCommand{{
			Type:    config.HookTypeCommand,
			Command: `echo '{"continue":false,"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}'`,
		}}}},
	}}}
	out, err := rt.ExecutePermissionRequest(context.Background(), PermissionRequestInput{
		BaseInput: BaseInput{HookEventName: EventPermissionRequest, SessionID: "s1", Cwd: home},
		ToolName:  "shell", ToolInput: map[string]any{"command": "echo ok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != "" {
		t.Fatalf("unsupported output must not decide: %+v", out)
	}
}

func TestExecutePermissionRequestExitTwoDeniesWithStderr(t *testing.T) {
	home := t.TempDir()
	rt := &Runtime{Home: home, Cfg: &config.Root{Hooks: config.HooksSettings{
		EventPermissionRequest: {{Matcher: "shell", Hooks: []config.HookCommand{{
			Type: config.HookTypeCommand, Command: `echo 'policy rejected' >&2; exit 2`,
		}}}},
	}}}
	out, err := rt.ExecutePermissionRequest(context.Background(), PermissionRequestInput{
		BaseInput: BaseInput{HookEventName: EventPermissionRequest, SessionID: "s1", Cwd: home},
		ToolName:  "shell", ToolInput: map[string]any{"command": "echo ok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != "deny" || out.Message != "policy rejected" {
		t.Fatalf("outcome=%+v", out)
	}
}

func TestExecutePermissionRequestRejectsInvalidDecisionTypesAndCasing(t *testing.T) {
	for _, output := range []string{
		`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"ALLOW"}}}`,
		`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":42}}}`,
		`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow","interrupt":"false"}}}`,
	} {
		home := t.TempDir()
		command := `printf '%s\n' '` + output + `'`
		rt := &Runtime{Home: home, Cfg: &config.Root{Hooks: config.HooksSettings{
			EventPermissionRequest: {{Matcher: "Bash", Hooks: []config.HookCommand{{Type: config.HookTypeCommand, Command: command}}}},
		}}}
		out, err := rt.ExecutePermissionRequest(context.Background(), PermissionRequestInput{
			BaseInput: BaseInput{HookEventName: EventPermissionRequest, SessionID: "s1", Cwd: home},
			ToolName:  "Bash", ToolInput: map[string]any{"command": "echo ok"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if out.Decision != "" {
			t.Fatalf("output=%s outcome=%+v", output, out)
		}
	}
}

func TestExecutePermissionRequestMatchesApplyPatchAliases(t *testing.T) {
	home := t.TempDir()
	rt := &Runtime{Home: home, Cfg: &config.Root{Hooks: config.HooksSettings{
		EventPermissionRequest: {{Matcher: "Write", Hooks: []config.HookCommand{{
			Type:    config.HookTypeCommand,
			Command: `echo '{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"no write"}}}'`,
		}}}},
	}}}
	out, err := rt.ExecutePermissionRequest(context.Background(), PermissionRequestInput{
		BaseInput: BaseInput{HookEventName: EventPermissionRequest, SessionID: "s1", Cwd: home},
		ToolName:  "apply_patch", MatcherAliases: []string{"Write", "Edit"}, ToolInput: map[string]any{"command": "patch"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision != "deny" || out.Message != "no write" {
		t.Fatalf("outcome=%+v", out)
	}
}

func TestPermissionRequestInputUsesNullTranscriptPath(t *testing.T) {
	raw, err := json.Marshal(PermissionRequestInput{BaseInput: BaseInput{HookEventName: EventPermissionRequest}, ToolInput: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if value, exists := payload["transcript_path"]; !exists || value != nil {
		t.Fatalf("payload=%s", raw)
	}
}

func TestToolMiddlewareBlocksRealToolExecution(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	defer db.Close()
	sess := state.NewSessionStore(db, "main")
	rt := &Runtime{
		Home: home,
		Sess: sess,
		Cfg: &config.Root{
			Hooks: config.HooksSettings{
				EventPreToolUse: {{
					Matcher: "demo_tool",
					Hooks: []config.HookCommand{{
						Type:    config.HookTypeCommand,
						Command: "echo '{\"continue\":false,\"stopReason\":\"stop tool\"}'",
					}},
				}},
			},
		},
	}
	testTool, err := llm.NewTool("demo_tool", "demo", func(context.Context, *struct{}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	testTool.Use(NewToolMiddleware(ToolMiddlewareOptions{Runtime: rt}))
	callCtx := llm.WithAgentSessionID(context.Background(), "s1")
	if _, err := testTool.Handle(callCtx, `{}`); err == nil || !strings.Contains(err.Error(), "stop tool") {
		t.Fatalf("expected blocked tool execution, got err=%v", err)
	}
}

func TestToolMiddlewarePreToolAskDoesNotAuthorizeOrRequestApproval(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	defer db.Close()
	sess := state.NewSessionStore(db, "main")
	rt := &Runtime{
		Home: home,
		Sess: sess,
		Cfg: &config.Root{
			Hooks: config.HooksSettings{
				EventPreToolUse: {{
					Matcher: "demo_tool",
					Hooks: []config.HookCommand{{
						Type:    config.HookTypeCommand,
						Command: "echo '{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"ask\",\"permissionDecisionReason\":\"need approval\"}}'",
					}},
				}},
			},
		},
	}
	testTool, err := llm.NewTool("demo_tool", "demo", func(context.Context, *struct{}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	testTool.Use(NewToolMiddleware(ToolMiddlewareOptions{Runtime: rt}))
	callCtx := llm.WithAgentSessionID(context.Background(), "s1")
	result, err := testTool.Handle(callCtx, `{}`)
	if err != nil || result != "ok" {
		t.Fatalf("hook ask is unsupported and must leave approval to policy: result=%v err=%v", result, err)
	}
}

func TestToolMiddlewarePreToolAllowContinuesWithoutAuthorizing(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	defer db.Close()
	sess := state.NewSessionStore(db, "main")
	rt := &Runtime{
		Home: home,
		Sess: sess,
		Cfg: &config.Root{
			Hooks: config.HooksSettings{
				EventPreToolUse: {{
					Matcher: "demo_tool",
					Hooks: []config.HookCommand{{
						Type:    config.HookTypeCommand,
						Command: "echo '{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"allow\",\"permissionDecisionReason\":\"hook approves\"}}'",
					}},
				}},
			},
		},
	}
	executed := false
	testTool, err := llm.NewTool("demo_tool", "demo", func(context.Context, *struct{}) (string, error) {
		executed = true
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	testTool.Use(NewToolMiddleware(ToolMiddlewareOptions{Runtime: rt}))
	callCtx := llm.WithAgentSessionID(context.Background(), "s1")
	if _, err := testTool.Handle(callCtx, `{}`); err != nil {
		t.Fatalf("hook allow must not error: %v", err)
	}
	if !executed {
		t.Fatal("hook allow should continue to the normal permission policy")
	}
}

func TestToolMiddlewarePreToolDenyClassifiesAsPermissionDenied(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	defer db.Close()
	sess := state.NewSessionStore(db, "main")
	rt := &Runtime{
		Home: home,
		Sess: sess,
		Cfg: &config.Root{
			Hooks: config.HooksSettings{
				EventPreToolUse: {{
					Matcher: "demo_tool",
					Hooks: []config.HookCommand{{
						Type:    config.HookTypeCommand,
						Command: "echo '{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"deny\",\"permissionDecisionReason\":\"hook says no\"}}'",
					}},
				}},
			},
		},
	}
	testTool, err := llm.NewTool("demo_tool", "demo", func(context.Context, *struct{}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	testTool.Use(NewToolMiddleware(ToolMiddlewareOptions{Runtime: rt}))
	callCtx := llm.WithAgentSessionID(context.Background(), "s1")
	_, err = testTool.Handle(callCtx, `{}`)
	if err == nil {
		t.Fatal("expected deny error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "permission denied") {
		t.Fatalf("error should include 'permission denied' for classification, got %q", msg)
	}
	if !strings.Contains(msg, "hook says no") {
		t.Fatalf("error should include hook reason, got %q", msg)
	}
}

func TestToolMiddlewarePassesActualPermissionMode(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db, err := state.OpenStateForTest(ctx, filepath.Join(home, "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	defer db.Close()
	sess := state.NewSessionStore(db, "main")
	rt := &Runtime{
		Home: home,
		Sess: sess,
		Cfg: &config.Root{
			Hooks: config.HooksSettings{
				EventPreToolUse: {{
					Matcher: "demo_tool",
					Hooks: []config.HookCommand{{
						Type:    config.HookTypeCommand,
						Command: "cat - | jq -r '.permission_mode' | xargs -I{} echo '{\"continue\":false,\"stopReason\":\"mode={}\"}'",
					}},
				}},
			},
		},
		PermissionMode: func() string { return "plan" },
	}
	testTool, err := llm.NewTool("demo_tool", "demo", func(context.Context, *struct{}) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	testTool.Use(NewToolMiddleware(ToolMiddlewareOptions{Runtime: rt}))
	callCtx := llm.WithAgentSessionID(context.Background(), "s1")
	_, err = testTool.Handle(callCtx, `{}`)
	if err == nil {
		t.Fatal("expected hook to block tool")
	}
	if !strings.Contains(err.Error(), "mode=plan") {
		t.Fatalf("hook should have observed permission_mode=plan, got %q", err.Error())
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
