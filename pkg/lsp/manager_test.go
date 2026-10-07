package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// pollPeekLate waits until the session's late queue has something to say.
func pollPeekLate(t *testing.T, m *Manager, sid string) (text string, token uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		text, token = m.PeekLate(sid)
		if text != "" {
			return text, token
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for late diagnostics for session %s", sid)
	return "", 0
}

func TestManagerZeroValueOnEmptyConfig(t *testing.T) {
	p := NewPool(nil)
	defer p.Close()
	m := p.NewManager(ManagerOptions{ProjectRoot: "/p", Trusted: true})
	defer m.Close()

	if m.Handles("/p/a.go") {
		t.Fatal("no enabled server handles anything")
	}
	if delta := m.DidWrite(context.Background(), "s1", []tool.FileChange{{AbsPath: "/p/a.go", After: []byte("x")}}); !delta.Empty() {
		t.Fatalf("DidWrite reported %q, want nothing", delta.Text)
	}
	if text, token := m.PeekLate("s1"); text != "" || token != 0 {
		t.Fatalf("PeekLate = (%q, %d), want empty", text, token)
	}
	m.DidRead(context.Background(), "/p/a.go", nil)
	m.DidRunShell(context.Background())
	m.AckLate("s1", 1)
	m.ReleaseIdle()
}

func TestDidWriteReportsNewProblemsOnly(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: diagScript(map[string]any{
			"alpha": []any{diagRule("alpha problem", 0)},
			"beta":  []any{diagRule("beta problem", 1)},
		}),
	})
	file := filepath.Join(env.project, "a.fk")
	mustWrite(t, file, "alpha\n")
	delta := env.manager.DidWrite(context.Background(), "s1", []tool.FileChange{{
		AbsPath: file,
		Before:  []byte("alpha\n"),
		After:   []byte("alpha\nbeta\n"),
	}})
	lines := strings.Split(delta.Text, "\n")
	if len(lines) < 3 {
		t.Fatalf("text =\n%s", delta.Text)
	}
	if !strings.HasPrefix(delta.Text, "<diagnostics>") {
		t.Fatalf("text does not open the diagnostics block:\n%s", delta.Text)
	}
	if lines[1] != "1 new problem after this edit (fake)" {
		t.Fatalf("first line = %q", lines[1])
	}
	if !strings.Contains(delta.Text, "beta problem") {
		t.Errorf("the new problem is missing:\n%s", delta.Text)
	}
	if strings.Contains(delta.Text, "alpha problem") {
		t.Errorf("the baseline problem leaked in:\n%s", delta.Text)
	}
	if delta.Summary.New != 1 || len(delta.Summary.Servers) != 1 || delta.Summary.Servers[0] != "fake" {
		t.Errorf("summary = %+v", delta.Summary)
	}
}

func TestDidWriteNoNewProblemsAddsNothing(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: diagScript(map[string]any{
			"alpha": []any{diagRule("alpha problem", 0)},
			"beta":  []any{diagRule("beta problem", 1)},
		}),
	})
	file := filepath.Join(env.project, "a.fk")
	mustWrite(t, file, "alpha\nbeta\n")
	delta := env.manager.DidWrite(context.Background(), "s1", []tool.FileChange{{
		AbsPath: file,
		Before:  []byte("alpha\nbeta\n"),
		After:   []byte("beta\nalpha\n"),
	}})
	if !delta.Empty() {
		t.Fatalf("an edit that introduces nothing reported:\n%s", delta.Text)
	}
}

func TestDidWriteWindowExpiresToPendingAndLate(t *testing.T) {
	lsp := appcfg.LSPSection{}
	lsp.Diagnostics.WaitMS = intPtr(100)
	env := newFakePool(t, lsp, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"diagnostics": map[string]any{
				"on":       []any{"didOpen", "didChange"},
				"delay_ms": 400,
				"rules":    map[string]any{"bad": []any{diagRule("bad thing", 0)}},
			},
		},
	})
	file := filepath.Join(env.project, "a.fk")
	mustWrite(t, file, "contains bad\n")
	delta := env.manager.DidWrite(context.Background(), "s1", []tool.FileChange{{AbsPath: file, After: []byte("contains bad\n")}})
	if !strings.Contains(delta.Text, "are still being computed") {
		t.Fatalf("the expired window must leave a pending line:\n%s", delta.Text)
	}
	if len(delta.Summary.PendingFiles) != 1 {
		t.Errorf("summary = %+v", delta.Summary)
	}

	text, token := pollPeekLate(t, env.manager, "s1")
	if !strings.HasPrefix(text, "Language server diagnostics changed") {
		t.Fatalf("late text =\n%s", text)
	}
	if !strings.Contains(text, "bad thing") {
		t.Fatalf("the late problem is missing:\n%s", text)
	}
	env.manager.AckLate("s1", token)
	if text, token := env.manager.PeekLate("s1"); text != "" || token != 0 {
		t.Fatalf("after AckLate PeekLate = (%q, %d), want empty", text, token)
	}
}

// warmInstance starts one server directly, so a following DidWrite finds a
// ready instance and only its wait behavior is under test. The root goes
// through ResolveRoot, the way DidWrite computes it.
func warmInstance(t *testing.T, env fakeEnv) {
	t.Helper()
	var srv ServerConfig
	for _, sc := range env.manager.servers() {
		if sc.ID == "fake" {
			srv = sc
		}
	}
	if srv.ID == "" {
		t.Fatal("no fake server in the configuration")
	}
	probe := filepath.Join(env.project, "warm.fk")
	mustWrite(t, probe, "warm\n")
	root, ok := ResolveRoot(probe, env.project, srv)
	if !ok {
		t.Fatalf("ResolveRoot rejected %s", probe)
	}
	if _, err := env.pool.acquire(context.Background(), env.manager, srv, root); err != nil {
		t.Fatalf("warm acquire: %v", err)
	}
}

