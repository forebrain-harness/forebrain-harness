// Project-level MCP server configuration: the one file a repository carries,
// the merge that combines it with the global list, and the stamps that let
// every downstream consumer tell the two scopes apart.
//
// The contract this file implements:
//
//   - Only <project>/.forebrain/mcp_servers.yaml is read. Forebrain Harness deliberately
//     does not read other agents' project files (.mcp.json,
//     .codex/config.toml): importing them is /migrate's job, and the import
//     copies their entries into this file. Reading them natively as well
//     would leave two sources of truth drifting apart in one repository.
//   - Project entries replace same-named higher-layer entries whole (no field
//     merge) and are stamped Scope/ProjectKey on a deep copy, so the shared
//     AppCfg lists are never mutated through them. The layers a session draws
//     from are the shared list, the primary agent's own list, and this file,
//     and precedence runs project > agent > shared.
//   - Project entries carry repository-controlled text. Approval modes are
//     clamped to at most "prompt" (default "prompt"), and the credential
//     store namespaces by scope elsewhere in this package using the stamps
//     applied here.
package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"go.yaml.in/yaml/v2"
)

// SameServerName reports whether two names identify the same MCP server:
// case-insensitive after trimming. Every reader of a configured server list
// must use this one comparison — a surface that matched names with == would
// miss a server a user typed in different case.
func SameServerName(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// IsProjectScope reports whether srv came from a project-level file.
func IsProjectScope(srv appcfg.MCPServerConfig) bool {
	return strings.TrimSpace(srv.Scope) == appcfg.MCPServerScopeProject
}

// IsAgentScope reports whether srv came from one primary agent's own list in
// forebrain.yaml (agents.definitions.<id>.mcp_servers).
func IsAgentScope(srv appcfg.MCPServerConfig) bool {
	return strings.TrimSpace(srv.Scope) == appcfg.MCPServerScopeAgent
}

// ProjectMCPPaths lists the project-level MCP files under projectRoot,
// highest priority first. It is exactly one file: forebrain's own location.
// Other agents' project files are migrated into it by /migrate, never read
// natively (see the package comment).
func ProjectMCPPaths(projectRoot string) []string {
	root := strings.TrimSpace(projectRoot)
	if root == "" {
		return nil
	}
	return []string{
		filepath.Join(root, ".forebrain", "mcp_servers.yaml"),
	}
}

// MCPFileNote records why a project file, or one entry in it, did not
// contribute to the effective list. Reason is one semantic sentence: callers
// surface it verbatim, so it must never echo the file's raw contents or a
// parser error payload.
type MCPFileNote struct {
	File   string
	Name   string // empty for a whole-file note
	Reason string
}

// LoadProjectMCPServers reads the project-level MCP files. It returns the
// parsed entries from all files, merged by precedence, sorted by name in
// ascending order, with duplicates and unparseable content dropped and noted.
//
// The caller owns gating (trust, consent); this function only reads and
// validates. Plaintext secrets are rejected here because a repository must
// not carry readable credentials regardless of who allowed the file.
func LoadProjectMCPServers(projectRoot string) ([]appcfg.MCPServerConfig, []MCPFileNote) {
	var notes []MCPFileNote
	byName := make(map[string]appcfg.MCPServerConfig)
	var order []string
	for _, path := range ProjectMCPPaths(projectRoot) {
		servers, fileNotes := parseProjectMCPFile(path)
		notes = append(notes, fileNotes...)
		for _, srv := range servers {
			name := strings.TrimSpace(srv.Name)
			if name == "" {
				continue
			}
			// Files are read highest priority first, and a name already
			// present came from a higher-priority file.
			if _, exists := byName[strings.ToLower(name)]; exists {
				continue
			}
			byName[strings.ToLower(name)] = srv
			order = append(order, name)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		return strings.ToLower(order[i]) < strings.ToLower(order[j])
	})
	out := make([]appcfg.MCPServerConfig, 0, len(order))
	for _, name := range order {
		out = append(out, byName[strings.ToLower(name)])
	}
	return out, notes
}

// parseProjectMCPFile reads the one project file. YAML and JSON both go
// through the YAML parser (every JSON document is YAML).
func parseProjectMCPFile(path string) ([]appcfg.MCPServerConfig, []MCPFileNote) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []MCPFileNote{{File: path, Reason: "file could not be read"}}
	}
	if err := appcfg.RejectPlaintextConfigSecrets(path, b); err != nil {
		return nil, []MCPFileNote{{File: path, Reason: "file contains a plaintext secret; use ${ENV_NAME} resolved from ~/.forebrain/.env"}}
	}
	var doc projectMCPDocument
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, []MCPFileNote{{File: path, Reason: "file could not be parsed as MCP server configuration"}}
	}
	return parseProjectMCPList(path, doc.Servers)
}

