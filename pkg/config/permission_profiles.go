package config

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"path/filepath"
	"sort"
	"strings"
)

const (
	PermissionProfileReadOnly         = ":read-only"
	PermissionProfileWorkspace        = ":workspace"
	PermissionProfileDangerFullAccess = ":danger-full-access"
)

type FileSystemAccess string

const (
	FileSystemAccessRead  FileSystemAccess = "read"
	FileSystemAccessWrite FileSystemAccess = "write"
	FileSystemAccessDeny  FileSystemAccess = "deny"
)

func (a FileSystemAccess) valid() bool {
	return a == FileSystemAccessRead || a == FileSystemAccessWrite || a == FileSystemAccessDeny
}

type FileSystemPermissionValue struct {
	Access FileSystemAccess
	Scoped map[string]FileSystemAccess
}

func (v *FileSystemPermissionValue) UnmarshalYAML(unmarshal func(any) error) error {
	var raw any
	if err := unmarshal(&raw); err != nil {
		return err
	}
	parsed, err := parseFileSystemPermissionValue(raw)
	if err != nil {
		return err
	}
	*v = parsed
	return nil
}

func (v FileSystemPermissionValue) MarshalYAML() (any, error) {
	if v.Scoped != nil {
		return v.Scoped, nil
	}
	return string(v.Access), nil
}

func (v *FileSystemPermissionValue) UnmarshalJSON(data []byte) error {
	var access string
	if json.Unmarshal(data, &access) == nil {
		parsed, err := parseFileSystemPermissionValue(access)
		if err != nil {
			return err
		}
		*v = parsed
		return nil
	}
	var scoped map[string]FileSystemAccess
	if err := json.Unmarshal(data, &scoped); err != nil {
		return err
	}
	parsed, err := parseFileSystemPermissionValue(scoped)
	if err != nil {
		return err
	}
	*v = parsed
	return nil
}

func (v FileSystemPermissionValue) MarshalJSON() ([]byte, error) {
	if v.Scoped != nil {
		return json.Marshal(v.Scoped)
	}
	return json.Marshal(v.Access)
}

func parseFileSystemPermissionValue(raw any) (FileSystemPermissionValue, error) {
	switch value := raw.(type) {
	case string:
		access := FileSystemAccess(strings.ToLower(strings.TrimSpace(value)))
		if !access.valid() {
			return FileSystemPermissionValue{}, fmt.Errorf("invalid filesystem access %q", value)
		}
		return FileSystemPermissionValue{Access: access}, nil
	case map[string]FileSystemAccess:
		return normalizedScopedPermissions(value)
	case map[string]any:
		scoped := make(map[string]FileSystemAccess, len(value))
		for path, rawAccess := range value {
			access, ok := rawAccess.(string)
			if !ok {
				return FileSystemPermissionValue{}, fmt.Errorf("filesystem subpath %q must use read, write, or deny", path)
			}
			scoped[path] = FileSystemAccess(strings.ToLower(strings.TrimSpace(access)))
		}
		return normalizedScopedPermissions(scoped)
	case map[any]any:
		scoped := make(map[string]FileSystemAccess, len(value))
		for rawPath, rawAccess := range value {
			path, pathOK := rawPath.(string)
			access, accessOK := rawAccess.(string)
			if !pathOK || !accessOK {
				return FileSystemPermissionValue{}, fmt.Errorf("filesystem scoped entries must map paths to read, write, or deny")
			}
			scoped[path] = FileSystemAccess(strings.ToLower(strings.TrimSpace(access)))
		}
		return normalizedScopedPermissions(scoped)
	default:
		return FileSystemPermissionValue{}, fmt.Errorf("filesystem permission must be read, write, deny, or a scoped table")
	}
}

func normalizedScopedPermissions(scoped map[string]FileSystemAccess) (FileSystemPermissionValue, error) {
	out := make(map[string]FileSystemAccess, len(scoped))
	for path, access := range scoped {
		path = strings.TrimSpace(path)
		access = FileSystemAccess(strings.ToLower(strings.TrimSpace(string(access))))
		if path == "" || !access.valid() {
			return FileSystemPermissionValue{}, fmt.Errorf("invalid scoped filesystem permission %q=%q", path, access)
		}
		out[path] = access
	}
	return FileSystemPermissionValue{Scoped: out}, nil
}

