package assembly

import "strings"

func modeBudget(mode string) int {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "plan":
		return 2200
	default:
		return 1800
	}
}

func ApplyRetrievalPlan(req *AssemblyRequest) {
	if req == nil {
		return
	}
	if req.LimitTokens <= 0 {
		req.LimitTokens = modeBudget(req.Mode)
	}
	if req.MaxItems <= 0 {
		switch strings.ToLower(strings.TrimSpace(req.Mode)) {
		case "plan":
			req.MaxItems = 14
		default:
			req.MaxItems = 18
		}
	}
}

func sourcePriorityBoost(mode string, sourceID string) int {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "plan":
		switch sourceID {
		case "rules_source", "working_set_source":
			return 12
		}
	default:
		switch sourceID {
		case "working_set_source", "rules_source", "turn_diff_source":
			return 8
		}
	}
	return 0
}
