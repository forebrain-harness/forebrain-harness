package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
)

// Session statuses recorded in the report.
const (
	StatusMigrated = "migrated"
	StatusSkipped  = "skipped"
	StatusFailed   = "failed"
	StatusOwned    = "owned-by-another-agent"
)

// SessionOutcome is one imported (or refused) conversation.
type SessionOutcome struct {
	SourcePath string
	TargetID   string
	Title      string
	Status     string
	Reason     string
	Messages   int
	IsChild    bool
}

// sessionUnit is one transcript to import, in source-agnostic form: where it
// lives, the target id it lands under, and the fallbacks a transcript too
// short to carry its own metadata inherits. Both sources build these lists;
// importUnits walks them identically.
type sessionUnit struct {
	Path     string
	TargetID string
	// ParentID is the target id of the conversation this one was spawned
	// from; empty for a main conversation.
	ParentID string
	IsChild  bool
	Bytes    int64
	// Title, when set, is the source index's authoritative title; it wins
	// over the transcript's own first-user-message fallback.
	Title string
	// Cwd, GitBranch, FirstSeen and LastSeen fill in aggregates a transcript
	// did not record itself (subagent transcripts inherit their parent's).
	Cwd       string
	GitBranch string
	FirstSeen int64
	LastSeen  int64
}

// applyDefaults folds the unit's fallbacks into a parsed session.
func (u sessionUnit) applyDefaults(parsed *parsedSession) {
	if strings.TrimSpace(u.Title) != "" {
		parsed.Title = strings.TrimSpace(u.Title)
	}
	if strings.TrimSpace(parsed.Cwd) == "" {
		parsed.Cwd = u.Cwd
	}
	if strings.TrimSpace(parsed.GitBranch) == "" {
		parsed.GitBranch = u.GitBranch
	}
	if parsed.FirstSeen == 0 {
		parsed.FirstSeen = u.FirstSeen
	}
	if parsed.LastSeen == 0 {
		parsed.LastSeen = u.LastSeen
	}
}

// importUnits parses and writes every unit via importUnitsCapture without
// keeping the parsed sessions.
func importUnits(ctx context.Context, units []sessionUnit, parse sessionParser, opts *Options, progress func(Progress)) ([]SessionOutcome, int, int64) {
	outcomes, rows, bytes, _ := importUnitsCapture(ctx, units, parse, opts, progress, nil)
	return outcomes, rows, bytes
}

// importUnitsCapture parses and writes every unit and, when capture is
// non-nil, records each parsed session by target id. The Codex plan
// extraction reads the capture instead of re-reading the files (§4.10).
// Each conversation is its own transaction: a failure rolls that
// conversation back, lands in the report, and never touches the others (§6).
func importUnitsCapture(ctx context.Context, units []sessionUnit, parse sessionParser, opts *Options, progress func(Progress), capture map[string]*parsedSession) ([]SessionOutcome, int, int64, map[string]*parsedSession) {
	out := []SessionOutcome{}
	rowsWritten := 0
	var sourceBytes int64
	total := len(units)
	done := 0
	reportProgress := func(detail string) {
		done++
		if progress != nil {
			progress(Progress{Stage: "sessions", Done: done, Total: total, Detail: detail})
		}
	}
	for _, unit := range units {
		if ctx.Err() != nil {
			break
		}
		parsed, err := parse(ctx, unit.Path, strings.TrimPrefix(unit.TargetID, "cli-"), nil)
		if err != nil {
			out = append(out, SessionOutcome{
				SourcePath: unit.Path, TargetID: unit.TargetID,
				Status: StatusFailed, Reason: err.Error(), IsChild: unit.IsChild,
			})
			reportProgress(filepath.Base(unit.Path) + " failed")
			continue
		}
		unit.applyDefaults(parsed)
		if capture != nil {
			capture[unit.TargetID] = parsed
		}
		if opts.DryRun {
			// A dry run settles what is already present exactly as a run
			// would, and counts the rest as the conversations it would import.
			outcome, pending := sessionOutcomeFor(ctx, opts, unit.Path, unit.TargetID, parsed, unit.IsChild)
			if pending {
				outcome.Status = StatusMigrated
				outcome.Messages = len(parsed.Rows)
				rowsWritten += len(parsed.Rows)
				sourceBytes += unit.Bytes
			}
			out = append(out, outcome)
			reportProgress(parsed.Title)
			continue
		}
		outcome, inserted := importOneSession(ctx, opts, unit.Path, unit.TargetID, unit.ParentID, parsed)
		out = append(out, outcome)
		rowsWritten += inserted
		if outcome.Status == StatusMigrated {
			sourceBytes += unit.Bytes
		}
		reportProgress(parsed.Title)
	}
	return out, rowsWritten, sourceBytes, capture
}

// sessionParser is the source-specific transcript reader importUnits calls.
type sessionParser func(ctx context.Context, path, sessionID string, onBadLine func()) (*parsedSession, error)

