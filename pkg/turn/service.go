package turn

import (
	"context"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

type RunEventStore interface {
	GetRun(ctx context.Context, id string) (*state.Run, error)
	ListRunEvents(ctx context.Context, runID string, limit int) ([]state.SessionEvent, error)
	ListRunsBySession(ctx context.Context, sessionID string, limit int) ([]state.Run, error)
}

type ChildRunStore interface {
	ListChildRuns(ctx context.Context, parentRunID string, limit int, statuses ...state.RunStatus) ([]state.Run, error)
}

type SessionRepository interface {
	Ensure(ctx context.Context, id string, title string) error
	SetTitle(ctx context.Context, id string, title string) error
	ListSessionsRecent(ctx context.Context, limit int) ([]state.SessionSummary, error)
	ListRecentMessages(ctx context.Context, sessionID string, limit int) ([]state.Message, error)
}

type PermissionFacade interface {
	PermissionSnapshot() safety.Snapshot
	PermissionSnapshotForSession(sessionID string) safety.Snapshot
	EvaluatePermission(toolName, input string) safety.Decision
	EvaluatePermissionForSession(sessionID, toolName, input string) safety.Decision
	ExplainPermission(toolName, input string) safety.ExplainResult
	ExplainPermissionForSession(sessionID, toolName, input string) safety.ExplainResult
	ApplyPermissionUpdate(update safety.PermissionUpdate)
}

type Service struct {
	workspaceRoot      string
	runEventStore      RunEventStore
	childRunStore      ChildRunStore
	sessionStore       SessionRepository
	permissionFacade   PermissionFacade
	inputHistoryReader InputHistoryReader
	runner             RunExecutor
	locker             ForegroundLocker
	sessionSource      string
	approval           ApprovalGate
	commands           CommandService
	chatgptModels      ChatGPTModelsSource
}

type Option func(*Service)

// WithWorkspaceRoot sets the active primary agent's workspace directory. The
// per-agent stores this service reads (currently the subagent ledger) hang off
// it, so passing FOREBRAIN_HOME here would serve one agent's history to every
// agent — and would miss the ledger the runner actually writes.
func WithWorkspaceRoot(root string) Option {
	return func(s *Service) {
		if s == nil {
			return
		}
		s.workspaceRoot = root
	}
}

func WithRunEventStore(store RunEventStore) Option {
	return func(s *Service) {
		if s == nil {
			return
		}
		s.runEventStore = store
	}
}

func WithChildRunStore(store ChildRunStore) Option {
	return func(s *Service) {
		if s == nil {
			return
		}
		s.childRunStore = store
	}
}

func WithSessionStore(store SessionRepository) Option {
	return func(s *Service) {
		if s == nil {
			return
		}
		s.sessionStore = store
	}
}

// WithChatGPTModels installs the ChatGPT subscription model source used by
// live model-catalog listings. Without it, listings answer from the static
// catalog and report ChatGPT as unconfigured.
func WithChatGPTModels(src ChatGPTModelsSource) Option {
	return func(s *Service) {
		if s == nil {
			return
		}
		s.chatgptModels = src
	}
}

func WithPermissionFacade(facade PermissionFacade) Option {
	return func(s *Service) {
		if s == nil {
			return
		}
		s.permissionFacade = facade
	}
}

// WithRunExecutor installs the agent-run port used by Submit.
func WithRunExecutor(runner RunExecutor) Option {
	return func(s *Service) {
		if s != nil {
			s.runner = runner
		}
	}
}

// SetRunExecutor installs the agent-run port after construction.
//
// A surface whose executor options close over the surface itself cannot pass
// WithRunExecutor to New, because the surface does not exist yet at that point.
// The TUI assigns a whole replacement Service for the same reason; the gateway
// hands its Server to the Core, so it needs to fill this one field in.
func (s *Service) SetRunExecutor(runner RunExecutor) {
	if s == nil {
		return
	}
	s.runner = runner
}

// WithForegroundLocker installs the per-session foreground lock port.
func WithForegroundLocker(locker ForegroundLocker) Option {
	return func(s *Service) {
		if s != nil {
			s.locker = locker
		}
	}
}

// WithSessionSource sets the ID source used for new sessions.
func WithSessionSource(source string) Option {
	return func(s *Service) {
		if s != nil {
			s.sessionSource = source
		}
	}
}

// WithApprovalGate installs the pending-approval gate port.
func WithApprovalGate(gate ApprovalGate) Option {
	return func(s *Service) {
		if s != nil {
			s.approval = gate
		}
	}
}

func New(opts ...Option) *Service {
	s := &Service{}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}
