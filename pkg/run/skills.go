// Skills in a run: the prompt catalog, explicit activation, the command bridge,
// and the write guard.
package run

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// sessionPromptStateSkillCatalog keys the frozen skills catalog in a session's
// prompt state.
const sessionPromptStateSkillCatalog = "skill_catalog"

// sessionPromptStateMemoryInstruction keys the frozen memory instruction. One
// session can drive runs of different kinds, so the variant it was rendered for
// is part of the key (see memoryInstructionVariantKey).
const sessionPromptStateMemoryInstruction = "memory_instruction"

// skillCatalogLLM injects the skills catalog ahead of the conversation.
//
// Skills are described to the model here and read from disk on demand. Keeping
// them out of the tool array is what lets that array stay fixed for the life of
// a session: it renders ahead of the system prompt and the whole conversation,
// so any change to it re-bills the entire cached prefix.
type skillCatalogLLM struct {
	inner llm.LLM
	// roots and stateRoot resolve the same skill set the rest of the runtime
	// sees; stateRoot's toggle store decides which skills are enabled, so a
	// disabled skill is never described to the model.
	roots     []string
	stateRoot string
	// sessions is where the rendered catalog is frozen for the life of a
	// session. See catalogForSession for why it must never be re-rendered
	// mid-session, and why the freeze belongs to the session rather than to
	// this process.
	sessions *state.SessionStore
	// renderOnce freezes one catalog for a runtime that keeps no session state
	// at all — a hook, an isolated run. There is no next session for those, so
	// one render for the life of the runtime is both the most stable answer
	// and a bounded one.
	renderOnce sync.Once
	rendered   string
}

func wrapSkillCatalogLLM(inner llm.LLM, roots []string, stateRoot string, sessions *state.SessionStore) llm.LLM {
	if inner == nil || len(roots) == 0 {
		return inner
	}
	return &skillCatalogLLM{
		inner:     inner,
		roots:     append([]string(nil), roots...),
		stateRoot: strings.TrimSpace(stateRoot),
		sessions:  sessions,
	}
}

func (w *skillCatalogLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if w == nil || w.inner == nil {
		return nil, nil
	}
	if catalog := w.catalogForSession(ctx); catalog != "" {
		messages = memory.InjectDeveloperInstruction(messages, catalog)
	}
	return w.inner.Execute(ctx, messages, tools)
}

// catalogForSession renders the catalog once per session and returns that exact
// string for every later call.
//
// The catalog is injected ahead of the whole conversation, so a provider only
// keeps its cached prefix while these bytes are identical. Re-reading the skill
// directory each turn would let a skill installed, renamed, or toggled
// mid-session change the prefix and re-bill every cached token in the session.
// A change to the set therefore takes effect in the next session, which is the
// same rule skill installation already follows.
//
// The freeze is stored with the session, not in this process. A session is the
// thing the frozen bytes belong to: keeping them in a map here meant a
// long-running gateway held one rendered catalog per session it had ever
// served, for as long as it ran, and a session resumed after a restart came
// back to a freshly rendered catalog that re-billed its whole prefix. A run
// with no session — a hook, a one-off — has nothing to stay identical across,
// and simply renders.
func (w *skillCatalogLLM) catalogForSession(ctx context.Context) string {
	sessionID := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
	if sessionID == "" || w.sessions == nil {
		return w.renderFrozen()
	}
	if frozen, ok, err := w.sessions.SessionPromptState(ctx, sessionID, sessionPromptStateSkillCatalog); err == nil && ok {
		return frozen
	} else if err != nil {
		slog.DebugContext(ctx, "skill catalog: read frozen catalog", "session", sessionID, "err", err)
	}
	frozen, ok, err := w.sessions.FreezeSessionPromptState(ctx, sessionID, sessionPromptStateSkillCatalog, w.render())
	if err != nil {
		slog.DebugContext(ctx, "skill catalog: freeze catalog", "session", sessionID, "err", err)
	}
	if err != nil || !ok {
		// Nothing was frozen — there is no session row to hang it on, or the
		// store refused. Returning the render that just happened would render
		// again on the next request; the runtime's own freeze is what keeps the
		// bytes identical for the rest of this process.
		return w.renderFrozen()
	}
	return frozen
}

// renderFrozen renders once and keeps that answer, for runs with no session to
// freeze against.
func (w *skillCatalogLLM) renderFrozen() string {
	w.renderOnce.Do(func() { w.rendered = w.render() })
	return w.rendered
}

