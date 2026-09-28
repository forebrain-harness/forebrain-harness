package hook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

type ToolMiddlewareOptions struct {
	Runtime *Runtime
}

func NewToolMiddleware(opts ToolMiddlewareOptions) llm.ToolMiddleware {
	rt := opts.Runtime
	return func(tool *llm.Tool, next llm.ToolHandler) llm.ToolHandler {
		return func(ctx context.Context, arguments string) (any, error) {
			if rt == nil || tool == nil {
				return next(ctx, arguments)
			}
			if inHookExecution(ctx) {
				return next(ctx, arguments)
			}
			toolName := strings.TrimSpace(tool.Name())
			sessionID := runtimeSessionID(rt, ctx)
			if sessionID == "" {
				sessionID = "default"
			}
			transcriptPath, err := WriteSessionTranscriptArtifact(rt.StateRoot(), rt.Sess, sessionID)
			if err != nil {
				return nil, err
			}
			inputMap := map[string]any{}
			if strings.TrimSpace(arguments) != "" {
				if err := json.Unmarshal([]byte(arguments), &inputMap); err != nil {
					inputMap = map[string]any{"raw": arguments}
				}
			}
			toolUseID := runtimeToolUseID(rt, ctx)
			if toolUseID == "" {
				toolUseID = "tool-" + sanitizePathSegment(toolName)
			}
			hookCtx := withHookExecution(ctx)
			permMode := currentPermissionMode(rt)
			pre, err := rt.ExecutePreToolUse(hookCtx, PreToolUseInput{
				BaseInput: BaseInput{
					HookEventName:  EventPreToolUse,
					SessionID:      sessionID,
					TranscriptPath: transcriptPath,
					Cwd:            runtimeCWD(rt.Home),
					PermissionMode: permMode,
				},
				ToolName:  toolName,
				ToolUseID: toolUseID,
				ToolInput: inputMap,
			})
			if err != nil {
				return nil, err
			}
			if pre.Blocked {
				return nil, fmt.Errorf("permission denied by hook: %s", strings.TrimSpace(pre.StopReason))
			}
			if len(pre.UpdatedInput) > 0 {
				inputMap = pre.UpdatedInput
				if b, err := json.Marshal(inputMap); err == nil {
					arguments = string(b)
				}
			}
			out, err := next(ctx, arguments)
			if err != nil {
				_, hookErr := rt.ExecutePostToolUseFailure(hookCtx, PostToolUseFailureInput{
					BaseInput: BaseInput{
						HookEventName:  EventPostToolUseFailure,
						SessionID:      sessionID,
						TranscriptPath: transcriptPath,
						Cwd:            runtimeCWD(rt.Home),
						PermissionMode: permMode,
					},
					ToolName:  toolName,
					ToolUseID: toolUseID,
					ToolInput: inputMap,
					Error:     err.Error(),
				})
				if hookErr != nil {
					return nil, hookErr
				}
				return nil, err
			}
			post, err := rt.ExecutePostToolUse(hookCtx, PostToolUseInput{
				BaseInput: BaseInput{
					HookEventName:  EventPostToolUse,
					SessionID:      sessionID,
					TranscriptPath: transcriptPath,
					Cwd:            runtimeCWD(rt.Home),
					PermissionMode: permMode,
				},
				ToolName:     toolName,
				ToolUseID:    toolUseID,
				ToolInput:    inputMap,
				ToolResponse: out,
			})
			if err != nil {
				return nil, err
			}
			if post.Blocked {
				return nil, errors.New(strings.TrimSpace(post.StopReason))
			}
			if post.UpdatedMCPToolOutput != nil {
				return post.UpdatedMCPToolOutput, nil
			}
			return out, nil
		}
	}
}

func runtimeSessionID(rt *Runtime, ctx context.Context) string {
	if rt == nil || rt.SessionID == nil {
		return ""
	}
	return strings.TrimSpace(rt.SessionID(ctx))
}

func runtimeToolUseID(rt *Runtime, ctx context.Context) string {
	if rt == nil || rt.ToolUseID == nil {
		return ""
	}
	return strings.TrimSpace(rt.ToolUseID(ctx))
}

func runtimeCWD(home string) string {
	if abs, err := filepath.Abs("."); err == nil && strings.TrimSpace(abs) != "" {
		return abs
	}
	return strings.TrimSpace(home)
}

func currentPermissionMode(rt *Runtime) string {
	if rt == nil || rt.PermissionMode == nil {
		return "on-request"
	}
	mode := strings.TrimSpace(rt.PermissionMode())
	if mode == "" {
		return "on-request"
	}
	return mode
}
