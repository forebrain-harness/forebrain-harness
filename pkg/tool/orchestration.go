// Tool-call orchestration helpers and the intermediate tool.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

const defaultToolConcurrency = 10

type ToolCallPlan struct {
	ConcurrencySafe bool
	ToolNames       []string
}

func MaxToolConcurrencyFromEnv() int {
	raw := strings.TrimSpace(os.Getenv("FOREBRAIN_MAX_TOOL_USE_CONCURRENCY"))
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultToolConcurrency
	}
	return n
}

func PartitionToolCalls(toolNames []string, metas map[string]event.ToolMeta) []ToolCallPlan {
	out := make([]ToolCallPlan, 0, len(toolNames))
	for _, raw := range toolNames {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		meta, ok := metas[name]
		safe := ok && meta.ReadOnly && meta.ConcurrencySafe && !meta.Destructive
		last := len(out) - 1
		if safe && last >= 0 && out[last].ConcurrencySafe {
			out[last].ToolNames = append(out[last].ToolNames, name)
			continue
		}
		out = append(out, ToolCallPlan{
			ConcurrencySafe: safe,
			ToolNames:       []string{name},
		})
	}
	return out
}

func ToolMetaMap(items []event.ToolMeta) map[string]event.ToolMeta {
	out := make(map[string]event.ToolMeta, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		out[name] = item
	}
	return out
}

func (s *State) PartitionToolCalls(toolNames []string) []ToolCallPlan {
	if s == nil {
		return PartitionToolCalls(toolNames, nil)
	}
	return PartitionToolCalls(toolNames, ToolMetaMap(s.ToolMetas()))
}

func (s *State) MarkToolInProgress(id string) {
	if s == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inProgressTools == nil {
		s.inProgressTools = make(map[string]struct{})
	}
	s.inProgressTools[id] = struct{}{}
}

func (s *State) ClearToolInProgress(id string) {
	if s == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inProgressTools, id)
}

func (s *State) InProgressToolIDs() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.inProgressTools))
	for id := range s.inProgressTools {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

const intermediateToolName = "intermediate_tool"

// The description stays lean on usage policy: the system prompt carries the
// "high-frequency working notes" guidance (agentIntermediateToolPromptSuffix
// in pkg/run), so repeating full sentences here would re-bill those bytes in
// the tool table on every cold cache generation for no added guidance. What
// must live here is only what the system prompt does not say: the note
// format, the read-back habit's consequence, and the action semantics.
func IntermediateToolDescription() string {
	return strings.TrimSpace(`Use this tool frequently while understanding code, reviewing changes, investigating bugs, or gathering evidence: save newly discovered facts, important snippets, hypotheses, review findings, design ideas, and your own reasoning as soon as you learn them.

Write notes as Markdown - headings, bullet/numbered lists, fenced code blocks, bold, tables. Prefer short, specific notes over long prose; they append to a per-session notes file readable back later.

Before responding to the user or closing out a task, read the saved notes back so you do not miss key findings or decisions.

- action=append (default): save the Markdown content to the session notes.
- action=read: return all saved notes for this state.
- action=clear: clear the saved notes for this state.`)
}

func intermediateToolSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "append, read, or clear. Default is append.",
				"enum":        []any{"append", "read", "clear"},
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Markdown content to save. Required for action=append. Use Markdown formatting (headings, bullets, fenced code blocks, etc.).",
			},
		},
	}
}

// NewIntermediateTool: stateRoot is the per-agent state root (a workspace root)
// onto which intermediatestore joins "state"; callers pass rt.StateRoot().
func NewIntermediateTool(st *State, stateRoot string) (*llm.Tool, error) {
	return llm.NewRawTool(
		intermediateToolName,
		IntermediateToolDescription(),
		intermediateToolSchema(),
		func(ctx context.Context, arguments string) (any, error) {
			if st == nil {
				return "", fmt.Errorf("nil state")
			}
			if strings.TrimSpace(stateRoot) == "" {
				return "", fmt.Errorf("state root required")
			}
			if err := st.GuardTool(ctx, intermediateToolName); err != nil {
				return "", err
			}
			sid, err := sessionKey(ctx)
			if err != nil {
				return "", err
			}
			var raw map[string]any
			if strings.TrimSpace(arguments) != "" {
				if err := json.Unmarshal([]byte(arguments), &raw); err != nil {
					return "", err
				}
			}
			action := "append"
			if raw != nil {
				if v, ok := raw["action"].(string); ok && strings.TrimSpace(v) != "" {
					action = strings.ToLower(strings.TrimSpace(v))
				}
			}
			switch action {
			case "read":
				content, err := state.Read(stateRoot, sid)
				if err != nil {
					return "", err
				}
				return map[string]any{
					"ok":      true,
					"action":  "read",
					"path":    state.Path(stateRoot, sid),
					"content": content,
				}, nil
			case "clear":
				if err := state.Clear(stateRoot, sid); err != nil {
					return "", err
				}
				return map[string]any{
					"ok":     true,
					"action": "clear",
					"path":   state.Path(stateRoot, sid),
				}, nil
			case "append":
				text := ""
				if raw != nil {
					if s, ok := raw["content"].(string); ok {
						text = strings.TrimSpace(s)
					}
				}
				if text == "" {
					return "", fmt.Errorf("content is required for append")
				}
				if err := state.Append(stateRoot, sid, text); err != nil {
					return "", err
				}
				return map[string]any{
					"ok":     true,
					"action": "append",
					"path":   state.Path(stateRoot, sid),
					// Echoed back so the TUI can render the note when the input
					// payload is unavailable (see tool.formatIntermediate).
					"content": text,
					"message": "Intermediate notes saved. Continue the investigation and keep recording important findings.",
				}, nil
			default:
				return "", fmt.Errorf("unknown action %q", action)
			}
		},
	)
}
