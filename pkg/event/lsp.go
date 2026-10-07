// Language-server data shared by the lsp runtime, the tools and the surfaces.
// Pure data: the behavior lives in pkg/lsp behind the ports in pkg/tool.
package event

// RunEventLSPRecommendation is the run event that carries an LSPRecommendation
// to the session's surfaces.
const RunEventLSPRecommendation = "lsp_recommendation"

// LSPServerState is one server's state as the surfaces show it.
type LSPServerState string

const (
	LSPStateNotInstalled LSPServerState = "not_installed"
	LSPStateAvailable    LSPServerState = "available" // installed, not enabled
	LSPStateBlocked      LSPServerState = "blocked"   // unusable configuration or project entry awaiting consent; see Note
	LSPStateStopped      LSPServerState = "stopped"   // enabled, no instance running
	LSPStateStarting     LSPServerState = "starting"
	LSPStateIndexing     LSPServerState = "indexing"
	LSPStateReady        LSPServerState = "ready"
	LSPStateFailed       LSPServerState = "failed"
)

// LSPServerStatus is one configured server in an LSPSnapshot.
type LSPServerStatus struct {
	ID              string         `json:"id"`
	DisplayName     string         `json:"display_name"`
	Languages       []string       `json:"languages,omitempty"`
	Role            string         `json:"role"`
	Scope           string         `json:"scope"` // "catalog" | "global" | "project"
	Enabled         bool           `json:"enabled"`
	State           LSPServerState `json:"state"`
	Command         string         `json:"command,omitempty"`
	BinaryPath      string         `json:"binary_path,omitempty"`
	Version         string         `json:"version,omitempty"`
	InstallCommand  string         `json:"install_command,omitempty"`
	Roots           []string       `json:"roots,omitempty"`
	PIDs            []int          `json:"pids,omitempty"`
	OpenDocuments   int            `json:"open_documents,omitempty"`
	Errors          int            `json:"errors,omitempty"`
	Warnings        int            `json:"warnings,omitempty"`
	IndexingPercent int            `json:"indexing_percent,omitempty"`
	LastError       string         `json:"last_error,omitempty"`
	LogPath         string         `json:"log_path,omitempty"`
	Note            string         `json:"note,omitempty"`
	ProjectWrites   []string       `json:"project_writes,omitempty"`
	// An install started from any surface: running, its last output lines,
	// and the failure text once it failed.
	Installing   bool     `json:"installing,omitempty"`
	InstallLog   []string `json:"install_log,omitempty"`
	InstallError string   `json:"install_error,omitempty"`
}

// LSPSnapshot is everything /lsp shows for one runner.
type LSPSnapshot struct {
	ProjectRoot                   string `json:"project_root,omitempty"`
	Trusted                       bool   `json:"trusted"`
	FeatureEnabled                bool   `json:"feature_enabled"`
	ToolRegistered                bool   `json:"tool_registered"`
	RecommendationsDisabled       bool   `json:"recommendations_disabled"`
	RecommendationsDisabledReason string `json:"recommendations_disabled_reason,omitempty"`
	// ProjectNotes says why .forebrain/lsp_servers.yaml, or an entry of it,
	// was ignored.
	ProjectNotes []string          `json:"project_notes,omitempty"`
	Servers      []LSPServerStatus `json:"servers"`
}

// LSPRecommendation offers to enable (or install and enable) one server.
type LSPRecommendation struct {
	ID               string   `json:"id"`
	ServerID         string   `json:"server_id"`
	DisplayName      string   `json:"display_name"`
	Languages        []string `json:"languages,omitempty"`
	TriggerExtension string   `json:"trigger_extension"`
	Mode             string   `json:"mode"` // "enable" | "install"
	BinaryPath       string   `json:"binary_path,omitempty"`
	Version          string   `json:"version,omitempty"`
	InstallCommand   string   `json:"install_command,omitempty"`
}

// LSPRecommendationChoice is the user's answer to an LSPRecommendation.
type LSPRecommendationChoice string

const (
	LSPChoiceEnable     LSPRecommendationChoice = "enable"
	LSPChoiceInstall    LSPRecommendationChoice = "install"
	LSPChoiceNotNow     LSPRecommendationChoice = "not_now"
	LSPChoiceNever      LSPRecommendationChoice = "never"
	LSPChoiceDisableAll LSPRecommendationChoice = "disable_all"
)

// Valid reports whether c is one of the five choices.
func (c LSPRecommendationChoice) Valid() bool {
	switch c {
	case LSPChoiceEnable, LSPChoiceInstall, LSPChoiceNotNow, LSPChoiceNever, LSPChoiceDisableAll:
		return true
	}
	return false
}

// LSPDiagnostic is one problem, positioned for people: 1-based line and
// column, path relative to the project root with forward slashes.
type LSPDiagnostic struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Severity string `json:"severity"` // "error" | "warning" | "info" | "hint"
	Source   string `json:"source,omitempty"`
	Code     string `json:"code,omitempty"`
	Message  string `json:"message"`
}

// LSPDiagnosticsSummary is what an edit tool reports under "lsp_diagnostics".
type LSPDiagnosticsSummary struct {
	New                 int             `json:"new"`
	Files               int             `json:"files"`
	Servers             []string        `json:"servers,omitempty"`
	Items               []LSPDiagnostic `json:"items,omitempty"`
	PendingFiles        []string        `json:"pending_files,omitempty"`
	BaselineUnavailable bool            `json:"baseline_unavailable,omitempty"`
}
