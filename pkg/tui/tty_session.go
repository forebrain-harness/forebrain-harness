// The TTY session, its writers, and the theme/view helpers.
package tui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"golang.org/x/term"
)

type rawTerminalSession struct {
	mu           sync.Mutex
	fd           int
	baseState    *term.State
	rawActive    bool
	suspendDepth int
	resumeOnZero bool
}

func newRawTerminalSession(file *os.File) (*rawTerminalSession, error) {
	if file == nil {
		return nil, nil
	}
	fd := int(file.Fd())
	baseState, err := term.GetState(fd)
	if err != nil {
		return nil, err
	}
	if baseState == nil {
		return nil, nil
	}
	return &rawTerminalSession{fd: fd, baseState: baseState}, nil
}

func (s *rawTerminalSession) EnterRaw() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rawActive {
		return nil
	}
	if s.suspendDepth > 0 {
		s.resumeOnZero = true
		return nil
	}
	_, err := term.MakeRaw(s.fd)
	if err != nil {
		return err
	}
	s.rawActive = true
	return nil
}

func (s *rawTerminalSession) Restore() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resumeOnZero = false
	if !s.rawActive {
		return nil
	}
	if err := term.Restore(s.fd, s.baseState); err != nil {
		return err
	}
	s.rawActive = false
	return nil
}

func (s *rawTerminalSession) Suspend() func() {
	if s == nil {
		return func() {}
	}
	s.mu.Lock()
	if s.suspendDepth == 0 {
		s.resumeOnZero = s.rawActive
		if s.rawActive {
			_ = term.Restore(s.fd, s.baseState)
			s.rawActive = false
		}
	}
	s.suspendDepth++
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.suspendDepth == 0 {
			return
		}
		s.suspendDepth--
		if s.suspendDepth != 0 {
			return
		}
		if s.resumeOnZero {
			if _, err := term.MakeRaw(s.fd); err == nil {
				s.rawActive = true
			}
		}
		s.resumeOnZero = false
	}
}

type ttyNewlineWriter struct {
	mu     sync.Mutex
	dst    io.Writer
	lastCR bool
}

func newTTYNewlineWriter(dst io.Writer) io.Writer {
	if dst == nil {
		return nil
	}
	return &ttyNewlineWriter{dst: dst}
}

