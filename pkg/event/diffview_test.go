package event

import "testing"

// ---- backward-compat tests (must stay green after rewrite) ----

func TestParseCollectsFileStats(t *testing.T) {
	raw := `diff --git a/README.md b/README.md
index a1..b2 100644
--- a/README.md
+++ b/README.md
@@ -1 +1,2 @@
-old
+new
+more
diff --git a/go.mod b/go.mod
index c1..d2 100644
--- a/go.mod
+++ b/go.mod
@@ -1 +1 @@
-go 1.20
+go 1.21`
	doc := Parse(raw)
	if len(doc.Files) != 2 {
		t.Fatalf("files=%d want 2", len(doc.Files))
	}
	if doc.Files[0].Path != "README.md" || doc.Files[0].Added != 2 || doc.Files[0].Deleted != 1 {
		t.Fatalf("unexpected first file stat: %#v", doc.Files[0])
	}
	if doc.Files[1].Path != "go.mod" || doc.Files[1].Added != 1 || doc.Files[1].Deleted != 1 {
		t.Fatalf("unexpected second file stat: %#v", doc.Files[1])
	}
	if doc.Summary != "diff: files=2 +3 -2" {
		t.Fatalf("summary=%q", doc.Summary)
	}
}

func TestBuildSummaryHandlesEmpty(t *testing.T) {
	if got := BuildSummary(DiffDoc{}); got != "diff: no changes" {
		t.Fatalf("summary=%q", got)
	}
}

func TestParseAndSummaryBranches(t *testing.T) {
	if got := Parse(" \n\t "); got.Raw != "" || got.Summary != "" || len(got.Files) != 0 {
		t.Fatalf("empty parse = %#v", got)
	}

	raw := "metadata only\n\n"
	doc := Parse(raw)
	if doc.Raw != "metadata only" {
		t.Fatalf("raw = %q", doc.Raw)
	}
	if doc.Summary != "diff: changes detected" {
		t.Fatalf("summary = %q", doc.Summary)
	}

	raw = `diff --git a

-
diff --git a/a.txt b/a.txt
 context`
	doc = Parse(raw)
	if len(doc.Files) != 2 {
		t.Fatalf("files = %#v", doc.Files)
	}
	if doc.Files[0].Path != "" || doc.Files[0].Added != 0 || doc.Files[0].Deleted != 1 {
		t.Fatalf("first file stat = %#v", doc.Files[0])
	}
	if doc.Summary != "diff: files=2 +0 -1" {
		t.Fatalf("summary = %q", doc.Summary)
	}

	if got := BuildSummary(DiffDoc{Files: []FileStat{{Path: "x"}}}); got != "diff: files=1 +0 -0" {
		t.Fatalf("zero-count summary = %q", got)
	}
	if got := parseDiffPath("diff --git short"); got != "" {
		t.Fatalf("short diff path = %q", got)
	}
}

// ---- hunk / structured-data tests ----

func TestParseHunks(t *testing.T) {
	raw := `diff --git a/foo.go b/foo.go
index abc..def 100644
--- a/foo.go
+++ b/foo.go
@@ -1,4 +1,5 @@
 package main
-// old comment
+// new comment
+// extra

 func main() {}`
	doc := Parse(raw)
	if len(doc.Files) != 1 {
		t.Fatalf("files=%d want 1", len(doc.Files))
	}
	f := doc.Files[0]
	if f.Path != "foo.go" {
		t.Fatalf("path=%q", f.Path)
	}
	if f.Added != 2 || f.Deleted != 1 {
		t.Fatalf("added=%d deleted=%d", f.Added, f.Deleted)
	}
	if len(f.Hunks) != 1 {
		t.Fatalf("hunks=%d want 1", len(f.Hunks))
	}
	h := f.Hunks[0]
	if h.OldStart != 1 || h.NewStart != 1 {
		t.Fatalf("hunk start old=%d new=%d", h.OldStart, h.NewStart)
	}
	if len(h.Lines) != 5 {
		t.Fatalf("hunk lines=%d want 5", len(h.Lines))
	}
	if h.Lines[0].Kind != LineContext || h.Lines[1].Kind != LineDel ||
		h.Lines[2].Kind != LineAdd || h.Lines[3].Kind != LineAdd {
		t.Fatalf("line kinds: %v %v %v %v", h.Lines[0].Kind, h.Lines[1].Kind, h.Lines[2].Kind, h.Lines[3].Kind)
	}
	// del line has OldNo; add line has NewNo
	if h.Lines[1].OldNo != 2 {
		t.Fatalf("del OldNo=%d want 2", h.Lines[1].OldNo)
	}
	if h.Lines[2].NewNo != 2 {
		t.Fatalf("add NewNo=%d want 2", h.Lines[2].NewNo)
	}
}

func TestParseRename(t *testing.T) {
	raw := `diff --git a/old.go b/new.go
similarity index 90%
rename from old.go
rename to new.go
index abc..def 100644
--- a/old.go
+++ b/new.go
@@ -1 +1 @@
-oldpkg
+newpkg`
	doc := Parse(raw)
	if len(doc.Files) != 1 {
		t.Fatalf("files=%d", len(doc.Files))
	}
	f := doc.Files[0]
	if f.Status != "renamed" {
		t.Fatalf("status=%q want renamed", f.Status)
	}
	if f.OldPath != "old.go" {
		t.Fatalf("old_path=%q", f.OldPath)
	}
	if f.Path != "new.go" {
		t.Fatalf("path=%q", f.Path)
	}
}

