// Sandbox configuration: types, conversion, effective config, and sources.
package safety

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

type ToolKind string

const (
	ToolKindShell     ToolKind = "shell"
	ToolKindUserShell ToolKind = "user_shell"
)

type Platform string

const (
	PlatformDarwin      Platform = "darwin"
	PlatformLinux       Platform = "linux"
	PlatformWSL         Platform = "wsl2"
	PlatformWindows     Platform = "windows"
	PlatformUnsupported Platform = "unsupported"
)

type Mode string

const (
	ModeSandboxed        Mode = "sandboxed"
	ModeDangerFullAccess Mode = "danger-full-access"
)

// Profile defines the filesystem privilege available to one sandboxed command.
// It is independent from command assessment: the operating-system sandbox, not
// a parser heuristic, enforces this boundary.
type Profile string

const (
	ProfileWorkspaceWrite Profile = "workspace-write"
	ProfileReadOnly       Profile = "read-only"
	ProfileManaged        Profile = "managed"
)

func (p Profile) normalized() Profile {
	switch p {
	case ProfileReadOnly, ProfileManaged:
		return p
	default:
		return ProfileWorkspaceWrite
	}
}

type SandboxPermissions string

const (
	SandboxPermissionsUseDefault                SandboxPermissions = "use_default"
	SandboxPermissionsRequireEscalated          SandboxPermissions = "require_escalated"
	SandboxPermissionsWithAdditionalPermissions SandboxPermissions = "with_additional_permissions"
)

func (p SandboxPermissions) Valid() bool {
	return p == "" || p == SandboxPermissionsUseDefault || p == SandboxPermissionsRequireEscalated || p == SandboxPermissionsWithAdditionalPermissions
}

func (p SandboxPermissions) Normalized() SandboxPermissions {
	if p == SandboxPermissionsRequireEscalated || p == SandboxPermissionsWithAdditionalPermissions {
		return p
	}
	return SandboxPermissionsUseDefault
}

type SandboxType string

const (
	SandboxTypeNone                   SandboxType = "none"
	SandboxTypeMacosSeatbelt          SandboxType = "macos_seatbelt"
	SandboxTypeLinuxSeccomp           SandboxType = "linux_seccomp"
	SandboxTypeWindowsRestrictedToken SandboxType = "windows_restricted_token"
)

type BackendName string

const (
	BackendSeatbelt   BackendName = "seatbelt"
	BackendBubblewrap BackendName = "bubblewrap"
	BackendWindows    BackendName = "windows"
	BackendHost       BackendName = "host"
)

type SandboxDecision struct {
	Mode               Mode
	Profile            Profile
	UseSandbox         bool
	Reason             string
	Backend            BackendName
	SandboxType        SandboxType
	UnavailableReason  string
	RequiresPermission bool
}

func (d SandboxDecision) ModeString() string {
	return string(d.Mode)
}

type CommandRequest struct {
	ToolKind ToolKind
	Command  string
	WorkDir  string
	Timeout  time.Duration
	// Profile is the OS-enforced filesystem profile for this one command.
	// Empty retains the regular workspace-write behavior for existing callers.
	Profile                 Profile
	SandboxPermissions      SandboxPermissions
	AdditionalWritablePaths []string
	// ExclusiveWritablePaths declares AdditionalWritablePaths to be this
	// command's complete write set: the configured allow-write roots (the
	// workspace, the temp directory, sandbox_workspace_write.writable_roots)
	// are not added on top of it.
	//
	// It exists for the caller that resolves its own write set from structured
	// grants and then promotes a read-only request to the managed profile to
	// keep the strict boundary while naming the exceptions. Without it that
	// promotion reads "these paths, and also the whole workspace", which is the
	// opposite of what it was for: a plan-mode command granted its build cache
	// would also gain the ability to edit the project it is planning.
	//
	// This narrows what the request may write. Denials are unaffected: the
	// protected-path deny list is not a grant that needs narrowing.
	ExclusiveWritablePaths  bool
	DeniedWritablePaths     []string
	AdditionalReadablePaths []string
	DeniedReadablePaths     []string
	DeniedReadablePatterns  []string
	IncludePlatformDefaults bool
	AllowNetwork            bool
	ManagedNetwork          *ManagedNetworkCommand
	ApprovedNetworkAccess   *ApprovedNetworkAccess
	NetworkApproval         NetworkApprovalHandler
	NetworkEnvironmentID    string
	NetworkExecutionID      string
	SessionNetworkRules     []SessionNetworkRule
	Env                     []string
	// OutputSpoolDir is a Forebrain-owned directory used to preserve large output
	// without retaining it in memory. Empty falls back to the OS temp dir.
	OutputSpoolDir string
	// OnOutput receives stdout/stderr chunks while the command is running.
	// The callback is invoked synchronously from the process pipe readers and
	// must return quickly. CommandResult contains a bounded preview, byte counts,
	// and an optional disk spool for explicit retrieval of larger output.
	OnOutput func(stream OutputStream, chunk []byte) `json:"-"`
}

