package config

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

const (
	DefaultNetworkProxyURL = "http://127.0.0.1:3128"
	DefaultNetworkSOCKSURL = "http://127.0.0.1:8081"
)

type NetworkProxyFeatureConfig struct {
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
}

func (c NetworkProxyFeatureConfig) IsZero() bool {
	return c.Enabled == nil && c.ProxyURL == nil && c.EnableSOCKS5 == nil && c.SOCKSURL == nil &&
		c.EnableSOCKS5UDP == nil && c.AllowUpstreamProxy == nil &&
		c.DangerouslyAllowNonLoopbackProxy == nil && c.DangerouslyAllowAllUnixSockets == nil &&
		strings.TrimSpace(c.Mode) == "" && c.Domains == nil && c.UnixSockets == nil && c.AllowLocalBinding == nil
}

func (c NetworkProxyFeatureConfig) EnabledValue() bool {
	return c.Enabled != nil && *c.Enabled
}

func (c *NetworkProxyFeatureConfig) UnmarshalYAML(unmarshal func(any) error) error {
	var enabled bool
	if err := unmarshal(&enabled); err == nil {
		*c = NetworkProxyFeatureConfig{Enabled: boolPointer(enabled)}
		return nil
	}
	if err := rejectUnknownYAMLFields(unmarshal, "features.network_proxy", networkProxyFeatureFields()); err != nil {
		return err
	}
	type plain NetworkProxyFeatureConfig
	var decoded plain
	if err := unmarshal(&decoded); err != nil {
		return err
	}
	*c = NetworkProxyFeatureConfig(decoded)
	return nil
}

func (c NetworkProxyFeatureConfig) MarshalYAML() (any, error) {
	if c.onlyEnabled() {
		return *c.Enabled, nil
	}
	type plain NetworkProxyFeatureConfig
	return plain(c), nil
}

func (c *NetworkProxyFeatureConfig) UnmarshalJSON(data []byte) error {
	var enabled bool
	if err := json.Unmarshal(data, &enabled); err == nil {
		*c = NetworkProxyFeatureConfig{Enabled: boolPointer(enabled)}
		return nil
	}
	if err := rejectUnknownJSONFields(data, "features.network_proxy", networkProxyFeatureFields()); err != nil {
		return err
	}
	type plain NetworkProxyFeatureConfig
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = NetworkProxyFeatureConfig(decoded)
	return nil
}

func (c NetworkProxyFeatureConfig) MarshalJSON() ([]byte, error) {
	if c.onlyEnabled() {
		return json.Marshal(*c.Enabled)
	}
	type plain NetworkProxyFeatureConfig
	return json.Marshal(plain(c))
}

func (c NetworkProxyFeatureConfig) onlyEnabled() bool {
	copy := c
	copy.Enabled = nil
	return c.Enabled != nil && copy.IsZero()
}

func networkProxyFeatureFields() []string {
	return []string{
		"enabled", "proxy_url", "enable_socks5", "socks_url", "enable_socks5_udp",
		"allow_upstream_proxy", "dangerously_allow_non_loopback_proxy",
		"dangerously_allow_all_unix_sockets", "mode", "domains", "unix_sockets",
		"allow_local_binding",
	}
}

type EffectiveNetworkProxyConfig struct {
	Enabled                          bool
	ProxyURL                         string
	EnableSOCKS5                     bool
	SOCKSURL                         string
	EnableSOCKS5UDP                  bool
	AllowUpstreamProxy               bool
	DangerouslyAllowNonLoopbackProxy bool
	DangerouslyAllowAllUnixSockets   bool
	Mode                             string
	Domains                          map[string]NetworkAccess
	UnixSockets                      map[string]NetworkAccess
	AllowLocalBinding                bool
	MITM                             bool
	MITMConfig                       *NetworkMITMConfig
	HardDenyAllowlistMisses          bool
}

type NetworkConstraints struct {
	Enabled                          *bool
	HTTPPort                         *int
	SOCKSPort                        *int
	AllowUpstreamProxy               *bool
	DangerouslyAllowNonLoopbackProxy *bool
	DangerouslyAllowAllUnixSockets   *bool
	Domains                          map[string]NetworkAccess
	ManagedAllowedDomainsOnly        *bool
	UnixSockets                      map[string]NetworkAccess
	AllowLocalBinding                *bool
}

