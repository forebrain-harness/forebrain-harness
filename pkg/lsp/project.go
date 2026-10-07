// Project-level language-server entries: <project>/.forebrain/lsp_servers.yaml,
// honored only in trusted, version-controlled projects and only entry by
// entry after the operator confirmed each one (spec §5.2).
//
// The file arrives with a checkout, so reading it is not enough to run what
// it says: every entry carries a fingerprint over the fields that decide what
// executes, and a decision is recorded per fingerprint, per project, per
// primary agent. An entry whose fingerprint has no recorded decision — or a
// recorded denial — contributes nothing, not even its override of a built-in
// server. The store mirrors the project-level MCP one field for field but
// lives in state/lsp/project_consent.json: the two consent histories stay
// separate because they answer different questions.
package lsp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"go.yaml.in/yaml/v2"
)

// ProjectLSPPath is <projectRoot>/.forebrain/lsp_servers.yaml.
func ProjectLSPPath(projectRoot string) string {
	return filepath.Join(strings.TrimSpace(projectRoot), ".forebrain", "lsp_servers.yaml")
}

// projectLSPDocument captures the shape a project file takes: a top-level
// servers mapping whose entries match the global lsp.servers ones.
type projectLSPDocument struct {
	Servers map[string]appcfg.LSPServerConfig `yaml:"servers" json:"servers"`
}

// LoadProjectServers parses the project file. A missing file is no entries and
// no notes; a file that cannot be used is no entries and one note saying why.
// The caller owns the trust gate; this function only reads and validates —
// except plaintext secrets, which a repository must not carry regardless of
// who allowed the file.
func LoadProjectServers(projectRoot string) (map[string]appcfg.LSPServerConfig, []string) {
	root := strings.TrimSpace(projectRoot)
	if root == "" {
		return nil, nil
	}
	path := ProjectLSPPath(root)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []string{"file could not be read"}
	}
	if err := appcfg.RejectPlaintextConfigSecrets(path, b); err != nil {
		return nil, []string{"lsp_servers.yaml contains a plaintext secret; use ${ENV_NAME} resolved from ~/.forebrain/.env"}
	}
	var top map[string]any
	if err := yaml.Unmarshal(b, &top); err != nil {
		return nil, []string{"file could not be parsed as language server configuration"}
	}
	var notes []string
	for _, k := range sortedTopLevelKeys(top) {
		if k != "servers" {
			notes = append(notes, `lsp_servers.yaml: unknown key "`+k+`"`)
		}
	}
	var doc projectLSPDocument
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, []string{"file could not be parsed as language server configuration"}
	}
	m := normalizeProjectServers(doc.Servers)
	if err := appcfg.ValidateLSPServers("lsp_servers.yaml servers", m); err != nil {
		return nil, []string{err.Error()}
	}
	for _, id := range sortedServerIDs(m) {
		if len(m[id].EnvPassthrough) == 0 && m[id].Priority == nil {
			continue
		}
		notes = append(notes, id+": env_passthrough and priority are ignored in project files")
		entry := m[id]
		entry.EnvPassthrough = nil
		entry.Priority = nil
		m[id] = entry
	}
	return m, notes
}