func (w *ttyNewlineWriter) Write(p []byte) (int, error) {
	if w == nil || w.dst == nil || len(p) == 0 {
		return len(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	buf := make([]byte, 0, len(p)+8)
	for _, b := range p {
		switch b {
		case '\n':
			if !w.lastCR {
				buf = append(buf, '\r')
			}
			buf = append(buf, '\n')
			w.lastCR = false
		case '\r':
			buf = append(buf, '\r')
			w.lastCR = true
		default:
			buf = append(buf, b)
			w.lastCR = false
		}
	}
	_, err := w.dst.Write(buf)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// syncWriter wraps an io.Writer with a mutex so renderer and transient status
// producers can write without interleaving bytes.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func newSyncWriter(w io.Writer) *syncWriter {
	if w == nil {
		w = io.Discard
	}
	return &syncWriter{w: w}
}

// Write serializes byte writes to the underlying writer.
func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// orphanSGRMousePattern matches the printable remainder of an SGR mouse report
// after its ESC prefix was lost by a terminal, clipboard, or multiplexer.
// Keep this intentionally narrow: only the three numeric fields used by SGR
// mouse reports are removed.
var orphanSGRMousePattern = regexp.MustCompile(`(?:\[)?<[0-9]+;[0-9]+;[0-9]+[Mm]`)

// sanitizeTerminalInputText removes terminal control sequences from text that
// will be rendered in the composer or copied from the viewport. It preserves
// ordinary Unicode text plus tabs and newlines (normalizing CRLF/CR to LF), while treating incomplete
// control sequences as control data rather than allowing them to desynchronize
// the terminal cursor from the composer's rune cursor.
func sanitizeTerminalInputText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(s); {
		switch s[i] {
		case 0x1b: // ESC
			if i+1 >= len(s) {
				return orphanSGRMousePattern.ReplaceAllString(b.String(), "")
			}
			switch s[i+1] {
			case '[': // CSI: ESC [ parameters/intermediates final-byte
				i += 2
				for i < len(s) {
					if s[i] >= 0x40 && s[i] <= 0x7e {
						i++
						break
					}
					i++
				}
				continue
			case ']': // OSC: terminated by BEL or ST (ESC \\)
				i += 2
				for i < len(s) {
					if s[i] == 0x07 {
						i++
						break
					}
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
				continue
			default:
				// Two-byte ESC controls (charset selection, save/restore cursor,
				// etc.) are never composer content.
				i += 2
				continue
			}
		case '\n', '\t':
			b.WriteByte(s[i])
		case 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
			0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1c, 0x1d, 0x1e, 0x1f, 0x7f:
			// C0/DEL controls cannot be safely rendered in a text composer.
		default:
			b.WriteByte(s[i])
		}
		i++
	}
	return orphanSGRMousePattern.ReplaceAllString(b.String(), "")
}

// sanitizeTerminalDraft returns sanitized text and maps a rune cursor from the
// original text into that sanitized text. This lets input boundaries recover
// from terminal-control contamination without cursor drift.
func sanitizeTerminalDraft(text string, cursor int) (string, int) {
	runes := []rune(text)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}
	clean := sanitizeTerminalInputText(text)
	cleanPrefix := sanitizeTerminalInputText(string(runes[:cursor]))
	return clean, len([]rune(cleanPrefix))
}

// terminalTitleTick is how often the animated terminal title advances one
// spinner frame. It matches the transient "Working" status cadence so the
// two animations feel like one turn.
const terminalTitleTick = 200 * time.Millisecond

// sanitizeTerminalTitleText strips control characters (ESC, BEL, newlines,
// C0/C1 controls, DEL) from a title so session content can never inject
// OSC/CSI sequences through the terminal title channel.
func sanitizeTerminalTitleText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// writeTerminalTitle sets the terminal window and tab title via the OSC 0
// sequence. Terminal emulators that compose the title bar (e.g. macOS
// Terminal.app shows "<cwd> — <title>" in the header and "<dir> — <title>"
// on the tab) pick the change up automatically.
func writeTerminalTitle(w io.Writer, title string) {
	if w == nil {
		return
	}
	_, _ = io.WriteString(w, "\x1b]0;"+title+"\x07")
}

// terminalTitleAnimator drives the animated terminal title while a turn is
// in flight and restores a static title when the session goes idle. All
// entry points are safe for nil receivers so call sites need no guards.
type terminalTitleAnimator struct {
	out io.Writer

	mu      sync.Mutex
	running bool
	base    string
	phase   int
	stop    chan struct{}
	done    chan struct{}
}

func newTerminalTitleAnimator(out io.Writer) *terminalTitleAnimator {
	return &terminalTitleAnimator{out: out}
}

// Animate starts the spinner animation with base as the title body (the
// session title). Calling it again while running only retargets the body.
func (a *terminalTitleAnimator) Animate(base string) {
	if a == nil || a.out == nil {
		return
	}
	base = sanitizeTerminalTitleText(base)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.base = base
	if a.running {
		return
	}
	a.running = true
	a.stop = make(chan struct{})
	a.done = make(chan struct{})
	writeTerminalTitle(a.out, a.frameLocked(base))
	go a.loop(a.stop, a.done)
}

func (a *terminalTitleAnimator) loop(stop, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(terminalTitleTick)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			a.mu.Lock()
			a.phase++
			base := a.base
			frame := a.frameLocked(base)
			a.mu.Unlock()
			writeTerminalTitle(a.out, frame)
		}
	}
}

// frameLocked renders one animated frame: the rotating spinner glyph plus
// the title body. It reuses the Braille spinner the in-app running-tool
// marker already uses — a different glyph family from the half-moon set
// other agents show in their terminal titles.
func (a *terminalTitleAnimator) frameLocked(base string) string {
	glyph := spinnerGlyphs[a.phase%len(spinnerGlyphs)]
	if base == "" {
		return glyph + " forebrain"
	}
	return glyph + " " + base
}

// Settle stops the animation (if any) and writes the static idle title.
func (a *terminalTitleAnimator) Settle(base string) {
	if a == nil {
		return
	}
	a.stopLoop()
	if a.out == nil {
		return
	}
	writeTerminalTitle(a.out, staticTerminalTitle(base))
}

func staticTerminalTitle(base string) string {
	base = sanitizeTerminalTitleText(base)
	if base == "" {
		return "forebrain"
	}
	return base
}

func (a *terminalTitleAnimator) stopLoop() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if !a.running {
		a.mu.Unlock()
		return
	}
	a.running = false
	stop, done := a.stop, a.done
	a.mu.Unlock()
	close(stop)
	<-done
}

