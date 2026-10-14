package config

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
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
		{name: "overflows a duration", seconds: MaxMCPTimeoutSeconds + 1, want: "must not exceed"},
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
	for _, seconds := range []float64{0, 0.001, 30, MaxMCPTimeoutSeconds} {
		if err := ValidateMCPServers("agents.defaults.mcp_servers", []MCPServerConfig{
			{Name: "srv", Command: "/bin/x", StartupTimeout: seconds},
		}); err != nil {
			t.Fatalf("startup_timeout=%v rejected: %v", seconds, err)
		}
	}
}

func TestValidateMCPTimeoutRejectsNaN(t *testing.T) {
	// Spelled out separately because NaN is the case that a naive comparison
	// chain lets through: NaN fails every ordering test, so a validator written
	// as `seconds < 0 || seconds > max` accepts it.
	if err := ValidateMCPTimeout(math.NaN()); err == nil {
		t.Fatal("NaN was accepted")
	}
}

// marshalRootForTest renders a loaded root the way Save would, so a test can
// read what would reach the file.
func marshalRootForTest(t *testing.T, root Root) (string, error) {
	t.Helper()
	b, err := yaml.Marshal(root)
	return string(b), err
}

// writeConfigFile writes one forebrain.yaml and returns its path.
func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The entry this suite is about: the real-world streamable-http server whose
// fields the configuration has to be able to express, written the way forebrain
// spells them (seconds, no aliases).
const smartDocConfig = `
agents:
  defaults:
    mcp_servers:
      - name: smart-doc
        url: http://192.168.73.51:8112/mcp
        transport: streamable-http
        headers:
          Authorization: Bearer ${SMART_DOC_TOKEN}
        startup_timeout: 10
        request_timeout: 30
        enabled: true
`

// TestMCPEntryFieldsLoadWithForebrainSpelling pins the alignment between the
// real-world entry and forebrain's own field names and units: url, transport,
// headers, the connect bound (startup_timeout, seconds) and the per-call bound
// (request_timeout, seconds) all land on the entry.
func TestMCPEntryFieldsLoadWithForebrainSpelling(t *testing.T) {
	root, err := LoadPersisted(writeConfigFile(t, smartDocConfig))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	servers := root.Agents.Defaults.MCPServers
	if len(servers) != 1 {
		t.Fatalf("servers = %+v", servers)
	}
	srv := servers[0]
	if srv.Name != "smart-doc" || srv.URL != "http://192.168.73.51:8112/mcp" || srv.Transport != "streamable-http" {
		t.Fatalf("identity fields = %+v", srv)
	}
	if got := srv.Headers["Authorization"]; got != "Bearer ${SMART_DOC_TOKEN}" {
		t.Fatalf("headers = %+v", srv.Headers)
	}
	if srv.StartupTimeout != 10 {
		t.Fatalf("startup_timeout = %v, want 10 seconds", srv.StartupTimeout)
	}
	if srv.RequestTimeout != 30 {
		t.Fatalf("request_timeout = %v, want 30 seconds", srv.RequestTimeout)
	}
	if !ServerEnabled(srv) {
		t.Fatalf("enabled: true must read as enabled: %+v", srv.Enabled)
	}
}

