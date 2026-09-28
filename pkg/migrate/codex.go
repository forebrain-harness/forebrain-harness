package migrate

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// codexData is everything one import reads from a Codex home, gathered before
// any writing starts so the dry run and the run agree on scope. The heavy
// indexes (titles, spawn edges, memories) come from snapshot copies of the
// source's SQLite files: those are a live app's databases, and the import
// must not contend with them (§4.5).
type codexData struct {
	Root     string
	Rollouts []codexRollout
	// ThreadIndex carries the title/cwd/git a thread's rollout file may not
	// hold itself; it merges state_5.threads with session_index.jsonl.
	ThreadIndex map[string]codexThreadMeta
	// SpawnEdges is state_5.thread_spawn_edges verbatim; each edge makes the
	// child rollout a child session of the parent thread.
	SpawnEdges []codexSpawnEdge
	Memories   []codexMemory
	// UserMCP is config.toml's [mcp_servers] in file order (name ascending).
	UserMCP []codexMCPServer
	// EnabledPlugins is config.toml's [plugins."<name>@<marketplace>"] with
	// enabled=true; only these contribute skills (§4.8).
	EnabledPlugins map[string]bool
	PluginSkills   []claudePluginSkill
	// GlobalInstructions is ~/.codex/AGENTS.md when it holds any text.
	GlobalInstructions string
	// History entries and the raw non-empty line count, so the merge's
	// acceptance arithmetic stays honest about unparsable lines.
	History      []claudeHistoryEntry
	HistoryLines int
	// ProjectPaths are every project root the source knows about: the
	// [projects] keys of config.toml plus the distinct thread cwds. Project
	// configuration migration scans them for .codex/config.toml.
	ProjectPaths []string
	// Degraded records what could not be read and what the import fell back
	// to (§4.5: a missing index degrades, never fails the import).
	Degraded []string
	// StateRootsMissing reports the SQLite snapshot could not be taken at
	// all; titles then come from session_index.jsonl or the transcripts.
	StateRootsMissing bool
}

// codexRollout is one sessions/<Y>/<M>/<D>/rollout-*.jsonl file.
type codexRollout struct {
	Path     string
	ThreadID string
	Bytes    int64
	// Cwd and FirstSeen come from the file's head session_meta.
	Cwd       string
	FirstSeen int64
}

// codexThreadMeta is one row of the merged thread index.
type codexThreadMeta struct {
	Title     string
	Cwd       string
	GitBranch string
	Archived  bool
}

type codexSpawnEdge struct {
	ParentThreadID string
	ChildThreadID  string
	Status         string
}

// codexMemory is one stage1_outputs row.
type codexMemory struct {
	ThreadID        string
	RawMemory       string
	RolloutSummary  string
	RolloutSlug     string
	GeneratedAt     int64
	SourceUpdatedAt int64
}

// codexMCPServer is one [mcp_servers.<name>] table with the fields the
// migration maps; anything else lands in Dropped for the report.
type codexMCPServer struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
	Cwd     string
	URL     string
	Headers map[string]string
	Tools   map[string]map[string]any
	// Enabled is tri-state: nil means the key was absent (Codex treats that
	// as enabled), false means disabled (skipped), true explicit.
	Enabled *bool
	// Dropped lists the keys present in the source table with no forebrain
	// counterpart (startup_timeout_sec, oauth_*): the report names them
	// rather than letting them vanish silently.
	Dropped []string
}

// discoverCodex walks the Codex home once. Everything is sorted so two runs
// over the same data produce the same order and the same report.
func discoverCodex(root string) (*codexData, error) {
	data := &codexData{
		Root:           root,
		ThreadIndex:    map[string]codexThreadMeta{},
		EnabledPlugins: map[string]bool{},
	}
	data.Rollouts = discoverCodexRollouts(filepath.Join(root, "sessions"))
	data.ThreadIndex = readCodexSessionIndex(filepath.Join(root, "session_index.jsonl"))
	data.loadCodexState(filepath.Join(root, "state_5.sqlite"))
	data.Memories = readCodexMemories(filepath.Join(root, "memories_1.sqlite"), data)
	data.UserMCP, data.EnabledPlugins, data.ProjectPaths = readCodexConfig(filepath.Join(root, "config.toml"))
	data.PluginSkills = codexPluginSkills(root, data.EnabledPlugins)
	data.GlobalInstructions = readCodexGlobalInstructions(filepath.Join(root, "AGENTS.md"))
	data.History, data.HistoryLines = readCodexHistory(filepath.Join(root, "history.jsonl"))

	// Project roots also come from the threads' cwds — a project used without
	// ever being named in config.toml still gets its .codex/config.toml
	// migrated.
	seen := map[string]bool{}
	for _, path := range data.ProjectPaths {
		seen[path] = true
	}
	var cwds []string
	for id := range data.ThreadIndex {
		if cwd := strings.TrimSpace(data.ThreadIndex[id].Cwd); cwd != "" && !seen[cwd] {
			seen[cwd] = true
			cwds = append(cwds, cwd)
		}
	}
	for _, rollout := range data.Rollouts {
		if cwd := strings.TrimSpace(rollout.Cwd); cwd != "" && !seen[cwd] {
			seen[cwd] = true
			cwds = append(cwds, cwd)
		}
	}
	sort.Strings(cwds)
	data.ProjectPaths = append(data.ProjectPaths, cwds...)
	sort.Strings(data.ProjectPaths)
	return data, nil
}