// Clear stops the animation and empties the terminal title so the emulator
// falls back to its own title once Forebrain Harness exits.
func (a *terminalTitleAnimator) Clear() {
	if a == nil {
		return
	}
	a.stopLoop()
	writeTerminalTitle(a.out, "")
}

// lookupSessionTitle returns the stored display title for sessionID, or ""
// when the store has none distinct from the raw session ID.
func lookupSessionTitle(ctx context.Context, session Session, sessionID string) string {
	if session == nil {
		return ""
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	rows, err := session.ListSessionsRecent(ctx, 100)
	if err != nil {
		return ""
	}
	for _, row := range rows {
		if strings.TrimSpace(row.ID) != sessionID {
			continue
		}
		title := strings.TrimSpace(row.Title)
		if title == "" || title == sessionID {
			return ""
		}
		return title
	}
	return ""
}

// sessionTitleForTurn resolves the body the animated terminal title should
// show for an outgoing turn: the stored session title when one exists,
// otherwise the provisional title derived from the user's first line — the
// same rule the store persists once the message lands.
func sessionTitleForTurn(ctx context.Context, session Session, sessionID, content string) string {
	if t := lookupSessionTitle(ctx, session, sessionID); t != "" {
		return t
	}
	t := state.SessionTitleFromContent(content)
	if t == "New session" {
		return ""
	}
	return t
}

// errClipboardNoImage reports that the clipboard holds something other than an
// image — text, most often. Pressing the paste-image hotkey then is an ordinary
// no-op rather than a failure, so callers ignore it instead of surfacing it.
var errClipboardNoImage = errors.New("clipboard does not contain an image")

type systemClipboardImageReader struct{}

func DefaultClipboardImageReader() ClipboardImageReader {
	return systemClipboardImageReader{}
}

func (systemClipboardImageReader) ReadClipboardImage(ctx context.Context) (InputAttachment, error) {
	tmp, err := os.CreateTemp(os.TempDir(), fmt.Sprintf("forebrain-clipboard-%d-*.png", os.Getpid()))
	if err != nil {
		return InputAttachment{}, err
	}
	path := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(path)
		return InputAttachment{}, err
	}
	if err := writeClipboardPNG(ctx, path); err != nil {
		_ = os.Remove(path)
		return InputAttachment{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		_ = os.Remove(path)
		return InputAttachment{}, err
	}
	if st.Size() == 0 {
		_ = os.Remove(path)
		return InputAttachment{}, errClipboardNoImage
	}
	return InputAttachment{Path: path, MIMEType: "image/png"}, nil
}

// ClipboardImageProbe is implemented by clipboard readers that can cheaply say
// whether the clipboard holds an image without extracting it.
type ClipboardImageProbe interface {
	ClipboardHasImage(ctx context.Context) bool
}

func (systemClipboardImageReader) ClipboardHasImage(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("osascript"); err != nil {
			return false
		}
		cmd = exec.CommandContext(ctx, "osascript", "-e", "clipboard info")
	case "linux":
		if _, err := exec.LookPath("wl-paste"); err == nil {
			cmd = exec.CommandContext(ctx, "wl-paste", "--list-types")
		} else if _, err := exec.LookPath("xclip"); err == nil {
			cmd = exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-t", "TARGETS", "-o")
		} else {
			return false
		}
	default:
		return false
	}
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	text := string(out)
	return strings.Contains(text, "image/png") || strings.Contains(text, "PNGf") || strings.Contains(text, "TIFF")
}

// watchClipboardImage polls the clipboard and keeps the renderer's paste hint
// in step with it until ctx ends.
func watchClipboardImage(ctx context.Context, probe ClipboardImageProbe, renderer *Renderer) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		renderer.SetClipboardImageAvailable(probe.ClipboardHasImage(ctx))
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func writeClipboardPNG(ctx context.Context, path string) error {
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("pngpaste"); err == nil {
			return runClipboardImageTool(exec.CommandContext(ctx, "pngpaste", path))
		}
		if _, err := exec.LookPath("osascript"); err == nil {
			script := fmt.Sprintf(`set outPath to POSIX file %q
try
  set pngData to the clipboard as «class PNGf»
  set outRef to open for access outPath with write permission
  set eof of outRef to 0
  write pngData to outRef
  close access outRef
on error errMsg
  try
    close access outPath
  end try
  error errMsg
end try`, path)
			return runClipboardImageTool(exec.CommandContext(ctx, "osascript", "-e", script))
		}
	case "linux":
		if _, err := exec.LookPath("wl-paste"); err == nil {
			return writeCommandStdoutToFile(ctx, path, "wl-paste", "--type", "image/png")
		}
		if _, err := exec.LookPath("xclip"); err == nil {
			return writeCommandStdoutToFile(ctx, path, "xclip", "-selection", "clipboard", "-t", "image/png", "-o")
		}
	}
	return fmt.Errorf("image clipboard paste is unavailable: install pngpaste, wl-paste, or xclip")
}

