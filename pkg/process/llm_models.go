// The model table's process-level write API, the one-time migration that
// rewrites a pre-table install's model references into row ids, and the legacy
// reader the migrations of the retired home .env need (plans 262, 268).
//
// Nothing in the runtime reads .env: the reader below exists only for the
// one-time migrations that lift its values into the state database. The write
// API is what a configuration surface calls to configure a model: the table is
// the only place a provider and its credential live (plan 315).
package process

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	homepkg "github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
	"github.com/joho/godotenv"
)

// openStateForWrite opens the state database a configuration write goes to.
// The flow that calls it may run before anything else has laid the home out,
// so the state directory is created here; the database lives where the
// configuration file's database section says (plan 209), and an empty URL is
// the home's own SQLite file.
func openStateForWrite(rt Context) (*sql.DB, error) {
	if rt.ConfigPath == "" {
		return nil, fmt.Errorf("empty config path")
	}
	if rt.Home == "" {
		return nil, fmt.Errorf("empty home")
	}
	if err := ensureStartupConfigFile(rt.ConfigPath); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(rt.Home, "state"), 0o755); err != nil {
		return nil, err
	}
	databaseURL, err := appcfg.FileDatabaseURL(rt.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("read database.url from %s: %w", rt.ConfigPath, err)
	}
	db, err := state.OpenStateFromHome(context.Background(), rt.Home, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}
	return db, nil
}

// readLegacyDotEnv reads <home>/.env for the one-time migrations that move its
// values into the state database (plans 262, 268). Nothing else reads the file:
// a missing or unreadable file is an empty map, never an error.
func readLegacyDotEnv(home string) map[string]string {
	b, err := os.ReadFile(homepkg.LegacyEnvPath(home))
	if err != nil {
		return map[string]string{}
	}
	// CRLF first, so a file written on Windows reads the way it was meant.
	out, err := godotenv.Unmarshal(strings.ReplaceAll(string(b), "\r\n", "\n"))
	if err != nil {
		return map[string]string{}
	}
	return out
}

// resolveLegacySecret turns a stored value into the plaintext it stood for: a
// whole-value ${NAME} reference resolves from the legacy .env first, then the
// process environment; any other value is already plaintext. ok is false when
// a reference resolved to nothing.
func resolveLegacySecret(value string, dotenv map[string]string) (string, bool) {
	value = strings.TrimSpace(value)
	if !isEnvReference(value) {
		// A plaintext key, or a partial reference the compose-time expansion
		// handles: stored as it stands.
		return value, true
	}
	name := strings.TrimSuffix(strings.TrimPrefix(value, "${"), "}")
	if resolved := strings.TrimSpace(dotenv[name]); resolved != "" {
		return resolved, true
	}
	if resolved := strings.TrimSpace(os.Getenv(name)); resolved != "" {
		return resolved, true
	}
	return "", false
}

// The two reads and the one write the session backfill makes. They are spelled
// out per dialect because every one of them carries placeholders.
var (
	legacySessionModelRefsSQL = dbx.SQL{
		SQLite: `SELECT DISTINCT s.agent_id, m.provider, m.model FROM fb_session_model_state m
JOIN fb_sessions s ON s.id = m.session_id WHERE m.model_id = ''`,
		MySQL: `SELECT DISTINCT s.agent_id, m.provider, m.model FROM fb_session_model_state m
JOIN fb_sessions s ON s.id = m.session_id WHERE m.model_id = ''`,
		Postgres: `SELECT DISTINCT s.agent_id, m.provider, m.model FROM fb_session_model_state m
JOIN fb_sessions s ON s.id = m.session_id WHERE m.model_id = ''`,
	}
	legacySessionModelBackfillSQL = dbx.SQL{
		SQLite: `UPDATE fb_session_model_state SET model_id = ? WHERE model_id = '' AND provider = ? AND model = ?
AND session_id IN (SELECT id FROM fb_sessions WHERE agent_id = ?)`,
		MySQL: `UPDATE fb_session_model_state SET model_id = ? WHERE model_id = '' AND provider = ? AND model = ?
AND session_id IN (SELECT id FROM fb_sessions WHERE agent_id = ?)`,
		Postgres: `UPDATE fb_session_model_state SET model_id = $1 WHERE model_id = '' AND provider = $2 AND model = $3
AND session_id IN (SELECT id FROM fb_sessions WHERE agent_id = $4)`,
	}
)

