package tui

import (
	"testing"
)

// V2: inside the agent create form, create a nested subagent entry (which is
// refused today), leave its form, then edit a field of the agent form.
func TestZZReproNestedThenEdit(t *testing.T) {
	session, stored := newReproSession(t)
	selector := &scriptedSelector{}
	renderer := runConfigPanelOn(t, session, selector, []richStep{
		{label: "Agent definitions", ok: true},
		{label: configAddRowLabel, ok: true},
		{label: "Name", ok: true},
		{label: "Subagents", ok: true},
		{label: configAddRowLabel, ok: true},
		{label: "Name", ok: true},
		{label: configCreateSaveLabel, ok: true}, // refused today
		{ok: false},                             // Esc out of the subagent form
		{ok: false},                             // Esc out of the subagents list
		{label: "Model", ok: true},
		{label: "deepseek/deepseek-flash", ok: true},
		{ok: false},
		{label: configCreateSaveLabel, ok: true},
		{ok: false},
		{ok: false},
		{ok: false},
	}, []selectorInput{
		{value: "frontend-expert", ok: true},
		{value: "helper", ok: true},
	}, "agents")

	t.Logf("pages:\n%s", pageLabels(selector))
	t.Logf("frames:\n%s", frameText(renderer))
	t.Logf("stored agents: %s", stored())
}