// TestMCPForeignTimeoutSpellingsAreIgnored holds the "no aliases" decision: the
// spellings other clients use for the same two bounds are not part of
// forebrain's configuration. They are dropped the way every unknown key is
// (decoding is deliberately not strict), they do not fail the load, and they do
// not disturb the fields forebrain does read.
func TestMCPForeignTimeoutSpellingsAreIgnored(t *testing.T) {
	root, err := LoadPersisted(writeConfigFile(t, `
agents:
  defaults:
    mcp_servers:
      - name: smart-doc
        url: http://192.168.73.51:8112/mcp
        transport: streamable-http
        connectionTimeoutMs: 10000
        connect_timeout: 10000
        connectTimeout: 10000
        requestTimeoutMs: 30000
        startup_timeout: 10
        request_timeout: 30
`))
	if err != nil {
		t.Fatalf("foreign spellings must not fail the load: %v", err)
	}
	srv := root.Agents.Defaults.MCPServers[0]
	if srv.StartupTimeout != 10 || srv.RequestTimeout != 30 {
		t.Fatalf("forebrain's own fields must be the ones that land: %+v", srv)
	}
	out, err := marshalRootForTest(t, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, foreign := range []string{"connectionTimeoutMs", "connect_timeout", "connectTimeout", "requestTimeoutMs"} {
		if strings.Contains(out, foreign) {
			t.Fatalf("%s must not be written back: %s", foreign, out)
		}
	}
}

// TestMCPTimeoutAndEnabledValidation pins the three rules the new fields carry:
// both timeouts are real seconds values, and a server cannot be switched off and
// required at the same time.
func TestMCPTimeoutAndEnabledValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "negative request timeout",
			body: "        request_timeout: -1\n",
			want: "request_timeout must not be negative",
		},
		{
			name: "request timeout that overflows a duration",
			body: "        request_timeout: 1e10\n",
			want: "request_timeout must not exceed",
		},
		{
			name: "switched off and required",
			body: "        enabled: false\n        required: true\n",
			want: "required is true while enabled is false",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadPersisted(writeConfigFile(t, "agents:\n  defaults:\n    mcp_servers:\n      - name: x\n        command: /bin/x\n"+tc.body))
			if err == nil {
				t.Fatalf("want a load error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestMCPConfigRoundTripKeepsTheNewFields pins that a read-modify-write of the
// configuration does not quietly drop what the new fields say: an entry the user
// switched off stays switched off in the file.
func TestMCPConfigRoundTripKeepsTheNewFields(t *testing.T) {
	root, err := LoadPersisted(writeConfigFile(t, `
agents:
  defaults:
    mcp_servers:
      - name: paused
        transport: stdio
        command: /bin/paused
        enabled: false
      - name: tuned
        transport: stdio
        command: /bin/tuned
        request_timeout: 30
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	path := filepath.Join(t.TempDir(), "forebrain.yaml")
	if err := Save(path, root); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, "enabled: false") {
		t.Fatalf("enabled: false must survive a save:\n%s", text)
	}
	if !strings.Contains(text, "request_timeout: 30") {
		t.Fatalf("request_timeout must survive a save:\n%s", text)
	}
	again, err := LoadPersisted(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	servers := again.Agents.Defaults.MCPServers
	if len(servers) != 2 {
		t.Fatalf("servers = %+v", servers)
	}
	if ServerEnabled(servers[0]) {
		t.Fatalf("paused must still be switched off: %+v", servers[0])
	}
	if servers[1].RequestTimeout != 30 {
		t.Fatalf("request_timeout = %v", servers[1].RequestTimeout)
	}
}

// TestAgentDefinitionMCPServers pins the second layer of the configuration: a
// primary agent owns a list of its own, and that list gets the same
// normalization and validation the shared one does — with the error naming the
// section a reader has to fix.
func TestAgentDefinitionMCPServers(t *testing.T) {
	root, err := LoadPersisted(writeConfigFile(t, `
agents:
  definitions:
    alice:
      primary: true
      mcp_servers:
        - name: codegraph
          transport: stdio
          command: /bin/alice-cg
          default_tools_approval_mode: PROMPT
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	own := root.Agents.MCPServersFor("alice")
	if len(own) != 1 || own[0].Name != "codegraph" || own[0].Command != "/bin/alice-cg" {
		t.Fatalf("alice's own servers = %+v", own)
	}
	if own[0].DefaultToolsApprovalMode != MCPToolApprovalPrompt {
		t.Fatalf("approval mode must be normalized, got %q", own[0].DefaultToolsApprovalMode)
	}
	if got := root.Agents.MCPServersFor("ALICE"); len(got) != 1 {
		t.Fatalf("the lookup is case-insensitive: %+v", got)
	}
	for _, id := range []string{"bob", "", "main"} {
		if got := root.Agents.MCPServersFor(id); got != nil {
			t.Fatalf("an agent without a list of its own owns nothing, %q got %+v", id, got)
		}
	}

	_, err = LoadPersisted(writeConfigFile(t, `
agents:
  definitions:
    alice:
      primary: true
      mcp_servers:
        - name: codegraph
          command: /bin/x
          default_tools_approval_mode: sometimes
`))
	if err == nil {
		t.Fatal("an agent's own list must be validated too")
	}
	if !strings.Contains(err.Error(), "agents.definitions.alice.mcp_servers") {
		t.Fatalf("error must name the section to fix: %v", err)
	}
}

// TestAgentDefinitionMCPServersDoNotSerializeOnEveryAgent pins where the list
// lives: on the definition that needs it, and nowhere else.
func TestAgentDefinitionMCPServersDoNotSerializeOnEveryAgent(t *testing.T) {
	root, err := LoadPersisted(writeConfigFile(t, `
agents:
  definitions:
    alice:
      primary: true
      mcp_servers:
        - name: codegraph
          command: /bin/alice-cg
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out, err := marshalRootForTest(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "mcp_servers") {
		t.Fatalf("the agent's own list must be persisted:\n%s", out)
	}
	if n := strings.Count(out, "mcp_servers"); n != 1 {
		t.Fatalf("only the agent that has a list writes one, got %d:\n%s", n, out)
	}
}
