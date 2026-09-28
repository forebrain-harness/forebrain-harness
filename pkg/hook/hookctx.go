package hook

import "context"

type HookContext struct {
	SessionID string
	RunID     string
	Channel   string
	Trigger   string
	RunKind   string
	AgentID   string
	RawInput  string
}

type PreHookResult struct {
	Text           string
	SystemAddendum string
}

type AgentHook func(ctx context.Context, phase string, hc HookContext, text string) (PreHookResult, error)
