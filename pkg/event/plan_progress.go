// Checklist progress, shared by every surface.
package event

import "strings"

// PlanProgress is the checklist state of a run as the worked line reports it:
// how many items are done out of how many, and the one in-flight title the
// line names when work is still moving.
type PlanProgress struct {
	Done   int
	Total  int
	Active string
}

// Facts is the progress as a run-end payload carries it.
func (p PlanProgress) Facts() RunPlanFacts {
	return RunPlanFacts{PlanDone: p.Done, PlanTotal: p.Total, PlanActive: p.Active}
}

// PlanProgressOf folds one plan update into the facts the worked line needs.
// Done and Total come from the payload's own counters; the active title is
// the shortest in-progress item — several can run at once but the line has
// room for one, and the shortest is the most legible stand-in; ties keep the
// first. A checklist with no in-progress item falls back to the payload's own
// summary line.
func PlanProgressOf(items []PlanUpdateItem, completed, total int, fallback string) PlanProgress {
	if completed < 0 {
		completed = 0
	}
	if total < 0 {
		total = 0
	}
	return PlanProgress{
		Done:   completed,
		Total:  total,
		Active: shortestActiveTaskTitle(items, fallback),
	}
}

func shortestActiveTaskTitle(items []PlanUpdateItem, fallback string) string {
	best := ""
	for _, item := range items {
		if strings.TrimSpace(item.Status) != "in_progress" {
			continue
		}
		// The label the plan card itself shows under its header is the
		// item's in-progress label — session_todo's title, carried here as
		// Active; the content is the checklist row. The working line says
		// what is being done, so it uses that label and only falls back to
		// the content.
		title := strings.TrimSpace(item.Active)
		if title == "" {
			title = strings.TrimSpace(item.Content)
		}
		if title == "" {
			continue
		}
		if best == "" || len(title) < len(best) {
			best = title
		}
	}
	if best == "" {
		return strings.TrimSpace(fallback)
	}
	return best
}