// render walks the skill roots and renders the catalog for what is on disk now.
func (w *skillCatalogLLM) render() string {
	catalog, err := skill.Catalog(w.roots, w.stateRoot)
	if err != nil {
		// A skill directory that cannot be read is not a reason to fail the
		// turn: the model simply does not learn about those skills.
		slog.Debug("skill catalog: render failed", "err", err)
		catalog = ""
	}
	return strings.TrimSpace(catalog)
}

// SkillToolInput names the skill to load. It carries a free-form string and
// never an enum of the installed skill names: the tool array renders ahead of
// the system prompt and the whole conversation, so a schema that listed the
// skills would re-bill the entire cached prefix on every install, rename or
// toggle. The names live in the catalog section instead, which sits behind the
// system breakpoint.
type SkillToolInput struct {
	Name string `json:"name" jsonschema_description:"Exact name of the skill to load, as spelled in the Skills section of this conversation."`
}

// skillToolDescription is a compile-time constant for the same reason
// SkillToolInput has no enum: this text is part of the cached prefix, and a
// description that grew or shrank with the installed skill set would invalidate
// it. It tells the model where the names are without repeating them.
const skillToolDescription = "Load a skill's complete instructions by its exact name from the Skills list: its SKILL.md and the paths of its bundled files."

// RegisterSkillTool gives the model the one way it can act on the skills
// catalog. The catalog describes what exists; this loads it.
//
// It is registered unconditionally, including for a session with no skills at
// all. Registering it only when skills exist would make the tool array depend
// on the skill set, which is precisely the mutability that keeping skills out
// of that array was meant to remove: install the first skill and the array — and
// with it the cached tools+system prefix — would change shape.
//
// The load itself is skill.LoadActivation, the same call the explicit slash
// invocation preloads through, so a skill the model loads and a skill the user
// selects arrive as byte-identical context.
func RegisterSkillTool(a tool.ToolAdder, st *tool.State) error {
	if a == nil {
		return nil
	}
	t, err := llm.NewTool(
		"skill",
		skillToolDescription,
		func(ctx context.Context, in *SkillToolInput) (string, error) {
			name := ""
			if in != nil {
				name = strings.TrimSpace(in.Name)
			}
			if name == "" {
				return "", fmt.Errorf("name is required: use a skill name from the Skills section")
			}
			if err := st.GuardTool(ctx, "skill"); err != nil {
				return "", err
			}
			item, ok := loadedSkillByName(st, name)
			if !ok {
				return "", fmt.Errorf("no skill named %q is available in this session", name)
			}
			// The session's pinned copy, not the formal directory: a version
			// published after this session started is the next session's skill.
			skillFile := item.PinnedSkillFile()
			activation, err := pinnedSkillLoad(item)
			if err != nil {
				return "", err
			}
			tool.CaptureToolOutput(ctx, map[string]any{
				"preview_kind": "skill",
				"skill_name":   activation.Name,
				"skill_path":   skillFile,
				"output_bytes": len(activation.Content),
			})
			return activation.Content, nil
		},
	)
	if err != nil {
		return err
	}
	if err := st.Register(a, t); err != nil {
		return err
	}
	if st != nil {
		meta := event.ToolMeta{
			Name:            "skill",
			Description:     skillToolDescription,
			Category:        "skill",
			ReadOnly:        true,
			ConcurrencySafe: true,
		}
		if schema, err := json.Marshal(t.InputSchema()); err == nil {
			meta.InputSchema = schema
		}
		st.RegisterToolMeta(meta)
	}
	return nil
}

// loadedSkillByName resolves a model-supplied name against the skills this
// agent actually discovered. Matching goes through skill.NormalizeToken so the
// spelling the model echoes back — case folded, an underscore for a hyphen —
// reaches the same skill the catalog named, and so this tool agrees with every
// other place a skill is addressed by name.
func loadedSkillByName(st *tool.State, name string) (tool.LoadedSkill, bool) {
	token := skill.NormalizeToken(name)
	if st == nil || token == "" {
		return tool.LoadedSkill{}, false
	}
	for _, item := range st.LoadedSkills() {
		if skill.NormalizeToken(item.Name) == token {
			return item, true
		}
	}
	return tool.LoadedSkill{}, false
}

