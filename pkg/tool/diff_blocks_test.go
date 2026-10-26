package tool

import (
	"fmt"
	"strings"
	"testing"
)

func diffBlocksRoundTrip(t *testing.T, blocks DiffBlocks) {
	t.Helper()
	body := diffBlocksMarkdown(blocks, 0)
	parsed, ok := ParseDiffBlocks(body)
	if !ok {
		t.Fatalf("ParseDiffBlocks(%q) ok=false", body)
	}
	if len(parsed.Files) != len(blocks.Files) {
		t.Fatalf("round-trip: parsed %d files, want %d (body:\n%s)", len(parsed.Files), len(blocks.Files), body)
	}
	for i, want := range blocks.Files {
		got := parsed.Files[i]
		if got.Verb != want.Verb || got.Path != want.Path || got.Added != want.Added || got.Deleted != want.Deleted {
			t.Fatalf("round-trip file %d = %+v, want %+v", i, got, want)
		}
		if strings.TrimRight(got.UnifiedDiff, "\n") != strings.TrimRight(want.UnifiedDiff, "\n") {
			t.Fatalf("round-trip file %d diff = %q, want %q", i, got.UnifiedDiff, want.UnifiedDiff)
		}
	}
	if len(parsed.Notes) != len(blocks.Notes) {
		t.Fatalf("round-trip: parsed %d notes, want %d", len(parsed.Notes), len(blocks.Notes))
	}
	for i, want := range blocks.Notes {
		if parsed.Notes[i] != want {
			t.Fatalf("round-trip note %d = %q, want %q", i, parsed.Notes[i], want)
		}
	}
}

func TestDiffBlocksRoundTrip(t *testing.T) {
	blocks := DiffBlocks{
		Files: []DiffBlock{
			{
				Verb: "Updated", Path: "pkg/tool/format.go", Added: 3, Deleted: 1,
				UnifiedDiff: "--- a/format.go:before\n+++ b/format.go:after\n@@ -1,3 +1,5 @@\n line1\n-removed\n+added\n+also\n",
			},
			{Verb: "Deleted", Path: "gone/empty.txt", Added: 0, Deleted: 0, UnifiedDiff: ""},
		},
		Notes: []string{diffNoteShared, "… 2 more files changed"},
	}
	body := diffBlocksMarkdown(blocks, 0)
	for _, want := range []string{
		"Updated `pkg/tool/format.go` (+3 -1)",
		"```diff",
		"Deleted `gone/empty.txt` (+0 -0)",
		diffNoteShared,
		"… 2 more files changed",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	// The header-only file must not be followed by a diff fence of its own.
	if strings.Count(body, "```diff") != 1 {
		t.Fatalf("expected exactly one diff fence, body:\n%s", body)
	}
	if !HasDiffBlocks(body) {
		t.Fatal("HasDiffBlocks(body) = false, want true")
	}
	diffBlocksRoundTrip(t, blocks)
}

func TestDiffBlocksFenceGrowsAroundBackticks(t *testing.T) {
	blocks := DiffBlocks{
		Files: []DiffBlock{{
			Verb: "Updated", Path: "doc.md", Added: 1, Deleted: 0,
			UnifiedDiff: "@@ -1 +1,2 @@\n+```go\n+fmt.Println()\n",
		}},
	}
	body := diffBlocksMarkdown(blocks, 0)
	if !strings.Contains(body, "````diff\n") {
		t.Fatalf("expected a 4-backtick fence, body:\n%s", body)
	}
	diffBlocksRoundTrip(t, blocks)
}

func TestDiffBlocksLimitsDropHunksNotHeaders(t *testing.T) {
	longDiff := &strings.Builder{}
	longDiff.WriteString("@@ -1,401 +1,402 @@\n")
	for i := 0; i < 401; i++ {
		longDiff.WriteString(fmt.Sprintf("+line %d\n", i))
	}
	shortDiff := "@@ -1 +1,2 @@\n+kept\n"

	// A 401-hunk-line file keeps only its header.
	huge := DiffBlocks{Files: []DiffBlock{{Verb: "Created", Path: "big.txt", Added: 401, Deleted: 0, UnifiedDiff: longDiff.String()}}}
	body := diffBlocksMarkdown(huge, 0)
	if strings.Contains(body, "```") {
		t.Fatalf("over-long hunk must not be fenced:\n%s", body)
	}
	if !strings.Contains(body, "Created `big.txt` (+401 -0)") {
		t.Fatalf("header missing:\n%s", body)
	}

	// A budget that cannot fit the second file's hunks still writes its header.
	blocks := DiffBlocks{
		Files: []DiffBlock{
			{Verb: "Updated", Path: "first.txt", Added: 1, Deleted: 0, UnifiedDiff: shortDiff},
			{Verb: "Updated", Path: "second.txt", Added: 1, Deleted: 0, UnifiedDiff: shortDiff},
		},
	}
	body = diffBlocksMarkdown(blocks, len("Updated `first.txt` (+1 -0)\n\n```diff\n@@ -1 +1,2 @@\n+kept\n```")+len("Updated `second.txt` (+1 -0)")-1)
	if strings.Count(body, "```diff") != 1 {
		t.Fatalf("expected one fenced diff, body:\n%s", body)
	}
	if !strings.Contains(body, "Updated `second.txt` (+1 -0)") {
		t.Fatalf("second header missing:\n%s", body)
	}
}

func TestDiffBlocksIgnoreFencedPseudoHeaders(t *testing.T) {
	body := "stdout:\n\n```text\nUpdated `x` (+1 -0)\n```\n"
	if _, ok := ParseDiffBlocks(body); ok {
		t.Fatalf("ParseDiffBlocks recognized a fenced pseudo-header: %q", body)
	}
	if HasDiffBlocks(body) {
		t.Fatalf("HasDiffBlocks recognized a fenced pseudo-header: %q", body)
	}
}

func TestDiffBlocksNoteTexts(t *testing.T) {
	if got := diffMoreFilesNote(true, 1, false); got != "… 1 more file changed" {
		t.Fatalf("diffMoreFilesNote(true,1,false) = %q", got)
	}
	if got := diffMoreFilesNote(true, 2, false); got != "… 2 more files changed" {
		t.Fatalf("diffMoreFilesNote(true,2,false) = %q", got)
	}
	if got := diffMoreFilesNote(false, 3, true); got != "3 files changed (binary, mode only or too large to show) (part of the diff is unavailable)" {
		t.Fatalf("diffMoreFilesNote(false,3,true) = %q", got)
	}
	if got := diffUnavailableNote(1); got != "(file diff unavailable for this command; 1 file changed)" {
		t.Fatalf("diffUnavailableNote(1) = %q", got)
	}
	if got := diffUnavailableNote(0); got != "(file diff unavailable for this command)" {
		t.Fatalf("diffUnavailableNote(0) = %q", got)
	}
	// Every note text must survive a round trip.
	blocks := DiffBlocks{
		Files: []DiffBlock{{Verb: "Updated", Path: "a.txt", Added: 1, Deleted: 1, UnifiedDiff: "@@ -1 +1 @@\n-a\n+b\n"}},
		Notes: []string{
			diffMoreFilesNote(true, 1, false),
			diffMoreFilesNote(true, 2, false),
			diffMoreFilesNote(false, 3, true),
			diffUnavailableNote(1),
			diffNoteSkipped,
			diffNoteShared,
			diffNoteTreeMoved,
		},
	}
	diffBlocksRoundTrip(t, blocks)
}