func TestParseBinary(t *testing.T) {
	raw := `diff --git a/img.png b/img.png
index abc..def 100644
Binary files a/img.png and b/img.png differ`
	doc := Parse(raw)
	if len(doc.Files) != 1 {
		t.Fatalf("files=%d", len(doc.Files))
	}
	f := doc.Files[0]
	if !f.Binary {
		t.Fatalf("binary=false want true")
	}
	if f.Status != "binary" {
		t.Fatalf("status=%q want binary", f.Status)
	}
	if len(f.Hunks) != 0 {
		t.Fatalf("binary file should have no hunks, got %d", len(f.Hunks))
	}
}

func TestParseModeOnly(t *testing.T) {
	raw := `diff --git a/script.sh b/script.sh
old mode 100644
new mode 100755`
	doc := Parse(raw)
	if len(doc.Files) != 1 {
		t.Fatalf("files=%d", len(doc.Files))
	}
	if doc.Files[0].Status != "mode-only" {
		t.Fatalf("status=%q want mode-only", doc.Files[0].Status)
	}
}

func TestParseNewFile(t *testing.T) {
	raw := `diff --git a/new.txt b/new.txt
new file mode 100644
index 0000000..abc1234
--- /dev/null
+++ b/new.txt
@@ -0,0 +1,2 @@
+line one
+line two`
	doc := Parse(raw)
	if len(doc.Files) != 1 {
		t.Fatalf("files=%d", len(doc.Files))
	}
	f := doc.Files[0]
	if f.Status != "added" {
		t.Fatalf("status=%q want added", f.Status)
	}
	if f.Added != 2 || f.Deleted != 0 {
		t.Fatalf("added=%d deleted=%d", f.Added, f.Deleted)
	}
}

func TestParseHunkHeader(t *testing.T) {
	cases := []struct {
		line    string
		wantOld int
		wantNew int
		wantOK  bool
	}{
		{"@@ -1,4 +1,5 @@", 1, 1, true},
		{"@@ -10 +10 @@ func foo()", 10, 10, true},
		{"@@ -0,0 +1,2 @@", 0, 1, true},
		{"not a hunk", 0, 0, false},
		{"@@ garbage @@", 0, 0, false},
	}
	for _, c := range cases {
		old, nw, ok := parseHunkHeader(c.line)
		if ok != c.wantOK || old != c.wantOld || nw != c.wantNew {
			t.Errorf("parseHunkHeader(%q): got (%d,%d,%v) want (%d,%d,%v)",
				c.line, old, nw, ok, c.wantOld, c.wantNew, c.wantOK)
		}
	}
}

func TestParseMultiHunk(t *testing.T) {
	raw := `diff --git a/big.go b/big.go
index abc..def 100644
--- a/big.go
+++ b/big.go
@@ -1,3 +1,3 @@
 ctx1
-del1
+add1
@@ -10,3 +10,3 @@
 ctx2
-del2
+add2`
	doc := Parse(raw)
	if len(doc.Files) != 1 {
		t.Fatalf("files=%d", len(doc.Files))
	}
	f := doc.Files[0]
	if len(f.Hunks) != 2 {
		t.Fatalf("hunks=%d want 2", len(f.Hunks))
	}
	if f.Hunks[1].OldStart != 10 {
		t.Fatalf("second hunk OldStart=%d want 10", f.Hunks[1].OldStart)
	}
}

func TestParseDashDashDashContent(t *testing.T) {
	// Lines starting with "---" (but not "--- ") or "+++" (but not "+++ ")
	// are legitimate content, not diff headers. They must be counted as
	// added/deleted lines.
	raw := `diff --git a/foo.txt b/foo.txt
index abc..def 100644
--- a/foo.txt
+++ b/foo.txt
@@ -1,3 +1,3 @@
 ctx
-----separator
-+old bullet
++++separator
+++new bullet`
	doc := Parse(raw)
	if len(doc.Files) != 1 {
		t.Fatalf("files=%d want 1", len(doc.Files))
	}
	f := doc.Files[0]
	if f.Added != 2 || f.Deleted != 2 {
		t.Fatalf("added=%d deleted=%d want 2 2", f.Added, f.Deleted)
	}
	if len(f.Hunks) != 1 {
		t.Fatalf("hunks=%d want 1", len(f.Hunks))
	}
	h := f.Hunks[0]
	if len(h.Lines) != 5 {
		t.Fatalf("hunk lines=%d want 5 (ctx + 2 del + 2 add)", len(h.Lines))
	}
	// deleted lines: "-----separator" → "-" marker + "----separator" content
	if h.Lines[1].Kind != LineDel || h.Lines[1].Text != "----separator" {
		t.Fatalf("del[0] = %v %q", h.Lines[1].Kind, h.Lines[1].Text)
	}
	// "-+old bullet" → "-" marker + "+old bullet" content
	if h.Lines[2].Kind != LineDel || h.Lines[2].Text != "+old bullet" {
		t.Fatalf("del[1] = %v %q", h.Lines[2].Kind, h.Lines[2].Text)
	}
	// added lines: "++++separator" → "+" marker + "+++separator" content
	if h.Lines[3].Kind != LineAdd || h.Lines[3].Text != "+++separator" {
		t.Fatalf("add[0] = %v %q", h.Lines[3].Kind, h.Lines[3].Text)
	}
	// "+++new bullet" → "+" marker + "++new bullet" content
	if h.Lines[4].Kind != LineAdd || h.Lines[4].Text != "++new bullet" {
		t.Fatalf("add[1] = %v %q", h.Lines[4].Kind, h.Lines[4].Text)
	}
}
