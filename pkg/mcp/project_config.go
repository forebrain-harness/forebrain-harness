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
//   - Project entries replace same-named global entries whole (no field
//     merge) and are stamped Scope/ProjectKey on a deep copy, so the shared
//     AppCfg list is never mutated through them.
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

// MCPMergeResult is the outcome of combining the global list with project
// entries.
type MCPMergeResult struct {
	// Servers is the effective list: globals (in their configured order,
	// minus replaced ones) followed by project entries in ascending name
	// order. Deterministic by construction.
	Servers []appcfg.MCPServerConfig
	// OverriddenGlobal names every global entry a project entry replaced,
	// including every duplicate of the name in the global list.
	OverriddenGlobal []string
}

// MergeSessionServers combines the global MCP list with project entries.
//
// Same-named entries (SameServerName) are replaced whole by the project entry:
// a repository redefines a server, it does not inherit fields from the global
// definition it shadows — inheriting would let a project file pair its own
// URL with a globally stored credential.
//
// Both inputs are stamped on deep copies. The returned list shares no maps or
// slices with the caller's, so later mutation of the effective list can never
// reach the config structs other readers still hold.
func MergeSessionServers(global, project []appcfg.MCPServerConfig, projectKey string) MCPMergeResult {
	projectKey = strings.TrimSpace(projectKey)
	replaced := make(map[string]struct{}, len(project))
	for _, srv := range project {
		replaced[strings.ToLower(strings.TrimSpace(srv.Name))] = struct{}{}
	}
	out := make([]appcfg.MCPServerConfig, 0, len(global)+len(project))
	var overridden []string
	for _, srv := range global {
		if _, hit := replaced[strings.ToLower(strings.TrimSpace(srv.Name))]; hit {
			overridden = append(overridden, strings.TrimSpace(srv.Name))
			continue
		}
		stamped := DeepCopyMCPServer(srv)
		stamped.Scope = appcfg.MCPServerScopeGlobal
		stamped.ProjectKey = ""
		out = append(out, stamped)
	}
	for _, srv := range project {
		stamped := DeepCopyMCPServer(srv)
		stamped.Scope = appcfg.MCPServerScopeProject
		stamped.ProjectKey = projectKey
		ClampProjectRuntimePolicy(&stamped)
		out = append(out, stamped)
	}
	return MCPMergeResult{Servers: out, OverriddenGlobal: overridden}
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
// Startup policy: "required" is dropped and the startup timeout is capped at
// the runtime default. Both are patience the session spends on the entry, and
// neither is covered by the consent the operator gave — consent answers "may
// this server run", not "may it hold the first turn for an hour and refuse to
// be skipped". A repository that wants a longer wait or a hard requirement asks
// for it in the operator's own configuration, where the operator can see it.
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
// project it belongs to for a project entry. A project's server and a
// same-named global one — or two projects' same-named servers — are toggled
// independently.
func disabledKey(srv appcfg.MCPServerConfig) string {
	name := strings.ToLower(strings.TrimSpace(srv.Name))
	if IsProjectScope(srv) {
		return appcfg.MCPServerScopeProject + "\x00" + name + "\x00" + strings.TrimSpace(srv.ProjectKey)
	}
	return "global\x00" + name
}

// SetServerDisabled marks srv disabled from the next session, or clears the
// mark. A server the configuration requires cannot be disabled: its absence
// would fail every unattended run that depends on it.
func SetServerDisabled(workspaceRoot string, srv appcfg.MCPServerConfig, disabled bool) error {
	if strings.TrimSpace(workspaceRoot) == "" {
		return fmt.Errorf("mcp: no agent workspace to record the choice in")
	}
	if disabled && srv.Required {
		return fmt.Errorf("mcp: %s is required by config and cannot be disabled", strings.TrimSpace(srv.Name))
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
// store marks disabled move to the returned remainder, which the session keeps
// so the choice stays visible — and reversible — from /mcp. A server the
// configuration requires is never filtered.
func FilterDisabledServers(workspaceRoot string, servers []appcfg.MCPServerConfig) (effective, disabled []appcfg.MCPServerConfig, err error) {
	if strings.TrimSpace(workspaceRoot) == "" {
		return servers, nil, nil
	}
	st, err := loadDisabledStore(workspaceRoot)
	if err != nil {
		return servers, nil, err
	}
	if len(st.Disabled) == 0 {
		return servers, nil, nil
	}
	for _, srv := range servers {
		if !srv.Required && st.Disabled[disabledKey(srv)] {
			disabled = append(disabled, srv)
			continue
		}
		effective = append(effective, srv)
	}
	return effective, disabled, nil
}
