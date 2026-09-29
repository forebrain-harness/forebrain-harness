package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type TodoStatus string

const (
	StatusPending    TodoStatus = "pending"
	StatusInProgress TodoStatus = "in_progress"
	StatusCompleted  TodoStatus = "completed"
	StatusCancelled  TodoStatus = "cancelled"
)

type Item struct {
	ID      string     `json:"id"`
	Content string     `json:"content"`
	Status  TodoStatus `json:"status"`
	// Title is the present-tense label shown while the item is in progress
	// ("Writing tests"), as opposed to Content, the checklist row.
	Title     string `json:"title,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
}

// UnmarshalJSON reads the title under its former name, active_form, so a
// checklist stored before the rename keeps its labels.
func (it *Item) UnmarshalJSON(b []byte) error {
	type plain Item
	aux := struct {
		*plain
		LegacyActiveForm string `json:"active_form"`
	}{plain: (*plain)(it)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	if it.Title == "" {
		it.Title = aux.LegacyActiveForm
	}
	return nil
}

type List struct {
	Items []Item `json:"items"`
}

func todoPath(home, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "default"
	}
	return filepath.Join(home, "state", "todos", sessionID+".json")
}

func Load(home, sessionID string) (List, error) {
	p := todoPath(home, sessionID)
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return List{Items: []Item{}}, nil
		}
		return List{Items: []Item{}}, err
	}
	var out List
	if err := json.Unmarshal(b, &out); err != nil {
		return List{Items: []Item{}}, err
	}
	if out.Items == nil {
		out.Items = []Item{}
	}
	return out, nil
}

func Save(home, sessionID string, l List) error {
	p := todoPath(home, sessionID)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	b, _ := json.MarshalIndent(l, "", "  ")
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

// Replace writes items as the complete checklist for the session: the rows are
// stored in the order given and any row that is not in items disappears.
//
// The checklist is full state, not a delta. A merge-by-id write cannot express
// "this item is no longer part of the plan", so every superseded row survived
// forever: when the model rewrote its plan with fresh ids the previous
// generation stayed pinned at pending/in_progress, and the progress UI showed
// two plans at once with two active items.
func Replace(home, sessionID string, items []Item) (List, error) {
	now := time.Now().Unix()
	out := List{Items: make([]Item, 0, len(items))}
	idx := make(map[string]int, len(items))
	for _, it := range items {
		it.ID = strings.TrimSpace(it.ID)
		if it.ID == "" {
			continue
		}
		if it.Status == "" {
			it.Status = StatusPending
		}
		it.UpdatedAt = now
		if j, ok := idx[it.ID]; ok {
			out.Items[j] = it
			continue
		}
		idx[it.ID] = len(out.Items)
		out.Items = append(out.Items, it)
	}
	return out, Save(home, sessionID, out)
}
