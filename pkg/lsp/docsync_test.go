package lsp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recvsByMethod returns the params of every recorded client message with
// method, in order.
func recvsByMethod(recs []map[string]any, method string) []map[string]any {
	var out []map[string]any
	for _, rec := range recs {
		recv, ok := rec["recv"].(map[string]any)
		if !ok || recv["method"] != method {
			continue
		}
		params, _ := recv["params"].(map[string]any)
		out = append(out, params)
	}
	return out
}

// textDocOf returns the textDocument object of recorded params.
func textDocOf(params map[string]any) map[string]any {
	td, _ := params["textDocument"].(map[string]any)
	return td
}

// recvURIs lists the document URIs of every recorded message with method.
func recvURIs(recs []map[string]any, method string) []string {
	var uris []string
	for _, params := range recvsByMethod(recs, method) {
		uri, _ := textDocOf(params)["uri"].(string)
		uris = append(uris, uri)
	}
	return uris
}

// waitForRecord waits until the record holds want messages with method.
func waitForRecord(t *testing.T, path string, method string, want int) {
	t.Helper()
	pollRecord(t, path, func(recs []map[string]any) bool {
		return len(recvURIs(recs, method)) >= want
	}, method)
}

func goLanguage(string) string { return "go" }

func TestEnsureSyncedOpensThenChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	inst, record := startFake(t, map[string]any{}, nil)
	docs := NewDocSync(inst, goLanguage, 0)

	version, content, err := docs.EnsureSynced(context.Background(), path)
	if err != nil {
		t.Fatalf("EnsureSynced: %v", err)
	}
	if version != 1 || string(content) != "package main\n" {
		t.Errorf("first sync = %d, %q", version, content)
	}
	recs := pollRecord(t, record, func(recs []map[string]any) bool {
		return findRecv(recs, "textDocument/didOpen") != nil
	}, "didOpen")
	params := findRecv(recs, "textDocument/didOpen")
	td := textDocOf(params)
	if td["uri"] != PathToURI(path) {
		t.Errorf("didOpen uri = %v", td["uri"])
	}
	if v, _ := td["version"].(float64); int(v) != 1 {
		t.Errorf("didOpen version = %v, want 1", td["version"])
	}
	if td["languageId"] != "go" {
		t.Errorf("didOpen languageId = %v, want go", td["languageId"])
	}
	if td["text"] != "package main\n" {
		t.Errorf("didOpen text = %q", td["text"])
	}

	// Unchanged on disk: no new message, the cache answers.
	if docs.Count() != 1 || !docs.IsOpen(path) {
		t.Fatalf("Count=%d IsOpen=%v after open", docs.Count(), docs.IsOpen(path))
	}
	if c, _, ok := docs.Content(path); !ok || string(c) != "package main\n" {
		t.Errorf("Content = %q, %v", c, ok)
	}
	time.Sleep(150 * time.Millisecond)
	if n := len(readRecord(t, record)); n != len(recs) {
		t.Errorf("record grew from %d to %d lines without a disk change", len(recs), n)
	}

	// Disk changes: didChange with the whole new text, version 2.
	if err := os.WriteFile(path, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	version, content, err = docs.EnsureSynced(context.Background(), path)
	if err != nil {
		t.Fatalf("EnsureSynced after change: %v", err)
	}
	if version != 2 || string(content) != "package main\n\nfunc main() {}\n" {
		t.Errorf("second sync = %d, %q", version, content)
	}
	recs = pollRecord(t, record, func(recs []map[string]any) bool {
		return findRecv(recs, "textDocument/didChange") != nil
	}, "didChange")
	td = textDocOf(findRecv(recs, "textDocument/didChange"))
	if v, _ := td["version"].(float64); int(v) != 2 {
		t.Errorf("didChange version = %v, want 2", td["version"])
	}
	changes, _ := findRecv(recs, "textDocument/didChange")["contentChanges"].([]any)
	if len(changes) != 1 {
		t.Fatalf("contentChanges = %v", changes)
	}
	change, _ := changes[0].(map[string]any)
	if change["text"] != "package main\n\nfunc main() {}\n" {
		t.Errorf("didChange text = %q", change["text"])
	}
}

func TestOpenWithBaselineThenChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.go")
	inst, record := startFake(t, map[string]any{}, nil)
	docs := NewDocSync(inst, goLanguage, 0)

	if v, err := docs.OpenWith(context.Background(), path, []byte("old\n")); err != nil || v != 1 {
		t.Fatalf("OpenWith = %d, %v", v, err)
	}
	recs := pollRecord(t, record, func(recs []map[string]any) bool {
		return findRecv(recs, "textDocument/didOpen") != nil
	}, "didOpen")
	if text := textDocOf(findRecv(recs, "textDocument/didOpen"))["text"]; text != "old\n" {
		t.Errorf("didOpen text = %q, want the baseline", text)
	}

	if v, err := docs.Change(context.Background(), path, []byte("new content\n")); err != nil || v != 2 {
		t.Fatalf("Change = %d, %v", v, err)
	}
	recs = pollRecord(t, record, func(recs []map[string]any) bool {
		return findRecv(recs, "textDocument/didChange") != nil
	}, "didChange")
	change, _ := findRecv(recs, "textDocument/didChange")["contentChanges"].([]any)[0].(map[string]any)
	if change["text"] != "new content\n" {
		t.Errorf("didChange text = %q", change["text"])
	}

	// The same content again is a no-op: the server already has it.
	time.Sleep(100 * time.Millisecond)
	before := len(readRecord(t, record))
	if v, err := docs.Change(context.Background(), path, []byte("new content\n")); err != nil || v != 2 {
		t.Fatalf("repeat Change = %d, %v", v, err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(readRecord(t, record)); n != before {
		t.Errorf("identical Change still sent a message (record %d → %d)", before, n)
	}

	// Close sends didClose and forgets; a second Close is a no-op.
	if err := docs.Close(context.Background(), path); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if docs.IsOpen(path) || docs.Count() != 0 {
		t.Errorf("IsOpen=%v Count=%d after Close", docs.IsOpen(path), docs.Count())
	}
	recs = pollRecord(t, record, func(recs []map[string]any) bool {
		return findRecv(recs, "textDocument/didClose") != nil
	}, "didClose")
	if uris := recvURIs(recs, "textDocument/didClose"); len(uris) != 1 || uris[0] != PathToURI(path) {
		t.Errorf("didClose uris = %v", uris)
	}
	if err := docs.Close(context.Background(), path); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestSaveIncludesTextOnlyWhenAsked(t *testing.T) {
	saveCase := func(t *testing.T, capabilities map[string]any, wantText bool) {
		path := filepath.Join(t.TempDir(), "s.go")
		inst, record := startFake(t, map[string]any{"capabilities": capabilities}, nil)
		docs := NewDocSync(inst, goLanguage, 0)
		if _, err := docs.OpenWith(context.Background(), path, []byte("body\n")); err != nil {
			t.Fatalf("OpenWith: %v", err)
		}
		waitForRecord(t, record, "textDocument/didOpen", 1)
		if err := docs.Save(context.Background(), path); err != nil {
			t.Fatalf("Save: %v", err)
		}
		waitForRecord(t, record, "textDocument/didSave", 1)
		params := recvsByMethod(readRecord(t, record), "textDocument/didSave")[0]
		_, hasText := params["text"]
		if wantText && (!hasText || params["text"] != "body\n") {
			t.Errorf("didSave text = %v, want the document text", params["text"])
		}
		if !wantText && hasText {
			t.Errorf("didSave carried text %q although the server did not ask", params["text"])
		}
		if uri, _ := textDocOf(params)["uri"].(string); uri != PathToURI(path) {
			t.Errorf("didSave uri = %v", uri)
		}
	}
	t.Run("includeText", func(t *testing.T) {
		saveCase(t, map[string]any{
			"textDocumentSync": map[string]any{"save": map[string]any{"includeText": true}},
		}, true)
	})
	t.Run("not asked", func(t *testing.T) {
		saveCase(t, map[string]any{}, false)
	})
}

func TestLRUCloseOldest(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("package "+name+"\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		paths = append(paths, p)
	}
	inst, record := startFake(t, map[string]any{}, nil)
	docs := NewDocSync(inst, goLanguage, 2)
	for i, p := range paths {
		if _, _, err := docs.EnsureSynced(context.Background(), p); err != nil {
			t.Fatalf("EnsureSynced %s: %v", p, err)
		}
		waitForRecord(t, record, "textDocument/didOpen", i+1)
		time.Sleep(10 * time.Millisecond) // separate lastUsed stamps
	}
	waitForRecord(t, record, "textDocument/didClose", 1)
	uris := recvURIs(readRecord(t, record), "textDocument/didClose")
	if len(uris) != 1 || uris[0] != PathToURI(paths[0]) {
		t.Fatalf("didClose uris = %v, want only %s", uris, paths[0])
	}
	if docs.Count() != 2 || docs.IsOpen(paths[0]) || !docs.IsOpen(paths[2]) {
		t.Errorf("Count=%d IsOpen(a)=%v IsOpen(c)=%v", docs.Count(), docs.IsOpen(paths[0]), docs.IsOpen(paths[2]))
	}
}

func TestRejectsLargeAndBinary(t *testing.T) {
	dir := t.TempDir()
	large := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(large, bytes.Repeat([]byte("a"), MaxDocumentBytes+1), 0o644); err != nil {
		t.Fatalf("write large: %v", err)
	}
	withNUL := filepath.Join(dir, "nul.txt")
	if err := os.WriteFile(withNUL, []byte("x\x00y\n"), 0o644); err != nil {
		t.Fatalf("write nul: %v", err)
	}
	inst, record := startFake(t, map[string]any{}, nil)
	docs := NewDocSync(inst, goLanguage, 0)

	if _, _, err := docs.EnsureSynced(context.Background(), large); !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("large EnsureSynced err = %v, want ErrFileTooLarge", err)
	}
	if _, _, err := docs.EnsureSynced(context.Background(), withNUL); !errors.Is(err, ErrNotText) {
		t.Errorf("NUL EnsureSynced err = %v, want ErrNotText", err)
	}
	if _, err := docs.OpenWith(context.Background(), filepath.Join(dir, "big.go"), bytes.Repeat([]byte("a"), MaxDocumentBytes+1)); !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("large OpenWith err = %v, want ErrFileTooLarge", err)
	}
	if _, err := docs.Change(context.Background(), filepath.Join(dir, "bin.go"), []byte("a\nb\x00")); !errors.Is(err, ErrNotText) {
		t.Errorf("NUL Change err = %v, want ErrNotText", err)
	}
	if docs.Count() != 0 {
		t.Errorf("Count = %d, want 0", docs.Count())
	}
	if uris := recvURIs(readRecord(t, record), "textDocument/didOpen"); len(uris) != 0 {
		t.Errorf("a rejected document was opened: %v", uris)
	}
}

