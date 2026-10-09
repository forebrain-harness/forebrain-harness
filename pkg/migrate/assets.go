package migrate

import (
	"bytes"
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
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"go.yaml.in/yaml/v2"
)

// MemoryOutcome reports one source memory directory's import.
type MemoryOutcome struct {
	ProjectDir   string // encoded source directory
	ResolvedPath string // real project path, "" when it fell back to global
	Scope        string // "project" | "global"
	Written      []string
	Skipped      []string
	Reason       string
}

// SkillOutcome reports one imported skill.
type SkillOutcome struct {
	Name   string
	Source string
	Status string // installed | up-to-date | diverged | native | skipped
	Detail string
}

// MCPOutcome reports one MCP entry.
type MCPOutcome struct {
	Name   string
	Scope  string // global | project
	Status string // written | skipped | native | pending-confirm
	Detail string
}

// HistoryOutcome reports the input-history merge.
type HistoryOutcome struct {
	SourceLines int
	Added       int
	Skipped     int
}

// resolveProjectPath applies the four-level resolution the plan mandates
// (B5): the source's own projects map first, then any cwd recorded in the
// transcripts, then filesystem-proven candidates of the lossy encoded name,
// and finally "" meaning the memories fall back to the global scope.
func resolveProjectPath(project claudeProject, claudeJSONPaths []string) string {
	for _, candidate := range claudeJSONPaths {
		if encodeProjectPath(candidate) == project.Encoded {
			if dirExists(candidate) {
				return candidate
			}
			return candidate
		}
	}
	if cwd := cwdFromSessions(project); cwd != "" {
		return cwd
	}
	if hit := existingDashCandidate(project.Encoded); hit != "" {
		return hit
	}
	return ""
}

// encodeProjectPath mirrors the source's directory naming: every path
// separator becomes a dash.
func encodeProjectPath(path string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' {
			return '-'
		}
		return r
	}, filepath.ToSlash(strings.TrimSpace(path)))
}

// cwdFromSessions returns the first cwd a transcript in this directory
// recorded; discovery already scanned each file's head, so this is a lookup.
func cwdFromSessions(project claudeProject) string {
	for _, session := range project.Sessions {
		if session.Cwd != "" {
			return session.Cwd
		}
	}
	return ""
}

// existingDashCandidate enumerates every '/'-vs-'-' reading of the encoded
// name and returns the one that exists as a directory, preferring the
// deepest match when several do.
func existingDashCandidate(encoded string) string {
	if encoded == "" {
		return ""
	}
	trimmed := strings.TrimPrefix(encoded, "-")
	parts := strings.Split(trimmed, "-")
	if len(parts) < 2 {
		return ""
	}
	var best string
	var walk func(index int, built string)
	walk = func(index int, built string) {
		if index == len(parts) {
			if dirExists(built) && strings.Count(built, "/") > strings.Count(best, "/") {
				best = built
			}
			return
		}
		next := parts[index]
		if built == "" {
			walk(index+1, "/"+next)
			return
		}
		walk(index+1, built+"/"+next)
		walk(index+1, built+"-"+next)
	}
	walk(0, "")
	if best == "" {
		return ""
	}
	return memory.ProjectRoot(best)
}

func dirExists(path string) bool {
	info, err := os.Stat(strings.TrimSpace(path))
	return err == nil && info.IsDir()
}

// importMemories writes every source memory note through the scoped note
// store, so path joining, symlink defense and duplicate refusal are the same
// ones the memories_* tools use.
func importMemories(ctx context.Context, data *claudeData, opts *Options, claudeJSONPaths []string, progress func(Progress)) ([]MemoryOutcome, error) {
	out := []MemoryOutcome{}
	roots, rootsErr := memory.ResolveRootsForAgent(opts.AgentWorkspace)
	globalRoot := memory.Root{}
	if rootsErr == nil {
		globalRoot = roots.Scope(memory.GlobalScope())
	}
	for _, project := range data.Projects {
		if len(project.Memories) == 0 {
			continue
		}
		if opts.OnlyProject && opts.CurrentProjectRoot != "" {
			resolved := resolveProjectPath(project, claudeJSONPaths)
			if resolved == "" || !strings.EqualFold(memory.ProjectRoot(resolved), memory.ProjectRoot(opts.CurrentProjectRoot)) {
				continue
			}
		}
		if progress != nil {
			progress(Progress{Stage: "memories", Detail: project.Encoded})
		}
		outcome := MemoryOutcome{ProjectDir: project.Encoded}
		resolved := resolveProjectPath(project, claudeJSONPaths)
		scope := memory.AdHocNoteScopeGlobal
		var backend memory.Backend
		haveBackend := false
		if rootsErr != nil {
			outcome.Reason = "agent workspace unavailable; notes not written"
			outcome.Skipped = append(outcome.Skipped, memoryNames(project.Memories)...)
			out = append(out, outcome)
			continue
		}
		if resolved != "" {
			if projectScope, ok := memory.ProjectScope(memory.ProjectKey(resolved)); ok {
				outcome.ResolvedPath = memory.ProjectRoot(resolved)
				outcome.Scope = "project"
				scope = memory.AdHocNoteScopeProject
				root := roots.Scope(projectScope)
				backend = memory.NewScoped(root.MemoryRoot, globalRoot.MemoryRoot)
				haveBackend = true
			}
		}
		if !haveBackend {
			outcome.Scope = "global"
			backend = memory.NewScoped("", globalRoot.MemoryRoot)
			outcome.Reason = "project path unresolved; notes written to the global scope — move them from there if you can place the project"
		}
		for _, file := range project.Memories {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			filename, exists := planMemoryNote(roots, globalRoot, resolved, file, opts.now())
			if exists {
				outcome.Skipped = append(outcome.Skipped, filename)
				continue
			}
			if opts.DryRun {
				outcome.Written = append(outcome.Written, filename)
				continue
			}
			name, err := importMemoryNote(backend, scope, file, opts.now())
			if err != nil {
				if strings.Contains(err.Error(), "already exists") {
					outcome.Skipped = append(outcome.Skipped, name)
					continue
				}
				outcome.Skipped = append(outcome.Skipped, filepath.Base(file))
				continue
			}
			outcome.Written = append(outcome.Written, name)
		}
		out = append(out, outcome)
	}
	return out, nil
}