func DefaultEffectiveNetworkProxyConfig() EffectiveNetworkProxyConfig {
	return EffectiveNetworkProxyConfig{
		ProxyURL:           DefaultNetworkProxyURL,
		EnableSOCKS5:       true,
		SOCKSURL:           DefaultNetworkSOCKSURL,
		EnableSOCKS5UDP:    true,
		AllowUpstreamProxy: true,
		Mode:               "full",
	}
}

func (r *Root) ResolveNetworkProxyConfig() (*EffectiveNetworkProxyConfig, error) {
	return r.resolveNetworkProxyConfig(false, nil)
}

func (r *Root) ResolveNetworkProxyConfigForNetworkEnabled() (*EffectiveNetworkProxyConfig, error) {
	return r.resolveNetworkProxyConfig(true, nil)
}

func (r *Root) ResolveNetworkProxyConfigForNetworkEnabledWithConstraints(constraints *NetworkConstraints) (*EffectiveNetworkProxyConfig, error) {
	return r.resolveNetworkProxyConfig(true, constraints)
}

func (r *Root) resolveNetworkProxyConfig(forceNetworkEnabled bool, constraints *NetworkConstraints) (*EffectiveNetworkProxyConfig, error) {
	if r == nil || r.DangerFullAccessEnabled() || (!r.Features.NetworkProxy.EnabledValue() && constraints == nil) {
		return nil, nil
	}
	profile, active, err := r.ResolvePermissionProfile()
	if err != nil {
		return nil, err
	}
	networkEnabled := forceNetworkEnabled
	var profileNetwork *NetworkPermissionConfig
	if active {
		profileNetwork = profile.Network
		if !networkEnabled && profileNetwork != nil && profileNetwork.Enabled != nil {
			networkEnabled = *profileNetwork.Enabled
		}
	} else if !networkEnabled && r.SandboxMode == SandboxModeWorkspaceWrite {
		networkEnabled = r.SandboxWorkspaceWrite.EffectiveNetworkAccess()
	}
	if !networkEnabled && constraints == nil {
		return nil, nil
	}
	out := DefaultEffectiveNetworkProxyConfig()
	applyProfileNetworkToEffectiveProxy(&out, profileNetwork)
	applyFeatureNetworkToEffectiveProxy(&out, r.Features.NetworkProxy)
	out.Enabled = r.Features.NetworkProxy.EnabledValue()
	if constraints != nil {
		if err := applyNetworkConstraints(&out, *constraints, !r.DangerFullAccessEnabled()); err != nil {
			return nil, err
		}
	} else {
		out.Enabled = true
	}
	out.MITM = strings.EqualFold(out.Mode, "limited") || (out.MITMConfig != nil && len(out.MITMConfig.Hooks) > 0)
	return &out, nil
}

