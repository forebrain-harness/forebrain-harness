// Project storage: the fb_projects table and its read/write surface.
//
// A project binds a working directory to the settings that travel with it.
// The primary agent is the tenant (agent_id), exactly like sessions; the
// project key is the memory identity of the bound root, so a project's
// memories and its ProjectKey agree by construction. Archiving is a soft
// delete — rows stay and lists hide them — and deletion removes the project
// and unbinds its sessions without touching their transcripts.
package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Memory scopes for a project.
const (
	ProjectMemoryShared      = "shared"
	ProjectMemoryProjectOnly = "project_only"
)

// Project is one row of fb_projects.
type Project struct {
	ID             string
	AgentID        string
	Name           string
	Icon           string
	Description    string
	Instructions   string
	Root           string
	ProjectKey     string
	MemoryScope    string
	ResourceAccess bool
	Pinned         bool
	// ArchivedAt is nil while the project is live; archiving is a soft delete.
	ArchivedAt *int64
	CreatedAt  int64
	UpdatedAt  int64
}

// ErrProjectNotFound reports a missing or cross-tenant project id.
var ErrProjectNotFound = errors.New("project not found")

// NormalizeProjectMemoryScope canonicalizes a stored scope value.
func NormalizeProjectMemoryScope(scope string) string {
	if strings.EqualFold(strings.TrimSpace(scope), ProjectMemoryProjectOnly) {
		return ProjectMemoryProjectOnly
	}
	return ProjectMemoryShared
}

// ProjectStore reads and writes fb_projects for one primary agent.
type ProjectStore struct {
	db      *sql.DB
	agentID string
}

// NewProjectStore opens the project store on behalf of one primary agent.
func NewProjectStore(db *sql.DB, agentID string) *ProjectStore {
	if db == nil {
		return nil
	}
	return &ProjectStore{db: db, agentID: strings.TrimSpace(agentID)}
}

func (s *ProjectStore) AgentID() string {
	if s == nil {
		return ""
	}
	return s.agentID
}

const projectColumns = `id, agent_id, name, icon, description, instructions, root, project_key, memory_scope, resource_access, pinned, archived_at, created_at, updated_at`

func scanProject(scanner interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var resourceAccess int
	var pinned int
	var archivedAt sql.NullInt64
	if err := scanner.Scan(&p.ID, &p.AgentID, &p.Name, &p.Icon, &p.Description, &p.Instructions, &p.Root, &p.ProjectKey, &p.MemoryScope, &resourceAccess, &pinned, &archivedAt, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return Project{}, err
	}
	p.ResourceAccess = resourceAccess != 0
	p.Pinned = pinned != 0
	if archivedAt.Valid {
		at := archivedAt.Int64
		p.ArchivedAt = &at
	}
	p.MemoryScope = NormalizeProjectMemoryScope(p.MemoryScope)
	return p, nil
}

// CreateProjectInput is the writable shape of a project.
type CreateProjectInput struct {
	Name           string
	Icon           string
	Description    string
	Instructions   string
	Root           string
	ProjectKey     string
	MemoryScope    string
	ResourceAccess bool
}

// Create inserts a project for this agent.
func (s *ProjectStore) Create(ctx context.Context, in CreateProjectInput) (Project, error) {
	if s == nil || s.db == nil {
		return Project{}, fmt.Errorf("project store unavailable")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return Project{}, fmt.Errorf("project name is required")
	}
	root := strings.TrimSpace(in.Root)
	if root == "" {
		return Project{}, fmt.Errorf("project root is required")
	}
	if strings.TrimSpace(s.agentID) == "" {
		return Project{}, fmt.Errorf("project store has no agent")
	}
	now := time.Now().Unix()
	p := Project{
		ID:             "prj_" + NewID("project"),
		AgentID:        s.agentID,
		Name:           name,
		Icon:           strings.TrimSpace(in.Icon),
		Description:    strings.TrimSpace(in.Description),
		Instructions:   strings.TrimSpace(in.Instructions),
		Root:           root,
		ProjectKey:     strings.TrimSpace(in.ProjectKey),
		MemoryScope:    NormalizeProjectMemoryScope(in.MemoryScope),
		ResourceAccess: in.ResourceAccess,
		Pinned:         false,
		ArchivedAt:     nil,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO fb_projects(`+projectColumns+`)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.AgentID, p.Name, p.Icon, p.Description, p.Instructions, p.Root, p.ProjectKey, p.MemoryScope, boolInt(p.ResourceAccess), boolInt(p.Pinned), p.ArchivedAt, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		return Project{}, err
	}
	return p, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Get returns one project by id. Another agent's project is not found, the
// same boundary the session store draws.
func (s *ProjectStore) Get(ctx context.Context, id string) (Project, error) {
	if s == nil || s.db == nil {
		return Project{}, ErrProjectNotFound
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM fb_projects WHERE id=? AND agent_id=?`, strings.TrimSpace(id), s.agentID)
	p, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrProjectNotFound
	}
	return p, err
}

// GetByRoot returns this agent's project bound to root (exact path), if any.
func (s *ProjectStore) GetByRoot(ctx context.Context, root string) (Project, bool, error) {
	if s == nil || s.db == nil {
		return Project{}, false, nil
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM fb_projects WHERE agent_id=? AND root=? ORDER BY archived_at ASC, updated_at DESC LIMIT 1`, s.agentID, strings.TrimSpace(root))
	p, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, false, nil
	}
	if err != nil {
		return Project{}, false, err
	}
	return p, true, nil
}

// UpdateProjectInput carries only the editable fields.
type UpdateProjectInput struct {
	Name           *string
	Icon           *string
	Description    *string
	Instructions   *string
	MemoryScope    *string
	ResourceAccess *bool
}

