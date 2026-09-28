package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// codex_assets.go imports the Codex-side assets (memories, global
// instructions, user-level MCP, plugin skills, input history) and extracts
// plan-mode documents from the imported assistant text (§4.10). Project-level
// configuration migration for both sources lives in assets.go.

// codexUnits expands the discovered rollouts into import units, parents
// before children (§4.4): a spawn edge makes the child rollout a child
// session of its parent thread, and a child whose parent rollout is gone is
// imported independently with a report note.
func codexUnits(data *codexData, opts *Options) []sessionUnit {
	byThread := make(map[string]*sessionUnit, len(data.Rollouts))
	units := make([]sessionUnit, 0, len(data.Rollouts))
	for _, rollout := range data.Rollouts {
		meta := data.ThreadIndex[rollout.ThreadID]
		unit := sessionUnit{
			Path:      rollout.Path,
			TargetID:  codexSessionTarget(rollout.ThreadID),
			Bytes:     rollout.Bytes,
			Title:     meta.Title,
			Cwd:       firstNonEmptyStr(meta.Cwd, rollout.Cwd),
			GitBranch: meta.GitBranch,
			FirstSeen: rollout.FirstSeen,
		}
		units = append(units, unit)
	}
	for i := range units {
		byThread[strings.TrimPrefix(units[i].TargetID, "cli-")] = &units[i]
	}
	orphans := map[string]bool{}
	for _, edge := range data.SpawnEdges {
		child, ok := byThread[strings.TrimSpace(edge.ChildThreadID)]
		if !ok {
			continue
		}
		parent, ok := byThread[strings.TrimSpace(edge.ParentThreadID)]
		if !ok {
			orphans[strings.TrimSpace(edge.ParentThreadID)] = true
			continue
		}
		child.ParentID = parent.TargetID
		child.IsChild = true
		if strings.TrimSpace(child.Cwd) == "" {
			child.Cwd = parent.Cwd
		}
	}
	if opts.OnlyProject && strings.TrimSpace(opts.CurrentProjectRoot) != "" {
		filtered := units[:0]
		for _, unit := range units {
			cwd := unit.Cwd
			if cwd == "" {
				cwd = data.ThreadIndex[strings.TrimPrefix(unit.TargetID, "cli-")].Cwd
			}
			if cwd == "" || !strings.EqualFold(memory.ProjectRoot(cwd), memory.ProjectRoot(opts.CurrentProjectRoot)) {
				continue
			}
			filtered = append(filtered, unit)
		}
		units = filtered
		// A filter can drop a child's parent (their cwds may differ); a
		// dangling parent_session_id would point at a row that never lands.
		present := make(map[string]bool, len(units))
		for _, unit := range units {
			present[unit.TargetID] = true
		}
		for i := range units {
			if units[i].ParentID != "" && !present[units[i].ParentID] {
				units[i].ParentID = ""
				units[i].IsChild = false
			}
		}
	}
	// Parents first: a child's parent_session_id must land on an existing
	// row, and any order derived from edge chains beats plain path order.
	units = orderUnitsParentsFirst(units)
	return units
}

// orderUnitsParentsFirst topologically sorts units so every parent precedes
// its children; units without parents keep their incoming (path) order.
func orderUnitsParentsFirst(units []sessionUnit) []sessionUnit {
	index := make(map[string]int, len(units))
	for i, unit := range units {
		index[unit.TargetID] = i
	}
	visited := make([]bool, len(units))
	var out []sessionUnit
	var visit func(i int)
	visit = func(i int) {
		if visited[i] {
			return
		}
		visited[i] = true
		if parentID := strings.TrimSpace(units[i].ParentID); parentID != "" {
			if j, ok := index[parentID]; ok && j != i {
				visit(j)
			}
		}
		out = append(out, units[i])
	}
	for i := range units {
		visit(i)
	}
	return out
}

