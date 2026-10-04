package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

// installTimeout bounds one install command (spec §6.5). A package-level
// variable only so tests can shorten it; production code must not write it.
var installTimeout = 10 * time.Minute

// installKillGrace bounds the wait for an install command's orphaned children
// to release the output pipes (exec.Cmd.WaitDelay): a killed installer may
// leave a grandchild holding the pipe, and the timeout must still bound the
// call. A package-level variable only so tests can shorten it.
var installKillGrace = 2 * time.Second

// installTailLines is how many output lines a failure report keeps.
const installTailLines = 20

// installScannerBuffer bounds one install output line (the same 1 MiB the
// protocol layer allows).
const installScannerBuffer = 1 << 20

// DetectResult is what Detect learned about one server binary (spec §6.4).
type DetectResult struct {
	Installed      bool
	Path           string
	Version        string
	InstallRecipe  *InstallRecipe // first usable recipe (spec §6.5); nil when none
	InstallCommand string         // strings.Join(recipe.Argv, " ")
}

// Detect locates srv's command on the server's PATH, extra dirs and (darwin)
// xcrun, and reads its version (spec §6.4). cachePath is detect.json (""
// disables caching).
func Detect(ctx context.Context, srv ServerConfig, env []string, goos string, cachePath string) DetectResult {
	res := DetectResult{}
	path := findServerBinary(srv, env, goos)
	if path == "" {
		path = findViaXcrun(ctx, srv.Detect.Xcrun, goos)
	}
	if path != "" {
		res.Path = path
		resolveVersion(ctx, &res, srv.Detect.VersionArgs, env, cachePath)
	}
	if recipe := UsableInstallRecipe(srv, env, goos); recipe != nil {
		recipeCopy := *recipe
		res.InstallRecipe = &recipeCopy
		res.InstallCommand = strings.Join(recipe.Argv, " ")
	}
	return res
}

// resolveVersion fills Installed and Version for a located binary: the
// detect.json cache answers while the path's mtime is unchanged, otherwise
// the version command runs (empty VersionArgs: found on disk counts as
// installed, no version).
func resolveVersion(ctx context.Context, res *DetectResult, versionArgs, env []string, cachePath string) {
	var mtime int64
	if st, err := os.Stat(res.Path); err == nil {
		mtime = st.ModTime().Unix()
	}
	if cachePath != "" {
		if entry, ok := loadDetectEntry(cachePath, res.Path); ok && entry.MTime == mtime {
			res.Installed = entry.Installed
			res.Version = entry.Version
			return
		}
	}
	if len(versionArgs) == 0 {
		res.Installed = true
	} else {
		res.Installed, res.Version = runVersionCommand(ctx, res.Path, versionArgs, env)
	}
	if cachePath != "" {
		saveDetectEntry(cachePath, res.Path, mtime, res.Version, res.Installed)
	}
}

// runVersionCommand executes <path> <args> with the server's environment and
// a 3-second timeout. A non-zero exit means not installed — a rustup proxy
// exists on disk but errors while its component is missing.
func runVersionCommand(ctx context.Context, path string, args, env []string) (installed bool, version string) {
	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, path, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, ""
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if len(line) > 80 {
		line = line[:80]
	}
	return true, line
}

// findServerBinary locates srv.Command as an absolute or explicit path, then
// on the PATH the server will see, then in the detect extra dirs.
func findServerBinary(srv ServerConfig, env []string, goos string) string {
	if filepath.IsAbs(srv.Command) || strings.ContainsRune(srv.Command, os.PathSeparator) {
		if executableFile(srv.Command) != "" {
			return srv.Command
		}
		return ""
	}
	if path := lookPathIn(srv.Command, PathFromEnv(env), goos, env); path != "" {
		return path
	}
	for _, dir := range srv.Detect.ExtraDirs {
		expanded := expandExtraDir(dir, env)
		if expanded == "" {
			continue
		}
		if path := lookPathIn(srv.Command, expanded, goos, env); path != "" {
			return path
		}
	}
	return ""
}

