// Language-server configuration: the lsp section of forebrain.yaml and the
// resolved values the runtime reads. See docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md §5.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type LSPSection struct {
	Recommendations *bool                      `yaml:"recommendations,omitempty" json:"recommendations,omitempty"`
	MaxServers      int                        `yaml:"max_servers,omitempty" json:"max_servers,omitempty"`
	IdleTimeout     int                        `yaml:"idle_timeout,omitempty" json:"idle_timeout,omitempty"`
	RequestTimeout  int                        `yaml:"request_timeout,omitempty" json:"request_timeout,omitempty"`
	Diagnostics     LSPDiagnosticsConfig       `yaml:"diagnostics,omitempty" json:"diagnostics,omitempty"`
	Servers         map[string]LSPServerConfig `yaml:"servers,omitempty" json:"servers,omitempty"`
}

type LSPDiagnosticsConfig struct {
	AfterEdit    *bool  `yaml:"after_edit,omitempty" json:"after_edit,omitempty"`
	WaitMS       *int   `yaml:"wait_ms,omitempty" json:"wait_ms,omitempty"`
	MinSeverity  string `yaml:"min_severity,omitempty" json:"min_severity,omitempty"`
	MaxPerFile   int    `yaml:"max_per_file,omitempty" json:"max_per_file,omitempty"`
	MaxFiles     int    `yaml:"max_files,omitempty" json:"max_files,omitempty"`
	LateDelivery *bool  `yaml:"late_delivery,omitempty" json:"late_delivery,omitempty"`
}

