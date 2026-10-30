package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

var overlaySlugRe = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

func oauthOverlayDir(home string) string {
	return filepath.Join(strings.TrimSpace(home), "state", "mcp-oauth")
}

// overlayProjectDir is the per-project namespace inside the overlay directory.
func overlayProjectDir(home, projectKey string) string {
	return filepath.Join(oauthOverlayDir(home), "projects", OverlaySlug(projectKey))
}

// OverlaySlug renders a namespace or server name the way the overlay tree
// names its directories and files: every character outside [a-zA-Z0-9_.-]
// becomes "_", and an empty result reads as "server". The slug is lossy —
// two names can share one — so the legacy import (plan 187) only adopts a
// file whose slug pair matches exactly one configured server.
func OverlaySlug(name string) string {
	s := overlaySlugRe.ReplaceAllString(strings.TrimSpace(name), "_")
	if s == "" {
		return "server"
	}
	return s
}

func oauthOverlayPath(home, projectKey, serverName string) string {
	s := OverlaySlug(serverName)
	if pk := OverlaySlug(projectKey); pk != "server" && pk != "" {
		return filepath.Join(overlayProjectDir(home, pk), s+".json")
	}
	return filepath.Join(oauthOverlayDir(home), s+".json")
}

// OverlayProjectKey returns the credential namespace a server's tokens belong
// to: the project key for project-scope entries, "agent:<id>" for an entry one
// primary agent owns, empty (the shared global store) otherwise. Every
// load/save call site derives its namespace from the server entry itself, so
// an entry can never be handed another's token by sharing its name — two
// primary agents that configure the same MCP server each hold their own
// credential.
func OverlayProjectKey(srv appcfg.MCPServerConfig) string {
	switch {
	case IsProjectScope(srv):
		return strings.TrimSpace(srv.ProjectKey)
	case IsAgentScope(srv):
		if id := strings.TrimSpace(srv.AgentKey); id != "" {
			return "agent:" + id
		}
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

// LoadOAuthOverlay reads one server's stored credential overlay (plan 188
// W12): through the installed store when one is — the state database — and
// the file tree under home otherwise. projectKey selects the namespace: empty
// means the global store, a project key means that project's. An absent
// overlay is "no overlay", not an error.
func LoadOAuthOverlay(home, projectKey, serverName string) (appcfg.MCPOAuthConfig, bool, error) {
	var zero appcfg.MCPOAuthConfig
	if strings.TrimSpace(serverName) == "" {
		return zero, false, nil
	}
	raw, err := LoadStoredOAuth(context.Background(), home, projectKey, serverName)
	if err != nil {
		return zero, false, err
	}
	if raw == nil {
		return zero, false, nil
	}
	var o appcfg.MCPOAuthConfig
	if err := json.Unmarshal(raw, &o); err != nil {
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
	cur, _, _ := LoadOAuthOverlay(home, projectKey, serverName)
	merged := mergeOAuthFields(cur, delta)
	b, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	return SaveStoredOAuth(context.Background(), home, projectKey, serverName, b)
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

// OAuthOverlayStore is the port behind the overlay tree: it hands back one
// server's stored credential bytes and takes new ones, in the JSON the tree's
// files hold. The composition root installs a database-backed store after
// the state database opens (plan 187); with none installed the overlays live
// in the files under <home>/state/mcp-oauth.
//
// This package sits below the state database, so the store arrives installed
// by the composition root, the same way llm's transport pieces do.
type OAuthOverlayStore interface {
	// LoadOAuth returns the server's stored bytes, or nil when no overlay is
	// stored for it.
	LoadOAuth(ctx context.Context, projectKey, serverName string) (json.RawMessage, error)
	SaveOAuth(ctx context.Context, projectKey, serverName string, raw json.RawMessage) error
}

var oauthOverlayStore atomic.Pointer[OAuthOverlayStore]

// SetOAuthOverlayStore installs the backend the store-based overlay accesses
// go through; nil restores the file default. Process-wide: one process has
// one overlay store.
func SetOAuthOverlayStore(store OAuthOverlayStore) {
	if store == nil {
		oauthOverlayStore.Store(nil)
		return
	}
	oauthOverlayStore.Store(&store)
}

// LoadStoredOAuth reads one server's stored overlay through the installed
// store — the file tree under home when none is. Absent is (nil, nil): "no
// overlay stored", not an error.
func LoadStoredOAuth(ctx context.Context, home, projectKey, serverName string) (json.RawMessage, error) {
	if s := oauthOverlayStore.Load(); s != nil {
		return (*s).LoadOAuth(ctx, projectKey, serverName)
	}
	if strings.TrimSpace(home) == "" || strings.TrimSpace(serverName) == "" {
		return nil, nil
	}
	b, err := os.ReadFile(oauthOverlayPath(home, projectKey, serverName))
	if err != nil && os.IsNotExist(err) {
		return nil, nil
	}
	return b, err
}

// SaveStoredOAuth writes one server's overlay through the installed store —
// the file tree under home when none is, at 0600 like SaveOAuthOverlayMerge
// writes it.
func SaveStoredOAuth(ctx context.Context, home, projectKey, serverName string, raw json.RawMessage) error {
	if s := oauthOverlayStore.Load(); s != nil {
		return (*s).SaveOAuth(ctx, projectKey, serverName, raw)
	}
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
	return os.WriteFile(oauthOverlayPath(home, projectKey, serverName), raw, 0o600)
}
