//go:build windows

package safety

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"golang.org/x/sys/windows"
)

const (
	windowsDisableMaxPrivilege = 0x01
	windowsLUAToken            = 0x04
	windowsWriteRestricted     = 0x08
	windowsFileDeleteChild     = 0x00000040
	windowsDesktopAllAccess    = 0x000F01FF
	windowsProcThreadJobList   = 0x0002000D
)

var (
	windowsAdvapi                    = windows.NewLazySystemDLL("advapi32.dll")
	windowsUser32                    = windows.NewLazySystemDLL("user32.dll")
	windowsCreateRestrictedTokenProc = windowsAdvapi.NewProc("CreateRestrictedToken")
	windowsCreateDesktopProc         = windowsUser32.NewProc("CreateDesktopW")
	windowsCloseDesktopProc          = windowsUser32.NewProc("CloseDesktop")

	windowsCapabilityMu sync.Mutex
)

func windowsSandboxUnavailableReason() string {
	for _, proc := range []*windows.LazyProc{
		windowsCreateRestrictedTokenProc,
		windowsCreateDesktopProc,
		windowsCloseDesktopProc,
	} {
		if err := proc.Find(); err != nil {
			return "windows_sandbox_api_unavailable"
		}
	}
	return ""
}

// runWindowsSandboxCommand runs one command in the restricted-token sandbox.
// There is one Windows sandbox and nothing to configure (owner 2026-10-31, plan
// 206): writes are bounded by the task's directories, reads are not isolated
// because a restricted token cannot hold an exclusive read set, and the network
// is reachable because the token cannot take it away. cfg is the dispatch
// signature's, unused here: no setting on it changes what the sandbox does.
func runWindowsSandboxCommand(ctx context.Context, _ *appcfg.Root, req CommandRequest, workDir string) (CommandResult, error) {
	req = constrainRequestToFilesystemProfile(req, workDir)

	baseToken, err := windowsSandboxBaseToken()
	if err != nil {
		return CommandResult{}, err
	}
	defer baseToken.Close()

	restrictedToken, logonSID, cleanupMarkers, err := prepareWindowsSandboxToken(baseToken, req, workDir)
	if err != nil {
		return CommandResult{}, err
	}
	defer restrictedToken.Close()
	defer cleanupMarkers()

	env := home.SafeSubprocessEnv(nil, home.Options{ExplicitEnv: explicitCommandEnv(req.Env, nil)})
	runCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	return spawnWindowsSandboxProcess(
		runCtx,
		restrictedToken,
		logonSID,
		req,
		workDir,
		env,
	)
}

func windowsSandboxBaseToken() (windows.Token, error) {
	var token windows.Token
	access := uint32(windows.TOKEN_ASSIGN_PRIMARY | windows.TOKEN_DUPLICATE | windows.TOKEN_QUERY |
		windows.TOKEN_ADJUST_DEFAULT | windows.TOKEN_ADJUST_SESSIONID | windows.TOKEN_ADJUST_PRIVILEGES)
	if err := windows.OpenProcessToken(windows.CurrentProcess(), access, &token); err != nil {
		return 0, fmt.Errorf("open current process token: %w", err)
	}
	return token, nil
}

