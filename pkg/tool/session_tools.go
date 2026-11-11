// Session-scoped tools: todos and memory tools.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
		// Office conversations never offer enter_plan_mode: the user enters
		// plan mode explicitly (hotkey, /plan, the web switch), and the model
		// cannot put itself there. A batch conversation offers neither plan
		// tool: its mode is fixed, nobody is there to approve a plan, and a
		// task that stopped to ask would stop for good (owner 2026-10-26).
		batch := rt.ProjectType == state.ProjectTypeBatch
		office := rt.ProjectType == state.ProjectTypeOffice
		var t3 *llm.Tool
		if !office && !batch {
			t3, err = newEnterPlanModeTool(st, stateRoot, projectKey)
			if err != nil {
				return err
			}
		}
		var t3b *llm.Tool
		if !batch {
			t3b, err = newExitPlanModeToolFor(st, stateRoot, projectKey, rt)
			if err != nil {
				return err
			}
		}
		t12, err := NewIntermediateTool(st, stateRoot)
		if err != nil {
			return err
		}
		registerToolMeta(st, t1, "Use when tracking step-by-step execution progress: a structured checklist with per-item status, rewritten in full on every update (for the prose strategy doc write/edit the plan file with write_file/edit_file)", "session", event.ToolMeta{})
		if t3 != nil {
			registerToolMeta(st, t3, "Use when switching the session into plan mode before implementing a non-trivial task", "session", event.ToolMeta{ReadOnly: true})
		}
		if t3b != nil {
			registerToolMeta(st, t3b, "Use when the plan is written and you're ready to leave plan mode for approval", "session", event.ToolMeta{ReadOnly: true})
		}
		registerToolMeta(st, t12, "Use when investigating or reviewing: save, read, or clear session-scoped working notes to synthesize later", "session", event.ToolMeta{})
		_ = st.Register(a, t1)
		if t3 != nil {
			_ = st.Register(a, t3)
		}
		if t3b != nil {
			_ = st.Register(a, t3b)
		}
		_ = st.Register(a, t12)
	}
	// A batch task has no memory: it runs for a business system, not for the
	// person whose preferences and history the memory tools serve.
	if rt != nil && memory.Enabled(rt.Cfg) && !batchProject(rt) {
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
	Action string            `json:"action,omitempty" jsonschema:"enum=set,enum=clear,enum=list" jsonschema_description:"set writes the list (implied when rows are sent), clear empties it, list reads it back."`
	Items  []sessionTodoItem `json:"items,omitempty" jsonschema_description:"The whole checklist after this update: rows with id, content and status (pending|in_progress|completed|cancelled). A row left out is deleted."`
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
		"Track step-by-step progress as a checklist that drives the progress UI. Every write replaces the whole list: send every row that still applies and leave out the rest. Keep exactly one row in_progress. Update it as you work: mark a row completed as soon as its work is fully done, not in a batch later. Before your final answer, bring the list up to date: finished rows completed, dropped rows cancelled; only a row you could not finish stays open, and your reply says why. The plan's prose belongs in the plan file, not here.",
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
	// The working line's task is derived once, here, by the one rule every
	// surface shares — not re-derived by each reader of the payload.
	payload.Active = event.PlanProgressOf(payload.Items, payload.Completed, payload.Total, payload.Explanation).Active
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

// memoriesAddAdHocNoteDescription carries where a note lands: the tool
// returns that path, and reading the note back by the bare filename it was
// written with fails, so the read-back rule lives here with the tool.
const memoriesAddAdHocNoteDescription = "Save one append-only memory note when the user asks to remember, forget or update something, or states a durable rule or preference for future sessions. It is stored under " + memory.AdHocNotesDir + "/ in the chosen scope; pass the returned path, relative to the memories root, verbatim to memories_read."

const enterPlanModeDescription = `Use proactively before a non-trivial implementation: plan mode lets you explore and get the user's sign-off on an approach before writing code, which prevents wasted effort. Entering it requires the user's consent.

Use it when any of these holds:
- new functionality, or a change to existing behavior or structure;
- several valid approaches, or an architectural choice between patterns or technologies;
- likely more than 2-3 files;
- requirements you must explore before you understand the scope;
- a choice the user would care about - plan instead of asking with user_interaction.
When unsure, plan.

Skip it only for few-line fixes, a single function with clear requirements, tasks the user specified in detail, and pure research (use subagent_run with subtype=explore).

In plan mode, explore the code and its existing patterns (explore subagents for parallel research), design the approach, ask the user only what they must decide, write the plan to the session plan file - the only file you may edit - and call exit_plan_mode when ready to implement.`

const exitPlanModeDescription = `Ask the user to approve the plan you have written to the session plan file (its path is in the plan-mode reminder); this tool takes no plan content. Use it only when planning an implementation that writes code, not after research. Resolve open questions with user_interaction first, and never ask there whether the plan is okay - calling this tool is that question.`

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
			planSID := PlanSessionIDFromContext(ctx)
			planDir := state.PlanDirForSession(stateRoot, projectKey, planSID)
			st.SetRuntimeSessionMode(ctx, sid, string(state.ModePlan), planDir)
			// Check whether a plan file already exists so we can guide the LLM
			// to create a NEW descriptive-named file instead of overwriting the
			// generic plan.md fallback.
			existingContent, _ := state.GetPlanForSession(stateRoot, projectKey, planSID)
			planExists := strings.TrimSpace(existingContent) != ""
			respPlanFile := ""
			nextSteps := "Explore the codebase, design the approach, then create a NEW <descriptive-name>.md plan file inside plan_dir (choose a name based on the task, e.g. 'add-auth-validation.md'). Do NOT use the generic name plan.md - it overwrites previous plans and loses history. Then call exit_plan_mode."
			if planExists {
				respPlanFile = state.PlanPathForSession(stateRoot, projectKey, planSID)
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
	return newExitPlanModeToolFor(st, stateRoot, projectKey, nil)
}

// actionKeptPlanOnly is the tool-package twin of turn.ActionKeptPlanOnly
// (the turn package imports this one, so it cannot be used here): it reads
// the same plan_only field out of an approved action's answer JSON.
func actionKeptPlanOnly(act *state.Action) bool {
	if act == nil || act.Status != state.ActionApproved {
		return false
	}
	var answer struct {
		PlanOnly bool `json:"plan_only"`
	}
	_ = json.Unmarshal([]byte(act.AnswerJSON), &answer)
	return answer.PlanOnly
}

// actionHandOffModel is the tool-package twin of turn.ActionHandOffModel
// (the turn package imports this one, so it cannot be used here): it reads
// the hand_off_model object out of an approved action's answer JSON. ok is
// false when the decision carried no hand-off, or named no model.
func actionHandOffModel(act *state.Action) (state.PlanHandOff, bool) {
	if act == nil || act.Status != state.ActionApproved {
		return state.PlanHandOff{}, false
	}
	var answer struct {
		HandOffModel *struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"hand_off_model"`
	}
	_ = json.Unmarshal([]byte(act.AnswerJSON), &answer)
	if answer.HandOffModel == nil {
		return state.PlanHandOff{}, false
	}
	handOff := state.PlanHandOff{
		ModelID:  strings.TrimSpace(answer.HandOffModel.ID),
		Provider: strings.TrimSpace(answer.HandOffModel.Provider),
		Model:    strings.TrimSpace(answer.HandOffModel.Model),
	}
	if handOff.Model == "" {
		return state.PlanHandOff{}, false
	}
	return handOff, true
}

// planFileTitle returns the plan file's first "# " heading, falling back to
// the file name when it has none.
func planFileTitle(planFile, fallback string) string {
	b, err := os.ReadFile(planFile)
	if err != nil {
		return fallback
	}
	for _, line := range strings.Split(string(b), "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(t, "# "))
		}
	}
	return fallback
}

// registerKeptPlanArtifact registers the kept plan file as a conversation
// artifact with origin 'plan' so the user can preview or download it. A
// registration failure is a warning only — the plan is already on disk, so
// the exit must not fail because the ledger could not be updated.
func registerKeptPlanArtifact(ctx context.Context, rt *AgentToolRuntime, planFile string) (map[string]any, bool) {
	if rt == nil || rt.Sess == nil {
		return nil, false
	}
	info, err := os.Stat(planFile)
	if err != nil || info.IsDir() {
		return nil, false
	}
	sum, kind, mimeType, preview, err := hashAndClassifyFile(planFile)
	if err != nil {
		slog.Warn("kept-plan artifact classification failed", "path", planFile, "err", err)
		return nil, false
	}
	name := filepath.Base(planFile)
	row, err := rt.Sess.AddArtifact(ctx, state.Artifact{
		SessionID:  ConversationSessionIDFromContext(ctx),
		RunID:      RunIDFromContext(ctx),
		ToolCallID: ToolUseIDFromContext(ctx),
		Path:       planFile,
		Name:       name,
		Title:      planFileTitle(planFile, name),
		Kind:       kind,
		MIME:       mimeType,
		Preview:    preview,
		Size:       info.Size(),
		MTimeNS:    info.ModTime().UnixNano(),
		SHA256:     sum,
		Origin:     state.ArtifactOriginPlan,
	})
	if err != nil {
		slog.Warn("kept-plan artifact registration failed", "path", planFile, "err", err)
		return nil, false
	}
	return map[string]any{
		"id": row.ID, "name": row.Name, "kind": row.Kind, "preview": row.Preview, "size": row.Size,
	}, true
}

// planExecutionContractFormat is the one execution contract an approved plan
// hands back to the primary agent: it arrives immediately in the exit_plan_mode
// result and again as the recurring implementation reminder, so office
// conversations cannot drift onto a second, shorter set of rules. The two
// placeholders are the plan file path and state.ImplementationHeading.
const planExecutionContractFormat = `You are the primary executor of an approved plan. The plan file is:

  %s

It remains writable in this mode; keep the original plan readable beside the implementation record.

You own implementation and review in the current approved workspace. Do not create a separate executor, worktree, branch, commit, merge or push unless the user or approved plan explicitly requires it.

## Execute against the approved contract

1. Run the plan's drift check first and compare load-bearing current-state excerpts with the live project. If the plan declares dependencies, confirm they are complete. Any load-bearing content mismatch, stale plan or missing dependency is a STOP that requires returning for reconciliation; no adaptation is allowed on that path.
2. Follow the steps in order. Run every verification command and confirm its expected result before moving on. Touch only in-scope files or deliverables and preserve unrelated user work.
3. Stop immediately and report when a plan STOP condition occurs, a required change falls out of scope, a load-bearing assumption is false, or verification still fails after a focused diagnosis and one justified retry. Do not hide a failure by weakening its test.
4. Only a non-load-bearing detail that triggers no drift or STOP condition may be adapted, and only when intent, scope, public behavior and done criteria remain unchanged. Record the adaptation and its evidence; every other deviation returns for reconciliation instead of being patched locally.

## Review your own work before claiming completion

- Re-run every done criterion; do not rely on an earlier step's success.
- Inspect the complete change set and compare it with the scope. Any unjustified out-of-scope change fails review.
- Read the actual changes and the new tests. Confirm they solve the stated problem, follow project conventions, and assert behavior that could regress rather than merely making a command pass.
- Use independent verification when available and useful, but the primary agent remains responsible for the verdict. After two edit-and-review cycles without convergence, stop and report instead of patching indefinitely.

## Close the plan loop

Before the final answer, mark each plan task done, partially done or not done and append exactly the heading %s. Record what landed and where, every deviation and why, anything not done, and each verification result. Update a backlog index only when the plan's own workflow assigns that status update to the executor.

Finish with this report structure; omit STOPPED BECAUSE unless stopped:

STATUS: COMPLETE | STOPPED
STEPS: per step — done/skipped + verification result
STOPPED BECAUSE: the exact condition and observation
FILES CHANGED: list
NOTES: deviations, surprises, judgment calls and anything left unresolved`

// ApprovedPlanExecutionPrompt is the contract the primary agent executes an
// approved plan under. It is returned to the model only: it never reaches a
// human-readable approval card.
func ApprovedPlanExecutionPrompt(planFile string) string {
	return fmt.Sprintf(planExecutionContractFormat, planFile, state.ImplementationHeading)
}

// planHandOffContractFormat is the contract a handed-off plan runs under,
// written after the improve skill's execute variant (owner 2026-11-10): the
// primary agent stays as the tech lead, one executor subagent does the work
// on the chosen model in its own git worktree, and the verdict is the tech
// lead's own review. It is the twin of planExecutionContractFormat above and
// must move with it when either changes. The placeholders are the plan file
// path (%[1]s), the model's display name (%[2]s, used twice), the model
// reference passed to subagent_run (%[3]s, always the configured row id) and
// state.ImplementationHeading (%[4]s).
const planHandOffContractFormat = `You are the tech lead for an approved plan. The user handed its implementation to another model, %[2]s. The plan file is:

  %[1]s

It remains writable in this mode; keep the original plan readable beside the implementation record.

You do not implement the plan yourself. One executor subagent does, on %[2]s, in its own git worktree. You dispatch it, review its work the way a tech lead reviews a pull request against its spec, and give the verdict. Never merge, push, rebase or commit on the user's current branch, and never remove the executor's worktree or branch: merging is the user's decision.

## Before you dispatch

1. Confirm the project is a git repository: "git rev-parse --show-toplevel" must succeed. If it does not, stop and tell the user this hand-off needs a git repository.
2. Run the plan's drift check yourself in the current checkout, and if the plan declares dependencies, confirm they are complete. A mismatch is a STOP: report it to the user instead of dispatching.
3. Run "git status --porcelain". A new worktree starts from the last commit, so uncommitted changes in files the plan touches will not be in it. If there are any, ask the user with user_interaction whether to proceed from the last commit or to wait. Never commit, stash or discard their changes yourself.

## Dispatch exactly one executor

Read the whole plan file now. Call subagent_run with subagent_type "general-purpose", model "%[3]s", a short title, and a task made of these four parts, in order:

1. Where to work. Pick a short kebab-case name after the plan (the plan file name is the natural source) and pass it as subagent_run's "worktree" argument. The dispatch creates <main worktree>/.worktrees/<name> on a new branch <name> from HEAD and runs the executor inside it: every read, edit, command and commit happens there and nowhere else. Your brief tells the executor it is already in that worktree; never ask it to create one.
2. This preamble, word for word:
   You are the executor for the implementation plan below. Work only inside the git worktree named above. Follow the plan step by step. Run every verification command and confirm the expected result before moving on. Touch only the files the plan lists as in scope. If any STOP condition occurs, stop immediately and report. Do not improvise around obstacles. A fresh worktree has no installed dependencies or build output: install and build what the verification commands need first. Commit your work in the worktree, on its own branch, following the plan's git workflow when it has one; never push, merge or touch any other branch. Skip any instruction to update a backlog index or the plan file: your reviewer maintains them. Before reporting, audit every claim in your report against an actual tool result from this session; only report what you can point to evidence for, and if a verification failed or was skipped, say so plainly. When finished, reply with exactly the report format below.
3. This report format:
   STATUS: COMPLETE | STOPPED
   WORKTREE: path and branch
   STEPS: per step - done/skipped + verification command result
   STOPPED BECAUSE: (only if STOPPED) which STOP condition, what was observed
   FILES CHANGED: list
   NOTES: deviations, surprises, judgment calls
4. The plan itself, copied in whole, followed by its file path. Do not summarize or shorten it.

## Review: your real job

Run your checks inside the executor's worktree; it is isolated and disposable. Never fix anything yourself.

1. Re-run every done criterion of the plan in the worktree. Do not trust the report: verify.
2. Scope: compare "git -C <worktree> diff --stat <base>..HEAD" and "git -C <worktree> status --porcelain" with the plan's in-scope list, where <base> is the commit the worktree started from. Any change outside scope fails review.
3. Read the full diff. Judge it against the plan's intent (does it solve the actual problem?) and against the project conventions the plan names.
4. Audit the new tests: read what they assert. A test that asserts nothing meaningful proves nothing.

Documented deviations are judged on merit: a minimal, explained adaptation that serves the plan's intent and stays in scope is acceptable; an undocumented deviation fails review.

## Verdict

- APPROVE: every criterion passes in your own run, scope is clean, quality holds.
- REVISE: fixable gaps. Send the same executor specific, actionable feedback with subagent_continue, then review again. At most two revision rounds; after that, BLOCK.
- BLOCK: a STOP condition was hit, scope was violated beyond repair, or the revision rounds ran out.

## Close the plan loop

Before your final answer, mark each plan task done, partially done or not done and append exactly the heading %[4]s to the plan file. Record the verdict, the worktree path and branch, the executor's commits, every deviation and why, anything not done, and each verification result you ran yourself.

Finish with this report; omit BLOCKED BECAUSE unless blocked:

VERDICT: APPROVE | BLOCK
WORKTREE: path and branch (merging it is the user's decision)
CHECKS: each done criterion - pass or fail, from your own run
SCOPE: files changed, and anything outside scope
BLOCKED BECAUSE: what stopped it
NOTES: deviations, revision rounds, anything left unresolved`

// HandOffPlanExecutionPrompt is the contract the tech lead executes a handed-
// off plan under. modelRef is what the dispatch passes to subagent_run's
// model argument: the configured row id, never a provider/model pair.
func HandOffPlanExecutionPrompt(planFile, modelDisplay, modelRef string) string {
	return fmt.Sprintf(planHandOffContractFormat, planFile, modelDisplay, modelRef, state.ImplementationHeading)
}

// newExitPlanModeToolFor is newExitPlanModeTool for a concrete runtime. A nil
// rt keeps the historical coding behaviour; an office runtime refuses the
// call outside plan mode (the user enters plan mode explicitly, the wrapper
// only offers the tool while they are there) and honours a "keep the plan
// only" approval by leaving plan mode without asking the model to carry the
// plan out.
func newExitPlanModeToolFor(st *State, stateRoot, projectKey string, rt *AgentToolRuntime) (*llm.Tool, error) {
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
			// Office guard, before any approval is requested: office only
			// offers exit_plan_mode while the user has the conversation in
			// plan mode, so a call in agent mode is the model hallucinating
			// the tool. A missing state file reads as agent mode and refuses
			// the same way.
			//
			// An approved replay is exempt: it is the engine finishing the
			// decision the user just made, not the model raising the gate on
			// its own. The gateway switches the conversation out of plan mode
			// as part of honouring the approval (turn.PlanModeForAction, in
			// resumeGatewayRun) and only then re-runs this call, so reading
			// the mode here would turn an approved exit into a failed call.
			// Skipping the guard for the replay does not widen the rule: a
			// call the model makes in agent mode still carries no approved
			// action id and is refused exactly as before.
			if rt != nil && rt.ProjectType == state.ProjectTypeOffice && strings.TrimSpace(ApprovedActionIDFromContext(ctx)) == "" {
				if cur, _ := state.Get(stateRoot, sid); cur.Mode != state.ModePlan {
					return "", fmt.Errorf("exit_plan_mode is only available while the user has this conversation in plan mode")
				}
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
			planSID := PlanSessionIDFromContext(ctx)
			planDir := state.PlanDirForSession(stateRoot, projectKey, planSID)
			planFile := state.PlanPathForSession(stateRoot, projectKey, planSID)
			if rt != nil && rt.Actions != nil {
				actionID := strings.TrimSpace(id)
				if actionID == "" {
					actionID = ApprovedActionIDFromContext(ctx)
				}
				planOnly := false
				var handOff state.PlanHandOff
				handedOff := false
				if actionID != "" {
					if act, err := rt.Actions.Get(ctx, actionID); err == nil {
						planOnly = actionKeptPlanOnly(act)
						handOff, handedOff = actionHandOffModel(act)
					}
				}
				if handedOff {
					// The user handed the plan's implementation to another model:
					// leave plan mode the way an approved exit does and record the
					// row, so the recurring reminder is this contract rather than
					// the self-execution one. The conversation's own model stays —
					// the primary agent is the tech lead, the chosen row runs the
					// executor subagent.
					if _, err := state.HandOffPlan(stateRoot, sid, restored, handOff); err != nil {
						return "", err
					}
					st.SetRuntimeSessionMode(ctx, sid, string(restored), planDir)
					display := llm.FormatProviderModel(handOff.Provider, handOff.Model)
					ref := strings.TrimSpace(handOff.ModelID) // B1 guarantees a row id
					b, _ := json.Marshal(map[string]any{
						"mode":           string(restored),
						"plan_file":      planFile,
						"hand_off_model": map[string]any{"id": handOff.ModelID, "provider": handOff.Provider, "model": handOff.Model},
						"message":        "Exited plan mode. The user handed this plan to " + display + ": dispatch one executor subagent on that model to carry it out in its own git worktree, then review its work. Do not implement the plan yourself.",
						"on_completion":  HandOffPlanExecutionPrompt(planFile, display, ref),
					})
					return string(b), nil
				}
				if planOnly {
					// The plan itself is the deliverable: leave plan mode the
					// way an approved exit does, but record that nothing
					// should nudge the model into implementing the plan.
					if _, err := state.KeepPlanOnly(stateRoot, sid, restored); err != nil {
						return "", err
					}
					st.SetRuntimeSessionMode(ctx, sid, string(restored), planDir)
					result := map[string]any{
						"mode":      string(restored),
						"plan_file": planFile,
						"plan_only": true,
						"message":   "The user kept this plan as the deliverable and does not want it carried out now. Do not start any of its tasks or create any other file. Reply in one or two sentences: where the plan is and that you can carry it out whenever they ask.",
					}
					if art, ok := registerKeptPlanArtifact(ctx, rt, planFile); ok {
						result["artifact"] = art
					}
					b, _ := json.Marshal(result)
					return string(b), nil
				}
			}
			// Switch records the exit so the next enter_plan_mode detects a
			// re-entry — but only when this actually left plan mode, so calling
			// exit_plan_mode outside plan mode no longer fakes one.
			if _, err := state.Switch(stateRoot, sid, restored); err != nil {
				return "", err
			}
			// Keep the plan directory writable in the restored mode so the agent
			// can close the loop by recording the implementation status in the
			// plan file once the work is done.
			st.SetRuntimeSessionMode(ctx, sid, string(restored), planDir)
			// Keep the LLM-facing payload minimal: only the restored mode, the
			// plan file reference, and a human-readable confirmation message.
			// The execution contract rides in on_completion, which is for the
			// model only and is rendered nowhere.
			b, _ := json.Marshal(map[string]any{
				"mode":          string(restored),
				"plan_file":     planFile,
				"message":       "Exited plan mode. You can now make edits, run tools, and take actions. Implement the approved plan in " + planFile + ".",
				"on_completion": ApprovedPlanExecutionPrompt(planFile),
			})
			return string(b), nil
		},
	)
}

type memoriesAddAdHocNoteInput struct {
	Filename string  `json:"filename" jsonschema:"minLength=24,maxLength=128,pattern=^\\d{4}-\\d{2}-\\d{2}T\\d{2}-\\d{2}-\\d{2}-[a-z0-9][a-z0-9-]{0\\,79}\\.md$,description=Name of the note file to create in YYYY-MM-DDTHH-MM-SS-<slug>.md format."`
	Note     string  `json:"note" jsonschema:"minLength=1,description=Verbatim Markdown note to append to the ad-hoc memory notes."`
	Scope    *string `json:"scope,omitempty" jsonschema:"enum=project,enum=global" jsonschema_description:"project (default) for this project; global only for the user's own preferences that apply in every project."`
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
	add, err := llm.NewTool("memories_add_ad_hoc_note", memoriesAddAdHocNoteDescription, func(ctx context.Context, input *memoriesAddAdHocNoteInput) (addAdHocNoteResponse, error) {
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
	search, err := llm.NewTool("memories_search", "Search memories by the words a memory would contain, not an exact phrase: any shared word recalls it, the most relevant few return and weak matches are dropped, so an empty result means nothing is stored on the subject. Each hit shows three lines around the match; read the file with memories_read for more.", func(ctx context.Context, input *memoriesSearchInput) (memory.SearchResponse, error) {
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