func TestDidWriteWaitZero(t *testing.T) {
	lsp := appcfg.LSPSection{}
	lsp.Diagnostics.WaitMS = intPtr(0)
	env := newFakePool(t, lsp, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"diagnostics": map[string]any{
				"on":       []any{"didOpen", "didChange"},
				"delay_ms": 50,
				"rules":    map[string]any{"bad": []any{diagRule("bad thing", 0)}},
			},
		},
	})
	warmInstance(t, env)
	file := filepath.Join(env.project, "a.fk")
	mustWrite(t, file, "contains bad\n")
	start := time.Now()
	delta := env.manager.DidWrite(context.Background(), "s1", []tool.FileChange{{AbsPath: file, After: []byte("contains bad\n")}})
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("wait_ms 0 returned after %s; the window must not wait", elapsed)
	}
	if !strings.Contains(delta.Text, "are still being computed") {
		t.Fatalf("wait_ms 0 must pend everything:\n%s", delta.Text)
	}
	// The diagnostics that arrive afterwards go the late way.
	if _, token := pollPeekLate(t, env.manager, "s1"); token == 0 {
		t.Fatal("the late queue answered with token 0")
	}
}

func TestDidWritePullMode(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"capabilities":     map[string]any{"diagnosticProvider": true},
			"pull_diagnostics": true,
			"diagnostics": map[string]any{
				"rules": map[string]any{"bad": []any{diagRule("bad thing", 0)}},
			},
		},
	})
	file := filepath.Join(env.project, "a.fk")
	mustWrite(t, file, "contains bad\n")
	delta := env.manager.DidWrite(context.Background(), "s1", []tool.FileChange{{AbsPath: file, After: []byte("contains bad\n")}})
	if !strings.Contains(delta.Text, "1 new problem after this edit (fake)") {
		t.Fatalf("the pull server's problem is missing:\n%s", delta.Text)
	}
	if strings.Contains(delta.Text, "still being computed") {
		t.Fatalf("a pull that answered must not be pending:\n%s", delta.Text)
	}
}

func TestDidWriteBaselineUnavailable(t *testing.T) {
	lsp := appcfg.LSPSection{}
	lsp.Diagnostics.WaitMS = intPtr(1000)
	env := newFakePool(t, lsp, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"diagnostics": map[string]any{
				"on": []any{"didChange"}, // didOpen never publishes: no baseline
				"rules": map[string]any{
					"alpha": []any{diagRule("alpha problem", 0)},
					"beta":  []any{diagRule("beta problem", 1)},
				},
			},
		},
	})
	file := filepath.Join(env.project, "a.fk")
	mustWrite(t, file, "alpha\n")
	delta := env.manager.DidWrite(context.Background(), "s1", []tool.FileChange{{
		AbsPath: file,
		Before:  []byte("alpha\n"),
		After:   []byte("alpha\nbeta\n"),
	}})
	lines := strings.Split(delta.Text, "\n")
	if len(lines) < 2 {
		t.Fatalf("text =\n%s", delta.Text)
	}
	if lines[1] != "2 problems reported after this edit (fake); earlier problems could not be told apart" {
		t.Fatalf("first line = %q", lines[1])
	}
	if !delta.Summary.BaselineUnavailable {
		t.Errorf("summary = %+v", delta.Summary)
	}
}

func TestDidWriteCrossFile(t *testing.T) {
	project := t.TempDir()
	bURI := PathToURI(filepath.Join(project, "b.fk"))
	env := newFakePoolIn(t, project, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"diagnostics": map[string]any{
				"on":    []any{"didOpen", "didChange"},
				"rules": map[string]any{"bad": []any{diagRule("bad thing", 0)}},
			},
			// The server also pushes a problem for a file the edit never
			// touched, a little after it initialized.
			"after_initialized": []any{map[string]any{
				"delay_ms": 150,
				"notify":   "textDocument/publishDiagnostics",
				"params": map[string]any{
					"uri":         bURI,
					"diagnostics": []any{diagRule("cross problem", 0)},
				},
			}},
		},
	})
	a := filepath.Join(project, "a.fk")
	mustWrite(t, a, "contains bad\n")
	delta := env.manager.DidWrite(context.Background(), "s1", []tool.FileChange{{AbsPath: a, After: []byte("contains bad\n")}})
	ia, ib := strings.Index(delta.Text, "a.fk"), strings.Index(delta.Text, "b.fk")
	if ia < 0 || ib < 0 {
		t.Fatalf("both files must appear in the report:\n%s", delta.Text)
	}
	if ia > ib {
		t.Fatalf("the edited file must come first:\n%s", delta.Text)
	}
	if !strings.Contains(delta.Text, "bad thing") || !strings.Contains(delta.Text, "cross problem") {
		t.Fatalf("a problem is missing:\n%s", delta.Text)
	}
	if delta.Summary.New != 2 || delta.Summary.Files != 2 {
		t.Errorf("summary = %+v", delta.Summary)
	}
}

func TestDidWriteNoServerIsFree(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "other", ext: ".other", script: map[string]any{},
	})
	file := filepath.Join(env.project, "a.fk")
	start := time.Now()
	delta := env.manager.DidWrite(context.Background(), "s1", []tool.FileChange{{AbsPath: file, After: []byte("x\n")}})
	if !delta.Empty() {
		t.Fatalf("an unowned file reported:\n%s", delta.Text)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("an unowned file waited %s", elapsed)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(env.records["other"]); !os.IsNotExist(err) {
		t.Fatal("a server was started although no file was owned")
	}
}

