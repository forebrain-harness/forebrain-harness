package tui

// The plan-review picker's filter: the exit_plan_mode approval panel chooses a
// reviewer — or a hand-off executor — from the same model list /model shows, and
// a list long enough to scan by eye carries the same filter bar. This file owns
// which models that filter leaves, in which order, and whether the list is long
// enough to need one at all; the bar's drawing, its keys and its caret live with
// the rest of the overlay in overlays.go.

import (
	"sort"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// planReviewShowsFilter reports whether the reviewer list is long enough to need
// a filter: the same threshold every picker in this terminal uses, counted over
// the rows the picker opens with so the bar does not vanish as the list narrows.
func (o *approvalOverlay) planReviewShowsFilter() bool {
	return pickerShowsFilter(len(o.req.PlanReviewModels))
}

// applyPlanReviewFilter narrows the list to the models whose shown name — or
// endpoint — contains the filter, best match first, and re-anchors the cursor on
// the first match, the way every other picker re-anchors after a filter change.
func (o *approvalOverlay) applyPlanReviewFilter() {
	query := strings.ToLower(strings.TrimSpace(o.planReviewFilter.text))
	o.planReviewFiltered = o.planReviewFiltered[:0]
	ranks := make(map[int]int, len(o.req.PlanReviewModels))
	for i, model := range o.req.PlanReviewModels {
		name := planReviewModelDisplay(model.Provider, model.Model)
		if rank := pickerMatchRank(name, model.Endpoint, query); rank >= 0 {
			o.planReviewFiltered = append(o.planReviewFiltered, i)
			ranks[i] = rank
		}
	}
	sort.SliceStable(o.planReviewFiltered, func(a, b int) bool {
		return ranks[o.planReviewFiltered[a]] < ranks[o.planReviewFiltered[b]]
	})
	o.planReviewCursor = 0
}

// planReviewVisible returns the models the filter leaves, in the order the list
// draws them.
func (o *approvalOverlay) planReviewVisible() []turn.PlanReviewModelOption {
	out := make([]turn.PlanReviewModelOption, 0, len(o.planReviewFiltered))
	for _, i := range o.planReviewFiltered {
		out = append(out, o.req.PlanReviewModels[i])
	}
	return out
}