// findViaXcrun answers `xcrun --find <tool>` on darwin (2-second timeout):
// installed when the command exits 0 naming an existing file.
func findViaXcrun(ctx context.Context, tool, goos string) string {
	if goos != "darwin" || tool == "" {
		return ""
	}
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(runCtx, "xcrun", "--find", tool).Output()
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if executableFile(path) != "" {
		return path
	}
	return ""
}

// UsableInstallRecipe returns the first recipe whose platform matches goos and
// whose Requires command is on the PATH in env.
func UsableInstallRecipe(srv ServerConfig, env []string, goos string) *InstallRecipe {
	path := PathFromEnv(env)
	for i := range srv.Install {
		recipe := &srv.Install[i]
		if len(recipe.Platforms) > 0 && !slices.Contains(recipe.Platforms, goos) {
			continue
		}
		if recipe.Requires != "" && lookPathIn(recipe.Requires, path, goos, env) == "" {
			continue
		}
		return recipe
	}
	return nil
}

// Install runs the first usable install recipe for srv as the user, on the
// host, with the user's full environment (spec §6.5). progress receives each
// output line (stdout and stderr merged). Only an explicit user action may
// call it: the recommendation dialog, /lsp, or forebrain lsp install.
func Install(ctx context.Context, srv ServerConfig, goos, detectCachePath string, progress func(line string)) error {
	recipe := UsableInstallRecipe(srv, os.Environ(), goos)
	if recipe == nil {
		notes := strings.TrimSpace(srv.Notes)
		if notes == "" {
			notes = "see the server's documentation"
		}
		return fmt.Errorf("no install recipe for %s on this system; install it manually: %s", srv.ID, notes) // appendix C, install-unavailable
	}

	ctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, recipe.Argv[0], recipe.Argv[1:]...)
	if dir, err := os.UserHomeDir(); err == nil {
		cmd.Dir = dir
	}
	cmd.Env = os.Environ()
	cmd.WaitDelay = installKillGrace
	// One pipe for both streams, so the lines interleave the way the user
	// would have seen them on a terminal.
	pipeR, pipeW := io.Pipe()
	cmd.Stdout = pipeW
	cmd.Stderr = pipeW

	var (
		tail     []string
		scanned  sync.WaitGroup
		runErr   error
		startErr error
	)
	scanned.Add(1)
	go func() {
		defer scanned.Done()
		scanner := bufio.NewScanner(pipeR)
		scanner.Buffer(make([]byte, 0, 64*1024), installScannerBuffer)
		for scanner.Scan() {
			line := scanner.Text()
			if len(tail) < installTailLines {
				tail = append(tail, line)
			} else {
				copy(tail, tail[1:])
				tail[len(tail)-1] = line
			}
			if progress != nil {
				progress(line)
			}
		}
	}()
	if err := cmd.Start(); err != nil {
		startErr = err
	} else {
		runErr = cmd.Wait()
	}
	_ = pipeW.Close() // EOF: the scanner drains what was written and stops
	scanned.Wait()
	if startErr != nil || runErr != nil {
		failure := runErr
		if startErr != nil {
			failure = startErr
		}
		return fmt.Errorf("install command %q failed: %v\n%s", strings.Join(recipe.Argv, " "), failure, strings.Join(tail, "\n"))
	}

	// Installed: drop this binary's cached detections and re-probe (spec
	// §6.5), with the environment the server itself will run with.
	dropDetectEntries(detectCachePath, filepath.Base(srv.Command))
	env, _ := BuildEnv(EnvSpec{Passthrough: srv.EnvPassthrough, Env: srv.Env, FromProject: srv.EnvFromProject})
	det := Detect(ctx, srv, env, goos, detectCachePath)
	if !det.Installed {
		return fmt.Errorf("the install command finished but %s is still not found; add its directory to PATH or set lsp.servers.%s.command to its absolute path", srv.Command, srv.ID)
	}
	return nil
}

// dropDetectEntries rewrites detect.json without the entries whose file name
// is name; a missing or unreadable cache is nothing to drop.
func dropDetectEntries(cachePath, name string) {
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return
	}
	cache := map[string]detectCacheEntry{}
	if err := json.Unmarshal(data, &cache); err != nil {
		return
	}
	changed := false
	for path := range cache {
		if filepath.Base(path) == name {
			delete(cache, path)
			changed = true
		}
	}
	if !changed {
		return
	}
	out, err := json.Marshal(cache)
	if err != nil {
		return
	}
	_ = writeFileAtomic(cachePath, out)
}

