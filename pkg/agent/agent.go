// Copyright 2026 Simone Vellei
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// Agent runs a chat loop using an llm.LLM and optional tools.
type Agent struct {
	llm         llm.LLM
	name        string
	description string

	tools          []*llm.Tool
	tracer         Tracer
	handoffs       map[string]*Agent
	sessionBuilder func(context.Context, []llm.ContentPart) ([]llm.Message, int, error)
}

// Result represents the final output of an agent after processing user input and executing any tool calls.
type Result struct {
	// Parts holds the multimodal content of the final assistant message.
	Parts []llm.ContentPart
	// Session is the canonical non-streaming message array accumulated during the run.
	Session []llm.Message
	// Reasoning holds provider-returned reasoning text when available.
	Reasoning string
	// LastResponseUsage holds the provider usage from the final LLM API
	// response in this agent run. Summary.Usage remains the whole-run total.
	LastResponseUsage *llm.Usage
	// MemoryCitation carries provenance for the final assistant message.
	MemoryCitation *llm.MemoryCitation
	// HandoffAgents lists the agents that were handed work; more than one means fan-out.
	HandoffAgents []*Agent
	Summary       *RunSummary
}

// TextContent returns the concatenation of all text parts in the result.
func (r *Result) TextContent() string {
	if r == nil {
		return ""
	}
	return llm.TextContent(r.Parts...)
}

// PartsCopy returns a defensive copy of the result's final-message parts, so a
// caller persisting them cannot mutate the result the run is still reading.
func (r *Result) PartsCopy() []llm.ContentPart {
	if r == nil || len(r.Parts) == 0 {
		return nil
	}
	return append([]llm.ContentPart(nil), r.Parts...)
}

// LastResponseUsageCopy returns a defensive copy of the final response's
// usage, or nil when the result carries none. Two surfaces used to keep their
// own copy of this, one in pkg/tui and one in pkg/gateway; it is a property of
// the Result itself.
func (r *Result) LastResponseUsageCopy() *llm.Usage {
	if r == nil || r.LastResponseUsage == nil {
		return nil
	}
	usage := *r.LastResponseUsage
	return &usage
}

// New creates a new Agent.
//
// name and description must be non-empty. client must be non-nil.
func New(client llm.LLM, name, description string) (*Agent, error) {
	if client == nil {
		return nil, ErrUndefinedLLM
	}

	if name == "" {
		return nil, ErrNameRequired
	}

	if description == "" {
		return nil, ErrDescriptionRequired
	}

	return &Agent{
		llm:         client,
		name:        name,
		description: description,
		tools:       make([]*llm.Tool, 0),
		tracer:      Noop,
		handoffs:    make(map[string]*Agent),
	}, nil
}

// Name returns the agent name.
func (a *Agent) Name() string {
	return a.name
}

// Description returns the agent system prompt.
func (a *Agent) Description() string {
	return a.description
}

// AddTool registers a function tool.
//
// It returns ToolAlreadyExistsError if a tool with the same name is already present.
func (a *Agent) AddTool(tool *llm.Tool) error {
	if _, exists := a.getTool(tool.Name()); exists {
		return &ToolAlreadyExistsError{Name: tool.Name()}
	}

	a.tools = append(a.tools, tool)
	return nil
}

// Tools returns the tools the agent offers the model, in the order its requests
// carry them.
func (a *Agent) Tools() []*llm.Tool {
	return append([]*llm.Tool(nil), a.tools...)
}

func (a *Agent) getTool(toolName string) (*llm.Tool, bool) {
	for _, t := range a.tools {
		if t.Name() == toolName {
			return t, true
		}
	}
	return nil, false
}

// AgentHandoffInput is the structured argument passed to a handoff tool.
type AgentHandoffInput struct {
	Context string `json:"context" jsonschema:"The contextual data gathered by the source agent to be passed to the receiving agent."`
}