// pinnedSkillLoad reads one loaded skill's instructions from the copy this
// session is pinned to. A versioned skill whose version directory has gone
// missing is an error naming the version — falling back to the formal
// directory would silently load content the session was never described to.
func pinnedSkillLoad(item tool.LoadedSkill) (skill.Activation, error) {
	skillFile := item.PinnedSkillFile()
	activation, err := skill.LoadActivation(skillFile)
	if err == nil {
		return activation, nil
	}
	if item.Version > 0 {
		if _, statErr := os.Stat(skillFile); statErr != nil {
			return skill.Activation{}, fmt.Errorf("skill %q v%d no longer exists; its version directory was removed — start a new session to load the skill's current version", item.Name, item.Version)
		}
	}
	return skill.Activation{}, err
}

// skillDraftResolver adapts the version store's draft redirection into the
// port pkg/tool defines for it. tool sits below skill in Layer 2's order and
// cannot import the store, so the runner closes over the versioned roots and
// hands this adapter in; the port is the whole of tool's knowledge about it.
func skillDraftResolver(roots []string) tool.SkillDraftResolver {
	roots = append([]string(nil), roots...)
	return func(path string) (tool.SkillDraftTarget, bool, error) {
		target, handled, err := skill.ResolveDraftTarget(roots, path)
		if err != nil || !handled {
			return tool.SkillDraftTarget{}, handled, err
		}
		return tool.SkillDraftTarget{
			SkillName: target.SkillName,
			Target:    target.Target,
			Source:    target.Source,
		}, true, nil
	}
}

type explicitSkillActivation struct {
	SkillName string
	Content   string
}

type explicitSkillActivationsContextKey struct{}

// WithExplicitSkillActivations seeds already-loaded activations into the run
// context, one per skill, in load order. It is the runner's own hand-off
// between the preload phase and the transcript builder: surfaces never call it,
// because they cannot be trusted to load skill files — a skill path is resolved
// and its content is read inside the runner, after the running card has been
// shown.
func WithExplicitSkillActivations(ctx context.Context, activations []explicitSkillActivation) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	kept := make([]explicitSkillActivation, 0, len(activations))
	for _, activation := range activations {
		name := strings.TrimSpace(activation.SkillName)
		if name == "" || strings.TrimSpace(activation.Content) == "" {
			continue
		}
		kept = append(kept, explicitSkillActivation{SkillName: name, Content: activation.Content})
	}
	if len(kept) == 0 {
		return ctx
	}
	return context.WithValue(ctx, explicitSkillActivationsContextKey{}, kept)
}

// WithExplicitSkillActivation seeds one activation; it is the single-skill
// shorthand for WithExplicitSkillActivations.
func WithExplicitSkillActivation(ctx context.Context, skillName, content string) context.Context {
	return WithExplicitSkillActivations(ctx, []explicitSkillActivation{{SkillName: skillName, Content: content}})
}

// explicitSkillActivationsFromContext returns every seeded activation, in load
// order.
func explicitSkillActivationsFromContext(ctx context.Context) ([]explicitSkillActivation, bool) {
	if ctx == nil {
		return nil, false
	}
	activations, ok := ctx.Value(explicitSkillActivationsContextKey{}).([]explicitSkillActivation)
	if !ok || len(activations) == 0 {
		return nil, false
	}
	return activations, true
}

// explicitSkillActivationFromContext returns the first seeded activation.
func explicitSkillActivationFromContext(ctx context.Context) (explicitSkillActivation, bool) {
	activations, ok := explicitSkillActivationsFromContext(ctx)
	if !ok {
		return explicitSkillActivation{}, false
	}
	return activations[0], true
}

// explicitSkillSelection is the trusted trace of one explicit skill
// invocation: which skill the user picked and where its SKILL.md lived when the
// command was registered. Nothing about the skill's content travels with it —
// the content is loaded by the runner's preload phase so the user sees the real
// load.
type explicitSkillSelection struct {
	SkillName string
	SkillPath string
}

type explicitSkillSelectionsContextKey struct{}

// SkillSelection names one skill a turn activates by name. It is the run
// package's copy of the pair turn.SkillSelection carries: run must not import
// turn (session, turn and run do not import each other), so the surface — which
// does see turn — hands the same name and path across this boundary.
type SkillSelection struct {
	Name string
	Path string
}

