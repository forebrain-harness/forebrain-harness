package gateway

import (
	"context"
	"fmt"

	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

func (s *Server) notifyRuntimeHook(ctx context.Context, req tool.NotificationRequest) {
	if s == nil || s.NotificationHook == nil {
		return
	}
	_ = tool.NotifyHook(ctx, s.NotificationHook, req)
	if s.Sessions == nil || s.Runner == nil {
		return
	}
	contractInput, ok := tool.BuildNotificationHookInput(req)
	if !ok {
		return
	}
	transcriptPath, err := hook.WriteSessionTranscriptArtifact(s.stateRoot(), s.Sessions, req.SessionID)
	if err != nil {
		return
	}
	rt := &hook.Runtime{
		Home:          s.Home,
		WorkspaceRoot: s.stateRoot(),
		Cfg:           s.liveCfg(),
		Sess:          s.Sessions,
		NewPromptRunner: func(label string) (hook.PromptRun, error) {
			fac := run.Factory{
				Home:          s.Home,
				AgentName:     s.Runner.AgentName,
				WorkspaceRoot: s.Runner.StateRoot(),
				ProjectRoot:   s.Runner.ProjectRoot,
				MemoryStore:   s.MemoryStore,
				AppCfg:        s.liveCfg(),
			}
			rr := fac.NewIsolatedRunner(label)
			if rr == nil {
				return nil, fmt.Errorf("nil isolated runner")
			}
			if err := rr.Load(); err != nil {
				return nil, err
			}
			return func(ctx context.Context, input string) (string, error) {
				return run.RunText(rr, ctx, input)
			}, nil
		},
		RunAgentHook: s.Runner.RunAgentHook,
		SessionID:    llm.AgentSessionIDFromContext,
		ToolUseID:    tool.ToolUseIDFromContext,
	}
	_, _ = rt.ExecuteNotification(ctx, hook.NotificationInput{
		BaseInput: hook.BaseInput{
			HookEventName:  hook.EventNotification,
			SessionID:      req.SessionID,
			TranscriptPath: transcriptPath,
			Cwd:            s.Home,
		},
		Message:          contractInput.Message,
		Title:            contractInput.Title,
		NotificationType: contractInput.NotificationType,
	})
}