// projectMCPDocument captures the shape a project file takes: a top-level
// mcp_servers list whose entries match forebrain's own configuration.
type projectMCPDocument struct {
	Servers []appcfg.MCPServerConfig `yaml:"mcp_servers" json:"mcp_servers"`
}

// parseProjectMCPList normalizes and validates one file's entries, dropping
// the ones without a name.
func parseProjectMCPList(path string, servers []appcfg.MCPServerConfig) ([]appcfg.MCPServerConfig, []MCPFileNote) {
	appcfg.NormalizeMCPServers(servers)
	if err := appcfg.ValidateMCPServers(strings.TrimSpace(path), servers); err != nil {
		return nil, []MCPFileNote{{File: path, Reason: "entry has an invalid approval mode"}}
	}
	var out []appcfg.MCPServerConfig
	for _, srv := range servers {
		if strings.TrimSpace(srv.Name) == "" {
			continue
		}
		out = append(out, srv)
	}
	return out, nil
}

// LoadProjectMCPServersForWrite reads the one project file whole, for a
// read-modify-write.
//
// It is strict on purpose, and that is the whole difference between it and
// LoadProjectMCPServers. The reader is lenient because a broken entry in a
// repository's file must not hide the working ones; a writer that reused that
// leniency would take "the entries I could read" as the file's contents and
// delete everything it could not — a file with a plaintext secret, an invalid
// approval mode or an unnamed entry would lose those entries the moment
// somebody added a server from the web UI. Here such a file is an error, and
// the caller is expected to refuse the write and leave the file to the user's
// editor.
func LoadProjectMCPServersForWrite(projectRoot string) ([]appcfg.MCPServerConfig, error) {
	paths := ProjectMCPPaths(projectRoot)
	if len(paths) == 0 {
		return nil, fmt.Errorf("mcp: no project root to read a project MCP file from")
	}
	path := paths[0]
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := appcfg.RejectPlaintextConfigSecrets(path, b); err != nil {
		return nil, err
	}
	var doc projectMCPDocument
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: file could not be parsed as MCP server configuration", path)
	}
	appcfg.NormalizeMCPServers(doc.Servers)
	if err := appcfg.ValidateMCPServers(path, doc.Servers); err != nil {
		return nil, err
	}
	for _, srv := range doc.Servers {
		if strings.TrimSpace(srv.Name) == "" {
			return nil, fmt.Errorf("%s: an entry has no name; name it before editing this file from here", path)
		}
	}
	return doc.Servers, nil
}

// SaveProjectMCPServers writes the project-level MCP file: the whole list the
// caller means the project to have, normalized, validated and sorted by name,
// through a temporary file so a crash mid-write cannot leave a half-written
// file where a repository's servers should be.
//
// The plaintext-secret scan runs on the marshalled bytes, not on the caller's
// structs, so what is refused is exactly what would have been written: a
// project file may reference ${ENV_NAME}, never carry the value.
func SaveProjectMCPServers(projectRoot string, servers []appcfg.MCPServerConfig) error {
	paths := ProjectMCPPaths(projectRoot)
	if len(paths) == 0 {
		return fmt.Errorf("mcp: no project root to write a project MCP file into")
	}
	path := paths[0]
	appcfg.NormalizeMCPServers(servers)
	if err := appcfg.ValidateMCPServers(path, servers); err != nil {
		return err
	}
	sorted := append([]appcfg.MCPServerConfig(nil), servers...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return strings.ToLower(strings.TrimSpace(sorted[i].Name)) < strings.ToLower(strings.TrimSpace(sorted[j].Name))
	})
	b, err := yaml.Marshal(projectMCPDocument{Servers: sorted})
	if err != nil {
		return err
	}
	if err := appcfg.RejectPlaintextConfigSecrets(path, b); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// MCPMergeResult is the outcome of combining the three lists a session draws
// its MCP servers from.
type MCPMergeResult struct {
	// Servers is the effective list: shared entries (in their configured
	// order, minus replaced ones), then the agent's own entries under the same
	// rule, then project entries in ascending name order. Deterministic by
	// construction.
	Servers []appcfg.MCPServerConfig
	// OverriddenGlobal names every shared entry a higher layer replaced,
	// including every duplicate of the name in the shared list.
	OverriddenGlobal []string
	// OverriddenAgent names every entry of the agent's own list a project
	// entry replaced.
	OverriddenAgent []string
}