// migrateLegacyLLMModels rewrites every model reference a pre-table install
// stored as a legacy "provider/model" label into the row id of the model the
// install already carries, and names the row every stored session selection was
// running on (fb_session_model_state.model_id). It is gated on content, not on a
// marker — a legacy yaml imported into a fresh home later (test harnesses do
// this) is migrated on the next start the same way.
//
// The provider half of the old migration is gone with the lists it moved (plan
// 315): the model table is the only place a model is configured, so a home that
// kept models in the agents section keeps none of them — its references fall
// back to the global default. No migration was written for those, by owner
// ruling.
func migrateLegacyLLMModels(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("no state database")
	}
	// One process migrates at a time: two replicas sharing a database would
	// otherwise both read the same legacy list and race to store it.
	release, err := dbx.AcquireMigrationLock(ctx, db)
	if err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer release()

	store := &state.ConfigStore{DB: db}
	sections, err := store.LoadSections(ctx)
	if err != nil {
		return fmt.Errorf("read stored configuration: %w", err)
	}
	agents, memories, err := storedModelSections(sections)
	if err != nil {
		return err
	}
	modelStore := &state.LLMModelStore{DB: db}
	rows, err := modelStore.List(ctx)
	if err != nil {
		return fmt.Errorf("read the model table: %w", err)
	}
	root := appcfg.Root{Agents: agents, Memories: memories}
	if root.LLMModels, err = llmModelsFromRows(rows); err != nil {
		return err
	}
	rowIDs := make(map[string]bool, len(rows))
	for _, row := range rows {
		rowIDs[row.ID] = true
	}
	if !legacyModelDataPresent(&root, rowIDs) {
		return nil
	}

	// The view is what says which entry a stored reference names.
	view := appcfg.ConfiguredLLMs(&root)
	edits := planModelReferenceEdits(&root, view, rowIDs)

	if err := backfillSessionModelIDs(ctx, db, &root, view); err != nil {
		return err
	}
	// The sections are rewritten last, through the store's transaction and
	// its validator: a candidate the loader would refuse fails the whole
	// start.
	if err := rewriteLegacyModelSections(ctx, store, edits); err != nil {
		return err
	}
	slog.Info("rewrote legacy model references into row ids")
	return nil
}

// storedModelSections unmarshals the two sections the migration rewrites. A
// section the database does not hold is its zero value, never an error.
func storedModelSections(sections map[string]json.RawMessage) (appcfg.AgentsSection, appcfg.MemorySection, error) {
	var agents appcfg.AgentsSection
	if raw := sections["agents"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &agents); err != nil {
			return appcfg.AgentsSection{}, appcfg.MemorySection{}, fmt.Errorf("read agents section: %w", err)
		}
	}
	var memories appcfg.MemorySection
	if raw := sections["memories"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &memories); err != nil {
			return appcfg.AgentsSection{}, appcfg.MemorySection{}, fmt.Errorf("read memories section: %w", err)
		}
	}
	return agents, memories, nil
}

// legacyModelDataPresent reports whether any configuration still holds a
// pre-table model reference: a "provider/model" label written before the model
// table existed.
func legacyModelDataPresent(root *appcfg.Root, rowIDs map[string]bool) bool {
	for _, def := range root.Agents.Definitions {
		if legacyModelLabel(def.DefaultModel, rowIDs) {
			return true
		}
		for _, sub := range def.Subagents {
			if legacyModelLabel(sub.Model, rowIDs) {
				return true
			}
		}
	}
	return legacyModelLabel(root.Memories.ExtractModel, rowIDs) ||
		legacyModelLabel(root.Memories.ConsolidationModel, rowIDs)
}

