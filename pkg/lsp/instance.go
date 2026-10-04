package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/joho/godotenv"
)

// quietPeriod is how long progress readiness waits for a silent work-done
// table before declaring the server ready (spec §7.7). A package-level
// variable only so tests can shorten it; production code must not write it.
var quietPeriod = 500 * time.Millisecond

// restartBackoff spaces crash restarts out (spec §7.8); past the last entry
// the final value repeats. A package-level variable only so tests can
// shorten it; production code must not write it.
var restartBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// crashWindow is the sliding window MaxRestarts counts crashes in.
const crashWindow = 10 * time.Minute

// serverLogRotateBytes is the size a log is rotated at (spec §5.4: 5 MiB,
// one previous copy kept as <path>.1).
const serverLogRotateBytes = 5 << 20

// EnvSpec describes a server process environment (spec §9.3).
type EnvSpec struct {
	Passthrough []string          // parent variables allowed through
	Env         map[string]string // explicit variables; values may reference ${VAR}
	FromProject bool              // ${VAR} resolves only from ~/.forebrain/.env
	Home        string            // FOREBRAIN_HOME
}

// BuildEnv returns the environment a server process runs with, and the
// ${VAR} references that did not resolve. Passthrough names the parent
// variables the server additionally needs (GOPATH, JAVA_HOME, ...); values
// of Env may reference ${VAR}, resolved from the process environment or —
// for project-scoped entries — only from ~/.forebrain/.env, exactly like
// pkg/mcp's stdio servers. Secret-looking names are filtered by
// home.SafeSubprocessEnv either way.
func BuildEnv(spec EnvSpec) (env []string, missing []string) {
	explicit := make(map[string]string)
	for _, name := range spec.Passthrough {
		if v, ok := os.LookupEnv(name); ok {
			explicit[name] = v
		}
	}
	lookup := os.LookupEnv
	if spec.FromProject {
		lookup = projectEnvLookup(spec.Home)
	}
	for name, value := range spec.Env {
		expanded, miss := expandEnvRefs(value, lookup)
		explicit[name] = expanded
		missing = append(missing, miss...)
	}
	sort.Strings(missing)
	return home.SafeSubprocessEnv(nil, home.Options{ExplicitEnv: explicit}), missing
}

// envRefPattern matches one ${NAME} reference inside an explicit value.
var envRefPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnvRefs expands ${NAME} references in value; unresolved names are
// reported and become empty strings. CacheDirPlaceholder is left alone: the
// launch step expands it, not the environment builder (spec §6.2).
func expandEnvRefs(value string, lookup func(string) (string, bool)) (string, []string) {
	var missing []string
	expanded := envRefPattern.ReplaceAllStringFunc(value, func(ref string) string {
		if ref == CacheDirPlaceholder {
			return ref
		}
		name := ref[2 : len(ref)-1]
		v, ok := lookup(name)
		if !ok {
			missing = append(missing, name)
			return ""
		}
		return v
	})
	return expanded, missing
}

// projectEnvLookup resolves ${VAR} only from ~/.forebrain/.env (spec §9.3):
// a project checkout must not probe the host process environment. A missing
// or unreadable file resolves nothing.
func projectEnvLookup(forebrainHome string) func(string) (string, bool) {
	m := map[string]string{}
	if forebrainHome != "" {
		if parsed, err := godotenv.Read(home.DefaultEnvPath(forebrainHome)); err == nil {
			m = parsed
		}
	}
	return func(name string) (string, bool) {
		v, ok := m[name]
		return v, ok
	}
}

// CacheDirPlaceholder is the only placeholder the lsp layer expands (spec
// §6.2); it stands for the per-server cache directory.
const CacheDirPlaceholder = "${LSP_CACHE_DIR}"

// ExpandCacheDir replaces CacheDirPlaceholder in s.
func ExpandCacheDir(s, cacheDir string) string {
	return strings.ReplaceAll(s, CacheDirPlaceholder, cacheDir)
}

// ExpandCacheDirJSON replaces CacheDirPlaceholder in every string value of a
// JSON document. Input that is empty or not valid JSON comes back unchanged.
func ExpandCacheDirJSON(raw json.RawMessage, cacheDir string) json.RawMessage {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw
	}
	out, err := json.Marshal(expandCacheDirValue(doc, cacheDir))
	if err != nil {
		return raw
	}
	return out
}

func expandCacheDirValue(v any, cacheDir string) any {
	switch t := v.(type) {
	case string:
		return ExpandCacheDir(t, cacheDir)
	case map[string]any:
		for k, e := range t {
			t[k] = expandCacheDirValue(e, cacheDir)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = expandCacheDirValue(e, cacheDir)
		}
		return t
	default:
		return v
	}
}

// InstanceState is one server process's lifecycle state (spec §7.7).
type InstanceState string

const (
	StateStarting     InstanceState = "starting"
	StateInitializing InstanceState = "initializing"
	StateIndexing     InstanceState = "indexing"
	StateReady        InstanceState = "ready"
	StateFailed       InstanceState = "failed"
	StateStopped      InstanceState = "stopped"
)