// PathFromEnv returns the PATH entry of env, or os.Getenv("PATH").
func PathFromEnv(env []string) string {
	if path := envValue(env, "PATH"); path != "" {
		return path
	}
	return os.Getenv("PATH")
}

// lookPathIn finds command in the dirs of path (a single dir or a whole PATH
// list), appending the PATHEXT extensions on windows as exec.LookPath does.
func lookPathIn(command, path, goos string, env []string) string {
	if command == "" {
		return ""
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		if found := tryExecutable(filepath.Join(dir, command), goos, env); found != "" {
			return found
		}
	}
	return ""
}

// tryExecutable answers candidate when it is executable, trying the windows
// PATHEXT extensions when the name does not already carry one.
func tryExecutable(candidate, goos string, env []string) string {
	if executableFile(candidate) != "" {
		return candidate
	}
	if goos != "windows" {
		return ""
	}
	ext := strings.ToLower(filepath.Ext(candidate))
	for _, pathExt := range pathExtensions(env) {
		if ext == strings.ToLower(pathExt) {
			return ""
		}
	}
	for _, pathExt := range pathExtensions(env) {
		if found := executableFile(candidate + pathExt); found != "" {
			return found
		}
	}
	return ""
}

// pathExtensions lists the PATHEXT suffixes windows tries, from env when
// present, else the documented default.
func pathExtensions(env []string) []string {
	value := envValue(env, "PATHEXT")
	if value == "" {
		value = os.Getenv("PATHEXT")
	}
	if value == "" {
		value = ".COM;.EXE;.BAT;.CMD"
	}
	return strings.Split(strings.TrimSpace(value), ";")
}

// expandExtraDir expands one detect.extra_dirs entry: $VAR from env (an
// entry referencing an unset variable is skipped), ~/ from the user's home
// directory. Results that are empty or still hold a $ are skipped.
func expandExtraDir(dir string, env []string) string {
	if rest, ok := strings.CutPrefix(dir, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, rest)
	}
	unresolved := false
	dir = os.Expand(dir, func(name string) string {
		value := envValue(env, name)
		if value == "" {
			unresolved = true
		}
		return value
	})
	if unresolved || dir == "" || strings.Contains(dir, "$") {
		return ""
	}
	return dir
}

// detectCacheEntry is one binary's cached detect result (spec §5.4).
type detectCacheEntry struct {
	MTime     int64  `json:"mtime"`
	Version   string `json:"version"`
	Installed bool   `json:"installed"`
}

// loadDetectEntry reads one binary's cache entry from detect.json.
func loadDetectEntry(cachePath, path string) (detectCacheEntry, bool) {
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return detectCacheEntry{}, false
	}
	cache := map[string]detectCacheEntry{}
	if err := json.Unmarshal(data, &cache); err != nil {
		return detectCacheEntry{}, false
	}
	entry, ok := cache[path]
	return entry, ok
}

// saveDetectEntry rewrites detect.json with one binary's result, atomically.
func saveDetectEntry(cachePath, path string, mtime int64, version string, installed bool) {
	cache := map[string]detectCacheEntry{}
	if data, err := os.ReadFile(cachePath); err == nil {
		_ = json.Unmarshal(data, &cache)
	}
	cache[path] = detectCacheEntry{MTime: mtime, Version: version, Installed: installed}
	data, err := json.Marshal(cache)
	if err != nil {
		return
	}
	_ = writeFileAtomic(cachePath, data)
}

// DoctorInput names what forebrain lsp doctor checks.
type DoctorInput struct {
	Config         *appcfg.Root
	Home           string
	AgentWorkspace string
	ProjectRoot    string // absolute; "" when the directory is not inside a project
	Trusted        bool   // safety.TrustedRoot(launch) != ""
	ServerID       string // "" checks every enabled server
	Handshake      bool   // start each server once and shut it down
}