// legacyModelLabel reports whether a stored reference is a pre-table
// "provider/model" label: it carries a slash, is not the memory pipeline's
// "current", and names no row of the table.
func legacyModelLabel(ref string, rowIDs map[string]bool) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" || !strings.Contains(ref, "/") {
		return false
	}
	if strings.EqualFold(ref, appcfg.MemoryModelCurrent) {
		return false
	}
	return !rowIDs[ref]
}

// viewRowIDForIdentity names the entry the view holds for one endpoint. Rows
// come before legacy entries and are de-duplicated on exactly this identity, so
// a legacy entry whose endpoint a row already carries answers the row's id —
// the reference has to name the row that will survive the strip.
func viewRowIDForIdentity(view []appcfg.AgentLLMProviderConfig, provider, model, baseURL, apiPath string) (string, bool) {
	provider, model, baseURL, apiPath = appcfg.NormalizeLLMModelIdentity(provider, model, baseURL, apiPath)
	for _, entry := range view {
		if entry.Provider == provider && entry.Model == model &&
			entry.BaseURL == baseURL && entry.APIPath == apiPath {
			return entry.ID, true
		}
	}
	return "", false
}

// resolveModelRefID maps one stored reference to the id of the row that
// carries the model it names.
func resolveModelRefID(root *appcfg.Root, view []appcfg.AgentLLMProviderConfig, agent, ref string) (string, bool) {
	entry, ok := appcfg.ResolveModelRef(root, agent, ref)
	if !ok {
		return "", false
	}
	if id, found := viewRowIDForIdentity(view, entry.Provider, entry.Model, entry.BaseURL, entry.APIPath); found {
		return id, true
	}
	return entry.ID, true
}

// legacyModelEdits is what the migration rewrites in the stored configuration,
// computed against the sections as they stand before the lists are stripped: a
// reference that named a legacy label now names the row carrying that model.
type legacyModelEdits struct {
	defaults    map[string]string         // agent -> the definition's new default_model
	subagents   map[string]map[int]string // agent -> subagent index -> new model
	extract     *string
	consolidate *string
}

// planModelReferenceEdits works out every rewritten reference. A label nothing
// resolves to is dropped rather than stored dangling — the loader refuses one,
// and the log line names the agent, never a key.
func planModelReferenceEdits(root *appcfg.Root, view []appcfg.AgentLLMProviderConfig, rowIDs map[string]bool) legacyModelEdits {
	edits := legacyModelEdits{
		defaults:  make(map[string]string, len(root.Agents.Definitions)),
		subagents: make(map[string]map[int]string),
	}
	for name, def := range root.Agents.Definitions {
		ref := strings.TrimSpace(def.DefaultModel)
		if legacyModelLabel(ref, rowIDs) {
			if id, ok := resolveModelRefID(root, view, name, ref); ok {
				edits.defaults[name] = id
			} else {
				edits.defaults[name] = ""
				slog.Warn("dropping a default model that matches no configured model", "agent", name)
			}
		}
		for i, sub := range def.Subagents {
			model := strings.TrimSpace(sub.Model)
			if !legacyModelLabel(model, rowIDs) {
				continue
			}
			id, ok := resolveModelRefID(root, view, name, model)
			if !ok {
				id = ""
				slog.Warn("dropping a subagent model that matches no configured model",
					"agent", name, "subagent", strings.TrimSpace(sub.Name))
			}
			if edits.subagents[name] == nil {
				edits.subagents[name] = make(map[int]string, len(def.Subagents))
			}
			edits.subagents[name][i] = id
		}
	}
	if id, changed := memoryModelEdit(root, view, root.Memories.ExtractModel, rowIDs); changed {
		edits.extract = id
	}
	if id, changed := memoryModelEdit(root, view, root.Memories.ConsolidationModel, rowIDs); changed {
		edits.consolidate = id
	}
	return edits
}

