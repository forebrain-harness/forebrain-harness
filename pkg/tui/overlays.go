// Modal overlays: approvals, questions, and the rich selector.
package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"golang.org/x/term"
)

// approvalOverlay renders the dedicated tool-approval UI: command header,
// decision-specific choices and an optional explain pane. It drives rawSelector
// key input directly — it does NOT call rawSelector.Select. The overlay
// renders IN the viewport surface as a bottom-pinned composer block with a
// separator rule above; the transcript stays visible and text-selectable.
type approvalOverlay struct {
	// ctx is the request's context: it cancels a wait for the input while
	// another surface owns it, so a cancelled run's approval leaves the queue
	// without an answer.
	ctx            context.Context
	out            io.Writer
	rs             *rawSelector
	req            turn.ToolApprovalRequest
	options        []overlayChoice
	cursor         int
	showExplain    bool
	showHelp       bool
	feedbackBuf    []rune
	feedbackCursor int
	// planDenyEditing is true when the user is entering feedback after choosing
	// to keep planning. feedbackBuf/feedbackCursor hold that text.
	planDenyEditing bool
	// feedbackFirstRow is the index, within the lines the overlay last built,
	// of the first row of the feedback field. feedbackLayout is the exact visual
	// layout rendered there, shared by cursor placement, vertical movement and
	// mouse hit-testing.
	feedbackFirstRow int
	feedbackLayout   approvalFeedbackLayout
	// planReviewPicking is true while the user is choosing which model should
	// review the plan. planReviewCursor indexes req.PlanReviewModels.
	planReviewPicking bool
	planReviewCursor  int
	// confirmFn is called with the decision before Run() returns (and before
	// the deferred release/EndOverlay) so the approval confirmation frame is
	// added to the viewport without a race window against async tool
	// notifications from the paused run.
	confirmFn func(turn.ToolApprovalRequest, turn.ToolApprovalDecision)
	// keepOptionsInView is true for renders that must bring the option rows
	// back on screen: the initial open and every key that changes what is
	// selected or shown. The scroll keys clear it so paging up through a tall
	// diff is not undone by the repaint that follows the key.
	keepOptionsInView bool
	// diffBody memoises the styled diff for the current width and theme. The
	// request is immutable for the overlay's lifetime, so the syntax
	// highlighting runs once instead of on every keystroke
	// (buildApprovalContent runs on each key, and a whole-file diff is far too
	// expensive to re-highlight that often).
	diffBody approvalDiffCache
}

// approvalDiffCache holds one rendered diff body keyed by the render inputs
// that can change while the overlay is open.
type approvalDiffCache struct {
	valid bool
	width int
	theme DiffTheme
	lines []string
}

// approvalFeedbackLayout is the one visual layout of the feedback field that
// rendering, the hardware cursor, vertical movement and mouse hit-testing all
// read. lines are the rows exactly as they are painted (prefix included); rows
// carries each row's prefix width and the half-open feedbackBuf rune range it
// shows; cursorRow/cursorCol are the caret's position within the field, with
// the column measured from the start of the row including its prefix.
type approvalFeedbackLayout struct {
	lines     []string
	rows      []composerTextRow
	cursorRow int
	cursorCol int
}

// overlayChoice holds the server-derived decision represented by one row.
type overlayChoice struct {
	label       string
	dst         safety.PermissionDestination
	allowOnce   bool
	denyOnce    bool
	cancel      bool
	ruleContent string
	isPlanDeny  bool
	// isPlanReview opens the reviewer picker instead of deciding the approval.
	isPlanReview           bool
	clearContext           bool
	strictReview           bool
	networkPolicyAmendment *safety.NetworkPolicyAmendment
	commandPrefix          safety.ExecPolicyAmendment
	// commandRules is the server-proposed rule set a remembered Bash command
	// stores, which replaces ruleContent when the row carries one.
	commandRules []safety.PermissionRuleValue
}

// rememberCommandLabel words the persistent command choice from the scope the
// service derived for it. The engine decides what the choice covers; this
// decides only how to say it, which is why every other surface renders the
// same scope in its own language instead of re-deriving the answer.
func rememberCommandLabel(scope safety.CommandApprovalScope) string {
	switch scope.Kind {
	case safety.CommandApprovalScopePrefix:
		if len(scope.Prefixes) == 0 {
			return "Yes, and don't ask again for commands that start with this prefix"
		}
		return fmt.Sprintf("Yes, and don't ask again for commands that start with `%s`", scope.Prefixes[0])
	case safety.CommandApprovalScopeCommandWithVariants:
		// Past a few prefixes the list is longer than the row, and naming some
		// of them would describe less than what is granted.
		if len(scope.Prefixes) > 3 {
			return "Yes, and don't ask again for this command or its variants"
		}
		quoted := make([]string, 0, len(scope.Prefixes))
		for _, prefix := range scope.Prefixes {
			quoted = append(quoted, "`"+prefix+"`")
		}
		return fmt.Sprintf("Yes, and don't ask again for this command or its %s variants", strings.Join(quoted, ", "))
	default:
		// The command is displayed in full above the choices and the rules
		// authorize it alone, so the row says so once. Repeating the text here
		// would wrap the long ones and, worse, read like a pattern.
		return "Yes, and don't ask again for this command"
	}
}

func newApprovalOverlay(ctx context.Context, out io.Writer, rs *rawSelector, req turn.ToolApprovalRequest, confirmFn func(turn.ToolApprovalRequest, turn.ToolApprovalDecision)) *approvalOverlay {
	percentUsed := 0
	if rs != nil && rs.renderer != nil {
		stats := rs.renderer.ComposerTokenStats("")
		if stats.PercentLeft > 0 {
			percentUsed = 100 - stats.PercentLeft
		}
	}
	choices := buildOverlayChoices(req, percentUsed)
	return &approvalOverlay{
		ctx:               ctx,
		out:               out,
		rs:                rs,
		req:               req,
		options:           choices,
		confirmFn:         confirmFn,
		keepOptionsInView: true,
	}
}

func buildOverlayChoices(req turn.ToolApprovalRequest, percentUsed int) []overlayChoice {
	if isApprovalExitPlanMode(req) {
		return buildExitPlanChoices(req, percentUsed)
	}
	return buildProposedOverlayChoices(req)
}

func buildProposedOverlayChoices(req turn.ToolApprovalRequest) []overlayChoice {
	ruleContent, _, _ := overlayRuleContent(req)
	choices := make([]overlayChoice, 0, len(req.AvailableDecisions))
	for _, option := range req.AvailableDecisions {
		switch option.Decision {
		case safety.DecisionAccept:
			label := "Yes, proceed"
			if isApprovalWebSearch(req) {
				label = "Yes, search the web"
			} else if req.NetworkApproval != nil {
				label = "Yes, just this once"
			}
			choices = append(choices, overlayChoice{label: label, allowOnce: true})
		case safety.DecisionAcceptForSession:
			label := "Yes, and don't ask again for this request in this session"
			switch {
			case req.RequestedPermissions != nil:
				label = "Yes, grant these permissions for this session"
			case req.NetworkApproval != nil:
				label = "Yes, and allow this host for this conversation"
			case isApprovalFileMutation(req):
				label = "Yes, and don't ask again for these files"
			case safety.ApprovalFileTool(approvalToolName(req)):
				label = "Yes, and don't ask again for this file in this session"
			case strings.HasPrefix(strings.ToLower(approvalToolName(req)), "mcp__"):
				label = "Yes, and don't ask again for this tool in this session"
			}
			choices = append(choices, overlayChoice{label: label, dst: safety.DestinationSession, ruleContent: ruleContent})
		case safety.DecisionAcceptAndRemember:
			label := "Yes, and don't ask again for this tool"
			host := strings.TrimSpace(strings.TrimPrefix(ruleContent, "domain:"))
			switch {
			case approvalIsBash(req):
				label = rememberCommandLabel(option.CommandScope)
			case host != "" && host != strings.TrimSpace(ruleContent):
				label = fmt.Sprintf("Yes, and don't ask again for `%s`", host)
			}
			choices = append(choices, overlayChoice{
				label: label, dst: safety.DestinationLocalSettings, ruleContent: ruleContent,
				commandRules: append([]safety.PermissionRuleValue(nil), option.CommandRules...),
			})
		case safety.DecisionAcceptWithExecPolicyAmendment:
			if len(option.ExecPolicyAmendment) > 0 {
				choices = append(choices, overlayChoice{
					label: rememberCommandLabel(option.CommandScope), dst: safety.DestinationLocalSettings,
					commandPrefix: append(safety.ExecPolicyAmendment(nil), option.ExecPolicyAmendment...),
				})
			}
		case safety.DecisionApplyNetworkPolicyAmendment:
			if option.NetworkPolicyAmendment != nil {
				label := "Yes, and allow this host in the future"
				if option.NetworkPolicyAmendment.Action == safety.NetworkPolicyDeny {
					label = "No, and deny this host in the future"
				}
				amendment := *option.NetworkPolicyAmendment
				choices = append(choices, overlayChoice{label: label, networkPolicyAmendment: &amendment})
			}
		case safety.DecisionGrantForTurn:
			choices = append(choices, overlayChoice{label: "Yes, grant these permissions for this turn", allowOnce: true})
		case safety.DecisionGrantForTurnStrictAutoReview:
			choices = append(choices, overlayChoice{label: "Yes, grant for this turn with strict auto review", allowOnce: true, strictReview: true})
		case safety.DecisionGrantForSession:
			choices = append(choices, overlayChoice{label: "Yes, grant these permissions for this session", dst: safety.DestinationSession})
		case safety.DecisionDecline:
			label := "No, continue without permissions"
			switch {
			case isApprovalMemoryNote(req):
				label = "No, don't save this note"
			case isApprovalMemoryQuery(req):
				label = "No, continue without reading memory"
			}
			choices = append(choices, overlayChoice{label: label, denyOnce: true})
		case safety.DecisionCancel:
			label := "No, and tell Forebrain Harness what to do differently"
			if isApprovalWebSearch(req) {
				label = "No, don't search"
			}
			choices = append(choices, overlayChoice{label: label, cancel: true})
		}
	}
	if len(choices) == 0 {
		return []overlayChoice{{label: "No, and tell Forebrain Harness what to do differently", cancel: true}}
	}
	return choices
}

func buildExitPlanChoices(req turn.ToolApprovalRequest, percentUsed int) []overlayChoice {
	clearLabel := "Yes, clear context and proceed"
	if percentUsed > 0 {
		clearLabel = fmt.Sprintf("Yes, clear context (%d%% used) and proceed", percentUsed)
	}
	choices := []overlayChoice{
		{label: clearLabel, allowOnce: true, clearContext: true},
		{label: "Yes, proceed", allowOnce: true},
		{label: "No, keep planning", denyOnce: true, isPlanDeny: true},
	}
	// The review row is appended rather than inserted so the three verdicts keep
	// the positions their number keys already had. It only appears when the
	// session offered reviewers, which is what keeps a surface without
	// configured models from showing a choice that cannot be taken.
	if len(req.PlanReviewModels) > 0 {
		choices = append(choices, overlayChoice{
			label: "Ask another model to review this plan", isPlanReview: true,
		})
	}
	return choices
}

func overlayRuleContent(req turn.ToolApprovalRequest) (ruleContent string, labelContent string, prefixMode bool) {
	exact := strings.TrimSpace(req.ExactRuleContent)
	prefix := strings.TrimSpace(req.PrefixRuleContent)
	tool := approvalToolName(req)
	canonical := safety.CanonicalToolName(tool)
	if strings.EqualFold(canonical, "Bash") {
		if prefix != "" && prefix != "*" {
			return prefix, prefix, true
		}
		return exact, "", false
	}
	if strings.HasPrefix(tool, "mcp__") {
		if prefix != "" && prefix != "*" {
			return prefix, prefix, true
		}
		if exact != "" {
			return exact, "", false
		}
		return tool, "", false
	}
	if prefix != "" && prefix != "*" {
		return prefix, prefix, true
	}
	if exact != "" {
		return exact, "", false
	}
	if tool != "" {
		return tool, "", false
	}
	return prefix, "", prefix != "" && prefix != "*"
}

// Run acquires raw input, renders the overlay IN the viewport surface as a
// bottom-pinned composer block (with a separator rule above), and blocks until
// the user makes a decision or the operation is cancelled. The transcript stays
// visible and text-selectable with the same mouse semantics as the main turn.
// In viewport mode the renderer mutex is NOT held across readKey — each
// renderViewport call locks it internally via SetOverlayComposer — so a racing
// frame paint can't deadlock. In non-viewport mode the raw-selector mutex is
// held for the overlay's background rendering but is released before the
// confirmation callback runs, so confirmFn (which calls
// PrintApprovalConfirmation → Renderer mutex) never re-enters the same mutex.
func (o *approvalOverlay) Run() (turn.ToolApprovalDecision, error) {
	if o.rs == nil {
		return turn.ToolApprovalDecision{}, fmt.Errorf("tui approvals require an interactive raw terminal")
	}
	release, acquired := o.waitForComposerInput()
	if !acquired {
		// The request's context was cancelled while this approval waited for
		// stdin. There is nobody left to answer it, so it leaves exactly the
		// way a cancelled approval always has.
		return o.dismissDecision(), nil
	}
	defer release()
	// Ignore selection keys that were queued before this overlay owned stdin.
	// Input typed after the first render below remains normal approval input.
	o.rs.discardPreOpenInput()
	// In non-viewport mode, hold the raw-selector mutex to serialise writes
	// against the renderer.  In viewport mode the renderer locks per-render
	// via SetOverlayComposer, so the mutex is NOT held across readKey.
	mutexHeld := false
	if o.rs != nil && !o.rs.inViewportOverlay() && o.rs.mu != nil {
		o.rs.mu.Lock()
		mutexHeld = true
	}
	defer func() {
		if mutexHeld {
			o.rs.mu.Unlock()
		}
	}()
	o.resetFocusForOpen()
	o.rs.resetOverlayViewportState()
	o.renderViewport()
	defer o.rs.withCaretPlacer(o.placeFeedbackCaret)()
	for {
		key, err := o.rs.readKeyWithMode(o.planDenyEditing)
		if err != nil {
			return turn.ToolApprovalDecision{}, err
		}
		switch key.kind {
		case rawKeyNone:
			continue
		case rawKeyMouse:
			// Forward the mouse event to the viewport's text selection
			// system so the user can drag-select text, double-click to
			// select a word, or triple-click to select a line in the
			// overlay content and the transcript above it. The viewport
			// routes press/drag/release for text selection, wheel for
			// scrolling (overlay content or transcript depending on the
			// pointer row), and move for hover highlights.
			// dispatchMouse handles all of these internally.
			o.rs.dispatchMouse(key.mouse)
			continue
		}
		decision, done := o.handleKey(key)
		if done {
			// Release the non-viewport mutex before the confirmation
			// callback so printApprovalConfirmation →
			// PrintApprovalConfirmation can acquire the renderer mutex
			// without re-entrancy.
			if mutexHeld {
				o.rs.mu.Unlock()
				mutexHeld = false
			}
			if o.confirmFn != nil {
				o.confirmFn(o.req, decision)
			}
			return decision, nil
		}
		o.renderViewport()
	}
}

func (o *approvalOverlay) resetFocusForOpen() {
	o.cursor = 0
	o.keepOptionsInView = true
	o.showExplain = false
	o.showHelp = false
	o.planDenyEditing = false
	o.planReviewPicking = false
	o.planReviewCursor = 0
	o.clearFeedback()
}

// clearFeedback drops the feedback field. feedbackLayout addresses feedbackBuf
// by rune range, so the two are only ever cleared together: a layout left
// behind would describe a buffer that no longer exists.
func (o *approvalOverlay) clearFeedback() {
	o.feedbackBuf = nil
	o.feedbackCursor = 0
	o.feedbackLayout = approvalFeedbackLayout{}
	o.feedbackFirstRow = 0
}

