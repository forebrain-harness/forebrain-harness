package migrate

import (
	"encoding/json"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/stretchr/testify/require"
)

// TestMapToolNameTaskAndAgentAreSubagents pins the identity of the subagent
// dispatch both source clients hand work to: Claude Code's Task and Codex's
// Agent start a subagent, so an import maps them to subagent_run — never to the
// notes tool, which would give a dispatch a notes card's words. The header is
// recomputed at replay time from the stored meta, so the pin goes through the
// real imported meta and the engine's own wording, not the lookup alone.
func TestMapToolNameTaskAndAgentAreSubagents(t *testing.T) {
	require.Equal(t, "subagent_run", mapToolName("Task"))
	require.Equal(t, "subagent_run", mapToolName("Agent"))

	for _, source := range []string{"Task", "Agent"} {
		meta, _ := buildToolMeta(source,
			json.RawMessage(`{"description":"probe","prompt":"count the cards"}`), "")
		require.Equal(t, "subagent_run", meta.ToolName, source)
		// The generic card the engine draws for a tool it has no words for
		// names what happened and the tool: the dispatch reads as a call.
		require.Equal(t, "Called subagent_run",
			tool.DisplayHeader(tool.DisplayFrame{Title: meta.ToolName, Meta: meta, Final: true}, ""), source)
	}
}
