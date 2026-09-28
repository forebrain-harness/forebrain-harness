package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNetworkConstraintsSeedAndPinAllowlist(t *testing.T) {
	enabled := true
	managedOnly := true
	cfg := &Root{
		SandboxMode: SandboxModeReadOnly,
		Features: FeaturesSection{NetworkProxy: NetworkProxyFeatureConfig{
			Domains: map[string]NetworkAccess{"user.example.com": NetworkAccessAllow},
		}},
	}
	effective, err := cfg.ResolveNetworkProxyConfigForNetworkEnabledWithConstraints(&NetworkConstraints{
		Enabled: &enabled, ManagedAllowedDomainsOnly: &managedOnly,
		Domains: map[string]NetworkAccess{"managed.example.com": NetworkAccessAllow},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !effective.Enabled || !effective.HardDenyAllowlistMisses || len(effective.Domains) != 1 || effective.Domains["managed.example.com"] != NetworkAccessAllow {
		t.Fatalf("effective=%+v", effective)
	}
}

func TestNetworkConstraintsExistEvenWhenProxyIsDisabled(t *testing.T) {
	disabled := false
	cfg := &Root{SandboxMode: SandboxModeReadOnly}
	effective, err := cfg.ResolveNetworkProxyConfigForNetworkEnabledWithConstraints(&NetworkConstraints{Enabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if effective == nil || effective.Enabled {
		t.Fatalf("effective=%+v", effective)
	}
}

func TestNetworkConstraintsRejectEphemeralPort(t *testing.T) {
	port := 0
	cfg := &Root{SandboxMode: SandboxModeReadOnly}
	if _, err := cfg.ResolveNetworkProxyConfigForNetworkEnabledWithConstraints(&NetworkConstraints{HTTPPort: &port}); err == nil {
		t.Fatal("expected invalid port")
	}
}

func TestNetworkProxyFeatureBooleanAndObjectForms(t *testing.T) {
	for name, body := range map[string]string{
		"boolean": "features:\n  network_proxy: true\n",
		"object":  "features:\n  network_proxy:\n    enabled: true\n    enable_socks5: false\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "forebrain.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadPersisted(path)
			if err != nil {
				t.Fatal(err)
			}
			if !cfg.Features.NetworkProxy.EnabledValue() {
				t.Fatalf("feature=%+v", cfg.Features.NetworkProxy)
			}
			if name == "object" && (cfg.Features.NetworkProxy.EnableSOCKS5 == nil || *cfg.Features.NetworkProxy.EnableSOCKS5) {
				t.Fatalf("feature=%+v", cfg.Features.NetworkProxy)
			}
		})
	}
}

func TestNetworkProxyDefaultsAndActivation(t *testing.T) {
	featureEnabled := true
	networkEnabled := true
	proxyURL := "http://127.0.0.1:43128"
	disableSOCKS := false
	cfg := &Root{
		DefaultPermissions: "dev",
		Permissions: PermissionProfiles{"dev": {
			Network: &NetworkPermissionConfig{
				Enabled:      &networkEnabled,
				ProxyURL:     &proxyURL,
				EnableSOCKS5: &disableSOCKS,
				Domains:      map[string]NetworkAccess{"EXAMPLE.COM.": NetworkAccessDeny},
			},
		}},
		SandboxMode: SandboxModeWorkspaceWrite,
		Features: FeaturesSection{NetworkProxy: NetworkProxyFeatureConfig{
			Enabled: &featureEnabled,
			Domains: map[string]NetworkAccess{"example.com": NetworkAccessAllow},
		}},
	}
	proxy, err := cfg.ResolveNetworkProxyConfig()
	if err != nil {
		t.Fatal(err)
	}
	if proxy == nil || !proxy.Enabled || proxy.ProxyURL != proxyURL || proxy.EnableSOCKS5 ||
		proxy.SOCKSURL != DefaultNetworkSOCKSURL || !proxy.EnableSOCKS5UDP || !proxy.AllowUpstreamProxy ||
		proxy.Mode != "full" || proxy.Domains["example.com"] != NetworkAccessAllow {
		t.Fatalf("proxy=%+v", proxy)
	}
}

func TestNetworkProxyFeatureDoesNotEnableSandboxNetwork(t *testing.T) {
	enabled := true
	for name, cfg := range map[string]*Root{
		"read only": {
			SandboxMode: SandboxModeReadOnly,
			Features:    FeaturesSection{NetworkProxy: NetworkProxyFeatureConfig{Enabled: &enabled}},
		},
		// Workspace-write grants network access by default, so "off" has to be
		// said out loud for this case to be about a sandbox with no network.
		"workspace network off": {
			SandboxMode:           SandboxModeWorkspaceWrite,
			SandboxWorkspaceWrite: SandboxWorkspaceWrite{NetworkAccess: BoolPtr(false)},
			Features:              FeaturesSection{NetworkProxy: NetworkProxyFeatureConfig{Enabled: &enabled}},
		},
		"danger": {
			SandboxMode: SandboxModeDangerFullAccess,
			Features:    FeaturesSection{NetworkProxy: NetworkProxyFeatureConfig{Enabled: &enabled}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			proxy, err := cfg.ResolveNetworkProxyConfig()
			if err != nil || proxy != nil {
				t.Fatalf("proxy=%+v err=%v", proxy, err)
			}
		})
	}
}

// The complement of the case above: workspace-write allows network access
// unless configured otherwise, and with the proxy feature enabled that is what
// resolves a proxy. Stated here so a change to the default reads as a change to
// this expectation rather than as an unrelated test breaking.
func TestNetworkProxyResolvesWhenWorkspaceNetworkIsAllowed(t *testing.T) {
	enabled := true
	cfg := &Root{
		SandboxMode: SandboxModeWorkspaceWrite,
		Features:    FeaturesSection{NetworkProxy: NetworkProxyFeatureConfig{Enabled: &enabled}},
	}
	if !cfg.SandboxWorkspaceWrite.EffectiveNetworkAccess() {
		t.Fatal("workspace-write network access defaults to allowed")
	}
	proxy, err := cfg.ResolveNetworkProxyConfig()
	if err != nil {
		t.Fatalf("ResolveNetworkProxyConfig: %v", err)
	}
	if proxy == nil || !proxy.Enabled {
		t.Fatalf("proxy=%+v, want an enabled proxy for a network-enabled sandbox", proxy)
	}
}

func TestNetworkProxyFeatureRejectsUnknownAndInvalidFields(t *testing.T) {
	for name, body := range map[string]string{
		"unknown": "features:\n  network_proxy:\n    enabled: true\n    unexpected: true\n",
		"mode":    "features:\n  network_proxy:\n    enabled: true\n    mode: invalid\n",
		"domain":  "features:\n  network_proxy:\n    enabled: true\n    domains:\n      example.com: invalid\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "forebrain.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadPersisted(path); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestNetworkProxyFeatureJSONBooleanRoundTrip(t *testing.T) {
	enabled := true
	raw, err := json.Marshal(NetworkProxyFeatureConfig{Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "true" {
		t.Fatalf("json=%s", raw)
	}
	var decoded NetworkProxyFeatureConfig
	if err := json.Unmarshal(raw, &decoded); err != nil || !decoded.EnabledValue() {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
}