// prepareWindowsSandboxToken builds the restricted token for one command. It
// bounds writes by the task's directories and leaves reads alone: a restricted
// token cannot hold an exclusive read set, so there is nothing to enforce and
// nothing to refuse (plans/150's fail-closed rule is replaced; plan 206).
func prepareWindowsSandboxToken(baseToken windows.Token, req CommandRequest, workDir string) (windows.Token, *windows.SID, func(), error) {
	writeRoots := append([]string(nil), req.AdditionalWritablePaths...)
	if req.Profile.normalized() == ProfileWorkspaceWrite {
		writeRoots = appendUniquePath(writeRoots, workDir)
	}
	writeRoots = normalizeWindowsSandboxPaths(writeRoots)
	deniedWrite := normalizeWindowsSandboxPaths(req.DeniedWritablePaths)

	markers, err := prepareWindowsDenyMarkers(deniedWrite, writeRoots)
	if err != nil {
		return 0, nil, func() {}, err
	}
	cleanup := func() { cleanupWindowsDenyMarkers(markers) }

	writeCapabilities, err := windowsCapabilitySIDs(writeRoots, deniedWrite)
	if err != nil {
		cleanup()
		return 0, nil, func() {}, err
	}
	capabilitySIDs := windowsCapabilitySIDList(writeCapabilities)
	if len(capabilitySIDs) == 0 {
		readonly, readonlyErr := windowsReadonlyCapabilitySID()
		if readonlyErr != nil {
			cleanup()
			return 0, nil, func() {}, readonlyErr
		}
		capabilitySIDs = append(capabilitySIDs, readonly)
	}

	fullAccess := windows.ACCESS_MASK(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE |
		windows.FILE_GENERIC_EXECUTE | windows.DELETE | windowsFileDeleteChild)
	writeAccess := windows.ACCESS_MASK(windows.FILE_GENERIC_WRITE | windows.DELETE | windowsFileDeleteChild)
	for _, capability := range writeCapabilities {
		if err := mergeWindowsPathACL(capability.Root, capability.SID, windows.GRANT_ACCESS, fullAccess); err != nil {
			cleanup()
			return 0, nil, func() {}, fmt.Errorf("grant sandbox write root %s: %w", capability.Root, err)
		}
	}
	for _, path := range deniedWrite {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		for _, capability := range writeCapabilities {
			if !windowsPathWithinAny(path, []string{capability.Root}) {
				continue
			}
			if err := mergeWindowsPathACL(path, capability.SID, windows.DENY_ACCESS, writeAccess); err != nil {
				cleanup()
				return 0, nil, func() {}, fmt.Errorf("deny sandbox write path %s: %w", path, err)
			}
		}
	}

	restricted, err := createWindowsRestrictedToken(baseToken, capabilitySIDs)
	if err != nil {
		cleanup()
		return 0, nil, func() {}, err
	}
	logonSID, err := windowsTokenLogonSID(baseToken)
	if err != nil {
		restricted.Close()
		cleanup()
		return 0, nil, func() {}, err
	}
	return restricted, logonSID, cleanup, nil
}

func createWindowsRestrictedToken(base windows.Token, capabilitySIDs []*windows.SID) (windows.Token, error) {
	logonSID, err := windowsTokenLogonSID(base)
	if err != nil {
		return 0, err
	}
	everyoneSID, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		return 0, fmt.Errorf("create Windows Everyone SID: %w", err)
	}
	entries := make([]windows.SIDAndAttributes, 0, len(capabilitySIDs)+2)
	for _, sid := range capabilitySIDs {
		entries = append(entries, windows.SIDAndAttributes{Sid: sid})
	}
	entries = append(entries, windows.SIDAndAttributes{Sid: logonSID})
	entries = append(entries, windows.SIDAndAttributes{Sid: everyoneSID})
	flags := uintptr(windowsDisableMaxPrivilege | windowsLUAToken | windowsWriteRestricted)
	defaultDACL := append([]*windows.SID{logonSID}, capabilitySIDs...)
	defaultDACL = append(defaultDACL, everyoneSID)
	var token windows.Token
	var entriesPtr uintptr
	if len(entries) > 0 {
		entriesPtr = uintptr(unsafe.Pointer(&entries[0]))
	}
	ok, _, callErr := windowsCreateRestrictedTokenProc.Call(
		uintptr(base),
		flags,
		0, 0,
		0, 0,
		uintptr(len(entries)), entriesPtr,
		uintptr(unsafe.Pointer(&token)),
	)
	if ok == 0 {
		return 0, fmt.Errorf("create restricted Windows token: %w", callErr)
	}
	if err := setWindowsTokenDefaultDACL(token, defaultDACL); err != nil {
		token.Close()
		return 0, err
	}
	if err := enableWindowsTokenPrivilege(token, "SeChangeNotifyPrivilege"); err != nil {
		token.Close()
		return 0, err
	}
	return token, nil
}

