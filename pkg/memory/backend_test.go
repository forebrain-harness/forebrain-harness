package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListAndSearchSkipHiddenFilesAndSymlinks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memories")
	outside := filepath.Join(t.TempDir(), "outside")
	for _, dir := range []string{filepath.Join(root, "nested"), outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fixtures := map[string]string{
		filepath.Join(root, "a.md"):           padded("visible needle\n"),
		filepath.Join(root, ".hidden.md"):     padded("hidden needle\n"),
		filepath.Join(root, "nested", "z.md"): padded("nested needle\n"),
		filepath.Join(outside, "secret.md"):   padded("outside needle\n"),
	}
	for path, content := range fixtures {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}

	backend := New(root).WithSearchIndex(testSearchIndexDB(t))
	listed, err := backend.List(ListRequest{MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Entries) != 2 || listed.Entries[0].Path != "a.md" || listed.Entries[1].Path != "nested" {
		t.Fatalf("entries = %#v", listed.Entries)
	}
	searched, err := backend.Search(SearchRequest{
		Queries: []string{"needle"}, TopK: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(searched.Matches) != 2 || searched.Matches[0].Path != "a.md" || searched.Matches[1].Path != "nested/z.md" {
		t.Fatalf("matches = %#v", searched.Matches)
	}
}

func TestReadUsesOneIndexedLines(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "MEMORY.md"), []byte("first\nsecond\nthird\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	one := 1
	response, err := New(root).Read(ReadRequest{Path: "MEMORY.md", LineOffset: 2, MaxLines: &one})
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "second\n" || response.StartLineNumber != 2 || !response.Truncated {
		t.Fatalf("response = %#v", response)
	}
}

func TestReadTruncatesTheMiddleByApproximateTokenBudget(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "MEMORY.md"), []byte("example output"), 0o644); err != nil {
		t.Fatal(err)
	}
	response, err := New(root).Read(ReadRequest{Path: "MEMORY.md", LineOffset: 1, MaxTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "ex…3 tokens truncated…ut" || !response.Truncated {
		t.Fatalf("response = %#v", response)
	}
}

func TestSearchUsesTextLineSemanticsForCRLF(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "MEMORY.md"), []byte("alpha\r\nneedle\r\n"+strings.Repeat(filler, 25)), 0o644); err != nil {
		t.Fatal(err)
	}
	response, err := New(root).WithSearchIndex(testSearchIndexDB(t)).Search(SearchRequest{
		Queries: []string{"needle"}, TopK: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Matches) != 1 || !strings.HasPrefix(response.Matches[0].Content, "alpha\nneedle") ||
		strings.Contains(response.Matches[0].Content, "\r") {
		t.Fatalf("matches = %#v", response.Matches)
	}
}

func TestTextLinesPreservesUnterminatedCarriageReturn(t *testing.T) {
	lines := textLines("first\r\nlast\r")
	if len(lines) != 2 || lines[0] != "first" || lines[1] != "last\r" {
		t.Fatalf("lines = %#v", lines)
	}
}

func TestAddAdHocNoteCreatesOnlyNewValidatedFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memories")
	backend := New(root).WithSearchIndex(testSearchIndexDB(t))
	filename := "2026-05-26T13-42-08-review-style.md"
	relative, err := backend.AddAdHocNote(AdHocNoteScopeProject, filename, "Keep review comments concise.")
	if err != nil {
		t.Fatal(err)
	}
	if relative != AdHocNotesDir+"/"+filename {
		t.Fatalf("relative = %q", relative)
	}
	path := filepath.Join(root, "extensions", "ad_hoc", "notes", filename)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "Keep review comments concise." {
		t.Fatalf("content = %q", content)
	}
	if _, err := backend.AddAdHocNote(AdHocNoteScopeProject, filename, "replacement"); err == nil {
		t.Fatal("duplicate note accepted")
	}
	if _, err := backend.AddAdHocNote(AdHocNoteScopeProject, "../"+filename, "bad"); err == nil {
		t.Fatal("path-like filename accepted")
	}
}

func TestBackendRejectsInvalidSignedRequestValues(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "MEMORY.md"), []byte("alpha\nneedle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := New(root).WithSearchIndex(testSearchIndexDB(t))
	negative := -1
	for name, call := range map[string]func() error{
		"list max results": func() error { _, err := backend.List(ListRequest{MaxResults: -1}); return err },
		"read line offset": func() error { _, err := backend.Read(ReadRequest{Path: "MEMORY.md", LineOffset: -1}); return err },
		"read max lines": func() error {
			_, err := backend.Read(ReadRequest{Path: "MEMORY.md", LineOffset: 1, MaxLines: &negative})
			return err
		},
		"read max tokens": func() error {
			_, err := backend.Read(ReadRequest{Path: "MEMORY.md", LineOffset: 1, MaxTokens: -1})
			return err
		},
		"search top k": func() error {
			_, err := backend.Search(SearchRequest{Queries: []string{"needle"}, TopK: -1})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("invalid signed value accepted")
			}
		})
	}
}

