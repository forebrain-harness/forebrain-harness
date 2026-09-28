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
	PinsProvider       func(string) []string
	ReadStatesProvider func() []tool.ReadState
	SnapshotWriter     func(string, AssemblyResult)
}

func NewRuntimeHook(params HookParams) *Hook {
	hook := NewHook(params.Cfg, params.Store)
	hook.Home = strings.TrimSpace(params.Home)
	hook.WorkspaceRoot = strings.TrimSpace(params.WorkspaceRoot)
	hook.RunRT = params.RunRT
	hook.SkillHub = params.SkillHub
	if params.Cfg != nil && params.PrimaryModel != nil {
		if cat, err := llm.Resolve(hook.Home); err == nil {
			hook.ModelLimitsProvider = func(sessionID string) (llm.Limits, bool) {
				provider, model := params.PrimaryModel(sessionID)
				return cat.Lookup(llm.JoinProviderModel(provider, model))
			}
		}
	}
	hook.ModeProvider = StoredModeProvider(modeStateRoot(hook.Home, hook.WorkspaceRoot))
	hook.PinsProvider = params.PinsProvider
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