type windowsTokenDefaultDACL struct {
	DACL *windows.ACL
}

func setWindowsTokenDefaultDACL(token windows.Token, sids []*windows.SID) error {
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(sids))
	for _, sid := range sids {
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	dacl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("build restricted token default DACL: %w", err)
	}
	info := windowsTokenDefaultDACL{DACL: dacl}
	if err := windows.SetTokenInformation(
		token,
		windows.TokenDefaultDacl,
		(*byte)(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		return fmt.Errorf("set restricted token default DACL: %w", err)
	}
	return nil
}

func enableWindowsTokenPrivilege(token windows.Token, name string) error {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, namePtr, &luid); err != nil {
		return fmt.Errorf("lookup %s: %w", name, err)
	}
	state := windows.Tokenprivileges{PrivilegeCount: 1}
	state.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
	if err := windows.AdjustTokenPrivileges(token, false, &state, 0, nil, nil); err != nil {
		return fmt.Errorf("enable %s: %w", name, err)
	}
	if err := windows.GetLastError(); err != nil {
		return fmt.Errorf("enable %s: %w", name, err)
	}
	return nil
}

func windowsTokenLogonSID(token windows.Token) (*windows.SID, error) {
	groups, err := token.GetTokenGroups()
	if err != nil {
		return nil, fmt.Errorf("read token groups: %w", err)
	}
	for _, group := range groups.AllGroups() {
		if group.Attributes&windows.SE_GROUP_LOGON_ID == windows.SE_GROUP_LOGON_ID {
			copy, err := group.Sid.Copy()
			if err != nil {
				return nil, err
			}
			return copy, nil
		}
	}
	return nil, errors.New("Windows token has no logon SID")
}

