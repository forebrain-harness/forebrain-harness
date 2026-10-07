package lsp

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"go.yaml.in/yaml/v2"
)

// requiredCatalogIDs is the first-release catalog; task 17 may add entries,
// so tests assert membership, never the total count.
var requiredCatalogIDs = []string{
	"clangd",
	"csharp-ls",
	"gopls",
	"intelephense",
	"jdtls",
	"kotlin-lsp",
	"metals",
	"pyright",
	"rust-analyzer",
	"sourcekit-lsp",
	"typescript-language-server",
}

func TestCatalogLoads(t *testing.T) {
	entries, err := Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(entries) < len(requiredCatalogIDs) {
		t.Fatalf("catalog holds %d entries; the first release needs at least %d", len(entries), len(requiredCatalogIDs))
	}
	ids := make(map[string]bool, len(entries))
	for _, e := range entries {
		ids[e.ID] = true
	}
	for _, id := range requiredCatalogIDs {
		if !ids[id] {
			t.Errorf("catalog is missing %s", id)
		}
	}
	for i := 1; i < len(entries); i++ {
		if entries[i-1].ID >= entries[i].ID {
			t.Fatalf("catalog is not sorted by id: %s then %s", entries[i-1].ID, entries[i].ID)
		}
	}
}

func TestCatalogCoversRequiredLanguages(t *testing.T) {
	entries, err := Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	for _, language := range []string{
		"Java", "Go", "Rust", "C", "C++", "Kotlin", "Swift", "Scala",
		"C#", "TypeScript", "JavaScript", "PHP", "Python",
		// Task 17's extended languages (spec §1.2).
		"Ruby", "Lua", "Dart", "Elixir", "Zig", "Haskell", "OCaml",
		"Bash", "Vue", "Svelte", "Terraform", "Clojure", "Erlang",
		"Nix", "Gleam", "YAML", "Dockerfile",
	} {
		covered := false
		for _, e := range entries {
			if e.Role != appcfg.LSPRolePrimary {
				continue
			}
			for _, l := range e.Languages {
				if l == language {
					covered = true
				}
			}
		}
		if !covered {
			t.Errorf("no primary catalog entry covers %s", language)
		}
	}
}

func TestCatalogEntriesAreComplete(t *testing.T) {
	entries, err := Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	for _, e := range entries {
		if e.Command == "" {
			t.Errorf("%s: command is empty", e.ID)
		}
		if len(e.ExtensionToLanguage) == 0 && len(e.Filenames) == 0 {
			t.Errorf("%s: no extension or filename mapping", e.ID)
		}
		for ext := range e.ExtensionToLanguage {
			if !strings.HasPrefix(ext, ".") || ext != strings.ToLower(ext) {
				t.Errorf("%s: extension key %q must start with a dot and be lowercase", e.ID, ext)
			}
		}
		switch e.Readiness {
		case "progress", "rust-analyzer-status", "jdtls-status", "none":
		default:
			t.Errorf("%s: unknown readiness %q", e.ID, e.Readiness)
		}
		switch e.RootStrategy {
		case "nearest", "cargo-workspace":
		default:
			t.Errorf("%s: unknown root strategy %q", e.ID, e.RootStrategy)
		}
		for i, recipe := range e.Install {
			if len(recipe.Argv) == 0 {
				t.Errorf("%s: install recipe %d has no argv", e.ID, i)
			}
		}
		if e.Notes == "" {
			t.Errorf("%s: notes are empty", e.ID)
		}
		if e.Role != appcfg.LSPRolePrimary && e.Role != appcfg.LSPRoleDiagnostics {
			t.Errorf("%s: unknown role %q", e.ID, e.Role)
		}
	}
}

