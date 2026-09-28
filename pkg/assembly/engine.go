package assembly

import (
	"sort"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

type Engine struct {
	sources []Source
}

func New() *Engine {
	return &Engine{}
}

func (e *Engine) Register(src Source) {
	if e == nil || src == nil {
		return
	}
	e.sources = append(e.sources, src)
}

func (e *Engine) Build(req AssemblyRequest) AssemblyResult {
	if req.Now.IsZero() {
		req.Now = time.Now().UTC()
	}
	limit := req.LimitTokens
	if limit <= 0 {
		limit = modeBudget(req.Mode)
	}
	maxItems := req.MaxItems
	if maxItems <= 0 {
		maxItems = 16
	}
	all := make([]ContextItem, 0, 32)
	prov := make([]ContextProvenance, 0, 32)
	for _, src := range e.sources {
		items, err := src.Collect(req)
		if err != nil {
			prov = append(prov, ContextProvenance{
				SourceID: src.ID(),
				Included: false,
				Reason:   err.Error(),
			})
			continue
		}
		if len(items) == 0 {
			prov = append(prov, ContextProvenance{
				SourceID: src.ID(),
				Included: false,
				Reason:   "empty",
			})
			continue
		}
		for i := range items {
			if items[i].EstimatedTokens <= 0 {
				items[i].EstimatedTokens = llm.EstimateText(items[i].Content)
			}
			if items[i].SourceID == "" {
				items[i].SourceID = src.ID()
			}
			items[i].Priority += sourcePriorityBoost(req.Mode, items[i].SourceID)
		}
		all = append(all, items...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Pinned != all[j].Pinned {
			return all[i].Pinned
		}
		if all[i].Priority != all[j].Priority {
			return all[i].Priority > all[j].Priority
		}
		return all[i].EstimatedTokens < all[j].EstimatedTokens
	})
	selected := make([]ContextItem, 0, len(all))
	used := 0
	for _, item := range all {
		if len(selected) >= maxItems {
			prov = append(prov, ContextProvenance{
				SourceID:        item.SourceID,
				Included:        false,
				Reason:          "max_items",
				EstimatedTokens: item.EstimatedTokens,
			})
			continue
		}
		if used+item.EstimatedTokens > limit && !item.Pinned {
			prov = append(prov, ContextProvenance{
				SourceID:        item.SourceID,
				Included:        false,
				Reason:          "budget",
				EstimatedTokens: item.EstimatedTokens,
			})
			continue
		}
		used += item.EstimatedTokens
		selected = append(selected, item)
		prov = append(prov, ContextProvenance{
			SourceID:        item.SourceID,
			Included:        true,
			Reason:          "selected",
			EstimatedTokens: item.EstimatedTokens,
		})
	}
	hitTok := make(map[string]int)
	for _, item := range selected {
		hitTok[item.SourceID] += item.EstimatedTokens
	}
	for _, src := range e.sources {
		if t := hitTok[src.ID()]; t > 0 {
			RecordHit(src.ID(), t)
		}
	}
	for _, p := range prov {
		if !p.Included && (p.Reason == "budget" || p.Reason == "max_items") {
			RecordEviction(p.SourceID)
		}
	}
	queryTokens := llm.EstimateText(req.Query)
	projectedContextTokens := used + queryTokens
	contextPressure := contextPressureForInjectedContext(projectedContextTokens, limit)
	evictions := make([]ContextProvenance, 0, len(prov))
	for _, p := range prov {
		if p.Included {
			continue
		}
		if p.Reason != "budget" && p.Reason != "max_items" {
			continue
		}
		evictions = append(evictions, p)
	}

	result := AssemblyResult{
		SessionID:  strings.TrimSpace(req.SessionID),
		Mode:       strings.TrimSpace(req.Mode),
		WorkingSet: normalizedSet(req.PinnedSet),
		Budget: ContextBudget{
			LimitTokens: limit,
			UsedTokens:  used,
			TotalItems:  len(all),
			UsedItems:   len(selected),
		},
		Items:                  selected,
		Provenance:             prov,
		EvictionDetails:        evictions,
		GeneratedAtUTC:         req.Now.UTC().Format(time.RFC3339),
		ProjectedContextTokens: projectedContextTokens,
		ContextPressure:        contextPressure,
	}
	RecordRun(req.SessionID, req.Mode, result)
	return result
}

func contextPressureForInjectedContext(projected, limit int) string {
	if limit <= 0 {
		return "ok"
	}
	blockAt := limit
	warnAt := limit - limit/10
	if warnAt < 1 {
		warnAt = blockAt
	}
	switch {
	case projected >= blockAt:
		return "block"
	case projected >= warnAt:
		return "warn"
	default:
		return "ok"
	}
}

func normalizedSet(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, x := range items {
		v := strings.TrimSpace(x)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
