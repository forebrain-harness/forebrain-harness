package migrate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"go.yaml.in/yaml/v2"
)

// claudeLSPPlugin is one enabled Claude Code plugin that declares language
// servers.
type claudeLSPPlugin struct {
	Key     string                     // "<name>@<marketplace>"
	Name    string                     // the part before the "@"
	Root    string                     // absolute version directory: ${CLAUDE_PLUGIN_ROOT}
	Servers map[string]claudeLSPServer // from .lsp.json and plugin.json lspServers
	Err     string                     // why its declaration could not be read
}

// claudeLSPServer mirrors one .lsp.json entry.
type claudeLSPServer struct {
	Command               string            `json:"command"`
	Args                  []string          `json:"args"`
	Transport             string            `json:"transport"`
	Env                   map[string]string `json:"env"`
	ExtensionToLanguage   map[string]string `json:"extensionToLanguage"`
	InitializationOptions json.RawMessage   `json:"initializationOptions"`
	Settings              json.RawMessage   `json:"settings"`
	WorkspaceFolder       string            `json:"workspaceFolder"`
	StartupTimeout        int               `json:"startupTimeout"`  // ms
	ShutdownTimeout       int               `json:"shutdownTimeout"` // ms
	RestartOnCrash        *bool             `json:"restartOnCrash"`
	MaxRestarts           *int              `json:"maxRestarts"`
	Diagnostics           *bool             `json:"diagnostics"`
}

