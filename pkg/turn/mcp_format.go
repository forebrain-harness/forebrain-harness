package turn

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
)

// MCPRuntimeView is what a surface knows about the MCP servers it actually
// started: one record per configured server, in configuration order.
//
// It exists because the configured list and the runtime list answer different
// questions. "Configured but not connected" is not a state of the
// configuration — it is what a startup in flight, a failure and a skip look
// like — and a count that could only say "connected or not" presented all three
// as the same absence.
type MCPRuntimeView struct {
	// Servers is the effective list in configuration order.
	Servers []mcp.ServerRecord
	// Generation names the startup these records belong to, empty when the
	// surface has no generation to name.
	Generation string
	// Pending reports whether that startup is still running.
	Pending bool
}

// MCPCountLine is the /status MCP row: per-state counts over the /mcp
// inventory, non-zero states only. It is computed from the inventory itself
// (MCPInventory.Counts), so /status and /mcp can never disagree.
type MCPCountLine struct {
	Total      int
	Connected  int
	Connecting int
	Failed     int
	NeedsAuth  int
	Idle       int // connections an idle runner released; they come back on use
	Skipped    int // the operator Esc-skipped the startup
	Disabled   int // left out of this session by the agent's disable store
	Unknown    int
}

// Render joins the non-zero counts: "2 connected, 1 failed". An empty list
// renders empty — the status row is omitted, never "0 servers".
func (l MCPCountLine) Render() string {
	parts := make([]string, 0, 8)
	add := func(n int, label string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, label))
		}
	}
	add(l.Connected, "connected")
	add(l.Connecting, "connecting")
	add(l.Failed, "failed")
	add(l.NeedsAuth, "needs authentication")
	add(l.Idle, "idle")
	add(l.Skipped, "skipped")
	add(l.Disabled, "disabled")
	add(l.Unknown, "unknown")
	return strings.Join(parts, ", ")
}

// MCPStatusKey is the semantic state one server is in, as the panel renders it.
type MCPStatusKey string

const (
	MCPStatusConnected  MCPStatusKey = "connected"
	MCPStatusFailed     MCPStatusKey = "failed"
	MCPStatusConnecting MCPStatusKey = "connecting"
	MCPStatusNeedsAuth  MCPStatusKey = "needs-auth"
	MCPStatusIdle       MCPStatusKey = "idle"
	MCPStatusSkipped    MCPStatusKey = "skipped"
	// MCPStatusDisabled is a server the disable store kept out of this
	// session: it was never started.
	MCPStatusDisabled MCPStatusKey = "disabled"
	MCPStatusUnknown  MCPStatusKey = "unknown"
)

// MCPToolParam is one row of a tool's parameter table: name, type, required
// flag, description, enum values and default — never the raw schema JSON.
// Nested object properties become children, rendered indented.
type MCPToolParam struct {
	Name        string         `json:"name"`
	Type        string         `json:"type,omitempty"`
	Description string         `json:"description,omitempty"`
	Required    bool           `json:"required,omitempty"`
	Enum        []string       `json:"enum,omitempty"`
	Default     string         `json:"default,omitempty"`
	Children    []MCPToolParam `json:"children,omitempty"`
}

// MCPToolSummary is one tool a server exposes to this session's model.
type MCPToolSummary struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Params      []MCPToolParam `json:"params,omitempty"`
}

// MCPServerEntry is one server as the /mcp panel shows it: identity, live
// state, what the next session will do with it, and the actions that make
// sense for that state.
type MCPServerEntry struct {
	Name   string
	Status MCPStatusKey
	// Running reports that the server is in this session's frozen list; a
	// server the disable store kept out is not.
	Running bool
	// DisabledNext reports that a new session will leave the server out. On a
	// running server it is a change still to come; on one that is not running
	// it is the choice this session was assembled with.
	DisabledNext bool
	ToolCount    int
	// Error is the server's own failure text, verbatim and complete — the
	// only copy of the reason the operator can reach from the panel.
	Error      string
	Scope      string // "project" | "global"
	Source     string // the config file the entry came from
	Transport  string
	Command    string // stdio target, joined with args
	URL        string // http target, userinfo and query stripped
	EnvKeys    []string
	HeaderKeys []string
	// Auth describes the credential story: "none" when no OAuth is configured,
	// otherwise the runtime's auth status ("needs-auth", "authenticated", …).
	Auth     string
	Required bool
	Tools    []MCPToolSummary
	// CanAuth reports that the server has OAuth configured.
	CanAuth bool
	// Resources reports a live connection whose server declared resources:
	// the resource list can be asked for right now.
	Resources bool
}

