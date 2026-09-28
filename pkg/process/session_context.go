package process

import (
	"context"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// AgentContext seeds the context with the active session's mode and, in plan
// mode, the default gateway/channel allowed plan path. Prefer AgentContextForProject
// when a launch-project key is available.
func AgentContext(base context.Context, stateRoot, sid string) context.Context {
	return AgentContextForProject(base, stateRoot, sid, "")
}

// AgentContextForProject seeds the context with the active session's mode and,
// in plan mode, the allowed plan path. stateRoot is the per-agent state root (a
// workspace root) onto which the mode/plan stores join "state" — callers must
// pass the SAME root every other per-agent seam resolves (via
// primaryagent.ActiveStateRoot), or the allowed plan path here won't match the
// path the model is told to edit / GuardWrite permits. projectKey is the
// session's launch-project identity (memories.ProjectKey); empty means
// gateway/channel plans live directly under <workspaceRoot>/plans.
func AgentContextForProject(base context.Context, stateRoot, sid, projectKey string) context.Context {
	sid = strings.TrimSpace(sid)
	if sid == "" {
		sid = "default"
	}
	projectKey = strings.TrimSpace(projectKey)
	if base == nil {
		base = context.Background()
	}
	agCtx := llm.WithAgentSessionID(base, sid)
	agCtx = tool.WithProjectKey(agCtx, projectKey)
	cacheKey := llm.PromptCacheKeyFromContext(base)
	if cacheKey == "" {
		cacheKey = sid
	}
	agCtx = llm.WithPromptCacheKey(agCtx, cacheKey)
	ms, err := state.Get(stateRoot, sid)
	if err != nil {
		return tool.WithMode(agCtx, string(state.ModeAgent))
	}
	modeName := strings.TrimSpace(string(ms.Mode))
	agCtx = tool.WithMode(agCtx, modeName)
	// The plan directory is writable in every mode, not only plan mode. In plan
	// mode it is the ONLY writable region; outside plan mode it is the one path
	// under the agent state root that GuardWrite's protected-metadata rule must
	// not reject, so that the agent can record the plan's implementation status
	// back into the plan file after the work is done.
	agCtx = tool.WithAllowedPlanPath(agCtx, state.PlanDirForProject(stateRoot, projectKey))
	return agCtx
}