// TestCatalogNoUnresolvedPrimaryConflicts: two primary servers sharing an
// extension or filename must be separable — different priorities, or a
// conflicts rule covering the pair (spec §6.3).
func TestCatalogNoUnresolvedPrimaryConflicts(t *testing.T) {
	entries, err := Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	byKey := make(map[string][]CatalogEntry)
	for _, e := range entries {
		if e.Role != appcfg.LSPRolePrimary {
			continue
		}
		for ext := range e.ExtensionToLanguage {
			byKey[ext] = append(byKey[ext], e)
		}
		for name := range e.Filenames {
			byKey[name] = append(byKey[name], e)
		}
	}
	for key, list := range byKey {
		for i := 0; i < len(list); i++ {
			for j := i + 1; j < len(list); j++ {
				a, b := list[i], list[j]
				if a.Priority != b.Priority {
					continue
				}
				if catalogConflictCovers(a, b) || catalogConflictCovers(b, a) {
					continue
				}
				t.Errorf("primary servers %s and %s share %q with equal priority and no conflicts rule", a.ID, b.ID, key)
			}
		}
	}
}

func catalogConflictCovers(a, b CatalogEntry) bool {
	for _, c := range a.Conflicts {
		if c.With == b.ID && len(c.WhenRootMarker) > 0 {
			return true
		}
	}
	return false
}

func TestCatalogFilenameMatchesID(t *testing.T) {
	names, err := fs.Glob(catalogFS, "catalog/*.yaml")
	if err != nil {
		t.Fatalf("glob embedded catalog: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("the embedded catalog is empty")
	}
	for _, name := range names {
		data, err := catalogFS.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var e CatalogEntry
		if err := yaml.UnmarshalStrict(data, &e); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if want := e.ID + ".yaml"; filepath.Base(name) != want {
			t.Errorf("%s: id %q does not match the file name", name, e.ID)
		}
	}
}

// TestCatalogDefaultsWin: for extensions the alternates also cover, the
// highest-priority primary is the default server (spec §6.3).
func TestCatalogDefaultsWin(t *testing.T) {
	entries, err := Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	for ext, want := range map[string]string{
		".ts":  "typescript-language-server",
		".py":  "pyright",
		".kt":  "kotlin-lsp",
		".php": "intelephense",
		".c":   "clangd",
	} {
		best, bestPriority := "", -1
		for _, e := range entries {
			if e.Role != appcfg.LSPRolePrimary {
				continue
			}
			if _, ok := e.ExtensionToLanguage[ext]; !ok {
				continue
			}
			if e.Priority > bestPriority || (e.Priority == bestPriority && (best == "" || e.ID < best)) {
				best, bestPriority = e.ID, e.Priority
			}
		}
		if best != want {
			t.Errorf("default primary for %s is %q, want %q", ext, best, want)
		}
	}
}

// TestCatalogDiagnosticsServersRequireMarker: a diagnostics server must not
// wake up in projects without its configuration (task 17).
func TestCatalogDiagnosticsServersRequireMarker(t *testing.T) {
	entries, err := Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	for _, e := range entries {
		if e.Role != appcfg.LSPRoleDiagnostics {
			continue
		}
		if !e.RequireRootMarker {
			t.Errorf("%s: diagnostics servers need require_root_marker", e.ID)
		}
		if len(e.RootMarkers) == 0 {
			t.Errorf("%s: require_root_marker needs root markers to check", e.ID)
		}
	}
}

// TestCatalogInstallArgv: install recipes run without a shell — argv[0] is
// the required package manager (or one of its aliases) and no argument
// carries shell metacharacters (spec §6.1).
func TestCatalogInstallArgv(t *testing.T) {
	entries, err := Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	knownInstallers := map[string]bool{
		"npm": true, "gem": true, "brew": true, "pipx": true, "opam": true,
		"ghcup": true, "nix": true, "go": true, "rustup": true, "dotnet": true, "cs": true,
	}
	for _, e := range entries {
		for i, recipe := range e.Install {
			if len(recipe.Argv) == 0 {
				t.Errorf("%s: install recipe %d has no argv", e.ID, i)
				continue
			}
			if recipe.Argv[0] != recipe.Requires && !knownInstallers[recipe.Argv[0]] {
				t.Errorf("%s: install recipe %d starts with %q, want the required %q or a known installer", e.ID, i, recipe.Argv[0], recipe.Requires)
			}
			for _, arg := range recipe.Argv {
				for _, metachar := range []string{";", "|", "&&", "`", "$("} {
					if strings.Contains(arg, metachar) {
						t.Errorf("%s: install argument %q carries the shell metacharacter %q", e.ID, arg, metachar)
					}
				}
			}
		}
	}
}