// importCodexMemories writes stage1_outputs rows as scoped ad-hoc notes
// (§4.6). A memory whose thread is not in the index falls back to the global
// scope and is called out in the report — the same level-4 fallback the
// Claude side applies.
func importCodexMemories(ctx context.Context, data *codexData, opts *Options, progress func(Progress)) ([]MemoryOutcome, error) {
	out := []MemoryOutcome{}
	if len(data.Memories) == 0 {
		return out, nil
	}
	roots, rootsErr := memory.ResolveRootsForAgent(opts.AgentWorkspace)
	globalRoot := memory.Root{}
	if rootsErr == nil {
		globalRoot = roots.Scope(memory.GlobalScope())
	}
	byThread := map[string]codexThreadMeta{}
	for id, meta := range data.ThreadIndex {
		byThread[id] = meta
	}
	for _, mem := range data.Memories {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if progress != nil {
			progress(Progress{Stage: "memories", Detail: mem.ThreadID})
		}
		outcome := MemoryOutcome{ProjectDir: mem.ThreadID}
		if rootsErr != nil {
			outcome.Reason = "agent workspace unavailable; notes not written"
			outcome.Skipped = append(outcome.Skipped, mem.RolloutSlug)
			out = append(out, outcome)
			continue
		}
		resolved := byThread[mem.ThreadID].Cwd
		scope := memory.AdHocNoteScopeGlobal
		targetRoot := globalRoot.MemoryRoot
		if resolved != "" {
			if projectScope, ok := memory.ProjectScope(memory.ProjectKey(resolved)); ok {
				outcome.ResolvedPath = memory.ProjectRoot(resolved)
				outcome.Scope = "project"
				scope = memory.AdHocNoteScopeProject
				targetRoot = roots.Scope(projectScope).MemoryRoot
			}
		}
		if outcome.Scope != "project" {
			outcome.Scope = "global"
			outcome.Reason = "thread cwd unknown; note written to the global scope — move it if you can place the project"
		}
		filename, note := codexMemoryNote(mem)
		if _, err := os.Stat(filepath.Join(targetRoot, "extensions", "ad_hoc", "notes", filename)); err == nil {
			outcome.Skipped = append(outcome.Skipped, filename)
			out = append(out, outcome)
			continue
		}
		if opts.DryRun {
			outcome.Written = append(outcome.Written, filename)
			out = append(out, outcome)
			continue
		}
		backend := memory.NewScoped(targetRoot, globalRoot.MemoryRoot)
		if _, err := backend.AddAdHocNote(scope, filename, note); err != nil {
			outcome.Skipped = append(outcome.Skipped, filename)
			outcome.Reason = err.Error()
			out = append(out, outcome)
			continue
		}
		outcome.Written = append(outcome.Written, filename)
		out = append(out, outcome)
	}
	return out, nil
}

// codexMemoryNote renders one stage1 row as a note file name and body.
func codexMemoryNote(mem codexMemory) (string, string) {
	at := mem.GeneratedAt
	if at == 0 {
		at = mem.SourceUpdatedAt
	}
	if at == 0 {
		at = time.Now().Unix()
	}
	slug := sanitizeSlug(mem.RolloutSlug)
	if slug == "" {
		slug = sanitizeSlug(mem.ThreadID)
		if len(slug) > 8 {
			slug = slug[:8]
		}
	}
	if slug == "" {
		slug = "memory"
	}
	if len(slug) > 80 {
		slug = slug[:80]
	}
	filename := time.Unix(at, 0).UTC().Format("2006-01-02T15-04-05") + "-" + slug + ".md"
	var body strings.Builder
	body.WriteString("# " + slug + "\n\n")
	if summary := strings.TrimSpace(mem.RolloutSummary); summary != "" {
		body.WriteString(summary + "\n\n")
	}
	if raw := strings.TrimSpace(mem.RawMemory); raw != "" {
		body.WriteString(raw + "\n\n")
	}
	body.WriteString("---\norigin: codex memory (thread " + mem.ThreadID + ")\n")
	return filename, body.String()
}