// MCPScopeGroup is one section of the /mcp list: the servers that came from
// one configuration source.
type MCPScopeGroup struct {
	Label   string // "Project MCPs" | "Global MCPs"
	Source  string // the config file's path
	Entries []MCPServerEntry
}

// MCPInventory is the whole /mcp panel: the session's servers grouped by
// scope, plus the project-scope notes that explain what is deliberately not
// running.
type MCPInventory struct {
	Groups       []MCPScopeGroup
	Total        int
	ErrorLogPath string
	// GlobalSource and ProjectSource name the two files servers are
	// configured in, so an empty list can say where to add one.
	GlobalSource  string
	ProjectSource string
	// NotInEffect lists project entries that exist on disk but did not join the
	// frozen list, one sentence each.
	NotInEffect []string
	// OverriddenGlobal names global entries the project list replaced.
	OverriddenGlobal []string
	// PendingChanges reports on-disk drift: the change lands in a new session.
	PendingChanges bool
	// ForeignConfig names another agent's project MCP file when one sits in the
	// repository, with the /migrate pointer.
	ForeignConfig string
	// DisableStoreError is the disable store's own read error, verbatim, when
	// the next-session marks could not be read.
	DisableStoreError string
}

// Entry finds one server by name.
func (inv *MCPInventory) Entry(name string) *MCPServerEntry {
	if inv == nil {
		return nil
	}
	for gi := range inv.Groups {
		for i := range inv.Groups[gi].Entries {
			if mcp.SameServerName(inv.Groups[gi].Entries[i].Name, name) {
				return &inv.Groups[gi].Entries[i]
			}
		}
	}
	return nil
}

// Counts is the /status MCP row over this inventory.
func (inv MCPInventory) Counts() MCPCountLine {
	var l MCPCountLine
	for _, group := range inv.Groups {
		for _, e := range group.Entries {
			l.Total++
			switch e.Status {
			case MCPStatusConnected:
				l.Connected++
			case MCPStatusConnecting:
				l.Connecting++
			case MCPStatusFailed:
				l.Failed++
			case MCPStatusNeedsAuth:
				l.NeedsAuth++
			case MCPStatusIdle:
				l.Idle++
			case MCPStatusSkipped:
				l.Skipped++
			case MCPStatusDisabled:
				l.Disabled++
			default:
				l.Unknown++
			}
		}
	}
	return l
}

// MCPInventorySource carries what BuildMCPInventory reads. Servers is the
// session's frozen effective list; Disabled holds the entries the disable
// store kept out of it when the session was assembled, shown so the operator
// can see — and reverse — the choice.
type MCPInventorySource struct {
	Servers  []appcfg.MCPServerConfig
	Disabled []appcfg.MCPServerConfig
	// DisabledNext reports whether a new session would leave a server out; nil
	// means nothing is marked.
	DisabledNext func(appcfg.MCPServerConfig) bool
	// DisableStoreError is the store's read error, shown verbatim.
	DisableStoreError string
	Runtime           *MCPRuntimeView
	Scope             mcp.ProjectMCPScopeSummary
	Tools             []*llm.Tool
	// ResourceServers names the servers with a live connection that declared
	// resources.
	ResourceServers map[string]bool
	ProjectSource   string
	GlobalSource    string
	ErrorLogPath    string
}

// AttributeMCPTools splits the session's frozen tool table by server using the
// mcp__<NormalizeName(server)>__<tool> naming contract, keyed by the server's
// configured name. A tool goes to the server whose prefix is the longest match,
// so a server whose normalized name itself contains "__" is never shadowed by
// a shorter one. A tool no prefix attributes is not an MCP tool and is
// dropped: a non-MCP tool in a server's list would be a lie about what that
// server provides. Tools keep the table's order.
func AttributeMCPTools(servers []appcfg.MCPServerConfig, tools []*llm.Tool) map[string][]MCPToolSummary {
	type prefix struct {
		server string
		text   string
	}
	prefixes := make([]prefix, 0, len(servers))
	for _, srv := range servers {
		name := strings.TrimSpace(srv.Name)
		if norm := mcp.NormalizeName(name); norm != "" {
			prefixes = append(prefixes, prefix{server: name, text: "mcp__" + norm + "__"})
		}
	}
	byServer := make(map[string][]MCPToolSummary)
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		name := tool.Name()
		best := -1
		for i, p := range prefixes {
			if strings.HasPrefix(name, p.text) && len(name) > len(p.text) && (best < 0 || len(p.text) > len(prefixes[best].text)) {
				best = i
			}
		}
		if best < 0 {
			continue
		}
		summary := MCPToolSummary{
			Name:        strings.TrimPrefix(name, prefixes[best].text),
			Description: strings.TrimSpace(tool.Description()),
			Params:      mcpToolParams(tool.InputSchema()),
		}
		byServer[prefixes[best].server] = append(byServer[prefixes[best].server], summary)
	}
	return byServer
}

