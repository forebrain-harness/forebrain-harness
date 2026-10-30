package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
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
	rows, err := s.DB.QueryContext(ctx, `SELECT section, value_json FROM fb_config`)
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
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var cur []byte
	switch err := tx.QueryRowContext(ctx, `SELECT value_json FROM fb_config WHERE section = ?`, section).Scan(&cur); {
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
		if _, err := tx.ExecContext(ctx, `DELETE FROM fb_config WHERE section = ?`, section); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO fb_config (section, value_json, updated_at_ms) VALUES (?,?,?)
ON CONFLICT(section) DO UPDATE SET value_json = excluded.value_json, updated_at_ms = excluded.updated_at_ms`,
			section, string(next), time.Now().UnixMilli()); err != nil {
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
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM fb_config`); err != nil {
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO fb_config (section, value_json, updated_at_ms) VALUES (?,?,?)`,
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
	switch err := s.DB.QueryRowContext(ctx, `SELECT value_json FROM fb_config WHERE section = ?`, strings.TrimSpace(section)).Scan(&value); {
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
		_, err := s.DB.ExecContext(ctx, `DELETE FROM fb_config WHERE section = ?`, section)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO fb_config (section, value_json, updated_at_ms) VALUES (?,?,?)
ON CONFLICT(section) DO UPDATE SET value_json = excluded.value_json, updated_at_ms = excluded.updated_at_ms`,
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
	switch err := s.DB.QueryRowContext(ctx,
		`SELECT value_json FROM fb_mcp_oauth WHERE project_key = ? AND server_name = ?`,
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
		_, err := s.DB.ExecContext(ctx, `DELETE FROM fb_mcp_oauth WHERE project_key = ? AND server_name = ?`, projectKey, serverName)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO fb_mcp_oauth (project_key, server_name, value_json, updated_at_ms) VALUES (?,?,?,?)
ON CONFLICT(project_key, server_name) DO UPDATE SET value_json = excluded.value_json, updated_at_ms = excluded.updated_at_ms`,
		projectKey, serverName, string(value), time.Now().UnixMilli())
	return err
}