func applyNetworkConstraints(out *EffectiveNetworkProxyConfig, constraints NetworkConstraints, sandboxActive bool) error {
	if out == nil {
		return nil
	}
	for name, port := range map[string]*int{"http_port": constraints.HTTPPort, "socks_port": constraints.SOCKSPort} {
		if port != nil && (*port <= 0 || *port > 65535) {
			return fmt.Errorf("network constraint %s must be between 1 and 65535", name)
		}
	}
	if err := validateNetworkConstraintEntries(constraints); err != nil {
		return err
	}
	if constraints.UnixSockets != nil && constraints.DangerouslyAllowAllUnixSockets == nil && out.DangerouslyAllowAllUnixSockets {
		return fmt.Errorf("network constraint unix_sockets forbids dangerously_allow_all_unix_sockets")
	}
	if constraints.Enabled != nil {
		out.Enabled = *constraints.Enabled
	}
	if constraints.HTTPPort != nil {
		out.ProxyURL = fmt.Sprintf("http://127.0.0.1:%d", *constraints.HTTPPort)
	}
	if constraints.SOCKSPort != nil {
		out.SOCKSURL = fmt.Sprintf("http://127.0.0.1:%d", *constraints.SOCKSPort)
	}
	if constraints.AllowUpstreamProxy != nil {
		out.AllowUpstreamProxy = *constraints.AllowUpstreamProxy
	}
	if constraints.DangerouslyAllowNonLoopbackProxy != nil {
		out.DangerouslyAllowNonLoopbackProxy = *constraints.DangerouslyAllowNonLoopbackProxy
	}
	if constraints.DangerouslyAllowAllUnixSockets != nil {
		out.DangerouslyAllowAllUnixSockets = *constraints.DangerouslyAllowAllUnixSockets
	}
	managedOnly := constraints.ManagedAllowedDomainsOnly != nil && *constraints.ManagedAllowedDomainsOnly
	out.HardDenyAllowlistMisses = managedOnly
	if constraints.Domains != nil || managedOnly {
		managedAllow := networkAccessEntries(constraints.Domains, NetworkAccessAllow)
		managedDeny := networkAccessEntries(constraints.Domains, NetworkAccessDeny)
		userAllow := networkAccessEntries(out.Domains, NetworkAccessAllow)
		userDeny := networkAccessEntries(out.Domains, NetworkAccessDeny)
		allow := managedAllow
		if sandboxActive && !managedOnly {
			allow = mergeNetworkPatterns(allow, userAllow)
		}
		deny := managedDeny
		if sandboxActive {
			deny = mergeNetworkPatterns(deny, userDeny)
		}
		out.Domains = make(map[string]NetworkAccess, len(allow)+len(deny))
		for _, pattern := range allow {
			out.Domains[pattern] = NetworkAccessAllow
		}
		for _, pattern := range deny {
			out.Domains[pattern] = NetworkAccessDeny
		}
		if len(out.Domains) == 0 {
			out.Domains = nil
		}
	}
	if constraints.UnixSockets != nil {
		out.UnixSockets = make(map[string]NetworkAccess)
		for path, access := range constraints.UnixSockets {
			if access == NetworkAccessAllow {
				out.UnixSockets[path] = NetworkAccessAllow
			}
		}
		if len(out.UnixSockets) == 0 {
			out.UnixSockets = nil
		}
	}
	if constraints.AllowLocalBinding != nil {
		out.AllowLocalBinding = *constraints.AllowLocalBinding
	}
	return nil
}

func validateNetworkConstraintEntries(constraints NetworkConstraints) error {
	for pattern, access := range constraints.Domains {
		if access != NetworkAccessAllow && access != NetworkAccessDeny {
			return fmt.Errorf("invalid network constraint domain %q=%q", pattern, access)
		}
		if err := validateNetworkDomainPattern(pattern, access); err != nil {
			return fmt.Errorf("invalid network constraint domain %q: %w", pattern, err)
		}
	}
	for path, access := range constraints.UnixSockets {
		if !filepath.IsAbs(strings.TrimSpace(path)) || (access != NetworkAccessAllow && access != NetworkAccessDeny) {
			return fmt.Errorf("invalid network constraint unix socket %q=%q", path, access)
		}
	}
	return nil
}

func networkAccessEntries(entries map[string]NetworkAccess, wanted NetworkAccess) []string {
	out := make([]string, 0, len(entries))
	for pattern, access := range entries {
		if access == wanted {
			out = append(out, pattern)
		}
	}
	return out
}

func mergeNetworkPatterns(base, additions []string) []string {
	out := append([]string(nil), base...)
	for _, addition := range additions {
		found := false
		for _, current := range out {
			if strings.EqualFold(current, addition) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, addition)
		}
	}
	return out
}

func applyProfileNetworkToEffectiveProxy(out *EffectiveNetworkProxyConfig, in *NetworkPermissionConfig) {
	if out == nil || in == nil {
		return
	}
	applyNetworkProxyFields(out, in.ProxyURL, in.EnableSOCKS5, in.SOCKSURL, in.EnableSOCKS5UDP,
		in.AllowUpstreamProxy, in.DangerouslyAllowNonLoopbackProxy, in.DangerouslyAllowAllUnixSockets,
		in.Mode, in.Domains, in.UnixSockets, in.AllowLocalBinding)
	out.MITMConfig = cloneNetworkMITMConfig(in.MITM)
}

func applyFeatureNetworkToEffectiveProxy(out *EffectiveNetworkProxyConfig, in NetworkProxyFeatureConfig) {
	if out == nil {
		return
	}
	applyNetworkProxyFields(out, in.ProxyURL, in.EnableSOCKS5, in.SOCKSURL, in.EnableSOCKS5UDP,
		in.AllowUpstreamProxy, in.DangerouslyAllowNonLoopbackProxy, in.DangerouslyAllowAllUnixSockets,
		in.Mode, in.Domains, in.UnixSockets, in.AllowLocalBinding)
}