// discoverCodexRollouts lists every rollout file under sessions/, each with
// the thread id and cwd from its head session_meta (the first authority; the
// filename suffix is only a fallback).
func discoverCodexRollouts(sessionsDir string) []codexRollout {
	var out []codexRollout
	_ = filepath.WalkDir(sessionsDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return nil
		}
		threadID, cwd, firstSeen := scanCodexHead(path)
		if threadID == "" {
			threadID = codexThreadIDFromFilename(name)
		}
		if threadID == "" {
			return nil
		}
		out = append(out, codexRollout{
			Path: path, ThreadID: threadID, Bytes: info.Size(),
			Cwd: cwd, FirstSeen: firstSeen,
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// scanCodexHead reads a rollout's first records for the session_meta line.
func scanCodexHead(path string) (threadID, cwd string, firstSeen int64) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", 0
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 1<<16)
	for i := 0; i < 50; i++ {
		line, err := reader.ReadString('\n')
		if strings.TrimSpace(line) != "" {
			var record struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal([]byte(strings.TrimSpace(line)), &record) == nil && record.Type == "session_meta" {
				var meta struct {
					SessionID string `json:"session_id"`
					ID        string `json:"id"`
					Cwd       string `json:"cwd"`
					Timestamp string `json:"timestamp"`
				}
				if json.Unmarshal(record.Payload, &meta) == nil {
					threadID = strings.TrimSpace(meta.SessionID)
					if threadID == "" {
						threadID = strings.TrimSpace(meta.ID)
					}
					return threadID, strings.TrimSpace(meta.Cwd), parseTimestamp(meta.Timestamp)
				}
			}
		}
		if err != nil {
			break
		}
	}
	return "", "", 0
}

// codexThreadIDFromFilename extracts the trailing UUID of
// rollout-<ISO>-<threadId>.jsonl; "" when the stem does not end in one.
func codexThreadIDFromFilename(name string) string {
	stem := strings.TrimSuffix(strings.TrimPrefix(name, "rollout-"), ".jsonl")
	if len(stem) < 36 {
		return ""
	}
	candidate := stem[len(stem)-36:]
	if !strings.Contains(candidate, "-") {
		return ""
	}
	return candidate
}

// readCodexSessionIndex reads session_index.jsonl, the lightweight title
// index; it is a subset of state_5.threads but survives without SQLite.
func readCodexSessionIndex(path string) map[string]codexThreadMeta {
	out := map[string]codexThreadMeta{}
	body, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var record struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal([]byte(line), &record) != nil || strings.TrimSpace(record.ID) == "" {
			continue
		}
		id := strings.TrimSpace(record.ID)
		entry := out[id]
		if strings.TrimSpace(entry.Title) == "" {
			entry.Title = strings.TrimSpace(record.ThreadName)
		}
		out[id] = entry
	}
	return out
}

