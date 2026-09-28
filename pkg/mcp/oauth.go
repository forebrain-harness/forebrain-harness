package mcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

func OAuthConfiguredPublic(o appcfg.MCPOAuthConfig) bool {
	return oauthConfigured(o)
}

func oauthConfigured(o appcfg.MCPOAuthConfig) bool {
	if strings.EqualFold(strings.TrimSpace(o.Mode), "none") {
		return false
	}
	if strings.TrimSpace(o.Mode) != "" {
		return true
	}
	if strings.TrimSpace(o.AccessToken) != "" {
		return true
	}
	if strings.TrimSpace(o.RefreshToken) != "" {
		return true
	}
	if strings.TrimSpace(o.ClientID) != "" && strings.TrimSpace(o.TokenURL) != "" {
		return true
	}
	if strings.TrimSpace(o.AuthorizationURL) != "" && strings.TrimSpace(o.ClientID) != "" {
		return true
	}
	return false
}

func oauthTokenSource(ctx context.Context, o appcfg.MCPOAuthConfig) (src oauth2.TokenSource, static bool, err error) {
	if !oauthConfigured(o) {
		return nil, false, nil
	}
	mode := strings.ToLower(strings.TrimSpace(o.Mode))
	if mode == "" || mode == "none" {
		switch {
		case strings.TrimSpace(o.AccessToken) != "":
			mode = "static"
		case strings.TrimSpace(o.RefreshToken) != "":
			mode = "refresh"
		case strings.TrimSpace(o.ClientID) != "" && strings.TrimSpace(o.TokenURL) != "":
			mode = "client_credentials"
		default:
			return nil, false, fmt.Errorf("oauth: set mode or access_token, or client_id+token_url, or refresh_token")
		}
	}
	switch mode {
	case "static", "bearer":
		at := strings.TrimSpace(o.AccessToken)
		if at == "" {
			return nil, false, fmt.Errorf("oauth static mode requires access_token")
		}
		tt := strings.TrimSpace(o.TokenType)
		if tt == "" {
			tt = "Bearer"
		}
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: at, TokenType: tt}), true, nil
	case "client_credentials":
		if strings.TrimSpace(o.TokenURL) == "" || strings.TrimSpace(o.ClientID) == "" {
			return nil, false, fmt.Errorf("oauth client_credentials requires token_url and client_id")
		}
		cfg := clientcredentials.Config{
			ClientID:       o.ClientID,
			ClientSecret:   strings.TrimSpace(o.ClientSecret),
			TokenURL:       strings.TrimSpace(o.TokenURL),
			Scopes:         o.Scopes,
			EndpointParams: nil,
		}
		return cfg.TokenSource(ctx), false, nil
	case "refresh":
		if strings.TrimSpace(o.TokenURL) == "" || strings.TrimSpace(o.ClientID) == "" || strings.TrimSpace(o.RefreshToken) == "" {
			return nil, false, fmt.Errorf("oauth refresh mode requires token_url, client_id, refresh_token")
		}
		conf := &oauth2.Config{
			ClientID:     o.ClientID,
			ClientSecret: strings.TrimSpace(o.ClientSecret),
			Endpoint: oauth2.Endpoint{
				TokenURL: strings.TrimSpace(o.TokenURL),
			},
			Scopes: o.Scopes,
		}
		tok := &oauth2.Token{RefreshToken: strings.TrimSpace(o.RefreshToken)}
		if strings.TrimSpace(o.AccessToken) != "" {
			tok.AccessToken = strings.TrimSpace(o.AccessToken)
		}
		return conf.TokenSource(ctx, tok), false, nil
	default:
		return nil, false, fmt.Errorf("oauth unknown mode %q", o.Mode)
	}
}

func oauthTokenSourceForServer(ctx context.Context, home, projectKey, serverName string, o appcfg.MCPOAuthConfig) (oauth2.TokenSource, bool, error) {
	src, static, err := oauthTokenSource(ctx, o)
	if err != nil || src == nil || static {
		return src, static, err
	}
	return &persistingOAuthTokenSource{
		src:                  src,
		home:                 strings.TrimSpace(home),
		projectKey:           strings.TrimSpace(projectKey),
		serverName:           strings.TrimSpace(serverName),
		fallbackRefreshToken: strings.TrimSpace(o.RefreshToken),
	}, false, nil
}

type persistingOAuthTokenSource struct {
	src                  oauth2.TokenSource
	home                 string
	projectKey           string
	serverName           string
	fallbackRefreshToken string
}

func (s *persistingOAuthTokenSource) Token() (*oauth2.Token, error) {
	tok, err := s.src.Token()
	if err != nil || tok == nil {
		return tok, err
	}
	if s.home == "" || s.serverName == "" {
		return tok, nil
	}
	refresh := strings.TrimSpace(tok.RefreshToken)
	if refresh == "" {
		refresh = s.fallbackRefreshToken
	}
	mode := "static"
	if refresh != "" {
		mode = "refresh"
	}
	_ = SaveOAuthOverlayMerge(s.home, s.projectKey, s.serverName, appcfg.MCPOAuthConfig{
		Mode:         mode,
		AccessToken:  tok.AccessToken,
		RefreshToken: refresh,
		TokenType:    tok.TokenType,
	})
	return tok, nil
}

func streamableOAuthHandlerForServer(ctx context.Context, home, projectKey, serverName string, o appcfg.MCPOAuthConfig) (auth.OAuthHandler, error) {
	src, static, err := oauthTokenSourceForServer(ctx, home, projectKey, serverName, o)
	if err != nil || src == nil {
		return nil, err
	}
	if static {
		return staticBearerHandler{src: src}, nil
	}
	return renewingOAuthHandler{src: src}, nil
}

type staticBearerHandler struct {
	src oauth2.TokenSource
}

func (h staticBearerHandler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	return h.src, nil
}

func (h staticBearerHandler) Authorize(ctx context.Context, req *http.Request, resp *http.Response) error {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	return fmt.Errorf("oauth static token was rejected by the server")
}

type renewingOAuthHandler struct {
	src oauth2.TokenSource
}

func (h renewingOAuthHandler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	return h.src, nil
}

func (h renewingOAuthHandler) Authorize(ctx context.Context, req *http.Request, resp *http.Response) error {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	_, err := h.src.Token()
	return err
}

func sseHTTPClientForServer(ctx context.Context, home, projectKey, serverName string, headers map[string]string, o appcfg.MCPOAuthConfig) (*http.Client, error) {
	if !oauthConfigured(o) && len(headers) == 0 {
		return nil, nil
	}
	src, _, err := oauthTokenSourceForServer(ctx, home, projectKey, serverName, o)
	if err != nil {
		return nil, err
	}
	var base http.RoundTripper = http.DefaultTransport
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		base = dt.Clone()
	}
	rt := base
	if src != nil {
		rt = &oauth2.Transport{Source: src, Base: rt}
	}
	if len(headers) > 0 {
		rt = &headerRoundTripper{next: rt, headers: headers}
	}
	return &http.Client{Transport: rt}, nil
}
