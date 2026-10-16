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
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// Session statuses recorded in the report.
const (
	StatusMigrated = "migrated"
	StatusSkipped  = "skipped"
	StatusFailed   = "failed"
	StatusOwned    = "owned-by-another-agent"
	// StatusRebound marks a conversation an earlier import wrote before
	// sessions carried a project: this run bound it to its project so the
	// project-scoped surfaces (/resume, the web project list) can see it
	// again. It is a healed state, not a failure.
	StatusRebound = "rebound"
	// StatusUpdated marks a conversation whose source transcript grew since
	// the last import: this run appended the new rows (owner decision
	// 2026-10-16 — "增量" includes a migrated session gaining messages).
	StatusUpdated = "updated"
)

// reasonUnchangedSinceLastRun is the fast path's skip reason: the migration
// ledger's fingerprint says the source transcript has not moved since the
// import that wrote this conversation, so it was not parsed again.
const reasonUnchangedSinceLastRun = "already imported; unchanged since last run"

// reasonSourceTranscriptGone names a migrated conversation whose source
// transcript is no longer on disk: this run cannot discover it, so nothing
// it does can bind the row, and the report says so instead of leaving a
// silently projectless row behind.
const reasonSourceTranscriptGone = "source transcript no longer present; cannot resolve its project — it stays unlisted in project-scoped /resume"

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
	// MtimeMs is the source file's modification time at discovery; with
	// Bytes it is the incremental fingerprint the migration ledger compares.
	MtimeMs int64
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
	if u.IsChild && strings.TrimSpace(u.Cwd) != "" {
		// A subagent conversation belongs to its parent's project (the v9
		// precedent: children inherit the parent's project and directory). Its
		// own transcript may record a cwd it ran a subtask in; binding there
		// would split it from the parent the report prints it under.
		parsed.Cwd = strings.TrimSpace(u.Cwd)
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
func importUnits(ctx context.Context, units []sessionUnit, parse sessionParser, opts *Options, progress func(Progress), ledger *unitLedger) ([]SessionOutcome, int, int64) {
	outcomes, rows, bytes, _ := importUnitsCapture(ctx, units, parse, opts, progress, nil, ledger)
	return outcomes, rows, bytes
}

// importUnitsCapture parses and writes every unit and, when capture is
// non-nil, records each parsed session by target id. The Codex plan
// extraction reads the capture instead of re-reading the files (§4.10).
// Each conversation is its own transaction: a failure rolls that
// conversation back, lands in the report, and never touches the others (§6).
//
// The migration ledger makes this incremental. A unit whose recorded
// fingerprint matches its file — and whose conversation row is still this
// agent's, bound, with every plan it owns intact — is not parsed at all.
// A unit whose file grew has the new rows appended; a unit the ledger has
// never seen keeps today's semantics, with the fingerprint backfilled so
// the next run takes the fast path.
func importUnitsCapture(ctx context.Context, units []sessionUnit, parse sessionParser, opts *Options, progress func(Progress), capture map[string]*parsedSession, ledger *unitLedger) ([]SessionOutcome, int, int64, map[string]*parsedSession) {
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
		if ledger != nil {
			fast, ok := fastPathOutcome(ctx, ledger, opts, unit)
			if ok {
				out = append(out, fast)
				reportProgress(fast.Title)
				continue
			}
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
			if pending && importProjectRoot(opts, parsed.Cwd) == "" {
				// The same scope a run settles: a conversation with no
				// directory to belong to is refused, not importable.
				outcome.Status = StatusSkipped
				outcome.Reason = "no working directory: a conversation must belong to a project"
				pending = false
			}
			if pending {
				outcome.Status = StatusMigrated
				outcome.Messages = len(parsed.Rows)
				rowsWritten += len(parsed.Rows)
				sourceBytes += unit.Bytes
			} else if outcome.Reason == "already migrated" {
				if _, hit := ledger.lookup(unit.TargetID); hit {
					// The ledger knows this unit and the file grew: the run
					// would append exactly these rows. A ledger miss is the
					// upgrade path — today's semantics, no append.
					if delta := countSessionDelta(ctx, opts, unit.TargetID, parsed); delta > 0 {
						outcome.Status = StatusUpdated
						outcome.Messages = delta
						rowsWritten += delta
					}
				}
			}
			out = append(out, outcome)
			reportProgress(parsed.Title)
			continue
		}
		_, hit := ledger.lookup(unit.TargetID)
		if hit {
			// The ledger knows this unit, so "already migrated" is an
			// increment, not a first sighting: append what grew, or refresh
			// the fingerprint when nothing did.
			if outcome, pending := sessionOutcomeFor(ctx, opts, unit.Path, unit.TargetID, parsed, unit.IsChild); !pending && outcome.Reason == "already migrated" {
				outcome, inserted := appendSessionDelta(ctx, opts, ledger, unit, parsed)
				out = append(out, outcome)
				rowsWritten += inserted
				reportProgress(parsed.Title)
				continue
			}
		}
		outcome, inserted := importOneSession(ctx, opts, unit, parsed, ledger)
		if !hit && outcome.Status == StatusSkipped && outcome.Reason == "already migrated" {
			// The conversation was migrated before the ledger existed
			// (upgrade path): today's semantics already skipped it, and the
			// fingerprint is backfilled from the file as it stands now — the
			// tail it grew before the upgrade stays out, exactly as before
			// (owner decision 2026-10-16: pure backfill, zero semantics
			// change). Codex plan records settle from this run's parse.
			_ = recordUnit(ctx, opts.DB, strings.TrimSpace(opts.AgentID), ledger.source, unit, len(parsed.Rows), ledger.settledPlans(), opts.now())
		}
		out = append(out, outcome)
		rowsWritten += inserted
		if outcome.Status == StatusMigrated {
			sourceBytes += unit.Bytes
		} else if hit && (outcome.Status == StatusOwned ||
			outcome.Status == StatusSkipped && outcome.Reason == "a native session with this id already exists") {
			// The row this fingerprint described is no longer this agent's
			// migrated conversation; keeping the fingerprint would force a
			// parse on every later run for a unit that never settles.
			_ = dropUnit(ctx, opts.DB, strings.TrimSpace(opts.AgentID), unit.TargetID)
		}
		reportProgress(parsed.Title)
	}
	return out, rowsWritten, sourceBytes, capture
}