// loadCodexState snapshots state_5.sqlite and folds its threads table into
// the thread index plus the spawn edges. Every failure degrades: the index
// keeps whatever session_index.jsonl provided, edges stay empty, and the
// degradation lands in the report with the underlying error (§4.5).
func (data *codexData) loadCodexState(path string) {
	db, cleanup, err := openCodexSnapshot(path)
	if err != nil {
		data.StateRootsMissing = true
		data.Degraded = append(data.Degraded,
			"thread index unavailable ("+filepath.Base(path)+": "+err.Error()+"); titles fall back to session_index.jsonl and the transcripts, subagent links fall back to independent sessions")
		return
	}
	defer cleanup()
	defer db.Close()
	rows, err := db.Query(`SELECT id, IFNULL(cwd,''), IFNULL(title,''), IFNULL(git_branch,''), IFNULL(archived,0) FROM threads`)
	if err != nil {
		data.Degraded = append(data.Degraded, "threads table unreadable: "+err.Error())
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, cwd, title, branch string
		var archived int
		if err := rows.Scan(&id, &cwd, &title, &branch, &archived); err != nil {
			continue
		}
		entry := data.ThreadIndex[strings.TrimSpace(id)]
		if t := strings.TrimSpace(title); t != "" {
			entry.Title = t
		}
		if c := strings.TrimSpace(cwd); c != "" {
			entry.Cwd = c
		}
		if b := strings.TrimSpace(branch); b != "" {
			entry.GitBranch = b
		}
		entry.Archived = archived != 0
		data.ThreadIndex[strings.TrimSpace(id)] = entry
	}
	edges, err := db.Query(`SELECT parent_thread_id, child_thread_id, IFNULL(status,'') FROM thread_spawn_edges`)
	if err == nil {
		for edges.Next() {
			var edge codexSpawnEdge
			if err := edges.Scan(&edge.ParentThreadID, &edge.ChildThreadID, &edge.Status); err == nil {
				data.SpawnEdges = append(data.SpawnEdges, edge)
			}
		}
		edges.Close()
	}
}

// openCodexSnapshot copies the database (plus its -wal/-shm) into a temp
// directory and opens the copy read-only: the original belongs to a running
// app, and copying first avoids both lock contention and WAL recovery on the
// live file (§4.5). The second return value removes the temp directory; the
// caller runs it after closing the database, so a dry-run-plus-run pair over
// a large source leaves nothing behind.
func openCodexSnapshot(path string) (*sql.DB, func(), error) {
	if _, err := os.Stat(path); err != nil {
		return nil, nil, err
	}
	tmp, err := os.MkdirTemp("", "forebrain-migrate-codex-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	mainCopy := filepath.Join(tmp, filepath.Base(path))
	for _, suffix := range []string{"", "-wal", "-shm"} {
		source := path + suffix
		if _, err := os.Stat(source); err != nil {
			continue
		}
		if err := copyFile(source, mainCopy+suffix); err != nil {
			cleanup()
			return nil, nil, err
		}
	}
	db, err := stateOpenReadOnly(mainCopy)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return db, cleanup, nil
}

// readCodexMemories snapshots memories_1.sqlite and lists its stage1_outputs
// rows in generation order. A missing or unreadable store is not an error:
// this machine simply has no memories (C7), and the report says which.
func readCodexMemories(path string, data *codexData) []codexMemory {
	db, cleanup, err := openCodexSnapshot(path)
	if err != nil {
		data.Degraded = append(data.Degraded,
			"memory store unavailable ("+filepath.Base(path)+": "+err.Error()+"); no memories imported")
		return nil
	}
	defer cleanup()
	defer db.Close()
	rows, err := db.Query(`SELECT IFNULL(thread_id,''), IFNULL(raw_memory,''), IFNULL(rollout_summary,''), IFNULL(rollout_slug,''), IFNULL(generated_at,0), IFNULL(source_updated_at,0) FROM stage1_outputs ORDER BY generated_at, rowid`)
	if err != nil {
		data.Degraded = append(data.Degraded, "stage1_outputs unreadable: "+err.Error())
		return nil
	}
	defer rows.Close()
	var out []codexMemory
	for rows.Next() {
		var mem codexMemory
		if err := rows.Scan(&mem.ThreadID, &mem.RawMemory, &mem.RolloutSummary, &mem.RolloutSlug, &mem.GeneratedAt, &mem.SourceUpdatedAt); err == nil {
			out = append(out, mem)
		}
	}
	return out
}

// readCodexConfig parses <CODEX_HOME>/config.toml through the shared TOML
// entry point (§5): the user-level MCP tables, the enabled plugins, and the
// [projects] keys (paths, for project-configuration discovery).
func readCodexConfig(path string) (servers []codexMCPServer, plugins map[string]bool, projectPaths []string) {
	plugins = map[string]bool{}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, plugins, nil
	}
	doc, err := tool.ParseTOMLDocument(string(body))
	if err != nil {
		return nil, plugins, nil
	}
	if mcpTables, ok := doc["mcp_servers"].(map[string]any); ok {
		names := make([]string, 0, len(mcpTables))
		for name := range mcpTables {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			table, _ := mcpTables[name].(map[string]any)
			if server, ok := codexMCPServerFromTable(name, table); ok {
				servers = append(servers, server)
			}
		}
	}
	if pluginTables, ok := doc["plugins"].(map[string]any); ok {
		for key, value := range pluginTables {
			table, _ := value.(map[string]any)
			if enabled, _ := table["enabled"].(bool); enabled {
				plugins[key] = true
			}
		}
	}
	if projects, ok := doc["projects"].(map[string]any); ok {
		for path := range projects {
			if path = strings.TrimSpace(path); path != "" {
				projectPaths = append(projectPaths, path)
			}
		}
	}
	sort.Strings(projectPaths)
	return servers, plugins, projectPaths
}