func TestDidWriteUntrustedOrOff(t *testing.T) {
	script := diagScript(map[string]any{"bad": []any{diagRule("bad thing", 0)}})

	off := appcfg.Root{Features: appcfg.FeaturesSection{LSP: boolPtr(false)}}
	p := NewPool(&off)
	m := p.NewManager(ManagerOptions{ProjectRoot: "/p", Trusted: true, AgentWorkspace: t.TempDir()})
	if delta := m.DidWrite(context.Background(), "s1", []tool.FileChange{{AbsPath: "/p/a.fk", After: []byte("x")}}); !delta.Empty() {
		t.Fatalf("features.lsp false reported:\n%s", delta.Text)
	}
	waitForBackgroundWriters(t, m)
	_ = p.Close()

	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{id: "fake", ext: ".fk", script: script})
	untrusted := env.pool.NewManager(ManagerOptions{
		Home:           t.TempDir(),
		AgentWorkspace: t.TempDir(),
		ProjectRoot:    env.project,
		Trusted:        false,
	})
	if delta := untrusted.DidWrite(context.Background(), "s1", []tool.FileChange{{AbsPath: filepath.Join(env.project, "a.fk"), After: []byte("x")}}); !delta.Empty() {
		t.Fatalf("an untrusted project reported:\n%s", delta.Text)
	}
	waitForBackgroundWriters(t, untrusted)
	untrusted.Close()
}

func TestDidWriteCancel(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"diagnostics": map[string]any{
				"on":       []any{"didOpen", "didChange"},
				"delay_ms": 600,
				"rules":    map[string]any{"bad": []any{diagRule("bad thing", 0)}},
			},
		},
	})
	file := filepath.Join(env.project, "a.fk")
	mustWrite(t, file, "before\n")
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	done := make(chan tool.DiagnosticsDelta, 1)
	go func() {
		done <- env.manager.DidWrite(ctx, "s1", []tool.FileChange{{AbsPath: file, Before: []byte("before\n"), After: []byte("contains bad\n")}})
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a canceled window did not return")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("a canceled window took %s", elapsed)
	}
	// The write itself is the caller's business: the file content on disk is
	// whatever the caller wrote, untouched by DidWrite.
}

func TestLateDeliveryOffRecordsNothing(t *testing.T) {
	lsp := appcfg.LSPSection{}
	lsp.Diagnostics.LateDelivery = boolPtr(false)
	lsp.Diagnostics.WaitMS = intPtr(0)
	env := newFakePool(t, lsp, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"diagnostics": map[string]any{
				"on":       []any{"didOpen", "didChange"},
				"delay_ms": 50,
				"rules":    map[string]any{"bad": []any{diagRule("bad thing", 0)}},
			},
		},
	})
	file := filepath.Join(env.project, "a.fk")
	env.manager.DidWrite(context.Background(), "s1", []tool.FileChange{{AbsPath: file, After: []byte("contains bad\n")}})
	// The publish lands after the window; nothing may be recorded.
	time.Sleep(300 * time.Millisecond)
	if text, token := env.manager.PeekLate("s1"); text != "" || token != 0 {
		t.Fatalf("late_delivery false recorded (%q, %d)", text, token)
	}
}

func TestDidReadNeverStarts(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: map[string]any{
			"diagnostics": map[string]any{
				"on":    []any{"didOpen", "didChange"},
				"rules": map[string]any{"bad": []any{diagRule("bad thing", 0)}},
			},
		},
	})
	aPath := filepath.Join(env.project, "a.fk")
	if err := os.WriteFile(aPath, []byte("contains bad\n"), 0o644); err != nil {
		t.Fatalf("write a.fk: %v", err)
	}
	bPath := filepath.Join(env.project, "b.fk")
	if err := os.WriteFile(bPath, []byte("contains bad\n"), 0o644); err != nil {
		t.Fatalf("write b.fk: %v", err)
	}

	// No instance runs: a read must not start one.
	env.manager.DidRead(context.Background(), aPath, []byte("contains bad\n"))
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(env.records["fake"]); !os.IsNotExist(err) {
		t.Fatal("DidRead started a server")
	}

	// With an instance running, the read opens the document.
	if delta := env.manager.DidWrite(context.Background(), "", []tool.FileChange{{AbsPath: bPath, After: []byte("contains bad\n")}}); delta.Text == "" {
		t.Fatal("DidWrite reported nothing; the server did not run")
	}
	env.manager.DidRead(context.Background(), aPath, []byte("contains bad\n"))
	uri := PathToURI(aPath)
	pollRecord(t, env.records["fake"], func(recs []map[string]any) bool {
		for _, rec := range recs {
			recv, _ := rec["recv"].(map[string]any)
			if recv["method"] != "textDocument/didOpen" {
				continue
			}
			params, _ := recv["params"].(map[string]any)
			td, _ := params["textDocument"].(map[string]any)
			if td["uri"] == uri {
				return true
			}
		}
		return false
	}, "didOpen of a.fk")
}

func TestDidRunShellResyncsOpenDocs(t *testing.T) {
	env := newFakePool(t, appcfg.LSPSection{}, fakeServerCfg{
		id: "fake", ext: ".fk",
		script: diagScript(map[string]any{"one": []any{diagRule("one problem", 0)}}),
	})
	path := filepath.Join(env.project, "a.fk")
	mustWrite(t, path, "one\n")
	if delta := env.manager.DidWrite(context.Background(), "", []tool.FileChange{{AbsPath: path, After: []byte("one\n")}}); delta.Text == "" {
		t.Fatalf("DidWrite reported nothing; the server did not run")
	}
	// A shell command changes the file behind the runtime's back.
	if err := os.WriteFile(path, []byte("two\n"), 0o644); err != nil {
		t.Fatalf("rewrite a.fk: %v", err)
	}
	env.manager.DidRunShell(context.Background())
	pollRecord(t, env.records["fake"], func(recs []map[string]any) bool {
		return countRecv(recs, "textDocument/didChange") >= 1
	}, "a didChange after the shell")
}

