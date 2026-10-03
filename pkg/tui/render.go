// Terminal rendering: the renderer, diff output, and standalone mode.
package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	homepkg "github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
	"github.com/mattn/go-runewidth"
	"github.com/yuin/goldmark"
	mdast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	mdtext "github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"golang.org/x/term"
)

// The renderer measures text in exactly one display-width model, and it is
// declared here rather than inherited from the environment.
//
// Every column the TUI computes — where a line breaks, where a truncation
// lands, which cell the software caret repaints — has to agree with the width
// the terminal actually gives a rune. lipgloss (x/ansi) counts an East Asian
// *Ambiguous* rune as one cell and offers no way to change that, while
// go-runewidth counts it as two whenever the process locale is East Asian
// (LANG=zh_CN.UTF-8). The glyphs the pickers, rules and status rows are built
// from — ◆ │ ─ ● ○ ↑ · … — are exactly that class, so under such a locale the
// two packages disagreed about every line carrying one, by one column per
// glyph. That is what made a picker's filter paint the character before the
// caret a second time underneath it, and what drew the overlay's rule at half
// width. Terminals render those runes in one cell unless explicitly configured
// otherwise, so one cell is the model, for go-runewidth and for every
// dependency that consults its default condition.
func init() {
	runewidth.DefaultCondition.EastAsianWidth = false
}

const (
	cardHorizontalChromeWidth = 4
	contentRightMargin        = 10 // right padding for all content lines
	// viewportRightPadding keeps readable transcript content off the terminal's
	// right edge. Full-bleed elements (the "Worked for" rule) undo it so they
	// span the true terminal width.
	viewportRightPadding = 2
	// sharedBlockBackgroundColor is intentionally lighter than the usual
	// near-black terminal backgrounds. Color 236 (#303030) blended into common
	// dark themes such as Terminal.app's; 238 (#444444) keeps the composer and
	// user-message cards visibly separate without turning them into bright
	// panels.
	sharedBlockBackgroundColor = "238"
	sharedBlockInputStyle      = "\x1b[48;5;" + sharedBlockBackgroundColor + "m"
	sharedBlockTextStyle       = "\x1b[38;5;252;48;5;" + sharedBlockBackgroundColor + "m"
	sharedBlockFooterStyle     = "\x1b[2m"
	sharedBlockReset           = "\x1b[0m"
	// sharedBlockFooterTruncateExtraRoom is the right margin reserved on the
	// composer footer line. Zero so the right-aligned token stats sit flush
	// against the terminal edge (no right padding), matching the full-bleed
	// "Worked for" rule and agent roster.
	sharedBlockFooterTruncateExtraRoom = 0
	composerPromptMarker               = "› "
	composerPromptStyle                = "\x1b[38;5;74;48;5;" + sharedBlockBackgroundColor + "m"
	// thinkingColor is the dim foreground used for streamed reasoning text. Emitted
	// raw (matching the rest of the renderer) so append-only streaming can reopen
	// it after each cursor reposition without rebuilding a lipgloss style.
	thinkingColor = "\x1b[38;5;244m"
)

// sgrPattern strips SGR (color/style) ANSI escape sequences for width measurement.
var sgrPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// selfContainedRows rewrites rendered rows so each one opens the style it is
// meant to be drawn with and leaves the terminal in the default state. The
// viewport's damage painter addresses one canvas row at a time — and the
// region scroll resets attributes before it — so a row that relied on the row
// above it to open a style was repainted in the terminal's default colour
// (the wrapped tool header whose continuation lines came back white), and a
// row that left a style open would bleed into whatever is painted after it.
//
// The tracked state is the ordered list of SGR sequences seen since the last
// full reset. Replaying that list from the default state lands on exactly the
// state sequential drawing produces, so individual attributes (fg/bg/bold/
// faint) never need modelling: stacked openers and disabling codes alike fall
// out. The carry therefore goes at the very start of the row — when the
// transcript painted sequentially, continuation prefixes and indents were
// drawn inside the open style, and normalisation must keep every cell
// rendering exactly as that sequential draw did.
//
// The input slice is never modified; a table with nothing to fix is returned
// as is, and the carry sequence is zero-width so display widths are unchanged.
// Rows without visible content carry no sequences in either direction, while
// the state still walks through them. The scan is a plain byte walk on
// purpose: this runs over every row of every block on every repaint, and a
// per-line regexp there is exactly what the render caches were built to avoid.
func selfContainedRows(rows []string) []string {
	var open []string
	changed := false
	out := make([]string, len(rows))
	for i, row := range rows {
		// The carry replays what is open at the START of the row, so build it
		// before the scan below mutates open.
		var carry string
		for _, seq := range open {
			carry += seq
		}
		visible := false
		for j := 0; j < len(row); {
			if row[j] == 0x1b {
				seq, n := paintEscapeSequence(row[j:])
				if n == 0 {
					j++ // malformed escape: skip the byte, keep scanning
					continue
				}
				if seq[len(seq)-1] == 'm' {
					params := seq[2 : len(seq)-1]
					if params == "" || params == "0" {
						open = open[:0]
					} else {
						dup := false
						for _, s := range open {
							if s == seq {
								dup = true
								break
							}
						}
						if !dup {
							// Applying the same SGR twice is the same as
							// applying it once, so deduplication keeps the
							// replay identical and the list short.
							open = append(open, seq)
						}
					}
				}
				j += n
				continue
			}
			if row[j] != ' ' {
				visible = true
			}
			j++
		}
		out[i] = row
		if !visible {
			continue
		}
		var fixed string
		if carry != "" || len(open) > 0 {
			tail := ""
			if len(open) > 0 {
				tail = "\x1b[0m"
			}
			fixed = carry + row + tail
			if fixed != row {
				out[i] = fixed
				changed = true
			}
		}
	}
	if !changed {
		return rows
	}
	return out
}

// spinnerGlyphs are Braille spinner characters used for the running-tool
// marker. They cycle in order to give a visual rotation effect.
var spinnerGlyphs = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerAnimationTick is how long one Braille spinner glyph, and one step of
// the compact progress bar's sweep, stays on screen.
const spinnerAnimationTick = 100 * time.Millisecond

var assistantMarkdown = goldmark.New(
	goldmark.WithExtensions(extension.Table),
)

type Renderer struct {
	out             io.Writer
	err             io.Writer
	mu              sync.Mutex
	transientActive bool
	// transientStatus renders the live status line for the view it is painted
	// into, rather than holding a finished string. The line describes the run
	// the reader is watching — the conversation's turn, or the subagent whose
	// view is open — and which view that is changes independently of the
	// producer that installed the line, so it can only be resolved at paint
	// time. Nil when no status is live.
	transientStatus func(view string) string
	// transientSources holds one live status line per producer, keyed by source,
	// and transientOrder is which of them wins when several are live at once.
	//
	// One slot was not enough for more than one producer: an MCP startup and a
	// conversation turn are both true at the same time, and with a single slot
	// whichever painted last erased the other, so the line flickered between two
	// truths and each producer's own "finish" call cleared a line that was not
	// its own.
	transientSources map[string]func(view string) string
	// transientOrder is highest priority first: the first live source in this
	// order is the one on screen.
	transientOrder []string
	composerState  ComposerRenderState
	footer         ComposerFooter
	// planMode is whether the conversation is in Plan mode; the footer says so
	// for as long as it lasts.
	planMode bool
	// autoContinueNotice is the warning line under the composer while a
	// conversation stopped by a usage limit waits to continue by itself.
	autoContinueNotice string
	composerTokens     ComposerTokenStats
	fullBodyMode       bool
	cwd                string
	// spinnerPhase is the animation frame for in-flight tools' Braille spinner
	// and for the indeterminate compact progress bar. It is derived from the
	// time since spinnerEpoch rather than counted per paint, so the animation
	// runs at its own steady rate instead of at whatever rate frames, keystrokes
	// and the caret blink happen to force a repaint.
	spinnerPhase int
	spinnerEpoch time.Time
	// Compact has no provider progress percentage, so its TUI progress is an
	// indeterminate moving bar driven by this dedicated repaint ticker. It runs
	// only while a non-final compact frame exists.
	compactAnimationTicker *time.Ticker
	compactAnimationStop   chan struct{}

	// composerSuppressed hides the interactive composer while a modal overlay
	// is active in non-viewport mode. In viewport mode the overlay paints as a
	// bottom-pinned composer block and this flag is not used.
	composerSuppressed bool
	// diffTheme is the terminal-background theme resolved once in run.go before
	// raw mode (the OSC 11 probe must not run inside raw mode). Render paths read
	// this cached value instead of re-probing per frame.
	diffTheme DiffTheme

	// --- virtual viewport (interactive TTY only) ---
	// viewportMode switches the renderer from the legacy append-only stream to a
	// program-owned alt-screen viewport: frames are retained in vm and the whole
	// screen (scrollable transcript + bottom-pinned composer) is repainted on
	// every change. Enabled by EnableViewportMode() from run.go when stdin is a
	// real terminal; left false for non-TTY/pipe runs, which keep the plain
	// append path so redirected output stays clean and tests are unaffected.
	viewportMode bool
	// paintSuppressed defers viewport repaints while >0 (see BeginBatch). Used
	// around bulk frame appends such as session replay, where repainting on
	// every one of thousands of appended frames is O(n^2) and freezes the TUI.
	paintSuppressed int
	// overlayComposer holds modal overlay lines (tool approval, /model picker)
	// rendered as a bottom-pinned block IN the viewport surface — replacing the
	// composer while the overlay is active — instead of taking over the whole
	// screen. The transcript stays visible (and text-selectable) above it, with
	// a separator rule prepended. Set via SetOverlayComposer; cleared by
	// ClearOverlayComposer / EndOverlay. paintViewportLocked swaps this in for
	// buildComposerBlock when non-empty, so the unified mouse selection (press /
	// drag / release / copy) and hover-highlight apply to overlay rows too —
	// identical semantics to the main conversation turn.
	overlayComposer composerBlock
	overlayActive   bool
	// overlaySoftWrap keeps logical overlay rows intact at the producer and
	// wraps them to the current viewport width at paint time. Question overlays
	// use this because their model-provided prompts, labels, and descriptions
	// can be wider than the terminal and must reflow after a resize.
	overlaySoftWrap bool
	// vpOverlayRefs maps every painted overlay row back to the logical line the
	// overlay installed, and vpOverlayFirstRow is the index of the first of
	// those rows that the last paint actually put on screen. Together they let
	// a mouse click on the overlay be handed back to the modal as a position in
	// the lines it wrote, whatever the paint did to them (soft wrapping, the
	// pinned separator, the overlay's own scroll offset).
	vpOverlayRefs     []overlayRowRef
	vpOverlayFirstRow int
	vm                viewModel
	// perAgentVM holds per-subagent transcripts keyed by AgentID (roster key).
	// A frame tagged with an AgentID is retained here and nowhere else, so each
	// subagent view shows its own tool/thinking/assistant history and none of it
	// reaches the conversation.
	perAgentVM map[string]*viewModel
	// subagentTypes is what the footer calls each subagent whose view can be
	// opened: the kind of agent it is. Entries are never removed — a view stays
	// open and readable long after the roster row that described it is gone,
	// which is precisely when the footer is the only thing still naming it.
	subagentTypes map[string]string
	// subagentModels holds the types that run on a model of their own, as
	// agents.definitions[<type>].llm_providers configures them. A type absent
	// from the map runs on the primary agent's model, exactly as the runtime's
	// typed-provider wrapper falls through, so the footer shows this renderer's
	// own ComposerFooter for it.
	subagentModels map[string]ComposerFooter
	// fanoutLineOwners maps each visual line the last renderFanout wrote to the
	// index of the fanout content line it came from (-1 for header and spacing).
	// It is filled by the scratch renderer the viewport uses to turn a frame
	// into block lines, and read back to attribute those lines to agent.
	fanoutLineOwners []int
	// activeView is the AgentID of the currently-displayed view. Empty string
	// means the primary (main agent) view.
	activeView      string
	viewBrowseState map[string]viewportBrowseState
	// vpScrollOffset is the absolute row at the top of the visible window;
	// vpFollow keeps it pinned to the bottom as new frames arrive until the user
	// scrolls up. vpHeight is the body height of the last paint (for wheel/click
	// math). vpLastRender caches the last viewportRender so a mouse click can be
	// hit-tested against the exact rows on screen.
	vpScrollOffset int
	vpFollow       bool
	vpHeight       int
	vpLastRender   viewportRender
	vpLastComposer composerBlock
	vpBodyHeight   int
	// vpPainted is the painter's shadow of the physical screen: entry i holds
	// the exact bytes the last paint put on 1-based screen row i+1, composed
	// but not yet compared. paintViewportLocked composes every row of the new
	// frame the same way and addresses ONLY the rows whose bytes differ, so a
	// row that did not change is never erased and never rewritten.
	//
	// Without this the painter re-emitted "\x1b[<row>;1H\x1b[2K<content>" for
	// every row of every frame. Repaints are frequent — one per streamed delta,
	// one per keystroke, plus the 200ms status, 500ms caret-blink and 120ms
	// compact tickers — and a terminal presents whenever it likes inside that
	// byte stream, so it could sample a row after the erase and before the
	// rewrite. Static rows (the composer's rules, transcript text already on
	// screen) therefore blanked and reappeared many times a second: the
	// reported flicker. Damage tracking removes the cause rather than hiding
	// it — those rows now receive no bytes at all.
	//
	// The shadow is only valid while nothing else writes to the screen, so it
	// is dropped whenever the geometry changes or the alt screen is entered,
	// left, or resized.
	vpPainted       []string
	vpPaintedWidth  int
	vpPaintedHeight int
	// vpCursor is the software caret currently on screen, tracked separately
	// from vpPainted because the caret owns a single cell of a row rather than
	// the row: blinking it or moving it must not disturb the line being typed.
	vpCursor            paintedCursor
	overlayScrollOffset int // rows scrolled within the overlay composer area
	// overlayPanel is the slash panel the overlay area shows, laid out at
	// every paint; nil while a modal's own lines own the area. Its scroll is
	// overlayScrollOffset, counted in body rows.
	overlayPanel *slashPanel
	// overlayFocusRow is the overlay panel's focus line (-1 for none) and
	// overlayFocusPending that it moved since the last paint: the paint then
	// scrolls it into view once, the way a modal's cursor row is kept visible.
	overlayFocusRow     int
	overlayFocusPending bool
	// slashMenuTop is the first body row the composer's slash menu shows.
	slashMenuTop int
	// overlayNote is a line the renderer adds under any overlay: approvals
	// queued behind whatever owns the input right now.
	overlayNote string
	// composerScrollOffset is the vertical scroll position of a normal composer
	// whose rendered height exceeds the allocated composer area. It is managed
	// by paintViewportLocked and kept at zero when the composer fits on screen.
	composerScrollOffset int
	// vpHoverRow is the 0-based screen row under the mouse cursor, set by
	// ViewportHover on inputEventMouseMove. The next paint applies a subtle
	// background highlight to clickable rows under the cursor. -1 means no
	// hover (e.g. cursor left the window or first move not yet received).
	vpHoverRow int
	// vpJumpToBottomPressed tracks a left-button press on the floating
	// "Jump to bottom" control until its matching release. Keeping this
	// separate from transcript selection prevents a button click from briefly
	// selecting the content row underneath the control.
	vpJumpToBottomPressed bool
	// vpNewMessages counts transcript messages that arrived in the visible view
	// while the user was scrolled away from the bottom, i.e. the ones they have
	// not seen. The floating control announces them ("1 new message (ctrl+End)
	// ↓"); reaching the bottom by any route clears the count.
	vpNewMessages int

	// Hardware cursor visibility tracking. DECTCEM show (\x1b[?25h) restarts the
	// terminal's blink timer in iTerm2/Terminal.app, so re-issuing it on every
	// repaint (which happens many times per second while the agent is streaming)
	// keeps the caret frozen in its "on" phase instead of blinking. To preserve
	// the native blink we only emit ?25h/?25l on actual visibility transitions;
	// when the caret stays visible at the same cell across frames we emit just
	// the cursor-position sequence (\x1b[<row>;<col>H), which does NOT reset the
	// blink phase. cursorScreenRow/Col are 1-based screen coords of the last
	// emitted caret position; 0 means "none / unknown".
	cursorShown     bool
	cursorScreenRow int
	cursorScreenCol int

	// Software cursor: when enabled, the hardware cursor is hidden (?25l)
	// and a reverse-video block is painted at the composer caret position
	// on each frame. macOS Terminal.app and iTerm2 restart the hardware
	// blink timer on continuous output, so during streaming the hardware
	// caret freezes solid in its "on" phase and never blinks. The software
	// cursor bypasses the terminal blink mechanism entirely;
	// cursorBlinkVisible is toggled by a 500ms ticker
	// (startCursorBlinkLocked) so the caret blinks at a steady rate
	// regardless of terminal output activity. EnableSoftwareCursor (called
	// from run.go after EnableViewportMode) activates this mode; existing
	// tests that call EnableViewportMode directly keep the hardware cursor.
	softwareCursor     bool
	cursorBlinkVisible bool
	cursorBlinkTicker  *time.Ticker
	cursorBlinkStop    chan struct{}

	// Text selection state. Anchors are stored in ABSOLUTE transcript-row
	// coordinates (not screen rows) so a selection stays pinned to its content
	// as the viewport scrolls. Composer rows are encoded in a separate sentinel
	// space (>= composerRowBase) since the composer is bottom-pinned and does
	// not scroll with the transcript.
	selectStartCol int
	selectStartRow int
	selectEndCol   int
	selectEndRow   int
	selectDidDrag  bool
	// selectDragging is true only while the left mouse button is held during a
	// selection (press -> release). It is the "active drag" indicator and drives
	// ONLY whether the hardware input cursor is hidden - decoupled from the
	// selection highlight so a released (persisted) selection keeps its
	// background while the composer cursor stays visible, matching a native
	// terminal. Reset to false on release and in selectClearLocked.
	selectDragging bool
	lastClickTime  time.Time
	lastClickCol   int
	lastClickRow   int
	// clickCount tracks rapid same-cell presses to distinguish single (1),
	// double (2, select word) and triple (3, select line) clicks. It cycles
	// 1->2->3->1 when consecutive presses land on the same cell within
	// doubleClickThreshold; any other press resets it to 1.
	clickCount int

	// Auto-scroll while drag-selecting past a viewport edge. autoScrollDir is
	// -1 (up), +1 (down), or 0 (idle); autoScrollStop signals the running
	// ticker goroutine to exit; autoScrollCol is the drag column the ticker
	// uses to extend the selection edge each step.
	autoScrollDir  int
	autoScrollStop chan struct{}
	autoScrollCol  int

	// Clipboard notification shown in the footer right side.
	clipNotification string
	clipNotifyTimer  *time.Timer

	// clipboardImage is true while the system clipboard holds an image, which
	// the composer advertises with a right-aligned hint above its top rule.
	clipboardImage bool
}

type viewportBrowseState struct {
	ScrollOffset int
	Follow       bool
	NewMessages  int
}

// RendererBrowseState is browsing state, not conversation history. It can be
// saved independently and restored only after the transcript VMs have been
// rebuilt from durable events.
type RendererBrowseState struct {
	ActiveView string                             `json:"active_view,omitempty"`
	Views      map[string]RendererViewBrowseState `json:"views,omitempty"`
	Expanded   map[string][]int                   `json:"expanded,omitempty"`
}

type RendererViewBrowseState struct {
	ScrollOffset int  `json:"scroll_offset"`
	Follow       bool `json:"follow"`
	NewMessages  int  `json:"new_messages,omitempty"`
}

// Maximum rendered rows before the interactive viewport folds a retained block
// behind a clickable disclosure header.
const toolOutputMaxLines = 3

type StartupInfo struct {
	Version   string
	Directory string
}

type ComposerFooter struct {
	Model           string
	ReasoningEffort string
	Directory       string
}

// ComposerTokenStats holds the live token figures shown on the right side of
// the composer footer.
type ComposerTokenStats struct {
	Active        bool
	InputTokens   int
	OutputTokens  int
	PercentLeft   int
	ContextWindow int
}

func NewRenderer(out io.Writer, err io.Writer) *Renderer {
	if out == nil {
		out = io.Discard
	}
	if err == nil {
		err = out
	}
	cwd, _ := os.Getwd()
	return &Renderer{out: out, err: err, cwd: cwd, vpHoverRow: -1, selectStartRow: -1, selectEndRow: -1, selectStartCol: -1, selectEndCol: -1, overlayFocusRow: -1}
}

// OutputWriter returns a writer that puts status text on screen through the
// renderer. Interactive setup flows use it for text the user has to act on
// (notably the ChatGPT OAuth URL), which must survive long enough to be read.
//
// In viewport mode the renderer owns the alt screen and repaints it from its
// retained blocks, so the text is retained as a block rather than written at
// the cursor: a direct write lands wherever the last paint left the cursor and
// is erased by the next one, which for a login URL means it can disappear
// before the user has finished with it. Outside viewport mode there is no
// block model, so the bytes go straight out under the renderer's lock.
func (r *Renderer) OutputWriter() io.Writer {
	if r == nil {
		return io.Discard
	}
	return rendererOutputWriter{renderer: r}
}

type rendererOutputWriter struct {
	renderer *Renderer
}

func (w rendererOutputWriter) Write(p []byte) (int, error) {
	if w.renderer == nil {
		return len(p), nil
	}
	return w.renderer.writeStatusText(p)
}

func (r *Renderer) writeStatusText(p []byte) (int, error) {
	if r == nil {
		return len(p), nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.viewportMode {
		// Pre-styled text retained verbatim, exactly as the startup banner is.
		r.seedRawViewportLocked(string(p))
		return len(p), nil
	}
	if r.out == nil {
		return len(p), nil
	}
	return r.out.Write(p)
}

// Mu returns the renderer's mutex for external callers that need to
// serialize output with the renderer (e.g. rawSelector).
func (r *Renderer) Mu() *sync.Mutex {
	if r == nil {
		return nil
	}
	return &r.mu
}

// WithDiffTheme caches the terminal-background theme resolved before raw mode.
func (r *Renderer) WithDiffTheme(t DiffTheme) *Renderer {
	if r == nil {
		return nil
	}
	r.diffTheme = t
	return r
}

// WithWorkingDir caches the TUI launch cwd for path-relative rendering.
func (r *Renderer) WithWorkingDir(cwd string) *Renderer {
	if r == nil {
		return nil
	}
	r.cwd = strings.TrimSpace(cwd)
	return r
}

// DiffTheme returns the cached terminal-background theme.
func (r *Renderer) DiffTheme() DiffTheme {
	if r == nil {
		return DiffThemeUnknown
	}
	return r.diffTheme
}

func (r *Renderer) Banner(info StartupInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.viewportMode {
		// The card is retained as one block whose render branch lays it out
		// for the current width, so dragging the terminal edge reflows it
		// without a restart.
		r.appendFrameViewportLocked(Frame{Kind: FrameBannerInfo, Title: info.Version, Content: info.Directory, Final: true})
		return
	}
	RenderForebrainBanner(r.out, info)
}

// StartupHint is retained as a compatibility hook. The hint text is now
// rendered inside Banner; this method intentionally does nothing.
func (r *Renderer) StartupHint() {}

// StartupInfo is retained as a compatibility hook. Startup chrome (version,
// directory, shortcut hints) is now rendered as part of Banner; callers that
// still pass info here are no-ops.
func (r *Renderer) StartupInfo(StartupInfo) {}

// seedRawViewportLocked retains pre-styled text as a FrameRaw block and repaints,
// so startup chrome (banner/info/hint) lives inside the alt-screen transcript
// rather than on the hidden normal buffer. Caller holds r.mu.
func (r *Renderer) seedRawViewportLocked(text string) {
	text = strings.TrimRight(text, "\r\n")
	if strings.TrimSpace(sgrPattern.ReplaceAllString(text, "")) == "" {
		return
	}
	r.appendFrameViewportLocked(Frame{Kind: FrameRaw, Content: text, Final: true})
}

func (r *Renderer) Newline() {
	// Viewport mode owns the screen; retained for run.go caller compatibility.
}

func (r *Renderer) RenderFrame(f Frame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.appendFrameViewportLocked(f)
}

// BeginBatch defers viewport repaints until the matching EndBatch. Wrap it
// around bulk frame appends (e.g. session replay of thousands of turns) so each
// append does not trigger a full O(blocks) paint - replay would otherwise be
// O(n^2) repaints and freeze the TUI. Nestable via a counter; the final
// EndBatch (count returns to 0) paints once. No-op when the renderer is nil or
// not in viewport mode (paintViewportLocked itself is a no-op then).
func (r *Renderer) BeginBatch() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.paintSuppressed++
	r.mu.Unlock()
}

// EndBatch ends a batch started by BeginBatch and, when the outermost batch
// closes, performs the single deferred repaint.
func (r *Renderer) EndBatch() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paintSuppressed > 0 {
		r.paintSuppressed--
	}
	if r.paintSuppressed == 0 {
		r.paintViewportLocked()
	}
}

func termWidthOrDefault() int {
	w, _, err := termSizeWithFallback()
	if err != nil || w <= 0 {
		return 80
	}
	return w
}

func termHeightOrDefault() int {
	_, h, err := termSizeWithFallback()
	if err != nil || h <= 0 {
		return 24
	}
	return h
}

func (r *Renderer) PrintError(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.appendFrameViewportLocked(Frame{Kind: FrameError, Title: "error", Content: llm.ExplainError(err), Final: true})
}

// PrintApprovalConfirmation writes the finished "✔ You approved …" line into
// the scrollback above the interactive composer, capped at maxLines visual
// lines (a truncated final line ends in an ellipsis and is intentionally not
// expandable). It clears the composer (and any transient status) first, so the
// line can never paint over it, then lets the composer repaint below via the
// next status tick / input render; relying on suppression timing alone would
// race the working-status redraw.
//
// agentID names the subagent whose tool call was authorised, and is empty for
// the conversation's own approvals. A subagent's confirmation is retained in
// that subagent's view, immediately above the call it authorises, because that
// is the only view the request itself was ever put in.
func (r *Renderer) PrintApprovalConfirmation(agentID, text string, maxLines int) {
	if r == nil {
		return
	}
	text = strings.TrimRight(text, "\r\n")
	if strings.TrimSpace(text) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.appendFrameViewportLocked(Frame{
		Kind:                 FrameStatus,
		Content:              strings.TrimSpace(sgrPattern.ReplaceAllString(text, "")),
		MaxDisplayLines:      maxLines,
		InsertBeforeLastTool: true,
		AgentID:              strings.TrimSpace(agentID),
		Final:                true,
	})
}

// FinalizePendingTools marks any in-flight (running or awaiting-approval) tool
// blocks as canceled. It is called when a run is aborted (e.g. the user
// cancels a tool approval or interrupts the run with Ctrl+C) so the transcript
// reflects the final state instead of leaving a stale "Running" frame that
// never completes. Only affects viewport mode; in non-viewport mode the old
// frame is already printed and cannot be revised.
func (r *Renderer) FinalizePendingTools() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := r.finalizePendingToolsVM(&r.vm)
	for _, vm := range r.perAgentVM {
		if r.finalizePendingToolsVM(vm) {
			changed = true
		}
	}
	if changed {
		r.paintViewportLocked()
	}
}

func (r *Renderer) finalizePendingToolsVM(vm *viewModel) bool {
	changed := false
	for _, b := range vm.blocks {
		if b.frame.Kind == FrameTool && toolStatusPending(b.frame) {
			b.frame.ToolMeta.Status = "canceled"
			b.frame.Final = true
			b.cache.valid = false
			changed = true
		}
	}
	return changed
}

// Transient status sources, highest priority first. A readiness wait is the
// most urgent thing on screen because it is what the reader is waiting on; a
// migration outranks the ordinary working line because it is a foreground
// operation the user started and cannot see otherwise.
const (
	transientSourceMCP     = "mcp"
	transientSourceMigrate = "migrate"
	transientSourceWorking = "working"
)

var transientSourcePriority = []string{
	transientSourceMCP,
	transientSourceMigrate,
	transientSourceWorking,
}

// RenderTransientStatus installs a fixed status line for one source: a producer
// that paints text of its own rather than a formatter evaluated per frame.
func (r *Renderer) RenderTransientStatus(source, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	r.installTransientStatus(source, func(string) string { return text })
}

// installTransientStatus makes render the source's live status line, and
// repaints so the new line is on screen immediately. Only that source is
// replaced: a line another producer installed stays live underneath.
func (r *Renderer) installTransientStatus(source string, render func(view string) string) {
	if r == nil || render == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.transientSources == nil {
		r.transientSources = make(map[string]func(view string) string)
	}
	r.transientSources[source] = render
	r.transientActive = true
	r.syncTransientLocked()
	r.paintViewportLocked()
}

// transientStatusTextLocked renders the live status line for the view on
// screen, from the highest-priority source that is live. Caller holds r.mu.
func (r *Renderer) transientStatusTextLocked() string {
	if !r.transientActive {
		return ""
	}
	if render := r.transientRenderLocked(); render != nil {
		return render(r.activeView)
	}
	return ""
}

// transientRenderLocked picks the source whose line is on screen. Caller holds
// r.mu.
func (r *Renderer) transientRenderLocked() func(view string) string {
	for _, source := range transientSourcePriority {
		if render, ok := r.transientSources[source]; ok && render != nil {
			return render
		}
	}
	// A source this file does not know about still gets to show: an unknown
	// producer must not be silently invisible.
	for _, render := range r.transientSources {
		if render != nil {
			return render
		}
	}
	return nil
}

// syncTransientLocked mirrors the winning source into the single line the paint
// path reads. Caller holds r.mu.
func (r *Renderer) syncTransientLocked() {
	r.transientStatus = r.transientRenderLocked()
	if r.transientStatus == nil {
		r.transientActive = false
	}
}

// FinishTransientStatus ends one source's status line. It ends only its own: a
// producer that finishes while another is still live leaves that one on screen
// instead of blanking the line for both of them.
func (r *Renderer) FinishTransientStatus(source string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishTransientLocked(source)
}

// FinishAllTransientStatus ends every source's line. For a session switch or a
// shutdown, where nothing that was on screen still applies.
func (r *Renderer) FinishAllTransientStatus() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transientSources = nil
	r.finishTransientLocked("")
}

// TransientStatusSourceLive reports whether one source currently has a line.
func (r *Renderer) TransientStatusSourceLive(source string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	render, ok := r.transientSources[source]
	return ok && render != nil
}

// StartTimedTransientStatus installs a source's live status line and drives its
// clock, returning the call that stops it. The formatter is evaluated on every
// paint — not once per tick — with the time elapsed since the line started, the
// spinner tick, and the view being painted, so the line is current and belongs
// to the transcript on screen no matter what triggered the repaint.
func (r *Renderer) StartTimedTransientStatus(source string, formatter transientStatusFormatter) func(finalText string) {
	if r == nil || formatter == nil {
		return func(string) {}
	}
	stop := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		started := time.Now()
		paint := func(tick int) {
			r.installTransientStatus(source, func(view string) string {
				return formatter(time.Since(started), tick, view)
			})
		}
		tick := 0
		paint(tick)
		for {
			select {
			case finalText := <-stop:
				if text := strings.TrimSpace(finalText); text != "" {
					r.RenderTransientStatus(source, text)
				}
				return
			case <-ticker.C:
				tick++
				paint(tick)
			}
		}
	}()
	return func(finalText string) {
		select {
		case stop <- finalText:
		default:
		}
		<-done
	}
}

// transientStatusFormatter renders the live status line: the time elapsed since
// the line started, the spinner tick, and the view it is being painted into —
// empty for the conversation, a subagent's id inside that subagent's view.
type transientStatusFormatter func(elapsed time.Duration, tick int, view string) string