// codexMCPServerFromTable maps one [mcp_servers.<name>] table onto the
// migration's intermediate shape. Keys with no forebrain counterpart are
// collected, not silently dropped.
func codexMCPServerFromTable(name string, table map[string]any) (codexMCPServer, bool) {
	if table == nil {
		return codexMCPServer{}, false
	}
	server := codexMCPServer{Name: strings.TrimSpace(name)}
	for key, value := range table {
		switch key {
		case "command":
			server.Command, _ = value.(string)
		case "cwd":
			server.Cwd, _ = value.(string)
		case "url":
			server.URL, _ = value.(string)
		case "bearer_token_env_var":
			// No forebrain counterpart: headers carry literal values or ${ENV}
			// references, never a named-env indirection. Reported, dropped.
			server.Dropped = append(server.Dropped, key)
		case "args":
			if list, ok := value.([]any); ok {
				for _, item := range list {
					if text, ok := item.(string); ok {
						server.Args = append(server.Args, text)
					}
				}
			}
		case "env":
			if env, ok := value.(map[string]any); ok {
				server.Env = map[string]string{}
				for k, v := range env {
					if text, ok := v.(string); ok {
						server.Env[k] = text
					}
				}
			}
		case "headers":
			if headers, ok := value.(map[string]any); ok {
				server.Headers = map[string]string{}
				for k, v := range headers {
					if text, ok := v.(string); ok {
						server.Headers[k] = text
					}
				}
			}
		case "enabled":
			if b, ok := value.(bool); ok {
				enabled := b
				server.Enabled = &enabled
			}
		case "tools":
			if tools, ok := value.(map[string]any); ok {
				server.Tools = map[string]map[string]any{}
				for toolName, toolValue := range tools {
					if toolTable, ok := toolValue.(map[string]any); ok {
						server.Tools[toolName] = toolTable
					}
				}
			}
		default:
			server.Dropped = append(server.Dropped, key)
		}
	}
	sort.Strings(server.Dropped)
	return server, true
}

// codexPluginSkills lists the skills of enabled plugins under the Codex
// home's plugin cache, reusing the Claude-side walker (highest version dir,
// SKILL.md present). Unenabled marketplaces stay out: importing them would
// flood /skills with entries the source never loaded.
func codexPluginSkills(root string, enabled map[string]bool) []claudePluginSkill {
	if len(enabled) == 0 {
		return nil
	}
	return pluginSkillsUnder(filepath.Join(root, "plugins", "cache"), enabled)
}

// readCodexGlobalInstructions returns the AGENTS.md body when the file holds
// any text; an empty file is "nothing to import", not an error (§4.7).
func readCodexGlobalInstructions(path string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if strings.TrimSpace(string(body)) == "" {
		return ""
	}
	return string(body)
}

// readCodexHistory reads history.jsonl ({session_id, ts, text}) and reports
// the parsable entries plus the raw non-empty line count.
func readCodexHistory(path string) ([]claudeHistoryEntry, int) {
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
			Text string `json:"text"`
			Ts   int64  `json:"ts"`
		}
		if json.Unmarshal([]byte(line), &record) != nil || strings.TrimSpace(record.Text) == "" {
			continue
		}
		out = append(out, claudeHistoryEntry{Display: record.Text, Timestamp: record.Ts})
	}
	return out, lines
}

func copyFile(source, dest string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// codexSourceCounts counts what an import of a Codex home would read, with
// the importer's own split of threads into conversations and the subagent
// threads they spawned.
func codexSourceCounts(root string) (sourceCounts, error) {
	data, err := discoverCodex(root)
	if err != nil {
		return sourceCounts{}, err
	}
	counts := sourceCounts{Memories: len(data.Memories), HistoryLines: data.HistoryLines, MCPServers: len(data.UserMCP)}
	for _, unit := range codexUnits(data, &Options{}) {
		if unit.IsChild {
			counts.Subagents++
		} else {
			counts.Sessions++
		}
	}
	if strings.TrimSpace(data.GlobalInstructions) != "" {
		counts.Memories++
	}
	return counts, nil
}

// stateOpenReadOnly is the snapshot opener, kept as a var so tests can stub
// the SQLite layer without writing fixture databases.
var stateOpenReadOnly = state.OpenReadOnly