func applyNetworkProxyFields(
	out *EffectiveNetworkProxyConfig,
	proxyURL *string,
	enableSOCKS5 *bool,
	socksURL *string,
	enableSOCKS5UDP *bool,
	allowUpstreamProxy *bool,
	allowNonLoopback *bool,
	allowAllUnixSockets *bool,
	mode string,
	domains map[string]NetworkAccess,
	unixSockets map[string]NetworkAccess,
	allowLocalBinding *bool,
) {
	if proxyURL != nil {
		out.ProxyURL = *proxyURL
	}
	if enableSOCKS5 != nil {
		out.EnableSOCKS5 = *enableSOCKS5
	}
	if socksURL != nil {
		out.SOCKSURL = *socksURL
	}
	if enableSOCKS5UDP != nil {
		out.EnableSOCKS5UDP = *enableSOCKS5UDP
	}
	if allowUpstreamProxy != nil {
		out.AllowUpstreamProxy = *allowUpstreamProxy
	}
	if allowNonLoopback != nil {
		out.DangerouslyAllowNonLoopbackProxy = *allowNonLoopback
	}
	if allowAllUnixSockets != nil {
		out.DangerouslyAllowAllUnixSockets = *allowAllUnixSockets
	}
	if strings.TrimSpace(mode) != "" {
		out.Mode = strings.ToLower(strings.TrimSpace(mode))
	}
	if domains != nil {
		out.Domains = mergeNetworkAccessMap(out.Domains, normalizeNetworkDomainMap(domains))
		if len(out.Domains) == 0 {
			out.Domains = nil
		}
	}
	if unixSockets != nil {
		out.UnixSockets = mergeNetworkAccessMap(out.UnixSockets, unixSockets)
		if len(out.UnixSockets) == 0 {
			out.UnixSockets = nil
		}
	}
	if allowLocalBinding != nil {
		out.AllowLocalBinding = *allowLocalBinding
	}
}

func validateNetworkProxyFeature(c NetworkProxyFeatureConfig) error {
	mode := strings.ToLower(strings.TrimSpace(c.Mode))
	if mode != "" && mode != "limited" && mode != "full" {
		return fmt.Errorf("features.network_proxy.mode must be limited or full")
	}
	for pattern, access := range c.Domains {
		if strings.TrimSpace(pattern) == "" || (access != NetworkAccessAllow && access != NetworkAccessDeny) {
			return fmt.Errorf("invalid features.network_proxy domain %q=%q", pattern, access)
		}
		if err := validateNetworkDomainPattern(pattern, access); err != nil {
			return fmt.Errorf("invalid features.network_proxy domain %q: %w", pattern, err)
		}
	}
	for path, access := range c.UnixSockets {
		if !filepath.IsAbs(strings.TrimSpace(path)) || (access != NetworkAccessAllow && access != NetworkAccessDeny) {
			return fmt.Errorf("invalid features.network_proxy unix socket %q=%q", path, access)
		}
	}
	return nil
}

func validateNetworkDomainPattern(pattern string, access NetworkAccess) error {
	pattern = normalizeNetworkDomainPattern(pattern)
	if access == NetworkAccessDeny && isGlobalNetworkDomainPattern(pattern) {
		return fmt.Errorf("global wildcard is not supported in the denylist")
	}
	if _, err := path.Match(pattern, "example.com"); err != nil {
		return err
	}
	return nil
}

func normalizeNetworkDomainPattern(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "*" {
		return pattern
	}
	prefix := ""
	switch {
	case strings.HasPrefix(pattern, "**."):
		prefix, pattern = "**.", strings.TrimPrefix(pattern, "**.")
	case strings.HasPrefix(pattern, "*."):
		prefix, pattern = "*.", strings.TrimPrefix(pattern, "*.")
	}
	return prefix + normalizeNetworkHost(pattern)
}

func isGlobalNetworkDomainPattern(pattern string) bool {
	pattern = normalizeNetworkDomainPattern(pattern)
	return pattern == "*" || pattern == "**.*"
}