// finishTransientLocked drops one source's line (all of them when source is
// empty) and repaints when the line on screen changed. Caller holds r.mu.
func (r *Renderer) finishTransientLocked(source string) {
	if !r.transientActive && r.transientSources == nil {
		return
	}
	before := r.transientStatus
	if source == "" {
		r.transientSources = nil
	} else {
		delete(r.transientSources, source)
	}
	r.syncTransientLocked()
	if !r.viewportMode {
		return
	}
	if (before == nil) != (r.transientStatus == nil) {
		r.paintViewportLocked()
	}
}

func (r *Renderer) renderAssistant(f Frame) {
	contentWidth := termWidthOrDefault() - lipgloss.Width("  ") - 1
	if contentWidth < 1 {
		contentWidth = 1
	}
	text := renderAssistantMarkdownWithWidth(normalizeAssistantText(f.Content), contentWidth, r.diffTheme)
	if text == "" {
		return
	}
	bullet := lipgloss.NewStyle().Foreground(lipgloss.Color("45")).Bold(true).Render("●")
	_, _ = fmt.Fprintln(r.out, prefixLines(text, bullet+" ", "  "))
	_, _ = fmt.Fprintln(r.out)
}

func renderAssistantMarkdownWithWidth(raw string, maxWidth int, theme DiffTheme) string {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, softWrapSeparator, ""))
	if raw == "" {
		return ""
	}
	source := []byte(raw)
	doc := assistantMarkdown.Parser().Parse(mdtext.NewReader(source))
	rendered := trimMarkdownBlockBoundary(renderMarkdownBlocks(doc, source, maxWidth, theme))
	if strings.TrimSpace(rendered) == "" {
		return raw
	}
	return strings.ReplaceAll(rendered, softWrapSeparator, "\n")
}

// markedSpanColor draws a marked span. It is deliberately neither the code
// colour nor the accent the tool header uses, so a mark cannot be mistaken for
// either the quoted text's own code spans or the card's chrome.
const markedSpanColor = "114"

func insideBlockquote(node mdast.Node) bool {
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		if _, quoted := parent.(*mdast.Blockquote); quoted {
			return true
		}
	}
	return false
}

func trimMarkdownBlockBoundary(rendered string) string {
	return strings.Trim(rendered, "\r\n")
}

func renderMarkdownBlocks(parent mdast.Node, source []byte, maxWidth int, theme DiffTheme) string {
	blocks := make([]string, 0, parent.ChildCount())
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		block := trimMarkdownBlockBoundary(renderMarkdownBlock(child, source, maxWidth, theme))
		if strings.TrimSpace(block) == "" {
			continue
		}
		blocks = append(blocks, block)
	}
	return strings.Join(blocks, "\n\n")
}

func renderMarkdownBlock(node mdast.Node, source []byte, maxWidth int, theme DiffTheme) string {
	switch n := node.(type) {
	case *mdast.Heading:
		text := strings.TrimSpace(renderMarkdownInlineChildren(n, source))
		if text == "" {
			return ""
		}
		return lipgloss.NewStyle().Bold(true).Render(wrapMarkdownText(text, maxWidth))
	case *mdast.Paragraph:
		return wrapMarkdownText(strings.TrimSpace(renderMarkdownInlineChildren(n, source)), maxWidth)
	case *mdast.TextBlock:
		return wrapMarkdownText(strings.TrimSpace(renderMarkdownInlineChildren(n, source)), maxWidth)
	case *mdast.Blockquote:
		// "> " marks a line of quoted text. The rows a long quoted line spills
		// onto are the terminal's width showing, not lines of the quote, so
		// they align under the marker instead of each claiming one: a reader
		// counts the quoted lines by counting the markers, and a wrapped line
		// that took a second marker read as a second line of the file.
		return prefixMarkdownRows(renderMarkdownBlocks(n, source, markdownChildWidth(maxWidth, "> "), theme), "> ", "> ", "  ")
	case *mdast.List:
		return renderMarkdownList(n, source, maxWidth, theme)
	case *extast.Table:
		return renderMarkdownTable(n, source, maxWidth)
	case *mdast.FencedCodeBlock:
		return renderMarkdownCodeBlock(codeBlockLanguage(n, source), blockLinesText(n.Lines(), source), maxWidth, theme)
	case *mdast.CodeBlock:
		return renderMarkdownCodeBlock("", blockLinesText(n.Lines(), source), maxWidth, theme)
	case *mdast.ThematicBreak:
		return "----------"
	default:
		if node.Type() == mdast.TypeInline {
			return strings.TrimSpace(renderMarkdownInline(node, source))
		}
		if node.HasChildren() {
			return renderMarkdownBlocks(node, source, maxWidth, theme)
		}
		if lines := node.Lines(); lines != nil {
			return strings.TrimSpace(blockLinesText(lines, source))
		}
		return strings.TrimSpace(string(node.Text(source)))
	}
}

func renderMarkdownList(list *mdast.List, source []byte, maxWidth int, theme DiffTheme) string {
	items := make([]string, 0, list.ChildCount())
	index := list.Start
	if index <= 0 {
		index = 1
	}
	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		prefix := "- "
		if list.IsOrdered() {
			prefix = fmt.Sprintf("%d. ", index)
			index++
		}
		content := renderMarkdownListItem(item, prefix, source, maxWidth, theme)
		if strings.TrimSpace(content) == "" {
			continue
		}
		items = append(items, content)
	}
	return strings.Join(items, "\n")
}

func renderMarkdownListItem(item mdast.Node, prefix string, source []byte, maxWidth int, theme DiffTheme) string {
	blocks := make([]string, 0, item.ChildCount())
	childWidth := markdownChildWidth(maxWidth, prefix)
	for child := item.FirstChild(); child != nil; child = child.NextSibling() {
		block := trimMarkdownBlockBoundary(renderMarkdownBlock(child, source, childWidth, theme))
		if strings.TrimSpace(block) == "" {
			continue
		}
		blocks = append(blocks, block)
	}
	if len(blocks) == 0 {
		return ""
	}
	first := prefixLines(blocks[0], prefix, strings.Repeat(" ", len(prefix)))
	if len(blocks) == 1 {
		return first
	}
	rest := make([]string, 0, len(blocks)-1)
	indent := strings.Repeat(" ", len(prefix))
	for _, block := range blocks[1:] {
		rest = append(rest, prefixLines(block, indent, indent))
	}
	return first + "\n" + strings.Join(rest, "\n")
}

// softWrapSeparator joins the rows one line was soft-wrapped onto, where a real
// line break joins two lines.
//
// A wrap is the terminal's doing, not the text's, and a marker that belongs to
// a line — a blockquote's "> ", a list item's bullet — must not be repeated on
// the rows a long line happened to spill onto. Nothing downstream can tell the
// two apart once both are a newline, so the distinction is carried until the
// markers are applied and resolved into newlines on the way out, by
// renderAssistantMarkdownWithWidth. The source is stripped of the separator
// before parsing, so a rune in the text can never be mistaken for one.
const softWrapSeparator = "\x00"

// wrapMarkdownText soft-wraps rendered inline text to maxWidth so it never runs
// past the content padding into the terminal's own autowrap. maxWidth <= 0 means
// "no cap" (width unknown). Each source line is wrapped independently so explicit
// hard breaks are preserved.
func wrapMarkdownText(text string, maxWidth int) string {
	if maxWidth <= 0 || text == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		wrapped = append(wrapped, strings.Join(wrapCardLine(line, maxWidth), softWrapSeparator))
	}
	return strings.Join(wrapped, "\n")
}

func renderMarkdownCodeBlock(lang string, body string, maxWidth int, theme DiffTheme) string {
	body = strings.TrimRight(body, "\n")
	lang = strings.TrimSpace(lang)
	if strings.TrimSpace(body) == "" {
		if lang == "" {
			return ""
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render("[" + lang + "]")
	}

	lines := strings.Split(body, "\n")

	// Resolve syntax highlighting lexer and style.
	lexer := resolveCodeBlockLexer(lang, theme)
	st := resolveCodeStyle(theme)

	// No line-number gutter here. A fenced block in a message is code the user
	// is meant to copy and run, and a gutter travels with the selection and has
	// to be stripped by hand. Numbers stay where they identify a position the
	// reader needs: diff rows and read_file output.
	codeMaxWidth := maxWidth - lipgloss.Width("    ")
	if codeMaxWidth < 10 {
		codeMaxWidth = 10
	}

	parts := make([]string, 0, len(lines)+1)

	// Language label.
	if lang != "" {
		label := lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render("[" + lang + "]")
		parts = append(parts, label)
	}

	fg := plainSyntaxBrightnessFor(theme)
	for _, line := range lines {
		// Wrap raw text before highlighting so width measurement is
		// ANSI-free (wrapCardLine is ANSI-aware, but wrapping first is
		// consistent with formatHighlightedToolOutput).
		wrapped := wrapCardLine(line, codeMaxWidth)
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		for _, segment := range wrapped {
			highlighted := segment
			if lexer != nil && st != nil && strings.TrimSpace(segment) != "" {
				if h := highlightCodeLine(segment, lexer, st, fg); h != "" {
					highlighted = h
				}
			}
			parts = append(parts, ensureTrailingANSIReset("    "+highlighted))
		}
	}

	return strings.Join(parts, "\n")
}

func ensureTrailingANSIReset(line string) string {
	if !strings.Contains(line, "\x1b[") || strings.HasSuffix(line, sharedBlockReset) {
		return line
	}
	return line + sharedBlockReset
}

// resolveCodeBlockLexer returns a chroma lexer for a markdown fenced-code-block
// language tag, or nil when colour is disabled or the language is unrecognized.
func resolveCodeBlockLexer(lang string, theme DiffTheme) chroma.Lexer {
	if noColorActive() {
		return nil
	}
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return nil
	}
	// Normalize common aliases that differ between markdown fence tags and
	// chroma's registered lexer names.
	switch strings.ToLower(lang) {
	case "js":
		lang = "javascript"
	case "ts":
		lang = "typescript"
	case "jsx":
		lang = "javascript"
	case "tsx":
		lang = "typescript"
	case "sh", "zsh":
		lang = "bash"
	case "yml":
		lang = "yaml"
	case "dockerfile", "docker":
		lang = "docker"
	case "make", "makefile":
		lang = "makefile"
	case "rs":
		lang = "rust"
	case "cpp", "c++":
		lang = "cpp"
	case "csharp", "c#":
		lang = "csharp"
	case "fsharp", "fs":
		lang = "fsharp"
	case "rb":
		lang = "ruby"
	case "py":
		lang = "python"
	case "md", "markdown":
		lang = "markdown"
	case "txt", "text", "plain", "plaintext":
		return nil // no highlighting for plain text
	}
	lexer := lexers.Get(lang)
	if lexer == nil {
		// Fall back: treat the language tag as a file extension.
		lexer = lexers.Match("." + lang)
	}
	if lexer == nil {
		return nil
	}
	return chroma.Coalesce(lexer)
}

func renderMarkdownTable(table *extast.Table, source []byte, maxWidth int) string {
	if table == nil {
		return ""
	}
	var header []string
	rows := make([][]string, 0, table.ChildCount())
	for child := table.FirstChild(); child != nil; child = child.NextSibling() {
		switch n := child.(type) {
		case *extast.TableHeader:
			header = renderMarkdownTableCells(n, source)
		case *extast.TableRow:
			row := renderMarkdownTableCells(n, source)
			if len(row) > 0 {
				rows = append(rows, row)
			}
		}
	}
	if len(header) == 0 && len(rows) == 0 {
		return ""
	}
	return formatMarkdownTable(header, rows, table.Alignments, maxWidth)
}

func renderMarkdownTableCells(row mdast.Node, source []byte) []string {
	cells := make([]string, 0, row.ChildCount())
	for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
		text := strings.TrimSpace(renderMarkdownInlineChildren(cell, source))
		cells = append(cells, text)
	}
	return cells
}

func formatMarkdownTable(header []string, rows [][]string, aligns []extast.Alignment, maxWidth int) string {
	colCount := len(header)
	for _, row := range rows {
		if len(row) > colCount {
			colCount = len(row)
		}
	}
	if colCount == 0 {
		return ""
	}
	widths := make([]int, colCount)
	for i := 0; i < colCount; i++ {
		if i < len(header) {
			widths[i] = markdownCellWidth(header[i])
		}
		for _, row := range rows {
			if i < len(row) {
				if w := markdownCellWidth(row[i]); w > widths[i] {
					widths[i] = w
				}
			}
		}
		if widths[i] == 0 {
			widths[i] = 1
		}
	}
	if maxWidth > 0 && markdownTableWidth(widths) > maxWidth {
		widths = fitMarkdownTableWidths(widths, maxWidth)
	}
	lines := make([]string, 0, len(rows)+3)
	if len(header) > 0 {
		lines = append(lines, formatMarkdownTableRows(header, widths, aligns)...)
		lines = append(lines, formatMarkdownTableDivider(widths, aligns))
	}
	for _, row := range rows {
		lines = append(lines, formatMarkdownTableRows(row, widths, aligns)...)
	}
	return strings.Join(lines, "\n")
}

func formatMarkdownTableRows(cells []string, widths []int, aligns []extast.Alignment) []string {
	wrapped := make([][]string, len(widths))
	rowCount := 1
	for i := range widths {
		value := ""
		if i < len(cells) {
			value = cells[i]
		}
		wrapped[i] = wrapMarkdownTableCell(value, widths[i])
		if len(wrapped[i]) > rowCount {
			rowCount = len(wrapped[i])
		}
	}
	rows := make([]string, 0, rowCount)
	for rowIdx := 0; rowIdx < rowCount; rowIdx++ {
		parts := make([]string, len(widths))
		for i := range widths {
			value := ""
			if rowIdx < len(wrapped[i]) {
				value = wrapped[i][rowIdx]
			}
			parts[i] = " " + alignMarkdownTableCell(value, widths[i], tableAlignmentAt(aligns, i)) + " "
		}
		rows = append(rows, "|"+strings.Join(parts, "|")+"|")
	}
	return rows
}

func formatMarkdownTableDivider(widths []int, aligns []extast.Alignment) string {
	parts := make([]string, len(widths))
	for i, width := range widths {
		fill := strings.Repeat("-", width+2)
		switch tableAlignmentAt(aligns, i) {
		case extast.AlignLeft:
			if len(fill) >= 2 {
				fill = ":" + fill[1:]
			}
		case extast.AlignRight:
			if len(fill) >= 2 {
				fill = fill[:len(fill)-1] + ":"
			}
		case extast.AlignCenter:
			if len(fill) >= 2 {
				fill = ":" + fill[1:len(fill)-1] + ":"
			}
		}
		parts[i] = fill
	}
	return "|" + strings.Join(parts, "|") + "|"
}

func alignMarkdownTableCell(value string, width int, align extast.Alignment) string {
	cellWidth := markdownCellWidth(value)
	if cellWidth >= width {
		return value
	}
	padding := width - cellWidth
	switch align {
	case extast.AlignRight:
		return strings.Repeat(" ", padding) + value
	case extast.AlignCenter:
		left := padding / 2
		right := padding - left
		return strings.Repeat(" ", left) + value + strings.Repeat(" ", right)
	default:
		return value + strings.Repeat(" ", padding)
	}
}

func wrapMarkdownTableCell(value string, width int) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return []string{""}
	}
	if width < 1 {
		width = 1
	}
	return wrapCardLine(value, width)
}

func markdownCellWidth(value string) int {
	return runewidth.StringWidth(sgrPattern.ReplaceAllString(value, ""))
}

func markdownTableWidth(widths []int) int {
	if len(widths) == 0 {
		return 0
	}
	total := 1
	for _, width := range widths {
		total += width + 3
	}
	return total
}

func fitMarkdownTableWidths(widths []int, maxWidth int) []int {
	out := append([]int(nil), widths...)
	if len(out) == 0 || maxWidth <= 0 {
		return out
	}
	budget := maxWidth - (3*len(out) + 1)
	if budget < len(out) {
		budget = len(out)
	}
	for markdownWidthsSum(out) > budget {
		maxWidth := 1
		secondMax := 1
		countMax := 0
		for _, width := range out {
			switch {
			case width > maxWidth:
				secondMax = maxWidth
				maxWidth = width
				countMax = 1
			case width == maxWidth:
				countMax++
			case width > secondMax:
				secondMax = width
			}
		}
		if maxWidth <= 1 || countMax == 0 {
			break
		}
		needed := markdownWidthsSum(out) - budget
		step := (needed + countMax - 1) / countMax
		if capStep := maxWidth - maxInt(secondMax, 1); step > capStep {
			step = capStep
		}
		if step < 1 {
			step = 1
		}
		for i, width := range out {
			if width != maxWidth {
				continue
			}
			out[i] = maxInt(1, width-step)
		}
	}
	return out
}

func markdownWidthsSum(widths []int) int {
	total := 0
	for _, width := range widths {
		total += width
	}
	return total
}

func markdownChildWidth(maxWidth int, prefix string) int {
	if maxWidth <= 0 {
		return 0
	}
	width := maxWidth - lipgloss.Width(prefix)
	if width < 1 {
		return 1
	}
	return width
}

func tableAlignmentAt(aligns []extast.Alignment, idx int) extast.Alignment {
	if idx < 0 || idx >= len(aligns) {
		return extast.AlignNone
	}
	return aligns[idx]
}

func codeBlockLanguage(block *mdast.FencedCodeBlock, source []byte) string {
	if block == nil {
		return ""
	}
	return strings.TrimSpace(string(block.Language(source)))
}

func blockLinesText(lines *mdtext.Segments, source []byte) string {
	if lines == nil || lines.Len() == 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < lines.Len(); i++ {
		segment := lines.At(i)
		b.Write(segment.Value(source))
	}
	return strings.ReplaceAll(b.String(), "\r", "")
}

func renderMarkdownInlineChildren(node mdast.Node, source []byte) string {
	var b strings.Builder
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		b.WriteString(renderMarkdownInline(child, source))
	}
	return collapseMarkdownWhitespace(b.String())
}

func renderMarkdownInline(node mdast.Node, source []byte) string {
	switch n := node.(type) {
	case *mdast.Text:
		text := markdownTextValue(n.Segment.Value(source), n.IsRaw())
		switch {
		case n.HardLineBreak():
			return text + "\n"
		case n.SoftLineBreak():
			return text + " "
		default:
			return text
		}
	case *mdast.String:
		return markdownTextValue(n.Value, n.IsRaw())
	case *mdast.CodeSpan:
		text := collapseMarkdownWhitespace(renderMarkdownInlineChildren(n, source))
		if text == "" {
			return ""
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(text)
	case *mdast.Emphasis:
		text := renderMarkdownInlineChildren(n, source)
		if text == "" {
			return ""
		}
		style := lipgloss.NewStyle()
		if n.Level >= 2 {
			style = style.Bold(true)
			// Strong emphasis inside a quote is how a tool marks a span of
			// quoted evidence — the term that recalled a memory, in the search
			// card. It gets a colour of its own because the rest of the quote
			// is escaped literal text drawn in the body colour, so bold alone
			// would be the only thing separating the mark from the line it sits
			// in. Emphasis outside a quote is ordinary prose and stays
			// uncoloured.
			if insideBlockquote(n) {
				style = style.Foreground(lipgloss.Color(markedSpanColor))
			}
		} else {
			style = style.Italic(true)
		}
		return style.Render(text)
	case *mdast.Link:
		label := collapseMarkdownWhitespace(renderMarkdownInlineChildren(n, source))
		dest := strings.TrimSpace(string(n.Destination))
		if label == "" {
			return dest
		}
		if dest == "" || label == dest {
			return label
		}
		return label + " (" + dest + ")"
	case *mdast.AutoLink:
		return strings.TrimSpace(string(n.Label(source)))
	case *mdast.Image:
		label := collapseMarkdownWhitespace(renderMarkdownInlineChildren(n, source))
		dest := strings.TrimSpace(string(n.Destination))
		switch {
		case label != "" && dest != "":
			return label + " <" + dest + ">"
		case label != "":
			return label
		default:
			return dest
		}
	case *mdast.RawHTML:
		return collapseMarkdownWhitespace(string(node.Text(source)))
	default:
		return renderMarkdownInlineChildren(node, source)
	}
}

// markdownTextValue is the text a run of Markdown says, as opposed to the
// source it is written as: a backslash before a punctuation character is the
// author saying "this one is not markup", and the character alone is what the
// reader is meant to see. Text a parser marked raw — the inside of a code span
// or of inline HTML — is exempt, because Markdown does not read escapes there
// either.
//
// A renderer that skips this shows a backslash that is not in the document, and
// the only way to put a literal asterisk or underscore on screen stops working:
// a tool quoting a file verbatim has to escape what it quotes, and a path like
// "chatibs_claw_client" is then either shown with backslashes or, unescaped,
// silently italicized into a path that does not exist.
func markdownTextValue(value []byte, raw bool) string {
	if raw {
		return string(value)
	}
	return string(util.UnescapePunctuations(value))
}

func collapseMarkdownWhitespace(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r", ""), "\n")
	for i, line := range lines {
		lines[i] = strings.Join(strings.Fields(line), " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// prefixLines indents text: firstPrefix on the first row, restPrefix on every
// other one. Rows a line was soft-wrapped onto take restPrefix like any other
// row — a wrapped list item lines up under its bullet — and stay marked as
// wraps, so an enclosing block can still tell them from real lines.
func prefixLines(text string, firstPrefix string, restPrefix string) string {
	return prefixMarkdownRows(text, firstPrefix, restPrefix, restPrefix)
}

// prefixMarkdownRows indents text with one prefix for the first row, one for
// each further line, and one for the rows a line was soft-wrapped onto.
func prefixMarkdownRows(text string, firstPrefix, linePrefix, wrapPrefix string) string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return ""
	}
	var out strings.Builder
	out.Grow(len(text) + 8)
	for lineIndex, line := range strings.Split(text, "\n") {
		if lineIndex > 0 {
			out.WriteString("\n")
		}
		for wrapIndex, row := range strings.Split(line, softWrapSeparator) {
			switch {
			case wrapIndex > 0:
				out.WriteString(softWrapSeparator)
				out.WriteString(wrapPrefix)
			case lineIndex > 0:
				out.WriteString(linePrefix)
			default:
				out.WriteString(firstPrefix)
			}
			out.WriteString(row)
		}
	}
	return out.String()
}

func normalizeAssistantText(raw string) string {
	raw = strings.ReplaceAll(raw, "\r", "")
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	lines := strings.Split(raw, "\n")
	if normalizeShiftedAssistantBody(lines) {
		return strings.Join(lines, "\n")
	}
	inFence := false
	minIndent := -1
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") {
			inFence = !inFence
			indent := leadingIndentPrefix(line)
			if minIndent == -1 || len(indent) < minIndent {
				minIndent = len(indent)
			}
			continue
		}
		if inFence || trim == "" {
			continue
		}
		indent := leadingIndentPrefix(line)
		if minIndent == -1 || len(indent) < minIndent {
			minIndent = len(indent)
		}
	}
	if minIndent <= 0 {
		return strings.Join(lines, "\n")
	}
	inFence = false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") {
			inFence = !inFence
			lines[i] = trimLeadingPrefix(line, minIndent)
			continue
		}
		if trim == "" {
			continue
		}
		if inFence {
			// Keep indentation inside fenced code blocks.
			continue
		}
		lines[i] = trimLeadingPrefix(line, minIndent)
	}
	return strings.Join(lines, "\n")
}

func normalizeShiftedAssistantBody(lines []string) bool {
	firstContent := -1
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			firstContent = i
			break
		}
	}
	if firstContent == -1 || firstContent >= len(lines)-1 {
		return false
	}
	if len(leadingIndentPrefix(lines[firstContent])) != 0 {
		return false
	}
	bodyStart := -1
	for i := firstContent + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		if len(leadingIndentPrefix(lines[i])) == 0 {
			return false
		}
		bodyStart = i
		break
	}
	if bodyStart == -1 {
		return false
	}
	inFence := false
	minIndent := -1
	for i := bodyStart; i < len(lines); i++ {
		line := lines[i]
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		indent := len(leadingIndentPrefix(line))
		if strings.HasPrefix(trim, "```") {
			if minIndent == -1 || indent < minIndent {
				minIndent = indent
			}
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if indent == 0 {
			return false
		}
		if minIndent == -1 || indent < minIndent {
			minIndent = indent
		}
	}
	if minIndent <= 0 {
		return false
	}
	inFence = false
	for i := bodyStart; i < len(lines); i++ {
		trim := strings.TrimSpace(lines[i])
		if trim == "" {
			continue
		}
		if strings.HasPrefix(trim, "```") {
			lines[i] = trimLeadingPrefix(lines[i], minIndent)
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		lines[i] = trimLeadingPrefix(lines[i], minIndent)
	}
	return true
}

func leadingIndentPrefix(line string) string {
	i := 0
	for i < len(line) {
		if line[i] != ' ' && line[i] != '\t' {
			break
		}
		i++
	}
	return line[:i]
}

func trimLeadingPrefix(line string, n int) string {
	if n <= 0 || line == "" {
		return line
	}
	i := 0
	for i < len(line) && i < n {
		if line[i] != ' ' && line[i] != '\t' {
			break
		}
		i++
	}
	return line[i:]
}

// renderCompactFrame renders transcript activity as a dense CLI stream rather
// than a bordered card.
func (r *Renderer) renderCompactFrame(f Frame, fallbackTitle string, color string) {
	title := strings.TrimSpace(f.Title)
	if title == "" {
		title = strings.TrimSpace(fallbackTitle)
	}
	content := strings.TrimSpace(f.Content)
	if toolStatusFailed(f) {
		content = normalizeFailedToolContent(content)
	}
	if content == "" && title == "" {
		return
	}
	summary := strings.TrimSpace(f.Summary)

	lineColor := color
	markerGlyph := "●"
	var actionColor string
	if f.Kind == FrameTool {
		lineColor = toolHeaderColor(f)
		actionColor = lineColor

		// Plan-mode tools get a distinct diamond marker.
		lowerTitle := strings.ToLower(strings.TrimSpace(title))
		switch {
		case lowerTitle == "enter_plan_mode" || lowerTitle == "exit_plan_mode":
			markerGlyph = "◆"
		case toolStatusCanceled(f):
			// Canceled tools use a hollow circle - the tool was never executed,
			// so it shouldn't look like a completed (filled) call.
			markerGlyph = "○"
		case skillCardStatus(f) == "failed":
			// A failed skill load is a definite failure, the same signal the
			// compaction notice and a failed fanout task carry.
			markerGlyph = "✗"
		case toolStatusRunning(f):
			// Animate the running-tool marker with a Braille spinner that rotates
			// on each repaint. Awaiting-approval tools never reach this case and
			// keep their default marker. r.spinnerPhase is the live phase in
			// viewport mode (the scratch renderer in renderFrameLines inherits it).
			markerGlyph = spinnerGlyphs[r.spinnerPhase%len(spinnerGlyphs)]
		}
	}
	bulletStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(lineColor)).Bold(true)
	actionStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(actionColor)).Bold(true)
	targetStyle := lipgloss.NewStyle()
	suffixStyle := lipgloss.NewStyle().Faint(true)
	summaryStyle := lipgloss.NewStyle().Faint(true)
	if f.Kind == FrameTool {
		// Keep every segment of a tool header on the same semantic foreground.
		// In particular, target and duration must not fall back to the terminal's
		// default white/dim styling while the action and folded header are colored.
		headerPartStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(lineColor))
		targetStyle = headerPartStyle
		suffixStyle = headerPartStyle
		summaryStyle = headerPartStyle
	}

	// Style A: "● Action  target  suffix"
	if f.Kind == FrameTool {
		action, target, suffix := toolDisplayParts(f, summary, r.cwd)
		line := bulletStyle.Render(markerGlyph)
		if action != "" {
			line += " " + actionStyle.Render(action)
		}
		if target != "" {
			line += " " + targetStyle.Render(target)
		}
		if suffix != "" {
			line += " " + suffixStyle.Render(suffix)
		}
		if toolStatusPending(f) {
			_, _ = fmt.Fprintln(r.out, wrapToolDisplayLine(line, "   "))
		} else {
			_, _ = fmt.Fprintln(r.out, wrapToolDisplayLine(line, "  │ "))
		}
	} else {
		line := bulletStyle.Render(markerGlyph)
		if title != "" {
			line += " " + actionStyle.Render(title)
		}
		if summary != "" {
			line += " " + summaryStyle.Render(summary)
		}
		if toolStatusPending(f) {
			_, _ = fmt.Fprintln(r.out, wrapToolDisplayLine(line, "   "))
		} else {
			_, _ = fmt.Fprintln(r.out, wrapToolDisplayLine(line, "  │ "))
		}
	}

	if f.Kind == FrameTool && isMCPToolTitle(title) && toolStatusPending(f) {
		// MCP start notifications carry the call arguments in Content so the
		// header can summarize them. They are not tool output; while the call is
		// still pending, keep the block header-only and wait for the completion
		// notification before rendering a body.
		_, _ = fmt.Fprintln(r.out)
		return
	}

	// A Skill card owns its whole body. Loading shows nothing but the header;
	// the terminal states carry the shared semantic body ("Loaded from …" /
	// "Failed to load from …" plus the raw error) with the absolute path
	// shortened against the project root. Anything else about a skill frame —
	// a status outside the three reachable ones — falls through to the
	// ordinary tool rendering instead of landing in the success template.
	switch skillCardStatus(f) {
	case "loading":
		_, _ = fmt.Fprintln(r.out)
		return
	case "completed", "failed":
		projectRoot := strings.TrimSpace(memory.ProjectRoot(r.cwd))
		if projectRoot == "" {
			projectRoot = strings.TrimSpace(r.cwd)
		}
		// No Faint here. formatToolOutputBlock already paints every tool's
		// output block in one foreground, so a Faint wrapper on top of it made
		// the skill body the only output in the transcript rendered at two
		// levels of dimming — visibly darker than the shell, read and edit
		// output right above it. This body is ordinary tool output and reads at
		// the same brightness as the rest.
		//
		// D4: the body is wrapped exactly once, by formatToolOutputBlock at
		// the output block's own (prefix-aware) width. Wrapping here first and
		// letting it re-wrap the fragments broke long paths twice —
		// "…/cont/ext-rest/ore/SKILL.md" chips on three lines.
		body := shortenSkillLoadPaths(content, projectRoot)
		_, _ = fmt.Fprintln(r.out, formatToolOutputBlock(body))
		_, _ = fmt.Fprintln(r.out)
		return
	}

	if content == "" {
		if f.Kind == FrameTool {
			if !toolStatusPending(f) {
				if strings.EqualFold(title, "exit_plan_mode") && toolStatusDenied(f) {
					content = lipgloss.NewStyle().Faint(true).Render(toolNoOutputText)
					content = wrapCardContent(content, maxCardContentWidth()-2)
					_, _ = fmt.Fprintln(r.out, formatToolOutputBlock(content))
					_, _ = fmt.Fprintln(r.out)
					return
				}
				if strings.EqualFold(title, "enter_plan_mode") {
					_, _ = fmt.Fprintln(r.out)
					return
				}
				if strings.EqualFold(title, "exit_plan_mode") {
					planPath := strings.TrimSpace(f.FilePath)
					if planPath == "" {
						planPath = filePathFromToolMeta(f.ToolMeta)
					}
					if planPath != "" {
						planLine := lipgloss.NewStyle().Faint(true).Render("Plan file: " + displayPath(planPath, r.cwd))
						_, _ = fmt.Fprintln(r.out, prefixLines(planLine, "  └ ", "    "))
					}
					_, _ = fmt.Fprintln(r.out)
					return
				}
				content = lipgloss.NewStyle().Faint(true).Render(toolNoOutputText)
				content = wrapCardContent(content, maxCardContentWidth()-2)
				_, _ = fmt.Fprintln(r.out, formatToolOutputBlock(content))
			}
			_, _ = fmt.Fprintln(r.out)
		}
		return
	}
	if f.Kind == FrameTool {
		if strings.EqualFold(title, "exit_plan_mode") && toolStatusDenied(f) {
			// A denied exit carries the user's feedback, not successful tool output.
			// Render it verbatim and never append the plan-file success affordance.
			content = strings.TrimSpace(f.Content)
			if content == "" {
				content = lipgloss.NewStyle().Faint(true).Render(toolNoOutputText)
			}
			content = wrapCardContent(content, maxCardContentWidth()-2)
			_, _ = fmt.Fprintln(r.out, formatToolOutputBlock(content))
			_, _ = fmt.Fprintln(r.out)
			return
		}
		if isDiffTool(title) {
			if block, ok := renderTurnDiffCard(f.Content, r.diffTheme, 0); ok {
				_, _ = fmt.Fprintln(r.out, block)
				_, _ = fmt.Fprintln(r.out)
				return
			}
		}
		// MCP tool results carry their payload as a JSON transport envelope whose
		// text blocks are Markdown. Extract every text block from the RAW frame
		// content once and render it as Markdown here, before the generic
		// summaryToolBody/formatToolContent pipeline runs. That pipeline strips
		// standalone "**bold**" lines (treating them as metadata labels) and
		// would delete the codegraph section headers ("**Exploration:**",
		// "**calls:**", …), leaving a flattened, unreadable blob. The viewport's
		// foldBlock collapses anything over 3 rows behind a click-to-expand
		// disclosure header (toolPreviewUsesRenderedBody keeps this rich body).
		if isMCPToolTitle(title) && !toolStatusFailed(f) {
			textContent, recognizedEnvelope := mcpToolOutputBodyResult(strings.TrimSpace(f.Content))
			emptyMCPOutput := recognizedEnvelope && textContent == ""
			if emptyMCPOutput {
				textContent = "(no output)"
			} else if textContent == "" {
				textContent = strings.TrimSpace(f.Content)
			}
			contentWidth := maxCardContentWidth() - 2
			if contentWidth < 20 {
				contentWidth = 20
			}
			md := ""
			if emptyMCPOutput {
				md = lipgloss.NewStyle().Faint(true).Render(textContent)
			} else {
				md = renderAssistantMarkdownWithWidth(textContent, contentWidth, r.diffTheme)
			}
			if md == "" {
				md = textContent
			}
			_, _ = fmt.Fprintln(r.out, prefixLines(md, "  └ ", "    "))
			_, _ = fmt.Fprintln(r.out)
			return
		}
		if !r.fullBodyMode && !toolStatusFailed(f) {
			content = summaryToolBody(f, title)
			if content == "" {
				_, _ = fmt.Fprintln(r.out)
				return
			}
		}
		// Format tool output for display — parse JSON, extract sections, etc.
		content = formatToolContentWithInput(title, content, r.cwd, f.ToolMeta.Input)
		if content == "" {
			content = lipgloss.NewStyle().Faint(true).Render(toolNoOutputText)
		}
		if toolStatusFailed(f) {
			_, _ = fmt.Fprintln(r.out, formatToolOutputBlock(content))
			_, _ = fmt.Fprintln(r.out)
			return
		}
		// Syntax-highlight read_file output.
		if isReadLikeTool(title) {
			fp := filePathFromToolMeta(f.ToolMeta)
			if fp == "" {
				fp = f.FilePath
			}
			if fp == "" {
				fp = filePathFromContent(content)
			}
			if fp != "" {
				_, _ = fmt.Fprintln(r.out, formatHighlightedToolOutput(content, fp, r.diffTheme))
				_, _ = fmt.Fprintln(r.out)
				return
			}
		}
		// Render note-tool output as Markdown. The notes are Markdown authored by
		// the model; render them directly before generic metadata/JSON formatting.
		// Defensive envelope fallbacks live inside noteToolMarkdownBody.
		if title == "intermediate_tool" || title == "memories_add_ad_hoc_note" {
			mdBody := noteToolMarkdownBody(title, f.Content, f.ToolMeta.Input)
			if mdBody == "" {
				_, _ = fmt.Fprintln(r.out)
				return
			}
			contentWidth := maxCardContentWidth() - 2
			if contentWidth < 20 {
				contentWidth = 20
			}
			md := renderAssistantMarkdownWithWidth(mdBody, contentWidth, r.diffTheme)
			if md == "" {
				md = mdBody
			}
			_, _ = fmt.Fprintln(r.out, prefixLines(md, "  └ ", "    "))
			_, _ = fmt.Fprintln(r.out)
			return
		}
		// Render web_fetch output as markdown.
		if isMarkdownBodyTool(title) && content != "" {
			contentWidth := maxCardContentWidth() - 2
			if contentWidth < 20 {
				contentWidth = 20
			}
			md := renderAssistantMarkdownWithWidth(content, contentWidth, r.diffTheme)
			if md == "" {
				md = content
			}
			_, _ = fmt.Fprintln(r.out, prefixLines(md, "  └ ", "    "))
			_, _ = fmt.Fprintln(r.out)
			return
		}
	}
	if f.Kind == FrameTool {
		if strings.EqualFold(title, "exit_plan_mode") {
			// Resolve the plan file path: prefer the frame's FilePath (carried
			// from the tool output via extractFilePathFromEvent), fall back to
			// the tool meta input. displayPath converts to a relative path when
			// it falls under the TUI's cwd, otherwise keeps it absolute.
			planPath := strings.TrimSpace(f.FilePath)
			if planPath == "" {
				planPath = filePathFromToolMeta(f.ToolMeta)
			}
			bodyContent := summaryToolBody(f, title)
			if bodyContent == "" {
				bodyContent = f.Content
			}
			bodyContent = formatToolContentWithInput(title, bodyContent, r.cwd, f.ToolMeta.Input)
			// Build the message body and the plan-file line as a single block
			// under the "└" prefix so the whole result reads as one cohesive
			// output area rather than a separate "plan:" header.
			var outLines []string
			if bodyContent != "" {
				bodyWidth := maxCardContentWidth() - 2
				if bodyWidth < 20 {
					bodyWidth = 20
				}
				md := renderAssistantMarkdownWithWidth(bodyContent, bodyWidth, r.diffTheme)
				if md == "" {
					md = bodyContent
				}
				outLines = append(outLines, md)
			}
			if planPath != "" {
				planLine := lipgloss.NewStyle().Faint(true).Render("Plan file: " + displayPath(planPath, r.cwd))
				outLines = append(outLines, planLine)
			}
			if len(outLines) > 0 {
				_, _ = fmt.Fprintln(r.out, prefixLines(strings.Join(outLines, "\n\n"), "  └ ", "    "))
			}
			_, _ = fmt.Fprintln(r.out)
			return
		}
		_, _ = fmt.Fprintln(r.out, formatToolOutputBlock(content))
		_, _ = fmt.Fprintln(r.out)
		return
	}
	content = wrapCardContent(content, maxCardContentWidth()-2)
	_, _ = fmt.Fprintln(r.out, prefixLines(content, "  ", "  "))
	_, _ = fmt.Fprintln(r.out)
}

