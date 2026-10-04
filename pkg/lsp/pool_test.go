package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

func boolPtr(b bool) *bool { return &b }

// countRecv counts the recorded messages with method.
func countRecv(recs []map[string]any, method string) int {
	n := 0
	for _, rec := range recs {
		if recv, ok := rec["recv"].(map[string]any); ok && recv["method"] == method {
			n++
		}
	}
	return n
}

// fakeServerCfg is one fake server wired through the configuration (the
// plan's test shape): a custom entry whose command is the compiled fake,
// keyed to its own extension, scripted through FAKE_LSP_SCRIPT.
type fakeServerCfg struct {
	id     string
	ext    string
	script map[string]any // extra script fields; "record" is injected
	cfg    func(*appcfg.LSPServerConfig)
}

// fakeEnv is a pool, its first manager and the fake servers' record paths.
type fakeEnv struct {
	pool    *Pool
	manager *Manager
	project string
	lsp     appcfg.LSPSection
	records map[string]string // server id → record path
}

// newFakePool builds the pool over one or more configured fake servers; the
// pool closes with the test.
func newFakePool(t *testing.T, lsp appcfg.LSPSection, servers ...fakeServerCfg) fakeEnv {
	t.Helper()
	return newFakePoolIn(t, t.TempDir(), lsp, servers...)
}

// newFakePoolIn is newFakePool over a project directory the test created, so
// a script can name paths inside the project before the pool exists.
func newFakePoolIn(t *testing.T, project string, lsp appcfg.LSPSection, servers ...fakeServerCfg) fakeEnv {
	t.Helper()
	if len(servers) == 0 {
		t.Fatal("newFakePool needs at least one server")
	}
	exe := buildFakeServer(t)
	entries := map[string]appcfg.LSPServerConfig{}
	records := map[string]string{}
	for _, s := range servers {
		script := map[string]any{}
		for k, v := range s.script {
			script[k] = v
		}
		record := filepath.Join(t.TempDir(), "record.jsonl")
		script["record"] = record
		blob, err := json.Marshal(script)
		if err != nil {
			t.Fatalf("marshal fake script: %v", err)
		}
		entry := appcfg.LSPServerConfig{
			Command:             exe,
			ExtensionToLanguage: map[string]string{s.ext: s.id},
			Env:                 map[string]string{"FAKE_LSP_SCRIPT": string(blob)},
		}
		if s.cfg != nil {
			s.cfg(&entry)
		}
		entries[s.id] = entry
		records[s.id] = record
	}
	lsp.Servers = entries
	p := NewPool(&appcfg.Root{LSP: lsp})
	t.Cleanup(func() { _ = p.Close() })
	m := p.NewManager(ManagerOptions{
		Home:           t.TempDir(),
		AgentWorkspace: t.TempDir(),
		ProjectRoot:    project,
		Trusted:        true,
	})
	waitForBackgroundWriters(t, m)
	return fakeEnv{pool: p, manager: m, project: project, lsp: lsp, records: records}
}

// diagScript is the standard push fake: one diagnostic per marker substring,
// published on didOpen and didChange.
func diagScript(rules map[string]any) map[string]any {
	return map[string]any{
		"diagnostics": map[string]any{
			"on":    []any{"didOpen", "didChange"},
			"rules": rules,
		},
	}
}

// diagRule is one fake-server diagnostic.
func diagRule(message string, line int) map[string]any {
	return map[string]any{
		"range": map[string]any{
			"start": map[string]any{"line": line, "character": 0},
			"end":   map[string]any{"line": line, "character": 4},
		},
		"severity": 1,
		"source":   "fake",
		"message":  message,
	}
}

// mustWrite puts a file on disk — the edit tools write before DidWrite
// (spec §8.3.1), and root resolution resolves real paths.
func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// waitForPoll runs cond until it holds or the deadline passes.
func waitForPoll(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// pidOf reads the fake server's recorded pid.
func pidOf(t *testing.T, record string) int {
	t.Helper()
	recs := pollRecord(t, record, func(recs []map[string]any) bool {
		_, ok := recordInt(recs, "pid")
		return ok
	}, "the server pid")
	pid, _ := recordInt(recs, "pid")
	return pid
}

// waitForBackgroundWriters lets the manager's orphan sweep and any pending
// binary detections settle, so t.TempDir cleanup never races their writes.
func waitForBackgroundWriters(t *testing.T, m *Manager) {
	t.Helper()
	waitForPoll(t, "the orphan sweep to settle", func() bool {
		if m.opts.AgentWorkspace == "" {
			return true
		}
		_, err := os.Stat(filepath.Join(StateDir(m.opts.AgentWorkspace), "pids.json"))
		return err == nil
	})
	waitForPoll(t, "detections to settle", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.detecting) == 0
	})
}

