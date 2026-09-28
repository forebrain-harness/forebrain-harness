package gateway

type callback struct {
	Kind       string `json:"kind"`
	JobID      string `json:"job_id,omitempty"`
	WorkItemID string `json:"work_item_id,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	ChannelID  string `json:"channel_id,omitempty"`
	Status     string `json:"status"`
	Title      string `json:"title,omitempty"`
	Message    string `json:"message,omitempty"`
}