// fastPathOutcome settles a unit whose recorded fingerprint matches its file
// without parsing it. It applies only when the conversation row is still this
// agent's, migrated and project-bound, and — for a Codex run consuming plans
// — every plan document the unit owns is still on disk byte for byte; any
// other state falls through to the parse path and heals itself there. The
// second return value reports whether the fast path settled the unit.
func fastPathOutcome(ctx context.Context, ledger *unitLedger, opts *Options, unit sessionUnit) (SessionOutcome, bool) {
	fingerprint, hit := ledger.lookup(unit.TargetID)
	if !hit || !fingerprint.matches(unit.Bytes, unit.MtimeMs) {
		return SessionOutcome{}, false
	}
	if ledger.source == string(KindCodex) && ledger.wantsPlans && !ledger.plansSatisfied(fingerprint) {
		return SessionOutcome{}, false
	}
	owner, origin, projectID, err := sessionOwner(ctx, opts.DB, unit.TargetID)
	if err != nil ||
		!strings.EqualFold(strings.TrimSpace(owner), strings.TrimSpace(opts.AgentID)) ||
		strings.TrimSpace(origin) != "migrated" ||
		strings.TrimSpace(projectID) == "" {
		return SessionOutcome{}, false
	}
	outcome := SessionOutcome{
		SourcePath: unit.Path,
		TargetID:   unit.TargetID,
		Title:      ledger.title(unit.TargetID),
		Status:     StatusSkipped,
		Reason:     reasonUnchangedSinceLastRun,
		IsChild:    unit.IsChild,
	}
	if ledger.source == string(KindCodex) && ledger.wantsPlans && len(fingerprint.Plans) > 0 {
		// Keep the run's plan report as complete as the first run's: every
		// recorded plan is verified present, so each reports up-to-date.
		synthetic := make([]PlanOutcome, 0, len(fingerprint.Plans))
		for _, record := range fingerprint.Plans {
			synthetic = append(synthetic, PlanOutcome{
				Name: record.Name, SessionID: unit.TargetID, Status: "up-to-date",
			})
		}
		ledger.plans[unit.TargetID] = synthetic
	}
	return outcome, true
}