// importSessions parses and writes every discovered Claude conversation via
// the shared unit importer (§7.1: the walk is source-agnostic).
func importSessions(ctx context.Context, data *claudeData, opts *Options, progress func(Progress)) ([]SessionOutcome, int, int64) {
	return importUnits(ctx, claudeUnits(data, opts), parseSessionFile, opts, progress)
}

// claudeUnits expands the discovered Claude projects into import units in
// dependency order (parent before subagent), applying the project filter.
func claudeUnits(data *claudeData, opts *Options) []sessionUnit {
	filtered := data.Projects
	if opts.OnlyProject {
		filtered = nil
		for _, project := range data.Projects {
			resolved := resolveProjectPath(project, data.JSONProjectPaths)
			if resolved == "" || !strings.EqualFold(memory.ProjectRoot(resolved), memory.ProjectRoot(opts.CurrentProjectRoot)) {
				continue
			}
			filtered = append(filtered, project)
		}
	}
	units := []sessionUnit{}
	for _, project := range filtered {
		for _, session := range project.Sessions {
			parent := sessionUnit{
				Path:     session.Path,
				TargetID: claudeSessionTarget(session.SessionID),
				Bytes:    session.Bytes,
				Cwd:      session.Cwd,
			}
			units = append(units, parent)
			for _, sub := range session.Subagents {
				title, _ := subagentMeta(sub)
				units = append(units, sessionUnit{
					Path:     sub.Path,
					TargetID: claudeSubagentTarget(session.SessionID, sub.AgentID),
					ParentID: parent.TargetID,
					IsChild:  true,
					Bytes:    subagentBytes(sub),
					Title:    title,
					Cwd:      session.Cwd,
				})
			}
		}
	}
	return units
}

// subagentMeta reads a subagent's .meta.json for its title: the description,
// else the agent type, else "" (the transcript's own fallback applies).
func subagentMeta(sub claudeSubagent) (string, string) {
	if sub.MetaPath == "" {
		return "", ""
	}
	var meta struct {
		Description string `json:"description"`
		AgentType   string `json:"agentType"`
	}
	if body, err := os.ReadFile(sub.MetaPath); err == nil && json.Unmarshal(body, &meta) == nil {
		if title := strings.TrimSpace(meta.Description); title != "" {
			return title, ""
		}
		if kind := strings.TrimSpace(meta.AgentType); kind != "" {
			return kind, ""
		}
	}
	return "", ""
}

// claudeSessionTarget builds the durable id of an imported conversation:
// the source session uuid under forebrain's native cli- prefix.
func claudeSessionTarget(sessionID string) string { return "cli-" + sessionID }

func claudeSubagentTarget(sessionID, agentID string) string {
	return claudeSessionTarget(sessionID) + "-" + agentID
}

// SessionIDOf renames nothing; subagent transcripts keep their parent's
// session identity in the source, and their rows are keyed by the child
// target id instead.
func (s claudeSubagent) SessionIDOf(parent string) string { return parent }

func subagentBytes(sub claudeSubagent) int64 {
	if info, err := os.Stat(sub.Path); err == nil {
		return info.Size()
	}
	return 0
}

// codexSessionTarget builds the durable id of an imported Codex thread:
// the thread id under forebrain's native cli- prefix (same shape as Claude's).
func codexSessionTarget(threadID string) string { return "cli-" + threadID }

// sessionOutcomeFor starts a conversation's outcome and settles it when the
// conversation cannot or need not be written: it is already this agent's —
// imported before, or a native session with the same id — or it belongs to
// another primary agent (B10), or the state cannot be read. It reports
// whether the conversation still has to be written.
func sessionOutcomeFor(ctx context.Context, opts *Options, sourcePath, targetID string, parsed *parsedSession, child bool) (SessionOutcome, bool) {
	outcome := SessionOutcome{SourcePath: sourcePath, TargetID: targetID, Title: parsed.Title, IsChild: child}
	if opts.DB == nil {
		outcome.Status = StatusFailed
		outcome.Reason = "state database unavailable"
		return outcome, false
	}
	owner, origin, err := sessionOwner(ctx, opts.DB, targetID)
	if err != nil {
		outcome.Status = StatusFailed
		outcome.Reason = err.Error()
		return outcome, false
	}
	if owner == "" {
		return outcome, true
	}
	outcome.Status = StatusSkipped
	switch {
	case !strings.EqualFold(strings.TrimSpace(owner), strings.TrimSpace(opts.AgentID)):
		outcome.Status = StatusOwned
		outcome.Reason = "session id already belongs to another primary agent"
	case strings.TrimSpace(origin) == "migrated":
		outcome.Reason = "already migrated"
	default:
		outcome.Reason = "a native session with this id already exists"
	}
	return outcome, false
}