// toolStatusFailed reports whether the frame represents a failed tool call.
func toolStatusFailed(f Frame) bool {
	return f.Kind == FrameTool && f.ToolMeta.Status == "failed"
}

// toolStatusRunning reports whether the frame represents a running tool call.
func toolStatusRunning(f Frame) bool {
	return f.Kind == FrameTool && f.ToolMeta.Status == "running"
}

// toolStatusPending reports whether the frame has no completed output yet
// (running or awaiting approval).
func toolStatusPending(f Frame) bool {
	s := f.ToolMeta.Status
	return f.Kind == FrameTool && (s == "running" || s == "awaiting approval")
}

// toolStatusAwaitingApproval reports whether the frame is a tool call parked at
// the approval prompt (not yet executing). Distinct from toolStatusRunning so
// the running-tool marker can animate (spinner) while awaiting-approval tools
// keep the static open circle.
func toolStatusAwaitingApproval(f Frame) bool {
	return f.Kind == FrameTool && f.ToolMeta.Status == "awaiting approval"
}

// toolStatusDenied reports whether the user rejected a tool approval.
func toolStatusDenied(f Frame) bool {
	return f.Kind == FrameTool && f.ToolMeta.Status == "denied"
}

// toolStatusCanceled reports whether the frame represents a tool call that was
// canceled before execution (e.g. the user dismissed the approval prompt).
func toolStatusCanceled(f Frame) bool {
	return f.Kind == FrameTool && f.ToolMeta.Status == "canceled"
}

// toolHeaderColor returns the semantic foreground shared by every segment of a
// tool header, including headers rebuilt by the viewport's folded path.
func toolHeaderColor(f Frame) string {
	lowerTitle := strings.ToLower(strings.TrimSpace(f.Title))
	switch lowerTitle {
	case "enter_plan_mode":
		if noColorActive() {
			return "244"
		}
		return "39" // blue
	case "exit_plan_mode":
		if toolStatusDenied(f) || toolStatusFailed(f) {
			return "203" // red on denial or failure
		}
		if noColorActive() {
			return "244"
		}
		return "70" // green on success
	case "request_permissions":
		if toolStatusDenied(f) || toolStatusFailed(f) {
			return "203" // red on denial or failure
		}
		if toolStatusRunning(f) || toolStatusCanceled(f) {
			return "244"
		}
		if noColorActive() {
			return "244"
		}
		return "70" // green on a granted request
	}

	switch {
	case toolStatusFailed(f), toolStatusDenied(f):
		return "203"
	case toolStatusRunning(f), toolStatusCanceled(f):
		// An in-flight tool is gray whatever it is doing. The Skill tint is a
		// "this loaded a skill" signal and only means something once the card
		// has settled, so it must never outrank the running color here.
		return "244"
	case strings.EqualFold(strings.TrimSpace(f.ToolMeta.Category), "skill"):
		return skillCardHeaderColor
	default:
		return ordinaryToolHeaderColor
	}
}

const (
	// skillCardHeaderColor tints a settled Skill card's glyph and header.
	//
	// Aqua, and deliberately not reasoningHeaderColor: a Skill card and a
	// "Thought for …" card sit next to each other in almost every turn that
	// loads a skill, and both used to be 147 — same glyph color, same header
	// color, one under the other — so the tint that is supposed to say "this
	// loaded a skill" carried no signal. It must not drift back to a
	// pink/magenta hue either: that reads as decoration here, not as a card
	// category. Nothing else in the transcript uses this hue, it is far from
	// the ordinary tool tint, and it stays clear of the 45 the assistant
	// bullet carries so the card glyph is not mistaken for a reply marker.
	skillCardHeaderColor = "43"
	// ordinaryToolHeaderColor tints every other settled tool card.
	ordinaryToolHeaderColor = "214"
	// reasoningHeaderColor tints the "Thought for …" card. It is named here,
	// beside the colors it must stay distinct from, so a future change to
	// either one is made with the other in view.
	reasoningHeaderColor = "147"
)

// failedActionPhrase returns a natural-language verb phrase for a failed tool,
// e.g. "edit" for edit_file. Falls back to the tool name
// verbatim when no specific mapping exists.
func failedActionPhrase(toolName string) string {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "read_file", "read":
		return "read"
	case "edit_file", "edit":
		return "edit"
	case "write_file", "write":
		return "write"
	case "shell", "bash", "sh":
		return "run"
	case "web_fetch", "webfetch":
		return "fetch"
	case "web_search", "websearch":
		return "search"
	case "subagent_run":
		return "run agent"
	case "subagent_send":
		return "send agent"
	case "subagent_status":
		return "check agent"
	case "subagent_wait":
		return "wait for agent"
	case "subagent_continue":
		return "continue agent"
	case "subagent_close":
		return "close agent"
	case "subagent_list":
		return "list agents"
	case "enter_plan_mode":
		return "enter plan mode"
	case "exit_plan_mode":
		return "exit plan mode"
	case "working_set_show":
		return "show working set"
	case "working_set_pin":
		return "pin"
	case "working_set_drop":
		return "drop"
	case "request_permissions":
		return "request permissions"
	case "session_todo":
		return "update todo"
	case "user_interaction":
		return "ask user"
	case "intermediate_tool":
		return "save notes"
	case "memories_list":
		return "list memories"
	case "memories_read":
		return "read memory"
	case "memories_search":
		return "search memories"
	default:
		return toolName
	}
}

// normalizeFailedToolContent removes the Markdown notification envelope used
// for failed tools while preserving the actual failure detail. Plain tool
// errors are returned unchanged.
func normalizeFailedToolContent(content string) string {
	trimmed := strings.TrimSpace(content)
	const errorHeading = "**error**"
	if !strings.HasPrefix(strings.ToLower(trimmed), errorHeading) {
		return content
	}

	heading := trimmed
	remainder := ""
	if idx := strings.IndexByte(trimmed, '\n'); idx >= 0 {
		heading = strings.TrimSpace(trimmed[:idx])
		remainder = strings.TrimSpace(trimmed[idx+1:])
	}
	if body := extractAnyFencedBody(remainder); body != "" {
		return body
	}
	if remainder != "" {
		return remainder
	}

	inline := strings.TrimSpace(heading[len(errorHeading):])
	if strings.HasPrefix(inline, "(") {
		if idx := strings.IndexByte(inline, ')'); idx >= 0 {
			inline = strings.TrimSpace(inline[idx+1:])
		}
	}
	if inline != "" {
		return inline
	}
	return content
}

// formatDuration formats a time.Duration for display in tool headers.
// Durations under 100ms return empty string (too fast to be useful).
// >= 60s renders as "1m23s"; >= 10s as "12s"; else "1.2s".
func formatDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	if d < 100*time.Millisecond {
		return "<0.1s"
	}
	sec := d.Seconds()
	if sec >= 60 {
		m := int(sec) / 60
		s := int(sec) % 60
		return fmt.Sprintf("%dm%ds", m, s)
	}
	if sec >= 10 {
		return fmt.Sprintf("%ds", int(sec))
	}
	return fmt.Sprintf("%.1fs", sec)
}

// displayPath returns a path relative to cwd when the path is under cwd,
// otherwise the path unchanged (absolute or already-relative).  When cwd is
// empty the function falls back to os.Getwd() so displayPath is correct even
// in contexts where the caller does not hold a cwd reference (e.g. the
// viewport fold-block path which re-captures via a scratch Renderer).
func displayPath(absPath, cwd string) string {
	if absPath == "" {
		return absPath
	}
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return absPath
		}
	}
	// If the path is already relative (no leading /) or outside cwd,
	// return unchanged.
	if absPath == cwd {
		return "."
	}
	if strings.HasPrefix(absPath, cwd+string(filepath.Separator)) {
		rel, err := filepath.Rel(cwd, absPath)
		if err == nil {
			return rel
		}
		return strings.TrimPrefix(absPath, cwd+string(filepath.Separator))
	}
	return absPath
}

// truncateURLForHeader middle-truncates a URL for display in a tool header,
// preserving the scheme+host and last path segment.
// Example: "https://example.com/a/b/c" → "…://example.com/…/c".
func truncateURLForHeader(url string, maxWidth int) string {
	if maxWidth <= 0 || lipgloss.Width(url) <= maxWidth {
		return url
	}
	// Find scheme+host boundary.
	idx := strings.Index(url, "://")
	if idx < 0 {
		return middleTruncate(url, maxWidth)
	}
	schemeHostEnd := idx + 3 // after "://"
	rest := url[schemeHostEnd:]
	hostEnd := strings.Index(rest, "/")
	if hostEnd < 0 {
		// No path — just scheme+host.
		return middleTruncate(url, maxWidth)
	}
	schemeHost := url[:schemeHostEnd+hostEnd]
	path := rest[hostEnd:]
	// Keep last path segment.
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	lastSegment := parts[len(parts)-1]
	if lastSegment == "" && len(parts) > 1 {
		lastSegment = parts[len(parts)-2]
	}
	candidate := schemeHost + "/…/" + lastSegment
	if lipgloss.Width(candidate) <= maxWidth {
		return candidate
	}
	return middleTruncate(url, maxWidth)
}

// middleTruncate truncates a string to maxWidth by removing characters from
// the middle and inserting "…". Preserves the beginning and end.
func middleTruncate(s string, maxWidth int) string {
	if maxWidth <= 0 || lipgloss.Width(s) <= maxWidth {
		return s
	}
	if maxWidth <= 3 {
		return s[:maxWidth]
	}
	runes := []rune(s)
	half := (maxWidth - 1) / 2 // reserve 1 for "…"
	left := half
	right := maxWidth - 1 - half
	if left+right > len(runes) {
		return s
	}
	return string(runes[:left]) + "…" + string(runes[len(runes)-right:])
}

// turnDiffStats extracts added/deleted line counts from a ```diff fenced block.
func turnDiffStats(content string) (added, deleted int, ok bool) {
	lines := strings.Split(content, "\n")
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inFence {
				// Match only the exact closing fence (``` with optional trailing
				// whitespace). Diff context lines (e.g. "  ```sh" or "  ``` ")
				// have leading space and must not terminate counting.
				if strings.TrimRight(line, " \t\r") == "```" {
					break
				}
				continue
			}
			inFence = strings.HasPrefix(trimmed, "```diff")
			continue
		}
		if inFence {
			if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "@@") {
				continue
			}
			if len(line) > 0 && line[0] == '+' {
				added++
			} else if len(line) > 0 && line[0] == '-' {
				deleted++
			}
		}
	}
	if added == 0 && deleted == 0 {
		return 0, 0, false
	}
	return added, deleted, true
}

// ToolDisplayHeader returns the Style A header text for a Frame, e.g. "Read types.go lines 2-101".
// Used by the viewport to build collapsed block headers.
func ToolDisplayHeader(f Frame, cwd string) string {
	action, target, suffix := toolDisplayParts(f, strings.TrimSpace(f.Summary), cwd)
	var parts []string
	if action != "" {
		parts = append(parts, action)
	}
	if target != "" {
		parts = append(parts, target)
	}
	if suffix != "" {
		parts = append(parts, suffix)
	}
	return strings.Join(parts, " ")
}

// toolDisplayParts maps a Frame's tool title and summary to three user-facing
// parts: action (past/progressive verb), target (the thing acted on), and
// suffix (extra metadata like duration or exit status).
func toolDisplayParts(f Frame, summary string, cwd string) (action, target, suffix string) {
	title := strings.TrimSpace(f.Title)
	if title == "" {
		return "", "", ""
	}
	meta := f.ToolMeta
	lower := strings.ToLower(title)

	isRunning := toolStatusRunning(f)
	isPending := toolStatusPending(f)
	isFailed := toolStatusFailed(f)
	isDenied := toolStatusDenied(f)
	isCanceled := toolStatusCanceled(f)
	durationStatus := strings.ToLower(strings.TrimSpace(meta.Status))
	isCompleted := durationStatus == "completed" || (durationStatus == "" && f.Final && !isFailed && !isDenied && !isCanceled)

	filePath := filePathFromToolMeta(meta)
	if filePath == "" {
		filePath = f.FilePath
	}
	displayP := ""
	if filePath != "" {
		displayP = displayPath(filePath, cwd)
	}

	switch {
	case skillCardStatus(f) != "":
		// The three reachable Skill states own their header. Loading says what
		// is happening; the terminal states name the skill and let the shared
		// tail below append the real duration. Everything else about this frame
		// is ordinary tool rendering.
		target = strings.TrimSpace(meta.SkillName)
		if skillCardStatus(f) == "loading" {
			action = "Loading skill"
		} else {
			action = "Skill"
		}

	// --- Filesystem ---
	case lower == "read_file" || lower == "read":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Reading"
		} else {
			action = "Read"
		}
		target = displayP
		if isCompleted {
			if lo, hi, ok := readFileDisplayLineRange(meta); ok {
				suffix = fmt.Sprintf("lines %d-%d", lo, hi)
			}
		}

	case lower == "edit_file":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Editing"
		} else {
			action = "Edited"
		}
		target = displayP

	case lower == "write_file":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Writing"
		} else {
			_, deleted, ok := turnDiffStats(f.Content)
			if ok {
				if deleted > 0 {
					action = "Wrote"
				} else {
					action = "Created"
				}
			} else {
				action = "Created"
			}
		}
		target = displayP

	case lower == "request_permissions":
		if isFailed {
			action = "Failed to request permissions"
		} else if isDenied {
			action = "Permission request denied"
		} else if isCanceled {
			action = "Permission request canceled"
		} else if isRunning || isPending {
			action = "Requesting permissions"
		} else {
			action = "Requested permissions"
		}

	// --- Shell ---
	case lower == "shell":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Running"
		} else {
			action = "Ran"
		}
		target = inputString(meta, "command")
		if target == "" {
			target = extractShellCommand(summary)
		}

	// --- Web ---
	case lower == "web_fetch" || lower == "webfetch":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Fetching"
		} else {
			action = "Fetched"
		}
		if u := inputString(meta, "url"); u != "" {
			target = truncateURLForHeader(u, 60)
		}

	case lower == "web_search" || lower == "websearch":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Searching"
		} else {
			action = "Searched"
		}
		if q := inputString(meta, "query"); q != "" {
			target = q
		}

	case lower == "retrieve_output":
		switch {
		case isFailed:
			action = "Failed to retrieve saved output"
		case isDenied:
			action = "Output retrieval denied"
		case isCanceled:
			action = "Output retrieval canceled"
		case inputString(meta, "query") != "" && (isRunning || isPending):
			action = "Searching saved output"
		case inputString(meta, "query") != "":
			action = "Searched saved output"
		case isRunning || isPending:
			action = "Retrieving saved output"
		default:
			action = "Retrieved saved output"
		}
		if id, ok := inputInt(meta, "id"); ok && id > 0 {
			target = fmt.Sprintf("#%d", id)
		}
		if query := strings.Join(strings.Fields(inputString(meta, "query")), " "); query != "" {
			suffix = "matching “" + truncateForDisplay(query, 72) + "”"
		} else if lines := strings.TrimSpace(inputString(meta, "lines")); lines != "" {
			suffix = "lines " + truncateForDisplay(lines, 32)
		}

	// --- Session ---
	case lower == "session_todo":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Updating todo"
		} else {
			action = "Todo"
		}

	case lower == "enter_plan_mode":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Entering plan mode"
		} else {
			action = "Entered plan mode"
		}

	case lower == "exit_plan_mode":
		if isDenied {
			action = "Kept planning"
		} else if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Exiting plan mode"
		} else {
			action = "Exited plan mode"
		}

	case lower == "memories_add_ad_hoc_note":
		switch {
		case isFailed:
			action = "Failed to save memory note"
		case isDenied:
			action = "Memory note save denied"
		case isCanceled:
			action = "Memory note save canceled"
		case isRunning || isPending:
			action = "Saving memory note"
		default:
			action = "Saved memory note"
		}
		target = inputString(meta, "filename")
		// A saved note is named by the path it is stored at — the path it is
		// read back by — so a reader is never left guessing where it lives. The
		// states that wrote nothing keep the bare filename rather than claiming
		// a location on disk.
		if target != "" && action == "Saved memory note" {
			target = memory.AdHocNotePath(target)
		}

	// A memory query that the user denied or dismissed never touched the store,
	// so these cases name the refused action rather than falling through to the
	// past tense ("Read memory MEMORY.md") that claims it happened.
	case lower == "memories_list":
		switch {
		case isFailed:
			action = "Failed to " + failedActionPhrase(lower)
		case isDenied:
			action = "Memory listing denied"
		case isCanceled:
			action = "Memory listing canceled"
		case isRunning || isPending:
			action = "Listing memories"
		default:
			action = "Listed memories"
		}
		target = displayLine(inputString(meta, "path"))

	case lower == "memories_read":
		switch {
		case isFailed:
			action = "Failed to " + failedActionPhrase(lower)
		case isDenied:
			action = "Memory read denied"
		case isCanceled:
			action = "Memory read canceled"
		case isRunning || isPending:
			action = "Reading memory"
		default:
			action = "Read memory"
		}
		target = displayLine(inputString(meta, "path"))
		if isCompleted {
			if lo, hi, ok := tool.MemoryReadLineRange(meta.Input); ok {
				suffix = fmt.Sprintf("lines %d-%d", lo, hi)
			}
		}

	case lower == "memories_search":
		switch {
		case isFailed:
			action = "Failed to " + failedActionPhrase(lower)
		case isDenied:
			action = "Memory search denied"
		case isCanceled:
			action = "Memory search canceled"
		case isRunning || isPending:
			action = "Searching memories"
		default:
			action = "Searched memories"
		}
		// The queries are the call. They are shown in full and the header wraps;
		// where it looked rides in the suffix, so "in MEMORY.md" stays legible
		// next to a long query list, and a search of the whole store shows no
		// scope.
		target = displayLine(tool.MemorySearchQueryLabel(meta.Input))
		suffix = displayLine(tool.MemorySearchScopeLabel(meta.Input))

	case lower == "intermediate_tool":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Saving notes"
		} else {
			action = "Notes"
		}
		if a := inputString(meta, "action"); a != "" {
			target = a
		}

	case lower == "user_interaction":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Asking user"
		} else {
			action = "Asked user"
		}
		// What the call asks, named in full: the same words the web's card
		// header names it with.
		target = tool.UserInteractionQuestionsLabel(meta.Input)

	// --- Context ---
	case lower == "working_set_show":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Showing working set"
		} else {
			action = "Working set"
		}

	case lower == "working_set_pin":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Pinning"
		} else {
			action = "Pinned"
		}
		target = displayP

	case lower == "working_set_drop":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Dropping"
		} else {
			action = "Dropped"
		}
		target = displayP

	// --- Subagent ---
	case lower == "subagent_run":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Running agent"
		} else {
			action = "Agent"
		}
		if t := inputString(meta, "title"); t != "" {
			target = t
		} else if d := inputString(meta, "description"); d != "" {
			target = truncateForDisplay(d, 60)
		}
		if at := strings.TrimSpace(meta.AgentType); at != "" {
			suffix = at
		}

	case lower == "subagent_fanout":
		return "", "", ""

	case lower == "subagent_send":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Sending agent"
		} else {
			action = "Sent agent"
		}
		if t := inputString(meta, "title"); t != "" {
			target = t
		} else if d := inputString(meta, "description"); d != "" {
			target = truncateForDisplay(d, 60)
		}
		if at := inputString(meta, "subagent_type"); at != "" {
			suffix = at
		} else if at := strings.TrimSpace(meta.AgentType); at != "" {
			suffix = at
		}

	case lower == "subagent_status":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Checking agent"
		} else {
			action = "Agent status"
		}
		if tid := inputString(meta, "task_id"); tid != "" {
			target = tid
		}

	case lower == "subagent_wait":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Waiting for agent"
		} else {
			action = "Agent done"
		}
		if tid := inputString(meta, "task_id"); tid != "" {
			target = tid
		}

	case lower == "subagent_continue":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Continuing agent"
		} else {
			action = "Continued agent"
		}
		if tid := inputString(meta, "task_id"); tid != "" {
			target = tid
		}

	case lower == "subagent_close":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Closing agent"
		} else {
			action = "Closed agent"
		}
		if tid := inputString(meta, "task_id"); tid != "" {
			target = tid
		}

	case lower == "subagent_list":
		if isFailed {
			action = "Failed to " + failedActionPhrase(lower)
		} else if isRunning || isPending {
			action = "Listing agents"
		} else {
			action = "Agent list"
		}

	// --- MCP ---
	case strings.HasPrefix(lower, "mcp__"):
		action = "MCP"
		if display, ok := tool.McpToolDisplayName(title); ok {
			target = display
		}
		suffix = tool.MCPToolInputSummary(meta.Input)
	default:
		action = title
		target = defaultToolInvocationTarget(title, meta)
	}

	if isCanceled && !toolStatesOwnCanceledLabel(lower) {
		action = "Canceled"
		suffix = ""
	}
	if isDenied && !toolStatesOwnDeniedLabel(lower) {
		action = "Denied"
		suffix = ""
	}

	// Append the executor-supplied duration for terminal states that represent an
	// actual execution. Pending, denied, and canceled cards intentionally omit it.
	if !isRunning && !isPending && !isDenied && !isCanceled && (isFailed || isCompleted) {
		if ds := formatDuration(f.Duration); ds != "" {
			if suffix == "" {
				suffix = ds
			} else {
				suffix = suffix + " · " + ds
			}
		}
	}

	// A tool that is still executing shows how long it has been running; the
	// viewport supplies the elapsed time, and sub-second runs show nothing.
	if isRunning && f.Duration >= time.Second && !toolStatusAwaitingApproval(f) {
		if suffix == "" {
			suffix = formatWorkingElapsed(f.Duration)
		} else {
			suffix = suffix + " · " + formatWorkingElapsed(f.Duration)
		}
	}

	return strings.TrimSpace(action), strings.TrimSpace(target), strings.TrimSpace(suffix)
}

// skillCardStatus reports which Skill card a frame renders as: "loading"
// while the load is in flight, "completed" or "failed" once it has settled.
// It returns "" whenever the frame is not a Skill card at all — and for any
// status outside the three reachable ones, so a value the shared status
// vocabulary may gain later can never land in the success template.
func skillCardStatus(f Frame) string {
	if f.Kind != FrameTool ||
		!strings.EqualFold(strings.TrimSpace(f.ToolMeta.Category), "skill") ||
		strings.TrimSpace(f.ToolMeta.SkillName) == "" ||
		strings.TrimSpace(f.ToolMeta.SkillPath) == "" {
		return ""
	}
	if toolStatusFailed(f) {
		return "failed"
	}
	if toolStatusPending(f) {
		return "loading"
	}
	status := strings.ToLower(strings.TrimSpace(f.ToolMeta.Status))
	if status == "completed" || (status == "" && f.Final) {
		return "completed"
	}
	return ""
}

// shortenSkillLoadPaths rewrites the absolute path in the shared skill body
// against the project root and leaves every other byte — including the raw
// error line below it — exactly as the shared layer wrote it.
func shortenSkillLoadPaths(body, projectRoot string) string {
	for _, prefix := range []string{"Loaded from ", "Failed to load from "} {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(body), prefix); ok {
			path := rest
			if idx := strings.IndexByte(rest, '\n'); idx >= 0 {
				path = rest[:idx]
			}
			return prefix + displayPath(strings.TrimSpace(path), projectRoot) + strings.TrimPrefix(rest, path)
		}
	}
	return body
}

// toolStatesOwnCanceledLabel reports whether a tool already phrases its own
// canceled header (e.g. "Memory read canceled"), which the generic "Canceled"
// override must leave alone.
func toolStatesOwnCanceledLabel(lower string) bool {
	return lower == "request_permissions" || lower == "memories_add_ad_hoc_note" || isMemoryQueryTool(lower)
}

// toolStatesOwnDeniedLabel is the same rule for the refused card. A tool the
// user refused never ran, so without this the switch above would report it in
// the past tense ("Ran ls", "Created main.go") and claim work that a denial
// prevented.
func toolStatesOwnDeniedLabel(lower string) bool {
	switch lower {
	case "request_permissions", "exit_plan_mode", "retrieve_output", "memories_add_ad_hoc_note":
		return true
	default:
		return isMemoryQueryTool(lower)
	}
}