// Update applies the set fields and touches updated_at.
func (s *ProjectStore) Update(ctx context.Context, id string, in UpdateProjectInput) (Project, error) {
	if s == nil || s.db == nil {
		return Project{}, ErrProjectNotFound
	}
	current, err := s.Get(ctx, id)
	if err != nil {
		return Project{}, err
	}
	if in.Name != nil {
		if strings.TrimSpace(*in.Name) == "" {
			return Project{}, fmt.Errorf("project name is required")
		}
		current.Name = strings.TrimSpace(*in.Name)
	}
	if in.Icon != nil {
		current.Icon = strings.TrimSpace(*in.Icon)
	}
	if in.Description != nil {
		current.Description = strings.TrimSpace(*in.Description)
	}
	if in.Instructions != nil {
		current.Instructions = strings.TrimSpace(*in.Instructions)
	}
	if in.MemoryScope != nil {
		current.MemoryScope = NormalizeProjectMemoryScope(*in.MemoryScope)
	}
	if in.ResourceAccess != nil {
		current.ResourceAccess = *in.ResourceAccess
	}
	current.UpdatedAt = time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `UPDATE fb_projects SET name=?, icon=?, description=?, instructions=?, memory_scope=?, resource_access=?, updated_at=? WHERE id=? AND agent_id=?`,
		current.Name, current.Icon, current.Description, current.Instructions, current.MemoryScope, boolInt(current.ResourceAccess), current.UpdatedAt, strings.TrimSpace(id), s.agentID)
	if err != nil {
		return Project{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Project{}, ErrProjectNotFound
	}
	return current, nil
}

// SetPinned pins or unpins a project.
func (s *ProjectStore) SetPinned(ctx context.Context, id string, pinned bool) error {
	if s == nil || s.db == nil {
		return ErrProjectNotFound
	}
	res, err := s.db.ExecContext(ctx, `UPDATE fb_projects SET pinned=?, updated_at=? WHERE id=? AND agent_id=?`, boolInt(pinned), time.Now().Unix(), strings.TrimSpace(id), s.agentID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// SetArchived soft-deletes (at > 0) or restores (at == 0) a project. Rows are
// never removed here: an archived project's sessions and files stay reachable.
func (s *ProjectStore) SetArchived(ctx context.Context, id string, at int64) error {
	if s == nil || s.db == nil {
		return ErrProjectNotFound
	}
	res, err := s.db.ExecContext(ctx, `UPDATE fb_projects SET archived_at=NULLIF(?,0), updated_at=? WHERE id=? AND agent_id=?`, at, time.Now().Unix(), strings.TrimSpace(id), s.agentID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// Delete removes the project row and unbinds its sessions (their transcripts
// stay; they simply no longer belong to a project).
func (s *ProjectStore) Delete(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return ErrProjectNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	id = strings.TrimSpace(id)
	res, err := tx.ExecContext(ctx, `DELETE FROM fb_projects WHERE id=? AND agent_id=?`, id, s.agentID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrProjectNotFound
	}
	// Unbinding the project's sessions is fb_sessions.project_id's ON DELETE
	// SET NULL; no statement here needs to remember it.
	return tx.Commit()
}

// ListProjectsOptions controls ListProjects.
type ListProjectsOptions struct {
	// Search filters by substring over name and root (case-insensitive).
	Search string
	// Sort is "updated" (default, newest first) or "name" (ascending).
	Sort string
	// IncludeArchived adds archived projects to the result.
	IncludeArchived bool
}

// ListProjects returns the agent's projects. Pinned ones lead within the
// chosen sort; the list pages rather than loading everything.
func (s *ProjectStore) ListProjects(ctx context.Context, opts ListProjectsOptions, limit, offset int) ([]Project, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	where := "agent_id=?"
	args := []any{s.agentID}
	if !opts.IncludeArchived {
		where += " AND archived_at IS NULL"
	}
	if q := strings.TrimSpace(opts.Search); q != "" {
		where += " AND (name LIKE ? OR root LIKE ?)"
		like := "%" + q + "%"
		args = append(args, like, like)
	}
	order := "pinned DESC, updated_at DESC, id"
	if strings.EqualFold(strings.TrimSpace(opts.Sort), "name") {
		order = "pinned DESC, name COLLATE NOCASE ASC, id"
	}
	query := `SELECT ` + projectColumns + ` FROM fb_projects WHERE ` + where + ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// BindSession records which project a session belongs to. The session store
// writes this when the session is created; it lives here so the ownership
// rules (same agent) are stated once.
func (s *ProjectStore) BindSession(ctx context.Context, sessionID, projectID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	projectID = strings.TrimSpace(projectID)
	if sessionID == "" {
		return nil
	}
	if projectID != "" {
		if _, err := s.Get(ctx, projectID); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE fb_sessions SET project_id=NULLIF(?,'') WHERE id=? AND agent_id=?`, projectID, sessionID, s.agentID)
	return err
}

// ProjectForSession returns the project a session is bound to, if any.
func (s *ProjectStore) ProjectForSession(ctx context.Context, sessionID string) (Project, bool, error) {
	if s == nil || s.db == nil {
		return Project{}, false, nil
	}
	var projectID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT project_id FROM fb_sessions WHERE id=? AND agent_id=?`, strings.TrimSpace(sessionID), s.agentID).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, false, nil
	}
	if err != nil {
		return Project{}, false, err
	}
	if !projectID.Valid || strings.TrimSpace(projectID.String) == "" {
		return Project{}, false, nil
	}
	p, err := s.Get(ctx, projectID.String)
	if err != nil {
		return Project{}, false, nil
	}
	return p, true, nil
}