// memoryModelEdit is the rewritten value of one memory model setting. The
// settings page stores "current" or a model name; a "provider/model" label is
// the only shape that never resolved, because the memory pipeline matches a
// bare model name against the configured rows — so a bare name keeps its shape
// and only a label is rewritten. A label nothing resolves to becomes "current",
// which is what an unset value means.
func memoryModelEdit(root *appcfg.Root, view []appcfg.AgentLLMProviderConfig, value string, rowIDs map[string]bool) (*string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, appcfg.MemoryModelCurrent) || rowIDs[value] {
		return nil, false
	}
	if !strings.Contains(value, "/") {
		return nil, false
	}
	if id, ok := resolveModelRefID(root, view, "main", value); ok {
		return &id, true
	}
	slog.Warn("a memory model matches no configured model; running the memory pipeline on the conversation's own model",
		"model", value)
	current := appcfg.MemoryModelCurrent
	return &current, true
}

// backfillSessionModelIDs names the row every stored session selection was
// running on before ids existed, so a resumed session keeps its model after the
// legacy lists are gone.
func backfillSessionModelIDs(ctx context.Context, db *sql.DB, root *appcfg.Root, view []appcfg.AgentLLMProviderConfig) error {
	rows, err := db.QueryContext(ctx, legacySessionModelRefsSQL.For(dbx.DialectOf(db)))
	if err != nil {
		return fmt.Errorf("read session model selections: %w", err)
	}
	type selection struct {
		agentID  string
		provider string
		model    string
	}
	var selections []selection
	for rows.Next() {
		var sel selection
		if err := rows.Scan(&sel.agentID, &sel.provider, &sel.model); err != nil {
			rows.Close()
			return fmt.Errorf("read session model selections: %w", err)
		}
		selections = append(selections, sel)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read session model selections: %w", err)
	}
	rows.Close()
	for _, sel := range selections {
		id, ok := resolveModelRefID(root, view, sel.agentID, sel.provider+"/"+sel.model)
		if !ok {
			continue
		}
		if _, err := dbx.Exec(ctx, db, legacySessionModelBackfillSQL.For(dbx.DialectOf(db)),
			id, sel.provider, sel.model, sel.agentID); err != nil {
			return fmt.Errorf("backfill session model ids: %w", err)
		}
	}
	return nil
}

// rewriteLegacyModelSections applies the planned references in one transaction
// over both sections. The values are re-read inside it: the stored configuration
// is the truth, and a section the database does not hold is never created here.
func rewriteLegacyModelSections(ctx context.Context, store *state.ConfigStore, edits legacyModelEdits) error {
	return store.UpdateSections(ctx, []string{"agents", "memories"}, func(cur map[string]json.RawMessage) (map[string]json.RawMessage, error) {
		next := make(map[string]json.RawMessage, 2)
		if raw := cur["agents"]; len(raw) > 0 {
			var agents appcfg.AgentsSection
			if err := json.Unmarshal(raw, &agents); err != nil {
				return nil, fmt.Errorf("read agents section: %w", err)
			}
			changed := false
			for name, def := range agents.Definitions {
				if value, ok := edits.defaults[name]; ok {
					def.DefaultModel = value
					changed = true
				}
				if slots, ok := edits.subagents[name]; ok {
					for index, value := range slots {
						if index < 0 || index >= len(def.Subagents) {
							continue
						}
						def.Subagents[index].Model = value
						changed = true
					}
				}
				agents.Definitions[name] = def
			}
			if changed {
				out, err := json.Marshal(agents)
				if err != nil {
					return nil, fmt.Errorf("write agents section: %w", err)
				}
				next["agents"] = out
			}
		}
		if raw := cur["memories"]; len(raw) > 0 {
			var memories appcfg.MemorySection
			if err := json.Unmarshal(raw, &memories); err != nil {
				return nil, fmt.Errorf("read memories section: %w", err)
			}
			changed := false
			if edits.extract != nil {
				memories.ExtractModel = *edits.extract
				changed = true
			}
			if edits.consolidate != nil {
				memories.ConsolidationModel = *edits.consolidate
				changed = true
			}
			if changed {
				out, err := json.Marshal(memories)
				if err != nil {
					return nil, fmt.Errorf("write memories section: %w", err)
				}
				next["memories"] = out
			}
		}
		return next, nil
	})
}

