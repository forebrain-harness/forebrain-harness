// The capture tool: a picture of the running app's own surface, produced here
// and handed to the model as pixels.
//
// It lives in pkg/run rather than pkg/tool because pkg/tool is at its
// production-file cap (pkg/architecture/cache_test.go) and a tool may be
// registered from here — RegisterSkillTool below it in this package is the
// precedent, and the reason is the same: this tool needs the Runner (its
// permission snapshot, its config and its state root), which pkg/tool cannot
// import.
package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// CaptureInput is the capture tool's one input shape. One tool with three
// modes, not three tools: the tool array renders ahead of the system prompt and
// sits in the cached prefix, so every entry costs every request.
type CaptureInput struct {
	Mode   string `json:"mode" jsonschema:"description=screen captures the whole display, region a rectangle of it, browser loads a URL in a headless browser and captures the page."`
	Path   string `json:"path,omitempty" jsonschema:"description=Where to write the PNG. Default <state root>/state/capture/<id>.png."`
	Rect   string `json:"rect,omitempty" jsonschema:"description=region: x,y,w,h in pixels."`
	URL    string `json:"url,omitempty" jsonschema:"description=browser: the page to load."`
	WaitMs int    `json:"wait_ms,omitempty" jsonschema:"description=browser: how long the page may settle before the shot. Default 1500."`
	Width  int    `json:"width,omitempty" jsonschema:"description=browser: viewport width. Default 1280."`
	Height int    `json:"height,omitempty" jsonschema:"description=browser: viewport height. Default 800."`
}

const captureToolDescription = "Take a picture and see it: `screen` (the whole display), `region` (a rectangle), or `browser` (load a URL in a headless browser and capture the page). This is how you check that something you changed actually renders - read the image, do not infer it from DOM text. The PNG lands under <state root>/state/capture/ and comes back as pixels plus a note naming the path and the dimensions; read_file on that path later returns it again. Prefer `browser` and `region` over `screen`: screen captures whatever is on the display, which can include another window's private content, and the result is sent to the model provider - this is the one tool that can hand over something the user never pointed at. In `browser` mode the page (and the server behind it) must already be running; start it with the process tool. A capture macOS refuses is reported as a permission problem naming System Settings -> Privacy & Security -> Screen Recording, never as a blank success."

// captureOutcome is what one attempt produced, as one of the four outcomes the
// plan names. The distinction between denied and sandbox-denied is load
// bearing: a sandbox refusal can be escalated and asked about, an OS permission
// refusal cannot be answered by anything the product does.
type captureOutcome string

const (
	captureCaptured      captureOutcome = "captured"
	captureDenied        captureOutcome = "denied"
	captureSandboxDenied captureOutcome = "sandbox_denied"
	captureNoBinary      captureOutcome = "no-binary"
	captureEmptyFrame    captureOutcome = "empty-frame"
)

const (
	captureDefaultWidth  = 1280
	captureDefaultHeight = 800
	captureDefaultWaitMs = 1500
	captureTimeout       = 2 * time.Minute
)

// captureLookPath answers whether a capture binary exists. It is a variable so
// a test can pin the no-binary branch: this host has every macOS binary, and a
// probe that always succeeds cannot prove the refusal.
var captureLookPath = exec.LookPath

// captureShape is one platform's answer for one mode: the command to run and
// what its absence means.
type captureShape struct {
	argv   []string
	binary string
	// hint explains where the binary comes from when it is missing, so a
	// no-binary outcome names the package instead of saying "not found".
	hint string
}

