package migrate

// ledger.go reads and writes fb_migration_units, the incremental-migration
// fingerprint table. One row records what a previous /migrate carried in for
// one import unit: the source file's size and mtime at import time, the
// message count it mapped, and — for Codex units — the plan documents the
// extraction wrote out of it. A later run compares the file's current
// (size, mtime) against the row and skips parsing everything that has not
// moved, which is what makes /migrate incremental instead of a full
// re-import on every execution.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"time"
)

// ledgerPlanRecord is one plan document a unit's extraction produced. Path is
// the file writeExtractedPlan chose; BodySha256 is the sha256 of the body the
// source would write there — the fast path hashes the file on disk and
// compares, so a deleted or hand-edited plan file forces the transcript to be
// parsed again (self-heal) instead of being masked as up-to-date.
type ledgerPlanRecord struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	BodySha256 string `json:"sha256"`
}

// unitFingerprint is the ledger's view of one imported unit.
type unitFingerprint struct {
	SourcePath   string
	Bytes        int64
	MtimeMs      int64
	MessageCount int
	// Plans is nil when plans_json was NULL — "not yet determined" (a Codex
	// unit whose importing run never reached the plan extraction). A non-nil
	// empty slice says the unit holds no plans.
	Plans []ledgerPlanRecord
}

// matches reports whether the source file is unchanged since the fingerprint
// was taken. Size catches appends and rewrites; mtime catches same-size
// rewrites. The path is deliberately not compared: a relocated source home
// with identical files is the same data.
func (f unitFingerprint) matches(bytes, mtimeMs int64) bool {
	return f.Bytes == bytes && f.MtimeMs == mtimeMs
}

// ledgerExecer is the write surface recordUnit accepts: the state database
// for standalone rows, or a transaction so the fingerprint lands atomically
// with the session it describes.
type ledgerExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// unitLedger carries one run's fingerprint state: the rows loaded at import
// start, the titles for fast-path outcomes, and the plan outcomes
// synthesized for skipped Codex units (so a second run's report.Plans stays
// as complete as the first run's).
type unitLedger struct {
	db     *sql.DB
	agent  string
	source string
	// dry runs read the ledger but never write it.
	dryRun bool
	// wantsPlans reports whether this run consumes extracted plans; only
	// then is the plan dependency part of the fast-path condition.
	wantsPlans bool
	prints     map[string]unitFingerprint
	titles     map[string]string
	// plans collects the up-to-date outcomes the fast path synthesized for
	// Codex units, keyed by target id, so extractCodexPlans can merge them
	// with the outcomes of the units it really parsed.
	plans map[string][]PlanOutcome
}

// newUnitLedger loads the fingerprints a run of `source` under `opts` can
// skip. It returns nil when there is no state database — without one there is
// nothing to consult, and the importer runs exactly as it did before the
// ledger existed.
func newUnitLedger(ctx context.Context, opts *Options, source string) *unitLedger {
	if opts == nil || opts.DB == nil {
		return nil
	}
	ledger := &unitLedger{
		db:         opts.DB,
		agent:      opts.AgentID,
		source:     source,
		dryRun:     opts.DryRun,
		wantsPlans: opts.wants("plans"),
		prints:     map[string]unitFingerprint{},
		plans:      map[string][]PlanOutcome{},
	}
	if rows, err := opts.DB.QueryContext(ctx,
		`SELECT target_id, source_path, source_bytes, source_mtime_ms, message_count, plans_json
		 FROM fb_migration_units WHERE agent_id=? AND source=?`, ledger.agent, source); err == nil {
		for rows.Next() {
			var target string
			var fp unitFingerprint
			var plansJSON sql.NullString
			if err := rows.Scan(&target, &fp.SourcePath, &fp.Bytes, &fp.MtimeMs, &fp.MessageCount, &plansJSON); err != nil {
				continue
			}
			if plansJSON.Valid && plansJSON.String != "" {
				var records []ledgerPlanRecord
				if json.Unmarshal([]byte(plansJSON.String), &records) == nil {
					fp.Plans = records
				}
			} else if plansJSON.Valid {
				fp.Plans = []ledgerPlanRecord{}
			}
			ledger.prints[target] = fp
		}
		_ = rows.Close()
	}
	return ledger
}

