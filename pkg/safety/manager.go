// The sandbox manager and workspace metadata protection.
package safety

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

type Manager struct {
	mu             sync.RWMutex
	cfg            *appcfg.Root
	sources        []SourceSettings
	snapshot       Snapshot
	cwd            string
	originalCwd    string
	tempDir        string
	additionalDirs []string
	// isolatedPeerRoots are workspaces belonging to other primary agents. They
	// are denied outright so the boundary holds for anything the sandbox
	// governs — a shell command included — not just for the file tools whose
	// allowed-roots list is filtered separately.
	isolatedPeerRoots []string
	runtimeConfig     RuntimeConfig
	networkMu         sync.Mutex
	networkLease      *managedNetworkLease
}

func NewManager() *Manager { return &Manager{} }

func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.networkMu.Lock()
	lease := m.networkLease
	m.networkLease = nil
	m.networkMu.Unlock()
	lease.close()
}

func profileForSandboxMode(cfg *appcfg.Root) Profile {
	if cfg != nil && strings.TrimSpace(cfg.DefaultPermissions) != "" {
		switch strings.TrimSpace(cfg.DefaultPermissions) {
		case appcfg.PermissionProfileReadOnly:
			return ProfileReadOnly
		case appcfg.PermissionProfileWorkspace:
			return ProfileWorkspaceWrite
		case appcfg.PermissionProfileDangerFullAccess:
			return ProfileReadOnly
		default:
			return ProfileManaged
		}
	}
	if cfg != nil && cfg.SandboxMode == appcfg.SandboxModeWorkspaceWrite {
		if detectPlatform() == PlatformWindows && cfg.Windows.Sandbox == "" {
			return ProfileReadOnly
		}
		return ProfileWorkspaceWrite
	}
	// When the user explicitly opts into read-only mode, honour that.
	if cfg != nil && cfg.SandboxMode == appcfg.SandboxModeReadOnly {
		return ProfileReadOnly
	}
	// A context-free config has no project-trust decision. Callers that know the
	// launch project must pass EffectiveConfig; raw config fails closed.
	return ProfileReadOnly
}

func ProfileForConfig(cfg *appcfg.Root) Profile {
	return profileForSandboxMode(cfg)
}

func sandboxTypeForPlatform(platform Platform) SandboxType {
	switch platform {
	case PlatformDarwin:
		return SandboxTypeMacosSeatbelt
	case PlatformLinux, PlatformWSL:
		return SandboxTypeLinuxSeccomp
	case PlatformWindows:
		return SandboxTypeWindowsRestrictedToken
	default:
		return SandboxTypeNone
	}
}

