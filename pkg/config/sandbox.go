// Sandbox configuration: validation and patching.
package config

import (
	"fmt"
	"strings"
)

func validateSandboxConfig(r *Root) error {
	if r == nil {
		return nil
	}
	for _, path := range r.SandboxWorkspaceWrite.WritableRoots {
		if err := validateSandboxPath(path, "sandbox_workspace_write.writable_roots"); err != nil {
			return err
		}
	}
	switch r.SandboxWorkspaceWrite.EffectiveGoCacheMode() {
	case GoCacheModeIsolated, GoCacheModeShared, GoCacheModeDisabled:
	default:
		return fmt.Errorf("sandbox_workspace_write.go_cache_mode must be isolated, shared, or disabled")
	}
	return nil
}

func normalizeSandboxConfig(r *Root) {
	if r == nil {
		return
	}
	r.SandboxWorkspaceWrite.WritableRoots = normalizePathList(r.SandboxWorkspaceWrite.WritableRoots)
	r.SandboxWorkspaceWrite.GoCacheMode = strings.ToLower(strings.TrimSpace(r.SandboxWorkspaceWrite.GoCacheMode))
	r.DefaultPermissions = strings.TrimSpace(r.DefaultPermissions)
	switch r.DefaultPermissions {
	case PermissionProfileReadOnly:
		r.SandboxMode = SandboxModeReadOnly
	case PermissionProfileWorkspace:
		r.SandboxMode = SandboxModeWorkspaceWrite
	case PermissionProfileDangerFullAccess:
		r.SandboxMode = SandboxModeDangerFullAccess
	case "":
	default:
		// Named profiles use the managed sandbox. Their compiled filesystem
		// entries decide which paths, if any, are writable.
		r.SandboxMode = SandboxModeWorkspaceWrite
	}
}

func normalizePathList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v == "" {
			out = append(out, "")
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func validateSandboxPath(path string, field string) error {
	v := strings.TrimSpace(path)
	if v == "" {
		return fmt.Errorf("%s contains empty path", field)
	}
	if strings.Contains(v, "\x00") {
		return fmt.Errorf("%s contains NUL byte", field)
	}
	return nil
}