// RegisterCaptureTool registers the capture tool. It is registered
// unconditionally: a tool array whose shape depends on the project type
// re-bills every cached prefix.
func RegisterCaptureTool(a tool.ToolAdder, st *tool.State, r *Runner) error {
	if a == nil || st == nil || r == nil {
		return nil
	}
	t, err := llm.NewTool(
		"capture",
		captureToolDescription,
		func(ctx context.Context, in *CaptureInput) ([]llm.ContentPart, error) {
			if err := st.GuardTool(ctx, "capture"); err != nil {
				return nil, err
			}
			if in == nil {
				return nil, fmt.Errorf("input required")
			}
			return r.capture(ctx, st, in)
		},
	)
	if err != nil {
		return err
	}
	if err := st.Register(a, t); err != nil {
		return err
	}
	meta := event.ToolMeta{
		Name:              "capture",
		Description:       captureToolDescription,
		Category:          "session",
		Destructive:       true,
		InterruptBehavior: "interrupt",
	}
	if schema, err := json.Marshal(t.InputSchema()); err == nil {
		meta.InputSchema = schema
	}
	st.RegisterToolMeta(meta)
	return nil
}

// captureDir is where a capture lands: the state root's own spool, mirroring
// state/tool-output. The sandbox denies writes under the Forebrain home, so
// this directory has to travel with the command as a writable root.
func (r *Runner) captureDir() string {
	return filepath.Join(r.workspaceRoot(), "state", "capture")
}

func (r *Runner) capture(ctx context.Context, st *tool.State, in *CaptureInput) ([]llm.ContentPart, error) {
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	switch mode {
	case "screen", "region", "browser":
	case "":
		return nil, fmt.Errorf("mode is required: screen, region or browser")
	default:
		return nil, fmt.Errorf("mode %q is not screen, region or browser", in.Mode)
	}
	if mode == "region" && strings.TrimSpace(in.Rect) == "" {
		return nil, fmt.Errorf("rect is required for mode region (x,y,w,h)")
	}
	if mode == "browser" && strings.TrimSpace(in.URL) == "" {
		return nil, fmt.Errorf("url is required for mode browser")
	}
	dir := r.captureDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path, writable, err := captureTarget(ctx, dir, in.Path)
	if err != nil {
		return nil, err
	}
	shape := captureCommandFor(mode, runtime.GOOS, path, in)
	if _, lookErr := captureLookPath(shape.binary); lookErr != nil {
		return nil, captureNoBinaryError(mode, shape, lookErr)
	}
	// A capture the model asks for must not silently return the previous file.
	_ = os.Remove(path)

	outcome, res, err := r.runCaptureCommand(ctx, st, shape, path, writable, mode)
	if err != nil {
		return nil, err
	}
	if outcome == captureSandboxDenied {
		retried, retryOutcome, retryErr := r.captureOutsideTheSandbox(ctx, st, shape, path, writable, mode, res)
		if retryErr != nil {
			return nil, retryErr
		}
		if !retried {
			return nil, nil
		}
		outcome = retryOutcome
	}
	switch outcome {
	case captureDenied:
		return nil, capturePermissionError(mode)
	case captureNoBinary:
		return nil, captureNoBinaryError(mode, shape, errors.New("not on PATH"))
	}
	if _, statErr := os.Stat(path); statErr != nil {
		return nil, fmt.Errorf("capture produced no file at %s (%s exited %d): %s", path, shape.binary, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return r.captureParts(ctx, st, path, mode, outcome)
}

// captureParts reads the bytes back, judges whether they hold a picture at all,
// and returns the note plus the pixels — the same two-part shape read_file
// produces, through the same image budget.
func (r *Runner) captureParts(ctx context.Context, st *tool.State, path, mode string, outcome captureOutcome) ([]llm.ContentPart, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	width, height := llm.DecodeImageConfig(data)
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("capture wrote %s but it is not a decodable png (%d bytes)", path, len(data))
	}
	if refusal := st.ImageInputRefusal(ctx); refusal != "" {
		return nil, fmt.Errorf("capture wrote %s (%dx%d) but this session's model cannot receive it: %s", path, width, height, refusal)
	}
	if uniformCapture(data) {
		// A single-colour frame is what a denied or minimized capture looks
		// like, and it is also a legal blank page — warn, never fail.
		outcome = captureEmptyFrame
	}
	// 0 = the product's own image budget ($FOREBRAIN_IMAGE_MAX_BYTES and the
	// downscale that follows it), the same resolution read_file uses.
	part, res, err := llm.ImagePartWithin(path, "image/png", data, 0)
	if err != nil {
		return nil, err
	}
	note := captureNote(mode, path, res, outcome, len(data))
	tool.CaptureToolOutput(ctx, tool.ImageReadOutput(path, path, res))
	return []llm.ContentPart{llm.Text(note), part}, nil
}

// captureNote is the one line the model reads beside the pixels: where the file
// is, what was captured, how big it is, and anything surprising.
func captureNote(mode, path string, res llm.ImageReadResult, outcome captureOutcome, bytes int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s capture %dx%d, %d bytes on disk", path, mode, res.Width, res.Height, bytes)
	if res.Scaled {
		fmt.Fprintf(&b, " (downscaled from %dx%d to fit the image budget)", res.SourceWidth, res.SourceHeight)
	}
	switch outcome {
	case captureEmptyFrame:
		b.WriteString("\nwarning: this frame is a single uniform colour. A genuinely blank page looks the same, but so does a capture the OS blocked or a window that was minimized — before you describe it, check that the app is actually rendering (the browser mode against a running server is the reliable way).")
	case captureCaptured:
	}
	return b.String()
}

// captureTarget resolves the output path and the writable root the command
// needs for it.
func captureTarget(ctx context.Context, dir, requested string) (path string, writable string, err error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return filepath.Join(dir, fmt.Sprintf("%d.png", time.Now().UnixNano())), dir, nil
	}
	if !filepath.IsAbs(requested) {
		wd, wdErr := tool.WorkingDir(ctx)
		if wdErr != nil {
			return "", "", wdErr
		}
		requested = filepath.Join(wd, requested)
	}
	path = filepath.Clean(requested)
	if filepath.Ext(path) == "" {
		path += ".png"
	}
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr != nil {
		return "", "", mkErr
	}
	return path, filepath.Dir(path), nil
}