func writeCommandStdoutToFile(ctx context.Context, path string, name string, args ...string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.CommandContext(ctx, strings.TrimSpace(name), args...)
	cmd.Stdout = f
	return runClipboardImageTool(cmd)
}

// runClipboardImageTool runs a clipboard extraction helper. The helper exits
// non-zero when the clipboard carries no image, which is a state of the
// clipboard and not a failure of the helper, so that exit becomes
// errClipboardNoImage; everything else (the helper failing to start, for
// instance) stays a real error.
func runClipboardImageTool(cmd *exec.Cmd) error {
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return errClipboardNoImage
	}
	return err
}

func writeTextToClipboard(ctx context.Context, text string) error {
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("pbcopy"); err == nil {
			cmd := exec.CommandContext(ctx, "pbcopy")
			cmd.Stdin = strings.NewReader(text)
			return cmd.Run()
		}
	case "linux":
		if _, err := exec.LookPath("wl-copy"); err == nil {
			cmd := exec.CommandContext(ctx, "wl-copy")
			cmd.Stdin = strings.NewReader(text)
			return cmd.Run()
		}
		if _, err := exec.LookPath("xclip"); err == nil {
			cmd := exec.CommandContext(ctx, "xclip", "-selection", "clipboard")
			cmd.Stdin = strings.NewReader(text)
			return cmd.Run()
		}
		if _, err := exec.LookPath("xsel"); err == nil {
			cmd := exec.CommandContext(ctx, "xsel", "--clipboard", "--input")
			cmd.Stdin = strings.NewReader(text)
			return cmd.Run()
		}
	case "windows":
		if _, err := exec.LookPath("clip.exe"); err == nil {
			cmd := exec.CommandContext(ctx, "clip.exe")
			cmd.Stdin = strings.NewReader(text)
			return cmd.Run()
		}
	}
	return fmt.Errorf("clipboard write unavailable: no supported clipboard tool found")
}

// DiffTheme is a three-value enum for the terminal background.
type DiffTheme int

const (
	DiffThemeUnknown DiffTheme = iota
	DiffThemeDark
	DiffThemeLight
)

// ResolveDiffTheme determines the terminal background using this resolution order:
//
//  1. FOREBRAIN_THEME env ("light", "dark", or "auto")
//  2. programmatic override set by SetDiffThemeOverride
//  3. bounded OSC 11 probe (~200ms timeout)
//
// IMPORTANT: call this BEFORE entering raw mode (newRawTerminalSession in run.go)
// because the OSC 11 probe manipulates termios. Once raw mode is active, calling
// this can corrupt the terminal state.
// After calling, cache the result with lipgloss.SetHasDarkBackground(dark).
func ResolveDiffTheme() DiffTheme {
	// 1. FOREBRAIN_THEME env
	switch strings.ToLower(strings.TrimSpace(os.Getenv("FOREBRAIN_THEME"))) {
	case "light":
		return DiffThemeLight
	case "dark":
		return DiffThemeDark
	case "auto", "":
		// fall through to detection
	default:
		// unrecognised value → treat as auto
	}

	// 3. bounded probe (safe only in cooked mode)
	if noColorActive() {
		return DiffThemeUnknown
	}

	// Short-circuit for known-dumb terminals (termenv does this internally too)
	termType := strings.ToLower(os.Getenv("TERM"))
	if termType == "dumb" || strings.HasPrefix(termType, "screen") || strings.HasPrefix(termType, "tmux") {
		return DiffThemeUnknown
	}

	// Bounded OSC 11 probe with 200ms timeout to avoid 5s hangs on non-responsive terminals
	isDark, ok := probeDarkBackgroundBounded(200 * time.Millisecond)
	if !ok {
		return DiffThemeUnknown
	}
	if isDark {
		return DiffThemeDark
	}
	return DiffThemeLight
}