type OutputStream string

const (
	OutputStreamStdout OutputStream = "stdout"
	OutputStreamStderr OutputStream = "stderr"
)

type CommandResult struct {
	Stdout string
	Stderr string
	// AggregatedOutput preserves the observed stdout/stderr chunk order.
	AggregatedOutput        string
	StdoutBytes             int64
	StderrBytes             int64
	StdoutOmittedBytes      int64
	StderrOmittedBytes      int64
	OutputSpoolPath         string
	OutputSpoolBytes        int64
	OutputSpoolOmittedBytes int64
	ExitCode                int
	ExecutionTiming         llm.ExecutionTiming
	NetworkDenial           *NetworkDenial
}

type ManagedNetworkCommand struct {
	HTTPPort            int
	SOCKSPort           int
	SOCKSUDPPort        int
	AllowLocalBinding   bool
	AllowedUnixSockets  []string
	AllowAllUnixSockets bool
}

type ApprovedNetworkAccess struct {
	Context NetworkApprovalContext
	Port    int
}

type NetworkApprovalDecision string

const (
	NetworkApprovalAllowOnce       NetworkApprovalDecision = "allow_once"
	NetworkApprovalAllowForSession NetworkApprovalDecision = "allow_for_session"
	NetworkApprovalAllowInFuture   NetworkApprovalDecision = "allow_in_future"
	NetworkApprovalDeny            NetworkApprovalDecision = "deny"
	NetworkApprovalDenyForSession  NetworkApprovalDecision = "deny_for_session"
	NetworkApprovalCancel          NetworkApprovalDecision = "cancel"
)

func (d NetworkApprovalDecision) Allows() bool {
	return d == NetworkApprovalAllowOnce || d == NetworkApprovalAllowForSession || d == NetworkApprovalAllowInFuture
}

type NetworkApprovalRequest struct {
	Context       NetworkApprovalContext `json:"context"`
	Port          int                    `json:"port"`
	Command       string                 `json:"command,omitempty"`
	WorkDir       string                 `json:"work_dir,omitempty"`
	EnvironmentID string                 `json:"environment_id,omitempty"`
	ExecutionID   string                 `json:"execution_id,omitempty"`
	CommandEnv    []string               `json:"-"`
}

type SessionNetworkRule struct {
	Context NetworkApprovalContext
	Port    int
	Allow   bool
}

type NetworkApprovalHandler func(context.Context, NetworkApprovalRequest) (NetworkApprovalDecision, error)

type NetworkDenial struct {
	Host     string                  `json:"host"`
	Reason   string                  `json:"reason"`
	Method   string                  `json:"method,omitempty"`
	Mode     string                  `json:"mode,omitempty"`
	Protocol NetworkApprovalProtocol `json:"protocol"`
	Decision string                  `json:"decision"`
	Source   string                  `json:"source,omitempty"`
	Port     int                     `json:"port,omitempty"`
}

type UnavailableError struct {
	Reason string
}

type TimeoutError struct {
	Result CommandResult
}

func (e *TimeoutError) Error() string { return "command timed out" }
func (e *TimeoutError) Unwrap() error { return context.DeadlineExceeded }

type SignalError struct {
	Signal int
}

func (e *SignalError) Error() string { return fmt.Sprintf("command terminated by signal %d", e.Signal) }

type DeniedError struct {
	Result CommandResult
}

func (e *DeniedError) Error() string { return "sandbox denied command" }

func (e *UnavailableError) Error() string {
	if e == nil || e.Reason == "" {
		return "sandbox unavailable"
	}
	return "sandbox unavailable: " + e.Reason
}