// Readiness strategies (spec §7.7).
const (
	ReadinessProgress           = "progress"
	ReadinessRustAnalyzerStatus = "rust-analyzer-status"
	ReadinessJDTLSStatus        = "jdtls-status"
	ReadinessNone               = "none"
)

// effectiveReadiness maps a spec value to a known strategy; everything
// unknown means ready as soon as initialize finished.
func effectiveReadiness(s string) string {
	switch s {
	case ReadinessProgress, ReadinessRustAnalyzerStatus, ReadinessJDTLSStatus:
		return s
	default:
		return ReadinessNone
	}
}

// InstanceSpec is everything needed to start one server process. It is
// frozen when StartInstance is called.
type InstanceSpec struct {
	ServerID              string
	Command               string // absolute path or a name looked up on the PATH in Env
	Args                  []string
	Env                   []string
	Root                  string // absolute: the process directory, rootUri and first workspace folder
	InitializationOptions json.RawMessage
	Settings              json.RawMessage
	AutoAnswers           map[string]string
	Readiness             string
	StartupTimeout        time.Duration // 0 means 60s
	ShutdownTimeout       time.Duration // 0 means 5s
	RestartOnCrash        bool
	MaxRestarts           int    // restarts allowed in a sliding 10-minute window
	LogPath               string // "" discards logs
	PIDFile               string // "" skips orphan bookkeeping
	// OnNotification receives every server notification after the instance
	// handled its own ones ($/progress, status). Must not block.
	OnNotification func(method string, params json.RawMessage)
	// OnStateChange is called after every state or progress change. Must not block.
	OnStateChange func()
	// OnRestart is called after a crash restart finished initializing, before
	// new calls are let through (the document layer re-opens its documents).
	OnRestart func(ctx context.Context)
}

// generation is one server process together with its connection. A restart
// replaces the whole generation.
type generation struct {
	cmd      *exec.Cmd
	conn     *Conn
	tree     *processTree
	waitDone chan struct{} // closed once cmd.Wait returned
	mu       sync.Mutex
	waitErr  error
}

func (g *generation) exitError() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.waitErr
}

// Instance is one running language server process plus its JSON-RPC
// connection. Later layers only interact with a server through its methods.
type Instance struct {
	spec    InstanceSpec
	log     *serverLog
	stderrW *stderrLogWriter

	mu           sync.Mutex
	state        InstanceState
	stateCh      chan struct{} // closed and replaced on every state or progress change
	lastError    string
	progress     map[string]*int // active work-done tokens → percentage (nil = none reported)
	caps         ServerCapabilities
	encoding     Encoding
	folders      []WorkspaceFolder
	regs         map[string]Registration
	pullStale    bool // a workspace/diagnostic/refresh arrived; task 07 re-pulls
	pid          int
	crashes      []time.Time // crash times inside the sliding window
	restartSeq   int         // index into restartBackoff
	readinessSeq int         // bumped per launch; stale readiness watchers exit
	stopping     bool        // a deliberate stop is in progress: exits are not crashes
	open         bool        // calls are let through
	initialized  bool        // the initialize handshake completed for gen
	gen          *generation
	done         chan struct{}
	doneClosed   bool

	kick chan struct{} // wakes the progress readiness watcher
}

// StartInstance starts the process and completes initialize within
// StartupTimeout. It returns an error (and leaves nothing running) when the
// command is missing, the process exits, or initialize does not complete.
func StartInstance(ctx context.Context, spec InstanceSpec) (*Instance, error) {
	i := &Instance{
		spec:     spec,
		state:    StateStarting,
		stateCh:  make(chan struct{}),
		progress: map[string]*int{},
		regs:     map[string]Registration{},
		done:     make(chan struct{}),
		kick:     make(chan struct{}, 1),
	}
	i.log = openServerLog(spec.LogPath)
	if i.log != nil {
		i.stderrW = &stderrLogWriter{log: i.log}
	}
	if err := i.launchAndInitialize(ctx); err != nil {
		i.setState(StateStopped)
		i.finishTerminal()
		return nil, err
	}
	i.mu.Lock()
	i.open = true
	i.signalLocked()
	i.mu.Unlock()
	return i, nil
}

// launchAndInitialize runs steps 1–3 of a launch: process, connection,
// initialize handshake, then the readiness hand-off. It is the single path
// behind StartInstance, crash restarts, and Restart.
func (i *Instance) launchAndInitialize(ctx context.Context) error {
	i.setState(StateStarting)
	gen, err := i.launch()
	if err != nil {
		return err
	}
	i.mu.Lock()
	if i.stopping || i.doneClosed {
		// A Shutdown won while this process was starting: it saw gen == nil
		// and cleaned nothing, so this launch stops its own generation. The
		// instance is already terminal.
		err := i.stoppedErrorLocked()
		i.mu.Unlock()
		i.stopGeneration(gen)
		return err
	}
	i.gen = gen
	i.pid = gen.cmd.Process.Pid
	i.mu.Unlock()
	if err := i.initialize(ctx, gen); err != nil {
		i.stopGeneration(gen)
		i.mu.Lock()
		if i.gen == gen {
			i.gen = nil
			i.pid = 0
		}
		i.mu.Unlock()
		return err
	}
	i.mu.Lock()
	i.initialized = true
	i.mu.Unlock()
	i.setState(StateIndexing)
	i.beginReadiness()
	return nil
}

