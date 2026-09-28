package assembly

import (
	"context"
	"strings"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/hook"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

type Hook struct {
	Cfg                 *appcfg.Root
	Home                string
	WorkspaceRoot       string
	Store               *state.SessionStore
	RunRT               *state.RunStore
	SkillHub            *skill.Hub
	PinsProvider        func(sessionID string) []string
	ReadStatesProvider  func() []tool.ReadState
	ModeProvider        func(sessionID string) (mode string, phase string)
	SnapshotWriter      func(sessionID string, snapshot AssemblyResult)
	ModelLimitsProvider func(sessionID string) (llm.Limits, bool)
}

func NewHook(cfg *appcfg.Root, store *state.SessionStore) *Hook {
	return &Hook{
		Cfg:   cfg,
		Store: store,
	}
}

func (h *Hook) Hook() hook.AgentHook {
	return h.run
}

func (h *Hook) Assemble(hc hook.HookContext, text string) (AssemblyResult, bool, error) {
	if h == nil {
		return AssemblyResult{}, false, nil
	}
	if !hook.InteractiveTrigger(hc.Trigger) {
		return AssemblyResult{}, false, nil
	}
	promptText := strings.TrimSpace(text)
	query := strings.TrimSpace(hc.RawInput)
	if query == "" {
		query = promptText
	}
	if promptText == "" || strings.HasPrefix(promptText, "/") {
		return AssemblyResult{}, false, nil
	}
	sid := strings.TrimSpace(hc.SessionID)
	if sid == "" {
		sid = "default"
	}
	mode := "agent"
	if h.ModeProvider != nil {
		m, _ := h.ModeProvider(sid)
		if strings.TrimSpace(m) != "" {
			mode = strings.TrimSpace(m)
		}
	}
	pins := []string(nil)
	if h.PinsProvider != nil {
		pins = h.PinsProvider(sid)
	}
	req := AssemblyRequest{
		SessionID:   sid,
		RunID:       strings.TrimSpace(hc.RunID),
		Channel:     hc.Channel,
		RunKind:     hc.RunKind,
		AgentID:     hc.AgentID,
		Mode:        mode,
		Query:       query,
		PinnedSet:   pins,
		LimitTokens: 0,
		MaxItems:    0,
		Now:         time.Now().UTC(),
	}
	ApplyRetrievalPlan(&req)
	engine := New()
	engine.Register(WorkingSetSource{})
	if h.ReadStatesProvider != nil {
		engine.Register(RecentFilesSource{ReadStatesProvider: h.ReadStatesProvider})
	}
	engine.Register(GitContextSource{})
	// Skills reach the model through the prompt catalog, not an index source.
	res := engine.Build(req)
	if h.Cfg != nil {
		rr := safety.ResolveFromRoot(h.Cfg)
		var nextItems []ContextItem
		for _, it := range res.Items {
			chunks := safety.ContextItemsToChunks(it.SourceID, it.Content)
			kept, _ := safety.EvaluateRetrieval(rr, chunks)
			if len(kept) == 0 {
				continue
			}
			it.Content = kept[0].Text
			nextItems = append(nextItems, it)
		}
		res.Items = nextItems
		// Resolve the model's real token limits (models.json) so auto-compaction
		// triggers against the actual context window, not the small RAG-injection
		// budget. Config ModelContextTokens is a fallback when the catalog misses.
		var limits llm.Limits
		if h.ModelLimitsProvider != nil {
			if l, ok := h.ModelLimitsProvider(sid); ok {
				limits = l
			}
		}
		if limits.Context <= 0 {
			limits.Context = h.Cfg.Agents.Defaults.ContextInject.ModelContextTokens
		}
		res.ModelContextTokens = limits.Context
		res.MaxOutputTokens = limits.EffectiveOutputReserve()
		effInput := limits.EffectiveInputLimit()
		res.EffectiveInputTokens = effInput
		// Conversation size: the whole prompt the model last reported (cache
		// reads and writes included) plus its output, then a local estimate of
		// what was appended since — or of everything, before any usage came
		// back. Read from the session's own messages, the same input the
		// composer footer's gauge uses, so the two cannot disagree.
		if h.Store != nil {
			if turns, err := h.Store.ListRecentMessages(context.Background(), sid, 400); err == nil {
				res.ConversationTokens = state.TokenCountWithEstimation(turns)
			}
		}
		if effInput > 0 {
			// Project the next request: prior conversation + this turn's injected
			// context + query. Conservative (slightly over-counts) so we compact
			// before the provider rejects the call.
			projected := res.ConversationTokens + res.ProjectedContextTokens
			remaining := effInput - projected
			res.ProjectedContextTokens = projected
			res.RemainingContextTokens = remaining
			blockAt := h.Cfg.Agents.Defaults.ContextInject.BlockRemainingTokens
			warnAt := h.Cfg.Agents.Defaults.ContextInject.WarnRemainingTokens
			if warnAt < blockAt {
				warnAt = blockAt
			}
			switch {
			case remaining <= blockAt:
				res.ContextPressure = "block"
			case remaining <= warnAt:
				res.ContextPressure = "warn"
			default:
				res.ContextPressure = "ok"
			}
		}
	}
	return res, true, nil
}

func (h *Hook) run(ctx context.Context, phase string, hc hook.HookContext, text string) (hook.PreHookResult, error) {
	out := hook.PreHookResult{Text: text}
	if h == nil || phase != "pre" {
		return out, nil
	}
	res, ok, err := h.Assemble(hc, text)
	if err != nil {
		return out, err
	}
	if !ok {
		return out, nil
	}
	if h.SnapshotWriter != nil {
		h.SnapshotWriter(strings.TrimSpace(res.SessionID), res)
	}
	return out, nil
}