func TestBlankCursorStartsAtTheFirstPage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "MEMORY.md"), []byte(padded("alpha\nneedle\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := New(root).WithSearchIndex(testSearchIndexDB(t))
	for _, cursor := range []string{"", "  "} {
		listed, err := backend.List(ListRequest{Cursor: &cursor, MaxResults: 10})
		if err != nil {
			t.Fatalf("list with cursor %q: %v", cursor, err)
		}
		if len(listed.Entries) != 1 || listed.Entries[0].Path != "MEMORY.md" {
			t.Fatalf("list with cursor %q entries = %#v", cursor, listed.Entries)
		}
	}
	invalid := "not-a-number"
	if _, err := backend.List(ListRequest{Cursor: &invalid, MaxResults: 10}); err == nil {
		t.Fatal("non-numeric cursor accepted")
	}
}

func TestHiddenAndEscapingPathsAreRejected(t *testing.T) {
	backend := New(t.TempDir()).WithSearchIndex(testSearchIndexDB(t))
	for _, path := range []string{"../outside.md", ".git/config", "a/.hidden"} {
		if _, err := backend.Read(ReadRequest{Path: path, LineOffset: 1}); err == nil {
			t.Fatalf("path %q accepted", path)
		}
	}
}

func TestScopedBackendReadsListsAndSearchesTheGlobalMount(t *testing.T) {
	projectRoot := filepath.Join(t.TempDir(), "project")
	globalRoot := filepath.Join(t.TempDir(), "global")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "MEMORY.md"), []byte(padded("project needle\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(globalRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalRoot, "MEMORY.md"), []byte(padded("global needle\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	backend := NewScoped(projectRoot, globalRoot).WithSearchIndex(testSearchIndexDB(t))

	root, err := backend.List(ListRequest{MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Entries) != 2 || root.Entries[0].Path != "MEMORY.md" || root.Entries[1].Path != "global" || root.Entries[1].EntryType != EntryDirectory {
		t.Fatalf("root entries = %#v", root.Entries)
	}

	globalListing, err := backend.List(ListRequest{Path: strPtr("global"), MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(globalListing.Entries) != 1 || globalListing.Entries[0].Path != "global/MEMORY.md" {
		t.Fatalf("global listing = %#v", globalListing.Entries)
	}

	globalRead, err := backend.Read(ReadRequest{Path: "global/MEMORY.md", LineOffset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(globalRead.Content, "global needle\n") || globalRead.Path != "global/MEMORY.md" {
		t.Fatalf("global read = %#v", globalRead)
	}

	// An unscoped search only covers the project root: reaching the global
	// scope always requires the explicit "global" path.
	projectSearch, err := backend.Search(SearchRequest{Queries: []string{"needle"}, TopK: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(projectSearch.Matches) != 1 || projectSearch.Matches[0].Path != "MEMORY.md" {
		t.Fatalf("project search = %#v", projectSearch.Matches)
	}

	globalSearch, err := backend.Search(SearchRequest{
		Queries: []string{"needle"}, Path: strPtr("global"), TopK: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(globalSearch.Matches) != 1 || globalSearch.Matches[0].Path != "global/MEMORY.md" {
		t.Fatalf("global search = %#v", globalSearch.Matches)
	}
}

func TestScopedBackendRoutesAdHocNotesByScope(t *testing.T) {
	projectRoot := filepath.Join(t.TempDir(), "project")
	globalRoot := filepath.Join(t.TempDir(), "global")
	backend := NewScoped(projectRoot, globalRoot).WithSearchIndex(testSearchIndexDB(t))
	filename := "2026-05-26T13-42-08-review-style.md"

	projectPath, err := backend.AddAdHocNote(AdHocNoteScopeProject, filename, "project rule")
	if err != nil {
		t.Fatal(err)
	}
	if projectPath != AdHocNotesDir+"/"+filename {
		t.Fatalf("project note path = %q", projectPath)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, AdHocNotesDir, filename)); err != nil {
		t.Fatalf("project note not on the project root: %v", err)
	}

	globalPath, err := backend.AddAdHocNote(AdHocNoteScopeGlobal, filename, "global rule")
	if err != nil {
		t.Fatal(err)
	}
	if globalPath != "global/"+AdHocNotesDir+"/"+filename {
		t.Fatalf("global note path = %q", globalPath)
	}
	if _, err := os.Stat(filepath.Join(globalRoot, AdHocNotesDir, filename)); err != nil {
		t.Fatalf("global note not on the global root: %v", err)
	}

	read, err := backend.Read(ReadRequest{Path: globalPath, LineOffset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if read.Content != "global rule" {
		t.Fatalf("read back global note = %#v", read)
	}
}

func TestScopelessProjectRootRejectsProjectScopedNotes(t *testing.T) {
	globalRoot := t.TempDir()
	backend := NewScoped("", globalRoot).WithSearchIndex(testSearchIndexDB(t))
	filename := "2026-05-26T13-42-08-review-style.md"
	if _, err := backend.AddAdHocNote(AdHocNoteScopeProject, filename, "note"); err == nil {
		t.Fatal("project-scope note accepted for a session with no project identity")
	}
	if _, err := backend.AddAdHocNote(AdHocNoteScopeGlobal, filename, "note"); err != nil {
		t.Fatalf("global note should still succeed: %v", err)
	}
}

func strPtr(s string) *string { return &s }

// MatchRanges is what a surface uses to mark the text that recalled a memory,
// so it has to find a term wherever the line spells it. Index terms come back
// folded and the line may not be, which is why case is folded here too.
//
// What it must never mark is a word the line does not contain: the search
// compares whole terms, and a mark on the "read" inside "read_file" tells the
// reader a line answered a word that is not written anywhere in it.
func TestMatchRangesLocateTheTermsThatRecalledAMemory(t *testing.T) {
	for _, test := range []struct {
		name  string
		text  string
		terms []string
		want  []string
	}{
		{name: "case folded", text: "Deploy and deploy", terms: []string{"deploy"}, want: []string{"Deploy", "deploy"}},
		{name: "term folded", text: "Deploy once", terms: []string{"DEPLOY"}, want: []string{"Deploy"}},
		{name: "overlapping terms merge", text: "走审批准入流程", terms: []string{"审批", "批准"}, want: []string{"审批准"}},
		{name: "wide runes", text: "计划审批走 exit_plan_mode 流程", terms: []string{"审批"}, want: []string{"审批"}},
		{name: "no hit", text: "nothing here", terms: []string{"deploy"}},
		{name: "not part of an identifier", text: "不要按假想文件名 read_file。", terms: []string{"read"}},
		{name: "not part of a hyphenated word", text: "mounted read-only here", terms: []string{"read"}},
		{name: "compound marked whole", text: "mounted read-only here", terms: []string{"read-only"}, want: []string{"read-only"}},
		{name: "identifier marked whole", text: "call read_file next", terms: []string{"read_file"}, want: []string{"read_file"}},
		{name: "a later occurrence still stands alone", text: "read_file, then read", terms: []string{"read"}, want: []string{"read"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ranges := MatchRanges(test.text, test.terms)
			got := make([]string, 0, len(ranges))
			for _, span := range ranges {
				got = append(got, test.text[span[0]:span[1]])
			}
			if len(got) != len(test.want) {
				t.Fatalf("ranges=%v want %v", got, test.want)
			}
			for index, want := range test.want {
				if got[index] != want {
					t.Fatalf("range %d = %q want %q", index, got[index], want)
				}
			}
		})
	}
}

// A match always comes back with the file around it, and never with a page of
// it.
//
// The window is the search's own, so there is no argument that could bring back
// a bare line — a line out of a memory file is usually the label of the record
// and not the record — or bring back the document.
func TestSearchAlwaysReturnsAWindowAroundTheMatch(t *testing.T) {
	b := indexedBackend(t, map[string]string{
		"notes.md": padded("one\ntwo\nthree\nneedle\nfive\nsix\nseven\n"),
	})
	response, err := b.Search(SearchRequest{Queries: []string{"needle"}, TopK: MaxSearchResults})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(response.Matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(response.Matches))
	}
	match := response.Matches[0]
	if match.ContentStartLineNumber != 1 {
		t.Fatalf("content starts at line %d, want 1", match.ContentStartLineNumber)
	}
	if want := "one\ntwo\nthree\nneedle\nfive\nsix\nseven"; match.Content != want {
		t.Fatalf("content = %q, want %q", match.Content, want)
	}
}

// The window stops at the sides of the match, so a hit deep in a long file
// comes back with its surroundings rather than with the rest of the memory.
func TestSearchWindowStopsAtTheSidesOfTheMatch(t *testing.T) {
	b := indexedBackend(t, map[string]string{
		"notes.md": padded(strings.Repeat("filler line\n", 30) + "needle\n" + strings.Repeat("tail line\n", 30)),
	})
	response, err := b.Search(SearchRequest{Queries: []string{"needle"}, TopK: MaxSearchResults})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(response.Matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(response.Matches))
	}
	match := response.Matches[0]
	if want := 2*searchContextLines + 1; strings.Count(match.Content, "\n")+1 != want {
		t.Fatalf("content = %q, want the %d lines of a ±%d window", match.Content, want, searchContextLines)
	}
	if want := match.MatchLineNumber - searchContextLines; match.ContentStartLineNumber != want {
		t.Fatalf("content starts at line %d, want %d", match.ContentStartLineNumber, want)
	}
}
