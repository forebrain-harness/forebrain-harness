package assembly

import (
	"path/filepath"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/skill"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

type HookParams struct {
	Cfg           *appcfg.Root
	Home          string
	WorkspaceRoot string
	Store         *state.SessionStore
	RunRT         *state.RunStore
	SkillHub      *skill.Hub
	// PrimaryModel names the model one session's conversation runs on; it
	// takes the session id because a shared runner may serve sessions on
	// different models.
	PrimaryModel       func(sessionID string) (string, string)
	ReadStatesProvider func() []tool.ReadState
	SnapshotWriter     func(string, AssemblyResult)
	// ModelHit answers what a session's model may do, read from the configured
	// row it runs on (owner 2026-11-10): the catalog's entry with that row's
	// capabilities laid over it. nil leaves the catalog's values in force.
	ModelHit func(sessionID string) (llm.Hit, bool)
}

func NewRuntimeHook(params HookParams) *Hook {
	hook := NewHook(params.Cfg, params.Store)
	hook.Home = strings.TrimSpace(params.Home)
	hook.WorkspaceRoot = strings.TrimSpace(params.WorkspaceRoot)
	hook.RunRT = params.RunRT
	hook.SkillHub = params.SkillHub
	if params.Cfg != nil && params.PrimaryModel != nil {
		// The catalog is the one this build ships beside its binary; a build
		// that carries none leaves ModelLimitsProvider nil, and the assembly
		// then reads the windows as unknown rather than failing.
		if cat, err := llm.Resolve(); err == nil {
			hook.ModelLimitsProvider = func(sessionID string) (llm.Limits, bool) {
				// The session's row wins over the catalog: a model page's
				// context window applies to the row it was set on, and two rows
				// naming one provider/model keep their own (owner 2026-11-10).
				if params.ModelHit != nil {
					if hit, ok := params.ModelHit(sessionID); ok {
						return hit.Limits(), true
					}
				}
				provider, model := params.PrimaryModel(sessionID)
				return cat.Lookup(llm.JoinProviderModel(provider, model))
			}
		}
	}
	if params.PrimaryModel != nil {
		hook.EstimatorProvider = func(sessionID string) llm.Estimator {
			provider, model := params.PrimaryModel(sessionID)
			return llm.EstimatorFor(provider, model)
		}
	}
	hook.ModeProvider = StoredModeProvider(modeStateRoot(hook.Home, hook.WorkspaceRoot))
	hook.ReadStatesProvider = params.ReadStatesProvider
	hook.SnapshotWriter = params.SnapshotWriter
	return hook
}

// modeStateRoot resolves the per-agent state root onto which the mode store
// joins "state": the agent's workspace root, falling back to <home>/workspace
// for the main agent. Must match the root every other per-agent seam resolves.
func modeStateRoot(home, workspaceRoot string) string {
	if ws := strings.TrimSpace(workspaceRoot); ws != "" {
		return ws
	}
	if h := strings.TrimSpace(home); h != "" {
		return filepath.Join(h, "workspace")
	}
	return ""
}

func StoredModeProvider(home string) func(sessionID string) (string, string) {
	root := strings.TrimSpace(home)
	return func(sessionID string) (string, string) {
		st, err := state.Get(root, sessionID)
		if err != nil {
			return "agent", ""
		}
		mode := strings.TrimSpace(string(st.Mode))
		if mode == "" {
			mode = "agent"
		}
		return mode, strings.TrimSpace(st.Phase)
	}
}