func TestGitSweepKeepsExistingFilesAsChanged(t *testing.T) {
	// A path leaving `git status` is not necessarily deleted: git add and
	// git commit drop a file that still exists from the listing. Only a
	// path gone from disk may be reported as an LSP deletion (type 3).
	dir := t.TempDir()
	// git answers the physical path; on macOS the temp dir is reached
	// through a /var -> /private/var symlink, so normalize up front.
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "t")
	env := newFakePoolIn(t, dir, appcfg.LSPSection{}, fakeServerCfg{id: "fake", ext: ".fk", script: map[string]any{}})

	// Snapshot 1: a.fk staged. A commit then removes it from the listing
	// while it still exists on disk.
	a := filepath.Join(dir, "a.fk")
	mustWrite(t, a, "one\n")
	git("add", "a.fk")
	env.manager.gitChanges()
	git("commit", "-q", "-m", "one")
	assertSweepType(t, env.manager.gitChanges(), a, 2)

	// Snapshot: b.fk untracked; deleting it also removes it from the
	// listing — that is a real deletion.
	b := filepath.Join(dir, "b.fk")
	mustWrite(t, b, "two\n")
	env.manager.gitChanges()
	if err := os.Remove(b); err != nil {
		t.Fatalf("remove b.fk: %v", err)
	}
	assertSweepType(t, env.manager.gitChanges(), b, 3)
}

func assertSweepType(t *testing.T, changes []gitChange, path string, want int) {
	t.Helper()
	for _, ch := range changes {
		if ch.path == path {
			if ch.typ != want {
				t.Fatalf("%s sweep type = %d, want %d", path, ch.typ, want)
			}
			return
		}
	}
	t.Fatalf("%s missing from sweep changes %+v", path, changes)
}

func TestExpandBracesParallelGroups(t *testing.T) {
	// The first `{` pairs with its own `}`, not the last one in the
	// pattern: parallel groups each expand.
	got := expandBraces("x{1,2}/y{a,b}")
	want := []string{"x1/ya", "x1/yb", "x2/ya", "x2/yb"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expandBraces(x{1,2}/y{a,b}) = %v, want %v", got, want)
	}
	got = expandBraces("{a,b{1,2}}")
	want = []string{"a", "b1", "b2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expandBraces({a,b{1,2}}) = %v, want %v", got, want)
	}
	if got := expandBraces("plain"); !reflect.DeepEqual(got, []string{"plain"}) {
		t.Fatalf("expandBraces(plain) = %v", got)
	}
	if got := expandBraces("x{1"); !reflect.DeepEqual(got, []string{"x{1"}) {
		t.Fatalf("expandBraces(x{1) = %v", got)
	}
}

// integrationCase is one real-server case of spec §1.4: a minimal project a
// language's toolchain recognizes directly, one call of a function defined
// in defFile, and the edit that introduces exactly one error into file.
type integrationCase struct {
	server  string            // catalog id
	files   map[string]string // fixture project, written under t.TempDir()
	file    string            // relative path used for the queries
	line    int               // 1-based line holding symbol
	symbol  string            // a call of a function defined in defFile
	defFile string            // where definition must point
	broken  string            // content of file that introduces one error
	settle  time.Duration     // extra wait for slow servers (rust-analyzer's cargo check)
	prepare []string          // argv run in the fixture before the server starts (no shell)
}

