// Session MCP resolution: the one place that decides which MCP servers a
// session runs. Three gates run in order, and an entry that fails any of them
// is excluded from the effective list and reported with a one-line reason:
//
//  1. Project gating — the launch project must be trusted, judged from the
//     session-frozen ProjectContext (never re-resolved here; safety.TrustedRoot
//     is the pure function of it). Version control is not part of the gate (R6).
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
	"sort"
	"strings"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/lsp"
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
// agent, under its workspace). agents is the operator-controlled configuration
// both MCP layers live in — the shared list every primary agent inherits and
// the list agentName owns — and launch is the frozen launch-project context.
// Taking the section rather than two slices means the agent's list and the key
// its stamps need cannot come from different agents.
func ResolveSessionMCP(home, agentWorkspace string, agents appcfg.AgentsSection, agentName string, launch safety.ProjectContext) MCPResolution {
	global := agents.Defaults.MCPServers
	agent := agents.MCPServersFor(agentName)
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

	merged := mcp.MergeSessionServers(global, agent, included, strings.ToLower(strings.TrimSpace(agentName)), projectKey)
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
	// The disk side is resolved with no configured lists at all: project
	// entries are the only thing this comparison is about, and the agent layer
	// has nothing to say about what a file on disk contains.
	res := ResolveSessionMCP("", agentWorkspace, appcfg.AgentsSection{}, "", launch)
	onDisk := append(append([]appcfg.MCPServerConfig(nil), res.Servers...), res.Disabled...)
	summary := res.Summary
	summary.OverriddenGlobal = nil
	// Overridden entries belong to the frozen list's merge, not the disk
	// state; recompute them from the frozen view so the section reports what
	// the running session actually replaced.
	var frozenGlobal, frozenAgent, frozenProject, diskProject []appcfg.MCPServerConfig
	for _, srv := range frozen {
		switch {
		case mcp.IsProjectScope(srv):
			frozenProject = append(frozenProject, srv)
		case mcp.IsAgentScope(srv):
			frozenAgent = append(frozenAgent, srv)
		default:
			frozenGlobal = append(frozenGlobal, srv)
		}
	}
	for _, srv := range onDisk {
		if mcp.IsProjectScope(srv) {
			diskProject = append(diskProject, srv)
		}
	}
	remerged := mcp.MergeSessionServers(frozenGlobal, frozenAgent, diskProject, "", "")
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

// StaleConsentError refuses a per-entry decision whose entry is no longer what
// the operator was shown: the project file changed (or lost the entry) between
// the page listing it and the click. Nothing is recorded when it is returned.
type StaleConsentError struct{ Names []string }

func (e *StaleConsentError) Error() string {
	return "project entries changed since they were listed: " + strings.Join(e.Names, ", ")
}

// StaleNames lists the entries whose fingerprint no longer matches.
func (e *StaleConsentError) StaleNames() []string { return e.Names }

// DecideProjectMCPConsentsFor records a decision for the named project
// entries only — allow when the name is also in allowed, deny otherwise —
// and leaves every other entry exactly as it was. Unlike the startup prompt's
// DecideProjectMCPConsents it overwrites a decision a named entry already
// has: that is how a declined entry is allowed again.
//
// fingerprints is what the surface showed for each named entry. A non-nil map
// binds the decision to exactly those entries: any entry the file no longer
// has, or whose fingerprint moved, makes the whole call fail with
// *StaleConsentError before a single decision is recorded. nil keeps the old
// behavior of deciding whatever the file says now.
func DecideProjectMCPConsentsFor(agentWorkspace string, launch safety.ProjectContext, decide, allowed []string, fingerprints map[string]string) error {
	root := safety.TrustedRoot(launch)
	if root == "" {
		return nil
	}
	projectKey := memory.ProjectKey(root)
	entries, _ := mcp.LoadProjectMCPServers(root)
	consents, err := mcp.LoadProjectConsents(agentWorkspace)
	if err != nil {
		// An unreadable store is not an empty one: saving a single decision
		// over it would drop every confirmation recorded before.
		return err
	}
	decideSet := make(map[string]struct{}, len(decide))
	for _, name := range decide {
		decideSet[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedSet[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	// The check and the recording below share this one read of the file: what
	// was verified is exactly what gets recorded.
	if fingerprints != nil {
		entryFPs := make(map[string]string, len(entries))
		for _, srv := range entries {
			entryFPs[strings.ToLower(strings.TrimSpace(srv.Name))] = mcp.ServerFingerprint(srv)
		}
		var stale []string
		seen := make(map[string]struct{}, len(decide))
		for _, name := range decide {
			key := strings.ToLower(strings.TrimSpace(name))
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			// Missing from the file and a moved fingerprint are the same
			// refusal: the operator confirmed an entry this file no longer
			// holds as shown.
			if fp, ok := entryFPs[key]; !ok || fp != fingerprints[key] {
				stale = append(stale, key)
			}
		}
		if len(stale) > 0 {
			return &StaleConsentError{Names: stale}
		}
	}
	for _, srv := range entries {
		name := strings.ToLower(strings.TrimSpace(srv.Name))
		if _, named := decideSet[name]; !named {
			continue
		}
		decision := mcp.ProjectConsentDeny
		if _, ok := allowedSet[name]; ok {
			decision = mcp.ProjectConsentAllow
		}
		consents.Decide(projectKey, srv.Name, mcp.ServerFingerprint(srv), decision, time.Now())
	}
	return mcp.SaveProjectConsents(agentWorkspace, consents)
}

// DeclinedProjectMCPConsents lists the project entries whose current
// fingerprint carries a recorded denial: the ones the startup prompt or the
// project page turned down. They do not load, but they stay on record so a
// denial can be taken back.
func DeclinedProjectMCPConsents(agentWorkspace string, launch safety.ProjectContext) []mcp.PendingProjectConsent {
	root := safety.TrustedRoot(launch)
	if root == "" {
		return nil
	}
	projectKey := memory.ProjectKey(root)
	entries, _ := mcp.LoadProjectMCPServers(root)
	consents, err := mcp.LoadProjectConsents(agentWorkspace)
	if err != nil {
		return nil
	}
	var declined []mcp.PendingProjectConsent
	for _, srv := range entries {
		fp := mcp.ServerFingerprint(srv)
		if decision, decided := consents.Decision(projectKey, srv.Name, fp); !decided || decision != mcp.ProjectConsentDeny {
			continue
		}
		declined = append(declined, mcp.PendingProjectConsent{
			Name:        strings.TrimSpace(srv.Name),
			Fingerprint: fp,
			Summary:     mcp.ConsentSummary(srv),
		})
	}
	return declined
}

// PendingProjectLSPConsents lists the project language-server entries that
// would apply but have no recorded decision. The interactive startup prompt
// shows exactly these; a nil/empty result means there is nothing to ask about.
func PendingProjectLSPConsents(agentWorkspace string, launch safety.ProjectContext) []lsp.PendingProjectServer {
	root := safety.TrustedRoot(launch)
	if root == "" {
		return nil
	}
	return lsp.ResolveProjectServers(agentWorkspace, root, memory.ProjectKey(root), true).Pending
}

// DecideProjectLSPConsents records the operator's answers from the startup
// prompt. allowed carries the server ids that were accepted; every other
// pending entry is recorded as declined so it is not re-asked until its
// fingerprint changes.
func DecideProjectLSPConsents(agentWorkspace string, launch safety.ProjectContext, allowed []string) error {
	root := safety.TrustedRoot(launch)
	if root == "" {
		return nil
	}
	return lsp.DecideProjectServers(agentWorkspace, root, memory.ProjectKey(root), allowed)
}

// DecideProjectLSPConsentsFor is DecideProjectMCPConsentsFor for language
// servers: it decides the named entries only and may overwrite a decision one
// of them already has. fingerprints carries what the surface showed per id and
// is enforced the same way (nil skips the check).
func DecideProjectLSPConsentsFor(agentWorkspace string, launch safety.ProjectContext, decide, allowed []string, fingerprints map[string]string) error {
	root := safety.TrustedRoot(launch)
	if root == "" {
		return nil
	}
	return lsp.DecideProjectServersFor(agentWorkspace, root, memory.ProjectKey(root), decide, allowed, fingerprints)
}

// ProjectLSPFingerprints maps every project language-server entry's id — the
// same ids InspectProjectLSP lists — to the entry's current fingerprint, so a
// surface can hand back exactly what it showed. An untrusted launch has no
// project entries at all and yields nil.
func ProjectLSPFingerprints(agentWorkspace string, launch safety.ProjectContext) map[string]string {
	root := safety.TrustedRoot(launch)
	if root == "" {
		return nil
	}
	entries, _ := lsp.LoadProjectServers(root)
	fingerprints := make(map[string]string, len(entries))
	for id, srv := range entries {
		fingerprints[id] = lsp.ProjectServerFingerprint(srv)
	}
	return fingerprints
}

// InspectProjectLSP is the project language-server view the gateway's project
// page reads: whether the launch project is trusted at all, and if it is,
// which entries are pending confirmation, allowed, denied, and why the file
// or an entry was ignored. It never feeds a running session; surfaces use it
// to present and record decisions.
func InspectProjectLSP(agentWorkspace string, launch safety.ProjectContext) (trusted bool, pending []lsp.PendingProjectServer, allowed, denied, notes []string) {
	root := safety.TrustedRoot(launch)
	if root == "" {
		return false, nil, nil, nil, nil
	}
	state := lsp.ResolveProjectServers(agentWorkspace, root, memory.ProjectKey(root), true)
	for _, id := range sortedMapKeys(state.Allowed) {
		allowed = append(allowed, id)
	}
	return true, state.Pending, allowed, state.Denied, state.Notes
}

// sortedMapKeys lists a map's keys in ascending order.
func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