func TestPoolCloseClosesManagers(t *testing.T) {
	p := NewPool(&appcfg.Root{})
	m1 := p.NewManager(ManagerOptions{ProjectRoot: "/p/one"})
	m2 := p.NewManager(ManagerOptions{ProjectRoot: "/p/two"})

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for i, m := range []*Manager{m1, m2} {
		if !m.closed {
			t.Fatalf("manager %d still open after pool Close", i)
		}
		// A closed manager's Subscribe hands back a callable cancel, so a
		// surface that subscribed before the pool closed never panics.
		cancel := m.Subscribe(func(event.LSPSnapshot) {})
		if cancel == nil {
			t.Fatalf("manager %d: nil cancel from Subscribe", i)
		}
		cancel()
	}
	// Idempotent: a second Close must not panic or error.
	if err := p.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestNewManagerAfterCloseIsClosed(t *testing.T) {
	p := NewPool(&appcfg.Root{})
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	m := p.NewManager(ManagerOptions{})
	if !m.closed {
		t.Fatal("a manager built after pool Close is not closed")
	}
}

func TestReconcileAdoptsConfig(t *testing.T) {
	first := &appcfg.Root{}
	p := NewPool(first)
	if p.config() != first {
		t.Fatal("config() must return the configuration NewPool stored")
	}

	next := &appcfg.Root{Features: appcfg.FeaturesSection{LSP: boolPtr(false)}}
	p.Reconcile(next)
	if p.config() != next {
		t.Fatal("Reconcile did not adopt the reloaded configuration")
	}

	// A nil reload must not replace the configuration in force.
	p.Reconcile(nil)
	if p.config() != next {
		t.Fatal("Reconcile(nil) replaced the configuration in force")
	}
}

func TestAcquireSharesInstance(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: diagScript(map[string]any{
			"bad":   []any{diagRule("bad thing", 0)},
			"worse": []any{diagRule("worse thing", 1)},
		}),
	})
	m2 := env.pool.NewManager(ManagerOptions{
		Home:           t.TempDir(),
		AgentWorkspace: t.TempDir(),
		ProjectRoot:    env.project,
		Trusted:        true,
	})
	defer m2.Close()

	file := filepath.Join(env.project, "a.fk")
	mustWrite(t, file, "contains bad\n")
	if delta := env.manager.DidWrite(context.Background(), "s1", []tool.FileChange{{AbsPath: file, After: []byte("contains bad\n")}}); delta.Text == "" {
		t.Fatalf("first DidWrite reported nothing; the server did not run")
	}
	if delta := m2.DidWrite(context.Background(), "s2", []tool.FileChange{{AbsPath: file, After: []byte("contains bad and worse\n")}}); delta.Text == "" {
		t.Fatalf("second DidWrite reported nothing; the shared server did not answer")
	}
	// Both managers used one instance: one initialize, ever.
	waitForPoll(t, "exactly one initialize", func() bool {
		return countRecv(readRecord(t, env.records["fake"]), "initialize") == 1
	})
}

func TestMaxServersEvictsIdle(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{MaxServers: 1},
		fakeServerCfg{id: "fake1", ext: ".f1", script: map[string]any{}},
		fakeServerCfg{id: "fake2", ext: ".f2", script: map[string]any{}},
	)
	var srv1, srv2 ServerConfig
	for _, sc := range env.manager.servers() {
		switch sc.ID {
		case "fake1":
			srv1 = sc
		case "fake2":
			srv2 = sc
		}
	}
	ctx := context.Background()
	entry1, err := env.pool.acquire(ctx, env.manager, srv1, env.project)
	if err != nil {
		t.Fatalf("acquire fake1: %v", err)
	}
	pid1 := pidOf(t, env.records["fake1"])

	// While fake1 is in flight nothing can be evicted: the second acquire
	// must refuse with the appendix C text.
	env.pool.begin(entry1)
	_, err = env.pool.acquire(ctx, env.manager, srv2, env.project)
	if err == nil || !strings.Contains(err.Error(), "too many language servers are running (1); raise lsp.max_servers or stop one with /lsp") {
		t.Fatalf("acquire under the cap error = %v", err)
	}
	env.pool.end(entry1)

	// Idle again, fake2's acquire evicts fake1.
	if _, err := env.pool.acquire(ctx, env.manager, srv2, env.project); err != nil {
		t.Fatalf("acquire fake2: %v", err)
	}
	waitForProcessGone(t, pid1)
}