type Status struct {
	EffectiveEnabled  bool
	Platform          Platform
	Backend           BackendName
	Mode              Mode
	SettingsLocked    bool
	UnavailableReason string
}

type DoctorReport struct {
	Platform          Platform
	Backend           BackendName
	DependencyErrors  []string
	DependencyWarning []string
}

type FilesystemConfig struct {
	AllowWrite              []string
	DenyWrite               []string
	AllowRead               []string
	DenyRead                []string
	DenyReadPatterns        []string
	GlobScanMaxDepth        *int
	IncludePlatformDefaults bool
}

type RuntimeConfig struct {
	Filesystem     FilesystemConfig
	NetworkEnabled bool
}

func ConvertToRuntimeConfig(sources []SourceSettings, snap Snapshot, cwd, originalCwd, tempDir string, additionalDirs []string) RuntimeConfig {
	var out RuntimeConfig
	profileConfig, profileActive := compileConfiguredPermissionProfile(sources, cwd, tempDir, additionalDirs)
	if profileActive {
		out = profileConfig
	}

	excludeTmpdirEnvVar := false
	excludeSlashTmp := false
	for _, src := range sources {
		excludeTmpdirEnvVar = excludeTmpdirEnvVar || src.SandboxWorkspaceWrite.ExcludeTmpdirEnvVar
		excludeSlashTmp = excludeSlashTmp || src.SandboxWorkspaceWrite.ExcludeSlashTmp
	}
	if !profileActive {
		root := string(filepath.Separator)
		if volume := filepath.VolumeName(cwd); volume != "" {
			root = volume + string(filepath.Separator)
		}
		out.Filesystem.AllowRead = appendUniquePath(out.Filesystem.AllowRead, root)
		workspaceWrite := false
		for _, src := range sources {
			workspaceWrite = workspaceWrite || src.SandboxMode == appcfg.SandboxModeWorkspaceWrite
		}
		if workspaceWrite {
			out.Filesystem.AllowWrite = appendUniquePath(out.Filesystem.AllowWrite, cwd)
			if !excludeTmpdirEnvVar {
				out.Filesystem.AllowWrite = appendUniquePath(out.Filesystem.AllowWrite, tempDir)
			}
			if !excludeSlashTmp && filepath.Separator == '/' {
				out.Filesystem.AllowWrite = appendUniquePath(out.Filesystem.AllowWrite, "/tmp")
			}
			for _, p := range additionalDirs {
				out.Filesystem.AllowWrite = appendUniquePath(out.Filesystem.AllowWrite, p)
			}
		}
	}

	for _, src := range sources {
		if !profileActive {
			for _, p := range src.SandboxWorkspaceWrite.WritableRoots {
				out.Filesystem.AllowWrite = appendUniquePath(out.Filesystem.AllowWrite, resolveSandboxPath(src.RootPath, p))
			}
			out.NetworkEnabled = out.NetworkEnabled || src.SandboxWorkspaceWrite.EffectiveNetworkAccess()
		}
	}

	for _, grant := range snap.FileSystemGrants {
		if grant.Scope == GrantScopeTurn && strings.TrimSpace(grant.RunID) != "" {
			continue
		}
		p, resolved := grant.Entry.Path.Resolve(cwd)
		if resolved && grant.Entry.MissingPathBehavior == FileSystemPermissionMissingPathSkip {
			if _, err := os.Lstat(p); os.IsNotExist(err) {
				continue
			}
		}
		switch grant.Entry.Access {
		case FileSystemAccessWrite:
			if !resolved {
				continue
			}
			out.Filesystem.AllowWrite = appendUniquePath(out.Filesystem.AllowWrite, p)
			out.Filesystem.AllowRead = appendUniquePath(out.Filesystem.AllowRead, p)
		case FileSystemAccessRead:
			if !resolved {
				continue
			}
			out.Filesystem.AllowRead = appendUniquePath(out.Filesystem.AllowRead, p)
		case FileSystemAccessDeny:
			if grant.Entry.Path.Type == FileSystemPermissionPathTypeGlobPattern {
				hadPatterns := len(out.Filesystem.DenyReadPatterns) > 0
				out.Filesystem.DenyReadPatterns = appendUniqueString(out.Filesystem.DenyReadPatterns, grant.Entry.Path.Pattern)
				if !hadPatterns {
					out.Filesystem.GlobScanMaxDepth = cloneDepth(grant.GlobScanMaxDepth)
				} else {
					out.Filesystem.GlobScanMaxDepth = mergeGlobScanMaxDepth(out.Filesystem.GlobScanMaxDepth, grant.GlobScanMaxDepth)
				}
			} else if resolved {
				out.Filesystem.DenyRead = appendUniquePath(out.Filesystem.DenyRead, p)
				out.Filesystem.DenyWrite = appendUniquePath(out.Filesystem.DenyWrite, p)
			}
		}
	}

	for _, p := range protectedWritePaths(cwd, originalCwd) {
		out.Filesystem.DenyWrite = appendUniquePath(out.Filesystem.DenyWrite, p)
	}

	return out
}