// captureCommandFor picks this platform's command for one mode. Every branch is
// present on every platform: an unsupported combination names the binary it
// would need instead of panicking on a nil.
func captureCommandFor(mode, goos, path string, in *CaptureInput) captureShape {
	width, height := in.Width, in.Height
	if width <= 0 {
		width = captureDefaultWidth
	}
	if height <= 0 {
		height = captureDefaultHeight
	}
	switch goos {
	case "darwin":
		switch mode {
		case "screen":
			return captureShape{argv: []string{"/usr/sbin/screencapture", "-x", "-o", path}, binary: "/usr/sbin/screencapture",
				hint: "screencapture ships with macOS at /usr/sbin/screencapture"}
		case "region":
			return captureShape{argv: []string{"/usr/sbin/screencapture", "-x", "-o", "-R", strings.TrimSpace(in.Rect), path}, binary: "/usr/sbin/screencapture",
				hint: "screencapture ships with macOS at /usr/sbin/screencapture"}
		}
	case "linux":
		switch mode {
		case "screen":
			if shape, ok := firstInstalledCapture(
				captureShape{argv: []string{"grim", path}, binary: "grim", hint: "install grim (Wayland, wlroots-based compositors)"},
				captureShape{argv: []string{"import", "-window", "root", path}, binary: "import", hint: "install imagemagick (X11)"},
				captureShape{argv: []string{"gnome-screenshot", "-f", path}, binary: "gnome-screenshot", hint: "install gnome-screenshot (GNOME)"},
				captureShape{argv: []string{"spectacle", "-b", "-n", "-o", path}, binary: "spectacle", hint: "install spectacle (KDE)"},
			); ok {
				return shape
			}
			return captureShape{binary: "grim", hint: "install one of grim (Wayland), imagemagick's import (X11), gnome-screenshot (GNOME) or spectacle (KDE)"}
		case "region":
			if shape, ok := firstInstalledCapture(
				captureShape{argv: []string{"grim", "-g", strings.TrimSpace(in.Rect), path}, binary: "grim", hint: "install grim (Wayland, wlroots-based compositors)"},
				captureShape{argv: []string{"import", "-window", "root", "-crop", captureImportCrop(in.Rect, width, height), path}, binary: "import", hint: "install imagemagick (X11)"},
			); ok {
				return shape
			}
			return captureShape{binary: "grim", hint: "a region capture on Linux needs grim (Wayland) or imagemagick's import (X11); gnome-screenshot and spectacle take their rectangle from a GUI"}
		}
	case "windows":
		return captureShape{argv: []string{"powershell", "-NoProfile", "-NonInteractive", "-Command", capturePowerShell(mode, path, in.Rect)}, binary: "powershell",
			hint: "Windows PowerShell with System.Drawing (a standard Windows component)"}
	}
	// browser is platform independent: the same probes on every OS.
	return captureBrowserShape(path, in.URL, width, height, in.WaitMs)
}