func defaultToolInvocationTarget(title string, meta tool.ToolMeta) string {
	invocation := strings.TrimSpace(meta.Invocation)
	if invocation == "" {
		return ""
	}
	toolName := strings.TrimSpace(title)
	if toolName == "" {
		toolName = strings.TrimSpace(meta.ToolName)
	}
	if toolName == "" {
		return invocation
	}
	if strings.EqualFold(invocation, toolName) {
		return ""
	}
	if len(invocation) > len(toolName) && strings.EqualFold(invocation[:len(toolName)], toolName) {
		rest := strings.TrimLeft(invocation[len(toolName):], " \t")
		if rest != invocation[len(toolName):] {
			return strings.TrimSpace(rest)
		}
	}
	return invocation
}

// extractShellCommand extracts the shell command from the summary.
// Summary looks like "> command" or "ran command"; trailing details such as
// " · exit N" are omitted from the header.
func extractShellCommand(summary string) string {
	summary = strings.TrimSpace(summary)
	if strings.HasPrefix(summary, "> ") {
		summary = strings.TrimPrefix(summary, "> ")
	} else if strings.HasPrefix(summary, "ran ") {
		summary = strings.TrimPrefix(summary, "ran ")
	} else {
		return ""
	}
	if idx := strings.Index(summary, " · "); idx >= 0 {
		summary = strings.TrimSpace(summary[:idx])
	}
	return summary
}

// filePathFromToolMeta extracts the file_path string from a tool's input metadata.
// Returns "" when no file_path key is present or the value is not a string.
func filePathFromToolMeta(tm tool.ToolMeta) string {
	if v, ok := tm.Input["file_path"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// inputString extracts a string value from a tool's Input metadata map.
// Returns "" when the key is absent or the value is not a string.
func inputString(tm tool.ToolMeta, key string) string {
	if v, ok := tm.Input[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// readFileDisplayLineRange returns the 1-based inclusive file line range a
// completed read_file card actually shows. Engine facts win over the request
// arguments: the caller's limit may exceed the page the engine returned (the
// 200-line ceiling), and the header must describe the result, not the request.
// The input-derived range remains only as the fallback for sessions replayed
// before the facts were carried in ToolMeta.
func readFileDisplayLineRange(tm tool.ToolMeta) (lo, hi int, ok bool) {
	if tm.ResultLines > 0 {
		return tm.ResultOffset + 1, tm.ResultOffset + tm.ResultLines, true
	}
	return readFileInputLineRange(tm)
}

// readFileInputLineRange converts read_file's 0-based offset and line limit
// into the 1-based inclusive range shown in completed tool headers.
func readFileInputLineRange(tm tool.ToolMeta) (lo, hi int, ok bool) {
	offset, hasOffset := inputInt(tm, "offset")
	limit, hasLimit := inputInt(tm, "limit")
	if !hasOffset || !hasLimit || offset < 0 || limit <= 0 {
		return 0, 0, false
	}
	return offset + 1, offset + limit, true
}

func inputInt(tm tool.ToolMeta, key string) (int, bool) {
	value, ok := tm.Input[key]
	if !ok {
		return 0, false
	}
	switch n := value.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		value := int(n)
		return value, float64(value) == n
	case json.Number:
		value, err := n.Int64()
		return int(value), err == nil
	default:
		return 0, false
	}
}

// filePathFromContent extracts a file path from formatted tool output content.
// read_file output is formatted as: path: `/path/to/file`\n\n```lang\n...\n```
// Returns "" when no path marker is found.
func filePathFromContent(content string) string {
	// Look for "path: `" pattern in the first few lines.
	lines := strings.Split(strings.ReplaceAll(content, "\r", ""), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "path:") {
			continue
		}
		rest := strings.TrimPrefix(trimmed, "path:")
		rest = strings.TrimSpace(rest)
		// Strip backtick quoting: `path` or ``path``
		rest = strings.Trim(rest, "`")
		rest = strings.TrimSpace(rest)
		if rest != "" {
			return rest
		}
	}
	return ""
}

// highlightCodeLine applies chroma syntax highlighting to a single line of code,
// normalised to the terminal theme's syntax brightness so fenced code blocks and
// read_file output read at the same level as the prose and diffs around them.
// Returns the original line when color is disabled or highlighting fails.
func highlightCodeLine(line string, lexer chroma.Lexer, st *chroma.Style, fg syntaxBrightness) string {
	if lexer == nil || st == nil || strings.TrimSpace(line) == "" {
		return line
	}
	it, err := lexer.Tokenise(nil, line)
	if err != nil {
		return line
	}
	trueColor := terminalTrueColor()
	var buf strings.Builder
	for _, tok := range it.Tokens() {
		val := tok.Value
		if val == "" {
			continue
		}
		if entry := st.Get(tok.Type); entry.Colour.IsSet() {
			buf.WriteString(fg.sequence(trueColor, entry.Colour.Red(), entry.Colour.Green(), entry.Colour.Blue()))
		} else {
			buf.WriteString("\x1b[39m")
		}
		buf.WriteString(val)
	}
	return strings.TrimRight(buf.String(), "\n")
}

// resolveReadFileLexer returns a chroma lexer for the given file path, or nil
// when color is disabled.
func resolveReadFileLexer(filePath string, theme DiffTheme) chroma.Lexer {
	if noColorActive() {
		return nil
	}
	lexer := lexers.Match(filepath.Base(filePath))
	if lexer == nil {
		lexer = lexers.Fallback
	}
	return chroma.Coalesce(lexer)
}

const (
	// codeStyleDark and codeStyleLight are the syntax theme every code surface
	// in the TUI is painted with: Monokai and its light sibling. One theme for
	// all of them — read_file output, markdown fenced code, the diff inside an
	// edit_file/write_file card and the same diff inside the approval overlay —
	// because a file does not change colour depending on which card is showing
	// it. Monokai's palette is the reason it is the one: a cyan keyword, a green
	// identifier, a pink operator and a yellow string survive the terminal's own
	// quantisation as four different colours, which the muted theme used here
	// before did not — after its syntax colours were pulled to one brightness,
	// every class converged on one pale tone and the highlighting stopped
	// reading as highlighting at all.
	codeStyleDark  = "monokai"
	codeStyleLight = "monokailight"
)

// codeStyleNameFor returns the chroma style name for a terminal theme.
func codeStyleNameFor(theme DiffTheme) string {
	if theme == DiffThemeLight {
		return codeStyleLight
	}
	return codeStyleDark
}

// resolveCodeStyle returns the chroma style for code painted straight onto the
// terminal background — read_file output and markdown fenced code blocks. Rows
// over a diff's add/del band resolve the same style through the diff palette;
// what differs between the two surfaces is only how far the colours are pulled
// towards the background's brightness (see syntaxBrightness), never which
// theme they come from.
func resolveCodeStyle(theme DiffTheme) *chroma.Style {
	if noColorActive() {
		return nil
	}
	return styles.Get(codeStyleNameFor(theme))
}

// formatHighlightedToolOutput wraps and syntax-highlights tool output (e.g. read_file).
// Unlike formatToolOutputBlock, it applies per-line chroma highlighting after wrapping,
// so ANSI codes never interfere with width measurement.
func formatHighlightedToolOutput(content, filePath string, theme DiffTheme) string {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return ""
	}
	width := termWidthOrDefault() - contentRightMargin
	if width <= 0 {
		width = 80
	}
	lexer := resolveReadFileLexer(filePath, theme)
	st := resolveCodeStyle(theme)
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		line = separateReadFileLineNumber(line)
		first := i == 0
		prefix := toolOutputLinePrefix("", first, true)
		wrapWidth := width - lipgloss.Width(prefix)
		if wrapWidth < 20 {
			wrapWidth = 20
		}
		wrapped := wrapCardLine(line, wrapWidth)
		for j, part := range wrapped {
			highlighted := highlightCodeLine(part, lexer, st, plainSyntaxBrightnessFor(theme))
			// Tree prefix in dim grey, code body in chroma syntax colours
			linePrefix := toolOutputLinePrefix("", first && j == 0, true)
			out = append(out, thinkingColor+linePrefix+sharedBlockReset+highlighted+sharedBlockReset)
		}
	}
	return strings.Join(out, "\n")
}

// separateReadFileLineNumber keeps the read_file line-number gutter visually
// distinct from the source text. The tool's transport format is "<n>|<text>";
// rendering that verbatim can create font ligatures such as "|}" when a
// top-level closing brace immediately follows the separator. Keep the compact
// transport contract unchanged and add the gutter spacing only in the TUI.
func separateReadFileLineNumber(line string) string {
	bar := strings.IndexByte(line, '|')
	if bar <= 0 {
		return line
	}
	for i := 0; i < bar; i++ {
		if line[i] < '0' || line[i] > '9' {
			return line
		}
	}
	return line[:bar+1] + " " + line[bar+1:]
}

func formatToolContentWithInput(title, content, cwd string, input map[string]any) string {
	// TrimRight preserves leading indentation on the first content line.
	content = strings.TrimRight(content, "\n\t\r ")
	if content == "" {
		return ""
	}
	lower := strings.ToLower(strings.TrimSpace(title))
	// retrieve_output returns the recovered value itself. It may contain
	// headings, lists, tables, or fenced code, so preserve the Markdown source
	// instead of running the generic metadata/JSON section stripper over it.
	if lower == "retrieve_output" {
		return retrieveOutputMarkdownBody(content)
	}
	if lower == "web_search" || lower == "websearch" {
		if formatted, recognized := tool.FormatWebSearchResult(content); recognized {
			return formatted
		}
		// New events are formatted before reaching the renderer. This fallback
		// protects replayed legacy cards whose JSON envelope is malformed: raw
		// provider transport data must never become the user-facing body.
		if isToolOutputJSON(content) || strings.Contains(strings.ToLower(content), "```json") {
			return "Web search completed, but its results could not be displayed."
		}
		// Completed events already arrive as Markdown from tool. Preserve
		// that Markdown verbatim so the generic metadata cleaner does not remove
		// the standalone bold result-count heading.
		return content
	}

	if lower == "web_fetch" || lower == "webfetch" {
		return webFetchMarkdownBody(content)
	}

	// enter_plan_mode: extract and display the reason field from JSON output.
	if lower == "enter_plan_mode" {
		return formatEnterPlanModeOutput(content)
	}

	// user_interaction: render question headers and answers without exposing the
	// JSON transport fields. Already formatted notification bodies are preserved.
	if lower == "user_interaction" {
		if body, ok := tool.FormatUserInteractionResult(content, input); ok {
			return body
		}
		return content
	}

	// Memory query results arrive as Markdown from tool. Preserve it
	// verbatim: the generic cleaner below drops standalone "**bold**" lines and
	// would delete the result heading ("**Memory matches for `deploy`**").
	if isMemoryQueryTool(lower) {
		return memoryToolOutputBody(content)
	}

	// MCP tool results use a JSON transport envelope whose text blocks are
	// Markdown. Unwrap it before the generic JSON pretty-printer sees it.
	if isMCPToolTitle(title) {
		return mcpToolOutputBody(content)
	}

	// Working set pin output is Markdown from agentnotify; keep the readable
	// structure but display paths with the same cwd-relative rule as path tools.
	if lower == "working_set_pin" {
		return formatWorkingSetPinOutput(content, cwd)
	}

	// Bash/shell tools: extract stdout section, keep it readable.
	if strings.Contains(lower, "bash") || strings.Contains(lower, "shell") ||
		strings.Contains(lower, "sh") && !strings.Contains(lower, "bashrc") {
		return formatShellOutput(content)
	}

	// All other tools (read, edit, write, webfetch, mcp, etc.):
	// strip metadata labels and extract fenced content.
	if clean := stripToolMetadataSections(content); clean != "" {
		if out := formatJSONOutput(clean); out != "" {
			return out
		}
		return clean
	}

	return content
}

func formatWorkingSetPinOutput(content, cwd string) string {
	payload := content
	if body := extractAnyFencedBody(content); body != "" {
		payload = body
	}
	var v struct {
		Count int      `json:"count"`
		Pins  []string `json:"pins"`
	}
	if err := json.Unmarshal([]byte(payload), &v); err == nil {
		if len(v.Pins) == 0 && v.Count == 0 {
			return "No working set entries are pinned."
		}
		count := v.Count
		if count == 0 {
			count = len(v.Pins)
		}
		label := "entry"
		if count != 1 {
			label = "entries"
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Pinned %d %s in the working set:", count, label))
		for _, pin := range v.Pins {
			pin = strings.TrimSpace(pin)
			if pin == "" {
				continue
			}
			sb.WriteString("\n- `")
			sb.WriteString(displayPath(pin, cwd))
			sb.WriteString("`")
		}
		return sb.String()
	}

	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- `") || !strings.HasSuffix(trimmed, "`") {
			continue
		}
		path := strings.TrimSuffix(strings.TrimPrefix(trimmed, "- `"), "`")
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		prefix := line[:strings.Index(line, "-")]
		lines[i] = prefix + "- `" + displayPath(path, cwd) + "`"
	}
	return strings.Join(lines, "\n")
}

// formatEnterPlanModeOutput extracts and displays the reason field from
// enter_plan_mode tool output. Returns empty string when no reason is present.
func formatEnterPlanModeOutput(content string) string {
	// Extract JSON payload: try fenced block bodies first, then raw content.
	payload := content
	if body := extractAnyFencedBody(content); body != "" {
		payload = body
	}

	// Live tool notifications wrap the tool's JSON response in the generic
	// formatter envelope: {"output":"{...}"}. Unwrap that textual output
	// before decoding the actual enter-plan result. Direct JSON responses remain
	// supported for replay and callers that do not use the generic formatter.
	var envelope struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal([]byte(payload), &envelope); err == nil && strings.TrimSpace(envelope.Output) != "" {
		payload = envelope.Output
	}

	var v struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		// Not valid JSON or parsing failed - return empty to suppress output.
		return ""
	}
	return strings.TrimSpace(v.Reason)
}

// formatJSONOutput pretty-prints JSON content with indentation. Returns empty
// string if content is not valid JSON.
func formatJSONOutput(content string) string {
	if !isToolOutputJSON(content) {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return ""
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return string(pretty)
}

// isToolOutputJSON reports whether content looks like a JSON value (object or
// array at the top level).
func isToolOutputJSON(content string) bool {
	content = strings.TrimSpace(content)
	return (strings.HasPrefix(content, "{") && strings.HasSuffix(content, "}")) ||
		(strings.HasPrefix(content, "[") && strings.HasSuffix(content, "]"))
}

// formatShellOutput extracts human-readable stdout from shell tool output.
// Shell output typically has "stdout:" and "stderr:" labeled sections with
// fenced content blocks.
func formatShellOutput(content string) string {
	// Use the existing section extractor to pull out stdout/stderr.
	sections := toolOutputSections(content)
	if len(sections) > 0 {
		out := strings.Join(sections, "\n")
		out = stripToolOutputInlineNotes(out)
		// TrimRight preserves leading indentation on the first content line.
		return strings.TrimRight(out, "\n\t\r ")
	}
	// Fall back to stripping metadata and keeping the gateway.
	return strings.TrimRight(stripToolMetadataSections(content), "\n\t\r ")
}

// extractAnyFencedBody extracts the body of the first fenced code block found
// in content, regardless of what label (if any) precedes it. Returns empty
// string when no fenced block is found. Unlike toolOutputSections, this does
// not require a stdout:/stderr: label — it finds any ```-delimited block.
func extractAnyFencedBody(content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r", ""), "\n")
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trimmed, "```") {
			continue
		}
		body := []string{}
		for i++; i < len(lines); i++ {
			if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
				return trimBlankFenceEdges(body)
			}
			body = append(body, lines[i])
		}
		return trimBlankFenceEdges(body)
	}
	return ""
}

// toolNoOutputText is the placeholder a tool card shows in place of an empty
// body. Callers pass it styled faint; formatToolOutputBlock keeps that style.
const toolNoOutputText = "(no output)"

// formatToolOutputBlock wraps tool output lines with the tree-drawing prefix
// ("  └ " for the first line, "    " for continuation). Empty content renders
// as nothing.
func formatToolOutputBlock(content string) string {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return ""
	}
	width := termWidthOrDefault() - contentRightMargin
	if width < 20 {
		width = 80
	}
	// Tool output is painted in one foreground below. Colour codes the command
	// itself printed (a reset, a default-foreground or a bright-white code) would
	// override that paint mid-line and leave the rest of the block in the
	// terminal's own foreground, so the output is drawn without them.
	content = sgrPattern.ReplaceAllString(content, "")
	if content == toolNoOutputText {
		// The placeholder is the renderer's own text, not command output: it
		// stays faint, as its callers styled it, instead of losing that style
		// with the command's colours.
		return thinkingColor + toolOutputLinePrefix(lipgloss.NewStyle().Faint(true).Render(toolNoOutputText), true, true) + sharedBlockReset
	}
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		first := i == 0
		prefix := toolOutputLinePrefix("", first, true)
		wrapWidth := width - lipgloss.Width(prefix)
		if wrapWidth < 20 {
			wrapWidth = 20
		}
		wrapped := wrapCardLine(line, wrapWidth)
		for j, part := range wrapped {
			out = append(out, thinkingColor+toolOutputLinePrefix(part, first && j == 0, true)+sharedBlockReset)
		}
	}
	return strings.Join(out, "\n")
}

func toolOutputLinePrefix(line string, first bool, tool bool) string {
	if !tool {
		return line
	}
	if first {
		return "  └ " + line
	}
	return "    " + line
}

func wrapToolDisplayLine(line string, continuationPrefix string) string {
	return wrapToolDisplayLineWidth(line, continuationPrefix, termWidthOrDefault())
}

// wrapToolDisplayLineWidth wraps a styled tool header line so neither the first
// line nor the continuation lines exceed the content right margin. It keeps
// ordinary words intact when they fit, then falls back to ANSI-safe,
// display-cell-based character wrapping for oversized tokens and CJK text.
func wrapToolDisplayLineWidth(line string, continuationPrefix string, width int) string {
	line = strings.TrimRight(line, "\n")
	if line == "" {
		return ""
	}
	if width <= 0 {
		width = 80
	}
	firstWidth := width - contentRightMargin
	if firstWidth < 20 {
		firstWidth = 20
	}
	prefixWidth := displayLineWidth(continuationPrefix)
	continuationWidth := width - contentRightMargin - prefixWidth
	if continuationWidth < 20 {
		continuationWidth = 20
	}

	words := strings.Fields(line)
	if len(words) == 0 {
		return line
	}

	out := make([]string, 0, len(words))
	var currentLine strings.Builder
	currentWidth := 0
	targetWidth := firstWidth
	flushCurrent := func() {
		if currentLine.Len() == 0 {
			return
		}
		out = append(out, currentLine.String())
		currentLine.Reset()
		currentWidth = 0
		targetWidth = continuationWidth
	}

	for _, originalWord := range words {
		word := originalWord
		if currentLine.Len() > 0 {
			wordWidth := displayLineWidth(word)
			if currentWidth+1+wordWidth <= targetWidth {
				currentLine.WriteByte(' ')
				currentLine.WriteString(word)
				currentWidth += 1 + wordWidth
				continue
			}
			flushCurrent()
		}

		// A token that cannot fit on an empty line must be split by visible
		// terminal columns. This covers uninterrupted CJK text and long paths.
		// wrapCardLineIndex also doubles as the fit check, so each successive
		// segment is scanned only once rather than repeatedly measuring the
		// entire shrinking suffix.
		for {
			cut := wrapCardLineIndex(word, targetWidth)
			if cut >= len(word) {
				currentLine.WriteString(word)
				currentWidth = displayLineWidth(word)
				break
			}
			if cut <= 0 {
				currentLine.WriteString(word)
				currentWidth = displayLineWidth(word)
				break
			}
			out = append(out, word[:cut])
			targetWidth = continuationWidth
			word = word[cut:]
		}
	}
	flushCurrent()

	if len(out) == 0 {
		return line
	}

	var result strings.Builder
	result.WriteString(out[0])
	for _, wrappedLine := range out[1:] {
		result.WriteByte('\n')
		result.WriteString(continuationPrefix)
		result.WriteString(wrappedLine)
	}
	return result.String()
}

func summaryToolBody(f Frame, title string) string {
	// Running / awaiting-approval frames have no result yet — keep just the
	// one-line header. MCP frames carry their input metadata as Content even
	// before the call completes, so suppress it explicitly here rather than
	// relying on empty Content.
	if f.StreamingOutput {
		return strings.TrimSpace(f.Content)
	}
	if toolStatusPending(f) && !f.StreamingOutput {
		return ""
	}
	// Canceled tools never executed, so they have no output body.
	if toolStatusCanceled(f) {
		return ""
	}
	content := strings.TrimSpace(f.Content)
	if content == "" {
		return ""
	}
	if strings.EqualFold(title, "web_search") || strings.EqualFold(title, "websearch") {
		if formatted, recognized := tool.FormatWebSearchResult(content); recognized {
			return formatted
		}
		if isToolOutputJSON(content) || strings.Contains(strings.ToLower(content), "```json") {
			return "Web search completed, but its results could not be displayed."
		}
		return content
	}
	if strings.EqualFold(title, "web_fetch") || strings.EqualFold(title, "webfetch") {
		return webFetchMarkdownBody(content)
	}
	// enter_plan_mode and user_interaction need formatToolContent to inspect the
	// raw result (the plan reason or structured answers respectively), so do not
	// strip their notification envelopes in the generic summary path.
	if strings.EqualFold(title, "enter_plan_mode") || strings.EqualFold(title, "user_interaction") {
		return content
	}
	if isMemoryQueryTool(title) {
		return memoryToolOutputBody(content)
	}
	if strings.EqualFold(strings.TrimSpace(title), "retrieve_output") {
		return retrieveOutputMarkdownBody(content)
	}
	if isMCPToolTitle(title) {
		return mcpToolOutputBody(content)
	}
	if isReadLikeTool(title) {
		if n := readFileContentLineCount(content); n > 0 {
			return fmt.Sprintf("Read %d lines", n)
		}
		return ""
	}
	if output := visibleToolOutput(content); output != "" {
		return output
	}
	return compactPlainLine(content)
}

func isMCPToolTitle(title string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(title)), "mcp__")
}

func retrieveOutputMarkdownBody(content string) string {
	return normalizeRetrievedOutputMarkdown(content)
}

// Query and line-range recovery responses start with a Markdown heading and
// then contain literal, line-oriented tool output. Fence that tail so Markdown
// rendering preserves every source line instead of folding adjacent lines into
// one paragraph. Full-output recovery remains untouched because it may already
// be Markdown from an MCP tool.
func normalizeRetrievedOutputMarkdown(body string) string {
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(strings.ToLower(body), "# output ") {
		return body
	}
	end := strings.IndexByte(body, '\n')
	if end < 0 {
		return body
	}
	heading := strings.TrimSpace(body[:end])
	literal := strings.TrimSpace(body[end+1:])
	if literal == "" || strings.HasPrefix(literal, "```") {
		return body
	}
	fence := "```"
	for strings.Contains(literal, fence) {
		fence += "`"
	}
	return heading + "\n\n" + fence + "\n" + literal + "\n" + fence
}

// mcpTextState distinguishes MCP content envelopes from arbitrary JSON. An
// envelope with no usable text is still recognized so callers can suppress the
// raw transport JSON and show their usual empty-output state.
type mcpTextState uint8

const (
	mcpTextUnrecognized mcpTextState = iota
	mcpTextEmpty
	mcpTextFound
)

// extractMCPToolOutputText unwraps the textual payload carried by an MCP result
// envelope. A result may arrive directly, inside a generic `output` string, or
// as a JSON-encoded string; recurse only a few levels to keep malformed output
// inexpensive and safe to display.
func extractMCPToolOutputText(content string) string {
	if text, state := extractMCPText(content, 0); state != mcpTextUnrecognized {
		return text
	}
	return strings.TrimSpace(content)
}

const maxMCPTextEnvelopeDepth = 4

func extractMCPText(content string, depth int) (string, mcpTextState) {
	content = strings.TrimSpace(content)
	if content == "" || depth >= maxMCPTextEnvelopeDepth {
		return "", mcpTextUnrecognized
	}
	var value any
	if err := json.Unmarshal([]byte(content), &value); err != nil {
		return "", mcpTextUnrecognized
	}
	return extractMCPTextValue(value, depth)
}

func extractMCPTextValue(value any, depth int) (string, mcpTextState) {
	if depth >= maxMCPTextEnvelopeDepth {
		return "", mcpTextUnrecognized
	}
	switch v := value.(type) {
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return "", mcpTextUnrecognized
		}
		if nested, state := extractMCPText(text, depth+1); state != mcpTextUnrecognized {
			return nested, state
		}
		return text, mcpTextFound
	case []any:
		texts := make([]string, 0, len(v))
		state := mcpTextUnrecognized
		for _, item := range v {
			text, itemState := extractMCPTextValue(item, depth+1)
			if itemState == mcpTextUnrecognized {
				continue
			}
			state = mcpTextFound
			if text != "" {
				texts = append(texts, text)
			}
		}
		if state != mcpTextUnrecognized {
			return strings.Join(texts, "\n\n"), state
		}
	case map[string]any:
		if content, ok := v["content"].([]any); ok {
			texts := make([]string, 0, len(content))
			for _, item := range content {
				block, ok := item.(map[string]any)
				if !ok {
					continue
				}
				text, _ := block["text"].(string)
				text = strings.TrimSpace(text)
				if text != "" {
					texts = append(texts, text)
				}
			}
			// Join with a blank line so each block stays a distinct Markdown
			// region. A content envelope is recognized even when no item has
			// usable text, preventing wrapper metadata from reaching the UI.
			if len(texts) == 0 {
				return "", mcpTextEmpty
			}
			return strings.Join(texts, "\n\n"), mcpTextFound
		}
		for _, key := range []string{"output", "result", "text", "content"} {
			if wrapped, ok := v[key]; ok {
				if extracted, state := extractMCPTextValue(wrapped, depth+1); state != mcpTextUnrecognized {
					return extracted, state
				}
			}
		}
	}
	return "", mcpTextUnrecognized
}

// mcpToolOutputBody renders the human-facing result of a completed MCP tool
// call: stdout/result-preview sections when present, otherwise the raw payload
// captured under the "output:" section (unwrapped from its JSON envelope so it
// reads as plain text rather than an escaped blob). Long bodies are clipped with
// a click-to-expand hint by the caller.
func mcpToolOutputBody(content string) string {
	text, _ := mcpToolOutputBodyResult(content)
	return text
}

func mcpToolOutputBodyResult(content string) (string, bool) {
	if extracted, state := extractMCPText(content, 0); state != mcpTextUnrecognized {
		return extracted, true
	}

	fallback := ""
	for _, output := range []string{visibleToolOutput(content), mcpOutputSectionBody(content)} {
		output = strings.TrimSpace(output)
		if output == "" {
			continue
		}
		if extracted, state := extractMCPText(output, 0); state != mcpTextUnrecognized {
			return extracted, true
		}
		if fallback == "" {
			fallback = extractMCPToolOutputText(output)
		}
	}
	if fallback != "" {
		return fallback, false
	}
	return compactPlainLine(content), false
}

func mcpOutputSectionBody(content string) string {
	idx := strings.Index(strings.ToLower(content), "output:")
	if idx < 0 {
		return ""
	}
	rest := content[idx+len("output:"):]
	fence := strings.Index(rest, "```")
	if fence < 0 {
		return ""
	}
	rest = rest[fence+3:]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		if lang := strings.TrimSpace(rest[:nl]); lang != "" && !strings.ContainsAny(lang, "{[\"") {
			rest = rest[nl+1:] // drop the ```json language tag line
		}
	}
	end := strings.LastIndex(rest, "```")
	if end < 0 {
		return ""
	}
	raw := strings.TrimSpace(rest[:end])
	if raw == "" {
		return ""
	}
	return raw
}

func visibleToolOutput(content string) string {
	sections := toolOutputSections(content)
	if len(sections) > 0 {
		return stripToolOutputInlineNotes(strings.Join(sections, "\n"))
	}
	return stripToolOutputInlineNotes(stripToolMetadataSections(content))
}

func isReadLikeTool(title string) bool {
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "read", "read_file":
		return true
	}
	return false
}

func isDiffTool(title string) bool {
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "edit_file", "edit", "write_file", "write":
		return true
	}
	return false
}

func isMarkdownBodyTool(title string) bool {
	if isMemoryQueryTool(title) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "web_fetch", "webfetch", "web_search", "websearch", "retrieve_output":
		return true
	}
	return false
}

// webFetchMarkdownBody preserves newly formatted Markdown verbatim and also
// repairs legacy completion cards that still contain the old generic
// `output:` JSON envelope.
func webFetchMarkdownBody(content string) string {
	trimmed := strings.TrimSpace(content)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "output:") && strings.Contains(lower, "```json") {
		if body, recognized := tool.FormatWebFetchResult(trimmed); recognized {
			return body
		}
		return "Web fetch completed, but its content could not be displayed."
	}
	return content
}

// isMemoryQueryTool reports whether a tool reads the memory store and therefore
// renders a Markdown result card (memories_add_ad_hoc_note writes, and has its
// own note-body path).
func isMemoryQueryTool(title string) bool {
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "memories_list", "memories_read", "memories_search":
		return true
	}
	return false
}

// memoryToolOutputBody guards the memory card against transport data: a payload
// that never went through agentnotify (a legacy or malformed row) must show a
// neutral line rather than raw JSON.
func memoryToolOutputBody(content string) string {
	if isToolOutputJSON(content) || strings.Contains(strings.ToLower(content), "```json") {
		return "Memory results could not be displayed."
	}
	return content
}

// readFileContentLineCount counts the lines of file content inside the code fence
// of read_file tool output. The output format is:
//
//	path: `/path/to/file`
//
//	```lang
//	<content lines>
//	```
func readFileContentLineCount(content string) int {
	body := extractAnyFencedBody(content)
	if body == "" {
		return 0
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return 0
	}
	return strings.Count(body, "\n") + 1
}

func toolOutputSections(content string) []string {
	lines := strings.Split(strings.ReplaceAll(content, "\r", ""), "\n")
	out := []string{}
	for i := 0; i < len(lines); i++ {
		label := strings.TrimSpace(strings.ToLower(lines[i]))
		if label != "stdout:" && label != "stderr:" && label != "stdout preview:" && label != "result preview:" {
			continue
		}
		if section := extractFencedSection(lines, &i); section != "" {
			out = append(out, section)
		}
	}
	return out
}

func extractFencedSection(lines []string, idx *int) string {
	i := *idx + 1
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i >= len(lines) || !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
		return ""
	}
	i++
	body := []string{}
	for i < len(lines) {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
			*idx = i
			return trimBlankFenceEdges(body)
		}
		body = append(body, lines[i])
		i++
	}
	return trimBlankFenceEdges(body)
}

func stripToolMetadataSections(content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r", ""), "\n")
	out := []string{}
	skipNextFence := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if trimmed == "" {
			continue
		}
		if skipNextFence {
			if strings.HasPrefix(trimmed, "```") {
				for i+1 < len(lines) {
					i++
					if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
						break
					}
				}
			}
			skipNextFence = false
			continue
		}
		switch {
		case strings.HasPrefix(lower, "tool:"),
			strings.HasPrefix(lower, "status:"),
			strings.HasPrefix(lower, "purpose:"),
			strings.HasPrefix(lower, "command:"),
			strings.HasPrefix(lower, "invocation:"),
			strings.HasPrefix(lower, "input:"),
			strings.HasPrefix(lower, "output:"),
			strings.HasPrefix(lower, "path:"),
			strings.HasPrefix(lower, "result preview:"),
			strings.HasPrefix(lower, "stdout preview:"):
			if lower == "input:" || lower == "output:" {
				skipNextFence = true
			}
			continue
		case strings.HasPrefix(trimmed, "```"):
			fenced := []string{}
			for i+1 < len(lines) {
				i++
				if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
					break
				}
				fenced = append(fenced, lines[i])
			}
			if body := trimBlankFenceEdges(fenced); body != "" {
				out = append(out, body)
			}
			continue
		case strings.HasPrefix(trimmed, "**") && strings.HasSuffix(trimmed, "**"):
			continue
		}
		out = append(out, line)
	}
	// TrimRight preserves leading indentation on the first content line while
	// still removing trailing blank lines.
	return strings.TrimRight(strings.Join(out, "\n"), "\n\t\r ")
}

