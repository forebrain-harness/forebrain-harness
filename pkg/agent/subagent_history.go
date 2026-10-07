package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Status string

const (
	StatusRunning   Status = "running"
	StatusOK        Status = "ok"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

type HistoryEntry struct {
	AgentID          string `json:"agent_id,omitempty"`
	AgentKind        string `json:"agent_kind,omitempty"`
	TaskID           string `json:"task_id,omitempty"`
	RunID            string `json:"run_id,omitempty"`
	ParentRunID      string `json:"parent_run_id,omitempty"`
	SessionID        string `json:"session_id,omitempty"`
	WorkerSessionID  string `json:"worker_session_id,omitempty"`
	ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
	TaskIndex        int    `json:"task_index"`
	ExecutionID      string `json:"execution_id,omitempty"`
	QuerySource      string `json:"query_source,omitempty"`
	// Title is the short name the dispatching agent gave this task. It is what
	// a roster row and a task card show; Task is the whole instruction, which
	// only the subagent's own view prints in full.
	Title       string `json:"title,omitempty"`
	Task        string `json:"task,omitempty"`
	Status      Status `json:"status,omitempty"`
	Output      string `json:"output,omitempty"`
	Error       string `json:"error,omitempty"`
	StartedAt   int64  `json:"started_at,omitempty"`
	UpdatedAt   int64  `json:"updated_at,omitempty"`
	FinishedAt  int64  `json:"finished_at,omitempty"`
	AgentType   string `json:"agent_type,omitempty"`
	RuntimeKind string `json:"runtime_kind,omitempty"`
	OneShot     bool   `json:"one_shot,omitempty"`
	Continuable bool   `json:"continuable,omitempty"`
	DefSource   string `json:"definition_source,omitempty"`
	// ModelProvider and Model are the model a dispatch-time override put
	// this agent on; empty when it runs on its definition's or the
	// conversation's model.
	ModelProvider string `json:"model_provider,omitempty"`
	Model         string `json:"model,omitempty"`
}

type Query struct {
	SessionID   string
	ParentRunID string
	TaskID      string
	RunID       string
	// Limit < 0 requests every matching record. Limit == 0 keeps the small
	// interactive default; callers that rebuild a persisted UI must use the
	// unbounded form (or their own cursor) rather than silently truncating it.
	Limit int
}

// historyPath locates one agent's subagent ledger. workspaceRoot must be the
// owning primary agent's workspace directory, never the shared FOREBRAIN_HOME:
// every entry carries the delegated task text and the subagent's output
// verbatim, so a home-rooted ledger would let any agent read what every other
// agent delegated. Writer and readers must agree on this root or listing
// silently returns nothing.
func historyPath(workspaceRoot string) string {
	return filepath.Join(strings.TrimSpace(workspaceRoot), "state", "subagent-history.jsonl")
}

func AppendHistory(workspaceRoot string, entry HistoryEntry) error {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return fmt.Errorf("empty workspace root")
	}
	// A relative root is a caller that resolved against an unset home; writing
	// would scatter a ledger into whatever the process cwd happens to be.
	if !filepath.IsAbs(workspaceRoot) {
		return fmt.Errorf("workspace root must be absolute, got %q", workspaceRoot)
	}
	entry = normalizeEntry(entry)
	dir := filepath.Dir(historyPath(workspaceRoot))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(historyPath(workspaceRoot), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

func ListHistory(workspaceRoot string, q Query) ([]HistoryEntry, error) {
	// Mirrors the writer: an unresolved root has no ledger, and reading the
	// relative path would pick up whatever sits under the process cwd.
	if !filepath.IsAbs(strings.TrimSpace(workspaceRoot)) {
		return []HistoryEntry{}, nil
	}
	raw, err := os.ReadFile(historyPath(workspaceRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return []HistoryEntry{}, nil
		}
		return nil, err
	}
	limit := q.Limit
	unbounded := limit < 0
	if limit == 0 {
		limit = 20
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	capacity := limit
	if unbounded {
		capacity = 64
	}
	out := make([]HistoryEntry, 0, capacity)
	seen := make(map[string]struct{}, capacity)
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var entry HistoryEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entry = normalizeEntry(entry)
		if !matchesQuery(entry, q) {
			continue
		}
		key := entryKey(entry)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, entry)
		if !unbounded && len(out) >= limit {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			if out[i].FinishedAt == out[j].FinishedAt {
				return out[i].StartedAt > out[j].StartedAt
			}
			return out[i].FinishedAt > out[j].FinishedAt
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, nil
}

func ListMerged(workspaceRoot string, q Query) ([]HistoryEntry, error) {
	live := RegistryFor(workspaceRoot).List(q)
	history, err := ListHistory(workspaceRoot, q)
	if err != nil {
		return nil, err
	}
	limit := q.Limit
	unbounded := limit < 0
	if limit == 0 {
		limit = 20
	}
	merged := make(map[string]HistoryEntry, len(live)+len(history))
	for _, entry := range history {
		merged[entryKey(entry)] = normalizeEntry(entry)
	}
	for _, entry := range live {
		key := entryKey(entry)
		cur, ok := merged[key]
		if !ok || entry.UpdatedAt >= cur.UpdatedAt {
			merged[key] = normalizeEntry(entry)
		}
	}
	out := make([]HistoryEntry, 0, len(merged))
	for _, entry := range merged {
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].StartedAt > out[j].StartedAt
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	if !unbounded && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func GetMerged(workspaceRoot string, q Query) (HistoryEntry, bool, error) {
	if strings.TrimSpace(q.TaskID) == "" && strings.TrimSpace(q.RunID) == "" {
		return HistoryEntry{}, false, nil
	}
	list, err := ListMerged(workspaceRoot, q)
	if err != nil {
		return HistoryEntry{}, false, err
	}
	if len(list) == 0 {
		return HistoryEntry{}, false, nil
	}
	return list[0], true, nil
}

func normalizeEntry(entry HistoryEntry) HistoryEntry {
	entry.AgentID = strings.TrimSpace(entry.AgentID)
	entry.AgentKind = strings.TrimSpace(entry.AgentKind)
	entry.TaskID = strings.TrimSpace(entry.TaskID)
	entry.RunID = strings.TrimSpace(entry.RunID)
	entry.ParentRunID = strings.TrimSpace(entry.ParentRunID)
	entry.SessionID = strings.TrimSpace(entry.SessionID)
	entry.WorkerSessionID = strings.TrimSpace(entry.WorkerSessionID)
	entry.ParentToolCallID = strings.TrimSpace(entry.ParentToolCallID)
	entry.ExecutionID = strings.TrimSpace(entry.ExecutionID)
	entry.QuerySource = strings.TrimSpace(entry.QuerySource)
	entry.Task = strings.TrimSpace(entry.Task)
	entry.Output = strings.TrimSpace(entry.Output)
	entry.Error = strings.TrimSpace(entry.Error)
	entry.DefSource = strings.TrimSpace(entry.DefSource)
	if entry.AgentKind == "" {
		switch strings.TrimSpace(entry.RuntimeKind) {
		case "typed_subagent":
			entry.AgentKind = "typed"
		case "fork_subagent":
			entry.AgentKind = "fork"
		default:
			if strings.TrimSpace(entry.AgentType) == "fork" {
				entry.AgentKind = "fork"
			}
		}
	}
	if entry.UpdatedAt <= 0 {
		switch {
		case entry.FinishedAt > 0:
			entry.UpdatedAt = entry.FinishedAt
		case entry.StartedAt > 0:
			entry.UpdatedAt = entry.StartedAt
		}
	}
	if entry.StartedAt <= 0 && entry.UpdatedAt > 0 {
		entry.StartedAt = entry.UpdatedAt
	}
	return entry
}

// RosterKey is the identifier every surface keys a subagent on: its roster row,
// the per-agent transcript its frames are routed into, and the HookAgentID its
// nested tool calls are tagged with. It is the task id, falling back to the
// agent type for a dispatch that never got one, and deliberately NOT the
// internal uuid AgentID — a surface that keys on the uuid and a runtime that
// tags with the task id would file the same subagent under two identities, and
// its view would open empty.
//
// It takes the two fields rather than a HistoryEntry because the identity has
// to be derived identically from a live entry, from a persisted step payload
// replayed after a reload, and from a spawn notification.
func RosterKey(taskID, agentType string) string {
	if key := strings.TrimSpace(taskID); key != "" {
		return key
	}
	return strings.TrimSpace(agentType)
}

func entryKey(entry HistoryEntry) string {
	switch {
	case entry.TaskID != "":
		return "task:" + entry.TaskID
	case entry.RunID != "":
		return "run:" + entry.RunID
	default:
		return fmt.Sprintf("session:%s/task:%s/updated:%d", entry.SessionID, entry.Task, entry.UpdatedAt)
	}
}

func matchesQuery(entry HistoryEntry, q Query) bool {
	if sid := strings.TrimSpace(q.SessionID); sid != "" && entry.SessionID != sid {
		return false
	}
	if parent := strings.TrimSpace(q.ParentRunID); parent != "" && entry.ParentRunID != parent {
		return false
	}
	if taskID := strings.TrimSpace(q.TaskID); taskID != "" && entry.TaskID != taskID {
		return false
	}
	if runID := strings.TrimSpace(q.RunID); runID != "" && entry.RunID != runID {
		return false
	}
	return true
}
