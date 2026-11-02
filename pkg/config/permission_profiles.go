package config

import (
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
)

type NetworkAccess string

const (
	NetworkAccessAllow NetworkAccess = "allow"
	NetworkAccessDeny  NetworkAccess = "deny"
)

// The MITM interception types below describe what a network proxy may rewrite
// in a request. They are no longer reachable from configuration: they were only
// ever set by a permission profile, and profiles are internal implementation of
// the approval presets now (plan 203). The managed proxy that consumed them and
// the per-host network approvals built on it are gone (plan 206), so what stays
// is the data shape alone, the way the guardian code does — a future surface can
// bring interception back without re-deriving it.
type NetworkMITMConfig struct {
	Hooks   map[string]NetworkMITMHook   `yaml:"hooks,omitempty" json:"hooks,omitempty"`
	Actions map[string]NetworkMITMAction `yaml:"actions,omitempty" json:"actions,omitempty"`
}

func (m *NetworkMITMConfig) UnmarshalYAML(unmarshal func(any) error) error {
	if err := rejectUnknownYAMLFields(unmarshal, "network.mitm", []string{"hooks", "actions"}); err != nil {
		return err
	}
	type plain NetworkMITMConfig
	var decoded plain
	if err := unmarshal(&decoded); err != nil {
		return err
	}
	*m = NetworkMITMConfig(decoded)
	return nil
}

func (m *NetworkMITMConfig) UnmarshalJSON(data []byte) error {
	if err := rejectUnknownJSONFields(data, "network.mitm", []string{"hooks", "actions"}); err != nil {
		return err
	}
	type plain NetworkMITMConfig
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*m = NetworkMITMConfig(decoded)
	return nil
}

type NetworkMITMHook struct {
	Host         string              `yaml:"host" json:"host"`
	Methods      []string            `yaml:"methods" json:"methods"`
	PathPrefixes []string            `yaml:"path_prefixes" json:"path_prefixes"`
	Query        map[string][]string `yaml:"query,omitempty" json:"query,omitempty"`
	Headers      map[string][]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Body         any                 `yaml:"body,omitempty" json:"body,omitempty"`
	Action       []string            `yaml:"action" json:"action"`
}

func (h *NetworkMITMHook) UnmarshalYAML(unmarshal func(any) error) error {
	required := []string{"host", "methods", "path_prefixes", "action"}
	if err := rejectUnknownYAMLFields(unmarshal, "network.mitm hook", []string{
		"host", "methods", "path_prefixes", "query", "headers", "body", "action",
	}); err != nil {
		return err
	}
	var raw map[string]any
	if err := unmarshal(&raw); err != nil {
		return err
	}
	for _, field := range required {
		if value, ok := raw[field]; !ok || value == nil {
			return fmt.Errorf("network.mitm hook requires %s", field)
		}
	}
	type plain NetworkMITMHook
	var decoded plain
	if err := unmarshal(&decoded); err != nil {
		return err
	}
	*h = NetworkMITMHook(decoded)
	return nil
}

func (h *NetworkMITMHook) UnmarshalJSON(data []byte) error {
	required := []string{"host", "methods", "path_prefixes", "action"}
	if err := rejectUnknownJSONFields(data, "network.mitm hook", []string{
		"host", "methods", "path_prefixes", "query", "headers", "body", "action",
	}); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, field := range required {
		value, ok := raw[field]
		if !ok || string(value) == "null" {
			return fmt.Errorf("network.mitm hook requires %s", field)
		}
	}
	type plain NetworkMITMHook
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*h = NetworkMITMHook(decoded)
	return nil
}

type NetworkMITMAction struct {
	StripRequestHeaders  []string                    `yaml:"strip_request_headers,omitempty" json:"strip_request_headers,omitempty"`
	InjectRequestHeaders []NetworkMITMInjectedHeader `yaml:"inject_request_headers,omitempty" json:"inject_request_headers,omitempty"`
}

type NetworkMITMInjectedHeader struct {
	Name         string  `yaml:"name" json:"name"`
	SecretEnvVar *string `yaml:"secret_env_var,omitempty" json:"secret_env_var,omitempty"`
	SecretFile   *string `yaml:"secret_file,omitempty" json:"secret_file,omitempty"`
	Prefix       *string `yaml:"prefix,omitempty" json:"prefix,omitempty"`
}

func rejectUnknownYAMLFields(unmarshal func(any) error, context string, allowed []string) error {
	var raw map[string]any
	if err := unmarshal(&raw); err != nil {
		return err
	}
	return rejectUnknownFieldNames(raw, context, allowed)
}

func rejectUnknownJSONFields(data []byte, context string, allowed []string) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	return rejectUnknownFieldNames(raw, context, allowed)
}

func rejectUnknownFieldNames(raw map[string]any, context string, allowed []string) error {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = struct{}{}
	}
	for field := range raw {
		if _, ok := allowedSet[field]; !ok {
			return fmt.Errorf("%s contains unknown field %q", context, field)
		}
	}
	return nil
}

func boolPointer(value bool) *bool { return &value }

func mergeNetworkAccessMap(parent, child map[string]NetworkAccess) map[string]NetworkAccess {
	out := make(map[string]NetworkAccess, len(parent)+len(child))
	for k, v := range parent {
		out[k] = v
	}
	for k, v := range child {
		out[k] = v
	}
	return out
}

// normalizeNetworkDomainMap keys a domain table by comparable host names, in
// sorted order so a merged table and the text written back out read the same
// way twice.
func normalizeNetworkDomainMap(in map[string]NetworkAccess) map[string]NetworkAccess {
	if in == nil {
		return nil
	}
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]NetworkAccess, len(in))
	for _, key := range keys {
		out[normalizeNetworkHost(key)] = in[key]
	}
	return out
}

func normalizeNetworkHost(host string) string {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") {
		if end := strings.IndexByte(host, ']'); end >= 0 {
			return normalizeNetworkHostValue(host[1:end])
		}
	}
	if strings.Count(host, ":") == 1 {
		host = strings.SplitN(host, ":", 2)[0]
	}
	return normalizeNetworkHostValue(host)
}

func normalizeNetworkHostValue(host string) string {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if ip, scope, ok := strings.Cut(host, "%25"); ok && net.ParseIP(ip) != nil {
		return ip + "%" + scope
	}
	return host
}
