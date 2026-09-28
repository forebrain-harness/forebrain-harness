package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateSourceRoots covers the source-directory contract (§6.2):
// empty resolves to the default, ~ expands, relative paths absolutize, a
// missing directory returns the underlying error verbatim, and a directory
// without the source's markers is refused.
func TestValidateSourceRoots(t *testing.T) {
	dir := t.TempDir()
	// A valid Claude home: projects/ marker.
	claudeHome := filepath.Join(dir, "claude-home")
	if err := os.MkdirAll(filepath.Join(claudeHome, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A valid Codex home: sessions/ marker.
	codexHome := filepath.Join(dir, "codex-home")
	if err := os.MkdirAll(filepath.Join(codexHome, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}

	if root, err := ValidateSourceRoot(KindClaude, claudeHome); err != nil || root != claudeHome {
		t.Fatalf("absolute claude root = %q err = %v", root, err)
	}
	if root, err := ValidateSourceRoot(KindCodex, codexHome); err != nil || root != codexHome {
		t.Fatalf("absolute codex root = %q err = %v", root, err)
	}

	// ~ expansion: proven on a stable path under the real home so the test
	// does not depend on where the OS places temp directories.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		nested := filepath.Join(home, ".forebrain-migrate-test-claude")
		if err := os.MkdirAll(filepath.Join(nested, "projects"), 0o755); err == nil {
			t.Cleanup(func() { _ = os.RemoveAll(nested) })
			if root, err := ValidateSourceRoot(KindClaude, "~/"+strings.TrimPrefix(nested, home+"/")); err != nil || root != nested {
				t.Fatalf("tilde root = %q err = %v", root, err)
			}
		}
	}

	// Relative paths absolutize against the working directory: the testdata
	// fixture path works as a relative marker-bearing directory.
	if root, err := ValidateSourceRoot(KindClaude, claudeHome); err != nil {
		t.Fatalf("relative claude root: %v", err)
	} else if !filepath.IsAbs(root) {
		t.Fatalf("resolved root %q is not absolute", root)
	}

	// A missing directory surfaces the underlying error text unchanged.
	_, missingErr := ValidateSourceRoot(KindClaude, filepath.Join(dir, "no-such-dir"))
	if missingErr == nil || !strings.Contains(missingErr.Error(), "no such file or directory") {
		t.Fatalf("missing dir error = %v, want the raw stat error", missingErr)
	}

	// A directory without markers is refused by name, not by invention.
	empty := t.TempDir()
	_, markerErr := ValidateSourceRoot(KindCodex, empty)
	if markerErr == nil || !strings.Contains(markerErr.Error(), "Codex") || !strings.Contains(markerErr.Error(), "sessions") {
		t.Fatalf("markerless error = %v", markerErr)
	}
}

// TestSourceRootPromptPrefillsDefault checks the picker input's label and
// prefill exist for both sources (§6.2: never an empty input box).
func TestSourceRootPromptPrefillsDefault(t *testing.T) {
	for _, kind := range []Kind{KindClaude, KindCodex} {
		label, def := SourceRootPrompt(kind)
		if strings.TrimSpace(label) == "" || strings.TrimSpace(def) == "" {
			t.Fatalf("%s prompt = %q / %q, both must be non-empty", kind, label, def)
		}
	}
}

// TestDetectSourcesWithOverrides checks an explicit home replaces default
// detection for each source independently.
func TestDetectSourcesWithOverrides(t *testing.T) {
	dir := t.TempDir()
	prevClaude, prevCodex := claudeRootOverride, codexRootOverride
	claudeRootOverride, codexRootOverride = "", ""
	t.Cleanup(func() { claudeRootOverride, codexRootOverride = prevClaude, prevCodex })

	fakeClaude := filepath.Join(dir, "c")
	if err := os.MkdirAll(filepath.Join(fakeClaude, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeCodex := filepath.Join(dir, "x")
	if err := os.MkdirAll(filepath.Join(fakeCodex, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}

	sources := DetectSources(fakeClaude, "")
	// The machine's own ~/.codex may or may not exist; only the Claude row
	// this test pins down is asserted.
	var claudeRow *SourceInfo
	for i := range sources {
		if sources[i].Kind == KindClaude {
			claudeRow = &sources[i]
		}
	}
	if claudeRow == nil || !claudeRow.Available {
		t.Fatalf("claude override = %+v", sources)
	}
	sources = DetectSources("", fakeCodex)
	var codexRow *SourceInfo
	for i := range sources {
		if sources[i].Kind == KindCodex {
			codexRow = &sources[i]
		}
	}
	if codexRow == nil || !codexRow.Available {
		t.Fatalf("codex override = %+v", sources)
	}
	sources = DetectSources(fakeClaude, fakeCodex)
	if len(sources) != 2 || sources[0].Kind != KindClaude || sources[1].Kind != KindCodex {
		t.Fatalf("both overrides = %+v", sources)
	}
}

// Both sources describe the home the picker was given in the same terms:
// sessions, subagent transcripts, memories, input history. The Codex row
// used to carry nothing.
func TestDetectSourcesCountsEveryHomeAlike(t *testing.T) {
	claude := buildFixtureHome(t)
	codex := buildFixtureCodexHome(t)
	var rows []SourceInfo
	for _, row := range DetectSources(claude.root, codex.root) {
		rows = append(rows, row)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	for _, row := range rows {
		if !strings.Contains(row.Description, "session") || !strings.Contains(row.Description, "input-history line") {
			t.Fatalf("%s row = %q", row.Title, row.Description)
		}
	}
	if !strings.Contains(rows[0].Description, "1 subagent transcript") {
		t.Fatalf("claude row = %q", rows[0].Description)
	}

	empty := t.TempDir()
	if err := os.MkdirAll(filepath.Join(empty, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, row := range DetectSources(empty, "") {
		if row.Kind == KindClaude && row.Description != empty+" holds nothing to import" {
			t.Fatalf("empty home row = %q", row.Description)
		}
	}
}
