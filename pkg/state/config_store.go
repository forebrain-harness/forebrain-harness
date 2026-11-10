package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

// ConfigStore reads and writes the install's runtime configuration, the
// fb_config table plan 187 introduced. One row is one section: a root section
// of forebrain.yaml (agents, hooks, permissions, …) marshaled with its own
// json tags, or a credential section (auth.chatgpt, auth.opencode) holding
// the raw on-disk shape of the login file it came from. The startup sections
// — gateway and the transition-period credentials pointer — never appear
// here; they stay in the file.
type ConfigStore struct {
	DB *sql.DB
}

// The configuration and MCP-OAuth statements. Both tables are keyed by the
// primary key their upsert conflicts on — fb_config by section, fb_mcp_oauth
// by (project_key, server_name) — so the conflict target is the same one
// MySQL's ON DUPLICATE KEY UPDATE keys on.
var (
	configSectionsSelectSQL = dbx.SQL{
		SQLite:   `SELECT section, value_json FROM fb_config`,
		MySQL:    `SELECT section, value_json FROM fb_config`,
		Postgres: `SELECT section, value_json FROM fb_config`,
	}
	configSectionSelectSQL = dbx.SQL{
		SQLite:   `SELECT value_json FROM fb_config WHERE section = ?`,
		MySQL:    `SELECT value_json FROM fb_config WHERE section = ?`,
		Postgres: `SELECT value_json FROM fb_config WHERE section = $1`,
	}
	configSectionDeleteSQL = dbx.SQL{
		SQLite:   `DELETE FROM fb_config WHERE section = ?`,
		MySQL:    `DELETE FROM fb_config WHERE section = ?`,
		Postgres: `DELETE FROM fb_config WHERE section = $1`,
	}
	configSectionUpsertSQL = dbx.SQL{
		SQLite: `
INSERT INTO fb_config (section, value_json, updated_at_ms) VALUES (?,?,?)
ON CONFLICT(section) DO UPDATE SET value_json = excluded.value_json, updated_at_ms = excluded.updated_at_ms`,
		MySQL: `
INSERT INTO fb_config (section, value_json, updated_at_ms) VALUES (?,?,?)
ON DUPLICATE KEY UPDATE value_json = VALUES(value_json), updated_at_ms = VALUES(updated_at_ms)`,
		Postgres: `
INSERT INTO fb_config (section, value_json, updated_at_ms) VALUES ($1,$2,$3)
ON CONFLICT(section) DO UPDATE SET value_json = excluded.value_json, updated_at_ms = excluded.updated_at_ms`,
	}
	configSectionsDeleteSQL = dbx.SQL{
		SQLite:   `DELETE FROM fb_config`,
		MySQL:    `DELETE FROM fb_config`,
		Postgres: `DELETE FROM fb_config`,
	}
	configSectionInsertSQL = dbx.SQL{
		SQLite:   `INSERT INTO fb_config (section, value_json, updated_at_ms) VALUES (?,?,?)`,
		MySQL:    `INSERT INTO fb_config (section, value_json, updated_at_ms) VALUES (?,?,?)`,
		Postgres: `INSERT INTO fb_config (section, value_json, updated_at_ms) VALUES ($1,$2,$3)`,
	}
	mcpOAuthRowSelectSQL = dbx.SQL{
		SQLite:   `SELECT value_json FROM fb_mcp_oauth WHERE project_key = ? AND server_name = ?`,
		MySQL:    `SELECT value_json FROM fb_mcp_oauth WHERE project_key = ? AND server_name = ?`,
		Postgres: `SELECT value_json FROM fb_mcp_oauth WHERE project_key = $1 AND server_name = $2`,
	}
	mcpOAuthRowDeleteSQL = dbx.SQL{
		SQLite:   `DELETE FROM fb_mcp_oauth WHERE project_key = ? AND server_name = ?`,
		MySQL:    `DELETE FROM fb_mcp_oauth WHERE project_key = ? AND server_name = ?`,
		Postgres: `DELETE FROM fb_mcp_oauth WHERE project_key = $1 AND server_name = $2`,
	}
	mcpOAuthRowUpsertSQL = dbx.SQL{
		SQLite: `
INSERT INTO fb_mcp_oauth (project_key, server_name, value_json, updated_at_ms) VALUES (?,?,?,?)
ON CONFLICT(project_key, server_name) DO UPDATE SET value_json = excluded.value_json, updated_at_ms = excluded.updated_at_ms`,
		MySQL: `
INSERT INTO fb_mcp_oauth (project_key, server_name, value_json, updated_at_ms) VALUES (?,?,?,?)
ON DUPLICATE KEY UPDATE value_json = VALUES(value_json), updated_at_ms = VALUES(updated_at_ms)`,
		Postgres: `
INSERT INTO fb_mcp_oauth (project_key, server_name, value_json, updated_at_ms) VALUES ($1,$2,$3,$4)
ON CONFLICT(project_key, server_name) DO UPDATE SET value_json = excluded.value_json, updated_at_ms = excluded.updated_at_ms`,
	}
)

func (s *ConfigStore) ok() bool { return s != nil && s.DB != nil }

