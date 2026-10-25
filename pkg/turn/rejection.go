package turn

import (
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// ResumeRejectedStop is the resume state for a gate the user refused without
// words: the refusal is recorded on the parked call and the turn ends there.
// The orchestration consumes this state like any other approval resume —
// same fence, same snapshot — but writes the refusal results and returns
// run.ErrTurnStoppedByRejection instead of asking the model, so the surface
// that dispatched it finishes the run the way a user cancel does.
func ResumeRejectedStop(snapshot []llm.Message) *tool.ToolApprovalResumeState {
	return &tool.ToolApprovalResumeState{
		Session: append([]llm.Message(nil), snapshot...),
		Stopped: true,
	}
}
