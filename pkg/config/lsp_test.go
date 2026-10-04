package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	yamlv2 "go.yaml.in/yaml/v2"
)

// LSP is on unless the user turns it off: the code intelligence tools freeze
// on the resolved value, and the default keeps them available.
func TestEffectiveFeaturesLSPDefaultsOn(t *testing.T) {
	var nilRoot *Root
	if !nilRoot.EffectiveFeatures().LSP {
		t.Fatal("a nil config must read lsp as on")
	}
	if !(&Root{}).EffectiveFeatures().LSP {
		t.Fatal("an unset features.lsp must read as on")
	}
	off := false
	cfg := &Root{Features: FeaturesSection{LSP: &off}}
	if cfg.EffectiveFeatures().LSP {
		t.Fatal("features.lsp: false must read as off")
	}
}

func TestEffectiveLSPDefaults(t *testing.T) {
	want := EffectiveLSP{
		Recommendations: true,
		MaxServers:      6,
		IdleTimeout:     600 * time.Second,
		RequestTimeout:  30 * time.Second,
		AfterEdit:       true,
		Wait:            2500 * time.Millisecond,
		MinSeverity:     "warning",
		MaxPerFile:      20,
		MaxFiles:        10,
		LateDelivery:    true,
	}
	var nilRoot *Root
	if got := nilRoot.EffectiveLSP(); got != want {
		t.Fatalf("nil root effective lsp = %+v, want %+v", got, want)
	}
	if got := (&Root{}).EffectiveLSP(); got != want {
		t.Fatalf("empty root effective lsp = %+v, want %+v", got, want)
	}
}

// wait_ms is a pointer so an explicit 0 ("no wait window, everything arrives
// as a late diagnostic") stays distinguishable from an unset value.
func TestEffectiveLSPWaitZeroMeansNoWait(t *testing.T) {
	zero := 0
	cfg := &Root{LSP: LSPSection{Diagnostics: LSPDiagnosticsConfig{WaitMS: &zero}}}
	if got := cfg.EffectiveLSP().Wait; got != 0 {
		t.Fatalf("explicit wait_ms 0 = %v, want no wait", got)
	}
	if got := (&Root{}).EffectiveLSP().Wait; got != 2500*time.Millisecond {
		t.Fatalf("unset wait_ms = %v, want 2500ms", got)
	}
}

func TestLSPJSONObjectYAML(t *testing.T) {
	var obj LSPJSONObject
	if err := yamlv2.Unmarshal([]byte("gopls:\n  staticcheck: true\nnested:\n  key: value\n"), &obj); err != nil {
		t.Fatalf("yaml map unmarshal: %v", err)
	}
	if !json.Valid(obj) || !strings.Contains(string(obj), "staticcheck") {
		t.Fatalf("object json = %s", obj)
	}

	var fromString LSPJSONObject
	if err := yamlv2.Unmarshal([]byte("' {\"gopls\":{\"staticcheck\":true}}'\n"), &fromString); err != nil {
		t.Fatalf("yaml string unmarshal: %v", err)
	}
	if string(fromString) != `{"gopls":{"staticcheck":true}}` {
		t.Fatalf("yaml string object = %s", fromString)
	}

	for _, raw := range []string{"'not-json'\n", "'[1]'\n", "- 1\n"} {
		var p LSPJSONObject
		if err := yamlv2.Unmarshal([]byte(raw), &p); err == nil {
			t.Fatalf("yaml unmarshal error = nil for %q", raw)
		}
	}

	var nilObj LSPJSONObject
	if err := yamlv2.Unmarshal([]byte("null\n"), &nilObj); err != nil {
		t.Fatalf("yaml null unmarshal: %v", err)
	}
	if nilObj != nil {
		t.Fatalf("null object = %s", nilObj)
	}
	if b, err := LSPJSONObject(nil).MarshalJSON(); err != nil || string(b) != "null" {
		t.Fatalf("nil MarshalJSON = %q err=%v", b, err)
	}
	if y, err := (LSPJSONObject)([]byte(`{"gopls":{"staticcheck":true},"items":[{"y":2}]}`)).MarshalYAML(); err != nil {
		t.Fatalf("MarshalYAML nested: %v", err)
	} else if m, ok := y.(map[string]interface{}); !ok || m["gopls"] == nil || m["items"] == nil {
		t.Fatalf("MarshalYAML nested output = %#v", y)
	}
	if _, err := (LSPJSONObject)([]byte(`not-json`)).MarshalYAML(); err == nil {
		t.Fatal("MarshalYAML invalid json error = nil")
	}

	var holder struct {
		Settings LSPJSONObject `json:"settings"`
	}
	if err := json.Unmarshal([]byte(`{"settings":{"gopls":{"staticcheck":true}}}`), &holder); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if !json.Valid(holder.Settings) || !strings.Contains(string(holder.Settings), "staticcheck") {
		t.Fatalf("settings json = %s", holder.Settings)
	}
	out, err := json.Marshal(&holder)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	if !strings.Contains(string(out), `"settings":{"gopls":{"staticcheck":true}}`) {
		t.Fatalf("json round trip = %s", out)
	}
	if err := json.Unmarshal([]byte(`{"settings":[1]}`), &holder); err == nil {
		t.Fatal("json array settings error = nil")
	}
}

func TestLSPSectionLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", dir)
	path := filepath.Join(dir, "forebrain.yaml")
	raw := []byte(`
features:
  lsp: false
lsp:
  diagnostics:
    wait_ms: 0
  servers:
    gopls:
      settings:
        gopls:
          staticcheck: true
    mylang:
      command: /opt/mylang/bin/mylang-ls
      args:
        - --stdio
      extension_to_language:
        .ml2: mylang
      root_markers:
        - mylang.toml
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	// An explicit wait_ms: 0 is a choice, not a gap: saving must keep it
	// instead of swapping in the default window.
	if reloaded.LSP.Diagnostics.WaitMS == nil || *reloaded.LSP.Diagnostics.WaitMS != 0 {
		t.Fatalf("round-tripped wait_ms = %v, want explicit 0", reloaded.LSP.Diagnostics.WaitMS)
	}
	if reloaded.Features.LSP == nil || *reloaded.Features.LSP {
		t.Fatalf("round-tripped features.lsp = %v, want the explicit false", reloaded.Features.LSP)
	}

	var got, want interface{}
	if err := json.Unmarshal(reloaded.LSP.Servers["gopls"].Settings, &got); err != nil {
		t.Fatalf("gopls settings json: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"gopls":{"staticcheck":true}}`), &want); err != nil {
		t.Fatalf("want settings json: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gopls settings = %s, want %s", reloaded.LSP.Servers["gopls"].Settings, `{"gopls":{"staticcheck":true}}`)
	}

	custom := reloaded.LSP.Servers["mylang"]
	if custom.Command != "/opt/mylang/bin/mylang-ls" {
		t.Fatalf("custom command = %q", custom.Command)
	}
	if len(custom.Args) != 1 || custom.Args[0] != "--stdio" {
		t.Fatalf("custom args = %#v", custom.Args)
	}
	if custom.ExtensionToLanguage[".ml2"] != "mylang" {
		t.Fatalf("custom extensions = %#v", custom.ExtensionToLanguage)
	}
	if len(custom.RootMarkers) != 1 || custom.RootMarkers[0] != "mylang.toml" {
		t.Fatalf("custom root markers = %#v", custom.RootMarkers)
	}
}

func TestValidateLSPSectionRejects(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", dir)
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "max servers too large", raw: "lsp:\n  max_servers: 33\n", want: "lsp.max_servers"},
		{name: "idle timeout too small", raw: "lsp:\n  idle_timeout: 5\n", want: "lsp.idle_timeout"},
		{name: "wait ms too large", raw: "lsp:\n  diagnostics:\n    wait_ms: 30001\n", want: "wait_ms"},
		{name: "unknown severity", raw: "lsp:\n  diagnostics:\n    min_severity: fatal\n", want: "min_severity"},
		{name: "server id punctuation", raw: "lsp:\n  servers:\n    \"Go!\":\n      command: x\n", want: "server id must match"},
		{name: "extension without dot", raw: "lsp:\n  servers:\n    mylang:\n      command: x\n      extension_to_language:\n        go: mylang\n", want: "extension_to_language"},
		{name: "unknown role", raw: "lsp:\n  servers:\n    mylang:\n      command: x\n      role: helper\n", want: "role must be primary or diagnostics"},
		{name: "startup timeout too large", raw: "lsp:\n  servers:\n    mylang:\n      command: x\n      startup_timeout: 601\n", want: "startup_timeout"},
		{name: "command with newline", raw: "lsp:\n  servers:\n    mylang:\n      command: \"echo\\nrm -rf\"\n", want: "command must not contain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, "forebrain.yaml")
			if err := os.WriteFile(path, []byte(tt.raw), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatalf("load error = nil for %q", tt.raw)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("load error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestNormalizeLSPSection(t *testing.T) {
	s := LSPSection{
		Diagnostics: LSPDiagnosticsConfig{MinSeverity: " Warning "},
		Servers: map[string]LSPServerConfig{
			" GoPLS ": {
				Command:             "  gopls  ",
				WorkspaceFolder:     " /ws  ",
				Role:                " Diagnostics ",
				ExtensionToLanguage: map[string]string{".GO": " Go "},
			},
		},
	}
	normalizeLSPSection(&s)
	server, ok := s.Servers["gopls"]
	if !ok || len(s.Servers) != 1 {
		t.Fatalf("normalized servers = %#v", s.Servers)
	}
	if server.Command != "gopls" || server.WorkspaceFolder != "/ws" || server.Role != "diagnostics" {
		t.Fatalf("normalized server = %#v", server)
	}
	if server.ExtensionToLanguage[".go"] != "Go" || len(server.ExtensionToLanguage) != 1 {
		t.Fatalf("normalized extensions = %#v", server.ExtensionToLanguage)
	}
	if s.Diagnostics.MinSeverity != "warning" {
		t.Fatalf("normalized min severity = %q", s.Diagnostics.MinSeverity)
	}
}

func TestSaveMaterializesLSPDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", dir)
	path := filepath.Join(dir, "forebrain.yaml")
	if err := Save(path, Root{}); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(raw)
	for _, want := range []string{"lsp:", "recommendations: true", "wait_ms: 2500", "lsp: true"} {
		if !strings.Contains(text, want) {
			t.Fatalf("persisted config missing %q:\n%s", want, text)
		}
	}
}