// mcpToolParams flattens a tool's JSON schema into a display table. Nested
// object properties become children so depth survives without showing JSON.
func mcpToolParams(schema map[string]any) []MCPToolParam {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	return namedParams(props, requiredSet(schema))
}

func requiredSet(schema map[string]any) map[string]bool {
	required := map[string]bool{}
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			if name, ok := r.(string); ok {
				required[name] = true
			}
		}
	}
	return required
}

// namedParams renders a properties object as rows: required parameters
// first, then the rest, each group by name — a stable order, never the
// schema map's iteration order.
func namedParams(props map[string]any, required map[string]bool) []MCPToolParam {
	out := make([]MCPToolParam, 0, len(props))
	for name, raw := range props {
		schema, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		param := MCPToolParam{
			Name:        name,
			Type:        schemaText(schema["type"]),
			Description: strings.TrimSpace(schemaText(schema["description"])),
			Required:    required[name],
		}
		if items, ok := schema["enum"].([]any); ok {
			for _, e := range items {
				param.Enum = append(param.Enum, schemaText(e))
			}
		}
		if d := schema["default"]; d != nil {
			param.Default = schemaText(d)
		}
		if childProps, ok := schema["properties"].(map[string]any); ok && len(childProps) > 0 {
			param.Children = namedParams(childProps, requiredSet(schema))
		}
		out = append(out, param)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Required != out[j].Required {
			return out[i].Required
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// schemaText renders a schema scalar; a list (a union "type", say) is joined.
func schemaText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, schemaText(item))
		}
		return strings.Join(parts, " | ")
	default:
		return fmt.Sprint(t)
	}
}

// BuildMCPInventory assembles the /mcp panel projection. The frozen effective
// list is grouped by scope in configuration order; entries the disable store
// kept out of the session trail their scope group so the choice stays visible.
func BuildMCPInventory(src MCPInventorySource) MCPInventory {
	inv := MCPInventory{
		ErrorLogPath:      strings.TrimSpace(src.ErrorLogPath),
		GlobalSource:      strings.TrimSpace(src.GlobalSource),
		ProjectSource:     strings.TrimSpace(src.ProjectSource),
		DisableStoreError: strings.TrimSpace(src.DisableStoreError),
	}
	toolsByServer := AttributeMCPTools(src.Servers, src.Tools)
	records := map[string]mcp.ServerRecord{}
	if src.Runtime != nil {
		for _, rec := range src.Runtime.Servers {
			records[strings.ToLower(strings.TrimSpace(rec.Name))] = rec
		}
	}
	add := func(srv appcfg.MCPServerConfig, running bool) {
		label, source := "Global MCPs", src.GlobalSource
		if mcp.IsProjectScope(srv) {
			label, source = "Project MCPs", src.ProjectSource
		}
		entry := buildMCPServerEntry(src, srv, records, toolsByServer, running, source)
		inv.Total++
		for i := range inv.Groups {
			if inv.Groups[i].Label == label {
				inv.Groups[i].Entries = append(inv.Groups[i].Entries, entry)
				return
			}
		}
		inv.Groups = append(inv.Groups, MCPScopeGroup{Label: label, Source: source, Entries: []MCPServerEntry{entry}})
	}
	for _, srv := range src.Servers {
		add(srv, true)
	}
	for _, srv := range src.Disabled {
		add(srv, false)
	}

	scope := src.Scope
	for _, item := range scope.NotApplied {
		label := strings.TrimSpace(item.Name)
		if label == "" {
			label = "(unnamed)"
		}
		inv.NotInEffect = append(inv.NotInEffect, label+" — "+strings.TrimSpace(item.Reason))
	}
	inv.OverriddenGlobal = append(inv.OverriddenGlobal, scope.OverriddenGlobal...)
	inv.PendingChanges = scope.PendingReload
	if root := strings.TrimSpace(scope.ProjectRoot); root != "" {
		inv.ForeignConfig = foreignProjectMCPConfig(root)
	}
	return inv
}