// launch starts the server process and its connection (spec §7.8).
func (i *Instance) launch() (*generation, error) {
	resolved, err := resolveCommand(i.spec.Command, i.spec.Env)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(resolved, i.spec.Args...)
	cmd.Dir = i.spec.Root
	cmd.Env = i.spec.Env
	prepareCommand(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if i.stderrW != nil {
		cmd.Stderr = i.stderrW // never the terminal (spec §7.1)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", resolved, err)
	}
	tree, err := attachTree(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, err
	}
	gen := &generation{cmd: cmd, tree: tree, waitDone: make(chan struct{})}
	gen.conn = NewConn(stdout, stdin, ConnOptions{
		OnRequest: i.handleServerRequest,
		OnNotify:  i.handleNotify,
	})
	go i.supervise(gen)
	if i.spec.PIDFile != "" {
		recordPID(i.spec.PIDFile, pidRecord{
			Server: i.spec.ServerID,
			PID:    cmd.Process.Pid,
			PGID:   tree.pgidOf(),
		})
	}
	return gen, nil
}

// initialize completes the LSP initialize handshake (spec §7.2).
func (i *Instance) initialize(ctx context.Context, gen *generation) error {
	timeout := i.spec.StartupTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	initCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	i.setState(StateInitializing)

	rootURI := PathToURI(i.spec.Root)
	params := initializeParams{
		ProcessID:             os.Getpid(),
		ClientInfo:            map[string]string{"name": "forebrain", "version": "dev"},
		Locale:                "en",
		RootPath:              i.spec.Root,
		RootURI:               rootURI,
		WorkspaceFolders:      []WorkspaceFolder{{URI: rootURI, Name: filepath.Base(i.spec.Root)}},
		InitializationOptions: i.spec.InitializationOptions,
		Capabilities:          json.RawMessage(clientCapabilitiesJSON),
		Trace:                 "off",
	}
	var result struct {
		Capabilities json.RawMessage `json:"capabilities"`
	}
	if err := gen.conn.Call(initCtx, "initialize", params, &result); err != nil {
		if errors.Is(err, context.DeadlineExceeded) && initCtx.Err() != nil {
			return fmt.Errorf("initialize did not complete within %s", timeout)
		}
		if errors.Is(err, ErrConnClosed) {
			<-gen.waitDone
			return fmt.Errorf("language server %s exited before initializing: %v", i.spec.ServerID, gen.exitError())
		}
		return err
	}
	if err := gen.conn.Notify("initialized", struct{}{}); err != nil {
		return err
	}
	if settings := bytes.TrimSpace(i.spec.Settings); len(settings) > 0 {
		changed := map[string]any{"settings": json.RawMessage(settings)}
		if err := gen.conn.Notify("workspace/didChangeConfiguration", changed); err != nil {
			return err
		}
	}
	caps := ServerCapabilities{}
	if len(bytes.TrimSpace(result.Capabilities)) > 0 {
		if err := json.Unmarshal(result.Capabilities, &caps); err != nil {
			return fmt.Errorf("decoding server capabilities: %w", err)
		}
	}
	i.mu.Lock()
	i.caps = caps
	i.encoding = ParseEncoding(caps.PositionEncoding())
	i.folders = []WorkspaceFolder{{URI: rootURI, Name: filepath.Base(i.spec.Root)}}
	i.progress = map[string]*int{}
	i.regs = map[string]Registration{}
	i.mu.Unlock()
	return nil
}

type initializeParams struct {
	ProcessID             int               `json:"processId"`
	ClientInfo            map[string]string `json:"clientInfo"`
	Locale                string            `json:"locale"`
	RootPath              string            `json:"rootPath"`
	RootURI               string            `json:"rootUri"`
	WorkspaceFolders      []WorkspaceFolder `json:"workspaceFolders"`
	InitializationOptions json.RawMessage   `json:"initializationOptions,omitempty"`
	Capabilities          json.RawMessage   `json:"capabilities"`
	Trace                 string            `json:"trace"`
}

// clientCapabilitiesJSON is the ClientCapabilities forebrain sends with
// initialize, verbatim from spec appendix A.
const clientCapabilitiesJSON = `{
  "general": { "positionEncodings": ["utf-8", "utf-16"] },
  "workspace": {
    "applyEdit": false,
    "workspaceFolders": true,
    "configuration": true,
    "didChangeConfiguration": { "dynamicRegistration": true },
    "didChangeWatchedFiles": { "dynamicRegistration": true, "relativePatternSupport": true },
    "symbol": { "dynamicRegistration": false, "resolveSupport": { "properties": ["location.range"] } },
    "diagnostics": { "refreshSupport": true },
    "workspaceEdit": { "documentChanges": true }
  },
  "textDocument": {
    "synchronization": { "didSave": true, "willSave": false, "dynamicRegistration": false },
    "publishDiagnostics": { "relatedInformation": true, "versionSupport": true, "codeDescriptionSupport": true, "dataSupport": false, "tagSupport": { "valueSet": [1, 2] } },
    "diagnostic": { "dynamicRegistration": true, "relatedDocumentSupport": true },
    "hover": { "contentFormat": ["markdown", "plaintext"] },
    "definition": { "linkSupport": true },
    "declaration": { "linkSupport": true },
    "typeDefinition": { "linkSupport": true },
    "implementation": { "linkSupport": true },
    "references": {},
    "documentSymbol": { "hierarchicalDocumentSymbolSupport": true },
    "callHierarchy": {},
    "typeHierarchy": {}
  },
  "window": { "workDoneProgress": true, "showMessage": { "messageActionItem": { "additionalPropertiesSupport": false } }, "showDocument": { "support": false } }
}`

// Call sends a request through the live connection. A call that was in
// flight when the server crashed fails with "restarted"; calls that arrive
// during a restart wait for the new connection (spec §7.8).
func (i *Instance) Call(ctx context.Context, method string, params, result any) error {
	gen, err := i.awaitConn(ctx)
	if err != nil {
		return err
	}
	if err := gen.conn.Call(ctx, method, params, result); err != nil {
		if errors.Is(err, ErrConnClosed) {
			return i.awaitCrashOutcome(ctx, gen)
		}
		return err
	}
	return nil
}

// Notify sends a notification through the live connection. Unlike Call it
// never waits for a restart: with no live connection it fails at once.
func (i *Instance) Notify(method string, params any) error {
	i.mu.Lock()
	var conn *Conn
	var err error
	switch {
	case i.state == StateStopped || i.state == StateFailed:
		err = i.stoppedErrorLocked()
	case !i.open || i.gen == nil:
		err = fmt.Errorf("language server %s is not running", i.spec.ServerID)
	default:
		conn = i.gen.conn
	}
	i.mu.Unlock()
	if err != nil {
		return err
	}
	return conn.Notify(method, params)
}

// awaitConn waits for a generation new calls may use: not during a restart,
// and only once the server is indexing or ready.
func (i *Instance) awaitConn(ctx context.Context) (*generation, error) {
	for {
		i.mu.Lock()
		if i.state == StateStopped || i.state == StateFailed {
			err := i.stoppedErrorLocked()
			i.mu.Unlock()
			return nil, err
		}
		if i.open && i.gen != nil && (i.state == StateIndexing || i.state == StateReady) {
			gen := i.gen
			i.mu.Unlock()
			return gen, nil
		}
		ch := i.stateCh
		i.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ch:
		case <-i.done:
			// Done closed is the terminal authority: resolve at once
			// instead of re-entering the loop.
			return nil, i.stoppedError()
		}
	}
}