// DoctorCheck is one line of a server's report.
type DoctorCheck struct {
	Name   string // "config" | "binary" | "environment" | "project" | "handshake"
	Status string // "ok" | "warn" | "fail" | "skip"
	Detail string
}

// DoctorReport is one server's checks, in the order above.
type DoctorReport struct {
	ID          string
	DisplayName string
	Languages   []string
	Enabled     bool
	LogPath     string // the handshake's log, "" when it did not run
	Checks      []DoctorCheck
}

// The check names in report order.
const (
	doctorCheckConfig      = "config"
	doctorCheckBinary      = "binary"
	doctorCheckEnvironment = "environment"
	doctorCheckProject     = "project"
	doctorCheckHandshake   = "handshake"
)

// doctorCheckNames is the report order; a failing check skips the rest.
var doctorCheckNames = []string{
	doctorCheckConfig, doctorCheckBinary, doctorCheckEnvironment,
	doctorCheckProject, doctorCheckHandshake,
}

// Doctor checks configured servers; it never enables or installs anything.
// An explicit ServerID checks that server whether or not it is enabled;
// otherwise every enabled server is checked, and none being enabled is an
// empty report, not an error.
func Doctor(ctx context.Context, in DoctorInput) ([]DoctorReport, error) {
	enabled, err := LoadEnabled(in.AgentWorkspace)
	if err != nil {
		enabled = nil // unreadable: resolve as if empty, the same as Manager
	}
	servers := ResolveServers(ResolveInput{Config: in.Config, Enabled: enabled, GOOS: runtime.GOOS})
	var selected []ServerConfig
	if in.ServerID != "" {
		for _, sc := range servers {
			if sc.ID == in.ServerID {
				selected = append(selected, sc)
				break
			}
		}
		if len(selected) == 0 {
			return nil, fmt.Errorf("unknown language server %q; run forebrain lsp list", in.ServerID)
		}
	} else {
		for _, sc := range servers {
			if sc.Enabled {
				selected = append(selected, sc)
			}
		}
	}
	reports := make([]DoctorReport, 0, len(selected))
	for _, sc := range selected {
		reports = append(reports, doctorServer(ctx, in, sc))
	}
	return reports, nil
}