// MergeSessionServers combines the three MCP lists a session can draw from:
// the shared list every primary agent inherits, the list one primary agent
// owns, and the entries of the project being launched.
//
// Different names add up — that is the inheritance, not an either/or: an agent
// that configures a server of its own still sees every shared one, and a
// project adds its own entries on top of both. Same-named entries are replaced
// whole, the higher layer winning (project > agent > shared): a repository or
// an agent redefines a server, it does not inherit fields from the definition
// it shadows — inheriting would let a project file pair its own URL with a
// globally stored credential. There is no field-level mixing at either
// boundary.
//
// Every input is stamped on a deep copy. The returned list shares no maps or
// slices with the caller's, so later mutation of the effective list can never
// reach the config structs other readers still hold.
func MergeSessionServers(global, agent, project []appcfg.MCPServerConfig, agentKey, projectKey string) MCPMergeResult {
	agentKey = strings.TrimSpace(agentKey)
	projectKey = strings.TrimSpace(projectKey)
	replacedByAgent := serverNames(agent)
	replacedByProject := serverNames(project)
	out := make([]appcfg.MCPServerConfig, 0, len(global)+len(agent)+len(project))
	var overriddenGlobal, overriddenAgent []string
	for _, srv := range global {
		name := strings.TrimSpace(srv.Name)
		key := strings.ToLower(name)
		if _, hit := replacedByAgent[key]; hit {
			overriddenGlobal = append(overriddenGlobal, name)
			continue
		}
		if _, hit := replacedByProject[key]; hit {
			overriddenGlobal = append(overriddenGlobal, name)
			continue
		}
		stamped := DeepCopyMCPServer(srv)
		stamped.Scope = appcfg.MCPServerScopeGlobal
		stamped.AgentKey = ""
		stamped.ProjectKey = ""
		out = append(out, stamped)
	}
	for _, srv := range agent {
		name := strings.TrimSpace(srv.Name)
		if _, hit := replacedByProject[strings.ToLower(name)]; hit {
			overriddenAgent = append(overriddenAgent, name)
			continue
		}
		stamped := DeepCopyMCPServer(srv)
		stamped.Scope = appcfg.MCPServerScopeAgent
		stamped.AgentKey = agentKey
		stamped.ProjectKey = ""
		out = append(out, stamped)
	}
	for _, srv := range project {
		stamped := DeepCopyMCPServer(srv)
		stamped.Scope = appcfg.MCPServerScopeProject
		stamped.AgentKey = ""
		stamped.ProjectKey = projectKey
		ClampProjectRuntimePolicy(&stamped)
		out = append(out, stamped)
	}
	return MCPMergeResult{Servers: out, OverriddenGlobal: overriddenGlobal, OverriddenAgent: overriddenAgent}
}

// serverNames indexes a configured list by lower-cased name — the comparison
// SameServerName makes — so a shadowed entry is found however it was spelled.
func serverNames(servers []appcfg.MCPServerConfig) map[string]struct{} {
	out := make(map[string]struct{}, len(servers))
	for _, srv := range servers {
		out[strings.ToLower(strings.TrimSpace(srv.Name))] = struct{}{}
	}
	return out
}

// DeepCopyMCPServer returns a copy of srv that shares no reference-type field
// with the original. A marshal round-trip copies every tagged field and drops
// the runtime stamps, which the caller re-applies.
func DeepCopyMCPServer(srv appcfg.MCPServerConfig) appcfg.MCPServerConfig {
	b, err := json.Marshal(srv)
	if err != nil {
		// Every field is plain JSON-encodable; reaching here means the struct
		// gained a field nobody taught this path about, and a shallow copy is
		// still safer than a panic.
		return srv
	}
	var out appcfg.MCPServerConfig
	if err := json.Unmarshal(b, &out); err != nil {
		return srv
	}
	return out
}

