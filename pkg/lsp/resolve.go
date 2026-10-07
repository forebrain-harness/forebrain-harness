package lsp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

// ServerConfig is one server after merging (spec §5.3).
type ServerConfig struct {
	ID, DisplayName       string
	Languages             []string
	Scope                 string // "catalog" | "global" | "project"
	InCatalog             bool
	Role                  string
	Priority              int
	Command               string
	Args                  []string
	ExtensionToLanguage   map[string]string
	Filenames             map[string]string
	RootMarkers           []string
	RootStrategy          string
	RequireRootMarker     bool
	WorkspaceFolder       string
	Env                   map[string]string
	EnvPassthrough        []string
	EnvFromProject        bool
	InitializationOptions json.RawMessage
	Settings              json.RawMessage
	StartupTimeout        time.Duration
	ShutdownTimeout       time.Duration
	RestartOnCrash        bool
	MaxRestarts           int
	Diagnostics           bool
	Prewarm               bool
	Readiness             string
	AutoAnswers           map[string]string
	Conflicts             []Conflict
	ProjectWrites         []string
	Install               []InstallRecipe
	Detect                DetectSpec
	Notes                 string
	Enabled               bool
	Fingerprint           string // 12 hex characters
	Invalid               string // why the entry cannot run; "" when usable
}

// ResolveInput names everything ResolveServers merges (spec §5.3).
type ResolveInput struct {
	Config         *appcfg.Root
	ProjectServers map[string]appcfg.LSPServerConfig // consented project entries only (task 15); nil before that
	Enabled        map[string]bool                   // enabled.json
	GOOS           string                            // runtime.GOOS in production
}

// ResolveServers merges catalog, global and project entries; sorted by ID.
func ResolveServers(in ResolveInput) []ServerConfig {
	// An UnmarshalStrict failure is a build defect the catalog test catches;
	// resolution still answers for explicitly configured servers.
	entries, _ := Catalog()
	byID := make(map[string]*ServerConfig)
	explicit := make(map[string]*bool) // the merged `enabled` from the overlay chain
	for _, e := range entries {
		sc := serverConfigFromCatalog(e, in.GOOS)
		byID[sc.ID] = &sc
	}
	var global map[string]appcfg.LSPServerConfig
	if in.Config != nil {
		global = in.Config.LSP.Servers
	}
	for _, id := range sortedServerIDs(global) {
		overlayUserEntry(byID, explicit, id, global[id], "global")
	}
	for _, id := range sortedServerIDs(in.ProjectServers) {
		overlayUserEntry(byID, explicit, id, in.ProjectServers[id], "project")
	}

	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]ServerConfig, 0, len(ids))
	for _, id := range ids {
		sc := byID[id]
		if !sc.InCatalog {
			if sc.Command == "" || (len(sc.ExtensionToLanguage) == 0 && len(sc.Filenames) == 0) {
				sc.Invalid = "custom servers need command and extension_to_language or filenames"
			}
		}
		sc.Enabled = resolvedEnabled(sc, explicit[id], in.Enabled)
		sc.Fingerprint = configFingerprint(*sc)
		out = append(out, *sc)
	}
	return out
}

// overlayUserEntry folds one configured entry (global or project) into the
// merge, creating a custom server when the id is not in the catalog.
func overlayUserEntry(byID map[string]*ServerConfig, explicit map[string]*bool, id string, e appcfg.LSPServerConfig, scope string) {
	if sc, ok := byID[id]; ok {
		overlayServerConfig(sc, e, scope == "project")
		sc.Scope = scope
		sc.EnvFromProject = sc.EnvFromProject || scope == "project"
	} else {
		sc := serverConfigFromUserEntry(id, e, scope)
		byID[id] = &sc
	}
	if e.Enabled != nil {
		value := *e.Enabled
		explicit[id] = &value
	}
}