// importOneSession writes one conversation atomically. Existence is checked
// before the transaction opens: a session owned by another primary agent is
// skipped as such (B10) rather than silently writing into it, and a session
// this agent already has is skipped as already imported — the per-session
// transaction means a previous run either wrote all of it or none.
func importOneSession(ctx context.Context, opts *Options, sourcePath, targetID, parentID string, parsed *parsedSession) (SessionOutcome, int) {
	outcome, pending := sessionOutcomeFor(ctx, opts, sourcePath, targetID, parsed, parentID != "")
	if !pending {
		return outcome, 0
	}

	tx, err := opts.DB.BeginTx(ctx, nil)
	if err != nil {
		outcome.Status = StatusFailed
		outcome.Reason = err.Error()
		return outcome, 0
	}
	defer func() { _ = tx.Rollback() }()

	if err := insertSession(ctx, tx, opts, targetID, parentID, parsed); err == nil {
		var inserted int
		inserted, err = insertRows(ctx, tx, targetID, parsed)
		if err == nil {
			err = tx.Commit()
		}
		if err == nil {
			outcome.Status = StatusMigrated
			outcome.Messages = inserted
			return outcome, inserted
		}
	}
	outcome.Status = StatusFailed
	outcome.Reason = err.Error()
	return outcome, 0
}

// sessionOwner reports the tenant and origin of an existing session row, or
// empty strings when the id is free.
func sessionOwner(ctx context.Context, db *sql.DB, sessionID string) (string, string, error) {
	var owner, origin string
	err := db.QueryRowContext(ctx,
		`SELECT agent_id, origin FROM fb_sessions WHERE id=?`, sessionID).
		Scan(&owner, &origin)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	return owner, origin, nil
}

// insertSession writes the conversation row with everything the transcript
// aggregated, including the migration identity (B9).
//
// The parent link names a conversation of this agent that exists. A parent is
// imported before its subagents, but one that failed to parse, or whose id
// belongs to another primary agent, is not there to link to: the subagent is
// imported on its own rather than refused by the parent reference or tied to
// another tenant's conversation.
func insertSession(ctx context.Context, tx *sql.Tx, opts *Options, targetID, parentID string, parsed *parsedSession) error {
	initialWindow := parsed.initialWindowID
	if initialWindow == "" {
		initialWindow = stableWindowID(parsed.SessionID, "initial")
	}
	agentID := strings.TrimSpace(opts.AgentID)
	result, err := tx.ExecContext(ctx, `
INSERT INTO fb_sessions(
  id, agent_id, title, updated_at, created_at, parent_session_id,
  cwd, git_branch,
  memory_mode, memory_source, initial_window_id, origin
) VALUES(?,?,?,?,?,(SELECT id FROM fb_sessions WHERE id=? AND agent_id=?),?,?,?,?,?,?)`,
		targetID, agentID, migratedTitle(parsed, targetID),
		parsed.LastSeen, nonZeroTime(parsed.FirstSeen, parsed.LastSeen), strings.TrimSpace(parentID), agentID,
		parsed.Cwd, parsed.GitBranch,
		"disabled", "tui", initialWindow, "migrated")
	if err != nil {
		return err
	}
	if _, err := result.RowsAffected(); err != nil {
		return err
	}
	return nil
}

// migratedTitle names an imported conversation: its own title when the source
// carried one, the session id when it did not — title is NOT NULL now.
func migratedTitle(parsed *parsedSession, targetID string) string {
	if t := strings.TrimSpace(parsed.Title); t != "" {
		return t
	}
	return targetID
}

// nonZeroTime keeps the created_at CHECK honest on imports whose source only
// recorded when the conversation was last seen.
func nonZeroTime(v, fallback int64) int64 {
	if v > 0 {
		return v
	}
	return fallback
}

// insertRows appends the mapped rows in transcript order and reports the last
// boundary row id for the session's compact boundary pointer.
func insertRows(ctx context.Context, tx *sql.Tx, targetID string, parsed *parsedSession) (int, error) {
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO fb_messages(
  session_id, role, content, created_at, message_id, parts, model,
  usage_json, tool_step_id, tool_meta_json
) VALUES(?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	inserted := 0
	var boundaryRowID int64
	for _, row := range parsed.Rows {
		res, err := stmt.ExecContext(ctx,
			targetID, row.Role, row.Content, row.CreatedAt, row.MessageID, row.PartsJSON, row.Model,
			row.UsageJSON, row.ToolStepID, row.ToolMetaJSON)
		if err != nil {
			return inserted, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return inserted, err
		}
		if row.IsBoundary {
			boundaryRowID = id
		}
		inserted++
	}
	// The boundary pointer is the LAST boundary (B3): the model context
	// rebuild projects from the newest checkpoint only.
	if boundaryRowID > 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE fb_sessions SET compact_boundary_message_id=? WHERE id=?`,
			fmt.Sprintf("%d", boundaryRowID), targetID); err != nil {
			return inserted, err
		}
	}
	return inserted, nil
}
