// Session-scoped tools: todos, working set, and memory tools.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
)

func registerSessionAndMemoryTools(a ToolAdder, st *State, rt *AgentToolRuntime) error {
	if a == nil || st == nil {
		return nil
	}
	if rt != nil {
		stateRoot := rt.StateRoot()
		if strings.TrimSpace(stateRoot) == "" {
			return registerProductTools(a, st, rt)
		}
		t1, err := newSessionTodoTool(st, stateRoot)
		if err != nil {
			return err
		}
		projectKey := strings.TrimSpace(rt.ProjectKey)
		t3, err := newEnterPlanModeTool(st, stateRoot, projectKey)
		if err != nil {
			return err
		}
		t3b, err := newExitPlanModeTool(st, stateRoot, projectKey)
		if err != nil {
			return err
		}
		t9, err := newWorkingSetShowTool(st)
		if err != nil {
			return err
		}
		t10, err := newWorkingSetPinTool(st)
		if err != nil {
			return err
		}
		t11, err := newWorkingSetDropTool(st)
		if err != nil {
			return err
		}
		t12, err := NewIntermediateTool(st, stateRoot)
		if err != nil {
			return err
		}
		registerToolMeta(st, t1, "Use when tracking step-by-step execution progress: a structured checklist with per-item status, rewritten in full on every update (for the prose strategy doc write/edit the plan file with write_file/edit_file)", "session", event.ToolMeta{})
		registerToolMeta(st, t3, "Use when switching the session into plan mode before implementing a non-trivial task", "session", event.ToolMeta{ReadOnly: true})
		registerToolMeta(st, t3b, "Use when the plan is written and you're ready to leave plan mode for approval", "session", event.ToolMeta{ReadOnly: true})
		registerToolMeta(st, t9, "Use when reviewing which entries are currently pinned", "context", event.ToolMeta{ReadOnly: true, ConcurrencySafe: true})
		registerToolMeta(st, t10, "Use when keeping a file or reference in context across rebuilds", "context", event.ToolMeta{})
		registerToolMeta(st, t11, "Use when freeing context by unpinning an entry", "context", event.ToolMeta{})
		registerToolMeta(st, t12, "Use when investigating or reviewing: save, read, or clear session-scoped working notes to synthesize later", "session", event.ToolMeta{})
		_ = st.Register(a, t1)
		_ = st.Register(a, t3)
		_ = st.Register(a, t3b)
		_ = st.Register(a, t9)
		_ = st.Register(a, t10)
		_ = st.Register(a, t11)
		_ = st.Register(a, t12)
	}
	if rt != nil && memory.DedicatedToolsEnabled(rt.Cfg) {
		tools, err := newDedicatedMemoryTools(st, rt)
		if err != nil {
			return err
		}
		// The three readers are pure reads over the memories store: without
		// metadata the scheduler cannot see that and serializes every call.
		memoryMeta := map[string]struct {
			desc  string
			flags event.ToolMeta
		}{
			"memories_list":            {"Use when listing what memory files exist", event.ToolMeta{ReadOnly: true, ConcurrencySafe: true}},
			"memories_read":            {"Use when reading a specific memory file", event.ToolMeta{ReadOnly: true, ConcurrencySafe: true}},
			"memories_search":          {"Use when searching memories for a substring", event.ToolMeta{ReadOnly: true, ConcurrencySafe: true}},
			"memories_add_ad_hoc_note": {"Use when the user asks Forebrain Harness to remember, forget, or update something, or states a rule that must outlive this session", event.ToolMeta{}},
		}
		for _, t := range tools {
			if meta, ok := memoryMeta[t.Name()]; ok {
				registerToolMeta(st, t, meta.desc, "memory", meta.flags)
			}
			_ = st.Register(a, t)
		}
	}
	return registerProductTools(a, st, rt)
}

func sessionKey(ctx context.Context) (string, error) {
	sid := llm.AgentSessionIDFromContext(ctx)
	if sid == "" {
		return "", fmt.Errorf("agent session not bound")
	}
	return sid, nil
}

type sessionTodoItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status" jsonschema:"enum=pending,enum=in_progress,enum=completed,enum=cancelled"`
	Title   string `json:"title,omitempty" jsonschema_description:"Short present-tense label shown while this row is in progress, e.g. 'Writing tests'. Defaults to content."`
}

