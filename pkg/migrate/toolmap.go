package migrate

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// toolNameMap lists source tool names whose forebrain spelling differs. Names
// absent here keep their source spelling: an mcp__<server>__<tool> call is
// already forebrain's form, and an unknown native tool renders as a plain tool
// card rather than being dropped.
var toolNameMap = map[string]string{
	"Bash":            "shell",
	"Read":            "read_file",
	"Write":           "write_file",
	"Edit":            "edit_file",
	"WebFetch":        "web_fetch",
	"WebSearch":       "web_search",
	"TaskCreate":      "session_todo",
	"TaskUpdate":      "session_todo",
	"TaskList":        "session_todo",
	"AskUserQuestion": "user_interaction",
	"Task":            "intermediate_tool",
	"Agent":           "intermediate_tool",
}

func mapToolName(name string) string {
	name = strings.TrimSpace(name)
	if mapped, ok := toolNameMap[name]; ok {
		return mapped
	}
	return name
}

// buildToolMeta renders the durable tool metadata stored beside a tool call
// or result row. The mapped tool name is what a replay renders; invocation is
// the one-line form of the arguments when one is derivable (a shell command);
// input carries the parsed arguments verbatim so nothing the model did is
// lost. agentID attaches a subagent's rows to its child session view.
func buildToolMeta(sourceName string, arguments json.RawMessage, agentID string) (tool.ToolMeta, string) {
	meta := tool.ToolMeta{
		ToolName: mapToolName(sourceName),
		Status:   "completed",
	}
	name := strings.TrimSpace(sourceName)
	args := strings.TrimSpace(string(arguments))
	if args == "" || args == "null" {
		args = "{}"
	}
	var parsed map[string]any
	if json.Unmarshal([]byte(args), &parsed) == nil && parsed != nil {
		meta.Input = parsed
	}
	// A canonical arguments form, map keys sorted by encoding/json, so the
	// same source call produces byte-identical rows across runs.
	canonical := args
	if parsed != nil {
		if b, err := json.Marshal(parsed); err == nil {
			canonical = string(b)
		}
	}
	meta.Invocation = invocationLine(name, meta.Input)
	if id := strings.TrimSpace(agentID); id != "" {
		meta.AgentID = id
	}
	return meta, canonical
}

// invocationLine renders the one-line description a replay shows under the
// tool name: the shell command for shell tools, the primary path for file
// tools, and the mapped name otherwise.
func invocationLine(sourceName string, input map[string]any) string {
	if input == nil {
		return ""
	}
	name := strings.TrimSpace(sourceName)
	switch name {
	case "Bash":
		if cmd, ok := input["command"].(string); ok && strings.TrimSpace(cmd) != "" {
			return firstLine(cmd)
		}
	case "Read", "Write", "Edit":
		for _, key := range []string{"file_path", "path", "notebook_path"} {
			if v, ok := input[key].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	case "WebFetch":
		if v, ok := input["url"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if len(input) == 0 {
		return ""
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		switch value := input[key].(type) {
		case string:
			parts = append(parts, key+"="+firstLine(value))
		case float64, bool:
			b, _ := json.Marshal(value)
			parts = append(parts, key+"="+string(b))
		default:
			parts = append(parts, key)
		}
	}
	joined := strings.Join(parts, " ")
	if len(joined) > 120 {
		joined = joined[:120] + "…"
	}
	return joined
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		s = s[:idx]
	}
	return s
}

// marshalToolMeta renders the metadata for the tool_meta_json column; the
// empty string means the column stays unset.
func marshalToolMeta(meta tool.ToolMeta) string {
	if strings.TrimSpace(meta.ToolName) == "" {
		return ""
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(b)
}
