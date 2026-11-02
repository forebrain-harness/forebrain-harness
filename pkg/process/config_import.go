// The one-time legacy import that moves a file-era install's runtime
// configuration into the state database (plan 187 §3.3): the YAML's runtime
// sections into fb_config, the login files into the auth.* credential rows,
// and the MCP OAuth overlay tree into fb_mcp_oauth.
package process

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	homepkg "github.com/forebrain-harness/forebrain-harness/pkg/home"
	openaiauth "github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/joho/godotenv"
	"go.yaml.in/yaml/v2"
)

// legacyImportMarkerSection is the fb_config row that records the one-time
// import of a file-era configuration (plan 207 §3.5). It replaces the old
// "the whole table is empty" test, which could not survive a startup that
// writes every stored default: defaults are rows, so "no rows yet" would be
// true only before the very first one.
const legacyImportMarkerSection = "meta.legacy_yaml_imported"

// importLegacyConfigIfNeeded runs the one-time move of a file-era install's
// configuration into the state database. It runs after the database opens and
// after the migration has pruned the rows plans 203–206 retired, and before the
// startup writes the stored defaults — so a file's own values win over a
// default and only the keys the file did not carry are filled.
//
// What says whether the import still owes work is the marker row, not the
// table's emptiness: an install imported by a 187-era binary has rows but no
// marker, so it only takes the marker (its configuration is already in the
// database, and the file has been rewritten down to its startup sections); an
// install the import has already run on takes nothing; a file-era install with
// an empty table is the one case that still imports.
//
// The sections are read by the one whole-file reader the codebase keeps
// (appcfg.LoadLegacyFileSections): the file's runtime sections, with ${ENV}
// references still references, so they reach the resolver as references rather
// than values a load already expanded.
//
// A failure fails the startup: an install whose configuration did not make
// it into the database must not come up silently running on defaults.
func importLegacyConfigIfNeeded(ctx context.Context, sqlDB *sql.DB, home, cfgPath string) error {
	store := &state.ConfigStore{DB: sqlDB}

	sections, err := store.LoadSections(ctx)
	if err != nil {
		return fmt.Errorf("read stored config sections: %w", err)
	}
	if _, imported := sections[legacyImportMarkerSection]; imported {
		return nil
	}

	persisted, err := appcfg.LoadLegacyFileSections(cfgPath)
	if err != nil {
		return fmt.Errorf("read persisted config %s: %w", cfgPath, err)
	}
	// peers is a database-only section (owner 2026-10-30): a copy in the file
	// is never imported and never takes effect, and the file is not touched —
	// the user's own edit is only worth a line in the log.
	if !reflect.ValueOf(persisted.Peers).IsZero() {
		slog.Warn("forebrain.yaml peers section is ignored; peer settings live in the state database")
	}

	// A. A file-era file's runtime sections become fb_config rows, and the
	// file is rewritten down to its supported keys. An install that already
	// holds Root rows was imported before the marker existed: its file is
	// already the rewritten one, so it only takes the marker.
	if !storedRootSectionPresent(sections) {
		if imported, err := importLegacyYAMLSections(ctx, store, home, cfgPath, persisted); err != nil {
			return err
		} else if imported != nil {
			slog.Info("imported runtime configuration into the state database",
				"sections", strings.Join(imported.sections, ","),
				"backup", imported.backup)
		}
	}
	// B. The login files become the auth.* credential rows. The files stay
	// where they are — the runtime no longer reads them, but they are the
	// user's own copy and nothing deletes them.
	//
	// ChatGPT reads the override the file-era configuration names
	// (credentials.chatgpt): the import is the one reader of that section
	// left, because it is what keeps a login living at ~/.codex/auth.json
	// from being lost. Once the import has run, the section is dead.
	if _, ok := sections["auth.chatgpt"]; !ok {
		path := openaiauth.ResolvePath(home, persisted.Credentials.ChatGPT)
		if err := importCredentialFile(ctx, store, "auth.chatgpt", path); err != nil {
			return err
		}
	}
	if _, ok := sections["auth.opencode"]; !ok {
		if err := importCredentialFile(ctx, store, "auth.opencode", opencode.DefaultPath(home)); err != nil {
			return err
		}
	}

	// C. The MCP OAuth overlay tree becomes fb_mcp_oauth rows.
	if err := importLegacyMCPOAuth(ctx, sqlDB, store, home, persisted); err != nil {
		return err
	}

	// The marker lands last: a startup that failed partway left it unwritten,
	// so the next one tries again instead of taking a half-imported install for
	// a finished one. Every part is gated on its own destination anyway.
	if err := store.SaveCredential(ctx, legacyImportMarkerSection, json.RawMessage(`true`)); err != nil {
		return fmt.Errorf("record the legacy import: %w", err)
	}
	return nil
}