// UnmarshalJSON accepts the title under its former name, active_form, so a
// model that still sends it is understood.
func (it *sessionTodoItem) UnmarshalJSON(b []byte) error {
	type plain sessionTodoItem
	aux := struct {
		*plain
		LegacyActiveForm string `json:"active_form"`
	}{plain: (*plain)(it)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	if it.Title == "" {
		it.Title = aux.LegacyActiveForm
	}
	return nil
}

// Both fields are optional on the wire: the handler reads the checklist back
// when action is empty, and a list/clear call carries no rows. They are tagged
// `omitempty` so the reflected schema says so too — a required `items` pushes
// the model into sending a placeholder array on a read.
type sessionTodoInput struct {
	Action string            `json:"action,omitempty" jsonschema:"enum=set,enum=clear,enum=list" jsonschema_description:"set to write the checklist (items becomes the whole list, replacing whatever was stored), clear to empty it, list to read it back. Omit when sending rows; any call that carries rows writes the list."`
	Items  []sessionTodoItem `json:"items,omitempty" jsonschema_description:"The complete checklist after this update: rows with id, content, status (pending|in_progress|completed|cancelled). This array replaces the stored list, so resend every row that still applies; a row you leave out is deleted, which is how a superseded or obsolete item is dropped."`
}

func normalizeSessionTodoItems(in sessionTodoInput) []sessionTodoItem {
	items := in.Items
	out := make([]sessionTodoItem, 0, len(items))
	for _, it := range items {
		id := strings.TrimSpace(it.ID)
		if id == "" {
			continue
		}
		status := strings.TrimSpace(it.Status)
		if status == "" {
			status = string(state.StatusPending)
		}
		out = append(out, sessionTodoItem{
			ID:      id,
			Content: it.Content,
			Status:  status,
			Title:   strings.TrimSpace(it.Title),
		})
	}
	return out
}

// newSessionTodoTool: stateRoot is the per-agent state root (a workspace root)
// onto which todostore joins "state"; callers pass rt.StateRoot(), not rt.Home.
func newSessionTodoTool(st *State, stateRoot string) (*llm.Tool, error) {
	return llm.NewTool(
		"session_todo",
		"Use when tracking step-by-step execution progress: a structured checklist of items with per-item status (pending/in_progress/completed/cancelled) that drives the progress UI. Each write carries the entire checklist and replaces the stored one, so send every row that still applies and leave out the rows that no longer do — rewriting the plan means sending the new list, not adding to the old one. Exactly one row should be in_progress at a time. For the prose strategy document, write/edit the plan file directly with write_file/edit_file. Session is fixed by the runtime; do not try to access other sessions.",
		func(ctx context.Context, in *sessionTodoInput) (string, error) {
			if st == nil {
				return "", fmt.Errorf("nil state")
			}
			if in == nil {
				in = &sessionTodoInput{}
			}
			sid, err := sessionKey(ctx)
			if err != nil {
				return "", err
			}
			act := strings.ToLower(strings.TrimSpace(in.Action))
			// A read/list never carries rows. If rows were supplied the caller
			// intends to write the checklist, regardless of the action label —
			// the observed failing call sent the full list with action "list".
			// Treat any payload-bearing call as a write so the checklist is
			// never silently dropped. Explicit "clear" still wins.
			if act != "clear" && len(in.Items) > 0 {
				act = "set"
			}
			switch act {
			case "", "list":
				l, err := state.Load(stateRoot, sid)
				if err != nil {
					return "", err
				}
				emitPlanUpdateStep(ctx, st, l)
				b, _ := json.MarshalIndent(l, "", "  ")
				return string(b), nil
			case "clear":
				l := state.List{Items: []state.Item{}}
				if err := state.Save(stateRoot, sid, l); err != nil {
					return "", err
				}
				emitPlanUpdateStep(ctx, st, l)
				return `{"ok":true,"cleared":true}`, nil
			case "set":
				normalized := normalizeSessionTodoItems(*in)
				items := make([]state.Item, 0, len(normalized))
				for _, it := range normalized {
					st := state.TodoStatus(it.Status)
					if st == "" {
						st = state.StatusPending
					}
					items = append(items, state.Item{
						ID:      it.ID,
						Content: it.Content,
						Status:  st,
						Title:   it.Title,
					})
				}
				l, err := state.Replace(stateRoot, sid, items)
				if err != nil {
					return "", err
				}
				emitPlanUpdateStep(ctx, st, l)
				b, _ := json.MarshalIndent(l, "", "  ")
				return string(b), nil
			default:
				return "", fmt.Errorf("unknown action %q", in.Action)
			}
		},
	)
}

// singleLineText normalizes a model-written label to the single line its field
// semantically is: runs of any whitespace (spaces, tabs, newlines) collapse to
// one space. A title like "step\tone" or a content carrying a literal
// newline is not two rows of a plan — it is one label written carelessly — and
// letting the raw bytes reach the terminal breaks every row painted below it.
func singleLineText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func emitPlanUpdateStep(ctx context.Context, st *State, l state.List) {
	if st == nil {
		return
	}
	step := StepHookFromContext(ctx, st)
	if step == nil {
		return
	}
	payload := event.PlanUpdatedPayload{
		Title:     "Updated Plan",
		Items:     make([]event.PlanUpdateItem, 0, len(l.Items)),
		Completed: countTodoStatus(l.Items, state.StatusCompleted),
		Total:     countNonEmptyTodos(l.Items),
		// A subagent keeps its own todo list, so the card belongs in that
		// subagent's view rather than the conversation the parent is having.
		AgentID: strings.TrimSpace(HookAgentIDFromContext(ctx)),
	}
	activeLabels := make([]string, 0, len(l.Items))
	for _, item := range l.Items {
		content := singleLineText(item.Content)
		if content == "" {
			continue
		}
		active := singleLineText(item.Title)
		if active == "" && item.Status == state.StatusInProgress {
			active = content
		}
		if active != "" && item.Status == state.StatusInProgress {
			activeLabels = append(activeLabels, active)
		}
		payload.Items = append(payload.Items, event.PlanUpdateItem{
			ID:      strings.TrimSpace(item.ID),
			Content: content,
			Status:  strings.TrimSpace(string(item.Status)),
			Active:  active,
		})
	}
	switch len(activeLabels) {
	case 0:
		payload.Explanation = ""
	case 1:
		payload.Explanation = activeLabels[0]
	default:
		payload.Explanation = strings.Join(activeLabels, " · ")
	}
	step(ctx, StepEvent{
		Kind:       event.RunEventPlanUpdated,
		StepID:     "plan-update-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		ToolName:   "session_todo",
		PlanUpdate: &payload,
		Output:     map[string]any{"title": payload.Title, "items": payload.Items},
	})
}

func countTodoStatus(items []state.Item, want state.TodoStatus) int {
	count := 0
	for _, item := range items {
		if strings.TrimSpace(item.Content) == "" {
			continue
		}
		if item.Status == want {
			count++
		}
	}
	return count
}

func countNonEmptyTodos(items []state.Item) int {
	count := 0
	for _, item := range items {
		if strings.TrimSpace(item.Content) != "" {
			count++
		}
	}
	return count
}

type enterPlanModeInput struct {
	Reason string `json:"reason,omitempty" jsonschema:"description=Optional one-line rationale for entering plan mode (logged in approval audit trail)."`
}

const enterPlanModeDescription = `Use this tool proactively when you're about to start a non-trivial implementation task. Getting user sign-off on your approach before writing code prevents wasted effort and ensures alignment. This tool transitions you into plan mode where you can explore the codebase and design an implementation approach for user approval.

## When to Use This Tool

**Prefer using EnterPlanMode** for implementation tasks unless they're simple. Use it when ANY of these conditions apply:

1. **New Feature Implementation**: Adding meaningful new functionality
   - Example: "Add a logout button" - where should it go? What should happen on click?
   - Example: "Add form validation" - what rules? What error messages?

2. **Multiple Valid Approaches**: The task can be solved in several different ways
   - Example: "Add caching to the API" - could use Redis, in-memory, file-based, etc.
   - Example: "Improve performance" - many optimization strategies possible

3. **Code Modifications**: Changes that affect existing behavior or structure
   - Example: "Update the login flow" - what exactly should change?
   - Example: "Refactor this component" - what's the target architecture?

4. **Architectural Decisions**: The task requires choosing between patterns or technologies
   - Example: "Add real-time updates" - WebSockets vs SSE vs polling
   - Example: "Implement state management" - Redux vs Context vs custom solution

5. **Multi-File Changes**: The task will likely touch more than 2-3 files
   - Example: "Refactor the authentication system"
   - Example: "Add a new API endpoint with tests"

6. **Unclear Requirements**: You need to explore before understanding the full scope
   - Example: "Make the app faster" - need to profile and identify bottlenecks
   - Example: "Fix the bug in checkout" - need to investigate root cause

7. **User Preferences Matter**: The implementation could reasonably go multiple ways
   - If you would use user_interaction to clarify the approach, use EnterPlanMode instead
   - Plan mode lets you explore first, then present options with context

## When NOT to Use This Tool

Only skip EnterPlanMode for simple tasks:
- Single-line or few-line fixes (typos, obvious bugs, small tweaks)
- Adding a single function with clear requirements
- Tasks where the user has given very specific, detailed instructions
- Pure research/exploration tasks (use subagent_run with subtype=explore instead)

## What Happens in Plan Mode

In plan mode, you'll:
1. Thoroughly explore the codebase using code_search, read_file, and shell when needed
2. Use subagent_run with subtype=explore (or subagent_fanout) for parallel research
3. Understand existing patterns and architecture
4. Design an implementation approach
5. Present your plan to the user for approval via user_interaction when you need to clarify approaches
6. Write the plan to the session plan file (the only file you may edit in plan mode)
7. Exit plan mode with exit_plan_mode when ready to implement

## Important Notes

- This tool REQUIRES user approval - they must consent to entering plan mode
- If unsure whether to use it, err on the side of planning - it's better to get alignment upfront than to redo work
- Users appreciate being consulted before significant changes are made to their codebase
- Once in plan mode, write/edit attempts on non-plan files are blocked. Only the session plan file is writable.`

const exitPlanModeDescription = `Use this tool when you are in plan mode and have finished writing your plan to the session plan file and are ready for user approval.

## How This Tool Works

- You should have already written your plan to the session plan file (the path is returned by enter_plan_mode and surfaced in the per-turn plan-mode system reminder)
- This tool does NOT take the plan content as a parameter - it will use the plan you already wrote
- This tool signals that you're done planning and ready for the user to review and approve

## When to Use This Tool

IMPORTANT: Only use this tool when the task requires planning the implementation steps of a task that requires writing code. For research tasks where you're gathering information, searching files, reading files or in general trying to understand the codebase - do NOT use this tool.

## Before Using This Tool

Ensure your plan is complete and unambiguous:
- If you have unresolved questions about requirements or approach, use user_interaction first (in earlier phases)
- Once your plan is finalized, use THIS tool to request approval

**Important:** Do NOT use user_interaction to ask "Is this plan okay?" or "Should I proceed?" - that's exactly what THIS tool does. exit_plan_mode inherently requests user approval of your plan.`

// NewPlanModeTools returns the enter/exit plan-mode tools as a pair. Exposed
// for cross-package wiring and integration tests; production registration
// happens via registerSessionAndMemoryTools.
func NewPlanModeTools(st *State, stateRoot string) (enter, exit *llm.Tool, err error) {
	enter, err = newEnterPlanModeTool(st, stateRoot, "")
	if err != nil {
		return nil, nil, err
	}
	exit, err = newExitPlanModeTool(st, stateRoot, "")
	if err != nil {
		return nil, nil, err
	}
	return enter, exit, nil
}

func newEnterPlanModeTool(st *State, stateRoot string, projectKeys ...string) (*llm.Tool, error) {
	projectKey := ""
	if len(projectKeys) > 0 {
		projectKey = strings.TrimSpace(projectKeys[0])
	}
	return llm.NewTool(
		"enter_plan_mode",
		enterPlanModeDescription,
		func(ctx context.Context, in *enterPlanModeInput) (string, error) {
			if st == nil {
				return "", fmt.Errorf("nil state")
			}
			if in == nil {
				in = &enterPlanModeInput{}
			}
			sid, err := sessionKey(ctx)
			if err != nil {
				return "", err
			}
			if hook := st.ActionHook(); hook != nil {
				if ApprovedActionIDFromContext(ctx) == "" {
					id, ok, err := hook(ctx, "enter_plan_mode", map[string]any{
						"session_id": sid,
						"action":     "enter",
						"reason":     strings.TrimSpace(in.Reason),
					})
					if err != nil {
						return "", err
					}
					if ok && id != "" {
						return "", &RequiresActionError{
							ActionID:   id,
							ActionKind: "enter_plan_mode",
							ToolName:   "enter_plan_mode",
							ToolInput:  in,
						}
					}
				}
			}
			prev, _ := state.Get(stateRoot, sid)
			// Detect re-entry: the session previously left plan mode and is coming
			// back. Guide the LLM to evaluate the existing plan first. Read it
			// before the switch, which clears the flag.
			isReentry := prev.HasExitedPlan
			// state.Switch is the one definition of the transition: it stashes
			// the mode being left for exit_plan_mode to restore and clears the
			// exited flag, matching what a mode switch from the UI does.
			if _, err := state.Switch(stateRoot, sid, state.ModePlan); err != nil {
				return "", err
			}
			planDir := state.PlanDirForProject(stateRoot, projectKey)
			st.SetRuntimeSessionMode(ctx, sid, string(state.ModePlan), planDir)
			// Check whether a plan file already exists so we can guide the LLM
			// to create a NEW descriptive-named file instead of overwriting the
			// generic plan.md fallback.
			existingContent, _ := state.GetPlanForProject(stateRoot, projectKey)
			planExists := strings.TrimSpace(existingContent) != ""
			respPlanFile := ""
			nextSteps := "Explore the codebase, design the approach, then create a NEW <descriptive-name>.md plan file inside plan_dir (choose a name based on the task, e.g. 'add-auth-validation.md'). Do NOT use the generic name plan.md - it overwrites previous plans and loses history. Then call exit_plan_mode."
			if planExists {
				respPlanFile = state.PlanPathForProject(stateRoot, projectKey)
				if isReentry {
					// Re-entering plan mode with an existing plan: guide the LLM
					// to read and evaluate the existing plan first, then decide
					// whether to continue refining it or start fresh.
					nextSteps = "Re-entering plan mode. FIRST read the existing plan file (plan_file) to understand what was previously planned. Evaluate the user's current request against that plan: if it's a different task, overwrite the plan with a new one; if it's a continuation, refine the existing plan. Then call exit_plan_mode."
				} else {
					nextSteps = "Explore the codebase, design the approach, write the plan to a new .md file inside plan_dir (or edit plan_file), then call exit_plan_mode."
				}
			}
			b, _ := json.Marshal(map[string]any{
				"mode":       "plan",
				"plan_file":  respPlanFile,
				"reason":     strings.TrimSpace(in.Reason),
				"next_steps": nextSteps,
			})
			return string(b), nil
		},
	)
}

func newExitPlanModeTool(st *State, stateRoot string, projectKeys ...string) (*llm.Tool, error) {
	projectKey := ""
	if len(projectKeys) > 0 {
		projectKey = strings.TrimSpace(projectKeys[0])
	}
	return llm.NewTool(
		"exit_plan_mode",
		exitPlanModeDescription,
		func(ctx context.Context, in *struct{}) (string, error) {
			if st == nil {
				return "", fmt.Errorf("nil state")
			}
			sid, err := sessionKey(ctx)
			if err != nil {
				return "", err
			}
			hook := st.ActionHook()
			if hook == nil {
				return "", fmt.Errorf("exit_plan_mode approval unavailable")
			}
			id, ok, err := hook(ctx, "exit_plan_mode", map[string]any{
				"session_id":          sid,
				"action":              "exit",
				"force_tool_approval": true,
				"approval_reason":     "exit_plan_mode_requires_user_approval",
			})
			if err != nil {
				return "", err
			}
			if ok && id != "" {
				return "", &RequiresActionError{
					ActionID:   id,
					ActionKind: "exit_plan_mode",
					ToolName:   "exit_plan_mode",
					ToolInput:  nil,
				}
			}
			if strings.TrimSpace(id) == "" {
				return "", fmt.Errorf("exit_plan_mode approval unavailable")
			}
			prev, _ := state.Get(stateRoot, sid)
			restored := prev.PrePlanMode
			if strings.TrimSpace(string(restored)) == "" {
				restored = state.ModeAgent
			}
			// Switch records the exit so the next enter_plan_mode detects a
			// re-entry — but only when this actually left plan mode, so calling
			// exit_plan_mode outside plan mode no longer fakes one.
			if _, err := state.Switch(stateRoot, sid, restored); err != nil {
				return "", err
			}
			planDir := state.PlanDirForProject(stateRoot, projectKey)
			// Keep the plan directory writable in the restored mode so the agent
			// can close the loop by recording the implementation status in the
			// plan file once the work is done.
			st.SetRuntimeSessionMode(ctx, sid, string(restored), planDir)
			planFile := state.PlanPathForProject(stateRoot, projectKey)
			// Keep the LLM-facing payload minimal: only the restored mode, the
			// plan file reference, and a human-readable confirmation message.
			b, _ := json.Marshal(map[string]any{
				"mode":      string(restored),
				"plan_file": planFile,
				"message":   "Exited plan mode. You can now make edits, run tools, and take actions. Implement the approved plan in " + planFile + ".",
				"on_completion": "When the plan's tasks are done (or you stop working on it), you MUST update " + planFile +
					" before reporting back: tick off the completed tasks and append a final '" + state.ImplementationHeading +
					"' section summarizing what actually landed, any deviations from the plan, what was left out, and how it was verified. The plan file is writable in this mode. This is not optional - a plan is only complete once its implementation status is recorded in it.",
			})
			return string(b), nil
		},
	)
}

type workingSetShowInput struct{}

func newWorkingSetShowTool(st *State) (*llm.Tool, error) {
	return llm.NewTool(
		"working_set_show",
		"Use when reviewing which entries are currently pinned in the session working set.",
		func(ctx context.Context, _ *workingSetShowInput) (string, error) {
			if st == nil {
				return "", fmt.Errorf("nil state")
			}
			sid, err := sessionKey(ctx)
			if err != nil {
				return "", err
			}
			pins := st.WorkingSetPins(sid)
			b, _ := json.MarshalIndent(map[string]any{
				"session_id": sid,
				"pins":       pins,
				"count":      len(pins),
			}, "", "  ")
			return string(b), nil
		},
	)
}

type workingSetPinInput struct {
	Entries []string `json:"entries" jsonschema:"minItems=1" jsonschema_description:"Paths or references to pin into working set."`
}

func newWorkingSetPinTool(st *State) (*llm.Tool, error) {
	return llm.NewTool(
		"working_set_pin",
		"Use when keeping a file or reference in context across rebuilds: pins it into the session working set.",
		func(ctx context.Context, in *workingSetPinInput) (string, error) {
			if st == nil {
				return "", fmt.Errorf("nil state")
			}
			if in == nil {
				in = &workingSetPinInput{}
			}
			sid, err := sessionKey(ctx)
			if err != nil {
				return "", err
			}
			next := st.UpsertWorkingSetPins(sid, in.Entries, nil)
			b, _ := json.MarshalIndent(map[string]any{
				"session_id": sid,
				"pins":       next,
				"count":      len(next),
			}, "", "  ")
			return string(b), nil
		},
	)
}

type workingSetDropInput struct {
	Entries []string `json:"entries" jsonschema:"minItems=1" jsonschema_description:"Paths or references to remove from working set."`
}

func newWorkingSetDropTool(st *State) (*llm.Tool, error) {
	return llm.NewTool(
		"working_set_drop",
		"Use when freeing context by unpinning a file or reference from the session working set.",
		func(ctx context.Context, in *workingSetDropInput) (string, error) {
			if st == nil {
				return "", fmt.Errorf("nil state")
			}
			if in == nil {
				in = &workingSetDropInput{}
			}
			sid, err := sessionKey(ctx)
			if err != nil {
				return "", err
			}
			next := st.UpsertWorkingSetPins(sid, nil, in.Entries)
			b, _ := json.MarshalIndent(map[string]any{
				"session_id": sid,
				"pins":       next,
				"count":      len(next),
			}, "", "  ")
			return string(b), nil
		},
	)
}

type memoriesAddAdHocNoteInput struct {
	Filename string  `json:"filename" jsonschema:"minLength=24,maxLength=128,pattern=^\\d{4}-\\d{2}-\\d{2}T\\d{2}-\\d{2}-\\d{2}-[a-z0-9][a-z0-9-]{0\\,79}\\.md$,description=Name of the note file to create in YYYY-MM-DDTHH-MM-SS-<slug>.md format."`
	Note     string  `json:"note" jsonschema:"minLength=1,description=Verbatim Markdown note to append to the ad-hoc memory notes."`
	Scope    *string `json:"scope,omitempty" jsonschema:"enum=project,enum=global" jsonschema_description:"Which memory this note belongs to. 'project' (default) is specific to the current project and is what almost every note should use. Use 'global' only for a preference that should apply in every project (tone, collaboration style) — never for project facts, repo conventions, or commands."`
}

// The optional fields below carry `json:",omitempty"` so the reflected schema
// leaves them out of `required` and the model may omit them. Without it every
// property is reflected as required, which forces the model to invent a value
// for parameters it does not want — most visibly `cursor`, whose only plausible
// "no cursor" string is `""`, which the backend then rejects as not a
// non-negative integer.
type memoriesListInput struct {
	Path       *string `json:"path,omitempty" jsonschema_description:"Directory or file path relative to the memories root, e.g. extensions/ad_hoc/notes for the ad-hoc notes. Omit to list the root."`
	Cursor     *string `json:"cursor,omitempty" jsonschema_description:"Verbatim next_cursor from a previous memories_list response. Omit to start at the first page."`
	MaxResults *int    `json:"max_results,omitempty" jsonschema:"minimum=1" jsonschema_description:"Maximum entries to return. Omit for the default page size."`
}

type memoriesReadInput struct {
	Path       string `json:"path" jsonschema_description:"File path relative to the memories root, e.g. extensions/ad_hoc/notes/<filename>.md for an ad-hoc note."`
	LineOffset *int   `json:"line_offset,omitempty" jsonschema:"minimum=1" jsonschema_description:"1-indexed line to start reading from. Omit to read from the first line."`
	MaxLines   *int   `json:"max_lines,omitempty" jsonschema:"minimum=1" jsonschema_description:"Maximum lines to return. Omit to read to the end of the file."`
}

type memoriesSearchInput struct {
	Queries []string `json:"queries" jsonschema:"minItems=1" jsonschema_description:"Words to search memories for. Each is split into terms, and a memory is recalled when it shares one."`
	Path    *string  `json:"path,omitempty" jsonschema_description:"Directory or file path relative to the memories root to restrict the search to, e.g. extensions/ad_hoc/notes for the ad-hoc notes. Omit to search all memory."`
}

// addAdHocNoteResponse hands the model back the path the note actually landed
// on. The tool takes a bare filename but the note is stored under
// extensions/ad_hoc/notes/, so without this the model reads back the filename
// it passed in and memories_read reports "was not found".
type addAdHocNoteResponse struct {
	Path string `json:"path"`
}

func newDedicatedMemoryTools(_ *State, runtime *AgentToolRuntime) ([]*llm.Tool, error) {
	backend, err := memoryBackend(runtime)
	if err != nil {
		return nil, err
	}
	add, err := llm.NewTool("memories_add_ad_hoc_note", "Create one append-only ad-hoc memory note: either when the user explicitly asks Forebrain Harness to remember, forget, or update something, or when their message states a durable rule, constraint, or preference that should still apply in future sessions. The note is stored under "+memory.AdHocNotesDir+"/ (under the project or global scope named by scope) and the response returns its path relative to the memories root; pass that path verbatim to memories_read, not the bare filename.", func(ctx context.Context, input *memoriesAddAdHocNoteInput) (addAdHocNoteResponse, error) {
		if input == nil {
			return addAdHocNoteResponse{}, fmt.Errorf("input is required")
		}
		scope := memory.AdHocNoteScopeProject
		if input.Scope != nil {
			scope = memory.AddAdHocNoteScope(strings.TrimSpace(*input.Scope))
		}
		path, err := backend.AddAdHocNote(scope, input.Filename, input.Note)
		recordMemoryToolCall("add_ad_hoc_note", "ad_hoc_notes:"+string(scope), err == nil, "unknown")
		return addAdHocNoteResponse{Path: path}, err
	})
	if err != nil {
		return nil, err
	}
	add.Use(strictMemoryArguments[memoriesAddAdHocNoteInput]())
	list, err := llm.NewTool("memories_list", "List immediate files and directories under a path in the Forebrain Harness memories store.", func(ctx context.Context, input *memoriesListInput) (memory.ListResponse, error) {
		if input == nil {
			input = &memoriesListInput{}
		}
		maxResults := memory.MaxListResults
		if input.MaxResults != nil {
			if *input.MaxResults < 1 {
				return memory.ListResponse{}, fmt.Errorf("max_results must be a positive integer")
			}
			maxResults = *input.MaxResults
		}
		listAt := func(path string) (memory.ListResponse, error) {
			return backend.List(memory.ListRequest{Path: &path, Cursor: input.Cursor, MaxResults: maxResults})
		}
		response, err := backend.List(memory.ListRequest{Path: input.Path, Cursor: input.Cursor, MaxResults: maxResults})
		scope := memoryToolOptionalScope(input.Path, "root")
		if input.Path != nil {
			var path string
			path, response, err = retryInAdHocNotes(*input.Path, response, err, listAt)
			scope = memoryToolScope(path)
		}
		recordMemoryToolCall("list", scope, err == nil, memoryTruncatedTag(response.Truncated, err == nil))
		return response, err
	})
	if err != nil {
		return nil, err
	}
	list.Use(strictMemoryArguments[memoriesListInput]())
	read, err := llm.NewTool("memories_read", "Read a Forebrain Harness memory file by relative path, optionally starting at a 1-indexed line offset and limiting the number of lines returned.", func(ctx context.Context, input *memoriesReadInput) (memory.ReadResponse, error) {
		if input == nil {
			return memory.ReadResponse{}, fmt.Errorf("input is required")
		}
		lineOffset := 1
		if input.LineOffset != nil {
			if *input.LineOffset < 1 {
				return memory.ReadResponse{}, fmt.Errorf("line_offset must be a 1-indexed line number")
			}
			lineOffset = *input.LineOffset
		}
		if input.MaxLines != nil && *input.MaxLines < 1 {
			return memory.ReadResponse{}, fmt.Errorf("max_lines must be a positive integer")
		}
		readAt := func(path string) (memory.ReadResponse, error) {
			return backend.Read(memory.ReadRequest{Path: path, LineOffset: lineOffset, MaxLines: input.MaxLines, MaxTokens: memory.DefaultReadMaxTokens})
		}
		response, err := readAt(input.Path)
		path, response, err := retryInAdHocNotes(input.Path, response, err, readAt)
		recordMemoryToolCall("read", memoryToolScope(path), err == nil, memoryTruncatedTag(response.Truncated, err == nil))
		return response, err
	})
	if err != nil {
		return nil, err
	}
	read.Use(strictMemoryArguments[memoriesReadInput]())
	search, err := llm.NewTool("memories_search", "Search Forebrain Harness memories by meaning-bearing words: each query is split into words, a memory is recalled when it shares one, and the most relevant few come back. Write queries as the words you expect the memory to contain, not as an exact phrase to match character for character. Weak matches are dropped rather than listed, so an empty result means memory holds nothing on the subject, not that the page was too short. Every hit comes back with three lines of the file on each side of the matching line; when you need more of a memory than that window holds, read the memory file itself with memories_read.", func(ctx context.Context, input *memoriesSearchInput) (memory.SearchResponse, error) {
		if input == nil {
			return memory.SearchResponse{}, fmt.Errorf("input is required")
		}
		// How many memories a search returns is the operator's budget, not the
		// model's: it is configuration, so a turn cannot widen it by asking.
		topK := memory.MaxSearchResults
		if runtime != nil {
			topK = runtime.Cfg.EffectiveMemories().SearchTopK
		}
		searchAt := func(path *string) (memory.SearchResponse, error) {
			return backend.Search(memory.SearchRequest{Queries: input.Queries, Path: path, TopK: topK})
		}
		response, err := searchAt(input.Path)
		scope := memoryToolOptionalScope(input.Path, "all")
		if input.Path != nil {
			var path string
			path, response, err = retryInAdHocNotes(*input.Path, response, err, func(candidate string) (memory.SearchResponse, error) {
				return searchAt(&candidate)
			})
			scope = memoryToolScope(path)
		}
		// A search is never truncated now: it returns its whole top K.
		recordMemoryToolCall("search", scope, err == nil, memoryTruncatedTag(false, err == nil))
		return response, err
	})
	if err != nil {
		return nil, err
	}
	search.Use(strictMemoryArguments[memoriesSearchInput]())
	return []*llm.Tool{add, list, read, search}, nil
}

// retryInAdHocNotes re-runs a memory lookup against the ad-hoc notes directory
// when a bare note filename missed at the root. memories_add_ad_hoc_note takes
// the filename alone but stores the note one directory down, so the name the
// model just used to write is otherwise unusable for reading, listing, or
// searching. It returns the path the answer actually came from, so telemetry
// and the response echo the stored location.
//
// Only a missing path is retried: answering a symlink, non-UTF-8, or I/O
// refusal with a different file would turn a deliberate rejection into a
// silently wrong success.
func retryInAdHocNotes[T any](path string, response T, err error, call func(string) (T, error)) (string, T, error) {
	if !errors.Is(err, memory.ErrNotFound) {
		return path, response, err
	}
	for _, candidate := range adHocNoteFallbackPaths(path) {
		retried, retryErr := call(candidate)
		if retryErr == nil {
			return candidate, retried, nil
		}
	}
	return path, response, err
}

// adHocNoteFallbackPaths maps a bare ad-hoc note filename to the stored paths
// it could name, project scope first and then global. Both are tried because a
// note is written to one scope or the other and the model has only the bare
// filename to go on; without the global candidate, a note the model just wrote
// with scope "global" would be unreadable by the name it used to write it.
// It only fires for a plain filename, so an explicit path the model gave still
// fails with the caller's own error.
func adHocNoteFallbackPaths(path string) []string {
	stored, ok := adHocNoteFallbackPath(path)
	if !ok {
		return nil
	}
	return []string{stored, "global/" + stored}
}

func adHocNoteFallbackPath(path string) (string, bool) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(path), "./")
	if trimmed == "" || strings.ContainsAny(trimmed, "/\\") || !strings.HasSuffix(trimmed, ".md") {
		return "", false
	}
	return memory.AdHocNotePath(trimmed), true
}