// integrationCases are the spec §1.4 acceptance projects. The fixtures are
// string constants on purpose: no testdata (spec §3.14).
var integrationCases = []integrationCase{
	{
		server: "gopls",
		files: map[string]string{
			"go.mod":  "module example.com/fixture\n\ngo 1.22\n",
			"calc.go": "package main\n\nfunc Add(a, b int) int { return a + b }\n",
			"main.go": "package main\n\nfunc main() { _ = Add(1, 2) }\n",
		},
		file: "main.go", line: 3, symbol: "Add", defFile: "calc.go",
		broken: "package main\n\nfunc main() { _ = Add(1, \"x\") }\n",
	},
	{
		server: "pyright",
		files: map[string]string{
			"pyproject.toml": "[project]\nname = \"fixture\"\n",
			"calc.py":        "def add(a: int, b: int) -> int:\n    return a + b\n",
			"main.py":        "from calc import add\n\nprint(add(1, 2))\n",
		},
		file: "main.py", line: 3, symbol: "add", defFile: "calc.py",
		broken: "from calc import add\n\nprint(add(1))\n",
	},
	{
		server: "typescript-language-server",
		files: map[string]string{
			"tsconfig.json": "{\"compilerOptions\":{\"strict\":true}}\n",
			"package.json":  "{\"name\":\"fixture\",\"private\":true}\n",
			"calc.ts":       "export function add(a: number, b: number): number {\n  return a + b;\n}\n",
			"main.ts":       "import { add } from \"./calc\";\n\nconsole.log(add(1, 2));\n",
		},
		file: "main.ts", line: 3, symbol: "add", defFile: "calc.ts",
		broken: "import { add } from \"./calc\";\n\nconsole.log(add(1, \"x\"));\n",
		// tsserver resolves only from the workspace's node_modules, so the
		// fixture installs its own typescript (dotnet restore pattern).
		// Pinned to 5.x: 7.x ships no lib/tsserver.js, which is the only
		// layout typescript-language-server 5.3.0 resolves.
		prepare: []string{"npm", "install", "--no-save", "typescript@5"},
	},
	{
		server: "rust-analyzer",
		files: map[string]string{
			"Cargo.toml":  "[package]\nname = \"fixture\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
			"src/calc.rs": "pub fn add(a: i32, b: i32) -> i32 {\n    a + b\n}\n",
			"src/main.rs": "mod calc;\n\nfn main() {\n    println!(\"{}\", calc::add(1, 2));\n}\n",
		},
		file: "src/main.rs", line: 4, symbol: "add", defFile: "src/calc.rs",
		broken: "mod calc;\n\nfn main() {\n    println!(\"{}\", calc::add(1, \"x\"));\n}\n",
		settle: 90 * time.Second,
	},
	{
		server: "clangd",
		files: map[string]string{
			"compile_flags.txt": "-std=c11\n",
			"calc.h":            "int add(int a, int b);\n",
			"calc.c":            "#include \"calc.h\"\n\nint add(int a, int b) { return a + b; }\n",
			// Two calls: clangd's references answer does not add the
			// declaration for includeDeclaration, so the calls alone must
			// carry the "at least two results" assertion.
			"main.c": "#include \"calc.h\"\n\nint main(void) { return add(1, 2) + add(3, 4); }\n",
		},
		file: "main.c", line: 3, symbol: "add", defFile: "calc.h",
		broken: "#include \"calc.h\"\n\nint main(void) { return add(1, undefined_name) + add(3, 4); }\n",
		settle: 30 * time.Second, // the background index warms up
	},
	{
		server: "clangd",
		files: map[string]string{
			"compile_flags.txt": "-std=c++17\n",
			"calc.hpp":          "#pragma once\n\nint add(int a, int b);\n",
			"calc.cpp":          "#include \"calc.hpp\"\n\nint add(int a, int b) { return a + b; }\n",
			"main.cpp":          "#include \"calc.hpp\"\n\nint main() { return add(1, 2) + add(3, 4); }\n",
		},
		file: "main.cpp", line: 3, symbol: "add", defFile: "calc.hpp",
		broken: "#include \"calc.hpp\"\n\nint main() { return add(1, undefined_name) + add(3, 4); }\n",
		settle: 30 * time.Second, // the background index warms up
	},
	{
		server: "jdtls",
		files: map[string]string{
			"pom.xml": "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" +
				"<project xmlns=\"http://maven.apache.org/POM/4.0.0\">\n" +
				"  <modelVersion>4.0.0</modelVersion>\n" +
				"  <groupId>com.example</groupId>\n" +
				"  <artifactId>fixture</artifactId>\n" +
				"  <version>1.0.0</version>\n" +
				"  <properties>\n" +
				"    <maven.compiler.source>17</maven.compiler.source>\n" +
				"    <maven.compiler.target>17</maven.compiler.target>\n" +
				"  </properties>\n" +
				"</project>\n",
			"src/main/java/com/example/Calc.java": "package com.example;\n\npublic final class Calc {\n    public static int add(int a, int b) {\n        return a + b;\n    }\n}\n",
			"src/main/java/com/example/Main.java": "package com.example;\n\npublic final class Main {\n    public static void main(String[] args) {\n        System.out.println(Calc.add(1, 2));\n    }\n}\n",
		},
		file: "src/main/java/com/example/Main.java", line: 5, symbol: "add",
		defFile: "src/main/java/com/example/Calc.java",
		broken:  "package com.example;\n\npublic final class Main {\n    public static void main(String[] args) {\n        System.out.println(Calc.add(1, \"x\"));\n    }\n}\n",
		settle:  60 * time.Second,
	},
	{
		server: "kotlin-lsp",
		files: map[string]string{
			"settings.gradle.kts":     "rootProject.name = \"fixture\"\n",
			"build.gradle.kts":        "plugins {\n    kotlin(\"jvm\") version \"2.0.21\"\n}\n\nrepositories {\n    mavenCentral()\n}\n",
			"src/main/kotlin/Calc.kt": "package com.example\n\nfun add(a: Int, b: Int): Int = a + b\n",
			"src/main/kotlin/Main.kt": "package com.example\n\nfun main() {\n    println(add(1, 2))\n}\n",
		},
		file: "src/main/kotlin/Main.kt", line: 4, symbol: "add",
		defFile: "src/main/kotlin/Calc.kt",
		broken:  "package com.example\n\nfun main() {\n    println(add(1, \"x\"))\n}\n",
		settle:  90 * time.Second,
	},
	{
		server: "sourcekit-lsp",
		files: map[string]string{
			"Package.swift":              "// swift-tools-version:5.9\nimport PackageDescription\n\nlet package = Package(\n    name: \"fixture\",\n    targets: [\n        .target(name: \"fixture\"),\n    ]\n)\n",
			"Sources/fixture/calc.swift": "func add(_ a: Int, _ b: Int) -> Int {\n    a + b\n}\n",
			"Sources/fixture/main.swift": "print(add(1, 2))\n",
		},
		file: "Sources/fixture/main.swift", line: 1, symbol: "add",
		defFile: "Sources/fixture/calc.swift",
		broken:  "print(add(1, \"x\"))\n",
		settle:  60 * time.Second,
		// Cross-file definition needs the module built (and its index).
		prepare: []string{"swift", "build"},
	},
	{
		server: "metals",
		files: map[string]string{
			"build.sbt":                 "ThisBuild / scalaVersion := \"3.3.3\"\n",
			"project/build.properties":  "sbt.version=1.10.1\n",
			"src/main/scala/calc.scala": "def add(a: Int, b: Int): Int = a + b\n",
			"src/main/scala/main.scala": "@main def run(): Unit =\n  println(add(1, 2))\n",
		},
		file: "src/main/scala/main.scala", line: 2, symbol: "add",
		defFile: "src/main/scala/calc.scala",
		broken:  "@main def run(): Unit =\n  println(add(1, \"x\"))\n",
		settle:  120 * time.Second,
	},
	{
		server: "csharp-ls",
		files: map[string]string{
			"fixture.csproj": "<Project Sdk=\"Microsoft.NET.Sdk\">\n  <PropertyGroup>\n    <TargetFramework>net8.0</TargetFramework>\n    <OutputType>Exe</OutputType>\n  </PropertyGroup>\n</Project>\n",
			"Calc.cs":        "namespace Fixture;\n\npublic static class Calc\n{\n    public static int Add(int a, int b) => a + b;\n}\n",
			"Main.cs":        "namespace Fixture;\n\npublic static class Program\n{\n    public static void Main() => System.Console.WriteLine(Calc.Add(1, 2));\n}\n",
		},
		file: "Main.cs", line: 5, symbol: "Add", defFile: "Calc.cs",
		broken:  "namespace Fixture;\n\npublic static class Program\n{\n    public static void Main() => System.Console.WriteLine(Calc.Add(1, \"x\"));\n}\n",
		prepare: []string{"dotnet", "restore"},
	},
	{
		server: "intelephense",
		files: map[string]string{
			"composer.json": "{\n  \"name\": \"example/fixture\",\n  \"description\": \"fixture\"\n}\n",
			"calc.php":      "<?php\n\nfunction add(int $a, int $b): int\n{\n    return $a + $b;\n}\n",
			"main.php":      "<?php\n\nrequire 'calc.php';\n\necho add(1, 2);\n",
		},
		file: "main.php", line: 5, symbol: "add", defFile: "calc.php",
		broken: "<?php\n\nrequire 'calc.php';\n\necho add(1, undefined_thing());\n",
	},
}