// ApplyDiffTheme caches the resolved theme into lipgloss so all subsequent
// lipgloss.AdaptiveColor calls use it without re-probing.
// Call exactly once, before newRawTerminalSession.
//
// Every outcome pins a value, DiffThemeUnknown included. An unpinned lipgloss
// background is not inert: the first AdaptiveColor render — the startup banner —
// makes lipgloss ask termenv, and termenv answers by writing OSC 11 and CSI 6n
// to the terminal and then reading the terminal fd itself, discarding every byte
// that is not the ESC it wants and waiting up to five seconds for each one. By
// then the TUI owns the terminal and the user is typing into the composer, so
// those keystrokes are swallowed before the input pipeline ever sees them.
// Unknown therefore pins dark, which is also where termenv's own fallback lands
// when nothing answers the query.
func ApplyDiffTheme(t DiffTheme) {
	lipgloss.SetHasDarkBackground(t != DiffThemeLight)
}

// probeDarkBackgroundBounded sends an OSC 11 query to the terminal and waits up to
// timeout for a response. Returns (isDark, true) if the terminal answered and the
// background is dark, (false, true) if light, or (false, false) if the terminal
// didn't answer within the timeout.
//
// This avoids termenv's hardcoded 5s OSCTimeout which causes startup hangs on
// terminals that don't support OSC 11 (CI environments, older terminals, some SSH).
//
// The answer is collected on this goroutine, and a read is only issued once poll
// says a byte is ready, so the probe can never leave a reader parked on the
// terminal fd. Handing the read to a goroutine and abandoning it at the timeout
// is not an option: a Read() blocked on a tty cannot be cancelled, so that
// goroutine stays live for the rest of the session and consumes whatever arrives
// next — the user's first keystrokes into the composer, silently discarded.
//
// Reading stops at the first byte that cannot belong to an OSC 11 answer, so a
// silent terminal costs at most the one byte that proved it silent, and the rest
// of a type-ahead burst stays in the tty for the input pipeline to read.
func probeDarkBackgroundBounded(timeout time.Duration) (isDark bool, ok bool) {
	if !canGateTerminalReads {
		// Without readiness gating the only way to collect an answer is a read
		// that cannot be abandoned safely, so report "unknown" and let
		// ApplyDiffTheme pin the default instead.
		return false, false
	}
	// Only probe if stdout is a terminal
	fd := int(os.Stdout.Fd())
	if !term.IsTerminal(fd) {
		return false, false
	}

	// Save original termios state
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return false, false
	}
	defer term.Restore(fd, oldState)

	// Input already waiting is the user's, not an answer to a query that has
	// not been asked yet — and once the two are in the same stream there is no
	// way to tell them apart. Skip the probe rather than read into their
	// type-ahead; ApplyDiffTheme pins the default for an unknown theme.
	//
	// This has to come after MakeRaw: a cooked terminal only reports an
	// unterminated line as readable once it sees a newline, so the check would
	// miss exactly the type-ahead it exists to protect.
	if pending, perr := pollReadableInputFD(int(os.Stdin.Fd()), 0); perr != nil || pending {
		return false, false
	}

	// Send OSC 11 query: "\x1b]11;?\x1b\\"
	_, err = os.Stdout.Write([]byte("\x1b]11;?\x1b\\"))
	if err != nil {
		return false, false
	}

	return readOSC11Response(os.Stdin, timeout)
}

// readOSC11Response collects the terminal's answer to an OSC 11 query already
// written to it. It reads only while the bytes so far still form the beginning
// of such an answer, and returns once one is terminated, the deadline passes,
// or the stream turns out to hold something else.
//
// It reads one byte at a time: a bulk read would swallow a whole type-ahead
// burst before its first byte could reveal that the terminal is not answering
// at all, and there is no way to put those bytes back into a tty.
//
// The answer is parsed only once its terminator has arrived, never on the bytes
// so far. A colour triple parses while digits are still in flight — the prefix
// "\x1b]11;rgb:1e1e/2a43/3" is already three well-formed hex components — and
// returning there ends the probe with the rest of the reply still in the tty,
// which the input pipeline then reads as the user's own keystrokes.
func readOSC11Response(in *os.File, timeout time.Duration) (isDark bool, ok bool) {
	fd := int(in.Fd())
	deadline := time.Now().Add(timeout)
	var response []byte
	var buf [1]byte
	for len(response) < osc11ResponseMaxBytes {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, false
		}
		readable, err := pollReadableInputFD(fd, int(remaining/time.Millisecond)+1)
		if err != nil || !readable {
			return false, false
		}
		n, err := in.Read(buf[:])
		if n > 0 {
			response = append(response, buf[0])
			if !isOSC11ResponsePrefix(response) {
				// Typed, not answered. It cannot go back into the tty, so hand
				// it to the input pipeline instead of dropping it.
				keepTerminalInputForInputPipeline(response)
				return false, false
			}
			if osc11ResponseTerminated(response) {
				return parseOSC11Response(string(response))
			}
		}
		if err != nil {
			return false, false
		}
	}
	return false, false
}

