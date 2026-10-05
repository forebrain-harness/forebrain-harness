package turn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

func Execute(ctx Context, content string) Result {
	line := strings.TrimSpace(content)
	if !strings.HasPrefix(line, "/") {
		return Result{}
	}
	toks := strings.Fields(line)
	if len(toks) == 0 {
		return Result{}
	}
	cmdName := strings.TrimPrefix(strings.ToLower(toks[0]), "/")
	if cmdName == "" {
		return Result{Handled: true, Reply: CommandCatalog(ctx.Surface, discoveryOptionsOf(ctx))}
	}
	cmd, ok := Find(cmdName)
	if !ok {
		return Result{Handled: true, Reply: UnknownCommandReply(ctx.Surface, cmdName, discoveryOptionsOf(ctx))}
	}
	if !cmd.AllowedOn(ctx.Surface) {
		return Result{Handled: true, Reply: fmt.Sprintf("/%s is unavailable in %s.", cmd.Name, surfaceInWords(ctx.Surface))}
	}
	if ctx.DuringRun && !availableDuringRun(cmd) {
		return Result{Handled: true, Reply: fmt.Sprintf("/%s is unavailable while a task is running.", cmd.Name)}
	}
	if ctx.SideConversation && !cmd.AvailableInSideConversation {
		return Result{Handled: true, Reply: fmt.Sprintf("/%s is unavailable in side conversations.", cmd.Name)}
	}
	// Argument shape precedes availability: a refusal for the argument is
	// the same answer whatever the model supports, and /fast an-arg must not
	// be answered with a model capability note.
	if reply, refuse := InlineArgsRefusal(cmd.Name, toks[1:]); refuse {
		return Result{Handled: true, Reply: reply}
	}
	if cmd.Name == "fast" && !ctx.FastAvailable {
		return Result{Handled: true, Reply: "fast: unavailable for current model"}
	}
	if dyn, ok := findDynamic(cmd.Name); ok {
		return dyn.Handler(ctx, line, toks)
	}
	switch cmd.Name {
	case "new":
		return execNew(ctx)
	case "resume":
		return execResume(ctx)
	case "rename":
		return execRename(ctx, line, toks)
	case "fork":
		return execFork(ctx)
	case "plan":
		return execPlan(ctx, line, toks)
	case "init":
		return execInit(ctx)
	case "goal":
		return execGoal(ctx, line, toks)
	case "status":
		return execStatus(ctx)
	case "permissions":
		return execPermissions(ctx, toks)
	case "fast":
		return execFast(ctx)
	case "agent":
		return execAgent(ctx)
	case "subagents":
		return execSubagents(ctx)
	case "mcp":
		return execMCP(ctx)
	case "lsp":
		return execLSP(ctx)
	case "sandbox":
		return execSandbox(ctx, toks)
	case "diff":
		return execDiff(ctx, toks)
	case "compact":
		return execCompact(ctx, toks)
	case "clear":
		return execClear(ctx, toks)
	case "context":
		return execContext(ctx, toks)
	case "memories":
		return execMemories(ctx, toks)
	case "migrate":
		return execMigrate()
	case "model":
		return execModel(ctx)
	case "skills":
		return execSkills(ctx, line)
	case "connect":
		return execConnect(ctx, toks)
	case "exit":
		return execExit()
	case "help":
		return Result{Handled: true, Reply: CommandCatalog(ctx.Surface, discoveryOptionsOf(ctx))}
	default:
		return Result{Handled: true, Reply: UnknownCommandReply(ctx.Surface, cmdName, discoveryOptionsOf(ctx))}
	}
}

func execConnect(ctx Context, toks []string) Result {
	if ctx.Surface == SurfaceTUI {
		// Handled natively by the TUI — just signal open-panel.
		return Result{Handled: true}
	}
	return Result{Handled: true, Reply: "/connect is only available in the terminal UI."}
}

// execMigrate answers /migrate where it cannot run. The terminal drives
// /migrate with its own pickers before the executor is consulted, so reaching
// here means a surface without them — including a gateway client that claims
// the terminal's channel (B11): AllowedSurfaces filters the catalog, and this
// refusal is the second guarantee that migration stays terminal-only.
func execMigrate() Result {
	return Result{Handled: true, Reply: "/migrate can only be run in the terminal UI."}
}

func ExecuteDynamicOnly(ctx Context, content string) Result {
	line := strings.TrimSpace(content)
	if !strings.HasPrefix(line, "/") {
		return Result{}
	}
	toks := strings.Fields(line)
	if len(toks) == 0 {
		return Result{}
	}
	cmdName := strings.TrimPrefix(strings.ToLower(toks[0]), "/")
	if cmdName == "" {
		return Result{}
	}
	dyn, ok := findDynamic(cmdName)
	if !ok {
		return Result{
			Handled: true,
			Reply:   "channel slash supports skill commands only",
		}
	}
	return dyn.Handler(ctx, line, toks)
}

