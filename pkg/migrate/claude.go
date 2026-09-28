package migrate

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// claudeData is everything one import reads from ~/.claude, gathered before
// any writing starts so the dry run and the run agree on scope.
type claudeData struct {
	Root     string
	Projects []claudeProject
	History  []claudeHistoryEntry
	// JSONProjectPaths is every key of ~/.claude.json's projects map, the
	// first authority for resolving an encoded directory back to a real
	// project path (B5).
	JSONProjectPaths []string
	UserMCP          map[string]rawMCPServer
	ProjectMCP       map[string]map[string]rawMCPServer // keyed by ~/.claude.json project path
	EnabledMCPJSON   map[string][]string                // project path -> enabled .mcp.json names
	DisabledMCPJSON  map[string][]string
	// EnableAllProjectMCP records enableAllProjectMcpServers=true per project
	// path. It is deliberately NOT turned into a blanket consent (§4.9.2):
	// its source meaning is "this machine once allowed every entry in this
	// .mcp.json", and pre-approving future entries would decide something the
	// user never decided.
	EnableAllProjectMCP map[string]bool
	PluginSkills        []claudePluginSkill
	// HistoryLines counts every non-empty line of history.jsonl, so the
	// merge report can state added + skipped == source lines even when some
	// lines carried no parsable display text.
	HistoryLines int
	// Plans are the plan files under plans/, named after the slug of the
	// conversation that wrote them.
	Plans []claudePlan
}

type claudePlan struct {
	Name string // slug stem
	Path string
}

// claudeProject is one encoded directory under projects/.
type claudeProject struct {
	Dir      string // absolute path of the encoded directory
	Encoded  string // directory base name
	Sessions []claudeSession
	Memories []string // absolute memory .md paths
}

// claudeSession is one main session jsonl and the subagent transcripts
// nested beside it.
type claudeSession struct {
	Path      string
	SessionID string // uuid stem of the file name
	Bytes     int64
	// Cwd and Slug come from the file's head records: cwd is the second
	// authority for resolving a project path (B5), and slug attributes a
	// plan file to the conversation that produced it.
	Cwd       string
	Slug      string
	Subagents []claudeSubagent
}

type claudeSubagent struct {
	Path     string
	AgentID  string // file stem, e.g. "agent-a24507f8e84e85e77"
	MetaPath string
}

type claudeHistoryEntry struct {
	Display   string
	Timestamp int64
}

type claudePluginSkill struct {
	Name string // skill directory name
	Dir  string // absolute skill directory
}

// rawMCPServer is one source MCP entry, kept in its source shape; conversion
// to forebrain's configuration happens at write time (assets.go).
type rawMCPServer struct {
	Type    string
	Command string
	Args    []string
	Env     map[string]string
	URL     string
	Headers map[string]string
}

// claudeSourceCounts counts what an import of a Claude Code home would read.
func claudeSourceCounts(root string) (sourceCounts, error) {
	data, err := discoverClaude(root)
	if err != nil {
		return sourceCounts{}, err
	}
	counts := sourceCounts{HistoryLines: data.HistoryLines, MCPServers: len(data.UserMCP)}
	for _, project := range data.Projects {
		counts.Sessions += len(project.Sessions)
		for _, session := range project.Sessions {
			counts.Subagents += len(session.Subagents)
		}
		counts.Memories += len(project.Memories)
	}
	if body, err := os.ReadFile(filepath.Join(root, "CLAUDE.md")); err == nil && strings.TrimSpace(string(body)) != "" {
		counts.Memories++
	}
	return counts, nil
}