func mergeWindowsPathACL(path string, sid *windows.SID, mode windows.ACCESS_MODE, access windows.ACCESS_MASK) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	var oldACL *windows.ACL
	if sd != nil {
		oldACL, _, err = sd.DACL()
		if err != nil && !errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) {
			return err
		}
	}
	inheritance := uint32(windows.NO_INHERITANCE)
	if info.IsDir() {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	entry := windows.EXPLICIT_ACCESS{
		AccessPermissions: access,
		AccessMode:        mode,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, oldACL)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

type windowsDenyMarker struct {
	path    string
	created bool
}

func prepareWindowsDenyMarkers(denied, writeRoots []string) ([]windowsDenyMarker, error) {
	markers := make([]windowsDenyMarker, 0, len(denied))
	for _, path := range denied {
		if _, err := os.Lstat(path); err == nil {
			markers = append(markers, windowsDenyMarker{path: path})
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			cleanupWindowsDenyMarkers(markers)
			return markers, err
		}
		if !windowsPathWithinAny(path, writeRoots) {
			continue
		}
		missing := make([]string, 0, 4)
		current := path
		for {
			if _, err := os.Lstat(current); err == nil {
				break
			} else if !errors.Is(err, os.ErrNotExist) {
				cleanupWindowsDenyMarkers(markers)
				return markers, err
			}
			missing = append(missing, current)
			parent := filepath.Dir(current)
			if parent == current {
				cleanupWindowsDenyMarkers(markers)
				return markers, fmt.Errorf("deny marker has no existing ancestor: %s", path)
			}
			current = parent
		}
		for index := len(missing) - 1; index >= 0; index-- {
			created := missing[index]
			if err := os.Mkdir(created, 0o700); err != nil {
				cleanupWindowsDenyMarkers(markers)
				return markers, fmt.Errorf("create deny marker %s: %w", created, err)
			}
			markers = append(markers, windowsDenyMarker{path: created, created: true})
		}
	}
	return markers, nil
}

func cleanupWindowsDenyMarkers(markers []windowsDenyMarker) {
	for index := len(markers) - 1; index >= 0; index-- {
		if markers[index].created {
			_ = os.Remove(markers[index].path)
		}
	}
}

func windowsPathWithinAny(path string, roots []string) bool {
	path = strings.ToLower(filepath.Clean(path))
	for _, root := range roots {
		root = strings.ToLower(filepath.Clean(root))
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func windowsRequestHasFullDiskRead(readRoots, deniedRead []string, workDir string) bool {
	if len(deniedRead) > 0 {
		return false
	}
	volume := filepath.VolumeName(filepath.Clean(workDir))
	if volume == "" {
		return false
	}
	return windowsPathWithinAny(volume+string(filepath.Separator), readRoots)
}

func normalizeWindowsSandboxPaths(paths []string) []string {
	seen := map[string]string{}
	for _, raw := range paths {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		seen[strings.ToLower(abs)] = abs
	}
	out := make([]string, 0, len(seen))
	for _, path := range seen {
		out = append(out, path)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

// windowsCapabilityState is the persisted map of the capability SIDs granted to
// the write roots the sandbox has seen. Reads carry no capability: a
// restricted token cannot hold an exclusive read set (plan 206).
type windowsCapabilityState struct {
	Readonly string            `json:"readonly"`
	Writable map[string]string `json:"writable"`
}

type windowsPathCapability struct {
	Root string
	SID  *windows.SID
}

func windowsCapabilitySIDs(roots, denied []string) ([]windowsPathCapability, error) {
	windowsCapabilityMu.Lock()
	defer windowsCapabilityMu.Unlock()
	state, path, err := loadWindowsCapabilityState()
	if err != nil {
		return nil, err
	}
	changed := false
	values := state.Writable
	if values == nil {
		values = map[string]string{}
		state.Writable = values
		changed = true
	}
	out := make([]windowsPathCapability, 0, len(roots))
	for _, root := range roots {
		key := windowsCapabilityPolicyKey(root, denied)
		value := values[key]
		if value == "" {
			value, err = randomWindowsCapabilitySID()
			if err != nil {
				return nil, err
			}
			values[key] = value
			changed = true
		}
		sid, sidErr := windows.StringToSid(value)
		if sidErr != nil {
			return nil, fmt.Errorf("invalid stored Windows capability SID: %w", sidErr)
		}
		out = append(out, windowsPathCapability{Root: root, SID: sid})
	}
	if changed {
		if err := persistWindowsCapabilityState(path, state); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func windowsCapabilityPolicyKey(root string, denied []string) string {
	root = strings.ToLower(filepath.Clean(root))
	relevant := make([]string, 0, len(denied))
	for _, path := range denied {
		path = strings.ToLower(filepath.Clean(path))
		if windowsPathWithinAny(path, []string{root}) {
			relevant = append(relevant, path)
		}
	}
	if len(relevant) == 0 {
		return root
	}
	sort.Strings(relevant)
	digest := sha256.Sum256([]byte(strings.Join(relevant, "\x00")))
	return fmt.Sprintf("%s#deny-%x", root, digest[:16])
}

func windowsCapabilitySIDList(groups ...[]windowsPathCapability) []*windows.SID {
	count := 0
	for _, group := range groups {
		count += len(group)
	}
	out := make([]*windows.SID, 0, count)
	for _, group := range groups {
		for _, capability := range group {
			out = append(out, capability.SID)
		}
	}
	return out
}

func windowsReadonlyCapabilitySID() (*windows.SID, error) {
	windowsCapabilityMu.Lock()
	defer windowsCapabilityMu.Unlock()
	state, path, err := loadWindowsCapabilityState()
	if err != nil {
		return nil, err
	}
	if state.Readonly == "" {
		state.Readonly, err = randomWindowsCapabilitySID()
		if err != nil {
			return nil, err
		}
		if err := persistWindowsCapabilityState(path, state); err != nil {
			return nil, err
		}
	}
	sid, err := windows.StringToSid(state.Readonly)
	if err != nil {
		return nil, fmt.Errorf("invalid stored Windows readonly SID: %w", err)
	}
	return sid, nil
}

func loadWindowsCapabilityState() (windowsCapabilityState, string, error) {
	home := strings.TrimSpace(os.Getenv("FOREBRAIN_HOME"))
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return windowsCapabilityState{}, "", err
		}
		home = filepath.Join(userHome, ".forebrain")
	}
	path := filepath.Join(home, "state", "windows_sandbox_capabilities.json")
	state := windowsCapabilityState{Writable: map[string]string{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, path, nil
	}
	if err != nil {
		return state, path, err
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, path, fmt.Errorf("parse Windows sandbox capabilities: %w", err)
	}
	if state.Writable == nil {
		state.Writable = map[string]string{}
	}
	return state, path, nil
}

func persistWindowsCapabilityState(path string, state windowsCapabilityState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "windows-sandbox-capabilities-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(tmpPath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return err
	}
	tmpPath = ""
	return nil
}

func randomWindowsCapabilitySID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"S-1-5-21-%d-%d-%d-%d",
		binary.LittleEndian.Uint32(raw[0:4]),
		binary.LittleEndian.Uint32(raw[4:8]),
		binary.LittleEndian.Uint32(raw[8:12]),
		binary.LittleEndian.Uint32(raw[12:16]),
	), nil
}

type windowsPrivateDesktop struct {
	handle windows.Handle
	name   string
}

// prepareWindowsPrivateDesktop always creates a private desktop: the desktop a
// sandboxed command runs on is no longer a setting (owner 2026-10-31, plan
// 206), and a private one keeps a stray window of the command out of the user's
// interactive desktop.
func prepareWindowsPrivateDesktop(logonSID *windows.SID) (*windowsPrivateDesktop, *uint16, error) {
	desktop := &windowsPrivateDesktop{}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, nil, err
	}
	desktopName := fmt.Sprintf("ForebrainSandboxDesktop-%x", random)
	desktopNamePtr, err := windows.UTF16PtrFromString(desktopName)
	if err != nil {
		return nil, nil, err
	}
	handle, _, callErr := windowsCreateDesktopProc.Call(
		uintptr(unsafe.Pointer(desktopNamePtr)),
		0, 0, 0,
		windowsDesktopAllAccess,
		0,
	)
	if handle == 0 {
		return nil, nil, fmt.Errorf("create private Windows desktop: %w", callErr)
	}
	desktop.handle = windows.Handle(handle)
	desktop.name = desktopName
	if err := grantWindowsObjectAccess(desktop.handle, windows.SE_WINDOW_OBJECT, logonSID, windowsDesktopAllAccess); err != nil {
		desktop.Close()
		return nil, nil, fmt.Errorf("grant private desktop access: %w", err)
	}
	startupName, err := windows.UTF16PtrFromString("Winsta0\\" + desktopName)
	if err != nil {
		desktop.Close()
		return nil, nil, err
	}
	return desktop, startupName, nil
}

func (desktop *windowsPrivateDesktop) Close() {
	if desktop == nil {
		return
	}
	if desktop.handle != 0 {
		windowsCloseDesktopProc.Call(uintptr(desktop.handle))
		desktop.handle = 0
	}
}

func grantWindowsObjectAccess(handle windows.Handle, objectType windows.SE_OBJECT_TYPE, sid *windows.SID, access uint32) error {
	sd, err := windows.GetSecurityInfo(handle, objectType, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	var oldACL *windows.ACL
	if sd != nil {
		oldACL, _, err = sd.DACL()
		if err != nil && !errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) {
			return err
		}
	}
	entry := windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.ACCESS_MASK(access),
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, oldACL)
	runtime.KeepAlive(sd)
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(handle, objectType, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func spawnWindowsSandboxProcess(
	ctx context.Context,
	token windows.Token,
	logonSID *windows.SID,
	req CommandRequest,
	workDir string,
	env []string,
) (CommandResult, error) {
	shell := hostShellCommand(ctx, req.Command, "")
	appName, err := windows.UTF16PtrFromString(shell.Path)
	if err != nil {
		return CommandResult{}, err
	}
	commandLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(shell.Args))
	if err != nil {
		return CommandResult{}, err
	}
	currentDir, err := windows.UTF16PtrFromString(workDir)
	if err != nil {
		return CommandResult{}, err
	}
	envBlock, err := windowsEnvironmentBlock(env)
	if err != nil {
		return CommandResult{}, err
	}

	desktop, desktopName, err := prepareWindowsPrivateDesktop(logonSID)
	if err != nil {
		return CommandResult{}, err
	}
	if desktop != nil {
		defer desktop.Close()
	}

	stdoutRead, stdoutWrite, err := windowsSandboxPipe(false)
	if err != nil {
		return CommandResult{}, err
	}
	defer func() {
		if stdoutRead != 0 {
			_ = windows.CloseHandle(stdoutRead)
		}
		if stdoutWrite != 0 {
			_ = windows.CloseHandle(stdoutWrite)
		}
	}()
	stderrRead, stderrWrite, err := windowsSandboxPipe(false)
	if err != nil {
		return CommandResult{}, err
	}
	defer func() {
		if stderrRead != 0 {
			_ = windows.CloseHandle(stderrRead)
		}
		if stderrWrite != 0 {
			_ = windows.CloseHandle(stderrWrite)
		}
	}()
	stdinRead, stdinWrite, err := windowsSandboxPipe(true)
	if err != nil {
		return CommandResult{}, err
	}
	defer func() {
		if stdinRead != 0 {
			_ = windows.CloseHandle(stdinRead)
		}
	}()
	_ = windows.CloseHandle(stdinWrite)

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return CommandResult{}, fmt.Errorf("create Windows sandbox job: %w", err)
	}
	defer windows.CloseHandle(job)
	var jobInfo windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	jobInfo.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&jobInfo)),
		uint32(unsafe.Sizeof(jobInfo)),
	); err != nil {
		return CommandResult{}, fmt.Errorf("configure Windows sandbox job: %w", err)
	}

	attributeList, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return CommandResult{}, fmt.Errorf("create Windows process attribute list: %w", err)
	}
	defer attributeList.Delete()
	inheritedHandles := []windows.Handle{stdinRead, stdoutWrite, stderrWrite}
	if err := attributeList.Update(
		windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
		unsafe.Pointer(&inheritedHandles[0]),
		uintptr(len(inheritedHandles))*unsafe.Sizeof(inheritedHandles[0]),
	); err != nil {
		return CommandResult{}, fmt.Errorf("restrict Windows inherited handles: %w", err)
	}
	if err := attributeList.Update(
		windowsProcThreadJobList,
		unsafe.Pointer(&job),
		unsafe.Sizeof(job),
	); err != nil {
		return CommandResult{}, fmt.Errorf("attach Windows process job attribute: %w", err)
	}
	startup := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{
		Cb:        uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
		Desktop:   desktopName,
		Flags:     windows.STARTF_USESTDHANDLES,
		StdInput:  stdinRead,
		StdOutput: stdoutWrite,
		StdErr:    stderrWrite,
	}, ProcThreadAttributeList: attributeList.List()}

	var process windows.ProcessInformation
	creationFlags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_UNICODE_ENVIRONMENT |
		windows.CREATE_NO_WINDOW | windows.EXTENDED_STARTUPINFO_PRESENT)
	if err := windows.CreateProcessAsUser(
		token,
		appName,
		commandLine,
		nil,
		nil,
		true,
		creationFlags,
		&envBlock[0],
		currentDir,
		&startup.StartupInfo,
		&process,
	); err != nil {
		return CommandResult{ExitCode: 1}, fmt.Errorf("create Windows sandbox process: %w", err)
	}
	defer windows.CloseHandle(process.Process)
	defer windows.CloseHandle(process.Thread)
	if _, err := windows.ResumeThread(process.Thread); err != nil {
		windows.TerminateJobObject(job, 1)
		return CommandResult{ExitCode: 1}, fmt.Errorf("resume Windows sandbox process: %w", err)
	}

	// Only the child retains the inheritable write handles from this point.
	_ = windows.CloseHandle(stdoutWrite)
	stdoutWrite = 0
	_ = windows.CloseHandle(stderrWrite)
	stderrWrite = 0
	_ = windows.CloseHandle(stdinRead)
	stdinRead = 0

	stdoutFile := os.NewFile(uintptr(stdoutRead), "windows-sandbox-stdout")
	stderrFile := os.NewFile(uintptr(stderrRead), "windows-sandbox-stderr")
	stdoutRead = 0
	stderrRead = 0
	defer stdoutFile.Close()
	defer stderrFile.Close()
	capture := newCommandCapture(req)
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		_, _ = io.Copy(capture.writer(OutputStreamStdout), stdoutFile)
	}()
	go func() {
		defer readers.Done()
		_, _ = io.Copy(capture.writer(OutputStreamStderr), stderrFile)
	}()

	waitDone := make(chan error, 1)
	go func() {
		_, waitErr := windows.WaitForSingleObject(process.Process, windows.INFINITE)
		waitDone <- waitErr
	}()
	var waitErr error
	var stopErr error
	select {
	case waitErr = <-waitDone:
	case <-ctx.Done():
		select {
		case waitErr = <-waitDone:
		default:
			stopErr = ctx.Err()
			_ = windows.TerminateJobObject(job, 1)
			waitErr = <-waitDone
		}
	}
	// Closing the job after the main process exits also terminates descendants
	// that inherited the capture pipes, guaranteeing the readers reach EOF.
	_ = windows.TerminateJobObject(job, 0)
	readers.Wait()

	res := capture.result()
	if waitErr != nil {
		res.ExitCode = 1
		return res, waitErr
	}
	if errors.Is(stopErr, context.DeadlineExceeded) {
		res.ExitCode = 124
		return res, &TimeoutError{Result: res}
	}
	if errors.Is(stopErr, context.Canceled) {
		res.ExitCode = 1
		return res, context.Canceled
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(process.Process, &exitCode); err != nil {
		res.ExitCode = 1
		return res, err
	}
	res.ExitCode = int(exitCode)
	return res, nil
}