// WithExplicitSkillSelections marks a run as activating these skills by name,
// in order. It is internal metadata: ordinary user prompt text cannot opt into
// this path by imitating a marker. Duplicate names collapse; an entry missing a
// name or path is dropped.
func WithExplicitSkillSelections(ctx context.Context, selections []SkillSelection) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	kept := make([]explicitSkillSelection, 0, len(selections))
	seen := map[string]struct{}{}
	for _, selection := range selections {
		name := strings.TrimSpace(selection.Name)
		path := strings.TrimSpace(selection.Path)
		if name == "" || path == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		kept = append(kept, explicitSkillSelection{SkillName: name, SkillPath: path})
	}
	if len(kept) == 0 {
		return ctx
	}
	return context.WithValue(ctx, explicitSkillSelectionsContextKey{}, kept)
}

// WithExplicitSkillSelection marks a run as one explicit skill invocation; it
// is the single-skill shorthand for WithExplicitSkillSelections.
func WithExplicitSkillSelection(ctx context.Context, skillName, skillPath string) context.Context {
	return WithExplicitSkillSelections(ctx, []SkillSelection{{Name: skillName, Path: skillPath}})
}

// explicitSkillSelectionsFromContext returns every explicit selection, in
// order.
func explicitSkillSelectionsFromContext(ctx context.Context) ([]explicitSkillSelection, bool) {
	if ctx == nil {
		return nil, false
	}
	selections, ok := ctx.Value(explicitSkillSelectionsContextKey{}).([]explicitSkillSelection)
	if !ok || len(selections) == 0 {
		return nil, false
	}
	return selections, true
}

// explicitSkillSelectionFromContext returns the first explicit selection.
func explicitSkillSelectionFromContext(ctx context.Context) (explicitSkillSelection, bool) {
	selections, ok := explicitSkillSelectionsFromContext(ctx)
	if !ok {
		return explicitSkillSelection{}, false
	}
	return selections[0], true
}

// withoutExplicitSkills drops the skill a dispatching turn's message selected,
// and the activation already loaded from it, so a child conversation never runs
// on a skill the user gave another conversation. A skill belongs to the message
// that selected it, not to the conversation that dispatched the child: left in
// place, the child loads it again in its own worker session and inlines that
// SKILL.md into every request it makes.
//
// Both readers treat an empty slice as "nothing selected", so a child that
// seeds its own selection after this boundary — a user typing the skill into the
// subagent view, a steer carrying it — loads it as it always did.
func withoutExplicitSkills(ctx context.Context) context.Context {
	if ctx == nil {
		return ctx
	}
	ctx = context.WithValue(ctx, explicitSkillSelectionsContextKey{}, []explicitSkillSelection(nil))
	return context.WithValue(ctx, explicitSkillActivationsContextKey{}, []explicitSkillActivation(nil))
}

// ExplicitSkillLoadError reports that an explicitly selected skill failed to
// load before the first LLM request. The failure is already shown to the user
// as the Skill failure card, so surfaces must not render a second generic
// error card for it.
type ExplicitSkillLoadError struct {
	SkillName string
	SkillPath string
	Err       error
}

func (e *ExplicitSkillLoadError) Error() string {
	if e == nil || e.Err == nil {
		return "skill load failed"
	}
	return "skill load failed: " + e.Err.Error()
}