type LSPServerConfig struct {
	Enabled               *bool             `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Command               string            `yaml:"command,omitempty" json:"command,omitempty"`
	Args                  []string          `yaml:"args,omitempty" json:"args,omitempty"`
	ExtensionToLanguage   map[string]string `yaml:"extension_to_language,omitempty" json:"extension_to_language,omitempty"`
	Filenames             map[string]string `yaml:"filenames,omitempty" json:"filenames,omitempty"`
	RootMarkers           []string          `yaml:"root_markers,omitempty" json:"root_markers,omitempty"`
	WorkspaceFolder       string            `yaml:"workspace_folder,omitempty" json:"workspace_folder,omitempty"`
	Env                   map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	EnvPassthrough        []string          `yaml:"env_passthrough,omitempty" json:"env_passthrough,omitempty"`
	InitializationOptions LSPJSONObject     `yaml:"initialization_options,omitempty" json:"initialization_options,omitempty"`
	Settings              LSPJSONObject     `yaml:"settings,omitempty" json:"settings,omitempty"`
	StartupTimeout        int               `yaml:"startup_timeout,omitempty" json:"startup_timeout,omitempty"`
	ShutdownTimeout       int               `yaml:"shutdown_timeout,omitempty" json:"shutdown_timeout,omitempty"`
	RestartOnCrash        *bool             `yaml:"restart_on_crash,omitempty" json:"restart_on_crash,omitempty"`
	MaxRestarts           int               `yaml:"max_restarts,omitempty" json:"max_restarts,omitempty"`
	Diagnostics           *bool             `yaml:"diagnostics,omitempty" json:"diagnostics,omitempty"`
	Role                  string            `yaml:"role,omitempty" json:"role,omitempty"`
	Priority              *int              `yaml:"priority,omitempty" json:"priority,omitempty"`
	Prewarm               bool              `yaml:"prewarm,omitempty" json:"prewarm,omitempty"`
}

// LSPJSONObject is a JSON object written as YAML in forebrain.yaml and kept
// as JSON bytes in memory, the same contract as LLMRequestParams.
type LSPJSONObject []byte

const (
	LSPRolePrimary     = "primary"
	LSPRoleDiagnostics = "diagnostics"
)

// EffectiveLSP is the lsp section with every default applied.
type EffectiveLSP struct {
	Recommendations bool
	MaxServers      int
	IdleTimeout     time.Duration
	RequestTimeout  time.Duration
	AfterEdit       bool
	Wait            time.Duration
	MinSeverity     string // "error" | "warning" | "information" | "hint"
	MaxPerFile      int
	MaxFiles        int
	LateDelivery    bool
}

var lspServerIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// EffectiveLSP resolves the lsp section against the §5.1 defaults. Zero values
// mean "unset", except Diagnostics.WaitMS, where an explicit 0 means "do not
// wait" and nil means the 2500ms default — hence the pointer.
func (r *Root) EffectiveLSP() EffectiveLSP {
	s := LSPSection{}
	if r != nil {
		s = r.LSP
	}
	wait := 2500 * time.Millisecond
	if s.Diagnostics.WaitMS != nil {
		wait = time.Duration(*s.Diagnostics.WaitMS) * time.Millisecond
	}
	minSeverity := s.Diagnostics.MinSeverity
	if minSeverity == "" {
		minSeverity = "warning"
	}
	maxServers := s.MaxServers
	if maxServers <= 0 {
		maxServers = 6
	}
	idleTimeout := s.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = 600
	}
	requestTimeout := s.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = 30
	}
	maxPerFile := s.Diagnostics.MaxPerFile
	if maxPerFile <= 0 {
		maxPerFile = 20
	}
	maxFiles := s.Diagnostics.MaxFiles
	if maxFiles <= 0 {
		maxFiles = 10
	}
	return EffectiveLSP{
		Recommendations: boolValue(s.Recommendations, true),
		MaxServers:      maxServers,
		IdleTimeout:     time.Duration(idleTimeout) * time.Second,
		RequestTimeout:  time.Duration(requestTimeout) * time.Second,
		AfterEdit:       boolValue(s.Diagnostics.AfterEdit, true),
		Wait:            wait,
		MinSeverity:     minSeverity,
		MaxPerFile:      maxPerFile,
		MaxFiles:        maxFiles,
		LateDelivery:    boolValue(s.Diagnostics.LateDelivery, true),
	}
}

func (p LSPJSONObject) MarshalJSON() ([]byte, error) {
	if len(p) == 0 {
		return []byte("null"), nil
	}
	return []byte(p), nil
}

func (p *LSPJSONObject) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*p = nil
		return nil
	}
	if !json.Valid(data) {
		return fmt.Errorf("lsp object: invalid json")
	}
	var probe any
	_ = json.Unmarshal(data, &probe)
	if _, ok := probe.(map[string]interface{}); !ok {
		return fmt.Errorf("lsp object: must be a json object")
	}
	cp := append(LSPJSONObject(nil), data...)
	*p = cp
	return nil
}

func (p LSPJSONObject) MarshalYAML() (interface{}, error) {
	if len(p) == 0 {
		return nil, nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(p, &m); err != nil {
		return nil, err
	}
	return toYAMLCompatibleMap(m), nil
}

func (p *LSPJSONObject) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var raw interface{}
	if err := unmarshal(&raw); err != nil {
		return err
	}
	if raw == nil {
		*p = nil
		return nil
	}
	b, err := lspObjectYAMLValueToJSONBytes(raw)
	if err != nil {
		return err
	}
	*p = b
	return nil
}

func lspObjectYAMLValueToJSONBytes(raw interface{}) (LSPJSONObject, error) {
	switch v := raw.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil, nil
		}
		if !json.Valid([]byte(s)) {
			return nil, fmt.Errorf("lsp object: invalid json")
		}
		var probe any
		_ = json.Unmarshal([]byte(s), &probe)
		if _, ok := probe.(map[string]interface{}); !ok {
			return nil, fmt.Errorf("lsp object: must be a json object")
		}
		return LSPJSONObject(s), nil
	default:
		j := toJSONCompatibleValue(raw)
		b, err := json.Marshal(j)
		if err != nil {
			return nil, err
		}
		var probe any
		_ = json.Unmarshal(b, &probe)
		if _, ok := probe.(map[string]interface{}); !ok {
			return nil, fmt.Errorf("lsp object: must be a json object")
		}
		return LSPJSONObject(b), nil
	}
}

// normalizeLSPSection trims and lowercases the identifiers users write by hand:
// server ids, extension keys, roles, and the severity name. Values that are
// free-form text (commands, language ids) are only trimmed.
func normalizeLSPSection(s *LSPSection) {
	s.Diagnostics.MinSeverity = strings.ToLower(strings.TrimSpace(s.Diagnostics.MinSeverity))
	if s.Servers == nil {
		return
	}
	normalized := make(map[string]LSPServerConfig, len(s.Servers))
	for id, server := range s.Servers {
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
	s.Servers = normalized
}

// ValidateLSPServers checks one servers mapping. It is also reused for the
// project-level lsp_servers.yaml (task 15), which is why it takes its own
// source prefix instead of assuming the global section.
func ValidateLSPServers(source string, servers map[string]LSPServerConfig) error {
	for id, server := range servers {
		if !lspServerIDPattern.MatchString(id) {
			return fmt.Errorf("%s.%s: server id must match [a-z0-9][a-z0-9._-]{0,63}", source, id)
		}
		if strings.ContainsAny(server.Command, "\n\r\x00") {
			return fmt.Errorf("%s.%s.command must not contain line breaks or NUL", source, id)
		}
		for ext, language := range server.ExtensionToLanguage {
			if !strings.HasPrefix(ext, ".") || len(ext) < 2 {
				return fmt.Errorf("%s.%s.extension_to_language.%s must start with a dot and be at least 2 characters", source, id, ext)
			}
			if strings.TrimSpace(language) == "" {
				return fmt.Errorf("%s.%s.extension_to_language.%s must not be empty", source, id, ext)
			}
		}
		for name, language := range server.Filenames {
			if strings.ContainsAny(name, `/\`) {
				return fmt.Errorf("%s.%s.filenames.%s must not contain a path separator", source, id, name)
			}
			if strings.TrimSpace(language) == "" {
				return fmt.Errorf("%s.%s.filenames.%s must not be empty", source, id, name)
			}
		}
		switch server.Role {
		case "", LSPRolePrimary, LSPRoleDiagnostics:
		default:
			return fmt.Errorf("%s.%s.role must be primary or diagnostics", source, id)
		}
		if server.StartupTimeout < 0 || server.StartupTimeout > 600 {
			return fmt.Errorf("%s.%s.startup_timeout must be between 0 and 600", source, id)
		}
		if server.ShutdownTimeout < 0 || server.ShutdownTimeout > 60 {
			return fmt.Errorf("%s.%s.shutdown_timeout must be between 0 and 60", source, id)
		}
		if server.MaxRestarts < 0 || server.MaxRestarts > 20 {
			return fmt.Errorf("%s.%s.max_restarts must be between 0 and 20", source, id)
		}
	}
	return nil
}

// validateLSPSection enforces the §5.1 numeric ranges. Zero means "take the
// default" for every counter, except wait_ms where an explicit 0 is a choice
// (no wait window) and nil is the default.
func validateLSPSection(r *Root) error {
	if v := r.LSP.MaxServers; v < 0 || v > 32 {
		return fmt.Errorf("lsp.max_servers must be between 1 and 32")
	}
	if v := r.LSP.IdleTimeout; v != 0 && (v < 60 || v > 86400) {
		return fmt.Errorf("lsp.idle_timeout must be between 60 and 86400")
	}
	if v := r.LSP.RequestTimeout; v < 0 || v > 600 {
		return fmt.Errorf("lsp.request_timeout must be between 1 and 600")
	}
	if v := r.LSP.Diagnostics.WaitMS; v != nil && (*v < 0 || *v > 30000) {
		return fmt.Errorf("lsp.diagnostics.wait_ms must be between 0 and 30000")
	}
	switch r.LSP.Diagnostics.MinSeverity {
	case "", "error", "warning", "information", "hint":
	default:
		return fmt.Errorf("lsp.diagnostics.min_severity must be error, warning, information, or hint")
	}
	if v := r.LSP.Diagnostics.MaxPerFile; v < 0 || v > 200 {
		return fmt.Errorf("lsp.diagnostics.max_per_file must be between 1 and 200")
	}
	if v := r.LSP.Diagnostics.MaxFiles; v < 0 || v > 50 {
		return fmt.Errorf("lsp.diagnostics.max_files must be between 1 and 50")
	}
	return ValidateLSPServers("lsp.servers", r.LSP.Servers)
}
