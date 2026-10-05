// One-shot and batch execution entry points.
package process

import (
	"context"

	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
)

type BatchReporter interface {
	OnWorkItemTerminal(ctx context.Context, runID, workItemID, sessionID, channelID string, failed bool, errMsg string)
}

func (env *Environment) RunSubagentExec(ctx context.Context, task string, superviseExistingRunID string, parentRunID string, sessionID string, workerSessionID string, subagentType string) (string, error) {
	res, _, err := RunSubagentSupervised(ctx, env, task, superviseExistingRunID, parentRunID, sessionID, workerSessionID, subagentType)
	if err != nil {
		return "", err
	}
	if res == nil {
		return "", nil
	}
	return res.TextContent(), nil
}

func (env *Environment) AssembleContextSnapshot(hc hook.HookContext, text string) (assembly.AssemblyResult, bool, error) {
	if env == nil || env.ctxHook == nil {
		return assembly.AssemblyResult{}, false, nil
	}
	return env.ctxHook.Assemble(hc, text)
}