// osc11ResponseMaxBytes caps the answer well above the longest real one
// ("\x1b]11;rgb:RRRR/GGGG/BBBB\x1b\\" is 25 bytes) so a terminal that pads or
// uses a longer colour form still parses, while a stream that never terminates
// cannot hold the probe past its deadline.
const osc11ResponseMaxBytes = 64

// osc11ResponseTerminated reports whether b ends with an OSC string terminator,
// which is what makes the answer complete: BEL, or ST written as ESC backslash.
// The leading ESC belongs to the answer's own introducer, so an ST can only
// start at index 2.
func osc11ResponseTerminated(b []byte) bool {
	if n := len(b); n > 0 && b[n-1] == 0x07 {
		return true
	} else if n > 2 && b[n-2] == 0x1b && b[n-1] == '\\' {
		return true
	}
	return false
}

// isOSC11ResponsePrefix reports whether b can still be the beginning of an
// OSC 11 answer. Anything else is the user typing ahead, and reading on would
// eat it: there is no way to put a byte back into a tty.
func isOSC11ResponsePrefix(b []byte) bool {
	if len(b) > 0 && b[0] != 0x1b {
		return false
	}
	if len(b) > 1 && b[1] != ']' {
		return false
	}
	return true
}

// parseOSC11Response parses an OSC 11 response and returns whether the background
// is dark. Expected format: "\x1b]11;rgb:RRRR/GGGG/BBBB\x1b\\" or "\x1b]11;rgb:RR/GG/BB\x07"
func parseOSC11Response(response string) (isDark bool, ok bool) {
	// Look for "rgb:" prefix
	idx := strings.Index(response, "rgb:")
	if idx == -1 {
		return false, false
	}

	// Extract the RGB values after "rgb:"
	rgbPart := response[idx+4:]
	// Terminate at ESC or BEL
	if i := strings.IndexAny(rgbPart, "\x1b\x07"); i != -1 {
		rgbPart = rgbPart[:i]
	}

	// Parse "RRRR/GGGG/BBBB" or "RR/GG/BB"
	parts := strings.Split(rgbPart, "/")
	if len(parts) != 3 {
		return false, false
	}

	var r, g, b int
	_, err := fmt.Sscanf(parts[0], "%x", &r)
	if err != nil {
		return false, false
	}
	_, err = fmt.Sscanf(parts[1], "%x", &g)
	if err != nil {
		return false, false
	}
	_, err = fmt.Sscanf(parts[2], "%x", &b)
	if err != nil {
		return false, false
	}

	// Normalize to 0-255 range (handles both 16-bit and 8-bit formats)
	if r > 255 {
		r = r >> 8
	}
	if g > 255 {
		g = g >> 8
	}
	if b > 255 {
		b = b >> 8
	}

	// Calculate perceived luminance using standard weights
	// https://www.w3.org/TR/WCAG20/#relativeluminancedef
	luminance := 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)

	// Dark if luminance < 128 (midpoint of 0-255)
	return luminance < 128, true
}

func noColorActive() bool {
	return strings.TrimSpace(os.Getenv("NO_COLOR")) != ""
}

// viewmodel.go holds the retained message model for the virtual viewport
// renderer. Unlike the legacy append-only stream (which printed each frame and
// forgot it), the viewport keeps every block in memory so any historical block
// — not just the newest — can be re-rendered, scrolled to, and clicked to
// expand or collapse.
//
// A viewBlock wraps one Frame plus its fold state. The viewModel owns the
// ordered block list and hands out monotonically increasing ids used by the
// viewport's hit-test to map a clicked screen row back to a block.