// resolvedEnabled applies the §5.3 order: enabled.json wins, then the merged
// `enabled`, then the defaults — false for catalog servers, true for custom.
func resolvedEnabled(sc *ServerConfig, explicit *bool, stateFile map[string]bool) bool {
	if v, ok := stateFile[sc.ID]; ok {
		return v
	}
	if explicit != nil {
		return *explicit
	}
	return !sc.InCatalog
}

// serverConfigFromCatalog converts one catalog entry, applying the §5.1/§5.3
// defaults and the darwin command override.
func serverConfigFromCatalog(e CatalogEntry, goos string) ServerConfig {
	sc := ServerConfig{
		ID:                    e.ID,
		DisplayName:           e.DisplayName,
		Languages:             e.Languages,
		Scope:                 "catalog",
		InCatalog:             true,
		Role:                  e.Role,
		Priority:              e.Priority,
		Command:               e.Command,
		Args:                  e.Args,
		ExtensionToLanguage:   e.ExtensionToLanguage,
		Filenames:             e.Filenames,
		RootMarkers:           e.RootMarkers,
		RootStrategy:          e.RootStrategy,
		RequireRootMarker:     e.RequireRootMarker,
		EnvPassthrough:        e.EnvPassthrough,
		InitializationOptions: json.RawMessage(e.InitializationOptions),
		Settings:              json.RawMessage(e.Settings),
		StartupTimeout:        time.Duration(e.StartupTimeout) * time.Second,
		ShutdownTimeout:       5 * time.Second,
		RestartOnCrash:        true,
		MaxRestarts:           3,
		Diagnostics:           true,
		Readiness:             e.Readiness,
		AutoAnswers:           e.AutoAnswers,
		Conflicts:             e.Conflicts,
		ProjectWrites:         e.ProjectWrites,
		Install:               e.Install,
		Detect:                e.Detect,
		Notes:                 e.Notes,
	}
	if sc.StartupTimeout == 0 {
		sc.StartupTimeout = 60 * time.Second
	}
	if goos == "darwin" && e.CommandDarwin != "" {
		sc.Command = e.CommandDarwin
		if e.ArgsDarwin != nil {
			sc.Args = e.ArgsDarwin
		}
	}
	return sc
}

// serverConfigFromUserEntry starts a server the catalog does not know, with
// the custom defaults (priority 50, enabled by default).
func serverConfigFromUserEntry(id string, e appcfg.LSPServerConfig, scope string) ServerConfig {
	sc := ServerConfig{
		ID:              id,
		DisplayName:     id,
		Scope:           scope,
		InCatalog:       false,
		Role:            appcfg.LSPRolePrimary,
		Priority:        50,
		RootStrategy:    "nearest",
		EnvFromProject:  scope == "project",
		StartupTimeout:  60 * time.Second,
		ShutdownTimeout: 5 * time.Second,
		RestartOnCrash:  true,
		MaxRestarts:     3,
		Diagnostics:     true,
		Readiness:       "none",
	}
	overlayServerConfig(&sc, e, scope == "project")
	return sc
}

