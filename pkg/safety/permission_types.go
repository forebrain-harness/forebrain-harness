// Permission vocabulary: types, decisions, profiles, presets, and options.
package safety

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

type PermissionMode string

const (
	ModeUnlessTrusted PermissionMode = "unless-trusted"
	ModeOnRequest     PermissionMode = "on-request"
	ModeNever         PermissionMode = "never"
)

type PermissionBehavior string

const (
	BehaviorAllow PermissionBehavior = "allow"
	BehaviorDeny  PermissionBehavior = "deny"
	BehaviorAsk   PermissionBehavior = "ask"
)

type ExecRequirement string

const (
	ExecSkip          ExecRequirement = "skip"
	ExecNeedsApproval ExecRequirement = "needs_approval"
	ExecForbidden     ExecRequirement = "forbidden"
)

// AskForApproval selects when execution may request user approval.
type AskForApproval = PermissionMode

const (
	ApprovalUnlessTrusted AskForApproval = ModeUnlessTrusted
	ApprovalOnRequest     AskForApproval = ModeOnRequest
	ApprovalNever         AskForApproval = ModeNever
)

type ApprovalPolicy struct {
	Mode AskForApproval `json:"mode" yaml:"mode"`
}

type PermissionSource string

// Every source below has a destination that can write it. A source with no
// destination could never hold a rule, so it is not modelled: the priority list
// stays a description of what actually competes during evaluation.
const (
	SourceProjectSettings PermissionSource = "projectSettings"
	SourceLocalSettings   PermissionSource = "localSettings"
	SourceSession         PermissionSource = "session"
)

// MayPermit reports whether rules from src can permit an action at all, i.e.
// carry the allow behavior.
//
// A source that cannot permit may still restrict: deny and ask are honoured
// from every source. This is what makes a project's committed rules safe to
// load — a repository can declare what is forbidden or must be confirmed for
// everyone working in it, but it can never decide that something is routine on
// someone else's machine. Permitting stays with the operator.
func MayPermit(src PermissionSource) bool {
	switch src {
	case SourceSession, SourceLocalSettings:
		return true
	default:
		return false
	}
}

// MayWidenSandbox reports whether rules from src are allowed to widen what the
// sandbox permits — lifting it for a command, or opening network egress.
//
// This is a whitelist on purpose. Only sources the operator controls directly
// qualify: a project's rules are committed to a repository, so honouring a
// widening from them would let any repository the operator trusted escalate
// itself the moment it is checked out. Narrowing is unrestricted — a deny from
// any source is always honoured — and a new source added later cannot widen
// anything until it is listed here deliberately.
func MayWidenSandbox(src PermissionSource) bool {
	switch src {
	case SourceSession, SourceLocalSettings:
		return true
	default:
		return false
	}
}

type PermissionDestination string

const (
	DestinationSession         PermissionDestination = "session"
	DestinationLocalSettings   PermissionDestination = "localSettings"
	DestinationProjectSettings PermissionDestination = "projectSettings"
)

