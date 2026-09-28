package mcp

import (
	"context"
	"fmt"
	"strings"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"golang.org/x/oauth2"
)

func ExchangeOAuthAuthCode(ctx context.Context, o appcfg.MCPOAuthConfig, code, codeVerifier string) (*oauth2.Token, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, fmt.Errorf("code required")
	}
	redir := strings.TrimSpace(o.RedirectURL)
	if redir == "" {
		return nil, fmt.Errorf("redirect_url required")
	}
	tu := strings.TrimSpace(o.TokenURL)
	if tu == "" {
		return nil, fmt.Errorf("token_url required")
	}
	cfg := &oauth2.Config{
		ClientID:     strings.TrimSpace(o.ClientID),
		ClientSecret: strings.TrimSpace(o.ClientSecret),
		RedirectURL:  redir,
		Scopes:       o.Scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:  strings.TrimSpace(o.AuthorizationURL),
			TokenURL: tu,
		},
	}
	return cfg.Exchange(ctx, code, oauth2.VerifierOption(strings.TrimSpace(codeVerifier)))
}