// captureBrowserShape probes the headless browsers plans 353/354 already assume,
// in that order, and builds Chrome's screenshot invocation.
func captureBrowserShape(path, url string, width, height, waitMs int) captureShape {
	if waitMs <= 0 {
		waitMs = captureDefaultWaitMs
	}
	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--no-first-run",
		"--hide-scrollbars",
		"--user-data-dir=" + filepath.Join(filepath.Dir(path), ".headless-profile"),
		"--window-size=" + strconv.Itoa(width) + "," + strconv.Itoa(height),
		"--virtual-time-budget=" + strconv.Itoa(waitMs),
		"--screenshot=" + path,
		strings.TrimSpace(url),
	}
	candidates := []captureShape{
		{argv: append([]string{"chrome"}, args...), binary: "chrome", hint: "install Google Chrome"},
		{argv: append([]string{"chromium"}, args...), binary: "chromium", hint: "install Chromium"},
		{argv: append([]string{"chromium-browser"}, args...), binary: "chromium-browser", hint: "install Chromium"},
		{argv: append([]string{"microsoft-edge"}, args...), binary: "microsoft-edge", hint: "install Microsoft Edge"},
		{argv: append([]string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}, args...), binary: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", hint: "install Google Chrome"},
		{argv: append([]string{"/Applications/Chromium.app/Contents/MacOS/Chromium"}, args...), binary: "/Applications/Chromium.app/Contents/MacOS/Chromium", hint: "install Chromium"},
		{argv: append([]string{`C:\Program Files\Google\Chrome\Application\chrome.exe`}, args...), binary: `C:\Program Files\Google\Chrome\Application\chrome.exe`, hint: "install Google Chrome"},
	}
	for _, candidate := range candidates {
		if _, err := captureLookPath(candidate.binary); err == nil {
			return candidate
		}
	}
	return captureShape{binary: "chrome", hint: "install a Chromium-based browser (chrome, chromium or microsoft-edge); with none installed, drive the page with the process tool and your own driver instead"}
}

// firstInstalledCapture returns the first candidate whose binary exists.
func firstInstalledCapture(candidates ...captureShape) (captureShape, bool) {
	for _, candidate := range candidates {
		if _, err := captureLookPath(candidate.binary); err == nil {
			return candidate, true
		}
	}
	return captureShape{}, false
}

// captureImportCrop turns x,y,w,h into ImageMagick's WxH+X+Y.
func captureImportCrop(rect string, width, height int) string {
	x, y, w, h := parseCaptureRect(rect, width, height)
	return fmt.Sprintf("%dx%d+%d+%d", w, h, x, y)
}

// parseCaptureRect reads the tool's x,y,w,h, falling back to the viewport size
// when a field is missing or unreadable.
func parseCaptureRect(rect string, fallbackWidth, fallbackHeight int) (x, y, w, h int) {
	fields := strings.Split(strings.TrimSpace(rect), ",")
	values := make([]int, 0, len(fields))
	for _, field := range fields {
		n, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil {
			n = 0
		}
		values = append(values, n)
	}
	for len(values) < 4 {
		values = append(values, 0)
	}
	if values[2] <= 0 {
		values[2] = fallbackWidth
	}
	if values[3] <= 0 {
		values[3] = fallbackHeight
	}
	return values[0], values[1], values[2], values[3]
}