type FileSystemPermissions struct {
	GlobScanMaxDepth *int
	Entries          map[string]FileSystemPermissionValue
}

func (f *FileSystemPermissions) UnmarshalYAML(unmarshal func(any) error) error {
	var raw map[string]any
	if err := unmarshal(&raw); err != nil {
		return err
	}
	return f.decodeMap(raw)
}

func (f FileSystemPermissions) MarshalYAML() (any, error) {
	out := make(map[string]any, len(f.Entries)+1)
	if f.GlobScanMaxDepth != nil {
		out["glob_scan_max_depth"] = *f.GlobScanMaxDepth
	}
	for path, value := range f.Entries {
		if value.Scoped != nil {
			out[path] = value.Scoped
		} else {
			out[path] = string(value.Access)
		}
	}
	return out, nil
}

func (f *FileSystemPermissions) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	return f.decodeMap(raw)
}

func (f FileSystemPermissions) MarshalJSON() ([]byte, error) {
	raw, _ := f.MarshalYAML()
	return json.Marshal(raw)
}

func (f *FileSystemPermissions) decodeMap(raw map[string]any) error {
	out := FileSystemPermissions{Entries: map[string]FileSystemPermissionValue{}}
	for key, rawValue := range raw {
		if key == "glob_scan_max_depth" {
			var depth int
			switch value := rawValue.(type) {
			case int:
				depth = value
			case float64:
				if math.Trunc(value) != value {
					return fmt.Errorf("glob_scan_max_depth must be an integer")
				}
				depth = int(value)
			default:
				return fmt.Errorf("glob_scan_max_depth must be an integer")
			}
			out.GlobScanMaxDepth = &depth
			continue
		}
		value, err := parseFileSystemPermissionValue(rawValue)
		if err != nil {
			return fmt.Errorf("filesystem path %q: %w", key, err)
		}
		out.Entries[key] = value
	}
	*f = out
	return nil
}

type NetworkAccess string

const (
	NetworkAccessAllow NetworkAccess = "allow"
	NetworkAccessDeny  NetworkAccess = "deny"
)

type NetworkPermissionConfig struct {
	Enabled                          *bool                    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	ProxyURL                         *string                  `yaml:"proxy_url,omitempty" json:"proxy_url,omitempty"`
	EnableSOCKS5                     *bool                    `yaml:"enable_socks5,omitempty" json:"enable_socks5,omitempty"`
	SOCKSURL                         *string                  `yaml:"socks_url,omitempty" json:"socks_url,omitempty"`
	EnableSOCKS5UDP                  *bool                    `yaml:"enable_socks5_udp,omitempty" json:"enable_socks5_udp,omitempty"`
	AllowUpstreamProxy               *bool                    `yaml:"allow_upstream_proxy,omitempty" json:"allow_upstream_proxy,omitempty"`
	DangerouslyAllowNonLoopbackProxy *bool                    `yaml:"dangerously_allow_non_loopback_proxy,omitempty" json:"dangerously_allow_non_loopback_proxy,omitempty"`
	DangerouslyAllowAllUnixSockets   *bool                    `yaml:"dangerously_allow_all_unix_sockets,omitempty" json:"dangerously_allow_all_unix_sockets,omitempty"`
	Mode                             string                   `yaml:"mode,omitempty" json:"mode,omitempty"`
	Domains                          map[string]NetworkAccess `yaml:"domains,omitempty" json:"domains,omitempty"`
	UnixSockets                      map[string]NetworkAccess `yaml:"unix_sockets,omitempty" json:"unix_sockets,omitempty"`
	AllowLocalBinding                *bool                    `yaml:"allow_local_binding,omitempty" json:"allow_local_binding,omitempty"`
	MITM                             *NetworkMITMConfig       `yaml:"mitm,omitempty" json:"mitm,omitempty"`
}