// TestIntegrationRealServers drives the real language servers through the
// spec §1.4 acceptance: definition / references / hover / document_symbols
// answer, an edit introduces exactly one error the tool result carries, and
// the fix makes it disappear. They run only when FOREBRAIN_LSP_INTEGRATION
// lists the servers to exercise ("all" for every case), and skip a case
// whose binary is missing unless FOREBRAIN_LSP_INTEGRATION_REQUIRE=1.
func TestIntegrationRealServers(t *testing.T) {
	selected := strings.TrimSpace(os.Getenv("FOREBRAIN_LSP_INTEGRATION"))
	if selected == "" {
		t.Skip("set FOREBRAIN_LSP_INTEGRATION to run real language servers")
	}
	require := os.Getenv("FOREBRAIN_LSP_INTEGRATION_REQUIRE") == "1"
	only := map[string]bool{}
	if selected != "all" {
		for _, id := range strings.Split(selected, ",") {
			if id = strings.TrimSpace(id); id != "" {
				only[id] = true
			}
		}
	}
	for _, c := range integrationCases {
		c := c
		name := c.server + "/" + strings.TrimPrefix(filepath.Ext(c.file), ".")
		t.Run(name, func(t *testing.T) {
			if len(only) > 0 && !only[c.server] {
				t.Skipf("not selected: %s", c.server)
			}
			srv, installed := integrationServer(t, c.server)
			if !installed {
				if require {
					t.Fatalf("%s is not installed", c.server)
				}
				t.Skipf("%s is not installed", c.server)
			}
			runIntegrationCase(t, c, srv)
		})
	}
}

// integrationServer answers the catalog entry and whether its binary is
// installed, without touching the detect cache.
func integrationServer(t *testing.T, id string) (ServerConfig, bool) {
	t.Helper()
	for _, sc := range ResolveServers(ResolveInput{Config: &appcfg.Root{}, GOOS: runtime.GOOS}) {
		if sc.ID != id {
			continue
		}
		env, _ := BuildEnv(EnvSpec{Passthrough: sc.EnvPassthrough, Env: sc.Env})
		return sc, Detect(context.Background(), sc, env, runtime.GOOS, "").Installed
	}
	t.Fatalf("no catalog server %q", id)
	return ServerConfig{}, false
}