// overlayServerConfig copies the non-zero fields of e onto sc: strings must
// be non-empty, slices and maps non-nil, pointers non-nil, counters non-zero.
// Project entries cannot widen the environment: their env_passthrough and
// priority are ignored (spec §5.2).
func overlayServerConfig(sc *ServerConfig, e appcfg.LSPServerConfig, project bool) {
	if e.Command != "" {
		sc.Command = e.Command
	}
	if e.Args != nil {
		sc.Args = e.Args
	}
	if e.ExtensionToLanguage != nil {
		sc.ExtensionToLanguage = e.ExtensionToLanguage
	}
	if e.Filenames != nil {
		sc.Filenames = e.Filenames
	}
	if e.RootMarkers != nil {
		sc.RootMarkers = e.RootMarkers
	}
	if e.WorkspaceFolder != "" {
		sc.WorkspaceFolder = e.WorkspaceFolder
	}
	if e.Env != nil {
		sc.Env = e.Env
	}
	if !project && e.EnvPassthrough != nil {
		sc.EnvPassthrough = e.EnvPassthrough
	}
	if len(e.InitializationOptions) > 0 {
		sc.InitializationOptions = json.RawMessage(e.InitializationOptions)
	}
	if len(e.Settings) > 0 {
		sc.Settings = json.RawMessage(e.Settings)
	}
	if e.StartupTimeout != 0 {
		sc.StartupTimeout = time.Duration(e.StartupTimeout) * time.Second
	}
	if e.ShutdownTimeout != 0 {
		sc.ShutdownTimeout = time.Duration(e.ShutdownTimeout) * time.Second
	}
	if e.RestartOnCrash != nil {
		sc.RestartOnCrash = *e.RestartOnCrash
	}
	if e.MaxRestarts != 0 {
		sc.MaxRestarts = e.MaxRestarts
	}
	if e.Diagnostics != nil {
		sc.Diagnostics = *e.Diagnostics
	}
	if e.Role != "" {
		sc.Role = e.Role
	}
	if !project && e.Priority != nil {
		sc.Priority = *e.Priority
	}
	if e.Prewarm {
		sc.Prewarm = true
	}
}

// configFingerprint is the first 12 hex characters of the sha256 over the
// merged config, excluding every field that must not restart instances when
// it changes: enabled, priority, prewarm, scope, invalid (spec §5.3).
func configFingerprint(sc ServerConfig) string {
	sc.Enabled = false
	sc.Priority = 0
	sc.Prewarm = false
	sc.Scope = ""
	sc.Fingerprint = ""
	sc.Invalid = ""
	blob, err := json.Marshal(sc)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])[:12]
}

func sortedServerIDs(m map[string]appcfg.LSPServerConfig) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// MatchFile returns every server (enabled or not) whose extensions or
// filenames cover absPath, in ID order.
func MatchFile(servers []ServerConfig, absPath string) []ServerConfig {
	ext := strings.ToLower(filepath.Ext(absPath))
	base := filepath.Base(absPath)
	var out []ServerConfig
	for _, sc := range servers {
		if ext != "" {
			if _, ok := sc.ExtensionToLanguage[ext]; ok {
				out = append(out, sc)
				continue
			}
		}
		if _, ok := sc.Filenames[base]; ok {
			out = append(out, sc)
		}
	}
	return out
}

// ServersForFile picks, among enabled and valid matches, the primary server
// (spec §6.3) and every diagnostics-role server. primary is nil when none.
// Entries with RequireRootMarker only count when one of their root markers
// sits between the file and the project root.
func ServersForFile(servers []ServerConfig, absPath, projectRoot string) (primary *ServerConfig, diagnostics []ServerConfig) {
	matches := MatchFile(servers, absPath)
	var candidates []int
	for i := range matches {
		m := &matches[i]
		if !m.Enabled || m.Invalid != "" {
			continue
		}
		if m.RequireRootMarker && !hasRootMarker(absPath, projectRoot, *m) {
			continue
		}
		switch m.Role {
		case appcfg.LSPRolePrimary:
			candidates = append(candidates, i)
		case appcfg.LSPRoleDiagnostics:
			diagnostics = append(diagnostics, *m)
		}
	}
	if len(candidates) == 0 {
		return nil, diagnostics
	}
	dir := filepath.Dir(absPath)
	sort.Slice(candidates, func(x, y int) bool {
		return primaryLess(&matches[candidates[x]], &matches[candidates[y]], dir, projectRoot)
	})
	return &matches[candidates[0]], diagnostics
}

// primaryLess orders two primary candidates by the §6.3 tie-breaks: project
// scope, a satisfied conflicts rule, priority, then the id.
func primaryLess(a, b *ServerConfig, dir, projectRoot string) bool {
	if (a.Scope == "project") != (b.Scope == "project") {
		return a.Scope == "project"
	}
	aWins := conflictWins(a, b, dir, projectRoot)
	bWins := conflictWins(b, a, dir, projectRoot)
	if aWins != bWins {
		return aWins
	}
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	return a.ID < b.ID
}

