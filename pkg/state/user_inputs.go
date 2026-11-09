package state

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

// UserInput is one thing the person put into a conversation: a message they
// sent, or a form they answered — a user_interaction's answers, or an
// exit_plan_mode approval they sent back with their feedback.
type UserInput struct {
	// At is when it reached the conversation, in Unix seconds.
	At int64
	// Message is set for a message the person sent.
	Message *UserMessageRef
	// Action is set for an answered user_interaction or a denied
	// exit_plan_mode; its payload and answer carry what was asked and said.
	Action *Action
}

// ListUserInputs returns the person's side of a conversation, oldest first:
// every visible message they sent, every user_interaction they answered and
// every exit_plan_mode approval they denied. Only the messages are transcript
// rows — the forms' answers live on their actions — so a form decided inside
// a stretch a rewind took back is left out here, the way that stretch's
// messages are hidden.
func (s *SessionStore) ListUserInputs(ctx context.Context, sessionID string) ([]UserInput, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("list user inputs: session store unavailable")
	}
	sid := strings.TrimSpace(sessionID)
	messages, err := s.ListVisibleUserMessages(ctx, sid, 0)
	if err != nil {
		return nil, err
	}
	stretches, err := s.rewoundStretches(ctx, sid)
	if err != nil {
		return nil, err
	}
	actions := &ActionService{DB: s.db}
	var decided []Action
	for _, f := range []ActionFilter{
		{SessionID: sid, Kind: "user_interaction", Status: string(ActionAnswered)},
		{SessionID: sid, Kind: "exit_plan_mode", Status: string(ActionDenied)},
	} {
		list, err := actions.List(ctx, "", f, -1)
		if err != nil {
			return nil, err
		}
		for _, action := range list {
			if !insideRewoundStretch(action.UpdatedAt, stretches) {
				decided = append(decided, action)
			}
		}
	}
	// Forms read in the order they were decided, and two decided in the same
	// second in the order they were asked.
	sort.Slice(decided, func(i, j int) bool {
		if decided[i].UpdatedAt != decided[j].UpdatedAt {
			return decided[i].UpdatedAt < decided[j].UpdatedAt
		}
		return decided[i].CreatedAt < decided[j].CreatedAt
	})
	// Messages come newest first; merge both oldest first, a message ahead of
	// a form decided in the same second.
	slices.Reverse(messages)
	out := make([]UserInput, 0, len(messages)+len(decided))
	for len(messages) > 0 || len(decided) > 0 {
		if len(decided) == 0 || (len(messages) > 0 && messages[0].CreatedAt <= decided[0].UpdatedAt) {
			out = append(out, UserInput{At: messages[0].CreatedAt, Message: &messages[0]})
			messages = messages[1:]
			continue
		}
		out = append(out, UserInput{At: decided[0].UpdatedAt, Action: &decided[0]})
		decided = decided[1:]
	}
	return out, nil
}

// rewoundStretch is the part of a conversation one rewind discarded: from the
// anchor message it went back to, until the rewind itself.
type rewoundStretch struct {
	fromUnix  int64
	untilUnix int64
}

func (s *SessionStore) rewoundStretches(ctx context.Context, sessionID string) ([]rewoundStretch, error) {
	rows, err := s.db.QueryContext(ctx, rewoundStretchesSQL.For(dbx.DialectOf(s.db)), sessionID, messageRewound)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rewoundStretch
	for rows.Next() {
		var from, untilMs int64
		if err := rows.Scan(&from, &untilMs); err != nil {
			return nil, err
		}
		out = append(out, rewoundStretch{fromUnix: from, untilUnix: untilMs / 1000})
	}
	return out, rows.Err()
}

func insideRewoundStretch(at int64, stretches []rewoundStretch) bool {
	for _, stretch := range stretches {
		if at >= stretch.fromUnix && at <= stretch.untilUnix {
			return true
		}
	}
	return false
}
