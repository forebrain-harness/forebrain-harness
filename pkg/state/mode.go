package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Mode string

const (
	ModeAgent Mode = "agent"
	ModePlan  Mode = "plan"
)

func CoerceMode(raw string) Mode {
	return normalizeMode(Mode(strings.TrimSpace(raw)))
}

type State struct {
	Mode          Mode   `json:"mode"`
	Phase         string `json:"phase,omitempty"`
	PrePlanMode   Mode   `json:"pre_plan_mode,omitempty"`
	PlanTurnCount int    `json:"plan_turn_count,omitempty"`
	// HasExitedPlan is set true when exit_plan_mode runs, and cleared when
	// enter_plan_mode runs. It lets enter_plan_mode detect a re-entry (user
	// previously exited plan mode and is coming back) and guide the LLM to
	// evaluate the existing plan file rather than blindly overwriting it.
	HasExitedPlan bool  `json:"has_exited_plan,omitempty"`
	UpdatedAt     int64 `json:"updated_at,omitempty"`
}

func modePath(home, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "default"
	}
	return filepath.Join(home, "state", "modes", sessionID+".json")
}

func Get(home, sessionID string) (State, error) {
	p := modePath(home, sessionID)
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return State{Mode: ModeAgent}, nil
		}
		return State{}, err
	}
	var st State
	if json.Unmarshal(b, &st) != nil {
		return State{Mode: ModeAgent}, nil
	}
	if st.Mode == "" {
		st.Mode = ModeAgent
	}
	st.Mode = normalizeMode(st.Mode)
	return st, nil
}

func Set(home, sessionID string, st State) error {
	p := modePath(home, sessionID)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	st.UpdatedAt = time.Now().Unix()
	if st.Mode == "" {
		st.Mode = ModeAgent
	}
	st.Mode = normalizeMode(st.Mode)
	if strings.TrimSpace(string(st.PrePlanMode)) != "" {
		st.PrePlanMode = normalizeMode(st.PrePlanMode)
	}
	b, err := json.MarshalIndent(st, "", "  ")
	_ = err
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

// Switch changes a session's mode while preserving the bookkeeping the
// plan-mode flow depends on, and returns the stored state.
//
// Every surface that flips the mode — the TUI hotkey, /plan, /agent, the
// gateway, an approval landing — must use this rather than Set with a freshly
// built State. Set overwrites the whole record, so those callers were erasing
// PrePlanMode (the mode exit_plan_mode restores) and HasExitedPlan (what makes
// the next enter_plan_mode recognize a re-entry and re-read the existing plan
// instead of overwriting it).
//
// Entering plan mode stashes the mode being left and clears the exited flag;
// leaving plan mode restores the phase and records that the session has exited,
// so leaving by hotkey and leaving by exit_plan_mode are the same transition.
func Switch(home, sessionID string, mode Mode) (State, error) {
	cur, err := Get(home, sessionID)
	if err != nil {
		cur = State{Mode: ModeAgent}
	}
	next := cur
	next.Mode = normalizeMode(mode)
	next.PlanTurnCount = 0
	if next.Mode == ModePlan {
		next.Phase = "plan"
		if cur.Mode != ModePlan {
			next.PrePlanMode = normalizeMode(cur.Mode)
		}
		next.HasExitedPlan = false
	} else {
		next.Phase = ""
		next.PrePlanMode = ""
		if cur.Mode == ModePlan {
			next.HasExitedPlan = true
		}
	}
	return next, Set(home, sessionID, next)
}

func normalizeMode(m Mode) Mode {
	switch strings.ToLower(strings.TrimSpace(string(m))) {
	case string(ModePlan):
		return ModePlan
	default:
		return ModeAgent
	}
}

// --- per-session /fast toggle --------------------------------------------
//
// This file persists per-session /fast toggle state. It is shaped like the
// mode store above: one JSON file per session under
// $FOREBRAIN_HOME/state/fast/<session>.json. Used by the /fast slash command and
// by the Anthropic agent LLM to decide service_tier=auto on outbound requests.

type FastState struct {
	Enabled   bool  `json:"enabled"`
	UpdatedAt int64 `json:"updated_at,omitempty"`
}

func fastPath(home, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "default"
	}
	return filepath.Join(home, "state", "fast", sessionID+".json")
}

// Get returns the persisted state or a zero FastState (Enabled=false) if absent.
// Read errors other than not-exist propagate.
func Fast(home, sessionID string) (FastState, error) {
	p := fastPath(home, sessionID)
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return FastState{}, nil
		}
		return FastState{}, err
	}
	var st FastState
	if json.Unmarshal(b, &st) != nil {
		return FastState{}, nil
	}
	return st, nil
}

func SetFast(home, sessionID string, enabled bool) error {
	p := fastPath(home, sessionID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	st := FastState{Enabled: enabled, UpdatedAt: time.Now().Unix()}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	// Write to a temp file then rename so a crash mid-write cannot truncate
	// the canonical file to zero bytes (Get returns zero-state silently on
	// unmarshal failure, which would lose the user's toggle without warning).
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// RemoveSessionStateFiles deletes the files a session keeps under one agent's
// state root, all named for the session id:
//
//	state/modes/<sid>.json          the session's mode and plan-mode bookkeeping
//	state/fast/<sid>.json           the session's /fast toggle
//	state/todos/<sid>.json          the session's checklist
//	state/intermediate/<sid>.md     the session's intermediate notes
//
// A file that does not exist is not an error: deletion must be repeatable
// over a session that never wrote some of them. Anything new that lands here
// must be added to this list or deleting a session leaves it orphaned.
//
// A session's plan directory (plans/<project>/<session>/, see
// PlanDirForSession) is deliberately not on this list: plans are kept as
// history after the conversation that wrote them is gone (owner decision
// 2026-10-09).
func RemoveSessionStateFiles(stateRoot, sessionID string) error {
	for _, p := range []string{
		modePath(stateRoot, sessionID),
		fastPath(stateRoot, sessionID),
		todoPath(stateRoot, sessionID),
		intermediatePath(stateRoot, sessionID),
	} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