// ClampProjectRuntimePolicy narrows what a project entry may decide for the
// session that runs it, so a repository cannot widen its own reach.
//
// Approval modes: the default (unset) mode becomes "prompt" rather than
// "auto", and no mode may be wider than "prompt". "auto" would mean a file that
// arrived with a checkout runs its tools with no operator in the loop, which
// only operator-controlled sources may do. Per-tool modes are clamped the same
// way as the server default.
//
// Startup policy: "required" is dropped and both timeouts are reset to the
// runtime defaults. All of them are patience the session spends on the entry,
// and none is covered by the consent the operator gave — consent answers "may
// this server run", not "may it hold the first turn for an hour, refuse to be
// skipped, or keep a tool call open forever". A repository that wants a longer
// wait or a hard requirement asks for it in the operator's own configuration,
// where the operator can see it.
func ClampProjectRuntimePolicy(srv *appcfg.MCPServerConfig) {
	if srv == nil {
		return
	}
	srv.DefaultToolsApprovalMode = clampProjectApprovalMode(srv.DefaultToolsApprovalMode)
	for name, tool := range srv.Tools {
		tool.ApprovalMode = clampProjectApprovalMode(tool.ApprovalMode)
		srv.Tools[name] = tool
	}
	srv.Required = false
	if timeout, err := ResolveStartupTimeout(srv.StartupTimeout); err != nil || timeout > DefaultStartupTimeout {
		srv.StartupTimeout = 0
	}
	if timeout, err := ResolveRequestTimeout(srv.RequestTimeout); err != nil || timeout > DefaultRequestTimeout {
		srv.RequestTimeout = 0
	}
}

func clampProjectApprovalMode(mode appcfg.MCPToolApprovalMode) appcfg.MCPToolApprovalMode {
	normalized := mode.Normalized()
	if normalized == appcfg.MCPToolApprovalAuto {
		return appcfg.MCPToolApprovalPrompt
	}
	return normalized
}

// MCPNotApplied is one project entry that is configured but not effective.
type MCPNotApplied struct {
	Name   string
	Reason string
}

// ProjectMCPScopeSummary is the project-level view a surface renders next to
// the effective list: which global entries a project replaced, and which
// project entries exist but are not in the running list and why.
type ProjectMCPScopeSummary struct {
	// ProjectRoot is the root the summary was computed for; empty when no
	// project applies.
	ProjectRoot string
	// OverriddenGlobal names the global entries the running list replaced.
	OverriddenGlobal []string
	// NotApplied lists project entries that exist on disk but are not in the
	// running list, with a one-line reason each.
	NotApplied []MCPNotApplied
	// PendingReload is true when the on-disk project files differ from what
	// the running list was built from — the change takes effect in a new
	// session.
	PendingReload bool
}

// SameProjectEntry reports whether the on-disk project list matches the
// project entries of the running list, name for name. It backs the
// "change lands in the next session" note: the comparison is by name only, so
// an edited command shows as pending, which is the honest report — the
// session's tool list is frozen and the edit has not taken effect.
func SameProjectEntry(a, b []appcfg.MCPServerConfig) bool {
	projectA := filterProjectEntries(a)
	projectB := filterProjectEntries(b)
	if len(projectA) != len(projectB) {
		return false
	}
	for i := range projectA {
		if !SameServerName(projectA[i].Name, projectB[i].Name) {
			return false
		}
	}
	return true
}

func filterProjectEntries(servers []appcfg.MCPServerConfig) []appcfg.MCPServerConfig {
	var out []appcfg.MCPServerConfig
	for _, srv := range servers {
		if IsProjectScope(srv) {
			out = append(out, srv)
		}
	}
	return out
}

// --- per-agent disable store (state/mcp/disabled.json) ---

// disabledStore is the per-primary-agent MCP disable store. It mirrors the
// skill disable store: it lives under the agent's workspace root, it is only
// read when a new session assembles its server list, and it never rewrites
// the user's YAML — rewriting would drop comments and could rotate a project
// consent fingerprint, prompting a re-consent for a server the user only
// wanted paused.
type disabledStore struct {
	// Disabled holds one key per disabled server (see disabledKey).
	Disabled map[string]bool `json:"disabled"`
}

func disabledStorePath(workspaceRoot string) string {
	return filepath.Join(strings.TrimSpace(workspaceRoot), "state", "mcp", "disabled.json")
}

// loadDisabledStore reads the store; a missing file is an empty store. A file
// that exists but does not parse is reported rather than read as empty, so a
// toggle never overwrites choices it could not read.
func loadDisabledStore(workspaceRoot string) (disabledStore, error) {
	st := disabledStore{Disabled: map[string]bool{}}
	raw, err := os.ReadFile(disabledStorePath(workspaceRoot))
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return st, fmt.Errorf("read %s: %w", disabledStorePath(workspaceRoot), err)
	}
	if st.Disabled == nil {
		st.Disabled = map[string]bool{}
	}
	return st, nil
}

