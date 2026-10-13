package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ProjectChannel is one delivery channel a project owns: the bot or webhook
// account that carries conversations in for the project and pushes their
// answers back out. ConfigJSON is the channel's ChannelsSection, stored as the
// JSON the engine renders and reads back; a secret field inside it holds a
// ${...} reference, never the credential itself.
type ProjectChannel struct {
	ProjectID  string
	Kind       string
	Enabled    bool
	ConfigJSON string
	// AccountKey is the account's identity (a bot token's hash, an app id).
	// One account belongs to exactly one project, which is what this store's
	// uniqueness enforces. Empty means the channel has no account yet.
	AccountKey string
	CreatedAt  int64
	UpdatedAt  int64
}

// ErrChannelAccountTaken reports that a channel account is already bound to a
// project, whose id it carries so a surface can name it to the person.
type ErrChannelAccountTaken struct {
	Project string
}

func (e ErrChannelAccountTaken) Error() string {
	if p := strings.TrimSpace(e.Project); p != "" {
		return fmt.Sprintf("channel account already belongs to project %s", p)
	}
	return "channel account already belongs to another project"
}

// ProjectChannelStore reads and writes the per-project channels of one primary
// agent. Channels are project data, so every read is scoped to a project the
// agent owns.
type ProjectChannelStore struct {
	db      *sql.DB
	agentID string
}

// NewProjectChannelStore opens the project channel store for one primary agent.
func NewProjectChannelStore(db *sql.DB, agentID string) *ProjectChannelStore {
	if db == nil {
		return nil
	}
	return &ProjectChannelStore{db: db, agentID: strings.TrimSpace(agentID)}
}

func (s *ProjectChannelStore) AgentID() string {
	if s == nil {
		return ""
	}
	return s.agentID
}

func (s *ProjectChannelStore) ok() bool { return s != nil && s.db != nil }

const projectChannelColumns = `project_id, kind, enabled, config_json, account_key, created_at, updated_at`

func scanProjectChannel(scanner interface{ Scan(...any) error }) (ProjectChannel, error) {
	var ch ProjectChannel
	var enabled int
	if err := scanner.Scan(&ch.ProjectID, &ch.Kind, &enabled, &ch.ConfigJSON, &ch.AccountKey, &ch.CreatedAt, &ch.UpdatedAt); err != nil {
		return ProjectChannel{}, err
	}
	ch.Enabled = enabled != 0
	return ch, nil
}

// ownsProject reports whether this agent owns the project, so a channel id
// learned elsewhere cannot read or change another agent's project.
func (s *ProjectChannelStore) ownsProject(ctx context.Context, projectID string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_projects WHERE id=? AND agent_id=?`,
		strings.TrimSpace(projectID), s.agentID).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// List returns every channel configured for one project, enabled or not.
func (s *ProjectChannelStore) List(ctx context.Context, projectID string) ([]ProjectChannel, error) {
	if !s.ok() {
		return nil, fmt.Errorf("project channel store unavailable")
	}
	projectID = strings.TrimSpace(projectID)
	owned, err := s.ownsProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, ErrProjectNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+projectChannelColumns+` FROM fb_project_channels WHERE project_id=? ORDER BY kind`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectChannel
	for rows.Next() {
		ch, err := scanProjectChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// ListEnabledForAgent returns every enabled channel of the agent's projects,
// which is the set the gateway binds. The query joins the project table so the
// tenant boundary is the project's owner.
func (s *ProjectChannelStore) ListEnabledForAgent(ctx context.Context) ([]ProjectChannel, error) {
	if !s.ok() {
		return nil, fmt.Errorf("project channel store unavailable")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT pc.project_id, pc.kind, pc.enabled, pc.config_json, pc.account_key, pc.created_at, pc.updated_at
FROM fb_project_channels pc
JOIN fb_projects p ON p.id = pc.project_id
WHERE p.agent_id = ? AND pc.enabled = 1
ORDER BY pc.project_id, pc.kind`, s.agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectChannel
	for rows.Next() {
		ch, err := scanProjectChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// Get returns one project's channel, reporting whether it exists.
func (s *ProjectChannelStore) Get(ctx context.Context, projectID, kind string) (ProjectChannel, bool, error) {
	if !s.ok() {
		return ProjectChannel{}, false, fmt.Errorf("project channel store unavailable")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+projectChannelColumns+` FROM fb_project_channels WHERE project_id=? AND kind=?`,
		strings.TrimSpace(projectID), strings.TrimSpace(kind))
	if err != nil {
		return ProjectChannel{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return ProjectChannel{}, false, rows.Err()
	}
	ch, err := scanProjectChannel(rows)
	if err != nil {
		return ProjectChannel{}, false, err
	}
	return ch, true, nil
}

// Upsert writes one project's channel. It refuses an account_key already bound
// to another project with ErrChannelAccountTaken, which is what keeps one bot
// account out of two projects.
func (s *ProjectChannelStore) Upsert(ctx context.Context, ch ProjectChannel) error {
	if !s.ok() {
		return fmt.Errorf("project channel store unavailable")
	}
	projectID := strings.TrimSpace(ch.ProjectID)
	kind := strings.TrimSpace(ch.Kind)
	if projectID == "" || kind == "" {
		return fmt.Errorf("project channel requires a project and a kind")
	}
	owned, err := s.ownsProject(ctx, projectID)
	if err != nil {
		return err
	}
	if !owned {
		return ErrProjectNotFound
	}
	key := strings.TrimSpace(ch.AccountKey)
	if key != "" {
		var owner string
		err := s.db.QueryRowContext(ctx,
			`SELECT project_id FROM fb_project_channels WHERE account_key=? AND project_id<>?`,
			key, projectID).Scan(&owner)
		switch {
		case err == nil:
			return ErrChannelAccountTaken{Project: owner}
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
	}
	now := time.Now().Unix()
	if ch.CreatedAt <= 0 {
		ch.CreatedAt = now
	}
	configJSON := strings.TrimSpace(ch.ConfigJSON)
	if configJSON == "" {
		configJSON = "{}"
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO fb_project_channels (project_id, kind, enabled, config_json, account_key, created_at, updated_at)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT(project_id, kind) DO UPDATE SET
  enabled=excluded.enabled, config_json=excluded.config_json, account_key=excluded.account_key, updated_at=excluded.updated_at`,
		projectID, kind, boolToInt(ch.Enabled), configJSON, key, ch.CreatedAt, now)
	if err != nil {
		// The unique index is the race-free authority; a concurrent insert of
		// the same account between the check above and this write is refused
		// here rather than surfacing as a constraint error.
		if strings.Contains(err.Error(), "idx_fb_project_channels_account") {
			return ErrChannelAccountTaken{}
		}
		return err
	}
	return nil
}

// Delete removes one project's channel.
func (s *ProjectChannelStore) Delete(ctx context.Context, projectID, kind string) error {
	if !s.ok() {
		return fmt.Errorf("project channel store unavailable")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM fb_project_channels WHERE project_id=? AND kind=?`,
		strings.TrimSpace(projectID), strings.TrimSpace(kind))
	return err
}
