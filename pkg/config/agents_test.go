package config

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
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

// A primary agent's subagent configuration is configuration: its own switch,
// an override of a public built-in and a custom subagent all load, and each
// field the subagent page edits reads back unchanged.
func TestLoadReadsPrimaryAgentSubagents(t *testing.T) {
	root, err := LoadPersisted(writeConfigFile(t, `
agents:
  defaults:
    enable_subagent: true
  definitions:
    main:
      primary: true
      enable_subagent: false
      subagents:
        - name: explore
          model: deepseek/deepseek-v4-flash
          reasoning_effort: low
          system_prompt: Search only the files the task names.
          enabled: true
        - name: repo-scout
          description: Finds the files a task touches
          system_prompt: You are repo-scout.
          tools: pick
          allowed_tools: [read_file, code_search]
          continuable: false
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	def := root.Agents.Definitions["main"]
	if def.EnableSubagent == nil || *def.EnableSubagent {
		t.Fatalf("enable_subagent = %v, want false", def.EnableSubagent)
	}
	if len(def.Subagents) != 2 {
		t.Fatalf("subagents = %d entries, want 2: %+v", len(def.Subagents), def.Subagents)
	}
	override := def.Subagents[0]
	if override.Name != "explore" || override.Model != "deepseek/deepseek-v4-flash" || override.ReasoningEffort != "low" {
		t.Fatalf("built-in override read back wrong: %+v", override)
	}
	if override.SystemPrompt != "Search only the files the task names." {
		t.Fatalf("override system_prompt = %q", override.SystemPrompt)
	}
	if override.Enabled == nil || !*override.Enabled {
		t.Fatalf("override enabled = %v, want true", override.Enabled)
	}
	custom := def.Subagents[1]
	if custom.Name != "repo-scout" || custom.Description != "Finds the files a task touches" || custom.SystemPrompt != "You are repo-scout." {
		t.Fatalf("custom subagent read back wrong: %+v", custom)
	}
	if custom.Tools != SubagentToolsPick || !reflect.DeepEqual(custom.AllowedTools, []string{"read_file", "code_search"}) {
		t.Fatalf("custom tool policy read back wrong: %+v", custom)
	}
	if custom.Continuable == nil || *custom.Continuable {
		t.Fatalf("continuable = %v, want false", custom.Continuable)
	}
}

// The switch is two-level: an agent that never set its own follows the global
// one, and an agent that did set it is decided by its own value alone.
func TestSubagentsEnabledForFollowsTheGlobalSwitchUntilTheAgentSetsItsOwn(t *testing.T) {
	off, on := false, true
	cfg := &Root{}
	cfg.Agents.Defaults.EnableSubagent = &on
	if !SubagentsEnabledFor(cfg, "main") {
		t.Fatal("an agent with no switch of its own must follow the global switch")
	}
	if SubagentsEnabledFor(nil, "main") || SubagentsEnabled(nil) {
		t.Fatal("no configuration means subagents are off")
	}
	cfg.Agents.Definitions = map[string]AgentDefinition{"main": {Primary: true, EnableSubagent: &off}}
	if SubagentsEnabledFor(cfg, " MAIN ") {
		t.Fatal("the agent's own false must win over the global true")
	}
	cfg.Agents.Defaults.EnableSubagent = &off
	cfg.Agents.Definitions["main"] = AgentDefinition{Primary: true, EnableSubagent: &on}
	if !SubagentsEnabledFor(cfg, "main") {
		t.Fatal("the agent's own true must win over the global false")
	}
}

// Each rule the subagent configuration enforces, one rejection each. The bug
// this guards: an entry that names a built-in cannot carry a description or a
// tool policy, because the built-in's tools are fixed and its description is
// what the model already knows the name by.
func TestLoadRejectsInvalidAgentSubagents(t *testing.T) {
	cases := []struct {
		name  string
		entry string
		want  string
	}{
		{
			name:  "invalid name",
			entry: "        - name: Repo_Scout\n          description: d\n          system_prompt: p\n",
			want:  "invalid name",
		},
		{
			name:  "duplicate name",
			entry: "        - name: scout\n          description: d\n          system_prompt: p\n        - name: scout\n          description: d\n          system_prompt: p\n",
			want:  "duplicate subagent name",
		},
		{
			name:  "system subagent",
			entry: "        - name: guardian\n          system_prompt: p\n",
			want:  "cannot be configured",
		},
		{
			name:  "built-in with a description",
			entry: "        - name: explore\n          description: d\n",
			want:  "is a built-in",
		},
		{
			name:  "built-in with a tool policy",
			entry: "        - name: explore\n          tools: read_only\n",
			want:  "is a built-in",
		},
		{
			name:  "custom without a description",
			entry: "        - name: scout\n          system_prompt: p\n",
			want:  "description required",
		},
		{
			name:  "custom without a system prompt",
			entry: "        - name: scout\n          description: d\n",
			want:  "system_prompt required",
		},
		{
			name:  "unknown tools mode",
			entry: "        - name: scout\n          description: d\n          system_prompt: p\n          tools: everything\n",
			want:  "must be read_only, edit or pick",
		},
		{
			name:  "pick without tools",
			entry: "        - name: scout\n          description: d\n          system_prompt: p\n          tools: pick\n",
			want:  "allowed_tools requires",
		},
		{
			name:  "pick with a subagent tool",
			entry: "        - name: scout\n          description: d\n          system_prompt: p\n          tools: pick\n          allowed_tools: [read_file, subagent_run]\n",
			want:  "cannot dispatch subagents",
		},
		{
			name:  "model without a provider",
			entry: "        - name: explore\n          model: deepseek-v4-flash\n",
			want:  "must be provider/model",
		},
		{
			name:  "reasoning effort without a model",
			entry: "        - name: explore\n          reasoning_effort: low\n",
			want:  "reasoning_effort requires model",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, "agents:\n  definitions:\n    main:\n      primary: true\n      subagents:\n"+tc.entry)
			_, err := LoadPersisted(path)
			if err == nil {
				t.Fatalf("LoadPersisted accepted an invalid subagent entry:\n%s", tc.entry)
			}
			if !strings.Contains(err.Error(), "agents.definitions.main.subagents") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name the entry and %q", err, tc.want)
			}
		})
	}
}

// The loader's name lists decide whether an entry overrides a built-in or
// declares a custom subagent, so they must be the definitions' own lists. A
// built-in missing from the copy would silently become a configurable custom
// name; add a new built-in to both places.
func TestBuiltInSubagentNameCopiesMatchAgentPackage(t *testing.T) {
	if !reflect.DeepEqual(builtInPublicSubagentNames, agent.PublicSubagentNames) {
		t.Fatalf("builtInPublicSubagentNames = %#v, agent.PublicSubagentNames = %#v", builtInPublicSubagentNames, agent.PublicSubagentNames)
	}
	if !reflect.DeepEqual(builtInSystemSubagentNames, agent.SystemSubagentNames) {
		t.Fatalf("builtInSystemSubagentNames = %#v, agent.SystemSubagentNames = %#v", builtInSystemSubagentNames, agent.SystemSubagentNames)
	}
}