// sortedTopLevelKeys lists a document's top-level keys in ascending order, so
// a file with several unknown keys reports them deterministically.
func sortedTopLevelKeys(top map[string]any) []string {
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// normalizeProjectServers applies the same hand-written-identifier rules as
// the global lsp section: server ids and extension keys are lowercased and
// trimmed, roles are lowercased, free-form text is only trimmed.
func normalizeProjectServers(servers map[string]appcfg.LSPServerConfig) map[string]appcfg.LSPServerConfig {
	if servers == nil {
		return nil
	}
	normalized := make(map[string]appcfg.LSPServerConfig, len(servers))
	for id, server := range servers {
		server.Command = strings.TrimSpace(server.Command)
		server.WorkspaceFolder = strings.TrimSpace(server.WorkspaceFolder)
		server.Role = strings.ToLower(strings.TrimSpace(server.Role))
		if len(server.ExtensionToLanguage) > 0 {
			extensions := make(map[string]string, len(server.ExtensionToLanguage))
			for ext, language := range server.ExtensionToLanguage {
				extensions[strings.ToLower(strings.TrimSpace(ext))] = strings.TrimSpace(language)
			}
			server.ExtensionToLanguage = extensions
		}
		normalized[strings.ToLower(strings.TrimSpace(id))] = server
	}
	return normalized
}

// ProjectServerFingerprint is the identity the consent is keyed by: sha256 of
// the canonical JSON of command, args, env, initialization_options, settings,
// workspace_folder, root_markers and enabled (spec §5.2), hex. encoding/json
// sorts map keys and compactJSONObject canonicalizes the embedded JSON
// documents, so two spellings of one configuration hash the same. Fields a
// project entry may not set (env_passthrough, priority) are not part of the
// identity: they were cleared at load and must not rotate a recorded decision
// either.
func ProjectServerFingerprint(srv appcfg.LSPServerConfig) string {
	canonical := struct {
		Command               string            `json:"command"`
		Args                  []string          `json:"args"`
		Env                   map[string]string `json:"env"`
		InitializationOptions json.RawMessage   `json:"initialization_options"`
		Settings              json.RawMessage   `json:"settings"`
		WorkspaceFolder       string            `json:"workspace_folder"`
		RootMarkers           []string          `json:"root_markers"`
		Enabled               *bool             `json:"enabled"`
	}{
		Command:               strings.TrimSpace(srv.Command),
		Args:                  append([]string(nil), srv.Args...),
		Env:                   srv.Env,
		InitializationOptions: compactJSONObject(srv.InitializationOptions),
		Settings:              compactJSONObject(srv.Settings),
		WorkspaceFolder:       strings.TrimSpace(srv.WorkspaceFolder),
		RootMarkers:           append([]string(nil), srv.RootMarkers...),
		Enabled:               srv.Enabled,
	}
	b, err := json.Marshal(canonical)
	if err != nil {
		// Same reachability argument as the MCP fingerprint: plain fields
		// and raw JSON only.
		b = []byte(srv.Command)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// compactJSONObject normalizes one JSON document for hashing: whitespace and
// key order must not change the fingerprint, and an absent value is null.
// Re-marshaling through an any sorts every object's keys; UseNumber keeps the
// number literals as written.
func compactJSONObject(raw appcfg.LSPJSONObject) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return json.RawMessage("null")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		var buf bytes.Buffer
		if json.Compact(&buf, trimmed) == nil {
			return buf.Bytes()
		}
		return json.RawMessage(trimmed)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(trimmed)
	}
	return out
}

// Project consent decisions.
const (
	ProjectConsentAllow = "allow"
	ProjectConsentDeny  = "deny"
)

// ProjectConsentRecord is one stored decision.
type ProjectConsentRecord struct {
	Fingerprint string `json:"fingerprint"`
	Decision    string `json:"decision"`
	DecidedAt   int64  `json:"decided_at"`
}

// ProjectConsents maps project key -> lowercased server id -> record.
type ProjectConsents map[string]map[string]ProjectConsentRecord

// ProjectConsentPath is where one agent workspace stores its project
// language-server consent decisions: <StateDir>/project_consent.json.
func ProjectConsentPath(agentWorkspace string) string {
	return filepath.Join(StateDir(agentWorkspace), "project_consent.json")
}

// LoadProjectConsents reads the store under agentWorkspace. A missing file is
// an empty store, not an error; a file that is not valid JSON is empty too —
// losing the history re-asks rather than failing closed forever.
func LoadProjectConsents(agentWorkspace string) (ProjectConsents, error) {
	b, err := os.ReadFile(ProjectConsentPath(agentWorkspace))
	if err != nil {
		if os.IsNotExist(err) {
			return ProjectConsents{}, nil
		}
		return nil, err
	}
	var c ProjectConsents
	if err := json.Unmarshal(b, &c); err != nil {
		return ProjectConsents{}, nil
	}
	return c, nil
}

// SaveProjectConsents writes the store atomically: a crash mid-write must not
// truncate a file that records which commands the operator already vouched
// for.
func SaveProjectConsents(agentWorkspace string, c ProjectConsents) error {
	p := ProjectConsentPath(agentWorkspace)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Decision looks up the stored decision for one server. decided is false when
// there is no record for this exact project, id, and fingerprint — an entry
// whose command or settings changed must be confirmed again, and a stale
// record for an older fingerprint does not count.
func (c ProjectConsents) Decision(projectKey, serverID, fingerprint string) (decision string, decided bool) {
	rec, ok := c[strings.TrimSpace(projectKey)][strings.ToLower(strings.TrimSpace(serverID))]
	if !ok {
		return "", false
	}
	if strings.TrimSpace(rec.Fingerprint) != strings.TrimSpace(fingerprint) {
		return "", false
	}
	switch rec.Decision {
	case ProjectConsentAllow, ProjectConsentDeny:
		return rec.Decision, true
	default:
		return "", false
	}
}

// Decide records a decision for one server.
func (c ProjectConsents) Decide(projectKey, serverID, fingerprint, decision string, at time.Time) {
	projectKey = strings.TrimSpace(projectKey)
	id := strings.ToLower(strings.TrimSpace(serverID))
	if projectKey == "" || id == "" {
		return
	}
	if decision != ProjectConsentAllow && decision != ProjectConsentDeny {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	inner := c[projectKey]
	if inner == nil {
		inner = make(map[string]ProjectConsentRecord)
		c[projectKey] = inner
	}
	inner[id] = ProjectConsentRecord{Fingerprint: fingerprint, Decision: decision, DecidedAt: at.Unix()}
}

// PendingProjectServer is one project entry awaiting confirmation.
type PendingProjectServer struct {
	ID          string `json:"id"`
	Fingerprint string `json:"-"`
	// Summary describes what the entry would run or change, in one line the
	// operator can act on without seeing the raw file.
	Summary string `json:"summary"`
}

// ProjectServerState is the project file as it applies to one agent.
type ProjectServerState struct {
	// Allowed is what ResolveInput.ProjectServers receives: the entries with
	// a recorded allow for this exact fingerprint.
	Allowed map[string]appcfg.LSPServerConfig
	Pending []PendingProjectServer
	Denied  []string
	Notes   []string // why the file or an entry was ignored
}

// ResolveProjectServers reads the file and the consent store. trusted false
// (not trusted or not version controlled) yields an empty state: the file is
// not even parsed. Each entry then lands in exactly one of Allowed, Pending
// and Denied by its recorded decision, in ascending id order.
func ResolveProjectServers(agentWorkspace, projectRoot, projectKey string, trusted bool) ProjectServerState {
	if !trusted || strings.TrimSpace(projectRoot) == "" {
		return ProjectServerState{}
	}
	entries, notes := LoadProjectServers(projectRoot)
	consents, err := LoadProjectConsents(agentWorkspace)
	if err != nil {
		// An unreadable consent store means no entry can be shown a valid
		// decision: fail closed rather than treat recorded confirmations as
		// absent for some entries and present for others.
		consents = nil
	}
	state := ProjectServerState{Allowed: map[string]appcfg.LSPServerConfig{}, Notes: notes}
	for _, id := range sortedServerIDs(entries) {
		fp := ProjectServerFingerprint(entries[id])
		decision, decided := consents.Decision(projectKey, id, fp)
		switch {
		case decided && decision == ProjectConsentAllow:
			state.Allowed[id] = entries[id]
		case decided && decision == ProjectConsentDeny:
			state.Denied = append(state.Denied, id)
		default:
			state.Pending = append(state.Pending, PendingProjectServer{
				ID:          id,
				Fingerprint: fp,
				Summary:     projectServerSummary(id, entries[id]),
			})
		}
	}
	return state
}

// projectServerSummary renders what one entry would do. A command is shown as
// the command line it runs; a commandless entry that only adjusts a built-in
// server says so; requesting enabled: true is appended, because enabling a
// server is a decision of its own.
func projectServerSummary(id string, srv appcfg.LSPServerConfig) string {
	var base string
	if cmd := strings.TrimSpace(srv.Command); cmd != "" {
		if len(srv.Args) > 0 {
			base = id + ": runs `" + cmd + " " + strings.Join(srv.Args, " ") + "`"
		} else {
			base = id + ": runs `" + cmd + "`"
		}
	} else if inCatalog(id) {
		base = id + ": changes settings of the built-in " + id + " server"
	} else {
		base = id + ": no command configured"
	}
	if srv.Enabled != nil && *srv.Enabled {
		base += " and enables it"
	}
	return base
}

// inCatalog reports whether id is one of the built-in servers; the summary's
// "changes settings" form is only true for those.
func inCatalog(id string) bool {
	entries, _ := Catalog()
	for _, e := range entries {
		if e.ID == id {
			return true
		}
	}
	return false
}

// DecideProjectServers records allow for the ids in allowed and deny for
// every other pending entry, so a declined entry is not asked again until its
// fingerprint changes.
func DecideProjectServers(agentWorkspace, projectRoot, projectKey string, allowed []string) error {
	if strings.TrimSpace(projectRoot) == "" {
		return nil
	}
	entries, _ := LoadProjectServers(projectRoot)
	consents, err := LoadProjectConsents(agentWorkspace)
	if err != nil {
		consents = ProjectConsents{}
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, id := range allowed {
		allowedSet[strings.ToLower(strings.TrimSpace(id))] = struct{}{}
	}
	for _, id := range sortedServerIDs(entries) {
		fp := ProjectServerFingerprint(entries[id])
		if _, decided := consents.Decision(projectKey, id, fp); decided {
			continue
		}
		decision := ProjectConsentDeny
		if _, ok := allowedSet[strings.ToLower(strings.TrimSpace(id))]; ok {
			decision = ProjectConsentAllow
		}
		consents.Decide(projectKey, id, fp, decision, time.Now())
	}
	return SaveProjectConsents(agentWorkspace, consents)
}