type legacyImportResult struct {
	sections []string
	backup   string
}

// storedRootSectionPresent reports whether the database already holds a
// configuration section — the fact that says a file-era install was imported
// before the marker row existed (plan 207 §3.5). Credential rows do not count:
// they land on every install, including one whose sections never made it in.
func storedRootSectionPresent(sections map[string]json.RawMessage) bool {
	for name := range sections {
		if _, ok := rootConfigSectionFields[name]; ok {
			return true
		}
	}
	return false
}

// importLegacyYAMLSections stores the file's non-zero runtime sections and
// rewrites the file down to gateway + credentials. It reports nil when there
// was nothing to import (no runtime section carried a value), in which case
// the file is left untouched.
func importLegacyYAMLSections(ctx context.Context, store *state.ConfigStore, home, cfgPath string, persisted appcfg.Root) (*legacyImportResult, error) {
	envValues, err := godotenv.Read(homepkg.DefaultEnvPath(home))
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read %s: %w", homepkg.DefaultEnvPath(home), err)
		}
		envValues = map[string]string{}
	}

	sections := make(map[string]json.RawMessage)
	names := make([]string, 0, len(rootConfigSections))
	src := reflect.ValueOf(&persisted).Elem()
	for _, name := range rootConfigSections {
		if dbOnlyConfigSections[name] {
			continue
		}
		field := src.FieldByIndex(rootConfigSectionFields[name])
		if field.IsZero() {
			continue
		}
		raw, err := json.Marshal(field.Interface())
		if err != nil {
			return nil, fmt.Errorf("marshal section %s: %w", name, err)
		}
		// The owner's 2026-10-30 ruling: credentials live in the database in
		// plaintext. A ${NAME} reference the home's .env can resolve is
		// replaced by the plaintext value on the way in; one it cannot stays
		// a reference, which compose-time expansion still resolves.
		raw, err = resolveSectionEnvReferences(raw, envValues)
		if err != nil {
			return nil, fmt.Errorf("resolve section %s references: %w", name, err)
		}
		sections[name] = raw
		names = append(names, name)
	}
	if len(sections) == 0 {
		return nil, nil
	}
	if err := store.ReplaceAll(ctx, sections); err != nil {
		return nil, fmt.Errorf("store runtime sections: %w", err)
	}
	backup, err := rewriteMinimalConfigFile(cfgPath, appcfg.FileRoot{
		Gateway:     persisted.Gateway,
		Credentials: persisted.Credentials,
	})
	if err != nil {
		return nil, err
	}
	return &legacyImportResult{sections: names, backup: backup}, nil
}

