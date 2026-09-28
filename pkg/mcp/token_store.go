package mcp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

var overlaySlugRe = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

func oauthOverlayDir(home string) string {
	return filepath.Join(strings.TrimSpace(home), "state", "mcp-oauth")
}

// overlayProjectDir is the per-project namespace inside the overlay directory.
func overlayProjectDir(home, projectKey string) string {
	return filepath.Join(oauthOverlayDir(home), "projects", overlaySlug(projectKey))
}

func overlaySlug(name string) string {
	s := overlaySlugRe.ReplaceAllString(strings.TrimSpace(name), "_")
	if s == "" {
		return "server"
	}
	return s
}

func oauthOverlayPath(home, projectKey, serverName string) string {
	s := overlaySlug(serverName)
	if pk := overlaySlug(projectKey); pk != "server" && pk != "" {
		return filepath.Join(overlayProjectDir(home, pk), s+".json")
	}
	return filepath.Join(oauthOverlayDir(home), s+".json")
}

// OverlayProjectKey returns the credential namespace a server's tokens belong
// to: the project key for project-scope entries, empty (the shared global
// store) otherwise. Every load/save call site derives its namespace from the
// server entry itself, so a project entry can never be handed a global
// server's token by sharing its name.
func OverlayProjectKey(srv appcfg.MCPServerConfig) string {
	if IsProjectScope(srv) {
		return strings.TrimSpace(srv.ProjectKey)
	}
	return ""
}

func mergeOAuthFields(base, over appcfg.MCPOAuthConfig) appcfg.MCPOAuthConfig {
	out := base
	if strings.TrimSpace(over.Mode) != "" {
		out.Mode = over.Mode
	}
	if strings.TrimSpace(over.AccessToken) != "" {
		out.AccessToken = over.AccessToken
	}
	if strings.TrimSpace(over.TokenType) != "" {
		out.TokenType = over.TokenType
	}
	if strings.TrimSpace(over.ClientID) != "" {
		out.ClientID = over.ClientID
	}
	if strings.TrimSpace(over.ClientSecret) != "" {
		out.ClientSecret = over.ClientSecret
	}
	if strings.TrimSpace(over.TokenURL) != "" {
		out.TokenURL = over.TokenURL
	}
	if strings.TrimSpace(over.AuthorizationURL) != "" {
		out.AuthorizationURL = over.AuthorizationURL
	}
	if strings.TrimSpace(over.RedirectURL) != "" {
		out.RedirectURL = over.RedirectURL
	}
	if strings.TrimSpace(over.RefreshToken) != "" {
		out.RefreshToken = over.RefreshToken
	}
	if len(over.Scopes) > 0 {
		out.Scopes = append([]string(nil), over.Scopes...)
	}
	return out
}

// LoadOAuthOverlay reads one server's stored credential overlay. projectKey
// selects the namespace: empty means the global store, a project key means
// that project's. An absent file is "no overlay", not an error.
func LoadOAuthOverlay(home, projectKey, serverName string) (appcfg.MCPOAuthConfig, bool, error) {
	var zero appcfg.MCPOAuthConfig
	if strings.TrimSpace(home) == "" || strings.TrimSpace(serverName) == "" {
		return zero, false, nil
	}
	p := oauthOverlayPath(home, projectKey, serverName)
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return zero, false, nil
		}
		return zero, false, err
	}
	var o appcfg.MCPOAuthConfig
	if err := json.Unmarshal(b, &o); err != nil {
		return zero, false, err
	}
	return o, true, nil
}

// SaveOAuthOverlayMerge merges delta into one server's stored overlay in the
// namespace projectKey selects. Empty projectKey is the global store.
func SaveOAuthOverlayMerge(home, projectKey, serverName string, delta appcfg.MCPOAuthConfig) error {
	if strings.TrimSpace(home) == "" || strings.TrimSpace(serverName) == "" {
		return errors.New("invalid home or server name")
	}
	projectKey = strings.TrimSpace(projectKey)
	dir := oauthOverlayDir(home)
	if projectKey != "" {
		dir = overlayProjectDir(home, projectKey)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	cur, _, _ := LoadOAuthOverlay(home, projectKey, serverName)
	merged := mergeOAuthFields(cur, delta)
	b, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	p := oauthOverlayPath(home, projectKey, serverName)
	return os.WriteFile(p, b, 0o600)
}

// MergeMCPServerOAuth merges the stored credential overlay onto srv. The
// namespace comes from srv's own scope stamp, so a project entry is always
// read from its project's store and can never pick up a global server's
// token by sharing its name.
func MergeMCPServerOAuth(home string, srv appcfg.MCPServerConfig) appcfg.MCPServerConfig {
	if strings.TrimSpace(home) == "" {
		return srv
	}
	nm := strings.TrimSpace(srv.Name)
	if nm == "" {
		return srv
	}
	ov, ok, err := LoadOAuthOverlay(home, OverlayProjectKey(srv), nm)
	if err != nil || !ok {
		return srv
	}
	out := srv
	out.OAuth = mergeOAuthFields(srv.OAuth, ov)
	return out
}