func cloneDepth(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func appendUniqueString(dst []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return dst
	}
	for _, current := range dst {
		if current == value {
			return dst
		}
	}
	return append(dst, value)
}

func mergeGlobScanMaxDepth(current, incoming *int) *int {
	if current == nil || incoming == nil {
		return nil
	}
	value := *current
	if *incoming > value {
		value = *incoming
	}
	return &value
}

func resolveSandboxPath(root, raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" {
		return ""
	}
	switch {
	case strings.HasPrefix(p, "//"):
		return filepath.Clean(p[1:])
	case strings.HasPrefix(p, "~"):
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Clean(strings.TrimPrefix(p, "~"))
		}
		return filepath.Clean(filepath.Join(home, strings.TrimPrefix(p, "~")))
	case filepath.IsAbs(p):
		return filepath.Clean(p)
	default:
		base := strings.TrimSpace(root)
		if base == "" {
			base = "."
		}
		return filepath.Clean(filepath.Join(base, p))
	}
}

func appendUniquePath(dst []string, raw string) []string {
	p := strings.TrimSpace(raw)
	if p == "" {
		return dst
	}
	p = filepath.Clean(p)
	for _, cur := range dst {
		if cur == p {
			return dst
		}
	}
	return append(dst, p)
}

// ProjectContext is the fixed launch-project trust decision used to derive an
// omitted sandbox mode. It is runtime state only; callers must never save the
// effective configuration back to forebrain.yaml.
type ProjectContext struct {
	Project    Project
	TrustLevel Level
}

// IsTrusted reports an explicit trust decision. A project nobody has judged is
// neither trusted nor untrusted.
func (c ProjectContext) IsTrusted() bool {
	return c.TrustLevel == LevelTrusted
}

// IsUntrusted reports an explicit rejection, which is distinct from the absence
// of a decision.
func (c ProjectContext) IsUntrusted() bool {
	return c.TrustLevel == LevelUntrusted
}

// ResolveProjectContext resolves the canonical launch project and its persisted
// trust decision. Callers that cannot safely prompt should use the returned
// error to report/log the failure and the returned false-trust context to fail
// closed.
func ResolveProjectContext(home, cwd string) (ProjectContext, error) {
	project, err := Resolve(cwd)
	if err != nil {
		return ProjectContext{}, err
	}
	level, err := TrustLevel(home, project)
	if err != nil {
		return ProjectContext{Project: project}, err
	}
	return ProjectContext{Project: project, TrustLevel: level}, nil
}

// EffectiveConfig derives the settings a launch project leaves unspecified and
// returns them in a copy of cfg. Configured values always win; only omitted
// settings are derived. The two derivations are independent: an explicit
// sandbox mode does not freeze the approval policy, and vice versa.
func EffectiveConfig(cfg appcfg.Root, launch ProjectContext) appcfg.Root {
	cfg.ApprovalPolicy = effectiveApprovalPolicy(cfg.ApprovalPolicy, launch)
	if strings.TrimSpace(cfg.DefaultPermissions) != "" || cfg.SandboxMode != "" {
		return cfg
	}
	cfg.SandboxMode = effectiveSandboxMode(launch, detectPlatform(), cfg.Windows.Sandbox)
	return cfg
}

