package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
)

const (
	HookTypeCommand = "command"
	HookTypePrompt  = "prompt"
	HookTypeAgent   = "agent"
	HookTypeHTTP    = "http"
)

var persistentHookTypes = map[string]struct{}{
	HookTypeCommand: {},
	HookTypePrompt:  {},
	HookTypeAgent:   {},
	HookTypeHTTP:    {},
}

type HookCommand struct {
	Type           string            `yaml:"type,omitempty" json:"type,omitempty"`
	Command        string            `yaml:"command,omitempty" json:"command,omitempty"`
	Prompt         string            `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	URL            string            `yaml:"url,omitempty" json:"url,omitempty"`
	If             string            `yaml:"if,omitempty" json:"if,omitempty"`
	Shell          string            `yaml:"shell,omitempty" json:"shell,omitempty"`
	Timeout        float64           `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	StatusMessage  string            `yaml:"status_message,omitempty" json:"status_message,omitempty"`
	Once           bool              `yaml:"once" json:"once"`
	Async          bool              `yaml:"async" json:"async"`
	AsyncRewake    bool              `yaml:"async_rewake" json:"async_rewake"`
	Model          string            `yaml:"model,omitempty" json:"model,omitempty"`
	Headers        map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	AllowedEnvVars []string          `yaml:"allowed_env_vars,omitempty" json:"allowed_env_vars,omitempty"`
}

type HookMatcher struct {
	Matcher string        `yaml:"matcher,omitempty" json:"matcher,omitempty"`
	Hooks   []HookCommand `yaml:"hooks,omitempty" json:"hooks,omitempty"`
}

type HooksSettings map[string][]HookMatcher

func ValidPersistentHookType(kind string) bool {
	_, ok := persistentHookTypes[strings.TrimSpace(kind)]
	return ok
}

func ValidateHookCommand(h HookCommand) error {
	kind := strings.TrimSpace(h.Type)
	if !ValidPersistentHookType(kind) {
		return fmt.Errorf("unsupported hook type: %s", h.Type)
	}
	if h.Timeout < 0 {
		return fmt.Errorf("hook timeout must be positive")
	}
	if strings.TrimSpace(h.If) != "" && !LooksLikePermissionRule(h.If) {
		return fmt.Errorf("hook condition must use permission rule syntax")
	}
	switch kind {
	case HookTypeCommand:
		if strings.TrimSpace(h.Command) == "" {
			return fmt.Errorf("command hook requires command")
		}
		if h.Shell != "" && h.Shell != "bash" && h.Shell != "powershell" {
			return fmt.Errorf("unsupported hook shell: %s", h.Shell)
		}
	case HookTypePrompt, HookTypeAgent:
		if strings.TrimSpace(h.Prompt) == "" {
			return fmt.Errorf("%s hook requires prompt", kind)
		}
	case HookTypeHTTP:
		u, err := url.Parse(strings.TrimSpace(h.URL))
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("http hook requires absolute url")
		}
	}
	return nil
}

func ValidateHookMatcher(m HookMatcher) error {
	if len(m.Hooks) == 0 {
		return fmt.Errorf("hook matcher requires hooks")
	}
	for i, h := range m.Hooks {
		if err := ValidateHookCommand(h); err != nil {
			return fmt.Errorf("hook %d: %w", i, err)
		}
	}
	return nil
}

// hookEventNames is the ordered list of events a hook may bind to. It is the
// one place the set is written down: the validator and the settings surface
// both read it, so a new event cannot become valid without also becoming
// offerable.
var hookEventNames = []string{
	"PreToolUse",
	"PermissionRequest",
	"PostToolUse",
	"PostToolUseFailure",
	"Notification",
	"UserPromptSubmit",
	"SessionStart",
	"PreCompact",
	"PostCompact",
	"Stop",
	"SubagentStop",
}

// HookEventNames returns the events a hook may bind to, in the order a surface
// should offer them.
func HookEventNames() []string {
	return append([]string(nil), hookEventNames...)
}

// PersistentHookTypes returns the hook kinds that may be written to the config,
// sorted so the list a surface renders is stable.
func PersistentHookTypes() []string {
	out := make([]string, 0, len(persistentHookTypes))
	for kind := range persistentHookTypes {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

func ValidHookEventName(name string) bool {
	name = strings.TrimSpace(name)
	for _, known := range hookEventNames {
		if known == name {
			return true
		}
	}
	return false
}

func ValidateHooksSettings(hooks HooksSettings) error {
	for event, matchers := range hooks {
		if !ValidHookEventName(event) {
			return fmt.Errorf("unsupported hook event: %s", event)
		}
		for i, matcher := range matchers {
			if err := ValidateHookMatcher(matcher); err != nil {
				return fmt.Errorf("%s matcher %d: %w", event, i, err)
			}
		}
	}
	return nil
}

func LooksLikePermissionRule(rule string) bool {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return false
	}
	open := strings.IndexByte(rule, '(')
	close := strings.LastIndexByte(rule, ')')
	if open <= 0 || close <= open+1 || close != len(rule)-1 {
		return false
	}
	tool := strings.TrimSpace(rule[:open])
	pat := strings.TrimSpace(rule[open+1 : close])
	return tool != "" && pat != ""
}

func HookConditionMatches(rule string, toolName string, input string) bool {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return true
	}
	if !LooksLikePermissionRule(rule) {
		return false
	}
	open := strings.IndexByte(rule, '(')
	close := strings.LastIndexByte(rule, ')')
	tool := strings.TrimSpace(rule[:open])
	pat := strings.TrimSpace(rule[open+1 : close])
	if !strings.EqualFold(tool, strings.TrimSpace(toolName)) {
		return false
	}
	ok, err := filepath.Match(pat, strings.TrimSpace(input))
	return err == nil && ok
}