// capturePowerShell is the Windows branch: System.Drawing's CopyFromScreen. It
// is implemented behind the same probe as every other platform; this host
// cannot run it (see the completion report).
func capturePowerShell(mode, path string, rect string) string {
	source := "[System.Windows.Forms.Screen]::PrimaryScreen.Bounds"
	if mode == "region" {
		x, y, w, h := parseCaptureRect(rect, captureDefaultWidth, captureDefaultHeight)
		source = fmt.Sprintf("New-Object System.Drawing.Rectangle(%d,%d,%d,%d)", x, y, w, h)
	}
	return strings.Join([]string{
		"Add-Type -AssemblyName System.Windows.Forms; Add-Type -AssemblyName System.Drawing;",
		"$bounds = " + source + ";",
		"$bmp = New-Object System.Drawing.Bitmap $bounds.Width, $bounds.Height;",
		"$gfx = [System.Drawing.Graphics]::FromImage($bmp);",
		"$gfx.CopyFromScreen($bounds.Location, [System.Drawing.Point]::Empty, $bounds.Size);",
		"$bmp.Save('" + strings.ReplaceAll(path, "'", "''") + "', [System.Drawing.Imaging.ImageFormat]::Png);",
		"$gfx.Dispose(); $bmp.Dispose()",
	}, " ")
}

// runCaptureCommand runs one attempt through the sandbox, exactly like a shell
// command: same manager, same config, same snapshot. The capture directory
// travels as a writable root so the sandbox lets the binary write the file.
func (r *Runner) runCaptureCommand(ctx context.Context, st *tool.State, shape captureShape, path, writable, mode string) (captureOutcome, safety.CommandResult, error) {
	cwd, err := tool.WorkingDir(ctx)
	if err != nil {
		return captureCaptured, safety.CommandResult{}, err
	}
	cfg := safety.ConfigForSnapshot(r.Config(), r.PermissionSnapshotForSession(tool.ConversationSessionIDFromContext(ctx)))
	manager := safety.NewManager()
	defer manager.Close()
	request := r.captureRequest(ctx, cfg, shape, cwd, writable, mode, safety.SandboxPermissionsUseDefault)
	res, decision, runErr := manager.RunCommand(ctx, cfg, request)
	outcome := classifyCapture(res, decision, runErr, mode)
	slog.DebugContext(ctx, "capture attempt", "mode", mode, "sandboxed", decision.UseSandbox, "exit", res.ExitCode, "outcome", string(outcome))
	return outcome, res, nil
}

// captureRequest is the sandboxed request: shell-shaped, with the capture
// directory (and the file's own directory) writable.
func (r *Runner) captureRequest(ctx context.Context, cfg *appcfg.Root, shape captureShape, cwd, writable, mode string, permissions safety.SandboxPermissions) safety.CommandRequest {
	writableRoots := []string{writable}
	if mode == "browser" {
		// A headless browser keeps its profile and its crash dumps beside the
		// shot rather than in $HOME, which the sandbox denies.
		writableRoots = append(writableRoots, filepath.Join(r.captureDir(), ".headless-profile"))
	}
	return safety.CommandRequest{
		ToolKind:                safety.ToolKindShell,
		Command:                 shellJoin(shape.argv),
		WorkDir:                 cwd,
		Timeout:                 captureTimeout,
		Profile:                 safety.ProfileForConfig(cfg),
		SandboxPermissions:      permissions,
		AdditionalWritablePaths: writableRoots,
		AllowNetwork:            r.Config() != nil && r.Config().SandboxWorkspaceWrite.EffectiveNetworkAccess(),
		AllowLoopback:           r.Config() == nil || r.Config().SandboxWorkspaceWrite.EffectiveNetworkLoopback(),
	}
}

