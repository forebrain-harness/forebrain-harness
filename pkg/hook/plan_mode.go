package hook

import (
	"context"
	"path/filepath"
	"strings"
)

type PlanModeLoader struct {
	// StateRoot is the per-agent state root (a workspace root) onto which the
	// mode store joins "state". Must match the root every other per-agent seam
	// resolves so the mode this hook reads is the one the agent actually set.
	StateRoot string
}

// NewPlanModeForWorkspace builds a plan-mode hook reading mode from the given agent
// workspace root. Per-agent callers pass runner.WorkspaceRoot so a non-main
// agent reads its own isolated mode.
func NewPlanModeForWorkspace(home, workspaceRoot string) *PlanModeLoader {
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		if h := strings.TrimSpace(home); h != "" {
			root = filepath.Join(h, "workspace")
		}
	}
	return &PlanModeLoader{StateRoot: root}
}

func (l *PlanModeLoader) Hook() AgentHook {
	return l.run
}

func (l *PlanModeLoader) run(ctx context.Context, phase string, hc HookContext, text string) (PreHookResult, error) {
	out := PreHookResult{Text: text}
	return out, nil
}