// awaitCrashOutcome resolves a call that died with its connection: it waits
// until the instance has dealt with that generation's death, then reports a
// restart or the terminal reason.
func (i *Instance) awaitCrashOutcome(ctx context.Context, dead *generation) error {
	for {
		i.mu.Lock()
		if i.gen != dead {
			if i.state == StateFailed || i.state == StateStopped {
				err := i.stoppedErrorLocked()
				i.mu.Unlock()
				return err
			}
			if i.open && i.gen != nil {
				i.mu.Unlock()
				return fmt.Errorf("language server %s restarted", i.spec.ServerID)
			}
		}
		ch := i.stateCh
		i.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		case <-i.done:
			// Done closed is the terminal authority: resolve at once
			// instead of re-entering the loop.
			return i.stoppedError()
		}
	}
}

func (i *Instance) stoppedErrorLocked() error {
	if i.lastError != "" {
		return fmt.Errorf("language server %s stopped: %s", i.spec.ServerID, i.lastError)
	}
	return fmt.Errorf("language server %s stopped", i.spec.ServerID)
}

func (i *Instance) stoppedError() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.stoppedErrorLocked()
}

// handleServerRequest answers one server-to-client request (spec §7.3). It
// runs on its own goroutine.
func (i *Instance) handleServerRequest(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "workspace/configuration":
		return i.answerConfiguration(params)
	case "client/registerCapability":
		var p RegistrationParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		i.mu.Lock()
		for _, r := range p.Registrations {
			i.regs[r.ID] = r
		}
		i.mu.Unlock()
		return nil, nil
	case "client/unregisterCapability":
		var p unregistrationParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		i.mu.Lock()
		for _, u := range p.Unregistrations {
			delete(i.regs, u.ID)
		}
		i.mu.Unlock()
		return nil, nil
	case "window/workDoneProgress/create":
		return nil, nil
	case "window/showMessageRequest":
		return i.answerShowMessageRequest(params)
	case "window/showDocument":
		return map[string]any{"success": false}, nil
	case "workspace/applyEdit":
		i.log.write("workspace/applyEdit", "rejected: read-only client")
		return map[string]any{"applied": false, "failureReason": "read-only client"}, nil
	case "workspace/workspaceFolders":
		i.mu.Lock()
		folders := append([]WorkspaceFolder(nil), i.folders...)
		i.mu.Unlock()
		return folders, nil
	case "workspace/diagnostic/refresh":
		i.mu.Lock()
		i.pullStale = true
		i.mu.Unlock()
		return nil, nil
	}
	if strings.HasSuffix(method, "/refresh") {
		return nil, nil
	}
	return nil, ErrMethodNotFound
}