// lookup returns the recorded fingerprint for a target id.
func (l *unitLedger) lookup(targetID string) (unitFingerprint, bool) {
	if l == nil {
		return unitFingerprint{}, false
	}
	fp, ok := l.prints[targetID]
	return fp, ok
}

// title returns the stored conversation title for a fast-path outcome.
func (l *unitLedger) title(targetID string) string {
	if l == nil {
		return ""
	}
	if l.titles == nil {
		l.titles = map[string]string{}
		if rows, err := l.db.QueryContext(context.Background(),
			`SELECT id, title FROM fb_sessions WHERE agent_id=? AND origin='migrated'`, l.agent); err == nil {
			for rows.Next() {
				var id, title string
				if err := rows.Scan(&id, &title); err == nil {
					l.titles[id] = title
				}
			}
			_ = rows.Close()
		}
	}
	return l.titles[targetID]
}

// plansSatisfied reports whether every plan this unit owns is still on disk
// byte for byte. A NULL plans list (not yet determined) forces a parse; an
// empty list says the unit holds no plans and satisfies trivially. Non-codex
// sources and runs that do not consume plans never depend on this.
func (l *unitLedger) plansSatisfied(fp unitFingerprint) bool {
	if fp.Plans == nil {
		return false
	}
	for _, rec := range fp.Plans {
		body, err := os.ReadFile(rec.Path)
		if err != nil {
			return false
		}
		if hashBytes(body) != rec.BodySha256 {
			return false
		}
	}
	return true
}

// settledPlans is the plans value a session-import-time ledger row carries.
// Claude units never depend on a parse for their plans (they are file
// copies), so their list is settled empty; Codex units get their records
// from the extraction that runs after the import, so their row records
// "pending" (nil) and recordUnitPlans settles it.
func (l *unitLedger) settledPlans() []ledgerPlanRecord {
	if l != nil && l.source == string(KindCodex) {
		return nil
	}
	return []ledgerPlanRecord{}
}

// recordUnit writes the unit's fingerprint, inside the caller's transaction
// when one is open. For Codex units plans is nil — the plan extraction fills
// the list in afterwards (recordUnitPlans); for Claude units it is an empty,
// settled list (their plans are file copies that never depend on a parse).
func recordUnit(ctx context.Context, db ledgerExecer, agent, source string, unit sessionUnit, messageCount int, plans []ledgerPlanRecord, now time.Time) error {
	var plansJSON any
	if plans != nil {
		encoded, err := json.Marshal(plans)
		if err != nil {
			return err
		}
		plansJSON = string(encoded)
	}
	_, err := db.ExecContext(ctx, `
INSERT INTO fb_migration_units(
  agent_id, source, target_id, source_path, source_bytes, source_mtime_ms,
  message_count, plans_json, imported_at_ms
) VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(agent_id, target_id) DO UPDATE SET
  source=excluded.source,
  source_path=excluded.source_path,
  source_bytes=excluded.source_bytes,
  source_mtime_ms=excluded.source_mtime_ms,
  message_count=excluded.message_count,
  plans_json=excluded.plans_json,
  imported_at_ms=excluded.imported_at_ms`,
		agent, source, unit.TargetID, unit.Path, unit.Bytes, unit.MtimeMs,
		messageCount, plansJSON, now.UnixMilli())
	return err
}

// recordUnitPlans fills in the plan records a unit's extraction produced,
// settling the NULL the session import left into the list later runs verify.
func recordUnitPlans(ctx context.Context, db *sql.DB, agent, source, targetID string, plans []ledgerPlanRecord) error {
	encoded, err := json.Marshal(plans)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx,
		`UPDATE fb_migration_units SET plans_json=? WHERE agent_id=? AND source=? AND target_id=?`,
		string(encoded), agent, source, targetID)
	return err
}

// dropUnit removes a fingerprint that no longer describes reality: the
// session row it pointed at is gone, or belongs to another agent, so keeping
// the row would force a pointless parse on every later run.
func dropUnit(ctx context.Context, db ledgerExecer, agent, targetID string) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM fb_migration_units WHERE agent_id=? AND target_id=?`, agent, targetID)
	return err
}

func hashBytes(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