// captureOutsideTheSandbox is the owner's 2026-11-23 ruling in code: a display
// service the seatbelt profile does not grant is not a reason to widen the
// profile, it is a reason to ask. The question is the product's own escalation
// approval — never asked in a batch conversation, which has nobody to answer it
// — and an approved replay re-runs the same command on the host. TCC's Screen
// Recording permission is granted per binary anyway, so the host run is the only
// path that can ever capture on macOS.
func (r *Runner) captureOutsideTheSandbox(ctx context.Context, st *tool.State, shape captureShape, path, writable, mode string, res safety.CommandResult) (bool, captureOutcome, error) {
	if r.ProjectType() == state.ProjectTypeBatch {
		return false, captureSandboxDenied, fmt.Errorf("the sandbox refused to read the display and a batch task has nobody to ask whether it may run outside the sandbox; use mode browser against the app instead")
	}
	policy := r.PermissionSnapshotForSession(tool.ConversationSessionIDFromContext(ctx)).ApprovalPolicy
	if policy.Mode == safety.ApprovalNever && tool.ApprovedActionIDFromContext(ctx) == "" {
		return false, captureSandboxDenied, fmt.Errorf("the sandbox refused to read the display and this conversation's approval policy never asks; grant Screen Recording or use mode browser")
	}
	alreadyApproved := tool.ApprovedActionIDFromContext(ctx) != ""
	if !alreadyApproved {
		hook := st.ActionHook()
		if hook == nil {
			return false, captureSandboxDenied, captureSandboxDeniedError(mode)
		}
		payload := map[string]any{
			"mode":                  mode,
			"command":               shellJoin(shape.argv),
			"sandbox_permissions":   string(safety.SandboxPermissionsRequireEscalated),
			"justification":         "Reading the display is a service the sandbox does not grant, and macOS ties Screen Recording permission to the binary. Allow this capture to run outside the sandbox?",
			"force_tool_approval":   true,
			"approval_reason":       "sandbox_denied",
			"sandbox_denial_reason": strings.TrimSpace(res.Stderr),
		}
		id, pending, err := hook(tool.WithApprovedActionID(ctx, ""), "capture", payload)
		if err != nil {
			return false, captureSandboxDenied, err
		}
		if !pending || id == "" {
			// Policy answered without asking anyone: the escalation is
			// authorized (a remembered rule or an approvals-bypassed run).
			alreadyApproved = true
		} else {
			tool.CaptureToolRequiresAction(ctx, id, "capture", map[string]any{
				"requires_action":       true,
				"sandbox_retry":         true,
				"sandbox_permissions":   string(safety.SandboxPermissionsRequireEscalated),
				"justification":         payload["justification"],
				"approval_reason":       "sandbox_denied",
				"sandbox_denial_reason": payload["sandbox_denial_reason"],
			})
			return false, captureSandboxDenied, &tool.RequiresActionError{
				RunID:        tool.RunIDFromContext(ctx),
				AgentID:      tool.HookAgentIDFromContext(ctx),
				SubagentType: tool.SubagentTypeFromContext(ctx),
				ActionID:     id,
				ActionKind:   "capture",
				ToolName:     "capture",
				ToolInput:    payload,
			}
		}
	}
	cwd, err := tool.WorkingDir(ctx)
	if err != nil {
		return false, captureSandboxDenied, err
	}
	cfg := safety.ConfigForSnapshot(r.Config(), r.PermissionSnapshotForSession(tool.ConversationSessionIDFromContext(ctx)))
	manager := safety.NewManager()
	defer manager.Close()
	request := r.captureRequest(ctx, cfg, shape, cwd, writable, mode, safety.SandboxPermissionsRequireEscalated)
	hostRes, decision, runErr := manager.RunCommand(ctx, cfg, request)
	outcome := classifyCapture(hostRes, decision, runErr, mode)
	return true, outcome, nil
}