// sessionParser is the source-specific transcript reader importUnits calls.
type sessionParser func(ctx context.Context, path, sessionID string, onBadLine func()) (*parsedSession, error)

// importSessions parses and writes every discovered Claude conversation via
// the shared unit importer (§7.1: the walk is source-agnostic).
func importSessions(ctx context.Context, data *claudeData, opts *Options, progress func(Progress), ledger *unitLedger) ([]SessionOutcome, int, int64) {
	return importUnits(ctx, claudeUnits(data, opts), parseSessionFile, opts, progress, ledger)
}

// claudeUnits expands the discovered Claude projects into import units in
// dependency order (parent before subagent), applying the project filter.
func claudeUnits(data *claudeData, opts *Options) []sessionUnit {
	// Each encoded directory resolves to its real project path once (B5):
	// the OnlyProject filter matches on it, and a session whose transcript
	// carries no cwd of its own falls back to it for the project binding.
	roots := make(map[string]string, len(data.Projects))
	for _, project := range data.Projects {
		roots[project.Dir] = resolveProjectPath(project, data.JSONProjectPaths)
	}
	filtered := data.Projects
	if opts.OnlyProject {
		filtered = nil
		for _, project := range data.Projects {
			resolved := roots[project.Dir]
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
				MtimeMs:  session.MtimeMs,
				Cwd:      firstNonEmptyStr(session.Cwd, roots[project.Dir]),
			}
			units = append(units, parent)
			for _, sub := range session.Subagents {
				title, _ := subagentMeta(sub)
				units = append(units, sessionUnit{
					Path:     sub.Path,
					TargetID: claudeSubagentTarget(session.SessionID, sub.AgentID),
					ParentID: parent.TargetID,
					IsChild:  true,
					Bytes:    sub.Bytes,
					MtimeMs:  sub.MtimeMs,
					Title:    title,
					// The child inherits the parent's effective cwd,
					// project-level fallback included: it ran where its
					// parent ran, so it binds to the same project.
					Cwd: parent.Cwd,
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

// codexSessionTarget builds the durable id of an imported Codex thread:
// the thread id under forebrain's native cli- prefix (same shape as Claude's).
func codexSessionTarget(threadID string) string { return "cli-" + threadID }

// reportOrphanedMigratedSessions closes the rebind loop's blind spot: a row
// an earlier import wrote without a project whose source transcript is gone
// can never be discovered again, so no rerun can bind it. The run ends with a
// read-only inventory that names each such row in the report — the "no
// unexplained NULL" contract covers rows the sources still name; the ones
// they no longer do are called out instead of silently passed over. Rows this
// run produced an outcome for keep that outcome's reason.
func reportOrphanedMigratedSessions(ctx context.Context, opts *Options, report *Report) {
	if opts.DB == nil || !opts.wants("sessions") {
		return
	}
	seen := make(map[string]bool, len(report.Sessions))
	for _, outcome := range report.Sessions {
		seen[outcome.TargetID] = true
	}
	rows, err := opts.DB.QueryContext(ctx,
		`SELECT id FROM fb_sessions WHERE origin='migrated' AND agent_id=? AND project_id IS NULL`,
		strings.TrimSpace(opts.AgentID))
	if err != nil {
		report.Notices = append(report.Notices, "orphaned migrated-conversation check failed: "+err.Error())
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		if seen[id] {
			continue
		}
		report.Sessions = append(report.Sessions, SessionOutcome{
			TargetID: id,
			Status:   StatusSkipped,
			Reason:   reasonSourceTranscriptGone,
		})
	}
	if err := rows.Err(); err != nil {
		report.Notices = append(report.Notices, "orphaned migrated-conversation check failed: "+err.Error())
	}
}

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
	owner, origin, projectID, err := sessionOwner(ctx, opts.DB, targetID)
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
		switch {
		case strings.TrimSpace(projectID) != "":
			outcome.Reason = "already migrated"
		case importProjectRoot(opts, parsed.Cwd) == "":
			// Imported before sessions carried a project, and nothing to
			// bind it to now: the row stays as it is, and the reason says
			// so instead of leaving a silent project_id NULL behind.
			outcome.Reason = "already migrated without a working directory; nothing to bind"
		default:
			// Imported before sessions carried a project: this run binds
			// it, so a rerun heals rows the project-scoped listings could
			// never see.
			outcome.Status = StatusRebound
			if opts.DryRun {
				outcome.Reason = "would bind to its project"
			}
		}
	default:
		outcome.Reason = "a native session with this id already exists"
	}
	return outcome, false
}

// importProjectRoot resolves the root of the project an imported conversation
// binds to: the launch project's root when the import was scoped to it, else
// the conversation's own working directory. Empty means the source recorded
// no directory and the conversation cannot belong to a project — the same
// rule schema migration v9 applied to projectless sessions (bind by canonical
// cwd, children follow their parent, no directory means no project).
func importProjectRoot(opts *Options, cwd string) string {
	if opts.OnlyProject {
		// The matcher accepted subdirectory cwds as belonging to the launch
		// project, so the binding must land on that root too — binding a
		// subdirectory session to its own directory would keep it invisible
		// in the project the import was scoped to.
		return strings.TrimSpace(opts.CurrentProjectRoot)
	}
	return strings.TrimSpace(cwd)
}

// importOneSession writes one conversation atomically. Existence is checked
// before the transaction opens: a session owned by another primary agent is
// skipped as such (B10) rather than silently writing into it, and a session
// this agent already has is skipped as already imported — the per-session
// transaction means a previous run either wrote all of it or none. A session
// an earlier run imported without a project is rebound instead (see
// rebindMigrated). A successful write also records the unit's fingerprint in
// the same transaction, so the next run can skip reparsing the file.
func importOneSession(ctx context.Context, opts *Options, unit sessionUnit, parsed *parsedSession, ledger *unitLedger) (SessionOutcome, int) {
	sourcePath, targetID, parentID := unit.Path, unit.TargetID, unit.ParentID
	outcome, pending := sessionOutcomeFor(ctx, opts, sourcePath, targetID, parsed, parentID != "")
	if !pending {
		if outcome.Status == StatusRebound {
			return rebindMigrated(ctx, opts, targetID, parsed, outcome)
		}
		return outcome, 0
	}

	root := importProjectRoot(opts, parsed.Cwd)
	if root == "" {
		// A conversation must belong to a project; writing it with a NULL
		// project_id is exactly the state this importer is being fixed of.
		outcome.Status = StatusSkipped
		outcome.Reason = "no working directory: a conversation must belong to a project"
		return outcome, 0
	}
	// The project row resolves on the shared connection, outside the insert
	// transaction: FindOrCreateByRoot is idempotent per (agent, root).
	project, _, err := state.NewProjectStore(opts.DB, opts.AgentID).FindOrCreateByRoot(ctx, root, state.CreateProjectInput{ResourceAccess: true})
	if err != nil {
		outcome.Status = StatusFailed
		outcome.Reason = err.Error()
		return outcome, 0
	}

	tx, err := opts.DB.BeginTx(ctx, nil)
	if err != nil {
		outcome.Status = StatusFailed
		outcome.Reason = err.Error()
		return outcome, 0
	}
	defer func() { _ = tx.Rollback() }()

	if err := insertSession(ctx, tx, opts, targetID, parentID, parsed, project); err == nil {
		var inserted int
		inserted, err = insertRows(ctx, tx, targetID, parsed)
		if err == nil && ledger != nil {
			// Codex plan records are not known yet — the extraction that
			// produces them runs after the session import — so the row is
			// written with the plans list pending (see recordUnitPlans).
			err = recordUnit(ctx, tx, strings.TrimSpace(opts.AgentID), ledger.source, unit, inserted, ledger.settledPlans(), opts.now())
		}
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

// countSessionDelta reports how many of a parsed transcript's rows are not
// yet in the conversation: the rows a run would append to one that grew.
func countSessionDelta(ctx context.Context, opts *Options, targetID string, parsed *parsedSession) int {
	existing := make(map[string]bool, len(parsed.Rows))
	if rows, err := opts.DB.QueryContext(ctx,
		`SELECT DISTINCT message_id FROM fb_messages WHERE session_id=? AND message_id<>''`, targetID); err == nil {
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				existing[id] = true
			}
		}
		_ = rows.Close()
	}
	delta := 0
	for _, row := range parsed.Rows {
		if row.MessageID != "" && !existing[row.MessageID] {
			delta++
		}
	}
	return delta
}

// appendSessionDelta extends an already-migrated conversation with the rows
// its transcript gained since the import (owner decision 2026-10-16: the
// incremental semantics include a migrated session being appended to). Rows
// are keyed by source message id: a row already present is not written twice,
// so re-running against the same grown file appends nothing. Nothing is ever
// deleted — records removed from the source stay in the database, which is
// the only honest reading of an append-only transcript.
//
// The caller has verified the conversation is this agent's, migrated and
// project-bound, and that the ledger knows the unit. When the parse yields no
// new rows the conversation is untouched and only the fingerprint moves — a
// rewritten-but-equivalent file converges onto the fast path instead of
// re-parsing on every later run.
func appendSessionDelta(ctx context.Context, opts *Options, ledger *unitLedger, unit sessionUnit, parsed *parsedSession) (SessionOutcome, int) {
	outcome := SessionOutcome{
		SourcePath: unit.Path, TargetID: unit.TargetID,
		Title: parsed.Title, Status: StatusSkipped, Reason: reasonUnchangedSinceLastRun, IsChild: unit.IsChild,
	}
	existing := make(map[string]bool, len(parsed.Rows))
	if rows, err := opts.DB.QueryContext(ctx,
		`SELECT DISTINCT message_id FROM fb_messages WHERE session_id=? AND message_id<>''`, unit.TargetID); err == nil {
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				existing[id] = true
			}
		}
		_ = rows.Close()
	}
	newRows := make([]parsedRow, 0, len(parsed.Rows))
	for _, row := range parsed.Rows {
		if row.MessageID != "" && !existing[row.MessageID] {
			newRows = append(newRows, row)
		}
	}
	if len(newRows) == 0 {
		// Nothing grew that maps to a new row; refresh the fingerprint on
		// its own so the next run takes the fast path.
		if !ledger.dryRun {
			_ = recordUnit(ctx, opts.DB, strings.TrimSpace(opts.AgentID), ledger.source, unit, len(parsed.Rows), ledger.settledPlans(), opts.now())
		}
		return outcome, 0
	}
	if opts.DryRun {
		outcome.Status = StatusUpdated
		outcome.Messages = len(newRows)
		outcome.Reason = ""
		return outcome, len(newRows)
	}
	tx, err := opts.DB.BeginTx(ctx, nil)
	if err != nil {
		outcome.Status = StatusFailed
		outcome.Reason = err.Error()
		return outcome, 0
	}
	defer func() { _ = tx.Rollback() }()
	inserted, err := insertRowsFiltered(ctx, tx, unit.TargetID, newRows)
	if err == nil {
		_, err = tx.ExecContext(ctx, `
UPDATE fb_sessions
SET updated_at = max(updated_at, ?),
    title = CASE WHEN TRIM(?) <> '' AND title <> TRIM(?) THEN TRIM(?) ELSE title END
WHERE id=? AND agent_id=? AND origin='migrated'`,
			parsed.LastSeen, parsed.Title, parsed.Title, parsed.Title, unit.TargetID, strings.TrimSpace(opts.AgentID))
	}
	if err == nil {
		err = recordUnit(ctx, tx, strings.TrimSpace(opts.AgentID), ledger.source, unit, len(parsed.Rows), ledger.settledPlans(), opts.now())
	}
	if err != nil {
		outcome.Status = StatusFailed
		outcome.Reason = err.Error()
		return outcome, 0
	}
	if err := tx.Commit(); err != nil {
		outcome.Status = StatusFailed
		outcome.Reason = err.Error()
		return outcome, 0
	}
	outcome.Status = StatusUpdated
	outcome.Reason = ""
	outcome.Messages = inserted
	return outcome, inserted
}

// rebindMigrated heals a conversation an earlier import wrote before sessions
// carried a project: it binds the row to its project so the project-scoped
// surfaces can list and resume it. Only this agent's migrated rows are ever
// touched; updated_at and source stay as they are so history order and the
// subagent-visibility contract (§4.4) are unchanged.
func rebindMigrated(ctx context.Context, opts *Options, targetID string, parsed *parsedSession, outcome SessionOutcome) (SessionOutcome, int) {
	project, _, err := state.NewProjectStore(opts.DB, opts.AgentID).FindOrCreateByRoot(ctx, importProjectRoot(opts, parsed.Cwd), state.CreateProjectInput{ResourceAccess: true})
	if err != nil {
		outcome.Status = StatusFailed
		outcome.Reason = err.Error()
		return outcome, 0
	}
	tx, err := opts.DB.BeginTx(ctx, nil)
	if err != nil {
		outcome.Status = StatusFailed
		outcome.Reason = err.Error()
		return outcome, 0
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
UPDATE fb_sessions
SET project_id=?, cwd=?
WHERE id=? AND agent_id=? AND origin='migrated'`,
		project.ID, project.Root, targetID, strings.TrimSpace(opts.AgentID))
	if err != nil {
		outcome.Status = StatusFailed
		outcome.Reason = err.Error()
		return outcome, 0
	}
	if n, err := result.RowsAffected(); err != nil {
		outcome.Status = StatusFailed
		outcome.Reason = err.Error()
		return outcome, 0
	} else if n == 0 {
		outcome.Status = StatusFailed
		outcome.Reason = "conversation is no longer a migrated row"
		return outcome, 0
	}
	if err := tx.Commit(); err != nil {
		outcome.Status = StatusFailed
		outcome.Reason = err.Error()
		return outcome, 0
	}
	outcome.Reason = ""
	return outcome, 0
}

// sessionOwner reports the tenant, origin and project binding of an existing
// session row, or empty strings when the id is free.
func sessionOwner(ctx context.Context, db *sql.DB, sessionID string) (string, string, string, error) {
	var owner, origin, projectID string
	err := db.QueryRowContext(ctx,
		`SELECT agent_id, origin, COALESCE(project_id,'') FROM fb_sessions WHERE id=?`, sessionID).
		Scan(&owner, &origin, &projectID)
	if err == sql.ErrNoRows {
		return "", "", "", nil
	}
	if err != nil {
		return "", "", "", err
	}
	return owner, origin, projectID, nil
}

// insertSession writes the conversation row with everything the transcript
// aggregated, including the migration identity (B9) and the project binding:
// an imported conversation is born under its project with the project's
// canonical root as its cwd, the same shape ensureSession gives a native one.
//
// The parent link names a conversation of this agent that exists. A parent is
// imported before its subagents, but one that failed to parse, or whose id
// belongs to another primary agent, is not there to link to: the subagent is
// imported on its own rather than refused by the parent reference or tied to
// another tenant's conversation.
func insertSession(ctx context.Context, tx *sql.Tx, opts *Options, targetID, parentID string, parsed *parsedSession, project state.Project) error {
	initialWindow := parsed.initialWindowID
	if initialWindow == "" {
		initialWindow = stableWindowID(parsed.SessionID, "initial")
	}
	agentID := strings.TrimSpace(opts.AgentID)
	result, err := tx.ExecContext(ctx, `
INSERT INTO fb_sessions(
  id, agent_id, title, updated_at, created_at, parent_session_id,
  cwd, git_branch,
  memory_mode, memory_source, initial_window_id, origin, project_id
) VALUES(?,?,?,?,?,(SELECT id FROM fb_sessions WHERE id=? AND agent_id=?),?,?,?,?,?,?,?)`,
		targetID, agentID, migratedTitle(parsed, targetID),
		parsed.LastSeen, nonZeroTime(parsed.FirstSeen, parsed.LastSeen), strings.TrimSpace(parentID), agentID,
		project.Root, parsed.GitBranch,
		"disabled", "tui", initialWindow, "migrated", project.ID)
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
	return insertRowsFiltered(ctx, tx, targetID, parsed.Rows)
}

// insertRowsFiltered appends the given mapped rows and tracks the last
// boundary among them. The full import passes every row; a delta append
// passes only the rows the transcript gained, so a boundary the conversation
// already has never moves the pointer and one the delta brings does.
func insertRowsFiltered(ctx context.Context, tx *sql.Tx, targetID string, rows []parsedRow) (int, error) {
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
	for _, row := range rows {
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
