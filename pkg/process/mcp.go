// Session MCP resolution: the one place that decides which MCP servers a
// session runs. Three gates run in order, and an entry that fails any of them
// is excluded from the effective list and reported with a one-line reason:
//
//  1. Project gating — the launch project must be version controlled and
//     trusted, judged from the session-frozen ProjectContext (never
//     re-resolved here; safety.TrustedRoot is the pure function of it).
//  2. Per-entry consent — each project entry is confirmed once per
//     fingerprint, per primary agent, per project. Unconfirmed entries fail
//     closed: they are not loaded, and surfaces show them as awaiting
//     confirmation.
//  3. Approval clamping — applied inside mcp.MergeSessionServers; a project
//     entry may not widen approvals beyond "prompt".
//
// The result is frozen for the session: recomputing it mid-session would
// rebuild the tool table and the system prefix that depends on it, which is
// exactly what the prompt-cache contract forbids.
package process

import (
	"log/slog"
	"strings"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/mcp"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

// MCPResolution is the effective MCP server list for one session plus the
// diagnostics surfaces render next to it.
type MCPResolution struct {
	// Servers is the effective list, frozen for the session's lifetime.
	Servers []appcfg.MCPServerConfig
	// Disabled lists the entries the agent's disable store kept out of
	// Servers. They are not started and register no tools; the session keeps
	// them only so /mcp can show — and reverse — the choice.
	Disabled []appcfg.MCPServerConfig
	// ProjectRoot is the root project entries run from (stdio working
	// directory); empty when the launch has no applicable project.
	ProjectRoot string
	// Summary describes the project view: overridden globals, entries that
	// exist but are not running and why, and whether the on-disk files have
	// drifted from what the session froze.
	Summary mcp.ProjectMCPScopeSummary
}

// ResolveSessionMCP computes the effective list for one session launch.
//
// home and agentWorkspace locate the two stores involved: the credential
// overlays (per scope, under home) and the consent records (per primary
// agent, under its workspace). global is the operator-controlled list from
// forebrain.yaml; launch is the frozen launch-project context.
func ResolveSessionMCP(home, agentWorkspace string, global []appcfg.MCPServerConfig, launch safety.ProjectContext) MCPResolution {
	projectRoot := strings.TrimSpace(launch.Project.Root)
	entries, notes := mcp.LoadProjectMCPServers(projectRoot)
	gated := safety.TrustedRoot(launch) != ""
	projectKey := memory.ProjectKey(projectRoot)
	consents, err := mcp.LoadProjectConsents(agentWorkspace)
	if err != nil {
		// An unreadable consent store means no entry can be shown a valid
		// decision: fail closed rather than treat recorded confirmations as
		// absent for some entries and present for others.
		consents = nil
	}

	var included []appcfg.MCPServerConfig
	var notApplied []mcp.MCPNotApplied
	var pending []mcp.PendingProjectConsent
	for _, note := range notes {
		notApplied = append(notApplied, mcp.MCPNotApplied{Name: note.Name, Reason: note.Reason})
	}
	for _, srv := range entries {
		name := strings.TrimSpace(srv.Name)
		fp := mcp.ServerFingerprint(srv)
		switch {
		case !launch.Project.VersionControlled:
			notApplied = append(notApplied, mcp.MCPNotApplied{Name: name, Reason: "project is not version controlled"})
		case !gated:
			notApplied = append(notApplied, mcp.MCPNotApplied{Name: name, Reason: "project is not trusted"})
		default:
			decision, decided := consents.Decision(projectKey, name, fp)
			switch {
			case decided && decision == mcp.ProjectConsentAllow:
				included = append(included, srv)
			case decided && decision == mcp.ProjectConsentDeny:
				notApplied = append(notApplied, mcp.MCPNotApplied{Name: name, Reason: "entry was declined for this project"})
			default:
				notApplied = append(notApplied, mcp.MCPNotApplied{Name: name, Reason: "awaiting confirmation"})
				pending = append(pending, mcp.PendingProjectConsent{
					Name:        name,
					Fingerprint: fp,
					Summary:     mcp.ConsentSummary(srv),
				})
			}
		}
	}

	merged := mcp.MergeSessionServers(global, included, projectKey)
	// Disabled servers (the agent's state/mcp/disabled.json) drop out of every
	// NEW session here, after the merge: the user's YAML is never rewritten and
	// a running session's frozen list is untouched, so nothing mid-session
	// moves. The store lives with the consent records, under the agent's
	// workspace.
	effective, disabled, err := mcp.FilterDisabledServers(agentWorkspace, merged.Servers)
	if err != nil {
		slog.Warn("mcp disable store unreadable; starting every configured server", "err", err)
	}
	return MCPResolution{
		Servers:     effective,
		Disabled:    disabled,
		ProjectRoot: projectRoot,
		Summary: mcp.ProjectMCPScopeSummary{
			ProjectRoot:      projectRoot,
			OverriddenGlobal: merged.OverriddenGlobal,
			NotApplied:       notApplied,
			PendingReload:    false,
		},
	}
}

// InspectProjectMCP recomputes the project view against the current on-disk
// state for display only. It never feeds the running list: its job is to tell
// /mcp (and the web view) what changed since the session froze its list, so
// it can say a change lands in the next session instead of silently applying
// or hiding it.
//
// frozen is the whole list the session froze, disabled entries included, and
// the disk side is compared the same way: a disable toggle is not a change to
// the configuration on disk, and must not read as one.
func InspectProjectMCP(agentWorkspace string, frozen []appcfg.MCPServerConfig, launch safety.ProjectContext) mcp.ProjectMCPScopeSummary {
	res := ResolveSessionMCP("", agentWorkspace, nil, launch)
	onDisk := append(append([]appcfg.MCPServerConfig(nil), res.Servers...), res.Disabled...)
	summary := res.Summary
	summary.OverriddenGlobal = nil
	// Overridden globals belong to the frozen list's merge, not the disk
	// state; recompute them from the frozen view so the section reports what
	// the running session actually replaced.
	var frozenProject, diskProject []appcfg.MCPServerConfig
	for _, srv := range frozen {
		if mcp.IsProjectScope(srv) {
			frozenProject = append(frozenProject, srv)
		}
	}
	global := make([]appcfg.MCPServerConfig, 0, len(frozen))
	for _, srv := range frozen {
		if !mcp.IsProjectScope(srv) {
			global = append(global, srv)
		}
	}
	for _, srv := range onDisk {
		if mcp.IsProjectScope(srv) {
			diskProject = append(diskProject, srv)
		}
	}
	remerged := mcp.MergeSessionServers(global, diskProject, "")
	summary.OverriddenGlobal = remerged.OverriddenGlobal
	summary.PendingReload = !mcp.SameProjectEntry(frozenProject, diskProject)
	return summary
}

// PendingProjectMCPConsents lists the project entries that would load but
// have no recorded decision. The interactive startup prompt shows exactly
// these; a nil/empty result means there is nothing to ask about.
func PendingProjectMCPConsents(agentWorkspace string, launch safety.ProjectContext) []mcp.PendingProjectConsent {
	// Reuse the resolution pipeline with an empty consent store: the pending
	// set is what the pipeline excludes for lack of a decision.
	entries, _ := mcp.LoadProjectMCPServers(strings.TrimSpace(launch.Project.Root))
	if safety.TrustedRoot(launch) == "" {
		return nil
	}
	projectKey := memory.ProjectKey(strings.TrimSpace(launch.Project.Root))
	consents, _ := mcp.LoadProjectConsents(agentWorkspace)
	var pending []mcp.PendingProjectConsent
	for _, srv := range entries {
		fp := mcp.ServerFingerprint(srv)
		if _, decided := consents.Decision(projectKey, srv.Name, fp); decided {
			continue
		}
		pending = append(pending, mcp.PendingProjectConsent{
			Name:        strings.TrimSpace(srv.Name),
			Fingerprint: fp,
			Summary:     mcp.ConsentSummary(srv),
		})
	}
	return pending
}

// DecideProjectMCPConsents records the operator's answers from the startup
// prompt. allowed carries the lowercased names that were accepted; every
// other pending entry is recorded as declined so it is not re-asked until its
// fingerprint changes.
func DecideProjectMCPConsents(agentWorkspace string, launch safety.ProjectContext, allowed []string) error {
	root := safety.TrustedRoot(launch)
	if root == "" {
		return nil
	}
	projectKey := memory.ProjectKey(root)
	entries, _ := mcp.LoadProjectMCPServers(root)
	consents, err := mcp.LoadProjectConsents(agentWorkspace)
	if err != nil {
		consents = mcp.ProjectConsents{}
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedSet[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	for _, srv := range entries {
		fp := mcp.ServerFingerprint(srv)
		if _, decided := consents.Decision(projectKey, srv.Name, fp); decided {
			continue
		}
		decision := mcp.ProjectConsentDeny
		if _, ok := allowedSet[strings.ToLower(strings.TrimSpace(srv.Name))]; ok {
			decision = mcp.ProjectConsentAllow
		}
		consents.Decide(projectKey, srv.Name, fp, decision, time.Now())
	}
	return mcp.SaveProjectConsents(agentWorkspace, consents)
}