// classifyCapture maps one attempt to its typed outcome. Order matters: the
// sandbox refusal is checked before the permission refusal, because the sandbox
// produces the same message and the two have different answers.
func classifyCapture(res safety.CommandResult, decision safety.SandboxDecision, runErr error, mode string) captureOutcome {
	if runErr != nil && res.ExitCode == 0 {
		return captureSandboxDenied
	}
	if res.ExitCode == 0 {
		return captureCaptured
	}
	stderr := strings.ToLower(res.Stderr)
	display := captureIsDisplayRefusal(stderr)
	if display {
		if decision.UseSandbox {
			return captureSandboxDenied
		}
		return captureDenied
	}
	if strings.Contains(stderr, "operation not permitted") || strings.Contains(stderr, "permission denied") ||
		strings.Contains(stderr, "access is denied") || strings.Contains(stderr, "could not create image") ||
		strings.Contains(stderr, "unable to open display") || strings.Contains(stderr, "cannot open display") {
		return captureDenied
	}
	if safety.IsLikelySandboxDenied(decision, res) {
		return captureSandboxDenied
	}
	// A binary that answered an error we cannot classify still failed; call it
	// denied so the caller gets the permission sentence rather than a silent
	// nothing.
	return captureDenied
}

// captureIsDisplayRefusal is the message every macOS screencapture prints when
// it cannot reach the display, whether the sandbox or TCC stopped it. A
// sandboxed run cannot enumerate displays at all, so even a region capture with
// a perfectly reachable rect reports "does not intersect any displays" — the
// probe on 2026-11-23 recorded exactly that, which is why it is a display
// refusal rather than an argument error.
func captureIsDisplayRefusal(stderr string) bool {
	return strings.Contains(stderr, "could not create image from display") ||
		strings.Contains(stderr, "does not intersect any displays") ||
		strings.Contains(stderr, "screencapture: no display") ||
		strings.Contains(stderr, "display is not available")
}

func capturePermissionError(mode string) error {
	if runtime.GOOS == "darwin" {
		return fmt.Errorf("macOS refused the %s capture: this program has no Screen Recording permission. Grant it in System Settings -> Privacy & Security -> Screen Recording, then restart this program (the permission is per binary, so a terminal-hosted run needs the terminal to have it). Nothing was captured - do not describe the screen from memory", mode)
	}
	return fmt.Errorf("the %s capture was refused by this machine's display server or capture permission; nothing was captured - use mode browser against the running app instead of describing the screen from memory", mode)
}

func captureSandboxDeniedError(mode string) error {
	return fmt.Errorf("the sandbox refused to read the display for the %s capture and this session cannot ask for an exception", mode)
}

func captureNoBinaryError(mode string, shape captureShape, lookErr error) error {
	if mode == "browser" {
		return fmt.Errorf("no headless browser found (%v): %s", lookErr, shape.hint)
	}
	return fmt.Errorf("no capture binary found for mode %s (%v): %s", mode, lookErr, shape.hint)
}

// uniformCapture reports whether every sampled pixel of the image is the same
// colour — the shape a denied or minimized capture takes.
func uniformCapture(data []byte) bool {
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return false
	}
	bounds := decoded.Bounds()
	if bounds.Empty() {
		return true
	}
	stepX := max(1, bounds.Dx()/16)
	stepY := max(1, bounds.Dy()/16)
	firstR, firstG, firstB, _ := decoded.At(bounds.Min.X, bounds.Min.Y).RGBA()
	for y := bounds.Min.Y; y < bounds.Max.Y; y += stepY {
		for x := bounds.Min.X; x < bounds.Max.X; x += stepX {
			pr, pg, pb, _ := decoded.At(x, y).RGBA()
			if pr != firstR || pg != firstG || pb != firstB {
				return false
			}
		}
	}
	return true
}

// shellJoin quotes argv so one command string runs it under a login shell the
// way the sandbox expects.
func shellJoin(argv []string) string {
	quoted := make([]string, 0, len(argv))
	for _, arg := range argv {
		if arg == "" {
			quoted = append(quoted, "''")
			continue
		}
		if !strings.ContainsAny(arg, " \t'\"$`\\&|;<>()*?[]#~=%") {
			quoted = append(quoted, arg)
			continue
		}
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", `'\''`)+"'")
	}
	return strings.Join(quoted, " ")
}