func windowsSandboxPipe(childReads bool) (windows.Handle, windows.Handle, error) {
	attributes := windows.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		InheritHandle: 1,
	}
	var read, write windows.Handle
	if err := windows.CreatePipe(&read, &write, &attributes, 0); err != nil {
		return 0, 0, err
	}
	parentHandle := read
	if childReads {
		parentHandle = write
	}
	if err := windows.SetHandleInformation(parentHandle, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		windows.CloseHandle(read)
		windows.CloseHandle(write)
		return 0, 0, err
	}
	return read, write, nil
}

func windowsEnvironmentBlock(env []string) ([]uint16, error) {
	values := map[string]string{}
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		values[strings.ToUpper(key)] = item
	}
	items := make([]string, 0, len(values))
	for _, item := range values {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToUpper(items[i]) < strings.ToUpper(items[j]) })
	block := make([]uint16, 0)
	for _, item := range items {
		encoded, err := windows.UTF16FromString(item)
		if err != nil {
			return nil, err
		}
		block = append(block, encoded...)
	}
	// Each item already carries one NUL terminator. The additional NUL closes
	// the environment block, including the empty-environment case.
	if len(block) == 0 {
		block = append(block, 0)
	}
	block = append(block, 0)
	return block, nil
}

// hostShellCommand builds the host shell invocation for a command string on
// Windows. The optional preference selects the shell:
//
//	"gitbash"            — Git Bash / MSYS2 sh (POSIX sh syntax)
//	"powershell"         — Windows PowerShell / PowerShell Core
//	"cmd"                — cmd.exe
//	"auto" / "" (default)— prefer Git Bash, then PowerShell, then cmd.exe
//
// Cancellation is NOT built in (no exec.CommandContext). The caller must
// set up process-tree cancellation via killProcessGroup and check ctx.Err()
// after Wait.
//
// forebrain shell commands are authored in POSIX sh syntax (identical to Linux and
// macOS), so Git Bash is preferred when present for cross-platform parity.
// PowerShell and cmd are offered for environments without Git Bash or when the
// user explicitly selects them.
func hostShellCommand(ctx context.Context, command string, pref string) *exec.Cmd {
	switch strings.ToLower(strings.TrimSpace(pref)) {
	case "gitbash", "bash", "sh":
		if sh := posixShellPath(); sh != "" {
			return exec.Command(sh, "-lc", command)
		}
		// Fall through to auto ordering when the requested shell is missing.
	case "powershell", "pwsh":
		if ps := powershellPath(); ps != "" {
			return exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", command)
		}
	case "cmd":
		return exec.Command("cmd.exe", "/c", command)
	}

	// auto / unset / requested-but-missing: prefer Git Bash, then PowerShell,
	// then cmd.exe.
	if sh := posixShellPath(); sh != "" {
		return exec.Command(sh, "-lc", command)
	}
	if ps := powershellPath(); ps != "" {
		return exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", command)
	}
	return exec.Command("cmd.exe", "/c", command)
}

