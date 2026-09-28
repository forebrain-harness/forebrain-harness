package event

import "time"

type TaskKind string

const (
	KindJob      TaskKind = "job"
	KindWorkItem TaskKind = "work_item"
	KindSubagent TaskKind = "subagent"
)

type TaskState string

const (
	StateQueued    TaskState = "queued"
	StateRunning   TaskState = "running"
	StateDone      TaskState = "done"
	StateFailed    TaskState = "failed"
	StateCancelled TaskState = "cancelled"
)

type Task struct {
	ID        string    `json:"id"`
	Kind      TaskKind  `json:"kind"`
	State     TaskState `json:"state"`
	SessionID string    `json:"session_id,omitempty"`
	ChannelID string    `json:"channel_id,omitempty"`
	Title     string    `json:"title,omitempty"`
	Prompt    string    `json:"prompt,omitempty"`
	Result    string    `json:"result,omitempty"`
	Error     string    `json:"error,omitempty"`
	Progress  float64   `json:"progress"`
	LogPath   string    `json:"log_path,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type EventKind string

const (
	EventStarted         EventKind = "started"
	EventProgress        EventKind = "progress"
	EventWaitingApproval EventKind = "waiting_approval"
	EventDone            EventKind = "done"
	EventFailed          EventKind = "failed"
)

type TaskEvent struct {
	EventKind EventKind `json:"event"`
	Task      Task      `json:"task"`
	Message   string    `json:"message,omitempty"`
}
