package event

import (
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
