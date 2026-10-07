// Forked runs: context, capture, cache, and runtime snapshot.
package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

const (
	forkBoilerplateTag    = "FORKED_SUBAGENT"
	forkDirectivePrefix   = "Assigned directive:\n"
	forkPlaceholderResult = "Fork started - processing in background"
)

func BuildForkedMessages(directive string, parentAssistant llm.Message) ([]llm.Message, error) {
	if parentAssistant.Role != llm.RoleAssistant {
		return nil, fmt.Errorf("parent assistant message required")
	}
	toolCalls := append([]llm.ToolCall(nil), parentAssistant.ToolCalls...)
	if len(toolCalls) == 0 {
		return []llm.Message{
			llm.UserMessage(llm.Text(buildForkChildDirective(directive))),
		}, nil
	}
	fullAssistant := deepCloneMessages([]llm.Message{parentAssistant})[0]
	msgs := []llm.Message{fullAssistant}
	for _, call := range toolCalls {
		msgs = append(msgs, llm.ToolResultMessage(
			strings.TrimSpace(call.ID),
			llm.Text(forkPlaceholderResult),
		))
	}
	msgs = append(msgs, llm.UserMessage(llm.Text(buildForkChildDirective(directive))))
	return msgs, nil
}

func buildForkChildDirective(directive string) string {
	directive = strings.TrimSpace(directive)
	var b strings.Builder
	b.WriteString("<")
	b.WriteString(forkBoilerplateTag)
	b.WriteString(">\n")
	b.WriteString("STOP. READ THIS FIRST.\n\n")
	b.WriteString("You are a forked worker. You are not the main agent.\n")
	b.WriteString("Do not fork again. Do not ask questions. Use tools directly and report once at the end.\n")
	b.WriteString("Keep the final report short, factual, and scoped to the assigned directive.\n")
	b.WriteString("</")
	b.WriteString(forkBoilerplateTag)
	b.WriteString(">\n\n")
	b.WriteString(forkDirectivePrefix)
	b.WriteString(directive)
	return b.String()
}

type forkCaptureLLM struct {
	inner llm.LLM
}

func wrapForkCaptureLLM(inner llm.LLM) llm.LLM {
	if inner == nil {
		return inner
	}
	return &forkCaptureLLM{inner: inner}
}

func (w *forkCaptureLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	// A fork inherits the parent request's prefix so the provider serves it
	// from cache, which is why the request is captured here rather than
	// rebuilt: only the wire messages carry the blocks injected ahead of the
	// conversation. A child run shares its parent's slot, so the source gate
	// keeps a subagent's own requests from overwriting what its parent forked
	// from.
	if slot := forkCacheSlotFromContext(ctx); slot != nil && ShouldSaveCacheSafeParams(QuerySourceFromContext(ctx), "") {
		slot.store(buildCacheSafeParamsFromMessages(messages))
	}
	return w.inner.Execute(ctx, messages, tools)
}

func ShouldSaveCacheSafeParams(querySource string, agentID string) bool {
	if agentID != "" {
		return false
	}
	switch querySource {
	case "", "repl_main_thread", "sdk":
		return true
	default:
		return false
	}
}

func buildCacheSafeParamsFromMessages(messages []llm.Message) *CacheSafeParams {
	params := &CacheSafeParams{
		UserContext:          map[string]string{},
		SystemContext:        map[string]string{},
		ToolUseContext:       map[string]string{},
		RenderedSystemPrompt: "",
	}
	if len(messages) == 0 {
		return params
	}
	start := 0
	if messages[0].Role == llm.RoleSystem {
		params.SystemPrompt = messages[0].TextContent()
		params.RenderedSystemPrompt = params.SystemPrompt
		start = 1
	}
	if start < len(messages) {
		params.ForkContextMessages = deepCloneMessages(messages[start:])
		params.ParentMessages = deepCloneMessages(messages[start:])
	}
	return params
}

// CacheSafeParamsForRun returns the parameters a fork spawned by this run
// inherits: the parent's last request, captured by forkCaptureLLM, or the
// agent's description alone when this run captured nothing.
func (r *Runner) CacheSafeParamsForRun(ctx context.Context) (*CacheSafeParams, error) {
	if r == nil {
		return nil, fmt.Errorf("nil runner")
	}
	if slot := forkCacheSlotFromContext(ctx); slot != nil {
		if params, ok := slot.load(); ok {
			return params, nil
		}
	}
	desc := strings.TrimSpace(r.MainAgentDescription())
	if desc == "" {
		return nil, fmt.Errorf("empty agent description")
	}
	return &CacheSafeParams{
		SystemPrompt:         desc,
		RenderedSystemPrompt: desc,
		UserContext:          map[string]string{},
		SystemContext:        map[string]string{},
		ToolUseContext:       map[string]string{},
	}, nil
}

// forkCacheSlot holds the cache-safe parameters captured during one run,
// together with the lock that guards them so the two cannot be separated.
//
// It lives in the run's context because that is exactly how long it is needed:
// the capture happens on the request that produced the fork tool call, and the
// fork that inherits it runs inside that same turn. Holding it in a map on the
// Runner keyed by session meant a cloned parent transcript per session for as
// long as the process ran, released by nothing, because a session has no end
// this runtime can observe.
type forkCacheSlot struct {
	mu     sync.Mutex
	params *CacheSafeParams
}

type forkCacheSlotKey struct{}

// WithForkCacheCapture gives a run somewhere to keep the parameters its forks
// inherit. Runner.RunContent installs one per turn; a run without one simply
// captures nothing, and its forks start from the agent description.
func WithForkCacheCapture(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, forkCacheSlotKey{}, &forkCacheSlot{})
}