// resolveSectionEnvReferences walks a section's JSON and replaces every
// single ${NAME} reference inside the credential fields — an api_key, a
// string under an MCP server's headers or oauth — with the value the home's
// .env holds for NAME. Anything else, including a reference .env cannot
// resolve, passes through unchanged.
func resolveSectionEnvReferences(raw json.RawMessage, envValues map[string]string) (json.RawMessage, error) {
	if len(envValues) == 0 {
		return raw, nil
	}
	var doc interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if !resolveEnvReferencesIn(doc, envValues) {
		return raw, nil
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resolveEnvReferencesIn mutates the decoded document in place and reports
// whether anything changed. Secret-bearing places are recognized by key:
// "api_key" (a provider's key) is a string leaf, while "headers" and "oauth"
// (an MCP server's credentials) name whole objects whose every string leaf
// is a credential — an arbitrary header name cannot be matched by key rule.
func resolveEnvReferencesIn(doc interface{}, envValues map[string]string) bool {
	changed := false
	switch v := doc.(type) {
	case map[string]interface{}:
		for key, val := range v {
			switch key {
			case "api_key":
				if s, ok := val.(string); ok {
					if resolved, ok := resolveEnvRef(s, envValues); ok {
						v[key] = resolved
						changed = true
					}
				}
			case "headers", "oauth":
				if resolveAllStringLeaves(val, envValues) {
					changed = true
				}
			default:
				if resolveEnvReferencesIn(val, envValues) {
					changed = true
				}
			}
		}
	case []interface{}:
		for _, item := range v {
			if resolveEnvReferencesIn(item, envValues) {
				changed = true
			}
		}
	}
	return changed
}

// resolveAllStringLeaves replaces every single ${NAME} reference in any
// string under a headers or oauth object — at any depth — with the value the
// home's .env holds for NAME.
func resolveAllStringLeaves(doc interface{}, envValues map[string]string) bool {
	changed := false
	switch v := doc.(type) {
	case string:
		return false
	case map[string]interface{}:
		for key, val := range v {
			if s, ok := val.(string); ok {
				if resolved, ok := resolveEnvRef(s, envValues); ok {
					v[key] = resolved
					changed = true
				}
				continue
			}
			if resolveAllStringLeaves(val, envValues) {
				changed = true
			}
		}
	case []interface{}:
		for _, item := range v {
			if resolveAllStringLeaves(item, envValues) {
				changed = true
			}
		}
	}
	return changed
}

func resolveEnvRef(s string, envValues map[string]string) (string, bool) {
	if !isEnvReference(s) {
		return s, false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(s, "${"), "}")
	if v, ok := envValues[name]; ok && strings.TrimSpace(v) != "" {
		return v, true
	}
	return s, false
}

// rewriteMinimalConfigFile backs the config file up and rewrites it holding
// only the supported keys (plan 207 §3.6): gateway, and credentials for the
// transition period (the read side still points ChatGPT logins at the file).
// The original bytes are copied verbatim; the rewrite is a fresh marshal of
// appcfg.FileRoot — the type that is exactly the file — so nothing else the
// file carried survives into it.
func rewriteMinimalConfigFile(cfgPath string, minimal appcfg.FileRoot) (string, error) {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return "", fmt.Errorf("read %s for backup: %w", cfgPath, err)
	}
	backup := cfgPath + ".pre-db-" + time.Now().UTC().Format("20060102T150405Z")
	if _, err := os.Stat(backup); err == nil {
		// A backup already exists; never overwrite one.
		return backup, nil
	}
	if err := os.WriteFile(backup, raw, 0o600); err != nil {
		return "", fmt.Errorf("back up %s: %w", cfgPath, err)
	}

	var body []byte
	if strings.ToLower(filepath.Ext(cfgPath)) == ".json" {
		body, err = json.MarshalIndent(minimal, "", "  ")
	} else {
		body, err = yaml.Marshal(minimal)
	}
	if err != nil {
		return "", fmt.Errorf("marshal minimal config: %w", err)
	}
	if strings.ToLower(filepath.Ext(cfgPath)) != ".json" {
		body = append([]byte("# runtime configuration lives in the state database (fb_config)\n"), body...)
	}
	if err := writeFileAtomic0600(cfgPath, body); err != nil {
		return "", fmt.Errorf("rewrite %s: %w", cfgPath, err)
	}
	return backup, nil
}

func writeFileAtomic0600(path string, body []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// importCredentialFile copies one login file's bytes into its credential row,
// byte for byte: the row holds the on-disk shape (the Codex auth.json shape
// for ChatGPT, the opencode.json shape for OpenCode) so no login round-trips
// through a different form on its way into the database. A missing file is
// "nothing to import"; a present but unreadable one is a failure.
func importCredentialFile(ctx context.Context, store *state.ConfigStore, section, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	if len(raw) == 0 {
		return nil
	}
	if err := store.SaveCredential(ctx, section, raw); err != nil {
		return fmt.Errorf("store %s: %w", section, err)
	}
	slog.Info("imported login credentials into the state database", "section", section, "path", path)
	return nil
}

// overlayTarget is the (namespace, server) pair an overlay file belongs to.
type overlayTarget struct{ projectKey, serverName string }

// mcpOverlayCandidates builds the names an install can still give an overlay
// file: the home configuration's servers — the shared defaults list is the
// global store, a definition's own list is that agent's — keyed by the slug
// pair the tree's layout produces, and the projects registered in the
// database, keyed by the slug of the key. The import (plan 187) and the final
// sync (plan 188 W12) resolve files through the same candidates, so the two
// agree on what a file's name recovers.
func mcpOverlayCandidates(ctx context.Context, sqlDB *sql.DB, persisted appcfg.Root) (map[string][]overlayTarget, map[string][]string, error) {
	candidates := make(map[string][]overlayTarget)
	addCandidate := func(projectKey, serverName string) {
		serverName = strings.TrimSpace(serverName)
		if serverName == "" {
			return
		}
		pair := slugKey(projectKey, serverName)
		candidates[pair] = append(candidates[pair], overlayTarget{projectKey, serverName})
	}
	for _, srv := range persisted.Agents.Defaults.MCPServers {
		addCandidate("", srv.Name)
	}
	for id, def := range persisted.Agents.Definitions {
		for _, srv := range def.MCPServers {
			addCandidate("agent:"+strings.TrimSpace(id), srv.Name)
		}
	}

	projectKeys := make(map[string][]string)
	prows, err := sqlDB.QueryContext(ctx, `SELECT DISTINCT project_key FROM fb_projects`)
	if err != nil {
		return nil, nil, err
	}
	for prows.Next() {
		var key string
		if err := prows.Scan(&key); err != nil {
			prows.Close()
			return nil, nil, err
		}
		slug := mcp.OverlaySlug(key)
		projectKeys[slug] = append(projectKeys[slug], key)
	}
	if err := prows.Err(); err != nil {
		prows.Close()
		return nil, nil, err
	}
	prows.Close()
	return candidates, projectKeys, nil
}

// importLegacyMCPOAuth walks the state/mcp-oauth overlay tree and stores each
// file as one fb_mcp_oauth row. The tree's directory and file names are slugs
// — every character outside [a-zA-Z0-9_.-] became "_" — so a row's real
// (project_key, server_name) pair is recovered by matching the slug pair
// against what the install can still name: the home configuration's servers
// (agents.defaults — the global store — and each definition's own list, the
// "agent:<id>" store) and the projects registered in the database (whose
// server names live in repository files this import does not read, so the
// file's own slug stands in for the name). Exactly one match imports; zero
// (a server nothing configures anymore) or several (two configurable names
// slug to the same pair) skip with a warning — the file stays on disk, so
// nothing is lost and a later configuration can still adopt it by hand.
func importLegacyMCPOAuth(ctx context.Context, sqlDB *sql.DB, store *state.ConfigStore, home string, persisted appcfg.Root) error {
	var rows int
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_mcp_oauth`).Scan(&rows); err != nil {
		return err
	}
	if rows > 0 {
		return nil
	}
	overlayDir := filepath.Join(strings.TrimSpace(home), "state", "mcp-oauth")
	top, err := os.ReadDir(overlayDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(top) == 0 {
		return nil
	}

	candidates, projectKeys, err := mcpOverlayCandidates(ctx, sqlDB, persisted)
	if err != nil {
		return err
	}

	imported := 0
	importFile := func(projectSlugDir, serverFile string) error {
		raw, err := os.ReadFile(serverFile)
		if err != nil {
			return err
		}
		serverSlug := strings.TrimSuffix(filepath.Base(serverFile), ".json")
		var target *overlayTarget
		matches := candidates[slugKey(projectSlugDir, serverSlug)]
		keyMatches := projectKeys[projectSlugDir]
		switch {
		case len(matches) == 1 && len(keyMatches) == 0:
			target = &matches[0]
		case len(matches) == 0 && len(keyMatches) == 1 && projectSlugDir != "":
			// A project-scoped overlay: the project key recovers from the
			// database, and the file's own slug stands in for the server
			// name, whose real spelling lives in the project's repository
			// files this import does not read. For the names anyone actually
			// configures — github, linear, … — the slug is the name.
			target = &overlayTarget{projectKey: keyMatches[0], serverName: serverSlug}
		default:
			slog.Warn("mcp oauth overlay not imported: its namespace or server name cannot be recovered",
				"path", serverFile, "namespace_matches", len(keyMatches), "server_matches", len(matches))
			return nil
		}
		if err := store.SaveMCPOAuthRow(ctx, target.projectKey, target.serverName, raw); err != nil {
			return fmt.Errorf("store overlay %s: %w", serverFile, err)
		}
		imported++
		return nil
	}
	for _, entry := range top {
		switch {
		case entry.Type().IsDir() && entry.Name() == "projects":
			projects, err := os.ReadDir(filepath.Join(overlayDir, entry.Name()))
			if err != nil {
				return err
			}
			for _, slugDir := range projects {
				if !slugDir.IsDir() {
					continue
				}
				files, err := os.ReadDir(filepath.Join(overlayDir, entry.Name(), slugDir.Name()))
				if err != nil {
					return err
				}
				for _, f := range files {
					if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
						continue
					}
					if err := importFile(slugDir.Name(), filepath.Join(overlayDir, entry.Name(), slugDir.Name(), f.Name())); err != nil {
						return err
					}
				}
			}
		case !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json"):
			if err := importFile("", filepath.Join(overlayDir, entry.Name())); err != nil {
				return err
			}
		}
	}
	if imported > 0 {
		slog.Info("imported mcp oauth overlays into the state database", "count", imported)
	}
	return nil
}

// slugKey renders the pair the overlay tree's layout encodes: the namespace's
// slug — empty for the global store, whose files sit at the top level
// rather than under projects/ — and the server name's slug.
func slugKey(projectKey, serverName string) string {
	pk := ""
	if strings.TrimSpace(projectKey) != "" {
		pk = mcp.OverlaySlug(projectKey)
	}
	return pk + "|" + mcp.OverlaySlug(serverName)
}