// runIntegrationCase walks one case through the Manager the tools use.
func runIntegrationCase(t *testing.T, c integrationCase, srv ServerConfig) {
	t.Helper()
	dir := t.TempDir()
	// Resolve the symlink macOS puts over its temp directories: servers
	// canonicalize the paths they report back (clangd, jdtls), and a
	// symlinked project root would file every answer under a path the
	// diagnostics store never looks up.
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	dir = canonical
	for name, content := range c.files {
		mustWrite(t, filepath.Join(dir, filepath.FromSlash(name)), content)
	}
	if len(c.prepare) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, c.prepare[0], c.prepare[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(c.prepare, " "), err, out)
		}
	}

	cfg := &appcfg.Root{}
	cfg.LSP.Servers = map[string]appcfg.LSPServerConfig{c.server: {Enabled: boolPtr(true)}}
	wait := 30000
	cfg.LSP.Diagnostics.WaitMS = &wait
	cfg.LSP.RequestTimeout = 300
	pool := NewPool(cfg)
	t.Cleanup(func() { _ = pool.Close() })
	m := pool.NewManager(ManagerOptions{
		Home:              t.TempDir(),
		AgentWorkspace:    t.TempDir(),
		ProjectRoot:       dir,
		Trusted:           true,
		VersionControlled: true,
	})

	ctx := context.Background()
	file := filepath.Join(dir, filepath.FromSlash(c.file))

	// Slow servers answer their first queries only once indexing finishes,
	// so each assertion retries within the settle budget (rust-analyzer's
	// cargo check, clangd's background index, a first gradle import).
	query := func(what string, q tool.CodeIntelQuery, accept func(tool.CodeIntelResult) error) {
		t.Helper()
		deadline := time.Now().Add(c.settle + time.Minute)
		var last string
		for {
			res, err := m.Query(ctx, q)
			switch {
			case err != nil:
				last = "error: " + err.Error()
			default:
				if problem := accept(res); problem == nil {
					return
				} else {
					last = problem.Error() + "\n" + res.Text
				}
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("%s: %s", what, last)
			}
			time.Sleep(500 * time.Millisecond)
		}
	}

	query("definition of "+c.symbol, tool.CodeIntelQuery{
		Operation: tool.LSPOpDefinition, AbsPath: file, Line: c.line, Symbol: c.symbol,
		PreviewAllowed: allowAll,
	}, func(res tool.CodeIntelResult) error {
		if !strings.Contains(res.Text, c.defFile) {
			return fmt.Errorf("no %s in the result", c.defFile)
		}
		return nil
	})

	query("references of "+c.symbol, tool.CodeIntelQuery{
		Operation: tool.LSPOpReferences, AbsPath: file, Line: c.line, Symbol: c.symbol,
		IncludeDeclaration: true, PreviewAllowed: allowAll,
	}, func(res tool.CodeIntelResult) error {
		if n, _ := res.Display["result_count"].(int); n < 2 {
			return fmt.Errorf("only %d results", n)
		}
		return nil
	})

	query("hover over "+c.symbol, tool.CodeIntelQuery{
		Operation: tool.LSPOpHover, AbsPath: file, Line: c.line, Symbol: c.symbol,
		PreviewAllowed: allowAll,
	}, func(res tool.CodeIntelResult) error {
		if strings.Contains(res.Text, "no hover information") {
			return fmt.Errorf("no information")
		}
		return nil
	})

	query("document_symbols of "+c.defFile, tool.CodeIntelQuery{
		Operation: tool.LSPOpDocumentSymbols, AbsPath: filepath.Join(dir, filepath.FromSlash(c.defFile)),
		PreviewAllowed: allowAll,
	}, func(res tool.CodeIntelResult) error {
		if !strings.Contains(res.Text, c.symbol) {
			return fmt.Errorf("no %s in the symbols", c.symbol)
		}
		return nil
	})

	// The edit that introduces one error (spec §8.3 end to end).
	orig := c.files[c.file]
	mustWrite(t, file, c.broken)
	delta := m.DidWrite(ctx, "it-session", []tool.FileChange{{AbsPath: file, Before: []byte(orig), After: []byte(c.broken)}})
	if delta.Summary.New < 1 && !containsString(delta.Summary.PendingFiles, c.file) {
		t.Fatalf("the broken edit reported nothing: %+v\n%s", delta.Summary, delta.Text)
	}
	integrationWaitDiagCount(t, ctx, m, file, c.settle, 1)

	// The fix: the diagnostics must disappear again.
	mustWrite(t, file, orig)
	delta = m.DidWrite(ctx, "it-session", []tool.FileChange{{AbsPath: file, Before: []byte(c.broken), After: []byte(orig)}})
	if delta.Summary.New != 0 {
		t.Fatalf("the fixing edit reported new problems: %+v\n%s", delta.Summary, delta.Text)
	}
	integrationWaitDiagCount(t, ctx, m, file, c.settle, 0)
}