// doctorServer runs one server's checks. A failing check skips the remaining
// ones, each naming what it skipped after.
func doctorServer(ctx context.Context, in DoctorInput, srv ServerConfig) DoctorReport {
	rep := DoctorReport{
		ID:          srv.ID,
		DisplayName: srv.DisplayName,
		Languages:   srv.Languages,
		Enabled:     srv.Enabled,
		Checks:      make([]DoctorCheck, 0, len(doctorCheckNames)),
	}
	failed := ""
	skip := func(name string) DoctorCheck {
		return DoctorCheck{Name: name, Status: "skip", Detail: "skipped: " + failed + " failed"}
	}

	// config
	if srv.Invalid != "" {
		failed = doctorCheckConfig
		rep.Checks = append(rep.Checks, DoctorCheck{Name: doctorCheckConfig, Status: "fail", Detail: srv.Invalid})
	} else {
		detail := fmt.Sprintf("%s entry, fingerprint %s", srv.Scope, srv.Fingerprint)
		rep.Checks = append(rep.Checks, DoctorCheck{Name: doctorCheckConfig, Status: "ok", Detail: detail})
	}

	// binary and environment share the server's environment.
	env, missing := BuildEnv(EnvSpec{
		Passthrough: srv.EnvPassthrough,
		Env:         srv.Env,
		FromProject: srv.EnvFromProject,
		Home:        in.Home,
	})
	det := DetectResult{}
	if failed == "" {
		det = Detect(ctx, srv, env, runtime.GOOS, filepath.Join(StateDir(in.AgentWorkspace), "detect.json"))
	}
	if failed != "" {
		rep.Checks = append(rep.Checks, skip(doctorCheckBinary))
	} else if det.Installed {
		detail := det.Path
		if det.Version != "" {
			detail += " (" + det.Version + ")"
		}
		rep.Checks = append(rep.Checks, DoctorCheck{Name: doctorCheckBinary, Status: "ok", Detail: detail})
	} else {
		failed = doctorCheckBinary
		detail := srv.Command + " not found"
		switch {
		case det.InstallCommand != "":
			detail += "; install with: forebrain lsp install " + srv.ID + " (runs: " + det.InstallCommand + ")"
		case srv.Notes != "":
			detail += "; " + srv.Notes
		default:
			detail += "; see the server's documentation"
		}
		rep.Checks = append(rep.Checks, DoctorCheck{Name: doctorCheckBinary, Status: "fail", Detail: detail})
	}

	if failed != "" {
		rep.Checks = append(rep.Checks, skip(doctorCheckEnvironment))
	} else if len(missing) > 0 {
		refs := make([]string, len(missing))
		for i, name := range missing {
			refs[i] = "${" + name + "}"
		}
		rep.Checks = append(rep.Checks, DoctorCheck{Name: doctorCheckEnvironment, Status: "warn",
			Detail: "unresolved: " + strings.Join(refs, ", ")})
	} else {
		rep.Checks = append(rep.Checks, DoctorCheck{Name: doctorCheckEnvironment, Status: "ok",
			Detail: doctorEnvironmentDetail(srv)})
	}

	// project
	if failed != "" {
		rep.Checks = append(rep.Checks, skip(doctorCheckProject))
	} else {
		status, detail := doctorProject(srv, in)
		if status == "fail" {
			failed = doctorCheckProject
		}
		rep.Checks = append(rep.Checks, DoctorCheck{Name: doctorCheckProject, Status: status, Detail: detail})
	}

	// handshake
	handshake := DoctorCheck{Name: doctorCheckHandshake, Status: "skip", Detail: "skipped"}
	switch {
	case failed != "":
		handshake = skip(doctorCheckHandshake)
	case !in.Handshake:
		// stays skipped
	case !srv.Enabled:
		handshake.Detail = "skipped: not enabled (forebrain lsp enable " + srv.ID + ")"
	case in.ProjectRoot == "" || !in.Trusted:
		handshake.Detail = "skipped: servers only run in trusted projects" // spec §9.2
	default:
		status, detail, logPath := doctorHandshake(ctx, in, srv)
		handshake = DoctorCheck{Name: doctorCheckHandshake, Status: status, Detail: detail}
		rep.LogPath = logPath
	}
	rep.Checks = append(rep.Checks, handshake)
	return rep
}

// doctorEnvironmentDetail names the allowed environment variables that carry
// values — never the values themselves.
func doctorEnvironmentDetail(srv ServerConfig) string {
	var present []string
	for _, name := range srv.EnvPassthrough {
		if _, ok := os.LookupEnv(name); ok {
			present = append(present, name)
		}
	}
	detail := fmt.Sprintf("passes %d of %d allowed variables", len(present), len(srv.EnvPassthrough))
	if len(present) > 0 {
		detail += ": " + strings.Join(present, ", ")
	}
	return detail
}

// doctorProject checks the project context: a root that exists and is
// trusted, shown with the server's markers that sit at it, plus the hints
// (spec §13 risks: clangd without a compilation database, unrestored .NET
// packages, servers that write into the project).
func doctorProject(srv ServerConfig, in DoctorInput) (status, detail string) {
	var hints []string
	switch {
	case in.ProjectRoot == "":
		return "warn", "not inside a project; servers only run in a project"
	case !in.Trusted:
		return "warn", "project is not trusted; servers only run in trusted projects"
	}
	root, err := filepath.EvalSymlinks(in.ProjectRoot)
	if err != nil {
		root = filepath.Clean(in.ProjectRoot)
	}
	detail = root
	var markers []string
	for _, marker := range srv.RootMarkers {
		if marker != "" && dirContainsAnyMarker(root, []string{marker}) {
			markers = append(markers, marker)
		}
	}
	if len(markers) > 0 {
		detail += " (" + strings.Join(markers, ", ") + ")"
	}
	hints = doctorHints(srv, root)
	if len(hints) > 0 {
		detail += "; " + strings.Join(hints, "; ")
		return "warn", detail
	}
	return "ok", detail
}