func TestReopenAfterRestart(t *testing.T) {
	previous := restartBackoff
	restartBackoff = []time.Duration{10 * time.Millisecond}
	t.Cleanup(func() { restartBackoff = previous })

	dir := t.TempDir()
	kept := filepath.Join(dir, "kept.go")
	vanished := filepath.Join(dir, "vanished.go")
	for _, p := range []string{kept, vanished} {
		if err := os.WriteFile(p, []byte("package main\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	var docs *DocSync
	inst, record := startFake(t, map[string]any{
		"crash_after_requests": 1,
		"state_dir":            t.TempDir(),
	}, func(s *InstanceSpec) {
		s.RestartOnCrash = true
		s.MaxRestarts = 1
		s.OnRestart = func(ctx context.Context) { docs.Reopen(ctx) }
	})
	docs = NewDocSync(inst, goLanguage, 0)
	for _, p := range []string{kept, vanished} {
		if _, _, err := docs.EnsureSynced(context.Background(), p); err != nil {
			t.Fatalf("EnsureSynced %s: %v", p, err)
		}
	}
	waitForRecord(t, record, "textDocument/didOpen", 2)

	if err := os.Remove(vanished); err != nil {
		t.Fatalf("remove: %v", err)
	}
	var out map[string]any
	if err := inst.Call(context.Background(), "fake/env", nil, &out); err == nil || !strings.Contains(err.Error(), "restarted") {
		t.Fatalf("call after crash err = %v, want it to mention restarted", err)
	}
	waitForRecord(t, record, "textDocument/didOpen", 3)

	recs := readRecord(t, record)
	var keptOpens, vanishedOpens int
	for _, uri := range recvURIs(recs, "textDocument/didOpen") {
		switch uri {
		case PathToURI(kept):
			keptOpens++
		case PathToURI(vanished):
			vanishedOpens++
		}
	}
	if keptOpens != 2 || vanishedOpens != 1 {
		t.Errorf("didOpen counts: kept=%d (want 2), vanished=%d (want only the initial one)",
			keptOpens, vanishedOpens)
	}
	if docs.Count() != 1 || !docs.IsOpen(kept) || docs.IsOpen(vanished) {
		t.Errorf("Count=%d IsOpen(kept)=%v IsOpen(vanished)=%v", docs.Count(), docs.IsOpen(kept), docs.IsOpen(vanished))
	}
}