// SectionValidator is the write-side gate UpdateSection runs on every
// candidate before it lands (plan 188 §1): fn's result — including a nil
// result, which removes the section — must compose into a configuration the
// loader would accept, or the transaction rolls back. The composition root
// installs the real one, which knows the section table and the startup file;
// with none installed the store writes as it did before. Credential rows
// (auth.*) are written through SaveCredential and never reach it.
type SectionValidator func(ctx context.Context, section string, candidate json.RawMessage) error

var sectionValidator atomic.Value

type sectionValidatorHolder struct{ fn SectionValidator }

// SetSectionValidator installs the validator every ConfigStore in this
// process runs. Process-wide, like the credential store ports: one process
// has one configuration, so one validator covers every construction site —
// including the ones a package builds field-by-field from a database handle.
func SetSectionValidator(fn SectionValidator) {
	if fn == nil {
		sectionValidator.Store(sectionValidatorHolder{})
		return
	}
	sectionValidator.Store(sectionValidatorHolder{fn: fn})
}

// CurrentSectionValidator is the validator SetSectionValidator installed last,
// nil when none is installed. A caller that binds the gate to a database of its
// own for one write — a setup flow that opens its own handle — reads this first
// and puts it back afterwards, so the process keeps the gate over the database
// it actually keeps instead of one that closed with the call.
func CurrentSectionValidator() SectionValidator {
	if h, ok := sectionValidator.Load().(sectionValidatorHolder); ok {
		return h.fn
	}
	return nil
}

// InvalidSectionError marks a write the section validator refused: the value
// the caller sent would not compose into a loadable configuration. Callers
// answer it 4xx; every other UpdateSection error is the store failing.
type InvalidSectionError struct {
	Section string
	Err     error
}

func (e *InvalidSectionError) Error() string {
	return fmt.Sprintf("config section %s: %v", e.Section, e.Err)
}

func (e *InvalidSectionError) Unwrap() error { return e.Err }

// validateSection runs the installed validator, if any. A missing validator
// accepts everything; the composition root installs the real one before any
// surface writes.
func validateSection(ctx context.Context, section string, candidate json.RawMessage) error {
	if h, ok := sectionValidator.Load().(sectionValidatorHolder); ok && h.fn != nil {
		if err := h.fn(ctx, section, candidate); err != nil {
			return &InvalidSectionError{Section: section, Err: err}
		}
	}
	return nil
}

// LoadSections returns every stored section: the section name to its raw
// JSON value. An empty table is a valid configuration — every section then
// reads as its zero value — so an absent map entry and an error say different
// things and must stay distinguishable.
func (s *ConfigStore) LoadSections(ctx context.Context) (map[string]json.RawMessage, error) {
	if !s.ok() {
		return nil, fmt.Errorf("config store unavailable")
	}
	rows, err := s.DB.QueryContext(ctx, configSectionsSelectSQL.For(dbx.DialectOf(s.DB)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]json.RawMessage)
	for rows.Next() {
		var section, value string
		if err := rows.Scan(&section, &value); err != nil {
			return nil, err
		}
		out[section] = json.RawMessage(value)
	}
	return out, rows.Err()
}