// conflictWins says whether a's conflicts rules beat b here: a rule naming b
// whose marker exists anywhere from the file's directory up to the project
// root.
func conflictWins(a, b *ServerConfig, dir, projectRoot string) bool {
	for _, c := range a.Conflicts {
		if c.With != b.ID {
			continue
		}
		if markersExistBetween(dir, projectRoot, c.WhenRootMarker) {
			return true
		}
	}
	return false
}

// ResolveRoot finds the workspace root for absFile (spec §7.4). ok is false
// when absFile is outside projectRoot.
func ResolveRoot(absFile, projectRoot string, srv ServerConfig) (root string, ok bool) {
	file, err := filepath.EvalSymlinks(absFile)
	if err != nil {
		file = filepath.Clean(absFile)
	}
	root, err = filepath.EvalSymlinks(projectRoot)
	if err != nil {
		root = filepath.Clean(projectRoot)
	}
	if !pathWithin(file, root) {
		return "", false
	}
	if srv.WorkspaceFolder != "" {
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
	dir := filepath.Dir(file)
	candidate := ""
	for d := dir; ; d = filepath.Dir(d) {
		if dirContainsAnyMarker(d, srv.RootMarkers) {
			candidate = d
			break
		}
		if SamePath(d, root) {
			break
		}
	}
	if candidate == "" {
		return root, true
	}
	if srv.RootStrategy == "cargo-workspace" {
		topmost := ""
		for d := candidate; ; d = filepath.Dir(d) {
			if cargoWorkspaceAt(d) {
				topmost = d // keep walking: the topmost workspace wins
			}
			if SamePath(d, root) {
				break
			}
		}
		if topmost != "" {
			return topmost, true
		}
	}
	return candidate, true
}

// hasRootMarker reports whether any of srv's root markers sits at the file's
// directory or a parent up to the project root (both inclusive) — the gate
// require_root_marker entries must pass before they serve a file. Both paths
// are EvalSymlinks-normalized first (spec §7.4) so mixed path forms (for
// example /tmp vs /private/tmp) cannot push the walk past the project root.
func hasRootMarker(absFile, projectRoot string, srv ServerConfig) bool {
	file, err := filepath.EvalSymlinks(absFile)
	if err != nil {
		file = filepath.Clean(absFile)
	}
	root, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		root = filepath.Clean(projectRoot)
	}
	for d := filepath.Dir(file); pathWithin(d, root); d = filepath.Dir(d) {
		if dirContainsAnyMarker(d, srv.RootMarkers) {
			return true
		}
		if SamePath(d, root) {
			return false
		}
	}
	return false
}