// AddTool registers a function tool.
//
// It returns ToolAlreadyExistsError if a tool with the same name is already present.
func (a *Agent) AddHandoff(handoffAgent *Agent) error {
	toolName := fmt.Sprintf("handoff_to_%s", SanitizeToolName(handoffAgent.Name()))

	if _, exists := a.getTool(toolName); exists {
		return &ToolAlreadyExistsError{Name: toolName}
	}

	tool, err := llm.NewTool(
		toolName,
		handoffAgent.Description(),
		func(ctx context.Context, i *AgentHandoffInput) (string, error) {
			return fmt.Sprintf("%s: success", toolName), nil
		},
	)
	if err != nil {
		return err
	}

	a.tools = append(a.tools, tool)
	a.handoffs[toolName] = handoffAgent

	return nil
}

// SetTracer configures the Tracer used to observe agent lifecycle events.
//
// If not set, all events are discarded (Noop is the default).
func (a *Agent) SetTracer(t Tracer) {
	a.tracer = t
}

// SetSessionBuilder overrides the default main-thread session assembly logic.
// Used by Forebrain Harness to supply a transcript/message-array based active view.
func (a *Agent) SetSessionBuilder(fn func(context.Context, []llm.ContentPart) ([]llm.Message, int, error)) {
	a.sessionBuilder = fn
}

// Run executes the agent loop for the given user input parts.
//
// Call with llm.Text("hello") for a plain-text message, or mix llm.Text and
// llm.ImageURL parts for multimodal input.
//
// The agent calls the LLM, executes any requested tool calls, and repeats until
// the model returns a message without tool calls.
//
// If the run succeeds but saving the session to memory fails, the result is
// still returned together with the save error joined via errors.Join.
func (a *Agent) Run(ctx context.Context, parts ...llm.ContentPart) (result *Result, err error) {
	usageAcc := llm.NewUsageAccumulator()
	ctx = llm.WithUsageAccumulator(ctx, usageAcc)
	ctx = WithTracer(ctx, a.tracer)
	ctx = WithAgentName(ctx, a.name)
	stats := newRunStats(a.name)
	var handoffAgentNames []string

	session, sessionIndex, err := a.prepareSession(ctx, parts, stats)
	if err != nil {
		return nil, err
	}

	inputText := llm.TextContent(parts...)
	a.tracer.Trace(AgentStartEvent{
		AgentName: a.name,
		Input:     inputText,
		Timestamp: time.Now(),
	})

	iteration := 0

	defer func() {
		if saveErr := a.saveSession(ctx, session, sessionIndex, stats); saveErr != nil {
			err = errors.Join(err, fmt.Errorf("%w: %w", ErrSessionSaveFailed, saveErr))
		}

		output := ""
		if result != nil {
			output = result.TextContent()
		}
		a.tracer.Trace(AgentEndEvent{
			AgentName:  a.name,
			Output:     output,
			Err:        err,
			Iterations: iteration,
			Timestamp:  time.Now(),
		})

		summary := stats.summary(iteration, handoffAgentNames, err)
		totalUsage := usageAcc.Snapshot()
		if totalUsage.InputTokens > 0 || totalUsage.OutputTokens > 0 ||
			totalUsage.CacheReadInputTokens > 0 || totalUsage.CacheCreationInputTokens > 0 {
			summary.Usage.InputTokens = totalUsage.InputTokens
			summary.Usage.OutputTokens = totalUsage.OutputTokens
			summary.Usage.CacheReadInputTokens = totalUsage.CacheReadInputTokens
			summary.Usage.CacheCreationInputTokens = totalUsage.CacheCreationInputTokens
		}
		if result != nil {
			result.Summary = summary
		}

		a.tracer.Trace(AgentRunSummaryEvent{
			Summary:   *summary,
			Timestamp: time.Now(),
		})
	}()

	for {
		iteration++
		a.tracer.Trace(AgentIterationEvent{
			AgentName: a.name,
			Iteration: iteration,
			Timestamp: time.Now(),
		})

		iterCtx := WithIteration(ctx, iteration)

		iterationResult, err := a.handleAgentIteration(iterCtx, session, iteration, stats)
		if err != nil {
			return nil, err
		}

		session = iterationResult.session
		if len(iterationResult.handoffAgents) > 0 {
			for _, ha := range iterationResult.handoffAgents {
				handoffAgentNames = append(handoffAgentNames, ha.Name())
			}
		}

		// If finalMessage is nil, it means the agent executed tool calls and needs to call the LLM again.
		if iterationResult.lastMessage != nil {
			return &Result{
				Parts:          iterationResult.lastMessage.Parts,
				Session:        append([]llm.Message(nil), session...),
				Reasoning:      extractReasoningFromMessage(iterationResult.lastMessage),
				MemoryCitation: cloneMemoryCitation(iterationResult.lastMessage.MemoryCitation),
				HandoffAgents:  iterationResult.handoffAgents,
			}, nil
		}
	}
}