func forkCacheSlotFromContext(ctx context.Context) *forkCacheSlot {
	if ctx == nil {
		return nil
	}
	slot, _ := ctx.Value(forkCacheSlotKey{}).(*forkCacheSlot)
	return slot
}

func (f *forkCacheSlot) store(params *CacheSafeParams) {
	if f == nil || params == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.params = cloneForkCacheParams(params)
}

func (f *forkCacheSlot) load() (*CacheSafeParams, bool) {
	if f == nil {
		return nil, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.params == nil {
		return nil, false
	}
	return cloneForkCacheParams(f.params), true
}

func cloneForkCacheParams(in *CacheSafeParams) *CacheSafeParams {
	if in == nil {
		return nil
	}
	out := &CacheSafeParams{
		SystemPrompt:               strings.TrimSpace(in.SystemPrompt),
		RenderedSystemPrompt:       strings.TrimSpace(in.RenderedSystemPrompt),
		UserContext:                cloneStringMap(in.UserContext),
		SystemContext:              cloneStringMap(in.SystemContext),
		ToolUseContext:             cloneStringMap(in.ToolUseContext),
		ForkContextMessages:        deepCloneMessages(in.ForkContextMessages),
		ParentMessages:             deepCloneMessages(in.ParentMessages),
		ParentAssistantToolMessage: cloneMessagePtr(in.ParentAssistantToolMessage),
		PlaceholderToolResults:     deepCloneMessages(in.PlaceholderToolResults),
		DirectivePrefixMessages:    deepCloneMessages(in.DirectivePrefixMessages),
		ParentRunID:                strings.TrimSpace(in.ParentRunID),
		ParentSessionID:            strings.TrimSpace(in.ParentSessionID),
		ToolPoolFingerprint:        strings.TrimSpace(in.ToolPoolFingerprint),
		ModelFingerprint:           strings.TrimSpace(in.ModelFingerprint),
	}
	return out
}

type forkRuntimeCtxKey string

const forkRuntimeSnapshotKey forkRuntimeCtxKey = "forebrain_fork_runtime_snapshot"

func withForkRuntimeSnapshot(ctx context.Context, messages []llm.Message) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, forkRuntimeSnapshotKey, deepCloneMessages(messages))
}

func forkRuntimeSnapshotFromContext(ctx context.Context) ([]llm.Message, bool) {
	if ctx == nil {
		return nil, false
	}
	msgs, ok := ctx.Value(forkRuntimeSnapshotKey).([]llm.Message)
	if !ok || len(msgs) == 0 {
		return nil, false
	}
	return deepCloneMessages(msgs), true
}

func deepCloneMessages(in []llm.Message) []llm.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]llm.Message, 0, len(in))
	for _, msg := range in {
		cloned := msg
		if len(msg.Parts) > 0 {
			cloned.Parts = append([]llm.ContentPart(nil), msg.Parts...)
		}
		if len(msg.ToolCalls) > 0 {
			cloned.ToolCalls = append([]llm.ToolCall(nil), msg.ToolCalls...)
		}
		if msg.ToolExecutionTiming != nil {
			timing := *msg.ToolExecutionTiming
			cloned.ToolExecutionTiming = &timing
		}
		out = append(out, cloned)
	}
	return out
}

func cloneMessagePtr(in *llm.Message) *llm.Message {
	if in == nil {
		return nil
	}
	msgs := deepCloneMessages([]llm.Message{*in})
	if len(msgs) == 0 {
		return nil
	}
	out := msgs[0]
	return &out
}

type sidechainEnvelope struct {
	At          int64           `json:"at"`
	ForkLabel   string          `json:"fork_label,omitempty"`
	QuerySource string          `json:"query_source,omitempty"`
	SessionID   string          `json:"session_id,omitempty"`
	Messages    json.RawMessage `json:"messages"`
}

func appendSidechainMessages(path string, forkLabel, querySource, sessionID string, messages []llm.Message) error {
	if path == "" {
		return nil
	}
	bundle, err := json.Marshal(messages)
	if err != nil {
		return err
	}
	env := sidechainEnvelope{
		At:          time.Now().Unix(),
		ForkLabel:   forkLabel,
		QuerySource: querySource,
		SessionID:   sessionID,
		Messages:    bundle,
	}
	line, err := json.Marshal(env)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}

// SidechainFilePath locates a fork's message log. workspaceRoot must be the
// owning agent's workspace directory, never the shared FOREBRAIN_HOME: the log
// replays that agent's prompts, tool arguments and tool results verbatim, so
// rooting it at the home would publish one tenant's conversation into a
// directory every other primary agent can read. The session's directory is
// hook.SessionSidechainDir — the one definition — so a deletion that walks it
// cannot miss logs written by this spelling either.
func SidechainFilePath(workspaceRoot, sessionID, forkLabel, runKey string) string {
	fl := sanitizePathSegment(forkLabel)
	rk := sanitizePathSegment(runKey)
	return filepath.Join(hook.SessionSidechainDir(workspaceRoot, sessionID), fl+"-"+rk+".jsonl")
}

func sanitizePathSegment(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "x"
	}
	return out
}

func forkAgentName(base, forkLabel string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "fork"
	}
	fl := sanitizePathSegment(forkLabel)
	if fl == "" || fl == "x" {
		return base + "_fork"
	}
	return base + "_" + fl
}

func validateRunParams(p RunParams) error {
	if p.LLM == nil {
		return fmt.Errorf("nil llm")
	}
	if p.CacheSafe == nil {
		return fmt.Errorf("nil cache safe params")
	}
	if strings.TrimSpace(p.CacheSafe.SystemPrompt) == "" {
		return fmt.Errorf("empty system prompt")
	}
	if p.RegisterTools == nil {
		return fmt.Errorf("nil register tools")
	}
	return nil
}