func recordMemoryToolCall(operation, scope string, success bool, truncated string) {
	status := "failed"
	if success {
		status = "succeeded"
	}
	telemetry.LogEvent("forebrain.memory.tool.call", telemetry.Metadata{
		"tool":      "memories/" + operation,
		"operation": operation,
		"scope":     scope,
		"status":    status,
		"truncated": truncated,
	})
}

func memoryToolOptionalScope(path *string, fallback string) string {
	if path == nil {
		return fallback
	}
	return memoryToolScope(*path)
}

func memoryToolScope(path string) string {
	path = strings.TrimPrefix(strings.Trim(strings.TrimSpace(path), "/"), "./")
	if path == "global" || strings.HasPrefix(path, "global/") {
		return "global"
	}
	switch {
	case path == "":
		return "root"
	case path == "MEMORY.md":
		return "memory_md"
	case path == "memory_summary.md":
		return "memory_summary"
	case path == "raw_memories.md":
		return "raw_memories"
	case path == "rollout_summaries" || strings.HasPrefix(path, "rollout_summaries/"):
		return "rollout_summaries"
	case path == "skills" || strings.HasPrefix(path, "skills/"):
		return "skills"
	case path == memory.AdHocNotesDir || strings.HasPrefix(path, memory.AdHocNotesDir+"/"):
		return "ad_hoc_notes"
	default:
		return "other"
	}
}

