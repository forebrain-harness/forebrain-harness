package assembly

import "time"

type Layer string

const (
	LayerSystem    Layer = "system"
	LayerWorkspace Layer = "workspace"
	LayerSession   Layer = "session"
	LayerWorking   Layer = "working_set"
	LayerEvidence  Layer = "evidence"
)

type ContextItem struct {
	SourceID        string    `json:"source_id"`
	Layer           Layer     `json:"layer"`
	Title           string    `json:"title"`
	Content         string    `json:"content"`
	Priority        int       `json:"priority"`
	EstimatedTokens int       `json:"estimated_tokens"`
	Pinned          bool      `json:"pinned,omitempty"`
	TTLSeconds      int       `json:"ttl_seconds,omitempty"`
	LastUsedAt      time.Time `json:"last_used_at,omitempty"`
	DebugReason     string    `json:"debug_reason,omitempty"`
}

type ContextBudget struct {
	LimitTokens int `json:"limit_tokens"`
	UsedTokens  int `json:"used_tokens"`
	TotalItems  int `json:"total_items"`
	UsedItems   int `json:"used_items"`
}

type ContextProvenance struct {
	SourceID        string `json:"source_id"`
	Included        bool   `json:"included"`
	Reason          string `json:"reason"`
	EstimatedTokens int    `json:"estimated_tokens"`
}

type AssemblyRequest struct {
	SessionID    string
	RunID        string
	Channel      string
	RunKind      string
	AgentID      string
	Mode         string
	Query        string
	PinnedSet    []string
	LimitTokens  int
	MaxItems     int
	Now          time.Time
	IncludeEmpty bool
}

type AssemblyResult struct {
	SessionID       string              `json:"session_id"`
	Mode            string              `json:"mode"`
	WorkingSet      []string            `json:"working_set"`
	Budget          ContextBudget       `json:"budget"`
	Items           []ContextItem       `json:"items"`
	Provenance      []ContextProvenance `json:"provenance"`
	EvictionDetails []ContextProvenance `json:"eviction_details,omitempty"`
	GeneratedAtUTC  string              `json:"generated_at_utc"`
	// ConversationTokens is how much of the window the conversation already
	// fills: the whole prompt the model last reported plus its output, and a
	// local estimate of whatever was appended since — or of the whole
	// conversation before any usage was reported. It is the input the
	// composer footer's gauge reads.
	ConversationTokens     int    `json:"conversation_tokens,omitempty"`
	ProjectedContextTokens int    `json:"projected_context_tokens,omitempty"`
	ModelContextTokens     int    `json:"model_context_tokens,omitempty"`
	EffectiveInputTokens   int    `json:"effective_input_tokens,omitempty"`
	MaxOutputTokens        int    `json:"max_output_tokens,omitempty"`
	RemainingContextTokens int    `json:"remaining_context_tokens,omitempty"`
	ContextPressure        string `json:"context_pressure,omitempty"`
}

type Source interface {
	ID() string
	Collect(req AssemblyRequest) ([]ContextItem, error)
}