func buildMCPServerEntry(src MCPInventorySource, srv appcfg.MCPServerConfig, records map[string]mcp.ServerRecord, toolsByServer map[string][]MCPToolSummary, running bool, sourcePath string) MCPServerEntry {
	name := strings.TrimSpace(srv.Name)
	entry := MCPServerEntry{
		Name:      name,
		Running:   running,
		Scope:     "global",
		Source:    sourcePath,
		Transport: strings.TrimSpace(srv.Transport),
		Required:  srv.Required,
		CanAuth:   mcp.OAuthConfiguredPublic(srv.OAuth),
		Auth:      string(mcp.AuthStatusNone),
		URL:       SanitizeEndpoint(srv.URL),
	}
	if mcp.IsProjectScope(srv) {
		entry.Scope = "project"
	}
	if entry.Transport == "" {
		if strings.TrimSpace(srv.URL) != "" {
			entry.Transport = "streamable_http"
		} else {
			entry.Transport = "stdio"
		}
	}
	if cmd := strings.TrimSpace(srv.Command); cmd != "" {
		entry.Command = strings.Join(append([]string{cmd}, srv.Args...), " ")
	}
	for key := range srv.Env {
		entry.EnvKeys = append(entry.EnvKeys, key)
	}
	sort.Strings(entry.EnvKeys)
	for key := range srv.Headers {
		entry.HeaderKeys = append(entry.HeaderKeys, key)
	}
	sort.Strings(entry.HeaderKeys)
	if src.DisabledNext != nil {
		entry.DisabledNext = src.DisabledNext(srv)
	}
	if !running {
		// Never started: nothing live to report.
		entry.Status = MCPStatusDisabled
		return entry
	}
	entry.Tools = toolsByServer[name]
	entry.ToolCount = len(entry.Tools)
	entry.Resources = src.ResourceServers[name]
	rec, ok := records[strings.ToLower(name)]
	if !ok {
		entry.Status = MCPStatusUnknown
		return entry
	}
	switch rec.ConnStatus {
	case mcp.ConnStatusConnecting:
		entry.Status = MCPStatusConnecting
	case mcp.ConnStatusConnected:
		entry.Status = MCPStatusConnected
	case mcp.ConnStatusError:
		entry.Status = MCPStatusFailed
		entry.Error = strings.TrimSpace(rec.Error)
	case mcp.ConnStatusCancelled:
		entry.Status = MCPStatusSkipped
	case mcp.ConnStatusDisconnected:
		entry.Status = MCPStatusIdle
	default:
		entry.Status = MCPStatusUnknown
	}
	// Auth is the credential story. The runtime records every connected
	// server as authenticated, which for a server with no OAuth only says the
	// connection was accepted; such a server reads "none" unless it asked for
	// credentials after all.
	switch {
	case rec.AuthStatus == mcp.AuthStatusNeedsAuth:
		// Auth is the state a reader can act on, and a connected but
		// unauthenticated server still gives the model nothing.
		if entry.Status == MCPStatusConnected {
			entry.Status = MCPStatusNeedsAuth
		}
		entry.Auth = string(mcp.AuthStatusNeedsAuth)
	case rec.AuthStatus == mcp.AuthStatusFailed:
		entry.Auth = string(mcp.AuthStatusFailed)
	case entry.CanAuth && rec.AuthStatus != "":
		entry.Auth = string(rec.AuthStatus)
	}
	return entry
}

// MCPServerStatusLabel is one server's state as both surfaces word it: the
// live state for a running server, with what the next session will do when
// that differs from this one.
func MCPServerStatusLabel(e MCPServerEntry) string {
	var label string
	switch e.Status {
	case MCPStatusConnected:
		label = "✓ connected"
		if e.ToolCount > 0 {
			label += " · " + countNoun(e.ToolCount, "tool")
		}
	case MCPStatusNeedsAuth:
		label = "△ needs authentication"
	case MCPStatusFailed:
		label = "✗ failed"
	case MCPStatusConnecting:
		label = "connecting…"
	case MCPStatusIdle:
		label = "○ idle (reconnects on use)"
	case MCPStatusSkipped:
		label = "– skipped"
	case MCPStatusDisabled:
		label = "○ disabled"
		if !e.DisabledNext {
			return label + " · enabled from next session"
		}
		return label
	default:
		label = "unknown"
	}
	if e.DisabledNext {
		label += " · disabled from next session"
	}
	return label
}