type unregistrationParams struct {
	Unregistrations []struct {
		ID     string `json:"id"`
		Method string `json:"method"`
	} `json:"unregistrations"`
}

// answerConfiguration serves workspace/configuration: one value per item,
// looked up by dotted section in the merged settings (spec §7.3).
func (i *Instance) answerConfiguration(params json.RawMessage) (any, error) {
	var p ConfigurationParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	settings := map[string]any{}
	if s := bytes.TrimSpace(i.spec.Settings); len(s) > 0 {
		if err := json.Unmarshal(s, &settings); err != nil {
			return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
		}
	}
	out := make([]any, 0, len(p.Items))
	for _, item := range p.Items {
		value := lookupSection(settings, item.Section)
		if item.Section == "python" {
			value = i.fillPythonPath(value)
		}
		out = append(out, value)
	}
	return out, nil
}

// lookupSection walks a dotted path through the settings map; anything
// missing along the way is nil, and an empty section is the whole settings.
func lookupSection(settings map[string]any, section string) any {
	if section == "" {
		return settings
	}
	var cur any = settings
	for _, part := range strings.Split(section, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		if cur, ok = m[part]; !ok {
			return nil
		}
	}
	return cur
}

// fillPythonPath backfills the interpreter for pyls-style servers: a python
// section (object or nil) without pythonPath gets the first interpreter
// found in spec order (spec §7.3).
func (i *Instance) fillPythonPath(value any) any {
	obj, _ := value.(map[string]any)
	if obj != nil {
		if _, ok := obj["pythonPath"]; ok {
			return value
		}
	}
	path := i.probePython()
	if path == "" {
		return value
	}
	if obj == nil {
		obj = map[string]any{}
	}
	obj["pythonPath"] = path
	return obj
}

// probePython looks for an interpreter: $VIRTUAL_ENV, the root's .venv and
// venv, then python3 on the PATH in Env (Windows uses Scripts\python.exe).
func (i *Instance) probePython() string {
	if runtime.GOOS == "windows" {
		if venv := envValue(i.spec.Env, "VIRTUAL_ENV"); venv != "" {
			if p := executableFile(filepath.Join(venv, "Scripts", "python.exe")); p != "" {
				return p
			}
		}
		for _, rel := range []string{`.venv\Scripts\python.exe`, `venv\Scripts\python.exe`} {
			if p := executableFile(filepath.Join(i.spec.Root, rel)); p != "" {
				return p
			}
		}
		if p, err := resolveCommand("python.exe", i.spec.Env); err == nil {
			return p
		}
		return ""
	}
	if venv := envValue(i.spec.Env, "VIRTUAL_ENV"); venv != "" {
		if p := executableFile(filepath.Join(venv, "bin", "python")); p != "" {
			return p
		}
	}
	for _, rel := range []string{".venv/bin/python", "venv/bin/python"} {
		if p := executableFile(filepath.Join(i.spec.Root, rel)); p != "" {
			return p
		}
	}
	if p, err := resolveCommand("python3", i.spec.Env); err == nil {
		return p
	}
	return ""
}

// answerShowMessageRequest auto-answers dialogs whose message contains a
// known key; everything else gets nil and the user sees nothing (spec §7.3).
func (i *Instance) answerShowMessageRequest(params json.RawMessage) (any, error) {
	var p ShowMessageRequestParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	action := ""
	message := strings.ToLower(p.Message)
	for key, title := range i.spec.AutoAnswers {
		if strings.Contains(message, strings.ToLower(key)) {
			action = title
			break
		}
	}
	if action == "" {
		i.log.write("window/showMessageRequest", p.Message+" (no auto-answer, dismissed)")
		return nil, nil
	}
	for _, a := range p.Actions {
		if a.Title == action {
			i.log.write("window/showMessageRequest", fmt.Sprintf("%s (answered %q)", p.Message, action))
			return MessageActionItem{Title: action}, nil
		}
	}
	i.log.write("window/showMessageRequest", fmt.Sprintf("%s (auto-answer %q not offered, dismissed)", p.Message, action))
	return nil, nil
}