// planMemoryNote computes the target filename of one source note and reports
// whether the note store already holds it, without writing. Dry runs use it
// so their counts describe exactly what a real run would write.
func planMemoryNote(roots memory.Roots, globalRoot memory.Root, resolved string, file string, now time.Time) (string, bool) {
	front, _ := splitFrontmatter(readFileOrEmpty(file))
	name := strings.TrimSpace(front["name"])
	if name == "" {
		name = sanitizeSlug(strings.TrimSuffix(filepath.Base(file), ".md"))
	}
	modified := parseModified(front["modified"])
	if modified.IsZero() {
		if info, err := os.Stat(file); err == nil {
			modified = info.ModTime()
		} else {
			modified = now
		}
	}
	filename := modified.UTC().Format("2006-01-02T15-04-05") + "-" + slugForNote(name) + ".md"
	targetDir := globalRoot.MemoryRoot
	if resolved != "" {
		if scope, ok := memory.ProjectScope(memory.ProjectKey(resolved)); ok {
			targetDir = roots.Scope(scope).MemoryRoot
		}
	}
	if _, err := os.Stat(filepath.Join(targetDir, "extensions", "ad_hoc", "notes", filename)); err == nil {
		return filename, true
	}
	return filename, false
}

func readFileOrEmpty(path string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(body)
}

func memoryNames(files []string) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		out = append(out, filepath.Base(file))
	}
	return out
}

// importMemoryNote converts one source memory file into an ad-hoc note and
// returns the stored filename.
func importMemoryNote(backend memory.Backend, scope memory.AddAdHocNoteScope, file string, now time.Time) (string, error) {
	body, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	front, noteBody := splitFrontmatter(string(body))
	name := strings.TrimSpace(front["name"])
	if name == "" {
		name = sanitizeSlug(strings.TrimSuffix(filepath.Base(file), ".md"))
	}
	description := strings.TrimSpace(front["description"])
	modified := parseModified(front["modified"])
	if modified.IsZero() {
		if info, err := os.Stat(file); err == nil {
			modified = info.ModTime()
		} else {
			modified = now
		}
	}
	filename := modified.UTC().Format("2006-01-02T15-04-05") + "-" + slugForNote(name) + ".md"
	var builder strings.Builder
	builder.WriteString("# " + name + "\n\n")
	if description != "" {
		builder.WriteString(description + "\n\n")
	}
	if trimmed := strings.TrimSpace(noteBody); trimmed != "" {
		builder.WriteString(trimmed + "\n\n")
	}
	origin := strings.TrimSpace(front["originSessionId"])
	suffix := ""
	if origin != "" {
		suffix = " (session " + origin + ")"
	}
	builder.WriteString("---\norigin: claude-code memory " + filepath.Base(file) + suffix + "\n")
	path, err := backend.AddAdHocNote(scope, filename, builder.String())
	if err != nil {
		return filename, err
	}
	_ = path
	return filename, nil
}

// splitFrontmatter separates a YAML front matter block from the note body.
func splitFrontmatter(body string) (map[string]string, string) {
	lines := strings.Split(body, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return map[string]string{}, body
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			front := map[string]string{}
			for _, line := range lines[1:i] {
				key, value, found := strings.Cut(line, ":")
				if !found {
					continue
				}
				front[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
			}
			return front, strings.Join(lines[i+1:], "\n")
		}
	}
	return map[string]string{}, body
}

func parseModified(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if ts, err := time.Parse(layout, value); err == nil {
			return ts
		}
	}
	if millis, err := parseInt64(value); err == nil && millis > 0 {
		return time.UnixMilli(millis)
	}
	return time.Time{}
}

func parseInt64(value string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(value, "%d", &n)
	return n, err
}

func slugForNote(name string) string {
	slug := sanitizeSlug(name)
	if slug == "" {
		slug = "memory"
	}
	if len(slug) > 80 {
		slug = slug[:80]
	}
	return slug
}