// StateDB is the install's state database for a surface that writes one model
// row itself: the terminal's model setup checks a row's identity before saving
// and lets a subscription write the models it offers (plan 267), both outside
// process.Execute. It is the same handle Execute opens — the database the
// configuration names, the home's own SQLite file when it names none.
func StateDB(rt Context) (*sql.DB, error) { return openStateForWrite(rt) }

// LLMModelInput is what a surface hands in to configure one model.
type LLMModelInput struct {
	Provider string
	Model    string
	BaseURL  string
	APIPath  string
	// APIKey nil keeps the stored key on an update; "" clears it.
	APIKey *string
	// ParamsJSON nil keeps the stored params on an update; "" clears them.
	ParamsJSON *string
	// Source and SourcePlan are the subscription that wrote the row (plan 264
	// sets them); "" from setup and /connect with a typed key. An update keeps
	// a non-empty stored source unless the input names one.
	Source     string
	SourcePlan string
	// URLVerbatim is 「自定义协议」: the row's API path only names the
	// protocol, and the request goes to BaseURL exactly as stored. It is the
	// row's own property, so an input that does not name it writes a row that
	// does not carry it.
	URLVerbatim bool
	// SubagentAllowed is the row's authorization switch (owner 2026-11-10):
	// may the agent pick this row for a subagent on its own through the model
	// argument? nil keeps the stored value on an update; a new row treats nil
	// as false. Setup answers the question in its own flow, so it always names
	// one, even when the row it replaces already exists.
	SubagentAllowed *bool
	// CapEfforts are the thinking levels this model supports, for a model no
	// source documents (the custom form's ticked levels, plan 267). nil keeps
	// the stored value on an update; an empty slice states "no levels".
	CapEfforts *[]string
	// CapContextWindow and CapImages are the capabilities the subscription's
	// own listing declared for this model — the row's values, which win over
	// the catalog's. nil keeps the stored value on an update and leaves the row
	// without one on a create; a caller that does not know the window names nil
	// rather than 0, because 0 is not a window.
	CapContextWindow *int64
	CapImages        *bool
	// CapReasoningOff is whether a mode that turns thinking off exists: the
	// row's value, rendered as "none" at the head of its levels. nil keeps the
	// stored value on an update and leaves the row without one on a create.
	CapReasoningOff *bool
	// CapDefaultEffort is the level the listing marks as its default: the row's
	// value, applied to a request whose params name no effort. nil keeps the
	// stored value on an update and leaves the row without one on a create; a
	// non-nil "" states the listing names no default.
	CapDefaultEffort *string
	// CatalogProvider is the provider whose model data describes this row's
	// model (owner 2026-11-16), for a custom model whose official data is
	// filed under another provider. nil keeps the stored value on an update;
	// a non-nil "" clears it; a new row without one carries none.
	CatalogProvider *string
}