type PermissionRuleValue struct {
	ToolName string `json:"tool_name"`
	// RuleContent is a pattern whose kind is inferred from its text: a
	// ":*" suffix makes it a prefix, an unescaped "*" makes it a wildcard,
	// anything else is literal. That inference is why the two typed fields
	// below exist — a command that legitimately contains "*" cannot say
	// "literally this" in a string whose meaning its own characters decide.
	RuleContent string `json:"rule_content,omitempty"`
	// CommandPrefix authorizes every shell command starting with these tokens.
	CommandPrefix []string `json:"command_prefix,omitempty" yaml:"command_prefix,omitempty"`
	// Command authorizes exactly this shell command and nothing else. It is
	// never read as a pattern, so a glob or a quote in it is just text.
	Command       string `json:"command,omitempty" yaml:"command,omitempty"`
	BypassSandbox bool   `json:"bypass_sandbox,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
}

type PermissionRule struct {
	Behavior PermissionBehavior  `json:"behavior"`
	Value    PermissionRuleValue `json:"value"`
	Source   PermissionSource    `json:"source"`
}

type PermissionGrantScope string

const (
	GrantScopeTurn    PermissionGrantScope = "turn"
	GrantScopeSession PermissionGrantScope = "session"
)

type FileSystemPermissionAccess string

const (
	FileSystemAccessRead  FileSystemPermissionAccess = "read"
	FileSystemAccessWrite FileSystemPermissionAccess = "write"
	FileSystemAccessDeny  FileSystemPermissionAccess = "deny"
)

func (a *FileSystemPermissionAccess) UnmarshalJSON(data []byte) error {
	if a == nil {
		return fmt.Errorf("nil filesystem permission access")
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value == "none" {
		value = string(FileSystemAccessDeny)
	}
	*a = FileSystemPermissionAccess(value)
	return nil
}

type FileSystemPermissionPathType string

const (
	FileSystemPermissionPathTypePath        FileSystemPermissionPathType = "path"
	FileSystemPermissionPathTypeGlobPattern FileSystemPermissionPathType = "glob_pattern"
	FileSystemPermissionPathTypeSpecial     FileSystemPermissionPathType = "special"
)

type FileSystemSpecialPathKind string

const (
	FileSystemSpecialPathRoot         FileSystemSpecialPathKind = "root"
	FileSystemSpecialPathMinimal      FileSystemSpecialPathKind = "minimal"
	FileSystemSpecialPathProjectRoots FileSystemSpecialPathKind = "project_roots"
	FileSystemSpecialPathTmpdir       FileSystemSpecialPathKind = "tmpdir"
	FileSystemSpecialPathSlashTmp     FileSystemSpecialPathKind = "slash_tmp"
	FileSystemSpecialPathUnknown      FileSystemSpecialPathKind = "unknown"
)

// The advertised `kind` enum is deliberately narrower than the constant set
// above: it lists only the kinds Resolve handles. `minimal` and `unknown`
// validate but fall through Resolve's default and therefore never match a
// path — a caller that picks one gets an entry that silently grants nothing.
// They stay accepted on the wire (foreign and legacy payloads carry them, and
// agentnotify renders them), as does the `current_working_directory` alias
// normalized in normalizeFileSystemSpecialPath; none of the three is something
// a caller should be steered toward choosing.
type FileSystemSpecialPath struct {
	Kind    FileSystemSpecialPathKind `json:"kind" jsonschema:"enum=root,enum=project_roots,enum=tmpdir,enum=slash_tmp" jsonschema_description:"Named location: root (filesystem root), project_roots (the workspace), tmpdir ($TMPDIR), or slash_tmp (/tmp; unavailable on Windows)."`
	Subpath string                    `json:"subpath,omitempty" jsonschema_description:"Path relative to the named location. Only valid with project_roots."`
	Path    string                    `json:"path,omitempty" jsonschema_description:"Reserved for round-tripping foreign permission payloads. Use a path-type entry instead of setting this."`
}

type FileSystemPermissionPath struct {
	Type    FileSystemPermissionPathType `json:"type" jsonschema:"enum=path,enum=glob_pattern,enum=special" jsonschema_description:"Which of path, pattern, or value carries the location."`
	Path    string                       `json:"path,omitempty" jsonschema:"description=Filesystem path."`
	Pattern string                       `json:"pattern,omitempty" jsonschema_description:"Glob pattern, when type is glob_pattern."`
	Value   *FileSystemSpecialPath       `json:"value,omitempty" jsonschema_description:"Named location, when type is special."`
}

func NewFileSystemPermissionPath(path string) FileSystemPermissionPath {
	return FileSystemPermissionPath{Type: FileSystemPermissionPathTypePath, Path: path}
}

func (p FileSystemPermissionPath) normalizedType() FileSystemPermissionPathType {
	if p.Type == "" && strings.TrimSpace(p.Path) != "" {
		return FileSystemPermissionPathTypePath
	}
	return p.Type
}

func (p FileSystemPermissionPath) MarshalJSON() ([]byte, error) {
	type wire FileSystemPermissionPath
	p.Type = p.normalizedType()
	return json.Marshal(wire(p))
}

func (p *FileSystemPermissionPath) UnmarshalJSON(data []byte) error {
	if p == nil {
		return fmt.Errorf("nil filesystem permission path")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var raw struct {
		Type    FileSystemPermissionPathType `json:"type"`
		Path    string                       `json:"path"`
		Pattern string                       `json:"pattern"`
		Value   *FileSystemSpecialPath       `json:"value"`
	}
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	switch raw.Type {
	case FileSystemPermissionPathTypePath:
		if strings.TrimSpace(raw.Path) == "" || raw.Pattern != "" || raw.Value != nil {
			return fmt.Errorf("filesystem path entry requires only `path`")
		}
	case FileSystemPermissionPathTypeGlobPattern:
		if strings.TrimSpace(raw.Pattern) == "" || raw.Path != "" || raw.Value != nil {
			return fmt.Errorf("filesystem glob entry requires only `pattern`")
		}
	case FileSystemPermissionPathTypeSpecial:
		if raw.Value == nil || raw.Path != "" || raw.Pattern != "" {
			return fmt.Errorf("filesystem special entry requires only `value`")
		}
		normalizeFileSystemSpecialPath(raw.Value)
		if err := validateFileSystemSpecialPath(*raw.Value); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid filesystem permission path type %q", raw.Type)
	}
	*p = FileSystemPermissionPath{Type: raw.Type, Path: raw.Path, Pattern: raw.Pattern, Value: raw.Value}
	return nil
}

func validateFileSystemSpecialPath(value FileSystemSpecialPath) error {
	switch value.Kind {
	case FileSystemSpecialPathRoot, FileSystemSpecialPathMinimal, FileSystemSpecialPathTmpdir, FileSystemSpecialPathSlashTmp:
		if value.Subpath != "" || value.Path != "" {
			return fmt.Errorf("filesystem special path %q does not accept parameters", value.Kind)
		}
	case FileSystemSpecialPathProjectRoots:
		if value.Path != "" {
			return fmt.Errorf("filesystem special path %q does not accept `path`", value.Kind)
		}
	case FileSystemSpecialPathUnknown:
		if strings.TrimSpace(value.Path) == "" {
			return fmt.Errorf("unknown filesystem special path requires `path`")
		}
	default:
		return fmt.Errorf("invalid filesystem special path kind %q", value.Kind)
	}
	return nil
}

func normalizeFileSystemSpecialPath(value *FileSystemSpecialPath) {
	if value != nil && value.Kind == "current_working_directory" {
		value.Kind = FileSystemSpecialPathProjectRoots
	}
}

func (p FileSystemPermissionPath) Key() string {
	switch p.normalizedType() {
	case FileSystemPermissionPathTypePath:
		return "path\x00" + filepath.Clean(strings.TrimSpace(p.Path))
	case FileSystemPermissionPathTypeGlobPattern:
		return "glob\x00" + strings.TrimSpace(p.Pattern)
	case FileSystemPermissionPathTypeSpecial:
		if p.Value == nil {
			return "special\x00"
		}
		return "special\x00" + string(p.Value.Kind) + "\x00" + p.Value.Subpath + "\x00" + p.Value.Path
	default:
		return ""
	}
}

func (p FileSystemPermissionPath) Resolve(cwd string) (string, bool) {
	cwd = filepath.Clean(strings.TrimSpace(cwd))
	switch p.normalizedType() {
	case FileSystemPermissionPathTypePath:
		path := strings.TrimSpace(p.Path)
		if path == "" {
			return "", false
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		return filepath.Clean(path), filepath.IsAbs(path)
	case FileSystemPermissionPathTypeSpecial:
		if p.Value == nil {
			return "", false
		}
		switch p.Value.Kind {
		case FileSystemSpecialPathRoot:
			if cwd == "" || !filepath.IsAbs(cwd) {
				return "", false
			}
			volume := filepath.VolumeName(cwd)
			return filepath.Clean(volume + string(filepath.Separator)), true
		case FileSystemSpecialPathProjectRoots:
			if cwd == "" || !filepath.IsAbs(cwd) {
				return "", false
			}
			if strings.TrimSpace(p.Value.Subpath) == "" {
				return cwd, true
			}
			subpath := strings.TrimSpace(p.Value.Subpath)
			if filepath.IsAbs(subpath) {
				return filepath.Clean(subpath), true
			}
			return filepath.Clean(filepath.Join(cwd, subpath)), true
		case FileSystemSpecialPathTmpdir:
			tmpdir := strings.TrimSpace(os.Getenv("TMPDIR"))
			if tmpdir == "" || !filepath.IsAbs(tmpdir) {
				return "", false
			}
			return filepath.Clean(tmpdir), true
		case FileSystemSpecialPathSlashTmp:
			if runtime.GOOS == "windows" {
				return "", false
			}
			if info, err := os.Stat("/tmp"); err != nil || !info.IsDir() {
				return "", false
			}
			return "/tmp", true
		default:
			return "", false
		}
	default:
		return "", false
	}
}

func (p FileSystemPermissionPath) Matches(candidate, cwd string) bool {
	candidate = filepath.Clean(strings.TrimSpace(candidate))
	if candidate == "" || !filepath.IsAbs(candidate) {
		return false
	}
	if p.normalizedType() == FileSystemPermissionPathTypeGlobPattern {
		pattern := strings.TrimSpace(p.Pattern)
		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(cwd, pattern)
		}
		for _, patternCandidate := range permissionPathCandidates(pattern, false) {
			for _, pathCandidate := range permissionPathCandidates(candidate, true) {
				if pathWildcardMatch(filepath.ToSlash(patternCandidate), filepath.ToSlash(pathCandidate)) {
					return true
				}
			}
		}
		return false
	}
	root, ok := p.Resolve(cwd)
	if !ok {
		return false
	}
	for _, pathCandidate := range permissionPathCandidates(candidate, true) {
		for _, rootCandidate := range permissionPathCandidates(root, true) {
			if permissionPathWithin(pathCandidate, rootCandidate) {
				return true
			}
		}
	}
	return false
}

func permissionPathCandidates(path string, resolveSymlinks bool) []string {
	path = filepath.Clean(strings.TrimSpace(path))
	values := []string{path}
	appendValue := func(value string) {
		value = filepath.Clean(value)
		for _, existing := range values {
			if existing == value {
				return
			}
		}
		values = append(values, value)
	}
	if resolveSymlinks {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			appendValue(resolved)
		}
	}
	if strings.HasPrefix(path, "/var/") {
		appendValue("/private" + path)
	}
	if strings.HasPrefix(path, "/private/var/") {
		appendValue(strings.TrimPrefix(path, "/private"))
	}
	return values
}

type FileSystemPermissionEntry struct {
	Path                FileSystemPermissionPath                `json:"path"`
	Access              FileSystemPermissionAccess              `json:"access" jsonschema:"enum=read,enum=write,enum=deny"`
	MissingPathBehavior FileSystemPermissionMissingPathBehavior `json:"missing_path_behavior,omitempty" jsonschema:"enum=skip" jsonschema_description:"Set to skip to drop this entry when the path does not exist. Omit to keep it."`
}

type FileSystemPermissionMissingPathBehavior string

const FileSystemPermissionMissingPathSkip FileSystemPermissionMissingPathBehavior = "skip"

type FileSystemPermissionProfile struct {
	Read    []string                    `json:"read,omitempty" jsonschema:"description=Paths to allow reading."`
	Write   []string                    `json:"write,omitempty" jsonschema:"description=Paths to allow writing."`
	Entries []FileSystemPermissionEntry `json:"entries,omitempty"`
	// A nil depth scans without a bound when deny globs are present.
	GlobScanMaxDepth *int `json:"glob_scan_max_depth,omitempty"`
}

func (p FileSystemPermissionProfile) MarshalJSON() ([]byte, error) {
	read := append([]string(nil), p.Read...)
	write := append([]string(nil), p.Write...)
	legacy := p.GlobScanMaxDepth == nil
	if legacy {
		for _, entry := range p.Entries {
			if entry.Path.normalizedType() != FileSystemPermissionPathTypePath ||
				entry.Access == FileSystemAccessDeny || entry.MissingPathBehavior != "" {
				legacy = false
				break
			}
			switch entry.Access {
			case FileSystemAccessRead:
				read = append(read, entry.Path.Path)
			case FileSystemAccessWrite:
				write = append(write, entry.Path.Path)
			default:
				legacy = false
			}
		}
	}
	if legacy {
		return json.Marshal(struct {
			Read  []string `json:"read,omitempty"`
			Write []string `json:"write,omitempty"`
		}{Read: read, Write: write})
	}
	return json.Marshal(struct {
		Entries          []FileSystemPermissionEntry `json:"entries,omitempty"`
		GlobScanMaxDepth *int                        `json:"glob_scan_max_depth,omitempty"`
	}{Entries: p.Entries, GlobScanMaxDepth: p.GlobScanMaxDepth})
}

func (p *FileSystemPermissionProfile) UnmarshalJSON(data []byte) error {
	if p == nil {
		return fmt.Errorf("nil filesystem permission profile")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	canonical := keys["entries"] != nil || keys["glob_scan_max_depth"] != nil
	if canonical && (keys["read"] != nil || keys["write"] != nil) {
		return fmt.Errorf("filesystem permission profile cannot mix entries with read/write roots")
	}
	if canonical {
		for key := range keys {
			if key != "entries" && key != "glob_scan_max_depth" {
				return fmt.Errorf("unknown filesystem permission field %q", key)
			}
		}
		var raw struct {
			Entries          []FileSystemPermissionEntry `json:"entries"`
			GlobScanMaxDepth *int                        `json:"glob_scan_max_depth"`
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&raw); err != nil {
			return err
		}
		*p = FileSystemPermissionProfile{Entries: raw.Entries, GlobScanMaxDepth: raw.GlobScanMaxDepth}
		return nil
	}
	for key := range keys {
		if key != "read" && key != "write" {
			return fmt.Errorf("unknown filesystem permission field %q", key)
		}
	}
	var raw struct {
		Read  []string `json:"read"`
		Write []string `json:"write"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	*p = FileSystemPermissionProfile{Read: raw.Read, Write: raw.Write}
	return nil
}