func saveDisabledStore(workspaceRoot string, st disabledStore) error {
	p := disabledStorePath(workspaceRoot)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(raw, '\n'), 0o644)
}

// disabledKey identifies one configured server: its scope and name, plus the
// project it belongs to for a project entry. A project's server, two primary
// agents' same-named servers, and a same-named shared one are toggled
// independently. Shared entries keep the historical "global" spelling, so a
// store written before the other scopes existed still means what it said.
func disabledKey(srv appcfg.MCPServerConfig) string {
	name := strings.ToLower(strings.TrimSpace(srv.Name))
	switch {
	case IsProjectScope(srv):
		return appcfg.MCPServerScopeProject + "\x00" + name + "\x00" + strings.TrimSpace(srv.ProjectKey)
	case IsAgentScope(srv):
		return appcfg.MCPServerScopeAgent + "\x00" + name
	default:
		return "global\x00" + name
	}
}

// SetServerDisabled marks srv disabled from the next session, or clears the
// mark. A server the configuration requires cannot be disabled: its absence
// would fail every unattended run that depends on it. A server the
// configuration switched off cannot be enabled here either — the file is the
// authority on that, and a local mark that silently contradicted it would
// leave the user with a server that never starts and no hint where to look.
func SetServerDisabled(workspaceRoot string, srv appcfg.MCPServerConfig, disabled bool) error {
	if strings.TrimSpace(workspaceRoot) == "" {
		return fmt.Errorf("mcp: no agent workspace to record the choice in")
	}
	if disabled && srv.Required {
		return fmt.Errorf("mcp: %s is required by config and cannot be disabled", strings.TrimSpace(srv.Name))
	}
	if !disabled && !appcfg.ServerEnabled(srv) {
		return fmt.Errorf("mcp: %s is disabled in the configuration (enabled: false); set enabled: true to run it", strings.TrimSpace(srv.Name))
	}
	st, err := loadDisabledStore(workspaceRoot)
	if err != nil {
		return err
	}
	key := disabledKey(srv)
	if disabled {
		st.Disabled[key] = true
	} else {
		delete(st.Disabled, key)
	}
	return saveDisabledStore(workspaceRoot, st)
}

// DisabledForNextSession reads the store once and reports, per server, whether
// a new session would leave it out. A running session uses it to label what
// will change in the next one.
func DisabledForNextSession(workspaceRoot string) (func(appcfg.MCPServerConfig) bool, error) {
	if strings.TrimSpace(workspaceRoot) == "" {
		return func(appcfg.MCPServerConfig) bool { return false }, nil
	}
	st, err := loadDisabledStore(workspaceRoot)
	if err != nil {
		return func(appcfg.MCPServerConfig) bool { return false }, err
	}
	return func(srv appcfg.MCPServerConfig) bool {
		return !srv.Required && st.Disabled[disabledKey(srv)]
	}, nil
}

// FilterDisabledServers splits a new session's effective list: servers the
// configuration switched off (enabled: false) and servers the store marks
// disabled move to the returned remainder, which the session keeps so the
// choice stays visible — and reversible — from /mcp. A server the
// configuration requires is never filtered by the store; one that is switched
// off cannot also be required, which the loader rejects.
//
// An unreadable store does not stop the configuration's own switch from
// applying: the file was read, it says the server is off, and starting it
// because a different file was corrupt is the one outcome the user did not ask
// for.
func FilterDisabledServers(workspaceRoot string, servers []appcfg.MCPServerConfig) (effective, disabled []appcfg.MCPServerConfig, err error) {
	offByConfig, rest := splitByConfigSwitch(servers)
	if strings.TrimSpace(workspaceRoot) == "" {
		return rest, offByConfig, nil
	}
	st, err := loadDisabledStore(workspaceRoot)
	if err != nil {
		return rest, offByConfig, err
	}
	if len(st.Disabled) == 0 {
		return rest, offByConfig, nil
	}
	disabled = offByConfig
	for _, srv := range rest {
		if !srv.Required && st.Disabled[disabledKey(srv)] {
			disabled = append(disabled, srv)
			continue
		}
		effective = append(effective, srv)
	}
	return effective, disabled, nil
}

// splitByConfigSwitch separates the entries switched off in the configuration
// from the rest, keeping each side in the order it was given.
func splitByConfigSwitch(servers []appcfg.MCPServerConfig) (off, on []appcfg.MCPServerConfig) {
	for _, srv := range servers {
		if !appcfg.ServerEnabled(srv) {
			off = append(off, srv)
			continue
		}
		on = append(on, srv)
	}
	return off, on
}
