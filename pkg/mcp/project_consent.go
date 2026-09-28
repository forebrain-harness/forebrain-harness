// Per-entry consent for project-level MCP servers.
//
// A project file arrives with a checkout, so before its servers may start the
// operator confirms each entry once. The confirmation is keyed by a
// fingerprint of everything the entry would execute or connect to: change the
// command or the URL and the entry asks again. Decisions are stored per
// primary agent (under the agent workspace) and per project (by project key),
// so confirming a server in one repository says nothing about any other.
package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

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

// ProjectConsents maps project key -> lowercased server name -> record.
type ProjectConsents map[string]map[string]ProjectConsentRecord

// ProjectConsentPath is where one agent workspace stores its project MCP
// consent decisions.
func ProjectConsentPath(agentWorkspace string) string {
	return filepath.Join(strings.TrimSpace(agentWorkspace), "state", "mcp", "project_consent.json")
}

// LoadProjectConsents reads the store under agentWorkspace. A missing file is
// an empty store, not an error.
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
// there is no record for this exact project, name, and fingerprint — an
// entry whose command or URL changed must be confirmed again, and a stale
// record for an older fingerprint does not count.
func (c ProjectConsents) Decision(projectKey, serverName, fingerprint string) (decision string, decided bool) {
	rec, ok := c[strings.TrimSpace(projectKey)][strings.ToLower(strings.TrimSpace(serverName))]
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
func (c ProjectConsents) Decide(projectKey, serverName, fingerprint, decision string, at time.Time) {
	projectKey = strings.TrimSpace(projectKey)
	name := strings.ToLower(strings.TrimSpace(serverName))
	if projectKey == "" || name == "" {
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
	inner[name] = ProjectConsentRecord{Fingerprint: fingerprint, Decision: decision, DecidedAt: at.Unix()}
}

// ServerFingerprint is the stable identity of what a server entry would
// execute or connect to: transport, command, args, env, url, headers, oauth
// settings, and approval modes. encoding/json sorts map keys, so the marshal
// output — and therefore the hash — is a pure function of the entry's
// contents. Runtime stamps (Scope, ProjectKey) are deliberately excluded:
// the same command confirmed once must not re-ask because it was loaded under
// a different spelling of the same project.
func ServerFingerprint(srv appcfg.MCPServerConfig) string {
	canonical := struct {
		Transport             string                                `json:"transport"`
		URL                   string                                `json:"url"`
		Command               string                                `json:"command"`
		Args                  []string                              `json:"args"`
		Env                   map[string]string                     `json:"env"`
		Headers               map[string]string                     `json:"headers"`
		InheritParentEnv      *bool                                 `json:"inherit_parent_env"`
		ExpectedCommandSHA256 string                                `json:"expected_command_sha256"`
		DisableStandaloneSSE  *bool                                 `json:"disable_standalone_sse"`
		OAuth                 appcfg.MCPOAuthConfig                 `json:"oauth"`
		DefaultApproval       appcfg.MCPToolApprovalMode            `json:"default_approval"`
		Tools                 map[string]appcfg.MCPServerToolConfig `json:"tools"`
	}{
		Transport:             strings.ToLower(strings.TrimSpace(srv.Transport)),
		URL:                   strings.TrimSpace(srv.URL),
		Command:               strings.TrimSpace(srv.Command),
		Args:                  append([]string(nil), srv.Args...),
		Env:                   srv.Env,
		Headers:               srv.Headers,
		InheritParentEnv:      srv.InheritParentEnv,
		ExpectedCommandSHA256: strings.TrimSpace(srv.ExpectedCommandSHA256),
		DisableStandaloneSSE:  srv.DisableStandaloneSSE,
		OAuth:                 srv.OAuth,
		DefaultApproval:       srv.DefaultToolsApprovalMode,
		Tools:                 srv.Tools,
	}
	b, err := json.Marshal(canonical)
	if err != nil {
		// Same reachability argument as DeepCopyMCPServer: plain fields only.
		b = []byte(srv.Name)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// PendingProjectConsent is one project entry presented for confirmation.
type PendingProjectConsent struct {
	Name        string `json:"name"`
	Fingerprint string `json:"-"`
	// Summary describes what the entry would run or contact, in one line the
	// operator can act on without seeing the raw file.
	Summary string `json:"summary"`
}

// ConsentSummary renders what an entry would execute or connect to.
func ConsentSummary(srv appcfg.MCPServerConfig) string {
	name := strings.TrimSpace(srv.Name)
	if cmd := strings.TrimSpace(srv.Command); cmd != "" {
		if len(srv.Args) > 0 {
			return name + ": runs `" + cmd + " " + strings.Join(srv.Args, " ") + "`"
		}
		return name + ": runs `" + cmd + "`"
	}
	if u := strings.TrimSpace(srv.URL); u != "" {
		return name + ": connects to " + u
	}
	return name + ": no command or url configured"
}
