package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

func int32Ptr(v int32) *int32 { return &v }

// prob builds one Problem for the golden tests.
func prob(path string, sev, line, col int, source, code, message string) Problem {
	return Problem{Path: path, Severity: sev, Line: line, Character: col, Source: source, Code: code, Message: message}
}

// reportOpts is the ReportOptions every formatting test starts from.
func reportOpts() ReportOptions {
	return ReportOptions{
		ProjectRoot: "/proj",
		MinSeverity: SeverityThreshold("warning"),
		MaxPerFile:  20,
		MaxFiles:    10,
		Position:    func(p Problem) (int, int) { return p.Line + 1, p.Character + 1 },
	}
}

func TestStorePublishGetAndWait(t *testing.T) {
	s := NewDiagStore()
	if _, _, _, ok := s.Get("gopls", "/w/a.go"); ok {
		t.Fatal("Get before any publish succeeded")
	}

	// WaitFor blocks until the publish, then returns true.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- s.WaitFor(ctx, "gopls", "/w/a.go", 1, 0) }()
	select {
	case ok := <-done:
		t.Fatalf("WaitFor returned %v before the publish", ok)
	case <-time.After(60 * time.Millisecond):
	}
	s.Publish("gopls", "/w/a.go", int32Ptr(1), []Diagnostic{{
		Range:    Range{Start: Position{Line: 2, Character: 4}, End: Position{Line: 2, Character: 8}},
		Severity: 0, // unspecified: stored as error
		Source:   "fake",
		Code:     json.RawMessage(`42`),
		Message:  "boom",
	}})
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("WaitFor returned false after the publish")
		}
	case <-time.After(time.Second):
		t.Fatal("WaitFor did not return after the publish")
	}

	problems, version, seq, ok := s.Get("gopls", "/w/a.go")
	if !ok || len(problems) != 1 {
		t.Fatalf("Get = %v, %v", problems, ok)
	}
	p := problems[0]
	if p.ServerID != "gopls" || p.Path != "/w/a.go" || p.Severity != 1 ||
		p.Line != 2 || p.Character != 4 || p.Source != "fake" ||
		p.Code != "42" || p.Message != "boom" {
		t.Errorf("problem = %+v", p)
	}
	if version == nil || *version != 1 {
		t.Errorf("version = %v, want 1", version)
	}
	if seq != 1 || s.Seq() != 1 {
		t.Errorf("seq = %d, store seq = %d, want 1 and 1", seq, s.Seq())
	}

	// A minVersion no publish satisfies: wait out the context, return false.
	strict, scancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer scancel()
	if s.WaitFor(strict, "gopls", "/w/a.go", 5, s.Seq()) {
		t.Fatal("WaitFor with an unsatisfiable minVersion returned true")
	}

	// A publish carrying a high enough version wakes the waiter.
	woken := make(chan bool, 1)
	wctx, wcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer wcancel()
	go func() { woken <- s.WaitFor(wctx, "gopls", "/w/a.go", 5, s.Seq()) }()
	time.Sleep(30 * time.Millisecond)
	s.Publish("gopls", "/w/a.go", int32Ptr(7), nil)
	select {
	case ok := <-woken:
		if !ok {
			t.Fatal("WaitFor returned false after the version-7 publish")
		}
	case <-time.After(time.Second):
		t.Fatal("WaitFor did not wake after the version-7 publish")
	}

	// PublishedSince: only this server's files published after the marker.
	marker := s.Seq()
	s.Publish("gopls", "/w/c.go", nil, nil)
	s.Publish("other", "/w/d.go", nil, nil)
	if got := s.PublishedSince("gopls", marker); len(got) != 1 || got[0] != "/w/c.go" {
		t.Errorf("PublishedSince = %v, want [/w/c.go]", got)
	}

	// Subscribe fires per publish and stops after cancel.
	var mu sync.Mutex
	var seen []string
	stop := s.Subscribe(func(serverID, path string) {
		mu.Lock()
		seen = append(seen, serverID+" "+path)
		mu.Unlock()
	})
	s.Publish("gopls", "/w/e.go", nil, []Diagnostic{{Message: "x", Severity: 1}})
	stop()
	s.Publish("gopls", "/w/f.go", nil, []Diagnostic{{Message: "w", Severity: 2}})
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "gopls /w/e.go" {
		t.Errorf("subscriber saw %v, want one call for /w/e.go", seen)
	}

	// Counts: one error (e.go) and one warning (f.go); a.go was emptied by
	// the version-7 publish.
	errs, warns := s.Counts("gopls")
	if errs != 1 || warns != 1 {
		t.Errorf("Counts = %d errors, %d warnings, want 1 and 1", errs, warns)
	}

	// Forget drops a file.
	s.Forget("gopls", "/w/a.go")
	if _, _, _, ok := s.Get("gopls", "/w/a.go"); ok {
		t.Error("Get after Forget succeeded")
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	s := NewDiagStore()
	s.Publish("gopls", "/w/a.go", nil, []Diagnostic{{Message: "one", Severity: 1}})
	snapshot := s.Snapshot("gopls")
	s.Publish("gopls", "/w/a.go", nil, []Diagnostic{
		{Message: "one", Severity: 1},
		{Message: "two", Severity: 1},
	})
	if got := snapshot["/w/a.go"]; len(got) != 1 || got[0].Message != "one" {
		t.Errorf("snapshot changed after a later publish: %+v", got)
	}
	snapshot["/w/a.go"][0].Message = "mutated"
	problems, _, _, _ := s.Get("gopls", "/w/a.go")
	if len(problems) != 2 || problems[0].Message != "one" {
		t.Errorf("mutating the snapshot reached the store: %+v", problems)
	}
}