// LLMModelHeld returns the row this install already holds for one endpoint, as a
// save recognises it. A provider this machine signs in to carries no API path of
// its own — the login decides the endpoint, and a stored path is only what the
// models page displays — so such a row is recognised by its provider, model and
// base URL alone. Everything else is recognised by the identity a save
// normalizes: provider, model, base URL and path.
func LLMModelHeld(ctx context.Context, db *sql.DB, provider, model, baseURL, apiPath string) (state.LLMModel, bool, error) {
	if db == nil {
		return state.LLMModel{}, false, fmt.Errorf("no state database")
	}
	provider, model, baseURL, apiPath = appcfg.NormalizeLLMModelIdentity(provider, model, baseURL, apiPath)
	store := &state.LLMModelStore{DB: db}
	if apiPath != "" || !appcfg.CredentialsBackedProvider(provider) {
		return store.FindByIdentity(ctx, provider, model, baseURL, apiPath)
	}
	rows, err := store.List(ctx)
	if err != nil {
		return state.LLMModel{}, false, err
	}
	for _, row := range rows {
		// Oldest first: the row this endpoint was first configured as is the
		// one a save updates, the same "first wins" rule ConfiguredLLMs uses.
		if row.Provider == provider && row.Model == model && row.BaseURL == baseURL {
			return row, true, nil
		}
	}
	return state.LLMModel{}, false, nil
}