// integrationWaitDiagCount polls the diagnostics operation until the file's
// problem count reaches want (any count ≥ 1 when want is 1, exactly 0 when
// want is 0), within settle plus a minute.
func integrationWaitDiagCount(t *testing.T, ctx context.Context, m *Manager, file string, settle time.Duration, want int) {
	t.Helper()
	deadline := time.Now().Add(settle + time.Minute)
	var last string
	for time.Now().Before(deadline) {
		res, err := m.Query(ctx, tool.CodeIntelQuery{Operation: tool.LSPOpDiagnostics, AbsPath: file})
		if err != nil {
			last = "error: " + err.Error()
		} else {
			last = res.Text
			// The count sits on the "diagnostics for <file>: N" line; note
			// lines (e.g. "still indexing") are prepended to the answer, so
			// scan for that line instead of trusting the first one.
			for _, line := range strings.Split(res.Text, "\n") {
				if !strings.HasPrefix(line, "diagnostics for ") {
					continue
				}
				if i := strings.LastIndex(line, ": "); i >= 0 {
					if n, perr := strconv.Atoi(strings.TrimSpace(line[i+2:])); perr == nil {
						if (want > 0 && n >= want) || (want == 0 && n == 0) {
							return
						}
					}
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("diagnostics for %s never reached %d (waited %s); last answer:\n%s",
		filepath.Base(file), want, settle+time.Minute, last)
}

// A project entry overrides a configured server only after the operator
// consented to that exact fingerprint: until then the global entry stands,
// after it the merged entry carries project scope, and the fingerprint change
// closes the running instance at the next reconcile (spec §5.2, §5.3).
func TestProjectEntryAppliesAfterConsent(t *testing.T) {
	project := t.TempDir()
	exe := buildFakeServer(t)
	record := filepath.Join(t.TempDir(), "record.jsonl")
	script, err := json.Marshal(map[string]any{
		"record": record,
		"diagnostics": map[string]any{
			"on":    []any{"didOpen", "didChange"},
			"rules": map[string]any{"bad": []any{diagRule("bad thing", 0)}},
		},
	})
	if err != nil {
		t.Fatalf("marshal fake script: %v", err)
	}
	writeProjectLSP(t, project, "servers:\n  fake:\n    args: [\"--project-mode\"]\n")
	ws := t.TempDir()
	cfgRoot := &appcfg.Root{LSP: appcfg.LSPSection{Servers: map[string]appcfg.LSPServerConfig{
		"fake": {
			Command:             exe,
			Args:                []string{"--global-mode"},
			ExtensionToLanguage: map[string]string{".fk": "fake"},
			Env:                 map[string]string{"FAKE_LSP_SCRIPT": string(script)},
		},
	}}}
	p := NewPool(cfgRoot)
	t.Cleanup(func() { _ = p.Close() })
	m := p.NewManager(ManagerOptions{
		Home:           t.TempDir(),
		AgentWorkspace: ws,
		ProjectRoot:    project,
		ProjectKey:     "projkey",
		Trusted:        true,
	})
	waitForBackgroundWriters(t, m)

	before := findServer(t, m.servers(), "fake")
	if before.Scope != "global" || len(before.Args) != 1 || before.Args[0] != "--global-mode" {
		t.Fatalf("before consent: scope=%s args=%v", before.Scope, before.Args)
	}
	if state := m.projectState(); len(state.Pending) != 1 || state.Pending[0].ID != "fake" {
		t.Fatalf("pending=%+v", state.Pending)
	}

	// The instance runs on the global args.
	file := filepath.Join(project, "a.fk")
	mustWrite(t, file, "contains bad\n")
	if delta := m.DidWrite(context.Background(), "", []tool.FileChange{{AbsPath: file, After: []byte("contains bad\n")}}); delta.Text == "" {
		t.Fatal("first DidWrite reported nothing; the server did not run")
	}
	pid := pidOf(t, record)

	if err := DecideProjectServers(ws, project, "projkey", []string{"fake"}); err != nil {
		t.Fatalf("decide: %v", err)
	}
	after := findServer(t, m.servers(), "fake")
	if after.Scope != "project" || len(after.Args) != 1 || after.Args[0] != "--project-mode" {
		t.Fatalf("after consent: scope=%s args=%v", after.Scope, after.Args)
	}
	if after.Fingerprint == before.Fingerprint {
		t.Fatal("the project override must change the configuration fingerprint")
	}
	if state := m.projectState(); len(state.Pending) != 0 || len(state.Allowed) != 1 {
		t.Fatalf("state after consent: allowed=%v pending=%+v", state.Allowed, state.Pending)
	}

	// The fingerprint change closes the old instance at the next reconcile;
	// the next use starts one on the project args.
	p.Reconcile(cfgRoot)
	waitForProcessGone(t, pid)
	if delta := m.DidWrite(context.Background(), "", []tool.FileChange{{AbsPath: file, After: []byte("contains bad\n")}}); delta.Text == "" {
		t.Fatal("second DidWrite reported nothing; the new server did not run")
	}
	waitForPoll(t, "two initializes", func() bool {
		return countRecv(readRecord(t, record), "initialize") == 2
	})
}

// A project entry's ${VAR} resolves only from ~/.forebrain/.env (spec §9.3):
// a variable that exists in the process environment but not in .env reaches
// the server as empty, because the project overlay marks the merged entry
// EnvFromProject.
func TestProjectEntryEnvFromDotEnvOnly(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	exe := buildFakeServer(t)
	script, err := json.Marshal(map[string]any{"echo_env": []string{"SECRET_FROM_ENV"}})
	if err != nil {
		t.Fatalf("marshal fake script: %v", err)
	}
	// The project entry carries the whole environment (the overlay replaces
	// env wholesale): the script plus the reference under test.
	writeProjectLSP(t, project, "servers:\n  fake:\n    command: "+exe+"\n    extension_to_language: {\".fk\": fake}\n    env:\n      FAKE_LSP_SCRIPT: |\n        "+strings.ReplaceAll(string(script), "\n", "\n        ")+"\n      SECRET_FROM_ENV: \"${SECRET_FROM_ENV}\"\n")
	t.Setenv("SECRET_FROM_ENV", "process-value")
	ws := t.TempDir()
	cfgRoot := &appcfg.Root{LSP: appcfg.LSPSection{}}
	p := NewPool(cfgRoot)
	t.Cleanup(func() { _ = p.Close() })
	m := p.NewManager(ManagerOptions{
		Home:           home,
		AgentWorkspace: ws,
		ProjectRoot:    project,
		ProjectKey:     "projkey",
		Trusted:        true,
	})
	waitForBackgroundWriters(t, m)
	if err := DecideProjectServers(ws, project, "projkey", []string{"fake"}); err != nil {
		t.Fatalf("decide: %v", err)
	}
	srv := findServer(t, m.servers(), "fake")
	if !srv.EnvFromProject {
		t.Fatalf("project entry must resolve ${VAR} from .env only: %+v", srv)
	}

	file := filepath.Join(project, "a.fk")
	mustWrite(t, file, "x\n")
	root, ok := ResolveRoot(file, project, srv)
	if !ok {
		t.Fatalf("no workspace root for %s", file)
	}
	entry, err := p.acquire(context.Background(), m, srv, root)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := entry.inst.WaitReady(ctx); err != nil {
		t.Fatalf("wait ready: %v", err)
	}
	var out map[string]string
	if err := entry.inst.Call(ctx, "fake/env", nil, &out); err != nil {
		t.Fatalf("fake/env: %v", err)
	}
	if v := out["SECRET_FROM_ENV"]; v != "" {
		t.Fatalf("process environment leaked into a project entry: SECRET_FROM_ENV=%q", v)
	}
}