func stripToolOutputInlineNotes(content string) string {
	if strings.TrimSpace(content) == "" {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r", ""), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if trimmed == "" {
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(lower, "preview truncated:") {
			continue
		}
		if strings.HasPrefix(lower, "[tool output truncated for context:") {
			continue
		}
		out = append(out, line)
	}
	joined := strings.Join(out, "\n")
	return trimBlankFenceEdges(strings.Split(joined, "\n"))
}

func trimBlankFenceEdges(lines []string) string {
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	end := len(lines)
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	if start >= end {
		return ""
	}
	return strings.Join(lines[start:end], "\n")
}

func compactPlainLine(content string) string {
	inFence := false
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence || line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if lower == "input:" || lower == "output:" || lower == "stdout:" ||
			lower == "stderr:" || lower == "stdout preview:" || lower == "result preview:" {
			continue
		}
		if strings.HasPrefix(line, "**") && strings.HasSuffix(line, "**") {
			continue
		}
		return cleanInlineDetail(line)
	}
	return ""
}

func cleanInlineDetail(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "`")
	s = strings.TrimSpace(strings.Trim(s, "`"))
	s = strings.TrimPrefix(s, "**")
	s = strings.TrimSuffix(s, "**")
	return strings.TrimSpace(s)
}

func (r *Renderer) renderUserMessage(content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	termWidth := termWidthOrDefault()

	content = wrapCardContent(content, termWidth-lipgloss.Width(composerPromptMarker)-1)
	lines := strings.Split(content, "\n")
	_, _ = fmt.Fprint(r.out, renderSharedBlockBorderLine(termWidth))
	for i, line := range lines {
		if i == 0 {
			r.writeSharedBlockPromptLine(line, termWidth)
		} else {
			r.writeSharedBlockContinuationLine(line, termWidth)
		}
	}
	_, _ = fmt.Fprint(r.out, renderSharedBlockBorderLine(termWidth))
	// Blank spacer below the user message so the activity area that follows is
	// not glued to the card. Default-background row = real whitespace.
	_, _ = fmt.Fprint(r.out, "\x1b[0m\x1b[2K\r\n")
}

// NoteSubagentSpawned records what kind of agent a subagent is, so its own view
// can be titled. The surface calls it from the same spawn notification that adds
// the roster row, because the row is dropped the moment the subagent finishes
// while the view it opened stays readable.
func (r *Renderer) NoteSubagentSpawned(agentID, agentType string) {
	agentID = strings.TrimSpace(agentID)
	agentType = strings.TrimSpace(agentType)
	if r == nil || agentID == "" || agentType == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.subagentTypes == nil {
		r.subagentTypes = make(map[string]string)
	}
	r.subagentTypes[agentID] = agentType
}

// SetSubagentModels declares which subagent types run on a model of their own.
// The map mirrors the runtime's per-type client map: a type is present only
// when agents.definitions[<type>].llm_providers gives it a chain, and an absent
// type runs on the primary agent's model.
func (r *Renderer) SetSubagentModels(models map[string]ComposerFooter) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subagentModels = models
}

func (r *Renderer) SetComposerFooter(footer ComposerFooter) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.footer = footer
}

// SetPlanMode says whether the conversation is in Plan mode, which the footer
// shows for as long as it lasts.
func (r *Renderer) SetPlanMode(on bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.planMode = on
}

// SetAutoContinueNotice sets the warning line drawn under the composer while a
// conversation stopped by a usage limit waits to continue by itself; "" removes
// it. The caller repaints the composer.
func (r *Renderer) SetAutoContinueNotice(text string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.autoContinueNotice = strings.TrimSpace(text)
}

// autoContinueNoticeLineLocked renders the auto-continue notice for a
// termWidth-wide row, or "" when there is none. Caller holds r.mu.
func (r *Renderer) autoContinueNoticeLineLocked(termWidth int) string {
	if r.autoContinueNotice == "" || r.activeView != "" {
		return ""
	}
	width := termWidth - sharedBlockFooterTruncateExtraRoom
	if width <= 0 {
		return ""
	}
	return autoContinueNoticeStyle + runewidth.Truncate(autoContinueNoticeGlyph+r.autoContinueNotice, width, "…") + sharedBlockReset
}

// planModeFooterLabel is the footer's Plan-mode segment, which also says how
// to leave it.
const planModeFooterLabel = "plan mode (shift+tab to exit)"

// SetComposerTokenStats updates the live token figures shown on the right side
// of the composer footer and re-renders the composer if it is active.
func (r *Renderer) SetComposerTokenStats(stats ComposerTokenStats) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.composerTokens = stats
	r.mu.Unlock()
}

// ComposerTokenStats returns a snapshot of the current composer token stats.
func (r *Renderer) ComposerTokenStats() ComposerTokenStats {
	if r == nil {
		return ComposerTokenStats{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.composerTokens
}

// RefreshComposerTokenUsage updates only the in/out figures of the composer
// footer, preserving the Active flag and PercentLeft captured from the last
// budget update. The footer's in/out must stay in lockstep with the live
// "Working"/"Worked for" line, which refreshes on every token-usage delta; the
// budget message that carries PercentLeft fires less often, so refreshing in/out
// only when it arrives left the footer trailing the working line by many deltas.
// No-op when the footer is not active, so it never lights up a blank footer.
func (r *Renderer) RefreshComposerTokenUsage(inputTokens, outputTokens int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if !r.composerTokens.Active {
		r.mu.Unlock()
		return
	}
	r.composerTokens.InputTokens = inputTokens
	r.composerTokens.OutputTokens = outputTokens
	r.mu.Unlock()
}

func formatComposerTokenStats(stats ComposerTokenStats) string {
	if !stats.Active {
		return ""
	}
	// Token in/out figures are tracked but no longer painted; the footer
	// reports only the auto-compact budget and the context window.
	parts := make([]string, 0, 2)
	if stats.PercentLeft > 0 {
		parts = append(parts, fmt.Sprintf("%d%%", stats.PercentLeft))
	} else {
		parts = append(parts, "compact pending")
	}
	if stats.ContextWindow > 0 {
		parts = append(parts, formatTokensCompact(stats.ContextWindow))
	}
	return strings.Join(parts, "/")
}

func layoutComposerFooter(left, right string, maxWidth int) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if maxWidth <= 0 {
		return ""
	}
	if right == "" {
		return runewidth.Truncate(left, maxWidth, "…")
	}
	if left == "" {
		return runewidth.Truncate(right, maxWidth, "…")
	}
	leftWidth := runewidth.StringWidth(left)
	rightWidth := runewidth.StringWidth(right)
	if leftWidth+3+rightWidth <= maxWidth {
		return left + strings.Repeat(" ", maxWidth-leftWidth-rightWidth) + right
	}
	if rightWidth+3 >= maxWidth {
		return runewidth.Truncate(right, maxWidth, "…")
	}
	return runewidth.Truncate(left, maxWidth-rightWidth-1, "…") + " " + right
}

// ComposerRenderState bundles all data the renderer needs to paint the
// composer area including the non-modal slash overlay rows below it.
type ComposerRenderState struct {
	Text           string
	Cursor         *int
	PendingInput   ComposerPendingInputPreview
	AgentRoster    AgentRosterSnapshot
	RosterSelected int
	// RosterFocused is true once the user has moved keyboard focus onto the
	// roster panel via Down. Only the focused row gets the per-kind hint text.
	RosterFocused bool
	// SlashMenu is the slash menu open under the composer, nil when none is.
	SlashMenu *slashPanel
	// OverlayRows is the @ mention menu under the composer.
	OverlayRows  []OverlayRow
	ArgumentHint string
}

// ComposerPendingInputPreview mirrors the active-run input queues shown above
// the composer while a turn is still running.
type ComposerPendingInputPreview struct {
	PendingSteers  []string
	RejectedSteers []string
	QueuedMessages []string
}

// OverlayRow is one row of the composer's @ mention menu under the input
// line. Lead is the row's name column, already
// padded to the menu's alignment; Text is what the row says, wrapped under
// itself at the terminal width and never cut short.
type OverlayRow struct {
	Lead     string
	Text     string
	Selected bool
}

// composerMenuRows is how many screen lines the composer's inline menu takes.
const composerMenuRows = 8

// composerMenuLines lays out the composer's inline menu at the terminal width:
// every row whole, within composerMenuRows screen lines around the selected
// row, padded so the menu keeps its height while the list narrows.
func (r *Renderer) composerMenuLines(rows []OverlayRow, termWidth int) []string {
	rendered := make([][]string, len(rows))
	heights := make([]int, len(rows))
	selected := 0
	for i, row := range rows {
		rendered[i] = composerMenuRowLines(row, termWidth)
		heights[i] = len(rendered[i])
		if row.Selected {
			selected = i
		}
	}
	start, end := menuWindowAround(heights, selected, composerMenuRows)
	lines := make([]string, 0, composerMenuRows)
	for _, row := range rendered[start:end] {
		lines = append(lines, row...)
	}
	for len(lines) < composerMenuRows {
		lines = append(lines, "")
	}
	return lines
}

// composerMenuRowLines renders one inline-menu row: indented, its lead, then
// its text wrapped one column short of the terminal width with continuation
// lines hanging under the text.
func composerMenuRowLines(row OverlayRow, termWidth int) []string {
	const indent = "  "
	leadWidth := runewidth.StringWidth(row.Lead)
	parts := wrapMenuText(row.Text, termWidth-1-len(indent)-leadWidth)
	lines := make([]string, len(parts))
	for i, part := range parts {
		lead := row.Lead
		if i > 0 {
			lead = strings.Repeat(" ", leadWidth)
		}
		line := indent + lead + part
		if row.Selected {
			line = "\x1b[48;5;240;38;5;110m" + line + sharedBlockReset
		}
		lines[i] = line
	}
	return lines
}

// BeginComposerOverlay is the entry for modal overlays that render IN the
// viewport surface (tool approval, /model picker) instead of taking over the
// whole screen. The transcript stays visible above and the overlay content is
// painted as a bottom-pinned block (with a separator rule) via
// SetOverlayComposer, so the main surface's mouse text-selection applies to
// overlay rows too. In non-viewport mode the composer is hidden while the
// overlay renders directly. Pairs with EndOverlay.
func (r *Renderer) BeginComposerOverlay() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishTransientLocked("")
	r.overlayActive = true
	// cursorRow must be -1 (not the zero value 0) so that a racing
	// paintViewportLocked (triggered by an agent frame arriving between
	// BeginComposerOverlay and the first SetOverlayComposer) hides the
	// hardware cursor instead of showing it at an off-screen row that the
	// terminal clamps onto a visible option. The real cursorRow is set
	// moments later by SetOverlayComposer.
	r.overlayComposer = composerBlock{cursorRow: -1}
	r.overlayPanel = nil
	r.overlayScrollOffset = 0
	r.overlaySoftWrap = false
	r.overlayFocusRow, r.overlayFocusPending = -1, false
}

// IsViewportMode reports whether the renderer is in the interactive alt-screen
// viewport mode (real terminal).
func (r *Renderer) IsViewportMode() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.viewportMode
}

// SetOverlayComposer installs modal overlay lines to display as the bottom
// composer block while a composer overlay is active in viewport mode. The
// transcript above is preserved; a separator rule is prepended at paint time
// to delimit the overlay from the conversation. The block becomes the viewport
// selection source (vpLastComposer), so drag-to-select + release-to-copy works
// on overlay text with the same semantics as the main turn. cursorRow/Col
// position the hardware cursor within the block (0-based, excluding the
// separator); pass cursorRow < 0 to hide the cursor.
func (r *Renderer) SetOverlayComposer(lines []string, cursorRow, cursorCol int) {
	r.setOverlayComposer(lines, cursorRow, cursorCol, false, false)
}

// SetOverlayComposerAtEnd installs overlay lines and pins the overlay's scroll
// offset to the end of the content in the same paint. An overlay taller than
// its area keeps its decision rows — the option list and footer, which sit at
// the bottom of the block — on screen even when the body above them (a full
// file diff, say) is far taller than the overlay area. Callers use it for the
// renders that follow a decision key and SetOverlayComposer for the renders
// that follow a scroll key, so a deliberate scroll up through the body is not
// undone by the repaint that follows it.
func (r *Renderer) SetOverlayComposerAtEnd(lines []string, cursorRow, cursorCol int) {
	r.setOverlayComposer(lines, cursorRow, cursorCol, false, true)
}

// SetSoftWrappingOverlayComposer installs logical modal-overlay lines that are
// reflowed to the current viewport width on every paint. This is intended for
// user_interaction, whose prompts and option descriptions are arbitrary model
// text. Keeping the unwrapped source here also makes terminal resize reflow the
// overlay without waiting for another key event.
func (r *Renderer) SetSoftWrappingOverlayComposer(lines []string, cursorRow, cursorCol int) {
	r.setOverlayComposer(lines, cursorRow, cursorCol, true, false)
}

func (r *Renderer) setOverlayComposer(lines []string, cursorRow, cursorCol int, softWrap, scrollToEnd bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.overlayActive = true
	// Strip stray BEL bytes from overlay content so modal text (which can come
	// from tool/user input) does not ring the terminal bell.
	clean := make([]string, len(lines))
	for i, line := range lines {
		clean[i] = stripTerminalBells(line)
	}
	r.overlayComposer = composerBlock{lines: clean, cursorRow: cursorRow, cursorCol: cursorCol}
	r.overlayPanel = nil
	r.overlaySoftWrap = softWrap
	r.overlayFocusRow, r.overlayFocusPending = -1, false
	if scrollToEnd {
		// Computed from the lines just installed, so the offset the paint
		// below uses is already the bottom of the new content.
		r.overlayScrollOffset = r.overlayMaxScrollLocked()
	}
	r.paintViewportLocked()
}

// SetOverlayPanel shows a slash panel in the overlay area. The panel is laid
// out at every paint, so it reflows on resize and scrolls its body within
// slashPanelHeight rows. top shows the body from its first row — a panel just
// opened or a page just switched. The focus is brought into view when it
// moved, or on every install when follow is set: a picker re-installs only on
// a key, which always wants the selection shown, while a panel also repaints
// on ticks, which must not undo a wheel scroll over an unchanged selection.
func (r *Renderer) SetOverlayPanel(p slashPanel, top, follow bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.overlayActive = true
	r.overlayComposer = composerBlock{cursorRow: -1}
	r.overlayPanel = &p
	r.overlaySoftWrap = false
	if top {
		r.overlayScrollOffset = 0
	}
	focus := -1
	if p.focusEnd > p.focusStart {
		focus = p.focusStart
	}
	if follow || top || focus != r.overlayFocusRow {
		r.overlayFocusPending = focus >= 0
	}
	r.overlayFocusRow = focus
	r.paintViewportLocked()
}

// SetOverlayNote sets the line the renderer shows under whatever overlay is
// open (empty clears it) and repaints when an overlay is on screen.
func (r *Renderer) SetOverlayNote(note string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.overlayNote == note {
		return
	}
	r.overlayNote = note
	if r.overlayActive {
		r.paintViewportLocked()
	}
}

// ResetOverlayScroll clears any stale scroll offset left by a previous modal
// overlay so the next freshly-opened overlay starts from the top.
func (r *Renderer) ResetOverlayScroll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.overlayScrollOffset = 0
}

// EndOverlay re-enables composer rendering after a modal overlay closes. The
// composer repaints lazily on the next status tick / input render, so any text
// the overlay flow prints afterward (e.g. an approval confirmation) lands above
// the restored composer rather than under it. In viewport mode it repaints the
// whole retained transcript + composer, restoring the main turn.
func (r *Renderer) EndOverlay() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.composerSuppressed = false
	r.overlayActive = false
	r.overlayComposer = composerBlock{cursorRow: -1}
	r.overlayPanel = nil
	r.overlaySoftWrap = false
	r.overlayFocusRow, r.overlayFocusPending = -1, false
	r.paintViewportLocked()
}

// ForceRepaint is the user's one-shot "repaint everything" (Ctrl+L): it drops
// the paint shadow, the painter's model of the physical screen, and repaints
// every row, re-aligning the model with reality after anything wrote to the
// terminal behind the painter's back. It is a user-triggered operation, not
// part of the per-frame path, so damage tracking's change-only repaints are
// untouched.
func (r *Renderer) ForceRepaint() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dropPaintShadowLocked()
	r.paintViewportLocked()
}

// RenderComposerState renders the composer with optional overlay rows below it.
func (r *Renderer) RenderComposerState(cs ComposerRenderState) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.composerState = cs
	if !r.transientActive {
		r.finishTransientLocked("")
	}
	r.paintViewportLocked()
}

type composerLayout struct {
	lines            []string
	cursorRowFromTop int
	cursorCol        int
	// spans is the byte range of the text each visual line shows, in the same
	// order as lines. The cursor position is derived from it, and so is the
	// reverse mapping a mouse click needs to turn a screen cell back into a
	// caret position in the composer buffer.
	spans []visualLineSpan
}

const composerPendingInputPreviewLineLimit = 3

func normalComposerContentWidth(termWidth int) int {
	prefixWidth := maxInt(lipgloss.Width(composerPromptMarker), lipgloss.Width("  "))
	// Leave an extra safety column beyond the explicit prompt/indent width so
	// terminals never hit the last printable cell and trigger hardware autowrap
	// before tui's own soft-wrap layout takes over.
	contentWidth := termWidth - prefixWidth - 2
	if contentWidth < 1 {
		contentWidth = 1
	}
	return contentWidth
}

func normalComposerLinePrefixWidth(row int) int {
	if row <= 0 {
		return lipgloss.Width(composerPromptMarker)
	}
	return lipgloss.Width("  ")
}

func layoutNormalComposer(text string, cursor *int, termWidth int) composerLayout {
	contentWidth := normalComposerContentWidth(termWidth)
	// Wrap the full text once, tracking each visual line's byte-offset span.
	// The cursor position is derived from these spans (not by re-wrapping the
	// text prefix) so that a caret sitting exactly at a soft-wrap break point -
	// right after the space consumed between two visual lines - is placed on the
	// next line instead of drifting into the trailing blank of the previous one.
	spans := wrapCardContentSpans(text, contentWidth)
	lines := make([]string, len(spans))
	for i, s := range spans {
		lines[i] = s.content
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	runeCount := utf8.RuneCountInString(text)
	cursorRunes := runeCount
	if cursor != nil {
		cursorRunes = *cursor
		if cursorRunes < 0 {
			cursorRunes = 0
		}
		if cursorRunes > runeCount {
			cursorRunes = runeCount
		}
	}
	cursorRow, contentCol := composerCursorVisualPos(text, cursorRunes, spans)
	cursorCol := contentCol + normalComposerLinePrefixWidth(cursorRow)
	return composerLayout{
		lines:            lines,
		cursorRowFromTop: cursorRow,
		cursorCol:        cursorCol,
		spans:            spans,
	}
}

func composerPreviewVisible(p ComposerPendingInputPreview) bool {
	return len(p.PendingSteers) > 0 || len(p.RejectedSteers) > 0 || len(p.QueuedMessages) > 0
}

// composerPreviewHasEditableQueuedInput gates the "edit last queued message"
// hint. Pending steers count: they stay retractable from the run's input runtime
// until it drains them at a tool boundary, and the preview drops them the moment
// that happens.
func composerPreviewHasEditableQueuedInput(p ComposerPendingInputPreview) bool {
	return len(p.QueuedMessages) > 0 || len(p.PendingSteers) > 0 || len(p.RejectedSteers) > 0
}

func formatComposerPendingInputPreview(p ComposerPendingInputPreview, termWidth int) []string {
	if !composerPreviewVisible(p) || termWidth < 4 {
		return nil
	}
	lines := []string{}
	appendBlankBetweenSections := func() {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
	}
	appendWrapped := func(text string, initialIndent string, subsequentIndent string, limit int, italic bool) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		width := termWidth - lipgloss.Width(initialIndent) - 1
		if width < 1 {
			width = 1
		}
		wrapped := strings.Split(wrapCardContent(text, width), "\n")
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		for i, line := range wrapped {
			if i >= limit {
				lines = append(lines, dimComposerPreviewLine(subsequentIndent+"…", false, termWidth))
				return
			}
			indent := initialIndent
			if i > 0 {
				indent = subsequentIndent
			}
			lines = append(lines, dimComposerPreviewLine(indent+line, italic, termWidth))
		}
	}
	appendHeader := func(header string) {
		lines = append(lines, formatComposerPreviewHeader(header, termWidth))
	}
	appendPendingHeader := func() {
		title := "Messages to be submitted after next tool call"
		hint := " (press esc to interrupt and send immediately)"
		firstLineMax := termWidth - lipgloss.Width("• ") - 1
		if firstLineMax < 1 {
			firstLineMax = 1
		}
		if lipgloss.Width(title+hint) <= firstLineMax {
			lines = append(lines, formatComposerPreviewPendingHeader(title, hint, termWidth))
			return
		}
		lines = append(lines, formatComposerPreviewHeader(title, termWidth))
		hintText := strings.TrimSpace(hint)
		width := termWidth - lipgloss.Width("  ") - 1
		if width < 1 {
			width = 1
		}
		for _, line := range strings.Split(wrapCardContent(hintText, width), "\n") {
			lines = append(lines, dimComposerPreviewLine("  "+line, false, termWidth))
		}
	}

	if len(p.PendingSteers) > 0 {
		appendPendingHeader()
		for _, steer := range p.PendingSteers {
			appendWrapped(steer, "  ↳ ", "    ", composerPendingInputPreviewLineLimit, false)
		}
	}
	// Rejected steers share this section instead of getting their own. They are
	// follow-ups by then — a steer that missed its window and now runs after the
	// turn like everything else here. The only thing still separating them is
	// that they run first, which is exactly the order they are listed in. A
	// second header would name an internal failure state the user never chose
	// and cannot act on differently.
	if queued := append(append([]string{}, p.RejectedSteers...), p.QueuedMessages...); len(queued) > 0 {
		appendBlankBetweenSections()
		appendHeader("Queued follow-up inputs")
		for _, message := range queued {
			appendWrapped(message, "  ↳ ", "    ", composerPendingInputPreviewLineLimit, true)
		}
	}
	// Indented to the section body rather than to a message row: the shortcuts
	// act on the queue as a whole, not on the last message listed above them.
	for _, line := range composerPreviewShortcutLines(composerQueueShortcutHints(p), "  ", termWidth) {
		lines = append(lines, dimComposerPreviewLine(line, false, termWidth))
	}
	return lines
}

const composerQueueShortcutSeparator = " · "

// composerQueueShortcutHints lists the queue actions reachable from the composer
// while a run is working, so the queue is operable without the user having to
// already know the keys.
//
// Recall is the whole story: it takes the message out of the queue and puts it
// in the composer, so it has already been cancelled by the time the user decides
// what to do with the draft. There is no separate cancel key to document, and
// esc belongs to pending steers alone — that section's header already says so.
func composerQueueShortcutHints(p ComposerPendingInputPreview) []string {
	hints := make([]string, 0, 2)
	if composerPreviewHasEditableQueuedInput(p) {
		hints = append(hints, "⇧+← recall last queued")
	}
	return append(hints, "tab queue follow-up")
}

// composerPreviewShortcutLines packs hints onto as few rows as fit the terminal.
// Wrapping between whole hints keeps a narrow terminal from truncating one
// mid-key ("⇧+…"), which would leave the shortcut unreadable.
func composerPreviewShortcutLines(hints []string, indent string, termWidth int) []string {
	if len(hints) == 0 {
		return nil
	}
	// displayLineWidth, not lipgloss.Width: the modifier glyphs (⌥ ⇧ ↑ ← ·) are
	// East Asian ambiguous-width, and the row is truncated downstream by
	// runewidth. Measuring with anything else overflows and loses the last key.
	width := termWidth - displayLineWidth(indent) - 1
	if width < 1 {
		width = 1
	}
	var lines []string
	flush := func(row string) {
		if row == "" {
			return
		}
		// A single hint can still outgrow a narrow terminal on its own. Wrap it
		// across rows rather than letting the row truncate, which would cut off
		// the very key the hint documents.
		for _, part := range strings.Split(wrapCardContent(row, width), "\n") {
			lines = append(lines, indent+part)
		}
	}
	row := ""
	for _, hint := range hints {
		if row == "" {
			row = hint
			continue
		}
		if candidate := row + composerQueueShortcutSeparator + hint; displayLineWidth(candidate) <= width {
			row = candidate
			continue
		}
		flush(row)
		row = hint
	}
	flush(row)
	return lines
}

func formatComposerPreviewHeader(header string, termWidth int) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	width := termWidth - lipgloss.Width("• ") - 1
	if width < 1 {
		width = 1
	}
	header = strings.ReplaceAll(strings.ReplaceAll(header, "\r\n", " "), "\n", " ")
	line := runewidth.Truncate(header, width, "…")
	return sharedBlockReset + "\x1b[2m• " + sharedBlockReset + line + "\x1b[K" + sharedBlockReset
}

func formatComposerPreviewPendingHeader(title string, hint string, termWidth int) string {
	// The row ends in \x1b[K, which is only a plain clear while the content
	// stays short of the terminal width; an unbounded title would fill the
	// row, park the cursor in the deferred-wrap cell, and the \x1b[K would
	// erase the last painted character instead. Bound the title by the width
	// the marker and the hint leave free.
	hint = strings.TrimSpace(hint)
	title = strings.TrimSpace(title)
	budget := termWidth - lipgloss.Width("• ") - lipgloss.Width(hint) - 1
	if budget < 1 {
		hint = ""
		budget = termWidth - lipgloss.Width("• ") - 1
	}
	if budget < 1 {
		budget = 1
	}
	title = runewidth.Truncate(title, budget, "…")
	return sharedBlockReset + "\x1b[2m• " + sharedBlockReset + title + "\x1b[2m" + hint + "\x1b[K" + sharedBlockReset
}

func dimComposerPreviewLine(text string, italic bool, termWidth int) string {
	if termWidth > 0 {
		text = runewidth.Truncate(text, termWidth-1, "…")
	}
	style := "\x1b[2m"
	if italic {
		style += "\x1b[3m"
	}
	return sharedBlockReset + style + text + "\x1b[K" + sharedBlockReset
}

func (r *Renderer) renderComposerStatusLine(text string, termWidth int) string {
	// One column short of the full width, like every other row of this block:
	// the line must never reach the terminal's autowrap threshold and spill
	// onto the spacer row below it, where nothing would ever erase it. The
	// whitespace fold is the same single-line rule the plan labels follow: a
	// status line is one line by definition, and a tab or newline slipping
	// through would spill exactly the same way.
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	text = runewidth.Truncate(text, termWidth-1, "…")
	return sharedBlockReset + "\x1b[2m" + text + "\x1b[K" + sharedBlockReset
}

// sharedCardPad returns the spaces that carry a shared-card row up to the full
// terminal width, emitted while the card background is the active SGR so every
// cell to the right edge is painted explicitly. The fill must be explicit
// because \x1b[K cannot do this job portably: terminals without
// back-color-erase (macOS Terminal.app) erase to the default background and
// leave a gap at the card's right edge.
func sharedCardPad(row string, termWidth int) string {
	return strings.Repeat(" ", max(termWidth-lipgloss.Width(row), 0))
}

// renderSharedBlockBorderLine returns the horizontal rule painted at the top
// and bottom of the composer / user-message card. The rule shares the card's
// own background (bg 238). The visible ─ glyphs are one column shorter than
// the card so they never reach the terminal's autowrap threshold; the final
// column is one explicit space on the card background rather than an \x1b[K
// fill, which would depend on back-color-erase (absent on macOS Terminal.app)
// and leave the edge unpainted. The rule color matches the forebrain banner logo
// (#5DADE2 dark / #0d6e9c light) so the card frame visually ties back to the
// startup banner.
func renderSharedBlockBorderLine(width int) string {
	if width < 2 {
		width = 1
	} else {
		width = width - 1
	}
	return sharedBlockInputStyle + "\x1b[38;5;245m" + strings.Repeat("─", width) + " " + sharedBlockReset + "\r\n"
}

func renderSharedBlockPromptLine(content string, termWidth int) string {
	row := composerPromptStyle + composerPromptMarker + sharedBlockTextStyle + content
	return row + sharedBlockTextStyle + sharedCardPad(row, termWidth) + sharedBlockReset + "\r\n"
}

func renderSharedBlockContinuationLine(content string, termWidth int) string {
	row := sharedBlockTextStyle + "  " + content
	return row + sharedCardPad(row, termWidth) + sharedBlockReset + "\r\n"
}

func (r *Renderer) writeSharedBlockPromptLine(content string, termWidth int) {
	if r == nil {
		return
	}
	_, _ = fmt.Fprint(r.out, renderSharedBlockPromptLine(content, termWidth))
}

func (r *Renderer) writeSharedBlockContinuationLine(content string, termWidth int) {
	if r == nil {
		return
	}
	_, _ = fmt.Fprint(r.out, renderSharedBlockContinuationLine(content, termWidth))
}
func maxCardContentWidth() int {
	width := termWidthOrDefault() - cardHorizontalChromeWidth
	if width < 1 {
		return 1
	}
	return width
}

func wrapCardContent(content string, maxWidth int) string {
	if maxWidth < 1 {
		return content
	}
	lines := strings.Split(content, "\n")
	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		wrapped = append(wrapped, wrapCardLine(line, maxWidth)...)
	}
	return strings.Join(wrapped, "\n")
}

// displayLineWidth is the visible column width of s: ANSI SGR escapes stripped,
// wide runes (em-dash, middot, CJK) counted via runewidth — i.e. the width the
// terminal actually occupies. lipgloss.Width undercounts these (em-dash and
// middot as 1), which the rest of the renderer never does; using runewidth here
// keeps wrap math consistent with the terminal and with cursor/footer math.
func displayLineWidth(s string) int {
	return runewidth.StringWidth(sgrPattern.ReplaceAllString(s, ""))
}

func wrapCardLine(line string, maxWidth int) []string {
	if maxWidth < 1 || line == "" {
		return []string{line}
	}
	// Break the line using wrapCardLineIndex, which walks runes exactly once
	// per call, instead of calling displayLineWidth on the whole remaining
	// suffix inside the loop. displayLineWidth runs an ANSI-stripping regex
	// plus runewidth over the ENTIRE string, so the previous loop was O(n^2):
	// for a multi-KB line (e.g. a replayed block whose fold header carried a
	// huge summary) it re-scanned the shrinking suffix on every iteration,
	// pinning the renderer lock for seconds and freezing the TUI.
	//
	// wrapCardLineIndex returns len(line) when the whole line fits within
	// maxWidth, so it doubles as the fit check; the total work across all
	// iterations is O(n).
	remaining := line
	var wrapped []string
	for {
		cut := wrapCardLineIndex(remaining, maxWidth)
		if cut >= len(remaining) {
			// Whole remaining fits: emit and done.
			wrapped = append(wrapped, remaining)
			return wrapped
		}
		if cut <= 0 {
			// No break point (e.g. a single rune wider than maxWidth at the
			// start). Emit the rest verbatim to avoid an infinite loop.
			wrapped = append(wrapped, remaining)
			return wrapped
		}
		segment := strings.TrimRightFunc(remaining[:cut], unicode.IsSpace)
		if segment == "" {
			segment = remaining[:cut]
		}
		wrapped = append(wrapped, segment)
		remaining = strings.TrimLeftFunc(remaining[cut:], unicode.IsSpace)
		if remaining == "" {
			return wrapped
		}
	}
}