// sanitizeSlug lowercases and reduces to the note-store's alphabet.
func sanitizeSlug(value string) string {
	var builder strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && builder.Len() > 0 {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}

// importSkills copies enabled-plugin skills into the user skill root.
// Repo-level and user-level source skill directories need no copy: forebrain
// discovers them natively, and duplicating them would create drift.
func importSkills(data *claudeData, opts *Options, progress func(Progress)) []SkillOutcome {
	return importSkillsFrom(data.PluginSkills, opts, progress, nativeSkillNotes)
}

// importSkillsFrom is the source-agnostic skill importer: identical
// candidates, and the trailing notes differ per source (Claude lists the
// natively discovered directories; Codex lists its built-in .system skills).
func importSkillsFrom(candidates []claudePluginSkill, opts *Options, progress func(Progress), notes func(*Options) []SkillOutcome) []SkillOutcome {
	out := []SkillOutcome{}
	destRoot := filepath.Join(strings.TrimSpace(opts.Home), "skills")
	for _, candidate := range candidates {
		if progress != nil {
			progress(Progress{Stage: "skills", Detail: candidate.Name})
		}
		dest := filepath.Join(destRoot, candidate.Name)
		sourceDigest, err := skill.DirectoryDigest(candidate.Dir)
		if err != nil {
			out = append(out, SkillOutcome{Name: candidate.Name, Source: candidate.Dir, Status: "skipped", Detail: "source unreadable: " + err.Error()})
			continue
		}
		if _, err := os.Stat(dest); err == nil {
			destDigest, err := skill.DirectoryDigest(dest)
			if err != nil {
				out = append(out, SkillOutcome{Name: candidate.Name, Source: candidate.Dir, Status: "diverged", Detail: "installed copy unreadable"})
				continue
			}
			if destDigest == sourceDigest {
				out = append(out, SkillOutcome{Name: candidate.Name, Source: candidate.Dir, Status: "up-to-date"})
			} else {
				out = append(out, SkillOutcome{Name: candidate.Name, Source: candidate.Dir, Status: "diverged", Detail: "installed copy differs; not overwritten"})
			}
			continue
		}
		if opts.DryRun {
			out = append(out, SkillOutcome{Name: candidate.Name, Source: candidate.Dir, Status: "installed", Detail: "planned; skill changes take effect in new sessions only"})
			continue
		}
		// The destination root must exist before copyDir runs: its path guard
		// is a no-op when asked to create exactly its own root, so a fresh
		// install whose ~/.forebrain/skills has not been bootstrapped yet would
		// otherwise fail with a bare mkdir error.
		if err := os.MkdirAll(destRoot, 0o755); err != nil {
			out = append(out, SkillOutcome{Name: candidate.Name, Source: candidate.Dir, Status: "skipped", Detail: err.Error()})
			continue
		}
		if err := skill.InstallFromDir(candidate.Dir, destRoot, candidate.Name); err != nil {
			out = append(out, SkillOutcome{Name: candidate.Name, Source: candidate.Dir, Status: "skipped", Detail: err.Error()})
			continue
		}
		out = append(out, SkillOutcome{Name: candidate.Name, Source: candidate.Dir, Status: "installed"})
	}
	if notes != nil {
		out = append(out, notes(opts)...)
	}
	return out
}

// nativeSkillNotes lists source skill directories forebrain already discovers
// on its own, so the report explains why nothing was copied for them.
func nativeSkillNotes(opts *Options) []SkillOutcome {
	out := []SkillOutcome{}
	roots := [][]string{}
	if root := strings.TrimSpace(opts.CurrentProjectRoot); root != "" {
		roots = append(roots, []string{root, ".claude", "skills"})
	}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, []string{home, ".claude", "skills"})
	}
	for _, parts := range roots {
		dir := filepath.Join(parts...)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, entry.Name(), "SKILL.md")); err != nil {
				continue
			}
			out = append(out, SkillOutcome{
				Name: entry.Name(), Source: dir, Status: "native",
				Detail: "already discovered by forebrain; no copy made",
			})
		}
	}
	return out
}

// importMCP migrates Claude Code's MCP servers: the user-level ones into
// forebrain.yaml, and every project's — its repository .mcp.json and its
// machine-private ~/.claude.json entries — into that project's own
// .forebrain/mcp_servers.yaml. Entries the source never approved are left
// pending rather than pre-confirmed (B7).
func importMCP(data *claudeData, opts *Options, progress func(Progress)) ([]MCPOutcome, []string, []ProjectWrite, error) {
	out := []MCPOutcome{}
	notices := []string{}
	if !opts.wants("mcp") {
		return out, notices, nil, nil
	}
	if progress != nil {
		progress(Progress{Stage: "mcp", Detail: "user-level servers"})
	}
	outcomes, globalNotices, err := importUserMCP(data, opts)
	out = append(out, outcomes...)
	notices = append(notices, globalNotices...)
	if err != nil {
		return out, notices, nil, err
	}
	projectOutcomes, projects := importProjectMCP(data, opts, progress)
	out = append(out, projectOutcomes...)
	return out, notices, projects, nil
}