func (m *Manager) UpdateConfig(cfg *appcfg.Root, sources []SourceSettings, snap Snapshot, cwd, originalCwd, tempDir string, additionalDirs []string) RuntimeConfig {
	if m == nil {
		return RuntimeConfig{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cfg
	m.sources = append([]SourceSettings(nil), sources...)
	m.snapshot = clonePermissionSnapshot(snap)
	m.cwd = strings.TrimSpace(cwd)
	m.originalCwd = strings.TrimSpace(originalCwd)
	m.tempDir = strings.TrimSpace(tempDir)
	m.additionalDirs = append([]string(nil), additionalDirs...)
	m.runtimeConfig = m.convertLocked()
	return m.runtimeConfig
}

// SetIsolatedPeerRoots denies the workspaces of every other primary agent.
//
// Multi-primary isolation cannot rest on the file tools alone: those filter
// their own allowed roots, while a shell command reaches the filesystem through
// the sandbox. Recording the peers here puts them on the sandbox's deny lists
// so agent A cannot read or write agent B's workspace by any route.
func (m *Manager) SetIsolatedPeerRoots(roots []string) RuntimeConfig {
	if m == nil {
		return RuntimeConfig{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.isolatedPeerRoots = normalizeIsolatedRoots(roots)
	m.runtimeConfig = m.convertLocked()
	return m.runtimeConfig
}

// convertLocked rebuilds the runtime config and re-applies the peer denials,
// which must survive every refresh: a config reload that dropped them would
// silently reopen another agent's workspace.
func (m *Manager) convertLocked() RuntimeConfig {
	out := ConvertToRuntimeConfig(m.sources, m.snapshot, m.cwd, m.originalCwd, m.tempDir, m.additionalDirs)
	for _, root := range m.isolatedPeerRoots {
		out.Filesystem.AllowWrite = removePath(out.Filesystem.AllowWrite, root)
		out.Filesystem.AllowRead = removePath(out.Filesystem.AllowRead, root)
		out.Filesystem.DenyWrite = appendUniquePath(out.Filesystem.DenyWrite, root)
		out.Filesystem.DenyRead = appendUniquePath(out.Filesystem.DenyRead, root)
	}
	return out
}

func normalizeIsolatedRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
		out = appendUniquePath(out, filepath.Clean(root))
	}
	return out
}

// removePath drops root and anything nested under it, so an allow entry cannot
// re-open a denied peer workspace through a subdirectory.
func removePath(list []string, root string) []string {
	if len(list) == 0 {
		return list
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		clean := filepath.Clean(strings.TrimSpace(item))
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func (m *Manager) RefreshConfig() RuntimeConfig {
	if m == nil {
		return RuntimeConfig{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runtimeConfig = m.convertLocked()
	return m.runtimeConfig
}

func (m *Manager) RuntimeConfig() RuntimeConfig {
	if m == nil {
		return RuntimeConfig{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneRuntimeConfig(m.runtimeConfig)
}

func (m *Manager) DecideShellCommand(cfg *appcfg.Root, _ string, sandboxPermissions SandboxPermissions, profile Profile, allowNetwork bool) SandboxDecision {
	cfg = m.cfgOrCached(cfg)
	if profile == "" {
		profile = profileForSandboxMode(cfg)
	}
	profile = profile.normalized()
	sandboxPermissions = sandboxPermissions.Normalized()
	if cfg == nil {
		return SandboxDecision{Mode: ModeSandboxed, Profile: profile, UseSandbox: false, Reason: "sandbox_unavailable", Backend: backendForPlatform(detectPlatform()), SandboxType: sandboxTypeForPlatform(detectPlatform()), UnavailableReason: "sandbox configuration unavailable"}
	}
	if cfg.DangerFullAccessEnabled() {
		return SandboxDecision{Mode: ModeDangerFullAccess, Profile: profile, UseSandbox: false, Reason: "danger_full_access", Backend: BackendHost, SandboxType: SandboxTypeNone}
	}
	if sandboxPermissions == SandboxPermissionsRequireEscalated {
		return SandboxDecision{Mode: ModeDangerFullAccess, Profile: profile, UseSandbox: false, Reason: "require_escalated", Backend: BackendHost, SandboxType: SandboxTypeNone, RequiresPermission: true}
	}
	platform := detectPlatform()
	if platform == PlatformWindows && cfg.Windows.Sandbox == "" {
		return SandboxDecision{Mode: ModeSandboxed, Profile: profile, UseSandbox: false, Reason: "windows_sandbox_disabled", Backend: BackendHost, SandboxType: SandboxTypeNone}
	}
	if reason := backendUnavailableReason(platform); reason != "" {
		return SandboxDecision{Mode: ModeSandboxed, Profile: profile, UseSandbox: false, Reason: "sandbox_unavailable", Backend: backendForPlatform(platform), SandboxType: sandboxTypeForPlatform(platform), UnavailableReason: reason}
	}
	return SandboxDecision{Mode: ModeSandboxed, Profile: profile, UseSandbox: true, Reason: string(cfg.SandboxMode), Backend: backendForPlatform(platform), SandboxType: sandboxTypeForPlatform(platform)}
}

func (m *Manager) Decide(cfg *appcfg.Root, kind ToolKind) SandboxDecision {
	cfg = m.cfgOrCached(cfg)
	if kind == ToolKindUserShell {
		return SandboxDecision{Mode: ModeDangerFullAccess, UseSandbox: false, Reason: "user_shell", Backend: BackendHost, SandboxType: SandboxTypeNone}
	}
	if kind == ToolKindShell {
		return m.DecideShellCommand(cfg, "", SandboxPermissionsUseDefault, profileForSandboxMode(cfg), false)
	}
	if cfg == nil {
		return SandboxDecision{Mode: ModeSandboxed, UseSandbox: false, Reason: "sandbox_unavailable", Backend: backendForPlatform(detectPlatform()), UnavailableReason: "sandbox configuration unavailable"}
	}
	if cfg.DangerFullAccessEnabled() {
		return SandboxDecision{Mode: ModeDangerFullAccess, UseSandbox: false, Reason: "danger_full_access", Backend: BackendHost, SandboxType: SandboxTypeNone}
	}
	platform := detectPlatform()
	if platform == PlatformWindows && cfg.Windows.Sandbox == "" {
		return SandboxDecision{Mode: ModeSandboxed, UseSandbox: false, Reason: "windows_sandbox_disabled", Backend: BackendHost, SandboxType: SandboxTypeNone}
	}
	if reason := backendUnavailableReason(platform); reason != "" {
		return SandboxDecision{Mode: ModeSandboxed, UseSandbox: false, Reason: "sandbox_unavailable", Backend: backendForPlatform(platform), SandboxType: sandboxTypeForPlatform(platform), UnavailableReason: reason}
	}
	return SandboxDecision{Mode: ModeSandboxed, UseSandbox: true, Reason: string(cfg.SandboxMode), Backend: backendForPlatform(platform), SandboxType: sandboxTypeForPlatform(platform)}
}

func (m *Manager) StartupCheck(cfg *appcfg.Root) error {
	cfg = m.cfgOrCached(cfg)
	if cfg == nil || cfg.DangerFullAccessEnabled() {
		return nil
	}
	decision := m.Decide(cfg, ToolKindShell)
	if decision.UnavailableReason == "" {
		return nil
	}
	return &UnavailableError{Reason: decision.UnavailableReason}
}

func (m *Manager) Status(cfg *appcfg.Root) Status {
	cfg = m.cfgOrCached(cfg)
	platform := detectPlatform()
	st := Status{
		Platform: platform,
		Backend:  backendForPlatform(platform),
	}
	if cfg == nil {
		st.Mode = ModeDangerFullAccess
		st.EffectiveEnabled = false
		st.UnavailableReason = "missing_config"
		return st
	}
	st.SettingsLocked = AreSandboxSettingsLockedByPolicy(m.settingsSources())
	decision := m.Decide(cfg, ToolKindShell)
	st.Mode = decision.Mode
	st.Backend = decision.Backend
	st.UnavailableReason = decision.UnavailableReason
	st.EffectiveEnabled = decision.UseSandbox
	return st
}

func (m *Manager) Doctor(cfg *appcfg.Root) DoctorReport {
	platform := detectPlatform()
	rep := DoctorReport{
		Platform: platform,
		Backend:  backendForPlatform(platform),
	}
	if reason := backendUnavailableReason(platform); reason != "" {
		rep.DependencyErrors = append(rep.DependencyErrors, reason)
	}
	if platform == PlatformLinux || platform == PlatformWSL {
		rep.DependencyWarning = append(rep.DependencyWarning, "linux_glob_limits_apply")
	}
	return rep
}

func (m *Manager) RunCommand(ctx context.Context, cfg *appcfg.Root, req CommandRequest) (result CommandResult, decision SandboxDecision, err error) {
	startedAt := time.Now()
	defer func() {
		result.ExecutionTiming = llm.NewExecutionTiming(startedAt, time.Now())
	}()

	cfg = m.cfgOrCached(cfg)
	if strings.TrimSpace(req.Command) == "" {
		return CommandResult{}, SandboxDecision{}, nil
	}
	if req.Timeout <= 0 {
		req.Timeout = 10 * time.Second
	}
	if strings.TrimSpace(req.WorkDir) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return CommandResult{}, SandboxDecision{}, err
		}
		req.WorkDir = wd
	}
	workDirAbs, err := filepath.Abs(strings.TrimSpace(req.WorkDir))
	if err != nil {
		return CommandResult{}, SandboxDecision{}, err
	}
	decision = m.Decide(cfg, req.ToolKind)
	if req.ToolKind == ToolKindShell {
		decision = m.DecideShellCommand(cfg, req.Command, req.SandboxPermissions, req.Profile, req.AllowNetwork)
		req.Profile = decision.Profile
	}
	if decision.UnavailableReason != "" {
		// Sandbox backend is unavailable. The LLM-driven command path always fails
		// closed: dropping to unsandboxed host execution would let a missing
		// backend silently remove isolation, a worse risk than refusing to run.
		// StartupCheck enforces the same fail-closed rule at boot whenever the
		// sandbox is enabled.
		return CommandResult{}, decision, &UnavailableError{Reason: decision.UnavailableReason}
	}
	runtimeCfg := m.runtimeConfigForCommand(cfg, workDirAbs, req)
	if !decision.UseSandbox {
		res, err := runHostCommand(ctx, workDirAbs, req.Timeout, req.Command, req.Env, "", req.OnOutput, req.OutputSpoolDir)
		finalized, finalizeErr := finalizeCommandResult(res, err)
		return finalized, decision, finalizeErr
	}
	req, err = applyRuntimeConfigToRequest(req, runtimeCfg, workDirAbs)
	if err != nil {
		return CommandResult{}, decision, err
	}
	req = constrainRequestToFilesystemProfile(req, workDirAbs)
	networkToken := ""
	runCtx := ctx
	var networkProxy *managedNetworkProxy
	var networkCommandLease *managedNetworkLease
	constraints := m.networkConstraints()
	// Proxy configuration and centrally supplied constraints may narrow network
	// access, but neither is itself a grant. Only attach the bridge after the
	// active filesystem/network profile or a structured grant enabled network
	// access for this command.
	if cfg != nil && req.AllowNetwork {
		effectiveNetwork, proxyErr := cfg.ResolveNetworkProxyConfigForNetworkEnabledWithConstraints(constraints)
		if proxyErr != nil {
			return CommandResult{}, decision, proxyErr
		}
		if effectiveNetwork != nil {
			m.networkMu.Lock()
			desiredKey := managedNetworkProxyConfigKey(*effectiveNetwork)
			var oldLease *managedNetworkLease
			if m.networkLease == nil || m.networkLease.key != desiredKey {
				var replacement *managedNetworkLease
				replacement, proxyErr = acquireManagedNetworkProxy(*effectiveNetwork)
				if proxyErr == nil {
					oldLease = m.networkLease
					m.networkLease = replacement
				}
			}
			if proxyErr == nil && m.networkLease != nil {
				networkCommandLease = m.networkLease.retain()
				if networkCommandLease != nil {
					networkProxy = networkCommandLease.proxy
				}
			}
			m.networkMu.Unlock()
			oldLease.close()
			if proxyErr != nil {
				return CommandResult{}, decision, proxyErr
			}
			if networkProxy == nil {
				return CommandResult{}, decision, errors.New("managed network proxy lease is unavailable")
			}
			defer networkCommandLease.close()
			rules := networkRulesFromSnapshot(m.permissionSnapshot())
			rules = append(rules, networkRulesFromSession(req.SessionNetworkRules)...)
			if effectiveNetwork.HardDenyAllowlistMisses {
				rules = networkDenyRulesOnly(rules)
			}
			approved := req.ApprovedNetworkAccess
			if effectiveNetwork.HardDenyAllowlistMisses {
				approved = nil
			}
			policy := networkCommandPolicy{
				config: *effectiveNetwork, rules: rules,
				approved: approved,
				canAsk:   networkApprovalPromptAllowed(m.approvalPolicy()) && !effectiveNetwork.HardDenyAllowlistMisses,
			}
			var proxyEnv map[string]string
			networkToken, proxyEnv, req.ManagedNetwork, proxyErr = networkProxy.register(ctx, policy, req.NetworkApproval, NetworkApprovalRequest{
				Command: req.Command, WorkDir: workDirAbs, EnvironmentID: req.NetworkEnvironmentID, ExecutionID: req.NetworkExecutionID,
				CommandEnv: req.Env,
			})
			if proxyErr != nil {
				return CommandResult{}, decision, proxyErr
			}
			runCtx = networkProxy.executionContext(networkToken)
			for key, value := range proxyEnv {
				req.Env = appendOrReplaceEnv(req.Env, key, value)
			}
		}
	}
	res, err := runSandboxCommand(runCtx, decision.Backend, cfg, req, workDirAbs)
	finalized, finalizeErr := finalizeCommandResult(res, err)
	if networkProxy != nil && networkToken != "" {
		finalized.NetworkDenial = networkProxy.unregister(networkToken)
		if finalized.NetworkDenial != nil && ctx.Err() == nil && errors.Is(finalizeErr, context.Canceled) {
			finalizeErr = nil
		}
	}
	if finalizeErr != nil {
		return finalized, decision, finalizeErr
	}
	if IsLikelySandboxDenied(decision, finalized) {
		return finalized, decision, &DeniedError{Result: finalized}
	}
	return finalized, decision, nil
}

func (m *Manager) networkConstraints() *appcfg.NetworkConstraints {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var constraints *appcfg.NetworkConstraints
	for _, source := range m.sources {
		if source.NetworkConstraints == nil {
			continue
		}
		copy := *source.NetworkConstraints
		constraints = &copy
	}
	return constraints
}

func (m *Manager) permissionSnapshot() Snapshot {
	if m == nil {
		return Snapshot{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return clonePermissionSnapshot(m.snapshot)
}

func (m *Manager) approvalPolicy() ApprovalPolicy {
	if m == nil {
		return ApprovalPolicy{Mode: ApprovalOnRequest}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshot.ApprovalPolicy
}

func networkApprovalPromptAllowed(policy ApprovalPolicy) bool {
	return policy.Mode != ApprovalNever
}

func appendOrReplaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	for index, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[index] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func applyRuntimeConfigToRequest(req CommandRequest, runtimeCfg RuntimeConfig, cwd string) (CommandRequest, error) {
	// A request that resolved its own write set keeps exactly that set. The
	// configured roots are the workspace's ambient policy, and such a request
	// has already decided against them: it states the exceptions to a strict
	// boundary, so adding the workspace back would erase the boundary the
	// exceptions were carved out of. Denials are not affected - they apply to
	// whatever the request writes, granted or not.
	writeRoots := runtimeCfg.Filesystem.AllowWrite
	if req.ExclusiveWritablePaths {
		writeRoots = nil
	}
	for _, path := range writeRoots {
		req.AdditionalWritablePaths = appendUniquePath(req.AdditionalWritablePaths, path)
	}
	for _, path := range runtimeCfg.Filesystem.DenyWrite {
		req.DeniedWritablePaths = appendUniquePath(req.DeniedWritablePaths, path)
	}
	for _, path := range runtimeCfg.Filesystem.AllowRead {
		req.AdditionalReadablePaths = appendUniquePath(req.AdditionalReadablePaths, path)
	}
	for _, path := range runtimeCfg.Filesystem.DenyRead {
		req.DeniedReadablePaths = appendUniquePath(req.DeniedReadablePaths, path)
	}
	for _, pattern := range runtimeCfg.Filesystem.DenyReadPatterns {
		req.DeniedReadablePatterns = appendUniqueString(req.DeniedReadablePatterns, pattern)
	}
	if len(req.DeniedReadablePatterns) > 0 {
		expanded, err := ExpandDeniedReadPatterns(req.DeniedReadablePatterns, cwd, runtimeCfg.Filesystem.GlobScanMaxDepth)
		if err != nil {
			return CommandRequest{}, err
		}
		for _, path := range expanded {
			req.DeniedReadablePaths = appendUniquePath(req.DeniedReadablePaths, path)
		}
	}
	req.IncludePlatformDefaults = req.IncludePlatformDefaults || runtimeCfg.Filesystem.IncludePlatformDefaults
	req.AllowNetwork = req.AllowNetwork || runtimeCfg.NetworkEnabled
	return req, nil
}

func (m *Manager) cfgOrCached(cfg *appcfg.Root) *appcfg.Root {
	if cfg != nil || m == nil {
		return cfg
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

func clonePermissionSnapshot(in Snapshot) Snapshot {
	out := Snapshot{
		Mode:                   in.Mode,
		ApprovalPolicy:         in.ApprovalPolicy,
		Rules:                  map[PermissionSource]map[PermissionBehavior][]PermissionRuleValue{},
		FileSystemGrants:       cloneFilesystemGrants(in.FileSystemGrants),
		NetworkGrants:          append([]NetworkPermissionGrant(nil), in.NetworkGrants...),
		StrictAutoReviewRunIDs: append([]string(nil), in.StrictAutoReviewRunIDs...),
	}
	for src, byBehavior := range in.Rules {
		out.Rules[src] = map[PermissionBehavior][]PermissionRuleValue{}
		for behavior, rules := range byBehavior {
			cp := make([]PermissionRuleValue, 0, len(rules))
			for _, rule := range rules {
				rule.CommandPrefix = append([]string(nil), rule.CommandPrefix...)
				cp = append(cp, rule)
			}
			out.Rules[src][behavior] = cp
		}
	}
	return out
}

func cloneFilesystemGrants(grants []FileSystemPermissionGrant) []FileSystemPermissionGrant {
	out := make([]FileSystemPermissionGrant, 0, len(grants))
	for _, grant := range grants {
		if grant.Entry.Path.Value != nil {
			value := *grant.Entry.Path.Value
			grant.Entry.Path.Value = &value
		}
		if grant.GlobScanMaxDepth != nil {
			depth := *grant.GlobScanMaxDepth
			grant.GlobScanMaxDepth = &depth
		}
		out = append(out, grant)
	}
	return out
}

func cloneRuntimeConfig(in RuntimeConfig) RuntimeConfig {
	return RuntimeConfig{
		Filesystem: FilesystemConfig{
			AllowWrite:              append([]string(nil), in.Filesystem.AllowWrite...),
			DenyWrite:               append([]string(nil), in.Filesystem.DenyWrite...),
			AllowRead:               append([]string(nil), in.Filesystem.AllowRead...),
			DenyRead:                append([]string(nil), in.Filesystem.DenyRead...),
			DenyReadPatterns:        append([]string(nil), in.Filesystem.DenyReadPatterns...),
			GlobScanMaxDepth:        cloneInt(in.Filesystem.GlobScanMaxDepth),
			IncludePlatformDefaults: in.Filesystem.IncludePlatformDefaults,
		},
		NetworkEnabled: in.NetworkEnabled,
	}
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func finalizeCommandResult(res CommandResult, runErr error) (CommandResult, error) {
	if res.AggregatedOutput == "" && (res.Stdout != "" || res.Stderr != "") {
		res.AggregatedOutput = res.Stdout + res.Stderr
	}
	return res, runErr
}

func (m *Manager) runtimeConfigForCommand(cfg *appcfg.Root, workDir string, req CommandRequest) RuntimeConfig {
	if m == nil {
		return RuntimeConfig{}
	}
	m.mu.RLock()
	cached := cloneRuntimeConfig(m.runtimeConfig)
	sources := append([]SourceSettings(nil), m.sources...)
	snapshot := clonePermissionSnapshot(m.snapshot)
	originalCwd := m.originalCwd
	tempDir := m.tempDir
	additionalDirs := append([]string(nil), m.additionalDirs...)
	m.mu.RUnlock()

	for _, p := range req.AdditionalWritablePaths {
		additionalDirs = appendUniquePath(additionalDirs, p)
	}
	if len(cached.Filesystem.AllowWrite) == 0 && len(cached.Filesystem.DenyWrite) == 0 {
		if tempDir == "" {
			tempDir = os.TempDir()
		}
		if len(sources) == 0 && cfg != nil {
			sources = LocalConfigSources(workDir, cfg)
		}
		cached = ConvertToRuntimeConfig(sources, snapshot, workDir, originalCwdOr(workDir, originalCwd), tempDir, additionalDirs)
	}
	for _, p := range req.AdditionalWritablePaths {
		cached.Filesystem.AllowWrite = appendUniquePath(cached.Filesystem.AllowWrite, p)
	}
	for _, p := range req.DeniedWritablePaths {
		cached.Filesystem.DenyWrite = appendUniquePath(cached.Filesystem.DenyWrite, p)
	}
	return cached
}

func (m *Manager) settingsSources() []SourceSettings {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]SourceSettings(nil), m.sources...)
}

func originalCwdOr(workDir, original string) string {
	original = strings.TrimSpace(original)
	if original != "" {
		return original
	}
	return workDir
}

func runHostCommand(ctx context.Context, workDir string, timeout time.Duration, command string, explicitEnv []string, shellPref string, onOutput func(OutputStream, []byte), spoolDirs ...string) (CommandResult, error) {
	if ctx.Err() != nil {
		return CommandResult{ExitCode: 1}, ctx.Err()
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := hostShellCommand(runCtx, command, shellPref)
	cmd.Dir = workDir
	cmd.Env = home.SafeSubprocessEnv(nil, home.Options{ExplicitEnv: explicitCommandEnv(explicitEnv, nil)})
	spoolDir := ""
	if len(spoolDirs) > 0 {
		spoolDir = spoolDirs[0]
	}
	req := CommandRequest{OnOutput: onOutput, OutputSpoolDir: spoolDir}
	capture := newCommandCapture(req)
	cmd.Stdout = capture.writer(OutputStreamStdout)
	cmd.Stderr = capture.writer(OutputStreamStderr)
	setupProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		_ = capture.result()
		return CommandResult{ExitCode: 1}, err
	}
	err, stopErr := waitForCommandContext(runCtx, cmd)
	res := capture.result()
	res.ExitCode = 0
	if err != nil {
		// If the context was cancelled or timed out, return that
		// error rather than the SIGKILL exit error.
		if errors.Is(stopErr, context.Canceled) || errors.Is(stopErr, context.DeadlineExceeded) {
			if errors.Is(stopErr, context.DeadlineExceeded) {
				res.ExitCode = 124
				return res, &TimeoutError{Result: res}
			}
			res.ExitCode = 1
			return res, context.Canceled
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			if signal, ok := processSignal(exitErr); ok {
				res.ExitCode = 128 + signal
				return res, &SignalError{Signal: signal}
			}
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		res.ExitCode = 1
		return res, err
	}
	return res, nil
}

func explicitCommandEnv(explicitEnv []string, extra map[string]string) map[string]string {
	explicit := map[string]string{}
	for _, item := range explicitEnv {
		k, v, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		explicit[k] = v
	}
	for k, v := range extra {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		explicit[k] = v
	}
	return explicit
}

func runSandboxCommand(ctx context.Context, backend BackendName, cfg *appcfg.Root, req CommandRequest, workDir string) (CommandResult, error) {
	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	switch backend {
	case BackendSeatbelt:
		return runSeatbeltCommand(runCtx, cfg, req, workDir)
	case BackendBubblewrap:
		return runBubblewrapCommand(runCtx, cfg, req, workDir)
	case BackendWindows:
		return runWindowsSandboxCommand(runCtx, cfg, req, workDir)
	default:
		return CommandResult{}, &UnavailableError{Reason: "sandbox_backend_unsupported"}
	}
}

// protectedMetadataDirNames are the workspace directories no command may write
// into. They are exactly the places agent configuration and project skills
// live: forebrain loads instructions and skills out of .forebrain, .agents, .claude
// and .codex, so a command that could write there could rewrite the
// instructions the model is about to be given.
var protectedMetadataDirNames = []string{".agents", ".forebrain", ".claude", ".codex"}

// gitExecutionSurfaces are the paths inside a repository whose contents git
// turns into commands. Hooks are scripts git executes on ordinary operations,
// the config files carry keys that name a program to run (core.pager,
// core.sshCommand, core.hooksPath, filter.*.clean, alias.*), and .git/modules
// holds each submodule's own copy of both. A payload written there runs on the
// host with the user's full privileges the next time anyone runs git, outside
// the sandbox and without passing an approval, so these stay denied.
//
// The rest of .git is deliberately writable. Index locks, refs, objects and
// logs are what builds, tests and commits actually touch, and denying them
// turned every test run into an approval prompt. Nothing in an ordinary build
// writes the surfaces below: they change when someone deliberately runs git
// config, installs a hook manager, or updates a submodule, each worth its one
// approval.
var gitExecutionSurfaces = []string{"hooks", "config", "config.worktree", "modules"}

// protectedWritePaths returns the paths to deny writes to, resolved against
// every workspace root a command can be launched from.
func protectedWritePaths(cwd, originalCwd string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, rawRoot := range []string{cwd, originalCwd} {
		root := strings.TrimSpace(rawRoot)
		if root == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		for _, name := range protectedMetadataDirNames {
			out = appendUniquePath(out, filepath.Join(abs, name))
		}
		// The repository directory is <root>/.git for an ordinary checkout, and
		// a path named by that file for a worktree or submodule checkout, which
		// also shares a common directory with the repository it came from.
		gitDirs := append([]string{filepath.Join(abs, ".git")}, gitDirsFromWorktreeFile(abs)...)
		for _, dir := range gitDirs {
			for _, surface := range gitExecutionSurfaces {
				out = appendUniquePath(out, filepath.Join(dir, surface))
			}
		}
	}
	return out
}

// gitDirsFromWorktreeFile resolves the repository directory and the common
// directory a "gitdir:" file points at. A worktree of a repository elsewhere on
// disk resolves outside the workspace, where the writable roots already refuse
// the write; a submodule resolves back inside it, where this is what protects
// the submodule's hooks.
func gitDirsFromWorktreeFile(root string) []string {
	data, err := os.ReadFile(filepath.Join(root, ".git"))
	if err != nil {
		return nil
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(strings.ToLower(line), "gitdir:") {
		return nil
	}
	gitDir := strings.TrimSpace(line[len("gitdir:"):])
	if gitDir == "" {
		return nil
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(root, gitDir)
	}
	gitDir = filepath.Clean(gitDir)
	out := []string{gitDir}
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		if raw := strings.TrimSpace(string(data)); raw != "" {
			common := filepath.Clean(raw)
			if !filepath.IsAbs(common) {
				common = filepath.Clean(filepath.Join(gitDir, raw))
			}
			if common != gitDir {
				out = append(out, common)
			}
		}
	}
	return out
}