// wrapCardLineIndex returns the byte offset at which to break line so the
// visible width of line[:offset] does not exceed maxWidth. It is ANSI-aware:
// SGR escape sequences contribute zero width and are never split, and visible
// width is measured with runewidth so wide runes are counted the way the
// terminal renders them.
//
// A line is broken between words: at whitespace, after a hyphen inside a word,
// or beside a wide (CJK) character, which is a word boundary of its own. A
// word that fits on a line is never split — it moves to the next line whole.
// Only a word wider than the whole line (a long path, a URL, an id) is broken
// inside itself, and since it has to be broken anyway it starts where it
// stands and fills the columns it is given.
func wrapCardLineIndex(line string, maxWidth int) int {
	if maxWidth < 1 {
		return len(line)
	}
	width := 0
	lastFit := 0
	// breakAt is the last offset a line may end at between two words, and
	// breakWidth the width of the text before it. seenText keeps a line's
	// indentation from counting as a word boundary: breaking there would leave
	// a row of nothing but spaces.
	breakAt, breakWidth := 0, 0
	seenText := false
	var prev2, prev rune
	i := 0
	for i < len(line) {
		if line[i] == 0x1b {
			// An SGR sequence (ESC [ ... m) has no visible width and is never
			// a break point; it is absorbed into the following segment.
			i = sgrSequenceEnd(line, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if seenText && wordBoundary(prev2, prev, r) {
			breakAt, breakWidth = lastFit, width
		}
		rw := runewidth.RuneWidth(r)
		if width+rw > maxWidth {
			switch {
			case lastFit == 0:
				return i + size
			case unicode.IsSpace(r):
				return lastFit
			case breakAt > 0 && !wordOverflows(line, i, width-breakWidth, prev2, prev, maxWidth):
				return breakAt
			default:
				return lastFit
			}
		}
		width += rw
		i += size
		lastFit = i
		if !unicode.IsSpace(r) {
			seenText = true
		}
		prev2, prev = prev, r
	}
	return len(line)
}

// wordOverflows reports whether the word that the rune at i continues is wider
// than maxWidth, given the used columns it already spans before i. It stops at
// the word's end, or as soon as the word has proved too wide, so the look ahead
// costs at most a line's width.
func wordOverflows(line string, i, used int, prev2, prev rune, maxWidth int) bool {
	first := true
	for i < len(line) {
		if line[i] == 0x1b {
			i = sgrSequenceEnd(line, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if !first && wordBoundary(prev2, prev, r) {
			return false
		}
		first = false
		used += runewidth.RuneWidth(r)
		if used > maxWidth {
			return true
		}
		i += size
		prev2, prev = prev, r
	}
	return false
}

// wordBoundary reports whether a line may break between prev and r, where
// prev2 is the rune before prev.
//
// Whitespace separates words. A hyphen between two letters or digits ends a
// word part ("read-" / "only"), so a line may break after it. A wide character
// is a word of its own — Chinese and Japanese are written without spaces — so
// a line may break on either side of one, except before punctuation that
// closes a phrase or after punctuation that opens one, which must stay with
// the character they belong to.
func wordBoundary(prev2, prev, r rune) bool {
	switch {
	case unicode.IsSpace(r) || unicode.IsSpace(prev):
		return true
	case prev == '-':
		return isLetterOrDigit(prev2) && isLetterOrDigit(r)
	case prev < utf8.RuneSelf && r < utf8.RuneSelf:
		return false
	case runewidth.RuneWidth(prev) == 2 || runewidth.RuneWidth(r) == 2:
		return !strings.ContainsRune(closingPunctuation, r) && !strings.ContainsRune(openingPunctuation, prev)
	}
	return false
}

// closingPunctuation may not begin a line, and openingPunctuation may not end
// one.
const (
	closingPunctuation = ",.;:!?)]}%，。、；：！？）」』》〉】〕”’…"
	openingPunctuation = "([{（「『《〈【〔“‘"
)

func isLetterOrDigit(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// sgrSequenceEnd returns the offset just past the SGR sequence (ESC [ ... m)
// that starts at i.
func sgrSequenceEnd(line string, i int) int {
	j := i + 1
	for j < len(line) && line[j] != 'm' {
		j++
	}
	if j < len(line) {
		j++ // include the terminating 'm'
	}
	return j
}

// visualLineSpan describes one soft-wrapped visual line produced by
// wrapCardContent: the byte-offset range of its VISIBLE content within the
// source string (excluding the break spaces consumed between lines) and the
// content itself. startByte/endByte let a cursor offset be mapped to the
// correct visual line even when it lands in the consumed break spaces.
type visualLineSpan struct {
	startByte int
	endByte   int
	content   string
}

// wrapCardLineSpans wraps a single hard line [lineStartByte, lineEndByte) of
// content the same way wrapCardLine does, but also records the byte-offset span
// of each visual line's visible content. It mirrors wrapCardLine +
// wrapCardLineIndex exactly so the spans stay byte-for-byte consistent with the
// displayed text.
func wrapCardLineSpans(content string, lineStartByte, lineEndByte, maxWidth int) []visualLineSpan {
	line := content[lineStartByte:lineEndByte]
	if maxWidth < 1 || line == "" {
		return []visualLineSpan{{startByte: lineStartByte, endByte: lineEndByte, content: line}}
	}
	var spans []visualLineSpan
	absStart := lineStartByte
	remaining := line
	for {
		cut := wrapCardLineIndex(remaining, maxWidth)
		if cut >= len(remaining) {
			spans = append(spans, visualLineSpan{startByte: absStart, endByte: absStart + len(remaining), content: remaining})
			return spans
		}
		if cut <= 0 {
			spans = append(spans, visualLineSpan{startByte: absStart, endByte: absStart + len(remaining), content: remaining})
			return spans
		}
		segment := strings.TrimRightFunc(remaining[:cut], unicode.IsSpace)
		segEnd := absStart + len(segment)
		if segment == "" {
			segment = remaining[:cut]
			segEnd = absStart + cut
		}
		spans = append(spans, visualLineSpan{startByte: absStart, endByte: segEnd, content: segment})
		trimmed := strings.TrimLeftFunc(remaining[cut:], unicode.IsSpace)
		consumed := len(remaining[cut:]) - len(trimmed)
		absStart += cut + consumed
		remaining = trimmed
		if remaining == "" {
			return spans
		}
	}
}

// wrapCardContentSpans wraps content the same way wrapCardContent does, but
// returns visualLineSpan entries so a cursor offset can be mapped to the
// correct visual line. Hard newlines split the content into independent hard
// lines (matching wrapCardContent); the consumed "\n" belongs to the preceding
// line.
func wrapCardContentSpans(content string, maxWidth int) []visualLineSpan {
	var spans []visualLineSpan
	startByte := 0
	for startByte <= len(content) {
		idx := strings.IndexByte(content[startByte:], '\n')
		var lineEnd int
		if idx < 0 {
			lineEnd = len(content)
		} else {
			lineEnd = startByte + idx
		}
		spans = append(spans, wrapCardLineSpans(content, startByte, lineEnd, maxWidth)...)
		if idx < 0 {
			break
		}
		startByte = lineEnd + 1 // skip the '\n'
	}
	if len(spans) == 0 {
		spans = []visualLineSpan{{startByte: 0, endByte: 0, content: ""}}
	}
	return spans
}

// composerCursorVisualPos maps a rune cursor offset to its (row, contentCol)
// position within the word-wrapped visual lines described by spans. contentCol
// is the display column within the visual line's content, excluding the line
// prefix (added by the caller). It is consistent with the wrap performed by
// wrapCardContent: a cursor landing in the break spaces consumed between two
// visual lines is clamped to the end of the earlier line's visible content, so
// the caret never sits in the trailing blank of a word-wrapped line.
func composerCursorVisualPos(text string, cursorRunes int, spans []visualLineSpan) (row, contentCol int) {
	if len(spans) == 0 {
		return 0, 0
	}
	// Convert the rune cursor offset to a byte offset in text.
	cursorByte := 0
	for i := 0; i < cursorRunes && cursorByte < len(text); i++ {
		_, size := utf8.DecodeRuneInString(text[cursorByte:])
		cursorByte += size
	}
	// Find the visual line containing the cursor. Break spaces and hard
	// newlines consumed between line K and K+1 belong to line K, so the cursor
	// only advances to line K+1 once it reaches that line's first content byte.
	row = 0
	for row < len(spans)-1 && cursorByte >= spans[row+1].startByte {
		row++
	}
	// The visible content of this line spans [startByte, endByte). A cursor in
	// the consumed break spaces past endByte is clamped to endByte so the caret
	// sits at the end of the visible content rather than in the trailing blank.
	end := cursorByte
	if end > spans[row].endByte {
		end = spans[row].endByte
	}
	contentCol = displayLineWidth(text[spans[row].startByte:end])
	return row, contentCol
}

// renderFanout renders a FrameFanout as a non-collapsible block with header
// and full body always visible. Used for subagent_fanout and subagent_run display.
func (r *Renderer) renderFanout(f Frame) {
	summary := strings.TrimSpace(f.Summary)
	content := strings.TrimSpace(f.Content)
	// Record which content line each visual line came from, so a click inside the
	// block can be resolved to the task it landed on. Ownership is captured in
	// the same loop that writes the lines, which is the only way it stays
	// correct across wrapping and prefix changes. -1 marks a line that belongs
	// to no task (header, spacing).
	r.fanoutLineOwners = r.fanoutLineOwners[:0]
	owner := func(contentLine int, count int) {
		for i := 0; i < count; i++ {
			r.fanoutLineOwners = append(r.fanoutLineOwners, contentLine)
		}
	}
	if summary == "" && content == "" {
		return
	}
	// Header: bullet + summary
	markerGlyph := "○"
	if f.Final {
		markerGlyph = "●"
	}
	bullet := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true).Render(markerGlyph)
	if summary != "" {
		if f.Final {
			if ds := formatDuration(f.Duration); ds != "" {
				summary += " · " + ds
			}
		}
		header := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true).Render(summary)
		headerLine := wrapToolDisplayLine(bullet+" "+header, "   ")
		_, _ = fmt.Fprintln(r.out, headerLine)
		owner(-1, strings.Count(headerLine, "\n")+1)
	}
	// Body: tree hierarchy with └ symbols showing parent-child relationship.
	// Task lines (starting with ✓/✗/○) are first-level children prefixed
	// with "  └ "; stat and activity lines are continuations indented with
	// "    " to align under the task title text.
	if content != "" {
		bodyWidth := termWidthOrDefault() - contentRightMargin
		if bodyWidth < 20 {
			bodyWidth = 20
		}
		for contentLine, line := range strings.Split(content, "\n") {
			line = strings.TrimRight(line, "\r")
			if strings.TrimSpace(line) == "" {
				_, _ = fmt.Fprintln(r.out)
				owner(-1, 1)
				continue
			}
			lead, leadWidth, text := fanoutRowLead(line)
			wrapWidth := bodyWidth - leadWidth
			if wrapWidth < 20 {
				wrapWidth = 20
			}
			wrapped := wrapCardLine(text, wrapWidth)
			continuation := strings.Repeat(" ", leadWidth)
			for i, wl := range wrapped {
				p := lead
				if i > 0 {
					p = continuation
				}
				styled := lipgloss.NewStyle().Faint(true).Render(p + wl)
				_, _ = fmt.Fprintln(r.out, styled)
			}
			owner(contentLine, len(wrapped))
		}
	}
	_, _ = fmt.Fprintln(r.out)
	owner(-1, 1)
}

// fanoutPrefixWidth is how many columns every fanout row is drawn past the
// block's left edge: both prefixes ("    " for a continuation, "  └ " for a
// task) are four columns wide as they are painted.
const fanoutPrefixWidth = 4

// fanoutRowLead splits a fanout content line into what precedes its text — the
// prefix the block draws the row with, plus the indent the content line carries
// to mark its own depth — and the text itself, reporting the columns that lead
// occupies.
//
// Both halves matter to wrapping: the text is what has to fit in the width the
// lead leaves, and a wrapped fragment repeats the lead's width so it sits under
// the row it continues. Indenting every continuation by a fixed four columns
// instead pulled the tail of an indented row — a stats line, a failure
// described in several sentences — back to the task's own depth.
func fanoutRowLead(line string) (lead string, width int, text string) {
	prefix := "    "
	if isFanoutTaskLine(line) {
		prefix = "  └ "
	}
	text = strings.TrimLeft(line, " ")
	indent := line[:len(line)-len(text)]
	return prefix + indent, fanoutPrefixWidth + len(indent), text
}

// isFanoutTaskLine reports whether a fanout content line is a subagent task
// entry (as opposed to a stat or activity continuation line).
// Task lines either start with a status icon (✓ done, ✗ failed) or are plain
// text without leading whitespace (running/pending tasks without an icon).
func isFanoutTaskLine(line string) bool {
	if strings.HasPrefix(line, "✓") || strings.HasPrefix(line, "✗") || strings.HasPrefix(line, "!") {
		return true
	}
	// Stat/activity lines start with spaces; everything else is a task title.
	return !strings.HasPrefix(line, " ")
}

func (r *Renderer) renderStatus(f Frame) {
	title := strings.TrimSpace(f.Title)
	content := strings.TrimSpace(f.Content)
	if title == "" && content == "" {
		return
	}
	line := title
	if content != "" {
		line = strings.TrimSpace(line + " " + content)
	}
	if strings.HasPrefix(title, "Worked for ") {
		ruleWidth := termWidthOrDefault()
		// Viewport capture forces the width to the padded transcript width; the
		// rule is full-bleed, so add the padding back to reach the true edge.
		if r.fullBodyMode {
			ruleWidth += viewportRightPadding
		}
		line = workedStatusLine(line, ruleWidth)
		_, _ = fmt.Fprintln(r.out, lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(line))
		_, _ = fmt.Fprintln(r.out)
		return
	}
	rendered := wrapToolDisplayLine(line, "  ")
	if f.MaxDisplayLines > 0 {
		rendered = limitVisualLines(rendered, f.MaxDisplayLines)
	}
	_, _ = fmt.Fprintln(r.out, lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(rendered))
	_, _ = fmt.Fprintln(r.out)
}

// limitVisualLines retains at most maxLines already-wrapped terminal lines.
// If content is omitted, the last retained line is shortened and terminated by
// an ellipsis. It is used for non-expandable transient confirmations.
func limitVisualLines(text string, maxLines int) string {
	if maxLines <= 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	if len(lines) <= maxLines {
		return text
	}
	lines = lines[:maxLines]
	last := strings.TrimRight(sgrPattern.ReplaceAllString(lines[maxLines-1], ""), " ")
	width := displayLineWidth(last)
	if width <= 1 {
		lines[maxLines-1] = "…"
	} else {
		lines[maxLines-1] = runewidth.Truncate(last, width-1, "…")
	}
	return strings.Join(lines, "\n")
}

// agentRosterPrimaryHint and agentRosterSubagentHint are the right-aligned
// hints shown on the roster row that currently has keyboard focus. They
// differ by row kind: the primary row can still move focus (so it advertises
// both arrows), while a focused subagent row advertises view/stop instead.
const (
	agentRosterPrimaryHint  = "↑/↓ to select · Enter to view"
	agentRosterSubagentHint = "Enter to view · x to stop"
)

// agentRosterRowLabel is the readable name for a roster row: the subagent's
// type, or the primary agent's name. Falls back to the row id only when a row
// arrives without either, which would otherwise render as a nameless bullet.
func agentRosterRowLabel(row AgentRosterRow) string {
	if label := strings.TrimSpace(row.Label); label != "" {
		return label
	}
	if id := strings.TrimSpace(row.ID); id != "" {
		return id
	}
	return "subagent"
}

// agentRosterRowSuffix is the shortest piece of a row's id that tells two rows
// of the same kind apart. Subagent ids are "subagent-<uuid>"; the uuid's first
// group is unique enough for a list this short.
func agentRosterRowSuffix(row AgentRosterRow) string {
	return shortAgentID(row.ID)
}

// shortAgentID is the shortest piece of a roster key that still tells two
// agents apart: the first group of the uuid behind the "subagent-" prefix. The
// roster panel and the subagent view's footer both use it, so an agent is
// called the same thing wherever it is named.
func shortAgentID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "subagent-")
	id = strings.TrimPrefix(id, "plan-review-")
	if id == "" {
		return ""
	}
	if runes := []rune(id); len(runes) > 4 {
		return string(runes[:4])
	}
	return id
}

// agentRosterRowDetail is what the row's agent is working on, shown to the
// right of its name: the tool it is running right now when one is in flight,
// and otherwise the short title its dispatcher named the task with. A subagent
// that shows only its type tells the reader nothing about which of several it
// is.
//
// The dispatch prompt is the last resort, not the default. It is a whole
// instruction — several sentences, often a path — and a row of a roster cannot
// say anything with the first 56 columns of one; the prompt has a place of its
// own as the first message of the agent's view.
func agentRosterRowDetail(row AgentRosterRow) string {
	detail := strings.TrimSpace(row.Activity)
	if detail == "" {
		detail = strings.TrimSpace(row.Title)
	}
	if detail == "" {
		detail = strings.TrimSpace(row.Task)
	}
	if detail == "" {
		return ""
	}
	if idx := strings.IndexAny(detail, "\r\n"); idx >= 0 {
		detail = strings.TrimSpace(detail[:idx])
	}
	return truncateForDisplay(detail, agentRosterDetailMaxWidth)
}

// agentRosterDetailMaxWidth keeps the detail from crowding out the name on a
// narrow terminal; the line itself is truncated to the terminal width after.
const agentRosterDetailMaxWidth = 56

// agentRosterIndexForView is the row for the transcript currently on screen:
// the subagent whose roster key the view is keyed on, or the primary row when
// the conversation itself is shown. -1 when the roster has no row for it, which
// a finished subagent's view is: its row is gone while its card still opens it.
func agentRosterIndexForView(snapshot AgentRosterSnapshot, activeView string) int {
	view := strings.TrimSpace(activeView)
	for i, row := range snapshot.Rows {
		if view == "" {
			if strings.EqualFold(strings.TrimSpace(row.Kind), "primary") {
				return i
			}
			continue
		}
		if strings.TrimSpace(row.ID) == view {
			return i
		}
	}
	return -1
}

// agentRosterMarkedIndex is the row the cursor points at. While the user is
// moving through the roster that is the row they are on — the one Enter would
// open — and otherwise it is the agent whose transcript is on screen.
//
// Deriving it from the view is what keeps the two in step. The roster used to
// carry its own selection, written only by Up/Down, so every other way of
// changing the view — clicking a subagent's card, escaping back to the
// conversation — left the cursor pointing at whatever row was last navigated
// to, naming an agent the user was not looking at.
func agentRosterMarkedIndex(snapshot AgentRosterSnapshot, focused bool, cursor int, activeView string) int {
	if len(snapshot.Rows) == 0 {
		return -1
	}
	if !focused {
		return agentRosterIndexForView(snapshot, activeView)
	}
	if cursor < 0 {
		return 0
	}
	if cursor >= len(snapshot.Rows) {
		return len(snapshot.Rows) - 1
	}
	return cursor
}

// formatAgentRosterLines paints the roster. marked is the row the cursor points
// at, as agentRosterMarkedIndex resolves it; -1 (or any index outside the rows)
// marks none, which is what the reader should see while the transcript on
// screen belongs to no listed agent.
func formatAgentRosterLines(snapshot AgentRosterSnapshot, focused bool, marked int, termWidth int) []string {
	if len(snapshot.Rows) == 0 {
		return nil
	}
	// A row is named by what it is — "plan-reviewer", "explore", the primary
	// agent's name — because the ids these rows key on are uuids that tell the
	// reader nothing. Two rows of the same kind (a fanout dispatches several
	// explores) get a short id suffix so they stay distinguishable.
	duplicated := map[string]int{}
	for _, row := range snapshot.Rows {
		duplicated[agentRosterRowLabel(row)]++
	}
	lines := make([]string, 0, len(snapshot.Rows)+1)
	for i, row := range snapshot.Rows {
		isMarked := i == marked
		// The same cursor the pickers use, pointing at the row whose agent is
		// on screen. Unmarked rows keep its column so the names stay aligned.
		glyph := " "
		if isMarked {
			glyph = selectorCursorGlyph
		}
		name := agentRosterRowLabel(row)
		if duplicated[name] > 1 {
			if suffix := agentRosterRowSuffix(row); suffix != "" {
				name += " · " + suffix
			}
		}
		line := fmt.Sprintf("%s %s", glyph, name)
		if detail := agentRosterRowDetail(row); detail != "" {
			line += "  " + detail
		}
		if focused && isMarked {
			isPrimary := strings.EqualFold(strings.TrimSpace(row.Kind), "primary")
			hint := agentRosterSubagentHint
			if isPrimary {
				hint = agentRosterPrimaryHint
			}
			lines = append(lines, layoutComposerFooter(line, hint, termWidth))
			continue
		}
		lines = append(lines, runewidth.Truncate(line, termWidth, "…"))
	}
	return lines
}

func agentRosterHasRunningSubagent(snapshot AgentRosterSnapshot) bool {
	for _, row := range snapshot.Rows {
		if strings.EqualFold(strings.TrimSpace(row.Kind), "subagent") && strings.EqualFold(strings.TrimSpace(row.Status), "running") {
			return true
		}
	}
	return false
}

func thinkingDisplayLinesWithWidth(f Frame, termWidth int) []string {
	content := strings.TrimSpace(f.Content)
	if content == "" {
		return nil
	}
	if termWidth <= 0 {
		termWidth = 80
	}
	lines := []string{}
	bodyWidth := maxInt(1, termWidth-contentRightMargin-2)
	body := wrapCardContent(strings.TrimRight(content, "\r\n"), bodyWidth)
	for idx, line := range strings.Split(body, "\n") {
		prefix := "  "
		if idx == 0 {
			prefix = "▸ "
		}
		lines = append(lines, thinkingColor+prefix+strings.TrimRight(line, "\r")+sharedBlockReset)
	}
	return append(lines, thinkingColor+sharedBlockReset)
}

// A compaction card's title names its state; the renderer and the folded
// header read the state from it.
const (
	compactTitleRunning   = "Compacting context"
	compactTitleDone      = "Context compacted"
	compactTitleFailed    = "Compaction failed"
	compactTitleCancelled = "Compaction cancelled"
)

// compactHeaderLine is a compaction card's first line: a marker that spins
// while the compaction runs, its state in bold, and the detail — the phase and
// the history size while running, the before → after banner once done.
func compactHeaderLine(f Frame, spinnerPhase int) string {
	title := strings.TrimSpace(f.Title)
	if title == "" {
		title = compactTitleRunning
	}
	marker, color := "●", "70"
	switch {
	case !f.Final:
		marker, color = spinnerGlyphs[spinnerPhase%len(spinnerGlyphs)], "75"
	case title == compactTitleFailed:
		marker, color = "✗", "203"
	case title == compactTitleCancelled:
		marker, color = "○", "250"
	}
	markerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
	titleColor := color
	if f.Final && title == compactTitleDone {
		titleColor = "75"
	}
	line := markerStyle.Render(marker) + " " + lipgloss.NewStyle().Foreground(lipgloss.Color(titleColor)).Bold(true).Render(title)
	if detail := strings.TrimSpace(f.Summary); detail != "" {
		line += lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(" · " + detail)
	}
	return line
}

func (r *Renderer) renderMemoryCompact(f Frame) {
	header := compactHeaderLine(f, r.spinnerPhase)
	if !f.Final {
		// The bar has a line of its own under the header, so it can be wide
		// enough to read at a glance and never competes with the text for room.
		_, _ = fmt.Fprintln(r.out, wrapToolDisplayLine(header, "   "))
		_, _ = fmt.Fprintln(r.out, "  "+renderCompactProgressBar(f.Progress, r.spinnerPhase, compactProgressBarWidth(termWidthOrDefault())))
		return
	}
	_, _ = fmt.Fprintln(r.out, wrapToolDisplayLine(header, "  │ "))
	content := strings.TrimSpace(f.Content)
	if content == "" {
		_, _ = fmt.Fprintln(r.out)
		return
	}
	contentWidth := maxCardContentWidth() - 2
	if contentWidth < 20 {
		contentWidth = 20
	}
	body := ""
	if strings.TrimSpace(f.Title) == compactTitleDone {
		// The body is the checkpoint summary, which the summarizer writes as
		// Markdown (headings, bullet lists, code spans). Render it the way
		// assistant prose is rendered rather than as plain wrapped text, so a
		// checkpoint reads like a document instead of a wall of literal "#".
		body = renderAssistantMarkdownWithWidth(content, contentWidth, r.diffTheme)
	}
	if body == "" {
		body = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(wrapCardContent(content, contentWidth))
	}
	_, _ = fmt.Fprintln(r.out, prefixLines(body, "  └ ", "    "))
	_, _ = fmt.Fprintln(r.out)
}

// renderGoal draws one of a /goal's lines: a marker and title in the goal's
// colour, what it amounts to after it, and below, in full, what the line is
// about — the objective, the check's reason for another round, or why the
// goal ended.
func (r *Renderer) renderGoal(f Frame) {
	marker, color := goalMarker(f.Title)
	header := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true).Render(marker + " " + strings.TrimSpace(f.Title))
	if summary := strings.TrimSpace(f.Summary); summary != "" {
		header += lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(" · " + summary)
	}
	_, _ = fmt.Fprintln(r.out, wrapToolDisplayLine(header, "  "))
	if content := strings.TrimSpace(f.Content); content != "" {
		body := lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(wrapCardContent(content, max(maxCardContentWidth()-2, 20)))
		_, _ = fmt.Fprintln(r.out, prefixLines(body, "  └ ", "    "))
	}
	_, _ = fmt.Fprintln(r.out)
}

// goalMarker is the glyph and colour a goal line leads with: amber while the
// goal is in progress, green when it was met, amber again when it stopped
// short, red when something failed, and plain when the user stopped it.
func goalMarker(title string) (string, string) {
	switch strings.TrimSpace(title) {
	case goalTitleDone:
		return "✓", "78"
	case goalTitleStuck, goalTitleCapped:
		return "■", "214"
	case goalTitleInterrupted:
		return "○", "250"
	case goalTitleFailed:
		return "✗", "203"
	case goalTitleStarted:
		return "◎", "214"
	default:
		return "↻", "214"
	}
}

// compactProgressBarWidth sizes the bar to the terminal: wide enough to read
// as the card's main element, and never wider than a comfortable glance.
func compactProgressBarWidth(termWidth int) int {
	// Two columns of indent, a gap and the "100%" label.
	width := termWidth - contentRightMargin - 2 - 2 - 4
	return min(max(width, 12), 64)
}

// compactBarRamp is the fill's gradient, deep blue to bright cyan. Each cell
// takes its colour from where it sits on the whole bar, so the gradient is
// revealed as the bar fills rather than squeezed into the filled part.
var compactBarRamp = []string{"25", "26", "27", "33", "39", "45", "51"}

// compactBarShimmer is the highlight that sweeps across the filled part while
// the compaction runs, brightest at its centre.
var compactBarShimmer = []string{"117", "159", "195", "159", "117"}

// renderCompactProgressBar draws the running compaction's bar and its
// percentage: a gradient fill over a quiet track, a highlight sweeping through
// the fill so the bar reads as alive even while the percentage holds, and the
// percentage itself, bold, after it.
func renderCompactProgressBar(percent, phase, width int) string {
	percent = min(max(percent, 0), 100)
	filled := (percent*width + 50) / 100
	if percent > 0 && filled == 0 {
		filled = 1
	}
	// The sweep covers the fill and runs off both ends, two cells per tick.
	shimmerAt := -len(compactBarShimmer)
	if filled > 0 {
		shimmerAt = (phase*2)%(filled+2*len(compactBarShimmer)) - len(compactBarShimmer)
	}
	var b strings.Builder
	for i := 0; i < width; i++ {
		color := "238"
		if i < filled {
			color = compactBarRamp[i*len(compactBarRamp)/width]
			if k := i - shimmerAt; k >= 0 && k < len(compactBarShimmer) {
				color = compactBarShimmer[k]
			}
		}
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render("━"))
	}
	label := lipgloss.NewStyle().Foreground(lipgloss.Color("45")).Bold(true).Render(fmt.Sprintf("%3d%%", percent))
	return b.String() + "  " + label
}

// skillInstallFailedSummary is the verdict a failed install's card carries.
// The card is the same block the running install owned, so the failure has to
// travel on the frame that replaces it rather than as a separate error line.
const skillInstallFailedSummary = "install failed"

// renderSkillInstall draws the card a background skill install owns. While
// the package is being fetched the card is one live line — what is being
// installed, what the install is doing now, and how long it has taken — and it
// becomes that install's result in place when it finishes, so the transcript
// keeps one durable block per install instead of a status line that vanishes
// with nothing to show for it.
//
// The running marker is the same Braille spinner a running tool carries: an
// install reports a phase but never a trustworthy fraction (the clone that
// dominates the wall clock reports nothing while it runs), so liveness is a
// spinner and progress is the phase plus the elapsed time, not a bar pretending
// to know how far along the fetch is.
func (r *Renderer) renderSkillInstall(f Frame) {
	source := strings.TrimSpace(f.Title)
	summary := strings.TrimSpace(f.Summary)
	content := strings.TrimSpace(f.Content)
	failed := f.Final && strings.EqualFold(summary, skillInstallFailedSummary)

	color := "75"
	markerGlyph := "●"
	switch {
	case !f.Final:
		markerGlyph = spinnerGlyphs[r.spinnerPhase%len(spinnerGlyphs)]
		color = "117"
	case failed:
		markerGlyph = "✗"
		color = "203"
	}
	headerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
	detailStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))

	head := summary
	if head == "" {
		head = "installing"
	}
	if source != "" {
		head += " · " + source
	}
	line := headerStyle.Render(markerGlyph) + " " + headerStyle.Render(head)
	segments := []string{}
	if !f.Final && content != "" {
		segments = append(segments, content)
	}
	if elapsed := skillInstallElapsedText(f.Duration); elapsed != "" {
		segments = append(segments, elapsed)
	}
	if len(segments) > 0 {
		line += detailStyle.Render(" · " + strings.Join(segments, " · "))
	}

	if !f.Final {
		_, _ = fmt.Fprintln(r.out, wrapToolDisplayLine(line, "   "))
		return
	}
	_, _ = fmt.Fprintln(r.out, wrapToolDisplayLine(line, "  │ "))
	if content == "" {
		_, _ = fmt.Fprintln(r.out)
		return
	}
	body := wrapCardContent(content, maxCardContentWidth()-2)
	_, _ = fmt.Fprintln(r.out, detailStyle.Render(prefixLines(body, "  └ ", "    ")))
	_, _ = fmt.Fprintln(r.out)
}

// skillInstallElapsedText formats a card's elapsed time. An install that
// finishes inside a second reports nothing rather than a meaningless "0s".
func skillInstallElapsedText(elapsed time.Duration) string {
	if elapsed < time.Second {
		return ""
	}
	return formatWorkingElapsed(elapsed)
}

func workedStatusLine(label string, width int) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	prefix := "─ " + label + " "
	if width <= lipgloss.Width(prefix) {
		// A label wider than the row is cut like any other row; returning it
		// verbatim spilled the tail onto the row below.
		return runewidth.Truncate(prefix, width, "…")
	}
	return prefix + strings.Repeat("─", width-lipgloss.Width(prefix))
}

// termSizeWithFallback returns the terminal dimensions by querying common fds.
// forcedTermWidth/forcedTermHeight override the real terminal size when non-zero
// (used by tests to override the real terminal size).
var forcedTermWidth, forcedTermHeight int