// buildApprovalContent renders the overlay body (everything EXCEPT the top
// separator rule) to a flat slice of visual lines plus the 0-based cursor row
// within them. renderViewport hands the lines to the viewport painter, which
// prepends the separator rule and positions the block at the bottom.
// cursorLine points at the feedback editor row when editing, or -1 to
// hide the hardware cursor during option selection (the "> " arrow is the
// indicator).
func (o *approvalOverlay) buildApprovalContent(width int) ([]string, int) {
	lines := make([]string, 0, 24)
	cursorLine := -1
	lineCount := 0
	writeLine := func(format string, args ...any) {
		line := format
		if len(args) != 0 {
			line = fmt.Sprintf(format, args...)
		}
		for _, visual := range approvalOverlayVisualLines(line, width) {
			lines = append(lines, visual)
			lineCount++
		}
	}

	// Command header
	switch {
	case isApprovalWebSearch(o.req):
		writeLine("\x1b[1mWeb search approval\x1b[0m")
	case isApprovalRetrieveOutput(o.req):
		writeLine("\x1b[1mSaved output approval\x1b[0m")
	case isApprovalMemoryNote(o.req):
		writeLine("\x1b[1mMemory note approval\x1b[0m")
	case isApprovalMemoryQuery(o.req):
		writeLine("\x1b[1mMemory access approval\x1b[0m")
	default:
		writeLine("\x1b[1mTool approval\x1b[0m")
	}
	if agentID := strings.TrimSpace(o.req.AgentID); agentID != "" {
		label := agentID
		if subtype := strings.TrimSpace(o.req.SubagentType); subtype != "" {
			label += " (" + subtype + ")"
		}
		writeLine("Requesting subagent: %s", label)
	}
	toolName := approvalToolName(o.req)
	input := approvalDisplayInput(o.req.ToolInputJSON)

	if isApprovalEnterPlanMode(o.req) {
		writeLine("Tool: %s", toolName)
		if reason := approvalEnterPlanReason(o.req); reason != "" {
			writeLine("Reason: %s", reason)
		}
	} else if isApprovalExitPlanMode(o.req) {
		writeLine("Tool: %s", toolName)
		planContent := approvalPlanContent(o)
		if planContent != "" {
			writeLine("")
			writeLine("Here is the plan:")
			writeLine("%s", approvalSeparatorLine(width))
			bodyWidth := width - 4
			if bodyWidth < 20 {
				bodyWidth = 20
			}
			md := renderAssistantMarkdownWithWidth(planContent, bodyWidth, DiffThemeUnknown)
			if md == "" {
				md = planContent
			}
			for _, l := range strings.Split(md, "\n") {
				writeLine("%s", l)
			}
			writeLine("%s", approvalSeparatorLine(width))
		}
		for _, review := range o.req.PlanReviews {
			writeLine("")
			writeLine("\x1b[1mReview by %s\x1b[0m", planReviewModelDisplay(review.Provider, review.Model))
			writeLine("%s", approvalSeparatorLine(width))
			bodyWidth := width - 4
			if bodyWidth < 20 {
				bodyWidth = 20
			}
			md := renderAssistantMarkdownWithWidth(strings.TrimSpace(review.Text), bodyWidth, DiffThemeUnknown)
			if md == "" {
				md = strings.TrimSpace(review.Text)
			}
			for _, l := range strings.Split(md, "\n") {
				writeLine("%s", l)
			}
			writeLine("%s", approvalSeparatorLine(width))
		}
		if planContent != "" || len(o.req.PlanReviews) > 0 {
			writeLine("Would you like to proceed?")
		}
	} else if isApprovalWebSearch(o.req) {
		writeLine("Forebrain Harness wants to search the public web.")
		if query := approvalWebSearchQuery(o.req); query != "" {
			writeLine("Query: %s", query)
		} else {
			writeLine("Query details are unavailable.")
		}
		if limit := approvalWebSearchLimit(o.req); limit > 0 {
			writeLine("Maximum results: %d", limit)
		}
	} else if isApprovalRetrieveOutput(o.req) {
		for _, detail := range retrieveOutputApprovalDetails(o.req) {
			writeLine("%s", detail)
		}
	} else if isApprovalMemoryNote(o.req) {
		writeLine("Action: Save durable memory note")
		filename, note, ok := approvalMemoryNoteInput(o.req)
		if !ok {
			writeLine("Memory note details are unavailable.")
		} else {
			writeLine("Filename: %s", filename)
			writeLine("")
			writeLine("Note preview:")
			writeLine("%s", approvalSeparatorLine(width))
			bodyWidth := width - 4
			if bodyWidth < 20 {
				bodyWidth = 20
			}
			theme := DiffThemeUnknown
			if o.rs != nil && o.rs.renderer != nil {
				theme = o.rs.renderer.DiffTheme()
			}
			md := renderAssistantMarkdownWithWidth(note, bodyWidth, theme)
			if md == "" {
				md = note
			}
			for _, line := range strings.Split(md, "\n") {
				writeLine("%s", line)
			}
			writeLine("%s", approvalSeparatorLine(width))
		}
	} else if isApprovalMemoryQuery(o.req) {
		for _, detail := range memoryQueryApprovalDetails(o.req) {
			writeLine("%s", detail)
		}
	} else if o.req.RequestedPermissions != nil {
		writeLine("Tool: %s", toolName)
		for _, entry := range o.req.RequestedPermissions.Permissions.FileSystem.Entries {
			writeLine("%s: %s", strings.ToUpper(string(entry.Access)), entry.Path.Path)
		}
	} else if approvalHasFileDiff(o.req) {
		fp, diffText := approvalFileDiff(o.req)
		writeLine("Tool: %s", toolName)
		if fp != "" {
			writeLine("Path: %s", fp)
		}
		if reason := approvalJustification(o.req); reason != "" {
			writeLine("Reason: %s", reason)
		}
		if diffText != "" {
			theme := DiffThemeUnknown
			if o.rs != nil && o.rs.renderer != nil {
				theme = o.rs.renderer.DiffTheme()
			}
			for _, l := range o.diffBodyLines(diffText, fp, width, theme) {
				lines = append(lines, l)
				lineCount++
			}
		}
	} else if approvalIsBash(o.req) {
		if cmd := approvalShellCommand(o.req); cmd != "" {
			writeLine("Command: %s", cmd)
		}
		if reason := strings.TrimSpace(o.req.Reason); reason != "" {
			writeLine("Reason: %s", reason)
		}
		if desc := strings.TrimSpace(o.req.Description); desc != "" {
			writeLine("Description: %s", desc)
		}
		if profile := strings.TrimSpace(o.req.SandboxProfile); profile != "" {
			if o.req.ProfileElevation {
				writeLine("Sandbox profile: %s → %s (this command only)", profile, strings.TrimSpace(o.req.RequestedProfile))
			} else {
				writeLine("Sandbox profile: %s", profile)
			}
		}
	} else if toolName != "" {
		writeLine("Tool: %s", toolName)
		if input != "" {
			writeLine("Input: %s", input)
		}
	} else if input != "" {
		writeLine("Input: %s", input)
	}
	if !approvalIsBash(o.req) && !isApprovalEnterPlanMode(o.req) && !isApprovalExitPlanMode(o.req) {
		if o.req.Description != "" {
			writeLine("Description: %s", o.req.Description)
		}
	}
	writeLine("")

	switch {
	case o.planDenyEditing:
		if len(o.req.PlanReviews) > 0 {
			writeLine("\x1b[2mThe review above is sent to Forebrain Harness with your feedback.\x1b[0m")
		}
		// The newline shortcut lives in the footer with the other key hints:
		// spelled out here too, this title runs past 80 columns and wraps.
		writeLine("Feedback for Forebrain Harness (Enter to send, Esc to cancel):")
		o.feedbackFirstRow = lineCount
		o.feedbackLayout = layoutApprovalFeedback(string(o.feedbackBuf), o.feedbackCursor, width)
		for _, line := range o.feedbackLayout.lines {
			lines = append(lines, line)
			lineCount++
		}
		cursorLine = o.feedbackFirstRow + o.feedbackLayout.cursorRow
	case o.planReviewPicking:
		writeLine("Which model should review this plan? (Esc to go back)")
		for i, model := range o.req.PlanReviewModels {
			marker := "  "
			if i == o.planReviewCursor {
				marker = "\x1b[1m> \x1b[0m"
			}
			writeLine("%s%d. %s", marker, i+1, planReviewModelRow(model))
		}
	default:
		for i, opt := range o.options {
			marker := "  "
			if i == o.cursor {
				marker = "\x1b[1m> \x1b[0m"
			}
			writeLine("%s%d. %s", marker, i+1, opt.label)
		}
	}

	if o.showExplain {
		writeLine("")
		writeLine("\x1b[2m--- Explain ---\x1b[0m")
		if isApprovalWebSearch(o.req) {
			if query := approvalWebSearchQuery(o.req); query != "" {
				writeLine("Search query: %s", query)
			}
			if limit := approvalWebSearchLimit(o.req); limit > 0 {
				writeLine("Maximum results: %d", limit)
			}
			writeLine("Access: Public web")
		} else if isApprovalRetrieveOutput(o.req) {
			for _, detail := range retrieveOutputApprovalDetails(o.req) {
				writeLine("%s", detail)
			}
			writeLine("Access: Forebrain Harness's saved tool-output cache")
		} else if isApprovalMemoryNote(o.req) {
			filename, _, ok := approvalMemoryNoteInput(o.req)
			if ok {
				writeLine("Action: Save durable memory note")
				writeLine("Filename: %s", filename)
			} else {
				writeLine("Memory note details are unavailable.")
			}
		} else if isApprovalMemoryQuery(o.req) {
			for _, detail := range memoryQueryApprovalDetails(o.req) {
				writeLine("%s", detail)
			}
			writeLine("Access: Your local memory store")
		} else {
			writeLine("Tool input: %s", truncate(o.req.ToolInputJSON, 800))
		}
		if o.req.Description != "" {
			writeLine("Description: %s", o.req.Description)
		}
		modeStr := string(o.req.PermissionMode)
		if modeStr == "" {
			modeStr = "n/a"
		}
		reason := strings.TrimSpace(o.req.PermissionReason)
		if reason == "" {
			reason = "n/a"
		}
		writeLine("Sandbox: %s (%s)", modeStr, reason)
	}

	if o.showHelp {
		writeLine("")
		writeLine("\x1b[2m--- Keymap ---\x1b[0m")
		writeLine("  ↑/↓ / j/k  move selection")
		writeLine("  1..5       jump to option")
		yesLabel := "Yes, proceed"
		if len(o.options) > 0 {
			yesLabel = o.options[0].label
		}
		noLabel := "No, continue without running it"
		if denyIdx := denyChoiceIndex(o.options); denyIdx >= 0 && denyIdx < len(o.options) {
			noLabel = o.options[denyIdx].label
		}
		writeLine("  y / Y      %s (option 1)", yesLabel)
		writeLine("  n / N      %s (option %d)", noLabel, denyChoiceIndex(o.options)+1)
		if idx := planReviewChoiceIndex(o.options); idx >= 0 {
			writeLine("  m / M      %s (option %d)", o.options[idx].label, idx+1)
		}
		writeLine("  Enter      commit selection")
		writeLine("  PgUp/PgDn  scroll overlay content")
		writeLine("  Home/End   jump to top/bottom of content")
		writeLine("  ctrl+e     toggle Explain pane")
		writeLine("  ?          toggle this Keymap pane")
		if o.dismissDecision().Denied {
			writeLine("  Esc        %s", noLabel)
			writeLine("  ctrl+c     cancel")
		} else {
			writeLine("  Esc/ctrl+c cancel")
		}
	}

	writeLine("")
	switch {
	case o.planDenyEditing:
		writeLine("\x1b[2mEsc to cancel · Ctrl+J/Alt+Enter for newline · Enter to submit feedback\x1b[0m")
	case o.planReviewPicking:
		writeLine("\x1b[2mEsc to go back · Enter to request the review\x1b[0m")
	default:
		dismiss := "Esc to cancel"
		if o.dismissDecision().Denied {
			dismiss = "Esc to decline"
		}
		writeLine("\x1b[2m%s · ctrl+e to explain · ? for keys\x1b[0m", dismiss)
	}

	return lines, cursorLine
}

// renderViewport paints the overlay as a viewport composer block. The painter
// prepends the separator rule and positions the block at the bottom. During
// option selection the hardware cursor is hidden (the "> " arrow is the
// indicator); during feedback editing it is positioned at the edit point.
func (o *approvalOverlay) renderViewport() {
	width, _ := o.termSize()
	lines, cursorLine := o.buildApprovalContent(width)
	cursorCol := 0
	if cursorLine >= 0 && o.planDenyEditing {
		cursorCol = o.feedbackLayout.cursorCol
	}
	if o.rs != nil && o.rs.inViewportOverlay() {
		if o.keepOptionsInView {
			o.rs.setOverlayComposerAtEnd(lines, cursorLine, cursorCol)
		} else {
			// The user scrolled away on purpose. The paint keeps a visible
			// caret on screen by scrolling back to it, which would undo that
			// scroll on the spot, so the caret is reported hidden until the
			// next key re-anchors the overlay on it.
			o.rs.setOverlayComposer(lines, -1, 0)
		}
		return
	}
	// Non-viewport fallback: write lines directly.
	for _, line := range lines {
		_, _ = fmt.Fprintln(o.out, line)
	}
}

func (o *approvalOverlay) handleKey(key parsedKey) (turn.ToolApprovalDecision, bool) {
	if o.planDenyEditing {
		return o.handlePlanDenyKey(key)
	}
	if o.planReviewPicking {
		return o.handlePlanReviewKey(key)
	}
	// Every key re-anchors the overlay on its option rows except the scroll
	// keys below, which exist precisely to move away from them.
	o.keepOptionsInView = true
	switch key.kind {
	case rawKeyUp:
		if o.cursor > 0 {
			o.cursor--
		}
	case rawKeyDown:
		if o.cursor < len(o.options)-1 {
			o.cursor++
		}
	case rawKeyPageUp:
		o.scrollOverlay(-10)
	case rawKeyPageDown:
		o.scrollOverlay(10)
	case rawKeyHome:
		o.scrollOverlay(-overlayScrollToEdge)
	case rawKeyEnd:
		o.scrollOverlay(overlayScrollToEdge)
	case rawKeyEnter:
		// If the selected option is a "keep planning" deny, enter feedback
		// text input mode instead of immediately denying.
		if o.cursor < len(o.options) && o.options[o.cursor].isPlanDeny {
			o.enterPlanDenyMode()
			return turn.ToolApprovalDecision{}, false
		}
		// The review row is not a verdict: it opens the reviewer picker and
		// leaves the approval on screen.
		if o.cursor < len(o.options) && o.options[o.cursor].isPlanReview {
			o.enterPlanReviewMode()
			return turn.ToolApprovalDecision{}, false
		}
		return o.commit(), true
	case rawKeyCtrlE:
		o.showExplain = !o.showExplain
	case rawKeyEscape:
		return o.dismissDecision(), true
	case rawKeyCtrlC:
		// ctrl+c is the terminal's interrupt, not an answer to the question on
		// screen, so it keeps tearing the run down whatever the request offers.
		return turn.ToolApprovalDecision{Cancelled: true}, true
	case rawKeyRune:
		switch key.r {
		case '?':
			o.showHelp = !o.showHelp
		case 'y', 'Y':
			o.cursor = 0
			return o.commit(), true
		case 'n', 'N':
			o.cursor = denyChoiceIndex(o.options)
			return o.commit(), true
		case 'r', 'R':
			if o.req.RequestedPermissions != nil && len(o.options) > 1 {
				o.cursor = 1
				return o.commit(), true
			}
		case 'm', 'M':
			if idx := planReviewChoiceIndex(o.options); idx >= 0 {
				o.cursor = idx
				o.enterPlanReviewMode()
			}
		case 'j':
			if o.cursor < len(o.options)-1 {
				o.cursor++
			}
		case 'k':
			if o.cursor > 0 {
				o.cursor--
			}
		case '1', '2', '3', '4', '5':
			idx := int(key.r - '1')
			if idx < len(o.options) {
				o.cursor = idx
			}
		}
	}
	return turn.ToolApprovalDecision{}, false
}

// dismissDecision is what Esc resolves to. Dismissing the prompt is a refusal,
// and which refusal it is belongs to the request rather than to the key: one
// that offers a cancel is torn down, while one whose only refusal is a decline
// — a memory note, a permission request — is declined, which hands the model a
// refusal result and leaves the run alive. Synthesizing a cancel the request
// never offered ended turns over something the user had merely said no to.
func (o *approvalOverlay) dismissDecision() turn.ToolApprovalDecision {
	declines := false
	for _, option := range o.req.AvailableDecisions {
		switch option.Decision {
		case safety.DecisionCancel:
			return turn.ToolApprovalDecision{Cancelled: true}
		case safety.DecisionDecline:
			declines = true
		}
	}
	if declines {
		return turn.ToolApprovalDecision{Denied: true}
	}
	return turn.ToolApprovalDecision{Cancelled: true}
}

func denyChoiceIndex(options []overlayChoice) int {
	for i, option := range options {
		if option.denyOnce {
			return i
		}
	}
	if len(options) == 0 {
		return 0
	}
	return len(options) - 1
}

func (o *approvalOverlay) insertEditText(text string) {
	insert := []rune(text)
	if len(insert) == 0 {
		return
	}
	o.feedbackBuf, o.feedbackCursor = insertRunesAtCursor(o.feedbackBuf, o.feedbackCursor, insert)
}

// approvalFeedbackPrompt precedes the feedback text on its first row;
// approvalFeedbackIndent is the equally wide blank that continuation rows carry
// instead, so a wrapped row is never mistaken for a second input prompt.
const (
	approvalFeedbackPrompt = "> "
	approvalFeedbackIndent = "  "
)

// layoutApprovalFeedback lays the feedback text out at the terminal width the
// overlay is painted at. It wraps once, through the same span wrapper the
// composer uses, and derives the painted rows, their rune ranges and the caret
// position from that single wrap: nothing downstream re-runs the line breaking,
// which is what keeps the text, the caret, Up/Down and a click on the field
// from drifting apart on hard newlines, soft wraps and wide runes.
func layoutApprovalFeedback(text string, cursor, width int) approvalFeedbackLayout {
	prefixWidth := displayLineWidth(approvalFeedbackPrompt)
	contentWidth := width - viewportRightPadding - prefixWidth
	if contentWidth < 1 {
		contentWidth = 1
	}
	spans := wrapCardContentSpans(text, contentWidth)
	rows := composerTextRowsFromSpans(text, spans)
	lines := make([]string, len(spans))
	for i, span := range spans {
		prefix := approvalFeedbackIndent
		if i == 0 {
			prefix = approvalFeedbackPrompt
		}
		rows[i].prefixWidth = displayLineWidth(prefix)
		lines[i] = prefix + span.content
	}
	cursor = composerCursorClampRunes([]rune(text), cursor)
	cursorRow, contentCol := composerCursorVisualPos(text, cursor, spans)
	return approvalFeedbackLayout{
		lines:     lines,
		rows:      rows,
		cursorRow: cursorRow,
		cursorCol: rows[cursorRow].prefixWidth + contentCol,
	}
}

// moveFeedbackCursorVertically moves the caret one visual row up or down within
// the layout that is currently on screen, keeping its display column. A row
// that ends before that column takes the caret to its end, and a move off
// either end of the field is a no-op.
func (o *approvalOverlay) moveFeedbackCursorVertically(delta int) {
	row := o.feedbackLayout.cursorRow
	target := row + delta
	if target < 0 || target >= len(o.feedbackLayout.rows) {
		return
	}
	contentCol := o.feedbackLayout.cursorCol - o.feedbackLayout.rows[row].prefixWidth
	targetRow := o.feedbackLayout.rows[target]
	targetRunes := o.feedbackBuf[targetRow.startRune:targetRow.endRune]
	o.feedbackCursor = targetRow.startRune + runeIndexAtVisualCol(targetRunes, contentCol)
}

// moveFeedbackCursorToLineEdge moves the caret to the start or the end of the
// hard line it sits on. A soft wrap is not a line boundary here: Up and Down
// already walk the visual rows, and a row that was broken mid-word shares its
// last position with the next row's first, so a visual-row End would leave the
// caret rendered a row below the key the user pressed. A hard line's edges are
// always positions the field can show, which is why Home and End work on the
// text as it was typed, the way a terminal line editor does.
func (o *approvalOverlay) moveFeedbackCursorToLineEdge(toEnd bool) {
	start := lineStart(o.feedbackBuf, o.feedbackCursor)
	if !toEnd {
		o.feedbackCursor = start
		return
	}
	o.feedbackCursor = visualLineEnd(o.feedbackBuf, start, 0)
}

// placeFeedbackCaret moves the feedback caret to the cell the user clicked.
// line and col are overlay-relative, as the renderer resolved them; the same
// row table that rendered the field maps the click back into feedbackBuf.
func (o *approvalOverlay) placeFeedbackCaret(line, col int) {
	if !o.planDenyEditing {
		return
	}
	row := line - o.feedbackFirstRow
	if row < 0 || row >= len(o.feedbackLayout.rows) {
		return
	}
	feedbackRow := o.feedbackLayout.rows[row]
	idx := caretIndexForClick(
		o.feedbackBuf[feedbackRow.startRune:feedbackRow.endRune],
		feedbackRow.prefixWidth,
		col,
	)
	o.feedbackCursor = feedbackRow.startRune + idx
	o.renderViewport()
}

// overlayScrollToEdge is a delta larger than any overlay is tall, which is how
// the jump-to-top and jump-to-bottom keys ask for the end of the content.
const overlayScrollToEdge = 100000

// scrollOverlay moves the overlay body by delta rows. A scroll that actually
// moved releases the view from the row the overlay otherwise keeps anchored —
// the option list, or the feedback caret — so the next paint does not pull the
// content straight back to it; a scroll with nowhere to go leaves that anchor
// alone, because nothing moved for the user to lose.
func (o *approvalOverlay) scrollOverlay(delta int) {
	if o.rs == nil || o.rs.renderer == nil || !o.rs.inViewportOverlay() {
		return
	}
	if o.rs.renderer.ViewportScrollOverlay(delta) {
		o.keepOptionsInView = false
	}
}

func (o *approvalOverlay) enterPlanDenyMode() {
	o.planDenyEditing = true
	o.clearFeedback()
}