// doctorHints lists what the user can still do about this server in this
// project (spec §13, risk table).
func doctorHints(srv ServerConfig, root string) []string {
	var hints []string
	switch srv.ID {
	case "clangd":
		if !fileExists(filepath.Join(root, "compile_commands.json")) &&
			!fileExists(filepath.Join(root, "build", "compile_commands.json")) &&
			!fileExists(filepath.Join(root, "compile_flags.txt")) {
			hints = append(hints, "no compile_commands.json: generate one with cmake -DCMAKE_EXPORT_COMPILE_COMMANDS=ON or bear -- make")
		}
	case "csharp-ls":
		if hasGlob(root, []string{"*.csproj", "*.sln"}) && !fileExists(filepath.Join(root, "obj", "project.assets.json")) &&
			!hasGlob(root, []string{"*/obj/project.assets.json"}) {
			hints = append(hints, "packages are not restored: run dotnet restore")
		}
	}
	if len(srv.ProjectWrites) > 0 {
		hints = append(hints, "writes into the project: "+strings.Join(srv.ProjectWrites, ", "))
	}
	return hints
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// hasGlob reports whether dir holds a file matching any of the patterns.
func hasGlob(dir string, patterns []string) bool {
	for _, pattern := range patterns {
		if matches, err := filepath.Glob(filepath.Join(dir, pattern)); err == nil && len(matches) > 0 {
			return true
		}
	}
	return false
}

// doctorHandshake starts the server once at the project root, reads what the
// surfaces would show (capabilities, encoding, memory) and shuts it down
// again. It runs only in a trusted project (spec §9.2).
func doctorHandshake(ctx context.Context, in DoctorInput, srv ServerConfig) (status, detail, logPath string) {
	root, ok := doctorWorkspaceRoot(srv, in.ProjectRoot)
	if !ok {
		return "fail", "the workspace folder is outside the project", ""
	}
	plan, err := planLaunch(ctx, srv, root, in.Home, in.AgentWorkspace)
	if err != nil {
		return "fail", err.Error(), ""
	}
	start := time.Now()
	inst, err := StartInstance(ctx, plan.Spec)
	if err != nil {
		return "fail", err.Error(), plan.Spec.LogPath
	}
	defer func() { _ = inst.Shutdown(ctx) }()

	caps := inst.Capabilities()
	var supported []string
	for _, capability := range []struct {
		label string
		key   string
	}{
		{"definition", "definitionProvider"},
		{"references", "referencesProvider"},
		{"hover", "hoverProvider"},
		{"document symbols", "documentSymbolProvider"},
		{"workspace symbols", "workspaceSymbolProvider"},
		{"call hierarchy", "callHierarchyProvider"},
		{"type hierarchy", "typeHierarchyProvider"},
		{"pull diagnostics", "diagnosticProvider"},
	} {
		if caps.Supports(capability.key) {
			supported = append(supported, capability.label)
		}
	}
	detail = fmt.Sprintf("initialized in %.1fs; %s; %s",
		time.Since(start).Seconds(), inst.Encoding(), strings.Join(supported, ", "))
	if mib := residentMiB(inst.PID()); mib > 0 {
		detail += fmt.Sprintf("; %d MiB resident", mib)
	}
	return "ok", detail, plan.Spec.LogPath
}

// doctorWorkspaceRoot is the root doctor hands the server: the project root,
// or the server's configured workspace folder when it declares one (the
// ResolveRoot rule).
func doctorWorkspaceRoot(srv ServerConfig, projectRoot string) (string, bool) {
	root, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		root = filepath.Clean(projectRoot)
	}
	if srv.WorkspaceFolder == "" {
		return root, true
	}
	folder := srv.WorkspaceFolder
	if !filepath.IsAbs(folder) {
		folder = filepath.Join(root, folder)
	}
	folder = filepath.Clean(folder)
	if !pathWithin(folder, root) {
		return "", false
	}
	return folder, true
}

// residentMiB reads a process's resident set size (in MiB) through ps; 0 when
// that is not possible on this platform or for this pid.
func residentMiB(pid int) int {
	if pid <= 0 || runtime.GOOS == "windows" {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	rss, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || rss <= 0 {
		return 0
	}
	return rss / 1024
}
