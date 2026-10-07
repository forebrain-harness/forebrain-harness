package lsp

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"go.yaml.in/yaml/v2"
)

//go:embed catalog/*.yaml
var catalogFS embed.FS

// CatalogEntry is one built-in language server as written in catalog/<id>.yaml
// (spec §6.1). Defaults are applied by Catalog, so callers never see an empty
// Role, RootStrategy, or Readiness.
type CatalogEntry struct {
	ID                    string               `yaml:"id"`
	DisplayName           string               `yaml:"display_name"`
	Languages             []string             `yaml:"languages"`
	Role                  string               `yaml:"role"`
	Priority              int                  `yaml:"priority"`
	Command               string               `yaml:"command"`
	Args                  []string             `yaml:"args"`
	CommandDarwin         string               `yaml:"command_darwin"`
	ArgsDarwin            []string             `yaml:"args_darwin"`
	ExtensionToLanguage   map[string]string    `yaml:"extension_to_language"`
	Filenames             map[string]string    `yaml:"filenames"`
	RootMarkers           []string             `yaml:"root_markers"`
	RootStrategy          string               `yaml:"root_strategy"` // "" | "nearest" | "cargo-workspace"
	RequireRootMarker     bool                 `yaml:"require_root_marker"`
	EnvPassthrough        []string             `yaml:"env_passthrough"`
	Detect                DetectSpec           `yaml:"detect"`
	Install               []InstallRecipe      `yaml:"install"`
	StartupTimeout        int                  `yaml:"startup_timeout"` // seconds
	Readiness             string               `yaml:"readiness"`
	AutoAnswers           map[string]string    `yaml:"auto_answers"`
	InitializationOptions appcfg.LSPJSONObject `yaml:"initialization_options"`
	Settings              appcfg.LSPJSONObject `yaml:"settings"`
	Conflicts             []Conflict           `yaml:"conflicts"`
	ProjectWrites         []string             `yaml:"project_writes"`
	Notes                 string               `yaml:"notes"`
}

// DetectSpec says where a server binary hides beyond PATH and how to ask it
// for a version (spec §6.4).
type DetectSpec struct {
	ExtraDirs   []string `yaml:"extra_dirs"`
	VersionArgs []string `yaml:"version_args"` // empty: found on disk counts as installed, no version
	Xcrun       string   `yaml:"xcrun"`        // darwin: tool name for `xcrun --find`
}

// InstallRecipe is one way to install a server (spec §6.5): argv runs without
// a shell once Requires is on the PATH and the platform matches.
type InstallRecipe struct {
	Requires  string   `yaml:"requires"`  // a command that must be on the PATH
	Argv      []string `yaml:"argv"`      // run without a shell
	Platforms []string `yaml:"platforms"` // GOOS values; empty means all
}

// Conflict records that this server should win over With when one of
// WhenRootMarker exists in the workspace (spec §6.3).
type Conflict struct {
	With           string   `yaml:"with"`
	WhenRootMarker []string `yaml:"when_root_marker"`
}

// catalogOverride replaces the embedded catalog in tests; nil in production.
var catalogOverride []CatalogEntry

var (
	catalogOnce  sync.Once
	catalogCache []CatalogEntry
	catalogErr   error
)

// Catalog returns every built-in server, sorted by ID. It parses the embedded
// files once; a parse error is a build defect that the catalog test catches.
func Catalog() ([]CatalogEntry, error) {
	if catalogOverride != nil {
		entries := append([]CatalogEntry(nil), catalogOverride...)
		sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
		return entries, nil
	}
	catalogOnce.Do(loadCatalog)
	return catalogCache, catalogErr
}

// LookupCatalog returns one built-in server.
func LookupCatalog(id string) (CatalogEntry, bool) {
	entries, err := Catalog()
	if err != nil {
		return CatalogEntry{}, false
	}
	for _, e := range entries {
		if e.ID == id {
			return e, true
		}
	}
	return CatalogEntry{}, false
}

// loadCatalog parses every embedded catalog file, rejecting unknown fields
// and files whose name does not match their id, then applies the §6.1
// defaults: Role "primary", RootStrategy "nearest", Readiness "none", and
// lowercased extension keys.
func loadCatalog() {
	names, err := fs.Glob(catalogFS, "catalog/*.yaml")
	if err != nil {
		catalogErr = err
		return
	}
	for _, name := range names {
		data, err := catalogFS.ReadFile(name)
		if err != nil {
			catalogErr = fmt.Errorf("lsp catalog %s: %w", name, err)
			return
		}
		var e CatalogEntry
		if err := yaml.UnmarshalStrict(data, &e); err != nil {
			catalogErr = fmt.Errorf("lsp catalog %s: %w", name, err)
			return
		}
		if filepath.Base(name) != e.ID+".yaml" {
			catalogErr = fmt.Errorf("lsp catalog %s: id must be %q", name, strings.TrimSuffix(filepath.Base(name), ".yaml"))
			return
		}
		applyCatalogDefaults(&e)
		catalogCache = append(catalogCache, e)
	}
	sort.Slice(catalogCache, func(i, j int) bool { return catalogCache[i].ID < catalogCache[j].ID })
}

// applyCatalogDefaults fills the fields the catalog may leave out.
func applyCatalogDefaults(e *CatalogEntry) {
	if e.Role == "" {
		e.Role = appcfg.LSPRolePrimary
	}
	if e.RootStrategy == "" {
		e.RootStrategy = "nearest"
	}
	if e.Readiness == "" {
		e.Readiness = "none"
	}
	if len(e.ExtensionToLanguage) > 0 {
		lowered := make(map[string]string, len(e.ExtensionToLanguage))
		for ext, language := range e.ExtensionToLanguage {
			lowered[strings.ToLower(ext)] = language
		}
		e.ExtensionToLanguage = lowered
	}
}