type NetworkPermissionProfile struct {
	Enabled *bool `json:"enabled,omitempty"`
}

func NewNetworkPermissionProfile(enabled bool) *NetworkPermissionProfile {
	return &NetworkPermissionProfile{Enabled: &enabled}
}

func (p *NetworkPermissionProfile) IsEmpty() bool {
	return p == nil || p.Enabled == nil
}

func (p *NetworkPermissionProfile) AllowsNetwork() bool {
	return p != nil && p.Enabled != nil && *p.Enabled
}

type RequestPermissionProfile struct {
	Network    *NetworkPermissionProfile    `json:"network,omitempty"`
	FileSystem *FileSystemPermissionProfile `json:"file_system,omitempty"`
}

func (p *RequestPermissionProfile) UnmarshalJSON(data []byte) error {
	if p == nil {
		return fmt.Errorf("nil request permission profile")
	}
	var raw struct {
		Network    *NetworkPermissionProfile    `json:"network"`
		FileSystem *FileSystemPermissionProfile `json:"file_system"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	p.Network = raw.Network
	p.FileSystem = raw.FileSystem
	return nil
}

type RequestPermissionsResponse struct {
	Permissions      RequestPermissionProfile `json:"permissions"`
	Scope            PermissionGrantScope     `json:"scope"`
	StrictAutoReview bool                     `json:"strict_auto_review,omitempty"`
}

// FileSystemPermissionGrant is a normalized runtime grant. Turn grants only
// apply when RunID matches; session grants apply to every later run owned by
// the same in-memory permission store.
type FileSystemPermissionGrant struct {
	Entry            FileSystemPermissionEntry `json:"entry"`
	Scope            PermissionGrantScope      `json:"scope"`
	SessionID        string                    `json:"session_id,omitempty"`
	RunID            string                    `json:"run_id,omitempty"`
	GlobScanMaxDepth *int                      `json:"glob_scan_max_depth,omitempty"`
}

type NetworkPermissionGrant struct {
	Enabled   bool                 `json:"enabled"`
	Scope     PermissionGrantScope `json:"scope"`
	SessionID string               `json:"session_id,omitempty"`
	RunID     string               `json:"run_id,omitempty"`
}

// ExecPolicyAmendment is the token prefix proposed for a persistent command
// allow rule.
type ExecPolicyAmendment []string

type PermissionUpdateType string

const (
	UpdateAddRules            PermissionUpdateType = "addRules"
	UpdateReplaceRules        PermissionUpdateType = "replaceRules"
	UpdateRemoveRules         PermissionUpdateType = "removeRules"
	UpdateSetMode             PermissionUpdateType = "setMode"
	UpdateAddPermissionGrants PermissionUpdateType = "addPermissionGrants"

	// UpdateSetSandboxMode sets one conversation's own sandbox mode; it is
	// accepted only with the session destination.
	UpdateSetSandboxMode PermissionUpdateType = "setSandboxMode"
)

type PermissionUpdate struct {
	Type        PermissionUpdateType  `json:"type"`
	Destination PermissionDestination `json:"destination"`
	SessionID   string                `json:"session_id,omitempty"`

	Behavior              PermissionBehavior          `json:"behavior,omitempty"`
	Rules                 []PermissionRuleValue       `json:"rules,omitempty"`
	Mode                  PermissionMode              `json:"mode,omitempty"`
	SandboxMode           appcfg.SandboxMode          `json:"sandbox_mode,omitempty"`
	FileSystemGrants      []FileSystemPermissionGrant `json:"file_system_grants,omitempty"`
	NetworkGrants         []NetworkPermissionGrant    `json:"network_grants,omitempty"`
	StrictAutoReviewRunID string                      `json:"strict_auto_review_run_id,omitempty"`
}

// Preset ids are the stable wire values; the labels are what a picker shows.
// The two ids are the configuration constants themselves, so the value a
// surface sends, the row the store keeps and the setting the panel writes are
// one spelling.
const (
	PresetWorkspaceWrite = string(appcfg.ApprovalPresetWorkspaceWrite)
	PresetFullAccess     = string(appcfg.ApprovalPresetFullAccess)
)

// ApprovalPreset pairs an approval policy with a sandbox mode.
//
// The pair is the whole of the interactive permission surface: how much the
// sandbox permits and when forebrain stops to ask are two halves of one decision,
// and letting them be chosen apart mostly produces states nobody wants (a
// read-only sandbox that never asks, a full-access one that asks about
// everything). Individual rules are declared in settings files instead, where
// they can be reviewed and committed — see the settings destinations in
// runtime.go.
type ApprovalPreset struct {
	// ID is the stable identifier used on the wire and in tests.
	ID string
	// Label is the display name shown in pickers.
	Label string
	// Description says what the preset lets Forebrain Harness do with files and commands;
	// DescriptionFor completes it with what the network settings allow.
	Description string
	// Approval is the mode the permission store runs in.
	Approval AskForApproval
	// SandboxMode is the sandbox the preset selects.
	SandboxMode appcfg.SandboxMode
}

// BuiltinApprovalPresets returns the presets in the order a picker should show
// them, from most to least restrictive.
func BuiltinApprovalPresets() []ApprovalPreset {
	return []ApprovalPreset{
		{
			ID:          PresetWorkspaceWrite,
			Label:       "Default",
			Description: "Forebrain Harness can read and edit files in the current workspace, and run commands. Approval is required to edit other files.",
			Approval:    ApprovalOnRequest,
			SandboxMode: appcfg.SandboxModeWorkspaceWrite,
		},
		{
			ID:          PresetFullAccess,
			Label:       "Full Access",
			Description: "Forebrain Harness can edit files outside this workspace and access the internet without asking for approval. Exercise caution when using.",
			Approval:    ApprovalNever,
			SandboxMode: appcfg.SandboxModeDangerFullAccess,
		},
	}
}

// DescriptionFor is the preset's whole explanation under cfg: what it lets
// Forebrain Harness do with files and commands, then what the commands it runs in the
// sandbox can reach on the network — which the sandbox's network settings
// decide, not the preset. Full Access runs outside the sandbox and says so
// itself.
func (p ApprovalPreset) DescriptionFor(cfg *appcfg.Root) string {
	if p.SandboxMode == appcfg.SandboxModeDangerFullAccess {
		return p.Description
	}
	network := SandboxNetworkInWords(cfg)
	return p.Description + " " + strings.ToUpper(network[:1]) + network[1:]
}

// ApprovalPresetByID resolves a preset from either its id or its display label,
// so a caller can round-trip whichever of the two it happens to be holding.
func ApprovalPresetByID(raw string) (ApprovalPreset, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ApprovalPreset{}, false
	}
	for _, preset := range BuiltinApprovalPresets() {
		if strings.EqualFold(raw, preset.ID) || strings.EqualFold(raw, preset.Label) {
			return preset, true
		}
	}
	return ApprovalPreset{}, false
}

// ApprovalPresetForSandboxMode names the preset a sandbox mode belongs to, so
// a surface that only knows the mode can still say which preset is running. The
// preset and the sandbox mode are one decision now (plan 203), which makes this
// a lookup rather than a match; a mode no preset derives — read-only, the trust
// narrowing of an undecided project — belongs to none of them rather than to the
// nearest one.
func ApprovalPresetForSandboxMode(mode appcfg.SandboxMode) (ApprovalPreset, bool) {
	for _, preset := range BuiltinApprovalPresets() {
		if preset.SandboxMode == mode {
			return preset, true
		}
	}
	return ApprovalPreset{}, false
}

// SessionUpdatesForLaunch is the preset in force for one conversation alone: its
// approval mode and its sandbox mode, both scoped to that conversation in the
// permission store. Nothing is written to the configuration, so the choice
// neither reaches other conversations nor survives into the next session, and a
// configuration reload does not undo it.
//
// The pair is derived for the launch project exactly as the configuration's own
// preset is (EffectiveConfig), which is what keeps the two halves meaning the
// same thing at session scope as at configuration scope. Writing the preset's
// raw sandbox instead handed every session a writable workspace: the default
// preset is narrowed to read-only in a project nobody has judged, and the
// approval half asks more in a project the user rejected. Full access is the
// exception on both counts — it runs uncontained and asks nothing wherever it
// was picked — because that is the YOLO posture the preset means.
func (p ApprovalPreset) SessionUpdatesForLaunch(sessionID string, cfg *appcfg.Root, launch ProjectContext) []PermissionUpdate {
	derived := EffectiveConfig(appcfg.Root{ApprovalPreset: appcfg.ApprovalPresetID(p.ID)}, launch)
	return []PermissionUpdate{
		{Type: UpdateSetMode, Destination: DestinationSession, SessionID: sessionID, Mode: PermissionMode(derived.ApprovalPolicy.Mode)},
		{Type: UpdateSetSandboxMode, Destination: DestinationSession, SessionID: sessionID, SandboxMode: derived.SandboxMode},
	}
}

// ConfigForSnapshot is the configuration one conversation's calls run under:
// cfg itself, or — when the conversation chose its own sandbox mode — a copy
// with that mode in force. Every sandbox decision taken for a call reads it,
// never the runtime's cfg directly, so a choice made for one conversation is
// exactly as wide as that conversation.
func ConfigForSnapshot(cfg *appcfg.Root, snap Snapshot) *appcfg.Root {
	return configForSandboxMode(cfg, snap.SandboxMode)
}

// configForSandboxMode is cfg with mode in force, or cfg itself when mode is
// empty. YOLO outranks a conversation's own choice the same way it outranks
// the rule engine's answer: it comes from the environment, and nothing picked
// inside a session walks it back.
func configForSandboxMode(cfg *appcfg.Root, mode appcfg.SandboxMode) *appcfg.Root {
	if cfg == nil || mode == "" || RuntimeYOLOEnabled() {
		return cfg
	}
	scoped := *cfg
	scoped.SandboxMode = mode
	return &scoped
}

type ApprovalDecisionName string

const (
	DecisionAccept                        ApprovalDecisionName = "accept"
	DecisionAcceptForSession              ApprovalDecisionName = "accept_for_session"
	DecisionAcceptAndRemember             ApprovalDecisionName = "accept_and_remember"
	DecisionAcceptWithExecPolicyAmendment ApprovalDecisionName = "accept_with_execpolicy_amendment"
	DecisionGrantForTurn                  ApprovalDecisionName = "grant_for_turn"
	DecisionGrantForTurnStrictAutoReview  ApprovalDecisionName = "grant_for_turn_with_strict_auto_review"
	DecisionGrantForSession               ApprovalDecisionName = "grant_for_session"
	DecisionDecline                       ApprovalDecisionName = "decline"
	DecisionCancel                        ApprovalDecisionName = "cancel"
)

type ApprovalDecisionOption struct {
	Decision            ApprovalDecisionName
	ExecPolicyAmendment ExecPolicyAmendment
	// CommandRules is what DecisionAcceptAndRemember stores for a Bash
	// command: one rule per clause that needs an approval of its own, which
	// for most commands is the single exact rule the whole line makes.
	CommandRules []PermissionRuleValue
	// CommandScope describes what those rules grant, so every surface words
	// the row from one server-derived answer rather than its own.
	CommandScope CommandApprovalScope
}

// BuildApprovalDecisionOptions is the single service-owned decision proposal
// used by interactive and remote approval surfaces.
func BuildApprovalDecisionOptions(actionKind, toolName string, toolInput any) []ApprovalDecisionOption {
	suggestion := BuildToolApprovalSuggestion(toolName, toolInput)
	if suggestion.PermissionToolName == "" {
		return nil
	}

	switch {
	case strings.EqualFold(strings.TrimSpace(actionKind), "request_permissions"):
		// Three answers and no reviewer: approving a permission request is a
		// matter of how long the grant lasts, not of who reviews it (plan 203).
		return []ApprovalDecisionOption{
			{Decision: DecisionGrantForTurn},
			{Decision: DecisionGrantForSession},
			{Decision: DecisionDecline},
		}
	case strings.EqualFold(strings.TrimSpace(actionKind), "apply_patch"):
		return []ApprovalDecisionOption{{Decision: DecisionAccept}, {Decision: DecisionAcceptForSession}, {Decision: DecisionCancel}}
	case strings.HasPrefix(strings.ToLower(strings.TrimSpace(suggestion.PermissionToolName)), "mcp__") && !suggestion.OneShotOnly:
		return []ApprovalDecisionOption{
			{Decision: DecisionAccept}, {Decision: DecisionAcceptForSession},
			{Decision: DecisionAcceptAndRemember}, {Decision: DecisionCancel},
		}
	case strings.EqualFold(CanonicalToolName(suggestion.PermissionToolName), "Bash") && !ToolApprovalHasAdditionalPermissions(toolInput):
		// One proposal decides both rows: the rule set the command is
		// remembered as, and — when that set is a single token prefix — the
		// amendment decision that carries it.
		if rules, ok := ProposedCommandApprovalRules(suggestion); ok {
			scope := CommandApprovalScopeFor(rules)
			remember := ApprovalDecisionOption{
				Decision: DecisionAcceptAndRemember, CommandRules: rules, CommandScope: scope,
			}
			if len(rules) == 1 && len(rules[0].CommandPrefix) > 0 {
				remember = ApprovalDecisionOption{
					Decision:            DecisionAcceptWithExecPolicyAmendment,
					ExecPolicyAmendment: ExecPolicyAmendment(rules[0].CommandPrefix),
					CommandScope:        scope,
				}
			}
			return []ApprovalDecisionOption{{Decision: DecisionAccept}, remember, {Decision: DecisionCancel}}
		}
	case strings.EqualFold(CanonicalToolName(suggestion.PermissionToolName), "WebFetch") &&
		strings.TrimSpace(suggestion.PrefixRuleContent) != "":
		return []ApprovalDecisionOption{
			{Decision: DecisionAccept}, {Decision: DecisionAcceptAndRemember}, {Decision: DecisionCancel},
		}
	case ApprovalFileTool(suggestion.PermissionToolName):
		return []ApprovalDecisionOption{{Decision: DecisionAccept}, {Decision: DecisionAcceptForSession}, {Decision: DecisionCancel}}
	case ApprovalMemoryTool(suggestion.PermissionToolName):
		// A memory call is something Forebrain Harness does alongside the user's request,
		// never the request itself: a note it decided to keep, a lookup it
		// wanted. Refusing one says "not that", not "stop" - so the refusal
		// here is a decline, which hands the model a refusal result and lets
		// the turn carry on, rather than the cancel that tears the turn down
		// and makes the user re-ask for the work they were already getting.
		return []ApprovalDecisionOption{{Decision: DecisionAccept}, {Decision: DecisionDecline}}
	}
	return []ApprovalDecisionOption{{Decision: DecisionAccept}, {Decision: DecisionCancel}}
}

// ApprovalFileTool reports whether a remembered approval for toolName is a file
// capability, which this service scopes to the current session and to the exact
// path in the prompt. Read is included: a read prompt is the same capability
// question as a write prompt, one step less dangerous, and leaving it out was
// the only reason a repeated read had no answer but "yes, this once".
func ApprovalFileTool(toolName string) bool {
	switch CanonicalToolName(toolName) {
	case "Read", "Write", "Edit", "MultiEdit":
		return true
	default:
		return false
	}
}

// ApprovalMemoryTool reports whether toolName is one of Forebrain Harness's memory tools.
// They share a refusal shape: none of them is the work the user asked for, so
// declining one leaves the turn with something to do.
func ApprovalMemoryTool(toolName string) bool {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "memories_add_ad_hoc_note", "memories_list", "memories_read", "memories_search":
		return true
	default:
		return false
	}
}

// MatchedRuleBlocksRemembering reports whether the policy decision that led to
// this prompt makes a persistent allow rule pointless: a deny or ask rule
// matched, so the prompt returns no matter what the user remembers here.
//
// A matched allow rule is the opposite case. The prompt happened because the
// sandbox denied the command, and a rule carrying bypass_sandbox is exactly
// what ends that loop; treating it as a blocker left the same prompt returning
// on every call with no way to answer it.
//
// It is the single condition behind WithoutPersistentCommandChoices and behind
// the server-side guards that reject such an update.
func MatchedRuleBlocksRemembering(decision Decision) bool {
	return decision.Matched != nil && decision.Behavior != BehaviorAllow
}

// WithoutPersistentCommandChoices removes both persistent command choices — the
// prefix amendment and the exact-command rule — for a decision that
// MatchedRuleBlocksRemembering reports on. Offering them there would promise
// something the engine will not honour.
func WithoutPersistentCommandChoices(options []ApprovalDecisionOption) []ApprovalDecisionOption {
	out := make([]ApprovalDecisionOption, 0, len(options))
	for _, option := range options {
		if option.Decision == DecisionAcceptWithExecPolicyAmendment || option.Decision == DecisionAcceptAndRemember {
			continue
		}
		out = append(out, option)
	}
	return out
}

func ToolApprovalHasAdditionalPermissions(toolInput any) bool {
	raw, err := json.Marshal(toolInputMap(toolInput))
	if err != nil {
		return false
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(raw, &payload) != nil {
		return false
	}
	value := payload["additional_permissions"]
	return len(value) > 0 && string(value) != "null"
}

// constrainRequestToFilesystemProfile applies the non-negotiable boundary of
// the selected profile before a platform backend translates paths into its
// native policy. A read-only request can gain write access only by first being
// promoted to the managed profile by the structured-permission layer.
func constrainRequestToFilesystemProfile(req CommandRequest, workDir string) CommandRequest {
	req.Profile = req.Profile.normalized()
	switch req.Profile {
	case ProfileReadOnly:
		req.AdditionalReadablePaths = appendUniquePath(req.AdditionalReadablePaths, workDir)
		req.AdditionalWritablePaths = nil
	case ProfileWorkspaceWrite:
		req.AdditionalWritablePaths = appendUniquePath(req.AdditionalWritablePaths, workDir)
	}
	return req
}