// viewBlock is one retained, independently foldable unit of transcript.
type viewBlock struct {
	// id is monotonically increasing and stable for the life of the block.
	// Hit-testing resolves a clicked row to a block id (see viewport.go).
	id int
	// frame is the original, full-content frame. The viewport renders from
	// this verbatim; nothing is discarded on collapse, so expanding is a pure
	// re-render with no data loss.
	frame Frame
	// expanded toggles the collapsed/preview/expanded render of a collapsible block.
	// Default false for collapsible kinds: shows a preview header with up to
	// 12 lines of body and a "+N lines" hint when truncated. True shows the
	// full body.
	// A still-streaming thinking block renders full regardless (see viewport),
	// so reasoning is visible live and folds to its header once finalized.
	expanded bool
	// collapsible marks kinds eligible for click-to-expand/collapse.
	collapsible bool

	// cache holds this block's last fully-rendered lines plus the signature
	// they were produced at. renderViewport reuses them when the signature is
	// unchanged so a redraw (scroll, click on another block, new frame) does
	// not re-run markdown/diff rendering for every block — only blocks whose
	// width, fold state, or frame content changed are re-rendered.
	cache blockLineCache
	// foldCache memoizes foldBlock's output so it is not recomputed for every
	// block on every repaint (which, for thousands of blocks, re-runs a
	// per-line ANSI-strip regex and rebuilds every header each frame).
	foldCache foldLineCache
	// runStart is when the block was first drawn as an executing tool. Frames
	// are replaced wholesale on every update, so the running clock lives here.
	runStart time.Time
}

// displayFrame is the frame as it is drawn: an executing tool carries the
// time it has been running so far, which the header shows next to the command.
func (b *viewBlock) displayFrame() Frame {
	f := b.frame
	if f.Kind != FrameTool || !toolStatusRunning(f) || toolStatusAwaitingApproval(f) {
		return f
	}
	if d, ok := f.ToolMeta.RunningFor(time.Now()); ok {
		f.Duration = d
		return f
	}
	// Legacy or replayed events carry no engine start time; fall back to when
	// this viewport first drew the block.
	if b.runStart.IsZero() {
		b.runStart = time.Now()
	}
	f.Duration = time.Since(b.runStart)
	return f
}

// blockLineCache memoizes a block's full rendered body. The cache is
// invalidated (valid=false) at every site that mutates b.frame, so a content
// change is detected without re-deriving a string signature each frame. gen
// increments each time the lines are regenerated, letting the fold cache detect
// that `full` changed.
type blockLineCache struct {
	valid        bool
	width        int
	cwd          string
	spinnerPhase int
	gen          int
	lines        []string
	// lineAgents attributes each cached line to a subagent, for blocks that
	// show several at once (fanout). Empty for every other kind, whose
	// ownership is the frame's own AgentID.
	lineAgents []string
}

// foldLineCache memoizes foldBlock's output keyed on the inputs that affect it:
// the width, the expanded toggle, and the generation of the cached full lines
// (which itself captures frame-content changes and, for running tools, the
// spinner phase). theme is renderer-wide and stable, so it is not part of the
// key (matching the pre-existing renderBlockFull cache behavior).
type foldLineCache struct {
	valid       bool
	width       int
	cwd         string
	expanded    bool
	gen         int
	lines       []string
	headerLocal int
}

// viewModel is the ordered, retained list of transcript blocks.
type viewModel struct {
	blocks []*viewBlock
	nextID int
	// spinnerPhase is stamped by the viewport painter (paintViewportLocked) on
	// every repaint and read by renderViewport to animate the running-tool
	// marker. It advances ~every 200ms while a turn is active (driven by the
	// working-status repaint) so in-flight tools show a rotating Braille
	// spinner instead of a static circle.
	spinnerPhase int
}

// append adds a frame as a new block and returns it, with one exception:
// consecutive streaming thinking frames coalesce into a single live block.
//
// Reasoning streams as a run of FrameThinking frames each carrying the FULL
// accumulated text (reducer.go:578), terminated by one FrameThinking{Final}.
// Stacking each delta would duplicate the whole block on every token. Instead,
// while the trailing block is a live (non-final) thinking block, incoming
// thinking frames replace its frame in place; the terminating Final frame
// updates and seals that same block.
//
// The terminating Final frame may arrive after other frames (tool, assistant)
// have already been appended — e.g. when flushReasoningFinal returns nil
// during a tool dispatch (buffer already empty), and ReasoningDoneMsg later
// flushes the real buffer. To guarantee the Final frame always coalesces with
// its streaming predecessor, a Final thinking frame searches backwards for the
// most recent non-final thinking block (not just the last block). A thinking
// frame arriving when no live thinking block exists starts a new block.
func (m *viewModel) append(f Frame) *viewBlock {
	if streamingFrameKind(f.Kind) {
		if f.Final {
			// Search backwards for the most recent streaming block of the same
			// kind, so the sealed frame always replaces the live one it closes
			// — the "▲ Thought for Xs" header replacing the "▸" delta display,
			// or the final answer replacing the partial one — even when
			// unrelated frames were inserted in between.
			for i := len(m.blocks) - 1; i >= 0; i-- {
				if m.blocks[i].frame.Kind == f.Kind && !m.blocks[i].frame.Final {
					m.blocks[i].frame = f
					m.blocks[i].cache.valid = false
					return m.blocks[i]
				}
			}
		} else {
			// Streaming delta: coalesce with the last block if it is a live
			// block of the same kind, so each token-level delta replaces rather
			// than duplicates the full accumulated text. Only the LAST block
			// qualifies, so text resumed after an interleaved tool call starts
			// a new block instead of growing the one before the tool.
			if n := len(m.blocks); n > 0 {
				last := m.blocks[n-1]
				if last.frame.Kind == f.Kind && !last.frame.Final {
					last.frame = f
					last.cache.valid = false
					return last
				}
			}
		}
	}
	b := &viewBlock{
		id:          m.nextID,
		frame:       f,
		collapsible: collapsibleKind(f.Kind),
	}
	m.nextID++
	m.blocks = append(m.blocks, b)
	return b
}