func (n *NetworkPermissionConfig) UnmarshalYAML(unmarshal func(any) error) error {
	if err := rejectUnknownYAMLFields(unmarshal, "permissions network", []string{
		"enabled", "proxy_url", "enable_socks5", "socks_url", "enable_socks5_udp",
		"allow_upstream_proxy", "dangerously_allow_non_loopback_proxy",
		"dangerously_allow_all_unix_sockets", "mode", "domains", "unix_sockets",
		"allow_local_binding", "mitm",
	}); err != nil {
		return err
	}
	type plain NetworkPermissionConfig
	var decoded plain
	if err := unmarshal(&decoded); err != nil {
		return err
	}
	*n = NetworkPermissionConfig(decoded)
	return nil
}

func (n *NetworkPermissionConfig) UnmarshalJSON(data []byte) error {
	if err := rejectUnknownJSONFields(data, "permissions network", []string{
		"enabled", "proxy_url", "enable_socks5", "socks_url", "enable_socks5_udp",
		"allow_upstream_proxy", "dangerously_allow_non_loopback_proxy",
		"dangerously_allow_all_unix_sockets", "mode", "domains", "unix_sockets",
		"allow_local_binding", "mitm",
	}); err != nil {
		return err
	}
	type plain NetworkPermissionConfig
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*n = NetworkPermissionConfig(decoded)
	return nil
}

type NetworkMITMConfig struct {
	Hooks   map[string]NetworkMITMHook   `yaml:"hooks,omitempty" json:"hooks,omitempty"`
	Actions map[string]NetworkMITMAction `yaml:"actions,omitempty" json:"actions,omitempty"`
}

