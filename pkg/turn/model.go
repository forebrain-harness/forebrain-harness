// Canonical turn types: request, outcome, and the shared vocabulary.
package turn

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// Origin identifies the adapter that submitted a turn.
type Origin struct {
	Surface   Surface `json:"surface"`
	ChannelID string  `json:"channel_id,omitempty"`
	RequestID string  `json:"request_id,omitempty"`
}

// Attachment is an adapter-owned file reference.
type Attachment struct {
	ID       string `json:"id,omitempty"`
	Path     string `json:"path"`
	Label    string `json:"label,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
}

// TurnRequest is the canonical input for one user interaction.
type TurnRequest struct {
	SessionID     string            `json:"session_id"`
	Origin        Origin            `json:"origin"`
	UserText      string            `json:"user_text"`
	DisplayText   string            `json:"display_text,omitempty"`
	RawInput      string            `json:"raw_input,omitempty"`
	Parts         []llm.ContentPart `json:"parts,omitempty"`
	PartsJSON     string            `json:"parts_json,omitempty"`
	Attachments   []Attachment      `json:"attachments,omitempty"`
	GoalObjective string            `json:"goal_objective,omitempty"`
	SkillName     string            `json:"skill_name,omitempty"`
	SkillPath     string            `json:"skill_path,omitempty"`
	// Trigger names what caused this turn, and reaches hooks as
	// HookContext.Trigger. It cannot be derived from ExistingRunID: the
	// gateway supervises an existing run for an ordinary "user" turn, while
	// the TUI reuses the same run ID to continue past an approval gate as
	// "resume". Empty means "user".
	Trigger       string `json:"trigger,omitempty"`
	ExistingRunID string `json:"existing_run_id,omitempty"`
	// AgentContextIsRunContext says the caller already built the run context --
	// it holds the cancel function the controller tracks and has seeded the
	// session and run ids. The executor then passes the context through
	// untouched instead of wrapping it, which would register a second cancel
	// for the same run and orphan the caller's.
	AgentContextIsRunContext bool   `json:"-"`
	ParentRunID              string `json:"parent_run_id,omitempty"`
}

// TurnStatus is the terminal or waiting state of a turn.
type TurnStatus string

const (
	TurnCompleted       TurnStatus = "completed"
	TurnWaitingApproval TurnStatus = "waiting_approval"
	TurnCancelled       TurnStatus = "cancelled"
	TurnFailed          TurnStatus = "failed"
)

// TurnOutcome is the canonical result of a submitted turn.
type TurnOutcome struct {
	SessionID string               `json:"session_id"`
	RunID     string               `json:"run_id,omitempty"`
	Status    TurnStatus           `json:"status"`
	Result    *agent.Result        `json:"-"`
	Approval  *ToolApprovalRequest `json:"approval,omitempty"`
	// Resume carries the execution state needed to continue past an approval
	// gate. It is deliberately separate from Approval: Approval is what a
	// surface draws (permission text, destination options, available
	// decisions), while this is how the run picks back up. Mixing the two puts
	// execution state in a type that exists for rendering.
	//
	// Set only when Status is TurnWaitingApproval, and only by an executor
	// that has the state; nil otherwise.
	Resume   *ApprovalResumeState `json:"-"`
	Error    error                `json:"-"`
	Usage    llm.Usage            `json:"usage"`
	Duration time.Duration        `json:"duration"`
}

// ApprovalResumeState is what a surface needs to resume a run that stopped at
// an approval gate: which action it is waiting on, which tool raised it, and
// the session as it stood when the gate fired.
//
// SessionSnapshot includes the assistant tool_use that triggered the gate and
// may include tool_result messages for earlier calls from the same assistant
// message that completed before it. Resuming replays those rather than
// re-executing them.
type ApprovalResumeState struct {
	ActionID        string
	ToolName        string
	SessionSnapshot []llm.Message
}

// EventSink receives canonical events. Implementations must not mutate event.
type EventSink interface {
	Publish(context.Context, event.RunEvent) error
}

var ErrAwaitingToolApproval = errors.New("turn: awaiting tool approval or blocked by pending approval")

type Surface string

const (
	SurfaceTUI     Surface = "tui"
	SurfaceChannel Surface = "channel"
	SurfaceWebChat Surface = "webchat"
)

type Visibility string

const (
	VisibilityPublic    Visibility = "public"
	VisibilityLocalOnly Visibility = "local_only"
	VisibilityHidden    Visibility = "hidden"
)

type Command struct {
	Name                        string
	CanonicalName               string
	Description                 string
	Category                    string
	ArgumentHint                string
	ActionKind                  string
	AllowedModes                []string
	AllowedSurfaces             []Surface
	SupportsInlineArgs          bool
	AvailableDuringRun          bool
	AvailableInSideConversation bool
	FeatureGate                 string
	Visibility                  Visibility
}

type DiscoveryOptions struct {
	DuringRun        bool
	SideConversation bool
	FastAvailable    bool
}

func (c Command) AllowedOn(surface Surface) bool {
	if len(c.AllowedSurfaces) == 0 {
		return true
	}
	for _, allowed := range c.AllowedSurfaces {
		if allowed == surface {
			return true
		}
	}
	return false
}

type CompactSlashHandler interface {
	HandleCompactSlash(ctx context.Context, sessionID, channel string, args []string) (reply string, handled bool)
}

type ClearSlashHandler interface {
	HandleClearSlash(ctx context.Context, sessionID, channel string, args []string) (reply string, handled bool)
}

type ContextSlashHandler interface {
	HandleContextSlash(ctx context.Context, sessionID, channel string, args []string) (reply string, handled bool)
}

// StatusSlashHandler answers /status. It takes no arguments (the executor
// refuses them); side reports that the command came from a side conversation,
// whose own session the report describes.
type StatusSlashHandler interface {
	HandleStatusSlash(sessionID, channel string, side bool) (reply string, handled bool)
}

type PermissionsSlashHandler interface {
	HandlePermissionsSlash(sessionID, channel string, args []string) (reply string, handled bool)
}

// MCPSlashHandler answers /mcp, which takes no arguments (the executor
// refuses them).
type MCPSlashHandler interface {
	HandleMCPSlash(sessionID, channel string) (reply string, handled bool)
}

type SandboxSlashHandler interface {
	HandleSandboxSlash(sessionID, channel string, args []string) (reply string, handled bool)
}

type DiffSlashHandler interface {
	HandleDiffSlash(sessionID, channel string, args []string) (reply string, handled bool)
}

// ModelSlashHandler is where /model reads a session's model settings, applies
// a live selection to the session's runtime, and refreshes the in-memory
// catalog after a default-file write.
type ModelSlashHandler interface {
	ModelSettings(sessionID string) (ModelSettings, error)
	// SelectModel applies choice as this session's live model. effort is nil
	// to derive the model's configured effort once, or an explicit pointer —
	// including "" — to pin that exact effort.
	SelectModel(ctx context.Context, sessionID string, choice ModelChoice, effort *string) error
	// ReloadModelCatalog synchronizes a successful default-file write into
	// this surface without selecting config index zero as the live model.
	ReloadModelCatalog(ctx context.Context, sessionID string) error
}

// ModelSelectionState is the live runtime selection a surface reports: the
// provider/model in force and the concrete reasoning effort it runs at. Set
// is false only when no runtime selection has been published.
type ModelSelectionState struct {
	Provider string
	Model    string
	Effort   string
	Set      bool
}

// ModelSettings is what /model works from: the live config, the agent whose
// models it offers, the config file a choice is written to, and the
// selection the runtime is actually running.
type ModelSettings struct {
	Config     *appcfg.Root
	AgentName  string
	ConfigPath string
	Selection  ModelSelectionState
}

// ModelApplyOutcome classifies how far a model selection got. A plain nil
// error means ModelNotApplied is irrelevant; partial outcomes travel as
// ModelSelectionError, never as bare constants.
type ModelApplyOutcome uint8

const (
	ModelNotApplied ModelApplyOutcome = iota
	ModelAppliedNotDurable
	ModelRuntimeUncertain
)

// modelSelectionOutcome is what the classifier asserts on. Unexported on
// purpose: the exported surface is the concrete error below, and the package
// stays free to add other carriers later.
type modelSelectionOutcome interface {
	ModelApplyOutcome() ModelApplyOutcome
}

// ModelSelectionError reports a partial or uncertain selection outcome
// without making callers parse a surface-specific error message. Err carries
// the underlying cause and stays reachable through Unwrap.
type ModelSelectionError struct {
	Outcome ModelApplyOutcome
	Err     error
}

func (e *ModelSelectionError) Error() string {
	if e == nil {
		return "model selection failed"
	}
	switch e.Outcome {
	case ModelAppliedNotDurable:
		return "model applied but not made durable: " + e.Err.Error()
	case ModelRuntimeUncertain:
		return "model runtime is uncertain: " + e.Err.Error()
	default:
		return e.Err.Error()
	}
}

func (e *ModelSelectionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *ModelSelectionError) ModelApplyOutcome() ModelApplyOutcome {
	if e == nil {
		return ModelNotApplied
	}
	return e.Outcome
}

// NewModelSelectionError is the only constructor surfaces use; outcome must
// be ModelAppliedNotDurable or ModelRuntimeUncertain (a plain error already
// means ModelNotApplied).
func NewModelSelectionError(outcome ModelApplyOutcome, err error) error {
	if err == nil {
		return nil
	}
	return &ModelSelectionError{Outcome: outcome, Err: err}
}

// SelectionApplyOutcome classifies err: a nil error and ordinary errors mean
// ModelNotApplied; the partial outcomes answer through the interface above.
func SelectionApplyOutcome(err error) ModelApplyOutcome {
	var carrier modelSelectionOutcome
	if errors.As(err, &carrier) {
		return carrier.ModelApplyOutcome()
	}
	return ModelNotApplied
}

// AgentSlashHandler lists the primary agents /agent offers and switches to
// the one chosen.
type AgentSlashHandler interface {
	PrimaryAgents() ([]PrimaryAgent, error)
	SwitchPrimaryAgent(ctx context.Context, id string) (PrimaryAgent, error)
}

// PrimaryAgent is one primary agent: a tenant with a workspace of its own.
type PrimaryAgent struct {
	ID            string
	WorkspaceRoot string
	Active        bool
}

// Picker is a choice a command asks for, drawn from data every surface
// reads the same way: what to choose among, which item is in force, and the
// command a choice goes back to through Choose.
type Picker struct {
	Command string       `json:"command"`
	Title   string       `json:"title"`
	Hint    string       `json:"hint,omitempty"`
	Items   []PickerItem `json:"items"`
}

// PickerItem is one choice a Picker offers.
type PickerItem struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Current     bool   `json:"current,omitempty"`
}

// SlashChoice is a choice made in a Picker: the command that offered it and
// the chosen item's value.
type SlashChoice struct {
	Command string `json:"command"`
	Value   string `json:"value"`
}

// FastSlashHandler routes /fast invocations from the slash executor to the
// owning ChatSession. The handler returns a human-readable reply and a
// handled flag; an unhandled result lets the executor fall back to the
// generic "fast: unavailable" message. Implementations should reject when
// the active model is not Anthropic Opus.
type FastSlashHandler interface {
	HandleFastSlash(sessionID, channel string, args []string) (reply string, handled bool)
}

type MemoriesSlashHandler interface {
	HandleMemoriesSlash(ctx context.Context, sessionID, channel string, args []string) (reply string, handled bool)
}

type SessionStore interface {
	Ensure(ctx context.Context, id string, title string) error
	ListSessionsRecent(ctx context.Context, limit int) ([]state.SessionSummary, error)
	// ForkInto makes target the same conversation as source, as stored.
	ForkInto(ctx context.Context, sourceID, targetID string) error
	SetTitle(ctx context.Context, id string, title string) error
	SetParentSessionID(ctx context.Context, id string, parentSessionID string) error
	ListChildSessionsRecent(ctx context.Context, parentSessionID string, limit int) ([]state.SessionSummary, error)
	// CopySessionModelSelection copies the source session's stored model
	// choice onto the target session. copied=false means the source had no
	// stored choice; a missing target is an error.
	CopySessionModelSelection(ctx context.Context, source, target string) (copied bool, err error)
}

type Context struct {
	// CommandContext bounds blocking slash handlers (notably /compact). It is
	// supplied by interactive surfaces so cancellation can reach provider and
	// database work instead of being detached onto context.Background().
	CommandContext context.Context
	Home           string
	// StateRoot is the active primary agent's per-agent state root (its
	// workspace root), onto which the mode/plan/todo stores join "state".
	// Constructors must fill it (via primaryagent.ActiveStateRoot) so slash
	// commands read/write the same isolated state the agent runtime uses.
	// Falls back to Home/workspace when empty (see stateRoot()).
	StateRoot string
	// ProjectRoot is the session's frozen launch-project directory, or empty
	// when the session has none. Project-scope listings (/skills) classify
	// against it instead of guessing a project from the process.
	ProjectRoot      string
	ProjectKey       string
	SessionID        string
	Channel          string
	Surface          Surface
	SessionSource    string
	RunID            string
	DuringRun        bool
	SideConversation bool
	FastAvailable    bool
	Sessions         SessionStore
	Compact          CompactSlashHandler
	Clear            ClearSlashHandler
	ContextDebug     ContextSlashHandler
	Status           StatusSlashHandler
	Permissions      PermissionsSlashHandler
	MCP              MCPSlashHandler
	Sandbox          SandboxSlashHandler
	Diff             DiffSlashHandler
	Model            ModelSlashHandler
	Agent            AgentSlashHandler
	Fast             FastSlashHandler
	Memories         MemoriesSlashHandler
}

type Result struct {
	Handled           bool
	Reply             string
	ShouldContinueRun bool
	ContinueInput     string
	ExitRequested     bool
	SessionChanged    bool
	SessionSwitched   bool
	SessionID         string
	SessionTitle      string
	SelectSession     bool
	ManagePermissions bool
	SelectSkill       bool
	ModeChanged       bool
	Mode              string
	Phase             string
	ForcePlan         bool
	// SkillName and SkillPath are set only for an explicit skill slash
	// invocation. They are internal run metadata, not user-authored prompt text.
	// They name the trusted selection — the runner loads the skill itself
	// before the first LLM request — and surfaces never read skill files.
	SkillName string
	SkillPath string

	// GoalObjective, when non-empty, tells the surface to run this turn in
	// goal-continuation mode (see run.Options.GoalObjective). Set only
	// by /goal. Carried alongside ShouldContinueRun/ContinueInput.
	GoalObjective string

	// Picker is a choice the command asks for; the surface shows it and
	// answers through Choose.
	Picker *Picker
}

// SlashOutcome is the result returned to a surface after slash execution.
type SlashOutcome = Result

type TurnSubmission struct {
	SessionID                  string
	Channel                    string
	UserText                   string
	DisplayText                string
	RawInput                   string
	Attachments                []InputAttachment
	GoalObjective              string
	SkillName                  string
	SkillPath                  string
	CallerRendersReturnedError bool
}

type InputAttachment struct {
	Path     string
	MIMEType string
	Label    string
}

type PendingPaste struct {
	ID          string
	Content     string
	Placeholder string
}

type PendingInputPreview struct {
	PendingSteers  []string `json:"pending_steers"`
	RejectedSteers []string `json:"rejected_steers"`
	QueuedMessages []string `json:"queued_messages"`
}

func (p PendingInputPreview) Visible() bool {
	return len(p.PendingSteers) > 0 || len(p.RejectedSteers) > 0 || len(p.QueuedMessages) > 0
}

type ToolApprovalRequest struct {
	SessionID            string
	ActionID             string
	RunID                string
	ToolName             string
	ActionKind           string
	ToolInputJSON        string
	Description          string
	Reason               string
	PermissionToolName   string
	PermissionInput      string
	ExactRuleContent     string
	PrefixRuleContent    string
	DestinationOptions   []safety.PermissionDestination
	SuggestedDestination safety.PermissionDestination
	PermissionMode       safety.PermissionMode
	PermissionReason     string
	BypassSandbox        bool
	AgentID              string
	SubagentType         string
	// ToolStepID is the LLM tool call the gate is holding, when the surface
	// could resolve it. It is the identity of the card the user is looking at,
	// so a display record of the gate can be anchored to that call on replay.
	// Empty when nothing resolves it, which is a legitimate outcome - the
	// request is then shown but no card is claimed for it.
	ToolStepID           string
	SandboxProfile       string
	RequestedProfile     string
	ProfileElevation     bool
	AskFormJSON          string
	PlanFilePath         string
	RequestedPermissions *safety.RequestPermissionsResponse
	OneShotOnly          bool
	AvailableDecisions   []safety.ApprovalDecisionOption
	NetworkApproval      *safety.NetworkApprovalContext
	NetworkPort          int
	PlanReviewModels     []PlanReviewModelOption
	PlanReviews          []PlanReviewNote
}

type PlanReviewModelOption struct {
	Provider string
	Model    string
	Label    string
	Current  bool
}

type PlanReviewNote struct {
	Provider string
	Model    string
	Text     string
	Duration time.Duration
}

type ToolApprovalDecision struct {
	Approved                   bool
	Denied                     bool
	Cancelled                  bool
	Update                     *safety.PermissionUpdate
	AskAnswerJSON              string
	RequestPermissionsResponse *safety.RequestPermissionsResponse
	NetworkPolicyAmendment     *safety.NetworkPolicyAmendment
	ClearContext               bool
	DenyReason                 string
	RequestPlanReview          *PlanReviewModelOption
}

type ToolApprovalDecisionSink interface {
	PromptToolApproval(context.Context, ToolApprovalRequest) (ToolApprovalDecision, error)
}

// StatusInstruction describes one file the merged rules body drew from.
// It is the projection assembly.RuleSource renders from, kept structurally
// identical so a caller can pass the assembly value straight through.
type StatusInstruction = assembly.RuleSource

// StatusPermissions is what a session's permission state adds up to, as
// PermissionsOf resolves it for /status and /permissions alike: the preset
// name is only meaningful when it really matched, so a state between presets
// reports itself as custom, with the two halves it is made of.
type StatusPermissions struct {
	Preset      string // preset label when matched ("Default", …)
	Description string // the matched preset's own words
	Matched     bool
	Approval    string // approval mode, used for the custom form
	Sandbox     string // effective sandbox mode, used for the custom form
	Rules       int    // permission rule count for the session
}

// StatusContext is the context-window gauge. The numbers come from the same
// function the surface's footer gauge uses (CalculateTokenBudgetWithOptions)
// fed with the same input — the last API response's whole prompt — so /status
// can never disagree with the footer.
type StatusContext struct {
	PercentLeft   int
	UsedTokens    int
	WindowTokens  int
	AutoCompactAt int
}

// StatusUsageTotals sums a session's consumption over its run tree, subagent
// runs included. Input is everything the requests sent — uncached input plus
// the provider's cache reads and writes — and the cache split beside it is
// what the hit rate is computed from.
type StatusUsageTotals struct {
	InputTokens     int // uncached + cache read + cache written
	OutputTokens    int
	CacheRead       int
	CacheWritten    int
	Uncached        int
	Requests        int
	CacheHitPercent int // read / (read + written + uncached)
}

// StatusLastTurn is the most recent completed turn's usage.
type StatusLastTurn struct {
	InputTokens     int // uncached + read + written: what the turn cost to send
	OutputTokens    int
	CacheHitPercent int
}

// StatusWork summarizes the plan/todo progress line.
type StatusWork struct {
	PlanSet bool
	Done    int
	Total   int
}

// StatusReport is the whole /status panel: the Status tab's identity and
// configuration lines plus the Usage tab's gauges. It is a pure projection —
// every number is engine-persisted or computed by the same function the
// surface's own gauge uses.
type StatusReport struct {
	Version     string
	SessionName string
	SessionID   string
	// Side marks a report built for a side conversation: it describes that
	// conversation's own session, and says so.
	Side         bool
	Directory    string
	AgentID      string
	Mode         string
	Phase        string // shown only in plan mode
	Proxy        string // userinfo stripped
	ModelLine    string // "provider / model · fast off"
	Endpoint     string // userinfo and query stripped
	Permissions  StatusPermissions
	Sandbox      string
	MCP          MCPCountLine
	Instructions []StatusInstruction
	ConfigFiles  []string
	// SkillOfferOff, when non-empty, is the rendered "off (setting)" line; the
	// row appears only when the feature is off, because that is the only time
	// the answer changes what the user does next.
	SkillOfferOff string
	Context       StatusContext
	Session       StatusUsageTotals
	LastTurn      StatusLastTurn
	Work          StatusWork
}

// StatusRunStore is the run-store slice /status reads. It is the smallest
// surface the assembly needs, so a test can supply a fake without a database.
type StatusRunStore interface {
	SessionUsageForSession(ctx context.Context, sessionID string) (state.SessionUsage, error)
	LastRunUsageForSession(ctx context.Context, sessionID string) (state.LastRunUsage, error)
}

// StatusSource carries the surface-specific data /status needs. The assembly
// is shared; only how each surface reaches the data differs.
type StatusSource struct {
	// Identity. SessionID must be resolved already; the panel shows it whole.
	// SessionName is the session's title, empty when it has none.
	Version     string
	SessionName string
	SessionID   string
	Directory   string
	AgentID     string
	Side        bool
	// StateRoot is the active primary agent's per-agent state root.
	StateRoot string
	// ProjectKey is the run's launch-project key.
	ProjectKey string
	RunStore   StatusRunStore
	// Provider, Model and Endpoint are the active agent's primary model facts.
	Provider string
	Model    string
	Endpoint string
	FastOn   bool
	// Permissions is the resolved line (preset match already attempted).
	Permissions StatusPermissions
	// Sandbox is the one-line summary from the same source as /sandbox.
	Sandbox string
	// MCP is exactly what /mcp is built from: the MCP row counts the entries
	// of that inventory, so /status and /mcp agree by construction.
	MCP MCPInventorySource
	// Instructions is the rules-cache source list from the assembly PreHook.
	Instructions []StatusInstruction
	// ConfigFiles lists the config files the session's configuration was
	// actually read from.
	ConfigFiles []string
	// SkillOffer is the resolved features.skill_offer value.
	SkillOffer bool
	// ContextUsage feeds the gauge: the same token count (the last API
	// response's whole prompt) and explicit limit the surface's footer uses,
	// so the two cannot disagree.
	ContextUsage func() (tokenUsage, explicitLimit int)
}

// BuildStatusReport assembles the /status projection for a session. Errors in
// optional data (usage store, plan, todos) leave the affected rows empty
// rather than failing the whole panel: a report the user asked for must render.
func BuildStatusReport(ctx context.Context, src StatusSource) StatusReport {
	sid := strings.TrimSpace(src.SessionID)
	if sid == "" {
		sid = "default"
	}
	rep := StatusReport{
		Version:      strings.TrimSpace(src.Version),
		SessionName:  strings.TrimSpace(src.SessionName),
		SessionID:    sid,
		Side:         src.Side,
		Directory:    strings.TrimSpace(src.Directory),
		AgentID:      strings.TrimSpace(src.AgentID),
		Proxy:        StatusProxy(),
		ModelLine:    statusModelLine(src.Provider, src.Model, src.FastOn),
		Endpoint:     SanitizeEndpoint(src.Endpoint),
		Permissions:  src.Permissions,
		Sandbox:      strings.TrimSpace(src.Sandbox),
		Instructions: src.Instructions,
		ConfigFiles:  src.ConfigFiles,
	}
	if !src.SkillOffer {
		rep.SkillOfferOff = "off (features.skill_offer)"
	}
	// Mode and phase come from the agent state store.
	if st, err := state.Get(src.StateRoot, sid); err == nil {
		rep.Mode = strings.TrimSpace(string(st.Mode))
		if rep.Mode == "" {
			rep.Mode = string(state.ModeAgent)
		}
		if rep.Mode == string(state.ModePlan) {
			rep.Phase = strings.TrimSpace(st.Phase)
		}
	}
	rep.MCP = BuildMCPInventory(src.MCP).Counts()
	// Work: plan existence and todo progress.
	if planText, err := state.GetPlanForProject(src.StateRoot, src.ProjectKey); err == nil {
		rep.Work.PlanSet = strings.TrimSpace(planText) != ""
	}
	if todos, err := state.Load(src.StateRoot, sid); err == nil {
		rep.Work.Total = len(todos.Items)
		for _, item := range todos.Items {
			if item.Status == state.StatusCompleted {
				rep.Work.Done++
			}
		}
	}
	// Usage tab.
	estimated := 0
	explicitLimit := 0
	if src.ContextUsage != nil {
		estimated, explicitLimit = src.ContextUsage()
	}
	rep.Context = ContextGaugeOf(src.Provider, src.Model, estimated, explicitLimit)
	if src.RunStore != nil {
		if totals, err := src.RunStore.SessionUsageForSession(ctx, sid); err == nil {
			rep.Session = SessionUsageTotalsOf(totals)
		}
		if last, err := src.RunStore.LastRunUsageForSession(ctx, sid); err == nil {
			rep.LastTurn = StatusLastTurn{
				InputTokens:     last.PromptTokens + last.CacheReadTokens + last.CacheWriteTokens,
				OutputTokens:    last.CompletionTokens,
				CacheHitPercent: CacheHitPercent(last.CacheReadTokens, last.CacheWriteTokens, last.PromptTokens),
			}
		}
	}
	return rep
}

// SessionUsageTotalsOf reads a session's stored usage the way every surface
// reports it: input is everything the requests sent, and the cache split beside
// it is what the hit rate is computed from.
func SessionUsageTotalsOf(totals state.SessionUsage) StatusUsageTotals {
	return StatusUsageTotals{
		InputTokens:     totals.PromptTokens + totals.CacheReadTokens + totals.CacheWriteTokens,
		OutputTokens:    totals.CompletionTokens,
		CacheRead:       totals.CacheReadTokens,
		CacheWritten:    totals.CacheWriteTokens,
		Uncached:        totals.PromptTokens,
		Requests:        totals.LLMCalls,
		CacheHitPercent: CacheHitPercent(totals.CacheReadTokens, totals.CacheWriteTokens, totals.PromptTokens),
	}
}

// CacheHitPercent is the provider-level cache-hit rate: read / (read + written
// + uncached input). Zero denominator, zero percent — there was nothing to hit.
func CacheHitPercent(read, written, uncached int) int {
	total := read + written + uncached
	if total <= 0 {
		return 0
	}
	return read * 100 / total
}

// FormatTokens renders a token count the way the composer footer does: exact
// under a thousand, then k, then M with trailing zeros trimmed. /status uses it
// so its numbers read exactly like the footer's.
func FormatTokens(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	if n < 10000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	if n < 1000000 {
		return strconv.Itoa(n/1000) + "k"
	}
	m := fmt.Sprintf("%.2f", float64(n)/1e6)
	m = strings.TrimRight(m, "0")
	return strings.TrimRight(m, ".") + "M"
}

// StatusProxy is the process proxy for the status panel: the first proxy
// variable set among HTTPS_PROXY / HTTP_PROXY / ALL_PROXY, userinfo stripped.
func StatusProxy() string {
	for _, key := range []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return SanitizeEndpoint(v)
		}
	}
	return ""
}

// SanitizeEndpoint strips userinfo and query from a URL-shaped endpoint so a
// panel never renders credentials that were embedded in a URL.
func SanitizeEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" && u.Host == "" && u.Opaque == "" {
		return raw
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func statusModelLine(provider, model string, fastOn bool) string {
	line := strings.TrimSpace(provider)
	if value := strings.TrimSpace(model); value != "" {
		if line != "" {
			line += " / "
		}
		line += value
	}
	if line == "" {
		line = "unknown"
	}
	if fastOn {
		line += " · fast on"
	} else {
		line += " · fast off"
	}
	return line
}

// StatusFact is one "Label: value" row of a /status tab. Rows with no data are
// never produced — a field is omitted rather than shown as "-" or "0".
type StatusFact struct {
	Label string
	Value string
}

// StatusAgentLine joins the agent, its mode and — in plan mode — the phase.
func StatusAgentLine(rep StatusReport) string {
	agent := rep.AgentID
	if rep.Mode != "" {
		if agent != "" {
			agent += " · "
		}
		agent += rep.Mode
		if rep.Phase != "" {
			agent += " (" + rep.Phase + ")"
		}
	}
	return agent
}

// StatusFacts is the Status tab: identity and configuration, in panel order.
// Both the TUI panel and the markdown rendering read these rows, so the two
// surfaces cannot show different facts.
func StatusFacts(rep StatusReport) []StatusFact {
	var facts []StatusFact
	add := func(label, value string) {
		if strings.TrimSpace(value) != "" {
			facts = append(facts, StatusFact{Label: label, Value: value})
		}
	}
	add("Version", rep.Version)
	add("Session name", rep.SessionName)
	add("Session ID", rep.SessionID)
	add("Directory", rep.Directory)
	add("Agent", StatusAgentLine(rep))
	add("Proxy", rep.Proxy)
	add("Model", rep.ModelLine)
	add("Endpoint", rep.Endpoint)
	add("Permissions", RenderPermissionsLine(rep.Permissions))
	add("Sandbox", rep.Sandbox)
	if counts := rep.MCP.Render(); counts != "" {
		add("MCP servers", counts+" · /mcp")
	}
	add("Instructions", RenderInstructionsLine(rep.Instructions))
	add("Config files", strings.Join(rep.ConfigFiles, ", "))
	add("Skill offer", rep.SkillOfferOff)
	return facts
}

// UsageFacts is the Usage tab: the context gauge, the session's consumption
// and cache hit rate, the last turn, and plan/todo progress.
func UsageFacts(rep StatusReport) []StatusFact {
	var facts []StatusFact
	add := func(label, value string) {
		if strings.TrimSpace(value) != "" {
			facts = append(facts, StatusFact{Label: label, Value: value})
		}
	}
	if rep.Context.WindowTokens > 0 && rep.Context.UsedTokens > 0 {
		add("Context", fmt.Sprintf("%s %d%% left · %s of %s",
			ContextGaugeBar(rep.Context.PercentLeft), rep.Context.PercentLeft,
			FormatTokens(rep.Context.UsedTokens), FormatTokens(rep.Context.WindowTokens)))
	}
	if rep.Context.AutoCompactAt > 0 {
		add("Auto-compact at", FormatTokens(rep.Context.AutoCompactAt))
	}
	// A provider that reports no usage leaves the counts unknown, not zero:
	// only what was reported is shown.
	var session []string
	if rep.Session.InputTokens > 0 {
		session = append(session, FormatTokens(rep.Session.InputTokens)+" input")
	}
	if rep.Session.OutputTokens > 0 {
		session = append(session, FormatTokens(rep.Session.OutputTokens)+" output")
	}
	if rep.Session.Requests > 0 {
		session = append(session, countNoun(rep.Session.Requests, "request"))
	}
	add("Session", strings.Join(session, " · "))
	if rep.Session.CacheRead > 0 || rep.Session.CacheWritten > 0 {
		cache := []string{fmt.Sprintf("%d%%", rep.Session.CacheHitPercent)}
		for _, part := range []struct {
			n     int
			label string
		}{{rep.Session.CacheRead, "read"}, {rep.Session.CacheWritten, "written"}, {rep.Session.Uncached, "uncached"}} {
			if part.n > 0 {
				cache = append(cache, FormatTokens(part.n)+" "+part.label)
			}
		}
		add("Cache hit rate", strings.Join(cache, " · "))
	}
	if rep.LastTurn.InputTokens > 0 || rep.LastTurn.OutputTokens > 0 {
		add("Last turn", fmt.Sprintf("%s input · %s output · %d%% cached",
			FormatTokens(rep.LastTurn.InputTokens), FormatTokens(rep.LastTurn.OutputTokens), rep.LastTurn.CacheHitPercent))
	}
	if rep.Work.PlanSet || rep.Work.Total > 0 {
		work := "no plan"
		if rep.Work.PlanSet {
			work = "plan set"
		}
		if rep.Work.Total > 0 {
			work += fmt.Sprintf(" · %d of %d todos done", rep.Work.Done, rep.Work.Total)
		}
		add("Work", work)
	}
	return facts
}

// ContextGaugeBar draws the Usage tab's context bar: twenty cells, the used
// share filled.
func ContextGaugeBar(percentLeft int) string {
	const cells = 20
	if percentLeft < 0 {
		percentLeft = 0
	}
	if percentLeft > 100 {
		percentLeft = 100
	}
	used := (100 - percentLeft) * cells / 100
	return strings.Repeat("█", used) + strings.Repeat("░", cells-used)
}

// countNoun renders "1 request" / "3 requests".
func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// StatusTitle names the report; a side conversation says whose session it
// describes.
func StatusTitle(rep StatusReport) string {
	if rep.Side {
		return "Session Status · side conversation"
	}
	return "Session Status"
}

// RenderStatusMarkdown renders the report for the web and non-TTY surfaces:
// the same rows as the TUI panel, in two sections, Status then Usage.
func RenderStatusMarkdown(rep StatusReport) string {
	var b strings.Builder
	b.WriteString(StatusTitle(rep) + "\n\n")
	for _, f := range StatusFacts(rep) {
		fmt.Fprintf(&b, "- %s: %s\n", f.Label, f.Value)
	}
	if usage := UsageFacts(rep); len(usage) > 0 {
		b.WriteString("\nUsage\n")
		for _, f := range usage {
			fmt.Fprintf(&b, "- %s: %s\n", f.Label, f.Value)
		}
	}
	return strings.TrimSpace(b.String())
}

// RenderPermissionsLine names the preset when the session's state is one, and
// otherwise the two halves the custom state is made of.
func RenderPermissionsLine(p StatusPermissions) string {
	parts := make([]string, 0, 4)
	if p.Matched && p.Preset != "" {
		parts = append(parts, p.Preset)
	} else {
		parts = append(parts, "Custom")
		for _, half := range []string{p.Approval, p.Sandbox} {
			if half = strings.TrimSpace(half); half != "" {
				parts = append(parts, half)
			}
		}
	}
	if p.Rules > 0 {
		parts = append(parts, countNoun(p.Rules, "rule"))
	}
	return strings.Join(parts, " · ")
}

// RenderInstructionsLine lists the instruction files in load order, each with
// what became of it: agent bootstrap files are marked, a file that did not
// make it into the merged body says why, and a cut one says where it was cut.
func RenderInstructionsLine(sources []StatusInstruction) string {
	parts := make([]string, 0, len(sources))
	for _, src := range sources {
		name := strings.TrimSpace(src.Name)
		if name == "" {
			continue
		}
		if src.Agent {
			name += " (agent)"
		}
		switch {
		case !src.Loaded:
			name += " (not loaded: " + src.NotLoadedReason + ")"
		case src.TruncatedTo > 0:
			name += " (truncated to " + FormatTokens(src.TruncatedTo) + " chars)"
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}

// AssistantOutcomeText resolves the assistant text and the reasoning text a
// finished turn should persist.
//
// Reasoning comes from the provider when it returns it, and is otherwise
// recovered from the accumulated session. When the assistant text is nothing
// but that same reasoning wrapped in a thinking fence, it is dropped: some
// providers echo their reasoning as the visible answer, and persisting both
// stores the same content twice and replays it to the model on the next turn.
//
// Both surfaces need this. The terminal had the echo check and the gateway did
// not, so the same run persisted a duplicated answer on the web and not in the
// terminal; the terminal's behaviour is the one that is right.
func AssistantOutcomeText(res *agent.Result) (text string, reasoning string) {
	if res == nil {
		return "", ""
	}
	text = res.TextContent()
	reasoning = strings.TrimSpace(res.Reasoning)
	if reasoning == "" && len(res.Session) > 0 {
		reasoning = state.ExtractReasoningText(res.Session)
	}
	if reasoning != "" && isReasoningOnlyAssistantEcho(text, reasoning) {
		text = ""
	}
	return text, reasoning
}

// isReasoningOnlyAssistantEcho reports whether the assistant text is exactly
// the reasoning text in a thinking fence and nothing else. The comparison is
// equality on purpose: an answer that merely quotes some of its own reasoning
// is still an answer, and trimming it would discard content the user saw.
func isReasoningOnlyAssistantEcho(outText string, reasoningText string) bool {
	outText = strings.TrimSpace(outText)
	reasoningText = strings.TrimSpace(reasoningText)
	if outText == "" || reasoningText == "" {
		return false
	}
	return outText == "```thinking\n"+reasoningText+"\n```"
}