// effectiveSandboxMode resolves the sandbox mode for a project that does not
// configure one.
//
// Any explicit trust decision earns a writable workspace, including a
// rejection: a rejected project is held back by its approval policy, which asks
// before anything that is not provably safe, rather than by a read-only
// filesystem. A project nobody has judged yet has no such backstop — its policy
// only asks when work leaves the sandbox — so the sandbox is what protects it
// and it stays read-only.
//
// Windows without a configured sandbox has no enforcement to fall back on, so
// it stays read-only whatever the trust decision.
func effectiveSandboxMode(launch ProjectContext, platform Platform, windowsSandbox appcfg.WindowsSandboxMode) appcfg.SandboxMode {
	decided := launch.IsTrusted() || launch.IsUntrusted()
	sandboxUnavailable := platform == PlatformWindows && strings.TrimSpace(string(windowsSandbox)) == ""
	if decided && !sandboxUnavailable {
		return appcfg.SandboxModeWorkspaceWrite
	}
	return appcfg.SandboxModeReadOnly
}

// effectiveApprovalPolicy resolves the approval policy for a project that does
// not configure one. A project the user has explicitly rejected asks before
// anything that is not provably safe; a trusted project, and equally a project
// nobody has judged yet, asks only when work needs to leave the sandbox.
//
// The derived value is runtime state. It must never be written back to
// forebrain.yaml: persisting it would pin one branch of this derivation and the
// project's trust decision could no longer change the policy.
func effectiveApprovalPolicy(configured appcfg.ApprovalPolicyConfig, launch ProjectContext) appcfg.ApprovalPolicyConfig {
	if configured.Mode != "" {
		return configured
	}
	if launch.IsUntrusted() {
		return appcfg.NewApprovalPolicy(appcfg.ApprovalPolicyUntrusted)
	}
	return appcfg.NewApprovalPolicy(appcfg.ApprovalPolicyOnRequest)
}

// ResolveEffectiveConfig combines non-interactive trust lookup with effective
// config derivation. Trust errors return a read-only effective config so a
// caller that logs and continues can never accidentally become writable.
func ResolveEffectiveConfig(cfg appcfg.Root, home, cwd string) (appcfg.Root, ProjectContext, error) {
	launch, err := ResolveProjectContext(home, cwd)
	return EffectiveConfig(cfg, launch), launch, err
}

type SettingsSource string

const (
	SourceUserSettings   SettingsSource = "userSettings"
	SettingsProject      SettingsSource = "projectSettings"
	SettingsLocal        SettingsSource = "localSettings"
	SourceFlagSettings   SettingsSource = "flagSettings"
	SourcePolicySettings SettingsSource = "policySettings"
	SourceCLIArg         SettingsSource = "cliArg"
	SourceCommand        SettingsSource = "command"
	SettingsSession      SettingsSource = "session"
)

type SourceSettings struct {
	Source                SettingsSource
	RootPath              string
	DefaultPermissions    string
	Permissions           appcfg.PermissionProfiles
	SandboxMode           appcfg.SandboxMode
	SandboxWorkspaceWrite appcfg.SandboxWorkspaceWrite
	NetworkConstraints    *appcfg.NetworkConstraints
}

type MergedSettings struct {
	Sources            []SourceSettings
	PermissionSnapshot Snapshot
}

func AreSandboxSettingsLockedByPolicy(sources []SourceSettings) bool {
	for _, src := range sources {
		switch src.Source {
		case SourcePolicySettings, SourceFlagSettings, SourceCLIArg:
			if src.SandboxMode != "" {
				return true
			}
		}
	}
	return false
}

func LocalConfigSources(home string, cfg *appcfg.Root) []SourceSettings {
	if cfg == nil {
		return nil
	}
	return []SourceSettings{{
		Source:                SettingsLocal,
		RootPath:              strings.TrimSpace(home),
		DefaultPermissions:    strings.TrimSpace(cfg.DefaultPermissions),
		Permissions:           cfg.Permissions,
		SandboxMode:           cfg.SandboxMode,
		SandboxWorkspaceWrite: cfg.SandboxWorkspaceWrite,
	}}
}

func UpdateManagerWithLocalConfig(m *Manager, home string, cfg *appcfg.Root, snap Snapshot, additionalDirs []string) RuntimeConfig {
	if m == nil {
		return RuntimeConfig{}
	}
	cwd, _ := os.Getwd()
	tempDir := os.TempDir()
	return m.UpdateConfig(cfg, LocalConfigSources(home, cfg), snap, cwd, cwd, tempDir, additionalDirs)
}
