// One-shot and batch execution entry points.
package process

import (
	"context"
	"log/slog"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

type AgentOnceInput struct {
	Input       string `json:"input"`
	SessionID   string `json:"session_id"`
	ChannelID   string `json:"channel_id"`
	ParentRunID string `json:"parent_run_id"`
}

type AgentOnceResult struct {
	Content string `json:"content"`
	Error   string `json:"error,omitempty"`
}

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

func (env *Environment) RunAgentOnceExec(ctx context.Context, sessionID string, channelID string, parentRunID string, input string) (string, string) {
	res, _, runErr := RunAgentOnceSupervised(ctx, env, AgentOnceInput{
		Input:       strings.TrimSpace(input),
		SessionID:   sessionID,
		ChannelID:   channelID,
		ParentRunID: parentRunID,
	})
	if runErr != nil {
		// This text is read by a person -- it lands in the cron run record and
		// in whatever a channel delivers back -- so a provider refusal is
		// described rather than dumped. The raw error stays in the log.
		slog.Error("agent one-shot turn failed", "session_id", sessionID, "channel_id", channelID, "err", runErr)
		return "", llm.ExplainError(runErr)
	}
	if res != nil {
		return res.TextContent(), ""
	}
	return "", ""
}

func (env *Environment) AssembleContextSnapshot(hc hook.HookContext, text string) (assembly.AssemblyResult, bool, error) {
	if env == nil || env.ctxHook == nil {
		return assembly.AssemblyResult{}, false, nil
	}
	return env.ctxHook.Assemble(hc, text)
}