func TestWaitQuiet(t *testing.T) {
	t.Run("waits out the quiet period", func(t *testing.T) {
		s := NewDiagStore()
		start := time.Now()
		go func() {
			time.Sleep(50 * time.Millisecond)
			s.Publish("gopls", "/w/a.go", nil, nil)
			time.Sleep(50 * time.Millisecond)
			s.Publish("gopls", "/w/a.go", nil, nil)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.WaitQuiet(ctx, "gopls", 80*time.Millisecond, time.Time{})
		if elapsed := time.Since(start); elapsed < 180*time.Millisecond {
			t.Errorf("WaitQuiet returned after %v, want at least 180ms", elapsed)
		}
	})
	t.Run("an earlier deadline wins", func(t *testing.T) {
		s := NewDiagStore()
		start := time.Now()
		go func() {
			time.Sleep(50 * time.Millisecond)
			s.Publish("gopls", "/w/a.go", nil, nil)
			time.Sleep(50 * time.Millisecond)
			s.Publish("gopls", "/w/a.go", nil, nil)
		}()
		s.WaitQuiet(context.Background(), "gopls", 80*time.Millisecond, start.Add(150*time.Millisecond))
		elapsed := time.Since(start)
		if elapsed < 140*time.Millisecond || elapsed >= 178*time.Millisecond {
			t.Errorf("WaitQuiet returned after %v, want the 150ms deadline", elapsed)
		}
	})
}

func TestPullDiagnosticsWithFake(t *testing.T) {
	t.Run("pulls and stores", func(t *testing.T) {
		diag := map[string]any{
			"range": map[string]any{
				"start": map[string]any{"line": 2, "character": 4},
				"end":   map[string]any{"line": 2, "character": 8},
			},
			"severity": 1,
			"code":     42,
			"source":   "fake",
			"message":  "bad thing",
		}
		inst, record := startFake(t, map[string]any{
			"capabilities":     map[string]any{"diagnosticProvider": true},
			"pull_diagnostics": true,
			"diagnostics":      map[string]any{"rules": map[string]any{"bad": []any{diag}}},
		}, nil)
		path := filepath.Join(t.TempDir(), "p.go")
		docs := NewDocSync(inst, goLanguage, 0)
		if _, err := docs.OpenWith(context.Background(), path, []byte("contains bad here\n")); err != nil {
			t.Fatalf("OpenWith: %v", err)
		}
		waitForRecord(t, record, "textDocument/didOpen", 1)

		store := NewDiagStore()
		supported, err := PullDiagnostics(context.Background(), inst, store, "fake", path)
		if err != nil {
			t.Fatalf("PullDiagnostics: %v", err)
		}
		if !supported {
			t.Fatal("PullDiagnostics reported no capability although the server declared one")
		}
		problems, version, _, ok := store.Get("fake", path)
		if !ok || len(problems) != 1 {
			t.Fatalf("Get = %v, %v", problems, ok)
		}
		p := problems[0]
		if p.Path != filepath.Clean(path) || p.Severity != 1 || p.Line != 2 ||
			p.Character != 4 || p.Code != "42" || p.Source != "fake" || p.Message != "bad thing" {
			t.Errorf("problem = %+v", p)
		}
		if version != nil {
			t.Errorf("a pull publish carried version %v", *version)
		}
	})
	t.Run("no capability", func(t *testing.T) {
		inst, record := startFake(t, map[string]any{
			"capabilities": map[string]any{"definitionProvider": true},
		}, nil)
		store := NewDiagStore()
		supported, err := PullDiagnostics(context.Background(), inst, store, "fake", "/w/x.go")
		if err != nil {
			t.Fatalf("PullDiagnostics: %v", err)
		}
		if supported {
			t.Error("PullDiagnostics reported a capability the server does not have")
		}
		if _, _, _, ok := store.Get("fake", "/w/x.go"); ok {
			t.Error("the store was touched without a capability")
		}
		if findRecv(readRecord(t, record), "textDocument/diagnostic") != nil {
			t.Error("a diagnostic request was sent without a capability")
		}
	})
}

// A server that registers textDocument/diagnostic dynamically (pyright's
// pattern: it sees the client's pull capability, registers the provider and
// then never pushes publishDiagnostics) is pulled from the moment the
// registration goes live, and stops being pulled once it unregisters.
func TestPullDiagnosticsRegistration(t *testing.T) {
	diag := map[string]any{
		"range": map[string]any{
			"start": map[string]any{"line": 2, "character": 4},
			"end":   map[string]any{"line": 2, "character": 8},
		},
		"severity": 1,
		"code":     42,
		"source":   "fake",
		"message":  "bad thing",
	}
	inst, record := startFake(t, map[string]any{
		"capabilities": map[string]any{"definitionProvider": true},
		"after_initialized": []map[string]any{
			{"request": "client/registerCapability", "delay_ms": 500, "params": map[string]any{
				"registrations": []map[string]any{{
					"id":              "d1",
					"method":          "textDocument/diagnostic",
					"registerOptions": map[string]any{"interFileDependencies": true, "identifier": "fake"},
				}},
			}},
			{"request": "client/unregisterCapability", "delay_ms": 500, "params": map[string]any{
				"unregistrations": []map[string]any{{"id": "d1", "method": "textDocument/diagnostic"}},
			}},
		},
		"pull_diagnostics": true,
		"diagnostics":      map[string]any{"rules": map[string]any{"bad": []any{diag}}},
	}, nil)
	path := filepath.Join(t.TempDir(), "p.go")
	docs := NewDocSync(inst, goLanguage, 0)
	if _, err := docs.OpenWith(context.Background(), path, []byte("contains bad here\n")); err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	waitForRecord(t, record, "textDocument/didOpen", 1)
	store := NewDiagStore()

	// Static capabilities say nothing; the registration has not landed yet.
	if supportsPullDiagnostics(inst) {
		t.Fatal("supportsPullDiagnostics true before the registration landed")
	}
	supported, err := PullDiagnostics(context.Background(), inst, store, "fake", path)
	if err != nil {
		t.Fatalf("PullDiagnostics before the registration: %v", err)
	}
	if supported {
		t.Error("PullDiagnostics pulled before the registration landed")
	}
	if findRecv(readRecord(t, record), "textDocument/diagnostic") != nil {
		t.Error("a diagnostic request was sent before the registration landed")
	}

	// The registration opens the gate and the pull really fetches.
	deadline := time.Now().Add(5 * time.Second)
	for !supportsPullDiagnostics(inst) {
		if time.Now().After(deadline) {
			t.Fatal("registration never became live")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if supported, err = PullDiagnostics(context.Background(), inst, store, "fake", path); err != nil {
		t.Fatalf("PullDiagnostics after the registration: %v", err)
	}
	if !supported {
		t.Fatal("PullDiagnostics reported no capability although the server registered the provider")
	}
	problems, _, _, ok := store.Get("fake", path)
	if !ok || len(problems) != 1 || problems[0].Message != "bad thing" {
		t.Fatalf("Get after the pull = %v, %v", problems, ok)
	}

	// Unregistration closes the gate again.
	deadline = time.Now().Add(5 * time.Second)
	for supportsPullDiagnostics(inst) {
		if time.Now().After(deadline) {
			t.Fatal("registration never went away")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if supported, err = PullDiagnostics(context.Background(), inst, store, "fake", path); err != nil {
		t.Fatalf("PullDiagnostics after the unregistration: %v", err)
	}
	if supported {
		t.Error("PullDiagnostics pulled after the unregistration")
	}
	if n := countRecv(readRecord(t, record), "textDocument/diagnostic"); n != 1 {
		t.Errorf("%d diagnostic requests recorded after the unregistration, want 1", n)
	}
}

func TestNewProblemsIgnoresLineShift(t *testing.T) {
	at := func(line int, msg string) Problem { return Problem{Severity: 1, Line: line, Message: msg} }
	cases := []struct {
		name   string
		before []Problem
		after  []Problem
		want   []Problem // by message, in order
	}{
		{"a shifted line is the old problem", []Problem{at(10, "A")}, []Problem{at(12, "A"), {Severity: 2, Line: 3, Message: "B"}}, []Problem{{Severity: 2, Line: 3, Message: "B"}}},
		{"the later copy survives", []Problem{at(5, "A"), at(6, "A")}, []Problem{at(1, "A"), at(7, "A"), at(8, "A")}, []Problem{at(8, "A")}},
		{"a different code is new", []Problem{{Severity: 1, Code: "E1", Message: "m"}}, []Problem{{Severity: 1, Code: "E2", Message: "m"}}, []Problem{{Severity: 1, Code: "E2", Message: "m"}}},
		{"whitespace folds away", []Problem{{Severity: 1, Message: "m \n x"}}, []Problem{{Severity: 1, Message: "m x"}}, nil},
		{"severity participates", []Problem{{Severity: 1, Message: "m"}}, []Problem{{Severity: 2, Message: "m"}}, []Problem{{Severity: 2, Message: "m"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewProblems(tc.before, tc.after)
			if len(got) != len(tc.want) {
				t.Fatalf("NewProblems = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i].Message != tc.want[i].Message || got[i].Line != tc.want[i].Line ||
					got[i].Severity != tc.want[i].Severity || got[i].Code != tc.want[i].Code {
					t.Errorf("NewProblems[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestSeverityThreshold(t *testing.T) {
	for name, want := range map[string]int{
		"error": 1, "warning": 2, "information": 3, "hint": 4,
		"":         2,
		"nonsense": 2,
	} {
		if got := SeverityThreshold(name); got != want {
			t.Errorf("SeverityThreshold(%q) = %d, want %d", name, got, want)
		}
	}
}

func TestFormatEditDiagnosticsGolden(t *testing.T) {
	servers := []string{"gopls"}

	t.Run("one problem", func(t *testing.T) {
		text, summary := FormatEditDiagnostics(
			[]Problem{prob("/proj/main.go", 1, 2, 9, "compile", "E2", "boom")},
			nil, servers, false, reportOpts())
		want := "<diagnostics>\n" +
			"1 new problem after this edit (gopls)\n" +
			"main.go\n" +
			"  error 3:10 boom [compile E2]\n" +
			"</diagnostics>"
		if text != want {
			t.Errorf("text =\n%s\nwant\n%s", text, want)
		}
		if summary.New != 1 || summary.Files != 1 || summary.BaselineUnavailable {
			t.Errorf("summary = %+v", summary)
		}
		if len(summary.Items) != 1 || summary.Items[0] != (event.LSPDiagnostic{
			Path: "main.go", Line: 3, Column: 10, Severity: "error",
			Source: "compile", Code: "E2", Message: "boom",
		}) {
			t.Errorf("items = %+v", summary.Items)
		}
	})

	t.Run("edited file first, filters applied", func(t *testing.T) {
		opts := reportOpts()
		opts.FirstPaths = []string{"/proj/z/edit.go"}
		opts.MinSeverity = SeverityThreshold("information")
		text, summary := FormatEditDiagnostics([]Problem{
			prob("/proj/z/edit.go", 2, 4, 1, "", "", "second"),
			prob("/proj/z/edit.go", 1, 0, 0, "", "TS1", "first"),
			prob("/proj/a/x.go", 3, 2, 6, "", "", "third"),
			prob("/proj/a/x.go", 4, 0, 0, "gopls", "", "dropped hint"),
			prob("/elsewhere/y.go", 1, 0, 0, "gopls", "", "outside"),
		}, nil, []string{"gopls", "tsserver"}, false, opts)
		want := "<diagnostics>\n" +
			"3 new problems after this edit (gopls, tsserver)\n" +
			"z/edit.go\n" +
			"  error 1:1 first [TS1]\n" +
			"  warning 5:2 second\n" +
			"a/x.go\n" +
			"  info 3:7 third\n" +
			"</diagnostics>"
		if text != want {
			t.Errorf("text =\n%s\nwant\n%s", text, want)
		}
		if summary.New != 3 || summary.Files != 2 || len(summary.Items) != 3 {
			t.Errorf("summary = %+v", summary)
		}
		if summary.Items[0].Message != "first" || summary.Items[1].Message != "second" || summary.Items[2].Message != "third" {
			t.Errorf("items order = %+v", summary.Items)
		}
	})

	t.Run("brackets and multiline messages", func(t *testing.T) {
		text, _ := FormatEditDiagnostics([]Problem{
			prob("/proj/m.go", 1, 0, 0, "src", "42", "m1"),
			prob("/proj/m.go", 1, 1, 0, "src", "", "m2"),
			prob("/proj/m.go", 1, 2, 0, "", "E9", "m3"),
			prob("/proj/m.go", 1, 3, 0, "", "", "line1\r\nline2\nline3"),
		}, nil, servers, false, reportOpts())
		want := "<diagnostics>\n" +
			"4 new problems after this edit (gopls)\n" +
			"m.go\n" +
			"  error 1:1 m1 [src 42]\n" +
			"  error 2:1 m2 [src]\n" +
			"  error 3:1 m3 [E9]\n" +
			"  error 4:1 line1 / line2 / line3\n" +
			"</diagnostics>"
		if text != want {
			t.Errorf("text =\n%s\nwant\n%s", text, want)
		}
	})

	t.Run("max per file", func(t *testing.T) {
		opts := reportOpts()
		opts.MaxPerFile = 2
		text, summary := FormatEditDiagnostics([]Problem{
			prob("/proj/f.go", 1, 0, 0, "", "", "a"),
			prob("/proj/f.go", 1, 1, 0, "", "", "b"),
			prob("/proj/f.go", 1, 2, 0, "", "", "c"),
		}, nil, servers, false, opts)
		want := "<diagnostics>\n" +
			"3 new problems after this edit (gopls)\n" +
			"f.go\n" +
			"  error 1:1 a\n" +
			"  error 2:1 b\n" +
			"  … 1 more in this file not shown\n" +
			"</diagnostics>"
		if text != want {
			t.Errorf("text =\n%s\nwant\n%s", text, want)
		}
		if summary.New != 3 || len(summary.Items) != 2 {
			t.Errorf("summary = %+v", summary)
		}
	})

	t.Run("max files", func(t *testing.T) {
		opts := reportOpts()
		opts.MaxFiles = 2
		text, summary := FormatEditDiagnostics([]Problem{
			prob("/proj/a.go", 1, 0, 0, "", "", "a"),
			prob("/proj/b.go", 1, 0, 0, "", "", "b"),
			prob("/proj/c.go", 1, 0, 0, "", "", "c"),
		}, nil, servers, false, opts)
		want := "<diagnostics>\n" +
			"3 new problems after this edit (gopls)\n" +
			"a.go\n" +
			"  error 1:1 a\n" +
			"b.go\n" +
			"  error 1:1 b\n" +
			"… 1 more files not shown\n" +
			"</diagnostics>"
		if text != want {
			t.Errorf("text =\n%s\nwant\n%s", text, want)
		}
		if summary.New != 3 || summary.Files != 2 || len(summary.Items) != 2 {
			t.Errorf("summary = %+v", summary)
		}
	})

	t.Run("baseline unavailable", func(t *testing.T) {
		text, summary := FormatEditDiagnostics([]Problem{
			prob("/proj/main.go", 1, 2, 9, "compile", "E2", "boom"),
			prob("/proj/main.go", 1, 8, 0, "", "", "bam"),
		}, nil, servers, true, reportOpts())
		want := "<diagnostics>\n" +
			"2 problems reported after this edit (gopls); earlier problems could not be told apart\n" +
			"main.go\n" +
			"  error 3:10 boom [compile E2]\n" +
			"  error 9:1 bam\n" +
			"</diagnostics>"
		if text != want {
			t.Errorf("text =\n%s\nwant\n%s", text, want)
		}
		if !summary.BaselineUnavailable || summary.New != 2 {
			t.Errorf("summary = %+v", summary)
		}
	})

	t.Run("only pending", func(t *testing.T) {
		text, summary := FormatEditDiagnostics(nil,
			[]string{"/proj/p1.go", "/proj/p2.go"}, servers, false, reportOpts())
		want := "<diagnostics>\n" +
			"diagnostics for p1.go are still being computed and will follow\n" +
			"diagnostics for p2.go are still being computed and will follow\n" +
			"</diagnostics>"
		if text != want {
			t.Errorf("text =\n%s\nwant\n%s", text, want)
		}
		if summary.New != 0 || len(summary.PendingFiles) != 2 ||
			summary.PendingFiles[0] != "p1.go" || summary.PendingFiles[1] != "p2.go" {
			t.Errorf("summary = %+v", summary)
		}
	})

	t.Run("problems and pending", func(t *testing.T) {
		text, _ := FormatEditDiagnostics(
			[]Problem{prob("/proj/main.go", 1, 2, 9, "compile", "E2", "boom")},
			[]string{"/proj/pending.go"}, servers, false, reportOpts())
		want := "<diagnostics>\n" +
			"1 new problem after this edit (gopls)\n" +
			"main.go\n" +
			"  error 3:10 boom [compile E2]\n" +
			"diagnostics for pending.go are still being computed and will follow\n" +
			"</diagnostics>"
		if text != want {
			t.Errorf("text =\n%s\nwant\n%s", text, want)
		}
	})

	t.Run("nothing to say", func(t *testing.T) {
		text, summary := FormatEditDiagnostics(nil, nil, servers, false, reportOpts())
		if text != "" || !reflect.DeepEqual(summary, event.LSPDiagnosticsSummary{}) {
			t.Errorf("text = %q, summary = %+v, want empty", text, summary)
		}
	})
}

func TestFormatLateDiagnosticsGolden(t *testing.T) {
	text := FormatLateDiagnostics([]LateChange{
		{Path: "/proj/other/util.go", Cleared: true},
		{Path: "/proj/main.go", New: []Problem{prob("/proj/main.go", 1, 2, 9, "compile", "E2", "boom")}},
	}, reportOpts())
	want := "Language server diagnostics changed for files you edited earlier in this session:\n" +
		"main.go\n" +
		"  error 3:10 boom [compile E2]\n" +
		"other/util.go: no remaining problems"
	if text != want {
		t.Errorf("text =\n%s\nwant\n%s", text, want)
	}

	if got := FormatLateDiagnostics(nil, reportOpts()); got != "" {
		t.Errorf("no changes gave %q, want empty", got)
	}
	// Everything filtered out is also nothing to say.
	if got := FormatLateDiagnostics([]LateChange{{
		Path: "/proj/x.go",
		New:  []Problem{prob("/proj/x.go", 4, 0, 0, "", "", "hint")},
	}}, reportOpts()); got != "" {
		t.Errorf("filtered-out changes gave %q, want empty", got)
	}
}

func TestLayoutPositionsComputedOncePerProblem(t *testing.T) {
	problems := make([]Problem, 0, 16)
	for i := 0; i < 16; i++ {
		problems = append(problems, prob("/proj/main.go", 1, 15-i, i, "", "", fmt.Sprintf("problem %d", i)))
	}
	calls := 0
	opts := reportOpts()
	opts.Position = func(p Problem) (int, int) {
		calls++
		return p.Line + 1, p.Character + 1
	}
	text, _ := FormatEditDiagnostics(problems, nil, []string{"gopls"}, false, opts)
	if text == "" {
		t.Fatal("no report rendered")
	}
	// Position may read the file behind the callback: once per problem to
	// order, once per shown problem to print, once for the summary — never
	// once per sort comparison.
	if calls > 3*len(problems) {
		t.Fatalf("Position called %d times for %d problems; a sort comparison is calling it", calls, len(problems))
	}
}