func (e *ExplicitSkillLoadError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func explicitSkillActivationMessage(activation explicitSkillActivation) llm.Message {
	// The skills catalog tells the model to read a skill's SKILL.md before
	// acting on it. Here the user picked the skill by name and its body is
	// already inlined, so this says exactly that and nothing more — the model
	// needs no extra protocol for a skill it did not have to find.
	text := "The user selected the `" + activation.SkillName + "` skill for this request. " +
		"Its full instructions are included below; follow them for this turn. " +
		"You do not need to read its SKILL.md again.\n\n" +
		strings.TrimSpace(activation.Content)
	msg := llm.UserMessage(llm.Text(text))
	msg.IsMeta = true
	// Rebuilt from the slash-command context on every request, so it must never
	// be persisted. Persisting it (a) accumulated one copy per attempt when a
	// turn failed and the user retried, and (b) misaligned the stored transcript
	// against the pre-pipeline user row, so AppendMessageSequence found no
	// common prefix and re-appended the whole turn, duplicating the user
	// message. See TestExplicitSkillActivationIsNotPersisted.
	msg.Ephemeral = true
	return msg
}

// preloadExplicitSkills runs each explicit skill's real load inside the runner,
// before the first LLM request, in order, drawing one Skill card per skill. The
// slash phase hands over only the trusted selections (name and path); this
// phase owns everything the user used to miss: the running card while the file
// is read, the true duration, and a failure card instead of a generic error.
//
// The load lives in loadExplicitSkill and nowhere else. Surfaces cannot be
// trusted to read skill files — they would race the skill being replaced on
// disk and they have no place to show the load happening — so they submit only
// the selections.
//
// On success the activation contents are seeded into the returned context,
// where the transcript builder picks them up. A load failure ends the turn
// before any LLM call with an ExplicitSkillLoadError, whose card the step
// events have already shown — the same stop a single explicit skill has always
// caused, so skills after the failing one are not loaded this turn.
func (r *Runner) preloadExplicitSkills(ctx context.Context) (context.Context, error) {
	selections, ok := explicitSkillSelectionsFromContext(ctx)
	if !ok || r == nil {
		return ctx, nil
	}
	var activations []explicitSkillActivation
	for i, selection := range selections {
		stepID := "skill-explicit-" + strconv.Itoa(i) + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
		activation, err := loadExplicitSkill(ctx, r.tools, selection, stepID)
		if err != nil {
			return ctx, &ExplicitSkillLoadError{SkillName: selection.SkillName, SkillPath: selection.SkillPath, Err: err}
		}
		activations = append(activations, activation)
	}
	return WithExplicitSkillActivations(ctx, activations), nil
}

// loadExplicitSkill runs one explicit skill's real load: the Skill card while
// the file is read, the true duration, and a failure card instead of a generic
// error. It is the one load both a turn's explicit skills and a skill-carrying
// steer go through, so the two are indistinguishable to the user and the
// model.
func loadExplicitSkill(ctx context.Context, st *tool.State, selection explicitSkillSelection, stepID string) (explicitSkillActivation, error) {
	step := tool.StepHookFromContext(ctx, st)
	emit := func(evt tool.StepEvent) {
		if step != nil {
			step(ctx, evt)
		}
	}
	// The pinned copy when this session's loaded catalog carries the skill: an
	// explicit invocation reads the same version every other load in this
	// session does. A skill outside the catalog (a cross-tool directory, a
	// built-in) keeps the path the slash command registered.
	skillPath := strings.TrimSpace(selection.SkillPath)
	if item, ok := loadedSkillByName(st, selection.SkillName); ok && item.Version > 0 {
		skillPath = item.PinnedSkillFile()
	}
	identity := tool.StepEvent{
		StepID:    stepID,
		ToolName:  "skill",
		Category:  "skill",
		SkillName: selection.SkillName,
		SkillPath: skillPath,
		Origin:    "explicit",
	}
	emit(tool.StepEvent{Kind: event.RunEventToolStarted, StepID: identity.StepID,
		ToolName: identity.ToolName, Category: identity.Category,
		SkillName: identity.SkillName, SkillPath: identity.SkillPath, Origin: identity.Origin})
	started := time.Now()
	activation, err := skill.LoadActivation(skillPath)
	if err == nil {
		if item, ok := loadedSkillByName(st, selection.SkillName); ok && item.Version > 0 {
			if _, statErr := os.Stat(skillPath); statErr != nil {
				err = fmt.Errorf("skill %q v%d no longer exists; its version directory was removed — start a new session to load the skill's current version", item.Name, item.Version)
			}
		}
	}
	duration := time.Since(started)
	if err == nil && !strings.EqualFold(skill.NormalizeToken(activation.Name), skill.NormalizeToken(selection.SkillName)) {
		err = fmt.Errorf("skill file at %s now names %q", selection.SkillPath, activation.Name)
	}
	if err != nil {
		emit(tool.StepEvent{Kind: event.RunEventToolCompleted, StepID: identity.StepID,
			ToolName: identity.ToolName, Category: identity.Category,
			SkillName: identity.SkillName, SkillPath: identity.SkillPath, Origin: identity.Origin,
			Duration: duration, Error: err.Error()})
		return explicitSkillActivation{}, err
	}
	emit(tool.StepEvent{Kind: event.RunEventToolCompleted, StepID: identity.StepID,
		ToolName: identity.ToolName, Category: identity.Category,
		SkillName: identity.SkillName, SkillPath: identity.SkillPath, Origin: identity.Origin,
		Duration: duration})
	return explicitSkillActivation{SkillName: selection.SkillName, Content: activation.Content}, nil
}

// SkillCommandHooks lets the turn-owned slash command registry stay in sync
// with installed skills without run importing turn directly (session, turn and
// run must not import each other). Both fields are optional: skill.Service
// nil-checks the callbacks it is handed, so a zero-valued SkillCommandHooks is
// a no-op, which is what tests that do not exercise skill-derived slash
// commands get by leaving Runner.SkillCommands unset. process is the only
// caller expected to populate it, wiring turn.RefreshSkills and
// turn.IsBuiltinName in when it constructs a Runner.
type SkillCommandHooks struct {
	// Refresh rebuilds the slash commands derived from installed skills. The
	// launch context is this runtime's frozen project trust decision, so the
	// derived commands describe the same skill set the catalog does.
	Refresh func(home, workspace string, launch safety.ProjectContext) error
	// IsBuiltin reports whether name collides with a builtin slash command.
	IsBuiltin func(name string) bool
}

// skillWriteGuard is a tool middleware that refuses a write whose result would
// be a SKILL.md no entry point can read.
//
// Discovery — the prompt catalog, the loaded-skill catalog, the /skills picker
// and the slash commands — all skip a skill directory whose SKILL.md has no
// description, and all of them address a skill by the name in its frontmatter.
// A file can therefore land on disk perfectly legally and be invisible
// everywhere, which is the gap this closes: the invariant is "a SKILL.md in a
// skill root is readable", and it holds no matter who writes the file — the
// generator skill, the model by hand, or any future path.
//
// It refuses before the write, so the model gets the parser's own error and can
// fix the file it was about to write. It covers every tool that writes a file
// by path: write_file, edit_file, and apply_patch issued through shell. A raw
// shell redirect is not covered — its "content" exists only as an opaque
// command — and the approval gate remains the backstop there.
func skillWriteGuard(roots []string, loadedTools func() *tool.State, isBuiltin func(string) bool) llm.ToolMiddleware {
	canonicalRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		if canonical := skill.CanonicalSkillPath(root); canonical != "" {
			canonicalRoots = append(canonicalRoots, canonical)
		}
	}
	return func(t *llm.Tool, next llm.ToolHandler) llm.ToolHandler {
		return func(ctx context.Context, arguments string) (any, error) {
			if err := guardSkillWrite(ctx, t, canonicalRoots, loadedTools, isBuiltin, arguments); err != nil {
				return nil, err
			}
			return next(ctx, arguments)
		}
	}
}