func TestMaxServersCountsInstancesNotKeys(t *testing.T) {
	// One server serving two roots through folder reuse is one instance
	// listed under two table keys; the cap counts distinct instances
	// (spec §7.5), so a second server still fits max_servers=2.
	env := newFakePool(t, appcfg.LSPSection{MaxServers: 2},
		fakeServerCfg{
			id:  "fake",
			ext: ".fk",
			script: map[string]any{
				"capabilities": map[string]any{
					"workspace": map[string]any{
						"workspaceFolders": map[string]any{"changeNotifications": true},
					},
				},
				"diagnostics": map[string]any{
					"on": []any{"didOpen", "didChange"},
					"rules": map[string]any{
						"bad":   []any{diagRule("bad thing", 0)},
						"worse": []any{diagRule("worse thing", 1)},
					},
				},
			},
			cfg: func(c *appcfg.LSPServerConfig) { c.RootMarkers = []string{"marker.txt"} },
		},
		fakeServerCfg{id: "other", ext: ".f2", script: map[string]any{}},
	)
	for _, name := range []string{"sub1/a.fk", "sub2/b.fk"} {
		dir := filepath.Join(env.project, filepath.Dir(name))
		mustWrite(t, filepath.Join(dir, "marker.txt"), "")
		path := filepath.Join(env.project, name)
		mustWrite(t, path, "contains bad\n")
		if delta := env.manager.DidWrite(context.Background(), "", []tool.FileChange{{AbsPath: path, After: []byte("contains bad\n")}}); delta.Text == "" {
			t.Fatalf("DidWrite of %s reported nothing", path)
		}
	}
	env.pool.mu.Lock()
	keys := len(env.pool.instances)
	seen := map[*poolInstance]bool{}
	for _, e := range env.pool.instances {
		seen[e] = true
	}
	env.pool.mu.Unlock()
	if keys != 2 || len(seen) != 1 {
		t.Fatalf("reuse left %d keys over %d instances, want 2 keys over 1", keys, len(seen))
	}

	var other ServerConfig
	for _, sc := range env.manager.servers() {
		if sc.ID == "other" {
			other = sc
		}
	}
	if _, err := env.pool.acquire(context.Background(), env.manager, other, env.project); err != nil {
		t.Fatalf("acquire other under the cap: %v (one multi-root instance must count once)", err)
	}
	// The reused instance survived: another write introduces a new problem
	// and still answers with one initialize, not a relaunch after eviction.
	path := filepath.Join(env.project, "sub1/a.fk")
	if delta := env.manager.DidWrite(context.Background(), "", []tool.FileChange{{AbsPath: path, After: []byte("still worse\n")}}); delta.Text == "" {
		t.Fatalf("DidWrite after the cap reported nothing; the reused instance was evicted")
	}
	if n := countRecv(readRecord(t, env.records["fake"]), "initialize"); n != 1 {
		t.Fatalf("fake initialized %d times, want 1 (the multi-root instance must not be evicted)", n)
	}
}

func TestJanitorStopsIdle(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: diagScript(map[string]any{"bad": []any{diagRule("bad thing", 0)}}),
	})
	file := filepath.Join(env.project, "a.fk")
	mustWrite(t, file, "contains bad\n")
	if delta := env.manager.DidWrite(context.Background(), "", []tool.FileChange{{AbsPath: file, After: []byte("contains bad\n")}}); delta.Text == "" {
		t.Fatal("DidWrite reported nothing; the server did not run")
	}
	pid := pidOf(t, env.records["fake"])

	// One janitor step with a clock past the idle timeout (the default is
	// 10 minutes; the minimum the config allows is 60s).
	env.pool.janitorOnce(time.Now().Add(11 * time.Minute))
	waitForProcessGone(t, pid)
}

func TestReconcileRestartsOnFingerprintChange(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: diagScript(map[string]any{"bad": []any{diagRule("bad thing", 0)}}),
		cfg:    func(c *appcfg.LSPServerConfig) { c.Args = []string{"--one"} },
	})
	file := filepath.Join(env.project, "a.fk")
	mustWrite(t, file, "contains bad\n")
	if delta := env.manager.DidWrite(context.Background(), "", []tool.FileChange{{AbsPath: file, After: []byte("contains bad\n")}}); delta.Text == "" {
		t.Fatal("first DidWrite reported nothing; the server did not run")
	}
	pid := pidOf(t, env.records["fake"])

	// Same server, different args: a different fingerprint, so the instance
	// stops and the next use starts a fresh one.
	next := env.lsp
	entries := map[string]appcfg.LSPServerConfig{}
	for id, e := range next.Servers {
		entries[id] = e
	}
	changed := entries["fake"]
	changed.Args = []string{"--two"}
	entries["fake"] = changed
	next.Servers = entries
	env.pool.Reconcile(&appcfg.Root{LSP: next})
	waitForProcessGone(t, pid)

	if delta := env.manager.DidWrite(context.Background(), "", []tool.FileChange{{AbsPath: file, After: []byte("contains bad\n")}}); delta.Text == "" {
		t.Fatal("second DidWrite reported nothing; the new server did not run")
	}
	waitForPoll(t, "two initializes", func() bool {
		return countRecv(readRecord(t, env.records["fake"]), "initialize") == 2
	})
}