var (
	posixShellOnce sync.Once
	posixShellLoc  string
	powershellOnce sync.Once
	powershellLoc  string
)

// posixShellPath locates a POSIX sh on Windows, caching the result. It honors
// FOREBRAIN_SHELL, then PATH, then common Git-for-Windows install locations.
func posixShellPath() string {
	posixShellOnce.Do(func() {
		if custom := os.Getenv("FOREBRAIN_SHELL"); custom != "" {
			if _, err := os.Stat(custom); err == nil {
				posixShellLoc = custom
				return
			}
		}
		for _, name := range []string{"sh.exe", "bash.exe", "sh", "bash"} {
			if p, err := exec.LookPath(name); err == nil {
				posixShellLoc = p
				return
			}
		}
		for _, base := range []string{
			os.Getenv("ProgramFiles"),
			os.Getenv("ProgramFiles(x86)"),
			`C:\Program Files`,
			`C:\Program Files (x86)`,
		} {
			if base == "" {
				continue
			}
			for _, rel := range []string{
				filepath.Join("Git", "bin", "sh.exe"),
				filepath.Join("Git", "usr", "bin", "sh.exe"),
			} {
				c := filepath.Join(base, rel)
				if _, err := os.Stat(c); err == nil {
					posixShellLoc = c
					return
				}
			}
		}
	})
	return posixShellLoc
}