// lastGoodTermSize caches the most recent successful term.GetSize result so a
// transient query failure falls back to the real size instead of the hardcoded
// 80x24 default.
//
// term.GetSize (an ioctl under the hood) can transiently fail — e.g. EINTR when
// a SIGWINCH or another signal interrupts the syscall, which is more likely
// during the rapid repaint bursts a mouse-wheel scroll produces. When it did,
// termSizeWithFallback collapsed to 80x24; a single such frame re-rendered
// full-bleed elements (the FrameUser card's top/bottom rules) at width 80 while
// the true terminal was wider, drawing a half-width rule with the row's
// background bar filling to the real edge. That wrong-width render was then
// cached per block (keyed on width), so the half-width card could persist until
// the next width change. Remembering the last good size makes the fallback
// return the true dimensions and eliminates the flash. Guarded by a mutex
// because several goroutines (paint, status/blink tickers, resize watch, modal
// overlays) call termSizeWithFallback.
var (
	lastGoodTermMu     sync.Mutex
	lastGoodTermWidth  int
	lastGoodTermHeight int
)

// termGetSize is indirected so tests can simulate a transient query failure.
var termGetSize = term.GetSize

func termSizeWithFallback() (width, height int, err error) {
	if forcedTermWidth > 0 || forcedTermHeight > 0 {
		w, h := forcedTermWidth, forcedTermHeight
		if w <= 0 {
			w = 80
		}
		if h <= 0 {
			h = 24
		}
		return w, h, nil
	}
	for _, fd := range []int{int(os.Stdout.Fd()), int(os.Stderr.Fd()), int(os.Stdin.Fd())} {
		w, h, e := termGetSize(fd)
		if e == nil && w > 0 && h > 0 {
			lastGoodTermMu.Lock()
			lastGoodTermWidth, lastGoodTermHeight = w, h
			lastGoodTermMu.Unlock()
			return w, h, nil
		}
	}
	// The query failed on every fd (e.g. transient EINTR). Prefer the last
	// known-good size over the hardcoded default so a momentary failure does
	// not shrink the layout to 80x24 and render full-bleed elements at the
	// wrong width.
	lastGoodTermMu.Lock()
	w, h := lastGoodTermWidth, lastGoodTermHeight
	lastGoodTermMu.Unlock()
	if w > 0 && h > 0 {
		return w, h, nil
	}
	return 80, 24, fmt.Errorf("no tty found")
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

// noteToolMarkdownBody extracts the Markdown payload for tools whose note body
// must remain rich text rather than being flattened by generic JSON formatting.
func noteToolMarkdownBody(title, content string, input map[string]any) string {
	payload := strings.TrimSpace(content)
	if payload != "" && !strings.HasPrefix(payload, "{") {
		return payload
	}
	if payload != "" {
		var obj map[string]any
		if err := json.Unmarshal([]byte(payload), &obj); err == nil {
			field := "content"
			if strings.EqualFold(title, "memories_add_ad_hoc_note") {
				field = "note"
			}
			if value, ok := obj[field].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	if strings.EqualFold(title, "memories_add_ad_hoc_note") {
		return strings.TrimSpace(inputString(tool.ToolMeta{Input: input}, "note"))
	}
	return ""
}

// RenderDiff converts a parsed DiffDoc into a themed ANSI string suitable for
// use as the Content of a Frame{Kind: FrameSystem}. Lines are
// rendered with a line-number gutter, full-width add/del bands, and syntax
// highlighting via the shared renderer in diffhighlight.go. Every changed
// line is shown; a long diff scrolls.
func RenderDiff(doc event.DiffDoc, theme DiffTheme) string {
	return renderDiffCore(doc.Files, theme, maxCardContentWidth()-2)
}

// renderDiffCore renders parsed diff files into a themed ANSI string, every
// changed line of every file. bandW is the visible width the band (and
// padding) should fill; callers adapt this to their container (tool cards vs
// approval overlay).
func renderDiffCore(files []event.FileStat, theme DiffTheme, bandW int) string {

	if bandW < 20 {
		bandW = 20
	}
	p := claudeDiffPaletteFor(theme)

	var sb strings.Builder
	for i, f := range files {
		if i > 0 {
			sb.WriteByte('\n')
		}
		renderFileHeader(&sb, f, p.noColor)
		if f.Binary {
			continue // header already shown; no line content for binary files
		}

		lexer, style := resolveDiffHighlight(f.Path, p)
		gutterW := diffGutterWidthFor(f.Hunks)
		for hi, hunk := range f.Hunks {
			if hi > 0 {
				sb.WriteString(p.meta("⋮"))
				sb.WriteByte('\n')
			}
			for _, line := range hunk.Lines {
				for _, row := range renderClaudeDiffLine(line, p, gutterW, bandW, lexer, style) {
					sb.WriteString(row)
					sb.WriteByte('\n')
				}
			}
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// renderFileHeader writes a colored "▾ path  +N −M" header line into sb.
func renderFileHeader(sb *strings.Builder, f event.FileStat, noColor bool) {
	chevron := "▾"

	path := f.Path
	if f.OldPath != "" && f.OldPath != f.Path {
		path = f.OldPath + " → " + f.Path
	}

	stats := ""
	if f.Added > 0 || f.Deleted > 0 {
		stats = "  +" + strconv.Itoa(f.Added) + " −" + strconv.Itoa(f.Deleted)
	}
	status := ""
	switch f.Status {
	case "binary":
		status = "  [binary]"
	case "mode-only":
		status = "  [mode change]"
	case "renamed":
		// path already shows old→new
	}

	header := chevron + " " + path + stats + status
	if !noColor {
		header = lipgloss.NewStyle().Bold(true).Render(header)
	}
	sb.WriteString(header)
	sb.WriteByte('\n')
}

// softWrap splits text into chunks of at most maxWidth terminal cells, at word
// boundaries (see wrapCardLineIndex).
func softWrap(text string, maxWidth int) []string {
	return wrapCardLine(text, maxWidth)
}

// claudeDiffPalette holds the truecolor row backgrounds and chroma style used
// for full-width add/del bands plus syntax highlighting. Context lines carry no
// band.
type claudeDiffPalette struct {
	noColor  bool
	addBG    [3]uint8
	delBG    [3]uint8
	addBG256 string // ANSI-256 code for add background (fallback when truecolor unavailable)
	delBG256 string // ANSI-256 code for del background (fallback when truecolor unavailable)
	// chromaStyle is the shared code theme (codeStyleNameFor), not a palette of
	// the diff's own: a diff row shows the same file the read_file card above it
	// showed, in the same colours.
	chromaStyle string
	gutterColor string // ANSI-256 code for the line-number gutter
	addMarker   string // ANSI-256 code for the "+" marker
	delMarker   string // ANSI-256 code for the "-" marker
	// fg normalises the chroma style's foreground colours to the brightness of
	// the assistant text around the diff.
	fg syntaxBrightness
	// trueColor is resolved once per palette so a diff emits one colour
	// encoding for every line rather than asking the environment per token.
	trueColor bool
}

// syntaxBrightness clamps chroma's foreground colours to the brightness a
// surface's background can carry. Chroma styles are tuned for an editor canvas
// and deliberately mute comments, punctuation and whitespace; in a transcript
// that mute can read as unreadable grey, and over a diff's add/del band it
// nearly disappears. A clamp is the wrong tool for that wherever it flattens
// colours that are already legible: pulling every token to one brightness
// erases the differences the theme uses to say what a token is, and it lets
// plain text out-shine the highlighted tokens rather than the other way round.
// So each surface gets the clamp its own background needs, and no more:
//
//   - a diff row sits on a coloured band, which raises the background out from
//     under the text, and every token is held at the palette's target there;
//   - code on the plain terminal background is held only at the legibility
//     limit, so the theme keeps its own emphasis between token classes.
type syntaxBrightness struct {
	// target is the relative luminance colours are pulled to, and lighten says
	// which way: towards white on a dark background, towards black on a light
	// one. A zero target disables normalisation.
	target  float64
	lighten bool
}

// syntaxLuminanceDark is the brightness floor for syntax colours on a dark
// background, chosen to sit at the level of a terminal's default foreground —
// the colour assistant messages are printed in — so highlighted code never
// reads dimmer than the prose next to it.
const syntaxLuminanceDark = 0.62

// syntaxLuminanceLight is the matching ceiling for a light background.
const syntaxLuminanceLight = 0.18

// plainLuminanceDark is the legibility floor for code on the plain terminal
// background: the darkest a token may read there. It is about 4.5:1 against a
// typical dark editor canvas (#1e1e1e), the usual floor for body text, and
// deliberately far below syntaxLuminanceDark — a floor only catches the colours
// that would be unreadable and stops there, leaving a comment dimmer than the
// keywords, strings and identifiers next to it instead of pinning all of them
// together.
const plainLuminanceDark = 0.24

// syntaxBrightnessFor returns the normalisation policy for a surface carrying a
// diff's add/del bands.
func syntaxBrightnessFor(theme DiffTheme) syntaxBrightness {
	if noColorActive() {
		return syntaxBrightness{}
	}
	if theme == DiffThemeLight {
		return syntaxBrightness{target: syntaxLuminanceLight}
	}
	return syntaxBrightness{target: syntaxLuminanceDark, lighten: true}
}

// plainSyntaxBrightnessFor returns the policy for code painted straight onto
// the terminal background (read_file output, fenced code blocks), which carries
// no band to fight. On a light canvas the same 4.5:1 limit reads as a ceiling
// rather than a floor — a near-white token disappears against a light
// background exactly as a near-black one does against a dark background — so
// the light side reuses syntaxLuminanceLight, which is that contrast against
// white.
func plainSyntaxBrightnessFor(theme DiffTheme) syntaxBrightness {
	if noColorActive() {
		return syntaxBrightness{}
	}
	if theme == DiffThemeLight {
		return syntaxBrightness{target: syntaxLuminanceLight}
	}
	return syntaxBrightness{target: plainLuminanceDark, lighten: true}
}

func claudeDiffPaletteFor(theme DiffTheme) claudeDiffPalette {
	if noColorActive() {
		return claudeDiffPalette{noColor: true}
	}
	trueColor := terminalTrueColor()
	switch theme {
	case DiffThemeLight:
		return claudeDiffPalette{
			addBG:       [3]uint8{0xcc, 0xf2, 0xd4},
			delBG:       [3]uint8{0xfb, 0xd5, 0xd2},
			addBG256:    "194", // 256-color light green fallback
			delBG256:    "217", // 256-color light salmon fallback
			chromaStyle: codeStyleLight,
			gutterColor: "238",
			addMarker:   "28",
			delMarker:   "124",
			fg:          syntaxBrightnessFor(DiffThemeLight),
			trueColor:   trueColor,
		}
	default: // dark + unknown
		return claudeDiffPalette{
			addBG:       [3]uint8{0x12, 0x4d, 0x1f},
			delBG:       [3]uint8{0x5a, 0x18, 0x18},
			addBG256:    "22", // 256-color dark green fallback
			delBG256:    "52", // 256-color maroon fallback
			chromaStyle: codeStyleDark,
			gutterColor: "252",
			addMarker:   "78",
			// #ffafaf rather than the darker #ff5f5f: the marker column is part
			// of the diff body and is held to the same brightness as the code.
			delMarker: "217",
			fg:        syntaxBrightnessFor(theme),
			trueColor: trueColor,
		}
	}
}

// bandBG returns the escape that paints an add/del row's full-width band.
// The 256-colour fallbacks are hand-picked rather than quantised from the
// 24-bit colour: the cube has no dark green or dark red near them, so
// quantisation lands on the grey ramp and the row loses the one thing the band
// is for — saying at a glance whether the line was added or removed.
func (p claudeDiffPalette) bandBG(rgb [3]uint8, fallback256 string) string {
	if p.trueColor {
		return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", rgb[0], rgb[1], rgb[2])
	}
	return "\x1b[48;5;" + fallback256 + "m"
}

// meta renders secondary diff chrome — hunk separators, collapse hints — in the
// gutter colour. ANSI dim (SGR 2) is deliberately never used inside a diff: it
// halves the brightness of whatever the terminal's palette resolves to, which
// is exactly the washed-out look the normalisation above exists to prevent.
func (p claudeDiffPalette) meta(text string) string {
	if p.noColor || p.gutterColor == "" {
		return text
	}
	return "\x1b[38;5;" + p.gutterColor + "m" + text + "\x1b[0m"
}

// sequence turns one chroma style colour into the SGR escape the terminal will
// honour: normalised to the target brightness, then encoded as 24-bit or as the
// nearest 256-colour entry that still holds that brightness.
func (b syntaxBrightness) sequence(trueColor bool, red, green, blue uint8) string {
	red, green, blue = b.normalize(red, green, blue)
	if trueColor {
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", red, green, blue)
	}
	return fmt.Sprintf("\x1b[38;5;%dm", nearest256(red, green, blue, b.target, b.lighten))
}

// normalize pulls one syntax colour to the target brightness, keeping its hue:
// a colour dimmer than the target on a dark background is blended towards
// white, and one brighter than the target on a light background is blended
// towards black. Colours already past the target are returned untouched, so a
// style's deliberate accents keep their exact value.
func (b syntaxBrightness) normalize(red, green, blue uint8) (uint8, uint8, uint8) {
	if b.target <= 0 {
		return red, green, blue
	}
	if b.lighten == (relativeLuminance(red, green, blue) >= b.target) {
		return red, green, blue
	}
	var toward uint8
	if b.lighten {
		toward = 0xff
	}
	// Blend fraction towards `toward`: luminance is monotonic in it, so a fixed
	// number of bisection steps lands within a rounding step of the target.
	lo, hi := 0.0, 1.0
	for i := 0; i < 16; i++ {
		mid := (lo + hi) / 2
		mr, mg, mb := blendToward(red, green, blue, toward, mid)
		if b.lighten == (relativeLuminance(mr, mg, mb) < b.target) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return blendToward(red, green, blue, toward, hi)
}

// blendToward mixes an RGB triple towards a grey level by fraction t (0 = the
// original colour, 1 = the target level).
func blendToward(r, g, b, target uint8, t float64) (uint8, uint8, uint8) {
	mix := func(c uint8) uint8 {
		return uint8(math.Round(float64(c) + (float64(target)-float64(c))*t))
	}
	return mix(r), mix(g), mix(b)
}

// relativeLuminance is the WCAG relative luminance of an sRGB triple (0 = black,
// 1 = white). It is used rather than HSL lightness because it tracks how bright
// a colour actually looks: #0000ff and #ffff00 share an HSL lightness but not a
// readable brightness.
func relativeLuminance(r, g, b uint8) float64 {
	return 0.2126*linearizeChannel(r) + 0.7152*linearizeChannel(g) + 0.0722*linearizeChannel(b)
}

func linearizeChannel(c uint8) float64 {
	v := float64(c) / 255
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// terminalTrueColor reports whether the terminal accepts 24-bit SGR colour. It
// is answered from the environment (COLORTERM, or a "-direct" terminfo name)
// rather than by probing, and callers resolve it once per render rather than
// per token.
//
// This is not a cosmetic preference. A terminal without 24-bit support does not
// ignore "\x1b[38;2;r;g;bm": it reads the parameters one at a time, 38 asks for
// an extended colour it cannot provide, and the following 2 lands as SGR 2 —
// faint. Every syntax-coloured span then renders at about half brightness, which
// is precisely the washed-out diff this palette exists to prevent. Apple
// Terminal is the common case. When in doubt the answer is false: the
// 256-colour form is honoured everywhere, while the 24-bit form is actively
// harmful on the terminals that lack it.
func terminalTrueColor() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("COLORTERM"))) {
	case "truecolor", "24bit":
		return true
	}
	term := strings.ToLower(strings.TrimSpace(os.Getenv("TERM")))
	return strings.Contains(term, "direct") || strings.Contains(term, "truecolor")
}

// xterm256Entry is one addressable entry of the ANSI 256-colour palette.
type xterm256Entry struct {
	r, g, b uint8
	lum     float64
}

// xterm256CubeLevels are the six channel values of the 6×6×6 colour cube. They
// are not evenly spaced, which is why quantisation searches the palette instead
// of scaling a channel into it.
var xterm256CubeLevels = [6]uint8{0, 95, 135, 175, 215, 255}

// xterm256Palette lists entries 16-255: the 6×6×6 cube followed by the 24-step
// grey ramp. The first 16 slots are left out on purpose — their colours are
// whatever the user's terminal theme sets, so nothing can be matched against
// them.
var xterm256Palette = sync.OnceValue(func() []xterm256Entry {
	entries := make([]xterm256Entry, 0, 240)
	for r := 0; r < 6; r++ {
		for g := 0; g < 6; g++ {
			for b := 0; b < 6; b++ {
				cr, cg, cb := xterm256CubeLevels[r], xterm256CubeLevels[g], xterm256CubeLevels[b]
				entries = append(entries, xterm256Entry{cr, cg, cb, relativeLuminance(cr, cg, cb)})
			}
		}
	}
	for n := 0; n < 24; n++ {
		v := uint8(8 + 10*n)
		entries = append(entries, xterm256Entry{v, v, v, relativeLuminance(v, v, v)})
	}
	return entries
})

// nearest256 returns the palette index closest to an RGB colour, considering
// only entries whose luminance is on the required side of minLum (at or above
// it when lighten is true, at or below it otherwise). When no entry qualifies
// the closest overall is returned.
//
// The brightness constraint is the point: the palette's steps are coarse, so
// plain nearest-colour matching rounds a normalised colour back down a step and
// quietly undoes the brightness floor on every terminal that renders the
// 256-colour form.
func nearest256(r, g, b uint8, minLum float64, lighten bool) uint8 {
	best, bestDist := -1, 1<<30
	fallback, fallbackDist := 0, 1<<30
	for i, e := range xterm256Palette() {
		dr, dg, db := int(r)-int(e.r), int(g)-int(e.g), int(b)-int(e.b)
		dist := dr*dr + dg*dg + db*db
		if dist < fallbackDist {
			fallback, fallbackDist = i, dist
		}
		if minLum > 0 && lighten == (e.lum < minLum) {
			continue
		}
		if dist < bestDist {
			best, bestDist = i, dist
		}
	}
	if best < 0 {
		best = fallback
	}
	return uint8(16 + best)
}

// diffTabWidth is the tab stop used when expanding tabs for display.
const diffTabWidth = 4

// expandTabs replaces tabs with spaces to the next tab stop so that display
// width matches what the terminal renders.
func expandTabs(s string, width int) string {
	if !strings.ContainsRune(s, '\t') || width <= 0 {
		return s
	}
	var sb strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			n := width - (col % width)
			sb.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		sb.WriteRune(r)
		col++
	}
	return sb.String()
}

// diffSeg is a run of code text sharing one foreground color.
type diffSeg struct {
	r, g, b uint8
	hasFG   bool
	text    string
}

// resolveDiffHighlight picks the chroma lexer (by filename) and style for a
// file. Returns (nil, nil) when color is disabled, so callers render plain text.
func resolveDiffHighlight(path string, p claudeDiffPalette) (chroma.Lexer, *chroma.Style) {
	if p.noColor {
		return nil, nil
	}
	lexer := lexers.Match(filepath.Base(path))
	if lexer == nil {
		lexer = lexers.Fallback
	}
	return chroma.Coalesce(lexer), styles.Get(p.chromaStyle)
}

// highlightSegs tokenizes a single line of code with chroma and returns
// foreground-colored segments. Tokenization is per-line, which loses cross-line
// state (block comments, multi-line strings) but is adequate for diff rows.
func highlightSegs(text string, lexer chroma.Lexer, st *chroma.Style, p claudeDiffPalette) []diffSeg {
	plain := []diffSeg{{text: text}}
	if lexer == nil || st == nil || strings.TrimSpace(text) == "" {
		return plain
	}
	it, err := lexer.Tokenise(nil, text)
	if err != nil {
		return plain
	}
	segs := make([]diffSeg, 0, 8)
	for _, tok := range it.Tokens() {
		val := strings.TrimRight(tok.Value, "\n")
		if val == "" {
			continue
		}
		seg := diffSeg{text: val}
		if entry := st.Get(tok.Type); entry.Colour.IsSet() {
			seg.hasFG = true
			seg.r, seg.g, seg.b = entry.Colour.Red(), entry.Colour.Green(), entry.Colour.Blue()
		}
		segs = append(segs, seg)
	}
	if len(segs) == 0 {
		return plain
	}
	return segs
}

// renderClaudeDiffLine renders one diff line (possibly soft-wrapped into
// several visual rows) with a right-aligned line-number gutter, a +/-/space
// marker, syntax-highlighted code, and for add/del lines a full-width
// background band padded to bandW.
//
// gutterW is the width reserved for the line number. bandW is the total visible
// width the band (and padding) should fill. lexer/st drive syntax highlighting
// (both nil = plain code).
func renderClaudeDiffLine(l event.DiffLine, p claudeDiffPalette, gutterW, bandW int, lexer chroma.Lexer, st *chroma.Style) []string {
	var num int
	var marker, markerColor, bg string
	band := false
	switch l.Kind {
	case event.LineAdd:
		num, marker, markerColor = l.NewNo, "+", p.addMarker
		bg = p.bandBG(p.addBG, p.addBG256)
		band = true
	case event.LineDel:
		num, marker, markerColor = l.OldNo, "-", p.delMarker
		bg = p.bandBG(p.delBG, p.delBG256)
		band = true
	default:
		marker = " "
		if l.NewNo != 0 {
			num = l.NewNo
		} else {
			num = l.OldNo
		}
	}

	// Expand tabs so band padding (computed from display width) lines up with
	// what the terminal actually renders — Go code is tab-indented.
	text := expandTabs(l.Text, diffTabWidth)

	// code column width after gutter + marker + space.
	codeW := bandW - gutterW - 3
	if codeW < 8 {
		codeW = 8
	}
	chunks := softWrap(text, codeW)
	if len(chunks) == 0 {
		chunks = []string{""}
	}

	rows := make([]string, 0, len(chunks))
	for ci, chunk := range chunks {
		numStr := ""
		if ci == 0 && num != 0 {
			numStr = strconv.Itoa(num)
		}
		gutter := fmt.Sprintf("%*s ", gutterW, numStr)

		if p.noColor {
			rows = append(rows, gutter+marker+" "+chunk)
			continue
		}

		var sb strings.Builder
		if band {
			sb.WriteString(bg)
		}
		sb.WriteString("\x1b[38;5;" + p.gutterColor + "m")
		sb.WriteString(gutter)
		if markerColor != "" {
			sb.WriteString("\x1b[38;5;" + markerColor + "m")
		} else {
			sb.WriteString("\x1b[39m")
		}
		sb.WriteString(marker + " ")
		for _, seg := range highlightSegs(chunk, lexer, st, p) {
			if seg.hasFG {
				sb.WriteString(p.fg.sequence(p.trueColor, seg.r, seg.g, seg.b))
			} else {
				sb.WriteString("\x1b[39m")
			}
			sb.WriteString(seg.text)
		}
		if band {
			visible := gutterW + 1 + 2 + lipgloss.Width(chunk)
			if pad := bandW - visible; pad > 0 {
				sb.WriteString("\x1b[39m" + strings.Repeat(" ", pad))
			}
		}
		sb.WriteString("\x1b[0m")
		rows = append(rows, sb.String())
	}
	return rows
}

// diffGutterWidthFor returns the gutter width needed to right-align the largest
// line number across the given hunks (min 3 to match common editors).
func diffGutterWidthFor(hunks []event.Hunk) int {
	maxNo := 0
	for _, h := range hunks {
		for _, l := range h.Lines {
			if l.NewNo > maxNo {
				maxNo = l.NewNo
			}
			if l.OldNo > maxNo {
				maxNo = l.OldNo
			}
		}
	}
	w := len(strconv.Itoa(maxNo))
	if w < 3 {
		w = 3
	}
	return w
}

// looksLikeTurnDiff reports whether a tool-result body is an edit "turn diff"
// (built by tool.turnDiffSection): a "turn diff: `path` (+N/-M)" header
// followed by a ```diff fenced block. Returns the file path on success.
func looksLikeTurnDiff(content string) (path string, ok bool) {
	lines := strings.Split(content, "\n")
	header := ""
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		header = strings.TrimSpace(l)
		break
	}
	if !strings.HasPrefix(strings.ToLower(header), "turn diff:") {
		return "", false
	}
	if !strings.Contains(content, "```diff") {
		return "", false
	}
	// path lives between the first pair of backticks on the header line.
	if i := strings.IndexByte(header, '`'); i >= 0 {
		if j := strings.IndexByte(header[i+1:], '`'); j >= 0 {
			path = header[i+1 : i+1+j]
		}
	}
	return path, true
}

// extractDiffFence returns the body of the first ```diff fenced block.
func extractDiffFence(content string) string {
	lines := strings.Split(content, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```diff") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return ""
	}
	body := make([]string, 0, len(lines)-start)
	for i := start; i < len(lines); i++ {
		// Match only the exact closing fence (``` with optional trailing
		// whitespace). Diff context lines (e.g. "  ```" or "  ```sh") have a
		// leading space from the unified-diff format and must not prematurely
		// terminate extraction.
		if strings.TrimRight(lines[i], " \t\r") == "```" {
			break
		}
		body = append(body, lines[i])
	}
	return strings.Join(body, "\n")
}

// renderTurnDiffCard renders a turn-diff tool body with the tool-card prefixes
// ("  └ " / "    ") baked in so the caller can print it directly. Returns
// false when the body cannot be parsed as a turn diff, so the caller can fall
// back to the generic renderer.
func renderTurnDiffCard(content string, theme DiffTheme, maxRows int) (string, bool) {
	path, ok := looksLikeTurnDiff(content)
	if !ok {
		return "", false
	}
	fence := extractDiffFence(content)
	if strings.TrimSpace(fence) == "" {
		return "", false
	}
	// event.Parse keys off a "diff --git" header; the difflib body only has
	// "--- path:before" / "+++ path:after". Prepend a synthetic header so the
	// existing hunk/line parsing applies.
	doc := event.Parse("diff --git a/" + path + " b/" + path + "\n" + fence)
	if len(doc.Files) == 0 {
		return "", false
	}
	f := doc.Files[0]

	p := claudeDiffPaletteFor(theme)
	lexer, style := resolveDiffHighlight(path, p)
	gutterW := diffGutterWidthFor(f.Hunks)
	bandW := termWidthOrDefault() - 4 // 4 = tool-card prefix width
	if bandW < 20 {
		bandW = 20
	}

	out := make([]string, 0, 16)
	out = append(out, "  └ "+turnDiffSummaryLine(f.Added, f.Deleted))

	for hi, hunk := range f.Hunks {
		if hi > 0 {
			out = append(out, "    "+p.meta("⋮"))
		}
		for _, line := range hunk.Lines {
			for _, row := range renderClaudeDiffLine(line, p, gutterW, bandW, lexer, style) {
				out = append(out, "    "+row)
			}
		}
	}
	return strings.Join(out, "\n"), true
}

func turnDiffSummaryLine(added, deleted int) string {
	return fmt.Sprintf("Added %s, removed %s", plural(added, "line"), plural(deleted, "line"))
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func planTitle(p event.PlanUpdatedPayload) string {
	if title := strings.TrimSpace(p.Title); title != "" {
		return title
	}
	return "Updated Plan"
}

func planText(p event.PlanUpdatedPayload) string {
	if len(p.Items) == 0 && strings.TrimSpace(p.Explanation) == "" {
		return "(empty plan)"
	}
	var b strings.Builder
	if explanation := strings.TrimSpace(p.Explanation); explanation != "" {
		b.WriteString(explanation)
	}
	for _, item := range p.Items {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(statusGlyph(item.Status))
		b.WriteByte(' ')
		b.WriteString(content)
	}
	if b.Len() == 0 {
		return "(empty plan)"
	}
	return b.String()
}

func statusGlyph(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "done":
		return "✓"
	case "in_progress", "in-progress", "active":
		return "▣"
	case "cancelled", "canceled":
		return "✕"
	default:
		return "□"
	}
}

// Setup pages: the standalone rendering path for rawSelector.
//
// In the interactive chat loop the selector renders THROUGH the renderer: its
// panel becomes the viewport's bottom-pinned overlay. The onboarding runs
// before there is a viewport and owns the terminal outright, so it shows each
// prompt as a page of its own on the alternate screen: the brand and the step
// the setup has reached on top, a rule that fills as the steps go by, and the
// prompt's panel in the rows left. Every paint redraws the whole page, so one
// page replaces the last without leaving anything behind.

// standalone reports whether the selector renders straight to a terminal
// instead of through the viewport renderer.
func (s *rawSelector) standalone() bool {
	return s != nil && s.renderer == nil && s.out != nil
}

// SetupPage records which page of a setup flow the next prompts belong to,
// out of how many; zero leaves the flow. The onboarding numbers its pages
// with it, and every prompt it opens says that Esc goes back once there is a
// page to go back to.
func (s *rawSelector) SetupPage(page, total int) {
	s.setupPage, s.setupPages = page, total
}

// escHint is what Esc does in the prompt being drawn.
func (s *rawSelector) escHint() string {
	switch {
	case s.setupPage > 1:
		return "Esc to go back"
	case s.standalone():
		return "Esc to quit"
	default:
		return "Esc to cancel"
	}
}

// dismissed is the error a prompt closed by key returns: Ctrl+C on a setup
// page quits the setup, while Esc — and Ctrl+C over the conversation — only
// closes the prompt.
func (s *rawSelector) dismissed(key rawKey) error {
	if key == rawKeyCtrlC && s.standalone() {
		return ErrCancelled
	}
	return nil
}

// paintPage draws p as the whole screen.
func (s *rawSelector) paintPage(p slashPanel) {
	width, height := s.termSize()
	lines, caretRow, caretCol := s.pageLines(p, width, height)
	var b strings.Builder
	b.WriteString("\x1b[?25l")
	for i, line := range lines {
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K%s%s", i+1, line, selectorResetStyle)
	}
	if len(lines) < height {
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[J", len(lines)+1)
	}
	if caretRow >= 0 {
		fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[?25h", caretRow+1, caretCol+1)
	}
	_, _ = io.WriteString(s.out, b.String())
}

// pageLines lays a setup page out at the terminal's size: a blank row, the
// brand with the step on the right, a blank row, the progress rule, and the
// panel in the rows left, scrolled so its selection shows. It returns the
// rows and where the field's caret goes (-1 without a field).
func (s *rawSelector) pageLines(p slashPanel, width, height int) ([]string, int, int) {
	width = maxInt(1, width-viewportRightPadding)
	lines := []string{"", pageHeader(width, s.setupPage, s.setupPages), "", pageProgressRule(width, s.setupPage, s.setupPages)}
	layout := p.layout(width, height-len(lines), s.pageTop, true)
	s.pageTop = layout.top
	caretRow := -1
	if layout.caretRow >= 0 {
		caretRow = len(lines) + layout.caretRow
	}
	return append(lines, selfContainedRows(layout.lines)...), caretRow, layout.caretCol
}

// pageHeader is the page's top row: the brand on the left, how far the setup
// has come on the right.
func pageHeader(width, page, pages int) string {
	left := panelIndent + forebrainLogoStyle.Render(bannerProductName)
	if page <= 0 || pages <= 0 {
		return left
	}
	right := fmt.Sprintf("Step %d of %d", page, pages)
	pad := maxInt(2, width-displayLineWidth(left)-displayLineWidth(right))
	return left + strings.Repeat(" ", pad) + right
}

// pageProgressRule is the rule under the header, filled in the accent colour
// as far as the setup has come.
func pageProgressRule(width, page, pages int) string {
	filled := 0
	if pages > 0 {
		filled = min(width, width*min(page, pages)/pages)
	}
	return panelAccentStyle.Render(strings.Repeat("━", filled)) + "\x1b[90m" + strings.Repeat("─", width-filled) + "\x1b[0m"
}

// StatusWriter is where a setup step reports what it is doing without the
// user — waiting on a browser sign-in, fetching models — shown as a page of
// its own under title. It is nil over the conversation, where such reports go
// to the transcript.
func (s *rawSelector) StatusWriter(title string) io.Writer {
	if !s.standalone() {
		return nil
	}
	return &pageStatusWriter{sel: s, title: title}
}

// pageStatusWriter repaints its page with everything written to it so far.
type pageStatusWriter struct {
	sel   *rawSelector
	title string
	text  strings.Builder
}

func (w *pageStatusWriter) Write(p []byte) (int, error) {
	w.text.WriteString(strings.ReplaceAll(string(p), "\r", ""))
	b := newPanelBuilder()
	pickerHead(b, w.title)
	for _, line := range strings.Split(strings.TrimRight(w.text.String(), "\n"), "\n") {
		b.body = append(b.body, panelReportLine(line))
	}
	b.hint("Ctrl+C to quit")
	w.sel.paintPage(b.panel())
	return len(p), nil
}

// NewStandaloneRawSelector wires the raw arrow-key selector into a one-off
// command (the onboarding) that runs outside the main chat loop.
//
// Unlike the chat loop, a standalone command owns the terminal for the entire
// prompt sequence, so this helper:
//   - puts the terminal fd into raw mode (so the selector can read arrow keys
//     byte-by-byte),
//   - switches to the alternate screen, where the selector draws its pages,
//     and
//   - wraps out in a CRLF-translating writer so plain "\n" written while raw
//     mode is active renders correctly.
//
// It returns the selector, the wrapped writer the caller must use for ALL
// output during the prompt sequence, and a restore closure that leaves the
// alternate screen and raw mode and re-shows the cursor. The closure is safe
// to call multiple times.
//
// ok is false when f is nil or not a terminal (e.g. piped stdin); callers
// should fall back to a line-based selector in that case. When ok is false the
// returned writer is out unchanged and restore is a no-op.
func NewStandaloneRawSelector(f *os.File, out io.Writer) (selector Selector, wrapped io.Writer, restore func(), ok bool) {
	noop := func() {}
	if out == nil {
		out = io.Discard
	}
	if f == nil || !term.IsTerminal(int(f.Fd())) {
		return nil, out, noop, false
	}
	session, err := newRawTerminalSession(f)
	if err != nil || session == nil {
		return nil, out, noop, false
	}
	if err := session.EnterRaw(); err != nil {
		return nil, out, noop, false
	}
	w := newTTYNewlineWriter(out)
	if w == nil {
		w = out
	}
	restored := false
	restore = func() {
		if restored {
			return
		}
		restored = true
		// Leave the pages behind and reset attributes, even if a selector
		// exited mid-redraw. The caret is the caller's: the onboarding hands
		// it back to the shell, a launch keeps it hidden until the TUI.
		_, _ = fmt.Fprint(out, "\x1b[?1049l\x1b[0m")
		_ = session.Restore()
	}
	_, _ = fmt.Fprint(out, "\x1b[?1049h\x1b[H\x1b[2J")
	return newStandaloneRawSelector(f, w), w, restore, true
}

var (
	mkdirAll = os.MkdirAll
	openFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		return telemetry.OpenRaw(name, flag, perm, telemetry.Options{})
	}
	fileWrite           = func(f *os.File, p []byte) (int, error) { return telemetry.WriteRaw(f, p, 0, 0) }
	fileSync            = telemetry.SyncRaw
	fileClose           = telemetry.CloseRaw
	attachStderrToError = attachProcessStderrToErrorLog
)

type tuiLogSink struct {
	mu       *sync.Mutex
	dbgF     *os.File
	infF     *os.File
	errF     *os.File
	minLevel slog.Level
	attrs    []slog.Attr
}

func (s *tuiLogSink) pickFile(level slog.Level) *os.File {
	switch {
	case level < slog.LevelInfo:
		return s.dbgF
	case level < slog.LevelWarn:
		return s.infF
	default:
		return s.errF
	}
}

func (s *tuiLogSink) Enabled(_ context.Context, level slog.Level) bool {
	return level >= s.minLevel
}

func (s *tuiLogSink) Handle(ctx context.Context, r slog.Record) error {
	f := s.pickFile(r.Level)
	if f == nil {
		return nil
	}
	r2 := r.Clone()
	if len(s.attrs) > 0 {
		r2.AddAttrs(s.attrs...)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	th := slog.NewTextHandler(telemetry.RawWriter{File: f}, &slog.HandlerOptions{
		AddSource: true,
		Level:     slog.LevelDebug,
	})
	if err := th.Handle(ctx, r2); err != nil {
		return err
	}
	if err := fileSync(f); err != nil {
		return err
	}
	return nil
}

func (s *tuiLogSink) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &tuiLogSink{
		mu:       s.mu,
		dbgF:     s.dbgF,
		infF:     s.infF,
		errF:     s.errF,
		minLevel: s.minLevel,
		attrs:    append(append([]slog.Attr{}, s.attrs...), attrs...),
	}
}

func (s *tuiLogSink) WithGroup(string) slog.Handler {
	return s
}

type tuiStdLogWriter struct{ s *tuiLogSink }

func logLineLooksErrory(low []byte) bool {
	return bytes.Contains(low, []byte("err=")) ||
		bytes.Contains(low, []byte("panic")) ||
		bytes.Contains(low, []byte("fatal")) ||
		bytes.Contains(low, []byte("failed")) ||
		bytes.Contains(low, []byte("handler_error")) ||
		bytes.Contains(low, []byte("docker error"))
}

func (w tuiStdLogWriter) Write(p []byte) (int, error) {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	if w.s.infF == nil {
		return 0, nil
	}
	n, err := fileWrite(w.s.infF, p)
	if err != nil {
		return n, err
	}
	if err := fileSync(w.s.infF); err != nil {
		return n, err
	}
	low := bytes.ToLower(p)
	if w.s.errF != nil && logLineLooksErrory(low) {
		if n2, e2 := fileWrite(w.s.errF, p); e2 != nil {
			if n2 > 0 {
				return n + n2, e2
			}
			return n, e2
		}
		if err := fileSync(w.s.errF); err != nil {
			return n, err
		}
	}
	return n, nil
}

func closeFiles(dbgF, infF, errF *os.File) error {
	var errs []error
	for _, f := range []*os.File{dbgF, infF, errF} {
		if f == nil {
			continue
		}
		if err := fileClose(f); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func RedirectProcessLoggingForTUI(home string) (restore func()) {
	home = strings.TrimSpace(home)
	if home == "" {
		return redirectLoggingToDiscard()
	}
	dir := filepath.Join(home, homepkg.LogsDir)
	if err := mkdirAll(dir, 0o755); err != nil {
		return redirectLoggingToDiscard()
	}
	flags := os.O_APPEND | os.O_CREATE | os.O_WRONLY
	debugPath := filepath.Join(dir, "debug.log")
	infoPath := filepath.Join(dir, "info.log")
	errorPath := filepath.Join(dir, "error.log")
	dbgF, err1 := openFile(debugPath, flags, 0o644)
	infF, err2 := openFile(infoPath, flags, 0o644)
	errF, err3 := openFile(errorPath, flags, 0o644)
	if err1 != nil || err2 != nil || err3 != nil {
		if cErr := closeFiles(dbgF, infF, errF); cErr != nil {
			return redirectLoggingToDiscard()
		}
		return redirectLoggingToDiscard()
	}
	restoreStderr := attachStderrToError(dir)
	stopRotation := telemetry.StartPeriodicRotation([]string{debugPath, infoPath, errorPath}, 0)
	mu := &sync.Mutex{}
	sink := &tuiLogSink{
		mu:       mu,
		dbgF:     dbgF,
		infF:     infF,
		errF:     errF,
		minLevel: slogLevelForTUILogFile(),
	}
	oldW := log.Writer()
	log.SetOutput(tuiStdLogWriter{sink})
	prev := slog.Default()
	slog.SetDefault(slog.New(sink))
	return func() {
		restoreStderr()
		stopRotation()
		log.SetOutput(oldW)
		slog.SetDefault(prev)
		_ = closeFiles(dbgF, infF, errF)
	}
}

func redirectLoggingToDiscard() (restore func()) {
	w := log.Writer()
	log.SetOutput(io.Discard)
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	return func() {
		log.SetOutput(w)
		slog.SetDefault(prev)
	}
}

func slogLevelForTUILogFile() slog.Level {
	for _, env := range []string{"LOG_LEVEL", "FOREBRAIN_LOG_LEVEL"} {
		switch strings.ToLower(strings.TrimSpace(os.Getenv(env))) {
		case "error":
			return slog.LevelError
		case "warn", "warning":
			return slog.LevelWarn
		case "info":
			return slog.LevelInfo
		case "debug":
			return slog.LevelDebug
		}
	}
	return slog.LevelDebug
}

var _ slog.Handler = (*tuiLogSink)(nil)

// --- slash panels ---
//
// Every overlay a slash command opens — the command menu under the composer,
// the pickers, the forms, the reports and the /status and /mcp panels — is a
// slashPanel: a head pinned at the top, a body scrolled within the rows left
// over, and the key hint pinned at the bottom, drawn in one palette and as
// tall as slashPanelHeight whatever it holds. A panel keeps its content as
// logical lines and is laid out afresh at every paint, so it reflows when the
// terminal is resized.

// The panel palette: titles and labels in the accent colour, values at the
// terminal's own foreground — the brightness of assistant text, never dim —
// and each state glyph coloured by what it says. Only the key hint and the
// scroll note are faint. No pink anywhere.
var (
	panelAccentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("110"))
	panelTitleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("110")).Bold(true)
	panelTabStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("110")).Bold(true).Underline(true)
	panelHintStyle   = lipgloss.NewStyle().Faint(true)
	panelOKStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("70"))
	panelFailStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	panelWarnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	panelGlyphStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// panelIndent is the left margin of every panel row; the selected row puts
// its marker there instead.
const panelIndent = "  "

// panelCursor marks a panel's selected row, in the margin panelIndent keeps.
var panelCursor = panelAccentStyle.Render(selectorCursorGlyph) + " "

// slashPanelHeight is how many screen rows every slash panel takes, the rule
// that separates it from the conversation included: half the terminal, so the
// conversation keeps the other half.
func slashPanelHeight(termH int) int {
	return termH / 2
}

// panelLine is one logical line of a panel: text wrapped at the panel's width
// under lead, each continuation row hanging under the text. lead carries its
// own styling; style, when set, colours every wrapped chunk of the text.
type panelLine struct {
	lead  string
	text  string
	style *lipgloss.Style
}

// rows wraps the line at width.
func (l panelLine) rows(width int) []string {
	leadWidth := displayLineWidth(l.lead)
	chunks := wrapPanelWords(l.text, maxInt(1, width-leadWidth))
	rows := make([]string, len(chunks))
	for i, chunk := range chunks {
		if l.style != nil && chunk != "" {
			chunk = l.style.Render(chunk)
		}
		lead := l.lead
		if i > 0 {
			lead = strings.Repeat(" ", leadWidth)
		}
		rows[i] = lead + chunk
	}
	return rows
}

// panelField is the one-line text field that can close a panel's head — a
// picker's filter, a form's answer — with the caret inside it. An empty field
// shows its placeholder.
type panelField struct {
	text        []rune
	cursor      int
	placeholder string
}

// panelFieldLead is what precedes a field's text: the composer's own prompt
// marker, so everything the user types into looks the same.
const panelFieldLead = composerPromptMarker

func (f panelField) row() string {
	if len(f.text) == 0 && f.placeholder != "" {
		return panelAccentStyle.Render(panelFieldLead) + panelHintStyle.Render(f.placeholder)
	}
	return panelAccentStyle.Render(panelFieldLead) + string(f.text)
}

// caretCol is the display column the caret stands at on the field's row.
func (f panelField) caretCol() int {
	cursor := composerCursorClampRunes(f.text, f.cursor)
	return displayLineWidth(panelFieldLead) + runewidth.StringWidth(string(f.text[:cursor]))
}

// slashPanel is one panel's content.
type slashPanel struct {
	head  []panelLine
	field *panelField
	body  []panelLine
	// focusStart and focusEnd are the body lines [start, end) kept in view
	// when the selection moves: the selected row, with the heading of the
	// group it opens. Equal when the body has no selection.
	focusStart, focusEnd int
	hint                 string
}

// panelLayout is a panel laid out in a given number of rows.
type panelLayout struct {
	lines []string
	// caretRow and caretCol place the field's caret; caretRow is -1 when the
	// panel has no field.
	caretRow, caretCol int
	// top is the first body row shown, maxTop the furthest it can go.
	top, maxTop int
}

// fieldRow is the row the panel's field sits on at width, -1 without one.
func (p slashPanel) fieldRow(width int) int {
	if p.field == nil {
		return -1
	}
	row := 0
	for _, l := range p.head {
		row += len(l.rows(width))
	}
	return row
}

// layout lays the panel out at width in rows screen rows: the head, a blank
// row, the body from its row top — moved first, when follow is set, just far
// enough to show the focus whole — padded to fill the room, a row saying how
// much of the body is out of view, and the hint. rows <= 0 lays the whole
// body out, for a terminal the panel is not pinned in.
func (p slashPanel) layout(width, rows, top int, follow bool) panelLayout {
	out := panelLayout{caretRow: -1}
	for _, l := range p.head {
		out.lines = append(out.lines, l.rows(width)...)
	}
	if p.field != nil {
		out.caretRow, out.caretCol = len(out.lines), p.field.caretCol()
		out.lines = append(out.lines, p.field.row())
	}
	out.lines = append(out.lines, "")

	var body []string
	focusStart, focusEnd := 0, 0
	for i, l := range p.body {
		if i == p.focusStart {
			focusStart = len(body)
		}
		body = append(body, l.rows(width)...)
		if i == p.focusEnd-1 {
			focusEnd = len(body)
		}
	}
	var hint []string
	if p.hint != "" {
		hint = panelLine{lead: panelIndent, text: p.hint, style: &panelHintStyle}.rows(width)
	}
	if rows <= 0 {
		out.lines = append(out.lines, body...)
		out.lines = append(out.lines, "")
		out.lines = append(out.lines, hint...)
		return out
	}

	room := maxInt(1, rows-len(out.lines)-1-len(hint))
	out.maxTop = maxInt(0, len(body)-room)
	if follow && p.focusEnd > p.focusStart {
		if focusEnd > top+room {
			top = focusEnd - room
		}
		if focusStart < top {
			top = focusStart
		}
	}
	out.top = clampInt(top, 0, out.maxTop)
	shown := body[out.top:min(out.top+room, len(body))]
	out.lines = append(out.lines, shown...)
	for i := len(shown); i < room; i++ {
		out.lines = append(out.lines, "")
	}
	out.lines = append(out.lines, panelScrollNote(out.top, len(body)-out.top-len(shown)))
	out.lines = append(out.lines, hint...)
	return out
}

// panelScrollNote says how many body rows are out of view above and below,
// or nothing when the body fits.
func panelScrollNote(above, below int) string {
	parts := make([]string, 0, 2)
	if above > 0 {
		parts = append(parts, fmt.Sprintf("↑ %d more", above))
	}
	if below > 0 {
		parts = append(parts, fmt.Sprintf("↓ %d more", below))
	}
	if len(parts) == 0 {
		return ""
	}
	return panelIndent + panelHintStyle.Render(strings.Join(parts, " · "))
}

// drawUIPanel paints the /status or /mcp panel into the overlay area.
func drawUIPanel(renderer *Renderer, state *streamState) {
	if renderer == nil || state == nil || state.panel == nil {
		return
	}
	renderer.SetOverlayPanel(buildUIPanel(state.panel, panelSpinnerGlyph()), state.panel.top, false)
	state.panel.top = false
}

// panelSpinnerGlyph animates a connecting server from the wall clock; the
// panel repaints on the startup's ticks.
func panelSpinnerGlyph() string {
	return spinnerGlyphs[int(time.Now().UnixMilli()/120)%len(spinnerGlyphs)]
}

// buildUIPanel renders the /status or /mcp panel's content. It is a pure
// function of the panel, so tests can lay it out at any width.
func buildUIPanel(panel *uiPanel, spinner string) slashPanel {
	b := newPanelBuilder()
	if panel == nil {
		return b.panel()
	}
	switch panel.kind {
	case "status":
		buildStatusPanel(b, panel)
	case "mcp":
		buildMCPPanel(b, panel, spinner)
	}
	return b.panel()
}

// panelBuilder accumulates a panel's lines.
type panelBuilder struct {
	head                 []panelLine
	body                 []panelLine
	focusStart, focusEnd int
	hintText             string
	// heading is the body line of the open group's heading, -1 before the
	// first group; grouped counts the selectable rows added under it.
	heading, grouped int
}

func newPanelBuilder() *panelBuilder { return &panelBuilder{heading: -1} }

func (b *panelBuilder) panel() slashPanel {
	return slashPanel{head: b.head, body: b.body, focusStart: b.focusStart, focusEnd: b.focusEnd, hint: b.hintText}
}

// title adds a row to the pinned head.
func (b *panelBuilder) title(text string) {
	b.head = append(b.head, panelLine{lead: panelIndent, text: text, style: &panelTitleStyle})
}

// subtitle adds a plain row to the pinned head, under the title.
func (b *panelBuilder) subtitle(text string) {
	b.head = append(b.head, panelLine{lead: panelIndent, text: text})
}

// text adds styled body text wrapped under prefix.
func (b *panelBuilder) text(prefix, text string, style *lipgloss.Style) {
	b.body = append(b.body, panelLine{lead: prefix, text: text, style: style})
}

func (b *panelBuilder) blank() { b.body = append(b.body, panelLine{}) }

// group opens a group of rows under its heading, a blank row before it
// unless it is the first thing in the body.
func (b *panelBuilder) group(heading string) {
	if len(b.body) > 0 {
		b.blank()
	}
	b.heading, b.grouped = len(b.body), 0
	b.text(panelIndent, heading, &panelAccentStyle)
}

// wrapPanelWords folds text at spaces to width display cells; only a token
// wider than the whole width (a long path, an id) is broken inside itself.
// Styled spans may cross a fold: the renderer makes every row self-contained.
func wrapPanelWords(text string, width int) []string {
	var lines []string
	cur, curW := "", 0
	for _, word := range strings.Split(text, " ") {
		if word == "" {
			continue
		}
		w := displayLineWidth(word)
		if w > width {
			if cur != "" {
				lines = append(lines, cur)
			}
			chunks := wrapCardLine(word, width)
			lines = append(lines, chunks[:len(chunks)-1]...)
			cur, curW = chunks[len(chunks)-1], displayLineWidth(chunks[len(chunks)-1])
			continue
		}
		switch {
		case cur == "":
			cur, curW = word, w
		case curW+1+w <= width:
			cur, curW = cur+" "+word, curW+1+w
		default:
			lines = append(lines, cur)
			cur, curW = word, w
		}
	}
	if cur != "" || len(lines) == 0 {
		lines = append(lines, cur)
	}
	return lines
}

func (b *panelBuilder) hint(text string) { b.hintText = text }

// facts renders "Label: value" rows with the values aligned in one column; a
// long value wraps inside that column, never across the panel edge.
func (b *panelBuilder) facts(facts []turn.StatusFact, styled map[string]string) {
	labelWidth := 0
	for _, f := range facts {
		labelWidth = maxInt(labelWidth, displayLineWidth(f.Label)+1)
	}
	for _, f := range facts {
		label := panelAccentStyle.Render(f.Label + ":")
		prefix := panelIndent + label + strings.Repeat(" ", labelWidth-displayLineWidth(f.Label)-1+2)
		value := f.Value
		if v, ok := styled[f.Label]; ok {
			value = v
		}
		b.text(prefix, value, nil)
	}
}

// selectable adds one cursor-addressable row, marking it and making it the
// focus when it is the selected one. lead follows the marker's margin: a check
// box, a name column. The first row of a group takes the group's heading into
// its focus, so bringing it into view never leaves the heading out.
func (b *panelBuilder) selectable(selected bool, lead, text string) {
	if selected {
		b.focusStart, b.focusEnd = len(b.body), len(b.body)+1
		if b.heading >= 0 && b.grouped == 0 {
			b.focusStart = b.heading
		}
		b.text(panelCursor+lead, text, nil)
	} else {
		b.text(panelIndent+lead, text, nil)
	}
	b.grouped++
}

func buildStatusPanel(b *panelBuilder, panel *uiPanel) {
	rep := panel.status
	tabs := []string{"Status", "Usage"}
	var tabLine strings.Builder
	tabLine.WriteString(panelIndent)
	for i, tab := range tabs {
		if i > 0 {
			tabLine.WriteString("   ")
		}
		if i == panel.tab {
			tabLine.WriteString(panelTabStyle.Render(tab))
		} else {
			tabLine.WriteString(tab)
		}
	}
	if rep.Side {
		tabLine.WriteString("   " + panelAccentStyle.Render("side conversation"))
	}
	b.head = append(b.head, panelLine{lead: tabLine.String()})
	if panel.tab == 0 {
		b.facts(turn.StatusFacts(*rep), map[string]string{"MCP servers": panelMCPCounts(rep.MCP)})
	} else if usage := turn.UsageFacts(*rep); len(usage) > 0 {
		b.facts(usage, nil)
	} else {
		b.text(panelIndent, "No usage recorded yet — it appears after the first turn", nil)
	}
	b.hint("Esc to close · Tab to switch · PgUp/PgDn to scroll")
}

// panelMCPCounts is the /status MCP row with each count in its state's colour.
func panelMCPCounts(l turn.MCPCountLine) string {
	parts := make([]string, 0, 8)
	add := func(n int, label string, style *lipgloss.Style) {
		if n <= 0 {
			return
		}
		text := fmt.Sprintf("%d %s", n, label)
		if style != nil {
			text = style.Render(text)
		}
		parts = append(parts, text)
	}
	add(l.Connected, "connected", &panelOKStyle)
	add(l.Connecting, "connecting", nil)
	add(l.Failed, "failed", &panelFailStyle)
	add(l.NeedsAuth, "needs authentication", &panelWarnStyle)
	add(l.Idle, "idle", nil)
	add(l.Skipped, "skipped", nil)
	add(l.Disabled, "disabled", nil)
	add(l.Unknown, "unknown", nil)
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ") + " · /mcp"
}

func buildMCPPanel(b *panelBuilder, panel *uiPanel, spinner string) {
	switch panel.page {
	case "detail":
		buildMCPDetail(b, panel, spinner)
	case "tools":
		buildMCPTools(b, panel)
	case "tool":
		buildMCPTool(b, panel)
	case "resources":
		buildMCPResources(b, panel)
	default:
		buildMCPList(b, panel, spinner)
	}
}

// mcpStatusText is one server's state with its glyph coloured by meaning.
func mcpStatusText(e turn.MCPServerEntry, spinner string) string {
	label := turn.MCPServerStatusLabel(e)
	if e.Status == turn.MCPStatusConnecting {
		return panelGlyphStyle.Render(spinner) + " " + label
	}
	glyph, rest, found := strings.Cut(label, " ")
	if !found {
		return label
	}
	switch e.Status {
	case turn.MCPStatusConnected:
		return panelOKStyle.Render(glyph) + " " + rest
	case turn.MCPStatusFailed:
		return panelFailStyle.Render(glyph) + " " + rest
	case turn.MCPStatusNeedsAuth:
		return panelWarnStyle.Render(glyph) + " " + rest
	case turn.MCPStatusIdle, turn.MCPStatusSkipped, turn.MCPStatusDisabled:
		return panelGlyphStyle.Render(glyph) + " " + rest
	default:
		return label
	}
}

func groupTitle(group turn.MCPScopeGroup) string {
	if group.Source == "" {
		return group.Label
	}
	return group.Label + " (" + group.Source + ")"
}

func buildMCPList(b *panelBuilder, panel *uiPanel, spinner string) {
	inv := panel.inv
	b.title("Manage MCP servers")
	if inv.Total == 0 {
		b.subtitle("No MCP servers configured")
		if inv.GlobalSource != "" {
			b.text(panelIndent, "Global servers live in "+inv.GlobalSource, nil)
		}
		if inv.ProjectSource != "" {
			b.text(panelIndent, "Project servers live in "+inv.ProjectSource, nil)
		}
	} else {
		b.subtitle(turnCountNoun(inv.Total, "server"))
	}
	row := 0
	for _, group := range inv.Groups {
		b.group(groupTitle(group))
		for _, e := range group.Entries {
			b.selectable(row == panel.cursor, "", e.Name+" · "+mcpStatusText(e, spinner))
			row++
		}
	}
	notes := make([]string, 0, 4)
	for _, note := range inv.NotInEffect {
		notes = append(notes, "Not in effect: "+note)
	}
	if len(inv.OverriddenGlobal) > 0 {
		notes = append(notes, "Global entries this project overrides: "+strings.Join(inv.OverriddenGlobal, ", "))
	}
	if inv.PendingChanges {
		notes = append(notes, "MCP config changes on disk take effect in a new session")
	}
	if inv.DisableStoreError != "" {
		notes = append(notes, "Disable store: "+inv.DisableStoreError)
	}
	if inv.ForeignConfig != "" {
		notes = append(notes, "Another agent's MCP config is here ("+inv.ForeignConfig+"); /migrate can import it")
	}
	if inv.ErrorLogPath != "" {
		notes = append(notes, "Error logs: "+inv.ErrorLogPath)
	}
	if len(notes) > 0 {
		if len(b.body) > 0 {
			b.blank()
		}
		for _, note := range notes {
			b.text(panelIndent, note, nil)
		}
	}
	if inv.Total == 0 {
		b.hint("Esc to close")
		return
	}
	b.hint("↑/↓ to navigate · Enter to open · Esc to close")
}

func buildMCPDetail(b *panelBuilder, panel *uiPanel, spinner string) {
	entry := panel.inv.Entry(panel.server)
	if entry == nil {
		b.text(panelIndent, panel.server+" is no longer in this session's MCP list", nil)
		b.hint("Esc to go back")
		return
	}
	b.title(entry.Name + " MCP server")
	var facts []turn.StatusFact
	add := func(label, value string) {
		if strings.TrimSpace(value) != "" {
			facts = append(facts, turn.StatusFact{Label: label, Value: value})
		}
	}
	add("Status", turn.MCPServerStatusLabel(*entry))
	add("Scope", strings.TrimSuffix(entry.Scope+" · "+entry.Source, " · "))
	add("Transport", entry.Transport)
	add("Command", entry.Command)
	add("URL", entry.URL)
	if len(entry.EnvKeys) > 0 {
		add("Env", strings.Join(entry.EnvKeys, ", ")+" (values hidden)")
	}
	if len(entry.HeaderKeys) > 0 {
		add("Headers", strings.Join(entry.HeaderKeys, ", ")+" (values hidden)")
	}
	add("Auth", entry.Auth)
	if entry.Status == turn.MCPStatusFailed {
		add("Error", entry.Error)
	}
	if entry.Required {
		add("Required", "yes — required by config, so it cannot be disabled")
	}
	b.facts(facts, map[string]string{"Status": mcpStatusText(*entry, spinner)})
	if flow := panel.auth[entry.Name]; flow != nil {
		b.blank()
		switch {
		case flow.result != "":
			b.text(panelIndent, flow.result, nil)
		case flow.url != "":
			b.text(panelIndent, "Open this URL to authenticate, then finish in the browser:", nil)
			b.text(panelIndent, flow.url, nil)
		default:
			b.text(panelIndent, "Starting authentication…", nil)
		}
	}
	if notice := panel.notice[entry.Name]; notice != "" {
		b.blank()
		b.text(panelIndent, notice, nil)
	}
	rows := panelSelectableRows(panel)
	if len(rows) > 0 {
		b.blank()
		for i, row := range rows {
			b.selectable(i == panel.cursor, "", row.label)
		}
		b.hint("↑/↓ to navigate · Enter to confirm · Esc to go back")
		return
	}
	b.hint("Esc to go back")
}

func buildMCPTools(b *panelBuilder, panel *uiPanel) {
	entry := panel.inv.Entry(panel.server)
	b.title(panel.server + " · tools")
	if entry == nil || len(entry.Tools) == 0 {
		b.text(panelIndent, "No tools from this server in this session", nil)
		b.hint("Esc to go back")
		return
	}
	b.subtitle(turnCountNoun(len(entry.Tools), "tool") + " exposed to the model this session")
	for i, t := range entry.Tools {
		b.selectable(i == panel.cursor, "", t.Name)
	}
	b.hint("↑/↓ to navigate · Enter for details · Esc to go back")
}

func buildMCPTool(b *panelBuilder, panel *uiPanel) {
	b.title(panel.server + " · " + panel.tool)
	var tool *turn.MCPToolSummary
	if entry := panel.inv.Entry(panel.server); entry != nil {
		for i := range entry.Tools {
			if entry.Tools[i].Name == panel.tool {
				tool = &entry.Tools[i]
				break
			}
		}
	}
	if tool == nil {
		b.text(panelIndent, panel.tool+" is no longer in this session's tool table", nil)
		b.hint("Esc to go back")
		return
	}
	if tool.Description != "" {
		for _, line := range strings.Split(tool.Description, "\n") {
			b.text(panelIndent, line, nil)
		}
		b.blank()
	}
	if len(tool.Params) == 0 {
		b.text(panelIndent, "No parameters", nil)
	} else {
		b.text(panelIndent, "Parameters", &panelAccentStyle)
		renderPanelParams(b, tool.Params, 0)
	}
	b.hint("PgUp/PgDn to scroll · Esc to go back")
}

// renderPanelParams draws the parameter table: one row per parameter — name,
// type, required, enum values, default — with its description beneath it and
// nested object properties indented under their parent.
func renderPanelParams(b *panelBuilder, params []turn.MCPToolParam, depth int) {
	indent := panelIndent + strings.Repeat("  ", depth)
	for _, p := range params {
		facts := make([]string, 0, 4)
		if p.Type != "" {
			facts = append(facts, p.Type)
		}
		if p.Required {
			facts = append(facts, panelWarnStyle.Render("required"))
		}
		if len(p.Enum) > 0 {
			facts = append(facts, "one of "+strings.Join(p.Enum, " | "))
		}
		if p.Default != "" {
			facts = append(facts, "default "+p.Default)
		}
		row := panelAccentStyle.Render(p.Name)
		if len(facts) > 0 {
			row += "  " + strings.Join(facts, " · ")
		}
		b.text(indent, row, nil)
		if p.Description != "" {
			for _, line := range strings.Split(p.Description, "\n") {
				b.text(indent+"  ", line, nil)
			}
		}
		if len(p.Children) > 0 {
			renderPanelParams(b, p.Children, depth+1)
		}
	}
}

func buildMCPResources(b *panelBuilder, panel *uiPanel) {
	b.title(panel.server + " · resources")
	res := panel.resources[panel.server]
	switch {
	case res == nil || res.loading:
		b.text(panelIndent, "Loading resources…", nil)
	case res.err != "":
		b.text(panelIndent, panelFailStyle.Render("✗")+" "+res.err, nil)
	case len(res.items) == 0:
		b.text(panelIndent, "This server advertises no resources", nil)
	default:
		for _, item := range res.items {
			name := firstNonEmpty(item.Name, item.URI)
			b.text(panelIndent, panelAccentStyle.Render(name), nil)
			if item.URI != "" && item.URI != name {
				b.text(panelIndent+"  ", item.URI, nil)
			}
		}
	}
	b.hint("PgUp/PgDn to scroll · Esc to go back")
}

// turnCountNoun renders "1 server" / "3 servers".
func turnCountNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
