package hook

import (
	"context"
	"sort"
	"strings"
)

type AgentPipeline struct {
	entries []struct {
		name string
		fn   AgentHook
	}
}

const (
	HookContextEngine  = "context-engine"
	HookGitContext     = "git-context"
	HookPlanMode       = "plan-mode"
	HookForebrainRules = "forebrain-rules"
)

// CorePreHooks is the hook set every surface installs, so the terminal and the
// gateway describe a turn to the model in the same way rather than each
// growing its own injection path.
type CorePreHooks struct {
	ForebrainRules AgentHook
	PlanMode       AgentHook
	ContextEngine  AgentHook
	GitContext     AgentHook
}

func NewAgentPipeline() *AgentPipeline {
	return &AgentPipeline{}
}

func (p *AgentPipeline) Add(name string, fn AgentHook) {
	if name == "" || fn == nil {
		return
	}
	p.entries = append(p.entries, struct {
		name string
		fn   AgentHook
	}{name, fn})
	sort.Slice(p.entries, func(i, j int) bool { return hookRunsBefore(p.entries[i].name, p.entries[j].name) })
}

// coreHookOrder is the order the core hooks run in. Each rewrites the text the
// next one sees, so the order is part of what the model is sent and must not
// follow from how the hooks happen to be spelled. Any other hook runs after
// them, by name.
var coreHookOrder = []string{HookContextEngine, HookGitContext, HookPlanMode, HookForebrainRules}

func hookRunsBefore(a, b string) bool {
	if ra, rb := coreHookRank(a), coreHookRank(b); ra != rb {
		return ra < rb
	}
	return a < b
}

func coreHookRank(name string) int {
	for i, core := range coreHookOrder {
		if name == core {
			return i
		}
	}
	return len(coreHookOrder)
}

func (p *AgentPipeline) AddCorePreHooks(h CorePreHooks) {
	if p == nil {
		return
	}
	p.Add(HookForebrainRules, h.ForebrainRules)
	p.Add(HookPlanMode, h.PlanMode)
	p.Add(HookContextEngine, h.ContextEngine)
	p.Add(HookGitContext, h.GitContext)
}

func (p *AgentPipeline) Names() []string {
	if p == nil || len(p.entries) == 0 {
		return nil
	}
	out := make([]string, 0, len(p.entries))
	for _, entry := range p.entries {
		out = append(out, entry.name)
	}
	return out
}

func (p *AgentPipeline) Run(ctx context.Context, phase string, hc HookContext, text string) (PreHookResult, error) {
	res := PreHookResult{Text: text}
	var err error
	for i := range p.entries {
		var out PreHookResult
		out, err = p.entries[i].fn(ctx, phase, hc, res.Text)
		if err != nil {
			return PreHookResult{}, err
		}
		res.Text = out.Text
		if out.SystemAddendum != "" {
			res.SystemAddendum = strings.TrimSpace(res.SystemAddendum + "\n\n" + out.SystemAddendum)
		}
	}
	return res, nil
}