func TestPoolCloseStopsEverything(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: diagScript(map[string]any{"bad": []any{diagRule("bad thing", 0)}}),
	})
	file := filepath.Join(env.project, "a.fk")
	mustWrite(t, file, "contains bad\n")
	if delta := env.manager.DidWrite(context.Background(), "", []tool.FileChange{{AbsPath: file, After: []byte("contains bad\n")}}); delta.Text == "" {
		t.Fatal("DidWrite reported nothing; the server did not run")
	}
	pid := pidOf(t, env.records["fake"])
	if err := env.pool.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitForProcessGone(t, pid)
}

func TestMultiRootReuse(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities": map[string]any{
				"workspace": map[string]any{
					"workspaceFolders": map[string]any{"changeNotifications": true},
				},
			},
			"diagnostics": map[string]any{
				"on":    []any{"didOpen", "didChange"},
				"rules": map[string]any{"bad": []any{diagRule("bad thing", 0)}},
			},
		},
		cfg: func(c *appcfg.LSPServerConfig) { c.RootMarkers = []string{"marker.txt"} },
	})
	sub1 := filepath.Join(env.project, "sub1")
	sub2 := filepath.Join(env.project, "sub2")
	for _, dir := range []string{sub1, sub2} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte(""), 0o644); err != nil {
			t.Fatalf("marker: %v", err)
		}
	}
	for _, tc := range []struct{ dir, name string }{{sub1, "a.fk"}, {sub2, "b.fk"}} {
		path := filepath.Join(tc.dir, tc.name)
		mustWrite(t, path, "contains bad\n")
		if delta := env.manager.DidWrite(context.Background(), "", []tool.FileChange{{AbsPath: path, After: []byte("contains bad\n")}}); delta.Text == "" {
			t.Fatalf("DidWrite of %s reported nothing", path)
		}
	}
	// One process for both roots, told about the second folder.
	waitForPoll(t, "one initialize and one folder change", func() bool {
		recs := readRecord(t, env.records["fake"])
		return countRecv(recs, "initialize") == 1 && findRecv(recs, "workspace/didChangeWorkspaceFolders") != nil
	})
}

func TestInstanceForServerPrefersOpenDocument(t *testing.T) {
	// Two instances of one server (no folder changes): the one holding
	// the problem's file open is the instance that produced the problem,
	// so its synced content must render the position, not a random one.
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: diagScript(map[string]any{"bad": []any{diagRule("bad thing", 0)}}),
	})
	var srv ServerConfig
	for _, sc := range env.manager.servers() {
		if sc.ID == "fake" {
			srv = sc
		}
	}
	rootA := filepath.Join(env.project, "a")
	rootB := filepath.Join(env.project, "b")
	mustWrite(t, filepath.Join(rootA, "a.fk"), "x\n")
	mustWrite(t, filepath.Join(rootB, "b.fk"), "y\n")
	ctx := context.Background()
	eA, err := env.pool.acquire(ctx, env.manager, srv, rootA)
	if err != nil {
		t.Fatalf("acquire root A: %v", err)
	}
	eB, err := env.pool.acquire(ctx, env.manager, srv, rootB)
	if err != nil {
		t.Fatalf("acquire root B: %v", err)
	}
	if eA == eB {
		t.Fatal("the fake does not watch folders; two roots must be two instances")
	}
	pathB := filepath.Join(rootB, "b.fk")
	if _, err := eB.docs.OpenWith(ctx, pathB, []byte("y\n")); err != nil {
		t.Fatalf("open b.fk on root B: %v", err)
	}
	view := env.pool.instanceForServer("fake", pathB)
	if view == nil {
		t.Fatal("no instance answered")
	}
	if view.root != rootB {
		t.Fatalf("instance for an open document has root %s, want %s", view.root, rootB)
	}
	// With no instance holding the file open, any running one answers.
	if view := env.pool.instanceForServer("fake", filepath.Join(rootA, "a.fk")); view == nil {
		t.Fatal("no fallback instance answered")
	}
}