// UpsertLLMModel stores one model by identity (normalized with
// appcfg.NormalizeLLMModelIdentity): an existing row with the same provider,
// model, base URL and API path is updated in place — /connect signing in
// again replaces the key it had — and any other input adds a row. It returns
// the row id. Surfaces that must refuse a duplicate instead (the web "add"
// dialog) call the store's Create directly and get state.ErrLLMModelExists.
//
// The api path is completed to the protocol's default before the lookup, so
// "" and an explicit "/chat/completions" are one identity and cannot become
// two rows.
//
// What counts as "the same row" is LLMModelHeld's answer: a login-backed
// provider's path is not part of the row, because the login decides the
// endpoint, so an empty input keeps the display path the row already carries
// instead of clearing it.
func UpsertLLMModel(ctx context.Context, db *sql.DB, in LLMModelInput) (string, error) {
	if db == nil {
		return "", fmt.Errorf("no state database")
	}
	store := &state.LLMModelStore{DB: db}
	provider, model, baseURL, apiPath := appcfg.NormalizeLLMModelIdentity(in.Provider, in.Model, in.BaseURL, in.APIPath)
	if provider == "" {
		return "", fmt.Errorf("provider required in non-interactive mode")
	}
	if model == "" {
		return "", fmt.Errorf("model required in non-interactive mode")
	}
	login := appcfg.CredentialsBackedProvider(provider)
	if !login && baseURL == "" {
		return "", fmt.Errorf("base_url required in non-interactive mode")
	}
	existing, found, err := LLMModelHeld(ctx, db, provider, model, baseURL, apiPath)
	if err != nil {
		return "", err
	}
	if found {
		row := existing
		row.Provider, row.Model, row.BaseURL = provider, model, baseURL
		if apiPath != "" {
			row.APIPath = apiPath
		}
		if in.APIKey != nil {
			row.APIKey = strings.TrimSpace(*in.APIKey)
		}
		if row.APIKey == "" && !login {
			return "", fmt.Errorf("api_key required in non-interactive mode")
		}
		if in.ParamsJSON != nil {
			row.ParamsJSON = strings.TrimSpace(*in.ParamsJSON)
		}
		row.URLVerbatim = in.URLVerbatim
		if in.SubagentAllowed != nil {
			row.SubagentAllowed = *in.SubagentAllowed
		}
		if in.CapEfforts != nil {
			row.CapEfforts = append([]string{}, (*in.CapEfforts)...)
		}
		if in.CapContextWindow != nil {
			window := *in.CapContextWindow
			row.CapContextWindow = &window
		}
		if in.CapImages != nil {
			images := *in.CapImages
			row.CapImages = &images
		}
		if in.CapReasoningOff != nil {
			off := *in.CapReasoningOff
			row.CapReasoningOff = &off
		}
		if in.CapDefaultEffort != nil {
			row.CapDefaultEffort = strings.TrimSpace(*in.CapDefaultEffort)
		}
		if in.CatalogProvider != nil {
			row.CatalogProvider = strings.Trim(strings.TrimSpace(*in.CatalogProvider), "/")
		}
		if source := strings.TrimSpace(in.Source); source != "" {
			row.Source = source
		}
		if plan := strings.TrimSpace(in.SourcePlan); plan != "" {
			row.SourcePlan = plan
		}
		if _, err := store.Update(ctx, row); err != nil {
			return "", err
		}
		return row.ID, nil
	}
	key := ""
	if in.APIKey != nil {
		key = strings.TrimSpace(*in.APIKey)
	}
	if key == "" && !login {
		return "", fmt.Errorf("api_key required in non-interactive mode")
	}
	params := ""
	if in.ParamsJSON != nil {
		params = strings.TrimSpace(*in.ParamsJSON)
	}
	var efforts []string
	if in.CapEfforts != nil {
		efforts = append([]string{}, (*in.CapEfforts)...)
	}
	var window *int64
	if in.CapContextWindow != nil {
		named := *in.CapContextWindow
		window = &named
	}
	var images *bool
	if in.CapImages != nil {
		named := *in.CapImages
		images = &named
	}
	var reasoningOff *bool
	if in.CapReasoningOff != nil {
		named := *in.CapReasoningOff
		reasoningOff = &named
	}
	defaultEffort := ""
	if in.CapDefaultEffort != nil {
		defaultEffort = strings.TrimSpace(*in.CapDefaultEffort)
	}
	// A new row defaults to not authorized: nil means "not answered", and an
	// unanswered switch is off (owner 2026-11-10).
	subagentAllowed := false
	if in.SubagentAllowed != nil {
		subagentAllowed = *in.SubagentAllowed
	}
	catalogProvider := ""
	if in.CatalogProvider != nil {
		catalogProvider = strings.Trim(strings.TrimSpace(*in.CatalogProvider), "/")
	}
	created, err := store.Create(ctx, state.LLMModel{
		Provider:         provider,
		Model:            model,
		BaseURL:          baseURL,
		APIPath:          apiPath,
		APIKey:           key,
		ParamsJSON:       params,
		CapEfforts:       efforts,
		CapContextWindow: window,
		CapImages:        images,
		CapReasoningOff:  reasoningOff,
		CapDefaultEffort: defaultEffort,
		Source:           strings.TrimSpace(in.Source),
		SourcePlan:       strings.TrimSpace(in.SourcePlan),
		URLVerbatim:      in.URLVerbatim,
		SubagentAllowed:  subagentAllowed,
		CatalogProvider:  catalogProvider,
	})
	if err != nil {
		// A concurrent writer stored the same identity between the lookup and
		// the insert: the row it made is the one this call means.
		var exists state.ErrLLMModelExists
		if errors.As(err, &exists) {
			return exists.ID, nil
		}
		return "", err
	}
	return created.ID, nil
}

// SetAgentDefaultModel stores modelID as agents.definitions.<agent>.default_model.
// The id must name a row; the definition must exist, except "main", which is
// created (Primary: true) the way setup always did.
func SetAgentDefaultModel(ctx context.Context, db *sql.DB, agentName, modelID string) error {
	if db == nil {
		return fmt.Errorf("no state database")
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return fmt.Errorf("model id required")
	}
	if _, err := (&state.LLMModelStore{DB: db}).Get(ctx, modelID); err != nil {
		return err
	}
	name := agentNameOrMain(agentName)
	env := &Environment{SQL: db}
	return env.UpdateRootSection(ctx, "agents", func(cfg *appcfg.Root) error {
		if cfg.Agents.Definitions == nil {
			cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{}
		}
		def, ok := cfg.Agents.Definitions[name]
		if !ok {
			// An empty name means main, which setup creates; any other name
			// must already exist, because minting a definition from a typo
			// would silently produce a new agent.
			if name != "main" {
				return fmt.Errorf("agent %q is not defined in the config", name)
			}
			def.Primary = true
		}
		def.DefaultModel = modelID
		cfg.Agents.Definitions[name] = def
		return nil
	})
}