func importUserMCP(data *claudeData, opts *Options) ([]MCPOutcome, []string, error) {
	out := []MCPOutcome{}
	notices := []string{}
	if len(data.UserMCP) == 0 {
		return out, notices, nil
	}
	cfg, err := appcfg.Load(opts.ConfigPath)
	if err != nil {
		return out, notices, fmt.Errorf("load %s: %w", opts.ConfigPath, err)
	}
	names := make([]string, 0, len(data.UserMCP))
	for name := range data.UserMCP {
		names = append(names, name)
	}
	sort.Strings(names)
	changed := false
	for _, name := range names {
		server := data.UserMCP[name]
		exists := false
		for _, existing := range cfg.Agents.Defaults.MCPServers {
			if mcp.SameServerName(existing.Name, name) {
				exists = true
				break
			}
		}
		if exists {
			out = append(out, MCPOutcome{Name: name, Scope: "global", Status: "skipped", Detail: "an entry with this name is already configured; not overwritten"})
			continue
		}
		cfg.Agents.Defaults.MCPServers = append(cfg.Agents.Defaults.MCPServers, convertMCPServer(name, server))
		changed = true
		out = append(out, MCPOutcome{Name: name, Scope: "global", Status: "written", Detail: describeMCPServer(server)})
	}
	if changed {
		if opts.DryRun {
			notices = append(notices, "written MCP entries take effect for new sessions only")
			return out, notices, nil
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

// convertMCPServer maps a source entry onto forebrain's configuration shape.
// Env values are copied verbatim; the report never prints them.
func convertMCPServer(name string, server rawMCPServer) appcfg.MCPServerConfig {
	transport := strings.ToLower(strings.TrimSpace(server.Type))
	switch transport {
	case "http":
		transport = "streamable_http"
	case "sse", "stdio", "ws", "streamable_http":
		// already forebrain spellings
	default:
		if strings.TrimSpace(server.URL) != "" {
			transport = "streamable_http"
		} else {
			transport = "stdio"
		}
	}
	return appcfg.MCPServerConfig{
		Name:      strings.TrimSpace(name),
		Transport: transport,
		URL:       strings.TrimSpace(server.URL),
		Headers:   server.Headers,
		Command:   strings.TrimSpace(server.Command),
		Args:      server.Args,
		Env:       server.Env,
	}
}

func describeMCPServer(server rawMCPServer) string {
	if command := strings.TrimSpace(server.Command); command != "" {
		parts := append([]string{command}, server.Args...)
		return "runs " + strings.Join(parts, " ")
	}
	if url := strings.TrimSpace(server.URL); url != "" {
		return "connects to " + url
	}
	return "no command or url"
}

type projectMCPDoc struct {
	Servers []appcfg.MCPServerConfig `yaml:"mcp_servers" json:"mcp_servers"`
}

// importProjectMCP migrates every Claude-side project-level configuration
// into forebrain's own project files (§4.9, the 2026-09-17 decision): the
// repository .mcp.json is copied into .forebrain/mcp_servers.yaml (forebrain no
// longer reads .mcp.json), the machine-private ~/.claude.json project
// entries land in the same file, and .claude/settings*.json permissions
// become .forebrain/safety.json rules. Approval states follow the source's
// explicit lists only (B7); enableAllProjectMcpServers never becomes a
// blanket allow. A project is reported only when something is written into
// it (in a dry run, would be).
func importProjectMCP(data *claudeData, opts *Options, progress func(Progress)) ([]MCPOutcome, []ProjectWrite) {
	out := []MCPOutcome{}
	var projects []ProjectWrite
	for _, projectPath := range claudeProjectPaths(data, opts) {
		if !dirExists(projectPath) {
			continue
		}
		if progress != nil {
			progress(Progress{Stage: "mcp", Detail: filepath.Base(projectPath)})
		}
		project := ProjectWrite{Path: projectPath}
		written := false

		// 1. Repository .mcp.json: team-shared entries, migrated not read.
		outcomes, fileNotes := importProjectMCPJSONFile(projectPath, data, opts)
		out = append(out, outcomes...)
		project.Notes = append(project.Notes, fileNotes...)
		written = written || anyWritten(outcomes)

		// 2. Machine-private ~/.claude.json project entries: same target
		// file, flagged because they become repository files.
		servers := data.ProjectMCP[projectPath]
		names := make([]string, 0, len(servers))
		for name := range servers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			entry := convertMCPServer(name, servers[name])
			_, outcome := appendProjectMCPServer(projectPath, entry, opts)
			if outcome.Status == "written" {
				written = true
				project.Notes = append(project.Notes,
					"'"+name+"' was private to this machine in Claude Code and becomes a file in the repository; add .forebrain/mcp_servers.yaml to .gitignore to keep it out of commits")
			}
			out = append(out, outcome)
		}

		// 3. .claude/settings*.json permissions → .forebrain/safety.json.
		added, ruleNotes := importClaudeProjectPermissions(projectPath, opts)
		project.Notes = append(project.Notes, ruleNotes...)
		written = written || added > 0

		// 4. Approval states, recorded only after the file exists on disk
		// (the fingerprint must match what will actually load). The blanket
		// flag may live in settings.local.json, not only ~/.claude.json.
		if err := presetEnabledConsents(opts, projectPath, data.EnabledMCPJSON[projectPath]); err != nil {
			project.Notes = append(project.Notes, "could not carry over the MCP approvals: "+err.Error())
		}
		if written && (data.EnableAllProjectMCP[projectPath] || settingsEnableAllProjectMCP(projectPath)) {
			project.Notes = append(project.Notes,
				"approving every project server at once (enableAllProjectMcpServers) is not carried over; each server still asks once")
		}
		if !written && len(project.Notes) == 0 {
			continue
		}
		if written {
			project.Unloaded = projectUnloaded(strings.TrimSpace(opts.Home), projectPath)
		}
		projects = append(projects, project)
	}
	return out, projects
}

// anyWritten reports whether any of outcomes was written (in a dry run, would
// be).
func anyWritten(outcomes []MCPOutcome) bool {
	for _, outcome := range outcomes {
		if outcome.Status == "written" {
			return true
		}
	}
	return false
}

// settingsEnableAllProjectMCP reads a project's .claude/settings*.json for
// enableAllProjectMcpServers=true — the flag lives either there or in
// ~/.claude.json's project entry, and both spellings mean the same refusal.
func settingsEnableAllProjectMCP(projectPath string) bool {
	for _, settingsName := range []string{"settings.json", "settings.local.json"} {
		body, err := os.ReadFile(filepath.Join(projectPath, ".claude", settingsName))
		if err != nil {
			continue
		}
		var settings struct {
			EnableAllProjectMcpServers bool `json:"enableAllProjectMcpServers"`
		}
		if json.Unmarshal(body, &settings) == nil && settings.EnableAllProjectMcpServers {
			return true
		}
	}
	return false
}

// claudeProjectPaths lists every project root the Claude source knows about:
// the ~/.claude.json projects keys, the per-project MCP keys, and the
// directory this import runs from. The last one matters because a
// repository's .mcp.json is the repository's own file — it exists whether or
// not the source's user-level index ever named the project. Sorted, so two
// runs report the same order.
func claudeProjectPaths(data *claudeData, opts *Options) []string {
	seen := map[string]bool{}
	var out []string
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	for _, path := range data.JSONProjectPaths {
		add(path)
	}
	for path := range data.ProjectMCP {
		add(path)
	}
	if opts != nil {
		add(opts.CurrentProjectRoot)
	}
	sort.Strings(out)
	return out
}

// importProjectMCPJSONFile copies a repository .mcp.json into forebrain's own
// project file. Entries the source explicitly disabled are skipped, entries
// it explicitly enabled are pre-approved, and entries on neither list stay
// pending their first confirmation (B7).
func importProjectMCPJSONFile(projectPath string, data *claudeData, opts *Options) ([]MCPOutcome, []string) {
	out := []MCPOutcome{}
	notices := []string{}
	jsonPath := filepath.Join(projectPath, ".mcp.json")
	body, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []string{"could not read .mcp.json: " + err.Error()}
	}
	var doc struct {
		MCPServers map[string]rawMCPServer `json:"mcpServers"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, []string{".mcp.json could not be read as MCP server configuration"}
	}
	names := make([]string, 0, len(doc.MCPServers))
	for name := range doc.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	enabled := data.EnabledMCPJSON[projectPath]
	disabled := data.DisabledMCPJSON[projectPath]
	for _, name := range names {
		if nameInList(disabled, name) {
			out = append(out, MCPOutcome{Name: name, Scope: "project", Status: "skipped", Detail: "disabled at the source, not migrated"})
			continue
		}
		entry := convertMCPServer(name, doc.MCPServers[name])
		_, outcome := appendProjectMCPServer(projectPath, entry, opts)
		if outcome.Status == "written" {
			if nameInList(enabled, name) {
				outcome.Detail += "; approval carried over"
			} else {
				outcome.Detail += "; needs your confirmation before it starts"
			}
		}
		out = append(out, outcome)
	}
	if anyWritten(out) {
		notices = append(notices, "its .mcp.json servers go into "+projectFileRel+"; Forebrain Harness does not read .mcp.json")
	}
	return out, notices
}

// importClaudeProjectPermissions converts permissions.allow/deny/ask from
// .claude/settings.json and settings.local.json into .forebrain/safety.json
// rules (§4.9.2): every rule is a PermissionRuleValue with the tool name
// mapped to forebrain's spelling, appended after dedup, with a backup first.
func importClaudeProjectPermissions(projectPath string, opts *Options) (int, []string) {
	notices := []string{}
	rules := map[string][]safety.PermissionRuleValue{}
	order := []string{"allow", "deny", "ask"}
	loaded := false
	for _, settingsName := range []string{"settings.json", "settings.local.json"} {
		body, err := os.ReadFile(filepath.Join(projectPath, ".claude", settingsName))
		if err != nil {
			continue
		}
		var settings struct {
			Permissions map[string][]string `json:"permissions"`
		}
		if json.Unmarshal(body, &settings) != nil {
			notices = append(notices, ".claude/"+settingsName+" could not be read, so its permission rules were left out")
			continue
		}
		for _, behavior := range order {
			for _, rule := range settings.Permissions[behavior] {
				if value, ok := parseClaudePermissionRule(rule); ok {
					rules[behavior] = append(rules[behavior], value)
					loaded = true
				}
			}
		}
	}
	if !loaded {
		return 0, notices
	}
	added, skipped, err := appendProjectSafetyRules(projectPath, rules, opts)
	if err != nil {
		notices = append(notices, "could not write .forebrain/safety.json: "+err.Error())
		return 0, notices
	}
	if added > 0 {
		note := fmt.Sprintf("%d permission rules go into .forebrain/safety.json", added)
		if skipped > 0 {
			note += fmt.Sprintf(", %d were already there", skipped)
		}
		notices = append(notices, note)
	}
	return added, notices
}

// parseClaudePermissionRule splits one Claude permission rule into forebrain's
// value shape: "Bash(go test *)" → {shell, "go test *"}, "Read" → {read_file}.
func parseClaudePermissionRule(rule string) (safety.PermissionRuleValue, bool) {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return safety.PermissionRuleValue{}, false
	}
	tool, content := rule, ""
	if open := strings.Index(rule, "("); open > 0 && strings.HasSuffix(rule, ")") {
		tool = strings.TrimSpace(rule[:open])
		content = strings.TrimSuffix(rule[open+1:], ")")
		content = strings.ReplaceAll(content, `\)`, `)`)
		content = strings.ReplaceAll(content, `\(`, `(`)
		content = strings.ReplaceAll(content, `\`, `\`)
	}
	if tool == "" {
		return safety.PermissionRuleValue{}, false
	}
	value := safety.PermissionRuleValue{ToolName: mapToolName(tool)}
	if strings.TrimSpace(content) != "" {
		value.RuleContent = content
	}
	return value, true
}

// appendProjectSafetyRules appends deduplicated rules to a project's
// .forebrain/safety.json, backing the file up first. Existing rules are counted
// as skipped, never rewritten.
func appendProjectSafetyRules(projectPath string, incoming map[string][]safety.PermissionRuleValue, opts *Options) (added, skipped int, err error) {
	path := filepath.Join(projectPath, ".forebrain", "safety.json")
	file := projectSafetyFile{Rules: map[string][]safety.PermissionRuleValue{}}
	if body, readErr := os.ReadFile(path); readErr == nil {
		if parseErr := json.Unmarshal(body, &file); parseErr != nil {
			return 0, 0, fmt.Errorf("parse %s: %w", path, parseErr)
		}
	} else if !os.IsNotExist(readErr) {
		return 0, 0, readErr
	}
	existing := map[string]bool{}
	for behavior, rules := range file.Rules {
		for _, rule := range rules {
			existing[safetyRuleKey(behavior, rule)] = true
		}
	}
	behaviors := []string{string(safety.BehaviorAllow), string(safety.BehaviorDeny), string(safety.BehaviorAsk)}
	incomingOrder := map[string][]safety.PermissionRuleValue{}
	for _, behavior := range behaviors {
		incomingOrder[behavior] = incoming[behavior]
	}
	changed := false
	for _, behavior := range behaviors {
		for _, rule := range incomingOrder[behavior] {
			key := safetyRuleKey(behavior, rule)
			if existing[key] {
				skipped++
				continue
			}
			existing[key] = true
			file.Rules[behavior] = append(file.Rules[behavior], rule)
			added++
			changed = true
		}
	}
	if !changed || opts.DryRun {
		if opts.DryRun && added > 0 {
			return added, skipped, nil
		}
		return added, skipped, nil
	}
	if err := backupFile(path, opts.now()); err != nil {
		return added, skipped, err
	}
	file.UpdatedAt = opts.now().Unix()
	body, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return added, skipped, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return added, skipped, err
	}
	return added, skipped, os.WriteFile(path, append(body, '\n'), 0o644)
}

func safetyRuleKey(behavior string, rule safety.PermissionRuleValue) string {
	return behavior + "\x00" + strings.TrimSpace(rule.ToolName) + "\x00" + strings.TrimSpace(rule.RuleContent)
}

// projectSafetyFile is the on-disk shape of .forebrain/safety.json (the same
// shape pkg/safety persists; repeated here because that struct is private
// and migrate must not grow a dependency to reach it).
type projectSafetyFile struct {
	Rules     map[string][]safety.PermissionRuleValue `json:"rules,omitempty"`
	UpdatedAt int64                                   `json:"updated_at,omitempty"`
}

const projectFileRel = ".forebrain/mcp_servers.yaml"

func readProjectMCPDoc(projectPath string) (projectMCPDoc, error) {
	path := filepath.Join(projectPath, ".forebrain", "mcp_servers.yaml")
	doc := projectMCPDoc{}
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return doc, nil
		}
		return doc, err
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return doc, fmt.Errorf("parse %s: %w", path, err)
	}
	return doc, nil
}

func writeProjectMCPDoc(projectPath string, doc projectMCPDoc) error {
	sort.Slice(doc.Servers, func(i, j int) bool {
		return strings.ToLower(doc.Servers[i].Name) < strings.ToLower(doc.Servers[j].Name)
	})
	body, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	dir := filepath.Join(projectPath, ".forebrain")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "mcp_servers.yaml"), body, 0o644)
}

func nameInList(list []string, name string) bool {
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

// presetEnabledConsents records allow decisions for the .mcp.json entries the
// source had explicitly enabled — nothing else is pre-decided (B7).
func presetEnabledConsents(opts *Options, projectPath string, enabled []string) error {
	if len(enabled) == 0 || strings.TrimSpace(opts.AgentWorkspace) == "" {
		return nil
	}
	project, err := safety.Resolve(projectPath)
	if err != nil {
		return nil
	}
	if root := safety.TrustedRoot(safety.ProjectContext{Project: project, TrustLevel: safety.LevelTrusted}); root == "" {
		return nil
	}
	consents, err := mcp.LoadProjectConsents(opts.AgentWorkspace)
	if err != nil {
		return err
	}
	projectKey := memory.ProjectKey(projectPath)
	for _, server := range mcpEntries(projectPath) {
		if !nameInList(enabled, server.Name) {
			continue
		}
		consents.Decide(projectKey, server.Name, mcp.ServerFingerprint(server), mcp.ProjectConsentAllow, opts.now())
	}
	return mcp.SaveProjectConsents(opts.AgentWorkspace, consents)
}

func mcpEntries(projectPath string) []appcfg.MCPServerConfig {
	servers, _ := mcp.LoadProjectMCPServers(projectPath)
	return servers
}

// PlanOutcome reports one migrated plan file.
type PlanOutcome struct {
	Name       string
	ProjectKey string // "" when the plan landed in the unscoped plans root
	SessionID  string // the imported conversation whose plan directory received it; "" for the unscoped plans root
	Status     string // installed | up-to-date | diverged | skipped
	Detail     string
}

// importPlans copies the source's plan files into forebrain's plan store:
// <workspaceRoot>/plans/<projectKey>/cli-<session>/<slug>.md. Attribution goes
// through the conversations that carried the plan — the slug names the file,
// each conversation's cwd names the project — and a slug shared by several
// conversations gives each of them its own copy (owner decision 2026-10-09:
// the plan belongs to the conversation that resumes it, and any of them may
// be the one resumed). A plan whose slug matches no imported conversation
// goes to the unscoped plans root as history — no conversation reads it —
// and the report says so. Content is never overwritten: an existing
// identical file is up-to-date, a differing one is diverged and left alone.
func importPlans(data *claudeData, opts *Options, progress func(Progress)) []PlanOutcome {
	out := []PlanOutcome{}
	if len(data.Plans) == 0 {
		return out
	}
	slugSessions := planSlugSessions(data)
	workspace := strings.TrimSpace(opts.AgentWorkspace)
	if workspace == "" {
		for _, plan := range data.Plans {
			out = append(out, PlanOutcome{Name: plan.Name, Status: "skipped", Detail: "agent workspace unavailable"})
		}
		return out
	}
	for _, plan := range data.Plans {
		if progress != nil {
			progress(Progress{Stage: "plans", Detail: plan.Name})
		}
		body, err := os.ReadFile(plan.Path)
		if err != nil {
			out = append(out, PlanOutcome{Name: plan.Name, Status: "skipped", Detail: err.Error()})
			continue
		}
		normalized := string(bytes.TrimRight(body, "\n")) + "\n"
		sessions := slugSessions[plan.Name]
		if len(sessions) == 0 {
			outcome := writeExtractedPlan(workspace, "", "", plan.Name, normalized, opts.DryRun)
			if outcome.Status == "installed" {
				outcome.Detail += " (no conversation matched this plan's slug)"
			}
			out = append(out, outcome)
			continue
		}
		for _, session := range sessions {
			projectKey := memory.ProjectKey(session.Cwd)
			out = append(out, writeExtractedPlan(workspace, projectKey, claudeSessionTarget(session.SessionID), plan.Name, normalized, opts.DryRun))
		}
	}
	return out
}

// planSlugSessions maps a plan slug to every imported conversation that
// carried it. A slug shared by several conversations gives each of them its
// own copy of the plan (owner decision 2026-10-09): a plan belongs to the
// conversation that resumes it, and any of them may be the one resumed. The
// list is deduplicated by target conversation id and sorted by it, so the
// report and the write order are deterministic.
func planSlugSessions(data *claudeData) map[string][]claudeSession {
	out := map[string][]claudeSession{}
	seen := map[string]map[string]bool{}
	for _, project := range data.Projects {
		for _, session := range project.Sessions {
			if session.Slug == "" || session.Cwd == "" {
				continue
			}
			target := claudeSessionTarget(session.SessionID)
			if seen[session.Slug] == nil {
				seen[session.Slug] = map[string]bool{}
			}
			if seen[session.Slug][target] {
				continue
			}
			seen[session.Slug][target] = true
			out[session.Slug] = append(out[session.Slug], session)
		}
	}
	for slug := range out {
		sessions := out[slug]
		sort.Slice(sessions, func(i, j int) bool {
			return claudeSessionTarget(sessions[i].SessionID) < claudeSessionTarget(sessions[j].SessionID)
		})
	}
	return out
}

// planContentEqual compares plan bodies ignoring trailing whitespace, the
// only difference a round trip through either store's writer introduces.
func planContentEqual(a, b []byte) bool {
	return strings.TrimRight(string(a), "\n \t") == strings.TrimRight(string(b), "\n \t")
}

// planFileSlug normalizes a session title into a plan file name stem.
func planFileSlug(title string) string {
	slug := sanitizeSlug(title)
	if len(slug) > 80 {
		slug = slug[:80]
	}
	return slug
}

// globalInstructionsNote renders a source's global instruction file
// (Claude's CLAUDE.md, Codex's AGENTS.md) as a global-scope ad-hoc note,
// named after the file's own modification time (§4.7).
func globalInstructionsNote(kind, filename, root, body string, now time.Time) (string, string) {
	at := now
	if info, err := os.Stat(filepath.Join(root, filename)); err == nil {
		at = info.ModTime()
	}
	noteName := at.UTC().Format("2006-01-02T15-04-05") + "-" + kind + "-global-instructions.md"
	front, noteBody := splitFrontmatter(body)
	var builder strings.Builder
	if name := strings.TrimSpace(front["name"]); name != "" {
		builder.WriteString("# " + name + "\n\n")
	} else {
		builder.WriteString("# " + kind + " global instructions\n\n")
	}
	if description := strings.TrimSpace(front["description"]); description != "" {
		builder.WriteString(description + "\n\n")
	}
	if trimmed := strings.TrimSpace(noteBody); trimmed != "" {
		builder.WriteString(trimmed + "\n\n")
	}
	builder.WriteString("---\norigin: " + kind + " global instructions (" + filename + ")\n")
	return noteName, builder.String()
}

// importClaudeGlobalInstructions writes ~/.claude/CLAUDE.md as a global-scope
// note when it exists and holds text — the branch the original plan named but
// never implemented; both sources now behave the same (§4.7).
func importClaudeGlobalInstructions(data *claudeData, opts *Options) MemoryOutcome {
	outcome := MemoryOutcome{ProjectDir: "CLAUDE.md", Scope: "global"}
	body, err := os.ReadFile(filepath.Join(data.Root, "CLAUDE.md"))
	if err != nil || strings.TrimSpace(string(body)) == "" {
		outcome.Reason = "empty or absent at the source"
		return outcome
	}
	filename, note := globalInstructionsNote("claude", "CLAUDE.md", data.Root, string(body), opts.now())
	outcome.ResolvedPath = "global scope"
	if opts.DryRun {
		outcome.Written = append(outcome.Written, filename)
		return outcome
	}
	roots, rootsErr := memory.ResolveRootsForAgent(opts.AgentWorkspace)
	if rootsErr != nil {
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

// backupFile copies a file aside before an append-patch rewrites it.
func backupFile(path string, at time.Time) error {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	backup := path + ".bak-" + at.UTC().Format("20060102T150405Z")
	return os.WriteFile(backup, body, 0o600)
}

// mergeInputHistory appends Claude source input-history lines the target file
// does not already hold.
func mergeInputHistory(data *claudeData, opts *Options, progress func(Progress)) (HistoryOutcome, error) {
	return mergeHistoryEntries(data.History, data.HistoryLines, opts, progress)
}

// mergeHistoryEntries appends source input-history lines the target file does
// not already hold. The target is one line per entry with newlines folded to
// spaces — the same normalization the composer applies before writing. Both
// sources share it, differing only in the field names their readers use.
func mergeHistoryEntries(entries []claudeHistoryEntry, sourceLines int, opts *Options, progress func(Progress)) (HistoryOutcome, error) {
	outcome := HistoryOutcome{SourceLines: sourceLines}
	if progress != nil {
		progress(Progress{Stage: "history", Detail: fmt.Sprintf("%d lines", len(entries))})
	}
	target := strings.TrimSpace(opts.InputHistoryPath)
	if target == "" {
		outcome.Skipped = len(entries)
		return outcome, nil
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Timestamp < entries[j].Timestamp })

	existing := map[string]bool{}
	if body, err := os.ReadFile(target); err == nil {
		for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "# forebrain-history-metadata") {
				continue
			}
			existing[normalizeHistoryLine(line)] = true
		}
	}
	var toAppend []string
	for _, entry := range entries {
		line := normalizeHistoryLine(entry.Display)
		if line == "" {
			continue
		}
		if existing[line] {
			outcome.Skipped++
			continue
		}
		existing[line] = true
		toAppend = append(toAppend, line)
		outcome.Added++
	}
	if len(toAppend) > 0 && !opts.DryRun {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return outcome, err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return outcome, err
		}
		for _, line := range toAppend {
			if _, err := file.WriteString(line + "\n"); err != nil {
				file.Close()
				return outcome, err
			}
		}
		if err := file.Close(); err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

// normalizeHistoryLine folds newlines to spaces and drops empties, matching
// the composer's history store.
func normalizeHistoryLine(line string) string {
	line = strings.ReplaceAll(line, "\r", "")
	line = strings.ReplaceAll(line, "\n", " ")
	if strings.TrimSpace(line) == "" {
		return ""
	}
	return line
}
