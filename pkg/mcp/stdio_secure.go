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
	"github.com/joho/godotenv"
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
// ${...} references from.
//
// A global entry is operator-authored configuration: it resolves from the
// process environment, as it always has. A project entry arrives with a
// repository checkout, and a repository must not be able to probe the host
// that cloned it — so its lookup is restricted to ~/.forebrain/.env, a file only
// the operator can write. An unset variable in that file is simply missing;
// the caller reports it like any other unresolved reference.
func envLookupForServer(homeDir string, srv appcfg.MCPServerConfig) func(string) (string, bool) {
	if !IsProjectScope(srv) {
		return os.LookupEnv
	}
	m := projectScopedEnvMap(homeDir)
	return func(name string) (string, bool) {
		v, ok := m[strings.TrimSpace(name)]
		return v, ok
	}
}

// projectScopedEnvMap reads ~/.forebrain/.env. A missing or unreadable file is
// an empty map: no project reference resolves, which fails closed.
func projectScopedEnvMap(homeDir string) map[string]string {
	out := map[string]string{}
	root := strings.TrimSpace(homeDir)
	if root == "" {
		return out
	}
	b, err := os.ReadFile(homepkg.DefaultEnvPath(root))
	if err != nil {
		return out
	}
	m, err := godotenv.Unmarshal(strings.ReplaceAll(string(b), "\r\n", "\n"))
	if err != nil {
		return out
	}
	return m
}

func mcpStdioEnv(homeDir string, srv appcfg.MCPServerConfig) []string {
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
	lookup := envLookupForServer(homeDir, srv)
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