// UpdateSection runs one read-modify-write of a single section. fn receives
// the current value (nil when the section is absent) and returns the next
// one; a nil result removes the section. The whole exchange runs inside one
// transaction, and every transaction on this pool takes the write lock up
// front (_txlock=immediate in the data source), so two writers serialize the
// way the file lock around forebrain.yaml used to make them. A fn error
// rolls the transaction back: the section keeps exactly the value it had.
// So does a validator rejection: the candidate — or the removal — must
// compose into a loadable configuration before it lands (plan 188 §1).
func (s *ConfigStore) UpdateSection(ctx context.Context, section string, fn func(cur json.RawMessage) (json.RawMessage, error)) error {
	if !s.ok() {
		return fmt.Errorf("config store unavailable")
	}
	if fn == nil {
		return fmt.Errorf("config section update needs a function")
	}
	section = strings.TrimSpace(section)
	if section == "" {
		return fmt.Errorf("config section name required")
	}
	tx, err := dbx.BeginTx(ctx, s.DB, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var cur []byte
	switch err := tx.QueryRowContext(ctx, configSectionSelectSQL.For(tx.Dialect()), section).Scan(&cur); {
	case err == sql.ErrNoRows:
		cur = nil
	case err != nil:
		return err
	}
	next, err := fn(json.RawMessage(cur))
	if err != nil {
		return err
	}
	if verr := validateSection(ctx, section, next); verr != nil {
		return verr
	}
	if next == nil {
		if _, err := tx.ExecContext(ctx, configSectionDeleteSQL.For(tx.Dialect()), section); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, configSectionUpsertSQL.For(tx.Dialect()),
			section, string(next), time.Now().UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpdateSections runs one read-modify-write of several sections in a single
// transaction (plan 191 §3.1). The settings page saves a group, and a group's
// items can live in more than one Root section — the approval group spans
// approval_preset, sandbox_workspace_write and batch — so such a save either
// lands whole or leaves every section exactly as it was. fn receives the
// current value of each named section (nil when absent) and returns the ones to
// write; a nil value for a name removes that section. Every written or removed
// section runs the same validator UpdateSection runs, inside the same
// transaction, so a group that would not compose is refused as a whole.
func (s *ConfigStore) UpdateSections(ctx context.Context, sections []string, fn func(cur map[string]json.RawMessage) (map[string]json.RawMessage, error)) error {
	if !s.ok() {
		return fmt.Errorf("config store unavailable")
	}
	if fn == nil {
		return fmt.Errorf("config section update needs a function")
	}
	names := make([]string, 0, len(sections))
	seen := make(map[string]bool, len(sections))
	for _, name := range sections {
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("config section name required")
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	if len(names) == 0 {
		return fmt.Errorf("config section name required")
	}
	tx, err := dbx.BeginTx(ctx, s.DB, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cur := make(map[string]json.RawMessage, len(names))
	for _, name := range names {
		var raw []byte
		switch err := tx.QueryRowContext(ctx, configSectionSelectSQL.For(tx.Dialect()), name).Scan(&raw); {
		case err == sql.ErrNoRows:
			cur[name] = nil
		case err != nil:
			return err
		default:
			cur[name] = json.RawMessage(raw)
		}
	}
	next, err := fn(cur)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for _, name := range names {
		value, ok := next[name]
		if !ok {
			continue
		}
		if verr := validateSection(ctx, name, value); verr != nil {
			return verr
		}
		if value == nil {
			if _, err := tx.ExecContext(ctx, configSectionDeleteSQL.For(tx.Dialect()), name); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, configSectionUpsertSQL.For(tx.Dialect()),
			name, string(value), now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReplaceAll swaps the table's whole contents for sections in one
// transaction. It exists for the one-time legacy import at startup, which
// must not leave a half-imported configuration behind; every later write
// goes through UpdateSection. An empty map clears the table.
func (s *ConfigStore) ReplaceAll(ctx context.Context, sections map[string]json.RawMessage) error {
	if !s.ok() {
		return fmt.Errorf("config store unavailable")
	}
	tx, err := dbx.BeginTx(ctx, s.DB, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, configSectionsDeleteSQL.For(tx.Dialect())); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for section, value := range sections {
		section = strings.TrimSpace(section)
		if section == "" {
			return fmt.Errorf("config section name required")
		}
		if value == nil {
			continue
		}
		if _, err := tx.ExecContext(ctx, configSectionInsertSQL.For(tx.Dialect()),
			section, string(value), now); err != nil {
			return fmt.Errorf("store section %s: %w", section, err)
		}
	}
	return tx.Commit()
}

// LoadCredential reads one credential section — auth.chatgpt or
// auth.opencode. Absent is not an error: it is the answer "no login stored",
// reported as a nil value.
func (s *ConfigStore) LoadCredential(ctx context.Context, section string) (json.RawMessage, error) {
	if !s.ok() {
		return nil, fmt.Errorf("config store unavailable")
	}
	var value string
	switch err := s.DB.QueryRowContext(ctx, configSectionSelectSQL.For(dbx.DialectOf(s.DB)), strings.TrimSpace(section)).Scan(&value); {
	case err == sql.ErrNoRows:
		return nil, nil
	case err != nil:
		return nil, err
	}
	return json.RawMessage(value), nil
}

// SaveCredential writes one credential section. A nil value removes it.
func (s *ConfigStore) SaveCredential(ctx context.Context, section string, value json.RawMessage) error {
	if !s.ok() {
		return fmt.Errorf("config store unavailable")
	}
	section = strings.TrimSpace(section)
	if section == "" {
		return fmt.Errorf("config section name required")
	}
	if value == nil {
		_, err := dbx.Exec(ctx, s.DB, configSectionDeleteSQL.For(dbx.DialectOf(s.DB)), section)
		return err
	}
	_, err := dbx.Exec(ctx, s.DB, configSectionUpsertSQL.For(dbx.DialectOf(s.DB)),
		section, string(value), time.Now().UnixMilli())
	return err
}

// LoadMCPOAuthRow reads one server's stored OAuth overlay. An empty
// projectKey is the shared global store; otherwise it is the project key or
// "agent:<id>" the server's scope stamp names. Absent is not an error: it is
// the answer "no overlay stored", reported as a nil value.
func (s *ConfigStore) LoadMCPOAuthRow(ctx context.Context, projectKey, serverName string) (json.RawMessage, error) {
	if !s.ok() {
		return nil, fmt.Errorf("config store unavailable")
	}
	var value string
	switch err := s.DB.QueryRowContext(ctx, mcpOAuthRowSelectSQL.For(dbx.DialectOf(s.DB)),
		strings.TrimSpace(projectKey), strings.TrimSpace(serverName)).Scan(&value); {
	case err == sql.ErrNoRows:
		return nil, nil
	case err != nil:
		return nil, err
	}
	return json.RawMessage(value), nil
}

// SaveMCPOAuthRow writes one server's OAuth overlay, replacing the row when
// it exists. A nil value removes it.
func (s *ConfigStore) SaveMCPOAuthRow(ctx context.Context, projectKey, serverName string, value json.RawMessage) error {
	if !s.ok() {
		return fmt.Errorf("config store unavailable")
	}
	projectKey, serverName = strings.TrimSpace(projectKey), strings.TrimSpace(serverName)
	if serverName == "" {
		return fmt.Errorf("mcp oauth server name required")
	}
	if value == nil {
		_, err := dbx.Exec(ctx, s.DB, mcpOAuthRowDeleteSQL.For(dbx.DialectOf(s.DB)), projectKey, serverName)
		return err
	}
	_, err := dbx.Exec(ctx, s.DB, mcpOAuthRowUpsertSQL.For(dbx.DialectOf(s.DB)),
		projectKey, serverName, string(value), time.Now().UnixMilli())
	return err
}

// The install-wide model configurations, the sibling table of fb_config and
// shared by every primary agent.
//
// LLMModel is one fb_llm_models row: one way this install can reach a model.
// The same provider+model may be configured several times — different
// base_url, api_path or key — so callers name a row by its ID, never by
// "provider/model". A nil capability field means "use the model catalog"; a
// non-nil one overrides it for this row only (owner 2026-11-07).
type LLMModel struct {
	ID         string
	Provider   string
	Model      string
	BaseURL    string
	APIPath    string
	APIKey     string
	ParamsJSON string

	CapImages       *bool // nil = catalog
	CapPDF          *bool
	CapVideo        *bool
	CapAudio        *bool
	CapReasoning    *bool
	CapReasoningOff *bool

	CapEfforts       []string // nil = catalog; empty (non-nil) = explicitly no levels
	CapDefaultEffort string   // '' = catalog / request default
	CapContextWindow *int64   // nil = catalog
	CapMaxOutput     *int64   // nil = catalog

	// Source is the subscription id that wrote the row ('' = added by hand);
	// SourcePlan the plan inside it. Neither is part of the identity.
	Source     string
	SourcePlan string

	// URLVerbatim sends requests to BaseURL exactly as stored (「自定义协议」);
	// APIPath still names the wire protocol. Not part of the identity.
	URLVerbatim bool

	// SubagentAllowed is the authorization switch (owner 2026-11-10): 1 = the
	// agent may pick this row for a subagent on its own through the model
	// argument, 0 = only the user's own choices use it. Not part of the
	// identity; authorization checks read the table live, not this copy.
	SubagentAllowed bool

	CreatedAt int64 // unix ms
	UpdatedAt int64 // unix ms
}

// ConfigRow projects this row into the configuration view's model type: the
// one conversion every reader of a live row shares (283), so a row the
// subagent dispatch reads from the table carries the same params and caps the
// snapshot's rows do. The identity fields are not normalized here — the view's
// own projection (appcfg.ProviderFromModelRow) does that on top.
func (m LLMModel) ConfigRow() (appcfg.LLMModelConfig, error) {
	params, err := appcfg.ParseLLMRequestParamsJSON(m.ParamsJSON)
	if err != nil {
		return appcfg.LLMModelConfig{}, err
	}
	return appcfg.LLMModelConfig{
		ID:       m.ID,
		Provider: m.Provider,
		Model:    m.Model,
		BaseURL:  m.BaseURL,
		APIPath:  m.APIPath,
		APIKey:   m.APIKey,
		Params:   params,
		Caps: appcfg.LLMModelCaps{
			Images:        m.CapImages,
			PDF:           m.CapPDF,
			Video:         m.CapVideo,
			Audio:         m.CapAudio,
			Reasoning:     m.CapReasoning,
			ReasoningOff:  m.CapReasoningOff,
			Efforts:       m.CapEfforts,
			DefaultEffort: m.CapDefaultEffort,
			ContextWindow: m.CapContextWindow,
			MaxOutput:     m.CapMaxOutput,
		},
		URLVerbatim:     m.URLVerbatim,
		SubagentAllowed: m.SubagentAllowed,
	}, nil
}

// ModelOverrideFromCaps projects a row's capability columns into the model
// catalog's override type. ok is false for a row that names no capability at
// all. It lives in pkg/state — which already imports pkg/config and pkg/llm —
// rather than on appcfg.LLMModelCaps, because pkg/config's production files
// do not import pkg/llm and must not start to (the package graph pins that).
func ModelOverrideFromCaps(caps appcfg.LLMModelCaps) (llm.ModelOverride, bool) {
	if caps.Images == nil && caps.PDF == nil && caps.Video == nil && caps.Audio == nil &&
		caps.Reasoning == nil && caps.ReasoningOff == nil && caps.Efforts == nil &&
		caps.ContextWindow == nil && caps.MaxOutput == nil {
		return llm.ModelOverride{}, false
	}
	return llm.ModelOverride{
		SupportsImages: caps.Images,
		SupportsPDF:    caps.PDF,
		SupportsVideo:  caps.Video,
		SupportsAudio:  caps.Audio,
		CanReason:      caps.Reasoning,
		ReasoningOff:   caps.ReasoningOff,
		Efforts:        caps.Efforts,
		ContextWindow:  caps.ContextWindow,
		MaxOutput:      caps.MaxOutput,
	}, true
}

// ErrLLMModelExists reports that another row already has this identity.
type ErrLLMModelExists struct{ ID string }

func (e ErrLLMModelExists) Error() string {
	return "a model with this provider, model, base URL and API path is already configured (" + e.ID + ")"
}

// ErrLLMModelNotFound reports a row id that no configuration carries.
var ErrLLMModelNotFound = errors.New("model configuration not found")

// LLMModelStore reads and writes the install's model configurations. The table
// is global — every primary agent shares it — so this store takes no agent id.
type LLMModelStore struct{ DB *sql.DB }

// NormalizeLLMModelIdentity puts the four identity fields into the one form
// the unique index and the lookups use, so "OpenAI" and "openai", or a
// base_url with a trailing slash, cannot slip two rows past a duplicate check.
// Provider is case-folded; Model keeps its case (some services compare it
// case-sensitively and custom models may carry a vendor prefix). An empty
// api_path stays empty: filling in a protocol's default path needs provider
// knowledge this layer does not have.
func NormalizeLLMModelIdentity(m *LLMModel) {
	if m == nil {
		return
	}
	m.Provider = strings.ToLower(strings.TrimSpace(m.Provider))
	m.Model = strings.TrimSpace(m.Model)
	m.BaseURL = strings.TrimRight(strings.TrimSpace(m.BaseURL), "/")
	m.APIPath = strings.TrimSpace(m.APIPath)
	if m.APIPath != "" && !strings.HasPrefix(m.APIPath, "/") {
		m.APIPath = "/" + m.APIPath
	}
	m.APIPath = strings.TrimRight(m.APIPath, "/")
}

const llmModelColumns = `id, provider, model, base_url, api_path, api_key, params_json,
cap_images, cap_pdf, cap_video, cap_audio, cap_reasoning, cap_reasoning_off,
cap_efforts_json, cap_default_effort, cap_context_window, cap_max_output,
source, source_plan, url_verbatim, subagent_allowed, created_at, updated_at`

var (
	llmModelListSQL = dbx.SQL{
		SQLite:   `SELECT ` + llmModelColumns + ` FROM fb_llm_models ORDER BY created_at, id`,
		MySQL:    `SELECT ` + llmModelColumns + ` FROM fb_llm_models ORDER BY created_at, id`,
		Postgres: `SELECT ` + llmModelColumns + ` FROM fb_llm_models ORDER BY created_at, id`,
	}
	llmModelGetSQL = dbx.SQL{
		SQLite:   `SELECT ` + llmModelColumns + ` FROM fb_llm_models WHERE id=?`,
		MySQL:    `SELECT ` + llmModelColumns + ` FROM fb_llm_models WHERE id=?`,
		Postgres: `SELECT ` + llmModelColumns + ` FROM fb_llm_models WHERE id=$1`,
	}
	llmModelFindIdentityPlaceholders = dbx.SQL{
		SQLite:   `SELECT ` + llmModelColumns + ` FROM fb_llm_models WHERE provider=? AND model=? AND base_url=? AND api_path=?`,
		MySQL:    `SELECT ` + llmModelColumns + ` FROM fb_llm_models WHERE provider=? AND model=? AND base_url=? AND api_path=?`,
		Postgres: `SELECT ` + llmModelColumns + ` FROM fb_llm_models WHERE provider=$1 AND model=$2 AND base_url=$3 AND api_path=$4`,
	}
	llmModelInsertSQL = dbx.SQL{
		SQLite: `INSERT INTO fb_llm_models (` + llmModelColumns + `) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		MySQL:  `INSERT INTO fb_llm_models (` + llmModelColumns + `) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		Postgres: `INSERT INTO fb_llm_models (` + llmModelColumns + `) VALUES
		($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)`,
	}
	llmModelUpdateSQL = dbx.SQL{
		SQLite: `UPDATE fb_llm_models SET provider=?, model=?, base_url=?, api_path=?, api_key=?, params_json=?,
		cap_images=?, cap_pdf=?, cap_video=?, cap_audio=?, cap_reasoning=?, cap_reasoning_off=?,
		cap_efforts_json=?, cap_default_effort=?, cap_context_window=?, cap_max_output=?,
		source=?, source_plan=?, url_verbatim=?, subagent_allowed=?, updated_at=? WHERE id=?`,
		MySQL: `UPDATE fb_llm_models SET provider=?, model=?, base_url=?, api_path=?, api_key=?, params_json=?,
		cap_images=?, cap_pdf=?, cap_video=?, cap_audio=?, cap_reasoning=?, cap_reasoning_off=?,
		cap_efforts_json=?, cap_default_effort=?, cap_context_window=?, cap_max_output=?,
		source=?, source_plan=?, url_verbatim=?, subagent_allowed=?, updated_at=? WHERE id=?`,
		Postgres: `UPDATE fb_llm_models SET provider=$1, model=$2, base_url=$3, api_path=$4, api_key=$5, params_json=$6,
		cap_images=$7, cap_pdf=$8, cap_video=$9, cap_audio=$10, cap_reasoning=$11, cap_reasoning_off=$12,
		cap_efforts_json=$13, cap_default_effort=$14, cap_context_window=$15, cap_max_output=$16,
		source=$17, source_plan=$18, url_verbatim=$19, subagent_allowed=$20, updated_at=$21 WHERE id=$22`,
	}
	llmModelDeleteSQL = dbx.SQL{
		SQLite:   `DELETE FROM fb_llm_models WHERE id=?`,
		MySQL:    `DELETE FROM fb_llm_models WHERE id=?`,
		Postgres: `DELETE FROM fb_llm_models WHERE id=$1`,
	}
	// llmModelSetSubagentAllowedSQL flips only the authorization switch (and
	// the row's updated_at), in all three dialects: config_store.go is not on
	// schema_migrations.go's dialect allowlist, so every statement here needs
	// explicit arms.
	llmModelSetSubagentAllowedSQL = dbx.SQL{
		SQLite:   `UPDATE fb_llm_models SET subagent_allowed=?, updated_at=? WHERE id=?`,
		MySQL:    `UPDATE fb_llm_models SET subagent_allowed=?, updated_at=? WHERE id=?`,
		Postgres: `UPDATE fb_llm_models SET subagent_allowed=$1, updated_at=$2 WHERE id=$3`,
	}
)

func (s *LLMModelStore) ok() bool { return s != nil && s.DB != nil }

// List returns every configuration, oldest first, with id breaking ties.
func (s *LLMModelStore) List(ctx context.Context) ([]LLMModel, error) {
	if !s.ok() {
		return nil, fmt.Errorf("model store unavailable")
	}
	rows, err := s.DB.QueryContext(ctx, llmModelListSQL.For(dbx.DialectOf(s.DB)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMModel
	for rows.Next() {
		m, err := scanLLMModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Get returns one configuration by id.
func (s *LLMModelStore) Get(ctx context.Context, id string) (LLMModel, error) {
	if !s.ok() {
		return LLMModel{}, fmt.Errorf("model store unavailable")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return LLMModel{}, ErrLLMModelNotFound
	}
	m, err := scanLLMModel(s.DB.QueryRowContext(ctx, llmModelGetSQL.For(dbx.DialectOf(s.DB)), id))
	if errors.Is(err, sql.ErrNoRows) {
		return LLMModel{}, ErrLLMModelNotFound
	}
	if err != nil {
		return LLMModel{}, err
	}
	return m, nil
}

// FindByIdentity returns the row with this normalized identity, if any. The
// values are normalized here, so a caller may pass what a person typed.
func (s *LLMModelStore) FindByIdentity(ctx context.Context, provider, model, baseURL, apiPath string) (LLMModel, bool, error) {
	if !s.ok() {
		return LLMModel{}, false, fmt.Errorf("model store unavailable")
	}
	candidate := LLMModel{Provider: provider, Model: model, BaseURL: baseURL, APIPath: apiPath}
	NormalizeLLMModelIdentity(&candidate)
	m, err := scanLLMModel(s.DB.QueryRowContext(ctx, llmModelFindIdentityPlaceholders.For(dbx.DialectOf(s.DB)),
		candidate.Provider, candidate.Model, candidate.BaseURL, candidate.APIPath))
	if errors.Is(err, sql.ErrNoRows) {
		return LLMModel{}, false, nil
	}
	if err != nil {
		return LLMModel{}, false, err
	}
	return m, true, nil
}

// Create stores a new configuration, filling its id and timestamps. The
// duplicate check is the normal path; the unique index is the race-free
// authority behind it, and a concurrent insert is translated into the same
// ErrLLMModelExists.
func (s *LLMModelStore) Create(ctx context.Context, m LLMModel) (LLMModel, error) {
	if !s.ok() {
		return LLMModel{}, fmt.Errorf("model store unavailable")
	}
	NormalizeLLMModelIdentity(&m)
	if m.Provider == "" || m.Model == "" {
		return LLMModel{}, fmt.Errorf("a model configuration requires a provider and a model")
	}
	if hit, found, err := s.FindByIdentity(ctx, m.Provider, m.Model, m.BaseURL, m.APIPath); err != nil {
		return LLMModel{}, err
	} else if found {
		return LLMModel{}, ErrLLMModelExists{ID: hit.ID}
	}
	m.ID = strings.TrimSpace(m.ID)
	if m.ID == "" {
		m.ID = NewID("llm")
	}
	now := time.Now().UnixMilli()
	m.CreatedAt, m.UpdatedAt = now, now
	if _, err := dbx.Exec(ctx, s.DB, llmModelInsertSQL.For(dbx.DialectOf(s.DB)), llmModelInsertArgs(m)...); err != nil {
		if dbx.IsUniqueViolation(err) {
			if hit, found, cerr := s.FindByIdentity(ctx, m.Provider, m.Model, m.BaseURL, m.APIPath); cerr == nil && found {
				return LLMModel{}, ErrLLMModelExists{ID: hit.ID}
			}
		}
		return LLMModel{}, err
	}
	return m, nil
}

// Update rewrites the row named by m.ID, keeping its creation time. Changing
// the identity onto another row's is refused the same way Create refuses a
// duplicate.
func (s *LLMModelStore) Update(ctx context.Context, m LLMModel) (LLMModel, error) {
	if !s.ok() {
		return LLMModel{}, fmt.Errorf("model store unavailable")
	}
	id := strings.TrimSpace(m.ID)
	existing, err := s.Get(ctx, id)
	if err != nil {
		return LLMModel{}, err
	}
	NormalizeLLMModelIdentity(&m)
	m.ID = id
	if m.Provider == "" || m.Model == "" {
		return LLMModel{}, fmt.Errorf("a model configuration requires a provider and a model")
	}
	if hit, found, ferr := s.FindByIdentity(ctx, m.Provider, m.Model, m.BaseURL, m.APIPath); ferr != nil {
		return LLMModel{}, ferr
	} else if found && hit.ID != m.ID {
		return LLMModel{}, ErrLLMModelExists{ID: hit.ID}
	}
	m.CreatedAt = existing.CreatedAt
	m.UpdatedAt = time.Now().UnixMilli()
	if _, err := dbx.Exec(ctx, s.DB, llmModelUpdateSQL.For(dbx.DialectOf(s.DB)),
		llmModelUpdateArgs(m)...); err != nil {
		if dbx.IsUniqueViolation(err) {
			if hit, found, cerr := s.FindByIdentity(ctx, m.Provider, m.Model, m.BaseURL, m.APIPath); cerr == nil && found {
				return LLMModel{}, ErrLLMModelExists{ID: hit.ID}
			}
		}
		return LLMModel{}, err
	}
	return m, nil
}

// Delete removes the row, reporting ErrLLMModelNotFound when it is not there.
func (s *LLMModelStore) Delete(ctx context.Context, id string) error {
	if !s.ok() {
		return fmt.Errorf("model store unavailable")
	}
	id = strings.TrimSpace(id)
	res, err := dbx.Exec(ctx, s.DB, llmModelDeleteSQL.For(dbx.DialectOf(s.DB)), id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrLLMModelNotFound
	}
	return nil
}

// SetSubagentAllowed flips only the authorization switch (owner 2026-11-10),
// touching updated_at so the configuration fingerprint sees the change; every
// other column keeps its stored value. A row that is not there reports
// ErrLLMModelNotFound.
func (s *LLMModelStore) SetSubagentAllowed(ctx context.Context, id string, allowed bool) error {
	if !s.ok() {
		return fmt.Errorf("model store unavailable")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrLLMModelNotFound
	}
	res, err := dbx.Exec(ctx, s.DB, llmModelSetSubagentAllowedSQL.For(dbx.DialectOf(s.DB)),
		boolToInt(allowed), time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrLLMModelNotFound
	}
	return nil
}

func llmModelInsertArgs(m LLMModel) []any {
	return []any{
		m.ID, m.Provider, m.Model, m.BaseURL, m.APIPath, m.APIKey, m.ParamsJSON,
		boolPtrToInt(m.CapImages), boolPtrToInt(m.CapPDF), boolPtrToInt(m.CapVideo),
		boolPtrToInt(m.CapAudio), boolPtrToInt(m.CapReasoning), boolPtrToInt(m.CapReasoningOff),
		llmModelEffortsJSON(m.CapEfforts), m.CapDefaultEffort,
		int64PtrToNull(m.CapContextWindow), int64PtrToNull(m.CapMaxOutput),
		m.Source, m.SourcePlan, boolToInt(m.URLVerbatim), boolToInt(m.SubagentAllowed),
		m.CreatedAt, m.UpdatedAt,
	}
}

func llmModelUpdateArgs(m LLMModel) []any {
	return []any{
		m.Provider, m.Model, m.BaseURL, m.APIPath, m.APIKey, m.ParamsJSON,
		boolPtrToInt(m.CapImages), boolPtrToInt(m.CapPDF), boolPtrToInt(m.CapVideo),
		boolPtrToInt(m.CapAudio), boolPtrToInt(m.CapReasoning), boolPtrToInt(m.CapReasoningOff),
		llmModelEffortsJSON(m.CapEfforts), m.CapDefaultEffort,
		int64PtrToNull(m.CapContextWindow), int64PtrToNull(m.CapMaxOutput),
		m.Source, m.SourcePlan, boolToInt(m.URLVerbatim), boolToInt(m.SubagentAllowed),
		m.UpdatedAt, m.ID,
	}
}

func llmModelEffortsJSON(efforts []string) any {
	if efforts == nil {
		return ""
	}
	raw, err := json.Marshal(efforts)
	if err != nil {
		// Only strings reach the encoder; a failure is impossible and the
		// catalog's default is the safe answer.
		return ""
	}
	return string(raw)
}

func scanLLMModel(scanner interface{ Scan(...any) error }) (LLMModel, error) {
	var (
		m                                                  LLMModel
		images, pdf, video, audio, reasoning, reasoningOff sql.NullInt64
		effortsJSON                                        string
		contextWindow, maxOutput                           sql.NullInt64
		urlVerbatim                                        int
		subagentAllowed                                    int
	)
	if err := scanner.Scan(
		&m.ID, &m.Provider, &m.Model, &m.BaseURL, &m.APIPath, &m.APIKey, &m.ParamsJSON,
		&images, &pdf, &video, &audio, &reasoning, &reasoningOff,
		&effortsJSON, &m.CapDefaultEffort, &contextWindow, &maxOutput,
		&m.Source, &m.SourcePlan, &urlVerbatim, &subagentAllowed, &m.CreatedAt, &m.UpdatedAt,
	); err != nil {
		return LLMModel{}, err
	}
	m.CapImages = boolFromNull(images)
	m.CapPDF = boolFromNull(pdf)
	m.CapVideo = boolFromNull(video)
	m.CapAudio = boolFromNull(audio)
	m.CapReasoning = boolFromNull(reasoning)
	m.CapReasoningOff = boolFromNull(reasoningOff)
	m.CapContextWindow = int64FromNull(contextWindow)
	m.CapMaxOutput = int64FromNull(maxOutput)
	m.URLVerbatim = urlVerbatim != 0
	m.SubagentAllowed = subagentAllowed != 0
	if strings.TrimSpace(effortsJSON) != "" {
		if err := json.Unmarshal([]byte(effortsJSON), &m.CapEfforts); err != nil {
			return LLMModel{}, fmt.Errorf("decode model reasoning levels: %w", err)
		}
		if m.CapEfforts == nil {
			m.CapEfforts = []string{}
		}
	}
	return m, nil
}

func boolFromNull(v sql.NullInt64) *bool {
	if !v.Valid {
		return nil
	}
	b := v.Int64 != 0
	return &b
}

func int64FromNull(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

// The operator's variables, the other sibling table of fb_config: the ${NAME}
// values MCP and LSP server entries resolve (plan 259, replacing the home .env).
//
// EnvVar is one operator-owned variable. MCP and LSP server entries reference
// these as ${NAME}; the table is the only source for a project-scoped entry,
// so a repository cannot read the host's environment (owner 2026-11-07).
type EnvVar struct {
	Name        string
	Value       string
	UpdatedAtMs int64
}

// ErrEnvVarName reports a name that is not a usable ${NAME} reference.
var ErrEnvVarName = errors.New("variable name must match [A-Za-z_][A-Za-z0-9_]*")

// envVarNamePattern is the same character set the LSP's envRefPattern accepts,
// so a name stored here is one a server entry can reference.
var envVarNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// EnvVarStore reads and writes the operator's variables.
type EnvVarStore struct{ DB *sql.DB }

var (
	envVarListSQL = dbx.SQL{
		SQLite:   `SELECT name, value, updated_at_ms FROM fb_env_vars ORDER BY name`,
		MySQL:    `SELECT name, value, updated_at_ms FROM fb_env_vars ORDER BY name`,
		Postgres: `SELECT name, value, updated_at_ms FROM fb_env_vars ORDER BY name`,
	}
	envVarSetSQL = dbx.SQL{
		SQLite: `INSERT INTO fb_env_vars(name, value, updated_at_ms) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET value=excluded.value, updated_at_ms=excluded.updated_at_ms`,
		MySQL: `INSERT INTO fb_env_vars(name, value, updated_at_ms) VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE value=VALUES(value), updated_at_ms=VALUES(updated_at_ms)`,
		Postgres: `INSERT INTO fb_env_vars(name, value, updated_at_ms) VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET value=excluded.value, updated_at_ms=excluded.updated_at_ms`,
	}
	envVarDeleteSQL = dbx.SQL{
		SQLite:   `DELETE FROM fb_env_vars WHERE name=?`,
		MySQL:    `DELETE FROM fb_env_vars WHERE name=?`,
		Postgres: `DELETE FROM fb_env_vars WHERE name=$1`,
	}
)

func (s *EnvVarStore) ok() bool { return s != nil && s.DB != nil }

// List returns every variable, ordered by name.
func (s *EnvVarStore) List(ctx context.Context) ([]EnvVar, error) {
	if !s.ok() {
		return nil, fmt.Errorf("variable store unavailable")
	}
	rows, err := s.DB.QueryContext(ctx, envVarListSQL.For(dbx.DialectOf(s.DB)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EnvVar
	for rows.Next() {
		var v EnvVar
		if err := rows.Scan(&v.Name, &v.Value, &v.UpdatedAtMs); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Map returns every variable as name → value.
func (s *EnvVarStore) Map(ctx context.Context) (map[string]string, error) {
	list, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(list))
	for _, v := range list {
		out[v.Name] = v.Value
	}
	return out, nil
}

// Set stores one variable, overwriting the value a previous Set wrote. Only a
// name matching the ${NAME} character set is accepted.
func (s *EnvVarStore) Set(ctx context.Context, name, value string) error {
	if !s.ok() {
		return fmt.Errorf("variable store unavailable")
	}
	name = strings.TrimSpace(name)
	if !envVarNamePattern.MatchString(name) {
		return ErrEnvVarName
	}
	_, err := dbx.Exec(ctx, s.DB, envVarSetSQL.For(dbx.DialectOf(s.DB)),
		name, value, time.Now().UnixMilli())
	return err
}

// Delete removes one variable; a name that is not there is not an error.
func (s *EnvVarStore) Delete(ctx context.Context, name string) error {
	if !s.ok() {
		return fmt.Errorf("variable store unavailable")
	}
	_, err := dbx.Exec(ctx, s.DB, envVarDeleteSQL.For(dbx.DialectOf(s.DB)), strings.TrimSpace(name))
	return err
}
