package event

import (
	"fmt"
	"strings"
	"testing"
)

func TestBuildSummarizesDiff(t *testing.T) {
	got, err := Build("demo.txt", []byte("a\nb\n"), []byte("a\nc\n"))
	if err != nil {
		t.Fatalf("build diff: %v", err)
	}
	if got.Path != "demo.txt" {
		t.Fatalf("unexpected path: %q", got.Path)
	}
	if got.Added != 1 || got.Deleted != 1 {
		t.Fatalf("unexpected counts: %+v", got)
	}
	if !strings.Contains(got.UnifiedDiff, "-b") {
		t.Fatalf("expected deleted line in diff: %q", got.UnifiedDiff)
	}
	if !strings.Contains(got.UnifiedDiff, "+c") {
		t.Fatalf("expected added line in diff: %q", got.UnifiedDiff)
	}
}

// A caller that can only show one hunk asks for one hunk: the `@@` header and
// no more body rows than it can draw. Everything else — the second hunk, the
// file headers — stays out of the payload.
func TestFirstHunkKeepsOnlyTheFirstHunk(t *testing.T) {
	unified := "--- demo.txt:before\n" +
		"+++ demo.txt:after\n" +
		"@@ -1,3 +1,3 @@\n" +
		" a\n" +
		"-b\n" +
		"+B\n" +
		" c\n" +
		"@@ -40,3 +40,3 @@\n" +
		" x\n" +
		"-y\n" +
		"+Y\n"
	got := FirstHunk(unified, 40)
	// One hunk header line — the header itself carries two `@@` markers, so the
	// count that matters is of lines, not of markers.
	headers := 0
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "@@") {
			headers++
		}
	}
	if headers != 1 {
		t.Fatalf("want exactly one @@ header line, got %d: %q", headers, got)
	}
	if want := "@@ -1,3 +1,3 @@\n a\n-b\n+B\n c"; got != want {
		t.Fatalf("first hunk = %q, want %q", got, want)
	}
}

func TestFirstHunkBoundsBodyRows(t *testing.T) {
	var b strings.Builder
	b.WriteString("@@ -1,100 +1,100 @@\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "+line-%03d\n", i)
	}
	got := FirstHunk(b.String(), 40)
	rows := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(rows) != 41 {
		t.Fatalf("want the header plus 40 body rows, got %d rows", len(rows))
	}
	if rows[0] != "@@ -1,100 +1,100 @@" {
		t.Fatalf("header = %q", rows[0])
	}
	if rows[40] != "+line-039" {
		t.Fatalf("last body row = %q, want +line-039 (the 40th)", rows[40])
	}
}

func TestFirstHunkWithoutAHunk(t *testing.T) {
	for _, unified := range []string{"", "a\nb", "--- a:before\n+++ a:after\n", "no diff here"} {
		if got := FirstHunk(unified, 40); got != "" {
			t.Fatalf("FirstHunk(%q) = %q, want empty", unified, got)
		}
	}
}

func TestFirstHunkRejectsANonPositiveBound(t *testing.T) {
	unified := "@@ -1 +1 @@\n-a\n+b\n"
	for _, max := range []int{0, -1, -40} {
		if got := FirstHunk(unified, max); got != "" {
			t.Fatalf("FirstHunk(max=%d) = %q, want empty", max, got)
		}
	}
}
