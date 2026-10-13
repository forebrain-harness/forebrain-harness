// Project storage: the fb_projects table and its read/write surface.
//
// A project binds a working directory to the settings that travel with it.
// The primary agent is the tenant (agent_id), exactly like sessions; every
// ordinary session belongs to a project (the sole exception is a
// primary-agent-level skill-workshop task). The project key is the memory
// identity of the bound root — the root itself, never a walk up to a git
// ancestor — so a project's memories and its ProjectKey agree by
// construction. Deleting a project deletes its conversations and cron jobs
// with it; the bound directory's files are not touched.
package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
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
	// LastAttachDir is the directory, relative to Root, the "save to" dialog
	// last uploaded into. Empty is the root itself.
	LastAttachDir string
	CreatedAt     int64
	UpdatedAt     int64
}

// ErrProjectNotFound reports a missing or cross-tenant project id.
var ErrProjectNotFound = errors.New("project not found")

// ErrProjectRootTaken reports that the agent already has a project bound to
// the requested root: one project per (agent, root). Create returns the
// existing row alongside it.
var ErrProjectRootTaken = errors.New("project root already registered")

// ErrProjectBusy reports that a project cannot be deleted while one of its
// conversations has a run in flight.
var ErrProjectBusy = errors.New("project has a run in flight")

// NormalizeProjectMemoryScope canonicalizes a stored scope value.
func NormalizeProjectMemoryScope(scope string) string {
	if strings.EqualFold(strings.TrimSpace(scope), ProjectMemoryProjectOnly) {
		return ProjectMemoryProjectOnly
	}
	return ProjectMemoryShared
}

// canonicalStatePath resolves a path to the single spelling the state store
// stores and compares it by: absolute, cleaned, and with symlinks resolved
// when the path exists. It mirrors safety.CanonicalPath, which pkg/state
// cannot call — the architecture layer table forbids a layer-1 package
// importing a layer-2 one — so the rule is stated here as well.
func canonicalStatePath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty project path")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && strings.TrimSpace(resolved) != "" {
		return filepath.Clean(resolved), nil
	}
	return abs, nil
}

// ProjectKeyForRoot is a project's memory and plan-file identity: the project
// root itself, canonical, with separators and colons turned into dashes. It
// never walks up to a git root — a subdirectory is its own project (owner
// 2026-10-11).
func ProjectKeyForRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	canonical, err := canonicalStatePath(root)
	if err != nil {
		canonical = filepath.Clean(root)
	}
	return denseProjectKey(canonical)
}

// denseProjectKey maps a path onto its dash-encoded key, the same transform
// memory.ProjectKey applies to a resolved root.
func denseProjectKey(path string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':':
			return '-'
		default:
			return r
		}
	}, filepath.ToSlash(path))
}

// canonicalProjectRoot resolves a caller's root to the single spelling every
// other part of the process stores and compares it by.
func canonicalProjectRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("project root is required")
	}
	canonical, err := canonicalStatePath(root)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(canonical) == "" {
		return "", fmt.Errorf("project root is required")
	}
	return canonical, nil
}

// projectNameForRoot derives a project's default name from its root: the last
// path segment, or the root itself when there is none (the filesystem root).
func projectNameForRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	base := filepath.Base(root)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return root
	}
	return base
}

// The validation errors a bound root can be refused with. They are the exact
// strings the project API has always answered with, so a client that matched
// on the text still does; the sentinels exist so a surface that shows its own
// wording (the interactive launch, which speaks Chinese) can tell which
// refusal it is holding without matching on a message.
var (
	ErrProjectRootFilesystem = errors.New("root must not be the filesystem root")
	ErrProjectRootIsHome     = errors.New("root must not be the forebrain home directory")
	ErrProjectRootInsideHome = errors.New("root must not be inside the forebrain home directory")
)

