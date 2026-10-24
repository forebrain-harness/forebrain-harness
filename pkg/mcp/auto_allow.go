// "Always allow this project's servers": one switch per primary agent and
// project that lets every entry the project's server files declare — MCP
// servers and language servers alike — run without being confirmed one by
// one. An entry the operator declined explicitly stays out regardless.
//
// It lives here because the composition root reads it for both kinds of
// entry and pkg/lsp may not import pkg/mcp; pkg/lsp is handed the answer.
package mcp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// errNoAgentWorkspace is what the write path returns for a missing agent
// workspace: a path relative to the process directory would be a different
// store for every launch directory, so there is nothing sane to write.
var errNoAgentWorkspace = errors.New("project auto-allow: no agent workspace")

// ProjectAutoAllowRecord is one project's switch; its presence means on.
type ProjectAutoAllowRecord struct {
	Since int64 `json:"since"`
}

// ProjectAutoAllow maps project key -> record.
type ProjectAutoAllow map[string]ProjectAutoAllowRecord

// ProjectAutoAllowPath is <agentWorkspace>/state/project_auto_allow.json, or ""
// when there is no workspace (a path relative to the process directory would
// be a different store for every launch directory).
func ProjectAutoAllowPath(agentWorkspace string) string {
	if strings.TrimSpace(agentWorkspace) == "" {
		return ""
	}
	return filepath.Join(strings.TrimSpace(agentWorkspace), "state", "project_auto_allow.json")
}

// LoadProjectAutoAllow reads the store under agentWorkspace. No workspace or
// a missing file is an empty store, not an error; a read error is returned;
// unparsable JSON reads as empty — the switch fails closed.
func LoadProjectAutoAllow(agentWorkspace string) (ProjectAutoAllow, error) {
	p := ProjectAutoAllowPath(agentWorkspace)
	if p == "" {
		return ProjectAutoAllow{}, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return ProjectAutoAllow{}, nil
		}
		return nil, err
	}
	var a ProjectAutoAllow
	if err := json.Unmarshal(b, &a); err != nil {
		return ProjectAutoAllow{}, nil
	}
	return a, nil
}

// SaveProjectAutoAllow writes the store atomically: a crash mid-write must
// not corrupt a file that decides which project entries run unconfirmed.
// No workspace is an error.
func SaveProjectAutoAllow(agentWorkspace string, a ProjectAutoAllow) error {
	p := ProjectAutoAllowPath(agentWorkspace)
	if p == "" {
		return errNoAgentWorkspace
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// On reports whether projectKey's switch is on ("" is never on).
func (a ProjectAutoAllow) On(projectKey string) bool {
	projectKey = strings.TrimSpace(projectKey)
	if projectKey == "" {
		return false
	}
	_, ok := a[projectKey]
	return ok
}

// SetProjectAutoAllow turns one project's switch on or off and saves. An empty
// key or no workspace is a no-op: there is nothing to store the switch in.
// An unreadable store is returned as an error and not overwritten.
func SetProjectAutoAllow(agentWorkspace, projectKey string, on bool, at time.Time) error {
	projectKey = strings.TrimSpace(projectKey)
	if projectKey == "" || ProjectAutoAllowPath(agentWorkspace) == "" {
		return nil
	}
	a, err := LoadProjectAutoAllow(agentWorkspace)
	if err != nil {
		return err
	}
	if at.IsZero() {
		at = time.Now()
	}
	if on {
		a[projectKey] = ProjectAutoAllowRecord{Since: at.Unix()}
	} else {
		delete(a, projectKey)
	}
	return SaveProjectAutoAllow(agentWorkspace, a)
}

// ProjectAutoAllowed is the read every resolution uses: any error reads as off.
func ProjectAutoAllowed(agentWorkspace, projectKey string) bool {
	a, err := LoadProjectAutoAllow(agentWorkspace)
	if err != nil {
		return false
	}
	return a.On(projectKey)
}