// importCodexGlobalInstructions writes AGENTS.md as a global-scope note when
// it holds any text (§4.7; the Claude-side CLAUDE.md branch lives in
// assets.go so both sources behave the same).
func importCodexGlobalInstructions(data *codexData, opts *Options) MemoryOutcome {
	outcome := MemoryOutcome{ProjectDir: "AGENTS.md", Scope: "global"}
	if strings.TrimSpace(data.GlobalInstructions) == "" {
		outcome.Reason = "empty at the source"
		return outcome
	}
	filename, note := globalInstructionsNote("codex", "AGENTS.md", data.Root, data.GlobalInstructions, opts.now())
	outcome.ResolvedPath = "global scope"
	if opts.DryRun {
		outcome.Written = append(outcome.Written, filename)
		return outcome
	}
	roots, err := memory.ResolveRootsForAgent(opts.AgentWorkspace)
	if err != nil {
		outcome.Reason = "agent workspace unavailable; note not written"
		return outcome
	}
	globalRoot := roots.Scope(memory.GlobalScope())
	backend := memory.NewScoped(globalRoot.MemoryRoot, globalRoot.MemoryRoot)
	if _, err := backend.AddAdHocNote(memory.AdHocNoteScopeGlobal, filename, note); err != nil {
		outcome.Reason = err.Error()
		return outcome
	}
	outcome.Written = append(outcome.Written, filename)
	return outcome
}

// importCodexUserMCP appends config.toml's enabled [mcp_servers] entries to
// forebrain.yaml's global list (§4.9.4): append-only with a timestamped backup,
// same-name entries skipped, disabled entries reported but not written, no
// pre-seeded confirmations.
func importCodexUserMCP(data *codexData, opts *Options) ([]MCPOutcome, []string, error) {
	out := []MCPOutcome{}
	notices := []string{}
	if len(data.UserMCP) == 0 {
		return out, notices, nil
	}
	cfg, err := appcfg.Load(opts.ConfigPath)
	if err != nil {
		return out, notices, fmt.Errorf("load %s: %w", opts.ConfigPath, err)
	}
	changed := false
	for _, server := range data.UserMCP {
		if server.Enabled != nil && !*server.Enabled {
			out = append(out, MCPOutcome{Name: server.Name, Scope: "global", Status: "skipped", Detail: "disabled at the source"})
			continue
		}
		exists := false
		for _, existing := range cfg.Agents.Defaults.MCPServers {
			if mcp.SameServerName(existing.Name, server.Name) {
				exists = true
				break
			}
		}
		if exists {
			out = append(out, MCPOutcome{Name: server.Name, Scope: "global", Status: "skipped", Detail: "an entry with this name is already configured; not overwritten"})
			continue
		}
		cfg.Agents.Defaults.MCPServers = append(cfg.Agents.Defaults.MCPServers, codexMCPServerConfig(server))
		changed = true
		detail := describeCodexMCPServer(server)
		if len(server.Dropped) > 0 {
			detail += "; dropped (no forebrain counterpart): " + strings.Join(server.Dropped, ", ")
		}
		out = append(out, MCPOutcome{Name: server.Name, Scope: "global", Status: "written", Detail: detail})
	}
	if changed {
		if opts.DryRun {
			return out, append(notices, "written MCP entries take effect for new sessions only"), nil
		}
		if err := backupFile(opts.ConfigPath, opts.now()); err != nil {
			return out, notices, err
		}
		if err := appcfg.Save(opts.ConfigPath, cfg); err != nil {
			return out, notices, fmt.Errorf("save %s: %w", opts.ConfigPath, err)
		}
		notices = append(notices, "written MCP entries take effect for new sessions only")
	}
	return out, notices, nil
}

// codexMCPServerConfig maps one source table onto forebrain's configuration.
func codexMCPServerConfig(server codexMCPServer) appcfg.MCPServerConfig {
	transport := "stdio"
	if strings.TrimSpace(server.URL) != "" {
		transport = "streamable_http"
	}
	cfg := appcfg.MCPServerConfig{
		Name:      strings.TrimSpace(server.Name),
		Transport: transport,
		URL:       strings.TrimSpace(server.URL),
		Headers:   server.Headers,
		Command:   strings.TrimSpace(server.Command),
		Args:      server.Args,
		Env:       server.Env,
	}
	if len(server.Tools) > 0 {
		cfg.Tools = map[string]appcfg.MCPServerToolConfig{}
		names := make([]string, 0, len(server.Tools))
		for name := range server.Tools {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			table := server.Tools[name]
			entry := appcfg.MCPServerToolConfig{}
			if mode, ok := table["approval_mode"].(string); ok {
				entry.ApprovalMode = appcfg.MCPToolApprovalMode(strings.TrimSpace(mode))
			}
			cfg.Tools[name] = entry
		}
	}
	return cfg
}