// ValidateProjectRoot checks the bound directory is a real place a project can
// live: it exists, is a directory, is not the forebrain home, and is not the
// filesystem root.
//
// It returns the canonical path, because that is the spelling every other part
// of the process uses for the same directory: the trust store records it that
// way, the launch context a route resolves reports it that way, and so does
// the project key written into the very same row. Keeping the path as the
// caller typed it would put two names for one directory in one record — a
// client could not tell that a skill listed under the canonical path belongs
// to the project it is showing, and the same checkout could be bound twice
// under two spellings.
//
// Existence is therefore checked before anything is compared: only a directory
// that is there can be resolved, and a check that mixed a resolved path with
// an unresolved one would silently stop catching what it is there to catch.
func ValidateProjectRoot(home, candidate string) (string, error) {
	root := strings.TrimSpace(candidate)
	if root == "" {
		return "", fmt.Errorf("root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("root must be an absolute path")
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("root directory does not exist")
	}
	if !info.IsDir() {
		return "", fmt.Errorf("root must be a directory")
	}
	abs, err = canonicalStatePath(abs)
	if err != nil {
		return "", fmt.Errorf("root directory could not be resolved")
	}
	if abs == string(filepath.Separator) {
		return "", ErrProjectRootFilesystem
	}
	if trimmedHome := strings.TrimSpace(home); trimmedHome != "" {
		canonicalHome, err := canonicalStatePath(trimmedHome)
		if err != nil {
			return "", fmt.Errorf("the forebrain home directory could not be resolved")
		}
		if abs == canonicalHome {
			return "", ErrProjectRootIsHome
		}
		if strings.HasPrefix(abs, canonicalHome+string(filepath.Separator)) {
			return "", ErrProjectRootInsideHome
		}
	}
	return abs, nil
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

const projectColumns = `id, agent_id, name, icon, description, instructions, root, project_key, memory_scope, resource_access, pinned, last_attach_dir, created_at, updated_at`

const projectColumnCount = 14

func scanProject(scanner interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var resourceAccess int
	var pinned int
	if err := scanner.Scan(&p.ID, &p.AgentID, &p.Name, &p.Icon, &p.Description, &p.Instructions, &p.Root, &p.ProjectKey, &p.MemoryScope, &resourceAccess, &pinned, &p.LastAttachDir, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return Project{}, err
	}
	p.ResourceAccess = resourceAccess != 0
	p.Pinned = pinned != 0
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

// Create inserts a project for this agent. A root the agent has already
// registered is refused with ErrProjectRootTaken, carrying the existing row;
// one project per (agent, root) is what the unique index declares.
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
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO fb_projects(`+projectColumns+`)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.AgentID, p.Name, p.Icon, p.Description, p.Instructions, p.Root, p.ProjectKey, p.MemoryScope, boolInt(p.ResourceAccess), boolInt(p.Pinned), p.LastAttachDir, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		if isUniqueConstraint(err) {
			if existing, ok, getErr := s.GetByRoot(ctx, root); getErr == nil && ok {
				return existing, ErrProjectRootTaken
			}
		}
		return Project{}, err
	}
	return p, nil
}

// isUniqueConstraint reports whether err is SQLite's UNIQUE-constraint
// failure, the conflict the root index raises.
func isUniqueConstraint(err error) bool {
	var sqliteErr sqlite3.Error
	return errors.As(err, &sqliteErr) && sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique
}

// FindOrCreateByRoot returns this agent's project bound to root, creating one
// from defaults when none exists. root is canonicalized, and a defaults value
// with no ProjectKey takes the key derived from the root itself. The insert is
// a no-op when a concurrent writer registered the same root first, so two
// surfaces opening one directory converge on a single row.
func (s *ProjectStore) FindOrCreateByRoot(ctx context.Context, root string, defaults CreateProjectInput) (Project, bool, error) {
	if s == nil || s.db == nil {
		return Project{}, false, fmt.Errorf("project store unavailable")
	}
	canonical, err := canonicalProjectRoot(root)
	if err != nil {
		return Project{}, false, err
	}
	if existing, ok, err := s.GetByRoot(ctx, canonical); err != nil {
		return Project{}, false, err
	} else if ok {
		return existing, false, nil
	}
	name := strings.TrimSpace(defaults.Name)
	if name == "" {
		name = projectNameForRoot(canonical)
	}
	if name == "" {
		name = canonical
	}
	key := strings.TrimSpace(defaults.ProjectKey)
	if key == "" {
		key = ProjectKeyForRoot(canonical)
	}
	now := time.Now().Unix()
	fresh := Project{
		ID:             "prj_" + NewID("project"),
		AgentID:        s.agentID,
		Name:           name,
		Icon:           strings.TrimSpace(defaults.Icon),
		Description:    strings.TrimSpace(defaults.Description),
		Instructions:   strings.TrimSpace(defaults.Instructions),
		Root:           canonical,
		ProjectKey:     key,
		MemoryScope:    NormalizeProjectMemoryScope(defaults.MemoryScope),
		ResourceAccess: defaults.ResourceAccess,
		Pinned:         false,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO fb_projects(`+projectColumns+`)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(agent_id, root) DO NOTHING`,
		fresh.ID, fresh.AgentID, fresh.Name, fresh.Icon, fresh.Description, fresh.Instructions, fresh.Root, fresh.ProjectKey, fresh.MemoryScope, boolInt(fresh.ResourceAccess), boolInt(fresh.Pinned), fresh.LastAttachDir, fresh.CreatedAt, fresh.UpdatedAt); err != nil {
		return Project{}, false, err
	}
	stored, ok, err := s.GetByRoot(ctx, canonical)
	if err != nil {
		return Project{}, false, err
	}
	if !ok {
		return Project{}, false, fmt.Errorf("project for root %q was not stored", canonical)
	}
	return stored, stored.ID == fresh.ID, nil
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
// The root index makes at most one row match.
func (s *ProjectStore) GetByRoot(ctx context.Context, root string) (Project, bool, error) {
	if s == nil || s.db == nil {
		return Project{}, false, nil
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM fb_projects WHERE agent_id=? AND root=?`, s.agentID, strings.TrimSpace(root))
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

// SetLastAttachDir records the directory the project's "save to" dialog last
// uploaded into, relative to the root with '/' separators (empty is the root
// itself). It deliberately does not touch updated_at: remembering where a file
// was saved is a dialog convenience, not an edit that should reorder the
// projects list. A missing project is refused with ErrProjectNotFound.
func (s *ProjectStore) SetLastAttachDir(ctx context.Context, id, dir string) error {
	if s == nil || s.db == nil {
		return ErrProjectNotFound
	}
	normalized := strings.Trim(strings.ReplaceAll(strings.TrimSpace(dir), "\\", "/"), "/")
	res, err := s.db.ExecContext(ctx, `UPDATE fb_projects SET last_attach_dir=? WHERE id=? AND agent_id=?`, normalized, strings.TrimSpace(id), s.agentID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// Delete removes a project together with its conversations, their runs and
// their cron jobs; the bound directory's files are not touched. A project
// whose conversations have a run in flight is refused with ErrProjectBusy.
func (s *ProjectStore) Delete(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return ErrProjectNotFound
	}
	id = strings.TrimSpace(id)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var live int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM fb_runs r
JOIN fb_sessions s ON s.id = r.session_id
WHERE s.project_id = ? AND s.agent_id = ? AND r.status IN ('running', 'waiting_action')`,
		id, s.agentID).Scan(&live); err != nil {
		return err
	}
	if live > 0 {
		return ErrProjectBusy
	}
	// The fire history and the stage-1 memory jobs carry no foreign key to the
	// sessions and cron jobs they name, so they are cleared by hand before the
	// rows they refer to go. Everything else hangs off fb_sessions through a
	// foreign key and cascades when those rows are deleted (foreign keys are on
	// at runtime).
	if _, err := tx.ExecContext(ctx, `DELETE FROM fb_cron_runs WHERE session_id IN (SELECT id FROM fb_sessions WHERE project_id=? AND agent_id=?)`, id, s.agentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fb_memory_jobs WHERE kind='memory_stage1' AND job_key IN (SELECT id FROM fb_sessions WHERE project_id=? AND agent_id=?)`, id, s.agentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fb_cron_runs WHERE job_id IN (SELECT id FROM fb_cron_jobs WHERE project_id=? AND agent_id=?)`, id, s.agentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fb_cron_jobs WHERE project_id=? AND agent_id=?`, id, s.agentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fb_sessions WHERE project_id=? AND agent_id=?`, id, s.agentID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM fb_projects WHERE id=? AND agent_id=?`, id, s.agentID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrProjectNotFound
	}
	return tx.Commit()
}

// DeletePreview reports what Delete would remove: the project's own
// conversations (sub-sessions excluded, so the count matches the list the
// user sees), its cron jobs, and whether a run is in flight.
func (s *ProjectStore) DeletePreview(ctx context.Context, id string) (sessions int, cronJobs int, running bool, err error) {
	if s == nil || s.db == nil {
		return 0, 0, false, ErrProjectNotFound
	}
	id = strings.TrimSpace(id)
	if _, err = s.Get(ctx, id); err != nil {
		return 0, 0, false, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_sessions WHERE project_id=? AND agent_id=? AND parent_session_id IS NULL`, id, s.agentID).Scan(&sessions); err != nil {
		return 0, 0, false, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fb_cron_jobs WHERE project_id=? AND agent_id=?`, id, s.agentID).Scan(&cronJobs); err != nil {
		return 0, 0, false, err
	}
	var live int
	if err = s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM fb_runs r
JOIN fb_sessions s ON s.id = r.session_id
WHERE s.project_id = ? AND s.agent_id = ? AND r.status IN ('running', 'waiting_action')`,
		id, s.agentID).Scan(&live); err != nil {
		return 0, 0, false, err
	}
	return sessions, cronJobs, live > 0, nil
}

// ListProjectsOptions controls ListProjects.
type ListProjectsOptions struct {
	// Search filters by substring over name and root (case-insensitive).
	Search string
	// Sort is "updated" (default, newest first) or "name" (ascending).
	Sort string
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