// CommandCatalog is /help on every surface: every command this surface
// offers, the built-in commands then the skills, each with what it does.
func CommandCatalog(surface Surface, opts DiscoveryOptions) string {
	cmds := FilterWithOptions(surface, "", opts)
	if len(cmds) == 0 {
		return "No slash commands are available here."
	}
	var b strings.Builder
	group := ""
	for _, cmd := range cmds {
		heading := "Commands"
		if cmd.Category == SkillCategory {
			heading = "Skills"
		}
		if heading != group {
			if group != "" {
				b.WriteString("\n")
			}
			b.WriteString(heading + "\n")
			group = heading
		}
		b.WriteString("/" + cmd.Name)
		if desc := strings.TrimSpace(cmd.Description); desc != "" {
			b.WriteString(" — " + desc)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// UnknownCommandReply answers a command this surface does not have, naming
// the closest ones it does.
func UnknownCommandReply(surface Surface, name string, opts DiscoveryOptions) string {
	matches := FilterWithOptions(surface, name, opts)
	if len(matches) == 0 {
		matches = closeByEdits(VisibleWithOptions(surface, opts), name)
	}
	var near []string
	for _, cmd := range matches {
		if len(near) == 3 {
			break
		}
		near = append(near, "/"+cmd.Name)
	}
	switch len(near) {
	case 0:
		return fmt.Sprintf("There is no /%s; type / to see every command.", name)
	case 1:
		return fmt.Sprintf("There is no /%s; did you mean %s?", name, near[0])
	default:
		return fmt.Sprintf("There is no /%s; did you mean %s or %s?", name, strings.Join(near[:len(near)-1], ", "), near[len(near)-1])
	}
}

// closeByEdits is the commands a mistyped name is a slip away from — a letter
// wrong, missing, extra or two swapped, one more for every four letters —
// nearest first.
func closeByEdits(cmds []Command, name string) []Command {
	name = strings.ToLower(strings.TrimSpace(name))
	limit := 1 + len([]rune(name))/4
	type near struct {
		cmd  Command
		dist int
	}
	var found []near
	for _, cmd := range cmds {
		if d := editDistance(name, strings.ToLower(cmd.Name)); d <= limit {
			found = append(found, near{cmd, d})
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].dist < found[j].dist })
	out := make([]Command, len(found))
	for i, f := range found {
		out[i] = f.cmd
	}
	return out
}

// editDistance counts the single-letter edits — change, insert, delete, or
// swap of neighbours — between a and b.
func editDistance(a, b string) int {
	x, y := []rune(a), []rune(b)
	d := make([][]int, len(x)+1)
	for i := range d {
		d[i] = make([]int, len(y)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(x); i++ {
		for j := 1; j <= len(y); j++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && x[i-1] == y[j-2] && x[i-2] == y[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(x)][len(y)]
}

// surfaceInWords names a surface the way a reply to its user does.
func surfaceInWords(surface Surface) string {
	switch surface {
	case SurfaceWebChat:
		return "the web chat"
	case SurfaceTUI:
		return "the terminal"
	}
	return string(surface)
}

func discoveryOptionsOf(ctx Context) DiscoveryOptions {
	return DiscoveryOptions{DuringRun: ctx.DuringRun, SideConversation: ctx.SideConversation, FastAvailable: ctx.FastAvailable}
}

func execNew(ctx Context) Result {
	if ctx.Sessions == nil {
		return Result{Handled: true, Reply: "A new conversation cannot be started here."}
	}
	sessionID := newSessionID(ctx.Surface, ctx.Channel)
	// Ensure the row with title==sessionID, the sentinel every other creation
	// path uses (see AppendMessageWithSource, execResume) to mean "no title yet".
	// Persisting a display placeholder like "New Session" here instead would
	// make lookupSessionTitle treat it as a real, finalized title and mask the
	// message-derived title for the whole first turn.
	createCtx := ctx.CommandContext
	if createCtx == nil {
		createCtx = context.Background()
	}
	if err := ctx.Sessions.Ensure(createCtx, sessionID, sessionID); err != nil {
		return Result{Handled: true, Reply: "Could not start a new conversation: " + err.Error()}
	}
	title := "New Session"
	return Result{
		Handled:         true,
		Reply:           "Started a new conversation.",
		SessionChanged:  true,
		SessionSwitched: true,
		SessionID:       sessionID,
		SessionTitle:    title,
	}
}

func execResume(ctx Context) Result {
	if ctx.Sessions == nil {
		return Result{Handled: true, Reply: "Conversations cannot be resumed here."}
	}
	// The selector owns the choice; scriptable resume lives in the CLI
	// (forebrain resume <id>), not in inline arguments.
	if ctx.Surface == SurfaceTUI {
		return Result{Handled: true, SelectSession: true}
	}
	summaries, err := ctx.Sessions.ListSessionsRecent(ctx.commandContext(), 51)
	if err != nil {
		return Result{Handled: true, Reply: "Could not list the conversations: " + err.Error()}
	}
	var items []PickerItem
	for _, summary := range summaries {
		// The conversation this command was typed in is not one to resume.
		if summary.ID != strings.TrimSpace(ctx.SessionID) && len(items) < 50 {
			items = append(items, sessionPickerItem(summary))
		}
	}
	if len(items) == 0 {
		return Result{Handled: true, Reply: "There is no other conversation to resume."}
	}
	return Result{Handled: true, Picker: &Picker{Command: "resume", Title: "Resume", Hint: "Pick a conversation to continue", Items: items}}
}

func execRename(ctx Context, line string, toks []string) Result {
	if ctx.Sessions == nil || strings.TrimSpace(ctx.SessionID) == "" {
		return Result{Handled: true, Reply: "This conversation cannot be renamed here."}
	}
	parts := strings.SplitN(line, " ", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		return Result{Handled: true, Reply: "Give the new name after the command, as in /rename <title>."}
	}
	sessionID := strings.TrimSpace(ctx.SessionID)
	title := strings.TrimSpace(parts[1])
	if err := ctx.Sessions.SetTitle(context.Background(), sessionID, title); err != nil {
		return Result{Handled: true, Reply: "Could not rename the conversation: " + err.Error()}
	}
	return Result{
		Handled:        true,
		Reply:          fmt.Sprintf("Renamed this conversation to “%s”.", title),
		SessionChanged: true,
		SessionID:      sessionID,
		SessionTitle:   title,
	}
}

func execFork(ctx Context) Result {
	sourceSessionID := strings.TrimSpace(ctx.SessionID)
	if ctx.Sessions == nil || sourceSessionID == "" {
		return Result{Handled: true, Reply: "This conversation cannot be forked here."}
	}
	sourceTitle := currentSessionTitle(ctx, sourceSessionID)
	if sourceTitle == "" {
		sourceTitle = sourceSessionID
	}
	targetTitle := "Fork of " + sourceTitle
	targetSessionID := newSessionID(ctx.Surface, ctx.Channel)
	createCtx := ctx.CommandContext
	if createCtx == nil {
		createCtx = context.Background()
	}
	failed := func(err error) Result {
		return Result{Handled: true, Reply: "Could not fork the conversation: " + err.Error()}
	}
	if err := ctx.Sessions.Ensure(createCtx, targetSessionID, targetTitle); err != nil {
		return failed(err)
	}
	if err := ctx.Sessions.SetParentSessionID(createCtx, targetSessionID, sourceSessionID); err != nil {
		return failed(err)
	}
	// The copy is the conversation as stored, row for row, so what the model
	// is sent from the fork is byte for byte what it was sent before it.
	if err := ctx.Sessions.ForkInto(createCtx, sourceSessionID, targetSessionID); err != nil {
		return failed(err)
	}
	if err := copySlashSessionState(ctx, sourceSessionID, targetSessionID); err != nil {
		return failed(err)
	}
	return Result{
		Handled:         true,
		Reply:           fmt.Sprintf("You are in “%s” now; the original conversation stays as it was.", targetTitle),
		SessionChanged:  true,
		SessionSwitched: true,
		SessionID:       targetSessionID,
		SessionTitle:    targetTitle,
	}
}

func execPlan(ctx Context, line string, toks []string) Result {
	// Inline arguments narrowed to the one free-text form: /plan <description>
	// enters plan mode and starts toward the description. The plan body is
	// written by the model; show lives in /status's Work row; leaving plan
	// mode belongs to the approval flow and the plan-mode hotkey.
	desc := strings.TrimSpace(strings.TrimPrefix(line, toks[0]))
	st, _ := state.Get(ctx.stateRoot(), ctx.SessionID)
	entered := false
	if st.Mode != state.ModePlan {
		_, _ = state.Switch(ctx.stateRoot(), ctx.SessionID, state.ModePlan)
		entered = true
	}
	if desc == "" {
		if entered {
			return Result{
				Handled:     true,
				Reply:       "Plan mode is on: Forebrain Harness works out a plan with you before it changes anything.",
				ModeChanged: true,
				Mode:        string(state.ModePlan),
				Phase:       "plan",
			}
		}
		p, err := state.GetPlanForProject(ctx.stateRoot(), ctx.projectKey())
		if err != nil {
			return Result{Handled: true, Reply: "Could not read the plan: " + err.Error()}
		}
		if strings.TrimSpace(p) == "" {
			return Result{Handled: true, Reply: "Plan mode is on and there is no plan yet; describe the task and Forebrain Harness will plan it."}
		}
		return Result{Handled: true, Reply: p}
	}
	return Result{
		ShouldContinueRun: true,
		ContinueInput:     desc,
		ModeChanged:       entered,
		Mode:              string(state.ModePlan),
		Phase:             "plan",
		ForcePlan:         true,
	}
}

func execInit(ctx Context) Result {
	wd, err := os.Getwd()
	if err != nil || strings.TrimSpace(wd) == "" {
		wd = ctx.stateRoot()
	}
	proj := projectRoot(wd)
	if strings.TrimSpace(proj) == "" {
		return Result{Handled: true, Reply: "init: unavailable"}
	}
	target := filepath.Join(proj, "FOREBRAIN.md")
	_, statErr := os.Stat(target)
	exists := statErr == nil
	return Result{
		ShouldContinueRun: true,
		ContinueInput:     buildInitPrompt(target, exists),
	}
}

func execGoal(ctx Context, line string, toks []string) Result {
	objective := ""
	if parts := strings.SplitN(line, " ", 2); len(parts) == 2 {
		objective = strings.TrimSpace(parts[1])
	}
	if objective == "" {
		return Result{
			Handled: true,
			Reply:   "Usage: /goal <objective> — works toward it in rounds and checks the workspace after each one, until it is met, stops making progress, or you press Esc. For example: /goal make the test suite pass",
		}
	}
	return Result{
		ShouldContinueRun: true,
		ContinueInput:     objective,
		GoalObjective:     objective,
	}
}

func buildInitPrompt(target string, exists bool) string {
	var b strings.Builder
	if exists {
		b.WriteString("Please analyze this codebase and improve the existing FOREBRAIN.md guidance file at ")
		b.WriteString(target)
		b.WriteString(".\n\n")
		b.WriteString("First read the current FOREBRAIN.md, then refine it without discarding useful user-authored content.\n")
	} else {
		b.WriteString("Please analyze this codebase and create a FOREBRAIN.md guidance file at ")
		b.WriteString(target)
		b.WriteString(".\n\n")
	}
	b.WriteString("FOREBRAIN.md is the project's instructions file: it is injected into context on every turn, so keep it concise and high-signal.\n\n")
	b.WriteString("What to add:\n")
	b.WriteString("1. Commands that will be commonly used, such as how to build, lint, and run tests. Include the necessary commands to develop in this codebase, such as how to run a single test.\n")
	b.WriteString("2. High-level code architecture and structure so that future sessions can be productive more quickly. Focus on the \"big picture\" architecture that requires reading multiple files to understand.\n")
	b.WriteString("3. Project-specific conventions and house rules worth enforcing.\n\n")
	b.WriteString("Usage notes:\n")
	b.WriteString("- When you make the initial FOREBRAIN.md, do not repeat yourself and do not include obvious instructions like \"Provide helpful error messages to users\", \"Write unit tests for all new utilities\", \"Never include sensitive information (API keys, tokens) in code or commits\".\n")
	b.WriteString("- Avoid listing every component or file structure that can be easily discovered.\n")
	b.WriteString("- Don't include generic development practices.\n")
	b.WriteString("- If there are Cursor rules (in .cursor/rules/ or .cursorrules) or Copilot rules (in .github/copilot-instructions.md), make sure to include the important parts.\n")
	b.WriteString("- If there is a README.md, make sure to include the important parts.\n")
	b.WriteString("- Do not make up information such as \"Common Development Tasks\", \"Tips for Development\", \"Support and Documentation\" unless this is expressly included in other files that you read.\n\n")
	b.WriteString("Inspect existing config (READMEs, build files, package manifests, CI) to ground the content in what the repo actually does. ")
	b.WriteString("Then write the file using the write_file tool. Keep it focused — commands, structure, and rules, not exhaustive documentation.")
	return strings.TrimSpace(b.String())
}

// projectRoot resolves the project root:
// walk up for a `.git` marker and return that repo root, else the cleaned
// absolute CWD. Kept local to avoid an import edge into assembly, which would
// close a cycle back through turn.
func projectRoot(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	if eval, err := filepath.EvalSymlinks(abs); err == nil && strings.TrimSpace(eval) != "" {
		abs = eval
	}
	cur := abs
	for {
		if st, err := os.Stat(filepath.Join(cur, ".git")); err == nil && (st.IsDir() || st.Mode().IsRegular()) {
			return filepath.Clean(cur)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return filepath.Clean(abs)
}

func execStatus(ctx Context) Result {
	if ctx.Status == nil {
		return Result{Handled: true, Reply: "status: unavailable"}
	}
	reply, handled := ctx.Status.HandleStatusSlash(ctx.SessionID, ctx.Channel, ctx.SideConversation)
	return Result{Handled: handled, Reply: reply}
}

func execPermissions(ctx Context, toks []string) Result {
	if len(toks) == 1 && ctx.Surface == SurfaceTUI {
		return Result{Handled: true, ManagePermissions: true}
	}
	if ctx.Permissions == nil {
		return Result{Handled: true, Reply: "permissions: unavailable"}
	}
	reply, handled := ctx.Permissions.HandlePermissionsSlash(ctx.SessionID, ctx.Channel, toks[1:])
	return Result{Handled: handled, Reply: reply}
}

func execFast(ctx Context) Result {
	if !ctx.FastAvailable {
		return Result{Handled: true, Reply: "fast: unavailable (Anthropic Opus only)"}
	}
	if ctx.Fast == nil {
		return Result{Handled: true, Reply: "fast: unavailable (no fast handler wired)"}
	}
	// No inline args: the toggle is the whole command, and the state is
	// visible in the footer and /status.
	reply, handled := ctx.Fast.HandleFastSlash(ctx.SessionID, ctx.Channel, nil)
	if !handled {
		return Result{Handled: true, Reply: "fast: unavailable"}
	}
	return Result{Handled: true, Reply: reply}
}

// execAgent answers /agent on a surface without the agent picker, the only
// place the choice is made.
// execAgent is /agent on every surface: the primary agents, each with the
// workspace it owns and the one in force marked, as a choice.
func execAgent(ctx Context) Result {
	if ctx.Agent == nil {
		return Result{Handled: true, Reply: "Primary agents cannot be switched here."}
	}
	agents, err := ctx.Agent.PrimaryAgents()
	if err != nil {
		return Result{Handled: true, Reply: "Could not list the primary agents: " + err.Error()}
	}
	if len(agents) == 0 {
		return Result{Handled: true, Reply: "No primary agents are configured."}
	}
	items := make([]PickerItem, len(agents))
	for i, a := range agents {
		items[i] = PickerItem{Value: a.ID, Label: a.ID, Description: a.WorkspaceRoot, Current: a.Active}
	}
	return Result{Handled: true, Picker: &Picker{Command: "agent", Title: "Primary agent", Hint: "Each one works in its own workspace", Items: items}}
}

// chooseAgent switches to the primary agent picked from /agent.
func chooseAgent(ctx Context, id string) Result {
	if ctx.Agent == nil {
		return Result{Handled: true, Reply: "Primary agents cannot be switched here."}
	}
	agents, err := ctx.Agent.PrimaryAgents()
	if err != nil {
		return Result{Handled: true, Reply: "Could not list the primary agents: " + err.Error()}
	}
	at := slices.IndexFunc(agents, func(a PrimaryAgent) bool { return a.ID == id })
	if at < 0 {
		return Result{Handled: true, Reply: "There is no primary agent " + id + "."}
	}
	if agents[at].Active {
		return Result{Handled: true, Reply: "Still on primary agent " + id + "."}
	}
	switched, err := ctx.Agent.SwitchPrimaryAgent(ctx.commandContext(), id)
	if err != nil {
		return Result{Handled: true, Reply: "Could not switch the primary agent: " + err.Error()}
	}
	if root := strings.TrimSpace(switched.WorkspaceRoot); root != "" {
		return Result{Handled: true, Reply: "Switched to primary agent " + switched.ID + ", working in " + root + "."}
	}
	return Result{Handled: true, Reply: "Switched to primary agent " + switched.ID + "."}
}

// execSubagents is /subagents on every surface: this conversation's subagent
// sessions, newest first, as a choice of which to continue in.
func execSubagents(ctx Context) Result {
	if ctx.Sessions == nil || strings.TrimSpace(ctx.SessionID) == "" {
		return Result{Handled: true, Reply: "Subagent sessions cannot be opened here."}
	}
	children, err := ctx.Sessions.ListChildSessionsRecent(ctx.commandContext(), ctx.SessionID, 50)
	if err != nil {
		return Result{Handled: true, Reply: "Could not list the subagent sessions: " + err.Error()}
	}
	if len(children) == 0 {
		return Result{Handled: true, Reply: "This chat has not started any subagents yet."}
	}
	items := make([]PickerItem, len(children))
	for i, child := range children {
		items[i] = sessionPickerItem(child)
	}
	return Result{Handled: true, Picker: &Picker{Command: "subagents", Title: "Subagent sessions", Hint: "Open one to continue in it", Items: items}}
}

// sessionPickerItem is a conversation as a picker lists it: when it was last
// active and what it is called.
func sessionPickerItem(summary state.SessionSummary) PickerItem {
	return PickerItem{Value: summary.ID, Label: sessionSummaryTitle(summary), Description: slashTimeAgo(summary.UpdatedAt)}
}

// chooseSession moves to the conversation picked from /resume or /subagents.
func chooseSession(ctx Context, id string) Result {
	id = strings.TrimSpace(id)
	if id == "" || ctx.Sessions == nil {
		return Result{Handled: true, Reply: "That conversation cannot be opened here."}
	}
	// The picker offers this agent's conversations, but the chosen id is
	// client input all the same: a conversation another primary agent owns
	// is refused here, in the engine, so no surface can complete the
	// switch its own boundary check missed.
	if err := ctx.Sessions.Ensure(ctx.commandContext(), id, id); err != nil {
		if errors.Is(err, state.ErrSessionNotOwned) {
			return Result{Handled: true, Reply: "That conversation belongs to another primary agent."}
		}
		return Result{Handled: true, Reply: "Could not open that conversation: " + err.Error()}
	}
	title := currentSessionTitle(ctx, id)
	if title == "" {
		title = id
	}
	return Result{
		Handled:         true,
		Reply:           fmt.Sprintf("You are in “%s” now.", title),
		SessionChanged:  true,
		SessionSwitched: true,
		SessionID:       id,
		SessionTitle:    title,
	}
}

// Choose applies a choice made in a Picker a command offered, on every
// surface.
func Choose(ctx Context, choice SlashChoice) Result {
	value := choice.Value
	switch choice.Command {
	case "model":
		return chooseModel(ctx, value)
	case "model-effort":
		return chooseReasoningEffort(ctx, value)
	case "agent":
		return chooseAgent(ctx, value)
	case "subagents", "resume":
		return chooseSession(ctx, value)
	}
	return Result{Handled: true, Reply: "That choice is no longer offered."}
}

func execMCP(ctx Context) Result {
	if ctx.MCP == nil {
		return Result{Handled: true, Reply: "mcp: unavailable"}
	}
	reply, handled := ctx.MCP.HandleMCPSlash(ctx.SessionID, ctx.Channel)
	return Result{Handled: handled, Reply: reply}
}

func execLSP(ctx Context) Result {
	if ctx.LSP == nil {
		return Result{Handled: true, Reply: "lsp: unavailable"}
	}
	reply, handled := ctx.LSP.HandleLSPSlash(ctx.SessionID, ctx.Channel)
	return Result{Handled: handled, Reply: reply}
}

func execSandbox(ctx Context, toks []string) Result {
	if ctx.Sandbox == nil {
		return Result{Handled: true, Reply: "sandbox: unavailable"}
	}
	reply, handled := ctx.Sandbox.HandleSandboxSlash(ctx.SessionID, ctx.Channel, toks[1:])
	if !handled {
		return Result{}
	}
	return Result{Handled: true, Reply: strings.TrimSpace(reply)}
}

func execDiff(ctx Context, toks []string) Result {
	if ctx.Diff == nil {
		return Result{Handled: true, Reply: "diff: unavailable"}
	}
	reply, handled := ctx.Diff.HandleDiffSlash(ctx.SessionID, ctx.Channel, toks[1:])
	return Result{Handled: handled, Reply: reply}
}

func execCompact(ctx Context, toks []string) Result {
	if ctx.Compact == nil {
		return Result{Handled: true, Reply: "compact: unavailable"}
	}
	parentCtx := ctx.CommandContext
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	if rid := strings.TrimSpace(ctx.RunID); rid != "" {
		parentCtx = tool.WithRunID(parentCtx, rid)
	}
	reply, handled := ctx.Compact.HandleCompactSlash(parentCtx, ctx.SessionID, ctx.Channel, toks[1:])
	return Result{Handled: handled, Reply: reply}
}

func execClear(ctx Context, toks []string) Result {
	if ctx.Clear == nil {
		return Result{Handled: true, Reply: "The context cannot be cleared here."}
	}
	parentCtx := ctx.CommandContext
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	if rid := strings.TrimSpace(ctx.RunID); rid != "" {
		parentCtx = tool.WithRunID(parentCtx, rid)
	}
	reply, handled := ctx.Clear.HandleClearSlash(parentCtx, ctx.SessionID, ctx.Channel, toks[1:])
	return Result{Handled: handled, Reply: reply}
}

func execContext(ctx Context, toks []string) Result {
	if ctx.ContextDebug == nil {
		return Result{Handled: true, Reply: "context: unavailable"}
	}
	parentCtx := ctx.CommandContext
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	if rid := strings.TrimSpace(ctx.RunID); rid != "" {
		parentCtx = tool.WithRunID(parentCtx, rid)
	}
	reply, handled := ctx.ContextDebug.HandleContextSlash(parentCtx, ctx.SessionID, ctx.Channel, toks[1:])
	return Result{Handled: handled, Reply: reply}
}

// execModel is /model on every surface: the models the session's agent is
// configured with, the one in force marked, as a choice.
func execModel(ctx Context) Result {
	if ctx.Model == nil {
		return Result{Handled: true, Reply: "The model cannot be changed here."}
	}
	settings, err := ctx.Model.ModelSettings(ctx.SessionID)
	if err != nil {
		return Result{Handled: true, Reply: "Could not read the model settings: " + err.Error()}
	}
	choices := ModelChoices(settings.Config, settings.AgentName)
	if len(choices) == 0 {
		return Result{Handled: true, Reply: "This agent has no models configured."}
	}
	items := make([]PickerItem, len(choices))
	for i, choice := range choices {
		// The live selection marks the current model; index zero is only the
		// fallback for a runtime that has never published one.
		items[i] = PickerItem{Value: choice.Label, Label: choice.Label, Current: i == 0 && !settings.Selection.Set || selectionMatches(settings.Selection, choice)}
	}
	return Result{Handled: true, Picker: &Picker{Command: "model", Title: "Model", Hint: "The model this agent runs on", Items: items}}
}

// selectionMatches reports whether the live selection is exactly choice
// (case-insensitive provider+model).
func selectionMatches(sel ModelSelectionState, choice ModelChoice) bool {
	if !sel.Set {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(sel.Provider), strings.TrimSpace(choice.Provider)) &&
		strings.EqualFold(strings.TrimSpace(sel.Model), strings.TrimSpace(choice.Model))
}

// modelSelectionFailureReply words a live-apply failure by how far it got,
// so the answer never claims a model is healthy in the uncertain case.
func modelSelectionFailureReply(name string, err error) string {
	switch SelectionApplyOutcome(err) {
	case ModelRuntimeUncertain:
		return "The switch to " + name + " failed and the previous model could not be restored either. Reload the configuration or restart before sending another message. (" + err.Error() + ")"
	case ModelAppliedNotDurable:
		return "Switched to " + name + ", but the choice could not be saved for this session, so a later session switch will not restore it. (" + err.Error() + ")"
	default:
		return "Could not switch to " + name + ": " + err.Error()
	}
}

// chooseModel applies a model picked from /model. A model already in force is
// not written again; one that reasons is followed by its reasoning effort.
// The live selection is applied before the shared file default is written, so
// a failed default write cannot masquerade as a failed switch.
func chooseModel(ctx Context, label string) Result {
	if ctx.Model == nil {
		return Result{Handled: true, Reply: "The model cannot be changed here."}
	}
	settings, err := ctx.Model.ModelSettings(ctx.SessionID)
	if err != nil {
		return Result{Handled: true, Reply: "Could not read the model settings: " + err.Error()}
	}
	choices := ModelChoices(settings.Config, settings.AgentName)
	at := slices.IndexFunc(choices, func(c ModelChoice) bool { return c.Label == label })
	if at < 0 {
		return Result{Handled: true, Reply: "That model is no longer configured for this agent."}
	}
	chosen := choices[at]
	name := llm.FormatProviderModel(chosen.Provider, chosen.Model)
	// A published live selection decides what "changed" means; a runtime
	// that never published one keeps the config-order reading.
	changed := false
	if settings.Selection.Set {
		changed = !selectionMatches(settings.Selection, chosen)
	} else {
		changed = at != 0
	}
	reply := ""
	if changed {
		// effort=nil: the runner derives and pins this entry's configured
		// effort.
		if err := ctx.Model.SelectModel(ctx.commandContext(), ctx.SessionID, chosen, nil); err != nil {
			return Result{Handled: true, Reply: modelSelectionFailureReply(name, err)}
		}
		reply = "Switched to " + name + "."
		if err := ChooseModel(settings.ConfigPath, settings.AgentName, chosen); err != nil {
			return Result{Handled: true, Reply: reply + "\nThe default for new sessions was not saved: " + err.Error()}
		}
		if err := ctx.Model.ReloadModelCatalog(ctx.commandContext(), ctx.SessionID); err != nil {
			return Result{Handled: true, Reply: reply + "\nThe default was saved, but this process could not refresh its model catalog: " + err.Error()}
		}
		if settings, err = ctx.Model.ModelSettings(ctx.SessionID); err != nil {
			return Result{Handled: true, Reply: reply}
		}
	}
	if !chosen.CanReason {
		if !changed {
			reply = "Still using " + name + "."
		}
		return Result{Handled: true, Reply: reply}
	}
	// The current marker reads the live selection's concrete effort, not
	// config order.
	current := settings.Selection.Effort
	items := make([]PickerItem, len(ReasoningEffortLevels))
	for i, effort := range ReasoningEffortLevels {
		items[i] = PickerItem{Value: encodeEffortChoice(chosen.Label, effort, changed), Label: effort, Current: effort == current}
	}
	return Result{Handled: true, Reply: reply, Picker: &Picker{Command: "model-effort", Title: "Reasoning effort", Hint: "How long " + name + " thinks before it answers", Items: items}}
}

// chooseReasoningEffort applies an effort picked after /model. The live
// selection carries it first; the shared file default follows as an
// independent write, and each partial outcome is reported truthfully.
func chooseReasoningEffort(ctx Context, value string) Result {
	label, effort, modelChanged := decodeEffortChoice(value)
	if ctx.Model == nil {
		return Result{Handled: true, Reply: "The model cannot be changed here."}
	}
	settings, err := ctx.Model.ModelSettings(ctx.SessionID)
	if err != nil {
		return Result{Handled: true, Reply: "Could not read the model settings: " + err.Error()}
	}
	choices := ModelChoices(settings.Config, settings.AgentName)
	at := slices.IndexFunc(choices, func(c ModelChoice) bool { return c.Label == label })
	if at < 0 || !slices.Contains(ReasoningEffortLevels, effort) {
		return Result{Handled: true, Reply: "That reasoning effort is no longer offered for this model."}
	}
	chosen := choices[at]
	if selectionMatches(settings.Selection, chosen) && effort == settings.Selection.Effort {
		if modelChanged {
			// The switch was already reported; its effort stayed as it was.
			return Result{Handled: true}
		}
		return Result{Handled: true, Reply: "Still using " + llm.FormatProviderModel(chosen.Provider, chosen.Model) + " at " + effort + " reasoning effort."}
	}
	if err := ctx.Model.SelectModel(ctx.commandContext(), ctx.SessionID, chosen, &effort); err != nil {
		return Result{Handled: true, Reply: modelSelectionFailureReply(llm.FormatProviderModel(chosen.Provider, chosen.Model), err)}
	}
	if err := ChooseReasoningEffort(settings.ConfigPath, settings.AgentName, chosen, effort); err != nil {
		return Result{Handled: true, Reply: "Reasoning effort set to " + effort + " for this session, but the default for new sessions was not saved: " + err.Error()}
	}
	if err := ctx.Model.ReloadModelCatalog(ctx.commandContext(), ctx.SessionID); err != nil {
		return Result{Handled: true, Reply: "Reasoning effort set to " + effort + ". The default was saved, but this process could not refresh its model catalog: " + err.Error()}
	}
	return Result{Handled: true, Reply: "Reasoning effort set to " + effort + "."}
}

// An effort choice carries the model it is for and whether picking that model
// just switched to it, so the answer can say what actually changed.
func encodeEffortChoice(label, effort string, modelChanged bool) string {
	return strings.Join([]string{label, effort, strconv.FormatBool(modelChanged)}, "\n")
}

func decodeEffortChoice(value string) (label, effort string, modelChanged bool) {
	parts := strings.SplitN(value, "\n", 3)
	if len(parts) != 3 {
		return "", "", false
	}
	changed, _ := strconv.ParseBool(parts[2])
	return parts[0], parts[1], changed
}

func execSkills(ctx Context, _ string) Result {
	if ctx.Surface == SurfaceTUI {
		return Result{Handled: true, SelectSkill: true}
	}
	names, _ := skill.DistinctSkillNamesForWorkspace(ctx.Home, ctx.stateRoot(), ctx.ProjectRoot)
	if len(names) == 0 {
		return Result{Handled: true, Reply: "No skills are installed."}
	}
	items := make([]PickerItem, 0, len(names))
	for _, name := range names {
		item := PickerItem{Value: name, Label: "/" + name}
		if cmd, ok := Find(name); ok {
			item.Description = strings.TrimSpace(cmd.Description)
		}
		items = append(items, item)
	}
	return Result{Handled: true, Picker: &Picker{Command: "skills", Title: "Skills", Hint: "Pick one to run it", Items: items}}
}

// execExit closes the terminal app. It is the terminal's alone: the web chat
// has no app of its own to close, and does not offer the command.
func execExit() Result {
	return Result{
		Handled:       true,
		Reply:         "Closing Forebrain Harness.",
		ExitRequested: true,
	}
}

func newSessionID(surface Surface, channel string) string {
	return state.NewForSurface(string(surface), channel)
}

func currentSessionTitle(ctx Context, sessionID string) string {
	if ctx.Sessions == nil {
		return strings.TrimSpace(sessionID)
	}
	title, err := ctx.Sessions.SessionTitle(context.Background(), sessionID)
	switch {
	case err != nil:
		return strings.TrimSpace(sessionID)
	case title == "":
		return "New conversation"
	default:
		return title
	}
}

func copySlashSessionState(ctx Context, sourceSessionID, targetSessionID string) error {
	stateRoot := ctx.stateRoot()
	if plan, err := state.GetPlanForProject(stateRoot, ctx.projectKey()); err != nil {
		return err
	} else if strings.TrimSpace(plan) != "" {
		if err := state.SetPlanForProject(stateRoot, ctx.projectKey(), plan); err != nil {
			return err
		}
	}
	if todos, err := state.Load(stateRoot, sourceSessionID); err != nil {
		return err
	} else if len(todos.Items) > 0 {
		items := make([]state.Item, 0, len(todos.Items))
		for _, item := range todos.Items {
			items = append(items, state.Item{
				ID:      item.ID,
				Content: item.Content,
				Status:  item.Status,
				Title:   item.Title,
			})
		}
		if _, err := state.Replace(stateRoot, targetSessionID, items); err != nil {
			return err
		}
	}
	modeState, err := state.Get(stateRoot, sourceSessionID)
	if err != nil {
		return err
	}
	if err := state.Set(stateRoot, targetSessionID, modeState); err != nil {
		return err
	}
	return copySlashModelSelection(ctx, sourceSessionID, targetSessionID)
}

// copySlashModelSelection makes the fork inherit the source session's model
// choice. The live selection is materialized as a durable source row first —
// a source that never ran /model can be pinned at runtime with no row at all
// — and only then copied. Forking never writes the shared default file.
func copySlashModelSelection(ctx Context, sourceSessionID, targetSessionID string) error {
	if ctx.Model == nil || ctx.Sessions == nil {
		return nil
	}
	settings, err := ctx.Model.ModelSettings(sourceSessionID)
	if err != nil {
		return err
	}
	if !settings.Selection.Set {
		// No live selection: nothing to inherit; the fork starts on the
		// file default like any new session.
		return nil
	}
	choice := ModelChoice{Provider: settings.Selection.Provider, Model: settings.Selection.Model}
	effort := settings.Selection.Effort
	// A Runner no-op for the live runtime; what it makes durable is the
	// source session's row, so the copy below has something to copy.
	if err := ctx.Model.SelectModel(ctx.commandContext(), sourceSessionID, choice, &effort); err != nil {
		return fmt.Errorf("fork: persist the source session's model choice: %w", err)
	}
	copied, err := ctx.Sessions.CopySessionModelSelection(ctx.commandContext(), sourceSessionID, targetSessionID)
	if err != nil {
		return err
	}
	if !copied && ctx.Surface == SurfaceTUI {
		return fmt.Errorf("fork: the source session's model choice could not be copied")
	}
	return nil
}

// stateRoot returns the per-agent state root for this slash context — the
// active agent's workspace root, onto which the mode/plan/todo stores join
// "state". Falls back to <home>/workspace (the main agent) when StateRoot was
// not filled by the constructor.
// commandContext is what a command's own work runs under: the surface's
// command context when it gave one.
func (c Context) commandContext() context.Context {
	if c.CommandContext != nil {
		return c.CommandContext
	}
	return context.Background()
}

func (c Context) stateRoot() string {
	if root := strings.TrimSpace(c.StateRoot); root != "" {
		return root
	}
	return workspaceRootForSlash(c.Home)
}

func (c Context) projectKey() string {
	return strings.TrimSpace(c.ProjectKey)
}

func workspaceRootForSlash(home string) string {
	root := strings.TrimSpace(home)
	if root == "" {
		return "workspace"
	}
	return filepath.Join(root, "workspace")
}

func slashTimeAgo(ts int64) string {
	if ts <= 0 {
		return ""
	}
	d := time.Since(time.Unix(ts, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/24/30))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/24/365))
	}
}

// sessionSummaryTitle is what a conversation is called in a list; one still
// titled by its id has not been named by a first message yet.
func sessionSummaryTitle(sum state.SessionSummary) string {
	title := strings.TrimSpace(sum.Title)
	if title == "" || title == strings.TrimSpace(sum.ID) {
		return "New conversation"
	}
	return title
}

func execMemories(ctx Context, toks []string) Result {
	if ctx.Memories == nil {
		return Result{Handled: true, Reply: "memories: unavailable"}
	}
	reply, handled := ctx.Memories.HandleMemoriesSlash(context.Background(), ctx.SessionID, ctx.Channel, toks[1:])
	return Result{Handled: handled, Reply: reply}
}