// RenderMCPInventoryMarkdown renders the inventory for the web surface and for
// non-TTY replies: scope groups, live states, the project notes.
func RenderMCPInventoryMarkdown(inv MCPInventory) string {
	if inv.Total == 0 && len(inv.NotInEffect) == 0 && !inv.PendingChanges {
		return strings.TrimSpace("No MCP servers configured\n\n" + mcpConfigLocations(inv))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "MCP servers · %s\n", countNoun(inv.Total, "server"))
	for _, group := range inv.Groups {
		fmt.Fprintf(&b, "\n%s (%s)\n", group.Label, group.Source)
		for _, e := range group.Entries {
			b.WriteString("- " + e.Name + " · " + MCPServerStatusLabel(e) + " · " + e.Transport + "\n")
			if e.Status == MCPStatusFailed && e.Error != "" {
				b.WriteString("  Error: " + e.Error + "\n")
			}
		}
	}
	if len(inv.OverriddenGlobal) > 0 {
		b.WriteString("\nGlobal entries overridden by this project: " + strings.Join(inv.OverriddenGlobal, ", ") + "\n")
	}
	for _, note := range inv.NotInEffect {
		b.WriteString("\nNot in effect: " + note + "\n")
	}
	if inv.PendingChanges {
		b.WriteString("\nMCP config changes on disk take effect in a new session\n")
	}
	if inv.DisableStoreError != "" {
		b.WriteString("\nDisable store: " + inv.DisableStoreError + "\n")
	}
	if inv.ForeignConfig != "" {
		b.WriteString("\nDetected another agent's project MCP config (" + inv.ForeignConfig + ") in this repository; /migrate can import it into .forebrain/mcp_servers.yaml\n")
	}
	if inv.ErrorLogPath != "" {
		b.WriteString("\nError logs: " + inv.ErrorLogPath + "\n")
	}
	return strings.TrimSpace(b.String())
}

// mcpConfigLocations says where servers are configured.
func mcpConfigLocations(inv MCPInventory) string {
	var lines []string
	if inv.GlobalSource != "" {
		lines = append(lines, "Global servers live in "+inv.GlobalSource)
	}
	if inv.ProjectSource != "" {
		lines = append(lines, "Project servers live in "+inv.ProjectSource)
	}
	return strings.Join(lines, "\n")
}

// SetMCPServerDisabled is the one Disable/Enable action both surfaces run: it
// finds the named server among the session's servers — the running list and
// the ones the store kept out — and records the choice for the next session.
// The reply is the sentence the surface shows.
func SetMCPServerDisabled(workspaceRoot string, running, disabled []appcfg.MCPServerConfig, name string, disable bool) (string, error) {
	for _, list := range [][]appcfg.MCPServerConfig{running, disabled} {
		for _, srv := range list {
			if !mcp.SameServerName(srv.Name, name) {
				continue
			}
			if err := mcp.SetServerDisabled(workspaceRoot, srv, disable); err != nil {
				return "", err
			}
			if disable {
				return strings.TrimSpace(srv.Name) + ": disabled from next session", nil
			}
			return strings.TrimSpace(srv.Name) + ": enabled from next session", nil
		}
	}
	return "", fmt.Errorf("mcp: no server named %q in this session", strings.TrimSpace(name))
}

// ProjectMCPSource names the project MCP file a session's project entries come
// from; empty when the session has no project.
func ProjectMCPSource(projectRoot string) string {
	if paths := mcp.ProjectMCPPaths(projectRoot); len(paths) > 0 {
		return paths[0]
	}
	return ""
}

// GlobalMCPSource names the config file the global MCP entries were read
// from: the first file the loaded configuration came from.
func GlobalMCPSource(cfg *appcfg.Root) string {
	if cfg == nil || len(cfg.SourceFiles) == 0 {
		return ""
	}
	return cfg.SourceFiles[0]
}

// StatusConfigFiles lists the configuration files a session is running on:
// the files its configuration was loaded from, then the project MCP file when
// the project has one.
func StatusConfigFiles(cfg *appcfg.Root, projectRoot string) []string {
	var files []string
	if cfg != nil {
		files = append(files, cfg.SourceFiles...)
	}
	if p := ProjectMCPSource(projectRoot); p != "" {
		if _, err := os.Stat(p); err == nil {
			files = append(files, p)
		}
	}
	return files
}

// foreignProjectMCPConfig names the first other-agent project MCP file under
// root, or "" when none is present.
func foreignProjectMCPConfig(root string) string {
	for _, rel := range []string{".mcp.json", filepath.Join(".codex", "config.toml")} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			return rel
		}
	}
	return ""
}
