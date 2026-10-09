package migrate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// TestResolveProjectPathFourLevels exercises B5's ladder in isolation:
// the .claude.json projects key, the transcript cwd, a filesystem-proven
// dash candidate, and the global fallback.
func TestResolveProjectPathFourLevels(t *testing.T) {
	// Level 1: the projects map names the path even when the encoded dir
	// alone cannot be reversed.
	encoded := "-Users-ada-work-my-project"
	if got := resolveProjectPath(claudeProject{Encoded: encoded},
		[]string{"/Users/ada/work/my-project"}); got != "/Users/ada/work/my-project" {
		t.Fatalf("level 1 = %q", got)
	}

	// Level 2: no map key, but the transcript's head records the cwd.
	// Discovery fills session.Cwd from that head; resolution reads it.
	dir := t.TempDir()
	session := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(session, []byte(`{"type":"user","cwd":"/Users/ada/work/from-cwd","message":{"role":"user","content":"x"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := claudeProject{Encoded: encodeProjectPath("/Users/ada/work/from-cwd"), Sessions: []claudeSession{{Path: session, Cwd: "/Users/ada/work/from-cwd"}}}
	if got := resolveProjectPath(project, nil); got != "/Users/ada/work/from-cwd" {
		t.Fatalf("level 2 = %q", got)
	}

	// Level 3: no map key and no transcript; the encoded name's dash/slash
	// candidates are tried against the filesystem. The expectation goes
	// through ProjectRoot too: on macOS it resolves /var → /private/var, so
	// comparing against the raw t.TempDir path would be symlink-fragile.
	base := t.TempDir()
	dashDir := filepath.Join(base, "dash-candidate")
	if err := os.MkdirAll(dashDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := existingDashCandidate("-" + strings.TrimPrefix(dashDir, "/")); got != memory.ProjectRoot(dashDir) {
		t.Fatalf("level 3 = %q want %q", got, memory.ProjectRoot(dashDir))
	}

	// Level 4: nothing resolves; "" tells the caller to fall back to global.
	if got := resolveProjectPath(claudeProject{Encoded: "-nowhere-known"}, nil); got != "" {
		t.Fatalf("level 4 = %q", got)
	}
}

// TestImportMemoriesScopedAndFallback writes one resolvable and one
// unresolvable memory directory: the first lands in its project scope, the
// second in global, and both filenames satisfy the note store's pattern.
func TestImportMemoriesScopedAndFallback(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	resolved := t.TempDir()

	data := &claudeData{}
	note := func(dir string) {
		if err := os.MkdirAll(filepath.Join(dir, "memory"), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: prefer-tags-fts5\ndescription: tests need tags\nmetadata:\n  modified: \"2026-09-02T03:04:05Z\"\n  originSessionId: s1\n---\n\nBody text.\n"
		if err := os.WriteFile(filepath.Join(dir, "memory", "prefer-tags-fts5.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		data.Projects = append(data.Projects, claudeProject{Dir: dir, Encoded: filepath.Base(dir),
			Memories: []string{filepath.Join(dir, "memory", "prefer-tags-fts5.md")}})
	}
	resolvedDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(resolvedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	note(resolvedDir)
	unresolved := t.TempDir()
	note(unresolved)
	// The encoded names must be the real encodings of the paths they stand
	// for, as discovery derives them; the unresolved one gets a suffix no
	// candidate or map key can ever match.
	data.Projects[len(data.Projects)-2].Encoded = encodeProjectPath(resolvedDir)
	data.Projects[len(data.Projects)-1].Encoded = encodeProjectPath(unresolved) + "-unresolvable"

	opts := &Options{AgentWorkspace: workspace, Now: fixedClock}
	outcomes, err := importMemories(ctx, data, opts, []string{resolved}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %d", len(outcomes))
	}
	if outcomes[0].Scope != "project" || outcomes[0].ResolvedPath == "" || len(outcomes[0].Written) != 1 {
		t.Fatalf("project outcome = %+v", outcomes[0])
	}
	if outcomes[1].Scope != "global" || len(outcomes[1].Written) != 1 {
		t.Fatalf("global outcome = %+v", outcomes[1])
	}
	name := outcomes[0].Written[0]
	if !strings.HasPrefix(name, "2026-09-02T03-04-05-") || !strings.HasSuffix(name, "-prefer-tags-fts5.md") {
		t.Fatalf("filename = %q", name)
	}
	projectNote := filepath.Join(workspace, "memories", "projects", memory.ProjectKey(resolvedDir),
		"extensions", "ad_hoc", "notes", name)
	body, err := os.ReadFile(projectNote)
	if err != nil {
		t.Fatalf("project note missing: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "# prefer-tags-fts5") || !strings.Contains(text, "tests need tags") ||
		!strings.Contains(text, "Body text.") || !strings.Contains(text, "origin: claude-code memory prefer-tags-fts5.md (session s1)") {
		t.Fatalf("note body = %q", text)
	}
	globalNote := filepath.Join(workspace, "memories", "global", "extensions", "ad_hoc", "notes", name)
	if _, err := os.Stat(globalNote); err != nil {
		t.Fatalf("global note missing: %v", err)
	}

	// Idempotent: a second import skips both.
	again, err := importMemories(ctx, data, opts, []string{resolved}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, outcome := range again {
		if len(outcome.Written) != 0 || len(outcome.Skipped) != 1 {
			t.Fatalf("second run outcome %d = %+v", i, outcome)
		}
	}
}

// TestImportSkillsStatuses covers install, up-to-date, diverged.
func TestImportSkillsStatuses(t *testing.T) {
	home := t.TempDir()
	source := func(name, body string) claudePluginSkill {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return claudePluginSkill{Name: name, Dir: dir}
	}
	fresh := source("fresh-skill", "# fresh\n")
	same := source("same-skill", "# same\n")
	edited := source("edited-skill", "# edited v2\n")

	dest := filepath.Join(home, "skills")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "same-skill", "SKILL.md"), []byte("# same\n"), 0o644); err != nil {
		if err2 := os.MkdirAll(filepath.Join(dest, "same-skill"), 0o755); err2 != nil {
			t.Fatal(err2)
		}
		if err := os.WriteFile(filepath.Join(dest, "same-skill", "SKILL.md"), []byte("# same\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dest, "edited-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "edited-skill", "SKILL.md"), []byte("# edited v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outcomes := importSkills(&claudeData{PluginSkills: []claudePluginSkill{fresh, same, edited}},
		&Options{Home: home, Now: fixedClock}, nil)
	byName := map[string]SkillOutcome{}
	for _, outcome := range outcomes {
		byName[outcome.Name] = outcome
	}
	if byName["fresh-skill"].Status != "installed" {
		t.Fatalf("fresh = %+v", byName["fresh-skill"])
	}
	if byName["same-skill"].Status != "up-to-date" {
		t.Fatalf("same = %+v", byName["same-skill"])
	}
	if byName["edited-skill"].Status != "diverged" {
		t.Fatalf("edited = %+v", byName["edited-skill"])
	}
	if body, err := os.ReadFile(filepath.Join(dest, "edited-skill", "SKILL.md")); err != nil || string(body) != "# edited v1\n" {
		t.Fatal("diverged copy was overwritten")
	}
}

// TestImportMCPAppendOnlyAndNotices covers the global append patch (backup,
// existing entries untouched, conflicts skipped), the project file write,
// and the notices the report must carry.
func TestImportMCPAppendOnlyAndNotices(t *testing.T) {
	fixture := buildFixtureHome(t)
	stubClaudeRoot(t, fixture)
	home := t.TempDir()
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".forebrain"), 0o755); err != nil {
		t.Fatal(err)
	}
	// .claude.json content is read from the real home; instead build data.
	data := &claudeData{
		UserMCP: map[string]rawMCPServer{
			"codegraph":  {Type: "stdio", Command: "/usr/local/bin/codegraph", Args: []string{"serve"}},
			"existing":   {Type: "stdio", Command: "/bin/conflict"},
			"http-thing": {Type: "http", URL: "https://mcp.example/api"},
		},
		ProjectMCP: map[string]map[string]rawMCPServer{
			project: {"caveman-shrink": {Type: "stdio", Command: "npx", Args: []string{"caveman-shrink"}, Env: map[string]string{"SECRET_TOKEN": "s3cr3t"}}},
		},
	}
	opts := &Options{Home: home, AgentWorkspace: t.TempDir(), ConfigPath: fixture.options(t, false).ConfigPath, Now: fixedClock}
	before, err := os.ReadFile(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}

	outcomes, notices, projects, err := importMCP(data, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]MCPOutcome{}
	for _, outcome := range outcomes {
		byName[outcome.Name+"/"+outcome.Scope] = outcome
	}
	if byName["codegraph/global"].Status != "written" {
		t.Fatalf("codegraph = %+v", byName["codegraph/global"])
	}
	if byName["existing/global"].Status != "skipped" {
		t.Fatalf("existing = %+v", byName["existing/global"])
	}
	if byName["caveman-shrink/project"].Status != "written" {
		t.Fatalf("caveman-shrink = %+v", byName["caveman-shrink/project"])
	}

	after, err := os.ReadFile(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "command: /bin/true") {
		t.Fatal("the pre-existing global entry was dropped")
	}
	if !strings.Contains(string(after), "codegraph") {
		t.Fatal("the new global entry was not appended")
	}
	backups := 0
	entries, _ := os.ReadDir(filepath.Dir(opts.ConfigPath))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "forebrain.yaml.bak-") {
			backups++
		}
	}
	if backups != 1 {
		t.Fatalf("backups = %d", backups)
	}
	if string(before) == string(after) && backups == 0 {
		t.Fatal("config unchanged and no backup")
	}

	projectFile := filepath.Join(project, ".forebrain", "mcp_servers.yaml")
	body, err := os.ReadFile(projectFile)
	if err != nil {
		t.Fatalf("project file missing: %v", err)
	}
	if !strings.Contains(string(body), "caveman-shrink") {
		t.Fatalf("project file = %s", body)
	}
	// The entry's env values are configuration the server needs, so the file
	// carries them; the REPORT is where values must never appear.
	if !strings.Contains(string(body), "SECRET_TOKEN") {
		t.Fatalf("env key missing from the project file: %s", body)
	}
	joined := (&Report{Source: KindClaude, MCP: outcomes, Notices: notices, Projects: projects}).Text()
	if strings.Contains(joined, "s3cr3t") {
		t.Fatal("secret env value leaked into the report")
	}
	for _, want := range []string{"take effect for new sessions", ".gitignore", "asks before it first starts"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("notices missing %q:\n%s", want, joined)
		}
	}

	// Idempotent: a second import skips both entries.
	again, _, _, err := importMCP(data, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range again {
		if outcome.Status == "written" && outcome.Name != "" && outcome.Scope == "project" && outcome.Name == "caveman-shrink" {
			t.Fatalf("project entry rewritten: %+v", outcome)
		}
	}
	var dupes int
	body2, _ := os.ReadFile(projectFile)
	dupes = strings.Count(string(body2), "caveman-shrink")
	// name appears once per entry; the list serializer writes it once.
	if dupes > 2 {
		t.Fatalf("project entry duplicated %d times", dupes)
	}
}

// TestMergeInputHistoryArithmetic pins the acceptance: added + skipped ==
// source lines, ordering by timestamp, newline folding, and idempotency.
func TestMergeInputHistoryArithmetic(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "state", "cli-input-history.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("second question\nalready there\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := &claudeData{
		HistoryLines: 4,
		History: []claudeHistoryEntry{
			{Display: "first question", Timestamp: 100},
			{Display: "second question", Timestamp: 50},
			{Display: "third\nmultiline", Timestamp: 200},
			{Display: "already there", Timestamp: 300},
		},
	}
	opts := &Options{InputHistoryPath: target, Now: fixedClock}
	outcome, err := mergeInputHistory(data, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.SourceLines != 4 || outcome.Added != 2 || outcome.Skipped != 2 {
		t.Fatalf("outcome = %+v; added+skipped must equal source lines", outcome)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	// Appends keep timestamp order after the existing entries.
	if lines[len(lines)-2] != "first question" || lines[len(lines)-1] != "third multiline" {
		t.Fatalf("tail lines = %v", lines)
	}
	again, err := mergeInputHistory(data, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.Added != 0 || again.Skipped != 4 {
		t.Fatalf("second merge = %+v", again)
	}
}

// TestImportPlansAttribution covers slug-based project scoping, the
// unscoped fallback, and divergence protection.
func TestImportPlansAttribution(t *testing.T) {
	workspace := t.TempDir()
	project := t.TempDir()
	data := &claudeData{
		Projects: []claudeProject{{
			Sessions: []claudeSession{{SessionID: "s-1", Slug: "known-slug", Cwd: project}},
		}},
		Plans: []claudePlan{
			{Name: "known-slug", Path: writePlanFile(t, "# scoped\n")},
			{Name: "orphan-slug", Path: writePlanFile(t, "# orphan\n")},
		},
	}
	opts := &Options{AgentWorkspace: workspace, Now: fixedClock}
	outcomes := importPlans(data, opts, nil)
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if outcomes[0].ProjectKey != memory.ProjectKey(project) || outcomes[0].Status != "installed" {
		t.Fatalf("scoped = %+v", outcomes[0])
	}
	if outcomes[0].SessionID != "cli-s-1" {
		t.Fatalf("scoped session = %q, want cli-s-1", outcomes[0].SessionID)
	}
	if outcomes[1].ProjectKey != "" || outcomes[1].Status != "installed" || outcomes[1].SessionID != "" {
		t.Fatalf("orphan = %+v", outcomes[1])
	}
	scoped := filepath.Join(state.PlanDirForSession(workspace, memory.ProjectKey(project), "cli-s-1"), "known-slug.md")
	if body, err := os.ReadFile(scoped); err != nil || !strings.HasPrefix(string(body), "# scoped") {
		t.Fatalf("scoped plan missing: %v", err)
	}
	// The imported conversation, once resumed, reads the plan as its own.
	if plan, err := state.GetPlanForSession(workspace, memory.ProjectKey(project), "cli-s-1"); err != nil || !strings.HasPrefix(plan, "# scoped") {
		t.Fatalf("GetPlanForSession(cli-s-1) = %q, %v", plan, err)
	}
	orphan := filepath.Join(workspace, "plans", "orphan-slug.md")
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("orphan plan missing: %v", err)
	}

	// Divergence: a differing local copy is never overwritten.
	if err := os.WriteFile(scoped, []byte("# local edits\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := importPlans(data, opts, nil)
	if second[0].Status != "diverged" {
		t.Fatalf("divergence = %+v", second[0])
	}
	if body, _ := os.ReadFile(scoped); string(body) != "# local edits\n" {
		t.Fatal("diverged plan was overwritten")
	}
}

// TestImportPlansCopiesIntoEveryConversationWithTheSlug pins the owner
// decision 2026-10-09: a slug carried by several conversations gives each of
// them its own copy in its own plan directory, and nothing is written into
// the flat project directory no conversation reads anymore.
func TestImportPlansCopiesIntoEveryConversationWithTheSlug(t *testing.T) {
	workspace := t.TempDir()
	project := t.TempDir()
	data := &claudeData{
		Projects: []claudeProject{{
			Sessions: []claudeSession{
				{SessionID: "s-b", Slug: "shared", Cwd: project},
				{SessionID: "s-a", Slug: "shared", Cwd: project},
			},
		}},
		Plans: []claudePlan{
			{Name: "shared", Path: writePlanFile(t, "# Shared\n")},
		},
	}
	opts := &Options{AgentWorkspace: workspace, Now: fixedClock}
	outcomes := importPlans(data, opts, nil)
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if outcomes[0].SessionID != "cli-s-a" || outcomes[1].SessionID != "cli-s-b" {
		t.Fatalf("session order = %q, %q; want cli-s-a, cli-s-b", outcomes[0].SessionID, outcomes[1].SessionID)
	}
	if outcomes[0].Status != "installed" || outcomes[1].Status != "installed" {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	first, err := os.ReadFile(filepath.Join(state.PlanDirForSession(workspace, memory.ProjectKey(project), "cli-s-a"), "shared.md"))
	if err != nil {
		t.Fatalf("copy for cli-s-a missing: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(state.PlanDirForSession(workspace, memory.ProjectKey(project), "cli-s-b"), "shared.md"))
	if err != nil {
		t.Fatalf("copy for cli-s-b missing: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("copies differ: %q vs %q", first, second)
	}
	if _, err := os.Stat(filepath.Join(state.PlanDirForProject(workspace, memory.ProjectKey(project)), "shared.md")); err == nil {
		t.Fatal("shared.md must not land in the flat project plans directory")
	}

	// Re-running is up-to-date for every copy, never a duplicate.
	again := importPlans(data, opts, nil)
	if len(again) != 2 || again[0].Status != "up-to-date" || again[1].Status != "up-to-date" {
		t.Fatalf("second run = %+v", again)
	}
}

func writePlanFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestMCPJSONMapDecodes checks the raw server shape parses from the real
// .claude.json spelling.
func TestMCPJSONMapDecodes(t *testing.T) {
	body := `{"mcpServers":{"a":{"type":"stdio","command":"x","args":["y"],"env":{"K":"V"}}}}`
	var doc struct {
		MCPServers map[string]rawMCPServer `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	server := doc.MCPServers["a"]
	if server.Type != "stdio" || server.Command != "x" || len(server.Args) != 1 || server.Env["K"] != "V" {
		t.Fatalf("server = %+v", server)
	}
	converted := convertMCPServer("a", server)
	if converted.Transport != "stdio" || converted.Name != "a" {
		t.Fatalf("converted = %+v", converted)
	}
	http := convertMCPServer("b", rawMCPServer{Type: "http", URL: "https://x"})
	if http.Transport != "streamable_http" {
		t.Fatalf("http transport = %q", http.Transport)
	}
}

// buildFixtureClaudeProject builds a real project directory carrying the
// Claude-side project configuration: .mcp.json, .claude/settings.json and
// settings.local.json with permissions, plus the ~/.claude.json project
// entry naming it.
func buildFixtureClaudeProject(t *testing.T, home string) string {
	t.Helper()
	project := filepath.Join(t.TempDir(), "cfg-proj")
	if err := os.MkdirAll(filepath.Join(project, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".mcp.json"), []byte(`{
  "mcpServers": {
    "graph": {"type": "http", "url": "http://127.0.0.1:9/graph"},
    "off": {"type": "stdio", "command": "/bin/off"}
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	settings := `{
  "permissions": {
    "allow": ["Bash(go test *)", "Read"],
    "deny": ["Bash(rm -rf *)"],
    "ask": ["WebFetch"]
  },
  "enabledMcpjsonServers": [],
  "disabledMcpjsonServers": ["off"],
  "hooks": {"SessionStart": []}
}`
	if err := os.WriteFile(filepath.Join(project, ".claude", "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	local := `{
  "permissions": {
    "allow": ["Bash(go test *)"]
  },
  "enableAllProjectMcpServers": true
}`
	if err := os.WriteFile(filepath.Join(project, ".claude", "settings.local.json"), []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}
	// ~/.claude.json names the project with a machine-private entry.
	claudeJSON := `{
  "mcpServers": {},
  "projects": {
    "` + project + `": {
      "mcpServers": {"caveman-shrink": {"type": "stdio", "command": "npx", "args": ["caveman-shrink"]}},
      "enabledMcpjsonServers": [],
      "disabledMcpjsonServers": ["off"]
    }
  }
}`
	if err := os.WriteFile(filepath.Join(home, "..", ".claude.json"), []byte(claudeJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return project
}

// TestClaudeProjectConfigMigratedIntoForebrainFiles proves the 2026-09-17
// decision end to end on the Claude side: .mcp.json is COPIED into
// .forebrain/mcp_servers.yaml (disabled entries skipped, others pending
// confirmation), the machine-private ~/.claude.json entry lands in the same
// file, permissions become .forebrain/safety.json rules with mapped tool names,
// and enableAllProjectMcpServers does not become a blanket consent (§4.9).
func TestClaudeProjectConfigMigratedIntoForebrainFiles(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	project := buildFixtureClaudeProject(t, fixture.root)
	stubClaudeRoot(t, fixture)
	opts := fixture.options(t, false)
	opts.CurrentProjectRoot = project

	report, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 1. .mcp.json copied into forebrain's own file; forebrain no longer reads the
	// original (mcp.LoadProjectMCPServers sees only .forebrain's file).
	servers, _ := mcp.LoadProjectMCPServers(project)
	byName := map[string]appcfg.MCPServerConfig{}
	for _, srv := range servers {
		byName[strings.ToLower(srv.Name)] = srv
	}
	if len(servers) != 2 {
		t.Fatalf("project servers = %+v, want graph + caveman-shrink", servers)
	}
	if got := byName["graph"].URL; got != "http://127.0.0.1:9/graph" {
		t.Fatalf("graph url = %q", got)
	}
	if _, present := byName["off"]; present {
		t.Fatal("source-disabled entry must not be migrated")
	}
	if got := byName["caveman-shrink"].Command; got != "npx" {
		t.Fatalf("machine-private entry missing: %+v", byName["caveman-shrink"])
	}

	// 2. Permissions landed in safety.json with forebrain tool names.
	safetyBody, err := os.ReadFile(filepath.Join(project, ".forebrain", "safety.json"))
	if err != nil {
		t.Fatal(err)
	}
	var safetyDoc struct {
		Rules map[string][]safety.PermissionRuleValue `json:"rules"`
	}
	if err := json.Unmarshal(safetyBody, &safetyDoc); err != nil {
		t.Fatal(err)
	}
	var sawAllowShell, sawDenyShell, sawAskWebFetch, sawAllowRead bool
	for _, rule := range safetyDoc.Rules["allow"] {
		switch {
		case rule.ToolName == "shell" && rule.RuleContent == "go test *":
			sawAllowShell = true
		case rule.ToolName == "read_file" && rule.RuleContent == "":
			sawAllowRead = true
		}
	}
	for _, rule := range safetyDoc.Rules["deny"] {
		if rule.ToolName == "shell" && rule.RuleContent == "rm -rf *" {
			sawDenyShell = true
		}
	}
	for _, rule := range safetyDoc.Rules["ask"] {
		if rule.ToolName == "web_fetch" {
			sawAskWebFetch = true
		}
	}
	if !sawAllowShell || !sawAllowRead || !sawDenyShell || !sawAskWebFetch {
		t.Fatalf("safety rules incomplete: %s", safetyBody)
	}
	// The duplicate rule from settings.local.json must not double-write.
	var allowCount int
	for _, rule := range safetyDoc.Rules["allow"] {
		if rule.ToolName == "shell" && rule.RuleContent == "go test *" {
			allowCount++
		}
	}
	if allowCount != 1 {
		t.Fatalf("duplicate allow rule written %d times", allowCount)
	}

	// 3. The report states the §4.9.5 facts: the copy is announced, the
	// pending confirmation is carried on the entry, and the blanket-allow
	// refusal is a notice.
	var notes []string
	for _, written := range report.Projects {
		notes = append(notes, written.Notes...)
	}
	notices := strings.Join(notes, "\n")
	for _, want := range []string{
		"Forebrain Harness does not read .mcp.json",
		"each server still asks once",
	} {
		if !strings.Contains(notices, want) {
			t.Fatalf("notice %q missing from:\n%s", want, notices)
		}
	}
	var sawPending bool
	for _, entry := range report.MCP {
		if entry.Name == "graph" && strings.Contains(entry.Detail, "needs your confirmation") {
			sawPending = true
		}
	}
	if !sawPending {
		t.Fatalf("graph entry missing its pending-confirmation detail: %+v", report.MCP)
	}
}

// TestClaudeProjectConfigIdempotent re-runs the migration and expects
// up-to-date/skipped outcomes with byte-identical files (§4.9.5).
func TestClaudeProjectConfigIdempotent(t *testing.T) {
	ctx := context.Background()
	fixture := buildFixtureHome(t)
	project := buildFixtureClaudeProject(t, fixture.root)
	stubClaudeRoot(t, fixture)
	opts := fixture.options(t, false)
	opts.CurrentProjectRoot = project
	if _, err := RunClaude(ctx, opts, nil); err != nil {
		t.Fatal(err)
	}
	mcpPath := filepath.Join(project, ".forebrain", "mcp_servers.yaml")
	first, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	report, err := RunClaude(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("re-run rewrote the project file:\n%s\n---\n%s", first, second)
	}
	for _, entry := range report.MCP {
		if entry.Scope == "project" && entry.Status == "written" {
			t.Fatalf("re-run rewrote %q", entry.Name)
		}
	}
}

// A project the import writes nothing into is not reported: what project
// files mean is said only about projects that get them.
func TestImportProjectMCPReportsOnlyProjectsItWritesInto(t *testing.T) {
	empty := t.TempDir()
	withServer := t.TempDir()
	if err := os.WriteFile(filepath.Join(withServer, ".mcp.json"), []byte(`{"mcpServers":{"graph":{"command":"graph-mcp"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	data := &claudeData{JSONProjectPaths: []string{empty, withServer}}
	opts := &Options{Home: t.TempDir(), DryRun: true}
	_, projects := importProjectMCP(data, opts, nil)
	if len(projects) != 1 || projects[0].Path != withServer {
		t.Fatalf("projects = %+v, want only the one with a server to copy", projects)
	}
	if projects[0].Unloaded != "not a version-controlled project" {
		t.Fatalf("unloaded = %q", projects[0].Unloaded)
	}
}