// probeTimeout bounds one connection test. It is longer than a request's
// patience on purpose: a reasoning model may spend seconds before its first
// token, and the person is watching a button they just pressed.
const probeTimeout = 20 * time.Second

// probeMaxOutputTokens is the shortest answer a model can be asked for. It is
// enough for a model to prove it answered, and small enough that a test costs
// nothing.
const probeMaxOutputTokens = 16

// ProbeLLMModel sends the smallest real request a configured model accepts —
// one user message, no tools, a 16-token answer — and reports whether it came
// back. It is what the models dialog's 测试连接 runs. The error is a short
// sentence safe to show: its class first, and never the key, the request body
// or the request URL.
func ProbeLLMModel(ctx context.Context, in LLMModelInput) error {
	provider, model, baseURL, apiPath := appcfg.NormalizeLLMModelIdentity(in.Provider, in.Model, in.BaseURL, in.APIPath)
	if provider == "" {
		return fmt.Errorf("provider is required")
	}
	if model == "" {
		return fmt.Errorf("model is required")
	}
	if appcfg.CredentialsBackedProvider(provider) {
		// A subscription row's credential is a login, and the login is what
		// lists and authenticates it: there is nothing to probe with a key.
		return errors.New("sign-in models are checked by signing in")
	}
	key := ""
	if in.APIKey != nil {
		key = strings.TrimSpace(*in.APIKey)
	}
	var params appcfg.LLMRequestParams
	if in.ParamsJSON != nil {
		params = appcfg.LLMRequestParams(strings.TrimSpace(*in.ParamsJSON))
	}
	client, err := run.NewLLMFromYAML(&run.LLMProviderYAML{
		Provider:        provider,
		Model:           model,
		APIKey:          key,
		BaseURL:         baseURL,
		APIPath:         apiPath,
		Params:          params,
		MaxOutputTokens: probeMaxOutputTokens,
		URLVerbatim:     in.URLVerbatim,
	})
	if err != nil {
		// A construction error is this build's own sentence ("a custom model
		// needs an API protocol"); it names no credential and no URL.
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if _, err := client.Execute(probeCtx, []llm.Message{llm.UserMessage(llm.Text("ping"))}, nil); err != nil {
		return probeFailure(err)
	}
	return nil
}

// probeFailure turns a request's failure into the one sentence a surface shows.
// Anything the provider or the transport said is dropped: an error string can
// carry the request URL's query, a response body, or a fragment of a key.
func probeFailure(err error) error {
	switch status := llm.HTTPStatusOf(err); {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return errors.New("the API key was refused")
	case status == http.StatusNotFound:
		return errors.New("the endpoint or model was not found")
	case status != 0:
		return fmt.Errorf("the request failed: HTTP %d", status)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("no answer within %s", probeTimeout)
	}
	var opErr *net.OpError
	var urlErr *url.Error
	if errors.As(err, &opErr) || errors.As(err, &urlErr) {
		return errors.New("could not reach the base URL")
	}
	if errors.Is(err, context.Canceled) {
		return errors.New("the request was cancelled")
	}
	return fmt.Errorf("the request failed: %s", probeErrorClass(err))
}

// probeErrorClass names a failure the provider reported without a status. The
// vocabulary is fixed: whatever the provider wrote stays out of it.
func probeErrorClass(err error) string {
	switch {
	case errors.Is(err, llm.ErrBlankModelOutput):
		return "an empty response"
	case errors.Is(err, llm.ErrStreamStalled):
		return "a stalled response"
	default:
		return "an unexpected response"
	}
}
