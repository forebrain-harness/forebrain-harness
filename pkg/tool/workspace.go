// Workspace-scoped tools: turn diffs.
package tool

import (
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

func attachTurnDiff(output map[string]any, absPath string, before, after []byte) {
	if output == nil {
		return
	}
	summary, err := event.Build(absPath, before, after)
	if err != nil {
		return
	}
	output["turn_diff"] = summary
}