// discoverClaude walks the source layout once. Everything is sorted so two
// runs over the same data produce the same order and the same report.
func discoverClaude(root string) (*claudeData, error) {
	data := &claudeData{
		Root:                root,
		UserMCP:             map[string]rawMCPServer{},
		ProjectMCP:          map[string]map[string]rawMCPServer{},
		EnabledMCPJSON:      map[string][]string{},
		DisabledMCPJSON:     map[string][]string{},
		EnableAllProjectMCP: map[string]bool{},
	}
	projectsDir := filepath.Join(root, "projects")
	entries, err := os.ReadDir(projectsDir)
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(projectsDir, entry.Name())
			project := claudeProject{Dir: dir, Encoded: entry.Name()}
			files, err := os.ReadDir(dir)
			if err == nil {
				for _, file := range files {
					if file.IsDir() || !strings.HasSuffix(file.Name(), ".jsonl") {
						continue
					}
					info, err := file.Info()
					if err != nil {
						continue
					}
					stem := strings.TrimSuffix(file.Name(), ".jsonl")
					if stem == "" {
						continue
					}
					session := claudeSession{
						Path:      filepath.Join(dir, file.Name()),
						SessionID: stem,
						Bytes:     info.Size(),
					}
					session.Cwd, session.Slug = scanSessionHead(session.Path)
					session.Subagents = discoverSubagents(dir, stem)
					project.Sessions = append(project.Sessions, session)
				}
			}
			sort.Slice(project.Sessions, func(i, j int) bool {
				return project.Sessions[i].SessionID < project.Sessions[j].SessionID
			})
			memDir := filepath.Join(dir, "memory")
			if mems, err := os.ReadDir(memDir); err == nil {
				for _, mem := range mems {
					if mem.IsDir() || !strings.HasSuffix(mem.Name(), ".md") {
						continue
					}
					project.Memories = append(project.Memories, filepath.Join(memDir, mem.Name()))
				}
				sort.Strings(project.Memories)
			}
			if len(project.Sessions) > 0 || len(project.Memories) > 0 {
				data.Projects = append(data.Projects, project)
			}
		}
	}
	sort.Slice(data.Projects, func(i, j int) bool { return data.Projects[i].Encoded < data.Projects[j].Encoded })

	data.History, data.HistoryLines = readClaudeHistory(filepath.Join(root, "history.jsonl"))
	// ~/.claude.json belongs to the home being read: beside the directory for
	// the default layout (~/.claude and ~/.claude.json), inside it when the
	// home was relocated with CLAUDE_CONFIG_DIR. Never the running user's own
	// ~/.claude.json when another home was chosen: its projects and servers
	// are not that home's, and importing them would write this machine's
	// project settings as if they came from the source.
	readClaudeJSON(filepath.Join(root, ".claude.json"), data)
	readClaudeJSON(filepath.Join(root, "..", ".claude.json"), data)
	data.PluginSkills = discoverPluginSkills(root)
	data.Plans = discoverPlans(root)
	return data, nil
}