// handleNotify processes one server notification: the instance's own state
// bookkeeping first, then the caller's callback. It runs on the read loop
// and must not block.
func (i *Instance) handleNotify(method string, params json.RawMessage) {
	switch method {
	case "$/progress":
		i.applyProgress(params)
	case "window/logMessage", "window/showMessage":
		var p struct {
			Type    int    `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(params, &p) == nil {
			i.log.write(method, p.Message)
			if p.Type == 1 {
				i.setLastError(p.Message)
			}
		}
	case "experimental/serverStatus":
		var p struct {
			Health    string `json:"health"`
			Quiescent bool   `json:"quiescent"`
			Message   string `json:"message"`
		}
		if json.Unmarshal(params, &p) == nil {
			if p.Health == "error" {
				i.setLastError(p.Message)
			}
			if p.Quiescent {
				i.markReadyFromSignal(ReadinessRustAnalyzerStatus)
			}
		}
	case "language/status":
		var p struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(params, &p) == nil {
			switch p.Type {
			case "ServiceReady":
				i.markReadyFromSignal(ReadinessJDTLSStatus)
			case "Error":
				i.setLastError(p.Message)
			}
		}
	}
	if cb := i.spec.OnNotification; cb != nil {
		cb(method, params)
	}
}

// applyProgress maintains the active work-done token table.
func (i *Instance) applyProgress(params json.RawMessage) {
	var p ProgressParams
	if json.Unmarshal(params, &p) != nil {
		return
	}
	var v WorkDoneProgressValue
	if json.Unmarshal(p.Value, &v) != nil {
		return
	}
	key := string(p.Token)
	i.mu.Lock()
	changed := false
	switch v.Kind {
	case "begin", "report":
		i.progress[key] = v.Percentage
		changed = true
	case "end":
		if _, ok := i.progress[key]; ok {
			delete(i.progress, key)
			changed = true
		}
	}
	i.mu.Unlock()
	if changed {
		i.kickWatcher()
		i.signal()
		i.fireOnStateChange()
	}
}

// beginReadiness moves an initialized server toward ready according to its
// strategy (spec §7.7).
func (i *Instance) beginReadiness() {
	i.mu.Lock()
	i.readinessSeq++
	seq := i.readinessSeq
	strategy := effectiveReadiness(i.spec.Readiness)
	indexing := i.state == StateIndexing
	i.mu.Unlock()
	switch strategy {
	case ReadinessNone:
		if indexing {
			i.setState(StateReady)
		}
	case ReadinessProgress:
		go i.progressWatcher(seq)
		i.kickWatcher()
	}
}

// progressWatcher turns a silent work-done table into readiness after
// quietPeriod. seq retires the watcher when a restart starts a newer one.
func (i *Instance) progressWatcher(seq int) {
	for {
		i.mu.Lock()
		stale := i.readinessSeq != seq
		idle := !stale && i.state == StateIndexing && len(i.progress) == 0
		i.mu.Unlock()
		if stale {
			return
		}
		if !idle {
			select {
			case <-i.kick:
			case <-i.done:
				return
			}
			continue
		}
		timer := time.NewTimer(quietPeriod)
		fired := false
		select {
		case <-timer.C:
			fired = true
		case <-i.kick:
		case <-i.done:
			timer.Stop()
			return
		}
		if !fired {
			timer.Stop()
			continue
		}
		i.mu.Lock()
		ready := i.readinessSeq == seq && i.state == StateIndexing && len(i.progress) == 0
		i.mu.Unlock()
		if ready {
			i.setState(StateReady)
			return
		}
	}
}

// markReadyFromSignal promotes an indexing server to ready when its status
// strategy's signal arrived.
func (i *Instance) markReadyFromSignal(strategy string) {
	i.mu.Lock()
	ok := i.state == StateIndexing && effectiveReadiness(i.spec.Readiness) == strategy
	i.mu.Unlock()
	if ok {
		i.setState(StateReady)
	}
}

// WaitReady returns when the server is ready, an error when it failed or
// stopped for good, and ctx.Err() when ctx ends first.
func (i *Instance) WaitReady(ctx context.Context) error {
	for {
		i.mu.Lock()
		state := i.state
		var err error
		switch state {
		case StateReady:
		case StateFailed, StateStopped:
			err = i.stoppedErrorLocked()
		}
		ch := i.stateCh
		i.mu.Unlock()
		switch state {
		case StateReady:
			return nil
		case StateFailed, StateStopped:
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		case <-i.done:
			// Done closed is the terminal authority: resolve at once
			// instead of re-entering the loop.
			return i.stoppedError()
		}
	}
}

// supervise reaps the process and turns an unexpected exit into a crash.
func (i *Instance) supervise(gen *generation) {
	waitErr := gen.cmd.Wait()
	gen.mu.Lock()
	gen.waitErr = waitErr
	close(gen.waitDone)
	gen.mu.Unlock()

	i.mu.Lock()
	crashed := !i.stopping && i.initialized && i.gen == gen
	if crashed {
		reason := fmt.Sprintf("exited: %v", waitErr)
		if line := i.log.lastLogLine(); line != "" {
			reason += "; last log: " + line
		}
		i.lastError = reason
	}
	i.mu.Unlock()
	if crashed {
		go i.handleCrash()
	}
}

// handleCrash runs the restart decision: kill what the crash left, count
// the crash, and either restart with backoff or fail for good (spec §7.8).
func (i *Instance) handleCrash() {
	i.mu.Lock()
	i.initialized = false
	i.open = false
	gen := i.gen
	i.gen = nil
	i.pid = 0
	now := time.Now()
	i.pruneCrashesLocked(now)
	i.crashes = append(i.crashes, now)
	n := len(i.crashes)
	terminal := i.stopping || i.doneClosed
	restartable := i.spec.RestartOnCrash && !terminal && n <= i.spec.MaxRestarts
	i.mu.Unlock()

	if terminal {
		// A deliberate stop owns the instance from here; this goroutine only
		// sweeps whatever the crash left in the old process group.
		i.stopGeneration(gen)
		return
	}

	i.setState(StateStarting)
	i.stopGeneration(gen)

	if !restartable {
		i.setLastError(fmt.Sprintf("crashed %d times in 10 minutes", n))
		i.setState(StateFailed)
		i.finishTerminal()
		return
	}

	i.mu.Lock()
	backoff := restartBackoff[i.restartSeq]
	if i.restartSeq < len(restartBackoff)-1 {
		i.restartSeq++
	}
	i.mu.Unlock()
	time.Sleep(backoff)

	i.mu.Lock()
	aborted := i.stopping || i.doneClosed
	i.mu.Unlock()
	if aborted {
		return
	}

	if err := i.launchAndInitialize(context.Background()); err != nil {
		i.setLastError(err.Error())
		i.setState(StateFailed)
		i.finishTerminal()
		return
	}
	if cb := i.spec.OnRestart; cb != nil {
		cb(context.Background())
	}
	i.mu.Lock()
	i.open = true
	i.signalLocked()
	i.mu.Unlock()
}

// pruneCrashesLocked drops crashes older than the sliding window.
func (i *Instance) pruneCrashesLocked(now time.Time) {
	kept := i.crashes[:0]
	for _, t := range i.crashes {
		if now.Sub(t) < crashWindow {
			kept = append(kept, t)
		}
	}
	i.crashes = kept
}

// Restart closes the current process deliberately (not a crash), clears the
// crash window, and starts a fresh one (spec §7.8).
func (i *Instance) Restart(ctx context.Context) error {
	i.mu.Lock()
	if i.stopping || i.doneClosed || i.gen == nil {
		err := i.stoppedErrorLocked()
		i.mu.Unlock()
		return err
	}
	i.stopping = true
	i.initialized = false
	i.open = false
	i.crashes = nil
	i.restartSeq = 0
	gen := i.gen
	i.gen = nil
	i.mu.Unlock()

	i.setState(StateStarting)
	i.stopGeneration(gen)
	i.mu.Lock()
	i.stopping = false
	// stopping gates callers (a concurrent Shutdown waits for it to clear),
	// so clearing it must wake them.
	i.signalLocked()
	i.mu.Unlock()

	if err := i.launchAndInitialize(ctx); err != nil {
		i.setLastError(err.Error())
		i.setState(StateFailed)
		i.finishTerminal()
		return err
	}
	if cb := i.spec.OnRestart; cb != nil {
		cb(ctx)
	}
	i.mu.Lock()
	i.open = true
	i.signalLocked()
	i.mu.Unlock()
	return nil
}

// Shutdown stops the server: gracefully if it answers, the whole process
// tree otherwise. Idempotent (spec §7.8).
func (i *Instance) Shutdown(ctx context.Context) error {
	for {
		i.mu.Lock()
		if i.doneClosed {
			i.mu.Unlock()
			return nil
		}
		if i.stopping {
			// Another deliberate stop is closing the current process (a
			// Restart mid-flight, or a concurrent Shutdown). Wait for that
			// phase to end and take over: a Restart that succeeds never
			// closes done, so waiting on done alone would block forever.
			ch := i.stateCh
			i.mu.Unlock()
			select {
			case <-ch:
			case <-i.done:
			}
			continue
		}
		i.stopping = true
		i.initialized = false
		i.open = false
		gen := i.gen
		i.gen = nil
		i.state = StateStopped
		i.signalLocked()
		i.mu.Unlock()
		i.fireOnStateChange()

		i.stopGeneration(gen)
		if i.stderrW != nil {
			i.stderrW.flush()
		}
		i.log.close()
		i.finishTerminal()
		return nil
	}
}

// stopGeneration runs the close sequence for one generation: shutdown
// request, exit notification, then SIGTERM and SIGKILL to the whole tree
// (spec §7.8). Every step is best effort — the goal is that nothing is left.
func (i *Instance) stopGeneration(gen *generation) {
	if gen == nil {
		return
	}
	timeout := i.spec.ShutdownTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	go func() {
		shCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		_ = gen.conn.Call(shCtx, "shutdown", nil, nil)
		_ = gen.conn.Notify("exit", nil)
	}()
	select {
	case <-gen.waitDone:
	case <-time.After(time.Second):
	}
	_ = gen.tree.terminate()
	select {
	case <-gen.waitDone:
	case <-time.After(2 * time.Second):
	}
	_ = gen.tree.kill()
	<-gen.waitDone
	gen.tree.release()
	_ = gen.conn.Close()
	forgetPID(i.spec.PIDFile, gen.cmd.Process.Pid)
}

// AddFolder adds a workspace folder and tells the server about it.
func (i *Instance) AddFolder(ctx context.Context, root string) error {
	folder := WorkspaceFolder{URI: PathToURI(root), Name: filepath.Base(root)}
	i.mu.Lock()
	known := false
	for _, f := range i.folders {
		if f.URI == folder.URI {
			known = true
			break
		}
	}
	if !known {
		i.folders = append(i.folders, folder)
	}
	i.mu.Unlock()
	if known {
		return nil
	}
	return i.Notify("workspace/didChangeWorkspaceFolders", map[string]any{
		"event": map[string]any{
			"added":   []WorkspaceFolder{folder},
			"removed": []WorkspaceFolder{},
		},
	})
}

// Registrations returns the registerOptions of the live registrations for
// method, in registration-id order.
func (i *Instance) Registrations(method string) []json.RawMessage {
	i.mu.Lock()
	defer i.mu.Unlock()
	ids := make([]string, 0, len(i.regs))
	for id, r := range i.regs {
		if r.Method == method {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]json.RawMessage, 0, len(ids))
	for _, id := range ids {
		out = append(out, i.regs[id].RegisterOptions)
	}
	return out
}

func (i *Instance) Capabilities() ServerCapabilities {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.caps
}

func (i *Instance) Encoding() Encoding {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.encoding
}

// consumePullStale reports whether a workspace/diagnostic/refresh arrived
// since the last call and clears the flag: the diagnostics layer re-pulls
// pull-model servers when it returns true.
func (i *Instance) consumePullStale() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	stale := i.pullStale
	i.pullStale = false
	return stale
}

func (i *Instance) State() InstanceState {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.state
}

// Progress reports the highest percentage among active work-done tokens;
// -1 when no active token reported one.
func (i *Instance) Progress() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	best := -1
	for _, pct := range i.progress {
		if pct != nil && *pct > best {
			best = *pct
		}
	}
	return best
}

func (i *Instance) LastError() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.lastError
}

func (i *Instance) PID() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.pid
}

// Folders returns the workspace folder URIs the server knows about.
func (i *Instance) Folders() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]string, 0, len(i.folders))
	for _, f := range i.folders {
		out = append(out, f.URI)
	}
	return out
}

// Done is closed when the instance stopped or failed for good.
func (i *Instance) Done() <-chan struct{} {
	return i.done
}

// setState records a state change and wakes waiters. OnStateChange runs
// outside the mutex. Once Done closed the instance is terminal for good:
// restart paths still in flight must not rewind the state.
func (i *Instance) setState(s InstanceState) {
	i.mu.Lock()
	if i.state == s || i.doneClosed {
		i.mu.Unlock()
		return
	}
	i.state = s
	i.signalLocked()
	i.mu.Unlock()
	i.fireOnStateChange()
}

// signal bumps stateCh without a state change (progress, open changes).
func (i *Instance) signal() {
	i.mu.Lock()
	i.signalLocked()
	i.mu.Unlock()
}

func (i *Instance) signalLocked() {
	close(i.stateCh)
	i.stateCh = make(chan struct{})
}

func (i *Instance) fireOnStateChange() {
	if cb := i.spec.OnStateChange; cb != nil {
		cb()
	}
}

func (i *Instance) setLastError(msg string) {
	i.mu.Lock()
	i.lastError = msg
	i.mu.Unlock()
}

func (i *Instance) kickWatcher() {
	select {
	case i.kick <- struct{}{}:
	default:
	}
}

// finishTerminal closes Done exactly once.
func (i *Instance) finishTerminal() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.doneClosed {
		close(i.done)
		i.doneClosed = true
	}
}

// resolveCommand resolves spec.Command against the PATH in env (falling
// back to this process's), like the server itself will see it.
func resolveCommand(command string, env []string) (string, error) {
	path := envValue(env, "PATH")
	if path != "" && !strings.ContainsRune(command, os.PathSeparator) {
		for _, dir := range filepath.SplitList(path) {
			if dir == "" {
				continue
			}
			candidate := filepath.Join(dir, command)
			if executableFile(candidate) != "" {
				return candidate, nil
			}
		}
		return "", fmt.Errorf("command %q not found", command)
	}
	resolved, err := exec.LookPath(command)
	if err != nil {
		return "", fmt.Errorf("command %q not found", command)
	}
	return resolved, nil
}

// envValue returns the last value of name in an exec environment.
func envValue(env []string, name string) string {
	prefix := name + "="
	value := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, prefix); ok {
			value = v
		}
	}
	return value
}

// executableFile returns path when it is an executable regular file.
func executableFile(path string) string {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
		return ""
	}
	return path
}

// serverLog is the instance's append-only log: every line is timestamped,
// and a file over 5 MiB is rotated when the instance starts (spec §5.4).
type serverLog struct {
	mu       sync.Mutex
	f        *os.File
	lastLine string
}

func openServerLog(path string) *serverLog {
	if path == "" {
		return nil
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if st, err := os.Stat(path); err == nil && st.Size() > serverLogRotateBytes {
		_ = os.Rename(path, path+".1") // only one previous log is kept
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	return &serverLog{f: f}
}

func (l *serverLog) write(kind, text string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	line := strings.TrimRight(text, "\r\n")
	fmt.Fprintf(l.f, "%s %s: %s\n", time.Now().Format(time.RFC3339), kind, line)
	l.lastLine = line
}

func (l *serverLog) lastLogLine() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastLine
}

func (l *serverLog) close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}

// stderrLogWriter line-buffers the server's stderr into the instance log;
// exec.Cmd's copy goroutine is the only writer.
type stderrLogWriter struct {
	log *serverLog
	mu  sync.Mutex
	buf []byte
}

func (w *stderrLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		idx := bytes.IndexByte(w.buf, '\n')
		if idx < 0 {
			break
		}
		w.log.write("stderr", string(w.buf[:idx]))
		w.buf = w.buf[idx+1:]
	}
	return len(p), nil
}

// flush writes a trailing partial line once the process is gone.
func (w *stderrLogWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.log.write("stderr", string(w.buf))
		w.buf = nil
	}
}
