// Approval actions: the service, its types, ask answers, and the TTL sweeper.
package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	ErrActionNotFound = errors.New("action not found")
	ErrNotPending     = errors.New("action not pending")
)

type ActionService struct {
	DB *sql.DB
}

func (s *ActionService) CreatePending(ctx context.Context, sessionID, kind string, payload any) (*Action, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("an approval belongs to one conversation; session required")
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return nil, fmt.Errorf("kind required")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	a := &Action{
		ID:          uuid.NewString(),
		SessionID:   sessionID,
		Kind:        kind,
		Status:      ActionPending,
		PayloadJSON: string(b),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	_, err = s.DB.ExecContext(ctx, `
INSERT INTO fb_actions(id, session_id, kind, status, payload_json, answer_json, error, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?,?)`,
		a.ID, a.SessionID, a.Kind, string(a.Status), a.PayloadJSON, "", "", a.CreatedAt, a.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (s *ActionService) CreateAsk(ctx context.Context, form AskForm) (*Action, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	if len(form.Questions) == 0 {
		return nil, fmt.Errorf("questions required")
	}
	for i := range form.Questions {
		q := form.Questions[i]
		if strings.TrimSpace(q.ID) == "" || strings.TrimSpace(q.Prompt) == "" || len(q.Options) < 2 {
			return nil, fmt.Errorf("invalid question")
		}
		for j := range q.Options {
			if strings.TrimSpace(q.Options[j].ID) == "" || strings.TrimSpace(q.Options[j].Label) == "" {
				return nil, fmt.Errorf("invalid option")
			}
		}
	}
	kind := strings.TrimSpace(form.Kind)
	if kind == "" {
		kind = "user_interaction"
	}
	sessionID := strings.TrimSpace(form.SessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("an ask belongs to one conversation; session required")
	}
	payload, _ := json.Marshal(form)
	now := time.Now().Unix()
	a := &Action{
		ID:          uuid.NewString(),
		SessionID:   sessionID,
		Kind:        kind,
		Status:      ActionPending,
		PayloadJSON: string(payload),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO fb_actions(id, session_id, kind, status, payload_json, answer_json, error, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?,?)`,
		a.ID, a.SessionID, a.Kind, string(a.Status), a.PayloadJSON, "", "", a.CreatedAt, a.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// ActionFilter narrows a list of one agent's actions; an empty field does not
// filter.
type ActionFilter struct {
	SessionID string
	Status    string
	// Requester is the subagent that raised the action, as its payload records
	// it; the primary agent's own actions name none.
	Requester string
}

// List returns one primary agent's actions, newest first, narrowed by f. The
// session JOIN is the tenant filter, and every narrowing is a predicate of the
// query, so a limit applies to the rows that match rather than to a page of
// everything filtered afterwards.
func (s *ActionService) List(ctx context.Context, agentID string, f ActionFilter, limit int) ([]Action, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	if limit == 0 {
		limit = 200
	}
	// An empty agent id leaves the tenant predicate off: a caller with no
	// sessions store bound (test harnesses) reads every conversation's
	// actions, as before that boundary existed.
	query := `
SELECT a.id, a.session_id, a.kind, a.status, a.payload_json, a.answer_json, a.error, a.created_at, a.updated_at
FROM fb_actions a
JOIN fb_sessions s ON s.id = a.session_id`
	var predicates []string
	var args []any
	if agentID = strings.TrimSpace(agentID); agentID != "" {
		predicates = append(predicates, "s.agent_id = ?")
		args = append(args, agentID)
	}
	if sessionID := strings.TrimSpace(f.SessionID); sessionID != "" {
		predicates = append(predicates, "a.session_id = ?")
		args = append(args, sessionID)
	}
	if status := strings.TrimSpace(f.Status); status != "" {
		predicates = append(predicates, "a.status = ?")
		args = append(args, status)
	}
	if requester := strings.TrimSpace(f.Requester); requester != "" {
		predicates = append(predicates, "TRIM(IFNULL(a.payload_json->>'$.agent_id','')) = ?")
		args = append(args, requester)
	}
	if len(predicates) > 0 {
		query += ` WHERE ` + strings.Join(predicates, " AND ")
	}
	query += ` ORDER BY a.updated_at DESC, a.id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Action
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanAction(scanner interface{ Scan(...any) error }) (Action, error) {
	var a Action
	var st string
	if err := scanner.Scan(&a.ID, &a.SessionID, &a.Kind, &st, &a.PayloadJSON, &a.AnswerJSON, &a.Error, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return Action{}, err
	}
	a.Status = ActionStatus(st)
	return a, nil
}

func (s *ActionService) Get(ctx context.Context, id string) (*Action, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrActionNotFound
	}
	a, err := scanAction(s.DB.QueryRowContext(ctx, `
SELECT id, session_id, kind, status, payload_json, answer_json, error, created_at, updated_at
FROM fb_actions WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, ErrActionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *ActionService) AnswerAsk(ctx context.Context, id string, ans AskAnswer) (*Action, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrActionNotFound
	}
	b, _ := json.Marshal(ans)
	now := time.Now().Unix()
	res, err := s.DB.ExecContext(ctx, `
UPDATE fb_actions
SET status=?, answer_json=?, updated_at=?
WHERE id=? AND status=?`, string(ActionAnswered), string(b), now, id, string(ActionPending))
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNotPending
	}
	return s.Get(ctx, id)
}

func (s *ActionService) Approve(ctx context.Context, id string, reason string) (*Action, error) {
	return s.ApproveWithAnswer(ctx, id, reason, "")
}

// ApproveWithAnswer atomically approves an action and records a structured
// approval response. request_permissions uses answerJSON for the client-granted
// subset and scope; ordinary approvals pass an empty answer.
func (s *ActionService) ApproveWithAnswer(ctx context.Context, id, reason, answerJSON string) (*Action, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrActionNotFound
	}
	now := time.Now().Unix()
	res, err := s.DB.ExecContext(ctx, `
UPDATE fb_actions
SET status=?, answer_json=?, error=?, updated_at=?
WHERE id=? AND status=?`, string(ActionApproved), strings.TrimSpace(answerJSON), strings.TrimSpace(reason), now, id, string(ActionPending))
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	_ = err
	if n == 0 {
		return nil, ErrNotPending
	}
	return s.Get(ctx, id)
}

func (s *ActionService) Deny(ctx context.Context, id string, reason string) (*Action, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrActionNotFound
	}
	now := time.Now().Unix()
	res, err := s.DB.ExecContext(ctx, `
UPDATE fb_actions
SET status=?, error=?, updated_at=?
WHERE id=? AND status=?`, string(ActionDenied), strings.TrimSpace(reason), now, id, string(ActionPending))
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	_ = err
	if n == 0 {
		return nil, ErrNotPending
	}
	return s.Get(ctx, id)
}

// DenyWithAnswer atomically denies an action and records a structured marker
// alongside the denial reason. The plan-review delivery uses it to stamp the
// denial as engine-closed: the reason column stays the display contract, the
// answer column carries the machine discriminator a manual denial cannot set.
func (s *ActionService) DenyWithAnswer(ctx context.Context, id, reason, answerJSON string) (*Action, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrActionNotFound
	}
	now := time.Now().Unix()
	res, err := s.DB.ExecContext(ctx, `
UPDATE fb_actions
SET status=?, error=?, answer_json=?, updated_at=?
WHERE id=? AND status=?`, string(ActionDenied), strings.TrimSpace(reason), strings.TrimSpace(answerJSON), now, id, string(ActionPending))
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	_ = err
	if n == 0 {
		return nil, ErrNotPending
	}
	return s.Get(ctx, id)
}

func (s *ActionService) Cancel(ctx context.Context, id string, reason string) (*Action, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrActionNotFound
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "cancelled"
	}
	now := time.Now().Unix()
	res, err := s.DB.ExecContext(ctx, `
UPDATE fb_actions
SET status=?, error=?, updated_at=?
WHERE id=? AND status=?`, string(ActionCancelled), reason, now, id, string(ActionPending))
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	_ = err
	if n == 0 {
		return nil, ErrNotPending
	}
	return s.Get(ctx, id)
}

// ExpirePending atomically expires every pending action whose CreatedAt is
// older than (now - ttl) and returns the IDs that were transitioned. Callers
// should use the returned IDs to clear any associated run waits and notify
// downstream surfaces. A ttl <= 0 is a no-op and returns nil.
func (s *ActionService) ExpirePending(ctx context.Context, ttl time.Duration, reason string) ([]string, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	if ttl <= 0 {
		return nil, nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "approval_ttl_expired"
	}
	cutoff := time.Now().Add(-ttl).Unix()
	rows, err := s.DB.QueryContext(ctx, `
UPDATE fb_actions
SET status=?, error=?, updated_at=?
WHERE status=? AND created_at<?
RETURNING id`, string(ActionExpired), reason, time.Now().Unix(), string(ActionPending), cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var expired []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		expired = append(expired, id)
	}
	return expired, rows.Err()
}

func (s *ActionService) ExpireIfPending(ctx context.Context, id, reason string) (bool, error) {
	if s == nil || s.DB == nil {
		return false, fmt.Errorf("nil db")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false, ErrActionNotFound
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "approval_ttl_expired"
	}
	now := time.Now().Unix()
	res, err := s.DB.ExecContext(ctx, `
UPDATE fb_actions
SET status=?, error=?, updated_at=?
WHERE id=? AND status=?`, string(ActionExpired), reason, now, id, string(ActionPending))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *ActionService) DenyIfPending(ctx context.Context, id, reason string) (bool, error) {
	if s == nil || s.DB == nil {
		return false, fmt.Errorf("nil db")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false, ErrActionNotFound
	}
	now := time.Now().Unix()
	res, err := s.DB.ExecContext(ctx, `
UPDATE fb_actions
SET status=?, error=?, updated_at=?
WHERE id=? AND status=?`, string(ActionDenied), strings.TrimSpace(reason), now, id, string(ActionPending))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	_ = err
	return n > 0, nil
}

type ActionStatus string

const (
	ActionPending   ActionStatus = "pending"
	ActionAnswered  ActionStatus = "answered"
	ActionApproved  ActionStatus = "approved"
	ActionDenied    ActionStatus = "denied"
	ActionCancelled ActionStatus = "cancelled"
	ActionExpired   ActionStatus = "expired"
	ActionError     ActionStatus = "error"
)

type Action struct {
	ID          string       `json:"id"`
	SessionID   string       `json:"session_id"`
	Kind        string       `json:"kind"`
	Status      ActionStatus `json:"status"`
	PayloadJSON string       `json:"payload_json"`
	AnswerJSON  string       `json:"answer_json,omitempty"`
	Error       string       `json:"error,omitempty"`
	CreatedAt   int64        `json:"created_at"`
	UpdatedAt   int64        `json:"updated_at"`
}

type AskForm struct {
	Kind         string        `json:"kind,omitempty"`
	Title        string        `json:"title,omitempty"`
	SessionID    string        `json:"session_id,omitempty"`
	AgentID      string        `json:"agent_id,omitempty"`
	SubagentType string        `json:"subagent_type,omitempty"`
	Questions    []AskQuestion `json:"questions"`
}

type AskQuestion struct {
	ID            string      `json:"id"`
	Prompt        string      `json:"prompt"`
	Options       []AskOption `json:"options"`
	AllowMultiple bool        `json:"allow_multiple,omitempty"`
	AllowOther    bool        `json:"allow_other,omitempty"`
}

type AskOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Preview     string `json:"preview,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

type AskAnswer struct {
	Answers []AskAnswerItem `json:"answers"`
}

type AskAnswerItem struct {
	QuestionID string   `json:"question_id"`
	OptionIDs  []string `json:"option_ids,omitempty"`
	OtherText  string   `json:"other_text,omitempty"`
}

func AskAnswerSelectFirstOptionEachQuestion(payloadJSON string) (AskAnswer, error) {
	var form AskForm
	if err := json.Unmarshal([]byte(strings.TrimSpace(payloadJSON)), &form); err != nil {
		return AskAnswer{}, err
	}
	out := AskAnswer{Answers: make([]AskAnswerItem, 0, len(form.Questions))}
	for _, q := range form.Questions {
		if strings.TrimSpace(q.ID) == "" || len(q.Options) == 0 {
			continue
		}
		opt0 := q.Options[0]
		if strings.TrimSpace(opt0.ID) == "" {
			continue
		}
		out.Answers = append(out.Answers, AskAnswerItem{
			QuestionID: strings.TrimSpace(q.ID),
			OptionIDs:  []string{strings.TrimSpace(opt0.ID)},
		})
	}
	if len(out.Answers) == 0 {
		return AskAnswer{}, fmt.Errorf("no answerable questions in ask form")
	}
	return out, nil
}

// ExpireSweeperConfig configures the background TTL sweeper. A TTL <= 0 or a
// nil ActionService disables the sweeper and StartExpireSweeper returns a no-op
// stop function.
type ExpireSweeperConfig struct {
	TTL      time.Duration
	Interval time.Duration
	Reason   string
	// OnExpired is called for every action ID transitioned from pending to
	// expired. Implementations should clear associated run waits and notify
	// downstream surfaces. Errors are logged but do not stop the sweeper.
	OnExpired func(ctx context.Context, actionID string)
	// Logger receives diagnostic messages. When nil, slog.Default is used.
	Logger *slog.Logger
}

// StartExpireSweeper launches a goroutine that periodically expires pending
// actions older than cfg.TTL using svc.ExpirePending and invokes
// cfg.OnExpired for each transitioned action. The returned stop function
// blocks until the goroutine has exited.
func StartExpireSweeper(ctx context.Context, svc *ActionService, cfg ExpireSweeperConfig) func() {
	if svc == nil || svc.DB == nil || cfg.TTL <= 0 {
		return func() {}
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = cfg.TTL / 4
		if interval < time.Second {
			interval = time.Second
		}
		if interval > 30*time.Second {
			interval = 30 * time.Second
		}
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	swCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-swCtx.Done():
				return
			case <-ticker.C:
				ids, err := svc.ExpirePending(swCtx, cfg.TTL, cfg.Reason)
				if err != nil {
					log.Error("approval ttl sweep", "err", err)
					continue
				}
				for _, id := range ids {
					if cfg.OnExpired != nil {
						cfg.OnExpired(swCtx, id)
					}
				}
			}
		}
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}