func (m *NetworkMITMConfig) UnmarshalYAML(unmarshal func(any) error) error {
	if err := rejectUnknownYAMLFields(unmarshal, "permissions network.mitm", []string{"hooks", "actions"}); err != nil {
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
	if err := rejectUnknownJSONFields(data, "permissions network.mitm", []string{"hooks", "actions"}); err != nil {
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
	if err := rejectUnknownYAMLFields(unmarshal, "permissions network.mitm hook", []string{
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
			return fmt.Errorf("permissions network.mitm hook requires %s", field)
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
	if err := rejectUnknownJSONFields(data, "permissions network.mitm hook", []string{
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
			return fmt.Errorf("permissions network.mitm hook requires %s", field)
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

type PermissionProfileConfig struct {
	Description    string                   `yaml:"description,omitempty" json:"description,omitempty"`
	Extends        string                   `yaml:"extends,omitempty" json:"extends,omitempty"`
	WorkspaceRoots map[string]bool          `yaml:"workspace_roots,omitempty" json:"workspace_roots,omitempty"`
	FileSystem     *FileSystemPermissions   `yaml:"filesystem,omitempty" json:"filesystem,omitempty"`
	Network        *NetworkPermissionConfig `yaml:"network,omitempty" json:"network,omitempty"`
}

func (p *PermissionProfileConfig) UnmarshalYAML(unmarshal func(any) error) error {
	if err := rejectUnknownYAMLFields(unmarshal, "permissions profile", []string{
		"description", "extends", "workspace_roots", "filesystem", "network",
	}); err != nil {
		return err
	}
	type plain PermissionProfileConfig
	var decoded plain
	if err := unmarshal(&decoded); err != nil {
		return err
	}
	*p = PermissionProfileConfig(decoded)
	return nil
}

func (p *PermissionProfileConfig) UnmarshalJSON(data []byte) error {
	if err := rejectUnknownJSONFields(data, "permissions profile", []string{
		"description", "extends", "workspace_roots", "filesystem", "network",
	}); err != nil {
		return err
	}
	type plain PermissionProfileConfig
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*p = PermissionProfileConfig(decoded)
	return nil
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

type PermissionProfiles map[string]PermissionProfileConfig

func (r *Root) ResolvePermissionProfile() (PermissionProfileConfig, bool, error) {
	if r == nil || strings.TrimSpace(r.DefaultPermissions) == "" {
		return PermissionProfileConfig{}, false, nil
	}
	name := strings.TrimSpace(r.DefaultPermissions)
	if profile, ok := r.builtInPermissionProfile(name); ok {
		return profile, true, nil
	}
	if strings.HasPrefix(name, ":") {
		return PermissionProfileConfig{}, false, fmt.Errorf("default_permissions refers to unknown built-in profile %q", name)
	}
	profile, err := r.Permissions.resolve(name, nil, map[string]bool{})
	return profile, err == nil, err
}

func (r *Root) DangerFullAccessEnabled() bool {
	if r == nil {
		return false
	}
	if selected := strings.TrimSpace(r.DefaultPermissions); selected != "" {
		return selected == PermissionProfileDangerFullAccess
	}
	return r.SandboxMode == SandboxModeDangerFullAccess
}

func (r *Root) builtInPermissionProfile(name string) (PermissionProfileConfig, bool) {
	return builtInPermissionProfile(name)
}

func (p PermissionProfiles) resolve(name string, chain []string, visiting map[string]bool) (PermissionProfileConfig, error) {
	if visiting[name] {
		return PermissionProfileConfig{}, fmt.Errorf("permissions profile inheritance cycle detected: %s", strings.Join(append(chain, name), " -> "))
	}
	profile, ok := p[name]
	if !ok {
		// Which setting is at fault depends on how the name was reached.
		// Blaming default_permissions for a typo in some profile's `extends`
		// sends the reader to a setting that is very likely correct.
		if len(chain) == 0 {
			return PermissionProfileConfig{}, fmt.Errorf("default_permissions refers to undefined profile %q", name)
		}
		return PermissionProfileConfig{}, fmt.Errorf("permissions profile %q extends undefined profile %q", chain[len(chain)-1], name)
	}
	if strings.TrimSpace(profile.Extends) == "" {
		return clonePermissionProfile(profile), nil
	}
	visiting[name] = true
	defer delete(visiting, name)
	parentName := strings.TrimSpace(profile.Extends)
	parent, builtIn := builtInExtensiblePermissionProfile(parentName)
	if !builtIn {
		if strings.HasPrefix(parentName, ":") {
			return PermissionProfileConfig{}, fmt.Errorf("permissions profile %q cannot extend unsupported built-in profile %q", name, parentName)
		}
		var err error
		parent, err = p.resolve(parentName, append(chain, name), visiting)
		if err != nil {
			return PermissionProfileConfig{}, err
		}
	}
	return mergePermissionProfile(parent, profile), nil
}

func builtInPermissionProfile(name string) (PermissionProfileConfig, bool) {
	readRoot := FileSystemPermissionValue{Access: FileSystemAccessRead}
	switch name {
	case PermissionProfileReadOnly:
		return PermissionProfileConfig{FileSystem: &FileSystemPermissions{Entries: map[string]FileSystemPermissionValue{":root": readRoot}}}, true
	case PermissionProfileWorkspace:
		return PermissionProfileConfig{FileSystem: &FileSystemPermissions{Entries: map[string]FileSystemPermissionValue{
			":root": readRoot, ":workspace_roots": {Access: FileSystemAccessWrite}, ":tmpdir": {Access: FileSystemAccessWrite}, ":slash_tmp": {Access: FileSystemAccessWrite},
		}}}, true
	case PermissionProfileDangerFullAccess:
		return PermissionProfileConfig{FileSystem: &FileSystemPermissions{Entries: map[string]FileSystemPermissionValue{":root": {Access: FileSystemAccessWrite}}}, Network: &NetworkPermissionConfig{Enabled: boolPointer(true)}}, true
	default:
		return PermissionProfileConfig{}, false
	}
}

func builtInExtensiblePermissionProfile(name string) (PermissionProfileConfig, bool) {
	if name == PermissionProfileReadOnly || name == PermissionProfileWorkspace {
		return builtInPermissionProfile(name)
	}
	return PermissionProfileConfig{}, false
}

func boolPointer(value bool) *bool { return &value }

func clonePermissionProfile(in PermissionProfileConfig) PermissionProfileConfig {
	out := in
	out.WorkspaceRoots = cloneBoolMap(in.WorkspaceRoots)
	if in.FileSystem != nil {
		fs := *in.FileSystem
		if in.FileSystem.GlobScanMaxDepth != nil {
			depth := *in.FileSystem.GlobScanMaxDepth
			fs.GlobScanMaxDepth = &depth
		}
		fs.Entries = cloneFileSystemEntries(in.FileSystem.Entries)
		out.FileSystem = &fs
	}
	if in.Network != nil {
		network := *in.Network
		network.Enabled = cloneBoolPointer(in.Network.Enabled)
		network.ProxyURL = cloneStringPointer(in.Network.ProxyURL)
		network.EnableSOCKS5 = cloneBoolPointer(in.Network.EnableSOCKS5)
		network.SOCKSURL = cloneStringPointer(in.Network.SOCKSURL)
		network.EnableSOCKS5UDP = cloneBoolPointer(in.Network.EnableSOCKS5UDP)
		network.AllowUpstreamProxy = cloneBoolPointer(in.Network.AllowUpstreamProxy)
		network.DangerouslyAllowNonLoopbackProxy = cloneBoolPointer(in.Network.DangerouslyAllowNonLoopbackProxy)
		network.DangerouslyAllowAllUnixSockets = cloneBoolPointer(in.Network.DangerouslyAllowAllUnixSockets)
		network.Domains = cloneNetworkAccessMap(in.Network.Domains)
		network.UnixSockets = cloneNetworkAccessMap(in.Network.UnixSockets)
		network.AllowLocalBinding = cloneBoolPointer(in.Network.AllowLocalBinding)
		network.MITM = cloneNetworkMITMConfig(in.Network.MITM)
		out.Network = &network
	}
	return out
}

func mergePermissionProfile(parent, child PermissionProfileConfig) PermissionProfileConfig {
	out := clonePermissionProfile(parent)
	out.Description = child.Description
	out.Extends = child.Extends
	out.WorkspaceRoots = mergeBoolMap(out.WorkspaceRoots, child.WorkspaceRoots)
	if child.FileSystem != nil {
		if out.FileSystem == nil {
			out.FileSystem = &FileSystemPermissions{}
		}
		if child.FileSystem.GlobScanMaxDepth != nil {
			depth := *child.FileSystem.GlobScanMaxDepth
			out.FileSystem.GlobScanMaxDepth = &depth
		}
		out.FileSystem.Entries = mergeFileSystemEntries(out.FileSystem.Entries, child.FileSystem.Entries)
	}
	if child.Network != nil {
		out.Network = mergeNetworkPermissionConfig(out.Network, child.Network)
	}
	return out
}

func mergeNetworkPermissionConfig(parent, child *NetworkPermissionConfig) *NetworkPermissionConfig {
	if parent == nil {
		cloned := clonePermissionProfile(PermissionProfileConfig{Network: child})
		return cloned.Network
	}
	out := *parent
	if child.Enabled != nil {
		out.Enabled = child.Enabled
	}
	if child.ProxyURL != nil {
		out.ProxyURL = cloneStringPointer(child.ProxyURL)
	}
	if child.EnableSOCKS5 != nil {
		out.EnableSOCKS5 = child.EnableSOCKS5
	}
	if child.SOCKSURL != nil {
		out.SOCKSURL = cloneStringPointer(child.SOCKSURL)
	}
	if child.EnableSOCKS5UDP != nil {
		out.EnableSOCKS5UDP = child.EnableSOCKS5UDP
	}
	if child.AllowUpstreamProxy != nil {
		out.AllowUpstreamProxy = child.AllowUpstreamProxy
	}
	if child.DangerouslyAllowNonLoopbackProxy != nil {
		out.DangerouslyAllowNonLoopbackProxy = child.DangerouslyAllowNonLoopbackProxy
	}
	if child.DangerouslyAllowAllUnixSockets != nil {
		out.DangerouslyAllowAllUnixSockets = child.DangerouslyAllowAllUnixSockets
	}
	if child.Mode != "" {
		out.Mode = child.Mode
	}
	if out.Domains != nil && child.Domains != nil {
		out.Domains = normalizeNetworkDomainMap(out.Domains)
		out.Domains = mergeNetworkAccessMap(out.Domains, normalizeNetworkDomainMap(child.Domains))
	} else {
		out.Domains = mergeNetworkAccessMap(out.Domains, child.Domains)
	}
	out.UnixSockets = mergeNetworkAccessMap(out.UnixSockets, child.UnixSockets)
	if child.AllowLocalBinding != nil {
		out.AllowLocalBinding = child.AllowLocalBinding
	}
	if child.MITM != nil {
		out.MITM = mergeNetworkMITMConfig(out.MITM, child.MITM)
	}
	return &out
}

func cloneBoolPointer(in *bool) *bool {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}

func cloneStringPointer(in *string) *string {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}

func cloneNetworkMITMConfig(in *NetworkMITMConfig) *NetworkMITMConfig {
	if in == nil {
		return nil
	}
	out := &NetworkMITMConfig{}
	if in.Hooks != nil {
		out.Hooks = make(map[string]NetworkMITMHook, len(in.Hooks))
		for name, hook := range in.Hooks {
			out.Hooks[name] = cloneNetworkMITMHook(hook)
		}
	}
	if in.Actions != nil {
		out.Actions = make(map[string]NetworkMITMAction, len(in.Actions))
		for name, action := range in.Actions {
			out.Actions[name] = cloneNetworkMITMAction(action)
		}
	}
	return out
}

func cloneNetworkMITMHook(in NetworkMITMHook) NetworkMITMHook {
	out := in
	out.Methods = append([]string(nil), in.Methods...)
	out.PathPrefixes = append([]string(nil), in.PathPrefixes...)
	out.Action = append([]string(nil), in.Action...)
	out.Query = cloneStringSliceMap(in.Query)
	out.Headers = cloneStringSliceMap(in.Headers)
	return out
}

func cloneNetworkMITMAction(in NetworkMITMAction) NetworkMITMAction {
	out := in
	out.StripRequestHeaders = append([]string(nil), in.StripRequestHeaders...)
	out.InjectRequestHeaders = make([]NetworkMITMInjectedHeader, len(in.InjectRequestHeaders))
	for index, header := range in.InjectRequestHeaders {
		out.InjectRequestHeaders[index] = header
		out.InjectRequestHeaders[index].SecretEnvVar = cloneStringPointer(header.SecretEnvVar)
		out.InjectRequestHeaders[index].SecretFile = cloneStringPointer(header.SecretFile)
		out.InjectRequestHeaders[index].Prefix = cloneStringPointer(header.Prefix)
	}
	return out
}

func cloneStringSliceMap(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := make(map[string][]string, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func mergeNetworkMITMConfig(parent, child *NetworkMITMConfig) *NetworkMITMConfig {
	if parent == nil {
		return cloneNetworkMITMConfig(child)
	}
	out := cloneNetworkMITMConfig(parent)
	if child.Hooks != nil {
		if out.Hooks == nil {
			out.Hooks = map[string]NetworkMITMHook{}
		}
		for name, hook := range child.Hooks {
			if inherited, ok := out.Hooks[name]; ok {
				hook.Query = mergeStringSliceMap(inherited.Query, hook.Query)
				hook.Headers = mergeStringSliceMap(inherited.Headers, hook.Headers)
				if hook.Body == nil {
					hook.Body = inherited.Body
				}
			}
			out.Hooks[name] = cloneNetworkMITMHook(hook)
		}
	}
	if child.Actions != nil {
		if out.Actions == nil {
			out.Actions = map[string]NetworkMITMAction{}
		}
		for name, action := range child.Actions {
			out.Actions[name] = cloneNetworkMITMAction(action)
		}
	}
	return out
}

func mergeStringSliceMap(parent, child map[string][]string) map[string][]string {
	out := cloneStringSliceMap(parent)
	if out == nil && child != nil {
		out = map[string][]string{}
	}
	for key, values := range child {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func cloneBoolMap(in map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
func mergeBoolMap(parent, child map[string]bool) map[string]bool {
	out := cloneBoolMap(parent)
	for k, v := range child {
		out[k] = v
	}
	return out
}
func cloneNetworkAccessMap(in map[string]NetworkAccess) map[string]NetworkAccess {
	out := map[string]NetworkAccess{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
func mergeNetworkAccessMap(parent, child map[string]NetworkAccess) map[string]NetworkAccess {
	out := cloneNetworkAccessMap(parent)
	for k, v := range child {
		out[k] = v
	}
	return out
}

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
func cloneFileSystemEntries(in map[string]FileSystemPermissionValue) map[string]FileSystemPermissionValue {
	return mergeFileSystemEntries(nil, in)
}
func mergeFileSystemEntries(parent, child map[string]FileSystemPermissionValue) map[string]FileSystemPermissionValue {
	out := map[string]FileSystemPermissionValue{}
	for k, v := range parent {
		out[k] = v
	}
	for k, v := range child {
		out[k] = v
	}
	return out
}

func validatePermissionProfiles(r *Root) error {
	if r == nil {
		return nil
	}
	for name := range r.Permissions {
		if strings.HasPrefix(name, ":") {
			return fmt.Errorf("permissions profile %q uses a reserved built-in profile prefix", name)
		}
	}
	for name, configured := range r.Permissions {
		if err := validateProfileNetwork(name, configured.Network); err != nil {
			return err
		}
	}
	if len(r.Permissions) > 0 && strings.TrimSpace(r.DefaultPermissions) == "" {
		return fmt.Errorf("config defines permissions profiles but does not set default_permissions")
	}
	profile, active, err := r.ResolvePermissionProfile()
	if err != nil || !active {
		return err
	}
	if profile.FileSystem != nil {
		if profile.FileSystem.GlobScanMaxDepth != nil && *profile.FileSystem.GlobScanMaxDepth < 1 {
			return fmt.Errorf("glob_scan_max_depth must be at least 1")
		}
		for path, permission := range profile.FileSystem.Entries {
			if err := validateProfileFileSystemEntry(path, permission); err != nil {
				return err
			}
		}
	}
	if err := validateProfileNetwork(strings.TrimSpace(r.DefaultPermissions), profile.Network); err != nil {
		return err
	}
	return nil
}

func validateProfileNetwork(profileName string, network *NetworkPermissionConfig) error {
	if network == nil {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(network.Mode))
	if mode != "" && mode != "limited" && mode != "full" {
		return fmt.Errorf("permissions profile %q network mode must be limited or full", profileName)
	}
	for pattern, access := range network.Domains {
		if strings.TrimSpace(pattern) == "" || (access != NetworkAccessAllow && access != NetworkAccessDeny) {
			return fmt.Errorf("invalid permissions profile %q network domain %q=%q", profileName, pattern, access)
		}
		if err := validateNetworkDomainPattern(pattern, access); err != nil {
			return fmt.Errorf("invalid permissions profile %q network domain %q: %w", profileName, pattern, err)
		}
	}
	for path, access := range network.UnixSockets {
		if !filepath.IsAbs(strings.TrimSpace(path)) || (access != NetworkAccessAllow && access != NetworkAccessDeny) {
			return fmt.Errorf("invalid permissions profile %q network unix socket %q=%q", profileName, path, access)
		}
	}
	if network.MITM != nil {
		for name, action := range network.MITM.Actions {
			if len(action.StripRequestHeaders) == 0 && len(action.InjectRequestHeaders) == 0 {
				return fmt.Errorf("permissions profile %q network.mitm.actions.%s must define at least one operation", profileName, name)
			}
			for _, header := range action.InjectRequestHeaders {
				if !validNetworkHeaderName(header.Name) || (header.SecretEnvVar == nil) == (header.SecretFile == nil) {
					return fmt.Errorf("permissions profile %q network.mitm.actions.%s has an invalid injected header", profileName, name)
				}
				if header.SecretEnvVar != nil && strings.TrimSpace(*header.SecretEnvVar) == "" {
					return fmt.Errorf("permissions profile %q network.mitm.actions.%s secret_env_var must not be empty", profileName, name)
				}
				if header.SecretFile != nil && (strings.TrimSpace(*header.SecretFile) == "" || !filepath.IsAbs(*header.SecretFile)) {
					return fmt.Errorf("permissions profile %q network.mitm.actions.%s secret_file must be absolute", profileName, name)
				}
				if header.Prefix != nil && strings.ContainsAny(*header.Prefix, "\r\n") {
					return fmt.Errorf("permissions profile %q network.mitm.actions.%s header prefix is invalid", profileName, name)
				}
			}
			for _, header := range action.StripRequestHeaders {
				if !validNetworkHeaderName(header) {
					return fmt.Errorf("permissions profile %q network.mitm.actions.%s has an invalid stripped header", profileName, name)
				}
			}
		}
		for name, hook := range network.MITM.Hooks {
			host := normalizeNetworkHost(hook.Host)
			if host == "" || strings.Contains(host, "*") || len(hook.Methods) == 0 || len(hook.PathPrefixes) == 0 || len(hook.Action) == 0 || hook.Body != nil {
				return fmt.Errorf("permissions profile %q network.mitm.hook.%s is invalid", profileName, name)
			}
			for _, method := range hook.Methods {
				if strings.TrimSpace(method) == "" {
					return fmt.Errorf("permissions profile %q network.mitm.hook.%s contains an empty method", profileName, name)
				}
			}
			for _, matcher := range hook.PathPrefixes {
				if err := validateNetworkPathMatcher(matcher); err != nil {
					return fmt.Errorf("permissions profile %q network.mitm.hook.%s path matcher: %w", profileName, name, err)
				}
			}
			for key, values := range hook.Query {
				if key == "" || len(values) == 0 {
					return fmt.Errorf("permissions profile %q network.mitm.hook.%s has an invalid query matcher", profileName, name)
				}
				for _, matcher := range values {
					if err := validateNetworkMatcher(matcher); err != nil {
						return fmt.Errorf("permissions profile %q network.mitm.hook.%s query matcher: %w", profileName, name, err)
					}
				}
			}
			for key, values := range hook.Headers {
				if !validNetworkHeaderName(key) {
					return fmt.Errorf("permissions profile %q network.mitm.hook.%s has an invalid header matcher", profileName, name)
				}
				for _, matcher := range values {
					if err := validateNetworkMatcher(matcher); err != nil {
						return fmt.Errorf("permissions profile %q network.mitm.hook.%s header matcher: %w", profileName, name, err)
					}
				}
			}
		}
	}
	return nil
}

func validateNetworkMatcher(value string) error {
	pattern, glob := strings.CutPrefix(value, "pattern:")
	if !glob {
		return nil
	}
	if pattern == "" {
		return fmt.Errorf("glob pattern must not be empty")
	}
	if _, err := filepath.Match(pattern, "candidate"); err != nil {
		return err
	}
	return nil
}

func validateNetworkPathMatcher(value string) error {
	if literal, ok := strings.CutPrefix(value, "literal:"); ok && literal == "" {
		return fmt.Errorf("path matcher must not be empty")
	}
	if value == "" {
		return fmt.Errorf("path matcher must not be empty")
	}
	return validateNetworkMatcher(value)
}

func validNetworkHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, ch := range name {
		if !(ch >= 'A' && ch <= 'Z') && !(ch >= 'a' && ch <= 'z') && !(ch >= '0' && ch <= '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", ch) {
			return false
		}
	}
	return true
}

func validateProfileFileSystemEntry(path string, permission FileSystemPermissionValue) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("filesystem permission path is empty")
	}
	if !strings.HasPrefix(path, ":") && path != "~" && !strings.HasPrefix(path, "~/") && !filepath.IsAbs(path) {
		return fmt.Errorf("filesystem path %q must be absolute, use ~/..., or start with :", path)
	}
	if permission.Scoped == nil {
		if !permission.Access.valid() {
			return fmt.Errorf("invalid filesystem access for %q", path)
		}
		return validateReadWriteGlob(path, permission.Access)
	}
	for subpath, access := range permission.Scoped {
		if !validProfileSubpath(subpath) {
			return fmt.Errorf("filesystem subpath %q must be a descendant path without . or .. components", subpath)
		}
		if path != ":workspace_roots" && (path == ":root" || path == ":minimal" || path == ":tmpdir" || path == ":slash_tmp") {
			return fmt.Errorf("filesystem path %q does not support nested entries", path)
		}
		if err := validateReadWriteGlob(subpath, access); err != nil {
			return err
		}
	}
	return nil
}

func validProfileSubpath(subpath string) bool {
	if subpath == "." {
		return true
	}
	if subpath == "" || filepath.IsAbs(subpath) {
		return false
	}
	for _, component := range strings.Split(filepath.ToSlash(subpath), "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func validateReadWriteGlob(path string, access FileSystemAccess) error {
	if access == FileSystemAccessDeny {
		return nil
	}
	withoutSubtree := strings.TrimSuffix(path, "/**")
	if strings.ContainsAny(withoutSubtree, "*?[]") {
		return fmt.Errorf("filesystem glob path %q only supports deny access; use an exact path or trailing /** for %s subtree access", path, access)
	}
	return nil
}