// handlePlanDenyKey handles feedback for a "No, keep planning" decision.
func (o *approvalOverlay) handlePlanDenyKey(key parsedKey) (turn.ToolApprovalDecision, bool) {
	o.keepOptionsInView = true
	switch key.kind {
	case rawKeyEscape:
		// Cancel feedback input, return to option list.
		o.planDenyEditing = false
		o.clearFeedback()
		return turn.ToolApprovalDecision{}, false
	case rawKeyCtrlC:
		// Esc steps back to the option list, so Ctrl+C is what abandons the
		// approval outright, exactly as it does on every other modal row.
		return turn.ToolApprovalDecision{Cancelled: true}, true
	case rawKeyEnter:
		// Submit denial with the feedback text as DenyReason.
		feedback := strings.TrimSpace(string(o.feedbackBuf))
		o.planDenyEditing = false
		o.clearFeedback()
		return turn.ToolApprovalDecision{Denied: true, DenyReason: feedback}, true
	case rawKeyBackspace:
		if o.feedbackCursor > 0 {
			o.feedbackBuf = append(o.feedbackBuf[:o.feedbackCursor-1], o.feedbackBuf[o.feedbackCursor:]...)
			o.feedbackCursor--
		}
	case rawKeyLeft:
		if o.feedbackCursor > 0 {
			o.feedbackCursor--
		}
	case rawKeyRight:
		if o.feedbackCursor < len(o.feedbackBuf) {
			o.feedbackCursor++
		}
	case rawKeyUp:
		o.moveFeedbackCursorVertically(-1)
	case rawKeyDown:
		o.moveFeedbackCursorVertically(1)
	case rawKeyHome:
		o.moveFeedbackCursorToLineEdge(false)
	case rawKeyEnd:
		o.moveFeedbackCursorToLineEdge(true)
	case rawKeyPageUp:
		// Home and End are caret keys in this field, so the page keys are what
		// scrolls the plan or diff the feedback is about back into view.
		o.scrollOverlay(-10)
	case rawKeyPageDown:
		o.scrollOverlay(10)
	case rawKeyNewline:
		o.insertEditText("\n")
	case rawKeyPaste:
		o.insertEditText(key.text)
	case rawKeyRune:
		o.insertEditText(string(key.r))
	case rawKeySpace:
		o.insertEditText(" ")
	}
	return turn.ToolApprovalDecision{}, false
}

func (o *approvalOverlay) enterPlanReviewMode() {
	o.planReviewPicking = true
	o.planReviewCursor = 0
	// Start on a model other than the one that wrote the plan: a second opinion
	// is the point of the row, and the current model is still one keypress away.
	for i, model := range o.req.PlanReviewModels {
		if !model.Current {
			o.planReviewCursor = i
			break
		}
	}
}

func (o *approvalOverlay) exitPlanReviewMode() {
	o.planReviewPicking = false
	o.planReviewCursor = 0
}

// handlePlanReviewKey drives the reviewer picker. Choosing a model returns a
// decision that requests the review without approving or denying, so the
// surface owner re-prompts this overlay once the review is in.
func (o *approvalOverlay) handlePlanReviewKey(key parsedKey) (turn.ToolApprovalDecision, bool) {
	o.keepOptionsInView = true
	models := o.req.PlanReviewModels
	switch key.kind {
	case rawKeyEscape:
		o.exitPlanReviewMode()
	case rawKeyCtrlC:
		return turn.ToolApprovalDecision{Cancelled: true}, true
	case rawKeyUp:
		if o.planReviewCursor > 0 {
			o.planReviewCursor--
		}
	case rawKeyDown:
		if o.planReviewCursor < len(models)-1 {
			o.planReviewCursor++
		}
	case rawKeyPageUp:
		o.scrollOverlay(-10)
	case rawKeyPageDown:
		o.scrollOverlay(10)
	case rawKeyEnter:
		if o.planReviewCursor < 0 || o.planReviewCursor >= len(models) {
			o.exitPlanReviewMode()
			return turn.ToolApprovalDecision{}, false
		}
		selected := models[o.planReviewCursor]
		o.exitPlanReviewMode()
		return turn.ToolApprovalDecision{RequestPlanReview: &selected}, true
	case rawKeyRune:
		switch {
		case key.r == 'j':
			if o.planReviewCursor < len(models)-1 {
				o.planReviewCursor++
			}
		case key.r == 'k':
			if o.planReviewCursor > 0 {
				o.planReviewCursor--
			}
		case key.r >= '1' && key.r <= '9':
			if idx := int(key.r - '1'); idx < len(models) {
				o.planReviewCursor = idx
			}
		}
	}
	return turn.ToolApprovalDecision{}, false
}

func planReviewChoiceIndex(options []overlayChoice) int {
	for i, option := range options {
		if option.isPlanReview {
			return i
		}
	}
	return -1
}

// planReviewModelRow renders one reviewer row: the provider/model pair the
// decision carries, plus the catalog name when it adds something the model id
// does not already say.
func planReviewModelRow(model turn.PlanReviewModelOption) string {
	row := planReviewModelDisplay(model.Provider, model.Model)
	if label := strings.TrimSpace(model.Label); label != "" && !strings.EqualFold(label, strings.TrimSpace(model.Model)) {
		row += " — " + label
	}
	if model.Current {
		row += " (current)"
	}
	return row
}

func planReviewModelDisplay(provider, model string) string {
	if label := llm.FormatProviderModel(provider, model); label != "" {
		return label
	}
	return "the selected model"
}

