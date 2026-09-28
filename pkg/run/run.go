// Running a turn: options, outcome, result, and params.
package run

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/google/uuid"
)

type RunParams struct {
	LLM            llm.LLM
	CacheSafe      *CacheSafeParams
	PromptMessages []llm.Message
	UserPrompt     string
	CanUseTool     func(string) bool
	// Tools is the dispatching runtime's tool state, whose middlewares the
	// fork's tools run in.
	Tools         *tool.State
	Overrides     *RunOverrides
	AgentBaseName string
	AgentType     string
	AgentID       string
	// WorkspaceRoot is the owning agent's workspace directory, under which the
	// fork's sidechain log is written. It is deliberately not the forebrain home:
	// the log holds this agent's conversation verbatim.
	WorkspaceRoot string
	SessionID     string
	ParentRunID   string
	ForkLabel     string
	QuerySource   string
	RegisterTools func(*ToolRegistry) error
}

func RunFork(ctx context.Context, p RunParams) (*RunOutcome, error) {
	if err := validateRunParams(p); err != nil {
		return nil, err
	}
	promptMessages := cloneMessages(p.PromptMessages)
	if len(promptMessages) == 0 && strings.TrimSpace(p.UserPrompt) != "" {
		promptMessages = []llm.Message{llm.UserMessage(llm.Text(strings.TrimSpace(p.UserPrompt)))}
	}
	subctx, err := CreateSubagentContext(CreateSubagentContextParams{
		CacheSafe:      p.CacheSafe,
		PromptMessages: promptMessages,
		Overrides:      p.Overrides,
	})
	if err != nil {
		return nil, err
	}
	runKey := uuid.NewString()
	agentID := strings.TrimSpace(p.AgentID)
	if agentID == "" {
		agentID = uuid.NewString()
	}
	agentType := strings.TrimSpace(p.AgentType)
	if agentType == "" {
		agentType = strings.TrimSpace(p.AgentBaseName)
	}
	if agentType == "" {
		agentType = "forked_subagent"
	}
	sidechainKey := runKey
	if strings.TrimSpace(p.ForkLabel) == "subagent" && agentID != "" {
		sidechainKey = agentID
	}
	path := SidechainFilePath(p.WorkspaceRoot, p.SessionID, p.ForkLabel, sidechainKey)
	if len(subctx.InitialMessages) > 0 {
		if err := appendSidechainMessages(path, p.ForkLabel, p.QuerySource, p.SessionID, subctx.InitialMessages); err != nil {
			return nil, err
		}
	}
	name := forkAgentName(p.AgentBaseName, p.ForkLabel)
	a, err := agent.New(p.LLM, name, subctx.SystemPrompt)
	if err != nil {
		return nil, err
	}
	a.SetSessionBuilder(func(ctx context.Context, parts []llm.ContentPart) ([]llm.Message, int, error) {
		messages := cloneMessages(subctx.InitialMessages)
		sessionIndex := len(messages)
		if len(parts) > 0 {
			messages = append(messages, llm.UserMessage(parts...))
		}
		return messages, sessionIndex, nil
	})
	if err := p.RegisterTools(NewToolRegistry(a, p.CanUseTool, p.Tools)); err != nil {
		return nil, err
	}
	t0 := time.Now()
	slog.Info("fork_agent_query_start",
		"agent_id", agentID,
		"agent_type", agentType,
		"fork_label", strings.TrimSpace(p.ForkLabel),
		"query_source", strings.TrimSpace(p.QuerySource),
		"session_id", strings.TrimSpace(p.SessionID),
		"parent_run_id", strings.TrimSpace(p.ParentRunID),
		"agent_name", name,
	)
	res, err := a.Run(ctx)
	if res != nil && len(res.Session) > len(subctx.InitialMessages) {
		if appendErr := appendSidechainMessages(path, p.ForkLabel, p.QuerySource, p.SessionID, res.Session[len(subctx.InitialMessages):]); appendErr != nil && err == nil {
			err = appendErr
		}
	}
	out := &RunOutcome{
		Result:          res,
		SidechainPath:   path,
		ForkExecutionID: runKey,
		AgentID:         agentID,
		AgentType:       agentType,
		ParentSessionID: strings.TrimSpace(p.SessionID),
		ParentRunID:     strings.TrimSpace(p.ParentRunID),
		QuerySource:     strings.TrimSpace(p.QuerySource),
		ForkLabel:       strings.TrimSpace(p.ForkLabel),
		RuntimeKind:     "forked_subagent",
	}
	if res != nil && res.Summary != nil {
		out.PromptTokens = res.Summary.Usage.InputTokens
		out.CompletionTokens = res.Summary.Usage.OutputTokens
		telemetry.AddForkAgentUsage(out.PromptTokens, out.CompletionTokens)
	}
	slog.Info("fork_agent_query_done",
		"agent_id", agentID,
		"agent_type", agentType,
		"fork_label", strings.TrimSpace(p.ForkLabel),
		"query_source", strings.TrimSpace(p.QuerySource),
		"session_id", strings.TrimSpace(p.SessionID),
		"parent_run_id", strings.TrimSpace(p.ParentRunID),
		"elapsed_ms", time.Since(t0).Milliseconds(),
		"prompt_tokens", out.PromptTokens,
		"completion_tokens", out.CompletionTokens,
		"err", err,
	)
	return out, err
}