func cloneMemoryCitation(citation *llm.MemoryCitation) *llm.MemoryCitation {
	if citation == nil {
		return nil
	}
	copy := *citation
	copy.Entries = append([]llm.MemoryCitationEntry(nil), citation.Entries...)
	copy.RolloutIDs = append([]string(nil), citation.RolloutIDs...)
	return &copy
}

func extractReasoningFromMessage(msg *llm.Message) string {
	if msg == nil {
		return ""
	}
	return llm.ExtractSummary(msg.Name)
}

// saveSession records memory-related stats.
func (a *Agent) saveSession(ctx context.Context, messages []llm.Message, sessionIndex int, stats *runStats) error {
	return nil
}

// agentIteration represents the result of one iteration of the agent loop.
type agentIteration struct {
	session       []llm.Message
	lastMessage   *llm.Message
	handoffAgents []*Agent
}

// handleAgentIteration executes one iteration of the agent loop: it calls the LLM with the current messages,
// adds the response to the messages and memory, and executes any tool calls in the response.
func (a *Agent) handleAgentIteration(ctx context.Context, session []llm.Message, iteration int, stats *runStats) (agentIteration, error) {
	tracedLLM := NewLLM(a.llm, a.tracer)
	start := time.Now()
	msg, err := tracedLLM.Execute(ctx, session, a.tools)
	duration := time.Since(start)
	if err != nil {
		stats.recordLLM(duration, nil)
		return agentIteration{session: session}, err
	}
	stats.recordLLM(duration, msg.Usage)

	// When an orchestration wrapper (e.g. toolOrchestrationLLM) ran its own
	// internal tool-calling loop, it returns the full accumulated session via
	// Result.Session. Replace the agent's session with it so that intermediate
	// tool calls and results are included in Result.Session and persisted.
	if msg.Session != nil {
		session = msg.Session
	} else {
		session = append(session, *msg.Message)
	}

	if len(msg.Message.ToolCalls) == 0 {
		return agentIteration{session: session, lastMessage: msg.Message}, nil
	}

	// Execute all tool calls concurrently, preserving order.
	toolCalls := msg.Message.ToolCalls
	results := make([]*llm.Message, len(toolCalls))

	var wg sync.WaitGroup
	wg.Add(len(toolCalls))
	for i, toolCall := range toolCalls {
		go func() {
			defer wg.Done()
			results[i] = a.handleToolCall(ctx, toolCall, iteration, stats)
		}()
	}
	wg.Wait()

	// Append results in order; collect all handoffs (fan-out when more than one).
	var handoffAgents []*Agent
	var handoffMsg *llm.Message
	for i, result := range results {
		session = append(session, *result)
		if hAgent, ok := a.handoffs[toolCalls[i].Function.Name]; ok {
			handoffAgents = append(handoffAgents, hAgent)
			if handoffMsg == nil {
				handoffMsg = result
			}
		}
	}

	if len(handoffAgents) > 0 {
		// All tool results are preserved in session. Return the first handoff tool's
		// result as lastMessage so callers receive it in Result.Parts.
		return agentIteration{session: session, lastMessage: handoffMsg, handoffAgents: handoffAgents}, nil
	}

	return agentIteration{session: session}, nil
}

