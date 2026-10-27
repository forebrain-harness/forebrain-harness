package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// LLMAttemptOutcome names how one physical model request ended.
const (
	LLMAttemptSuccess     = "success"
	LLMAttemptFailed      = "failed"
	LLMAttemptKeptPartial = "kept-partial"
	LLMAttemptCancelled   = "cancelled"
)

// LLMAttempt is one row of a run's attempt ledger: one physical model request.
// Call orders the logical requests (one pass through the network-retry
// wrapper each), attempt the physical tries within one; the pair names the
// row. InputTokens/OutputTokens are nil when the attempt reported no usage —
// a transport failure never does, and nil is "not reported", never an
// estimate. Streaming is nil where the run layer cannot observe whether the
// attempt was sent as a stream.
type LLMAttempt struct {
	ID           int64     `json:"id"`
	RunID        string    `json:"run_id"`
	Call         int       `json:"call"`
	Attempt      int       `json:"attempt"`
	Streaming    *bool     `json:"streaming,omitempty"`
	Status       int       `json:"status"`
	Error        string    `json:"error,omitempty"`
	WaitMs       int64     `json:"wait_ms"`
	ElapsedMs    int64     `json:"elapsed_ms"`
	InputTokens  *int64    `json:"input_tokens,omitempty"`
	OutputTokens *int64    `json:"output_tokens,omitempty"`
	Outcome      string    `json:"outcome"`
	CreatedAt    time.Time `json:"created_at"`
}

// InsertLLMAttempt appends one attempt row. The ledger is an observation, not
// a gate: the caller keeps running whatever the write did.
func (s *RunStore) InsertLLMAttempt(ctx context.Context, in LLMAttempt) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("nil db")
	}
	in.RunID = strings.TrimSpace(in.RunID)
	if in.RunID == "" {
		return fmt.Errorf("llm attempt requires run id")
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO fb_llm_attempts(run_id, call, attempt, streaming, status, error, wait_ms, elapsed_ms, input_tokens, output_tokens, outcome, created_at_ms)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.RunID, in.Call, in.Attempt, boolPtrToInt(in.Streaming), in.Status, in.Error,
		in.WaitMs, in.ElapsedMs, int64PtrToNull(in.InputTokens), int64PtrToNull(in.OutputTokens),
		in.Outcome, in.CreatedAt.UnixMilli())
	return err
}

// ListLLMAttempts returns a run's attempt rows oldest first, insertion order
// preserved, so a reader replays the run's requests in the order they happened.
func (s *RunStore) ListLLMAttempts(ctx context.Context, runID string, limit int) ([]LLMAttempt, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 500
	}
	if limit > 5000 {
		limit = 5000
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, run_id, call, attempt, streaming, status, error, wait_ms, elapsed_ms, input_tokens, output_tokens, outcome, created_at_ms
FROM fb_llm_attempts WHERE run_id=? ORDER BY id ASC LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMAttempt
	for rows.Next() {
		var a LLMAttempt
		var streaming, inputTokens, outputTokens sql.NullInt64
		var occurred int64
		if err := rows.Scan(&a.ID, &a.RunID, &a.Call, &a.Attempt, &streaming, &a.Status, &a.Error,
			&a.WaitMs, &a.ElapsedMs, &inputTokens, &outputTokens, &a.Outcome, &occurred); err != nil {
			return nil, err
		}
		if streaming.Valid {
			v := streaming.Int64 != 0
			a.Streaming = &v
		}
		if inputTokens.Valid {
			v := inputTokens.Int64
			a.InputTokens = &v
		}
		if outputTokens.Valid {
			v := outputTokens.Int64
			a.OutputTokens = &v
		}
		a.CreatedAt = time.UnixMilli(occurred).UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}

// LatestPrimaryRunIDForSession names the session's most recent top-level run,
// whatever its status: a failed or cancelled run's attempts are exactly the
// rows a reader wants after a bad turn. Empty when the session has none.
// updated_at is second-granularity, so same-second ties break by insertion
// order — the later-created run wins, never the storage engine's pick.
func (s *RunStore) LatestPrimaryRunIDForSession(ctx context.Context, sessionID string) (string, error) {
	if s == nil || s.DB == nil {
		return "", fmt.Errorf("nil db")
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return "", fmt.Errorf("empty session id")
	}
	var runID string
	err := s.DB.QueryRowContext(ctx, `
SELECT id FROM fb_runs WHERE session_id=? AND parent_run_id IS NULL
ORDER BY updated_at DESC, rowid DESC LIMIT 1`, sid).Scan(&runID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return runID, err
}

// LatestPrimaryRunTreeAttempts returns the attempt rows of the session's
// latest primary run and every run below it — its subagents' runs included,
// each row carrying its own run id. The owner's ruling on plan 165's fourth
// open question: "how many times did this answer try" must count the agents
// the answer dispatched. Oldest first, insertion order preserved; limit caps
// the rows (0 means the 500 default ListLLMAttempts applies).
func (s *RunStore) LatestPrimaryRunTreeAttempts(ctx context.Context, sessionID string, limit int) ([]LLMAttempt, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return nil, fmt.Errorf("empty session id")
	}
	if limit <= 0 {
		limit = 500
	}
	if limit > 5000 {
		limit = 5000
	}
	// One statement, one snapshot: the latest primary run's choice and the
	// run-tree recursion live in the same query, so no run can slip between
	// the two reads.
	rows, err := s.DB.QueryContext(ctx, `
WITH RECURSIVE latest AS (
  SELECT id FROM fb_runs WHERE session_id=? AND parent_run_id IS NULL
  ORDER BY updated_at DESC, rowid DESC LIMIT 1
), run_tree(id) AS (
  SELECT id FROM latest
  UNION
  SELECT r.id FROM fb_runs r JOIN run_tree t ON r.parent_run_id=t.id
)
SELECT id, run_id, call, attempt, streaming, status, error, wait_ms, elapsed_ms,
       input_tokens, output_tokens, outcome, created_at_ms
FROM fb_llm_attempts WHERE run_id IN (SELECT id FROM run_tree)
ORDER BY id ASC LIMIT ?`, sid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMAttempt
	for rows.Next() {
		var a LLMAttempt
		var streaming, inputTokens, outputTokens sql.NullInt64
		var occurred int64
		if err := rows.Scan(&a.ID, &a.RunID, &a.Call, &a.Attempt, &streaming, &a.Status, &a.Error,
			&a.WaitMs, &a.ElapsedMs, &inputTokens, &outputTokens, &a.Outcome, &occurred); err != nil {
			return nil, err
		}
		if streaming.Valid {
			v := streaming.Int64 != 0
			a.Streaming = &v
		}
		if inputTokens.Valid {
			v := inputTokens.Int64
			a.InputTokens = &v
		}
		if outputTokens.Valid {
			v := outputTokens.Int64
			a.OutputTokens = &v
		}
		a.CreatedAt = time.UnixMilli(occurred).UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}

func boolPtrToInt(b *bool) any {
	if b == nil {
		return nil
	}
	if *b {
		return 1
	}
	return 0
}

func int64PtrToNull(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
