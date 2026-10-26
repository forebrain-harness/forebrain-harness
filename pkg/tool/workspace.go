// Workspace-scoped tools: turn diffs.
package tool

import (
	"time"

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
	// One call == one tool just finished changing this path: the tool-write
	// ledger a concurrent shell card subtracts from its own window (plan 180).
	noteToolFileWrite(absPath, time.Now())
}