func (o *approvalOverlay) commit() turn.ToolApprovalDecision {
	choice := o.options[o.cursor]
	if o.req.RequestedPermissions != nil && !choice.denyOnce && !choice.cancel {
		response := *o.req.RequestedPermissions
		response.Scope = safety.GrantScopeTurn
		response.StrictAutoReview = choice.strictReview
		if choice.dst == safety.DestinationSession {
			response.Scope = safety.GrantScopeSession
			response.StrictAutoReview = false
		}
		return turn.ToolApprovalDecision{Approved: true, RequestPermissionsResponse: &response}
	}
	if choice.allowOnce {
		return turn.ToolApprovalDecision{Approved: true, ClearContext: choice.clearContext}
	}
	if choice.denyOnce {
		return turn.ToolApprovalDecision{Denied: true}
	}
	if choice.cancel {
		return turn.ToolApprovalDecision{Cancelled: true}
	}
	if choice.networkPolicyAmendment != nil {
		if o.req.NetworkApproval == nil || strings.TrimSpace(o.req.NetworkApproval.Host) == "" {
			return turn.ToolApprovalDecision{Cancelled: true}
		}
		amendment := *choice.networkPolicyAmendment
		return turn.ToolApprovalDecision{
			Approved:               true,
			NetworkPolicyAmendment: &amendment,
		}
	}
	if len(choice.commandPrefix) > 0 {
		return turn.ToolApprovalDecision{Approved: true, Update: &safety.PermissionUpdate{
			Type:        safety.UpdateAddRules,
			Destination: safety.DestinationLocalSettings,
			Behavior:    safety.BehaviorAllow,
			Rules: []safety.PermissionRuleValue{{
				ToolName:      "Bash",
				CommandPrefix: append([]string(nil), choice.commandPrefix...),
				BypassSandbox: true,
			}},
		}}
	}
	if len(choice.commandRules) > 0 {
		return turn.ToolApprovalDecision{Approved: true, Update: &safety.PermissionUpdate{
			Type:        safety.UpdateAddRules,
			Destination: safety.DestinationLocalSettings,
			Behavior:    safety.BehaviorAllow,
			Rules:       append([]safety.PermissionRuleValue(nil), choice.commandRules...),
		}}
	}
	if strings.EqualFold(strings.TrimSpace(approvalToolName(o.req)), "apply_patch") {
		var payload struct {
			ResolvedPaths []string `json:"resolved_paths"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(o.req.ToolInputJSON)), &payload) != nil {
			return turn.ToolApprovalDecision{Cancelled: true}
		}
		rules := make([]safety.PermissionRuleValue, 0, len(payload.ResolvedPaths))
		seen := map[string]struct{}{}
		for _, path := range payload.ResolvedPaths {
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			rules = append(rules, safety.PermissionRuleValue{ToolName: "apply_patch", RuleContent: path, BypassSandbox: true})
		}
		if len(rules) == 0 {
			return turn.ToolApprovalDecision{Cancelled: true}
		}
		return turn.ToolApprovalDecision{Approved: true, Update: &safety.PermissionUpdate{
			Type: safety.UpdateAddRules, Destination: safety.DestinationSession,
			Behavior: safety.BehaviorAllow, Rules: rules,
		}}
	}
	// Build the exact server-proposed rule decision.
	req := o.req
	if choice.ruleContent != "" {
		req.PrefixRuleContent = choice.ruleContent
	}
	dst := choice.dst
	if dst == "" {
		dst = persistentDestination(req)
	}
	decision, err := buildRuleDecisionToDestination(req, dst)
	if err != nil {
		// Surface the failure: silently downgrading to allow-once would let
		// the user think they persisted a rule when they didn't. Cancel and
		// print why so the user can retry without persisting a different rule.
		_, _ = fmt.Fprintf(o.out, "\r\nApproval rule could not be built: %v\r\nCancelled — no rule persisted.\r\n", err)
		return turn.ToolApprovalDecision{Cancelled: true}
	}
	return decision
}

func (o *approvalOverlay) termSize() (width, height int) {
	if o != nil && o.rs != nil {
		return o.rs.termSize()
	}
	w, h, err := termSizeWithFallback()
	if err == nil && w > 0 && h > 0 {
		return w, h
	}
	return 80, 24
}

// approvalSeparatorLine generates a width-aware horizontal separator for the
// approval overlay, matching the style used in plan content blocks.
func approvalSeparatorLine(width int) string {
	if width < 1 {
		width = 80
	}
	return "\x1b[2m" + strings.Repeat("─", width) + "\x1b[0m"
}

// approvalOverlayVisualLines folds one overlay line to width with the same
// word wrap every other surface uses.
func approvalOverlayVisualLines(line string, width int) []string {
	line = strings.TrimRight(line, "\r")
	if width < 1 {
		width = 80
	}
	plainParts := strings.Split(line, "\n")
	out := make([]string, 0, len(plainParts))
	for _, part := range plainParts {
		out = append(out, wrapCardLine(part, width)...)
	}
	return out
}

func approvalToolName(req turn.ToolApprovalRequest) string {
	if s := strings.TrimSpace(req.PermissionToolName); s != "" {
		return s
	}
	return strings.TrimSpace(req.ToolName)
}

func approvalIsBash(req turn.ToolApprovalRequest) bool {
	return strings.EqualFold(safety.CanonicalToolName(approvalToolName(req)), "Bash")
}

func isApprovalMemoryNote(req turn.ToolApprovalRequest) bool {
	return strings.EqualFold(strings.TrimSpace(approvalToolName(req)), "memories_add_ad_hoc_note")
}

func approvalMemoryNoteInput(req turn.ToolApprovalRequest) (filename, note string, ok bool) {
	raw := strings.TrimSpace(req.ToolInputJSON)
	if raw == "" {
		return "", "", false
	}
	var input struct {
		Filename string `json:"filename"`
		Note     string `json:"note"`
	}
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return "", "", false
	}
	filename = strings.TrimSpace(input.Filename)
	note = strings.TrimSpace(input.Note)
	return filename, note, filename != "" && note != ""
}

func isApprovalMemoryQuery(req turn.ToolApprovalRequest) bool {
	return isMemoryQueryTool(approvalToolName(req))
}

// memoryQueryApprovalAction names what a memory query approval authorizes
// ("reading memory MEMORY.md"), so confirmations describe the action instead of
// asking the user to read back the tool's argument JSON. Returns "" for tools
// that are not memory queries.
func memoryQueryApprovalAction(req turn.ToolApprovalRequest) string {
	input := approvalMemoryQueryInput(req)
	path, _ := input["path"].(string)
	path = strings.TrimSpace(path)
	switch strings.ToLower(strings.TrimSpace(approvalToolName(req))) {
	case "memories_read":
		if path != "" {
			return "reading memory " + path
		}
		return "reading this memory file"
	case "memories_search":
		if queries := tool.MemorySearchQueryLabel(input); queries != "" {
			return "searching memories for " + queries
		}
		return "searching your memories"
	case "memories_list":
		if path != "" {
			return "listing memories in " + path
		}
		return "listing your memories"
	default:
		return ""
	}
}

// memoryQueryApprovalDetails describes a memory query as the fields the user
// needs to decide on it, replacing the generic "Tool: … / Input: {json}" panel.
func memoryQueryApprovalDetails(req turn.ToolApprovalRequest) []string {
	input := approvalMemoryQueryInput(req)
	path, _ := input["path"].(string)
	path = strings.TrimSpace(path)
	switch strings.ToLower(strings.TrimSpace(approvalToolName(req))) {
	case "memories_read":
		details := []string{"Action: Read a memory file"}
		if path == "" {
			return append(details, "Memory file details are unavailable.")
		}
		details = append(details, "File: "+path)
		if lo, hi, ok := tool.MemoryReadLineRange(input); ok {
			details = append(details, fmt.Sprintf("Lines: %d-%d", lo, hi))
		}
		return details
	case "memories_search":
		details := []string{"Action: Search memories"}
		if queries := tool.MemorySearchQueryLabel(input); queries != "" {
			details = append(details, "Queries: "+queries)
		} else {
			details = append(details, "Query details are unavailable.")
		}
		if path != "" {
			details = append(details, "Scope: "+path)
		} else {
			details = append(details, "Scope: All memories")
		}
		return details
	case "memories_list":
		if path == "" {
			return []string{"Action: List memories", "Folder: Memory root"}
		}
		return []string{"Action: List memories", "Folder: " + path}
	default:
		return nil
	}
}

// approvalMemoryQueryInput decodes the memory tool arguments. The permission
// fields carry the same JSON for these tools, so they serve as fallbacks when
// the request was rebuilt without ToolInputJSON.
func approvalMemoryQueryInput(req turn.ToolApprovalRequest) map[string]any {
	for _, raw := range []string{req.ToolInputJSON, req.PermissionInput, req.ExactRuleContent} {
		raw = strings.TrimSpace(raw)
		if !strings.HasPrefix(raw, "{") {
			continue
		}
		var input map[string]any
		if json.Unmarshal([]byte(raw), &input) == nil && len(input) > 0 {
			return input
		}
	}
	return nil
}

func isApprovalWebSearch(req turn.ToolApprovalRequest) bool {
	return strings.EqualFold(safety.CanonicalToolName(approvalToolName(req)), "WebSearch")
}

func isApprovalRetrieveOutput(req turn.ToolApprovalRequest) bool {
	return strings.EqualFold(strings.TrimSpace(approvalToolName(req)), "retrieve_output")
}

type approvalRetrieveOutputInput struct {
	ID       int64  `json:"id"`
	Query    string `json:"query"`
	Lines    string `json:"lines"`
	Reason   string `json:"reason"`
	Top      int    `json:"top"`
	Category string `json:"category"`
}

func approvalRetrieveOutputInputValue(req turn.ToolApprovalRequest) (approvalRetrieveOutputInput, bool) {
	raw := strings.TrimSpace(req.ToolInputJSON)
	if !strings.HasPrefix(raw, "{") {
		return approvalRetrieveOutputInput{}, false
	}
	var input approvalRetrieveOutputInput
	if json.Unmarshal([]byte(raw), &input) == nil && input.ID > 0 {
		input.Query = approvalWebSearchDisplayText(input.Query)
		input.Lines = approvalWebSearchDisplayText(input.Lines)
		input.Reason = approvalWebSearchDisplayText(input.Reason)
		input.Category = approvalWebSearchDisplayText(input.Category)
		return input, true
	}
	return approvalRetrieveOutputInput{}, false
}

func retrieveOutputApprovalDetails(req turn.ToolApprovalRequest) []string {
	input, ok := approvalRetrieveOutputInputValue(req)
	if !ok {
		return []string{
			"Action: Retrieve saved tool output",
			"Saved output details are unavailable.",
		}
	}
	action := "Retrieve the full saved output"
	if input.Query != "" {
		action = "Search within saved output"
	} else if input.Lines != "" {
		action = "Read lines from saved output"
	}
	details := []string{
		"Action: " + action,
		fmt.Sprintf("Saved output: #%d", input.ID),
	}
	switch strings.ToLower(input.Category) {
	case "shell":
		details = append(details, "Source: Shell command output")
	case "mcp":
		details = append(details, "Source: MCP tool output")
	}
	if input.Query != "" {
		details = append(details, "Query: "+input.Query)
		if input.Top > 0 {
			details = append(details, fmt.Sprintf("Maximum matches: %d", input.Top))
		}
	}
	if input.Lines != "" {
		details = append(details, "Lines: "+input.Lines)
	}
	if input.Reason != "" {
		details = append(details, "Reason: "+input.Reason)
	}
	return details
}

func approvalWebSearchQuery(req turn.ToolApprovalRequest) string {
	for _, raw := range []string{req.ToolInputJSON, req.PermissionInput, req.ExactRuleContent} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var payload struct {
			Query string `json:"query"`
			Q     string `json:"q"`
		}
		if json.Unmarshal([]byte(raw), &payload) == nil {
			if query := strings.TrimSpace(payload.Query); query != "" {
				return approvalWebSearchDisplayText(query)
			}
			if query := strings.TrimSpace(payload.Q); query != "" {
				return approvalWebSearchDisplayText(query)
			}
			continue
		}
		if strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[") {
			continue
		}
		if query, ok := strings.CutPrefix(raw, "query:"); ok {
			raw = strings.TrimSpace(query)
		}
		if raw != "" && !strings.EqualFold(raw, "web_search") && !strings.EqualFold(raw, "WebSearch") {
			return approvalWebSearchDisplayText(raw)
		}
	}
	return ""
}

func approvalWebSearchLimit(req turn.ToolApprovalRequest) int {
	raw := strings.TrimSpace(req.ToolInputJSON)
	if raw == "" {
		return 5
	}
	var payload struct {
		Limit int `json:"limit"`
	}
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return 0
	}
	if payload.Limit <= 0 {
		return 5
	}
	if payload.Limit > 10 {
		return 10
	}
	return payload.Limit
}

func approvalWebSearchDisplayText(value string) string {
	clean := make([]rune, 0, len(value))
	for _, r := range value {
		if unicode.IsControl(r) {
			clean = append(clean, ' ')
			continue
		}
		clean = append(clean, r)
	}
	return strings.Join(strings.Fields(string(clean)), " ")
}

func isApprovalFileMutation(req turn.ToolApprovalRequest) bool {
	if strings.EqualFold(strings.TrimSpace(approvalToolName(req)), "apply_patch") {
		return true
	}
	switch safety.CanonicalToolName(approvalToolName(req)) {
	case "Write", "Edit", "MultiEdit":
		return true
	default:
		return false
	}
}

func approvalBashCommand(req turn.ToolApprovalRequest) string {
	if s := strings.TrimSpace(req.PermissionInput); s != "" {
		return s
	}
	raw := strings.TrimSpace(req.ToolInputJSON)
	if raw == "" {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(raw), &in); err == nil && strings.TrimSpace(in.Command) != "" {
		return strings.TrimSpace(in.Command)
	}
	return raw
}

func approvalDisplayInput(raw string) string {
	input := strings.TrimSpace(raw)
	if input == "" || input == "{}" {
		return ""
	}
	return input
}

func isApprovalEnterPlanMode(req turn.ToolApprovalRequest) bool {
	return strings.EqualFold(approvalToolName(req), "enter_plan_mode") ||
		strings.EqualFold(safety.CanonicalToolName(approvalToolName(req)), "EnterPlanMode")
}

func isApprovalExitPlanMode(req turn.ToolApprovalRequest) bool {
	return strings.EqualFold(approvalToolName(req), "exit_plan_mode") ||
		strings.EqualFold(safety.CanonicalToolName(approvalToolName(req)), "ExitPlanMode")
}

func approvalEnterPlanReason(req turn.ToolApprovalRequest) string {
	raw := strings.TrimSpace(req.ToolInputJSON)
	if raw == "" || raw == "{}" {
		return ""
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return ""
	}
	return strings.TrimSpace(in.Reason)
}

// approvalJustification returns the sentence an approval has to show when the
// runtime supplied one. The question the user is being asked is not always
// "may this write happen?": a protected path, a plan-mode command whose effect
// could not be proven read-only, and an escalation the sandbox already refused
// all carry their own reason, and that reason is the only thing on the overlay
// the user can act on. Crucially the test is whether the payload carries a
// reason, not which tag it carries: enumerating tags means every new reason
// silently renders as a bare action id.
func approvalJustification(req turn.ToolApprovalRequest) string {
	raw := strings.TrimSpace(req.ToolInputJSON)
	if raw == "" || raw == "{}" {
		return ""
	}
	var in struct {
		Justification string `json:"justification"`
	}
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return ""
	}
	return strings.TrimSpace(in.Justification)
}

func approvalHasFileDiff(req turn.ToolApprovalRequest) bool {
	tool := strings.ToLower(strings.TrimSpace(approvalToolName(req)))
	return tool == "edit_file" || tool == "edit" || tool == "write_file" || tool == "write" || tool == "apply_patch"
}

func approvalFileDiff(req turn.ToolApprovalRequest) (filePath, diffText string) {
	raw := strings.TrimSpace(req.ToolInputJSON)
	if raw == "" || raw == "{}" {
		return "", ""
	}
	var patchInput struct {
		Patch         string   `json:"patch"`
		ResolvedPaths []string `json:"resolved_paths"`
	}
	if strings.EqualFold(strings.TrimSpace(approvalToolName(req)), "apply_patch") && json.Unmarshal([]byte(raw), &patchInput) == nil {
		return strings.Join(patchInput.ResolvedPaths, ", "), strings.TrimSpace(patchInput.Patch)
	}
	var in struct {
		FilePath  string `json:"file_path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
		Content   string `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return "", ""
	}
	filePath = strings.TrimSpace(in.FilePath)
	if in.OldString != "" || in.NewString != "" {
		if in.OldString != in.NewString {
			summary, err := event.Build(filepath.Base(filePath), []byte(in.OldString), []byte(in.NewString))
			if err == nil && summary.UnifiedDiff != "" {
				diffText = summary.UnifiedDiff
			} else {
				diffText = in.OldString + "\n" + in.NewString
			}
		}
	} else if in.Content != "" {
		summary, err := event.Build(filepath.Base(filePath), []byte(""), []byte(in.Content))
		if err == nil && summary.UnifiedDiff != "" {
			diffText = summary.UnifiedDiff
		} else {
			diffText = in.Content
		}
	}
	return filePath, diffText
}

// diffBodyLines returns the rendered diff rows for the approval body, rendering
// them at most once per (width, theme). buildApprovalContent runs on every
// keystroke, and the styled renderer runs a chroma tokeniser over every line of
// the diff, so re-rendering per key is what makes a large diff feel slow — not
// showing it. The request cannot change while the overlay is open, so the rows
// are memoised and every key after the first is a slice read.
func (o *approvalOverlay) diffBodyLines(diffText, filePath string, width int, theme DiffTheme) []string {
	if o.diffBody.valid && o.diffBody.width == width && o.diffBody.theme == theme {
		return o.diffBody.lines
	}
	rows := strings.Split(renderApprovalDiff(diffText, filePath, width, theme), "\n")
	o.diffBody = approvalDiffCache{valid: true, width: width, theme: theme, lines: rows}
	return rows
}

// renderApprovalDiff parses a unified diff string and renders it with the same
// styled pipeline used for tool-result diff cards: line-number gutter,
// full-width add/del background bands, and syntax highlighting.
// width is the overlay width; the band fills width - 2.
// filePath is the file being edited; used to construct a synthetic "diff --git"
// header when the diff text (from go-difflib/event.Build) lacks one.
//
// Every changed line is rendered, however large the diff: this is the modal
// that asks the user to approve the write, so hiding part of what they are
// approving defeats it. Height is the overlay's problem, not the renderer's —
// the block scrolls (PgUp/PgDn/Home/End, wheel) — and cost is handled by
// diffBodyLines, which renders once per width and theme.
func renderApprovalDiff(diffText, filePath string, width int, theme DiffTheme) string {
	doc := event.Parse(diffText)
	// event.Build uses go-difflib which produces unified diffs without the
	// "diff --git a/… b/…" header that event.Parse expects to detect files.
	// Prepend a synthetic header so the structured parser can consume the hunks.
	if len(doc.Files) == 0 && strings.TrimSpace(diffText) != "" {
		base := filepath.Base(filePath)
		if base == "" {
			base = "file"
		}
		doc = event.Parse("diff --git a/" + base + " b/" + base + "\n" + diffText)
	}
	bandW := width - 2
	if bandW < 20 {
		bandW = 20
	}
	return renderDiffCore(doc.Files, theme, bandW)
}

func approvalShellCommand(req turn.ToolApprovalRequest) string {
	if s := strings.TrimSpace(req.PermissionInput); s != "" {
		return s
	}
	raw := strings.TrimSpace(req.ToolInputJSON)
	if raw == "" {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(raw), &in); err == nil && strings.TrimSpace(in.Command) != "" {
		return strings.TrimSpace(in.Command)
	}
	return raw
}

func approvalPlanContent(o *approvalOverlay) string {
	planFile := approvalPlanFilePath(o.req)
	if planFile == "" {
		return ""
	}
	b, err := os.ReadFile(planFile)
	if err != nil || len(b) == 0 {
		return "No plan found."
	}
	return strings.TrimSpace(string(b))
}

func approvalPlanFilePath(req turn.ToolApprovalRequest) string {
	// First try the PlanFilePath field (set by surface for enter/exit_plan_mode).
	if pp := strings.TrimSpace(req.PlanFilePath); pp != "" {
		return pp
	}
	// Fall back to parsing plan_file from the tool input JSON.
	raw := strings.TrimSpace(req.ToolInputJSON)
	if raw == "" {
		return ""
	}
	var in struct {
		PlanFile string `json:"plan_file"`
	}
	if err := json.Unmarshal([]byte(raw), &in); err != nil || in.PlanFile == "" {
		return ""
	}
	return in.PlanFile
}

// questionOverlay renders a multi-question Ask form created by the
// user_interaction tool:
//   - Navigation bar with per-question chips (☐ unanswered / ✔ answered) and
//     a Submit tab, bounded by ← → arrows.
//   - Question title (the question prompt).
//   - Numbered options in compact-vertical layout with descriptions and a
//     pointer (❯) on the focused row.
//   - "Other" row that is a text field holding the caret for as long as it
//     is focused.
//   - Submit button (multi-select only) to advance after toggling options.
//   - Divider, then "Chat about this" footer action.
//   - Bottom help line: "Enter to select · Tab/Arrow keys to navigate · Esc to cancel"
//     (or "↑/↓ to navigate" when there is a single question).
//   - Submit review view when the Submit tab is focused.
//
// Key bindings:
//   - ↑/↓ or j/k or ctrl+p/ctrl+n: move focus through options → Other →
//     (Submit button if multi-select) → "Chat about this".
//   - Enter: select option (single-select auto-advances / auto-submits when
//     only one question); toggle option (multi-select); confirm the Other text;
//     advance from the Submit button; cancel from "Chat about this".
//   - Space: toggle option (multi-select).
//   - Tab / → : next question tab (or Submit tab).
//   - Shift+Tab / ← : previous question tab.
//   - 1..9: jump to option by number.
//   - Esc / ctrl+c: cancel.
//   - ?: toggle keymap help (forebrain extension).
//
// While the Other row is focused its edit session owns every key: printable
// runes and Space type, ←/→ move the caret, ↑/↓ and Tab leave the field and
// save what was typed, Enter confirms, and Esc discards the uncommitted edit —
// once there is nothing left to discard it cancels the form like anywhere else.
type questionOverlay struct {
	// ctx is the request's context (see approvalOverlay).
	ctx       context.Context
	out       io.Writer
	rs        *rawSelector
	req       turn.ToolApprovalRequest
	form      state.AskForm
	current   int            // 0..len(questions)-1 for questions, len(questions) for submit view
	cursor    int            // index of focused row within the current question (see row indices below)
	answers   []map[int]bool // per-question selected option indices (multi-select aware)
	otherText []string       // per-question custom free-text answer
	// editingOther reports whether an edit session is open, i.e. whether
	// otherBuf/otherCursor hold the Other answer of question `current` rather
	// than stale scratch from a question visited earlier. syncOtherEditMode is
	// the only place that opens and closes it, always following focus.
	editingOther bool
	otherBuf     []rune // text buffer while editing Other
	otherCursor  int    // cursor position within otherBuf
	// otherRow is the index, among the lines the last render produced, of the
	// Other row while it is being edited (-1 otherwise), and otherPrefixWidth
	// is the display width of the pointer, number and label that precede the
	// text on it. Together they turn a click on the row into a caret position
	// in otherBuf.
	otherRow         int
	otherPrefixWidth int
	showHelp         bool // toggle keymap pane with "?"

	// Submit review view state. submitCursor is 0 = "Submit answers", 1 = "Cancel".
	submitCursor int
}

// Row indices within a question (cursor values):
//
//	0 .. len(q.Options)-1            : option rows
//	len(q.Options)                   : "Other" row
//	len(q.Options)+1 (multi-select)  : Submit button row ("Submit" / "Next")
//	after divider:
//	chatIdx                          : "Chat about this"
//
// For single-select: chatIdx = len(q.Options)+1
// For multi-select:  chatIdx = len(q.Options)+2
func newQuestionOverlay(ctx context.Context, out io.Writer, rs *rawSelector, req turn.ToolApprovalRequest) (*questionOverlay, error) {
	var form state.AskForm
	payload := strings.TrimSpace(req.AskFormJSON)
	if payload == "" {
		return nil, fmt.Errorf("missing ask form payload")
	}
	if err := json.Unmarshal([]byte(payload), &form); err != nil {
		return nil, fmt.Errorf("decode ask form: %w", err)
	}
	if len(form.Questions) == 0 {
		return nil, fmt.Errorf("ask form has no questions")
	}
	answers := make([]map[int]bool, len(form.Questions))
	for i := range answers {
		answers[i] = make(map[int]bool)
	}
	otherText := make([]string, len(form.Questions))
	return &questionOverlay{
		ctx:       ctx,
		out:       out,
		rs:        rs,
		req:       req,
		form:      form,
		answers:   answers,
		otherText: otherText,
		otherRow:  -1,
	}, nil
}

func (o *questionOverlay) Run() (turn.ToolApprovalDecision, error) {
	if o.rs == nil {
		return turn.ToolApprovalDecision{}, fmt.Errorf("tui question overlays require an interactive raw terminal")
	}
	release, acquired := o.waitForComposerInput()
	if !acquired {
		// The request's context was cancelled while this question waited for
		// stdin; there is nobody left to answer it.
		return turn.ToolApprovalDecision{Cancelled: true}, nil
	}
	defer release()
	// Ignore selection keys that were queued before this overlay owned stdin.
	// Input typed after the first render below remains normal question input.
	o.rs.discardPreOpenInput()

	// In non-viewport mode serialise writes against the renderer, matching
	// approvalOverlay so direct fmt.Fprintln writes bracketed by the mutex
	// cannot interleave with renderer output.
	mutexHeld := false
	if o.rs != nil && !o.rs.inViewportOverlay() && o.rs.mu != nil {
		o.rs.mu.Lock()
		mutexHeld = true
	}
	defer func() {
		if mutexHeld {
			o.rs.mu.Unlock()
		}
	}()

	o.resetFocusForOpen()
	o.rs.resetOverlayViewportState()
	o.syncOtherEditMode()
	o.render()
	defer o.rs.withCaretPlacer(o.placeOtherCaret)()
	for {
		key, err := o.rs.readKey()
		if err != nil {
			return turn.ToolApprovalDecision{}, err
		}
		if key.kind == rawKeyNone {
			continue
		}
		if key.kind == rawKeyMouse {
			o.rs.dispatchMouse(key.mouse)
			continue
		}
		decision, done := o.handleKey(key)
		if done {
			return decision, nil
		}
		o.render()
	}
}

func (o *questionOverlay) resetFocusForOpen() {
	o.current = 0
	o.cursor = 0
	o.editingOther = false
	o.otherBuf = nil
	o.otherCursor = 0
	o.showHelp = false
	o.submitCursor = 0
}

// ---- helpers for question/tab structure ----

func (o *questionOverlay) curQuestion() *state.AskQuestion {
	if o.current < 0 || o.current >= len(o.form.Questions) {
		return nil
	}
	return &o.form.Questions[o.current]
}

func (o *questionOverlay) inSubmitView() bool {
	return o.current == len(o.form.Questions)
}

func (o *questionOverlay) showSubmitTab() bool {
	return !(len(o.form.Questions) == 1 && !o.form.Questions[0].AllowMultiple)
}

func (o *questionOverlay) showArrows() bool {
	return o.showSubmitTab()
}

// maxTabIndex is the highest value `current` can reach via Tab/→ navigation.
// It is len(questions) when the Submit tab is shown, else len(questions)-1.
func (o *questionOverlay) maxTabIndex() int {
	if o.showSubmitTab() {
		return len(o.form.Questions)
	}
	return len(o.form.Questions) - 1
}

func (o *questionOverlay) otherIdx() int {
	q := o.curQuestion()
	if q == nil {
		return 0
	}
	return len(q.Options)
}

// submitIdx returns the cursor index of the Submit button row, or -1 when
// the current question is single-select (no Submit button).
func (o *questionOverlay) submitIdx() int {
	q := o.curQuestion()
	if q == nil || !q.AllowMultiple {
		return -1
	}
	return len(q.Options) + 1
}

func (o *questionOverlay) chatIdx() int {
	q := o.curQuestion()
	if q == nil {
		return 0
	}
	if q.AllowMultiple {
		return len(q.Options) + 2
	}
	return len(q.Options) + 1
}

func (o *questionOverlay) maxCursor() int {
	return o.chatIdx()
}

// isQuestionAnswered reports whether the question at index i has any selection
// or non-empty Other text.
func (o *questionOverlay) isQuestionAnswered(i int) bool {
	if i < 0 || i >= len(o.form.Questions) {
		return false
	}
	if len(o.answers[i]) > 0 {
		return true
	}
	return strings.TrimSpace(o.otherText[i]) != ""
}

// allQuestionsAnswered reports whether every question has an answer.
func (o *questionOverlay) allQuestionsAnswered() bool {
	for i := range o.form.Questions {
		if !o.isQuestionAnswered(i) {
			return false
		}
	}
	return true
}

// syncOtherEditMode reconciles the Other field's edit session with the focused
// row. That row is a text field, so it owns an edit session — the buffer and
// its caret — for exactly as long as the cursor rests on it: focus arriving
// opens one seeded from the saved answer, and focus leaving (or the form moving
// to the submit view) writes the buffer back. Every cursor and tab movement
// goes through here, which is what makes a question reached again through the
// tab bar put the caret back into its Other text instead of pointing at a field
// nothing types into.
func (o *questionOverlay) syncOtherEditMode() {
	if o.inSubmitView() || o.cursor != o.otherIdx() {
		o.flushOtherEdit()
		return
	}
	if !o.editingOther {
		o.beginOtherEdit()
	}
}

func (o *questionOverlay) beginOtherEdit() bool {
	if o.current < 0 || o.current >= len(o.otherText) {
		return false
	}
	o.otherBuf = []rune(o.otherText[o.current])
	o.otherCursor = len(o.otherBuf)
	o.editingOther = true
	return true
}

func (o *questionOverlay) endOtherEdit() {
	o.editingOther = false
	o.otherBuf = nil
	o.otherCursor = 0
}

func (o *questionOverlay) flushOtherEdit() {
	if !o.editingOther {
		return
	}
	if o.current >= 0 && o.current < len(o.otherText) {
		o.otherText[o.current] = strings.TrimSpace(string(o.otherBuf))
	}
	o.endOtherEdit()
}

// ---- key handling ----

func (o *questionOverlay) handleKey(key parsedKey) (turn.ToolApprovalDecision, bool) {
	if o.inSubmitView() {
		return o.handleSubmitViewKey(key)
	}
	if o.editingOther {
		return o.handleOtherEditKey(key)
	}
	return o.handleQuestionKey(key)
}

func (o *questionOverlay) handleQuestionKey(key parsedKey) (turn.ToolApprovalDecision, bool) {
	q := o.curQuestion()
	if q == nil {
		return turn.ToolApprovalDecision{Cancelled: true}, true
	}
	switch key.kind {
	case rawKeyUp:
		o.moveCursorUp()
	case rawKeyDown:
		o.moveCursorDown()
	case rawKeyRune:
		switch key.r {
		case 'j':
			o.moveCursorDown()
		case 'k':
			o.moveCursorUp()
		case '?':
			o.showHelp = !o.showHelp
		default:
			o.handleQuestionRune(key.r)
		}
	case rawKeySpace:
		if q.AllowMultiple && o.cursor < len(q.Options) {
			o.toggleAt(o.cursor)
		}
	case rawKeyEnter:
		return o.commitCurrent()
	case rawKeyTab:
		o.goNextTab()
	case rawKeyShiftTab:
		o.goPrevTab()
	case rawKeyLeft:
		o.goPrevTab()
	case rawKeyRight:
		o.goNextTab()
	case rawKeyEscape, rawKeyCtrlC:
		return turn.ToolApprovalDecision{Cancelled: true}, true
	}
	return turn.ToolApprovalDecision{}, false
}

// handleQuestionRune handles printable runes on an option row: 1..9 jumps to
// that option. Runes cannot reach here with the Other row focused, because that
// row always holds an open edit session and handleOtherEditKey takes its keys.
func (o *questionOverlay) handleQuestionRune(r rune) {
	q := o.curQuestion()
	if q == nil {
		return
	}
	if r >= '1' && r <= '9' {
		idx := int(r - '1')
		if idx < len(q.Options) {
			o.cursor = idx
			o.syncOtherEditMode()
		}
	}
}

func (o *questionOverlay) handleOtherEditKey(key parsedKey) (turn.ToolApprovalDecision, bool) {
	switch key.kind {
	case rawKeyEscape:
		// The field is focused for as long as the cursor rests on its row, so
		// Esc discards the uncommitted edit instead of closing the session, and
		// cancels the form once there is nothing left to discard — which keeps
		// Esc a cancel key on a field the user has not typed into.
		if saved := o.otherText[o.current]; string(o.otherBuf) != saved {
			o.otherBuf = []rune(saved)
			o.otherCursor = len(o.otherBuf)
			return turn.ToolApprovalDecision{}, false
		}
		return turn.ToolApprovalDecision{Cancelled: true}, true
	case rawKeyCtrlC:
		return turn.ToolApprovalDecision{Cancelled: true}, true
	case rawKeyEnter:
		text := strings.TrimSpace(string(o.otherBuf))
		if text == "" {
			return turn.ToolApprovalDecision{}, false
		}
		o.otherText[o.current] = text
		q := o.curQuestion()
		if q != nil && !q.AllowMultiple {
			// Focus leaves this question, so the session closes with it.
			o.endOtherEdit()
			sel := o.answers[o.current]
			for k := range sel {
				delete(sel, k)
			}
			return o.advanceOrSubmit()
		}
		// Multi-select stays on the row until the Submit button, so the session
		// stays open on the text it just committed.
		o.otherBuf = []rune(text)
		o.otherCursor = len(o.otherBuf)
		return turn.ToolApprovalDecision{}, false
	case rawKeyBackspace:
		if o.otherCursor > 0 {
			o.otherBuf = append(o.otherBuf[:o.otherCursor-1], o.otherBuf[o.otherCursor:]...)
			o.otherCursor--
		}
	case rawKeyLeft:
		if o.otherCursor > 0 {
			o.otherCursor--
		}
	case rawKeyRight:
		if o.otherCursor < len(o.otherBuf) {
			o.otherCursor++
		}
	case rawKeyHome:
		o.otherCursor = 0
	case rawKeyEnd:
		o.otherCursor = len(o.otherBuf)
	case rawKeyUp:
		o.flushOtherEdit()
		o.moveCursorUp()
	case rawKeyDown:
		o.flushOtherEdit()
		o.moveCursorDown()
	case rawKeyTab:
		o.flushOtherEdit()
		o.goNextTab()
	case rawKeyShiftTab:
		o.flushOtherEdit()
		o.goPrevTab()
	case rawKeyPaste:
		// The Other answer is a single-line field: a pasted newline would split
		// the row it is rendered on, so it folds to a space the way the picker
		// filters do.
		insert := []rune(strings.ReplaceAll(key.text, "\n", " "))
		o.otherBuf, o.otherCursor = insertRunesAtCursor(o.otherBuf, o.otherCursor, insert)
	case rawKeyRune:
		o.otherBuf, o.otherCursor = insertRunesAtCursor(o.otherBuf, o.otherCursor, []rune{key.r})
	case rawKeySpace:
		o.otherBuf, o.otherCursor = insertRunesAtCursor(o.otherBuf, o.otherCursor, []rune{' '})
	}
	return turn.ToolApprovalDecision{}, false
}

func (o *questionOverlay) handleSubmitViewKey(key parsedKey) (turn.ToolApprovalDecision, bool) {
	switch key.kind {
	case rawKeyUp:
		if o.submitCursor > 0 {
			o.submitCursor--
		}
	case rawKeyDown:
		if o.submitCursor < 1 {
			o.submitCursor++
		}
	case rawKeyRune:
		switch key.r {
		case 'j':
			if o.submitCursor < 1 {
				o.submitCursor++
			}
		case 'k':
			if o.submitCursor > 0 {
				o.submitCursor--
			}
		case '?':
			o.showHelp = !o.showHelp
		}
	case rawKeyEnter:
		if o.submitCursor == 0 {
			return o.commitForm(), true
		}
		return turn.ToolApprovalDecision{Cancelled: true}, true
	case rawKeyTab, rawKeyRight:
		// Already at Submit tab; no further tab.
	case rawKeyShiftTab, rawKeyLeft:
		o.current = len(o.form.Questions) - 1
		o.restoreCursor()
		o.syncOtherEditMode()
	case rawKeyEscape, rawKeyCtrlC:
		return turn.ToolApprovalDecision{Cancelled: true}, true
	}
	return turn.ToolApprovalDecision{}, false
}

// ---- navigation ----

func (o *questionOverlay) moveCursorUp() {
	if o.cursor > 0 {
		o.cursor--
	}
	o.syncOtherEditMode()
}

func (o *questionOverlay) moveCursorDown() {
	if o.cursor < o.maxCursor() {
		o.cursor++
	}
	o.syncOtherEditMode()
}

func (o *questionOverlay) goNextTab() {
	if o.inSubmitView() {
		return
	}
	o.flushOtherEdit()
	maxTab := o.maxTabIndex()
	if o.current < maxTab {
		o.current++
		if o.current < len(o.form.Questions) {
			o.restoreCursor()
			o.syncOtherEditMode()
		} else {
			o.submitCursor = 0
		}
	}
}

func (o *questionOverlay) goPrevTab() {
	if o.current > 0 {
		o.flushOtherEdit()
		o.current--
		o.restoreCursor()
		o.syncOtherEditMode()
	}
}

// restoreCursor positions the cursor on the previously selected option when
// navigating back to a question, or at the first option otherwise.
func (o *questionOverlay) restoreCursor() {
	q := o.curQuestion()
	if q == nil {
		return
	}
	sel := o.answers[o.current]
	if !q.AllowMultiple {
		for i := range q.Options {
			if sel[i] {
				o.cursor = i
				return
			}
		}
		if strings.TrimSpace(o.otherText[o.current]) != "" {
			o.cursor = o.otherIdx()
			return
		}
		o.cursor = 0
		return
	}
	for i := range q.Options {
		if sel[i] {
			o.cursor = i
			return
		}
	}
	if strings.TrimSpace(o.otherText[o.current]) != "" {
		o.cursor = o.otherIdx()
		return
	}
	o.cursor = 0
}

func (o *questionOverlay) toggleAt(idx int) {
	q := o.curQuestion()
	if q == nil || idx < 0 || idx >= len(q.Options) {
		return
	}
	sel := o.answers[o.current]
	if sel[idx] {
		delete(sel, idx)
	} else {
		sel[idx] = true
	}
}

// otherCaretCell is the reverse-video cell the Other field draws at its caret.
// It is one column of decoration inside the text, which is why a click past it
// is shifted back by its width before it is resolved to a rune offset.
const otherCaretCell = "\x1b[7m \x1b[0m"

// placeOtherCaret moves the Other field's caret to the cell the user clicked.
// line and col are overlay-relative, as the renderer resolved them through the
// soft wrap it applied to the row.
func (o *questionOverlay) placeOtherCaret(line, col int) {
	if !o.editingOther || o.otherRow < 0 || line != o.otherRow {
		return
	}
	o.otherCursor = caretIndexAroundBlock(o.otherBuf, o.otherCursor, o.otherPrefixWidth, 1, col)
	o.render()
}

func (o *questionOverlay) commitCurrent() (turn.ToolApprovalDecision, bool) {
	q := o.curQuestion()
	if q == nil {
		return turn.ToolApprovalDecision{Cancelled: true}, true
	}

	if o.cursor == o.chatIdx() {
		return turn.ToolApprovalDecision{Cancelled: true}, true
	}

	if o.cursor == o.submitIdx() {
		return o.advanceOrSubmit()
	}

	if o.cursor < len(q.Options) {
		if q.AllowMultiple {
			o.toggleAt(o.cursor)
			return turn.ToolApprovalDecision{}, false
		}
		sel := o.answers[o.current]
		for k := range sel {
			delete(sel, k)
		}
		sel[o.cursor] = true
		o.otherText[o.current] = ""
		return o.advanceOrSubmit()
	}

	return turn.ToolApprovalDecision{}, false
}

// advanceOrSubmit moves to the next question, the submit view, or commits the
// form. For a single-question single-select form, Enter auto-submits
// immediately (no submit view).
func (o *questionOverlay) advanceOrSubmit() (turn.ToolApprovalDecision, bool) {
	if len(o.form.Questions) == 1 && !o.form.Questions[0].AllowMultiple {
		return o.commitForm(), true
	}
	if o.current < len(o.form.Questions)-1 {
		o.current++
		o.restoreCursor()
		o.syncOtherEditMode()
		return turn.ToolApprovalDecision{}, false
	}
	if o.showSubmitTab() {
		o.current = len(o.form.Questions)
		o.submitCursor = 0
		return turn.ToolApprovalDecision{}, false
	}
	return o.commitForm(), true
}

func (o *questionOverlay) commitForm() turn.ToolApprovalDecision {
	ans := state.AskAnswer{Answers: make([]state.AskAnswerItem, 0, len(o.form.Questions))}
	for i, q := range o.form.Questions {
		sel := o.answers[i]
		otherText := strings.TrimSpace(o.otherText[i])
		if len(sel) == 0 && otherText == "" {
			continue
		}
		ids := make([]string, 0, len(sel))
		for j := range q.Options {
			if sel[j] {
				ids = append(ids, q.Options[j].ID)
			}
		}
		item := state.AskAnswerItem{
			QuestionID: q.ID,
			OptionIDs:  ids,
		}
		if otherText != "" {
			item.OtherText = otherText
		}
		ans.Answers = append(ans.Answers, item)
	}
	b, err := json.Marshal(ans)
	if err != nil {
		_, _ = fmt.Fprintf(o.out, "\r\nFailed to encode answer: %v\r\n", err)
		return turn.ToolApprovalDecision{Cancelled: true}
	}
	return turn.ToolApprovalDecision{Approved: true, AskAnswerJSON: string(b)}
}

// ---- rendering ----

func (o *questionOverlay) render() {
	if o.inSubmitView() {
		o.renderSubmitView()
	} else {
		o.renderQuestionView()
	}
}

// renderQuestionView renders a single question with the nav bar, options,
// Other row, optional Submit button, footer, and help line.
func (o *questionOverlay) renderQuestionView() {
	c := o.newCollector()

	q := o.curQuestion()

	// Nav bar
	o.renderNavBar(c)

	// Question title
	c.writeLine("")
	c.writeLinef("\x1b[1m%s\x1b[0m", strings.TrimSpace(q.Prompt))
	c.writeLine("")

	// Options
	sel := o.answers[o.current]
	maxIdx := len(q.Options)                     // for number width
	idxWidth := len(fmt.Sprintf("%d", maxIdx+1)) // width of the largest option number (Other = maxIdx+1)
	for i, opt := range q.Options {
		o.renderOptionRow(c, i, opt, sel, q.AllowMultiple, idxWidth)
	}

	// Other row
	o.renderOtherRow(c, q, idxWidth)

	// Submit button (multi-select only)
	if si := o.submitIdx(); si >= 0 {
		btnFocused := o.cursor == si
		pointer := "  "
		if btnFocused {
			pointer = "\x1b[36m❯\x1b[0m "
		}
		label := "Next"
		if o.current == len(o.form.Questions)-1 {
			label = "Submit"
		}
		c.writeLinef("%s\x1b[1m%s\x1b[0m", pointer, label)
	}

	// Preview (below options, before divider) — forebrain renders previews inline
	// rather than side-by-side; the focused option's preview is shown here.
	if o.cursor < len(q.Options) {
		if preview := strings.TrimSpace(q.Options[o.cursor].Preview); preview != "" {
			c.writeLine("")
			c.writeLine("\x1b[2m--- Preview ---\x1b[0m")
			for _, line := range strings.Split(preview, "\n") {
				c.writeLinef("%s", line)
			}
		}
	}

	// Divider
	c.writeLine("")
	c.writeLine(c.questionSeparatorLine())

	// Chat about this footer
	chatFocused := o.cursor == o.chatIdx()
	// +2 because "Other" (len(q.Options)+1) sits between the last option
	// and this row.  The Submit button in multi-select mode is unnumbered
	// so the offset is the same for both single- and multi-select.
	chatNum := len(q.Options) + 2
	chatPointer := "  "
	chatColor := ""
	if chatFocused {
		chatPointer = "\x1b[36m❯\x1b[0m "
		chatColor = "\x1b[36m"
	}
	c.writeLinef("%s%s%d. Chat about this\x1b[0m", chatPointer, chatColor, chatNum)

	// Help line
	c.writeLine("")
	helpNav := "Tab/Arrow keys to navigate"
	if len(o.form.Questions) == 1 {
		helpNav = "↑/↓ to navigate"
	}
	// The overlay is soft-wrapped at paint, so the hint wraps whole.
	if o.editingOther {
		c.writeLine("\x1b[2mEnter to confirm · ←/→ to move cursor · ↑/↓ to leave the field · Esc to discard\x1b[0m")
	} else if q.AllowMultiple {
		c.writeLinef("\x1b[2mEnter to select · %s · Space to toggle · Esc to cancel\x1b[0m", helpNav)
	} else {
		c.writeLinef("\x1b[2mEnter to select · %s · Esc to cancel\x1b[0m", helpNav)
	}

	if o.showHelp {
		c.writeLine("")
		c.writeLine("\x1b[2m--- Keymap ---\x1b[0m")
		c.writeLine("  ↑/↓ or j/k       move selection")
		c.writeLine("  1..9             jump to option")
		c.writeLine("  Tab / ←/→        next question / Submit")
		c.writeLine("  Shift+Tab / ←    previous question")
		c.writeLine("  Space            toggle (multi-select)")
		c.writeLine("  Enter            select / advance / submit")
		c.writeLine("  Esc / ctrl+c     cancel")
		c.writeLine("  ?                toggle this Keymap pane")
	}

	o.finishCollector(c)
}

// renderOptionRow renders a single option line (and its description) in the
// compact-vertical layout: `❯ 1. Label` for the focused row, `  1. Label`
// otherwise. Selected single-select options get a trailing ✓; multi-select
// options get a `[x]` / `[ ]` prefix.
func (o *questionOverlay) renderOptionRow(c *qlineCollector, i int, opt state.AskOption, sel map[int]bool, multi bool, idxWidth int) {
	focused := o.cursor == i
	pointer := "  "
	if focused {
		pointer = "\x1b[36m❯\x1b[0m "
	}
	numStr := fmt.Sprintf("%d.", i+1)
	if len(numStr) < idxWidth+1 {
		numStr = numStr + strings.Repeat(" ", idxWidth+1-len(numStr))
	}
	label := opt.Label
	if opt.Recommended {
		label += " \x1b[36m(recommended)\x1b[0m"
	}
	var row string
	if multi {
		check := " "
		if sel[i] {
			check = "x"
		}
		row = fmt.Sprintf("%s\x1b[2m%s\x1b[0m [%s] %s", pointer, numStr, check, label)
		if focused {
			row = fmt.Sprintf("%s\x1b[2m%s\x1b[0m [%s] \x1b[36m%s\x1b[0m", pointer, numStr, check, label)
		}
	} else {
		tick := ""
		if sel[i] {
			tick = " \x1b[32m✓\x1b[0m"
		}
		row = fmt.Sprintf("%s\x1b[2m%s\x1b[0m %s%s", pointer, numStr, label, tick)
		if focused {
			row = fmt.Sprintf("%s\x1b[2m%s\x1b[0m \x1b[36m%s\x1b[0m%s", pointer, numStr, label, tick)
		}
	}
	c.writeLinef("%s", row)
	if desc := strings.TrimSpace(opt.Description); desc != "" {
		pad := strings.Repeat(" ", idxWidth+4)
		c.writeLinef("%s\x1b[2m%s\x1b[0m", pad, truncate(desc, 200))
	}
}

// renderOtherRow renders the "Other" option row. When editing, shows the text
// buffer with a cursor; when not editing but text exists, shows the saved text;
// when empty, shows the "Type something." placeholder.
func (o *questionOverlay) renderOtherRow(c *qlineCollector, q *state.AskQuestion, idxWidth int) {
	otherIdx := o.otherIdx()
	focused := o.cursor == otherIdx
	pointer := "  "
	if focused {
		pointer = "\x1b[36m❯\x1b[0m "
	}
	num := len(q.Options) + 1
	numStr := fmt.Sprintf("%d.", num)
	if len(numStr) < idxWidth+1 {
		numStr = numStr + strings.Repeat(" ", idxWidth+1-len(numStr))
	}
	otherText := strings.TrimSpace(o.otherText[o.current])
	if o.editingOther {
		before := string(o.otherBuf[:o.otherCursor])
		after := string(o.otherBuf[o.otherCursor:])
		prefix := fmt.Sprintf("%s\x1b[2m%s\x1b[0m Other: ", pointer, numStr)
		if q.AllowMultiple {
			check := " "
			if otherText != "" {
				check = "x"
			}
			prefix = fmt.Sprintf("%s\x1b[2m%s\x1b[0m [%s] Other: ", pointer, numStr, check)
		}
		o.otherRow = len(c.lines)
		o.otherPrefixWidth = lipgloss.Width(prefix)
		c.writeLine(prefix + before + otherCaretCell + after)
		c.writeLinef("%s\x1b[2mEnter to confirm · Esc to discard · ←/→ to move cursor · click to place the cursor\x1b[0m", strings.Repeat(" ", idxWidth+4))
		return
	}
	o.otherRow = -1
	if otherText != "" {
		if q.AllowMultiple {
			check := " "
			if otherText != "" {
				check = "x"
			}
			c.writeLinef("%s\x1b[2m%s\x1b[0m [%s] Other: %s", pointer, numStr, check, otherText)
		} else {
			c.writeLinef("%s\x1b[2m%s\x1b[0m Other: %s", pointer, numStr, otherText)
		}
		return
	}
	// Empty, not editing
	if q.AllowMultiple {
		c.writeLinef("%s\x1b[2m%s\x1b[0m [ ] Other \x1b[2m(type to enter custom text)\x1b[0m", pointer, numStr)
	} else {
		c.writeLinef("%s\x1b[2m%s\x1b[0m Other \x1b[2m(type to enter custom text)\x1b[0m", pointer, numStr)
	}
}

// renderNavBar renders the top navigation bar:
//
//	← [☐ header1] [✔ header2] ... [✔ Submit] →
//
// The current tab is rendered in reverse video. Arrows and the Submit tab are
// hidden for single-question single-select forms.
func (o *questionOverlay) renderNavBar(c *qlineCollector) {
	if !o.showSubmitTab() && !o.showArrows() && len(o.form.Questions) <= 1 {
		// Still render the single chip so the user sees the question header.
	}
	var b strings.Builder
	if o.showArrows() {
		if o.current == 0 {
			b.WriteString("\x1b[2m←\x1b[0m ")
		} else {
			b.WriteString("← ")
		}
	}
	for i, q := range o.form.Questions {
		header := strings.TrimSpace(q.ID)
		if header == "" {
			header = fmt.Sprintf("Q%d", i+1)
		}
		answered := o.isQuestionAnswered(i)
		check := "☐"
		if answered {
			check = "✔"
		}
		isCurrent := i == o.current
		chip := fmt.Sprintf(" %s %s ", check, header)
		if isCurrent {
			b.WriteString("\x1b[7m" + chip + "\x1b[0m")
		} else if answered {
			b.WriteString("\x1b[32m" + check + "\x1b[0m " + header)
		} else {
			b.WriteString("\x1b[2m" + chip + "\x1b[0m")
		}
	}
	if o.showSubmitTab() {
		isSubmit := o.current == len(o.form.Questions)
		chip := " ✔ Submit "
		if isSubmit {
			b.WriteString("\x1b[7m" + chip + "\x1b[0m")
		} else {
			b.WriteString("\x1b[2m" + chip + "\x1b[0m")
		}
	}
	if o.showArrows() {
		if o.inSubmitView() {
			b.WriteString(" \x1b[2m→\x1b[0m")
		} else {
			b.WriteString(" →")
		}
	}
	c.writeLine(b.String())
}

// renderSubmitView renders the submit review screen shown when the Submit tab
// is focused: a recap of answers, then a Submit/Cancel select.
func (o *questionOverlay) renderSubmitView() {
	c := o.newCollector()

	// Nav bar
	o.renderNavBar(c)

	c.writeLine("")
	c.writeLinef("\x1b[1mReview your answers\x1b[0m")
	c.writeLine("")

	if !o.allQuestionsAnswered() {
		c.writeLine("\x1b[33m⚠ You have not answered all questions\x1b[0m")
		c.writeLine("")
	}

	for _, q := range o.form.Questions {
		answer := o.answerSummary(q)
		c.writeLinef("  • %s", strings.TrimSpace(q.Prompt))
		if answer != "" {
			c.writeLinef("    \x1b[32m→ %s\x1b[0m", answer)
		} else {
			c.writeLine("    \x1b[2m(no answer)\x1b[0m")
		}
	}

	c.writeLine("")
	c.writeLine("Ready to submit your answers?")
	c.writeLine("")

	// Submit / Cancel select
	labels := []string{"Submit answers", "Cancel"}
	for i, label := range labels {
		pointer := "  "
		color := ""
		if i == o.submitCursor {
			pointer = "\x1b[36m❯\x1b[0m "
			color = "\x1b[36m"
		}
		c.writeLinef("%s%s%s\x1b[0m", pointer, color, label)
	}

	c.writeLine("")
	c.writeLine("\x1b[2mEnter to confirm · ↑/↓ to choose · Esc to cancel\x1b[0m")

	o.finishCollector(c)
}

// answerSummary builds a human-readable summary of a question's answer for the
// submit review view: selected option labels joined by ", " plus any Other
// text.
func (o *questionOverlay) answerSummary(q state.AskQuestion) string {
	i := -1
	for j := range o.form.Questions {
		if o.form.Questions[j].ID == q.ID {
			i = j
			break
		}
	}
	if i < 0 {
		return ""
	}
	sel := o.answers[i]
	var parts []string
	for j, opt := range q.Options {
		if sel[j] {
			parts = append(parts, opt.Label)
		}
	}
	if other := strings.TrimSpace(o.otherText[i]); other != "" {
		parts = append(parts, "Other: "+other)
	}
	return strings.Join(parts, ", ")
}

// ---- viewport collector ----

// qlineCollector collects overlay content as clean visual lines for the
// viewport painter. The painter prepends a separator rule and pins the block
// at the bottom of the viewport surface, keeping the transcript visible and
// text-selectable above.
type qlineCollector struct {
	lines []string
	width int
}

func (o *questionOverlay) newCollector() *qlineCollector {
	width, _ := o.termSize()
	return &qlineCollector{width: width}
}

func (c *qlineCollector) writeLinef(format string, args ...any) {
	line := format
	if len(args) != 0 {
		line = fmt.Sprintf(format, args...)
	}
	c.writeLine(line)
}

func (c *qlineCollector) writeLine(line string) {
	if strings.Contains(line, "\n") {
		c.lines = append(c.lines, strings.Split(line, "\n")...)
		return
	}
	c.lines = append(c.lines, line)
}

func (o *questionOverlay) finishCollector(c *qlineCollector) {
	if o.rs != nil && o.rs.inViewportOverlay() {
		o.rs.setSoftWrappingOverlayComposer(c.lines, -1, 0)
		return
	}
	// Non-viewport fallback: write lines directly.
	for _, line := range c.lines {
		_, _ = fmt.Fprintln(o.out, line)
	}
}

func (o *questionOverlay) termSize() (width, height int) {
	if o != nil && o.rs != nil {
		return o.rs.termSize()
	}
	w, h, err := termSizeWithFallback()
	if err == nil && w > 0 && h > 0 {
		return w, h
	}
	return 80, 24
}

// questionSeparatorLine returns a special marker that will be expanded to a
// full-width separator during soft wrapping. This allows the separator to
// adapt to terminal resizes.
func (c *qlineCollector) questionSeparatorLine() string {
	return "\x00QSEP\x00"
}

// errStdinBusy is returned by acquireComposerInput when another modal
// (e.g. an approval overlay on the agent goroutine) already holds stdinReadMu.
// Callers should surface this as a user-visible message and skip the operation
// rather than retrying, because the lock holder is waiting for user input that
// would deadlock with the caller.
var errStdinBusy = errors.New("stdin busy: another dialog is active — complete it before opening this one")

// rawSelector implements Selector by reading raw bytes directly from the
// terminal fd. It stays in raw mode the entire time — no cooked-mode handoff,
// no stdin race. The caller must ensure no other goroutine reads from `in`
// while a rawSelector method is active (the raw input reader blocks on
// <-resume before we get here).
type rawSelector struct {
	in       *os.File
	mu       *sync.Mutex // renderer mu — held during renders to avoid clobbering
	composer composerSuspender
	renderer *Renderer // non-nil in interactive mode; drives the in-surface composer-overlay path

	// out is set only for standalone commands (onboarding wizard) that own the
	// terminal and have no renderer/viewport to render through. See
	// standalone_render.go.
	out io.Writer
	// placeCaret is installed by whichever modal currently owns the screen and
	// has an editable field on it. dispatchMouse calls it with the position of
	// a plain click inside the overlay — the index of the line the modal wrote
	// and the display column within it — so the modal moves its own text cursor
	// to the cell the user clicked. Modals nest, so it is saved and restored
	// rather than cleared.
	placeCaret func(line, col int)
	// setupPage and setupPages place the prompts in a setup flow: the page
	// they belong to and how many the flow has, zero outside one.
	setupPage, setupPages int
	// pageTop is the first body row a setup page shows.
	pageTop int
}

// composerSuspender hides/restores the interactive composer around a modal
// overlay. *Renderer implements it; nil in tests and non-interactive paths.
type composerSuspender interface {
	EndOverlay()
	BeginComposerOverlay()
}

func newRawSelector(in *os.File, renderer *Renderer) *rawSelector {
	rs := &rawSelector{in: in, renderer: renderer}
	if renderer != nil {
		rs.mu = renderer.Mu()
		rs.composer = renderer
	}
	return rs
}

// newStandaloneRawSelector builds a selector for a command that owns the
// terminal outright (no renderer, no viewport): every prompt is painted
// directly to out. See standalone_render.go.
func newStandaloneRawSelector(in *os.File, out io.Writer) *rawSelector {
	return &rawSelector{in: in, out: out}
}

// inViewportOverlay reports whether the selector should render IN the viewport
// surface (as a bottom-pinned, text-selectable composer block). True only for
// real interactive terminals in viewport mode; false for tests and non-TTY
// paths.
func (s *rawSelector) inViewportOverlay() bool {
	return s != nil && s.renderer != nil && s.renderer.IsViewportMode()
}

// dispatchMouse forwards an SGR mouse report to the viewport's app-level text
// selection while a composer overlay owns the screen. This is what makes the
// overlay text (and the transcript above it) selectable with the same
// press/drag/release/copy semantics as the main conversation turn.
func (s *rawSelector) dispatchMouse(mev sgrMouseEvent) {
	if !s.inViewportOverlay() {
		return
	}
	ev, ok := mouseEventFromSGR(mev)
	if !ok {
		return
	}
	switch ev.kind {
	case inputEventMouseClick:
		s.renderer.ViewportClickToggle(ev.mouseCol, ev.mouseRow)
	case inputEventMousePress:
		s.renderer.ViewportSelectStart(ev.mouseCol, ev.mouseRow)
	case inputEventMouseDrag:
		s.renderer.ViewportSelectDrag(ev.mouseCol, ev.mouseRow)
	case inputEventMouseRelease:
		// A release that ended a drag copied a selection; only a plain click
		// moves the caret, which is what the modal's placeCaret does with the
		// overlay-relative position of the cell that was clicked.
		if click := s.renderer.ViewportSelectEnd(ev.mouseCol, ev.mouseRow); click && s.placeCaret != nil {
			if line, col, ok := s.renderer.OverlayClickPosition(ev.mouseCol, ev.mouseRow); ok {
				s.placeCaret(line, col)
			}
		}
	case inputEventMouseMove:
		s.renderer.ViewportHover(ev.mouseCol, ev.mouseRow)
	case inputEventMouseWheel:
		s.renderer.ViewportScrollAt(ev.wheelDelta, ev.mouseCol, ev.mouseRow)
	}
}

// withCaretPlacer installs the caret hook for the duration of one modal and
// returns the restore for its defer. Nested modals stack, so the previous
// owner's hook is put back rather than dropped.
func (s *rawSelector) withCaretPlacer(place func(line, col int)) func() {
	previous := s.placeCaret
	s.placeCaret = place
	return func() { s.placeCaret = previous }
}

// setOverlayComposer installs the selector's rendered lines as the viewport's
// composer block (with a separator rule above). No-op outside viewport mode.
func (s *rawSelector) setOverlayComposer(lines []string, cursorRow, cursorCol int) {
	if !s.inViewportOverlay() {
		return
	}
	s.renderer.SetOverlayComposer(lines, cursorRow, cursorCol)
}

// setOverlayComposerAtEnd is the variant used for renders that follow a
// decision key: it installs the lines and pins the overlay scroll to the end of
// the content, keeping the option rows visible under a body taller than the
// overlay area. No-op outside viewport mode.
func (s *rawSelector) setOverlayComposerAtEnd(lines []string, cursorRow, cursorCol int) {
	if !s.inViewportOverlay() {
		return
	}
	s.renderer.SetOverlayComposerAtEnd(lines, cursorRow, cursorCol)
}

// setSoftWrappingOverlayComposer is the long-form text variant used by
// user_interaction. The renderer retains logical rows and reflows them at the
// current terminal width, including after resize events.
func (s *rawSelector) setSoftWrappingOverlayComposer(lines []string, cursorRow, cursorCol int) {
	if !s.inViewportOverlay() {
		return
	}
	s.renderer.SetSoftWrappingOverlayComposer(lines, cursorRow, cursorCol)
}

// resetOverlayViewportState clears per-overlay viewport state that can linger
// across modal opens (for example wheel-scrolled overlay offset). Call this
// exactly once when a fresh modal/picker opens, before its first render.
func (s *rawSelector) resetOverlayViewportState() {
	if !s.inViewportOverlay() {
		return
	}
	s.renderer.ResetOverlayScroll()
}

// discardPreOpenInput clears bytes that were already waiting for a newly opened
// modal before it could take exclusive ownership of stdin. It must only be used
// after tryAcquireComposerInput succeeds and before the modal's first render:
// keys entered after that render remain normal modal input.
//
// The raw input reader and readKey both use stdinReadMu, so hold it while
// draining to prevent an overlapping fd read. drainImmediatelyAvailableInput
// consumes queued escape sequences as raw bytes, allowing a short grace window
// for a burst's tail so a partial CSI cannot leak into the new modal.
func (s *rawSelector) discardPreOpenInput() {
	if s == nil || s.in == nil || !s.inViewportOverlay() {
		return
	}
	stdinReadMu.Lock()
	defer stdinReadMu.Unlock()
	drainImmediatelyAvailableInput(s.in)
}

// acquireComposerInput pauses the shared input reader (so the selector owns the
// fd) and marks the renderer for composer-overlay render without clearing the
// screen or suppressing the transcript. The returned closure restores the normal
// composer + input reader on exit.
//
// stdinReadMu is acquired before setting the pause flag so any in-flight
// background Read() on the terminal fd completes first. After releasing it
// the inner reader goroutine checks the pause flag before starting its next
// read, so readKey() (which also holds stdinReadMu around each Read()) never
// races the background reader on the same fd.
func (s *rawSelector) acquireComposerInput() (func(), error) {
	// A new prompt starts its page from the top.
	s.pageTop = 0
	release, ok := s.tryAcquireComposerInput()
	if !ok {
		// Another modal (e.g. an approval overlay opened by the agent
		// goroutine during an active run) holds stdinReadMu.  Return an
		// error so the caller can surface a message instead of deadlocking
		// (as the old blocking Lock did) or crashing (as the old panic did).
		return func() {}, errStdinBusy
	}
	return release, nil
}

// tryAcquireComposerInput is like acquireComposerInput but returns false
// when another modal (e.g. a selector opened by a slash command during an
// active run) already holds stdinReadMu via its readKey(). Callers that
// can be invoked from a different goroutine than the outer modal
// (approvalOverlay, questionOverlay) must use this and cancel themselves
// when ok==false instead of calling readKey() and deadlocking.
func (s *rawSelector) tryAcquireComposerInput() (func(), bool) {
	if !stdinReadMu.TryLock() {
		return func() {}, false
	}
	acquireInteractiveInputPause()
	stdinReadMu.Unlock()
	// Wait for the inner goroutine to acknowledge the pause, retrying with
	// a doubling timeout when the viewport-mode input pipeline is active.
	// The inner goroutine normally responds within 2 ms, but when the
	// middle-goroutine pipeline is congested it may take longer.  Outside
	// of viewport mode (tests, non-TTY) there is no background reader, so
	// we skip the retry and rely on the original 50 ms timeout.
	pauseInteractiveInputReadAndWait(50 * time.Millisecond)
	if s.inViewportOverlay() {
		deadline := time.Now().Add(2 * time.Second)
		limit := 100 * time.Millisecond
		for time.Now().Before(deadline) && interactiveInputPaused.Load() && !interactiveInputPausedAck.Load() {
			pauseInteractiveInputReadAndWait(limit)
			limit *= 2
			if limit > 200*time.Millisecond {
				limit = 200 * time.Millisecond
			}
		}
	}
	// Re-acquire stdinReadMu to drain any read that started before the inner
	// goroutine reached its pause check. By the time we unlock, the inner
	// goroutine is either in its pause loop (not holding the lock) or it has
	// just finished a read and is now acknowledging — in either case the next
	// readKey() call has exclusive access to stdin.
	stdinReadMu.Lock()
	stdinReadMu.Unlock()
	if s.composer != nil {
		s.composer.BeginComposerOverlay()
	}
	return func() {
		if s.composer != nil {
			s.composer.EndOverlay()
		}
		releaseInteractiveInputPause()
	}, true
}

// waitForComposerInput queues this overlay behind whichever surface owns the
// input instead of declining the request. The holder is a user-facing surface
// — a selector, another approval — that never waits on the agent, so the
// queue cannot deadlock: the holder closes on its own or the user closes it,
// and the approval takes over the moment the input frees. Approvals take the
// input one at a time, in turn. Waiting ends when the input is acquired (true)
// or the request's context is cancelled (false); while it lasts, the renderer
// shows how many approvals are waiting under whatever holds the input.
func (o *approvalOverlay) waitForComposerInput() (func(), bool) {
	return waitForComposerInput(o.ctx, o.rs)
}

func (o *questionOverlay) waitForComposerInput() (func(), bool) {
	return waitForComposerInput(o.ctx, o.rs)
}

// approvalTurn is held by the one approval or question that owns the input.
var approvalTurn = make(chan struct{}, 1)

// approvalQueueDepth counts approvals and questions waiting for the input.
var approvalQueueDepth atomic.Int64

func waitForComposerInput(ctx context.Context, rs *rawSelector) (func(), bool) {
	noop := func() {}
	queued := false
	enqueue := func() {
		if !queued {
			queued = true
			noteApprovalQueue(rs, approvalQueueDepth.Add(1))
		}
	}
	defer func() {
		if queued {
			noteApprovalQueue(rs, approvalQueueDepth.Add(-1))
		}
	}()
	select {
	case approvalTurn <- struct{}{}:
	default:
		enqueue()
		select {
		case approvalTurn <- struct{}{}:
		case <-ctx.Done():
			return noop, false
		}
	}
	for {
		// The pause depth is the ownership signal: a modal holds the input for
		// its whole Run, not just during each raw read, so a nonzero depth
		// means another surface owns it even in the gaps between key reads,
		// and trying to acquire in those gaps would steal it.
		if interactiveInputPauseDepth.Load() == 0 {
			if release, acquired := rs.tryAcquireComposerInput(); acquired {
				return func() {
					release()
					<-approvalTurn
				}, true
			}
		}
		enqueue()
		select {
		case <-ctx.Done():
			<-approvalTurn
			return noop, false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// noteApprovalQueue shows the waiting count under whatever overlay holds the
// input; zero clears it.
func noteApprovalQueue(rs *rawSelector, n int64) {
	if rs == nil || rs.renderer == nil {
		return
	}
	rs.renderer.SetOverlayNote(pendingApprovalNote(n))
}

func pendingApprovalNote(n int64) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return "1 approval waiting — it opens when this closes"
	default:
		return fmt.Sprintf("%d approvals waiting — the next opens when this closes", n)
	}
}

// --- key parsing ---

type rawKey int

const (
	rawKeyNone rawKey = iota
	rawKeyUp
	rawKeyDown
	rawKeyLeft
	rawKeyRight
	rawKeyPageUp
	rawKeyPageDown
	rawKeyHome
	rawKeyEnd
	rawKeyEnter
	rawKeyNewline
	rawKeyEscape
	rawKeyCtrlC
	rawKeyCtrlE
	rawKeyBackspace
	rawKeyTab
	rawKeySpace
	rawKeyShiftTab
	rawKeyMouse
	rawKeyPaste
	rawKeyRune
)

type parsedKey struct {
	kind  rawKey
	r     rune
	text  string
	mouse sgrMouseEvent
}

func (s *rawSelector) readKey() (parsedKey, error) {
	return s.readKeyWithMode(false)
}

func (s *rawSelector) readKeyWithMode(multiline bool) (parsedKey, error) {
	// Hold stdinReadMu for the entire call so follow-up reads for escape
	// sequences and UTF-8 continuation bytes are also serialised against
	// the background reader goroutine. All callers of readKey run inside
	// a modal overlay that has already paused the background reader via
	// acquireComposerInput, so the mutex is uncontended.
	stdinReadMu.Lock()
	defer stdinReadMu.Unlock()

	buf := make([]byte, 1)
	_, err := s.in.Read(buf)
	if err != nil {
		return parsedKey{}, err
	}
	b := buf[0]

	switch b {
	case 0x03:
		return parsedKey{kind: rawKeyCtrlC}, nil
	case 0x05:
		return parsedKey{kind: rawKeyCtrlE}, nil
	case 0x1b:
		// Check if more bytes follow within a short window (50ms).
		// In a real terminal, escape sequences arrive as a burst; bare Esc
		// has no follow-up. On a pipe, data is immediately available.
		fd := int(s.in.Fd())
		readable, _ := pollReadableInputFD(fd, 50)
		if !readable {
			return parsedKey{kind: rawKeyEscape}, nil
		}
		next := make([]byte, 1)
		n, err := s.in.Read(next)
		if err != nil || n == 0 {
			return parsedKey{kind: rawKeyEscape}, nil
		}
		if next[0] != '[' && next[0] != 'O' {
			if multiline && (next[0] == '\r' || next[0] == '\n') {
				return parsedKey{kind: rawKeyNewline}, nil
			}
			return parsedKey{kind: rawKeyEscape}, nil
		}
		// SGR mouse report: ESC [ < Cb ; Cx ; Cy (M|m). Read until the M/m
		// terminator rather than the A-Z/~ terminator the arrow-key loop below
		// expects — a release ('m') would otherwise never terminate that loop.
		if next[0] == '[' {
			peek := make([]byte, 1)
			pn, perr := s.in.Read(peek)
			if perr != nil || pn == 0 {
				return parsedKey{kind: rawKeyEscape}, nil
			}
			if peek[0] == '<' {
				seq := []byte{'[', '<'}
				for {
					tb := make([]byte, 1)
					tn, terr := s.in.Read(tb)
					if terr != nil || tn == 0 {
						return parsedKey{kind: rawKeyNone}, nil
					}
					seq = append(seq, tb[0])
					if tb[0] == 'M' || tb[0] == 'm' {
						full := append([]byte{0x1b}, seq...)
						if mev, ok := parseSGRMouse(full); ok {
							return parsedKey{kind: rawKeyMouse, mouse: mev}, nil
						}
						return parsedKey{kind: rawKeyNone}, nil
					}
					if len(seq) > 32 {
						return parsedKey{kind: rawKeyNone}, nil
					}
				}
			}
			// Not a mouse report — fall through with the byte we peeked already
			// part of the sequence.
			seq := []byte{next[0], peek[0]}
			return s.readEscapeTail(seq)
		}
		// Read until we get a terminator (A-Z or ~)
		seq := []byte{next[0]}
		return s.readEscapeTail(seq)
	case '\r':
		return parsedKey{kind: rawKeyEnter}, nil
	case '\n':
		if multiline {
			return parsedKey{kind: rawKeyNewline}, nil
		}
		return parsedKey{kind: rawKeyEnter}, nil
	case 0x7f, 0x08:
		return parsedKey{kind: rawKeyBackspace}, nil
	case '\t':
		return parsedKey{kind: rawKeyTab}, nil
	case ' ':
		return parsedKey{kind: rawKeySpace}, nil
	}

	if b < 0x20 {
		return parsedKey{kind: rawKeyNone}, nil
	}

	// UTF-8: accumulate multi-byte rune
	pending := []byte{b}
	for !utf8.FullRune(pending) {
		extra := make([]byte, 1)
		n, err := s.in.Read(extra)
		if err != nil || n == 0 {
			return parsedKey{kind: rawKeyNone}, nil
		}
		pending = append(pending, extra[0])
		if len(pending) > 4 {
			return parsedKey{kind: rawKeyNone}, nil
		}
	}
	r, size := utf8.DecodeRune(pending)
	if r == utf8.RuneError && size == 1 {
		return parsedKey{kind: rawKeyNone}, nil
	}
	return parsedKey{kind: rawKeyRune, r: r}, nil
}

// readEscapeTail consumes a CSI/SS3 escape sequence body (seq already holds the
// bytes after ESC) up to its A-Z or '~' terminator and maps it to a navigation
// key. Returns rawKeyNone for sequences it does not recognize.
func (s *rawSelector) readEscapeTail(seq []byte) (parsedKey, error) {
	for {
		// Terminator may already be present (caller pre-read bytes).
		if n := len(seq); n > 0 {
			c := seq[n-1]
			if (c >= 'A' && c <= 'Z') || c == '~' {
				if string(seq) == "[200~" {
					return s.readBracketedPaste()
				}
				return mapEscapeSeq(seq), nil
			}
		}
		tb := make([]byte, 1)
		tn, terr := s.in.Read(tb)
		if terr != nil || tn == 0 {
			return parsedKey{kind: rawKeyEscape}, nil
		}
		seq = append(seq, tb[0])
		if len(seq) > 32 {
			return parsedKey{kind: rawKeyNone}, nil
		}
	}
}

func (s *rawSelector) readBracketedPaste() (parsedKey, error) {
	end := []byte{0x1b, '[', '2', '0', '1', '~'}
	var content []byte
	for {
		b := make([]byte, 1)
		n, err := s.in.Read(b)
		if err != nil {
			return parsedKey{}, err
		}
		if n == 0 {
			continue
		}
		content = append(content, b[0])
		if bytes.HasSuffix(content, end) {
			content = content[:len(content)-len(end)]
			return parsedKey{kind: rawKeyPaste, text: sanitizeTerminalInputText(string(content))}, nil
		}
	}
}

func mapEscapeSeq(seq []byte) parsedKey {
	if len(seq) == 0 {
		return parsedKey{kind: rawKeyNone}
	}
	c := seq[len(seq)-1]
	switch c {
	case 'A':
		return parsedKey{kind: rawKeyUp}
	case 'B':
		return parsedKey{kind: rawKeyDown}
	case 'C':
		return parsedKey{kind: rawKeyRight}
	case 'D':
		return parsedKey{kind: rawKeyLeft}
	case 'Z':
		return parsedKey{kind: rawKeyShiftTab}
	case 'H':
		return parsedKey{kind: rawKeyHome}
	case 'F':
		return parsedKey{kind: rawKeyEnd}
	case '~':
		switch string(seq) {
		case "[1~", "[7~":
			return parsedKey{kind: rawKeyHome}
		case "[4~", "[8~":
			return parsedKey{kind: rawKeyEnd}
		case "[5~":
			return parsedKey{kind: rawKeyPageUp}
		case "[6~":
			return parsedKey{kind: rawKeyPageDown}
		}
	}
	return parsedKey{kind: rawKeyNone}
}

// --- terminal size ---

func (s *rawSelector) termSize() (width, height int) {
	// Check for test-injected forced dimensions first
	if forcedTermWidth > 0 || forcedTermHeight > 0 {
		w, h := forcedTermWidth, forcedTermHeight
		if w <= 0 {
			w = 80
		}
		if h <= 0 {
			h = 24
		}
		return w, h
	}
	if s.in != nil {
		w, h, err := term.GetSize(int(s.in.Fd()))
		if err == nil && w > 0 && h > 0 {
			return w, h
		}
	}
	fds := []int{int(os.Stdout.Fd()), int(os.Stderr.Fd())}
	for _, fd := range fds {
		w, h, err := term.GetSize(fd)
		if err == nil && w > 0 && h > 0 {
			return w, h
		}
	}
	return 80, 24
}

// --- rendering ---

const (
	selectorFilterStyle = "\x1b[33m" // yellow — a prompt's label outside the viewport
	selectorResetStyle  = "\x1b[0m"
)

// Picker glyphs: the cursor on the selected row, and the check boxes of a
// multi-select.
const (
	selectorCursorGlyph    = "❯"
	selectorCheckedGlyph   = "●"
	selectorUncheckedGlyph = "○"
)

// filterField is the editable filter a picker shows next to its label: the
// text plus the caret position inside it. Typing, backspace, the arrow keys and
// a mouse click all act at the caret, which is why the pickers keep one instead
// of appending to a bare string.
type filterField struct {
	text   string
	cursor int // rune offset into text
}

func (f *filterField) runes() []rune { return []rune(f.text) }

func (f *filterField) insert(text string) {
	if text == "" {
		return
	}
	runes, cursor := insertRunesAtCursor(f.runes(), f.cursor, []rune(text))
	f.text, f.cursor = string(runes), cursor
}

// backspace deletes the rune before the caret and reports whether anything was
// deleted, so the caller re-filters only when the query actually changed.
func (f *filterField) backspace() bool {
	runes, cursor, deleted := deleteRuneBeforeCursor(f.runes(), f.cursor)
	if !deleted {
		return false
	}
	f.text, f.cursor = string(runes), cursor
	return true
}

func (f *filterField) move(delta int) {
	f.cursor = composerCursorClampRunes(f.runes(), f.cursor+delta)
}

func (f *filterField) moveTo(cursor int) {
	f.cursor = composerCursorClampRunes(f.runes(), cursor)
}

// placeCaretAt moves the caret onto the text cell a click at display column col
// landed on, measured against the label that precedes the filter on the row.
func (f *filterField) placeCaretAt(prefixWidth, col int) {
	f.cursor = caretIndexForClick(f.runes(), prefixWidth, col)
}

type selectState struct {
	options     []string
	filtered    []int // indices into options
	cursor      int   // index into filtered
	filter      filterField
	label       string
	esc         string // what Esc does, for the key hint
	multiSelect bool
	selected    map[int]bool // indices into options (for multi-select)
}

func (st *selectState) applyFilter() {
	query := strings.ToLower(strings.TrimSpace(st.filter.text))
	st.filtered = st.filtered[:0]
	ranks := make(map[int]int, len(st.options))
	for i, opt := range st.options {
		if rank := pickerMatchRank(opt, "", query); rank >= 0 {
			st.filtered = append(st.filtered, i)
			ranks[i] = rank
		}
	}
	sort.SliceStable(st.filtered, func(a, b int) bool { return ranks[st.filtered[a]] < ranks[st.filtered[b]] })
	// Reset the cursor to the first matching option so the selection arrow
	// is always on the first item after a filter change. Without this the
	// cursor stays at its old (possibly off-list) index and is merely
	// clamped, leaving the arrow on a non-first option.
	st.cursor = 0
}

func (s *rawSelector) renderSelect(st *selectState) {
	s.showPanel(st.panel())
}

// panel lays the picker out as a slash panel: the label and the filter in the
// head, one row per matching option — the ❯ on the focused one, a check box
// before each in a multi-select — and the keys.
func (st *selectState) panel() slashPanel {
	b := newPanelBuilder()
	pickerHead(b, st.label)
	for vi, optIdx := range st.filtered {
		lead := ""
		if st.multiSelect {
			lead = selectorUncheckedGlyph + " "
			if st.selected[optIdx] {
				lead = selectorCheckedGlyph + " "
			}
		}
		b.selectable(vi == st.cursor, lead, st.options[optIdx])
	}
	if len(st.filtered) == 0 {
		b.text(panelIndent, "No matches", nil)
	}
	b.hint(selectFooterHint(st.multiSelect, st.esc))
	p := b.panel()
	p.field = st.filter.panelField()
	return p
}

// panelField is the filter as a panel's field.
func (f *filterField) panelField() *panelField {
	return &panelField{text: f.runes(), cursor: f.cursor, placeholder: "Type to filter"}
}

// pickerHead puts a picker's label in its panel's head: the first line as the
// title, the lines after it as the subtitle.
func pickerHead(b *panelBuilder, label string) {
	lines := compactNonEmpty(strings.Split(strings.ReplaceAll(label, "\r", ""), "\n"))
	if len(lines) == 0 {
		return
	}
	b.title(lines[0])
	if len(lines) > 1 {
		b.subtitle(strings.Join(lines[1:], " "))
	}
}

func selectFooterHint(multi bool, esc string) string {
	if esc == "" {
		esc = "Esc to cancel"
	}
	if multi {
		return "↑/↓ to navigate · Space to select · Enter to confirm · " + esc
	}
	return "↑/↓ to navigate · Enter to confirm · " + esc
}

// showPanel paints a selector's panel: pinned in the overlay area inside the
// viewport, or as a setup page outside it; either way a key always brings the
// selection into view.
func (s *rawSelector) showPanel(p slashPanel) {
	if s.standalone() {
		s.paintPage(p)
		return
	}
	if s.inViewportOverlay() {
		s.renderer.SetOverlayPanel(p, false, true)
	}
}

// panelWidth is the width the overlay area lays a panel out at.
func (s *rawSelector) panelWidth() int {
	width, _ := s.termSize()
	return maxInt(1, width-viewportRightPadding)
}

// placeFieldCaret moves a panel field's caret onto the cell a click at line
// and col landed on, reporting whether the click was on the field's row.
func (s *rawSelector) placeFieldCaret(p slashPanel, f *filterField, line, col int) bool {
	if line != p.fieldRow(s.panelWidth()) {
		return false
	}
	f.placeCaretAt(displayLineWidth(panelFieldLead), col)
	return true
}

// --- Select ---

func (s *rawSelector) Select(label string, options []string, defaultOption string) (string, bool, error) {
	if len(options) == 0 {
		return "", false, nil
	}
	release, err := s.acquireComposerInput()
	if err != nil {
		return "", false, err
	}
	defer release()

	menuOptions, values := pickerOptions(options)
	if len(menuOptions) == 0 {
		return "", false, nil
	}

	st := &selectState{
		options:  menuOptions,
		filtered: make([]int, 0, len(menuOptions)),
		label:    label,
		esc:      s.escHint(),
	}
	st.applyFilter()
	for i, optIdx := range st.filtered {
		if values[optIdx] == strings.TrimSpace(defaultOption) {
			st.cursor = i
			break
		}
	}
	// Honor defaultOption only for the initial open. Subsequent filter edits
	// still call applyFilter(), which resets the cursor to the first matching
	// option so type-to-filter behavior stays predictable.
	s.resetOverlayViewportState()

	s.renderSelect(st)
	defer s.withCaretPlacer(func(line, col int) {
		if s.placeFieldCaret(st.panel(), &st.filter, line, col) {
			s.renderSelect(st)
		}
	})()

	for {
		key, err := s.readKey()
		if err != nil {
			return "", false, err
		}
		switch key.kind {
		case rawKeyNone:
			continue
		case rawKeyMouse:
			// Forward to the viewport's app-level text selection so overlay and
			// transcript text is selectable/copyable like the main turn.
			s.dispatchMouse(key.mouse)
			continue
		case rawKeyUp:
			if st.cursor > 0 {
				st.cursor--
			}
		case rawKeyDown:
			if st.cursor < len(st.filtered)-1 {
				st.cursor++
			}
		case rawKeyEnter:
			if len(st.filtered) == 0 {
				return "", false, nil
			}
			return values[st.filtered[st.cursor]], true, nil
		case rawKeyEscape, rawKeyCtrlC:
			return "", false, s.dismissed(key.kind)
		case rawKeyBackspace:
			if st.filter.backspace() {
				st.applyFilter()
			}
		case rawKeyLeft:
			st.filter.move(-1)
		case rawKeyRight:
			st.filter.move(1)
		case rawKeyHome:
			st.filter.moveTo(0)
		case rawKeyEnd:
			st.filter.moveTo(len(st.filter.runes()))
		case rawKeyTab:
			// Tab does nothing in select
			continue
		case rawKeyPaste:
			st.filter.insert(strings.ReplaceAll(key.text, "\n", " "))
			st.applyFilter()
		case rawKeyRune:
			st.filter.insert(string(key.r))
			st.applyFilter()
		case rawKeySpace:
			st.filter.insert(" ")
			st.applyFilter()
		}
		s.renderSelect(st)
	}
}

// memorySettingsPanel is the /memories panel: the two switches and the reset
// row, or, once reset is chosen, what to reset.
func memorySettingsPanel(cursor int, useMemories, generateMemories, confirming bool) slashPanel {
	rows := []string{
		"Use memories — Use memories in following threads. Applied at next thread.",
		"Generate memories — Generate memories from following threads. Current thread included.",
		"Reset memories — Clear local memory files and summaries. Threads remain intact.",
	}
	b := newPanelBuilder()
	if confirming {
		rows = []string{"Reset this project's memories", "Reset everything (every project + global)", "Go back"}
		b.title("Reset memories")
		b.hint("↑/↓ to navigate · Enter to confirm · Esc to go back")
	} else {
		b.title("Memories")
		b.hint("↑/↓ to navigate · Space to toggle · Enter to save · Esc to cancel")
	}
	for i, row := range rows {
		box := ""
		if !confirming && i < 2 {
			checked := useMemories
			if i == 1 {
				checked = generateMemories
			}
			box = "[ ] "
			if checked {
				box = "[x] "
			}
		}
		b.selectable(i == cursor, box, row)
	}
	return b.panel()
}

func (s *rawSelector) SelectMemorySettings(useMemories, generateMemories bool) (MemorySettingsResult, error) {
	release, err := s.acquireComposerInput()
	if err != nil {
		return MemorySettingsResult{}, err
	}
	defer release()
	s.resetOverlayViewportState()
	cursor := 0
	confirming := false
	render := func() {
		s.showPanel(memorySettingsPanel(cursor, useMemories, generateMemories, confirming))
	}
	render()
	for {
		key, err := s.readKey()
		if err != nil {
			return MemorySettingsResult{}, err
		}
		count := 3
		switch key.kind {
		case rawKeyNone:
			continue
		case rawKeyMouse:
			s.dispatchMouse(key.mouse)
			continue
		case rawKeyUp:
			if cursor > 0 {
				cursor--
			}
		case rawKeyDown:
			if cursor < count-1 {
				cursor++
			}
		case rawKeySpace:
			if !confirming {
				switch cursor {
				case 0:
					useMemories = !useMemories
				case 1:
					generateMemories = !generateMemories
				}
			}
		case rawKeyEnter:
			if confirming {
				switch cursor {
				case 0:
					return MemorySettingsResult{Action: MemorySettingsResetProject}, nil
				case 1:
					return MemorySettingsResult{Action: MemorySettingsResetAll}, nil
				default:
					confirming = false
					cursor = 2
				}
			} else if cursor == 2 {
				confirming = true
				// Default to "Go back" (the last, safest row) so confirming a
				// destructive reset always takes a deliberate Up before Enter.
				cursor = 2
			} else {
				return MemorySettingsResult{Action: MemorySettingsSave, UseMemories: useMemories, GenerateMemories: generateMemories}, nil
			}
		case rawKeyEscape:
			if confirming {
				confirming = false
				cursor = 2
			} else {
				return MemorySettingsResult{Action: MemorySettingsCancel}, nil
			}
		case rawKeyCtrlC:
			return MemorySettingsResult{Action: MemorySettingsCancel}, nil
		}
		render()
	}
}

// --- ShowInfo ---

// ShowInfo paints a read-only report as a composer overlay and returns when the
// user dismisses it. It reuses the picker's machinery — the same input
// acquisition, the same bottom-pinned block, the same mouse forwarding — so the
// report is selectable and scrollable exactly like a picker, and the transcript
// stays visible above it. Body lines are handed over as logical rows and reflow
// at the current width, which matters here because a status line can be wider
// than the terminal.
func (s *rawSelector) ShowInfo(label string, body string) error {
	_, err := s.showInfoBlock(label, body, "")
	return err
}

// ConfirmInfo shows a report the user has to read before deciding, and asks
// for the decision in the same panel: Enter takes action, Esc declines. The
// key hint is pinned under the report, so both keys are on screen however
// long the report is; the report scrolls.
func (s *rawSelector) ConfirmInfo(label string, body string, action string) (bool, error) {
	return s.showInfoBlock(label, body, action)
}

// showInfoBlock draws a report as a slash panel and waits for the key that
// closes it. With no action, Enter or Esc close it; with one,
// Enter takes the action (true) and Esc declines it. Keys typed before the
// block opened are dropped, so a report cannot be dismissed by a key the
// user pressed while waiting for it.
func (s *rawSelector) showInfoBlock(label string, body string, action string) (bool, error) {
	release, err := s.acquireComposerInput()
	if err != nil {
		return false, err
	}
	defer release()
	s.resetOverlayViewportState()
	s.discardPreOpenInput()

	panel := infoPanel(label, body, action)
	render := func() { s.showPanel(panel) }
	scroll := func(delta int) {
		switch {
		case s.standalone():
			s.pageTop = max(0, s.pageTop+delta)
		case s.renderer != nil && s.inViewportOverlay():
			s.renderer.ViewportScrollOverlay(delta)
		}
	}
	render()
	for {
		key, err := s.readKey()
		if err != nil {
			return false, err
		}
		switch key.kind {
		case rawKeyMouse:
			s.dispatchMouse(key.mouse)
			continue
		case rawKeyEnter:
			return action != "", nil
		case rawKeyEscape, rawKeyCtrlC:
			return false, nil
		case rawKeyUp:
			scroll(-1)
		case rawKeyDown:
			scroll(1)
		case rawKeyPageUp:
			scroll(-10)
		case rawKeyPageDown:
			scroll(10)
		case rawKeyHome:
			scroll(-overlayScrollToEdge)
		case rawKeyEnd:
			scroll(overlayScrollToEdge)
		case rawKeyRune:
			if action == "" && (key.r == 'q' || key.r == 'Q') {
				return false, nil
			}
		}
		render()
	}
}

// infoPanel lays a report out as a slash panel: the label in the head, the
// report as the body and the keys that close it — or, for a report that asks
// for a decision, the keys that take and decline its action.
func infoPanel(label string, body string, action string) slashPanel {
	b := newPanelBuilder()
	pickerHead(b, label)
	for _, row := range strings.Split(strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), "\n"), "\n") {
		b.body = append(b.body, panelReportLine(row))
	}
	if action != "" {
		b.hint("Enter to " + action + " · Esc to cancel · ↑/↓ PgUp/PgDn to scroll")
	} else {
		b.hint("↑/↓ PgUp/PgDn to scroll · Enter or Esc to close")
	}
	return b.panel()
}

// panelReportLine is one line of a report, its indentation and list bullet
// kept as the lead its wrapped rows hang under.
func panelReportLine(row string) panelLine {
	if strings.TrimSpace(row) == "" {
		return panelLine{}
	}
	text := strings.TrimLeft(row, " ")
	lead := panelIndent + row[:len(row)-len(text)]
	for _, bullet := range []string{"· ", "↳ ", "• ", "- "} {
		if strings.HasPrefix(text, bullet) {
			lead += bullet
			text = text[len(bullet):]
			break
		}
	}
	return panelLine{lead: lead, text: text}
}

// --- MultiSelect ---

func (s *rawSelector) MultiSelect(label string, options []string, defaultOptions []string) ([]string, bool, error) {
	if len(options) == 0 {
		return nil, false, nil
	}
	release, err := s.acquireComposerInput()
	if err != nil {
		return nil, false, err
	}
	defer release()

	menuOptions, values := pickerOptions(options)
	if len(menuOptions) == 0 {
		return nil, false, nil
	}
	defaults := make(map[string]bool, len(defaultOptions))
	for _, opt := range defaultOptions {
		defaults[strings.TrimSpace(opt)] = true
	}
	selected := make(map[int]bool, len(defaults))
	for i, value := range values {
		if defaults[value] {
			selected[i] = true
		}
	}

	st := &selectState{
		options:     menuOptions,
		filtered:    make([]int, 0, len(menuOptions)),
		label:       label,
		esc:         s.escHint(),
		multiSelect: true,
		selected:    selected,
	}
	st.applyFilter()

	s.renderSelect(st)
	defer s.withCaretPlacer(func(line, col int) {
		if s.placeFieldCaret(st.panel(), &st.filter, line, col) {
			s.renderSelect(st)
		}
	})()

	for {
		key, err := s.readKey()
		if err != nil {
			return nil, false, err
		}
		switch key.kind {
		case rawKeyNone:
			continue
		case rawKeyMouse:
			s.dispatchMouse(key.mouse)
			continue
		case rawKeyUp:
			if st.cursor > 0 {
				st.cursor--
			}
		case rawKeyDown:
			if st.cursor < len(st.filtered)-1 {
				st.cursor++
			}
		case rawKeyEnter:
			out := make([]string, 0, len(selected))
			// Preserve original order
			for i, value := range values {
				if selected[i] {
					out = append(out, value)
				}
			}
			return out, true, nil
		case rawKeyEscape, rawKeyCtrlC:
			return nil, false, s.dismissed(key.kind)
		case rawKeyBackspace:
			if st.filter.backspace() {
				st.applyFilter()
			}
		case rawKeyLeft:
			st.filter.move(-1)
		case rawKeyRight:
			st.filter.move(1)
		case rawKeyHome:
			st.filter.moveTo(0)
		case rawKeyEnd:
			st.filter.moveTo(len(st.filter.runes()))
		case rawKeySpace:
			// Toggle selection
			if len(st.filtered) > 0 {
				optIdx := st.filtered[st.cursor]
				if selected[optIdx] {
					delete(selected, optIdx)
				} else {
					selected[optIdx] = true
				}
			}
		case rawKeyTab:
			continue
		case rawKeyPaste:
			st.filter.insert(strings.ReplaceAll(key.text, "\n", " "))
			st.applyFilter()
		case rawKeyRune:
			st.filter.insert(string(key.r))
			st.applyFilter()
		}
		s.renderSelect(st)
	}
}

// --- Input ---

func (s *rawSelector) Input(label string, defaultValue string) (string, bool, error) {
	return s.input(label, defaultValue, false)
}

// Secret asks for a value that must not be shown, such as an API key: the
// field masks what is typed or pasted and shows only its last characters,
// enough to tell which key it is.
func (s *rawSelector) Secret(label string, defaultValue string) (string, bool, error) {
	return s.input(label, defaultValue, true)
}

func (s *rawSelector) input(label string, defaultValue string, secret bool) (string, bool, error) {
	release, err := s.acquireComposerInput()
	if err != nil {
		return "", false, err
	}
	defer release()
	value := []rune(defaultValue)
	cursor := len(value)
	panel := func() slashPanel { return inputPanel(label, value, cursor, secret, s.escHint()) }
	s.showPanel(panel())
	// Clicks on the field row move the caret; the field's text starts after
	// its prompt marker.
	defer s.withCaretPlacer(func(line, col int) {
		if line != panel().fieldRow(s.panelWidth()) {
			return
		}
		cursor = caretIndexForClick(value, displayLineWidth(panelFieldLead), col)
		s.showPanel(panel())
	})()

	for {
		key, err := s.readKey()
		if err != nil {
			return "", false, err
		}
		switch key.kind {
		case rawKeyNone:
			continue
		case rawKeyMouse:
			s.dispatchMouse(key.mouse)
			continue
		case rawKeyEnter:
			return strings.TrimSpace(string(value)), true, nil
		case rawKeyEscape, rawKeyCtrlC:
			return "", false, s.dismissed(key.kind)
		case rawKeyLeft:
			if cursor > 0 {
				cursor--
			}
		case rawKeyRight:
			if cursor < len(value) {
				cursor++
			}
		case rawKeyBackspace:
			value, cursor, _ = deleteRuneBeforeCursor(value, cursor)
		case rawKeyPaste:
			value, cursor = insertRunesAtCursor(value, cursor, []rune(key.text))
		case rawKeyRune:
			value, cursor = insertRunesAtCursor(value, cursor, []rune{key.r})
		case rawKeySpace:
			value, cursor = insertRunesAtCursor(value, cursor, []rune{' '})
		case rawKeyTab:
			continue
		default:
			continue
		}
		s.showPanel(panel())
	}
}

// inputPanel lays a question out as a slash panel: the question in the head,
// the answer as its field — masked when it is a secret — and the keys that
// answer it.
func inputPanel(title string, value []rune, cursor int, secret bool, esc string) slashPanel {
	b := newPanelBuilder()
	pickerHead(b, title)
	b.hint("Enter to confirm · " + esc)
	p := b.panel()
	if secret {
		value = maskSecret(value)
	}
	p.field = &panelField{text: value, cursor: cursor}
	return p
}

// maskSecret hides a secret behind dots, one per character so the caret
// still lands where it is, keeping the last four in sight once there are
// enough characters left hidden that they give nothing away.
func maskSecret(value []rune) []rune {
	const shown = 4
	masked := make([]rune, len(value))
	for i, r := range value {
		masked[i] = '•'
		if len(value) > 2*shown && i >= len(value)-shown {
			masked[i] = r
		}
	}
	return masked
}

// --- Confirm ---

func (s *rawSelector) Confirm(label string, defaultValue bool) (bool, bool, error) {
	release, err := s.acquireComposerInput()
	if err != nil {
		return false, false, err
	}
	defer release()
	keys := "Y to confirm · N or Enter to decline · "
	if defaultValue {
		keys = "Y or Enter to confirm · N to decline · "
	}
	b := newPanelBuilder()
	pickerHead(b, label)
	b.hint(keys + s.escHint())
	s.showPanel(b.panel())

	for {
		key, err := s.readKey()
		if err != nil {
			return false, false, err
		}
		switch key.kind {
		case rawKeyNone:
			continue
		case rawKeyMouse:
			s.dispatchMouse(key.mouse)
			continue
		case rawKeyEnter:
			return defaultValue, true, nil
		case rawKeyEscape, rawKeyCtrlC:
			return false, false, s.dismissed(key.kind)
		case rawKeyRune:
			switch key.r {
			case 'y', 'Y':
				return true, true, nil
			case 'n', 'N':
				return false, true, nil
			}
		default:
			continue
		}
	}
}

// --- Review ---

// Review shows facts for the user to check and the actions they can take on
// them, opening on actions[defaultIdx] (the first when -1), and returns the
// index of the action chosen. The label is the title, with any lines after
// the first as its subtitle.
func (s *rawSelector) Review(label string, facts []turn.StatusFact, actions []string, defaultIdx int) (int, bool, error) {
	if len(actions) == 0 {
		return -1, false, nil
	}
	release, err := s.acquireComposerInput()
	if err != nil {
		return -1, false, err
	}
	defer release()
	cursor := 0
	if defaultIdx >= 0 && defaultIdx < len(actions) {
		cursor = defaultIdx
	}
	panel := func() slashPanel {
		b := newPanelBuilder()
		pickerHead(b, label)
		b.facts(facts, nil)
		b.blank()
		for i, action := range actions {
			b.selectable(i == cursor, "", action)
		}
		b.hint("↑/↓ to navigate · Enter to confirm · " + s.escHint())
		return b.panel()
	}
	s.showPanel(panel())
	for {
		key, err := s.readKey()
		if err != nil {
			return -1, false, err
		}
		switch key.kind {
		case rawKeyMouse:
			s.dispatchMouse(key.mouse)
			continue
		case rawKeyUp:
			cursor = max(0, cursor-1)
		case rawKeyDown:
			cursor = min(len(actions)-1, cursor+1)
		case rawKeyEnter:
			return cursor, true, nil
		case rawKeyEscape, rawKeyCtrlC:
			return -1, false, s.dismissed(key.kind)
		default:
			continue
		}
		s.showPanel(panel())
	}
}

// --- helpers ---

// pickerMatchRank is how well a picker row matches a lowercased filter, lower
// is better and -1 is no match: the label equal to the filter, the label
// starting with it, a word of the label starting with it, the label holding
// it, the description holding it. Every picker lists its matches in this
// order, keeping the rows' own order within a rank.
func pickerMatchRank(label, description, query string) int {
	if query == "" {
		return 0
	}
	label = strings.ToLower(label)
	switch at := strings.Index(label, query); {
	case label == query:
		return 0
	case at == 0:
		return 1
	case at > 0 && pickerWordStartsWith(label, query):
		return 2
	case at > 0:
		return 3
	case strings.Contains(strings.ToLower(description), query):
		return 4
	}
	return -1
}

// pickerWordStartsWith reports whether a word of s — a run of letters and
// digits — starts with query.
func pickerWordStartsWith(s, query string) bool {
	for at := 0; at < len(s); {
		i := strings.Index(s[at:], query)
		if i < 0 {
			return false
		}
		i += at
		if prev, _ := utf8.DecodeLastRuneInString(s[:i]); i == 0 || !isPickerWordRune(prev) {
			return true
		}
		at = i + 1
	}
	return false
}

func isPickerWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// pickerRightPad keeps a picker's rows off the terminal's last columns, where
// a row would reach the autowrap threshold.
const pickerRightPad = 2

// wrapMenuText wraps a menu row's text at width: whole, word-wrapped, a word
// longer than the width broken — never cut short.
func wrapMenuText(text string, width int) []string {
	return strings.Split(xansi.WrapWc(text, max(width, 1), ""), "\n")
}

// pickerOptions is what a picker lists for its options — each one on one
// line, its whitespace collapsed — and, row for row, the option it stands
// for, trimmed. Blank options are left out.
func pickerOptions(options []string) (display, values []string) {
	for _, opt := range options {
		if row := sanitizeInteractiveOption(opt); row != "" {
			display = append(display, row)
			values = append(values, strings.TrimSpace(opt))
		}
	}
	return display, values
}

// menuWindowAround is the run of rows [start, end) an inline menu shows within
// budget screen lines around the selected row: about half the room above it,
// the rest below, and whatever an end of the list leaves over.
func menuWindowAround(heights []int, selected, budget int) (start, end int) {
	n := len(heights)
	if n == 0 {
		return 0, 0
	}
	selected = min(max(selected, 0), n-1)
	start, end = selected, selected+1
	used := heights[selected]
	for above := 0; start > 0 && above+heights[start-1] <= (budget-heights[selected])/2; {
		start--
		above += heights[start]
		used += heights[start]
	}
	for end < n && used+heights[end] <= budget {
		used += heights[end]
		end++
	}
	for start > 0 && used+heights[start-1] <= budget {
		start--
		used += heights[start]
	}
	return start, end
}

// --- SelectRich ---

// richPickerState extends selectState with pre-computed selectable indices
// (items where Disabled=false) so j/k navigation is O(1) per keypress.
type richPickerState struct {
	items      []SelectItem
	selectable []int // indices into items where Disabled=false
	cursor     int   // index into selectable
	filter     filterField
	esc        string // what Esc does, for the key hint
}

func (p *richPickerState) rebuildSelectable() {
	query := strings.ToLower(strings.TrimSpace(p.filter.text))
	p.selectable = p.selectable[:0]
	ranks := make(map[int]int, len(p.items))
	// A category ranks by its best match, so the rows stay grouped under one
	// header each while the best match leads.
	categoryRank := map[string]int{}
	for i, item := range p.items {
		if item.Disabled {
			continue
		}
		rank := pickerMatchRank(item.Label, item.Description, query)
		if rank < 0 {
			continue
		}
		p.selectable = append(p.selectable, i)
		ranks[i] = rank
		if best, ok := categoryRank[item.Category]; !ok || rank < best {
			categoryRank[item.Category] = rank
		}
	}
	sort.SliceStable(p.selectable, func(a, b int) bool {
		ia, ib := p.items[p.selectable[a]], p.items[p.selectable[b]]
		if ia.Category != ib.Category {
			return categoryRank[ia.Category] < categoryRank[ib.Category]
		}
		return ranks[p.selectable[a]] < ranks[p.selectable[b]]
	})
	// Reset the cursor to the first selectable item so the selection arrow
	// is always on the first option after a filter change. Without this the
	// cursor stays at its old (possibly off-list) index and is merely
	// clamped, leaving the arrow on a non-first option.
	p.cursor = 0
}

func (p *richPickerState) absoluteRow() int {
	if len(p.selectable) == 0 {
		return -1
	}
	return p.selectable[p.cursor]
}

func (s *rawSelector) renderRichPicker(p *richPickerState, label string) {
	s.showPanel(p.panel(label))
}

// panel lays the rich picker out as a slash panel: the label and the filter
// in the head, the matching items grouped under their categories' headings,
// each with its description, the ❯ on the focused one, and the keys.
func (p *richPickerState) panel(label string) slashPanel {
	b := newPanelBuilder()
	pickerHead(b, label)
	category := ""
	for ci, idx := range p.selectable {
		item := p.items[idx]
		if item.Category != category && item.Category != "" {
			b.group(item.Category)
			category = item.Category
		}
		text := item.Label
		if desc := strings.TrimSpace(item.Description); desc != "" {
			text += " — " + desc
		}
		b.selectable(ci == p.cursor, "", text)
	}
	if len(p.selectable) == 0 {
		b.text(panelIndent, "No matches", nil)
	}
	b.hint(selectFooterHint(false, p.esc))
	panel := b.panel()
	panel.field = p.filter.panelField()
	return panel
}

func (s *rawSelector) SelectRich(label string, items []SelectItem, defaultIdx int) (int, bool, error) {
	return s.selectRichPaged(label, items, defaultIdx, nil)
}

// SelectRichPaged behaves like SelectRich but grows the item list lazily:
// pressing ↓ on the last loaded row asks fetchMore for the next page and
// appends it, so a store-backed picker can browse an arbitrarily long list
// while holding only the pages the user actually visited. An empty page from
// fetchMore marks the source exhausted and stops further calls.
func (s *rawSelector) SelectRichPaged(label string, items []SelectItem, defaultIdx int, fetchMore func() []SelectItem) (int, bool, error) {
	if fetchMore == nil {
		return s.SelectRich(label, items, defaultIdx)
	}
	return s.selectRichPaged(label, items, defaultIdx, fetchMore)
}

func (s *rawSelector) selectRichPaged(label string, items []SelectItem, defaultIdx int, fetchMore func() []SelectItem) (int, bool, error) {
	if len(items) == 0 {
		return -1, false, nil
	}
	release, err := s.acquireComposerInput()
	if err != nil {
		return -1, false, err
	}
	defer release()

	p := &richPickerState{
		items:      items,
		selectable: make([]int, 0, len(items)),
		esc:        s.escHint(),
	}
	p.rebuildSelectable()
	// The picker opens on the row the caller names — the setting in force,
	// or the answer given before on a page shown again — and on the first
	// row when it names none.
	for ci, idx := range p.selectable {
		if idx == defaultIdx {
			p.cursor = ci
			break
		}
	}

	s.renderRichPicker(p, label)
	defer s.withCaretPlacer(func(line, col int) {
		if s.placeFieldCaret(p.panel(label), &p.filter, line, col) {
			s.renderRichPicker(p, label)
		}
	})()

	for {
		key, err := s.readKey()
		if err != nil {
			return -1, false, err
		}
		switch key.kind {
		case rawKeyNone:
			continue
		case rawKeyMouse:
			s.dispatchMouse(key.mouse)
			continue
		case rawKeyUp:
			if p.cursor > 0 {
				p.cursor--
			}
		case rawKeyDown:
			if p.cursor < len(p.selectable)-1 {
				p.cursor++
			} else if fetchMore != nil {
				more := fetchMore()
				if len(more) > 0 {
					// rebuildSelectable resets the cursor (wanted after a
					// filter edit, wrong here): restore it onto the row the
					// user is on, then step onto the first newly loaded row.
					cur := p.cursor
					p.items = append(p.items, more...)
					p.rebuildSelectable()
					if cur < len(p.selectable) {
						p.cursor = cur
						if p.cursor < len(p.selectable)-1 {
							p.cursor++
						}
					}
				}
			}
		case rawKeyEnter:
			if len(p.selectable) == 0 {
				return -1, false, nil
			}
			return p.absoluteRow(), true, nil
		case rawKeyEscape, rawKeyCtrlC:
			return -1, false, s.dismissed(key.kind)
		case rawKeyBackspace:
			if p.filter.backspace() {
				p.rebuildSelectable()
			}
		case rawKeyLeft:
			p.filter.move(-1)
		case rawKeyRight:
			p.filter.move(1)
		case rawKeyHome:
			p.filter.moveTo(0)
		case rawKeyEnd:
			p.filter.moveTo(len(p.filter.runes()))
		case rawKeyPaste:
			p.filter.insert(strings.ReplaceAll(key.text, "\n", " "))
			p.rebuildSelectable()
		case rawKeyRune:
			p.filter.insert(string(key.r))
			p.rebuildSelectable()
		case rawKeySpace:
			p.filter.insert(" ")
			p.rebuildSelectable()
		}
		s.renderRichPicker(p, label)
	}
}
