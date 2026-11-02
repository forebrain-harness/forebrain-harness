// The runtime configuration's composed source: the YAML file's startup
// sections plus the state database's runtime sections (plan 187), and the
// per-turn sync that adopts a changed configuration at the turn boundary.
package process

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	homepkg "github.com/forebrain-harness/forebrain-harness/pkg/home"
	openaiauth "github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"go.yaml.in/yaml/v2"
)

// rootConfigSections lists the Root sections fb_config persists, by the name a
// reader sees in forebrain.yaml. gateway and credentials are deliberately
// absent: they are the file's startup sections — the gateway binds before a
// database may exist, and the credentials pointer is a transition-period
// file concern (plan 187 §1.1). A new Root section must be added here (or
// deliberately kept out and documented) the moment it is added to Root.
var rootConfigSections = []string{
	"approval_preset",
	"sandbox_workspace_write",
	"compact",
	"cron",
	"agents",
	"hooks",
	"memories",
	"batch",
	"features",
	"lsp",
	"peers",
}

// dbOnlyConfigSections are the Root sections forebrain.yaml never sets: they
// live in the state database only (owner 2026-10-30), so the one-time legacy
// import skips them — a file-era install cannot have carried one — and the
// composition's file zeroing already keeps any stray file copy dead.
var dbOnlyConfigSections = map[string]bool{
	"peers": true,
}

// rootConfigSectionFields resolves each section name to its Root field, by
// yaml tag, once. A name with no matching field panics at init: the section
// table and Root cannot drift apart silently (plan 187 §8).
var rootConfigSectionFields = func() map[string][]int {
	t := reflect.TypeOf(appcfg.Root{})
	out := make(map[string][]int, len(rootConfigSections))
	for _, name := range rootConfigSections {
		for i := 0; i < t.NumField(); i++ {
			if strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0] == name {
				out[name] = []int{i}
				break
			}
		}
	}
	if len(out) != len(rootConfigSections) {
		panic("rootConfigSections is out of sync with config.Root")
	}
	return out
}()

// configStore returns the fb_config accessor over this environment's state
// database. The store itself is stateless; a nil database is the caller's
// error to report.
func (env *Environment) configStore() (*state.ConfigStore, error) {
	if env == nil || env.SQL == nil {
		return nil, fmt.Errorf("no state database")
	}
	return &state.ConfigStore{DB: env.SQL}, nil
}

// composeConfig reads the runtime's effective configuration source (plan 187
// §1.2): the YAML file contributes its startup sections — gateway and the
// transition-period credentials pointer — and fb_config contributes every
// runtime section it holds. A section the database does not hold reads as its
// zero value: after the legacy import rewrote the file, the file's runtime
// sections are dead and the database is the only source. The composed root
// then runs through the same tail Load runs — .env merge, ${ENV} expansion,
// validation — so a composed configuration and a file-loaded one behave
// identically.
func composeConfig(ctx context.Context, env *Environment) (appcfg.Root, error) {
	if env == nil || strings.TrimSpace(env.ConfigPath) == "" {
		return appcfg.Root{}, fmt.Errorf("compose config: no config path")
	}
	if env.SQL == nil {
		return appcfg.Root{}, fmt.Errorf("compose config: no state database")
	}
	return composeConfigFrom(ctx, env.ConfigPath, env.SQL)
}

func composeConfigFrom(ctx context.Context, cfgPath string, db *sql.DB) (appcfg.Root, error) {
	out, err := composeSectionsFrom(ctx, cfgPath, db, nil)
	if err != nil {
		return appcfg.Root{}, err
	}
	if err := appcfg.FinishLoad(&out); err != nil {
		return appcfg.Root{}, fmt.Errorf("compose config: %w", err)
	}
	return out, nil
}

