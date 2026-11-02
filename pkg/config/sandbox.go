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
	return nil
}

// DangerFullAccessEnabled reports whether commands run uncontained. It is the
// derived sandbox mode and nothing else now (plan 203): a permission profile
// used to outrank it, and profiles are gone, so the sandbox mode the preset
// derives is the single answer.
func (r *Root) DangerFullAccessEnabled() bool {
	if r == nil {
		return false
	}
	return r.SandboxMode == SandboxModeDangerFullAccess
}

func normalizeSandboxConfig(r *Root) {
	if r == nil {
		return
	}
	r.SandboxWorkspaceWrite.WritableRoots = normalizePathList(r.SandboxWorkspaceWrite.WritableRoots)
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