type RunOutcome struct {
	Result           *agent.Result
	SidechainPath    string
	ForkExecutionID  string
	AgentID          string
	AgentType        string
	ParentSessionID  string
	ParentRunID      string
	QuerySource      string
	ForkLabel        string
	RuntimeKind      string
	PromptTokens     int
	CompletionTokens int
}

// mergeAgentResults combines the visible output and accounting of sequential
// goal rounds without mutating either input.
func mergeAgentResults(first, next *agent.Result) *agent.Result {
	if first == nil {
		return next
	}
	if next == nil {
		return first
	}
	out := *first
	out.Parts = append(append([]llm.ContentPart(nil), first.Parts...), next.Parts...)
	out.Session = append(append([]llm.Message(nil), first.Session...), next.Session...)
	out.Reasoning = strings.TrimSpace(strings.Join(nonEmpty(first.Reasoning, next.Reasoning), "\n"))
	if next.LastResponseUsage != nil {
		usage := *next.LastResponseUsage
		out.LastResponseUsage = &usage
	}
	if next.MemoryCitation != nil {
		out.MemoryCitation = next.MemoryCitation
	}
	out.HandoffAgents = append(append([]*agent.Agent(nil), first.HandoffAgents...), next.HandoffAgents...)
	out.Summary = mergeSummaries(first.Summary, next.Summary)
	return &out
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func mergeSummaries(first, next *agent.RunSummary) *agent.RunSummary {
	if first == nil {
		return next
	}
	if next == nil {
		return first
	}
	out := *first
	out.Usage.InputTokens += next.Usage.InputTokens
	out.Usage.OutputTokens += next.Usage.OutputTokens
	out.ToolCalls += next.ToolCalls
	out.ToolErrors += next.ToolErrors
	out.Iterations += next.Iterations
	out.LLMCalls += next.LLMCalls
	out.Latency.Total += next.Latency.Total
	out.Latency.LLM += next.Latency.LLM
	out.Latency.Tool += next.Latency.Tool
	out.Tools = append(append([]agent.ToolCallSummary(nil), first.Tools...), next.Tools...)
	out.HandoffAgents = append(append([]string(nil), first.HandoffAgents...), next.HandoffAgents...)
	if next.AgentName != "" {
		out.AgentName = next.AgentName
	}
	if next.Error != "" {
		out.Error = next.Error
	}
	return &out
}

type CacheSafeParams struct {
	SystemPrompt               string
	UserContext                map[string]string
	SystemContext              map[string]string
	ToolUseContext             map[string]string
	ForkContextMessages        []llm.Message
	RenderedSystemPrompt       string
	ParentMessages             []llm.Message
	ParentAssistantToolMessage *llm.Message
	PlaceholderToolResults     []llm.Message
	DirectivePrefixMessages    []llm.Message
	ParentRunID                string
	ParentSessionID            string
	ToolPoolFingerprint        string
	ModelFingerprint           string
}

// RunPipeline applies pre-hooks then executes one agent request.
func RunPipeline(ctx context.Context, runner *Runner, pipe *hook.AgentPipeline, hc hook.HookContext, text string, parts []llm.ContentPart) (*agent.Result, hook.PreHookResult, error) {
	if runner == nil {
		return nil, hook.PreHookResult{}, errors.New("nil runner")
	}
	pre := hook.PreHookResult{Text: text}
	var err error
	if pipe != nil {
		pre, err = pipe.Run(ctx, "pre", hc, text)
		if err != nil {
			return nil, hook.PreHookResult{}, err
		}
	}
	if pre.SystemAddendum != "" {
		ctx = tool.WithSystemAddendum(ctx, pre.SystemAddendum)
	}
	if len(parts) > 0 {
		res, err := runner.RunContent(ctx, pipelineParts(pre.Text, parts))
		return res, pre, err
	}
	res, err := runner.Run(ctx, pre.Text)
	return res, pre, err
}

func pipelineParts(text string, parts []llm.ContentPart) []llm.ContentPart {
	out := append([]llm.ContentPart(nil), parts...)
	for i := range out {
		if out[i].Type == llm.ContentTypeText {
			out[i].Text = text
			return out
		}
	}
	return append([]llm.ContentPart{llm.Text(text)}, out...)
}