// discoverLSPPlugins lists the enabled plugins that declare language servers.
// A plugin with no declaration at all is left out (skill plugins and the
// like); a plugin whose declaration cannot be read is kept with Err set so
// the report can say so.
func discoverLSPPlugins(root string) []claudeLSPPlugin {
	enabled := enabledClaudePlugins(root)
	if len(enabled) == 0 {
		return nil
	}
	var out []claudeLSPPlugin
	for key := range enabled {
		parts := strings.SplitN(key, "@", 2)
		if len(parts) != 2 {
			continue
		}
		name, marketplace := parts[0], parts[1]
		dir := highestPluginVersionDir(filepath.Join(root, "plugins", "cache", marketplace, name))
		if dir == "" {
			continue
		}
		plugin := claudeLSPPlugin{Key: key, Name: name, Root: dir, Servers: map[string]claudeLSPServer{}}
		readPluginLSPServers(&plugin)
		if plugin.Err == "" && len(plugin.Servers) == 0 {
			continue
		}
		out = append(out, plugin)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// readPluginLSPServers folds a plugin's language-server declarations into
// plugin: .lsp.json first, then the manifest's lspServers — an inline map, a
// path to a JSON map file, or an array mixing both. A server name declared in
// both places keeps .lsp.json's definition.
func readPluginLSPServers(plugin *claudeLSPPlugin) {
	if body, err := os.ReadFile(filepath.Join(plugin.Root, ".lsp.json")); err == nil {
		servers := map[string]claudeLSPServer{}
		if err := json.Unmarshal(body, &servers); err != nil {
			plugin.Err = ".lsp.json: " + err.Error()
			return
		}
		for name, server := range servers {
			if strings.TrimSpace(name) != "" {
				plugin.Servers[name] = server
			}
		}
	} else if !os.IsNotExist(err) {
		plugin.Err = ".lsp.json: " + err.Error()
		return
	}
	body, err := os.ReadFile(filepath.Join(plugin.Root, ".claude-plugin", "plugin.json"))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		plugin.Err = ".claude-plugin/plugin.json: " + err.Error()
		return
	}
	var manifest struct {
		LSPServers json.RawMessage `json:"lspServers"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		plugin.Err = ".claude-plugin/plugin.json: " + err.Error()
		return
	}
	extra := map[string]claudeLSPServer{}
	foldLSPServerDeclarations(manifest.LSPServers, plugin, extra)
	for name, server := range extra {
		if _, fromDotLSP := plugin.Servers[name]; fromDotLSP {
			continue
		}
		plugin.Servers[name] = server
	}
}

// foldLSPServerDeclarations resolves one lspServers value into extra. A name
// repeated inside the manifest keeps the later declaration, the source's own
// order; .lsp.json keeps precedence over all of them.
func foldLSPServerDeclarations(raw json.RawMessage, plugin *claudeLSPPlugin, extra map[string]claudeLSPServer) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return
	}
	if strings.HasPrefix(trimmed, "[") {
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			setPluginLSPError(plugin, "lspServers: "+err.Error())
			return
		}
		for _, item := range items {
			foldLSPServerDeclarations(item, plugin, extra)
		}
		return
	}
	var ref string
	if err := json.Unmarshal(raw, &ref); err == nil {
		foldLSPServerFile(ref, plugin, extra)
		return
	}
	servers := map[string]claudeLSPServer{}
	if err := json.Unmarshal(raw, &servers); err != nil {
		setPluginLSPError(plugin, "lspServers: "+err.Error())
		return
	}
	for name, server := range servers {
		if strings.TrimSpace(name) != "" {
			extra[name] = server
		}
	}
}

// foldLSPServerFile reads a JSON file of server definitions the manifest
// points at. The path is relative to the plugin root and must stay inside it.
func foldLSPServerFile(ref string, plugin *claudeLSPPlugin, extra map[string]claudeLSPServer) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	if filepath.IsAbs(ref) {
		setPluginLSPError(plugin, "lspServers path escapes the plugin directory: "+ref)
		return
	}
	abs := filepath.Clean(filepath.Join(plugin.Root, ref))
	if rel, err := filepath.Rel(plugin.Root, abs); err != nil || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		setPluginLSPError(plugin, "lspServers path escapes the plugin directory: "+ref)
		return
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		setPluginLSPError(plugin, "lspServers file "+ref+": "+err.Error())
		return
	}
	servers := map[string]claudeLSPServer{}
	if err := json.Unmarshal(body, &servers); err != nil {
		setPluginLSPError(plugin, "lspServers file "+ref+": "+err.Error())
		return
	}
	for name, server := range servers {
		if strings.TrimSpace(name) != "" {
			extra[name] = server
		}
	}
}

func setPluginLSPError(plugin *claudeLSPPlugin, reason string) {
	if plugin.Err == "" {
		plugin.Err = reason
	}
}

// officialLSPMarketplace is the marketplace Claude Code's own plugins come from.
const officialLSPMarketplace = "claude-plugins-official"

// officialLSPPlugins maps Claude Code's official LSP plugins onto forebrain's
// built-in catalog ids. An empty id means forebrain's catalog has no such
// server yet; the plugin's own declaration is imported as a custom entry.
var officialLSPPlugins = map[string]string{
	"clangd-lsp":        "clangd",
	"csharp-lsp":        "csharp-ls",
	"gopls-lsp":         "gopls",
	"jdtls-lsp":         "jdtls",
	"kotlin-lsp":        "kotlin-lsp",
	"php-lsp":           "intelephense",
	"pyright-lsp":       "pyright",
	"rust-analyzer-lsp": "rust-analyzer",
	"swift-lsp":         "sourcekit-lsp",
	"typescript-lsp":    "typescript-language-server",
	"lua-lsp":           "lua-language-server",
	"ruby-lsp":          "ruby-lsp",
	"liquid-lsp":        "",
}

// LSPOutcome reports one language-server entry of an import.
type LSPOutcome struct {
	Name   string
	Status string // enabled | written | skipped
	Detail string
}

// importLSP applies the enabled plugins' language servers to forebrain.yaml:
// an official plugin whose server forebrain's catalog holds just enables that
// built-in server, every other declaration becomes a custom lsp.servers entry
// (§5.5). Nothing is copied for an entry the config already has.
func importLSP(data *claudeData, opts *Options, progress func(Progress)) ([]LSPOutcome, []string, error) {
	out := []LSPOutcome{}
	notices := []string{}
	if !opts.wants("lsp") {
		return out, notices, nil
	}
	if progress != nil {
		progress(Progress{Stage: "lsp", Detail: "language servers"})
	}
	if len(data.LSPPlugins) == 0 {
		return out, notices, nil
	}
	cfg, err := appcfg.Load(opts.ConfigPath)
	if err != nil {
		return out, notices, fmt.Errorf("load %s: %w", opts.ConfigPath, err)
	}
	if cfg.LSP.Servers == nil {
		cfg.LSP.Servers = map[string]appcfg.LSPServerConfig{}
	}
	changed := false
	for _, plugin := range data.LSPPlugins {
		if plugin.Err != "" {
			out = append(out, LSPOutcome{Name: plugin.Key, Status: "skipped", Detail: plugin.Err})
			continue
		}
		if parts := strings.SplitN(plugin.Key, "@", 2); len(parts) == 2 &&
			parts[1] == officialLSPMarketplace && officialLSPPlugins[plugin.Name] != "" {
			id := officialLSPPlugins[plugin.Name]
			if _, exists := cfg.LSP.Servers[id]; exists {
				out = append(out, LSPOutcome{Name: id, Status: "skipped", Detail: "an entry with this id is already configured; not overwritten"})
				continue
			}
			// The catalog entry carries the server's definition; only the
			// enablement is the user's to migrate.
			cfg.LSP.Servers[id] = appcfg.LSPServerConfig{Enabled: appcfg.BoolPtr(true)}
			changed = true
			out = append(out, LSPOutcome{Name: id, Status: "enabled", Detail: fmt.Sprintf("built-in %s server enabled (from %s)", id, plugin.Key)})
			continue
		}
		names := make([]string, 0, len(plugin.Servers))
		for name := range plugin.Servers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			id := lspServerID(name, plugin.Name)
			entry, reason := convertLSPServer(plugin, plugin.Servers[name])
			if reason != "" {
				out = append(out, LSPOutcome{Name: id, Status: "skipped", Detail: reason})
				continue
			}
			if _, exists := cfg.LSP.Servers[id]; exists {
				out = append(out, LSPOutcome{Name: id, Status: "skipped", Detail: "an entry with this id is already configured; not overwritten"})
				continue
			}
			if err := appcfg.ValidateLSPServers("lsp.servers", map[string]appcfg.LSPServerConfig{id: entry}); err != nil {
				out = append(out, LSPOutcome{Name: id, Status: "skipped", Detail: err.Error()})
				continue
			}
			body, err := yaml.Marshal(entry)
			if err != nil {
				return out, notices, fmt.Errorf("marshal lsp.servers.%s: %w", id, err)
			}
			if err := appcfg.RejectPlaintextConfigSecrets("forebrain.yaml", body); err != nil {
				out = append(out, LSPOutcome{Name: id, Status: "skipped",
					Detail: "its env holds a plaintext secret; add it by hand with ${ENV_NAME} from ~/.forebrain/.env"})
				continue
			}
			cfg.LSP.Servers[id] = entry
			changed = true
			extensions := make([]string, 0, len(entry.ExtensionToLanguage))
			for ext := range entry.ExtensionToLanguage {
				extensions = append(extensions, ext)
			}
			sort.Strings(extensions)
			out = append(out, LSPOutcome{Name: id, Status: "written",
				Detail: fmt.Sprintf("%s for %s (from %s)", entry.Command, strings.Join(extensions, ", "), plugin.Key)})
		}
	}
	if changed {
		if opts.DryRun {
			notices = append(notices, "enabled language servers apply to edits from the next session; run forebrain lsp doctor to check them")
			return out, notices, nil
		}
		if err := backupFile(opts.ConfigPath, opts.now()); err != nil {
			return out, notices, err
		}
		if err := appcfg.Save(opts.ConfigPath, cfg); err != nil {
			return out, notices, fmt.Errorf("save %s: %w", opts.ConfigPath, err)
		}
		notices = append(notices, "enabled language servers apply to edits from the next session; run forebrain lsp doctor to check them")
	}
	return out, notices, nil
}

// lspServerID turns a source server name into a forebrain server id: lower
// case, everything outside [a-z0-9._-] folded to "-", leading and trailing
// "-" dropped, an empty result falling back to the plugin name.
func lspServerID(name, pluginName string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	id := strings.Trim(b.String(), "-")
	if id == "" {
		return strings.ToLower(pluginName)
	}
	return id
}

// convertLSPServer maps one plugin server declaration onto forebrain's
// lsp.servers shape (§5.5): ${CLAUDE_PLUGIN_ROOT} resolves to the plugin's
// version directory, millisecond timeouts become ceil-seconds, everything
// else keeps its source meaning. reason says why the entry cannot be
// imported at all.
func convertLSPServer(plugin claudeLSPPlugin, server claudeLSPServer) (appcfg.LSPServerConfig, string) {
	if transport := strings.TrimSpace(server.Transport); transport != "" && !strings.EqualFold(transport, "stdio") {
		return appcfg.LSPServerConfig{}, "only stdio language servers are supported"
	}
	if usesUnsupportedLSPPlaceholder(server, "${CLAUDE_PROJECT_DIR}") {
		return appcfg.LSPServerConfig{}, "uses ${CLAUDE_PROJECT_DIR}, which forebrain does not provide"
	}
	if usesUnsupportedLSPPlaceholder(server, "${user_config.") {
		return appcfg.LSPServerConfig{}, "uses plugin user settings, which forebrain does not provide"
	}
	if strings.TrimSpace(server.Command) == "" || len(server.ExtensionToLanguage) == 0 {
		return appcfg.LSPServerConfig{}, "the declaration has no command or extensionToLanguage"
	}
	entry := appcfg.LSPServerConfig{
		Enabled:         appcfg.BoolPtr(true),
		Command:         strings.ReplaceAll(server.Command, "${CLAUDE_PLUGIN_ROOT}", plugin.Root),
		WorkspaceFolder: strings.ReplaceAll(server.WorkspaceFolder, "${CLAUDE_PLUGIN_ROOT}", plugin.Root),
	}
	if len(server.Args) > 0 {
		entry.Args = make([]string, len(server.Args))
		for i, arg := range server.Args {
			entry.Args[i] = strings.ReplaceAll(arg, "${CLAUDE_PLUGIN_ROOT}", plugin.Root)
		}
	}
	if len(server.Env) > 0 {
		entry.Env = make(map[string]string, len(server.Env))
		for key, value := range server.Env {
			entry.Env[key] = strings.ReplaceAll(value, "${CLAUDE_PLUGIN_ROOT}", plugin.Root)
		}
	}
	if len(server.ExtensionToLanguage) > 0 {
		entry.ExtensionToLanguage = make(map[string]string, len(server.ExtensionToLanguage))
		for ext, language := range server.ExtensionToLanguage {
			entry.ExtensionToLanguage[strings.ToLower(ext)] = language
		}
	}
	if len(server.InitializationOptions) > 0 {
		entry.InitializationOptions = appcfg.LSPJSONObject(append([]byte(nil), server.InitializationOptions...))
	}
	if len(server.Settings) > 0 {
		entry.Settings = appcfg.LSPJSONObject(append([]byte(nil), server.Settings...))
	}
	entry.StartupTimeout = (server.StartupTimeout + 999) / 1000
	entry.ShutdownTimeout = (server.ShutdownTimeout + 999) / 1000
	entry.RestartOnCrash = server.RestartOnCrash
	if server.MaxRestarts != nil {
		entry.MaxRestarts = *server.MaxRestarts
	}
	entry.Diagnostics = server.Diagnostics
	return entry, ""
}

// usesUnsupportedLSPPlaceholder reports whether a declaration references a
// variable forebrain cannot supply, in any string it would copy.
func usesUnsupportedLSPPlaceholder(server claudeLSPServer, marker string) bool {
	if strings.Contains(server.Command, marker) || strings.Contains(server.WorkspaceFolder, marker) {
		return true
	}
	for _, arg := range server.Args {
		if strings.Contains(arg, marker) {
			return true
		}
	}
	for _, value := range server.Env {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}