// streamingFrameKind reports whether a frame kind arrives as a run of live
// updates carrying the full accumulated text, each replacing the last, and is
// closed by a Final frame. Both of forebrain's streamed model outputs work this
// way: the answer and the reasoning behind it.
func streamingFrameKind(k FrameKind) bool {
	return k == FrameThinking || k == FrameAssistant
}

// blockByID returns the block with the given id, or nil. Linear scan: block
// counts are small (one session's transcript) and clicks are rare, so an index
// map would cost more upkeep than it saves.
func (m *viewModel) blockByID(id int) *viewBlock {
	for _, b := range m.blocks {
		if b.id == id {
			return b
		}
	}
	return nil
}

// reset clears all blocks, e.g. on session switch. nextID keeps climbing so a
// stale click on a row from the previous session never resolves to a new block.
func (m *viewModel) reset() {
	m.blocks = m.blocks[:0]
}

// removeLast deletes the last block whose frame matches a predicate and reports
// whether it found one. nextID keeps climbing, so a click already in flight on
// the removed row never resolves to whatever block takes its place.
func (m *viewModel) removeLast(match func(Frame) bool) bool {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if !match(m.blocks[i].frame) {
			continue
		}
		copy(m.blocks[i:], m.blocks[i+1:])
		m.blocks[len(m.blocks)-1] = nil
		m.blocks = m.blocks[:len(m.blocks)-1]
		return true
	}
	return false
}

// insertBeforeLast inserts a frame as a new block immediately before the last
// block whose frame matches a predicate. When no block matches, appends to end.
func (m *viewModel) insertBeforeLast(match func(Frame) bool, f Frame) *viewBlock {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if match(m.blocks[i].frame) {
			b := &viewBlock{
				id:          m.nextID,
				frame:       f,
				collapsible: collapsibleKind(f.Kind),
			}
			m.nextID++
			m.blocks = append(m.blocks[:i], append([]*viewBlock{b}, m.blocks[i:]...)...)
			return b
		}
	}
	return m.append(f)
}
func (m *viewModel) replaceOrAppendBlock(f Frame) *viewBlock {
	if f.StepID != "" {
		switch f.Kind {
		case FrameFanout, FrameTool, FrameMemoryCompact, FrameSkillInstall:
			for _, b := range m.blocks {
				if b.frame.Kind == f.Kind && b.frame.StepID == f.StepID && !b.frame.RetainAsHistory {
					b.frame = f
					b.cache.valid = false
					return b
				}
			}
		}
	}
	return m.append(f)
}

// collapsibleKind reports whether a frame kind participates in click-to-expand.
// Thinking and the compact "card" kinds (tool/memory compact) fold; every other
// kind — conversational kinds (assistant, user), status lines, plan updates,
// error messages, and system reports — always renders in full: a report the
// user asked for is never folded away (long content scrolls instead).
func collapsibleKind(kind FrameKind) bool {
	switch kind {
	case FrameThinking,
		FrameTool,
		FrameMemoryCompact:
		return true
	default:
		return false
	}
}

func tuiReadLine(ctx context.Context, in io.Reader) func(context.Context) (string, error) {
	reader := bufio.NewReader(in)
	return func(context.Context) (string, error) {
		_ = ctx
		line, err := reader.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), err
	}
}

// ErrCancelled reports that the user chose to quit before a session started,
// out of onboarding or at the trust prompt. Quitting was their answer rather
// than a failure, so the launch ends without an error message.
var ErrCancelled = errors.New("cancelled")
