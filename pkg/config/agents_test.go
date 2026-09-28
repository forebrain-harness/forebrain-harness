package config

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"go.yaml.in/yaml/v2"
)

// TestMCPServerStartupPolicyRoundTrips pins the two startup-policy fields as
// configuration: they survive YAML and JSON, they are omitted when unset so an
// existing file stays byte-identical, and they are independent of each other.
func TestMCPServerStartupPolicyRoundTrips(t *testing.T) {
	raw := `
name: docs
command: /bin/docs
startup_timeout: 12.5
required: true
`
	var servers []MCPServerConfig
	if err := yaml.Unmarshal([]byte("- "+strings.ReplaceAll(strings.TrimSpace(raw), "\n", "\n  ")), &servers); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("servers = %d, want 1", len(servers))
	}
	got := servers[0]
	if got.StartupTimeout != 12.5 {
		t.Fatalf("startup_timeout = %v, want 12.5", got.StartupTimeout)
	}
	if !got.Required {
		t.Fatal("required = false, want true")
	}
	out, err := yaml.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back MCPServerConfig
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if back.StartupTimeout != got.StartupTimeout || back.Required != got.Required || back.Name != got.Name || back.Command != got.Command {
		t.Fatalf("yaml round trip changed the entry:\n got=%+v\nwant=%+v", back, got)
	}
	jsonBody, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	var fromJSON MCPServerConfig
	if err := json.Unmarshal(jsonBody, &fromJSON); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if fromJSON.StartupTimeout != 12.5 || !fromJSON.Required {
		t.Fatalf("json round trip lost the policy: %+v", fromJSON)
	}

	// An entry that does not set them must not grow keys in a file the operator
	// wrote by hand: the defaults live in the resolver, not in the serialization.
	plain, err := yaml.Marshal(MCPServerConfig{Name: "plain", Command: "/bin/x"})
	if err != nil {
		t.Fatalf("marshal plain: %v", err)
	}
	if strings.Contains(string(plain), "startup_timeout") || strings.Contains(string(plain), "required") {
		t.Fatalf("unset startup policy must be omitted:\n%s", plain)
	}
	plainJSON, err := json.Marshal(MCPServerConfig{Name: "plain", Command: "/bin/x"})
	if err != nil {
		t.Fatalf("json marshal plain: %v", err)
	}
	if strings.Contains(string(plainJSON), "startup_timeout") || strings.Contains(string(plainJSON), "required") {
		t.Fatalf("unset startup policy must be omitted from json:\n%s", plainJSON)
	}
}

// TestValidateMCPServersRejectsUnusableStartupTimeout pins that a value the
// runtime cannot honor is an error at load, named with the source and the index
// of the entry that carries it — a silent substitution would hide a typo behind
// a bound the operator never chose.
func TestValidateMCPServersRejectsUnusableStartupTimeout(t *testing.T) {
	cases := []struct {
		name    string
		seconds float64
		want    string
	}{
		{name: "negative", seconds: -1, want: "must not be negative"},
		{name: "NaN", seconds: math.NaN(), want: "must be a number of seconds"},
		{name: "positive infinity", seconds: math.Inf(1), want: "must be a finite number of seconds"},
		{name: "negative infinity", seconds: math.Inf(-1), want: "must be a finite number of seconds"},
		{name: "overflows a duration", seconds: MaxMCPStartupTimeoutSeconds + 1, want: "must not exceed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMCPServers("agents.defaults.mcp_servers", []MCPServerConfig{
				{Name: "ok", Command: "/bin/x"},
				{Name: "bad", Command: "/bin/y", StartupTimeout: tc.seconds},
			})
			if err == nil {
				t.Fatal("unusable startup_timeout was accepted")
			}
			msg := err.Error()
			if !strings.HasPrefix(msg, "agents.defaults.mcp_servers[1].startup_timeout") {
				t.Fatalf("error must name the source, index and field: %q", msg)
			}
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("error %q does not say %q", msg, tc.want)
			}
		})
	}
}

// TestValidateMCPServersAcceptsTheStartupPolicyRange pins what is accepted, so
// the rejections above are the boundary and not a blanket refusal: zero means
// "use the default", and the largest representable duration itself is allowed.
func TestValidateMCPServersAcceptsTheStartupPolicyRange(t *testing.T) {
	for _, seconds := range []float64{0, 0.001, 30, MaxMCPStartupTimeoutSeconds} {
		if err := ValidateMCPServers("agents.defaults.mcp_servers", []MCPServerConfig{
			{Name: "srv", Command: "/bin/x", StartupTimeout: seconds},
		}); err != nil {
			t.Fatalf("startup_timeout=%v rejected: %v", seconds, err)
		}
	}
}

func TestValidateMCPStartupTimeoutRejectsNaN(t *testing.T) {
	// Spelled out separately because NaN is the case that a naive comparison
	// chain lets through: NaN fails every ordering test, so a validator written
	// as `seconds < 0 || seconds > max` accepts it.
	if err := ValidateMCPStartupTimeout(math.NaN()); err == nil {
		t.Fatal("NaN was accepted")
	}
}