func memoryTruncatedTag(truncated, known bool) string {
	if !known {
		return "unknown"
	}
	if truncated {
		return "true"
	}
	return "false"
}

func strictMemoryArguments[T any]() llm.ToolMiddleware {
	return func(_ *llm.Tool, next llm.ToolHandler) llm.ToolHandler {
		return func(ctx context.Context, arguments string) (any, error) {
			if strings.TrimSpace(arguments) == "" {
				arguments = "{}"
			}
			decoder := json.NewDecoder(strings.NewReader(arguments))
			decoder.DisallowUnknownFields()
			var input T
			if err := decoder.Decode(&input); err != nil {
				return nil, fmt.Errorf("invalid memory tool arguments: %w", err)
			}
			var trailing any
			if err := decoder.Decode(&trailing); err != io.EOF {
				return nil, fmt.Errorf("invalid memory tool arguments: trailing content")
			}
			return next(ctx, arguments)
		}
	}
}

// memoryBackend binds the memory tools to the calling primary agent's own
// store, scoped to the calling session's project plus the agent's global
// preferences — never to every project the agent has ever seen. It resolves
// from StateRoot (the agent's workspace root), never from the shared Home: a
// primary agent is a tenant, and a home-derived root would hand every tenant
// the same memory.
//
// A session with no project identity (runtime.ProjectKey empty — the
// gateway/channel case with no launch directory) gets a backend whose primary
// root has nothing on it; it still reaches global/ but every project-scoped
// operation (including memories_add_ad_hoc_note's default scope) fails
// closed rather than falling back to a shared bucket.
func memoryBackend(runtime *AgentToolRuntime) (memory.Backend, error) {
	if runtime == nil || strings.TrimSpace(runtime.StateRoot()) == "" {
		return memory.Backend{}, fmt.Errorf("memory runtime unavailable")
	}
	roots, err := memory.ResolveRootsForAgent(runtime.StateRoot())
	if err != nil {
		return memory.Backend{}, err
	}
	projectRoot := ""
	if key := strings.TrimSpace(runtime.ProjectKey); key != "" {
		projectRoot = roots.Scope(memory.Scope{Kind: memory.ScopeProject, Key: key}).MemoryRoot
	}
	globalRoot := roots.Scope(memory.GlobalScope()).MemoryRoot
	backend := memory.NewScoped(projectRoot, globalRoot)
	// The BM25 index lives in the state database, which the session store owns
	// the handle to. Without it the tools still search, by exact substring in
	// path order; with it a query also recalls memories that merely share a
	// term with it, and results come back most relevant first.
	if runtime.Sess != nil {
		backend = backend.WithSearchIndex(runtime.Sess.DB())
	}
	return backend, nil
}