// discoverPlans lists the plan files under ~/.claude/plans. Each is named
// after the slug of the conversation that wrote it; attribution to a project
// happens at import time through the session that carries the slug.
func discoverPlans(root string) []claudePlan {
	entries, err := os.ReadDir(filepath.Join(root, "plans"))
	if err != nil {
		return nil
	}
	var out []claudePlan
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		out = append(out, claudePlan{
			Name: strings.TrimSuffix(entry.Name(), ".md"),
			Path: filepath.Join(root, "plans", entry.Name()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// scanSessionHead reads the head of a transcript and returns the first cwd
// and slug it records. One of each is enough for everything the importer
// needs — project resolution and plan attribution — and both live near the
// top of every observed file.
func scanSessionHead(path string) (cwd string, slug string) {
	file, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 1<<16)
	for i := 0; i < 500 && (cwd == "" || slug == ""); i++ {
		line, err := reader.ReadString('\n')
		if strings.TrimSpace(line) != "" {
			var head struct {
				Cwd  string `json:"cwd"`
				Slug string `json:"slug"`
			}
			if json.Unmarshal([]byte(strings.TrimSpace(line)), &head) == nil {
				if cwd == "" {
					cwd = strings.TrimSpace(head.Cwd)
				}
				if slug == "" {
					slug = strings.TrimSpace(head.Slug)
				}
			}
		}
		if err != nil {
			break
		}
	}
	return cwd, slug
}

// discoverSubagents lists agent-*.jsonl under <projectDir>/<sessionID>/
// subagents, paired with their .meta.json sidecars.
func discoverSubagents(projectDir, sessionID string) []claudeSubagent {
	subDir := filepath.Join(projectDir, sessionID, "subagents")
	entries, err := os.ReadDir(subDir)
	if err != nil {
		return nil
	}
	var out []claudeSubagent
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "agent-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		stem := strings.TrimSuffix(entry.Name(), ".jsonl")
		meta := filepath.Join(subDir, stem+".meta.json")
		if _, err := os.Stat(meta); err != nil {
			meta = ""
		}
		out = append(out, claudeSubagent{
			Path:     filepath.Join(subDir, entry.Name()),
			AgentID:  stem,
			MetaPath: meta,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return out
}

// readClaudeHistory reads history.jsonl and reports both the parsable entries
// and the raw non-empty line count, so the merge's acceptance arithmetic
// stays honest about unparsable lines.
func readClaudeHistory(path string) ([]claudeHistoryEntry, int) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, 0
	}
	out := []claudeHistoryEntry{}
	lines := 0
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines++
		var record struct {
			Display   string `json:"display"`
			Timestamp int64  `json:"timestamp"`
		}
		if json.Unmarshal([]byte(line), &record) != nil || strings.TrimSpace(record.Display) == "" {
			continue
		}
		out = append(out, claudeHistoryEntry{Display: record.Display, Timestamp: record.Timestamp})
	}
	return out, lines
}

// readClaudeJSON folds ~/.claude.json's MCP sections into the discovery: the
// user-level mcpServers, and each project entry's private mcpServers plus its
// enabled/disabled .mcp.json approval lists.
func readClaudeJSON(path string, data *claudeData) {
	body, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var doc struct {
		MCPServers map[string]rawMCPServer `json:"mcpServers"`
		Projects   map[string]struct {
			MCPServers                 map[string]rawMCPServer `json:"mcpServers"`
			EnabledMcpjsonServers      []string                `json:"enabledMcpjsonServers"`
			DisabledMcpjsonServers     []string                `json:"disabledMcpjsonServers"`
			EnableAllProjectMcpServers bool                    `json:"enableAllProjectMcpServers"`
		} `json:"projects"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return
	}
	for name, server := range doc.MCPServers {
		if strings.TrimSpace(name) != "" {
			data.UserMCP[name] = server
		}
	}
	for path, project := range doc.Projects {
		if strings.TrimSpace(path) == "" {
			continue
		}
		data.JSONProjectPaths = append(data.JSONProjectPaths, path)
		if len(project.MCPServers) > 0 {
			data.ProjectMCP[path] = project.MCPServers
		}
		if len(project.EnabledMcpjsonServers) > 0 {
			data.EnabledMCPJSON[path] = project.EnabledMcpjsonServers
		}
		if len(project.DisabledMcpjsonServers) > 0 {
			data.DisabledMCPJSON[path] = project.DisabledMcpjsonServers
		}
		if project.EnableAllProjectMcpServers {
			data.EnableAllProjectMCP[path] = true
		}
	}
	sort.Strings(data.JSONProjectPaths)
}

// discoverPluginSkills lists the skills of plugins the user enabled in
// settings.json. Marketplace plugins that were never enabled are excluded:
// importing them would flood /skills with entries the source never loaded.
func discoverPluginSkills(root string) []claudePluginSkill {
	settings := struct {
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}{}
	if body, err := os.ReadFile(filepath.Join(root, "settings.json")); err == nil {
		_ = json.Unmarshal(body, &settings)
	}
	enabled := map[string]bool{}
	for key, on := range settings.EnabledPlugins {
		if on {
			enabled[key] = true
		}
	}
	if len(enabled) == 0 {
		return nil
	}
	return pluginSkillsUnder(filepath.Join(root, "plugins", "cache"), enabled)
}

// pluginSkillsUnder walks a plugin cache for the skills of enabled plugins.
// A plugin key is "<name>@<marketplace>"; the lexicographically highest
// version directory wins (both sources version their cache the same way).
// Shared by the Claude and Codex discovery paths.
func pluginSkillsUnder(cacheDir string, enabled map[string]bool) []claudePluginSkill {
	var out []claudePluginSkill
	for key := range enabled {
		parts := strings.SplitN(key, "@", 2)
		if len(parts) != 2 {
			continue
		}
		name, marketplace := parts[0], parts[1]
		out = append(out, pluginSkillsFor(cacheDir, marketplace, name)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// pluginSkillsFor lists one plugin's skills from its highest version dir.
func pluginSkillsFor(cacheDir, marketplace, name string) []claudePluginSkill {
	pluginBase := filepath.Join(cacheDir, marketplace, name)
	versions, err := os.ReadDir(pluginBase)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(versions))
	for _, version := range versions {
		if version.IsDir() {
			names = append(names, version.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil
	}
	var out []claudePluginSkill
	skillsDir := filepath.Join(pluginBase, names[len(names)-1], "skills")
	skills, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil
	}
	for _, skill := range skills {
		if !skill.IsDir() {
			continue
		}
		if info, err := os.Stat(filepath.Join(skillsDir, skill.Name(), "SKILL.md")); err != nil || info.IsDir() {
			continue
		}
		out = append(out, claudePluginSkill{Name: skill.Name(), Dir: filepath.Join(skillsDir, skill.Name())})
	}
	return out
}