// dirContainsAnyMarker reports whether dir holds any of markers, honoring
// the `*` wildcard through filepath.Glob.
func dirContainsAnyMarker(dir string, markers []string) bool {
	for _, marker := range markers {
		if marker == "" {
			continue
		}
		if strings.Contains(marker, "*") {
			matches, err := filepath.Glob(filepath.Join(dir, marker))
			if err == nil && len(matches) > 0 {
				return true
			}
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

// markersExistBetween reports whether any marker exists at dir itself or at
// one of its parents up to root (both inclusive). Both paths are
// EvalSymlinks-normalized first (spec §7.4) so mixed path forms cannot push
// the walk past the root.
func markersExistBetween(dir, root string, markers []string) bool {
	if len(markers) == 0 {
		return false
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		realDir = filepath.Clean(dir)
	}
	if dirContainsAnyMarker(realDir, markers) {
		return true
	}
	if root == "" {
		return false
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = filepath.Clean(root)
	}
	for d := filepath.Dir(realDir); pathWithin(d, realRoot); d = filepath.Dir(d) {
		if dirContainsAnyMarker(d, markers) {
			return true
		}
		if SamePath(d, realRoot) {
			return false
		}
	}
	return false
}

// cargoWorkspaceAt reports whether dir's Cargo.toml declares a `[workspace]`
// section on its own line.
func cargoWorkspaceAt(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "Cargo.toml"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "[workspace]" {
			return true
		}
	}
	return false
}

// pathWithin reports whether p is root or inside it, comparing with the
// SamePath rule (case-insensitive on darwin and windows).
func pathWithin(p, root string) bool {
	if root == "" {
		return false
	}
	p, root = filepath.Clean(p), filepath.Clean(root)
	if root == string(filepath.Separator) {
		return true
	}
	if SamePath(p, root) {
		return true
	}
	if len(p) <= len(root) {
		return false
	}
	return p[len(root)] == filepath.Separator && SamePath(p[:len(root)], root)
}

// StateDir is <agentWorkspace>/state/lsp.
func StateDir(agentWorkspace string) string {
	return filepath.Join(agentWorkspace, "state", "lsp")
}

type enabledStateFile struct {
	Enabled map[string]bool `json:"enabled"`
}

// LoadEnabled reads <StateDir>/enabled.json; a missing file is an empty map.
func LoadEnabled(agentWorkspace string) (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join(StateDir(agentWorkspace), "enabled.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	var state enabledStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.Enabled == nil {
		state.Enabled = map[string]bool{}
	}
	return state.Enabled, nil
}

// SaveEnabled writes one server's switch atomically (read-modify-write).
func SaveEnabled(agentWorkspace, id string, enabled bool) error {
	state, err := LoadEnabled(agentWorkspace)
	if err != nil {
		return err
	}
	state[id] = enabled
	data, err := json.Marshal(enabledStateFile{Enabled: state})
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(StateDir(agentWorkspace), "enabled.json"), data)
}

// writeFileAtomic replaces path via a temp file in the same directory and a
// rename (spec §5.4).
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

// rootHash8 is the first 8 hex characters of the sha256 of a root path.
func rootHash8(root string) string {
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:])[:8]
}

// CacheDir names the per-root cache directory (spec §5.4):
// <StateDir>/cache/<id>/<roothash8>.
func CacheDir(agentWorkspace, id, root string) string {
	return filepath.Join(StateDir(agentWorkspace), "cache", id, rootHash8(root))
}

// LogPath names the per-root instance log (spec §5.4):
// <StateDir>/logs/<id>-<roothash8>.log.
func LogPath(agentWorkspace, id, root string) string {
	return filepath.Join(StateDir(agentWorkspace), "logs", id+"-"+rootHash8(root)+".log")
}

// PIDFile names the orphan-cleanup registry (spec §5.4): <StateDir>/pids.json.
func PIDFile(agentWorkspace string) string {
	return filepath.Join(StateDir(agentWorkspace), "pids.json")
}

// ToolEnabled: features.lsp && Trusted && ProjectRoot != "" && at least one
// enabled, valid, primary server (spec §8.5). Consented project entries
// count: the answer is taken at the freeze point, when the decisions already
// recorded are the ones that apply.
func ToolEnabled(cfg *appcfg.Root, opts ManagerOptions) bool {
	if !cfg.EffectiveFeatures().LSP {
		return false
	}
	if !opts.Trusted || opts.ProjectRoot == "" {
		return false
	}
	enabled, err := LoadEnabled(opts.AgentWorkspace) // unreadable state file: treat as empty
	if err != nil {
		enabled = nil
	}
	project := ResolveProjectServers(opts.AgentWorkspace, opts.ProjectRoot, opts.ProjectKey, opts.Trusted)
	for _, sc := range ResolveServers(ResolveInput{Config: cfg, ProjectServers: project.Allowed, Enabled: enabled, GOOS: runtime.GOOS}) {
		if sc.Enabled && sc.Invalid == "" && sc.Role == appcfg.LSPRolePrimary {
			return true
		}
	}
	return false
}