// handleToolCall executes a tool call and returns the result as a message to be added to the conversation.
func (a *Agent) handleToolCall(ctx context.Context, toolCall llm.ToolCall, iteration int, stats *runStats) *llm.Message {
	a.tracer.Trace(ToolCallEvent{
		AgentName: a.name,
		ToolName:  toolCall.Function.Name,
		Arguments: toolCall.Function.Arguments,
		CallID:    toolCall.ID,
		Iteration: iteration,
		Timestamp: time.Now(),
	})

	resultParts, timing, err := a.executeToolCall(ctx, toolCall)
	stats.recordTool(toolCall.Function.Name, err, timing.Duration)

	// Trace with text representation.
	traceResult := llm.TextContent(resultParts...)
	if err != nil {
		traceResult = err.Error()
	}
	a.tracer.Trace(ToolResultEvent{
		AgentName: a.name,
		ToolName:  toolCall.Function.Name,
		Result:    traceResult,
		Err:       err,
		CallID:    toolCall.ID,
		Iteration: iteration,
		Timestamp: time.Now(),
	})

	if err != nil {
		resultParts = []llm.ContentPart{llm.Text(fmt.Sprintf("Error executing tool '%s': %v", toolCall.Function.Name, err))}
	}

	return &llm.Message{
		Role:                llm.RoleTool,
		Parts:               resultParts,
		ToolCallID:          toolCall.ID,
		ToolExecutionTiming: &timing,
	}
}

// prepareSession prepares the messages for the LLM call, including the system prompt and user input.
func (a *Agent) prepareSession(ctx context.Context, parts []llm.ContentPart, stats *runStats) ([]llm.Message, int, error) {
	if a.sessionBuilder != nil {
		return a.sessionBuilder(ctx, parts)
	}
	messages := []llm.Message{llm.SystemMessage(a.description)}
	sessionIndex := len(messages)

	if len(parts) > 0 {
		messages = append(messages, llm.UserMessage(parts...))
	}

	return messages, sessionIndex, nil
}

// executeToolCall executes a tool call and returns its content plus executor timing.
func (a *Agent) executeToolCall(ctx context.Context, tc llm.ToolCall) ([]llm.ContentPart, llm.ExecutionTiming, error) {
	tool, found := a.getTool(tc.Function.Name)
	if !found {
		return nil, llm.ExecutionTiming{}, &ToolUnknownError{Name: tc.Function.Name}
	}

	execution, err := tool.Execute(ctx, tc.Function.Arguments)
	if err != nil {
		return nil, execution.Timing, &ToolExecutionError{Name: tc.Function.Name, Err: err}
	}

	return anyToContentParts(execution.Result), execution.Timing, nil
}

// anyToContentParts converts any tool result value to a slice of ContentParts.
//
// If the value is already []llm.ContentPart it is returned directly.
// A plain string becomes a single text part.
// All other types are JSON-marshalled into a text part.
func anyToContentParts(v any) []llm.ContentPart {
	if v == nil {
		return nil
	}
	if parts, ok := v.([]llm.ContentPart); ok {
		return parts
	}
	if s, ok := v.(string); ok {
		return []llm.ContentPart{llm.Text(s)}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return []llm.ContentPart{llm.Text(fmt.Sprintf("failed to marshal tool result: %v", err))}
	}
	return []llm.ContentPart{llm.Text(string(b))}
}

// AsTool exports this agent as an OpenAI function tool.
//
// The returned handler keeps an internal message history so repeated tool calls
// act like a continuing conversation with this agent.
//
// The agent's Description is injected as the system prompt by Run().
//
// Tool arguments schema: {"input": "..."}.
func (a *Agent) AsTool(toolName, toolDescription string) (*llm.Tool, error) {
	type ToolInput struct {
		Input string `json:"input" jsonschema_description:"Instructions for the agent. Describe the task, question, or problem the agent should solve."`
	}

	type ToolOutput struct {
		Output string `json:"output" jsonschema:"description=The agent's response"`
	}

	handler := func(ctx context.Context, input *ToolInput) (*ToolOutput, error) {
		response, err := a.Run(ctx, llm.Text(input.Input))
		if err != nil {
			return nil, err
		}
		return &ToolOutput{Output: response.TextContent()}, nil
	}

	return llm.NewTool(
		toolName,
		toolDescription,
		handler,
	)
}

// SanitizeToolName maps any string to one accepted by LLM providers.
// Only [a-zA-Z0-9_-] are kept (all others become '_'); capped at 64 chars;
// empty result falls back to "agent".
func SanitizeToolName(name string) string {
	s := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, name)
	if len(s) > 64 {
		s = s[:64]
	}
	if s == "" {
		s = "agent"
	}
	return s
}
