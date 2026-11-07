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
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
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

// CallConcurrencySafe reports whether this call, with these arguments, may run
// alongside other concurrency-safe calls. A tool whose metadata says so always
// may; shell may when its command is provably read-only and it asks for no
// sandbox override. Arguments that do not parse never may.
func (s *State) CallConcurrencySafe(call llm.ToolCall) bool {
	name := strings.TrimSpace(call.Function.Name)
	if name == "" {
		return false
	}
	if s != nil {
		if meta, ok := ToolMetaMap(s.ToolMetas())[name]; ok &&
			meta.ReadOnly && meta.ConcurrencySafe && !meta.Destructive {
			return true
		}
	}
	if name != "shell" {
		return false
	}
	var in struct {
		Command               string          `json:"command"`
		SandboxPermissions    string          `json:"sandbox_permissions"`
		AdditionalPermissions json.RawMessage `json:"additional_permissions"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &in); err != nil {
		return false
	}
	if strings.TrimSpace(in.SandboxPermissions) != "" && strings.TrimSpace(in.SandboxPermissions) != "use_default" {
		return false
	}
	additional := strings.TrimSpace(string(in.AdditionalPermissions))
	if additional != "" && additional != "null" && additional != "{}" {
		return false
	}
	return safety.ShellCommandIsKnownSafe(in.Command)
}

// PartitionToolCallsFor is PartitionToolCalls by call: adjacent calls that are
// each CallConcurrencySafe form one group.
func (s *State) PartitionToolCallsFor(calls []llm.ToolCall) []ToolCallPlan {
	out := make([]ToolCallPlan, 0, len(calls))
	for _, call := range calls {
		name := strings.TrimSpace(call.Function.Name)
		if name == "" {
			continue
		}
		safe := s.CallConcurrencySafe(call)
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

// The description is where the usage policy lives: every agent that has the
// tool reads it, subagents included, which no system-prompt text reaches. The
// coding system prompt therefore says nothing more about notes; office and
// batch add only their document-flavoured suffix.
func IntermediateToolDescription() string {
	return strings.TrimSpace(`Session working notes. Use it often while reading code, reviewing, investigating or gathering evidence: append short, specific Markdown notes (headings, lists, fenced code, tables) of facts, snippets, hypotheses, findings and reasoning as you learn them. Before responding to the user or finishing, read them back so nothing is missed. action=append (default) saves content, read returns all notes, clear empties them.`)
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
				"description": "Markdown to save; required for append.",
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