// guardSkillWrite resolves what a tool call would write and validates every
// target that is a skill's SKILL.md. Targets that are not skill files, and
// calls whose content cannot be previewed without executing them, are left to
// the tool itself.
func guardSkillWrite(ctx context.Context, t *llm.Tool, canonicalRoots []string, loadedTools func() *tool.State, isBuiltin func(string) bool, arguments string) error {
	if len(canonicalRoots) == 0 {
		return nil
	}
	name := ""
	if t != nil {
		name = strings.TrimSpace(t.Name())
	}
	switch name {
	case "write_file":
		var in struct {
			FilePath string `json:"file_path"`
			Content  string `json:"content"`
		}
		if err := json.Unmarshal([]byte(arguments), &in); err != nil {
			return nil
		}
		return validateSkillWrite(in.FilePath, []byte(in.Content), canonicalRoots, isBuiltin)
	case "edit_file":
		var in struct {
			FilePath   string `json:"file_path"`
			OldString  string `json:"old_string"`
			NewString  string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		}
		if err := json.Unmarshal([]byte(arguments), &in); err != nil {
			return nil
		}
		var editState *tool.State
		if loadedTools != nil {
			editState = loadedTools()
		}
		content, ok := previewEdit(editState, in.FilePath, in.OldString, in.NewString, in.ReplaceAll)
		if !ok {
			// The edit does not apply as written, or the file is not there.
			// The tool reports that itself, in its own words.
			return nil
		}
		return validateSkillWrite(in.FilePath, content, canonicalRoots, isBuiltin)
	case "shell":
		var in struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(arguments), &in); err != nil {
			return nil
		}
		if !tool.ShellCommandIsApplyPatch(in.Command) {
			return nil
		}
		var st *tool.State
		if loadedTools != nil {
			st = loadedTools()
		}
		writes, err := tool.PreviewApplyPatchWrites(ctx, st, in.Command)
		if err != nil {
			return nil
		}
		for _, write := range writes {
			if write.Delete {
				continue
			}
			if err := validateSkillWrite(write.Path, write.Content, canonicalRoots, isBuiltin); err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
}

// previewEdit computes the file content an edit_file call would produce, or
// reports that it cannot. It mirrors the tool's own replacement semantics (the
// exact-string match, replace-all or first occurrence) and stays conservative
// in the one place they can differ: when the match is ambiguous, or the file
// cannot be read, the caller is told to leave the call alone rather than to
// refuse it.
//
// The preview reads through the same draft redirection the edit itself goes
// through: a skill's edit reads the draft, so a preview read from the formal
// directory would validate content the tool is not about to write.
func previewEdit(st *tool.State, path, oldString, newString string, replaceAll bool) ([]byte, bool) {
	path = strings.TrimSpace(path)
	if path == "" || oldString == "" {
		return nil, false
	}
	readPath := path
	if st != nil {
		if target, handled, err := st.SkillDraftWriteTarget(path); err == nil && handled {
			readPath = target
		}
	}
	abs := readPath
	if resolved, err := filepath.Abs(readPath); err == nil {
		abs = resolved
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, false
	}
	src := string(raw)
	count := strings.Count(src, oldString)
	if count == 0 {
		return nil, false
	}
	var out string
	if replaceAll {
		out = strings.ReplaceAll(src, oldString, newString)
	} else {
		out = strings.Replace(src, oldString, newString, 1)
	}
	return []byte(out), true
}

// validateSkillWrite refuses a write that targets a skill's SKILL.md and would
// leave it unreadable. An ordinary file — and a plan file, which lives outside
// every skill root — is not this guard's business.
func validateSkillWrite(path string, content []byte, canonicalRoots []string, isBuiltin func(string) bool) error {
	dir, ok := skillRootChildDir(path, canonicalRoots)
	if !ok {
		return nil
	}
	if err := skill.ValidateSkillContent(content); err != nil {
		// The parser's own error, unchanged: it names what is wrong with the
		// file, and the model fixes it rather than being told a story about
		// why it matters.
		return err
	}
	parsed, err := skill.Parse(strings.NewReader(string(content)))
	if err != nil || parsed == nil {
		return err
	}
	slug := filepath.Base(dir)
	if name := strings.TrimSpace(parsed.Name); name != "" && !strings.EqualFold(name, slug) {
		return fmt.Errorf("frontmatter name %q does not match the skill directory %q: a skill is addressed by the name in its frontmatter, so the two must be spelled the same", name, slug)
	}
	// A skill whose name collides with a builtin slash command would take that
	// command over, because a skill-derived command answers before the builtin
	// table does. Refused when the write would introduce the collision: a
	// SKILL.md that already exists under that name collides no less for being
	// refused, and refusing its edits would only make a shipped skill
	// uncorrectable.
	if isBuiltin != nil && isBuiltin(skill.NormalizeToken(slug)) {
		if _, statErr := os.Stat(filepath.Join(dir, "SKILL.md")); statErr != nil {
			return fmt.Errorf("skill name %q collides with a built-in slash command; pick another name, because a skill named this answers to /%s instead of the built-in command", slug, skill.NormalizeToken(slug))
		}
	}
	return nil
}

// skillRootChildDir reports whether path is a skill's SKILL.md — the formal
// directory (<skill root>/<name>/SKILL.md, one bundle level deeper included)
// or its draft (<skill root>/.drafts/<name>/SKILL.md) — and returns the
// directory the skill's name must match. Both sides are canonicalized first —
// symlink and all — so a path that only reaches the same file through a link
// is judged like the file it is.
func skillRootChildDir(path string, canonicalRoots []string) (string, bool) {
	canonical := skill.CanonicalSkillPath(path)
	if canonical == "" || !strings.EqualFold(filepath.Base(canonical), "SKILL.md") {
		return "", false
	}
	dir := filepath.Dir(canonical)
	parent := filepath.Dir(dir)
	for _, root := range canonicalRoots {
		if parent == root {
			return dir, true
		}
		// A bundled skill lives one level deeper inside a namespace directory
		// (a bundle's own subdirectory), which discovery also accepts.
		if filepath.Dir(parent) == root {
			return dir, true
		}
		// A draft lives in the root's .drafts directory; the directory name
		// beside the SKILL.md is the skill's name there too.
		if filepath.Base(parent) == ".drafts" && filepath.Dir(parent) == root {
			return dir, true
		}
	}
	return "", false
}
