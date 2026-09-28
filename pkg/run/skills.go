// Skills in a run: the prompt catalog, explicit activation, the command bridge,
// the offer gate, and the write guard.
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

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
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
	// offerGuidance renders the standing skill-offer criteria appended to the
	// catalog inside the same frozen developer block: one more paragraph in a
	// block that was already frozen per session, so the prefix gains neither
	// blocks nor entries. Nil, or a function returning "", means the feature
	// is off and nothing is appended.
	offerGuidance func() string
	// renderOnce freezes one catalog for a runtime that keeps no session state
	// at all — a hook, an isolated run. There is no next session for those, so
	// one render for the life of the runtime is both the most stable answer
	// and a bounded one.
	renderOnce sync.Once
	rendered   string
}

func wrapSkillCatalogLLM(inner llm.LLM, roots []string, stateRoot string, sessions *state.SessionStore, offerGuidance func() string) llm.LLM {
	if inner == nil || len(roots) == 0 {
		return inner
	}
	return &skillCatalogLLM{
		inner:         inner,
		roots:         append([]string(nil), roots...),
		stateRoot:     strings.TrimSpace(stateRoot),
		sessions:      sessions,
		offerGuidance: offerGuidance,
	}
}

// skillOfferGuidance renders the standing skill-offer criteria for this
// runtime, honoring the features.skill_offer switch. wrapSkillCatalogLLM calls
// it once per session render, so the frozen block reads the config live rather
// than a snapshot taken at wrap time.
func (r *Runner) skillOfferGuidance() string {
	enabled := true
	if r != nil && r.AppCfg != nil {
		enabled = r.AppCfg.EffectiveFeatures().SkillOffer
	}
	return skill.RenderSkillOfferGuidance(skill.SkillOfferGuidanceOptions{Enabled: enabled})
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
		slog.Debug("skill catalog: read frozen catalog", "session", sessionID, "err", err)
	}
	frozen, ok, err := w.sessions.FreezeSessionPromptState(ctx, sessionID, sessionPromptStateSkillCatalog, w.render())
	if err != nil {
		slog.Debug("skill catalog: freeze catalog", "session", sessionID, "err", err)
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

// render walks the skill roots and renders the catalog for what is on disk now,
// then appends the standing skill-offer criteria. The criteria render even for
// a session with no skills at all: the catalog is empty there, but the model
// still needs the judgment to notice work worth keeping.
func (w *skillCatalogLLM) render() string {
	catalog, err := skill.Catalog(w.roots, w.stateRoot)
	if err != nil {
		// A skill directory that cannot be read is not a reason to fail the
		// turn: the model simply does not learn about those skills.
		slog.Debug("skill catalog: render failed", "err", err)
		catalog = ""
	}
	return strings.TrimSpace(catalog + "\n\n" + w.offerGuidanceSection())
}

// offerGuidanceSection renders the skill-offer criteria, or "" when the
// feature is off. The catalog itself renders "" for zero skills; the two are
// kept independent so the criteria never depend on the installed set.
func (w *skillCatalogLLM) offerGuidanceSection() string {
	if w == nil || w.offerGuidance == nil {
		return ""
	}
	return strings.TrimSpace(w.offerGuidance())
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
const skillToolDescription = "Load one skill's complete instructions. The Skills section of this conversation lists every skill available here by name and description; when the user names a skill, or a skill's description matches the work, call this with that exact name before starting the work and then follow what it returns. The result is the skill's whole SKILL.md plus the paths of the reference, script and asset files bundled with it."

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
			skillFile := filepath.Join(item.RootDir, "SKILL.md")
			activation, err := skill.LoadActivation(skillFile)
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

type explicitSkillActivation struct {
	SkillName string
	Content   string
}

type explicitSkillActivationContextKey struct{}

// WithExplicitSkillActivation seeds an already-loaded activation into the run
// context. It is the runner's own hand-off between the preload phase and the
// transcript builder: surfaces never call it, because they cannot be trusted to
// load skill files — a skill path is resolved and its content is read inside
// the runner, after the running card has been shown.
func WithExplicitSkillActivation(ctx context.Context, skillName, content string) context.Context {
	skillName = strings.TrimSpace(skillName)
	content = strings.TrimSpace(content)
	if ctx == nil {
		ctx = context.Background()
	}
	if skillName == "" || content == "" {
		return ctx
	}
	return context.WithValue(ctx, explicitSkillActivationContextKey{}, explicitSkillActivation{
		SkillName: skillName,
		Content:   content,
	})
}

func explicitSkillActivationFromContext(ctx context.Context) (explicitSkillActivation, bool) {
	if ctx == nil {
		return explicitSkillActivation{}, false
	}
	activation, ok := ctx.Value(explicitSkillActivationContextKey{}).(explicitSkillActivation)
	if !ok || strings.TrimSpace(activation.SkillName) == "" || strings.TrimSpace(activation.Content) == "" {
		return explicitSkillActivation{}, false
	}
	return activation, true
}

// explicitSkillSelection is the trusted trace of an explicit skill invocation:
// which skill the user picked and where its SKILL.md lived when the command was
// registered. Nothing about the skill's content travels with it — the content
// is loaded by the runner's preload phase so the user sees the real load.
type explicitSkillSelection struct {
	SkillName string
	SkillPath string
}

type explicitSkillSelectionContextKey struct{}

// WithExplicitSkillSelection marks a run as an explicit skill invocation. It is
// internal metadata: ordinary user prompt text cannot opt into this path by
// imitating a marker.
func WithExplicitSkillSelection(ctx context.Context, skillName, skillPath string) context.Context {
	skillName = strings.TrimSpace(skillName)
	skillPath = strings.TrimSpace(skillPath)
	if ctx == nil {
		ctx = context.Background()
	}
	if skillName == "" || skillPath == "" {
		return ctx
	}
	return context.WithValue(ctx, explicitSkillSelectionContextKey{}, explicitSkillSelection{
		SkillName: skillName,
		SkillPath: skillPath,
	})
}

func explicitSkillSelectionFromContext(ctx context.Context) (explicitSkillSelection, bool) {
	if ctx == nil {
		return explicitSkillSelection{}, false
	}
	selection, ok := ctx.Value(explicitSkillSelectionContextKey{}).(explicitSkillSelection)
	if !ok || strings.TrimSpace(selection.SkillName) == "" || strings.TrimSpace(selection.SkillPath) == "" {
		return explicitSkillSelection{}, false
	}
	return selection, true
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

// preloadExplicitSkill runs the explicit skill's real load inside the runner,
// before the first LLM request. The slash phase hands over only the trusted
// selection (name and path); this phase owns everything the user used to miss:
// the running card while the file is read, the true duration, and a failure
// card instead of a generic error.
//
// The load lives here and nowhere else. Surfaces cannot be trusted to read
// skill files — they would race the skill being replaced on disk and they have
// no place to show the load happening — so they submit only the selection.
//
// On success the activation content is seeded into the returned context, where
// the transcript builder picks it up; the model context is byte-identical to
// the previous behavior. On failure the turn ends before any LLM call with an
// ExplicitSkillLoadError, whose card the step events have already shown.
func (r *Runner) preloadExplicitSkill(ctx context.Context) (context.Context, error) {
	selection, ok := explicitSkillSelectionFromContext(ctx)
	if !ok || r == nil {
		return ctx, nil
	}
	step := tool.StepHookFromContext(ctx, r.tools)
	stepID := "skill-explicit-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	identity := tool.StepEvent{
		StepID:    stepID,
		ToolName:  "skill",
		Category:  "skill",
		SkillName: selection.SkillName,
		SkillPath: selection.SkillPath,
		Origin:    "explicit",
	}
	emit := func(evt tool.StepEvent) {
		if step != nil {
			step(ctx, evt)
		}
	}
	emit(tool.StepEvent{Kind: event.RunEventToolStarted, StepID: identity.StepID,
		ToolName: identity.ToolName, Category: identity.Category,
		SkillName: identity.SkillName, SkillPath: identity.SkillPath, Origin: identity.Origin})
	started := time.Now()
	activation, err := skill.LoadActivation(selection.SkillPath)
	duration := time.Since(started)
	if err == nil && !strings.EqualFold(skill.NormalizeToken(activation.Name), skill.NormalizeToken(selection.SkillName)) {
		err = fmt.Errorf("skill file at %s now names %q", selection.SkillPath, activation.Name)
	}
	if err != nil {
		emit(tool.StepEvent{Kind: event.RunEventToolCompleted, StepID: identity.StepID,
			ToolName: identity.ToolName, Category: identity.Category,
			SkillName: identity.SkillName, SkillPath: identity.SkillPath, Origin: identity.Origin,
			Duration: duration, Error: err.Error()})
		return ctx, &ExplicitSkillLoadError{SkillName: selection.SkillName, SkillPath: selection.SkillPath, Err: err}
	}
	emit(tool.StepEvent{Kind: event.RunEventToolCompleted, StepID: identity.StepID,
		ToolName: identity.ToolName, Category: identity.Category,
		SkillName: identity.SkillName, SkillPath: identity.SkillPath, Origin: identity.Origin,
		Duration: duration})
	return WithExplicitSkillActivation(ctx, selection.SkillName, activation.Content), nil
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

// skillOfferMarker is the phrase the injected offer reminder carries. The
// once-per-session rule reads it back out of the transcript-visible messages,
// so a reminder persists across requests, resumes, and compactions exactly the
// way the plan-mode reminders do.
const skillOfferMarker = "Skill worth keeping"

// skillOfferCommandName is the skill whose slash command the reminder names.
// It must be the name the generator skill is installed under: the command the
// model proposes only exists while that skill does (see the availability
// suppression below).
const skillOfferCommandName = skill.DefaultSkillOfferCommand

// Gate thresholds. They deliberately sit in one place: real-machine acceptance
// tunes them against actual noise, and nothing else may restate them.
const (
	// skillOfferMinToolResults and skillOfferMinDistinctTools are the
	// breadth branch: enough steps, drawn from enough tools, that the run is
	// unlikely to be a single lookup.
	skillOfferMinToolResults   = 8
	skillOfferMinDistinctTools = 3
	// skillOfferRecoveredMinToolResults is the depth branch: fewer steps, but
	// the run hit a failure and found its way past it — exactly the knowledge
	// that is hardest to rediscover.
	skillOfferRecoveredMinToolResults = 5
)

// skillOfferLLM is the gate around the conversation: the standing criteria in
// the frozen catalog decide what is worth keeping, and this wrapper decides
// whether this run has earned the question. The division is deliberate — the
// gate counts what it can count (tool results, distinct tools, recovered
// failures, one reminder already in the history) and never judges whether the
// work itself deserves a skill; that judgment stays with the model, which can
// still stay silent when the gate is open.
//
// Everything the gate knows is derived from msgs and ctx, never from runtime
// state: counting on the transcript-visible slice is what keeps the decision
// drift-free after compaction and correct after a resume, the same property
// analyzePlanReminders bought the plan-mode reminders.
type skillOfferLLM struct {
	inner llm.LLM
	// appCfg carries the features.skill_offer switch. Nil reads as defaults.
	appCfg *appcfg.Root
	// loadedSkills resolves the loaded-skill catalog at call time. The
	// generator skill must be in it: if the skill is unavailable, or turned
	// off from /skills, its command is gone and a proposal would name
	// something that cannot run. It is a getter because the catalog is owned
	// by the tool state, which is built later in the same Load that wraps this
	// chain — capturing the pointer at wrap time would pin nil or a stale one.
	loadedSkills func() *tool.State
	// stateRoot resolves the session mode, so an offer is not proposed while
	// the session is planning — work still in plan mode has not run yet.
	stateRoot string
	// project is the frozen launch project. An offer proposes writing into the
	// project's skills directory; without a version-controlled project root
	// there is nowhere to write and the proposal is an empty promise.
	project safety.ProjectContext
	// commandName is the slash command the reminder names. Blank falls back to
	// skillOfferCommandName.
	commandName string
	anchor      *reminderRunAnchor
}

func wrapSkillOfferLLM(inner llm.LLM, appCfg *appcfg.Root, loadedSkills func() *tool.State, stateRoot string, project safety.ProjectContext) llm.LLM {
	if inner == nil {
		return nil
	}
	return &skillOfferLLM{
		inner:        inner,
		appCfg:       appCfg,
		loadedSkills: loadedSkills,
		stateRoot:    strings.TrimSpace(stateRoot),
		project:      project,
		anchor:       newReminderRunAnchor(),
	}
}

func (w *skillOfferLLM) Execute(ctx context.Context, msgs []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	if w.suppressed(ctx, msgs) {
		return w.inner.Execute(ctx, msgs, tools)
	}
	runID := strings.TrimSpace(tool.RunIDFromContext(ctx))
	reminder, insertAt := w.anchor.anchor(runID, msgs, w.reminder())
	message := planReminderMessage(reminder)
	// The anchor may already hold this exact reminder because the loop adopted
	// it on an earlier iteration; a second copy would send the same paragraph
	// twice and break the append-only prefix the anchor preserves.
	if insertAt < len(msgs) && planReminderMessagesEqual(msgs[insertAt], message) {
		return w.inner.Execute(ctx, msgs, tools)
	}
	recordReminderAdoption(ctx, message, insertAt)
	return w.inner.Execute(ctx, insertPlanReminderMessage(msgs, message, insertAt), tools)
}

// suppressed reports whether this request must not carry an offer reminder.
// Every condition is a hard suppression from the design: any one of them makes
// a proposal either impossible to act on or explicitly unwanted.
func (w *skillOfferLLM) suppressed(ctx context.Context, msgs []llm.Message) bool {
	if w == nil || w.inner == nil {
		return true
	}
	// features.skill_offer off: the switch turns off the proposing, not the
	// command — /skill-generator itself keeps working.
	if !w.appCfg.EffectiveFeatures().SkillOffer {
		return true
	}
	// No version-controlled project root: there is no project skills directory
	// to write into, so the proposal could not be kept.
	if strings.TrimSpace(w.project.Project.Root) == "" || !w.project.Project.VersionControlled {
		return true
	}
	// The generator skill unavailable or disabled: its command is gone, so
	// there is nothing for the user to accept with.
	if w.loadedSkills == nil {
		return true
	}
	if _, available := loadedSkillByName(w.loadedSkills(), skillOfferCommandName); !available {
		return true
	}
	// Proposals belong to the main thread. A subagent or fork child's output
	// belongs to its own view, and the parent would never see the offer.
	if tool.IsForkChildFromContext(ctx) || strings.TrimSpace(tool.SubagentTypeFromContext(ctx)) != "" {
		return true
	}
	// A run the generator itself started must not propose generating.
	if selection, ok := explicitSkillSelectionFromContext(ctx); ok &&
		skill.NormalizeToken(selection.SkillName) == skill.NormalizeToken(skillOfferCommandName) {
		return true
	}
	// One offer per session: the marker in any persisted reminder counts.
	if skillOfferAlreadyInHistory(msgs) {
		return true
	}
	// Plan mode: planned work has not run yet, so there is nothing finished to
	// keep. Checked late because it is the one suppression that touches disk.
	if w.inPlanMode(ctx) {
		return true
	}
	// The gate itself: enough evidence that this run carried real work.
	sig := analyzeSkillOfferSignals(msgs)
	broad := sig.toolResults >= skillOfferMinToolResults && sig.distinctTools >= skillOfferMinDistinctTools
	recovered := sig.recoveredFailure && sig.toolResults >= skillOfferRecoveredMinToolResults
	return !broad && !recovered
}

// inPlanMode reports whether the session driving this request is currently in
// plan mode.
func (w *skillOfferLLM) inPlanMode(ctx context.Context) bool {
	sid := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
	if sid == "" || w.stateRoot == "" {
		return false
	}
	st, err := state.Get(w.stateRoot, sid)
	if err != nil {
		// An unreadable mode cannot rule out plan mode, and an offer made
		// during planning is exactly the one the design forbids.
		return true
	}
	return st.Mode == state.ModePlan
}

// reminder renders the offer reminder. It is turn-scoped and self-terminating:
// it asks the model to judge the work it is finishing now, and says outright
// that it will not recur, so the persisted reminder never becomes a standing
// voice nagging every later turn.
func (w *skillOfferLLM) reminder() string {
	command := strings.TrimSpace(w.commandName)
	if command == "" {
		command = skillOfferCommandName
	}
	return fmt.Sprintf(`%s - one-time note.

This session has just done substantial tool-driven work, which may be worth keeping as a reusable project skill. When you finish this work, judge it against the skill criteria in your instructions: if the steps are worth keeping, propose that in a single sentence at the very end of your final message, naming /%s; if they are not, say nothing about it. This reminder applies to finishing the current work only - it appears once and is not a standing instruction for later turns.`,
		skillOfferMarker, command)
}

// skillOfferSignals is what the gate reads out of the transcript-visible
// messages.
type skillOfferSignals struct {
	// toolResults counts the tool result messages of the current run segment.
	toolResults int
	// distinctTools counts how many different tools those results answered.
	distinctTools int
	// recoveredFailure records that a tool failed and the same tool later
	// succeeded: knowledge bought twice, and the cheapest real signal of work
	// that was not straightforward.
	recoveredFailure bool
	// alreadyOffered records that a reminder is already in the history.
	alreadyOffered bool
}

// analyzeSkillOfferSignals derives the gate's signals from msgs alone. The
// current run segment is everything after the last genuine (non-meta) user
// message: the turns the user is actually watching, not the accumulated
// history. Deriving from the slice rather than from runtime counters is what
// makes the decision identical after a compaction and after a resume.
func analyzeSkillOfferSignals(msgs []llm.Message) skillOfferSignals {
	var sig skillOfferSignals
	segmentStart := 0
	for i, m := range msgs {
		if m.Role == llm.RoleUser && !m.IsMeta {
			segmentStart = i + 1
		}
		if m.Role == llm.RoleUser && m.IsMeta && strings.Contains(llm.TextContent(m.Parts...), skillOfferMarker) {
			sig.alreadyOffered = true
		}
	}
	nameByCallID := make(map[string]string)
	distinct := make(map[string]struct{})
	failed := make(map[string]bool)
	for i := segmentStart; i < len(msgs); i++ {
		m := msgs[i]
		switch m.Role {
		case llm.RoleAssistant:
			for _, tc := range m.ToolCalls {
				if id := strings.TrimSpace(tc.ID); id != "" {
					nameByCallID[id] = strings.TrimSpace(tc.Function.Name)
				}
			}
		case llm.RoleTool:
			sig.toolResults++
			name := nameByCallID[strings.TrimSpace(m.ToolCallID)]
			if name == "" {
				continue
			}
			if _, ok := distinct[name]; !ok {
				distinct[name] = struct{}{}
				sig.distinctTools++
			}
			if strings.HasPrefix(llm.TextContent(m.Parts...), fmt.Sprintf("Error executing tool '%s':", name)) {
				failed[name] = true
			} else if failed[name] {
				sig.recoveredFailure = true
			}
		}
	}
	return sig
}

// skillOfferAlreadyInHistory reports whether a persisted offer reminder is
// already part of the conversation.
func skillOfferAlreadyInHistory(msgs []llm.Message) bool {
	for _, m := range msgs {
		if m.Role == llm.RoleUser && m.IsMeta && strings.Contains(llm.TextContent(m.Parts...), skillOfferMarker) {
			return true
		}
	}
	return false
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
		content, ok := previewEdit(in.FilePath, in.OldString, in.NewString, in.ReplaceAll)
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
func previewEdit(path, oldString, newString string, replaceAll bool) ([]byte, bool) {
	path = strings.TrimSpace(path)
	if path == "" || oldString == "" {
		return nil, false
	}
	abs := path
	if resolved, err := filepath.Abs(path); err == nil {
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

// skillRootChildDir reports whether path is exactly <skill root>/<name>/SKILL.md
// and returns <skill root>/<name>. Both sides are canonicalized first — symlink
// and all — so a path that only reaches the same file through a link is judged
// like the file it is.
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
	}
	return "", false
}