// composeSectionsFrom assembles the persisted form of the composed source:
// the YAML file's startup sections plus the database's stored sections, with
// override substituting (or, for a nil value, removing) sections for this
// one composition. No .env merge, no expansion, no validation run — this is
// the layer a read-modify-write reads and the candidate validator composes,
// the role LoadPersisted played for the file (plan 188 §1).
func composeSectionsFrom(ctx context.Context, cfgPath string, db *sql.DB, override map[string]json.RawMessage) (appcfg.Root, error) {
	store := &state.ConfigStore{DB: db}
	yamlRoot, err := appcfg.LoadPersisted(cfgPath)
	if err != nil {
		return appcfg.Root{}, fmt.Errorf("compose config: load %s: %w", cfgPath, err)
	}
	out := yamlRoot
	v := reflect.ValueOf(&out).Elem()
	for _, idx := range rootConfigSectionFields {
		f := v.FieldByIndex(idx)
		f.Set(reflect.Zero(f.Type()))
	}
	sections, err := store.LoadSections(ctx)
	if err != nil {
		return appcfg.Root{}, fmt.Errorf("compose config: read sections: %w", err)
	}
	for name, raw := range override {
		if raw == nil {
			delete(sections, name)
			continue
		}
		sections[name] = raw
	}
	for name, raw := range sections {
		idx, ok := rootConfigSectionFields[name]
		if !ok || len(raw) == 0 {
			// Credential sections (auth.*) are rows in the same table but not
			// Root fields; they are read through the credential stores.
			continue
		}
		if err := json.Unmarshal(raw, v.FieldByIndex(idx).Addr().Interface()); err != nil {
			return appcfg.Root{}, fmt.Errorf("compose config: section %s: %w", name, err)
		}
	}
	return out, nil
}

// validateRootSection is the write-side gate the state store runs on every
// section update (plan 188 §1): the candidate — or its removal — is composed
// with the rest of the install's configuration and the result runs the same
// tail Load runs. Only value-domain checks apply: the plaintext-secret scans
// are file-side guardrails, and credentials live in the database in
// plaintext (owner ruling 2026-10-30, plan 187 §1.3). Sections that are not
// Root fields (the auth.* credential rows) never compose and are exempt.
func validateRootSection(ctx context.Context, cfgPath string, db *sql.DB, section string, candidate json.RawMessage) error {
	if _, ok := rootConfigSectionFields[section]; !ok {
		return nil
	}
	composed, err := composeSectionsFrom(ctx, cfgPath, db, map[string]json.RawMessage{section: candidate})
	if err != nil {
		return err
	}
	return appcfg.FinishLoad(&composed)
}

// InstallConfigSectionValidator installs the store's write-side gate for
// this process: every UpdateSection — from any surface, including packages
// that build their store straight from a database handle — runs it. Open
// calls it with the environment's config path and database; a test harness
// that never opens one calls it directly with the same pair.
func InstallConfigSectionValidator(cfgPath string, db *sql.DB) {
	state.SetSectionValidator(func(ctx context.Context, section string, candidate json.RawMessage) error {
		return validateRootSection(ctx, cfgPath, db, section, candidate)
	})
}

