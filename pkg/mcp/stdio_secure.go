package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	homepkg "github.com/forebrain-harness/forebrain-harness/pkg/home"
)

func validateMCPStdioCommand(cmd string) error {
	c := strings.TrimSpace(cmd)
	if c == "" {
		return fmt.Errorf("empty command")
	}
	if strings.ContainsAny(c, "\n\r;&|") {
		return fmt.Errorf("invalid command characters")
	}
	if strings.Contains(c, "${") || strings.Contains(c, "`") {
		return fmt.Errorf("invalid command")
	}
	return nil
}

func verifyMCPCommandSHA256(cmd string, wantHex string) error {
	wantHex = strings.TrimSpace(strings.ToLower(wantHex))
	if wantHex == "" {
		return nil
	}
	p := strings.TrimSpace(cmd)
	if !filepath.IsAbs(p) {
		return fmt.Errorf("mcp command must be absolute path when expected_command_sha256 is set")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("mcp command sha256 read: %w", err)
	}
	sum := sha256.Sum256(b)
	got := hex.EncodeToString(sum[:])
	if got != wantHex {
		return fmt.Errorf("mcp command sha256 mismatch")
	}
	return nil
}

// envLookupForServer picks the variable source one server entry may resolve
// ${...} references from. vars is the operator's variables table, read once by
// the caller.
//
// A project entry arrives with a repository checkout, and a repository must not
// be able to probe the host that cloned it — so its lookup is restricted to
// that table, values only the operator can write. A global entry is
// operator-authored configuration and resolves from the table first, then from
// the process environment, as a container-supplied value has always worked.
func envLookupForServer(vars map[string]string, srv appcfg.MCPServerConfig) func(string) (string, bool) {
	fromTable := func(name string) (string, bool) {
		v, ok := vars[strings.TrimSpace(name)]
		return v, ok
	}
	if IsProjectScope(srv) {
		return fromTable
	}
	return func(name string) (string, bool) {
		if v, ok := fromTable(name); ok {
			return v, true
		}
		return os.LookupEnv(name)
	}
}

func mcpStdioEnv(vars map[string]string, srv appcfg.MCPServerConfig) []string {
	inherit := srv.InheritParentEnv != nil && *srv.InheritParentEnv
	// A project entry never inherits the parent environment, whatever its
	// file says: inherit_parent_env in a checkout is repository-controlled
	// text, and the parent environment is the host's.
	if IsProjectScope(srv) {
		inherit = false
	}
	base := homepkg.SafeSubprocessEnv(nil, homepkg.Options{})
	if inherit {
		base = homepkg.SafeSubprocessEnv(base, homepkg.Options{})
	}
	lookup := envLookupForServer(vars, srv)
	explicit := make(map[string]string, len(srv.Env))
	for k, v := range srv.Env {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		expanded, _ := ExpandEnvVarsInString(v, lookup)
		explicit[k] = expanded
	}
	return homepkg.SafeSubprocessEnv(base, homepkg.Options{ExplicitEnv: explicit})
}