// powershellPath locates PowerShell (Core "pwsh" preferred, then Windows
// PowerShell), caching the result.
func powershellPath() string {
	powershellOnce.Do(func() {
		for _, name := range []string{"pwsh.exe", "pwsh", "powershell.exe", "powershell"} {
			if p, err := exec.LookPath(name); err == nil {
				powershellLoc = p
				return
			}
		}
	})
	return powershellLoc
}

func replacePermissionFile(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// setupProcessGroup is a no-op on Windows. Process-group creation is not
// used; tree termination is handled by killProcessGroup via taskkill.
func setupProcessGroup(*exec.Cmd) {}

// killProcessGroup terminates the process tree of cmd on Windows using
// taskkill /T. Falls back to Process.Kill if taskkill is unavailable.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return
	}
	// taskkill /T terminates the entire process tree rooted at this PID.
	_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	// Direct Process.Kill as a fallback for the immediate process.
	_ = cmd.Process.Kill()
}

// waitWithTimeout calls cmd.Wait() but returns after d if Wait has not
// completed. This prevents descendant processes that inherited our stdout/
// stderr pipes from causing Wait to block forever after the process group
// has been killed.
func waitWithTimeout(cmd *exec.Cmd, d time.Duration) error {
	errc := make(chan error, 1)
	go func() {
		errc <- cmd.Wait()
	}()
	select {
	case err := <-errc:
		return err
	case <-time.After(d):
		return nil
	}
}

// DefaultWaitTimeout is the maximum time to wait for a killed process
// group's pipes to drain before the caller stops waiting.
const DefaultWaitTimeout = 3 * time.Second