// ensureStartupConfigFile creates forebrain.yaml when a home has none yet
// (plan 188 W9): the setup flow writes its configuration to the state
// database, but the file must exist — the gateway binds before a database
// may exist, and its startup sections live there. A file that exists is
// never touched.
func ensureStartupConfigFile(cfgPath string) error {
	if _, err := os.Stat(cfgPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	// appcfg.FileRoot is exactly the file (plan 207 §3.6): marshaling anything
	// else — a Root, even a zero one — would drag a stored section along.
	var body []byte
	var err error
	if strings.ToLower(filepath.Ext(cfgPath)) == ".json" {
		body, err = json.MarshalIndent(appcfg.FileRoot{}, "", "  ")
	} else {
		body, err = yaml.Marshal(appcfg.FileRoot{})
	}
	if err != nil {
		return err
	}
	if strings.ToLower(filepath.Ext(cfgPath)) != ".json" {
		body = append([]byte("# runtime configuration lives in the state database (fb_config)\n"), body...)
	}
	dir := filepath.Dir(cfgPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(cfgPath, body, 0o600)
}

// RootConfigSections names the Root sections fb_config persists, by the name
// a reader sees in forebrain.yaml (a copy; callers cannot mutate the table).
// gateway and credentials are deliberately absent — they are the file's
// startup sections (plan 187 §1.1).
func RootConfigSections() []string {
	out := make([]string, len(rootConfigSections))
	copy(out, rootConfigSections)
	return out
}

// MarshalRootSection renders one section of root as the bytes its fb_config
// row holds: the Root field's own JSON, or nil when the field is zero — a
// zero section stores no row. An unknown name is the caller's error.
func MarshalRootSection(root appcfg.Root, section string) (json.RawMessage, error) {
	idx, ok := rootConfigSectionFields[section]
	if !ok {
		return nil, fmt.Errorf("unknown config section %q", section)
	}
	field := reflect.ValueOf(&root).Elem().FieldByIndex(idx)
	if field.IsZero() {
		return nil, nil
	}
	out, err := json.Marshal(field.Interface())
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ConfigStore returns the fb_config accessor over this environment's state
// database, with the write-side validation Open installed. A nil database is
// the caller's error to report.
func (env *Environment) ConfigStore() (*state.ConfigStore, error) {
	return env.configStore()
}

// ComposedPersistedConfig reads the persisted form of the runtime
// configuration — the YAML's startup sections plus the stored sections, as
// LoadPersisted composed them: no .env merge, no ${ENV} expansion, no
// validation. It is the read-modify-write base for a surface changing one
// section, the role file-era readers gave LoadPersisted.
func (env *Environment) ComposedPersistedConfig(ctx context.Context) (appcfg.Root, error) {
	if env == nil || strings.TrimSpace(env.ConfigPath) == "" {
		return appcfg.Root{}, fmt.Errorf("no config path is bound")
	}
	if env.SQL == nil {
		return appcfg.Root{}, fmt.Errorf("no state database")
	}
	return composeSectionsFrom(ctx, env.ConfigPath, env.SQL, nil)
}

// WriteRootSections writes several sections of root in one transaction (plan
// 191 §3.1): a settings group's items can live in more than one Root section —
// the approval group spans approval_preset, sandbox_workspace_write and batch —
// and such a save lands whole or not at all. The caller reads the configuration
// first (ComposedPersistedConfig), changes the sections it means to change, and
// hands the result here; each named section is rendered and stored inside the
// store's single transaction, where the validator still judges it.
func (env *Environment) WriteRootSections(ctx context.Context, sections []string, root appcfg.Root) error {
	if env == nil || env.SQL == nil {
		return fmt.Errorf("no state database")
	}
	if len(sections) == 0 {
		return fmt.Errorf("config section name required")
	}
	next := make(map[string]json.RawMessage, len(sections))
	for _, name := range sections {
		raw, err := MarshalRootSection(root, name)
		if err != nil {
			return err
		}
		next[name] = raw
	}
	store, err := env.configStore()
	if err != nil {
		return err
	}
	return store.UpdateSections(ctx, sections, func(map[string]json.RawMessage) (map[string]json.RawMessage, error) {
		return next, nil
	})
}

// UpdateRootSection runs one read-modify-write of one Root section in the
// state database (plan 188 W2): fn receives a root holding exactly the named
// section — its current stored value, the zero value when nothing is stored
// — mutates it in place, and that section is what lands: inside the store's
// BEGIN IMMEDIATE transaction, so concurrent section writes serialize the
// way the file mutex used to make them, and validated there — a candidate
// that would not compose into a loadable configuration rolls back as
// *state.InvalidSectionError. A surface whose decision needs other sections
// reads them first through ComposedPersistedConfig; changes to any other
// section fn makes are dropped.
func (env *Environment) UpdateRootSection(ctx context.Context, section string, fn func(cfg *appcfg.Root) error) error {
	if env == nil || env.SQL == nil {
		return fmt.Errorf("no state database")
	}
	if fn == nil {
		return fmt.Errorf("config section update needs a function")
	}
	if _, ok := rootConfigSectionFields[section]; !ok {
		return fmt.Errorf("unknown config section %q", section)
	}
	store, err := env.configStore()
	if err != nil {
		return err
	}
	return store.UpdateSection(ctx, section, func(cur json.RawMessage) (json.RawMessage, error) {
		cfg := appcfg.Root{}
		field := reflect.ValueOf(&cfg).Elem().FieldByIndex(rootConfigSectionFields[section])
		if len(cur) > 0 {
			// The row holds the section's own JSON — the value its Root field
			// marshals to — so it unmarshals into that field, not into a Root.
			if err := json.Unmarshal(cur, field.Addr().Interface()); err != nil {
				return nil, fmt.Errorf("read section %s: %w", section, err)
			}
		}
		if err := fn(&cfg); err != nil {
			return nil, err
		}
		// Only the named section's field lands, whatever else fn touched: a
		// row holds exactly one section, and a write that reaches a second
		// one is a bug this drop makes visible instead of silent corruption.
		field = reflect.ValueOf(&cfg).Elem().FieldByIndex(rootConfigSectionFields[section])
		if field.IsZero() {
			// A zero section reads the same absent as stored empty; dropping
			// the row keeps the table to what the install means.
			return nil, nil
		}
		out, err := json.Marshal(field.Interface())
		if err != nil {
			return nil, fmt.Errorf("marshal section %s: %w", section, err)
		}
		return out, nil
	})
}

// StartupConfig reads the configuration a process starts from before its
// environment exists: the startup gates (first-setup, startup-config), the
// pre-session readers, and anything else that must judge the configuration
// before Open runs. The state database is the source once it holds sections;
// until then the file is, and it is read whole — a file-era home the legacy
// import has not reached yet still starts on what its file says (plan 207
// §3.5), which is also what the import will move into the database.
//
// A home with no configuration at all reads as an empty root: a caller that
// must tell that apart from a configured one asks NeedsFirstSetup.
func StartupConfig(ctx context.Context, home string) (appcfg.Root, error) {
	root, fromFile, err := readStartupConfig(ctx, home)
	if err != nil || !fromFile {
		return root, err
	}
	// The file-era read takes the same tail Load gave it: the home's .env
	// merge, ${ENV} expansion and validation, so a copied configuration whose
	// secret is missing reads as missing rather than as a reference that looks
	// set.
	if err := appcfg.FinishLoad(&root); err != nil {
		return appcfg.Root{}, err
	}
	return root, nil
}

// PersistedStartupConfig is StartupConfig without Load's tail: ${ENV}
// references stay references. It is what a surface that writes the
// configuration back reads — /connect must not bake one machine's expanded
// secrets into the file it saves.
func PersistedStartupConfig(ctx context.Context, home string) (appcfg.Root, error) {
	root, _, err := readStartupConfig(ctx, home)
	return root, err
}

// readStartupConfig is the one decision both readers share: the composed
// configuration once the state database holds sections, the file's own
// sections until then (the fromFile flag). The file is read whole for that
// case, the way the legacy import reads it.
func readStartupConfig(ctx context.Context, home string) (appcfg.Root, bool, error) {
	if composed, ok, err := ComposedStartupConfig(ctx, home); err != nil {
		return appcfg.Root{}, false, err
	} else if ok {
		return composed, false, nil
	}
	// The same loose path resolution the startup resolve uses: a home with no
	// config file at all has no configuration either way.
	cfgPath, err := homepkg.ResolveConfigPath(home)
	if err != nil {
		cfgPath = homepkg.ConfigPath(home)
	}
	root, err := appcfg.LoadLegacyFileSections(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return appcfg.Root{}, true, nil
		}
		return appcfg.Root{}, true, err
	}
	return root, true, nil
}

// ComposedStartupConfig reads the composed configuration for a process that
// has not opened its environment yet — the startup gates (first-setup,
// startup-config) and anything else that must judge the configuration before
// Open runs. It reports false in the transition cases where the file is still
// the answer: no database yet, one this binary cannot read read-only (an
// older binary has not migrated it), or one that holds no sections (the
// legacy import has not run). A database that holds sections but cannot be
// composed is an error — fail-closed, the same verdict Open gives later.
//
// The database opens read-only: this path runs in commands that must not
// migrate anything, and a database needing migration is exactly the case
// where the file-era configuration is the right answer.
func ComposedStartupConfig(ctx context.Context, home string) (appcfg.Root, bool, error) {
	// The same loose path resolution the startup resolve uses: a home with
	// no config file at all has nothing to compose either way.
	cfgPath, err := homepkg.ResolveConfigPath(home)
	if err != nil {
		cfgPath = homepkg.ConfigPath(home)
	}
	db, err := state.Open(ctx, state.StateDBPath(home), &state.OpenOptions{ReadOnly: true})
	if err != nil {
		return appcfg.Root{}, false, nil
	}
	defer db.Close()
	store := &state.ConfigStore{DB: db}
	sections, err := store.LoadSections(ctx)
	if err != nil {
		return appcfg.Root{}, false, fmt.Errorf("read stored config sections: %w", err)
	}
	if len(sections) == 0 {
		return appcfg.Root{}, false, nil
	}
	root, err := composeConfigFrom(ctx, cfgPath, db)
	if err != nil {
		return appcfg.Root{}, false, err
	}
	return root, true, nil
}

// configSnapshotFingerprint renders a composed root to its canonical JSON —
// Go's encoder sorts map keys and emits struct fields in declaration order,
// so the same configuration always hashes the same — and digests it. Source
// files are runtime-only (json:"-") and stay out.
func configSnapshotFingerprint(root appcfg.Root) ([]byte, error) {
	b, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	return sum[:], nil
}

// SyncConfigFromDB re-reads the composed configuration and adopts it when it
// changed. Submit calls it at the turn boundary, before any queueing or
// execution, so a running turn never sees its configuration move and a write
// one surface made becomes effective on the next turn of every surface
// (plan 187 §3.2). The steady state is one sqlite read, one file read, and a
// hash comparison; the full re-application runs only when the hash moved.
//
// A read or validation failure fails the turn (fail-closed): the write side
// validated before storing, so a read that errors means the store itself is
// broken, not that the configuration is undesirable.
//
// Callers must not hold a lock the OnConfigReload callback acquires: this
// method invokes that callback while holding reloadMu, the order
// applyComposedConfigLocked always runs under.
func (env *Environment) SyncConfigFromDB(ctx context.Context) error {
	if env == nil {
		return nil
	}
	if !env.runtimeConfigFromDB || env.SQL == nil || strings.TrimSpace(env.ConfigPath) == "" {
		// An environment that does not compose — the OpenOptions.Config test
		// bypass, or a hand-built one with no database — has nothing to sync;
		// the production surfaces always have all three.
		return nil
	}
	composed, err := composeConfig(ctx, env)
	if err != nil {
		return fmt.Errorf("sync config: %w", err)
	}
	next, err := configSnapshotFingerprint(composed)
	if err != nil {
		return fmt.Errorf("sync config: %w", err)
	}
	env.reloadMu.Lock()
	defer env.reloadMu.Unlock()
	if bytes.Equal(env.configFingerprint, next) {
		return nil
	}
	if err := env.applyComposedConfigLocked(composed); err != nil {
		return fmt.Errorf("sync config: %w", err)
	}
	env.configFingerprint = next
	return nil
}

// dbCredentialStore adapts fb_config's credential rows to the storage ports
// pkg/llm/openai and pkg/llm/opencode declare. Both logins live in the table
// under their section name, in the on-disk shape of the file each came from;
// a nil byte value means "no login stored", which Load returns for an absent
// row and Save writes by removing it.
type dbCredentialStore struct {
	store   *state.ConfigStore
	section string
}

func (s dbCredentialStore) LoadCredentials(ctx context.Context) (json.RawMessage, error) {
	return s.store.LoadCredential(ctx, s.section)
}

func (s dbCredentialStore) SaveCredentials(ctx context.Context, raw json.RawMessage) error {
	return s.store.SaveCredential(ctx, s.section, raw)
}

// dbMCPOAuthStore adapts the fb_mcp_oauth table to pkg/mcp's overlay port:
// one row per (namespace, server), the file's JSON as the row's bytes.
type dbMCPOAuthStore struct {
	store *state.ConfigStore
}

func (s dbMCPOAuthStore) LoadOAuth(ctx context.Context, projectKey, serverName string) (json.RawMessage, error) {
	return s.store.LoadMCPOAuthRow(ctx, projectKey, serverName)
}

func (s dbMCPOAuthStore) SaveOAuth(ctx context.Context, projectKey, serverName string, raw json.RawMessage) error {
	return s.store.SaveMCPOAuthRow(ctx, projectKey, serverName, raw)
}

// installCredentialStores installs the database-backed implementations
// behind the credential storage ports openai, opencode, and mcp declare,
// after the state database has opened and the legacy import filled the rows
// (plan 187 §1.4). The packages' own Load and Save keep reading and writing
// their files until plan 188 switches those paths over; installing now means
// that switch needs no composition change. Like the other port
// installations in Open, repeating it across Open calls is safe: every call
// registers the same shape of implementation.
func installCredentialStores(sqlDB *sql.DB) {
	store := &state.ConfigStore{DB: sqlDB}
	openaiauth.SetCredentialsStore(dbCredentialStore{store: store, section: "auth.chatgpt"})
	opencode.SetCredentialsStore(dbCredentialStore{store: store, section: "auth.opencode"})
	mcp.SetOAuthOverlayStore(dbMCPOAuthStore{store: store})
}

// ReadConfigFile returns the file's text. A home with no file yet reads as the
// empty string: the editor has nothing to show, and writes what the user types.
func ReadConfigFile(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(body), nil
}

// WriteConfigFile replaces the file with body, atomically — no reader ever sees
// a half-written configuration — at 0600 for a file it creates and at the mode
// of the file it replaces. It writes no database row: the file is the user's.
func WriteConfigFile(path string, body []byte) error {
	return writeFileAtomic0600(path, body)
}