func describeCodexMCPServer(server codexMCPServer) string {
	if command := strings.TrimSpace(server.Command); command != "" {
		parts := append([]string{command}, server.Args...)
		return "runs " + strings.Join(parts, " ")
	}
	if url := strings.TrimSpace(server.URL); url != "" {
		return "connects to " + url
	}
	return "no command or url"
}

// codexSystemSkillNotes reports the built-in .system skills forebrain neither
// migrates nor discovers (§4.8): they are Codex's own capabilities, and a
// user watching /skills would otherwise wonder where they went.
func codexSystemSkillNotes(root string) []SkillOutcome {
	out := []SkillOutcome{}
	dir := filepath.Join(root, "skills", ".system")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		out = append(out, SkillOutcome{
			Name:   entry.Name(),
			Source: dir,
			Status: "skipped",
			Detail: "Codex built-in system skill; not migrated (not a user asset, and forebrain does not discover this directory)",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// extractCodexPlans pulls <proposed_plan> blocks out of the imported
// assistant text and writes each as a plan document (§4.10). It reads the
// same parse the session import used — no second pass over the files.
func extractCodexPlans(parsed map[string]*parsedSession, opts *Options, progress func(Progress)) []PlanOutcome {
	out := []PlanOutcome{}
	workspace := strings.TrimSpace(opts.AgentWorkspace)
	if workspace == "" {
		return out
	}
	used := map[string]string{} // filename → owning thread, for collision suffixes
	// Deterministic order: by target id.
	targets := make([]string, 0, len(parsed))
	for target := range parsed {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	for _, target := range targets {
		session := parsed[target]
		blocks := proposedPlanBlocks(sessionText(session))
		if len(blocks) == 0 {
			continue
		}
		threadID := strings.TrimPrefix(target, "cli-")
		if progress != nil {
			progress(Progress{Stage: "plans", Detail: threadID})
		}
		slug := planFileSlug(session.Title)
		if slug == "" {
			slug = sanitizeSlug(threadID)
		}
		projectKey := ""
		if cwd := strings.TrimSpace(session.Cwd); cwd != "" {
			projectKey = memory.ProjectKey(memory.ProjectRoot(cwd))
		}
		for i, block := range blocks {
			name := slug
			if i > 0 {
				name = fmt.Sprintf("%s-%d", slug, i+1)
			}
			if owner, taken := used[name]; taken && owner != threadID {
				name = fmt.Sprintf("%s-%s", name, threadIDSuffix(threadID))
			}
			used[name] = threadID
			body := strings.TrimRight(block, "\n") + "\n\n---\norigin: codex plan mode (thread " + threadID + ")\n"
			outcome := writeExtractedPlan(workspace, projectKey, name, body, opts.DryRun)
			out = append(out, outcome)
		}
	}
	return out
}

// proposedPlanBlocks returns every paired <proposed_plan>…</proposed_plan>
// block, in order of appearance. The tags match literally: Codex's own
// prompts forbid renaming them.
func proposedPlanBlocks(text string) []string {
	const open, close = "<proposed_plan>", "</proposed_plan>"
	var out []string
	for {
		start := strings.Index(text, open)
		if start < 0 {
			return out
		}
		rest := text[start+len(open):]
		end := strings.Index(rest, close)
		if end < 0 {
			return out
		}
		out = append(out, strings.TrimSpace(rest[:end]))
		text = rest[end+len(close):]
	}
}

// sessionText concatenates a session's assistant bodies, the only place a
// plan block can live.
func sessionText(session *parsedSession) string {
	var parts []string
	for _, row := range session.Rows {
		if row.Role == "assistant" && strings.TrimSpace(row.Content) != "" {
			parts = append(parts, row.Content)
		}
	}
	return strings.Join(parts, "\n\n")
}

func threadIDSuffix(threadID string) string {
	clean := sanitizeSlug(threadID)
	if len(clean) > 8 {
		clean = clean[:8]
	}
	return clean
}

// writeExtractedPlan writes one extracted plan with the same three-state
// idempotency the file-copy importer uses: identical → up-to-date,
// differing → diverged and left alone.
func writeExtractedPlan(workspace, projectKey, name, body string, dryRun bool) PlanOutcome {
	scope := "the unscoped plans root"
	if projectKey != "" {
		scope = "project " + projectKey
	}
	destDir := state.PlanDirForProject(workspace, projectKey)
	dest := filepath.Join(destDir, name+".md")
	if existing, err := os.ReadFile(dest); err == nil {
		if planContentEqual(existing, []byte(body)) {
			return PlanOutcome{Name: name, ProjectKey: projectKey, Status: "up-to-date"}
		}
		return PlanOutcome{Name: name, ProjectKey: projectKey, Status: "diverged", Detail: "a plan with this name already differs in " + scope + "; not overwritten"}
	}
	if dryRun {
		return PlanOutcome{Name: name, ProjectKey: projectKey, Status: "installed", Detail: "planned for " + scope}
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return PlanOutcome{Name: name, ProjectKey: projectKey, Status: "skipped", Detail: err.Error()}
	}
	if err := os.WriteFile(dest, []byte(body), 0o600); err != nil {
		return PlanOutcome{Name: name, ProjectKey: projectKey, Status: "skipped", Detail: err.Error()}
	}
	return PlanOutcome{Name: name, ProjectKey: projectKey, Status: "installed", Detail: "written to " + scope}
}

// importCodexProjectConfig migrates every discovered project root's
// .codex/config.toml into that project's .forebrain/mcp_servers.yaml (§4.9) and
// reports the sections with no forebrain counterpart. A project is reported only
// when something is written into it (in a dry run, would be) or something
// about it has to be said.
func importCodexProjectConfig(data *codexData, opts *Options, progress func(Progress)) ([]MCPOutcome, []ProjectWrite) {
	out := []MCPOutcome{}
	var projects []ProjectWrite
	for _, projectPath := range data.ProjectPaths {
		configPath := filepath.Join(projectPath, ".codex", "config.toml")
		if _, err := os.Stat(configPath); err != nil {
			continue
		}
		if progress != nil {
			progress(Progress{Stage: "mcp", Detail: filepath.Base(projectPath)})
		}
		project := ProjectWrite{Path: projectPath}
		body, err := os.ReadFile(configPath)
		if err != nil {
			project.Notes = append(project.Notes, "could not read .codex/config.toml: "+err.Error())
			projects = append(projects, project)
			continue
		}
		doc, err := tool.ParseTOMLDocument(string(body))
		if err != nil {
			project.Notes = append(project.Notes, "could not read .codex/config.toml: "+err.Error())
			projects = append(projects, project)
			continue
		}
		mcpTables, _ := doc["mcp_servers"].(map[string]any)
		names := make([]string, 0, len(mcpTables))
		for name := range mcpTables {
			names = append(names, name)
		}
		sort.Strings(names)
		var outcomes []MCPOutcome
		for _, name := range names {
			table, _ := mcpTables[name].(map[string]any)
			server, ok := codexMCPServerFromTable(name, table)
			if !ok {
				continue
			}
			if server.Enabled != nil && !*server.Enabled {
				outcomes = append(outcomes, MCPOutcome{Name: name, Scope: "project", Status: "skipped", Detail: "disabled at the source"})
				continue
			}
			entry := codexMCPServerConfig(server)
			// The project file records approval modes verbatim; loading
			// clamps them later (mcp.ClampProjectRuntimePolicy), so what is
			// written stays faithful to the source.
			_, outcome := appendProjectMCPServer(projectPath, entry, opts)
			outcomes = append(outcomes, outcome)
		}
		out = append(out, outcomes...)
		project.Notes = append(project.Notes, codexNoCounterpartNotice(doc)...)
		if anyWritten(outcomes) {
			project.Unloaded = projectUnloaded(strings.TrimSpace(opts.Home), projectPath)
		} else if len(project.Notes) == 0 {
			continue
		}
		projects = append(projects, project)
	}
	return out, projects
}

// codexNoCounterpartNotice lists the config sections that have no forebrain
// project-level counterpart (§4.9.2): they are reported, never silently
// dropped.
func codexNoCounterpartNotice(doc map[string]any) []string {
	sections := []string{
		"sandbox_mode", "approval_policy", "hooks", "model", "model_reasoning_effort",
	}
	var names []string
	for _, key := range sections {
		if _, present := doc[key]; present {
			names = append(names, key)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return []string{"left out, Forebrain Harness has no project setting for them: " + strings.Join(names, ", ")}
}

// appendProjectMCPServer adds one entry to <project>/.forebrain/mcp_servers.yaml
// with the shared idempotency semantics: same name and same definition →
// up-to-date; same name, different definition → diverged, not overwritten;
// new name → written after a timestamped backup.
func appendProjectMCPServer(projectPath string, entry appcfg.MCPServerConfig, opts *Options) (bool, MCPOutcome) {
	doc, err := readProjectMCPDoc(projectPath)
	if err != nil {
		return false, MCPOutcome{Name: entry.Name, Scope: "project", Status: "skipped", Detail: err.Error()}
	}
	for _, existing := range doc.Servers {
		if mcp.SameServerName(existing.Name, entry.Name) {
			if projectServerEqual(existing, entry) {
				return false, MCPOutcome{Name: entry.Name, Scope: "project", Status: "up-to-date", Detail: "already present in " + projectFileRel}
			}
			return false, MCPOutcome{Name: entry.Name, Scope: "project", Status: "diverged", Detail: "an entry with this name already differs in " + projectFileRel + "; not overwritten"}
		}
	}
	if opts.DryRun {
		return true, MCPOutcome{Name: entry.Name, Scope: "project", Status: "written", Detail: "planned for " + projectFileRel}
	}
	doc.Servers = append(doc.Servers, entry)
	if err := writeProjectMCPDoc(projectPath, doc); err != nil {
		return false, MCPOutcome{Name: entry.Name, Scope: "project", Status: "skipped", Detail: err.Error()}
	}
	return true, MCPOutcome{Name: entry.Name, Scope: "project", Status: "written", Detail: "written to " + projectFileRel}
}

// projectServerEqual compares two project entries by their persisted
// definition (name, transport, command, args, env, url, headers, approvals).
func projectServerEqual(a, b appcfg.MCPServerConfig) bool {
	left, err := json.Marshal(normalizeProjectServerForCompare(a))
	if err != nil {
		return false
	}
	right, err := json.Marshal(normalizeProjectServerForCompare(b))
	if err != nil {
		return false
	}
	return string(left) == string(right)
}

func normalizeProjectServerForCompare(srv appcfg.MCPServerConfig) appcfg.MCPServerConfig {
	srv.Scope = ""
	srv.ProjectKey = ""
	if srv.Args == nil {
		srv.Args = []string{}
	}
	if srv.Env == nil {
		srv.Env = map[string]string{}
	}
	if srv.Headers == nil {
		srv.Headers = map[string]string{}
	}
	return srv
}

// projectUnloaded says why forebrain will not load the project files written
// into projectPath — they load only in a version-controlled project the user
// trusts — or "" when it will.
func projectUnloaded(home, projectPath string) string {
	if strings.TrimSpace(home) == "" {
		return ""
	}
	project, err := safety.Resolve(projectPath)
	if err != nil {
		return "the directory could not be inspected: " + err.Error()
	}
	if !project.VersionControlled {
		return "not a version-controlled project"
	}
	trusted, err := safety.IsTrusted(home, project)
	if err != nil {
		return "its trust could not be read: " + err.Error()
	}
	if !trusted {
		return "not trusted yet"
	}
	return ""
}
